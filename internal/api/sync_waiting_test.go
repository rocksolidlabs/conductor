package api

import (
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// While a held answer's recheck cannot be made (the identity provider
// cannot say now) the decision's view says whose answer waits and why;
// once the provider says yes it is applied and says nothing more.
func TestTheDecisionSaysItWaitsToRecheck(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(func(ext.AnswerCheck, ext.Grant) ext.Admission {
		if len(f.id.asks()) == 1 {
			return ext.AdmitYes // the door's
		}
		return ext.AdmitUnknown
	})
	f.hand(t, held(did, "h1", "ana", 0, 1))
	var wait chan time.Time
	select {
	case wait = <-f.waits:
	case <-time.After(testWait):
		t.Fatal("the recheck never waited to ask again")
	}
	d := f.decisionWhen(t, did, "saying it waits", func(d map[string]any) bool { return d["waiting"] != nil })
	if w, _ := d["waiting"].(string); w != "waiting to recheck user:ana's answer: the identity provider cannot say now" || d["state"] != ledger.DecisionAnswered {
		t.Fatalf("the decision: %v", d)
	}
	f.id.set(always(ext.AdmitYes))
	wait <- time.Now()
	d = f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	if d["waiting"] != nil {
		t.Fatalf("an applied decision still says %q", d["waiting"])
	}
	if strings.Contains(fmtDecisions(t, f), "waiting to recheck") {
		t.Fatal("the decisions' list says it still waits")
	}
}

// fmtDecisions is the decisions' list as text.
func fmtDecisions(t *testing.T, f *heldFixture) string {
	t.Helper()
	_, b, _ := f.call(t, "GET", "/api/decisions", nil)
	return string(b)
}
