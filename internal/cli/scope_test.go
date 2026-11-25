package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBatchGroupSelection(t *testing.T) {
	projects := []string{"alpha/a", "alpha/team/b", "alphabet/c", "beta/d", "123"}
	for _, tc := range []struct {
		name, input string
		flags       []string
		only        []string
		interactive bool
		want        []int
		fail        bool
	}{
		{name: "exact group", flags: []string{"--group", "alpha"}, want: []int{0}},
		{name: "nested group", flags: []string{"--group", "alpha/team"}, want: []int{1}},
		{name: "interactive", interactive: true, input: "2\n", want: []int{1}},
		{name: "all interactive", interactive: true, input: "6\n", want: []int{0, 1, 2, 3, 4}},
		{name: "all flag", interactive: true, flags: []string{"--all-groups"}, want: []int{0, 1, 2, 3, 4}},
		{name: "only bypasses menu", interactive: true, only: []string{"beta/d"}, want: []int{3}},
		{name: "outside selection", flags: []string{"--group", "alpha"}, only: []string{"beta/d"}, fail: true},
		{name: "missing group", flags: []string{"--group", "missing"}, fail: true},
		{name: "empty group", flags: []string{"--group", ""}, fail: true},
		{name: "conflicting flags", flags: []string{"--group", "alpha", "--all-groups"}, fail: true},
		{name: "noninteractive default", want: []int{0, 1, 2, 3, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := &app{prompt: &prompter{reader: bufio.NewReader(strings.NewReader(tc.input)), out: &out, interactive: func() bool { return tc.interactive }}}
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			var scope groupOptions
			scope.bind(cmd)
			if err := cmd.ParseFlags(tc.flags); err != nil {
				t.Fatal(err)
			}
			got, err := a.selectBatch(cmd, projects, tc.only, scope)
			if (err != nil) != tc.fail || (!tc.fail && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("indices=%v err=%v", got, err)
			}
		})
	}
}

func TestSingleCommandsUseFullProjectPathsInsteadOfGroupFlag(t *testing.T) {
	for _, args := range [][]string{{"tag", "one"}, {"tag", "delete", "one"}, {"build", "one"}} {
		args = append(args, "--group", "company/team")
		_, _, err := executeTest(args, "", false)
		if err == nil || !strings.Contains(err.Error(), "unknown flag: --group") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestBatchFlowsRestrictRequestsByGroup(t *testing.T) {
	for _, operation := range [][]string{{"tag", "batch"}, {"tag", "delete", "batch"}, {"build", "batch", "--tag", "v1"}} {
		for _, interactive := range []bool{false, true} {
			t.Run(fmt.Sprint(operation, interactive), func(t *testing.T) {
				workingDirectory(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.Contains(r.URL.EscapedPath(), "/projects/alpha%2Fa/") {
						t.Errorf("outside selection: %s", r.URL.EscapedPath())
					}
					fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
				}))
				defer server.Close()
				writeConfig(t, server.URL, "tagging:\n  defaults:\n    tag: v1\n  repositories:\n    - project: alpha/a\n    - project: beta/b\nbuilding:\n  repositories:\n    - project: alpha/a\n      source: {tag: v1}\n      image: a:1\n    - project: beta/b\n      source: {tag: v1}\n      image: b:1\n")
				args := append(append([]string{}, operation...), "--dry-run")
				if len(operation) == 2 && operation[0] == "tag" && !interactive {
					args = append(args, "--branch", "main")
				}
				if operation[0] == "tag" {
					args = append(args, "--tag", "v1")
				}
				if !interactive {
					args = append(args, "--group", "alpha")
				}
				if _, _, err := executeTest(args, "1\nmain\n", interactive); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
