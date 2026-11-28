package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
)

// TagsAtCommit lists tags whose resolved commit is exactly sha, including
// annotated tags. Tag-object targets and ancestor reachability are not used.
// Every page must succeed before any names are returned.
func (c *Client) TagsAtCommit(ctx context.Context, project, sha string) ([]string, error) {
	const pageSize, maxPages = 100, 1000
	action := fmt.Sprintf("list tags pointing to commit %q in repository %q", sha, project)
	if err := ctx.Err(); err != nil {
		return nil, actionError(action, err, false)
	}
	pinnedSHA, err := resolvedSHA(sha)
	if err != nil {
		return nil, &OperationError{Message: "Cannot " + action + ": use a full commit SHA.", Cause: err}
	}
	names := make([]string, 0)
	seen := make(map[string]string)
	page, listed := 1, 0
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
			Name   *string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		decodeErr := decodeReleaseResponse(resp.Body, &entries)
		closeErr := resp.Body.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return nil, actionError(action, err, false)
		}
		if entries == nil || len(entries) > pageSize {
			return nil, responseError(action, errors.New("missing, null, or oversized commit-tag page"))
		}
		newNames := 0
		for _, entry := range entries {
			if entry.Name == nil || !validListedTag(*entry.Name) {
				return nil, responseError(action, errors.New("missing or invalid tag name"))
			}
			commit, err := resolvedSHA(entry.Commit.ID)
			if err != nil {
				return nil, responseError(action, err)
			}
			if previous, duplicate := seen[*entry.Name]; duplicate {
				if previous != commit {
					return nil, withExplanation(responseError(action, errors.New("one tag name resolved to conflicting commit SHAs")), "The tag listing changed while it was being read; the complete set of matching tags could not be verified.")
				}
				continue
			}
			seen[*entry.Name] = commit
			newNames++
			if commit == pinnedSHA {
				names = append(names, *entry.Name)
			}
		}
		listed += len(entries)
		next, done, err := releasePageNext(resp.Header, page, len(entries), listed, pageSize, maxPages)
		if err != nil {
			return nil, withExplanation(responseError(action, err), "The matching-tag listing is incomplete because GitLab returned invalid or unsupported pagination.")
		}
		if done {
			if err := ctx.Err(); err != nil {
				return nil, actionError(action, err, false)
			}
			sort.Strings(names)
			return names, nil
		}
		if newNames == 0 {
			return nil, withExplanation(responseError(action, errors.New("tag pagination repeated a page without advancing")), "The complete set of matching tags could not be verified.")
		}
		page = next
	}
	return nil, &OperationError{Message: "Cannot " + action + ": the matching-tag listing is incomplete; the 1000-page safety limit was reached."}
}
