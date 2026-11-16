package crew

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A member's turn's end told twice, both reports naming the turn (Codex's
// Stop hook, then its notify program), around the engine's next line (the
// answer of the decision the member ended its turn to wait for, typed and
// entered): the engine takes the end once. The second report is not read
// as the end of the turn the answer began, which would remind the member,
// at work on the answer, that a done does not finish its task; that
// turn's own end, under the next turn's name, reminds it once.
func TestAMembersEndToldTwiceAroundItsNextLineIsTakenOnce(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, p := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	q := mirrorOf(t, l, d.ID)
	const turn, next = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
	end := func(words, name string) {
		t.Helper()
		if err := l.TrySetAttentionTurn(session.AttentionDone, words, session.SourceAPI, session.KindDone, nil, "", name); err != nil {
			t.Fatal(err)
		}
	}
	// The member ends its turn to wait for the decision: its hook's report.
	end("Waiting for the merge decision.", turn)
	answer(t, e, d.ID, Response{ResponseID: "r1", Choice: choice(0), ExpectedRev: 1})
	decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	lineWith(t, p, "Decision "+d.ID)
	infoWhen(t, l, "past the answered question", func(info session.Info) bool { return info.Attention.ID != q.ID })
	// The same end, from the notify program, after the answer's Enter. The
	// session hands its entries to the engine as it records them, so a
	// reminder it caused would be noted by now.
	end("Waiting for the merge decision.", turn)
	if n := logCount(t, e, run.ID, "reports done without a result"); n != 0 {
		t.Fatalf("the end told again was taken as the answer's turn's end: %d reminders", n)
	}
	// The answer's turn ends: the reminder, once.
	end("Merged.", next)
	waitLogged(t, e, run.ID, "reports done without a result; reminded once", 1, 10*time.Second)
	lineWith(t, p, "a done does not finish task")
	stopRun(t, e, run.ID)
}
