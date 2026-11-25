package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveBatchMetadata struct {
	BatchID          string `json:"batch_id"`
	ArchiveDirectory string `json:"archive_directory"`
}

func decodeArchiveBatchMetadata(t *testing.T, output string) []archiveBatchMetadata {
	t.Helper()
	var metadata []archiveBatchMetadata
	if err := json.Unmarshal([]byte(output), &metadata); err != nil {
		t.Fatalf("decode archive batch metadata: %v", err)
	}
	return metadata
}

func TestSaveBatchSharesTimeIDAndDeterministicFilenamesAcrossRepeatedRuns(t *testing.T) {
	f := setupSaveCLI(t)
	var previousID string
	for run := 0; run < 2; run++ {
		reportPath := filepath.Join("audit", fmt.Sprintf("export-%d.html", run))
		output, _, err := executeTest([]string{"build", "batch", "--tag", "v1", "--save", "--concurrency", "2", "--format", "json", "--report", reportPath}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		results := decodeSaveResults(t, output)
		metadata := decodeArchiveBatchMetadata(t, output)
		if len(results) != 2 || len(metadata) != 2 {
			t.Fatalf("results=%s", output)
		}
		id := metadata[0].BatchID
		if id == "" || id == previousID {
			t.Fatalf("batch IDs are empty or reused: previous=%q current=%q", previousID, id)
		}
		if _, err := time.Parse("20060102T150405.000000000Z", id); err != nil {
			t.Fatalf("batch ID is not a nanosecond UTC timestamp: %q: %v", id, err)
		}
		parent, err := filepath.EvalSymlinks(filepath.Join(f.directory, "images"))
		if err != nil {
			t.Fatal(err)
		}
		wantDirectory := filepath.Join(parent, id)
		for i, r := range results {
			if r.Status != "built" || r.SaveStatus != "saved" || metadata[i].BatchID != id || metadata[i].ArchiveDirectory != wantDirectory {
				t.Fatalf("result or shared batch metadata=%s", output)
			}
			wantName := []string{"a_v1.tar", "b_v1.tar"}[i]
			if r.Archive != filepath.Join(wantDirectory, wantName) {
				t.Fatalf("archive=%q want=%q", r.Archive, filepath.Join(wantDirectory, wantName))
			}
			if data, err := os.ReadFile(r.Archive); err != nil || len(data) == 0 {
				t.Fatalf("archive=%q content=%q error=%v", r.Archive, data, err)
			}
		}
		entries, err := os.ReadDir(wantDirectory)
		if err != nil || len(entries) != 2 || entries[0].Name() != "a_v1.tar" || entries[1].Name() != "b_v1.tar" {
			t.Fatalf("batch directory entries=%v error=%v", entries, err)
		}
		report := readReportHTML(t, reportPath)
		for _, expected := range []string{id, wantDirectory, "a_v1.tar", "b_v1.tar"} {
			if !strings.Contains(report, expected) {
				t.Errorf("report missing archive batch detail %q", expected)
			}
		}
		previousID = id
	}
	entries, err := os.ReadDir(filepath.Join(f.directory, "images"))
	if err != nil || len(entries) != 2 || !entries[0].IsDir() || !entries[1].IsDir() {
		t.Fatalf("repeated runs did not create two dedicated directories: entries=%v error=%v", entries, err)
	}
}

func TestSaveBatchFilenameCollisionFailsBeforeSecondBuildWithoutOverwrite(t *testing.T) {
	f := setupSaveCLI(t)
	output, _, err := executeTest([]string{
		"build", "batch", "--tag", "v1", "--save", "--save-dir", "release images", "--concurrency", "1", "--format", "json", "--no-report",
		"--image", "group/a=registry.example.com/alpha/shared:v1",
		"--image", "group/b=registry.example.com/beta/shared:v1",
	}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	results := decodeSaveResults(t, output)
	metadata := decodeArchiveBatchMetadata(t, output)
	if len(results) != 2 || len(metadata) != 2 || results[0].Status != "built" || results[0].SaveStatus != "saved" || results[1].Status != "failed" || results[1].BuildStatus == "built" || results[1].Archive != "" {
		t.Fatalf("collision results=%s", output)
	}
	if !strings.Contains(strings.ToLower(results[1].Error), "distinct") {
		t.Fatalf("collision error does not explain how to resolve it: %q", results[1].Error)
	}
	if metadata[0].BatchID == "" || metadata[1] != metadata[0] {
		t.Fatalf("collision result lacks shared batch metadata: %s", output)
	}
	if filepath.Base(results[0].Archive) != "shared_v1.tar" || filepath.Dir(results[0].Archive) != metadata[0].ArchiveDirectory {
		t.Fatalf("archive does not use expected filename and batch directory: %s", output)
	}
	commands, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "build ") != 1 || strings.Count(string(commands), "image save --output ") != 1 {
		t.Fatalf("archive collision was not rejected before Docker build: %q", commands)
	}
	entries, err := os.ReadDir(metadata[0].ArchiveDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "shared_v1.tar" {
		t.Fatalf("collision left unexpected archive files: entries=%v error=%v", entries, err)
	}
	if data, err := os.ReadFile(results[0].Archive); err != nil || len(data) == 0 {
		t.Fatalf("successful archive was not preserved: content=%q error=%v", data, err)
	}
}
