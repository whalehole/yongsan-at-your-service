package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeleteTagOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, want                 string
		missing, dry, inaccessible bool
		deleteStatus               int
	}{
		{name: "deleted", want: "deleted", deleteStatus: 204},
		{name: "missing tag", missing: true, want: "skipped"},
		{name: "missing project", missing: true, inaccessible: true},
		{name: "dry run", dry: true, want: "planned"},
		{name: "protected tag denied", deleteStatus: 403},
		{name: "raced deletion", deleteStatus: 404, want: "skipped"},
		{name: "server failure is not retried", deleteStatus: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/projects/group/project") {
					if tc.inaccessible {
						w.WriteHeader(404)
					} else {
						fmt.Fprint(w, `{"id":123}`)
					}
					return
				}
				if !strings.HasSuffix(r.URL.EscapedPath(), "/tags/release%2Fv1") {
					t.Errorf("path=%s", r.URL.EscapedPath())
				}
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(tc.deleteStatus)
					return
				}
				if tc.missing {
					w.WriteHeader(404)
				} else {
					writeReference(w, commitA)
				}
			})
			status, _, err := client.DeleteTag(context.Background(), "group/project", "release/v1", tc.dry)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil || status != tc.want {
				t.Fatalf("status=%q err=%v", status, err)
			}
			wantDeletes := int32(1)
			if tc.missing || tc.dry {
				wantDeletes = 0
			}
			if deletes.Load() != wantDeletes {
				t.Fatalf("DELETE count=%d want=%d", deletes.Load(), wantDeletes)
			}
		})
	}
}

func TestDeleteTagVerificationFailureExplainsLookupVersusDeletion(t *testing.T) {
	for _, sent := range []bool{false, true} {
		for _, statusCode := range []int{401, 403, 404, 500} {
			t.Run(fmt.Sprintf("delete-sent=%v/project-status=%d", sent, statusCode), func(t *testing.T) {
				var deletes, lookups, projects atomic.Int32
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/projects/group/project"):
						projects.Add(1)
						w.WriteHeader(statusCode)
					case r.Method == http.MethodDelete:
						deletes.Add(1)
						w.WriteHeader(http.StatusNotFound)
					default:
						lookups.Add(1)
						if sent {
							writeReference(w, commitA)
						} else {
							w.WriteHeader(http.StatusNotFound)
						}
					}
				})
				status, _, err := client.DeleteTag(context.Background(), "group/project", "release/v1", false)
				if status != "" || err == nil || lookups.Load() != 1 || projects.Load() != 1 {
					t.Fatalf("status=%q error=%v tagGET=%d projectGET=%d", status, err, lookups.Load(), projects.Load())
				}
				var response *HTTPError
				if !errors.As(err, &response) || response.StatusCode != statusCode {
					t.Fatalf("verification HTTP error lost: %v", err)
				}
				for _, want := range []string{"release/v1", "group/project", fmt.Sprintf("HTTP %d", statusCode)} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("message %q lacks %q", err, want)
					}
				}
				if sent {
					if deletes.Load() != 1 || !strings.Contains(err.Error(), "A deletion request was sent") || !strings.Contains(err.Error(), "result cannot be confirmed") || strings.Contains(err.Error(), "deletion was not attempted") {
						t.Fatalf("misleading post-deletion verification error: %v; DELETE=%d", err, deletes.Load())
					}
				} else {
					if deletes.Load() != 0 || !strings.Contains(err.Error(), "Cannot confirm that the tag is absent; deletion was not attempted") {
						t.Fatalf("misleading tag-lookup verification error: %v; DELETE=%d", err, deletes.Load())
					}
					if statusCode == 403 {
						want := `Tag "release/v1" was not found by the tag lookup, but GitLab denied access while verifying repository "group/project" (HTTP 403). Cannot confirm that the tag is absent; deletion was not attempted. Check repository access and token permissions.`
						if err.Error() != want {
							t.Fatalf("message=%q want=%q", err.Error(), want)
						}
					}
				}
				if statusCode == 404 && !strings.Contains(err.Error(), "missing or inaccessible") {
					t.Errorf("repository 404 treated as confirmed missing: %v", err)
				}
			})
		}
	}
}

func TestDeleteTagDeniedMessageIdentifiesTheDeniedPhase(t *testing.T) {
	for _, denyLookup := range []bool{true, false} {
		t.Run(fmt.Sprintf("lookup-denied=%v", denyLookup), func(t *testing.T) {
			var deletes, projects atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/projects/group/project") {
					projects.Add(1)
				}
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if denyLookup {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				writeReference(w, commitA)
			})
			status, _, err := client.DeleteTag(context.Background(), "group/project", "release/v1", false)
			if status != "" || err == nil || projects.Load() != 0 {
				t.Fatalf("status=%q error=%v projectGET=%d", status, err, projects.Load())
			}
			for _, want := range []string{"release/v1", "group/project", "HTTP 403", "denied"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q lacks %q", err, want)
				}
			}
			if denyLookup {
				if deletes.Load() != 0 || !strings.Contains(err.Error(), "look up tag") || !strings.Contains(err.Error(), "Deletion was not attempted") {
					t.Fatalf("lookup denial implied deletion: %v; DELETE=%d", err, deletes.Load())
				}
			} else if deletes.Load() != 1 || !strings.Contains(err.Error(), "delete tag") || !strings.Contains(err.Error(), "protected-tag rules that may apply") || strings.Contains(err.Error(), "was not attempted") {
				t.Fatalf("deletion denial has an incorrect cause or phase: %v; DELETE=%d", err, deletes.Load())
			}
		})
	}
}

func TestMissingTagSkipsOnlyAfterValidRepositoryVerification(t *testing.T) {
	for _, body := range []string{`{"id":123}`, `{"id":0}`, `invalid private response`} {
		t.Run(body, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/projects/group/project") {
					fmt.Fprint(w, body)
					return
				}
				if r.Method != http.MethodGet {
					t.Error("missing tag triggered a mutating request")
				}
				w.WriteHeader(http.StatusNotFound)
			})
			status, _, err := client.DeleteTag(context.Background(), "group/project", "v1", false)
			if body == `{"id":123}` {
				if status != "skipped" || err != nil {
					t.Fatalf("verified absent tag was not skipped: %q, %v", status, err)
				}
			} else {
				if status != "" || err == nil || !strings.Contains(err.Error(), "Cannot confirm that the tag is absent") || !strings.Contains(err.Error(), "deletion was not attempted") || !strings.Contains(err.Error(), "invalid or incomplete response while verifying repository") {
					t.Fatalf("unverified absence was treated as success or misdiagnosed: %q, %v", status, err)
				}
				if strings.Contains(err.Error(), "private response") {
					t.Fatalf("repository response content exposed: %v", err)
				}
			}
		})
	}
}

func TestDeleteTagTransportFailureDoesNotClaimNoServerChange(t *testing.T) {
	cause := errors.New("private diagnostic test-token")
	client, err := New("https://gitlab.example.com", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	var deletes atomic.Int32
	client.HTTPClient = &http.Client{Transport: errorResponseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			return nil, cause
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"commit":{"id":%q}}`, commitA)))}, nil
	})}
	status, commit, err := client.DeleteTag(context.Background(), "group/project", "v1", false)
	if status != "" || commit != commitA || !errors.Is(err, cause) || deletes.Load() != 1 {
		t.Fatalf("status=%q commit=%q error=%v DELETE=%d", status, commit, err, deletes.Load())
	}
	for _, want := range []string{"delete tag", "group/project", "v1", "result could not be confirmed", "Check the tag in GitLab before retrying"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "private diagnostic") || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "was not attempted") {
		t.Fatalf("unsafe or confidential message: %v", err)
	}
}

func TestDeleteTagTimeoutAfterSendingRequestIsUnconfirmed(t *testing.T) {
	client, err := New("https://gitlab.example.com", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	var deletes atomic.Int32
	client.HTTPClient = &http.Client{Transport: errorResponseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"commit":{"id":%q}}`, commitA)))}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = client.DeleteTag(ctx, "group/project", "v1", false)
	if !errors.Is(err, context.DeadlineExceeded) || deletes.Load() != 1 || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "result could not be confirmed") || strings.Contains(err.Error(), "was not attempted") {
		t.Fatalf("unsafe timeout message: %v; DELETE=%d", err, deletes.Load())
	}
}
