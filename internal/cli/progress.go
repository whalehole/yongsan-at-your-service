package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type progressKey struct{}
type progressFunc func(result, string)

func progress(ctx context.Context, r result, phase string) {
	if report, ok := ctx.Value(progressKey{}).(progressFunc); ok {
		report(r, phase)
	}
}

// The shared log mutex serializes events with Docker logs as well as protecting
// counters. Multiple START events before a DONE event show actual overlapping jobs.
func (a *app) progressJobs(jobs []job, concurrency int) ([]job, func() error) {
	if a.noProgress {
		return jobs, func() error { return nil }
	}
	active, completed := 0, 0
	var outputErr error
	emit := func(id int, r result, event, detail string) {
		if outputErr != nil {
			return
		}
		target := r.Tag
		if r.Image != "" {
			target = r.Image
		}
		// Keep each event to one line even when external errors contain newlines.
		clean := func(s string) string {
			return strings.Map(func(ch rune) rune {
				if ch < 32 || ch == 127 {
					return ' '
				}
				return ch
			}, s)
		}
		_, outputErr = fmt.Fprintf(a.options.Err, "[%s] job %d/%d [%s %s -> %s] %s %s | active=%d completed=%d/%d\n",
			time.Now().Format("15:04:05.000"), id+1, len(jobs), clean(r.Operation), clean(r.Project), clean(target), event, clean(detail), active, completed, len(jobs))
	}
	a.logMu.Lock()
	_, outputErr = fmt.Fprintf(a.options.Err, "Running %d repository job(s), concurrency=%d (maximum simultaneous jobs).\n", len(jobs), concurrency)
	a.logMu.Unlock()
	wrapped := make([]job, len(jobs))
	for i, original := range jobs {
		i, original := i, original
		wrapped[i] = func(ctx context.Context) result {
			started := false
			begin := time.Now()
			report := progressFunc(func(r result, phase string) {
				a.logMu.Lock()
				defer a.logMu.Unlock()
				event := "STEP"
				if !started {
					started = true
					active++
					event = "START"
				}
				emit(i, r, event, phase)
			})
			r := original(context.WithValue(ctx, progressKey{}, report))
			a.logMu.Lock()
			if started {
				active--
			}
			completed++
			detail := r.Status + " (" + time.Since(begin).Round(time.Millisecond).String() + ")"
			if r.Error != "" {
				detail += " " + r.Error
			}
			emit(i, r, "DONE", detail)
			a.logMu.Unlock()
			return r
		}
	}
	return wrapped, func() error { a.logMu.Lock(); defer a.logMu.Unlock(); return outputErr }
}
