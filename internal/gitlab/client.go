// Package gitlab provides the small subset of GitLab's REST API used by yas.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client authenticates requests to one GitLab installation. The caller controls
// operation deadlines through the context supplied to each method.
type Client struct {
	baseURL *url.URL
	token   string

	// HTTPClient can customize the transport or timeouts. Redirects are always
	// rejected so that the PRIVATE-TOKEN header cannot reach another endpoint.
	HTTPClient *http.Client
}

// HTTPError identifies an unsuccessful GitLab response. Response bodies are
// deliberately excluded because they can contain confidential repository data.
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GitLab %s %s returned HTTP %d (%s)", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
}

// IsNotFound reports whether err is a GitLab HTTP 404 response.
func IsNotFound(err error) bool {
	var responseErr *HTTPError
	return errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusNotFound
}

// New accepts a GitLab installation URL, optionally including its deployment
// prefix or /api/v4 suffix. Credentials must be supplied separately from the URL.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("invalid GitLab URL")
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Opaque != "" {
		return nil, errors.New("GitLab URL must be an absolute HTTP or HTTPS URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("GitLab URL must not contain credentials, a query, or a fragment")
	}
	for _, component := range strings.Split(u.Path, "/") {
		if component == "." || component == ".." {
			return nil, errors.New("GitLab URL must not contain relative path components")
		}
	}
	rawPath := strings.TrimRight(u.EscapedPath(), "/")
	if !strings.HasSuffix(rawPath, "/api/v4") {
		rawPath += "/api/v4"
	}
	u.Path, err = url.PathUnescape(rawPath)
	if err != nil {
		return nil, errors.New("invalid GitLab URL path")
	}
	u.RawPath = rawPath
	return &Client{baseURL: u, token: token, HTTPClient: &http.Client{}}, nil
}

type commitResponse struct {
	ID string `json:"id"`
}

// ProjectPath returns the repository slug, including when addressed by ID.
func (c *Client) ProjectPath(ctx context.Context, project string) (string, error) {
	var info struct {
		Path string `json:"path"`
	}
	if err := c.getJSON(ctx, c.endpoint(project), &info); err != nil {
		return "", fmt.Errorf("resolve repository name: %w", err)
	}
	if info.Path == "" || strings.Contains(info.Path, "/") {
		return "", errors.New("GitLab returned an invalid repository name")
	}
	return info.Path, nil
}

type referenceResponse struct {
	Commit commitResponse `json:"commit"`
}

// ResolveCommit verifies a commit and returns its full SHA.
func (c *Client) ResolveCommit(ctx context.Context, project, sha string) (string, error) {
	var commit commitResponse
	if err := c.getJSON(ctx, c.endpoint(project, "repository", "commits", sha), &commit); err != nil {
		if len(sha) < 40 && IsNotFound(err) {
			return c.resolveCommitPrefix(ctx, project, sha)
		}
		return "", fmt.Errorf("resolve commit %q: %w", sha, err)
	}
	full, err := resolvedSHA(commit.ID)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(full, strings.ToLower(sha)) {
		return "", fmt.Errorf("GitLab resolved %q to a different commit; use a longer or full commit hash", sha)
	}
	return full, nil
}

// Some GitLab installations reject an otherwise valid abbreviated revision.
// Search all visible commit metadata, never source files or cloned Git history.
// Exhaust the listing before accepting a match so a later match cannot be
// silently ignored. Both pagination and the caller's deadline bound the work.
func (c *Client) resolveCommitPrefix(ctx context.Context, project, prefix string) (string, error) {
	const pageSize = 100
	const maxPages = 1000
	match := ""
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("resolve prefix %q: search incomplete: %w", prefix, err)
		}
		u := c.endpoint(project, "repository", "commits")
		query := u.Query()
		query.Set("all", "true")
		query.Set("per_page", strconv.Itoa(pageSize))
		query.Set("page", strconv.Itoa(page))
		u.RawQuery = query.Encode()
		var commits []commitResponse
		if err := c.getJSON(ctx, u, &commits); err != nil {
			return "", fmt.Errorf("resolve prefix %q: could not complete commit search; use a longer hash: %w", prefix, err)
		}
		for _, commit := range commits {
			full, err := resolvedSHA(commit.ID)
			if err != nil {
				return "", err
			}
			if !strings.HasPrefix(full, strings.ToLower(prefix)) {
				continue
			}
			if match != "" && match != full {
				return "", fmt.Errorf("commit prefix %q matches multiple commits; use a longer hash", prefix)
			}
			match = full
		}
		if len(commits) < pageSize {
			if match == "" {
				return "", fmt.Errorf("commit prefix %q was not found in GitLab's commit listing; ensure the commit is pushed to this project", prefix)
			}
			// Verify the selected full SHA still exists before the caller can write.
			return c.ResolveCommit(ctx, project, match)
		}
	}
	return "", fmt.Errorf("commit prefix %q: search exceeded %d commits; uniqueness was not established, use a longer hash", prefix, pageSize*maxPages)
}

// ResolveBranch returns the current commit SHA of a branch.
func (c *Client) ResolveBranch(ctx context.Context, project, branch string) (string, error) {
	var ref referenceResponse
	if err := c.getJSON(ctx, c.endpoint(project, "repository", "branches", branch), &ref); err != nil {
		return "", fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	return resolvedSHA(ref.Commit.ID)
}

// ResolveTag returns the commit SHA of a lightweight or annotated tag.
func (c *Client) ResolveTag(ctx context.Context, project, tag string) (string, error) {
	return c.tagCommit(ctx, project, tag)
}

// ValidateBranch checks reachability: commit may be the branch tip or any of its
// ancestors. The branch tip is resolved once before comparing the two commits.
func (c *Client) ValidateBranch(ctx context.Context, project, commit, branch string) error {
	tip, err := c.ResolveBranch(ctx, project, branch)
	if err != nil {
		return err
	}
	if strings.EqualFold(commit, tip) {
		return nil
	}
	u := c.endpoint(project, "repository", "merge_base")
	query := u.Query()
	query.Add("refs[]", commit)
	query.Add("refs[]", tip)
	u.RawQuery = query.Encode()
	var ancestor commitResponse
	if err := c.getJSON(ctx, u, &ancestor); err != nil {
		if IsNotFound(err) {
			return fmt.Errorf("commit %s is not reachable from branch %q", commit, branch)
		}
		return fmt.Errorf("validate branch %q: %w", branch, err)
	}
	if !strings.EqualFold(ancestor.ID, commit) {
		return fmt.Errorf("commit %s is not reachable from branch %q", commit, branch)
	}
	return nil
}

// EnsureTag creates a tag without moving existing tags. A tag already targeting
// sha is skipped, including annotated tags whose tag-object target differs from
// their commit ID. dryRun performs the same checks but never creates a tag.
func (c *Client) EnsureTag(ctx context.Context, project, sha, tag, message string, dryRun bool) (string, error) {
	current, err := c.tagCommit(ctx, project, tag)
	if err == nil {
		return existingTagStatus(tag, sha, current)
	}
	if !IsNotFound(err) {
		return "", err
	}
	if dryRun {
		return "planned", nil
	}
	form := url.Values{"tag_name": {tag}, "ref": {sha}}
	if message != "" {
		form.Set("message", message)
	}
	resp, createErr := c.request(ctx, http.MethodPost, c.endpoint(project, "repository", "tags"), []byte(form.Encode()))
	if createErr == nil {
		discardAndClose(resp.Body)
		return "created", nil
	}
	// A competing operation may have created the tag, or the server may have
	// committed our request before a connection failure. Re-read instead of
	// blindly replaying a mutating request.
	var responseErr *HTTPError
	// A final explicit rate-limit response means the create request was rejected;
	// there is no ambiguous write to reconcile, and an immediate GET would ignore
	// that response's requested quiet period.
	rateLimited := errors.As(createErr, &responseErr) && responseErr.StatusCode == http.StatusTooManyRequests
	if ctx.Err() == nil && !rateLimited {
		if current, readErr := c.tagCommit(ctx, project, tag); readErr == nil {
			return existingTagStatus(tag, sha, current)
		}
	}
	return "", fmt.Errorf("create tag %q: %w", tag, createErr)
}

func existingTagStatus(tag, wanted, current string) (string, error) {
	if strings.EqualFold(wanted, current) {
		return "skipped", nil
	}
	return "", fmt.Errorf("tag %q already points to %s, expected %s", tag, current, wanted)
}

// DeleteTag removes exactly the named tag, regardless of its target commit.
// Missing tags are successful skips, but inaccessible/missing projects are errors.
// Ambiguous transport errors are not retried: a concurrently recreated tag must
// not be removed by replaying a deletion whose first outcome is unknown.
func (c *Client) DeleteTag(ctx context.Context, project, tag string, dryRun bool) (status, commit string, err error) {
	commit, err = c.tagCommit(ctx, project, tag)
	if IsNotFound(err) {
		return c.missingDeletedTag(ctx, project)
	}
	if err != nil {
		return "", "", err
	}
	if dryRun {
		return "planned", commit, nil
	}
	resp, err := c.request(ctx, http.MethodDelete, c.endpoint(project, "repository", "tags", tag), nil)
	if IsNotFound(err) {
		status, _, checkErr := c.missingDeletedTag(ctx, project)
		return status, commit, checkErr
	}
	if err != nil {
		return "", commit, fmt.Errorf("delete tag %q: %w", tag, err)
	}
	discardAndClose(resp.Body)
	return "deleted", commit, nil
}

func (c *Client) missingDeletedTag(ctx context.Context, project string) (string, string, error) {
	// A tag 404 can conceal a missing project or insufficient permissions.
	var info struct {
		ID int64 `json:"id"`
	}
	if err := c.getJSON(ctx, c.endpoint(project), &info); err != nil {
		return "", "", fmt.Errorf("verify project before skipping absent tag: %w", err)
	}
	if info.ID <= 0 {
		return "", "", errors.New("GitLab returned an invalid project while checking an absent tag")
	}
	return "skipped", "", nil
}

// Archive streams a source snapshot without Git history. path is a repository
// subdirectory, or "."/empty for the repository root. The caller closes the body.
func (c *Client) Archive(ctx context.Context, project, sha, path string) (io.ReadCloser, error) {
	u := c.endpoint(project, "repository", "archive.tar.gz")
	query := u.Query()
	query.Set("sha", sha)
	query.Set("include_lfs_blobs", "true")
	if path != "" && path != "." {
		query.Set("path", path)
	}
	u.RawQuery = query.Encode()
	resp, err := c.request(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("download source archive: %w", err)
	}
	return resp.Body, nil
}

func (c *Client) tagCommit(ctx context.Context, project, tag string) (string, error) {
	var ref referenceResponse
	if err := c.getJSON(ctx, c.endpoint(project, "repository", "tags", tag), &ref); err != nil {
		return "", fmt.Errorf("resolve tag %q: %w", tag, err)
	}
	return resolvedSHA(ref.Commit.ID)
}

func resolvedSHA(sha string) (string, error) {
	if len(sha) != 40 && len(sha) != 64 {
		return "", errors.New("GitLab returned a missing or invalid commit SHA")
	}
	for _, ch := range sha {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return "", errors.New("GitLab returned an invalid commit SHA")
		}
	}
	return strings.ToLower(sha), nil
}

func (c *Client) endpoint(project string, segments ...string) *url.URL {
	u := *c.baseURL
	rawPath := strings.TrimRight(u.EscapedPath(), "/") + "/projects/" + url.PathEscape(project)
	for _, segment := range segments {
		rawPath += "/" + url.PathEscape(segment)
	}
	u.Path, _ = url.PathUnescape(rawPath)
	u.RawPath = rawPath
	return &u
}

func (c *Client) getJSON(ctx context.Context, endpoint *url.URL, value any) error {
	resp, err := c.request(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(value); err != nil {
		return fmt.Errorf("decode GitLab response: %w", err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method string, endpoint *url.URL, body []byte) (*http.Response, error) {
	transportClient := c.HTTPClient
	if transportClient == nil {
		transportClient = http.DefaultClient
	}
	client := *transportClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	const retries = 3
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("construct GitLab request: %w", err)
		}
		if c.token != "" {
			req.Header.Set("PRIVATE-TOKEN", c.token)
		}
		req.Header.Set("User-Agent", "yas")
		req.Header.Set("Accept", "application/json")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		if err != nil {
			if method == http.MethodGet && attempt < retries && ctx.Err() == nil {
				if err := wait(ctx, retryDelay("", attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("GitLab request failed: %w", err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		responseErr := &HTTPError{Method: method, Path: endpoint.EscapedPath(), StatusCode: resp.StatusCode}
		delay := retryDelay(resp.Header.Get("Retry-After"), attempt)
		discardAndClose(resp.Body)
		// GETs are idempotent. A POST may only be replayed after an explicit 429,
		// which rejects the create operation; transport failures and 5xx responses
		// can conceal a completed write and must be reconciled by EnsureTag.
		canRetry := (method == http.MethodGet && retryable(resp.StatusCode)) ||
			((method == http.MethodPost || method == http.MethodDelete) && resp.StatusCode == http.StatusTooManyRequests)
		if attempt >= retries || !canRetry {
			return nil, responseErr
		}
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds >= 0 {
		const maximum = time.Duration(1<<63 - 1)
		if seconds > int64(maximum/time.Second) {
			return maximum
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(header); err == nil {
		if delay := time.Until(deadline); delay > 0 {
			return delay
		}
		return 0
	}
	return (100 * time.Millisecond) << attempt
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func discardAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}
