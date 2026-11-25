package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSaveImageArchiveUsesSelectedDockerAndAbsolutePaths(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "printf 'archive for %s' \"$5\" > \"$4\"\nprintf 'saved image\\n'\nprintf 'save diagnostics\\n' >&2")
	directory := filepath.Join(t.TempDir(), "nested", "exports")
	image := "registry.example.com:5000/team/service:release-v1"
	var output bytes.Buffer
	var phases []string
	b := &Builder{Docker: docker, Output: &output, Progress: func(phase string) { phases = append(phases, phase) }}
	path, err := b.Save(context.Background(), image, directory)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || filepath.Dir(path) != directory || filepath.Base(path) != "service_release-v1.tar" {
		t.Fatalf("archive path=%q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "archive for "+image {
		t.Fatalf("archive=%q error=%v", data, err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	if len(got) != 5 || !filepath.IsAbs(got[3]) || filepath.Dir(got[3]) != directory || filepath.Ext(got[3]) != ".partial" {
		t.Fatalf("Docker arguments=%q", got)
	}
	want := []string{"image", "save", "--output", got[3], image}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker arguments=%q want=%q", got, want)
	}
	if !strings.Contains(output.String(), "saved image\n") || !strings.Contains(output.String(), "save diagnostics\n") || strings.Contains(output.String(), "archive for ") {
		t.Fatalf("Docker logs=%q", output.String())
	}
	if len(phases) != 1 || !strings.Contains(phases[0], "saving Docker image") {
		t.Fatalf("progress=%v", phases)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("export files=%v error=%v", entries, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("archive permissions=%v error=%v", info, err)
		}
	}
}

func TestSaveDefaultsToDockerOnPath(t *testing.T) {
	docker, _, _ := fakeDocker(t, "printf archive > \"$4\"")
	t.Setenv("PATH", filepath.Dir(docker)+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := &Builder{}
	if path, err := b.Save(context.Background(), "service:v1", t.TempDir()); err != nil || path == "" {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
}

func TestSaveFailureRemovesPartialArchive(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"process failure", "printf incomplete > \"$4\"\nexit 17"},
		{"empty output", "exit 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docker, _, _ := fakeDocker(t, tc.body)
			directory := t.TempDir()
			b := &Builder{Docker: docker}
			path, err := b.Save(context.Background(), "service:v1", directory)
			if err == nil || path != "" {
				t.Fatalf("archive path=%q error=%v", path, err)
			}
			assertEmpty(t, directory)
		})
	}
}

func TestSaveCancellationStopsDockerAndCleansArchive(t *testing.T) {
	docker, _, _ := fakeDocker(t, "printf incomplete > \"$4\"\nexec sleep 30")
	directory := t.TempDir()
	b := &Builder{Docker: docker}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	path, err := b.Save(ctx, "service:v1", directory)
	if !errors.Is(err, context.DeadlineExceeded) || path != "" {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("canceled Docker save did not stop promptly")
	}
	assertEmpty(t, directory)
}

func TestSavePreCanceledDoesNotInvokeDocker(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "printf archive > \"$4\"")
	directory := filepath.Join(t.TempDir(), "not-created")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &Builder{Docker: docker}
	path, err := b.Save(ctx, "service:v1", directory)
	if !errors.Is(err, context.Canceled) || path != "" {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
	for _, name := range []string{argsFile, directory} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("canceled export created %q: %v", name, err)
		}
	}
}

func TestSaveUnavailableDirectoryDoesNotInvokeDocker(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "printf archive > \"$4\"")
	file := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(file, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &Builder{Docker: docker}
	path, err := b.Save(context.Background(), "service:v1", filepath.Join(file, "exports"))
	if err == nil || path != "" {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatalf("Docker was invoked for invalid directory: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "preserve me" {
		t.Fatalf("existing file changed: %q error=%v", data, err)
	}
}

func TestSaveExistingDeterministicArchiveIsPreserved(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "printf archive > \"$4\"")
	directory := t.TempDir()
	existing := filepath.Join(directory, "service_release.tar")
	if err := os.WriteFile(existing, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &Builder{Docker: docker}
	path, err := b.Save(context.Background(), "registry.example.com:5000/team/service:release", directory)
	if path != "" || !errors.Is(err, os.ErrExist) {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatalf("Docker invoked despite existing archive: %v", err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "preserve me" {
		t.Fatalf("existing archive changed: %q error=%v", data, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive files=%v error=%v", entries, err)
	}
}

type saveOutcome struct {
	path string
	err  error
}

func concurrentSaves(b *Builder, images []string, directory string) []saveOutcome {
	outcomes := make(chan saveOutcome, len(images))
	for _, image := range images {
		image := image
		go func() {
			path, err := b.Save(context.Background(), image, directory)
			outcomes <- saveOutcome{path, err}
		}()
	}
	results := make([]saveOutcome, 0, len(images))
	for range images {
		results = append(results, <-outcomes)
	}
	return results
}

func TestSaveConcurrentSameNamesOnlyOneSucceeds(t *testing.T) {
	docker, _, _ := fakeDocker(t, "printf 'archive for %s' \"$5\" > \"$4\"")
	directory := t.TempDir()
	b := &Builder{Docker: docker, Output: io.Discard}
	const count = 12
	images := make([]string, count)
	for i := 0; i < count; i++ {
		images[i] = "registry.example.com:5000/team/service:release"
	}
	succeeded, collisions := 0, 0
	for _, item := range concurrentSaves(b, images, directory) {
		if item.err != nil {
			if !errors.Is(item.err, os.ErrExist) || item.path != "" {
				t.Fatalf("unexpected save failure: path=%q error=%v", item.path, item.err)
			}
			collisions++
			continue
		}
		succeeded++
		if item.path != filepath.Join(directory, "service_release.tar") {
			t.Fatalf("unexpected deterministic archive path=%q", item.path)
		}
	}
	if succeeded != 1 || collisions != count-1 {
		t.Fatalf("concurrent exports: %d succeeded, %d collisions", succeeded, collisions)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "service_release.tar" {
		t.Fatalf("archive files=%v error=%v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "service_release.tar"))
	if err != nil || string(data) != "archive for "+images[0] {
		t.Fatalf("completed archive changed: %q error=%v", data, err)
	}
}

func TestSaveConcurrentDistinctArchivesSucceed(t *testing.T) {
	docker, _, _ := fakeDocker(t, "printf 'archive for %s' \"$5\" > \"$4\"")
	directory := t.TempDir()
	b := &Builder{Docker: docker, Output: io.Discard}
	const count = 8
	images := make([]string, count)
	want := map[string]string{}
	for i := range images {
		images[i] = fmt.Sprintf("registry.example.com:5000/team/service%d:v1.2.0", i)
		want[filepath.Join(directory, fmt.Sprintf("service%d_v1.2.0.tar", i))] = "archive for " + images[i]
	}
	for _, item := range concurrentSaves(b, images, directory) {
		if item.err != nil {
			t.Fatal(item.err)
		}
		data, err := os.ReadFile(item.path)
		if expected, ok := want[item.path]; !ok || err != nil || string(data) != expected {
			t.Fatalf("archive %q content=%q error=%v", item.path, data, err)
		}
		delete(want, item.path)
	}
	if len(want) != 0 {
		t.Fatalf("missing archives=%v", want)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != count {
		t.Fatalf("archive files=%v error=%v", entries, err)
	}
}

func TestSaveValidatesArgumentsAndDockerAvailability(t *testing.T) {
	for _, tc := range []struct {
		image, directory string
	}{
		{"", t.TempDir()},
		{"service:v1", ""},
		{"service:v1", t.TempDir()},
	} {
		b := &Builder{Docker: filepath.Join(t.TempDir(), "missing-docker")}
		if path, err := b.Save(context.Background(), tc.image, tc.directory); err == nil || path != "" {
			t.Fatalf("Save(%q, %q): path=%q error=%v", tc.image, tc.directory, path, err)
		}
	}
}

func TestArchiveNamePreservesRepositoryBasenameAndTag(t *testing.T) {
	for _, tc := range []struct{ image, want string }{
		{"service:latest", "service_latest.tar"},
		{"registry.example.com:5000/team/service:v1.2.0", "service_v1.2.0.tar"},
		{"localhost:5000/team/api_server:V1.2_Release-4", "api_server_V1.2_Release-4.tar"},
		{"registry.example.com/team/my.service-name__client:_Release.3-TEST", "my.service-name__client__Release.3-TEST.tar"},
		{strings.Repeat("a", 122) + ":" + strings.Repeat("T", 128), strings.Repeat("a", 122) + "_" + strings.Repeat("T", 128) + ".tar"},
	} {
		name, err := ArchiveName(tc.image)
		if err != nil || name != tc.want {
			t.Errorf("ArchiveName(%q)=%q error=%v want=%q", tc.image, name, err, tc.want)
		}
	}
}

func TestArchiveNameRejectsInvalidReferencesAndLongFilenames(t *testing.T) {
	for _, image := range []string{
		"", "service", "registry.example.com:5000/team/service", "service:", ":v1", "registry.example.com/team/:v1",
		"service@sha256:abcdef", "service:v1@sha256:abcdef", "service:v1/bad", "service:bad\\tag", "service:bad:tag", "service:.bad", "service:-bad", "service:bad tag", "service:💾", "../service:v1", "service/../app:v1", "team//service:v1", "Service:v1",
		"service:" + strings.Repeat("t", 129), strings.Repeat("a", 123) + ":" + strings.Repeat("t", 128),
	} {
		if name, err := ArchiveName(image); err == nil || name != "" {
			t.Errorf("ArchiveName(%q)=%q error=%v", image, name, err)
		}
	}
}

func TestSaveInvalidImageDoesNotInvokeDockerOrCreateFiles(t *testing.T) {
	docker, argsFile, _ := fakeDocker(t, "printf archive > \"$4\"")
	directory := filepath.Join(t.TempDir(), "not-created")
	b := &Builder{Docker: docker}
	for _, image := range []string{"service", "service@sha256:abcdef", "service:bad/tag", strings.Repeat("a", 123) + ":" + strings.Repeat("t", 128)} {
		if path, err := b.Save(context.Background(), image, directory); err == nil || path != "" {
			t.Fatalf("Save(%q): path=%q error=%v", image, path, err)
		}
	}
	for _, name := range []string{argsFile, directory} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("invalid image created %q: %v", name, err)
		}
	}
}

func TestSaveSupportsMaximumLengthArchiveFilename(t *testing.T) {
	docker, _, _ := fakeDocker(t, "printf archive > \"$4\"")
	b := &Builder{Docker: docker}
	image := strings.Repeat("a", 122) + ":" + strings.Repeat("T", 128)
	path, err := b.Save(context.Background(), image, t.TempDir())
	if err != nil || len(filepath.Base(path)) != 255 {
		t.Fatalf("archive path=%q error=%v", path, err)
	}
}
