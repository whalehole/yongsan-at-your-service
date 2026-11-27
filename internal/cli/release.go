package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
	"github.com/yongsan/yongsan-at-your-service/internal/release"
	"go.yaml.in/yaml/v3"
)

type releaseDocument = release.Document
type releaseMetadata = release.Metadata
type releaseScanMetadata = release.ScanMetadata

func (a *app) releaseScan() *cobra.Command {
	var options release.Options
	var scope groupOptions
	var only []string
	var output string
	var concurrency int
	cmd := &cobra.Command{Use: "scan", Short: "Collect new commits between releases and write draft release-notes YAML", Args: cobra.NoArgs}
	scope.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&options.From, "from", "", "Previous release Git tag (exclusive with --previous)")
	f.StringVar(&options.To, "to", "", "New release Git tag")
	f.StringVar(&options.ToBranch, "to-branch", "", "Current release branch for an untagged release (exclusive with --to and --to-commit)")
	f.StringVar(&options.ToCommit, "to-commit", "", "Current release commit for an untagged release (exclusive with --to and --to-branch)")
	f.StringVar(&options.Version, "version", "", "Release version label in the draft (default: target tag, branch, or commit)")
	f.BoolVar(&options.Previous, "previous", false, "Detect the previous existing stable version tag in the same major/minor release line")
	f.StringArrayVar(&only, "only", nil, "Select a configured project identifier (repeatable)")
	f.StringVar(&output, "output", "", "New .yaml/.yml draft file (default: release-notes-VERSION.yaml; never overwrites)")
	f.IntVar(&concurrency, "concurrency", 0, "Maximum simultaneous repository scans (default: release.concurrency)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load(cmd, "", true)
		if err != nil {
			return err
		}
		if a.dryRun {
			return usageError(fmt.Errorf("release scan is already read-only in GitLab; omit --dry-run to generate the local YAML draft"))
		}
		targetCount := 0
		for _, target := range []string{options.To, options.ToBranch, options.ToCommit} {
			if target != "" {
				targetCount++
			}
		}
		if targetCount > 1 {
			return usageError(fmt.Errorf("--to, --to-branch, and --to-commit are mutually exclusive"))
		}
		if options.Previous && options.From != "" {
			return usageError(fmt.Errorf("--from and --previous are mutually exclusive"))
		}
		if len(cfg.Release.Repositories) == 0 {
			return usageError(fmt.Errorf("release.repositories in yas.yaml is empty; add the <group>/<repository> projects to scan for release notes"))
		}
		projects := []string{}
		seen := map[string]bool{}
		add := func(project string) {
			if !seen[project] {
				projects = append(projects, project)
				seen[project] = true
			}
		}
		workers, timeoutText := cfg.Release.Concurrency, cfg.Release.Timeout
		for _, r := range cfg.Release.Repositories {
			add(r.Project)
		}
		if changed(cmd, "concurrency") {
			workers = concurrency
		}
		if changed(cmd, "timeout") {
			timeoutText = a.timeout
		}
		timeout, err := time.ParseDuration(timeoutText)
		if workers < 1 || err != nil || timeout <= 0 {
			return usageError(fmt.Errorf("release scan requires positive concurrency and a positive job timeout (for example 2m)"))
		}
		indices, err := a.selectBatch(cmd, projects, only, scope)
		if err != nil {
			return usageError(err)
		}
		selected := make([]string, 0, len(indices))
		for _, i := range indices {
			if err := config.ValidateProject(projects[i]); err != nil {
				return usageError(err)
			}
			selected = append(selected, projects[i])
		}
		if options.To == "" && options.ToBranch == "" && options.ToCommit == "" {
			if err := a.prompt.required(cmd.Context(), "to", "New release Git tag (for example 2.2.8)", &options.To, config.ValidateGitRef); err != nil {
				return usageError(err)
			}
		}
		if options.From == "" && !options.Previous {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("supply --from or --previous"))
			}
			choices := []string{"Enter previous release tag"}
			if release.ValidateOptions(release.Options{To: options.To, Previous: true}) == nil {
				choices = append(choices, "Detect previous stable version in the same major/minor release line")
			}
			choice := 0
			if len(choices) > 1 {
				choice, err = a.prompt.choose(cmd.Context(), "Select the comparison baseline:", choices)
				if err != nil {
					return usageError(err)
				}
			}
			if choice == 1 {
				options.Previous = true
			} else if err := a.prompt.required(cmd.Context(), "from", "Previous release Git tag (for example 2.2.7)", &options.From, config.ValidateGitRef); err != nil {
				return usageError(err)
			}
		}
		for label, value := range map[string]string{"from": options.From, "to": options.To, "to-branch": options.ToBranch} {
			if value != "" {
				if err := config.ValidateGitRef(value); err != nil {
					return usageError(fmt.Errorf("--%s: %w", label, err))
				}
			}
		}
		if options.ToCommit != "" {
			if err := config.ValidateCommit(options.ToCommit); err != nil {
				return usageError(fmt.Errorf("--to-commit: %w", err))
			}
		}
		if err := release.ValidateOptions(options); err != nil {
			return usageError(err)
		}
		target := options.To
		if target == "" {
			target = options.ToBranch
		}
		if target == "" {
			target = options.ToCommit
		}
		version := options.Version
		if version == "" {
			version = target
		}
		if strings.TrimSpace(version) == "" || strings.ContainsAny(version, "\x00\r\n") {
			return usageError(fmt.Errorf("--version must be a nonempty release label without newline characters"))
		}
		if changed(cmd, "output") && strings.TrimSpace(output) == "" {
			return usageError(fmt.Errorf("--output must name a new .yaml or .yml file"))
		}
		if output == "" {
			output = defaultReleaseOutput(version)
			if a.prompt.interactive() {
				answer, err := a.prompt.line(cmd.Context(), "Release notes YAML file ["+output+"] (Enter keeps default)")
				if err != nil {
					return usageError(err)
				}
				if answer != "" {
					output = answer
				}
			}
		}
		if extension := strings.ToLower(filepath.Ext(output)); extension != ".yaml" && extension != ".yml" {
			return usageError(fmt.Errorf("--output must have a .yaml or .yml extension"))
		}
		client, err := clientFor(cfg)
		if err != nil {
			return err
		}
		file, err := reserveReleaseOutput(output)
		if err != nil {
			return operationError(err)
		}
		saved := false
		defer func() {
			file.Close()
			if !saved {
				os.Remove(file.Name())
			}
		}()
		components := make([]release.Component, len(selected))
		jobs := make([]job, len(selected))
		for i, project := range selected {
			i, project := i, project
			jobs[i] = func(ctx context.Context) result {
				r := result{Project: project, Operation: "release-scan", Tag: version, Status: "failed"}
				progress(ctx, r, "resolving release references and listing new commits")
				start := time.Now()
				component := release.Scan(ctx, client, project, options)
				component.Duration = time.Since(start).Round(time.Millisecond).String()
				components[i] = component
				r.Status, r.Error, r.Commit = component.Status, component.Error, component.ToCommit
				return r
			}
		}
		if workers > len(jobs) {
			workers = len(jobs)
		}
		started := time.Now()
		jobs, progressError := a.progressJobs(jobs, workers)
		runJobs(cmd.Context(), jobs, workers, timeout)
		document := releaseDocument{
			SchemaVersion: 1,
			Release:       releaseMetadata{Title: "Release " + version, Version: version, Date: started.Format("2006-01-02"), Summary: "", Draft: true, From: options.From, To: target},
			Scan:          releaseScanMetadata{StartedAt: started.UTC().Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), Concurrency: workers, Timeout: timeout.String(), Total: len(components)},
			Components:    components, UpgradeNotes: []string{}, KnownIssues: []string{},
		}
		for _, component := range components {
			switch component.Status {
			case "changed":
				document.Scan.Changed++
			case "unchanged":
				document.Scan.Unchanged++
			default:
				document.Scan.Failed++
			}
		}
		saveErr := writeReleaseDocument(file, document)
		saved = saveErr == nil
		var pathErr error
		if saved {
			_, pathErr = fmt.Fprintf(a.options.Err, "Release notes draft: %s\n", file.Name())
		}
		outputErr := writeReleaseResults(a.options.Out, a.format, components)
		var scanErr error
		if document.Scan.Failed > 0 {
			location := "component results"
			if saved {
				location = "YAML draft"
			}
			scanErr = fmt.Errorf("%d repository release scan(s) failed; review the failed components in the %s", document.Scan.Failed, location)
		}
		if err := errors.Join(scanErr, saveErr, outputErr, pathErr, progressError()); err != nil {
			return operationError(err)
		}
		return nil
	}
	return cmd
}

func defaultReleaseOutput(version string) string {
	var name strings.Builder
	for _, ch := range version {
		if name.Len() >= 80 {
			break
		}
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-' {
			name.WriteRune(ch)
		} else {
			name.WriteByte('-')
		}
	}
	return "release-notes-" + name.String() + ".yaml"
}

func reserveReleaseOutput(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve release notes path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, fmt.Errorf("prepare release notes directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("prepare release notes file (existing files are not overwritten): %w", err)
	}
	return file, nil
}

func writeReleaseDocument(file *os.File, document releaseDocument) error {
	_, headerErr := io.WriteString(file, "# Draft release notes generated from GitLab commit metadata.\n# Edit and enrich each commit entry, then add the release summary, upgrade notes, and known issues.\n# Failed components are incomplete scans, not evidence of an empty commit range.\n")
	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)
	var encodeErr error
	if headerErr == nil {
		encodeErr = encoder.Encode(document)
	}
	encoderErr := encoder.Close()
	var syncErr error
	if errors.Join(headerErr, encodeErr, encoderErr) == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(headerErr, encodeErr, encoderErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("save release notes draft %q: %w", file.Name(), err)
	}
	return nil
}

func writeReleaseResults(out io.Writer, format string, components []release.Component) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(components)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSTATUS\tFROM\tTO\tNEW COMMITS")
	changed, unchanged, failed := 0, 0, 0
	for _, component := range components {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", component.Project, component.Status, component.FromRef, component.ToRef, len(component.Commits))
		if component.Error != "" {
			fmt.Fprintf(w, "  %s\n", component.Error)
		}
		switch component.Status {
		case "changed":
			changed++
		case "unchanged":
			unchanged++
		default:
			failed++
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\n%d total; %d with new commits, %d without new commits, %d failed.\n", len(components), changed, unchanged, failed)
	return err
}
