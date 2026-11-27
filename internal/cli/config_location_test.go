package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigCommandsAndOperationsUseOneLocationAcrossWorkingDirectories(t *testing.T) {
	caller := workingDirectory(t)
	filename := filepath.Join(t.TempDir(), "yas.yaml")
	localConfig := filepath.Join(caller, "yas.yaml")
	localContents := []byte("malformed: [\n")
	if err := os.WriteFile(localConfig, localContents, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		var output bytes.Buffer
		command := New(Options{ConfigPath: filename, In: strings.NewReader(""), Out: &output, Err: &output})
		command.SetArgs(args)
		err := command.ExecuteContext(context.Background())
		return output.String(), err
	}
	out, err := run("config", "init")
	if err != nil || !strings.Contains(out, filename) {
		t.Fatalf("init output=%q error=%v", out, err)
	}
	original, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("config is not private: info=%v error=%v", info, err)
		}
	}
	if _, err := run("config", "validate"); err != nil {
		t.Fatalf("validate used the malformed caller configuration: %v", err)
	}
	if _, err := run("config", "init"); ExitCode(err) != 2 || !strings.Contains(err.Error(), filename) {
		t.Fatalf("existing executable-local config was not protected: %v", err)
	}
	current, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("init changed existing config: %v", err)
	}

	// A missing token fails locally after loading the intended configuration;
	// no GitLab request can be sent by this operation.
	t.Setenv("YAS_CONFIG_LOCATION_TEST_TOKEN", "")
	if err := os.WriteFile(filename, []byte("version: 1\ngitlab:\n  token_env: YAS_CONFIG_LOCATION_TEST_TOKEN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = run("tag", "one", "--project", "group/service", "--branch", "main", "--tag", "v1", "--non-interactive")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "YAS_CONFIG_LOCATION_TEST_TOKEN") {
		t.Fatalf("operation did not load the shared configuration: %v", err)
	}
	current, err = os.ReadFile(localConfig)
	if err != nil || !bytes.Equal(current, localContents) {
		t.Fatalf("caller configuration was modified: %v", err)
	}

	// A valid caller-local file must not become a fallback when the intended
	// configuration is absent.
	if err := os.WriteFile(localConfig, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filename, filename+".saved"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "validate"},
		{"tag", "one", "--project", "group/service", "--branch", "main", "--tag", "v1", "--non-interactive"},
	} {
		_, err := run(args...)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), filename) || !strings.Contains(err.Error(), "yas config init") {
			t.Fatalf("%v fell back to caller configuration: %v", args, err)
		}
	}
}
