package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeleteTagOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, want                 string
		missing, dry, inaccessible bool
		deleteStatus               int
	}{
		{name: "deleted", want: "deleted", deleteStatus: 204},
		{name: "missing tag", missing: true, want: "skipped"},
		{name: "missing project", missing: true, inaccessible: true},
		{name: "dry run", dry: true, want: "planned"},
		{name: "protected tag denied", deleteStatus: 403},
		{name: "raced deletion", deleteStatus: 404, want: "skipped"},
		{name: "server failure is not retried", deleteStatus: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/projects/group/project") {
					if tc.inaccessible {
						w.WriteHeader(404)
					} else {
						fmt.Fprint(w, `{"id":123}`)
					}
					return
				}
				if !strings.HasSuffix(r.URL.EscapedPath(), "/tags/release%2Fv1") {
					t.Errorf("path=%s", r.URL.EscapedPath())
				}
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					w.WriteHeader(tc.deleteStatus)
					return
				}
				if tc.missing {
					w.WriteHeader(404)
				} else {
					writeReference(w, commitA)
				}
			})
			status, _, err := client.DeleteTag(context.Background(), "group/project", "release/v1", tc.dry)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil || status != tc.want {
				t.Fatalf("status=%q err=%v", status, err)
			}
			wantDeletes := int32(1)
			if tc.missing || tc.dry {
				wantDeletes = 0
			}
			if deletes.Load() != wantDeletes {
				t.Fatalf("DELETE count=%d want=%d", deletes.Load(), wantDeletes)
			}
		})
	}
}
