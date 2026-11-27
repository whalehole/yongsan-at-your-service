package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func batchRenderResults(t *testing.T, output string) []releaseRenderOutcome {
	t.Helper()
	var results []releaseRenderOutcome
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("invalid batch JSON %q: %v", output, err)
	}
	return results
}

func TestReleaseRenderBatchContinuesAfterInvalidMissingAndExistingOutputs(t *testing.T) {
	workingDirectory(t)
	for _, name := range []string{"first.yaml", "existing.yaml", "last.yaml"} {
		writeRenderFixture(t, name)
	}
	if err := os.WriteFile("invalid.yaml", []byte("invalid: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(filepath.FromSlash(releaseHTMLDirectory), "existing.html")
	if err := os.MkdirAll(filepath.Dir(existing), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"release", "render", "--format", "json", "--group-by", "author"}
	names := []string{"first.yaml", "invalid.yaml", "missing.yaml", "existing.yaml", "last.yaml"}
	for _, name := range names {
		args = append(args, "--input", name)
	}
	out, _, err := executeTest(args, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("batch error=%v", err)
	}
	results := batchRenderResults(t, out)
	if len(results) != len(names) {
		t.Fatalf("results=%s", out)
	}
	for i, result := range results {
		if filepath.Base(result.Input) != names[i] || result.GroupBy != "author" {
			t.Errorf("wrong result order/group: %+v", result)
		}
		if i == 0 || i == 4 {
			if result.Status != "rendered" || result.Commits != 3 || result.Error != "" {
				t.Errorf("valid input failed: %+v", result)
			}
			if _, err := os.Stat(result.Output); err != nil {
				t.Error(err)
			}
		} else if result.Status != "failed" || result.Error == "" {
			t.Errorf("failure was omitted: %+v", result)
		}
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "do not overwrite" {
		t.Fatalf("existing HTML changed: %q %v", data, err)
	}
	for _, name := range []string{"first.yaml", "existing.yaml", "last.yaml"} {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != renderYAMLFixture {
			t.Fatalf("input changed: %s %v", name, err)
		}
	}
}

func TestReleaseRenderBatchRejectsAllCollisionsAndRendersOtherFiles(t *testing.T) {
	workingDirectory(t)
	for _, name := range []string{"one/shared.yaml", "two/shared.yml", "unique.yaml"} {
		writeRenderFixture(t, filepath.FromSlash(name))
	}
	out, _, err := executeTest([]string{"release", "render", "--input", "one/shared.yaml", "--input", "two/shared.yml", "--input", "unique.yaml", "--output-dir", "published", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("collision error=%v", err)
	}
	results := batchRenderResults(t, out)
	if len(results) != 3 || results[2].Status != "rendered" {
		t.Fatalf("results=%s", out)
	}
	for _, result := range results[:2] {
		if result.Status != "failed" || !strings.Contains(result.Error, "collision") {
			t.Errorf("collision result=%+v", result)
		}
	}
	if _, err := os.Stat(filepath.Join("published", "shared.html")); !os.IsNotExist(err) {
		t.Fatalf("colliding destination was written: %v", err)
	}
}

func TestReleaseRenderRepeatedInputDeduplicatesAndKeepsSingleJSON(t *testing.T) {
	workingDirectory(t)
	writeRenderFixture(t, "release.yaml")
	out, _, err := executeTest([]string{"release", "render", "--input", "release.yaml", "--input", "./release.yaml", "--output-dir", "custom", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["group_by"] != "repository" || result["commits"] != float64(3) {
		t.Fatalf("single JSON changed: %s, %v", out, err)
	}
	if _, exists := result["status"]; exists {
		t.Fatalf("single-file JSON acquired batch fields: %s", out)
	}
	if _, err := os.Stat(filepath.Join("custom", "release.html")); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRenderPickerUsesRecentOrderAndSharedOptions(t *testing.T) {
	workingDirectory(t)
	directory := filepath.FromSlash(releaseYAMLDirectory)
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"newest.yaml", "older.yaml"} {
		path := filepath.Join(directory, name)
		writeRenderFixture(t, path)
		modified := base.Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	writeRenderFixture(t, "outside-picker.yaml")
	out, logs, err := executeTest([]string{"release", "render", "--format", "json"}, " \x1b[B \n4\n\n", true)
	if err != nil {
		t.Fatalf("logs=%s error=%v", logs, err)
	}
	results := batchRenderResults(t, out)
	if len(results) != 2 || filepath.Base(results[0].Input) != "newest.yaml" || filepath.Base(results[1].Input) != "older.yaml" {
		t.Fatalf("wrong recent order: %s", out)
	}
	for _, result := range results {
		if result.GroupBy != "remarks" || result.Status != "rendered" {
			t.Fatalf("shared options not applied: %+v", result)
		}
	}
	if strings.Count(logs, "Group commits by:") != 1 || strings.Count(logs, "HTML output directory [") != 1 || strings.Contains(logs, "outside-picker.yaml") {
		t.Fatalf("wrong picker scope/prompts: %s", logs)
	}
}

func TestReleaseRenderManualEntryAddsToSelectionAndDeduplicates(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "adds external file", true: "deduplicates relative filename"}[duplicate], func(t *testing.T) {
			workingDirectory(t)
			writeRenderFixture(t, filepath.Join(filepath.FromSlash(releaseYAMLDirectory), "selected.yaml"))
			manual := filepath.Join(t.TempDir(), "manual.yaml")
			writeRenderFixture(t, manual)
			if duplicate {
				manual = "selected.yaml"
			}
			keys := " \x1b[B \n" + manual + "\n\n\n"
			out, logs, err := executeTest([]string{"release", "render", "--format", "json"}, keys, true)
			if err != nil {
				t.Fatalf("logs=%s error=%v", logs, err)
			}
			if duplicate {
				var result releaseRenderSummary
				if err := json.Unmarshal([]byte(out), &result); err != nil || filepath.Base(result.Input) != "selected.yaml" {
					t.Fatalf("manual duplicate wasn't removed: %s %v", out, err)
				}
			} else if results := batchRenderResults(t, out); len(results) != 2 || results[1].Input != manual {
				t.Fatalf("manual input wasn't added: %s", out)
			}
		})
	}
}

func TestReleaseRenderPickerFallbacksAllowManualAbsoluteInput(t *testing.T) {
	for _, state := range []string{"missing", "empty", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			workingDirectory(t)
			directory := filepath.FromSlash(releaseYAMLDirectory)
			if state != "missing" {
				if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
					t.Fatal(err)
				}
				if state == "empty" {
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(directory, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manual := filepath.Join(t.TempDir(), "manual.yaml")
			writeRenderFixture(t, manual)
			_, logs, err := executeTest([]string{"release", "render", "--group-by", "repository", "--output-dir", "rendered"}, manual+"\n", true)
			if err != nil || !strings.Contains(logs, "Enter a filename manually") {
				t.Fatalf("manual fallback logs=%s error=%v", logs, err)
			}
			if _, err := os.Stat(filepath.Join("rendered", "manual.html")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type cancelAfterFirstHTML struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelAfterFirstHTML) Write(data []byte) (int, error) {
	count, err := w.buffer.Write(data)
	if strings.Contains(string(data), "HTML release notes:") {
		w.cancel()
	}
	return count, err
}

func TestReleaseRenderBatchCancellationPreservesCompletedHTML(t *testing.T) {
	workingDirectory(t)
	for _, name := range []string{"first.yaml", "second.yaml", "third.yaml"} {
		writeRenderFixture(t, name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	logs := &cancelAfterFirstHTML{cancel: cancel}
	command := New(Options{Out: &output, Err: logs, ConfigPath: "yas.yaml"})
	command.SetArgs([]string{"release", "render", "--input", "first.yaml", "--input", "second.yaml", "--input", "third.yaml", "--format", "json"})
	err := command.ExecuteContext(ctx)
	if ExitCode(err) != 1 {
		t.Fatalf("cancel error=%v", err)
	}
	results := batchRenderResults(t, output.String())
	if len(results) != 3 || results[0].Status != "rendered" || results[1].Status != "failed" || results[2].Status != "failed" {
		t.Fatalf("cancel results=%s", output.String())
	}
	if _, err := os.Stat(results[0].Output); err != nil {
		t.Fatal(err)
	}
	for _, result := range results[1:] {
		if !strings.Contains(result.Error, "canceled") {
			t.Errorf("missing canceled outcome: %+v", result)
		}
		if _, err := os.Stat(result.Output); !os.IsNotExist(err) {
			t.Errorf("canceled output was created: %v", err)
		}
	}
}

func TestReleaseRenderBatchRejectsAmbiguousOutputFlagsBeforeWriting(t *testing.T) {
	for _, extra := range [][]string{{"--output", "one.html"}, {"--output", "one.html", "--output-dir", "out"}, {"--output-dir", ""}} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			workingDirectory(t)
			writeRenderFixture(t, "first.yaml")
			writeRenderFixture(t, "second.yaml")
			args := append([]string{"release", "render", "--input", "first.yaml", "--input", "second.yaml"}, extra...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 {
				t.Fatalf("invalid flags error=%v", err)
			}
			if _, err := os.Stat(filepath.FromSlash(releaseHTMLDirectory)); !os.IsNotExist(err) {
				t.Fatalf("invalid flags generated outputs: %v", err)
			}
		})
	}
}

func TestReleaseRenderPickerCancellationDoesNotCreateHTML(t *testing.T) {
	for _, key := range []string{"\x03", "\x04"} {
		t.Run(fmt.Sprintf("key_%d", key[0]), func(t *testing.T) {
			workingDirectory(t)
			writeRenderFixture(t, filepath.Join(filepath.FromSlash(releaseYAMLDirectory), "draft.yaml"))
			_, _, err := executeTest([]string{"release", "render"}, key, true)
			if ExitCode(err) != 1 {
				t.Fatalf("picker cancellation exit: %v", err)
			}
			if _, err := os.Stat(filepath.FromSlash(releaseHTMLDirectory)); !os.IsNotExist(err) {
				t.Fatalf("canceled picker generated HTML: %v", err)
			}
		})
	}
}
