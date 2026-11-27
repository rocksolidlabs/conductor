package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The move between ways inside the session (docs/plans/chat-or-terminal.md,
// design 9, row 5), against fake processes and a fake driver.

// movable is a server session in its terminal whose first turn has ended,
// on the conversation conv-1: at rest, a move may be made.
func movable(t *testing.T, group int) (*Local, *fakeProc) {
	t.Helper()
	p := newFakeProc()
	p.group = group
	s := NewLocal(Info{ID: "sess", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true}}, p, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		p.exit()
	})
	s.SetAttention(AttentionDone, "a turn of its own", SourceAPI)
	return s, p
}

// terminals is a MoveStarts.Terminal that starts fake terminals, each on the
// conversation it is given, failing once fail is set.
type terminals struct {
	mu    sync.Mutex
	procs []*fakeProc
	convs []string
	fail  error
	// before, when set, runs as a start begins.
	before func()
}

func (ts *terminals) start(_ context.Context, conv string, _, _ uint16) (Process, error) {
	if ts.before != nil {
		ts.before()
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.fail != nil {
		return nil, ts.fail
	}
	p := newFakeProc()
	ts.procs = append(ts.procs, p)
	ts.convs = append(ts.convs, conv)
	return p, nil
}

func (ts *terminals) started() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.convs...)
}

func (ts *terminals) cleanup(t *testing.T) {
	t.Cleanup(func() {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		for _, p := range ts.procs {
			p.exit()
		}
	})
}

// waitFor waits for ok; the bound only bounds a hang.
func moveWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settled waits for the move under way to end.
func settled(t *testing.T, s *Local) {
	t.Helper()
	moveWaitFor(t, "the move's end", func() bool { return !s.Moving() })
}

// wayMsg waits for a way message on sink that ok takes.
func wayMsg(t *testing.T, sink *chanSink, what string, ok func(proto.Way) bool) proto.Way {
	t.Helper()
	var got proto.Way
	m := waitControl(sink, func(m map[string]any) bool {
		if m["t"] != proto.CtlWay {
			return false
		}
		w := proto.Way{T: proto.CtlWay}
		w.Way, _ = m["way"].(string)
		w.Protocol, _ = m["protocol"].(string)
		w.Noted, _ = m["noted"].(string)
		w.Why, _ = m["why"].(string)
		if ok(w) {
			got = w
			return true
		}
		return false
	})
	if m == nil {
		t.Fatalf("no way message: %s", what)
	}
	return got
}

// toChat moves s to a chat with f's driver, at rest, and waits for it.
func toChat(t *testing.T, s *Local, f *sessiontest.FakeProtocol, ts *terminals) {
	t.Helper()
	st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(), Terminal: ts.start, Gone: GroupGone})
	if err != nil || !st.Moving {
		t.Fatalf("SwapWay = %+v, %v", st, err)
	}
	settled(t, s)
	if s.Way() != WayProtocol {
		t.Fatalf("way %q after the move", s.Way())
	}
}

// I1: the new child starts only after the old one's exit and its group's
// end, proven through the move's Gone; the seam after it (AfterOldExit)
// runs before the start. A start made earlier is seen by the gated Gone,
// every run, in both directions.
func TestMoveProvesTheOldProcessGone(t *testing.T) {
	type gated struct {
		released atomic.Bool
		started  atomic.Int32
		calls    chan int
		gate     chan struct{}
		open     sync.Once
		after    atomic.Bool
	}
	newGated := func(t *testing.T) *gated {
		g := &gated{calls: make(chan int, 1), gate: make(chan struct{})}
		// A test that fails while the move is held lets it go, so that the
		// session's stop at its cleanup does not wait for it for ever.
		t.Cleanup(func() { g.open.Do(func() { close(g.gate) }) })
		return g
	}
	gone := func(t *testing.T, g *gated, old <-chan struct{}) func(context.Context, int) error {
		return func(ctx context.Context, group int) error {
			select {
			case <-old:
			default:
				t.Error("the group was looked at before the old process exited")
			}
			if n := g.started.Load(); n != 0 {
				t.Errorf("%d new starts before the old group's end was proven", n)
			}
			g.calls <- group
			<-g.gate
			return nil
		}
	}
	check := func(t *testing.T, g *gated) {
		t.Helper()
		if !g.released.Load() {
			t.Error("the new way started before the old group was proven gone")
		}
		if !g.after.Load() {
			t.Error("the new way started before the seam after the old one's exit")
		}
		g.started.Add(1)
	}
	t.Run("terminal to chat", func(t *testing.T) {
		s, old := movable(t, 4101)
		g := newGated(t)
		f := sessiontest.NewFakeProtocol()
		start := f.Start()
		m := MoveStarts{
			Protocol: func(ctx context.Context, conv string) (Child, Protocol, error) {
				check(t, g)
				return start(ctx, conv)
			},
			Gone:         gone(t, g, old.Done()),
			AfterOldExit: func() { g.after.Store(true) },
		}
		if _, err := s.SwapWay(WayProtocol, m); err != nil {
			t.Fatal(err)
		}
		if group := <-g.calls; group != 4101 {
			t.Fatalf("proved group %d gone, want the terminal's 4101", group)
		}
		if g.started.Load() != 0 || s.Way() != WayTerminal {
			t.Fatal("the chat started while the old group was not yet proven gone")
		}
		g.released.Store(true)
		g.open.Do(func() { close(g.gate) })
		settled(t, s)
		if s.Way() != WayProtocol || g.started.Load() != 1 {
			t.Fatalf("way %q, %d starts", s.Way(), g.started.Load())
		}
	})
	t.Run("chat to terminal", func(t *testing.T) {
		s, _ := movable(t, 0)
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		f.Child().Group = 4102
		toChat(t, s, f, ts)
		g := newGated(t)
		ts.before = func() { check(t, g) }
		m := MoveStarts{Terminal: ts.start, Gone: gone(t, g, f.Child().Done()), AfterOldExit: func() { g.after.Store(true) }}
		if _, err := s.SwapWay(WayTerminal, m); err != nil {
			t.Fatal(err)
		}
		if group := <-g.calls; group != 4102 {
			t.Fatalf("proved group %d gone, want the child's 4102", group)
		}
		g.released.Store(true)
		g.open.Do(func() { close(g.gate) })
		settled(t, s)
		if s.Way() != WayTerminal || len(ts.started()) != 1 {
			t.Fatalf("way %q, terminals %v", s.Way(), ts.started())
		}
	})
	t.Run("no proof, no move", func(t *testing.T) {
		// A process that reports a group and a move given nothing to prove
		// it gone with: the session ends, nothing new starts.
		s, _ := movable(t, 4103)
		f := sessiontest.NewFakeProtocol()
		var started atomic.Bool
		start := f.Start()
		_, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: func(ctx context.Context, conv string) (Child, Protocol, error) {
			started.Store(true)
			return start(ctx, conv)
		}})
		if err != nil {
			t.Fatal(err)
		}
		settled(t, s)
		if started.Load() || !s.Info().Status.Ended() {
			t.Fatalf("started %v, status %q", started.Load(), s.Info().Status)
		}
	})
}

// GroupGone proves a real group gone: at once for a group none is in, and,
// for one whose member outlives its leader, once the member is killed.
func TestGroupGoneOfAGroupWithNoMember(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// No process has a group of the largest pid (far above pid_max).
	if err := GroupGone(ctx, 1<<30); err != nil {
		t.Fatal(err)
	}
}

// The conversation is read at the move: an id the stream changed after the
// first move is the one the move back resumes.
func TestMoveReadsTheIdAtTheMove(t *testing.T) {
	s, _ := movable(t, 0)
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	toChat(t, s, f, ts)
	if on := f.StartedOn(); on != "conv-1" {
		t.Fatalf("the chat started on %q", on)
	}
	f.Conversation("conv-2")
	f.TurnStarted("t")
	f.TurnEnded("done", Usage{})
	moveWaitFor(t, "the new id, resumable", func() bool {
		as := s.Info().AgentSession
		return as != nil && as.ID == "conv-2" && as.Resumable && !s.Moving()
	})
	moveWaitFor(t, "the turn's end shown", func() bool { return s.Info().Attention.State == AttentionDone })
	if _, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil {
		t.Fatal(err)
	}
	settled(t, s)
	if got := ts.started(); len(got) != 1 || got[0] != "conv-2" {
		t.Fatalf("the terminal resumed %v, want [conv-2]", got)
	}
}

// The session stays the one session: its id, its viewers and their
// connections, its people's chat, its activity; its way changes, the way
// message goes to every viewer and the boundary's notice is in the
// transcript, both ways.
func TestMoveKeepsTheSessionInside(t *testing.T) {
	s, _ := movable(t, 0)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Ada", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	if _, err := s.Chat(sub, proto.ChatPost{T: proto.CtlChat, Text: "before the move"}); err != nil {
		t.Fatal(err)
	}
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	toChat(t, s, f, ts)
	wayMsg(t, sink, "the chat", func(w proto.Way) bool {
		return w.Way == proto.WayProtocol && w.Protocol == "fake-protocol" && w.Noted == ""
	})
	if s.Info().ID != "sess" || s.Viewers() != 1 || s.Info().Way != WayProtocol {
		t.Fatalf("after the move: %q, %d viewers, way %q", s.Info().ID, s.Viewers(), s.Info().Way)
	}
	es := allEntries(t, s)
	if e := es[len(es)-1]; e.Kind != EntryNotice || e.Words != MovedToChatWords {
		t.Fatalf("the boundary: %+v", e)
	}
	if _, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil {
		t.Fatal(err)
	}
	settled(t, s)
	wayMsg(t, sink, "the terminal", func(w proto.Way) bool { return w.Way == proto.WayTerminal && w.Protocol == "" })
	es = allEntries(t, s)
	if e := es[len(es)-1]; e.Kind != EntryNotice || e.Words != MovedToTerminalWords {
		t.Fatalf("the boundary back: %+v", e)
	}
	if s.Info().Way != "" || s.Info().Status.Ended() || s.Viewers() != 1 {
		t.Fatalf("back: way %q, status %q, %d viewers", s.Info().Way, s.Info().Status, s.Viewers())
	}
	if chat := s.ChatHistory(); !hasChat(chat, "before the move") {
		t.Fatalf("the people's chat: %+v", chat)
	}
	var lines []string
	for _, e := range s.Activity() {
		if e.Type == ActivityStatus {
			lines = append(lines, e.Message)
		}
	}
	if !strings.Contains(strings.Join(lines, "|"), MovedToChatWords) || !strings.Contains(strings.Join(lines, "|"), MovedToTerminalWords) {
		t.Fatalf("the activity: %q", lines)
	}
	// The new terminal is the one the session drives.
	if err := s.Input(sub, []byte("x")); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	p := ts.procs[0]
	ts.mu.Unlock()
	select {
	case b := <-p.input:
		if string(b) != "x" {
			t.Fatalf("the new terminal got %q", b)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the keys reached no terminal")
	}
}

func hasChat(ms []ChatMessage, text string) bool {
	for _, m := range ms {
		if strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

// A move pressed during a turn, or while what holds it lasts, is noted:
// the way message with noted reaches every viewer and a new viewer's
// welcome, with the sentence of what holds it, and the move is made once
// that ends (the turn's end, the request closed, the work ended, the
// message's turn ended). Pressed away, noted clears and nothing moves.
func TestMoveWaitsForTheTurnsEnd(t *testing.T) {
	chat := func(t *testing.T) (*Local, *sessiontest.FakeProtocol, *terminals, *chanSink) {
		s, _ := movable(t, 0)
		ts := &terminals{}
		ts.cleanup(t)
		f := sessiontest.NewFakeProtocol()
		toChat(t, s, f, ts)
		sink := newChanSink(false)
		sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Ada"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Detach(sub) })
		return s, f, ts, sink
	}
	press := func(t *testing.T, s *Local, ts *terminals, sink *chanSink, why string) {
		t.Helper()
		st, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start})
		if err != nil || st.Noted != WayTerminal || st.Why != why || st.Moving {
			t.Fatalf("pressed: %+v, %v; want noted with %q", st, err, why)
		}
		wayMsg(t, sink, "noted", func(w proto.Way) bool { return w.Noted == proto.WayTerminal && w.Why == why })
		late := newChanSink(false)
		sub, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Bo"}, late)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Detach(sub)
		if m := waitControl(late, func(m map[string]any) bool { return m["t"] == proto.CtlWelcome }); m == nil || m["noted"] != proto.WayTerminal {
			t.Fatalf("a new viewer's welcome: %v", m)
		}
	}
	made := func(t *testing.T, s *Local, ts *terminals, sink *chanSink) {
		t.Helper()
		wayMsg(t, sink, "made", func(w proto.Way) bool { return w.Way == proto.WayTerminal && w.Noted == "" })
		settled(t, s)
		if s.Way() != WayTerminal || len(ts.started()) != 1 {
			t.Fatalf("way %q, %d terminals", s.Way(), len(ts.started()))
		}
	}
	stays := func(t *testing.T, s *Local, ts *terminals) {
		t.Helper()
		if s.Way() != WayProtocol || s.Moving() || len(ts.started()) != 0 {
			t.Fatalf("moved while held: way %q, %d terminals", s.Way(), len(ts.started()))
		}
	}
	t.Run("in a turn", func(t *testing.T) {
		s, f, ts, sink := chat(t)
		f.TurnStarted("t")
		press(t, s, ts, sink, MovesToTerminalWords)
		stays(t, s, ts)
		f.TurnEnded("done", Usage{})
		made(t, s, ts, sink)
	})
	t.Run("a request open", func(t *testing.T) {
		s, f, ts, sink := chat(t)
		f.TurnStarted("t")
		askInStream(f, "r1", "c1", "Bash", `{"command":"ls"}`)
		moveWaitFor(t, "the request open", func() bool { why, _ := s.atRestWhy(); return why == RequestWaitsWords })
		press(t, s, ts, sink, RequestWaitsWords)
		stays(t, s, ts)
		f.PermissionWithdrawn("r1")
		stays(t, s, ts)
		f.TurnEnded("done", Usage{})
		made(t, s, ts, sink)
	})
	t.Run("background work", func(t *testing.T) {
		s, f, ts, sink := chat(t)
		f.TurnStarted("t")
		f.Background(true)
		press(t, s, ts, sink, MovesToTerminalWords)
		f.TurnEnded("done", Usage{})
		wayMsg(t, sink, "held by the work", func(w proto.Way) bool { return w.Noted == proto.WayTerminal && w.Why == BackgroundWords })
		stays(t, s, ts)
		f.Background(false)
		made(t, s, ts, sink)
	})
	t.Run("a submission pending", func(t *testing.T) {
		s, f, ts, sink := chat(t)
		gate := make(chan struct{})
		f.BeforeSend = func(Message) { <-gate }
		_, err := s.Submit(context.Background(), Submission{Text: "hello"})
		mustPending(t, err)
		moveWaitFor(t, "the message on its way", func() bool { why, _ := s.atRestWhy(); return why == MovesToTerminalWords })
		press(t, s, ts, sink, MovesToTerminalWords)
		close(gate)
		waitCalls(t, f, "the message written", sends(1))
		stays(t, s, ts)
		f.Accepted(f.Sent()[0].Ref)
		f.TurnStarted("t")
		f.TurnEnded("done", Usage{})
		made(t, s, ts, sink)
	})
	t.Run("pressed away", func(t *testing.T) {
		s, f, ts, sink := chat(t)
		f.TurnStarted("t")
		press(t, s, ts, sink, MovesToTerminalWords)
		st, err := s.SwapWay(WayProtocol, MoveStarts{})
		if err != nil || st.Noted != "" {
			t.Fatalf("pressed away: %+v, %v", st, err)
		}
		wayMsg(t, sink, "cleared", func(w proto.Way) bool { return w.Way == proto.WayProtocol && w.Noted == "" })
		if to, _ := s.Noted(); to != "" {
			t.Fatalf("still noted %q", to)
		}
		f.TurnEnded("done", Usage{})
		moveWaitFor(t, "the turn's end", func() bool { return s.Info().Attention.State == AttentionDone })
		stays(t, s, ts)
	})
	t.Run("a terminal's turn", func(t *testing.T) {
		// From the terminal: pressed while it works, made at its done.
		s, _ := movable(t, 0)
		sink := newChanSink(false)
		sub, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Ada"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Detach(sub) })
		s.SetAttention(AttentionWorking, "", SourceAPI)
		f := sessiontest.NewFakeProtocol()
		st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()})
		if err != nil || st.Noted != WayProtocol || st.Why != MovesToChatWords {
			t.Fatalf("pressed: %+v, %v", st, err)
		}
		wayMsg(t, sink, "noted", func(w proto.Way) bool { return w.Way == proto.WayTerminal && w.Noted == proto.WayProtocol })
		s.SetAttention(AttentionDone, "done", SourceAPI)
		wayMsg(t, sink, "made", func(w proto.Way) bool { return w.Way == proto.WayProtocol })
		settled(t, s)
	})
}

// atRestWhy is atRestLocked under the lock.
func (s *Local) atRestWhy() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.atRestLocked()
}

// I21: a move to Chat is refused before the terminal's first turn has
// ended, in the plan's words; after a done from a source that reports
// turns it is made. A bell's or an operator's done is no turn's.
func TestMoveNeedsAFirstTurnInside(t *testing.T) {
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true}}, p, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { _ = s.Stop(context.Background()); p.exit() })
	f := sessiontest.NewFakeProtocol()
	s.SetAttention(AttentionDone, "marked", SourceAdmin)
	_, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()})
	var r *MoveRefusal
	if !errors.As(err, &r) || r.Words != FirstReplyWords {
		t.Fatalf("before a turn: %v", err)
	}
	s.SetAttention(AttentionDone, "a turn", SourceAPI)
	if st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()}); err != nil || !st.Moving {
		t.Fatalf("after a turn: %+v, %v", st, err)
	}
	settled(t, s)
	if s.Way() != WayProtocol {
		t.Fatalf("way %q", s.Way())
	}
}

// Info.FirstTurn is the fact the move to Chat is judged by, and the one a
// page offers it by: false on a session whose conversation is already
// resumable (a resume's, or a launch whose prompt was submitted), through a
// working of the agent's own (its prompt-submit hook) and an operator's
// done, the move refused all the while; true at the first done from a
// source that reports turns, told (OnChange), then the move is made; in the
// JSON as firstTurn only once true.
func TestFirstTurnIsWhatTheMoveJudges(t *testing.T) {
	p := newFakeProc()
	var mu sync.Mutex
	var told []bool
	s := NewLocal(Info{ID: "sess", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true, Source: AgentSessionResumed}}, p, Options{ScrollbackBytes: 4096, OnChange: func(i Info) {
		mu.Lock()
		told = append(told, i.FirstTurn)
		mu.Unlock()
	}})
	t.Cleanup(func() { _ = s.Stop(context.Background()); p.exit() })
	f := sessiontest.NewFakeProtocol()
	refused := func(when string) {
		t.Helper()
		if s.Info().FirstTurn {
			t.Fatalf("%s: firstTurn already", when)
		}
		_, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()})
		var r *MoveRefusal
		if !errors.As(err, &r) || r.Words != FirstReplyWords {
			t.Fatalf("%s: %v", when, err)
		}
	}
	refused("a resumed conversation")
	if b, _ := json.Marshal(s.Info()); bytes.Contains(b, []byte("firstTurn")) {
		t.Fatalf("firstTurn in the JSON while false: %s", b)
	}
	s.SetAttention(AttentionWorking, "", SourceAPI)
	refused("the agent at work on its first prompt")
	s.SetAttention(AttentionDone, "marked", SourceAdmin)
	refused("an operator's done")
	s.SetAttention(AttentionDone, "a turn", SourceAPI)
	if !s.Info().FirstTurn {
		t.Fatal("a turn's end did not set firstTurn")
	}
	if b, _ := json.Marshal(s.Info()); !bytes.Contains(b, []byte(`"firstTurn":true`)) {
		t.Fatalf("firstTurn not in the JSON: %s", b)
	}
	mu.Lock()
	last := len(told) > 0 && told[len(told)-1]
	mu.Unlock()
	if !last {
		t.Fatal("the change was not told")
	}
	if st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()}); err != nil || !st.Moving {
		t.Fatalf("after the turn: %+v, %v", st, err)
	}
	settled(t, s)
	if !s.Info().FirstTurn {
		t.Fatal("the move cleared firstTurn")
	}
}

// A submission, an answer and an interrupt during the move are refused
// moving, over the wire with the code moving; nothing is written.
func TestMovingRefusesActs(t *testing.T) {
	s, _ := movable(t, 0)
	sub := attachRole(t, s, RoleControl)
	gate := make(chan struct{})
	held := make(chan struct{})
	f := sessiontest.NewFakeProtocol()
	if _, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start(), AfterOldExit: func() { close(held); <-gate }}); err != nil {
		t.Fatal(err)
	}
	<-held
	if _, err := s.Submit(context.Background(), Submission{Text: "hello", By: sub}); !errors.Is(err, ErrMoving) {
		t.Errorf("a submission: %v", err)
	}
	if err := s.AnswerInput(context.Background(), sub, "q1", []byte("1")); !errors.Is(err, ErrMoving) {
		t.Errorf("an answer: %v", err)
	}
	if err := s.Input(sub, []byte("x")); !errors.Is(err, ErrMoving) {
		t.Errorf("keys: %v", err)
	}
	if err := s.Interrupt(context.Background(), sub); !errors.Is(err, ErrMoving) {
		t.Errorf("an interrupt: %v", err)
	}
	if _, err := s.SwapWay(WayTerminal, MoveStarts{}); !errors.Is(err, ErrMoving) {
		t.Errorf("another move: %v", err)
	}
	for _, f := range [][]byte{AnswerResult("r1", ErrMoving), InterruptWord("r2", ErrMoving)} {
		m := decodeControl(t, f)
		if m["code"] != proto.ErrCodeMoving || m["message"] != MovingWords {
			t.Errorf("on the wire: %v", m)
		}
	}
	close(gate)
	settled(t, s)
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d messages written", n)
	}
}

// A crew member's session and a hosted one never move: refused with the
// plan's words, nothing stopped.
func TestMoveRefusedForACrewMemberAndAHost(t *testing.T) {
	f := sessiontest.NewFakeProtocol()
	s, p := movable(t, 0)
	s.SetCrew(CrewRef{RunID: "run-1", Member: "writer"})
	_, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()})
	var r *MoveRefusal
	if !errors.As(err, &r) || r.Words != CrewStaysWords {
		t.Fatalf("a crew member: %v", err)
	}
	hp := newFakeProc()
	h := NewLocal(Info{ID: "hosted", Kind: KindHosted, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true}}, hp, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { _ = h.Stop(context.Background()); hp.exit() })
	h.SetAttention(AttentionDone, "a turn", SourceAPI)
	if _, err := h.SwapWay(WayProtocol, MoveStarts{Protocol: f.Start()}); !errors.As(err, &r) || r.Words != HostedStaysWords {
		t.Fatalf("a hosted session: %v", err)
	}
	for _, d := range []<-chan struct{}{p.Done(), hp.Done()} {
		select {
		case <-d:
			t.Fatal("a refused move stopped the session's process")
		default:
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("the driver was called: %v", f.Calls())
	}
}

// A new start that fails goes back the way the session came from, once,
// its words on the way message and in the activity; when that fails too,
// the session ends with both failures' words.
func TestMoveFailureTriesBackOnce(t *testing.T) {
	failed := errors.New("no chat")
	words := func(err error) string {
		if errors.Is(err, failed) {
			return "The agent did not start a chat."
		}
		return "The agent's terminal did not start."
	}
	t.Run("back", func(t *testing.T) {
		s, _ := movable(t, 0)
		sink := newChanSink(false)
		sub, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "Ada"}, sink)
		t.Cleanup(func() { s.Detach(sub) })
		ts := &terminals{}
		ts.cleanup(t)
		if _, err := s.SwapWay(WayProtocol, MoveStarts{
			Protocol: func(context.Context, string) (Child, Protocol, error) { return nil, nil, failed },
			Terminal: ts.start, Words: words,
		}); err != nil {
			t.Fatal(err)
		}
		w := wayMsg(t, sink, "back", func(w proto.Way) bool { return w.Why != "" })
		settled(t, s)
		if w.Way != proto.WayTerminal || w.Why != "The agent did not start a chat." {
			t.Fatalf("the way message: %+v", w)
		}
		if got := ts.started(); len(got) != 1 || got[0] != "conv-1" || s.Info().Status.Ended() || s.Way() != WayTerminal {
			t.Fatalf("back to %v, status %q, way %q", got, s.Info().Status, s.Way())
		}
	})
	t.Run("both", func(t *testing.T) {
		s, _ := movable(t, 0)
		ts := &terminals{fail: errors.New("no terminal")}
		if _, err := s.SwapWay(WayProtocol, MoveStarts{
			Protocol: func(context.Context, string) (Child, Protocol, error) { return nil, nil, failed },
			Terminal: ts.start, Words: words,
		}); err != nil {
			t.Fatal(err)
		}
		settled(t, s)
		if !s.Info().Status.Ended() {
			t.Fatalf("status %q", s.Info().Status)
		}
		want := "The agent did not start a chat. The agent's terminal did not start."
		found := false
		for _, e := range s.Activity() {
			found = found || e.Type == ActivityStatus && e.Message == want
		}
		if !found {
			t.Fatalf("the activity holds no %q: %+v", want, s.Activity())
		}
	})
}
