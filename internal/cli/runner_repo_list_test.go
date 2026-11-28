package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func repositoryLookupPresentationResults() []result {
	return []result{
		{Project: "team/z-matched", Operation: "repo-list", Tag: "release-v2", Commit: testSHA, CommitTitle: "fix: retain the existing payment result", Status: "found", Message: "The selected Git tag was found."},
		{Project: "team/a-absent", Operation: "repo-list", Tag: "release-v2", Status: "skipped", Message: "The selected Git tag is absent; repository access was verified."},
		{Project: "team/m-failed", Operation: "repo-list", Tag: "release-v2", Status: "failed", Error: "Access was denied; tag presence could not be determined."},
	}
}

func TestWriteResultsRepositoryLookupShowsStableCoverageAndReadOnlyCounts(t *testing.T) {
	results := repositoryLookupPresentationResults()
	var output bytes.Buffer
	if err := writeResults(&output, "text", results); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if header := strings.Join(strings.Fields(strings.SplitN(text, "\n", 2)[0]), " "); header != "REPOSITORY STATUS TAG COMMIT TITLE" {
		t.Fatalf("repository lookup columns=%q", header)
	}
	for _, value := range []string{"REPOSITORY", "STATUS", "TAG", "COMMIT", "TITLE", "release-v2", testSHA, results[0].CommitTitle, results[1].Message, results[2].Error, "3 checked; 1 found, 1 absent, 1 failed."} {
		if !strings.Contains(text, value) {
			t.Errorf("repository lookup output is missing %q", value)
		}
	}
	previous := -1
	for _, result := range results {
		index := strings.Index(text, result.Project)
		if index <= previous {
			t.Fatalf("terminal output changed the checked repository order: %s", text)
		}
		previous = index
	}
	for _, phrase := range []string{"created,", "deleted,", "built,", "skipped,", "planned,"} {
		if strings.Contains(text, phrase) {
			t.Errorf("read-only lookup printed operation-write totals %q", phrase)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == results[1].Project && fields[1] != "absent" {
			t.Fatalf("verified missing tag did not get an absent status: %s", line)
		}
	}
}

func TestWriteResultsRepositoryLookupJSONRetainsAllChecksAndOriginalMetadata(t *testing.T) {
	results := repositoryLookupPresentationResults()
	results[0].CommitTitle = "fix: original\ttitle\nwith details"
	var output bytes.Buffer
	if err := writeResults(&output, "json", results); err != nil {
		t.Fatal(err)
	}
	var decoded []result
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || len(decoded) != len(results) {
		t.Fatalf("JSON omitted checked repositories: %s (%v)", output.String(), err)
	}
	for i, result := range decoded {
		if result.Project != results[i].Project || result.Status != results[i].Status || result.Tag != results[i].Tag || result.Commit != results[i].Commit || result.CommitTitle != results[i].CommitTitle || result.Error != results[i].Error {
			t.Errorf("JSON result metadata was changed: got=%+v want=%+v", result, results[i])
		}
	}
	output.Reset()
	if err := writeResults(&output, "text", results); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "fix: original title with details") {
		t.Error("commit title was not kept on one terminal table row")
	}
	if results[0].CommitTitle != "fix: original\ttitle\nwith details" {
		t.Error("formatting modified the original result title")
	}
}

func TestWriteResultsRepositoryLookupDistinguishesAbsentFromUnknown(t *testing.T) {
	for _, failed := range []bool{false, true} {
		results := []result{{Project: "team/service", Operation: "repo-list", Tag: "v2", Status: "skipped"}}
		if failed {
			results[0].Status, results[0].Error = "failed", "Lookup failed before tag presence could be checked."
		}
		var output bytes.Buffer
		if err := writeResults(&output, "text", results); err != nil {
			t.Fatal(err)
		}
		if failed {
			if !strings.Contains(output.String(), "1 checked; 0 found, 0 absent, 1 failed.") || !strings.Contains(output.String(), "tag presence remains unknown for failed lookups") || strings.Contains(output.String(), "No checked repositories contain") {
				t.Errorf("failed lookup was presented as verified absence: %s", output.String())
			}
		} else if !strings.Contains(output.String(), "1 checked; 0 found, 1 absent, 0 failed.") || !strings.Contains(output.String(), "No checked repositories contain the selected Git tag.") {
			t.Errorf("verified absent result is unclear: %s", output.String())
		}
	}
}

func TestWriteResultsKeepsExistingOperationStatusTotals(t *testing.T) {
	var output bytes.Buffer
	if err := writeResults(&output, "text", []result{{Project: "team/service", Operation: "tag", Tag: "v1", Commit: testSHA, Status: "created"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "PROJECT") || !strings.Contains(output.String(), "1 total; 1 created, 0 deleted, 0 built, 0 skipped, 0 planned, 0 failed.") || strings.Contains(output.String(), " found,") {
		t.Fatalf("existing operation output changed: %s", output.String())
	}
}
