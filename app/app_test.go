package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/cli"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// TestRunVersion runs the CLI through the entry point another module's main
// calls: `conductor version` answers as the binary does, the version line
// and the credit, exit 0.
func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("Run(version) = %d, %v; stderr %q", code, err, stderr.String())
	}
	want := "conductor " + version.String() + "\n" + cli.Credit + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing", stderr.String())
	}

	// An unknown command is refused with the usage and exit 2, as the
	// binary refuses it.
	stdout.Reset()
	code, err = Run(context.Background(), []string{"no-such-command"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || err == nil || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("Run(no-such-command) = %d, %v; stderr %q", code, err, stderr.String())
	}
}
