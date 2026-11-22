package ledger

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// Counted responses (SW1 PR-9): an eligible approval counted toward a
// router's policy of several places is kept with the status counted while
// the decision is open and unanswered, and leaves it open; an accepted
// response answers it after; none is kept once it is answered.

const anaIdentity = `{"grant":"decide","via":"identity","subject":"ana","boot":"boot-1"}`

// respondAs keeps a response of status to n under auth, in a transition of
// run, and answers its error.
func respondAs(l *Ledger, run string, n int64, id, auth, status string) error {
	return try(l, run, func(tx *Tx) error {
		_, err := tx.Respond(n, ResponseRow{Actor: "user:" + id, Authority: auth, ResponseID: id, Fingerprint: "fp-" + id, Status: status, Receipt: `{"words":"counted"}`})
		return err
	})
}

// A counted response is kept while the decision is open, two of them
// beside each other, and the decision stays open; the accepted response
// after them answers it; a counted one after the answer is refused, as a
// second acceptance is.
func TestACountedResponseLeavesTheDecisionOpen(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, spareGate(run, "g1"))
	if ok, err := putPolicy(l, run, n, routerPolicy, 1); err != nil || !ok {
		t.Fatalf("the policy: %v %v", ok, err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := respondAs(l, run, n, id, anaIdentity, ResponseCounted); err != nil {
			t.Fatalf("the counted response %s: %v", id, err)
		}
	}
	if rec := record(t, l, n); rec.State != DecisionOpen || len(rec.Responses) != 2 || rec.Responses[0].Status != ResponseCounted {
		t.Fatalf("after two counted responses: %s %+v", rec.State, rec.Responses)
	}
	answer(t, l, run, n, "a1", anaIdentity)
	if err := respondAs(l, run, n, "c3", anaIdentity, ResponseCounted); !errors.Is(err, ErrDecisionState) {
		t.Fatalf("a counted response after the answer: %v", err)
	}
	if err := respondAs(l, run, n, "x1", anaIdentity, "maybe"); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("a response of no status the ledger keeps: %v", err)
	}
}

// A routed decision superseded by its replacement with counted responses
// (its rows ran short) is voided, as one whose accepted answer was voided
// is: its replacement stands in its place among the run's decisions.
// Superseded with no response, or with counted ones but no replacement
// (another decision named in its place that does not follow it), it
// counts among them.
func TestACountedDecisionSupersededIsVoided(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	counted := open(t, l, spareGate(run, "g1"))
	bare := open(t, l, spareGate(run, "g2"))
	for _, n := range []int64{counted, bare} {
		if ok, err := putPolicy(l, run, n, routerPolicy, 1); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	newer := open(t, l, spareGate(run, "g3"))
	if ok, err := putPolicy(l, run, newer, routerPolicy, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, n := range []int64{counted, newer} {
		if err := respondAs(l, run, n, fmt.Sprintf("c%d", n), anaIdentity, ResponseCounted); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := replaceSpare(l, run, counted, "g1-again"); err != nil {
		t.Fatal(err)
	}
	setState(t, l, run, bare, DecisionSuperseded, "")
	// Superseded with counted approvals but no replacement (an agent's
	// newer request names another decision that does not follow it): it
	// stays among the run's decisions.
	in(t, l, run, func(tx *Tx) error {
		d, _, err := tx.Decision(newer)
		if err != nil {
			return err
		}
		d.State, d.Rev, d.SupersededBy = DecisionSuperseded, d.Rev+1, DecisionID(bare)
		return tx.PutDecision(d)
	})
	if got := countRows(t, l, "SELECT COUNT(*) FROM decisions d WHERE "+voidedCond); got != 1 {
		t.Fatalf("%d voided, want the counted one its replacement follows, alone", got)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM decisions d WHERE d.n = ? AND "+voidedCond, counted); got != 1 {
		t.Fatalf("the replaced counted decision is not the voided one")
	}
}

// An open decision with no spare holds a place for its replacement from
// its first counted identity response on (the room it was checked for
// then), and its later responses are not checked for room again.
func TestACountedIdentityResponseHoldsAPlace(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	place := "SELECT COUNT(*) FROM decisions d WHERE d.n = ? AND " + placeCond
	if got := countRows(t, l, place, n); got != 0 {
		t.Fatalf("an open decision with no spare holds %d places", got)
	}
	if err := respondAs(l, run, n, "c1", anaIdentity, ResponseCounted); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, l, place, n); got != 1 {
		t.Fatalf("after its counted identity response it holds %d places, want 1", got)
	}
}
