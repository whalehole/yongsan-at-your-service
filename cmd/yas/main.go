package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yongsan/yongsan-at-your-service/internal/cli"
	"golang.org/x/term"
)

// Set with go build -ldflags "-X main.version=v1.0.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := cli.New(cli.Options{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		Interactive: term.IsTerminal(int(os.Stdin.Fd())), Version: version,
	})
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(cli.ExitCode(err))
	}
}
