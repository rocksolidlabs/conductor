package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A decision router's rows (RecordRoute) and its answers (RespondDecision
// with the router's authority): docs/plans/decisions.md, `ext` and
// `ext/exttest`, and Owner decisions, 8 (#638, review round 1).

// routersRows is the routed rows of a decision a router wrote.
func routersRows(rec ledger.DecisionRecord) []ledger.HistoryRow {
	var out []ledger.HistoryRow
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryRouted && strings.HasPrefix(h.By, "router:") {
			out = append(out, h)
		}
	}
	return out
}

// The router's row is the second of the opening's two routed rows, never
// one of the operator's operations: written once the operator has spent
// all 32, and the operator's 33rd is still refused. Told the decision
// again (a server that took the ledger over), it is answered the first
// row's receipt, whatever it names this time, and writes nothing.
func TestARoutersRowIsOncePerDecisionAndNoOperation(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	d := ask(t, e, b.Attempt.SessionID, clarification("q1", "Which index?"))
	mirrorOf(t, l, d.ID)
	for i := range ledger.MaxDecisionOperations {
		if _, err := e.CommentDecision(t.Context(), d.ID, fmt.Sprintf("c%d", i), 0, CommentKind, "Noted.", "admin"); err != nil {
			t.Fatalf("the operator's operation %d: %v", i, err)
		}
	}
	rc, err := e.RecordRoute(t.Context(), d.ID, "switchman", []Destination{{Kind: "person", Name: "Ada"}}, "owns the index")
	if err != nil || rc.Repeat || rc.Op != ledger.HistoryRouted || rc.Words != d.ID+" is routed by switchman to person Ada" {
		t.Fatalf("the router's row after the operator's 32 operations: %+v %v", rc, err)
	}
	again, err := e.RecordRoute(t.Context(), d.ID, "switchman", []Destination{{Kind: "role", Name: "release"}}, "another rule")
	if err != nil || !again.Repeat || again.Seq != rc.Seq || again.Words != rc.Words {
		t.Fatalf("the router told again: %+v %v (first %+v)", again, err, rc)
	}
	var de *DecisionError
	if _, err := e.CommentDecision(t.Context(), d.ID, "c-past", 0, CommentKind, "One more.", "admin"); !errors.As(err, &de) || de.Code != CodeDecisionLimit {
		t.Fatalf("the operator's 33rd operation: %v", err)
	}
	rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
	rows := routersRows(rec)
	if len(rows) != 1 || rows[0].OpID != "" || !strings.Contains(rows[0].Detail, `"name":"Ada"`) {
		t.Fatalf("the router's rows: %+v", rows)
	}
	stopRun(t, e, run.ID)
}

// A router's row keeps what the router named at its bounds, as encoded:
// one destination of 60 characters JSON writes six bytes each, a rule of
// 200 such and a provider's name of 40 are kept whole, the words saying
// one destination (naming it too would pass the row's 2 KiB); eight such
// destinations keep those the row holds, the words counting them; no row
// is ever cut.
func TestARoutersRowFitsAtTheBounds(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	by := strings.Repeat("s", 40)
	name, rule := strings.Repeat("<", 60), strings.Repeat("<", 200)
	for _, tc := range []struct {
		request string
		to      int
		words   string
	}{
		{"q1", 1, " is routed by " + by + " to one destination"},
		{"q2", 8, " is routed by " + by + " to 8 destinations, "},
	} {
		d := ask(t, e, b.Attempt.SessionID, clarification(tc.request, "Which index?"))
		mirrorOf(t, l, d.ID)
		to := make([]Destination, tc.to)
		for i := range to {
			to[i] = Destination{Kind: "person", Name: name}
		}
		rc, err := e.RecordRoute(t.Context(), d.ID, by, to, rule)
		if err != nil || !strings.HasPrefix(rc.Words, d.ID+tc.words) {
			t.Fatalf("%d destinations: %+v %v", tc.to, rc, err)
		}
		rec := decisionWhen(t, ld, d.ID, "read", func(ledger.DecisionRecord) bool { return true })
		rows := routersRows(rec)
		if len(rows) != 1 || len(rows[0].Detail) > ledger.MaxHistoryDetail {
			t.Fatalf("%d destinations: the rows %+v", tc.to, rows)
		}
		var dt struct {
			To    []map[string]string `json:"to"`
			Rule  string              `json:"rule"`
			Words string              `json:"words"`
			Cut   bool                `json:"cut"`
		}
		if err := json.Unmarshal([]byte(rows[0].Detail), &dt); err != nil || dt.Cut || dt.Rule != rule || len(dt.To) == 0 || dt.To[0]["name"] != name {
			t.Fatalf("%d destinations: the row %s %v", tc.to, rows[0].Detail, err)
		}
		if tc.to > 1 && dt.Words != fmt.Sprintf("%s is routed by %s to %d destinations, %d of them kept", d.ID, by, tc.to, len(dt.To)) {
			t.Fatalf("the words %q for %d kept", dt.Words, len(dt.To))
		}
	}
	stopRun(t, e, run.ID)
}

// A router's answer is refused (not_approver) and kept, whatever its
// shape and wherever its run is: a line to an approval, and a choice to a
// decision whose run has stopped, each a refused response with its
// responded row; neither is applied, and the operator's answer still is.
func TestARoutersAnswerIsKeptRefused(t *testing.T) {
	dir := newRepo(t)
	e, fl, ld := decisionEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	b, l, _ := attemptOn(t, e, fl, run.ID, "build")
	router := Authority{Via: ViaRouter, Subject: "nate"}
	kept := func(t *testing.T, id string, resp Response) {
		t.Helper()
		rc, err := e.RespondDecision(t.Context(), id, resp, "user:nate", router)
		var de *DecisionError
		if !errors.As(err, &de) || de.Code != CodeNotApprover || rc.Status != "refused" || rc.K == 0 {
			t.Fatalf("the router's answer to %s: %+v %v", id, rc, err)
		}
		rec := decisionWhen(t, ld, id, "the refusal kept", func(r ledger.DecisionRecord) bool { return len(r.Responses) == 1 })
		responded := 0
		for _, h := range rec.History {
			if h.Kind == ledger.HistoryResponded {
				responded++
			}
		}
		if rec.Responses[0].Status != ledger.ResponseRefused || responded != 1 || rec.State != ledger.DecisionOpen {
			t.Fatalf("%s after the router's answer: %+v", id, rec)
		}
	}
	line := ask(t, e, b.Attempt.SessionID, approval("q1", "Merge it?"))
	mirrorOf(t, l, line.ID)
	kept(t, line.ID, Response{ResponseID: RouterResponseID, Text: "ship it", ExpectedRev: 1, ByName: "Nate"})
	stopped := ask(t, e, b.Attempt.SessionID, approval("q2", "Deploy it?"))
	mirrorOf(t, l, stopped.ID)
	stopRun(t, e, run.ID)
	kept(t, stopped.ID, Response{ResponseID: RouterResponseID, Choice: choice(0), ExpectedRev: 1, ByName: "Nate"})
}
