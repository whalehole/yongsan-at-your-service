package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

func (a *app) listRepositories() *cobra.Command {
	var group, tag string
	var concurrency int
	var report batchReportOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories in a configured GitLab group that contain a Git tag",
		Long:  "Discover repositories directly in a configured gitlab.groups namespace and check an exact Git tag in each.\nPrint matching repositories and their tag commits, with explicit absent-tag and lookup-failure results. Save the results in a batch HTML report.\nShared projects and nested subgroups are excluded. This command does not change GitLab or download source.",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&group, "group", "", "Select one group path from gitlab.groups (prompted if missing)")
	cmd.Flags().StringVar(&tag, "tag", "", "Exact Git tag to find in the group's repositories (prompted if missing)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "Maximum simultaneous repository tag lookups")
	report.bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := report.validate(cmd); err != nil {
			return usageError(err)
		}
		if a.dryRun {
			return usageError(fmt.Errorf("--dry-run is not supported for repo list; the command already performs read-only GitLab lookups and saves a report"))
		}
		if concurrency < 1 {
			return usageError(fmt.Errorf("--concurrency must be positive"))
		}
		timeout := 2 * time.Minute
		if changed(cmd, "timeout") {
			var err error
			timeout, err = time.ParseDuration(a.timeout)
			if err != nil || timeout <= 0 {
				return usageError(fmt.Errorf("--timeout must be a positive duration, for example 2m"))
			}
		}
		cfg, err := a.load(cmd, "repo-list", true)
		if err != nil {
			return err
		}
		report.configure(cmd, cfg)
		if len(cfg.GitLab.Groups) == 0 {
			return usageError(fmt.Errorf("configure at least one group path in gitlab.groups before listing repositories"))
		}
		groups := make([]string, 0, len(cfg.GitLab.Groups))
		seen := map[string]bool{}
		for _, configured := range cfg.GitLab.Groups {
			if !seen[configured] {
				groups = append(groups, configured)
				seen[configured] = true
			}
		}
		if changed(cmd, "group") {
			if !seen[group] {
				return usageError(fmt.Errorf("--group must name a group configured in gitlab.groups; %q is not configured", group))
			}
		} else {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("missing required --group; select a group configured in gitlab.groups"))
			}
			index, err := a.prompt.choose(cmd.Context(), "Select a GitLab group from config:", groups)
			if err != nil {
				return usageError(err)
			}
			group = groups[index]
		}
		if changed(cmd, "tag") && strings.TrimSpace(tag) == "" {
			return usageError(fmt.Errorf("--tag must not be empty"))
		}
		if err := a.prompt.required(cmd.Context(), "tag", "Git tag to find in "+group, &tag, config.ValidateGitRef); err != nil {
			return usageError(err)
		}
		client, err := clientFor(cfg)
		if err != nil {
			return err
		}
		listContext, cancel := context.WithTimeout(cmd.Context(), timeout)
		projects, err := client.ListGroupProjects(listContext, group)
		cancel()
		if err != nil {
			return operationError(err)
		}
		report.selection = fmt.Sprintf("GitLab group = %q; Git tag = %q; direct repositories only", group, tag)
		jobs := make([]job, len(projects))
		for i, project := range projects {
			project := project
			jobs[i] = func(ctx context.Context) result {
				r := result{Project: project.PathWithNamespace, Operation: "repo-list", Tag: tag, Status: "failed"}
				if err := ctx.Err(); err != nil {
					r.Error = fmt.Sprintf("Tag lookup for %q in repository %q did not start: %v.", tag, r.Project, err)
					return r
				}
				progress(ctx, r, "checking whether the exact Git tag exists")
				commit, found, err := client.FindTag(ctx, project.PathWithNamespace, tag)
				if err != nil {
					r.Error = err.Error()
					return r
				}
				if found {
					r.Status, r.Commit, r.CommitTitle = "found", commit.SHA, commit.Title
					r.Message = fmt.Sprintf("Tag %q exists in repository %q at commit %s.", tag, r.Project, r.Commit)
				} else {
					r.Status = "skipped"
					r.Message = fmt.Sprintf("Tag %q does not exist in repository %q; repository access was verified.", tag, r.Project)
				}
				return r
			}
		}
		return a.executeBatch(cmd.Context(), jobs, concurrency, timeout, report, "repo-list", cfg.GitLab.URL)
	}
	return cmd
}
