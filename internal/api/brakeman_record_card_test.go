package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Brakeman's card about a decision record (issue 1046, the end-to-end
// plan's step 9): a decision/1 card whose about is an open decision record
// of what its request is about, at the rev its turn read, is the brief of
// the operator's item for that record; a decide principal's page, a link's
// lane and the router are told nothing new, and the record is unchanged.
// Each runs the walk's fixture (workflow_fixture_test.go) to the decision
// given to the decide grant.

// A card about the decision record is the operator's item's brief, marked
// Brakeman's (briefedItem); ana's decide page answers the same bytes as
// before it; a link's lane keeps Conductor's brief on the record's item;
// the router is told nothing more.
func TestABriefCardsADecisionRecordForTheOperatorAlone(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	clock := w.bk.fakeClock()
	w.bk.enable(t, "bk", nil)
	_, viewBefore := w.decide().readAs(t, passAna, w.did)
	routed, resolved := len(w.fakes.router.Decisions()), len(w.fakes.router.Resolutions())

	w.briefedItem(t, clock)

	if status, view := w.decide().readAs(t, passAna, w.did); status != http.StatusOK || !bytes.Equal(view, viewBefore) || bytes.Contains(view, []byte("BRAKEMANMARK")) {
		t.Fatalf("ana's decide page after the card: %d %s, before %s", status, view, viewBefore)
	}
	if r, s := len(w.fakes.router.Decisions()), len(w.fakes.router.Resolutions()); r != routed || s != resolved {
		t.Fatalf("the router was told %d decisions and %d resolutions, before %d and %d", r, s, routed, resolved)
	}
	// A link's lane: the record's item as before, no card; its read too.
	sc := laneScope{run: w.run}
	items, _, _, err := w.srv.laneItems(t.Context(), sc)
	if err != nil {
		t.Fatal(err)
	}
	ref := ledger.DecisionRef(numberOf(w.did))
	found := false
	for _, it := range items {
		if it.Ref != ref {
			continue
		}
		found = true
		if it.BriefBy != "conductor" || it.Card != "" || it.Uncertainty != "" || it.Recommended != -1 || strings.Contains(it.Brief, "BRAKEMANMARK") {
			t.Fatalf("the lane's item for %s: %+v", w.did, it)
		}
	}
	if !found {
		t.Fatalf("the lane lists no item for %s", w.did)
	}
	rec := w.record(t, w.did)
	if frame := w.srv.readRecordItem(t.Context(), sc, "r1", rec); bytes.Contains(frame, []byte("BRAKEMANMARK")) || bytes.Contains(frame, []byte(`"briefBy":"brakeman"`)) {
		t.Fatalf("the lane's read of %s: %s", w.did, frame)
	}
}

// A card about a decision record is refused, its brief not kept and the
// operator's item unchanged, when the record is none, when it is closed,
// when it is another run's than the request's subject, when it names a rev
// the record is not at, and when its request names no subject at all.
func TestADecisionRecordCardIsRefusedOutsideItsRequest(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	w.bk.fakeClock()
	w.bk.enable(t, "bk", nil)
	// A closed decision of the subject's task: asked and withdrawn.
	closed := entityOf(t, w.decide().ask(t, approvalBody("closed-1"), http.StatusCreated, ""), "decision")["id"].(string)
	w.expectAs(t, w.tok, http.MethodPost, "/api/sessions/"+w.sid+"/decisions/"+closed+"/withdraw", map[string]any{}, http.StatusOK, "")
	// An open decision of another run.
	other, release, err := w.srv.runs.LaunchNamed(t.Context(), managedCrew(w.repo), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	osid := runningAttempt(t, w.testEnv, other.ID, "build", 1)["sessionId"].(string)
	otherDid := entityOf(t, w.expectAs(t, w.agentToken(osid), http.MethodPost, "/api/sessions/"+osid+"/decisions", approvalBody("other-1"), http.StatusCreated, ""), "decision")["id"].(string)
	w.assertTruthful(t, "the decisions made")

	itemBefore := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
	id, sid, tok := w.turn(t, brakeman.KindDecision, "task:"+w.run+"/build")
	rev := strconv.Itoa(w.record(t, w.did).Rev)
	evidence := []brakeman.Ref{ref(t, "run:"+w.run)}
	for i, c := range []struct {
		about, rev, why string
	}{
		{"decision:dr-99", "1", "resolves to nothing"},
		{"decision:" + closed, strconv.Itoa(w.record(t, closed).Rev), closed + " is cancelled: a card is about an open decision"},
		{"decision:" + otherDid, "1", "is not of what this request is about"},
		{"decision:" + w.did, "99", "the item names revision 99"},
	} {
		status, out := w.bk.submit(t, sid, tok, fmt.Sprintf("card-%d", i), brakemanBrief("A card", "Brakeman's card.",
			brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
			walkCard(t, ref(t, c.about), c.rev, evidence)))
		if status != http.StatusUnprocessableEntity || errorCode(out) != "bad_refs" || !strings.Contains(fmt.Sprint(out), c.why) {
			t.Fatalf("a card about %s at %s: %d %v", c.about, c.rev, status, out)
		}
	}
	if r := w.bk.waitState(t, id, brakeman.StateRunning); r.HasBrief {
		t.Fatalf("a refused card kept a brief: %+v", r)
	}
	itemAfter := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
	if itemAfter["briefBy"] != "conductor" || itemAfter["brief"] != itemBefore["brief"] || itemAfter["card"] != nil {
		t.Fatalf("the operator's item after the refused cards: %v", itemAfter)
	}
	// The same card at the record's rev is taken: the refusals above were
	// each its own reason, not the turn's.
	if status, out := w.bk.submit(t, sid, tok, "card-ok", brakemanBrief("A card", "Brakeman's card.",
		brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
		walkCard(t, ref(t, "decision:"+w.did), rev, evidence))); status != http.StatusCreated {
		t.Fatalf("the card at the record's rev: %d %v", status, out)
	}
	// A request that names no subject cards no decision record.
	cfg := brakeman.DefaultConfig(w.root)
	b := brakemanBrief("A card", "Brakeman's card.", brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
		walkCard(t, ref(t, "decision:"+w.did), rev, evidence))
	_, err = w.srv.resolveBriefRefs(t.Context(), w.bk.desk.st, brakeman.Request{ID: "br-99", Kind: brakeman.KindBriefing, Config: brakeman.SnapshotOf(cfg)}, b, brakeman.ReadLog{}, cfg)
	if why := badOf(t, err)["decision:"+w.did]; !strings.Contains(why, "is not of what this request is about") {
		t.Fatalf("a briefing's card about %s: %q", w.did, why)
	}
}
