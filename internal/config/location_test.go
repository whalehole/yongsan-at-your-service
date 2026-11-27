package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeLocationConfig(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func locationTestWorkingDirectory(t *testing.T, directory string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
}

func TestResolveLocationDefaultsWithoutCreatingFiles(t *testing.T) {
	directory := t.TempDir()
	bootstrap := filepath.Join(directory, Filename)
	location, err := ResolveLocation(bootstrap)
	if err != nil || location != (Location{BootstrapPath: bootstrap, ConfigPath: bootstrap, WorkingDirectory: directory}) {
		t.Fatalf("default location=%+v error=%v", location, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("resolving a missing locator wrote files: %v; %v", entries, err)
	}
	executableLocator, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	location, err = ResolveLocation("")
	if err != nil || location.BootstrapPath != executableLocator || location.WorkingDirectory != filepath.Dir(executableLocator) || location.ConfigPath != executableLocator {
		t.Fatalf("default process location=%+v error=%v", location, err)
	}
}

func TestResolveLocationAnchorsRelativeWorkspaceToBinaryDirectory(t *testing.T) {
	root := t.TempDir()
	binaryDirectory := filepath.Join(root, "binary directory")
	callerDirectory := filepath.Join(root, "caller")
	if err := os.MkdirAll(callerDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	locationTestWorkingDirectory(t, callerDirectory)
	writeLocationConfig(t, filepath.Join(callerDirectory, Filename), "working_directory: wrong-caller-workspace\n")
	bootstrap := filepath.Join(binaryDirectory, Filename)
	for _, tc := range []struct{ setting, want string }{
		{"", binaryDirectory},
		{".", binaryDirectory},
		{"workspace", filepath.Join(binaryDirectory, "workspace")},
		{"../shared workspace", filepath.Join(root, "shared workspace")},
		{filepath.Join(root, "absolute workspace"), filepath.Join(root, "absolute workspace")},
	} {
		writeLocationConfig(t, bootstrap, fmt.Sprintf("working_directory: %q\n", tc.setting))
		location, err := ResolveLocation(bootstrap)
		if err != nil || location.BootstrapPath != bootstrap || location.WorkingDirectory != tc.want || location.ConfigPath != filepath.Join(tc.want, Filename) {
			t.Errorf("setting=%q location=%+v error=%v", tc.setting, location, err)
		}
	}
	writeLocationConfig(t, bootstrap, "working_directory: null\n")
	location, err := ResolveLocation(bootstrap)
	if err != nil || location.WorkingDirectory != binaryDirectory {
		t.Fatalf("null workspace did not retain the default: %+v; %v", location, err)
	}
}

func TestResolveLocationLoadsOnlyTheAuthoritativeLocator(t *testing.T) {
	root := t.TempDir()
	binaryDirectory := filepath.Join(root, "bin")
	workspace := filepath.Join(root, "workspace")
	bootstrap := filepath.Join(binaryDirectory, Filename)
	target := filepath.Join(workspace, Filename)
	writeLocationConfig(t, bootstrap, "working_directory: ../workspace\nversion: 99\ngitlab: {url: not-a-url, token_env: BAD-NAME}\ntagging: {concurrency: 0, timeout: invalid}\nbuilding: {concurrency: 0}\nrelease: {concurrency: 0}\n")
	writeLocationConfig(t, target, "version: 1\nworking_directory: ../must-not-follow\nrelease:\n  repositories:\n    - project: release-group/selected\n")
	writeLocationConfig(t, filepath.Join(root, "must-not-follow", Filename), "version: 1\nrelease:\n  repositories:\n    - project: wrong-group/redirected\n")
	location, err := ResolveLocation(bootstrap)
	if err != nil || location.WorkingDirectory != workspace || location.ConfigPath != target {
		t.Fatalf("locator unexpectedly validated operation settings or followed a redirect: %+v; %v", location, err)
	}
	cfg, err := LoadFile(location.ConfigPath)
	if err != nil || len(cfg.Release.Repositories) != 1 || cfg.Release.Repositories[0].Project != "release-group/selected" || cfg.WorkingDirectory != "../must-not-follow" {
		t.Fatalf("operational config was not loaded raw: %+v; %v", cfg, err)
	}
	bootstrapConfig, err := LoadFile(bootstrap)
	if err != nil || bootstrapConfig.Version != 99 || bootstrapConfig.WorkingDirectory != "../workspace" {
		t.Fatalf("LoadFile unexpectedly redirected instead of reading its argument: %+v; %v", bootstrapConfig, err)
	}
	writeLocationConfig(t, target, "malformed: [\n")
	if got, err := ResolveLocation(bootstrap); err != nil || got != location {
		t.Fatalf("resolver read the operational target instead of just its locator: %+v; %v", got, err)
	}
}

func TestResolveLocationAllowsMissingWorkspaceAndTargetForInitialization(t *testing.T) {
	root := t.TempDir()
	bootstrap := filepath.Join(root, "bin", Filename)
	workspace := filepath.Join(root, "not-created", "nested workspace")
	writeLocationConfig(t, bootstrap, "working_directory: ../not-created/nested workspace\n")
	location, err := ResolveLocation(bootstrap)
	if err != nil || location.WorkingDirectory != workspace || location.ConfigPath != filepath.Join(workspace, Filename) {
		t.Fatalf("missing workspace was rejected: %+v; %v", location, err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-created")); !os.IsNotExist(err) {
		t.Fatalf("resolver created a workspace directory: %v", err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveLocation(bootstrap); err != nil || got != location {
		t.Fatalf("existing workspace without operational config was rejected: %+v; %v", got, err)
	}
}

func TestResolveLocationRejectsInvalidLocatorOrWorkspace(t *testing.T) {
	root := t.TempDir()
	bootstrap := filepath.Join(root, "bin", Filename)
	workspaceFile := filepath.Join(root, "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("preserve existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	writeLocationConfig(t, bootstrap, "working_directory: ../workspace-file\n")
	if _, err := ResolveLocation(bootstrap); err == nil || !strings.Contains(err.Error(), "working_directory") || !strings.Contains(err.Error(), workspaceFile) || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("file workspace error was unclear: %v", err)
	}
	for _, content := range []string{
		"malformed: [\n", "working_directory: ../work\nunknown: true\n", "working_directory: '   '\n", "working_directory: \"bad\\x00path\"\n",
	} {
		writeLocationConfig(t, bootstrap, content)
		if _, err := ResolveLocation(bootstrap); err == nil || !strings.Contains(err.Error(), bootstrap) {
			t.Fatalf("invalid locator was ignored or lost its path: %v", err)
		}
	}
}

func TestWorkingDirectoryRequiresAStringAndValidPathText(t *testing.T) {
	for _, value := range []string{"123", "true", "1.5", "[]", "{}"} {
		if _, err := Parse(strings.NewReader("version: 1\nworking_directory: " + value + "\n")); err == nil || !strings.Contains(err.Error(), "working_directory must be a string") {
			t.Errorf("non-string working_directory %q was accepted: %v", value, err)
		}
	}
	for _, directory := range []string{"   ", "bad\x00path", "line\nbreak"} {
		cfg := mustParse(t, "version: 1\n")
		cfg.WorkingDirectory = directory
		if err := cfg.ValidateCommon(); err == nil || !strings.Contains(err.Error(), "working_directory") {
			t.Errorf("invalid workspace path %q was accepted: %v", directory, err)
		}
	}
	for _, input := range []string{
		"working_directory: ../workspace\nunknown: true\n",
		"working_directory: ../workspace\n---\nworking_directory: other\n",
		"gitlab: {url: &number 123}\nworking_directory: *number\n",
		"<<: {working_directory: 123}\n",
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("workspace setting bypassed strict parsing: %s", input)
		}
	}
}

func TestResolveLocationDoesNotTreatBrokenLocatorSymlinkAsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks can require additional Windows privileges")
	}
	root := t.TempDir()
	bootstrap := filepath.Join(root, Filename)
	if err := os.Symlink(filepath.Join(root, "missing-config.yaml"), bootstrap); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocation(bootstrap); err == nil || !strings.Contains(err.Error(), bootstrap) {
		t.Fatalf("broken authoritative locator was silently ignored: %v", err)
	}
}
