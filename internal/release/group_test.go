package release

import (
	"reflect"
	"testing"
)

func groupID(value int64) *int64 { return &value }

func groupingDocument() Document {
	return Document{Components: []Component{
		{Project: "team/a", Status: "changed", Commits: []Commit{
			{SHA: "a1", Remarks: "Restart", Author: "Ada", ChangeRequest: groupID(7)},
			{SHA: "a2", Incident: groupID(0)},
			{SHA: "a3", Remarks: " Restart", Author: "ada", ChangeRequest: groupID(0), Incident: groupID(17)},
		}},
		{Project: "team/no-change", Status: "unchanged", Commits: []Commit{}},
		{Project: "team/b", Status: "changed", Commits: []Commit{
			{SHA: "b1", Remarks: "Restart", Author: "Ada", ChangeRequest: groupID(7), Incident: groupID(17)},
			{SHA: "b2", Remarks: "Restart ", Author: "Unknown author"},
			{SHA: "b3", Remarks: "No remarks", Author: " Ada ", ChangeRequest: groupID(-1), Incident: groupID(0)},
		}},
		{Project: "team/failed", Status: "failed", Commits: []Commit{{SHA: "f1", Remarks: "restart", Author: " ", ChangeRequest: groupID(0)}}},
	}}
}

func TestGroupCommitsAllModesPreserveExactValuesOrderAndRepository(t *testing.T) {
	type expected struct {
		value   string
		missing bool
		shas    []string
	}
	for _, tc := range []struct {
		by   GroupBy
		want []expected
	}{
		{GroupRepository, []expected{
			{"team/a", false, []string{"a1", "a2", "a3"}}, {"team/b", false, []string{"b1", "b2", "b3"}}, {"team/failed", false, []string{"f1"}},
		}},
		{GroupChangeRequest, []expected{
			{"7", false, []string{"a1", "b1"}}, {"No change request", true, []string{"a2", "b2"}}, {"0", false, []string{"a3", "f1"}}, {"-1", false, []string{"b3"}},
		}},
		{GroupIncident, []expected{
			{"No incident", true, []string{"a1", "b2", "f1"}}, {"0", false, []string{"a2", "b3"}}, {"17", false, []string{"a3", "b1"}},
		}},
		{GroupRemarks, []expected{
			{"Restart", false, []string{"a1", "b1"}}, {"No remarks", true, []string{"a2"}}, {" Restart", false, []string{"a3"}}, {"Restart ", false, []string{"b2"}}, {"No remarks", false, []string{"b3"}}, {"restart", false, []string{"f1"}},
		}},
		{GroupAuthor, []expected{
			{"Ada", false, []string{"a1", "b1"}}, {"Unknown author", true, []string{"a2"}}, {"ada", false, []string{"a3"}}, {"Unknown author", false, []string{"b2"}}, {" Ada ", false, []string{"b3"}}, {" ", false, []string{"f1"}},
		}},
	} {
		t.Run(string(tc.by), func(t *testing.T) {
			document := groupingDocument()
			originals := map[string]GroupedCommit{}
			for _, component := range document.Components {
				for _, commit := range component.Commits {
					originals[commit.SHA] = GroupedCommit{Repository: component.Project, Commit: commit}
				}
			}
			groups := GroupCommits(document, tc.by)
			if len(groups) != len(tc.want) {
				t.Fatalf("groups=%+v want=%+v", groups, tc.want)
			}
			seen := map[string]bool{}
			for i, group := range groups {
				want := tc.want[i]
				if group.Value != want.value || group.Missing != want.missing {
					t.Errorf("group %d=%+v want=%+v", i, group, want)
				}
				var shas []string
				for _, item := range group.Commits {
					shas = append(shas, item.Commit.SHA)
					if seen[item.Commit.SHA] || !reflect.DeepEqual(item, originals[item.Commit.SHA]) {
						t.Fatalf("commit duplicated, changed, or detached from repository: %+v", item)
					}
					seen[item.Commit.SHA] = true
				}
				if !reflect.DeepEqual(shas, want.shas) {
					t.Errorf("group %q commit order=%v want=%v", group.Value, shas, want.shas)
				}
			}
			if len(seen) != len(originals) {
				t.Fatalf("grouping dropped commits: retained=%d original=%d", len(seen), len(originals))
			}
		})
	}
}

func TestGroupingDoesNotDeduplicateSharedCommitSHAsAcrossRepositories(t *testing.T) {
	document := Document{Components: []Component{
		{Project: "team/a", Commits: []Commit{{SHA: "shared", Remarks: "Same deployment"}}},
		{Project: "team/b", Commits: []Commit{{SHA: "shared", Remarks: "Same deployment"}}},
	}}
	groups := GroupCommits(document, GroupRemarks)
	if len(groups) != 1 || len(groups[0].Commits) != 2 || groups[0].Commits[0].Repository != "team/a" || groups[0].Commits[1].Repository != "team/b" {
		t.Fatalf("shared SHA lost repository provenance: %+v", groups)
	}
}

func TestParseGroupByAcceptsOnlyExactOptions(t *testing.T) {
	for _, mode := range []GroupBy{GroupRepository, GroupChangeRequest, GroupIncident, GroupRemarks, GroupAuthor} {
		if got, err := ParseGroupBy(string(mode)); err != nil || got != mode {
			t.Errorf("ParseGroupBy(%q)=%q error=%v", mode, got, err)
		}
	}
	if got, err := ParseGroupBy(""); err != nil || got != GroupRepository {
		t.Fatalf("default grouping=%q error=%v", got, err)
	}
	for _, value := range []string{"Repository", "change_request", "remarks ", " remarks", "authors", "unknown"} {
		if got, err := ParseGroupBy(value); err == nil || got != "" {
			t.Errorf("invalid grouping %q accepted: %q error=%v", value, got, err)
		}
	}
}
