package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A comparison over HTTP (docs/plans/compare-crew.md, PR-3): the
// template's route, the plan revision's statement of what a comparison's
// launch starts, and the launch that must repeat the count.

// newCompareTicket is a planTest whose catalog has ready-cat, every run
// the engine holds stopped when the test ends.
func newCompareTicket(t *testing.T) *planTest {
	t.Helper()
	p := newPlanTest(t, nil, readyCat)
	srv := p.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	return p
}

// compareSpecBody is a template's body of n candidates on ready-cat in
// repo, one check and one criterion, reviewed when reviews, with the
// extra fields over it.
func compareSpecBody(name, repo string, n int, reviews bool, extra map[string]any) map[string]any {
	var cands []map[string]any
	for i := range n {
		cands = append(cands, map[string]any{"agentId": "ready-cat", "approach": fmt.Sprintf("approach number %d", i+1)})
	}
	b := map[string]any{"name": name, "cwd": repo, "goal": "Make the lookup fast.", "instructions": "Make it fast.",
		"criteria": []map[string]any{{"id": "fast", "text": "The lookup is fast."}},
		"checks":   []map[string]any{{"id": "ok", "argv": []string{"true"}}}, "candidates": cands}
	if reviews {
		b["reviews"], b["reviewAgent"] = true, "ready-cat"
	}
	maps.Copy(b, extra)
	return b
}

// compareRevision makes the comparison's crew of body through the
// template's route and an approved plan revision of it for CND-1, and
// returns the revision's id.
func compareRevision(t *testing.T, p *planTest, body map[string]any) string {
	t.Helper()
	c := entityOf(t, p.expect(t, "POST", "/api/crews/templates/compare", body, http.StatusCreated, ""), "crew")
	plan := p.proposed(t, c["id"].(string), nil)
	p.expect(t, "POST", "/api/dispatch/plans/"+plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	return plan
}

// The template's route makes a comparison's crew and saves it as POST
// /api/crews does: 201 {crew, revision}, the crew read back the same,
// launched by nothing. A rule broken is 400 invalid_crew in the rule's
// words, an agent the catalog has not too; a field the spec has not, a
// body that is not one object and one past 64 KiB are 400
// invalid_request; an agent's session token and a share link are refused
// 401. Nothing is saved for a refusal.
func TestCompareTemplateRoute(t *testing.T) {
	p := newCompareTicket(t)
	out := p.expect(t, "POST", "/api/crews/templates/compare", compareSpecBody("  Compare the lookup  ", p.repo, 3, true, nil), http.StatusCreated, "")
	c := entityOf(t, out, "crew")
	cmp, _ := c["compare"].(map[string]any)
	if c["name"] != "Compare the lookup" || cmp == nil || len(cmp["candidates"].([]any)) != 3 || len(c["tasks"].([]any)) != 6 || out["revision"] == "" {
		t.Fatalf("the crew made: %v", out)
	}
	read := p.expect(t, "GET", "/api/crews/"+c["id"].(string), nil, http.StatusOK, "")
	if read["revision"] != out["revision"] {
		t.Fatalf("read back: %v, made %v", read["revision"], out["revision"])
	}
	if runs := p.srv.runs.List(); len(runs) != 0 {
		t.Fatalf("the template launched %d runs", len(runs))
	}
	saved := func() int {
		t.Helper()
		_, n, err := p.srv.crews.List(0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := saved()
	for _, tc := range []struct {
		name  string
		body  any
		code  string
		words string
	}{
		{"one candidate", compareSpecBody("One", p.repo, 1, false, nil), "invalid_crew", "2 to 4 candidates, not 1"},
		{"five candidates", compareSpecBody("Five", p.repo, 5, false, nil), "invalid_crew", "2 to 4 candidates, not 5"},
		{"a review agent without reviews", compareSpecBody("Rev", p.repo, 2, false, map[string]any{"reviewAgent": "ready-cat"}), "invalid_crew", "reviews need a reviewAgent"},
		{"no check and no manifest", compareSpecBody("Nothing", p.repo, 2, false, map[string]any{"checks": nil}), "invalid_crew", "nothing to compare"},
		{"an agent the catalog has not", compareSpecBody("Gone", p.repo, 2, false, map[string]any{"candidates": []map[string]any{{"agentId": "ready-cat"}, {"agentId": "no-such-agent"}}}), "invalid_crew", "no-such-agent"},
		{"a field the spec has not", compareSpecBody("Extra", p.repo, 2, false, map[string]any{"selector": "role:architecture"}), "invalid_request", "unknown field"},
		{"a crew's own field", compareSpecBody("Id", p.repo, 2, false, map[string]any{"id": "chosen"}), "invalid_request", "unknown field"},
		{"two objects", []byte(`{"name":"a"}{"name":"b"}`), "invalid_request", "a single JSON object"},
		{"past 256 KiB", compareSpecBody("Big", p.repo, 2, false, map[string]any{"goal": strings.Repeat("x", maxCompareSpecBody)}), "invalid_request", ""},
	} {
		status, raw, got := p.call(t, "POST", "/api/crews/templates/compare", tc.body)
		if status != http.StatusBadRequest || errorCode(got) != tc.code || !strings.Contains(apiMessage(got), tc.words) {
			t.Errorf("%s: %d %.300s, want 400 %s %q", tc.name, status, raw, tc.code, tc.words)
		}
	}
	if n := saved(); n != before {
		t.Fatalf("%d crews saved after the refusals, %d before", n, before)
	}

	// The route is the admin's: an agent's session token, a share link
	// (a controller's) and no token are refused, nothing saved.
	_, share, err := p.srv.links.Create("s1", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lt := &launchTest{planTest: p, crew: p.saveCrew(t, teamCrew("Team", "ready-cat"))}
	lt.plan = lt.approved(t, nil)
	run := entityOf(t, lt.launch(t, p.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	sid := runningAttempt(t, p.testEnv, run, "build", 1)["sessionId"].(string)
	p.srv.limiter = newRateLimiter(1e6, 1e6)
	before = saved()
	for name, tok := range map[string]string{"an agent's token": p.agentToken(sid), "a share link": share, "no token": ""} {
		if resp, got := p.do("POST", "/api/crews/templates/compare", tok, compareSpecBody("By "+name, p.repo, 2, false, nil)); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %v", name, resp.StatusCode, got)
		}
	}
	if n := saved(); n != before {
		t.Fatal("a refused caller saved a crew")
	}
}

// I11: a comparison's plan revision says what its launch starts, computed
// from its body and never stored: the candidates, the review tasks (each
// a session only after its candidate succeeds), the checks each candidate
// runs at the base (its task's and the manifest's verify steps, the crew's
// form or the file form's read at the revision's base) and whether
// Brakeman drafts; never a total. A revision of no comparison says none.
// The ticket's list and the revision's own read say the same.
func TestThePlanReadStatesTheMultiplier(t *testing.T) {
	p := newCompareTicket(t)
	commitFile(t, p.repo, "conductor.manifest.json", `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}, {"id": "unit", "argv": ["true"]}]}`)
	verify := []map[string]any{{"id": "vet", "argv": []string{"true"}}}
	for _, tc := range []struct {
		name string
		body map[string]any
		want map[string]any
	}{
		{"two reviewed", compareSpecBody("Two", p.repo, 2, true, nil), map[string]any{"candidates": 2.0, "reviews": 2.0, "checksAtBase": 1.0, "brakemanTurn": false}},
		{"four, the crew's manifest, Brakeman", compareSpecBody("Four", p.repo, 4, false, map[string]any{"manifest": map[string]any{"verify": verify}, "brakeman": true}),
			map[string]any{"candidates": 4.0, "reviews": 0.0, "checksAtBase": 2.0, "brakemanTurn": true}},
		{"four reviewed", compareSpecBody("Four reviewed", p.repo, 4, true, nil), map[string]any{"candidates": 4.0, "reviews": 4.0, "checksAtBase": 1.0, "brakemanTurn": false}},
		{"two, the file form", compareSpecBody("File", p.repo, 2, false, map[string]any{"checks": nil, "manifest": map[string]any{"file": true}}),
			map[string]any{"candidates": 2.0, "reviews": 0.0, "checksAtBase": 2.0, "brakemanTurn": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := compareRevision(t, p, tc.body)
			got := entityOf(t, p.expect(t, "GET", "/api/dispatch/plans/"+plan, nil, http.StatusOK, ""), "plan")
			if !maps.Equal(got["compare"].(map[string]any), tc.want) {
				t.Fatalf("the revision's compare: %v, want %v", got["compare"], tc.want)
			}
			// The revision's count of checks is the statement's: the
			// candidates' one set, not a set for each candidate.
			if got["expectedChecks"] != tc.want["checksAtBase"] {
				t.Fatalf("the revision expects %v checks, its launch states %v", got["expectedChecks"], tc.want["checksAtBase"])
			}
			var listed map[string]any
			for _, v := range p.expect(t, "GET", "/api/dispatch/tickets/CND-1/plans", nil, http.StatusOK, "")["plans"].([]any) {
				if v.(map[string]any)["id"] == plan {
					listed = v.(map[string]any)
				}
			}
			if listed == nil || !maps.Equal(listed["compare"].(map[string]any), tc.want) {
				t.Fatalf("the list's compare: %v", listed)
			}
		})
	}
	plain := p.proposed(t, p.saveCrew(t, teamCrew("Plain", "ready-cat")).ID, nil)
	if got := entityOf(t, p.expect(t, "GET", "/api/dispatch/plans/"+plain, nil, http.StatusOK, ""), "plan"); got["compare"] != nil {
		t.Fatalf("an ordinary revision's compare: %v", got["compare"])
	}
}

// I11: the launch of a comparison's revision must repeat its count. No
// count, or another, is 409 confirm_candidates in words that say it, and
// nothing is kept, so the same op id may be sent again with the count; the
// count launches the comparison's run; the same body again answers that
// launch; the same op id with another count is op_conflict; a count on an
// ordinary revision is 400 invalid, nothing kept; a count below 0 is 400
// invalid_request.
func TestACompareLaunchSaysHowMany(t *testing.T) {
	p := newCompareTicket(t)
	plan := compareRevision(t, p, compareSpecBody("Three", p.repo, 3, false, nil))
	lt := &launchTest{planTest: p, plan: plan}
	words := "this revision starts 3 candidates: send candidates 3 to launch them"
	op := opID()
	for _, body := range []map[string]any{{"opId": op}, {"opId": op, "candidates": 2}, {"opId": op, "candidates": 4}} {
		if out := lt.launch(t, p.testEnv, body, http.StatusConflict, dispatch.RefusalConfirmCandidates); apiMessage(out) != words || out["receipt"] != nil {
			t.Fatalf("%v: %v", body, out)
		}
	}
	lt.launch(t, p.testEnv, map[string]any{"candidates": -1}, http.StatusBadRequest, "invalid_request")
	if n := len(lt.launches(t)); n != 0 {
		t.Fatalf("%d launches kept for the refusals", n)
	}
	out := lt.launch(t, p.testEnv, map[string]any{"opId": op, "candidates": 3}, http.StatusCreated, "")
	launch := entityOf(t, out, "launch")
	run := entityOf(t, out, "run")
	cmp, _ := run["compare"].(map[string]any)
	if cmp == nil || len(cmp["candidates"].([]any)) != 3 {
		t.Fatalf("the run launched: %v", run)
	}
	again := lt.launch(t, p.testEnv, map[string]any{"opId": op, "candidates": 3}, http.StatusOK, "")
	if entityOf(t, again, "launch")["id"] != launch["id"] {
		t.Fatalf("the resend: %v", again)
	}
	lt.launch(t, p.testEnv, map[string]any{"opId": op, "candidates": 2}, http.StatusConflict, dispatch.RefusalOpConflict)
	if n := len(lt.launches(t)); n != 1 {
		t.Fatalf("%d launches, want 1", n)
	}
	// The run's read says it is a comparison, with its lanes; an ordinary
	// run's says nothing of it.
	got := entityOf(t, p.expect(t, "GET", "/api/runs/"+launch["runId"].(string), nil, http.StatusOK, ""), "run")
	if lanes := got["compare"].(map[string]any)["candidates"].([]any); len(lanes) != 3 || lanes[0].(map[string]any)["task"] != "approach-a" {
		t.Fatalf("the run's compare: %v", got["compare"])
	}

	ordinary := &launchTest{planTest: p, crew: p.saveCrew(t, teamCrew("Team", "ready-cat"))}
	ordinary.plan = ordinary.approved(t, nil)
	if out := ordinary.launch(t, p.testEnv, map[string]any{"candidates": 1}, http.StatusBadRequest, dispatch.RefusalInvalid); !strings.Contains(apiMessage(out), "starts none") {
		t.Fatalf("a count on an ordinary revision: %v", out)
	}
	plainOut := ordinary.launch(t, p.testEnv, nil, http.StatusCreated, "")
	if r := entityOf(t, plainOut, "run"); r["compare"] != nil {
		t.Fatalf("an ordinary run's compare: %v", r["compare"])
	}
	if n := len(lt.launches(t)); n != 2 {
		t.Fatalf("%d launches, want 2", n)
	}
}

// The guide lists the comparison's routes as the admin's, with their
// commands and bounds, and the tools an agent's MCP client lists are
// unchanged (no tool of them).
func TestComparisonGuideRows(t *testing.T) {
	ops := operationsTable()
	for _, want := range []struct{ name, method, path string }{
		{"crew_compare", http.MethodPost, "/api/crews/templates/compare"},
		{"comparison", http.MethodGet, "/api/runs/{run}/comparison"},
	} {
		i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == want.name })
		if i < 0 {
			t.Fatalf("no operation %s", want.name)
		}
		o := ops[i]
		if o.Scope != "admin" || o.Method != want.method || o.Path != want.path || o.MCP != "" || len(o.CLI) == 0 || len(o.Bounds) == 0 {
			t.Fatalf("%s: %+v", want.name, o)
		}
		if want.name == "comparison" && o.Bounds["reply"] != crew.MaxComparison {
			t.Fatalf("the comparison's bound: %v", o.Bounds)
		}
	}
}

// I7: a comparison is launched by a person, who reads what it starts. A
// provider's launch through ext.Host's LaunchApproved, and Brakeman's
// granted launch act (the desk's Host), of a comparison's revision are
// refused before anything is written, in words, with no launch, no
// receipt and no run; the act's entry keeps the words. The act's body
// takes no count at all. An ordinary revision is launched by both.
func TestAComparisonIsLaunchedByAPerson(t *testing.T) {
	f := newActFixture(t, everyGrant, nil)
	cloud := testHost(f.srv, t.Context(), "cloud")
	if ref, err := cloud.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: f.plan, OpID: "p-plain", By: ada}); err != nil || ref.ID == "" {
		t.Fatalf("a provider's launch of an ordinary revision: %+v %v", ref, err)
	}
	if status, out := f.launchAct(t, map[string]any{"opId": "a-plain", "expectedRev": f.ticketRev(t)}); status != http.StatusCreated {
		t.Fatalf("the act's launch of an ordinary revision: %d %v", status, out)
	}
	launched := len(f.launches(t))
	runs := len(f.srv.runs.List())

	plan := compareRevision(t, f.planTest, compareSpecBody("Compared", f.repo, 2, false, nil))
	refused := func(what string, err error) {
		t.Helper()
		var ae *apiError
		if !errors.Is(err, ext.ErrRefused) || !errors.As(err, &ae) || ae.Code != "refused" || ae.Message != personLaunchesWords {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := cloud.LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: plan, OpID: "p-cmp", By: ada})
	refused("a provider's launch", err)
	_, err = f.srv.BrakemanHost().LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: plan, OpID: "p-desk", By: ada})
	refused("the desk's launch", err)
	status, out := f.launchAct(t, map[string]any{"opId": "a-cmp", "plan": plan, "expectedRev": f.ticketRev(t)})
	if status != http.StatusConflict || codeOf(out) != "refused" || apiMessage(out) != personLaunchesWords {
		t.Fatalf("the act's launch: %d %v", status, out)
	}
	i := slices.IndexFunc(f.acts(t), func(a brakeman.Act) bool { return a.OpID == "a-cmp" })
	if i < 0 || f.acts(t)[i].State != brakeman.ActRefused || f.acts(t)[i].Why != personLaunchesWords {
		t.Fatalf("the act's entry: %+v", f.acts(t))
	}
	// The act takes no count: the person's alone.
	if status, out := f.launchAct(t, map[string]any{"opId": "a-count", "plan": plan, "candidates": 2}); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
		t.Fatalf("the act with a count: %d %v", status, out)
	}
	for actor, id := range map[string]string{ext.SubjectActor + ada.Subject: "p-cmp", brakemanActor: "p-desk"} {
		if _, found, err := f.srv.dispatch.Receipt(t.Context(), actor, id); err != nil || found {
			t.Fatalf("a receipt of %s kept: %v %v", id, found, err)
		}
	}
	if n := len(f.launches(t)); n != launched {
		t.Fatalf("%d launches, %d before", n, launched)
	}
	if n := len(f.srv.runs.List()); n != runs {
		t.Fatalf("%d runs, %d before", n, runs)
	}
	// The person's launch of it, with the count, is taken.
	f.launch(t, f.testEnv, map[string]any{"plan": plan, "candidates": 2}, http.StatusCreated, "")
}

// I9: a comparison's candidate is delivered once it is selected; before
// its lanes settle, a delivery naming a candidate of any
// run of a comparison's chain, the launched one or a continuation, is
// refused 409 not_selected before anything is kept, though the candidate
// stands with its receipts; an ordinary run's candidate on the same
// ticket is delivered.
func TestACompareCandidateIsNotDeliveredYet(t *testing.T) {
	e := deliveryEnv(t)
	c := compareCrewIn(t, "/work/repo")
	c.ID = "cmp"
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	for _, r := range []struct{ id, from string }{{"cmp-0000abcd", ""}, {"cmp-0000abce", "cmp-0000abcd"}} {
		if _, err := e.srv.ledger.Transition(t.Context(), r.id, func(tx *ledger.Tx) error {
			if err := tx.CreateRun(ledger.RunRow{ID: r.id, CrewID: c.ID, Crew: string(raw), Base: deliveryCommit(0), State: ledger.RunFinished, ContinuedFrom: r.from, CreatedAt: tx.Now()}); err != nil {
				return err
			}
			if r.from != "" {
				if err := tx.Continued(r.from); err != nil {
					return err
				}
			}
			if err := tx.PutTask(ledger.TaskRow{ID: "approach-a", Member: "cand-a", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1, ResultCommit: deliveryCommit(1)}); err != nil {
				return err
			}
			res, _ := json.Marshal(crew.Result{Commit: deliveryCommit(1)})
			if err := tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "approach-a", N: 1, Member: "cand-a", StartCommit: deliveryCommit(0), ResultCommit: deliveryCommit(1), State: ledger.AttemptSucceeded, Result: string(res)}); err != nil {
				return err
			}
			return tx.PutCheck(ledger.CheckRow{TaskID: "approach-a", N: 1, ID: "ok", K: 1, Subject: deliveryCommit(1), State: ledger.CheckFinished, Exit: 0, Stable: true, Where: ledger.WhereNative, StartedAt: at, FinishedAt: at.Add(time.Second)})
		}); err != nil {
			t.Fatal(err)
		}
	}
	saveRecord(t, e.srv, crew.Run{ID: "cmp-0000abcd", CrewID: c.ID, Name: c.Name, StartedAt: at, State: crew.RunStopped})
	linkRun(t, e, "CND-1", "cmp-0000abcd", http.StatusCreated, "")
	links := func() int {
		return len(listOf(t, entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket"), "links"))
	}
	before := links()
	for _, run := range []string{"cmp-0000abcd", "cmp-0000abce"} {
		op := opID()
		out := e.deliver(t, "CND-1", op, "https://example.com/pull/"+run, run+"/approach-a/1@"+deliveryCommit(1), http.StatusConflict, "not_selected")
		if apiMessage(out) != "select a candidate first: the trial has not settled" || out["receipt"] != nil {
			t.Fatalf("%s: %v", run, out)
		}
		// Nothing kept: the op id is free for another link.
		if _, found, err := e.srv.dispatch.Receipt(t.Context(), adminActor, op); err != nil || found {
			t.Fatalf("%s: a receipt kept: %v %v", run, found, err)
		}
	}
	if n := links(); n != before {
		t.Fatalf("%d links, %d before", n, before)
	}
	e.deliver(t, "CND-1", opID(), "https://example.com/pull/ordinary", "synth-0000abcd/build/2@"+deliveryCommit(2), http.StatusCreated, "")
}

// A comparison's plan revision is proposed only with something to compare
// at its base: candidates with no check, and a file-form manifest whose
// steps at the base include no verify step, are refused 409 invalid in the
// launch's words, no revision kept, since every launch of it would be
// refused at that base; with a verify step there it is proposed.
func TestAComparisonsProposalNeedsSomethingToCompare(t *testing.T) {
	p := newCompareTicket(t)
	commitFile(t, p.repo, "conductor.manifest.json", `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`)
	c := entityOf(t, p.expect(t, "POST", "/api/crews/templates/compare", compareSpecBody("Nothing", p.repo, 2, false, map[string]any{"checks": nil, "manifest": map[string]any{"file": true}}), http.StatusCreated, ""), "crew")
	out := p.propose(t, "CND-1", map[string]any{"crew": c["id"]}, http.StatusConflict, dispatch.RefusalInvalid)
	if !strings.Contains(apiMessage(out), "nothing to compare: the candidates have no checks and conductor.manifest.json at ") || !strings.Contains(apiMessage(out), "has no verify step") {
		t.Fatalf("the proposal: %v", out)
	}
	if plans := p.expect(t, "GET", "/api/dispatch/tickets/CND-1/plans", nil, http.StatusOK, "")["plans"].([]any); len(plans) != 0 {
		t.Fatalf("%d revisions kept", len(plans))
	}
	commitFile(t, p.repo, "conductor.manifest.json", `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}]}`)
	plan := p.proposed(t, c["id"].(string), nil)
	if got := entityOf(t, p.expect(t, "GET", "/api/dispatch/plans/"+plan, nil, http.StatusOK, ""), "plan"); got["compare"].(map[string]any)["checksAtBase"] != 1.0 {
		t.Fatalf("the revision: %v", got["compare"])
	}
}

// A spec whose texts are each within their bounds in characters, written
// in a three-byte script and escaped to ASCII as many clients write JSON
// (\uXXXX for each character), is past 64 KiB of body and is taken.
func TestCompareTemplateTakesEscapedText(t *testing.T) {
	p := newCompareTicket(t)
	han := func(n int) string { return strings.Repeat(`\u6f22`, n) }
	var criteria []string
	for i := range 12 {
		criteria = append(criteria, fmt.Sprintf(`{"id":"c%d","text":"%s"}`, i, han(500)))
	}
	var cands []string
	for range 4 {
		cands = append(cands, fmt.Sprintf(`{"agentId":"ready-cat","approach":"%s"}`, han(2000)))
	}
	body := fmt.Sprintf(`{"name":"Escaped","cwd":%q,"goal":"%s","instructions":%q,"criteria":[%s],"checks":[{"id":"ok","argv":["true"]}],"candidates":[%s]}`,
		p.repo, han(2000), strings.Repeat("x", 8000), strings.Join(criteria, ","), strings.Join(cands, ","))
	if len(body) <= 64<<10 || len(body) > maxCompareSpecBody {
		t.Fatalf("the body is %d bytes", len(body))
	}
	status, raw, out := p.call(t, "POST", "/api/crews/templates/compare", []byte(body))
	if status != http.StatusCreated {
		t.Fatalf("the escaped spec: %d %.300s", status, raw)
	}
	if c := entityOf(t, out, "crew"); len(c["compare"].(map[string]any)["criteria"].([]any)) != 12 {
		t.Fatalf("the crew: %v", c["compare"])
	}
}
