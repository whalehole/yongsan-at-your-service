package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func checkboxPrompt(input string, output io.Writer) *prompter {
	reader := strings.NewReader(input)
	return &prompter{reader: bufio.NewReader(reader), input: reader, out: output, interactive: func() bool { return true }}
}

func TestCheckboxSelectionKeysAndFollowingPrompts(t *testing.T) {
	for _, tc := range []struct {
		name, keys string
		want       []int
	}{
		{"two choices", " \x1b[B \r\nnext answer\n", []int{0, 1}},
		{"wrap up", "\x1b[A \nnext answer\n", []int{2}},
		{"toggle off", "  \x1bOB \nnext answer\n", []int{1}},
		{"empty enter retries", "\n \nnext answer\n", []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			prompt := checkboxPrompt(tc.keys, &output)
			got, err := prompt.chooseMany(context.Background(), "Select files", []string{"first", "second", "manual"})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selection=%v error=%v", got, err)
			}
			next, err := prompt.line(context.Background(), "Next")
			if err != nil || next != "next answer" {
				t.Fatalf("checkbox consumed following answer: %q %v", next, err)
			}
		})
	}
}

func TestCheckboxCancellationEOFAndOutputErrors(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		writer      io.Writer
		want        error
	}{
		{name: "cancel", input: "\x03", writer: io.Discard, want: context.Canceled},
		{name: "cancel after Escape", input: "\x1b\x03", writer: io.Discard, want: context.Canceled},
		{name: "cancel interrupted arrow", input: "\x1b[\x03", writer: io.Discard, want: context.Canceled},
		{name: "EOF after checking", input: " ", writer: io.Discard, want: io.EOF},
		{name: "Ctrl D", input: "\x04", writer: io.Discard, want: io.EOF},
		{name: "writer failure", input: " \n", writer: unavailableReportOutput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := checkboxPrompt(tc.input, tc.writer).chooseMany(context.Background(), "Select", []string{"a"})
			if err == nil || len(selection) != 0 || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("selection=%v error=%v", selection, err)
			}
		})
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	prompt := &prompter{reader: bufio.NewReader(reader), input: reader, out: io.Discard, interactive: func() bool { return true }}
	done := make(chan error, 1)
	go func() { _, err := prompt.chooseMany(ctx, "Select", []string{"a"}); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation=%v", err)
	}
}

func TestCheckboxEscapesControlCharactersInLabels(t *testing.T) {
	var output bytes.Buffer
	_, err := checkboxPrompt(" \n", &output).chooseMany(context.Background(), "Select", []string{"bad\x1b[2J\nfilename.yaml"})
	if err != nil || strings.Contains(output.String(), "\x1b") || strings.Contains(output.String(), "\nfilename") || !strings.Contains(output.String(), "\\u001b") {
		t.Fatalf("unsafe menu label: %q, error=%v", output.String(), err)
	}
}
