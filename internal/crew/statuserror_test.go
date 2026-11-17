package crew

import (
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// programStatus is an agent's program status as Codex writes it (OSC 7501).
func programStatus(state string) string {
	return "\x1b]7501;state=" + state + ":app=codex\x1b\\"
}

// #991: a member whose agent's turn ends in an error (its program status
// says working, then error) waits for a person as any question does: the
// run shows it needing input, its task stays running on the same attempt
// (neither failed nor tried again), and the program working again lets the
// wait go.
func TestAMemberWhoseTurnEndedInAnErrorWaitsForAPerson(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	l, p := fl.attemptSession(t, b)
	if err := p.Print(programStatus("working") + "Error: unexpected status 401 Unauthorized\r\n" + programStatus("error")); err != nil {
		t.Fatal(err)
	}
	waitAttention(t, l, "the failed turn's wait", func(a session.Attention) bool {
		return a.State == session.AttentionNeedsInput && a.Source == session.SourceStatus && a.Message == "The agent's turn ended in an error; its terminal says why"
	})
	waitTrue(t, "the run needs input", func() bool {
		r, ok := e.Get(run.ID)
		return ok && r.State == RunNeedsInput && r.NeedsInput == 1
	})
	ts := taskOf(t, e, run.ID, "build")
	if ts.State != ledger.TaskRunning || ts.Attempts != 1 || ts.Attempt == nil || ts.Attempt.N != 1 || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("the task after the failed turn: %+v %+v", ts, ts.Attempt)
	}
	if err := p.Print(programStatus("working")); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the run running again", func() bool {
		r, ok := e.Get(run.ID)
		return ok && r.State == RunRunning && r.NeedsInput == 0
	})
	stopRun(t, e, run.ID)
}
