package cli

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeleteBatchExplainsConfirmedAbsenceAndDifferentAccessFailures(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			workingDirectory(t)
			const tag = "release/v2"
			projects := []string{"group/missing", "group/verification-denied", "group/lookup-denied", "group/delete-denied"}
			deletions := make(map[string]*atomic.Int32, len(projects))
			for _, project := range projects {
				deletions[project] = &atomic.Int32{}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				project := ""
				for _, candidate := range projects {
					if strings.Contains(r.URL.Path, "/projects/"+candidate) {
						project = candidate
						break
					}
				}
				if project == "" {
					t.Errorf("unexpected project request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Method == http.MethodDelete {
					deletions[project].Add(1)
					if project != "group/delete-denied" {
						t.Errorf("deletion attempted without a confirmed tag: %s", project)
					}
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if strings.Contains(r.URL.Path, "/repository/tags/") {
					switch project {
					case "group/missing", "group/verification-denied":
						w.WriteHeader(http.StatusNotFound)
					case "group/lookup-denied":
						w.WriteHeader(http.StatusForbidden)
					case "group/delete-denied":
						fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
					}
					return
				}
				switch project {
				case "group/missing":
					fmt.Fprint(w, `{"id":42}`)
				case "group/verification-denied":
					w.WriteHeader(http.StatusForbidden)
				default:
					t.Errorf("unexpected repository verification for %s", project)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			var config strings.Builder
			config.WriteString("tagging:\n  repositories:\n")
			for _, project := range projects {
				fmt.Fprintf(&config, "    - project: %s\n", project)
			}
			writeConfig(t, server.URL, config.String())
			output, _, err := executeTest([]string{"tag", "delete", "batch", "--tag", tag, "--format", format, "--concurrency", "2", "--no-progress", "--report", "errors.html"}, "", false)
			if ExitCode(err) != 1 {
				t.Fatalf("error=%v", err)
			}
			for _, project := range projects {
				want := int32(0)
				if project == "group/delete-denied" {
					want = 1
				}
				if count := deletions[project].Load(); count != want {
					t.Errorf("DELETE requests for %s=%d want=%d", project, count, want)
				}
			}
			report := html.UnescapeString(readReportHTML(t, "errors.html"))
			if format == "json" {
				var results []struct {
					Project string `json:"project"`
					Tag     string `json:"tag"`
					Status  string `json:"status"`
					Message string `json:"message"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal([]byte(output), &results); err != nil {
					t.Fatalf("stdout is not JSON: %q: %v", output, err)
				}
				if len(results) != len(projects) {
					t.Fatalf("results=%s", output)
				}
				for i, r := range results {
					if r.Project != projects[i] || r.Tag != tag {
						t.Fatalf("results lost repository/tag identity or order: %s", output)
					}
					if i == 0 {
						if r.Status != "skipped" || r.Error != "" || r.Message == "" {
							t.Fatalf("confirmed missing tag result=%#v", r)
						}
						assertDeleteOutcomeExplanation(t, r.Project, tag, r.Message)
						if !strings.Contains(report, r.Message) {
							t.Errorf("HTML report lost confirmed-absence explanation %q", r.Message)
						}
					} else {
						if r.Status != "failed" || r.Error == "" {
							t.Fatalf("access failure was not reported as failed: %#v", r)
						}
						assertDeleteOutcomeExplanation(t, r.Project, tag, r.Error)
						if !strings.Contains(report, r.Error) {
							t.Errorf("HTML report lost failure explanation %q", r.Error)
						}
					}
				}
			} else {
				for _, project := range projects {
					assertDeleteOutcomeExplanation(t, project, tag, deleteReportRow(t, report, project))
				}
				for _, expected := range []string{"skipped", "failed", "cannot confirm", "not attempted", "403", tag} {
					if !strings.Contains(strings.ToLower(output), expected) {
						t.Errorf("text output missing %q: %s", expected, output)
					}
				}
			}
		})
	}
}

func assertDeleteOutcomeExplanation(t *testing.T, project, tag, explanation string) {
	t.Helper()
	lower := strings.ToLower(explanation)
	for _, identity := range []string{project, tag} {
		if !strings.Contains(explanation, identity) {
			t.Errorf("explanation lacks %q: %s", identity, explanation)
		}
	}
	switch project {
	case "group/missing":
		if !strings.Contains(lower, "absent") && !strings.Contains(lower, "does not exist") {
			t.Errorf("skipped explanation does not confirm missing tag: %s", explanation)
		}
	case "group/verification-denied":
		for _, required := range []string{"403", "cannot confirm", "not attempted"} {
			if !strings.Contains(lower, required) {
				t.Errorf("verification failure lacks %q: %s", required, explanation)
			}
		}
	case "group/lookup-denied":
		for _, required := range []string{"403", "look up", "not attempted"} {
			if !strings.Contains(lower, required) {
				t.Errorf("tag lookup failure lacks %q: %s", required, explanation)
			}
		}
	case "group/delete-denied":
		for _, required := range []string{"403", "delete", "denied"} {
			if !strings.Contains(lower, required) {
				t.Errorf("deletion failure lacks %q: %s", required, explanation)
			}
		}
		if strings.Contains(lower, "not attempted") {
			t.Errorf("denied DELETE incorrectly claims deletion was not attempted: %s", explanation)
		}
	}
}

func deleteReportRow(t *testing.T, report, project string) string {
	t.Helper()
	projectAt := strings.Index(report, project)
	if projectAt < 0 {
		t.Fatalf("report lacks repository %s", project)
	}
	start := strings.LastIndex(report[:projectAt], "<tr")
	end := strings.Index(report[projectAt:], "</tr>")
	if start < 0 || end < 0 {
		t.Fatalf("report lacks a result row for %s", project)
	}
	return report[start : projectAt+end]
}
