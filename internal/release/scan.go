// Package release prepares release notes from existing GitLab references.
package release

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
)

// Source is the read-only GitLab access needed by a component scan.
type Source interface {
	ResolveTag(context.Context, string, string) (string, error)
	ResolveBranch(context.Context, string, string) (string, error)
	ResolveCommit(context.Context, string, string) (string, error)
	CommitsBetween(context.Context, string, string, string) (gitlab.CommitRange, error)
	ListTags(context.Context, string) ([]string, error)
}

type Options struct {
	From, To, ToBranch, ToCommit, Version string
	Previous                              bool
}

type Component struct {
	Project    string   `yaml:"project" json:"project"`
	FromRef    string   `yaml:"from_ref,omitempty" json:"from_ref,omitempty"`
	ToRef      string   `yaml:"to_ref,omitempty" json:"to_ref,omitempty"`
	FromCommit string   `yaml:"from_commit,omitempty" json:"from_commit,omitempty"`
	ToCommit   string   `yaml:"to_commit,omitempty" json:"to_commit,omitempty"`
	ToKind     string   `yaml:"to_kind,omitempty" json:"to_kind,omitempty"`
	Status     string   `yaml:"status" json:"status"`
	Error      string   `yaml:"error,omitempty" json:"error,omitempty"`
	Commits    []Commit `yaml:"commits" json:"commits"`
	Duration   string   `yaml:"duration,omitempty" json:"duration,omitempty"`
}

type Commit struct {
	SHA   string `yaml:"sha" json:"sha"`
	Title string `yaml:"title" json:"title"`
	// Keep unfilled external IDs visible as null placeholders in draft output.
	ChangeRequest *int64 `yaml:"change_request" json:"change_request"`
	Incident      *int64 `yaml:"incident" json:"incident"`
	Message       string `yaml:"message,omitempty" json:"message,omitempty"`
	Author        string `yaml:"author,omitempty" json:"author,omitempty"`
	CommittedAt   string `yaml:"committed_at,omitempty" json:"committed_at,omitempty"`
	URL           string `yaml:"url,omitempty" json:"url,omitempty"`
}

// ValidateOptions checks scan selectors without contacting GitLab.
func ValidateOptions(options Options) error {
	targets := 0
	for _, ref := range []string{options.To, options.ToBranch, options.ToCommit} {
		if ref != "" {
			targets++
		}
	}
	if targets != 1 {
		return errors.New("select exactly one target tag, branch, or commit")
	}
	if (options.From != "") == options.Previous {
		return errors.New("supply exactly one baseline: an explicit from tag or previous-tag selection")
	}
	if options.Previous {
		if options.To == "" {
			return errors.New("automatic previous-tag selection requires a target Git tag; release version metadata cannot identify a baseline")
		}
		if _, err := stableTarget(options.To); err != nil {
			return err
		}
	}
	return nil
}

// Scan resolves each reference once, then finds commits reachable from the
// pinned target that are not reachable from the pinned baseline.
// Version is release metadata owned by the caller; it never substitutes for To.
func Scan(ctx context.Context, source Source, project string, options Options) Component {
	component := Component{
		Project: project, FromRef: options.From, Status: "failed", Commits: []Commit{},
	}
	fail := func(phase string, err error) Component {
		var operation *gitlab.OperationError
		if errors.As(err, &operation) {
			component.Error = err.Error()
			return component
		}
		component.Error = fmt.Sprintf("%s for repository %q: %v", phase, project, err)
		return component
	}
	var resolveTarget func(context.Context, string, string) (string, error)
	for _, target := range []struct {
		kind, ref string
	}{
		{kind: "tag", ref: options.To},
		{kind: "branch", ref: options.ToBranch},
		{kind: "commit", ref: options.ToCommit},
	} {
		if target.ref != "" {
			component.ToKind, component.ToRef = target.kind, target.ref
		}
	}
	if err := ctx.Err(); err != nil {
		return fail("start release scan", err)
	}
	if err := ValidateOptions(options); err != nil {
		return fail("validate scan options", err)
	}
	if source == nil {
		return fail("start release scan", errors.New("GitLab source is unavailable"))
	}
	switch component.ToKind {
	case "tag":
		resolveTarget = source.ResolveTag
	case "branch":
		resolveTarget = source.ResolveBranch
	case "commit":
		resolveTarget = source.ResolveCommit
	}
	sha, err := resolveTarget(ctx, project, component.ToRef)
	if err != nil {
		return fail(fmt.Sprintf("resolve target %s %q", component.ToKind, component.ToRef), err)
	}
	if !fullSHA(sha) {
		return fail(fmt.Sprintf("resolve target %s %q", component.ToKind, component.ToRef), errors.New("GitLab source returned an invalid full commit SHA"))
	}
	component.ToCommit = strings.ToLower(sha)
	if err := ctx.Err(); err != nil {
		return fail("continue release scan after resolving target", err)
	}
	if options.Previous {
		tags, err := source.ListTags(ctx, project)
		if err != nil {
			return fail("list existing tags for previous-tag selection", err)
		}
		if err := ctx.Err(); err != nil {
			return fail("select previous tag", err)
		}
		component.FromRef, err = previousTag(options.To, tags)
		if err != nil {
			return fail("select previous tag", err)
		}
	}
	if component.ToKind == "tag" && component.FromRef == component.ToRef {
		// A tag supplied as both endpoints is one reference, even if somebody
		// moves it while this scan runs. Reuse its pinned resolution.
		component.FromCommit, component.Status = component.ToCommit, "unchanged"
		return component
	}
	sha, err = source.ResolveTag(ctx, project, component.FromRef)
	if err != nil {
		return fail(fmt.Sprintf("resolve baseline tag %q", component.FromRef), err)
	}
	if !fullSHA(sha) {
		return fail(fmt.Sprintf("resolve baseline tag %q", component.FromRef), errors.New("GitLab source returned an invalid full commit SHA"))
	}
	component.FromCommit = strings.ToLower(sha)
	if err := ctx.Err(); err != nil {
		return fail("find new release commits", err)
	}
	if component.FromCommit == component.ToCommit {
		component.Status = "unchanged"
		return component
	}
	commitRange, err := source.CommitsBetween(ctx, project, component.FromCommit, component.ToCommit)
	if err != nil {
		return fail(fmt.Sprintf("find new release commits %s..%s", component.FromCommit, component.ToCommit), err)
	}
	if err := ctx.Err(); err != nil {
		return fail("read release commits", err)
	}
	// Reject incomplete provenance before adding any notes, including notes from
	// otherwise valid commits in the same response.
	for _, commit := range commitRange.Commits {
		if !fullSHA(commit.SHA) {
			return fail("read release commits", errors.New("GitLab source returned a commit without a valid full SHA; release notes were not generated"))
		}
	}
	for _, commit := range commitRange.Commits {
		component.Commits = append(component.Commits, Commit{
			SHA: strings.ToLower(commit.SHA), Title: commit.Title, Message: commit.Message,
			Author: commit.Author, CommittedAt: commit.CommittedAt, URL: commit.URL,
		})
	}
	component.Status = "unchanged"
	if len(component.Commits) > 0 {
		component.Status = "changed"
	}
	return component
}

func fullSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, char := range sha {
		if !('0' <= char && char <= '9' || 'a' <= char && char <= 'f' || 'A' <= char && char <= 'F') {
			return false
		}
	}
	return true
}
