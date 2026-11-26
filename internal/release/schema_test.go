package release

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
	"go.yaml.in/yaml/v3"
)

func TestComponentSerializesOnlyOneEditableCommitList(t *testing.T) {
	for _, status := range []string{"changed", "unchanged", "failed"} {
		t.Run(status, func(t *testing.T) {
			source := newFakeSource()
			if status != "unchanged" {
				source.commitRange.Commits = []gitlab.ReleaseCommit{{SHA: targetSHA, Title: "feat: preserve the actual commit"}}
			}
			if status == "failed" {
				source.commitsErr = errors.New("commit listing is incomplete")
			}
			component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
			if component.Status != status {
				t.Fatalf("status = %s, want %s: %#v", component.Status, status, component)
			}
			for _, format := range []string{"json", "yaml"} {
				var document map[string]any
				var data []byte
				var err error
				if format == "json" {
					data, err = json.Marshal(component)
					if err == nil {
						err = json.Unmarshal(data, &document)
					}
				} else {
					data, err = yaml.Marshal(component)
					if err == nil {
						err = yaml.Unmarshal(data, &document)
					}
				}
				if err != nil {
					t.Fatalf("%s serialization: %v", format, err)
				}
				if _, exists := document["changes"]; exists {
					t.Errorf("%s generated a second editable changes list: %s", format, data)
				}
				commits, ok := document["commits"].([]any)
				if !ok || len(commits) != len(component.Commits) {
					t.Errorf("%s commits list is missing, null, or altered: %s", format, data)
				}
				if status != "changed" && len(commits) != 0 {
					t.Errorf("%s generated commits for %s scan: %s", format, status, data)
				}
			}
		})
	}
}
