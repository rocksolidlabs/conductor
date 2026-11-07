package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// count is how many times the step was taken for the decision.
func (l *recordLog) count(step, decision string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.steps {
		if s.step == step && s.decision == decision {
			n++
		}
	}
	return n
}

// readSince waits until the router has ended a read of the decision's
// record past the before'th ("done"): whatever it held, it judged the
// record as a commit after the mark left it.
func (l *recordLog) readSince(t *testing.T, decision string, before int) {
	t.Helper()
	until(t, "the router read "+decision+" again", func() bool { return l.count("done", decision) > before })
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
	// A comment changes the record, and routes nothing again: judged once
	// the router has read the record the comment's commit left.
	reads := records.count("done", "dr-1")
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "Looking", "expectedRev": 1}, http.StatusOK, "")
	records.readSince(t, "dr-1", reads)
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

	// Withdrawn by its attempt: resolved once (on a goroutine of its own).
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	until(t, "dr-1 resolved", func() bool { _, n := fr.resolution("dr-1"); return n > 0 })
	// Applied: resolved once.
	f.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-2", "applied", inState(ledger.DecisionApplied))
	until(t, "dr-2 resolved", func() bool { _, n := fr.resolution("dr-2"); return n > 0 })
	// A comment on a final record changes it, and tells nothing more.
	rec := f.decisionWhen(t, "dr-1", "cancelled", inState(ledger.DecisionCancelled))
	reads = records.count("done", "dr-1")
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c2", "kind": "comment", "text": "Noted", "expectedRev": rec["rev"]}, http.StatusOK, "")
	records.readSince(t, "dr-1", reads)
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
	if got := fr.routedIDs(); !slices.Equal(got, []string{"dr-1", "dr-2"}) {
		t.Fatalf("routed %q after the final record's comment", got)
	}
	f.srv.router.mu.Lock()
	left := len(f.srv.router.opened)
	f.srv.router.mu.Unlock()
	if left != 0 {
		t.Errorf("the router keeps %d decisions past their end", left)
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
		switch d.Request {
		case "":
			return ext.Route{}, nil
		case "dr-2":
			// A line to an approval: refused and kept all the same.
			return ext.Route{Answer: &ext.Answer{Text: "ship it", By: ada}}, nil
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

	// A line to an approval, which takes a choice, is refused as the
	// router's and kept, never refused for its shape with nothing kept.
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	step = records.wait(t, "routed", "dr-2")
	if !errors.As(step.err, &de) || de.Code != crew.CodeNotApprover {
		t.Fatalf("the routed line to an approval: %v", step.err)
	}
	d = f.decisionWhen(t, "dr-2", "the router's refusal recorded", func(d map[string]any) bool {
		rs, _ := d["responses"].([]any)
		return len(rs) == 1
	})
	if rc := d["responses"].([]any)[0].(map[string]any); rc["status"] != "refused" || rc["code"] != crew.CodeNotApprover || rc["text"] != "ship it" || d["state"] != ledger.DecisionOpen {
		t.Fatalf("the router's line: %v", d)
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

// historyOf is the kinds and authors of a decision's history, in order.
func historyOf(d map[string]any) []string {
	var out []string
	for _, h := range d["history"].([]any) {
		h := h.(map[string]any)
		by, _ := h["by"].(string)
		out = append(out, fmt.Sprint(h["kind"], " ", by))
	}
	return out
}

// The free build registers no router, and decisions are as they were
// before the router was told of them: the same sequence as
// TestTheRouterIsToldEachDecisionOnce (two decisions, a comment, a
// withdrawal, an applied answer) writes no router's row and no response
// but the operator's, and the router keeps nothing once they are final.
func TestNoRouterLeavesDecisionsAsTheyWere(t *testing.T) {
	records := watchRecords(t)
	f := newDecisionFixture(t, nil, nil)
	if n := f.srv.exts.router.Name(); n != local.Name {
		t.Fatalf("the router is %q, not the free default", n)
	}
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	f.mirrorOf(t, "dr-2")
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "Looking", "expectedRev": 1}, http.StatusOK, "")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	f.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	one := f.decisionWhen(t, "dr-1", "cancelled", inState(ledger.DecisionCancelled))
	two := f.decisionWhen(t, "dr-2", "applied", inState(ledger.DecisionApplied))
	for did, want := range map[string]string{"dr-1": "opened lead|routed local/1|commented admin|cancelled lead", "dr-2": "opened lead|routed local/1|responded admin|applied yardmaster"} {
		d := one
		if did == "dr-2" {
			d = two
		}
		if got := strings.Join(historyOf(d), "|"); !strings.HasPrefix(got, want) || strings.Contains(got, "router:") {
			t.Errorf("%s's history %q, want %q (and no router's row)", did, got, want)
		}
	}
	if rs, _ := two["responses"].([]any); len(rs) != 1 || rs[0].(map[string]any)["actor"] != "admin" {
		t.Fatalf("dr-2's responses: %v", two["responses"])
	}
	for _, did := range []string{"dr-1", "dr-2"} {
		records.readSince(t, did, 0)
	}
	until(t, "the router keeps nothing", func() bool {
		f.srv.router.mu.Lock()
		defer f.srv.router.mu.Unlock()
		return len(f.srv.router.opened) == 0 && f.srv.router.untold == 0 && len(f.srv.router.told) == 0
	})
}

// lowRetry lowers the wait before an owed pass for the test.
func lowRetry(t *testing.T) {
	was, wasMost := recordRetryFirst, recordRetryMost
	recordRetryFirst, recordRetryMost = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { recordRetryFirst, recordRetryMost = was, wasMost })
}

// A read of a decision's record that fails owes a pass, which tells it:
// the read of dr-1's opening fails once, the router is told nothing then,
// and the pass after the wait lists dr-1 open and tells it, once.
func TestARecordReadThatFailsIsToldByTheNextPass(t *testing.T) {
	records := watchRecords(t)
	lowRetry(t)
	var failed atomic.Int32
	fail := func(decision string) error {
		if decision == "dr-1" && failed.CompareAndSwap(0, 1) {
			return errors.New("the disk is busy")
		}
		return nil
	}
	recordReadFault.Store(&fail)
	t.Cleanup(func() { recordReadFault.Store(nil) })
	fr := &fakeRouter{}
	f := routerDecisionFixture(t, fr, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-1")
	if failed.Load() != 1 {
		t.Fatal("no read of dr-1 failed")
	}
	if records.count("listed", "dr-1") == 0 {
		t.Fatal("dr-1 was told without a pass listing it")
	}
	if got := fr.routedIDs(); !slices.Equal(got, []string{"dr-1"}) {
		t.Fatalf("routed %q", got)
	}
}

// A tell that fails is told again by the next pass: the router's Route
// fails for dr-1 once (an error), and the pass after the wait tells it
// again, which it takes; the record keeps the second route's row alone.
// A failed Route is "nothing told", whatever reached the provider.
func TestATellThatFailsIsToldAgain(t *testing.T) {
	records := watchRecords(t)
	lowRetry(t)
	var calls atomic.Int32
	fr := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		if calls.Add(1) == 1 {
			return ext.Route{}, errors.New("the switchboard is down")
		}
		return ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}}, nil
	}}
	f := routerDecisionFixture(t, fr, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	until(t, "dr-1 told twice", func() bool { return count(fr.routedIDs(), "dr-1") >= 2 })
	until(t, "dr-1's route taken", func() bool {
		records.mu.Lock()
		defer records.mu.Unlock()
		for _, st := range records.steps {
			if st.step == "routed" && st.decision == "dr-1" && st.err == nil && len(st.rt.To) == 1 {
				return true
			}
		}
		return false
	})
	if records.count("listed", "dr-1") == 0 {
		t.Fatal("dr-1 was told again without a pass")
	}
	d := f.decisionWhen(t, "dr-1", "the route recorded", func(d map[string]any) bool {
		return slices.Contains(historyOf(d), "routed router:switchman")
	})
	if n := count(historyOf(d), "routed router:switchman"); n != 1 {
		t.Fatalf("dr-1 has %d router's rows: %q", n, historyOf(d))
	}
}

// A provider that blocks for ever, ignoring its context, holds one slot
// and nothing waits for it: dr-1's end is told to a Resolved that never
// returns, and dr-2 opens, is told, ends and is told its end all the
// same; the requests that open and close them answer at once.
func TestAProviderThatBlocksHoldsOneSlot(t *testing.T) {
	records := watchRecords(t)
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	fr := &blockingRouter{fakeRouter: fakeRouter{}, block: "dr-1", never: never}
	p := ext.Providers{Router: fr, All: []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: fr}}}
	f := newDecisionFixture(t, nil, nil, WithExtensions(local.Fill(p)))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-1")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	until(t, "dr-1's end held in Resolved", func() bool { return fr.blocked.Load() })
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-2")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-2/withdraw", map[string]any{}, http.StatusOK, "")
	until(t, "dr-2 resolved past the held one", func() bool { _, n := fr.resolution("dr-2"); return n == 1 })
	if _, n := fr.resolution("dr-1"); n != 0 {
		t.Fatalf("dr-1's held Resolved returned")
	}
}

// blockingRouter is a fakeRouter whose Resolved of block never returns
// until never is closed, whatever its context.
type blockingRouter struct {
	fakeRouter
	block   string
	never   chan struct{}
	blocked atomic.Bool
}

func (b *blockingRouter) Resolved(ctx context.Context, r ext.Resolution) {
	if r.Request == b.block {
		b.blocked.Store(true)
		<-b.never
		return
	}
	b.fakeRouter.Resolved(ctx, r)
}

// A provider whose calls never return, whatever their context, takes the
// bound's worth of call slots and no more, Resolved counted as Route is:
// dr-1 is told, and its end hangs in Resolved (one slot); dr-2's Route
// hangs, each tell's caller moves on at its deadline (the router's wait)
// and the next pass tells it again in a slot of its own, until every slot
// is held. From then on a tell is refused before anything goes into the
// provider ("skipped", no goroutine begun for it) and the provider is
// called no more, while a decision opens, shows in its session and is
// withdrawn, the requests answering at once. Once the provider returns,
// its slots come back and dr-2 is told.
func TestAHungProviderHoldsAtMostTheBound(t *testing.T) {
	records := watchRecords(t)
	lowRetry(t)
	never := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(never) }) }
	t.Cleanup(release)
	h := &hungRouter{free: "dr-1", never: never}
	p := ext.Providers{Router: h, All: []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: h}}}
	f := newDecisionFixture(t, nil, nil, WithExtensions(local.Fill(p)))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	until(t, "dr-1's route taken", func() bool { return records.routedOK("dr-1") })
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	until(t, "dr-1's end held in Resolved", func() bool { return h.resolving.Load() == 1 })

	// Set only now: no tell is under way to read it (dr-1's took its route).
	f.srv.router.wait = 20 * time.Millisecond
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	until(t, "every call slot held", func() bool { return h.inside.Load() >= extInFlight })
	called := len(h.routedIDs())
	listed := records.count("listed", "dr-2")
	until(t, "two passes more over dr-2", func() bool { return records.count("listed", "dr-2") >= listed+2 })
	if most := h.most.Load(); most != extInFlight {
		t.Fatalf("%d calls in the provider at once, want the bound, %d", most, extInFlight)
	}
	if n := len(h.routedIDs()); n != called {
		t.Fatalf("the provider was called %d times more with every slot held", n-called)
	}
	if n := h.resolving.Load(); n != 1 {
		t.Fatalf("%d Resolved under way, want dr-1's", n)
	}
	if n := records.count("skipped", "dr-2"); n == 0 {
		t.Fatal("no tell of dr-2 refused with every slot held")
	}
	if n := records.countErr("routed", "dr-2", errUnrouted); n < extInFlight-1 {
		t.Fatalf("%d tells of dr-2 moved on at their deadline, want %d", n, extInFlight-1)
	}

	// Meanwhile a decision opens, shows in its session and is withdrawn.
	f.ask(t, approvalBody("r3"), http.StatusCreated, "")
	f.mirrorOf(t, "dr-3")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-3/withdraw", map[string]any{}, http.StatusOK, "")
	f.decisionWhen(t, "dr-3", "withdrawn", inState(ledger.DecisionCancelled))
	if slices.Contains(h.routedIDs(), "dr-3") {
		t.Fatal("dr-3 went into the provider with every slot held")
	}

	release()
	until(t, "dr-2 told once the provider returned", func() bool { return records.routedOK("dr-2") })
	until(t, "every call returned", func() bool { return h.inside.Load() == 0 })
}

// routedOK reports whether the decision's route was taken: a "routed" step
// with no error.
func (l *recordLog) routedOK(decision string) bool {
	return l.countErr("routed", decision, nil) > 0
}

// countErr is how many times the step was taken for the decision with err.
func (l *recordLog) countErr(step, decision string, err error) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.steps {
		if s.step == step && s.decision == decision && errors.Is(s.err, err) {
			n++
		}
	}
	return n
}

// hungRouter is a fakeRouter whose calls, but Route of free, never return
// until never is closed, whatever their context. inside counts its calls
// under way, most the most at once, resolving the Resolved among them.
type hungRouter struct {
	fakeRouter
	free                    string
	never                   chan struct{}
	inside, most, resolving atomic.Int32
}

// hang holds the call until never is closed.
func (h *hungRouter) hang() {
	n := h.inside.Add(1)
	for m := h.most.Load(); n > m && !h.most.CompareAndSwap(m, n); m = h.most.Load() {
	}
	<-h.never
	h.inside.Add(-1)
}

func (h *hungRouter) Route(ctx context.Context, d ext.Decision) (ext.Route, error) {
	rt, err := h.fakeRouter.Route(ctx, d)
	if d.Request != h.free {
		h.hang()
	}
	return rt, err
}

func (h *hungRouter) Resolved(ctx context.Context, r ext.Resolution) {
	h.resolving.Add(1)
	h.hang()
	h.resolving.Add(-1)
	h.fakeRouter.Resolved(ctx, r)
}

// The router reads a decision's state through its Host (ext.Host.Decision):
// open, then cancelled and final once withdrawn, with when it closed; a
// decision the ledger does not hold is ext.ErrNotFound, and a request that
// is not a decision's id is refused.
func TestARouterReadsADecisionsState(t *testing.T) {
	f := routerDecisionFixture(t, &fakeRouter{}, nil)
	h := &extHost{s: f.srv, name: "switchman", ctx: context.Background()}
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	st, err := h.Decision(t.Context(), "dr-1")
	if err != nil || st.Request != "dr-1" || st.State != ledger.DecisionOpen || st.Final || !st.ClosedAt.IsZero() {
		t.Fatalf("dr-1 read %+v, %v", st, err)
	}
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-1/withdraw", map[string]any{}, http.StatusOK, "")
	st, err = h.Decision(t.Context(), "dr-1")
	if err != nil || st.State != ledger.DecisionCancelled || !st.Final || st.ClosedAt.IsZero() {
		t.Fatalf("dr-1 read %+v, %v after its withdrawal", st, err)
	}
	if _, err := h.Decision(t.Context(), "dr-9"); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("dr-9 read %v", err)
	}
	for _, bad := range []string{"", "dr-0", "dr-x", "br-1", "dr-" + strings.Repeat("9", 19)} {
		if _, err := h.Decision(t.Context(), bad); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%q read %v, want refused", bad, err)
		}
	}
}

// serveOver starts a second server over the data directory of f's, with
// the decision router fr, the way a restart's server comes up while the
// first still holds the ledger's owner lock: it waits for the lock (far
// longer than the test takes: LockWait only bounds a hang).
func serveOver(t *testing.T, f *decisionFixture, fr *fakeRouter) *testEnv {
	t.Helper()
	was := ledger.LockWait
	ledger.LockWait = 5 * time.Minute
	t.Cleanup(func() { ledger.LockWait = was })
	srv, err := New(f.srv.cfg, f.srv.base, f.srv.log, nil, f.srv.store, WithExtensions(local.Fill(ext.Providers{
		Router: fr, All: []ext.Entry{{Kind: ext.KindDecisions, Name: "switchman", Value: fr}},
	})))
	if err != nil {
		t.Fatal(err)
	}
	e := f.serve(srv)
	t.Cleanup(func() { stopEveryRun(srv) })
	return e
}

// crash ends f's server as a crash would for the ledger: its engine lets
// its run go, its router tells nothing more, and its ledger lets go of the
// owner lock.
func crash(t *testing.T, f *decisionFixture) {
	t.Helper()
	f.srv.runs.ForgetForTest(f.run)
	f.srv.stopExtensions()
	if err := f.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
}

// The router is told what the ledger holds, by the ledger's owner only,
// across a restart: server A routes dr-1 and dr-2, and dr-2 is withdrawn
// while A's router is held in its Resolved, never told it. Server B,
// started over the same data directory while A holds the ledger, tells its
// router nothing, though A changes dr-1. A ends as a crash ends it; B
// takes the ledger over and tells its router dr-1 again (open: routed by
// A, Route told again, a repeat that is nothing new), never dr-2 (closed,
// its end owed to no one): B's router reads dr-2's end through its Host.
// dr-1's record gains no second router's row and no second response,
// whatever B's router answered. B's router hangs in the route it is told
// at the takeover, and nothing waits on it: dr-1 is answered meanwhile
// and applied as its run is continued, and the router is told its end,
// through another slot, while the route still hangs.
func TestTheRouterIsToldWhatTheLedgerHoldsAcrossARestart(t *testing.T) {
	records := watchRecords(t)
	ada := ext.Principal{Subject: "nate", Name: "Nate"}
	routeTo := func(name string) func(context.Context, ext.Decision) (ext.Route, error) {
		return func(_ context.Context, d ext.Decision) (ext.Route, error) {
			return ext.Route{To: []ext.Destination{{Kind: "person", Name: name}}, Answer: &ext.Answer{Input: "1", By: ada}}, nil
		}
	}
	frA := &fakeRouter{route: routeTo("Ada"), gate: make(chan struct{})}
	f := routerDecisionFixture(t, frA, nil)
	// Registered after the fixture's, so run before its end waits for
	// A's goroutines: A's Resolved is let go.
	t.Cleanup(func() { close(frA.gate) })
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-1")
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	records.wait(t, "routed", "dr-2")
	f.expectAs(t, f.tok, http.MethodPost, "/api/sessions/"+f.sid+"/decisions/dr-2/withdraw", map[string]any{}, http.StatusOK, "")
	f.decisionWhen(t, "dr-2", "cancelled", inState(ledger.DecisionCancelled))
	until(t, "A's router held in Resolved", func() bool {
		frA.mu.Lock()
		defer frA.mu.Unlock()
		return frA.calls == 1
	})

	// B waits for the lock; A changes dr-1, and B's router is told nothing.
	hung, release := make(chan struct{}), make(chan struct{})
	var hangOnce, hungOnce sync.Once
	t.Cleanup(func() { hangOnce.Do(func() { close(release) }) })
	frB := &fakeRouter{route: func(ctx context.Context, d ext.Decision) (ext.Route, error) {
		hungOnce.Do(func() { close(hung) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return routeTo("Grace")(ctx, d)
	}}
	// The route's wait only bounds a hang: B's route stays under way, its
	// decision told, until the test lets it go.
	long := 5 * time.Minute
	extWaiting.Store(&long)
	t.Cleanup(func() { extWaiting.Store(nil) })
	b := serveOver(t, f, frB)
	if ro, _ := b.srv.ledger.ReadOnly(); !ro {
		t.Fatal("B took the owner lock A holds")
	}
	passed := records.count("passed", "")
	reads := records.count("done", "dr-1")
	rev := f.decisionWhen(t, "dr-1", "read", func(map[string]any) bool { return true })["rev"]
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "Looking", "expectedRev": rev}, http.StatusOK, "")
	records.readSince(t, "dr-1", reads)
	if got, n := frB.routedIDs(), len(frB.resolvedIDs()); len(got) != 0 || n != 0 {
		t.Fatalf("B's router was told %q and %d resolutions while A owned the ledger", got, n)
	}

	// A crashes; B takes the ledger over and tells what it holds open.
	crash(t, f)
	until(t, "B's pass", func() bool { return records.count("passed", "") > passed })
	<-hung
	// dr-2 ended as the ledger changed hands: B's router reads its end.
	st, err := (&extHost{s: b.srv, name: "switchman", ctx: context.Background()}).Decision(t.Context(), "dr-2")
	if err != nil || st.State != ledger.DecisionCancelled || !st.Final {
		t.Fatalf("B's router read dr-2 as %+v, %v", st, err)
	}
	// While B's router hangs in dr-1's route, the operator answers dr-1
	// (its run interrupted, the answer recorded), and the run is
	// continued, which applies it.
	rev = b.decisionWhen(t, "dr-1", "read", func(map[string]any) bool { return true })["rev"]
	b.respond(t, adminToken, "dr-1", "a1", 0, int(rev.(float64)), http.StatusCreated, "")
	b.decisionWhen(t, "dr-1", "answered", inState(ledger.DecisionAnswered))
	b.expect(t, http.MethodPost, "/api/runs/"+f.run+"/resume", nil, http.StatusCreated, "")
	b.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	until(t, "dr-1 resolved by B while its route hangs", func() bool { _, n := frB.resolution("dr-1"); return n > 0 })
	hangOnce.Do(func() { close(release) })
	records.wait(t, "routed", "dr-1")
	until(t, "B's route of dr-1 handled", func() bool { return records.count("routed", "dr-1") >= 2 })
	if got := frB.routedIDs(); !slices.Equal(got, []string{"dr-1"}) {
		t.Fatalf("B's router was routed %q, want dr-1 alone (dr-2 is closed)", got)
	}
	if r, n := frB.resolution("dr-1"); n != 1 || r.Request != "dr-1" {
		t.Errorf("B's router told dr-1 resolved %d times: %+v", n, r)
	}
	if _, n := frB.resolution("dr-2"); n != 0 {
		t.Errorf("B's router told dr-2's end %d times, which B never told open", n)
	}
	if got := frA.resolvedIDs(); len(got) != 0 {
		t.Fatalf("A's router was told %q", got)
	}
	d := b.decisionWhen(t, "dr-1", "the routes recorded", func(d map[string]any) bool { return true })
	routed, answers := 0, 0
	for _, h := range historyOf(d) {
		switch h {
		case "routed router:switchman":
			routed++
		case "responded " + ext.SubjectActor + "nate":
			answers++
		}
	}
	if routed != 1 || answers != 1 {
		t.Fatalf("dr-1 has %d router's rows and %d router's responses: %q", routed, answers, historyOf(d))
	}
}
