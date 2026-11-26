package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
)

const baselineSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const targetSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type fakeSource struct {
	refs        map[string]string
	errors      map[string]error
	tags        []string
	commitRange gitlab.CommitRange
	commitsErr  error
	calls       []string
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		refs: map[string]string{"tag:v1.2.1": baselineSHA, "tag:v1.2.3": targetSHA}, errors: map[string]error{},
	}
}

func (s *fakeSource) resolve(kind, ref string) (string, error) {
	key := kind + ":" + ref
	s.calls = append(s.calls, key)
	if err := s.errors[key]; err != nil {
		return "", err
	}
	if sha, ok := s.refs[key]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("reference %q was not found", ref)
}

func (s *fakeSource) ResolveTag(_ context.Context, _, ref string) (string, error) {
	return s.resolve("tag", ref)
}

func (s *fakeSource) ResolveBranch(_ context.Context, _, ref string) (string, error) {
	return s.resolve("branch", ref)
}

func (s *fakeSource) ResolveCommit(_ context.Context, _, ref string) (string, error) {
	return s.resolve("commit", ref)
}

func (s *fakeSource) CommitsBetween(_ context.Context, _, from, to string) (gitlab.CommitRange, error) {
	s.calls = append(s.calls, "commits:"+from+".."+to)
	return s.commitRange, s.commitsErr
}

func (s *fakeSource) ListTags(_ context.Context, _ string) ([]string, error) {
	s.calls = append(s.calls, "list-tags")
	return s.tags, s.errors["list-tags"]
}

func TestScanPinsReferencesAndKeepsCommitProvenance(t *testing.T) {
	source := newFakeSource()
	source.commitRange.Commits = []gitlab.ReleaseCommit{{
		SHA: targetSHA, Title: "feat(api): add customer search", Message: "feat(api): add customer search\n\nSupport exact customer IDs.",
		Author: "Example Developer", CommittedAt: "2026-11-26T10:15:00+08:00", URL: "https://gitlab.example.com/group/service/-/commit/" + targetSHA,
	}}
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
	if component.Status != "changed" || component.Error != "" || component.FromCommit != baselineSHA || component.ToCommit != targetSHA || component.FromRef != "v1.2.1" || component.ToRef != "v1.2.3" || component.ToKind != "tag" {
		t.Fatalf("component = %#v", component)
	}
	wantCalls := []string{"tag:v1.2.3", "tag:v1.2.1", "commits:" + baselineSHA + ".." + targetSHA}
	if !reflect.DeepEqual(source.calls, wantCalls) {
		t.Fatalf("unpinned or repeated reference lookup: %v", source.calls)
	}
	if len(component.Commits) != 1 {
		t.Fatalf("notes = %#v", component)
	}
	commit := component.Commits[0]
	if commit.SHA != targetSHA || commit.Title != source.commitRange.Commits[0].Title || commit.Message != source.commitRange.Commits[0].Message || commit.Author != "Example Developer" || commit.CommittedAt != "2026-11-26T10:15:00+08:00" || commit.URL == "" {
		t.Fatalf("commit provenance = %#v", commit)
	}
}

func TestScanBranchAndCommitTargetsKeepVersionSeparate(t *testing.T) {
	for _, kind := range []string{"branch", "commit"} {
		source := newFakeSource()
		options := Options{From: "v1.2.1", Version: "v8.0.0"}
		ref := "release/customer-search"
		if kind == "branch" {
			options.ToBranch = ref
		} else {
			ref = targetSHA[:8]
			options.ToCommit = ref
		}
		source.refs[kind+":"+ref] = targetSHA
		component := Scan(context.Background(), source, "group/service", options)
		if component.Status != "unchanged" || component.ToRef != ref || component.ToKind != kind || component.ToCommit != targetSHA {
			t.Fatalf("%s: %#v", kind, component)
		}
		if source.calls[0] != kind+":"+ref || len(source.calls) != 3 {
			t.Fatalf("%s: calls = %v", kind, source.calls)
		}
	}
}

func TestScanUnchangedRequiresPinnedIdentityOrCompletedEmptyCommitRange(t *testing.T) {
	for _, identical := range []bool{true, false} {
		source := newFakeSource()
		if identical {
			source.refs["tag:v1.2.1"] = targetSHA
		}
		component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
		if component.Status != "unchanged" || component.Error != "" || component.Commits == nil || len(component.Commits) != 0 {
			t.Fatalf("identical=%v: %#v", identical, component)
		}
		wantCalls := 3
		if identical {
			wantCalls = 2
		}
		if len(source.calls) != wantCalls {
			t.Fatalf("identical=%v: calls = %v", identical, source.calls)
		}
	}
}

func TestScanDistinctRollbackTargetWithNoNewCommitsIsUnchanged(t *testing.T) {
	source := newFakeSource()
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.3", To: "v1.2.1"})
	if component.Status != "unchanged" || component.Error != "" || component.FromCommit != targetSHA || component.ToCommit != baselineSHA || len(component.Commits) != 0 {
		t.Fatalf("empty new-commit range was not accepted: %#v", component)
	}
	wantCalls := []string{"tag:v1.2.1", "tag:v1.2.3", "commits:" + targetSHA + ".." + baselineSHA}
	if !reflect.DeepEqual(source.calls, wantCalls) {
		t.Fatalf("rollback scan did not inspect the exact pinned new-commit range: %v", source.calls)
	}
}

func TestScanPreservesContextualGitLabOperationErrorWithoutDuplicatePhase(t *testing.T) {
	source := newFakeSource()
	source.commitsErr = &gitlab.OperationError{
		Message: `find new release commits in repository "group/service": GitLab rejected access (HTTP 403); no release notes were generated.`,
		Cause:   errors.New("forbidden"),
	}
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
	if component.Status != "failed" || component.Error != source.commitsErr.Error() || component.FromCommit != baselineSHA || component.ToCommit != targetSHA {
		t.Fatalf("GitLab action context was lost or repeated: %#v", component)
	}
	assertNoNotes(t, component)
}

func TestScanSameTagResolvesOnePinnedReference(t *testing.T) {
	source := newFakeSource()
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.3", To: "v1.2.3"})
	if component.Status != "unchanged" || component.FromCommit != targetSHA || component.ToCommit != targetSHA {
		t.Fatalf("component = %#v", component)
	}
	if !reflect.DeepEqual(source.calls, []string{"tag:v1.2.3"}) {
		t.Fatalf("same tag was resolved more than once: %v", source.calls)
	}
}

func TestScanExplicitMissingBaselineDoesNotGuessPreviousTag(t *testing.T) {
	source := newFakeSource()
	source.tags = []string{"v1.2.1", "v1.2.2"}
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.0", To: "v1.2.3"})
	if component.Status != "failed" || !strings.Contains(component.Error, `resolve baseline tag "v1.2.0"`) || component.FromRef != "v1.2.0" || component.ToCommit != targetSHA || component.FromCommit != "" {
		t.Fatalf("component = %#v", component)
	}
	if !reflect.DeepEqual(source.calls, []string{"tag:v1.2.3", "tag:v1.2.0"}) {
		t.Fatalf("missing explicit baseline triggered fallback: %v", source.calls)
	}
	assertNoNotes(t, component)
}

func TestScanPhaseFailuresPreserveResolvedMetadataWithoutNotes(t *testing.T) {
	tests := []struct {
		name, phase string
		setup       func(*fakeSource)
		wantTarget  bool
		wantBase    bool
	}{
		{name: "target access", phase: `resolve target tag "v1.2.3"`, setup: func(s *fakeSource) { s.errors["tag:v1.2.3"] = errors.New("access denied") }},
		{name: "baseline access", phase: `resolve baseline tag "v1.2.1"`, setup: func(s *fakeSource) { s.errors["tag:v1.2.1"] = errors.New("access denied") }, wantTarget: true},
		{name: "incomplete commit listing", phase: "find new release commits", setup: func(s *fakeSource) {
			s.commitRange.Commits = []gitlab.ReleaseCommit{{SHA: targetSHA, Title: "feat: partial result must not become release notes"}}
			s.commitsErr = errors.New("commit listing timed out; results are incomplete")
		}, wantTarget: true, wantBase: true},
		{name: "malformed commit provenance", phase: "read release commits", setup: func(s *fakeSource) {
			s.commitRange.Commits = []gitlab.ReleaseCommit{{SHA: targetSHA, Title: "feat: valid but incomplete batch"}, {SHA: "short", Title: "fix: missing full SHA"}}
		}, wantTarget: true, wantBase: true},
		{name: "malformed target", phase: "resolve target tag", setup: func(s *fakeSource) { s.refs["tag:v1.2.3"] = "not-a-full-sha" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := newFakeSource()
			test.setup(source)
			component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
			if component.Status != "failed" || !strings.Contains(component.Error, test.phase) || !strings.Contains(component.Error, `repository "group/service"`) {
				t.Fatalf("component = %#v", component)
			}
			if (component.ToCommit != "") != test.wantTarget || (component.FromCommit != "") != test.wantBase {
				t.Fatalf("lost or invented resolution metadata: %#v", component)
			}
			assertNoNotes(t, component)
		})
	}
}

func TestScanPreviousTagUsesExistingStableTrackAndPinsSelectedTag(t *testing.T) {
	source := newFakeSource()
	source.tags = []string{"v1.1.999", "v1.2.0", "1.2.1", "v1.2.2-rc.1", "invalid-tag", "v1.3.0", "v1.2.4"}
	source.refs["tag:1.2.1"] = baselineSHA
	component := Scan(context.Background(), source, "group/service", Options{To: "v1.2.3", Previous: true})
	if component.Status != "unchanged" || component.FromRef != "1.2.1" || component.FromCommit != baselineSHA {
		t.Fatalf("component = %#v", component)
	}
	wantCalls := []string{"tag:v1.2.3", "list-tags", "tag:1.2.1", "commits:" + baselineSHA + ".." + targetSHA}
	if !reflect.DeepEqual(source.calls, wantCalls) {
		t.Fatalf("calls = %v", source.calls)
	}
	for _, listError := range []error{errors.New("permission denied"), nil} {
		source = newFakeSource()
		source.errors["list-tags"] = listError
		component = Scan(context.Background(), source, "group/service", Options{To: "v1.2.3", Previous: true})
		if component.Status != "failed" || component.ToCommit != targetSHA || component.FromCommit != "" {
			t.Fatalf("listError=%v: %#v", listError, component)
		}
		assertNoNotes(t, component)
	}
}

func TestScanRetainsAllRawCommitsAndSourceMetadataInOrder(t *testing.T) {
	source := newFakeSource()
	titles := []string{"feat(api): new endpoint", "fix: correct timezone", "perf(db): fewer queries", "refactor: simplify adapter", "feat!: remove old API", "fix(api)!: incompatible response", "docs: migration notes", "chore: migration", "Merge branch 'release' into main", "Improve a plain commit", "", "docs: mention breaking syntax", "  Refine customer display  "}
	for i, title := range titles {
		commit := gitlab.ReleaseCommit{
			SHA: fmt.Sprintf("%040x", i+1), Title: title, Message: title + "\n\nOriginal commit body.",
			Author: fmt.Sprintf("Developer %d", i+1), CommittedAt: fmt.Sprintf("2026-11-26T%02d:15:00+08:00", i),
			URL: fmt.Sprintf("https://gitlab.example.com/commit/%d", i+1),
		}
		if i == 6 {
			commit.Message = title + "\n\nBREAKING CHANGE: old flags were removed"
		} else if i == 7 {
			commit.Message = title + "\n\nBREAKING-CHANGE: authentication changed"
		} else if i == 11 {
			commit.Message = "This sentence mentions BREAKING CHANGE: as an example, without declaring one."
		}
		source.commitRange.Commits = append(source.commitRange.Commits, commit)
	}
	component := Scan(context.Background(), source, "group/service", Options{From: "v1.2.1", To: "v1.2.3"})
	if component.Status != "changed" || len(component.Commits) != len(source.commitRange.Commits) {
		t.Fatalf("commits were lost or fabricated: %#v", component)
	}
	for i, commit := range component.Commits {
		original := source.commitRange.Commits[i]
		want := Commit{SHA: original.SHA, Title: original.Title, Message: original.Message, Author: original.Author, CommittedAt: original.CommittedAt, URL: original.URL}
		if !reflect.DeepEqual(commit, want) {
			t.Fatalf("source commit %d was reordered or altered:\n got: %#v\nwant: %#v", i, commit, want)
		}
	}
}

func TestValidateOptionsAndCanceledScansDoNotContactGitLab(t *testing.T) {
	for _, options := range []Options{
		{}, {From: "v1"}, {From: "v1", To: "v2", ToBranch: "release"}, {To: "v2"},
		{From: "v1", To: "v2", Previous: true}, {ToBranch: "release", Version: "v1.2.3", Previous: true},
		{To: "release-2026", Previous: true}, {To: "v1.2.3-rc.1", Previous: true},
	} {
		source := newFakeSource()
		if err := ValidateOptions(options); err == nil {
			t.Fatalf("accepted invalid options: %#v", options)
		}
		component := Scan(context.Background(), source, "group/service", options)
		if component.Status != "failed" || len(source.calls) != 0 {
			t.Fatalf("invalid options contacted GitLab: %#v %v", component, source.calls)
		}
	}
	options := Options{From: "v1.2.1", To: "release-2026"}
	if err := ValidateOptions(options); err != nil {
		t.Fatalf("explicit baseline should allow non-SemVer target: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := newFakeSource()
	component := Scan(ctx, source, "group/service", options)
	if component.Status != "failed" || !strings.Contains(component.Error, "context canceled") || len(source.calls) != 0 {
		t.Fatalf("canceled scan = %#v %v", component, source.calls)
	}
	assertNoNotes(t, component)
	component = Scan(context.Background(), nil, "group/service", options)
	if component.Status != "failed" || !strings.Contains(component.Error, "unavailable") {
		t.Fatalf("nil source = %#v", component)
	}
}

func assertNoNotes(t *testing.T, component Component) {
	t.Helper()
	if component.Commits == nil || len(component.Commits) != 0 {
		t.Fatalf("failed scan has notes or a null commits list: %#v", component)
	}
	data, err := json.Marshal(component)
	if err != nil || strings.Contains(string(data), ":null") {
		t.Fatalf("editable result has null lists: %s (%v)", data, err)
	}
}
