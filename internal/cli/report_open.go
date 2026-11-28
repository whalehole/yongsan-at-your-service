package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Opening is independent of the batch context: a saved report can still be
// useful after cancellation. The launcher is bounded even in headless sessions.
func openBatchReport(filename string) error {
	command, args, err := reportOpenCommand(runtime.GOOS, filename)
	if err != nil {
		return err
	}
	if err := runReportOpen(command, args, 7*time.Second); err != nil {
		return fmt.Errorf("open report %s: %w", quoteReportOpenText(filename, 512), err)
	}
	return nil
}

func reportOpenCommand(goos, filename string) (string, []string, error) {
	if strings.ContainsRune(filename, '\x00') {
		return "", nil, errors.New("report path must not contain NUL characters")
	}
	switch goos {
	case "darwin", "linux":
		if !strings.HasPrefix(filename, "/") {
			return "", nil, errors.New("report path must be absolute")
		}
		command := "open"
		if goos == "linux" {
			command = "xdg-open"
		}
		return command, []string{filename}, nil
	case "windows":
		fileURL, err := windowsReportURL(filename)
		if err != nil {
			return "", nil, err
		}
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", fileURL}, nil
	default:
		return "", nil, fmt.Errorf("opening batch reports is not supported on %s", quoteReportOpenText(goos, 64))
	}
}

func windowsReportURL(filename string) (string, error) {
	path := strings.ReplaceAll(filename, `\`, "/")
	upper := strings.ToUpper(path)
	if strings.HasPrefix(upper, "//?/UNC/") {
		path = "//" + path[len("//?/UNC/"):]
	} else if strings.HasPrefix(path, "//?/") {
		path = path[len("//?/"):]
	}
	if strings.HasPrefix(path, "//") {
		parts := strings.SplitN(path[2:], "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[1] == "" {
			return "", errors.New("Windows report path must name an absolute drive or UNC file")
		}
		return (&url.URL{Scheme: "file", Host: parts[0], Path: "/" + parts[1]}).String(), nil
	}
	if len(path) < 3 || !(path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z') || path[1] != ':' || path[2] != '/' {
		return "", errors.New("Windows report path must name an absolute drive or UNC file")
	}
	return (&url.URL{Scheme: "file", Path: "/" + path}).String(), nil
}

func runReportOpen(command string, args []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = 2 * time.Second
	var stderr reportOpenStderr
	// A nil stdout connects directly to the null device, avoiding a pipe that
	// a browser descendant could retain after the launcher exits.
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if len(stderr.data) > 0 {
			return fmt.Errorf("default report launcher %s failed: %w; stderr: %s", quoteReportOpenText(command, 128), err, stderr.String())
		}
		return fmt.Errorf("default report launcher %s failed: %w", quoteReportOpenText(command, 128), err)
	}
	return nil
}

type reportOpenStderr struct {
	data      []byte
	truncated bool
}

func (w *reportOpenStderr) Write(data []byte) (int, error) {
	const limit = 1024
	remaining := limit - len(w.data)
	if len(data) > remaining {
		w.data = append(w.data, data[:remaining]...)
		w.truncated = true
	} else {
		w.data = append(w.data, data...)
	}
	return len(data), nil
}

func (w *reportOpenStderr) String() string {
	message := quoteReportOpenText(strings.TrimSpace(string(w.data)), 1024)
	if w.truncated {
		message += " [stderr truncated]"
	}
	return message
}

func quoteReportOpenText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
		value += "..."
	}
	return strconv.Quote(value)
}
