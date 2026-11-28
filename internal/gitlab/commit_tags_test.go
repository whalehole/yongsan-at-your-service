package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTagsAtCommitUsesExactResolvedCommitForAnnotatedAndLightweightTags(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/gitlab/api/v4/projects/company%2Fteam%2Fservice/repository/tags" || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		want := map[string][]string{"page": {"1"}, "per_page": {"100"}, "order_by": {"name"}, "sort": {"asc"}}
		if !reflect.DeepEqual(map[string][]string(r.URL.Query()), want) {
			t.Errorf("tag inventory query=%v", r.URL.Query())
		}
		body := fmt.Sprintf(`[
			{"name":"selected-release","target":%q,"commit":{"id":%q}},
			{"name":"annotated-alias","target":%q,"message":"Annotation","commit":{"id":%q,"title":123}},
			{"name":"descendant-release","target":%q,"commit":{"id":%q}},
			{"name":"ancestor-release","commit":{"id":%q}},
			{"name":"Release-Alias","commit":{"id":%q}},
			{"name":"release/alias","commit":{"id":%q}}
		]`, commitA, commitA, commitC, strings.ToUpper(commitA), commitA, commitB, commitC, commitA, commitA)
		return releaseAPIResponse(200, nil, body), nil
	})
	tags, err := client.TagsAtCommit(context.Background(), "company/team/service", strings.ToUpper(commitA))
	want := []string{"Release-Alias", "annotated-alias", "release/alias", "selected-release"}
	if err != nil || calls != 1 || !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func commitTagsPage(start, count int, sha string) string {
	var body strings.Builder
	body.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"name":"release-%03d","commit":{"id":%q}}`, start+i, sha)
	}
	body.WriteByte(']')
	return body.String()
}

func TestTagsAtCommitConsumesAllPagesWithoutPaginationHeaders(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("page") != strconv.Itoa(calls) {
			t.Errorf("unexpected page: %s", r.URL)
		}
		if calls == 1 {
			return releaseAPIResponse(200, nil, commitTagsPage(1, 100, commitB)), nil
		}
		return releaseAPIResponse(200, nil, commitTagsPage(101, 2, commitA)), nil
	})
	tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
	if err != nil || calls != 2 || !reflect.DeepEqual(tags, []string{"release-101", "release-102"}) {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestTagsAtCommitExplicitPaginationDeduplicatesAndSorts(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		header := make(http.Header)
		header.Set("X-Page", strconv.Itoa(calls))
		header.Set("X-Total", "4")
		header.Set("X-Total-Pages", "2")
		if calls == 1 {
			header.Set("X-Next-Page", "2")
			return releaseAPIResponse(200, header, fmt.Sprintf(`[{"name":"zeta","commit":{"id":%q}},{"name":"unrelated","commit":{"id":%q}}]`, commitA, commitB)), nil
		}
		header.Set("X-Next-Page", "")
		return releaseAPIResponse(200, header, fmt.Sprintf(`[{"name":"zeta","commit":{"id":%q}},{"name":"alpha","commit":{"id":%q}}]`, strings.ToUpper(commitA), commitA)), nil
	})
	tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
	if err != nil || calls != 2 || !reflect.DeepEqual(tags, []string{"alpha", "zeta"}) {
		t.Fatalf("tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestTagsAtCommitEmptyAndSHA256Ranges(t *testing.T) {
	for _, body := range []string{`[]`, fmt.Sprintf(`[{"name":"other-commit","commit":{"id":%q}}]`, commitB)} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) { return releaseAPIResponse(200, nil, body), nil })
		tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
		if err != nil || tags == nil || len(tags) != 0 {
			t.Fatalf("empty matching tags=%v error=%v", tags, err)
		}
	}
	sha := strings.Repeat("f", 64)
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		return releaseAPIResponse(200, nil, fmt.Sprintf(`[{"name":"sha256-release","commit":{"id":%q}}]`, sha)), nil
	})
	tags, err := client.TagsAtCommit(context.Background(), "group/service", strings.ToUpper(sha))
	if err != nil || !reflect.DeepEqual(tags, []string{"sha256-release"}) {
		t.Fatalf("SHA-256 matching tags=%v error=%v", tags, err)
	}
}

func TestTagsAtCommitRejectsInvalidRowsAndConflictingTagMappings(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[null]`, `[{}]`, `[{"name":null}]`, `[{"name":"bad..ref"}]`,
		`[{"name":"release","commit":null}]`, `[{"name":"release","commit":{}}]`,
		`[{"name":"unrelated","commit":{"id":"not-a-full-sha"}}]`,
		`[{"name":"unrelated","commit":{"id":123}}]`,
		fmt.Sprintf(`[{"name":"release","commit":{"id":%q}},{"name":"release","commit":{"id":%q}}]`, commitA, commitB),
		`[] {"private":"test-token"}`, `PRIVATE BODY test-token`,
		commitTagsPage(1, 101, commitA),
	} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) { return releaseAPIResponse(200, nil, body), nil })
		tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
		if err == nil || tags != nil {
			t.Fatalf("accepted invalid tag metadata: tags=%v error=%v", tags, err)
		}
		for _, want := range []string{"group/service", commitA, "tags pointing to commit", "invalid or incomplete response"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "PRIVATE BODY") || strings.Contains(err.Error(), "test-token") {
			t.Fatalf("error exposed response data: %v", err)
		}
	}
}

func TestTagsAtCommitFailedLaterPageNeverReturnsPartialMatches(t *testing.T) {
	for _, failure := range []string{"forbidden", "malformed", "pagination", "changed mapping", "repeated page"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return releaseAPIResponse(200, http.Header{"X-Next-Page": {"2"}}, commitTagsPage(1, 1, commitA)), nil
				}
				switch failure {
				case "forbidden":
					return releaseAPIResponse(403, nil, "PRIVATE BODY test-token"), nil
				case "pagination":
					return releaseAPIResponse(200, http.Header{"X-Next-Page": {"4"}}, `[]`), nil
				case "changed mapping":
					return releaseAPIResponse(200, nil, commitTagsPage(1, 1, commitB)), nil
				case "repeated page":
					return releaseAPIResponse(200, http.Header{"X-Next-Page": {"3"}}, commitTagsPage(1, 1, commitA)), nil
				default:
					return releaseAPIResponse(200, nil, `null`), nil
				}
			})
			tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
			if err == nil || tags != nil || calls != 2 {
				t.Fatalf("partial matching tags=%v error=%v calls=%d", tags, err, calls)
			}
		})
	}
}

func TestTagsAtCommitPreservesHTTPAndCancellationCauses(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
			return releaseAPIResponse(status, nil, "PRIVATE BODY test-token"), nil
		})
		tags, err := client.TagsAtCommit(context.Background(), "group/service", commitA)
		var response *HTTPError
		if tags != nil || !errors.As(err, &response) || response.StatusCode != status || !strings.Contains(err.Error(), strconv.Itoa(status)) {
			t.Fatalf("status %d tags=%v error=%v", status, tags, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	client := releaseAPIClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return releaseAPIResponse(200, http.Header{"X-Next-Page": {"2"}}, commitTagsPage(1, 1, commitA)), nil
		}
		cancel()
		return nil, r.Context().Err()
	})
	tags, err := client.TagsAtCommit(ctx, "group/service", commitA)
	if tags != nil || !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("canceled matching tags=%v error=%v calls=%d", tags, err, calls)
	}
}

func TestTagsAtCommitRejectsInvalidSHAAndPreCanceledContextBeforeRequests(t *testing.T) {
	calls := 0
	client := releaseAPIClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not be called")
	})
	for _, sha := range []string{"", commitA[:7], strings.Repeat("g", 40)} {
		tags, err := client.TagsAtCommit(context.Background(), "group/service", sha)
		if err == nil || tags != nil || calls != 0 {
			t.Fatalf("invalid SHA %q tags=%v error=%v calls=%d", sha, tags, err, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tags, err := client.TagsAtCommit(ctx, "group/service", commitA)
	if tags != nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-canceled tags=%v error=%v calls=%d", tags, err, calls)
	}
}
