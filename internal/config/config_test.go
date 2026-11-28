package config

import (
	"os"
	"strings"
	"testing"
)

func mustParse(t *testing.T, input string) *Config {
	t.Helper()
	c, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaultsAndEmptyOperationLists(t *testing.T) {
	c := mustParse(t, "version: 1\n")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.GitLab.URL != "https://gitlab.com" || c.GitLab.TokenEnv != "GITLAB_TOKEN" || c.GitLab.InsecureSkipVerify {
		t.Fatalf("unexpected GitLab defaults: %+v", c.GitLab)
	}
	if c.Tagging.Concurrency != 8 || c.Tagging.Timeout != "2m" || c.Building.Concurrency != 2 || c.Building.Timeout != "30m" || c.Release.Concurrency != 8 || c.Release.Timeout != "2m" || c.ImageDeletion.Concurrency != 4 {
		t.Fatalf("unexpected execution defaults: %+v", c)
	}
	if c.Building.Defaults.Context != "." || c.Building.Defaults.Dockerfile != "Dockerfile" || c.Building.Defaults.NoCache == nil || *c.Building.Defaults.NoCache {
		t.Fatalf("unexpected build defaults: %+v", c.Building.Defaults)
	}
	if c.Reports.AutoOpen {
		t.Fatal("report opening must be disabled by default")
	}
}

func TestReportsAutoOpenConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		want           bool
	}{
		{name: "omitted"},
		{name: "empty section", settings: "reports: {}\n"},
		{name: "enabled", settings: "reports:\n  auto_open: true\n", want: true},
		{name: "disabled", settings: "reports:\n  auto_open: false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, "version: 1\n"+tc.settings)
			if c.Reports.AutoOpen != tc.want {
				t.Fatalf("reports.auto_open=%v, want %v", c.Reports.AutoOpen, tc.want)
			}
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, settings := range []string{"auto_open: 1", "auto_open: enabled", "auto_open: [true]", "auto_open: {enabled: true}", "auto_opne: true"} {
		if _, err := Parse(strings.NewReader("version: 1\nreports:\n  " + settings + "\n")); err == nil {
			t.Fatalf("invalid report configuration was accepted: %s", settings)
		}
	}
}

func TestExampleValidates(t *testing.T) {
	c := mustParse(t, Example)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Tagging.Repositories) != 2 || len(c.Building.Repositories) != 2 || len(c.Release.Repositories) != 2 {
		t.Fatal("example should demonstrate independent repository lists")
	}
}

func TestBuildImageMayBeOmitted(t *testing.T) {
	c := mustParse(t, "version: 1\nbuilding:\n  repositories:\n    - project: group/service\n      source: {tag: v1}\n")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestImagePrefixConfigValidation(t *testing.T) {
	c := mustParse(t, "version: 1\nbuilding:\n  image_prefixes: [registry.example.com/team, 'localhost:5000/test']\n")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "https://registry.example.com", "team:latest", "../team", "registry.example.com//team"} {
		c.Building.ImagePrefixes = []string{prefix}
		if err := c.ValidateBuilding(); err == nil {
			t.Errorf("accepted %q", prefix)
		}
	}
}

func TestBatchBranchIsRuntimeOnly(t *testing.T) {
	c := mustParse(t, "version: 1\ntagging:\n  defaults:\n    tag: v1\n  repositories:\n    - project: group/service\n")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(strings.NewReader("version: 1\ntagging:\n  repositories:\n    - project: group/service\n      branch: main\n")); err == nil {
		t.Fatal("obsolete YAML branch was accepted")
	}
}

func TestDirectTokenConfiguration(t *testing.T) {
	c := mustParse(t, "version: 1\ngitlab:\n  token: configured-token\n  token_env: ''\n")
	if err := c.ValidateCommon(); err != nil {
		t.Fatal(err)
	}
	if c.GitLab.Token != "configured-token" {
		t.Fatal("configured token was not decoded")
	}
	c.GitLab.Token = "invalid\nvalue"
	if err := c.ValidateCommon(); err == nil {
		t.Fatal("newline in token was accepted")
	}
}

func TestGitLabTLSVerificationIsSkippedOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          bool
	}{
		{name: "omitted"},
		{name: "explicit false", setting: "  insecure_skip_verify: false\n"},
		{name: "explicit true", setting: "  insecure_skip_verify: true\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, "version: 1\ngitlab:\n  url: https://gitlab.example.com\n"+tc.setting)
			if err := c.ValidateCommon(); err != nil {
				t.Fatal(err)
			}
			if c.GitLab.InsecureSkipVerify != tc.want {
				t.Fatalf("insecure_skip_verify=%v want=%v", c.GitLab.InsecureSkipVerify, tc.want)
			}
		})
	}
}

func TestGitLabTLSVerificationSettingRejectsNonBooleanValues(t *testing.T) {
	for _, value := range []string{"1", "enabled", "[true]", "{enabled: true}"} {
		t.Run(value, func(t *testing.T) {
			_, err := Parse(strings.NewReader("version: 1\ngitlab:\n  insecure_skip_verify: " + value + "\n"))
			if err == nil {
				t.Fatal("non-boolean insecure_skip_verify value was accepted")
			}
		})
	}
}

func TestStrictYAML(t *testing.T) {
	for name, input := range map[string]string{
		"empty":                 "",
		"empty document":        "---\n",
		"null":                  "null\n",
		"unknown root":          "version: 1\nunexpected: true\n",
		"unknown nested":        "version: 1\nbuilding:\n  defaults:\n    docker_file: Dockerfile\n",
		"unknown entry":         "version: 1\ntagging:\n  repositories:\n    - project: group/project\n      typo: true\n",
		"unknown release field": "version: 1\nrelease:\n  defaults: {}\n",
		"unknown release entry": "version: 1\nrelease:\n  repositories:\n    - project: group/project\n      branch: main\n",
		"multiple documents":    "version: 1\n---\nversion: 1\n",
		"duplicate fields":      "version: 1\nversion: 2\n",
		"malformed":             "version: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestExplicitZeroSettingsAreRejected(t *testing.T) {
	c := mustParse(t, "version: 1\ntagging:\n  concurrency: 0\n")
	if err := c.ValidateTagging(); err == nil {
		t.Fatal("explicit zero concurrency must not become a default")
	}
	for _, input := range []string{
		"version: 1\nbuilding:\n  concurrency: -1\n",
		"version: 1\nbuilding:\n  timeout: 0s\n",
		"version: 1\ntagging:\n  timeout: tomorrow\n",
	} {
		if err := mustParse(t, input).Validate(); err == nil {
			t.Fatalf("expected invalid execution settings: %s", input)
		}
	}
}

func TestImageDeletionConcurrencyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		want           int
		valid          bool
	}{
		{name: "omitted", want: 4, valid: true},
		{name: "empty section", settings: "image_deletion: {}\n", want: 4, valid: true},
		{name: "serial", settings: "image_deletion:\n  concurrency: 1\n", want: 1, valid: true},
		{name: "larger batch", settings: "image_deletion:\n  concurrency: 64\n", want: 64, valid: true},
		{name: "zero", settings: "image_deletion:\n  concurrency: 0\n"},
		{name: "negative", settings: "image_deletion:\n  concurrency: -2\n", want: -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, "version: 1\n"+tc.settings)
			if c.ImageDeletion.Concurrency != tc.want {
				t.Fatalf("image_deletion.concurrency=%d, want %d", c.ImageDeletion.Concurrency, tc.want)
			}
			for _, validate := range []func() error{c.ValidateImageDeletion, c.Validate} {
				err := validate()
				if tc.valid && err != nil {
					t.Fatal(err)
				}
				if !tc.valid && (err == nil || !strings.Contains(err.Error(), "image_deletion.concurrency")) {
					t.Fatalf("invalid concurrency was not identified: %v", err)
				}
			}
		})
	}
	for _, value := range []string{"many", "1.5", "true", "[4]", "{workers: 4}"} {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(strings.NewReader("version: 1\nimage_deletion:\n  concurrency: " + value + "\n")); err == nil {
				t.Fatal("non-integer concurrency was accepted")
			}
		})
	}
	for _, settings := range []string{
		"tagging: {concurrency: &workers 1.5}\nimage_deletion: {concurrency: *workers}\n",
		"image_deletion:\n  <<: {concurrency: 1.5}\n",
		"image_deletion:\n  <<: &defaults {concurrency: 1.5}\n",
		"image_deletion:\n  <<: [{concurrency: 1.5}]\n",
	} {
		if _, err := Parse(strings.NewReader("version: 1\n" + settings)); err == nil || !strings.Contains(err.Error(), "image_deletion.concurrency must be an integer") {
			t.Fatalf("fractional YAML value was accepted: %q (%v)", settings, err)
		}
	}
}

func TestCommonValidation(t *testing.T) {
	for _, input := range []string{
		"gitlab: {}\n",
		"version: 2\n",
		"version: 1\ngitlab:\n  url: ftp://example.com\n",
		"version: 1\ngitlab:\n  url: https://user:secret@example.com\n",
		"version: 1\ngitlab:\n  url: https://example.com?token=secret\n",
		"version: 1\ngitlab:\n  token_env: BAD-NAME\n",
	} {
		if err := mustParse(t, input).ValidateCommon(); err == nil {
			t.Fatalf("expected common validation failure: %s", input)
		}
	}
	c := mustParse(t, "version: 1\ngitlab:\n  url: http://gitlab.example.com:8080/gitlab\n")
	if err := c.ValidateCommon(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeBuildOptionsPrecedenceAndIndependence(t *testing.T) {
	base := BuildOptions{Context: ".", Dockerfile: "Dockerfile", NoCache: boolPointer(true), Pull: boolPointer(true), BuildArgs: map[string]string{"ENV": "production", "VERSION": "1"}}
	override := BuildOptions{Context: "services/api", NoCache: boolPointer(false), BuildArgs: map[string]string{"ENV": "staging", "EXTRA": "yes"}}
	merged := MergeBuildOptions(base, override)
	if merged.Context != "services/api" || merged.Dockerfile != "Dockerfile" || *merged.NoCache || !*merged.Pull || merged.BuildArgs["ENV"] != "staging" || merged.BuildArgs["VERSION"] != "1" || merged.BuildArgs["EXTRA"] != "yes" {
		t.Fatalf("unexpected merged options: %+v", merged)
	}
	merged.BuildArgs["ENV"] = "changed"
	*merged.NoCache = true
	*merged.Pull = false
	if base.BuildArgs["ENV"] != "production" || override.BuildArgs["ENV"] != "staging" || *override.NoCache || !*base.Pull {
		t.Fatal("merge mutated inherited maps or pointers")
	}
}

func TestYAMLInlineBuildOverrides(t *testing.T) {
	c := mustParse(t, `version: 1
building:
  defaults:
    no_cache: true
    pull: true
    build_args:
      APP_ENV: production
      VERSION: "1"
  repositories:
    - project: 12345
      source: {tag: v1}
      image: service:1
      context: services/api
      no_cache: false
      build_args:
        APP_ENV: staging
`)
	if err := c.ValidateBuilding(); err != nil {
		t.Fatal(err)
	}
	entry := c.Building.Repositories[0]
	options := MergeBuildOptions(c.Building.Defaults, entry.BuildOptions)
	if entry.Project != "12345" || options.Context != "services/api" || *options.NoCache || !*options.Pull || options.BuildArgs["APP_ENV"] != "staging" || options.BuildArgs["VERSION"] != "1" {
		t.Fatalf("YAML overrides were not preserved: %+v, %+v", entry, options)
	}
}

func TestTagValidation(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, entry := range []TagRepository{
		{Project: "group/subgroup/project", Branch: "release/1.2", Tag: "v1.2.0"},
		{Project: "12345", Commit: sha, Tag: "v1.2.0"},
		{Project: "group/project", Commit: sha, Branch: "main", Tag: "v1.2.0"},
	} {
		if err := ValidateTag(entry); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []TagRepository{
		{Project: "group/project", Tag: "v1"},
		{Project: "group/project", Branch: "main"},
		{Project: "group/project", Commit: "abc123", Tag: "v1"},
		{Project: "https://gitlab.com/group/project", Branch: "main", Tag: "v1"},
		{Project: "group/project", Branch: "bad..branch", Tag: "v1"},
	} {
		if err := ValidateTag(entry); err == nil {
			t.Fatalf("expected invalid tag entry: %+v", entry)
		}
	}
}

func TestReferenceAndProjectValidation(t *testing.T) {
	for _, value := range []string{"", "@", "/v1", "v1/", "v1.", "bad..ref", "bad@{ref", ".hidden", "x/.hidden", "x.lock", "x.lock/y", "x//y", "x y", "x\ny", "x~y", "x^y", "x:y", "x?y", "x*y", "x[y", "x\\y"} {
		if err := ValidateGitRef(value); err == nil {
			t.Errorf("expected invalid Git reference %q", value)
		}
	}
	for _, value := range []string{"0", "-1", "project", "../project", "group/../project", "/group/project", "group/project/", "group//project", "https://example.com/group/project"} {
		if err := ValidateProject(value); err == nil {
			t.Errorf("expected invalid project %q", value)
		}
	}
}

func TestConfiguredGroups(t *testing.T) {
	c := mustParse(t, "version: 1\ngitlab:\n  groups: [company, company/team]\n")
	if err := c.ValidateCommon(); err != nil {
		t.Fatal(err)
	}
	if len(c.GitLab.Groups) != 2 {
		t.Fatal("groups not decoded")
	}
	for _, group := range []string{"", "../bad", "company/", "https://gitlab.com/company"} {
		c.GitLab.Groups = []string{group}
		if err := c.ValidateCommon(); err == nil {
			t.Errorf("accepted group %q", group)
		}
	}
}

func TestAbbreviatedCommitValidation(t *testing.T) {
	for _, sha := range []string{"f63850b", "F63850B123", strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		if err := ValidateCommit(sha); err != nil {
			t.Errorf("%q: %v", sha, err)
		}
	}
	for _, sha := range []string{"abc123", "not-a-hash", strings.Repeat("a", 41), strings.Repeat("b", 65)} {
		if err := ValidateCommit(sha); err == nil {
			t.Errorf("accepted %q", sha)
		}
	}
}

func TestDuplicateTagDestinationAndImage(t *testing.T) {
	c := mustParse(t, `version: 1
tagging:
  defaults:
    tag: v1
  repositories:
    - project: group/project
    - project: group/project
building:
  repositories:
    - project: group/a
      source: {tag: v1}
      image: example:1
    - project: group/b
      source: {tag: v2}
      image: example:1
`)
	if err := c.ValidateTagging(); err == nil {
		t.Fatal("duplicate project/tag destinations must fail")
	}
	if err := c.ValidateBuilding(); err == nil {
		t.Fatal("concurrent builds must not write the same image tag")
	}
}

func TestBuildSourceAndOptionsValidation(t *testing.T) {
	valid := BuildRepository{Project: "group/project", Source: Source{Tag: "v1"}, Image: "registry.example.com:5000/team/api:1.2.0", BuildOptions: BuildOptions{Context: "services/api", Dockerfile: "docker/Dockerfile"}}
	if err := ValidateBuild(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*BuildRepository){
		"no source":            func(e *BuildRepository) { e.Source = Source{} },
		"ambiguous source":     func(e *BuildRepository) { e.Source.Commit = strings.Repeat("a", 40) },
		"short SHA":            func(e *BuildRepository) { e.Source = Source{Commit: "abc123"} },
		"context escape":       func(e *BuildRepository) { e.Context = "../api" },
		"context absolute":     func(e *BuildRepository) { e.Context = "/services/api" },
		"dockerfile escape":    func(e *BuildRepository) { e.Dockerfile = "../Dockerfile" },
		"dockerfile directory": func(e *BuildRepository) { e.Dockerfile = "." },
		"Windows path":         func(e *BuildRepository) { e.Context = `C:\services\api` },
		"missing image tag":    func(e *BuildRepository) { e.Image = "registry.example.com:5000/team/api" },
		"uppercase repository": func(e *BuildRepository) { e.Image = "Team/API:1" },
		"bad argument":         func(e *BuildRepository) { e.BuildArgs = map[string]string{"BAD-NAME": "value"} },
	} {
		t.Run(name, func(t *testing.T) {
			entry := valid
			mutate(&entry)
			if err := ValidateBuild(entry); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestReleaseConfigurationIsIndependent(t *testing.T) {
	c := mustParse(t, `version: 1
tagging:
  repositories:
    - project: tag-group/tag-only
building:
  repositories:
    - project: build-group/build-only
release:
  concurrency: 4
  timeout: 45s
  repositories:
    - project: release-group/service
    - project: 12345
`)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Release.Concurrency != 4 || c.Release.Timeout != "45s" || len(c.Release.Repositories) != 2 || c.Release.Repositories[0].Project != "release-group/service" || c.Release.Repositories[1].Project != "12345" {
		t.Fatalf("release configuration was not preserved independently: %+v", c.Release)
	}
	c.Tagging.Concurrency = 0
	c.Building.Defaults.Context = "../invalid"
	if err := c.ValidateRelease(); err != nil {
		t.Fatalf("release validation depends on tagging/building settings: %v", err)
	}
}

func TestReleaseOnlyConfigurationAndLegacyCompatibility(t *testing.T) {
	for _, input := range []string{
		"version: 1\n",
		"version: 1\ntagging:\n  repositories:\n    - project: group/tag-only\nbuilding:\n  repositories:\n    - project: group/build-only\n",
	} {
		c := mustParse(t, input)
		if err := c.Validate(); err != nil {
			t.Fatalf("legacy configuration rejected: %v", err)
		}
		if c.Release.Concurrency != 8 || c.Release.Timeout != "2m" || len(c.Release.Repositories) != 0 {
			t.Fatalf("release inherited legacy operation repositories: %+v", c.Release)
		}
	}
	c := mustParse(t, "version: 1\nrelease:\n  repositories:\n    - project: release-group/service\n")
	if err := c.Validate(); err != nil || len(c.Tagging.Repositories) != 0 || len(c.Building.Repositories) != 0 || len(c.Release.Repositories) != 1 {
		t.Fatalf("release-only configuration rejected or linked to other operations: %+v; %v", c, err)
	}
	c.Release.Repositories = nil
	if err := c.ValidateRelease(); err != nil {
		t.Fatalf("empty release list must be valid until a scan is requested: %v", err)
	}
}

func TestReleaseValidationRejectsInvalidSettingsAndProjects(t *testing.T) {
	for name, content := range map[string]string{
		"zero concurrency":     "  concurrency: 0\n",
		"negative concurrency": "  concurrency: -1\n",
		"zero timeout":         "  timeout: 0s\n",
		"negative timeout":     "  timeout: -1m\n",
		"invalid timeout":      "  timeout: tomorrow\n",
		"empty project":        "  repositories:\n    - project: ''\n",
		"invalid project":      "  repositories:\n    - project: ../service\n",
		"duplicate project":    "  repositories:\n    - project: group/service\n    - project: group/service\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := mustParse(t, "version: 1\nrelease:\n"+content)
			if err := c.ValidateRelease(); err == nil || !strings.Contains(err.Error(), "release") {
				t.Fatalf("invalid release configuration accepted: %v", err)
			}
			if err := c.Validate(); err == nil {
				t.Fatal("global validation ignored release configuration")
			}
			if err := c.ValidateTagging(); err != nil {
				t.Fatalf("tagging validation depends on release settings: %v", err)
			}
			if err := c.ValidateBuilding(); err != nil {
				t.Fatalf("building validation depends on release settings: %v", err)
			}
		})
	}
}

func TestExampleConfigurationMirrorsEmbeddedStarter(t *testing.T) {
	data, err := os.ReadFile("../../yas.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Example {
		t.Fatal("yas.example.yaml and the embedded config init starter differ")
	}
	c := mustParse(t, string(data))
	if err := c.ValidateRelease(); err != nil || len(c.Release.Repositories) != 2 {
		t.Fatalf("release example invalid: %+v; %v", c.Release, err)
	}
}
