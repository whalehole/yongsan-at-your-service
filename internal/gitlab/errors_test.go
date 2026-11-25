package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActionErrorsExplainHTTPStatusAndPreserveCause(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "authentication or access"}, {403, "denied access"}, {404, "or it is not accessible"},
		{429, "rate limited"}, {500, "server error"}, {503, "server error"}, {302, "redirect"}, {400, "rejected"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			cause := &HTTPError{Method: http.MethodGet, Path: "/api/v4/private-endpoint", StatusCode: tc.status}
			err := actionError(`look up tag "release/v1" in repository "group/service"`, cause, false)
			for _, want := range []string{"release/v1", "group/service", fmt.Sprintf("HTTP %d", tc.status), tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q does not contain %q", err, want)
				}
			}
			var response *HTTPError
			var operation *OperationError
			if !errors.As(err, &response) || response != cause || !errors.As(err, &operation) || !errors.Is(err, cause) {
				t.Fatalf("error cause lost: %v", err)
			}
			if strings.Contains(err.Error(), cause.Path) {
				t.Error("raw endpoint leaked into human message")
			}
			if IsNotFound(err) != (tc.status == 404) {
				t.Errorf("IsNotFound(%v) has wrong result", err)
			}
		})
	}
}

func TestCanceledAndTimedOutWriteErrorsDoNotClaimNoChange(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, write := range []bool{false, true} {
			err := actionError(`delete tag "release/v1" from repository "group/service"`, cause, write)
			if !errors.Is(err, cause) {
				t.Fatalf("context cause lost: %v", err)
			}
			ambiguous := strings.Contains(err.Error(), "may have reached GitLab") && strings.Contains(err.Error(), "result could not be confirmed") && strings.Contains(err.Error(), "Check the tag in GitLab before retrying")
			if ambiguous != write {
				t.Errorf("write=%v message=%q", write, err)
			}
			if strings.Contains(err.Error(), "was not attempted") || strings.Contains(err.Error(), "was not changed") {
				t.Errorf("unsafe claim about a write outcome: %v", err)
			}
		}
	}
	for _, status := range []int{500, 502, 503, 504} {
		err := actionError(`create tag "v1" in repository "group/service"`, &HTTPError{Method: http.MethodPost, StatusCode: status}, true)
		if !strings.Contains(err.Error(), "result could not be confirmed") || !strings.Contains(err.Error(), "Check the tag in GitLab before retrying") {
			t.Errorf("write server failure lacks uncertainty: %v", err)
		}
	}
}

func TestHumanErrorDoesNotExposeDiagnosticText(t *testing.T) {
	cause := errors.New("private response body token=test-token password=secret")
	err := actionError(`resolve branch "main" in repository "group/service"`, cause, false)
	if strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "password") {
		t.Fatalf("diagnostic text exposed: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("diagnostic cause was not preserved")
	}
}

func TestExplicitClientErrorResponsesDoNotClaimAmbiguousWrites(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 422, 429} {
		err := actionError(`create tag "v1" in repository "group/service"`, &HTTPError{Method: http.MethodPost, StatusCode: status}, true)
		if strings.Contains(err.Error(), "may have reached") || strings.Contains(err.Error(), "result could not be confirmed") {
			t.Errorf("explicit HTTP %d rejection claimed an uncertain outcome: %v", status, err)
		}
	}
}

type errorResponseTransport func(*http.Request) (*http.Response, error)

func (f errorResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadAPIErrorsNameRepositoryReferenceAndAction(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
		want []string
	}{
		{"commit", func(c *Client) error {
			_, err := c.ResolveCommit(context.Background(), "group/service", commitA)
			return err
		}, []string{"resolve commit", commitA}},
		{"branch", func(c *Client) error {
			_, err := c.ResolveBranch(context.Background(), "group/service", "release/one")
			return err
		}, []string{"resolve branch", "release/one"}},
		{"tag", func(c *Client) error {
			_, err := c.ResolveTag(context.Background(), "group/service", "release/v1")
			return err
		}, []string{"look up tag", "release/v1"}},
		{"project", func(c *Client) error { _, err := c.ProjectPath(context.Background(), "group/service"); return err }, []string{"resolve the name"}},
		{"archive", func(c *Client) error {
			_, err := c.Archive(context.Background(), "group/service", commitA, "services/api")
			return err
		}, []string{"download the source archive", commitA, "services/api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("https://gitlab.example.com", "test-token")
			if err != nil {
				t.Fatal(err)
			}
			client.HTTPClient = &http.Client{Transport: errorResponseTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private response test-token"))}, nil
			})}
			err = tc.call(client)
			if err == nil {
				t.Fatal("expected access error")
			}
			for _, want := range append(tc.want, "group/service", "HTTP 403", "denied access") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "test-token") {
				t.Error("response body or token exposed")
			}
		})
	}
}

func TestInvalidReadResponseIsContextualAndDoesNotExposeBody(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "private response body: test-token")
	})
	_, err := client.ResolveTag(context.Background(), "group/service", "v1")
	for _, want := range []string{"group/service", "v1", "invalid or incomplete response"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("message=%v want=%q", err, want)
		}
	}
	if strings.Contains(err.Error(), "private response") || strings.Contains(err.Error(), "test-token") {
		t.Fatal("invalid response content exposed")
	}
}

func TestCreateTagFailureNamesActionAndPreservesUnconfirmedOutcome(t *testing.T) {
	for _, statusCode := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(statusCode), func(t *testing.T) {
			var gets, posts atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(statusCode)
					return
				}
				gets.Add(1)
				w.WriteHeader(http.StatusNotFound)
			})
			status, err := client.EnsureTag(context.Background(), "group/service", commitA, "v1", "", false)
			if err == nil || status != "" || gets.Load() != 2 || posts.Load() != 1 {
				t.Fatalf("status=%q error=%v GET=%d POST=%d", status, err, gets.Load(), posts.Load())
			}
			for _, want := range []string{"create tag", "group/service", "v1", fmt.Sprintf("HTTP %d", statusCode)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q lacks %q", err, want)
				}
			}
			var response *HTTPError
			if !errors.As(err, &response) || response.StatusCode != statusCode {
				t.Fatalf("creation cause lost: %v", err)
			}
			if strings.Contains(err.Error(), "creation was not attempted") || strings.Contains(err.Error(), "was not changed") {
				t.Errorf("unsafe creation claim: %v", err)
			}
			if statusCode == 500 && !strings.Contains(err.Error(), "result could not be confirmed") {
				t.Errorf("ambiguous creation result not explained: %v", err)
			}
		})
	}
}

func TestCreateTagReconciliationConflictPreservesCreationFailure(t *testing.T) {
	var gets, posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if gets.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeReference(w, commitB)
	})
	status, err := client.EnsureTag(context.Background(), "group/service", commitA, "v1", "", false)
	if status != "" || err == nil || gets.Load() != 2 || posts.Load() != 1 {
		t.Fatalf("status=%q error=%v GET=%d POST=%d", status, err, gets.Load(), posts.Load())
	}
	for _, want := range []string{"group/service", "v1", commitA, commitB, "already points"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict message %q lacks %q", err, want)
		}
	}
	var response *HTTPError
	if !errors.As(err, &response) || response.Method != http.MethodPost || response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("POST diagnostic cause was lost: %v", err)
	}
}

func TestInvalidMergeBaseResponseDoesNotClaimCommitIsUnreachable(t *testing.T) {
	for _, ancestor := range []string{"", "invalid", strings.Repeat("z", 40)} {
		t.Run(ancestor, func(t *testing.T) {
			var mergeBases atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					writeReference(w, commitB)
					return
				}
				mergeBases.Add(1)
				fmt.Fprintf(w, `{"id":%q}`, ancestor)
			})
			err := client.ValidateBranch(context.Background(), "group/service", commitA, "main")
			if err == nil || mergeBases.Load() != 1 || strings.Contains(err.Error(), "not reachable") {
				t.Fatalf("invalid merge-base response was accepted or misdiagnosed: %v; calls=%d", err, mergeBases.Load())
			}
			for _, want := range []string{"group/service", "main", commitA, "invalid or incomplete response"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message %q lacks %q", err, want)
				}
			}
		})
	}
}
