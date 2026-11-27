package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func (p *prompter) chooseMany(ctx context.Context, label string, choices []string) (selected []int, err error) {
	if !p.interactive() {
		return nil, fmt.Errorf("file selection requires an interactive terminal; supply --input")
	}
	if len(choices) == 0 {
		return nil, fmt.Errorf("there are no choices to select")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, inputIsFile := p.input.(*os.File)
	output, outputIsFile := p.out.(*os.File)
	terminalInput := inputIsFile && term.IsTerminal(int(input.Fd()))
	terminalOutput := outputIsFile && term.IsTerminal(int(output.Fd()))
	if terminalInput && !terminalOutput {
		return nil, fmt.Errorf("checkbox selection requires terminal output; supply --input when redirecting prompts")
	}
	var original *term.State
	if terminalInput {
		original, err = term.MakeRaw(int(input.Fd()))
		if err != nil {
			return nil, fmt.Errorf("prepare checkbox terminal: %w", err)
		}
	}
	ansi := original != nil && terminalOutput
	defer func() {
		if original != nil {
			if restoreErr := term.Restore(int(input.Fd()), original); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore checkbox terminal: %w", restoreErr))
			}
		}
		if ansi {
			_, cursorErr := io.WriteString(p.out, "\x1b[?25h\r\n")
			err = errors.Join(err, cursorErr)
		}
	}()
	if ansi {
		if _, err := io.WriteString(p.out, "\x1b[?25l"); err != nil {
			return nil, err
		}
	}
	checked := make([]bool, len(choices))
	cursor, previousLines := 0, 0
	message := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines := []string{checkboxLabel(label), "Up/Down: move  Space: check/uncheck  Enter: confirm  Ctrl+C: cancel"}
		for i, choice := range choices {
			pointer, mark := " ", " "
			if i == cursor {
				pointer = ">"
			}
			if checked[i] {
				mark = "x"
			}
			lines = append(lines, fmt.Sprintf("%s [%s] %s", pointer, mark, checkboxLabel(choice)))
		}
		lines = append(lines, message)
		var frame strings.Builder
		if ansi && previousLines > 0 {
			fmt.Fprintf(&frame, "\x1b[%dA", previousLines)
		}
		width := 0
		if ansi {
			if columns, _, sizeErr := term.GetSize(int(output.Fd())); sizeErr == nil {
				width = columns - 1
			}
		}
		for _, line := range lines {
			if ansi {
				frame.WriteString("\r\x1b[2K")
			}
			if width > 0 && len(line) > width {
				if width > 3 {
					line = line[:width-3] + "..."
				} else {
					line = line[:width]
				}
			}
			frame.WriteString(line)
			if original != nil {
				frame.WriteString("\r\n")
			} else {
				frame.WriteByte('\n')
			}
		}
		if _, err := io.WriteString(p.out, frame.String()); err != nil {
			return nil, err
		}
		previousLines = len(lines)
		key, err := p.checkboxByte(ctx)
		if err != nil {
			return nil, fmt.Errorf("input closed while selecting release YAML files: %w", err)
		}
		switch key {
		case 3:
			return nil, context.Canceled
		case 4:
			return nil, fmt.Errorf("input closed while selecting release YAML files: %w", io.EOF)
		case ' ':
			checked[cursor] = !checked[cursor]
			message = ""
		case '\r', '\n':
			// Consume an already-buffered LF after CR without reading ahead into
			// the next filename/group/output answer.
			if key == '\r' && p.reader.Buffered() > 0 {
				if next, _ := p.reader.Peek(1); len(next) == 1 && next[0] == '\n' {
					p.reader.ReadByte()
				}
			}
			for i, value := range checked {
				if value {
					selected = append(selected, i)
				}
			}
			if len(selected) > 0 {
				return selected, nil
			}
			message = "Select at least one file or choose Enter filename manually."
		case 27:
			prefix, err := p.checkboxByte(ctx)
			if err != nil {
				return nil, err
			}
			if prefix == 3 {
				return nil, context.Canceled
			}
			if prefix == 4 {
				return nil, io.EOF
			}
			if prefix != '[' && prefix != 'O' {
				continue
			}
			direction, err := p.checkboxByte(ctx)
			if err != nil {
				return nil, err
			}
			if direction == 3 {
				return nil, context.Canceled
			}
			if direction == 4 {
				return nil, io.EOF
			}
			if direction == 'A' {
				cursor = (cursor + len(choices) - 1) % len(choices)
			} else if direction == 'B' {
				cursor = (cursor + 1) % len(choices)
			}
		}
	}
}

// Read only the current key; a confirmed selection must not consume answers for
// subsequent prompts. A canceled command may abandon one blocked input read.
func (p *prompter) checkboxByte(ctx context.Context) (byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p.reader.Buffered() > 0 {
		return p.reader.ReadByte()
	}
	type result struct {
		value byte
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		value, err := p.reader.ReadByte()
		ch <- result{value, err}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case read := <-ch:
		return read.value, read.err
	}
}

// Keep menu labels on one terminal row and prevent filenames from injecting
// terminal control sequences. ASCII escapes also make truncation predictable.
func checkboxLabel(value string) string {
	var safe strings.Builder
	for _, character := range value {
		if character >= 32 && character <= 126 {
			safe.WriteRune(character)
		} else if character <= 0xffff {
			fmt.Fprintf(&safe, "\\u%04x", character)
		} else {
			fmt.Fprintf(&safe, "\\U%08x", character)
		}
	}
	return safe.String()
}
