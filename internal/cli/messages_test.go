package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceLookupFailureShowsBuildAndSaveWereNotRun(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tags/v1") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group/service\n")
	t.Setenv("PATH", t.TempDir())
	output, _, err := executeTest([]string{"build", "batch", "--tag", "v1", "--save", "--format", "json", "--report", "errors.html"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 1 || results[0].Status != "failed" || results[0].BuildStatus != "not-run" || results[0].SaveStatus != "not-run" {
		t.Fatalf("source failure should not imply a failed Docker command: %s", output)
	}
	for _, want := range []string{"look up tag", "v1", "group/service", "403"} {
		if !strings.Contains(results[0].Error, want) {
			t.Errorf("source error lacks %q: %s", want, results[0].Error)
		}
	}
	if strings.Contains(results[0].Error, "Docker") {
		t.Fatalf("source error was mislabeled as Docker failure: %s", results[0].Error)
	}
	if _, err := os.Stat("images"); !os.IsNotExist(err) {
		t.Fatalf("source failure created archive output: %v", err)
	}
	report := readReportHTML(t, "errors.html")
	if strings.Count(report, ">NOT RUN</span>") != 2 {
		t.Fatalf("report did not identify both unattempted stages: %s", report)
	}
}

func TestReconciledTagCreationDoesNotClaimNoTagWasCreated(t *testing.T) {
	workingDirectory(t)
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
			} else {
				fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			}
		}
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  repositories:\n    - project: group/service\n")
	output, _, err := executeTest([]string{"tag", "batch", "--branch", "main", "--tag", "v1", "--format", "json", "--no-report"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	results := decodeSaveResults(t, output)
	if len(results) != 1 || results[0].Status != "skipped" || !strings.Contains(results[0].Message, "no further change was needed") {
		t.Fatalf("reconciled creation explanation=%s", output)
	}
	if strings.Contains(results[0].Message, "no tag was created") {
		t.Fatalf("POST result was uncertain; message must not claim no creation: %s", results[0].Message)
	}
}
