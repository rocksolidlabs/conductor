package hostagent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// answerViewer is one viewer of a host's session, handed frames as the
// frame loop hands them (handleFrame), with what the session sends it on
// frames.
type answerViewer struct {
	t      *testing.T
	p      *peer
	sub    *session.Subscription
	frames chan []byte
}

func (v *answerViewer) answer(reqID, question, input, text string) {
	f := proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: reqID, Question: question, Input: input, Text: text})
	v.p.handleFrame(proto.Frame{Type: proto.TypeControl, Payload: f[1:]})
}

// submit hands the viewer's submit to the frame loop, as a page sends it (#322).
func (v *answerViewer) submit(reqID, text string, unlessWaiting bool) {
	f := proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: reqID, Text: text, UnlessWaiting: unlessWaiting})
	v.p.handleFrame(proto.Frame{Type: proto.TypeControl, Payload: f[1:]})
}

// verdict waits for the session's word on the answer or submit reqID.
func (v *answerViewer) verdict(reqID string) map[string]any {
	v.t.Helper()
	return untilControl(v.t, v.frames, func(m map[string]any) bool {
		return (m["t"] == proto.CtlAnswerOK || m["t"] == proto.CtlSubmitOK || m["t"] == proto.CtlError) && m["requestId"] == reqID
	})
}

// answerHost is a host's session over a FakeProc with two of its viewers,
// controllers on the same session.
func answerHost(t *testing.T) (*session.Local, *sessiontest.FakeProc, *answerViewer, *answerViewer) {
	t.Helper()
	proc := sessiontest.NewFakeProc()
	local := session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{SubmitPause: 30 * time.Millisecond, FileEdit: "off", Log: discardLog})
	t.Cleanup(func() { proc.End(0) })
	a := &agent{opts: Options{}, local: local, peers: map[string]*peer{}, log: discardLog}
	viewer := func(id, name string) *answerViewer {
		p := newPeer(a, id, session.RoleControl, "", "", true)
		frames := make(chan []byte, 1024)
		sub, err := local.AttachWith(session.AttachOptions{ID: id, Role: session.RoleControl, Name: name}, sinkFunc(func(f []byte) { frames <- f }))
		if err != nil {
			t.Fatal(err)
		}
		p.sub = sub
		t.Cleanup(p.close)
		return &answerViewer{t: t, p: p, sub: sub, frames: frames}
	}
	return local, proc, viewer("0123456789abcdef", "nate"), viewer("fedcba9876543210", "jane")
}

// Two viewers of a host's session answer one question together: their
// answers wait in their own queues for the session's writer, and the one
// taken first is written (answer_ok naming it) while the other is refused
// (question_answered naming it), nothing of it written; the next question
// waits, untouched by the late one, and takes its own answer. A line sent
// for a question through the typing queue is held to the same.
func TestTwoViewersOfAHostAnswerOneQuestion(t *testing.T) {
	local, proc, nate, jane := answerHost(t)
	local.SetAttentionFull(session.AttentionNeedsInput, "Allow Bash?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}})
	q := local.Info().Attention.ID
	// The program is slow to take its input: both answers wait for it.
	for len(proc.Input) < cap(proc.Input) {
		proc.Input <- nil
	}
	nate.answer("n1", q, "1", "")
	jane.answer("j1", q, "1", "")
	// Each viewer's queue hands its answer to the session; then the
	// program reads: the fillers, then the one answer written.
	time.Sleep(100 * time.Millisecond)
	first := func() string {
		for {
			select {
			case b := <-proc.Input:
				if b != nil {
					return string(b)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("nothing written")
			}
		}
	}()
	if first != "1" {
		t.Fatalf("written %q", first)
	}
	words := []map[string]any{nate.verdict("n1"), jane.verdict("j1")}
	var ok, refused int
	for _, m := range words {
		switch {
		case m["t"] == proto.CtlAnswerOK:
			ok++
		case m["code"] == proto.ErrCodeQuestionAnswered:
			refused++
		default:
			t.Fatalf("a word %v", m)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("%d taken, %d refused: %v", ok, refused, words)
	}
	select {
	case b := <-proc.Input:
		t.Fatalf("written again %q", b)
	case <-time.After(200 * time.Millisecond):
	}

	// The next question; the late answer to the first lands on nothing.
	local.SetAttentionFull(session.AttentionNeedsInput, "Which database?", session.SourceAPI, session.KindPrompt, nil)
	next := local.Info().Attention.ID
	jane.answer("j2", q, "1", "")
	if m := jane.verdict("j2"); m["code"] != proto.ErrCodeQuestionAnswered {
		t.Fatalf("the late answer: %v", m)
	}
	jane.answer("j3", q, "", "SQLite")
	if m := jane.verdict("j3"); m["code"] != proto.ErrCodeQuestionAnswered {
		t.Fatalf("the late line: %v", m)
	}
	if a := local.Info().Attention; a.ID != next || a.State != session.AttentionNeedsInput {
		t.Fatalf("the next question: %+v", a)
	}
	// Its own line answers it, typed through the typing queue; the same
	// line from the other viewer after it is refused.
	nate.answer("n2", next, "", "Postgres")
	if m := nate.verdict("n2"); m["t"] != proto.CtlAnswerOK {
		t.Fatalf("the line: %v", m)
	}
	for _, want := range []string{"Postgres", "\r"} {
		select {
		case b := <-proc.Input:
			if string(b) != want {
				t.Fatalf("written %q, want %q", b, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q not written", want)
		}
	}
	jane.answer("j4", next, "", "SQLite")
	if m := jane.verdict("j4"); m["code"] != proto.ErrCodeQuestionAnswered {
		t.Fatalf("the second line: %v", m)
	}
	// An answer past its bounds names its request; nothing waits for it.
	nate.answer("n3", "", "1", "")
	if m := nate.verdict("n3"); m["code"] != proto.ErrCodeBadFrame {
		t.Fatalf("an answer naming no question: %v", m)
	}
	select {
	case b := <-proc.Input:
		t.Fatalf("written %q", b)
	case <-time.After(200 * time.Millisecond):
	}
}

// A view-only viewer's answer is refused on the frame loop, naming it, and
// never queued.
func TestAViewOnlyViewersAnswerIsRefused(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	local := session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog})
	t.Cleanup(func() { proc.End(0) })
	a := &agent{opts: Options{}, local: local, peers: map[string]*peer{}, log: discardLog}
	p := newPeer(a, "0123456789abcdef", session.RoleView, "", "", false)
	frames := make(chan []byte, 64)
	sub, err := local.AttachWith(session.AttachOptions{ID: p.id, Role: session.RoleView, Name: "guest"}, sinkFunc(func(f []byte) { frames <- f }))
	if err != nil {
		t.Fatal(err)
	}
	p.sub = sub
	t.Cleanup(p.close)
	local.SetAttentionFull(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "y"}})
	v := &answerViewer{t: t, p: p, sub: sub, frames: frames}
	v.answer("g1", local.Info().Attention.ID, "y", "")
	if m := v.verdict("g1"); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view-only answer: %v", m)
	}
	select {
	case b := <-proc.Input:
		t.Fatalf("written %q", b)
	case <-time.After(100 * time.Millisecond):
	}
}

// A report the server forwards takes no id in the server's listing (the
// session gives it its own as it applies it). The host tells the server
// its attention, with its id, after every one: also after one its session
// takes as changing nothing (the same as it shows: no change, no OnChange),
// so that the server never holds none, or an older one. (The bell agent's
// review of its own design, hosted sessions.)
func TestTheHostTellsItsIDAfterEveryForwardedReport(t *testing.T) {
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	sent := make(chan proto.HostAttentionMsg, 16)
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog}
	a.sendHook = func(v any) {
		if m, ok := v.(proto.HostAttentionMsg); ok {
			sent <- m
		}
	}
	a.local = session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{FileEdit: "off", Log: discardLog, OnChange: a.noteChange})
	report := func() []byte {
		b, _ := json.Marshal(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow Bash?", Source: session.SourceAPI, Kind: session.KindPermission, Options: []proto.AttentionOption{{Label: "Yes", Input: "1"}}})
		return b
	}
	next := func(what string) proto.HostAttentionMsg {
		t.Helper()
		select {
		case m := <-sent:
			return m
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the host told the server nothing", what)
			return proto.HostAttentionMsg{}
		}
	}
	if err := a.handleControl(t.Context(), report()); err != nil {
		t.Fatal(err)
	}
	id := a.local.Info().Attention.ID
	if m := next("the first report"); m.ID != id || id == "" {
		t.Fatalf("told %+v, the session's id %q", m, id)
	}
	for len(sent) > 0 {
		<-sent
	}
	// The same report again: the session keeps what it shows, and its id.
	if err := a.handleControl(t.Context(), report()); err != nil {
		t.Fatal(err)
	}
	if a.local.Info().Attention.ID != id {
		t.Fatal("the same report gave the question another id")
	}
	if m := next("the same report again"); m.ID != id || m.State != "needs_input" {
		t.Fatalf("told %+v after the same report, the session's id %q", m, id)
	}
}

// The host tells the server each question with its id, a question asked
// again with the same words included: the server's listings name the
// question the host holds.
func TestTheHostTellsTheServerEachQuestionsID(t *testing.T) {
	x := session.Attention{State: session.AttentionNeedsInput, Message: "Allow?", ID: "0123456789abcdef"}
	y := x
	y.ID = "fedcba9876543210"
	if sameAttention(x, y) {
		t.Fatal("the same words asked again were taken for the question told already")
	}
	if m := hostAttentionMsg("s", x, nil); m.ID != x.ID {
		t.Fatalf("the message %+v", m)
	}
}
