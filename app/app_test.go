package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor"
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

// SetThirdPartyNotices refuses an empty text and one over its limit, 8 MiB,
// which the embedded notices are well under. (What it takes, and that it
// refuses a text once a server started, is internal/cli's
// TestServeServesTheNoticesAMainHandedIn: a text taken here would stay in
// the process's holder.)
func TestSetThirdPartyNoticesRefuses(t *testing.T) {
	if MaxThirdPartyNotices != 8<<20 {
		t.Fatalf("MaxThirdPartyNotices = %d, want 8 MiB", MaxThirdPartyNotices)
	}
	if n := len(conductor.ThirdPartyNotices); n == 0 || n > MaxThirdPartyNotices {
		t.Fatalf("the embedded notices are %d bytes, over the limit a main's own are held to, %d", n, MaxThirdPartyNotices)
	}
	if err := SetThirdPartyNotices(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("SetThirdPartyNotices(empty) = %v, want refused", err)
	}
	if err := SetThirdPartyNotices(strings.Repeat("n", MaxThirdPartyNotices+1)); err == nil || !strings.Contains(err.Error(), "over the limit") {
		t.Fatalf("SetThirdPartyNotices(over the limit) = %v, want refused", err)
	}
}
