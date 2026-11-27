package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yongsan/yongsan-at-your-service/internal/release"
)

type releaseRenderSummary struct {
	Input              string          `json:"input"`
	Output             string          `json:"output"`
	GroupBy            release.GroupBy `json:"group_by"`
	Repositories       int             `json:"repositories"`
	Commits            int             `json:"commits"`
	FailedRepositories int             `json:"failed_repositories"`
	Draft              bool            `json:"draft"`
}

type releaseRenderOutcome struct {
	releaseRenderSummary
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func validateReleaseInput(path string) error {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
		return fmt.Errorf("--input must name a .yaml or .yml file")
	}
	return nil
}

func releaseHTMLName(input string) string {
	return strings.TrimSuffix(filepath.Base(input), filepath.Ext(input)) + ".html"
}

func (a *app) pickReleaseInputs(ctx context.Context) ([]string, error) {
	directory, err := a.resolvePath(filepath.FromSlash(releaseYAMLDirectory))
	if err != nil {
		return nil, err
	}
	candidates, listErr := recentReleaseDrafts(directory)
	manual := false
	inputs := []string{}
	if listErr != nil || len(candidates) == 0 {
		var message string
		if listErr != nil {
			message = fmt.Sprintf("Could not list release YAML files in %q: %v. Enter a filename manually.\n", directory, listErr)
		} else {
			message = fmt.Sprintf("No release YAML files found in %q. Enter a filename manually.\n", directory)
		}
		if _, err := fmt.Fprint(a.prompt.out, message); err != nil {
			return nil, err
		}
		manual = true
	} else {
		choices := make([]string, 0, len(candidates)+1)
		for _, candidate := range candidates {
			choices = append(choices, fmt.Sprintf("%q - modified %s", candidate.Name, candidate.ModifiedAt.Local().Format("2006-01-02 15:04:05 -0700")))
		}
		choices = append(choices, "Enter filename manually")
		selected, err := a.prompt.chooseMany(ctx, "Select release YAML files (newest modified first):", choices)
		if err != nil {
			return nil, err
		}
		for _, index := range selected {
			if index == len(candidates) {
				manual = true
			} else {
				inputs = append(inputs, candidates[index].Path)
			}
		}
	}
	if manual {
		var input string
		if err := a.prompt.required(ctx, "input", "Release YAML filename (relative to "+directory+") or absolute path", &input, validateReleaseInput); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(input) {
			input = filepath.Join(directory, input)
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// Preserve the first spelling/order, but avoid rendering a selected file twice
// when a manual path, symlink, or repeated --input refers to that same file.
func (a *app) resolveReleaseInputs(inputs []string) ([]string, error) {
	paths := make([]string, 0, len(inputs))
	seen := map[string]bool{}
	infos := []os.FileInfo{}
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" {
			return nil, fmt.Errorf("--input must name a .yaml or .yml file")
		}
		path, err := a.resolvePath(input)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		info, statErr := os.Stat(path)
		duplicate := false
		if statErr == nil {
			for _, prior := range infos {
				if os.SameFile(prior, info) {
					duplicate = true
					break
				}
			}
		}
		seen[path] = true
		if duplicate {
			continue
		}
		paths = append(paths, path)
		if statErr == nil {
			infos = append(infos, info)
		}
	}
	return paths, nil
}

func readReleaseInput(input string) (release.Document, error) {
	if err := validateReleaseInput(input); err != nil {
		return release.Document{}, usageError(err)
	}
	file, err := os.Open(input)
	if err != nil {
		return release.Document{}, usageError(fmt.Errorf("open release notes YAML %q: %w", input, err))
	}
	document, readErr := release.ReadDocument(file)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return release.Document{}, usageError(fmt.Errorf("read release notes YAML %q: %w", input, err))
	}
	return document, nil
}

func saveReleaseHTML(ctx context.Context, input, output string, grouping release.GroupBy, document release.Document) (releaseRenderSummary, error) {
	summary := releaseRenderSummary{Input: input, Output: output, GroupBy: grouping, Repositories: len(document.Components), Draft: document.Release.Draft}
	for _, component := range document.Components {
		summary.Commits += len(component.Commits)
		if component.Status == "failed" {
			summary.FailedRepositories++
		}
	}
	var html bytes.Buffer
	if err := release.RenderHTML(&html, document, grouping); err != nil {
		return summary, operationError(fmt.Errorf("render release notes %q: %w", input, err))
	}
	if err := ctx.Err(); err != nil {
		return summary, operationError(err)
	}
	destination, err := reserveReleaseOutput(output)
	if err != nil {
		return summary, operationError(err)
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
	if err := errors.Join(writeErr, syncErr, destination.Close()); err != nil {
		return summary, operationError(fmt.Errorf("save HTML release notes %q: %w", destination.Name(), err))
	}
	saved = true
	return summary, nil
}
