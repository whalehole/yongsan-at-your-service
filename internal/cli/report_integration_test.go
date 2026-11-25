package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const reportTestConfig = "tagging:\n  repositories:\n    - project: group/a\n    - project: group/b\nbuilding:\n  repositories:\n    - project: group/a\n    - project: group/b\n"

func reportTestServer(t *testing.T, operation string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("dry run made a mutating request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/branches/main"):
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
		case strings.Contains(r.URL.Path, "/tags/"):
			if operation == "tag" {
				w.WriteHeader(http.StatusNotFound)
			} else {
				fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func readReportHTML(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(data)), "<html") {
		t.Fatalf("report is not HTML: %s", data)
	}
	return string(data)
}

func TestBatchCommandsAutomaticallySaveOneSelectedRepositoryReport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		args      []string
	}{
		{"tag", "tag", []string{"tag", "batch", "--branch", "main", "--tag", "release"}},
		{"build", "build", []string{"build", "batch", "--tag", "release"}},
		{"delete", "delete", []string{"tag", "delete", "batch", "--tag", "release"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			var requests atomic.Int32
			server := reportTestServer(t, tc.operation, &requests)
			defer server.Close()
			writeConfig(t, server.URL, reportTestConfig)
			args := append(append([]string{}, tc.args...), "--only", "group/a", "--dry-run", "--format", "json", "--no-progress")
			out, logs, err := executeTest(args, "", false)
			if err != nil {
				t.Fatal(err)
			}
			var results []result
			if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 || results[0].Status != "planned" {
				t.Fatalf("results=%s error=%v", out, err)
			}
			paths, err := filepath.Glob(filepath.Join("reports", "*.html"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("report paths=%v error=%v", paths, err)
			}
			report := readReportHTML(t, paths[0])
			for _, expected := range []string{"group/a", "planned"} {
				if !strings.Contains(report, expected) {
					t.Errorf("report does not contain %q: %s", expected, report)
				}
			}
			if strings.Contains(report, "group/b") {
				t.Error("report includes an unselected repository")
			}
			if !strings.Contains(logs, filepath.Base(paths[0])) {
				t.Fatalf("report path missing from stderr with --no-progress: %q", logs)
			}
			if requests.Load() == 0 {
				t.Fatal("batch did not check the selected repository")
			}
		})
	}
}

func TestBatchReportCustomPathPreservesPartialFailureAndJSONOutput(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run made a mutating request: %s", r.Method)
		}
		if strings.Contains(r.URL.Path, "group/b") || strings.Contains(r.URL.Path, "/tags/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	writeConfig(t, server.URL, reportTestConfig)
	path := filepath.Join("audit", "nested", "release.html")
	out, logs, err := executeTest([]string{"tag", "batch", "--branch", "main", "--tag", "release", "--dry-run", "--format", "json", "--no-progress", "--report", path}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("stdout is not just JSON: %s error=%v", out, err)
	}
	if len(results) != 2 || results[0].Project != "group/a" || results[0].Status != "planned" || results[1].Project != "group/b" || results[1].Status != "failed" || results[1].Error == "" {
		t.Fatalf("partial results=%s", out)
	}
	report := readReportHTML(t, path)
	for _, expected := range []string{"group/a", "group/b", "release", "planned", "failed", "404"} {
		if !strings.Contains(report, expected) {
			t.Errorf("report does not contain %q: %s", expected, report)
		}
	}
	if strings.Contains(report, "test-token") {
		t.Error("authentication token leaked into report")
	}
	if !strings.Contains(logs, filepath.Base(path)) || strings.Contains(out, path) {
		t.Fatalf("report path output: stdout=%q stderr=%q", out, logs)
	}
	if _, err := os.Stat("reports"); !os.IsNotExist(err) {
		t.Fatalf("custom report also created automatic report directory: %v", err)
	}
}

func TestReportsAreOptionalForBatchAndNotAutomaticForSingleCommands(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		args      []string
	}{
		{"disabled batch", "tag", []string{"tag", "batch", "--branch", "main", "--tag", "release", "--no-report"}},
		{"single tag", "tag", []string{"tag", "one", "--project", "group/a", "--branch", "main", "--tag", "release"}},
		{"single build", "build", []string{"build", "one", "--project", "group/a", "--tag", "release", "--image", "service:test"}},
		{"single deletion", "delete", []string{"tag", "delete", "one", "--project", "group/a", "--tag", "release"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			var requests atomic.Int32
			server := reportTestServer(t, tc.operation, &requests)
			defer server.Close()
			writeConfig(t, server.URL, reportTestConfig)
			args := append(append([]string{}, tc.args...), "--dry-run", "--no-progress")
			_, logs, err := executeTest(args, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat("reports"); !os.IsNotExist(err) {
				t.Fatalf("unexpected reports directory: %v", err)
			}
			if strings.Contains(logs, ".html") {
				t.Fatalf("unexpected report announcement: %q", logs)
			}
			if requests.Load() == 0 {
				t.Fatal("operation did not run")
			}
		})
	}
}

func TestBatchReportInvalidFlagsFailBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"wrong extension", []string{"--report", "report.json"}},
		{"empty path", []string{"--report="}},
		{"conflicting flags", []string{"--report", "report.html", "--no-report"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			var requests atomic.Int32
			server := reportTestServer(t, "tag", &requests)
			defer server.Close()
			writeConfig(t, server.URL, reportTestConfig)
			args := append([]string{"tag", "batch", "--branch", "main", "--tag", "release", "--dry-run"}, tc.flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || requests.Load() != 0 {
				t.Fatalf("error=%v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestBatchReportUnavailableDestinationFailsBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string
		path     string
	}{
		{"existing report", "report.html", "report.html"},
		{"parent is file", "parent", filepath.Join("parent", "report.html")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			const original = "preserve this existing file"
			if err := os.WriteFile(tc.existing, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := reportTestServer(t, "tag", &requests)
			defer server.Close()
			writeConfig(t, server.URL, reportTestConfig)
			_, _, err := executeTest([]string{"tag", "batch", "--branch", "main", "--tag", "release", "--dry-run", "--report", tc.path}, "", false)
			if ExitCode(err) != 1 || requests.Load() != 0 {
				t.Fatalf("error=%v requests=%d", err, requests.Load())
			}
			data, err := os.ReadFile(tc.existing)
			if err != nil || string(data) != original {
				t.Fatalf("existing file changed: %q error=%v", data, err)
			}
		})
	}
}
