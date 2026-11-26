package release

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
	"go.yaml.in/yaml/v3"
)

func TestScannedCommitsExposeUnfilledExternalEventIDs(t *testing.T) {
	source := newFakeSource()
	source.commitRange.Commits = []gitlab.ReleaseCommit{
		{SHA: targetSHA, Title: "fix: restore checkout", Message: "Incident 128; change_request: 256"},
		{SHA: strings.Repeat("c", 40), Title: "feat: improve release reporting"},
	}
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
	if component.Status != "changed" || len(component.Commits) != 2 {
		t.Fatalf("scan=%#v", component)
	}
	for _, commit := range component.Commits {
		if commit.ChangeRequest != nil || commit.Incident != nil {
			t.Fatalf("IDs must remain for the user to fill, not be inferred: %#v", commit)
		}
	}
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var data []byte
			var err error
			var document map[string]any
			if format == "yaml" {
				data, err = yaml.Marshal(component)
				if err == nil {
					err = yaml.Unmarshal(data, &document)
				}
			} else {
				data, err = json.Marshal(component)
				if err == nil {
					err = json.Unmarshal(data, &document)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			commits, ok := document["commits"].([]any)
			if !ok || len(commits) != 2 {
				t.Fatalf("serialized commits=%s", data)
			}
			for _, entry := range commits {
				commit, ok := entry.(map[string]any)
				if !ok {
					t.Fatalf("invalid commit object=%#v", entry)
				}
				for _, field := range []string{"change_request", "incident"} {
					value, exists := commit[field]
					if !exists || value != nil {
						t.Errorf("%s must be present and null for each commit: %s", field, data)
					}
				}
			}
		})
	}
}

func TestUserFilledExternalEventIDsRemainNumericAndOptional(t *testing.T) {
	const edited = `project: group/service
status: changed
commits:
  - sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    title: "fix: restore checkout"
    change_request: 256
    incident: 128
  - sha: cccccccccccccccccccccccccccccccccccccccc
    title: "feat: improve release reporting"
    change_request: null
    incident: null
`
	var component Component
	if err := yaml.Unmarshal([]byte(edited), &component); err != nil {
		t.Fatal(err)
	}
	first, second := component.Commits[0], component.Commits[1]
	if first.ChangeRequest == nil || *first.ChangeRequest != 256 || first.Incident == nil || *first.Incident != 128 || second.ChangeRequest != nil || second.Incident != nil {
		t.Fatalf("edited associations were not preserved: %#v", component.Commits)
	}
	data, err := yaml.Marshal(component)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"change_request: 256", "incident: 128", "change_request: null", "incident: null"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("missing numeric/optional association %q: %s", field, data)
		}
	}
}
