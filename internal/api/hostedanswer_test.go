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
	"sync"
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

// sentUpToMarker has the server send the fake host h a marker in its own
// lane (Tell), the lane every answer_as takes, and answers what h got
// before it: an absence among them is ordered after the mechanism, never
// read off a wait on the clock.
func sentUpToMarker(t *testing.T, e *testEnv, h *fakeHost) []map[string]any {
	t.Helper()
	const marker = "test_marker"
	if err := hostedSession(t, e, h.sessionID).Tell(map[string]string{"t": marker}); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	deadline := time.After(testWait)
	for {
		select {
		case m := <-h.msgs:
			if m["t"] == marker {
				return got
			}
			got = append(got, m)
		case <-h.done:
			t.Fatalf("the host's connection ended before the marker: %v", h.err)
		case <-deadline:
			t.Fatal("the marker never reached the host")
		}
	}
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

// A route that names whom a real host's question went to records its
// status entry through the host, which records it and reports it back,
// with the session's bucket spent meanwhile by the agent's reports: the
// entry spends no token of it, as a server session's own spend none.
func TestHostedRouteRecordsItsStatus(t *testing.T) {
	seams := watchRouter(t)
	entered, gate := make(chan struct{}, 1), make(chan struct{})
	f := &fakeRouter{route: func(ctx context.Context, _ ext.Decision) (ext.Route, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}, Rule: "on call"}, nil
	}}
	e := routerEnv(t, f)
	const words = "routed: person Ada (on call)"
	var mu sync.Mutex
	recorded := map[string]int{}
	e.srv.events.addSink(func(sess string, en session.ActivityEntry, _ session.AttentionState) {
		if en.Type == session.ActivityStatus && en.Message == words {
			mu.Lock()
			recorded[sess]++
			mu.Unlock()
		}
	})
	id := startHost(t, e, []string{"/bin/cat"})
	until(t, "the host says answerAs", func() bool { return hostedSession(t, e, id).TakesAnswers() })
	q := hostedQuestion(t, e, f, id, map[string]any{"state": "needs_input", "message": "Allow?", "kind": "permission", "options": yes})
	select {
	case <-entered:
	case <-time.After(testWait):
		t.Fatal("the route was never asked")
	}
	// The agent's reports spend the session's bucket while the route is
	// asked: a forwarded report would find no token for the entry.
	until(t, "the bucket spent", func() bool {
		resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "progress", "message": "busy"})
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return true
		case http.StatusAccepted, http.StatusOK:
			return false
		}
		t.Fatalf("report: %d %v", resp.StatusCode, out)
		return false
	})
	close(gate)
	seams.wait(t, "routed", q)
	until(t, "the entry reported back by the host", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return recorded[id] > 0
	})
	seams.settle(t, e, id)
	mu.Lock()
	n := recorded[id]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("the entry recorded %d times", n)
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
	if got := ofType(sentUpToMarker(t, e, h), proto.HostAnswerAs); len(got) != 0 {
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
	if got := ofType(sentUpToMarker(t, e, h), proto.HostAnswerAs); len(got) != 0 {
		t.Fatalf("a switchyard sent %v", got)
	}
}

// A provider's answer to a hosted question under a context already ended
// (given up, or past its deadline) answers the context's error and sends
// the host nothing, as a server session's submission types nothing; one
// under a live context is sent.
func TestHostedAnswerHonoursItsContext(t *testing.T) {
	f := &fakeRouter{}
	e := routerEnv(t, f)
	h, _ := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop", AnswerAs: true}, proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}, "Bearer test-host-token")
	const q = "0123456789abcdef"
	h.send(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow?", Source: session.SourceAPI, Kind: session.KindPermission, ID: q, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}})
	until(t, "the question shown", func() bool { return hostedSession(t, e, h.sessionID).Info().Attention.ID == q })
	host := testHost(e.srv, t.Context(), "switchman")
	req := ext.SubmitRequest{Session: h.sessionID, Question: q, Input: "y", By: ada}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := host.Submit(cancelled, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("under a cancelled context: %v, want context.Canceled", err)
	}
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := host.Submit(expired, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("under an expired context: %v, want context.DeadlineExceeded", err)
	}
	if got := ofType(sentUpToMarker(t, e, h), proto.HostAnswerAs); len(got) != 0 {
		t.Fatalf("an answer given up was sent: %v", got)
	}
	if res, err := host.Submit(t.Context(), req); err != nil || res.Status != "sent" {
		t.Fatalf("under a live context: %+v %v", res, err)
	}
	if got := ofType(sentUpToMarker(t, e, h), proto.HostAnswerAs); len(got) != 1 || got[0]["question"] != q {
		t.Fatalf("the live answer sent as %v", got)
	}
}

// A host that said answerAs comes back without it (resumed by an older
// host) while a routed question is outstanding: the router routes none of
// its new questions, yet resolves the routed one once the host says it no
// longer waits, told Resolved once, its place under the server's bound
// given back.
func TestHostedResolvedAfterAnswerAsWithdrawn(t *testing.T) {
	seams := watchRouter(t)
	f := &fakeRouter{}
	e := routerEnv(t, f)
	sess := proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}
	h, registered := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop", AnswerAs: true}, sess, "Bearer test-host-token")
	id := h.sessionID
	secret, _ := registered["secret"].(string)
	if secret == "" {
		t.Fatal("the server sent no secret to resume with")
	}
	const q, q2 = "0123456789abcdef", "fedcba9876543210"
	question := func(h *fakeHost, id string) {
		h.send(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow?", Source: session.SourceAPI, Kind: session.KindPermission, ID: id, Options: []proto.AttentionOption{{Label: "Yes", Input: "y"}}})
	}
	question(h, q)
	seams.wait(t, "routed", q)

	// The host goes, and comes back as an older host.
	h.c.CloseNow()
	until(t, "the host gone", func() bool { return hostedSession(t, e, id).Info().Status == session.StatusHostDisconnected })
	h2, again := dialFakeHostRegister(t, e, proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"}, Session: sess,
		Resume: &proto.HostResume{SessionID: id, Secret: secret}}, "Bearer test-host-token", nil)
	if again["resumed"] != true || h2.sessionID != id {
		t.Fatalf("resume: %v", again)
	}
	until(t, "answerAs taken back", func() bool { return !hostedSession(t, e, id).TakesAnswers() })
	question(h2, q)
	until(t, "the host back", func() bool { return hostedSession(t, e, id).Info().Status == session.StatusRunning })
	seams.settle(t, e, id)
	if _, n := f.resolution(q); n != 0 {
		t.Fatal("the question shown still was resolved")
	}

	// A new question is not routed; the routed one, no longer shown, is
	// resolved.
	question(h2, q2)
	until(t, "the new question shown", func() bool { return hostedSession(t, e, id).Info().Attention.ID == q2 })
	until(t, "Resolved", func() bool { _, n := f.resolution(q); return n == 1 })
	seams.settle(t, e, id)
	if _, n := f.resolution(q); n != 1 {
		t.Fatalf("told %d times", n)
	}
	if n := count(f.routedIDs(), q2); n != 0 {
		t.Fatalf("the new question routed %d times", n)
	}
	e.srv.router.mu.Lock()
	untold := e.srv.router.untold
	e.srv.router.mu.Unlock()
	if untold != 0 {
		t.Fatalf("%d routed questions still count against the bound", untold)
	}
}
