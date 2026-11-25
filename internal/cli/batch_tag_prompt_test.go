package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBatchPromptsForTagWithoutConfigTags(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/branches/") {
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/tags/v2") {
			t.Errorf("unexpected tag: %s", r.URL.Path)
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  repositories:\n    - project: group/a\n")
	if _, _, err := executeTest([]string{"config", "validate"}, "", false); err != nil {
		t.Fatal(err)
	}
	out, prompts, err := executeTest([]string{"tag", "batch", "--all-groups", "--branch", "main", "--dry-run"}, "v2\n", true)
	if err != nil || !strings.Contains(prompts, "New Git tag name for all selected repositories") || !strings.Contains(out, "planned") {
		t.Fatalf("out=%s prompts=%s err=%v", out, prompts, err)
	}
	_, _, err = executeTest([]string{"tag", "batch", "--all-groups", "--branch", "main", "--non-interactive"}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("missing tag error=%v", err)
	}
}
