package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGitLabTLSBypassFromConfig(t *testing.T) {
	workingDirectory(t)
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("expected authenticated read-only request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"commit":{"id":%q}}`, testSHA)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	// Check secure defaults again after the bypass has been used, to catch a
	// transport accidentally shared between command invocations.
	for _, tc := range []struct {
		name, setting string
		bypass        bool
	}{
		{name: "omitted verifies certificates"},
		{name: "explicit bypass", setting: "  insecure_skip_verify: true\n", bypass: true},
		{name: "explicit false verifies certificates", setting: "  insecure_skip_verify: false\n"},
		{name: "omitted still verifies after bypass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, server.URL, tc.setting)
			before := requests.Load()
			out, _, err := executeTest([]string{
				"tag", "one", "--project", "group/service", "--branch", "main", "--tag", "v1",
				"--dry-run", "--non-interactive", "--no-progress", "--format", "json",
			}, "", false)
			var results []result
			if decodeErr := json.Unmarshal([]byte(out), &results); decodeErr != nil || len(results) != 1 {
				t.Fatalf("results=%q decode error=%v command error=%v", out, decodeErr, err)
			}
			if tc.bypass {
				if err != nil || results[0].Status != "skipped" || requests.Load() <= before {
					t.Fatalf("explicit bypass failed: results=%+v error=%v", results, err)
				}
			} else if ExitCode(err) != 1 || results[0].Status != "failed" || requests.Load() != before {
				t.Fatalf("untrusted TLS server was not rejected: results=%+v error=%v", results, err)
			}
		})
	}
}
