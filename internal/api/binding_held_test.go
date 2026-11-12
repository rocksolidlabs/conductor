package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// removeAccount asks DELETE /api/accounts/{id} at the current rev: its
// status and reply.
func (e *bindEnv) removeAccount(t *testing.T, id string) (int, map[string]any) {
	t.Helper()
	status, _, out := e.call(t, "DELETE", "/api/accounts/"+id+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil)
	return status, out
}

// An account is held while a process started under it can still run: a
// session whose stop failed holds it, and so does one removed from the
// listing while its process runs; once the process has ended, nothing
// does.
func TestAccountHeldWhileItsProcessRuns(t *testing.T) {
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 4)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := &bindEnv{testEnv: capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin),
		withNativeRuntime(&sessiontest.FakeRuntime{Place: "seam", Base: terms})), probes: probes, pin: pin}
	a, _ := e.account(t, "claude", "Work")
	out := e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": a, "name": "held"}, http.StatusCreated, "")
	sid := out["id"].(string)
	p := <-terms.procs
	l := e.local(sid)
	if err := l.Stop(context.Background()); err == nil || !l.Info().Status.Ended() {
		t.Fatalf("the stop: %v, the status %s", err, l.Info().Status)
	}
	status, out := e.removeAccount(t, a)
	if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), "session held") {
		t.Fatalf("its stop failed, its process running: %d %v", status, out)
	}
	if resp, out := e.do("DELETE", "/api/sessions/"+sid, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the removal: %d %v", resp.StatusCode, out)
	}
	status, out = e.removeAccount(t, a)
	if status != http.StatusConflict || !strings.Contains(strings.Join(namesOf(out), "|"), "session held") {
		t.Fatalf("removed from the listing, its process running: %d %v", status, out)
	}
	p.End(0)
	<-l.ProcessDone()
	if status, out := e.removeAccount(t, a); status != http.StatusNoContent {
		t.Fatalf("its process ended: %d %v", status, out)
	}
}

// The ledger's runs hold the accounts their crews as launched bind, with
// the engine holding none of them: one running and one interrupted are
// named; one whose crew cannot be read holds every account; one stopped
// is no bar, nor one interrupted that another run continued.
func TestRemoveAccountInUseByTheLedger(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	bound, _ := json.Marshal(crew.Crew{ID: "kept", Name: "Kept", Goal: "g", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "claude-p", Start: crew.Start{When: crew.StartTask},
			Bound: &session.BoundAccount{ID: a, Label: "Work", Folder: folderA, Source: session.SourceAccount, Login: keptLogin("claude", folderA)}}}})
	put := func(id, state, crewJSON string) {
		t.Helper()
		if _, err := e.srv.ledger.Transition(t.Context(), id, func(tx *ledger.Tx) error {
			return tx.CreateRun(ledger.RunRow{ID: id, CrewID: "kept", Crew: crewJSON, State: state, CreatedAt: time.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("kept-0000aaaa", ledger.RunRunning, string(bound))
	put("kept-0000bbbb", ledger.RunInterrupted, string(bound))
	put("kept-0000cccc", ledger.RunStopped, string(bound))
	status, out := e.removeAccount(t, a)
	names := strings.Join(namesOf(out), "|")
	if status != http.StatusConflict || !strings.Contains(names, "run kept-0000aaaa") || !strings.Contains(names, "run kept-0000bbbb") || strings.Contains(names, "kept-0000cccc") {
		t.Fatalf("the ledger's runs: %d %v", status, out)
	}
	for _, id := range []string{"kept-0000aaaa", "kept-0000bbbb"} {
		if _, err := e.srv.ledger.Transition(t.Context(), id, func(tx *ledger.Tx) error {
			return tx.PutRun(ledger.RunRow{ID: id, CrewID: "kept", Crew: string(bound), State: ledger.RunStopped, CreatedAt: time.Now(), ClosedAt: time.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("kept-0000dddd", ledger.RunInterrupted, `{"members":[{"name":`)
	status, out = e.removeAccount(t, a)
	if names := strings.Join(namesOf(out), "|"); status != http.StatusConflict || !strings.Contains(names, "kept-0000dddd") || strings.Contains(names, "kept-0000aaaa") {
		t.Fatalf("a crew that cannot be read: %d %v", status, out)
	}
	// An interrupted run another run continued starts nothing again and
	// holds nothing: its continuation's own row holds what it binds, and,
	// that one ended, the account is taken.
	if _, err := e.srv.ledger.Transition(t.Context(), "kept-0000dddd", func(tx *ledger.Tx) error {
		return tx.PutRun(ledger.RunRow{ID: "kept-0000dddd", CrewID: "kept", Crew: string(bound), State: ledger.RunStopped, CreatedAt: time.Now(), ClosedAt: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	put("kept-0000eeee", ledger.RunInterrupted, string(bound))
	if _, err := e.srv.ledger.Transition(t.Context(), "kept-0000ffff", func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: "kept-0000ffff", CrewID: "kept", Crew: string(bound), State: ledger.RunStopped, ContinuedFrom: "kept-0000eeee", CreatedAt: time.Now(), ClosedAt: time.Now()}); err != nil {
			return err
		}
		return tx.Continued("kept-0000eeee")
	}); err != nil {
		t.Fatal(err)
	}
	if status, out := e.removeAccount(t, a); status != http.StatusNoContent {
		t.Fatalf("an interrupted run continued by one that ended: %d %v", status, out)
	}
}

// An account is held by whatever keeps its folder: an entry that pins a
// registered account's folder, launched naming none, is bound pinned (its
// source what the environment says) and holds that account: the remove is
// refused naming its session, and the account's count includes it.
func TestAccountHeldByItsFolder(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folder := e.account(t, "claude", "Work")
	e.addAgents(t, map[string]map[string]string{"claude-on-a": {"CLAUDE_CONFIG_DIR": folder}})
	sid := e.launch(t, "claude-on-a", map[string]any{"name": "in its folder"})
	if got, b := e.probe(t, sid), e.bound(t, sid); got != login(folder) || b.ID != capacity.DefaultID || b.Source != session.SourcePinned || b.Folder != folder {
		t.Fatalf("the launch: %s %+v", got, b)
	}
	status, out := e.removeAccount(t, a)
	if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), "session in its folder") {
		t.Fatalf("a session in the account's folder: %d %v", status, out)
	}
	if v := viewOfKey(t, e.testEnv, "agent=claude-on-a&account="+a); v["counts"].(map[string]any)["running"] != float64(1) {
		t.Fatalf("the account's counts: %v", v["counts"])
	}
}

// A launch whose process started holds its account until that process has
// ended, whether or not its session was registered: at the session cap the
// launch's registration is refused after its process started, and its stop
// fails; the remove is refused while the process lives (a launch under
// way) and taken once it has ended.
func TestAccountHeldByAProcessNeverRegistered(t *testing.T) {
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 4)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := &bindEnv{testEnv: capEnv(t, func(c *config.Config) { c.MaxSessions = 1 }, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin),
		withNativeRuntime(&sessiontest.FakeRuntime{Place: "seam", Base: terms})), probes: probes, pin: pin}
	a, _ := e.account(t, "claude", "Work")
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p"}, http.StatusCreated, "")
	first := <-terms.procs
	t.Cleanup(func() { first.End(0) })
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": a}, http.StatusConflict, "too_many_sessions")
	p := <-terms.procs
	status, out := e.removeAccount(t, a)
	if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), "launch") {
		t.Fatalf("its registration refused, its stop failed, its process running: %d %v", status, out)
	}
	p.End(0)
	// The release follows the process's end: the remove is asked until it
	// is taken, and is never anything but in use before.
	for {
		status, out = e.removeAccount(t, a)
		if status == http.StatusNoContent {
			break
		}
		if status != http.StatusConflict {
			t.Fatalf("its process ended: %d %v", status, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// managedOf saves a managed crew of one task, build, done by lead, with
// lead's fields over agentId and start: the crew's id.
func (e *bindEnv) managedOf(t *testing.T, lead map[string]any) string {
	t.Helper()
	m := map[string]any{"name": "lead", "prompt": "", "start": map[string]any{"when": "task"}}
	for k, v := range lead {
		m[k] = v
	}
	return e.sendCrew("POST", "/api/crews", map[string]any{"name": "Kept", "goal": "build it", "cwd": e.root, "where": "server", "isolation": "worktree",
		"members": []any{m}, "tasks": []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusCreated)["id"].(string)
}

// A managed run's member on a registered account, its run stopped by the
// server's end: the next server's continuation of it launches its first
// attempt on the account. Stopped again, the run is no bar, and the
// account removed, the next continuation's attempt fails at its launch
// with words (account_gone) and starts no process. (The ledger's runs
// running or interrupted holding the account, the engine holding none:
// TestRemoveAccountInUseByTheLedger.)
func TestContinuationKeepsAccounts(t *testing.T) {
	e := newBindEnv(t, nil)
	e.gitRepo(t)
	a, folderA := e.account(t, "claude", "Work")
	id := e.managedOf(t, map[string]any{"agentId": "claude-p", "account": a})
	out := e.expect(t, "POST", "/api/crews/"+id+"/launch", nil, http.StatusCreated, "")
	run := out["run"].(map[string]any)["id"].(string)
	if sid := startedAttempt(t, e.testEnv, run, 1)["sessionId"].(string); e.probe(t, sid) != login(folderA) {
		t.Fatalf("attempt 1's login: %s", e.probe(t, sid))
	}
	// The server's end stops the run; the next server continues it.
	shutdown(e.testEnv)
	next := &bindEnv{testEnv: serveSecond(t, e.testEnv, newFakeUsage("claude"), newFakeUsage("codex")), probes: e.probes, pin: e.pin}
	out = next.expect(t, "POST", "/api/runs/"+run+"/resume", nil, http.StatusCreated, "")
	cont := out["run"].(map[string]any)["id"].(string)
	att := startedAttempt(t, next.testEnv, cont, 0)
	if got := next.probe(t, att["sessionId"].(string)); got != login(folderA) {
		t.Fatalf("the continuation's first attempt: %s", got)
	}
	next.expect(t, "POST", "/api/runs/"+cont+"/stop", nil, http.StatusOK, "")
	next.stopAndWait(att["sessionId"].(string))
	if status, out := next.removeAccount(t, a); status != http.StatusNoContent {
		t.Fatalf("the stopped run: %d %v", status, out)
	}
	out = next.expect(t, "POST", "/api/runs/"+cont+"/resume", nil, http.StatusCreated, "")
	gone := out["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { next.do("POST", "/api/runs/"+gone+"/stop", adminToken, nil) })
	if row := ledgerAttemptIn(t, next.srv, gone, "build", 0, ledger.AttemptFailed); !strings.Contains(row.Error, "launch it anew") || row.SessionID != "" {
		t.Fatalf("the continuation's attempt, its account gone: %+v", row)
	}
}

// startedAttempt is build's attempt n (any, for 0) once its session has
// started: the stubs print nothing, so it stays launched, never running.
func startedAttempt(t *testing.T, e *testEnv, run string, n int) map[string]any {
	t.Helper()
	return taskVia(t, e, run, "build", "started", func(task map[string]any) bool {
		a, _ := task["attempt"].(map[string]any)
		sid, _ := a["sessionId"].(string)
		return sid != "" && (n == 0 || a["n"] == float64(n))
	})["attempt"].(map[string]any)
}

// catalogLoad is the catalog of list with a put in its entry's place.
func catalogLoad(list []catalog.Agent, a catalog.Agent) (catalog.Catalog, error) {
	return catalog.Load(catalog.File{DisableDefaults: true, Agents: replaceAgent(list, a)})
}

// TestAttemptAccountGone: a managed run's next attempt whose member's
// login is not the bound one (the pinned entry's folder changed in the
// catalog) fails at its launch with words (account_gone), and no process
// is started for it.
func TestAttemptAccountGone(t *testing.T) {
	e := newBindEnv(t, nil)
	e.gitRepo(t)
	id := e.managedOf(t, map[string]any{"agentId": "claude-pin"})
	out := e.expect(t, "POST", "/api/crews/"+id+"/launch", nil, http.StatusCreated, "")
	run := out["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
	sid := startedAttempt(t, e.testEnv, run, 1)["sessionId"].(string)
	if got := e.probe(t, sid); got != login(e.pin) {
		t.Fatalf("attempt 1's login: %s", got)
	}
	cat := e.srv.Catalog()
	p, _ := cat.Get("claude-pin")
	p.Env = map[string]string{"PROBE_DIR": e.probes, "CLAUDE_CONFIG_DIR": realRoot(t, t.TempDir())}
	changed, err := catalogLoad(cat.List(), p)
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(changed)
	command(t, e.testEnv, run, "tasks/build/cancel")
	command(t, e.testEnv, run, "tasks/build/retry")
	if row := ledgerAttemptIn(t, e.srv, run, "build", 2, ledger.AttemptFailed); !strings.Contains(row.Error, "launch it anew") || row.SessionID != "" {
		t.Fatalf("attempt 2, its login changed: %+v", row)
	}
}
