package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

// Save exports an image from the selected Docker daemon to a new tar archive.
// Incomplete output stays hidden and is removed if the export does not succeed.
func (b *Builder) Save(ctx context.Context, image, directory string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := ArchiveName(image)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(directory) == "" {
		return "", errors.New("image save directory is required")
	}
	docker := b.Docker
	if docker == "" {
		docker = "docker"
	}
	docker, err = exec.LookPath(docker)
	if err != nil {
		return "", fmt.Errorf("Docker executable is unavailable: %w", err)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve image save directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create image save directory: %w", err)
	}
	archivePath := filepath.Join(directory, name)
	// Reserve the deterministic filename before invoking Docker. Other exports
	// with the same repository basename and tag must fail without overwriting it.
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("prepare final image archive: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			os.Remove(archivePath)
		}
	}()
	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("close final image archive reservation: %w", err)
	}
	partial, err := os.CreateTemp(directory, ".yas-image-*.partial")
	if err != nil {
		return "", fmt.Errorf("prepare image archive: %w", err)
	}
	partialPath := partial.Name()
	defer os.Remove(partialPath)
	if err := partial.Close(); err != nil {
		return "", fmt.Errorf("close image archive reservation: %w", err)
	}
	cmd := exec.CommandContext(ctx, docker, "image", "save", "--output", partialPath, image)
	cmd.WaitDelay = 2 * time.Second
	output := b.Output
	if output == nil {
		output = io.Discard
	}
	cmd.Stdout, cmd.Stderr = output, output
	if b.Progress != nil {
		b.Progress("saving Docker image to tar archive")
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("Docker image save failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Lstat(partialPath)
	if err != nil {
		return "", fmt.Errorf("check saved image archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("Docker image save did not produce a nonempty regular archive")
	}
	if err := os.Rename(partialPath, archivePath); err != nil {
		return "", fmt.Errorf("publish saved image archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	complete = true
	return archivePath, nil
}

// ArchiveName returns a portable, deterministic repository-basename_tag.tar
// filename. Registry and namespace prefixes do not appear in the filename.
func ArchiveName(image string) (string, error) {
	if strings.Contains(image, "@") {
		return "", errors.New("image archives require an explicit tag, not a digest")
	}
	if err := config.ValidateImage(image); err != nil {
		return "", fmt.Errorf("image archive name: %w", err)
	}
	colon := strings.LastIndexByte(image, ':')
	leaf := image[strings.LastIndexByte(image[:colon], '/')+1 : colon]
	name := leaf + "_" + image[colon+1:] + ".tar"
	if len(name) > 255 {
		return "", errors.New("image archive filename exceeds 255 bytes")
	}
	return name, nil
}
