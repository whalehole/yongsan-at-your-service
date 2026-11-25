package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// OperationError presents a contextual message while retaining the original
// cause for errors.Is, errors.As, and lower-level diagnostics.
type OperationError struct {
	Message string
	Cause   error
}

func (e *OperationError) Error() string { return e.Message }
func (e *OperationError) Unwrap() error { return e.Cause }

type responseDecodeError struct{ cause error }

func (e *responseDecodeError) Error() string { return "GitLab response could not be decoded" }
func (e *responseDecodeError) Unwrap() error { return e.cause }

func actionError(action string, cause error, write bool) error {
	reason := "GitLab could not complete the request. Check the connection and configured GitLab URL."
	ambiguous := write
	var response *HTTPError
	var decode *responseDecodeError
	switch {
	case errors.Is(cause, context.Canceled):
		reason = "the request was canceled."
	case errors.Is(cause, context.DeadlineExceeded):
		reason = "the request timed out."
	case errors.As(cause, &decode):
		reason = "GitLab returned an invalid or incomplete response."
	case errors.As(cause, &response):
		status := response.StatusCode
		ambiguous = write && status >= 500
		switch {
		case status == http.StatusUnauthorized:
			reason = "GitLab rejected authentication or access (HTTP 401). Check that the token is valid and has access to the repository."
			ambiguous = false
		case status == http.StatusForbidden:
			reason = "GitLab denied access (HTTP 403). Check repository access and token permissions."
			if write {
				reason = "GitLab denied the request (HTTP 403). Check repository access, token permissions, and any protected-tag rules that may apply."
			}
			ambiguous = false
		case status == http.StatusNotFound:
			reason = "GitLab could not find the requested resource, or it is not accessible to this token (HTTP 404). Check the repository, reference, and token access."
		case status == http.StatusTooManyRequests:
			reason = "GitLab rate limited the request (HTTP 429). Wait before retrying."
			ambiguous = false
		case status >= 500:
			reason = fmt.Sprintf("GitLab reported a server error (HTTP %d). Try again later.", status)
			if write {
				reason = fmt.Sprintf("GitLab reported a server error (HTTP %d).", status)
			}
		case status >= 300 && status < 400:
			reason = fmt.Sprintf("GitLab redirected the request (HTTP %d); redirects are not followed. Check the configured GitLab URL.", status)
		default:
			reason = fmt.Sprintf("GitLab rejected the request (HTTP %d). Check the repository, reference, and request permissions.", status)
		}
	}
	if ambiguous {
		reason += " The request may have reached GitLab; its result could not be confirmed. Check the tag in GitLab before retrying."
	}
	return &OperationError{Message: "Cannot " + action + ": " + reason, Cause: cause}
}

func responseError(action string, cause error) error {
	return &OperationError{Message: "Cannot " + action + ": GitLab returned an invalid or incomplete response.", Cause: cause}
}

func withExplanation(cause error, explanation string) error {
	return &OperationError{Message: cause.Error() + " " + explanation, Cause: cause}
}

func missingTagVerificationError(project, tag string, deletionSent bool, cause error) error {
	prefix := fmt.Sprintf("Tag %q was not found by the tag lookup, but ", tag)
	if deletionSent {
		prefix = fmt.Sprintf("The deletion request for tag %q in repository %q returned HTTP 404, but ", tag, project)
	}
	verification := fmt.Sprintf("verification of repository %q could not be completed.", project)
	advice := "Check repository access, token permissions, and the connection."
	var response *HTTPError
	var decode *responseDecodeError
	switch {
	case errors.Is(cause, context.Canceled):
		verification = fmt.Sprintf("verification of repository %q was canceled.", project)
	case errors.Is(cause, context.DeadlineExceeded):
		verification = fmt.Sprintf("verification of repository %q timed out.", project)
	case errors.As(cause, &decode):
		verification = fmt.Sprintf("GitLab returned an invalid or incomplete response while verifying repository %q.", project)
		advice = "Check the GitLab server and retry repository verification."
	case errors.As(cause, &response):
		switch status := response.StatusCode; {
		case status == http.StatusUnauthorized:
			verification = fmt.Sprintf("GitLab rejected authentication or access while verifying repository %q (HTTP 401).", project)
			advice = "Check that the token is valid and has access to the repository."
		case status == http.StatusForbidden:
			verification = fmt.Sprintf("GitLab denied access while verifying repository %q (HTTP 403).", project)
			advice = "Check repository access and token permissions."
		case status == http.StatusNotFound:
			verification = fmt.Sprintf("repository %q may be missing or inaccessible to this token (HTTP 404).", project)
			advice = "Check the repository identifier and token access."
		case status == http.StatusTooManyRequests:
			verification = fmt.Sprintf("GitLab rate limited verification of repository %q (HTTP 429).", project)
			advice = "Wait before retrying."
		case status >= 500:
			verification = fmt.Sprintf("GitLab reported a server error while verifying repository %q (HTTP %d).", project, status)
			advice = "Try again later."
		default:
			verification = fmt.Sprintf("GitLab rejected verification of repository %q (HTTP %d).", project, status)
		}
	}
	outcome := "Cannot confirm that the tag is absent; deletion was not attempted."
	if deletionSent {
		outcome = "A deletion request was sent; its result cannot be confirmed. Check the tag in GitLab before retrying."
	}
	return &OperationError{Message: prefix + verification + " " + outcome + " " + advice, Cause: cause}
}
