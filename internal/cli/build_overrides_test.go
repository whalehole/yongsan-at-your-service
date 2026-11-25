package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

func TestInteractiveContextOverride(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"\n", "."}, {"../bad\nservices/api\n", "services/api"}} {
		var logs bytes.Buffer
		a := &app{prompt: &prompter{reader: bufio.NewReader(strings.NewReader(tc.input)), out: &logs, interactive: func() bool { return true }}}
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		opts := config.BuildOptions{Context: "."}
		if err := a.buildContext(cmd, &opts); err != nil || opts.Context != tc.want {
			t.Fatalf("context=%q err=%v", opts.Context, err)
		}
	}
}

func TestBatchImageOverrideValidation(t *testing.T) {
	for _, value := range []string{"other/a=custom:1", "group/a=invalid", "custom:1"} {
		if _, err := batchImageOverrides([]string{value}, []string{"group/a"}); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if _, err := batchImageOverrides([]string{"group/a=a:1", "group/a=b:1"}, []string{"group/a"}); err == nil {
		t.Fatal("duplicate override accepted")
	}
}

func TestBatchImagesDefaultWithoutPromptsAndAllowExplicitFlags(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprint(interactive), func(t *testing.T) {
			workingDirectory(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA) }))
			defer server.Close()
			writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group/a\n    - project: group/b\n")
			args := []string{"build", "batch", "--tag", "v1", "--all-groups", "--no-image-prefix", "--dry-run", "--format", "json"}
			input := "services/api\n"
			if !interactive {
				args = append(args, "--image", "group/a=custom:2", "--context", "services/api", "--non-interactive")
			}
			out, prompts, err := executeTest(args, input, interactive)
			if err != nil {
				t.Fatal(err)
			}
			var results []result
			if err := json.Unmarshal([]byte(out), &results); err != nil {
				t.Fatal(err)
			}
			wantFirst := "custom:2"
			if interactive {
				wantFirst = "a:v1"
			}
			if len(results) != 2 || results[0].Image != wantFirst || results[1].Image != "b:v1" {
				t.Fatalf("results=%s", out)
			}
			if strings.Contains(prompts, "Output Docker image") {
				t.Fatalf("batch must not prompt for image names: %s", prompts)
			}
			if interactive && !strings.Contains(prompts, "Build context [.]") {
				t.Fatalf("prompts=%s", prompts)
			}
		})
	}
}
