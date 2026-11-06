// Package app is Conductor's entry point for a main of this module or of
// another: the official app's main blank-imports what registers its
// providers (package ext), then calls Main. This module's own main
// (cmd/conductor) calls Main with nothing registered: the free build.
//
// A main that links more modules than this one hands in its own
// third-party notices text first, with SetThirdPartyNotices, so the server
// serves the licences of what that binary ships:
//
//	//go:embed THIRD_PARTY_NOTICES
//	var notices string
//
//	func main() {
//		if err := app.SetThirdPartyNotices(notices); err != nil {
//			fmt.Fprintln(os.Stderr, "conductor:", err)
//			os.Exit(1)
//		}
//		app.Main()
//	}
//
// A main that hands in none serves this module's embedded notices.
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

// MaxThirdPartyNotices bounds the text SetThirdPartyNotices takes, in
// bytes (8 MiB).
const MaxThirdPartyNotices = cli.MaxNotices

// SetThirdPartyNotices hands in the third-party notices text the server
// serves at /third-party-notices.txt (on a switchyard too, with the same
// headers) in place of this module's embedded THIRD_PARTY_NOTICES. Call it
// before Run or Main: the server reads it once, when it starts. It
// replaces the text and nothing else; it checks no licence, plan or build.
//
// It refuses, with an error and changing nothing, an empty text (a main
// that has none hands in none, and the embedded notices are served), a
// text over MaxThirdPartyNotices, a second text, and a text handed in
// after a server of this process has started.
func SetThirdPartyNotices(text string) error { return cli.SetNotices(text) }

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
