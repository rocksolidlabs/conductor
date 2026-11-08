package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// DR1's records in the decision inbox (docs/plans/decision-inbox.md,
// PR-3): the record as an item, its actions through DR1's routes, its
// four states as the record reports them, the answered ones tracked,
// Recent, the record on a decision link without actions and in parts of
// one rev. Each test waits on the record (decisionWhen) or on the item
// the inbox reads, never on a clock.

// decisionTrackOf reads the four steps of an action on a record, by name:
// available, recorded, validated and applied.
func (e *testEnv) decisionTrackOf(t *testing.T, did, receipt string) map[string]deliveryStep {
	t.Helper()
	status, body := e.get(t, "/api/inbox/track?"+url.Values{"ref": {"decision:" + did}, "receipt": {receipt}}.Encode())
	if status != http.StatusOK || len(body) > maxInboxTrack {
		t.Fatalf("track %s %s: %d %s", did, receipt, status, body)
	}
	var out struct {
		Steps []deliveryStep `json:"steps"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return stepsByName(t, out.Steps)
}

// stepsByName is a record's four steps by name, failing t unless they are
// the four in order.
func stepsByName(t *testing.T, steps []deliveryStep) map[string]deliveryStep {
	t.Helper()
	if len(steps) != 4 {
		t.Fatalf("steps: %+v", steps)
	}
	m := map[string]deliveryStep{}
	for i, s := range steps {
		if want := []string{stepAvailable, stepRecorded, stepValidated, stepApplied}[i]; s.Step != want {
			t.Fatalf("step %d is %s: %+v", i, s.Step, steps)
		}
		m[s.Step] = s
	}
	return m
}

// stateOf says each step's state, in order.
func stateOf(m map[string]deliveryStep) string {
	return strings.Join([]string{m[stepAvailable].State, m[stepRecorded].State, m[stepValidated].State, m[stepApplied].State}, " ")
}

// mirrored waits for the attempt's session to show the record did as its
// question, and answers the question's id.
func (f *decisionFixture) mirrored(t *testing.T, did string) string {
	t.Helper()
	var q string
	waitUntil(t, "the session mirrors "+did, func() bool {
		d, _ := f.srv.registry.Get(f.sid)
		a := d.Info().Attention
		q = a.ID
		return a.State == session.AttentionNeedsInput && a.Decision == did
	})
	return q
}

// clarificationBody is a clarification with two options under the id.
func clarificationBody(id string) map[string]any {
	return map[string]any{"requestId": id, "type": "clarification", "question": "Which table for " + id + "?",
		"options": []any{map[string]any{"label": "users"}, map[string]any{"label": "accounts"}}}
}

// A question that mirrors a record is the record's item: listed once, as
// the record, its actions DR1's; the question's own item is still read by
// its ref, with no answer. Applied, the record leaves the list for Recent,
// its four states done.
func TestADecisionRecordReplacesItsQuestion(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	q := f.mirrored(t, "dr-1")
	page, _ := f.inboxList(t, "")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Ref != "decision:dr-1" || page.Items[0].Kind != inboxDecision || page.Items[0].Tracking || page.Tracking != 0 {
		t.Fatalf("the list: %+v", page)
	}
	if it := f.itemOf(t, questionRef(f.sid, q)); len(it.Actions) != 0 {
		t.Fatalf("the question's own item: %+v", it)
	}
	it := f.itemOf(t, "decision:dr-1")
	if got := actionNames(it); !slices.Equal(got, []string{"approve", "reject", "more_info", "comment"}) {
		t.Fatalf("the actions: %v", got)
	}
	for i, a := range it.Actions[:2] {
		if a.Choice == nil || *a.Choice != i || a.Expect != "1" || a.Route != "POST /api/decisions/dr-1/respond" || !a.Confirm || !strings.Contains(a.Scope, "the deploy") {
			t.Fatalf("action %d: %+v", i, a)
		}
	}
	if it.State != ledger.DecisionOpen || it.Revision != "1" || it.Question != "Ship build r1?" || !slices.Contains(it.Blocking.Refs, "task:"+f.run+"/build") {
		t.Fatalf("the item: %+v", it)
	}
	if got := stateOf(stepsByName(t, it.Delivery)); got != "done waiting waiting waiting" {
		t.Fatalf("its four states: %s", got)
	}
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	page, _ = f.inboxList(t, "")
	if page.Total != 0 || len(page.Recent) != 1 || page.Recent[0].Ref != "decision:dr-1" || page.Recent[0].State != ledger.DecisionApplied || page.Recent[0].Closed == nil {
		t.Fatalf("after its application: %+v", page)
	}
	it = f.itemOf(t, "decision:dr-1")
	if len(it.Actions) != 0 || !strings.Contains(it.Now, "dr-1 is applied") {
		t.Fatalf("the item applied: %+v", it)
	}
	steps := stepsByName(t, it.Delivery)
	if stateOf(steps) != "done done done done" || steps[stepRecorded].Receipt != "dr-1/1" {
		t.Fatalf("its four states applied: %+v", steps)
	}
	if got := f.decisionTrackOf(t, "dr-1", "a1"); !reflect.DeepEqual(got, steps) {
		t.Fatalf("the response's track: %+v", got)
	}
}

// Each action names DR1's route and the record's rev: more information
// and a comment through the comment operation, each with its id, a repeat
// answered with the first receipt; an answer, an approve and a reject
// through the respond operation, a repeat the same; each one's steps read
// back by its id.
func TestInboxDecisionActionsUseDR1sRoutes(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("c1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	it := f.itemOf(t, "decision:dr-1")
	if got := actionNames(it); !slices.Equal(got, []string{"answer", "answer", "answer", "more_info", "comment"}) {
		t.Fatalf("a clarification's actions: %v", got)
	}
	if a := it.Actions[2]; !a.Text || a.Choice != nil || a.Route != "POST /api/decisions/dr-1/respond" {
		t.Fatalf("its line: %+v", a)
	}
	for _, name := range []string{"more_info", "comment"} {
		if a := actionOf(t, it, name); !a.Text || a.Route != "POST /api/decisions/dr-1/comment" || a.Expect != "1" {
			t.Fatalf("%s: %+v", name, a)
		}
	}
	more := map[string]any{"opId": "m1", "kind": crew.MoreInfoKind, "text": "Which, and why?", "expectedRev": 1}
	first := f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", more, http.StatusOK, "")["receipt"].(map[string]any)
	again := f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", more, http.StatusOK, "")["receipt"].(map[string]any)
	if again["repeat"] != true || again["seq"] != first["seq"] || again["rev"] != first["rev"] {
		t.Fatalf("a repeat: %v, the first %v", again, first)
	}
	// The next action carries the rev the comment made.
	c := actionOf(t, f.itemOf(t, "decision:dr-1"), "comment")
	if c.Expect != "2" {
		t.Fatalf("the comment after: %+v", c)
	}
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "k1", "kind": crew.CommentKind, "text": "Noted.", "expectedRev": 2}, http.StatusOK, "")
	for id, words := range map[string]string{"m1": "keeps dr-1 open", "k1": "a comment changes nothing"} {
		got := f.decisionTrackOf(t, "dr-1", id)
		if stateOf(got) != "done done not_applicable not_applicable" || !strings.Contains(got[stepValidated].Words, words) {
			t.Fatalf("%s's steps: %+v", id, got)
		}
	}
	// A response at the rev the item read before the comments is refused,
	// kept, and its steps say so.
	f.respond(t, adminToken, "dr-1", "s1", 1, 1, http.StatusConflict, crew.CodeStaleDecision)
	if got := f.decisionTrackOf(t, "dr-1", "s1"); stateOf(got) != "done refused not_applicable not_applicable" || !strings.Contains(got[stepRecorded].Words, crew.CodeStaleDecision) {
		t.Fatalf("the stale response's steps: %+v", got)
	}
	choice := actionOf(t, f.itemOf(t, "decision:dr-1"), "answer")
	if choice.Expect != "3" || choice.Choice == nil || *choice.Choice != 0 {
		t.Fatalf("the answer offered: %+v", choice)
	}
	f.respond(t, adminToken, "dr-1", "a1", 0, 3, http.StatusCreated, "")
	f.respond(t, adminToken, "dr-1", "a1", 0, 3, http.StatusOK, "")
	f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	if got := f.decisionTrackOf(t, "dr-1", "a1"); stateOf(got) != "done done done done" {
		t.Fatalf("the answer's steps: %+v", got)
	}
	// An approval's Approve and Reject, each its option by index.
	for i, name := range []string{"approve", "reject"} {
		did := fmt.Sprintf("dr-%d", i+2)
		f.ask(t, approvalBody(name), http.StatusCreated, "")
		f.mirrored(t, did)
		a := actionOf(t, f.itemOf(t, "decision:"+did), name)
		f.respond(t, adminToken, did, name, *a.Choice, 1, http.StatusCreated, "")
		d := f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
		if d["answer"] != []string{"Approve", "Reject"}[i] {
			t.Fatalf("%s answered %v", name, d["answer"])
		}
	}
	if got := f.decisionTrackOf(t, "dr-1", "nobody"); got[stepRecorded].State != stepUnknown {
		t.Fatalf("an id nothing was kept under: %+v", got)
	}
}

// keptResponse is response k of decision did as the record's whole read
// gives it, without what tells two records' responses apart (the record
// it names, in its fields and its words, its number, its time).
func keptResponse(t *testing.T, e *testEnv, did string, k int) crew.ResponseReceipt {
	t.Helper()
	d, err := e.srv.runs.Decision(t.Context(), did)
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range d.Responses {
		if rc.K == k {
			rc.Decision, rc.K, rc.At, rc.Words = "", 0, time.Time{}, strings.ReplaceAll(rc.Words, did, "the decision")
			return rc
		}
	}
	t.Fatalf("%s keeps no response %d: %+v", did, k, d.Responses)
	return crew.ResponseReceipt{}
}

// A clarification's decide (a choice, a text of 4,096 bytes with
// characters JSON escapes, a rationale at its bound) maps onto DR1's
// respond body field for field: the response recorded through the
// operation is the one the same body sent to /respond records. The same
// opId with another text is response_conflict; an input on a record is
// invalid, here and on a link. A decide of 4,096 escaping characters
// maps whole (recordResponse); DR1 keeps a response's receipt within 8
// KiB as encoded, so the text it records here escapes in part (#751).
func TestDecideCarriesADR1ResponseWhole(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("c1"), http.StatusCreated, "")
	f.ask(t, clarificationBody("c2"), http.StatusCreated, "")
	rationale := strings.Repeat("&", proto.MaxRationale/6)
	one := 1
	widest := proto.Decide{ReqID: "q0", Ref: "decision:dr-2", Action: "answer", Revision: "1", Choice: &one, Text: strings.Repeat("<", proto.MaxSubmit), Rationale: rationale, OpID: "op-0"}
	if _, err := proto.EncodeDecide(widest); err != nil {
		t.Fatalf("the decide at its bounds: %v", err)
	}
	if resp, err := recordResponse(widest); err != nil || resp.Text != widest.Text || resp.Rationale != rationale || *resp.Choice != 1 || resp.ExpectedRev != 1 || resp.ResponseID != "op-0" {
		t.Fatalf("the widest decide mapped: %+v %v", resp, err)
	}
	text := strings.Repeat("<", 300) + strings.Repeat("a", proto.MaxSubmit-300)
	m := proto.Decide{ReqID: "q1", Ref: "decision:dr-2", Action: "answer", Revision: "1", Choice: &one, Text: text, Rationale: rationale, OpID: "op-1"}
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/respond", map[string]any{"responseId": "op-1", "choice": 1, "text": text, "rationale": rationale, "expectedRev": 1}, http.StatusCreated, "")
	auth := crew.Authority{Grant: "admin", Via: crew.ViaWorkbench}
	receipt, repeat, err := f.srv.decideRecord(t.Context(), "dr-2", m, adminActor, auth)
	if err != nil || receipt != "op-1" || repeat {
		t.Fatalf("the decide: %q %v %v", receipt, repeat, err)
	}
	route, lane := keptResponse(t, f.testEnv, "dr-1", 1), keptResponse(t, f.testEnv, "dr-2", 1)
	if !reflect.DeepEqual(route, lane) || lane.Text != text || lane.Rationale != rationale || lane.Choice == nil || *lane.Choice != 1 {
		t.Fatalf("through the route %s\nthrough the decide %s", fitText(string(decisionJSON(t, route)), 400), fitText(string(decisionJSON(t, lane)), 400))
	}
	if _, repeat, err := f.srv.decideRecord(t.Context(), "dr-2", m, adminActor, auth); err != nil || !repeat {
		t.Fatalf("the same decide again: %v %v", repeat, err)
	}
	other := m
	other.Text = "something else"
	var de *crew.DecisionError
	if _, _, err := f.srv.decideRecord(t.Context(), "dr-2", other, adminActor, auth); !errors.As(err, &de) || de.Code != crew.CodeResponseConflict {
		t.Fatalf("another text under the opId: %v", err)
	}
	keys := m
	keys.Choice, keys.Text, keys.Rationale, keys.Input, keys.OpID = nil, "", "", "1\r", "op-2"
	if _, _, err := f.srv.decideRecord(t.Context(), "dr-2", keys, adminActor, auth); !errors.Is(err, errRecordDecide) {
		t.Fatalf("an input: %v", err)
	}
	// On a link: an input is invalid, any other decide unsupported,
	// nothing done.
	f.ask(t, clarificationBody("c3"), http.StatusCreated, "")
	_, tok := f.decisionLink(t, "/api/sessions/"+f.sid+"/links", "phone", 0)
	w := dialLane(t, f.testEnv, "/ws/sessions/"+f.sid, tok)
	keys.Ref = "decision:dr-3"
	if got := w.decide(keys); !refusedWith(got, proto.DecideInvalid) {
		t.Fatalf("an input on a link: %v", got)
	}
	m.Ref, m.OpID = "decision:dr-3", "op-3"
	if got := w.decide(m); !refusedWith(got, proto.DecideUnsupported) || !strings.Contains(got["message"].(string), "a link cannot answer") {
		t.Fatalf("a response on a link: %v", got)
	}
	if d := f.decisionWhen(t, "dr-3", "open", inState(ledger.DecisionOpen)); d["responses"] != nil {
		t.Fatalf("a response was kept: %v", d)
	}
}

// writeGates opens n gates of run in the server's ledger, run made there
// first, the last answered ones of each (past MaxOpenPerRun-4) answered,
// each made after the one before.
func writeGate(t *testing.T, ld *ledger.Ledger, run string, i int, answered bool) {
	t.Helper()
	ctx := context.Background()
	req := fmt.Sprintf(`{"question":"Ship %d?","options":[{"label":"Approve"},{"label":"Reject"}],"authorizes":"deploy","subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`, i)
	var n int64
	if _, err := ld.Transition(ctx, run, func(tx *ledger.Tx) error {
		d, _, err := tx.OpenDecision(ledger.DecisionRow{Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: fmt.Sprintf("g%d", i), Plan: "pl-1", PlanDigest: "d1",
			Fingerprint: fmt.Sprintf("fp-%d", i), Request: req, Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
		n = d.N
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !answered {
		return
	}
	if _, err := ld.Transition(ctx, run, func(tx *ledger.Tx) error {
		if _, err := tx.Respond(n, ledger.ResponseRow{Actor: adminActor, Authority: `{"grant":"admin","via":"workbench","boot":"b"}`, ResponseID: fmt.Sprintf("a%d", i),
			Fingerprint: fmt.Sprintf("fa-%d", i), Status: ledger.ResponseAccepted, Receipt: `{"words":"approved"}`}); err != nil {
			return err
		}
		ok, err := tx.Answer(n, run, 1)
		if err == nil && !ok {
			err = fmt.Errorf("decision %d not answered", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// 230 DR1 records over three runs (each run's open ones to the bound, the
// rest answered), made interleaved with questions, a failed task and plan
// revisions: read page by page from the cursor, every item once, in
// (since, ref) order, the total and the tracking count right on every
// page.
func TestInboxPagesDecisions(t *testing.T) {
	p := newPlanTest(t, func(c *config.Config) { c.MaxSessions = 100 }, readyCat)
	failed := failBuild(t, p.testEnv, failingCrew(p.repo))
	want := map[string]bool{"task:" + failed + "/build": true}
	saved := p.saveCrew(t, teamCrew("Team", "ready-cat"))
	first := p.proposed(t, saved.ID, nil)
	_, body, _ := p.planBodyOf(t, first)
	want[planRef(1)] = true
	ctx := context.Background()
	runs := []string{"gates-00000001", "gates-00000002", "gates-00000003"}
	for _, r := range runs {
		if _, err := p.srv.ledger.Transition(ctx, r, func(tx *ledger.Tx) error {
			return tx.PutRun(ledger.RunRow{ID: r, CrewID: "gates", Crew: `{"id":"gates"}`, Base: "abc123", State: ledger.RunRunning, CreatedAt: tx.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	opened := map[string]int{}
	tracking := 0
	for i := range 230 {
		run := runs[i%3]
		opened[run]++
		answered := opened[run] > ledger.MaxOpenPerRun-4
		writeGate(t, p.srv.ledger, run, i, answered)
		if answered {
			tracking++
		}
		want[fmt.Sprintf("decision:dr-%d", i+1)] = true
		switch {
		case i%25 == 0:
			fs := newFakeSession(t, p.testEnv, sessionHex(fmt.Sprintf("decisions-%d", i)), session.Options{})
			want[questionRef(fs.l.Info().ID, fs.ask(t, "Which?", dbChoices))] = true
		case i%25 == 12:
			rc, err := p.srv.dispatch.ProposePlan(ctx, dispatch.Op{Actor: adminActor, ID: fmt.Sprintf("bulk-%d", i), By: "Operator"}, "CND-1", dispatch.PlanRequest{Crew: saved.ID}, body)
			if err != nil || rc.Status != dispatch.StatusDone {
				t.Fatalf("propose %d: %+v %v", i, rc, err)
			}
			v, err := p.srv.dispatch.Plan(ctx, rc.ID)
			if err != nil {
				t.Fatal(err)
			}
			want[dispatch.PlanRef("CND-1", v.N)] = true
		}
	}
	seen := map[string]bool{}
	var last *inboxSummary
	after := ""
	for pages := 0; ; pages++ {
		page, n := p.inboxList(t, after)
		if page.Total != len(want) || page.Tracking != tracking || n > maxInboxReply {
			t.Fatalf("page %d: total %d of %d, tracking %d of %d, %d bytes", pages, page.Total, len(want), page.Tracking, tracking, n)
		}
		for i := range page.Items {
			s := page.Items[i]
			if seen[s.Ref] || !want[s.Ref] {
				t.Fatalf("%s repeated or unknown", s.Ref)
			}
			if last != nil && (s.Since.Before(last.Since) || (s.Since.Equal(last.Since) && s.Ref <= last.Ref)) {
				t.Fatalf("%s after %s", s.Ref, last.Ref)
			}
			if s.Tracking != (s.State == ledger.DecisionAnswered) || (s.Kind == inboxDecision) != strings.HasPrefix(s.Ref, "decision:") {
				t.Fatalf("a summary: %+v", s)
			}
			seen[s.Ref], last = true, &page.Items[i]
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != len(want) {
		t.Fatalf("read %d of %d", len(seen), len(want))
	}
}

// fullestRefs are twenty refs at their bound, as TestTheFullestDecisionFitsItsBound
// builds them: their characters escaping to six bytes.
func fullestRefs(t *testing.T) []decisionRef {
	t.Helper()
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	var refs []decisionRef
	for i := range ledger.MaxDecisionEvidence {
		ref, err := brakeman.ParseRef(fmt.Sprintf("file:%016x//%s", i, strings.Repeat("&", ledger.MaxEvidenceRef-len("file:0123456789abcdef//"))))
		if err != nil {
			t.Fatal(err)
		}
		esc := strings.Repeat("<", brakeman.MaxRefState)
		st := brakeman.RefState{URL: "/sessions?q=" + strings.Repeat("&", brakeman.MaxURL-len("/sessions?q=")), State: esc, Rev: strings.Repeat("<", brakeman.MaxRevision), SHA256: strings.Repeat("f", 64), At: at}
		refs = append(refs, fitRef(decisionRef{Ref: ref, Seen: brakeman.RefState{State: st.State, Rev: st.Rev, SHA256: st.SHA256}, Now: st, Change: refUnread}))
	}
	return refs
}

// assemble reads a record whole through its parts at one rev: the head,
// then every page of its responses and of its history, each part's frame
// at most MaxDecision; the record as its route gives it, and its refs.
func assemble(t *testing.T, read func(part, rev, after string) (laneRecordPart, []byte)) (crew.Decision, []decisionRef) {
	t.Helper()
	head, f := read(proto.RecordHead, "", "")
	if len(f) > 1+proto.MaxDecision || head.Decision == nil {
		t.Fatalf("the head: %d bytes, %+v", len(f), head)
	}
	d := *head.Decision
	for _, part := range []string{proto.RecordResponses, proto.RecordHistory} {
		after := ""
		for {
			p, f := read(part, head.Rev, after)
			if len(f) > 1+proto.MaxDecision || p.Rev != head.Rev {
				t.Fatalf("%s after %q: %d bytes at rev %s", part, after, len(f), p.Rev)
			}
			if part == proto.RecordResponses {
				if after == "" {
					d.Answer = p.Answer
				}
				d.Responses = append(d.Responses, p.Responses...)
			} else {
				d.History = append(d.History, p.History...)
			}
			if p.Next == "" {
				break
			}
			after = p.Next
		}
	}
	// Newest first in the parts; oldest first in the record.
	slices.Reverse(d.Responses)
	slices.Reverse(d.History)
	return d, head.Refs
}

// sameJSON says whether a and b encode to the same JSON value, objects'
// keys in any order (a record's request and policy as kept, or decoded
// and encoded again).
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(decisionJSON(t, a), &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decisionJSON(t, b), &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// recordFrame is the part as the lane sends it.
func recordFrame(t *testing.T, p laneRecordPart) []byte {
	t.Helper()
	f := decisionFrame("r1", func(d *proto.Decision, b json.RawMessage) { d.Record = b }, p)
	fr, err := proto.Decode(f)
	if err != nil || fr.Type != proto.TypeDecision {
		t.Fatalf("a record's part is no decision frame: %v %s", err, f)
	}
	return f
}

// DR1's fullest record, every part at its bound and its twenty refs at
// theirs, read part by part and page by page: every frame at most
// MaxDecision, the record assembled equal to the route's; and the fullest
// record's item at most 96 KiB.
func TestTheFullestDecisionRecordThroughTheLane(t *testing.T) {
	d := fullestDecision(t)
	refs := fullestRefs(t)
	got, gotRefs := assemble(t, func(part, rev, after string) (laneRecordPart, []byte) {
		p, code, words := recordPart(d, refs, part, rev, after)
		if code != "" {
			t.Fatalf("%s: %s %s", part, code, words)
		}
		return p, recordFrame(t, p)
	})
	if !sameJSON(t, got, d) || !sameJSON(t, gotRefs, refs) {
		t.Fatal("the record assembled is not the route's")
	}
	if _, code, _ := recordPart(d, nil, proto.RecordHistory, "7", ""); code != proto.DecideRecordMoved {
		t.Fatalf("a part at another rev: %s", code)
	}
	// A row DR1 adds without moving the rev (a delivery's) moves the mark.
	mark := recordMark(d)
	moved := d
	moved.History = append(slices.Clone(d.History[:len(d.History)-1]), d.History[0], d.History[1])
	if _, code, _ := recordPart(moved, nil, proto.RecordHistory, mark, ""); code != proto.DecideRecordMoved || moved.Rev != d.Rev {
		t.Fatalf("a history row more at the same rev: %s", code)
	}
	// The fullest record's item: its question of 500 characters each four
	// bytes, its options, evidence and authorizes at their bounds.
	e := newTestEnv(t, nil)
	big := strings.Repeat("\U0001F600", ledger.MaxDecisionQuestion)
	req := map[string]any{"question": big, "authorizes": strings.Repeat("<", ledger.MaxDecisionAuthorizes), "subject": map[string]any{"kind": "commit", "commit": strings.Repeat("a", 40)}}
	var opts []any
	for range ledger.MaxDecisionOptions {
		opts = append(opts, map[string]any{"label": strings.Repeat("<", ledger.MaxOptionLabel)})
	}
	var ev []string
	for i := range ledger.MaxDecisionEvidence {
		ev = append(ev, fmt.Sprintf("file:%016x//%s", i, strings.Repeat("&", ledger.MaxEvidenceRef-len("file:0123456789abcdef//"))))
	}
	req["options"], req["evidence"] = opts, ev
	raw, _ := json.Marshal(req)
	row := ledger.DecisionRow{N: 1<<62 - 1, RunID: strings.Repeat("c", 55) + "-0123abcd", CurrentRun: strings.Repeat("c", 55) + "-0123abcd", Origin: ledger.OriginAgent,
		Type: ledger.DecisionClarification, TaskID: strings.Repeat("t", 40), Attempt: 1 << 30, SessionID: sessionHex("big"), Request: string(raw), State: ledger.DecisionOpen, Rev: 1 << 30,
		CreatedAt: time.Now()}
	rd := e.srv.newInboxRead(t.Context())
	// Its asker live, so it offers more information too.
	rd.askerLive = func(ledger.DecisionRow) bool { return true }
	it, err := rd.decisionItem(row, &ledger.DecisionRecord{DecisionRow: row})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decisionJSON(t, it)); n > maxInboxItem || it.Question != big || len(it.Actions) != 9 {
		t.Fatalf("the fullest item: %d bytes, %d actions", n, len(it.Actions))
	}
	if n := len(decisionJSON(t, it.summary())); n > maxInboxSummary {
		t.Fatalf("its summary: %d bytes", n)
	}
}

// A page of a record's responses at its worst fits its frame, whatever
// ledger.MaxDecisionReceipt is: recordResponsesPage receipts each kept at
// that bound (their number set after, as the read sets it) beside the
// answer of an escaping label and text, the page full and its frame
// within proto.MaxDecision.
func TestARecordsResponsesPageFitsItsFrame(t *testing.T) {
	if recordResponsesPage < 1 || ledger.MaxDecisionResponses < recordResponsesPage+1 {
		t.Fatalf("a page of %d receipts of %d", recordResponsesPage, ledger.MaxDecisionResponses)
	}
	d := fullestDecision(t)
	at := time.Date(2026, 11, 5, 23, 59, 59, 999999999, time.UTC)
	choice := 5
	d.Responses = nil
	for range ledger.MaxDecisionResponses {
		rc := crew.ResponseReceipt{Decision: d.ID, Status: "refused", Code: crew.CodeStaleDecision, Actor: strings.Repeat("a", ledger.MaxDecisionActor), Choice: &choice, At: at, Rev: 1 << 30}
		rc.Words = strings.Repeat("a", ledger.MaxDecisionReceipt-len(decisionJSON(t, rc)))
		if n := len(decisionJSON(t, rc)); n != ledger.MaxDecisionReceipt {
			t.Fatalf("a receipt kept at %d bytes, not %d", n, ledger.MaxDecisionReceipt)
		}
		rc.K = 1<<63 - 1
		d.Responses = append(d.Responses, rc)
	}
	d.Answer = strings.Repeat("<", ledger.MaxOptionLabel) + ": " + strings.Repeat("\x01", proto.MaxSubmit)
	p, code, words := recordPart(d, nil, proto.RecordResponses, recordMark(d), "")
	if code != "" || len(p.Responses) != recordResponsesPage || p.Answer != d.Answer || p.Next == "" {
		t.Fatalf("the first page: %s %s, %d receipts", code, words, len(p.Responses))
	}
	if f := recordFrame(t, p); len(f) > 1+proto.MaxDecision {
		t.Fatalf("a page of %d receipts: %d bytes", recordResponsesPage, len(f))
	}
	f, err := proto.EncodeDecision(proto.Decision{ReqID: strings.Repeat("r", proto.MaxDecisionReqID), Record: decisionJSON(t, p)})
	if err != nil || len(f) > 1+proto.MaxDecision {
		t.Fatalf("a page of %d receipts with the longest request id: %d bytes, %v", recordResponsesPage, len(f), err)
	}
}

// A record read across a change, its evidence naming the session and
// the run: a head whose read the lane holds at its seam (recordRead,
// after the one read its scope is judged on and its refs resolved) while
// a response is recorded and applied is cut whole from that read, the
// record open at rev 1 with the refs the route gave at that rev; the next part asked at its mark is refused record_moved with
// the new mark; read again from the head, the record assembled is the
// route's at the new mark and its refs are the route's, never a mix.
func TestADecisionRecordReadAcrossAChange(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	body := approvalBody("r1")
	body["evidence"] = []string{"session:" + f.sid, "run:" + f.run}
	f.ask(t, body, http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	armed, held, release := make(chan struct{}, 1), make(chan int), make(chan struct{})
	f.srv.laneSeam = &laneSeams{recordRead: func(part string, rev int) {
		select {
		case <-armed:
			held <- rev
			<-release
		default:
		}
	}}
	_, tok := f.decisionLink(t, "/api/sessions/"+f.sid+"/links", "phone", 0)
	w := dialLane(t, f.testEnv, "/ws/sessions/"+f.sid, tok)
	ask := func(part, rev, after string) string {
		id := w.reqID()
		w.send(proto.MustControl(proto.DecisionRecord{T: proto.CtlDecisionRecord, ReqID: id, Ref: "decision:dr-1", Part: part, Rev: rev, After: after}))
		return id
	}
	answer := func(id string) (laneRecordPart, []byte, map[string]any) {
		d, ctl := w.reply(id)
		if d == nil {
			return laneRecordPart{}, nil, ctl
		}
		var p laneRecordPart
		if err := json.Unmarshal(d.Record, &p); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(d)
		return p, b, nil
	}
	read := func(part, rev, after string) (laneRecordPart, []byte, map[string]any) {
		return answer(ask(part, rev, after))
	}
	// The route's refs as the lane's head must carry them: the same refs,
	// seen and now, but for the moment each was resolved at.
	refsOf := func(refs []decisionRef) string {
		out := slices.Clone(refs)
		for i := range out {
			out[i].Now.At, out[i].Now.Rev = time.Time{}, ""
		}
		return string(decisionJSON(t, out))
	}
	routeRefs := func() (map[string]any, []decisionRef) {
		route := f.expect(t, http.MethodGet, "/api/decisions/dr-1", nil, http.StatusOK, "")
		var refs []decisionRef
		if err := json.Unmarshal(decisionJSON(t, route["refs"]), &refs); err != nil {
			t.Fatal(err)
		}
		return route, refs
	}
	_, before := routeRefs()
	armed <- struct{}{}
	id := ask(proto.RecordHead, "", "")
	if rev := <-held; rev != 1 {
		t.Fatalf("the head was read at rev %d", rev)
	}
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	// Applied, and its line's delivery settled: nothing changes it after.
	now := f.decisionWhen(t, "dr-1", "applied and delivered", func(d map[string]any) bool {
		h, _ := d["history"].([]any)
		last, _ := h[len(h)-1].(map[string]any)
		return d["state"] == ledger.DecisionApplied && slices.Contains([]any{ledger.HistoryDeliveryEntered, ledger.HistoryDeliveryUntyped, ledger.HistoryDeliveryUncertain}, last["kind"])
	})
	close(release)
	head, _, no := answer(id)
	if no != nil || head.Rev != "100002" || head.Decision.Rev != 1 || head.Decision.State != ledger.DecisionOpen || len(head.Refs) != 2 || refsOf(head.Refs) != refsOf(before) {
		t.Fatalf("the head held across the change: %+v %v\nthe route's refs before it %s", head, no, refsOf(before))
	}
	mark := fmt.Sprintf("%v01%03d", now["rev"], len(now["history"].([]any)))
	for _, part := range []string{proto.RecordResponses, proto.RecordHistory} {
		if _, _, no = read(part, head.Rev, ""); !refusedWith(no, proto.DecideRecordMoved) || no["rev"] != mark {
			t.Fatalf("the %s at the held head's mark: %v, now %s", part, no, mark)
		}
	}
	got, refs := assemble(t, func(part, rev, after string) (laneRecordPart, []byte) {
		p, b, no := read(part, rev, after)
		if no != nil {
			t.Fatalf("%s at %s: %v", part, rev, no)
		}
		return p, b
	})
	route, after := routeRefs()
	var want crew.Decision
	if err := json.Unmarshal(decisionJSON(t, route["decision"]), &want); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, got, want) || got.State != ledger.DecisionApplied || len(refs) != 2 || refsOf(refs) != refsOf(after) {
		t.Fatalf("assembled %s\nthe route's %s\nrefs %s\nthe route's %s", decisionJSON(t, got), decisionJSON(t, want), refsOf(refs), refsOf(after))
	}
}

// A decision link is told of every change of a record in its scope, not
// only of its state: a comment from the workbench (the rev moves, nothing
// the item summarizes does) and a stale response refused (the rev stays,
// its responses grow) each change the link's hash, made of one read of
// the scope, and send decisions_changed.
func TestALinkIsToldOfARecordsChange(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	link, tok := f.decisionLink(t, "/api/sessions/"+f.sid+"/links", "phone", 0)
	w := dialLane(t, f.testEnv, "/ws/sessions/"+f.sid, tok)
	if page := w.list(""); page.Total != 1 || page.Items[0].Ref != "decision:dr-1" {
		t.Fatalf("the link's list: %+v", page)
	}
	hash := func() string {
		h, ok := f.srv.laneHash(t.Context(), link)
		if !ok {
			t.Fatal("the scope could not be read")
		}
		return h
	}
	first := hash()
	f.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", map[string]any{"opId": "k1", "kind": crew.CommentKind, "text": "Noted.", "expectedRev": 1}, http.StatusOK, "")
	f.decisionWhen(t, "dr-1", "commented", func(d map[string]any) bool { return d["rev"] == float64(2) })
	second := hash()
	if second == first {
		t.Fatal("a comment left the link's hash as it was")
	}
	w.waitChanged()
	f.respond(t, adminToken, "dr-1", "s1", 0, 1, http.StatusConflict, crew.CodeStaleDecision)
	f.decisionWhen(t, "dr-1", "refused", func(d map[string]any) bool {
		rs, _ := d["responses"].([]any)
		return len(rs) == 1 && d["rev"] == float64(2)
	})
	if hash() == second {
		t.Fatal("a refused response left the link's hash as it was")
	}
	w.waitChanged()
}

// A scope whose records cannot be read is no scope without them: the
// lane's list is refused unavailable and its hash says it read nothing;
// an item whose run's holds cannot be read is none (unavailable), never
// one saying nothing is held.
func TestAFailedReadOfDecisionsIsNoAnswer(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	link, _ := f.decisionLink(t, "/api/sessions/"+f.sid+"/links", "phone", 0)
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	got := f.srv.Lane().List(gone, link, f.sid, proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"})
	var m map[string]any
	fr, err := proto.Decode(got)
	if err == nil {
		err = json.Unmarshal(fr.Payload, &m)
	}
	if err != nil || fr.Type != proto.TypeControl || !refusedWith(m, "unavailable") {
		t.Fatalf("the list of a scope not read: %s %v", got, err)
	}
	if _, ok := f.srv.laneHash(gone, link); ok {
		t.Fatal("a hash of a scope not read")
	}
	rec, found, err := f.srv.ledger.Decision(t.Context(), 1)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if it, err := f.srv.newInboxRead(gone).decisionItem(rec.DecisionRow, &rec); err == nil {
		t.Fatalf("an item whose holds were not read: %+v", it)
	}
}

// An agent's record whose asking attempt has ended (the server ended and
// came up again: the attempt interrupted, the record kept open) offers no
// request for more information, which DR1 would refuse no_recipient, and
// its answer says the task's next attempt reads it in its task file.
func TestAnEndedAskerIsAskedNoMore(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, clarificationBody("c1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	if got := actionNames(f.itemOf(t, "decision:dr-1")); !slices.Contains(got, crew.MoreInfoKind) {
		t.Fatalf("a live asker's record: %v", got)
	}
	again := restarted(t, f.testEnv)
	it := again.itemOf(t, "decision:dr-1")
	if it.State != ledger.DecisionOpen || slices.Contains(actionNames(it), crew.MoreInfoKind) {
		t.Fatalf("an ended asker's record: %s %v", it.State, actionNames(it))
	}
	if a := actionOf(t, it, "answer"); !strings.Contains(a.Scope, "build's next attempt reads the answer in its task file") {
		t.Fatalf("its answer's scope: %s", a.Scope)
	}
	rev, _ := strconv.Atoi(it.Revision)
	more := map[string]any{"opId": "m1", "kind": crew.MoreInfoKind, "text": "Which?", "expectedRev": rev}
	again.expect(t, http.MethodPost, "/api/decisions/dr-1/comment", more, http.StatusConflict, crew.CodeNoRecipient)
}

// Whom a record is for, in the answerer's words, as DR1's card says it
// (approverWords): each responsibility in HM1's words, a person by name,
// and a label that named no one (or none) the operator alone.
func TestADecisionsAnswererSaysWhomItIsFor(t *testing.T) {
	for _, c := range []struct {
		label, person, resp, want string
	}{
		{"role:architecture", "nate", crew.RespArchitecture, "you, the operator, for nate (architecture owner)"},
		{"role:product", "ana", crew.RespProduct, "you, the operator, for ana (product owner)"},
		{"role:security", "ana", crew.RespSecurity, "you, the operator, for ana (security reviewer)"},
		{"role:acceptance", "kim", crew.RespAcceptance, "you, the operator, for kim (acceptance owner)"},
		{"person:nate", "nate", "", "you, the operator, for nate"},
		{"role:x", "nate", "x", "you, the operator, for nate"},
		{"role:architecture", "", crew.RespArchitecture, "you, the operator"},
		{"", "", "", "you, the operator"},
	} {
		var r recordRequest
		raw := fmt.Sprintf(`{"for":{"label":%q,"person":%q,"responsibility":%q}}`, c.label, c.person, c.resp)
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if got := answererOf(r); got != c.want {
			t.Errorf("%s: %q, want %q", raw, got, c.want)
		}
	}
	if got := answererOf(recordRequest{}); got != inboxAnswerer {
		t.Errorf("no for: %q", got)
	}
}

// A decision link's lane shows a record in its scope with its four states
// and no action, saying where it is answered; the mirrored question is not
// listed beside it; the record's parts read; a record of another task is
// not in the scope.
func TestALinkShowsADecisionRecordWithoutActions(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.mirrored(t, "dr-1")
	_, tok := f.decisionLink(t, "/api/sessions/"+f.sid+"/links", "phone", 0)
	w := dialLane(t, f.testEnv, "/ws/sessions/"+f.sid, tok)
	page := w.list("")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Ref != "decision:dr-1" || len(page.Items[0].Actions) != 0 {
		t.Fatalf("the link's list: %+v", page)
	}
	it := w.read("decision:dr-1")
	if len(it.Actions) != 0 || !strings.Contains(it.Now, linkCannotAnswer) || stateOf(stepsByName(t, it.Delivery)) != "done waiting waiting waiting" {
		t.Fatalf("the link's item: %+v", it)
	}
	if got := w.decide(proto.Decide{Ref: "decision:dr-1", Action: "approve", Revision: "1", Choice: new(int), OpID: "o1"}); !refusedWith(got, proto.DecideUnsupported) {
		t.Fatalf("an approve: %v", got)
	}
	id := w.reqID()
	if code := w.refusedRead(proto.DecisionTrack{T: proto.CtlDecisionTrack, ReqID: id, Ref: "decision:dr-1", Receipt: "o1", Attempt: "o1"}, id); code != proto.DecideUnsupported {
		t.Fatalf("a track: %s", code)
	}
	// Another run's attempt's record is out of this link's scope.
	other, release, err := f.srv.runs.LaunchNamed(t.Context(), managedCrew(f.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	sid := runningAttempt(t, f.testEnv, other.ID, "build", 1)["sessionId"].(string)
	f.expectAs(t, f.agentToken(sid), http.MethodPost, "/api/sessions/"+sid+"/decisions", approvalBody("x1"), http.StatusCreated, "")
	id = w.reqID()
	if code := w.refusedRead(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: id, Ref: "decision:dr-2"}, id); code != proto.DecideNotInScope {
		t.Fatalf("another run's record: %s", code)
	}
	id = w.reqID()
	if code := w.refusedRead(proto.DecisionRecord{T: proto.CtlDecisionRecord, ReqID: id, Ref: "decision:dr-2", Part: proto.RecordHead}, id); code != proto.DecideNotInScope {
		t.Fatalf("another run's record's head: %s", code)
	}
	// Answered on the desktop: applied, it leaves the link's list.
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	f.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))
	until(t, "the record gone from the link's list", func() bool { return w.list("").Total == 0 })
	if it := w.read("decision:dr-1"); stateOf(stepsByName(t, it.Delivery)) != "done done done done" {
		t.Fatalf("its four states through the link: %+v", it.Delivery)
	}
}

// gatedLaunch is a launchTest whose approved revision names a gate on
// build, launched: the run, its gate's id.
func gatedLaunch(t *testing.T, l *launchTest) (run, did string) {
	t.Helper()
	plan := l.approved(t, map[string]any{"decisions": []any{map[string]any{"id": "ship", "question": "Ship it?", "kind": "approval", "before": []string{"build"}}}})
	run = entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)
	taskVia(t, l.testEnv, run, "build", "held", func(task map[string]any) bool {
		holds, _ := task["holds"].([]any)
		return len(holds) == 1
	})
	rows, err := l.srv.ledger.Decisions(t.Context(), ledger.DecisionFilter{CurrentRun: run})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the gate: %v %v", rows, err)
	}
	return run, rows[0].ID()
}

// restarted ends e's server and starts another over its data directory,
// as `conductor serve` comes up again: the runs it held interrupted. The
// new server's runs are stopped at the end, before its directories go.
func restarted(t *testing.T, e *testEnv) *testEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	again := e.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	return again
}

// A gate answered while its run is interrupted (the server ended mid-run
// and started again, no continuation yet): answered, a tracking item with
// its four states (recorded, not applied) on every page read and after
// another restart, not in the bar's number; the run continued, applied,
// out of the tracking items and into Recent.
func TestAnAnsweredDecisionStaysTracked(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, did := gatedLaunch(t, l)
	again := restarted(t, l.testEnv)
	again.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
	again.decisionWhen(t, did, "answered", inState(ledger.DecisionAnswered))
	check := func(e *testEnv) {
		t.Helper()
		page, _ := e.inboxList(t, "")
		i := slices.IndexFunc(page.Items, func(s inboxSummary) bool { return s.Ref == "decision:"+did })
		if i < 0 || !page.Items[i].Tracking || page.Tracking != 1 || len(page.Items[i].Actions) != 1 {
			t.Fatalf("not tracked: %+v", page)
		}
		it := e.itemOf(t, "decision:"+did)
		steps := stepsByName(t, it.Delivery)
		if stateOf(steps) != "done done waiting waiting" || !strings.Contains(steps[stepValidated].Words, "continued") || !strings.Contains(it.Now, "not yet applied") {
			t.Fatalf("its four states: %+v", it)
		}
	}
	check(again)
	check(again)
	third := restarted(t, again)
	check(third)
	next := third.expect(t, http.MethodPost, "/api/runs/"+run+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)["id"].(string)
	third.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	page, _ := third.inboxList(t, "")
	if listed(page, "decision:"+did) || page.Tracking != 0 || !slices.ContainsFunc(page.Recent, func(s inboxSummary) bool { return s.Ref == "decision:"+did }) {
		t.Fatalf("after the continuation %s: %+v", next, page)
	}
	if got := third.decisionTrackOf(t, did, "a1"); stateOf(got) != "done done done done" {
		t.Fatalf("its steps applied: %+v", got)
	}
}

// A gate open across a restart over the same data directory is the same
// item; after a response, its four states are the same after another.
func TestADecisionSurvivesARestart(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	_, did := gatedLaunch(t, l)
	before := l.itemOf(t, "decision:"+did)
	again := restarted(t, l.testEnv)
	after := again.itemOf(t, "decision:"+did)
	if after.Ref != before.Ref || after.Question != before.Question || after.Revision != before.Revision || !slices.Equal(actionNames(after), actionNames(before)) || !after.Since.Equal(before.Since) {
		t.Fatalf("before %+v\nafter %+v", before, after)
	}
	again.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
	again.decisionWhen(t, did, "answered", inState(ledger.DecisionAnswered))
	answered := again.itemOf(t, "decision:"+did).Delivery
	third := restarted(t, again)
	if got := third.itemOf(t, "decision:"+did).Delivery; !reflect.DeepEqual(got, answered) {
		t.Fatalf("after the restart %+v\nbefore %+v", got, answered)
	}
}

// A gate a plan requires before the run's first attempt, with no member
// alive, listed over the run's desk, at the server (GET
// /ws/runs/{run}/desk) and at a switchyard (the desk's connection), with
// no action; its four states read back through the lane.
func TestAGateOverTheDesk(t *testing.T) {
	checkDesk := func(t *testing.T, desk *laneClient, run, did string) {
		t.Helper()
		page := desk.list("")
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Ref != "decision:"+did || len(page.Items[0].Actions) != 0 {
			t.Fatalf("the desk's list: %+v", page)
		}
		it := desk.read("decision:" + did)
		if len(it.Actions) != 0 || !strings.Contains(it.Now, linkCannotAnswer) || !slices.Contains(it.Blocking.Refs, "task:"+run+"/build") {
			t.Fatalf("the desk's item: %+v", it)
		}
		if got := desk.decide(proto.Decide{Ref: "decision:" + did, Action: "approve", Revision: "1", Choice: new(int), OpID: "o1"}); !refusedWith(got, proto.DecideUnsupported) {
			t.Fatalf("an approve over the desk: %v", got)
		}
		if got := stateOf(stepsByName(t, it.Delivery)); got != "done waiting waiting waiting" {
			t.Fatalf("its four states: %s", got)
		}
	}
	t.Run("server", func(t *testing.T) {
		l := newLaunchTest(t, nil, nil)
		run, did := gatedLaunch(t, l)
		_, tok := l.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
		checkDesk(t, dialLane(t, l.testEnv, "/ws/runs/"+run+"/desk", tok), run, did)
	})
	t.Run("switchyard", func(t *testing.T) {
		yard := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
		yard.srv.hosts.SetDecisions(true)
		l := newLaunchTest(t, nil, nil)
		l.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
			Decisions: l.srv.Lane(), LinkGone: l.srv.RemoteLinkGone}})
		run, did := gatedLaunch(t, l)
		_, token := remoteDecisionLink(t, l.testEnv, "/api/runs/"+run+"/links", "phone")
		desk := joinDesk(t, yard, token)
		checkDesk(t, desk, run, did)
		// Answered on the desktop: its four states through the desk.
		l.respond(t, adminToken, did, "a1", 0, 1, http.StatusCreated, "")
		l.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
		if got := stateOf(stepsByName(t, desk.read("decision:"+did).Delivery)); got != "done done done done" {
			t.Fatalf("applied, over the desk: %s", got)
		}
	})
}
