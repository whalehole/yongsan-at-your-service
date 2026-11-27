package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const releaseFromSHA = "1111111111111111111111111111111111111111"
const releaseToSHA = "2222222222222222222222222222222222222222"
const releaseFeatureSHA = "3333333333333333333333333333333333333333"

type releaseComponentProbe struct {
	Project    string `yaml:"project" json:"project"`
	FromRef    string `yaml:"from_ref" json:"from_ref"`
	ToRef      string `yaml:"to_ref" json:"to_ref"`
	FromCommit string `yaml:"from_commit" json:"from_commit"`
	ToCommit   string `yaml:"to_commit" json:"to_commit"`
	ToKind     string `yaml:"to_kind" json:"to_kind"`
	Status     string `yaml:"status" json:"status"`
	Error      string `yaml:"error" json:"error"`
	Commits    []struct {
		SHA     string `yaml:"sha" json:"sha"`
		Title   string `yaml:"title" json:"title"`
		Message string `yaml:"message" json:"message"`
		Author  string `yaml:"author" json:"author"`
		URL     string `yaml:"url" json:"url"`
	} `yaml:"commits" json:"commits"`
}

type releaseDraftProbe struct {
	SchemaVersion int `yaml:"schema_version"`
	Release       struct {
		Version string `yaml:"version"`
		Summary string `yaml:"summary"`
		Draft   bool   `yaml:"draft"`
		From    string `yaml:"from"`
		To      string `yaml:"to"`
	} `yaml:"release"`
	Scan struct {
		StartedAt   time.Time `yaml:"started_at"`
		FinishedAt  time.Time `yaml:"finished_at"`
		Concurrency int       `yaml:"concurrency"`
		Timeout     string    `yaml:"timeout"`
		Total       int       `yaml:"total"`
		Changed     int       `yaml:"changed"`
		Unchanged   int       `yaml:"unchanged"`
		Failed      int       `yaml:"failed"`
	} `yaml:"scan"`
	Components   []releaseComponentProbe `yaml:"components"`
	UpgradeNotes []string                `yaml:"upgrade_notes"`
	KnownIssues  []string                `yaml:"known_issues"`
}

type releaseScanFixture struct {
	server         *httptest.Server
	requests       atomic.Int32
	commitListings atomic.Int32
	mu             sync.Mutex
	projects       map[string]int
}

func newReleaseScanFixture(t *testing.T, behavior map[string]string) *releaseScanFixture {
	t.Helper()
	f := &releaseScanFixture{projects: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("release scan made a mutating request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/")
		project, endpoint, ok := strings.Cut(path, "/repository/")
		if !ok {
			t.Errorf("unexpected scan endpoint: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.projects[project]++
		f.mu.Unlock()
		switch behavior[project] {
		case "missing":
			w.WriteHeader(http.StatusNotFound)
			return
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case endpoint == "tags":
			fmt.Fprint(w, `[{"name":"2.2.6"},{"name":"2.2.7"},{"name":"2.3.0"}]`)
		case strings.HasPrefix(endpoint, "tags/"):
			ref := strings.TrimPrefix(endpoint, "tags/")
			sha := releaseFromSHA
			if ref == "2.2.8" && behavior[project] != "unchanged" {
				sha = releaseToSHA
			}
			fmt.Fprintf(w, `{"name":%q,"commit":{"id":%q}}`, ref, sha)
		case endpoint == "commits":
			f.commitListings.Add(1)
			if query := r.URL.Query(); query.Get("ref_name") != releaseFromSHA+".."+releaseToSHA || query.Get("page") != "1" || query.Get("per_page") != "100" || len(query) != 3 {
				t.Errorf("commit listing did not use only the pinned SHA range and pagination: %v", query)
			}
			if behavior[project] == "empty-range" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"id":%q,"title":"feat: add release dashboard","message":"feat: add release dashboard\n\nShows deployment status.","author_name":"Release Author","committed_date":"2026-11-26T00:00:00Z","web_url":%q},{"id":%q,"title":"fix: preserve release order","message":"fix: preserve release order","author_name":"Release Author","committed_date":"2026-11-26T00:01:00Z","web_url":%q}]`,
				releaseFeatureSHA, f.server.URL+"/"+project+"/-/commit/"+releaseFeatureSHA, releaseToSHA, f.server.URL+"/"+project+"/-/commit/"+releaseToSHA)
		default:
			t.Errorf("unexpected release endpoint: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func readReleaseDraft(t *testing.T, path string) releaseDraftProbe {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var draft releaseDraftProbe
	if err := yaml.Unmarshal(data, &draft); err != nil {
		t.Fatalf("release draft is not valid YAML: %v\n%s", err, data)
	}
	if strings.Contains(string(data), "test-token") {
		t.Error("release draft contains the GitLab authentication token")
	}
	if strings.Contains(string(data), "repository_list:") {
		t.Error("release draft includes obsolete tagging/building list metadata")
	}
	if strings.Contains(string(data), "comparison_url:") {
		t.Error("commit-only release draft includes an obsolete file comparison link")
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, item := range document["components"].([]any) {
		component := item.(map[string]any)
		if _, exists := component["changes"]; exists {
			t.Error("release draft duplicates the editable commits list in a changes section")
		}
		for _, item := range component["commits"].([]any) {
			commit := item.(map[string]any)
			for _, field := range []string{"change_request", "incident"} {
				if value, exists := commit[field]; !exists || value != nil {
					t.Errorf("commit must retain an empty %s field for editing: %#v", field, commit)
				}
			}
		}
	}
	return draft
}

func TestReleaseScanWritesOneEditableCommitListWithOrderedProvenance(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, map[string]string{"group/unchanged": "unchanged"})
	writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/changed\n    - project: group/unchanged\n")
	outputPath := filepath.Join("drafts", "nested", "release.yaml")
	output, logs, err := executeTest([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", outputPath, "--concurrency", "1", "--timeout", "12s", "--format", "json", "--no-progress"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var results []releaseComponentProbe
	if err := json.Unmarshal([]byte(output), &results); err != nil || len(results) != 2 || results[0].Project != "group/changed" || results[0].Status != "changed" || results[1].Project != "group/unchanged" || results[1].Status != "unchanged" {
		t.Fatalf("JSON scan results=%s error=%v", output, err)
	}
	draft := readReleaseDraft(t, outputPath)
	if draft.SchemaVersion != 1 || !draft.Release.Draft || draft.Release.Version != "2.2.8" || draft.Release.From != "2.2.7" || draft.Release.To != "2.2.8" || draft.Release.Summary != "" {
		t.Fatalf("draft release metadata=%#v", draft)
	}
	if draft.Scan.Concurrency != 1 || draft.Scan.Timeout != "12s" || draft.Scan.Total != 2 || draft.Scan.Changed != 1 || draft.Scan.Unchanged != 1 || draft.Scan.Failed != 0 || draft.Scan.StartedAt.IsZero() || draft.Scan.FinishedAt.Before(draft.Scan.StartedAt) {
		t.Fatalf("scan metadata=%#v", draft.Scan)
	}
	if len(draft.Components) != 2 || draft.Components[0].Project != "group/changed" || draft.Components[1].Project != "group/unchanged" || draft.Components[1].Status != "unchanged" {
		t.Fatalf("components lost order or unchanged repository: %#v", draft.Components)
	}
	changed := draft.Components[0]
	if changed.FromRef != "2.2.7" || changed.ToRef != "2.2.8" || changed.FromCommit != releaseFromSHA || changed.ToCommit != releaseToSHA || changed.ToKind != "tag" || len(changed.Commits) != 2 {
		t.Fatalf("changed component lacks pinned commit-range provenance: %#v", changed)
	}
	for i, commit := range changed.Commits {
		if commit.SHA != []string{releaseFeatureSHA, releaseToSHA}[i] || commit.Title != []string{"feat: add release dashboard", "fix: preserve release order"}[i] {
			t.Errorf("commit order or original text changed: %#v", commit)
		}
		if commit.Message == "" || commit.Author != "Release Author" || commit.URL == "" {
			t.Errorf("commit provenance incomplete: %#v", commit)
		}
	}
	if len(draft.UpgradeNotes) != 0 || len(draft.KnownIssues) != 0 {
		t.Fatalf("editorial sections should start empty: %#v", draft)
	}
	if !strings.Contains(logs, filepath.Base(outputPath)) || strings.Contains(output, outputPath) {
		t.Fatalf("draft path must be on stderr only: stdout=%q stderr=%q", output, logs)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(outputPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("release draft permissions=%v error=%v", info, err)
		}
	}
	if _, err := os.Stat("reports"); !os.IsNotExist(err) {
		t.Fatalf("release scan unexpectedly produced HTML reports: %v", err)
	}
	if f.commitListings.Load() != 1 {
		t.Fatalf("expected one commit listing for distinct source/target SHAs, got %d", f.commitListings.Load())
	}
}

func TestReleaseScanEmptyCommitRangeIsUnchangedWithDifferentResolvedSHAs(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, map[string]string{"group/rollback": "empty-range"})
	writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/rollback\n")
	output, _, err := executeTest([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "empty-range.yaml", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	draft := readReleaseDraft(t, "empty-range.yaml")
	if len(draft.Components) != 1 || draft.Scan.Unchanged != 1 || draft.Scan.Failed != 0 {
		t.Fatalf("empty target-only commit set was not a successful unchanged component: %#v", draft)
	}
	component := draft.Components[0]
	if component.FromCommit != releaseFromSHA || component.ToCommit != releaseToSHA || component.Status != "unchanged" || component.Error != "" || len(component.Commits) != 0 || f.commitListings.Load() != 1 {
		t.Fatalf("unchanged status depends on new commits, not matching endpoints: %#v", component)
	}
	var results []releaseComponentProbe
	if err := json.Unmarshal([]byte(output), &results); err != nil || len(results) != 1 || results[0].Status != "unchanged" || len(results[0].Commits) != 0 {
		t.Fatalf("empty commit-range JSON=%s error=%v", output, err)
	}
}

func TestReleaseScanUsesIndependentRepositoriesAndExactGroupBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name, project string
		flags         []string
	}{
		{"exact release group", "alpha/changed", []string{"--group", "alpha"}},
		{"selected release project", "beta/other", []string{"--only", "beta/other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			f := newReleaseScanFixture(t, nil)
			writeConfig(t, f.server.URL, "tagging:\n  repositories:\n    - project: tag/unused\nbuilding:\n  repositories:\n    - project: build/unused\nrelease:\n  repositories:\n    - project: alpha/changed\n    - project: alpha/team/nested\n    - project: alphabet/other\n    - project: beta/other\n")
			args := append([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "release.yml"}, tc.flags...)
			if _, _, err := executeTest(args, "", false); err != nil {
				t.Fatal(err)
			}
			draft := readReleaseDraft(t, "release.yml")
			if len(draft.Components) != 1 || draft.Components[0].Project != tc.project {
				t.Fatalf("selection=%#v", draft)
			}
			data, err := os.ReadFile("release.yml")
			if err != nil {
				t.Fatal(err)
			}
			for _, excluded := range []string{"tag/unused", "build/unused", "repository_list:", "tagging:", "building:"} {
				if strings.Contains(string(data), excluded) {
					t.Errorf("independent release draft includes %q", excluded)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.projects) != 1 || f.projects[tc.project] == 0 {
				t.Fatalf("requests escaped exact selected group/list: %v", f.projects)
			}
		})
	}
}

func TestReleaseScanPreservesMissingTagAndAccessFailuresInPartialDraft(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, map[string]string{"group/missing": "missing", "group/denied": "denied"})
	writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/changed\n    - project: group/missing\n    - project: group/denied\n")
	output, _, err := executeTest([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "partial.yaml", "--format", "json"}, "", false)
	if ExitCode(err) != 1 {
		t.Fatalf("error=%v", err)
	}
	draft := readReleaseDraft(t, "partial.yaml")
	if draft.Scan.Total != 3 || draft.Scan.Changed != 1 || draft.Scan.Failed != 2 || len(draft.Components) != 3 {
		t.Fatalf("partial draft summary=%#v", draft)
	}
	for i, project := range []string{"group/changed", "group/missing", "group/denied"} {
		if draft.Components[i].Project != project {
			t.Fatalf("partial draft lost input order: %#v", draft.Components)
		}
	}
	if draft.Components[0].Status != "changed" || draft.Components[1].Status != "failed" || !strings.Contains(draft.Components[1].Error, "404") || draft.Components[2].Status != "failed" || !strings.Contains(draft.Components[2].Error, "403") {
		t.Fatalf("missing or inaccessible tags were not failures: %#v", draft.Components)
	}
	var results []releaseComponentProbe
	if err := json.Unmarshal([]byte(output), &results); err != nil || len(results) != 3 || results[1].Status != "failed" || results[2].Status != "failed" {
		t.Fatalf("partial JSON=%s error=%v", output, err)
	}
}

func TestReleaseScanRefusesExistingOutputBeforeGitLabRequests(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, nil)
	writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/changed\n")
	const original = "editorial release notes already exist\n"
	if err := os.WriteFile("release.yaml", []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeTest([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "release.yaml"}, "", false)
	if err == nil || f.requests.Load() != 0 {
		t.Fatalf("existing output did not stop before GitLab: error=%v requests=%d", err, f.requests.Load())
	}
	data, err := os.ReadFile("release.yaml")
	if err != nil || string(data) != original {
		t.Fatalf("existing release draft was overwritten: %q error=%v", data, err)
	}
}

func TestReleaseScanRejectsInvalidInputBeforeGitLabRequests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"missing target", []string{"--from", "2.2.7"}},
		{"missing previous choice", []string{"--to", "2.2.8"}},
		{"conflicting previous inputs", []string{"--to", "2.2.8", "--from", "2.2.7", "--previous"}},
		{"conflicting target inputs", []string{"--from", "2.2.7", "--to", "2.2.8", "--to-branch", "main"}},
		{"dry run", []string{"--from", "2.2.7", "--to", "2.2.8", "--dry-run"}},
		{"non-YAML output", []string{"--from", "2.2.7", "--to", "2.2.8", "--output", "release.json"}},
		{"removed repository selection flag", []string{"--from", "2.2.7", "--to", "2.2.8", "--repositories", "tagging"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workingDirectory(t)
			f := newReleaseScanFixture(t, nil)
			writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/changed\n")
			args := append([]string{"release", "scan"}, tc.flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || f.requests.Load() != 0 {
				t.Fatalf("invalid release inputs reached GitLab: error=%v requests=%d", err, f.requests.Load())
			}
		})
	}
}

func TestReleaseScanInteractiveSelectionThroughDirectCommandAndRootMenu(t *testing.T) {
	for _, rootMenu := range []bool{false, true} {
		t.Run(fmt.Sprintf("root_menu=%v", rootMenu), func(t *testing.T) {
			workingDirectory(t)
			f := newReleaseScanFixture(t, nil)
			writeConfig(t, f.server.URL, "tagging:\n  repositories:\n    - project: tag/unused\nbuilding:\n  repositories:\n    - project: build/unused\nrelease:\n  repositories:\n    - project: alpha/changed\n    - project: alpha/team/nested\n    - project: beta/other\n")
			args := []string{"release", "scan"}
			input := "1\n2.2.8\n1\n2.2.7\n\n"
			if rootMenu {
				args = nil
				input = "7\n" + input
			}
			_, prompts, err := executeTest(args, input, true)
			if err != nil {
				t.Fatal(err)
			}
			draft := readReleaseDraft(t, filepath.Join(filepath.FromSlash(releaseYAMLDirectory), "release-notes-2.2.8.yaml"))
			if len(draft.Components) != 1 || draft.Components[0].Project != "alpha/changed" || draft.Components[0].FromRef != "2.2.7" || draft.Components[0].ToRef != "2.2.8" {
				t.Fatalf("interactive choices were not respected: %#v", draft)
			}
			for _, excluded := range []string{"Select a repository list", "Tagging repositories", "Building repositories", "tag/unused", "build/unused"} {
				if strings.Contains(prompts, excluded) {
					t.Errorf("release flow includes an unrelated operation choice %q: %s", excluded, prompts)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.projects) != 1 || f.projects["alpha/changed"] == 0 {
				t.Fatalf("interactive scan requested unselected repositories: %v", f.projects)
			}
		})
	}
}

func TestReleaseScanPreviousFlagUsesDetectedBaseline(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, nil)
	writeConfig(t, f.server.URL, "release:\n  repositories:\n    - project: group/changed\n")
	output, _, err := executeTest([]string{"release", "scan", "--to", "2.2.8", "--previous", "--output", "previous.yaml", "--format", "json"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	draft := readReleaseDraft(t, "previous.yaml")
	if len(draft.Components) != 1 || draft.Components[0].FromRef != "2.2.7" || draft.Components[0].FromCommit != releaseFromSHA || draft.Components[0].ToCommit != releaseToSHA || draft.Components[0].Status != "changed" {
		t.Fatalf("automatic baseline was not wired into the draft: %#v", draft.Components)
	}
	var results []releaseComponentProbe
	if err := json.Unmarshal([]byte(output), &results); err != nil || len(results) != 1 || results[0].FromRef != "2.2.7" {
		t.Fatalf("automatic baseline JSON=%s error=%v", output, err)
	}
}

func TestReleaseScanRequiresItsOwnRepositoryListWithoutFallback(t *testing.T) {
	workingDirectory(t)
	f := newReleaseScanFixture(t, nil)
	writeConfig(t, f.server.URL, "tagging:\n  repositories:\n    - project: tag/unused\nbuilding:\n  repositories:\n    - project: build/unused\n")
	_, _, err := executeTest([]string{"release", "scan", "--from", "2.2.7", "--to", "2.2.8", "--output", "release.yaml"}, "", false)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "release.repositories") || f.requests.Load() != 0 {
		t.Fatalf("empty release list fell back to another operation: error=%v requests=%d", err, f.requests.Load())
	}
	if _, err := os.Stat("release.yaml"); !os.IsNotExist(err) {
		t.Fatalf("empty release repository list created an output draft: %v", err)
	}
}
