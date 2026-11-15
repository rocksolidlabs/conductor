//go:build unix

package api

import (
	"context"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A session's process tells it how much of its output waits unread (#902:
// a line to a sign-in screen waits until the screen it is judged on holds
// all of it), through the server's guard, from the native Runtime's
// terminal; a Runtime's terminal that cannot tell says so with an error,
// and the session then takes no such line.
func TestAGuardedTerminalTellsItsOutputNotReadYet(t *testing.T) {
	e := runtimeEnv(t, nativeSeam(), nil)
	g := guardRuntime{s: e.srv, name: "fake"}
	nat, err := local.Native().StartTerminal(t.Context(), ext.TerminalSpec{Argv: []string{"/bin/sh", "-c", "printf abc; exec sleep 30"}, Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nat.Stop(context.Background(), time.Second) })
	type unread interface{ Unread() (int, error) }
	p, ok := session.RuntimeProcess(g.terminal(nat)).(unread)
	if !ok {
		t.Fatal("the session's process cannot be asked")
	}
	deadline := time.Now().Add(testWait)
	for {
		n, err := p.Unread()
		if err != nil {
			t.Fatal(err)
		}
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unread %d, want 3", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	fake := session.RuntimeProcess(g.terminal(exttest.NewFakeTerminal(ext.ProcessInfo{}))).(unread)
	if _, err := fake.Unread(); err == nil {
		t.Fatal("a terminal that cannot tell answered")
	}
}
