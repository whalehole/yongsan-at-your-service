package cli

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const repositoryListGroup = "team/sub"
const repositoryListTag = "release/1.2.9"
const repositoryListTitle = "Fix <checkout> & preserve payment status"

type repositoryListFixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []string
}

func newRepositoryListFixture(t *testing.T, mode string) *repositoryListFixture {
	t.Helper()
	fixture := &repositoryListFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, r.Method+" "+r.URL.Path)
		fixture.mu.Unlock()
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("unexpected request method or authentication: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		groupPath := "/api/v4/groups/" + repositoryListGroup
		switch r.URL.Path {
		case groupPath:
			if mode == "discovery denied" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"id":77,"path":"sub","full_path":%q}`, repositoryListGroup)
			return
		case groupPath + "/projects", "/api/v4/groups/77/projects":
			if mode == "discovery denied" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if mode == "empty" {
				fmt.Fprint(w, `[]`)
				return
			}
			if r.URL.Query().Get("include_subgroups") != "false" || r.URL.Query().Get("with_shared") != "false" {
				t.Errorf("group discovery did not exclude shared/nested repositories: %s", r.URL)
			}
			projects := []map[string]any{}
			for i, name := range []string{"z-found", "m-denied", "a-missing"} {
				projects = append(projects, map[string]any{
					"id": int64(3 - i), "path_with_namespace": repositoryListGroup + "/" + name,
					"namespace": map[string]any{"id": 77, "full_path": repositoryListGroup},
				})
			}
			_ = json.NewEncoder(w).Encode(projects)
			return
		}
		for i, name := range []string{"a-missing", "m-denied", "z-found"} {
			project := "/api/v4/projects/" + repositoryListGroup + "/" + name
			if r.URL.Path == project {
				fmt.Fprintf(w, `{"id":%d}`, i+1)
				return
			}
			if r.URL.Path == project+"/repository/tags" {
				if r.URL.Query().Get("per_page") == "100" {
					if mode == "companion denied" && name == "m-denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					tags := []map[string]any{{"name": repositoryListTag, "commit": map[string]string{"id": testSHA}}}
					if mode != "no companions" {
						for _, tag := range []string{"v1.2.9", "deployment/production"} {
							tags = append(tags, map[string]any{"name": tag, "commit": map[string]string{"id": testSHA}})
						}
						tags = append(tags, map[string]any{"name": "v1.2.8", "commit": map[string]string{"id": strings.Repeat("b", 40)}})
					}
					_ = json.NewEncoder(w).Encode(tags)
					return
				}
				fmt.Fprint(w, `[]`)
				return
			}
			if r.URL.Path == project+"/repository/tags/"+repositoryListTag {
				if mode == "all absent" || name == "a-missing" {
					w.WriteHeader(http.StatusNotFound)
				} else if name == "m-denied" && mode == "partial" {
					w.WriteHeader(http.StatusForbidden)
				} else {
					fmt.Fprintf(w, `{"commit":{"id":%q,"title":%q}}`, testSHA, repositoryListTitle)
				}
				return
			}
		}
		t.Errorf("unexpected repository discovery request: %s", r.URL)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *repositoryListFixture) requested() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.requests...)
}

func writeRepositoryListConfig(t *testing.T, fixture *repositoryListFixture) {
	t.Helper()
	// No repository list is configured: discovery must use GitLab's inventory.
	writeConfig(t, fixture.server.URL, "  groups: [other-group, team/sub, team/sub]\n")
}

func TestRepositoryListDiscoversActualGroupAndReportsMatchesAbsenceAndFailures(t *testing.T) {
	workingDirectory(t)
	fixture := newRepositoryListFixture(t, "partial")
	writeRepositoryListConfig(t, fixture)
	output, logs, err := executeTest([]string{"repo", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--concurrency", "2", "--format", "json", "--no-progress"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("partial lookup failure must return exit 1: %v (%s)", err, logs)
	}
	var results []result
	if err := json.Unmarshal([]byte(output), &results); err != nil || len(results) != 3 {
		t.Fatalf("missing group results: %s (%v)", output, err)
	}
	for i, expected := range []struct{ name, status string }{{"a-missing", "skipped"}, {"m-denied", "failed"}, {"z-found", "found"}} {
		r := results[i]
		if r.Project != repositoryListGroup+"/"+expected.name || r.Status != expected.status || r.Tag != repositoryListTag || r.Operation != "repo-list" {
			t.Fatalf("unexpected ordered result %d: %+v", i, r)
		}
	}
	if results[2].Commit != testSHA || results[2].CommitTitle != repositoryListTitle || !strings.Contains(results[0].Message, "does not exist") || !strings.Contains(results[1].Error, "403") {
		t.Fatalf("lookup metadata/explanations lost: %+v", results)
	}
	if strings.Contains(results[1].Error, "does not exist") || strings.Contains(results[1].Error, "deletion") {
		t.Fatalf("denied lookup was incorrectly described as absence or deletion: %s", results[1].Error)
	}
	paths, _ := filepath.Glob("reports/*.html")
	if len(paths) != 1 || !strings.Contains(logs, filepath.Base(paths[0])) {
		t.Fatalf("report missing: paths=%v logs=%s", paths, logs)
	}
	report := html.UnescapeString(readReportHTML(t, paths[0]))
	for _, expected := range []string{repositoryListGroup, repositoryListTag, testSHA, repositoryListTitle, "403", "<dt>Concurrency</dt><dd>2</dd>"} {
		if !strings.Contains(report, expected) {
			t.Errorf("report missing %q", expected)
		}
	}
	for _, request := range fixture.requested() {
		if strings.Contains(request, "other-group") || !strings.HasPrefix(request, "GET ") {
			t.Errorf("lookup escaped the selected group or performed a write: %s", request)
		}
	}
}

func TestRepositoryListRootFlowChoosesGroupBeforeTag(t *testing.T) {
	workingDirectory(t)
	fixture := newRepositoryListFixture(t, "normal")
	writeRepositoryListConfig(t, fixture)
	output, logs, err := executeTest([]string{"--format", "json", "--no-progress"}, "10\n2\n"+repositoryListTag+"\n", true)
	if err != nil {
		t.Fatalf("root listing failed: %v (%s)", err, logs)
	}
	if len(decodeImageDeleteResults(t, output)) != 3 {
		t.Fatalf("root listing results=%s", output)
	}
	groupPrompt, tagPrompt := strings.Index(logs, "Select a GitLab group from config"), strings.Index(logs, "Git tag to find in "+repositoryListGroup)
	if groupPrompt < 0 || tagPrompt <= groupPrompt || strings.Contains(logs, "3) team/sub") {
		t.Fatalf("group/tag flow was missing, reversed, or duplicated: %s", logs)
	}
}

func TestRepositoryListAllAbsentAndEmptyGroupSucceed(t *testing.T) {
	for _, mode := range []string{"all absent", "empty"} {
		t.Run(mode, func(t *testing.T) {
			workingDirectory(t)
			fixture := newRepositoryListFixture(t, mode)
			writeRepositoryListConfig(t, fixture)
			output, _, err := executeTest([]string{"repository", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--format", "json", "--no-progress"}, "", false)
			if err != nil {
				t.Fatal(err)
			}
			results := decodeImageDeleteResults(t, output)
			if mode == "empty" && len(results) != 0 || mode == "all absent" && len(results) != 3 {
				t.Fatalf("wrong result count: %s", output)
			}
			for _, result := range results {
				if result.Status != "skipped" || result.Error != "" {
					t.Fatalf("tag absence was treated as failure: %+v", result)
				}
			}
			paths, _ := filepath.Glob("reports/*.html")
			if len(paths) != 1 {
				t.Fatalf("no-match report missing: %v", paths)
			}
		})
	}
}

func TestRepositoryListRejectsInvalidInputsBeforeGitLab(t *testing.T) {
	for _, flags := range [][]string{
		{}, {"--tag", repositoryListTag}, {"--group", repositoryListGroup},
		{"--group", "not-configured", "--tag", repositoryListTag},
		{"--group", "", "--tag", repositoryListTag},
		{"--group", repositoryListGroup, "--tag", ""},
		{"--group", repositoryListGroup, "--tag", "bad tag"},
		{"--group", repositoryListGroup, "--tag", repositoryListTag, "--concurrency", "0"},
		{"--group", repositoryListGroup, "--tag", repositoryListTag, "--timeout", "0s"},
		{"--group", repositoryListGroup, "--tag", repositoryListTag, "--dry-run"},
		{"--group", repositoryListGroup, "--tag", repositoryListTag, "--open-report", "--no-report"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			workingDirectory(t)
			fixture := newRepositoryListFixture(t, "normal")
			writeRepositoryListConfig(t, fixture)
			args := append([]string{"repo", "list", "--non-interactive"}, flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || len(fixture.requested()) != 0 {
				t.Fatalf("invalid inputs reached GitLab: %v; requests=%v", err, fixture.requested())
			}
		})
	}
}

func TestRepositoryListDiscoveryFailureDoesNotClaimTagAbsence(t *testing.T) {
	workingDirectory(t)
	fixture := newRepositoryListFixture(t, "discovery denied")
	writeRepositoryListConfig(t, fixture)
	_, _, err := executeTest([]string{"repo", "list", "--group", repositoryListGroup, "--tag", repositoryListTag}, "", false)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("group discovery denial was inaccurate: %v", err)
	}
	if _, err := os.Stat("reports"); !os.IsNotExist(err) {
		t.Fatalf("failed inventory discovery created a report: %v", err)
	}
}

func TestRepositoryListRequiresConfiguredGroupsAndHonorsReportOverwriteProtection(t *testing.T) {
	workingDirectory(t)
	fixture := newRepositoryListFixture(t, "normal")
	writeConfig(t, fixture.server.URL, "")
	_, _, err := executeTest([]string{"repo", "list", "--group", repositoryListGroup, "--tag", repositoryListTag}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "gitlab.groups") || len(fixture.requested()) != 0 {
		t.Fatalf("unconfigured group was queried: %v; requests=%v", err, fixture.requested())
	}
	writeRepositoryListConfig(t, fixture)
	if err := os.WriteFile("existing.html", []byte("keep this report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = executeTest([]string{"repo", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--report", "existing.html"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("existing report should prevent repository lookup jobs: %v", err)
	}
	for _, request := range fixture.requested() {
		if strings.Contains(request, "/repository/tags/") {
			t.Fatalf("tag lookup started before report reservation: %s", request)
		}
	}
	if data, err := os.ReadFile("existing.html"); err != nil || string(data) != "keep this report\n" {
		t.Fatalf("existing report was overwritten: %v", err)
	}
}

func TestRepositoryListWorkspaceReportsAndOpeningFlags(t *testing.T) {
	caller := workingDirectory(t)
	launcher := setupReportAutoOpenLauncher(t, caller)
	fixture := newRepositoryListFixture(t, "normal")
	workspace := t.TempDir()
	bootstrap := writeWorkspaceLocator(t, t.TempDir(), workspace)
	configuration := fmt.Sprintf("version: 1\ngitlab:\n  url: %s\n  token: test-token\n  groups: [team/sub]\nreports: {auto_open: true}\n", fixture.server.URL)
	if err := os.WriteFile(filepath.Join(workspace, "yas.yaml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caller, "yas.yaml"), []byte("wrong: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, _, err := workspaceCommand(t, bootstrap, "repos", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--format", "json", "--no-progress", "--report", "audit/tag-list.html")
	if err != nil || len(decodeImageDeleteResults(t, output)) != 3 {
		t.Fatalf("workspace lookup failed: %v (%s)", err, output)
	}
	launcher.assert(t, true, filepath.Join(workspace, "audit", "tag-list.html"))
	if _, err := os.Stat(filepath.Join(caller, "audit")); !os.IsNotExist(err) {
		t.Fatalf("report was written in invocation directory: %v", err)
	}
	output, _, err = workspaceCommand(t, bootstrap, "repositories", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--format", "json", "--no-progress", "--no-report")
	if err != nil || len(decodeImageDeleteResults(t, output)) != 3 {
		t.Fatalf("listing without report failed: %v (%s)", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "yas.yaml")); err != nil || string(data) != configuration {
		t.Fatalf("lookup changed the config: %v", err)
	}
}

func TestRepositoryListAccompanyingTagsShareCommitAndExcludeSelectedTag(t *testing.T) {
	for _, mode := range []string{"normal", "no companions", "companion denied"} {
		t.Run(mode, func(t *testing.T) {
			workingDirectory(t)
			fixture := newRepositoryListFixture(t, mode)
			writeRepositoryListConfig(t, fixture)
			output, _, err := executeTest([]string{"repo", "list", "--group", repositoryListGroup, "--tag", repositoryListTag, "--format", "json", "--no-progress"}, "", false)
			if mode == "companion denied" && ExitCode(err) != 1 || mode != "companion denied" && err != nil {
				t.Fatalf("unexpected result for %s: %v (%s)", mode, err, output)
			}
			results := decodeImageDeleteResults(t, output)
			if len(results) != 3 {
				t.Fatalf("missing results: %s", output)
			}
			if results[0].Status != "skipped" || results[0].AccompanyingTagsStatus != "" {
				t.Fatalf("absent tag should not scan accompanying tags: %+v", results[0])
			}
			for _, result := range results[1:] {
				if result.Commit != testSHA || result.CommitTitle != repositoryListTitle || result.Tag != repositoryListTag {
					t.Fatalf("selected tag metadata was changed: %+v", result)
				}
				if mode == "companion denied" && strings.HasSuffix(result.Project, "/m-denied") {
					if result.Status != "failed" || result.AccompanyingTagsStatus != "failed" || len(result.AccompanyingTags) != 0 || !strings.Contains(result.AccompanyingTagsError, "403") || !strings.Contains(result.Error, "was found") {
						t.Fatalf("incomplete tag list lost confirmed presence or claimed completeness: %+v", result)
					}
					continue
				}
				if result.Status != "found" || result.AccompanyingTagsStatus != "complete" || result.AccompanyingTagsError != "" {
					t.Fatalf("accompanying-tag metadata was not completed: %+v", result)
				}
				want := "deployment/production,v1.2.9"
				if mode == "no companions" {
					want = ""
				}
				if strings.Join(result.AccompanyingTags, ",") != want {
					t.Fatalf("selected or different-commit tags appeared, or tags were not sorted: %+v", result)
				}
			}
			paths, _ := filepath.Glob("reports/*.html")
			if len(paths) != 1 {
				t.Fatalf("report missing: %v", paths)
			}
			report := readReportHTML(t, paths[0])
			if !strings.Contains(report, "Selected tag") || !strings.Contains(report, repositoryListTag) {
				t.Fatal("report did not distinguish the selected tag")
			}
			if mode != "no companions" && (!strings.Contains(report, "deployment/production") || !strings.Contains(report, "v1.2.9")) {
				t.Fatal("report omitted accompanying tags")
			}
			if strings.Contains(report, "v1.2.8") {
				t.Fatal("report included a tag on a different commit")
			}
		})
	}
}
