package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathUsesTheRealProcessExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(filepath.Dir(resolved), Filename))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Path()
	if err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("Path()=%q error=%v want=%q", got, err, want)
	}
}

func TestExecutableConfigurationPathResolvesFileAndDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks can require additional Windows privileges")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDirectory := filepath.Join(root, "real binary directory")
	aliasDirectory := filepath.Join(root, "aliases")
	for _, directory := range []string{realDirectory, aliasDirectory} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(realDirectory, "yas-bin")
	if err := os.WriteFile(binary, []byte("synthetic executable"), 0755); err != nil {
		t.Fatal(err)
	}
	fileAlias := filepath.Join(aliasDirectory, "yas")
	if err := os.Symlink(filepath.Join("..", "real binary directory", "yas-bin"), fileAlias); err != nil {
		t.Fatal(err)
	}
	directoryAlias := filepath.Join(root, "bin-link")
	if err := os.Symlink("real binary directory", directoryAlias); err != nil {
		t.Fatal(err)
	}
	chainedAlias := filepath.Join(root, "yas-wrapper")
	if err := os.Symlink(filepath.Join("aliases", "yas"), chainedAlias); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realDirectory, Filename)
	for _, invocation := range []string{binary, fileAlias, filepath.Join(directoryAlias, "yas-bin"), chainedAlias} {
		got, err := pathForExecutable(invocation)
		if err != nil || got != want {
			t.Errorf("pathForExecutable(%q)=%q error=%v want=%q", invocation, got, err, want)
		}
	}
}

func TestExecutableConfigurationPathErrorsNameTheExecutable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-yas")
	got, err := pathForExecutable(missing)
	if got != "" || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing executable path=%q error=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "broken-yas-link")
		if err := os.Symlink(missing, alias); err != nil {
			t.Fatal(err)
		}
		got, err := pathForExecutable(alias)
		if got != "" || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), alias) {
			t.Fatalf("broken executable link path=%q error=%v", got, err)
		}
	}
}

func TestLoadFileUsesTheExplicitConfigurationPath(t *testing.T) {
	filename := filepath.Join(t.TempDir(), Filename)
	if err := os.WriteFile(filename, []byte("version: 1\nrelease:\n  repositories:\n    - project: release-group/service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filename, relative} {
		cfg, err := LoadFile(path)
		if err != nil || cfg == nil || len(cfg.Release.Repositories) != 1 || cfg.Release.Repositories[0].Project != "release-group/service" {
			t.Fatalf("LoadFile(%q)=%+v error=%v", path, cfg, err)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadFileMissingAndInvalidErrorsIncludeTheAbsolutePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created", Filename)
	_, err := LoadFile(missing)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing configuration cause lost: %v", err)
	}
	for _, want := range []string{missing, "yas.yaml was not found", "yas config init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing configuration error %q lacks %q", err, want)
		}
	}
	for _, body := range []string{"", "version: [\n", "version: 1\nunknown_field: value\n"} {
		filename := filepath.Join(t.TempDir(), Filename)
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if cfg, err := LoadFile(filename); err == nil || cfg != nil || !strings.Contains(err.Error(), filename) {
			t.Fatalf("invalid configuration lost its path: config=%+v error=%v", cfg, err)
		}
	}
	directory := t.TempDir()
	if cfg, err := LoadFile(directory); err == nil || cfg != nil || !strings.Contains(err.Error(), directory) {
		t.Fatalf("unreadable configuration lost its path: config=%+v error=%v", cfg, err)
	}
}

func TestLoadDoesNotFallBackToWorkingDirectoryConfiguration(t *testing.T) {
	executableConfig, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(executableConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test executable unexpectedly has a configuration: %q, %v", executableConfig, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, Filename), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Error(err)
		}
	})
	cfg, err := Load()
	if cfg != nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), executableConfig) {
		t.Fatalf("Load used the working directory instead of %q: config=%+v error=%v", executableConfig, cfg, err)
	}
}
