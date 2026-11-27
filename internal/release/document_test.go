package release

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReadDocumentPreservesEditedMetadataAndAnnotations(t *testing.T) {
	const input = `schema_version: 1
release:
  title: Release 2.2.8
  version: 2.2.8
  date: "2026-11-27"
  summary: Improve checkout reliability.
  draft: false
  from: 2.2.7
  to: release/2.2
scan:
  started_at: "2026-11-27T01:00:00Z"
  finished_at: "2026-11-27T01:00:05Z"
  concurrency: 8
  timeout: 2m0s
  total: 3
  changed: 1
  unchanged: 1
  failed: 1
components:
  - project: team/checkout
    from_ref: 2.2.7
    to_ref: release/2.2
    from_commit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    to_commit: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    to_kind: branch
    status: changed
    duration: 1s
    commits:
      - sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        title: Restore checkout
        remarks: " Restart API \n"
        change_request: 0
        incident: 17
        message: "Restore checkout\n\nKeep existing carts."
        author: " Ada "
        committed_at: "2026-11-26T10:00:00+08:00"
        url: https://gitlab.example.com/team/checkout/-/commit/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  - project: team/unchanged
    status: unchanged
    commits: []
  - project: team/unavailable
    status: failed
    error: GitLab denied access.
    commits: []
upgrade_notes:
  - Restart the API after deployment.
known_issues: []
`
	document, err := ReadDocument(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || document.Release.Version != "2.2.8" || document.Release.Title != "Release 2.2.8" || document.Release.Date != "2026-11-27" || document.Release.Draft || document.Release.From != "2.2.7" || document.Release.To != "release/2.2" || document.Scan.Total != 3 || document.Scan.Changed != 1 || document.Scan.Unchanged != 1 || document.Scan.Failed != 1 {
		t.Fatalf("metadata was not retained: %+v", document)
	}
	commit := document.Components[0].Commits[0]
	if commit.Remarks != " Restart API \n" || commit.Author != " Ada " || commit.ChangeRequest == nil || *commit.ChangeRequest != 0 || commit.Incident == nil || *commit.Incident != 17 || commit.Message != "Restore checkout\n\nKeep existing carts." || commit.URL == "" {
		t.Fatalf("edited annotations were changed: %+v", commit)
	}
	if !reflect.DeepEqual(document.UpgradeNotes, []string{"Restart the API after deployment."}) || len(document.KnownIssues) != 0 || document.Components[2].Error != "GitLab denied access." {
		t.Fatal("release notes or failure details were not retained")
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadDocument(strings.NewReader(string(encoded)))
	if err != nil || !reflect.DeepEqual(roundTrip, document) {
		t.Fatalf("document YAML tags do not round trip: %+v, %v", roundTrip, err)
	}
}

func TestReadDocumentAllowsEmptyNoChangeLists(t *testing.T) {
	for _, components := range []string{"", "components: []\n", "components: null\n", "components:\n  - project: team/service\n    status: unchanged\n    commits: []\n"} {
		document, err := ReadDocument(strings.NewReader("schema_version: 1\nrelease:\n  version: 2.2.8\nupgrade_notes: []\nknown_issues: []\n" + components))
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []GroupBy{GroupRepository, GroupChangeRequest, GroupIncident, GroupRemarks, GroupAuthor} {
			if groups := GroupCommits(document, mode); len(groups) != 0 {
				t.Fatalf("no-change document produced phantom groups: %+v", groups)
			}
		}
	}
}

func TestReadDocumentRejectsInvalidOrUnsupportedYAML(t *testing.T) {
	const minimal = "schema_version: 1\nrelease:\n  version: 2.2.8\n"
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", "empty"},
		{"null", "null\n", "empty or null"},
		{"empty document", "---\n", "empty or null"},
		{"unsupported schema", "schema_version: 2\nrelease: {version: 2.2.8}\n", "schema_version must be 1"},
		{"missing schema", "release: {version: 2.2.8}\n", "schema_version must be 1"},
		{"missing release version", "schema_version: 1\nrelease: {}\n", "release.version is required"},
		{"blank release version", "schema_version: 1\nrelease: {version: '  '}\n", "release.version is required"},
		{"unknown root field", minimal + "unknown: true\n", "field unknown"},
		{"unknown release field", "schema_version: 1\nrelease: {version: 2.2.8, typo: true}\n", "field typo"},
		{"unknown scan field", minimal + "scan: {typo: true}\n", "field typo"},
		{"unknown component field", minimal + "components:\n  - project: team/service\n    status: changed\n    typo: true\n", "field typo"},
		{"unknown commit field", minimal + "components:\n  - project: team/service\n    status: changed\n    commits: [{sha: abc, typo: true}]\n", "field typo"},
		{"unknown status", minimal + "components: [{project: team/service, status: built}]\n", "components[0].status"},
		{"missing status", minimal + "components: [{project: team/service}]\n", "components[0].status"},
		{"missing project", minimal + "components: [{status: unchanged, commits: []}]\n", "components[0].project is required"},
		{"blank project", minimal + "components: [{project: '  ', status: unchanged, commits: []}]\n", "components[0].project is required"},
		{"nonnumeric annotation", minimal + "components:\n  - project: team/service\n    status: changed\n    commits: [{sha: abc, change_request: CR-123}]\n", "change_request must be an integer or null"},
		{"multiple documents", minimal + "---\n" + minimal, "only one"},
		{"second null document", minimal + "---\nnull\n", "only one"},
		{"duplicate fields", minimal + "schema_version: 1\n", "already defined"},
		{"malformed", "schema_version: [\n", "decode release-notes YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadDocument(strings.NewReader(tc.input)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want=%q", err, tc.want)
			}
		})
	}
}

func TestReadDocumentAnnotationsRequireIntegerOrNullWithoutCoercion(t *testing.T) {
	for _, field := range []string{"change_request", "incident"} {
		for _, value := range []string{"1.5", "1.0", "true", "'17'", "[]", "{}"} {
			input := "schema_version: 1\nrelease: {version: 2.2.8}\ncomponents:\n  - project: team/service\n    status: changed\n    commits:\n      - sha: abc\n        " + field + ": " + value + "\n"
			if _, err := ReadDocument(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), field+" must be an integer or null") {
				t.Errorf("%s=%s was accepted or misdiagnosed: %v", field, value, err)
			}
		}
	}
	for _, value := range []string{"0", "17", "-1", "null"} {
		input := "schema_version: 1\nrelease: {version: 2.2.8}\ncomponents:\n  - project: ' team/service '\n    status: changed\n    commits:\n      - sha: abc\n        change_request: " + value + "\n        incident: " + value + "\n"
		document, err := ReadDocument(strings.NewReader(input))
		if err != nil || document.Components[0].Project != " team/service " {
			t.Fatalf("valid annotation or exact repository text changed: %+v, %v", document, err)
		}
		commit := document.Components[0].Commits[0]
		if value == "0" && (commit.ChangeRequest == nil || *commit.ChangeRequest != 0 || commit.Incident == nil || *commit.Incident != 0) {
			t.Fatal("numeric zero was treated as missing")
		}
		if value == "null" && (commit.ChangeRequest != nil || commit.Incident != nil) {
			t.Fatal("null annotations were not retained as missing")
		}
	}
}

func TestReadDocumentRejectsFractionalIDsThroughAliasesAndMerges(t *testing.T) {
	for _, commit := range []string{
		"      - sha: abc\n        remarks: &fraction 1.5\n        change_request: *fraction\n",
		"      - sha: abc\n        <<: {incident: 1.5}\n",
	} {
		input := "schema_version: 1\nrelease: {version: 2.2.8}\ncomponents:\n  - project: team/service\n    status: changed\n    commits:\n" + commit
		if _, err := ReadDocument(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "must be an integer or null") {
			t.Fatalf("fractional annotation was silently coerced: %v", err)
		}
	}
}
