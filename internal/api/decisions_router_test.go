package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The decision router told of durable decisions (Program item DR1,
// docs/plans/decisions.md, PR-5): a decision's record is routed once, as
// it opens, never the session question that mirrors it, and told Resolved
// at its final state; a router's answer is refused under local/1 and
// recorded. Each test waits on the router's own steps (recordSeam,
// routerSeam) and on the record, never on the clock.

// recordLog keeps the router's steps for the durable decisions.
type recordLog struct {
	mu    sync.Mutex
	steps []recordedStep
}

type recordedStep struct {
	step, decision string
	rt             ext.Route
	err            error
}

// watchRecords installs a recordLog for the test.
func watchRecords(t *testing.T) *recordLog {
	t.Helper()
	l := &recordLog{}
	f := func(step, decision string, rt ext.Route, err error) {
		l.mu.Lock()
		l.steps = append(l.steps, recordedStep{step, decision, rt, err})
		l.mu.Unlock()
	}
	recordSeam.Store(&f)
	t.Cleanup(func() { recordSeam.Store(nil) })
	return l
}

// wait waits for the step for the decision and answers it.
func (l *recordLog) wait(t *testing.T, step, decision string) recordedStep {
	t.Helper()
	var got recordedStep
	until(t, step+" "+decision, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, s := range l.steps {
			if s.step == step && s.decision == decision {
				got = s
				return true
			}
		}
		return false
	})
	return got
}

// routerDecisionFixture is a decisionFixture whose server's decision
// router is f, named switchman, beside the identity provider id when
// there is one.
func routerDecisionFixture(t *testing.T, f *fakeRouter, id ext.IdentityProvider) *decisionFixture {
	t.Helper()
	p := ext.Providers{Router: f, All: []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: f}}}
	if id != nil {
		p.Identity = id
		p.All = append(p.All, ext.Entry{Kind: ext.KindIdentity, Name: "cloud", Value: id})
	}
	return newDecisionFixture(t, nil, nil, WithExtensions(local.Fill(p)))
}

// mirrorOf waits for the attempt's session to show decision did's
// question, and answers it as the session shows it.
func (f *decisionFixture) mirrorOf(t *testing.T, did string) session.Attention {
	t.Helper()
	l := f.local(f.sid)
	var a session.Attention
	until(t, "the session shows "+did, func() bool {
		a = l.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Decision == did
	})
	return a
}

// drained waits until the router has told every resolution it queued.
func drained(t *testing.T, r *decisionRouter) {
	t.Helper()
	until(t, "the resolutions told", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.told) == 0 && !r.telling
	})
}

// The router is told each decision once, as its record opens: the
// record's id as ID and Request, kind decision, the asking session with
// its run and task, the question and the options' labels, each picked by
// its place. The session question that mirrors it is never routed,
// however often the router reads the session; a later change of the
// record (a comment) routes nothing again. Resolved is told once, when
// the record reaches a final state (withdrawn, applied), and a change of
// a final record tells nothing more.
func TestTheRouterIsToldEachDecisionOnce(t *testing.T) {
	seams := watchRouter(t)
	records := watchRecords(t)
	fr := &fakeRouter{}
	f := routerDecisionFixture(t, fr, nil)

	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-1")
	m1 := f.mirrorOf(t, "dr-1")
	seams.settle(t, f.testEnv, f.sid)
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-2")
	m2 := f.mirrorOf(t, "dr-2")
	seams.settle(t, f.testEnv, f.sid)
	// A comment changes the record, and routes nothing again.
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "Looking", "expectedRev": 1}, http.StatusOK, "")
	seams.settle(t, f.testEnv, f.sid)

	if got := fr.routedIDs(); !slices.Equal(got, []string{"dr-1", "dr-2"}) {
		t.Fatalf("routed %q, want dr-1 and dr-2 once each (the mirrors %s and %s never)", got, m1.ID, m2.ID)
	}
	fr.mu.Lock()
	d := fr.routed[0]
	fr.mu.Unlock()
	want := ext.Decision{ID: "dr-1", Request: "dr-1", Kind: "decision", Message: "Ship build r1?",
		Session: ext.SessionRef{ID: f.sid, Run: f.run, Task: "build", Attempt: 1},
		Options: []ext.DecisionOption{{Label: "Approve", Input: "1"}, {Label: "Reject", Input: "2"}}}
	got := d
	got.Since = want.Since
	if b1, b2 := mustJSON(got), mustJSON(want); b1 != b2 || d.Since.IsZero() {
		t.Fatalf("the router was told %s, want %s (since %v)", b1, b2, d.Since)
	}

	// Withdrawn by its attempt: resolved once.
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	until(t, "dr-1 resolved", func() bool { _, n := fr.resolution("dr-1"); return n > 0 })
	// Applied: resolved once.
	f.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-2", "applied", inState(ledger.DecisionApplied))
	until(t, "dr-2 resolved", func() bool { _, n := fr.resolution("dr-2"); return n > 0 })
	// A comment on a final record changes it, and tells nothing more.
	rec := f.decisionWhen(t, "dr-1", "cancelled", inState(ledger.DecisionCancelled))
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c2", "kind": "comment", "text": "Noted", "expectedRev": rec["rev"]}, http.StatusOK, "")
	drained(t, f.srv.router)
	for _, did := range []string{"dr-1", "dr-2"} {
		r, n := fr.resolution(did)
		if n != 1 || r.Request != did || r.Session.ID != f.sid || r.Session.Run != f.run || r.Session.Task != "build" {
			t.Errorf("%s resolved %d times: %+v", did, n, r)
		}
	}
	if got := fr.resolvedIDs(); slices.Contains(got, m1.ID) || slices.Contains(got, m2.ID) {
		t.Errorf("a mirror was resolved as a session question: %q", got)
	}
	f.srv.router.mu.Lock()
	left := len(f.srv.router.records)
	f.srv.router.mu.Unlock()
	if left != 0 {
		t.Errorf("the router keeps %d records past their resolution", left)
	}
}

// A router's answer is not the operator's under local/1, though its
// subject names an admin principal of the identity provider: offered under
// the actor user:<subject> with the router's authority and no grant, it is
// refused (not_approver), its refusal kept as a response's receipt with a
// responded history row, and never applied. The router's destinations are
// the record's routed history row. The operator then answers at the same
// rev, and that answer is applied.
func TestARoutersAnswerIsRefusedUnderLocalPolicy(t *testing.T) {
	records := watchRecords(t)
	ada := ext.Principal{Subject: "nate", Name: "Nate"}
	fr := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		if d.Request == "" {
			return ext.Route{}, nil
		}
		return ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}, {Kind: "role", Name: "release"}}, Rule: "owns the deploy", Answer: &ext.Answer{Input: "1", By: ada}}, nil
	}}
	id := &tokenIdentity{known: map[string]ext.Principal{"cloud-admin-token": cloudAdmin("nate")}}
	f := routerDecisionFixture(t, fr, id)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	step := records.wait(t, "routed", "dr-1")
	var de *crew.DecisionError
	if !errors.As(step.err, &de) || de.Code != crew.CodeNotApprover || !strings.Contains(de.Words, "refused under local/1") {
		t.Fatalf("the routed answer: %v", step.err)
	}
	d := f.decisionWhen(t, "dr-1", "the router's refusal recorded", func(d map[string]any) bool {
		rs, _ := d["responses"].([]any)
		return len(rs) == 1
	})
	if d["state"] != ledger.DecisionOpen || d["rev"] != 1.0 {
		t.Fatalf("the decision after the router's answer: %v", d)
	}
	rc := d["responses"].([]any)[0].(map[string]any)
	if rc["status"] != "refused" || rc["code"] != crew.CodeNotApprover || rc["actor"] != ext.SubjectActor+"nate" || rc["byName"] != "Nate" || !strings.Contains(rc["words"].(string), "refused under local/1") {
		t.Fatalf("the router's receipt: %v", rc)
	}
	var routed, responded []map[string]any
	for _, h := range d["history"].([]any) {
		h := h.(map[string]any)
		switch {
		case h["kind"] == ledger.HistoryRouted && h["by"] == "router:switchman":
			routed = append(routed, h)
		case h["kind"] == ledger.HistoryResponded:
			responded = append(responded, h)
		}
	}
	if len(routed) != 1 || len(responded) != 1 {
		t.Fatalf("the history: %v", d["history"])
	}
	detail := routed[0]["detail"].(map[string]any)
	to, _ := json.Marshal(detail["to"])
	if string(to) != `[{"kind":"person","name":"Ada"},{"kind":"role","name":"release"}]` || detail["rule"] != "owns the deploy" {
		t.Fatalf("the routed row: %v", routed[0])
	}
	if rd := responded[0]["detail"].(map[string]any); rd["status"] != ledger.ResponseRefused {
		t.Fatalf("the responded row: %v", responded[0])
	}
	// The operator answers at the rev the router's refusal left.
	f.respond(t, adminToken, "dr-1", "a1", 1, 1, http.StatusCreated, "")
	if d := f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied)); d["answer"] != "Reject" {
		t.Fatalf("the operator's answer: %v", d["answer"])
	}
}

// A viewer is shown the decision a question mirrors (the attention
// message's decision), and an answer naming that question, a choice's
// keys or a line, is refused over the server's WebSocket with the code
// decision and words naming the decision, nothing written: only the
// decision's own answer answers it.
func TestAnswerToADecisionIsRefused(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	q := f.mirrorOf(t, "dr-1")
	c := dialViewer(t, f.testEnv, f.sid, adminToken)
	c.hello(0, 0)
	if w := c.expectControl(proto.CtlWelcome); w["answer"] != true {
		t.Fatalf("welcome %v", w)
	}
	if m := c.expectControl(proto.CtlAttention); m["id"] != q.ID || m["decision"] != "dr-1" {
		t.Fatalf("the attention shown: %v", m)
	}
	c.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: q.ID, Input: "1"}))
	c.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a2", Question: q.ID, Text: "Approve"}))
	for _, req := range []string{"a1", "a2"} {
		m := c.verdict(req)
		if m["t"] != proto.CtlError || m["code"] != proto.ErrCodeDecision || !strings.Contains(m["message"].(string), "dr-1 is answered as a decision") {
			t.Fatalf("the answer %s: %v", req, m)
		}
	}
	if a := f.local(f.sid).Info().Attention; a.ID != q.ID || a.Decision != "dr-1" || a.State != session.AttentionNeedsInput {
		t.Fatalf("the mirror after the refusals: %+v", a)
	}
	if d := f.decisionWhen(t, "dr-1", "open", inState(ledger.DecisionOpen)); d["responses"] != nil {
		t.Fatalf("a refused answer reached the decision: %v", d["responses"])
	}
}
