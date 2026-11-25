package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type prompter struct {
	reader      *bufio.Reader
	out         io.Writer
	interactive func() bool
}

// project applies a selected namespace only to interactive, missing project
// input. Explicit --project values always retain their original meaning.
func (p *prompter) project(ctx context.Context, groups []string, value *string) error {
	if *value != "" || !p.interactive() || len(groups) == 0 {
		return p.required(ctx, "project", "GitLab project path or ID", value, config.ValidateProject)
	}
	choices := append(append([]string(nil), groups...), "Skip (enter full project path or ID)")
	index, err := p.choose(ctx, "Select a GitLab group:", choices)
	if err != nil {
		return err
	}
	if index == len(groups) {
		return p.required(ctx, "project", "GitLab project path or ID", value, config.ValidateProject)
	}
	group := groups[index]
	var relative string
	if err := p.required(ctx, "project", "Repository path within "+group, &relative, func(input string) error {
		return config.ValidateProject(group + "/" + input)
	}); err != nil {
		return err
	}
	*value = group + "/" + relative
	return nil
}

func (p *prompter) line(ctx context.Context, label string) (string, error) {
	if !p.interactive() {
		return "", fmt.Errorf("%s is required; supply the corresponding flag in non-interactive mode", label)
	}
	fmt.Fprint(p.out, label, ": ")
	type answer struct {
		text string
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		line, err := p.reader.ReadString('\n')
		ch <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case a := <-ch:
		if a.err != nil && !(a.err == io.EOF && len(a.text) > 0) {
			return "", fmt.Errorf("input closed while reading %s", label)
		}
		return strings.TrimSpace(a.text), nil
	}
}

func (p *prompter) required(ctx context.Context, flag, label string, value *string, validate func(string) error) error {
	if *value != "" {
		if validate != nil {
			return validate(*value)
		}
		return nil
	}
	if !p.interactive() {
		return fmt.Errorf("missing required --%s", flag)
	}
	for {
		input, err := p.line(ctx, label)
		if err != nil {
			return err
		}
		if input == "" {
			fmt.Fprintln(p.out, "A value is required.")
			continue
		}
		if validate != nil {
			if err := validate(input); err != nil {
				fmt.Fprintln(p.out, err)
				continue
			}
		}
		*value = input
		return nil
	}
}

func (p *prompter) choose(ctx context.Context, label string, choices []string) (int, error) {
	if !p.interactive() {
		return 0, fmt.Errorf("%s: required source flags are missing", label)
	}
	fmt.Fprintln(p.out, label)
	for i, choice := range choices {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, choice)
	}
	for {
		input, err := p.line(ctx, fmt.Sprintf("Choose [1-%d]", len(choices)))
		if err != nil {
			return 0, err
		}
		index, err := strconv.Atoi(input)
		if err == nil && index >= 1 && index <= len(choices) {
			return index - 1, nil
		}
		fmt.Fprintf(p.out, "Enter a number from 1 to %d.\n", len(choices))
	}
}
