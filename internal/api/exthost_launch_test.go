package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A provider's launch with an origin and its three doors (Run, RunByOp,
// StopRun): exthost_launch.go.

// originCrew saves a crew of one task in e's repository, and answers its
// id.
func originCrew(t *testing.T, e *testEnv, name string) string {
	t.Helper()
	return e.sendCrew("POST", "/api/crews", map[string]any{"name": name, "goal": "g", "cwd": e.root, "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks":   []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusCreated)["id"].(string)
}

// endRunsAtCleanup ends, as the test ends and before its folders go, every
// run e's engine holds: each managed one once its first attempt is made
// (or failed, or the run paused, stopped or done), so no attempt's
// worktree is being made as the test's folders are removed.
func endRunsAtCleanup(t *testing.T, e *testEnv) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		for _, r := range e.srv.runs.List() {
			waitFor(testWait, func() bool {
				run, ok := e.srv.runs.Get(r.ID)
				if !ok || run.Managed == nil || run.StoppedAt != nil || run.Paused || run.Outcome != "" {
					return true
				}
				for _, ts := range run.Tasks {
					if a := ts.Attempt; a != nil && a.State != "" && a.State != "reserved" {
						return true
					}
				}
				return false
			})
			_ = e.srv.runs.Stop(ctx, r.ID)
		}
	})
}

// restartEnding is e.restart, its runs ended as the test ends.
func restartEnding(t *testing.T, e *testEnv) *testEnv {
	t.Helper()
	again := e.restart()
	endRunsAtCleanup(t, again)
	return again
}

// originEnv is a ready environment with a repository and one saved crew
// of tasks, and the crew's id.
func originEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	e := readyEnv(t, nil)
	e.gitRepo(t)
	endRunsAtCleanup(t, e)
	return e, originCrew(t, e, "Reviews")
}

// originLaunch is a provider's launch of the crew with the op id.
func originLaunch(crewID, opID string) ext.LaunchRequest {
	return ext.LaunchRequest{Crew: crewID, By: ada, OpID: opID, Origin: ext.Origin{Ref: "example:42", Label: "Review of change 42"},
		Subject:   ext.Subject{Ref: "change:git.example.com/acme/app#42@" + strings.Repeat("a", 40), URL: "https://git.example.com/acme/app/pull/42"},
		StopAfter: time.Hour}
}

// crashServer ends e's server as a crash would for the ledger: its engine
// lets the runs go, its providers end, and its ledger lets go of the owner
// lock, nothing recorded; the runs' sessions end with the test.
func crashServer(t *testing.T, e *testEnv, runs ...string) {
	t.Helper()
	e.stopEverything(t)
	for _, id := range runs {
		e.srv.runs.ForgetForTest(id)
	}
	e.srv.stopExtensions()
	if err := e.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
}

// ledgerRuns is how many runs the server's ledger holds.
func ledgerRuns(t *testing.T, e *testEnv) int {
	t.Helper()
	rows, err := e.srv.ledger.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// A launch with an operation id makes one run, whatever the retries: two
// calls answer one run, and a server whose ledger is read-only answers the
// retry from it; a restart between them, one run; two calls at once, one
// run, both answered its id, one origin row.
func TestHostLaunchOpIDIsIdempotent(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	first, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil || again.ID != first.ID || again.Crew != id || again.URL != first.URL {
		t.Fatalf("the retry: %+v %v, the first %+v", again, err, first)
	}
	if n := ledgerRuns(t, e); n != 1 {
		t.Fatalf("%d runs after a retry", n)
	}
	// A retry is answered from the ledger before anything is launched: a
	// second server over the same data, whose ledger is read-only while
	// the first owns it and which launches no crew with tasks, answers it.
	ro := restartEnding(t, e)
	if r, _ := ro.srv.ledger.ReadOnly(); !r {
		t.Fatal("the second server owns the ledger")
	}
	if got, err := testHost(ro.srv, t.Context(), "example").Launch(t.Context(), originLaunch(id, "op-1")); err != nil || got.ID != first.ID {
		t.Fatalf("the retry on a read-only ledger: %+v %v", got, err)
	}
	shutdown(ro)
	shutdown(e)
	e2 := restartEnding(t, e)
	h2 := testHost(e2.srv, t.Context(), "example")
	after, err := h2.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil || after.ID != first.ID {
		t.Fatalf("the retry after a restart: %+v %v", after, err)
	}
	if n := ledgerRuns(t, e2); n != 1 {
		t.Fatalf("%d runs after a restart's retry", n)
	}

	// Two at once: one run, both answered it.
	var wg sync.WaitGroup
	refs := make([]ext.RunRef, 2)
	errs := make([]error, 2)
	for i := range refs {
		wg.Go(func() { refs[i], errs[i] = h2.Launch(t.Context(), originLaunch(id, "op-2")) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || refs[0].ID != refs[1].ID || refs[0].ID == first.ID {
		t.Fatalf("two launches at once: %+v %v", refs, errs)
	}
	if n := ledgerRuns(t, e2); n != 2 {
		t.Fatalf("%d runs after two launches at once", n)
	}
	if row, ok, err := e2.srv.ledger.RunByOp(t.Context(), "example", "op-2"); err != nil || !ok || row.RunID != refs[0].ID {
		t.Fatalf("op-2's row: %+v %v %v", row, ok, err)
	}
	if _, ok := e2.srv.runs.Get(refs[0].ID); !ok {
		t.Fatal("the engine does not hold op-2's run")
	}
	// Its attempt made, then stopped, before the test's folders go.
	runningAttempt(t, e2, refs[0].ID, "build", 1)
	if err := e2.srv.runs.Stop(t.Context(), refs[0].ID); err != nil {
		t.Fatal(err)
	}
}

// A retry with an operation id answers the run that id made, whatever else
// it asks (another crew, subject or limit), and launches nothing; the
// run's status says what it was launched with. Another provider's same id
// is its own, and makes its own run.
func TestHostLaunchOpIDAnswersItsRunWhateverTheRequest(t *testing.T) {
	e, id := originEnv(t)
	other := originCrew(t, e, "Other")
	h := testHost(e.srv, t.Context(), "example")
	first, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	changed := []func(*ext.LaunchRequest){
		func(r *ext.LaunchRequest) { r.Crew = other },
		func(r *ext.LaunchRequest) {
			r.Subject.Ref = "change:git.example.com/acme/app#43@" + strings.Repeat("b", 40)
		},
		func(r *ext.LaunchRequest) { r.StopAfter = 2 * time.Hour },
		func(r *ext.LaunchRequest) { r.Origin.Label = "Another" },
	}
	for i, change := range changed {
		r := originLaunch(id, "op-1")
		change(&r)
		ref, err := h.Launch(t.Context(), r)
		if err != nil || ref.ID != first.ID || ref.Crew != id {
			t.Fatalf("retry %d: %+v %v", i, ref, err)
		}
	}
	if n := ledgerRuns(t, e); n != 1 {
		t.Fatalf("%d runs after the retries", n)
	}
	st, err := h.Run(t.Context(), first.ID)
	if err != nil || st.Origin.Label != "Review of change 42" || st.Origin.OpID != "op-1" || !strings.Contains(st.Origin.Subject.Ref, "#42@") {
		t.Fatalf("the run's status: %+v %v", st, err)
	}
	theirs, err := testHost(e.srv, t.Context(), "elsewhere").Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil || theirs.ID == first.ID {
		t.Fatalf("another provider's op-1: %+v %v", theirs, err)
	}
	if n := ledgerRuns(t, e); n != 2 {
		t.Fatalf("%d runs after another provider's launch", n)
	}
}

// The origin is committed with the run's first row, before any attempt:
// the run's first event is run.created, and its origin row is there with
// it. A crash before the record is written loses nothing: after a restart
// the record is written again from the ledger with the origin and the
// deadline, a retry with the op id answers that run, and RunByOp finds it,
// interrupted.
func TestOriginCommittedBeforeTheFirstAttempt(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	before := time.Now().UTC().Truncate(time.Second)
	ref, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := e.srv.ledger.Events(t.Context(), ref.ID, 0, 100)
	if err != nil || len(evs) == 0 || evs[0].Kind != "run.created" {
		t.Fatalf("the run's first event: %+v %v", evs, err)
	}
	row, ok, err := e.srv.ledger.Origin(t.Context(), ref.ID)
	if err != nil || !ok || row.OpID != "op-1" || row.Provider != "example" || row.Deadline.Before(before.Add(time.Hour)) || row.LimitSeconds != 3600 {
		t.Fatalf("the origin row: %+v %v %v", row, ok, err)
	}
	crashServer(t, e, ref.ID)
	// The record as a crash before its write leaves it: none.
	if err := os.Remove(filepath.Join(e.srv.cfg.DataDir, "runs", ref.ID+".json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	e2 := restartEnding(t, e)
	h2 := testHost(e2.srv, t.Context(), "example")
	resp, out := e2.do("GET", "/api/runs/"+ref.ID, adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the run after a restart: %d %v", resp.StatusCode, out)
	}
	origin, _ := out["run"].(map[string]any)["origin"].(map[string]any)
	if origin["provider"] != "example" || origin["deadline"] != row.Deadline.Format(time.RFC3339) || origin["limitSeconds"] != float64(3600) || origin["subjectUrl"] == nil {
		t.Fatalf("the recovered record's origin: %v", origin)
	}
	if again, err := h2.Launch(t.Context(), originLaunch(id, "op-1")); err != nil || again.ID != ref.ID {
		t.Fatalf("the retry after a restart: %+v %v", again, err)
	}
	st, err := h2.RunByOp(t.Context(), "op-1")
	if err != nil || st.ID != ref.ID || st.State != ext.RunInterrupted || !st.Origin.Deadline.Equal(row.Deadline) {
		t.Fatalf("RunByOp after a restart: %+v %v", st, err)
	}
}

// RunByOp never creates: an unknown op id is ext.ErrNotFound, with no run,
// no row and no revision moved; a read-only ledger answers it too.
func TestRunByOpNeverCreates(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	ref, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, ref.ID, "build", 1)
	row, _, _ := e.srv.ledger.Run(t.Context(), ref.ID)
	if _, err := h.RunByOp(t.Context(), "op-2"); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("an unknown op id: %v", err)
	}
	if n := ledgerRuns(t, e); n != 1 {
		t.Fatalf("%d runs after a look-up", n)
	}
	if after, _, _ := e.srv.ledger.Run(t.Context(), ref.ID); after.Revision != row.Revision {
		t.Fatalf("a look-up moved the revision: %d, was %d", after.Revision, row.Revision)
	}
	if _, err := h.RunByOp(t.Context(), "op 2"); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Fatalf("an op id of another shape: %v", err)
	}
	// A second server over the same data, its ledger read-only while the
	// first owns it: the look-up is answered.
	ro := restartEnding(t, e)
	if r, _ := ro.srv.ledger.ReadOnly(); !r {
		t.Fatal("the second server owns the ledger")
	}
	st, err := testHost(ro.srv, t.Context(), "example").RunByOp(t.Context(), "op-1")
	if err != nil || st.ID != ref.ID {
		t.Fatalf("RunByOp on a read-only ledger: %+v %v", st, err)
	}
	if _, err := testHost(ro.srv, t.Context(), "example").RunByOp(t.Context(), "op-2"); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("an unknown op id on a read-only ledger: %v", err)
	}
}

// rawLedger runs stmt on the server's ledger file through a connection of
// its own.
func rawLedger(t *testing.T, e *testEnv, stmt string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.srv.cfg.DataDir, "runs", ledger.File)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

// Run and StopRun answer only the provider's own runs: a person's run,
// another provider's run and a person's continuation of the provider's
// run are ext.ErrNotFound, as no run is, and none is stopped. A record
// edited by hand to name the provider grants nothing: the ledger decides.
func TestHostRunAndStopAnswerOnlyTheirOwn(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a person's launch: %d %v", resp.StatusCode, out)
	}
	persons := out["run"].(map[string]any)["id"].(string)
	theirs, err := testHost(e.srv, t.Context(), "elsewhere").Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: mine.ID, By: ada}); err != nil {
		t.Fatal(err)
	}
	if resp, out := e.do("POST", "/api/runs/"+mine.ID+"/resume", adminToken, nil); resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("a person's continuation: %d %v", resp.StatusCode, out)
	}
	mineRow, _, _ := e.srv.ledger.Run(t.Context(), mine.ID)
	cont := mineRow.ContinuedBy
	if cont == "" {
		t.Fatal("no continuation")
	}
	// The person's run's record says it is the provider's: it grants
	// nothing.
	if rec, ok, err := e.srv.records.Get(persons); err == nil && ok {
		rec.Origin = &crew.RunOrigin{Provider: "example", Ref: "example:42", Label: "x"}
		if err := e.srv.records.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	for _, other := range []string{persons, theirs.ID, cont} {
		if _, err := h.Run(t.Context(), other); !errors.Is(err, ext.ErrNotFound) {
			t.Fatalf("Run of %s: %v", other, err)
		}
		if err := h.StopRun(t.Context(), ext.StopRequest{Run: other, By: ada, Why: "mine?"}); !errors.Is(err, ext.ErrNotFound) {
			t.Fatalf("StopRun of %s: %v", other, err)
		}
		if run, ok := e.srv.runs.Get(other); !ok || run.StoppedAt != nil {
			t.Fatalf("%s after the provider's stop: %+v %v", other, run.StoppedAt, ok)
		}
	}
	if _, err := h.Run(t.Context(), "reviews-00000000"); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("Run of no run: %v", err)
	}
	if _, err := h.Run(t.Context(), "Reviews!"); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Fatalf("Run of an id of another shape: %v", err)
	}
	for _, id := range []string{persons, theirs.ID, cont} {
		_ = e.srv.runs.Stop(t.Context(), id)
	}
}

// StopRun of a run that is not running (stopped, finished, interrupted)
// answers ext.ErrNotRunning, its state and log as they were; Run answers
// the interrupted one after a restart.
func TestStopRunOfARunNotRunning(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	stopped, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: stopped.ID, By: ada, Why: "superseded"}); err != nil {
		t.Fatal(err)
	}
	run, _ := e.srv.runs.Get(stopped.ID)
	if run.State != crew.RunStopped || !runLogged(run, "stopped by Ada through example: superseded") {
		t.Fatalf("the stopped run: %s %+v", run.State, run.Log)
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: stopped.ID, By: ada}); !errors.Is(err, ext.ErrNotRunning) {
		t.Fatalf("a stop of a stopped run: %v", err)
	}
	if again, _ := e.srv.runs.Get(stopped.ID); len(again.Log) != len(run.Log) {
		t.Fatalf("the second stop logged: %+v", again.Log)
	}

	finished, err := h.Launch(t.Context(), originLaunch(id, "op-2"))
	if err != nil {
		t.Fatal(err)
	}
	sid := runningAttempt(t, e, finished.ID, "build", 1)["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, finished.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	if !waitFor(testWait, func() bool { r, _ := e.srv.runs.Get(finished.ID); return r.Outcome != "" }) {
		t.Fatal("the run has no outcome")
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: finished.ID, By: ada}); !errors.Is(err, ext.ErrNotRunning) {
		t.Fatalf("a stop of a finished run: %v", err)
	}
	if r, _ := e.srv.runs.Get(finished.ID); r.State != crew.RunFinished || r.StoppedAt != nil {
		t.Fatalf("the finished run after a stop: %s", r.State)
	}

	running, err := h.Launch(t.Context(), originLaunch(id, "op-3"))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, running.ID, "build", 1)
	crashServer(t, e, stopped.ID, finished.ID, running.ID)
	e2 := restartEnding(t, e)
	h2 := testHost(e2.srv, t.Context(), "example")
	st, err := h2.Run(t.Context(), running.ID)
	if err != nil || st.State != ext.RunInterrupted || st.Origin.OpID != "op-3" {
		t.Fatalf("Run of the interrupted run: %+v %v", st, err)
	}
	revision := func() int64 { r, _, _ := e2.srv.ledger.Run(t.Context(), running.ID); return r.Revision }
	was := revision()
	if err := h2.StopRun(t.Context(), ext.StopRequest{Run: running.ID, By: ada}); !errors.Is(err, ext.ErrNotRunning) {
		t.Fatalf("a stop of an interrupted run: %v", err)
	}
	if revision() != was {
		t.Fatal("the stop of an interrupted run wrote")
	}
	if st, err := h2.Run(t.Context(), finished.ID); err != nil || st.State != ext.RunFinished || st.Outcome != ext.OutcomeSucceeded {
		t.Fatalf("Run of the finished run after a restart: %+v %v", st, err)
	}
}

// runLogged reports whether the run's log has an entry saying msg.
func runLogged(r crew.Run, msg string) bool {
	for _, l := range r.Log {
		if l.Message == msg {
			return true
		}
	}
	return false
}

// A launch with an origin is of a crew with tasks: one without is refused
// not_managed, nothing launched, a held launch's too.
func TestLaunchNeedsAManagedCrew(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	id := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Plain", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{catMember("core", "immediately")},
	}, http.StatusCreated)["id"].(string)
	before := len(e.srv.runs.List())
	_, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), originLaunch(id, "op-1"))
	if ext.RefusalCode(err) != ext.RefusalNotManaged || !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a launch with an origin of a crew without tasks: %v", err)
	}
	if after := len(e.srv.runs.List()); after != before {
		t.Fatalf("%d runs, were %d", after, before)
	}
	// A held launch (a project, a digest, a start commit) is refused the
	// same, before its project or commit is looked at; a resolve still
	// answers Managed false.
	e.gitRepo(t)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": e.root})
	h := testHost(e.srv, t.Context(), "example")
	rl, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND"})
	if err != nil || rl.Managed {
		t.Fatalf("a resolve of a crew without tasks: %+v %v", rl, err)
	}
	head := gitIn(t, e.root, "rev-parse", "HEAD")
	for name, change := range map[string]func(*ext.LaunchRequest){
		"a project":          func(r *ext.LaunchRequest) { r.Project = "CND" },
		"a digest":           func(r *ext.LaunchRequest) { r.Project, r.ResolvedDigest = "CND", rl.Digest },
		"a start commit":     func(r *ext.LaunchRequest) { r.StartCommit = head },
		"an unknown project": func(r *ext.LaunchRequest) { r.Project = "ZZZ" },
	} {
		r := originLaunch(id, "op-held")
		change(&r)
		if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalNotManaged {
			t.Fatalf("a held launch with %s of a crew without tasks: %v", name, err)
		}
	}
	if after := len(e.srv.runs.List()); after != before {
		t.Fatalf("%d runs after the held launches, were %d", after, before)
	}
}

// The earlier run a launch names must be one this provider launched: a
// person's run, another provider's run, a person's continuation of the
// provider's run and an id no run has are each prior_unknown, in the same
// words.
func TestPriorMustBeTheProvidersOwnRun(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a person's launch: %d %v", resp.StatusCode, out)
	}
	persons := out["run"].(map[string]any)["id"].(string)
	theirs, err := testHost(e.srv, t.Context(), "elsewhere").Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	mine, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: mine.ID, By: ada}); err != nil {
		t.Fatal(err)
	}
	if resp, out := e.do("POST", "/api/runs/"+mine.ID+"/resume", adminToken, nil); resp.StatusCode >= 300 {
		t.Fatalf("a person's continuation: %d %v", resp.StatusCode, out)
	}
	row, _, _ := e.srv.ledger.Run(t.Context(), mine.ID)
	var words string
	for i, prior := range []string{persons, theirs.ID, row.ContinuedBy, "reviews-00000000"} {
		r := originLaunch(id, "op-p"+string(rune('a'+i)))
		r.Subject.Prior = prior
		_, err := h.Launch(t.Context(), r)
		if ext.RefusalCode(err) != ext.RefusalPriorUnknown {
			t.Fatalf("prior %s: %v", prior, err)
		}
		if words == "" {
			words = err.Error()
		}
		if err.Error() != words {
			t.Fatalf("prior %s is refused in other words: %q, not %q", prior, err, words)
		}
	}
	// The provider's own run is taken.
	r := originLaunch(id, "op-own")
	r.Subject.Prior = mine.ID
	if _, err := h.Launch(t.Context(), r); err != nil {
		t.Fatalf("the provider's own earlier run: %v", err)
	}
	for _, run := range e.srv.runs.List() {
		_ = e.srv.runs.Stop(t.Context(), run.ID)
	}
}

// A subject's reference in no form the server knows is invalid_subject;
// a Brakeman reference and a change: reference are taken.
func TestASubjectInNoKnownFormIsRefused(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	r := originLaunch(id, "op-1")
	r.Subject.Ref = "nothing:42"
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalInvalidSubject {
		t.Fatalf("a subject in no known form: %v", err)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs after a refusal", n)
	}
	r.Subject.Ref = "session:0123456789abcdef"
	ref, err := h.Launch(t.Context(), r)
	if err != nil {
		t.Fatalf("a Brakeman reference: %v", err)
	}
	_ = e.srv.runs.Stop(t.Context(), ref.ID)
}

// The desk's Host opens none of a provider's doors: Run, RunByOp, StopRun
// and a launch with an origin are ext.ErrUnsupported.
func TestTheDesksHostOpensNoProviderDoor(t *testing.T) {
	e, id := originEnv(t)
	desk := &extHost{s: e.srv, name: "brakeman", ctx: t.Context(), desk: true}
	if _, err := desk.Launch(t.Context(), originLaunch(id, "op-1")); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's launch with an origin: %v", err)
	}
	if _, err := desk.Run(t.Context(), "reviews-00000000"); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's Run: %v", err)
	}
	if _, err := desk.RunByOp(t.Context(), "op-1"); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's RunByOp: %v", err)
	}
	if err := desk.StopRun(t.Context(), ext.StopRequest{Run: "reviews-00000000", By: ada}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's StopRun: %v", err)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs", n)
	}
}

// A continuation of a run whose launch's time limit has passed is refused
// 409 time_limit_passed, nothing made.
func TestAContinuationPastItsTimeLimitIsRefused(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	ref, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StopRun(t.Context(), ext.StopRequest{Run: ref.ID, By: ada}); err != nil {
		t.Fatal(err)
	}
	// The instant moved into the past, as an hour gone by would leave it.
	rawLedger(t, e, "UPDATE run_origin SET deadline = ? WHERE run_id = ?", time.Now().UTC().Add(-time.Minute).Truncate(time.Second).Format(time.RFC3339Nano), ref.ID)
	n := ledgerRuns(t, e)
	resp, out := e.do("POST", "/api/runs/"+ref.ID+"/resume", adminToken, nil)
	if code, msg := apiErr(out); resp.StatusCode != http.StatusConflict || code != "time_limit_passed" || !strings.Contains(msg, "time limit has passed") {
		t.Fatalf("a continuation past the time limit: %d %v", resp.StatusCode, out)
	}
	if after := ledgerRuns(t, e); after != n {
		t.Fatalf("%d runs, were %d", after, n)
	}
}

// Nothing of a run's origin is handed to a continuity store: the run's
// record as the store is given it has no origin.
func TestARunsOriginStaysOnTheServer(t *testing.T) {
	rec := crew.Run{ID: "reviews-0000000a", CrewID: "reviews", Origin: &crew.RunOrigin{Provider: "example", Ref: "example:42", Label: "Review",
		Subject: "change:x", SubjectURL: "https://git.example.com/acme/app/pull/42", Deadline: time.Now().UTC().Truncate(time.Second), LimitSeconds: 3600}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"origin"`) {
		t.Fatal("the record carries no origin")
	}
	out := publicRunDoc(b)
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["origin"]; ok || strings.Contains(string(out), "git.example.com") || strings.Contains(string(out), "example:42") {
		t.Fatalf("the store is given the origin: %s", out)
	}
	if doc["id"] != rec.ID {
		t.Fatalf("the store's run: %s", out)
	}
}

// A call whose try was given up (its context ended) is answered
// context.Canceled by every door, never ledger_unavailable, and the
// server logs no ledger failure for it.
func TestAGivenUpCallIsNoLedgerFailure(t *testing.T) {
	logs := &syncBuffer{}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(logs, nil)), []catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}})
	e.gitRepo(t)
	endRunsAtCleanup(t, e)
	id := originCrew(t, e, "Reviews")
	h := testHost(e.srv, t.Context(), "example")
	ref, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.Run(ended, ref.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run of a given-up call: %v", err)
	}
	if _, err := h.RunByOp(ended, "op-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunByOp of a given-up call: %v", err)
	}
	if err := h.StopRun(ended, ext.StopRequest{Run: ref.ID, By: ada}); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopRun of a given-up call: %v", err)
	}
	if _, err := h.Launch(ended, originLaunch(id, "op-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("a retry of a given-up call: %v", err)
	}
	if strings.Contains(logs.String(), "the ledger could not be read") {
		t.Fatalf("a given-up call was logged as a ledger failure:\n%s", logs.String())
	}
	if run, _ := e.srv.runs.Get(ref.ID); run.StoppedAt != nil {
		t.Fatal("a given-up stop stopped the run")
	}
}

// On a server whose ledger is read-only (another server owns it and runs
// the run), StopRun answers ledger_unavailable, never that the run is not
// running, and stops nothing.
func TestStopRunOnAReadOnlyLedgerSaysSo(t *testing.T) {
	e, id := originEnv(t)
	h := testHost(e.srv, t.Context(), "example")
	ref, err := h.Launch(t.Context(), originLaunch(id, "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, ref.ID, "build", 1)
	ro := restartEnding(t, e)
	if r, _ := ro.srv.ledger.ReadOnly(); !r {
		t.Fatal("the second server owns the ledger")
	}
	err = testHost(ro.srv, t.Context(), "example").StopRun(t.Context(), ext.StopRequest{Run: ref.ID, By: ada})
	if ext.RefusalCode(err) != "ledger_unavailable" || errors.Is(err, ext.ErrNotRunning) {
		t.Fatalf("StopRun on a read-only ledger: %v", err)
	}
	if run, _ := e.srv.runs.Get(ref.ID); run.StoppedAt != nil {
		t.Fatal("the run was stopped")
	}
}
