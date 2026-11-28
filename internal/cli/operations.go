package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yongsan/yongsan-at-your-service/internal/builder"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
)

func (a *app) tagJob(c *gitlab.Client, repo config.TagRepository) job {
	return func(ctx context.Context) result {
		reference := repo.Commit
		if reference == "" {
			reference = repo.Branch
		}
		r := result{Project: repo.Project, Operation: "tag", Reference: reference, Tag: repo.Tag, Status: "failed"}
		progress(ctx, r, "resolving source commit")
		if err := ctx.Err(); err != nil {
			r.Error = fmt.Sprintf("Tag creation for %q in repository %q did not start: %v.", repo.Tag, repo.Project, err)
			return r
		}
		var commit gitlab.CommitDetails
		var err error
		if repo.Commit != "" {
			commit, err = c.ResolveCommitDetails(ctx, repo.Project, repo.Commit)
		} else {
			commit, err = c.ResolveBranchDetails(ctx, repo.Project, repo.Branch)
		}
		if err != nil {
			r.Error = err.Error()
			return r
		}
		sha := commit.SHA
		r.Commit, r.CommitTitle = sha, commit.Title
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
		} else {
			switch r.Status {
			case "created":
				r.Message = fmt.Sprintf("Tag %q was created at commit %s in repository %q.", r.Tag, r.Commit, r.Project)
			case "skipped":
				r.Message = fmt.Sprintf("Tag %q points to the requested commit in repository %q; no further change was needed.", r.Tag, r.Project)
			case "planned":
				r.Message = fmt.Sprintf("Tag %q would be created in repository %q; no tag was created (dry run).", r.Tag, r.Project)
			}
		}
		return r
	}
}

func boolValue(value *bool) bool { return value != nil && *value }

func (a *app) buildJob(c *gitlab.Client, repo config.BuildRepository, save imageSaveOptions) job {
	if save.enabled && save.run == nil {
		save.run = &imageSaveRun{parent: save.directory, started: time.Now().UTC()}
	}
	return func(ctx context.Context) result {
		reference := repo.Source.Commit
		if reference == "" {
			reference = repo.Source.Tag
		}
		r := result{Project: repo.Project, Operation: "build", Reference: reference, Image: repo.Image, Status: "failed", BuildStatus: "not-run"}
		if save.enabled {
			r.SaveStatus = "not-run"
		}
		progress(ctx, r, "resolving source commit")
		if err := ctx.Err(); err != nil {
			r.Error = fmt.Sprintf("Build for repository %q did not start: %v.", repo.Project, err)
			return r
		}
		var commit gitlab.CommitDetails
		var err error
		if repo.Source.Commit != "" {
			commit, err = c.ResolveCommitDetails(ctx, repo.Project, repo.Source.Commit)
		} else {
			commit, err = c.ResolveTagDetails(ctx, repo.Project, repo.Source.Tag)
		}
		if err != nil {
			r.Error = err.Error()
			return r
		}
		sha := commit.SHA
		r.Commit, r.CommitTitle = sha, commit.Title
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
		if save.enabled {
			name, err := builder.ArchiveName(r.Image)
			if err == nil {
				err = save.run.reserveName(name)
			}
			if err != nil {
				r.Error = err.Error()
				return r
			}
		}
		if a.dryRun {
			r.Status = "planned"
			r.BuildStatus = "planned"
			r.Message = fmt.Sprintf("Image %q would be built from repository %q; Docker was not executed (dry run).", r.Image, r.Project)
			if save.enabled {
				r.SaveStatus = "planned"
			}
			return r
		}
		logs := &lineWriter{sinkMu: &a.logMu, out: a.options.Err, prefix: repo.Project + " -> " + r.Image}
		b := &builder.Builder{Source: c, Output: logs, Progress: func(phase string) { progress(ctx, r, phase) }}
		r.BuildStatus = "failed"
		err = b.Build(ctx, builder.Options{
			Project: repo.Project, Commit: sha, Context: repo.Context,
			Dockerfile: repo.Dockerfile, Image: r.Image, BuildArgs: repo.BuildArgs,
			NoCache: boolValue(repo.NoCache), Pull: boolValue(repo.Pull),
		})
		closeErr := logs.Close()
		if err != nil {
			r.Error = dockerFailureMessage(err, r.Image, "build")
			return r
		}
		r.BuildStatus = "built"
		if closeErr != nil {
			r.Error = fmt.Sprintf("Image %q was built, but its log output could not be written: %v.", r.Image, closeErr)
			if save.enabled {
				r.Error += " Image saving was not attempted."
			}
			return r
		}
		if save.enabled {
			r.SaveStatus = "failed"
			directory, saveErr := save.run.destination()
			var archive string
			if saveErr == nil {
				archive, saveErr = b.Save(ctx, r.Image, directory)
			}
			closeErr := logs.Close()
			if saveErr != nil {
				r.SaveError = dockerFailureMessage(saveErr, r.Image, "save")
				r.Error = r.SaveError + " The built image remains in Docker; no new completed archive was produced."
				return r
			}
			r.Archive, r.SaveStatus = archive, "saved"
			progress(ctx, r, "saved Docker image archive: "+archive)
			if closeErr != nil {
				r.Error = fmt.Sprintf("Image %q was built and saved to %q, but its log output could not be written: %v.", r.Image, r.Archive, closeErr)
				return r
			}
		}
		r.Status = "built"
		r.Message = fmt.Sprintf("Image %q was built in the selected Docker daemon.", r.Image)
		if save.enabled {
			r.Message = fmt.Sprintf("Image %q was built and saved to %q.", r.Image, r.Archive)
		}
		return r
	}
}

func (a *app) execute(ctx context.Context, jobs []job, concurrency int, timeout time.Duration) error {
	return a.executeWithReport(ctx, jobs, concurrency, timeout, nil)
}

func (a *app) executeWithReport(ctx context.Context, jobs []job, concurrency int, timeout time.Duration, report *batchReportRun) error {
	a.logMu.Lock()
	a.buildTargets = map[string]bool{}
	a.logMu.Unlock()
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	if concurrency < 1 && len(jobs) > 0 {
		concurrency = 1
	}
	if report != nil {
		report.data.StartedAt = time.Now()
		report.data.Concurrency = concurrency
	}
	jobs, progressError := a.progressJobs(jobs, concurrency)
	results := runJobs(ctx, jobs, concurrency, timeout)
	if saveRun, ok := ctx.Value(imageSaveRunKey{}).(*imageSaveRun); ok {
		saveRun.annotate(results)
	}
	var reportErr error
	if report != nil {
		report.data.FinishedAt = time.Now()
		report.data.Canceled = ctx.Err() != nil
		report.data.Results = results
		reportErr = report.save()
		if reportErr == nil {
			_, reportErr = fmt.Fprintf(a.options.Err, "Batch report: %s\n", report.path)
		}
	}
	outputErr := writeResults(a.options.Out, a.format, results)
	progressErr := progressError()
	if report != nil && report.saved && report.autoOpen {
		if err := openBatchReport(report.path); err != nil {
			// Opening is a convenience; preserve the batch result and saved
			// report when a browser or desktop session is unavailable.
			fmt.Fprintf(a.options.Err, "Warning: could not open batch report %q: %v\n", report.path, err)
		}
	}
	failures := 0
	for _, r := range results {
		if r.Status == "failed" {
			failures++
		}
	}
	var failureErr error
	if failures > 0 {
		failureErr = fmt.Errorf("%d repository operation(s) failed", failures)
	}
	if err := errors.Join(failureErr, reportErr, outputErr, progressErr); err != nil {
		return operationError(err)
	}
	return nil
}
