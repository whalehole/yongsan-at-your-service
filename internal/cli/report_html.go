package cli

import (
	"fmt"
	"html/template"
	"io"
	"time"
)

// batchReport deliberately includes operation results, not configuration or logs.
type batchReport struct {
	Operation, Version, Server string
	StartedAt, FinishedAt      time.Time
	Concurrency                int
	Timeout                    time.Duration
	DryRun, Canceled           bool
	Results                    []result
}

type reportSummary struct {
	Total, Passed, Failed, Skipped, Planned int
}

type reportSaveSummary struct {
	Total, Saved, Failed, Planned, NotRun int
}

type reportRow struct {
	result
	Number                                                            int
	Outcome, Class, Detail                                            string
	Target, TargetLabel                                               string
	BuildOutcome, BuildClass, SaveOutcome, SaveClass, SaveExplanation string
}

func renderBatchReport(out io.Writer, report batchReport) error {
	data := struct {
		batchReport
		Title, Outcome, Class, Started, Finished, Duration string
		BatchID, ArchiveDirectory                          string
		Summary                                            reportSummary
		Saves                                              reportSaveSummary
		Rows                                               []reportRow
	}{batchReport: report, Summary: reportSummary{Total: len(report.Results)}}
	data.Title = map[string]string{"tag": "Batch tag creation", "tag-delete": "Batch tag deletion", "build": "Batch Docker build"}[report.Operation]
	if data.Title == "" {
		data.Title = "Batch operation"
	}
	data.Started = report.StartedAt.Format(time.RFC3339)
	data.Finished = report.FinishedAt.Format(time.RFC3339)
	data.Duration = report.FinishedAt.Sub(report.StartedAt).Round(time.Millisecond).String()
	for i, r := range report.Results {
		if data.BatchID == "" {
			data.BatchID = r.BatchID
		}
		if data.ArchiveDirectory == "" {
			data.ArchiveDirectory = r.ArchiveDirectory
		}
		row := reportRow{result: r, Number: i + 1, Target: r.Tag, TargetLabel: "Git tag"}
		if r.Image != "" {
			row.Target, row.TargetLabel = r.Image, "Docker image"
		}
		switch r.Status {
		case "created", "deleted", "built":
			data.Summary.Passed++
			row.Outcome, row.Class = "PASS", "pass"
			row.Detail = map[string]string{"created": "Git tag created.", "deleted": "Git tag deleted.", "built": "Docker image built in the selected daemon; not pushed to a registry."}[r.Status]
		case "skipped":
			data.Summary.Skipped++
			row.Outcome, row.Class = "SKIPPED", "skipped"
			row.Detail = fmt.Sprintf("No change was needed for repository %q.", r.Project)
			if r.Operation == "tag" {
				row.Detail = fmt.Sprintf("Tag %q already points to the requested commit in repository %q; no change was needed.", r.Tag, r.Project)
			} else if r.Operation == "tag-delete" {
				row.Detail = fmt.Sprintf("Tag %q was not found in repository %q; nothing remained to delete.", r.Tag, r.Project)
			}
		case "planned":
			data.Summary.Planned++
			row.Outcome, row.Class = "PLANNED", "planned"
			switch r.Operation {
			case "tag-delete":
				row.Detail = fmt.Sprintf("Tag %q exists in repository %q; no deletion was performed (dry run).", r.Tag, r.Project)
			case "tag":
				row.Detail = fmt.Sprintf("Tag %q would be created in repository %q; no tag was created (dry run).", r.Tag, r.Project)
			case "build":
				row.Detail = fmt.Sprintf("Docker image %q would be built from repository %q; Docker was not executed (dry run).", r.Image, r.Project)
			default:
				row.Detail = fmt.Sprintf("No operation was executed for repository %q (dry run).", r.Project)
			}
		default:
			data.Summary.Failed++
			row.Outcome, row.Class, row.Detail = "FAIL", "fail", "Operation did not complete."
		}
		if r.Message != "" {
			row.Detail = r.Message
		}
		if r.Error != "" {
			row.Detail = r.Error
		}
		switch r.BuildStatus {
		case "built":
			row.BuildOutcome, row.BuildClass = "PASS", "pass"
		case "failed":
			row.BuildOutcome, row.BuildClass = "FAIL", "fail"
		case "planned":
			row.BuildOutcome, row.BuildClass = "PLANNED", "planned"
		case "not-run":
			row.BuildOutcome, row.BuildClass = "NOT RUN", "skipped"
		}
		if r.SaveStatus != "" {
			data.Saves.Total++
			switch r.SaveStatus {
			case "saved":
				data.Saves.Saved++
				row.SaveOutcome, row.SaveClass = "SAVED", "pass"
			case "failed":
				data.Saves.Failed++
				row.SaveOutcome, row.SaveClass = "FAIL", "fail"
				if r.Error == "" && r.SaveError != "" {
					row.Detail = r.SaveError
				}
			case "planned":
				data.Saves.Planned++
				row.SaveOutcome, row.SaveClass = "PLANNED", "planned"
				row.SaveExplanation = "Image archive was not written (dry run)."
			case "not-run":
				data.Saves.NotRun++
				row.SaveOutcome, row.SaveClass = "NOT RUN", "skipped"
				row.SaveExplanation = "Save was not attempted because the build workflow stopped before export."
			}
		}
		data.Rows = append(data.Rows, row)
	}
	data.Outcome, data.Class = "Completed", "pass"
	if report.DryRun {
		data.Outcome, data.Class = "Dry-run checks complete", "planned"
	}
	if data.Summary.Failed > 0 {
		data.Outcome, data.Class = "Completed with failures", "fail"
		if report.DryRun {
			data.Outcome = "Dry-run checks failed"
		}
	}
	if report.Canceled {
		data.Outcome, data.Class = "Canceled", "fail"
	}
	return batchReportTemplate.Execute(out, data)
}

var batchReportTemplate = template.Must(template.New("batch-report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
<title>{{.Title}} — YAS report</title>
<style>
:root{color-scheme:light;font-family:system-ui,-apple-system,"Segoe UI",sans-serif;color:#202a38;background:#f3f5f8;font-size:14px}
*{box-sizing:border-box}body{margin:0;padding:36px 24px}main{max-width:1440px;margin:auto;background:white;border:1px solid #dce2e9;border-radius:12px;padding:32px}
header{display:flex;justify-content:space-between;align-items:flex-start;gap:20px;margin-bottom:24px}h1{font-size:28px;letter-spacing:-.6px;margin:6px 0 0}h2{font-size:19px;margin:28px 0 14px}.eyebrow{font-size:12px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;color:#5b6879}
.badge{display:inline-block;border:1px solid;border-radius:5px;padding:5px 8px;font-size:12px;font-weight:750;line-height:1.4;white-space:nowrap}.pass{color:#17613a;background:#edf8f0;border-color:#b6ddc2}.fail{color:#a22229;background:#fff0f0;border-color:#ecc0c2}.skipped{color:#536071;background:#f0f2f5;border-color:#cdd3dc}.planned{color:#76520d;background:#fff8e5;border-color:#e9d597}
.notice{border-left:4px solid #bc881a;background:#fff9e9;padding:14px 16px;line-height:1.6;margin:0 0 22px}.summary{display:grid;grid-template-columns:repeat(5,1fr);gap:12px;margin:24px 0}.stat{border:1px solid #dce2e9;border-radius:7px;padding:14px 16px}.stat strong{display:block;font-size:29px;line-height:1.1;margin-bottom:6px}.stat span{font-size:12px;font-weight:650;text-transform:uppercase;letter-spacing:.5px}
dl{display:grid;grid-template-columns:130px minmax(0,1fr) 130px minmax(0,1fr);gap:9px 14px;font-size:13px;margin:0}dt{color:#5b6879}dd{margin:0;overflow-wrap:anywhere;font-variant-numeric:tabular-nums}
.table-wrap{overflow-x:auto}table{border-collapse:collapse;width:100%;table-layout:fixed;font-size:13px}th{text-align:left;background:#f3f5f8;color:#465467;font-size:11px;text-transform:uppercase;letter-spacing:.5px;padding:11px 10px;border-bottom:2px solid #dce2e9}td{vertical-align:top;padding:14px 10px;border-bottom:1px solid #e1e6ec;overflow-wrap:anywhere;line-height:1.5}th:nth-child(1){width:17%}th:nth-child(2){width:11%}th:nth-child(3){width:21%}th:nth-child(4){width:17%}th:nth-child(5){width:8%}th:nth-child(6){width:26%}
code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;overflow-wrap:anywhere}.muted{color:#687586}.sub{display:block;margin-top:5px;font-size:11px;color:#687586}.detail{white-space:pre-wrap}.stage{margin-bottom:7px}.stage .badge{padding:2px 6px;font-size:10px}.stage-label{display:inline-block;min-width:77px;font-weight:650}.archive{display:block;margin:5px 0 10px}.duration{font-variant-numeric:tabular-nums}.save-summary{padding:13px 16px;border:1px solid #dce2e9;border-radius:7px;line-height:1.7;margin:0 0 24px}.save-summary strong{margin-right:7px}.save-summary span{display:inline-block;margin-right:15px}footer{margin-top:22px;padding-top:16px;border-top:1px solid #e1e6ec;color:#687586;font-size:12px;line-height:1.6}
@media(max-width:760px){body{padding:12px}main{padding:18px}header{display:block}header>.badge{margin-top:16px}h1{font-size:24px}.summary{gap:6px}.stat{padding:10px 6px}.stat strong{font-size:23px}.stat span{font-size:10px}dl{grid-template-columns:105px minmax(0,1fr)}table{min-width:950px}}
@media print{@page{size:A4 landscape;margin:12mm}:root{font-size:11px;background:white}body{padding:0}main{max-width:none;border:0;border-radius:0;padding:0}h1{font-size:23px}header{margin-bottom:18px}.summary{margin:18px 0}.stat{padding:10px 12px}.stat strong{font-size:24px}.table-wrap{overflow:visible}table{font-size:10px;min-width:0}th,td{padding:9px 7px}code{font-size:9px}.badge{font-size:10px}.sub{font-size:9px}thead{display:table-header-group}tr{break-inside:avoid}footer{font-size:10px}*{-webkit-print-color-adjust:exact;print-color-adjust:exact}}
</style>
</head>
<body><main>
<header><div><div class="eyebrow">YAS · Batch report</div><h1>{{.Title}}</h1></div><span class="badge {{.Class}}">{{.Outcome}}</span></header>
{{if .DryRun}}<p class="notice"><strong>DRY RUN — planned operations were not executed.</strong><br>These results describe source and tag checks, not completed changes or successful image builds.{{if .Saves.Total}} No image archives were written.{{end}}</p>{{end}}
{{if .Canceled}}<p class="notice"><strong>The batch was canceled.</strong> Completed operations remain completed. Failed rows include any jobs interrupted or canceled before execution.</p>{{end}}
<dl>
<dt>Started</dt><dd>{{.Started}}</dd><dt>Finished</dt><dd>{{.Finished}}</dd>
<dt>Total duration</dt><dd>{{.Duration}}</dd><dt>Mode</dt><dd>{{if .DryRun}}Dry run{{else}}Execute{{end}}</dd>
<dt>GitLab server</dt><dd>{{.Server}}</dd><dt>YAS version</dt><dd>{{.Version}}</dd>
<dt>Concurrency</dt><dd>{{.Concurrency}}</dd><dt>Job timeout</dt><dd>{{.Timeout}}</dd>
{{if .BatchID}}<dt>Batch ID</dt><dd><code>{{.BatchID}}</code></dd>{{end}}{{if .ArchiveDirectory}}<dt>Archive directory</dt><dd><code>{{.ArchiveDirectory}}</code></dd>{{end}}
</dl>
<section class="summary" aria-label="Result totals">
<div class="stat"><strong>{{.Summary.Total}}</strong><span>Total</span></div>
<div class="stat pass"><strong>{{.Summary.Passed}}</strong><span>Passed</span></div>
<div class="stat fail"><strong>{{.Summary.Failed}}</strong><span>Failed</span></div>
<div class="stat skipped"><strong>{{.Summary.Skipped}}</strong><span>Skipped</span></div>
<div class="stat planned"><strong>{{.Summary.Planned}}</strong><span>Planned</span></div>
</section>
{{if .Saves.Total}}<section class="save-summary" aria-label="Docker save totals"><strong>Docker save</strong><span>{{.Saves.Saved}} saved</span><span>{{.Saves.Failed}} failed</span><span>{{.Saves.Planned}} planned</span><span>{{.Saves.NotRun}} not run</span></section>{{end}}
<h2>Repository results</h2>
<div class="table-wrap"><table>
<thead><tr><th scope="col">Repository</th><th scope="col">Result</th><th scope="col">Source / commit</th><th scope="col">Target</th><th scope="col">Duration</th><th scope="col">Details / failure reason</th></tr></thead>
<tbody>{{range .Rows}}
<tr><td><span class="muted">{{.Number}}.</span> <strong>{{.Project}}</strong></td><td><span class="badge {{.Class}}">{{.Outcome}}</span><span class="sub">{{.Status}}</span></td>
<td>{{if .Reference}}<code>{{.Reference}}</code>{{else}}<span class="muted">—</span>{{end}}{{if .Commit}}<span class="sub">Resolved commit</span><code>{{.Commit}}</code>{{end}}</td>
<td>{{if .Target}}<span class="sub">{{.TargetLabel}}</span><code>{{.Target}}</code>{{else}}<span class="muted">—</span>{{end}}</td><td class="duration">{{.Duration}}</td><td>{{if .BuildOutcome}}<div class="stage"><span class="stage-label">Build</span> <span class="badge {{.BuildClass}}">{{.BuildOutcome}}</span></div>{{end}}{{if .SaveOutcome}}<div class="stage"><span class="stage-label">Docker save</span> <span class="badge {{.SaveClass}}">{{.SaveOutcome}}</span></div>{{end}}{{if and (eq .SaveStatus "saved") .Archive}}<span class="sub">Image archive</span><code class="archive">{{.Archive}}</code>{{end}}{{if .SaveExplanation}}<p class="detail">{{.SaveExplanation}}</p>{{end}}<div class="detail">{{.Detail}}</div></td></tr>
{{else}}<tr><td colspan="6">No repository jobs were selected.</td></tr>{{end}}</tbody>
</table></div>
<footer>{{if .Saves.Total}}Overall PASS means the requested operation completed, including Docker save when requested. Build and Docker save outcomes are shown separately.{{else}}PASS means a tag was created or deleted, or an image was built.{{end}} SKIPPED means no change was needed. PLANNED means the operation was not executed.<br>Results follow the selected configuration order. Job durations exclude time waiting for a worker. Open this file in a browser to read it or print it to PDF.</footer>
</main></body></html>
`))
