package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReportOpenCommandUsesPlatformHandlerAndOpaqueArguments(t *testing.T) {
	for _, tc := range []struct {
		os, path, command string
		args              []string
	}{
		{"darwin", "/tmp/release notes #1 % & 中文.html", "open", []string{"/tmp/release notes #1 % & 中文.html"}},
		{"linux", "/tmp/$(not-a-command); release #1.html", "xdg-open", []string{"/tmp/$(not-a-command); release #1.html"}},
		{"windows", `C:\Users\Zoë\Reports\release #1%?.html`, "rundll32.exe", []string{"url.dll,FileProtocolHandler", "file:///C:/Users/Zo%C3%AB/Reports/release%20%231%25%3F.html"}},
		{"windows", `c:/reports/release & notes.html`, "rundll32.exe", []string{"url.dll,FileProtocolHandler", "file:///c:/reports/release%20&%20notes.html"}},
		{"windows", `\\server\share\release #1.html`, "rundll32.exe", []string{"url.dll,FileProtocolHandler", "file://server/share/release%20%231.html"}},
		{"windows", `\\?\C:\Reports\release notes.html`, "rundll32.exe", []string{"url.dll,FileProtocolHandler", "file:///C:/Reports/release%20notes.html"}},
		{"windows", `\\?\UNC\Server\share\release notes.html`, "rundll32.exe", []string{"url.dll,FileProtocolHandler", "file://Server/share/release%20notes.html"}},
	} {
		t.Run(tc.os+" "+tc.path, func(t *testing.T) {
			command, args, err := reportOpenCommand(tc.os, tc.path)
			if err != nil || command != tc.command || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("command=%q args=%q error=%v want=%q %q", command, args, err, tc.command, tc.args)
			}
		})
	}
}

func TestReportOpenCommandRejectsUnsupportedOrNonabsolutePaths(t *testing.T) {
	for _, tc := range []struct{ os, path string }{
		{"freebsd", "/tmp/report.html"}, {"linux", "relative.html"}, {"darwin", "--malicious-option"},
		{"windows", `C:relative.html`}, {"windows", `\relative.html`}, {"windows", `\\server`}, {"windows", `\\.\C:\report.html`},
		{"linux", "/tmp/invalid\x00.html"},
	} {
		if command, args, err := reportOpenCommand(tc.os, tc.path); err == nil || command != "" || args != nil {
			t.Errorf("invalid command accepted: os=%q path=%q command=%q args=%q error=%v", tc.os, tc.path, command, args, err)
		}
	}
}

func fakeReportLauncher(t *testing.T, body string) (string, string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("fake report launcher uses a POSIX shell; platform command construction is tested separately")
	}
	name := "open"
	if runtime.GOOS == "linux" {
		name = "xdg-open"
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, name)
	argsFile := filepath.Join(directory, "args")
	t.Setenv("YAS_REPORT_OPEN_TEST_ARGS", argsFile)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$YAS_REPORT_OPEN_TEST_ARGS\"\n" + body + "\n"
	if err := os.WriteFile(executable, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return executable, argsFile
}

func TestOpenBatchReportExecutesFakeHostLauncherWithoutShellInterpretation(t *testing.T) {
	_, argsFile := fakeReportLauncher(t, "printf 'launcher stdout must be discarded\\n'")
	filename := filepath.Join(t.TempDir(), "release 中文 #1 % $(not-a-command); &.html")
	if err := os.WriteFile(filename, []byte("<!doctype html><title>test report</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := openBatchReport(filename); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if !reflect.DeepEqual(args, []string{filename}) {
		t.Fatalf("filename was not passed as one opaque argument: %q", args)
	}
}

func TestOpenBatchReportReturnsBoundedQuotedDiagnostics(t *testing.T) {
	fakeReportLauncher(t, "printf '\\033[31mfailed\\nnext\\tline\\n' >&2\nprintf '%06000d' 0 >&2\nexit 9")
	filename := filepath.Join(t.TempDir(), "release\n\tname.html")
	err := openBatchReport(filename)
	if err == nil || !strings.Contains(err.Error(), "default report launcher") || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "exit status 9") || !strings.Contains(err.Error(), "stderr truncated") || len(err.Error()) > 7000 {
		t.Fatalf("missing or unbounded opener diagnostics: %v", err)
	}
	for _, control := range []rune{'\n', '\r', '\t', '\x1b'} {
		if strings.ContainsRune(err.Error(), control) {
			t.Errorf("raw terminal control %q appeared in opener error: %q", control, err.Error())
		}
	}
	if !strings.Contains(err.Error(), `\n`) || !strings.Contains(err.Error(), `\t`) {
		t.Fatalf("filename control characters were not quoted: %q", err.Error())
	}
}

func TestReportLauncherTimeoutIsBoundedAndPreservesCause(t *testing.T) {
	executable, _ := fakeReportLauncher(t, "exec sleep 30")
	start := time.Now()
	err := runReportOpen(executable, []string{"/tmp/report.html"}, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("headless launcher did not time out promptly: %v", err)
	}
}

func TestOpenBatchReportMissingHandlerReturnsAnErrorWithoutFallback(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("PATH-only launcher test runs on POSIX hosts")
	}
	t.Setenv("PATH", t.TempDir())
	if err := openBatchReport("/tmp/report.html"); err == nil || !strings.Contains(err.Error(), "default report launcher") {
		t.Fatalf("missing handler did not fail clearly: %v", err)
	}
}
