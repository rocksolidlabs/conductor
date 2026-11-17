package ledger

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A comparison's selection (docs/plans/compare-crew.md, PR-6a): a record of
// origin compare, its options' subjects and its comparison subject, in
// DR1's tables as they ship.

// compareRequest is a selection's request as its column keeps it: two
// candidates and None of these, the comparison its subject.
const compareRequest = `{"question":"Which approach does CND-12 continue with?",` +
	`"options":[{"label":"approach-a at 4b825dc","tradeoffs":"checks 2/2 passed","subject":{"kind":"candidate","run":"team-00000001","task":"approach-a","n":1,"commit":"4b825dc"}},` +
	`{"label":"approach-b at 9f8e7d6","subject":{"kind":"candidate","run":"team-00000001","task":"approach-b","n":2,"commit":"9f8e7d6"}},` +
	`{"label":"None of these"}],"recommendation":-1,"authorizes":"Continuing the selected candidate.",` +
	`"subject":{"kind":"comparison","run":"team-00000001","base":"abc123","digest":"d1"}}`

// compareDecision is a selection of run.
func compareDecision(run, request string) DecisionRow {
	return DecisionRow{RunID: run, Origin: OriginCompare, Type: DecisionApproval, Ticket: "CND-12", Fingerprint: "fp-cmp", Request: request, Policy: localPolicy}
}

func TestTheLedgerAdmitsACompareRecord(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "approach-a")

	n := open(t, l, compareDecision(run, compareRequest))
	if got := record(t, l, n); got.Origin != OriginCompare || got.Request != compareRequest || got.TaskID != "" || len(got.Held) != 0 {
		t.Fatalf("the selection was kept as %+v", got.DecisionRow)
	}

	refused := func(name string, d DecisionRow) {
		t.Helper()
		err := try(l, d.RunID, func(tx *Tx) error {
			_, _, err := tx.OpenDecision(d)
			return err
		})
		if !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("%s: %v, want invalid", name, err)
		}
	}
	optionSubject := `{"question":"Ship it?","options":[{"label":"Approve","subject":{"kind":"candidate","run":"team-00000001","task":"approach-a","n":1,"commit":"4b825dc"}},{"label":"Reject"}],` +
		`"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`
	agent := agentDecision(run, "approach-a", 1, "q1")
	agent.Type, agent.Request = DecisionApproval, optionSubject
	refused("an agent's option with a subject", agent)
	gate := gateDecision(run, "g1")
	gate.Request = optionSubject
	refused("a gate's option with a subject", gate)

	comparisonSubject := `{"question":"Which table?","subject":{"kind":"comparison","run":"team-00000001","base":"abc123","digest":"d1"}}`
	other := agentDecision(run, "approach-a", 1, "q2")
	other.Request = comparisonSubject
	refused("a comparison subject on an agent's", other)
	gate = gateDecision(run, "g2")
	gate.Request = strings.Replace(request(DecisionApproval), `{"kind":"plan","plan":"pl-1","digest":"d1"}`, `{"kind":"comparison","run":"team-00000001","base":"abc123","digest":"d1"}`, 1)
	refused("a comparison subject on a gate", gate)

	withTask := compareDecision(run, compareRequest)
	withTask.TaskID, withTask.Attempt = "approach-a", 1
	refused("a selection with an asking task", withTask)
	withGate := compareDecision(run, compareRequest)
	withGate.Gate = "g1"
	refused("a selection with a gate", withGate)
	withMember := compareDecision(run, compareRequest)
	withMember.Member, withMember.SessionID = "cand-a", "s-1"
	refused("a selection with a member and a session", withMember)
	clar := compareDecision(run, compareRequest)
	clar.Type = DecisionClarification
	refused("a selection that is not an approval", clar)
	refused("a selection about a candidate", compareDecision(run, strings.Replace(compareRequest,
		`"subject":{"kind":"comparison","run":"team-00000001","base":"abc123","digest":"d1"}`,
		`"subject":{"kind":"candidate","run":"team-00000001","task":"approach-a","n":1,"commit":"4b825dc"}`, 1)))
	refused("a comparison subject with no base", compareDecision(run, strings.Replace(compareRequest, `"base":"abc123",`, "", 1)))
	refused("a selection that recommends", compareDecision(run, strings.Replace(compareRequest, `"recommendation":-1`, `"recommendation":0`, 1)))
	refused("a selection with no recommendation", compareDecision(run, strings.Replace(compareRequest, `"recommendation":-1,`, "", 1)))
	refused("a selection with evidence", compareDecision(run, strings.Replace(compareRequest, `"recommendation":-1,`, `"recommendation":-1,"evidence":["check:team-00000001/approach-a/1/test/1"],`, 1)))
	refused("an option's subject that is a commit", compareDecision(run, strings.Replace(compareRequest,
		`{"kind":"candidate","run":"team-00000001","task":"approach-b","n":2,"commit":"9f8e7d6"}`, `{"kind":"commit","commit":"9f8e7d6"}`, 1)))
	refused("a selection given to the decide grant", compareDecision(run, strings.Replace(compareRequest, `"recommendation":-1,`, `"recommendation":-1,"for":{"label":"grant:decide","person":""},`, 1)))

	// It holds no task, and whom it is for is never routed.
	if err := try(l, run, func(tx *Tx) error { return tx.Hold(run, "approach-a", n) }); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("a hold of the selection: %v", err)
	}
	given := strings.Replace(compareRequest, `"recommendation":-1,`, `"recommendation":-1,"for":{"label":"person:ana","person":"ana"},`, 1)
	if err := try(l, run, func(tx *Tx) error { return tx.RouteFor(n, given) }); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("a route of the selection: %v", err)
	}
	if got := record(t, l, n); got.Request != compareRequest || len(got.Held) != 0 || got.Rev != 1 {
		t.Fatalf("the refusals changed the selection: %+v", got.DecisionRow)
	}
	if c := countRows(t, l, "SELECT COUNT(*) FROM decisions"); c != 1 {
		t.Fatalf("%d decisions, want the one selection", c)
	}

	// Answered and applied as any record, its receipt naming the candidate.
	answer(t, l, run, n, "r1", workbenchAuth)
	setState(t, l, run, n, DecisionApplied, `{"how":"selected","selected":{"task":"approach-b","run":"team-00000001","attempt":2,"commit":"9f8e7d6"}}`)
	if got := record(t, l, n); got.State != DecisionApplied {
		t.Fatalf("the selection is %s", got.State)
	}
}
