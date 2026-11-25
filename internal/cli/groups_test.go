package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestProjectGroupPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, input, existing, want      string
		groups                           []string
		interactive, wantMenu, wantError bool
	}{
		{name: "group prefix", groups: []string{"group", "company/team"}, input: "2\nservice\n", want: "company/team/service", interactive: true, wantMenu: true},
		{name: "relative subgroup", groups: []string{"group"}, input: "1\nsub/service\n", want: "group/sub/service", interactive: true, wantMenu: true},
		{name: "skip prefix", groups: []string{"group"}, input: "2\nother/service\n", want: "other/service", interactive: true, wantMenu: true},
		{name: "skip numeric ID", groups: []string{"group"}, input: "2\n123\n", want: "123", interactive: true, wantMenu: true},
		{name: "empty list", input: "other/service\n", want: "other/service", interactive: true},
		{name: "explicit project", groups: []string{"group"}, existing: "other/service", want: "other/service", interactive: true},
		{name: "noninteractive", groups: []string{"group"}, wantError: true},
		{name: "retry invalid relative path", groups: []string{"group"}, input: "1\n../bad\nservice\n", want: "group/service", interactive: true, wantMenu: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &prompter{reader: bufio.NewReader(strings.NewReader(tc.input)), out: &out, interactive: func() bool { return tc.interactive }}
			value := tc.existing
			err := p.project(context.Background(), tc.groups, &value)
			if (err != nil) != tc.wantError || value != tc.want {
				t.Fatalf("value=%q err=%v", value, err)
			}
			if strings.Contains(out.String(), "Select a GitLab group") != tc.wantMenu {
				t.Fatalf("prompts=%q", out.String())
			}
			if tc.wantMenu && !strings.Contains(out.String(), "Skip") {
				t.Fatal("missing skip choice")
			}
		})
	}
}

func TestSingleOperationsUseSelectedGroup(t *testing.T) {
	for _, args := range [][]string{
		{"tag", "one", "--branch", "main", "--tag", "v1", "--dry-run"},
		{"tag", "delete", "one", "--tag", "v1", "--dry-run"},
		{"build", "one", "--tag", "v1", "--image", "service:1", "--dry-run"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			workingDirectory(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.EscapedPath(), "/projects/company%2Fteam%2Fservice/") {
					t.Errorf("wrong project: %s", r.URL.EscapedPath())
				}
				if r.Method != http.MethodGet {
					t.Errorf("dry-run mutation: %s", r.Method)
				}
				fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			}))
			defer server.Close()
			content := fmt.Sprintf("version: 1\ngitlab:\n  url: %s\n  token: test-token\n  groups:\n    - company/team\n", server.URL)
			if err := os.WriteFile("yas.yaml", []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := executeTest(args, "1\nservice\n", true); err != nil {
				t.Fatal(err)
			}
		})
	}
}
