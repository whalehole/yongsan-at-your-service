package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func exampleBatchReport() batchReport {
	started := time.Date(2026, 11, 25, 17, 30, 0, 0, time.FixedZone("SGT", 8*60*60))
	return batchReport{
		Operation: "build", Version: "v1.2.3", Server: "https://gitlab.example.com",
		StartedAt: started, FinishedAt: started.Add(2 * time.Second), Concurrency: 2, Timeout: time.Minute,
	}
}

func TestBatchHTMLReportMixedResults(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{
		{Project: "group/first", Operation: "build", Reference: "v1.2.3", Commit: testSHA, Image: "registry.example.com/first:v1.2.3", Status: "built", Duration: "1.5s"},
		{Project: "group/second", Operation: "build", Reference: "v1.2.3", Image: "second:1", Status: "failed", Error: "build failed: executable missing\nexit status 1", Duration: "1s"},
		{Project: "group/third", Operation: "tag", Tag: "v1.2.3", Status: "skipped", Duration: "200ms"},
		{Project: "group/fourth", Operation: "build", Status: "planned", Duration: "100ms"},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{
		"Batch Docker build", "Completed with failures", "v1.2.3", "https://gitlab.example.com", "2026-11-25T17:30:00&#43;08:00", "2026-11-25T17:30:02&#43;08:00", "<dd>2s</dd>", "<dd>1m0s</dd>",
		"<strong>4</strong><span>Total</span>", "<strong>1</strong><span>Passed</span>", "<strong>1</strong><span>Failed</span>", "<strong>1</strong><span>Skipped</span>", "<strong>1</strong><span>Planned</span>",
		">PASS</span>", ">FAIL</span>", ">SKIPPED</span>", ">PLANNED</span>", testSHA, "registry.example.com/first:v1.2.3", "build failed: executable missing\nexit status 1", "Tag already points to the requested commit", "Not executed (dry run).",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("missing %q", text)
		}
	}
	previous := -1
	for _, r := range report.Results {
		index := strings.Index(html, r.Project)
		if index <= previous {
			t.Fatalf("results do not retain input order: %s", r.Project)
		}
		previous = index
	}
}

func TestBatchHTMLReportEscapesUntrustedText(t *testing.T) {
	report := exampleBatchReport()
	report.Version = `<img src=x onerror="alert(1)">`
	report.Server = `https://gitlab.example.com/<script>alert(1)</script>`
	report.Results = []result{{
		Project: `group/<script>alert(1)</script>`, Reference: `<b>ref</b>`, Commit: `<b>sha</b>`,
		Image: `<img src=x>`, Status: "failed", Error: `<script>alert("failure")</script> & bad`,
	}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, raw := range []string{"<script>", "<img src=x", "<b>ref</b>", "<b>sha</b>"} {
		if strings.Contains(html, raw) {
			t.Errorf("unescaped text %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;img src=x&gt;", "&lt;b&gt;ref&lt;/b&gt;", "&lt;b&gt;sha&lt;/b&gt;", "&amp; bad"} {
		if !strings.Contains(html, escaped) {
			t.Errorf("missing escaped text %q", escaped)
		}
	}
}

func TestBatchHTMLReportDryRunAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		report := exampleBatchReport()
		report.Operation, report.DryRun, report.Canceled = "tag-delete", true, canceled
		report.Results = []result{
			{Project: "group/absent", Operation: "tag-delete", Tag: "v1", Status: "skipped"},
			{Project: "group/planned", Operation: "tag-delete", Tag: "v1", Status: "planned"},
		}
		if canceled {
			report.Results = append(report.Results, result{Project: "group/canceled", Status: "failed", Error: "context canceled"})
		}
		var out bytes.Buffer
		if err := renderBatchReport(&out, report); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		for _, text := range []string{"Batch tag deletion", "DRY RUN", "planned operations were not executed", "<strong>0</strong><span>Passed</span>", "Tag is already absent"} {
			if !strings.Contains(html, text) {
				t.Errorf("canceled=%v: missing %q", canceled, text)
			}
		}
		if strings.Contains(html, ">PASS</span>") {
			t.Error("planned or skipped operation was labeled PASS")
		}
		if canceled && (!strings.Contains(html, ">Canceled</span>") || !strings.Contains(html, "context canceled")) {
			t.Error("cancellation is not recorded")
		}
		if !canceled && !strings.Contains(html, "Dry-run checks complete") {
			t.Error("dry run outcome is missing")
		}
	}
}

type failedReportWriter struct{}

func (failedReportWriter) Write([]byte) (int, error) { return 0, errors.New("report disk full") }

func TestBatchHTMLReportReturnsWriteFailure(t *testing.T) {
	if err := renderBatchReport(failedReportWriter{}, exampleBatchReport()); err == nil || !strings.Contains(err.Error(), "report disk full") {
		t.Fatalf("error = %v", err)
	}
}
