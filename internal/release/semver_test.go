package release

import (
	"strings"
	"testing"
)

func TestPreviousTagUsesSemVerPrecedenceAndExistingSameTrackTags(t *testing.T) {
	tests := []struct {
		name, target, want string
		tags               []string
	}{
		{name: "numeric ordering and gaps", target: "v1.2.111", tags: []string{"v1.2.9", "v1.2.100", "v1.2.20"}, want: "v1.2.100"},
		{name: "stable track only", target: "1.2.10", tags: []string{"1.1.999", "1.3.0", "2.0.0", "1.2.11", "1.2.9-rc.1", "1.2.4", "date-2026-11-26"}, want: "1.2.4"},
		{name: "optional prefix", target: "v1.2.10", tags: []string{"1.2.9", "v1.2.3"}, want: "1.2.9"},
		{name: "target build metadata", target: "1.2.10+release.03", tags: []string{"v1.2.9+build.04", "1.2.8"}, want: "v1.2.9+build.04"},
		{name: "invalid tags ignored", target: "1.2.10", tags: []string{"1.2", "01.2.9", "1.2.09", "1.2.8-01", "1.2.7+", "1.2.6", "latest"}, want: "1.2.6"},
		{name: "duplicate tag name", target: "v1.2.10", tags: []string{"v1.2.9", "v1.2.9"}, want: "v1.2.9"},
		{name: "older ambiguous versions do not defeat unique newest", target: "v1.2.10", tags: []string{"1.2.1", "v1.2.1", "1.2.9"}, want: "1.2.9"},
		{name: "unbounded numeric version", target: "1.2.999999999999999999999999999999", tags: []string{"1.2.9999999999999999999999999999", "1.2.888888888888888888888888888888"}, want: "1.2.888888888888888888888888888888"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := previousTag(test.target, test.tags)
			if err != nil || got != test.want {
				t.Fatalf("previousTag(%q, %v) = %q, %v; want %q", test.target, test.tags, got, err, test.want)
			}
		})
	}
}

func TestPreviousTagRequiresExplicitBaselineWhenSelectionIsUnsafe(t *testing.T) {
	tests := []struct {
		target, errorText string
		tags              []string
	}{
		{target: "v1.2.10", tags: []string{"v1.2.9", "1.2.9"}, errorText: "ambiguous"},
		{target: "v1.2.10", tags: []string{"1.2.9+first", "1.2.9+second"}, errorText: "ambiguous"},
		{target: "1.2.0", tags: []string{"1.1.999", "1.2.0", "1.2.0-rc.1"}, errorText: "same 1.2 release track"},
		{target: "0.0.0", tags: []string{"1.0.0"}, errorText: "no existing stable"},
		{target: "release-20261126", tags: []string{"v1.0.0"}, errorText: "not a SemVer"},
		{target: "v1.2.10-rc.2", tags: []string{"v1.2.9", "v1.2.10-rc.1"}, errorText: "stable targets only"},
	}
	for _, test := range tests {
		got, err := previousTag(test.target, test.tags)
		if err == nil || got != "" || !strings.Contains(err.Error(), test.errorText) || !strings.Contains(err.Error(), "explicit baseline") {
			t.Fatalf("previousTag(%q, %v) = %q, %v", test.target, test.tags, got, err)
		}
	}
}

func TestParseVersionStrictSemVerIdentifiers(t *testing.T) {
	for _, tag := range []string{"0.0.0", "v1.2.3", "1.2.3-rc.1", "1.2.3-rc.1+build.01", "1.2.3+build-with-hyphen", "100000000000000000000000.2.3"} {
		if _, valid := parseVersion(tag); !valid {
			t.Errorf("rejected valid SemVer %q", tag)
		}
	}
	for _, tag := range []string{"V1.2.3", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-rc..1", "1.2.3+build+again", " 1.2.3", "1.2.3/extra"} {
		if _, valid := parseVersion(tag); valid {
			t.Errorf("accepted invalid SemVer %q", tag)
		}
	}
}
