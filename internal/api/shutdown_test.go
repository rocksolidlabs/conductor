package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Every run's record is written before the server exits, however its
// shutdown budget stands (#128): the ends the stops hand the records are
// waited for, within a grace of the records' own, before the records close.
// Twenty runs stopped under a budget already spent all have their records,
// each stopped.
func TestShutdownWritesEveryRunsRecordWhenItsBudgetIsSpent(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	const n = 20
	for i := range n {
		e.launchCrew(t, fmt.Sprintf("Crew %d", i), catMember("core", "immediately"))
	}
	// A budget already spent: every stop is cut short at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.srv.Shutdown(ctx)
	got, err := e.srv.records.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("%d of %d records written", len(got), n)
	}
	for _, r := range got {
		if r.StoppedAt == nil {
			t.Fatalf("record %s is not stopped: %s", r.ID, r.State)
		}
	}
}

// A run whose tree has a service that ignores SIGTERM: Server.Shutdown,
// under the CLI's budget of 10 s, ends it (SIGTERM, then SIGKILL after the
// grace, or at the teardown's cut-off armed at the shutdown's entry), and
// its pid is gone when Shutdown returns; the service's end and the tree's
// are in the ledger, written before it closed.
func TestShutdownEndsAServiceThatIgnoresSIGTERM(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := crew.Crew{ID: "served", Name: "Served", Goal: "build it", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members:  []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks:    []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it."}},
		Manifest: &crew.Manifest{Services: []crew.Check{{ID: "web", Argv: []string{"sh", "-c", `trap "" TERM; echo $$ > "` + pidFile + `"; while :; do sleep 0.05; done`}}}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	pid := servicePid(t, pidFile)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the service %d is not running: %v", pid, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("the service's process %d outlived the shutdown", pid)
	}
	ld, err := ledger.Open(filepath.Join(e.srv.cfg.DataDir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	rows, err := ld.Checks(context.Background(), run.ID, "build", 1, false)
	if err != nil || len(rows) != 1 || rows[0].Phase != ledger.PhaseService || rows[0].State != ledger.CheckFinished || rows[0].Signal != "killed" {
		t.Fatalf("the service's row after the shutdown: %+v %v", rows, err)
	}
	trees, err := ld.Trees(context.Background(), run.ID)
	if err != nil || len(trees) != 1 || trees[0].State != ledger.TreeEnded || trees[0].Interrupted {
		t.Fatalf("the tree after the shutdown: %+v %v", trees, err)
	}
}

// A run whose sole task was cancelled while a prerequisite was held at its
// gate is finished, the prerequisites' generation ended by that commit;
// the shutdown's stop loop stops it as any other (its StoppedAt is nil),
// and the step's process is gone and its row interrupted before the
// ledger closes.
func TestShutdownEndsAFinishedRunsPrerequisitesBeforeTheLedgerCloses(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	dir := t.TempDir()
	gate, pidFile := filepath.Join(dir, "gate"), filepath.Join(dir, "pid")
	c := crew.Crew{ID: "gated", Name: "Gated", Goal: "build it", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members:  []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks:    []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it."}},
		Manifest: &crew.Manifest{Prerequisites: []crew.Check{{ID: "tools", Argv: []string{"sh", "-c", `echo $$ > "` + pidFile + `"; while [ ! -e "` + gate + `" ]; do sleep 0.05; done`}}}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for pid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if pid == 0 && time.Now().After(deadline) {
			t.Fatal("the prerequisite did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/tasks/build/cancel", adminToken, map[string]any{"commandId": "c1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d %v", resp.StatusCode, out)
	}
	if r, _ := e.srv.runs.Get(run.ID); r.Outcome != "cancelled" || r.StoppedAt != nil {
		t.Fatalf("the run after the cancel: outcome %q, stopped %v", r.Outcome, r.StoppedAt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("the prerequisite's process %d outlived the shutdown", pid)
	}
	ld, err := ledger.Open(filepath.Join(e.srv.cfg.DataDir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	rows, err := ld.Prerequisites(context.Background(), run.ID, false)
	if err != nil || len(rows) != 1 || rows[0].State != ledger.CheckInterrupted || !strings.Contains(rows[0].Error, "tools was interrupted") {
		t.Fatalf("the prerequisite's row after the shutdown: %+v %v", rows, err)
	}
}

// A finished run (its task succeeded) whose trees' cleanup step, begun under
// the run's teardown before the shutdown, would sleep past the budget:
// Server.Shutdown, under the CLI's 10 s, arms the teardown's cut-off at
// its entry, so the step is killed at the deadline less the reserve and
// its pid is gone when Shutdown returns. Once the ledger is reopened the
// step's rows are interrupted and the trees' rows ended, by the cut when
// the reaping and the rows fit the reserve, or by the recovery when the
// machine missed it. Nobody stopped the run (its StoppedAt nil, so the
// stop loop stops it, and its stop waits for its trees), or a stop from
// the UI returned early under a short context, cleaning above zero, which
// the stop loop passes over and Drain waits for: the same rows.
func TestShutdownCutsACleanupStepThatOutlastsItsBudget(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "nobody stopped it", true: "stopped from the UI before"}[stopped], func(t *testing.T) {
			e := readyEnv(t, nil)
			e.gitRepo(t)
			c := crew.Crew{ID: "cleaned", Name: "Cleaned", Goal: "build it", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
				Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
				Tasks:   []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it."}},
				Manifest: &crew.Manifest{Cleanup: []crew.Check{
					{ID: "slow", Argv: []string{"sh", "-c", `trap "" TERM; echo $$ > "$CONDUCTOR_STATE_DIR/slow.pid"; while :; do sleep 0.05; done`}},
					{ID: "after", Argv: []string{"true"}},
				}}}
			run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
			release()
			if err != nil {
				t.Fatal(err)
			}
			a := runningAttempt(t, e, run.ID, "build", 1)
			sid := a["sessionId"].(string)
			if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
				t.Fatalf("result: %d %v", resp.StatusCode, out)
			}
			wt := a["worktree"].(string)
			var pids []int
			for _, tree := range []string{wt, wt + ".check"} {
				pids = append(pids, servicePid(t, filepath.Join(tree, ".conductor", "state", "slow.pid")))
			}
			if stopped {
				short, cancel := context.WithCancel(context.Background())
				cancel()
				_ = e.srv.runs.Stop(short, run.ID)
				if r, _ := e.srv.runs.Get(run.ID); r.StoppedAt == nil || r.Cleaning == 0 {
					t.Fatalf("the run after a stop under an ended context: stopped %v, cleaning %d", r.StoppedAt, r.Cleaning)
				}
			} else if r, _ := e.srv.runs.Get(run.ID); r.StoppedAt != nil || r.Outcome != "succeeded" || r.Cleaning == 0 {
				t.Fatalf("the run before the shutdown: stopped %v, outcome %q, cleaning %d", r.StoppedAt, r.Outcome, r.Cleaning)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			e.srv.Shutdown(ctx)
			for _, pid := range pids {
				if syscall.Kill(pid, 0) == nil {
					t.Fatalf("the cleanup step's process %d outlived the shutdown", pid)
				}
			}
			ld, err := ledger.Open(filepath.Join(e.srv.cfg.DataDir, "runs"))
			if err != nil {
				t.Fatal(err)
			}
			defer ld.Close()
			bg := context.Background()
			cut := func() (string, bool) {
				rows, err := ld.Checks(bg, run.ID, "build", 1, false)
				if err != nil {
					return err.Error(), false
				}
				slow := 0
				for _, row := range rows {
					switch {
					case row.ID == "after":
						return "a cleanup step started after the cut-off", false
					case row.ID == "slow" && row.State == ledger.CheckInterrupted:
						slow++
					}
				}
				trees, err := ld.Trees(bg, run.ID)
				if err != nil {
					return err.Error(), false
				}
				ended := 0
				for _, tr := range trees {
					if tr.State == ledger.TreeEnded && tr.Interrupted {
						ended++
					}
				}
				return fmt.Sprintf("%d slow rows interrupted, %d of %d trees ended interrupted", slow, ended, len(trees)), slow == 2 && ended == 2 && len(trees) == 2
			}
			if words, ok := cut(); !ok {
				// The machine missed the reserve: the recovery at the next
				// start concludes what the cut left open.
				if _, err := ld.Recover(bg); err != nil {
					t.Fatal(err)
				}
				if again, ok := cut(); !ok {
					t.Fatalf("after the shutdown: %s; after the recovery: %s", words, again)
				}
			}
		})
	}
}
