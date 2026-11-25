package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveBatchDirectoryIDsHandleIdenticalStartTimesWithoutOverwriting(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "images")
	started := time.Date(2026, 11, 25, 17, 30, 0, 123456789, time.FixedZone("SGT", 8*60*60))
	id, directory, err := reserveSaveRunDirectory(parent, started)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("20060102T150405.000000000Z", id); err != nil {
		t.Fatalf("batch ID is not a UTC timestamp: %q", id)
	}
	if id != "20261125T093000.123456789Z" {
		t.Fatalf("batch ID does not represent start time in UTC: %q", id)
	}
	sentinel := filepath.Join(directory, "existing.tar")
	if err := os.WriteFile(sentinel, []byte("previous run"), 0600); err != nil {
		t.Fatal(err)
	}
	nextID, nextDirectory, err := reserveSaveRunDirectory(parent, started)
	if err != nil || nextID == id || nextDirectory == directory {
		t.Fatalf("colliding run reused existing folder: id=%q directory=%q error=%v", nextID, nextDirectory, err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "previous run" {
		t.Fatalf("previous run changed: data=%q error=%v", data, err)
	}
}
