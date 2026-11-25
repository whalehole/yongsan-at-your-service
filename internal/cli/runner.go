package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

type result struct {
	Project          string `json:"project"`
	Operation        string `json:"operation"`
	Reference        string `json:"reference,omitempty"`
	Commit           string `json:"commit,omitempty"`
	Tag              string `json:"tag,omitempty"`
	Image            string `json:"image,omitempty"`
	BuildStatus      string `json:"build_status,omitempty"`
	SaveStatus       string `json:"save_status,omitempty"`
	Archive          string `json:"archive,omitempty"`
	SaveError        string `json:"save_error,omitempty"`
	BatchID          string `json:"batch_id,omitempty"`
	ArchiveDirectory string `json:"archive_directory,omitempty"`
	Status           string `json:"status"`
	Error            string `json:"error,omitempty"`
	Duration         string `json:"duration"`
}

type job func(context.Context) result

// Results retain configuration order even when jobs finish in a different order.
func runJobs(ctx context.Context, jobs []job, concurrency int, timeout time.Duration) []result {
	results := make([]result, len(jobs))
	if len(jobs) == 0 {
		return results
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	indices := make(chan int, len(jobs))
	for i := range jobs {
		indices <- i
	}
	close(indices)
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				start := time.Now()
				jobCtx, cancel := context.WithTimeout(ctx, timeout)
				r := jobs[i](jobCtx)
				cancel()
				r.Duration = time.Since(start).Round(time.Millisecond).String()
				results[i] = r
			}
		}()
	}
	wg.Wait()
	return results
}

func writeResults(out io.Writer, format string, results []result) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	for _, r := range results {
		if r.BatchID != "" {
			if _, err := fmt.Fprintf(out, "Image archive batch: %s\nArchive directory: %s\n\n", r.BatchID, r.ArchiveDirectory); err != nil {
				return err
			}
			break
		}
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSTATUS\tCOMMIT\tTARGET")
	counts := map[string]int{}
	saveCounts := map[string]int{}
	completedBuilds, requestedSaves := 0, 0
	for _, r := range results {
		target := r.Tag
		if r.Image != "" {
			target = r.Image
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Project, r.Status, r.Commit, target)
		if r.SaveStatus != "" {
			requestedSaves++
			saveCounts[r.SaveStatus]++
			fmt.Fprintf(w, "  Build: %s; Docker save: %s\n", r.BuildStatus, r.SaveStatus)
			if r.Archive != "" {
				fmt.Fprintf(w, "  Archive: %s\n", r.Archive)
			}
		}
		if r.BuildStatus == "built" {
			completedBuilds++
		}
		if r.Error != "" {
			fmt.Fprintf(w, "  %s\n", r.Error)
		}
		counts[r.Status]++
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\n%d total; %d created, %d deleted, %d built, %d skipped, %d planned, %d failed.\n",
		len(results), counts["created"], counts["deleted"], counts["built"], counts["skipped"], counts["planned"], counts["failed"])
	if err == nil && requestedSaves > 0 {
		_, err = fmt.Fprintf(out, "%d image build(s) completed; Docker save: %d saved, %d failed, %d planned, %d not run.\n",
			completedBuilds, saveCounts["saved"], saveCounts["failed"], saveCounts["planned"], saveCounts["not-run"])
	}
	return err
}

// Every complete Docker log line is labeled and written under a shared lock.
type lineWriter struct {
	mu      sync.Mutex
	sinkMu  *sync.Mutex
	out     io.Writer
	prefix  string
	pending string
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(data)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		if err := w.emit(w.pending[:index]); err != nil {
			return 0, err
		}
		w.pending = w.pending[index+1:]
	}
	// Bound buffering for commands that emit very long lines or progress updates.
	if len(w.pending) > 64*1024 {
		if err := w.emit(w.pending); err != nil {
			return 0, err
		}
		w.pending = ""
	}
	return len(data), nil
}

func (w *lineWriter) emit(line string) error {
	w.sinkMu.Lock()
	defer w.sinkMu.Unlock()
	_, err := fmt.Fprintf(w.out, "[%s] %s\n", w.prefix, strings.TrimSuffix(line, "\r"))
	return err
}

func (w *lineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == "" {
		return nil
	}
	err := w.emit(w.pending)
	w.pending = ""
	return err
}
