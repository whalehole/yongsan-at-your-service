package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type reportAutoOpenLauncher struct{ control string }

func setupReportAutoOpenLauncher(t *testing.T, directory string) reportAutoOpenLauncher {
	t.Helper()
	var launcher string
	switch runtime.GOOS {
	case "darwin":
		launcher = "open"
	case "linux":
		launcher = "xdg-open"
	default:
		t.Skip("fake host launcher uses a POSIX shell on macOS/Linux")
	}
	control := filepath.Join(directory, "fake-report-launcher")
	if err := os.Mkdir(control, 0700); err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
root=$YAS_REPORT_OPEN_CONTROL
printf '%s\n' "$#" > "$root/argc"
printf '%s\n' "$1" > "$root/path"
if [ "$#" != 1 ] || [ ! -f "$1" ]; then exit 91; fi
case "$1" in /*) ;; *) exit 92 ;; esac
complete=no
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in *'</html>'*) complete=yes ;; esac
done < "$1"
if [ "$complete" != yes ]; then exit 93; fi
: > "$root/ready"
if [ "$YAS_REPORT_OPEN_WAIT" = 1 ]; then
  while [ ! -f "$root/release" ]; do /bin/sleep 0.01; done
fi
printf 'launcher stdout must not enter JSON\n'
printf 'launcher stderr diagnostic\n' >&2
if [ "$YAS_REPORT_OPEN_FAIL" = 1 ]; then exit 7; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(directory, launcher), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// Only this fixture's fake launcher (and fake Docker, if present) can be
	// found by name. Tests never invoke a system browser or GUI launcher.
	t.Setenv("PATH", directory)
	t.Setenv("YAS_REPORT_OPEN_CONTROL", control)
	t.Setenv("YAS_REPORT_OPEN_WAIT", "")
	t.Setenv("YAS_REPORT_OPEN_FAIL", "")
	return reportAutoOpenLauncher{control: control}
}

func (launcher reportAutoOpenLauncher) assert(t *testing.T, opened bool, expectedPath string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(launcher.control, "path"))
	if !opened {
		if !os.IsNotExist(err) {
			t.Fatalf("report unexpectedly reached a launcher: %q (%v)", data, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("report did not reach the fake launcher: %v", err)
	}
	path := strings.TrimSuffix(string(data), "\n")
	actualResolved, actualError := filepath.EvalSymlinks(path)
	expectedResolved, expectedError := filepath.EvalSymlinks(expectedPath)
	if !filepath.IsAbs(path) || actualError != nil || expectedError != nil || actualResolved != expectedResolved {
		t.Fatalf("launcher received %q, want absolute path to report %q: %v / %v", path, expectedPath, actualError, expectedError)
	}
	arguments, err := os.ReadFile(filepath.Join(launcher.control, "argc"))
	if err != nil || string(arguments) != "1\n" {
		t.Fatalf("report path was split into launcher arguments: %q (%v)", arguments, err)
	}
	if _, err := os.Stat(filepath.Join(launcher.control, "ready")); err != nil {
		t.Fatalf("launcher did not read a complete HTML report: %v", err)
	}
}

func TestBatchReportAutoOpenConfigAndFlagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		flags        []string
		opened       bool
		saved        bool
	}{
		{name: "missing config does not launch", saved: true},
		{name: "configured true", config: "reports: {auto_open: true}\n", opened: true, saved: true},
		{name: "open flag overrides false", config: "reports: {auto_open: false}\n", flags: []string{"--open-report"}, opened: true, saved: true},
		{name: "no-open flag overrides true", config: "reports: {auto_open: true}\n", flags: []string{"--no-open-report"}, saved: true},
		{name: "explicit open false overrides true", config: "reports: {auto_open: true}\n", flags: []string{"--open-report=false"}, saved: true},
		{name: "explicit no-open false enables opening", config: "reports: {auto_open: false}\n", flags: []string{"--no-open-report=false"}, opened: true, saved: true},
		{name: "no-report suppresses configured opening", config: "reports: {auto_open: true}\n", flags: []string{"--no-report"}},
		{name: "no-report permits explicit open false", config: "reports: {auto_open: true}\n", flags: []string{"--no-report", "--open-report=false"}},
		{name: "no-report permits no-open flag", config: "reports: {auto_open: true}\n", flags: []string{"--no-report", "--no-open-report"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			directory := filepath.Dir(fixture.log)
			launcher := setupReportAutoOpenLauncher(t, directory)
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(directory, "yas.yaml"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"image", "delete", "batch", "--tag", "1.2.9", "--dry-run", "--format", "json", "--no-progress"}, tc.flags...)
			output, logs, err := executeTest(args, "", false)
			if err != nil || len(decodeImageDeleteResults(t, output)) != 3 {
				t.Fatalf("report config/flag result=%s logs=%s error=%v", output, logs, err)
			}
			paths, err := filepath.Glob(filepath.Join(directory, "reports", "*.html"))
			if err != nil || (len(paths) == 1) != tc.saved || len(paths) > 1 {
				t.Fatalf("saved report paths=%v error=%v", paths, err)
			}
			path := ""
			if tc.saved {
				path = paths[0]
			}
			launcher.assert(t, tc.opened, path)
		})
	}
}

func TestEveryBatchCommandSupportsExplicitOpenAndRejectsConflictingFlags(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		args            []string
	}{
		{name: "tag", operation: "tag", args: []string{"tag", "batch", "--branch", "main", "--tag", "release", "--only", "group/a"}},
		{name: "build", operation: "build", args: []string{"build", "batch", "--tag", "release", "--only", "group/a"}},
		{name: "tag deletion", operation: "delete", args: []string{"tag", "delete", "batch", "--tag", "release", "--only", "group/a"}},
		{name: "image deletion", operation: "image-delete", args: []string{"image", "delete", "batch", "--tag", "1.2.9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			directory := filepath.Dir(fixture.log)
			launcher := setupReportAutoOpenLauncher(t, directory)
			var requests atomic.Int32
			server := reportTestServer(t, tc.operation, &requests)
			defer server.Close()
			writeConfig(t, server.URL, reportTestConfig)
			path := filepath.Join(directory, "audit folder", `report "release".html`)
			args := append(append([]string{}, tc.args...), "--dry-run", "--format", "json", "--no-progress", "--report", path, "--open-report")
			output, logs, err := executeTest(args, "", false)
			if err != nil || len(decodeImageDeleteResults(t, output)) == 0 {
				t.Fatalf("explicit open did not work: output=%s logs=%s error=%v", output, logs, err)
			}
			launcher.assert(t, true, path)
			beforeRequests, beforeDocker := requests.Load(), len(fixture.commands(t))
			conflict := append(append([]string{}, tc.args...), "--dry-run", "--open-report", "--no-open-report")
			_, _, err = executeTest(conflict, "", false)
			if ExitCode(err) != 2 || !strings.Contains(err.Error(), "open-report") || requests.Load() != beforeRequests || len(fixture.commands(t)) != beforeDocker {
				t.Fatalf("conflicting opener flags reached a repository/Docker operation: %v", err)
			}
		})
	}
}

func TestBatchReportOpenFlagConflictsFailBeforeDocker(t *testing.T) {
	for _, flags := range [][]string{
		{"--open-report=false", "--no-open-report=false"},
		{"--open-report", "--no-report"},
		{"--no-open-report=false", "--no-report"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			launcher := setupReportAutoOpenLauncher(t, filepath.Dir(fixture.log))
			args := append([]string{"image", "delete", "batch", "--tag", "1.2.9", "--dry-run"}, flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || len(fixture.commands(t)) != 0 {
				t.Fatalf("invalid report flags reached Docker: %v", err)
			}
			launcher.assert(t, false, "")
		})
	}
}

func TestBatchReportOpensAfterSaveClosesFileEvenForFailedBatch(t *testing.T) {
	directory := workingDirectory(t)
	launcher := setupReportAutoOpenLauncher(t, directory)
	t.Setenv("YAS_REPORT_OPEN_WAIT", "1")
	report, err := reserveBatchReport(directory, "failed-batch.html", "build")
	if err != nil {
		t.Fatal(err)
	}
	report.autoOpen = true
	reservedFile := report.file
	var stdout, stderr bytes.Buffer
	a := &app{options: Options{Out: &stdout, Err: &stderr}, format: "json", noProgress: true}
	done := make(chan struct{})
	var executionError error
	go func() {
		executionError = a.executeWithReport(context.Background(), []job{func(context.Context) result {
			return result{Project: "group/failed", Operation: "build", Status: "failed", Error: "The build failed before producing an image."}
		}}, 1, time.Second, report)
		close(done)
	}()
	release := func() { _ = os.WriteFile(filepath.Join(launcher.control, "release"), nil, 0600) }
	t.Cleanup(func() {
		release()
		select {
		case <-done:
			report.cleanup()
		case <-time.After(10 * time.Second):
			t.Error("report opener did not finish after its barrier was released")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(launcher.control, "ready")); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("failed batch did not invoke the blocking fake launcher")
		case <-deadline.C:
			t.Fatal("report launcher did not receive the saved report")
		case <-ticker.C:
		}
	}
	if _, err := reservedFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("the report file was still open when the launcher ran: %v", err)
	}
	launcher.assert(t, true, report.path)
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("report opener did not finish")
	}
	if ExitCode(executionError) != 1 || !report.saved || report.file != nil || len(decodeImageDeleteResults(t, stdout.String())) != 1 {
		t.Fatalf("failed batch/report outcome was lost: saved=%v error=%v output=%s", report.saved, executionError, stdout.String())
	}
}

func TestBatchReportSaveFailureNeverInvokesLauncher(t *testing.T) {
	directory := workingDirectory(t)
	launcher := setupReportAutoOpenLauncher(t, directory)
	report, err := reserveBatchReport(directory, "broken.html", "build")
	if err != nil {
		t.Fatal(err)
	}
	defer report.cleanup()
	report.autoOpen = true
	if err := report.file.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := &app{options: Options{Out: &stdout, Err: &stderr}, format: "json", noProgress: true}
	err = a.executeWithReport(context.Background(), []job{func(context.Context) result {
		return result{Project: "group/service", Operation: "build", Status: "built"}
	}}, 1, time.Second, report)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "save batch report") || len(decodeImageDeleteResults(t, stdout.String())) != 1 {
		t.Fatalf("storage failure lost operation results: %v (%s)", err, stdout.String())
	}
	launcher.assert(t, false, "")
}

func TestCanceledBatchStillAutoOpensItsSavedReport(t *testing.T) {
	directory := workingDirectory(t)
	launcher := setupReportAutoOpenLauncher(t, directory)
	var stdout, stderr bytes.Buffer
	a := &app{options: Options{Out: &stdout, Err: &stderr, ConfigPath: filepath.Join(directory, "yas.yaml")}, format: "json", noProgress: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.executeBatch(ctx, []job{func(ctx context.Context) result {
		return result{Project: "group/canceled", Operation: "build", Status: "failed", Error: ctx.Err().Error()}
	}}, 1, time.Second, batchReportOptions{path: "canceled.html", autoOpen: true}, "build", "https://gitlab.example.com")
	results := decodeImageDeleteResults(t, stdout.String())
	if ExitCode(err) != 1 || len(results) != 1 || !strings.Contains(results[0].Error, "context canceled") {
		t.Fatalf("canceled result was lost: output=%s error=%v", stdout.String(), err)
	}
	path := filepath.Join(directory, "canceled.html")
	launcher.assert(t, true, path)
	if !strings.Contains(readReportHTML(t, path), ">Canceled</span>") || strings.Contains(stderr.String(), "Warning: could not open batch report") {
		t.Fatalf("saved canceled report was not opened independently: %s", stderr.String())
	}
}

func TestBatchReportOpenerFailureWarnsWithoutChangingSuccessOrJSON(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	directory := filepath.Dir(fixture.log)
	launcher := setupReportAutoOpenLauncher(t, directory)
	t.Setenv("YAS_REPORT_OPEN_FAIL", "1")
	output, logs, err := executeTest([]string{"image", "delete", "batch", "--tag", "1.2.9", "--dry-run", "--open-report", "--format", "json", "--no-progress"}, "", false)
	if err != nil || len(decodeImageDeleteResults(t, output)) != 3 || !strings.Contains(logs, "Warning: could not open batch report") {
		t.Fatalf("launcher failure changed success or was not warned: output=%s logs=%s error=%v", output, logs, err)
	}
	paths, _ := filepath.Glob(filepath.Join(directory, "reports", "*.html"))
	if len(paths) != 1 {
		t.Fatalf("launcher failure removed the saved report: %v", paths)
	}
	launcher.assert(t, true, paths[0])
}

func TestRootImageBatchConfiguredAutoOpenPreservesFailureAndJSON(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	directory := filepath.Dir(fixture.log)
	launcher := setupReportAutoOpenLauncher(t, directory)
	t.Setenv("YAS_REPORT_OPEN_FAIL", "1")
	t.Setenv("YAS_IMAGE_TEST_REMOVE_FAIL", "accesscontrols-service:1.2.9")
	if err := os.WriteFile(filepath.Join(directory, "yas.yaml"), []byte("reports: {auto_open: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, logs, err := executeTest([]string{"--format", "json", "--no-progress"}, "9\n2\n1.2.9\nyes\n", true)
	results := decodeImageDeleteResults(t, output)
	failed := 0
	for _, result := range results {
		if result.Status == "failed" {
			failed++
		}
	}
	if ExitCode(err) != 1 || len(results) != 3 || failed != 1 || !strings.Contains(logs, "Warning: could not open batch report") {
		t.Fatalf("root batch or opener changed partial failure: output=%s logs=%s error=%v", output, logs, err)
	}
	paths, _ := filepath.Glob(filepath.Join(directory, "reports", "*.html"))
	if len(paths) != 1 {
		t.Fatalf("failed root batch did not save a report: %v", paths)
	}
	launcher.assert(t, true, paths[0])
}

func TestBatchReportAutoOpenUsesOperationalWorkspaceConfig(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace *bool
		other     bool
		opened    bool
	}{
		{name: "workspace enables despite locator and caller", workspace: boolReportAutoOpen(true), opened: true},
		{name: "workspace disables despite locator and caller", workspace: boolReportAutoOpen(false), other: true},
		{name: "missing operational config does not inherit locator", other: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			caller := filepath.Dir(fixture.log)
			launcher := setupReportAutoOpenLauncher(t, caller)
			binaryDirectory, workspace := t.TempDir(), t.TempDir()
			bootstrap := filepath.Join(binaryDirectory, "yas.yaml")
			if err := os.WriteFile(bootstrap, []byte(fmt.Sprintf("working_directory: %q\nreports: {auto_open: %t}\n", workspace, tc.other)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(caller, "yas.yaml"), []byte(fmt.Sprintf("reports: {auto_open: %t}\n", tc.other)), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.workspace != nil {
				if err := os.WriteFile(filepath.Join(workspace, "yas.yaml"), []byte(fmt.Sprintf("reports: {auto_open: %t}\n", *tc.workspace)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			output, logs, err := workspaceCommand(t, bootstrap, "image", "delete", "batch", "--tag", "1.2.9", "--dry-run", "--format", "json", "--no-progress")
			if err != nil || len(decodeImageDeleteResults(t, output)) != 3 {
				t.Fatalf("workspace auto-open configuration failed: output=%s logs=%s error=%v", output, logs, err)
			}
			paths, _ := filepath.Glob(filepath.Join(workspace, "reports", "*.html"))
			if len(paths) != 1 {
				t.Fatalf("workspace report missing: %v", paths)
			}
			launcher.assert(t, tc.opened, paths[0])
		})
	}
}

func boolReportAutoOpen(value bool) *bool { return &value }
