package crew

import (
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A handoff reaches only a member of the reporting session's own run: a
// name of another run's member is no member of this run, noted in its log
// and typed nowhere (neither into that other member nor into anyone here);
// in a managed run, a handoff to such a name is no message. A chat's floor
// lets an agent report one without asking because of this bound
// (agents.ProtocolUnaskedTools); a reporter's own name and an unknown name
// are TestHandoffToItselfLogged's and TestHandoffUnknownMemberLogged's,
// and a session in no run, ignored, is TestHandoffUnknownMemberLogged's.
func TestAHandoffReachesOnlyItsOwnRun(t *testing.T) {
	e, fl := newEngine(t)
	run := runningPair(t, e, fl)
	other, err := e.Launch(t.Context(), testCrew(immediate("outsider", "Wait.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, other.ID, "outsider", MemberRunning, 5*time.Second)
	_, po := fl.member("outsider")
	typed(po) // its prompt
	lead, pl := fl.member("lead")
	_, pt := fl.member("tests")
	lead.Record(handoffEntry("outsider", "over to you"))
	if n := logCount(t, e, run.ID, `handoff to unknown member "outsider" from lead`); n != 1 {
		t.Fatalf("%d notes in the reporter's run", n)
	}
	// The other run's log says nothing of it, and nothing is typed into
	// anyone; a handoff within the run, after, is the next thing typed.
	if n := logCount(t, e, other.ID, "handoff"); n != 0 {
		t.Fatalf("the other run noted %d handoffs", n)
	}
	lead.Record(handoffEntry("tests", "for you"))
	if got := waitTyped(t, pt, 5*time.Second); got != "Handoff from lead: for you\r" {
		t.Fatalf("typed into tests %q", got)
	}
	for name, p := range map[string]func() []string{"outsider": func() []string { return typed(po) }, "lead": func() []string { return typed(pl) }} {
		if got := p(); len(got) != 0 {
			t.Fatalf("typed into %s %q", name, got)
		}
	}
}

// In a managed run a handoff to a name no member of the run has is no
// message: here the name is a member of another run on the same engine,
// which is typed nothing and keeps nothing, and the reporter's run notes
// the handoff not sent and keeps no message.
func TestAManagedHandoffToANameOutsideTheRunIsNoMessage(t *testing.T) {
	e, fl, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(newRepo(t), work("build", "lead"), work("docs", "writer", "build")))
	defer stopRun(t, e, run.ID)
	other := launchManaged(t, e, repoCrew(newRepo(t), work("other", "outsider")))
	defer stopRun(t, e, other.ID)
	o := waitAttempt(t, e, other.ID, "other", ledger.AttemptRunning)
	_, po := fl.attemptSession(t, o)
	typed(po) // its task line
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	e.OnActivity(b.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityHandoff, To: "outsider", Message: "over to you", At: time.Now().UTC()}, session.AttentionDone, "")
	waitLogged(t, e, run.ID, "the handoff from lead to outsider was not sent", 1, 10*time.Second)
	for _, id := range []string{run.ID, other.ID} {
		if msgs, _ := e.MessagesOf(id); len(msgs) != 0 {
			t.Fatalf("run %s kept a message: %+v", id, msgs)
		}
	}
	if n := logCount(t, e, other.ID, "handoff"); n != 0 {
		t.Fatalf("the other run noted %d handoffs", n)
	}
	for _, line := range typed(po) {
		if strings.Contains(line, "over to you") {
			t.Fatalf("the other run's member was typed %q", line)
		}
	}
}
