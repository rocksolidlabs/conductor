package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// The batch fired after the retry holds the first turn's wake, whose
	// turn never started, and the one gathered since.
	q, err := b.desk.st.Get(wake)
	if err != nil {
		t.Fatal(err)
	}
	fired := b.roleRecord(t).Fired
	if fired == nil || fired.ID != q.Batch || len(fired.Wakes) != 2 || fired.Wakes[0].Subject != "task:run-1/build" || fired.Wakes[1].Subject != "task:run-1/lint" {
		t.Fatalf("the batch fired %+v for %s", fired, q.Batch)
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

// TestARoleWaitingForTheLockTakesUpTheLastOwnersRecord: a second server
// over the same data directory, waiting for the owner lock, answers the
// role as the owner has it at each read, never a copy of its first; once
// the lock is its own it takes up the record the first left (its batch,
// log and notes), and its next wake joins that batch under the id given,
// never one given before.
func TestARoleWaitingForTheLockTakesUpTheLastOwnersRecord(t *testing.T) {
	// Not parallel: the wait for the lock is the store package's, raised
	// here so a loaded machine's slow shutdown is not what the test sees.
	wait := brakeman.LockWait
	brakeman.LockWait = 2 * time.Minute
	t.Cleanup(func() { brakeman.LockWait = wait })
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) { cfg["wake"].(map[string]any)["windowSeconds"] = brakeman.MaxWindowSeconds })
	second := &bkEnv{testEnv: b.restart()}
	second.desk = second.srv.brakeman
	if st := second.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, ""); st["readOnly"] != true {
		t.Fatalf("the second server holds the records: %v", st)
	}
	if log, _ := second.expect(t, "GET", "/api/brakeman/role", nil, http.StatusOK, "")["log"].([]any); len(log) != 0 {
		t.Fatalf("%v", log)
	}
	// The owner changes the role while the second server waits.
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	if _, err := b.desk.role.KeepNote(crew.RoleNote{Text: "kept by the first server"}); err != nil {
		t.Fatal(err)
	}
	got := second.expect(t, "GET", "/api/brakeman/role", nil, http.StatusOK, "")
	if log, _ := got["log"].([]any); len(log) != 1 || entityOf(t, got, "role")["state"] != crew.RoleGathering {
		t.Fatalf("the waiting server answered the role it read first: %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	if !waitFor(time.Minute, second.desk.recovered.Load) {
		t.Fatal("the second server never took the records")
	}
	second.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1", At: time.Now()})
	r := second.roleRecord(t)
	if r.Last != 1 || r.Open == nil || r.Open.ID != "wb-1" || len(r.Open.Wakes) != 2 || len(r.Log) != 2 || len(r.Notes) != 1 {
		t.Fatalf("the record taken up: %+v", r)
	}
	doc, ok, err := second.desk.st.Role()
	if err != nil || !ok || !strings.Contains(string(doc), "kept by the first server") || !strings.Contains(string(doc), "task:run-1/build") {
		t.Fatalf("role.json: %v %v %s", ok, err, doc)
	}
}

// TestATicketMadeReadyWhileTheRoleIsOffWakesNothing: a ticket made ready
// while the role is off wakes nothing once it is turned on, as nothing
// else that fired meanwhile does; one made ready after wakes it.
func TestATicketMadeReadyWhileTheRoleIsOffWakesNothing(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "While off"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-2", "project": "CND", "title": "Once on"}, http.StatusCreated, "")
	mode := func(m string) { b.configure(t, func(c map[string]any) { c["wake"].(map[string]any)["mode"] = m }) }
	mode(brakeman.WakeInvoked)
	b.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "e-1", "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	b.roleSettle(t)
	mode(brakeman.WakeBackground)
	b.expect(t, "POST", "/api/dispatch/tickets/CND-2/edit", map[string]any{"opId": "e-2", "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		return len(b.roleRecord(t).Log) > 0
	}) {
		t.Fatal("the ticket made ready once on woke nothing")
	}
	b.roleSettle(t)
	if rows := b.roleRecord(t).Log; len(rows) != 1 || rows[0].Subject != "ticket:CND-2" {
		t.Fatalf("%+v", rows)
	}
}

// TestTheFirstCommitAfterAStartIsReadWhole: the first Dispatch commit
// after the server starts moves a ticket to ready and sets two fields
// after its status; its every row is read, and the ticket wakes the role.
func TestTheFirstCommitAfterAStartIsReadWhole(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b.srv.Shutdown(ctx)
	second := &bkEnv{testEnv: b.restart()}
	second.desk = second.srv.brakeman
	if !waitFor(30*time.Second, second.desk.recovered.Load) {
		t.Fatal("the second server never took the records")
	}
	second.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "e-1", "set": map[string]any{"status": "ready", "priority": 2, "owner": "Ada"}}, http.StatusOK, "")
	if !waitFor(30*time.Second, func() bool {
		second.desk.role.Poke()
		return len(second.roleRecord(t).Log) > 0
	}) {
		t.Fatal("the first commit's status row was never read")
	}
	if rows := second.roleRecord(t).Log; len(rows) != 1 || rows[0].Source != crew.WakeTicketReady || rows[0].Subject != "ticket:CND-1" {
		t.Fatalf("%+v", rows)
	}
}

// TestAWakeClosedAtItsDequeueIsFollowedUnpoked: a wake request closed
// over budget at its dequeue (no run made) is followed by the role from
// the request's own change: it is unavailable in the budget's words, its
// batch kept, with nothing of the test's poking the role once the request
// is queued.
func TestAWakeClosedAtItsDequeueIsFollowedUnpoked(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) { cfg["budget"].(map[string]any)["turnsPerDay"] = 2 })
	ask := b.briefing(t)
	sid, tok, _ := b.turnSession(t, ask)
	b.waitState(t, ask, brakeman.StateRunning)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	var wake string
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		if r := b.roleRecord(t); r.State == crew.RoleBriefing {
			wake = r.Request
		}
		return wake != ""
	}) {
		t.Fatalf("the batch never fired: %+v", b.roleRecord(t))
	}
	if r := b.waitState(t, wake, brakeman.StateQueued); r.Kind != brakeman.KindWake {
		t.Fatalf("%+v", r)
	}
	b.configure(t, func(cfg map[string]any) { cfg["budget"].(map[string]any)["turnsPerDay"] = 1 })
	// From here the test pokes the desk's loop, never the role's.
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.waitState(t, wake, brakeman.StateOverBudget)
	var r crew.RoleRecord
	if !waitFor(30*time.Second, func() bool {
		r = b.roleRecord(t)
		return r.State == crew.RoleUnavailable
	}) {
		t.Fatalf("the role never followed its request's close: %+v", r)
	}
	if !strings.Contains(r.Why, "the day's budget is spent") || r.Open == nil || len(r.Open.Wakes) != 1 || r.Open.Wakes[0].Subject != "task:run-1/build" {
		t.Fatalf("%+v", r)
	}
}

// TestALegacyRunsEndWakesTheRole: a person's legacy run, its one member
// ended, is finished, and its end (told to OnEnd alone, a session's
// change) wakes the role.
func TestALegacyRunsEndWakesTheRole(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
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
	b.roleSettle(t)
	if err := b.local(sid).Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		r, _ := b.srv.runs.Get(run.ID)
		return r.State == crew.RunFinished
	}) {
		t.Fatal("the run never finished")
	}
	var rows []crew.RoleLogRow
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		rows = b.roleRecord(t).Log
		return slices.ContainsFunc(rows, func(row crew.RoleLogRow) bool {
			return row.Source == crew.WakeRunEnded && row.Subject == "run:"+run.ID
		})
	}) {
		t.Fatalf("the run's end woke nothing: %+v", rows)
	}
}

// TestARunABrakemanActLaunchedWakesNothing: a run the desk's Host launched
// (a granted launch_approved_plan act's) is known as Brakeman's from
// Dispatch's launch, with no request naming it, and a question of its
// member's wakes nothing; a person's run's member's question open past
// the threshold wakes the role once, and one answered before it, nothing.
func TestARunABrakemanActLaunchedWakesNothing(t *testing.T) {
	l := newLaunchTest(t, nil, nil, bkAgents()...)
	b := bkEnvOf(t, l.testEnv)
	c := b.background(t, "bk", nil)
	person, err := testHost(l.srv, t.Context(), "cloud").LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, OpID: "p-1", Label: "a person's", By: ada})
	if err != nil {
		t.Fatal(err)
	}
	desk, err := l.srv.BrakemanHost().LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, OpID: "p-2", Label: "Brakeman's", By: ada})
	if err != nil {
		t.Fatal(err)
	}
	pr, ok1 := l.srv.runs.Get(person.ID)
	dr, ok2 := l.srv.runs.Get(desk.ID)
	if !ok1 || !ok2 || pr.Ticket == nil || dr.Ticket == nil {
		t.Fatalf("the runs: %v %v %+v %+v", ok1, ok2, pr.Ticket, dr.Ticket)
	}
	if page, _, _ := b.desk.st.List("", 10); len(page) != 0 {
		t.Fatalf("a request names the launch: %+v", page)
	}
	if b.desk.madeByTurn(pr) || !b.desk.madeByTurn(dr) {
		t.Fatalf("the person's run is Brakeman's %v; the desk's is %v", b.desk.madeByTurn(pr), b.desk.madeByTurn(dr))
	}
	// A question of a member of each run, and a third, answered before
	// its threshold.
	member := func(run string) string {
		var sid string
		if !waitFor(30*time.Second, func() bool {
			r, _ := l.srv.runs.Get(run)
			for _, m := range r.Members {
				if m.SessionID != "" {
					sid = m.SessionID
				}
			}
			return sid != ""
		}) {
			t.Fatalf("run %s's member never started", run)
		}
		return sid
	}
	ps, ds := member(person.ID), member(desk.ID)
	answered := b.createSession("cat")
	for _, sid := range []string{ps, ds, answered} {
		b.local(sid).SetAttentionFull(session.AttentionNeedsInput, "Allow Bash?", session.SourceAPI, session.KindPermission, []session.Option{{Label: "Yes", Input: "1"}})
	}
	b.roleSettle(t)
	b.local(answered).SetAttention(session.AttentionWorking, "", session.SourceAPI)
	c.add(time.Duration(brakeman.DefaultQuestionMinutes+1) * time.Minute)
	b.roleSettle(t)
	var questions []crew.RoleLogRow
	for _, row := range b.roleRecord(t).Log {
		if row.Source == crew.WakeQuestionOpen {
			questions = append(questions, row)
		}
	}
	if len(questions) != 1 || !strings.Contains(questions[0].Subject, ps) {
		t.Fatalf("the questions that woke the role: %+v (the person's member %s, Brakeman's %s, answered %s)", questions, ps, ds, answered)
	}
}

// refusingLedger is the engine's ledger refusing, while armed, every
// transaction of a run the engine shows paused as the transaction begins,
// nothing of it written: the first waits until the test lets it go.
type refusingLedger struct {
	crew.Ledger
	runs    *crew.Engine
	armed   atomic.Bool
	first   sync.Once
	held    chan struct{}
	release chan struct{}
}

func (g *refusingLedger) Transition(ctx context.Context, runID string, fn func(crew.LedgerTx) error) (int64, error) {
	if run, ok := g.runs.Get(runID); ok && run.Paused && g.armed.Load() {
		g.first.Do(func() {
			close(g.held)
			<-g.release
		})
		return 0, errors.New("the ledger refused the transaction")
	}
	return g.Ledger.Transition(ctx, runID, fn)
}

// hasWake reports whether the role's log holds a wake of the source for
// the subject.
func (b *bkEnv) hasWake(t *testing.T, source, subject string) bool {
	t.Helper()
	return slices.ContainsFunc(b.roleRecord(t).Log, func(row crew.RoleLogRow) bool { return row.Source == source && row.Subject == subject })
}

// TestAWakeIsReadOnlyFromACommittedRun: a person pauses a managed run
// and the ledger holds the pause's transaction, then refuses it and the
// engine's own pause after it; the engine shows the run paused meanwhile,
// and the role's passes read no pause from it; once a transaction of the
// run commits, its pause wakes the role.
func TestAWakeIsReadOnlyFromACommittedRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil, bkAgents()...)
	b := bkEnvOf(t, l.testEnv)
	g := &refusingLedger{Ledger: crew.SQLLedger(l.srv.ledger), runs: l.srv.runs, held: make(chan struct{}), release: make(chan struct{})}
	l.srv.runs.UseLedger(g)
	var once sync.Once
	let := func() { once.Do(func() { close(g.release) }) }
	t.Cleanup(let)
	b.background(t, "bk", nil)
	run, err := testHost(l.srv, t.Context(), "cloud").LaunchApproved(t.Context(), ext.ApprovedLaunch{Ticket: "CND-1", Plan: l.plan, OpID: "p-1", Label: "a person's", By: ada})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		_, settled, held := l.srv.runs.Settled(run.ID)
		return settled && held
	}) {
		t.Fatal("the run's launch never settled")
	}
	subject := "run:" + run.ID
	g.armed.Store(true)
	answered := make(chan int, 1)
	go func() {
		resp, _ := b.do("POST", "/api/runs/"+run.ID+"/pause", adminToken, map[string]any{"commandId": "c1"})
		answered <- resp.StatusCode
	}()
	select {
	case <-g.held:
	case <-t.Context().Done():
		t.Fatal("the pause's transaction never began")
	}
	if r, _ := l.srv.runs.Get(run.ID); !r.Paused {
		t.Fatal("the engine does not show the pause in its transaction")
	}
	b.desk.runChanged(run.ID)
	b.roleSettle(t)
	if b.hasWake(t, crew.WakeRunPaused, subject) {
		t.Fatal("a pause not committed woke the role")
	}
	let()
	if status := <-answered; status == http.StatusOK {
		t.Fatal("the refused pause was answered as done")
	}
	b.desk.runChanged(run.ID)
	b.roleSettle(t)
	if b.hasWake(t, crew.WakeRunPaused, subject) {
		t.Fatal("a refused pause woke the role")
	}
	g.armed.Store(false)
	if resp, out := b.do("POST", "/api/runs/"+run.ID+"/pause", adminToken, map[string]any{"commandId": "c2"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the second pause: %d %v", resp.StatusCode, out)
	}
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		return b.hasWake(t, crew.WakeRunPaused, subject)
	}) {
		t.Fatalf("the committed pause woke nothing: %+v", b.roleRecord(t).Log)
	}
}

// readyTicket makes ticket CND-n ready as a person does and waits until
// the role's source has heard of the commit and its loop has made a pass
// since: the feed was read.
func (b *bkEnv) readyTicket(t *testing.T, key, opID string) {
	t.Helper()
	b.desk.src.mu.Lock()
	top := b.desk.src.ticketTop
	b.desk.src.mu.Unlock()
	b.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID, "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	if !waitFor(30*time.Second, func() bool {
		b.desk.src.mu.Lock()
		defer b.desk.src.mu.Unlock()
		return b.desk.src.ticketTop > top
	}) {
		t.Fatal("the role never heard of the commit")
	}
	b.roleSettle(t)
}

// TestAWakeBatchIsReadUnderTheTurnsGrant: a ticket made ready while
// Dispatch is not visible wakes nothing; a turn's batch read once the
// visibility is narrowed leaves out the ticket's wake, and once the scope
// is narrowed to Brakeman's own crew, the run's too.
func TestAWakeBatchIsReadUnderTheTurnsGrant(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-2", "project": "CND", "title": "Seen"}, http.StatusCreated, "")
	dispatchSeen := func(on bool) {
		b.configure(t, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = on })
	}
	dispatchSeen(false)
	b.readyTicket(t, "CND-1", "e-1")
	if r := b.roleRecord(t); len(r.Log) != 0 {
		t.Fatalf("a ticket Brakeman may not see woke the role: %+v", r.Log)
	}
	dispatchSeen(true)
	b.readyTicket(t, "CND-2", "e-2")
	if !b.hasWake(t, crew.WakeTicketReady, "ticket:CND-2") {
		t.Fatalf("the ticket seen woke nothing: %+v", b.roleRecord(t).Log)
	}
	id := b.failTask(t, c, "build")
	sid, tok, _ := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	subjects := func() string {
		t.Helper()
		status, out := b.readRequest(t, sid, tok)
		batch, _ := entityOf(t, out, "request")["batch"].(map[string]any)
		if status != http.StatusOK || batch == nil {
			t.Fatalf("%d %v", status, out)
		}
		return fmt.Sprint(batch["wakes"])
	}
	if got := subjects(); !strings.Contains(got, "ticket:CND-2") || !strings.Contains(got, "task:run-1/build") {
		t.Fatalf("the batch: %s", got)
	}
	dispatchSeen(false)
	if got := subjects(); strings.Contains(got, "ticket:CND-2") || !strings.Contains(got, "task:run-1/build") {
		t.Fatalf("the batch with Dispatch not visible: %s", got)
	}
	b.configure(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []any{"brakeman"}} })
	if got := subjects(); got != "[]" {
		t.Fatalf("the batch within Brakeman's own crew: %s", got)
	}
}

// TestAPausedRoleStartsNoQueuedWake: a wake request queued behind a
// person's turn when the role is paused waits in the queue once the slot
// is free, and starts once the role is resumed.
func TestAPausedRoleStartsNoQueuedWake(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	ask := b.briefing(t)
	sid, tok, _ := b.turnSession(t, ask)
	b.waitState(t, ask, brakeman.StateRunning)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	var wake string
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		if r := b.roleRecord(t); r.State == crew.RoleBriefing {
			wake = r.Request
		}
		return wake != ""
	}) {
		t.Fatalf("the batch never fired: %+v", b.roleRecord(t))
	}
	b.waitState(t, wake, brakeman.StateQueued)
	b.expect(t, "POST", "/api/brakeman/role", map[string]any{"action": "pause"}, http.StatusOK, "")
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.waitState(t, ask, brakeman.StateBriefed)
	// The person's turn gives up the slot, or the wake request takes it.
	if !waitFor(30*time.Second, func() bool {
		r, err := b.desk.st.Get(wake)
		b.desk.mu.Lock()
		defer b.desk.mu.Unlock()
		return err == nil && (r.State != brakeman.StateQueued || b.desk.turn == nil)
	}) {
		t.Fatal("the person's turn never gave up the slot")
	}
	b.settle(t)
	if r := b.waitState(t, wake, brakeman.StateQueued, brakeman.StateStarting, brakeman.StateRunning); r.State != brakeman.StateQueued {
		t.Fatalf("the paused role's wake request started: %s", r.State)
	}
	b.expect(t, "POST", "/api/brakeman/role", map[string]any{"action": "resume"}, http.StatusOK, "")
	b.waitState(t, wake, brakeman.StateStarting, brakeman.StateRunning)
}

// breakRoleFile makes role.json a folder, so the role's every write
// fails, until the function it answers puts it back.
func (b *bkEnv) breakRoleFile(t *testing.T) func() {
	t.Helper()
	path := filepath.Join(b.srv.store.Dir(), brakeman.Dir, brakeman.RoleFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := false
	fix := func() {
		if !fixed {
			fixed = true
			if err := os.RemoveAll(path); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(fix)
	return fix
}

// TestASourcesWakeOutlivesAFailedWrite: a person's legacy run ends and a
// ticket is made ready while the role's record cannot be written; neither
// wake is lost with the write, and both are logged once it can be.
func TestASourcesWakeOutlivesAFailedWrite(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
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
	b.roleSettle(t)
	fix := b.breakRoleFile(t)
	if err := b.local(sid).Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		r, _ := b.srv.runs.Get(run.ID)
		return r.State == crew.RunFinished
	}) {
		t.Fatal("the run never finished")
	}
	b.roleSettle(t)
	b.readyTicket(t, "CND-1", "e-1")
	if r := b.roleRecord(t); len(r.Log) != 0 {
		t.Fatalf("a wake was taken with its write failing: %+v", r.Log)
	}
	fix()
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		return b.hasWake(t, crew.WakeRunEnded, "run:"+run.ID) && b.hasWake(t, crew.WakeTicketReady, "ticket:CND-1")
	}) {
		t.Fatalf("a wake was lost with the failed write: %+v", b.roleRecord(t).Log)
	}
}

// TestAnUnreadableConfigurationKeepsTheRolesWork: a batch is gathering
// and a ticket is made ready while the configuration cannot be read; past
// the window the batch is kept, unfired, and the ticket's wake waits; once
// the configuration reads again the batch fires (dropped, it never would)
// and the ticket's wake is taken.
func TestAnUnreadableConfigurationKeepsTheRolesWork(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) { cfg["wake"].(map[string]any)["windowSeconds"] = brakeman.MaxWindowSeconds })
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	path := filepath.Join(b.srv.store.Dir(), brakeman.Dir, brakeman.ConfigFile)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, good, 0o600) })
	b.readyTicket(t, "CND-1", "e-1")
	c.add(time.Duration(brakeman.MaxWindowSeconds+1) * time.Second)
	b.roleSettle(t)
	if r := b.roleRecord(t); r.State != crew.RoleGathering || r.Open == nil || r.Open.ID != "wb-1" || len(r.Log) != 1 {
		t.Fatalf("the unreadable configuration changed the role: %+v", r)
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	// The batch fires and the ticket's wake is taken, in either order:
	// the wake joins the batch, or the next once the batch has fired.
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		fired := slices.ContainsFunc(b.roleRecord(t).Log, func(row crew.RoleLogRow) bool {
			return row.Subject == "task:run-1/build" && row.Batch == "wb-1" && row.Request != ""
		})
		return fired && b.hasWake(t, crew.WakeTicketReady, "ticket:CND-1")
	}) {
		t.Fatalf("the batch was dropped or the ticket's wake lost: %+v", b.roleRecord(t))
	}
}

// TestATicketWakesByTheStatusItsChangeSet: with the role's pass held,
// one ticket is made ready and then moved on, another moved on and then
// made ready; each wakes the role once, for the change that made it
// ready.
func TestATicketWakesByTheStatusItsChangeSet(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	for i := 1; i <= 2; i++ {
		b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": fmt.Sprintf("t-%d", i), "project": "CND", "title": fmt.Sprintf("Ticket %d", i)}, http.StatusCreated, "")
	}
	// The pass is held: the loop takes no step while the desk does not
	// hold the records, and two of its passes make sure one in flight has
	// ended.
	b.desk.recovered.Store(false)
	b.roleSettle(t)
	set := func(key, op, status string) {
		b.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": op, "set": map[string]any{"status": status}}, http.StatusOK, "")
	}
	set("CND-1", "e-1", "ready")
	set("CND-1", "e-2", "in_progress")
	set("CND-2", "e-3", "in_progress")
	set("CND-2", "e-4", "ready")
	b.desk.recovered.Store(true)
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		return b.hasWake(t, crew.WakeTicketReady, "ticket:CND-1") && b.hasWake(t, crew.WakeTicketReady, "ticket:CND-2")
	}) {
		t.Fatalf("a ticket made ready woke nothing: %+v", b.roleRecord(t).Log)
	}
	b.roleSettle(t)
	if rows := b.roleRecord(t).Log; len(rows) != 2 {
		t.Fatalf("each ticket made ready once wakes the role once: %+v", rows)
	}
}
