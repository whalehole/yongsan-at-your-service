package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func releaseAPIClient(t *testing.T, handler func(*http.Request) (*http.Response, error)) *Client {
	t.Helper()
	client, err := New("https://gitlab.example.com/gitlab", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = &http.Client{Transport: errorResponseTransport(handler)}
	return client
}

func releaseAPIResponse(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCommitsBetweenUsesPinnedReachabilityRangeAndMetadata(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/gitlab/api/v4/projects/company%2Fteam%2Fservice/repository/commits" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		query := r.URL.Query()
		if query.Get("ref_name") != commitA+".."+commitB || query.Get("per_page") != "100" || query.Get("page") != "1" || len(query) != 3 {
			t.Errorf("range query=%v", query)
		}
		for _, forbidden := range []string{"since", "until", "first_parent", "all", "from", "to", "straight", "with_stats"} {
			if query.Has(forbidden) {
				t.Errorf("release range unexpectedly filters or compares files: %s", forbidden)
			}
		}
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("missing configured token")
		}
		body := fmt.Sprintf(`[{"id":%q,"title":"Fix checkout","message":"Fix checkout\n\nKeep existing carts.","author_name":"Jane Doe","author_email":"private@example.com","committed_date":"2026-11-26T10:00:00+08:00","web_url":"https://gitlab.example.com/team/service/-/commit/%s","unexpected_source_diff":"PRIVATE SOURCE DIFF"}]`, commitB, commitB)
		return releaseAPIResponse(200, nil, body), nil
	})
	result, err := client.CommitsBetween(context.Background(), "company/team/service", strings.ToUpper(commitA), strings.ToUpper(commitB))
	if err != nil || calls != 1 || len(result.Commits) != 1 {
		t.Fatalf("range=%#v error=%v calls=%d", result, err, calls)
	}
	got := result.Commits[0]
	if got.SHA != commitB || got.Title != "Fix checkout" || got.Message != "Fix checkout\n\nKeep existing carts." || got.Author != "Jane Doe" || got.CommittedAt != "2026-11-26T10:00:00+08:00" || !strings.HasSuffix(got.URL, commitB) {
		t.Fatalf("commit metadata=%#v", got)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "PRIVATE SOURCE DIFF") || strings.Contains(string(encoded), "private@example.com") {
		t.Fatalf("range exposed unnecessary response data: %s error=%v", encoded, err)
	}
}

func TestCommitsBetweenAcceptsEmptyRangeWithoutFileComparison(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/repository/commits") {
			t.Errorf("empty range tried a file comparison: %s", r.URL)
		}
		return releaseAPIResponse(200, nil, `[]`), nil
	})
	result, err := client.CommitsBetween(context.Background(), "group/service", commitA, commitB)
	if err != nil || result.Commits == nil || len(result.Commits) != 0 || calls != 1 {
		t.Fatalf("empty range=%#v error=%v calls=%d", result, err, calls)
	}
}

func releaseCommitSHA(number int) string { return fmt.Sprintf("%040x", number) }

func commitPageBody(start, count int) string {
	var body strings.Builder
	body.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":%q,"title":"Commit %d"}`, releaseCommitSHA(start+i), start+i)
	}
	body.WriteByte(']')
	return body.String()
}

func TestCommitsBetweenConsumesMoreThanOneHundredCommitsWithoutTotals(t *testing.T) {
	calls := 0
	to := releaseCommitSHA(1)
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if query := r.URL.Query(); query.Get("ref_name") != commitA+".."+to || query.Get("page") != strconv.Itoa(calls) || len(query) != 3 {
			t.Errorf("range changed across pages: %v", query)
		}
		count := 100
		if calls == 3 {
			count = 5
		}
		return releaseAPIResponse(200, nil, commitPageBody((calls-1)*100+1, count)), nil
	})
	result, err := client.CommitsBetween(context.Background(), "group/service", commitA, to)
	if err != nil || calls != 3 || len(result.Commits) != 205 || result.Commits[0].SHA != to || result.Commits[204].SHA != releaseCommitSHA(205) {
		t.Fatalf("range has %d commits; error=%v calls=%d", len(result.Commits), err, calls)
	}
}

func TestCommitsBetweenFollowsExplicitNextPageAndEnd(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		header.Set("X-Page", strconv.Itoa(calls))
		if calls == 1 {
			header.Set("X-Next-Page", "2")
			return releaseAPIResponse(200, header, fmt.Sprintf(`[{"id":%q}]`, commitB)), nil
		}
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, fmt.Sprintf(`[{"id":%q}]`, commitC)), nil
	})
	result, err := client.CommitsBetween(context.Background(), "group/service", commitA, commitB)
	if err != nil || calls != 2 || len(result.Commits) != 2 || result.Commits[0].SHA != commitB || result.Commits[1].SHA != commitC {
		t.Fatalf("range=%#v error=%v calls=%d", result, err, calls)
	}
}

func TestCommitsBetweenHonorsExplicitEndAfterFullPage(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, commitPageBody(1, 100)), nil
	})
	result, err := client.CommitsBetween(context.Background(), "group/service", commitA, releaseCommitSHA(1))
	if err != nil || calls != 1 || len(result.Commits) != 100 {
		t.Fatalf("range has %d commits; error=%v calls=%d", len(result.Commits), err, calls)
	}
}

func TestCommitsBetweenRejectsInvalidRowsAndProvenance(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[null]`, `[{}]`, `[{"id":null}]`, `[{"id":"bad"}]`, `private body test-token`, `[] {}`,
		fmt.Sprintf(`[{"id":%q},{"id":%q}]`, commitB, commitB),
		fmt.Sprintf(`[{"id":%q}]`, commitA), fmt.Sprintf(`[{"id":%q}]`, commitC),
		`{"commits":[],"diffs":[],"compare_timeout":false}`,
	} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) { return releaseAPIResponse(200, nil, body), nil })
		result, err := client.CommitsBetween(context.Background(), "group/service", commitA, commitB)
		if err == nil || result.Commits != nil {
			t.Fatalf("accepted invalid or partial range: %#v, %v", result, err)
		}
		for _, want := range []string{"group/service", commitA, commitB, "release commits"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "private body") || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "file comparison") {
			t.Fatalf("unsafe or irrelevant error: %v", err)
		}
	}
}

func TestCommitsBetweenRejectsRepeatedOrFailedLaterPagesWithoutPartialSuccess(t *testing.T) {
	for _, failure := range []string{"repeated page", "permission", "null page"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return releaseAPIResponse(200, nil, commitPageBody(1, 100)), nil
				}
				switch failure {
				case "permission":
					return releaseAPIResponse(403, nil, "private response test-token"), nil
				case "null page":
					return releaseAPIResponse(200, nil, `null`), nil
				default:
					return releaseAPIResponse(200, nil, commitPageBody(1, 100)), nil
				}
			})
			result, err := client.CommitsBetween(context.Background(), "group/service", commitA, releaseCommitSHA(1))
			if err == nil || result.Commits != nil || calls != 2 {
				t.Fatalf("partial range returned: %#v error=%v calls=%d", result, err, calls)
			}
			if failure == "permission" {
				var response *HTTPError
				if !errors.As(err, &response) || response.StatusCode != 403 {
					t.Fatalf("later-page HTTP cause lost: %v", err)
				}
			}
		})
	}
}

func TestCommitsBetweenRejectsMalformedPagination(t *testing.T) {
	for _, header := range []http.Header{
		{"X-Next-Page": []string{"1"}}, {"X-Next-Page": []string{"3"}}, {"X-Next-Page": []string{"bad"}},
		{"X-Next-Page": []string{"1001"}}, {"X-Page": []string{"0"}},
		{"X-Next-Page": []string{""}, "X-Total-Pages": []string{"2"}},
	} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(200, header, fmt.Sprintf(`[{"id":%q}]`, commitB)), nil
		})
		result, err := client.CommitsBetween(context.Background(), "group/service", commitA, commitB)
		if err == nil || result.Commits != nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("malformed pagination accepted: %#v, %v header=%v", result, err, header)
		}
	}
}

func TestCommitsBetweenStopsAtPageLimit(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return releaseAPIResponse(200, nil, commitPageBody((calls-1)*100+1, 100)), nil
	})
	result, err := client.CommitsBetween(context.Background(), "group/service", commitA, releaseCommitSHA(1))
	if err == nil || result.Commits != nil || calls != 1000 || !strings.Contains(err.Error(), "1000-page") {
		t.Fatalf("unbounded or partial range: %d commits error=%v calls=%d", len(result.Commits), err, calls)
	}
}

func TestCommitsBetweenValidatesPinnedSHAsAndAuthenticationBeforeRequest(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) { calls++; return releaseAPIResponse(200, nil, `[]`), nil })
	for _, refs := range [][2]string{{"v2.2.7", commitB}, {commitA, "main"}, {strings.Repeat("z", 40), commitB}} {
		if _, err := client.CommitsBetween(context.Background(), "group/service", refs[0], refs[1]); err == nil {
			t.Fatalf("accepted unpinned range: %v", refs)
		}
	}
	client.token = ""
	if _, err := client.CommitsBetween(context.Background(), "group/service", commitA, commitB); err == nil || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("anonymous request accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached server: %d", calls)
	}
}

func TestReleaseCommitRangeAndTagListPreservePermissionAndMissingErrors(t *testing.T) {
	for _, status := range []int{403, 404} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(status, nil, "private response test-token"), nil
		})
		_, rangeErr := client.CommitsBetween(context.Background(), "group/service", commitA, commitB)
		_, tagsErr := client.ListTags(context.Background(), "group/service")
		for _, err := range []error{rangeErr, tagsErr} {
			var response *HTTPError
			if err == nil || !errors.As(err, &response) || response.StatusCode != status || IsNotFound(err) != (status == 404) || !strings.Contains(err.Error(), "group/service") || strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("unsafe or lost HTTP error: %v", err)
			}
		}
	}
}

func tagPageBody(start, count int) string {
	var body strings.Builder
	body.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"name":"release/v%d"}`, start+i)
	}
	body.WriteByte(']')
	return body.String()
}

func TestListTagsConsumesFullPagesWithoutPaginationHeaders(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/gitlab/api/v4/projects/team%2Fservice/repository/tags" || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		query := r.URL.Query()
		if query.Get("per_page") != "100" || query.Get("order_by") != "name" || query.Get("sort") != "asc" || query.Get("page") != strconv.Itoa(calls) {
			t.Errorf("tags query=%v", query)
		}
		if calls == 1 {
			return releaseAPIResponse(200, nil, tagPageBody(0, 100)), nil
		}
		return releaseAPIResponse(200, nil, tagPageBody(100, 2)), nil
	})
	tags, err := client.ListTags(context.Background(), "team/service")
	if err != nil || len(tags) != 102 || tags[0] != "release/v0" || tags[101] != "release/v101" || calls != 2 {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestListTagsUsesExplicitNextPageAndPreservesOrder(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		header.Set("X-Page", strconv.Itoa(calls))
		header.Set("X-Total-Pages", "2")
		header.Set("X-Total", "3")
		if calls == 1 {
			header.Set("X-Next-Page", "2")
			return releaseAPIResponse(200, header, `[{"name":"v2.2.10"},{"name":"v2.2.8"}]`), nil
		}
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, `[{"name":"v2.2.9"}]`), nil
	})
	tags, err := client.ListTags(context.Background(), "team/service")
	if err != nil || !reflect.DeepEqual(tags, []string{"v2.2.10", "v2.2.8", "v2.2.9"}) || calls != 2 {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestListTagsHonorsExplicitEndAfterAFullPage(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, tagPageBody(0, 100)), nil
	})
	tags, err := client.ListTags(context.Background(), "team/service")
	if err != nil || len(tags) != 100 || calls != 1 {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestListTagsRejectsMalformedNamesAndPagination(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[null]`, `[{}]`, `[{"name":null}]`, `[{"name":""}]`, `[{"name":5}]`, `[{"name":"bad tag"}]`, `[{"name":"../bad"}]`, `[{"name":"v1"},{"name":"v1"}]`, `private response test-token`} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) { return releaseAPIResponse(200, nil, body), nil })
		tags, err := client.ListTags(context.Background(), "team/service")
		if err == nil || tags != nil || strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "test-token") {
			t.Fatalf("invalid tags response accepted or exposed: %v; %v", tags, err)
		}
	}
	for _, header := range []http.Header{
		{"X-Next-Page": []string{"1"}}, {"X-Next-Page": []string{"3"}}, {"X-Next-Page": []string{"bad"}},
		{"X-Next-Page": []string{"1001"}}, {"X-Next-Page": []string{""}, "X-Total-Pages": []string{"2"}},
		{"X-Next-Page": []string{""}, "X-Total": []string{"2"}}, {"X-Page": []string{"2"}}, {"X-Total-Pages": []string{"bad"}},
	} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(200, header, `[{"name":"v1"}]`), nil
		})
		if tags, err := client.ListTags(context.Background(), "team/service"); err == nil || tags != nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("invalid pagination accepted: %v; %v header=%v", tags, err, header)
		}
	}
}

func TestCommitsBetweenAndListTagsPreserveCancellationAndDeadline(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, compareErr := client.CommitsBetween(ctx, "group/service", commitA, commitB)
	_, tagsErr := client.ListTags(ctx, "group/service")
	if !errors.Is(compareErr, context.Canceled) || !errors.Is(tagsErr, context.Canceled) || calls != 0 {
		t.Fatalf("pre-cancel compare=%v tags=%v calls=%d", compareErr, tagsErr, calls)
	}
	for _, compare := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		var err error
		if compare {
			_, err = client.CommitsBetween(ctx, "group/service", commitA, commitB)
		} else {
			_, err = client.ListTags(ctx, "group/service")
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("deadline cause lost: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("canceled requests were retried: calls=%d", calls)
	}
}
