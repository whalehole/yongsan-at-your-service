package release

import (
	"bytes"
	"errors"
	stdhtml "html"
	"regexp"
	"strings"
	"testing"
)

func htmlFixture() Document {
	change, incident, zero := int64(256), int64(128), int64(0)
	return Document{
		SchemaVersion: 1,
		Release:       Metadata{Title: "Customer release", Version: "1.0.9", Date: "2026-11-27", Summary: "Faster checkout.\nClearer payment status.", Draft: true, From: "1.0.8", To: "1.0.9"},
		Scan:          ScanMetadata{StartedAt: "2026-11-27T01:00:00Z", FinishedAt: "2026-11-27T01:01:00Z"},
		Components: []Component{
			{Project: "platform/checkout", Status: "changed", FromRef: "1.0.8", ToRef: "1.0.9", FromCommit: baselineSHA, ToCommit: targetSHA, Commits: []Commit{
				{SHA: targetSHA, Title: "feat: improve checkout", Remarks: "  Improve checkout  ", Message: "feat: improve checkout\n\nKeep original details.", Author: "Release Author", CommittedAt: "2026-11-27T09:15:00+08:00", URL: "https://gitlab.example.com/checkout/-/commit/" + targetSHA, ChangeRequest: &change, Incident: &incident},
				{SHA: strings.Repeat("c", 40), Title: "Merge branch 'release' into main"},
			}},
			{Project: "platform/worker", Status: "changed", Commits: []Commit{{SHA: strings.Repeat("d", 40), Title: "fix: retry settlement", Remarks: "Improve checkout", Author: "Other Author", ChangeRequest: &zero}}},
			{Project: "platform/catalog", Status: "unchanged", FromRef: "1.0.8", ToRef: "1.0.9", Commits: []Commit{}},
			{Project: "platform/payments", Status: "failed", Error: "The baseline tag could not be read.\nAccess was denied.", Commits: []Commit{}},
		},
		UpgradeNotes: []string{"Restart workers after deployment."}, KnownIssues: []string{"A historical payment may display an old label."},
	}
}

func renderTestHTML(t *testing.T, document Document, by GroupBy) string {
	t.Helper()
	var output bytes.Buffer
	if err := RenderHTML(&output, document, by); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func viewHTML(t *testing.T, output string, by GroupBy) string {
	t.Helper()
	start := strings.Index(output, `<section id="view-`+string(by)+`"`)
	if start < 0 {
		t.Fatalf("missing grouping view %s", by)
	}
	view := output[start:]
	if next := strings.Index(view[1:], `<section id="view-`); next >= 0 {
		view = view[:next+1]
	}
	return view
}

func TestRenderHTMLPreservesReleaseContentAndEveryGrouping(t *testing.T) {
	document := htmlFixture()
	output := renderTestHTML(t, document, "")
	visible := stdhtml.UnescapeString(output)
	for _, text := range []string{
		document.Release.Title, document.Release.Summary, document.Release.Version, document.Release.Date, "Draft · review before sharing",
		"  Improve checkout  ", "Improve checkout", "feat: improve checkout", document.Components[0].Commits[0].Message,
		"Release Author", "Other Author", "2026-11-27T09:15:00+08:00", baselineSHA, targetSHA, "1.0.8", "1.0.9",
		"Restart workers after deployment.", "A historical payment may display an old label.", "platform/catalog", "No new commits",
		"Incomplete scan · 1 repository scan failed", document.Components[3].Error, "platform/payments", "No change request", "No incident", "No remarks", "Unknown author",
	} {
		if !strings.Contains(visible, text) {
			t.Errorf("missing release content %q", text)
		}
	}
	for _, by := range []GroupBy{GroupRepository, GroupChangeRequest, GroupIncident, GroupRemarks, GroupAuthor} {
		view := viewHTML(t, output, by)
		if strings.Count(view, `<article class="commit">`) != 3 {
			t.Errorf("group %s dropped or duplicated a commit", by)
		}
		opening := view[:strings.IndexByte(view, '>')+1]
		if strings.Contains(opening, " hidden") != (by != GroupRepository) {
			t.Errorf("wrong default visibility for %s: %s", by, opening)
		}
	}
	if strings.Count(output, " selected") != 1 || !strings.Contains(output, `<option value="repository" selected>Repository</option>`) {
		t.Error("repository is not the single default grouping")
	}
	remarks := viewHTML(t, output, GroupRemarks)
	for _, heading := range []string{`class="group-value">  Improve checkout  </h3>`, `class="group-value">Improve checkout</h3>`} {
		if !strings.Contains(remarks, heading) {
			t.Errorf("edited remarks grouping was normalized: missing %s", heading)
		}
	}
	if !strings.Contains(output, `href="https://gitlab.example.com/checkout/-/commit/`+targetSHA+`"`) || !strings.Contains(output, `rel="noopener noreferrer"`) {
		t.Error("safe commit link or its navigation protection is missing")
	}
	for _, count := range []string{`class="group-count">1 commit</span>`, `class="group-count">2 commits</span>`} {
		if !strings.Contains(output, count) {
			t.Errorf("missing singular/plural count %s", count)
		}
	}
}

func TestRenderHTMLInitialGroupingAndInvalidOptions(t *testing.T) {
	for _, by := range []GroupBy{GroupRepository, GroupChangeRequest, GroupIncident, GroupRemarks, GroupAuthor} {
		output := renderTestHTML(t, htmlFixture(), by)
		view := viewHTML(t, output, by)
		if strings.Contains(view[:strings.IndexByte(view, '>')+1], " hidden") || !strings.Contains(output, `<option value="`+string(by)+`" selected>`) {
			t.Errorf("initial grouping %s was not selected and visible", by)
		}
	}
	var output bytes.Buffer
	if err := RenderHTML(&output, htmlFixture(), GroupBy("unsafe")); err == nil || output.Len() != 0 {
		t.Fatalf("invalid grouping rendered a partial document: %v (%d bytes)", err, output.Len())
	}
}

func TestRenderHTMLEscapesUserContentAndRejectsUnsafeLinks(t *testing.T) {
	document := htmlFixture()
	document.Release.Title = `<script>USER_TITLE</script>`
	document.Release.Summary = `<img src=x onerror="USER_SUMMARY">`
	document.Components[0].Project = `<svg onload="USER_REPOSITORY">`
	document.Components[0].Commits[0].Remarks = `</p><script>USER_REMARKS</script>`
	document.Components[0].Commits[0].Title = `<b>USER_COMMIT</b>`
	document.Components[0].Commits[0].Message = `<iframe src="https://unsafe.example">USER_MESSAGE</iframe>`
	document.Components[0].Commits[0].Author = `<img src=x onerror="USER_AUTHOR">`
	document.Components[0].Commits[0].URL = `javascript:alert('USER_LINK')`
	document.Components[3].Error = `<script>USER_ERROR</script>`
	document.UpgradeNotes = []string{`<img src=x onerror="USER_UPGRADE">`}
	document.KnownIssues = []string{`<script>USER_ISSUE</script>`}
	output := renderTestHTML(t, document, GroupRemarks)
	if strings.Count(output, "<script>") != 1 || strings.Contains(output, "<img src=x") || strings.Contains(output, "<iframe src=") || strings.Contains(output, "<svg onload=") || strings.Contains(output, `href="javascript:`) {
		t.Fatal("untrusted content became executable markup or an unsafe link")
	}
	for _, escaped := range []string{"&lt;script&gt;USER_TITLE", "&lt;img src=x", "&lt;svg onload=", "&lt;/p&gt;&lt;script&gt;USER_REMARKS", "&lt;b&gt;USER_COMMIT", "&lt;iframe src=", "&lt;script&gt;USER_ERROR"} {
		if !strings.Contains(output, escaped) {
			t.Errorf("escaped user content missing: %q", escaped)
		}
	}
	script := output[strings.LastIndex(output, "<script>"):strings.LastIndex(output, "</script>")]
	if strings.Contains(script, "USER_") {
		t.Error("user content was interpolated into JavaScript")
	}
	if strings.Contains(output, "#ZgotmplZ") {
		t.Error("unsafe URL reached the template instead of becoming plain commit text")
	}
	for _, value := range []string{"javascript:alert(1)", "data:text/html,unsafe", "file:///etc/passwd", "//unsafe.example/commit", "https://name:password@example.com/commit", "https://example.com/commit\nunsafe", `https:\\unsafe.example`} {
		if got := safeCommitURL(value); got != "" {
			t.Errorf("unsafe URL accepted: %q", value)
		}
	}
	for _, value := range []string{"https://gitlab.example.com/commit/abc?x=1&y=2", "http://gitlab.internal/commit/abc"} {
		if got := safeCommitURL(value); got != value {
			t.Errorf("safe URL rejected: %q", value)
		}
	}
}

func TestRenderHTMLEmptyAndIncompleteReleaseVisibility(t *testing.T) {
	for _, failed := range []bool{false, true} {
		document := Document{SchemaVersion: 1, Release: Metadata{Version: "1.0.9"}, Components: []Component{{Project: "platform/service", Status: "unchanged", Commits: []Commit{}}}}
		if failed {
			document.Components[0].Status, document.Components[0].Error = "failed", "Release commits could not be read."
		}
		output := renderTestHTML(t, document, "")
		if strings.Contains(output, `<article class="commit">`) || !strings.Contains(output, "No commit entries recorded") || !strings.Contains(output, "platform/service") {
			t.Errorf("empty release state is misleading: failed=%v", failed)
		}
		if failed {
			if !strings.Contains(output, "Incomplete scan") || !strings.Contains(output, "Release commits could not be read.") || !strings.Contains(output, "These failures are not evidence of no changes.") {
				t.Error("failed empty scan is not clearly incomplete")
			}
		} else if strings.Contains(output, "Incomplete scan") || !strings.Contains(output, ">No new commits</span>") {
			t.Error("unchanged repository is not clearly distinguished from failure")
		}
	}
	empty := Document{SchemaVersion: 1, Release: Metadata{Version: "1.0.9"}}
	view := viewHTML(t, renderTestHTML(t, empty, ""), GroupRepository)
	if !strings.Contains(view, "No repository scans were recorded in this document.") || strings.Contains(view, "release ranges contain no new commits") {
		t.Error("zero scans were presented as proof of no new commits")
	}
	failed := htmlFixture()
	failed.Components = append(failed.Components, Component{Project: "platform/other", Status: "failed"})
	if !strings.Contains(renderTestHTML(t, failed, ""), "Incomplete scan · 2 repository scans failed") {
		t.Error("multiple failed repository scans were not pluralized")
	}
}

func TestRenderHTMLMissingBucketsRemainDistinctFromLiteralRemarks(t *testing.T) {
	document := htmlFixture()
	document.Components[0].Commits[0].Remarks = "No remarks"
	output := viewHTML(t, renderTestHTML(t, document, GroupRemarks), GroupRemarks)
	if strings.Count(output, `class="group-value">No remarks</h3>`) != 2 || !strings.Contains(output, `class="group-kicker">Remarks · Not provided</p>`) {
		t.Error("literal remarks and the missing-remarks bucket were merged or lost")
	}
}

type failingHTMLWriter struct{}

func (failingHTMLWriter) Write([]byte) (int, error) { return 0, errors.New("HTML disk full") }

func TestRenderHTMLReturnsWriterErrors(t *testing.T) {
	if err := RenderHTML(failingHTMLWriter{}, htmlFixture(), ""); err == nil || !strings.Contains(err.Error(), "HTML disk full") {
		t.Fatalf("writer failure = %v", err)
	}
}

func TestRenderHTMLHasSelfContainedAccessibleControlsAndPrintStyles(t *testing.T) {
	output := renderTestHTML(t, htmlFixture(), "")
	for _, required := range []string{`<label for="group-by">`, `id="group-by" aria-controls="grouped-commits"`, `aria-live="polite"`, `@media print`, `window.addEventListener('beforeprint'`, `default-src 'none'`, `[hidden]{display:none!important}`} {
		if !strings.Contains(output, required) {
			t.Errorf("missing offline/accessibility/print support %q", required)
		}
	}
	if regexp.MustCompile(`<(?:script|link|img)[^>]+(?:src|href)\s*=`).MatchString(output) {
		t.Error("release HTML depends on an external asset")
	}
}
