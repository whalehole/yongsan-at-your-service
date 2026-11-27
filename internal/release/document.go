package release

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Document is the editable release-notes YAML produced by a release scan.
type Document struct {
	SchemaVersion int          `yaml:"schema_version"`
	Release       Metadata     `yaml:"release"`
	Scan          ScanMetadata `yaml:"scan"`
	Components    []Component  `yaml:"components"`
	UpgradeNotes  []string     `yaml:"upgrade_notes"`
	KnownIssues   []string     `yaml:"known_issues"`
}

type Metadata struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
	Date    string `yaml:"date"`
	Summary string `yaml:"summary"`
	Draft   bool   `yaml:"draft"`
	From    string `yaml:"from,omitempty"`
	To      string `yaml:"to"`
}

type ScanMetadata struct {
	StartedAt   string `yaml:"started_at"`
	FinishedAt  string `yaml:"finished_at"`
	Concurrency int    `yaml:"concurrency"`
	Timeout     string `yaml:"timeout"`
	Total       int    `yaml:"total"`
	Changed     int    `yaml:"changed"`
	Unchanged   int    `yaml:"unchanged"`
	Failed      int    `yaml:"failed"`
}

// ReadDocument reads one strict release-notes document. It preserves edited
// commit text and annotations, including empty lists for no-change releases.
func ReadDocument(reader io.Reader) (Document, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Document{}, fmt.Errorf("read release-notes YAML: %w", err)
	}
	var source yaml.Node
	if err := yaml.Unmarshal(data, &source); err != nil {
		return Document{}, fmt.Errorf("decode release-notes YAML: %w", err)
	}
	if err := validateAnnotationScalars(&source); err != nil {
		return Document{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document *Document
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return Document{}, errors.New("release-notes document is empty")
		}
		return Document{}, fmt.Errorf("decode release-notes YAML: %w", err)
	}
	if document == nil {
		return Document{}, errors.New("release-notes document is empty or null")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Document{}, fmt.Errorf("decode trailing release-notes YAML: %w", err)
		}
		return Document{}, errors.New("only one release-notes YAML document is allowed")
	}
	if document.SchemaVersion != 1 {
		return Document{}, errors.New("release-notes schema_version must be 1")
	}
	if strings.TrimSpace(document.Release.Version) == "" {
		return Document{}, errors.New("release.version is required in the release-notes document")
	}
	for i, component := range document.Components {
		if strings.TrimSpace(component.Project) == "" {
			return Document{}, fmt.Errorf("components[%d].project is required", i)
		}
		switch component.Status {
		case "changed", "unchanged", "failed":
		default:
			return Document{}, fmt.Errorf("components[%d].status must be changed, unchanged, or failed", i)
		}
	}
	return *document, nil
}

// Inspect annotations before decoding int64 fields: yaml.v3 otherwise accepts
// floating-point scalars by truncating them, which could select the wrong ID.
// This pass leaves strict field validation to the typed decoder above.
func validateAnnotationScalars(root *yaml.Node) error {
	visited := map[*yaml.Node]bool{}
	var walk func(*yaml.Node, string) error
	walk = func(node *yaml.Node, path string) error {
		node = annotationNode(node)
		if node == nil || visited[node] {
			return nil
		}
		visited[node] = true
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				field, value := node.Content[i].Value, node.Content[i+1]
				location := field
				if path != "" {
					location = path + "." + field
				}
				if field == "change_request" || field == "incident" {
					scalar := annotationNode(value)
					if scalar == nil || scalar.Kind != yaml.ScalarNode || scalar.ShortTag() != "!!int" && scalar.ShortTag() != "!!null" {
						return fmt.Errorf("%s must be an integer or null", location)
					}
				}
				if err := walk(value, location); err != nil {
					return err
				}
			}
		case yaml.SequenceNode:
			for i, child := range node.Content {
				if err := walk(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		case yaml.DocumentNode:
			for _, child := range node.Content {
				if err := walk(child, path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, "")
}

func annotationNode(node *yaml.Node) *yaml.Node {
	seen := map[*yaml.Node]bool{}
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil
		}
		seen[node] = true
		node = node.Alias
	}
	return node
}
