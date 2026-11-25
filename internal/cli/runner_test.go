package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunJobsBoundsConcurrencyAndPreservesOrder(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	jobs := make([]job, 4)
	for i := range jobs {
		i := i
		jobs[i] = func(ctx context.Context) result {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			active.Add(-1)
			return result{Project: fmt.Sprint(i), Status: "planned"}
		}
	}
	done := make(chan []result, 1)
	go func() { done <- runJobs(context.Background(), jobs, 2, time.Second) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("two workers did not start concurrently")
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("more than two jobs started before a worker was released")
	default:
	}
	close(release)
	results := <-done
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d", peak.Load())
	}
	for i, r := range results {
		if r.Project != fmt.Sprint(i) {
			t.Fatalf("results lost input ordering: %#v", results)
		}
	}
}

func TestRunJobsTimeoutDoesNotStopOtherJobs(t *testing.T) {
	jobs := []job{
		func(ctx context.Context) result {
			<-ctx.Done()
			return result{Status: "failed", Error: ctx.Err().Error()}
		},
		func(context.Context) result { return result{Status: "built"} },
	}
	results := runJobs(context.Background(), jobs, 1, 10*time.Millisecond)
	if !strings.Contains(results[0].Error, "deadline exceeded") || results[1].Status != "built" {
		t.Fatalf("results = %#v", results)
	}
}

func TestLineWriterLabelsAndSerializesConcurrentOutput(t *testing.T) {
	var output bytes.Buffer
	var sinkMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &lineWriter{sinkMu: &sinkMu, out: &output, prefix: fmt.Sprint(i)}
			for j := 0; j < 4; j++ {
				_, _ = w.Write([]byte("part"))
				_, _ = w.Write([]byte("ial\n"))
			}
			_, _ = w.Write([]byte("tail"))
			_ = w.Close()
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 40 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, "] partial") && !strings.HasSuffix(line, "] tail") {
			t.Fatalf("interleaved line: %q", line)
		}
	}
}
