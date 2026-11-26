package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ReleaseCommit contains release-note metadata, excluding email addresses.
type ReleaseCommit struct {
	SHA         string `json:"id"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Author      string `json:"author_name"`
	CommittedAt string `json:"committed_date"`
	URL         string `json:"web_url"`
}

type CommitRange struct {
	Commits []ReleaseCommit
}

const releaseResponseLimit = 16 << 20

// CommitsBetween lists commits reachable from the current release but not the
// previous release, using the pinned previousSHA..currentSHA revision range.
// Every page must succeed before any commit list is returned.
func (c *Client) CommitsBetween(ctx context.Context, project, fromSHA, toSHA string) (CommitRange, error) {
	const pageSize, maxPages = 100, 1000
	action := fmt.Sprintf("list release commits for range %q in repository %q", fromSHA+".."+toSHA, project)
	if err := ctx.Err(); err != nil {
		return CommitRange{}, actionError(action, err, false)
	}
	from, fromErr := resolvedSHA(fromSHA)
	to, toErr := resolvedSHA(toSHA)
	if fromErr != nil || toErr != nil {
		return CommitRange{}, &OperationError{Message: "Cannot " + action + ": previous and current must be full commit SHAs.", Cause: errors.Join(fromErr, toErr)}
	}
	if strings.TrimSpace(c.token) == "" {
		return CommitRange{}, &OperationError{Message: "Cannot " + action + ": an access token is required to list release commits."}
	}
	result := CommitRange{Commits: make([]ReleaseCommit, 0)}
	seen := map[string]bool{}
	page := 1
	for request := 0; request < maxPages; request++ {
		if err := ctx.Err(); err != nil {
			return CommitRange{}, actionError(action, err, false)
		}
		u := c.endpoint(project, "repository", "commits")
		query := u.Query()
		query.Set("ref_name", from+".."+to)
		query.Set("per_page", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))
		u.RawQuery = query.Encode()
		resp, err := c.request(ctx, http.MethodGet, u, nil)
		if err != nil {
			return CommitRange{}, actionError(action, err, false)
		}
		var entries []ReleaseCommit
		decodeErr := decodeReleaseResponse(resp.Body, &entries)
		closeErr := resp.Body.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return CommitRange{}, actionError(action, err, false)
		}
		if entries == nil || len(entries) > pageSize {
			return CommitRange{}, responseError(action, errors.New("missing, null, or oversized release-commit page"))
		}
		for i := range entries {
			sha, err := resolvedSHA(entries[i].SHA)
			if err != nil || seen[sha] || sha == from {
				return CommitRange{}, withExplanation(responseError(action, errors.New("invalid, repeated, or excluded commit in release range")), "The complete release-commit list could not be verified.")
			}
			seen[sha] = true
			entries[i].SHA = sha
		}
		result.Commits = append(result.Commits, entries...)
		next, done, err := releasePageNext(resp.Header, page, len(entries), len(result.Commits), pageSize, maxPages)
		if err != nil {
			return CommitRange{}, withExplanation(responseError(action, err), "The release-commit listing is incomplete because GitLab returned invalid or unsupported pagination.")
		}
		if done {
			if err := ctx.Err(); err != nil {
				return CommitRange{}, actionError(action, err, false)
			}
			if len(result.Commits) > 0 && !seen[to] {
				return CommitRange{}, withExplanation(responseError(action, errors.New("current release commit was absent from the nonempty range")), "The complete release-commit list could not be verified.")
			}
			return result, nil
		}
		page = next
	}
	return CommitRange{}, &OperationError{Message: "Cannot " + action + ": the release-commit listing is incomplete; the 1000-page safety limit was reached."}
}

// ListTags consumes every page and preserves the server's order. A partial or
// malformed listing is an error, so it cannot select a misleading predecessor.
func (c *Client) ListTags(ctx context.Context, project string) ([]string, error) {
	const pageSize, maxPages = 100, 1000
	action := fmt.Sprintf("list tags in repository %q", project)
	tags := make([]string, 0)
	seen := map[string]bool{}
	page := 1
	for request := 0; request < maxPages; request++ {
		if err := ctx.Err(); err != nil {
			return nil, actionError(action, err, false)
		}
		u := c.endpoint(project, "repository", "tags")
		query := u.Query()
		query.Set("per_page", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))
		query.Set("order_by", "name")
		query.Set("sort", "asc")
		u.RawQuery = query.Encode()
		resp, err := c.request(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, actionError(action, err, false)
		}
		var entries []struct {
			Name *string `json:"name"`
		}
		decodeErr := decodeReleaseResponse(resp.Body, &entries)
		closeErr := resp.Body.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return nil, actionError(action, err, false)
		}
		if entries == nil || len(entries) > pageSize {
			return nil, responseError(action, errors.New("missing, null, or oversized tag page"))
		}
		for _, entry := range entries {
			if entry.Name == nil || !validListedTag(*entry.Name) || seen[*entry.Name] {
				return nil, responseError(action, errors.New("invalid, missing, or repeated tag name"))
			}
			seen[*entry.Name] = true
			tags = append(tags, *entry.Name)
		}
		next, done, err := releasePageNext(resp.Header, page, len(entries), len(tags), pageSize, maxPages)
		if err != nil {
			return nil, withExplanation(responseError(action, err), "The tag listing is incomplete because GitLab returned invalid or unsupported pagination.")
		}
		if done {
			if err := ctx.Err(); err != nil {
				return nil, actionError(action, err, false)
			}
			return tags, nil
		}
		page = next
	}
	return nil, &OperationError{Message: "Cannot " + action + ": the tag listing is incomplete; the 1000-page safety limit was reached."}
}

func releasePageNext(header http.Header, page, count, listed, pageSize, maxPages int) (int, bool, error) {
	if raw := strings.TrimSpace(header.Get("X-Page")); raw != "" {
		actual, err := strconv.Atoi(raw)
		if err != nil || actual != page {
			return 0, false, errors.New("response page does not match the requested page")
		}
	}
	totalPages, totalTags := -1, -1
	for name, target := range map[string]*int{"X-Total-Pages": &totalPages, "X-Total": &totalTags} {
		if raw := strings.TrimSpace(header.Get(name)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || name == "X-Total-Pages" && (value > maxPages || value > 0 && value < page || value == 0 && count > 0) || name == "X-Total" && value < listed {
				return 0, false, errors.New("invalid pagination totals")
			}
			*target = value
		}
	}
	if headerPresent(header, "X-Next-Page") {
		raw := strings.TrimSpace(header.Get("X-Next-Page"))
		if raw == "" || raw == "0" {
			if hasNextLink(header.Get("Link")) || totalPages > page || totalTags >= 0 && totalTags != listed {
				return 0, false, errors.New("conflicting pagination headers")
			}
			return 0, true, nil
		}
		next, err := strconv.Atoi(raw)
		if err != nil || next != page+1 || next > maxPages || totalPages >= 0 && next > totalPages {
			return 0, false, errors.New("invalid or nonincreasing next page")
		}
		return next, false, nil
	}
	if totalPages >= 0 {
		if totalPages > page {
			return page + 1, false, nil
		}
		if hasNextLink(header.Get("Link")) || totalTags >= 0 && totalTags != listed {
			return 0, false, errors.New("incomplete listing totals")
		}
		return 0, true, nil
	}
	if count < pageSize {
		if hasNextLink(header.Get("Link")) || totalTags > listed {
			return 0, false, errors.New("unsupported next-page link without a numeric page header")
		}
		return 0, true, nil
	}
	return page + 1, false, nil
}

func headerPresent(header http.Header, name string) bool {
	for key := range header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func hasNextLink(link string) bool {
	link = strings.ToLower(link)
	return strings.Contains(link, `rel="next"`) || strings.Contains(link, "rel=next")
}

func validListedTag(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, ch := range name {
		if ch <= ' ' || ch == 127 || strings.ContainsRune(`~^:?*[\`, ch) {
			return false
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func decodeReleaseResponse(body io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(body, releaseResponseLimit+1))
	if err != nil {
		return &responseDecodeError{cause: err}
	}
	if len(data) > releaseResponseLimit {
		return &responseDecodeError{cause: errors.New("GitLab response exceeded the 16 MiB safety limit")}
	}
	if err := json.Unmarshal(data, value); err != nil {
		return &responseDecodeError{cause: err}
	}
	return nil
}
