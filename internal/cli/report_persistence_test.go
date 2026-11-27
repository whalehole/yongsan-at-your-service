package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

func TestCanceledBatchReportIncludesQueuedJobsAndSourceReferences(t *testing.T) {
	workingDirectory(t)
	var stdout, stderr bytes.Buffer
	a := &app{options: Options{Out: &stdout, Err: &stderr, Version: "test", ConfigPath: "yas.yaml"}, format: "json", noProgress: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := []job{
		a.tagJob(nil, config.TagRepository{Project: "group/tag", Branch: "release/1.0", Tag: "v1.0"}),
		a.buildJob(nil, config.BuildRepository{Project: "group/build-a", Source: config.Source{Tag: "v1.0"}}, imageSaveOptions{}),
		a.buildJob(nil, config.BuildRepository{Project: "group/build-b", Source: config.Source{Tag: "v1.0"}}, imageSaveOptions{}),
	}
	err := a.executeBatch(ctx, jobs, 1, time.Second, batchReportOptions{}, "build", "https://gitlab.example.com")
	if ExitCode(err) != 1 {
		t.Fatalf("expected canceled batch failure, got %v", err)
	}
	var results []result
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("missing queued results: %#v", results)
	}
	for _, r := range results {
		if r.Reference == "" || r.Status != "failed" || !strings.Contains(r.Error, "context canceled") {
			t.Fatalf("lost canceled job metadata: %#v", r)
		}
	}
	paths, err := filepath.Glob("reports/*.html")
	if err != nil || len(paths) != 1 {
		t.Fatalf("reports=%v err=%v", paths, err)
	}
	content, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Canceled", "group/tag", "group/build-a", "group/build-b", "release/1.0", "v1.0", "context canceled"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("report missing %q", want)
		}
	}
}

type unavailableReportOutput struct{}

func (unavailableReportOutput) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

func TestBatchReportSavedEvenWhenStdoutFails(t *testing.T) {
	workingDirectory(t)
	a := &app{options: Options{Out: unavailableReportOutput{}, Err: io.Discard, ConfigPath: "yas.yaml"}, noProgress: true}
	jobs := []job{func(context.Context) result {
		return result{Project: "group/service", Operation: "build", Image: "service:v1", Status: "built"}
	}}
	err := a.executeBatch(context.Background(), jobs, 1, time.Second, batchReportOptions{path: "saved.html"}, "build", "https://gitlab.example.com")
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("error=%v", err)
	}
	content, err := os.ReadFile("saved.html")
	if err != nil || !strings.Contains(string(content), "service:v1") || !strings.Contains(string(content), ">PASS<") {
		t.Fatalf("report not saved after output failure: %v", err)
	}
}

func TestBatchReportWriteFailureStillReturnsOperationResults(t *testing.T) {
	directory := workingDirectory(t)
	report, err := reserveBatchReport(directory, "broken.html", "tag")
	if err != nil {
		t.Fatal(err)
	}
	defer report.cleanup()
	// Simulate storage becoming unavailable after successful reservation.
	if err := report.file.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	a := &app{options: Options{Out: &stdout, Err: io.Discard}, format: "json", noProgress: true}
	jobs := []job{func(context.Context) result {
		return result{Project: "group/service", Operation: "tag", Tag: "v1", Status: "created"}
	}}
	err = a.executeWithReport(context.Background(), jobs, 1, time.Second, report)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "save batch report") {
		t.Fatalf("expected report write failure, got %v", err)
	}
	var results []result
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil || len(results) != 1 || results[0].Status != "created" {
		t.Fatalf("completed operation result lost: %s, %v", stdout.String(), err)
	}
}

func TestBatchReportReservationsAreUniqueAndPrivate(t *testing.T) {
	directory := workingDirectory(t)
	first, err := reserveBatchReport(directory, "", "tag")
	if err != nil {
		t.Fatal(err)
	}
	defer first.cleanup()
	second, err := reserveBatchReport(directory, "", "tag")
	if err != nil {
		t.Fatal(err)
	}
	defer second.cleanup()
	if first.path == second.path {
		t.Fatal("successive runs reserved the same report")
	}
	if runtime.GOOS != "windows" {
		info, err := first.file.Stat()
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("report is not private: info=%v err=%v", info, err)
		}
	}
}
