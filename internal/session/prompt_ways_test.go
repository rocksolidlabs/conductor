package session

import (
	"context"
	"errors"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A line typed only at the agent's prompt (promptOnly) and the session's
// two ways to run its agent (move.go): a chat types nothing into a
// terminal, a terminal moved back to reads its screen from its first
// frame, and a move waits for what holds the turn.

// shapedMovable is a server session of the agent a's, on a resumable
// conversation, a controlling viewer attached, its first line ownLine
// entered at its prompt and its first turn ended: at rest, a move may be
// made.
func shapedMovable(t *testing.T, a shapedAgent, opts Options) (*Local, *fakeProc, *Subscription) {
	t.Helper()
	opts.PromptShape = builtInShape(t, a.id)
	opts.TrustPattern = regexp.MustCompile(`Trust\s*this\s*folder\?`)
	opts.ScrollbackBytes = 4096
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true}}, p, quiet(opts))
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		p.exit()
	})
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	draw(t, s, p, a.prompt)
	if res, err := s.Submit(t.Context(), Submission{Text: ownLine, By: sub}); err != nil || !res.Entered {
		t.Fatalf("the first line: %+v %v", res, err)
	}
	typed(t, p, ownLine)
	s.SetAttention(AttentionDone, "a turn of its own", SourceAPI)
	return s, p, sub
}

// TestALineInAChatTypesNothingIntoATerminal: a session of an agent with a
// prompt shape, in a chat, its terminal's screen never drawn: a person's
// numbered line goes to the chat's child, is entered at its word, nothing
// is written to a terminal, the screen is not read for it, and the line is
// not kept as one the agent shows in a terminal's history.
func TestALineInAChatTypesNothingIntoATerminal(t *testing.T) {
	for _, a := range shapedAgents() {
		t.Run(a.id, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{PromptShape: builtInShape(t, a.id), ScrollbackBytes: 4096}))
			sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			f := sessiontest.NewFakeProtocol()
			if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Stop(context.Background()) })
			words := make(chan finishWord, 2)
			_, err = s.Submit(t.Context(), Submission{Text: ownLine, By: sub, Finished: finishedInto(words)})
			mustPending(t, err)
			waitCalls(t, f, "the write", sends(1))
			m := f.Sent()[0]
			if m.Text != ownLine {
				t.Fatalf("sent %+v", m)
			}
			f.Accepted(m.Ref)
			if w := takeWord(t, words); w.err != nil || !w.res.Entered || w.res.Typed || w.res.NotAtPrompt {
				t.Fatalf("the chat's word: %+v", w)
			}
			nothingIn(t, s, p)
			if s.enteredRow("❯ " + ownLine) {
				t.Fatal("a chat's line is kept as a terminal's")
			}
		})
	}
}

// TestATerminalMovedBackReadsItsScreenFromItsFirstFrame: a session moved
// to a chat and back runs a new terminal on the same conversation. The
// startup watch is not armed again: until the new terminal draws its
// prompt, a line is refused, nothing written; once it draws its history,
// the numbered line the session entered before the move among it, the
// next line is typed and entered.
func TestATerminalMovedBackReadsItsScreenFromItsFirstFrame(t *testing.T) {
	for _, a := range shapedAgents() {
		t.Run(a.id, func(t *testing.T) {
			s, _, sub := shapedMovable(t, a, Options{})
			ts := &terminals{}
			ts.cleanup(t)
			toChat(t, s, sessiontest.NewFakeProtocol(), ts)
			if _, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil {
				t.Fatal(err)
			}
			settled(t, s)
			if s.Way() != WayTerminal || len(ts.started()) != 1 {
				t.Fatalf("way %q, %d terminals", s.Way(), len(ts.started()))
			}
			ts.mu.Lock()
			p := ts.procs[0]
			ts.mu.Unlock()
			if s.trust.Watching() {
				t.Fatal("the startup watch is armed again for the terminal moved back to")
			}
			if res, err := s.Submit(t.Context(), Submission{Text: "now run the tests", By: sub}); !errors.Is(err, ErrNotAtPrompt) || res.Typed {
				t.Fatalf("a line before the new terminal draws its prompt: %+v %v", res, err)
			}
			nothingIn(t, s, p)
			draw(t, s, p, history(a.id, ownLine, false))
			if res, err := s.Submit(t.Context(), Submission{Text: "now run the tests", By: sub}); err != nil || !res.Entered {
				t.Fatalf("a line under the redrawn history: %+v %v", res, err)
			}
			typed(t, p, "now run the tests")
			nothingIn(t, s, p)
		})
	}
}

// TestAMoveWaitsForAHeldTurnAndAnEnterUnderWay: a move pressed while the
// session's turn is held for a line its screen refused the Enter of, or
// while a line's Enter is under way past its wait, is noted, not made:
// nothing more is typed, the line's Enter ends one way, its word given,
// and the move is made once the session is at rest again.
func TestAMoveWaitsForAHeldTurnAndAnEnterUnderWay(t *testing.T) {
	a := shapedAgents()[0]
	t.Run("a held turn", func(t *testing.T) {
		s, p, sub := shapedMovable(t, a, Options{})
		var shown atomic.Bool
		hook := func() { shown.Store(drawsOn(s, p, pickFrame)) }
		s.beforeEnter.Store(&hook)
		res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", By: sub})
		s.beforeEnter.Store(nil)
		if !shown.Load() || !errors.Is(err, ErrLineStranded) || res.Entered {
			t.Fatalf("the line: %+v %v", res, err)
		}
		if got := written(t, p); got != "fix the bug" {
			t.Fatalf("written %q", got)
		}
		waitAttention(t, s, func(a Attention) bool { return a.Source == SourceSubmit })
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(), Terminal: ts.start, Gone: GroupGone})
		if err != nil || st.Moving || st.Noted != WayProtocol {
			t.Fatalf("a move while the turn is held: %+v %v", st, err)
		}
		if s.Way() != WayTerminal || s.Moving() || !s.TurnHeld() {
			t.Fatalf("way %q, moving %v, held %v", s.Way(), s.Moving(), s.TurnHeld())
		}
		nothingIn(t, s, p)
		// A person deals with the text in the terminal; the agent's turn
		// that follows ends: the move is made.
		if err := s.Input(sub, []byte("\r")); err != nil {
			t.Fatal(err)
		}
		if got := written(t, p); got != "\r" {
			t.Fatalf("written %q", got)
		}
		moveWaitFor(t, "the hold's end", func() bool { return !s.TurnHeld() })
		// The turn the hold took is given back as its goroutine ends; the
		// agent's done can come first. Taken here for that moment (the
		// session's turn is a slot), the done comes while it is taken and
		// the move stays noted; the turn given back makes it.
		s.submitting <- struct{}{}
		s.SetAttention(AttentionDone, "its turn", SourceAPI)
		if s.Way() != WayTerminal || s.Moving() {
			t.Fatalf("moved with the turn taken: way %q, moving %v", s.Way(), s.Moving())
		}
		s.giveTurn()
		moveWaitFor(t, "the move made", func() bool { return s.Way() == WayProtocol && !s.Moving() })
	})
	t.Run("an Enter under way", func(t *testing.T) {
		s, p, _ := shapedMovable(t, a, Options{InputWait: 300 * time.Millisecond})
		gate := make(chan struct{})
		var once atomic.Bool
		p.onBytes = func(b []byte) {
			if string(b) == "\r" && once.CompareAndSwap(false, true) {
				<-gate
			}
		}
		words := make(chan error, 2)
		res, err := s.Submit(t.Context(), Submission{Text: "fix the bug", ByName: "crew", Finished: func(r SubmitResult, err error) {
			if err == nil && !r.Entered {
				err = errors.New("not entered")
			}
			words <- err
		}})
		if !errors.Is(err, ErrSubmitPending) || res.Entered {
			t.Fatalf("the line: %+v %v", res, err)
		}
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(), Terminal: ts.start, Gone: GroupGone})
		if err != nil || st.Moving || st.Noted != WayProtocol {
			t.Fatalf("a move while an Enter is under way: %+v %v", st, err)
		}
		close(gate)
		if err := <-words; err != nil {
			t.Fatalf("the line's word: %v", err)
		}
		typed(t, p, "fix the bug")
		if s.TurnHeld() || s.Moving() || s.Way() != WayTerminal {
			t.Fatalf("held %v, moving %v, way %q", s.TurnHeld(), s.Moving(), s.Way())
		}
		select {
		case err := <-words:
			t.Fatalf("a second word: %v", err)
		default:
		}
		s.SetAttention(AttentionDone, "its turn", SourceAPI)
		moveWaitFor(t, "the move made", func() bool { return s.Way() == WayProtocol && !s.Moving() })
	})
}
