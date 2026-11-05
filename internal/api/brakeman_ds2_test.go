package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// DS2's records as Brakeman's evidence and drafts (docs/plans/brakeman.md,
// PR-10): the refs plan:, launch: and candidate: resolved against
// Dispatch's plan revisions and launches and the ledger's candidates; the
// reads of a ticket's plan revisions and of a run's candidates through
// the principal brakeman; a draft_plan/1 made work through Dispatch's own
// propose route under the actor admin, with the person's name; and no way
// for Brakeman to approve. The fixture is obsFixture's server with a
// repository, a saved crew of it, CND-1's revision 1 approved, a launch of
// it kept pending in the store (nothing starts it), and a managed run of
// the ledger whose build submitted a commit, checked once against it.
// Each test runs git under a HOME of its own (planRepo), so none is
// parallel.

// ds2Fixture is the fixture.
type ds2Fixture struct {
	*obsFixture
	repo    string
	crew    crew.Crew
	plan    string // CND-1's revision 1, approved
	launch  string // a launch of it, pending
	candRun string // a managed run with a candidate
	commit  string // the commit its build's attempt 1 submitted
}

func newDS2Fixture(t *testing.T) *ds2Fixture {
	t.Helper()
	f := &ds2Fixture{obsFixture: newObsFixture(t), candRun: "team-0000cccc"}
	ctx := t.Context()
	f.repo = filepath.Join(f.root, "repo")
	base := planRepo(t, f.repo)
	c := teamCrew("Team", "cat")
	c.Cwd = f.repo
	saved, err := f.srv.crews.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	f.crew = saved
	out := f.expect(t, "POST", "/api/dispatch/tickets/"+f.ticket+"/plans", map[string]any{"opId": opID(), "crew": saved.ID}, http.StatusCreated, "")
	f.plan = entityOf(t, out, "plan")["id"].(string)
	f.expect(t, "POST", "/api/dispatch/plans/"+f.plan+"/approve", map[string]any{"opId": opID(), "byName": "Ann", "note": "Go."}, http.StatusOK, "")
	done := doneOf(t)
	f.launch = done(f.srv.dispatch.Launch(ctx, adminOp("Ann"), f.ticket, f.plan, "CND-1 Fix it")).ID

	f.commit = commitFile(t, f.repo, "fix.go", "package fix\n")
	result, _ := json.Marshal(crew.Result{Summary: "built", Commit: f.commit})
	crewJSON, _ := json.Marshal(crew.Crew{ID: "team", Name: "team", Cwd: f.root, Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}}})
	sum := sha256.Sum256([]byte("ok\n"))
	if _, err := f.srv.ledger.Transition(ctx, f.candRun, func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutRun(ledger.RunRow{ID: f.candRun, CrewID: "team", Crew: string(crewJSON), State: ledger.RunRunning, Base: base, CreatedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 2, ResultAttempt: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptSucceeded, StartCommit: base, ResultCommit: f.commit, Result: string(result), ReservedAt: now, ClosedAt: now}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 2, Member: "lead", State: ledger.AttemptFailed, StartCommit: base, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, Subject: f.commit, State: ledger.CheckFinished, Exit: 0, Log: []byte("ok\n"), LogSHA256: hex.EncodeToString(sum[:]), StartedAt: now, FinishedAt: now}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// resolve resolves a brief of items for a request begun under cfg, read
// under cfg, with the turn's reads.
func (f *ds2Fixture) resolve(t *testing.T, cfg brakeman.Config, items []brakeman.Item, reads ...brakeman.Read) ([]brakeman.Resolution, error) {
	t.Helper()
	req := brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: items}
	return f.srv.resolveBriefRefs(t.Context(), f.desk.st, req, b, brakeman.ReadLog{Reads: reads}, cfg)
}

// postAsTurn posts body to path with the turn's token, its session named
// in X-Conductor-Session when named is set, and answers the status.
func (f *ds2Fixture) postAsTurn(t *testing.T, path string, body any, named bool) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.http.URL+path, bytes.NewReader(encodeNoHTML(t, body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.tok)
	req.Header.Set("Content-Type", "application/json")
	if named {
		req.Header.Set(brakemanSessionHeader, f.sid)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// planRev is CND-1's revision n's rev and state, as Dispatch holds it.
func (f *ds2Fixture) planRev(t *testing.T, n int) (string, string) {
	t.Helper()
	plans, err := f.srv.dispatch.Plans(t.Context(), f.ticket)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.N == n {
			return strconv.FormatInt(p.Rev, 10), p.State
		}
	}
	t.Fatalf("no revision %d", n)
	return "", ""
}

// byRef is resolutions by their ref.
func byRef(res []brakeman.Resolution) map[string]brakeman.Resolution {
	out := map[string]brakeman.Resolution{}
	for _, r := range res {
		out[r.Ref.String()] = r
	}
	return out
}

// The refs of DS2's records resolve against their stores: a plan revision
// by its ticket and number to its state and rev, a launch to its state, a
// candidate to whether it stands and the commit it submitted; one that
// names nothing (no such revision, an attempt that submitted nothing, no
// such launch) is refused; one outside the scope or the visibility is
// refused; a decision card about a plan revision names the rev it read;
// and once the ticket is deleted, what the turn read of it resolves
// deleted beside what it read.
func TestPlanRefsResolve(t *testing.T) {
	f := newDS2Fixture(t)
	cfg := brakeman.DefaultConfig(f.root)
	plan, launch, cand := "plan:"+f.ticket+"/1", "launch:"+f.launch, "candidate:"+f.candRun+"/build/1"
	rev, state := f.planRev(t, 1)
	res, err := f.resolve(t, cfg, []brakeman.Item{fact("what was approved and launched", ref(t, plan), ref(t, launch), ref(t, cand))})
	if err != nil {
		t.Fatal(err)
	}
	got := byRef(res)
	l, err := f.srv.dispatch.LaunchByID(t.Context(), f.launch)
	if err != nil {
		t.Fatal(err)
	}
	for r, want := range map[string]brakeman.RefState{
		plan:   {URL: ticketURL(f.ticket), State: dispatch.PlanApproved, Rev: rev},
		launch: {URL: "/api/dispatch/launches/" + f.launch, State: dispatch.LaunchPending, Rev: strconv.FormatInt(l.Rev, 10)},
		cand:   {URL: "/runs/" + f.candRun + "?view=tasks&task=build", State: "standing", Rev: f.commit},
	} {
		now := got[r].Now
		if now.URL != want.URL || now.State != want.State || now.Rev != want.Rev || got[r].Seen.State != refUnobserved {
			t.Errorf("%s: now %+v seen %+v, want now %+v and unobserved", r, now, got[r].Seen, want)
		}
	}
	if state != dispatch.PlanApproved {
		t.Fatalf("the fixture's revision is %s", state)
	}

	// What names nothing.
	none := []string{"plan:" + f.ticket + "/2", "plan:CND-99/1", "launch:0123456789abcdef", "candidate:" + f.candRun + "/build/2", "candidate:" + f.candRun + "/deploy/1"}
	var refs []brakeman.Ref
	for _, s := range none {
		refs = append(refs, ref(t, s))
	}
	_, err = f.resolve(t, cfg, []brakeman.Item{fact("nothing", refs...)})
	bad := badOf(t, err)
	for _, s := range none {
		if bad[s] != "resolves to nothing" {
			t.Errorf("%s: %q", s, bad[s])
		}
	}

	// Outside the scope: a plan revision and a launch by their ticket's
	// project, a candidate by its run.
	other := cfg
	other.Scope = brakeman.Scope{Projects: []string{"OTH"}}
	_, err = f.resolve(t, other, []brakeman.Item{fact("elsewhere", ref(t, plan), ref(t, launch))})
	if bad := badOf(t, err); bad[plan] != "outside the request's scope" || bad[launch] != "outside the request's scope" {
		t.Errorf("a scope of OTH: %v", bad)
	}
	other.Scope = brakeman.Scope{Runs: []string{f.other}}
	_, err = f.resolve(t, other, []brakeman.Item{fact("elsewhere", ref(t, cand))})
	if bad := badOf(t, err); bad[cand] != "outside the request's scope" {
		t.Errorf("a scope of another run: %v", bad)
	}
	// Not visible: Dispatch off.
	blind := cfg
	blind.Visibility.Dispatch = false
	_, err = f.resolve(t, blind, []brakeman.Item{fact("unseen", ref(t, plan))})
	if bad := badOf(t, err); !strings.HasPrefix(bad[plan], "not visible") {
		t.Errorf("Dispatch not visible: %v", bad)
	}

	// A decision card about the revision names the rev it read.
	card := func(subjectRev string) brakeman.Item {
		rec := 0
		return brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: ref(t, plan), Happened: "Revision 1 was approved.", WhyHuman: "Only the operator approves.",
			Question: "Approve it again?", Alternatives: []brakeman.Alternative{{Label: "Approve", Tradeoffs: "a"}, {Label: "Revise", Tradeoffs: "b"}}, Recommendation: &rec,
			Authorizes: "A launch of revision 1.", Evidence: []brakeman.Ref{ref(t, plan)}, SubjectRevision: subjectRev}}
	}
	if _, err := f.resolve(t, cfg, []brakeman.Item{card(rev)}); err != nil {
		t.Fatalf("a decision at the revision's rev: %v", err)
	}
	_, err = f.resolve(t, cfg, []brakeman.Item{card(rev + "0")})
	if bad := badOf(t, err); !strings.Contains(bad[plan], "is at revision "+rev) {
		t.Errorf("a decision at another rev: %v", bad)
	}

	// The ticket deleted: what the turn read of it is deleted.
	at := f.desk.now()
	reads := []brakeman.Read{
		{Ref: ref(t, plan), Class: brakeman.ClassDispatch, State: dispatch.PlanApproved, Rev: rev, At: at},
		{Ref: ref(t, launch), Class: brakeman.ClassDispatch, State: dispatch.LaunchPending, Rev: strconv.FormatInt(l.Rev, 10), At: at},
	}
	doneOf(t)(f.srv.dispatch.DeleteTicket(t.Context(), adminOp("Ann"), f.ticket))
	res, err = f.resolve(t, cfg, []brakeman.Item{fact("gone", ref(t, plan), ref(t, launch))}, reads...)
	if err != nil {
		t.Fatal(err)
	}
	for r, res := range byRef(res) {
		if res.Now.State != refDeleted || res.Seen.State == refUnobserved || res.Seen.Rev == "" {
			t.Errorf("%s after the ticket's deletion: now %+v seen %+v", r, res.Now, res.Seen)
		}
	}
}

// A turn reads a ticket's plan revisions through the shared route, each
// with its ref, beside the approved one and whether its approval still
// holds, and the launches made from them; the ticket's intent edited
// since the approval, the approval no longer holds and its ref reads
// stale. The admin's answer is the route's own; a ticket outside the
// scope is refused; each read is counted.
func TestObservePlansSaysWhetherTheApprovalHolds(t *testing.T) {
	f := newDS2Fixture(t)
	f.turn(t, nil)
	path := "/api/dispatch/tickets/" + f.ticket + "/plans"
	plan := "plan:" + f.ticket + "/1"
	before, _ := f.desk.st.Get(f.req)
	out := f.expectRead(t, path, http.StatusOK, "")
	if got := refsOf(t, out["plans"]); len(got) != 1 || got[0] != plan || out["approved"] != plan || out["holds"] != true {
		t.Fatalf("the plans: %v approved %v holds %v", got, out["approved"], out["holds"])
	}
	launches, _ := out["launches"].([]any)
	if len(launches) != 1 {
		t.Fatalf("the launches: %v", out["launches"])
	}
	if l := launches[0].(map[string]any); l["ref"] != "launch:"+f.launch || l["plan"] != plan || l["state"] != dispatch.LaunchPending {
		t.Fatalf("the launch: %v", l)
	}
	rev, _ := f.planRev(t, 1)
	if rd, ok := f.readOf(t, plan); !ok || rd.State != dispatch.PlanApproved || rd.Rev != rev {
		t.Fatalf("the read of %s: %+v %v", plan, rd, ok)
	}
	if _, ok := f.readOf(t, "launch:"+f.launch); !ok {
		t.Fatal("the launch's read is not noted")
	}
	if after, _ := f.desk.st.Get(f.req); after.Reads != before.Reads+1 {
		t.Fatalf("reads %d then %d", before.Reads, after.Reads)
	}
	admin := f.expect(t, "GET", path, nil, http.StatusOK, "")
	if _, ok := admin["approved"]; ok || refsOf(t, admin["plans"])[0] != "" {
		t.Fatalf("the admin's answer is filtered: %v", admin)
	}

	// The ticket's title edited: its intent changed since the approval.
	title := "Fix the build on main"
	doneOf(t)(f.srv.dispatch.EditTicket(t.Context(), adminOp("Ann"), f.ticket, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &title}}))
	out = f.expectRead(t, path, http.StatusOK, "")
	if out["approved"] != plan || out["holds"] != false {
		t.Fatalf("after the edit: approved %v holds %v", out["approved"], out["holds"])
	}
	if rd, _ := f.readOf(t, plan); rd.State != "stale" {
		t.Fatalf("the read after the edit: %+v", rd)
	}
	// A brief citing it resolves it as read, stale.
	f.submitFact(t, plan)
	_, res, err := f.desk.st.Brief(f.req)
	if err != nil {
		t.Fatal(err)
	}
	if r := byRef(res)[plan]; r.Seen.State != "stale" || r.Now.State != "stale" {
		t.Fatalf("the brief's resolution: %+v", r)
	}

	// A ticket outside the scope.
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"projects": []string{"OTH"}} })
	f.expectRead(t, path, http.StatusForbidden, "outside_scope")
	f.expectRead(t, "/api/dispatch/tickets/"+f.otherTicket+"/plans", http.StatusOK, "")
}

// A turn reads a run's candidates through the shared route: each with its
// candidate: ref, noted standing at the commit it submitted, and each
// receipt with its check: ref, noted with its log's SHA-256 as the step
// route's read notes it; a brief citing both resolves them as read. A run
// outside the scope is refused.
func TestObserveCandidatesCarryTheirRefs(t *testing.T) {
	f := newDS2Fixture(t)
	f.turn(t, nil)
	path := "/api/runs/" + f.candRun + "/candidates"
	cand, check := "candidate:"+f.candRun+"/build/1", "check:"+f.candRun+"/build/1/test/1"
	out := f.expectRead(t, path, http.StatusOK, "")
	tasks, _ := out["candidates"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("the candidates: %v", out)
	}
	list := tasks[0].(map[string]any)["candidates"]
	if got := refsOf(t, list); len(got) != 1 || got[0] != cand {
		t.Fatalf("the candidates' refs: %v", got)
	}
	receipts := list.([]any)[0].(map[string]any)["receipts"]
	if got := refsOf(t, receipts); len(got) != 1 || got[0] != check {
		t.Fatalf("the receipts' refs: %v", got)
	}
	if rd, ok := f.readOf(t, cand); !ok || rd.State != "standing" || rd.Rev != f.commit {
		t.Fatalf("the candidate's read: %+v %v", rd, ok)
	}
	if rd, ok := f.readOf(t, check); !ok || rd.State != "exit:0" || rd.Rev != f.commit || rd.SHA256 == "" {
		t.Fatalf("the receipt's read: %+v %v", rd, ok)
	}
	if status, out := f.submit(t, f.sid, f.tok, fmt.Sprintf("b-%d", bkOps.Add(1)), factBrief(cand, check)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
	_, res, err := f.desk.st.Brief(f.req)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{cand, check} {
		got := byRef(res)[r]
		if got.Seen.State != got.Now.State || got.Seen.Rev != got.Now.Rev || got.Seen.SHA256 != got.Now.SHA256 {
			t.Errorf("%s changed between the read and the brief: seen %+v now %+v", r, got.Seen, got.Now)
		}
	}
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.other}} })
	f.expectRead(t, path, http.StatusForbidden, "outside_scope")
}

// draftPlan is a draft_plan/1 item of CND-1's for the fixture's crew,
// revising revision 1, handing over its comment, with one decision.
func (f *ds2Fixture) draftPlan(t *testing.T) brakeman.Item {
	t.Helper()
	based := ref(t, "plan:"+f.ticket+"/1")
	return brakeman.Item{Kind: brakeman.ItemDraftPlan, DraftPlan: &brakeman.DraftPlan{Ticket: ref(t, "ticket:"+f.ticket), Crew: f.crew.ID, BasedOn: &based,
		Comments: []brakeman.Ref{ref(t, "comment:"+f.ticket+"/"+f.comment)}, Constraints: "Keep the build green.",
		Decisions: []brakeman.DraftDecision{{ID: "design", Question: "Fix the test or the code?", Kind: "approval", Before: []string{"build"}}},
		Note:      "Revises 1 after the failures."}}
}

// A draft plan revision is made work by a person through Dispatch's own
// propose route under the actor admin, with the person's name: a
// revision of the ticket, proposed and waiting for the operator's
// approval, based on the one it revises and handing over the comment; the
// same opId again answers it, another is already made; Brakeman's token
// reaches no make. A comment of another ticket is refused before
// anything is proposed, and a crew that is not there settles the make
// refused.
func TestADraftPlanIsProposedByTheOperator(t *testing.T) {
	f := newDS2Fixture(t)
	req := f.drafts(t, f.draftPlan(t))
	status, out := f.make(t, req, 0, "plan-1", nil)
	if status != http.StatusCreated {
		t.Fatalf("the make: %d %v", status, out)
	}
	m := f.made(t, req, 0)
	if m.State != brakeman.MadeDone || m.Key != f.ticket || m.Plan == "" {
		t.Fatalf("made: %+v", m)
	}
	if p := entityOf(t, out, "plan"); p["id"] != m.Plan || p["state"] != dispatch.PlanProposed || p["n"] != 2.0 {
		t.Fatalf("the reply's revision: %v", p)
	}
	v, err := f.srv.dispatch.Plan(t.Context(), m.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if v.CreatedBy != "Ann" || v.BasedOn != f.plan || v.CrewID != f.crew.ID || v.State != dispatch.PlanProposed {
		t.Fatalf("the revision: %+v", v.PlanSummary)
	}
	var body dispatch.PlanBody
	if err := json.Unmarshal(v.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Comments) != 1 || body.Comments[0].ID != f.comment || len(body.Decisions) != 1 || body.Decisions[0].ID != "design" || body.Constraints != "Keep the build green." {
		t.Fatalf("the revision's body: %+v", body)
	}
	// The operation is the operator's: its receipt is admin's under the
	// make's derived op, and no actor brakeman holds one.
	if rc, ok, err := f.srv.dispatch.Receipt(t.Context(), dispatch.ActorAdmin, m.Op); err != nil || !ok || rc.Status != dispatch.StatusDone || rc.ID != m.Plan {
		t.Fatalf("admin's receipt: %+v %v %v", rc, ok, err)
	}
	if _, ok, _ := f.srv.dispatch.Receipt(t.Context(), "brakeman", m.Op); ok {
		t.Fatal("a receipt under the actor brakeman")
	}
	// The approved revision is still 1: proposing approves nothing.
	if _, state := f.planRev(t, 1); state != dispatch.PlanApproved {
		t.Fatalf("revision 1 is %s", state)
	}
	if again, out := f.make(t, req, 0, "plan-1", nil); again != http.StatusOK || entityOf(t, out, "made")["plan"] != m.Plan {
		t.Fatalf("the same opId again: %d %v", again, out)
	}
	if other, out := f.make(t, req, 0, "plan-2", nil); other != http.StatusConflict || errorCode(out) != "already_made" {
		t.Fatalf("another opId: %d %v", other, out)
	}
	if plans, _ := f.srv.dispatch.Plans(t.Context(), f.ticket); len(plans) != 2 {
		t.Fatalf("%d revisions", len(plans))
	}

	// Brakeman's own token makes nothing.
	f.turn(t, nil)
	if status := f.postAsTurn(t, fmt.Sprintf("/api/brakeman/requests/%s/items/0/make", req), map[string]any{"opId": "plan-3"}, true); status != http.StatusUnauthorized {
		t.Fatalf("Brakeman's token on make: %d", status)
	}

	// An edit that hands over another ticket's comment is refused, with
	// nothing proposed.
	req2 := f.drafts(t, f.draftPlan(t))
	edit := *f.draftPlan(t).DraftPlan
	edit.Comments = []brakeman.Ref{ref(t, "comment:"+f.otherTicket+"/"+f.comment)}
	if status, out := f.make(t, req2, 0, "plan-4", edit); status != http.StatusBadRequest || errorCode(out) != "invalid_draft" {
		t.Fatalf("another ticket's comment: %d %v", status, out)
	}
	// A crew that is not there: refused by the propose route, settled.
	edit = *f.draftPlan(t).DraftPlan
	edit.Crew = "no-such-crew"
	status, out = f.make(t, req2, 0, "plan-5", edit)
	if status != http.StatusConflict || f.made(t, req2, 0).State != brakeman.MadeRefused || !strings.Contains(f.made(t, req2, 0).Why, "no crew") {
		t.Fatalf("a crew that is not there: %d %v %+v", status, out, f.made(t, req2, 0))
	}
	if plans, _ := f.srv.dispatch.Plans(t.Context(), f.ticket); len(plans) != 2 {
		t.Fatalf("%d revisions after the refusals", len(plans))
	}
}

// A draft plan revision whose make the server left between the proposal
// and the outcome's write is completed by the recovery from Dispatch's
// receipt: one revision, made.
func TestAPendingDraftPlanIsProposedOnce(t *testing.T) {
	f := newDS2Fixture(t)
	req := f.drafts(t, f.draftPlan(t))
	crashAt(t, "committed")
	if status, out := f.make(t, req, 0, "plan-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the crash: %d %v", status, out)
	}
	if m := f.made(t, req, 0); m.State != brakeman.MadePending {
		t.Fatalf("the claim after the crash: %+v", m)
	}
	f.recoverNow(t)
	m := f.made(t, req, 0)
	if m.State != brakeman.MadeDone || m.Plan == "" {
		t.Fatalf("made after the recovery: %+v", m)
	}
	if plans, _ := f.srv.dispatch.Plans(t.Context(), f.ticket); len(plans) != 2 {
		t.Fatalf("%d revisions", len(plans))
	}
}

// A candidate's state is of one revision of its run, as the candidates'
// route reads it: a verification committed between its task's row and
// its attempt's (the attempt succeeded and made its task's result, both
// rows in one transition) is read again, so the candidate is standing,
// never the attempt's succeeded beside the task's earlier result.
func TestACandidateIsReadAtOneRevision(t *testing.T) {
	f := newDS2Fixture(t)
	cfg := brakeman.DefaultConfig(f.root)
	ctx := t.Context()
	result, _ := json.Marshal(crew.Result{Summary: "built again", Commit: f.commit})
	attempt := ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 3, Member: "lead", State: ledger.AttemptVerifying, StartCommit: f.commit, ResultCommit: f.commit, Result: string(result)}
	if _, err := f.srv.ledger.Transition(ctx, f.candRun, func(tx *ledger.Tx) error {
		attempt.ReservedAt = tx.Now()
		if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskRunning, Attempts: 3, ResultAttempt: 1}); err != nil {
			return err
		}
		return tx.PutAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	rr := f.srv.newRefResolver(f.desk.st, brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}, brakeman.ReadLog{}, cfg)
	reads := 0
	rr.taskRead = func() {
		reads++
		if reads > 1 {
			return
		}
		if _, err := f.srv.ledger.Transition(ctx, f.candRun, func(tx *ledger.Tx) error {
			attempt.State, attempt.ClosedAt = ledger.AttemptSucceeded, tx.Now()
			if err := tx.PutAttempt(attempt); err != nil {
				return err
			}
			return tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 3, ResultAttempt: 3})
		}); err != nil {
			t.Error(err)
		}
	}
	cand := ref(t, "candidate:"+f.candRun+"/build/3")
	got, err := rr.resolveAll(ctx, brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{fact("the candidate", cand)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := got[0].Now; n.State != "standing" || n.Rev != f.commit || reads != 2 {
		t.Fatalf("now %+v after %d reads", n, reads)
	}
}

// The same make asked again while it runs waits for it: a rejoin whose
// propose route would refuse what the first had already read (the crew
// deleted after the first read it) never settles the make refused while
// the first's proposal commits. The first is held inside the propose
// route, past its crew; the second is let go once it waits (or, without
// the wait, once it has answered).
func TestARejoinedPlanMakeWaitsForTheFirst(t *testing.T) {
	f := newDS2Fixture(t)
	req := f.drafts(t, f.draftPlan(t))
	var claimed sync.Once
	waiting := make(chan struct{}, 1)
	atSeam(t, func(stage, _ string, _ int) error {
		var err error
		switch stage {
		case "claimed":
			claimed.Do(func() { err = errors.New("the server stopped at claimed") })
		case "waiting":
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
		return err
	})
	if status, out := f.make(t, req, 0, "plan-1", nil); status != http.StatusInternalServerError {
		t.Fatalf("the claim: %d %v", status, out)
	}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hold := func(string) { once.Do(func() { close(held); <-release }) }
	proposeSeam.Store(&hold)
	t.Cleanup(func() { proposeSeam.Store(nil) })
	type answer struct {
		status int
		out    map[string]any
	}
	first, second := make(chan answer, 1), make(chan answer, 1)
	go func() {
		status, out := f.make(t, req, 0, "plan-1", nil)
		first <- answer{status, out}
	}()
	<-held
	if _, err := f.srv.crews.Delete(f.crew.ID); err != nil {
		t.Fatal(err)
	}
	go func() {
		status, out := f.make(t, req, 0, "plan-1", nil)
		second <- answer{status, out}
	}()
	var early *answer
	select {
	case <-waiting:
	case a := <-second:
		early = &a
	}
	close(release)
	a := <-first
	b := answer{}
	if early != nil {
		b = *early
	} else {
		b = <-second
	}
	if a.status != http.StatusOK || b.status != http.StatusOK {
		t.Errorf("the first rejoin %d %v, the second %d %v", a.status, a.out, b.status, b.out)
	}
	if m := f.made(t, req, 0); m.State != brakeman.MadeDone || m.Plan == "" {
		t.Fatalf("made: %+v", m)
	}
	if plans, _ := f.srv.dispatch.Plans(t.Context(), f.ticket); len(plans) != 2 {
		t.Fatalf("%d revisions", len(plans))
	}
}

// Brakeman never approves a plan revision: its token, granted every read,
// reaches no approval route (answered as no token is), and Dispatch
// refuses an approval by any actor but the operator.
func TestBrakemanCannotApprove(t *testing.T) {
	f := newDS2Fixture(t)
	req := f.drafts(t, f.draftPlan(t))
	if status, out := f.make(t, req, 0, "plan-1", nil); status != http.StatusCreated {
		t.Fatalf("the make: %d %v", status, out)
	}
	id := f.made(t, req, 0).Plan
	f.turn(t, nil)
	body := map[string]any{"opId": opID(), "byName": "Brakeman", "note": "Looks right."}
	if status := f.postAsTurn(t, "/api/dispatch/plans/"+id+"/approve", body, false); status != http.StatusUnauthorized {
		t.Fatalf("the turn's token without its session named: %d", status)
	}
	if status := f.postAsTurn(t, "/api/dispatch/plans/"+id+"/approve", body, true); status != http.StatusUnauthorized {
		t.Fatalf("the principal brakeman on approve: %d", status)
	}
	rc, err := f.srv.dispatch.ApprovePlan(context.Background(), dispatch.Op{Actor: "brakeman", ID: opID(), By: "Brakeman"}, id, 0, "")
	if err == nil && rc.Status == dispatch.StatusDone {
		t.Fatalf("the store approved for the actor brakeman: %+v", rc)
	}
	v, err := f.srv.dispatch.Plan(t.Context(), id)
	if err != nil || v.State != dispatch.PlanProposed || v.ApprovedBy != "" {
		t.Fatalf("the revision after: %+v %v", v.PlanSummary, err)
	}
}

// A draft plan revision's bounds are Dispatch's own for a proposal, and
// the propose route's base.
func TestTheDraftPlansBoundsAreDispatchs(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"base", brakeman.MaxDraftPlanBase, maxPlanBase},
		{"references", brakeman.MaxDraftPlanReferences, dispatch.MaxPlanReferences},
		{"reference path", brakeman.MaxDraftReferencePath, dispatch.MaxReferencePath},
		{"reference note", brakeman.MaxDraftReferenceNote, dispatch.MaxReferenceNote},
		{"attachments", brakeman.MaxDraftPlanAttachments, dispatch.MaxPlanAttachments},
		{"comments", brakeman.MaxDraftPlanComments, dispatch.MaxPlanComments},
		{"constraints", brakeman.MaxDraftPlanConstraints, dispatch.MaxPlanConstraints},
		{"decisions", brakeman.MaxDraftPlanDecisions, dispatch.MaxPlanDecisions},
		{"question", brakeman.MaxDraftDecisionAsk, dispatch.MaxDecisionQuestion},
		{"before", brakeman.MaxDraftDecisionBefore, dispatch.MaxDecisionBefore},
		{"note", brakeman.MaxDraftPlanNote, dispatch.MaxPlanNote},
	} {
		if c.got != c.want {
			t.Errorf("%s: a draft keeps %d, Dispatch %d", c.name, c.got, c.want)
		}
	}
	// A base the draft takes is one the propose route takes, and the
	// other way round: a multi-byte base at the bound and past it, and
	// the spaces and controls beyond ASCII's.
	for _, base := range []string{"main", "HEAD~2", "-main", "main x", "main\tx", "main\u0085x", "main\u00a0x", "main\u2028x", "main\u3000x", "main\u007fx",
		strings.Repeat("a", maxPlanBase), strings.Repeat("a", maxPlanBase+1), strings.Repeat("é", maxPlanBase/2), strings.Repeat("é", maxPlanBase/2+1), "réf/é"} {
		if draft, route := brakeman.DraftPlanBase(base), planBaseShape(base) == ""; draft != route {
			t.Errorf("the base %q: the draft takes it %v, the propose route %v", base, draft, route)
		}
	}
	kinds := []string{dispatch.DecisionApproval, dispatch.DecisionClarification}
	for _, f := range brakeman.ItemKinds() {
		if f.Kind != brakeman.ItemDraftPlan {
			continue
		}
		for _, field := range f.Fields {
			if field.Name != "decisions" {
				continue
			}
			for _, sub := range field.Of.Fields {
				if sub.Name == "kind" && strings.Join(sub.Enum, ",") != strings.Join(kinds, ",") {
					t.Errorf("a decision's kinds: %v, Dispatch's %v", sub.Enum, kinds)
				}
			}
		}
	}
}

// A receipt a turn reads on an attempt's steps is noted at the state its
// check: ref resolves to, read from the ledger's row: a check that was
// killed is killed, not the exit its step's answer gives, so a brief
// citing it shows nothing changed since the read.
func TestAKilledCheckIsReadAsItResolves(t *testing.T) {
	f := newObsFixture(t)
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		now := tx.Now()
		return tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 2, Argv: []string{"go", "test"}, State: ledger.CheckFinished,
			Exit: -1, Killed: true, StartedAt: now, FinishedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	f.turn(t, nil)
	check := "check:" + f.run + "/build/1/test/2"
	f.expectRead(t, "/api/runs/"+f.run+"/tasks/build/attempts/1/steps", http.StatusOK, "")
	if rd, ok := f.readOf(t, check); !ok || rd.State != "killed" {
		t.Fatalf("the steps' read of %s: %+v %v", check, rd, ok)
	}
	f.submitFact(t, check)
	_, res, err := f.desk.st.Brief(f.req)
	if err != nil {
		t.Fatal(err)
	}
	if r := byRef(res)[check]; r.Seen.State != r.Now.State || r.Now.State != "killed" {
		t.Fatalf("the brief's resolution: %+v", r)
	}
}
