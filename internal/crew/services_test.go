package crew

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"weak"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// serviceLoop is a service that writes its pid to pidName in its tree's
// state directory, then runs until it is stopped; with ignoreTerm it
// ignores SIGTERM (and so do the sleeps it starts), so only the grace's
// SIGKILL ends it.
func serviceLoop(id, pidName string, ignoreTerm bool) Check {
	trap := ""
	if ignoreTerm {
		trap = `trap "" TERM; `
	}
	return shStep(id, trap+`echo $$ > "$CONDUCTOR_STATE_DIR/`+pidName+`"; while :; do sleep 0.05; done`)
}

// byFile is a ready probe that passes once the file name is in the tree's
// state directory, tried every second.
func byFile(id, name string) Probe {
	return Probe{ID: id, Argv: []string{"test", "-s", "${CONDUCTOR_STATE_DIR}/" + name}, IntervalSeconds: 1}
}

// never is a ready probe that never passes: a test waits on what else
// ends the ready phase (a service exiting before it).
var never = Probe{ID: "never", Argv: []string{"test", "-e", "${CONDUCTOR_STATE_DIR}/never"}, IntervalSeconds: 1}

// stateFile is the path of name in a tree's state directory.
func stateFile(tree, name string) string {
	return filepath.Join(tree, taskDir, stateDirName, name)
}

// rowOf is the receipt of id at k among rows, and whether it is there.
func rowOf(rows []ledger.CheckRow, id string, k int) (ledger.CheckRow, bool) {
	for _, r := range rows {
		if r.ID == id && r.K == k {
			return r, true
		}
	}
	return ledger.CheckRow{}, false
}

// before reports whether the first event of kind a comes before the first
// of kind b in kinds, both there.
func before(kinds []string, a, b string) bool {
	i, j := slices.Index(kinds, a), slices.Index(kinds, b)
	return i >= 0 && j >= 0 && i < j
}

// previewOf is the preview of the task's latest attempt as the run reads.
func previewOf(t *testing.T, e *Engine, runID, taskID string) string {
	t.Helper()
	ts := taskOf(t, e, runID, taskID)
	if ts.Attempt == nil {
		return ""
	}
	return ts.Attempt.Preview
}

// gatedStop is a service whose Stop tells called, then waits for gate
// before it stops the service: a test reads the run while a tree's
// services are being stopped.
type gatedStop struct {
	service
	gate   <-chan struct{}
	called chan<- struct{}
}

func (g *gatedStop) Stop(ctx context.Context) error {
	select {
	case g.called <- struct{}{}:
	default:
	}
	<-g.gate
	return g.service.Stop(ctx)
}

// gateStops makes every service e starts from now on a gatedStop, and
// returns the channel each Stop tells and the gate's opener.
func gateStops(t *testing.T, e *Engine) (called <-chan struct{}, open func()) {
	gate, told := make(chan struct{}), make(chan struct{}, 16)
	native := e.exec.serve
	e.exec.serve = func(ctx context.Context, s proc.Spec) (service, error) {
		svc, err := native(ctx, s)
		if err != nil {
			return nil, err
		}
		return &gatedStop{service: svc, gate: gate, called: told}, nil
	}
	opened := false
	open = func() {
		if !opened {
			opened = true
			close(gate)
		}
	}
	t.Cleanup(open)
	return told, open
}

// The helper leaves its process group (setsid), writes its pid and then a
// mark to the directory its arguments name, and sleeps: a daemon a service
// or a step could start, which the group's signals cannot reach. It runs
// only when its arguments say so (after --), never in a test run.
func TestHelperDaemonizes(t *testing.T) {
	args := flag.Args()
	if len(args) != 2 || args[0] != "conductor-test-daemon" {
		t.Skip("a helper")
	}
	if _, err := syscall.Setsid(); err != nil {
		os.Exit(2)
	}
	os.WriteFile(filepath.Join(args[1], "daemon.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	os.WriteFile(filepath.Join(args[1], "daemon.ready"), nil, 0o600)
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// daemonizing is a command of sh that starts the helper (which leaves its
// group) in the background, waits until it has, and exits 0; redirect
// says whether the helper's output goes elsewhere than the command's.
func daemonizing(id string, redirect bool) Check {
	out := ""
	if redirect {
		out = " >/dev/null 2>&1"
	}
	line := `"$0" -test.run='^TestHelperDaemonizes$' -- conductor-test-daemon "$CONDUCTOR_STATE_DIR"` + out + ` & while [ ! -e "$CONDUCTOR_STATE_DIR/daemon.ready" ]; do sleep 0.05; done; exit 0`
	return Check{ID: id, Argv: []string{"sh", "-c", line, os.Args[0]}}
}

// A tree's services run beside it from its services phase, and its ready
// probes wait for them (EN1 PR-4): a service is a row of phase service put
// started before its process and running with its pid, beside the tree as
// long as it lives; a probe by file passes once the service wrote its pid
// in the state directory, a probe over HTTP (the harness answers it from a
// map) once its URL answers under 300, each one row with its tries; then
// tree.ready, with the preview, before the launch. The attempt's preview
// is its worktree's URL; the task files name the preview and the
// services; the session is given CONDUCTOR_PREVIEW_URL. The checkout of
// the result is a tree of its own, its service on its own port, which the
// task's check reaches. Once the task succeeded both trees end, their
// services stopped (service.ended, the rows finished), their pids gone, and
// the preview with the attempt's close.
func TestATreesServicesRunBesideItAndItsProbesWaitForThem(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", `test -s "$CONDUCTOR_STATE_DIR/web.pid"`)}
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)},
		Ready:   []Probe{byFile("up", "web.pid"), {ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}},
		Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
	url, ckURL := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst), fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst+1)
	fl.probes.answer(url, http.StatusOK)
	fl.probes.answer(ckURL, http.StatusNoContent)
	run := launchManaged(t, e, repoManifestCrew(dir, m, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	wt := a.Attempt.Worktree
	pid := pidIn(t, stateFile(wt, "web.pid"))
	if !alive(pid) {
		t.Fatalf("the service %d is not running beside the tree", pid)
	}
	rows := l.stepRows(run.ID, "build", 1)
	web, ok1 := rowOf(rows, "web", 1)
	up, ok2 := rowOf(rows, "up", 1)
	page, ok3 := rowOf(rows, "page", 1)
	if !ok1 || !ok2 || !ok3 || len(rows) != 3 {
		t.Fatalf("the worktree's receipts:\n%s", rowWords(rows))
	}
	if web.Phase != ledger.PhaseService || web.State != ledger.CheckRunning || web.PID != pid || web.Group != pid || web.ProcessStartedAt.IsZero() || web.Dir != wt || web.Manifest != "crew:0" {
		t.Fatalf("the service's row: %+v", web)
	}
	if up.Phase != ledger.PhaseReady || up.State != ledger.CheckFinished || up.Error != "" || up.Tries < 1 || up.Argv[2] != stateFile(wt, "web.pid") {
		t.Fatalf("the probe by file: %+v", up)
	}
	if page.Phase != ledger.PhaseReady || page.State != ledger.CheckFinished || page.Error != "" || page.Exit != 0 || page.URL != url || len(page.Argv) != 0 || page.Tries != fl.probes.tries(url) ||
		!strings.HasSuffix(string(page.Log), fmt.Sprintf("try %d: answered 200 OK\n", page.Tries)) {
		t.Fatalf("the probe over HTTP: %+v (asked %d times)", page, fl.probes.tries(url))
	}
	kinds := l.kinds(run.ID)
	for _, o := range [][2]string{{"attempt.workspace", "step.started"}, {"step.running", "step.finished"}, {"step.finished", "tree.ready"}, {"tree.ready", "attempt.launched"}} {
		if !before(kinds, o[0], o[1]) {
			t.Fatalf("%s before %s: %v", o[0], o[1], kinds)
		}
	}
	if p := l.payloads(run.ID, "tree.ready")[0]; p["tree"] != ledger.TreeWorktree || p["preview"] != url {
		t.Fatalf("tree.ready: %v", p)
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State != ledger.TreeReady || tr.Preview != url {
		t.Fatalf("the worktree's row: %+v", tr)
	}
	if a.Attempt.Preview != url {
		t.Fatalf("the attempt's preview: %q", a.Attempt.Preview)
	}
	md, f := taskFileOf(t, a)
	if f.Manifest == nil || f.Manifest.Preview != url || !slices.Equal(f.Manifest.Services, []string{"web"}) ||
		!strings.Contains(md, "Its service web runs beside your session") || !strings.Contains(md, "The preview, what a person opens to look at the work, is "+url+" (`CONDUCTOR_PREVIEW_URL`).") {
		t.Fatalf("the task file's manifest: %+v\n%s", f.Manifest, md)
	}
	fl.mu.Lock()
	inject := fl.calls[len(fl.calls)-1].inject
	fl.mu.Unlock()
	if inject["CONDUCTOR_PREVIEW_URL"] != url {
		t.Fatalf("the session's names: %v", inject)
	}
	// The checkout: its own service on its own port, which the check finds.
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	waitTreesEnded(t, e, run.ID)
	ckPid := pidIn(t, stateFile(wt+".check", "web.pid"))
	for _, p := range []int{pid, ckPid} {
		if alive(p) {
			t.Fatalf("the service %d outlived its tree", p)
		}
	}
	rows = l.stepRows(run.ID, "build", 1)
	for _, k := range []int{1, 2} {
		r, ok := rowOf(rows, "web", k)
		if !ok || r.State != ledger.CheckFinished || r.Signal != "terminated" || r.Error != "" || r.FinishedAt.IsZero() || r.LogSHA256 != sha(r.Log) {
			t.Fatalf("the service's row k%d at its end: %+v", k, r)
		}
	}
	if unit, ok := rowOf(rows, "unit", 1); !ok || unit.Exit != 0 || unit.Error != "" {
		t.Fatalf("the check beside the checkout's service: %+v", unit)
	}
	ended := l.payloads(run.ID, "service.ended")
	if len(ended) != 2 || ended[0]["why"] != "attempt 1 succeeded" || ended[0]["signal"] != "terminated" || ended[1]["why"] != "attempt 1 succeeded" {
		t.Fatalf("service.ended: %v", ended)
	}
	if kinds := l.kinds(run.ID); !before(kinds, "service.ended", "tree.ended") || !before(kinds, "task.succeeded", "service.ended") {
		t.Fatalf("the trees' ends: %v", kinds)
	}
	if p := previewOf(t, e, run.ID, "build"); p != "" {
		t.Fatalf("the preview after the attempt closed: %q", p)
	}
	stopRun(t, e, run.ID)
}

// In an engine on the native probe, a probe over HTTP asks the tree's own
// port on the loopback (here an httptest server's, the port scripted): it
// passes when the URL answers, and one that answers a redirect never
// passes, the server the redirect names asked nothing: the probe fails at
// its timeout, each try's answer in its log, in a worktree uncounted.
func TestAnEngineProbeAsksTheLoopbackAndFollowsNoRedirect(t *testing.T) {
	t.Run("an answer", func(t *testing.T) {
		var asked atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				asked.Add(1)
			}
		}))
		defer srv.Close()
		port := srv.Listener.Addr().(*net.TCPAddr).Port
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		e.exec.probe = proc.Probe
		fl.ports.script(port)
		m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)},
			Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health", IntervalSeconds: 1}}, Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		want := fmt.Sprintf("http://127.0.0.1:%d/health", port)
		if page, ok := rowOf(l.stepRows(run.ID, "build", 1), "page", 1); !ok || page.URL != want || page.Error != "" || asked.Load() < 1 {
			t.Fatalf("the probe: %+v, asked %d", page, asked.Load())
		}
		if a.Attempt.Preview != fmt.Sprintf("http://127.0.0.1:%d/", port) {
			t.Fatalf("the preview: %q", a.Attempt.Preview)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("a redirect", func(t *testing.T) {
		var elsewhere atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
		defer other.Close()
		moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/", http.StatusFound)
		}))
		defer moved.Close()
		port := moved.Listener.Addr().(*net.TCPAddr).Port
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		e.exec.probe = proc.Probe
		fl.ports.script(port)
		m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)},
			Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1, TimeoutSeconds: 3}}}
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		attemptFailed(t, e, run.ID, "build", 1)
		row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
		if row.Stage != "ready page" || !strings.Contains(row.Error, "page did not pass within 3s") {
			t.Fatalf("attempt 1: %+v", row)
		}
		// Every try is in the probe's log: the redirect was each one's answer.
		if page, ok := rowOf(l.stepRows(run.ID, "build", 1), "page", 1); !ok || page.Tries < 1 || !strings.Contains(string(page.Log), "answered 302 Found") || page.URL != fmt.Sprintf("http://127.0.0.1:%d/", port) {
			t.Fatalf("the probe: %+v", page)
		}
		if n := elsewhere.Load(); n != 0 {
			t.Fatalf("the redirect was followed: %d requests elsewhere", n)
		}
		waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
		stopRun(t, e, run.ID)
	})
}

// A service's row is put started, and committed, before its process
// exists (the executor finds it in the ledger when it is asked to start
// the service); a running row the ledger refuses stops the service and
// reaps it, and fails the tree at the service with the ledger's words, in a
// worktree uncounted, the run paused. The service's end is recorded with
// the tree's.
func TestAServicesRowComesFirstAndARefusedRunningRowStopsIt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	native := e.exec.serve
	var startedFirst atomic.Bool
	e.exec.serve = func(ctx context.Context, s proc.Spec) (service, error) {
		l.mu.Lock()
		for _, c := range l.checks {
			if c.Phase == ledger.PhaseService && c.ID == "web" && c.State == ledger.CheckStarted {
				startedFirst.Store(true)
			}
		}
		l.mu.Unlock()
		return native(ctx, s)
	}
	l.mu.Lock()
	l.failOn, l.failOnce = "step.running[phase=service]", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}}, work("build", "lead")))
	attemptFailed(t, e, run.ID, "build", 1)
	if !startedFirst.Load() {
		t.Fatal("the service was started before its row was committed")
	}
	words := "the ledger could not record the service web: ledger: disk full (injected)"
	if row := l.attemptWhen(t, run.ID, "build", 1, failedRow); row.Stage != "service web" || row.Error != words {
		t.Fatalf("attempt 1: %+v", row)
	}
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	waitTreesEnded(t, e, run.ID)
	web, ok := rowOf(l.stepRows(run.ID, "build", 1), "web", 1)
	if !ok || web.State != ledger.CheckFinished || web.PID == 0 {
		t.Fatalf("the service's row: %+v", web)
	}
	if alive(web.PID) {
		t.Fatalf("the service %d outlived its refused running row", web.PID)
	}
	if p := l.payloads(run.ID, "service.ended"); len(p) != 1 || p[0]["why"] != "the ledger could not record its start" {
		t.Fatalf("service.ended: %v", p)
	}
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && strings.Contains(r.PausedWhy, words) })
	stopRun(t, e, run.ID)
}

// A worktree whose tree.ready the ledger refuses could not be made usable:
// the attempt fails at ready, uncounted (the task ready with why, one more
// attempt allowed), and the run's dispatch pauses with the same words. A
// continuation reads its row so too (Codex's review of #441, round 2): a
// bare ready is a manifest's stage (manifestStage), and the attempt is
// never taken for the task's last counted one. Attempt 1's result B,
// refused at a check, attempt 2 at B refused at ready, attempt 3 at B, in
// the run and after a continuation made while it is paused.
func TestATreeReadyTheLedgerRefusesIsNotCounted(t *testing.T) {
	for _, continued := range []bool{false, true} {
		t.Run(map[bool]string{false: "in the run", true: "after a continuation"}[continued], func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			build := work("build", "lead")
			build.Checks = []Check{shStep("ok", "test -e ok.txt")}
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}}, build))
			a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			// Attempt 1's worktree is ready: the next worktree's tree.ready
			// is refused, once; the checkout's is not.
			l.mu.Lock()
			l.failOn, l.failOnce = "tree.ready[tree="+ledger.TreeWorktree+"]", true
			l.mu.Unlock()
			commitResult(t, e, a1, "half.txt", "half\n", "half")
			attemptFailed(t, e, run.ID, "build", 2)
			row1, row2 := l.attemptWhen(t, run.ID, "build", 1, failedRow), l.attemptWhen(t, run.ID, "build", 2, failedRow)
			b := row1.ResultCommit
			words := "the ledger could not record the tree ready: ledger: disk full (injected)"
			if row1.Stage != "check ok" || b == "" || row2.StartCommit != b || row2.Stage != "ready" || row2.Error != words || row2.ResultCommit != "" || !uncountedRow(row2) {
				t.Fatalf("attempt 1 %+v\nattempt 2 %+v", row1, row2)
			}
			failed := l.payloads(run.ID, "attempt.failed")
			if p := failed[len(failed)-1]; p["attempt"] != 2 || p["uncounted"] != true || p["stage"] != "ready" {
				t.Fatalf("attempt.failed: %v", p)
			}
			why := "attempt 2 could not be made usable at ready: " + words
			waitFake(t, "the pause recorded", func() bool {
				l.mu.Lock()
				defer l.mu.Unlock()
				return l.runs[run.ID].Paused && l.runs[run.ID].PausedWhy == why
			})
			runID, eng := run.ID, e
			if continued {
				stopRun(t, e, run.ID)
				e2, _ := readyEngine(t)
				e2.UseLedger(l)
				next, err := e2.ContinueRun(t.Context(), run.ID, "")
				if err != nil {
					t.Fatal(err)
				}
				if !next.Paused || next.PausedWhy != why {
					t.Fatalf("the continuation's pause: %v %q", next.Paused, next.PausedWhy)
				}
				runID, eng = next.ID, e2
			}
			pauseRun(t, eng, runID, false)
			a3 := attemptN(t, eng, runID, "build", 3)
			if a3.Attempt.Start != b {
				t.Fatalf("attempt 3 starts at %s, not at attempt 2's start %s", short(a3.Attempt.Start), short(b))
			}
			md, f := taskFileOf(t, a3)
			if f.Previous == nil || f.Previous.Attempt != 1 || f.Previous.Stage != "check ok" || f.Unusable == nil || f.Unusable.Attempt != 2 || f.Unusable.Stage != "ready" ||
				f.StartFrom != "attempt 1's result" || !strings.Contains(md, "Attempt 2 could not be made usable: it failed at ready") {
				t.Fatalf("attempt 3's task file: previous %+v unusable %+v from %q\n%s", f.Previous, f.Unusable, f.StartFrom, md)
			}
			stopRun(t, eng, runID)
		})
	}
}

// weakService is weak pointers to the log and the handle of the first
// service of the attempt's worktree, taken under e.mu: the test keeps no
// strong one.
func weakService(e *Engine, runID, taskID string, n int) (weak.Pointer[bytes.Buffer], weak.Pointer[proc.Service]) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ts := e.runs[runID].task(taskID).attempts[n-1].trees[0].services[0]
	return weak.Make(ts.log), weak.Make(ts.svc.(*proc.Service))
}

// collected runs the collector until p's value has gone, for at most 10 s,
// and says whether it went.
func collected[T any](p weak.Pointer[T]) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if p.Value() == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// An ended tree keeps none of its services (Codex's review of #441, round
// 2): an ended tree stays its attempt's as long as the engine holds the
// run, and once their ends are recorded it lets go of their handles and
// their output, so the log it buffered, up to ledger.MaxLog a service, is
// the ledger's alone. The worktree's and the checkout's service rows keep
// what web wrote, a refused commit's carried by the next, and the
// worktree service's log and handle are collected. Each tree is ready
// only once its web has written its line and then its pid (the probe up),
// so neither tree ends before the line is written (Codex's review of
// #441, round 3).
func TestAnEndedTreeLetsGoOfItsServices(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "their ends recorded", true: "their ends refused once"}[refused], func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			web := shStep("web", `echo serving; echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; while :; do sleep 0.05; done`)
			m := Manifest{Services: []Check{web}, Ready: []Probe{byFile("up", "web.pid")}}
			run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			logOf, svcOf := weakService(e, run.ID, "build", 1)
			if refused {
				l.mu.Lock()
				l.failOn, l.failOnce = "service.ended", true
				l.mu.Unlock()
			}
			commitResult(t, e, a, "done.txt", "done\n", "done")
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
			waitTreesEnded(t, e, run.ID)
			l.mu.Lock()
			unused := l.failOn != ""
			l.mu.Unlock()
			if unused {
				t.Fatal("no service.ended was refused")
			}
			rows := l.stepRows(run.ID, "build", 1)
			for k := 1; k <= 2; k++ {
				if row, ok := rowOf(rows, "web", k); !ok || row.State != ledger.CheckFinished || string(row.Log) != "serving\n" {
					t.Fatalf("web's row %d: %+v", k, row)
				}
			}
			e.mu.Lock()
			trees := e.runs[run.ID].task("build").attempts[0].trees
			var holding []string
			for _, tr := range trees {
				if !tr.ended || tr.services != nil || tr.dead != nil {
					holding = append(holding, tr.kind)
				}
			}
			n := len(trees)
			e.mu.Unlock()
			if n != 2 || len(holding) > 0 {
				t.Fatalf("%d trees; not ended, or holding their services: %v", n, holding)
			}
			if !collected(logOf) || !collected(svcOf) {
				t.Fatal("the worktree service's log or handle outlived its tree's end")
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A service is a foreground process: one that exits before its tree is
// ready fails the tree at it (the phase waiting on a probe that would
// never pass is told at once), its end recorded with the words, the tree
// ended after; in a worktree uncounted, the run paused. A service that
// dies says how, and one that has exited before the executor hands it
// over has its end recorded after its running row, never written over by
// it, the probe after it run no try (tries 0, no exit: the independent
// review of #441, finding 3); a daemonizer, whose command puts its child
// outside its group and exits 0, is told a service must stay in the
// foreground, and its child, not the tree's, is neither stopped nor
// reported: the test ends it. With no probe to wait on, a service whose
// end its watcher has not recorded yet, held at its seam, still keeps the
// tree from being recorded ready: the phase reads its process's end
// itself (Codex's review of #441, round 1).
func TestAServiceThatExitsBeforeTheTreeIsReadyFailsIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service Check
		words   string
		daemon  bool
		// atOnce hands the service over only once it has exited: its
		// watcher sees its end before its running row is put.
		atOnce bool
		// noProbe leaves the never probe out; hold holds the service's
		// watcher at its seam until the attempt has failed.
		noProbe, hold bool
	}{
		{"one that dies", shStep("web", `echo the port is taken; exit 1`), "web exited 1 before the tree was ready: the port is taken", false, false, false, false},
		{"one that exits at once", shStep("web", `echo gone; exit 1`), "web exited 1 before the tree was ready: gone", false, true, false, false},
		{"a daemonizer", daemonizing("web", true), "web exited 0 before the tree was ready: a service must stay in the foreground", true, false, false, false},
		{"one that exits at once, no probe, its watcher held", shStep("web", `echo gone; exit 1`), "web exited 1 before the tree was ready: gone", false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			if tc.atOnce {
				native := e.exec.serve
				e.exec.serve = func(ctx context.Context, s proc.Spec) (service, error) {
					svc, err := native(ctx, s)
					if err == nil {
						<-svc.Done()
					}
					return svc, err
				}
			}
			release := func() {}
			if tc.hold {
				held, released := make(chan struct{}), false
				release = func() {
					if !released {
						released = true
						close(held)
					}
				}
				t.Cleanup(release)
				e.beforeServiceEnd = func(string) { <-held }
			}
			ready := []Probe{never}
			if tc.noProbe {
				ready = nil
			}
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{tc.service}, Ready: ready}, work("build", "lead")))
			attemptFailed(t, e, run.ID, "build", 1)
			row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
			if row.Stage != "service web" || row.Error != tc.words {
				t.Fatalf("attempt 1: %+v", row)
			}
			if tc.hold {
				if kinds := l.kinds(run.ID); slices.Contains(kinds, "tree.ready") || slices.Contains(kinds, "service.ended") {
					t.Fatalf("the tree recorded ready, or the end recorded, while the watcher was held: %v", kinds)
				}
				release()
			}
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
			waitTreesEnded(t, e, run.ID)
			rows := l.stepRows(run.ID, "build", 1)
			web, ok := rowOf(rows, "web", 1)
			if !ok || web.State != ledger.CheckFinished || web.Error != tc.words {
				t.Fatalf("the service's row:\n%s", rowWords(rows))
			}
			if p, ok := rowOf(rows, "never", 1); !tc.noProbe && (!ok || p.State != ledger.CheckFinished || p.Error != tc.words) {
				t.Fatalf("the probe's row:\n%s", rowWords(rows))
			}
			if tc.atOnce && !tc.noProbe {
				// The service was gone before the probe's first try: the
				// probe ran none, and its receipt says no command exited.
				if p, _ := rowOf(rows, "never", 1); p.Tries != 0 || p.Exit != -1 || p.PID != 0 {
					t.Fatalf("the probe that ran no try:\n%s", rowWords(rows))
				}
				var fin map[string]any
				for _, p := range l.payloads(run.ID, "step.finished") {
					if p["id"] == "never" {
						fin = p
					}
				}
				if fin == nil || fin["exit"] != -1 || fin["tries"] != 0 {
					t.Fatalf("the probe's step.finished: %v", fin)
				}
			}
			if p := l.payloads(run.ID, "service.ended"); len(p) != 1 || p[0]["why"] != "it exited before the tree was ready" {
				t.Fatalf("service.ended: %v", p)
			}
			if kinds := l.kinds(run.ID); slices.Contains(kinds, "tree.ready") || slices.Contains(kinds, "attempt.launched") || !before(kinds, "step.running", "service.ended") {
				t.Fatalf("the tree was ready, or the service's end came before its running row: %v", kinds)
			}
			waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
			if tc.daemon {
				child := pidIn(t, stateFile(row.Worktree, "daemon.pid"))
				if !alive(child) {
					t.Fatalf("the daemon %d, not the tree's, was ended with it", child)
				}
				syscall.Kill(child, syscall.SIGKILL)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A service that exits while a probe's try is under way fails the tree at
// the service at once (Codex's review of #441, round 1): the try is cut, a
// command's group killed or a fetch's context ended, never left to run on
// to the probe's timeout, an hour here; the stage is the service's, and
// the probe's row says so with its one try.
func TestAServiceThatExitsCutsAProbesTryUnderWay(t *testing.T) {
	url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst)
	for _, byURL := range []bool{false, true} {
		t.Run(map[bool]string{false: "a command", true: "a URL"}[byURL], func(t *testing.T) {
			dir, gates := newRepo(t), t.TempDir()
			e, fl, l := managedEngine(t)
			fl.probes.answerWith(url, func(ctx context.Context) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			})
			probe := Probe{ID: "held", Argv: []string{"sh", "-c", `echo $$ > "` + gates + `/probe.pid"; while :; do sleep 0.05; done`}, TimeoutSeconds: 3600}
			if byURL {
				probe = Probe{ID: "held", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", TimeoutSeconds: 3600}
			}
			web := shStep("web", `while [ ! -e "`+gates+`/die" ]; do sleep 0.05; done; echo leaving; exit 1`)
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Ports: []string{"web"}, Services: []Check{web}, Ready: []Probe{probe}}, work("build", "lead")))
			probePid := 0
			if byURL {
				waitFake(t, "the probe asked", func() bool { return fl.probes.tries(url) > 0 })
			} else {
				probePid = pidIn(t, filepath.Join(gates, "probe.pid"))
				t.Cleanup(func() {
					if alive(probePid) {
						syscall.Kill(probePid, syscall.SIGKILL)
					}
				})
			}
			if err := os.WriteFile(filepath.Join(gates, "die"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			attemptFailed(t, e, run.ID, "build", 1)
			words := "web exited 1 before the tree was ready: leaving"
			if row := l.attemptWhen(t, run.ID, "build", 1, failedRow); row.Stage != "service web" || row.Error != words {
				t.Fatalf("attempt 1: %+v", row)
			}
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
			waitTreesEnded(t, e, run.ID)
			rows := l.stepRows(run.ID, "build", 1)
			held, ok := rowOf(rows, "held", 1)
			if !ok || held.State != ledger.CheckFinished || held.Tries != 1 || held.Error != words {
				t.Fatalf("the probe's row:\n%s", rowWords(rows))
			}
			if byURL && string(held.Log) != "try 1: cut: a service of the tree exited\n" {
				t.Fatalf("the probe's log: %q", held.Log)
			}
			if probePid != 0 && alive(probePid) {
				t.Fatalf("the probe's try %d outlived its cut", probePid)
			}
			if p := l.payloads(run.ID, "service.ended"); len(p) != 1 || p[0]["why"] != "it exited before the tree was ready" {
				t.Fatalf("service.ended: %v", p)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A service that exits 0 once its tree is in use is told the foreground
// rule all the same (the independent review of #441, finding 2): with no
// ready probe the tree is ready once its services run, and a daemonizer's
// exit comes after it (held here until the attempt runs). Its row, its
// service.ended and the run log's line say a service must stay in the
// foreground, the preview is withdrawn and the attempt goes on. The daemon
// it started, outside its group, is not the tree's: the test ends it.
func TestAServiceThatExits0OnceTheTreeIsInUseIsToldTheRule(t *testing.T) {
	dir, gates := newRepo(t), t.TempDir()
	e, _, l := managedEngine(t)
	line := `"$0" -test.run='^TestHelperDaemonizes$' -- conductor-test-daemon "$CONDUCTOR_STATE_DIR" >/dev/null 2>&1 & ` +
		`while [ ! -e "$CONDUCTOR_STATE_DIR/daemon.ready" ] || [ ! -e "` + gates + `/exit" ]; do sleep 0.05; done; exit 0`
	m := Manifest{Ports: []string{"web"}, Services: []Check{{ID: "web", Argv: []string{"sh", "-c", line, os.Args[0]}}}, Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst); a.Attempt.Preview != url {
		t.Fatalf("the preview after tree.ready: %q", a.Attempt.Preview)
	}
	daemon := pidIn(t, stateFile(a.Attempt.Worktree, "daemon.pid"))
	t.Cleanup(func() { syscall.Kill(daemon, syscall.SIGKILL) })
	if err := os.WriteFile(filepath.Join(gates, "exit"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFake(t, "web's end recorded", func() bool { return len(l.payloads(run.ID, "service.ended")) == 1 })
	const rule = "a service must stay in the foreground"
	if p := l.payloads(run.ID, "service.ended")[0]; p["id"] != "web" || p["exit"] != 0 || p["why"] != "it exited 0 while the tree was in use: "+rule {
		t.Fatalf("service.ended: %v", p)
	}
	if row, _ := rowOf(l.stepRows(run.ID, "build", 1), "web", 1); row.State != ledger.CheckFinished || row.Exit != 0 || row.Error != "web exited 0 while the tree was in use: "+rule {
		t.Fatalf("web's row: %+v", row)
	}
	r, _ := e.Get(run.ID)
	want := "task build: attempt 1: service web exited 0 while the tree was in use: " + rule + "; the preview is withdrawn"
	if !slices.ContainsFunc(r.Log, func(en session.ActivityEntry) bool { return en.Message == want }) {
		t.Fatalf("the run log has no %q", want)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempt.Preview != "" || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("the attempt after its service's exit: %+v", ts.Attempt)
	}
	if !alive(daemon) {
		t.Fatalf("the daemon %d, not the tree's, was ended with it", daemon)
	}
	stopRun(t, e, run.ID)
}

// A probe's log over HTTP keeps a line per try while they fit beside the
// room it keeps for its end, then the last try's line, after a line that
// counts those left out, and stays within its bound (the independent
// review of #441, finding 4): the try that passed is in the log however
// many failed before it.
func TestAProbesLogKeepsItsLastTry(t *testing.T) {
	l := probeLog{limit: probeLogEnd + 200}
	for i := 1; i < 50; i++ {
		l.add(fmt.Sprintf("try %d: answered 503 Service Unavailable\n", i))
	}
	l.add("try 50: answered 200 OK\n")
	got := string(l.bytes())
	head := "try 1: answered 503 Service Unavailable\ntry 2: answered 503 Service Unavailable\ntry 3: answered 503 Service Unavailable\n" +
		"try 4: answered 503 Service Unavailable\ntry 5: answered 503 Service Unavailable\n"
	if want := head + "… 44 tries not logged …\ntry 50: answered 200 OK\n"; got != want || len(got) > l.limit {
		t.Fatalf("%d bytes:\n%s", len(got), got)
	}
	// Tries that fit are all kept, and nothing is counted.
	few := probeLog{limit: maxProbeLog}
	for i := 1; i <= 3; i++ {
		few.add(fmt.Sprintf("try %d: answered 503 Service Unavailable\n", i))
	}
	if got := string(few.bytes()); got != "try 1: answered 503 Service Unavailable\ntry 2: answered 503 Service Unavailable\ntry 3: answered 503 Service Unavailable\n" {
		t.Fatalf("%q", got)
	}
	// A bound too small for the end's room still holds: the last line is
	// cut to what is left of it.
	tiny := probeLog{limit: 40}
	for i := 1; i <= 3; i++ {
		tiny.add(fmt.Sprintf("try %d: answered 503 Service Unavailable\n", i))
	}
	if got := tiny.bytes(); len(got) != 40 || !strings.HasPrefix(string(got), "… 2 tries not logged …\ntry 3: ") {
		t.Fatalf("%d bytes: %q", len(got), got)
	}
}

// What a setup step puts outside its group is not the tree's: a child that
// left the group and holds the step's output fails the step with proc's
// words in its receipt, in a worktree uncounted; one that redirected its
// output leaves the step to finish as its exit says. Either child is left
// alone: the test ends it.
func TestASetupStepsChildThatLeftTheGroupIsNotTheTrees(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{false: "holding the step's output", true: "its output redirected"}[redirect], func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Setup: []Check{daemonizing("deps", redirect)}}, work("build", "lead")))
			var wt string
			if redirect {
				a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
				wt = a.Attempt.Worktree
				if deps, _ := rowOf(l.stepRows(run.ID, "build", 1), "deps", 1); deps.Exit != 0 || deps.Error != "" {
					t.Fatalf("the step: %+v", deps)
				}
			} else {
				attemptFailed(t, e, run.ID, "build", 1)
				row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
				wt = row.Worktree
				want := "deps could not run: proc: a process that left the group still holds the output; it is left alone"
				if row.Stage != "setup deps" || row.Error != want {
					t.Fatalf("attempt 1: %+v", row)
				}
				if deps, _ := rowOf(l.stepRows(run.ID, "build", 1), "deps", 1); deps.Error != want {
					t.Fatalf("the step: %+v", deps)
				}
				waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
			}
			child := pidIn(t, stateFile(wt, "daemon.pid"))
			if !alive(child) {
				t.Fatalf("the child %d that left the group was ended", child)
			}
			syscall.Kill(child, syscall.SIGKILL)
			stopRun(t, e, run.ID)
		})
	}
}

// The checkout is compared with the result once after its ready phase,
// the services having run beside the probes: a change there is the phase's,
// whoever made it, a service or a probe command, and the words name every
// one of them. The result is refused before its checks (counted), and the
// next attempt starts where the refused one did.
func TestTheReadyPhaseMayChangeNothingTrackedInTheCheckout(t *testing.T) {
	inCheckout := func(line string) string { return `case "$(pwd)" in *.check) ` + line + `;; esac; ` }
	for _, tc := range []struct {
		name  string
		m     Manifest
		words string
	}{
		{"a service that writes", Manifest{Services: []Check{serviceLoop("db", "db.pid", false), shStep("web", inCheckout("echo service >> README")+`echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; while :; do sleep 0.05; done`)},
			Ready: []Probe{byFile("up", "web.pid")}}, "tracked files changed while the services db, web ran and the probe up was tried: README"},
		{"a probe that writes", Manifest{Services: []Check{serviceLoop("web", "web.pid", false)},
			Ready: []Probe{byFile("up", "web.pid"), {ID: "touch", Argv: []string{"sh", "-c", inCheckout("echo probe >> README") + "exit 0"}, IntervalSeconds: 1}}},
			"tracked files changed while the service web ran and the probes up, touch were tried: README"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", "true")}
			run := launchManaged(t, e, repoManifestCrew(dir, tc.m, build))
			a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a1, "built.txt", "yes\n", "built")
			a2 := attemptN(t, e, run.ID, "build", 2)
			row := l.attemptRow(t, run.ID, "build", 1)
			if row.Stage != "ready" || row.Error != tc.words || a2.Attempt.Start != a1.Attempt.Start {
				t.Fatalf("attempt 1: %+v; attempt 2 starts at %s", row, short(a2.Attempt.Start))
			}
			if n := l.count(run.ID, "check.started"); n != 0 {
				t.Fatalf("%d checks ran after the ready phase changed tracked files", n)
			}
			if r, _ := e.Get(run.ID); r.Paused {
				t.Fatal("a refused result paused the run")
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A check beside a running service: a change found after the check names
// the service as its other possible author (the comparison cannot tell
// whose write it was), counted as a failing check is, the next attempt at
// the result.
func TestACheckBesideAServiceThatWritesNamesItAsTheOtherAuthor(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	// In the checkout, the service waits for the check to begin, then
	// writes a tracked file and says so; the check waits for that.
	svc := shStep("web", `echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; case "$(pwd)" in *.check) `+gated("$CONDUCTOR_STATE_DIR/checking")+`; echo service >> README; touch "$CONDUCTOR_STATE_DIR/written";; esac; while :; do sleep 0.05; done`)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", `touch "$CONDUCTOR_STATE_DIR/checking"; `+gated("$CONDUCTOR_STATE_DIR/written"))}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{svc}, Ready: []Probe{byFile("up", "web.pid")}}, build))
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a1, "built.txt", "yes\n", "built")
	a2 := attemptN(t, e, run.ID, "build", 2)
	words := "check unit, or a service of the tree (web), changed tracked files: README"
	row := l.attemptRow(t, run.ID, "build", 1)
	if row.Stage != "check unit" || row.Error != words || a2.Attempt.Start != row.ResultCommit {
		t.Fatalf("attempt 1: %+v; attempt 2 starts at %s", row, short(a2.Attempt.Start))
	}
	if unit, ok := rowOf(l.stepRows(run.ID, "build", 1), "unit", 1); !ok || unit.Error != words || unit.Stable {
		t.Fatalf("the check's row: %+v", unit)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 2 {
		t.Fatalf("the failing check was not counted: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A service that cannot run in the checkout of a result refuses the
// result before its checks, counted: the next attempt starts where the
// refused one did.
func TestACheckoutRefusedAtAService(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	svc := shStep("web", `case "$(pwd)" in *.check) echo no room >&2; exit 3;; esac; echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; while :; do sleep 0.05; done`)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "true")}
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{svc}, Ready: []Probe{byFile("up", "web.pid")}}, build))
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a1, "built.txt", "yes\n", "built")
	a2 := attemptN(t, e, run.ID, "build", 2)
	if row := l.attemptRow(t, run.ID, "build", 1); row.Stage != "service web" || row.Error != "web exited 3 before the tree was ready: no room" || a2.Attempt.Start != a1.Attempt.Start {
		t.Fatalf("attempt 1: %+v; attempt 2 starts at %s", row, short(a2.Attempt.Start))
	}
	if n := l.count(run.ID, "check.started"); n != 0 {
		t.Fatalf("%d checks ran after the checkout's service failed", n)
	}
	if r, _ := e.Get(run.ID); r.Paused {
		t.Fatal("a refused result paused the run")
	}
	stopRun(t, e, run.ID)
}

// The attempt's preview is offered while its worktree answers behind it,
// and never else (EN1 PR-4): none before tree.ready (a probe held at a
// gate), the URL after it; none once the attempt's close is committed,
// read while the tree's services are still being stopped (their stops held
// at a gate), and none after; still none when the next tree draws the
// number its tree let go. A service of the worktree that exits on its own
// after tree.ready withdraws it too: service.ended with its exit, the
// preview gone in that transaction, a line in the run log, the attempt
// still running and its other service stopped only when it closes.
func TestTheAttemptsPreviewIsOfferedWhileItsTreeAnswers(t *testing.T) {
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false), serviceLoop("db", "db.pid", false)},
		Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}}, Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
	url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst)
	t.Run("the attempt's close", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		gate := make(chan struct{})
		fl.probes.answerWith(url, func(ctx context.Context) (int, error) {
			select {
			case <-gate:
				return http.StatusOK, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
		stopping, openStops := gateStops(t, e)
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		waitFake(t, "the probe asked", func() bool { return fl.probes.tries(url) > 0 })
		if p := previewOf(t, e, run.ID, "build"); p != "" {
			t.Fatalf("a preview before tree.ready: %q", p)
		}
		close(gate)
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		if a.Attempt.Preview != url {
			t.Fatalf("the preview after tree.ready: %q", a.Attempt.Preview)
		}
		wt := a.Attempt.Worktree
		pids := []int{pidIn(t, stateFile(wt, "web.pid")), pidIn(t, stateFile(wt, "db.pid"))}
		if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-stopping:
		case <-time.After(20 * time.Second):
			t.Fatal("no service of the tree was being stopped after 20 s")
		}
		if p := previewOf(t, e, run.ID, "build"); p != "" {
			t.Fatalf("a preview while the tree's services are being stopped: %q", p)
		}
		if r, _ := e.Get(run.ID); r.Cleaning != 1 {
			t.Fatalf("cleaning %d while the tree's services are being stopped", r.Cleaning)
		}
		openStops()
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
		waitTreesEnded(t, e, run.ID)
		for _, p := range pids {
			if alive(p) {
				t.Fatalf("the service %d outlived its tree", p)
			}
		}
		if p := previewOf(t, e, run.ID, "build"); p != "" {
			t.Fatalf("a preview after the tree ended: %q", p)
		}
		// The next tree draws the number the ended one let go.
		fl.ports.script(fakePortsFirst)
		next := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		na := waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
		if w, _ := l.treeRow(next.ID, "build", 1, ledger.TreeWorktree); w.Ports["web"] != fakePortsFirst || na.Attempt.Preview != url {
			t.Fatalf("the next tree: %+v, its preview %q", w, na.Attempt.Preview)
		}
		if p := previewOf(t, e, run.ID, "build"); p != "" {
			t.Fatalf("the ended attempt's preview once its number is another tree's: %q", p)
		}
		stopRun(t, e, next.ID)
		stopRun(t, e, run.ID)
	})
	t.Run("a service's own exit", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		fl.probes.answer(url, http.StatusOK)
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		if a.Attempt.Preview != url {
			t.Fatalf("the preview after tree.ready: %q", a.Attempt.Preview)
		}
		wt := a.Attempt.Worktree
		web, db := pidIn(t, stateFile(wt, "web.pid")), pidIn(t, stateFile(wt, "db.pid"))
		syscall.Kill(web, syscall.SIGKILL)
		waitFake(t, "web's end recorded", func() bool { return len(l.payloads(run.ID, "service.ended")) == 1 })
		if p := l.payloads(run.ID, "service.ended")[0]; p["id"] != "web" || p["signal"] != "killed" || p["why"] != "it exited while the tree was in use" {
			t.Fatalf("service.ended: %v", p)
		}
		ts := taskOf(t, e, run.ID, "build")
		if ts.Attempt.Preview != "" || ts.Attempt.State != ledger.AttemptRunning {
			t.Fatalf("the attempt after its service's own exit: %+v", ts.Attempt)
		}
		r, _ := e.Get(run.ID)
		line := "task build: attempt 1: service web was ended by a signal (killed) while the tree was in use; the preview is withdrawn"
		if !slices.ContainsFunc(r.Log, func(en session.ActivityEntry) bool { return en.Message == line }) {
			t.Fatalf("the run log has no %q", line)
		}
		if row, _ := rowOf(l.stepRows(run.ID, "build", 1), "web", 1); row.State != ledger.CheckFinished || row.Signal != "killed" || !strings.HasPrefix(row.Error, "web was ended by a signal (killed) while the tree was in use") {
			t.Fatalf("web's row: %+v", row)
		}
		if !alive(db) {
			t.Fatal("db was stopped with web, before the attempt closed")
		}
		if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
			t.Fatal(err)
		}
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
		waitTreesEnded(t, e, run.ID)
		if alive(db) {
			t.Fatalf("db %d outlived its tree", db)
		}
		if p := l.payloads(run.ID, "service.ended"); len(p) != 2 || p[1]["id"] != "db" || !strings.HasPrefix(p[1]["why"].(string), "attempt 1 cancelled") {
			t.Fatalf("service.ended: %v", p)
		}
		stopRun(t, e, run.ID)
	})
}

// A stop ends the services of the run's live trees itself, beside the
// sessions and before it records the run stopped: with time to finish it
// returns once the service's pid is gone and the tree's end is committed
// (service.ended, why the run was stopped; tree.ended). Under a context
// already short it kills them at once and returns all the same, with the
// tree's end still under way (cleaning above zero: the end is held at its
// seam here), which ends after; Drain waits for it.
func TestAStopEndsTheServicesBeforeItReturns(t *testing.T) {
	m := Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")}}
	t.Run("with time to finish", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		pid := pidIn(t, stateFile(a.Attempt.Worktree, "web.pid"))
		stopRun(t, e, run.ID)
		if alive(pid) {
			t.Fatalf("the service %d outlived the stop", pid)
		}
		if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State != ledger.TreeEnded {
			t.Fatalf("the worktree after the stop returned: %+v", tr)
		}
		if p := l.payloads(run.ID, "service.ended"); len(p) != 1 || p[0]["why"] != "the run was stopped" || p[0]["signal"] != "terminated" {
			t.Fatalf("service.ended: %v", p)
		}
		if r, _ := e.Get(run.ID); r.Cleaning != 0 {
			t.Fatalf("cleaning %d after the stop", r.Cleaning)
		}
	})
	t.Run("under a context already short", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		held, release := make(chan string, 2), make(chan struct{})
		e.beforeTreeEnd = func(kind string) {
			held <- kind
			<-release
		}
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		pid := pidIn(t, stateFile(a.Attempt.Worktree, "web.pid"))
		short, cancel := context.WithCancel(context.Background())
		cancel()
		_ = e.Stop(short, run.ID)
		if alive(pid) {
			t.Fatalf("the service %d outlived a stop under an ended context", pid)
		}
		if r, _ := e.Get(run.ID); r.StoppedAt == nil || r.Cleaning != 1 {
			t.Fatalf("the run after the stop: stopped %v, cleaning %d", r.StoppedAt, r.Cleaning)
		}
		if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State == ledger.TreeEnded {
			t.Fatalf("the worktree ended before its end was let through: %+v", tr)
		}
		<-held
		close(release)
		drain, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		e.Drain(drain)
		if r, _ := e.Get(run.ID); r.Cleaning != 0 {
			t.Fatalf("cleaning %d after Drain", r.Cleaning)
		}
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	})
}

// A stop that comes while a service is being started owns it all the same
// (Codex's review of #441, round 3). Its process made and the executor
// held before it hands the service over, a stop under a context already
// ended sees the start under way, waits for the service and kills it
// before it returns, so no process of the run outlives the stop that
// returned; the service's end is recorded with its tree's, why the run
// was stopped, its row finished with its pid. A start the stop came
// before (held once its row is committed, before it is registered on the
// tree) makes no process at all: its row is finished with why, and no
// service.ended follows.
func TestAStopOwnsAServiceBeingStarted(t *testing.T) {
	// held is a hold a test opens once, and its opener.
	held := func(t *testing.T) (<-chan struct{}, func()) {
		hold, open := make(chan struct{}), false
		release := func() {
			if !open {
				open = true
				close(hold)
			}
		}
		t.Cleanup(release)
		return hold, release
	}
	m := Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}}
	t.Run("its process made", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		native := e.exec.serve
		made := make(chan int, 1)
		hold, release := held(t)
		e.exec.serve = func(ctx context.Context, s proc.Spec) (service, error) {
			svc, err := native(ctx, s)
			if err == nil {
				made <- svc.Result().Process.PID
			}
			<-hold
			return svc, err
		}
		awaiting := make(chan string, 1)
		e.awaitingStart = func(id string) { awaiting <- id }
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		var pid int
		select {
		case pid = <-made:
		case <-time.After(20 * time.Second):
			t.Fatal("the service's process was not made after 20 s")
		}
		if !alive(pid) {
			t.Fatalf("the service %d is not running as it is handed over", pid)
		}
		short, cancel := context.WithCancel(context.Background())
		cancel()
		outlived := make(chan bool, 1)
		go func() {
			_ = e.Stop(short, run.ID)
			outlived <- alive(pid)
		}()
		select {
		case live := <-outlived:
			t.Fatalf("the stop returned before the service being started was handed over (its process alive: %v)", live)
		case id := <-awaiting:
			if id != "web" {
				t.Fatalf("the stop waited for the start of %q", id)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the stop did not wait for the service being started after 20 s")
		}
		release()
		select {
		case live := <-outlived:
			if live {
				t.Fatalf("the service %d outlived the stop", pid)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the stop did not return after 20 s")
		}
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
		waitTreesEnded(t, e, run.ID)
		if p := l.payloads(run.ID, "service.ended"); len(p) != 1 || p[0]["why"] != "the run was stopped" {
			t.Fatalf("service.ended: %v", p)
		}
		if web, ok := rowOf(l.stepRows(run.ID, "build", 1), "web", 1); !ok || web.State != ledger.CheckFinished || web.PID != pid || web.Error != "" {
			t.Fatalf("the service's row: %+v", web)
		}
	})
	t.Run("its process not made yet", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		native := e.exec.serve
		var served atomic.Int32
		e.exec.serve = func(ctx context.Context, s proc.Spec) (service, error) {
			served.Add(1)
			return native(ctx, s)
		}
		reached := make(chan struct{}, 1)
		hold, release := held(t)
		e.beforeServe = func(string) {
			reached <- struct{}{}
			<-hold
		}
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		select {
		case <-reached:
		case <-time.After(20 * time.Second):
			t.Fatal("the service's start did not reach its process after 20 s")
		}
		short, cancel := context.WithCancel(context.Background())
		cancel()
		_ = e.Stop(short, run.ID)
		release()
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
		waitTreesEnded(t, e, run.ID)
		if n := served.Load(); n != 0 {
			t.Fatalf("%d processes made for a start the stop came before", n)
		}
		words := "web was not started: its tree's end had begun"
		if web, ok := rowOf(l.stepRows(run.ID, "build", 1), "web", 1); !ok || web.State != ledger.CheckFinished || web.PID != 0 || web.Error != words {
			t.Fatalf("the service's row: %+v", web)
		}
		if n := l.count(run.ID, "service.ended"); n != 0 {
			t.Fatalf("%d service.ended for a service never started", n)
		}
	})
}

// A stop the ledger refuses (its recordStop) has ended the services all
// the same, before it returns its refusal: the run is stopped, the
// service's pid is gone, no tree.ended is recorded (the tree is handed
// over by no commit: the recovery concludes it at the next start), and the
// run reads with no preview from the stop's first step on (read while the
// stop is ending the service, its stop held at a gate).
func TestAStopTheLedgerRefusesLeavesNoServiceRunning(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst)
	fl.probes.answer(url, http.StatusOK)
	stopping, openStops := gateStops(t, e)
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}},
		Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a.Attempt.Preview != url {
		t.Fatalf("the preview: %q", a.Attempt.Preview)
	}
	pid := pidIn(t, stateFile(a.Attempt.Worktree, "web.pid"))
	l.mu.Lock()
	l.failOn = "run.stopped"
	l.mu.Unlock()
	errc := make(chan error, 1)
	go func() { errc <- e.Stop(context.Background(), run.ID) }()
	select {
	case <-stopping:
	case <-time.After(20 * time.Second):
		t.Fatal("the stop did not reach the service after 20 s")
	}
	if p := previewOf(t, e, run.ID, "build"); p != "" {
		t.Fatalf("a preview while the run stops: %q", p)
	}
	openStops()
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "the ledger could not record the stop") {
		t.Fatalf("the stop's error: %v", err)
	}
	if alive(pid) {
		t.Fatalf("the service %d outlived a stop the ledger refused", pid)
	}
	r, _ := e.Get(run.ID)
	if r.StoppedAt == nil || r.Tasks[0].Attempt.Preview != "" {
		t.Fatalf("the run after the refused stop: stopped %v, preview %q", r.StoppedAt, r.Tasks[0].Attempt.Preview)
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State != ledger.TreeReady || l.count(run.ID, "tree.ended") != 0 {
		t.Fatalf("the worktree after the refused stop: %+v", tr)
	}
}

// A run whose tree is being ended (cleaning above zero) is not evicted,
// whatever the pressure: here its sole task cancelled, the run finished,
// and its tree's service being stopped (its stop held at a gate). Once the
// tree has ended, the run goes as any finished run does.
func TestARunWhoseTreeIsEndingIsNotEvicted(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	stopping, openStops := gateStops(t, e)
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}}, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopping:
	case <-time.After(20 * time.Second):
		t.Fatal("the tree's service was not being stopped after 20 s")
	}
	if r, _ := e.Get(run.ID); r.Outcome != ledger.OutcomeCancelled || r.Cleaning != 1 {
		t.Fatalf("the run while its tree ends: outcome %q, cleaning %d", r.Outcome, r.Cleaning)
	}
	maxRuns = 1
	other := testCrew(manual("lead", ""))
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the run was forgotten while its tree was being ended")
	}
	openStops()
	waitTreesEnded(t, e, run.ID)
	waitTrue(t, "the attempt's session ended", func() bool {
		r, _ := e.Get(run.ID)
		return r.Members[0].Status == MemberEnded
	})
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the finished run was kept past the bound once its tree had ended")
	}
}

// At the shutdown's cut-off (Draining: the deadline less teardownReserve)
// every run's teardown context ends: a service that ignores SIGTERM is
// killed then, with its group, its end recorded (the shutdown cut it) and
// the preview withdrawn, without a stop.
func TestTheShutdownsCutOffKillsTheServices(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst)
	fl.probes.answer(url, http.StatusOK)
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", true)}, Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}},
		Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	pid := pidIn(t, stateFile(a.Attempt.Worktree, "web.pid"))
	// A deadline the reserve takes all of: the cut-off is now.
	ctx, cancel := context.WithTimeout(context.Background(), teardownReserve)
	defer cancel()
	e.Draining(ctx)
	waitFake(t, "the service's end recorded", func() bool { return len(l.payloads(run.ID, "service.ended")) == 1 })
	if alive(pid) {
		t.Fatalf("the service %d outlived the cut-off", pid)
	}
	if p := l.payloads(run.ID, "service.ended")[0]; p["why"] != "the server's shutdown cut it" || p["signal"] != "killed" {
		t.Fatalf("service.ended: %v", p)
	}
	if p := previewOf(t, e, run.ID, "build"); p != "" {
		t.Fatalf("a preview once the service was cut: %q", p)
	}
}

// A service left running across the owner's end (a real ledger, closed
// with the service running) is reported by the recovery at the next start,
// never signalled: its row interrupted, Recovered.Services naming its pid,
// group and start, confirmed, in its tree; the process still alive.
func TestAServiceLeftRunningAcrossARestartIsReportedNotSignalled(t *testing.T) {
	dir := newRepo(t)
	e, _ := readyEngine(t)
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	e.UseLedger(SQLLedger(ld))
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}}, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	pid := pidIn(t, stateFile(a.Attempt.Worktree, "web.pid"))
	// The owner ends here: its ledger closed with the service running.
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	ctx := context.Background()
	rec, err := again.Recover(ctx)
	if err != nil || len(rec) != 1 || len(rec[0].Services) != 1 {
		t.Fatalf("the recovery: %+v %v", rec, err)
	}
	if sv := rec[0].Services[0]; sv.Task != "build" || sv.N != 1 || sv.Tree != ledger.TreeWorktree || sv.ID != "web" || sv.Pid != pid || sv.Group != pid || sv.StartedAt.IsZero() || !sv.Confirmed {
		t.Fatalf("the service reported: %+v (its pid %d)", sv, pid)
	}
	rows, _ := again.Checks(ctx, run.ID, "build", 1, false)
	if web, ok := rowOf(rows, "web", 1); !ok || web.State != ledger.CheckInterrupted {
		t.Fatalf("the service's row after the recovery: %+v", web)
	}
	if !alive(pid) {
		t.Fatalf("the service %d was signalled by the recovery", pid)
	}
}

// A crash between a tree's services' end and its own end (the engine ended
// at the seam after service.ended, the attempt closed, the run finished,
// the trees' rows ready): the recovery ends each tree, interrupted, with its
// tree.ended; the services' rows finished before are not reported again;
// the run read after has nothing cleaning.
func TestATreeLeftEndingAfterItsServicesIsEndedByTheRecovery(t *testing.T) {
	dir := newRepo(t)
	e, _ := readyEngine(t)
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	e.UseLedger(SQLLedger(ld))
	held, release := make(chan string, 2), make(chan struct{})
	e.beforeTreeEnd = func(kind string) {
		held <- kind
		<-release
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")}}, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	kinds := []string{<-held, <-held}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{ledger.TreeCheckout, ledger.TreeWorktree}) {
		t.Fatalf("the trees at the seam: %v", kinds)
	}
	snap, _ := e.Get(run.ID)
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	ctx := context.Background()
	rec, err := again.Recover(ctx)
	if err != nil || len(rec) != 1 || rec[0].Trees != 2 || len(rec[0].Services) != 0 || rec[0].Interrupted {
		t.Fatalf("the recovery: %+v %v", rec, err)
	}
	evs, _ := again.Events(ctx, run.ID, 0, 1000)
	ended := 0
	for _, ev := range evs {
		if ev.Kind == "service.ended" {
			ended++
		}
	}
	if ended != 2 {
		t.Fatalf("%d service.ended events: the services' ends before the crash, and none after", ended)
	}
	from, ok, err := SQLLedger(again).Load(ctx, run.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if read := Recovered(snap, from); read.Cleaning != 0 || read.State != RunFinished {
		t.Fatalf("the run read after: cleaning %d, %s", read.Cleaning, read.State)
	}
	// The trees held at the seam end now, on the closed ledger, as the
	// harness's end of the engine waits for them.
	close(release)
}
