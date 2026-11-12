package crew

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The decide grant in Yardmaster (docs/plans/decide-grant.md): who a
// decision admits (AdmissionOf over EntriesOf), the route that gives it to
// the grant and the one that takes it back, the operation kept for the
// take-back, the response's transaction judging the entries it reads, a
// replacement left to the operator, and the outcome a responder reads.
// Every test waits on the ledger's record, or holds a step at the engine's
// own seam (beforeRespond).

// decideAuth is an identity provider's decide principal's authority.
func decideAuth(e *Engine, subject string) Authority {
	return Authority{Grant: "decide", Via: ViaIdentity, Subject: subject, Boot: e.Boot()}
}

// routeToGrant is the operator's route of id to the decide grant at rev.
func routeToGrant(e *Engine, id, opID string, rev int) (OpReceipt, error) {
	return e.RouteDecision(context.Background(), id, opID, rev, []Destination{{Kind: DestGrant, Grant: "decide"}}, "", "admin")
}

// codeOf is err's refusal code, "" for none.
func codeOf(err error) string {
	var de *DecisionError
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// identityRaw is a response's authority as kept for an identity's decide
// answer, routerRaw a decision router's offered in a person's name.
func identityRaw(subject string) string {
	return `{"grant":"decide","via":"identity","subject":"` + subject + `","boot":"b1"}`
}
func routerRaw(subject string) string {
	return `{"via":"router","subject":"` + subject + `","boot":"b1"}`
}

// grantedRequest is a request as the ledger keeps it, given to the grant
// when given is set.
func grantedRequest(given bool) string {
	if given {
		return `{"question":"Which index?","subject":{"kind":"commit","commit":"abc"},"for":{"label":"grant:decide","person":""}}`
	}
	return `{"question":"Which index?","subject":{"kind":"commit","commit":"abc"}}`
}

// One table: who is admitted to what of a record, by its target, its
// policy, its state and whether the principal has a response of its own.
func TestAdmission(t *testing.T) {
	admin := Admittee{Grants: []string{"admin", "host"}, Via: ViaWorkbench}
	who := map[string]Admittee{
		"admin":            admin,
		"identity admin":   {Grants: []string{"admin", "host"}, Via: ViaIdentity, Subject: "nate"},
		"decide":           {Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"},
		"admin and decide": {Grants: []string{"admin", "host", "decide"}, Via: ViaIdentity, Subject: "ana"},
		"host and decide":  {Grants: []string{"host", "decide"}, Via: ViaIdentity, Subject: "ana"},
		"none":             {Via: ViaIdentity, Subject: "ana"},
		"a router":         {Via: ViaRouter},
		// The admin grant held by any way in but the workbench token and an
		// identity admits nothing, as the operator's entry has it.
		"a router holding admin":            {Grants: []string{"admin", "host"}, Via: ViaRouter, Subject: "ana"},
		"a router holding admin and decide": {Grants: []string{"admin", "decide"}, Via: ViaRouter, Subject: "ana"},
		"an unknown way in holding admin":   {Grants: []string{"admin", "host"}, Via: "share"},
	}
	policies := map[string]string{"local/1": policyJSON, "unreadable": "{", "an unknown kind": `{"revision":"local/1","authority":[{"kind":"subject"}]}`}
	for wname, w := range who {
		for pname, policy := range policies {
			for _, given := range []bool{true, false} {
				for _, state := range []string{ledger.DecisionOpen, ledger.DecisionAnswered, ledger.DecisionApplied, ledger.DecisionCancelled} {
					for _, own := range []bool{true, false} {
						rec := ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, State: state, Policy: policy, Request: grantedRequest(given)}}
						if own {
							rec.Responses = []ledger.ResponseRow{{Actor: "user:ana", Authority: identityRaw("ana"), K: 1, Status: ledger.ResponseRefused}}
						}
						want := AdmitNone
						entry := given && pname != "unreadable"
						decide := wname == "decide" || wname == "host and decide"
						switch {
						case wname == "admin" || wname == "identity admin" || wname == "admin and decide":
							want = AdmitWhole
						case decide && entry && state == ledger.DecisionOpen:
							want = AdmitOpen
						case decide && entry && own:
							want = AdmitOutcome
						}
						if got := AdmissionOf(rec, w); got != want {
							t.Errorf("%s, policy %s, given %v, %s, own %v: %d, want %d", wname, pname, given, state, own, got, want)
						}
					}
				}
			}
		}
	}
	// The entries: the operator's admits the admin grant through the
	// workbench or an identity, never a router; the grant's an identity
	// holding it; an unknown kind nobody.
	op, grant := PolicyEntry{Kind: EntryOperator}, PolicyEntry{Kind: EntryGrant, Grant: "decide"}
	switch {
	case !Admits([]PolicyEntry{op}, admin), !Admits([]PolicyEntry{op}, who["identity admin"]):
		t.Error("the operator's entry refused the admin")
	case Admits([]PolicyEntry{op}, Admittee{Grants: []string{"admin"}, Via: ViaRouter}):
		t.Error("the operator's entry admitted a router")
	case !Admits([]PolicyEntry{grant}, who["decide"]), Admits([]PolicyEntry{grant}, Admittee{Grants: []string{"decide"}, Via: ViaWorkbench}):
		t.Error("the grant's entry is not an identity's holding it")
	case Admits([]PolicyEntry{{Kind: "subject", Grant: "decide"}}, who["decide"]), Admits(nil, admin):
		t.Error("an unknown kind or no entry admitted")
	}
	if got := EntriesOf(ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{Policy: policyJSON, Request: grantedRequest(true)}}); len(got) != 2 || got[1] != grant {
		t.Errorf("the entries of a decision given to the grant: %+v", got)
	}
	// One's own rows are those one sent through an identity: a router's row
	// kept in ana's name, a row whose authority does not decode and another
	// subject's are nobody's of hers.
	for name, auth := range map[string]string{"a router's": routerRaw("ana"), "not JSON": "{", "another subject's": identityRaw("bo")} {
		rec := ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, State: ledger.DecisionApplied, Policy: policyJSON, Request: grantedRequest(true)},
			Responses: []ledger.ResponseRow{{Actor: "user:ana", Authority: auth, K: 1, Status: ledger.ResponseRefused}}}
		if got := AdmissionOf(rec, who["decide"]); got != AdmitNone {
			t.Errorf("%s row: admitted %d", name, got)
		}
		if out, code := OutcomeOf(rec, who["decide"]); out != "" || code != "" {
			t.Errorf("%s row: the outcome %q %q", name, out, code)
		}
	}
}

// grantedDecision is a run of peopleCrew with an open clarification of its
// build attempt: the engine, its ledger and the decision.
func grantedDecision(t *testing.T) (*Engine, *ledger.Ledger, Run, Decision) {
	t.Helper()
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	req := clarification("q1", "Which index?")
	req.For = "role:architecture"
	return e, ld, run, ask(t, e, b.Attempt.SessionID, req)
}

// The route to the grant: open, held, at its rev, the request's for the
// grant (no person), a routed row with for and forWas, rev + 1, the policy
// column byte for byte the opening's; again, a row and rev + 1; refused
// with no row when answered, closed, stale or not held; a route to another
// grant or with a name is invalid; a gate's label naming a grant opens no
// target.
func TestRouteToGrantSetsTheTarget(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	opened := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	rc, err := routeToGrant(e, d.ID, "g1", 1)
	if err != nil || rc.Rev != 2 || rc.Words != d.ID+" is now for anyone holding the decide grant: routed by admin" {
		t.Fatalf("the route to the grant: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, d.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	if f := requestForOf(t, rec.Request); f == nil || *f != (DecisionFor{Label: GrantDecideLabel}) || !GivenToGrant(rec.Request) || requestFor(rec.Request) != nil {
		t.Fatalf("for after the route: %+v", f)
	}
	if rec.Policy != opened.Policy || rec.Policy != policyJSON {
		t.Fatalf("the policy column changed: %s", rec.Policy)
	}
	rows := routedRows(t, rec)
	to, _ := json.Marshal(rows[1]["to"])
	now, _ := json.Marshal(rows[1]["for"])
	was, _ := json.Marshal(rows[1]["forWas"])
	if len(rows) != 2 || string(to) != `[{"kind":"operator"},{"grant":"decide","kind":"grant"}]` || string(now) != `{"label":"grant:decide","person":""}` ||
		string(was) != `{"label":"role:architecture","person":"nate","responsibility":"architecture"}` || rows[1]["rule"] != "local/1: the operator, or anyone holding the decide grant" {
		t.Fatalf("the route's row: %+v", rows)
	}
	if rc, err := routeToGrant(e, d.ID, "g2", 2); err != nil || rc.Rev != 3 {
		t.Fatalf("given again: %+v %v", rc, err)
	}
	for i, bad := range [][]Destination{{{Kind: DestGrant, Grant: "admin"}}, {{Kind: DestGrant, Grant: "host"}}, {{Kind: DestGrant, Grant: "decide", Name: "ana"}}, {{Kind: DestGrant}}, {{Kind: DestOperator, Grant: "decide"}}} {
		if _, err := e.RouteDecision(t.Context(), d.ID, fmt.Sprintf("bad%d", i), 3, bad, "", "admin"); codeOf(err) != CodeInvalid {
			t.Fatalf("a route to %+v: %v", bad, err)
		}
	}
	if _, err := routeToGrant(e, d.ID, "g3", 2); codeOf(err) != CodeStaleDecision {
		t.Fatalf("at a stale rev: %v", err)
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 3})
	done := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if _, err := routeToGrant(e, d.ID, "g4", done.Rev); codeOf(err) != CodeDecisionClosed {
		t.Fatalf("an applied decision: %v", err)
	}
	if n := kindCount(decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied)), ledger.HistoryRouted); n != 3 {
		t.Fatalf("%d routed rows, want the opening's and two routes", n)
	}
	stopRun(t, e, run.ID)

	// Not held: the route to the grant waits for the run.
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run = launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d = ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	if _, err := routeToGrant(e2, d.ID, "g1", 1); codeOf(err) != CodeNotHeld || !strings.Contains(err.Error(), "route it to the grant") {
		t.Fatalf("a route to the grant of a run not held: %v", err)
	}
	if rec := decisionWhen(t, ld2, d.ID, "open", inState(ledger.DecisionOpen)); rec.Rev != 1 || GivenToGrant(rec.Request) {
		t.Fatalf("the refused route changed it: %+v", rec.DecisionRow)
	}
}

// A gate's label never names the grant's target: a label the crew does not
// resolve is kept as given, unless it names a grant.
func TestAGateNeverNamesTheGrant(t *testing.T) {
	if f := (&run{}).gateFor(PackageDecision{For: GrantDecideLabel}); f != nil {
		t.Fatalf("a gate for the grant: %+v", f)
	}
	if f := (&run{}).gateFor(PackageDecision{For: "role:legal"}); f == nil || f.Label != "role:legal" {
		t.Fatalf("a gate's unresolved label: %+v", f)
	}
}

// The take-back: a route to the operator of a decision given to the
// grant, held and not held, writes no for, a row and rev + 1; at a stale
// rev nothing changes; of an answered one, stale at the rev before the
// answer and decision_answered at the answered rev; a route to a person
// replaces the grant.
func TestTakeBackWhileOpen(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := route(e, d.ID, "o1", 1, DestOperator, ""); codeOf(err) != CodeStaleDecision {
		t.Fatalf("a take-back at a stale rev: %v", err)
	}
	rc, err := route(e, d.ID, "o2", 2, DestOperator, "")
	if err != nil || rc.Rev != 3 || rc.Words != d.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the take-back: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, d.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 })
	rows := routedRows(t, rec)
	was, _ := json.Marshal(rows[len(rows)-1]["forWas"])
	if requestForOf(t, rec.Request) != nil || GivenToGrant(rec.Request) || string(was) != `{"label":"grant:decide","person":""}` || rows[len(rows)-1]["for"] != nil {
		t.Fatalf("after the take-back: %s, %+v", rec.Request, rows[len(rows)-1])
	}
	// The operator's route of a decision not given to it is DR1's: no rev.
	if rc, err := route(e, d.ID, "o3", 3, DestOperator, ""); err != nil || rc.Rev != 3 {
		t.Fatalf("a plain route to the operator: %+v %v", rc, err)
	}
	// A route to a person replaces the grant.
	if _, err := routeToGrant(e, d.ID, "g2", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := route(e, d.ID, "p1", 4, DestPerson, "ana"); err != nil {
		t.Fatal(err)
	}
	rec = decisionWhen(t, ld, d.ID, "for ana", func(rec ledger.DecisionRecord) bool { return rec.Rev == 5 })
	if f := requestForOf(t, rec.Request); f == nil || f.Person != "ana" || GivenToGrant(rec.Request) {
		t.Fatalf("after a route to a person: %+v", f)
	}
	stopRun(t, e, run.ID)

	// Not held: the take-back goes through the ledger alone; of an answered
	// decision it is refused, stale or answered.
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run = launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	first := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	second := ask(t, e, b.Attempt.SessionID, clarification("q2", "Which table?"))
	for _, x := range []Decision{first, second} {
		if _, err := routeToGrant(e, x.ID, "g1", 1); err != nil {
			t.Fatal(err)
		}
	}
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	if rc, err := route(e2, first.ID, "o1", 2, DestOperator, ""); err != nil || rc.Rev != 3 {
		t.Fatalf("a take-back through the ledger alone: %+v %v", rc, err)
	}
	if rec := decisionWhen(t, ld2, first.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 }); GivenToGrant(rec.Request) {
		t.Fatalf("still given: %s", rec.Request)
	}
	answer(t, e2, second.ID, Response{ResponseID: "r1", Text: "users", ExpectedRev: 2})
	answered := decisionWhen(t, ld2, second.ID, "answered", inState(ledger.DecisionAnswered))
	if _, err := route(e2, second.ID, "o2", 2, DestOperator, ""); codeOf(err) != CodeStaleDecision {
		t.Fatalf("a take-back at the rev before the answer: %v", err)
	}
	if _, err := route(e2, second.ID, "o3", answered.Rev, DestOperator, ""); codeOf(err) != CodeDecisionAnswered {
		t.Fatalf("a take-back at the answered rev: %v", err)
	}
	if rec := decisionWhen(t, ld2, second.ID, "answered", inState(ledger.DecisionAnswered)); rec.Rev != answered.Rev || !GivenToGrant(rec.Request) {
		t.Fatalf("the refused take-backs changed it: %+v", rec.DecisionRow)
	}
}

// One operation is kept for the take-back: a decision with 30 operations
// takes the route to the grant as its 31st, refuses a comment, and takes
// the take-back as its 32nd; one with 31 refuses the route to the grant.
func TestTakeBackKeepsItsSlot(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	comments := func(id string, n int) int {
		t.Helper()
		for i := range n {
			if _, err := e.CommentDecision(t.Context(), id, fmt.Sprintf("c%d", i), i+1, CommentKind, "noted", "admin"); err != nil {
				t.Fatal(err)
			}
		}
		return n + 1
	}
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	rev := comments(d.ID, 30)
	if _, err := routeToGrant(e, d.ID, "g1", rev); err != nil {
		t.Fatalf("the route to the grant as the 31st: %v", err)
	}
	rev++
	_, err := e.CommentDecision(t.Context(), d.ID, "c-last", rev, CommentKind, "one more", "admin")
	if codeOf(err) != CodeDecisionLimit || !strings.Contains(err.Error(), "kept for taking the decide grant back") {
		t.Fatalf("a comment that would take the last operation: %v", err)
	}
	if _, err := route(e, d.ID, "o1", rev, DestOperator, ""); err != nil {
		t.Fatalf("the take-back as the 32nd: %v", err)
	}
	rec := decisionWhen(t, ld, d.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == rev+1 })
	if GivenToGrant(rec.Request) || kindCount(rec, ledger.HistoryCommented) != 30 {
		t.Fatalf("after the take-back: %s", rec.Request)
	}
	other := ask(t, e, b.Attempt.SessionID, clarification("q2", "Which table?"))
	rev = comments(other.ID, 31)
	if _, err := routeToGrant(e, other.ID, "g1", rev); codeOf(err) != CodeDecisionLimit {
		t.Fatalf("the route to the grant as the 32nd: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A decide principal's response is judged by the entries its transaction
// reads: accepted while the decision is given to the grant, not_approver
// otherwise; a take-back committed while it is held between its look and
// its transaction refuses it not_approver, kept; a response committed
// first makes the take-back stale at the rev both read and answered at
// the answered rev, and the answer is applied once confirmed.
func TestDecideResponseUnderTheTarget(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "a0", Text: "btree", ExpectedRev: 1}, "user:ana", decideAuth(e, "ana")); codeOf(err) != CodeNotApprover {
		t.Fatalf("a decide answer of a decision not given to the grant: %v", err)
	}
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	// Held at beforeRespond while the take-back commits.
	var once sync.Once
	e.beforeRespond = func(int64) {
		once.Do(func() {
			if _, err := route(e, d.ID, "o1", 2, DestOperator, ""); err != nil {
				t.Errorf("the take-back: %v", err)
			}
		})
	}
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "a1", Text: "btree", ExpectedRev: 2}, "user:ana", decideAuth(e, "ana"))
	e.beforeRespond = nil
	if codeOf(err) != CodeNotApprover || rc.Status != "refused" || rc.Kept != nil {
		t.Fatalf("the response held across the take-back: %+v %v", rc, err)
	}
	rec := decisionWhen(t, ld, d.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 })
	if len(rec.Responses) != 2 || rec.Responses[1].Status != ledger.ResponseRefused || rec.State != ledger.DecisionOpen {
		t.Fatalf("the refusal was not kept: %+v", rec.Responses)
	}
	// Committed first: the take-back is stale, then answered.
	if _, err := routeToGrant(e, d.ID, "g2", 3); err != nil {
		t.Fatal(err)
	}
	rc, err = e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "a2", Text: "btree", ExpectedRev: 4}, "user:ana", decideAuth(e, "ana"))
	if err != nil || rc.Status != "accepted" {
		t.Fatalf("the decide answer: %+v %v", rc, err)
	}
	answered := decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered))
	if _, err := route(e, d.ID, "o2", 4, DestOperator, ""); codeOf(err) != CodeStaleDecision {
		t.Fatalf("a take-back at the rev both read: %v", err)
	}
	if _, err := route(e, d.ID, "o3", answered.Rev, DestOperator, ""); codeOf(err) != CodeDecisionAnswered {
		t.Fatalf("a take-back at the answered rev: %v", err)
	}
	e.ConfirmAuthority(d.ID, rc.K, true)
	done := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	if out, _ := OutcomeOf(done, Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}); out != proto.OutcomeApplied {
		t.Fatalf("the outcome: %s", out)
	}
	stopRun(t, e, run.ID)
}

// An identity's answer not confirmed: the replacement's request has no
// for, and a decide principal is not admitted to it; the answered one's
// outcome for its responder is not_used.
func TestReplacementIsTheOperators(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "a1", Text: "btree", ExpectedRev: 2}, "user:ana", decideAuth(e, "ana"))
	if err != nil {
		t.Fatal(err)
	}
	decisionWhen(t, ld, d.ID, "answered", inState(ledger.DecisionAnswered))
	e.ConfirmAuthority(d.ID, rc.K, false)
	old := decisionWhen(t, ld, d.ID, "superseded", inState(ledger.DecisionSuperseded))
	fresh := decisionWhen(t, ld, old.SupersededBy, "open", inState(ledger.DecisionOpen))
	ana := Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}
	if GivenToGrant(fresh.Request) || requestForOf(t, fresh.Request) != nil || AdmissionOf(fresh, ana) != AdmitNone {
		t.Fatalf("the replacement: %s", fresh.Request)
	}
	if out, code := OutcomeOf(old, ana); AdmissionOf(old, ana) != AdmitOutcome || out != proto.OutcomeNotUsed || code != "" {
		t.Fatalf("the answered one's outcome: %s %s", out, code)
	}
	stopRun(t, e, run.ID)
}

// The outcome of one's own answers, from the record whole, never a reason;
// a router's row in one's name is not one's own.
func TestOutcomeOf(t *testing.T) {
	const ana = "user:ana"
	resp := func(actor string, k int, status, code string) ledger.ResponseRow {
		rc, _ := json.Marshal(ResponseReceipt{Status: status, Code: code, Words: "dr-7 was answered by Nate"})
		auth := `{"grant":"admin","via":"workbench","boot":"b1"}`
		if subject, ok := strings.CutPrefix(actor, "user:"); ok {
			auth = identityRaw(subject)
		}
		return ledger.ResponseRow{Actor: actor, Authority: auth, K: k, Status: status, Receipt: string(rc)}
	}
	routers := func(k int) ledger.ResponseRow {
		r := resp(ana, k, ledger.ResponseRefused, CodeNotApprover)
		r.Authority = routerRaw("ana")
		return r
	}
	accepted, refused := ledger.ResponseAccepted, ledger.ResponseRefused
	rec := func(state, applied string, rs ...ledger.ResponseRow) ledger.DecisionRecord {
		return ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, State: state, Applied: applied, Why: "the subject moved"}, Responses: rs}
	}
	eight := []ledger.ResponseRow{resp(ana, 1, accepted, "")}
	for k := 2; k <= 9; k++ {
		eight = append(eight, resp(ana, k, refused, CodeDecisionAnswered))
	}
	for _, tc := range []struct {
		name, want, code string
		rec              ledger.DecisionRecord
	}{
		{"waiting", proto.OutcomeWaiting, "", rec(ledger.DecisionAnswered, "", resp(ana, 1, accepted, ""))},
		{"applied", proto.OutcomeApplied, "", rec(ledger.DecisionApplied, `{"response":1}`, resp(ana, 1, accepted, ""))},
		{"applied, then superseded by a new candidate", proto.OutcomeApplied, "", rec(ledger.DecisionSuperseded, `{"response":1}`, resp(ana, 1, accepted, ""))},
		{"not confirmed", proto.OutcomeNotUsed, "", rec(ledger.DecisionSuperseded, "", resp(ana, 1, accepted, ""))},
		{"its subject moved", proto.OutcomeNotUsed, "", rec(ledger.DecisionCancelled, "", resp(ana, 1, accepted, ""))},
		{"refused, the newest's code", proto.OutcomeRefused, CodeDecisionAnswered,
			rec(ledger.DecisionApplied, `{"response":2}`, resp(ana, 1, refused, CodeStaleDecision), resp("admin", 2, accepted, ""), resp(ana, 3, refused, CodeDecisionAnswered))},
		{"its accepted one after eight refusals", proto.OutcomeWaiting, "", rec(ledger.DecisionAnswered, "", eight...)},
		{"none of its own", "", "", rec(ledger.DecisionApplied, `{"response":1}`, resp("admin", 1, accepted, ""))},
		{"a router's alone", "", "", rec(ledger.DecisionApplied, `{"response":2}`, routers(1), resp("admin", 2, accepted, ""))},
		{"its own beside a router's newer", proto.OutcomeRefused, CodeStaleDecision,
			rec(ledger.DecisionApplied, `{"response":2}`, resp(ana, 1, refused, CodeStaleDecision), resp("admin", 2, accepted, ""), routers(3))},
	} {
		if out, code := OutcomeOf(tc.rec, Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}); out != tc.want || code != tc.code {
			t.Errorf("%s: %q %q, want %q %q", tc.name, out, code, tc.want, tc.code)
		}
	}
}

// A decision router's refused answer kept in ana's name, the decision then
// answered by the operator and applied: ana, holding the decide grant, is
// admitted to nothing of it and has no outcome.
func TestARoutersAnswerIsNotOnesOwn(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	rc, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: RouterResponseID, Text: "btree", ExpectedRev: 2}, "user:ana", Authority{Via: ViaRouter, Subject: "ana", Boot: e.Boot()})
	if codeOf(err) != CodeNotApprover || rc.Kept != nil {
		t.Fatalf("the router's answer: %+v %v", rc, err)
	}
	answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 2})
	done := decisionWhen(t, ld, d.ID, "applied", inState(ledger.DecisionApplied))
	ana := Admittee{Grants: []string{"decide"}, Via: ViaIdentity, Subject: "ana"}
	if len(done.Responses) != 2 || done.Responses[0].Actor != "user:ana" {
		t.Fatalf("the responses kept: %+v", done.Responses)
	}
	if got := AdmissionOf(done, ana); got != AdmitNone {
		t.Fatalf("ana is admitted %d by the router's row", got)
	}
	if out, code := OutcomeOf(done, ana); out != "" || code != "" {
		t.Fatalf("ana's outcome of the router's row: %q %q", out, code)
	}
	stopRun(t, e, run.ID)
}

// failingLook is the engine's ledger with the next decision read made
// under a context marked lookMark failing once, as a busy store's may.
type failingLook struct {
	sqlLedger
	armed *atomic.Bool
}

type lookMark struct{}

func (f failingLook) Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error) {
	if ctx.Value(lookMark{}) != nil && f.armed.CompareAndSwap(true, false) {
		return ledger.DecisionRecord{}, false, errors.New("the ledger is busy (test)")
	}
	return f.sqlLedger.Decision(ctx, n)
}

// A route to the operator whose read of the decision fails answers that
// failure and writes nothing; sent again, it takes the grant back.
func TestARouteAnswersItsFailedRead(t *testing.T) {
	dir := newRepo(t)
	e, fl := readyEngine(t)
	ld, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld.Close() })
	var armed atomic.Bool
	e.UseLedger(failingLook{sqlLedger{l: ld}, &armed})
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	given := decisionWhen(t, ld, d.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	ctx := context.WithValue(t.Context(), lookMark{}, true)
	armed.Store(true)
	rc, err := e.RouteDecision(ctx, d.ID, "o1", 2, []Destination{{Kind: DestOperator}}, "", "admin")
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err == nil || codeOf(err) != "" || armed.Load() || rec.Rev != 2 || !GivenToGrant(rec.Request) || len(rec.History) != len(given.History) {
		t.Fatalf("a route whose read failed: %+v %v; rev %d, given %v, %d history rows", rc, err, rec.Rev, GivenToGrant(rec.Request), len(rec.History))
	}
	rc, err = e.RouteDecision(ctx, d.ID, "o1", 2, []Destination{{Kind: DestOperator}}, "", "admin")
	if err != nil || rc.Rev != 3 || rc.Words != d.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the route sent again: %+v %v", rc, err)
	}
	if rec := decisionWhen(t, ld, d.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 }); GivenToGrant(rec.Request) {
		t.Fatalf("still given: %s", rec.Request)
	}
	stopRun(t, e, run.ID)
}

// A route to the operator that names no rev, sent while a route to the
// grant holds the operations' lock (held at beforeOperate until the second
// has entered operate, enterOperate) is chosen on what its look reads
// under the lock: a decision given to the grant, whose take-back names
// the rev it read, so it is refused invalid.
func TestARouteIsChosenUnderTheLock(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	reached, release, entered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enters, befores atomic.Int32
	e.enterOperate = func(int64) {
		if enters.Add(1) == 2 {
			close(entered)
		}
	}
	e.beforeOperate = func(int64) {
		if befores.Add(1) == 1 {
			close(reached)
			<-release
		}
	}
	type result struct {
		rc  OpReceipt
		err error
	}
	granted, routed := make(chan result, 1), make(chan result, 1)
	go func() { rc, err := routeToGrant(e, d.ID, "g1", 1); granted <- result{rc, err} }()
	<-reached
	go func() { rc, err := route(e, d.ID, "o1", 0, DestOperator, ""); routed <- result{rc, err} }()
	<-entered
	close(release)
	g, o := <-granted, <-routed
	e.enterOperate, e.beforeOperate = nil, nil
	rec := decisionWhen(t, ld, d.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	if g.err != nil || o.err == nil && GivenToGrant(rec.Request) {
		t.Fatalf("the route to the grant %+v %v; the route to the operator %+v %v; given %v", g.rc, g.err, o.rc, o.err, GivenToGrant(rec.Request))
	}
	if codeOf(o.err) != CodeInvalid || !GivenToGrant(rec.Request) || kindCount(rec, ledger.HistoryRouted) != 2 {
		t.Fatalf("the route to the operator with no rev: %v; given %v, %d routed rows", o.err, GivenToGrant(rec.Request), kindCount(rec, ledger.HistoryRouted))
	}
	stopRun(t, e, run.ID)
}

// The row the route's transaction reads judges its choice again: whom the
// decision is for written straight to the ledger between the look and the
// transaction (beforeOperate), given to the grant or taken from it, makes
// the route chosen again from a new look, never committed as the other.
func TestTheRowJudgesTheRoutesChoice(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	n, _ := ledger.ParseDecisionID(d.ID)
	looks := 0
	writeAtFirstLook := func(f *DecisionFor) {
		looks = 0
		e.beforeOperate = func(int64) {
			if looks++; looks > 1 {
				return
			}
			rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
			req, err := withFor(rec.Request, f)
			if err == nil {
				_, err = ld.Transition(context.Background(), rec.CurrentRun, func(tx *ledger.Tx) error { return tx.RouteFor(n, req) })
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Given to the grant since the look: the take-back.
	writeAtFirstLook(&DecisionFor{Label: GrantDecideLabel})
	rc, err := route(e, d.ID, "o1", 1, DestOperator, "")
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || looks != 2 || rc.Rev != 2 || rc.Words != d.ID+" is now for the operator alone: routed by admin" || GivenToGrant(rec.Request) {
		t.Fatalf("given since the look: %+v %v; %d looks, given %v", rc, err, looks, GivenToGrant(rec.Request))
	}
	// Taken from the grant since the look: DR1's route.
	e.beforeOperate = nil
	if _, err := routeToGrant(e, d.ID, "g1", 2); err != nil {
		t.Fatal(err)
	}
	writeAtFirstLook(nil)
	rc, err = route(e, d.ID, "o2", 3, DestOperator, "")
	rec = decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || looks != 2 || rc.Rev != 3 || rc.Words != d.ID+" is routed to the operator" || rec.Rev != 3 || GivenToGrant(rec.Request) {
		t.Fatalf("taken from the grant since the look: %+v %v; %d looks, rev %d, given %v", rc, err, looks, rec.Rev, GivenToGrant(rec.Request))
	}
	e.beforeOperate = nil
	stopRun(t, e, run.ID)
}

// A Brakeman turn's route to the operator of a decision given to the
// grant, sent straight to the engine: refused stale_decision, no row, the
// grant in place; the take-back is the operator's own. Of a decision not
// given to it, a turn's route is DR1's.
func TestATurnLeavesTheGrantToTheOperator(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	_, err := e.RouteDecision(t.Context(), d.ID, "t1", 2, []Destination{{Kind: DestOperator}}, "", ledger.TurnActor)
	rec := decisionWhen(t, ld, d.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	if codeOf(err) != CodeStaleDecision || !GivenToGrant(rec.Request) || kindCount(rec, ledger.HistoryRouted) != 2 {
		t.Fatalf("a turn's take-back: %v; given %v, %d routed rows", err, GivenToGrant(rec.Request), kindCount(rec, ledger.HistoryRouted))
	}
	if _, err := route(e, d.ID, "o1", 2, DestOperator, ""); err != nil {
		t.Fatal(err)
	}
	if rc, err := e.RouteDecision(t.Context(), d.ID, "t2", 3, []Destination{{Kind: DestOperator}}, "", ledger.TurnActor); err != nil || rc.Rev != 3 {
		t.Fatalf("a turn's route of a decision not given: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
}

// The response id a decision router's answer is kept under is a router's
// alone, and a router answers under it alone. A router's answer kept in
// ana's name; ana, holding the decide grant, sends that id with the
// router's body and with another, of a decision given to the grant and of
// one never given: invalid both times, no receipt, nothing kept. The
// router's own repeat is its receipt; a router's answer under another id,
// and the operator's under the router's, are invalid; the operator's
// answer under an id of its own is taken.
func TestTheRouterResponseIDIsTheRouters(t *testing.T) {
	for _, given := range []bool{true, false} {
		e, ld, run, d := grantedDecision(t)
		rev := 1
		if given {
			if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
				t.Fatal(err)
			}
			rev = 2
		}
		router := Authority{Via: ViaRouter, Subject: "ana", Boot: e.Boot()}
		sent := Response{ResponseID: RouterResponseID, Text: "secret router words", ExpectedRev: rev, ByName: "Ana via chat"}
		first, err := e.RespondDecision(t.Context(), d.ID, sent, "user:ana", router)
		if codeOf(err) != CodeNotApprover || first.K != 1 {
			t.Fatalf("given %v: the router's answer: %+v %v", given, first, err)
		}
		kept := decisionWhen(t, ld, d.ID, "the router's answer kept", func(rec ledger.DecisionRecord) bool { return len(rec.Responses) == 1 })
		unchanged := func(why string) {
			t.Helper()
			rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
			if len(rec.Responses) != 1 || len(rec.History) != len(kept.History) || rec.Rev != kept.Rev {
				t.Fatalf("given %v: %s kept something: %d responses, %d history rows, rev %d", given, why, len(rec.Responses), len(rec.History), rec.Rev)
			}
		}
		other := sent
		other.Text = "another body"
		for _, resp := range []Response{sent, other} {
			rc, err := e.RespondDecision(t.Context(), d.ID, resp, "user:ana", decideAuth(e, "ana"))
			if codeOf(err) != CodeInvalid || !strings.Contains(err.Error(), "reserved") || rc.Decision != "" || rc.K != 0 || rc.Repeat || rc.Text != "" {
				t.Fatalf("given %v: ana's send of the router's id (%q): %+v %v", given, resp.Text, rc, err)
			}
			unchanged("ana's send of the router's id")
		}
		if rc, err := e.RespondDecision(t.Context(), d.ID, sent, "user:ana", router); codeOf(err) != CodeNotApprover || !rc.Repeat || rc.K != 1 {
			t.Fatalf("given %v: the router's repeat: %+v %v", given, rc, err)
		}
		if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: "r9", Text: "btree", ExpectedRev: rev}, "user:ana", router); codeOf(err) != CodeInvalid {
			t.Fatalf("given %v: a router's answer under another id: %v", given, err)
		}
		if _, err := e.RespondDecision(t.Context(), d.ID, Response{ResponseID: RouterResponseID, Text: "btree", ExpectedRev: rev}, "admin", workbench(e)); codeOf(err) != CodeInvalid {
			t.Fatalf("given %v: the operator's answer under the router's id: %v", given, err)
		}
		unchanged("a send under the wrong id")
		answer(t, e, d.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: rev})
		if rec := decisionWhen(t, ld, d.ID, "answered", func(rec ledger.DecisionRecord) bool { return len(rec.Responses) == 2 }); rec.Responses[1].ResponseID != "r1" || rec.Responses[1].Status != ledger.ResponseAccepted {
			t.Fatalf("given %v: the operator's answer: %+v", given, rec.Responses)
		}
		stopRun(t, e, run.ID)
	}
}

// The operator is admitted by the operator entry's own rule, before any
// policy is read. Of a decision whose policy names no operator entry its
// answer is taken; of one whose policy does not decode it is refused
// stale_policy. A decide principal is refused not_approver by both.
func TestTheOperatorAnswersByItsOwnRule(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	noOperator := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	unreadable := ask(t, e, b.Attempt.SessionID, clarification("q2", "Which table?"))
	db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	for id, policy := range map[string]string{noOperator.ID: `{"revision":"local/1","authority":[{"kind":"subject"}]}`, unreadable.ID: "{"} {
		n, _ := ledger.ParseDecisionID(id)
		if _, err := db.Exec("UPDATE decisions SET policy = ? WHERE n = ?", policy, n); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for _, id := range []string{noOperator.ID, unreadable.ID} {
		rc, err := e.RespondDecision(t.Context(), id, Response{ResponseID: "d1", Text: "btree", ExpectedRev: 1}, "user:ana", decideAuth(e, "ana"))
		if codeOf(err) != CodeNotApprover || rc.Words != id+" is the operator's to answer: refused under local/1" {
			t.Fatalf("a decide principal's answer to %s: %+v %v", id, rc, err)
		}
	}
	answer(t, e, noOperator.ID, Response{ResponseID: "r1", Text: "btree", ExpectedRev: 1})
	if rec := decisionWhen(t, ld, noOperator.ID, "answered", func(rec ledger.DecisionRecord) bool { return rec.State != ledger.DecisionOpen }); rec.AnsweredAt.IsZero() {
		t.Fatalf("the operator's answer under a policy with no operator entry: %+v", rec.DecisionRow)
	}
	if rc, err := e.RespondDecision(t.Context(), unreadable.ID, Response{ResponseID: "r1", Text: "users", ExpectedRev: 1, ByName: "Nate"}, "admin", workbench(e)); codeOf(err) != CodeStalePolicy {
		t.Fatalf("the operator's answer under a policy that does not decode: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
}

// A route to the operator of a run not held (after a restart) is chosen on
// the record its look reads and judged by the row its transaction reads: a
// failed read answers the error and writes nothing, and the same route sent
// again takes the grant back; whom the decision is for written between the
// look and the transaction (beforeOperate), either way, makes the route
// chosen again from a new look.
func TestTheTakeBackIsChosenOnARunNotHeld(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, fl, ld := decisionEngineAt(t, ldir)
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	failed := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	notGiven := ask(t, e, b.Attempt.SessionID, clarification("q2", "Which table?"))
	given := ask(t, e, b.Attempt.SessionID, clarification("q3", "Which column?"))
	for _, x := range []Decision{failed, given} {
		if _, err := routeToGrant(e, x.ID, "g1", 1); err != nil {
			t.Fatal(err)
		}
	}
	e2, _, ld2 := restart(t, e, ld, ldir, run.ID)
	var armed atomic.Bool
	e2.UseLedger(failingLook{sqlLedger{l: ld2}, &armed})
	operator := []Destination{{Kind: DestOperator}}

	before := decisionWhen(t, ld2, failed.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	ctx := context.WithValue(t.Context(), lookMark{}, true)
	armed.Store(true)
	rc, err := e2.RouteDecision(ctx, failed.ID, "o1", 2, operator, "", "admin")
	rec := decisionWhen(t, ld2, failed.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err == nil || codeOf(err) != "" || armed.Load() || rec.Rev != 2 || !GivenToGrant(rec.Request) || len(rec.History) != len(before.History) {
		t.Fatalf("a route whose read failed: %+v %v; rev %d, given %v, %d history rows", rc, err, rec.Rev, GivenToGrant(rec.Request), len(rec.History))
	}
	rc, err = e2.RouteDecision(ctx, failed.ID, "o1", 2, operator, "", "admin")
	if err != nil || rc.Rev != 3 || rc.Words != failed.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the route sent again: %+v %v", rc, err)
	}
	if rec := decisionWhen(t, ld2, failed.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 3 }); GivenToGrant(rec.Request) {
		t.Fatalf("still given: %s", rec.Request)
	}

	looks := 0
	writeAtFirstLook := func(id string, f *DecisionFor) {
		looks = 0
		n, _ := ledger.ParseDecisionID(id)
		e2.beforeOperate = func(int64) {
			if looks++; looks > 1 {
				return
			}
			rec := decisionWhen(t, ld2, id, "read", func(ledger.DecisionRecord) bool { return true })
			req, err := withFor(rec.Request, f)
			if err == nil {
				_, err = ld2.Transition(context.Background(), rec.CurrentRun, func(tx *ledger.Tx) error { return tx.RouteFor(n, req) })
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Given to the grant since the look: the take-back.
	writeAtFirstLook(notGiven.ID, &DecisionFor{Label: GrantDecideLabel})
	rc, err = e2.RouteDecision(t.Context(), notGiven.ID, "o2", 1, operator, "", "admin")
	rec = decisionWhen(t, ld2, notGiven.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || looks != 2 || rc.Rev != 2 || rc.Words != notGiven.ID+" is now for the operator alone: routed by admin" || GivenToGrant(rec.Request) {
		t.Fatalf("given since the look: %+v %v; %d looks, given %v", rc, err, looks, GivenToGrant(rec.Request))
	}
	// Taken from the grant since the look: DR1's route.
	writeAtFirstLook(given.ID, nil)
	rc, err = e2.RouteDecision(t.Context(), given.ID, "o3", 2, operator, "", "admin")
	rec = decisionWhen(t, ld2, given.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || looks != 2 || rc.Rev != 2 || rc.Words != given.ID+" is routed to the operator" || rec.Rev != 2 || GivenToGrant(rec.Request) {
		t.Fatalf("taken from the grant since the look: %+v %v; %d looks, rev %d, given %v", rc, err, looks, rec.Rev, GivenToGrant(rec.Request))
	}
	e2.beforeOperate = nil
}

// A route whose first look chose DR1's route, whose transaction then found
// the decision given to the grant (written at beforeOperate) and whose
// second look fails: the failure is answered and nothing is written; sent
// again, the route takes the grant back.
func TestARouteAnswersItsSecondFailedRead(t *testing.T) {
	dir := newRepo(t)
	e, fl := readyEngine(t)
	ld, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld.Close() })
	var armed atomic.Bool
	e.UseLedger(failingLook{sqlLedger{l: ld}, &armed})
	run := launchManaged(t, e, peopleCrew(dir))
	b, _, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	n, _ := ledger.ParseDecisionID(d.ID)
	looks := 0
	e.beforeOperate = func(int64) {
		if looks++; looks > 1 {
			return
		}
		rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
		req, err := withFor(rec.Request, &DecisionFor{Label: GrantDecideLabel})
		if err == nil {
			_, err = ld.Transition(context.Background(), rec.CurrentRun, func(tx *ledger.Tx) error { return tx.RouteFor(n, req) })
		}
		if err != nil {
			t.Fatal(err)
		}
		armed.Store(true)
	}
	ctx := context.WithValue(t.Context(), lookMark{}, true)
	rc, err := e.RouteDecision(ctx, d.ID, "o1", 1, []Destination{{Kind: DestOperator}}, "", "admin")
	e.beforeOperate = nil
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err == nil || codeOf(err) != "" || armed.Load() || looks != 1 || rec.Rev != 1 || !GivenToGrant(rec.Request) || kindCount(rec, ledger.HistoryRouted) != 1 {
		t.Fatalf("a route whose second read failed: %+v %v; %d looks, rev %d, given %v, %d routed rows", rc, err, looks, rec.Rev, GivenToGrant(rec.Request), kindCount(rec, ledger.HistoryRouted))
	}
	rc, err = e.RouteDecision(ctx, d.ID, "o1", 1, []Destination{{Kind: DestOperator}}, "", "admin")
	if err != nil || rc.Rev != 2 || rc.Words != d.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the route sent again: %+v %v", rc, err)
	}
	if rec := decisionWhen(t, ld, d.ID, "taken back", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 }); GivenToGrant(rec.Request) {
		t.Fatalf("still given: %s", rec.Request)
	}
	stopRun(t, e, run.ID)
}

// A route's id sent again answers its first receipt, marked a repeat, and
// makes no second act, whatever the decision became since: DR1's route
// after the decision was given to the grant, and the take-back after it
// was given anew. The take-back's id with another body is op_conflict.
func TestARoutesRepeatIsItsFirstReceipt(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	plain, err := route(e, d.ID, "o1", 1, DestOperator, "")
	if err != nil || plain.Rev != 1 || plain.Words != d.ID+" is routed to the operator" {
		t.Fatalf("DR1's route: %+v %v", plain, err)
	}
	if _, err := routeToGrant(e, d.ID, "g1", 1); err != nil {
		t.Fatal(err)
	}
	given := decisionWhen(t, ld, d.ID, "given", func(rec ledger.DecisionRecord) bool { return rec.Rev == 2 })
	again, err := route(e, d.ID, "o1", 1, DestOperator, "")
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || !again.Repeat || again.Rev != plain.Rev || again.Words != plain.Words || rec.Rev != 2 || !GivenToGrant(rec.Request) || len(rec.History) != len(given.History) {
		t.Fatalf("DR1's route again, the decision given: %+v %v; rev %d, given %v", again, err, rec.Rev, GivenToGrant(rec.Request))
	}
	back, err := route(e, d.ID, "o2", 2, DestOperator, "")
	if err != nil || back.Rev != 3 || back.Words != d.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the take-back: %+v %v", back, err)
	}
	if _, err := routeToGrant(e, d.ID, "g2", 3); err != nil {
		t.Fatal(err)
	}
	anew := decisionWhen(t, ld, d.ID, "given anew", func(rec ledger.DecisionRecord) bool { return rec.Rev == 4 })
	again, err = route(e, d.ID, "o2", 2, DestOperator, "")
	rec = decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if err != nil || !again.Repeat || again.Rev != back.Rev || again.Words != back.Words || rec.Rev != 4 || !GivenToGrant(rec.Request) || len(rec.History) != len(anew.History) {
		t.Fatalf("the take-back again, the decision given anew: %+v %v; rev %d, given %v", again, err, rec.Rev, GivenToGrant(rec.Request))
	}
	if _, err := route(e, d.ID, "o2", 4, DestPerson, "ana"); codeOf(err) != CodeOpConflict {
		t.Fatalf("the take-back's id with another body: %v", err)
	}
	stopRun(t, e, run.ID)
}

// A writer that flips whom the decision is for before every try's
// transaction (beforeOperate): the route is refused once its tries are
// spent, in words that say the decision changed, with no row; its id stays
// free, and sent again with nothing writing it is made.
func TestARouteOnATargetThatKeepsChanging(t *testing.T) {
	e, ld, run, d := grantedDecision(t)
	n, _ := ledger.ParseDecisionID(d.ID)
	before := decisionWhen(t, ld, d.ID, "open", inState(ledger.DecisionOpen))
	looks := 0
	e.beforeOperate = func(int64) {
		looks++
		rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
		var f *DecisionFor
		if !GivenToGrant(rec.Request) {
			f = &DecisionFor{Label: GrantDecideLabel}
		}
		req, err := withFor(rec.Request, f)
		if err == nil {
			_, err = ld.Transition(context.Background(), rec.CurrentRun, func(tx *ledger.Tx) error { return tx.RouteFor(n, req) })
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := route(e, d.ID, "o1", 1, DestOperator, "")
	e.beforeOperate = nil
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	if codeOf(err) != CodeDecisionMoved || !strings.Contains(err.Error(), d.ID+" changed while the route was made") || looks != maxDecisionTries ||
		rec.Rev != 1 || kindCount(rec, ledger.HistoryRouted) != kindCount(before, ledger.HistoryRouted) {
		t.Fatalf("a route on a target that kept changing: %v; %d looks, rev %d, %d routed rows", err, looks, rec.Rev, kindCount(rec, ledger.HistoryRouted))
	}
	// Flipped an odd number of times: given, so the route is the take-back.
	if rc, err := route(e, d.ID, "o1", 1, DestOperator, ""); err != nil || rc.Rev != 2 || rc.Words != d.ID+" is now for the operator alone: routed by admin" {
		t.Fatalf("the route sent again: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
}
