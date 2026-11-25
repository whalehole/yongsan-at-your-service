package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxCompressedBytes = int64(1 << 30) // 1 GiB downloaded archive.
	maxExpandedBytes   = int64(2 << 30) // 2 GiB including tar metadata/padding.
	maxFileBytes       = int64(1 << 30)
	maxArchiveEntries  = 100_000
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type archiveLink struct {
	name, target string
}

// GitLab archives have a single generated top-level directory, which is
// stripped here. Subdirectory archives can preserve the repository path or
// flatten it below that generated directory; both layouts are accepted.
func extractArchive(ctx context.Context, source io.Reader, destination, contextPath string) (string, error) {
	compressed := &io.LimitedReader{R: contextReader{ctx, source}, N: maxCompressedBytes + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: contextReader{ctx, gz}, N: maxExpandedBytes + 1}
	tr := tar.NewReader(expanded)
	var topLevel string
	var links []archiveLink
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return "", fmt.Errorf("archive exceeds %d entries", maxArchiveEntries)
		}
		// git archive emits a global PAX header containing the commit ID.
		// This is metadata, not an entry below GitLab's generated root.
		// It still counts toward entry and expanded-byte limits.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := archiveName(header.Name)
		if err != nil {
			return "", err
		}
		parts := strings.SplitN(name, "/", 2)
		if topLevel == "" {
			topLevel = parts[0]
		} else if topLevel != parts[0] {
			return "", errors.New("archive must contain one top-level directory")
		}
		if len(parts) == 1 {
			if header.Typeflag != tar.TypeDir {
				return "", errors.New("archive root must be a directory")
			}
			continue
		}
		rel := parts[1]
		target := filepath.Join(destination, filepath.FromSlash(rel))
		if !within(destination, target) {
			return "", fmt.Errorf("archive path %q escapes extraction directory", header.Name)
		}
		if err := ensureParents(destination, filepath.Dir(target)); err != nil {
			return "", err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureDirectory(target); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxFileBytes {
				return "", fmt.Errorf("archive file %q exceeds the 1 GiB file limit", header.Name)
			}
			// Never overwrite a path: duplicate members and link/file collisions
			// are rejected rather than depending on archive ordering.
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0777)
			if err != nil {
				return "", fmt.Errorf("create archive file %q: %w", rel, err)
			}
			_, copyErr := io.Copy(file, tr)
			closeErr := file.Close()
			if copyErr != nil {
				return "", fmt.Errorf("extract archive file %q: %w", rel, copyErr)
			}
			if closeErr != nil {
				return "", closeErr
			}
		case tar.TypeSymlink:
			link := header.Linkname
			if link == "" || strings.ContainsAny(link, "\\\x00:") || path.IsAbs(link) || filepath.IsAbs(link) {
				return "", fmt.Errorf("archive symlink %q has an unsafe target", rel)
			}
			resolved := path.Join(path.Dir(rel), link)
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return "", fmt.Errorf("archive symlink %q escapes extraction directory", rel)
			}
			links = append(links, archiveLink{rel, link})
		default:
			return "", fmt.Errorf("unsupported archive entry type for %q (hard links and special files are not supported)", rel)
		}
	}
	// Consume gzip trailers to verify checksums and enforce limits even when
	// extra compressed data follows the tar end marker.
	if _, err := io.Copy(io.Discard, expanded); err != nil {
		return "", fmt.Errorf("verify archive: %w", err)
	}
	if compressed.N <= 0 || expanded.N <= 0 {
		return "", errors.New("archive exceeds the 1 GiB compressed or 2 GiB expanded size limit")
	}
	if entries == 0 || topLevel == "" {
		return "", errors.New("source archive is empty")
	}
	// Regular files are fully written before links exist, preventing archive
	// members from writing through symlinked parent directories.
	for _, link := range links {
		target := filepath.Join(destination, filepath.FromSlash(link.name))
		if err := ensureParents(destination, filepath.Dir(target)); err != nil {
			return "", err
		}
		if err := os.Symlink(filepath.FromSlash(link.target), target); err != nil {
			return "", fmt.Errorf("create archive symlink %q: %w", link.name, err)
		}
	}
	root := destination
	if contextPath != "." && contextPath != "" {
		preserved := filepath.Join(destination, filepath.FromSlash(contextPath))
		if info, err := os.Lstat(preserved); err == nil {
			if !info.IsDir() {
				return "", errors.New("selected build context is not a directory")
			}
			root = preserved
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve selected build context: %w", err)
	}
	for _, link := range links {
		name := filepath.Join(destination, filepath.FromSlash(link.name))
		if !within(root, name) {
			return "", fmt.Errorf("archive entry %q lies outside the selected build context", link.name)
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(name), filepath.FromSlash(link.target)))
		if !within(root, target) {
			return "", fmt.Errorf("symlink %q points outside the selected build context", link.name)
		}
		// Lexical cleaning alone is insufficient: for a -> . and b ->
		// a/../outside, the kernel follows a before interpreting "..".
		// Resolve complete link chains against the filesystem as well.
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return "", fmt.Errorf("archive symlink %q is broken or cyclic: %w", link.name, err)
		}
		if !within(resolvedRoot, resolved) {
			return "", fmt.Errorf("symlink %q resolves outside the selected build context", link.name)
		}
	}
	return resolvedRoot, nil
}

func archiveName(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\\\x00:") || path.IsAbs(value) || filepath.IsAbs(value) {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", fmt.Errorf("unsafe archive path %q", value)
		}
	}
	name := path.Clean(value)
	if name == "." {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	return name, nil
}

func ensureParents(root, directory string) error {
	if !within(root, directory) {
		return errors.New("archive parent directory escapes extraction directory")
	}
	rel, err := filepath.Rel(root, directory)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if err := ensureDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectory(name string) error {
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return os.Mkdir(name, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("archive parent %q is not a regular directory", name)
	}
	return nil
}
