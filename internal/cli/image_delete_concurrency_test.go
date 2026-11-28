package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDockerImageDeleteConcurrencyOverlapsProcessesWithoutLosingResultOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker uses a POSIX shell")
	}
	for _, tc := range []struct {
		name        string
		concurrency string
		matches     int
		workers     int
	}{
		{name: "default four workers", matches: 6, workers: 4},
		{name: "override two workers", concurrency: "2", matches: 6, workers: 2},
		{name: "override one worker", concurrency: "1", matches: 6, workers: 1},
		{name: "workers capped by matches", matches: 2, workers: 2},
		{name: "no matches starts no workers", matches: 0, workers: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := workingDirectory(t)
			control := filepath.Join(directory, "barriers")
			for _, child := range []string{"started", "completed", "release"} {
				if err := os.MkdirAll(filepath.Join(control, child), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var inventory bytes.Buffer
			imageID := "sha256:" + strings.Repeat("a", 64)
			wantReferences := make([]string, tc.matches)
			for i := 0; i < tc.matches; i++ {
				repository := fmt.Sprintf("team/service-%02d", i)
				wantReferences[i] = repository + ":batch"
				// Every selected tag deliberately shares one image ID. The fake
				// refuses rm-by-ID and force, even while removals overlap.
				if err := json.NewEncoder(&inventory).Encode(map[string]string{"Repository": repository, "Tag": "batch", "ID": imageID}); err != nil {
					t.Fatal(err)
				}
			}
			const script = `#!/bin/sh
if [ "$1" = image ] && [ "$2" = ls ]; then
  printf '%s' "$YAS_IMAGE_CONCURRENCY_INVENTORY"
  exit 0
fi
if [ "$1" != image ] || [ "$2" != rm ] || [ "$3" != -- ] || [ "$#" != 4 ]; then
  printf 'Unexpected Docker removal arguments\n' >&2
  exit 90
fi
case "$4" in team/service-0[0-5]:batch) ;; *) printf 'Only exact selected references may be removed\n' >&2; exit 91 ;; esac
root=$YAS_IMAGE_CONCURRENCY_CONTROL
key=${4##*/}
key=${key%:*}
lock() {
  while ! /bin/mkdir "$root/lock" 2>/dev/null; do /bin/sleep 0.01; done
}
unlock() { /bin/rmdir "$root/lock"; }
lock
active=0
peak=0
if [ -f "$root/active" ]; then read -r active < "$root/active"; fi
if [ -f "$root/peak" ]; then read -r peak < "$root/peak"; fi
active=$((active + 1))
printf '%s\n' "$active" > "$root/active"
if [ "$active" -gt "$peak" ]; then printf '%s\n' "$active" > "$root/peak"; fi
printf '%s\n' "$4" >> "$root/removals"
: > "$root/started/$key"
unlock
while [ ! -f "$root/release-all" ] && [ ! -f "$root/release/$key" ]; do /bin/sleep 0.01; done
lock
read -r active < "$root/active"
printf '%s\n' "$((active - 1))" > "$root/active"
printf '%s\n' "$4" >> "$root/finished"
: > "$root/completed/$key"
unlock
printf 'Untagged: %s\n' "$4"
`
			if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			t.Setenv("YAS_IMAGE_CONCURRENCY_CONTROL", control)
			t.Setenv("YAS_IMAGE_CONCURRENCY_INVENTORY", inventory.String())
			var stdout, stderr bytes.Buffer
			command := New(Options{In: strings.NewReader(""), Out: &stdout, Err: &stderr, ConfigPath: filepath.Join(directory, "yas.yaml")})
			reportPath := filepath.Join(directory, "deletion.html")
			args := []string{"image", "delete", "batch", "--tag", "batch", "--yes", "--format", "json", "--no-progress", "--report", reportPath}
			if tc.concurrency != "" {
				args = append(args, "--concurrency", tc.concurrency)
			}
			command.SetArgs(args)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var commandError error
			go func() {
				commandError = command.ExecuteContext(ctx)
				close(done)
			}()
			releaseAll := func() {
				if err := os.WriteFile(filepath.Join(control, "release-all"), nil, 0600); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() {
				releaseAll()
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("fake Docker removals did not stop during cleanup")
				}
			})
			if tc.workers > 0 {
				started := waitForImageDeleteMarkers(t, filepath.Join(control, "started"), tc.workers, done)
				if len(started) != tc.workers {
					t.Fatalf("more rm processes started than allowed while all were blocked: %v; limit=%d", started, tc.workers)
				}
				completed, err := os.ReadDir(filepath.Join(control, "completed"))
				if err != nil || len(completed) != 0 {
					t.Fatalf("rm processes did not actually overlap at the barrier: completed=%v error=%v", completed, err)
				}
				// Finish a later reference first, proving final output does not
				// inherit subprocess completion order when concurrency exceeds one.
				firstFinished := started[len(started)-1]
				if err := os.WriteFile(filepath.Join(control, "release", firstFinished), nil, 0600); err != nil {
					t.Fatal(err)
				}
				waitForImageDeleteMarkers(t, filepath.Join(control, "completed"), 1, done)
				finished, err := os.ReadFile(filepath.Join(control, "finished"))
				if err != nil || strings.TrimSpace(string(finished)) != "team/"+firstFinished+":batch" {
					t.Fatalf("controlled first completion was not observed: %q (%v)", finished, err)
				}
				if tc.workers > 1 && firstFinished == "service-00" {
					t.Fatal("test did not force completion out of the selected-image order")
				}
			}
			releaseAll()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("image deletion did not finish after its barriers were released")
			}
			if commandError != nil {
				t.Fatalf("command failed: %v\n%s", commandError, stderr.String())
			}
			results := decodeImageDeleteResults(t, stdout.String())
			if len(results) != tc.matches {
				t.Fatalf("missing completed results: %s", stdout.String())
			}
			for i, result := range results {
				if result.Image != wantReferences[i] || result.Status != "deleted" || result.ImageID != imageID {
					t.Errorf("ordered result %d=%+v, want %s deleted", i, result, wantReferences[i])
				}
			}
			if tc.workers > 0 {
				peakData, err := os.ReadFile(filepath.Join(control, "peak"))
				peak, parseError := strconv.Atoi(strings.TrimSpace(string(peakData)))
				if err != nil || parseError != nil || peak != tc.workers {
					t.Fatalf("actual rm process overlap exceeded or never reached worker limit: peak=%q want=%d errors=%v/%v", peakData, tc.workers, err, parseError)
				}
				active, err := os.ReadFile(filepath.Join(control, "active"))
				if err != nil || strings.TrimSpace(string(active)) != "0" {
					t.Fatalf("rm processes remain active: %q (%v)", active, err)
				}
				removalData, err := os.ReadFile(filepath.Join(control, "removals"))
				removed := strings.Fields(string(removalData))
				sort.Strings(removed)
				if err != nil || strings.Join(removed, "\n") != strings.Join(wantReferences, "\n") {
					t.Fatalf("rm did not use each exact shared-ID reference once: %v (%v)", removed, err)
				}
			} else if entries, err := os.ReadDir(filepath.Join(control, "started")); err != nil || len(entries) != 0 {
				t.Fatalf("zero matches started deletion processes: %v (%v)", entries, err)
			}
			report := readReportHTML(t, reportPath)
			if !strings.Contains(report, fmt.Sprintf("<dt>Concurrency</dt><dd>%d</dd>", tc.workers)) {
				t.Errorf("report did not record actual capped concurrency %d", tc.workers)
			}
			previous := -1
			for _, result := range results {
				index := strings.Index(report, "<strong>"+result.Project+"</strong>")
				if index <= previous {
					t.Fatalf("report follows completion order instead of selected-image order: %s", result.Project)
				}
				previous = index
			}
		})
	}
}

func waitForImageDeleteMarkers(t *testing.T, directory string, count int, commandDone <-chan struct{}) []string {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) >= count {
			names := make([]string, len(entries))
			for i, entry := range entries {
				names[i] = entry.Name()
			}
			return names
		}
		select {
		case <-commandDone:
			t.Fatalf("image command exited before %d subprocesses reached %s", count, directory)
		case <-deadline.C:
			t.Fatalf("only %d of %d Docker rm subprocesses reached %s", len(entries), count, directory)
		case <-poll.C:
		}
	}
}
