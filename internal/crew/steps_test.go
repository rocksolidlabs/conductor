package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// stepRows is the attempt's receipts as the fake ledger last has them,
// one per id and k, in the order each was first put.
func (l *fakeLedger) stepRows(runID, taskID string, n int) []ledger.CheckRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ledger.CheckRow
	for _, c := range l.checks {
		if c.RunID != runID || c.TaskID != taskID || c.N != n {
			continue
		}
		if i := slices.IndexFunc(out, func(o ledger.CheckRow) bool { return o.ID == c.ID && o.K == c.K }); i >= 0 {
			out[i] = c
			continue
		}
		out = append(out, c)
	}
	return out
}

// prereqRows is the run's prerequisites' receipts as the fake ledger last
// has them, one per id and k, in the order each was first put.
func (l *fakeLedger) prereqRows(runID string) []ledger.PrerequisiteRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ledger.PrerequisiteRow
	for _, c := range l.prerequisites {
		if c.RunID != runID {
			continue
		}
		if i := slices.IndexFunc(out, func(o ledger.PrerequisiteRow) bool { return o.ID == c.ID && o.K == c.K }); i >= 0 {
			out[i] = c
			continue
		}
		out = append(out, c)
	}
	return out
}

// treeRow is a tree's row as the fake ledger last has it.
func (l *fakeLedger) treeRow(runID, taskID string, n int, kind string) (ledger.TreeRow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.trees[fmt.Sprintf("%s/%s/%d/%s", runID, taskID, n, kind)]
	return row, ok
}

// payloads is the payloads of the run's events of the kind, in order.
func (l *fakeLedger) payloads(runID, kind string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, ev := range l.events {
		if ev.runID == runID && ev.kind == kind {
			p, _ := ev.payload.(map[string]any)
			out = append(out, p)
		}
	}
	return out
}

// waitFake waits up to 10 s for ok, holding nothing, and fails with what.
func waitFake(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("not %s after 10 s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitTreesEnded waits up to 10 s for the run's trees being ended to be
// none, on the count itself (r.trees): a tree's row is ended by its last
// commit and the count drops after it, once endTree's transaction returns,
// so a row seen ended does not say the count has dropped (Codex's review
// of #422, round 2).
func waitTreesEnded(t *testing.T, e *Engine, runID string) {
	t.Helper()
	r := runOf(t, e, runID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.waitTrees(ctx, r)
	e.mu.Lock()
	n := r.trees.n
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d trees of %s still being ended after 10 s", n, runID)
	}
}

// rowWords is a receipt in words, for a failure's message.
func rowWords(rows []ledger.CheckRow) string {
	var out []string
	for _, c := range rows {
		out = append(out, fmt.Sprintf("%s/%s k%d %s exit %d dir %s manifest %q error %q", c.Phase, c.ID, c.K, c.State, c.Exit, c.Dir, c.Manifest, c.Error))
	}
	return strings.Join(out, "\n")
}

// repoManifestCrew is a managed crew in dir with the manifest m, its
// tasks given.
func repoManifestCrew(dir string, m Manifest, tasks ...Task) Crew {
	c := repoCrew(dir, tasks...)
	c.Manifest = &m
	return c
}

// shStep is a step running a line of sh.
func shStep(id, line string) Check {
	return Check{ID: id, Argv: []string{"sh", "-c", line}}
}

// gated is a line of sh that waits for the file gate to exist.
func gated(gate string) string {
	return `while [ ! -e "` + gate + `" ]; do sleep 0.05; done`
}

// open makes the file at path.
func open(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "open\n")
}

// sha is the hex SHA-256 of b.
func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Every step of a tree is a receipt (EN1 PR-2): a row of its phase with
// the manifest that ran it, its directory and commit, its exit and log
// and the log's hash, put started, running and finished; the worktree's
// and the checkout's are told apart by k and dir. The tree's row and its
// events say when it was resolved and when it ended, after the attempt's
// close; the task file names the manifest.
func TestEveryStepOfATreeIsAReceipt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "test -f dep.txt")}
	c := repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", "echo installing; echo dep > dep.txt"), shStep("seed", "echo seeded")}}, build)
	c.ManifestRevision = 3
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 2 {
		t.Fatalf("the worktree's receipts:\n%s", rowWords(rows))
	}
	for i, want := range []struct{ id, log string }{{"deps", "installing\n"}, {"seed", "seeded\n"}} {
		r := rows[i]
		if r.ID != want.id || r.K != 1 || r.Phase != ledger.PhaseSetup || r.Manifest != "crew:3" || r.Dir != a.Attempt.Worktree || r.Subject != a.Attempt.Start ||
			r.State != ledger.CheckFinished || r.Exit != 0 || r.Error != "" || string(r.Log) != want.log || r.LogSHA256 != sha(r.Log) || r.Tries != 1 || r.PID == 0 {
			t.Fatalf("receipt %d: %+v", i, r)
		}
	}
	kinds := l.kinds(run.ID)
	order := []string{"attempt.reserved", "manifest.resolved", "attempt.workspace", "step.started", "step.running", "step.finished", "tree.ready", "attempt.launched"}
	for i := 1; i < len(order); i++ {
		if slices.Index(kinds, order[i-1]) < 0 || slices.Index(kinds, order[i-1]) > slices.Index(kinds, order[i]) {
			t.Fatalf("%s before %s: %v", order[i-1], order[i], kinds)
		}
	}
	if p := l.payloads(run.ID, "manifest.resolved")[0]; p["tree"] != ledger.TreeWorktree || p["source"] != "crew" || p["revision"] != int64(3) {
		t.Fatalf("manifest.resolved: %v", p)
	}
	if tr, ok := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); !ok || tr.State != ledger.TreeReady || tr.Dir != a.Attempt.Worktree || tr.Commit != a.Attempt.Start || tr.Manifest != "crew:3" {
		t.Fatalf("the worktree's row while it runs: %+v", tr)
	}
	if p := l.payloads(run.ID, "tree.ready"); len(p) != 1 || p[0]["tree"] != ledger.TreeWorktree || p[0]["preview"] != "" {
		t.Fatalf("tree.ready: %v", p)
	}
	md, f := taskFileOf(t, a)
	if f.Manifest == nil || f.Manifest.Source != "crew" || f.Manifest.Revision != 3 || !strings.Contains(md, "The crew's project manifest (revision 3) made the tree usable") {
		t.Fatalf("the task file's manifest: %+v\n%s", f.Manifest, md)
	}
	// The result: the checkout's setup, then the check, each the next k of
	// its id on the attempt; then both trees end, after the decision.
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitFake(t, "both trees ended", func() bool {
		w, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree)
		c, _ := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout)
		return w.State == ledger.TreeEnded && c.State == ledger.TreeEnded
	})
	rows = l.stepRows(run.ID, "build", 1)
	if len(rows) != 5 || rows[2].ID != "deps" || rows[2].K != 2 || rows[2].Dir != a.Attempt.Worktree+".check" || rows[3].ID != "seed" || rows[3].K != 2 || rows[4].Phase != ledger.PhaseCheck || rows[4].ID != "unit" || rows[4].K != 1 || rows[4].Manifest != "" {
		t.Fatalf("the attempt's receipts:\n%s", rowWords(rows))
	}
	kinds = l.kinds(run.ID)
	succeeded := slices.Index(kinds, "task.succeeded")
	ended := 0
	for i, k := range kinds {
		if k == "tree.ended" {
			ended++
			if i < succeeded {
				t.Fatalf("a tree ended before the decision: %v", kinds)
			}
		}
	}
	if ended != 2 {
		t.Fatalf("%d tree.ended events: %v", ended, kinds)
	}
	if w, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); w.Why != "attempt 1 succeeded" || w.EndedAt.IsZero() {
		t.Fatalf("the worktree's end: %+v", w)
	}
	waitTreesEnded(t, e, run.ID)
	if r, _ := e.Get(run.ID); r.Cleaning != 0 || r.Manifest == nil || r.Manifest.Source != "crew" || r.Manifest.Revision != 3 {
		t.Fatalf("the run: cleaning %d, manifest %+v", r.Cleaning, r.Manifest)
	}
	stopRun(t, e, run.ID)
}

// A crew without a project manifest makes no tree and keeps no step
// receipt: it runs as before manifests.
func TestARunWithoutAManifestMakesNoTree(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "true")}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	for _, k := range []string{"manifest.resolved", "tree.ended", "step.started", "prerequisites.passed"} {
		if n := l.count(run.ID, k); n != 0 {
			t.Fatalf("%d %s events in a run without a manifest", n, k)
		}
	}
	if rows := l.stepRows(run.ID, "build", 1); len(rows) != 1 || rows[0].Phase != ledger.PhaseCheck || rows[0].K != 1 {
		t.Fatalf("the receipts:\n%s", rowWords(rows))
	}
	if r, _ := e.Get(run.ID); r.Manifest != nil {
		t.Fatalf("a manifest on a run without one: %+v", r.Manifest)
	}
	stopRun(t, e, run.ID)
}

// failsInTheWorktree is a setup step that passes in a checkout and fails
// in a worktree while mark exists.
func failsInTheWorktree(mark string) Check {
	return shStep("deps", `case "$(pwd)" in *.check) exit 0;; esac; if [ -e "`+mark+`" ]; then echo the registry is down >&2; exit 1; fi`)
}

// A setup step that fails in an attempt's worktree is the host's or the
// manifest's, not the agent's: the attempt fails at setup deps, uncounted
// (the task ready with why, one more attempt allowed), the run's dispatch
// pauses with the same words, and nothing is dispatched until a person
// unpauses, which runs the prerequisites again (k 2). The next attempt is
// numbered after it and starts where it started: attempt 1's result B,
// refused at a check, attempt 2 at B failing at setup deps, attempt 3 at
// B. A continuation made while it is paused does the same.
func TestASetupStepThatFailsInTheWorktreeIsNotCounted(t *testing.T) {
	for _, continued := range []bool{false, true} {
		t.Run(map[bool]string{false: "in the run", true: "after a continuation"}[continued], func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			mark := filepath.Join(t.TempDir(), "registry-down")
			build := work("build", "lead")
			build.Checks = []Check{shStep("ok", "test -e ok.txt")}
			c := repoManifestCrew(dir, Manifest{Prerequisites: []Check{{ID: "tools", Argv: []string{"true"}}}, Setup: []Check{failsInTheWorktree(mark)}}, build)
			run := launchManaged(t, e, c)
			a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			open(t, mark)
			commitResult(t, e, a1, "half.txt", "half\n", "half")
			attemptFailed(t, e, run.ID, "build", 2)
			// The engine's view is one change ahead of the ledger while that
			// change commits: attempt 2's row, its task's and its
			// attempt.failed are waited for in the ledger (one commit).
			row1, row2 := l.attemptWhen(t, run.ID, "build", 1, failedRow), l.attemptWhen(t, run.ID, "build", 2, failedRow)
			b := row1.ResultCommit
			if row1.Stage != "check ok" || b == "" || row2.StartCommit != b || row2.Stage != "setup deps" || !strings.Contains(row2.Error, "deps exited 1: the registry is down") || row2.ResultCommit != "" {
				t.Fatalf("attempt 1 %+v\nattempt 2 %+v", row1, row2)
			}
			why := "attempt 2 could not be made usable at setup deps: " + row2.Error
			ts := taskOf(t, e, run.ID, "build")
			if ts.State != ledger.TaskReady || ts.Reason != why || ts.Attempts != 2 {
				t.Fatalf("the task: %+v", ts)
			}
			l.mu.Lock()
			extra := l.tasks[run.ID+"/build"].Extra
			l.mu.Unlock()
			if extra != 1 {
				t.Fatalf("extra %d", extra)
			}
			failed := l.payloads(run.ID, "attempt.failed")
			if p := failed[len(failed)-1]; p["attempt"] != 2 || p["uncounted"] != true || p["stage"] != "setup deps" {
				t.Fatalf("attempt.failed: %v", p)
			}
			waitFake(t, "the pause recorded", func() bool {
				l.mu.Lock()
				defer l.mu.Unlock()
				return l.runs[run.ID].Paused && l.runs[run.ID].PausedWhy == why
			})
			if r, _ := e.Get(run.ID); !r.Paused || r.PausedWhy != why {
				t.Fatalf("the run: paused %v %q", r.Paused, r.PausedWhy)
			}
			// Pumped, the paused run dispatches nothing.
			e.mu.Lock()
			rr := e.runs[run.ID]
			e.mu.Unlock()
			e.pumpLocked(rr)
			if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 2 {
				t.Fatalf("an attempt dispatched while paused: %+v", ts)
			}
			if p := l.prereqRows(run.ID); len(p) != 1 || p[0].K != 1 || p[0].State != ledger.CheckFinished {
				t.Fatalf("the prerequisites before the pause: %+v", p)
			}
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
				// Its prerequisites run again, at its base: their generation
				// ends before the unpause, which then runs them a second time.
				waitFake(t, "the continuation's prerequisites passed", func() bool { return l.count(next.ID, "prerequisites.passed") == 1 })
				runID, eng = next.ID, e2
			}
			if err := os.Remove(mark); err != nil {
				t.Fatal(err)
			}
			pauseRun(t, eng, runID, false)
			a3 := attemptN(t, eng, runID, "build", 3)
			if a3.Attempt.Start != b {
				t.Fatalf("attempt 3 starts at %s, not at attempt 2's start %s", short(a3.Attempt.Start), short(b))
			}
			prereqs := l.prereqRows(runID)
			if want := map[bool]int{false: 2, true: 2}[continued]; len(prereqs) != want || prereqs[len(prereqs)-1].K != want {
				t.Fatalf("the prerequisites after the unpause: %+v", prereqs)
			}
			md, f := taskFileOf(t, a3)
			if f.Previous == nil || f.Previous.Attempt != 1 || f.Previous.Stage != "check ok" || f.Unusable == nil || f.Unusable.Attempt != 2 || f.Unusable.Stage != "setup deps" ||
				f.StartFrom != "attempt 1's result" || !strings.Contains(md, "Attempt 2 could not be made usable: it failed at setup deps") {
				t.Fatalf("attempt 3's task file: previous %+v unusable %+v from %q\n%s", f.Previous, f.Unusable, f.StartFrom, md)
			}
			stopRun(t, eng, runID)
		})
	}
}

// checkpointAndEnd commits broken.txt in the attempt's worktree, saves a
// checkpoint at that commit and ends the attempt's session: the attempt
// fails without a result, its checkpoint the commit C it returns.
func checkpointAndEnd(t *testing.T, e *Engine, fl *fakeLauncher, ts TaskState) string {
	t.Helper()
	writeFile(t, filepath.Join(ts.Attempt.Worktree, "broken.txt"), "x\n")
	commitAll(t, ts.Attempt.Worktree, "breaks the setup")
	cp, err := e.SaveCheckpoint(t.Context(), ts.Attempt.SessionID, "broke it", "fix it", "")
	if err != nil {
		t.Fatal(err)
	}
	_, p := fl.attemptSession(t, ts)
	p.End(0)
	return cp.Checkpoint.Commit
}

// A worktree made at a checkpoint the agent saved (a commit of its own
// that nothing verified) that cannot be made usable refuses the checkpoint
// as a refused result is refused: attempt 1 saves C and its session ends,
// attempt 2 at C fails at setup deps, uncounted, the run not paused, its
// attempt.failed naming C, and attempt 3 is dispatched at once at attempt
// 1's start, its task file naming the checkpoint as not usable and having
// no checkpoint section. A second failure there, at a commit the agent did
// not make, pauses as any other. A continuation made between attempts 2
// and 3 reads the refusal from the rows: its task's checkpoint is none.
func TestAWorktreeAtACheckpointThatCannotBeMadeUsableRefusesIt(t *testing.T) {
	t.Run("in the run", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		c := repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", "if [ -e broken.txt ]; then echo broken >&2; exit 1; fi")}}, work("build", "lead"))
		run := launchManaged(t, e, c)
		a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		cp := checkpointAndEnd(t, e, fl, a1)
		a3 := attemptN(t, e, run.ID, "build", 3)
		row2 := l.attemptRow(t, run.ID, "build", 2)
		if row2.StartCommit != cp || row2.State != ledger.AttemptFailed || row2.Stage != "setup deps" {
			t.Fatalf("attempt 2: %+v", row2)
		}
		if a3.Attempt.Start != a1.Attempt.Start {
			t.Fatalf("attempt 3 starts at %s, not at attempt 1's start %s", short(a3.Attempt.Start), short(a1.Attempt.Start))
		}
		failed := l.payloads(run.ID, "attempt.failed")
		if p := failed[len(failed)-1]; p["attempt"] != 2 || p["uncounted"] != true || p["checkpoint"] != cp {
			t.Fatalf("attempt.failed: %v", p)
		}
		if r, _ := e.Get(run.ID); r.Paused || l.count(run.ID, "run.paused") != 0 {
			t.Fatalf("the run paused for a refused checkpoint: %+v", r.Paused)
		}
		md, f := taskFileOf(t, a3)
		if f.Checkpoint != nil || f.Previous == nil || f.Previous.Attempt != 1 || f.Previous.Checkpoint != cp || f.Previous.Branch != a1.Attempt.Branch || f.Unusable == nil || f.Unusable.Attempt != 2 ||
			!strings.Contains(md, fmt.Sprintf("Attempt 1's checkpoint, the commit %s on the branch %s, could not be made usable: attempt 2 failed at setup deps", cp, a1.Attempt.Branch)) ||
			strings.Contains(md, "## Where the last attempt got to") || f.StartFrom != "where attempt 1 started: its checkpoint "+short(cp)+" could not be made usable" {
			t.Fatalf("attempt 3's task file: checkpoint %+v previous %+v unusable %+v from %q\n%s", f.Checkpoint, f.Previous, f.Unusable, f.StartFrom, md)
		}
		stopRun(t, e, run.ID)
	})
	t.Run("a second failure pauses", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		mark := filepath.Join(t.TempDir(), "down")
		c := repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", `if [ -e broken.txt ] || [ -e "`+mark+`" ]; then exit 1; fi`)}}, work("build", "lead"))
		run := launchManaged(t, e, c)
		a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		open(t, mark)
		checkpointAndEnd(t, e, fl, a1)
		attemptFailed(t, e, run.ID, "build", 3)
		if row3 := l.attemptRow(t, run.ID, "build", 3); row3.StartCommit != a1.Attempt.Start || row3.Stage != "setup deps" {
			t.Fatalf("attempt 3: %+v", row3)
		}
		waitTrue(t, "the run paused", func() bool {
			r, _ := e.Get(run.ID)
			return r.Paused && strings.HasPrefix(r.PausedWhy, "attempt 3 could not be made usable at setup deps")
		})
		if err := os.Remove(mark); err != nil {
			t.Fatal(err)
		}
		pauseRun(t, e, run.ID, false)
		if a4 := attemptN(t, e, run.ID, "build", 4); a4.Attempt.Start != a1.Attempt.Start {
			t.Fatalf("attempt 4 starts at %s, not at attempt 1's start", short(a4.Attempt.Start))
		}
		stopRun(t, e, run.ID)
	})
	t.Run("after a continuation", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, l := managedEngine(t)
		gate := filepath.Join(t.TempDir(), "gate")
		c := repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", `if [ -e broken.txt ]; then `+gated(gate)+`; exit 1; fi`)}}, work("build", "lead"))
		run := launchManaged(t, e, c)
		a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		cp := checkpointAndEnd(t, e, fl, a1)
		// Attempt 2's setup holds at the gate; the run is paused, then the
		// step fails: the checkpoint refused, nothing dispatched.
		waitFake(t, "attempt 2's setup running", func() bool {
			rows := l.stepRows(run.ID, "build", 2)
			return len(rows) == 1 && rows[0].State == ledger.CheckRunning
		})
		pauseRun(t, e, run.ID, true)
		open(t, gate)
		attemptFailed(t, e, run.ID, "build", 2)
		stopRun(t, e, run.ID)
		e2, _ := readyEngine(t)
		e2.UseLedger(l)
		next, err := e2.ContinueRun(t.Context(), run.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		e2.mu.Lock()
		task := e2.runs[next.ID].task("build")
		cpGone, u := task.checkpoint == nil, task.unusable
		e2.mu.Unlock()
		if !cpGone || u == nil || u.n != 2 || u.checkpoint != cp || u.next != a1.Attempt.Start {
			t.Fatalf("the continuation's task: checkpoint gone %v, unusable %+v", cpGone, u)
		}
		pauseRun(t, e2, next.ID, false)
		a3 := attemptN(t, e2, next.ID, "build", 3)
		if a3.Attempt.Start != a1.Attempt.Start {
			t.Fatalf("attempt 3 starts at %s, not at attempt 1's start", short(a3.Attempt.Start))
		}
		md, f := taskFileOf(t, a3)
		if f.Checkpoint != nil || f.Previous == nil || f.Previous.Attempt != 1 || f.Previous.Checkpoint != cp || f.Unusable == nil || f.Unusable.Attempt != 2 || strings.Contains(md, "## Where the last attempt got to") {
			t.Fatalf("attempt 3's task file: checkpoint %+v previous %+v unusable %+v\n%s", f.Checkpoint, f.Previous, f.Unusable, md)
		}
		stopRun(t, e2, next.ID)
	})
}

// A setup step's running row the ledger refuses ends its process with its
// group and fails the step with the ledger's words (Codex's review of
// #422), as a refused started row does: in a worktree the attempt is not
// counted and the run pauses with those words.
func TestARefusedRunningRowEndsAndFailsTheSetupStep(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	gate := filepath.Join(t.TempDir(), "gate")
	t.Cleanup(func() { open(t, gate) })
	l.mu.Lock()
	l.failOn, l.failOnce = "step.running[phase=setup]", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", gated(gate))}}, work("build", "lead")))
	attemptFailed(t, e, run.ID, "build", 1)
	words := "the ledger could not record the step deps: ledger: disk full (injected)"
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 1 || rows[0].State != ledger.CheckFinished || rows[0].Error != words || rows[0].PID == 0 {
		t.Fatalf("the step:\n%s", rowWords(rows))
	}
	if alive(rows[0].PID) {
		t.Fatalf("the setup step's process %d outlived its refused row", rows[0].PID)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.Stage != "setup deps" || row.Error != words {
		t.Fatalf("attempt 1: %+v", row)
	}
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && strings.Contains(r.PausedWhy, words) })
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskReady {
		t.Fatalf("the task after an uncounted attempt: %+v", ts)
	}
	stopRun(t, e, run.ID)
}

// fakePorts is the harness's port executor (treeExec.port): numbers from a
// range in turn, wrapping, never the network, a test's scripted ones
// first; it keeps every number it drew.
type fakePorts struct {
	mu    sync.Mutex
	next  int
	queue []int
	drawn []int
}

// The range fakePorts draws from.
const (
	fakePortsFirst = 41000
	fakePortsLast  = 41999
)

func newFakePorts() *fakePorts { return &fakePorts{next: fakePortsFirst} }

// port draws a number: the first scripted one, else the range's next.
func (f *fakePorts) port(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	if len(f.queue) > 0 {
		n, f.queue = f.queue[0], f.queue[1:]
	} else {
		n = f.next
		if f.next++; f.next > fakePortsLast {
			f.next = fakePortsFirst
		}
	}
	f.drawn = append(f.drawn, n)
	return n, nil
}

// script puts ns first in line, in order.
func (f *fakePorts) script(ns ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, ns...)
}

// draws is every number drawn so far, in order.
func (f *fakePorts) draws() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.drawn)
}

// fakeProbes is the harness's probe executor (treeExec.probe): it answers
// a probe over HTTP from a map, by URL, never the network; a URL it has no
// answer for is refused, as a port nothing listens on is. It counts the
// tries each URL was asked.
type fakeProbes struct {
	mu      sync.Mutex
	answers map[string]func(ctx context.Context) (int, error)
	asked   map[string]int
}

func newFakeProbes() *fakeProbes {
	return &fakeProbes{answers: map[string]func(ctx context.Context) (int, error){}, asked: map[string]int{}}
}

// probe is one try of the probe of url.
func (f *fakeProbes) probe(ctx context.Context, url string, _ time.Duration) (int, error) {
	f.mu.Lock()
	answer := f.answers[url]
	f.asked[url]++
	f.mu.Unlock()
	if answer == nil {
		return 0, errors.New("dial tcp: connect: connection refused (no answer in the harness)")
	}
	return answer(ctx)
}

// answer makes url answer status from now on.
func (f *fakeProbes) answer(url string, status int) {
	f.answerWith(url, func(context.Context) (int, error) { return status, nil })
}

// answerWith makes url answer as fn says from now on.
func (f *fakeProbes) answerWith(url string, fn func(ctx context.Context) (int, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[url] = fn
}

// tries is how many times url was asked.
func (f *fakeProbes) tries(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[url]
}

// leases is the engine's port leases: each number and the tree it is
// leased to.
func leases(e *Engine) map[int]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[int]string{}
	for n, t := range e.ports {
		out[n] = fmt.Sprintf("%s.%d %s", t.a.t.def.ID, t.a.n, t.kind)
	}
	return out
}

// attemptWhen waits up to 10 s for the attempt's row in the fake ledger to
// satisfy ok, and returns it: the run as read is one change ahead of the
// ledger while that change commits, so what the ledger must hold is waited
// for in the ledger.
func (l *fakeLedger) attemptWhen(t *testing.T, runID, taskID string, n int, ok func(ledger.AttemptRow) bool) ledger.AttemptRow {
	t.Helper()
	var row ledger.AttemptRow
	waitFake(t, fmt.Sprintf("the row of %s's attempt %d as wanted", taskID, n), func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		r, found := l.attempts[fmt.Sprintf("%s/%s/%d", runID, taskID, n)]
		row = r
		return found && ok(r)
	})
	return row
}

// failedRow is an attempt's row closed failed.
func failedRow(r ledger.AttemptRow) bool { return r.State == ledger.AttemptFailed }

// succeededRow is an attempt's row closed succeeded.
func succeededRow(r ledger.AttemptRow) bool { return r.State == ledger.AttemptSucceeded }

// taskWhen waits up to 10 s for the task's row in the fake ledger to
// satisfy ok, and returns it, as attemptWhen does for an attempt's: a task
// read in the engine's view is one change ahead of the ledger while that
// change commits.
func (l *fakeLedger) taskWhen(t *testing.T, runID, taskID, what string, ok func(ledger.TaskRow) bool) ledger.TaskRow {
	t.Helper()
	var row ledger.TaskRow
	waitFake(t, fmt.Sprintf("the row of %s %s", taskID, what), func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		r, found := l.tasks[runID+"/"+taskID]
		row = r
		return found && ok(r)
	})
	return row
}

// waitTreeRowsEnded waits up to 10 s for the rows of the attempt's trees of
// the kinds given to be ended in the fake ledger. A task read as succeeded
// is one commit ahead of the ledger, and the trees its close handed over
// are counted (r.trees) only once that commit is made: the count read in
// that window is none before their end has begun. So a test waits for the
// rows, then for the count (waitTreesEnded), which drops once each end's
// commit and what follows it are done (Codex's review of #426, round 1).
func (l *fakeLedger) waitTreeRowsEnded(t *testing.T, runID, taskID string, n int, kinds ...string) {
	t.Helper()
	waitFake(t, fmt.Sprintf("the rows of %s's attempt %d's trees %v ended", taskID, n, kinds), func() bool {
		for _, kind := range kinds {
			if tr, _ := l.treeRow(runID, taskID, n, kind); tr.State != ledger.TreeEnded {
				return false
			}
		}
		return true
	})
}

// envFile is the environment a step wrote to the file at path (env >
// path), by name.
func envFile(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// conductorVars is env's CONDUCTOR_ names with their values.
func conductorVars(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if strings.HasPrefix(k, "CONDUCTOR_") {
			out[k] = v
		}
	}
	return out
}

// A tree has the host's names, its ports and its state directory (EN1
// PR-3). A step's environment (env written to a file) holds the manifest's
// env names the server approved and no other name of its passthrough, the
// tree's ports as CONDUCTOR_PORT_*, exactly the injectable CONDUCTOR_
// names and no token; a placeholder in its argv is put in; the session is
// given the tree's names and the approved names; the task files name the
// ports and the state directory as they are on disk before the launch; the
// events, the tree's row and every receipt carry the ports. The checkout
// of the result is a tree of its own: other ports, its own state
// directory with .conductor's .gitignore beside it, and the task's check
// given its names as its steps are, so it reaches what the tree holds.
// Once both trees have ended their ports are nobody's.
func TestATreeHasTheHostsNamesItsPortsAndItsStateDirectory(t *testing.T) {
	t.Setenv("EN1_STEP_TOKEN", "from-the-host")
	t.Setenv("EN1_NOT_NAMED", "not-asked-for")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "the-server's")
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	e.SetEnvPassthrough([]string{"EN1_STEP_TOKEN", "EN1_NOT_NAMED"})
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", `test -d "$CONDUCTOR_STATE_DIR/cache/$CONDUCTOR_PORT_WEB" && test "$EN1_STEP_TOKEN" = from-the-host && env > "$CONDUCTOR_STATE_DIR/check-env.txt"`)}
	m := Manifest{Env: []string{"EN1_STEP_TOKEN"}, Ports: []string{"web", "db"},
		Setup: []Check{shStep("env", `env > "$CONDUCTOR_STATE_DIR/env.txt"`), {ID: "cache", Argv: []string{"mkdir", "-p", "${CONDUCTOR_STATE_DIR}/cache/${CONDUCTOR_PORT_WEB}"}}}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	wt := a.Attempt.Worktree
	state := filepath.Join(wt, ".conductor", "state")
	ports := map[string]int{"web": fakePortsFirst, "db": fakePortsFirst + 1}
	want := map[string]string{"CONDUCTOR_RUN": run.ID, "CONDUCTOR_TASK": "build", "CONDUCTOR_ATTEMPT": "1", "CONDUCTOR_STATE_DIR": state,
		"CONDUCTOR_PORT_WEB": strconv.Itoa(ports["web"]), "CONDUCTOR_PORT_DB": strconv.Itoa(ports["db"])}
	env := envFile(t, filepath.Join(state, "env.txt"))
	if got := conductorVars(env); !maps.Equal(got, want) {
		t.Fatalf("the step's CONDUCTOR_ names: %v, want %v", got, want)
	}
	if env["EN1_STEP_TOKEN"] != "from-the-host" || env["EN1_NOT_NAMED"] != "" {
		t.Fatalf("the step's host names: EN1_STEP_TOKEN=%q EN1_NOT_NAMED=%q", env["EN1_STEP_TOKEN"], env["EN1_NOT_NAMED"])
	}
	cache := filepath.Join(state, "cache", strconv.Itoa(ports["web"]))
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("the placeholders were not put in: %v", err)
	}
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 2 || !maps.Equal(rows[0].Ports, ports) || !maps.Equal(rows[1].Ports, ports) || rows[1].Argv[2] != cache {
		t.Fatalf("the worktree's receipts:\n%s\nports %v %v, argv %q", rowWords(rows), rows[0].Ports, rows[1].Ports, rows[1].Argv)
	}
	// The session: the tree's names and the approved names, beside its
	// crew's own.
	fl.mu.Lock()
	call := fl.calls[len(fl.calls)-1]
	fl.mu.Unlock()
	wantInject := map[string]string{"CONDUCTOR_STATE_DIR": state, "CONDUCTOR_PORT_WEB": want["CONDUCTOR_PORT_WEB"], "CONDUCTOR_PORT_DB": want["CONDUCTOR_PORT_DB"]}
	if call.name != "lead · build #1" || !maps.Equal(call.inject, wantInject) || !slices.Equal(call.allow, []string{"EN1_STEP_TOKEN"}) {
		t.Fatalf("the session %q: inject %v, allow %v", call.name, call.inject, call.allow)
	}
	// The task files, written before the launch.
	md, f := taskFileOf(t, a)
	if f.Manifest == nil || !maps.Equal(f.Manifest.Ports, ports) || !strings.Contains(md, "web 41000 (`CONDUCTOR_PORT_WEB`)") || !strings.Contains(md, "db 41001 (`CONDUCTOR_PORT_DB`)") ||
		!strings.Contains(md, "The tree's state directory is `.conductor/state`") {
		t.Fatalf("the task file's manifest: %+v\n%s", f.Manifest, md)
	}
	kinds := l.kinds(run.ID)
	for _, order := range [][2]string{{"manifest.resolved", "manifest.ports"}, {"manifest.ports", "attempt.workspace"}, {"attempt.workspace", "attempt.launched"}} {
		if i, j := slices.Index(kinds, order[0]), slices.Index(kinds, order[1]); i < 0 || i > j {
			t.Fatalf("%s before %s: %v", order[0], order[1], kinds)
		}
	}
	if p := l.payloads(run.ID, "manifest.ports")[0]; p["tree"] != ledger.TreeWorktree || !maps.Equal(p["ports"].(map[string]int), ports) || p["preview"] != "" {
		t.Fatalf("manifest.ports: %v", p)
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); !maps.Equal(tr.Ports, ports) {
		t.Fatalf("the worktree's row: %+v", tr)
	}
	// The checkout of the result: a tree of its own, its check given its
	// names.
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	ck := wt + ".check"
	ckState := filepath.Join(ck, ".conductor", "state")
	ckPorts := map[string]int{"web": fakePortsFirst + 2, "db": fakePortsFirst + 3}
	ckEnv := envFile(t, filepath.Join(ckState, "env.txt"))
	if ckEnv["CONDUCTOR_STATE_DIR"] != ckState || ckEnv["CONDUCTOR_PORT_WEB"] != strconv.Itoa(ckPorts["web"]) || ckEnv["CONDUCTOR_PORT_DB"] != strconv.Itoa(ckPorts["db"]) {
		t.Fatalf("the checkout's step: %v", conductorVars(ckEnv))
	}
	if checkEnv := envFile(t, filepath.Join(ckState, "check-env.txt")); !maps.Equal(conductorVars(checkEnv), conductorVars(ckEnv)) || checkEnv["EN1_STEP_TOKEN"] != "from-the-host" || checkEnv["EN1_NOT_NAMED"] != "" {
		t.Fatalf("the check's environment: %v", conductorVars(checkEnv))
	}
	if ignore, err := os.ReadFile(filepath.Join(ck, ".conductor", ".gitignore")); err != nil || string(ignore) != "*\n" {
		t.Fatalf("the checkout's .gitignore: %q %v", ignore, err)
	}
	rows = l.stepRows(run.ID, "build", 1)
	if len(rows) != 5 || rows[2].Dir != ck || !maps.Equal(rows[2].Ports, ckPorts) || rows[4].Phase != ledger.PhaseCheck || !maps.Equal(rows[4].Ports, ckPorts) {
		t.Fatalf("the attempt's receipts:\n%s", rowWords(rows))
	}
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	waitTreesEnded(t, e, run.ID)
	if held := leases(e); len(held) != 0 {
		t.Fatalf("ports leased after every tree ended: %v", held)
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout); tr.State != ledger.TreeEnded || !maps.Equal(tr.Ports, ckPorts) {
		t.Fatalf("the checkout's row: %+v", tr)
	}
	stopRun(t, e, run.ID)
}

// A port is one live tree's at a time (EN1 PR-3): leased to it in the
// transaction that makes it its attempt's, let go in its tree.ended and
// not before. Two trees in flight never share a number: two tasks' first
// draws scripted to one number, one gets it and the other draws again; the
// checkout drawing its worktree's number draws again; once every tree has
// ended, the number is the next tree's at its first draw.
func TestAPortIsOneLiveTreesAtATime(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	const shared = 41500
	fl.ports.script(shared, shared)
	// As each tree's end is about to be recorded, its ports are still its
	// own.
	var early []string
	e.beforeTreeEnd = func(string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, r := range e.runs {
			for _, tk := range r.tasks {
				for _, at := range tk.attempts {
					for _, tr := range at.trees {
						for name, n := range tr.ports {
							if tr.ending && !tr.ended && e.ports[n] != tr {
								early = append(early, fmt.Sprintf("%s.%d %s %s %d", tk.def.ID, at.n, tr.kind, name, n))
							}
						}
					}
				}
			}
		}
	}
	m := Manifest{Ports: []string{"web"}, Setup: []Check{shStep("deps", "true")}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead"), work("docs", "writer")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	d := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	bw, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree)
	dw, _ := l.treeRow(run.ID, "docs", 1, ledger.TreeWorktree)
	bPort, dPort := bw.Ports["web"], dw.Ports["web"]
	if draws := fl.ports.draws(); bPort == dPort || (bPort != shared && dPort != shared) || len(draws) != 3 || draws[0] != shared || draws[1] != shared {
		t.Fatalf("two worktrees in flight: build %d, docs %d, draws %v", bPort, dPort, draws)
	}
	if got := leases(e); len(got) != 2 {
		t.Fatalf("the leases: %v", got)
	}
	// The checkout of build's result draws its worktree's number first.
	fl.ports.script(bPort)
	commitResult(t, e, b, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	bc, _ := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout)
	if draws := fl.ports.draws(); len(draws) != 5 || draws[3] != bPort || bc.Ports["web"] != draws[4] || bc.Ports["web"] == bPort || bc.Ports["web"] == dPort {
		t.Fatalf("build's checkout: %v, draws %v", bc.Ports, draws)
	}
	commitResult(t, e, d, "docs.txt", "yes\n", "documented")
	waitTask(t, e, run.ID, "docs", ledger.TaskSucceeded, 1)
	l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	l.waitTreeRowsEnded(t, run.ID, "docs", 1, ledger.TreeWorktree, ledger.TreeCheckout)
	waitTreesEnded(t, e, run.ID)
	if got := leases(e); len(got) != 0 {
		t.Fatalf("ports leased after every tree ended: %v", got)
	}
	e.mu.Lock()
	gone := slices.Clone(early)
	e.mu.Unlock()
	if len(gone) > 0 {
		t.Fatalf("let go before their tree.ended: %v", gone)
	}
	// Let go, the number is the next tree's at its first draw.
	fl.ports.script(shared)
	before := len(fl.ports.draws())
	next := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	if w, _ := l.treeRow(next.ID, "build", 1, ledger.TreeWorktree); w.Ports["web"] != shared || len(fl.ports.draws()) != before+1 {
		t.Fatalf("the next tree: %v, draws %v", w.Ports, fl.ports.draws()[before:])
	}
	stopRun(t, e, next.ID)
	stopRun(t, e, run.ID)
}

// A port is let go only once its tree's end is in the ledger (Codex's
// review of #426, round 1). transact lets e.mu go before its commit, so a
// lease let go in tree.ended's change would be another run's to draw while
// the ledger still has the tree resolved, and stay so should the ledger
// refuse the end. With the end's commit held, and with it refused, the
// tree keeps its lease: another run's tree drawing the number draws again.
// The number is let go by the commit that writes the end: its own, let
// through, or the run's next (here a pause), which carries the refused one.
func TestAPortIsLetGoOnlyOnceItsTreesEndIsCommitted(t *testing.T) {
	const shared = 41500
	m := Manifest{Ports: []string{"web"}, Setup: []Check{shStep("deps", "true")}}
	for _, tc := range []struct {
		name    string
		refused bool
	}{{"the end's commit held", false}, {"the end's commit refused", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, l := managedEngine(t)
			fl.ports.script(shared)
			run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			if w, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); w.Ports["web"] != shared {
				t.Fatalf("build's worktree: %+v", w)
			}
			// The end's commit held is let through once, by the test or, should
			// it fail first, by its cleanup.
			var release func()
			l.mu.Lock()
			if tc.refused {
				l.failOn, l.failOnce = "tree.ended", true
			} else {
				held := make(chan struct{})
				l.blockOn, l.blocked, l.release = "tree.ended", make(chan struct{}, 1), held
				release = sync.OnceFunc(func() { close(held) })
				t.Cleanup(release)
			}
			l.mu.Unlock()
			if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
				t.Fatal(err)
			}
			if tc.refused {
				// The cancel's commit counted the tree before CancelTask
				// returned: the count drops once its refused end returns.
				waitTreesEnded(t, e, run.ID)
			} else {
				select {
				case <-l.blocked:
				case <-time.After(10 * time.Second):
					t.Fatal("the tree's end did not reach its commit")
				}
			}
			if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.State != ledger.TreeReady || l.count(run.ID, "tree.ended") != 0 {
				t.Fatalf("the worktree's row: %+v; events %v", tr, l.kinds(run.ID))
			}
			if got := leases(e); !maps.Equal(got, map[int]string{shared: "build.1 " + ledger.TreeWorktree}) {
				t.Fatalf("the leases while the end is not in the ledger: %v", got)
			}
			// Another run's tree draws the number: it is drawn again.
			fl.ports.script(shared)
			before := len(fl.ports.draws())
			other := launchManaged(t, e, repoManifestCrew(dir, m, work("docs", "writer")))
			waitAttempt(t, e, other.ID, "docs", ledger.AttemptRunning)
			ow, _ := l.treeRow(other.ID, "docs", 1, ledger.TreeWorktree)
			if draws := fl.ports.draws()[before:]; len(draws) != 2 || draws[0] != shared || draws[1] == shared || ow.Ports["web"] != draws[1] {
				t.Fatalf("the other run's worktree: %v, draws %v", ow.Ports, draws)
			}
			// The end written: its own commit let through, or the run's
			// next, a pause, carrying the refused one.
			if tc.refused {
				pauseRun(t, e, run.ID, true)
				if kinds := l.kinds(run.ID); !slices.Equal(kinds[len(kinds)-2:], []string{"tree.ended", "run.paused"}) {
					t.Fatalf("the pause's commit did not carry the refused end: %v", kinds)
				}
			} else {
				release()
			}
			l.waitTreeRowsEnded(t, run.ID, "build", 1, ledger.TreeWorktree)
			waitTreesEnded(t, e, run.ID)
			if got := leases(e); !maps.Equal(got, map[int]string{ow.Ports["web"]: "docs.1 " + ledger.TreeWorktree}) {
				t.Fatalf("the leases once the end is in the ledger: %v", got)
			}
			stopRun(t, e, other.ID)
			stopRun(t, e, run.ID)
		})
	}
}

// Two ports of one tree never share a number (EN1 PR-3; the independent
// review of #426): natively two binds of 127.0.0.1:0 may give one number
// twice, so a number an earlier port of the tree drew is drawn again for
// the later one. The executor scripted to repeat a number, the tree's two
// ports differ and both are leased to it, and its step's environment, its
// session, its task files, its event, its row and its receipt all carry
// the distinct numbers.
func TestATreesTwoPortsNeverShareANumber(t *testing.T) {
	const repeated = 41500
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	fl.ports.script(repeated, repeated)
	m := Manifest{Ports: []string{"web", "db"}, Setup: []Check{shStep("env", `env > "$CONDUCTOR_STATE_DIR/env.txt"`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ports := map[string]int{"web": repeated, "db": fakePortsFirst}
	if draws := fl.ports.draws(); !slices.Equal(draws, []int{repeated, repeated, fakePortsFirst}) {
		t.Fatalf("the draws: %v", draws)
	}
	worktree := "build.1 " + ledger.TreeWorktree
	if got := leases(e); !maps.Equal(got, map[int]string{repeated: worktree, fakePortsFirst: worktree}) {
		t.Fatalf("the leases: %v", got)
	}
	web, db := strconv.Itoa(ports["web"]), strconv.Itoa(ports["db"])
	if env := envFile(t, filepath.Join(a.Attempt.Worktree, ".conductor", "state", "env.txt")); env["CONDUCTOR_PORT_WEB"] != web || env["CONDUCTOR_PORT_DB"] != db {
		t.Fatalf("the step's ports: %v", conductorVars(env))
	}
	fl.mu.Lock()
	call := fl.calls[len(fl.calls)-1]
	fl.mu.Unlock()
	if call.inject["CONDUCTOR_PORT_WEB"] != web || call.inject["CONDUCTOR_PORT_DB"] != db {
		t.Fatalf("the session's ports: %v", call.inject)
	}
	md, f := taskFileOf(t, a)
	if f.Manifest == nil || !maps.Equal(f.Manifest.Ports, ports) || !strings.Contains(md, "web "+web+" (`CONDUCTOR_PORT_WEB`)") || !strings.Contains(md, "db "+db+" (`CONDUCTOR_PORT_DB`)") {
		t.Fatalf("the task file's ports: %+v\n%s", f.Manifest, md)
	}
	if p := l.payloads(run.ID, "manifest.ports"); len(p) != 1 {
		t.Fatalf("manifest.ports: %v", p)
	} else if got, _ := p[0]["ports"].(map[string]int); !maps.Equal(got, ports) {
		t.Fatalf("manifest.ports: %v", p[0])
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); !maps.Equal(tr.Ports, ports) {
		t.Fatalf("the worktree's row: %+v", tr)
	}
	if rows := l.stepRows(run.ID, "build", 1); len(rows) != 1 || !maps.Equal(rows[0].Ports, ports) {
		t.Fatalf("the receipt:\n%s", rowWords(rows))
	}
	stopRun(t, e, run.ID)
}

// A port's draws are bounded (EN1 PR-3; the independent review of #426):
// a number another live tree holds is drawn again at most maxPortDraws
// times a port, so a tree never loops on held numbers. A worktree whose
// every draw for its port is the number another run's tree holds fails at
// manifest with the words after exactly maxPortDraws draws, attributed by
// a worktree's rule (uncounted, the run paused), the tree never made: no
// row, no manifest.resolved, nothing written in it, no lease.
func TestAPortsDrawsAreBounded(t *testing.T) {
	const held = 41500
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	m := Manifest{Ports: []string{"web"}, Setup: []Check{shStep("deps", "true")}}
	fl.ports.script(held)
	holder := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	waitAttempt(t, e, holder.ID, "build", ledger.AttemptRunning)
	fl.ports.script(slices.Repeat([]int{held}, maxPortDraws)...)
	before := len(fl.ports.draws())
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("docs", "writer")))
	attemptFailed(t, e, run.ID, "docs", 1)
	row := l.attemptWhen(t, run.ID, "docs", 1, failedRow)
	words := fmt.Sprintf("the port web: %d numbers drawn, each another tree's", maxPortDraws)
	if row.Stage != "manifest" || row.Error != words {
		t.Fatalf("docs' attempt 1: %+v\nwant %q", row, words)
	}
	if draws := fl.ports.draws()[before:]; len(draws) != maxPortDraws || slices.ContainsFunc(draws, func(n int) bool { return n != held }) {
		t.Fatalf("docs' draws: %v", draws)
	}
	if failed := l.payloads(run.ID, "attempt.failed"); len(failed) != 1 || failed[0]["uncounted"] != true || failed[0]["stage"] != "manifest" {
		t.Fatalf("attempt.failed: %v", failed)
	}
	why := "attempt 1 could not be made usable at manifest: " + words
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
	if ts := taskOf(t, e, run.ID, "docs"); ts.State != ledger.TaskReady || ts.Reason != why {
		t.Fatalf("docs: %+v", ts)
	}
	if _, err := os.Lstat(filepath.Join(row.Worktree, ".conductor")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("written in the worktree: %v", err)
	}
	if _, ok := l.treeRow(run.ID, "docs", 1, ledger.TreeWorktree); ok || l.count(run.ID, "manifest.resolved") != 0 || len(l.stepRows(run.ID, "docs", 1)) != 0 {
		t.Fatalf("a tree was made: %v", l.kinds(run.ID))
	}
	if got := leases(e); !maps.Equal(got, map[int]string{held: "build.1 " + ledger.TreeWorktree}) {
		t.Fatalf("the leases: %v", got)
	}
	stopRun(t, e, run.ID)
	stopRun(t, e, holder.ID)
}

// The native executor draws a port of the loopback and lets it go: the
// number it gives is free to bind. Another process may take a free number
// between the two, so a number that cannot be bound is drawn again, three
// times at most: one that the executor held would fail every time.
func TestTheNativeExecutorDrawsAPortAndLetsItGo(t *testing.T) {
	var errs []string
	for range 3 {
		n, err := nativeExec().port(t.Context())
		if err != nil || n < 1 || n > maxPort {
			t.Fatalf("drawn %d: %v", n, err)
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
		if err == nil {
			l.Close()
			return
		}
		errs = append(errs, err.Error())
	}
	t.Fatalf("no drawn port could be bound: %v", errs)
}

// The bounds hold on the expanded form (EN1 PR-3): a manifest the rules
// accept, whose placeholders put the state directory in past an argv
// element's 4,096 bytes, a probe URL's 2,048 or the preview's 512, fails
// its tree at manifest with the size it expands to, before anything is
// written or run in it, attributed by the tree's rule: uncounted in an
// attempt's worktree, the run paused; counted in the checkout of a result,
// refused before its checks, the next attempt where the refused one
// started. The preview and the probe reach the bound in a checkout, by a
// file a result commits: this build refuses both at the launch (PR-4 runs
// them), and a tree checks the bounds before that refusal.
func TestATreeWhoseManifestExpandsPastItsBoundsFailsAtManifest(t *testing.T) {
	long := strings.Repeat(stateDirPlaceholder, 180)
	t.Run("an argv element in a worktree", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		c := repoManifestCrew(dir, Manifest{Setup: []Check{{ID: "cache", Argv: []string{"mkdir", "-p", long}}}}, work("build", "lead"))
		if err := c.ValidateSave(); err != nil || len(long) > maxArg {
			t.Fatalf("the manifest is not valid: %v (%d bytes)", err, len(long))
		}
		run := launchManaged(t, e, c)
		attemptFailed(t, e, run.ID, "build", 1)
		row := l.attemptWhen(t, run.ID, "build", 1, failedRow)
		state := filepath.Join(row.Worktree, ".conductor", "state")
		words := fmt.Sprintf("setup cache: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))
		if row.Stage != "manifest" || row.Error != words {
			t.Fatalf("attempt 1: %+v\nwant %q", row, words)
		}
		why := "attempt 1 could not be made usable at manifest: " + words
		waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
		if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskReady || ts.Reason != why {
			t.Fatalf("the task: %+v", ts)
		}
		if _, err := os.Lstat(filepath.Join(row.Worktree, ".conductor")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("written in the worktree: %v", err)
		}
		if _, ok := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); ok || len(l.stepRows(run.ID, "build", 1)) != 0 || l.count(run.ID, "manifest.resolved") != 0 || len(leases(e)) != 0 {
			t.Fatalf("a tree was made: %v, receipts:\n%s", l.kinds(run.ID), rowWords(l.stepRows(run.ID, "build", 1)))
		}
		stopRun(t, e, run.ID)
	})
	url := "http://127.0.0.1/"
	for _, tc := range []struct {
		name, content string
		words         func(state string) string
	}{
		{"the preview in a checkout", `{"schema": 1, "preview": "` + url + strings.Repeat(stateDirPlaceholder, 20) + `"}`, func(state string) string {
			return fmt.Sprintf("preview expands to %s bytes, more than 512", byteCount(len(url)+20*len(state)))
		}},
		{"a probe's url in a checkout", `{"schema": 1, "ready": [{"id": "up", "url": "` + url + strings.Repeat(stateDirPlaceholder, 90) + `"}]}`, func(state string) string {
			return fmt.Sprintf("ready up: its url expands to %s bytes, more than 2,048", byteCount(len(url)+90*len(state)))
		}},
		{"an argv element in a checkout", `{"schema": 1, "setup": [{"id": "cache", "argv": ["mkdir", "-p", "` + long + `"]}]}`, func(state string) string {
			return fmt.Sprintf("setup cache: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`)
			e, _, l := managedEngine(t)
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", "true")}
			run := launchManaged(t, e, fileCrew(dir, build))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitManifestFile(t, a.Attempt.Worktree, tc.content)
			submit(t, e, a.Attempt.SessionID, Result{Summary: "a manifest that grows"})
			a2 := attemptN(t, e, run.ID, "build", 2)
			ck := a.Attempt.Worktree + ".check"
			row := l.attemptRow(t, run.ID, "build", 1)
			words := manifestFile + " at " + short(row.ResultCommit) + ": " + tc.words(filepath.Join(ck, ".conductor", "state"))
			if row.State != ledger.AttemptFailed || row.Stage != "manifest" || row.Error != words || a2.Attempt.Start != a.Attempt.Start {
				t.Fatalf("attempt 1: %+v\nwant %q; attempt 2 starts at %s", row, words, short(a2.Attempt.Start))
			}
			if _, err := os.Lstat(filepath.Join(ck, ".conductor")); !errors.Is(err, fs.ErrNotExist) || l.count(run.ID, "check.started") != 0 {
				t.Fatalf("written or run in the checkout: %v, %d checks", err, l.count(run.ID, "check.started"))
			}
			if _, ok := l.treeRow(run.ID, "build", 1, ledger.TreeCheckout); ok {
				t.Fatal("the checkout became a tree")
			}
			if r, _ := e.Get(run.ID); r.Paused {
				t.Fatal("a refused result paused the run")
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A manifest's env names are prerequisites (EN1 PR-3): before any of the
// prerequisites' commands, each name must be in the server's
// envPassthrough and set in the server's environment. One that is not
// pauses the run before its first dispatch with the name in the words, the
// commands not run and nothing spent; one the host does not have says a
// restart fixes it, and an unpause alone finds the host as it was (R13:
// nothing reloads the server's environment). Names allowed and set pass,
// with commands or none, and reach the commands.
func TestTheEnvNamesArePrerequisites(t *testing.T) {
	t.Setenv("EN1_SET", "yes")
	for _, tc := range []struct {
		name, env, words string
		passthrough      []string
	}{
		{"a name the server does not allow", "EN1_SET", "the prerequisites failed: EN1_SET is not in the server's envPassthrough", nil},
		{"a name the host does not have", "EN1_UNSET", "the prerequisites failed: the host has no EN1_UNSET: set it where the server starts, start it again and continue the run", []string{"EN1_SET", "EN1_UNSET"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			e.SetEnvPassthrough(tc.passthrough)
			m := Manifest{Env: []string{tc.env}, Prerequisites: []Check{shStep("tools", "true")}, Setup: []Check{shStep("deps", "true")}}
			run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
			waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == tc.words })
			if ts := taskOf(t, e, run.ID, "build"); ts.Attempts != 0 || len(l.prereqRows(run.ID)) != 0 || l.count(run.ID, "prerequisites.passed") != 0 {
				t.Fatalf("spent or run: %+v, prerequisites %+v", ts, l.prereqRows(run.ID))
			}
			// An unpause alone: the host is as it was. The pause, the
			// unpause and the second pause, in the ledger.
			pauseRun(t, e, run.ID, false)
			waitTrue(t, "the run paused again", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == tc.words })
			waitFake(t, "the second pause recorded", func() bool { return l.count(run.ID, "run.paused") == 3 })
			if p := l.payloads(run.ID, "run.paused"); p[0]["why"] != tc.words || p[1]["paused"] != false || p[2]["why"] != tc.words ||
				taskOf(t, e, run.ID, "build").Attempts != 0 || len(l.prereqRows(run.ID)) != 0 {
				t.Fatalf("after the unpause: run.paused %v, prerequisites %+v", p, l.prereqRows(run.ID))
			}
			stopRun(t, e, run.ID)
		})
	}
	for _, tc := range []struct {
		name     string
		commands []Check
	}{{"names allowed and set reach the commands", []Check{shStep("token", `test "$EN1_SET" = yes`)}}, {"names alone", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			e.SetEnvPassthrough([]string{"EN1_SET"})
			run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Env: []string{"EN1_SET"}, Prerequisites: tc.commands}, work("build", "lead")))
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			rows := l.prereqRows(run.ID)
			if len(rows) != len(tc.commands) || l.count(run.ID, "prerequisites.passed") != 1 {
				t.Fatalf("the prerequisites: %+v", rows)
			}
			for _, row := range rows {
				if row.State != ledger.CheckFinished || row.Exit != 0 || row.Error != "" {
					t.Fatalf("the prerequisite %s: %+v", row.ID, row)
				}
			}
			stopRun(t, e, run.ID)
		})
	}
}

// The env rule holds in every tree, whichever commit's manifest named the
// name (EN1 PR-3): a server started again without a name a result added
// (one its envPassthrough no longer allows, or one its environment no
// longer has) fails the next tree made at that result in its worktree, at
// manifest, uncounted, the run's scheduling paused with the words, as any
// failure of a worktree's manifest is: the continuation's prerequisites,
// at a base whose manifest names nothing, pass, and the worktree's own
// check is what stops the name.
func TestTheEnvRuleHoldsInAWorktreeAfterARestart(t *testing.T) {
	t.Setenv("EN1_ADDED", "yes")
	for _, tc := range []struct {
		name, words string
		passthrough []string
		unset       bool
	}{
		{"no longer allowed", "EN1_ADDED is not in the server's envPassthrough", nil, false},
		{"no longer set", "the host has no EN1_ADDED: set it where the server starts, start it again and continue the run", []string{"EN1_ADDED"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`)
			e, fl, l := managedEngine(t)
			e.SetEnvPassthrough([]string{"EN1_ADDED"})
			run := launchManaged(t, e, fileCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
			b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			// build's result names EN1_ADDED, which this server allows and
			// has: its checkout passes. docs is held at its place.
			commitManifestFile(t, b.Attempt.Worktree, `{"schema": 1, "env": ["EN1_ADDED"], "setup": [{"id": "deps", "argv": ["sh", "-c", "test \"$EN1_ADDED\" = yes"]}]}`)
			hold := make(chan struct{})
			fl.mu.Lock()
			fl.reserveHold = hold
			fl.mu.Unlock()
			t.Cleanup(func() { close(hold) })
			submit(t, e, b.Attempt.SessionID, Result{Summary: "needs EN1_ADDED"})
			result := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Attempt.Result
			stopRun(t, e, run.ID)
			// The server started again without it.
			if tc.unset {
				os.Unsetenv("EN1_ADDED")
			}
			e2, _ := readyEngine(t)
			e2.UseLedger(l)
			e2.SetEnvPassthrough(tc.passthrough)
			next, err := e2.ContinueRun(t.Context(), run.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			attemptFailed(t, e2, next.ID, "docs", 1)
			row := l.attemptWhen(t, next.ID, "docs", 1, failedRow)
			if row.StartCommit != result || row.Stage != "manifest" || row.Error != tc.words {
				t.Fatalf("docs' attempt 1 (at %s): %+v", short(result), row)
			}
			why := "attempt 1 could not be made usable at manifest: " + tc.words
			waitTrue(t, "the continuation paused", func() bool { r, _ := e2.Get(next.ID); return r.Paused && r.PausedWhy == why })
			if ts := taskOf(t, e2, next.ID, "docs"); ts.State != ledger.TaskReady || ts.Reason != why {
				t.Fatalf("docs: %+v", ts)
			}
			stopRun(t, e2, next.ID)
		})
	}
}
