package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// A capture's send to a decision (PV1 PR-11): a comment on a decision
// record the API makes, kept pending with its text until the decision's
// receipt answers.

var (
	toDecision      = Destination{Kind: SendDecision, Decision: "dr-3"}
	commentDecision = SendFacts{Request: json.RawMessage(`{"decision":"dr-3","comment":"Capture r1 is attached: capture:x/y."}`)}
)

// The store admits a send to a decision: pending with its request, sent
// to the decision it names and no other, listed among the decision's
// sends; a send that names no decision, another kind's fields, no request
// or a decision of the wrong form is refused, and a decision named on
// another kind's send too.
func TestTheStoreAdmitsADecisionSend(t *testing.T) {
	ctx := context.Background()
	s, _ := attachmentTicket(t)
	n, f := testCapture(false)
	id := mustCreate(t, s, "c1", n, f, nil)
	sid := mustSend(t, s, "s1", id, 1, toDecision, commentDecision)
	sv := sendOf(t, s, sid)
	if sv.State != SendPending || sv.Kind != SendDecision || string(sv.Request) != `{"decision":"dr-3","comment":"Capture r1 is attached: capture:x/y."}` {
		t.Fatalf("the send: %+v %s", sv, sv.Request)
	}
	if got := pending(t, s); !slices.Equal(got, []string{sid}) {
		t.Fatalf("pending: %v", got)
	}
	rc, err := s.MarkSent(ctx, testOp("m-other"), sid, "dr-4")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.MarkSent(ctx, testOp("m-br"), sid, "br-3")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, err = s.MarkSent(ctx, testOp("m1"), sid, "dr-3")
	want(t, rc, err, StatusDone, "")
	if sv := sendOf(t, s, sid); sv.State != SendSent || sv.Target != "dr-3" {
		t.Fatalf("sent: %+v", sv)
	}
	if got := sentToIDs(t, s, SendDecision, "dr-3"); !slices.Equal(got, []string{sid}) {
		t.Fatalf("the decision's sends: %v", got)
	}
	if _, err := s.CaptureSends(ctx, SendDecision, "dr-0", "", 10); err == nil {
		t.Fatal("a decision's sends of no decision's id")
	}
	other := mustSend(t, s, "s2", id, 1, toDecision, commentDecision)
	rc, err = s.MarkRefused(ctx, testOp("r1"), other, "dr-3 holds 32 operations")
	want(t, rc, err, StatusDone, "")
	for i, c := range []struct {
		to      Destination
		request string
	}{
		{Destination{Kind: SendDecision}, `{"comment":"x"}`},
		{Destination{Kind: SendDecision, Decision: "dr-0"}, `{"comment":"x"}`},
		{Destination{Kind: SendDecision, Decision: "7"}, `{"comment":"x"}`},
		{Destination{Kind: SendDecision, Decision: "dr-3", Key: "CND-1"}, `{"comment":"x"}`},
		{Destination{Kind: SendDecision, Decision: "dr-3"}, ""},
		{Destination{Kind: SendDecision, Decision: "dr-3"}, `["a list"]`},
		{Destination{Kind: SendBrakeman, Decision: "dr-3"}, `{"question":"x"}`},
		{Destination{Kind: SendTicket, Key: "CND-1", Decision: "dr-3"}, ""},
	} {
		var req SendFacts
		if c.request != "" {
			req.Request = json.RawMessage(c.request)
		}
		rc, err := send(t, s, opID("bad", i), id, 1, c.to, req)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	// A review stays refused until its own row lifts it.
	rc, err = send(t, s, "review", id, 1, Destination{Kind: SendReview}, commentDecision)
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// The store's form of a decision's id is the one Brakeman's grammar and
// the ledger give it.
func TestADecisionsIDIsTheLedgers(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "brakeman", "refs.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("decisionIDPattern += regexp\\.MustCompile\\(`([^`]*)`\\)").FindSubmatch(src)
	if m == nil || string(m[1]) != decisionTarget.String() {
		t.Fatalf("Brakeman's decision id is %q, the store's %q", m, decisionTarget)
	}
}
