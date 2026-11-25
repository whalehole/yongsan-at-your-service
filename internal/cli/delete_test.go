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

func TestDeleteBatchUsesOnlyProjectsAndDeduplicates(t *testing.T) {
	workingDirectory(t)
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.EscapedPath(), "/tags/release%2Fv1") {
			t.Errorf("path=%s", r.URL.EscapedPath())
		}
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			if strings.Contains(r.URL.Path, "group/b") {
				w.WriteHeader(403)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	// Deliberately omit source/tag settings: deletion needs project identities only.
	writeConfig(t, server.URL, "tagging:\n  repositories:\n    - project: group/a\n    - project: group/a\n    - project: group/b\nbuilding:\n  repositories:\n    - project: group/never\n")
	out, _, err := executeTest([]string{"tag", "delete", "batch", "--tag", "release/v1", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != "deleted" || results[1].Status != "failed" || deletes.Load() != 2 {
		t.Fatalf("results=%s deletes=%d", out, deletes.Load())
	}
}

func TestDeletePromptsAndDryRunDoesNotMutate(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("dry run mutated tag")
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  defaults:\n    tag: never-inherit\n")
	out, prompts, err := executeTest([]string{"tag", "delete", "one", "--dry-run"}, "group/service\nv1\n", true)
	if err != nil || !strings.Contains(out, "planned") || !strings.Contains(prompts, "Exact Git tag name to delete") {
		t.Fatalf("out=%q prompts=%q err=%v", out, prompts, err)
	}
	_, _, err = executeTest([]string{"tag", "delete", "one", "--project", "group/service", "--non-interactive"}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("missing tag error=%v", err)
	}
}

func TestDeleteBatchSelection(t *testing.T) {
	workingDirectory(t)
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "group/a") {
			t.Errorf("unselected project accessed: %s", r.URL.Path)
		}
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  repositories:\n    - project: group/a\n    - project: group/b\n")
	_, _, err := executeTest([]string{"tag", "delete", "batch", "--tag", "v1", "--only", "group/a", "--concurrency", "2"}, "", false)
	if err != nil || deletes.Load() != 1 {
		t.Fatalf("deletes=%d err=%v", deletes.Load(), err)
	}
}
