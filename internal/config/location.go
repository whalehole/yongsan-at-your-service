package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Location is the fixed configuration and artifact workspace for an invocation.
// Only BootstrapPath supplies working_directory; ConfigPath never redirects it.
type Location struct {
	BootstrapPath    string
	ConfigPath       string
	WorkingDirectory string
}

// ResolveLocation reads the executable-side locator without validating GitLab
// authentication or operation settings. Missing files/directories are allowed
// so config init and local rendering can use the chosen workspace.
func ResolveLocation(bootstrapPath string) (Location, error) {
	if bootstrapPath == "" {
		var err error
		bootstrapPath, err = Path()
		if err != nil {
			return Location{}, err
		}
	}
	absolute, err := filepath.Abs(bootstrapPath)
	if err != nil {
		return Location{}, fmt.Errorf("resolve bootstrap configuration path %q: %w", bootstrapPath, err)
	}
	baseDirectory := filepath.Dir(absolute)
	location := Location{BootstrapPath: absolute, ConfigPath: filepath.Join(baseDirectory, Filename), WorkingDirectory: baseDirectory}
	_, statErr := os.Lstat(absolute)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Location{}, fmt.Errorf("check bootstrap configuration %q: %w", absolute, statErr)
	}
	if statErr == nil {
		bootstrap, err := LoadFile(absolute)
		if err != nil {
			return Location{}, fmt.Errorf("read bootstrap configuration %q: %w", absolute, err)
		}
		if err := validateWorkingDirectory(bootstrap.WorkingDirectory); err != nil {
			return Location{}, fmt.Errorf("bootstrap configuration %q: %w", absolute, err)
		}
		if bootstrap.WorkingDirectory != "" {
			directory := bootstrap.WorkingDirectory
			if !filepath.IsAbs(directory) {
				directory = filepath.Join(baseDirectory, directory)
			}
			location.WorkingDirectory = filepath.Clean(directory)
			location.ConfigPath = filepath.Join(location.WorkingDirectory, Filename)
		}
	}
	if err := checkWorkingDirectory(location.WorkingDirectory); err != nil {
		return Location{}, err
	}
	return location, nil
}

func validateWorkingDirectory(directory string) error {
	if strings.ContainsAny(directory, "\x00\r\n") {
		return errors.New("working_directory must not contain NUL or newline characters")
	}
	if directory != "" && strings.TrimSpace(directory) == "" {
		return errors.New("working_directory must be empty for the executable directory, or name a directory")
	}
	return nil
}

func checkWorkingDirectory(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("check working_directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working_directory %q must be a directory, but it is a file", directory)
	}
	return nil
}

// YAML decodes numeric and Boolean scalars into string fields by coercion.
// A workspace path must instead be a string (or null for the default), while
// the normal typed decoder continues to enforce all known configuration fields.
func validateWorkingDirectoryType(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("decode YAML: %w", err)
	}
	seen := map[*yaml.Node]bool{}
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node == nil || seen[node] {
			return nil
		}
		seen[node] = true
		if node.Kind == yaml.AliasNode {
			return walk(node.Alias)
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value != "working_directory" {
					continue
				}
				value := node.Content[i+1]
				aliases := map[*yaml.Node]bool{}
				for value != nil && value.Kind == yaml.AliasNode && !aliases[value] {
					aliases[value] = true
					value = value.Alias
				}
				if value == nil || value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" && value.ShortTag() != "!!null" {
					return errors.New("working_directory must be a string path or null for the executable directory")
				}
			}
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(&root)
}
