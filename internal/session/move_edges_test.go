package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The move's edges (the bundle's review of row 5): a message as a move
// begins, a restart's background work, a signed-out chat, a Stop during a
// move, the failed start's child, a question answered while a move waits,
// a restart Interrupt made.

// A message that reaches a chat as a move begins (it passed Submit's look
// before the move began) is refused moving under the lock, never queued on
// the way that ends; one queued there all the same is told moving, its
// claim let go, and nothing of it reaches the child that ends.
func TestMoveRefusesAMessageAsItBegins(t *testing.T) {
	t.Run("refused under the lock", func(t *testing.T) {
		s, _ := movable(t, 0)
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		toChat(t, s, f, ts)
		gate := make(chan struct{})
		ts.before = func() { <-gate }
		t.Cleanup(func() {
			select {
			case <-gate:
			default:
				close(gate)
			}
		})
		if _, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil {
			t.Fatal(err)
		}
		p := s.way.Load()
		if p == nil {
			t.Fatal("the chat left before the test looked")
		}
		words := make(chan finishWord, 1)
		if _, err := s.submitProtocol(p, Submission{Text: "hello", Finished: finishedInto(words)}); !errors.Is(err, ErrMoving) {
			t.Fatalf("a message as the move began: %v, want ErrMoving", err)
		}
		close(gate)
		settled(t, s)
		if n := len(f.Sent()); n != 0 {
			t.Fatalf("%d messages written to the child that ended", n)
		}
	})
	t.Run("the way left finishes what waits", func(t *testing.T) {
		s, _ := movable(t, 0)
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		toChat(t, s, f, ts)
		words := make(chan finishWord, 1)
		var out applied
		s.mu.Lock()
		p := s.way.Load()
		q := &queuedMessage{sub: Submission{Text: "late", Finished: finishedInto(words)}, msg: Message{Ref: NewID(), Text: "late"}, state: PromptQueued}
		q.seq = s.addEntryLocked(TranscriptEntry{Kind: EntryPrompt, Text: "late", State: PromptQueued}, &out)
		p.queue = append(p.queue, q)
		s.beginMoveLocked()
		s.mu.Unlock()
		s.sendApplied(out)
		go s.move(WayProtocol, WayTerminal, MoveStarts{Terminal: ts.start}, "")
		w := takeWord(t, words)
		if !errors.Is(w.err, ErrMoving) {
			t.Fatalf("the queued message's word: %v, want ErrMoving", w.err)
		}
		settled(t, s)
		if n := len(f.Sent()); n != 0 {
			t.Fatalf("%d messages written to the child that ended", n)
		}
		if e := entryOf(t, s, q.seq); e.State != PromptWithdrawn {
			t.Fatalf("its entry: %q", e.State)
		}
	})
}

// A child started again (an interrupt the agent cannot take) starts with no
// background work of the old one's: a move to the terminal is made at rest.
func TestRestartClearsBackgroundWork(t *testing.T) {
	s, _ := movable(t, 0)
	ts := &terminals{}
	ts.cleanup(t)
	f, again := sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol()
	f.InterruptErr = ErrNoInterrupt
	if _, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(again), Terminal: ts.start}); err != nil {
		t.Fatal(err)
	}
	settled(t, s)
	f.TurnStarted("t")
	f.Background(true)
	if err := s.Interrupt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, again, "the new child's begin", func(c []string) bool { return sessiontest.Count(c, "begin") == 1 })
	moveWaitFor(t, "the restart's end", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.way.Load()
		return p != nil && !p.restarting
	})
	if s.BackgroundWork() {
		t.Fatal("the old child's background work outlived its restart")
	}
	if st, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil || !st.Moving {
		t.Fatalf("the move after the restart: %+v, %v", st, err)
	}
	settled(t, s)
	if s.Way() != WayTerminal {
		t.Fatalf("way %q", s.Way())
	}
}

// notSignedIn is a driver's Read error that sends the session back to its
// terminal, as Claude Code's not-signed-in error does.
type notSignedIn struct{}

func (notSignedIn) Error() string        { return "The agent is not signed in; sign in from its terminal." }
func (notSignedIn) BackToTerminal() bool { return true }

// A chat whose agent says it is not signed in goes back to its terminal on
// the same conversation with that sentence; the session does not end.
func TestSignedOutChatGoesBackToItsTerminal(t *testing.T) {
	s, _ := movable(t, 0)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Ada"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	toChat(t, s, f, ts)
	f.Fail(notSignedIn{})
	w := wayMsg(t, sink, "back", func(w proto.Way) bool { return w.Way == proto.WayTerminal })
	settled(t, s)
	if w.Why != (notSignedIn{}).Error() {
		t.Fatalf("the way message: %+v", w)
	}
	if s.Info().Status.Ended() || s.Way() != WayTerminal {
		t.Fatalf("status %q, way %q", s.Info().Status, s.Way())
	}
	if got := ts.started(); len(got) != 1 || got[0] != "conv-1" {
		t.Fatalf("the terminal back on %v, want [conv-1]", got)
	}
}

// A Stop during a slow move cancels the move's start, starts nothing after
// it, and answers as a Stop: the session ends stopped.
func TestStopDuringAMoveStartsNothing(t *testing.T) {
	s, _ := movable(t, 0)
	ts := &terminals{}
	ts.cleanup(t)
	starting := make(chan struct{})
	if _, err := s.SwapWay(WayProtocol, MoveStarts{
		Protocol: func(ctx context.Context, _ string) (Child, Protocol, error) {
			close(starting)
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
		Terminal: ts.start,
	}); err != nil {
		t.Fatal(err)
	}
	<-starting
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop during the move: %v", err)
	}
	if st := s.Info().Status; st != StatusStopped {
		t.Fatalf("status %q, want stopped", st)
	}
	if got := ts.started(); len(got) != 0 {
		t.Fatalf("a terminal started after the Stop: %v", got)
	}
}

// I1 on the way back: a chat's start whose child never became ready has its
// child's group proven gone before the terminal starts again.
func TestMoveBackProvesTheFailedChildGone(t *testing.T) {
	s, _ := movable(t, 0)
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	f.BeginErr = errors.New("no initialize answer")
	f.Child().Group = 4201
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	var proving, released atomic.Bool
	ts.before = func() {
		if !released.Load() {
			t.Error("the terminal started before the failed child's group was proven gone")
		}
	}
	gone := func(_ context.Context, group int) error {
		if group == 4201 {
			proving.Store(true)
			<-gate
		}
		return nil
	}
	if _, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(), Terminal: ts.start, Gone: gone}); err != nil {
		t.Fatal(err)
	}
	moveWaitFor(t, "the failed child's group asked", proving.Load)
	if n := len(ts.started()); n != 0 {
		t.Fatalf("%d terminals before the proof", n)
	}
	released.Store(true)
	close(gate)
	settled(t, s)
	if s.Way() != WayTerminal || len(ts.started()) != 1 || s.Info().Status.Ended() {
		t.Fatalf("way %q, terminals %v, status %q", s.Way(), ts.started(), s.Info().Status)
	}
}

// A move noted while a question waits says the request's sentence; once the
// question is answered through Conductor it says the turn's.
func TestAnsweredQuestionRetriesTheNotedMove(t *testing.T) {
	s, _ := movable(t, 0)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Ada", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	s.SetAttentionFull(AttentionNeedsInput, "Run a command?", SourceAPI, KindPermission, []Option{{Label: "Yes", Input: "1"}})
	id := s.Info().Attention.ID
	if id == "" {
		t.Fatal("no question")
	}
	st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: sessiontest.NewFakeProtocol().Start()})
	if err != nil || st.Why != RequestWaitsWords {
		t.Fatalf("pressed: %+v, %v", st, err)
	}
	if err := s.AnswerInput(context.Background(), sub, id, []byte("1")); err != nil {
		t.Fatal(err)
	}
	wayMsg(t, sink, "the turn's sentence", func(w proto.Way) bool { return w.Noted == proto.WayProtocol && w.Why == MovesToChatWords })
	if to, why := s.Noted(); to != WayProtocol || why != MovesToChatWords {
		t.Fatalf("noted %q %q", to, why)
	}
}

// A restart Interrupt made, while a restart at the turn's end was asked
// for, is that restart: the next turn's end starts nothing again.
func TestInterruptRestartClearsTheTurnEndRestart(t *testing.T) {
	s, _ := movable(t, 0)
	ts := &terminals{}
	ts.cleanup(t)
	f, again, third := sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol()
	f.InterruptErr = ErrNoInterrupt
	toChatWith(t, s, f.Start(again, third), ts)
	f.TurnStarted("t")
	if err := s.RestartProtocol("restarted", "Started again."); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, again, "the new child's begin", func(c []string) bool { return sessiontest.Count(c, "begin") == 1 })
	moveWaitFor(t, "the restart's end", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := s.way.Load()
		return p != nil && !p.restarting
	})
	again.TurnStarted("t2")
	again.TurnEnded("done", Usage{})
	s.mu.Lock()
	restarting := s.way.Load().restarting
	s.mu.Unlock()
	if restarting {
		t.Fatal("the next turn's end started the child again")
	}
	if on := third.StartedOn(); on != "" {
		t.Fatalf("a third child started on %q", on)
	}
}

// toChatWith moves s to a chat started by start, at rest.
func toChatWith(t *testing.T, s *Local, start ProtocolStart, ts *terminals) {
	t.Helper()
	if _, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: start, Terminal: ts.start}); err != nil {
		t.Fatal(err)
	}
	settled(t, s)
	if s.Way() != WayProtocol {
		t.Fatalf("way %q", s.Way())
	}
}
