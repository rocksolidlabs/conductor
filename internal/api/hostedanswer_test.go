package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The decision router over hosted sessions (C5, answer_as): a question a
// host's session shows is routed once its host said answerAs, a route's
// answer travels to the host, which applies it under its own claim, and
// the host's attention (with what its session keeps) says when the
// question stops waiting. The tests wait on the router's steps
// (routerSeam), the server's copy of the session and the program's own
// output, never on the clock.

// hostedQuestion reports the question body to the hosted session id as an
// admin does (forwarded to its host), and answers the id the router f was
// given for it once the host told it back: the router's record holds,
// where the question shown may pass by at once (a route that answers it).
func hostedQuestion(t *testing.T, e *testEnv, f *fakeRouter, id string, body map[string]any) string {
	t.Helper()
	report(t, e, id, body)
	var q string
	until(t, "the router given "+fmt.Sprint(body["message"]), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, d := range f.routed {
			if d.Session.ID == id && d.Message == body["message"] {
				q = d.ID
				return true
			}
		}
		return false
	})
	return q
}

// report posts body as the session's attention, again while it is
// refused for the bucket (429), which fills as time passes.
func report(t *testing.T, e *testEnv, id string, body map[string]any) {
	t.Helper()
	until(t, "the report taken", func() bool {
		resp, out := e.do("POST", "/api/sessions/"+id+"/attention", adminToken, body)
		switch resp.StatusCode {
		case http.StatusOK:
			return true
		case http.StatusTooManyRequests:
			return false
		}
		t.Fatalf("report %v: %d %v", body, resp.StatusCode, out)
		return false
	})
}

// hostedSession is the hosted session id of e.
func hostedSession(t *testing.T, e *testEnv, id string) *signal.HostedSession {
	t.Helper()
	d, ok := e.srv.registry.Get(id)
	if !ok {
		t.Fatalf("no session %s", id)
	}
	hs, ok := d.(*signal.HostedSession)
	if !ok {
		t.Fatalf("session %s is not hosted", id)
	}
	return hs
}

// yes is a question's one choice, which a line's program takes.
var yes = []session.Option{{Label: "Yes", Input: "y\r"}}

// A route's answer for a question a real `conductor host` session shows is
// sent to the host (answer_as) and applied there once: the program reads
// the choice once, and the host's attention says the question no longer
// waits, which resolves it once.
func TestRoutedAnswerReachesAHostedSession(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		return ext.Route{Answer: &ext.Answer{Input: "y\r", By: ada}}, nil
	}}
	e := routerEnv(t, f)
	out := filepath.Join(e.root, "read")
	id := startHost(t, e, []string{"/bin/sh", "-c", `exec cat > "$0"`, out})
	until(t, "the host says answerAs", func() bool { return hostedSession(t, e, id).TakesAnswers() })
	q := hostedQuestion(t, e, f, id, map[string]any{"state": "needs_input", "message": "Allow?", "kind": "permission", "options": yes})
	routed := seams.wait(t, "routed", q)
	if routed.rt.Answer == nil || routed.sess != id {
		t.Fatalf("routed %+v", routed)
	}
	if got := seams.wait(t, "applied", q); got.err != nil {
		t.Fatalf("applied: %v", got.err)
	}
	until(t, "the program read the choice", func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), "y\n")
	})
	until(t, "the host's attention says it was answered", func() bool {
		return hostedSession(t, e, id).Info().Attention.ID != q
	})
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, id)
	if b, _ := os.ReadFile(out); string(b) != "y\n" {
		t.Fatalf("the program read %q", b)
	}
	if n := count(f.routedIDs(), q); n != 1 {
		t.Fatalf("routed %d times", n)
	}
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
}

// A host that did not say answerAs (an older one): a provider's answer to
// its question is unsupported and nothing is sent it, and the router
// routes none of its questions.
func TestOlderHostUnsupported(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	h := dialFakeHost(t, e, "")
	const q = "0123456789abcdef"
	h.send(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow?", Source: session.SourceAPI, Kind: session.KindPermission, ID: q, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}})
	until(t, "the question shown", func() bool { return hostedSession(t, e, h.sessionID).Info().Attention.ID == q })
	seams.settle(t, e, h.sessionID)
	if ids := f.routedIDs(); len(ids) != 0 {
		t.Fatalf("an older host's question routed: %v", ids)
	}
	_, err := testHost(e.srv, t.Context(), "switchman").Submit(t.Context(), ext.SubmitRequest{Session: h.sessionID, Question: q, Input: "y", By: ada})
	if !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("an answer to an older host's question: %v", err)
	}
	if got := ofType(h.drain(200*time.Millisecond), proto.HostAnswerAs); len(got) != 0 {
		t.Fatalf("an older host was sent %v", got)
	}
}

// A real host's agent asks its own question A and the work sets it aside;
// 70 questions are routed and answered through answer_as before A shows
// again at the turn's done: A is routed once, kept (never resolved) all
// along, and told Resolved once, at its answer.
func TestHostedSetAsidePastTheBound(t *testing.T) {
	seams := watchRouter(t)
	const ownWords = "Which database?"
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		if d.Message == ownWords {
			return ext.Route{}, nil
		}
		return ext.Route{Answer: &ext.Answer{Input: "SQLite\r", By: ada}}, nil
	}}
	e := routerEnv(t, f)
	id := startHost(t, e, []string{"/bin/cat"})
	until(t, "the host says answerAs", func() bool { return hostedSession(t, e, id).TakesAnswers() })
	a := hostedQuestion(t, e, f, id, map[string]any{"state": "needs_input", "message": ownWords, "kind": "prompt", "options": dbChoices, "ask": true})
	seams.wait(t, "routed", a)
	report(t, e, id, map[string]any{"state": "working"})
	until(t, "A set aside, kept", func() bool {
		info := hostedSession(t, e, id).Info()
		return info.Attention.State == session.AttentionWorking && slices.Equal(info.Kept, []string{a})
	})
	for i := range 70 {
		q := hostedQuestion(t, e, f, id, map[string]any{"state": "needs_input", "message": fmt.Sprintf("Question %d?", i), "kind": "prompt", "options": dbChoices})
		seams.wait(t, "routed", q)
		until(t, fmt.Sprintf("question %d resolved", i), func() bool { _, n := f.resolution(q); return n == 1 })
		if _, n := f.resolution(a); n != 0 {
			t.Fatalf("A resolved at question %d", i)
		}
	}
	seams.settle(t, e, id)
	e.srv.router.mu.Lock()
	kept := len(e.srv.router.sessions[id].order)
	e.srv.router.mu.Unlock()
	if kept > routedKept {
		t.Fatalf("the router keeps %d questions of the session, past %d", kept, routedKept)
	}
	report(t, e, id, map[string]any{"state": "done", "message": "done for now", "kind": "done"})
	until(t, "A shown again at the done", func() bool { return hostedSession(t, e, id).Info().Attention.ID == a })
	seams.settle(t, e, id)
	if n := count(f.routedIDs(), a); n != 1 {
		t.Fatalf("A routed %d times", n)
	}
	if _, n := f.resolution(a); n != 0 {
		t.Fatal("A resolved before its answer")
	}
	res, err := testHost(e.srv, t.Context(), "switchman").Submit(t.Context(), ext.SubmitRequest{Session: id, Question: a, Input: "Postgres\r", By: ada})
	if err != nil || res.Status != "sent" {
		t.Fatalf("A's answer: %+v %v", res, err)
	}
	until(t, "A resolved", func() bool { _, n := f.resolution(a); return n == 1 })
	seams.settle(t, e, id)
	if _, n := f.resolution(a); n != 1 {
		t.Fatalf("A told %d times", n)
	}
}

// A routed question a host's session marks answered by keys typed in its
// terminal, the host quiet after (the dialog still drawn): Resolved once,
// from the mark. The host's side is driven by hand, sending what a host
// sends: a host's session marks a question answered in the terminal only
// for a dialog it reads off the screen, and `conductor host` watches none.
func TestHostedAnsweredInTheTerminalResolved(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	h, _ := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop", AnswerAs: true}, proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}, "Bearer test-host-token")
	const q = "0123456789abcdef"
	shown := proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow curl?", Source: session.SourceQuestion, Kind: session.KindPermission, ID: q, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}}
	h.send(shown)
	seams.wait(t, "routed", q)
	shown.Answered = session.AnsweredTerminal
	h.send(shown)
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, h.sessionID)
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
	if a := hostedSession(t, e, h.sessionID).Info().Attention; a.ID != q {
		t.Fatalf("the dialog went: %+v", a)
	}
}

// A switchyard routes no hosted session's question, a host's that said
// answerAs included, and sends no answer_as: a provider's answer is
// unsupported there.
func TestSwitchyardNeverRoutesAHostedSession(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		return ext.Route{Answer: &ext.Answer{Input: "y", By: ada}}, nil
	}}
	e := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = true }, slog.New(slog.DiscardHandler), nil, WithExtensions(local.Fill(ext.Providers{
		Router: f,
		All:    []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: f}},
	})))
	h, _ := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop", AnswerAs: true}, proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: "x", Cols: 80, Rows: 24}, "Bearer test-host-token")
	const q = "0123456789abcdef"
	h.send(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow?", Source: session.SourceAPI, Kind: session.KindPermission, ID: q, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}})
	until(t, "the question shown", func() bool { return hostedSession(t, e, h.sessionID).Info().Attention.ID == q })
	seams.settle(t, e, h.sessionID)
	if ids := f.routedIDs(); len(ids) != 0 {
		t.Fatalf("a switchyard routed %v", ids)
	}
	_, err := testHost(e.srv, t.Context(), "switchman").Submit(t.Context(), ext.SubmitRequest{Session: h.sessionID, Question: q, Input: "y", By: ada})
	if !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a switchyard's Host.Submit to a hosted session: %v", err)
	}
	if got := ofType(h.drain(200*time.Millisecond), proto.HostAnswerAs); len(got) != 0 {
		t.Fatalf("a switchyard sent %v", got)
	}
}
