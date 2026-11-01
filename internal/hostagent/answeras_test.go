package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Routed answers on the host (answer_as, C5, answeras.go): the tests drive
// the control messages through handleControl on a session over a process
// in memory, and wait on the session's own state, the answer queue and the
// process's writes, never on the clock.

// answerWait bounds a wait for what must happen; no product bound is
// tested by it.
const answerWait = 10 * time.Second

// logRecorder keeps the messages logged at warn and above, and the
// outcome of each routed answer applied (logged at debug).
type logRecorder struct {
	mu      sync.Mutex
	msgs    []string
	applied []error
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Level >= slog.LevelWarn {
		r.msgs = append(r.msgs, rec.Message)
	}
	if rec.Message == "routed answer" {
		var err error
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "err" {
				err, _ = a.Value.Any().(error)
			}
			return true
		})
		r.applied = append(r.applied, err)
	}
	return nil
}

// outcomes is the errors the routed answers applied so far ended with
// (nil for one applied), in order.
func (r *logRecorder) outcomes() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.applied)
}
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// count is how many messages logged contain words.
func (r *logRecorder) count(words string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if strings.Contains(m, words) {
			n++
		}
	}
	return n
}

// gatedProc is a process that takes no write until open is called: a
// program that reads nothing. entered is told each write begun.
type gatedProc struct {
	*sessiontest.FakeProc
	gate    chan struct{}
	once    sync.Once
	entered chan []byte
}

func newGatedProc() *gatedProc {
	return &gatedProc{FakeProc: sessiontest.NewFakeProc(), gate: make(chan struct{}), entered: make(chan []byte, 64)}
}

func (g *gatedProc) Write(b []byte) (int, error) {
	select {
	case g.entered <- bytes.Clone(b):
	default:
	}
	select {
	case <-g.gate:
	case <-g.Done():
	}
	return g.FakeProc.Write(b)
}

func (g *gatedProc) open() { g.once.Do(func() { close(g.gate) }) }

// answering is an agent hosting a session over proc, its attention changes
// recorded as the server would be told them, with an answer queue open as
// a control connection opens one.
type answering struct {
	a     *agent
	logs  *logRecorder
	sent  chan proto.HostAttentionMsg
	close func()
}

func newAnswering(t *testing.T, proc session.Process, opts Options) *answering {
	t.Helper()
	logs := &logRecorder{}
	h := &answering{logs: logs, sent: make(chan proto.HostAttentionMsg, 256)}
	h.a = &agent{opts: opts, peers: map[string]*peer{}, log: slog.New(logs), sessID: "s"}
	h.a.sendHook = func(v any) {
		if m, ok := v.(proto.HostAttentionMsg); ok {
			h.sent <- m
		}
	}
	h.a.local = session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24, Status: session.StatusRunning}, proc, session.Options{FileEdit: "off", Log: discardLog, OnChange: h.a.noteChange, InputWait: answerWait})
	var once sync.Once
	end := h.a.openAnswers(context.Background())
	h.close = func() { once.Do(end) }
	t.Cleanup(h.close)
	return h
}

// answer sends the host an answer_as as the server does.
func (h *answering) answer(t *testing.T, m proto.HostAnswerAsMsg) {
	t.Helper()
	m.T = proto.HostAnswerAs
	b, _ := json.Marshal(m)
	if err := h.a.handleControl(t.Context(), b); err != nil {
		t.Fatal(err)
	}
}

// raw sends the host the bytes data as a control message.
func (h *answering) raw(t *testing.T, data string) {
	t.Helper()
	if err := h.a.handleControl(t.Context(), []byte(data)); err != nil {
		t.Fatal(err)
	}
}

// idle waits until the answer queue applies nothing.
func (h *answering) idle() {
	h.a.mu.Lock()
	ra := h.a.answers
	h.a.mu.Unlock()
	if ra != nil {
		ra.q.wait()
	}
}

// started is how many goroutines the answer queue has begun.
func (h *answering) started() int {
	h.a.mu.Lock()
	ra := h.a.answers
	h.a.mu.Unlock()
	if ra == nil {
		return 0
	}
	ra.q.mu.Lock()
	defer ra.q.mu.Unlock()
	return ra.q.started
}

// ask shows a question with choices and answers its id.
func ask(t *testing.T, l *session.Local, words string, choices []session.Option) string {
	t.Helper()
	l.SetAttentionFull(session.AttentionNeedsInput, words, session.SourceAPI, session.KindPrompt, choices)
	a := l.Info().Attention
	if a.State != session.AttentionNeedsInput || a.ID == "" {
		t.Fatalf("asked %q: %+v", words, a)
	}
	return a.ID
}

// waitFor polls ok until it holds, failing after answerWait.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(answerWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// writes takes what the process was written so far.
func writes(proc *sessiontest.FakeProc) []string {
	var out []string
	for {
		select {
		case b := <-proc.Input:
			out = append(out, string(b))
		default:
			return out
		}
	}
}

var yesNo = []session.Option{{Label: "Yes", Input: "y"}, {Label: "No", Input: "n"}}

// A choice answer_as writes the choice's keys once, recorded under the
// name it carries, and the host's next attention tells the server the
// question no longer waits; a line answer_as is typed as a reply naming
// its question, entered once.
func TestAnswerAsApplied(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{})
	l := h.a.local
	q := ask(t, l, "Allow?", yesNo)
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "y", ByName: "Ada", BySubject: "u-1"})
	h.idle()
	var got []string
	waitFor(t, "the choice written", func() bool { got = append(got, writes(proc)...); return len(got) > 0 })
	if !slices.Equal(got, []string{"y"}) {
		t.Fatalf("written %q", got)
	}
	info := l.Info()
	if info.Attention.ID == q || info.LastAnswer == nil || info.LastAnswer.ByName != "Ada" {
		t.Fatalf("after the answer: %+v, answer %+v", info.Attention, info.LastAnswer)
	}
	waitFor(t, "the server told the question is answered", func() bool {
		for {
			select {
			case m := <-h.sent:
				if m.ID != q {
					return true
				}
			default:
				return false
			}
		}
	})
	// The same answer again: the question no longer waits, nothing written.
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "y", ByName: "Ada"})
	h.idle()
	if w := writes(proc); len(w) != 0 {
		t.Fatalf("a second answer wrote %q", w)
	}

	q2 := ask(t, l, "Which database?", nil)
	h.answer(t, proto.HostAnswerAsMsg{Question: q2, Text: "Postgres", ByName: "Ada"})
	h.idle()
	all := strings.Join(writes(proc), "")
	if strings.Count(all, "Postgres") != 1 || !strings.HasSuffix(all, "\r") {
		t.Fatalf("the line written %q", all)
	}
	if info := l.Info(); info.Attention.ID == q2 {
		t.Fatalf("the line left its question shown: %+v", info.Attention)
	}
}

// An answer_as out of its bounds is dropped and logged, nothing written;
// so is one no viewer could have sent either way (keys that are no choice
// of the question, a line blank once cleaned): the session refuses it.
func TestAnswerAsInvalidDropped(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{})
	q := ask(t, h.a.local, "Allow?", yesNo)
	for _, m := range []proto.HostAnswerAsMsg{
		{Question: q, Input: "y", Text: "yes", ByName: "Ada"},
		{Question: q, ByName: "Ada"},
		{Question: "", Input: "y", ByName: "Ada"},
		{Question: q, Input: "y"},
		{Question: q, Input: "y", ByName: strings.Repeat("a", proto.MaxNameLen+1)},
		{Question: q, Input: "y", ByName: "Ada", BySubject: "u 1"},
		{Question: q, Input: strings.Repeat("y", proto.MaxAnswerInput+1), ByName: "Ada"},
	} {
		h.answer(t, m)
	}
	h.raw(t, `{"t":"answer_as","question":7}`)
	if n := h.logs.count("out of its bounds"); n != 8 {
		t.Fatalf("%d logged out of bounds, want 8", n)
	}
	if n := h.started(); n != 0 {
		t.Fatalf("an answer out of its bounds was queued (%d goroutines)", n)
	}
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "z", ByName: "Ada"})
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "y\x1b[2J", ByName: "Ada"})
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Text: " \r\n\t ", ByName: "Ada"})
	h.idle()
	if w := writes(proc); len(w) != 0 {
		t.Fatalf("written %q", w)
	}
	if a := h.a.local.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
		t.Fatalf("the question went: %+v", a)
	}
}

// An answer_as is taken off the control loop: handleControl returns while
// the program has not taken the answer's keys, and they are written once it
// does.
func TestAnswerAsNeverInTheControlLoop(t *testing.T) {
	proc := newGatedProc()
	t.Cleanup(func() { proc.open(); proc.End(0) })
	h := newAnswering(t, proc, Options{})
	q := ask(t, h.a.local, "Allow?", yesNo)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.answer(t, proto.HostAnswerAsMsg{Question: q, Input: "y", ByName: "Ada"})
	}()
	select {
	case <-returned:
	case <-time.After(answerWait):
		t.Fatal("handleControl waited on the program")
	}
	select {
	case b := <-proc.entered:
		if string(b) != "y" {
			t.Fatalf("began writing %q", b)
		}
	case <-time.After(answerWait):
		t.Fatal("the answer's keys were never written")
	}
	if w := writes(proc.FakeProc); len(w) != 0 {
		t.Fatalf("the program took %q with its gate shut", w)
	}
	proc.open()
	var got []string
	waitFor(t, "the keys taken", func() bool { got = append(got, writes(proc.FakeProc)...); return len(got) > 0 })
	if !slices.Equal(got, []string{"y"}) {
		t.Fatalf("written %q", got)
	}
}

// A burst of line answers while the program reads nothing: at most
// answerQueue held (the one applying among them), the rest dropped and
// logged, one goroutine applying them; the connection's end ends them all,
// and only the first is ever typed.
func TestAnswerAsQueueBounded(t *testing.T) {
	proc := newGatedProc()
	t.Cleanup(func() { proc.open(); proc.End(0) })
	h := newAnswering(t, proc, Options{})
	q := ask(t, h.a.local, "Which database?", nil)
	const burst = 10
	for i := range burst {
		h.answer(t, proto.HostAnswerAsMsg{Question: q, Text: fmt.Sprintf("answer-%d", i), ByName: "Ada"})
	}
	if n := h.logs.count("too many routed answers wait"); n != burst-answerQueue {
		t.Fatalf("%d dropped, want %d", n, burst-answerQueue)
	}
	select {
	case b := <-proc.entered:
		if !strings.Contains(string(b), "answer-0") {
			t.Fatalf("began writing %q", b)
		}
	case <-time.After(answerWait):
		t.Fatal("the first answer was never typed")
	}
	if n := h.started(); n != 1 {
		t.Fatalf("%d goroutines applied the answers", n)
	}
	// The connection ends: those waiting are dropped, the one applying
	// ends through its context, and the queue's goroutine with it. The
	// queue is looked at itself, kept from before its connection let it
	// go: the agent holds none after.
	h.a.mu.Lock()
	ra := h.a.answers
	h.a.mu.Unlock()
	ended := make(chan struct{})
	go func() {
		h.close()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(answerWait):
		t.Fatal("the answers outlived their connection")
	}
	ra.q.mu.Lock()
	running, closed, done := ra.q.running, ra.q.closed, ra.q.done
	ra.q.mu.Unlock()
	if running || !closed {
		t.Fatalf("after its connection the queue runs %v, closed %v", running, closed)
	}
	select {
	case <-done:
	default:
		t.Fatal("the queue's goroutine outlived its connection")
	}
	// The one applying ended through its context, with the program still
	// reading nothing: its text's write begun, it is accepted (pending) or
	// withdrawn, never applied, and nothing else was.
	got := h.logs.outcomes()
	if len(got) != 1 || got[0] == nil || !errors.Is(got[0], session.ErrSubmitPending) && !errors.Is(got[0], session.ErrRevoked) && !errors.Is(got[0], context.Canceled) {
		t.Fatalf("the answers applied ended with %v", got)
	}
	proc.open()
	var all string
	waitFor(t, "the first answer taken", func() bool {
		all += strings.Join(writes(proc.FakeProc), "")
		return strings.Contains(all, "answer-0")
	})
	for i := 1; i < burst; i++ {
		if strings.Contains(all, fmt.Sprintf("answer-%d", i)) {
			t.Fatalf("answer %d was typed: %q", i, all)
		}
	}
}

// The connection ends while a line answer is in its pause, its text typed
// and its Enter not: the answer is withdrawn (a kind of ErrRevoked), its
// Enter never written, and the session's turn is held for a person, its
// question's claim let go as the hold shows, so that no later line joins
// the text and a person's keys show the question again to be answered.
func TestAnswerAsWithdrawnWithItsConnection(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	logs := &logRecorder{}
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: slog.New(logs), sessID: "s"}
	a.sendHook = func(any) {}
	// The pause after the text outlasts the test: only the connection's
	// end ends it.
	a.local = session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24, Status: session.StatusRunning}, proc, session.Options{FileEdit: "off", Log: discardLog, OnChange: a.noteChange, InputWait: answerWait, SubmitPause: time.Hour})
	end := a.openAnswers(context.Background())
	var once sync.Once
	t.Cleanup(func() { once.Do(end) })
	h := &answering{a: a, logs: logs, close: func() { once.Do(end) }}
	l := a.local
	q := ask(t, l, "Which database?", []session.Option{{Label: "SQLite", Input: "s"}})
	h.answer(t, proto.HostAnswerAsMsg{Question: q, Text: "Postgres", ByName: "Ada"})
	var typed string
	waitFor(t, "the text typed", func() bool { typed += strings.Join(writes(proc), ""); return strings.Contains(typed, "Postgres") })
	h.close()
	got := logs.outcomes()
	if len(got) != 1 || !errors.Is(got[0], session.ErrRevoked) {
		t.Fatalf("the answer in its pause ended with %v, want a kind of ErrRevoked", got)
	}
	if strings.Contains(typed+strings.Join(writes(proc), ""), "\r") {
		t.Fatal("the Enter was written after the connection ended")
	}
	waitFor(t, "the turn held for a person", l.Held)
	owner, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Nate", Owner: true}, discardSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(owner, []byte{0x15}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question shown again, open", func() bool {
		return !l.Held() && l.Info().Attention.ID == q
	})
	// Its claim let go, the question takes an answer again: a choice's
	// keys, which have no pause.
	ctx, cancel := context.WithTimeout(t.Context(), answerWait)
	defer cancel()
	if err := l.AnswerAs(ctx, q, "s", session.Actor{Name: "Nate"}); err != nil {
		t.Fatalf("an answer after the hold: %v", err)
	}
	if info := l.Info(); info.LastAnswer == nil || info.LastAnswer.ByName != "Nate" {
		t.Fatalf("after the answer: %+v", info.LastAnswer)
	}
}

// Keys typed in the host's terminal answer the question shown, which stays
// drawn with its id (Answered: a dialog read off the screen, which a
// session marks so at once): the host's forwarder tells the server the
// change that only marks it, so that its router resolves a routed
// question while the program stays quiet, and tells nothing again for the
// same.
func TestAnsweredMarkSent(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{})
	const q = "0123456789abcdef"
	shown := session.Attention{State: session.AttentionNeedsInput, Message: "Allow curl?", Source: session.SourceQuestion, Kind: session.KindPermission, ID: q, Options: yesNo}
	// The forwarder sends on the caller's goroutine (sendHook): what it
	// told is in sent once onLocalChange returns.
	told := func() []proto.HostAttentionMsg {
		var out []proto.HostAttentionMsg
		for len(h.sent) > 0 {
			out = append(out, <-h.sent)
		}
		return out
	}
	told()
	h.a.onLocalChange(session.Info{ID: "s", Attention: shown})
	if got := told(); len(got) != 1 || got[0].ID != q || got[0].Answered != "" {
		t.Fatalf("the question told as %+v", got)
	}
	answered := shown
	answered.Answered = session.AnsweredTerminal
	h.a.onLocalChange(session.Info{ID: "s", Attention: answered})
	if got := told(); len(got) != 1 || got[0].ID != q || got[0].State != "needs_input" || got[0].Answered != session.AnsweredTerminal {
		t.Fatalf("the mark told as %+v", got)
	}
	h.a.onLocalChange(session.Info{ID: "s", Attention: answered})
	if got := told(); len(got) != 0 {
		t.Fatalf("the same told again: %+v", got)
	}
}

// Published to a switchyard, the host says no answerAs, an answer_as that
// arrives anyway types nothing and is logged, and its attention goes
// without kept; to the owner's own server it says answerAs.
func TestSwitchyardAnswerAsRefused(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{Switchyard: true})
	if reg := h.a.registration(80, 24); reg.Host.AnswerAs {
		t.Fatal("a switchyard was told the host takes answers")
	}
	if reg := (&agent{opts: Options{}}).registration(80, 24); !reg.Host.AnswerAs {
		t.Fatal("the owner's own server was not told the host takes answers")
	}
	l := h.a.local
	a := ask(t, l, "Allow?", yesNo)
	h.answer(t, proto.HostAnswerAsMsg{Question: a, Input: "y", ByName: "Ada"})
	if n := h.logs.count("dropped unread"); n != 1 {
		t.Fatalf("%d logged dropped", n)
	}
	h.idle()
	if w := writes(proc); len(w) != 0 || h.started() != 0 {
		t.Fatalf("written %q (%d goroutines)", w, h.started())
	}
	// The forwarder sends on the caller's goroutine (sendHook): what it
	// told is in sent once onLocalChange returns.
	h.a.onLocalChange(session.Info{ID: "s", Attention: session.Attention{State: session.AttentionWorking}, Kept: []string{a}})
	told := false
	for len(h.sent) > 0 {
		m := <-h.sent
		if m.Kept != nil {
			t.Fatalf("a switchyard was told kept %v", m.Kept)
		}
		told = told || m.State == "working"
	}
	if !told {
		t.Fatal("the working change was never told")
	}
}

// What the session keeps changes while what it shows does not (the agent's
// own question set aside under a working state, then let go by a person's
// keys): the server is told an attention whose kept alone changed.
func TestKeptSent(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	h := newAnswering(t, proc, Options{})
	l := h.a.local
	l.Ask("Which database?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Postgres", Input: "Postgres\r"}})
	a := l.Info().Attention.ID
	l.SetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil)
	if info := l.Info(); info.Attention.State != session.AttentionWorking || !slices.Equal(info.Kept, []string{a}) {
		t.Fatalf("set aside: %+v %v", info.Attention, info.Kept)
	}
	next := func(what string, ok func(proto.HostAttentionMsg) bool) proto.HostAttentionMsg {
		t.Helper()
		deadline := time.After(answerWait)
		for {
			select {
			case m := <-h.sent:
				if ok(m) {
					return m
				}
			case <-deadline:
				t.Fatalf("never told %s", what)
			}
		}
	}
	aside := next("working with A kept", func(m proto.HostAttentionMsg) bool {
		return m.State == "working" && slices.Equal(m.Kept, []string{a})
	})
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Nate", Owner: true}, discardSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A let go", func() bool { return len(l.Info().Kept) == 0 })
	gone := next("A no longer kept", func(m proto.HostAttentionMsg) bool { return len(m.Kept) == 0 })
	aside.Kept, gone.Kept = nil, nil
	if fmt.Sprint(aside) != fmt.Sprint(gone) {
		t.Fatalf("more than kept changed: %+v then %+v", aside, gone)
	}
}

// Changes made while the server reads nothing and a retry is due (A asked,
// set aside, shown again at the done): every attention message the server
// gets has its shown id and its kept from one Info, A in one or the other
// in each, and the last shows A, kept by none.
func TestKeptSentWithItsAttention(t *testing.T) {
	s, c, pc := dialHeld(t, 0)
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", controlWait: 100 * time.Millisecond}
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog})
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	big := bytes.Repeat([]byte("b"), 1<<20)
	if err := w.sendRelay(big, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	backedUp(t, pc)
	l := a.local
	l.Ask("Which database?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Postgres", Input: "Postgres\r"}})
	id := l.Info().Attention.ID
	waitFor(t, "a retry under way", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.attentionRetry
	})
	l.SetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil)
	waitFor(t, "the working change noted", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.lastAttention.State == session.AttentionWorking
	})
	l.SetAttentionFull(session.AttentionDone, "done for now", session.SourceAPI, session.KindDone, nil)
	if got := l.Info(); got.Attention.ID != id || len(got.Kept) != 0 {
		t.Fatalf("at the done: %+v %v", got.Attention, got.Kept)
	}
	close(s.read)
	s.expectGot(t, big)
	// The retry and the forwarder done with every change, a marker sent
	// after them in the connection's order: every message before it is
	// one the forwarder sent, and the last tells the session as it is now.
	waitFor(t, "the retry and the forwarder done", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.attentionRetry && !a.forwarding
	})
	const marker = "test_marker"
	if err := a.send(map[string]string{"t": marker}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(answerWait)
	var last proto.HostAttentionMsg
	for n := 0; ; n++ {
		var m proto.HostAttentionMsg
		select {
		case got := <-s.got:
			if err := json.Unmarshal(got, &m); err != nil {
				t.Fatalf("the server got %.80q", got)
			}
		case <-deadline:
			t.Fatal("the server was never sent the marker")
		}
		if m.T == marker {
			if n == 0 || last.ID != id || len(last.Kept) != 0 || last.State != "needs_input" {
				t.Fatalf("after %d messages the last told %+v, want A shown, kept by none", n, last)
			}
			return
		}
		if m.T != proto.HostAttention {
			t.Fatalf("the server got %+v", m)
		}
		if m.ID != id && !slices.Contains(m.Kept, id) {
			t.Fatalf("message %d shows %q and keeps %v: A in neither", n, m.ID, m.Kept)
		}
		if m.ID == id && slices.Contains(m.Kept, id) {
			t.Fatalf("message %d shows and keeps A", n)
		}
		last = m
	}
}
