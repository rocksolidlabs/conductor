package crew

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

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The manifest's verify steps and cleanup steps (EN1 PR-5): a verify step
// runs in the checkout of a result after the task's checks, as a check of
// the crew's; a tree's cleanup steps run once it ends, however it ends,
// after its services are stopped.

// eventAt is the index among the run's events of the first of the kind
// whose payload ok takes (nil takes any), -1 for none.
func (l *fakeLedger) eventAt(runID, kind string, ok func(map[string]any) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := 0
	for _, ev := range l.events {
		if ev.runID != runID {
			continue
		}
		p, _ := ev.payload.(map[string]any)
		if ev.kind == kind && (ok == nil || ok(p)) {
			return i
		}
		i++
	}
	return -1
}

// lastAt is the index among the run's events of the last of the kind
// whose payload ok takes (nil takes any), -1 for none.
func (l *fakeLedger) lastAt(runID, kind string, ok func(map[string]any) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, i := -1, 0
	for _, ev := range l.events {
		if ev.runID != runID {
			continue
		}
		p, _ := ev.payload.(map[string]any)
		if ev.kind == kind && (ok == nil || ok(p)) {
			at = i
		}
		i++
	}
	return at
}

// stepOf takes a step event of the phase and id in the tree.
func stepOf(phase, id, tree string) func(map[string]any) bool {
	return func(p map[string]any) bool { return p["phase"] == phase && p["id"] == id && p["tree"] == tree }
}

// ofTree takes an event of the tree kind.
func ofTree(tree string) func(map[string]any) bool {
	return func(p map[string]any) bool { return p["tree"] == tree }
}

// logHas reports whether the run log has the line.
func logHas(e *Engine, runID, line string) bool {
	r, _ := e.Get(runID)
	return slices.ContainsFunc(r.Log, func(en session.ActivityEntry) bool { return en.Message == line })
}

// logLines is the run log's lines, for a failure's message.
func logLines(e *Engine, runID string) string {
	r, _ := e.Get(runID)
	var out []string
	for _, en := range r.Log {
		out = append(out, en.Message)
	}
	return strings.Join(out, "\n")
}

// rowWhen waits up to 10 s for the receipt of id at k on the attempt to
// satisfy ok, and returns it.
func (l *fakeLedger) rowWhen(t *testing.T, runID, taskID string, n int, id string, k int, ok func(ledger.CheckRow) bool) ledger.CheckRow {
	t.Helper()
	var row ledger.CheckRow
	waitFake(t, fmt.Sprintf("%s k%d of %s's attempt %d", id, k, taskID, n), func() bool {
		var found bool
		row, found = rowOf(l.stepRows(runID, taskID, n), id, k)
		return found && ok(row)
	})
	return row
}

// A verify step runs in the checkout of a result after the task's checks,
// as a check of the crew's: a row of phase verify with the manifest that
// ran it, in the checkout, at the result, compared after it; its events
// are the steps', after the checks' and before the result is taken.
func TestAVerifyStepRunsAfterTheChecks(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "test -e built.txt")}
	m := Manifest{Verify: []Check{shStep("e2e", `test -e built.txt && echo verified`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	result := taskOf(t, e, run.ID, "build").Result.Commit
	rows := l.stepRows(run.ID, "build", 1)
	unit, ok1 := rowOf(rows, "unit", 1)
	e2e, ok2 := rowOf(rows, "e2e", 1)
	if !ok1 || !ok2 {
		t.Fatalf("the attempt's receipts:\n%s", rowWords(rows))
	}
	checkout := a.Attempt.Worktree + ".check"
	if unit.Phase != ledger.PhaseCheck || unit.Manifest != "" {
		t.Fatalf("the task's check: %+v", unit)
	}
	if e2e.Phase != ledger.PhaseVerify || e2e.Manifest != "crew:0" || e2e.Dir != checkout || e2e.Subject != result || e2e.State != ledger.CheckFinished || e2e.Exit != 0 ||
		!e2e.Stable || e2e.Error != "" || string(e2e.Log) != "verified\n" || e2e.LogSHA256 != sha(e2e.Log) || e2e.PID == 0 {
		t.Fatalf("the verify step's row: %+v", e2e)
	}
	checked := l.eventAt(run.ID, "check.finished", func(p map[string]any) bool { return p["check"] == "unit" })
	started := l.eventAt(run.ID, "step.started", stepOf(ledger.PhaseVerify, "e2e", ledger.TreeCheckout))
	finished := l.eventAt(run.ID, "step.finished", stepOf(ledger.PhaseVerify, "e2e", ledger.TreeCheckout))
	succeeded := l.eventAt(run.ID, "task.succeeded", nil)
	if checked < 0 || started < checked || finished < started || succeeded < finished {
		t.Fatalf("the ledger's order: %v", l.kinds(run.ID))
	}
	if p := l.payloads(run.ID, "step.started"); !slices.ContainsFunc(p, func(p map[string]any) bool {
		return p["phase"] == ledger.PhaseVerify && p["manifest"] == "crew:0" && p["subject"] == result && p["task"] == "build" && p["attempt"] == 1
	}) {
		t.Fatalf("no step.started of the verify step: %v", p)
	}
}

// A verify step that fails is a failing check: the attempt fails at
// "verify <id>", counted, the run not paused, and the next attempt starts
// at the result with the step's words, its tail among them, in its task
// file. One that changes tracked content beside a service fails at it,
// the service named as the other possible author.
func TestAVerifyStepThatFailsIsAFailingCheck(t *testing.T) {
	t.Run("its exit", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		m := Manifest{Verify: []Check{shStep("e2e", `echo "the e2e suite broke here"; exit 3`)}}
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "yes\n", "built")
		a2 := attemptN(t, e, run.ID, "build", 2)
		row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
		if row.Stage != "verify e2e" || !strings.Contains(row.Error, "exited 3: the e2e suite broke here") || row.ResultCommit == "" {
			t.Fatalf("attempt 1: %+v", row)
		}
		if a2.Attempt.Start != row.ResultCommit {
			t.Fatalf("attempt 2 starts at %s, not attempt 1's result %s", short(a2.Attempt.Start), short(row.ResultCommit))
		}
		_, f := taskFileOf(t, a2)
		if f.Previous == nil || f.Previous.Stage != "verify e2e" || !strings.Contains(f.Previous.Error, "the e2e suite broke here") {
			t.Fatalf("attempt 2's task file: %+v", f.Previous)
		}
		if p := l.payloads(run.ID, "attempt.failed"); len(p) != 1 || p[0]["uncounted"] != nil {
			t.Fatalf("attempt.failed: %v", p)
		}
		if r, _ := e.Get(run.ID); r.Paused {
			t.Fatal("a failing verify step paused the run")
		}
		e2e, _ := rowOf(l.stepRows(run.ID, "build", 1), "e2e", 1)
		if e2e.Phase != ledger.PhaseVerify || e2e.Exit != 3 || !strings.Contains(e2e.Error, "exited 3") || e2e.Tail != "the e2e suite broke here\n" {
			t.Fatalf("the verify step's row: %+v", e2e)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("a change to tracked content beside a service", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		m := Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")},
			Verify: []Check{shStep("e2e", `echo changed >> README`)}}
		run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "yes\n", "built")
		attemptN(t, e, run.ID, "build", 2)
		row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
		if want := "verify e2e, or a service of the tree (web), changed tracked files: README"; row.Stage != "verify e2e" || row.Error != want {
			t.Fatalf("attempt 1: stage %q, %q", row.Stage, row.Error)
		}
		if e2e, _ := rowOf(l.stepRows(run.ID, "build", 1), "e2e", 1); e2e.Stable || e2e.Exit != 0 {
			t.Fatalf("the verify step's row: %+v", e2e)
		}
		stopRun(t, e, run.ID)
	})
}

// cleanedMark is a cleanup step that writes to cleaned in the tree's state
// directory whether the service whose pid is in web.pid still ran.
var cleanedMark = shStep("note", `if kill -0 "$(cat "$CONDUCTOR_STATE_DIR/web.pid")" 2>/dev/null; then echo alive; else echo gone; fi > "$CONDUCTOR_STATE_DIR/cleaned"`)

// A tree's cleanup steps run once it ends, in each tree, after its services
// are stopped: after a result taken (the ledger's order: the checks, the
// task's success, then each tree's cleanup and its tree.ended, once per
// tree) and after a failed check (the attempt's state as its close left
// it). Each step is a row of phase cleanup in its tree, with the manifest
// and the tree's ports; every step runs, in order.
func TestATreesCleanupRunsOnceItEnds(t *testing.T) {
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")},
		Cleanup: []Check{cleanedMark, shStep("tidy", `echo tidied`)}}
	for _, tc := range []struct {
		name, check string
		state       string
	}{
		{"a result taken", "true", ledger.AttemptSucceeded},
		{"a failed check", "exit 1", ledger.AttemptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", tc.check)}
			run := launchManaged(t, e, repoManifestCrew(dir, m, build))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
			wt := a.Attempt.Worktree
			for tree, dir := range map[string]string{ledger.TreeWorktree: wt, ledger.TreeCheckout: wt + ".check"} {
				b, err := os.ReadFile(stateFile(dir, "cleaned"))
				if err != nil || string(b) != "gone\n" {
					t.Fatalf("the %s's cleanup saw its service %q (%v)", tree, b, err)
				}
				ended := l.eventAt(run.ID, "service.ended", ofTree(tree))
				noted := l.eventAt(run.ID, "step.started", stepOf(ledger.PhaseCleanup, "note", tree))
				noteDone := l.eventAt(run.ID, "step.finished", stepOf(ledger.PhaseCleanup, "note", tree))
				tidied := l.eventAt(run.ID, "step.started", stepOf(ledger.PhaseCleanup, "tidy", tree))
				treeEnded := l.eventAt(run.ID, "tree.ended", ofTree(tree))
				if ended < 0 || noted < ended || tidied < noteDone || treeEnded < l.eventAt(run.ID, "step.finished", stepOf(ledger.PhaseCleanup, "tidy", tree)) {
					t.Fatalf("the %s's end in the ledger: %v", tree, l.kinds(run.ID))
				}
			}
			rows := l.stepRows(run.ID, "build", 1)
			// The two trees end side by side: each step's k is the order its
			// rows were first put in, so each is found by its tree.
			for _, want := range []struct{ id, dir string }{{"note", wt}, {"tidy", wt}, {"note", wt + ".check"}, {"tidy", wt + ".check"}} {
				var row ledger.CheckRow
				for _, c := range rows {
					if c.ID == want.id && c.Dir == want.dir {
						row = c
					}
				}
				if row.Phase != ledger.PhaseCleanup || row.State != ledger.CheckFinished || row.Exit != 0 || row.Error != "" || row.Manifest != "crew:0" || len(row.Ports) != 1 || row.Ports["web"] == 0 {
					t.Fatalf("the cleanup step %s in %s:\n%s", want.id, want.dir, rowWords(rows))
				}
			}
			var ends []map[string]any
			for _, p := range l.payloads(run.ID, "tree.ended") {
				if p["attempt"] == 1 {
					ends = append(ends, p)
				}
			}
			if p := ends; len(p) != 2 || p[0]["tree"] == p[1]["tree"] || p[0]["cleanupFailed"] != nil || p[1]["cleanupFailed"] != nil || p[0]["interrupted"] != nil {
				t.Fatalf("tree.ended, once per tree: %v", p)
			}
			if tc.state == ledger.AttemptSucceeded {
				succeeded := l.eventAt(run.ID, "task.succeeded", nil)
				checked := l.lastAt(run.ID, "check.finished", nil)
				first := l.eventAt(run.ID, "tree.ended", nil)
				if checked < 0 || succeeded < checked || first < succeeded {
					t.Fatalf("the ledger's order on a result taken: %v", l.kinds(run.ID))
				}
			}
			if row := l.attemptRow(t, run.ID, "build", 1); row.State != tc.state {
				t.Fatalf("attempt 1 after its trees' cleanup: %+v", row)
			}
			waitTreesEnded(t, e, run.ID)
			if r, _ := e.Get(run.ID); r.Cleaning != 0 {
				t.Fatalf("cleaning %d once the trees ended", r.Cleaning)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A cleanup step that fails is a receipt and a line in the run log, never
// a change to the attempt's state; the steps after it run all the same,
// and tree.ended names the first that failed.
func TestACleanupStepThatFailsIsAReceiptAndALogLine(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	m := Manifest{Cleanup: []Check{shStep("db", `echo "the database would not drop"; exit 1`), shStep("tidy", `echo tidied`), shStep("cache", "exit 2")}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	rows := l.stepRows(run.ID, "build", 1)
	db, _ := rowOf(rows, "db", 1)
	tidy, _ := rowOf(rows, "tidy", 1)
	cache, _ := rowOf(rows, "cache", 1)
	if db.Phase != ledger.PhaseCleanup || db.Exit != 1 || db.Error != "db exited 1: the database would not drop" || tidy.Exit != 0 || tidy.Error != "" || cache.Exit != 2 {
		t.Fatalf("the worktree's cleanup:\n%s", rowWords(rows))
	}
	for _, p := range l.payloads(run.ID, "tree.ended") {
		if p["cleanupFailed"] != "db" {
			t.Fatalf("tree.ended: %v", p)
		}
	}
	for _, line := range []string{
		"task build: attempt 1: cleanup db exited 1: the database would not drop",
		"task build: attempt 1: cleanup cache exited 2",
		"task build: attempt 1: the checkout's cleanup db exited 1: the database would not drop",
	} {
		if !logHas(e, run.ID, line) {
			t.Fatalf("the run log has no %q:\n%s", line, logLines(e, run.ID))
		}
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptSucceeded || row.Stage != "" || row.Error != "" {
		t.Fatalf("attempt 1 after a cleanup that failed: %+v", row)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskSucceeded {
		t.Fatalf("the task after a cleanup that failed: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A cancel mid-step: the setup step, a shell that ignores SIGTERM, is
// killed by the cancel's commit with its group and recorded interrupted;
// the cleanup's row starts only after it, and the cleanup runs under its
// own context, to its end.
func TestACancelMidStepRunsTheCleanupAfterTheStep(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate, pidFile := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() { open(t, gate) })
	m := Manifest{Setup: []Check{shStep("deps", `trap "" TERM; echo $$ > "`+pidFile+`"; `+gated(gate))}, Cleanup: []Check{shStep("tidy", `echo tidied`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	pid := pidIn(t, pidFile)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	if alive(pid) {
		t.Fatalf("the setup step's process %d outlived the cancel", pid)
	}
	rows := l.stepRows(run.ID, "build", 1)
	deps, _ := rowOf(rows, "deps", 1)
	tidy, ok := rowOf(rows, "tidy", 1)
	if deps.State != ledger.CheckInterrupted || !ok || tidy.Phase != ledger.PhaseCleanup || tidy.State != ledger.CheckFinished || tidy.Exit != 0 || string(tidy.Log) != "tidied\n" {
		t.Fatalf("the steps:\n%s", rowWords(rows))
	}
	interrupted := l.eventAt(run.ID, "step.finished", stepOf(ledger.PhaseSetup, "deps", ledger.TreeWorktree))
	started := l.eventAt(run.ID, "step.started", stepOf(ledger.PhaseCleanup, "tidy", ledger.TreeWorktree))
	ended := l.eventAt(run.ID, "tree.ended", nil)
	if interrupted < 0 || started < interrupted || ended < started || l.count(run.ID, "tree.ended") != 1 {
		t.Fatalf("the ledger's order: %v", l.kinds(run.ID))
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled {
		t.Fatalf("the task: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// A stop mid-service: the stop returns once the service's pid is gone and
// the tree's cleanup and tree.ended are committed. Under a context already
// short it returns with cleaning above zero, the cleanup going on under
// the run's teardown (held here at a gate) and the tree ending after, which
// Drain waits for.
func TestAStopMidServiceRunsTheCleanup(t *testing.T) {
	t.Run("with time to finish", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		m := Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")}, Cleanup: []Check{cleanedMark}}
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
		if note, _ := rowOf(l.stepRows(run.ID, "build", 1), "note", 1); note.Phase != ledger.PhaseCleanup || note.State != ledger.CheckFinished || note.Exit != 0 {
			t.Fatalf("the cleanup step after the stop returned: %+v", note)
		}
		if b, err := os.ReadFile(stateFile(a.Attempt.Worktree, "cleaned")); err != nil || string(b) != "gone\n" {
			t.Fatalf("the cleanup saw the service %q (%v)", b, err)
		}
		if r, _ := e.Get(run.ID); r.Cleaning != 0 {
			t.Fatalf("cleaning %d after the stop", r.Cleaning)
		}
	})
	t.Run("under a context already short", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		gate := filepath.Join(t.TempDir(), "gate")
		t.Cleanup(func() { open(t, gate) })
		m := Manifest{Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")}, Cleanup: []Check{shStep("slow", gated(gate))}}
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
		l.rowWhen(t, run.ID, "build", 1, "slow", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
		if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State == ledger.TreeEnded {
			t.Fatalf("the worktree ended under its cleanup step: %+v", tr)
		}
		open(t, gate)
		// Drain under no deadline of the test's own: it returns once the
		// tree has ended, a hang left to the test's timeout (the
		// independent review of #498).
		e.Drain(context.Background())
		if r, _ := e.Get(run.ID); r.Cleaning != 0 {
			t.Fatalf("cleaning %d after Drain", r.Cleaning)
		}
		l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
		if slow, _ := rowOf(l.stepRows(run.ID, "build", 1), "slow", 1); slow.State != ledger.CheckFinished || slow.Exit != 0 {
			t.Fatalf("the cleanup step: %+v", slow)
		}
	})
}

// The attempt's preview is gone once its close is committed: a cleanup step
// held at a gate after the services ended is read with no preview, the
// run cleaning, the port still the tree's until tree.ended; and once the
// tree has ended, still none, its port let go.
func TestThePreviewIsGoneWhileACleanupStepIsHeld(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	gate := filepath.Join(t.TempDir(), "gate")
	t.Cleanup(func() { open(t, gate) })
	url := fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst)
	fl.probes.answer(url, http.StatusOK)
	fl.probes.answer(fmt.Sprintf("http://127.0.0.1:%d/", fakePortsFirst+1), http.StatusOK)
	m := Manifest{Ports: []string{"web"}, Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", IntervalSeconds: 1}},
		Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", Cleanup: []Check{shStep("slow", gated(gate))}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a.Attempt.Preview != url {
		t.Fatalf("the preview while the attempt runs: %q", a.Attempt.Preview)
	}
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	waitFake(t, "the worktree's cleanup step running", func() bool {
		return slices.ContainsFunc(l.stepRows(run.ID, "build", 1), func(c ledger.CheckRow) bool {
			return c.ID == "slow" && c.Dir == a.Attempt.Worktree && c.State == ledger.CheckRunning
		})
	})
	if l.eventAt(run.ID, "service.ended", ofTree(ledger.TreeWorktree)) < 0 {
		t.Fatalf("the worktree's cleanup runs before its services ended: %v", l.kinds(run.ID))
	}
	if p := previewOf(t, e, run.ID, "build"); p != "" {
		t.Fatalf("a preview while the tree's cleanup runs: %q", p)
	}
	if r, _ := e.Get(run.ID); r.Cleaning == 0 {
		t.Fatal("the run is not cleaning while a cleanup step runs")
	}
	if got := leases(e)[fakePortsFirst]; got != "build.1 worktree" {
		t.Fatalf("the worktree's port while its cleanup runs: %q", got)
	}
	open(t, gate)
	waitTreesEnded(t, e, run.ID)
	if p := previewOf(t, e, run.ID, "build"); p != "" {
		t.Fatalf("a preview after tree.ended: %q", p)
	}
	if got := leases(e); len(got) != 0 {
		t.Fatalf("ports leased after the trees ended: %v", got)
	}
	stopRun(t, e, run.ID)
}

// At the shutdown's cut-off (Draining) a cleanup step under way is killed
// with its group, its row interrupted with why, and no step starts after
// it: tree.ended says interrupted, the tree's row too, and the run log
// names what was not run. Drain then finds every tree ended.
func TestTheShutdownsCutOffInterruptsACleanupStep(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	m := Manifest{Cleanup: []Check{shStep("slow", `trap "" TERM; echo $$ > "$CONDUCTOR_STATE_DIR/slow.pid"; while :; do sleep 0.05; done`), shStep("after", "true")}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	wt, ck := a.Attempt.Worktree, a.Attempt.Worktree+".check"
	pids := []int{pidIn(t, stateFile(wt, "slow.pid")), pidIn(t, stateFile(ck, "slow.pid"))}
	for _, k := range []int{1, 2} {
		l.rowWhen(t, run.ID, "build", 1, "slow", k, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
	}
	// A deadline the reserve takes all of: the cut-off is now.
	ctx, cancel := context.WithTimeout(context.Background(), teardownReserve)
	defer cancel()
	e.Draining(ctx)
	drain, done := context.WithTimeout(context.Background(), 20*time.Second)
	defer done()
	e.Drain(drain)
	waitTreesEnded(t, e, run.ID)
	for _, pid := range pids {
		if alive(pid) {
			t.Fatalf("the cleanup step %d outlived the cut-off", pid)
		}
	}
	rows := l.stepRows(run.ID, "build", 1)
	for _, k := range []int{1, 2} {
		if slow, _ := rowOf(rows, "slow", k); slow.State != ledger.CheckInterrupted || slow.Error != "slow was interrupted: the server's shutdown cut it" {
			t.Fatalf("the cleanup step cut:\n%s", rowWords(rows))
		}
	}
	if _, ran := rowOf(rows, "after", 1); ran {
		t.Fatalf("a cleanup step started after the cut-off:\n%s", rowWords(rows))
	}
	for _, kind := range []string{ledger.TreeWorktree, ledger.TreeCheckout} {
		if tr, _ := l.treeRow(run.ID, "build", 1, kind); tr.State != ledger.TreeEnded || !tr.Interrupted {
			t.Fatalf("the %s's row: %+v", kind, tr)
		}
	}
	for _, p := range l.payloads(run.ID, "tree.ended") {
		if p["interrupted"] != true || p["cleanupFailed"] != nil {
			t.Fatalf("tree.ended: %v", p)
		}
	}
	for _, line := range []string{
		"task build: attempt 1: cleanup slow was interrupted: the server's shutdown cut it",
		"task build: attempt 1: cleanup after not run: the server's shutdown cut it",
		"task build: attempt 1: the checkout's cleanup after not run: the server's shutdown cut it",
	} {
		if !logHas(e, run.ID, line) {
			t.Fatalf("the run log has no %q:\n%s", line, logLines(e, run.ID))
		}
	}
}

// A withdrawn attempt's tree ends as a cancelled one's does: the review
// asks changes of build, docs' attempt in flight is withdrawn, and its
// worktree's cleanup runs, its tree ended and its port let go.
func TestAWithdrawnAttemptsTreeIsCleanedUp(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	m := Manifest{Ports: []string{"web"}, Cleanup: []Check{shStep("tidy", `echo "tidied $CONDUCTOR_TASK $CONDUCTOR_ATTEMPT on $CONDUCTOR_PORT_WEB"`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead"), review("check", "critic", "build"), work("docs", "writer", "build")))
	b1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, b1, "built.txt", "built\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rv := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	d1 := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	port := 0
	waitFake(t, "docs' port leased", func() bool {
		for n, who := range leases(e) {
			if who == "docs.1 worktree" {
				port = n
			}
		}
		return port != 0
	})
	asksChanges(t, e, rv)
	l.waitTreeRowsEnded(t, run.ID, "docs", 1, ledger.TreeWorktree)
	if row := l.attemptRow(t, run.ID, "docs", 1); row.State != ledger.AttemptCancelled || row.Stage != "withdrawn" {
		t.Fatalf("docs' attempt 1: %+v", row)
	}
	tidy, ok := rowOf(l.stepRows(run.ID, "docs", 1), "tidy", 1)
	if want := fmt.Sprintf("tidied docs 1 on %d\n", port); !ok || tidy.Phase != ledger.PhaseCleanup || tidy.Dir != d1.Attempt.Worktree || string(tidy.Log) != want || tidy.Exit != 0 {
		t.Fatalf("docs' cleanup: %+v (want the log %q)", tidy, want)
	}
	waitTreesEnded(t, e, run.ID)
	if who, held := leases(e)[port]; held && strings.HasPrefix(who, "docs.1") {
		t.Fatalf("docs' attempt 1's port %d still leased after its tree ended", port)
	}
	stopRun(t, e, run.ID)
}

// A finished run whose tree's cleanup is held at a gate is cleaning: the
// engine does not forget it under eviction pressure, and the ledger (a real
// one) refuses to prune it while the tree's row is not ended; once the
// cleanup has run and the tree ended, both let it go.
func TestAFinishedRunCleaningUpIsNeitherForgottenNorPruned(t *testing.T) {
	dir := newRepo(t)
	e, fl := readyEngine(t)
	ld, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld.Close() })
	e.UseLedger(SQLLedger(ld))
	old := maxRuns
	t.Cleanup(func() { maxRuns = old })
	gate := filepath.Join(t.TempDir(), "gate")
	t.Cleanup(func() { open(t, gate) })
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Cleanup: []Check{shStep("slow", gated(gate))}}, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e, run.ID, ledger.OutcomeSucceeded)
	ctx := context.Background()
	waitFake(t, "the cleanup step running", func() bool {
		rows, _ := ld.Checks(ctx, run.ID, "build", 1, false)
		row, ok := rowOf(rows, "slow", 1)
		return ok && row.State == ledger.CheckRunning
	})
	if err := ld.Prune(ctx, run.ID); !errors.Is(err, ledger.ErrNotPrunable) {
		t.Fatalf("a prune of the run while its tree's cleanup runs: %v", err)
	}
	// The succeeded attempt's session stays idle (R4), which would hold the
	// run by itself: ended, the tree's end is all that holds it.
	s, _ := fl.attemptSession(t, a)
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the attempt's session ended", func() bool {
		r, _ := e.Get(run.ID)
		return r.Members[0].Status == MemberEnded
	})
	maxRuns = 1
	other := testCrew(manual("lead", ""))
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the run was forgotten while its tree's cleanup ran")
	}
	open(t, gate)
	waitTreesEnded(t, e, run.ID)
	trees, err := ld.Trees(ctx, run.ID)
	if err != nil || len(trees) != 2 || trees[0].State != ledger.TreeEnded || trees[1].State != ledger.TreeEnded {
		t.Fatalf("the trees once their cleanup ran: %+v %v", trees, err)
	}
	if _, err := e.Launch(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the finished run was kept past the bound once its trees had ended")
	}
	if err := ld.Prune(ctx, run.ID); err != nil {
		t.Fatalf("a prune of the finished run once its trees ended: %v", err)
	}
}

// A setup step that fails in the worktree closes the attempt uncounted and
// pauses the run; the tree it could not make usable is cleaned up all the
// same, its cleanup run after the failed step's row, the attempt's state
// and the pause as the failure left them.
func TestACleanupRunsAfterAFailedSetupStep(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	m := Manifest{Setup: []Check{shStep("deps", `echo "no registry"; exit 1`)}, Cleanup: []Check{shStep("tidy", `echo tidied`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	rows := l.stepRows(run.ID, "build", 1)
	deps, _ := rowOf(rows, "deps", 1)
	tidy, ok := rowOf(rows, "tidy", 1)
	if deps.Exit != 1 || !ok || tidy.Phase != ledger.PhaseCleanup || tidy.State != ledger.CheckFinished || tidy.Exit != 0 || string(tidy.Log) != "tidied\n" {
		t.Fatalf("the steps:\n%s", rowWords(rows))
	}
	failed := l.eventAt(run.ID, "step.finished", stepOf(ledger.PhaseSetup, "deps", ledger.TreeWorktree))
	if started := l.eventAt(run.ID, "step.started", stepOf(ledger.PhaseCleanup, "tidy", ledger.TreeWorktree)); failed < 0 || started < failed {
		t.Fatalf("the ledger's order: %v", l.kinds(run.ID))
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptFailed || row.Stage != "setup deps" {
		t.Fatalf("attempt 1 after its tree's cleanup: %+v", row)
	}
	if r, _ := e.Get(run.ID); !r.Paused || !strings.Contains(r.PausedWhy, "could not be made usable at setup deps") {
		t.Fatalf("the run after its tree's cleanup: paused %v, %q", r.Paused, r.PausedWhy)
	}
	stopRun(t, e, run.ID)
}

// A close that comes after a worktree is the attempt's and before it is
// ready to work in (its state directory, its task files) ends it only once
// that is done: the tree is held from its resolution on, so its cleanup
// finds what the dispatch made, and nothing is made after the cleanup ran
// (Codex's review of #498). The dispatch is held at a seam between the
// tree's commit and its state directory while the cancel is committed.
func TestACancelBeforeATreeIsReadyEndsItOnceItIsMade(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var resumed atomic.Bool
	early := make(chan string, 2)
	e.afterTreeResolved = func(kind string) {
		if kind != ledger.TreeWorktree {
			return
		}
		once.Do(func() {
			close(reached)
			<-resume
			resumed.Store(true)
		})
	}
	e.beforeTreeEnd = func(kind string) {
		if !resumed.Load() {
			early <- kind
		}
	}
	m := Manifest{Cleanup: []Check{shStep("look", `test -d "$CONDUCTOR_STATE_DIR" && test -f .conductor/task.md && echo made`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the worktree was not resolved after 10 s")
	}
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		close(resume)
		t.Fatal(err)
	}
	l.attemptWhen(t, run.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptCancelled })
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State == ledger.TreeEnded {
		close(resume)
		t.Fatalf("the worktree ended before it was made: %+v", tr)
	}
	close(resume)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	select {
	case kind := <-early:
		t.Fatalf("the %s's end began before the dispatch had made it", kind)
	default:
	}
	look, ok := rowOf(l.stepRows(run.ID, "build", 1), "look", 1)
	if !ok || look.Phase != ledger.PhaseCleanup || look.Exit != 0 || string(look.Log) != "made\n" {
		t.Fatalf("the cleanup found the tree unmade: %+v", look)
	}
	for _, p := range l.payloads(run.ID, "tree.ended") {
		if p["cleanupFailed"] != nil || p["interrupted"] != nil {
			t.Fatalf("tree.ended: %v", p)
		}
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled {
		t.Fatalf("the task: %+v", ts)
	}
	waitTreesEnded(t, e, run.ID)
	stopRun(t, e, run.ID)
}

// checkKinds are the two kinds of check runCheck runs in a result's
// checkout: a task's check and the manifest's verify step, each with the
// crew it is in (a manifest, so the checkout is a tree), the event of its running row and the words of a refused
// one and of an interrupted one.
var checkKinds = []struct {
	name, running, stage, refused, interrupted string
	crew                                       func(dir string, ch Check) Crew
}{
	{
		name: "a task's check", running: "check.running", stage: "check e2e",
		refused: "the ledger could not record the check: ledger: disk full (injected)", interrupted: "the attempt closed under the check",
		crew: func(dir string, ch Check) Crew {
			build := work("build", "lead")
			build.Checks = []Check{ch}
			return repoManifestCrew(dir, Manifest{Cleanup: []Check{shStep("tidy", "true")}}, build)
		},
	},
	{
		name: "a verify step", running: "step.running[phase=verify]", stage: "verify e2e",
		refused: "the ledger could not record the step e2e: ledger: disk full (injected)", interrupted: "e2e was interrupted: the attempt was cancelled",
		crew: func(dir string, ch Check) Crew {
			return repoManifestCrew(dir, Manifest{Verify: []Check{ch}}, work("build", "lead"))
		},
	},
}

// A check's running row the ledger refuses ends its process with its group
// and fails the check with the ledger's words, as a manifest step's does
// (runStep): nothing runs that the ledger does not record, and the result
// is not taken on a check whose process the ledger never had (Codex's
// review of #498).
func TestARefusedRunningRowEndsAndFailsTheCheck(t *testing.T) {
	for _, kind := range checkKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			l.mu.Lock()
			l.failOn, l.failOnce = kind.running, true
			l.mu.Unlock()
			run := launchManaged(t, e, kind.crew(dir, shStep("e2e", gated(gate))))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
			if row.Stage != kind.stage || row.Error != kind.refused {
				t.Fatalf("attempt 1: stage %q, %q", row.Stage, row.Error)
			}
			e2e, _ := rowOf(l.stepRows(run.ID, "build", 1), "e2e", 1)
			if e2e.State != ledger.CheckFinished || e2e.Error != kind.refused || e2e.PID == 0 || e2e.Stable {
				t.Fatalf("the check's row: %+v", e2e)
			}
			if alive(e2e.PID) {
				t.Fatalf("the check's process %d outlived its refused row", e2e.PID)
			}
			if ts := taskOf(t, e, run.ID, "build"); ts.State == ledger.TaskSucceeded {
				t.Fatalf("the result was taken on a check the ledger never had running: %+v", ts)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A verify step's finished row the ledger refuses once is kept for the
// next commit, as a manifest step's is (runStep): the attempt fails at the
// step with the ledger's words, and the step's receipt is there finished,
// its log and hash with it, never left running (Codex's review of #498).
func TestARefusedFinishedRowOfAVerifyStepIsKept(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	l.mu.Lock()
	l.failOn, l.failOnce = "step.finished[phase=verify]", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Verify: []Check{shStep("e2e", `echo "all green"`)}}, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
	if want := "the ledger could not record the outcome of the step e2e: ledger: disk full (injected)"; row.Stage != "verify e2e" || row.Error != want {
		t.Fatalf("attempt 1: stage %q, %q", row.Stage, row.Error)
	}
	e2e, _ := rowOf(l.stepRows(run.ID, "build", 1), "e2e", 1)
	if e2e.State != ledger.CheckFinished || e2e.Exit != 0 || e2e.PID == 0 || !e2e.Stable ||
		string(e2e.Log) != "all green\n" || e2e.LogSHA256 != sha(e2e.Log) || e2e.Tail != "all green\n" {
		t.Fatalf("the verify step's row after its refused outcome: %+v", e2e)
	}
	if l.eventAt(run.ID, "step.finished", func(p map[string]any) bool { return p["phase"] == ledger.PhaseVerify && p["attempt"] == 1 }) < 0 {
		t.Fatalf("no step.finished for the verify step: %v", l.kinds(run.ID))
	}
	stopRun(t, e, run.ID)
}

// A check whose attempt is cancelled while it waits for the server's check
// slot is concluded interrupted, not left started: its receipt reads as a
// check that never ran (no process), and the result's checkout ends after
// it (Codex's review of #498). The test holds the only slot itself.
func TestACheckCancelledWhileItWaitsForItsSlotIsConcluded(t *testing.T) {
	for _, kind := range checkKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			sem := e.checkSlots()
			sem <- struct{}{}
			held := true
			t.Cleanup(func() {
				if held {
					<-sem
				}
			})
			run := launchManaged(t, e, kind.crew(dir, shStep("e2e", "true")))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckStarted })
			if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
				t.Fatal(err)
			}
			e2e := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State != ledger.CheckStarted })
			if e2e.State != ledger.CheckInterrupted || e2e.Error != kind.interrupted || e2e.PID != 0 || len(e2e.Log) != 0 {
				t.Fatalf("the check cancelled in its slot's queue: %+v", e2e)
			}
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
			<-sem
			held = false
			if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled {
				t.Fatalf("the task: %+v", ts)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A check its attempt's cancel interrupts keeps what its process left: its
// pid, its group, its output, the output's hash and tail, so the receipt's
// log is there to read (Codex's review of #498).
func TestAnInterruptedCheckKeepsItsOutputAndProcess(t *testing.T) {
	for _, kind := range checkKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate, said := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "said")
			t.Cleanup(func() { open(t, gate) })
			run := launchManaged(t, e, kind.crew(dir, shStep("e2e", `echo "diagnostics before the cancel"; touch "`+said+`"; `+gated(gate))))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			running := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
			waitFake(t, "the check's output written", func() bool { _, err := os.Stat(said); return err == nil })
			if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
				t.Fatal(err)
			}
			e2e := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckInterrupted })
			if e2e.Error != kind.interrupted || e2e.PID == 0 || e2e.PID != running.PID || e2e.Group != running.Group || e2e.ProcessStartedAt.IsZero() ||
				string(e2e.Log) != "diagnostics before the cancel\n" || e2e.LogSHA256 != sha(e2e.Log) || e2e.Tail != "diagnostics before the cancel\n" {
				t.Fatalf("the interrupted check's row: %+v", e2e)
			}
			if alive(e2e.PID) {
				t.Fatalf("the check's process %d outlived the cancel", e2e.PID)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A check whose attempt is cancelled while its checkout is compared is
// concluded interrupted, not failed for a checkout that could not be
// compared: the cancel cuts the comparison's git commands, so their error
// says nothing of the check, and the receipt keeps what the process left
// (Codex's review of #498). The test holds the comparison until the cancel
// has ended the check's context.
func TestACheckCancelledWhileItsCheckoutIsComparedIsInterrupted(t *testing.T) {
	for _, kind := range checkKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			comparing, cut := make(chan struct{}, 1), make(chan struct{}, 1)
			e.beforeCompare = func(ctx context.Context, id string) {
				if id != "e2e" {
					return
				}
				comparing <- struct{}{}
				<-ctx.Done()
				cut <- struct{}{}
			}
			run := launchManaged(t, e, kind.crew(dir, shStep("e2e", `echo "all green"`)))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			select {
			case <-comparing:
			case <-time.After(30 * time.Second):
				t.Fatal("the check's checkout was not compared after 30 s")
			}
			running, _ := rowOf(l.stepRows(run.ID, "build", 1), "e2e", 1)
			if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
				t.Fatal(err)
			}
			<-cut
			e2e := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State != ledger.CheckRunning })
			if e2e.State != ledger.CheckInterrupted || e2e.Error != kind.interrupted || e2e.Stable ||
				e2e.PID == 0 || e2e.PID != running.PID || e2e.Group != running.Group || e2e.ProcessStartedAt.IsZero() || e2e.Exit != 0 ||
				string(e2e.Log) != "all green\n" || e2e.LogSHA256 != sha(e2e.Log) || e2e.Tail != "all green\n" {
				t.Fatalf("the check cancelled while its checkout was compared: %+v", e2e)
			}
			if strings.Contains(e2e.Error, "could not be compared") {
				t.Fatalf("the cut comparison was read as the check's: %q", e2e.Error)
			}
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
			if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled {
				t.Fatalf("the task: %+v", ts)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A cleanup step that fails on its own is a failure even when the
// shutdown's cut-off lands right after its receipt is committed: the
// tree's end takes the cut from the step's own receipt, never from the
// teardown read again after it (the independent review of #498). The
// step's finished commit is held while the cut-off is armed and lands.
func TestACleanupStepThatFailsJustBeforeTheCutOffIsAFailure(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "step.finished[phase=cleanup]", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	released := false
	t.Cleanup(func() {
		if !released {
			close(l.release)
		}
	})
	m := Manifest{Cleanup: []Check{shStep("db", `echo "the database would not drop"; exit 1`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the cleanup step's receipt was not committed after 10 s")
	}
	// A deadline the reserve takes all of: the cut-off is now.
	ctx, cancel := context.WithTimeout(context.Background(), teardownReserve)
	defer cancel()
	e.Draining(ctx)
	r := runOf(t, e, run.ID)
	waitFake(t, "the cut-off", func() bool { return r.teardown.Err() != nil })
	released = true
	close(l.release)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
	if db, _ := rowOf(l.stepRows(run.ID, "build", 1), "db", 1); db.State != ledger.CheckFinished || db.Exit != 1 {
		t.Fatalf("the cleanup step: %+v", db)
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.Interrupted {
		t.Fatalf("the worktree's row says interrupted with nothing cut: %+v", tr)
	}
	if p := l.payloads(run.ID, "tree.ended"); len(p) != 1 || p[0]["cleanupFailed"] != "db" || p[0]["interrupted"] != nil {
		t.Fatalf("tree.ended: %v", p)
	}
	waitTreesEnded(t, e, run.ID)
}
