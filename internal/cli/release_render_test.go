package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const renderYAMLFixture = `schema_version: 1
release:
  title: Release 2.2.8
  version: 2.2.8
  date: "2026-11-27"
  summary: Restores customer checkout.
  draft: true
  from: 2.2.7
  to: 2.2.8
components:
  - project: group/service-a
    status: changed
    commits:
      - sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        title: "fix: restore checkout"
        remarks: Restores checkout after payment failures.
        change_request: 256
        incident: 128
        author: Alex
  - project: group/service-b
    status: changed
    commits:
      - sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        title: "fix: retry checkout"
        remarks: Restores checkout after payment failures.
        change_request: 256
        author: Morgan
      - sha: cccccccccccccccccccccccccccccccccccccccc
        title: Update documentation
        remarks: ""
  - project: group/unchanged
    status: unchanged
    commits: []
  - project: group/unavailable
    status: failed
    error: Baseline tag was inaccessible.
    commits: []
upgrade_notes:
  - Restart the checkout service.
known_issues:
  - Legacy payment retries may be delayed.
`

func writeRenderFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(renderYAMLFixture), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRenderUsesEditedYAMLWithoutConfigurationOrCredentials(t *testing.T) {
	workingDirectory(t)
	// The locator may be read for working_directory, but rendering must not
	// validate these operational GitLab settings or attempt authentication.
	if err := os.WriteFile("yas.yaml", []byte("version: 1\ngitlab:\n  url: invalid-operational-url\n  token: ''\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join("drafts", "release.yaml")
	writeRenderFixture(t, input)
	out, logs, err := executeTest([]string{"release", "render", "--input", input, "--format", "json", "--non-interactive"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Input              string `json:"input"`
		Output             string `json:"output"`
		GroupBy            string `json:"group_by"`
		Repositories       int    `json:"repositories"`
		Commits            int    `json:"commits"`
		FailedRepositories int    `json:"failed_repositories"`
		Draft              bool   `json:"draft"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil || summary.GroupBy != "repository" || summary.Repositories != 4 || summary.Commits != 3 || summary.FailedRepositories != 1 || !summary.Draft {
		t.Fatalf("summary=%s error=%v", out, err)
	}
	want, _ := filepath.Abs("release.html")
	if summary.Output != want || !strings.Contains(logs, want) {
		t.Fatalf("wrong default output: %s; logs=%q", out, logs)
	}
	html, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Release 2.2.8", "Restores checkout after payment failures.", "Baseline tag was inaccessible.", "group/unchanged", "Restart the checkout service."} {
		if !strings.Contains(string(html), text) {
			t.Errorf("HTML omits %q", text)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(want)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("output permissions: %v %v", info, err)
		}
	}
	original, err := os.ReadFile(input)
	if err != nil || string(original) != renderYAMLFixture {
		t.Fatalf("render changed input: %v", err)
	}
}

func TestReleaseRenderSupportsEveryInitialGroupingAndPreservesExistingOutput(t *testing.T) {
	workingDirectory(t)
	writeRenderFixture(t, "release.yaml")
	for _, group := range []string{"repository", "change-request", "incident", "remarks", "author"} {
		out, _, err := executeTest([]string{"release", "render", "--input", "release.yaml", "--output", group + ".html", "--group-by", group, "--format", "json"}, "", false)
		var result struct {
			GroupBy string `json:"group_by"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.GroupBy != group {
			t.Fatalf("group=%q result=%s error=%v", group, out, err)
		}
	}
	before, err := os.ReadFile("repository.html")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executeTest([]string{"release", "render", "--input", "release.yaml", "--output", "repository.html"}, "", false)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "not overwritten") {
		t.Fatalf("existing output error=%v", err)
	}
	after, err := os.ReadFile("repository.html")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing HTML was changed: %v", err)
	}
}

func TestReleaseRenderInteractiveCommandAndRootMenuWithoutConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		input string
		group string
	}{
		{name: "direct accepts default", args: []string{"release", "render", "--format", "json"}, input: "release.yaml\n\n\n", group: "repository"},
		{name: "root chooses remarks", args: []string{"--format", "json"}, input: "8\nrelease.yaml\n4\n\n", group: "remarks"},
		{name: "explicit options bypass prompts", args: []string{"release", "render", "--input", "release.yaml", "--group-by", "author", "--output", "release.html", "--format", "json"}, group: "author"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			writeRenderFixture(t, "release.yaml")
			out, logs, err := executeTest(tc.args, tc.input, true)
			var result struct {
				GroupBy string `json:"group_by"`
			}
			if err != nil {
				t.Fatalf("output=%q logs=%q error=%v", out, logs, err)
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.GroupBy != tc.group {
				t.Fatalf("result=%s error=%v", out, err)
			}
			if tc.input == "" && strings.Contains(logs, "Group commits by:") {
				t.Fatalf("explicit options prompted: %s", logs)
			}
		})
	}
}

func TestReleaseRenderValidationDoesNotCreateOutput(t *testing.T) {
	for _, extra := range [][]string{
		{"--group-by", "unknown"}, {"--group-by", ""}, {"--output", "bad.txt"},
		{"--output", ""}, {"--dry-run"}, {"--format", "invalid"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			workingDirectory(t)
			writeRenderFixture(t, "release.yaml")
			args := append([]string{"release", "render", "--input", "release.yaml"}, extra...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 {
				t.Fatalf("args=%v error=%v", args, err)
			}
			if _, err := os.Stat("release.html"); !os.IsNotExist(err) {
				t.Fatalf("invalid invocation created output: %v", err)
			}
		})
	}
}

func TestReleaseRenderSavedHTMLSurvivesBrokenTerminalOutput(t *testing.T) {
	workingDirectory(t)
	writeRenderFixture(t, "release.yaml")
	command := New(Options{Out: unavailableReportOutput{}, Err: &bytes.Buffer{}, ConfigPath: "yas.yaml"})
	command.SetArgs([]string{"release", "render", "--input", "release.yaml"})
	err := command.ExecuteContext(context.Background())
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("terminal error=%v", err)
	}
	if data, err := os.ReadFile("release.html"); err != nil || !strings.Contains(string(data), "Release 2.2.8") {
		t.Fatalf("saved HTML was lost: %v", err)
	}
}
