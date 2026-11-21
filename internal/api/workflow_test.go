package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The walk itself (docs/plans/end-to-end.md, The workflow, step by step
// and The conditions): TestWorkflowEndToEnd walks the fourteen steps with
// every provider a fake, TestWorkflowFree the same steps with none, and
// one test per condition starts from the decision given to the decide
// grant (toDecision) and goes as far as main can show it; the walk
// through sync's seams is workflow_sync_test.go's.

// TestWorkflowEndToEnd walks the workflow with every provider slot it
// passes through filled by a fake, truthful after every step.
func TestWorkflowEndToEnd(t *testing.T) {
	walkWorkflow(t, newWorkflow(t, false))
}

// TestWorkflowFree walks the same steps with no provider registered: the
// operator answers, no context is given, nothing is routed; every promise
// holds.
func TestWorkflowFree(t *testing.T) {
	walkWorkflow(t, newWorkflow(t, true))
}

// walkWorkflow is the fourteen steps, assertTruthful after each.
func walkWorkflow(t *testing.T, w *workflow) {
	clock := w.bk.fakeClock()

	// 1. A ticket says what done means.
	w.fileTicket(t)
	w.assertTruthful(t, "step 1, the ticket filed")

	// 2. Context of the exact commit is prepared: nothing of the server's
	// until an attempt starts, and the provider told the run's base as the
	// run starts (step 5).
	if w.fakes != nil {
		if qs, ns := w.fakes.context.Queries(), w.fakes.context.commits(); len(qs) != 0 || len(ns) != 0 {
			t.Fatalf("the context provider was asked %+v and told %+v before any run", qs, ns)
		}
	}

	// 3. Brakeman drafts the plan; the operator proposes it.
	w.draftPlan(t, clock)
	w.assertTruthful(t, "step 3, the plan drafted and proposed")

	// 4. The operator approves the revision; an approval that no longer
	// holds is never launched, and says so.
	w.approve(t)
	w.assertTruthful(t, "step 4, the revision approved")

	// 5. Yardmaster starts the managed run; the context provider is told
	// its base.
	w.launch(t)
	w.commitsNoted(t, ext.CommitNote{Run: w.run, Kind: ext.CommitBase, Commit: w.head})
	w.assertTruthful(t, "step 5, the run launched")

	// 6. The implementing agent, at work, looks its context up and raises
	// an architecture question.
	w.lookUp(t)
	w.assertTruthful(t, "step 6, the implementer's lookups")
	w.raise(t)
	w.assertTruthful(t, "step 6, the question raised")

	// 7. Yardmaster keeps it and holds only the asking task.
	w.holdsTheAskerAlone(t)
	w.assertTruthful(t, "step 7, the asking task held alone")

	// 8. It is given to someone who may answer.
	if w.free() {
		before := w.record(t, w.did)
		out := w.expect(t, http.MethodPost, "/api/decisions/"+w.did+"/route", map[string]any{"opId": "give-free", "to": toGrant, "expectedRev": before.Rev}, http.StatusConflict, "no_identity")
		if !strings.Contains(apiMessage(out), "no identity provider is registered") {
			t.Fatalf("the route to the grant with no provider: %v", out)
		}
		if after := w.record(t, w.did); after.Rev != before.Rev || len(after.History) != len(before.History) {
			t.Fatalf("the refused route wrote: rev %d, %d history rows (were %d, %d)", after.Rev, len(after.History), before.Rev, len(before.History))
		}
	} else {
		w.routedByPolicy(t)
	}
	w.assertTruthful(t, "step 8, the decision given")

	// 9. Brakeman prepares a brief.
	w.briefDecision(t, clock)
	w.assertTruthful(t, "step 9, Brakeman's brief")

	// 10 and 11. The person responds from the decide page; Yardmaster
	// validates and applies it once.
	w.respond(t)
	w.assertTruthful(t, "steps 10 and 11, the answer applied")

	// 12. The work goes on to a candidate, its checks and its review.
	candidate := w.candidate(t)
	w.commitsNoted(t, ext.CommitNote{Run: w.run, Kind: ext.CommitBase, Commit: w.head},
		ext.CommitNote{Run: w.run, Task: "build", Attempt: 1, Kind: ext.CommitCandidate, Commit: candidate})
	attemptContextListed(t, w)
	w.assertTruthful(t, "step 12, the candidate reviewed")

	// 13. A person batches their findings into a repair.
	repaired := w.repair(t, candidate)
	w.assertTruthful(t, "step 13, the repair")

	// 14. Dispatch records the outcome.
	w.outcome(t, repaired)
	w.assertTruthful(t, "step 14, the outcome")

	if w.free() {
		return
	}
	// The router told of the decision's end, the sync store of the
	// commits, the sink of the answer.
	for what, f := range map[string]struct {
		fake interface{ Changed() <-chan struct{} }
		ok   func() bool
	}{
		"the router told of the end": {w.fakes.router, func() bool {
			return slices.ContainsFunc(w.fakes.router.Resolutions(), func(r ext.Resolution) bool { return r.Request == w.did })
		}},
		"the sync store told of the commits": {w.fakes.sync, func() bool { return len(w.fakes.sync.Docs()) > 0 }},
		"the sink handed the answer": {w.fakes.sink, func() bool {
			return slices.ContainsFunc(w.fakes.sink.Notifications(), func(n ext.Notification) bool {
				return n.Type == "decision" && n.State == ledger.DecisionAnswered
			})
		}},
	} {
		awaitFake(t, f.fake, what, f.ok)
	}
}

// TestWorkflowStaleCard: ana reads the card; before her tap lands, the
// operator comments, held between her send's read and its transaction
// (decideSeams.afterRead). Her answer is refused stale_decision in the
// server's words, its refusal kept with its receipt, never counted; the
// decision stays open and the card shows its new rev, at which her next
// answer is taken and applied once.
func TestWorkflowStaleCard(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	f := w.decide()
	read := f.view(t, passAna, w.did)
	kept := len(w.record(t, w.did).Responses)
	at, gate := f.holdAt(t, false)
	sent := make(chan walkReply, 1)
	client, base := w.client, w.http.URL
	go func() {
		sent <- walkSend(client, base, passAna, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "stale-1", "choice": 0, "expectedRev": read.Rev})
	}()
	reached(t, at, "ana's answer")
	f.srv.decideSeam.Store(nil)
	w.expect(t, http.MethodPost, "/api/decisions/"+w.did+"/comment", map[string]any{"opId": "c-1", "kind": "comment", "text": "Check the SSO callback first.", "expectedRev": read.Rev}, http.StatusOK, "")
	close(gate)
	got := received(t, sent, "ana's stale answer")
	if got.err != nil {
		t.Fatal(got.err)
	}
	w.assertTruthful(t, "the stale answer refused")
	if got.status != http.StatusConflict {
		t.Fatalf("an answer to a card the decision moved past was taken: %d %s", got.status, got.data)
	}
	var r proto.DecideRefusal
	strictly(t, got.data, &r)
	if got.status != http.StatusConflict || r.Error.Code != crew.CodeStaleDecision || r.Error.Message != decideWords(crew.CodeStaleDecision, w.did) ||
		r.Receipt == nil || r.Receipt.Status != "refused" || r.Receipt.Code != crew.CodeStaleDecision {
		t.Fatalf("the stale answer: %d %s", got.status, got.data)
	}
	rec := w.record(t, w.did)
	if rec.State != ledger.DecisionOpen || len(rec.Responses) != kept+1 || rec.Responses[kept].Status != "refused" || rec.Rev != read.Rev+1 {
		t.Fatalf("after the stale answer: %s rev %d, %+v", rec.State, rec.Rev, rec.Responses)
	}
	now := f.view(t, passAna, w.did)
	if now.Rev != read.Rev+1 || now.State != ledger.DecisionOpen || len(now.Own) != 1 || now.Own[0].Code != crew.CodeStaleDecision {
		t.Fatalf("the card at its new rev: %+v", now)
	}
	if status, data, _ := f.respondAs(t, passAna, w.did, "fresh-1", 0, now.Rev); status != http.StatusCreated {
		t.Fatalf("ana's answer at the new rev: %d %s", status, data)
	}
	w.applied(t, w.did)
	w.assertTruthful(t, "the answer at the new rev applied")
}

// TestWorkflowDuplicateTap: ana taps Approve, the reply is lost and the
// phone sends it again with the same response id; the operator answers
// too. One receipt, the same each time; the operator's refused
// decision_answered with words naming who answered first; one
// application, one release of the held work, the answer typed into the
// asking attempt once; and once the build goes on to its candidate and the
// review starts, the build still has its one attempt: no work ran twice.
func TestWorkflowDuplicateTap(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	f := w.decide()
	before := w.record(t, w.did)
	rev := before.Rev
	status, first, _ := f.respondAs(t, passAna, w.did, "tap-1", 0, rev)
	if status != http.StatusCreated {
		t.Fatalf("the first tap: %d %s", status, first)
	}
	status, again, _ := f.respondAs(t, passAna, w.did, "tap-1", 0, rev)
	if status != http.StatusOK {
		t.Fatalf("the tap sent again is not answered from the first's receipt: %d %s", status, again)
	}
	var r1, r2 proto.DecideReply
	strictly(t, first, &r1)
	strictly(t, again, &r2)
	if status != http.StatusOK || r2.Receipt.K != r1.Receipt.K || !r2.Receipt.At.Equal(r1.Receipt.At) || r2.Receipt.Status != "accepted" {
		t.Fatalf("the tap sent again: %d %s, the first %s", status, again, first)
	}
	out := w.respond1(t, w.did, adminToken, "op-1", 1, http.StatusConflict, crew.CodeDecisionAnswered)
	if !strings.Contains(apiMessage(out), walkAna.Name) {
		t.Fatalf("the operator is not told who answered first: %v", out)
	}
	w.applied(t, w.did)
	w.assertTruthful(t, "a tap sent twice and the operator's answer")
	holds, err := w.srv.ledger.Holds(t.Context(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	released := 0
	for _, h := range holds {
		if h.Decision == numberOf(w.did) && !h.ReleasedAt.IsZero() {
			released++
		}
	}
	rec := w.record(t, w.did)
	accepted := 0
	for _, rr := range rec.Responses {
		if rr.Status == "accepted" {
			accepted++
		}
	}
	if released != 1 || accepted != 1 || len(rec.Responses) != len(before.Responses)+2 {
		t.Fatalf("%d releases, %d accepted of %d responses (%d before the taps)", released, accepted, len(rec.Responses), len(before.Responses))
	}
	// The build goes on: its candidate, its check, the review started.
	w.candidate(t)
	w.assertTruthful(t, "the build's candidate after the taps")
	attempts, err := w.srv.ledger.Attempts(t.Context(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	for _, a := range attempts {
		if a.TaskID == "build" {
			builds++
		}
	}
	if builds != 1 {
		t.Fatalf("the build ran %d attempts for one answer: %v", builds, attemptNumbers(attempts))
	}
	if n := w.typedTimes(t, w.sid, w.did); n != typedShows {
		t.Fatalf("the answer to %s shows %d times in the asking attempt's terminal, one typing shows it %d times", w.did, n, typedShows)
	}
}

// TestWorkflowWithdrawnResponder: the identity provider stops admitting
// the architecture owner's pass. Before she responds: the decide page's
// read and answer are refused as a pass it does not know, nothing kept;
// the operator takes the decision back from the grant, a decide principal
// whose pass holds is refused it too, and the operator answers under the
// free policy. Between her response's
// commit and its application (the recheck held at the provider's gate):
// her response is kept, the decision superseded by a fresh one carrying
// its hold, nothing applied, and the operator answers the fresh one.
func TestWorkflowWithdrawnResponder(t *testing.T) {
	t.Run("before the response", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		kept := len(w.record(t, w.did).Responses)
		w.fakes.id.Remove(passAna)
		delete(w.eligible, ext.SubjectActor+walkAna.Subject)
		if status, data := w.decide().readAs(t, passAna, w.did); status != http.StatusUnauthorized {
			t.Fatalf("the withdrawn pass's read: %d %s", status, data)
		}
		if status, data, _ := w.respondAs(t, passAna, "late-1", 0); status != http.StatusUnauthorized {
			t.Fatalf("the withdrawn pass's answer: %d %s", status, data)
		}
		if rec := w.record(t, w.did); len(rec.Responses) != kept || rec.State != ledger.DecisionOpen {
			t.Fatalf("after the withdrawn pass: %s %+v", rec.State, rec.Responses)
		}
		w.assertTruthful(t, "the withdrawn pass refused")
		// The operator takes it back from the grant: no decide principal
		// may answer it now, bob, whose pass still holds, among them.
		w.decide().takeBack(t, w.did)
		delete(w.eligible, ext.SubjectActor+walkBob.Subject)
		status, data, _ := w.respondAs(t, passBob, "bob-1", 0)
		w.assertTruthful(t, "a decide principal's answer once taken back")
		if status != http.StatusNotFound {
			t.Fatalf("bob's answer once taken back: %d %s", status, data)
		}
		w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
		w.applied(t, w.did)
		w.assertTruthful(t, "the operator's answer applied")
	})
	t.Run("between the response and its application", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		holdRechecks(t)
		g := w.fakes.id
		gate := make(chan struct{})
		g.mu.Lock()
		g.asked[passAna], g.then[passAna], g.hold[passAna] = 0, nil, gate
		g.mu.Unlock()
		t.Cleanup(func() {
			select {
			case <-gate:
			default:
				close(gate)
			}
		})
		rev := w.record(t, w.did).Rev
		sent := make(chan walkReply, 1)
		client, base := w.client, w.http.URL
		go func() {
			sent <- walkSend(client, base, passAna, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "ana-1", "choice": 0, "expectedRev": rev})
		}()
		select {
		case <-g.reached:
		case <-time.After(testWait):
			t.Fatal("the recheck never reached the provider")
		}
		g.Remove(passAna)
		close(gate)
		if got := received(t, sent, "ana's answer"); got.err != nil || got.status != http.StatusCreated {
			t.Fatalf("ana's answer, recorded before the recheck: %d %s %v", got.status, got.data, got.err)
		}
		old := w.decisionWhen(t, w.did, "superseded", inState(ledger.DecisionSuperseded))
		fresh, _ := old["supersededBy"].(string)
		if old["applied"] != nil || fresh == "" {
			t.Fatalf("the answer not rechecked: %v", old)
		}
		w.assertTruthful(t, "the recheck refused")
		rec := w.record(t, fresh)
		if rec.State != ledger.DecisionOpen || !slices.Equal(rec.Held, []string{"build"}) || len(rec.Responses) != 0 {
			t.Fatalf("the fresh decision: %s held %v, %d responses", rec.State, rec.Held, len(rec.Responses))
		}
		// The operator's card says why it asks again.
		if why, _ := old["why"].(string); why == "" {
			t.Fatalf("superseded with no words: %v", old)
		}
		t.Logf("%s superseded by %s: %s", w.did, fresh, old["why"])
		w.respond1(t, fresh, adminToken, "op-1", 0, http.StatusCreated, "")
		w.applied(t, fresh)
		w.assertTruthful(t, "the fresh decision answered by the operator")
	})
}

// TestWorkflowContextUnavailable: the context provider fails, or is late
// past its wait, when an attempt starts and when its agent looks up. The
// attempt starts with no Context section and nothing of the provider's,
// its run going on: the late provider's call is ended by the guard's
// wait, not by the provider. The lookup says context is unavailable, and
// the run page lists none for the attempt.
func TestWorkflowContextUnavailable(t *testing.T) {
	t.Run("failing", func(t *testing.T) {
		fakes := newWorkflowFakes()
		fakes.context.SetErr(errors.New("the graph is down"))
		w := newWorkflowWith(t, fakes)
		w.noContext = true
		w.toDecision(t)
		if qs := fakes.context.Queries(); !slices.ContainsFunc(qs, func(q ext.ContextQuery) bool { return q.Run == w.run && q.Task == "build" }) {
			t.Fatalf("the failing provider was never asked: %+v", qs)
		}
		w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
		w.applied(t, w.did)
		w.assertTruthful(t, "the operator's answer with the provider failing")
		contextAbsent(t, w)
		w.assertTruthful(t, "the lookup with the provider failing")
	})
	t.Run("late", func(t *testing.T) {
		fakes := newWorkflowFakes()
		fakes.late = &lateContext{walkContext: fakes.context, gate: make(chan struct{}), asked: make(chan ext.ContextQuery, 8)}
		w := newWorkflowWith(t, fakes)
		// Run before the server's cleanup (last in, first out): the late
		// calls return.
		t.Cleanup(func() { close(fakes.late.gate) })
		w.noContext = true
		w.toDecision(t)
		if q := received(t, fakes.late.asked, "the late provider asked"); q.Run != w.run || q.Task != "build" {
			t.Fatalf("the late provider was asked %+v", q)
		}
		// The guard ended the call at the product's wait, and says so: the
		// attempt started on that, not on the provider (which never
		// answers while the test runs) and not on a later bound.
		note := fmt.Sprintf("kind=context provider=graph failure=late failures=1 err=\"no answer within %s\"", ext.ContextWait)
		waitUntil(t, "the guard's note of the late call", func() bool { return strings.Contains(w.logs.String(), note) })
		w.assertTruthful(t, "an attempt started past a late provider")
		// The lookup is ended by the guard's own wait for lookups, the
		// provider never answering while the test runs.
		contextAbsent(t, w)
		w.assertTruthful(t, "the lookup past a late provider")
	})
}

// TestWorkflowRouterAway: the router can no longer stand behind its
// policy for the decision ana alone may answer (decision-policy's I10):
// while it answers that its policy is behind, within ext.MaxBehind of its
// first such answer on the server's clock, the operator's answer is
// refused stale_policy, nothing kept, the decision open and its hold
// kept, and ana is not shown the decision. Past the bound the policy is
// not in force: ana is still refused, and the operator answers under
// local/1, the receipt saying why; the answer is applied once.
func TestWorkflowRouterAway(t *testing.T) {
	w := newWorkflow(t, false)
	clock := &policyFakeClock{now: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	w.srv.policyClock.mu.Lock()
	w.srv.policyClock.now = clock.Now
	w.srv.policyClock.mu.Unlock()
	w.toPolicy(t)
	w.fakes.router.SetConfirm(w.did, func(ext.ConfirmQuery) ext.Confirmation { return ext.Confirmation{InForce: true, Behind: true} })
	kept := len(w.record(t, w.did).Responses)
	stale := func(step string) {
		t.Helper()
		w.respond0(t, adminToken, "op-away", 0, http.StatusConflict, crew.CodeStalePolicy)
		if status, data, _ := w.respondAs(t, passAna, "ana-away", 0); status != http.StatusNotFound {
			t.Fatalf("ana's answer with the router behind: %d %s", status, data)
		}
		rec := w.record(t, w.did)
		if rec.State != ledger.DecisionOpen || len(rec.Responses) != kept || !slices.Equal(rec.Held, []string{"build"}) {
			t.Fatalf("%s: %s, %d responses (were %d), held %v", step, rec.State, len(rec.Responses), kept, rec.Held)
		}
		w.assertTruthful(t, step)
	}
	stale("the router behind")
	clock.add(ext.MaxBehind)
	stale("the router behind, at its bound")
	clock.add(time.Second)
	// Past the bound: not in force. The operator answers with the same
	// response id its refusals kept nothing of; ana is refused still.
	w.eligible = map[string]bool{adminActor: true}
	if status, data, _ := w.respondAs(t, passAna, "ana-away", 0); status != http.StatusNotFound {
		t.Fatalf("ana's answer past the bound: %d %s", status, data)
	}
	w.respond0(t, adminToken, "op-away", 0, http.StatusCreated, "")
	w.answer = "op-away"
	w.applied(t, w.did)
	if rc := w.decide().receiptOf(t, w.did, kept+1); rc.Status != "accepted" || rc.Policy != "local/1" || !strings.Contains(rc.PolicyWhy, "did not settle") || rc.Override {
		t.Fatalf("the operator's receipt past the bound: %+v", rc)
	}
	w.assertTruthful(t, "the operator's answer under local/1")
}

// TestWorkflowOwnerDisconnected: the owning server is out of ana's reach
// when she opens the card: her page cannot reach it, and nothing is sent
// or kept. The server back over its data directory, with the same fakes:
// the card at its rev, still open, its hold kept; the run continued, her
// answer is taken and applied once. An answer held elsewhere while the
// server was away is handed in once by the sync store and applied once;
// where the router confirms policies it is refused at its door.
func TestWorkflowOwnerDisconnected(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	read := w.decide().view(t, passAna, w.did)
	kept := len(w.record(t, w.did).Responses)
	// Out of reach: the phone's dial is refused as the kernel refuses a port
	// nobody listens on, through the client's own dial, never by dialling a
	// port the test has freed.
	if got := walkSend(refusingClient(), w.http.URL, passAna, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "away-1", "choice": 0, "expectedRev": read.Rev}); !errors.Is(got.err, syscall.ECONNREFUSED) {
		t.Fatalf("the server out of reach answered: %d %s %v", got.status, got.data, got.err)
	}
	w.restart(t)
	rec := w.record(t, w.did)
	if rec.State != ledger.DecisionOpen || rec.Rev != read.Rev || len(rec.Responses) != kept || !slices.Equal(rec.Held, []string{"build"}) {
		t.Fatalf("after the restart: %s rev %d (read at %d), %d responses, held %v", rec.State, rec.Rev, read.Rev, len(rec.Responses), rec.Held)
	}
	if v := w.decide().view(t, passAna, w.did); v.Rev != read.Rev || v.State != ledger.DecisionOpen {
		t.Fatalf("the card after the restart: %+v", v)
	}
	w.assertTruthful(t, "the server back")
	next := entityOf(t, w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
	w.continued(next)
	w.assertTruthful(t, "the run continued")
	status, data, _ := w.respondAs(t, passAna, "back-1", 0)
	if status != http.StatusCreated {
		t.Fatalf("ana's answer once the run is continued: %d %s", status, data)
	}
	w.applied(t, w.did)
	w.assertTruthful(t, "the answer applied in the continuation")
	t.Run("an answer held while the server was away, handed in once", heldWhileAway)
	t.Run("a held answer where the router confirms policies, refused at its door", heldRefusedWherePoliciesAreConfirmed)
}

// TestWorkflowAgentRestarted: the implementing agent's session ends while
// the decision is open: the task waits for it, the attempt given back and
// not counted, and the answer reaches the next attempt in its task file.
// The server restarted between a response and its application: the
// operator's answer moves to the continuation and is applied once there;
// ana's (an identity's, which cannot be rechecked after a restart) is
// kept, its decision superseded by a fresh one carrying its hold, nothing
// applied.
func TestWorkflowAgentRestarted(t *testing.T) {
	t.Run("the agent's session ends", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		d, ok := w.srv.registry.Get(w.sid)
		if !ok {
			t.Fatalf("no session %s", w.sid)
		}
		if err := d.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		w.decisionWhen(t, w.did, "its attempt given back", func(d map[string]any) bool {
			return slices.ContainsFunc(historyOf(d), func(h string) bool { return strings.HasPrefix(h, ledger.HistoryRefunded+" ") })
		})
		taskVia(t, w.testEnv, w.run, "build", "waiting for "+w.did, func(task map[string]any) bool {
			holds, _ := task["holds"].([]any)
			return len(holds) == 1 && holds[0].(map[string]any)["id"] == w.did
		})
		w.assertTruthful(t, "the asking session ended")
		w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
		w.applied(t, w.did)
		a := runningAttempt(t, w.testEnv, w.run, "build", 0)
		w.assertTruthful(t, "the next attempt started")
		if a["n"] != 2.0 {
			t.Fatalf("the next attempt is %v", a["n"])
		}
		md := string(w.launches().treeFile(t, w.run, "build", 2, "task.md"))
		if !strings.Contains(md, w.did) || !strings.Contains(md, "Keep the session cookie") {
			t.Fatalf("the next attempt's task.md lacks the answer:\n%s", md)
		}
		if a["start"] != w.head {
			t.Fatalf("the next attempt starts at %v", a["start"])
		}
		w.assertTruthful(t, "the answer handed to the next attempt")
	})
	t.Run("the operator's answer across a restart", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/stop", nil, http.StatusOK, "")
		w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
		w.decisionWhen(t, w.did, "answered", inState(ledger.DecisionAnswered))
		w.assertTruthful(t, "the operator's answer recorded, not applied")
		w.restart(t)
		w.assertTruthful(t, "the server back with the answer recorded")
		next := entityOf(t, w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
		w.continued(next)
		w.assertTruthful(t, "the run continued")
		w.applied(t, w.did)
		runningAttempt(t, w.testEnv, next, "build", 0)
		w.assertTruthful(t, "the answer applied once in the continuation")
	})
	t.Run("ana's answer across a restart", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		// The recheck held: the answer stays recorded and unapplied while
		// the server ends.
		holdRechecks(t)
		g := w.fakes.id
		gate := g.recheckAs(passAna, walkAna, true)
		t.Cleanup(func() {
			select {
			case <-gate:
			default:
				close(gate)
			}
		})
		rev := w.record(t, w.did).Rev
		sent := make(chan walkReply, 1)
		client, base := w.client, w.http.URL
		go func() {
			sent <- walkSend(client, base, passAna, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "ana-1", "choice": 0, "expectedRev": rev})
		}()
		select {
		case <-g.reached:
		case <-time.After(testWait):
			t.Fatal("the recheck never reached the provider")
		}
		if rec := w.record(t, w.did); rec.State != ledger.DecisionAnswered {
			t.Fatalf("held at the recheck: %s", rec.State)
		}
		w.assertTruthful(t, "ana's answer recorded, its recheck held")
		crash(t, &decisionFixture{testEnv: w.testEnv, run: w.run})
		w.serveAgain(t)
		w.assertTruthful(t, "the server back with ana's answer recorded")
		next := entityOf(t, w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
		w.continued(next)
		w.assertTruthful(t, "the run continued")
		old := w.decisionWhen(t, w.did, "superseded", inState(ledger.DecisionSuperseded))
		fresh, _ := old["supersededBy"].(string)
		if old["applied"] != nil || fresh == "" {
			t.Fatalf("an identity's answer after a restart: %v", old)
		}
		if rec := w.record(t, fresh); rec.State != ledger.DecisionOpen || !slices.Equal(rec.Held, []string{"build"}) {
			t.Fatalf("the fresh decision: %s held %v", rec.State, rec.Held)
		}
		w.assertTruthful(t, "ana's answer superseded after the restart")
		close(gate)
		received(t, sent, "the ended server's reply to ana's answer")
		w.respond1(t, fresh, adminToken, "op-1", 0, http.StatusCreated, "")
		w.applied(t, fresh)
		w.assertTruthful(t, "the fresh decision answered by the operator")
	})
}

// TestWorkflowSidekickAway: Brakeman is not configured, or its turn ends
// with no brief. The card carries the decision's own words and no brief;
// the request is ended (or none is kept); the decision is unchanged and
// answerable without Brakeman.
func TestWorkflowSidekickAway(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		before := w.record(t, w.did)
		w.expect(t, http.MethodPost, "/api/brakeman/requests", map[string]any{"opId": "ask-off", "kind": brakeman.KindDecision, "subjects": []string{"task:" + w.run + "/build"}}, http.StatusConflict, "brakeman_off")
		if page, _, _ := w.bk.desk.st.List("", 10); len(page) != 0 {
			t.Fatalf("a request was kept: %v", page)
		}
		if v := w.decide().view(t, passAna, w.did); v.Question != walkQuestion || v.Rev != before.Rev {
			t.Fatalf("the card: %+v", v)
		}
		if status, data, _ := w.respondAs(t, passAna, "ana-1", 0); status != http.StatusCreated {
			t.Fatalf("ana's answer: %d %s", status, data)
		}
		w.applied(t, w.did)
		w.assertTruthful(t, "answered with Brakeman off")
	})
	t.Run("its turn ends with no brief", func(t *testing.T) {
		w := newWorkflow(t, false)
		w.toDecision(t)
		before := w.record(t, w.did)
		w.bk.enable(t, "bk", nil)
		id, sid, _ := w.turn(t, brakeman.KindDecision, "task:"+w.run+"/build")
		d, ok := w.srv.registry.Get(sid)
		if !ok {
			t.Fatalf("no session %s", sid)
		}
		if err := d.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if r := w.bk.waitState(t, id, brakeman.StateEnded); r.Why != "the session ended with no brief" || r.HasBrief {
			t.Fatalf("the request: %+v", r)
		}
		if after := w.record(t, w.did); after.Rev != before.Rev || after.State != ledger.DecisionOpen || len(after.Responses) != len(before.Responses) {
			t.Fatalf("the turn changed %s: %+v", w.did, after.DecisionRow)
		}
		if status, data, _ := w.respondAs(t, passAna, "ana-1", 0); status != http.StatusCreated {
			t.Fatalf("ana's answer: %d %s", status, data)
		}
		w.applied(t, w.did)
		w.assertTruthful(t, "answered after Brakeman's turn ended")
	})
}

// TestWorkflowEntitlementEnded: the providers stop giving what they gave,
// as providers whose entitlement ran out would: the identity provider no
// longer admits the decide passes, the router (which routed the first
// decision to ana) answers no destination, the context provider no block.
// The decision is still given to the decide grant, which no pass reaches
// now; its gates and holds are as they were; no response but the
// operator's is taken; stop, inspect and export are answered; the operator
// takes the decision back. The server asks the other two providers again
// and gets their lapse: the asking session ends, the operator's answer
// goes to the next attempt, whose start asks the context provider and is
// given no block; that attempt raises a second decision, told to the
// router, which routes nothing and holds no answer, so it stays open and
// held, the operator's to answer. No code of this repository asked the
// entitlement source.
func TestWorkflowEntitlementEnded(t *testing.T) {
	records := watchRecords(t)
	w := newWorkflow(t, false)
	w.toDecision(t)
	before := w.record(t, w.did)
	for _, pass := range []string{passAna, passBob} {
		w.fakes.id.Remove(pass)
	}
	for _, p := range []ext.Principal{walkAna, walkBob} {
		delete(w.eligible, ext.SubjectActor+p.Subject)
	}
	const second = "dr-2"
	w.fakes.router.SetRoute(second, ext.Route{})
	w.fakes.context.SetBlocks(nil)
	asked := len(w.fakes.context.Queries())
	for _, pass := range []string{passAna, passBob} {
		if status, data, _ := w.respondAs(t, pass, "lapsed-1", 0); status != http.StatusUnauthorized {
			t.Fatalf("%s's answer after the lapse: %d %s", pass, status, data)
		}
	}
	rec := w.record(t, w.did)
	if f := forOf(t, rec.Request); rec.State != ledger.DecisionOpen || rec.Rev != before.Rev || f["label"] != "grant:decide" || !slices.Equal(rec.Held, before.Held) || len(rec.Responses) != len(before.Responses) {
		t.Fatalf("after the lapse: %s rev %d for %v held %v, %d responses", rec.State, rec.Rev, f, rec.Held, len(rec.Responses))
	}
	w.assertTruthful(t, "the providers lapsed")
	// Inspect and export are the operator's still.
	runOf(t, w.testEnv, w.run)
	if resp, _ := w.do(http.MethodGet, "/api/export", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the export: %d", resp.StatusCode)
	}
	w.decide().takeBack(t, w.did)
	if f := forOf(t, w.record(t, w.did).Request); f["label"] == "grant:decide" {
		t.Fatalf("taken back, still for %v", f)
	}
	// The asking session ends; the operator's answer goes to the next
	// attempt, which asks the context provider as it starts.
	d, ok := w.srv.registry.Get(w.sid)
	if !ok {
		t.Fatalf("no session %s", w.sid)
	}
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.decisionWhen(t, w.did, "its attempt given back", func(d map[string]any) bool {
		return slices.ContainsFunc(historyOf(d), func(h string) bool { return strings.HasPrefix(h, ledger.HistoryRefunded+" ") })
	})
	w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
	w.applied(t, w.did)
	w.assertTruthful(t, "the operator's answer after the lapse")
	a := runningAttempt(t, w.testEnv, w.run, "build", 2)
	if qs := w.fakes.context.Queries(); !slices.ContainsFunc(qs[asked:], func(q ext.ContextQuery) bool { return q.Run == w.run && q.Task == "build" && q.Attempt == 2 }) {
		t.Fatalf("the next attempt's start never asked the context provider after the lapse: %+v", qs[asked:])
	}
	if md := string(w.launches().treeFile(t, w.run, "build", 2, "task.md")); strings.Contains(md, "## Context") || strings.Contains(md, walkBlockMark) || !strings.Contains(md, w.did) {
		t.Fatalf("the next attempt's task.md after the lapse:\n%s", md)
	}
	// The next attempt raises a second decision: the router is told of it
	// and routes nothing.
	w.sid = a["sessionId"].(string)
	w.tok = w.agentToken(w.sid)
	got := entityOf(t, w.decide().ask(t, w.raiseBody("arch-2"), http.StatusCreated, ""), "decision")
	if got["id"] != second {
		t.Fatalf("the second decision: %v", got)
	}
	awaitFake(t, w.fakes.router, "the router told of "+second, func() bool {
		return slices.ContainsFunc(w.fakes.router.Decisions(), func(d ext.Decision) bool { return d.Request == second })
	})
	// The router's answer to it handled (the router's own step, its seam):
	// no destination and no answer.
	if step := records.wait(t, "routed", second); step.err != nil || len(step.rt.To) != 0 || step.rt.Answer != nil {
		t.Fatalf("the lapsed router's answer to %s: %+v", second, step)
	}
	rec = w.record(t, second)
	f := forOf(t, rec.Request)
	if rec.State != ledger.DecisionOpen || !slices.Equal(rec.Held, []string{"build"}) || len(rec.Responses) != 0 || f["label"] != "role:"+crew.RespArchitecture {
		t.Fatalf("%s after the router's lapse: %s held %v, %d responses, for %v", second, rec.State, rec.Held, len(rec.Responses), f)
	}
	for _, h := range rec.History {
		if h.By == "router:switchman" {
			t.Fatalf("the lapsed router routed %s: %+v", second, h)
		}
	}
	w.assertTruthful(t, "a second decision after the lapse")
	w.respond1(t, second, adminToken, "op-2", 0, http.StatusCreated, "")
	w.applied(t, second)
	w.assertTruthful(t, "the second decision answered by the operator")
	w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/stop", nil, http.StatusOK, "")
	if asked := w.ents.Asked(); len(asked) != 0 {
		t.Fatalf("the entitlement source was asked about %v", asked)
	}
}
