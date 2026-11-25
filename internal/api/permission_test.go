package api

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Who answers a chat's permission request through the server
// (docs/plans/chat-or-terminal.md, design 7, row 6).

func entryState(t *testing.T, l *session.Local, seq uint64) session.TranscriptEntry {
	t.Helper()
	es, _ := l.Transcript(seq-1, 1)
	if len(es) != 1 || es[0].Seq != seq {
		t.Fatalf("entry %d not kept", seq)
	}
	return es[0]
}

func permissionAnswer(e session.TranscriptEntry, reqID, choice, message string) []byte {
	return proto.MustControl(proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: reqID, Question: e.Question, Call: e.Call, Sha256: e.InputSha256, Choice: choice, Message: message})
}

// chatAsking is a chat in a turn whose agent's call c-1 is announced and
// put to the prompt tool, the POST held.
func chatAsking(t *testing.T, e *testEnv) (string, *session.Local, *sessiontest.FakeProtocol, string, *heldPost, session.TranscriptEntry) {
	t.Helper()
	id, l, f := chatSession(t, e)
	tok := e.agentToken(id)
	f.TurnStarted("")
	f.ToolStarted(session.ToolStep{Ref: "c-1", Name: "Bash", Input: []byte(`{"command":"make"}`)})
	h := postPermission(e, id, tok, permissionBody("c-1", "Bash", `{"command":"make"}`))
	return id, l, f, tok, h, openEntry(t, l, "c-1")
}

// Through the route and the socket: the operator's allow reaches the held
// POST as {"behavior":"allow"}, recorded allowed once delivered; a deny
// carries the operator's words; a call's result withdraws the request with
// the documented deny; a POST whose client goes is withdrawn "The request
// was lost.", and a later answer is question_answered.
func TestPermissionThroughTheRoute(t *testing.T) {
	e := newTestEnv(t, nil)
	id, l, f, tok, h, r := chatAsking(t, e)
	op := dialViewer(t, e, id, adminToken)
	helloChat(op, ^uint64(0))
	if w := op.until("the welcome", controlOf(proto.CtlWelcome), nil); w.m["permissionAnswer"] != true {
		t.Fatalf("the operator's welcome %v", w.m)
	}
	op.until("ready", controlOf(proto.CtlReady), nil)
	if att := l.Info().Attention; att.Kind != session.KindPermission || att.Source != session.SourceProtocol || att.ID != r.Question {
		t.Fatalf("the attention %+v", att)
	}
	op.send(permissionAnswer(r, "a1", proto.PermissionAllowOnce, ""))
	if c := op.until("the answer's word", func(c chatFrame) bool { return c.names("a1") }, nil); c.m["t"] != proto.CtlAnswerOK {
		t.Fatalf("the operator's answer: %v", c.m)
	}
	if status, out := h.answered(t); status != http.StatusOK || out["behavior"] != "allow" || out["message"] != nil {
		t.Fatalf("the POST: %d %v", status, out)
	}
	if e := entryState(t, l, r.Seq); e.State != session.PermissionAllowed {
		t.Fatalf("the entry %+v", e)
	}

	f.ToolStarted(session.ToolStep{Ref: "c-2", Name: "Bash", Input: []byte(`{"command":"rm -rf build"}`)})
	h2 := postPermission(e, id, tok, permissionBody("c-2", "Bash", `{"command":"rm -rf build"}`))
	r2 := openEntry(t, l, "c-2")
	op.send(permissionAnswer(r2, "a2", proto.PermissionDeny, "Run make clean instead."))
	op.until("the deny's word", func(c chatFrame) bool { return c.names("a2") && c.m["t"] == proto.CtlAnswerOK }, nil)
	if status, out := h2.answered(t); status != http.StatusOK || out["behavior"] != "deny" || out["message"] != "Run make clean instead." {
		t.Fatalf("the denied POST: %d %v", status, out)
	}

	f.ToolStarted(session.ToolStep{Ref: "c-3", Name: "Bash", Input: []byte(`{"command":"ls"}`)})
	h3 := postPermission(e, id, tok, permissionBody("c-3", "Bash", `{"command":"ls"}`))
	openEntry(t, l, "c-3")
	f.ToolEnded("c-3", session.ToolResult{Status: session.ToolOK})
	if status, out := h3.answered(t); status != http.StatusOK || out["behavior"] != "deny" || out["message"] != session.WithdrawnElsewhereWords {
		t.Fatalf("a withdrawn POST: %d %v", status, out)
	}

	f.ToolStarted(session.ToolStep{Ref: "c-4", Name: "Bash", Input: []byte(`{"command":"pwd"}`)})
	h4 := postPermission(e, id, tok, permissionBody("c-4", "Bash", `{"command":"pwd"}`))
	r4 := openEntry(t, l, "c-4")
	h4.cancel()
	<-h4.done
	for deadline := time.Now().Add(30 * time.Second); ; {
		if e := entryState(t, l, r4.Seq); e.State == session.PermissionWithdrawn && e.Why == session.RequestLostWords {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a POST gone: %+v", entryState(t, l, r4.Seq))
		}
		time.Sleep(time.Millisecond)
	}
	op.send(permissionAnswer(r4, "a4", proto.PermissionAllowOnce, ""))
	if c := op.until("the late answer's word", func(c chatFrame) bool { return c.names("a4") }, nil); c.m["code"] != proto.ErrCodeQuestionAnswered {
		t.Fatalf("an answer after the POST went: %v", c.m)
	}
}

// quietSink takes a test subscription's frames and drops them.
type quietSink struct{}

func (quietSink) WriteFrame([]byte) error { return nil }
func (quietSink) Close(error)             {}

// clientControls is every control message's type the proto package names
// (its Ctl constants), read from its source: a message a later change adds
// joins the walk below by itself.
func clientControls(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("../proto")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("../proto", ent.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if !strings.HasPrefix(n.Name, "Ctl") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						if !slices.Contains(out, v) {
							out = append(out, v)
						}
					}
				}
			}
		}
	}
	if !slices.Contains(out, proto.CtlAnswer) || !slices.Contains(out, proto.CtlPermissionAnswer) {
		t.Fatalf("the controls read %v", out)
	}
	return out
}

// I10: only the operator answers a request put to Conductor. A view link, a
// control link, a decision link (its lane's choice and its text answer), a
// relayed viewer (the publisher's answer, ErrNotAnswerer), AnswerAs through
// the router's apply, an extension's Host.Submit, the agent's token on the
// attention route and every route its doors open, and the decide grant's
// routes, each with a choice and with a line, and every control message the
// protocol names sent by every connection that is not the operator's: none
// reaches a verdict. The router is told of the question, its answer
// recorded refused; the inbox's item offers no action. The operator's
// answer, last, does.
func TestPermissionOnlyTheOperator(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	id, l, f, tok, h, r := chatAsking(t, e)
	stillOpen := func(what string) {
		t.Helper()
		if !h.held() {
			_, out := h.answered(t)
			t.Fatalf("%s: the held POST was answered %v", what, out)
		}
		if e := entryState(t, l, r.Seq); e.State != session.PermissionOpen {
			t.Fatalf("%s: the request's entry %+v", what, e)
		}
		if att := l.Info().Attention; att.ID != r.Question || att.Kind != session.KindPermission {
			t.Fatalf("%s: the attention %+v", what, att)
		}
	}

	// Every connection that is not the operator's own, each control
	// message the protocol names, with a choice's keys and a line.
	_, viewTok, err := e.srv.links.Create(id, session.RoleView, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewTok
	subs := map[string]session.AttachOptions{
		"a view link":         {Role: session.RoleView, LinkID: "link-view", Name: "viewer"},
		"a control link":      {Role: session.RoleControl, LinkID: "link-control", Name: "controller"},
		"a decision link":     {Role: session.RoleControl, LinkID: "link-decide", Decisions: true, Name: "desk"},
		"a control, no owner": {Role: session.RoleControl, Name: "someone"},
	}
	for who, o := range subs {
		sub, err := l.AttachWith(o, quietSink{})
		if err != nil {
			t.Fatal(err)
		}
		for _, ctl := range clientControls(t) {
			if ctl == proto.CtlInterrupt {
				// It stops the turn, which withdraws every request (never
				// allows: TestPermissionNeverSilentlyAllowed).
				continue
			}
			for _, fields := range []map[string]any{
				{"input": "y\r", "choice": proto.PermissionAllowOnce},
				{"text": "yes", "choice": proto.PermissionAllowOnce},
			} {
				m := map[string]any{"t": ctl, "reqId": "x1", "question": r.Question, "call": r.Call, "sha256": r.InputSha256, "revision": r.Question}
				for k, v := range fields {
					m[k] = v
				}
				payload, _ := json.Marshal(m)
				e.srv.handleLocalControl(sub, l, &heldTyping{}, payload)
			}
		}
		stillOpen(who)
		if err := l.AnswerPermission(context.Background(), sub, proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "x", Question: r.Question, Call: r.Call, Sha256: r.InputSha256, Choice: proto.PermissionAllowOnce}); err == nil {
			t.Fatalf("%s answered the request", who)
		}
		l.Detach(sub)
	}

	// A view link's socket: its permission_answer is not_answerer.
	view := dialViewer(t, e, id, viewTok)
	helloChat(view, ^uint64(0))
	if w := view.until("the welcome", controlOf(proto.CtlWelcome), nil); w.m["permissionAnswer"] != nil {
		t.Fatalf("a view link's welcome offers the answer: %v", w.m)
	}
	view.until("ready", controlOf(proto.CtlReady), nil)
	view.send(permissionAnswer(r, "v1", proto.PermissionAllowOnce, ""))
	if c := view.until("the view link's word", func(c chatFrame) bool { return c.names("v1") }, nil); c.m["code"] != proto.ErrCodeNotAnswerer || c.m["message"] != session.NotAnswererWords {
		t.Fatalf("a view link's permission_answer: %v", c.m)
	}
	stillOpen("a view link's socket")

	// A decision link's lane: its choice and its text answer.
	rd := e.srv.newInboxRead(context.Background())
	it, ok := rd.questionItem(l.Info())
	if !ok || len(it.Actions) != 0 || !strings.Contains(it.Blocking.Words, PermissionInChatWords) || !strings.Contains(it.Brief, PermissionInChatWords) {
		t.Fatalf("the inbox's item %+v (%v)", it, ok)
	}
	// The lane draws it with no action and the not-answerer's words: who
	// answers is the person who runs this server, never the link.
	items, _, _, err := e.srv.laneItems(context.Background(), laneScope{session: id})
	if err != nil {
		t.Fatal(err)
	}
	li := slices.IndexFunc(items, func(x *inboxItem) bool { return x.Ref == questionRef(id, r.Question) })
	if li < 0 {
		t.Fatalf("the lane has no item for the request: %d items", len(items))
	}
	if lit := items[li]; len(lit.Actions) != 0 || !strings.Contains(lit.Brief, session.NotAnswererWords) || !strings.Contains(lit.Blocking.Words, session.NotAnswererWords) ||
		strings.Contains(lit.Brief+lit.Blocking.Words, PermissionInChatWords) || lit.Answerer != PermissionAnswererWords {
		t.Fatalf("the lane's item %+v", lit)
	}
	ref := brakeman.Ref{Kind: "question", Session: id, Question: r.Question}
	for _, m := range []proto.Decide{{ReqID: "d1", Revision: r.Question, Input: "y\r"}, {ReqID: "d2", Revision: r.Question, Text: "yes"}} {
		if frame, _ := e.srv.laneAnswer(context.Background(), "link-decide", id, laneScope{}, ref, it, m, nil, nil); frame == nil {
			t.Fatal("the lane's answer was taken")
		}
	}
	stillOpen("a decision link's lane")

	// The router: told of the question, kind permission; its answer, a
	// choice or a line, recorded refused.
	d := decisionOf(l.Info())
	if d.Kind != session.KindPermission || d.ID != r.Question || d.Message != "Run a command?" {
		t.Fatalf("the router is told %+v", d)
	}
	e.srv.router.apply(l, d, ext.Answer{Input: "y\r", By: ext.Principal{Name: "router"}})
	e.srv.router.apply(l, d, ext.Answer{Text: "yes", By: ext.Principal{Name: "router"}})
	refusedLines := 0
	for _, a := range l.Activity() {
		if a.Message == RoutedPermissionRefusedWords {
			refusedLines++
		}
	}
	if refusedLines != 2 {
		t.Fatalf("the router's answers recorded refused %d times", refusedLines)
	}
	stillOpen("the router's answers")

	// An extension's Host.Submit, a choice and a line.
	host := &extHost{s: e.srv, name: "provider", ctx: context.Background()}
	for _, req := range []ext.SubmitRequest{{Session: id, Question: r.Question, Input: "y\r", By: ext.Principal{Subject: "provider-1", Name: "provider"}}, {Session: id, Question: r.Question, Text: "yes", By: ext.Principal{Subject: "provider-1", Name: "provider"}}} {
		if _, err := host.Submit(context.Background(), req); err == nil || !strings.Contains(err.Error(), session.PermissionCardWords) {
			t.Fatalf("Host.Submit %+v: %v", req, err)
		}
	}
	stillOpen("Host.Submit")

	// The agent's token: its attention reports, and every route its doors
	// open, with a body naming the request.
	for _, b := range []map[string]any{{"state": "clear"}, {"state": "needs_input", "ask": true, "message": "allow it?"}, {"state": "done"}} {
		resp, out := e.do("POST", "/api/sessions/"+id+"/attention", tok, b)
		if resp.StatusCode >= 500 {
			t.Fatalf("the attention report %v: %d %v", b, resp.StatusCode, out)
		}
	}
	for pattern := range compareDoors {
		method, path, _ := strings.Cut(pattern, " ")
		if method != http.MethodPost || strings.HasSuffix(path, "/permission") {
			continue
		}
		path = strings.ReplaceAll(path, "{id}", id)
		path = strings.ReplaceAll(path, "{did}", r.Question)
		path = strings.ReplaceAll(path, "{key}", "T-1")
		e.do(method, path, tok, map[string]any{"question": r.Question, "input": "y\r", "text": "yes", "choice": proto.PermissionAllowOnce})
	}
	stillOpen("the agent's token")

	// The decide grant's routes take a durable decision's id alone.
	for _, path := range []string{"/api/decisions/" + r.Question + "/respond", "/api/decisions/" + id + "/respond"} {
		resp, out := e.do("POST", path, adminToken, map[string]any{"choice": 0, "expectedRev": 1, "text": "yes"})
		if resp.StatusCode < 400 {
			t.Fatalf("the decide route %s: %d %v", path, resp.StatusCode, out)
		}
	}
	stillOpen("the decide grant's routes")
	if n := sessiontest.Count(f.Calls(), "reply"); n != 0 {
		t.Fatalf("the driver was replied %d times", n)
	}

	// The operator's own answer, last, reaches it.
	op := dialViewer(t, e, id, adminToken)
	helloChat(op, ^uint64(0))
	op.until("ready", controlOf(proto.CtlReady), nil)
	op.send(permissionAnswer(r, "o1", proto.PermissionAllowOnce, ""))
	op.until("the operator's word", func(c chatFrame) bool { return c.names("o1") && c.m["t"] == proto.CtlAnswerOK }, nil)
	if status, out := h.answered(t); status != http.StatusOK || out["behavior"] != "allow" {
		t.Fatalf("the operator's allow: %d %v", status, out)
	}
}

// A control link's interrupt ends the turn, which withdraws the open
// request with the turn-stopped words and answers its POST the withdrawal:
// never an allow.
func TestAControlLinksInterruptWithdrawsNeverAllows(t *testing.T) {
	e := newTestEnv(t, nil)
	_, l, f, _, h, r := chatAsking(t, e)
	link, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, LinkID: "link-control", Name: "controller"}, quietSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Detach(link)
	e.srv.handleLocalControl(link, l, &heldTyping{}, proto.MustControl(proto.Interrupt{T: proto.CtlInterrupt, ReqID: "i1"})[1:])
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatalf("waited for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitFor("the interrupt to reach the driver", func() bool { return sessiontest.Count(f.Calls(), "interrupt") == 1 })
	f.TurnEnded("interrupted", session.Usage{})
	if status, out := h.answered(t); status != http.StatusOK || out["behavior"] != "deny" || out["message"] != session.WithdrawnElsewhereWords {
		t.Fatalf("the held POST: %d %v", status, out)
	}
	if got := entryState(t, l, r.Seq); got.State != session.PermissionWithdrawn || got.Why != session.TurnStoppedWords {
		t.Fatalf("the request after the interrupt %+v", got)
	}
}
