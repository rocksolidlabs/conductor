package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A launch from a ticket (docs/plans/dispatch-evidence.md, PR-7): the
// route in its order, the reconcile at the ledger's recovery, the
// package's files in every attempt from Dispatch's store, and the agent
// selection the revision approved at every attempt. The tests wait on the
// mechanism: a launch held at the seam before the engine is asked for its
// run (beforePlanned), an attempt held before its session takes the
// catalog entry (attempt), the reconcile held before it reads the pending
// launches (beforeReconcile), and the ledger's and the store's rows.
// Each runs git under a HOME of its own (planRepo), so none is parallel.

// readyCat is a cat that prints a line first, so an attempt's session is
// ready for its task line at once.
var readyCat = catalog.Agent{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}

// launchTest is a planTest whose ticket CND-1 has an approved plan
// revision of a saved managed crew of one member on ready-cat (shaped by
// the test), every run the engine holds stopped when the test ends.
type launchTest struct {
	*planTest
	crew     crew.Crew
	plan     string
	ticketID string
}

// newLaunchTest makes a launchTest; mutate shapes the config, shape the
// crew, and agents join the catalog.
func newLaunchTest(t *testing.T, mutate func(*config.Config), shape func(*crew.Crew), agents ...catalog.Agent) *launchTest {
	t.Helper()
	p := newPlanTest(t, mutate, append([]catalog.Agent{readyCat}, agents...)...)
	c := teamCrew("Team", "ready-cat")
	if shape != nil {
		shape(&c)
	}
	lt := &launchTest{planTest: p, crew: p.saveCrew(t, c)}
	lt.plan = lt.approved(t, nil)
	lt.ticketID = entityOf(t, p.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")["id"].(string)
	srv := p.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	return lt
}

// stopEveryRun stops every run srv's engine holds.
func stopEveryRun(srv *Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range srv.runs.List() {
		_ = srv.runs.Stop(ctx, r.ID)
	}
}

// approved proposes the crew for CND-1 with the fields and approves the
// revision, returning its id.
func (l *launchTest) approved(t *testing.T, fields map[string]any) string {
	t.Helper()
	id := l.proposed(t, l.crew.ID, fields)
	l.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	return id
}

// launch sends a launch of CND-1, the fixture's revision and a new op id
// unless the body names them, and fails t unless it answers the status
// and code.
func (l *launchTest) launch(t *testing.T, e *testEnv, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["opId"]; !ok {
		body["opId"] = opID()
	}
	if _, ok := body["plan"]; !ok {
		body["plan"] = l.plan
	}
	return e.expect(t, "POST", "/api/dispatch/tickets/CND-1/launch", body, status, code)
}

// launches is CND-1's launches as the store has them.
func (l *launchTest) launches(t *testing.T) []dispatch.LaunchSummary {
	t.Helper()
	out, err := l.srv.dispatch.Launches(context.Background(), "CND-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// launchOf is the launch with the id as the store has it, whole.
func (l *launchTest) launchOf(t *testing.T, id string) dispatch.LaunchView {
	t.Helper()
	v, err := l.srv.dispatch.LaunchByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// submit submits the attempt's result with its session's own token.
func (l *launchTest) submit(t *testing.T, sid string) {
	t.Helper()
	if resp, out := l.do("POST", "/api/sessions/"+sid+"/task/result", l.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
}

// waitUntil polls cond until it holds, failing t after testWait (a hang,
// never a timing).
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(testWait)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("%s: not after %s", what, testWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// treeFile is a file of the attempt's worktree's .conductor directory.
func (l *launchTest) treeFile(t *testing.T, run, task string, n int, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.repo, ".conductor", "worktrees", run, task+"."+strconv.Itoa(n), ".conductor", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ledgerAttempt is the attempt's row in srv's ledger once it is in the
// state.
func ledgerAttemptIn(t *testing.T, srv *Server, run, task string, n int, state string) ledger.AttemptRow {
	t.Helper()
	var got ledger.AttemptRow
	waitUntil(t, "attempt "+task+"."+strconv.Itoa(n)+" "+state, func() bool {
		rows, err := srv.ledger.Attempts(context.Background(), run)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.TaskID == task && (n == 0 || r.N == n) && r.State == state {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

// runOf is GET /api/runs/{run}'s run.
func runOf(t *testing.T, e *testEnv, run string) map[string]any {
	t.Helper()
	return e.expect(t, "GET", "/api/runs/"+run, nil, http.StatusOK, "")["run"].(map[string]any)
}

// command sends a run's command with a new command id.
func command(t *testing.T, e *testEnv, run, path string) {
	t.Helper()
	e.expect(t, "POST", "/api/runs/"+run+"/"+path, map[string]any{"commandId": opID()}, http.StatusOK, "")
}

// A launch from a ticket's approved revision: 201 with the launch started
// and its agents, the run named after the ticket and carrying its package
// (the run's ticket, the ledger row's ticket and package, its digest the
// launch's), the attempt handed ticket.md and package.json byte for byte;
// the launch read whole.
func TestLaunchFromATicket(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	out := l.launch(t, l.testEnv, map[string]any{"byName": "Nate"}, http.StatusCreated, "")
	launch := entityOf(t, out, "launch")
	run := entityOf(t, out, "run")
	runID := launch["runId"].(string)
	if launch["state"] != dispatch.LaunchStarted || run["id"] != runID || run["label"] != "CND-1 Fix the login loop" || receiptOf(t, out)["id"] != launch["id"] {
		t.Fatalf("the launch: %v\nthe run: %v", launch, run)
	}
	if agents, _ := launch["agents"].([]any); len(agents) != 1 || agents[0].(map[string]any)["member"] != "lead" || agents[0].(map[string]any)["agent"] != "ready-cat" {
		t.Fatalf("the launch's agents: %v", launch["agents"])
	}
	v := l.launchOf(t, launch["id"].(string))
	tk, _ := run["ticket"].(map[string]any)
	if tk == nil || tk["id"] != l.ticketID || tk["key"] != "CND-1" || tk["plan"] != l.plan || tk["planN"] != 1.0 || tk["launch"] != v.ID || tk["digest"] != v.PackageSHA256 {
		t.Fatalf("the run's ticket: %v", run["ticket"])
	}
	row, ok, err := l.srv.ledger.Run(t.Context(), runID)
	if err != nil || !ok || row.Ticket != l.ticketID || row.Package != string(v.Package) || shaHex([]byte(row.Package)) != v.PackageSHA256 || row.Base != l.head {
		t.Fatalf("the ledger's row: %+v %v %v", row, ok, err)
	}
	runningAttempt(t, l.testEnv, runID, "build", 1)
	if md := l.treeFile(t, runID, "build", 1, "ticket.md"); !bytes.HasPrefix(md, []byte("# CND-1: Fix the login loop\n")) {
		t.Fatalf("ticket.md: %q", md)
	}
	if pkg := l.treeFile(t, runID, "build", 1, "package.json"); !bytes.Equal(pkg, v.Package) {
		t.Fatalf("package.json is not the launch's package:\n%s\n%s", pkg, v.Package)
	}
	// The read: the summary and the package.
	read := entityOf(t, l.expect(t, "GET", "/api/dispatch/launches/"+v.ID, nil, http.StatusOK, ""), "launch")
	if read["id"] != v.ID || read["state"] != dispatch.LaunchStarted || read["package"] == nil {
		t.Fatalf("the launch's read: %v", read)
	}
	l.expect(t, "GET", "/api/dispatch/launches/0123456789abcdef", nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", "/api/dispatch/launches/nope", nil, http.StatusBadRequest, "invalid_request")
	l.expect(t, "GET", "/api/dispatch/launches/"+v.ID+"?x=1", nil, http.StatusBadRequest, "invalid_request")
	// What the route refuses before the store sees it.
	l.launch(t, l.testEnv, map[string]any{"plan": "nope"}, http.StatusBadRequest, "invalid_request")
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/launch", map[string]any{"opId": opID(), "plan": l.plan, "nope": 1}, http.StatusBadRequest, "invalid_request")
	// The store's refusals keep their receipts: a revision missing.
	if out := l.launch(t, l.testEnv, map[string]any{"plan": "0123456789abcdef"}, http.StatusNotFound, "not_found"); out["receipt"] == nil {
		t.Fatalf("a revision missing: %v", out)
	}
}

// The same opId sent while the first launch is held before the engine is
// asked for its run waits for it and answers the same run; sent after, it
// answers the same again: one launch, one run.
func TestLaunchResentMakesNoSecondRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	held, release := make(chan string, 1), make(chan struct{})
	l.srv.launches.setSeams(func(g *launchGate) {
		g.beforePlanned = func(id string) {
			held <- id
			<-release
		}
	})
	type answer struct {
		status int
		out    map[string]any
	}
	answers := make(chan answer, 2)
	send := func() {
		status, _, out := l.call(t, "POST", "/api/dispatch/tickets/CND-1/launch", map[string]any{"opId": "once", "plan": l.plan})
		answers <- answer{status, out}
	}
	go send()
	id := <-held
	go send()
	waitUntil(t, "the resend waits for the first", func() bool {
		l.srv.launches.mu.Lock()
		defer l.srv.launches.mu.Unlock()
		return l.srv.launches.waits == 1
	})
	close(release)
	first, second := <-answers, <-answers
	var runs []string
	for _, a := range []answer{first, second} {
		if a.status != http.StatusCreated && a.status != http.StatusOK {
			t.Fatalf("a launch: %d %v", a.status, a.out)
		}
		launch := entityOf(t, a.out, "launch")
		if launch["id"] != id || launch["state"] != dispatch.LaunchStarted {
			t.Fatalf("a launch: %v", launch)
		}
		runs = append(runs, launch["runId"].(string))
	}
	if runs[0] != runs[1] || first.status == second.status {
		t.Fatalf("two answers: %d %v, %d %v", first.status, runs[0], second.status, runs[1])
	}
	again := l.launch(t, l.testEnv, map[string]any{"opId": "once"}, http.StatusOK, "")
	if receiptOf(t, again)["repeat"] != true || entityOf(t, again, "launch")["runId"] != runs[0] {
		t.Fatalf("after: %v", again)
	}
	if n := len(l.launches(t)); n != 1 {
		t.Fatalf("%d launches", n)
	}
	rows, err := l.srv.ledger.Runs(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != runs[0] {
		t.Fatalf("the ledger's runs: %+v %v", rows, err)
	}
	if n := len(l.srv.runs.List()); n != 1 {
		t.Fatalf("the engine holds %d runs", n)
	}
}

// A settling whose read of the launch inside the hold fails (its context
// ended) releases the hold by the launch's id: nothing is left in flight,
// so the same opId sent again settles the launch, started, without
// waiting on a settling that is over.
func TestASettlingThatCannotReadReleasesItsLaunch(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	op := adminOp("")
	v := l.pendingLaunch(t, op)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, release, pending, err := l.srv.settleLaunch(ctx, l.srv.dispatch, v, "")
	release()
	if !errors.Is(err, context.Canceled) || pending != nil {
		t.Fatalf("the settling: %v %v", pending, err)
	}
	l.srv.launches.mu.Lock()
	left := maps.Clone(l.srv.launches.inFlight)
	l.srv.launches.mu.Unlock()
	if len(left) != 0 {
		t.Fatalf("in flight after the settling: %v", left)
	}
	if got := l.launchOf(t, v.ID); got.State != dispatch.LaunchPending {
		t.Fatalf("the launch: %+v", got.LaunchSummary)
	}
	out := l.launch(t, l.testEnv, map[string]any{"opId": op.ID}, http.StatusOK, "")
	if launch := entityOf(t, out, "launch"); launch["id"] != v.ID || launch["state"] != dispatch.LaunchStarted || launch["runId"] != v.RunID {
		t.Fatalf("the resend: %v", out)
	}
	l.srv.launches.mu.Lock()
	waits := l.srv.launches.waits
	l.srv.launches.mu.Unlock()
	if waits != 0 {
		t.Fatalf("the resend waited %d times", waits)
	}
}

// A launch whose run cannot be started (an agent not installed) is kept,
// failed with why: 409 launch_failed, and the same opId again answers the
// same; no run.
func TestLaunchFailedIsKept(t *testing.T) {
	ghost := catalog.Agent{ID: "ghost", Name: "ghost", Command: []string{"/nonexistent/ghost-agent"}}
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Members[0].AgentID = "ghost" }, ghost)
	for i := range 2 {
		out := l.launch(t, l.testEnv, map[string]any{"opId": "doomed"}, http.StatusConflict, "launch_failed")
		launch := entityOf(t, out, "launch")
		if launch["state"] != dispatch.LaunchFailed || !strings.Contains(launch["why"].(string), "not installed") || out["error"].(map[string]any)["message"] != launch["why"] ||
			(i == 1) != (receiptOf(t, out)["repeat"] == true) {
			t.Fatalf("try %d: %v", i, out)
		}
	}
	if n := len(l.launches(t)); n != 1 {
		t.Fatalf("%d launches", n)
	}
	if rows, err := l.srv.ledger.Runs(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("the ledger's runs: %+v %v", rows, err)
	}
}

// A launch this server may not advance keeps no pending row: managed runs
// off (409 managed_off), no ledger, and a ledger another server owns (503
// ledger_unavailable).
func TestLaunchGates(t *testing.T) {
	off := false
	cases := []struct {
		name   string
		mutate func(*config.Config)
		status int
		code   string
	}{
		{"managed runs off", func(cfg *config.Config) { cfg.Crews.Managed = &off }, http.StatusConflict, "managed_off"},
		{"no ledger", func(cfg *config.Config) {
			runs := filepath.Join(cfg.DataDir, "runs")
			if err := os.MkdirAll(runs, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(runs, "elsewhere.sqlite"), filepath.Join(runs, ledger.File)); err != nil {
				t.Fatal(err)
			}
		}, http.StatusServiceUnavailable, "ledger_unavailable"},
		{"a read-only ledger", func(cfg *config.Config) {
			other, err := ledger.Open(filepath.Join(cfg.DataDir, "runs"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
		}, http.StatusServiceUnavailable, "ledger_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaunchTest(t, tc.mutate, nil)
			out := l.launch(t, l.testEnv, nil, tc.status, tc.code)
			if out["receipt"] != nil {
				t.Fatalf("a receipt kept: %v", out)
			}
			if n := len(l.launches(t)); n != 0 {
				t.Fatalf("%d launches", n)
			}
		})
	}
}

// pendingLaunch makes a launch of the fixture's revision through the
// store alone, pending as a server that ended before its run left it.
func (l *launchTest) pendingLaunch(t *testing.T, op dispatch.Op) dispatch.LaunchView {
	t.Helper()
	rc := doneOf(t)(l.srv.dispatch.Launch(context.Background(), op, "CND-1", l.plan, ""))
	return l.launchOf(t, rc.ID)
}

// putRunRow writes a run's first row into ld as the engine's first
// transaction does.
func (l *launchTest) putRunRow(t *testing.T, ld *ledger.Ledger, id, ticket, pkg, state string) {
	t.Helper()
	crewJSON, _ := json.Marshal(l.crew)
	if _, err := ld.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		return tx.CreateRun(ledger.RunRow{ID: id, CrewID: l.crew.ID, Crew: string(crewJSON), Base: l.head, State: state, Ticket: ticket, Package: pkg, CreatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
}

// The reconcile settles every pending launch by the ledger: its own run
// there, started; none, failed; another run's row of the id, failed and
// that row untouched; with no ledger, still pending.
func TestReconcileLaunches(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	ctx := t.Context()
	own, none, other, unread := l.pendingLaunch(t, adminOp("")), l.pendingLaunch(t, adminOp("")), l.pendingLaunch(t, adminOp("")), l.pendingLaunch(t, adminOp(""))
	l.putRunRow(t, l.srv.ledger, own.RunID, l.ticketID, string(own.Package), ledger.RunInterrupted)
	l.putRunRow(t, l.srv.ledger, other.RunID, "", "", ledger.RunFinished)
	before, _, _ := l.srv.ledger.Run(ctx, other.RunID)
	l.srv.reconcileLaunches(ctx, nil)
	if got := l.launchOf(t, unread.ID); got.State != dispatch.LaunchPending {
		t.Fatalf("with no ledger: %+v", got.LaunchSummary)
	}
	// unread's run reads as none from here on: it is reconciled with the rest.
	l.srv.reconcileLaunches(ctx, l.srv.ledger)
	for _, tc := range []struct {
		v     dispatch.LaunchView
		state string
		why   string
	}{
		{own, dispatch.LaunchStarted, ""},
		{none, dispatch.LaunchFailed, "the server stopped before the run started"},
		{other, dispatch.LaunchFailed, "the run id is another run's"},
		{unread, dispatch.LaunchFailed, "the server stopped before the run started"},
	} {
		if got := l.launchOf(t, tc.v.ID); got.State != tc.state || got.Why != tc.why {
			t.Fatalf("launch of run %s: %+v, want %s %q", tc.v.RunID, got.LaunchSummary, tc.state, tc.why)
		}
	}
	if got := l.launchOf(t, own.ID); len(got.Agents) != 1 || got.Agents[0].Member != "lead" || got.Agents[0].Agent != "ready-cat" {
		t.Fatalf("the adopted launch's agents: %+v", got.Agents)
	}
	if after, _, _ := l.srv.ledger.Run(ctx, other.RunID); !reflect.DeepEqual(before, after) {
		t.Fatalf("another run's row changed:\n%+v\n%+v", before, after)
	}
	// The reconcile starts nothing.
	if n := len(l.srv.runs.List()); n != 0 {
		t.Fatalf("the reconcile started %d runs", n)
	}
}

// lateLedger holds the owner lock of the data directory's ledger before
// the server opens it, so the server takes it late; the server waits for
// it far longer than the test takes (LockWait only bounds a hang).
func lateLedger(t *testing.T, other **ledger.Ledger) func(*config.Config) {
	was := ledger.LockWait
	ledger.LockWait = 5 * time.Minute
	t.Cleanup(func() { ledger.LockWait = was })
	return func(cfg *config.Config) {
		ld, err := ledger.Open(filepath.Join(cfg.DataDir, "runs"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ld.Close() })
		*other = ld
	}
}

// A ledger whose lock is taken late: the one OnOwned callback runs the
// recovery and then the reconcile, once each, in that order: the launch
// whose run the last owner left running is recorded started after its row
// was concluded.
func TestReconcileRunsAfterRecoveryInTheOneCallback(t *testing.T) {
	var other *ledger.Ledger
	l := newLaunchTest(t, lateLedger(t, &other), nil)
	if ro, _ := l.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server took the owner lock another ledger holds")
	}
	v := l.pendingLaunch(t, adminOp(""))
	l.putRunRow(t, other, v.RunID, l.ticketID, string(v.Package), ledger.RunRunning)
	var mu sync.Mutex
	var calls []string
	l.srv.launches.setSeams(func(g *launchGate) {
		g.beforeReconcile = func() {
			row, _, err := l.srv.ledger.Run(context.Background(), v.RunID)
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, row.State+" "+errString(err))
		}
	})
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the launch settled", func() bool { return l.launchOf(t, v.ID).State == dispatch.LaunchStarted })
	waitUntil(t, "launches admitted", l.srv.launches.ready.Load)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != ledger.RunInterrupted+" " {
		t.Fatalf("the reconcile ran %v (the run's row as it saw it)", calls)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// While the server takes the ledger over (the lock taken, the reconcile
// not finished), a launch is 503 ledger_unavailable with no pending row;
// once it has finished, the launch is 201 and stays started.
func TestNoLaunchWhileTheLedgerIsTakenOver(t *testing.T) {
	var other *ledger.Ledger
	l := newLaunchTest(t, lateLedger(t, &other), nil)
	reached, release := make(chan struct{}), make(chan struct{})
	l.srv.launches.setSeams(func(g *launchGate) {
		g.beforeReconcile = func() {
			close(reached)
			<-release
		}
	})
	l.launch(t, l.testEnv, nil, http.StatusServiceUnavailable, "ledger_unavailable")
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	<-reached
	if ro, why := l.srv.ledger.ReadOnly(); ro {
		t.Fatalf("the ledger is read-only at the reconcile: %s", why)
	}
	out := l.launch(t, l.testEnv, nil, http.StatusServiceUnavailable, "ledger_unavailable")
	if msg := out["error"].(map[string]any)["message"]; msg != "the server is taking over the ledger" {
		t.Fatalf("the refusal: %v", out)
	}
	if n := len(l.launches(t)); n != 0 {
		t.Fatalf("%d launches while the ledger was taken over", n)
	}
	close(release)
	waitUntil(t, "launches admitted", l.srv.launches.ready.Load)
	launch := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")
	if got := l.launchOf(t, launch["id"].(string)); got.State != dispatch.LaunchStarted {
		t.Fatalf("the launch: %+v", got.LaunchSummary)
	}
}

// Two servers on one data directory: A commits a pending launch and is
// held before its run; the same opId sent to B, whose ledger is read-only
// or whose managed runs are off, answers it pending with 503
// ledger_unavailable and B's reason, and records nothing; A released, its
// run starts and the launch is started.
func TestAReplayOnTheServerWithoutTheLedger(t *testing.T) {
	for _, tc := range []struct {
		name, why string
		second    func(t *testing.T, l *launchTest) *testEnv
	}{
		{"read-only ledger", "", func(t *testing.T, l *launchTest) *testEnv {
			b := l.restart()
			if ro, _ := b.srv.ledger.ReadOnly(); !ro {
				t.Fatal("the second server owns the ledger")
			}
			return b
		}},
		{"managed runs off", "crews with tasks are off on this server (crews.managed)", func(t *testing.T, l *launchTest) *testEnv {
			cfg := *l.srv.cfg
			off := false
			cfg.Crews.Managed = &off
			srv, err := New(&cfg, l.srv.base, l.srv.log, nil, l.srv.store)
			if err != nil {
				t.Fatal(err)
			}
			return l.serve(srv)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLaunchTest(t, nil, nil)
			b := tc.second(t, l)
			held, release := make(chan string, 1), make(chan struct{})
			var released sync.Once
			let := func() { released.Do(func() { close(release) }) }
			// A failure before A is released still lets it go, so the
			// test ends instead of hanging on A's run.
			t.Cleanup(let)
			l.srv.launches.setSeams(func(g *launchGate) {
				g.beforePlanned = func(id string) {
					held <- id
					<-release
				}
			})
			type answer struct {
				status int
				out    map[string]any
			}
			answers := make(chan answer, 1)
			go func() {
				status, _, out := l.call(t, "POST", "/api/dispatch/tickets/CND-1/launch", map[string]any{"opId": "shared", "plan": l.plan})
				answers <- answer{status, out}
			}()
			id := <-held
			out := l.launch(t, b, map[string]any{"opId": "shared"}, http.StatusServiceUnavailable, "ledger_unavailable")
			if launch := entityOf(t, out, "launch"); launch["id"] != id || launch["state"] != dispatch.LaunchPending || receiptOf(t, out)["repeat"] != true {
				t.Fatalf("B's answer: %v", out)
			}
			if msg := out["error"].(map[string]any)["message"]; tc.why != "" && msg != tc.why {
				t.Fatalf("B's reason: %v", out)
			}
			if got := l.launchOf(t, id); got.State != dispatch.LaunchPending {
				t.Fatalf("B recorded the launch: %+v", got.LaunchSummary)
			}
			let()
			a := <-answers
			if a.status != http.StatusCreated || entityOf(t, a.out, "launch")["state"] != dispatch.LaunchStarted {
				t.Fatalf("A's answer: %d %v", a.status, a.out)
			}
		})
	}
}

// A pending launch whose run id the ledger has under another package is
// recorded failed with its words, that row untouched: no launch adopts
// another's run.
func TestLaunchAdoptsOnlyItsOwnRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	op := adminOp("")
	v := l.pendingLaunch(t, op)
	l.putRunRow(t, l.srv.ledger, v.RunID, l.ticketID, `{"another":"package"}`, ledger.RunFinished)
	before, _, _ := l.srv.ledger.Run(t.Context(), v.RunID)
	out := l.launch(t, l.testEnv, map[string]any{"opId": op.ID}, http.StatusConflict, "launch_failed")
	if launch := entityOf(t, out, "launch"); launch["id"] != v.ID || launch["why"] != "the run id is another run's" {
		t.Fatalf("the launch: %v", out)
	}
	if after, _, _ := l.srv.ledger.Run(t.Context(), v.RunID); !reflect.DeepEqual(before, after) {
		t.Fatalf("the other run's row changed:\n%+v\n%+v", before, after)
	}
	if n := len(l.srv.runs.List()); n != 0 {
		t.Fatalf("the engine holds %d runs", n)
	}
}

// staleLaunch fails t unless a launch of the revision is 409 stale_plan
// with the words, keeping no launch.
func (l *launchTest) staleLaunch(t *testing.T, plan, words string) {
	t.Helper()
	out := l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusConflict, dispatch.RefusalStalePlan)
	if msg := out["error"].(map[string]any)["message"]; msg != words || out["receipt"] != nil {
		t.Fatalf("the refusal: %v, want %q", out, words)
	}
	if n := len(l.launches(t)); n != 0 {
		t.Fatalf("%d launches", n)
	}
}

// boldEntry is the catalog entry of an agent with an env, a session
// recipe and a yolo recipe, each part as the edit gives it.
func boldEntry(edit func(map[string]any)) map[string]any {
	b := map[string]any{"id": "bold", "name": "bold", "command": []string{"/bin/sh", "-c", "echo ready; exec cat", "sh", "--model", "sonnet"},
		"env":     map[string]string{"AGENT_VAR": "one"},
		"session": map[string]any{"startArgs": []string{"--session-id", "{id}"}, "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]{36}$"},
		"yolo":    map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO_MODE": "auto"}}}
	if edit != nil {
		edit(b)
	}
	return b
}

// An agent whose catalog entry was edited after the approval (its command,
// its env) refuses the launch 409 stale_plan naming it, before anything
// is kept.
func TestLaunchRefusedWhenAnAgentChanged(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Members[0].AgentID = "bold" }, catalog.Agent{ID: "bold", Name: "bold", Command: []string{"/bin/true"}})
	l.save(boldEntry(nil))
	plan := l.approved(t, nil)
	for _, tc := range []struct {
		edit  func(map[string]any)
		words string
	}{
		{func(b map[string]any) {
			b["command"] = []string{"/bin/sh", "-c", "echo ready; exec cat", "sh", "--model", "opus"}
		}, "the agent bold's command changed"},
		{func(b map[string]any) { b["env"] = map[string]string{"AGENT_VAR": "two"} }, "the agent bold's environment changed"},
	} {
		l.save(boldEntry(tc.edit))
		l.staleLaunch(t, plan, tc.words+" since revision 2 was approved: revise it")
	}
	l.save(boldEntry(nil))
	l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, "")
}

// For a revision with yolo on, the yolo recipe's arguments (a --model
// changed), its env and the session recipe's start arguments, each edited
// after the approval, refuse the launch naming the agent and the part; a
// yolo recipe edited is no change for a revision with yolo off, whose
// launch is taken.
func TestLaunchRefusedWhenARecipeChanged(t *testing.T) {
	on, off := true, false
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Members[0].AgentID = "bold"; c.Yolo = &on }, catalog.Agent{ID: "bold", Name: "bold", Command: []string{"/bin/true"}})
	l.save(boldEntry(nil))
	withYolo := l.approved(t, nil)
	for _, tc := range []struct {
		edit  func(map[string]any)
		words string
	}{
		{func(b map[string]any) {
			b["yolo"] = map[string]any{"args": []string{"--yes", "--model", "opus"}, "env": map[string]string{"YOLO_MODE": "auto"}}
		},
			"the agent bold's yolo recipe's arguments changed"},
		{func(b map[string]any) {
			b["yolo"] = map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO_MODE": "manual"}}
		},
			"the agent bold's environment changed"},
		{func(b map[string]any) {
			b["session"] = map[string]any{"startArgs": []string{"--session", "{id}"}, "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]{36}$"}
		}, "the agent bold's session recipe's start arguments changed"},
	} {
		l.save(boldEntry(tc.edit))
		l.staleLaunch(t, withYolo, tc.words+" since revision 2 was approved: revise it")
	}
	// Yolo off: the crew saved so, a revision of it approved, then the
	// yolo recipe edited.
	l.save(boldEntry(nil))
	c := l.crew
	c.Yolo = &off
	saved, err := l.srv.crews.Update(c.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	l.crew = saved
	withoutYolo := l.approved(t, nil)
	l.save(boldEntry(func(b map[string]any) { b["yolo"] = map[string]any{"args": []string{"--no"}} }))
	l.launch(t, l.testEnv, map[string]any{"plan": withoutYolo}, http.StatusCreated, "")
}

// holdAttempts holds every attempt's session before it takes the catalog
// entry, telling reached of each, until release is closed.
func holdAttempts(srv *Server) (reached chan crew.LaunchSpec, release chan struct{}) {
	reached, release = make(chan crew.LaunchSpec, 8), make(chan struct{})
	srv.launches.setSeams(func(g *launchGate) {
		g.attempt = func(spec crew.LaunchSpec) {
			reached <- spec
			<-release
		}
	})
	return reached, release
}

// waitPaused waits until the run is paused with words holding want.
func waitPaused(t *testing.T, e *testEnv, run, want string) {
	t.Helper()
	waitUntil(t, "run "+run+" paused", func() bool {
		r := runOf(t, e, run)
		managed, _ := r["managed"].(map[string]any)
		why, _ := managed["pausedWhy"].(string)
		if why == "" {
			why, _ = r["pausedWhy"].(string)
		}
		return r["paused"] == true && strings.Contains(why, want)
	})
}

// Every attempt of a run launched from a ticket starts only the agent its
// revision approved, through New's own wiring: the entry's command edited
// after the launch, with the first attempt held before its session takes
// the entry, fails that attempt at agent, uncounted, with no session, and
// pauses the run with the words; the entry restored and the run unpaused,
// the attempt starts. The same for a retry, for the saved entry deleted
// (the configured one it replaced back, another command that takes no
// extra arguments, while the member has some), for the entry hidden (no
// longer in the catalog at all), and for a continuation after
// a restart with the entry edited before its first attempt; a crew
// launched from the Crews page, with no package, launches on the edited
// entry as before.
func TestAnAttemptStartsOnlyTheApprovedAgent(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Members[0].AgentID = "bold"
		c.Members[0].Args = []string{"--verbose"}
	}, catalog.Agent{ID: "bold", Name: "bold", Command: []string{"/bin/true"}})
	// The saved entry takes the member's extra arguments; the configured
	// one it replaces does not.
	entry := func(edit func(map[string]any)) map[string]any {
		b := boldEntry(edit)
		b["allowArgs"] = true
		return b
	}
	l.save(entry(nil))
	plan := l.approved(t, nil)
	edited := func(b map[string]any) { b["command"] = []string{"/bin/sh", "-c", "echo edited; exec cat", "sh"} }
	words := "the agent bold is not the one plan revision 2 approved: its catalog entry changed since"

	reached, release := holdAttempts(l.srv)
	runID := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "launch")["runId"].(string)
	if spec := <-reached; spec.AgentSHA256 == "" || spec.Ref.RunID != runID {
		t.Fatalf("the attempt's spec: %+v", spec)
	}
	l.save(entry(edited))
	close(release)
	if row := ledgerAttemptIn(t, l.srv, runID, "build", 1, ledger.AttemptFailed); row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("attempt 1: %+v", row)
	}
	waitPaused(t, l.testEnv, runID, words)
	l.srv.launches.setSeams(func(g *launchGate) { g.attempt = nil })
	l.save(entry(nil))
	command(t, l.testEnv, runID, "unpause")
	runningAttempt(t, l.testEnv, runID, "build", 2)

	// A retry: the task cancelled, the entry edited, the task retried.
	command(t, l.testEnv, runID, "tasks/build/cancel")
	l.save(entry(edited))
	command(t, l.testEnv, runID, "tasks/build/retry")
	if row := ledgerAttemptIn(t, l.srv, runID, "build", 3, ledger.AttemptFailed); row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("the retry's attempt: %+v", row)
	}
	waitPaused(t, l.testEnv, runID, words)

	// A removal while the run is paused: the saved entry deleted, so the
	// configured one it replaced is back (another command, no extra
	// arguments taken, the member's refused only after the selection is),
	// then that one hidden too. Unpaused each time, the attempt fails at
	// agent the same way, uncounted, with no session; the entry brought
	// back and the run unpaused, the attempt starts.
	if status := l.del("bold"); status != http.StatusNoContent {
		t.Fatalf("delete bold: %d", status)
	}
	if a, ok := l.srv.Catalog().Get("bold"); !ok || a.AllowArgs || !slices.Equal(a.Command, []string{"/bin/true"}) {
		t.Fatalf("the configured bold: %+v %v", a, ok)
	}
	command(t, l.testEnv, runID, "unpause")
	if row := ledgerAttemptIn(t, l.srv, runID, "build", 4, ledger.AttemptFailed); row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("the configured agent's attempt: %+v", row)
	}
	waitPaused(t, l.testEnv, runID, words)
	if status := l.del("bold"); status != http.StatusNoContent {
		t.Fatalf("hide bold: %d", status)
	}
	if _, ok := l.srv.Catalog().Get("bold"); ok {
		t.Fatal("bold is still in the catalog")
	}
	command(t, l.testEnv, runID, "unpause")
	if row := ledgerAttemptIn(t, l.srv, runID, "build", 5, ledger.AttemptFailed); row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("the removed agent's attempt: %+v", row)
	}
	waitPaused(t, l.testEnv, runID, words)
	l.expect(t, "POST", "/api/catalog/bold/unhide", nil, http.StatusOK, "")
	l.save(entry(nil))
	command(t, l.testEnv, runID, "unpause")
	runningAttempt(t, l.testEnv, runID, "build", 6)
	l.save(entry(edited))

	// A continuation after a restart, the entry still edited.
	l.expect(t, "POST", "/api/runs/"+runID+"/stop", nil, http.StatusOK, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	next := again.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)["id"].(string)
	if r := runOf(t, again, next); r["ticket"] == nil {
		t.Fatalf("the continuation carries no ticket: %v", r)
	}
	command(t, again, next, "unpause")
	if row := ledgerAttemptIn(t, again.srv, next, "build", 0, ledger.AttemptFailed); row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("the continuation's attempt: %+v", row)
	}
	waitPaused(t, again, next, words)

	// A crew launched from the Crews page has no package: it launches on
	// the entry as it is (in the project's folder, the repository).
	c := l.crew
	c.Cwd = l.repo
	if _, err := again.srv.crews.Update(c.ID, c); err != nil {
		t.Fatal(err)
	}
	resp, out := again.do("POST", "/api/crews/"+l.crew.ID+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the Crews page's launch: %d %v", resp.StatusCode, out)
	}
	plain := out["run"].(map[string]any)["id"].(string)
	runningAttempt(t, again, plain, "build", 1)
}

// An attachment the revision hands over reaches every attempt byte for
// byte under its derived name, through New's own wiring (Dispatch's
// OpenLaunchFile given the engine): removed from the ticket and swept
// after the launch, a later attempt still gets it.
func TestLaunchAttachmentsReachEveryAttempt(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Tasks[0].Checks = []crew.Check{{ID: "test", Argv: []string{"false"}}} })
	content := []byte("the spec, line one\nline two\n")
	attachment := entityOf(t, l.upload(t, "CND-1", "spec sheet.txt", "text/plain", content, http.StatusCreated, ""), "attachment")["id"].(string)
	plan := l.approved(t, map[string]any{"attachments": []string{attachment}})
	runID := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "launch")["runId"].(string)
	a1 := runningAttempt(t, l.testEnv, runID, "build", 1)
	if got := l.treeFile(t, runID, "build", 1, "attachments/01-spec_sheet.txt"); !bytes.Equal(got, content) {
		t.Fatalf("attempt 1's attachment: %q", got)
	}
	// Paused, the attachment removed and the store swept (a store opened
	// again sweeps), then attempt 1's result fails its check.
	command(t, l.testEnv, runID, "pause")
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/attachments/"+attachment+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	swept, err := dispatch.Open(filepath.Join(l.srv.store.Dir(), "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := swept.SweepError(); err != nil {
		t.Fatal(err)
	}
	swept.Close()
	l.submit(t, a1["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, runID, "build", 1, ledger.AttemptFailed)
	command(t, l.testEnv, runID, "unpause")
	runningAttempt(t, l.testEnv, runID, "build", 2)
	if got := l.treeFile(t, runID, "build", 2, "attachments/01-spec_sheet.txt"); !bytes.Equal(got, content) {
		t.Fatalf("attempt 2's attachment: %q", got)
	}
}

// The acceptance boundary's second half: a launched run paused, the
// ticket's title, description and criteria edited and a comment added,
// the run unpaused: the next attempt's ticket.md and package.json are the
// launch's byte for byte, the run's digest unchanged, ChangedSince names
// the three fields and the comment, and a follow-up launch is 409
// stale_plan until the revision is approved again.
func TestEditingTheTicketNeverRewritesAnActiveRun(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Tasks[0].Checks = []crew.Check{{ID: "test", Argv: []string{"false"}}} })
	launch := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")
	runID, launchID := launch["runId"].(string), launch["id"].(string)
	a1 := runningAttempt(t, l.testEnv, runID, "build", 1)
	md1, pkg1 := l.treeFile(t, runID, "build", 1, "ticket.md"), l.treeFile(t, runID, "build", 1, "package.json")
	digest := runOf(t, l.testEnv, runID)["ticket"].(map[string]any)["digest"]

	command(t, l.testEnv, runID, "pause")
	l.submit(t, a1["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, runID, "build", 1, ledger.AttemptFailed)
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(),
		"set": map[string]any{"title": "Fix the login loop for good", "description": "It loops forever.", "acceptance": "It never loops."}}, http.StatusOK, "")
	l.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "Also on Safari."}, http.StatusCreated, "")
	command(t, l.testEnv, runID, "unpause")
	runningAttempt(t, l.testEnv, runID, "build", 2)
	if md2 := l.treeFile(t, runID, "build", 2, "ticket.md"); !bytes.Equal(md2, md1) {
		t.Fatalf("attempt 2's ticket.md is not the launch's:\n%s\n---\n%s", md2, md1)
	}
	if pkg2 := l.treeFile(t, runID, "build", 2, "package.json"); !bytes.Equal(pkg2, pkg1) {
		t.Fatal("attempt 2's package.json is not the launch's")
	}
	if now := runOf(t, l.testEnv, runID)["ticket"].(map[string]any)["digest"]; now != digest || now != l.launchOf(t, launchID).PackageSHA256 {
		t.Fatalf("the run's digest: %v, launched %v", now, digest)
	}
	changed, err := l.srv.dispatch.ChangedSince(t.Context(), launchID)
	if err != nil || !reflect.DeepEqual(changed.Fields, []string{"title", "description", "acceptance"}) || changed.Comments != 1 {
		t.Fatalf("changed since: %+v %v", changed, err)
	}
	// A follow-up: refused until the revision is approved again.
	out := l.launch(t, l.testEnv, nil, http.StatusConflict, dispatch.RefusalStalePlan)
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "approve it again or revise it") {
		t.Fatalf("the follow-up: %v", out)
	}
	l.expect(t, "POST", "/api/dispatch/plans/"+l.plan+"/approve", map[string]any{"opId": opID(), "note": "Still right."}, http.StatusOK, "")
	follow := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")
	if follow["runId"] == runID {
		t.Fatalf("the follow-up took the run's id: %v", follow)
	}
}

// The launch read's largest reply: a package at its bound with the
// summary's texts (why, the agents, the label, the author) at their
// escaped worst, encoded as the route encodes it, is under MaxLaunchReply.
func TestTheLaunchReadAtItsWorstIsUnderItsBound(t *testing.T) {
	worst := func(n int) string { return strings.Repeat(string(rune(0x2028)), n) }
	pad := `{"x":"` + strings.Repeat("a", dispatch.MaxPackage-len(`{"x":""}`)) + `"}`
	if len(pad) != dispatch.MaxPackage || !json.Valid([]byte(pad)) {
		t.Fatalf("the package is %d bytes", len(pad))
	}
	agents := make([]dispatch.LaunchAgent, dispatch.MaxPlanAgents)
	for i := range agents {
		agents[i] = dispatch.LaunchAgent{Member: strings.Repeat("m", 40), Agent: strings.Repeat("a", 64), Version: strings.Repeat(`"`, dispatch.MaxLaunchAgentVersion)}
	}
	hex64 := strings.Repeat("f", 64)
	v := dispatch.LaunchView{LaunchSummary: dispatch.LaunchSummary{ID: strings.Repeat("0", 16), TicketID: strings.Repeat("1", 16), TicketKey: "ABCDEF-999999",
		PlanID: strings.Repeat("2", 16), PlanN: dispatch.MaxPlans, RunID: strings.Repeat("c", 40) + "-0000aaaa", Label: worst(dispatch.MaxLaunchLabel), TicketRev: 1 << 40,
		Intent: hex64, Criteria: hex64, PackageSHA256: hex64, State: dispatch.LaunchFailed, Why: worst(dispatch.MaxLaunchWhy), Agents: agents,
		CreatedAt: time.Now(), CreatedBy: worst(dispatch.MaxByName), Fingerprint: hex64, StartedAt: time.Now(), Rev: 1 << 40}, Package: json.RawMessage(pad)}
	rec := httptest.NewRecorder()
	writeDispatch(rec, http.StatusOK, map[string]any{"launch": v})
	if n := rec.Body.Len(); n > MaxLaunchReply || n < dispatch.MaxPackage {
		t.Fatalf("the largest launch read is %d bytes, its bound %d", n, MaxLaunchReply)
	}
}
