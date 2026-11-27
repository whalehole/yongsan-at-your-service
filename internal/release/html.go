package release

import (
	_ "embed"
	"html/template"
	"io"
	"net/url"
	"strconv"
	"strings"
)

//go:embed release.html.tmpl
var releaseHTML string

type htmlGrouping struct {
	By       GroupBy
	Label    string
	Selected bool
	Groups   []CommitGroup
}

type htmlRepository struct {
	Component
	Label, Class string
}

type htmlStatistics struct {
	Repositories, Commits, ChangeRequests, Incidents int
	Changed, Unchanged, Failed                       int
}

// RenderHTML produces a standalone release document. All input text is escaped;
// the embedded script switches already-rendered views without interpolating it.
func RenderHTML(writer io.Writer, document Document, by GroupBy) error {
	by, err := ParseGroupBy(string(by))
	if err != nil {
		return err
	}
	data := struct {
		Document
		Title, InitialLabel string
		Views               []htmlGrouping
		Repositories        []htmlRepository
		Stats               htmlStatistics
	}{Document: document, Title: document.Release.Title}
	if data.Title == "" {
		data.Title = "Release " + document.Release.Version
		if document.Release.Version == "" {
			data.Title = "Release notes"
		}
	}
	changeRequests, incidents := map[int64]bool{}, map[int64]bool{}
	data.Stats.Repositories = len(document.Components)
	for _, component := range document.Components {
		repository := htmlRepository{Component: component}
		switch component.Status {
		case "changed":
			data.Stats.Changed++
			repository.Label, repository.Class = "New commits", "complete"
		case "unchanged":
			data.Stats.Unchanged++
			repository.Label, repository.Class = "No new commits", "neutral"
		default:
			data.Stats.Failed++
			repository.Label, repository.Class = "Incomplete", "incomplete"
		}
		data.Repositories = append(data.Repositories, repository)
		data.Stats.Commits += len(component.Commits)
		for _, commit := range component.Commits {
			if commit.ChangeRequest != nil {
				changeRequests[*commit.ChangeRequest] = true
			}
			if commit.Incident != nil {
				incidents[*commit.Incident] = true
			}
		}
	}
	data.Stats.ChangeRequests, data.Stats.Incidents = len(changeRequests), len(incidents)
	for _, view := range []struct {
		by    GroupBy
		label string
	}{
		{GroupRepository, "Repository"}, {GroupChangeRequest, "Change request"},
		{GroupIncident, "Incident"}, {GroupRemarks, "Remarks"}, {GroupAuthor, "Author"},
	} {
		selected := view.by == by
		if selected {
			data.InitialLabel = view.label
		}
		data.Views = append(data.Views, htmlGrouping{By: view.by, Label: view.label, Selected: selected, Groups: GroupCommits(document, view.by)})
	}
	return releaseHTMLTemplate.Execute(writer, data)
}

func safeCommitURL(value string) string {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" || parsed.User != nil || (!strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http")) {
		return ""
	}
	return value
}

var releaseHTMLTemplate = template.Must(template.New("release-notes").Funcs(template.FuncMap{
	"safeURL": safeCommitURL,
	"shortSHA": func(value string) string {
		if len(value) > 12 {
			return value[:12]
		}
		return value
	},
	"eventID": func(value *int64) string {
		if value == nil {
			return "Not linked"
		}
		return strconv.FormatInt(*value, 10)
	},
}).Parse(releaseHTML))
