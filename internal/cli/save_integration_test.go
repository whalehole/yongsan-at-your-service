package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type saveCLIEnvironment struct {
	directory       string
	log             string
	requests        atomic.Int32
	archiveRequests atomic.Int32
}

func setupSaveCLI(t *testing.T) *saveCLIEnvironment {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker uses a POSIX shell")
	}
	f := &saveCLIEnvironment{directory: workingDirectory(t)}
	f.log = filepath.Join(f.directory, "docker-invocations")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tw := tar.NewWriter(gz)
	const content = "FROM scratch\n"
	if err := tw.WriteHeader(&tar.Header{Name: "repo-sha/Dockerfile", Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags/v1"):
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
		case strings.HasSuffix(r.URL.Path, "/archive.tar.gz"):
			f.archiveRequests.Add(1)
			if r.URL.Query().Get("sha") != testSHA {
				t.Errorf("archive query=%v", r.URL.Query())
			}
			_, _ = w.Write(compressed.Bytes())
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group/a\n    - project: group/b\n")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$YAS_SAVE_TEST_LOG"
if [ "$1" = build ]; then
  if [ "$YAS_SAVE_TEST_BUILD_FAIL" = 1 ]; then exit 9; fi
  exit 0
fi
if [ "$1" = image ] && [ "$2" = save ]; then
  printf 'fake docker image archive\n' > "$4"
  if [ "$5" = "$YAS_SAVE_TEST_FAIL_IMAGE" ]; then exit 8; fi
  exit 0
fi
exit 90
`
	if err := os.WriteFile(filepath.Join(f.directory, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.directory)
	t.Setenv("YAS_SAVE_TEST_LOG", f.log)
	t.Setenv("YAS_SAVE_TEST_BUILD_FAIL", "")
	t.Setenv("YAS_SAVE_TEST_FAIL_IMAGE", "")
	return f
}

func decodeSaveResults(t *testing.T, output string) []result {
	t.Helper()
	var results []result
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("stdout is not JSON: %q: %v", output, err)
	}
	return results
}

func TestSaveBuildOneExportsSuccessfulImageAndReportsAbsoluteArchive(t *testing.T) {
	f := setupSaveCLI(t)
	output, _, err := executeTest([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--image", "service:test", "--save", "--save-dir", "release images", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 1 {
		t.Fatalf("results=%s", output)
	}
	r := results[0]
	if r.Status != "built" || r.BuildStatus != "built" || r.SaveStatus != "saved" || r.SaveError != "" {
		t.Fatalf("result=%#v", r)
	}
	// macOS can expose the temporary root through both /var and /private/var.
	wantDirectory, err := filepath.EvalSymlinks(filepath.Join(f.directory, "release images"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(r.Archive) || filepath.Dir(filepath.Dir(r.Archive)) != wantDirectory || filepath.Base(r.Archive) != "service_test.tar" {
		t.Fatalf("archive path=%q", r.Archive)
	}
	if r.BatchID == "" || r.ArchiveDirectory != filepath.Dir(r.Archive) || filepath.Base(r.ArchiveDirectory) != r.BatchID {
		t.Fatalf("missing archive batch metadata: %#v", r)
	}
	data, err := os.ReadFile(r.Archive)
	if err != nil || len(data) == 0 {
		t.Fatalf("saved archive=%q error=%v", data, err)
	}
	commands, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "build ") || !strings.HasPrefix(lines[1], "image save --output ") || !strings.HasSuffix(lines[1], " service:test") {
		t.Fatalf("Docker invocation order=%q", commands)
	}
	if f.archiveRequests.Load() != 1 {
		t.Fatalf("source archive requests=%d", f.archiveRequests.Load())
	}
}

func TestSaveFailedBuildDoesNotAttemptExport(t *testing.T) {
	f := setupSaveCLI(t)
	t.Setenv("YAS_SAVE_TEST_BUILD_FAIL", "1")
	output, _, err := executeTest([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--image", "service:test", "--save", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 1 || results[0].Status != "failed" || results[0].BuildStatus != "failed" || results[0].SaveStatus != "not-run" || results[0].Archive != "" || results[0].SaveError != "" {
		t.Fatalf("results=%s", output)
	}
	for _, explanation := range []string{"Docker build", "service:test", "exit code 9", "Docker logs"} {
		if !strings.Contains(results[0].Error, explanation) {
			t.Errorf("build failure lacks %q: %s", explanation, results[0].Error)
		}
	}
	commands, err := os.ReadFile(f.log)
	if err != nil || strings.Contains(string(commands), "image save") {
		t.Fatalf("Docker commands=%q error=%v", commands, err)
	}
	if _, err := os.Stat("images"); !os.IsNotExist(err) {
		t.Fatalf("failed build created archive directory: %v", err)
	}
}

func TestSaveBatchReportsExportFailureSeparatelyFromSuccessfulBuild(t *testing.T) {
	f := setupSaveCLI(t)
	t.Setenv("YAS_SAVE_TEST_FAIL_IMAGE", "b:v1")
	reportPath := filepath.Join("audit", "exports.html")
	output, _, err := executeTest([]string{"build", "batch", "--tag", "v1", "--save", "--concurrency", "1", "--format", "json", "--report", reportPath}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 2 || results[0].Project != "group/a" || results[0].Status != "built" || results[0].SaveStatus != "saved" || results[1].Project != "group/b" || results[1].Status != "failed" || results[1].BuildStatus != "built" || results[1].SaveStatus != "failed" || results[1].SaveError == "" || results[1].Archive != "" {
		t.Fatalf("results=%s", output)
	}
	for _, explanation := range []string{"Docker save", "b:v1", "exit code 8", "built image remains", "no new completed archive"} {
		if !strings.Contains(results[1].Error, explanation) {
			t.Errorf("save failure lacks %q: %s", explanation, results[1].Error)
		}
	}
	entries, err := os.ReadDir(filepath.Join(f.directory, "images"))
	if err != nil || len(entries) != 1 || !entries[0].IsDir() || entries[0].Name() != results[0].BatchID {
		t.Fatalf("archive batch directories=%v error=%v", entries, err)
	}
	entries, err = os.ReadDir(results[0].ArchiveDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "a_v1.tar" || results[0].BatchID != results[1].BatchID {
		t.Fatalf("archive directory has failed or partial output: entries=%v error=%v", entries, err)
	}
	report := html.UnescapeString(readReportHTML(t, reportPath))
	for _, expected := range []string{"group/a", "group/b", "Docker save", results[0].Archive, results[1].SaveError, "1 saved", "1 failed"} {
		if !strings.Contains(report, expected) {
			t.Errorf("report missing %q", expected)
		}
	}
	if count := strings.Count(report, `stage-label">Build</span> <span class="badge pass">PASS`); count != 2 {
		t.Fatalf("report did not preserve two successful builds (count=%d): %s", count, report)
	}
}

func TestSaveDefaultsAndInteractiveChoice(t *testing.T) {
	for _, tc := range []struct {
		name        string
		flags       []string
		input       string
		interactive bool
		save        bool
		prompts     int
	}{
		{name: "non-interactive default"},
		{name: "destination alone", flags: []string{"--save-dir", "images"}},
		{name: "interactive default no", interactive: true, prompts: 1},
		{name: "interactive no", interactive: true, input: "no\n", prompts: 1},
		{name: "interactive yes", interactive: true, input: "y\n", save: true, prompts: 1},
		{name: "invalid answer retries", interactive: true, input: "maybe\nyes\n", save: true, prompts: 2},
		{name: "explicit false bypasses prompt", interactive: true, flags: []string{"--save=false"}},
		{name: "explicit true bypasses prompt", interactive: true, flags: []string{"--save"}, save: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSaveCLI(t)
			args := append([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--image", "service:test", "--context", ".", "--format", "json"}, tc.flags...)
			output, prompts, err := executeTest(args, tc.input, tc.interactive)
			if err != nil {
				t.Fatal(err)
			}
			results := decodeSaveResults(t, output)
			if len(results) != 1 || results[0].Status != "built" {
				t.Fatalf("results=%s", output)
			}
			if count := strings.Count(prompts, "Save built images as .tar files after build?"); count != tc.prompts {
				t.Fatalf("save prompt count=%d want=%d: %s", count, tc.prompts, prompts)
			}
			commands, err := os.ReadFile(f.log)
			if err != nil {
				t.Fatal(err)
			}
			if saved := strings.Contains(string(commands), "image save --output"); saved != tc.save {
				t.Fatalf("saved=%v want=%v commands=%q", saved, tc.save, commands)
			}
			if tc.save {
				if results[0].SaveStatus != "saved" || results[0].Archive == "" {
					t.Fatalf("save results=%s", output)
				}
			} else {
				if results[0].SaveStatus != "" || results[0].Archive != "" {
					t.Fatalf("unrequested save results=%s", output)
				}
				if _, err := os.Stat("images"); !os.IsNotExist(err) {
					t.Fatalf("disabled saving created archive directory: %v", err)
				}
			}
		})
	}
}

func TestSaveDryRunDoesNotInvokeDockerOrCreateArchiveDirectory(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "one"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			f := setupSaveCLI(t)
			args := []string{"build", name, "--tag", "v1", "--dry-run", "--save", "--save-dir", "planned archives", "--format", "json"}
			want := 1
			if batch {
				args = append(args, "--no-report")
				want = 2
			} else {
				args = append(args, "--project", "group/service", "--image", "service:test")
			}
			output, _, err := executeTest(args, "", false)
			if err != nil {
				t.Fatal(err)
			}
			results := decodeSaveResults(t, output)
			if len(results) != want {
				t.Fatalf("results=%s", output)
			}
			for _, r := range results {
				if r.Status != "planned" || r.BuildStatus != "planned" || r.SaveStatus != "planned" || r.Archive != "" {
					t.Fatalf("planned result=%#v", r)
				}
			}
			if _, err := os.Stat("planned archives"); !os.IsNotExist(err) {
				t.Fatalf("dry run created archive directory: %v", err)
			}
			if _, err := os.Stat(f.log); !os.IsNotExist(err) || f.archiveRequests.Load() != 0 {
				t.Fatalf("dry run invoked Docker or downloaded sources: log error=%v requests=%d", err, f.archiveRequests.Load())
			}
		})
	}
}

func TestSaveInteractiveBatchAsksOnceAndPlansEverySelectedImage(t *testing.T) {
	setupSaveCLI(t)
	output, prompts, err := executeTest([]string{"build", "batch", "--all-groups", "--tag", "v1", "--context", ".", "--no-image-prefix", "--dry-run", "--no-report", "--format", "json"}, "yes\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(prompts, "Save built images as .tar files after build?"); count != 1 {
		t.Fatalf("save prompt count=%d: %s", count, prompts)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 2 || results[0].SaveStatus != "planned" || results[1].SaveStatus != "planned" {
		t.Fatalf("results=%s", output)
	}
}

func TestSaveTextResultsIncludeExportStatusAndArchivePath(t *testing.T) {
	setupSaveCLI(t)
	output, _, err := executeTest([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--image", "service:test", "--save"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(filepath.Join("images", "*", "*.tar"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%v error=%v", archives, err)
	}
	absolute, err := filepath.Abs(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Docker save: saved") || !strings.Contains(output, absolute) {
		t.Fatalf("text result lacks save outcome or path: %s", output)
	}
}

func TestSaveEmptyDirectoryFailsBeforeRepositoryJobs(t *testing.T) {
	f := setupSaveCLI(t)
	_, _, err := executeTest([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--image", "service:test", "--save", "--save-dir="}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--save-dir") {
		t.Fatalf("error=%v", err)
	}
	if f.requests.Load() != 0 {
		t.Fatalf("invalid save directory made %d network requests", f.requests.Load())
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("invalid save directory invoked Docker: %v", err)
	}
}
