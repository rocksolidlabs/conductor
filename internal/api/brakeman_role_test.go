package api

import (
	"context"
	"encoding/json"
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
// role is unavailable with the words and a retry of its own; it starts
// nothing by itself before then (a second failure is gathered, not
// fired), and a person's own request, which fails the same way, fires
// nothing into the same state either. Its retry fires the batch kept,
// whose turn fails again: the next retry waits twice as long.
func TestALoggedOutAgentMakesTheRoleUnavailable(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bkexit", nil)
	id := b.failTask(t, c, "build")
	b.waitState(t, id, brakeman.StateFailed)
	r := b.waitRole(t, crew.RoleUnavailable)
	if !strings.Contains(r.Why, "is it logged in?") || !strings.Contains(r.Why, "Please run /login") || r.Reason != crew.ReasonStart {
		t.Fatalf("%+v", r)
	}
	retry := r.RetryAt
	b.slotFree(t)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1", At: c.now()})
	c.add(retry.Sub(c.now()) - time.Minute)
	b.roleSettle(t)
	if r := b.ledgerRole(t); r.State != crew.RoleUnavailable || r.Open == nil {
		t.Fatalf("%+v", r)
	}
	if page, _, _ := b.desk.st.List("", 10); len(page) != 1 {
		t.Fatalf("an unavailable role started %d turns", len(page))
	}
	// A person's own request fails the same way, and fires nothing more.
	ask := b.briefing(t)
	b.waitState(t, ask, brakeman.StateFailed)
	b.slotFree(t)
	b.roleSettle(t)
	if page, _, _ := b.desk.st.List("", 10); len(page) != 2 {
		t.Fatalf("the person's request fired the batch into the same state: %d requests", len(page))
	}
	if got := b.ledgerRole(t).Log; len(got) != 3 || got[2].Source != crew.WakeRequest || got[2].Subject != ask {
		t.Fatalf("%+v", got)
	}
	// The role's retry fires the batch kept: the first turn's wake, whose
	// turn never started, and the one gathered since.
	c.add(time.Minute)
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
		t.Fatal("the batch kept never fired at the role's retry")
	}
	q, err := b.desk.st.Get(wake)
	if err != nil {
		t.Fatal(err)
	}
	fired := b.roleRecord(t).Fired
	if fired == nil || fired.ID != q.Batch || len(fired.Wakes) != 2 || fired.Wakes[0].Subject != "task:run-1/build" || fired.Wakes[1].Subject != "task:run-1/lint" {
		t.Fatalf("the batch fired %+v for %s", fired, q.Batch)
	}
	b.waitState(t, wake, brakeman.StateFailed)
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		r := b.ledgerRole(t)
		return r.State == crew.RoleUnavailable && r.Tries == 2
	}) {
		t.Fatalf("the second turn that did not start: %+v", b.ledgerRole(t))
	}
	if r := b.ledgerRole(t); !r.RetryAt.Equal(c.now().Add(2 * crew.RoleStartRetry)) {
		t.Fatalf("the next retry: %+v", r)
	}
}

// typedInto is every byte that reached the terminal of the request id's
// scripted agent (bkRecord: bkSignInScript, bkUpdateScript,
// bkUnknownScript), read raw: "" when nothing did. The record is made
// before the agent draws its screen, so one never made is a stub that did
// not run, never "nothing typed".
func (b *bkEnv) typedInto(t *testing.T, id string) string {
	t.Helper()
	var data []byte
	if !waitFor(30*time.Second, func() bool {
		var err error
		data, err = os.ReadFile(filepath.Join(b.root, "typed-"+id+".txt"))
		return err == nil
	}) {
		t.Fatalf("%s's agent kept no record of its terminal", id)
	}
	return string(data)
}

// TestASignedOutAgentMakesTheRoleUnavailable: an agent that does not exit
// signed out but waits at its sign-in screen (Claude Code's, found by the
// built-in startup question that has no answers) ends its wake turn at
// once, on the desk's clock that never reaches the turn's deadline, with
// words that say what it shows; the turn is charged only the time it ran,
// no key reaches the agent, and the role is unavailable with those words
// and fires nothing more by itself (a later wake is gathered, not fired).
// A person's own request against it says the same. The stub that exits
// at once (TestALoggedOutAgentMakesTheRoleUnavailable) never met one
// that waits.
func TestASignedOutAgentMakesTheRoleUnavailable(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bksignin", nil)
	id := b.failTask(t, c, "build")
	q := b.waitState(t, id, brakeman.StateFailed)
	if !strings.Contains(q.Why, "its login refused") || !strings.Contains(q.Why, "Browser didn't open? Use the url below to sign in") || strings.Contains(q.Why, "example.invalid") || q.Reads != 0 {
		t.Fatalf("the turn: %+v", q)
	}
	b.waitRole(t, crew.RoleUnavailable)
	r := b.ledgerRole(t)
	if r.State != crew.RoleUnavailable || !strings.Contains(r.Why, "its login refused") || r.Reason != crew.ReasonStart || r.Tries != 1 || !r.RetryAt.Equal(c.now().Add(crew.RoleStartRetry)) || r.Open == nil {
		t.Fatalf("the role: %+v", r)
	}
	b.slotFree(t)
	if m, err := b.desk.st.MinutesToday(c.now()); err != nil || m != 0 {
		t.Fatalf("the turn was charged %d minutes (%v), not the moment it ran", m, err)
	}
	if got := b.typedInto(t, id); got != "" {
		t.Fatalf("keys reached the sign-in screen: %q", got)
	}
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1", At: c.now()})
	c.add(crew.RoleStartRetry - time.Minute)
	b.roleSettle(t)
	if r := b.ledgerRole(t); r.State != crew.RoleUnavailable || r.Open == nil || len(r.Open.Wakes) != 2 {
		t.Fatalf("the role fired into the same screen: %+v", r)
	}
	if page, _, _ := b.desk.st.List("", 10); len(page) != 1 {
		t.Fatalf("an unavailable role started %d turns", len(page))
	}
	// A person's own request meets the same screen and says the same.
	ask := b.briefing(t)
	if q := b.waitState(t, ask, brakeman.StateFailed); !strings.Contains(q.Why, "its login refused") {
		t.Fatalf("the person's request: %+v", q)
	}
	b.slotFree(t)
	if got := b.typedInto(t, ask); got != "" {
		t.Fatalf("keys reached the sign-in screen: %q", got)
	}
}

// TestABackgroundTurnEndsAtAStartupQuestion: an agent that opens on a
// startup question it has answers to (an update dialog) ends a wake turn,
// whose questions page no one, with words naming the dialog and what a
// person must do, the role unavailable with them, no key typed; a
// person's own turn on the same agent is not ended by it: the dialog
// shows under Needs you with its answers, for the person.
func TestABackgroundTurnEndsAtAStartupQuestion(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bkupdate", nil)
	id := b.failTask(t, c, "build")
	q := b.waitState(t, id, brakeman.StateFailed)
	if !strings.Contains(q.Why, "a background turn may not answer") || !strings.Contains(q.Why, "Update available") || !strings.Contains(q.Why, "answer it in a session of that agent") {
		t.Fatalf("the turn: %+v", q)
	}
	if r := b.waitRole(t, crew.RoleUnavailable); !strings.Contains(r.Why, "Update available") {
		t.Fatalf("the role: %+v", r)
	}
	b.slotFree(t)
	if got := b.typedInto(t, id); got != "" {
		t.Fatalf("keys reached the update dialog: %q", got)
	}
	ask := b.briefing(t)
	_, _, l := b.turnSession(t, ask)
	if !waitFor(30*time.Second, func() bool {
		a := l.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Source == session.SourceTrust && len(a.Options) > 0
	}) {
		t.Fatalf("the person's turn never showed the dialog: %+v", l.Info().Attention)
	}
	b.settle(t)
	if q, _ := b.desk.st.Get(ask); !q.Open() {
		t.Fatalf("the person's turn was ended at a dialog the person may answer: %+v", q)
	}
	b.expect(t, "POST", "/api/brakeman/requests/"+ask+"/cancel", map[string]any{"opId": "c-1"}, http.StatusOK, "")
	b.slotFree(t)
	if got := b.typedInto(t, ask); got != "" {
		t.Fatalf("keys reached the update dialog: %q", got)
	}
}

// launched waits for the launch of the turn holding the slot for id to
// have returned (its agent's process made): the moment its start
// bound counts from (deskTurn.started).
func (b *bkEnv) launched(t *testing.T, id string) {
	t.Helper()
	if !waitFor(30*time.Second, func() bool {
		b.desk.mu.Lock()
		defer b.desk.mu.Unlock()
		return b.desk.turn != nil && b.desk.turn.id == id && !b.desk.turn.started.IsZero()
	}) {
		t.Fatalf("%s's launch never returned", id)
	}
}

// ledgerRole is the role as its records hold it (the desk's store),
// read afresh: what a restart would take up.
func (b *bkEnv) ledgerRole(t *testing.T) crew.RoleRecord {
	t.Helper()
	r, err := b.desk.role.Peek()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// startBy is when the turn holding the slot for id ends as one whose
// agent did not start unless its agent calls (startBy, from the desk's
// own record of the launch's return), and how long after that return.
func (b *bkEnv) startBy(t *testing.T, id string) (time.Time, time.Duration) {
	t.Helper()
	q, err := b.desk.st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	b.desk.mu.Lock()
	defer b.desk.mu.Unlock()
	if b.desk.turn == nil || b.desk.turn.id != id || b.desk.turn.started.IsZero() {
		t.Fatalf("%s holds no slot with its launch returned", id)
	}
	by := startBy(q, b.desk.turn.started)
	return by, by.Sub(b.desk.turn.started)
}

// typedWait waits for the request id's scripted agent's terminal record
// (typedInto) to hold want, and returns the record.
func (b *bkEnv) typedWait(t *testing.T, id, want string) string {
	t.Helper()
	var got string
	if !waitFor(30*time.Second, func() bool {
		got = b.typedInto(t, id)
		return strings.Contains(got, want)
	}) {
		t.Fatalf("%q never reached %s's terminal: %q", want, id, got)
	}
	return got
}

// TestAnAgentAtAScreenNobodyKnowsDidNotStart: an agent that draws a
// full-screen prompt no catalog entry knows (another program's sign-in, a
// reworded screen), with no dialog's numbered choice on it, is not at its
// prompt: its screen does not match its agent's prompt shape (Claude
// Code's), so the request's line is never typed into it, nor its Enter
// (#714). Its agent then never calls the server, and the turn is held only
// until its start bound (a third of its 15 minutes: 5), on the desk's
// clock, far short of its deadline: it ends failed, its screen not
// recognised and its line not typed, with what its screen shows (its link
// left out), charged the minutes it ran. The role is unavailable with
// those words and a retry of its own, read from its records; a wake is
// gathered, not fired, and a person's own request in the same state says
// the same at the same bound instead of waiting out its minutes. At the
// role's retry the batch kept fires, and once the agent reads that
// request (it started) the role counts from 0 again and the bound no
// longer applies. Nothing reaches the screen throughout.
func TestAnAgentAtAScreenNobodyKnowsDidNotStart(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bkunknown", nil)
	id := b.failTask(t, c, "build")
	b.launched(t, id)
	_, _, l := b.turnSession(t, id)
	if !waitFor(30*time.Second, func() bool { return strings.Contains(strings.Join(l.ScreenLines(12), "\n"), "[ Sign in ]") }) {
		t.Fatalf("the screen was never drawn: %q", l.ScreenLines(12))
	}
	// The screen went quiet and is not the agent's prompt: the line waits.
	b.heldUnrecognised(t, id)
	by, wait := b.startBy(t, id)
	if wait != 5*time.Minute {
		t.Fatalf("a 15-minute turn's start bound is %v, want a third of it", wait)
	}
	c.add(by.Sub(c.now()) - time.Second)
	b.settle(t)
	if q, _ := b.desk.st.Get(id); !q.Open() {
		t.Fatalf("ended before its bound: %+v", q)
	}
	c.add(time.Second)
	b.settle(t)
	q := b.waitState(t, id, brakeman.StateFailed)
	for _, want := range []string{"the agent's screen was not recognised", "its request's line was not typed in 5 minutes", "You are not signed in.", "[ Sign in ]", "[link]"} {
		if !strings.Contains(q.Why, want) {
			t.Fatalf("the turn's words lack %q: %+v", want, q)
		}
	}
	if strings.Contains(q.Why, "example.invalid") || q.Reads != 0 {
		t.Fatalf("the turn: %+v", q)
	}
	b.waitRole(t, crew.RoleUnavailable)
	failedAt := c.now()
	r := b.ledgerRole(t)
	if r.Reason != crew.ReasonStart || r.Tries != 1 || !r.RetryAt.Equal(failedAt.Add(crew.RoleStartRetry)) || !strings.Contains(r.Why, "You are not signed in.") || r.Open == nil {
		t.Fatalf("the role: %+v", r)
	}
	b.slotFree(t)
	if m, err := b.desk.st.MinutesToday(c.now()); err != nil || m > int(wait/time.Minute) {
		t.Fatalf("the turn was charged %d minutes (%v), more than it ran", m, err)
	}
	// A person's own request in the same state: ended at the same bound.
	ask := b.briefing(t)
	b.launched(t, ask)
	_, _, la := b.turnSession(t, ask)
	if !waitFor(30*time.Second, func() bool { return strings.Contains(strings.Join(la.ScreenLines(12), "\n"), "[ Sign in ]") }) {
		t.Fatalf("the screen was never drawn: %q", la.ScreenLines(12))
	}
	b.heldUnrecognised(t, ask)
	by, _ = b.startBy(t, ask)
	c.add(by.Sub(c.now()))
	b.settle(t)
	if q := b.waitState(t, ask, brakeman.StateFailed); !strings.Contains(q.Why, "line was not typed in 5 minutes") || !strings.Contains(q.Why, "You are not signed in.") {
		t.Fatalf("the person's request: %+v", q)
	}
	b.slotFree(t)
	for _, req := range []string{id, ask} {
		if got := b.typedInto(t, req); got != "" {
			t.Fatalf("keys reached the screen: %q", got)
		}
	}
	// A wake meanwhile is gathered, not fired; nor is the batch by the
	// person's request.
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1", At: c.now()})
	b.roleSettle(t)
	if r := b.ledgerRole(t); r.State != crew.RoleUnavailable || r.Open == nil || len(r.Open.Wakes) != 2 {
		t.Fatalf("the role fired into the same screen: %+v", r)
	}
	if page, _, _ := b.desk.st.List("", 10); len(page) != 2 {
		t.Fatalf("%d requests, want the wake turn and the person's", len(page))
	}
	// The role's retry: the batch kept fires; its agent reads it now.
	c.add(failedAt.Add(crew.RoleStartRetry).Sub(c.now()))
	r = b.waitRole(t, crew.RoleBriefing)
	if r.Fired == nil || len(r.Fired.Wakes) != 2 || r.Request == "" {
		t.Fatalf("the retry: %+v", r)
	}
	b.launched(t, r.Request)
	sid, tok, _ := b.turnSession(t, r.Request)
	if status, _ := b.readRequest(t, sid, tok); status != http.StatusOK {
		t.Fatalf("read: %d", status)
	}
	if !waitFor(30*time.Second, func() bool { return b.ledgerRole(t).Tries == 0 }) {
		t.Fatalf("the agent started, yet the role counts: %+v", b.ledgerRole(t))
	}
	by, _ = b.startBy(t, r.Request)
	c.add(by.Sub(c.now()) + time.Minute)
	b.settle(t)
	if q, _ := b.desk.st.Get(r.Request); !q.Open() {
		t.Fatalf("a turn whose agent read its request was ended at the bound: %+v", q)
	}
}

// TestAnUnknownDialogGetsNoLine: an agent that opens on a dialog no
// catalog entry knows, a choice highlighted (bkDialogScript), is not at
// its prompt: a person's turn types nothing into it, neither its line nor
// the Enter that would pick the choice, and the engine notes once that the
// line waits (crew.UnknownScreen). At the turn's start bound, on the
// desk's clock, the turn ends failed with words saying the agent's screen
// was not recognised and what it shows; still nothing has reached it. The
// same turn against an agent at its prompt types its line
// (TestTheLineIsTypedAtTheAgentsPrompt).
func TestAnUnknownDialogGetsNoLine(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.fakeClock()
	b.enable(t, "bkdialog", nil)
	id := b.briefing(t)
	b.launched(t, id)
	b.heldUnrecognised(t, id)
	if got := b.typedInto(t, id); got != "" {
		t.Fatalf("keys reached the dialog: %q", got)
	}
	by, _ := b.startBy(t, id)
	c.add(by.Sub(c.now()))
	b.settle(t)
	q := b.waitState(t, id, brakeman.StateFailed)
	for _, want := range []string{"the agent's screen was not recognised", "its request's line was not typed", "Select a sign-in method", "1. Acme account with subscription"} {
		if !strings.Contains(q.Why, want) {
			t.Fatalf("the turn's words lack %q: %+v", want, q)
		}
	}
	b.slotFree(t)
	if got := b.typedInto(t, id); got != "" {
		t.Fatalf("keys reached the dialog: %q", got)
	}
}

// heldUnrecognised waits for the engine to note that the line of the turn
// for request id waits at a screen not recognised as its agent's prompt
// (crew.UnknownScreen): what the desk's words of the turn then say.
func (b *bkEnv) heldUnrecognised(t *testing.T, id string) {
	t.Helper()
	sid, _, l := b.turnSession(t, id)
	run := b.runOf(t, sid)
	if !waitFor(30*time.Second, func() bool {
		r, _ := b.srv.runs.Get(run)
		return slices.ContainsFunc(r.Log, func(e session.ActivityEntry) bool { return strings.Contains(e.Message, crew.UnknownScreen) })
	}) {
		t.Fatalf("the engine never held the line: %q", l.ScreenLines(12))
	}
}

// TestTheLineIsTypedAtTheAgentsPrompt: at its prompt, as Claude Code
// 2.1.319 and Codex 0.163.0 draw it (bkPromptScript, bkCodexPromptScript),
// each agent's screen matches its prompt shape: the request's line is
// typed, and its Enter pressed.
func TestTheLineIsTypedAtTheAgentsPrompt(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	for i, agent := range []string{"bkprompt", "bkcodexprompt"} {
		b.enable(t, agent, nil)
		ask := b.briefing(t)
		if got := b.typedWait(t, ask, "\r"); !strings.HasPrefix(got, "Brakeman request "+ask+" (briefing): ") || !strings.HasSuffix(got, "\r") {
			t.Fatalf("%s's terminal: %q", agent, got)
		}
		b.expect(t, "POST", "/api/brakeman/requests/"+ask+"/cancel", map[string]any{"opId": fmt.Sprintf("c-%d", i)}, http.StatusOK, "")
		b.slotFree(t)
	}
}

// TestATurnWhoseAgentNeverSettlesGetsNoLine: an agent at its prompt whose
// screen never goes quiet (bkSpinScript: a spinner under the prompt) is
// never ready; past the readiness wait's cap
// (a second here, SetReadyCapForTest) a person's run would have its
// prompt typed anyway, a Brakeman turn's line never is: the engine notes
// the line waits at a screen not recognised, and at the turn's start
// bound, on the desk's clock, the turn ends with words saying so. Nothing
// reaches the agent's terminal.
func TestATurnWhoseAgentNeverSettlesGetsNoLine(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.srv.runs.SetReadyCapForTest(time.Second)
	c := b.fakeClock()
	b.enable(t, "bkspin", nil)
	id := b.briefing(t)
	b.launched(t, id)
	b.heldUnrecognised(t, id)
	by, _ := b.startBy(t, id)
	c.add(by.Sub(c.now()))
	b.settle(t)
	q := b.waitState(t, id, brakeman.StateFailed)
	if !strings.Contains(q.Why, "the agent's screen was not recognised") || !strings.Contains(q.Why, "its request's line was not typed") {
		t.Fatalf("the turn's words: %+v", q)
	}
	b.slotFree(t)
	if got := b.typedInto(t, id); got != "" {
		t.Fatalf("keys reached the agent: %q", got)
	}
}

// TestClaudeCodesFirstRunScreensGetNoKey: Claude Code on an account
// folder never used opens on its theme picker, then its login-method
// screen (#659's third live check: the line's Enter picked a theme and a
// login method, and a browser opened). Both are the catalog's startup
// screens with no answers: a turn that meets either types nothing into
// it and ends at once with the screen's own words, its login refused,
// not at its start bound.
func TestClaudeCodesFirstRunScreensGetNoKey(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	for agent, words := range map[string]string{"bktheme": "Choose the text style that looks best with your terminal", "bklogin": "Select login method:"} {
		b.enable(t, agent, nil)
		id := b.briefing(t)
		q := b.waitState(t, id, brakeman.StateFailed)
		if !strings.Contains(q.Why, "its login refused") || !strings.Contains(q.Why, words) || q.Reads != 0 {
			t.Fatalf("%s's turn: %+v", agent, q)
		}
		b.slotFree(t)
		if got := b.typedInto(t, id); got != "" {
			t.Fatalf("keys reached %s's screen: %q", agent, got)
		}
	}
}

// TestAScreensWordsLeaveAWrappedLinkOutWhole: the words of a turn whose
// agent did not start are the screen's rows (ScreenLines), and a link
// wrapped across rows, its one-time state on the next row, is left out
// whole: a row that fills the screen's width runs on into the next. A
// row short of it does not, and blank rows are dropped.
func TestAScreensWordsLeaveAWrappedLinkOutWhole(t *testing.T) {
	t.Parallel()
	const cols = 20
	rows := []string{
		"  Acme Agent 9.0",
		"",
		"Open https://ex.inv/",
		"device?state=SECRET1",
		"23 to continue",
		"  [ Sign in ]",
	}
	got := screenWordsOf(rows, cols)
	if want := "Acme Agent 9.0 | Open [link] to continue | [ Sign in ]"; got != want {
		t.Fatalf("the words: %q, want %q", got, want)
	}
	if strings.Contains(got, "SECRET") || strings.Contains(got, "state=") {
		t.Fatalf("the link's state shows: %q", got)
	}
	// Read a row short of the width, the next row is its own line.
	if got := screenWordsOf([]string{"Open https://ex.inv/a", "Press Enter"}, 80); got != "Open [link] | Press Enter" {
		t.Fatalf("an unwrapped row: %q", got)
	}
	// The last screenWordLines lines not blank, the oldest dropped.
	var many []string
	for i := range screenWordLines + 3 {
		many = append(many, fmt.Sprintf("line %d", i), "")
	}
	if got := screenWordsOf(many, 80); !strings.HasPrefix(got, "line 3 |") || !strings.HasSuffix(got, fmt.Sprintf("line %d", screenWordLines+2)) {
		t.Fatalf("the last lines: %q", got)
	}
}

// TestAnAgentThatHasCalledRunsToItsMinutes: an agent slow to read its
// request (a loaded machine, a slow MCP start, a long look at the guide
// first) is not one that did not start: any call it makes with its own
// token counts. Its guide read a minute in, its request read six minutes
// in, past the 15-minute turn's start bound, the turn runs, and runs on
// to its minutes; the role is not made unavailable. Before, only the
// request's read counted, and the turn was ended at five minutes.
func TestAnAgentThatHasCalledRunsToItsMinutes(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	id := b.failTask(t, c, "build")
	b.launched(t, id)
	sid, tok, _ := b.turnSession(t, id)
	by, _ := b.startBy(t, id)
	c.add(time.Minute)
	b.settle(t)
	b.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide", nil, http.StatusOK, "")
	c.add(by.Sub(c.now()) + time.Minute)
	b.settle(t)
	if q, _ := b.desk.st.Get(id); !q.Open() {
		t.Fatalf("a turn whose agent read the guide was ended at the start bound: %+v", q)
	}
	if status, _ := b.readRequest(t, sid, tok); status != http.StatusOK {
		t.Fatalf("read: %d", status)
	}
	q, _ := b.desk.st.Get(id)
	c.add(q.Deadline.Sub(c.now()) - time.Minute)
	b.settle(t)
	if q, _ := b.desk.st.Get(id); q.State != brakeman.StateRunning {
		t.Fatalf("a turn whose agent called was ended before its minutes: %+v", q)
	}
	if r := b.ledgerRole(t); r.State != crew.RoleBriefing || r.Tries != 0 {
		t.Fatalf("the role: %+v", r)
	}
}

// TestALongTurnIsGivenLongerToStart: the start bound is a third of the
// turn's own minutes: a 60-minute turn's agent has 20 to make its first
// call, so one quiet for ten minutes (past the fixed five there were
// before) is still running, and one that has made no call at twenty is
// ended, the words naming its twenty.
func TestALongTurnIsGivenLongerToStart(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) {
		cfg["budget"].(map[string]any)["turnMinutes"] = 60
	})
	id := b.failTask(t, c, "build")
	b.launched(t, id)
	by, wait := b.startBy(t, id)
	if wait != 20*time.Minute {
		t.Fatalf("a 60-minute turn's start bound is %v, want a third of it", wait)
	}
	c.add(10 * time.Minute)
	b.settle(t)
	if q, _ := b.desk.st.Get(id); !q.Open() {
		t.Fatalf("a 60-minute turn was ended ten minutes in: %+v", q)
	}
	c.add(by.Sub(c.now()) - time.Second)
	b.settle(t)
	if q, _ := b.desk.st.Get(id); !q.Open() {
		t.Fatalf("ended before its bound: %+v", q)
	}
	c.add(time.Second)
	b.settle(t)
	if q := b.waitState(t, id, brakeman.StateFailed); !strings.Contains(q.Why, "did not start in 20 minutes, no call to Conductor") {
		t.Fatalf("the turn: %+v", q)
	}
}

// TestTheRecordOfATerminalCatchesOneKey: the scripted agents' record of
// their terminal (bkRecord), which the tests that promise no key reached
// a startup screen read, catches one byte with no Enter after it: a
// digit typed into the terminal is in the record at once. The agent is
// one at its update dialog, a person's turn, which the dialog does not
// end and into which Conductor types nothing: the digit is the only byte.
func TestTheRecordOfATerminalCatchesOneKey(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bkupdate", nil)
	ask := b.briefing(t)
	_, _, l := b.turnSession(t, ask)
	if !waitFor(30*time.Second, func() bool {
		a := l.Info().Attention
		return a.State == session.AttentionNeedsInput && a.Source == session.SourceTrust && len(a.Options) > 0
	}) {
		t.Fatalf("the dialog was never shown: %+v", l.Info().Attention)
	}
	if got := b.typedInto(t, ask); got != "" {
		t.Fatalf("typed before any key: %q", got)
	}
	sub, err := l.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Nate", Owner: true}, &gapSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Input(sub, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool { return b.typedInto(t, ask) == "1" }) {
		t.Fatalf("the record missed one key: %q", b.typedInto(t, ask))
	}
	b.expect(t, "POST", "/api/brakeman/requests/"+ask+"/cancel", map[string]any{"opId": "c-1"}, http.StatusOK, "")
	b.slotFree(t)
}

// TestASaveWeighsOnlyWhatItChanged: the role unavailable because its
// agent did not start (signed out at its sign-in screen) is left so by a
// save that changes nothing, and by one that changes what cannot start it
// (a grant, its visibility): same words, no request made. A save that
// changes what the agent starts with (its model here; an account, an
// agent) tries again: the batch kept fires.
func TestASaveWeighsOnlyWhatItChanged(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bksignin", nil)
	id := b.failTask(t, c, "build")
	b.waitState(t, id, brakeman.StateFailed)
	before := b.waitRole(t, crew.RoleUnavailable)
	b.slotFree(t)
	for name, mod := range map[string]func(map[string]any){
		"nothing":    func(map[string]any) {},
		"visibility": func(cfg map[string]any) { cfg["visibility"].(map[string]any)["touched"] = false },
	} {
		b.configure(t, mod)
		b.roleSettle(t)
		b.settle(t)
		if r := b.ledgerRole(t); r.State != crew.RoleUnavailable || r.Why != before.Why || r.Reason != crew.ReasonStart || !r.RetryAt.Equal(before.RetryAt) {
			t.Fatalf("a save of %s: %+v", name, r)
		}
		if page, _, _ := b.desk.st.List("", 10); len(page) != 1 {
			t.Fatalf("a save of %s started a turn: %d requests", name, len(page))
		}
	}
	b.configure(t, func(cfg map[string]any) { cfg["model"] = "sonnet" })
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		page, _, _ := b.desk.st.List("", 10)
		return len(page) == 2
	}) {
		t.Fatalf("a save of the model did not try again: %+v", b.ledgerRole(t))
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

// TestASavedConfigurationIsLookedAtAgain: the role unavailable with the
// day's turns spent looks again when the configuration is saved through
// its route: turns raised but minutes now spent, it stays unavailable in
// the words true now (the minutes, no longer the turns); the minutes
// raised too, the batch it kept fires, a wake request made. Each step is
// waited on in the role's record, never on a clock.
func TestASavedConfigurationIsLookedAtAgain(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", func(cfg map[string]any) { cfg["budget"].(map[string]any)["turnsPerDay"] = 1 })
	id := b.briefing(t)
	b.waitState(t, id, brakeman.StateStarting, brakeman.StateRunning)
	b.desk.roleWake(crew.RoleWake{Source: crew.WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1", At: c.now()})
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	if r := b.waitRole(t, crew.RoleUnavailable); !strings.Contains(r.Why, "1 of 1 turns") {
		t.Fatalf("%+v", r)
	}
	b.configure(t, func(cfg map[string]any) {
		bu := cfg["budget"].(map[string]any)
		bu["turnsPerDay"], bu["minutesPerDay"] = 3, 10
	})
	var r crew.RoleRecord
	if !waitFor(30*time.Second, func() bool {
		b.desk.role.Poke()
		r = b.roleRecord(t)
		return r.State == crew.RoleUnavailable && strings.Contains(r.Why, "of 10 minutes")
	}) {
		t.Fatalf("the role after a save that still refuses: %+v", r)
	}
	if r.Open == nil || len(r.Open.Wakes) != 1 || r.Request != "" {
		t.Fatalf("the batch is not kept: %+v", r)
	}
	b.configure(t, func(cfg map[string]any) { cfg["budget"].(map[string]any)["minutesPerDay"] = 240 })
	r = b.waitRole(t, crew.RoleBriefing)
	q, err := b.desk.st.Get(r.Request)
	if err != nil || q.Kind != brakeman.KindWake || r.Why != "" || r.Fired == nil || len(r.Fired.Wakes) != 1 || r.Fired.Wakes[0].Subject != "task:run-1/build" {
		t.Fatalf("the batch kept, fired: %+v %+v %v", r, q, err)
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

// TestAWakeBatchReadIsInTheBriefsFootprint: a wake turn reads its batch,
// a ticket made ready among its wakes, and keeps a brief citing only its
// own session: the brief's footprint holds Dispatch and the runs its
// task's wake is of, so a later turn that may not see Dispatch is given
// the brief withheld.
func TestAWakeBatchReadIsInTheBriefsFootprint(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t-1", "project": "CND", "title": "Ship the role"}, http.StatusCreated, "")
	b.readyTicket(t, "CND-1", "e-1")
	id := b.failTask(t, c, "build")
	sid, tok, _ := b.turnSession(t, id)
	b.waitState(t, id, brakeman.StateRunning)
	status, out := b.readRequest(t, sid, tok)
	batch, _ := entityOf(t, out, "request")["batch"].(map[string]any)
	if status != http.StatusOK || !strings.Contains(fmt.Sprint(batch["wakes"]), "ticket:CND-1") {
		t.Fatalf("%d %v", status, out)
	}
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	if r, _ := b.desk.st.Get(id); r.Footprint == nil || !slices.Contains(r.Footprint.Classes, brakeman.ClassDispatch) || !slices.Contains(r.Footprint.Classes, brakeman.ClassRuns) {
		t.Fatalf("the brief's footprint: %+v", r.Footprint)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.waitState(t, id, brakeman.StateBriefed)
	b.slotFree(t)
	b.configure(t, func(c map[string]any) { c["visibility"].(map[string]any)["dispatch"] = false })
	ask := b.briefing(t)
	sid2, tok2, _ := b.turnSession(t, ask)
	b.waitState(t, ask, brakeman.StateRunning)
	req, err := http.NewRequest(http.MethodGet, b.http.URL+"/api/brakeman/briefs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok2)
	req.Header.Set(brakemanSessionHeader, sid2)
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var briefs map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&briefs); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the briefs: %d %v %v", resp.StatusCode, err, briefs)
	}
	found := false
	for _, x := range briefs["briefs"].([]any) {
		if m := x.(map[string]any); m["id"] == id {
			found = true
			if whole(m) || m["withheld"] == nil {
				t.Fatalf("the wake turn's brief, with Dispatch not visible: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("the brief %s is not listed: %v", id, briefs)
	}
}

// TestTurningTheRoleOffCancelsAQueuedWake: a wake request queued behind
// a person's turn when the role is set to run only when asked is
// cancelled, and never starts once the slot is free.
func TestTurningTheRoleOffCancelsAQueuedWake(t *testing.T) {
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
	b.configure(t, func(c map[string]any) { c["wake"].(map[string]any)["mode"] = brakeman.WakeInvoked })
	if r := b.waitState(t, wake, brakeman.StateQueued, brakeman.StateCancelled, brakeman.StateStarting, brakeman.StateRunning); r.State != brakeman.StateCancelled {
		t.Fatalf("the wake request with the role off: %s", r.State)
	}
	if status, out := b.submit(t, sid, tok, "b-1", factBrief("session:"+sid)); status != http.StatusCreated {
		t.Fatalf("brief: %d %v", status, out)
	}
	c.add(brakemanGrace + time.Second)
	b.settle(t)
	b.waitState(t, ask, brakeman.StateBriefed)
	b.slotFree(t)
	b.settle(t)
	if r, _ := b.desk.st.Get(wake); r.State != brakeman.StateCancelled {
		t.Fatalf("the cancelled wake request is %s", r.State)
	}
}

// TestTurningTheRoleOffDropsItsBriefingMarkFirst: a save that turns the
// role off drops the briefing times' mark before the configuration is
// written, so one whose mark cannot be written changes nothing.
func TestTurningTheRoleOffDropsItsBriefingMarkFirst(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	if b.roleRecord(t).Briefed.IsZero() {
		t.Fatal("the role's first look marked nothing")
	}
	fix := b.breakRoleFile(t)
	c := b.config(t)
	rev := c["rev"]
	c["wake"].(map[string]any)["mode"] = brakeman.WakeInvoked
	b.expect(t, "PUT", "/api/brakeman/config", map[string]any{"rev": rev, "config": c}, http.StatusInternalServerError, "store_failed")
	if !b.desk.mustConfig(t).Background() {
		t.Fatal("the role was turned off with its mark not written")
	}
	fix()
	b.configure(t, func(c map[string]any) { c["wake"].(map[string]any)["mode"] = brakeman.WakeInvoked })
	if r, err := b.desk.role.Peek(); err != nil || !r.Briefed.IsZero() {
		t.Fatalf("the mark after the role was turned off: %v %+v", err, r.Briefed)
	}
}

// TestAPersonsRequestOutlivesAFailedWrite: a person's request accepted
// while the role's record cannot be written is noted once it can be,
// unprompted, and its op id asked again logs it once.
func TestAPersonsRequestOutlivesAFailedWrite(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	fix := b.breakRoleFile(t)
	body := map[string]any{"opId": "ask-once", "kind": "briefing"}
	status, _, out := b.call(t, "POST", "/api/brakeman/requests", body)
	if status != http.StatusCreated {
		t.Fatalf("ask: %d %v", status, out)
	}
	id := entityOf(t, out, "request")["id"].(string)
	if b.hasWake(t, crew.WakeRequest, id) {
		t.Fatal("the request was noted with the write failing")
	}
	fix()
	if !waitFor(60*time.Second, func() bool { return b.hasWake(t, crew.WakeRequest, id) }) {
		t.Fatalf("the person's request was lost with the failed write: %+v", b.roleRecord(t).Log)
	}
	if status, _, out := b.call(t, "POST", "/api/brakeman/requests", body); status != http.StatusOK {
		t.Fatalf("ask again: %d %v", status, out)
	}
	b.roleSettle(t)
	n := 0
	for _, row := range b.roleRecord(t).Log {
		if row.Source == crew.WakeRequest && row.Subject == id {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the request is logged %d times", n)
	}
}

// TestTheFeedReadsOnPastAPassesPages: with the role's pass held, more of
// Dispatch's changes wait than a pass reads (its pages made small); once
// the pass is let go, every ticket made ready wakes the role, though no
// commit is announced after.
func TestTheFeedReadsOnPastAPassesPages(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.background(t, "bk", nil)
	b.desk.src.mu.Lock()
	b.desk.src.ticketPage, b.desk.src.ticketPages = 1, 2
	b.desk.src.mu.Unlock()
	b.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p-1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	b.desk.recovered.Store(false)
	b.roleSettle(t)
	for i := 1; i <= 3; i++ {
		b.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": fmt.Sprintf("t-%d", i), "project": "CND", "title": fmt.Sprintf("Ticket %d", i)}, http.StatusCreated, "")
		b.expect(t, "POST", fmt.Sprintf("/api/dispatch/tickets/CND-%d/edit", i), map[string]any{"opId": fmt.Sprintf("e-%d", i), "set": map[string]any{"status": "ready"}}, http.StatusOK, "")
	}
	head, err := b.srv.dispatch.Head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		b.desk.src.mu.Lock()
		defer b.desk.src.mu.Unlock()
		return b.desk.src.ticketTop >= head
	}) {
		t.Fatal("the role never heard of the last commit")
	}
	b.desk.src.mu.Lock()
	if left := head - b.desk.src.ticketSeq; left <= 2 {
		b.desk.src.mu.Unlock()
		t.Fatalf("only %d changes wait: a pass reads them all", left)
	}
	b.desk.src.mu.Unlock()
	b.desk.recovered.Store(true)
	b.desk.role.Poke()
	// Waited on without a poke: only the pass that reached its pages asks
	// for the next.
	if !waitFor(30*time.Second, func() bool {
		return b.hasWake(t, crew.WakeTicketReady, "ticket:CND-1") && b.hasWake(t, crew.WakeTicketReady, "ticket:CND-2") && b.hasWake(t, crew.WakeTicketReady, "ticket:CND-3")
	}) {
		t.Fatalf("a ticket made ready past the pass's pages woke nothing: %+v", b.roleRecord(t).Log)
	}
}

// TestAWakeOpIDIsTheRolesOwn: a person's request may not take an op id
// beginning wake-; one kept under wake-wb-1 before that was refused is
// not taken for the batch wb-1's request, which is made under the
// batch's next op id and runs.
func TestAWakeOpIDIsTheRolesOwn(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	c := b.background(t, "bk", nil)
	if status, _, out := b.call(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "wake-wb-2", "kind": "briefing"}); status != http.StatusBadRequest {
		t.Fatalf("a person's request under a wake op id: %d %v", status, out)
	}
	old, _, err := b.desk.st.Create(brakeman.Request{OpID: "wake-wb-1", Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.desk.closeTurn(old.ID, brakeman.StateCancelled, "kept before wake- was the role's"); err != nil {
		t.Fatal(err)
	}
	id := b.failTask(t, c, "build")
	if id == old.ID {
		t.Fatalf("the batch was taken as fired by %s", old.ID)
	}
	req := b.waitState(t, id, brakeman.StateStarting, brakeman.StateRunning)
	if req.Kind != brakeman.KindWake || req.Batch != "wb-1" || req.OpID != "wake-wb-1.2" {
		t.Fatalf("%+v", req)
	}
	if r, _ := b.desk.st.Get(old.ID); r.Kind != brakeman.KindBriefing || r.State != brakeman.StateCancelled {
		t.Fatalf("the person's request: %+v", r)
	}
}
