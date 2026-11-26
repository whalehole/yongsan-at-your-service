package release

import (
	"fmt"
	"sort"
	"strings"
)

// Decimal strings avoid imposing a machine-integer limit on SemVer versions.
type semVersion struct {
	major, minor, patch, prerelease string
}

func parseVersion(tag string) (semVersion, bool) {
	var version semVersion
	text := strings.TrimPrefix(tag, "v")
	if base, metadata, ok := strings.Cut(text, "+"); ok {
		if !versionIdentifiers(metadata, false) {
			return version, false
		}
		text = base
	}
	if base, prerelease, ok := strings.Cut(text, "-"); ok {
		if !versionIdentifiers(prerelease, true) {
			return version, false
		}
		text, version.prerelease = base, prerelease
	}
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return version, false
	}
	for _, part := range parts {
		if !decimal(part) || len(part) > 1 && part[0] == '0' {
			return version, false
		}
	}
	version.major, version.minor, version.patch = parts[0], parts[1], parts[2]
	return version, true
}

func versionIdentifiers(value string, rejectLeadingZeros bool) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		for _, char := range identifier {
			if !('0' <= char && char <= '9' || 'a' <= char && char <= 'z' || 'A' <= char && char <= 'Z' || char == '-') {
				return false
			}
		}
		if rejectLeadingZeros && decimal(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func stableTarget(tag string) (semVersion, error) {
	version, ok := parseVersion(tag)
	if !ok {
		return version, fmt.Errorf("target tag %q is not a SemVer tag; supply an explicit baseline tag", tag)
	}
	if version.prerelease != "" {
		return version, fmt.Errorf("automatic previous-tag selection supports stable targets only; target tag %q is a prerelease, so supply an explicit baseline tag", tag)
	}
	return version, nil
}

func previousTag(target string, tags []string) (string, error) {
	version, err := stableTarget(target)
	if err != nil {
		return "", err
	}
	var best semVersion
	var matches []string
	seen := map[string]bool{}
	for _, tag := range tags {
		candidate, valid := parseVersion(tag)
		if !valid || candidate.prerelease != "" || candidate.major != version.major || candidate.minor != version.minor || compareDecimal(candidate.patch, version.patch) >= 0 || seen[tag] {
			continue
		}
		seen[tag] = true
		comparison := 1
		if len(matches) > 0 {
			comparison = compareDecimal(candidate.patch, best.patch)
		}
		if comparison > 0 {
			best, matches = candidate, []string{tag}
		} else if comparison == 0 {
			matches = append(matches, tag)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no existing stable SemVer tag below %q in the same %s.%s release track; supply an explicit baseline tag", target, version.major, version.minor)
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", fmt.Errorf("previous tag for %q is ambiguous: tags %s have the same SemVer precedence; supply an explicit baseline tag", target, strings.Join(matches, ", "))
	}
	return matches[0], nil
}

func compareDecimal(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}
