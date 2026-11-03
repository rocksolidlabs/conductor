//go:build unix

package sessiontest

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// A FakeRuntime with PanicAfterStart starts through its Base, then ends and
// reaps what its Base started before it panics: the panic is its own, and
// by the time it is recovered the process is gone, reaped, its pid no
// process's.
func TestFakeRuntimePanicsAfterStartLeavingNothing(t *testing.T) {
	dir := t.TempDir()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := &FakeRuntime{Base: local.Native(), PanicAfterStart: true}
	ref := ext.ProcessRef{Session: "s-1", Run: "r-1", Task: "build", Attempt: 1, Tree: "worktree"}
	func() {
		defer func() {
			if v := recover(); v != PanicAfterStartValue {
				t.Fatalf("recovered %v", v)
			}
		}()
		f.StartTerminal(t.Context(), ext.TerminalSpec{Argv: []string{"sleep", "60"}, Dir: dir, DirFile: d, Env: []string{"PATH=" + os.Getenv("PATH")}, Ref: ref})
		t.Fatal("StartTerminal returned")
	}()
	c := f.Asked("StartTerminal")
	if len(c) != 1 || c[0].Ref != ref || c[0].Process.PID <= 0 {
		t.Fatalf("the start recorded: %+v", c)
	}
	if err := syscall.Kill(c[0].Process.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the process %d after the panic: %v", c[0].Process.PID, err)
	}
}
