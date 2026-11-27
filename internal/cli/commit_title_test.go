package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBatchReportKeepsPinnedCommitTitleThroughValidationAndFailure(t *testing.T) {
	workingDirectory(t)
	const title = "Pinned checkout fix"
	otherSHA := strings.Repeat("b", 40)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/"+testSHA):
			fmt.Fprintf(w, `{"id":%q,"title":%q}`, testSHA, title)
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			fmt.Fprintf(w, `{"commit":{"id":%q,"title":"Unrelated branch tip"}}`, otherSHA)
		case strings.HasSuffix(r.URL.Path, "/merge_base"):
			fmt.Fprintf(w, `{"id":%q}`, testSHA)
		case strings.HasSuffix(r.URL.Path, "/tags/release"):
			fmt.Fprintf(w, `{"commit":{"id":%q,"title":"Conflicting destination title"}}`, otherSHA)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	writeConfig(t, server.URL, fmt.Sprintf("tagging:\n  repositories:\n    - project: group/a\n      commit: %s\n", testSHA))
	out, _, err := executeTest([]string{"tag", "batch", "--branch", "main", "--tag", "release", "--dry-run", "--format", "json", "--no-progress", "--report", "report.html"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("expected destination conflict, got %v", err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 || results[0].Status != "failed" || results[0].Commit != testSHA || results[0].CommitTitle != title {
		t.Fatalf("pinned metadata was lost: %s error=%v", out, err)
	}
	html := readReportHTML(t, "report.html")
	if !strings.Contains(html, title) || strings.Contains(html, "Unrelated branch tip") || strings.Contains(html, "Conflicting destination title") {
		t.Fatalf("report associated the wrong commit title: %s", html)
	}
	if requests.Load() != 4 {
		t.Fatalf("unexpected metadata request: %d", requests.Load())
	}
}

func TestBatchDeleteReportUsesTitleObservedBeforeTagRemoval(t *testing.T) {
	workingDirectory(t)
	const title = "Remove obsolete checkout release"
	var lookups, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tags/old-release") {
			t.Errorf("unexpected metadata request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet || deletes.Load() != 0 {
			t.Error("title was fetched after deletion")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		lookups.Add(1)
		fmt.Fprintf(w, `{"message":"Tag annotation is not the title","commit":{"id":%q,"title":%q}}`, testSHA, title)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  repositories:\n    - project: group/a\n")
	out, _, err := executeTest([]string{"tag", "delete", "batch", "--tag", "old-release", "--format", "json", "--no-progress", "--report", "report.html"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 || results[0].Status != "deleted" || results[0].Commit != testSHA || results[0].CommitTitle != title {
		t.Fatalf("deletion metadata was lost: %s error=%v", out, err)
	}
	if lookups.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("unexpected request counts: lookups=%d deletes=%d", lookups.Load(), deletes.Load())
	}
	if html := readReportHTML(t, "report.html"); !strings.Contains(html, title) || strings.Contains(html, "Tag annotation is not the title") {
		t.Fatalf("wrong deletion title: %s", html)
	}
}
