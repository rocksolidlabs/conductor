package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Several approvals at the routes, after a re-route and in what the
// operator reads (SW1 PR-9): approvals that come to fill every place are
// completed by the next eligible approval; an approver moved to another
// group is judged anew; the operator's count includes the approval that
// answered; the operator's read says which counted approvals stand.

// rerouted has the router hold pol for did and re-routes did to it.
func (f *quorumFixture) rerouted(t *testing.T, did string, pol ext.RoutePolicy, opID string) {
	t.Helper()
	f.hold(did, pol)
	res, err := f.routerHost(t).Reroute(t.Context(), ext.RerouteRequest{Decision: did, Route: ext.Route{Policy: &pol}, Why: ext.Rerouted, OpID: opID, ExpectedRev: f.rev(t, did)})
	if err != nil || res.Superseded != "" || res.Unchanged {
		t.Fatalf("the re-route to %s: %+v %v", pol.Revision, res, err)
	}
}

// operatorResponses are did's responses as the operator's read gives them.
func (f *quorumFixture) operatorResponses(t *testing.T, did string) []crew.ResponseReceipt {
	t.Helper()
	status, data, _ := f.call(t, http.MethodGet, "/api/decisions/"+did, nil)
	if status != http.StatusOK {
		t.Fatalf("the operator's read: %d %s", status, data)
	}
	var reply struct {
		Decision crew.Decision `json:"decision"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Decision.Responses
}

// Two groups of one: ana counted; a re-route takes her away; cam counted;
// a re-route names ana again. Both places are filled by the counted
// approvals, the decision open, "2 of 2"; ben's approval answers it, and it
// is applied once, the count 2 of 2 after.
func TestApprovalsBroughtBackByAReRouteAreCompletedAtTheRoutes(t *testing.T) {
	f := newQuorumFixture(t)
	sec := group("security", 1, "cam")
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", false, group("architecture", 1, "ana"), sec))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.rerouted(t, did, groupsPolicy("rev-2", false, group("architecture", 1, "ben"), sec), "op-1")
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptCounted)
	f.rerouted(t, did, groupsPolicy("rev-3", false, group("architecture", 1, "ana", "ben"), sec), "op-2")
	if q := f.quorumOf(t, did); q.Filled != 2 || q.Places != 2 || f.record(t, did).State != ledger.DecisionOpen {
		t.Fatalf("after ana's return: %+v", q)
	}
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
	if q := f.quorumOf(t, did); q.Filled != 2 || !q.Groups[0].Met || !q.Groups[1].Met {
		t.Fatalf("the count once applied: %+v", q)
	}
}

// A re-route moves ana from architecture to security, the only person it
// names there: her fresh approval is counted, her next is already counted
// and takes no row, ben's answers it.
func TestAnApproverMovedByAReRouteIsJudgedAnewAtTheRoutes(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.rerouted(t, did, groupsPolicy("rev-2", true, group("architecture", 1, "ben"), group("security", 1, "ana")), "op-1")
	f.approved(t, "ana", did, "a2", http.StatusCreated, proto.ReceiptCounted)
	rows := len(f.record(t, did).Responses)
	if status, out := f.answerAs(t, "ana", did, "a3", 0); status != http.StatusConflict || errorCode(out) != crew.CodeAlreadyCounted {
		t.Fatalf("ana's third approval: %d %v", status, out)
	}
	if got := len(f.record(t, did).Responses); got != rows {
		t.Fatalf("already counted took a row: %d, was %d", got, rows)
	}
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
}

// The operator's count includes the approval that answered: 2 of 2 once
// answered and once applied. A Reject and an override fill no place: the
// counted approval alone, 1 of 2.
func TestTheCountIncludesTheApprovalThatAnswered(t *testing.T) {
	f := newQuorumFixture(t)
	pol := func(rev string) ext.RoutePolicy {
		return groupsPolicy(rev, true, group("architecture", 1, "ana"), group("security", 1, "cam"))
	}
	did := f.openUnder(t, "r1", "dr-1", pol("rev-1"))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptAccepted)
	if q := f.quorumOf(t, did); q.Filled != 2 || !q.Groups[1].Met {
		t.Fatalf("the count after the last approval: %+v", q)
	}
	f.appliedOnce(t, did)
	if q := f.quorumOf(t, did); q.Filled != 2 || !q.Groups[0].Met || !q.Groups[1].Met {
		t.Fatalf("the count once applied: %+v", q)
	}
	rejected := f.openUnder(t, "r2", "dr-2", pol("rev-2"))
	f.approved(t, "ana", rejected, "a1", http.StatusCreated, proto.ReceiptCounted)
	if status, out := f.answerAs(t, "cam", rejected, "c1", 1); status != http.StatusCreated {
		t.Fatalf("cam's Reject: %d %v", status, out)
	}
	f.appliedOnce(t, rejected)
	if q := f.quorumOf(t, rejected); q.Filled != 1 || q.Groups[1].Met {
		t.Fatalf("the count after a Reject: %+v", q)
	}
	overridden := f.openUnder(t, "r3", "dr-3", pol("rev-3"))
	f.approved(t, "ana", overridden, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.expect(t, http.MethodPost, "/api/decisions/"+overridden+"/respond", operatorBody("o1", 0, f.rev(t, overridden), true, "the release cannot wait for the owners"), http.StatusCreated, "")
	f.appliedOnce(t, overridden)
	if q := f.quorumOf(t, overridden); q.Filled != 1 || q.Groups[1].Met {
		t.Fatalf("the count after an override: %+v", q)
	}
}

// The operator's read says a counted approval that no longer counts: cam's,
// her recheck refusing it, reads refused not_confirmed, asking for another
// answer while the decision is open; ana's counted approval, after it,
// reads counted; once dan's approval answers it, cam's response reads so
// with no "answer again", and her own view's outcome is not used, never
// not_confirmed.
func TestTheOperatorsReadSaysWhichCountedApprovalsStand(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam", "dan")))
	cam := f.pass("cam")
	f.id.recheckAs(cam, ext.Principal{}, false)
	f.approved(t, "cam", did, "c1", http.StatusCreated, proto.ReceiptCounted)
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	byK := func() map[int]crew.ResponseReceipt {
		out := map[int]crew.ResponseReceipt{}
		for _, rc := range f.operatorResponses(t, did) {
			out[rc.K] = rc
		}
		return out
	}
	got := byK()
	if got[2].Status != proto.ReceiptCounted || got[1].Status != proto.ReceiptRefused || got[1].Code != crew.CodeNotConfirmed || !strings.HasSuffix(got[1].Words, ": answer again") {
		t.Fatalf("the operator's read while open: %+v", got)
	}
	if v := f.view(t, cam, did); v.Outcome != proto.OutcomeRefused || v.OutcomeCode != crew.CodeNotConfirmed {
		t.Fatalf("cam's view while open: %+v", v)
	}
	f.approved(t, "dan", did, "d1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
	got = byK()
	if got[1].Status != proto.ReceiptRefused || got[1].Code != crew.CodeNotConfirmed || strings.Contains(got[1].Words, "answer again") || got[2].Status != proto.ReceiptCounted {
		t.Fatalf("the operator's read once applied: %+v", got)
	}
	if v := f.view(t, cam, did); v.Outcome != proto.OutcomeNotUsed || v.OutcomeCode != "" || len(v.Own) != 1 || v.Own[0].Code != crew.CodeNotConfirmed {
		t.Fatalf("cam's view once applied: %+v", v)
	}
	if q := f.quorumOf(t, did); q.Filled != 2 {
		t.Fatalf("the count once applied: %+v", q)
	}
}

// A person whose approval is counted may still Reject: ana's Reject after
// her counted approval is taken and answers at once, rejected, applied.
func TestACountedApproverMayStillReject(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	status, out := f.answerAs(t, "ana", did, "a2", 1)
	if status != http.StatusCreated || out["receipt"].(map[string]any)["status"] != proto.ReceiptAccepted {
		t.Fatalf("ana's Reject: %d %v", status, out)
	}
	f.appliedOnce(t, did)
	if rc := f.receiptOf(t, did, 2); rc.Choice == nil || *rc.Choice != 1 {
		t.Fatalf("the answer: %+v", rc)
	}
}

// An override answers every group at once and waits for no counted
// approval's recheck: with ana's recheck held at the provider, the
// operator's override is applied; ana's approval stays counted.
func TestAnOverrideWaitsForNoCountedRecheck(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "ana"), group("security", 1, "cam")))
	ana := f.pass("ana")
	// The provider's recheck is held at the gate for as long as the test
	// holds it, not the guard's second.
	long := time.Hour
	extWaiting.Store(&long)
	t.Cleanup(func() { extWaiting.Store(nil) })
	gate := f.id.recheckAs(ana, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}}, true)
	body, _ := json.Marshal(map[string]any{"responseId": "a1", "choice": 0, "expectedRev": f.rev(t, did)})
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, f.http.URL+"/api/decisions/"+did+"/respond", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ana)
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.client.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	if got := <-f.id.reached; got != ana {
		t.Fatalf("the recheck held is %s's", got)
	}
	if rec := f.record(t, did); len(rec.Responses) != 1 || rec.Responses[0].Status != ledger.ResponseCounted {
		t.Fatalf("ana's approval before its recheck: %+v", rec.Responses)
	}
	if !f.srv.runs.CountedPending(numberOf(did), 1) {
		t.Fatal("ana's approval does not wait for its recheck")
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/respond", operatorBody("o1", 0, f.rev(t, did), true, "the release cannot wait for the owners"), http.StatusCreated, "")
	f.appliedOnce(t, did)
	select {
	case status := <-done:
		t.Fatalf("ana's recheck answered before the override was applied: %d", status)
	default:
	}
	if !f.srv.runs.CountedPending(numberOf(did), 1) {
		t.Fatal("the override was applied only once ana's recheck had answered")
	}
	close(gate)
	if status := <-done; status != http.StatusCreated {
		t.Fatalf("ana's approval: %d", status)
	}
}

// At the routes: under distinct persons, ana counted for security; a
// re-route names her in architecture too and ben in security; ana's
// approval, widening where she may be placed, is counted; ben's answers.
func TestAnApprovalThatWidensItsPersonsGroupsIsCountedAtTheRoutes(t *testing.T) {
	f := newQuorumFixture(t)
	did := f.openUnder(t, "r1", "dr-1", groupsPolicy("rev-1", true, group("architecture", 1, "cam"), group("security", 1, "ana")))
	f.approved(t, "ana", did, "a1", http.StatusCreated, proto.ReceiptCounted)
	f.rerouted(t, did, groupsPolicy("rev-2", true, group("architecture", 1, "ana"), group("security", 1, "ana", "ben")), "op-1")
	f.approved(t, "ana", did, "a2", http.StatusCreated, proto.ReceiptCounted)
	f.approved(t, "ben", did, "b1", http.StatusCreated, proto.ReceiptAccepted)
	f.appliedOnce(t, did)
	if q := f.quorumOf(t, did); q.Filled != 2 {
		t.Fatalf("the count once applied: %+v", q)
	}
}
