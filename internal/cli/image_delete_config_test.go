package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useSixImageDeletionConfigMatches(t *testing.T) {
	t.Helper()
	var inventory bytes.Buffer
	for i := 0; i < 6; i++ {
		if err := json.NewEncoder(&inventory).Encode(map[string]string{
			"Repository": fmt.Sprintf("configured/service-%02d", i), "Tag": "selected", "ID": "sha256:" + strings.Repeat("a", 64),
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("YAS_IMAGE_TEST_INVENTORY", inventory.String())
	t.Setenv("GITLAB_TOKEN", "")
}

func assertImageDeletionConfigReport(t *testing.T, output, directory string, concurrency int) {
	t.Helper()
	results := decodeImageDeleteResults(t, output)
	if len(results) != 6 {
		t.Fatalf("expected six image references: %s", output)
	}
	for _, result := range results {
		if result.Status != "planned" {
			t.Fatalf("configuration test performed a mutation: %+v", result)
		}
	}
	paths, err := filepath.Glob(filepath.Join(directory, "reports", "*.html"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one report under %s: %v (%v)", directory, paths, err)
	}
	if report := readReportHTML(t, paths[0]); !strings.Contains(report, fmt.Sprintf("<dt>Concurrency</dt><dd>%d</dd>", concurrency)) {
		t.Fatalf("image-deletion report did not use effective concurrency %d", concurrency)
	}
}

func TestDockerImageDeleteConfigConcurrencyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, config, override string
		want                   int
	}{
		{name: "missing config defaults to four", want: 4},
		{name: "omitted setting defaults to four", config: "version: 1\n", want: 4},
		{name: "configured two", config: "image_deletion:\n  concurrency: 2\n", want: 2},
		{name: "CLI overrides configured two", config: "image_deletion:\n  concurrency: 2\n", override: "3", want: 3},
		{name: "CLI overrides default", override: "1", want: 1},
		{name: "CLI replaces invalid zero", config: "image_deletion:\n  concurrency: 0\n", override: "2", want: 2},
		{name: "CLI replaces invalid negative", config: "image_deletion:\n  concurrency: -4\n", override: "3", want: 3},
		{name: "GitLab and other operation settings are not validated", config: "version: 99\ngitlab:\n  url: not-a-url\n  token_env: BAD-NAME\ntagging:\n  concurrency: 0\n  timeout: invalid\nbuilding:\n  concurrency: 0\nrelease:\n  concurrency: 0\nimage_deletion:\n  concurrency: 2\n", want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			useSixImageDeletionConfigMatches(t)
			if tc.config != "" {
				if err := os.WriteFile("yas.yaml", []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"image", "delete", "batch", "--tag", "selected", "--dry-run", "--format", "json", "--no-progress"}
			if tc.override != "" {
				args = append(args, "--concurrency", tc.override)
			}
			output, logs, err := executeTest(args, "", false)
			if err != nil {
				t.Fatalf("local image command rejected effective settings: %v\n%s", err, logs)
			}
			assertImageDeletionConfigReport(t, output, filepath.Dir(fixture.log), tc.want)
			if commands := fixture.commands(t); len(commands) != 1 || !strings.HasPrefix(commands[0], "image ls ") {
				t.Fatalf("config/dry-run test executed unexpected Docker commands: %v", commands)
			}
		})
	}
}

func TestDockerImageDeleteRejectsInvalidConfigBeforeDockerEvenWithoutReports(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		flags        []string
	}{
		{name: "zero", config: "image_deletion:\n  concurrency: 0\n"},
		{name: "negative", config: "image_deletion:\n  concurrency: -2\n"},
		{name: "malformed YAML", config: "image_deletion: [\n"},
		{name: "nonnumeric setting cannot be repaired by CLI override", config: "image_deletion:\n  concurrency: fast\n", flags: []string{"--concurrency", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			if err := os.WriteFile("yas.yaml", []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"image", "delete", "batch", "--tag", "selected", "--dry-run", "--no-report", "--no-progress"}, tc.flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || len(fixture.commands(t)) != 0 {
				t.Fatalf("invalid configuration reached Docker: error=%v commands=%v", err, fixture.commands(t))
			}
			if _, err := os.Stat("reports"); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration wrote report artifacts: %v", err)
			}
		})
	}
}

func TestDockerImageDeleteUsesWorkspaceConfigInsteadOfLocatorOrCaller(t *testing.T) {
	for _, operationalConfig := range []bool{true, false} {
		t.Run(map[bool]string{true: "workspace value", false: "missing workspace config uses default"}[operationalConfig], func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			useSixImageDeletionConfigMatches(t)
			caller := filepath.Dir(fixture.log)
			binaryDirectory, workspace := t.TempDir(), t.TempDir()
			bootstrap := filepath.Join(binaryDirectory, "yas.yaml")
			if err := os.WriteFile(bootstrap, []byte(fmt.Sprintf("working_directory: %q\nimage_deletion:\n  concurrency: 5\n", workspace)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(caller, "yas.yaml"), []byte("image_deletion:\n  concurrency: 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want := 4
			if operationalConfig {
				want = 2
				if err := os.WriteFile(filepath.Join(workspace, "yas.yaml"), []byte("version: 99\ngitlab: {url: invalid, token_env: BAD-NAME}\nimage_deletion:\n  concurrency: 2\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			output, logs, err := workspaceCommand(t, bootstrap, "image", "delete", "batch", "--tag", "selected", "--dry-run", "--format", "json", "--no-progress")
			if err != nil {
				t.Fatalf("workspace image-deletion settings failed: %v\n%s", err, logs)
			}
			assertImageDeletionConfigReport(t, output, workspace, want)
			for _, directory := range []string{caller, binaryDirectory} {
				if _, err := os.Stat(filepath.Join(directory, "reports")); !os.IsNotExist(err) {
					t.Errorf("image report used non-workspace directory %s: %v", directory, err)
				}
			}
			if len(fixture.commands(t)) != 1 {
				t.Fatalf("workspace config test executed unexpected Docker commands: %v", fixture.commands(t))
			}
		})
	}
}

func TestDockerImageDeleteRejectsBrokenOperationalConfigSymlink(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	workspace := t.TempDir()
	bootstrap := writeWorkspaceLocator(t, t.TempDir(), workspace)
	if err := os.Symlink(filepath.Join(workspace, "missing.yaml"), filepath.Join(workspace, "yas.yaml")); err != nil {
		t.Fatal(err)
	}
	_, _, err := workspaceCommand(t, bootstrap, "image", "delete", "batch", "--tag", "selected", "--dry-run", "--no-report")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), filepath.Join(workspace, "yas.yaml")) || len(fixture.commands(t)) != 0 {
		t.Fatalf("broken config symlink was treated as an absent config: %v; commands=%v", err, fixture.commands(t))
	}
}
