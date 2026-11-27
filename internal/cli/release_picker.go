package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	releaseYAMLDirectory = "release-notes/yaml"
	releaseHTMLDirectory = "release-notes/html"
)

type releaseDraftCandidate struct {
	Path       string
	Name       string
	ModifiedAt time.Time
}

// recentReleaseDrafts lists direct regular YAML files without interpreting their
// contents. A selected draft is validated later by the render command.
func recentReleaseDrafts(directory string) ([]releaseDraftCandidate, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve release draft directory %q: %w", directory, err)
	}
	entries, err := os.ReadDir(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return []releaseDraftCandidate{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read release draft directory %q: %w", absolute, err)
	}
	candidates := make([]releaseDraftCandidate, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		path := filepath.Join(absolute, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, releaseDraftCandidate{Path: path, Name: entry.Name(), ModifiedAt: info.ModTime()})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ModifiedAt.Equal(candidates[right].ModifiedAt) {
			return candidates[left].Name < candidates[right].Name
		}
		return candidates[left].ModifiedAt.After(candidates[right].ModifiedAt)
	})
	if len(candidates) > 10 {
		candidates = candidates[:10]
	}
	return candidates, nil
}
