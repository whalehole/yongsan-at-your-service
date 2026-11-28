package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Project identifies a repository returned by GitLab's group inventory.
type Project struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

// ListGroupProjects returns every visible repository owned directly by a group.
// Shared projects and repositories in nested subgroups are excluded. An error
// never returns a partial inventory.
func (c *Client) ListGroupProjects(ctx context.Context, group string) ([]Project, error) {
	const pageSize, maxPages = 100, 1000
	action := fmt.Sprintf("list repositories directly in group %q", group)
	if err := ctx.Err(); err != nil {
		return nil, actionError(action, err, false)
	}
	if !validGroupPath(group) {
		return nil, &OperationError{Message: "Cannot " + action + ": use a nonempty group path or positive numeric group ID."}
	}
	// Resolving the group first also handles numeric IDs and alternate-case paths
	// without comparing the returned projects against an uncanonicalized input.
	var canonical struct {
		ID       int64  `json:"id"`
		FullPath string `json:"full_path"`
	}
	u := c.groupEndpoint(group)
	query := u.Query()
	query.Set("with_projects", "false")
	u.RawQuery = query.Encode()
	if err := c.getJSON(ctx, u, &canonical); err != nil {
		return nil, actionError(action, err, false)
	}
	if canonical.ID <= 0 || !validGroupPath(canonical.FullPath) {
		return nil, responseError(action, errors.New("missing or invalid canonical group identity"))
	}

	projects := make([]Project, 0)
	byID := make(map[int64]string)
	byPath := make(map[string]int64)
	page, listed := 1, 0
	for request := 0; request < maxPages; request++ {
		if err := ctx.Err(); err != nil {
			return nil, actionError(action, err, false)
		}
		u := c.groupEndpoint(strconv.FormatInt(canonical.ID, 10), "projects")
		query := u.Query()
		query.Set("per_page", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))
		query.Set("include_subgroups", "false")
		query.Set("with_shared", "false")
		query.Set("order_by", "id")
		query.Set("sort", "asc")
		u.RawQuery = query.Encode()
		resp, err := c.request(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, actionError(action, err, false)
		}
		var entries []struct {
			Project
			Namespace *struct {
				ID int64 `json:"id"`
			} `json:"namespace"`
		}
		decodeErr := decodeReleaseResponse(resp.Body, &entries)
		closeErr := resp.Body.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return nil, actionError(action, err, false)
		}
		if entries == nil || len(entries) > pageSize {
			return nil, responseError(action, errors.New("missing, null, or oversized group-project page"))
		}
		for _, entry := range entries {
			slash := strings.LastIndexByte(entry.PathWithNamespace, '/')
			if entry.ID <= 0 || slash < 0 || !validGroupPath(entry.PathWithNamespace) || entry.Namespace != nil && entry.Namespace.ID <= 0 {
				return nil, responseError(action, errors.New("missing or invalid repository identity in group inventory"))
			}
			parent := entry.PathWithNamespace[:slash]
			if path, ok := byID[entry.ID]; ok && path != entry.PathWithNamespace {
				return nil, responseError(action, errors.New("conflicting repository paths for one project ID"))
			}
			if id, ok := byPath[entry.PathWithNamespace]; ok && id != entry.ID {
				return nil, responseError(action, errors.New("conflicting project IDs for one repository path"))
			}
			_, duplicate := byID[entry.ID]
			byID[entry.ID] = entry.PathWithNamespace
			byPath[entry.PathWithNamespace] = entry.ID
			direct := parent == canonical.FullPath
			if entry.Namespace != nil {
				if (entry.Namespace.ID == canonical.ID) != direct {
					return nil, responseError(action, errors.New("repository namespace identity conflicts with its path"))
				}
				direct = entry.Namespace.ID == canonical.ID
			}
			if direct && !duplicate {
				projects = append(projects, entry.Project)
			}
		}
		listed += len(entries)
		next, done, err := releasePageNext(resp.Header, page, len(entries), listed, pageSize, maxPages)
		if err != nil {
			return nil, withExplanation(responseError(action, err), "The repository inventory is incomplete because GitLab returned invalid or unsupported pagination.")
		}
		if done {
			if err := ctx.Err(); err != nil {
				return nil, actionError(action, err, false)
			}
			sort.Slice(projects, func(i, j int) bool {
				if projects[i].PathWithNamespace == projects[j].PathWithNamespace {
					return projects[i].ID < projects[j].ID
				}
				return projects[i].PathWithNamespace < projects[j].PathWithNamespace
			})
			return projects, nil
		}
		page = next
	}
	return nil, &OperationError{Message: "Cannot " + action + ": the repository inventory is incomplete; the 1000-page safety limit was reached."}
}

// FindTag distinguishes a confirmed absent tag from an inaccessible repository.
// After a 404, it verifies both project visibility and access to repository tags.
func (c *Client) FindTag(ctx context.Context, project, tag string) (CommitDetails, bool, error) {
	if err := ctx.Err(); err != nil {
		return CommitDetails{}, false, actionError(fmt.Sprintf("look up tag %q in repository %q", tag, project), err, false)
	}
	commit, err := c.ResolveTagDetails(ctx, project, tag)
	if err == nil {
		return commit, true, nil
	}
	if !IsNotFound(err) {
		return CommitDetails{}, false, err
	}
	var info struct {
		ID int64 `json:"id"`
	}
	verificationErr := c.getJSON(ctx, c.endpoint(project), &info)
	if verificationErr == nil && info.ID <= 0 {
		verificationErr = &responseDecodeError{cause: errors.New("GitLab returned an invalid repository ID")}
	}
	if verificationErr == nil {
		verificationErr = ctx.Err()
	}
	if verificationErr != nil {
		action := fmt.Sprintf("verify repository access after the lookup for tag %q in repository %q returned HTTP 404", tag, project)
		return CommitDetails{}, false, withExplanation(actionError(action, verificationErr, false), "Cannot confirm whether the tag is absent.")
	}
	// Project visibility does not establish repository-read permission for Guest
	// or custom roles. A small tag-list request checks that separate permission.
	action := fmt.Sprintf("verify tag access after the lookup for tag %q in repository %q returned HTTP 404", tag, project)
	u := c.endpoint(project, "repository", "tags")
	query := u.Query()
	query.Set("per_page", "1")
	query.Set("page", "1")
	u.RawQuery = query.Encode()
	resp, err := c.request(ctx, http.MethodGet, u, nil)
	if err != nil {
		return CommitDetails{}, false, withExplanation(actionError(action, err, false), "Cannot confirm whether the tag is absent.")
	}
	var tags []struct {
		Name   *string         `json:"name"`
		Commit json.RawMessage `json:"commit"`
	}
	decodeErr := decodeReleaseResponse(resp.Body, &tags)
	closeErr := resp.Body.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return CommitDetails{}, false, withExplanation(actionError(action, err, false), "Cannot confirm whether the tag is absent.")
	}
	if tags == nil || len(tags) > 1 || len(tags) == 1 && (tags[0].Name == nil || !validListedTag(*tags[0].Name)) {
		return CommitDetails{}, false, withExplanation(responseError(action, errors.New("missing, null, or invalid tag-access response")), "Cannot confirm whether the tag is absent.")
	}
	if err := ctx.Err(); err != nil {
		return CommitDetails{}, false, withExplanation(actionError(action, err, false), "Cannot confirm whether the tag is absent.")
	}
	if len(tags) == 1 && *tags[0].Name == tag {
		// A tag may have appeared between the original lookup and this request.
		var commit commitResponse
		if err := json.Unmarshal(tags[0].Commit, &commit); err != nil {
			return CommitDetails{}, false, withExplanation(responseError(action, err), "Cannot confirm whether the tag is absent.")
		}
		sha, err := resolvedSHA(commit.ID)
		if err != nil {
			return CommitDetails{}, false, withExplanation(responseError(action, err), "Cannot confirm whether the tag is absent.")
		}
		return commit.details(sha), true, nil
	}
	return CommitDetails{}, false, nil
}

func (c *Client) groupEndpoint(group string, segments ...string) *url.URL {
	u := *c.baseURL
	rawPath := strings.TrimRight(u.EscapedPath(), "/") + "/groups/" + url.PathEscape(group)
	for _, segment := range segments {
		rawPath += "/" + url.PathEscape(segment)
	}
	u.Path, _ = url.PathUnescape(rawPath)
	u.RawPath = rawPath
	return &u
}

func validGroupPath(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") {
			return false
		}
		for i, ch := range part {
			if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || i > 0 && (ch == '.' || ch == '-') {
				continue
			}
			return false
		}
	}
	return true
}
