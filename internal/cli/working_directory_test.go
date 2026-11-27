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
)

func workspaceCommand(t *testing.T, bootstrap string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := New(Options{ConfigPath: bootstrap, In: strings.NewReader(""), Out: &stdout, Err: &stderr})
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func writeWorkspaceLocator(t *testing.T, binaryDirectory, workingDirectory string) string {
	t.Helper()
	if err := os.MkdirAll(binaryDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(binaryDirectory, "yas.yaml")
	if err := os.WriteFile(filename, []byte(fmt.Sprintf("version: 1\nworking_directory: %q\n", workingDirectory)), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestWorkingDirectoryInitializesAndLoadsOperationalConfig(t *testing.T) {
	workingDirectory(t)
	caller, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binaryDirectory := filepath.Join(root, "binary")
	workspace := filepath.Join(root, "workspace", "nested")
	bootstrap := writeWorkspaceLocator(t, binaryDirectory, filepath.Join("..", "workspace", "nested"))
	before, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("yas.yaml", []byte("malformed: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wantConfig := filepath.Join(workspace, "yas.yaml")
	_, _, err = workspaceCommand(t, bootstrap, "config", "validate")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), wantConfig) || !strings.Contains(err.Error(), "yas config init") {
		t.Fatalf("missing operational config was not reported: %v", err)
	}
	out, _, err := workspaceCommand(t, bootstrap, "config", "init")
	if err != nil || !strings.Contains(out, wantConfig) {
		t.Fatalf("config init output=%q error=%v", out, err)
	}
	if _, _, err := workspaceCommand(t, bootstrap, "config", "validate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspaceCommand(t, bootstrap, "config", "init"); ExitCode(err) != 2 || !strings.Contains(err.Error(), "not overwritten") {
		t.Fatalf("operational config was not protected: %v", err)
	}

	t.Setenv("YAS_WORKSPACE_AUTH_TEST", "")
	operational := "version: 1\ngitlab:\n  token_env: YAS_WORKSPACE_AUTH_TEST\n"
	if err := os.WriteFile(wantConfig, []byte(operational), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = workspaceCommand(t, bootstrap, "tag", "one", "--project", "group/service", "--branch", "main", "--tag", "v1", "--non-interactive")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "YAS_WORKSPACE_AUTH_TEST") {
		t.Fatalf("operation loaded the locator instead of workspace config: %v", err)
	}
	after, err := os.ReadFile(bootstrap)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("binary-side locator was changed: %v", err)
	}
	current, err := os.Getwd()
	if err != nil || current != caller {
		t.Fatalf("command changed process cwd: %q %v", current, err)
	}
}

func TestWorkingDirectoryRoutesReleaseInputAndAllOutputChoices(t *testing.T) {
	caller := workingDirectory(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	bootstrap := writeWorkspaceLocator(t, filepath.Join(root, "binary"), workspace)
	input := filepath.Join(workspace, "drafts", "release.yaml")
	writeRenderFixture(t, input)
	if err := os.MkdirAll("drafts", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("drafts", "release.yaml"), []byte("wrong caller input\n"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, input, output, want string
	}{
		{name: "default", input: filepath.Join("drafts", "release.yaml"), want: filepath.Join(workspace, "release.html")},
		{name: "relative output", input: filepath.Join("drafts", "release.yaml"), output: filepath.Join("published", "notes.html"), want: filepath.Join(workspace, "published", "notes.html")},
		{name: "absolute output", input: input, output: filepath.Join(root, "explicit", "notes.html"), want: filepath.Join(root, "explicit", "notes.html")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"release", "render", "--input", tc.input, "--format", "json"}
			if tc.output != "" {
				args = append(args, "--output", tc.output)
			}
			out, _, err := workspaceCommand(t, bootstrap, args...)
			if err != nil {
				t.Fatal(err)
			}
			var result struct{ Input, Output string }
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Input != input || result.Output != tc.want {
				t.Fatalf("wrong workspace paths: %s error=%v", out, err)
			}
			if _, err := os.Stat(tc.want); err != nil {
				t.Fatal(err)
			}
		})
	}
	external := filepath.Join(root, "external", "external-release.yaml")
	writeRenderFixture(t, external)
	if _, _, err := workspaceCommand(t, bootstrap, "release", "render", "--input", external); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "external-release.html")); err != nil {
		t.Fatalf("default HTML did not use working directory for external input: %v", err)
	}
	for _, unexpected := range []string{
		filepath.Join(caller, "release.html"), filepath.Join(caller, "published"),
		filepath.Join(root, "binary", "release.html"), filepath.Join(root, "external", "external-release.html"),
		filepath.Join(workspace, "yas.yaml"),
	} {
		if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
			t.Fatalf("unexpected config or output at %s: %v", unexpected, err)
		}
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != renderYAMLFixture {
		t.Fatalf("release input was changed: %v", err)
	}
}

func TestWorkingDirectoryDefaultsLocalRenderingToBinaryDirectory(t *testing.T) {
	caller := workingDirectory(t)
	binaryDirectory := t.TempDir()
	bootstrap := filepath.Join(binaryDirectory, "yas.yaml") // No locator or operational config exists.
	writeRenderFixture(t, filepath.Join(binaryDirectory, "release.yaml"))
	if _, _, err := workspaceCommand(t, bootstrap, "release", "render", "--input", "release.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(binaryDirectory, "release.html")); err != nil {
		t.Fatalf("default output did not use binary directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(caller, "release.html")); !os.IsNotExist(err) {
		t.Fatalf("default output used caller directory: %v", err)
	}
	if _, err := os.Stat(bootstrap); !os.IsNotExist(err) {
		t.Fatalf("local render created an unwanted config file: %v", err)
	}
}

func TestWorkingDirectoryRoutesReleaseScanDraft(t *testing.T) {
	caller := workingDirectory(t)
	fixture := newReleaseScanFixture(t, nil)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	bootstrap := writeWorkspaceLocator(t, filepath.Join(root, "binary"), workspace)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	operational := fmt.Sprintf("version: 1\ngitlab:\n  url: %s\n  token: test-token\nrelease:\n  repositories:\n    - project: group/changed\n", fixture.server.URL)
	if err := os.WriteFile(filepath.Join(workspace, "yas.yaml"), []byte(operational), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspaceCommand(t, bootstrap, "release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--no-progress"); err != nil {
		t.Fatal(err)
	}
	draft := readReleaseDraft(t, filepath.Join(workspace, "release-notes-2.2.8.yaml"))
	if len(draft.Components) != 1 || draft.Components[0].Status != "changed" {
		t.Fatalf("wrong operational scan result: %+v", draft)
	}
	if _, err := os.Stat(filepath.Join(caller, "release-notes-2.2.8.yaml")); !os.IsNotExist(err) {
		t.Fatalf("draft leaked into caller directory: %v", err)
	}
}
