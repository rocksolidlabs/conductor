package api

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
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
// Brakeman's (briefedItem); ana's decide page and her list, a link's lane
// (its item for the record and its read of it) answer the same bytes after
// the card as before it; the router is told nothing more.
func TestABriefCardsADecisionRecordForTheOperatorAlone(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	clock := w.bk.fakeClock()
	w.bk.enable(t, "bk", nil)
	sc := laneScope{run: w.run}
	ref := ledger.DecisionRef(numberOf(w.did))
	// What every reader but the operator reads, before the card.
	reads := func() map[string][]byte {
		t.Helper()
		_, view := w.decide().readAs(t, passAna, w.did)
		list, _ := w.decide().list(t, passAna)
		items, _, _, err := w.srv.laneItems(t.Context(), sc)
		if err != nil {
			t.Fatal(err)
		}
		var lane []byte
		for _, it := range items {
			if it.Ref == ref {
				lane = decisionJSON(t, it)
			}
		}
		if lane == nil {
			t.Fatalf("the lane lists no item for %s", w.did)
		}
		return map[string][]byte{"ana's decide page": view, "ana's list": list, "the link's item": lane,
			"the link's read": w.srv.readRecordItem(t.Context(), sc, "r1", w.record(t, w.did))}
	}
	before := reads()
	routed, resolved := len(w.fakes.router.Decisions()), len(w.fakes.router.Resolutions())

	w.briefedItem(t, clock)

	for what, now := range reads() {
		if !bytes.Equal(now, before[what]) || bytes.Contains(now, []byte("BRAKEMANMARK")) {
			t.Fatalf("%s after the card:\n%s\nbefore:\n%s", what, now, before[what])
		}
	}
	if r, s := len(w.fakes.router.Decisions()), len(w.fakes.router.Resolutions()); r != routed || s != resolved {
		t.Fatalf("the router was told %d decisions and %d resolutions, before %d and %d", r, s, routed, resolved)
	}
}

// A card about a decision record is refused, its brief not kept and the
// operator's item unchanged, when the record is none, when it is another
// run's than the request's subject (in the same words as none, nothing of
// its state or revision said, at a wrong rev too), when it is closed, when
// it names a rev the record is not at, and when its request names no
// subject at all.
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
	words := map[int]string{}
	for i, c := range []struct {
		about, rev, why string
	}{
		{"decision:dr-99", "1", "resolves to nothing"},
		{"decision:" + otherDid, "1", "resolves to nothing"},
		{"decision:" + otherDid, "99", "resolves to nothing"},
		{"decision:" + closed, strconv.Itoa(w.record(t, closed).Rev), closed + " is cancelled: a card is about an open decision"},
		{"decision:" + w.did, "99", "the item names revision 99"},
	} {
		status, out := w.bk.submit(t, sid, tok, fmt.Sprintf("card-%d", i), brakemanBrief("A card", "Brakeman's card.",
			brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
			walkCard(t, ref(t, c.about), c.rev, evidence)))
		if status != http.StatusUnprocessableEntity || errorCode(out) != "bad_refs" || !strings.Contains(fmt.Sprint(out), c.why) {
			t.Fatalf("a card about %s at %s: %d %v", c.about, c.rev, status, out)
		}
		words[i] = strings.ReplaceAll(string(decisionJSON(t, out["error"])), c.about, "<record>")
	}
	// One not there and one of another run are refused in the same words.
	if words[0] != words[1] || words[1] != words[2] {
		t.Fatalf("the refusals' words differ:\n%s\n%s\n%s", words[0], words[1], words[2])
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
	if why := badOf(t, err)["decision:"+w.did]; why != "resolves to nothing" {
		t.Fatalf("a briefing's card about %s: %q", w.did, why)
	}
}

// A request about one attempt admits a card about a record that attempt
// asked, and about no other attempt's record of the same task (answered
// as one not there); a request whose subject is the record (a person asks
// Brakeman about it) admits a card about that record.
func TestADecisionRecordCardIsOfItsAttemptOrItsRecord(t *testing.T) {
	w := newWorkflow(t, false)
	w.toDecision(t)
	clock := w.bk.fakeClock()
	w.bk.enable(t, "bk", nil)
	first := w.did
	// Attempt 1's session ends; the operator answers; attempt 2 asks dr-2.
	d, ok := w.srv.registry.Get(w.sid)
	if !ok {
		t.Fatalf("no session %s", w.sid)
	}
	if err := d.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.decisionWhen(t, first, "its attempt given back", func(d map[string]any) bool {
		return slices.ContainsFunc(historyOf(d), func(h string) bool { return strings.HasPrefix(h, ledger.HistoryRefunded+" ") })
	})
	w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, "")
	w.applied(t, first)
	a := runningAttempt(t, w.testEnv, w.run, "build", 2)
	w.sid = a["sessionId"].(string)
	w.tok = w.agentToken(w.sid)
	second := entityOf(t, w.decide().ask(t, w.raiseBody("arch-2"), http.StatusCreated, ""), "decision")["id"].(string)
	w.did = second
	w.assertTruthful(t, "attempt 2's decision")

	evidence := []brakeman.Ref{ref(t, "run:"+w.run)}
	brief := func(about string) brakeman.Brief {
		return brakemanBrief("A card", "Brakeman's card.", brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
			walkCard(t, ref(t, about), strconv.Itoa(w.record(t, strings.TrimPrefix(about, "decision:")).Rev), evidence))
	}
	// About attempt 2: attempt 1's record is not of it (and is applied:
	// nothing of that is said); attempt 2's is.
	id, sid, tok := w.turn(t, brakeman.KindExplain, fmt.Sprintf("attempt:%s/build/2", w.run))
	status, out := w.bk.submit(t, sid, tok, "card-first", brief("decision:"+first))
	if status != http.StatusUnprocessableEntity || errorCode(out) != "bad_refs" || !strings.Contains(fmt.Sprint(out), "resolves to nothing") || strings.Contains(fmt.Sprint(out), "applied") {
		t.Fatalf("attempt 1's record on a request about attempt 2: %d %v", status, out)
	}
	w.briefed(t, clock, id, sid, tok, brief("decision:"+second))
	// About the record itself: a person asks Brakeman about dr-2.
	status, req := w.bk.ask(t, brakeman.KindAsk, map[string]any{"subjects": []string{ledger.DecisionRef(numberOf(second))}, "note": "Should the build keep the cookie?"})
	if status != http.StatusCreated {
		t.Fatalf("the ask about %s: %d %v", second, status, req)
	}
	id = req["id"].(string)
	sid, tok, _ = w.bk.turnSession(t, id)
	w.bk.waitState(t, id, brakeman.StateRunning)
	w.briefed(t, clock, id, sid, tok, brief("decision:"+second))
	it := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(second)), nil, http.StatusOK, ""), "item")
	if it["briefBy"] != "brakeman" || !strings.HasPrefix(fmt.Sprint(it["card"]), "brief:"+id+"/") {
		t.Fatalf("the operator's item for %s: %v", second, it)
	}
}
