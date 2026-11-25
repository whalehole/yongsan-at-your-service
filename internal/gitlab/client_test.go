package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	commitC = "cccccccccccccccccccccccccccccccccccccccc"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeReference(w http.ResponseWriter, commit string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"commit":{"id":%q},"target":%q}`, commit, commitC)
}

func TestResolveAbbreviatedCommit(t *testing.T) {
	full := "f63850b" + strings.Repeat("a", 33)
	for _, tc := range []struct {
		name, response string
		status         int
		wantError      bool
	}{
		{"resolves", full, 200, false},
		{"different prefix", commitA, 200, true},
		{"missing or ambiguous", "", 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/commits") {
					fmt.Fprint(w, `[]`)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/commits/f63850b") {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"id":%q}`, tc.response)
			})
			got, err := client.ResolveCommit(context.Background(), "group/project", "f63850b")
			if (err != nil) != tc.wantError {
				t.Fatalf("result=%q error=%v", got, err)
			}
			if err == nil && got != full {
				t.Fatalf("resolved=%q", got)
			}
		})
	}
}

func TestCommitPrefixFallbackSearchesAllPages(t *testing.T) {
	full := "f63850b7" + strings.Repeat("1", 32)
	other := "f63850b8" + strings.Repeat("2", 32)
	for _, tc := range []struct {
		name                  string
		ambiguous, failSecond bool
	}{
		{name: "unique match across pages"},
		{name: "collision on later page", ambiguous: true},
		{name: "incomplete listing cannot accept match", failSecond: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var verified atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/commits/f63850b"):
					w.WriteHeader(404)
				case strings.HasSuffix(r.URL.Path, "/commits/"+full):
					verified.Add(1)
					fmt.Fprintf(w, `{"id":%q}`, full)
				case strings.HasSuffix(r.URL.Path, "/commits"):
					if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("per_page") != "100" {
						t.Errorf("query=%v", r.URL.Query())
					}
					if r.URL.Query().Get("page") == "1" {
						fmt.Fprintf(w, `[{"id":%q}`, full)
						for i := 1; i < 100; i++ {
							fmt.Fprintf(w, `,{"id":%q}`, commitA)
						}
						fmt.Fprint(w, `]`)
					} else if tc.failSecond {
						w.WriteHeader(403)
					} else if tc.ambiguous {
						fmt.Fprintf(w, `[{"id":%q}]`, other)
					} else {
						fmt.Fprint(w, `[]`)
					}
				default:
					t.Errorf("unexpected path=%s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			got, err := client.ResolveCommit(context.Background(), "group/project", "f63850b")
			if tc.ambiguous || tc.failSecond {
				if err == nil || verified.Load() != 0 {
					t.Fatalf("accepted unverified unique match: %q, %v", got, err)
				}
			} else if err != nil || got != full || verified.Load() != 1 {
				t.Fatalf("result=%q err=%v verified=%d", got, err, verified.Load())
			}
		})
	}
}

func TestEncodedNamespaceAndReferenceWithDeploymentPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/gitlab/api/v4/projects/company%2Fplatform%2Fapi/repository/branches/release%2F1.2"
		if r.URL.EscapedPath() != want {
			t.Errorf("escaped path = %q, want %q", r.URL.EscapedPath(), want)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "secret" {
			t.Error("missing authentication header")
		}
		writeReference(w, commitA)
	}))
	defer server.Close()
	for _, suffix := range []string{"/gitlab", "/gitlab/", "/gitlab/api/v4", "/gitlab/api/v4/"} {
		client, err := New(server.URL+suffix, "secret")
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.ResolveBranch(context.Background(), "company/platform/api", "release/1.2")
		if err != nil || got != commitA {
			t.Fatalf("ResolveBranch() = %q, %v", got, err)
		}
	}
}

func TestNewRejectsInvalidOrCredentialBearingURLs(t *testing.T) {
	for _, address := range []string{
		"", "gitlab.com", "ssh://gitlab.com", "https://user:password@gitlab.com",
		"https://gitlab.com?token=secret", "https://gitlab.com?", "https://gitlab.com#fragment",
		"https://gitlab.com/../secret", "https://gitlab.com/%2e%2e/secret",
	} {
		t.Run(address, func(t *testing.T) {
			if _, err := New(address, "token"); err == nil {
				t.Error("New() accepted an invalid or unsafe URL")
			}
		})
	}
}

func TestResolveCommitRequiresValidSHAInResponse(t *testing.T) {
	for _, sha := range []string{"", "short", strings.Repeat("z", 40), strings.ToUpper(commitA)} {
		t.Run(sha, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":%q}`, sha)
			})
			got, err := client.ResolveCommit(context.Background(), "group/project", commitA)
			if sha == strings.ToUpper(commitA) {
				if err != nil || got != commitA {
					t.Fatalf("ResolveCommit() = %q, %v", got, err)
				}
			} else if err == nil {
				t.Error("invalid SHA in response was accepted")
			}
		})
	}
}

func TestValidateBranchUsesMergeBaseForAncestor(t *testing.T) {
	for _, tc := range []struct {
		name      string
		base      string
		status    int
		wantError bool
	}{
		{name: "ancestor", base: commitA},
		{name: "different ancestor", base: commitC, wantError: true},
		{name: "unrelated histories", status: http.StatusNotFound, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mergeCalls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.EscapedPath(), "/branches/release%2F1.2") {
					writeReference(w, commitB)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/merge_base") {
					t.Errorf("unexpected endpoint: %s", r.URL.EscapedPath())
					w.WriteHeader(http.StatusNotFound)
					return
				}
				mergeCalls.Add(1)
				if got := r.URL.Query()["refs[]"]; !reflect.DeepEqual(got, []string{commitA, commitB}) {
					t.Errorf("merge-base refs = %v", got)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = fmt.Fprintf(w, `{"id":%q}`, tc.base)
			})
			err := client.ValidateBranch(context.Background(), "group/project", commitA, "release/1.2")
			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateBranch() error = %v, want error %v", err, tc.wantError)
			}
			if mergeCalls.Load() != 1 {
				t.Errorf("merge-base calls = %d, want 1", mergeCalls.Load())
			}
		})
	}
}

func TestValidateBranchTipDoesNotNeedMergeBase(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/merge_base") {
			t.Error("tip equality should not request merge base")
		}
		writeReference(w, commitA)
	})
	if err := client.ValidateBranch(context.Background(), "group/project", commitA, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureTagSkipsAnnotatedTagByCommitAndDetectsConflict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		want    string
		wantErr bool
	}{
		{name: "same commit", current: commitA, want: "skipped"},
		{name: "different commit", current: commitB, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("existing tag triggered a mutating request: %s", r.Method)
				}
				if !strings.HasSuffix(r.URL.EscapedPath(), "/tags/release%2F1.2") {
					t.Errorf("tag name was not URL encoded: %s", r.URL.EscapedPath())
				}
				writeReference(w, tc.current)
			})
			status, err := client.EnsureTag(context.Background(), "group/project", commitA, "release/1.2", "new annotation", false)
			if status != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("EnsureTag() = %q, %v", status, err)
			}
		})
	}
}

func TestEnsureTagCreatesAnnotatedTag(t *testing.T) {
	var posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("ref") != commitA || r.Form.Get("tag_name") != "release/1.2" || r.Form.Get("message") != "Release & hotfix" {
			t.Errorf("incorrect tag form: %v", r.Form)
		}
		w.WriteHeader(http.StatusCreated)
		writeReference(w, commitA)
	})
	status, err := client.EnsureTag(context.Background(), "group/project", commitA, "release/1.2", "Release & hotfix", false)
	if err != nil || status != "created" || posts.Load() != 1 {
		t.Fatalf("EnsureTag() = %q, %v; POST count %d", status, err, posts.Load())
	}
}

func TestEnsureTagDryRunDoesNotCreate(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("dry run attempted to mutate GitLab")
		}
		w.WriteHeader(http.StatusNotFound)
	})
	status, err := client.EnsureTag(context.Background(), "group/project", commitA, "v1.2.0", "", true)
	if err != nil || status != "planned" || calls.Load() != 1 {
		t.Fatalf("EnsureTag() = %q, %v; calls %d", status, err, calls.Load())
	}
}

func TestEnsureTagRechecksAfterCreateRace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		wantErr bool
	}{
		{name: "same target", current: commitA},
		{name: "different target", current: commitB, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets, posts atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if gets.Add(1) == 1 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				writeReference(w, tc.current)
			})
			status, err := client.EnsureTag(context.Background(), "group/project", commitA, "v1.2.0", "", false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("EnsureTag() = %q, %v", status, err)
			}
			if !tc.wantErr && status != "skipped" {
				t.Errorf("status = %q, want skipped", status)
			}
			if posts.Load() != 1 || gets.Load() != 2 {
				t.Errorf("POST count %d, GET count %d", posts.Load(), gets.Load())
			}
		})
	}
}

func TestArchiveRequestsPinnedCommitAndSubdirectory(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repository/archive.tar.gz") {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("sha") != commitA || query.Get("path") != "services/worker" || query.Get("include_lfs_blobs") != "true" {
			t.Errorf("incorrect archive query: %v", query)
		}
		_, _ = w.Write([]byte("source archive"))
	})
	body, err := client.Archive(context.Background(), "group/project", commitA, "services/worker")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "source archive" {
		t.Fatalf("archive = %q, %v", got, err)
	}
}

func TestGETRetriesRateLimitAndTransientErrors(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if count <= 2 {
			w.Header().Set("Retry-After", "0")
			if count == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q}`, commitA)
	})
	got, err := client.ResolveCommit(context.Background(), "group/project", commitA)
	if err != nil || got != commitA || calls.Load() != 3 {
		t.Fatalf("ResolveCommit() = %q, %v; calls %d", got, err, calls.Load())
	}
}

func TestRetryLimitAndErrorDoesNotExposeResponseBody(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "private repository detail and test-token")
	})
	_, err := client.ResolveCommit(context.Background(), "group/project", commitA)
	var responseErr *HTTPError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 4 {
		t.Errorf("calls = %d, want 4", calls.Load())
	}
	if strings.Contains(err.Error(), "private repository") || strings.Contains(err.Error(), "test-token") {
		t.Error("response body or token was exposed in an error")
	}
}

func TestPOSTIsNotRetried(t *testing.T) {
	var posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := client.EnsureTag(context.Background(), "group/project", commitA, "v1.2.0", "", false); err == nil {
		t.Error("expected create failure")
	}
	if posts.Load() != 1 {
		t.Errorf("POST count = %d, want 1", posts.Load())
	}
}

func TestEnsureTagRetriesExplicitRateLimitedCreation(t *testing.T) {
	var gets, posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("ref") != commitA || r.Form.Get("tag_name") != "v1.2.0" {
			t.Errorf("replayed tag form = %v", r.Form)
		}
		if posts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	status, err := client.EnsureTag(context.Background(), "group/project", commitA, "v1.2.0", "", false)
	if err != nil || status != "created" {
		t.Fatalf("EnsureTag() = %q, %v", status, err)
	}
	if gets.Load() != 1 || posts.Load() != 2 {
		t.Errorf("GET count %d, POST count %d; want 1 and 2", gets.Load(), posts.Load())
	}
}

func TestEnsureTagRateLimitRetriesAreBounded(t *testing.T) {
	var gets, posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := client.EnsureTag(context.Background(), "group/project", commitA, "v1.2.0", "", false)
	var responseErr *HTTPError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want HTTP 429", err)
	}
	if gets.Load() != 1 || posts.Load() != 4 {
		t.Errorf("GET count %d, POST count %d; want 1 and 4", gets.Load(), posts.Load())
	}
}

func TestEnsureTagRateLimitWaitCanBeCancelled(t *testing.T) {
	var gets, posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.EnsureTag(ctx, "group/project", commitA, "v1.2.0", "", false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if gets.Load() != 1 || posts.Load() != 1 {
		t.Errorf("GET count %d, POST count %d; want 1 and 1", gets.Load(), posts.Load())
	}
}

func TestRetryAfterWaitCanBeCancelled(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.ResolveCommit(ctx, "group/project", commitA)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestRedirectIsRejectedWithoutLeakingToken(t *testing.T) {
	var redirected atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		writeReference(w, commitA)
	}))
	defer other.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})
	// A caller-supplied redirect policy must not bypass the client's restriction.
	client.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	_, err := client.ResolveTag(context.Background(), "group/project", "v1.2.0")
	var responseErr *HTTPError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusFound {
		t.Fatalf("unexpected redirect error: %v", err)
	}
	if redirected.Load() {
		t.Error("GitLab request followed a redirect")
	}
}

func TestRetryAfterHTTPDateAndInvalidHeader(t *testing.T) {
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	delay := retryDelay(future.Format(http.TimeFormat), 0)
	if delay <= 55*time.Second || delay > time.Minute {
		t.Errorf("date delay = %v, expected about one minute", delay)
	}
	if got := retryDelay("invalid", 2); got != 400*time.Millisecond {
		t.Errorf("fallback delay = %v", got)
	}
	if got := retryDelay("-1", 0); got != 100*time.Millisecond {
		t.Errorf("negative Retry-After delay = %v", got)
	}
}
