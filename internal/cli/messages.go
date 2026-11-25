package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Report facts about the failed stage without guessing at a Dockerfile, daemon,
// or registry cause that is only available in the labeled Docker output.
func dockerFailureMessage(err error, image, stage string) string {
	action := "build workflow"
	if stage == "save" {
		action = "archive export"
	}
	prefix := fmt.Sprintf("The %s for image %q", action, image)
	switch {
	case errors.Is(err, context.Canceled):
		return prefix + " was canceled before completion (context canceled)."
	case errors.Is(err, context.DeadlineExceeded):
		return prefix + " did not complete before the repository job timed out (context deadline exceeded)."
	case errors.Is(err, exec.ErrNotFound):
		return prefix + " could not start because the Docker CLI was not found on PATH. Install Docker or correct PATH."
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("The Docker %s command for image %q exited unsuccessfully (exit code %d). See the labeled Docker logs for this repository for the command's failure details.", stage, image, exitErr.ExitCode())
	}
	return fmt.Sprintf("%s could not complete: %v", prefix, err)
}
