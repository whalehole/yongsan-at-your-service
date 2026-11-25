package builder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	data                  []byte
	err                   error
	project, commit, path string
	called                bool
}

func (s *fakeSource) Archive(ctx context.Context, project, commit, path string) (io.ReadCloser, error) {
	s.called, s.project, s.commit, s.path = true, project, commit, path
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func fakeDocker(t *testing.T, body string) (executable, argsFile, workDirFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	dir := t.TempDir()
	executable = filepath.Join(dir, "docker")
	argsFile = filepath.Join(dir, "args")
	workDirFile = filepath.Join(dir, "cwd")
	t.Setenv("YAS_TEST_ARGS", argsFile)
	t.Setenv("YAS_TEST_WORKDIR", workDirFile)
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" > \"$YAS_TEST_WORKDIR\"\nprintf '%s\\n' \"$@\" > \"$YAS_TEST_ARGS\"\n" + body + "\n"
	if err := os.WriteFile(executable, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return executable, argsFile, workDirFile
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary source files were not cleaned up: %v", entries)
	}
}

func TestBuildDownloadsSelectedSnapshotAndRunsDocker(t *testing.T) {
	docker, argsFile, workDirFile := fakeDocker(t, "printf 'built image\\n'")
	source := &fakeSource{data: makeArchive(t,
		member{name: "repo-sha/services/api/Dockerfile.release", content: "FROM scratch"},
		member{name: "repo-sha/services/api/app", content: "source"},
	)}
	temp := t.TempDir()
	var output bytes.Buffer
	b := &Builder{Source: source, Docker: docker, TempDir: temp, Output: &output}
	opts := Options{Project: "group/service", Commit: "deadbeef", Context: "services/api", Dockerfile: "Dockerfile.release", Image: "service:1.2.0", NoCache: true, Pull: true, BuildArgs: map[string]string{"Z": "two words; $(false)", "A": "production"}}
	if err := b.Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if source.project != opts.Project || source.commit != opts.Commit || source.path != "services/api" {
		t.Fatalf("archive request = %q %q %q", source.project, source.commit, source.path)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{"build", "--no-cache", "--pull", "--build-arg", "A=production", "--build-arg", "Z=two words; $(false)", "--file", "Dockerfile.release", "--tag", "service:1.2.0", "."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
	cwd, err := os.ReadFile(workDirFile)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(cwd)), filepath.FromSlash("services/api")) {
		t.Fatalf("Docker working directory = %q, error = %v", cwd, err)
	}
	if output.String() != "built image\n" {
		t.Fatalf("Docker output = %q", output.String())
	}
	assertEmpty(t, temp)
}

func TestBuildRootContextAndDefaults(t *testing.T) {
	docker, _, _ := fakeDocker(t, "exit 0")
	source := &fakeSource{data: makeArchive(t, member{name: "repo/Dockerfile", content: "FROM scratch"})}
	b := &Builder{Source: source, Docker: docker, TempDir: t.TempDir()}
	if err := b.Build(context.Background(), Options{Project: "group/repo", Commit: "sha", Image: "repo:latest"}); err != nil {
		t.Fatal(err)
	}
	if source.path != "" {
		t.Fatalf("root archive path = %q, want empty", source.path)
	}
	assertEmpty(t, b.TempDir)
}

func TestBuildChecksDockerBeforeDownloading(t *testing.T) {
	source := &fakeSource{}
	b := &Builder{Source: source, Docker: filepath.Join(t.TempDir(), "missing-docker")}
	err := b.Build(context.Background(), Options{Project: "group/repo", Commit: "sha", Image: "repo:latest"})
	if err == nil || !strings.Contains(err.Error(), "Docker executable") {
		t.Fatalf("error = %v", err)
	}
	if source.called {
		t.Fatal("source was downloaded before checking Docker availability")
	}
}

func TestBuildFailureCleansTemporaryFiles(t *testing.T) {
	for _, failure := range []string{"download", "extract", "Docker"} {
		t.Run(failure, func(t *testing.T) {
			docker, _, _ := fakeDocker(t, "exit 17")
			source := &fakeSource{data: makeArchive(t, member{name: "repo/Dockerfile", content: "FROM scratch"})}
			if failure == "download" {
				source.err = errors.New("access denied")
			}
			if failure == "extract" {
				source.data = []byte("not an archive")
			}
			b := &Builder{Source: source, Docker: docker, TempDir: t.TempDir()}
			if err := b.Build(context.Background(), Options{Project: "group/repo", Commit: "sha", Image: "repo:latest"}); err == nil {
				t.Fatal("expected failure")
			}
			assertEmpty(t, b.TempDir)
		})
	}
}

func TestBuildRejectsSubmodules(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "exit 0")
	source := &fakeSource{data: makeArchive(t,
		member{name: "repo/Dockerfile", content: "FROM scratch"},
		member{name: "repo/.gitmodules", content: "[submodule \"lib\"]"},
	)}
	b := &Builder{Source: source, Docker: docker, TempDir: t.TempDir()}
	err := b.Build(context.Background(), Options{Project: "group/repo", Commit: "sha", Image: "repo:latest"})
	if err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("error = %v, want unsupported submodules", err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatal("Docker was invoked for a submodule build")
	}
	assertEmpty(t, b.TempDir)
}

func TestBuildCancellationStopsDockerAndCleansTemporaryFiles(t *testing.T) {
	docker, _, _ := fakeDocker(t, "exec sleep 30")
	source := &fakeSource{data: makeArchive(t, member{name: "repo/Dockerfile", content: "FROM scratch"})}
	b := &Builder{Source: source, Docker: docker, TempDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := b.Build(ctx, Options{Project: "group/repo", Commit: "sha", Image: "repo:latest"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("canceled Docker build did not stop promptly")
	}
	assertEmpty(t, b.TempDir)
}
