package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestResolveDetailsUseTheSameLookupAndMatchingCommitTitle(t *testing.T) {
	for _, kind := range []string{"commit", "branch", "tag"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			const title = "fix: <checkout> & preserve carts"
			client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				path := "/repository/commits/" + commitA
				body := fmt.Sprintf(`{"id":%q,"title":%q}`, strings.ToUpper(commitA), title)
				if kind != "commit" {
					collection := "branches"
					if kind == "tag" {
						collection = "tags"
					}
					path = "/repository/" + collection + "/release%2Fv1"
					body = fmt.Sprintf(`{"title":"wrong top-level title","message":"wrong tag annotation","target":%q,"commit":{"id":%q,"title":%q}}`, commitC, strings.ToUpper(commitA), title)
				}
				if !strings.HasSuffix(r.URL.EscapedPath(), path) {
					t.Errorf("unexpected lookup path: %s want suffix %s", r.URL.EscapedPath(), path)
				}
				return releaseAPIResponse(200, nil, body), nil
			})
			var details CommitDetails
			var sha string
			var err error
			switch kind {
			case "commit":
				details, err = client.ResolveCommitDetails(context.Background(), "group/service", commitA)
			case "branch":
				details, err = client.ResolveBranchDetails(context.Background(), "group/service", "release/v1")
			case "tag":
				details, err = client.ResolveTagDetails(context.Background(), "group/service", "release/v1")
			}
			if err != nil || details != (CommitDetails{SHA: commitA, Title: title}) || calls != 1 {
				t.Fatalf("details=%+v error=%v calls=%d", details, err, calls)
			}
			switch kind {
			case "commit":
				sha, err = client.ResolveCommit(context.Background(), "group/service", commitA)
			case "branch":
				sha, err = client.ResolveBranch(context.Background(), "group/service", "release/v1")
			case "tag":
				sha, err = client.ResolveTag(context.Background(), "group/service", "release/v1")
			}
			if err != nil || sha != commitA || calls != 2 {
				t.Fatalf("legacy wrapper changed requests/results: SHA=%q error=%v calls=%d", sha, err, calls)
			}
		})
	}
}

func TestCommitDetailsOptionalTitleNeverChangesResolution(t *testing.T) {
	for _, title := range []string{"", "null", "123", "true", "{}", "[]"} {
		for _, kind := range []string{"commit", "branch", "tag"} {
			calls := 0
			client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				body := fmt.Sprintf(`{"id":%q}`, commitA)
				if title != "" {
					body = fmt.Sprintf(`{"id":%q,"title":%s}`, commitA, title)
				}
				if kind != "commit" {
					body = `{"message":"must not substitute annotation","commit":` + body + `}`
				}
				return releaseAPIResponse(200, nil, body), nil
			})
			var details CommitDetails
			var err error
			switch kind {
			case "commit":
				details, err = client.ResolveCommitDetails(context.Background(), "group/service", commitA)
			case "branch":
				details, err = client.ResolveBranchDetails(context.Background(), "group/service", "main")
			case "tag":
				details, err = client.ResolveTagDetails(context.Background(), "group/service", "v1")
			}
			if err != nil || details.SHA != commitA || details.Title != "" || calls != 1 {
				t.Fatalf("optional title %q changed %s resolution: %+v error=%v calls=%d", title, kind, details, err, calls)
			}
		}
	}
}

func TestResolveCommitDetailsPrefixUsesExistingFullSHAVerification(t *testing.T) {
	const prefix = "f63850b"
	full := prefix + strings.Repeat("a", 33)
	var requests []string
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/"+prefix):
			return releaseAPIResponse(404, nil, ""), nil
		case strings.HasSuffix(r.URL.Path, "/commits/"+full):
			return releaseAPIResponse(200, nil, fmt.Sprintf(`{"id":%q,"title":"verified title"}`, full)), nil
		case strings.HasSuffix(r.URL.Path, "/commits"):
			if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("page") != "1" {
				t.Errorf("prefix search query changed: %v", r.URL.Query())
			}
			return releaseAPIResponse(200, nil, fmt.Sprintf(`[{"id":%q,"title":"listing title"}]`, full)), nil
		default:
			t.Fatalf("extra metadata request: %s", r.URL)
			return nil, errors.New("unexpected request")
		}
	})
	details, err := client.ResolveCommitDetails(context.Background(), "group/service", prefix)
	if err != nil || details != (CommitDetails{SHA: full, Title: "verified title"}) || len(requests) != 3 {
		t.Fatalf("prefix details=%+v error=%v requests=%v", details, err, requests)
	}
}

func TestResolveDetailsDoNotAttachMetadataToInvalidOrDeniedCommit(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(status, nil, `{"id":"invalid","title":"do not associate this title"}`), nil
		})
		details, err := client.ResolveCommitDetails(context.Background(), "group/service", commitA)
		if err == nil || details != (CommitDetails{}) {
			t.Fatalf("invalid lookup retained metadata: %+v, %v", details, err)
		}
		if status != 200 {
			var response *HTTPError
			if !errors.As(err, &response) || response.StatusCode != status {
				t.Fatalf("HTTP cause changed: %v", err)
			}
		}
	}
}

func TestDeleteTagDetailsRetainPreMutationMetadataWithoutExtraRequests(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		lookup, deletion, check int
		dry                     bool
		want                    string
		wantError               bool
		methods                 []string
	}{
		{"deleted", 200, 204, 0, false, "deleted", false, []string{"GET", "DELETE"}},
		{"planned", 200, 0, 0, true, "planned", false, []string{"GET"}},
		{"denied deletion", 200, 403, 0, false, "", true, []string{"GET", "DELETE"}},
		{"server failure", 200, 500, 0, false, "", true, []string{"GET", "DELETE"}},
		{"raced deletion", 200, 404, 200, false, "skipped", false, []string{"GET", "DELETE", "GET"}},
		{"unconfirmed raced deletion", 200, 404, 403, false, "", true, []string{"GET", "DELETE", "GET"}},
		{"initially absent", 404, 0, 200, false, "skipped", false, []string{"GET", "GET"}},
		{"unverified absence", 404, 0, 403, false, "", true, []string{"GET", "GET"}},
		{"denied lookup", 403, 0, 0, false, "", true, []string{"GET"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var methods []string
			client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
				methods = append(methods, r.Method)
				if strings.HasSuffix(r.URL.EscapedPath(), "/projects/group%2Fservice") {
					if tc.check == 0 {
						t.Fatal("unexpected project metadata lookup")
					}
					return releaseAPIResponse(tc.check, nil, `{"id":123,"title":"not a commit title"}`), nil
				}
				if r.Method == http.MethodDelete {
					if len(methods) != 2 {
						t.Error("deletion happened before its metadata lookup")
					}
					return releaseAPIResponse(tc.deletion, nil, ""), nil
				}
				return releaseAPIResponse(tc.lookup, nil, fmt.Sprintf(`{"message":"tag annotation","target":%q,"commit":{"id":%q,"title":"pre-delete title"}}`, commitC, commitA)), nil
			})
			status, details, err := client.DeleteTagDetails(context.Background(), "group/service", "v1", tc.dry)
			if status != tc.want || (err != nil) != tc.wantError || !reflect.DeepEqual(methods, tc.methods) {
				t.Fatalf("status=%q details=%+v error=%v requests=%v want=%v", status, details, err, methods, tc.methods)
			}
			wantDetails := CommitDetails{}
			if tc.lookup == 200 {
				wantDetails = CommitDetails{SHA: commitA, Title: "pre-delete title"}
			}
			if details != wantDetails {
				t.Fatalf("pre-delete metadata changed or attached to absent tag: %+v want=%+v", details, wantDetails)
			}
		})
	}
}

func TestDeleteTagDetailsPreserveMetadataOnAmbiguousTransportFailure(t *testing.T) {
	cause := errors.New("private diagnostic test-token")
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method == http.MethodDelete {
			return nil, cause
		}
		return releaseAPIResponse(200, nil, fmt.Sprintf(`{"commit":{"id":%q,"title":"pinned title"}}`, commitA)), nil
	})
	status, details, err := client.DeleteTagDetails(context.Background(), "group/service", "v1", false)
	if status != "" || details != (CommitDetails{SHA: commitA, Title: "pinned title"}) || !errors.Is(err, cause) || calls != 2 || !strings.Contains(err.Error(), "result could not be confirmed") || strings.Contains(err.Error(), "private diagnostic") {
		t.Fatalf("ambiguous deletion lost metadata or cause: %q %+v %v calls=%d", status, details, err, calls)
	}
}
