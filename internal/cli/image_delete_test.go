package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type imageDeleteFixture struct {
	log string
}

func setupImageDeleteCLI(t *testing.T) imageDeleteFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker uses a POSIX shell")
	}
	directory := workingDirectory(t)
	log := filepath.Join(directory, "docker-commands")
	var inventory bytes.Buffer
	for _, image := range []struct{ repository, tag, id string }{
		{"accesscontrols-service", "1.2.9", "a"},
		{"accesscontrols-service", "1.2.90", "b"},
		{"registry.example:5000/team/accesscontrols-service", "2.0.0", "c"},
		{"accesscontrols-service-helper", "1.2.9", "d"},
		{"payments", "1.2.9", "a"},
		{"payments", "latest", "a"},
		{"<none>", "<none>", "e"},
	} {
		if err := json.NewEncoder(&inventory).Encode(map[string]string{"Repository": image.repository, "Tag": image.tag, "ID": "sha256:" + strings.Repeat(image.id, 64)}); err != nil {
			t.Fatal(err)
		}
	}
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$YAS_IMAGE_TEST_LOG"
if [ "$1" = image ] && [ "$2" = ls ]; then
  if [ "$YAS_IMAGE_TEST_LIST_FAIL" = 1 ]; then printf 'Cannot connect to Docker daemon\n' >&2; exit 1; fi
  reference=''
  for argument in "$@"; do
    case "$argument" in reference=*) reference=${argument#reference=} ;; esac
  done
  if [ -n "$reference" ] && [ "$reference" = "$YAS_IMAGE_TEST_MISSING" ]; then exit 0; fi
  if [ -n "$reference" ] && [ "$reference" = "$YAS_IMAGE_TEST_CHANGED" ]; then
    printf '%s\n' "$YAS_IMAGE_TEST_CHANGED_INVENTORY"
    exit 0
  fi
  printf '%s' "$YAS_IMAGE_TEST_INVENTORY"
  exit 0
fi
if [ "$1" = image ] && [ "$2" = rm ]; then
  if [ "$3" != -- ] || [ "$#" != 4 ]; then printf 'Unexpected removal arguments\n' >&2; exit 91; fi
  if [ "$4" = "$YAS_IMAGE_TEST_REMOVE_FAIL" ]; then printf 'conflict: image is used by a running container\n' >&2; exit 1; fi
  printf 'Untagged: %s\n' "$4"
  exit 0
fi
printf 'Unexpected Docker command\n' >&2
exit 90
`
	if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("YAS_IMAGE_TEST_LOG", log)
	t.Setenv("YAS_IMAGE_TEST_INVENTORY", inventory.String())
	for _, variable := range []string{"YAS_IMAGE_TEST_LIST_FAIL", "YAS_IMAGE_TEST_MISSING", "YAS_IMAGE_TEST_CHANGED", "YAS_IMAGE_TEST_CHANGED_INVENTORY", "YAS_IMAGE_TEST_REMOVE_FAIL"} {
		t.Setenv(variable, "")
	}
	return imageDeleteFixture{log: log}
}

func (fixture imageDeleteFixture) commands(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(fixture.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func decodeImageDeleteResults(t *testing.T, output string) []result {
	t.Helper()
	var results []result
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("invalid results %q: %v", output, err)
	}
	return results
}

func TestDockerImageDeleteDryRunMatchesNameTagAndIntersectionExactly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  []string
	}{
		{"name", []string{"--name", "accesscontrols-service"}, []string{"accesscontrols-service:1.2.9", "accesscontrols-service:1.2.90", "registry.example:5000/team/accesscontrols-service:2.0.0"}},
		{"tag", []string{"--tag", "1.2.9"}, []string{"accesscontrols-service-helper:1.2.9", "accesscontrols-service:1.2.9", "payments:1.2.9"}},
		{"both narrow", []string{"--name", "accesscontrols-service", "--tag", "1.2.9"}, []string{"accesscontrols-service:1.2.9"}},
		{"full repository", []string{"--name", "registry.example:5000/team/accesscontrols-service"}, []string{"registry.example:5000/team/accesscontrols-service:2.0.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			args := append([]string{"image", "delete", "batch", "--dry-run", "--format", "json", "--no-progress"}, tc.flags...)
			out, logs, err := executeTest(args, "", false)
			if err != nil {
				t.Fatal(err)
			}
			results := decodeImageDeleteResults(t, out)
			var images []string
			for _, result := range results {
				images = append(images, result.Image)
				if result.Status != "planned" || result.ImageID == "" || result.Commit != "" || result.CommitTitle != "" {
					t.Errorf("incorrect dry-run result: %+v", result)
				}
			}
			sort.Strings(images)
			if !reflect.DeepEqual(images, tc.want) {
				t.Fatalf("matched %v, wanted %v", images, tc.want)
			}
			if commands := fixture.commands(t); len(commands) != 1 || !strings.HasPrefix(commands[0], "image ls ") {
				t.Fatalf("dry run executed removal/recheck: %v", commands)
			}
			if !strings.Contains(logs, "IMAGE ID") {
				t.Fatalf("missing preview: %s", logs)
			}
			paths, err := filepath.Glob(filepath.Join("reports", "*.html"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("missing report: %v %v", paths, err)
			}
			html := readReportHTML(t, paths[0])
			for _, text := range []string{"Batch Docker image deletion", "Image repository", "Image ID", "Docker target", "DRY RUN"} {
				if !strings.Contains(html, text) {
					t.Errorf("report missing %q", text)
				}
			}
			if strings.Contains(html, "GitLab server") || strings.Contains(html, "Commit title unavailable") {
				t.Error("Docker image report contains GitLab commit labels")
			}
		})
	}
}

func TestDockerImageDeleteRemovesReferencesNotSharedIDs(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	out, _, err := executeTest([]string{"image", "delete", "batch", "--tag", "1.2.9", "--yes", "--format", "json", "--no-progress"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	results := decodeImageDeleteResults(t, out)
	if len(results) != 3 {
		t.Fatalf("results=%s", out)
	}
	for _, result := range results {
		if result.Status != "deleted" {
			t.Errorf("removal failed: %+v", result)
		}
	}
	var removed []string
	rechecks := 0
	for _, command := range fixture.commands(t) {
		if strings.Contains(command, "--filter reference=") {
			rechecks++
		}
		if strings.HasPrefix(command, "image rm ") {
			removed = append(removed, strings.TrimPrefix(command, "image rm -- "))
			if strings.Contains(command, "--force") || strings.Contains(command, "sha256:") || strings.Contains(command, ":latest") {
				t.Errorf("removed beyond requested references: %s", command)
			}
		}
	}
	want := []string{"accesscontrols-service-helper:1.2.9", "accesscontrols-service:1.2.9", "payments:1.2.9"}
	sort.Strings(removed)
	if !reflect.DeepEqual(removed, want) || rechecks != 3 {
		t.Fatalf("removals=%v rechecks=%d", removed, rechecks)
	}
}

func TestDockerImageDeleteContinuesAfterChangedMissingAndRejectedReferences(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "Docker conflict", true: "ID changed"}[changed], func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			if changed {
				t.Setenv("YAS_IMAGE_TEST_CHANGED", "accesscontrols-service:1.2.9")
				t.Setenv("YAS_IMAGE_TEST_CHANGED_INVENTORY", `{"Repository":"accesscontrols-service","Tag":"1.2.9","ID":"sha256:`+strings.Repeat("f", 64)+`"}`)
			} else {
				t.Setenv("YAS_IMAGE_TEST_REMOVE_FAIL", "accesscontrols-service:1.2.9")
			}
			t.Setenv("YAS_IMAGE_TEST_MISSING", "payments:1.2.9")
			out, _, err := executeTest([]string{"image", "delete", "batch", "--tag", "1.2.9", "--yes", "--format", "json", "--no-progress"}, "", false)
			if ExitCode(err) != 1 {
				t.Fatalf("expected partial failure: %v", err)
			}
			statuses := map[string]string{}
			for _, result := range decodeImageDeleteResults(t, out) {
				statuses[result.Image] = result.Status
				if result.Status == "failed" && result.Error == "" {
					t.Error("failed reference has no reason")
				}
			}
			want := map[string]string{"accesscontrols-service-helper:1.2.9": "deleted", "accesscontrols-service:1.2.9": "failed", "payments:1.2.9": "skipped"}
			if !reflect.DeepEqual(statuses, want) {
				t.Fatalf("partial statuses=%v", statuses)
			}
			for _, command := range fixture.commands(t) {
				if command == "image rm -- payments:1.2.9" || changed && command == "image rm -- accesscontrols-service:1.2.9" {
					t.Errorf("removed missing/changed image: %s", command)
				}
			}
		})
	}
}

func TestDockerImageDeleteInteractiveRootPreviewDefaultsToNo(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	out, logs, err := executeTest([]string{"--format", "json"}, "9\n2\n1.2.9\n\n", true)
	if err != nil || len(decodeImageDeleteResults(t, out)) != 0 {
		t.Fatalf("decline result=%s logs=%s error=%v", out, logs, err)
	}
	if !strings.Contains(logs, "IMAGE ID") || !strings.Contains(logs, "[y/N]") || !strings.Contains(logs, "cancelled") {
		t.Fatalf("preview/confirmation missing: %s", logs)
	}
	if commands := fixture.commands(t); len(commands) != 1 {
		t.Fatalf("decline executed removals: %v", commands)
	}
	if _, err := os.Stat("reports"); !os.IsNotExist(err) {
		t.Fatalf("decline created a report: %v", err)
	}
}

func TestDockerImageDeleteInteractiveApprovalAndYesBypass(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirm", true: "yes flag"}[explicit], func(t *testing.T) {
			setupImageDeleteCLI(t)
			args := []string{"image", "delete", "batch", "--format", "json", "--no-report", "--no-progress"}
			input := "1\naccesscontrols-service\nyes\n"
			if explicit {
				args = append(args, "--name", "accesscontrols-service", "--yes")
				input = ""
			}
			out, logs, err := executeTest(args, input, true)
			if err != nil {
				t.Fatalf("logs=%s error=%v", logs, err)
			}
			if len(decodeImageDeleteResults(t, out)) != 3 {
				t.Fatalf("results=%s", out)
			}
			if explicit && strings.Contains(logs, "[y/N]") {
				t.Fatalf("--yes prompted: %s", logs)
			}
		})
	}
}

func TestDockerImageDeleteNoMatchesAndReportPreparation(t *testing.T) {
	t.Run("no matches", func(t *testing.T) {
		fixture := setupImageDeleteCLI(t)
		out, _, err := executeTest([]string{"image", "delete", "batch", "--name", "not-present", "--yes", "--format", "json"}, "", false)
		if err != nil || len(decodeImageDeleteResults(t, out)) != 0 || len(fixture.commands(t)) != 1 {
			t.Fatalf("no-match output=%s error=%v", out, err)
		}
		paths, _ := filepath.Glob("reports/*.html")
		if len(paths) != 1 || !strings.Contains(readReportHTML(t, paths[0]), "No matching Docker image references were found") {
			t.Fatalf("no-match report missing: %v", paths)
		}
	})
	t.Run("existing report blocks deletion", func(t *testing.T) {
		fixture := setupImageDeleteCLI(t)
		if err := os.WriteFile("existing.html", []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := executeTest([]string{"image", "delete", "batch", "--tag", "1.2.9", "--yes", "--report", "existing.html"}, "", false)
		if ExitCode(err) != 1 || len(fixture.commands(t)) != 1 {
			t.Fatalf("report failure did not block removal: %v", err)
		}
		if data, err := os.ReadFile("existing.html"); err != nil || string(data) != "preserve" {
			t.Fatalf("existing report changed: %v", err)
		}
	})
}

func TestDockerImageDeleteInvalidInputsFailBeforeDocker(t *testing.T) {
	for _, flags := range [][]string{
		{}, {"--yes"}, {"--tag", "1.2.9"}, {"--name", ""}, {"--tag", "", "--yes"},
		{"--name", "*", "--yes"}, {"--tag", "1.2.*", "--yes"}, {"--name", "service:tag", "--yes"},
		{"--tag", "1.2.9", "--yes", "--force"}, {"--tag", "1.2.9", "--yes", "--timeout", "invalid"},
		{"--tag", "1.2.9", "--yes", "--report", "bad.txt"},
		{"--tag", "1.2.9", "--yes", "--concurrency", "0"},
		{"--tag", "1.2.9", "--yes", "--concurrency", "-1"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			fixture := setupImageDeleteCLI(t)
			args := append([]string{"image", "delete", "batch"}, flags...)
			_, _, err := executeTest(args, "", false)
			if ExitCode(err) != 2 || len(fixture.commands(t)) != 0 {
				t.Fatalf("invalid selector executed Docker: args=%v error=%v", args, err)
			}
		})
	}
}

func TestDockerImageDeleteInventoryFailureNeverRemovesImages(t *testing.T) {
	fixture := setupImageDeleteCLI(t)
	t.Setenv("YAS_IMAGE_TEST_LIST_FAIL", "1")
	_, _, err := executeTest([]string{"image", "delete", "batch", "--tag", "1.2.9", "--yes"}, "", false)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "Docker daemon") || len(fixture.commands(t)) != 1 {
		t.Fatalf("inventory failure=%v commands=%v", err, fixture.commands(t))
	}
}
