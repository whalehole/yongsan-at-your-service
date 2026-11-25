package builder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type member struct {
	name, content, link string
	kind                byte
	mode, size          int64
}

func makeArchive(t *testing.T, members ...member) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		kind := m.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := m.mode
		if mode == 0 {
			mode = 0644
		}
		size := int64(len(m.content))
		if kind != tar.TypeReg && kind != tar.TypeRegA {
			size = 0
		}
		if m.size != 0 {
			size = m.size
		}
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Typeflag: kind, Mode: mode, Size: size, Linkname: m.link}); err != nil {
			t.Fatal(err)
		}
		if size != 0 {
			if _, err := tw.Write([]byte(m.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestExtractArchiveStripsRootAndPreservesExecutable(t *testing.T) {
	dir := t.TempDir()
	data := makeArchive(t,
		member{name: "service-sha/", kind: tar.TypeDir},
		member{name: "service-sha/Dockerfile", content: "FROM scratch\n"},
		member{name: "service-sha/bin/run", content: "#!/bin/sh\n", mode: 0755},
	)
	root, err := extractArchive(context.Background(), bytes.NewReader(data), dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root != wantRoot {
		t.Fatalf("context root = %q, want %q", root, wantRoot)
	}
	content, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil || string(content) != "FROM scratch\n" {
		t.Fatalf("Dockerfile = %q, error = %v", content, err)
	}
	info, err := os.Stat(filepath.Join(root, "bin", "run"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode = %o", info.Mode().Perm())
	}
}

func TestExtractArchiveAcceptsGitGlobalPAXHeader(t *testing.T) {
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	// Match git archive's commit metadata before the repository prefix.
	if err := tw.WriteHeader(&tar.Header{
		Name:       "pax_global_header",
		Typeflag:   tar.TypeXGlobalHeader,
		PAXRecords: map[string]string{"comment": "0123456789012345678901234567890123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "service-sha/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	const dockerfile = "FROM scratch\n"
	if err := tw.WriteHeader(&tar.Header{Name: "service-sha/Dockerfile", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(dockerfile))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := extractArchive(context.Background(), bytes.NewReader(data.Bytes()), dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil || string(content) != dockerfile {
		t.Fatalf("Dockerfile = %q, error = %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "pax_global_header")); !os.IsNotExist(err) {
		t.Fatalf("global metadata was extracted as a file: %v", err)
	}
}

func TestExtractArchiveSubdirectoryLayouts(t *testing.T) {
	for _, layout := range []struct {
		name, archivePath, expectedRoot string
	}{
		{"preserved", "service-sha/services/api/Dockerfile", "services/api"},
		{"flattened", "service-sha/Dockerfile", "."},
	} {
		t.Run(layout.name, func(t *testing.T) {
			dir := t.TempDir()
			data := makeArchive(t, member{name: layout.archivePath, content: "FROM scratch\n"})
			root, err := extractArchive(context.Background(), bytes.NewReader(data), dir, "services/api")
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(layout.expectedRoot)))
			if err != nil {
				t.Fatal(err)
			}
			if root != want {
				t.Fatalf("root = %q, want %q", root, want)
			}
			if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtractArchiveRejectsUnsafeMembers(t *testing.T) {
	tests := []struct {
		name    string
		members []member
		context string
	}{
		{"parent traversal", []member{{name: "repo/../../escaped", content: "bad"}}, "."},
		{"absolute path", []member{{name: "/tmp/escaped", content: "bad"}}, "."},
		{"Windows path", []member{{name: `repo\..\escaped`, content: "bad"}}, "."},
		{"drive path", []member{{name: "C:/escaped", content: "bad"}}, "."},
		{"multiple roots", []member{{name: "repo/a", content: "a"}, {name: "other/b", content: "b"}}, "."},
		{"escaping symlink", []member{{name: "repo/link", kind: tar.TypeSymlink, link: "../escaped"}}, "."},
		{"absolute symlink", []member{{name: "repo/link", kind: tar.TypeSymlink, link: "/tmp/escaped"}}, "."},
		{"hard link", []member{{name: "repo/a", content: "a"}, {name: "repo/link", kind: tar.TypeLink, link: "repo/a"}}, "."},
		{"special file", []member{{name: "repo/device", kind: tar.TypeChar}}, "."},
		{"duplicate file", []member{{name: "repo/a", content: "a"}, {name: "repo/a", content: "b"}}, "."},
		{"link/file collision", []member{{name: "repo/link", kind: tar.TypeSymlink, link: "safe"}, {name: "repo/link/escaped", content: "bad"}}, "."},
		{"outside selected context", []member{{name: "repo/services/api/Dockerfile", content: "FROM scratch"}, {name: "repo/services/api/link", kind: tar.TypeSymlink, link: "../shared"}}, "services/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := extractArchive(context.Background(), bytes.NewReader(makeArchive(t, tt.members...)), dir, tt.context)
			if err == nil {
				t.Fatal("expected unsafe archive to be rejected")
			}
		})
	}
}

func TestExtractArchiveAllowsSafeSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	data := makeArchive(t,
		member{name: "repo/services/api/data/version", content: "1.2.0"},
		member{name: "repo/services/api/current", kind: tar.TypeSymlink, link: "data/version"},
	)
	root, err := extractArchive(context.Background(), bytes.NewReader(data), dir, "services/api")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "current"))
	if err != nil || string(content) != "1.2.0" {
		t.Fatalf("read safe link = %q, %v", content, err)
	}
}

func TestExtractArchiveRejectsChainedSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "context")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside"), []byte("must not be included"), 0600); err != nil {
		t.Fatal(err)
	}
	data := makeArchive(t,
		member{name: "repo/a", kind: tar.TypeSymlink, link: "."},
		member{name: "repo/b", kind: tar.TypeSymlink, link: "a/../outside"},
	)
	_, err := extractArchive(context.Background(), bytes.NewReader(data), dir, ".")
	if err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("error = %v, want chained symlink escape rejection", err)
	}
}

func TestExtractArchiveRejectsBrokenAndCyclicSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	for _, tt := range []struct {
		name    string
		members []member
	}{
		{"broken", []member{{name: "repo/a", kind: tar.TypeSymlink, link: "missing"}}},
		{"cyclic", []member{{name: "repo/a", kind: tar.TypeSymlink, link: "b"}, {name: "repo/b", kind: tar.TypeSymlink, link: "a"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := makeArchive(t, tt.members...)
			_, err := extractArchive(context.Background(), bytes.NewReader(data), t.TempDir(), ".")
			if err == nil || !strings.Contains(err.Error(), "broken or cyclic") {
				t.Fatalf("error = %v, want invalid symlink rejection", err)
			}
		})
	}
}

func TestExtractArchiveChecksGzipChecksum(t *testing.T) {
	data := makeArchive(t, member{name: "repo/Dockerfile", content: "FROM scratch"})
	data[len(data)-8] ^= 0xff
	_, err := extractArchive(context.Background(), bytes.NewReader(data), t.TempDir(), ".")
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %v, want gzip checksum failure", err)
	}
}

func TestExtractArchiveHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := extractArchive(ctx, bytes.NewReader(makeArchive(t, member{name: "repo/Dockerfile", content: "FROM scratch"})), t.TempDir(), ".")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCleanRelativeRejectsEscapes(t *testing.T) {
	for _, value := range []string{"../service", "a/../b", "/absolute", `C:\source`, `a\b`, "bad\x00path"} {
		if _, err := cleanRelative(value, true); err == nil {
			t.Errorf("cleanRelative(%q) unexpectedly succeeded", value)
		}
	}
}
