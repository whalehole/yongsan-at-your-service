package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type batchReportOptions struct {
	path      string
	disabled  bool
	open      bool
	noOpen    bool
	autoOpen  bool
	selection string
}

func (o *batchReportOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.path, "report", "", "Save the batch HTML report to a new .html file (default: reports/unique-name.html)")
	cmd.Flags().BoolVar(&o.disabled, "no-report", false, "Do not save a batch report")
	cmd.Flags().BoolVar(&o.open, "open-report", false, "Open the saved batch report in the default browser (overrides reports.auto_open)")
	cmd.Flags().BoolVar(&o.noOpen, "no-open-report", false, "Do not open the saved batch report (overrides reports.auto_open)")
}

func (o batchReportOptions) validate(cmd *cobra.Command) error {
	if changed(cmd, "open-report") && changed(cmd, "no-open-report") {
		return fmt.Errorf("--open-report and --no-open-report cannot be used together")
	}
	if o.disabled && (changed(cmd, "open-report") && o.open || changed(cmd, "no-open-report") && !o.noOpen) {
		return fmt.Errorf("opening a batch report cannot be enabled with --no-report")
	}
	if changed(cmd, "report") && strings.TrimSpace(o.path) == "" {
		return fmt.Errorf("--report must name a new .html file")
	}
	if o.path != "" {
		if o.disabled {
			return fmt.Errorf("--report and --no-report cannot be used together")
		}
		if !strings.EqualFold(filepath.Ext(o.path), ".html") {
			return fmt.Errorf("--report must have a .html extension")
		}
	}
	return nil
}

func (o *batchReportOptions) configure(cmd *cobra.Command, cfg *config.Config) {
	o.autoOpen = cfg.Reports.AutoOpen
	if changed(cmd, "open-report") {
		o.autoOpen = o.open
	}
	if changed(cmd, "no-open-report") {
		o.autoOpen = !o.noOpen
	}
}

type batchReportRun struct {
	data     batchReport
	file     *os.File
	path     string
	saved    bool
	autoOpen bool
}

// Reserve a new destination before any repository jobs run. This also prevents
// a custom report path from overwriting configuration or an earlier report.
func reserveBatchReport(baseDirectory, path, operation string) (*batchReportRun, error) {
	baseDirectory, err := filepath.Abs(baseDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve batch report working directory: %w", err)
	}
	directory := filepath.Join(baseDirectory, "reports")
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDirectory, path)
		}
		directory = filepath.Dir(path)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("prepare batch report directory: %w", err)
	}
	var file *os.File
	if path == "" {
		pattern := "yas-" + operation + "-" + time.Now().UTC().Format("20060102T150405Z") + "-*.html"
		file, err = os.CreateTemp(directory, pattern)
	} else {
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return nil, fmt.Errorf("prepare batch report file: %w", err)
	}
	absolutePath, err := filepath.Abs(file.Name())
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, fmt.Errorf("resolve batch report path: %w", err)
	}
	return &batchReportRun{file: file, path: absolutePath}, nil
}

func (r *batchReportRun) save() error {
	renderErr := renderBatchReport(r.file, r.data)
	var syncErr error
	if renderErr == nil {
		syncErr = r.file.Sync()
	}
	closeErr := r.file.Close()
	r.file = nil
	if err := errors.Join(renderErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("save batch report %q: %w", r.path, err)
	}
	r.saved = true
	return nil
}

func (r *batchReportRun) cleanup() {
	if r.file != nil {
		r.file.Close()
	}
	if !r.saved {
		os.Remove(r.path)
	}
}

func (a *app) executeBatch(ctx context.Context, jobs []job, concurrency int, timeout time.Duration, options batchReportOptions, operation, server string) error {
	if options.disabled {
		return a.execute(ctx, jobs, concurrency, timeout)
	}
	location, err := a.location()
	if err != nil {
		return operationError(err)
	}
	report, err := reserveBatchReport(location.WorkingDirectory, options.path, operation)
	if err != nil {
		return operationError(err)
	}
	defer report.cleanup()
	report.autoOpen = options.autoOpen
	report.data = batchReport{
		Operation: operation, Version: a.options.Version, Server: server,
		DryRun: a.dryRun, Timeout: timeout,
		Selection: options.selection,
	}
	return a.executeWithReport(ctx, jobs, concurrency, timeout, report)
}
