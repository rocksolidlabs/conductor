package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The protocol way inside the session, against a fake child and driver
// (docs/plans/chat-or-terminal.md, row 2).

// protocolSession is a session of the fake terminal moved into the
// protocol way with a fake driver, stopped at the test's end.
func protocolSession(t *testing.T, opts Options) (*Local, *fakeProc, *sessiontest.FakeProtocol) {
	t.Helper()
	if opts.ScrollbackBytes == 0 {
		opts.ScrollbackBytes = 4096
	}
	s, p := newLocalWith(t, opts)
	f := sessiontest.NewFakeProtocol()
	if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, p, f
}

// waitCalls waits for ok to hold of the fake's calls; the bound only
// bounds a hang.
func waitCalls(t *testing.T, f *sessiontest.FakeProtocol, what string, ok func([]string) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !f.Await(ctx, ok) {
		t.Fatalf("waited for %s; the driver's calls were %v", what, f.Calls())
	}
}

func sends(n int) func([]string) bool {
	return func(c []string) bool { return sessiontest.Count(c, "send") >= n }
}

type finishWord struct {
	res SubmitResult
	err error
}

func finishedInto(ch chan finishWord) func(SubmitResult, error) {
	return func(res SubmitResult, err error) { ch <- finishWord{res, err} }
}

func takeWord(t *testing.T, ch chan finishWord) finishWord {
	t.Helper()
	select {
	case w := <-ch:
		return w
	case <-time.After(30 * time.Second):
		t.Fatal("no word on the submission")
	}
	return finishWord{}
}

// allEntries is the whole transcript, oldest first.
func allEntries(t *testing.T, s *Local) []TranscriptEntry {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TranscriptEntry
	for _, re := range s.transcript.entries {
		out = append(out, re.e)
	}
	return out
}

func entryOf(t *testing.T, s *Local, seq uint64) TranscriptEntry {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	re := s.transcript.find(seq)
	if re == nil {
		t.Fatalf("entry %d is not kept", seq)
	}
	return re.e
}

func lastEntry(t *testing.T, s *Local) TranscriptEntry {
	t.Helper()
	es := allEntries(t, s)
	if len(es) == 0 {
		t.Fatal("the transcript is empty")
	}
	return es[len(es)-1]
}

func mustPending(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrSubmitPending) {
		t.Fatalf("Submit = %v, want ErrSubmitPending", err)
	}
}

// I4: a message is written once, entered at the agent's acknowledgement;
// a second acknowledgement, the caller's own end and the turn after change
// nothing.
func TestProtocolSubmitIsAcceptedOnce(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	words := make(chan finishWord, 4)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.Submit(ctx, Submission{Text: "hello", Finished: finishedInto(words)})
	cancel() // the caller's time runs out at once: nothing is written again for it
	mustPending(t, err)
	waitCalls(t, f, "the write", sends(1))
	m := f.Sent()[0]
	if m.Text != "hello" || m.Ref == "" {
		t.Fatalf("sent %+v", m)
	}
	f.Accepted(m.Ref)
	w := takeWord(t, words)
	if w.err != nil || !w.res.Entered || w.res.Typed || w.res.Reentered || w.res.NotAtPrompt || w.res.Unconfirmed {
		t.Fatalf("word = %+v", w)
	}
	mark := func() uint64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.transcript.mark
	}
	before := mark()
	f.Accepted(m.Ref)
	if mark() != before {
		t.Fatal("a second acknowledgement changed the transcript")
	}
	f.TurnStarted("")
	f.TurnEnded("done", Usage{})
	if n := len(words); n != 0 {
		t.Fatalf("%d more words after the acknowledgement", n)
	}
	if got := sessiontest.Count(f.Calls(), "send"); got != 1 {
		t.Fatalf("written %d times", got)
	}
	var prompt TranscriptEntry
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt {
			prompt = e
		}
	}
	if prompt.State != PromptAccepted || prompt.Text != "hello" || prompt.By != "conductor" {
		t.Fatalf("prompt entry %+v", prompt)
	}
}

// I4: a message the agent does not acknowledge within AcceptWait is
// unconfirmed and never written again: the next message is the next write.
func TestProtocolUnconfirmedNeverResent(t *testing.T) {
	s, _, f := protocolSession(t, Options{AcceptWait: 20 * time.Millisecond})
	words := make(chan finishWord, 4)
	_, err := s.Submit(context.Background(), Submission{Text: "first", Finished: finishedInto(words)})
	mustPending(t, err)
	w := takeWord(t, words)
	if !errors.Is(w.err, ErrUnconfirmed) || !w.res.Unconfirmed || w.res.Entered {
		t.Fatalf("word = %+v", w)
	}
	second := make(chan finishWord, 1)
	_, err = s.Submit(context.Background(), Submission{Text: "second", Finished: finishedInto(second)})
	mustPending(t, err)
	waitCalls(t, f, "the second write", sends(2))
	sent := f.Sent()
	if len(sent) != 2 || sent[0].Text != "first" || sent[1].Text != "second" {
		t.Fatalf("sent %+v", sent)
	}
	// Taken late, the first is not written again either.
	f.Accepted(sent[0].Ref)
	f.Accepted(sent[1].Ref)
	if w := takeWord(t, second); w.err != nil || !w.res.Entered {
		t.Fatalf("second word = %+v", w)
	}
	f.TurnStarted("")
	f.TurnEnded("done", Usage{})
	if got := sessiontest.Count(f.Calls(), "send"); got != 2 {
		t.Fatalf("written %d times", got)
	}
	if len(words) != 0 {
		t.Fatal("the first message was told twice")
	}
}

// A message sent during a turn is queued at once and written at the
// turn's end, once, in order with the next; it is not unconfirmed while
// it waits.
func TestProtocolSubmitDuringTurnIsQueued(t *testing.T) {
	s, _, f := protocolSession(t, Options{AcceptWait: time.Hour})
	var mu sync.Mutex
	turnOpen := true
	var duringTurn []string
	f.BeforeSend = func(m Message) {
		mu.Lock()
		defer mu.Unlock()
		if turnOpen {
			duringTurn = append(duringTurn, m.Text)
		}
	}
	f.TurnStarted("")
	words := make(chan finishWord, 4)
	returned := make(chan error, 2)
	go func() {
		_, err := s.Submit(context.Background(), Submission{Text: "one", Finished: finishedInto(words)})
		returned <- err
		_, err = s.Submit(context.Background(), Submission{Text: "two", Finished: finishedInto(words)})
		returned <- err
	}()
	for range 2 {
		select {
		case err := <-returned:
			mustPending(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("Submit waited for the turn")
		}
	}
	// The turn goes on; nothing is written, nobody told anything.
	f.Text("t1", "working on it", true)
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"ls"}`)})
	f.ToolEnded("c1", ToolResult{Status: ToolOK})
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("%d written during the turn", n)
	}
	if len(words) != 0 {
		t.Fatal("a queued message was told something")
	}
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt && e.State != PromptQueued {
			t.Fatalf("queued prompt %+v", e)
		}
	}
	mu.Lock()
	turnOpen = false
	mu.Unlock()
	f.TurnEnded("done", Usage{})
	waitCalls(t, f, "the first write", sends(1))
	first := f.Sent()[0]
	if first.Text != "one" {
		t.Fatalf("first written %q", first.Text)
	}
	f.Accepted(first.Ref)
	if w := takeWord(t, words); w.err != nil || !w.res.Entered {
		t.Fatalf("first word %+v", w)
	}
	// The second waits for the first's turn.
	mu.Lock()
	turnOpen = true
	mu.Unlock()
	f.TurnStarted("")
	f.Text("t2", "done that", true)
	if n := len(f.Sent()); n != 1 {
		t.Fatalf("%d written during the first's turn", n)
	}
	mu.Lock()
	turnOpen = false
	mu.Unlock()
	f.TurnEnded("done", Usage{})
	waitCalls(t, f, "the second write", sends(2))
	second := f.Sent()[1]
	f.Accepted(second.Ref)
	if w := takeWord(t, words); w.err != nil || !w.res.Entered {
		t.Fatalf("second word %+v", w)
	}
	if second.Text != "two" || sessiontest.Count(f.Calls(), "send") != 2 {
		t.Fatalf("sent %+v", f.Sent())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(duringTurn) != 0 {
		t.Fatalf("written during a turn: %v", duringTurn)
	}
}

// The seventeenth message queued is refused, and nothing of it is kept.
func TestProtocolQueueBound(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	for i := range MaxQueuedMessages {
		_, err := s.Submit(context.Background(), Submission{Text: fmt.Sprintf("m%d", i)})
		mustPending(t, err)
	}
	_, err := s.Submit(context.Background(), Submission{Text: "one too many"})
	if !errors.Is(err, ErrTooManyQueued) || err.Error() != TooManyQueuedWords {
		t.Fatalf("the seventeenth = %v", err)
	}
	prompts := 0
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt {
			prompts++
			if e.Text == "one too many" {
				t.Fatal("the refused message has an entry")
			}
		}
	}
	if prompts != MaxQueuedMessages || len(f.Sent()) != 0 {
		t.Fatalf("%d prompts, %d written", prompts, len(f.Sent()))
	}
	// One written frees a place.
	f.TurnEnded("done", Usage{})
	waitCalls(t, f, "the first write", sends(1))
	_, err = s.Submit(context.Background(), Submission{Text: "now there is room"})
	mustPending(t, err)
}

// I10: a submission naming an open permission request is refused before
// any claim, with UnlessWaiting and without.
func TestSubmitRefusesAPermissionQuestion(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "call-1", Name: "Bash", Input: []byte(`{"command":"rm -rf build"}`)})
	f.Permission(PermissionRequest{Ref: "req-1", Call: "call-1", Tool: "Bash", Input: []byte(`{"command":"rm -rf build"}`)})
	req := lastEntry(t, s)
	if req.Kind != EntryPermission || req.Question == "" || req.State != PermissionOpen {
		t.Fatalf("request entry %+v", req)
	}
	ctl := attachRole(t, s, RoleControl)
	for _, unless := range []bool{false, true} {
		for _, by := range []*Subscription{nil, ctl} {
			_, err := s.Submit(context.Background(), Submission{Text: "yes", Question: req.Question, UnlessWaiting: unless, By: by})
			if !errors.Is(err, ErrPermissionCard) || err.Error() != PermissionCardWords {
				t.Fatalf("UnlessWaiting %v by %v: %v", unless, by != nil, err)
			}
			s.mu.Lock()
			claims := len(s.claims) + len(s.claiming)
			s.mu.Unlock()
			if claims != 0 {
				t.Fatalf("UnlessWaiting %v: %d claims taken", unless, claims)
			}
		}
	}
	if len(f.Sent()) != 0 {
		t.Fatal("a refused line was written")
	}
}

// attachRole attaches a viewer of role.
func attachRole(t *testing.T, s *Local, role Role) *Subscription {
	t.Helper()
	sub, err := s.AttachWith(AttachOptions{Role: role, Name: "Ada", Cols: 100, Rows: 30}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	return sub
}

// I3: the terminal's calls are refused on a protocol session and write
// nothing, to the driver or to any terminal.
func TestProtocolRefusesTerminalCalls(t *testing.T) {
	s, p, f := protocolSession(t, Options{})
	sub := attachRole(t, s, RoleControl)
	if err := s.Input(sub, []byte("ls\r")); !errors.Is(err, ErrNotTerminal) || err.Error() != NotTerminalWords {
		t.Fatalf("Input = %v", err)
	}
	if err := s.InputContext(context.Background(), sub, []byte("x")); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("InputContext = %v", err)
	}
	if err := s.Resize(sub, 120, 40); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Resize = %v", err)
	}
	if err := s.ResizeWith(sub, 120, 40, true); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("ResizeWith = %v", err)
	}
	if got := f.Calls(); len(got) != 1 || got[0] != "begin" {
		t.Fatalf("the driver was called %v", got)
	}
	select {
	case b := <-p.input:
		t.Fatalf("the terminal was written %q", b)
	case r := <-p.resize:
		t.Fatalf("the terminal was resized to %v", r)
	default:
	}
	if s.Way() != WayProtocol {
		t.Fatalf("Way = %q", s.Way())
	}
}

// Interrupt is a protocol session's: a terminal session answers
// ErrNotProtocol, and so does a read of its transcript.
func TestInterruptOnATerminalIsNotProtocol(t *testing.T) {
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	if err := s.Interrupt(context.Background(), nil); !errors.Is(err, ErrNotProtocol) || err.Error() != NotProtocolWords {
		t.Fatalf("Interrupt = %v", err)
	}
	if _, err := s.Transcript(0, 10); !errors.Is(err, ErrNotProtocol) {
		t.Fatalf("Transcript = %v", err)
	}
	if s.Way() != WayTerminal || s.Ready() != nil {
		t.Fatalf("Way %q, Ready %v", s.Way(), s.Ready())
	}
	ps, _, _ := protocolSession(t, Options{})
	select {
	case <-ps.Ready():
	default:
		t.Fatal("Ready is not closed once Begin answered")
	}
	if err := ps.Interrupt(context.Background(), nil); !errors.Is(err, ErrNoTurn) || err.Error() != NoTurnWords {
		t.Fatalf("Interrupt with no turn = %v", err)
	}
	if err := ps.Interrupt(context.Background(), attachRole(t, ps, RoleView)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a viewer's Interrupt = %v", err)
	}
}

// A protocol turn reports the session's attention: working at its start,
// done at its end, from the protocol, a source that reports turns.
func TestProtocolTurnReportsAttention(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	if !ReportsTurn(AttentionWorking, SourceProtocol) || !ReportsTurn(AttentionDone, SourceProtocol) {
		t.Fatal("the protocol does not report turns")
	}
	f.TurnStarted("")
	if a := s.Info().Attention; a.State != AttentionWorking || a.Source != SourceProtocol {
		t.Fatalf("at the start %+v", a)
	}
	if !s.Info().Turns {
		t.Fatal("Info.Turns not set")
	}
	f.Text("m1", "All done.", true)
	f.TurnEnded("done", Usage{InputTokens: 10, OutputTokens: 5})
	a := s.Info().Attention
	if a.State != AttentionDone || a.Source != SourceProtocol || a.Kind != KindDone || a.Message != "All done." {
		t.Fatalf("at the end %+v", a)
	}
	end := lastEntry(t, s)
	if end.Kind != EntryTurn || end.Phase != "end" || end.Outcome != "done" || end.Turn != 1 || end.Usage == nil || end.Usage.OutputTokens != 5 {
		t.Fatalf("turn's end %+v", end)
	}
}

// I12: an interrupted turn's done is of kind interrupted.
func TestInterruptedTurnIsNotDone(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"sleep 100"}`)})
	f.TurnEnded("interrupted", Usage{})
	a := s.Info().Attention
	if a.State != AttentionDone || a.Kind != KindInterrupted || a.Source != SourceProtocol {
		t.Fatalf("attention %+v", a)
	}
	if !ValidKind(KindDone) || ValidKind(KindInterrupted) {
		t.Fatal("a report from outside could carry the interrupted kind")
	}
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryTool && e.Status != ToolInterrupted {
			t.Fatalf("the step left %q", e.Status)
		}
	}
	if end := lastEntry(t, s); end.Outcome != "interrupted" {
		t.Fatalf("turn's end %+v", end)
	}
}

// An agent with no interrupt it confirms has its child ended and started
// again on the same conversation, a notice saying so, the session open.
func TestInterruptWithoutReceiptRestarts(t *testing.T) {
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	s.SetAgentSession(AgentSession{ID: "conv-1", Source: AgentSessionSet})
	f, again := sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol()
	f.InterruptErr = ErrNoInterrupt
	if err := s.enterProtocol(context.Background(), f.Start(again)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	f.TurnStarted("")
	if err := s.Interrupt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, again, "the child started again", func(c []string) bool { return sessiontest.Count(c, "begin") == 1 })
	calls := f.Calls()
	if strings.Join(calls, " ") != "begin interrupt close_input stop" {
		t.Fatalf("the first child's calls %v", calls)
	}
	if again.StartedOn() != "conv-1" {
		t.Fatalf("started again on %q", again.StartedOn())
	}
	// The next message goes to the new child once it is ready: the
	// restart's notice is in by then.
	_, err := s.Submit(context.Background(), Submission{Text: "go on"})
	mustPending(t, err)
	waitCalls(t, again, "the write to the new child", sends(1))
	if st := s.Info().Status; st != StatusRunning {
		t.Fatalf("the session is %s", st)
	}
	if a := s.Info().Attention; a.State != AttentionDone || a.Kind != KindInterrupted {
		t.Fatalf("attention %+v", a)
	}
	var notice *TranscriptEntry
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryNotice && e.Code == noticeRestarted {
			notice = &e
		}
	}
	if notice == nil || notice.Words != RestartedWords {
		t.Fatal("no notice of the restart")
	}
	if len(f.Sent()) != 0 {
		t.Fatal("the old child was written to")
	}
}

// I7: with every screen rule's trigger fed, a protocol session's working
// stays, and so does its done.
func TestProtocolAttentionIgnoresScreenRules(t *testing.T) {
	s, _, f := protocolSession(t, Options{
		Pattern:      regexp.MustCompile(`^> $`),
		TrustPattern: regexp.MustCompile(`trust this folder`),
		TrustAnswers: []Option{{Label: "Yes", Input: "1"}},
		Questions:    []StartupQuestion{{Pattern: regexp.MustCompile(`Run this command\?`), Anytime: true, Answers: []Option{{Label: "Yes", Input: "y"}}}},
		WorkingTitle: regexp.MustCompile(`Working`),
	})
	triggers := []struct {
		name string
		feed func()
	}{
		{"bell", func() { s.scanOutput([]byte("\a"), s.turn.Load()) }},
		{"osc notification", func() { s.scanOutput([]byte("\x1b]9;Look here\x07"), s.turn.Load()) }},
		{"working title", func() { s.scanOutput([]byte("\x1b]0;⠋ Working\x07"), s.turn.Load()) }},
		{"idle title", func() { s.scanOutput([]byte("\x1b]0;Ready\x07"), s.turn.Load()) }},
		{"program status blocked", func() { s.scanOutput([]byte("\x1b]7501;state=blocked:app=codex:kind=question\a"), s.turn.Load()) }},
		{"program status working", func() { s.scanOutput([]byte("\x1b]7501;state=working:app=codex\a"), s.turn.Load()) }},
		{"screen pattern", func() { s.firePattern("> ") }},
		{"startup question", func() {
			_ = s.setAttentionLooked(AttentionNeedsInput, "Do you trust this folder?", SourceTrust, KindPrompt, s.opts.TrustAnswers, unlessWaiting|startupLook, anyTurn, 0, "")
		}},
		{"dialog during the work", func() {
			_ = s.setAttentionLooked(AttentionNeedsInput, "Run this command?", SourceQuestion, KindPermission, []Option{{Label: "Yes", Input: "y"}}, fromReport, anyTurn, 0, "")
		}},
		{"screen moved on", func() { s.screenQuiet("") }},
		{"held text", func() { s.SetAttention(AttentionNeedsInput, HeldWords, SourceSubmit) }},
		{"screen's empty state", func() { s.SetAttention(AttentionNone, "", SourceScreen) }},
	}
	check := func(want Attention) {
		t.Helper()
		for _, tr := range triggers {
			tr.feed()
			if got := s.Info().Attention; got.State != want.State || got.Source != want.Source || got.ID != want.ID || got.Kind != want.Kind {
				t.Fatalf("after the %s: %+v, want %+v", tr.name, got, want)
			}
		}
	}
	f.TurnStarted("")
	working := s.Info().Attention
	if working.State != AttentionWorking || working.Source != SourceProtocol {
		t.Fatalf("working %+v", working)
	}
	check(working)
	f.TurnEnded("done", Usage{})
	done := s.Info().Attention
	if done.State != AttentionDone || done.Source != SourceProtocol {
		t.Fatalf("done %+v", done)
	}
	check(done)
}

// stepClock is a clock a test moves, backwards too.
type stepClock struct {
	mu sync.Mutex
	at time.Time
}

func newStepClock() *stepClock { return &stepClock{at: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} }

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// I5: entries take their seq in the order the driver handed the events,
// whatever the clock says; a revision keeps its seq and takes a new mark.
func TestTranscriptOrderIsTheStreams(t *testing.T) {
	clock := newStepClock()
	s, _, f := protocolSession(t, Options{Now: clock.now})
	type added struct {
		kind, ref string
	}
	var want []added
	revisedOf := map[string]uint64{} // a tool's ref: the mark before its end
	var lastMark uint64
	for i := range 1000 {
		if i%7 == 3 {
			clock.add(-5 * time.Second)
		} else {
			clock.add(time.Millisecond)
		}
		ref := fmt.Sprintf("r%d", i)
		switch i % 4 {
		case 0:
			f.Notice("note", fmt.Sprintf("notice %d", i))
			want = append(want, added{EntryNotice, ""})
		case 1:
			f.ToolStarted(ToolStep{Ref: ref, Name: "Read", Input: []byte(`{"file_path":"a.go"}`)})
			want = append(want, added{EntryTool, ref})
		case 2:
			prev := fmt.Sprintf("r%d", i-1)
			s.mu.Lock()
			revisedOf[prev] = s.transcript.mark
			s.mu.Unlock()
			f.ToolEnded(prev, ToolResult{Status: ToolOK, Output: []byte("package a")})
		case 3:
			f.Text(ref, "words", true)
			want = append(want, added{EntryText, ref})
		}
	}
	var got []TranscriptEntry
	var after uint64
	for {
		page, err := s.Transcript(after, MaxTranscriptPage)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		after = page[len(page)-1].Seq
	}
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d", len(got), len(want))
	}
	marks := map[uint64]bool{}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
		if e.Kind != want[i].kind || e.Ref != want[i].ref {
			t.Fatalf("entry %d is %s %q, want %s %q", i, e.Kind, e.Ref, want[i].kind, want[i].ref)
		}
		if marks[e.Mark] {
			t.Fatalf("mark %d given twice", e.Mark)
		}
		marks[e.Mark] = true
		if e.Mark > lastMark {
			lastMark = e.Mark
		}
		if e.Kind == EntryTool {
			before, ended := revisedOf[e.Ref]
			if !ended {
				continue
			}
			if e.Rev != 1 || e.Status != ToolOK || e.Mark <= before {
				t.Fatalf("revised step %+v, the mark before its end %d", e, before)
			}
		}
	}
	s.mu.Lock()
	mark := s.transcript.mark
	s.mu.Unlock()
	if mark != lastMark {
		t.Fatalf("the newest mark %d is not the session's %d", lastMark, mark)
	}
}

// I20: the ring and the parts keep their bounds, dropping the oldest, but
// never an open request's entry or its input; answered, they go on the
// next overflow.
func TestTranscriptBounds(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("") // seq 1
	input := bytes.Repeat([]byte("i"), 200<<10)
	f.Permission(PermissionRequest{Ref: "req-1", Call: "c0", Tool: "Bash", Input: []byte(`{"command":"` + string(input) + `"}`)})
	req := lastEntry(t, s)
	if req.Kind != EntryPermission || req.State != PermissionOpen {
		t.Fatalf("request %+v", req)
	}
	counts := func() (entries, size, parts int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.transcript.entries), s.transcript.bytes, s.transcript.partBytes
	}

	// Past 1,000 entries the oldest go.
	for i := range MaxTranscriptEntries {
		f.Notice("n", fmt.Sprintf("%d", i))
	}
	if n, _, _ := counts(); n != MaxTranscriptEntries {
		t.Fatalf("%d entries kept", n)
	}
	if _, err := s.Transcript(0, 1); !errors.Is(err, ErrTranscriptGone) || err.Error() != TranscriptGoneWords {
		t.Fatalf("the oldest = %v", err)
	}
	if e := entryOf(t, s, req.Seq); e.State != PermissionOpen {
		t.Fatalf("the open request after 1,000 entries: %+v", e)
	}

	// Past 2 MiB of entries the oldest go.
	text := strings.Repeat("w", MaxEntryText)
	for i := range 600 {
		f.Text(fmt.Sprintf("t%d", i), text, true)
	}
	if _, size, _ := counts(); size > MaxTranscriptBytes {
		t.Fatalf("%d bytes of entries kept", size)
	}
	if es := allEntries(t, s); es[1].Kind != EntryText {
		t.Fatalf("a notice outlived 2 MiB of texts: %+v", es[1])
	}
	entryOf(t, s, req.Seq)

	// A part past 256 KiB is kept cut with its size.
	f.ToolStarted(ToolStep{Ref: "big", Name: "Bash", Input: []byte(`{"command":"cat log"}`)})
	big := lastEntry(t, s)
	out := bytes.Repeat([]byte("o"), 300<<10)
	f.ToolEnded("big", ToolResult{Status: ToolOK, Output: out})
	if e := entryOf(t, s, big.Seq); e.OutputBytes != len(out) {
		t.Fatalf("outputBytes %d", e.OutputBytes)
	}
	var kept []byte
	for {
		piece, total, cut, err := s.TranscriptPart(big.Seq, PartOutput, len(kept))
		if err != nil || total != len(out) || !cut || len(piece) > MaxTranscriptPiece {
			t.Fatalf("piece of %d, total %d, cut %v: %v", len(piece), total, cut, err)
		}
		if len(piece) == 0 {
			break
		}
		kept = append(kept, piece...)
	}
	if len(kept) != MaxTranscriptPart {
		t.Fatalf("%d bytes of the part kept", len(kept))
	}

	// Past 8 MiB of parts the oldest part answers gone, its entry kept.
	full := bytes.Repeat([]byte("p"), MaxTranscriptPart)
	for i := range 40 {
		ref := fmt.Sprintf("p%d", i)
		f.ToolStarted(ToolStep{Ref: ref, Name: "Bash", Input: []byte(`{}`)})
		f.ToolEnded(ref, ToolResult{Status: ToolOK, Output: full})
	}
	if _, _, parts := counts(); parts > MaxTranscriptParts {
		t.Fatalf("%d bytes of parts", parts)
	}
	entryOf(t, s, big.Seq)
	if _, _, _, err := s.TranscriptPart(big.Seq, PartOutput, 0); !errors.Is(err, ErrTranscriptGone) {
		t.Fatalf("the oldest part = %v", err)
	}
	// The open request's input outlived 8 MiB of later parts.
	if piece, total, _, err := s.TranscriptPart(req.Seq, PartInput, 0); err != nil || total != req.InputBytes || len(piece) == 0 {
		t.Fatalf("the open request's input: %d of %d: %v", len(piece), total, err)
	}

	// Answered (withdrawn by the agent), both go on the next overflow.
	f.PermissionWithdrawn("req-1")
	if e := entryOf(t, s, req.Seq); e.State != PermissionWithdrawn || e.Why != AnsweredByAgentWords {
		t.Fatalf("withdrawn %+v", e)
	}
	// The ring is within one text of its bound: one more overflows it.
	f.Text("last", text, true)
	s.mu.Lock()
	gone := s.transcript.find(req.Seq) == nil
	s.mu.Unlock()
	if !gone {
		t.Fatal("the answered request's entry outlived the next overflow")
	}
	if _, _, _, err := s.TranscriptPart(req.Seq, PartInput, 0); !errors.Is(err, ErrTranscriptGone) {
		t.Fatalf("the answered request's input = %v", err)
	}
	if _, _, _, err := s.TranscriptPart(req.Seq, "secrets", 0); !errors.Is(err, ErrNoSuchPart) {
		t.Fatalf("a part outside the set = %v", err)
	}
}

// At most MaxPendingPermissions requests are open: the next is denied at
// once with the bound's words, and an open one's end frees a place.
func TestProtocolPermissionBound(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	for i := range MaxPendingPermissions {
		f.Permission(PermissionRequest{Ref: fmt.Sprintf("req-%d", i), Tool: "Bash", Input: []byte(`{}`)})
	}
	f.Permission(PermissionRequest{Ref: "req-past", Tool: "Bash", Input: []byte(`{}`)})
	past := lastEntry(t, s)
	if past.State != PermissionDenied || past.Why != TooManyRequestsWords || past.Question != "" {
		t.Fatalf("the ninth %+v", past)
	}
	waitCalls(t, f, "the deny", func(c []string) bool { return sessiontest.Count(c, "reply") == 1 })
	if r := f.Replies(); r[0].Ref != "req-past" || r[0].Verdict.Allow || r[0].Verdict.Message != TooManyRequestsWords {
		t.Fatalf("replied %+v", r)
	}
	f.PermissionWithdrawn("req-0")
	f.Permission(PermissionRequest{Ref: "req-next", Tool: "Bash", Input: []byte(`{}`)})
	if e := lastEntry(t, s); e.State != PermissionOpen {
		t.Fatalf("after a place freed %+v", e)
	}
}

// The tool steps and texts the session follows are bounded: past
// maxOpenSteps the oldest is no longer followed.
func TestProtocolOpenStepsBound(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	for i := range maxOpenSteps + 1 {
		f.ToolStarted(ToolStep{Ref: fmt.Sprintf("c%d", i), Name: "Read", Input: []byte(`{}`)})
	}
	s.mu.Lock()
	p := s.way.Load()
	open, order := len(p.tools), len(p.toolOrder)
	s.mu.Unlock()
	if open != maxOpenSteps || order != maxOpenSteps {
		t.Fatalf("%d steps followed", open)
	}
	mark := func() uint64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.transcript.mark
	}
	before := mark()
	f.ToolEnded("c0", ToolResult{Status: ToolOK})
	if mark() != before {
		t.Fatal("a step no longer followed was revised")
	}
	f.ToolEnded(fmt.Sprintf("c%d", maxOpenSteps), ToolResult{Status: ToolOK})
	if mark() == before {
		t.Fatal("the newest step's end was not applied")
	}
}

// The agent's streamed words are revised at most once per TextCoalesce of
// the session's clock, every word kept.
func TestTextCoalescedPerEntry(t *testing.T) {
	clock := newStepClock()
	s, _, f := protocolSession(t, Options{Now: clock.now})
	stream := func(ref string, n int, step time.Duration) TranscriptEntry {
		t.Helper()
		var sent strings.Builder
		var seq uint64
		for i := range n {
			d := fmt.Sprintf("w%d ", i)
			sent.WriteString(d)
			f.Text(ref, d, false)
			if seq == 0 {
				seq = lastEntry(t, s).Seq
			}
			clock.add(step)
		}
		elapsed := time.Duration(n) * step
		e := entryOf(t, s, seq)
		if limit := uint32(elapsed / TextCoalesce); e.Rev > limit {
			t.Fatalf("%d revisions in %v", e.Rev, elapsed)
		}
		f.Text(ref, "", true)
		e = entryOf(t, s, seq)
		if e.Text != sent.String() || !e.Final {
			t.Fatalf("kept %d bytes of %d, final %v", len(e.Text), sent.Len(), e.Final)
		}
		if limit := uint32(elapsed/TextCoalesce) + 1; e.Rev > limit {
			t.Fatalf("%d revisions in %v with the final one", e.Rev, elapsed)
		}
		return e
	}
	// 500 deltas in 50 ms: the entry, then its final revision.
	if e := stream("m1", 500, 100*time.Microsecond); e.Rev != 1 {
		t.Fatalf("%d revisions in 50 ms", e.Rev)
	}
	// 500 deltas in 500 ms: about one each 100 ms.
	if e := stream("m2", 500, time.Millisecond); e.Rev < 2 {
		t.Fatalf("only %d revisions in 500 ms", e.Rev)
	}
}

// stateSink records, as each frame arrives, the session's state then.
type stateSink struct {
	s      *Local
	mu     sync.Mutex
	seen   []sinkRead
	arrive chan struct{}
}

type sinkRead struct {
	t, frameState, activity string
	attention               Attention
	last                    TranscriptEntry
	step                    TranscriptEntry
}

func (k *stateSink) WriteFrame(frame []byte) error {
	f, err := proto.Decode(frame)
	if err != nil || f.Type != proto.TypeControl {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(f.Payload, &m) != nil {
		return nil
	}
	t, _ := m["t"].(string)
	if t != proto.CtlAttention && t != proto.CtlActivity {
		return nil
	}
	r := sinkRead{t: t, attention: k.s.Info().Attention}
	r.frameState, _ = m["state"].(string)
	r.activity, _ = m["type"].(string)
	k.s.mu.Lock()
	if n := len(k.s.transcript.entries); n > 0 {
		r.last = k.s.transcript.entries[n-1].e
	}
	for _, re := range k.s.transcript.entries {
		if re.e.Kind == EntryTool {
			r.step = re.e
		}
	}
	k.s.mu.Unlock()
	k.mu.Lock()
	k.seen = append(k.seen, r)
	k.mu.Unlock()
	select {
	case k.arrive <- struct{}{}:
	default:
	}
	return nil
}

func (k *stateSink) Close(error) {}

func (k *stateSink) reads() []sinkRead {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]sinkRead(nil), k.seen...)
}

// waitRead waits for a read matching ok.
func (k *stateSink) waitRead(t *testing.T, ok func(sinkRead) bool) sinkRead {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		for _, r := range k.reads() {
			if ok(r) {
				return r
			}
		}
		select {
		case <-k.arrive:
		case <-deadline:
			t.Fatalf("no such frame; the sink read %+v", k.reads())
		}
	}
}

// I6: each event changes the session's state before any viewer is sent a
// frame of it: held between the two (afterApply), the state is there and
// no frame of it is; a viewer that reads the state as each frame arrives
// always sees the frame's.
func TestStateBeforeViewers(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	sink := &stateSink{s: s, arrive: make(chan struct{}, 1)}
	if _, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Ada"}, sink); err != nil {
		t.Fatal(err)
	}
	held := make(chan string)
	release := make(chan struct{})
	hold := func(event string) {
		switch event {
		case "turn_started", "tool_ended", "turn_ended":
			held <- event
			<-release
		}
	}
	s.afterApply.Store(&hold)
	t.Cleanup(func() { s.afterApply.Store(nil) })

	step := func(event string, hand func(), state func() bool, frame func(sinkRead) bool) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			hand()
		}()
		if got := <-held; got != event {
			t.Fatalf("held %q, want %q", got, event)
		}
		if !state() {
			t.Fatalf("%s: held before its state was applied", event)
		}
		// Everything sent before the hold has reached the sink; nothing of
		// this event has.
		for !s.Drained() {
			<-time.After(time.Millisecond)
		}
		for _, r := range sink.reads() {
			if frame(r) {
				t.Fatalf("%s: a viewer had its frame before the state: %+v", event, r)
			}
		}
		close(release)
		<-done
		release = make(chan struct{})
		sink.waitRead(t, frame)
	}

	step("turn_started", func() { f.TurnStarted("") },
		func() bool { e := lastEntry(t, s); return e.Kind == EntryTurn && e.Phase == "start" },
		func(r sinkRead) bool { return r.t == proto.CtlAttention && r.frameState == string(AttentionWorking) })
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"go test"}`)})
	step("tool_ended", func() { f.ToolEnded("c1", ToolResult{Status: ToolOK, Output: []byte("ok")}) },
		func() bool { return lastEntry(t, s).Status == ToolOK },
		func(r sinkRead) bool { return r.t == proto.CtlActivity && r.activity == ActivityToolUse })
	step("turn_ended", func() { f.TurnEnded("done", Usage{}) },
		func() bool { e := lastEntry(t, s); return e.Kind == EntryTurn && e.Phase == "end" },
		func(r sinkRead) bool { return r.t == proto.CtlAttention && r.frameState == string(AttentionDone) })

	for _, r := range sink.reads() {
		switch {
		case r.t == proto.CtlAttention && r.frameState == string(AttentionWorking):
			if r.attention.State != AttentionWorking || r.last.Kind != EntryTurn || r.last.Phase != "start" {
				t.Fatalf("working's frame met %+v", r)
			}
		case r.t == proto.CtlActivity && r.activity == ActivityToolUse:
			if r.step.Status != ToolOK {
				t.Fatalf("the step's activity met the step %+v", r.step)
			}
		case r.t == proto.CtlAttention && r.frameState == string(AttentionDone):
			if r.attention.State != AttentionDone || r.last.Phase != "end" {
				t.Fatalf("done's frame met %+v", r)
			}
		}
	}
}

// A submission naming a question makes a prompt entry that names it and
// carries no text, for every viewer; the agent is sent the text.
func TestPromptThatAnswersCarriesNoText(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	id, err := s.TryAskID("Which branch?", SourceAPI, KindPrompt, nil)
	if err != nil || id == "" {
		t.Fatalf("ask %q: %v", id, err)
	}
	sub := attachRole(t, s, RoleControl)
	_, err = s.Submit(context.Background(), Submission{Text: "the secret branch", Question: id, By: sub, ReqID: "r1"})
	mustPending(t, err)
	waitCalls(t, f, "the write", sends(1))
	if m := f.Sent()[0]; m.Text != "the secret branch" {
		t.Fatalf("sent %q", m.Text)
	}
	var prompt *TranscriptEntry
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryPrompt {
			prompt = &e
		}
	}
	if prompt == nil || prompt.Answers != id || prompt.Text != "" || prompt.By != "person" || prompt.ByName != "Ada" {
		t.Fatalf("prompt %+v", prompt)
	}
	raw, _ := json.Marshal(allEntries(t, s))
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatal("the answer's text is in the transcript")
	}
	// Its write resolved the question, by whom alone.
	deadline := time.After(30 * time.Second)
	for {
		if r, ok := s.QuestionResult(id); ok {
			if r.ByName != "Ada" || r.ReqID != "r1" {
				t.Fatalf("result %+v", r)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("the question was not resolved")
		case <-time.After(time.Millisecond):
		}
	}
}

// Stop answers the open requests, closes the agent's input, then stops
// the child, then ends the session.
func TestProtocolStopClosesInputFirst(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.Permission(PermissionRequest{Ref: "req-1", Tool: "Bash", Input: []byte(`{}`)})
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.Calls(), " "); got != "begin reply close_input stop" {
		t.Fatalf("calls %s", got)
	}
	if r := f.Replies(); len(r) != 1 || r[0].Verdict.Allow || r[0].Verdict.Message != EndedBeforeAnswerWords {
		t.Fatalf("replied %+v", r)
	}
	if st := s.Info().Status; st != StatusStopped {
		t.Fatalf("status %s", st)
	}
	<-s.ProcessDone()
}

// I13: every byte the driver captured reaches Options.OnCaptured.
func TestProtocolCapturedReachesOnCaptured(t *testing.T) {
	var mu sync.Mutex
	var got bytes.Buffer
	s, _, f := protocolSession(t, Options{OnCaptured: func(id string, data []byte) {
		if id != "sess" {
			t.Errorf("captured for %q", id)
		}
		mu.Lock()
		got.Write(data)
		mu.Unlock()
	}})
	var want bytes.Buffer
	words := []string{"Here is ", "the plan", ".\n"}
	for i, w := range words {
		f.Text("m1", w, i == len(words)-1)
		want.WriteString(w)
	}
	in := []byte(`{"command":"cat secret.txt"}`)
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: in})
	want.Write(in)
	out := bytes.Repeat([]byte("line of output\n"), 30000) // past a part's bound: captured whole
	f.ToolEnded("c1", ToolResult{Status: ToolOK, Output: out})
	want.Write(out)
	diff := []byte("--- a/x.go\n+++ b/x.go\n+new line\n")
	f.FileChanged(FileChange{Tool: "c1", Path: "x.go", Op: ChangeEdit, Added: 1, Diff: diff})
	want.Write(diff)
	req := []byte(`{"command":"curl example.invalid"}`)
	f.Permission(PermissionRequest{Ref: "req-1", Tool: "Bash", Input: req})
	want.Write(req)
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("captured %d bytes, want %d", got.Len(), want.Len())
	}
	_ = s
}

// A tool step and a file change make the activity entries the hook
// mappers make: tool_use, tool_denied, error as the step ended, and file.
func TestProtocolToolStepsAreActivity(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	cwd := s.Info().Cwd
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"go test ./..."}`)})
	f.ToolEnded("c1", ToolResult{Status: ToolOK})
	f.ToolStarted(ToolStep{Ref: "c2", Name: "Bash", Input: []byte(`{"command":"rm -rf /"}`)})
	f.ToolEnded("c2", ToolResult{Status: ToolDenied})
	f.ToolStarted(ToolStep{Ref: "c3", Name: "Bash", Input: []byte(`{"command":"false"}`)})
	f.ToolEnded("c3", ToolResult{Status: ToolError, Output: []byte("exit status 1\nmore")})
	f.ToolStarted(ToolStep{Ref: "c4", Name: "Edit", Input: []byte(`{"file_path":"main.go"}`)})
	f.FileChanged(FileChange{Tool: "c4", Path: cwd + "/main.go", Op: ChangeEdit, Added: 3, Removed: 1})
	f.FileChanged(FileChange{Tool: "c4", Path: "/elsewhere/notes.txt", Op: ChangeCreate})
	f.ToolEnded("c4", ToolResult{Status: ToolOK})
	var got []ActivityEntry
	for _, e := range s.Activity() {
		switch e.Type {
		case ActivityToolUse, ActivityToolDenied, ActivityError, ActivityFile:
			e.At = time.Time{}
			got = append(got, e)
		}
	}
	want := []ActivityEntry{
		{Type: ActivityToolUse, Tool: "Bash", ByName: "agent"},
		{Type: ActivityToolDenied, Tool: "Bash", ByName: "agent"},
		{Type: ActivityError, Tool: "Bash", ByName: "agent", Message: "exit status 1"},
		{Type: ActivityFile, Op: FileOpEdit, Path: cwd + "/main.go", Tool: "Edit", ByName: "agent"},
		{Type: ActivityFile, Op: FileOpWrite, Path: "/elsewhere/notes.txt", Tool: "Edit", ByName: "agent"},
		{Type: ActivityToolUse, Tool: "Edit", ByName: "agent"},
	}
	if len(got) != len(want) {
		t.Fatalf("activity %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d is %+v, want %+v", i, got[i], want[i])
		}
	}
	var files []TranscriptEntry
	for _, e := range allEntries(t, s) {
		if e.Kind == EntryFile {
			files = append(files, e)
		}
	}
	if len(files) != 2 || files[0].Path != "main.go" || files[0].Outside || files[0].Added != 3 || files[1].Path != "" || !files[1].Outside || files[1].Op != ChangeCreate {
		t.Fatalf("file entries %+v", files)
	}
}

// Read is never held by a request: the event after an in-stream
// permission request is applied before anyone answers it.
func TestReadIsNotBlockedByARequest(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		f.Permission(PermissionRequest{Ref: "req-1", Call: "c1", Tool: "Bash", Input: []byte(`{"command":"make"}`)})
		f.Notice("after", "the next event")
	}()
	select {
	case <-applied:
	case <-time.After(30 * time.Second):
		t.Fatal("the read waits on the request")
	}
	if e := lastEntry(t, s); e.Kind != EntryNotice || e.Code != "after" {
		t.Fatalf("the newest entry %+v", e)
	}
	if r := f.Replies(); len(r) != 0 {
		t.Fatalf("answered %+v", r)
	}
}

// A start whose Begin fails leaves the session in its terminal, the child
// ended, nothing of it kept.
func TestProtocolBeginFailureStaysInTheTerminal(t *testing.T) {
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	f := sessiontest.NewFakeProtocol()
	f.BeginErr = errors.New("no answer to initialize")
	if err := s.enterProtocol(context.Background(), f.Start()); !errors.Is(err, f.BeginErr) {
		t.Fatalf("enter = %v", err)
	}
	if s.Way() != WayTerminal || s.Ready() != nil || s.Info().Status != StatusRunning {
		t.Fatalf("Way %q, status %s", s.Way(), s.Info().Status)
	}
	if got := strings.Join(f.Calls(), " "); got != "begin close_input stop" {
		t.Fatalf("calls %s", got)
	}
	// The terminal takes keys as ever.
	sub := attachRole(t, s, RoleControl)
	if err := s.Input(sub, []byte("x")); err != nil {
		t.Fatalf("Input = %v", err)
	}
}

// The child's own exit ends the session with its code: the open requests
// denied, a queued message told the session ended.
func TestProtocolChildExitEndsTheSession(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.Permission(PermissionRequest{Ref: "req-1", Tool: "Bash", Input: []byte(`{}`)})
	req := lastEntry(t, s)
	words := make(chan finishWord, 1)
	_, err := s.Submit(context.Background(), Submission{Text: "later", Finished: finishedInto(words)})
	mustPending(t, err)
	f.Child().End(3)
	<-s.Ended()
	info := s.Info()
	if info.Status != StatusExited || info.ExitCode == nil || *info.ExitCode != 3 {
		t.Fatalf("status %s, exit %v", info.Status, info.ExitCode)
	}
	if e := entryOf(t, s, req.Seq); e.State != PermissionDenied || e.Why != EndedBeforeAnswerWords {
		t.Fatalf("the open request %+v", e)
	}
	if w := takeWord(t, words); !errors.Is(w.err, ErrSessionEnded) {
		t.Fatalf("the queued message's word %+v", w)
	}
	if _, err := s.Submit(context.Background(), Submission{Text: "after"}); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("Submit after the end = %v", err)
	}
	if len(f.Sent()) != 0 {
		t.Fatal("written after the end")
	}
}

// A line the driver cannot read ends the child and the session, with a
// notice.
func TestProtocolUnreadableLineEndsTheSession(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.Fail(errors.New("not the protocol's"))
	<-s.Ended()
	if e := lastEntry(t, s); e.Kind != EntryNotice || e.Code != noticeUnreadable || e.Words != UnreadableWords {
		t.Fatalf("the newest entry %+v", e)
	}
	if got := strings.Join(f.Calls(), " "); got != "begin close_input stop" {
		t.Fatalf("calls %s", got)
	}
	if st := s.Info().Status; st != StatusExited {
		t.Fatalf("status %s", st)
	}
}

// I11: an open request the stream's own result or the turn's end leaves
// behind is withdrawn with its words, its driver sent the protocol's
// cancel, never an allow.
func TestTurnEndWithdrawsOpenRequests(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	f.TurnStarted("")
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"make"}`)})
	f.Permission(PermissionRequest{Ref: "req-1", Call: "c1", Tool: "Bash", Input: []byte(`{"command":"make"}`)})
	first := lastEntry(t, s)
	f.Permission(PermissionRequest{Ref: "req-2", Call: "c2", Tool: "Bash", Input: []byte(`{"command":"make install"}`)})
	second := lastEntry(t, s)
	f.ToolEnded("c1", ToolResult{Status: ToolOK})
	if e := entryOf(t, s, first.Seq); e.State != PermissionWithdrawn || e.Why != AnsweredByAgentWords {
		t.Fatalf("the request whose call ended %+v", e)
	}
	f.TurnEnded("interrupted", Usage{})
	if e := entryOf(t, s, second.Seq); e.State != PermissionWithdrawn || e.Why != TurnStoppedWords {
		t.Fatalf("the request left at the turn's end %+v", e)
	}
	waitCalls(t, f, "both requests answered", func(c []string) bool { return sessiontest.Count(c, "reply") == 2 })
	for _, r := range f.Replies() {
		if r.Verdict.Allow || !r.Verdict.Cancel {
			t.Fatalf("answered %+v", r)
		}
	}
	s.mu.Lock()
	open := len(s.way.Load().perms)
	s.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d requests still open", open)
	}
}
