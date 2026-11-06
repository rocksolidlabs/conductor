package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The decisions' routes (Program item DR1, docs/plans/decisions.md,
// PR-4). Each test waits on the record (decisionWhen, the route's read
// of the ledger) for the state it needs, never on a clock.

// decisionFixture is a server with the ready cat whose managed run of
// managedCrew has its build attempt running: the run, the attempt's
// session and its token. Every run is stopped when the test ends.
type decisionFixture struct {
	*testEnv
	run, sid, tok string
}

// newDecisionFixture makes a decisionFixture; mutate shapes the config,
// log is the server's (none for nil), opts its options.
func newDecisionFixture(t *testing.T, mutate func(*config.Config), log *slog.Logger, opts ...Option) *decisionFixture {
	t.Helper()
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := newTestEnvAgents(t, mutate, log, []catalog.Agent{readyCat}, opts...)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	srv := e.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	return &decisionFixture{testEnv: e, run: run.ID, sid: sid, tok: e.agentToken(sid)}
}

// approvalBody is an approval request, Approve or Reject, under the id.
func approvalBody(id string) map[string]any {
	return map[string]any{"requestId": id, "type": "approval", "question": "Ship build " + id + "?",
		"options": []any{map[string]any{"label": "Approve"}, map[string]any{"label": "Reject"}}, "authorizes": "the deploy"}
}

// ask sends the attempt's request and fails t unless it answers the
// status and code.
func (f *decisionFixture) ask(t *testing.T, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	return f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions", body, status, code)
}

// respond answers decision did as the token with the choice at the rev.
func (e *testEnv) respond(t *testing.T, token, did, responseID string, choice, rev, status int, code string) map[string]any {
	t.Helper()
	return e.expectAs(t, token, http.MethodPost, "/api/decisions/"+did+"/respond", map[string]any{"responseId": responseID, "choice": choice, "expectedRev": rev}, status, code)
}

// decisionWhen polls the record of decision did until ok holds of it, and
// answers it; a hang fails t after testWait.
func (e *testEnv) decisionWhen(t *testing.T, did, what string, ok func(d map[string]any) bool) map[string]any {
	t.Helper()
	var d map[string]any
	waitUntil(t, did+" "+what, func() bool {
		d = entityOf(t, e.expect(t, http.MethodGet, "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
		return ok(d)
	})
	return d
}

// inState is a decisionWhen test of the state.
func inState(state string) func(map[string]any) bool {
	return func(d map[string]any) bool { return d["state"] == state }
}

// An attempt's request is answered at once with the record, open, and a
// repeat of its requestId with the same; its task is held and its result
// refused while it waits; the routes are the attempt's token's alone.
func TestADecisionRequestAnswersAtOnce(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	d := entityOf(t, f.ask(t, approvalBody("r1"), http.StatusCreated, ""), "decision")
	asker, _ := d["asker"].(map[string]any)
	if d["id"] != "dr-1" || d["state"] != "open" || d["origin"] != "agent" || d["run"] != f.run || asker["task"] != "build" || d["rev"] != 1.0 {
		t.Fatalf("the decision: %v", d)
	}
	if again := entityOf(t, f.ask(t, approvalBody("r1"), http.StatusOK, ""), "decision"); again["id"] != "dr-1" {
		t.Fatalf("the repeat: %v", again)
	}
	other := approvalBody("r1")
	other["question"] = "Something else?"
	f.ask(t, other, http.StatusConflict, crew.CodeDecisionConflict)
	f.ask(t, map[string]any{"requestId": "r2", "type": "approval", "question": "No options?"}, http.StatusBadRequest, crew.CodeInvalid)
	f.ask(t, map[string]any{"requestId": "r3", "type": "clarification", "question": "q", "nope": 1}, http.StatusBadRequest, crew.CodeInvalid)
	// The task is held, its result refused while the decision waits.
	taskVia(t, f.testEnv, f.run, "build", "held by dr-1", func(task map[string]any) bool {
		holds, _ := task["holds"].([]any)
		return len(holds) == 1 && holds[0].(map[string]any)["id"] == "dr-1"
	})
	if ds, _ := f.expect(t, http.MethodGet, "/api/runs/"+f.run, nil, http.StatusOK, "")["decisions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["id"] != "dr-1" {
		t.Fatalf("the run's decisions: %v", ds)
	}
	if ds, _ := f.expect(t, http.MethodGet, "/api/runs/"+f.run+"/tasks/build", nil, http.StatusOK, "")["decisions"].([]any); len(ds) != 1 {
		t.Fatalf("the task's decisions: %v", ds)
	}
	out := f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/task/result", map[string]any{"summary": "built"}, http.StatusConflict, crew.CodeDecisionOpen)
	if !strings.Contains(apiMessage(out), "dr-1 is not answered yet") {
		t.Fatalf("the refusal: %v", out)
	}
	// The attempt's list and read; another decision's id is not its own.
	list := f.expectAs(t, f.tok, http.MethodGet, "/api/sessions/"+f.sid+"/decisions", nil, http.StatusOK, "")
	if ds, _ := list["decisions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["question"] != "Ship build r1?" || list["next"] != nil {
		t.Fatalf("the list: %v", list)
	}
	f.expectAs(t, f.tok, http.MethodGet, "/api/sessions/"+f.sid+"/decisions/dr-1", nil, http.StatusOK, "")
	f.expectAs(t, f.tok, http.MethodGet, "/api/sessions/"+f.sid+"/decisions/dr-9", nil, http.StatusNotFound, crew.CodeNotFound)
	// The workbench token is not the attempt's.
	f.expectAs(t, adminToken, http.MethodPost, "/api/sessions/"+f.sid+"/decisions", approvalBody("r4"), http.StatusUnauthorized, "unauthorized")
	f.expectAs(t, adminToken, http.MethodGet, "/api/sessions/"+f.sid+"/decisions", nil, http.StatusUnauthorized, "unauthorized")
	// Withdrawn, the task waits no more and its result is taken again.
	w := entityOf(t, f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, ""), "decision")
	if w["state"] != ledger.DecisionCancelled {
		t.Fatalf("withdrawn: %v", w)
	}
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusConflict, crew.CodeDecisionClosed)
}

// The operator answers: the workbench token and an identity provider's
// principal with the admin grant; a share link's controller, the agent's
// token, a principal with the host grant alone and no token are refused
// every admin route.
func TestOnlyTheOperatorResponds(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{
		"cloud-admin-token": cloudAdmin("nate"),
		"cloud-host-token":  {Subject: "hosty", Name: "Hosty", Grants: []ext.Grant{ext.GrantHost}},
	}}
	f := newDecisionFixture(t, nil, nil, withIdentity(id))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	_, lo := f.do(http.MethodPost, "/api/sessions/"+f.sid+"/links", adminToken, map[string]any{"role": "control"})
	link, _ := lo["token"].(string)
	if link == "" {
		t.Fatalf("no link: %v", lo)
	}
	for name, tok := range map[string]string{"a share link's controller": link, "the agent's token": f.tok, "a host principal": "cloud-host-token", "no token": ""} {
		for _, route := range []string{"respond", "cancel", "comment", "route"} {
			if status, data, _ := f.callAs(t, tok, http.MethodPost, "/api/decisions/dr-1/"+route, []byte(`{}`)); status != http.StatusUnauthorized {
				t.Errorf("%s on %s: %d %.200s", name, route, status, data)
			}
		}
		for _, path := range []string{"/api/decisions", "/api/decisions/dr-1", "/api/dispatch/tickets/CND-1/decisions"} {
			if status, data, _ := f.callAs(t, tok, http.MethodGet, path, nil); status != http.StatusUnauthorized {
				t.Errorf("%s reading %s: %d %.200s", name, path, status, data)
			}
		}
	}
	rc := receiptOf(t, f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, ""))
	if rc["status"] != "accepted" || rc["actor"] != "admin" {
		t.Fatalf("the workbench token's receipt: %v", rc)
	}
	rc = receiptOf(t, f.respond(t, "cloud-admin-token", "dr-2", "a1", 0, 1, http.StatusCreated, ""))
	if rc["status"] != "accepted" || rc["actor"] != ext.SubjectActor+"nate" {
		t.Fatalf("the principal's receipt: %v", rc)
	}
	for _, did := range []string{"dr-1", "dr-2"} {
		d := f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
		if d["answer"] != "Approve" {
			t.Fatalf("%s's answer: %v", did, d["answer"])
		}
	}
}

// A second response, another person's, is refused with the first's words:
// who answered; its refusal is kept as its receipt.
func TestASecondResponseNamesTheFirst(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/respond", map[string]any{"responseId": "a1", "choice": 0, "expectedRev": 1, "byName": "Nate"}, http.StatusCreated, "")
	out := f.respond(t, adminToken, "dr-1", "a2", 1, 1, http.StatusConflict, crew.CodeDecisionAnswered)
	if !strings.Contains(apiMessage(out), "Nate") {
		t.Fatalf("the second's words: %v", out)
	}
	if rc := receiptOf(t, out); rc["status"] != "refused" || rc["code"] != crew.CodeDecisionAnswered {
		t.Fatalf("the second's receipt: %v", rc)
	}
}

// A response sent again with its id and body answers the same receipt,
// repeat true; with another body, response_conflict.
func TestARetriedResponseIsTheSameReceipt(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	first := receiptOf(t, f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, ""))
	again := receiptOf(t, f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusOK, ""))
	if again["repeat"] != true || again["k"] != first["k"] || again["at"] != first["at"] || again["status"] != "accepted" {
		t.Fatalf("the retry %v, the first %v", again, first)
	}
	f.respond(t, adminToken, "dr-1", "a1", 1, 1, http.StatusConflict, crew.CodeResponseConflict)
}

// A response at a rev the decision moved past is refused stale_decision
// with what changed; an operation the same, unless it was sent before.
func TestAStaleRevExplainsWhatChanged(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	more := map[string]any{"opId": "m1", "kind": "more_info", "text": "Which tests ran?", "expectedRev": 1}
	rc := receiptOf(t, f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", more, http.StatusOK, ""))
	if rc["rev"] != 2.0 {
		t.Fatalf("more_info's receipt: %v", rc)
	}
	out := f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusConflict, crew.CodeStaleDecision)
	if msg := apiMessage(out); !strings.Contains(msg, "dr-1") || len(msg) < 20 {
		t.Fatalf("the stale response's words: %q", msg)
	}
	// The same operation again answers its receipt; a new one at the old rev is stale.
	if again := receiptOf(t, f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", more, http.StatusOK, "")); again["repeat"] != true || again["seq"] != rc["seq"] {
		t.Fatalf("the operation again: %v", again)
	}
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c2", "kind": "comment", "text": "x", "expectedRev": 1}, http.StatusConflict, crew.CodeStaleDecision)
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/route", map[string]any{"opId": "o1", "to": map[string]any{"kind": "operator"}, "expectedRev": 2}, http.StatusOK, "")
	f.respond(t, adminToken, "dr-1", "a2", 0, 2, http.StatusCreated, "")
}

// decisionEventOf decodes a "decision <json>" event of the stream.
func decisionEventOf(t *testing.T, ev string) decisionEvent {
	t.Helper()
	var d decisionEvent
	if err := json.Unmarshal([]byte(strings.TrimPrefix(ev, "decision ")), &d); err != nil {
		t.Fatalf("%q: %v", ev, err)
	}
	return d
}

// A decision event follows each commit that changed the decision: open,
// answered, then applied, each with the run, the task and the rev.
func TestTheDecisionEventFollowsEachCommit(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	events := f.sse(t)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	var states []string
	end := time.After(testWait)
	for !slices.Contains(states, ledger.DecisionApplied) {
		select {
		case ev := <-events:
			if !strings.HasPrefix(ev, "decision ") {
				continue
			}
			d := decisionEventOf(t, ev)
			if d.ID != "dr-1" || d.Run != f.run || d.Task != "build" || d.Rev < 1 {
				t.Fatalf("the event: %+v", d)
			}
			if len(states) == 0 || states[len(states)-1] != d.State {
				states = append(states, d.State)
			}
		case <-end:
			t.Fatalf("the events so far: %v", states)
		}
	}
	if !slices.Equal(states, []string{ledger.DecisionOpen, ledger.DecisionAnswered, ledger.DecisionApplied}) {
		t.Fatalf("the states in order: %v", states)
	}
}

// The largest decision event fits its bound: the longest id, a run of the
// longest crew id, the longest task id, the longest state; one past it is
// dropped, never cut.
func TestDecisionEventIsBounded(t *testing.T) {
	ev := decisionEvent{ID: ledger.DecisionID(1<<63 - 1), Run: strings.Repeat("c", 64) + "-0123abcd", Task: strings.Repeat("t", 40), State: ledger.DecisionSuperseded, Rev: 1<<31 - 1}
	b, err := json.Marshal(ev)
	if err != nil || len(b) > maxDecisionEvent {
		t.Fatalf("the largest event is %d bytes: %v", len(b), err)
	}
	h := newEventHub()
	ch := h.subscribe()
	h.decision(decisionEvent{ID: ev.ID, Run: strings.Repeat("c", maxDecisionEvent), State: ev.State})
	h.decision(ev)
	got := string(<-ch)
	if got != "event: decision\ndata: "+string(b)+"\n\n" {
		t.Fatalf("the event sent: %q", got)
	}
	select {
	case more := <-ch:
		t.Fatalf("an event past the bound was sent: %q", more)
	default:
	}
}

// A provider's sink is handed one notification after each commit: the
// decision event, its state, the question, the run page's task and, once
// answered, the responder; queued, so the hub never waits on it.
func TestASinkGetsTheDecisionNotice(t *testing.T) {
	sink := &recSink{name: "push"}
	p := ext.Providers{Notifications: []ext.NotificationSink{sink}, All: []ext.Entry{{Kind: ext.KindNotifications, Name: "push", Value: sink}}}
	f := newDecisionFixture(t, nil, nil, WithExtensions(local.Fill(p)))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	decisions := func(ns []ext.Notification) []ext.Notification {
		return slices.DeleteFunc(slices.Clone(ns), func(n ext.Notification) bool { return n.Event != config.EventDecision })
	}
	got := decisions(sink.waitFor(t, "the opening", func(ns []ext.Notification) bool { return len(decisions(ns)) > 0 }))
	n := got[0]
	if n.Type != "decision" || n.State != ledger.DecisionOpen || n.Message != "Ship build r1?" || n.Session.ID != f.sid || n.Session.Run != f.run || n.Session.Task != "build" ||
		n.URL != "/runs/"+f.run+"?view=tasks&task=build&decision=dr-1" || n.By != "" {
		t.Fatalf("the opening's notification: %+v", n)
	}
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	got = decisions(sink.waitFor(t, "the answer", func(ns []ext.Notification) bool {
		return slices.ContainsFunc(decisions(ns), func(n ext.Notification) bool { return n.State == ledger.DecisionAnswered })
	}))
	if i := slices.IndexFunc(got, func(n ext.Notification) bool { return n.State == ledger.DecisionAnswered }); got[i].By != "admin" {
		t.Fatalf("the answer's notification: %+v", got[i])
	}
}

// A webhook that lists decision is POSTed the decision's change, as an
// entry of type decision under X-Conductor-Event: decision.
func TestTheWebhookTakesDecision(t *testing.T) {
	rcv := newReceiver(t, nil)
	f := newDecisionFixture(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: []string{config.EventDecision}, AllowPrivate: true}}
	}, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	d := rcv.next(t)
	if ev := d.header.Get("X-Conductor-Event"); ev != config.EventDecision {
		t.Fatalf("X-Conductor-Event %q", ev)
	}
	p := d.webhookPayload(t)
	if p.SessionID != f.sid || p.Entry["type"] != "decision" || p.Entry["message"] != "Ship build r1?" || !strings.HasSuffix(fmt.Sprint(p.Entry["url"]), "&decision=dr-1") {
		t.Fatalf("the payload: %+v", p)
	}
}

// An identity's response to a decision whose run the server does not
// hold (stopped) is refused not_held; the workbench token's is recorded,
// to be applied when the run is continued.
func TestAnIdentityAnswerToARunNotHeldIsRefused(t *testing.T) {
	id := &tokenIdentity{known: map[string]ext.Principal{"cloud-admin-token": cloudAdmin("nate")}}
	f := newDecisionFixture(t, nil, nil, withIdentity(id))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.expect(t, http.MethodPost, "/api/runs/"+f.run+"/stop", nil, http.StatusOK, "")
	out := f.respond(t, "cloud-admin-token", "dr-1", "a1", 0, 1, http.StatusConflict, crew.CodeNotHeld)
	if !strings.Contains(apiMessage(out), "continue it, or answer with the workbench token") {
		t.Fatalf("the refusal: %v", out)
	}
	if rc := receiptOf(t, f.respond(t, adminToken, "dr-1", "a2", 0, 1, http.StatusCreated, "")); rc["status"] != "accepted" {
		t.Fatalf("the workbench token's receipt: %v", rc)
	}
	f.decisionWhen(t, "dr-1", "answered", inState(ledger.DecisionAnswered))
}

// recheckIdentity admits each token it knows at its first ask (the
// response's authentication) and answers its second, the recheck after
// the response committed, as the token's mode says: admit, revoke, or
// late (an answer only once its context ends, past ext.IdentityWait).
type recheckIdentity struct {
	mode map[string]string // token → admit, revoke or late

	mu    sync.Mutex
	calls map[string]int
}

func (*recheckIdentity) Name() string { return "cloud" }

func (p *recheckIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	p.mu.Lock()
	p.calls[tok]++
	n := p.calls[tok]
	p.mu.Unlock()
	m, ok := p.mode[tok]
	switch {
	case !ok:
		return ext.Principal{}, false, nil
	case n == 1, m == "admit":
		return cloudAdmin(m), true, nil
	case m == "late":
		<-ctx.Done()
		return ext.Principal{}, false, ctx.Err()
	}
	return ext.Principal{}, false, nil
}

// An identity's accepted response is applied only once the provider
// admits its token again after the commit: admitted, applied; revoked
// before the recheck, or no answer within ext.IdentityWait, the decision
// is superseded by a fresh one with its hold and nothing is applied. The
// token is in no ledger row and no log line.
func TestAnIdentityIsRecheckedBeforeItsAnswerApplies(t *testing.T) {
	tokens := map[string]string{"admit": "tok-admit-5f0c2b8e9d1a", "revoke": "tok-revoke-77a1c3e4b2f0", "late": "tok-late-0b9e8d7c6a5f"}
	p := &recheckIdentity{mode: map[string]string{}, calls: map[string]int{}}
	for m, tok := range tokens {
		p.mode[tok] = m
	}
	logs := &logBuffer{}
	dataDir := filepath.Join(t.TempDir(), "data")
	f := newDecisionFixture(t, func(c *config.Config) {
		c.DataDir = dataDir
		seedDispatch(t, dataDir)
	}, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), withIdentity(p))
	n := 0
	for _, m := range []string{"admit", "revoke", "late"} {
		f.ask(t, approvalBody("r-"+m), http.StatusCreated, "")
		n++
		did := ledger.DecisionID(int64(n))
		if rc := receiptOf(t, f.respond(t, tokens[m], did, "a-"+m, 0, 1, http.StatusCreated, "")); rc["status"] != "accepted" || rc["actor"] != ext.SubjectActor+m {
			t.Fatalf("%s: the receipt %v", m, rc)
		}
		if m == "admit" {
			if d := f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied)); d["applied"] == nil {
				t.Fatalf("admitted, not applied: %v", d)
			}
			continue
		}
		d := f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
		n++
		next := ledger.DecisionID(int64(n))
		if d["applied"] != nil || d["supersededBy"] != next {
			t.Fatalf("%s: applied or not replaced: %v", m, d)
		}
		fresh := f.decisionWhen(t, next, "open", inState(ledger.DecisionOpen))
		if fresh["question"] != nil || !bytes.Contains(decisionJSON(t, fresh["request"]), []byte("Ship build r-"+m+"?")) {
			t.Fatalf("%s: the fresh decision %v", m, fresh)
		}
	}
	// The held task keeps its hold on each fresh decision.
	taskVia(t, f.testEnv, f.run, "build", "held by the fresh decisions", func(task map[string]any) bool {
		holds, _ := task["holds"].([]any)
		return len(holds) == 2
	})
	stopEveryRun(f.srv)
	files, err := filepath.Glob(filepath.Join(dataDir, "runs", "managed.sqlite*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the ledger's files: %v %v", files, err)
	}
	for _, tok := range tokens {
		if strings.Contains(logs.String(), tok) {
			t.Errorf("a log line holds the token %s", tok)
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(tok)) {
				t.Errorf("%s holds the token %s", filepath.Base(file), tok)
			}
		}
	}
}

// decisionJSON is v as JSON.
func decisionJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// encodedReply is v as writeJSON writes it.
func encodedReply(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The fullest decision, every part at its bound, with twenty refs each at
// theirs, is read whole within maxDecisionReply, each ref within
// maxDecisionRef.
func TestTheFullestDecisionFitsItsBound(t *testing.T) {
	d := fullestDecision(t)
	text := func(n int) string { return strings.Repeat("a", n) }
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	// Each ref at its bound, of characters writeJSON escapes to six bytes
	// (an ampersand in its path, angle brackets in its states and
	// revisions, its URL of ampersands), held to its bound by fitRef; and
	// one of plain letters, whose URL fits as it is.
	var refs []decisionRef
	for i := range ledger.MaxDecisionEvidence {
		fill := "&"
		if i == 0 {
			fill = "p"
		}
		ref, err := brakeman.ParseRef(fmt.Sprintf("file:%016x//%s", i, strings.Repeat(fill, ledger.MaxEvidenceRef-len("file:0123456789abcdef//"))))
		if err != nil {
			t.Fatal(err)
		}
		esc := func(n int) string { return strings.Repeat("<", n) }
		if i == 0 {
			esc = text
		}
		st := brakeman.RefState{URL: "/sessions?q=" + strings.Repeat(fill, brakeman.MaxURL-len("/sessions?q=")), State: esc(brakeman.MaxRefState), Rev: esc(brakeman.MaxRevision), SHA256: strings.Repeat("f", 64), At: at}
		r := fitRef(decisionRef{Ref: ref, Seen: brakeman.RefState{State: st.State, Rev: st.Rev, SHA256: st.SHA256}, Now: st, Change: refUnread})
		if n := len(encodedReply(t, r)); n > maxDecisionRef {
			t.Fatalf("a ref of %d bytes, over %d", n, maxDecisionRef)
		}
		if (r.Now.URL == "") != (i > 0) {
			t.Fatalf("ref %d's URL: %q", i, r.Now.URL)
		}
		refs = append(refs, r)
	}
	if n := len(encodedReply(t, map[string]any{"decision": d, "refs": refs})); n > maxDecisionReply {
		t.Fatalf("the fullest decision is %d bytes, over %d", n, maxDecisionReply)
	}
}

// The operator's lists (GET /api/decisions, a ticket's decisions) give
// each record without its history and its responses (listedOf): the
// fullest such record fits maxDecisionListed, so a page of them with its
// next fits maxDecisionListPage; and a record answered lists its answer,
// not its receipts (#637, review round 1).
func TestTheOperatorsDecisionListFitsItsBound(t *testing.T) {
	d := listedOf(fullestDecision(t))
	if d.History != nil || d.Responses != nil || d.Answer == "" {
		t.Fatalf("listed: %d history rows, %d responses, answer %q", len(d.History), len(d.Responses), d.Answer)
	}
	if n := len(encodedReply(t, d)); n > maxDecisionListed {
		t.Fatalf("the fullest listed record is %d bytes, over %d", n, maxDecisionListed)
	}
	page := make([]crew.Decision, maxDecisionsListed)
	for i := range page {
		page[i] = d
	}
	if n := len(encodedReply(t, map[string]any{"decisions": page, "next": d.ID})); n > maxDecisionListPage {
		t.Fatalf("a page of %d bytes, over %d", n, maxDecisionListPage)
	}
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	listed := f.expect(t, http.MethodGet, "/api/decisions?run="+f.run, nil, http.StatusOK, "")["decisions"].([]any)[0].(map[string]any)
	if listed["responses"] != nil || listed["history"] != nil || listed["answer"] != "Approve" {
		t.Fatalf("the listed record: %v", listed)
	}
}

// fullestDecision is the fullest record: every part at its bound.
func fullestDecision(t *testing.T) crew.Decision {
	t.Helper()
	text := func(n int) string { return strings.Repeat("a", n) }
	raw := func(n int) json.RawMessage { return json.RawMessage(`"` + text(n-2) + `"`) }
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	choice := 5
	d := crew.Decision{ID: ledger.DecisionID(1<<63 - 1), Run: strings.Repeat("c", 64) + "-0123abcd", OpenedIn: strings.Repeat("c", 64) + "-0123abce",
		Origin: ledger.OriginAgent, Type: ledger.DecisionApproval, Gate: text(40), Asker: &crew.DecisionAsker{Task: text(40), Attempt: 1 << 30, Live: true},
		Member: text(64), Ticket: text(16), Plan: text(16), PlanDigest: text(64), RequestID: text(32),
		Request: raw(ledger.MaxDecisionRequest), Evidence: raw(ledger.MaxDecisionBaseline), Policy: raw(ledger.MaxDecisionPolicy),
		State: ledger.DecisionSuperseded, Rev: 1 << 30, Why: text(ledger.MaxDecisionWhy), SupersededBy: ledger.DecisionID(1<<63 - 1),
		Answer: text(ledger.MaxOptionLabel*4) + ": " + text(ext.MaxAnswerText), CreatedAt: at, AnsweredAt: &at, ClosedAt: &at}
	for range ledger.MaxDecisionResponses {
		rc := crew.ResponseReceipt{Decision: d.ID, K: 1 << 30, Status: "refused", Code: crew.CodeStaleDecision, Actor: text(ledger.MaxDecisionActor), Choice: &choice, At: at, Rev: 1 << 30}
		fixed := len(decisionJSON(t, rc))
		rc.Words = text(ledger.MaxDecisionReceipt - fixed - len(`,"words":""`))
		if n := len(decisionJSON(t, rc)); n > ledger.MaxDecisionReceipt {
			t.Fatalf("a receipt of %d bytes", n)
		}
		d.Responses = append(d.Responses, rc)
	}
	for i := range ledger.MaxDecisionHistory {
		d.History = append(d.History, crew.DecisionEntry{Seq: 1<<30 + i, At: at, Kind: ledger.HistoryDeliveryUncertain, By: text(ledger.MaxDecisionActor), Detail: raw(ledger.MaxHistoryDetail)})
	}
	app := crew.Application{At: at, By: "yardmaster", Run: d.Run, Task: text(40), Attempt: 1 << 30, Response: 1 << 30, Validated: true, How: crew.HowCancelled}
	app.Words = text(ledger.MaxDecisionApplied - len(decisionJSON(t, app)) - len(`"words":"",`) - 64)
	d.Applied = &app
	if n := len(decisionJSON(t, app)); n > ledger.MaxDecisionApplied {
		t.Fatalf("an application of %d bytes", n)
	}
	return d
}

// A page of a task's decisions at its worst fits maxDecisionSummaryPage:
// each summary's question, answer and why of control characters, which
// encode to six bytes each, cut at a character's end and marked cut; and
// a list of more than a page names its next, the next page the rest.
func TestAFullDecisionListFitsItsBound(t *testing.T) {
	ctrl := func(n int) string { return strings.Repeat("\x01", n) }
	q, _ := json.Marshal(map[string]string{"question": ctrl(ledger.MaxDecisionQuestion)})
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	row := ledger.DecisionRow{N: 1<<63 - 1, Origin: ledger.OriginAgent, Type: ledger.DecisionClarification, TaskID: strings.Repeat("t", 40), Gate: strings.Repeat("g", 40),
		RequestID: strings.Repeat("r", 32), Request: string(q), State: ledger.DecisionSuperseded, Rev: 1 << 30, Why: ctrl(ledger.MaxDecisionWhy),
		SupersededBy: ledger.DecisionID(1<<63 - 1), CreatedAt: at, AnsweredAt: at, ClosedAt: at}
	var page []decisionSummary
	for range maxDecisionsListed {
		sum := summaryOf(row, ctrl(4096+ledger.MaxOptionLabel))
		if !sum.Cut || len(sum.Question) == 0 || len(encodedReply(t, sum.Question)) > maxDecisionSummaryText+3 {
			t.Fatalf("the summary is not cut: %d bytes of question", len(sum.Question))
		}
		if n := len(encodedReply(t, sum)); n > maxDecisionSummary {
			t.Fatalf("a summary of %d bytes, over %d", n, maxDecisionSummary)
		}
		page = append(page, sum)
	}
	if n := len(encodedReply(t, map[string]any{"decisions": page, "next": row.ID()})); n > maxDecisionSummaryPage {
		t.Fatalf("a page of %d bytes, over %d", n, maxDecisionSummaryPage)
	}
	if got := cutEncoded("ab<é ", 4); got != "ab" {
		t.Fatalf("cutEncoded: %q", got)
	}
	// Two decisions, a page of one: the newest with next, then the other without.
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	for _, c := range []struct{ token, path string }{{f.tok, "/api/sessions/" + f.sid + "/decisions"}, {adminToken, "/api/decisions"}} {
		first := f.expectAs(t, c.token, http.MethodGet, c.path+"?limit=1", nil, http.StatusOK, "")
		if ds, _ := first["decisions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["id"] != "dr-2" || first["next"] != "dr-2" {
			t.Fatalf("%s's first page: %v", c.path, first)
		}
		second := f.expectAs(t, c.token, http.MethodGet, c.path+"?limit=1&after=dr-2", nil, http.StatusOK, "")
		if ds, _ := second["decisions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["id"] != "dr-1" || second["next"] != nil {
			t.Fatalf("%s's second page: %v", c.path, second)
		}
		f.expectAs(t, c.token, http.MethodGet, c.path+"?limit=101", nil, http.StatusBadRequest, "invalid_request")
	}
	if ds, _ := f.expect(t, http.MethodGet, "/api/decisions?run="+f.run+"&state=open", nil, http.StatusOK, "")["decisions"].([]any); len(ds) != 2 || ds[0].(map[string]any)["history"] != nil {
		t.Fatalf("the admin's list: %v", ds)
	}
}

// A request's evidence refs are resolved into its baseline at the
// request, and against the records again when it is read whole: a ref
// that names nothing, or is not a ref, refuses the request.
func TestADecisionResolvesItsEvidence(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "run")["id"].(string)
	sid := runningAttempt(t, l.testEnv, run, "build", 1)["sessionId"].(string)
	tok := l.agentToken(sid)
	ask := func(body map[string]any, status int, code string) map[string]any {
		return l.expectAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/decisions", body, status, code)
	}
	body := approvalBody("r1")
	body["evidence"] = []string{"ticket:CND-1", "task:" + run + "/build", "run:" + run}
	ask(body, http.StatusCreated, "")
	out := l.expect(t, http.MethodGet, "/api/decisions/dr-1", nil, http.StatusOK, "")
	refs, _ := out["refs"].([]any)
	if len(refs) != 3 {
		t.Fatalf("the refs: %v", out["refs"])
	}
	ticket := refs[0].(map[string]any)
	seen, now := ticket["seen"].(map[string]any), ticket["now"].(map[string]any)
	if ticket["ref"] != "ticket:CND-1" || seen["rev"] == nil || seen["rev"] != now["rev"] || ticket["change"] != nil || now["url"] == nil {
		t.Fatalf("the ticket's ref: %v", ticket)
	}
	// The run's revision, and its task's, moved with the decision's own
	// opening: each ref is unchanged while its state is (#637, review
	// round 1; the task's, #689, review round 1).
	if task := refs[1].(map[string]any); task["ref"] != "task:"+run+"/build" || task["seen"].(map[string]any)["state"] == nil || task["change"] != nil {
		t.Fatalf("the task's ref: %v", task)
	}
	if r := refs[2].(map[string]any); r["ref"] != "run:"+run || r["change"] != nil {
		t.Fatalf("the run's ref: %v", r)
	}
	for _, ev := range []string{"task:" + run + "/nope", "nope:x", "ticket:CND-99"} {
		b := approvalBody("r-" + ev)
		b["requestId"] = "r9"
		b["evidence"] = []string{ev}
		ask(b, http.StatusBadRequest, crew.CodeInvalid)
	}
}

// A ticket ref whose ticket is edited after the request and before the
// record's first read reads changed, with the ticket's revision at the
// request as what it was.
func TestEvidenceChangedBeforeTheFirstReadIsShown(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "run")["id"].(string)
	sid := runningAttempt(t, l.testEnv, run, "build", 1)["sessionId"].(string)
	before := entityOf(t, l.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")["rev"]
	body := approvalBody("r1")
	body["evidence"] = []string{"ticket:CND-1"}
	l.expectAs(t, l.agentToken(sid), http.MethodPost, "/api/sessions/"+sid+"/decisions", body, http.StatusCreated, "")
	l.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": "Fix the login loop, again"}}, http.StatusOK, "")
	ref := l.expect(t, http.MethodGet, "/api/decisions/dr-1", nil, http.StatusOK, "")["refs"].([]any)[0].(map[string]any)
	seen, now := ref["seen"].(map[string]any), ref["now"].(map[string]any)
	if ref["change"] != refChanged || seen["rev"] != fmt.Sprint(before) || now["rev"] == seen["rev"] {
		t.Fatalf("the ref after the edit (the ticket was at rev %v): %v", before, ref)
	}
}

// The guide lists the decision operations: the attempt's four with their
// tools, the operator's four with the ledger's receipt and the decision's
// revision.
func TestTheGuideListsTheDecisionOperations(t *testing.T) {
	ops := map[string]Operation{}
	for _, op := range Operations() {
		ops[op.Name] = op
	}
	for _, name := range []string{"decision_request", "decision_list", "decision_read", "decision_withdraw"} {
		if op := ops[name]; op.Scope != "task" || op.MCP != name || op.Description == "" || !strings.HasPrefix(op.Path, "/api/sessions/{id}/decisions") {
			t.Errorf("%s: %+v", name, op)
		}
	}
	for _, name := range []string{"decision_respond", "decision_cancel", "decision_comment", "decision_route"} {
		if op := ops[name]; op.Scope != "admin" || op.MCP != "" || op.Receipt != "ledger" || op.Revision != "decision" || op.Method != http.MethodPost ||
			op.Path != "/api/decisions/{did}/"+strings.TrimPrefix(name, "decision_") {
			t.Errorf("%s: %+v", name, op)
		}
	}
	if !strings.Contains(guideRules[2], "decision_request") {
		t.Fatalf("the rules: %v", guideRules)
	}
	e := newTestEnv(t, nil)
	_, _, out := guideOf(t, e, adminToken, "/api/guide?section=operations")
	if got := opNames(t, out); !slices.Contains(got, "decision_respond") || !slices.Contains(got, "decision_request") {
		t.Fatalf("the admin's operations: %v", got)
	}
}

// A launch from a ticket's approved revision opens its gates: the gated
// task does not start, the ticket lists its decision, and the operator's
// approval releases it.
func TestATicketListsItsDecisions(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	plan := l.approved(t, map[string]any{"decisions": []any{map[string]any{"id": "ship", "question": "Ship it?", "kind": "approval", "before": []string{"build"}}}})
	run := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)
	task := taskVia(t, l.testEnv, run, "build", "held", func(task map[string]any) bool {
		holds, _ := task["holds"].([]any)
		return len(holds) == 1
	})
	if task["attempt"] != nil {
		t.Fatalf("a gated task started: %v", task)
	}
	list := l.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-1/decisions", nil, http.StatusOK, "")["decisions"].([]any)
	if len(list) != 1 {
		t.Fatalf("the ticket's decisions: %v", list)
	}
	d := list[0].(map[string]any)
	if d["origin"] != ledger.OriginPlan || d["gate"] != "ship" || d["state"] != ledger.DecisionOpen || d["ticket"] != l.ticketID || d["history"] != nil {
		t.Fatalf("the gate: %v", d)
	}
	did := d["id"].(string)
	if ds := l.expect(t, http.MethodGet, "/api/decisions?ticket=CND-1&state=open", nil, http.StatusOK, "")["decisions"].([]any); len(ds) != 1 {
		t.Fatalf("by the ticket's key: %v", ds)
	}
	l.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-99/decisions", nil, http.StatusNotFound, "not_found")
	l.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
	if app := l.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))["applied"].(map[string]any); app["how"] != crew.HowReleased {
		t.Fatalf("the application: %v", app)
	}
	runningAttempt(t, l.testEnv, run, "build", 1)
}

// A withdrawal names only the attempt's own decisions: one of another
// run's attempt with the same task and number, closed, is not found, never
// that run's decision's state and words (#637, review round 1).
func TestAWithdrawalOfAnotherRunsDecisionIsNotFound(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusConflict, crew.CodeDecisionClosed)
	other, release, err := f.srv.runs.LaunchNamed(t.Context(), managedCrew(f.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	sid := runningAttempt(t, f.testEnv, other.ID, "build", 1)["sessionId"].(string)
	out := f.expectAs(t, f.agentToken(sid), http.MethodPost, "/api/sessions/"+sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusNotFound, crew.CodeNotFound)
	if msg := apiMessage(out); strings.Contains(msg, "withdrawn") || strings.Contains(msg, ledger.DecisionCancelled) {
		t.Fatalf("another run's decision's words: %q", msg)
	}
}

// A request sent again (a reply lost) answers its decision before its
// evidence is looked at again, whatever its refs resolve to now, and one
// with another body is decision_conflict, also without a look (#637,
// review round 1).
func TestARequestSentAgainIsNotResolvedAgain(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	body := approvalBody("r1")
	body["evidence"] = []string{"session:" + f.sid, "run:" + f.run}
	first := entityOf(t, f.ask(t, body, http.StatusCreated, ""), "decision")
	looked := func(ref string) { t.Errorf("%s was resolved again", ref) }
	baselineSeam.Store(&looked)
	t.Cleanup(func() { baselineSeam.Store(nil) })
	if again := entityOf(t, f.ask(t, body, http.StatusOK, ""), "decision"); again["id"] != first["id"] || string(decisionJSON(t, again["evidence"])) != string(decisionJSON(t, first["evidence"])) {
		t.Fatalf("the repeat %v, the first %v", again, first)
	}
	other := approvalBody("r1")
	other["question"] = "Something else?"
	other["evidence"] = body["evidence"]
	f.ask(t, other, http.StatusConflict, crew.CodeDecisionConflict)
}

// A request's evidence refs are resolved under the asker's own grant,
// what its agent token reads of each kind of record elsewhere: its run,
// its task's attempts and checks, its run's commits, its session, and its
// run's ticket, the comments, plan revision and launch its package
// carries are taken; every other ref, of each kind, is refused in the
// words of a ref that names nothing, the same whether or not its record
// exists, and no decision is opened with it (#637, review round 1).
func TestAnAskersRefsAreResolvedUnderItsGrant(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	ctx := t.Context()
	ds := l.srv.dispatch
	done := doneOf(t)
	inPkg := done(ds.AddComment(ctx, adminOp("Ann"), "CND-1", "Use the new flow.")).ID
	plan := l.approved(t, map[string]any{"comments": []string{inPkg}})
	run := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)
	sid := runningAttempt(t, l.testEnv, run, "build", 1)["sessionId"].(string)
	tok := l.agentToken(sid)
	row, ok, err := l.srv.ledger.Run(ctx, run)
	if err != nil || !ok {
		t.Fatalf("the run's row: %v %v", ok, err)
	}
	var pkg crew.Package
	if err := json.Unmarshal([]byte(row.Package), &pkg); err != nil || len(pkg.Comments) != 1 || pkg.Launch.ID == "" {
		t.Fatalf("the package: %+v %v", pkg, err)
	}
	// What lies outside: another run's attempt and session, another
	// ticket, a comment and a plan revision made after the launch.
	other, release, err := l.srv.runs.LaunchNamed(ctx, managedCrew(l.repo), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	otherSid := runningAttempt(t, l.testEnv, other.ID, "build", 1)["sessionId"].(string)
	otherTicket := done(ds.CreateTicket(ctx, adminOp("Ann"), dispatch.NewTicket{Project: "CND", Title: "Elsewhere"})).Key
	later := done(ds.AddComment(ctx, adminOp("Ann"), "CND-1", "And another thing.")).ID
	l.approved(t, nil)
	base := l.head
	ask := func(id string, refs []string, status int, code string) map[string]any {
		b := approvalBody(id)
		b["evidence"] = refs
		return l.expectAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/decisions", b, status, code)
	}
	// Within the grant: one request names them all.
	ask("in", []string{"run:" + run, "task:" + run + "/build", "attempt:" + run + "/build/1", "commit:" + run + "/" + base, "session:" + sid,
		"ticket:CND-1", fmt.Sprintf("comment:CND-1/%s", inPkg), fmt.Sprintf("plan:CND-1/%d", pkg.Plan.N), "launch:" + pkg.Launch.ID}, http.StatusCreated, "")
	// Outside it, each kind: one whose record exists (where the fixture
	// has one) and one whose record does not, answered alike.
	none := "0123456789abcdef"
	for _, c := range []struct{ kind, exists, absent string }{
		{"run", "run:" + other.ID, "run:routes-0000dead"},
		{"task", "task:" + other.ID + "/build", "task:" + run + "/nope"},
		{"attempt", "attempt:" + other.ID + "/build/1", "attempt:" + run + "/docs/1"},
		{"check", "check:" + other.ID + "/build/1/test/1", "check:" + run + "/nope/1/test/1"},
		{"command", "command:" + run + "/c1", "command:" + run + "/c2"},
		{"event", "event:" + run + "/1", "event:" + run + "/999999"},
		{"message", "message:" + other.ID + "/1", "message:" + run + "/999999"},
		{"session", "session:" + otherSid, "session:" + none},
		{"question", "question:" + otherSid + "/" + none, "question:" + none + "/" + none},
		{"ticket", "ticket:" + otherTicket, "ticket:CND-99"},
		{"comment", "comment:CND-1/" + later, "comment:CND-1/" + none},
		{"op", "op:admin/" + opID(), "op:admin/nope"},
		{"commit", "commit:" + other.ID + "/" + base, "commit:" + run + "/" + strings.Repeat("0", 40)},
		{"file", "file:" + sid + "/" + l.repo + "/README", "file:" + none + "//nope"},
		{"brief", "brief:br-1/0", "brief:br-9/0"},
		{"plan", fmt.Sprintf("plan:CND-1/%d", pkg.Plan.N+1), "plan:CND-1/99"},
		{"launch", "launch:" + none, "launch:fedcba9876543210"},
		{"candidate", "candidate:" + other.ID + "/build/1", "candidate:" + run + "/build/9"},
	} {
		words := map[string]string{}
		for _, ref := range []string{c.exists, c.absent} {
			out := ask("out-"+c.kind, []string{ref}, http.StatusBadRequest, crew.CodeInvalid)
			words[ref] = strings.ReplaceAll(apiMessage(out), cutBytes(ref, 80), "<ref>")
		}
		if words[c.exists] != words[c.absent] || !strings.Contains(words[c.exists], "<ref> resolves to nothing") {
			t.Errorf("%s: %q, and for one that names nothing %q", c.kind, words[c.exists], words[c.absent])
		}
	}
	if ds := l.expectAs(t, tok, http.MethodGet, "/api/sessions/"+sid+"/decisions", nil, http.StatusOK, "")["decisions"].([]any); len(ds) != 1 {
		t.Fatalf("decisions opened: %v", ds)
	}
}
