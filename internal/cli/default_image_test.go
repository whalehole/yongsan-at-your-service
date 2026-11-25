package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultImageNames(t *testing.T) {
	for _, tc := range []struct {
		name, project, tag, commit, prefix, override, input, want string
		interactive                                               bool
	}{
		{name: "tag", project: "group/dummy-1", tag: "v1.2.0", want: "dummy-1:v1.2.0"},
		{name: "subgroup", project: "group/team/dummy-1", tag: "v1", want: "dummy-1:v1"},
		{name: "resolved commit", project: "group/dummy-1", commit: "0123456", want: "dummy-1:" + testSHA},
		{name: "numeric project", project: "123", tag: "v1", want: "dummy-1:v1"},
		{name: "prefix", project: "group/dummy-1", tag: "v1", prefix: "registry.example.com/team", want: "registry.example.com/team/dummy-1:v1"},
		{name: "explicit override", project: "group/dummy-1", tag: "v1", override: "custom:2", want: "custom:2"},
		{name: "interactive accepts default", project: "group/dummy-1", tag: "v1", input: "\n", interactive: true, want: "dummy-1:v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/projects/123"):
					fmt.Fprint(w, `{"path":"dummy-1"}`)
				case strings.Contains(r.URL.Path, "/commits/"):
					fmt.Fprintf(w, `{"id":%q}`, testSHA)
				default:
					fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
				}
			}))
			defer server.Close()
			writeConfig(t, server.URL, "")
			args := []string{"build", "one", "--project", tc.project, "--dry-run", "--format", "json"}
			if tc.tag != "" {
				args = append(args, "--tag", tc.tag)
			} else {
				args = append(args, "--commit", tc.commit)
			}
			if tc.prefix != "" {
				args = append(args, "--image-prefix", tc.prefix)
			}
			if tc.override != "" {
				args = append(args, "--image", tc.override)
			}
			out, _, err := executeTest(args, tc.input, tc.interactive)
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
		})
	}
}

func TestBatchDefaultImageCollisionFailsWithoutOverwriting(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA) }))
	defer server.Close()
	writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group-a/service\n      source: {tag: v1}\n    - project: group-b/service\n      source: {tag: v1}\n")
	if _, _, err := executeTest([]string{"config", "validate"}, "", false); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeTest([]string{"build", "batch", "--tag", "v1", "--all-groups", "--dry-run", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
		if r.Image != "service:v1" {
			t.Fatalf("image=%s", r.Image)
		}
	}
	if counts["planned"] != 1 || counts["failed"] != 1 {
		t.Fatalf("results=%s", out)
	}
}
