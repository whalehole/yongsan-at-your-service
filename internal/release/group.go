package release

import (
	"fmt"
	"strconv"
)

type GroupBy string

const (
	GroupRepository    GroupBy = "repository"
	GroupChangeRequest GroupBy = "change-request"
	GroupIncident      GroupBy = "incident"
	GroupRemarks       GroupBy = "remarks"
	GroupAuthor        GroupBy = "author"
)

func ParseGroupBy(value string) (GroupBy, error) {
	if value == "" {
		return GroupRepository, nil
	}
	group := GroupBy(value)
	switch group {
	case GroupRepository, GroupChangeRequest, GroupIncident, GroupRemarks, GroupAuthor:
		return group, nil
	default:
		return "", fmt.Errorf("invalid grouping %q; choose repository, change-request, incident, remarks, or author", value)
	}
}

type GroupedCommit struct {
	Repository string
	Commit     Commit
}

type CommitGroup struct {
	Value   string
	Missing bool
	Commits []GroupedCommit
}

// GroupCommits keeps every commit, in first-encounter group and commit order.
// Empty or unrecognized modes use repository grouping; ParseGroupBy validates
// user-facing options. Remarks and authors match exactly, without normalization.
func GroupCommits(document Document, by GroupBy) []CommitGroup {
	type key struct {
		value   string
		missing bool
	}
	groups := make([]CommitGroup, 0)
	indices := map[key]int{}
	for _, component := range document.Components {
		for _, commit := range component.Commits {
			group := key{value: component.Project}
			switch by {
			case GroupChangeRequest:
				group.value, group.missing = "No change request", commit.ChangeRequest == nil
				if !group.missing {
					group.value = strconv.FormatInt(*commit.ChangeRequest, 10)
				}
			case GroupIncident:
				group.value, group.missing = "No incident", commit.Incident == nil
				if !group.missing {
					group.value = strconv.FormatInt(*commit.Incident, 10)
				}
			case GroupRemarks:
				group.value = commit.Remarks
				if group.value == "" {
					group.value, group.missing = "No remarks", true
				}
			case GroupAuthor:
				group.value = commit.Author
				if group.value == "" {
					group.value, group.missing = "Unknown author", true
				}
			}
			index, exists := indices[group]
			if !exists {
				index = len(groups)
				indices[group] = index
				groups = append(groups, CommitGroup{Value: group.value, Missing: group.missing, Commits: []GroupedCommit{}})
			}
			groups[index].Commits = append(groups[index].Commits, GroupedCommit{Repository: component.Project, Commit: commit})
		}
	}
	return groups
}
