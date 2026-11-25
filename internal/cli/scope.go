package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type groupOptions struct {
	group string
	all   bool
}

func (g *groupOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&g.group, "group", "", "Select projects whose full namespace exactly matches this group path")
	cmd.Flags().BoolVar(&g.all, "all-groups", false, "Select all configured groups without prompting")
}

func projectGroup(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[:i]
	}
	return "" // Numeric IDs have no namespace available from configuration.
}

func (a *app) selectBatch(cmd *cobra.Command, projects, only []string, scope groupOptions) ([]int, error) {
	indices, err := selectProjects(projects, only)
	if err != nil {
		return nil, err
	}
	if changed(cmd, "group") && scope.all {
		return nil, fmt.Errorf("--group and --all-groups cannot be used together")
	}
	filter := changed(cmd, "group")
	selected := scope.group
	if filter {
		if err := config.ValidateProject(selected + "/project"); err != nil {
			return nil, fmt.Errorf("--group must be a valid group/subgroup path")
		}
	} else if !scope.all && len(only) == 0 && a.prompt.interactive() {
		groups := []string{}
		seen := map[string]bool{}
		for _, project := range projects {
			group := projectGroup(project)
			if !seen[group] {
				groups = append(groups, group)
				seen[group] = true
			}
		}
		choices := make([]string, 0, len(groups)+1)
		for _, group := range groups {
			if group == "" {
				choices = append(choices, "Project IDs (no group path)")
			} else {
				choices = append(choices, group)
			}
		}
		choices = append(choices, "All groups")
		choice, err := a.prompt.choose(cmd.Context(), "Select a group from the configured repository list:", choices)
		if err != nil {
			return nil, err
		}
		if choice < len(groups) {
			selected = groups[choice]
			filter = true
		}
	}
	if !filter {
		return indices, nil
	}
	result := []int{}
	for _, i := range indices {
		if projectGroup(projects[i]) == selected {
			result = append(result, i)
		} else if len(only) > 0 {
			return nil, fmt.Errorf("--only project %q is outside --group %q", projects[i], selected)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no configured projects belong to group %q", selected)
	}
	return result, nil
}
