package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A decision router's policy on a decision record (SW1,
// docs/plans/decision-policy.md), in the ledger: the policy's one writer
// after the opening (PutPolicy), the router's history group, the spare and
// its place, and the outcome outbox written in a record's final
// transaction.

const (
	spareLocal = `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}],"spare":true}`
	// routerPolicy is a router's policy as the server writes it.
	routerPolicy = `{"revision":"r1","approver":"router","quorum":1,"authority":[{"kind":"operator"}],"router":"switchman","scope":"s1",` +
		`"pinned":{"rule":"r-1","groups":[{"role":"architecture","quorum":1}],"override":"operator","notify":"now"},` +
		`"groups":[{"role":"architecture","label":"architecture owner","quorum":1,"rule":"r-1","eligible":[{"subject":"ana","name":"Ana","via":"role","ref":"ref-1"}]}],` +
		`"override":"operator","routedAt":"2026-11-19T09:00:00Z","spare":true,"fallback":"local/1"}`
)

// spareGate is a gate opened while a router routes policies: its spare
// held from its opening.
func spareGate(run, gate string) DecisionRow {
	d := gateDecision(run, gate)
	d.Policy = spareLocal
	return d
}

// putPolicy writes policy to n at rev in a transition of run.
func putPolicy(l *Ledger, run string, n int64, policy string, rev int) (bool, error) {
	var ok bool
	err := try(l, run, func(tx *Tx) error {
		var err error
		ok, err = tx.PutPolicy(n, policy, rev)
		return err
	})
	return ok, err
}

// A comparison's selection, which only the operator answers, takes no
// router's policy: PutPolicy refuses it, writing nothing.
func TestPutPolicyRefusesASelection(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, compareDecision(run, compareRequest))
	if _, err := putPolicy(l, run, n, routerPolicy, 1); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("a policy on a selection: %v", err)
	}
	if got := record(t, l, n); got.Policy != localPolicy {
		t.Fatalf("the selection's policy: %s", got.Policy)
	}
}

// PutPolicy writes only while the decision is open and at the rev named:
// a policy after the answer, or at another rev, writes nothing (I16).
func TestPolicyAfterAnswerChangesNothing(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, spareGate(run, "g1"))
	if ok, err := putPolicy(l, run, n, routerPolicy, 2); err != nil || ok {
		t.Fatalf("a policy at another rev: %v %v", ok, err)
	}
	answer(t, l, run, n, "a1", workbenchAuth)
	if ok, err := putPolicy(l, run, n, routerPolicy, record(t, l, n).Rev); err != nil || ok {
		t.Fatalf("a policy after the answer: %v %v", ok, err)
	}
	if got := record(t, l, n); got.Policy != spareLocal {
		t.Fatalf("the policy after the answer: %s", got.Policy)
	}
}

// A router's policy is kept in its canonical form, whatever the spelling
// it was written in (keys escaped, out of order): what SQL reads of one
// member (the spare) is what the decoder reads. A policy of a shape the
// column does not keep (an unknown member, a router's without its router)
// is refused.
func TestPolicyColumnIsCanonical(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(routerPolicy), &m); err != nil {
		t.Fatal(err)
	}
	// The same policy, its spare spelled escaped and put first.
	// The escape is built from its parts, so no tool reads it first.
	escaped := `{"` + "\\" + `u0073pare":true`
	spelled := escaped
	for k, v := range m {
		if k != "spare" {
			spelled += fmt.Sprintf(",%q:%s", k, v)
		}
	}
	spelled += "}"
	if ok, err := putPolicy(l, run, n, spelled, 1); err != nil || !ok {
		t.Fatalf("the policy: %v %v", ok, err)
	}
	got := record(t, l, n).Policy
	if strings.Contains(got, "\\") || !strings.HasPrefix(got, `{"revision":"r1","approver":"router"`) {
		t.Fatalf("the policy kept: %s", got)
	}
	if c := countRows(t, l, "SELECT COUNT(*) FROM decisions WHERE n = ? AND json_extract(policy, '$.spare') = 1", n); c != 1 {
		t.Fatal("SQL does not read the spare the decoder reads")
	}
	for name, bad := range map[string]string{
		"an unknown member": strings.Replace(routerPolicy, `"scope":"s1"`, `"scope":"s1","extra":1`, 1),
		"no router":         strings.Replace(routerPolicy, `"router":"switchman",`, ``, 1),
		"past its bound":    strings.Replace(routerPolicy, `"scope":"s1"`, `"scope":"`+strings.Repeat("s", MaxDecisionPolicy)+`"`, 1),
	} {
		n2 := open(t, l, gateDecision(run, "g-"+strings.ReplaceAll(name, " ", "-")))
		if ok, err := putPolicy(l, run, n2, bad, 1); err == nil || ok {
			t.Errorf("%s: written: %v %v", name, ok, err)
		}
	}
}

// A decision router's changes (assigned, delegated, escalated, rerouted)
// take a group of their own: with the operator's 32 operations spent, 16
// router rows with their op ids are written, a 17th is refused
// decision_limit, an operator's further operation is refused only by the
// operator's own bound, and a router row sent again is answered as a
// repeat.
func TestRouterChangesHaveTheirOwnBound(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	write := func(h HistoryRow) (HistoryRow, bool, error) {
		var got HistoryRow
		var repeat bool
		err := try(l, run, func(tx *Tx) error {
			var err error
			got, repeat, err = tx.History(n, h)
			return err
		})
		return got, repeat, err
	}
	for i := range MaxDecisionOperations {
		if _, _, err := write(HistoryRow{Kind: HistoryCommented, By: "admin", OpID: fmt.Sprintf("c%d", i), Fingerprint: "fp"}); err != nil {
			t.Fatalf("comment %d: %v", i, err)
		}
	}
	kinds := []string{HistoryDelegated, HistoryRerouted}
	for i := range MaxDecisionRouterChanges {
		if _, _, err := write(HistoryRow{Kind: kinds[i%2], By: "router:switchman", OpID: fmt.Sprintf("r%d", i), Fingerprint: "fp"}); err != nil {
			t.Fatalf("router row %d: %v", i, err)
		}
	}
	if _, _, err := write(HistoryRow{Kind: HistoryRerouted, By: "router:switchman", OpID: "r-past", Fingerprint: "fp"}); !errors.Is(err, ErrDecisionLimit) {
		t.Fatalf("a 17th router row: %v", err)
	}
	_, _, err := write(HistoryRow{Kind: HistoryCommented, By: "admin", OpID: "c-past", Fingerprint: "fp"})
	if !errors.Is(err, ErrDecisionLimit) || !strings.Contains(err.Error(), "commented rows") {
		t.Fatalf("a further operator comment: %v", err)
	}
	if got, repeat, err := write(HistoryRow{Kind: HistoryDelegated, By: "router:switchman", OpID: "r0", Fingerprint: "fp"}); err != nil || !repeat || got.OpID != "r0" {
		t.Fatalf("a router row sent again: %+v %v %v", got, repeat, err)
	}
	if ops := countRows(t, l, "SELECT COUNT(*) FROM decision_history WHERE decision = ? AND kind IN ('delegated', 'rerouted')", n); ops != MaxDecisionRouterChanges {
		t.Fatalf("%d router rows", ops)
	}
}

// A record opened while a router routes policies holds its spare from its
// opening: the opening is refused decision_limit when the run's decisions
// and places, its own spare among them, would pass MaxDecisionsPerRun; a
// record without a spare still opens there.
func TestOpeningUnderARouterReservesItsSpare(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	// The run at one place from its bound: its decisions, each closed,
	// made by SQL alone (no writer of the ledger's makes 999 at once): one
	// gate opened, then copies of it.
	first := open(t, l, gateDecision(run, "g0"))
	setState(t, l, run, first, DecisionCancelled, "")
	if _, err := l.db.Exec(`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < ?)
		INSERT INTO decisions (run_id, current_run, origin, type, gate, fingerprint, request, policy, state, rev, created_at, closed_at)
		SELECT run_id, current_run, origin, type, gate || '-' || x, fingerprint || '-' || x, request, policy, state, rev, created_at, closed_at
		FROM decisions, c WHERE n = ?`, MaxDecisionsPerRun-2, first); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM decisions WHERE run_id = ?", run); got != MaxDecisionsPerRun-1 {
		t.Fatalf("the run holds %d decisions", got)
	}
	err := try(l, run, func(tx *Tx) error {
		_, _, err := tx.OpenDecision(spareGate(run, "spare"))
		return err
	})
	if !errors.Is(err, ErrDecisionLimit) {
		t.Fatalf("a spare's opening at the last place: %v", err)
	}
	n := open(t, l, gateDecision(run, "plain"))
	if got := record(t, l, n); got.State != DecisionOpen {
		t.Fatalf("the plain opening: %s", got.State)
	}
}

// outbox is the outbox's rows as their outcomes.
func outbox(t *testing.T, l *Ledger) []Outcome {
	t.Helper()
	rows, err := l.Outcomes(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []Outcome
	for _, r := range rows {
		var o Outcome
		if err := json.Unmarshal([]byte(r.Outcome), &o); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

// A record that carried a router's policy writes its outbox row in the
// transaction that gives it its final state: a failed transaction writes
// none, and a later final state (an applied approval superseded) keeps
// the first. A record under the server's own policy writes none.
func TestOutcomeOutboxWrittenInTheFinalTransaction(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	routed := open(t, l, spareGate(run, "g1"))
	if ok, err := putPolicy(l, run, routed, routerPolicy, 1); err != nil || !ok {
		t.Fatalf("the policy: %v %v", ok, err)
	}
	plain := open(t, l, gateDecision(run, "g2"))
	answer(t, l, run, routed, "a1", `{"grant":"admin","via":"identity","subject":"ana","boot":"boot-1"}`)
	// A final transaction that fails writes nothing.
	boom := errors.New("boom")
	if err := try(l, run, func(tx *Tx) error {
		d, _, _ := tx.Decision(routed)
		d.State, d.Rev, d.Applied = DecisionApplied, d.Rev+1, `{"how":"noted"}`
		if err := tx.PutDecision(d); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got := outbox(t, l); len(got) != 0 {
		t.Fatalf("a failed transaction's outbox: %+v", got)
	}
	setState(t, l, run, routed, DecisionApplied, `{"how":"noted"}`)
	setState(t, l, run, plain, DecisionCancelled, "")
	got := outbox(t, l)
	if len(got) != 1 || got[0].Decision != DecisionID(routed) || got[0].State != DecisionApplied || got[0].By != "ana" || got[0].Via != OutcomeLocal || got[0].RoutedAt != "2026-11-19T09:00:00Z" {
		t.Fatalf("the outbox: %+v", got)
	}
	if got[0].Record.Question != "Ship the migration?" || len(got[0].Record.Options) != 2 || got[0].Record.Run != run || got[0].Record.Gate != "g1" {
		t.Fatalf("the outcome's record: %+v", got[0].Record)
	}
	setState(t, l, run, routed, DecisionSuperseded, `{"how":"noted"}`)
	if got := outbox(t, l); len(got) != 1 || got[0].State != DecisionApplied {
		t.Fatalf("after a later final state: %+v", got)
	}
}

// Every final state has its outcome, answered or not: a routed record
// cancelled while open (no one decided it: By and Via empty) and one
// superseded with the decision that replaced it.
func TestOutboxCarriesCancellationsAndSupersessions(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	a := open(t, l, spareGate(run, "g1"))
	b := open(t, l, spareGate(run, "g2"))
	for _, n := range []int64{a, b} {
		if ok, err := putPolicy(l, run, n, routerPolicy, 1); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	setState(t, l, run, a, DecisionCancelled, "")
	in(t, l, run, func(tx *Tx) error {
		d, _, _ := tx.Decision(b)
		d.State, d.Rev, d.Why, d.SupersededBy = DecisionSuperseded, d.Rev+1, "its subject moved", "dr-99"
		return tx.PutDecision(d)
	})
	got := outbox(t, l)
	if len(got) != 2 {
		t.Fatalf("the outbox: %+v", got)
	}
	if got[0].State != DecisionCancelled || got[0].By != "" || got[0].Via != "" {
		t.Fatalf("the cancellation: %+v", got[0])
	}
	if got[1].State != DecisionSuperseded || got[1].SupersededBy != "dr-99" || got[1].Why != "its subject moved" || got[1].By != "" {
		t.Fatalf("the supersession: %+v", got[1])
	}
}

// An outbox row is self-contained and has no foreign key: the run pruned,
// its row stays, whole.
func TestOutcomeSurvivesAPrunedRun(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, spareGate(run, "g1"))
	if ok, err := putPolicy(l, run, n, routerPolicy, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	setState(t, l, run, n, DecisionCancelled, "")
	finish(t, l, run)
	if err := l.Prune(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Decision(context.Background(), n); ok {
		t.Fatal("the pruned run's decision stayed")
	}
	got := outbox(t, l)
	if len(got) != 1 || got[0].Decision != DecisionID(n) || got[0].Record.Question == "" || len(got[0].Policy) == 0 {
		t.Fatalf("the outbox after the prune: %+v", got)
	}
}

// The fullest outcome (a policy at MaxDecisionPolicy, a question and
// options escaping as JSON does at their bounds, the longest why) is
// within MaxDecisionOutcome: a final transaction is never refused for its
// outbox row.
func TestTheFullestOutcomeFitsItsBound(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	d := spareGate(run, "g1")
	var opts []string
	for i := range MaxDecisionOptions {
		opts = append(opts, fmt.Sprintf(`{"label":%q}`, strings.Repeat("<", MaxOptionLabel-1)+string(rune('a'+i))))
	}
	d.Request = `{"question":"` + strings.Repeat("<", MaxDecisionQuestion) + `","options":[` + strings.Join(opts, ",") + `],"authorizes":"deploy","subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`
	n := open(t, l, d)
	pad := MaxDecisionPolicy - len(routerPolicy)
	full := strings.Replace(routerPolicy, `"scope":"s1"`, `"scope":"`+strings.Repeat("s", pad+2)+`"`, 1)
	if len(full) != MaxDecisionPolicy {
		t.Fatalf("the policy is %d bytes", len(full))
	}
	if ok, err := putPolicy(l, run, n, full, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	in(t, l, run, func(tx *Tx) error {
		row, _, _ := tx.Decision(n)
		row.State, row.Rev, row.Why = DecisionCancelled, row.Rev+1, strings.Repeat("<", MaxDecisionWhy)
		return tx.PutDecision(row)
	})
	rows, err := l.Outcomes(context.Background(), 0, 10)
	if err != nil || len(rows) != 1 || len(rows[0].Outcome) > MaxDecisionOutcome {
		t.Fatalf("the fullest outcome: %d rows %v", len(rows), err)
	}
}
