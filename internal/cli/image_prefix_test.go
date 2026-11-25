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
)

func TestImagePrefixChoice(t *testing.T) {
	for _, tc := range []struct {
		name, input, want         string
		flags                     []string
		prefixes                  []string
		interactive, prompt, fail bool
	}{
		{name: "choose", input: "1\n", prefixes: []string{"registry.example.com/team"}, interactive: true, prompt: true, want: "registry.example.com/team"},
		{name: "skip", input: "2\n", prefixes: []string{"registry.example.com/team"}, interactive: true, prompt: true},
		{name: "empty list", interactive: true, prompt: true},
		{name: "noninteractive", prefixes: []string{"registry.example.com/team"}, prompt: true},
		{name: "explicit image bypass", prefixes: []string{"registry.example.com/team"}, interactive: true},
		{name: "flag", flags: []string{"--image-prefix", "localhost:5000/team/"}, want: "localhost:5000/team"},
		{name: "skip flag", flags: []string{"--no-image-prefix"}, prefixes: []string{"registry.example.com/team"}, interactive: true, prompt: true},
		{name: "conflict", flags: []string{"--image-prefix", "team", "--no-image-prefix"}, fail: true},
		{name: "invalid", flags: []string{"--image-prefix", "https://registry.example.com"}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			a := &app{prompt: &prompter{reader: bufio.NewReader(strings.NewReader(tc.input)), out: &logs, interactive: func() bool { return tc.interactive }}}
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			var flags imagePrefixFlags
			flags.bind(cmd)
			if err := cmd.ParseFlags(tc.flags); err != nil {
				t.Fatal(err)
			}
			got, err := a.chooseImagePrefix(cmd, tc.prefixes, flags, tc.prompt)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("prefix=%q err=%v", got, err)
			}
		})
	}
}

func TestPrefixedImageNames(t *testing.T) {
	for _, tc := range []struct {
		prefix, image, want string
		fail                bool
	}{
		{"registry.example.com/team", "app:1", "registry.example.com/team/app:1", false},
		{"registry.example.com/team", "sub/app:1", "registry.example.com/team/sub/app:1", false},
		{"registry.example.com/team", "registry.example.com/team/app:1", "registry.example.com/team/app:1", false},
		{"", "other.example.com/app:1", "other.example.com/app:1", false},
		{"registry.example.com/team", "other.example.com/app:1", "", true},
		{"registry.example.com/team", "app", "", true},
	} {
		got, err := prefixedImage(tc.prefix, tc.image)
		if (err != nil) != tc.fail || got != tc.want {
			t.Errorf("%s + %s = %q, %v", tc.prefix, tc.image, got, err)
		}
	}
}

func TestBuildPrefixFlagAndInteractiveResults(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		input, want string
		interactive bool
	}{
		{name: "single flag", args: []string{"build", "one", "--project", "group/a", "--tag", "v1", "--image", "a:1", "--image-prefix", "registry.example.com/team"}, want: "registry.example.com/team/a:1"},
		{name: "single prompt", args: []string{"build", "one", "--project", "group/a", "--tag", "v1"}, input: "1\na:1\n", interactive: true, want: "registry.example.com/team/a:1"},
		{name: "batch flag", args: []string{"build", "batch", "--tag", "v1", "--all-groups", "--image-prefix", "registry.example.com/team"}, want: "registry.example.com/team/a:v1"},
		{name: "batch prompt", args: []string{"build", "batch", "--tag", "v1", "--all-groups"}, input: "1\n", interactive: true, want: "registry.example.com/team/a:v1"},
		{name: "batch skip", args: []string{"build", "batch", "--tag", "v1", "--all-groups"}, input: "2\n", interactive: true, want: "a:v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/tags/v1") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			}))
			defer server.Close()
			writeConfig(t, server.URL, "building:\n  image_prefixes: [registry.example.com/team]\n  repositories:\n    - project: group/a\n      source: {tag: v1}\n      image: a:1\n")
			args := append(append([]string{}, tc.args...), "--dry-run", "--format", "json")
			out, logs, err := executeTest(args, tc.input, tc.interactive)
			if err != nil {
				t.Fatal(err)
			}
			var results []result
			if err := json.Unmarshal([]byte(out), &results); err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Image != tc.want {
				t.Fatalf("results=%s", out)
			}
			if !strings.Contains(logs, tc.want) {
				t.Fatalf("progress missing final image: %s", logs)
			}
		})
	}
}
