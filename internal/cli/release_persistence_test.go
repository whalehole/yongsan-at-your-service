package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestCanceledReleaseScanWritesEverySelectedComponent(t *testing.T) {
	workingDirectory(t)
	fixture := newReleaseScanFixture(t, nil)
	writeConfig(t, fixture.server.URL, "release:\n  repositories:\n    - project: group/first\n    - project: group/queued\n")
	var stdout, stderr bytes.Buffer
	command := New(Options{Out: &stdout, Err: &stderr, ConfigPath: "yas.yaml"})
	command.SetArgs([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "canceled.yaml", "--concurrency", "1", "--format", "json", "--no-progress"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := command.ExecuteContext(ctx)
	if ExitCode(err) != 1 || fixture.requests.Load() != 0 {
		t.Fatalf("canceled scan error=%v requests=%d", err, fixture.requests.Load())
	}
	draft := readReleaseDraft(t, "canceled.yaml")
	if draft.Scan.Failed != 2 || len(draft.Components) != 2 {
		t.Fatalf("canceled components were lost: %#v", draft)
	}
	for i, project := range []string{"group/first", "group/queued"} {
		component := draft.Components[i]
		if component.Project != project || component.Status != "failed" || component.FromRef != "2.2.7" || component.ToRef != "2.2.8" || !strings.Contains(component.Error, "context canceled") {
			t.Fatalf("canceled component lacks identity or outcome: %#v", component)
		}
	}
}

func TestReleaseDraftSurvivesBrokenTerminalOutput(t *testing.T) {
	workingDirectory(t)
	fixture := newReleaseScanFixture(t, nil)
	writeConfig(t, fixture.server.URL, "release:\n  repositories:\n    - project: group/changed\n")
	command := New(Options{Out: unavailableReportOutput{}, Err: io.Discard, ConfigPath: "yas.yaml"})
	command.SetArgs([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "saved.yaml", "--no-progress"})
	err := command.ExecuteContext(context.Background())
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("broken stdout error=%v", err)
	}
	draft := readReleaseDraft(t, "saved.yaml")
	if len(draft.Components) != 1 || draft.Components[0].Status != "changed" || len(draft.Components[0].Commits) != 2 {
		t.Fatalf("completed scan was not saved before output failure: %#v", draft)
	}
}
