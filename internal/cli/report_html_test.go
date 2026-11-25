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

func TestBatchHTMLReportBuildAndSaveOutcomes(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{
		{Project: "group/saved", Operation: "build", Image: "saved:v1", Status: "built", BuildStatus: "built", SaveStatus: "saved", Archive: "/releases/saved-v1.tar", Duration: "1.5s"},
		{Project: "group/save-failed", Operation: "build", Image: "save-failed:v1", Status: "failed", BuildStatus: "built", SaveStatus: "failed", Error: "image export failed", SaveError: "docker save failed: disk full\nCould not write archive.", Duration: "1s"},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{
		"Completed with failures", "<strong>1</strong><span>Passed</span>", "<strong>1</strong><span>Failed</span>",
		`aria-label="Docker save totals"`, "<span>1 saved</span>", "<span>1 failed</span>", "<span>0 planned</span>", "<span>0 not run</span>",
		`<span class="stage-label">Docker save</span> <span class="badge pass">SAVED</span>`,
		`<span class="stage-label">Docker save</span> <span class="badge fail">FAIL</span>`,
		"/releases/saved-v1.tar", "docker save failed: disk full\nCould not write archive.",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("missing %q", text)
		}
	}
	failedRow := html[strings.Index(html, "group/save-failed"):]
	failedRow = failedRow[:strings.Index(failedRow, "</tr>")]
	if !strings.Contains(failedRow, `<span class="stage-label">Build</span> <span class="badge pass">PASS</span>`) {
		t.Error("successful build was not reported separately from failed save")
	}
	if strings.Contains(failedRow, "Image archive</span>") {
		t.Error("failed save was presented as a completed image archive")
	}
}

func TestBatchHTMLReportPlannedAndUnattemptedSaves(t *testing.T) {
	report := exampleBatchReport()
	report.DryRun = true
	report.Results = []result{
		{Project: "group/planned", Operation: "build", Status: "planned", BuildStatus: "planned", SaveStatus: "planned"},
		{Project: "group/build-failed", Operation: "build", Status: "failed", BuildStatus: "failed", SaveStatus: "not-run", Error: "source tag not found"},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{
		"<strong>0</strong><span>Passed</span>", "<span>0 saved</span>", "<span>0 failed</span>", "<span>1 planned</span>", "<span>1 not run</span>",
		`<span class="stage-label">Build</span> <span class="badge planned">PLANNED</span>`,
		`<span class="stage-label">Build</span> <span class="badge fail">FAIL</span>`,
		`<span class="stage-label">Docker save</span> <span class="badge skipped">NOT RUN</span>`,
		"Image archive was not written (dry run).", "Save was not attempted because the build workflow stopped before export.", "source tag not found", "No image archives were written.",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("missing %q", text)
		}
	}
	if strings.Contains(html, ">SAVED</span>") || strings.Contains(html, ">PASS</span>") || strings.Contains(html, "Image archive</span>") {
		t.Error("planned or unattempted saves were presented as completed")
	}
}

func TestBatchHTMLReportSaveNotRunAfterSuccessfulBuild(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{{Project: "group/built", Operation: "build", Status: "failed", BuildStatus: "built", SaveStatus: "not-run", Error: "could not write terminal logs"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{
		`<span class="stage-label">Build</span> <span class="badge pass">PASS</span>`,
		`<span class="stage-label">Docker save</span> <span class="badge skipped">NOT RUN</span>`,
		"<span>0 failed</span>", "<span>1 not run</span>", "could not write terminal logs", "Save was not attempted because the build workflow stopped before export.",
	} {
		if !strings.Contains(html, text) {
			t.Errorf("missing %q", text)
		}
	}
}

func TestBatchHTMLReportEscapesSaveDetails(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{
		{Project: "group/saved", Operation: "build", Status: "built", BuildStatus: "built", SaveStatus: "saved", Archive: `/releases/<script>alert("path")</script>.tar`},
		{Project: "group/save-failed", Operation: "build", Status: "failed", BuildStatus: "built", SaveStatus: "failed", SaveError: `<img src=x onerror="alert(1)"> & export failed`},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if strings.Contains(html, "<script>") || strings.Contains(html, "<img src=x") {
		t.Error("save details contain unescaped markup")
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;/script&gt;.tar", "&lt;img src=x", "&amp; export failed"} {
		if !strings.Contains(html, escaped) {
			t.Errorf("missing escaped text %q", escaped)
		}
	}
}

func TestBatchHTMLReportWithoutSaveHasNoSaveSummary(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{{Project: "group/built", Operation: "build", Status: "built", BuildStatus: "built"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if strings.Contains(html, `aria-label="Docker save totals"`) || strings.Contains(html, `<span class="stage-label">Docker save</span>`) {
		t.Error("report implies an image save was requested")
	}
	if !strings.Contains(html, `<span class="stage-label">Build</span> <span class="badge pass">PASS</span>`) {
		t.Error("build outcome is missing")
	}
}

func TestBatchHTMLReportArchiveRunMetadata(t *testing.T) {
	for _, withMetadata := range []bool{true, false} {
		report := exampleBatchReport()
		report.Results = []result{
			{Project: "group/build-failed", Operation: "build", Status: "failed", BuildStatus: "failed", SaveStatus: "not-run", Error: "source tag not found"},
			{Project: "group/saved", Operation: "build", Status: "built", BuildStatus: "built", SaveStatus: "saved", Archive: "/images/run/saved_v1.tar"},
		}
		if withMetadata {
			report.Results[1].BatchID = "20261125T093000.123456789Z"
			report.Results[1].ArchiveDirectory = "/images/<archive>&exports/20261125T093000.123456789Z"
		}
		var out bytes.Buffer
		if err := renderBatchReport(&out, report); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		if !withMetadata {
			if strings.Contains(html, "<dt>Batch ID</dt>") || strings.Contains(html, "<dt>Archive directory</dt>") {
				t.Error("report shows unavailable archive run metadata")
			}
			continue
		}
		for _, text := range []string{
			"<dt>Batch ID</dt><dd><code>20261125T093000.123456789Z</code></dd>",
			"<dt>Archive directory</dt><dd><code>/images/&lt;archive&gt;&amp;exports/20261125T093000.123456789Z</code></dd>",
			"group/build-failed", "group/saved", "/images/run/saved_v1.tar", "source tag not found",
		} {
			if !strings.Contains(html, text) {
				t.Errorf("missing %q", text)
			}
		}
		if strings.Contains(html, "/images/<archive>") {
			t.Error("archive directory contains unescaped markup")
		}
	}
}
