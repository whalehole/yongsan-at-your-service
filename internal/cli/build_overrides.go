package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

func (a *app) buildContext(cmd *cobra.Command, options *config.BuildOptions) error {
	if changed(cmd, "context") || !a.prompt.interactive() {
		return nil
	}
	for {
		value, err := a.prompt.line(cmd.Context(), "Build context ["+options.Context+"] (Enter keeps default)")
		if err != nil {
			return err
		}
		if value == "" {
			return nil
		}
		if err := config.ValidateRelativePath(value); err != nil {
			fmt.Fprintln(a.prompt.out, err)
			continue
		}
		options.Context = value
		return nil
	}
}

func (a *app) imageOverride(cmd *cobra.Command, image *string, prefix, project string) error {
	if *image != "" || !a.prompt.interactive() {
		return nil
	}
	for {
		value, err := a.prompt.line(cmd.Context(), "Output Docker image for "+project+" (Enter = repository name with source tag/commit)")
		if err != nil {
			return err
		}
		if value == "" {
			return nil
		}
		if _, err := prefixedImage(prefix, value); err != nil {
			fmt.Fprintln(a.prompt.out, err)
			continue
		}
		*image = value
		return nil
	}
}

func batchImageOverrides(values, projects []string) (map[string]string, error) {
	selected := map[string]bool{}
	for _, p := range projects {
		selected[p] = true
	}
	result := map[string]string{}
	for _, value := range values {
		project, image, ok := strings.Cut(value, "=")
		if !ok || !selected[project] {
			return nil, fmt.Errorf("--image requires SELECTED_PROJECT=NAME:TAG; unknown or unselected project in %q", value)
		}
		if err := config.ValidateImage(image); err != nil {
			return nil, err
		}
		if _, exists := result[project]; exists {
			return nil, fmt.Errorf("duplicate --image override for %q", project)
		}
		result[project] = image
	}
	return result, nil
}
