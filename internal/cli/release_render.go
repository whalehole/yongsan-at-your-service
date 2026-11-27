package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/release"
)

var releaseGroupingChoices = []struct {
	key   release.GroupBy
	label string
}{
	{release.GroupRepository, "Repository"},
	{release.GroupChangeRequest, "Change Request"},
	{release.GroupIncident, "Incident"},
	{release.GroupRemarks, "Remarks (exact matches)"},
	{release.GroupAuthor, "Author"},
}

func (a *app) releaseRender() *cobra.Command {
	var input, output, groupBy string
	cmd := &cobra.Command{
		Use: "render", Short: "Generate standalone HTML release notes from edited release YAML",
		Long: "Generate HTML release notes from an edited release-scan YAML file.\nThis local operation does not require yas.yaml, a GitLab token, or network access.",
		Args: cobra.NoArgs,
	}
	f := cmd.Flags()
	f.StringVar(&input, "input", "", "Release notes .yaml/.yml file to render")
	f.StringVar(&output, "output", "", "New .html file (default: input filename with .html extension; never overwrites)")
	f.StringVar(&groupBy, "group-by", string(release.GroupRepository), "Initial grouping: repository, change-request, incident, remarks, or author")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.format != "text" && a.format != "json" {
			return usageError(fmt.Errorf("--format must be text or json"))
		}
		if a.dryRun {
			return usageError(fmt.Errorf("release render is local; omit --dry-run to generate the HTML file"))
		}
		if changed(cmd, "group-by") && strings.TrimSpace(groupBy) == "" {
			return usageError(fmt.Errorf("--group-by must be repository, change-request, incident, remarks, or author"))
		}
		grouping, err := release.ParseGroupBy(groupBy)
		if err != nil {
			return usageError(err)
		}
		validateInput := func(path string) error {
			if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
				return fmt.Errorf("--input must name a .yaml or .yml file")
			}
			return nil
		}
		if err := a.prompt.required(cmd.Context(), "input", "Release notes YAML file", &input, validateInput); err != nil {
			return usageError(err)
		}
		if err := cmd.Context().Err(); err != nil {
			return operationError(err)
		}
		inputPath, err := filepath.Abs(input)
		if err != nil {
			return usageError(fmt.Errorf("resolve release notes input: %w", err))
		}
		file, err := os.Open(inputPath)
		if err != nil {
			return usageError(fmt.Errorf("open release notes YAML %q: %w", inputPath, err))
		}
		document, readErr := release.ReadDocument(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return usageError(fmt.Errorf("read release notes YAML %q: %w", inputPath, err))
		}
		if !changed(cmd, "group-by") && a.prompt.interactive() {
			grouping, err = a.promptReleaseGrouping(cmd.Context())
			if err != nil {
				return usageError(err)
			}
		}
		if changed(cmd, "output") && strings.TrimSpace(output) == "" {
			return usageError(fmt.Errorf("--output must name a new .html file"))
		}
		if output == "" {
			output = strings.TrimSuffix(input, filepath.Ext(input)) + ".html"
			if a.prompt.interactive() {
				answer, err := a.prompt.line(cmd.Context(), "HTML release notes file ["+output+"] (Enter keeps default)")
				if err != nil {
					return usageError(err)
				}
				if answer != "" {
					output = answer
				}
			}
		}
		if strings.ToLower(filepath.Ext(output)) != ".html" {
			return usageError(fmt.Errorf("--output must have a .html extension"))
		}
		var html bytes.Buffer
		if err := release.RenderHTML(&html, document, grouping); err != nil {
			return operationError(fmt.Errorf("render release notes: %w", err))
		}
		if err := cmd.Context().Err(); err != nil {
			return operationError(err)
		}
		destination, err := reserveReleaseOutput(output)
		if err != nil {
			return operationError(err)
		}
		saved := false
		defer func() {
			destination.Close()
			if !saved {
				os.Remove(destination.Name())
			}
		}()
		_, writeErr := destination.Write(html.Bytes())
		var syncErr error
		if writeErr == nil {
			syncErr = destination.Sync()
		}
		closeErr = destination.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return operationError(fmt.Errorf("save HTML release notes %q: %w", destination.Name(), err))
		}
		saved = true
		summary := struct {
			Input              string          `json:"input"`
			Output             string          `json:"output"`
			GroupBy            release.GroupBy `json:"group_by"`
			Repositories       int             `json:"repositories"`
			Commits            int             `json:"commits"`
			FailedRepositories int             `json:"failed_repositories"`
			Draft              bool            `json:"draft"`
		}{Input: inputPath, Output: destination.Name(), GroupBy: grouping, Repositories: len(document.Components), Draft: document.Release.Draft}
		for _, component := range document.Components {
			summary.Commits += len(component.Commits)
			if component.Status == "failed" {
				summary.FailedRepositories++
			}
		}
		_, pathErr := fmt.Fprintf(a.options.Err, "HTML release notes: %s\n", destination.Name())
		var outputErr error
		if a.format == "json" {
			outputErr = json.NewEncoder(a.options.Out).Encode(summary)
		} else {
			_, outputErr = fmt.Fprintf(a.options.Out, "Rendered %d commit(s) across %d repositories; grouped by %s.\n", summary.Commits, summary.Repositories, grouping)
		}
		if err := errors.Join(cmd.Context().Err(), pathErr, outputErr); err != nil {
			return operationError(err)
		}
		return nil
	}
	return cmd
}

func (a *app) promptReleaseGrouping(ctx context.Context) (release.GroupBy, error) {
	fmt.Fprintln(a.options.Err, "Group commits by:")
	for i, choice := range releaseGroupingChoices {
		label := choice.label
		if i == 0 {
			label += " (default)"
		}
		fmt.Fprintf(a.options.Err, "  %d) %s\n", i+1, label)
	}
	for {
		answer, err := a.prompt.line(ctx, "Choose [1-5, default 1]")
		if err != nil {
			return "", err
		}
		if answer == "" {
			return release.GroupRepository, nil
		}
		index, err := strconv.Atoi(answer)
		if err == nil && index >= 1 && index <= len(releaseGroupingChoices) {
			return releaseGroupingChoices[index-1].key, nil
		}
		fmt.Fprintln(a.options.Err, "Enter a number from 1 to 5, or press Enter for Repository.")
	}
}
