package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writePickerDraft(t *testing.T, directory, name string, modified time.Time) string {
	t.Helper()
	path := filepath.Join(directory, name)
	// Discovery must not parse or require a valid release document.
	if err := os.WriteFile(path, []byte("deliberately invalid YAML: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecentReleaseDraftsSortsByModificationTimeBeforeLimiting(t *testing.T) {
	directory := t.TempDir()
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		writePickerDraft(t, directory, fmt.Sprintf("release-%02d.yaml", i), base.Add(time.Duration(i)*time.Second))
	}
	candidates, err := recentReleaseDrafts(directory)
	if err != nil || len(candidates) != 10 {
		t.Fatalf("candidates=%v error=%v", candidates, err)
	}
	for i, candidate := range candidates {
		index := 11 - i
		name := fmt.Sprintf("release-%02d.yaml", index)
		if candidate.Name != name || candidate.Path != filepath.Join(directory, name) || !filepath.IsAbs(candidate.Path) || !candidate.ModifiedAt.Equal(base.Add(time.Duration(index)*time.Second)) {
			t.Errorf("candidate %d=%+v; want %s", i, candidate, name)
		}
	}
}

func TestRecentReleaseDraftsUsesLexicalNamesForEqualModificationTimes(t *testing.T) {
	directory := t.TempDir()
	modified := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"zeta.YAML", "alpha.yaml", "Beta.yml"} {
		writePickerDraft(t, directory, name, modified)
	}
	candidates, err := recentReleaseDrafts(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, candidate := range candidates {
		names = append(names, candidate.Name)
	}
	if !reflect.DeepEqual(names, []string{"Beta.yml", "alpha.yaml", "zeta.YAML"}) {
		t.Fatalf("lexical tie order=%v", names)
	}
}

func TestRecentReleaseDraftsIncludesOnlyDirectRegularYAMLFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), `drafts "for review"`)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	wantNames := []string{`release "quoted name".YAML`, "release with spaces.YmL"}
	for _, name := range append(append([]string{}, wantNames...), "release.yaml.bak", "release.yml.txt", "notes.html") {
		writePickerDraft(t, directory, name, modified)
	}
	for _, name := range []string{"nested", "directory.yaml"} {
		child := filepath.Join(directory, name)
		if err := os.Mkdir(child, 0700); err != nil {
			t.Fatal(err)
		}
		writePickerDraft(t, child, "nested-release.yaml", modified.Add(time.Hour))
	}
	candidates, err := recentReleaseDrafts(directory)
	if err != nil || len(candidates) != len(wantNames) {
		t.Fatalf("discovery recursed, parsed contents, or included unsupported files: %+v (%v)", candidates, err)
	}
	for i, candidate := range candidates {
		if candidate.Name != wantNames[i] || candidate.Path != filepath.Join(directory, wantNames[i]) {
			t.Errorf("file name/path changed: %+v", candidate)
		}
		data, err := os.ReadFile(candidate.Path)
		if err != nil || string(data) != "deliberately invalid YAML: [\n" {
			t.Errorf("discovery changed or rejected draft contents: %q (%v)", data, err)
		}
	}
}

func TestRecentReleaseDraftsExcludesSymlinks(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	target := writePickerDraft(t, outside, "external.yaml", time.Now())
	if err := os.Symlink(target, filepath.Join(directory, "linked.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "linked-directory.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing.yaml"), filepath.Join(directory, "dangling.YAML")); err != nil {
		t.Fatal(err)
	}
	candidates, err := recentReleaseDrafts(directory)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("symlink drafts should not be offered: %+v (%v)", candidates, err)
	}
}

func TestRecentReleaseDraftsMissingDirectoryIsEmptyButNonDirectoryIsAnError(t *testing.T) {
	directory := t.TempDir()
	candidates, err := recentReleaseDrafts(filepath.Join(directory, "missing"))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("missing directory=%+v (%v)", candidates, err)
	}
	file := writePickerDraft(t, directory, "file.yaml", time.Now())
	if candidates, err := recentReleaseDrafts(file); err == nil {
		t.Fatalf("non-directory unexpectedly accepted: %+v", candidates)
	}
}

func TestRecentReleaseDraftsReturnsDirectoryReadErrors(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	if _, err := os.ReadDir(directory); err == nil {
		t.Skip("directory permissions are not enforced for this account or platform")
	}
	if candidates, err := recentReleaseDrafts(directory); err == nil {
		t.Fatalf("unreadable directory unexpectedly accepted: %+v", candidates)
	}
}

func TestRecentReleaseDraftsSkipsEntriesWhoseMetadataCannotBeRead(t *testing.T) {
	directory := t.TempDir()
	path := writePickerDraft(t, directory, "unreadable.yaml", time.Now())
	if err := os.Chmod(directory, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	if _, err := os.ReadDir(directory); err != nil {
		t.Skipf("platform cannot list an unsearchable directory: %v", err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Skip("directory search permissions are not enforced for this account or platform")
	}
	candidates, err := recentReleaseDrafts(directory)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unreadable entry aborted discovery or was offered: %+v (%v)", candidates, err)
	}
}
