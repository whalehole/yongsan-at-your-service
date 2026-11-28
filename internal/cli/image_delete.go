package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
	"github.com/yongsan/yongsan-at-your-service/internal/dockerimage"
)

func (a *app) deleteDockerImages() *cobra.Command {
	var filter dockerimage.Filter
	var report batchReportOptions
	var yes bool
	var concurrency int
	cmd := &cobra.Command{
		Use: "batch", Short: "Delete Docker image references by exact repository name or tag",
		Long: "List matching tagged images in the current Docker context/environment, then remove their exact repository:tag references.\nAn unqualified --name matches repository basenames across namespaces; a full repository path matches exactly. Name and tag filters combine with AND.\nNo GitLab token or operational configuration is required. Deletion never uses --force or removes containers.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&filter.Name, "name", "", "Exact image repository basename (all tags), or full repository path")
	cmd.Flags().StringVar(&filter.Tag, "tag", "", "Exact Docker image tag across repositories (not a Git tag)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Delete the previewed image references without an interactive confirmation")
	cmd.Flags().IntVar(&concurrency, "concurrency", config.DefaultImageDeletionConcurrency, "Maximum simultaneous image-reference deletion jobs (overrides image_deletion.concurrency)")
	report.bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.format != "text" && a.format != "json" {
			return usageError(fmt.Errorf("--format must be text or json"))
		}
		if changed(cmd, "concurrency") && concurrency < 1 {
			return usageError(fmt.Errorf("--concurrency must be positive"))
		}
		if err := report.validate(cmd); err != nil {
			return usageError(err)
		}
		filename, err := a.configPath()
		if err != nil {
			return usageError(err)
		}
		cfg, err := config.LoadFile(filename)
		if err != nil {
			// A missing operational config is optional; an existing broken
			// symlink must not silently discard configured execution settings.
			_, statErr := os.Lstat(filename)
			if !errors.Is(err, os.ErrNotExist) || !errors.Is(statErr, os.ErrNotExist) {
				return usageError(err)
			}
			cfg = &config.Config{ImageDeletion: config.ImageDeletionConfig{Concurrency: config.DefaultImageDeletionConcurrency}}
		}
		if changed(cmd, "concurrency") {
			cfg.ImageDeletion.Concurrency = concurrency
		}
		if err := cfg.ValidateImageDeletion(); err != nil {
			return usageError(err)
		}
		concurrency = cfg.ImageDeletion.Concurrency
		for name, value := range map[string]string{"name": filter.Name, "tag": filter.Tag} {
			if changed(cmd, name) && strings.TrimSpace(value) == "" {
				return usageError(fmt.Errorf("--%s must not be empty", name))
			}
		}
		if filter.Name == "" && filter.Tag == "" {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("supply --name or --tag to select Docker images"))
			}
			choice, err := a.prompt.choose(cmd.Context(), "Select Docker images by:", []string{"Image repository name (all tags)", "Image tag (all repositories)"})
			if err != nil {
				return usageError(err)
			}
			if choice == 0 {
				if err := a.prompt.required(cmd.Context(), "name", "Docker image repository name", &filter.Name, func(name string) error { return (dockerimage.Filter{Name: name}).Validate() }); err != nil {
					return usageError(err)
				}
			} else if err := a.prompt.required(cmd.Context(), "tag", "Docker image tag", &filter.Tag, func(tag string) error { return (dockerimage.Filter{Tag: tag}).Validate() }); err != nil {
				return usageError(err)
			}
		}
		if err := filter.Validate(); err != nil {
			return usageError(err)
		}
		timeout := 2 * time.Minute
		if changed(cmd, "timeout") {
			var err error
			timeout, err = time.ParseDuration(a.timeout)
			if err != nil || timeout <= 0 {
				return usageError(fmt.Errorf("--timeout must be a positive duration, for example 2m"))
			}
		}
		if !a.dryRun && !yes && !a.prompt.interactive() {
			return usageError(fmt.Errorf("deleting Docker images in non-interactive mode requires --yes; use --dry-run to preview matches"))
		}
		client := &dockerimage.Client{}
		listContext, cancel := context.WithTimeout(cmd.Context(), timeout)
		images, err := client.List(listContext, filter)
		cancel()
		if err != nil {
			return operationError(err)
		}
		report.selection = dockerImageSelection(filter)
		if err := a.previewDockerImages(images, report.selection); err != nil {
			return operationError(err)
		}
		if len(images) > 0 && !a.dryRun && !yes {
			confirmed, err := a.confirmDockerImageDeletion(cmd.Context())
			if err != nil {
				return operationError(err)
			}
			if !confirmed {
				if _, err := fmt.Fprintln(a.options.Err, "Deletion cancelled; no Docker image references were removed."); err != nil {
					return operationError(err)
				}
				if a.format == "json" {
					if err := json.NewEncoder(a.options.Out).Encode([]result{}); err != nil {
						return operationError(err)
					}
					return nil
				}
				_, err := fmt.Fprintln(a.options.Out, "No images were removed.")
				if err != nil {
					return operationError(err)
				}
				return nil
			}
		}
		jobs := make([]job, len(images))
		for i, image := range images {
			image := image
			jobs[i] = func(ctx context.Context) result {
				r := result{Project: image.Repository, Operation: "image-delete", Image: image.Reference(), ImageID: image.ID, Tag: image.Tag, Status: "failed"}
				if err := ctx.Err(); err != nil {
					r.Error = fmt.Sprintf("Deletion of Docker image reference %q did not start: %v.", r.Image, err)
					return r
				}
				if a.dryRun {
					progress(ctx, r, "dry run: matching Docker image reference; no deletion")
					r.Status = "planned"
					r.Message = fmt.Sprintf("Docker image reference %q would be removed; no deletion was attempted (dry run).", r.Image)
					return r
				}
				progress(ctx, r, "rechecking image ID and removing the selected reference")
				status, err := client.Remove(ctx, image)
				if err != nil {
					r.Error = err.Error()
					return r
				}
				r.Status = status
				if status == "skipped" {
					r.Message = fmt.Sprintf("Docker image reference %q is already absent; removal was not attempted.", r.Image)
				} else {
					r.Message = fmt.Sprintf("Docker image reference %q was removed. Docker retains layers still referenced elsewhere.", r.Image)
				}
				return r
			}
		}
		return a.executeBatch(cmd.Context(), jobs, concurrency, timeout, report, "image-delete", "Current Docker context/environment")
	}
	return cmd
}

func dockerImageSelection(filter dockerimage.Filter) string {
	parts := []string{}
	if filter.Name != "" {
		parts = append(parts, fmt.Sprintf("Repository name = %q", filter.Name))
	}
	if filter.Tag != "" {
		parts = append(parts, fmt.Sprintf("Docker tag = %q", filter.Tag))
	}
	return strings.Join(parts, " AND ")
}

func (a *app) previewDockerImages(images []dockerimage.Image, selection string) error {
	if _, err := fmt.Fprintf(a.options.Err, "Docker image selection: %s\nTarget: current Docker context/environment.\n%d matching tagged image reference(s).\n", selection, len(images)); err != nil {
		return err
	}
	if len(images) == 0 {
		_, err := fmt.Fprintln(a.options.Err, "No matching Docker image references were found; no deletion will be attempted.")
		return err
	}
	writer := tabwriter.NewWriter(a.options.Err, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "IMAGE\tIMAGE ID"); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := fmt.Fprintf(writer, "%s\t%s\n", image.Reference(), image.ID); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a *app) confirmDockerImageDeletion(ctx context.Context) (bool, error) {
	for {
		answer, err := a.prompt.line(ctx, "Remove these Docker image references? [y/N]")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "", "n", "no":
			return false, nil
		case "y", "yes":
			return true, nil
		default:
			fmt.Fprintln(a.options.Err, "Enter y or n.")
		}
	}
}
