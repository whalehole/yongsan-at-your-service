package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

// These tests remain serial because yas deliberately uses the process cwd.
func workingDirectory(t *testing.T) string {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func writeConfig(t *testing.T, serverURL, content string) {
	t.Helper()
	base := fmt.Sprintf("version: 1\ngitlab:\n  url: %s\n  token_env: YAS_TEST_TOKEN\n", serverURL)
	if err := os.WriteFile("yas.yaml", []byte(base+content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAS_TEST_TOKEN", "test-token")
}

func executeTest(args []string, input string, interactive bool) (string, string, error) {
	var stdout, stderr bytes.Buffer
	// Accept optional build overrides at their defaults after scripted required answers.
	if interactive {
		input += "\n\n\n\n\n"
	}
	cmd := New(Options{In: strings.NewReader(input), Out: &stdout, Err: &stderr, Interactive: interactive, Version: "test"})
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestFixedConfigMissingBeforePrompts(t *testing.T) {
	workingDirectory(t)
	_, prompts, err := executeTest([]string{"tag", "one"}, "ignored\n", true)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "yas.yaml was not found") || !strings.Contains(err.Error(), "yas config init") {
		t.Fatalf("error = %v", err)
	}
	if prompts != "" {
		t.Fatalf("prompted before finding configuration: %q", prompts)
	}
	_, _, err = executeTest([]string{"tag", "batch", "--config", "other.yaml"}, "", false)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--config was accepted: %v", err)
	}
}

func TestHelpVersionInitAndValidateWithoutCredentials(t *testing.T) {
	workingDirectory(t)
	for _, args := range [][]string{{"--help"}, {"tag", "one", "--help"}, {"help", "build", "one"}, {"version"}} {
		out, _, err := executeTest(args, "", false)
		if err != nil || out == "" {
			t.Fatalf("%v: output=%q err=%v", args, out, err)
		}
	}
	if _, _, err := executeTest([]string{"config", "init"}, "", false); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("yas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeTest([]string{"config", "validate"}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeTest([]string{"config", "init"}, "", false); err == nil {
		t.Fatal("init overwrote existing configuration")
	}
	current, _ := os.ReadFile("yas.yaml")
	if !bytes.Equal(current, original) {
		t.Fatal("existing configuration changed")
	}
}

func TestOnePromptsOnlyForMissingRequiredFields(t *testing.T) {
	workingDirectory(t)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("authentication header missing")
		}
		switch {
		case strings.Contains(r.URL.Path, "/branches/main"):
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tags/"):
			w.WriteHeader(404)
		case r.Method == http.MethodPost:
			_ = r.ParseForm()
			if r.Form.Get("tag_name") != "v1.2.0" || r.Form.Get("ref") != testSHA {
				t.Errorf("unexpected tag form: %v", r.Form)
			}
			posts.Add(1)
			w.WriteHeader(201)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	writeConfig(t, server.URL, "")
	out, prompts, err := executeTest([]string{"tag", "one", "--project", "group/service", "--branch", "main"}, "\nv1.2.0\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompts, "GitLab project") || strings.Contains(prompts, "Select tag source") || !strings.Contains(prompts, "A value is required") {
		t.Fatalf("unexpected prompts: %q", prompts)
	}
	if posts.Load() != 1 || !strings.Contains(out, "created") {
		t.Fatalf("posts=%d output=%q", posts.Load(), out)
	}
}

func TestFullyInteractiveTagAndRootMenu(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/branches/main") {
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			return
		}
		if strings.Contains(r.URL.Path, "/tags/v1") {
			w.WriteHeader(404)
			return
		}
		t.Errorf("unexpected dry-run request: %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "")
	out, prompts, err := executeTest([]string{"--dry-run"}, "1\ninvalid\ngroup/service\n1\nmain\nv1\n", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Choose an operation", "GitLab project path or ID", "Select tag source", "Branch", "New Git tag name"} {
		if !strings.Contains(prompts, expected) {
			t.Errorf("missing prompt %q in %q", expected, prompts)
		}
	}
	if !strings.Contains(out, "planned") {
		t.Fatalf("output=%q", out)
	}
}

func TestNonInteractiveDoesNotInheritSingleCommandIdentity(t *testing.T) {
	workingDirectory(t)
	writeConfig(t, "https://gitlab.com", "tagging:\n  defaults:\n    tag: configured\n  repositories:\n    - project: group/service\n")
	for _, args := range [][]string{
		{"tag", "one", "--non-interactive"},
		{"tag", "one", "--project", "group/service", "--branch", "main", "--non-interactive"},
	} {
		_, prompts, err := executeTest(args, "", true)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), "missing required --") || prompts != "" {
			t.Fatalf("args=%v prompts=%q err=%v", args, prompts, err)
		}
	}
}

func TestTagBatchOverridesAndSelection(t *testing.T) {
	workingDirectory(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "group/b") {
			t.Error("unselected repository was accessed")
		}
		if strings.Contains(r.URL.Path, "/branches/main") {
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags/v2") {
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("tag_name") != "v2" || r.Form.Get("message") != "release" {
				t.Errorf("form=%v", r.Form)
			}
			w.WriteHeader(201)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "tagging:\n  concurrency: 0\n  defaults:\n    tag: 'invalid tag'\n  repositories:\n    - project: group/a\n      tag: old\n    - project: group/b\n      tag: old\n")
	out, _, err := executeTest([]string{"tag", "batch", "--branch", "main", "--concurrency", "2", "--tag", "v2", "--message", "release", "--only", "group/a", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Project != "group/a" || results[0].Tag != "v2" || results[0].Status != "created" || requests.Load() != 3 {
		t.Fatalf("results=%#v requests=%d", results, requests.Load())
	}
}

func TestInvalidBatchSelectionAndEmptyTag(t *testing.T) {
	workingDirectory(t)
	writeConfig(t, "https://gitlab.com", "tagging:\n  defaults:\n    tag: v1\n  repositories:\n    - project: group/a\n")
	for _, flags := range [][]string{{"--only", "group/missing"}, {"--tag", ""}} {
		args := append([]string{"tag", "batch"}, flags...)
		_, _, err := executeTest(args, "", false)
		if ExitCode(err) != 2 {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestBuildDryRunResolvesWithoutDockerOrArchive(t *testing.T) {
	workingDirectory(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/tags/v1") {
			t.Errorf("unexpected dry-run request: %s", r.URL)
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "")
	t.Setenv("PATH", t.TempDir())
	out, _, err := executeTest([]string{"build", "one", "--dry-run", "--format", "json"}, "group/service\n1\nv1\nservice:1\n", true)
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Commit != testSHA || results[0].Status != "planned" || requests.Load() != 1 {
		t.Fatalf("results=%#v requests=%d", results, requests.Load())
	}
}

func TestBuildRunsSelectedArchiveWithEffectiveOptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker uses a POSIX shell")
	}
	dir := workingDirectory(t)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tw := tar.NewWriter(gz)
	content := "FROM scratch\n"
	if err := tw.WriteHeader(&tar.Header{Name: "repo-sha/services/api/Dockerfile.release", Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags/v1") {
			fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/archive.tar.gz") {
			if r.URL.Query().Get("sha") != testSHA || r.URL.Query().Get("path") != "services/api" {
				t.Errorf("archive query=%v", r.URL.Query())
			}
			_, _ = w.Write(compressed.Bytes())
			return
		}
		t.Errorf("unexpected request: %s", r.URL)
		w.WriteHeader(500)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "building:\n  defaults:\n    no_cache: true\n    build_args:\n      APP_ENV: default\n      KEEP: retained\n")
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YAS_TEST_ARGS\"\nprintf 'build log\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("YAS_TEST_ARGS", argsFile)
	out, logs, err := executeTest([]string{"build", "one", "--project", "group/service", "--tag", "v1", "--context", "services/api", "--dockerfile", "Dockerfile.release", "--no-cache=false", "--pull", "--build-arg", "APP_ENV=cli", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "build\n--pull\n--build-arg\nAPP_ENV=cli\n--build-arg\nKEEP=retained\n--file\nDockerfile.release\n--tag\nservice:v1\n.\n"
	if string(args) != want {
		t.Fatalf("Docker args=%q want=%q", args, want)
	}
	if !strings.Contains(logs, "[group/service -> service:v1] build log") {
		t.Fatalf("logs=%q", logs)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 || results[0].Status != "built" {
		t.Fatalf("results=%s err=%v", out, err)
	}
}

func TestBatchPartialFailureReturnsJSONAndExecutionExitCode(t *testing.T) {
	workingDirectory(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "group/b") {
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	defer server.Close()
	writeConfig(t, server.URL, "building:\n  repositories:\n    - project: group/a\n      source:\n        tag: v1\n      image: a:1\n    - project: group/b\n      source:\n        tag: v1\n      image: b:1\n")
	out, _, err := executeTest([]string{"build", "batch", "--tag", "v1", "--dry-run", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("exit code=%d err=%v", ExitCode(err), err)
	}
	var results []result
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != "planned" || results[1].Status != "failed" || results[1].Error == "" {
		t.Fatalf("results=%#v", results)
	}
}
