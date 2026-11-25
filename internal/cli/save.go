package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

type imageSaveOptions struct {
	enabled   bool
	directory string
	run       *imageSaveRun
}

type imageSaveRunKey struct{}

// A run shares one destination and reserves archive names across its workers.
// The directory is created only when the first successful build reaches saving.
type imageSaveRun struct {
	parent    string
	started   time.Time
	once      sync.Once
	mu        sync.Mutex
	names     map[string]bool
	id        string
	directory string
	err       error
}

func (o imageSaveOptions) context(ctx context.Context) context.Context {
	if o.run == nil {
		return ctx
	}
	return context.WithValue(ctx, imageSaveRunKey{}, o.run)
}

func (r *imageSaveRun) reserveName(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = make(map[string]bool)
	}
	// Be portable to filesystems where case does not distinguish filenames.
	key := strings.ToLower(name)
	if r.names[key] {
		return fmt.Errorf("another selected image maps to archive %q; use distinct image names or tags", name)
	}
	r.names[key] = true
	return nil
}

func (r *imageSaveRun) destination() (string, error) {
	r.once.Do(func() {
		id, directory, err := reserveSaveRunDirectory(r.parent, r.started)
		r.mu.Lock()
		r.id, r.directory, r.err = id, directory, err
		r.mu.Unlock()
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.directory, r.err
}

func (r *imageSaveRun) annotate(results []result) {
	r.mu.Lock()
	id, directory := r.id, r.directory
	r.mu.Unlock()
	if directory == "" {
		return
	}
	for i := range results {
		if results[i].Operation == "build" && results[i].SaveStatus != "" {
			results[i].BatchID, results[i].ArchiveDirectory = id, directory
		}
	}
}

func reserveSaveRunDirectory(parent string, started time.Time) (string, string, error) {
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", "", fmt.Errorf("create image archive parent directory: %w", err)
	}
	for offset := 0; offset < 1000; offset++ {
		id := started.UTC().Add(time.Duration(offset) * time.Nanosecond).Format("20060102T150405.000000000Z")
		directory := filepath.Join(parent, id)
		if err := os.Mkdir(directory, 0700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", "", fmt.Errorf("create image archive batch directory: %w", err)
		}
		return id, directory, nil
	}
	return "", "", fmt.Errorf("could not reserve a unique image archive batch directory")
}

func (o *imageSaveOptions) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.enabled, "save", false, "Save each successfully built Docker image as a .tar archive")
	cmd.Flags().StringVar(&o.directory, "save-dir", "images", "Parent directory for timestamped image archive batches (requires --save or answering yes)")
}

func (a *app) chooseImageSave(cmd *cobra.Command, options imageSaveOptions) (imageSaveOptions, error) {
	if strings.TrimSpace(options.directory) == "" || strings.ContainsRune(options.directory, '\x00') {
		return options, fmt.Errorf("--save-dir must name a directory")
	}
	if !changed(cmd, "save") && a.prompt.interactive() {
		for {
			answer, err := a.prompt.line(cmd.Context(), "Save built images as .tar files after build? [y/N]")
			if err != nil {
				return options, err
			}
			switch strings.ToLower(answer) {
			case "", "n", "no":
				options.enabled = false
			case "y", "yes":
				options.enabled = true
			default:
				fmt.Fprintln(a.prompt.out, "Enter y or n.")
				continue
			}
			break
		}
	}
	if options.enabled {
		absolute, err := filepath.Abs(options.directory)
		if err != nil {
			return options, fmt.Errorf("resolve save directory: %w", err)
		}
		options.directory = absolute
		options.run = &imageSaveRun{parent: absolute, started: time.Now().UTC()}
	}
	return options, nil
}
