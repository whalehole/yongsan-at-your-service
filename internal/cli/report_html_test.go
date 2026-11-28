package cli

import (
	"bytes"
	"errors"
	stdhtml "html"
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
		">PASS</span>", ">FAIL</span>", ">SKIPPED</span>", ">PLANNED</span>", testSHA, "registry.example.com/first:v1.2.3", "build failed: executable missing\nexit status 1", "already points to the requested commit in repository", "Docker was not executed (dry run).",
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
		for _, text := range []string{"Batch tag deletion", "DRY RUN", "planned operations were not executed", "<strong>0</strong><span>Passed</span>", "nothing remained to delete"} {
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
		{Project: "group/save-failed", Operation: "build", Image: "save-failed:v1", Status: "failed", BuildStatus: "built", SaveStatus: "failed", Message: "Build completed before export.", Error: "image export failed", SaveError: "docker save failed: disk full\nCould not write archive.", Duration: "1s"},
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
		"/releases/saved-v1.tar", "image export failed",
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
	if strings.Contains(failedRow, "Build completed before export.") {
		t.Error("outcome message hid the save error")
	}
}

func TestBatchHTMLReportPlannedAndUnattemptedSaves(t *testing.T) {
	report := exampleBatchReport()
	report.DryRun = true
	report.Results = []result{
		{Project: "group/planned", Operation: "build", Status: "planned", BuildStatus: "planned", SaveStatus: "planned"},
		{Project: "group/build-failed", Operation: "build", Status: "failed", BuildStatus: "failed", SaveStatus: "not-run", Error: "Docker build failed: exit status 1"},
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
		"Image archive was not written (dry run).", "Save was not attempted because the build workflow stopped before export.", "Docker build failed: exit status 1", "No image archives were written.",
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
			{Project: "group/build-failed", Operation: "build", Status: "failed", BuildStatus: "not-run", SaveStatus: "not-run", Error: "source tag not found"},
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

func TestBatchHTMLReportUsesEscapedMessageAndPreservesFailure(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{
		{Project: "group/missing", Operation: "tag-delete", Tag: "v1", Status: "skipped", Message: `Tag "v1" <already absent> in repository "group/missing"; no deletion needed & no change made.`},
		{Project: "group/denied", Operation: "tag-delete", Tag: "v1", Status: "failed", Message: "Optimistic message must not hide the error.", Error: `Access denied to repository "group/denied"; tag existence could not be checked.`},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	visible := stdhtml.UnescapeString(html)
	for _, text := range []string{report.Results[0].Message, report.Results[1].Error} {
		if !strings.Contains(visible, text) {
			t.Errorf("missing detail %q", text)
		}
	}
	if strings.Contains(html, "<already absent>") || !strings.Contains(html, "&lt;already absent&gt;") || !strings.Contains(html, "&amp; no change made") {
		t.Error("explicit outcome message was not escaped")
	}
	if strings.Contains(html, report.Results[1].Message) {
		t.Error("outcome message hid an operation error")
	}
}

func TestBatchHTMLReportSkippedAndPlannedExplanations(t *testing.T) {
	report := exampleBatchReport()
	report.DryRun = true
	report.Results = []result{
		{Project: "group/missing", Operation: "tag-delete", Tag: "release-v1", Status: "skipped"},
		{Project: "group/existing", Operation: "tag-delete", Tag: "release-v1", Status: "planned"},
		{Project: "group/new-tag", Operation: "tag", Tag: "release-v2", Status: "planned"},
		{Project: "group/image", Operation: "build", Image: "service:v2", Status: "planned"},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	visible := stdhtml.UnescapeString(html)
	for _, text := range []string{
		`Tag "release-v1" was not found in repository "group/missing"; nothing remained to delete.`,
		`Tag "release-v1" exists in repository "group/existing"; no deletion was performed (dry run).`,
		`Tag "release-v2" would be created in repository "group/new-tag"; no tag was created (dry run).`,
		`Docker image "service:v2" would be built from repository "group/image"; Docker was not executed (dry run).`,
		"<strong>0</strong><span>Failed</span>", "<strong>1</strong><span>Skipped</span>", "<strong>3</strong><span>Planned</span>",
	} {
		if !strings.Contains(visible, text) {
			t.Errorf("missing precise outcome %q", text)
		}
	}
	if strings.Contains(html, ">FAIL</span>") || strings.Contains(html, ">PASS</span>") {
		t.Error("skipped or planned operation was labeled as failed or executed")
	}
}

func TestBatchHTMLReportSourceLookupFailureDidNotRunBuild(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{{
		Project: "group/missing-ref", Operation: "build", Reference: "release-v1", Status: "failed", BuildStatus: "not-run", SaveStatus: "not-run",
		Error: `Tag "release-v1" was not found in repository "group/missing-ref"; Docker build was not started.`,
	}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if !strings.Contains(html, `<span class="stage-label">Build</span> <span class="badge skipped">NOT RUN</span>`) {
		t.Error("report did not distinguish source lookup failure from a build failure")
	}
	if !strings.Contains(stdhtml.UnescapeString(html), report.Results[0].Error) {
		t.Error("precise source lookup error is missing")
	}
	if strings.Contains(html, `<span class="stage-label">Build</span> <span class="badge fail">FAIL</span>`) {
		t.Error("report incorrectly states the unattempted build failed")
	}
}

func TestBatchHTMLReportPreservesFullSaveFailureContext(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{{
		Project: "group/service", Operation: "build", Image: "service:v1", Status: "failed", BuildStatus: "built", SaveStatus: "failed",
		Message:   "Image built successfully.",
		Error:     `Docker save failed for image "service:v1": disk full. The built image remains in Docker; no new archive was created.`,
		SaveError: "disk full",
	}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if !strings.Contains(stdhtml.UnescapeString(html), report.Results[0].Error) {
		t.Error("report discarded the full save failure context")
	}
	if strings.Contains(html, report.Results[0].Message) {
		t.Error("successful build message hid the save failure")
	}
	if !strings.Contains(html, `<span class="stage-label">Build</span> <span class="badge pass">PASS</span>`) || !strings.Contains(html, `<span class="stage-label">Docker save</span> <span class="badge fail">FAIL</span>`) {
		t.Error("report lost separate build and save outcomes")
	}
}

func TestBatchHTMLReportShowsCommitTitleBesideFullHash(t *testing.T) {
	report := exampleBatchReport()
	report.Results = []result{{Project: "group/service", Operation: "build", Reference: "release-v1", Commit: testSHA, CommitTitle: "fix: retain the original payment status", Status: "built"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	want := `<code>release-v1</code><span class="sub">Commit</span><span class="commit-title">fix: retain the original payment status</span><code>` + testSHA + `</code>`
	if !strings.Contains(html, want) {
		t.Fatal("source reference, readable commit title, and full hash are not shown together")
	}
}

func TestBatchHTMLReportEscapesCommitTitlesWithoutTruncatingLongTitles(t *testing.T) {
	for _, title := range []string{`fix: <script>alert("title")</script> & preserve status`, strings.Repeat("PaymentReconciliation", 40) + " retains the original result"} {
		report := exampleBatchReport()
		report.Results = []result{{Project: "group/service", Operation: "build", Commit: testSHA, CommitTitle: title, Status: "built"}}
		var out bytes.Buffer
		if err := renderBatchReport(&out, report); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		if !strings.Contains(stdhtml.UnescapeString(html), title) || !strings.Contains(html, "<code>"+testSHA+"</code>") {
			t.Error("commit title or full hash was altered or truncated")
		}
		if strings.Contains(html, "<script>") || strings.Contains(title, "<script>") && !strings.Contains(html, "&lt;script&gt;") {
			t.Error("commit title became executable markup")
		}
	}
}

func TestBatchHTMLReportUnavailableCommitTitleRequiresResolvedHash(t *testing.T) {
	for _, title := range []string{"", " \t\n "} {
		report := exampleBatchReport()
		report.Results = []result{{Project: "group/service", Operation: "build", Commit: testSHA, CommitTitle: title, Status: "built"}}
		var out bytes.Buffer
		if err := renderBatchReport(&out, report); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `<span class="commit-title">Commit title unavailable</span><code>`+testSHA+`</code>`) {
			t.Errorf("blank title %q did not produce an explicit unavailable label", title)
		}
		if report.Results[0].CommitTitle != title {
			t.Error("rendering fabricated a title in the operation result")
		}
	}
	report := exampleBatchReport()
	report.Results = []result{{Project: "group/unresolved", Operation: "build", Status: "failed"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `<span class="commit-title">`) || !strings.Contains(out.String(), `<td><span class="muted">—</span></td>`) {
		t.Error("unresolved commit was given a title or lost its empty-source marker")
	}
}

func TestBatchHTMLReportLabelsLastObservedTagCommit(t *testing.T) {
	report := exampleBatchReport()
	report.Operation = "tag-delete"
	report.Results = []result{{Project: "group/service", Operation: "tag-delete", Tag: "release-v1", Commit: testSHA, CommitTitle: "fix: preserve the original tag target", Status: "failed", Error: "The tag target changed during deletion."}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `<span class="sub">Last observed tag commit</span><span class="commit-title">fix: preserve the original tag target</span><code>`+testSHA+`</code>`) {
		t.Error("deletion report implies the observed tag commit is a fresh resolution")
	}
}

func TestBatchHTMLReportImageDeletionShowsImageIdentityAndAllOutcomes(t *testing.T) {
	report := exampleBatchReport()
	report.Operation, report.Server, report.Selection = "image-delete", "Current Docker context/environment", `tag "1.0.9" in repository prefix "registry.example.com/team"`
	imageID := "sha256:" + strings.Repeat("a", 64)
	report.Results = []result{
		{Project: "registry.example.com/team/service", Operation: "image-delete", Image: "registry.example.com/team/service:1.0.9", Tag: "1.0.9", ImageID: imageID, Status: "deleted", Commit: "git-only-sha", CommitTitle: "Git-only title must not appear", Reference: "git-only-reference"},
		{Project: "registry.example.com/team/old", Operation: "image-delete", Image: "registry.example.com/team/old:1.0.9", ImageID: imageID, Status: "skipped"},
		{Project: "registry.example.com/team/planned", Operation: "image-delete", Image: "registry.example.com/team/planned:1.0.9", ImageID: imageID, Status: "planned"},
		{Project: "registry.example.com/team/busy", Operation: "image-delete", Image: "registry.example.com/team/busy:1.0.9", ImageID: imageID, Status: "failed", Error: "Docker refused to remove the reference because it is in use."},
	}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	visible := stdhtml.UnescapeString(html)
	for _, text := range []string{
		"Batch Docker image deletion", "<dt>Docker target</dt>", "Current Docker context/environment", "<dt>Selection</dt>", report.Selection,
		">Image repository</th>", ">Image ID</th>", "<code>" + imageID + "</code>", "registry.example.com/team/service:1.0.9", ">Docker image</span>",
		"<strong>4</strong><span>Total</span>", "<strong>1</strong><span>Passed</span>", "<strong>1</strong><span>Failed</span>", "<strong>1</strong><span>Skipped</span>", "<strong>1</strong><span>Planned</span>",
		">PASS</span>", ">FAIL</span>", ">SKIPPED</span>", ">PLANNED</span>",
		`Docker image reference "registry.example.com/team/service:1.0.9" was removed`,
		`Docker image reference "registry.example.com/team/old:1.0.9" is already absent`,
		`Docker image reference "registry.example.com/team/planned:1.0.9" would be removed`,
		report.Results[3].Error, "underlying image data", "Removal never uses force.", "Results follow the selected Docker image list.",
	} {
		if !strings.Contains(visible, text) {
			t.Errorf("missing image deletion report content %q", text)
		}
	}
	for _, unwanted := range []string{"<dt>GitLab server</dt>", ">Source / commit</th>", "<span class=\"commit-title\">", "Commit title unavailable", "git-only-sha", "Git-only title must not appear", "git-only-reference", "Git tag deleted.", "selected configuration order"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("image report contains GitLab-specific or incorrect content %q", unwanted)
		}
	}
}

func TestBatchHTMLReportImageDeletionDryRunAndNoMatches(t *testing.T) {
	report := exampleBatchReport()
	report.Operation, report.DryRun = "image-delete", true
	report.Results = []result{{Project: "team/service", Operation: "image-delete", Image: "team/service:v1", Status: "planned"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{"DRY RUN", "No image references were removed.", "no image reference was removed (dry run).", "<strong>0</strong><span>Passed</span>", "<strong>1</strong><span>Planned</span>"} {
		if !strings.Contains(html, text) {
			t.Errorf("missing image dry-run explanation %q", text)
		}
	}
	if strings.Contains(html, "source and tag checks") || strings.Contains(html, "Commit title unavailable") {
		t.Error("image dry run contains GitLab or commit placeholders")
	}
	report.Results = nil
	out.Reset()
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No matching Docker image references were found.") || strings.Contains(out.String(), "No repository jobs were selected.") {
		t.Error("zero matching image references have the wrong empty-state explanation")
	}
}

func TestBatchHTMLReportEscapesImageDeletionMetadata(t *testing.T) {
	report := exampleBatchReport()
	report.Operation = "image-delete"
	report.Server = `Docker <target> & environment`
	report.Selection = `<script>FILTER</script> & "quoted"`
	report.Results = []result{{Project: `<script>REPOSITORY</script>`, Operation: "image-delete", Image: `<svg onload="IMAGE">:v1`, ImageID: `<img src=x onerror="ID">`, Status: "failed", Error: `<script>ERROR</script>`}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, raw := range []string{"<script>", "<svg onload=", "<img src=x", "Docker <target>"} {
		if strings.Contains(html, raw) {
			t.Errorf("unescaped image report value %q", raw)
		}
	}
	for _, escaped := range []string{"Docker &lt;target&gt; &amp; environment", "&lt;script&gt;FILTER", "&lt;script&gt;REPOSITORY", "&lt;svg onload=", "&lt;img src=x", "&lt;script&gt;ERROR"} {
		if !strings.Contains(html, escaped) {
			t.Errorf("missing escaped image report value %q", escaped)
		}
	}
}

func TestBatchHTMLReportKeepsGitLabColumnsForExistingOperations(t *testing.T) {
	report := exampleBatchReport()
	report.Operation = "tag"
	report.Results = []result{{Project: "group/service", Operation: "tag", Reference: "main", Commit: testSHA, CommitTitle: "fix: keep the existing report layout", Tag: "v1", Status: "created"}}
	var out bytes.Buffer
	if err := renderBatchReport(&out, report); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, text := range []string{"Batch tag creation", "<dt>GitLab server</dt>", ">Repository</th>", ">Source / commit</th>", "fix: keep the existing report layout", testSHA, "Git tag created.", "Results follow the selected configuration order."} {
		if !strings.Contains(html, text) {
			t.Errorf("existing GitLab report lost %q", text)
		}
	}
	if strings.Contains(html, "<dt>Docker target</dt>") || strings.Contains(html, ">Image ID</th>") || strings.Contains(html, "<dt>Selection</dt>") {
		t.Error("existing GitLab report acquired image-deletion-only fields")
	}
}
