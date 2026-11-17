package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The selection on the routes (CP1 PR-6b): who answers it, the reply's
// selection part, Brakeman's read without it, the deliveries and repairs
// a selection admits, and the ticket comment of the selected lane.

// state is the comparison's state as the engine reads it from the ledger.
func (de *cmpDraftEnv) state(t *testing.T) crew.ComparisonState {
	t.Helper()
	st, err := de.srv.runs.ComparisonStateOf(t.Context(), de.run)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// waitState waits for the comparison to stand in one of states.
func (de *cmpDraftEnv) waitState(t *testing.T, states ...string) crew.ComparisonState {
	t.Helper()
	var st crew.ComparisonState
	waitUntil(t, "the comparison "+strings.Join(states, " or "), func() bool {
		st = de.state(t)
		for _, s := range states {
			if st.State == s {
				return true
			}
		}
		return false
	})
	return st
}

// selecting settles the lanes and answers the selection's record, open.
func (de *cmpDraftEnv) selecting(t *testing.T) string {
	t.Helper()
	de.settleLanes(t)
	return de.waitState(t, crew.CompareSelecting).Decision
}

// decisionRev is the record's rev as its route reads it.
func (de *cmpDraftEnv) decisionRev(t *testing.T, did string) int {
	t.Helper()
	d := entityOf(t, de.expect(t, "GET", "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
	return int(d["rev"].(float64))
}

// respond answers the record with tok: the option's index and the
// rationale.
func (de *cmpDraftEnv) respond(t *testing.T, tok, did string, choice int, rationale string) (int, []byte, map[string]any) {
	t.Helper()
	return de.callAs(t, tok, "POST", "/api/decisions/"+did+"/respond", map[string]any{"responseId": opID(), "choice": choice, "rationale": rationale, "expectedRev": de.decisionRev(t, did)})
}

// selectB answers the record with approach-b, as the operator, and waits
// for its application.
func (de *cmpDraftEnv) selectB(t *testing.T, did string) crew.ComparisonState {
	t.Helper()
	if status, raw, _ := de.respond(t, adminToken, did, 1, "b reads simpler"); status >= 300 {
		t.Fatalf("the operator's answer: %d %s", status, raw)
	}
	st := de.waitState(t, crew.CompareSelected)
	if st.Selected == nil || st.Selected.Task != "approach-b" {
		t.Fatalf("the selection applied: %+v", st)
	}
	return st
}

// candidate is the lane's shown candidate in the comparison: run/task/n@commit.
func (de *cmpDraftEnv) candidate(t *testing.T, task string) string {
	t.Helper()
	_, raw, out := de.readComparison(t, adminToken, "", de.run)
	for _, c := range out["candidates"].([]any) {
		c := c.(map[string]any)
		if c["task"] == task {
			return fmt.Sprintf("%s/%s/%v@%s", c["run"], task, c["attempt"], c["commit"])
		}
	}
	t.Fatalf("no %s in %s", task, raw)
	return ""
}

// The operator alone answers a selection: a lane's token, an ordinary
// member's, a share link with control and a principal of the decide grant
// are refused, the record left open; nor is it routed to the decide
// grant; the workbench's answer, with its rationale, is applied.
func TestOnlyTheOperatorSelects(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{identity: decideIdentity()})
	did := de.selecting(t)
	_, share, err := de.srv.links.Create(de.a, session.RoleControl, "", 3600e9)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"a lane's token": de.aTok, "an ordinary member's token": de.plainTok, "a share link with control": share, "a decide principal": passAna} {
		if status, raw, _ := de.respond(t, tok, did, 0, "a is mine"); status < 400 {
			t.Errorf("%s answered: %d %s", name, status, raw)
		}
	}
	if status, raw, _ := de.callAs(t, adminToken, "POST", "/api/decisions/"+did+"/route", map[string]any{"opId": opID(), "to": map[string]any{"kind": "grant", "grant": "decide"}, "expectedRev": de.decisionRev(t, did)}); status < 400 {
		t.Errorf("routed to the decide grant: %d %s", status, raw)
	}
	if st := de.state(t); st.State != crew.CompareSelecting || st.Decision != did {
		t.Fatalf("after the refusals: %+v", st)
	}
	de.selectB(t, did)
}

// I7 on the route: an answer with no rationale, and a cancel, are refused
// 400 invalid in the engine's words, the record unchanged.
func TestTheRouteNeedsARationale(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	rev := de.decisionRev(t, did)
	status, raw, out := de.respond(t, adminToken, did, 1, "")
	if status != http.StatusBadRequest || errorCode(out) != "invalid" || !strings.Contains(apiMessage(out), "say why: the rationale is the record of this selection") {
		t.Fatalf("no rationale: %d %s", status, raw)
	}
	status, raw, out = de.callAs(t, adminToken, "POST", "/api/decisions/"+did+"/cancel", map[string]any{"commandId": opID(), "expectedRev": rev, "why": "not now"})
	if status != http.StatusBadRequest || errorCode(out) != "invalid" || !strings.Contains(apiMessage(out), "a selection is not cancelled") {
		t.Fatalf("the cancel: %d %s", status, raw)
	}
	if de.decisionRev(t, did) != rev || de.state(t).State != crew.CompareSelecting {
		t.Fatal("a refusal changed the record")
	}
}

// The comparison carries its selection: open with its options, each
// candidate's subject and None of these last, nothing chosen; then the
// answer, its rationale, who gave it and how it was applied; the state
// beside it; within its bound.
func TestTheComparisonShowsTheSelection(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	_, _, out := de.readComparison(t, adminToken, "", de.run)
	if _, ok := out["selection"]; ok || out["state"] != crew.CompareRunning {
		t.Fatalf("before the settling: %v %v", out["selection"], out["state"])
	}
	did := de.selecting(t)
	_, raw, out := de.readComparison(t, adminToken, "", de.run)
	sel, _ := out["selection"].(map[string]any)
	opts, _ := sel["options"].([]any)
	if sel["decision"] != did || sel["state"] != "open" || len(opts) != 3 || sel["chosen"] != nil || out["state"] != crew.CompareSelecting {
		t.Fatalf("open: %s", raw)
	}
	for i, task := range []string{"approach-a", "approach-b"} {
		sub, _ := opts[i].(map[string]any)["subject"].(map[string]any)
		if sub["kind"] != "candidate" || sub["task"] != task {
			t.Fatalf("option %d: %v", i, opts[i])
		}
	}
	if opts[2].(map[string]any)["label"] != "None of these" || opts[2].(map[string]any)["subject"] != nil {
		t.Fatalf("the last option: %v", opts[2])
	}
	de.selectB(t, did)
	_, raw, out = de.readComparison(t, adminToken, "", de.run)
	sel, _ = out["selection"].(map[string]any)
	applied, _ := sel["applied"].(map[string]any)
	selected, _ := applied["selected"].(map[string]any)
	if sel["state"] != "applied" || sel["chosen"] != float64(1) || sel["rationale"] != "b reads simpler" || sel["by"] == "" || sel["at"] == nil ||
		applied["how"] != "selected" || selected["task"] != "approach-b" || out["state"] != crew.CompareSelected {
		t.Fatalf("applied: %s", raw)
	}
	enc, _ := json.Marshal(sel)
	if len(enc) > maxSelection {
		t.Fatalf("the selection is %d bytes", len(enc))
	}
}

// The selection part keeps its bound with every text at its own, every
// character '<'.
func TestTheSelectionKeepsItsBound(t *testing.T) {
	long := strings.Repeat("<", 8<<10)
	var opts []map[string]any
	for range 7 {
		opts = append(opts, map[string]any{"label": long, "tradeoffs": long, "subject": map[string]any{"kind": "candidate", "run": "r", "task": "t", "n": 1, "commit": strings.Repeat("a", 40)}})
	}
	req, _ := json.Marshal(map[string]any{"options": opts, "for": map[string]any{"label": long, "person": long, "responsibility": long}})
	choice := 0
	d := crew.Decision{ID: "dr-1", State: "applied", Request: req,
		Responses: []crew.ResponseReceipt{{Status: "accepted", Choice: &choice, Rationale: long, ByName: long}},
		Applied:   &crew.Application{How: "selected", Words: long, Selected: &crew.SettledCandidate{Task: "t", Run: "r", Attempt: 1, Commit: strings.Repeat("a", 40)}}}
	enc, _ := json.Marshal(selectionOf(d))
	if len(enc) > maxSelection {
		t.Fatalf("the selection is %d bytes, past %d", len(enc), maxSelection)
	}
}

// Brakeman's read of an answered comparison has no selection and no state
// it gives; the workbench's has both.
func TestBrakemanReadsNoSelection(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", nil)
	did := de.selecting(t)
	// Asked while selecting (a draft is asked before the selection alone),
	// read once it is answered.
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != 201 {
		t.Fatalf("ask: %d %v", status, req)
	}
	sid, tok, _ := de.bk.turnSession(t, req["id"].(string))
	de.selectB(t, did)
	status, raw, out := de.readComparison(t, tok, sid, de.run)
	if _, sel := out["selection"]; status != http.StatusOK || sel || out["state"] != nil || strings.Contains(string(raw), did) || strings.Contains(string(raw), "b reads simpler") {
		t.Fatalf("Brakeman's read: %d %s", status, raw)
	}
	_, _, admin := de.readComparison(t, adminToken, "", de.run)
	if admin["selection"] == nil || admin["state"] != crew.CompareSelected {
		t.Fatalf("the workbench's read: %v", admin)
	}
}

// I9: a delivery naming a comparison's candidate is refused before
// anything is kept while the lanes run, while the record is open, after a
// stop while selecting and while its answer waits for the continuation
// that applies it; once applied, the not-selected lane's is refused in
// the engine's words and the selected one is taken.
func TestADeliveryNamesTheSelectedCandidate(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	deliver := func(cand string, status int) map[string]any {
		t.Helper()
		op := opID()
		out := de.deliver(t, "CND-1", op, "https://example.test/pull/"+op, cand, status, map[bool]string{true: "not_selected"}[status == http.StatusConflict])
		if status == http.StatusConflict {
			if _, found, err := de.srv.dispatch.Receipt(t.Context(), adminActor, op); err != nil || found {
				t.Fatalf("a refused delivery kept a receipt: %v %v", found, err)
			}
		}
		return out
	}
	// While the lanes run: refused before the candidate is looked at.
	running := de.run + "/approach-a/1@" + strings.Repeat("0", 40)
	if out := deliver(running, http.StatusConflict); !strings.HasPrefix(apiMessage(out), "select a candidate first") {
		t.Fatalf("running: %v", out)
	}
	did := de.selecting(t)
	a, b := de.candidate(t, "approach-a"), de.candidate(t, "approach-b")
	for _, c := range []string{a, b} {
		if out := deliver(c, http.StatusConflict); apiMessage(out) != "select a candidate first: "+did+" is open" {
			t.Fatalf("selecting: %v", out)
		}
	}
	de.expect(t, "POST", "/api/runs/"+de.run+"/stop", nil, http.StatusOK, "")
	if out := deliver(b, http.StatusConflict); apiMessage(out) != "select a candidate first: "+did+" is open" {
		t.Fatalf("stopped while selecting: %v", out)
	}
	// Answered on the stopped run, applied by its continuation.
	if status, raw, _ := de.respond(t, adminToken, did, 1, "b reads simpler"); status >= 300 {
		t.Fatalf("the answer: %d %s", status, raw)
	}
	de.waitState(t, crew.CompareAnswered)
	if out := deliver(b, http.StatusConflict); apiMessage(out) != "select a candidate first: "+did+" is open" {
		t.Fatalf("answered: %v", out)
	}
	de.expect(t, "POST", "/api/runs/"+de.run+"/resume", nil, http.StatusCreated, "")
	if st := de.waitState(t, crew.CompareSelected); st.Selected.Task != "approach-b" {
		t.Fatalf("applied: %+v", st)
	}
	if out := deliver(a, http.StatusConflict); apiMessage(out) != "approach-a was not selected in "+did+": launch a new comparison to try it again" {
		t.Fatalf("the lane not selected: %v", out)
	}
	deliver(b, http.StatusCreated)
}

// None of these: every candidate's delivery is refused "no candidate was
// selected", and a Request changes of any lane.
func TestNoneOfTheseDeliversNothing(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	if status, raw, _ := de.respond(t, adminToken, did, 2, "neither is fast enough"); status >= 300 {
		t.Fatalf("None of these: %d %s", status, raw)
	}
	de.waitState(t, crew.CompareNone)
	for _, task := range []string{"approach-a", "approach-b"} {
		cand := de.candidate(t, task)
		op := opID()
		out := de.deliver(t, "CND-1", op, "https://example.test/pull/"+op, cand, http.StatusConflict, "not_selected")
		if apiMessage(out) != "no candidate was selected: launch a new comparison" {
			t.Fatalf("%s: %v", task, out)
		}
		out = sendReview(t, de.testEnv, "CND-1", reviewOf(cand, de.base(t), "request_changes", changeOf("Make it faster.")), http.StatusConflict, "not_selected")
		if apiMessage(out) != "no candidate was selected: launch a new comparison" {
			t.Fatalf("%s's repair: %v", task, out)
		}
	}
}

// base is the comparison's base commit.
func (de *cmpDraftEnv) base(t *testing.T) string {
	t.Helper()
	row, ok, err := de.srv.ledger.Run(t.Context(), de.run)
	if err != nil || !ok {
		t.Fatalf("the run: %v %v", ok, err)
	}
	return row.Base
}

// reviewsOnTicket is how many reviews CND-1 has.
func (de *cmpDraftEnv) reviewsOnTicket(t *testing.T) int {
	t.Helper()
	return reviewsOn(t, de.srv, "CND-1")
}

// Request changes waits for the selection: refused not_selected while the
// record is open, nothing kept in Dispatch; a comment and an approval are
// taken.
func TestRequestChangesWaitsForTheSelection(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.selecting(t)
	b := de.candidate(t, "approach-b")
	before := de.reviewsOnTicket(t)
	out := sendReview(t, de.testEnv, "CND-1", reviewOf(b, de.base(t), "request_changes", changeOf("Make it faster.")), http.StatusConflict, "not_selected")
	if apiMessage(out) != "select a candidate first: a repair before the selection would make the candidates unlike" {
		t.Fatalf("the refusal: %v", out)
	}
	if n := de.reviewsOnTicket(t); n != before {
		t.Fatalf("%d reviews, %d before", n, before)
	}
	for _, verdict := range []string{"comment", "approve"} {
		sendReview(t, de.testEnv, "CND-1", reviewOf(b, de.base(t), verdict), http.StatusCreated, "")
	}
}

// Door 15: after the selection, a Request changes of the lane not selected
// is refused in the engine's words, nothing kept; the selected lane's is
// taken.
func TestRequestChangesOnAnUnselectedCandidateIsRefused(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	de.selectB(t, did)
	a, b := de.candidate(t, "approach-a"), de.candidate(t, "approach-b")
	before := de.reviewsOnTicket(t)
	out := sendReview(t, de.testEnv, "CND-1", reviewOf(a, de.base(t), "request_changes", changeOf("Make it faster.")), http.StatusConflict, "not_selected")
	if apiMessage(out) != "approach-a was not selected in "+did+": launch a new comparison to try it again" {
		t.Fatalf("the refusal: %v", out)
	}
	if n := de.reviewsOnTicket(t); n != before {
		t.Fatalf("%d reviews, %d before", n, before)
	}
	sendReview(t, de.testEnv, "CND-1", reviewOf(b, de.base(t), "request_changes", changeOf("Make it faster.")), http.StatusCreated, "")
}

// decideIdentity is an identity provider whose pass passAna is a principal
// of the decide grant.
func decideIdentity() ext.IdentityProvider {
	fake := exttest.NewFakeIdentity("cloud")
	fake.Add(passAna, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}})
	return fake
}

// Door 8: once the selection is applied, the selected candidate's own
// session (its repair attempt's) comments on its ticket and the guide
// names ticket_comment to it; its reviewer's session and the lane not
// selected stay refused 409 lane, the guide naming it to neither; before
// the application none comments.
func TestTicketCommentIsTakenFromTheSelectedLane(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	comment := func(sid string, status int) {
		t.Helper()
		got, raw, _ := de.callAs(t, de.agentToken(sid), "POST", "/api/sessions/"+sid+"/ticket/comments", map[string]any{"opId": opID(), "body": "the selected one speaks"})
		if got != status {
			t.Fatalf("%s's comment: %d %s, want %d", sid, got, raw, status)
		}
	}
	guided := func(sid string) bool {
		t.Helper()
		_, raw, _ := de.callAs(t, de.agentToken(sid), "GET", "/api/sessions/"+sid+"/guide?section=operations", nil)
		return strings.Contains(string(raw), `"ticket_comment"`)
	}
	comment(de.b, http.StatusConflict)
	de.selectB(t, did)
	comment(de.a, http.StatusConflict)
	if guided(de.a) {
		t.Error("the guide names ticket_comment to the lane not selected")
	}
	b := de.candidate(t, "approach-b")
	out := sendReview(t, de.testEnv, "CND-1", map[string]any{"opId": opID(), "candidate": b, "base": de.base(t), "verdict": "request_changes", "summary": "Again.", "findings": []map[string]any{changeOf("Name the index.")}, "byName": "Nate", "continueRun": true}, http.StatusCreated, "")
	run, _ := applicationOf(t, out)["run"].(string)
	if run == "" {
		t.Fatalf("the repair's application: %v", out)
	}
	b2 := runningAttempt(t, de.testEnv, run, "approach-b", 2)["sessionId"].(string)
	if !guided(b2) {
		t.Error("the guide does not name ticket_comment to the selected candidate")
	}
	comment(b2, http.StatusCreated)
	// Its review, once the repair is submitted, is the lane's but not the
	// candidate's.
	de.expectAs(t, de.agentToken(b2), "POST", "/api/sessions/"+b2+"/task/result", map[string]any{"summary": "named"}, http.StatusCreated, "")
	rev := runningAttempt(t, de.testEnv, run, "review-b", 2)["sessionId"].(string)
	if guided(rev) {
		t.Error("the guide names ticket_comment to the selected lane's reviewer")
	}
	comment(rev, http.StatusConflict)
}

// The server's Shutdown with a comparison selecting stops its run as it
// stops every run: a new server over the same data reads the record open
// with the same options and the comparison selecting; the cancel is
// refused 400 invalid, the record unchanged; the workbench answers it
// through the route, the comparison then answered, and Continue applies
// it.
func TestAShutdownKeepsTheSelection(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	options := func(e *testEnv) string {
		t.Helper()
		d := entityOf(t, e.expect(t, "GET", "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
		raw, _ := json.Marshal(d["request"].(map[string]any)["options"])
		return string(raw)
	}
	before := options(de.testEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	de.srv.Shutdown(ctx)
	again := de.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	d := entityOf(t, again.expect(t, "GET", "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
	if d["state"] != "open" || options(again) != before {
		t.Fatalf("after the restart: %v", d)
	}
	st, err := again.srv.runs.ComparisonStateOf(t.Context(), de.run)
	if err != nil || st.State != crew.CompareSelecting || st.Decision != did {
		t.Fatalf("the comparison after the restart: %+v %v", st, err)
	}
	rev := int(d["rev"].(float64))
	status, raw, out := again.callAs(t, adminToken, "POST", "/api/decisions/"+did+"/cancel", map[string]any{"commandId": opID(), "expectedRev": rev, "why": "restarted"})
	if status != http.StatusBadRequest || errorCode(out) != "invalid" {
		t.Fatalf("the cancel: %d %s", status, raw)
	}
	status, raw, _ = again.callAs(t, adminToken, "POST", "/api/decisions/"+did+"/respond", map[string]any{"responseId": opID(), "choice": 0, "rationale": "a held up", "expectedRev": rev})
	if status >= 300 {
		t.Fatalf("the answer: %d %s", status, raw)
	}
	_, raw, cmp := again.callAs(t, adminToken, "GET", "/api/runs/"+de.run+"/comparison", nil)
	if cmp["state"] != crew.CompareAnswered || cmp["selection"].(map[string]any)["state"] != "answered" {
		t.Fatalf("answered on a run not held: %s", raw)
	}
	again.expect(t, "POST", "/api/runs/"+de.run+"/resume", nil, http.StatusCreated, "")
	waitUntil(t, "the continuation's application", func() bool {
		st, err := again.srv.runs.ComparisonStateOf(t.Context(), de.run)
		return err == nil && st.State == crew.CompareSelected && st.Selected.Task == "approach-a"
	})
}

// I6, agreement is not proof: both candidates' summaries and approach-b's
// approving review favour approach-a, and Brakeman's brief recommends it,
// while approach-a's check failed on every attempt. The measured part
// shows approach-a's failed cells and nothing of what was said; the
// statements are under supporting alone; the selection's record
// recommends nothing, offers no failed candidate and says it failed.
func TestAgreementIsNeverEvidence(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{checks: []crew.Check{{ID: "speed", Argv: []string{"sh", "-c", `case "$PWD" in *approach-a*) exit 1;; esac`}}}})
	de.bk.enable(t, "bk", nil)
	const praise = "approach-a is the faster one, clearly"
	for n := 1; n <= 3; n++ {
		sid := runningAttempt(t, de.testEnv, de.run, "approach-a", n)["sessionId"].(string)
		de.expectAs(t, de.agentToken(sid), "POST", "/api/sessions/"+sid+"/task/result", map[string]any{"summary": praise}, http.StatusCreated, "")
	}
	de.expectAs(t, de.bTok, "POST", "/api/sessions/"+de.b+"/task/result", map[string]any{"summary": praise + ", mine is second"}, http.StatusCreated, "")
	rev := runningAttempt(t, de.testEnv, de.run, "review-b", 1)["sessionId"].(string)
	de.expectAs(t, de.agentToken(rev), "POST", "/api/sessions/"+rev+"/task/result", map[string]any{"verdict": crew.VerdictApproved, "summary": praise}, http.StatusCreated, "")
	did := de.waitState(t, crew.CompareSelecting).Decision
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, req)
	}
	sid, tok, _ := de.bk.turnSession(t, req["id"].(string))
	brief := de.compareBrief("recommend approach-a")
	brief["summary"] = praise
	if status, out := de.bk.submit(t, sid, tok, "b-1", brief); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	_, raw, out := de.readComparison(t, adminToken, "", de.run)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	measured := string(m["candidates"]) + string(m["checks"])
	if strings.Contains(measured, "faster") || strings.Contains(measured, "recommend") {
		t.Fatalf("the measured part holds what was said: %s", measured)
	}
	failed := false
	for _, ch := range out["checks"].([]any) {
		for _, cell := range ch.(map[string]any)["cells"].([]any) {
			cell := cell.(map[string]any)
			if cell["task"] == "approach-a" && cell["exit"] == float64(1) {
				failed = true
			}
		}
	}
	if !failed {
		t.Fatalf("approach-a's failed cell is not shown: %s", m["checks"])
	}
	if !strings.Contains(string(m["supporting"]), praise) {
		t.Fatalf("the statements are not under supporting: %s", m["supporting"])
	}
	d := entityOf(t, de.expect(t, "GET", "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
	request := d["request"].(map[string]any)
	words, _ := json.Marshal(request)
	// The context says approach-a failed; naming the failed check there
	// is the record's own words (internal/crew), not this row's.
	if request["recommendation"] != float64(-1) || !strings.Contains(request["context"].(string), "approach-a: no result (failed)") || strings.Contains(string(words), "faster") {
		t.Fatalf("the record: %s", words)
	}
}

// Brakeman drafts a comparison before its selection, never with the
// person's answer as its input: a turn asked while the comparison was
// selecting keeps no brief once the answer is applied, and a compare
// request asked after it is refused 409 selection_made, nothing kept.
func TestADraftIsMadeBeforeTheSelectionAlone(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", nil)
	did := de.selecting(t)
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != http.StatusCreated {
		t.Fatalf("ask while selecting: %d %v", status, req)
	}
	id := req["id"].(string)
	sid, tok, _ := de.bk.turnSession(t, id)
	de.selectB(t, did)
	status, out := de.bk.submit(t, sid, tok, "b-late", de.compareBrief("late"))
	if status != http.StatusConflict || errorCode(out) != "selection_made" {
		t.Fatalf("a brief after the answer: %d %v", status, out)
	}
	if r := de.bk.request(t, id); r["hasBrief"] == true {
		t.Fatalf("a brief was kept: %v", r)
	}
	before := len(de.compareRequests(t))
	status, _, out = de.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": opID(), "kind": "compare", "subjects": []string{"run:" + de.run}})
	if status != http.StatusConflict || errorCode(out) != "selection_made" {
		t.Fatalf("an ask after the answer: %d %v", status, out)
	}
	if n := len(de.compareRequests(t)); n != before {
		t.Fatalf("%d compare requests, %d before", n, before)
	}
}

// turnAs sends body to path as the Brakeman turn of the session sid.
func (de *cmpDraftEnv) turnAs(t *testing.T, tok, sid, method, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	status, _, out := de.callAsTurn(t, tok, sid, method, path, raw)
	return status, out
}

// A selection is routed by no Brakeman turn: a turn granted
// route_decision is refused 409 stale_target before its act is written;
// and a compare turn runs no act at all, whatever the person granted
// (403 not_granted, nothing written), its guide naming none.
func TestABrakemanTurnActsOnNoSelection(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	did := de.selecting(t)
	de.bk.enable(t, "bk", func(c map[string]any) {
		c["permissions"].(map[string]any)["act"] = []any{brakeman.ActRouteDecision, brakeman.ActRequestRepair}
	})
	id := de.bk.briefing(t)
	sid, tok, _ := de.bk.turnSession(t, id)
	de.bk.waitState(t, id, brakeman.StateRunning)
	route := map[string]any{"opId": "r-1", "expectedRev": de.decisionRev(t, did), "to": map[string]any{"kind": "operator"}}
	status, out := de.turnAs(t, tok, sid, http.MethodPost, "/api/decisions/"+did+"/route", route)
	if status != http.StatusConflict || errorCode(out) != "stale_target" {
		t.Fatalf("a briefing turn's route of the selection: %d %v", status, out)
	}
	if r, _ := de.bk.desk.st.Get(id); len(r.Acts) != 0 {
		t.Fatalf("acts written: %+v", r.Acts)
	}
	de.expect(t, "POST", "/api/runs/"+de.bk.runOf(t, sid)+"/stop", nil, http.StatusOK, "")
	de.bk.slotFree(t)

	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, req)
	}
	id = req["id"].(string)
	sid, tok, _ = de.bk.turnSession(t, id)
	de.bk.waitState(t, id, brakeman.StateRunning)
	route["opId"] = "r-2"
	repair := map[string]any{"opId": "q-1", "candidate": de.candidate(t, "approach-b"), "base": de.base(t), "verdict": "request_changes", "summary": "Again.", "findings": []map[string]any{changeOf("Name the index.")}}
	for path, body := range map[string]map[string]any{"/api/decisions/" + did + "/route": route, "/api/dispatch/tickets/CND-1/reviews": repair} {
		status, out := de.turnAs(t, tok, sid, http.MethodPost, path, body)
		if status != http.StatusForbidden || errorCode(out) != "not_granted" {
			t.Fatalf("a compare turn's act on %s: %d %v", path, status, out)
		}
	}
	if r, _ := de.bk.desk.st.Get(id); len(r.Acts) != 0 {
		t.Fatalf("acts written: %+v", r.Acts)
	}
	_, raw, _ := de.callAs(t, tok, "GET", "/api/sessions/"+sid+"/guide?section=operations", nil)
	for _, op := range []string{brakeman.ActRouteDecision, brakeman.ActRequestRepair} {
		if strings.Contains(string(raw), `"`+op+`"`) {
			t.Errorf("a compare turn's guide names %s", op)
		}
	}
}

// Door 8 is the candidate task's: in a crew whose member reviews its own
// lane's candidate too, the review's session is refused 409 lane after
// the application while the candidate's own repair session comments.
func TestTheSelectedCandidatesTaskCommentsNotItsReview(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{shape: func(c *crew.Crew) {
		for i := range c.Tasks {
			if c.Tasks[i].ID == "review-b" {
				c.Tasks[i].Member = "cand-b"
			}
		}
		for i := range c.Members {
			if c.Members[i].Name == "rev-b" {
				c.Members = append(c.Members[:i], c.Members[i+1:]...)
				break
			}
		}
	}})
	did := de.selecting(t)
	de.selectB(t, did)
	out := sendReview(t, de.testEnv, "CND-1", map[string]any{"opId": opID(), "candidate": de.candidate(t, "approach-b"), "base": de.base(t), "verdict": "request_changes", "summary": "Again.", "findings": []map[string]any{changeOf("Name the index.")}, "byName": "Nate", "continueRun": true}, http.StatusCreated, "")
	run, _ := applicationOf(t, out)["run"].(string)
	b2 := runningAttempt(t, de.testEnv, run, "approach-b", 2)["sessionId"].(string)
	de.expectAs(t, de.agentToken(b2), "POST", "/api/sessions/"+b2+"/ticket/comments", map[string]any{"opId": opID(), "body": "mine"}, http.StatusCreated, "")
	de.expectAs(t, de.agentToken(b2), "POST", "/api/sessions/"+b2+"/task/result", map[string]any{"summary": "named"}, http.StatusCreated, "")
	rev := runningAttempt(t, de.testEnv, run, "review-b", 2)["sessionId"].(string)
	de.expectAs(t, de.agentToken(rev), "POST", "/api/sessions/"+rev+"/ticket/comments", map[string]any{"opId": opID(), "body": "the review's"}, http.StatusConflict, "lane")
}

// Once the server's background wait has begun, a run's change starts no
// drafts' read: nothing is noted and no drain runs.
func TestNoDraftIsReadOnceTheServerStops(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	de.srv.WaitBackground(ctx)
	d := &de.srv.drafts
	d.runChanged(de.srv, de.run)
	d.mu.Lock()
	draining, pending := d.draining, len(d.pending)
	d.mu.Unlock()
	if draining || pending != 0 {
		t.Fatalf("after the background wait: draining %v, %d noted", draining, pending)
	}
}
