// Package dockerimage lists and removes tagged images in the selected Docker
// daemon, inheriting the caller's Docker context, configuration, and environment.
package dockerimage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type Filter struct {
	Name string
	Tag  string
}

func (f Filter) Validate() error {
	if f.Name == "" && f.Tag == "" {
		return errors.New("supply an image repository name, an exact tag, or both")
	}
	if f.Name != "" {
		if err := config.ValidateImage(f.Name + ":yas-check"); err != nil {
			return fmt.Errorf("image name must be a repository without a tag or wildcards: %w", err)
		}
	}
	if f.Tag != "" {
		if err := config.ValidateImage("yas-check:" + f.Tag); err != nil {
			return fmt.Errorf("image tag must be an exact Docker tag without wildcards: %w", err)
		}
	}
	return nil
}

type Image struct {
	Repository string
	Tag        string
	ID         string
}

func (image Image) Reference() string { return image.Repository + ":" + image.Tag }

type Client struct{ Docker string }

// List validates the entire tagged-image listing before selecting references.
// A name without a slash matches the exact repository basename; a name with a
// slash matches the complete repository. When both filters are set, both match.
func (c *Client) List(ctx context.Context, filter Filter) ([]Image, error) {
	return c.list(ctx, filter, "")
}

func (c *Client) list(ctx context.Context, filter Filter, exactReference string) ([]Image, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	args := []string{"image", "ls", "--all", "--no-trunc", "--format", "{{json .}}"}
	if exactReference != "" {
		args = append(args, "--filter", "reference="+exactReference)
	}
	var stdout bytes.Buffer
	if err := c.run(ctx, "list Docker daemon images", &stdout, args...); err != nil {
		return nil, err
	}
	images, err := parseImages(&stdout)
	if err != nil {
		return nil, err
	}
	selected := make([]Image, 0)
	for _, image := range images {
		if exactReference != "" && image.Reference() != exactReference {
			continue
		}
		name := image.Repository
		if !strings.Contains(filter.Name, "/") {
			name = name[strings.LastIndexByte(name, '/')+1:]
		}
		if filter.Name != "" && name != filter.Name || filter.Tag != "" && image.Tag != filter.Tag {
			continue
		}
		selected = append(selected, image)
	}
	return selected, nil
}

// Remove rechecks the exact reference and full image ID before removing that
// reference. This narrows races but is not atomic with Docker's subsequent rm.
// It never forces deletion or removes an image by its shared image ID.
func (c *Client) Remove(ctx context.Context, image Image) (string, error) {
	if err := config.ValidateImage(image.Reference()); err != nil {
		return "", fmt.Errorf("cannot remove an invalid image reference: %w", err)
	}
	id, err := fullImageID(image.ID)
	if err != nil {
		return "", fmt.Errorf("cannot remove %q: %w", image.Reference(), err)
	}
	images, err := c.list(ctx, Filter{Name: image.Repository, Tag: image.Tag}, image.Reference())
	if err != nil {
		return "", fmt.Errorf("recheck image %q before removal: %w", image.Reference(), err)
	}
	if len(images) == 0 {
		return "skipped", nil
	}
	if images[0].ID != id {
		return "", fmt.Errorf("cannot remove image %q: its image ID changed from %s to %s; refresh the preview before retrying", image.Reference(), id, images[0].ID)
	}
	if err := c.run(ctx, fmt.Sprintf("remove Docker image reference %q", image.Reference()), io.Discard, "image", "rm", "--", image.Reference()); err != nil {
		return "", err
	}
	return "deleted", nil
}

func parseImages(output io.Reader) ([]Image, error) {
	byReference := map[string]Image{}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var image Image
		if err := json.Unmarshal(scanner.Bytes(), &image); err != nil {
			return nil, fmt.Errorf("Docker returned invalid image-list JSON on line %d", line)
		}
		if image.Repository == "<none>" || image.Tag == "<none>" {
			continue
		}
		if err := config.ValidateImage(image.Reference()); err != nil {
			return nil, fmt.Errorf("Docker returned an invalid tagged image reference on line %d: %w", line, err)
		}
		id, err := fullImageID(image.ID)
		if err != nil {
			return nil, fmt.Errorf("Docker returned invalid image metadata for %q: %w", image.Reference(), err)
		}
		image.ID = id
		if previous, exists := byReference[image.Reference()]; exists && previous.ID != image.ID {
			return nil, fmt.Errorf("Docker returned ambiguous image IDs for %q; removal of this reference was not attempted", image.Reference())
		}
		byReference[image.Reference()] = image
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Docker image listing: %w", err)
	}
	images := make([]Image, 0, len(byReference))
	for _, image := range byReference {
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Reference() < images[j].Reference() })
	return images, nil
}

func fullImageID(value string) (string, error) {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return "", errors.New("image ID must be a full sha256: value with 64 hexadecimal characters")
	}
	for _, char := range value[len("sha256:"):] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return "", errors.New("image ID must be a full sha256: value with 64 hexadecimal characters")
		}
	}
	return strings.ToLower(value), nil
}

func (c *Client) run(ctx context.Context, action string, stdout io.Writer, args ...string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	docker := c.Docker
	if docker == "" {
		docker = "docker"
	}
	executable, err := exec.LookPath(docker)
	if err != nil {
		return fmt.Errorf("Docker executable is unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = 2 * time.Second
	var stderr boundedStderr
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		detail := stderr.String()
		if detail != "" {
			return fmt.Errorf("%s failed: %s (%w)", action, detail, err)
		}
		return fmt.Errorf("%s failed: %w", action, err)
	}
	return nil
}

type boundedStderr struct {
	data      []byte
	truncated bool
}

func (w *boundedStderr) Write(data []byte) (int, error) {
	const limit = 4096
	remaining := limit - len(w.data)
	if len(data) > remaining {
		w.data = append(w.data, data[:remaining]...)
		w.truncated = true
	} else {
		w.data = append(w.data, data...)
	}
	return len(data), nil
}

func (w *boundedStderr) String() string {
	value := strings.TrimSpace(strings.Map(func(char rune) rune {
		if unicode.IsControl(char) && char != '\n' {
			return ' '
		}
		return char
	}, strings.ToValidUTF8(string(w.data), "�")))
	if w.truncated {
		value += " [stderr truncated]"
	}
	return value
}
