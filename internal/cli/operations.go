package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yongsan/yongsan-at-your-service/internal/builder"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
)

func (a *app) tagJob(c *gitlab.Client, repo config.TagRepository) job {
	return func(ctx context.Context) result {
		r := result{Project: repo.Project, Operation: "tag", Tag: repo.Tag, Status: "failed"}
		progress(ctx, r, "resolving source commit")
		if err := ctx.Err(); err != nil {
			r.Error = err.Error()
			return r
		}
		var sha string
		var err error
		if repo.Commit != "" {
			r.Reference = repo.Commit
			sha, err = c.ResolveCommit(ctx, repo.Project, repo.Commit)
		} else {
			r.Reference = repo.Branch
			sha, err = c.ResolveBranch(ctx, repo.Project, repo.Branch)
		}
		if err != nil {
			r.Error = err.Error()
			return r
		}
		r.Commit = sha
		if repo.Commit != "" && repo.Branch != "" {
			progress(ctx, r, "validating commit against branch "+repo.Branch)
			if err := c.ValidateBranch(ctx, repo.Project, sha, repo.Branch); err != nil {
				r.Error = err.Error()
				return r
			}
		}
		phase := "checking existing tag and creating tag if absent"
		if a.dryRun {
			phase = "dry run: checking tag without creating it"
		}
		progress(ctx, r, phase)
		r.Status, err = c.EnsureTag(ctx, repo.Project, sha, repo.Tag, repo.Message, a.dryRun)
		if err != nil {
			r.Status = "failed"
			r.Error = err.Error()
		}
		return r
	}
}

func boolValue(value *bool) bool { return value != nil && *value }

func (a *app) buildJob(c *gitlab.Client, repo config.BuildRepository) job {
	return func(ctx context.Context) result {
		r := result{Project: repo.Project, Operation: "build", Image: repo.Image, Status: "failed"}
		progress(ctx, r, "resolving source commit")
		if err := ctx.Err(); err != nil {
			r.Error = err.Error()
			return r
		}
		var sha string
		var err error
		if repo.Source.Commit != "" {
			r.Reference = repo.Source.Commit
			sha, err = c.ResolveCommit(ctx, repo.Project, repo.Source.Commit)
		} else {
			r.Reference = repo.Source.Tag
			sha, err = c.ResolveTag(ctx, repo.Project, repo.Source.Tag)
		}
		if err != nil {
			r.Error = err.Error()
			return r
		}
		r.Commit = sha
		if r.Image == "" {
			name := repo.Project
			if index := strings.LastIndex(name, "/"); index >= 0 {
				name = name[index+1:]
			} else {
				progress(ctx, r, "resolving repository name for default image")
				name, err = c.ProjectPath(ctx, repo.Project)
				if err != nil {
					r.Error = err.Error()
					return r
				}
			}
			tag := repo.Source.Tag
			if tag == "" {
				tag = sha
			}
			r.Image, err = prefixedImage(repo.ImagePrefix, strings.ToLower(name)+":"+tag)
			if err != nil {
				r.Error = "cannot generate default image name; set --image (or image in config) explicitly: " + err.Error()
				return r
			}
			progress(ctx, r, "resolved default output image")
		}
		a.logMu.Lock()
		if a.buildTargets == nil {
			a.buildTargets = map[string]bool{}
		}
		duplicate := a.buildTargets[r.Image]
		a.buildTargets[r.Image] = true
		a.logMu.Unlock()
		if duplicate {
			r.Error = "another selected build uses the same output image " + r.Image + "; set distinct image overrides"
			return r
		}
		if a.dryRun {
			r.Status = "planned"
			return r
		}
		logs := &lineWriter{sinkMu: &a.logMu, out: a.options.Err, prefix: repo.Project + " -> " + r.Image}
		b := &builder.Builder{Source: c, Output: logs, Progress: func(phase string) { progress(ctx, r, phase) }}
		err = b.Build(ctx, builder.Options{
			Project: repo.Project, Commit: sha, Context: repo.Context,
			Dockerfile: repo.Dockerfile, Image: r.Image, BuildArgs: repo.BuildArgs,
			NoCache: boolValue(repo.NoCache), Pull: boolValue(repo.Pull),
		})
		closeErr := logs.Close()
		if err != nil {
			r.Error = err.Error()
			return r
		}
		if closeErr != nil {
			r.Error = closeErr.Error()
			return r
		}
		r.Status = "built"
		return r
	}
}

func (a *app) execute(ctx context.Context, jobs []job, concurrency int, timeout time.Duration) error {
	a.logMu.Lock()
	a.buildTargets = map[string]bool{}
	a.logMu.Unlock()
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	jobs, progressError := a.progressJobs(jobs, concurrency)
	results := runJobs(ctx, jobs, concurrency, timeout)
	if err := writeResults(a.options.Out, a.format, results); err != nil {
		return operationError(err)
	}
	if err := progressError(); err != nil {
		return operationError(err)
	}
	failures := 0
	for _, r := range results {
		if r.Status == "failed" {
			failures++
		}
	}
	if failures > 0 {
		return operationError(fmt.Errorf("%d repository operation(s) failed", failures))
	}
	return nil
}
