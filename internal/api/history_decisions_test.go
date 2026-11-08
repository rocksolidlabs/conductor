package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// DR1's decision records in the history (docs/plans/history.md, PR-9):
// each request a record, each response it accepted a decision, read
// through the ledger's own record of them. Every wait is on the indexer
// (histSettle, reconcileNow) or on the ledger's own rows, never on a
// clock.

const histPolicy = `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`

// histAsk opens a decision of the run's task build as its attempt 1's
// agent would (an approval of two designs about commit 1), or a plan's
// gate g1 (an approval of Approve and Reject about plan pl-1) when gate,
// and answers its id.
func histAsk(t *testing.T, ld *ledger.Ledger, run string, gate bool) string {
	t.Helper()
	d := ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionApproval, TaskID: "build", Attempt: 1, SessionID: "s-build-1",
		Member: "lead", RequestID: "q-" + run, Fingerprint: "fp-" + run, Policy: histPolicy,
		Request: fmt.Sprintf(`{"question":"Which store keeps the sessions?","context":"Two fit the load.","options":[{"label":"Use Postgres","tradeoffs":"a server to run"},{"label":"Use SQLite","tradeoffs":"one file"}],"recommendation":1,"authorizes":"the store's migration","subject":{"kind":"commit","commit":%q}}`, commitID(1))}
	if gate {
		d = ledger.DecisionRow{Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: "g1", Plan: "pl-1", PlanDigest: "d1", Fingerprint: "fp-g1-" + run, Policy: histPolicy,
			Request: `{"question":"Ship the login redesign?","options":[{"label":"Approve"},{"label":"Reject"}],"authorizes":"review may start","subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`}
	}
	var id string
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		got, _, err := tx.OpenDecision(d)
		id = got.ID()
		return err
	})
	return id
}

// histRespond records a response to decision id: accepted (the decision
// answered with it) or refused, choosing choice, by Nate.
func histRespond(t *testing.T, ld *ledger.Ledger, run, id string, k, choice int, accepted bool, rationale string) {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	status := ledger.ResponseAccepted
	if !accepted {
		status = ledger.ResponseRefused
	}
	rc := crew.ResponseReceipt{Decision: id, Status: status, Words: "recorded", Actor: adminActor, ByName: "Nate", Choice: &choice, Rationale: rationale, At: histNow.Add(time.Duration(k) * time.Minute)}
	b, _ := json.Marshal(rc)
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		if _, err := tx.Respond(n, ledger.ResponseRow{Actor: adminActor, Authority: `{"grant":"admin","via":"workbench","boot":"b1"}`,
			ResponseID: fmt.Sprintf("r%d", k), Fingerprint: fmt.Sprintf("fp-r%d", k), Status: status, Receipt: string(b)}); err != nil {
			return err
		}
		if !accepted {
			return nil
		}
		if ok, err := tx.Answer(n, run, d.Rev); err != nil || !ok {
			return fmt.Errorf("the answer of %s: %v %v", id, ok, err)
		}
		return nil
	})
}

// histSettleDecision puts decision id in state with app as its
// application receipt (none when nil) and why.
func histSettleDecision(t *testing.T, ld *ledger.Ledger, run, id, state string, app *crew.Application, why, by string) {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		d.State, d.Rev, d.Why, d.SupersededBy = state, d.Rev+1, why, by
		if app != nil {
			b, _ := json.Marshal(app)
			d.Applied = string(b)
		}
		return tx.PutDecision(d)
	})
}

// An agent's request and the response the decision accepted are indexed
// and found by their keys: the request a record of its question, its
// subject's commit and its scope, grouped under its asking task; the
// response a decision by its actor, its rationale searchable, linked to
// the request it answers and to the attempt Yardmaster applied it to. A
// refusal decided nothing and has no entry. The search serves both with
// the decision's card as their page and its state now as their
// availability, and a reindex from nothing finds them again.
func TestADecisionAndItsResponseAreIndexedByTheirKeys(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000d1"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, false)
	histRespond(t, ld, run, id, 1, 0, false, "")
	histRespond(t, ld, run, id, 2, 1, true, "one box holds the load")
	histSettleDecision(t, ld, run, id, ledger.DecisionApplied, &crew.Application{At: histNow, By: "yardmaster", Run: run, Task: "build", Attempt: 1, Response: 2, Validated: true, How: crew.HowTyped, Words: "typed"}, "", "")
	ix := histSettle(t, e)

	req := histEntry(t, ix, hDecisionRef(id))
	if req.Kind != "decision" || req.Class != history.ClassRecord || req.Title != "Which store keeps the sessions?" || req.Commit != commitID(1) ||
		req.Run != run || req.Anchor != hTaskRef(run, "build") || req.Outcome != ledger.DecisionApplied || req.Agent != "cat" {
		t.Fatalf("the request: %+v", req.Entry)
	}
	if !strings.Contains(req.Summary, "a yes authorizes: the store's migration") || !strings.Contains(req.Summary, "asked by build #1") {
		t.Fatalf("the request's scope: %q", req.Summary)
	}
	resp := histEntry(t, ix, hResponseRef(id, 2))
	if resp.Kind != "decision.response" || resp.Class != history.ClassDecided || resp.Actor != adminActor || resp.Outcome != "answered" ||
		resp.Title != id+": Use SQLite, by Nate" || resp.Commit != commitID(1) || resp.Anchor != hTaskRef(run, "build") {
		t.Fatalf("the response: %+v", resp.Entry)
	}
	histGone(t, ix, hResponseRef(id, 1))
	if got := histOut(t, ix, hResponseRef(id, 2), relAnswers); !slices.Equal(got, []string{hDecisionRef(id)}) {
		t.Fatalf("answers: %v", got)
	}
	if got := histOut(t, ix, hResponseRef(id, 2), relApplied); !slices.Equal(got, []string{attemptKey(run, "build", 1)}) {
		t.Fatalf("applied: %v", got)
	}

	out := hsSearch(t, e, "load kind:decision.response")
	if keys := hsKeys(out); !slices.Equal(keys, []string{hResponseRef(id, 2)}) {
		t.Fatalf("the rationale's words: %v", keys)
	}
	got := hsEntry(out, hResponseRef(id, 2))
	if want := "/runs/" + run + "?view=tasks&task=build&decision=" + id; got["url"] != want {
		t.Fatalf("the response's page: %v, want %s", got["url"], want)
	}
	if a := availOf(got); a["state"] != ledger.DecisionApplied {
		t.Fatalf("the response's availability: %v", a)
	}
	full := hsEntryOf(t, e, hDecisionRef(id))
	if src, _ := full["source"].(map[string]any); src["label"] != "decision request" || src["store"] != "ledger" {
		t.Fatalf("the request's source: %v", full["source"])
	}
	found := false
	for _, l := range full["links"].([]any) {
		m := l.(map[string]any)
		if m["rel"] == relAnswers && m["dir"] == "in" && m["key"] == hResponseRef(id, 2) {
			found = m["url"] == "/runs/"+run+"?view=tasks&task=build&decision="+id && m["class"] == history.ClassDecided
		}
	}
	if !found {
		t.Fatalf("the request's answer among its links: %v", full["links"])
	}

	// From nothing: the catch-up lists the ledger's decisions.
	before, _ := ix.Stats(t.Context())
	e.srv.history.askReindex()
	ix = histSettle(t, e)
	if after, _ := ix.Stats(t.Context()); after.Generation == before.Generation {
		t.Fatal("the index was not cleared")
	}
	histEntry(t, ix, hDecisionRef(id))
	histEntry(t, ix, hResponseRef(id, 2))
}

// A gate's Reject is a rejected design: its response's outcome is
// rejected, the History's question "Rejected designs" (its query as the
// palette asks it, web/app/utils/history.ts) finds it, and its link names
// the tasks the rejection cancelled. Another gate approved is approved
// and not among them.
func TestAGatesRejectionIsARejectedDesign(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const rejected, approved = "hist-000000d2", "hist-000000d3"
	for _, run := range []string{rejected, approved} {
		histCreate(t, ld, run, "", histCrew(e.root))
	}
	no := histAsk(t, ld, rejected, true)
	histRespond(t, ld, rejected, no, 1, 1, true, "the redesign drops the remembered user")
	histSettleDecision(t, ld, rejected, no, ledger.DecisionApplied, &crew.Application{At: histNow, By: "yardmaster", Run: rejected, Response: 1, Validated: true, How: crew.HowCancelled, Tasks: []string{"review"}, Words: "cancelled"}, "", "")
	yes := histAsk(t, ld, approved, true)
	histRespond(t, ld, approved, yes, 1, 0, true, "")
	ix := histSettle(t, e)

	if r := histEntry(t, ix, hResponseRef(no, 1)); r.Outcome != "rejected" || r.Title != no+": Reject, by Nate" || r.Anchor != hRunRef(rejected) || r.Revision != "pl-1" {
		t.Fatalf("the rejection: %+v", r.Entry)
	}
	if r := histEntry(t, ix, hResponseRef(yes, 1)); r.Outcome != "approved" {
		t.Fatalf("the approval: %+v", r.Entry)
	}
	if got := histOut(t, ix, hResponseRef(no, 1), relApplied); !slices.Equal(got, []string{hTaskRef(rejected, "review")}) {
		t.Fatalf("applied: %v", got)
	}
	out := hsSearch(t, e, "kind:plan.withdrawn,plan.superseded,review.verdict,decision.response outcome:withdrawn,superseded,needs_changes,rejected")
	if keys := hsKeys(out); !slices.Equal(keys, []string{hResponseRef(no, 1)}) {
		t.Fatalf("the rejected designs: %v", keys)
	}
	if u := hsEntry(out, hResponseRef(no, 1))["url"]; u != "/runs/"+rejected+"?view=tasks&decision="+no {
		t.Fatalf("a gate's page: %v", u)
	}
}

// A superseded request says so in its availability, with its successor
// and DR1's words of why (a stale subject among them), read from the
// ledger as the page is answered; one still open says it waits. Its
// outcome follows its state as the index reads it again.
func TestASupersededRequestSaysSoInItsAvailability(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000d4"
	histCreate(t, ld, run, "", histCrew(e.root))
	old := histAsk(t, ld, run, false)
	histSettle(t, e)
	if a := availOf(hsEntryOf(t, e, hDecisionRef(old))); a["state"] != ledger.DecisionOpen || a["words"] != "open: waiting for its answer" {
		t.Fatalf("an open request: %v", a)
	}
	var next string
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionClarification, TaskID: "build", Attempt: 2, Member: "lead",
			RequestID: "q-next", Fingerprint: "fp-next", Policy: histPolicy, Request: fmt.Sprintf(`{"question":"Which store now?","subject":{"kind":"commit","commit":%q}}`, commitID(2))})
		next = d.ID()
		return err
	})
	const why = "the task's next candidate replaced its subject"
	histSettleDecision(t, ld, run, old, ledger.DecisionSuperseded, nil, why, next)
	// The page reads the ledger now, before the index has read it again.
	a := availOf(hsEntryOf(t, e, hDecisionRef(old)))
	if a["state"] != ledger.DecisionSuperseded || a["words"] != "superseded by "+next+": "+why {
		t.Fatalf("a superseded request: %v", a)
	}
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(old)); h.Outcome != ledger.DecisionSuperseded {
		t.Fatalf("the superseded request's outcome: %+v", h.Entry)
	}
	if h := histEntry(t, ix, hDecisionRef(next)); h.Outcome != ledger.DecisionOpen || h.Commit != commitID(2) {
		t.Fatalf("its successor: %+v", h.Entry)
	}
}

// A decision moved to a run's continuation is indexed under the run that
// holds it; one whose run is pruned leaves the index.
func TestADecisionFollowsItsRunAndLeavesWithIt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const from, to = "hist-000000d5", "hist-000000d6"
	histCreate(t, ld, from, "", histCrew(e.root))
	id := histAsk(t, ld, from, true)
	finish := func(run string) {
		histTx(t, ld, run, func(tx *ledger.Tx) error {
			row, _, err := tx.Run()
			if err != nil {
				return err
			}
			row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeFailed, histNow
			return tx.PutRun(row)
		})
	}
	finish(from)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != from {
		t.Fatalf("before the continuation: %+v", h.Entry)
	}
	histCreate(t, ld, to, from, histCrew(e.root))
	histTx(t, ld, to, func(tx *ledger.Tx) error {
		_, err := tx.MoveDecisions(from, to)
		return err
	})
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != to || h.Anchor != hRunRef(to) {
		t.Fatalf("after the continuation: %+v", h.Entry)
	}
	// Cancelled (final), its run finished and pruned: the record goes with
	// it, and so does its entry.
	histSettleDecision(t, ld, to, id, ledger.DecisionCancelled, nil, "cancelled by the operator", "")
	finish(to)
	ix = histSettle(t, e)
	histEntry(t, ix, hDecisionRef(id))
	if err := ld.Prune(t.Context(), to); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	// Handed back to the run that opened it, it is indexed there again.
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != from {
		t.Fatalf("handed back: %+v", h.Entry)
	}
	if err := ld.Prune(t.Context(), from); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	histGone(t, ix, hDecisionRef(id))
}

// A decision another process's engine answered, the ledger's owner,
// reaches no hook of this server's: the reconcile finds it through its
// run's revision, as it finds the run.
func TestADecisionAnsweredByTheLedgersOwnerIsReconciled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	const run = "hist-000000d7"
	histCreate(t, ld, run, "", histCrew(dir))
	id := histAsk(t, ld, run, false)
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server holds the ledger's lock")
	}
	noTick(t, e)
	ix := histSettle(t, e)
	histEntry(t, ix, hDecisionRef(id))
	histGone(t, ix, hResponseRef(id, 1))
	histRespond(t, ld, run, id, 1, 0, true, "Postgres for the replicas")
	ix = histSettle(t, e)
	histGone(t, ix, hResponseRef(id, 1))
	ix = histReconcile(t, e)
	if h := histEntry(t, ix, hResponseRef(id, 1)); h.Title != id+": Use Postgres, by Nate" {
		t.Fatalf("the answer the owner recorded: %+v", h.Entry)
	}
}
