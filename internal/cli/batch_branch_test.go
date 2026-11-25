package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBatchUsesSharedBranchFromFlagOrPrompt(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprint(interactive), func(t *testing.T) {
			workingDirectory(t)
			var branches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("dry-run wrote to GitLab")
				}
				if strings.Contains(r.URL.Path, "/branches/") {
					branches.Add(1)
					if !strings.HasSuffix(r.URL.EscapedPath(), "/branches/release%2Ftest") {
						t.Errorf("wrong branch: %s", r.URL.EscapedPath())
					}
					fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
					return
				}
				w.WriteHeader(404)
			}))
			defer server.Close()
			writeConfig(t, server.URL, "tagging:\n  defaults:\n    tag: v1\n  repositories:\n    - project: group/a\n    - project: group/b\n")
			args := []string{"tag", "batch", "--all-groups", "--dry-run"}
			if !interactive {
				args = append(args, "--branch", "release/test", "--non-interactive")
			}
			_, prompts, err := executeTest(args, "release/test\n", interactive)
			if err != nil || branches.Load() != 2 {
				t.Fatalf("branch requests=%d err=%v", branches.Load(), err)
			}
			if interactive && strings.Count(prompts, "Source branch for all selected repositories") != 1 {
				t.Fatalf("prompts=%s", prompts)
			}
		})
	}
}

func TestBatchRequiresBranchWithoutChangingDelete(t *testing.T) {
	workingDirectory(t)
	writeConfig(t, "https://gitlab.example.com", "tagging:\n  defaults:\n    tag: v1\n  repositories:\n    - project: group/a\n")
	_, _, err := executeTest([]string{"tag", "batch", "--all-groups", "--non-interactive"}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--branch") {
		t.Fatalf("error=%v", err)
	}
}
