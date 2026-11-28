package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func groupAPIClient(t *testing.T, group string, handler func(*http.Request) (*http.Response, error)) *Client {
	t.Helper()
	return releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("unexpected request or authentication: %s %s", r.Method, r.URL)
		}
		if r.URL.EscapedPath() == "/gitlab/api/v4/groups/"+group {
			if query := r.URL.Query(); query.Get("with_projects") != "false" || len(query) != 1 {
				t.Errorf("canonical group query=%v", query)
			}
			return releaseAPIResponse(200, nil, `{"id":42,"full_path":"company/team"}`), nil
		}
		if r.URL.EscapedPath() != "/gitlab/api/v4/groups/42/projects" {
			t.Errorf("unexpected project inventory URL: %s", r.URL)
		}
		query := r.URL.Query()
		for key, want := range map[string]string{"per_page": "100", "include_subgroups": "false", "with_shared": "false", "order_by": "id", "sort": "asc"} {
			if query.Get(key) != want {
				t.Errorf("inventory query %s=%q want %q", key, query.Get(key), want)
			}
		}
		if len(query) != 6 {
			t.Errorf("unexpected inventory query=%v", query)
		}
		return handler(r)
	})
}

func TestListGroupProjectsCanonicalGroupPaginationOwnershipAndStableSort(t *testing.T) {
	calls := 0
	client := groupAPIClient(t, "Company%2FTeam", func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("page") != strconv.Itoa(calls) {
			t.Errorf("unexpected inventory page: %s", r.URL)
		}
		header := make(http.Header)
		header.Set("X-Page", strconv.Itoa(calls))
		header.Set("X-Total", "5")
		header.Set("X-Total-Pages", "2")
		if calls == 1 {
			header.Set("X-Next-Page", "2")
			return releaseAPIResponse(200, header, `[
				{"id":7,"path_with_namespace":"company/team/zeta","web_url":{"unused":"metadata"},"namespace":{"id":42}},
				{"id":8,"path_with_namespace":"company/team/subgroup/nested","namespace":{"id":43}},
				{"id":9,"path_with_namespace":"other/shared","namespace":{"id":99}}
			]`), nil
		}
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, `[
			{"id":7,"path_with_namespace":"company/team/zeta","namespace":{"id":42}},
			{"id":10,"path_with_namespace":"company/team/alpha"}
		]`), nil
	})
	projects, err := client.ListGroupProjects(context.Background(), "Company/Team")
	want := []Project{{ID: 10, PathWithNamespace: "company/team/alpha"}, {ID: 7, PathWithNamespace: "company/team/zeta"}}
	if err != nil || calls != 2 || !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects=%+v error=%v list calls=%d", projects, err, calls)
	}
}

func TestListGroupProjectsNumericGroupAndHeaderlessFullPage(t *testing.T) {
	calls := 0
	client := groupAPIClient(t, "42", func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("page") != strconv.Itoa(calls) {
			t.Errorf("unexpected page: %s", r.URL)
		}
		projects := []Project{}
		if calls == 1 {
			for i := 1; i <= 100; i++ {
				projects = append(projects, Project{ID: int64(i), PathWithNamespace: fmt.Sprintf("company/team/service-%03d", i)})
			}
		} else {
			projects = append(projects, Project{ID: 101, PathWithNamespace: "company/team/service-101"})
		}
		body, err := json.Marshal(projects)
		if err != nil {
			t.Fatal(err)
		}
		return releaseAPIResponse(200, nil, string(body)), nil
	})
	projects, err := client.ListGroupProjects(context.Background(), "42")
	if err != nil || calls != 2 || len(projects) != 101 || projects[100].PathWithNamespace != "company/team/service-101" {
		t.Fatalf("projects=%d error=%v calls=%d", len(projects), err, calls)
	}
}

func TestListGroupProjectsEmptyInventoryIsSuccessful(t *testing.T) {
	client := groupAPIClient(t, "company%2Fteam", func(*http.Request) (*http.Response, error) {
		return releaseAPIResponse(200, nil, `[]`), nil
	})
	projects, err := client.ListGroupProjects(context.Background(), "company/team")
	if err != nil || projects == nil || len(projects) != 0 {
		t.Fatalf("empty inventory=%v error=%v", projects, err)
	}
}

func TestListGroupProjectsRejectsMalformedInventoriesWithoutPartialSuccess(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[null]`, `[{}]`, `[{"id":1,"path_with_namespace":"service"}]`,
		`[{"id":0,"path_with_namespace":"company/team/service"}]`,
		`[{"id":1.5,"path_with_namespace":"company/team/service"}]`,
		`[{"id":"1","path_with_namespace":"company/team/service"}]`,
		`[{"id":1,"path_with_namespace":"company/team/../service"}]`,
		`[{"id":1,"path_with_namespace":"company/team/service","namespace":{"id":0}}]`,
		`[{"id":1,"path_with_namespace":"company/team/subgroup/service","namespace":{"id":42}}]`,
		`[{"id":1,"path_with_namespace":"company/team/service"},{"id":1,"path_with_namespace":"company/team/renamed"}]`,
		`[{"id":1,"path_with_namespace":"company/team/service"},{"id":2,"path_with_namespace":"company/team/service"}]`,
		`[] {"private":"test-token"}`, `PRIVATE BODY test-token`,
	} {
		t.Run(body, func(t *testing.T) {
			client := groupAPIClient(t, "company%2Fteam", func(*http.Request) (*http.Response, error) {
				return releaseAPIResponse(200, nil, body), nil
			})
			projects, err := client.ListGroupProjects(context.Background(), "company/team")
			if err == nil || projects != nil {
				t.Fatalf("accepted malformed inventory: projects=%v error=%v", projects, err)
			}
			if !strings.Contains(err.Error(), "company/team") || !strings.Contains(err.Error(), "invalid or incomplete response") || strings.Contains(err.Error(), "PRIVATE BODY") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("unhelpful or unsafe error: %v", err)
			}
		})
	}
}

func TestListGroupProjectsLaterFailureDoesNotReturnPartialInventory(t *testing.T) {
	for _, failure := range []string{"forbidden", "malformed", "pagination"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			client := groupAPIClient(t, "company%2Fteam", func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return releaseAPIResponse(200, http.Header{"X-Next-Page": {"2"}}, `[{"id":1,"path_with_namespace":"company/team/one"}]`), nil
				}
				switch failure {
				case "forbidden":
					return releaseAPIResponse(403, nil, "PRIVATE BODY test-token"), nil
				case "pagination":
					return releaseAPIResponse(200, http.Header{"X-Next-Page": {"4"}}, `[]`), nil
				default:
					return releaseAPIResponse(200, nil, `null`), nil
				}
			})
			projects, err := client.ListGroupProjects(context.Background(), "company/team")
			if err == nil || projects != nil || calls != 2 {
				t.Fatalf("partial inventory=%v error=%v calls=%d", projects, err, calls)
			}
			if failure == "forbidden" {
				var response *HTTPError
				if !errors.As(err, &response) || response.StatusCode != 403 {
					t.Fatalf("lost HTTP error: %v", err)
				}
			}
		})
	}
}

func TestListGroupProjectsCanonicalLookupErrorsAndCancellation(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"id":0,"full_path":"company/team"}`, `{"id":42,"full_path":""}`} {
		calls := 0
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			return releaseAPIResponse(200, nil, body), nil
		})
		projects, err := client.ListGroupProjects(context.Background(), "company/team")
		if err == nil || projects != nil || calls != 1 {
			t.Fatalf("accepted invalid canonical group %s: projects=%v error=%v calls=%d", body, projects, err, calls)
		}
	}
	for _, status := range []int{401, 403, 404} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(status, nil, "PRIVATE BODY test-token"), nil
		})
		projects, err := client.ListGroupProjects(context.Background(), "company/team")
		var response *HTTPError
		if projects != nil || !errors.As(err, &response) || response.StatusCode != status || !strings.Contains(err.Error(), strconv.Itoa(status)) {
			t.Fatalf("lookup status %d: projects=%v error=%v", status, projects, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not be called")
	})
	projects, err := client.ListGroupProjects(ctx, "company/team")
	if projects != nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled inventory=%v error=%v calls=%d", projects, err, calls)
	}
}

func TestFindTagUsesOneEncodedMetadataLookup(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/gitlab/api/v4/projects/company%2Fteam%2Fservice/repository/tags/release%2Fv1" || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("unexpected tag lookup: %s %s", r.Method, r.URL)
		}
		return releaseAPIResponse(200, nil, fmt.Sprintf(`{"commit":{"id":%q,"title":"Release checkout"}}`, commitA)), nil
	})
	commit, found, err := client.FindTag(context.Background(), "company/team/service", "release/v1")
	if err != nil || !found || commit != (CommitDetails{SHA: commitA, Title: "Release checkout"}) || calls != 1 {
		t.Fatalf("commit=%+v found=%t error=%v calls=%d", commit, found, err, calls)
	}
}

func TestFindTagConfirmsAbsenceWithRepositoryLookup(t *testing.T) {
	var paths []string
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("read-only lookup attempted %s", r.Method)
		}
		paths = append(paths, r.URL.EscapedPath())
		if strings.Contains(r.URL.Path, "/repository/tags/") {
			return releaseAPIResponse(404, nil, ""), nil
		}
		if strings.HasSuffix(r.URL.Path, "/repository/tags") {
			if query := r.URL.Query(); query.Get("per_page") != "1" || query.Get("page") != "1" || len(query) != 2 {
				t.Errorf("unexpected tag-access query: %v", query)
			}
			return releaseAPIResponse(200, nil, `[]`), nil
		}
		return releaseAPIResponse(200, nil, `{"id":7}`), nil
	})
	commit, found, err := client.FindTag(context.Background(), "company/team/service", "release/v1")
	want := []string{"/gitlab/api/v4/projects/company%2Fteam%2Fservice/repository/tags/release%2Fv1", "/gitlab/api/v4/projects/company%2Fteam%2Fservice", "/gitlab/api/v4/projects/company%2Fteam%2Fservice/repository/tags"}
	if err != nil || found || commit != (CommitDetails{}) || !reflect.DeepEqual(paths, want) {
		t.Fatalf("commit=%+v found=%t error=%v requests=%v", commit, found, err, paths)
	}
}

func TestFindTagVerificationFailureIsUnknownRatherThanAbsent(t *testing.T) {
	for _, verification := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 403, "PRIVATE BODY test-token"},
		{"missing or inaccessible repository", 404, ""},
		{"invalid repository ID", 200, `{"id":0}`},
		{"malformed response", 200, `PRIVATE BODY test-token`},
	} {
		t.Run(verification.name, func(t *testing.T) {
			calls := 0
			client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatalf("read-only lookup attempted %s", r.Method)
				}
				if calls == 1 {
					return releaseAPIResponse(404, nil, ""), nil
				}
				return releaseAPIResponse(verification.status, nil, verification.body), nil
			})
			commit, found, err := client.FindTag(context.Background(), "company/team/service", "release/v1")
			if err == nil || found || commit != (CommitDetails{}) || calls != 2 {
				t.Fatalf("commit=%+v found=%t error=%v calls=%d", commit, found, err, calls)
			}
			for _, text := range []string{"release/v1", "company/team/service", "returned HTTP 404", "Cannot confirm whether the tag is absent"} {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("verification error %q lacks %q", err, text)
				}
			}
			for _, text := range []string{"delet", "PRIVATE BODY", "test-token"} {
				if strings.Contains(err.Error(), text) {
					t.Errorf("unsafe or unrelated verification error: %v", err)
				}
			}
			if verification.status != 200 {
				var response *HTTPError
				if !errors.As(err, &response) || response.StatusCode != verification.status {
					t.Fatalf("lost verification status: %v", err)
				}
			}
		})
	}
}

func TestFindTagVisibleProjectStillRequiresTagReadAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"tag read denied", 403, "PRIVATE BODY test-token"},
		{"repository hidden", 404, ""},
		{"null listing", 200, `null`},
		{"object listing", 200, `{}`},
		{"invalid row", 200, `[null]`},
		{"missing name", 200, `[{}]`},
		{"invalid name", 200, `[{"name":"bad..ref"}]`},
		{"oversized listing", 200, `[{"name":"v2"},{"name":"v3"}]`},
		{"matching missing commit", 200, `[{"name":"release/v1"}]`},
		{"matching invalid commit", 200, `[{"name":"release/v1","commit":{"id":"bad"}}]`},
		{"malformed body", 200, `PRIVATE BODY test-token`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatalf("read-only lookup attempted %s", r.Method)
				}
				switch calls {
				case 1:
					return releaseAPIResponse(404, nil, ""), nil
				case 2:
					return releaseAPIResponse(200, nil, `{"id":7}`), nil
				default:
					return releaseAPIResponse(tc.status, nil, tc.body), nil
				}
			})
			commit, found, err := client.FindTag(context.Background(), "company/team/service", "release/v1")
			if err == nil || found || commit != (CommitDetails{}) || calls != 3 {
				t.Fatalf("commit=%+v found=%t error=%v calls=%d", commit, found, err, calls)
			}
			for _, want := range []string{"verify tag access", "release/v1", "company/team/service", "Cannot confirm whether the tag is absent"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			for _, forbidden := range []string{"delet", "PRIVATE BODY", "test-token"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("unsafe or irrelevant error: %v", err)
				}
			}
			if tc.status != 200 {
				var response *HTTPError
				if !errors.As(err, &response) || response.StatusCode != tc.status {
					t.Fatalf("lost tag-access HTTP status: %v", err)
				}
			}
		})
	}
}

func TestFindTagAccessCheckHandlesTagCreatedBetweenRequests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		found bool
	}{
		{"matching new tag", fmt.Sprintf(`[{"name":"release/v1","commit":{"id":%q,"title":"Raced release"}}]`, strings.ToUpper(commitA)), true},
		{"different tag needs no commit metadata", `[{"name":"release/v2","commit":"not used"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return releaseAPIResponse(404, nil, ""), nil
				}
				if calls == 2 {
					return releaseAPIResponse(200, nil, `{"id":7}`), nil
				}
				return releaseAPIResponse(200, nil, tc.body), nil
			})
			commit, found, err := client.FindTag(context.Background(), "company/team/service", "release/v1")
			want := CommitDetails{}
			if tc.found {
				want = CommitDetails{SHA: commitA, Title: "Raced release"}
			}
			if err != nil || found != tc.found || commit != want || calls != 3 {
				t.Fatalf("commit=%+v found=%t error=%v calls=%d", commit, found, err, calls)
			}
		})
	}
}

func TestFindTagDoesNotVerifyRepositoryAfterOtherLookupFailures(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		calls := 0
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			return releaseAPIResponse(status, nil, "PRIVATE BODY test-token"), nil
		})
		commit, found, err := client.FindTag(context.Background(), "company/team/service", "v1")
		var response *HTTPError
		if found || commit != (CommitDetails{}) || !errors.As(err, &response) || response.StatusCode != status || calls != 1 {
			t.Fatalf("status %d commit=%+v found=%t error=%v calls=%d", status, commit, found, err, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("pre-canceled lookup made a request")
		return nil, nil
	})
	_, found, err := client.FindTag(ctx, "company/team/service", "v1")
	if found || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup found=%t error=%v", found, err)
	}
}
