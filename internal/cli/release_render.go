package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

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
	var inputs []string
	var output, outputDirectory, groupBy string
	cmd := &cobra.Command{
		Use: "render", Short: "Generate standalone HTML release notes from selected release YAML files",
		Long: "Generate one HTML document per release YAML. Without --input, an interactive checkbox picker lists the ten most recently modified drafts in release-notes/yaml.\nRelative --input and output overrides start from working_directory. Manually entered filenames start from release-notes/yaml. No operational configuration, GitLab token, or network access is required.",
		Args: cobra.NoArgs,
	}
	f := cmd.Flags()
	f.StringArrayVar(&inputs, "input", nil, "Release .yaml/.yml file relative to working_directory (repeatable; bypasses picker)")
	f.StringVar(&output, "output", "", "New .html file for one input, relative to working_directory (exclusive with --output-dir)")
	f.StringVar(&outputDirectory, "output-dir", "", "Shared HTML destination relative to working_directory (default: release-notes/html)")
	f.StringVar(&groupBy, "group-by", string(release.GroupRepository), "Initial grouping for all files: repository, change-request, incident, remarks, or author")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.format != "text" && a.format != "json" {
			return usageError(fmt.Errorf("--format must be text or json"))
		}
		if a.dryRun {
			return usageError(fmt.Errorf("release render is local; omit --dry-run to generate HTML files"))
		}
		if changed(cmd, "group-by") && strings.TrimSpace(groupBy) == "" {
			return usageError(fmt.Errorf("--group-by must be repository, change-request, incident, remarks, or author"))
		}
		grouping, err := release.ParseGroupBy(groupBy)
		if err != nil {
			return usageError(err)
		}
		if changed(cmd, "output") && strings.TrimSpace(output) == "" {
			return usageError(fmt.Errorf("--output must name a new .html file"))
		}
		if output != "" && !strings.EqualFold(filepath.Ext(output), ".html") {
			return usageError(fmt.Errorf("--output must have a .html extension"))
		}
		if changed(cmd, "output-dir") && strings.TrimSpace(outputDirectory) == "" {
			return usageError(fmt.Errorf("--output-dir must name a directory"))
		}
		if output != "" && outputDirectory != "" {
			return usageError(fmt.Errorf("--output and --output-dir cannot be used together"))
		}
		if err := cmd.Context().Err(); err != nil {
			return operationError(err)
		}
		if len(inputs) == 0 {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("missing required --input"))
			}
			inputs, err = a.pickReleaseInputs(cmd.Context())
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
					return operationError(err)
				}
				return usageError(err)
			}
		}
		paths, err := a.resolveReleaseInputs(inputs)
		if err != nil {
			return usageError(err)
		}
		if len(paths) == 0 {
			return usageError(fmt.Errorf("select at least one release YAML file"))
		}
		if len(paths) > 1 && output != "" {
			return usageError(fmt.Errorf("--output is only available for one input; use --output-dir for multiple files"))
		}
		var singleDocument release.Document
		if len(paths) == 1 {
			singleDocument, err = readReleaseInput(paths[0])
			if err != nil {
				return err
			}
		}
		if !changed(cmd, "group-by") && a.prompt.interactive() {
			grouping, err = a.promptReleaseGrouping(cmd.Context())
			if err != nil {
				return usageError(err)
			}
		}
		destinations, err := a.releaseRenderDestinations(cmd, paths, output, outputDirectory)
		if err != nil {
			return usageError(err)
		}
		outcomes := make([]releaseRenderOutcome, len(paths))
		counts := map[string]int{}
		for _, destination := range destinations {
			counts[strings.ToLower(filepath.Clean(destination))]++
		}
		for i, input := range paths {
			outcomes[i] = releaseRenderOutcome{releaseRenderSummary: releaseRenderSummary{Input: input, Output: destinations[i], GroupBy: grouping}, Status: "failed"}
			if counts[strings.ToLower(filepath.Clean(destinations[i]))] > 1 {
				outcomes[i].Error = fmt.Sprintf("Output filename collision: more than one selected input maps to %q; render these inputs separately with distinct --output paths.", destinations[i])
			}
		}
		var terminalErr error
		for i, input := range paths {
			if outcomes[i].Error != "" {
				continue
			}
			if err := cmd.Context().Err(); err != nil {
				if len(paths) == 1 {
					return operationError(err)
				}
				outcomes[i].Error = fmt.Sprintf("Render did not start: %v.", err)
				continue
			}
			document := singleDocument
			if len(paths) > 1 {
				document, err = readReleaseInput(input)
			}
			if err == nil {
				outcomes[i].releaseRenderSummary, err = saveReleaseHTML(cmd.Context(), input, destinations[i], grouping, document)
			}
			if err != nil {
				if len(paths) == 1 {
					return err
				}
				outcomes[i].Error = err.Error()
				err = nil
				continue
			}
			outcomes[i].Status = "rendered"
			_, pathErr := fmt.Fprintf(a.options.Err, "HTML release notes: %s\n", destinations[i])
			terminalErr = errors.Join(terminalErr, pathErr)
		}
		failed := 0
		for _, outcome := range outcomes {
			if outcome.Status != "rendered" {
				failed++
			}
		}
		var outputErr error
		if a.format == "json" {
			if len(outcomes) == 1 {
				outputErr = json.NewEncoder(a.options.Out).Encode(outcomes[0].releaseRenderSummary)
			} else {
				outputErr = json.NewEncoder(a.options.Out).Encode(outcomes)
			}
		} else if len(outcomes) == 1 {
			summary := outcomes[0]
			_, outputErr = fmt.Fprintf(a.options.Out, "Rendered %d commit(s) across %d repositories; grouped by %s.\n", summary.Commits, summary.Repositories, grouping)
		} else {
			table := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
			_, outputErr = fmt.Fprintln(table, "INPUT\tSTATUS\tOUTPUT / ERROR")
			for _, outcome := range outcomes {
				detail := outcome.Output
				if outcome.Error != "" {
					detail = outcome.Error
				}
				_, rowErr := fmt.Fprintf(table, "%q\t%s\t%s\n", outcome.Input, outcome.Status, detail)
				outputErr = errors.Join(outputErr, rowErr)
			}
			outputErr = errors.Join(outputErr, table.Flush())
		}
		var batchErr error
		if failed > 0 {
			batchErr = fmt.Errorf("%d release YAML file(s) failed to render; review the per-file results", failed)
		}
		if err := errors.Join(batchErr, terminalErr, outputErr, cmd.Context().Err()); err != nil {
			return operationError(err)
		}
		return nil
	}
	return cmd
}

func (a *app) releaseRenderDestinations(cmd *cobra.Command, inputs []string, output, directory string) ([]string, error) {
	if len(inputs) > 1 || directory != "" {
		if directory == "" {
			directory = filepath.FromSlash(releaseHTMLDirectory)
			if a.prompt.interactive() {
				answer, err := a.prompt.line(cmd.Context(), "HTML output directory ["+directory+"] (Enter keeps default)")
				if err != nil {
					return nil, err
				}
				if answer != "" {
					directory = answer
				}
			}
		}
		base, err := a.resolvePath(directory)
		if err != nil {
			return nil, err
		}
		paths := make([]string, len(inputs))
		for i, input := range inputs {
			paths[i] = filepath.Join(base, releaseHTMLName(input))
		}
		return paths, nil
	}
	if output == "" {
		output = filepath.Join(filepath.FromSlash(releaseHTMLDirectory), releaseHTMLName(inputs[0]))
		if a.prompt.interactive() {
			answer, err := a.prompt.line(cmd.Context(), "HTML release notes file ["+output+"] (Enter keeps default)")
			if err != nil {
				return nil, err
			}
			if answer != "" {
				output = answer
			}
		}
	}
	if !strings.EqualFold(filepath.Ext(output), ".html") {
		return nil, fmt.Errorf("--output must have a .html extension")
	}
	absolute, err := a.resolvePath(output)
	if err != nil {
		return nil, err
	}
	return []string{absolute}, nil
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
