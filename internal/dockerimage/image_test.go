package dockerimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testImage(repository, tag string, digit byte) Image {
	return Image{Repository: repository, Tag: tag, ID: "sha256:" + strings.Repeat(string(digit), 64)}
}

func fakeDocker(t *testing.T, body string) (*Client, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "docker")
	calls := filepath.Join(directory, "calls")
	listing := filepath.Join(directory, "images")
	t.Setenv("YAS_IMAGE_TEST_CALLS", calls)
	t.Setenv("YAS_IMAGE_TEST_LIST", listing)
	if body == "" {
		body = "case \"$1 $2\" in\n  'image ls') cat \"$YAS_IMAGE_TEST_LIST\" ;;\n  'image rm') printf 'Untagged: %s\\n' \"$4\" ;;\n  *) exit 99 ;;\nesac"
	}
	script := "#!/bin/sh\nprintf '%s\\000' \"$@\" >> \"$YAS_IMAGE_TEST_CALLS\"\nprintf '\\n' >> \"$YAS_IMAGE_TEST_CALLS\"\n" + body + "\n"
	if err := os.WriteFile(executable, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return &Client{Docker: executable}, calls, listing
}

func writeImages(t *testing.T, path string, images ...Image) {
	t.Helper()
	var data strings.Builder
	for _, image := range images {
		line, err := json.Marshal(image)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func dockerCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return calls
}

func TestFilterRequiresLiteralRepositoryOrTag(t *testing.T) {
	for _, filter := range []Filter{{Name: "api"}, {Name: "team/api"}, {Name: "registry.example.com:5000/team/api"}, {Tag: "V1.2_release-4"}, {Name: "api", Tag: "v1"}} {
		if err := filter.Validate(); err != nil {
			t.Errorf("valid filter %+v rejected: %v", filter, err)
		}
	}
	for _, filter := range []Filter{{}, {Name: "api:v1"}, {Name: "registry:5000"}, {Name: "*api"}, {Name: "team//api"}, {Name: "API"}, {Tag: "v*"}, {Tag: "v1?"}, {Tag: "v1/v2"}, {Tag: " v1"}, {Name: "api", Tag: "bad tag"}} {
		if err := filter.Validate(); err == nil {
			t.Errorf("invalid filter %+v accepted", filter)
		}
	}
}

func TestListMatchesExactNameTagAndBothWithStableReferences(t *testing.T) {
	client, callsFile, listing := fakeDocker(t, "")
	first := testImage("registry.example.com:5000/team/api", "v1", 'a')
	writeImages(t, listing,
		testImage("team/web", "v1", 'f'), first,
		testImage("registry.example.com:5000/team/api", "v2", 'a'),
		testImage("other/team/api", "v1", 'b'), testImage("api", "v1", 'c'),
		testImage("team/api-extra", "v1", 'd'), testImage("team/my-api", "v1", 'e'),
		testImage("team/api", "V1", 'a'), testImage("team/api", "v10", 'a'),
		testImage("<none>", "<none>", 'a'), first,
	)
	for _, tc := range []struct {
		filter Filter
		want   []string
	}{
		{Filter{Name: "api"}, []string{"api:v1", "other/team/api:v1", "registry.example.com:5000/team/api:v1", "registry.example.com:5000/team/api:v2", "team/api:V1", "team/api:v10"}},
		{Filter{Tag: "v1"}, []string{"api:v1", "other/team/api:v1", "registry.example.com:5000/team/api:v1", "team/api-extra:v1", "team/my-api:v1", "team/web:v1"}},
		{Filter{Name: "api", Tag: "v1"}, []string{"api:v1", "other/team/api:v1", "registry.example.com:5000/team/api:v1"}},
		{Filter{Name: "registry.example.com:5000/team/api"}, []string{"registry.example.com:5000/team/api:v1", "registry.example.com:5000/team/api:v2"}},
		{Filter{Name: "registry.example.com:5000/team/api", Tag: "v2"}, []string{"registry.example.com:5000/team/api:v2"}},
		{Filter{Tag: "V1"}, []string{"team/api:V1"}},
		{Filter{Name: "team/api", Tag: "v1"}, []string{}},
	} {
		images, err := client.List(context.Background(), tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(images))
		for _, image := range images {
			got = append(got, image.Reference())
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("filter %+v returned %v want=%v", tc.filter, got, tc.want)
		}
	}
	for _, args := range dockerCalls(t, callsFile) {
		want := []string{"image", "ls", "--all", "--no-trunc", "--format", "{{json .}}"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("Docker listing arguments=%q want=%q", args, want)
		}
	}
}

func TestListAndRemoveRejectInvalidOrAmbiguousListingsBeforeMutation(t *testing.T) {
	valid := testImage("team/api", "v1", 'a')
	line, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	otherID := testImage("team/api", "v1", 'b')
	otherLine, _ := json.Marshal(otherID)
	for _, data := range []string{
		"not JSON\n", "null\n", `{"Repository":"team/api","Tag":"v1","ID":"sha256:abc"}`,
		`{"Repository":"UPPER","Tag":"v1","ID":"sha256:` + strings.Repeat("a", 64) + `"}`,
		`{"Repository":"team/api","ID":"sha256:` + strings.Repeat("a", 64) + `"}`,
		`{"Repository":"team/api","Tag":"v1","ID":"sha256:` + strings.Repeat("z", 64) + `"}`,
		string(line) + "\n" + string(otherLine) + "\n",
	} {
		t.Run(data[:min(len(data), 30)], func(t *testing.T) {
			client, callsFile, listing := fakeDocker(t, "")
			if err := os.WriteFile(listing, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if images, err := client.List(context.Background(), Filter{Name: "other"}); err == nil || images != nil {
				t.Fatalf("invalid unselected data did not fail listing: %+v; %v", images, err)
			}
			if status, err := client.Remove(context.Background(), valid); err == nil || status != "" {
				t.Fatalf("invalid recheck did not stop deletion: %q; %v", status, err)
			}
			for _, call := range dockerCalls(t, callsFile) {
				if len(call) < 2 || call[1] != "ls" {
					t.Fatalf("malformed listing triggered mutation: %q", call)
				}
			}
		})
	}
}

func TestRemoveOnlyTheReferenceAfterExactIDRecheck(t *testing.T) {
	client, callsFile, listing := fakeDocker(t, "")
	selected := testImage("registry.example.com:5000/team/api", "v1", 'a')
	writeImages(t, listing, selected, testImage(selected.Repository, "v2", 'a'), testImage("api", "v1", 'a'))
	status, err := client.Remove(context.Background(), selected)
	if err != nil || status != "deleted" {
		t.Fatalf("status=%q error=%v", status, err)
	}
	want := [][]string{
		{"image", "ls", "--all", "--no-trunc", "--format", "{{json .}}", "--filter", "reference=" + selected.Reference()},
		{"image", "rm", "--", selected.Reference()},
	}
	if calls := dockerCalls(t, callsFile); !reflect.DeepEqual(calls, want) {
		t.Fatalf("removal arguments=%q want=%q", calls, want)
	}
}

func TestRemoveSkipsMissingReferencesAndRejectsChangedIDs(t *testing.T) {
	selected := testImage("api", "v1", 'a')
	for _, tc := range []struct {
		name   string
		images []Image
		status string
	}{
		{"missing", nil, "skipped"},
		{"only another namespace", []Image{testImage("team/api", "v1", 'a')}, "skipped"},
		{"only another tag", []Image{testImage("api", "v2", 'a')}, "skipped"},
		{"changed ID", []Image{testImage("api", "v1", 'b')}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, callsFile, listing := fakeDocker(t, "")
			writeImages(t, listing, tc.images...)
			status, err := client.Remove(context.Background(), selected)
			if status != tc.status || (err != nil) != (tc.status == "") {
				t.Fatalf("status=%q error=%v", status, err)
			}
			if err != nil && !strings.Contains(err.Error(), "image ID changed") {
				t.Fatalf("changed image error is unclear: %v", err)
			}
			if calls := dockerCalls(t, callsFile); len(calls) != 1 || calls[0][1] != "ls" {
				t.Fatalf("unsafe or missing-reference mutation: %q", calls)
			}
		})
	}
}

func TestRemoveReturnsConflictWithBoundedStderr(t *testing.T) {
	client, callsFile, listing := fakeDocker(t, "if [ \"$2\" = ls ]; then\n  cat \"$YAS_IMAGE_TEST_LIST\"\nelse\n  printf 'image is used by a stopped container\\n' >&2\n  printf '%06000d' 0 >&2\n  exit 1\nfi")
	selected := testImage("team/api", "v1", 'a')
	writeImages(t, listing, selected)
	status, err := client.Remove(context.Background(), selected)
	if status != "" || err == nil || !strings.Contains(err.Error(), selected.Reference()) || !strings.Contains(err.Error(), "stopped container") || !strings.Contains(err.Error(), "stderr truncated") || len(err.Error()) > 4500 {
		t.Fatalf("conflict diagnostic missing or unbounded: status=%q error=%v", status, err)
	}
	if calls := dockerCalls(t, callsFile); len(calls) != 2 || !reflect.DeepEqual(calls[1], []string{"image", "rm", "--", selected.Reference()}) {
		t.Fatalf("conflict caused forced or extra removals: %q", calls)
	}
}

func TestListingCommandFailureDoesNotRemoveAnything(t *testing.T) {
	client, callsFile, _ := fakeDocker(t, "printf 'Docker daemon is unavailable\\n' >&2\nexit 1")
	if status, err := client.Remove(context.Background(), testImage("team/api", "v1", 'a')); status != "" || err == nil || !strings.Contains(err.Error(), "Docker daemon is unavailable") {
		t.Fatalf("listing failure was not reported: status=%q error=%v", status, err)
	}
	if calls := dockerCalls(t, callsFile); len(calls) != 1 || calls[0][1] != "ls" {
		t.Fatalf("failed listing triggered removal: %q", calls)
	}
}

func TestInvalidFiltersAndImagesNeverInvokeDocker(t *testing.T) {
	client, callsFile, _ := fakeDocker(t, "exit 99")
	if _, err := client.List(context.Background(), Filter{}); err == nil {
		t.Fatal("empty deletion scope was accepted")
	}
	if _, err := client.Remove(context.Background(), Image{Repository: "api", Tag: "v1", ID: "abc"}); err == nil {
		t.Fatal("truncated selected ID was accepted")
	}
	if calls := dockerCalls(t, callsFile); len(calls) != 0 {
		t.Fatalf("invalid inputs invoked Docker: %q", calls)
	}
}

func TestDefaultDockerUsesInheritedContextAndNormalizesFullIDs(t *testing.T) {
	client, _, listing := fakeDocker(t, "if [ \"$DOCKER_CONTEXT\" != yas-image-test ]; then exit 91; fi\ncat \"$YAS_IMAGE_TEST_LIST\"")
	t.Setenv("PATH", filepath.Dir(client.Docker)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONTEXT", "yas-image-test")
	client.Docker = ""
	image := testImage("team/api", "v1", 'A')
	writeImages(t, listing, image)
	images, err := client.List(context.Background(), Filter{Name: "api"})
	if err != nil || len(images) != 1 || images[0].ID != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("default Docker context or full ID failed: %+v; %v", images, err)
	}
}

func TestCancellationPreservesCauseAndStopsCommands(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "remove"}[remove], func(t *testing.T) {
			body := "exec sleep 30"
			if remove {
				body = "if [ \"$2\" = ls ]; then cat \"$YAS_IMAGE_TEST_LIST\"; else exec sleep 30; fi"
			}
			client, callsFile, listing := fakeDocker(t, body)
			selected := testImage("team/api", "v1", 'a')
			writeImages(t, listing, selected)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := 1
			if remove {
				want = 2
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if remove {
					_, err = client.Remove(ctx, selected)
				} else {
					_, err = client.List(ctx, Filter{Name: "api"})
				}
				done <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for len(dockerCalls(t, callsFile)) < want && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("command did not stop with its context: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled Docker command did not stop promptly")
			}
			if calls := dockerCalls(t, callsFile); len(calls) != want {
				t.Fatalf("unexpected commands on cancellation: %q", calls)
			}
		})
	}
	client, callsFile, _ := fakeDocker(t, "exit 99")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.List(ctx, Filter{Name: "api"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled context cause lost: %v", err)
	}
	if calls := dockerCalls(t, callsFile); len(calls) != 0 {
		t.Fatalf("pre-canceled operation invoked Docker: %q", calls)
	}
}
