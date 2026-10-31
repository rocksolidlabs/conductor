// Package app is Conductor's entry point for a main of this module or of
// another: the official app's main blank-imports what registers its
// providers (package ext), then calls Main. This module's own main
// (cmd/conductor) calls Main with nothing registered: the free build.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/rocksolidlabs/conductor/internal/cli"
)

// Run executes the command line args (without the program's name) and
// returns the process's exit code: conductor serve, host, notify and the
// rest, as the conductor binary runs them.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return cli.Run(ctx, args, stdin, stdout, stderr)
}

// Main runs the command line os.Args names until it ends or SIGINT or
// SIGTERM ends its context, prints its error on stderr, and exits with its
// code (1 for an error that came with none).
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	code, err := Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conductor:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
