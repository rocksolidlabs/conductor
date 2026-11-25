package api

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A Chat run a registered Runtime without pipes takes stays in that
// Runtime, as every run it takes does: its attempts start there, in their
// terminals, and the run's log says why (docs/plans/chat-or-terminal.md,
// design 8, A run keeps its Runtime).
func TestPipelessRuntimeKeepsTheRunInATerminal(t *testing.T) {
	takesAnswered(t)
	seam := nativeSeam()
	offered := &sessiontest.FakeRuntime{Base: local.Native(), TakesRun: func(ext.ProcessRef) bool { return true }}
	if _, ok := any(offered).(ext.PipeStarter); ok {
		t.Fatal("the test's Runtime has pipes")
	}
	e := runtimeEnv(t, seam, nil, withRuntime(offered))
	e.gitRepo(t)
	c := oneTaskCrew("one", e.root)
	c.Way = crew.WayChat
	runID := launchOne(t, e, c)
	sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
	if got, want := terminalRefs(offered), []ext.ProcessRef{attemptRef(sid, runID, 1)}; !slices.Equal(got, want) {
		t.Fatalf("the attempt's session started in %+v, want %+v", got, want)
	}
	if got := terminalRefs(seam); len(got) != 0 {
		t.Fatalf("the native Runtime started %+v", got)
	}
	if l, ok := e.srv.lookupLocal(sid); !ok || l.Way() != session.WayTerminal {
		t.Fatalf("the attempt's session: %v", ok)
	}
	const line = "lead (task build, attempt 1): in its terminal: the run's Runtime has no pipes."
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, _ := e.srv.runs.Get(runID)
		var log []string
		for _, en := range run.Log {
			log = append(log, en.Message)
		}
		if slices.Contains(log, line) {
			if run.Way != crew.WayChat || slices.ContainsFunc(log, func(s string) bool { return strings.Contains(s, "runs as a chat") }) {
				t.Fatalf("the run: way %q, log %q", run.Way, log)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run's log never said %q: %q", line, log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
