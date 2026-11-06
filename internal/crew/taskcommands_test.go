package crew

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// taskCommandRow is a row of testdata/task-commands.json.
type taskCommandRow struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Kind     string `json:"kind"`
	Attempts []struct {
		Commit string `json:"commit"`
	} `json:"attempts"`
	Withdrawn int      `json:"withdrawn"`
	Run       string   `json:"run"`
	Commands  []string `json:"commands"`
	Accept    int      `json:"accept"`
}

// taskCommandRows reads the fixture the decision page's inbox shares
// with the run page's taskActions (PR-2's vitest case).
func taskCommandRows(t *testing.T) []taskCommandRow {
	t.Helper()
	b, err := os.ReadFile("testdata/task-commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		About string           `json:"about"`
		Rows  []taskCommandRow `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) == 0 {
		t.Fatal("no rows")
	}
	return f.Rows
}

// rowRun puts the run a fixture row describes into e, its one task in the
// row's state with the row's attempts, as the engine would hold it.
func rowRun(t *testing.T, e *Engine, i int, row taskCommandRow) *run {
	return rowRunAt(t, e, i, row, 7)
}

// rowRunAt is rowRun at the revision rev.
func rowRunAt(t *testing.T, e *Engine, i int, row taskCommandRow, rev int64) *run {
	t.Helper()
	tk := &task{def: Task{ID: "build", Member: "lead", Kind: row.Kind}, state: row.State, withdrawn: row.Withdrawn}
	lead := &member{def: Member{Name: "lead"}}
	for n, a := range row.Attempts {
		// Every attempt closed, failed, but a running or verifying task's
		// last, which is in flight.
		at := &attempt{t: tk, m: lead, n: n + 1, state: ledger.AttemptFailed, closedAt: time.Now().UTC(), where: ext.WhereNative}
		if inFlight := row.State == ledger.TaskRunning || row.State == ledger.TaskVerifying; inFlight && n == len(row.Attempts)-1 {
			at.state, at.closedAt = ledger.AttemptRunning, time.Time{}
			at.ctx, at.cancel = context.WithCancel(t.Context())
		}
		if a.Commit != "" {
			at.submitted, at.result = &Result{Summary: "tried", Commit: a.Commit}, a.Commit
		}
		tk.attempts = append(tk.attempts, at)
	}
	r := &run{id: fmt.Sprintf("fixture-%08x", i), managed: true, created: true, revision: rev, tasks: []*task{tk}, ctx: t.Context()}
	switch row.Run {
	case "live":
	case "stopped":
		at := time.Now().UTC()
		r.stoppedAt = &at
	case "continued":
		r.resumedBy = "fixture-ffffffff"
	case "launching":
		r.created = false
	case "legacy":
		r.managed = false
	default:
		t.Fatalf("%s: run %q", row.Name, row.Run)
	}
	e.mu.Lock()
	e.runs[r.id] = r
	e.mu.Unlock()
	return r
}

// Every row of the fixture against TaskCommands and the engine itself:
// the commands TaskCommands names are the row's, with the attempt an
// Accept anyway takes, and each of the three, sent to the engine (Retry,
// Accept, Cancel) at the revision the set was judged at on a run of its
// own in the row's state, is taken (its receipt done) exactly when the set
// names it and refused otherwise: a precondition a command gains that
// TaskCommands does not judge fails a row here.
func TestTaskCommandsFollowTheFixture(t *testing.T) {
	e, _, l := managedEngine(t)
	ctx := context.Background()
	send := map[string]func(runID string, cmd Command) (Receipt, error){
		TaskRetry:  func(runID string, cmd Command) (Receipt, error) { return e.Retry(ctx, runID, "admin", cmd, "build") },
		TaskAccept: func(runID string, cmd Command) (Receipt, error) { return e.Accept(ctx, runID, "admin", cmd, "build") },
		TaskCancel: func(runID string, cmd Command) (Receipt, error) { return e.Cancel(ctx, runID, "admin", cmd, "build") },
	}
	runs := 0
	for _, row := range taskCommandRows(t) {
		r := rowRun(t, e, runs, row)
		runs++
		got, err := e.TaskCommands(r.id, "build")
		if err != nil {
			t.Fatalf("%s: %v", row.Name, err)
		}
		if !slices.Equal(got.Commands, row.Commands) || got.Revision != 7 || got.AcceptAttempt != row.Accept || got.Task.ID != "build" || got.Task.State != row.State {
			t.Errorf("%s: TaskCommands %+v, the fixture %v (accept %d)", row.Name, got, row.Commands, row.Accept)
		}
		if row.Accept > 0 && got.AcceptCommit != row.Attempts[row.Accept-1].Commit {
			t.Errorf("%s: accept takes %q", row.Name, got.AcceptCommit)
		}
		for _, name := range []string{TaskRetry, TaskAccept, TaskCancel} {
			// A run of its own for each command (one taken changes the
			// task), at the fake ledger's revision, which its next commit
			// follows as a run's own ledger revision does.
			l.mu.Lock()
			rev := l.rev
			l.mu.Unlock()
			own := rowRunAt(t, e, runs, row, rev)
			runs++
			set, err := e.TaskCommands(own.id, "build")
			if err != nil || !slices.Equal(set.Commands, got.Commands) || set.Revision != rev {
				t.Fatalf("%s: TaskCommands on its own run %+v %v", row.Name, set, err)
			}
			rc, err := send[name](own.id, Command{ID: name + "-1", ExpectedRevision: set.Revision})
			named := slices.Contains(row.Commands, name)
			if taken := err == nil && rc.Status == ledger.CommandDone; taken != named {
				t.Errorf("%s: the engine on %s: %+v %v, named %v", row.Name, name, rc, err, named)
			}
		}
	}
	if _, err := e.TaskCommands("nope-00000000", "build"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("no run: %v", err)
	}
	if _, err := e.TaskCommands("fixture-00000000", "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("no task: %v", err)
	}
	// Without a ledger the engine takes no command, and none is named.
	e2, _ := newEngine(t)
	r := rowRun(t, e2, 0, taskCommandRows(t)[0])
	if got, err := e2.TaskCommands(r.id, "build"); err != nil || len(got.Commands) != 0 {
		t.Fatalf("no ledger: %+v %v", got, err)
	}
	if _, err := e2.Retry(ctx, r.id, "admin", Command{ID: "r-1"}, "build"); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("no ledger, a retry: %v", err)
	}
}

// The fixture's rows a live run passes through, against the engine
// itself: a task failing its check on every attempt is offered Retry and
// Accept anyway and refuses Cancel, its dependent offered Cancel alone;
// each command the set names is taken at the revision it was judged at,
// and one it leaves out is refused by the task's state.
func TestTaskCommandsFollowALiveRun(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "never", Argv: []string{"sh", "-c", "exit 1"}}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer", "build")))
	ctx := context.Background()
	for n := 1; n <= 3; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", "running", func(ts TaskState) bool {
			return ts.Attempts == n && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, e, a, "built.txt", fmt.Sprintf("try %d\n", n), "try")
	}
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	waitOutcome(t, e, run.ID, "failed")
	set, err := e.TaskCommands(run.ID, "build")
	if err != nil || !slices.Equal(set.Commands, []string{TaskRetry, TaskAccept}) || set.AcceptAttempt != 3 || set.AcceptCommit != failed.Attempt.Result {
		t.Fatalf("build: %+v %v", set, err)
	}
	// A Retry starts where the engine would start it: the failed attempt's
	// result, not where that attempt started.
	if set.RetryStart != failed.Attempt.Result || set.RetryStart == failed.Attempt.Start || set.Task.State != ledger.TaskFailed || set.Task.Attempts != 3 {
		t.Fatalf("build's retry starts at %q (attempt 3 started at %q, its result %q); the task %+v", set.RetryStart, failed.Attempt.Start, failed.Attempt.Result, set.Task)
	}
	if docs, _ := e.TaskCommands(run.ID, "docs"); !slices.Equal(docs.Commands, []string{TaskCancel}) {
		t.Fatalf("docs: %+v", docs)
	}
	if rc, err := e.Cancel(ctx, run.ID, "admin", Command{ID: "c1", ExpectedRevision: set.Revision}, "build"); !errors.Is(err, ErrTaskState) || rc.Status != ledger.CommandFailed {
		t.Fatalf("a cancel the set leaves out: %+v %v", rc, err)
	}
	set, _ = e.TaskCommands(run.ID, "build")
	if rc, err := e.Accept(ctx, run.ID, "admin", Command{ID: "a1", ExpectedRevision: set.Revision}, "build"); err != nil || rc.Task == nil || rc.Task.State != ledger.TaskSucceeded || rc.Task.Result.Attempt != set.AcceptAttempt {
		t.Fatalf("accept at its revision: %+v %v", rc, err)
	}
	if after, _ := e.TaskCommands(run.ID, "build"); len(after.Commands) != 0 {
		t.Fatalf("build after the accept: %+v", after)
	}
	stopRun(t, e, run.ID)
}
