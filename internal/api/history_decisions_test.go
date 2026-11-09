package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
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
	histRespondWith(t, ld, run, id, k, choice, accepted, rationale, nil)
}

// histRespondWith is histRespond with its receipt, as the ledger keeps
// it, changed by edit first (nil for none).
func histRespondWith(t *testing.T, ld *ledger.Ledger, run, id string, k, choice int, accepted bool, rationale string, edit func(rc map[string]any)) {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	status := ledger.ResponseAccepted
	if !accepted {
		status = ledger.ResponseRefused
	}
	rc := crew.ResponseReceipt{Decision: id, Status: status, Words: "recorded", Actor: adminActor, ByName: "Nate", Choice: &choice, Rationale: rationale, At: histNow.Add(time.Duration(k) * time.Minute)}
	b, _ := json.Marshal(rc)
	if edit != nil {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		edit(m)
		b, _ = json.Marshal(m)
	}
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
		resp.Title != id+": Use SQLite" || !strings.HasPrefix(resp.Summary, "by Nate · "+id+" asked: ") || resp.Commit != commitID(1) || resp.Anchor != hTaskRef(run, "build") {
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

	if r := histEntry(t, ix, hResponseRef(no, 1)); r.Outcome != "rejected" || r.Title != no+": Reject" || r.Anchor != hRunRef(rejected) || r.Revision != "pl-1" {
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

// histFinish finishes run, failed, so it may be pruned.
func histFinish(t *testing.T, ld *ledger.Ledger, run string) {
	t.Helper()
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		row, _, err := tx.Run()
		if err != nil {
			return err
		}
		row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeFailed, histNow
		return tx.PutRun(row)
	})
}

// A decision moved along a run's continuations is indexed under the run
// that holds it. A run pruned takes with it the decisions it opened and
// hands back those it held to the run it continued, under which they are
// indexed again, its record kept or not: the last of a chain of three,
// whose record is kept, hands its decision to the middle one, not to the
// run that opened it; the middle one, no record, to the first; and the
// first takes it with it.
func TestADecisionFollowsItsRunAndLeavesWithIt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const first, mid, last = "hist-000000d5", "hist-000000d6", "hist-000000d8"
	histCreate(t, ld, first, "", histCrew(e.root))
	id := histAsk(t, ld, first, true)
	histFinish(t, ld, first)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != first {
		t.Fatalf("before the continuation: %+v", h.Entry)
	}
	for _, c := range [][2]string{{first, mid}, {mid, last}} {
		histCreate(t, ld, c[1], c[0], histCrew(e.root))
		histTx(t, ld, c[1], func(tx *ledger.Tx) error {
			_, err := tx.MoveDecisions(c[0], c[1])
			return err
		})
		if c[1] == mid {
			histFinish(t, ld, mid)
		}
	}
	own := histAsk(t, ld, last, false)
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != last || h.Anchor != hRunRef(last) {
		t.Fatalf("after the continuations: %+v", h.Entry)
	}
	// Both cancelled (final), the last run finished, its record kept and
	// its rows pruned: the decision it opened goes with it, and the one it
	// held is indexed under the run it continued.
	for _, d := range []string{id, own} {
		histSettleDecision(t, ld, last, d, ledger.DecisionCancelled, nil, "cancelled by the operator", "")
	}
	histFinish(t, ld, last)
	saveRecord(t, e.srv, crew.Run{ID: last, StartedAt: histNow})
	ix = histSettle(t, e)
	histEntry(t, ix, hDecisionRef(own))
	if err := ld.Prune(t.Context(), last); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	histEntry(t, ix, hRunRef(last)) // its record is still indexed
	histGone(t, ix, hDecisionRef(own))
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != mid || h.Anchor != hRunRef(mid) {
		t.Fatalf("handed back by the last: %+v", h.Entry)
	}
	if err := ld.Prune(t.Context(), mid); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != first || h.Anchor != hRunRef(first) {
		t.Fatalf("handed back by the middle: %+v", h.Entry)
	}
	if err := ld.Prune(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	histGone(t, ix, hDecisionRef(id))
}

// A decision handed back by a prune to a run whose move no pass indexed
// is indexed again under it: indexed under the first of a chain of three,
// moved to the middle and on to the last before any pass read either
// move, the middle's pass scanning while the last holds it, and the last
// cancelled, finished and pruned (by the ledger's owner, which tells no
// hook of this server's) before the last's pass. Its mark still names the
// first, so the last's pass, finding the run gone, does not find it; the
// prune moved the middle's revision, so the reconcile reads the middle
// again, and its pass finds the decision it now holds.
func TestADecisionHandedBackPastAnUnindexedMoveIsIndexedAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	const first, mid, last = "hist-000000e1", "hist-000000e2", "hist-000000e3"
	histCreate(t, ld, first, "", histCrew(dir))
	id := histAsk(t, ld, first, true)
	histFinish(t, ld, first)
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server holds the ledger's lock")
	}
	n, _ := ledger.ParseDecisionID(id)
	var armed atomic.Bool
	pruned := make(chan error, 1)
	freshIndexer(t, e, func(h *historyIndexer) {
		h.after = func(d time.Duration) <-chan time.Time {
			if d == h.reconcileTick {
				return nil
			}
			return time.After(d)
		}
		// The middle's pass has scanned its decisions (none: the last holds
		// it); before it commits, the owner cancels the decision, finishes
		// the last run and prunes it.
		h.beforeRunReplace = func(key string) {
			if key != hRunRef(mid) || !armed.CompareAndSwap(true, false) {
				return
			}
			pruned <- func() error {
				if _, err := ld.Transition(context.Background(), last, func(tx *ledger.Tx) error {
					d, _, err := tx.Decision(n)
					if err != nil {
						return err
					}
					d.State, d.Rev, d.Why = ledger.DecisionCancelled, d.Rev+1, "cancelled by the operator"
					if err := tx.PutDecision(d); err != nil {
						return err
					}
					row, _, err := tx.Run()
					if err != nil {
						return err
					}
					row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeFailed, histNow
					return tx.PutRun(row)
				}); err != nil {
					return err
				}
				return ld.Prune(context.Background(), last)
			}()
		}
	})
	ix := histReconcile(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != first {
		t.Fatalf("before the moves: %+v", h.Entry)
	}
	for _, c := range [][2]string{{first, mid}, {mid, last}} {
		histCreate(t, ld, c[1], c[0], histCrew(dir))
		histTx(t, ld, c[1], func(tx *ledger.Tx) error {
			_, err := tx.MoveDecisions(c[0], c[1])
			return err
		})
		if c[1] == mid {
			histFinish(t, ld, mid)
		}
	}
	armed.Store(true)
	histReconcile(t, e)
	select {
	case err := <-pruned:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("the middle run's pass never ran")
	}
	if rec, ok, err := ld.Decision(t.Context(), n); err != nil || !ok || rec.CurrentRun != mid {
		t.Fatalf("the ledger after the prune: %+v %v %v", rec.DecisionRow, ok, err)
	}
	ix = histReconcile(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != mid || h.Anchor != hRunRef(mid) || h.Outcome != ledger.DecisionCancelled {
		t.Fatalf("handed back to the middle run: %+v", h.Entry)
	}
}

// A decision indexed without its repository (its reads past their
// retries) is read again by the reconcile like every other such source,
// though its run's mark holds the repository and its run's pass compares
// nothing: after a restart, the decision's read failing just before its
// run's succeeds, the reconcile finds the decision by its mark (U) and
// writes it whole.
func TestADecisionIndexedWithoutItsRepositoryIsReadAgain(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	var failNext atomic.Bool
	set := func(h *historyIndexer) {
		h.repoRetries = 0
		h.after = func(d time.Duration) <-chan time.Time {
			if d == h.reconcileTick {
				return nil
			}
			return time.After(d)
		}
		h.repoOf = func(ctx context.Context, dir string) (string, string, error) {
			if dir == e.root && failNext.CompareAndSwap(true, false) {
				return "", "", errors.New("git rev-parse: timed out")
			}
			return gitcli.RepoOf(ctx, dir)
		}
	}
	freshIndexer(t, e, set)
	ld := e.srv.ledger
	const run = "hist-000000e4"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, false)
	ix := histSettle(t, e)
	key := hDecisionRef(id)
	if h := histEntry(t, ix, key); h.Repo == "" || repoNotKnown(histMark(t, ix, key)) {
		t.Fatalf("before the restart: repo %q, mark %s", h.Repo, histMark(t, ix, key))
	}
	// A restart, its catch-up reading the decision (decision: sorts before
	// run:) as the repository fails once, then the run, whose mark is as
	// it was.
	failNext.Store(true)
	freshIndexer(t, e, set)
	ix = histSettle(t, e)
	if h := histEntry(t, ix, key); h.Repo != "" || !repoNotKnown(histMark(t, ix, key)) {
		t.Fatalf("the decision read as the repository failed: repo %q, mark %s", h.Repo, histMark(t, ix, key))
	}
	if m := histMark(t, ix, hRunRef(run)); repoNotKnown(m) {
		t.Fatalf("the run's mark: %s", m)
	}
	ix = histReconcile(t, e)
	if h := histEntry(t, ix, key); h.Repo == "" || repoNotKnown(histMark(t, ix, key)) {
		t.Fatalf("after the reconcile: repo %q, mark %s", h.Repo, histMark(t, ix, key))
	}
}

// A decision's words are searched past its entries' titles, each cut at
// its first line and MaxTitle: a request's question whole (DR1 allows 500
// characters) is in its body, and who answered a response and whom for
// lead its summary, its title cut from the answer alone, so a long or
// many-lined answer keeps them searchable.
func TestADecisionsWordsAreSearchedPastItsTitles(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000e5"
	histCreate(t, ld, run, "", histCrew(e.root))
	question := "Which store keeps the sessions " + strings.Repeat("once the replicas are measured under load ", 5) + "and the zebrafinch dashboard reads it?"
	if strings.Index(question, "zebrafinch") <= history.MaxTitle {
		t.Fatalf("the question's word at byte %d", strings.Index(question, "zebrafinch"))
	}
	// Two requests of the question, one answered in two lines, the other
	// at length.
	ids := make([]string, 2)
	answers := []string{"Yes\nProceed with the migration", strings.Repeat("the replicas take the load in turn ", 8)}
	for i := range ids {
		histTx(t, ld, run, func(tx *ledger.Tx) error {
			d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionApproval, TaskID: "build", Attempt: 1, SessionID: "s-build-1",
				Member: "lead", RequestID: fmt.Sprintf("q-long-%d", i), Fingerprint: fmt.Sprintf("fp-long-%d", i), Policy: histPolicy,
				Request: fmt.Sprintf(`{"question":%q,"options":[{"label":"Use Postgres"},{"label":"Use SQLite"}],"subject":{"kind":"commit","commit":%q}}`, question, commitID(1))})
			ids[i] = d.ID()
			return err
		})
		histRespondWith(t, ld, run, ids[i], 1, 1, true, "", func(rc map[string]any) {
			rc["text"] = answers[i]
			rc["byName"] = fmt.Sprintf("Quinlan%d", i)
			rc["for"] = map[string]any{"person": fmt.Sprintf("ana%d", i), "responsibility": crew.RespArchitecture}
		})
	}
	ix := histSettle(t, e)
	for _, id := range ids {
		if h := histEntry(t, ix, hDecisionRef(id)); !strings.HasPrefix(h.Body, question+"\n") || len(h.Title) > history.MaxTitle {
			t.Fatalf("the request: title %q, body %q", h.Title, h.Body)
		}
	}
	if keys := hsKeys(hsSearch(t, e, "zebrafinch kind:decision")); len(keys) != 2 || !slices.Contains(keys, hDecisionRef(ids[0])) || !slices.Contains(keys, hDecisionRef(ids[1])) {
		t.Fatalf("a word of the question past its title: %v", keys)
	}
	for i, id := range ids {
		ref := hResponseRef(id, 1)
		h := histEntry(t, ix, ref)
		if strings.Contains(h.Title, "Quinlan") || !strings.HasPrefix(h.Summary, fmt.Sprintf("by Quinlan%d for ana%d, the architecture owner · ", i, i)) {
			t.Fatalf("response %d: title %q, summary %q", i, h.Title, h.Summary)
		}
		for _, word := range []string{fmt.Sprintf("Quinlan%d", i), fmt.Sprintf("ana%d", i)} {
			if keys := hsKeys(hsSearch(t, e, word)); !slices.Equal(keys, []string{ref}) {
				t.Fatalf("a search for %s: %v", word, keys)
			}
		}
	}
	if h := histEntry(t, ix, hResponseRef(ids[0], 1)); h.Title != ids[0]+": Use SQLite: Yes" {
		t.Fatalf("a many-lined answer's title: %q", h.Title)
	}
}

// failingScan is the ledger as the indexer reads it, its scan of one
// run's decisions failing once when armed.
type failingScan struct {
	indexLedger
	run    string
	armed  atomic.Bool
	failed atomic.Int32
}

func (f *failingScan) Decisions(ctx context.Context, q ledger.DecisionFilter) ([]ledger.DecisionRow, error) {
	if q.CurrentRun == f.run && f.armed.CompareAndSwap(true, false) {
		f.failed.Add(1)
		return nil, errors.New("the decisions could not be read")
	}
	return f.indexLedger.Decisions(ctx, q)
}

// A run's scan of its decisions that fails is made again by its pass
// retried, with no other transition of the run: the run's mark is
// committed only once its decisions were compared, so the retry does not
// find the run as indexed and stop.
func TestAFailedScanOfARunsDecisionsIsMadeAgain(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000d9"
	histCreate(t, ld, run, "", histCrew(e.root))
	f := &failingScan{indexLedger: ld, run: run}
	freshIndexer(t, e, func(h *historyIndexer) { h.ledger = func() indexLedger { return f } })
	histSettle(t, e)
	f.armed.Store(true)
	id := histAsk(t, ld, run, false)
	ix := histSettle(t, e)
	if f.failed.Load() != 1 {
		t.Fatalf("the scans that failed: %d", f.failed.Load())
	}
	if h := histEntry(t, ix, hDecisionRef(id)); h.Run != run {
		t.Fatalf("the decision after the scan failed: %+v", h.Entry)
	}
}

// A decision takes what its entries carry from its run's record when the
// record arrives after it was indexed: a gate's, opened before the run's
// record was written, gains the record's ticket.
func TestADecisionTakesItsRunsRecordWhenItArrives(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000da"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, true)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Ticket != "" {
		t.Fatalf("before the record: %+v", h.Entry)
	}
	saveRecord(t, e.srv, crew.Run{ID: run, StartedAt: histNow, Ticket: &crew.RunTicket{ID: "t-1", Key: "CON-7"}})
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hDecisionRef(id)); h.Ticket != "CON-7" {
		t.Fatalf("after the record: %+v", h.Entry)
	}
	if keys := hsKeys(hsSearch(t, e, "ticket:CON-7 kind:decision")); !slices.Equal(keys, []string{hDecisionRef(id)}) {
		t.Fatalf("the ticket's decisions: %v", keys)
	}
}

// An assignment's response is done or not_done by the option it chose,
// in DR1's order (Done, then Not done), its title the option's label.
func TestAnAssignmentsResponseIsDoneOrNotDone(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000db"
	histCreate(t, ld, run, "", histCrew(e.root))
	ids := make([]string, 2)
	for i := range ids {
		histTx(t, ld, run, func(tx *ledger.Tx) error {
			d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionAssignment, TaskID: "build", Attempt: i + 1, SessionID: fmt.Sprintf("s-build-%d", i+1),
				Member: "lead", RequestID: fmt.Sprintf("q-assign-%d", i), Fingerprint: fmt.Sprintf("fp-assign-%d", i), Policy: histPolicy,
				Request: fmt.Sprintf(`{"question":"Rotate the staging keys?","subject":{"kind":"commit","commit":%q}}`, commitID(1))})
			ids[i] = d.ID()
			return err
		})
		histRespond(t, ld, run, ids[i], 1, i, true, "")
	}
	ix := histSettle(t, e)
	for i, want := range []struct{ outcome, label string }{{"done", "Done"}, {"not_done", "Not done"}} {
		if h := histEntry(t, ix, hResponseRef(ids[i], 1)); h.Outcome != want.outcome || h.Title != ids[i]+": "+want.label || !strings.HasPrefix(h.Summary, "by Nate · ") {
			t.Fatalf("the assignment answered %s: %+v", want.label, h.Entry)
		}
	}
}

// A decision's availability is pruned once the ledger no longer keeps it
// and unknown when the ledger is not open or cannot be read, never said
// as gone: read through availability.decision itself, after the prune and
// before the index has read it again.
func TestADecisionsAvailabilityIsPrunedOrUnknown(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000dc"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, false)
	histSettleDecision(t, ld, run, id, ledger.DecisionCancelled, nil, "cancelled by the operator", "")
	histFinish(t, ld, run)
	ix := histSettle(t, e)
	if err := ld.Prune(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	ref := hDecisionRef(id)
	if a := e.srv.newAvailability(t.Context(), ix, ld).decision(ref); a.State != availPruned || a.Words != "pruned: the ledger no longer keeps "+id {
		t.Fatalf("a pruned decision: %+v", a)
	}
	if a := e.srv.newAvailability(t.Context(), ix, nil).decision(ref); a.State != availUnknown || a.Words != "the ledger is not open here" {
		t.Fatalf("no ledger: %+v", a)
	}
	closed, err := ledger.Open(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if a := e.srv.newAvailability(t.Context(), ix, closed).decision(ref); a.State != availUnknown || a.Words != "the ledger could not be read; try again" {
		t.Fatalf("a ledger that cannot be read: %+v", a)
	}
}

// Whom a decision is for, as its record keeps it (HM1): the person an
// accepted response answered for, its receipt's, in the response's
// summary beside who answered, and a search for the person finds it; the
// request's for, the person its opening resolved (the object the ledger
// keeps, never the label as sent), in the request's summary.
func TestADecisionSaysWhomItIsFor(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000dd"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, true)
	histRespondWith(t, ld, run, id, 1, 1, true, "", func(rc map[string]any) {
		rc["for"] = map[string]any{"person": "ana", "responsibility": crew.RespArchitecture}
	})
	var named string
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: "g2", Plan: "pl-1", PlanDigest: "d1", Fingerprint: "fp-g2", Policy: histPolicy,
			Request: `{"question":"Ship the login redesign?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"},` +
				`"for":{"label":"role:architecture","person":"ana","responsibility":"architecture"}}`})
		named = d.ID()
		return err
	})
	histRespondWith(t, ld, run, named, 1, 1, true, "", func(rc map[string]any) {
		rc["for"] = map[string]any{"person": "bo"}
	})
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hResponseRef(id, 1)); h.Title != id+": Reject" || !strings.HasPrefix(h.Summary, "by Nate for ana, the architecture owner · ") {
		t.Fatalf("the response: %+v", h.Entry)
	}
	if r := histEntry(t, ix, hDecisionRef(named)); r.Title != "Ship the login redesign?" || r.Revision != "pl-1" || !strings.Contains(r.Summary, "for ana, the architecture owner") {
		t.Fatalf("the request: %+v", r.Entry)
	}
	if r := histEntry(t, ix, hResponseRef(named, 1)); r.Title != named+": Reject" || !strings.HasPrefix(r.Summary, "by Nate for bo · ") {
		t.Fatalf("the response for a person named by name: %+v", r.Entry)
	}
	if keys := hsKeys(hsSearch(t, e, "ana kind:decision.response")); !slices.Equal(keys, []string{hResponseRef(id, 1)}) {
		t.Fatalf("a search for the person: %v", keys)
	}
	if keys := hsKeys(hsSearch(t, e, "ana kind:decision")); !slices.Equal(keys, []string{hDecisionRef(named)}) {
		t.Fatalf("a search for the person a request is for: %v", keys)
	}
}

// A response is indexed as its ledger row's actor, which the ledger
// requires, whatever its receipt's copy says; a response with no actor is
// indexed with none and named by no one, never as the operator's.
func TestAResponseIsIndexedAsItsRowsActor(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000de"
	histCreate(t, ld, run, "", histCrew(e.root))
	id := histAsk(t, ld, run, true)
	histRespondWith(t, ld, run, id, 1, 0, true, "", func(rc map[string]any) {
		delete(rc, "byName")
		rc["actor"] = ""
	})
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hResponseRef(id, 1)); h.Actor != adminActor || h.Title != id+": Approve" || !strings.HasPrefix(h.Summary, "by "+adminActor+" · ") {
		t.Fatalf("the response: %+v", h.Entry)
	}
	zero := 0
	entries, _ := decisionEntries(runPlace{run: run}, crew.Decision{ID: "dr-9", Run: run, Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: "g1",
		Request:   json.RawMessage(`{"question":"Ship it?","options":[{"label":"Approve"},{"label":"Reject"}]}`),
		Responses: []crew.ResponseReceipt{{Decision: "dr-9", K: 1, Status: ledger.ResponseAccepted, Choice: &zero}}})
	if len(entries) != 2 || entries[1].Actor != "" || entries[1].Title != "dr-9: Approve" || entries[1].Summary != "dr-9 asked: Ship it?" {
		t.Fatalf("a response with no actor: %+v", entries)
	}
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
	if h := histEntry(t, ix, hResponseRef(id, 1)); h.Title != id+": Use Postgres" || !strings.HasPrefix(h.Summary, "by Nate · ") {
		t.Fatalf("the answer the owner recorded: %+v", h.Entry)
	}
}

// An applied approval an ancestor holds, superseded in a transition of
// its continuation, is indexed again. A continuation carries its chain's
// applied approvals as settled and never moves them, so the supersession
// (crew's closeDecision, a next candidate's) writes the decision's row in
// the continuation's transition with the ancestor still holding it: the
// decision's rev moves and the holder's revision does not, and the
// continuation's pass scans only what the continuation holds. Every
// reconcile compares each decision the ledger keeps with its mark by the
// run that holds it and its rev, whatever moved it: the decision is read
// again, its outcome superseded, and the search by outcome finds it.
func TestADecisionSupersededInAContinuationIsIndexedAgain(t *testing.T) {
	e := newTestEnv(t, nil)
	noTick(t, e)
	ld := e.srv.ledger
	const from, to = "hist-000000e6", "hist-000000e7"
	histCreate(t, ld, from, "", histCrew(e.root))
	id := histAsk(t, ld, from, false)
	histRespond(t, ld, from, id, 1, 1, true, "")
	histSettleDecision(t, ld, from, id, ledger.DecisionApplied, &crew.Application{At: histNow, By: "yardmaster", Run: from, Task: "build", Attempt: 1, Response: 1, Validated: true, How: crew.HowTyped, Words: "typed"}, "", "")
	histFinish(t, ld, from)
	histCreate(t, ld, to, from, histCrew(e.root))
	histTx(t, ld, to, func(tx *ledger.Tx) error {
		_, err := tx.MoveDecisions(from, to) // an applied decision stays where it is
		return err
	})
	ix := histReconcile(t, e)
	key := hDecisionRef(id)
	if h := histEntry(t, ix, key); h.Run != from || h.Outcome != ledger.DecisionApplied {
		t.Fatalf("before the supersession: %+v", h.Entry)
	}
	histEntry(t, ix, hRunRef(to))
	n, _ := ledger.ParseDecisionID(id)
	before, _, _ := ld.Pointer(t.Context(), from)
	histSettleDecision(t, ld, to, id, ledger.DecisionSuperseded, nil, "the task's next candidate replaced its subject", "")
	rec, _, err := ld.Decision(t.Context(), n)
	if err != nil || rec.CurrentRun != from || rec.State != ledger.DecisionSuperseded {
		t.Fatalf("the ledger after the supersession: %+v %v", rec.DecisionRow, err)
	}
	if after, _, _ := ld.Pointer(t.Context(), from); after.Revision != before.Revision {
		t.Fatalf("the holder's revision moved: %d, was %d", after.Revision, before.Revision)
	}
	histSettle(t, e) // the continuation's pass, which its commit asked for
	ix = histReconcile(t, e)
	if h := histEntry(t, ix, key); h.Run != from || h.Outcome != ledger.DecisionSuperseded {
		t.Fatalf("after the reconcile: %+v", h.Entry)
	}
	if m := histMark(t, ix, key); !markHolds(m, from, rec.Rev) {
		t.Fatalf("the mark %s, of %s at rev %d", m, from, rec.Rev)
	}
	if keys := hsKeys(hsSearch(t, e, "kind:decision outcome:superseded")); !slices.Equal(keys, []string{key}) {
		t.Fatalf("the superseded decisions: %v", keys)
	}
}

// An answer is linked to what its application receipt says it reached:
// typed into the asking attempt, to that attempt; put in its task's next
// file, to the task, though the receipt keeps the attempt that asked (the
// next attempt, not that one, reads the file).
func TestAnAnswerIsAppliedToWhatItReached(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const typed, filed = "hist-000000e8", "hist-000000e9"
	ids := map[string]string{}
	for run, how := range map[string]string{typed: crew.HowTyped, filed: crew.HowTaskFile} {
		histCreate(t, ld, run, "", histCrew(e.root))
		id := histAsk(t, ld, run, false)
		histRespond(t, ld, run, id, 1, 1, true, "")
		histSettleDecision(t, ld, run, id, ledger.DecisionApplied, &crew.Application{At: histNow, By: "yardmaster", Run: run, Task: "build", Attempt: 1, Response: 1, Validated: true, How: how, Words: how}, "", "")
		ids[run] = id
	}
	ix := histSettle(t, e)
	if got := histOut(t, ix, hResponseRef(ids[typed], 1), relApplied); !slices.Equal(got, []string{attemptKey(typed, "build", 1)}) {
		t.Fatalf("a typed answer applied to: %v", got)
	}
	if got := histOut(t, ix, hResponseRef(ids[filed], 1), relApplied); !slices.Equal(got, []string{hTaskRef(filed, "build")}) {
		t.Fatalf("an answer put in the task's file applied to: %v", got)
	}
}

// The option a response chose is searched whole though its title cuts
// it: a label of 79 characters (DR1 allows 80) that is 215 bytes, chosen
// with no words of its own, is whole at the head of the response's body,
// and a word at its end finds the response.
func TestAChosenOptionIsSearchedWhole(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	const run = "hist-000000ea"
	histCreate(t, ld, run, "", histCrew(e.root))
	label := strings.Repeat("界", 68) + " zebrafinch"
	if n := utf8.RuneCountInString(label); n > ledger.MaxOptionLabel || len(label) <= history.MaxTitle {
		t.Fatalf("the label: %d characters, %d bytes", n, len(label))
	}
	var id string
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginAgent, Type: ledger.DecisionApproval, TaskID: "build", Attempt: 1, SessionID: "s-build-1",
			Member: "lead", RequestID: "q-label", Fingerprint: "fp-label", Policy: histPolicy,
			Request: fmt.Sprintf(`{"question":"Which one?","options":[{"label":"Use Postgres"},{"label":%q}],"subject":{"kind":"commit","commit":%q}}`, label, commitID(1))})
		id = d.ID()
		return err
	})
	histRespond(t, ld, run, id, 1, 1, true, "")
	ix := histSettle(t, e)
	ref := hResponseRef(id, 1)
	h := histEntry(t, ix, ref)
	if strings.Contains(h.Title, "zebrafinch") || !strings.HasPrefix(h.Body, "Chose: "+label) {
		t.Fatalf("the response: title %q, body %q", h.Title, h.Body)
	}
	if keys := hsKeys(hsSearch(t, e, "zebrafinch kind:decision.response")); !slices.Equal(keys, []string{ref}) {
		t.Fatalf("the chosen option's last word: %v", keys)
	}
}
