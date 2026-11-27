package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func artifactLocationApp(t *testing.T, redirected bool) (*app, string, string) {
	t.Helper()
	binaryDirectory := t.TempDir()
	workingDirectory := binaryDirectory
	locator := filepath.Join(binaryDirectory, "yas.yaml")
	if redirected {
		workingDirectory = t.TempDir()
		if err := os.WriteFile(locator, []byte(fmt.Sprintf("working_directory: %q\n", workingDirectory)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &app{options: Options{ConfigPath: locator, Out: io.Discard, Err: io.Discard, Version: "test"}, noProgress: true}, binaryDirectory, workingDirectory
}

func TestBatchReportsResolveAgainstWorkingDirectoryInsteadOfCaller(t *testing.T) {
	caller := workingDirectory(t)
	initialCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		redirected bool
		absolute   bool
	}{
		{name: "default beside binary without locator"},
		{name: "default under redirected working directory", redirected: true},
		{name: "relative custom report", path: filepath.Join("audit", "release.html"), redirected: true},
		{name: "absolute custom report", path: "release.html", redirected: true, absolute: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, binaryDirectory, workspace := artifactLocationApp(t, tc.redirected)
			path := tc.path
			if tc.absolute {
				path = filepath.Join(t.TempDir(), path)
			}
			calls := 0
			jobs := []job{func(context.Context) result {
				calls++
				return result{Project: "group/service", Operation: "build", Image: "service:v1", Status: "built"}
			}}
			options := batchReportOptions{path: path}
			if err := a.executeBatch(context.Background(), jobs, 1, time.Second, options, "build", "https://gitlab.example.com"); err != nil {
				t.Fatal(err)
			}
			pattern := filepath.Join(workspace, "reports", "*.html")
			if path != "" {
				pattern = path
				if !filepath.IsAbs(path) {
					pattern = filepath.Join(workspace, path)
				}
			}
			paths, err := filepath.Glob(pattern)
			if err != nil || len(paths) != 1 || calls != 1 {
				t.Fatalf("report was not saved under working directory: paths=%v calls=%d error=%v", paths, calls, err)
			}
			original, err := os.ReadFile(paths[0])
			if err != nil || !strings.Contains(string(original), "service:v1") {
				t.Fatalf("saved report content=%q error=%v", original, err)
			}
			if path != "" {
				if err := a.executeBatch(context.Background(), jobs, 1, time.Second, options, "build", "https://gitlab.example.com"); ExitCode(err) != 1 || calls != 1 {
					t.Fatalf("existing report did not stop execution: calls=%d error=%v", calls, err)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil || string(data) != string(original) {
					t.Fatalf("existing workspace report was overwritten: %v", err)
				}
			}
			for _, root := range []string{caller, binaryDirectory} {
				if root == workspace {
					continue
				}
				for _, relative := range []string{"reports", "audit"} {
					if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
						t.Errorf("report artifacts leaked to %s: %v", filepath.Join(root, relative), err)
					}
				}
			}
			if cwd, err := os.Getwd(); err != nil || cwd != initialCWD {
				t.Fatalf("report routing changed process cwd: %q, %v", cwd, err)
			}
		})
	}
}

func TestImageSaveDirectoriesResolveAgainstWorkingDirectoryInsteadOfCaller(t *testing.T) {
	caller := workingDirectory(t)
	initialCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		redirected bool
		absolute   bool
	}{
		{name: "default beside binary without locator"},
		{name: "default under redirected working directory", redirected: true},
		{name: "relative custom archive parent", path: filepath.Join("release images", "archives"), redirected: true},
		{name: "absolute custom archive parent", path: "archives", redirected: true, absolute: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, binaryDirectory, workspace := artifactLocationApp(t, tc.redirected)
			var options imageSaveOptions
			command := &cobra.Command{}
			options.bind(command)
			if err := command.Flags().Set("save", "true"); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(workspace, "images")
			if tc.path != "" {
				path := tc.path
				if tc.absolute {
					path = filepath.Join(t.TempDir(), path)
				}
				if err := command.Flags().Set("save-dir", path); err != nil {
					t.Fatal(err)
				}
				parent = path
				if !filepath.IsAbs(parent) {
					parent = filepath.Join(workspace, parent)
				}
			}
			selected, err := a.chooseImageSave(command, options)
			if err != nil || selected.run == nil || selected.directory != parent || selected.run.parent != parent {
				t.Fatalf("save destination was not based on working directory: options=%+v error=%v want=%q", selected, err, parent)
			}
			if _, err := os.Stat(parent); !os.IsNotExist(err) {
				t.Fatalf("save directory was created before a successful build requested export: %v", err)
			}
			directory, err := selected.run.destination()
			if err != nil || filepath.Dir(directory) != parent {
				t.Fatalf("timestamped save run is outside configured parent: %q, %v", directory, err)
			}
			if _, err := time.Parse("20060102T150405.000000000Z", filepath.Base(directory)); err != nil {
				t.Fatalf("save batch naming changed: %q", directory)
			}
			if again, err := selected.run.destination(); err != nil || again != directory {
				t.Fatalf("workers no longer share a single save batch: %q, %v", again, err)
			}
			for _, root := range []string{caller, binaryDirectory} {
				if root == workspace {
					continue
				}
				for _, relative := range []string{"images", "release images"} {
					if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
						t.Errorf("image artifacts leaked to %s: %v", filepath.Join(root, relative), err)
					}
				}
			}
			if cwd, err := os.Getwd(); err != nil || cwd != initialCWD {
				t.Fatalf("save routing changed process cwd: %q, %v", cwd, err)
			}
		})
	}
}
