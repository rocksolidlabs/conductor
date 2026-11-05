package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// BK3's background role on the desk (docs/plans/brakeman.md, PR-14): the
// role's records over Brakeman's store, its sources, a wake turn as BK1's
// turn of kind wake, and its controls' routes. The role's clock is the
// desk's, which the tests move; a pass of its loop is waited on through
// its step count, never a sleep.

// background turns Brakeman on in the background with the agent given and
// the window at its least, on a clock of the test's.
func (b *bkEnv) background(t *testing.T, agent string, mod func(c map[string]any)) *clock {
	t.Helper()
	c := b.fakeClock()
	b.enable(t, agent, func(cfg map[string]any) {
		w := cfg["wake"].(map[string]any)
		w["mode"], w["windowSeconds"] = brakeman.WakeBackground, brakeman.MinWindowSeconds
		if mod != nil {
			mod(cfg)
		}
	})
	b.roleSettle(t)
	return c
}

// roleSettle waits until the role's loop has made a full pass after a
// poke: what is due at the desk's clock now has been done.
func (b *bkEnv) roleSettle(t *testing.T) {
	t.Helper()
	n := b.desk.role.Steps()
	b.desk.role.Poke()
	if !waitFor(10*time.Second, func() bool {
		b.desk.role.Poke()
		return b.desk.role.Steps() >= n+2
	}) {
		t.Fatal("the role's loop made no pass")
	}
}

// roleRecord is the role as the desk holds it.
func (b *bkEnv) roleRecord(t *testing.T) crew.RoleRecord {
	t.Helper()
	r, err := b.desk.role.Record()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// waitRole waits for the role to be in state, which holds once reached.
func (b *bkEnv) waitRole(t *testing.T, state string) crew.RoleRecord {
	t.Helper()
	var r crew.RoleRecord
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		r = b.roleRecord(t)
		return r.State == state
	}) {
		t.Fatalf("the role is %s (%s), not %s", r.State, r.Why, state)
	}
	return r
}

// failTask wakes the role as a task failing in a person's run does, and
// moves the clock past the window: the batch fires, and its request is
// answered.
func (b *bkEnv) failTask(t *testing.T, c *clock, task string) string {
	t.Helper()
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/" + task, Key: "attempts 1", At: c.now()})
	if r := b.roleRecord(t); r.Open == nil {
		t.Fatalf("no batch opened: %+v", r)
	}
	batch := b.roleRecord(t).Open.ID
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	// The request the batch fired, from the log, which keeps it: the role
	// may be briefing, or past it already for a turn that fails at once.
	var id string
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		for _, row := range b.roleRecord(t).Log {
			if row.Batch == batch && row.Request != "" {
				id = row.Request
			}
		}
		return id != ""
	}) {
		t.Fatalf("the batch %s never fired: %+v", batch, b.roleRecord(t))
	}
	return id
}

// TestAWakeTurnIsBK1sTurnOfKindWake: a task failing wakes the role; past
// the window one request of kind wake is kept under the batch's op id and
// launched as BK1's turn, its session reads the batch and the role's notes
// through its request, its brief is kept, and the role cools, then is
// idle; the request's run ending wakes nothing (TestABriefingWakesNothing,
// below).
func TestAWakeTurnIsBK1sTurnOfKindWake(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	id := b.failTask(t, c, "build")
	req := b.waitState(t, id, brakeman.StateStarting, brakeman.StateRunning)
	if req.Kind != brakeman.KindWake || req.Batch != "wb-1" || req.OpID != "wake-wb-1" {
		t.Fatalf("%+v", req)
	}
	sid, tok, _ := b.turnSession(t, id)
	status, out := b.readRequest(t, sid, tok)
	batch, _ := entityOf(t, out, "request")["batch"].(map[string]any)
	if status != http.StatusOK || batch["id"] != "wb-1" || !strings.Contains(fmt.Sprint(batch["wakes"]), "task:run-1/build") {
		t.Fatalf("%d %v", status, out)
	}
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	if r := b.waitRole(t, crew.RoleCooling); !r.CoolUntil.After(c.now()) {
		t.Fatalf("cools until %s", r.CoolUntil)
	}
	c.add(31 * time.Minute)
	b.waitRole(t, crew.RoleIdle)
	// The role's records read back as the routes answer them.
	got := b.expect(t, "GET", "/api/brakeman/role", nil, http.StatusOK, "")
	log, _ := got["log"].([]any)
	if len(log) != 1 || !strings.Contains(fmt.Sprint(log[0]), "request:"+id) || entityOf(t, got, "role")["state"] != crew.RoleIdle {
		t.Fatalf("%v", got)
	}
	state := b.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if entityOf(t, state, "role")["state"] != crew.RoleIdle {
		t.Fatalf("%v", state)
	}
}

// TestABriefingWakesNothing: one external failure is one turn, and the
// turn's own run, which ends once its brief is kept, wakes nothing: no
// source fires for a Brakeman turn's run or session.
func TestABriefingWakesNothing(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	id := b.failTask(t, c, "build")
	sid, tok, _ := b.turnSession(t, id)
	run := b.runOf(t, sid)
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid, "run:"+run)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.runStopped(t, run)
	b.slotFree(t)
	// Its run's changes and its session's question reach the sources.
	b.desk.runChanged(run)
	b.desk.activity(sid, session.ActivityEntry{Type: session.ActivityAttention}, session.AttentionNeedsInput)
	if r, ok := b.srv.runs.Get(run); !ok || len(b.desk.runWakes(r)) != 0 {
		t.Fatalf("a Brakeman turn's run gives wakes: %v", ok)
	}
	b.waitRole(t, crew.RoleCooling)
	c.add(31 * time.Minute)
	b.roleSettle(t)
	r := b.roleRecord(t)
	if len(r.Log) != 1 || r.Open != nil || r.State != crew.RoleIdle {
		t.Fatalf("the turn woke the role: %+v", r)
	}
	page, _, _ := b.desk.st.List("", 10)
	if len(page) != 1 {
		t.Fatalf("%d requests", len(page))
	}
}

// TestRunWakesNameEachSource: a person's run gives a wake for each task
// failed with proof (its attempts the key), one as it becomes paused, and
// one at its end; a Brakeman turn's run gives none.
func TestRunWakesNameEachSource(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	run := crew.Run{ID: "team-1", State: crew.RunRunning, Paused: true, Managed: &crew.Managed{Revision: 7},
		Tasks: []crew.TaskState{{ID: "build", State: ledger.TaskFailed, Attempts: 3}, {ID: "lint", State: ledger.TaskSucceeded}}}
	got := b.desk.runWakes(run)
	if len(got) != 2 || got[0].Source != crew.WakeTaskFailed || got[0].Subject != "task:team-1/build" || got[0].Key != "attempts 3" ||
		got[1].Source != crew.WakeRunPaused || got[1].Key != "paused at revision 7" {
		t.Fatalf("%+v", got)
	}
	// Still paused: no second pause wake.
	if got := b.desk.runWakes(run); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	run.Paused, run.Outcome = false, "failed"
	if got := b.desk.runWakes(run); len(got) != 2 || got[1].Source != crew.WakeRunEnded || got[1].Key != "outcome failed" {
		t.Fatalf("%+v", got)
	}
	run.Class, run.Request = crew.ClassBrakeman, "br-1"
	if got := b.desk.runWakes(run); len(got) != 0 {
		t.Fatalf("a turn's run: %+v", got)
	}
}

// TestAWakeTurnsSessionIsMarkedQuiet: a wake turn's session carries the
// quiet mark from its creation: the first snapshot the registry holds of
// it, and the sessions list; a person's request's does not.
func TestAWakeTurnsSessionIsMarkedQuiet(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	id := b.failTask(t, c, "build")
	var first *session.BrakemanMark
	if !waitFor(30*time.Second, func() bool {
		b.srv.registry.Each(func(d session.Driver) {
			if ref := d.Info().Crew; first == nil && ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id {
				m := *ref.Brakeman
				first = &m
			}
		})
		return first != nil
	}) {
		t.Fatal("no session")
	}
	if !first.Quiet {
		t.Fatalf("the first snapshot %+v", first)
	}
	list := b.expect(t, "GET", "/api/sessions", nil, http.StatusOK, "")
	if !strings.Contains(fmt.Sprint(list), "brakeman:map[quiet:true request:"+id+"]") {
		t.Fatalf("%v", list)
	}
}

// TestAWakeTurnsQuestionNotifiesNobody: a question a wake turn's session
// asks reaches no webhook; a person's session's question after it does,
// first.
func TestAWakeTurnsQuestionNotifiesNobody(t *testing.T) {
	t.Parallel()
	rcv := newReceiver(t, nil)
	b := brakemanEnv(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: []string{"needs_input"}, AllowPrivate: true}}
	})
	c := b.background(t, "bk", nil)
	id := b.failTask(t, c, "build")
	sid, tok, _ := b.turnSession(t, id)
	if status, data, _ := b.callAs(t, tok, "POST", "/api/sessions/"+sid+"/attention", map[string]any{"state": "needs_input", "message": "may I read the logs?"}); status >= 300 {
		t.Fatalf("ask: %d %s", status, data)
	}
	other := b.createSession("cat")
	b.callAs(t, b.agentToken(other), "POST", "/api/sessions/"+other+"/attention", map[string]any{"state": "needs_input", "message": "loud"})
	if d := rcv.next(t); d.message(t) != "loud" {
		t.Fatalf("a wake turn's question paged: %s", d.body)
	}
	// Needs you still lists it.
	if a := b.srv.registryInfo(t, sid).Attention; a.State != session.AttentionNeedsInput {
		t.Fatalf("%+v", a)
	}
}

// registryInfo is a session's info as the registry holds it.
func (s *Server) registryInfo(t *testing.T, id string) session.Info {
	t.Helper()
	d, ok := s.registry.Get(id)
	if !ok {
		t.Fatalf("no session %s", id)
	}
	return d.Info()
}

// TestALoggedOutAgentMakesTheRoleUnavailable: an agent that exits before
// it reads its request (a logged-out CLI) fails the wake turn, and the
// role is unavailable with the words; it starts nothing by itself after
// (a second failure is gathered, not fired) until a person's own request
// tries again.
func TestALoggedOutAgentMakesTheRoleUnavailable(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bkexit", nil)
	id := b.failTask(t, c, "build")
	b.waitState(t, id, brakeman.StateFailed)
	r := b.waitRole(t, crew.RoleUnavailable)
	if !strings.Contains(r.Why, "is it logged in?") || !strings.Contains(r.Why, "Please run /login") {
		t.Fatalf("%+v", r)
	}
	b.slotFree(t)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1", At: c.now()})
	c.add(time.Hour)
	b.roleSettle(t)
	if r := b.roleRecord(t); r.State != crew.RoleUnavailable || r.Open == nil {
		t.Fatalf("%+v", r)
	}
	if page, _, _ := b.desk.st.List("", 10); len(page) != 1 {
		t.Fatalf("an unavailable role started %d turns", len(page))
	}
	// A person's own request tries again: the batch kept fires.
	ask := b.briefing(t)
	b.waitState(t, ask, brakeman.StateFailed)
	b.slotFree(t)
	var wake string
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		page, _, _ := b.desk.st.List("", 10)
		for _, q := range page {
			if q.Kind == brakeman.KindWake && q.ID != id {
				wake = q.ID
			}
		}
		return wake != ""
	}) {
		t.Fatal("the batch kept never fired after the person's request")
	}
	if got := b.roleRecord(t).Log; len(got) != 3 || got[2].Source != crew.WakeRequest || got[2].Subject != ask {
		t.Fatalf("%+v", got)
	}
}

// TestAnUnavailableRoleHoldsNothingElse: while the role is unavailable a
// person's crew runs and a question its member asks is answered as ever:
// nothing waits on Brakeman.
func TestAnUnavailableRoleHoldsNothingElse(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bkexit", nil)
	id := b.failTask(t, c, "build")
	b.waitState(t, id, brakeman.StateFailed)
	b.waitRole(t, crew.RoleUnavailable)
	run, release, err := b.srv.runs.LaunchNamed(t.Context(), crew.Crew{ID: "team", Name: "Team", Cwd: b.root, Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Members: []crew.Member{{Name: "dev", AgentID: "bkfixed", Start: crew.Start{When: crew.StartImmediately}}}}, "a person's run")
	release()
	if err != nil {
		t.Fatal(err)
	}
	var sid string
	if !waitFor(30*time.Second, func() bool {
		r, _ := b.srv.runs.Get(run.ID)
		if len(r.Members) > 0 {
			sid = r.Members[0].SessionID
		}
		return sid != ""
	}) {
		t.Fatal("the person's crew never ran")
	}
	l := b.local(sid)
	l.SetAttentionFull(session.AttentionNeedsInput, "Allow Bash?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}})
	q := l.Info().Attention.ID
	if res, err := testHost(b.srv, t.Context(), "person").Submit(t.Context(), ext.SubmitRequest{Session: sid, Question: q, Input: "1", By: ada}); err != nil || res.Status != "entered" {
		t.Fatalf("the question's answer: %+v %v", res, err)
	}
	if r := b.roleRecord(t); r.State != crew.RoleUnavailable {
		t.Fatalf("%+v", r)
	}
}

// TestThePauseAndTheNotesAreThePersons: paused through its route, a wake
// joins no batch and nothing fires; resumed, the role takes wakes again;
// a brief's fact is kept as a note, given to the next turn, and cleared.
func TestThePauseAndTheNotesAreThePersons(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	if got := entityOf(t, b.expect(t, "POST", "/api/brakeman/role", map[string]any{"action": "pause"}, http.StatusOK, ""), "role"); got["state"] != crew.RolePaused {
		t.Fatalf("%v", got)
	}
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	c.add(time.Hour)
	b.roleSettle(t)
	if r := b.roleRecord(t); r.State != crew.RolePaused || r.Open != nil || r.Log[0].Held != "paused" {
		t.Fatalf("%+v", r)
	}
	b.expect(t, "POST", "/api/brakeman/role", map[string]any{"action": "resume"}, http.StatusOK, "")
	b.expect(t, "POST", "/api/brakeman/role", map[string]any{"action": "sleep"}, http.StatusBadRequest, "invalid_request")
	id := b.failTask(t, c, "lint")
	sid, tok, _ := b.turnSession(t, id)
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	b.waitState(t, id, brakeman.StateBriefed)
	b.expect(t, "POST", "/api/brakeman/role/notes", map[string]any{"request": id, "item": 3}, http.StatusNotFound, "not_found")
	role := entityOf(t, b.expect(t, "POST", "/api/brakeman/role/notes", map[string]any{"request": id, "item": 0}, http.StatusOK, ""), "role")
	if !strings.Contains(fmt.Sprint(role["notes"]), "the turn's session ran") || !strings.Contains(fmt.Sprint(role["notes"]), "brief:"+id+"/0") {
		t.Fatalf("%v", role)
	}
	role = entityOf(t, b.expect(t, "DELETE", "/api/brakeman/role/notes", nil, http.StatusOK, ""), "role")
	if role["notes"] != nil {
		t.Fatalf("%v", role)
	}
	// A wake request is the role's own: a person cannot ask one.
	_, _, data := b.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "w", "kind": brakeman.KindWake})
	if errorCode(data) != "invalid_request" {
		t.Fatalf("%v", data)
	}
}

// TestTheRoleSpendsNoMoreThanTheDaysTurns: a batch the day's turns cannot
// take leaves the role unavailable with the budget's words, until the
// next day.
func TestTheRoleSpendsNoMoreThanTheDaysTurns(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) { cfg["budget"].(map[string]any)["turnsPerDay"] = 1 })
	id := b.briefing(t)
	b.waitState(t, id, brakeman.StateStarting, brakeman.StateRunning)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	r := b.waitRole(t, crew.RoleUnavailable)
	if !strings.Contains(r.Why, "the day's budget is spent: 1 of 1 turns") || !r.RetryAt.After(c.now()) || r.Open == nil {
		t.Fatalf("%+v", r)
	}
}

// TestATicketMadeReadyWakesTheRole: a ticket whose status a person moves
// to ready wakes the role, once, from Dispatch's change feed; a move under
// an op id a request derived (a make, an act) wakes nothing.
func TestATicketMadeReadyWakesTheRole(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-2", "project": "CND", "title": "Made by a make"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets/CND-2/edit", map[string]any{"opId": deriveOp("make", "br-1", "0", "o"), "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	b.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "e-1", "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	var rows []crew.RoleLogRow
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		rows = b.roleRecord(t).Log
		return len(rows) > 0
	}) {
		t.Fatal("the ticket made ready woke nothing")
	}
	b.roleSettle(t)
	rows = b.roleRecord(t).Log
	if len(rows) != 1 || rows[0].Source != crew.WakeTicketReady || rows[0].Subject != "ticket:CND-1" {
		t.Fatalf("%+v", rows)
	}
}
