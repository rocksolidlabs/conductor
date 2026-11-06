package crew

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A failed receipt's class (docs/plans/review-repair.md, RV1 PR-2a), from
// receipts the engine wrote: each case runs a crew whose check or step
// ends one way and classes the receipt the ledger holds, never a row made
// up for the test.

// classCase is a receipt as the ledger holds it, with its attempt and the
// other receipts of its check on its commit, and the class and words it
// must have.
type classCase struct {
	a     ledger.AttemptRow
	r     ledger.CheckRow
	same  []ledger.CheckRow
	class string
	words string
}

// buildWith is a crew of one work task, build, whose checks are checks.
func buildWith(dir string, checks ...Check) Crew {
	build := work("build", "lead")
	build.Checks = checks
	return repoCrew(dir, build)
}

// firstResult launches c on e and has build's attempt 1 commit a result
// with broken.txt in it (which a step can tell from the worktree, made
// before the result): it returns the run and the attempt as submitted.
func firstResult(t *testing.T, e *Engine, c Crew) (Run, TaskState) {
	t.Helper()
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "broken.txt", "yes\n", "built")
	return run, a
}

// endedAs is build's attempt 1 and its receipt id at k as the fake ledger
// holds them once the attempt is in the state.
func endedAs(t *testing.T, l *fakeLedger, runID, state, id string, k int) (ledger.AttemptRow, ledger.CheckRow) {
	t.Helper()
	a := l.attemptWhen(t, runID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == state })
	r, ok := rowOf(l.stepRows(runID, "build", 1), id, k)
	if !ok {
		t.Fatalf("no receipt %s k%d: %s", id, k, rowWords(l.stepRows(runID, "build", 1)))
	}
	return a, r
}

// sqlEngineIn is an engine over a ledger of its own in dir, which the
// test closes.
func sqlEngineIn(t *testing.T, dir string) (*Engine, *ledger.Ledger) {
	t.Helper()
	e, _ := readyEngine(t)
	ld, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.UseLedger(SQLLedger(ld))
	return e, ld
}

// sqlAttemptWhen waits for the task's attempt n in ld to satisfy ok.
func sqlAttemptWhen(t *testing.T, ld *ledger.Ledger, runID, taskID string, n int, ok func(ledger.AttemptRow) bool) ledger.AttemptRow {
	t.Helper()
	var row ledger.AttemptRow
	waitFake(t, "the attempt's row", func() bool {
		attempts, err := ld.Attempts(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		row = byN(attempts, taskID, n)
		return row.N == n && ok(row)
	})
	return row
}

// sqlCheck is the receipt in ld, the zero row when there is none.
func sqlCheck(t *testing.T, ld *ledger.Ledger, runID, taskID string, n int, id string, k int) ledger.CheckRow {
	t.Helper()
	r, _, err := ld.Check(context.Background(), runID, taskID, n, id, k)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// flakyChain spends build's attempts on c, whose check or step id fails
// until mark exists, checks attempt 3's candidate again (failing, attempt
// 4), then again once the mark is made (passing, attempt 5): three
// receipts of id on one commit. It returns attempt 3, its receipt, and the
// receipts ChecksOn reads of id on that commit.
func flakyChain(t *testing.T, c Crew, phase, id, mark string) (ledger.AttemptRow, ledger.CheckRow, []ledger.CheckRow) {
	t.Helper()
	e, ld := sqlEngine(t)
	run := launchManaged(t, e, c)
	c3 := spend(t, e, run.ID, "build", "built.txt").Attempt.Result
	sqlAttemptWhen(t, ld, run.ID, "build", 3, failedRow)
	if rc, err := recheck(e, run.ID, "r1", "build", 3, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again: %+v %v", rc, err)
	}
	sqlAttemptWhen(t, ld, run.ID, "build", 4, failedRow)
	mustMark(t, mark)
	if rc, err := recheck(e, run.ID, "r2", "build", 4, c3); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Check again of attempt 4: %+v %v", rc, err)
	}
	sqlAttemptWhen(t, ld, run.ID, "build", 5, succeededRow)
	ctx := context.Background()
	attempts, err := ld.Attempts(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	same, err := ld.ChecksOn(ctx, []string{run.ID}, "build", phase, id, c3)
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != 3 {
		t.Fatalf("the receipts of %s on %s: %s", id, short(c3), rowWords(same))
	}
	stopRun(t, e, run.ID)
	return byN(attempts, "build", 3), sqlCheck(t, ld, run.ID, "build", 3, id, 1), same
}

// without is rows without those of attempt n.
func without(rows []ledger.CheckRow, n int) []ledger.CheckRow {
	return slices.DeleteFunc(slices.Clone(rows), func(r ledger.CheckRow) bool { return r.N == n })
}

// passOf is the receipt of attempt n among rows, changed by f: a field the
// engine never writes differently within one chain (another task, another
// argv or manifest on the same commit), changed on the engine's own row.
func passOf(t *testing.T, rows []ledger.CheckRow, n int, f func(*ledger.CheckRow)) ledger.CheckRow {
	t.Helper()
	i := slices.IndexFunc(rows, func(r ledger.CheckRow) bool { return r.N == n })
	if i < 0 {
		t.Fatalf("no receipt of attempt %d: %s", n, rowWords(rows))
	}
	r := rows[i]
	r.Argv = slices.Clone(r.Argv)
	f(&r)
	return r
}

// TestClassifyFailure goes through the plan's table in its order, every
// clause of every row, each on a receipt the engine wrote.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) []classCase
	}{
		// cancelled: the attempt's state, whatever the receipt says.
		{"cancelled: a stop interrupts the check (not killed)", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", gated(gate))))
			l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
			stopRun(t, e, run.ID)
			a, r := endedAs(t, l, run.ID, ledger.AttemptStopped, "e2e", 1)
			if r.State != ledger.CheckInterrupted || r.Killed {
				t.Fatalf("the stopped check's receipt: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureCancelled, words: "stopped"}}
		}},
		{"cancelled: a cancel", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", gated(gate))))
			l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
			if err := e.CancelTask(t.Context(), run.ID, "build"); err != nil {
				t.Fatal(err)
			}
			a, _ := endedAs(t, l, run.ID, ledger.AttemptCancelled, "e2e", 1)
			r := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State != ledger.CheckRunning })
			stopRun(t, e, run.ID)
			return []classCase{{a: a, r: r, class: FailureCancelled, words: "cancelled"}}
		}},
		{"cancelled: a withdrawal", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			docs := work("docs", "writer", "build")
			docs.Checks = []Check{shStep("e2e", gated(gate))}
			run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("rb", "critic", "build"), docs))
			commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "b\n", "built")
			rb := waitAttempt(t, e, run.ID, "rb", ledger.AttemptRunning)
			commitResult(t, e, waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning), "docs.txt", "d\n", "docs")
			l.rowWhen(t, run.ID, "docs", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckRunning })
			asksChanges(t, e, rb)
			a := l.attemptWhen(t, run.ID, "docs", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptCancelled })
			r := l.rowWhen(t, run.ID, "docs", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State != ledger.CheckRunning })
			stopRun(t, e, run.ID)
			if a.Stage != "withdrawn" {
				t.Fatalf("docs' attempt 1: %+v", a)
			}
			return []classCase{{a: a, r: r, class: FailureCancelled, words: "withdrawn"}}
		}},

		// infrastructure.
		{"infrastructure: the check never ran", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, _ := firstResult(t, e, buildWith(dir, Check{ID: "e2e", Argv: []string{filepath.Join(t.TempDir(), "no-such-command")}}))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "it never ran: could not run"}}
		}},
		{"infrastructure: the server ended under the check", func(t *testing.T) []classCase {
			dir := newRepo(t)
			ldir := t.TempDir()
			e, ld := sqlEngineIn(t, ldir)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", gated(gate))))
			waitFake(t, "the check running in the ledger", func() bool {
				return sqlCheck(t, ld, run.ID, "build", 1, "e2e", 1).State == ledger.CheckRunning
			})
			// The owner ends: its ledger closed under the run, whose end
			// writes nothing more; the next owner's recovery concludes it.
			if err := ld.Close(); err != nil {
				t.Fatal(err)
			}
			_ = e.Stop(t.Context(), run.ID)
			again, err := ledger.Open(ldir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { again.Close() })
			if _, err := again.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			a := sqlAttemptWhen(t, again, run.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptInterrupted })
			r := sqlCheck(t, again, run.ID, "build", 1, "e2e", 1)
			if r.State != ledger.CheckInterrupted {
				t.Fatalf("the check after the recovery: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "the server ended"}}
		}},
		{"infrastructure: the ledger refused the running row and the engine ended the check", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			gate := filepath.Join(t.TempDir(), "gate")
			t.Cleanup(func() { open(t, gate) })
			l.mu.Lock()
			l.failOn, l.failOnce = "check.running", true
			l.mu.Unlock()
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", gated(gate))))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			if r.State != ledger.CheckFinished || r.Exit != -1 || r.Signal == "" {
				t.Fatalf("the check the engine ended: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "the engine ended it: the ledger could not record the check"}}
		}},
		{"infrastructure: the ledger refused a check's outcome", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			l.mu.Lock()
			l.failOn, l.failOnce = "check.finished", true
			l.mu.Unlock()
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "true")))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			if r.State != ledger.CheckRunning {
				t.Fatalf("the check whose outcome was refused: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "the ledger never recorded its outcome: the ledger could not record the check's outcome"}}
		}},
		{"infrastructure: the ledger refused a verify step's outcome", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			l.mu.Lock()
			l.failOn, l.failOnce = "step.finished[phase=verify]", true
			l.mu.Unlock()
			run, _ := firstResult(t, e, repoManifestCrew(dir, Manifest{Verify: []Check{shStep("e2e", "true")}}, work("build", "lead")))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			if r.State != ledger.CheckFinished || r.Exit != 0 || !r.Stable {
				t.Fatalf("the step whose outcome was refused: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "the ledger never recorded its outcome: the ledger could not record the outcome of the step e2e"}}
		}},
		{"infrastructure: it exited 0 and its checkout could not be compared", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			var tree string
			e.beforeCompare = func(_ context.Context, id string) {
				// The server's comparison fails: the checkout's link to
				// its repository gone, which the check never touched.
				if id == "e2e" {
					git := filepath.Join(tree+".check", ".git")
					_ = os.RemoveAll(git)
					_ = os.WriteFile(git, []byte("gitdir: "+filepath.Join(tree, "no-such-repository")+"\n"), 0o644)
				}
			}
			run := launchManaged(t, e, buildWith(dir, shStep("e2e", "true")))
			at := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			tree = at.Attempt.Worktree
			commitResult(t, e, at, "built.txt", "yes\n", "built")
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			if r.Exit != 0 || r.Stable || !strings.Contains(r.Error, "could not be compared") {
				t.Fatalf("the check whose checkout could not be compared: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureInfrastructure, words: "could not compare its checkout"}}
		}},
		{"infrastructure: a setup step, a service and a ready probe of the checkout", func(t *testing.T) []classCase {
			var out []classCase
			for _, m := range []struct {
				id string
				m  Manifest
			}{
				{"deps", Manifest{Setup: []Check{shStep("deps", "test ! -e broken.txt")}}},
				{"web", Manifest{Services: []Check{shStep("web", `test -e broken.txt && exit 3; echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; while :; do sleep 0.05; done`)}, Ready: []Probe{byFile("up", "web.pid")}}},
				{"page", Manifest{Ready: []Probe{{ID: "page", Argv: []string{"sh", "-c", "test ! -e broken.txt"}, IntervalSeconds: 1, TimeoutSeconds: 1}}}},
			} {
				dir := newRepo(t)
				e, _, l := managedEngine(t)
				run, _ := firstResult(t, e, repoManifestCrew(dir, m.m, buildWith(dir, shStep("ok", "true")).Tasks...))
				a := l.attemptWhen(t, run.ID, "build", 1, failedRow)
				var r ledger.CheckRow
				for _, c := range l.stepRows(run.ID, "build", 1) {
					if c.ID == m.id && c.Subject == a.ResultCommit && failedOutcome(c) {
						r = c
					}
				}
				stopRun(t, e, run.ID)
				if r.State == "" {
					t.Fatalf("no failing receipt of %s in the checkout: %s", m.id, rowWords(l.stepRows(run.ID, "build", 1)))
				}
				out = append(out, classCase{a: a, r: r, class: FailureInfrastructure, words: "the " + r.Phase + " step " + m.id + " failed as the checkout was made ready"})
			}
			return out
		}},

		// flaky: another receipt of the same check on the same commit passed.
		{"flaky: a task's check, and what never makes it flaky", func(t *testing.T) []classCase {
			dir := newRepo(t)
			mark := filepath.Join(t.TempDir(), "passes")
			a, r, same := flakyChain(t, buildWith(dir, markedCheck("test", mark)), ledger.PhaseCheck, "test", mark)
			other := func(f func(*ledger.CheckRow)) []ledger.CheckRow {
				return append(without(same, 5), passOf(t, same, 5, f))
			}
			return []classCase{
				{a: a, r: r, same: same, class: FailureFlaky, words: "passed on the same commit as attempt 5"},
				{a: a, r: r, same: without(same, 5), class: FailureCode, words: "exited 1"},
				{a: a, r: r, same: other(func(c *ledger.CheckRow) { c.TaskID = "docs" }), class: FailureCode, words: "exited 1"},
				{a: a, r: r, same: other(func(c *ledger.CheckRow) { c.Argv = append(c.Argv, "-v") }), class: FailureCode, words: "exited 1"},
				{a: a, r: r, same: other(func(c *ledger.CheckRow) { c.Subject = strings.Repeat("0", 40) }), class: FailureCode, words: "exited 1"},
			}
		}},
		{"flaky: a verify step of the same manifest, never of another", func(t *testing.T) []classCase {
			dir := newRepo(t)
			mark := filepath.Join(t.TempDir(), "passes")
			a, r, same := flakyChain(t, repoManifestCrew(dir, Manifest{Verify: []Check{shStep("e2e", "test -e "+mark)}}, work("build", "lead")), ledger.PhaseVerify, "e2e", mark)
			return []classCase{
				{a: a, r: r, same: same, class: FailureFlaky, words: "passed on the same commit"},
				{a: a, r: r, same: append(without(same, 5), passOf(t, same, 5, func(c *ledger.CheckRow) { c.Manifest += "-other" })), class: FailureCode, words: "exited 1"},
			}
		}},

		// unavailable.
		{"unavailable: no receipt in the ledger", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, ld := sqlEngine(t)
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "exit 1")))
			a := sqlAttemptWhen(t, ld, run.ID, "build", 1, failedRow)
			stopRun(t, e, run.ID)
			return []classCase{{a: a, r: sqlCheck(t, ld, run.ID, "build", 1, "e2e", 2), class: FailureUnavailable, words: "no receipt"}}
		}},
		{"unavailable: its log dropped and no output kept; code with its tail kept", func(t *testing.T) []classCase {
			var out []classCase
			for _, c := range []struct {
				tail        []byte
				class, word string
			}{{nil, FailureUnavailable, "dropped"}, {[]byte("FAIL\n"), FailureCode, "exited 1"}} {
				dir := newRepo(t)
				e, _, l := managedEngine(t)
				l.mu.Lock()
				l.dropLogs = true
				l.mu.Unlock()
				// A Runtime that keeps the log but hands back no tail of it
				// (a remote one), or the tail it kept.
				offer(t, e, &sessiontest.FakeRuntime{Result: func(s ext.CommandSpec) ext.CommandResult {
					if s.Started != nil {
						s.Started(ext.ProcessInfo{PID: 4242, Group: 4242, StartedAt: time.Now()})
					}
					_, _ = io.WriteString(s.Log, "FAIL\n")
					return ext.CommandResult{Exit: 1, Tail: c.tail, Total: 5}
				}})
				run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "exit 1")))
				a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
				stopRun(t, e, run.ID)
				if !r.LogDropped || r.Tail != string(c.tail) {
					t.Fatalf("the receipt with its log dropped: %+v", r)
				}
				out = append(out, classCase{a: a, r: r, class: c.class, words: c.word})
			}
			return out
		}},
		{"unavailable: a receipt with no failing fact; code: one that exited non-zero", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, _ := firstResult(t, e, buildWith(dir, shStep("vet", "true"), shStep("test", "echo FAIL; exit 1")))
			a, vet := endedAs(t, l, run.ID, ledger.AttemptFailed, "vet", 1)
			_, test := endedAs(t, l, run.ID, ledger.AttemptFailed, "test", 1)
			stopRun(t, e, run.ID)
			return []classCase{
				{a: a, r: vet, class: FailureUnavailable, words: "holds nothing that says why the check failed"},
				{a: a, r: test, class: FailureCode, words: "exited 1"},
			}
		}},

		// code.
		{"code: a signal", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "kill -TERM $$")))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			return []classCase{{a: a, r: r, class: FailureCode, words: "ended by the signal"}}
		}},
		{"code: a checkout left changed", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "echo more >> broken.txt")))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			if r.Exit != 0 || r.Stable {
				t.Fatalf("the check that changed its checkout: %+v", r)
			}
			return []classCase{{a: a, r: r, class: FailureCode, words: "left the checkout changed: changed tracked files: broken.txt"}}
		}},
		{"code: killed and exited non-zero", func(t *testing.T) []classCase {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			offer(t, e, &sessiontest.FakeRuntime{Result: func(s ext.CommandSpec) ext.CommandResult {
				if s.Started != nil {
					s.Started(ext.ProcessInfo{PID: 4242, Group: 4242, StartedAt: time.Now()})
				}
				_, _ = io.WriteString(s.Log, "FAIL\n")
				return ext.CommandResult{Exit: 3, Killed: true, Tail: []byte("FAIL\n"), Total: 5}
			}})
			run, _ := firstResult(t, e, buildWith(dir, shStep("e2e", "exit 3")))
			a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
			stopRun(t, e, run.ID)
			return []classCase{{a: a, r: r, class: FailureCode, words: "exited 3; a process it left behind held its output"}}
		}},
		{"code: a timeout queued behind another check's slot", func(t *testing.T) []classCase {
			a, r := timedOutBehindTheSlot(t)
			return []classCase{{a: a, r: r, class: FailureCode, words: "timed out after 1 s: a hang in the code, or a machine too slow"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i, k := range c.run(t) {
				got := ClassifyFailure(k.a, k.r, k.same)
				if got.Class != k.class || !strings.Contains(got.Words, k.words) {
					t.Fatalf("case %d (%s k%d, attempt %s at %q): %+v, want %s with %q", i, k.r.ID, k.r.K, k.a.State, k.a.Stage, got, k.class, k.words)
				}
			}
		})
	}
}

// timedOutBehindTheSlot runs a check allowed 1 s that never ends, queued
// behind another check that holds the server's one check slot (the
// engine's own, checkSlots) until the queued receipt has waited more than
// twice its timeout: the receipt's span counts that wait, and its class's
// words must not. It returns the attempt and the receipt.
func timedOutBehindTheSlot(t *testing.T) (ledger.AttemptRow, ledger.CheckRow) {
	t.Helper()
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	slot := e.checkSlots()
	slot <- struct{}{} // another check runs
	held := true
	t.Cleanup(func() {
		if held {
			<-slot
		}
	})
	check := shStep("e2e", "sleep 30")
	check.TimeoutSeconds = 1
	run, _ := firstResult(t, e, buildWith(dir, check))
	queued := l.rowWhen(t, run.ID, "build", 1, "e2e", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckStarted })
	waitFake(t, "the check queued for 3 s", func() bool { return time.Since(queued.StartedAt) > 3*time.Second })
	<-slot
	held = false
	a, r := endedAs(t, l, run.ID, ledger.AttemptFailed, "e2e", 1)
	stopRun(t, e, run.ID)
	if !r.TimedOut || r.FinishedAt.Sub(r.StartedAt) < 3*time.Second {
		t.Fatalf("the timed-out receipt: timed out %v, span %s", r.TimedOut, r.FinishedAt.Sub(r.StartedAt))
	}
	return a, r
}
