package crew

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Whom a decision is for (docs/plans/decisions.md, HM1, PR-12): the label
// a gate or an agent names, resolved in the run's crew as launched, in the
// transaction that opens the decision; a route to a person's label, taken
// only while the decision is open and at the rev read; the receipt, the
// typed line and the task file keeping whom the operator answered for.
// Every test waits on the ledger's record or the engine's state.

// requestForOf is whom a record's request is for.
func requestForOf(t *testing.T, raw string) *DecisionFor {
	t.Helper()
	var body requestBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	return body.For
}

// routedRows are the details of a record's routed rows, in order.
func routedRows(t *testing.T, rec ledger.DecisionRecord) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, h := range rec.History {
		if h.Kind != ledger.HistoryRouted {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(h.Detail), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// gateFor is an approval gate of id on before, for the label.
func gateFor(id, label string, before ...string) PackageDecision {
	return PackageDecision{ID: id, Question: "Is the schema right?", Kind: ledger.DecisionApproval, Before: before, For: label}
}

// A gate names its label: the request keeps the label, the person it
// named and the responsibility, written by the run's first transaction;
// the opening's one routed row names the operator and the person beside
// it, so the history is DR1's two rows; a package whose label the crew
// does not resolve is refused at the launch.
func TestAGateNamesItsLabel(t *testing.T) {
	dir := newRepo(t)
	e, _, ld := decisionEngine(t)
	run := launchGated(t, e, dir, "api-sweep-000000f1", peopleCrew(dir), gateFor("schema", "role:architecture", "build"))
	g := gateOf(t, e, ld, run.ID)
	f := requestForOf(t, string(g.Request))
	if f == nil || *f != (DecisionFor{Label: "role:architecture", Person: "nate", Responsibility: RespArchitecture}) {
		t.Fatalf("the gate's for: %+v", f)
	}
	// The run's first transaction wrote the gate and build's hold: the
	// task's row names it from the start.
	if row := ledgerTask(t, ld, run.ID, "build"); row.State != ledger.TaskReady || row.Reason != "waits for decision "+g.ID {
		t.Fatalf("build's row: %+v", row)
	}
	rec := decisionWhen(t, ld, g.ID, "open", inState(ledger.DecisionOpen))
	if got := historyKinds(rec); !slices.Equal(got, []string{ledger.HistoryOpened, ledger.HistoryRouted}) {
		t.Fatalf("history %q", got)
	}
	routed := routedRows(t, rec)
	to, _ := json.Marshal(routed[0]["to"])
	if string(to) != `[{"kind":"operator"},{"kind":"person","name":"nate"}]` || routed[0]["rule"] != "local/1: the operator answers, for the architecture owner (role:architecture)" {
		t.Fatalf("the routed row: %+v", routed[0])
	}
	if order := eventOrder(t, ld, run.ID); order["run.created"] > order["decision.opened"] {
		t.Fatalf("decision.opened before run.created: %v", order)
	}
	stopRun(t, e, run.ID)

	for _, label := range []string{"role:security", "person:zed", "role:legal"} {
		_, release, err := e.LaunchPlanned(t.Context(), peopleCrew(dir), "CND-12 Fix the login loop", gatedPlan(t, "api-sweep-000000f2", head(t, dir), gateFor("schema", label, "build")))
		release()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), label) {
			t.Fatalf("a gate for %s launched: %v", label, err)
		}
	}
	// The labels travel beside the package, never in it: one naming no
	// decision of the package is refused.
	p := gatedPlan(t, "api-sweep-000000f2", head(t, dir), gateFor("schema", "", "build"))
	if strings.Contains(string(p.Package), `"for"`) {
		t.Fatalf("the package carries for: %s", p.Package)
	}
	p.For = map[string]string{"nope": "role:architecture"}
	_, release, err := e.LaunchPlanned(t.Context(), peopleCrew(dir), "CND-12 Fix the login loop", p)
	release()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("a label of no decision launched: %v", err)
	}
}

// A gate asks again for whom the decision it follows was for, read from
// that decision's record in the ledger: in the run that launched it (the
// launch's label, nate), and in a continuation, whose package carries no
// label, after a route moved its decision to ana (the record's, ana).
func TestAGateAsksAgainForItsLabel(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	run := launchGated(t, e, dir, "api-sweep-000000f6", peopleCrew(dir), gateFor("schema", "role:architecture", "build"))
	reject := func(e *Engine, ld *ledger.Ledger, runID, id string, rev int) {
		t.Helper()
		answer(t, e, id, Response{ResponseID: "r-" + id, Choice: choice(1), ExpectedRev: rev})
		decisionWhen(t, ld, id, "applied", inState(ledger.DecisionApplied))
		waitTask(t, e, runID, "build", ledger.TaskCancelled, 0)
		waitOutcome(t, e, runID, ledger.OutcomeCancelled)
	}
	g := gateOf(t, e, ld, run.ID)
	reject(e, ld, run.ID, g.ID, 1)
	if err := e.RetryTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	fresh := gateOf(t, e, ld, run.ID)
	if f := requestForOf(t, string(fresh.Request)); fresh.ID == g.ID || f == nil || *f != (DecisionFor{Label: "role:architecture", Person: "nate", Responsibility: RespArchitecture}) {
		t.Fatalf("the gate asked again in the run: %s for %+v", fresh.ID, f)
	}
	if _, err := route(e, fresh.ID, "o1", 1, DestPerson, "ana"); err != nil {
		t.Fatal(err)
	}
	decisionWhen(t, ld, fresh.ID, "for ana", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	reject(e, ld, run.ID, fresh.ID, 2)
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	if err := e2.RetryTask(t.Context(), next.ID, "build"); err != nil {
		t.Fatal(err)
	}
	again := gateOf(t, e2, ld2, next.ID)
	if f := requestForOf(t, string(again.Request)); again.ID == fresh.ID || f == nil || *f != (DecisionFor{Label: "person:ana", Person: "ana"}) {
		t.Fatalf("the gate asked again in the continuation: %s for %+v", again.ID, f)
	}
	rec := decisionWhen(t, ld2, again.ID, "open", inState(ledger.DecisionOpen))
	if rows := routedRows(t, rec); len(rows) != 1 || rows[0]["rule"] != "local/1: the operator answers, for ana (person:ana)" {
		t.Fatalf("the routed rows: %+v", rows)
	}
	stopRun(t, e2, next.ID)
}

// A label resolves once, when its decision opens: the run continued, the
// record still names the person the launch's crew named, and the
// continuation resolves nothing again (one routed row). The engine never
// reads a saved crew: a continuation takes the crew from the chain's
// launch row, so the saved crew edited after the approval is
// internal/api's to test (TestTheOperatorAnswersForALabel), where a saved
// crew exists.
func TestALabelResolvesFromTheCrewAsLaunched(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, _, ld := decisionEngineAt(t, ldir)
	run := launchGated(t, e, dir, "api-sweep-000000f3", peopleCrew(dir), gateFor("accept", "role:acceptance", "docs"))
	g := gateOf(t, e, ld, run.ID)
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	next := continueRun(t, e2, run.ID)
	rec := decisionWhen(t, ld2, g.ID, "carried", func(rec ledger.DecisionRecord) bool { return rec.CurrentRun == next.ID })
	if f := requestForOf(t, rec.Request); f == nil || f.Person != "ana" || f.Responsibility != RespAcceptance {
		t.Fatalf("the continued gate's for: %+v", f)
	}
	if rows := routedRows(t, rec); len(rows) != 1 {
		t.Fatalf("the continuation routed it again: %+v", rows)
	}
	stopRun(t, e2, next.ID)
}

// An agent's label no person of the run's crew holds is refused invalid,
// in words that name the crew's people and what they hold, and nothing is
// opened; a label they hold opens the decision for that person.
func TestAnAgentsLabelNoPersonHoldsIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	for _, label := range []string{"role:security", "person:zed", "role:legal", "team:api"} {
		req := clarification("q-"+strings.ReplaceAll(label, ":", "-"), "Which index?")
		req.For = label
		_, _, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, req)
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeInvalid || !strings.Contains(de.Words, "they hold architecture (nate), product (nate), acceptance (ana)") {
			t.Fatalf("a request for %s: %v", label, err)
		}
	}
	if rows, err := ld.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("decisions opened: %+v %v", rows, err)
	}
	req := clarification("q1", "Which index?")
	req.For = "role:product"
	d := ask(t, e, b.Attempt.SessionID, req)
	if f := requestForOf(t, string(d.Request)); f == nil || f.Person != "nate" || f.Responsibility != RespProduct || f.Label != "role:product" {
		t.Fatalf("the request's for: %+v", f)
	}
	rec := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	if rows := routedRows(t, rec); len(rows) != 1 || rows[0]["rule"] != "local/1: the operator answers, for the product owner (role:product)" {
		t.Fatalf("the routed rows: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// An assignment gate for a person: the gate is of DR1's type assignment,
// for ana; Done releases the tasks it holds and Not done cancels them, as
// DR1's gate does; the receipt keeps whom the operator answered for.
func TestAnAssignmentGateForAPerson(t *testing.T) {
	assign := PackageDecision{ID: "migrate", Question: "Run the migration on staging", Kind: ledger.DecisionAssignment, Before: []string{"build"}, For: "person:ana"}
	for _, tc := range []struct {
		run    string
		choice int
		how    string
	}{{"api-sweep-000000f4", 0, HowReleased}, {"api-sweep-000000f5", 1, HowCancelled}} {
		dir := newRepo(t)
		e, _, ld := decisionEngine(t)
		run := launchGated(t, e, dir, tc.run, peopleCrew(dir), assign)
		g := gateOf(t, e, ld, run.ID)
		if g.Type != ledger.DecisionAssignment {
			t.Fatalf("the gate's type %s", g.Type)
		}
		if f := requestForOf(t, string(g.Request)); f == nil || f.Person != "ana" || f.Responsibility != "" {
			t.Fatalf("the gate's for: %+v", f)
		}
		rc := answer(t, e, g.ID, Response{ResponseID: "r1", Choice: choice(tc.choice), ExpectedRev: 1})
		if rc.For == nil || rc.For.Person != "ana" || rc.Actor != "admin" || rc.Words != "Recorded: "+g.ID+" answered by Nate for ana" {
			t.Fatalf("the receipt: %+v %+v", rc, rc.For)
		}
		rec := decisionWhen(t, ld, g.ID, "applied", inState(ledger.DecisionApplied))
		if app := applied(t, rec); app.How != tc.how || !slices.Equal(app.Tasks, []string{"build"}) {
			t.Fatalf("the application: %+v", app)
		}
		if tc.how == HowReleased {
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		} else {
			waitTask(t, e, run.ID, "build", ledger.TaskCancelled, 0)
		}
		stopRun(t, e, run.ID)
	}
}

// route is the operator's route of id to one destination at rev.
func route(e *Engine, id, opID string, rev int, kind, name string) (OpReceipt, error) {
	return e.RouteDecision(context.Background(), id, opID, rev, []Destination{{Kind: kind, Name: name}}, "", "admin")
}

// A route to a person and to a responsibility rewrites whom the decision
// is for, bumps its rev and writes one routed row with the label before
// and after; a responsibility nobody holds, a person not in the crew, an
// unnamed one, another kind and no rev are refused with nothing written;
// the operator's route is DR1's, no rev.
func TestRouteDecisionToAPersonAndARole(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	req := clarification("q1", "Which index?")
	req.For = "role:architecture"
	d := ask(t, e, b.Attempt.SessionID, req)
	// The fingerprint as the opening kept it, read before any route.
	opened := requestFingerprint(t, ld, d.ID)
	rc, err := route(e, d.ID, "o1", 1, DestPerson, "ana")
	if err != nil || rc.Rev != 2 || rc.Words != d.ID+" is now for ana: routed by admin" {
		t.Fatalf("the route to ana: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, d.ID, "for ana", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	if f := requestForOf(t, rec.Request); f == nil || *f != (DecisionFor{Label: "person:ana", Person: "ana"}) {
		t.Fatalf("for after the route: %+v", f)
	}
	rows := routedRows(t, rec)
	if len(rows) != 2 {
		t.Fatalf("routed rows: %+v", rows)
	}
	was, _ := json.Marshal(rows[1]["forWas"])
	now, _ := json.Marshal(rows[1]["for"])
	if string(was) != `{"label":"role:architecture","person":"nate","responsibility":"architecture"}` || string(now) != `{"label":"person:ana","person":"ana"}` {
		t.Fatalf("the route's row: %s %s", was, now)
	}
	if rec.Fingerprint != opened {
		t.Fatalf("the route changed the request's fingerprint: %s, %s at the opening", rec.Fingerprint, opened)
	}
	// The request sent again, as the agent sent it, is still the same
	// request: answered by its decision, none opened.
	if again, created, err := e.RequestDecision(t.Context(), b.Attempt.SessionID, req); err != nil || created || again.ID != d.ID {
		t.Fatalf("the request sent again after the route: %+v created %v %v", again, created, err)
	}
	if rc, err := route(e, d.ID, "o2", 2, DestRole, RespProduct); err != nil || rc.Rev != 3 || rc.Words != d.ID+" is now for nate, the product owner: routed by admin" {
		t.Fatalf("the route to the product owner: %+v %v", rc, err)
	}
	for i, bad := range []Destination{{Kind: DestRole, Name: RespSecurity}, {Kind: DestRole, Name: "legal"}, {Kind: DestPerson, Name: "zed"}, {Kind: DestPerson}, {Kind: "team", Name: "api"}} {
		_, err := e.RouteDecision(t.Context(), d.ID, "bad"+string(rune('a'+i)), 3, []Destination{bad}, "", "admin")
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeInvalid {
			t.Fatalf("a route to %+v: %v", bad, err)
		}
	}
	if _, err := route(e, d.ID, "o3", 0, DestPerson, "ana"); err == nil {
		t.Fatal("a route to a person with no rev was taken")
	}
	if rc, err := route(e, d.ID, "o4", 3, DestOperator, ""); err != nil || rc.Rev != 3 {
		t.Fatalf("the route to the operator: %+v %v", rc, err)
	}
	rec = decisionWhen(t, ld, d.ID, "at rev 3", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 })
	if f := requestForOf(t, rec.Request); f == nil || f.Person != "nate" || f.Responsibility != RespProduct {
		t.Fatalf("for at the end: %+v", f)
	}
	if n := kindCount(rec, ledger.HistoryRouted); n != 4 {
		t.Fatalf("%d routed rows, want the opening's, two routes and the operator's", n)
	}
	stopRun(t, e, run.ID)
}

// requestFingerprint is the decision's fingerprint as its row keeps it.
func requestFingerprint(t *testing.T, ld *ledger.Ledger, id string) string {
	t.Helper()
	n, _ := ledger.ParseDecisionID(id)
	rec, _, err := ld.Decision(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Fingerprint
}

// A route changes whom a decision is for only while it is open: a route to
// ana bumps rev, and a response at the older rev is stale_decision naming
// ana first; one at the new rev is answered for ana, the typed line saying
// so. A route between the response and its application (the run
// interrupted) is refused decision_answered, and the receipt's, the
// application's and the next attempt's task file's for stay nate's; a
// route of an applied decision is refused decision_closed.
func TestARouteChangesForOnlyWhileOpen(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, ld := decisionEngine(t)
		run := launchManaged(t, e, peopleCrew(dir))
		b, _, p := attemptOn(t, e, fl, run.ID, "build")
		req := clarification("q1", "Which index?")
		req.For = "role:architecture"
		d := ask(t, e, b.Attempt.SessionID, req)
		if _, err := route(e, d.ID, "o1", 1, DestPerson, "ana"); err != nil {
			t.Fatal(err)
		}
		rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1, ByName: "Nate"}, "admin", workbench(e))
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeStaleDecision || !strings.HasPrefix(rc.Words, d.ID+" is now for ana: routed by admin: ") {
			t.Fatalf("the response at rev 1: %+v %v", rc, err)
		}
		rc = answer(t, e, d.ID, Response{ResponseID: "r2", Text: "btree", ExpectedRev: 2})
		if rc.For == nil || rc.For.Person != "ana" {
			t.Fatalf("the receipt's for: %+v", rc.For)
		}
		done := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
		lineWith(t, p, "Decision "+d.ID+" (clarification) answered by Nate for ana: btree.")
		_, err = route(e, d.ID, "o2", done.Rev, DestPerson, "nate")
		if !errors.As(err, &de) || de.Code != CodeDecisionClosed {
			t.Fatalf("a route of an applied decision: %v", err)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("answered", func(t *testing.T) {
		dir, ldir := newRepo(t), t.TempDir()
		e, fl, ld := decisionEngineAt(t, ldir)
		run := launchManaged(t, e, peopleCrew(dir))
		b, _, _ := attemptOn(t, e, fl, run.ID, "build")
		req := clarification("q1", "Which index?")
		req.For = "role:architecture"
		d := ask(t, e, b.Attempt.SessionID, req)
		e2, fl2, ld2 := restart(t, e, ld, ldir, run.ID)
		// Open, its run not held: a route to a person waits for the run.
		_, err := route(e2, d.ID, "o0", 1, DestPerson, "ana")
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeNotHeld {
			t.Fatalf("a route while the run is interrupted: %v", err)
		}
		rc := answer(t, e2, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
		if rc.For == nil || rc.For.Person != "nate" || rc.For.Responsibility != RespArchitecture || !strings.HasSuffix(rc.Words, " for nate, the architecture owner") {
			t.Fatalf("the receipt: %+v %+v", rc, rc.For)
		}
		decisionWhen(t, ld2, d.ID, "answered", inState(ledger.DecisionAnswered))
		_, err = route(e2, d.ID, "o1", 2, DestPerson, "ana")
		if !errors.As(err, &de) || de.Code != CodeDecisionAnswered || !strings.Contains(de.Words, "answered by Nate") {
			t.Fatalf("a route of an answered decision: %v", err)
		}
		next := continueRun(t, e2, run.ID)
		rec := decisionWhen(t, ld2, d.ID, "applied", inState(ledger.DecisionApplied))
		if f := requestForOf(t, rec.Request); f == nil || f.Person != "nate" {
			t.Fatalf("for after the refused route: %+v", f)
		}
		b2 := attemptLine(t, e2, fl2, next.ID, "build", 2)
		md, f := taskFileOf(t, b2)
		if td := decisionIn(t, f, d.ID); td.For != "nate, the architecture owner" || td.Answer != "btree" {
			t.Fatalf("the task file's decision: %+v", td)
		}
		if !strings.Contains(md, "Nate for nate, the architecture owner answered: btree.") {
			t.Fatalf("task.md's decisions:\n%s", md)
		}
		stopRun(t, e2, next.ID)
	})
}
