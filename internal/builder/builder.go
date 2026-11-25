// Package builder downloads a single source snapshot and builds a Docker image.
package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options identifies an immutable source snapshot and its Docker build settings.
// Context is relative to the repository root; Dockerfile is relative to Context.
type Options struct {
	Project, Commit, Context, Dockerfile, Image string
	BuildArgs                                   map[string]string
	NoCache, Pull                               bool
}

// ArchiveSource provides GitLab source archives without cloning Git history.
type ArchiveSource interface {
	Archive(ctx context.Context, project, sha, path string) (io.ReadCloser, error)
}

// Builder runs the Docker executable against an isolated temporary snapshot.
type Builder struct {
	Source   ArchiveSource
	Docker   string
	TempDir  string
	Output   io.Writer
	Progress func(string)
}

// Build downloads, extracts, and builds one source snapshot. Temporary source
// files are removed on success, failure, and cancellation.
func (b *Builder) Build(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Source == nil {
		return errors.New("source archive provider is required")
	}
	if strings.TrimSpace(opts.Project) == "" || strings.TrimSpace(opts.Commit) == "" || strings.TrimSpace(opts.Image) == "" {
		return errors.New("project, resolved commit, and output image are required")
	}
	contextPath, err := cleanRelative(opts.Context, true)
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}
	dockerfile := opts.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	dockerfile, err = cleanRelative(dockerfile, false)
	if err != nil {
		return fmt.Errorf("Dockerfile: %w", err)
	}
	docker := b.Docker
	if docker == "" {
		docker = "docker"
	}
	docker, err = exec.LookPath(docker)
	if err != nil {
		return fmt.Errorf("Docker executable is unavailable: %w", err)
	}
	workDir, err := os.MkdirTemp(b.TempDir, "yas-build-*")
	if err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	archivePath := contextPath
	if archivePath == "." {
		archivePath = ""
	}
	if b.Progress != nil {
		b.Progress("requesting source archive")
	}
	archive, err := b.Source.Archive(ctx, opts.Project, opts.Commit, archivePath)
	if err != nil {
		return fmt.Errorf("download source: %w", err)
	}
	defer archive.Close()
	// Closing the response also interrupts an in-flight read on cancellation.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			archive.Close()
		case <-finished:
		}
	}()
	if b.Progress != nil {
		b.Progress("downloading and extracting source archive")
	}
	contextRoot, err := extractArchive(ctx, archive, workDir, contextPath)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("extract source: %w", err)
	}
	if err := rejectSubmodules(contextRoot); err != nil {
		return err
	}
	dockerfilePath := filepath.Join(contextRoot, filepath.FromSlash(dockerfile))
	resolved, err := filepath.EvalSymlinks(dockerfilePath)
	if err != nil {
		return fmt.Errorf("Dockerfile %q is unavailable in the selected context: %w", dockerfile, err)
	}
	if !within(contextRoot, resolved) {
		return errors.New("Dockerfile points outside the selected build context")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("Dockerfile %q must be a regular file", dockerfile)
	}
	args := []string{"build"}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	if opts.Pull {
		args = append(args, "--pull")
	}
	keys := make([]string, 0, len(opts.BuildArgs))
	for key := range opts.BuildArgs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--build-arg", key+"="+opts.BuildArgs[key])
	}
	args = append(args, "--file", filepath.FromSlash(dockerfile), "--tag", opts.Image, ".")
	cmd := exec.CommandContext(ctx, docker, args...)
	cmd.Dir = contextRoot
	// A Docker plugin must not keep inherited output pipes open forever after
	// the Docker command has been canceled.
	cmd.WaitDelay = 2 * time.Second
	output := b.Output
	if output == nil {
		output = io.Discard
	}
	cmd.Stdout, cmd.Stderr = output, output
	if b.Progress != nil {
		b.Progress("running Docker build")
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Docker build failed: %w", err)
	}
	return nil
}

func cleanRelative(value string, allowRoot bool) (string, error) {
	if value == "" && allowRoot {
		return ".", nil
	}
	if value == "" || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) || filepath.IsAbs(value) || strings.Contains(value, ":") {
		return "", errors.New("must be a repository-relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", errors.New("parent-directory traversal is not allowed")
		}
	}
	clean := path.Clean(value)
	if clean == "." && !allowRoot {
		return "", errors.New("must name a file")
	}
	return clean, nil
}

func within(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rejectSubmodules(root string) error {
	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".gitmodules" {
			return errors.New("source contains .gitmodules: submodule source is not included in GitLab archives; submodule builds are not supported")
		}
		return nil
	})
}
