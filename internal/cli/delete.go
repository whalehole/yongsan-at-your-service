package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

func (a *app) deleteTag(batch bool) *cobra.Command {
	var report batchReportOptions
	var project, tag string
	var scope groupOptions
	var only []string
	var concurrency int
	name, description := "one", "Delete a named Git tag from one repository"
	if batch {
		name, description = "batch", "Delete the same named Git tag from tagging.repositories"
	}
	cmd := &cobra.Command{Use: name, Short: description, Args: cobra.NoArgs}
	if batch {
		report.bind(cmd)
		scope.bind(cmd)
	}
	cmd.Flags().StringVar(&tag, "tag", "", "Exact Git tag name to delete (required; never inherited from config)")
	if batch {
		cmd.Flags().StringArrayVar(&only, "only", nil, "Select a configured project identifier (repeatable)")
		cmd.Flags().IntVar(&concurrency, "concurrency", 0, "Maximum simultaneous repository jobs")
	} else {
		cmd.Flags().StringVar(&project, "project", "", "GitLab project ID or full namespace/project path")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if batch {
			if err := report.validate(cmd); err != nil {
				return usageError(err)
			}
		}
		cfg, err := a.load(cmd, "tag", batch)
		if err != nil {
			return err
		}
		if batch && changed(cmd, "concurrency") {
			cfg.Tagging.Concurrency = concurrency
		}
		settings := *cfg
		settings.Tagging.Repositories = nil
		settings.Tagging.Defaults = config.TagDefaults{}
		if err := settings.ValidateTagging(); err != nil {
			return usageError(err)
		}
		projects := []string{project}
		if batch {
			projects = nil
			seen := map[string]bool{}
			for _, entry := range cfg.Tagging.Repositories {
				if !seen[entry.Project] {
					projects = append(projects, entry.Project)
					seen[entry.Project] = true
				}
			}
			indices, err := a.selectBatch(cmd, projects, only, scope)
			if err != nil {
				return usageError(err)
			}
			selected := make([]string, 0, len(indices))
			for _, i := range indices {
				selected = append(selected, projects[i])
			}
			projects = selected
		} else {
			if err := a.prompt.project(cmd.Context(), cfg.GitLab.Groups, &project); err != nil {
				return usageError(err)
			}
			projects[0] = project
		}
		for _, p := range projects {
			if err := config.ValidateProject(p); err != nil {
				return usageError(err)
			}
		}
		if err := a.prompt.required(cmd.Context(), "tag", "Exact Git tag name to delete", &tag, config.ValidateGitRef); err != nil {
			return usageError(err)
		}
		client, err := clientFor(cfg)
		if err != nil {
			return err
		}
		jobs := make([]job, len(projects))
		for i, p := range projects {
			p := p
			jobs[i] = func(ctx context.Context) result {
				r := result{Project: p, Operation: "tag-delete", Tag: tag, Status: "failed"}
				phase := "checking tag and deleting it if present"
				if a.dryRun {
					phase = "dry run: checking tag without deleting it"
				}
				progress(ctx, r, phase)
				status, commit, err := client.DeleteTagDetails(ctx, p, tag, a.dryRun)
				r.Commit, r.CommitTitle = commit.SHA, commit.Title
				if err != nil {
					r.Error = err.Error()
				} else {
					r.Status = status
					switch status {
					case "skipped":
						r.Message = fmt.Sprintf("Tag %q does not exist in repository %q; nothing remained to delete.", tag, p)
					case "planned":
						r.Message = fmt.Sprintf("Tag %q exists in repository %q; deletion was not attempted (dry run).", tag, p)
					case "deleted":
						r.Message = fmt.Sprintf("Tag %q was deleted from repository %q.", tag, p)
					}
				}
				return r
			}
		}
		timeout, _ := time.ParseDuration(cfg.Tagging.Timeout)
		workers := 1
		if batch {
			workers = cfg.Tagging.Concurrency
			return a.executeBatch(cmd.Context(), jobs, workers, timeout, report, "tag-delete", cfg.GitLab.URL)
		}
		return a.execute(cmd.Context(), jobs, workers, timeout)
	}
	return cmd
}
