package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedProgress struct {
	mu     sync.Mutex
	data   bytes.Buffer
	starts chan struct{}
}

func (w *observedProgress) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(p)
	if strings.Contains(string(p), "] START ") {
		w.starts <- struct{}{}
	}
	return n, err
}
func (w *observedProgress) snapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func TestProgressShowsOverlappingJobsBeforeCompletion(t *testing.T) {
	logs := &observedProgress{starts: make(chan struct{}, 3)}
	var out bytes.Buffer
	a := &app{options: Options{Out: &out, Err: logs}, format: "json"}
	release := make(chan struct{})
	jobs := make([]job, 3)
	for i := range jobs {
		i := i
		jobs[i] = func(ctx context.Context) result {
			r := result{Project: fmt.Sprintf("group/service-%d", i), Operation: "tag", Tag: "v1", Status: "created"}
			progress(ctx, r, "resolving source commit")
			select {
			case <-release:
			case <-ctx.Done():
				r.Status = "failed"
				r.Error = ctx.Err().Error()
			}
			return r
		}
	}
	done := make(chan error, 1)
	go func() { done <- a.execute(context.Background(), jobs, 2, 5*time.Second) }()
	for i := 0; i < 2; i++ {
		select {
		case <-logs.starts:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("parallel job did not start")
		}
	}
	snapshot := logs.snapshot()
	if !strings.Contains(snapshot, "active=2") || strings.Contains(snapshot, "DONE") {
		close(release)
		<-done
		t.Fatalf("progress did not show overlap: %s", snapshot)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.snapshot(), "active=0 completed=3/3") {
		t.Fatalf("final counters: %s", logs.snapshot())
	}
	var results []result
	if err := json.Unmarshal(out.Bytes(), &results); err != nil || len(results) != 3 {
		t.Fatalf("JSON results invalid: %s, %v", out.String(), err)
	}
}

func TestProgressCanBeDisabledAndReportsFailures(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		var out, logs bytes.Buffer
		a := &app{options: Options{Out: &out, Err: &logs}, format: "json", noProgress: disabled}
		j := func(ctx context.Context) result {
			r := result{Project: "group/a", Operation: "build", Image: "a:1", Status: "failed", Error: "build failed"}
			progress(ctx, r, "running Docker build")
			return r
		}
		err := a.execute(context.Background(), []job{j}, 1, time.Second)
		if ExitCode(err) != 1 {
			t.Fatalf("error=%v", err)
		}
		if disabled && logs.Len() != 0 {
			t.Fatalf("disabled progress: %s", logs.String())
		}
		if !disabled && (!strings.Contains(logs.String(), "START") || !strings.Contains(logs.String(), "DONE failed") || !strings.Contains(logs.String(), "completed=1/1")) {
			t.Fatalf("missing progress: %s", logs.String())
		}
	}
}
