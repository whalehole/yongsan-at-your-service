package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildBatchTagFromFlagOrPrompt(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprint(interactive), func(t *testing.T) {
			workingDirectory(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/tags/v2") {
					t.Errorf("unexpected source request: %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			}))
			defer server.Close()
			// Both omitted and legacy source fields must use the runtime selection.
			writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group/a\n    - project: group/b\n      source: {tag: old-tag}\n")
			args := []string{"build", "batch", "--all-groups", "--no-image-prefix", "--dry-run", "--format", "json"}
			if !interactive {
				args = append(args, "--tag", "v2", "--non-interactive")
			}
			out, prompts, err := executeTest(args, "v2\n", interactive)
			if err != nil {
				t.Fatal(err)
			}
			var results []result
			if err := json.Unmarshal([]byte(out), &results); err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 || results[0].Image != "a:v2" || results[1].Image != "b:v2" {
				t.Fatalf("results=%s", out)
			}
			if interactive && strings.Count(prompts, "Source Git tag for all selected repositories") != 1 {
				t.Fatalf("prompts=%s", prompts)
			}
		})
	}
}

func TestBuildBatchRequiresRuntimeTag(t *testing.T) {
	workingDirectory(t)
	writeConfig(t, "https://gitlab.example.com", "building:\n  repositories:\n    - project: group/a\n")
	if _, _, err := executeTest([]string{"config", "validate"}, "", false); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeTest([]string{"build", "batch", "--all-groups", "--non-interactive"}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("error=%v", err)
	}
}
