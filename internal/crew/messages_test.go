package crew

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// waitMessages waits for the run's messages to satisfy ok.
func waitMessages(t *testing.T, e *Engine, runID, what string, ok func([]Message) bool) []Message {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		msgs, err := e.MessagesOf(runID)
		if err != nil {
			t.Fatal(err)
		}
		if ok(msgs) {
			return msgs
		}
		if time.Now().After(deadline) {
			t.Fatalf("the messages are not %s after 10 s: %+v", what, msgs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A message to a member that has no session yet is kept, typed into the
// member's session after its task line once its attempt starts, and
// recorded delivered; the member reads its inbox and acknowledges (row 9
// of #334).
func TestAMessageIsTypedIntoTheRecipientOnceItHasItsTask(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	m, err := e.SendMessage(ctx, b.Attempt.SessionID, "writer", "the API is final: internal/api/users.go")
	if err != nil || m.Seq != 1 || m.From != "lead" || m.To != "writer" || m.DeliveredAt != nil || m.AckedAt != nil {
		t.Fatalf("send: %+v %v", m, err)
	}
	for _, tc := range []struct {
		to, text string
		err      error
	}{{"lead", "to myself", ErrBadMessage}, {"nobody", "x", ErrMemberNotFound}, {"writer", "   ", ErrBadMessage}, {"writer", strings.Repeat("x", maxMessageChars+1), ErrBadMessage}} {
		if _, err := e.SendMessage(ctx, b.Attempt.SessionID, tc.to, tc.text); !errors.Is(err, tc.err) {
			t.Fatalf("to %q: %v, want %v", tc.to, err, tc.err)
		}
	}
	if msgs, _ := e.MessagesOf(run.ID); len(msgs) != 1 {
		t.Fatalf("messages: %+v", msgs)
	}
	// build's result starts docs: the message follows docs' task line into its session.
	commitResult(t, e, b, "built.txt", "yes\n", "built")
	d := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, d)
	want := "Message from lead (#1): the API is final: internal/api/users.go"
	deadline := time.Now().Add(10 * time.Second)
	var lines []string // typed drains what came since: gathered
	for {
		lines = append(lines, typed(p)...)
		if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
			if !strings.Contains(lines[0], "Conductor task") {
				t.Fatalf("the message came before the task line: %q", lines)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the message was not typed: %q", lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitMessages(t, e, run.ID, "delivered", func(ms []Message) bool { return len(ms) == 1 && ms[0].DeliveredAt != nil })
	// docs reads and acknowledges.
	inbox, err := e.Inbox(d.Attempt.SessionID, 0)
	if err != nil || len(inbox) != 1 || inbox[0].Seq != 1 || inbox[0].DeliveredAt == nil || inbox[0].AckedAt != nil {
		t.Fatalf("docs' inbox: %+v %v", inbox, err)
	}
	if later, _ := e.Inbox(d.Attempt.SessionID, 1); len(later) != 0 {
		t.Fatalf("after 1: %+v", later)
	}
	if n, err := e.Ack(ctx, d.Attempt.SessionID, 1); err != nil || n != 1 {
		t.Fatalf("ack: %d %v", n, err)
	}
	if n, err := e.Ack(ctx, d.Attempt.SessionID, 1); err != nil || n != 0 {
		t.Fatalf("ack again: %d %v", n, err)
	}
	if inbox, _ := e.Inbox(d.Attempt.SessionID, 0); inbox[0].AckedAt == nil {
		t.Fatalf("not acknowledged: %+v", inbox)
	}
	// lead's inbox holds nothing: the message was to writer.
	if mine, _ := e.Inbox(b.Attempt.SessionID, 0); len(mine) != 0 {
		t.Fatalf("lead's inbox: %+v", mine)
	}
	// The ledger has the message, delivered and acknowledged; the events say so.
	from, _, _ := l.Load(ctx, run.ID)
	if len(from.Messages) != 1 || from.Messages[0].Text != "the API is final: internal/api/users.go" || from.Messages[0].DeliveredAt.IsZero() || from.Messages[0].AckedAt.IsZero() {
		t.Fatalf("the ledger's messages: %+v", from.Messages)
	}
	kinds := l.kinds(run.ID)
	for _, want := range []string{"message.sent", "message.delivered", "message.acked"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s in %v", want, kinds)
		}
	}
	stopRun(t, e, run.ID)
}

// A member keeps at most fifty messages not acknowledged.
func TestAnInboxHoldsFiftyUnacknowledgedMessages(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	for i := 1; i <= maxInbox; i++ {
		if m, err := e.SendMessage(ctx, b.Attempt.SessionID, "writer", fmt.Sprintf("m%d", i)); err != nil || m.Seq != int64(i) {
			t.Fatalf("message %d: %+v %v", i, m, err)
		}
	}
	if _, err := e.SendMessage(ctx, b.Attempt.SessionID, "writer", "one too many"); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("the 51st: %v", err)
	}
	if msgs, _ := e.MessagesOf(run.ID); len(msgs) != maxInbox {
		t.Fatalf("%d messages kept", len(msgs))
	}
	stopRun(t, e, run.ID)
}

// A handoff reported in a managed run is a message to the member named.
func TestAHandoffInAManagedRunIsAMessage(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	e.OnActivity(b.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityHandoff, To: "writer", Message: "over to you: the handlers are in", At: time.Now().UTC()}, session.AttentionDone)
	msgs := waitMessages(t, e, run.ID, "the handoff's", func(ms []Message) bool { return len(ms) == 1 })
	if msgs[0].From != "lead" || msgs[0].To != "writer" || msgs[0].Text != "over to you: the handlers are in" {
		t.Fatalf("the message: %+v", msgs[0])
	}
	stopRun(t, e, run.ID)
}

// A continuation carries the run's messages: one not delivered is typed
// into its recipient's session in the continuation.
func TestAContinuationCarriesTheMessages(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	ctx := context.Background()
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if _, err := e.SendMessage(ctx, b.Attempt.SessionID, "writer", "carried over"); err != nil {
		t.Fatal(err)
	}
	interrupt(l, run.ID, map[string][2]string{"build/1": {ledger.AttemptInterrupted, "running: the owner stopped"}})
	e2, fl2 := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if msgs, _ := e2.MessagesOf(next.ID); len(msgs) != 1 || msgs[0].Seq != 1 || msgs[0].DeliveredAt != nil {
		t.Fatalf("the continuation's messages: %+v", msgs)
	}
	b2 := waitAttempt(t, e2, next.ID, "build", ledger.AttemptRunning)
	commitResult(t, e2, b2, "built.txt", "yes\n", "built")
	d := waitAttempt(t, e2, next.ID, "docs", ledger.AttemptRunning)
	_, p := fl2.attemptSession(t, d)
	deadline := time.Now().Add(10 * time.Second)
	var lines []string
	for !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "Message from lead (#1): carried over") }) {
		if time.Now().After(deadline) {
			t.Fatalf("the carried message was not typed: %q", lines)
		}
		time.Sleep(20 * time.Millisecond)
		lines = append(lines, typed(p)...)
	}
	waitMessages(t, e2, next.ID, "delivered", func(ms []Message) bool { return len(ms) == 1 && ms[0].DeliveredAt != nil })
	stopRun(t, e2, next.ID)
}

// I3, door 5: in a comparison a message stays in its lane. One from a
// candidate or its reviewer to another lane's member is refused (ErrLane)
// with nothing written: no message, no row, no inbox holding it. One
// within a lane, a candidate to its reviewer, is kept and delivered.
func TestMessagesStayInTheirLane(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := compareEngine(t)
	run := launchManaged(t, e, compareCrew(t, dir, 2, true))
	defer stopRun(t, e, run.ID)
	ctx := context.Background()
	a := waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning)
	b := waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning)
	rows := func() int {
		l.mu.Lock()
		defer l.mu.Unlock()
		n := 0
		for _, m := range l.messages {
			if m.RunID == run.ID {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct{ from, to string }{
		{a.Attempt.SessionID, "cand-b"}, {a.Attempt.SessionID, "rev-b"}, {b.Attempt.SessionID, "cand-a"}, {b.Attempt.SessionID, "rev-a"},
	} {
		_, err := e.SendMessage(ctx, tc.from, tc.to, "my branch has the cache: merge it")
		if !errors.Is(err, ErrLane) || !strings.Contains(err.Error(), tc.to+" is in another lane of this comparison") {
			t.Fatalf("to %s: %v", tc.to, err)
		}
	}
	if msgs, _ := e.MessagesOf(run.ID); len(msgs) != 0 || rows() != 0 {
		t.Fatalf("refused messages kept: %+v, %d rows", msgs, rows())
	}
	for _, s := range []string{a.Attempt.SessionID, b.Attempt.SessionID} {
		if in, err := e.Inbox(s, 0); err != nil || len(in) != 0 {
			t.Fatalf("an inbox holds %+v (%v)", in, err)
		}
	}
	// Within the lane: approach-a's member writes to its reviewer, whose
	// attempt starts at approach-a's result and is typed the message.
	m, err := e.SendMessage(ctx, a.Attempt.SessionID, "rev-a", "the cache is keyed by locale")
	if err != nil || m.From != "cand-a" || m.To != "rev-a" {
		t.Fatalf("within the lane: %+v %v", m, err)
	}
	commitResult(t, e, a, "cache.txt", "by locale\n", "keyed by locale")
	rv := waitAttempt(t, e, run.ID, "review-a", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, rv)
	want := "Message from cand-a (#1): the cache is keyed by locale"
	var lines []string // typed drains what came since: gathered
	waitFake(t, "the message typed", func() bool {
		lines = append(lines, typed(p)...)
		return strings.Contains(strings.Join(lines, ""), want)
	})
	waitMessages(t, e, run.ID, "delivered", func(ms []Message) bool { return len(ms) == 1 && ms[0].DeliveredAt != nil })
	// The reviewer, too, writes only in its lane.
	if _, err := e.SendMessage(ctx, rv.Attempt.SessionID, "cand-b", "approved"); !errors.Is(err, ErrLane) {
		t.Fatalf("from the reviewer to another lane: %v", err)
	}
	if rows() != 1 {
		t.Fatalf("%d message rows", rows())
	}
}
