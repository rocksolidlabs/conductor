package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A comparison's Brakeman draft and its supporting part (CP1 PR-5): the
// compare request kind takes a comparison's run alone; a settled
// comparison whose block asks for a draft queues one request, once per
// event; the supporting part holds what agents said, at its bound.

// cmpDraftEnv is a compareEnv whose catalog holds Brakeman's scripted
// agents, with Brakeman's desk as a bkEnv sees it.
type cmpDraftEnv struct {
	*compareEnv
	bk *bkEnv
}

func newCmpDraftEnv(t *testing.T, o compareOpts) *cmpDraftEnv {
	t.Helper()
	o.agents = append(o.agents, bkAgents()...)
	ce := newCompareEnv(t, o)
	return &cmpDraftEnv{compareEnv: ce, bk: &bkEnv{testEnv: ce.testEnv, desk: ce.srv.brakeman}}
}

// compareRequests is every compare request Brakeman's store holds.
func (de *cmpDraftEnv) compareRequests(t *testing.T) []brakeman.Request {
	t.Helper()
	page, _, err := de.bk.desk.st.List("", brakeman.MaxPage)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(page, func(r brakeman.Request) bool { return r.Kind != brakeman.KindCompare })
}

// fakeComparison writes a run the engine does not hold into the ledger: a
// comparison of the env's crew whose block asks Brakeman for a draft when
// ask, made from the launch, with a compare.settled event; it answers the
// event's sequence.
func (de *cmpDraftEnv) fakeComparison(t *testing.T, id, launch string, ask bool) int64 {
	t.Helper()
	c := compareCrewIn(t, de.root)
	c.ID, c.Compare.Brakeman = "fake", ask
	crewJSON, _ := json.Marshal(c)
	pkg, _ := json.Marshal(map[string]string{"launch": launch})
	if _, err := de.srv.ledger.Transition(t.Context(), id, func(tx *ledger.Tx) error {
		if err := tx.PutRun(ledger.RunRow{ID: id, CrewID: "fake", Crew: string(crewJSON), Base: "base0", State: ledger.RunRunning, CreatedAt: tx.Now(), Package: string(pkg)}); err != nil {
			return err
		}
		return tx.Event(eventCompareSettled, map[string]any{"candidates": []any{}})
	}); err != nil {
		t.Fatal(err)
	}
	events, err := de.srv.ledger.Events(t.Context(), id, 0, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("the events: %v %v", events, err)
	}
	return events[len(events)-1].Seq
}

// wake tells the drafts the run changed, as the engine's OnRunChange
// does, and waits for their drain to end.
func (de *cmpDraftEnv) wake(t *testing.T, run string) {
	t.Helper()
	de.srv.drafts.runChanged(de.srv, run)
	waitUntil(t, "the drafts' drain", func() bool {
		d := &de.srv.drafts
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.draining
	})
}

// A compare request names one run, of a comparison: the comparison's
// launched run is taken; another run, a run of no record, another ref
// kind and two subjects are refused 400 invalid_request before anything
// is kept; and an op id the drafts' own prefix begins is a person's to
// choose no more than a wake's.
func TestCompareRequestTakesACompareRun(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", nil)
	for _, c := range []struct {
		subjects []string
		field    string
	}{
		{[]string{"run:" + de.plain}, "subjects[0]"},
		{[]string{"run:nope-0000abcd"}, "subjects[0]"},
		{[]string{"task:" + de.run + "/approach-a"}, "subjects[0]"},
		{[]string{"ticket:CND-1"}, "subjects[0]"},
		{[]string{"run:" + de.run, "run:" + de.plain}, "subjects"},
		{nil, "subjects"},
	} {
		status, _, out := de.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": opID(), "kind": "compare", "subjects": c.subjects})
		if status != http.StatusBadRequest || errorCode(out) != "invalid_request" || fieldOf(out) != c.field {
			t.Errorf("%v: %d %v", c.subjects, status, out)
		}
	}
	if status, _, out := de.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": compareOpID(de.run, 1), "kind": "compare", "subjects": []string{"run:" + de.run}}); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Errorf("the drafts' op id: %d %v", status, out)
	}
	if got := de.compareRequests(t); len(got) != 0 {
		t.Fatalf("refused asks kept %v", got)
	}
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != http.StatusCreated || req["kind"] != "compare" {
		t.Fatalf("the comparison's run: %d %v", status, req)
	}
}

// fieldOf is a refusal's field.
func fieldOf(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	f, _ := e["field"].(string)
	return f
}

// The op id of a settled comparison's request is cmp- and 24 hex digits
// of the run and the event's sequence: within Brakeman's op id pattern
// with a run id at its longest and a sequence past a million, the same
// for the same event and another for another.
func TestTheDraftsOpIDIsTheEvents(t *testing.T) {
	long := strings.Repeat("c", 64) + "-0123abcd"
	if !crew.ValidRunID(long) {
		t.Fatalf("%s is no run id", long)
	}
	pattern := regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)
	id := compareOpID(long, 1_000_001)
	if len(id) != 28 || !strings.HasPrefix(id, "cmp-") || !pattern.MatchString(id) || !regexp.MustCompile(`^cmp-[0-9a-f]{24}$`).MatchString(id) {
		t.Fatalf("op id %q", id)
	}
	if compareOpID(long, 1_000_001) != id || compareOpID(long, 1_000_002) == id || compareOpID(long[1:], 1_000_001) == id {
		t.Fatal("the op id is not the event's alone")
	}
	// The run and the sequence are kept apart: run "a-1" at 23 is not run
	// "a-12" at 3.
	if compareOpID("a-1", 23) == compareOpID("a-12", 3) {
		t.Fatal("the run and the sequence run together")
	}
	in := brakeman.Request{OpID: id, Kind: brakeman.KindCompare, Subjects: []brakeman.Ref{{Kind: brakeman.RefRun, Run: long}}}
	if raw, _ := json.Marshal(in); strings.Contains(string(raw), `"via"`) {
		t.Fatalf("a request carries a via: %s", raw)
	}
}

// The drafts queue one request for a committed compare.settled of a
// comparison whose block asks for one: by its launch's maker, about its
// run, under the event's op id, with no via; a second wake and a second
// server queue no second; a block that does not ask, Brakeman off,
// Brakeman not seeing runs and an event before the server started queue
// none; and with no request the supporting part says so and Ask Brakeman
// to compare queues one.
func TestTheDraftsQueueOnceAnEvent(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", nil)
	done := doneOf(t)
	launch := done(de.srv.dispatch.Launch(t.Context(), adminOp("Ann Example"), "CND-1", de.plan, "CND-1 Compare")).ID

	run := "fake-0000aaa1"
	seq := de.fakeComparison(t, run, launch, true)
	if got := de.srv.draftOf([]string{run}); got != (supportBrakeman{}) {
		t.Fatalf("before the wake: %+v", got)
	}
	de.wake(t, run)
	reqs := de.compareRequests(t)
	if len(reqs) != 1 {
		t.Fatalf("requests %v", reqs)
	}
	r := reqs[0]
	if r.OpID != compareOpID(run, seq) || len(r.Subjects) != 1 || r.Subjects[0].String() != "run:"+run || r.By != "Ann Example" {
		t.Fatalf("the request %+v", r)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), `"via"`) {
		t.Fatalf("the request carries a via: %s", raw)
	}
	if got := de.srv.draftOf([]string{"other-0000aaa9", run}); got.Request != r.ID || got.Why != "" {
		t.Fatalf("the supporting part's draft: %+v", got)
	}
	// A second wake, and the drafts as a server started anew finds them:
	// the event is read again and its request found, none made.
	de.wake(t, run)
	de.srv.drafts.mu.Lock()
	de.srv.drafts.runs = nil
	de.srv.drafts.mu.Unlock()
	de.wake(t, run)
	if got := de.compareRequests(t); len(got) != 1 {
		t.Fatalf("a second wake: %v", got)
	}

	// A block that does not ask Brakeman queues nothing.
	quiet := "fake-0000aaa2"
	de.fakeComparison(t, quiet, launch, false)
	de.wake(t, quiet)
	// Brakeman not seeing runs queues nothing, and the comparison says so.
	unseen := "fake-0000aaa3"
	de.bk.configure(t, func(c map[string]any) { c["visibility"].(map[string]any)["runs"] = false })
	de.fakeComparison(t, unseen, launch, true)
	de.wake(t, unseen)
	if got := de.srv.draftOf([]string{unseen}); got.Why != draftNoRunsClass || got.Request != "" {
		t.Fatalf("runs unseen: %+v", got)
	}
	de.bk.configure(t, func(c map[string]any) { c["visibility"].(map[string]any)["runs"] = true })
	// Brakeman off queues nothing, and the comparison says it is not set up.
	off := "fake-0000aaa4"
	de.bk.configure(t, func(c map[string]any) { c["enabled"] = false })
	de.fakeComparison(t, off, launch, true)
	de.wake(t, off)
	if got := de.srv.draftOf([]string{off}); got.Why != draftNotSetUp {
		t.Fatalf("Brakeman off: %+v", got)
	}
	de.bk.configure(t, func(c map[string]any) { c["enabled"] = true })
	// An event committed before the server started asks nothing.
	early := "fake-0000aaa5"
	de.fakeComparison(t, early, launch, true)
	de.srv.drafts.since = time.Now().Add(time.Hour)
	de.wake(t, early)
	if got := de.compareRequests(t); len(got) != 1 {
		t.Fatalf("requests past the first: %v", got)
	}
	// With none made, Ask Brakeman to compare queues one, which the
	// comparison then shows.
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + early}})
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, req)
	}
	if got := de.srv.draftOf([]string{early}); got.Request != req["id"] {
		t.Fatalf("asked by hand: %+v", got)
	}
}

// Past the day's turns the comparison's request is queued all the same,
// where a person's ask is refused, and its start records it over_budget,
// which the comparison says.
func TestTheDraftPastTheBudgetIsRecordedOverBudget(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", func(c map[string]any) { c["budget"].(map[string]any)["turnsPerDay"] = 1 })
	first := de.bk.briefing(t)
	sid, _, _ := de.bk.turnSession(t, first)
	if status, _ := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}}); status != http.StatusTooManyRequests {
		t.Fatalf("a person's ask past the budget: %d", status)
	}
	run := "fake-0000bbb1"
	de.fakeComparison(t, run, "", true)
	de.wake(t, run)
	reqs := de.compareRequests(t)
	if len(reqs) != 1 {
		t.Fatalf("requests %v", reqs)
	}
	de.expect(t, "POST", "/api/runs/"+de.bk.runOf(t, sid)+"/stop", nil, http.StatusOK, "")
	de.bk.waitState(t, reqs[0].ID, brakeman.StateOverBudget)
	if got := de.srv.draftOf([]string{run}); got.State != brakeman.StateOverBudget || got.Why != draftOverBudget || got.Request != reqs[0].ID {
		t.Fatalf("the supporting part: %+v", got)
	}
}

// The supporting part keeps its bound: four candidates' summaries of
// every character '<' cut at 4 KiB as encoded, their reviews' thirty
// findings cut to twenty titles of 300 bytes, together under 48 KiB; the
// review read is the newest of the candidate's commit, and a candidate
// with no result has no claim.
func TestTheSupportingPartKeepsItsBound(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	run := "wide-0000ccc1"
	tasks := []crew.Task{}
	var cands []crew.ComparedCandidate
	long := strings.Repeat("<", 10<<10)
	var findings []crew.Finding
	for range 30 {
		findings = append(findings, crew.Finding{Severity: "major", Path: "a.go", Line: 3, Message: strings.Repeat("<", 1000) + "\nmore"})
	}
	for _, l := range []string{"a", "b", "c", "d"} {
		tasks = append(tasks, crew.Task{ID: "approach-" + l, Member: "cand-" + l, Kind: crew.TaskWork}, crew.Task{ID: "review-" + l, Member: "rev-" + l, Kind: crew.TaskReview, Needs: []string{"approach-" + l}})
		cands = append(cands, crew.ComparedCandidate{Task: "approach-" + l, Run: run, Attempt: 1, Commit: "c0mmit" + l})
	}
	crewJSON, _ := json.Marshal(crew.Crew{ID: "wide", Name: "wide", Tasks: tasks})
	result := func(r crew.Result) string { b, _ := json.Marshal(r); return string(b) }
	if _, err := de.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		now := tx.Now()
		if err := tx.PutRun(ledger.RunRow{ID: run, CrewID: "wide", Crew: string(crewJSON), Base: "base0", State: ledger.RunRunning, CreatedAt: now}); err != nil {
			return err
		}
		for _, task := range tasks {
			if err := tx.PutTask(ledger.TaskRow{ID: task.ID, Member: task.Member, Kind: task.Kind, State: ledger.TaskSucceeded}); err != nil {
				return err
			}
		}
		for _, l := range []string{"a", "b", "c", "d"} {
			rows := []ledger.AttemptRow{
				{TaskID: "approach-" + l, N: 1, Member: "cand-" + l, State: ledger.AttemptSucceeded, StartCommit: "base0", ResultCommit: "c0mmit" + l, Result: result(crew.Result{Summary: long})},
				// A review of an earlier commit, and two of the candidate's: the newest is read.
				{TaskID: "review-" + l, N: 1, Member: "rev-" + l, State: ledger.AttemptSucceeded, StartCommit: "earlier", Result: result(crew.Result{Verdict: crew.VerdictApproved})},
				{TaskID: "review-" + l, N: 2, Member: "rev-" + l, State: ledger.AttemptSucceeded, StartCommit: "c0mmit" + l, Result: result(crew.Result{Verdict: crew.VerdictApproved})},
				{TaskID: "review-" + l, N: 3, Member: "rev-" + l, State: ledger.AttemptSucceeded, StartCommit: "c0mmit" + l, Result: result(crew.Result{Verdict: crew.VerdictNeedsChanges, Findings: findings})},
			}
			if l == "d" {
				rows[0].Result, rows[0].State = "", ledger.AttemptFailed
			}
			for _, a := range rows {
				a.Where, a.ReservedAt = ledger.WhereNative, now
				if err := tx.PutAttempt(a); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sup := de.srv.comparisonSupporting(t.Context(), crew.Comparison{Run: run, Candidates: cands})
	enc, err := json.Marshal(sup)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > maxSupporting {
		t.Fatalf("the supporting part is %d bytes, past %d", len(enc), maxSupporting)
	}
	if len(sup.Claims) != 3 || len(sup.Reviews) != 3 {
		t.Fatalf("claims %d, reviews %d", len(sup.Claims), len(sup.Reviews))
	}
	for _, c := range sup.Claims {
		if e, _ := json.Marshal(c.Summary); len(e)-2 > maxClaimSummary || len(e)-2 < maxClaimSummary-6 {
			t.Errorf("%s's summary is %d bytes encoded", c.Task, len(e)-2)
		}
	}
	for _, r := range sup.Reviews {
		if r.Attempt != 3 || r.Verdict != crew.VerdictNeedsChanges || len(r.Findings) != maxReviewFindings {
			t.Errorf("review %s/%d %s with %d findings", r.Task, r.Attempt, r.Verdict, len(r.Findings))
		}
		for _, f := range r.Findings {
			if e, _ := json.Marshal(f); len(e)-2 > maxFindingTitle || strings.Contains(f, "more") || !strings.HasPrefix(f, "major a.go:3: ") {
				t.Errorf("a finding's title of %d bytes: %.40q", len(e)-2, f)
			}
		}
	}
	// No field counts agreement.
	keys := strings.ToLower(strings.Join(jsonKeysOf(t, enc), " "))
	for _, word := range []string{"agree", "count", "score", "vote"} {
		if strings.Contains(keys, word) {
			t.Errorf("the supporting part has a field of %s: %s", word, keys)
		}
	}
}

// jsonKeysOf is every object key in the JSON document, at any depth.
func jsonKeysOf(t *testing.T, doc []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				out = append(out, k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// cutEncoded counts what encoding/json writes.
func TestCutEncodedCountsTheEscapes(t *testing.T) {
	for _, s := range []string{strings.Repeat("<", 100), strings.Repeat("é", 100), strings.Repeat("\"\n", 50), strings.Repeat("\x01", 50), "a\xffb c" + strings.Repeat("&", 40), strings.Repeat("ab", 60)} {
		for _, n := range []int{0, 1, 5, 6, 7, 40, 99, 1000} {
			got := cutEncoded(s, n)
			enc, _ := json.Marshal(got)
			if len(enc)-2 > n || !strings.HasPrefix(s, got) {
				t.Errorf("%.10q at %d: %d bytes encoded", s, n, len(enc)-2)
			}
			if next := s[len(got):]; next != "" {
				_, size := utf8.DecodeRuneInString(next)
				more, _ := json.Marshal(s[:len(got)+size])
				if len(more)-2 <= n {
					t.Errorf("%.10q at %d: cut short", s, n)
				}
			}
		}
	}
}

// compareBrief is a brief a compare request's check takes for the env's
// comparison, every text of it carrying mark: a fact and the table, each
// citing approach-a's first attempt.
func (de *cmpDraftEnv) compareBrief(mark string) map[string]any {
	cand := "candidate:" + de.run + "/approach-a/1"
	var rows []any
	for _, l := range []string{"Behaviour", "Complexity", "Dependency changes", "Checks", "Unresolved tradeoffs"} {
		rows = append(rows, map[string]any{"label": l, "cells": []any{mark + " a", mark + " b"}})
	}
	return map[string]any{"schema": brakeman.BriefSchema, "title": mark, "summary": mark + ": approach-a caches.",
		"items": []any{
			map[string]any{"kind": brakeman.ItemFact, "text": mark + " approach-a submitted.", "evidence": []any{cand}},
			map[string]any{"kind": brakeman.ItemComparison, "title": mark, "columns": []any{"approach-a", "approach-b"}, "rows": rows, "evidence": []any{cand}},
		}}
}

// Door 11: a lane's token is refused Brakeman's four tools (its request,
// its brief, a read, an act), 409 lane; and once a compare brief is kept,
// a lane's attempt started after it reads nothing of it in its task file,
// its run, its ticket or its inbox.
func TestTheDraftReachesNoLane(t *testing.T) {
	de := newCmpDraftEnv(t, compareOpts{})
	de.bk.enable(t, "bk", nil)
	for _, pattern := range []string{
		"GET /api/sessions/{id}/brakeman/request",
		"POST /api/sessions/{id}/brakeman/brief",
		"GET /api/brakeman/changes",
		"GET /api/runs/{run}/candidates",
		"POST /api/dispatch/tickets/{key}/launch",
	} {
		status, raw, out := de.callDoor(t, de.aTok, pattern, de.a)
		laneRefused(t, pattern, status, raw, out)
	}
	// approach-a submits, so its candidate is there to cite.
	de.expectAs(t, de.aTok, "POST", "/api/sessions/"+de.a+"/task/result", map[string]any{"summary": "cached"}, http.StatusCreated, "")
	status, req := de.bk.ask(t, "compare", map[string]any{"subjects": []string{"run:" + de.run}})
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, req)
	}
	id := req["id"].(string)
	sid, tok, _ := de.bk.turnSession(t, id)
	const mark = "BRAKEMANDRAFT7731"
	if status, out := de.bk.submit(t, sid, tok, "b-1", de.compareBrief(mark)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	// approach-b runs again: its attempt 2 starts after the brief.
	de.expect(t, "POST", "/api/runs/"+de.run+"/tasks/approach-b/cancel", map[string]any{"commandId": opID()}, http.StatusOK, "")
	taskVia(t, de.testEnv, de.run, "approach-b", "cancelled", func(task map[string]any) bool { return task["state"] == "cancelled" })
	de.expect(t, "POST", "/api/runs/"+de.run+"/tasks/approach-b/retry", map[string]any{"commandId": opID()}, http.StatusOK, "")
	b2 := runningAttempt(t, de.testEnv, de.run, "approach-b", 2)["sessionId"].(string)
	b2Tok := de.agentToken(b2)
	if b2 == de.b {
		t.Fatal("approach-b's attempt 2 has attempt 1's session")
	}
	for _, path := range []string{"/task", "/run", "/ticket", "/inbox"} {
		status, raw, _ := de.callAs(t, b2Tok, "GET", "/api/sessions/"+b2+path, nil)
		if status != http.StatusOK || strings.Contains(string(raw), mark) || strings.Contains(string(raw), id) {
			t.Errorf("%s: %d %s", path, status, raw)
		}
	}
}
