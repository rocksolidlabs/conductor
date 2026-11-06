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
	t.Helper()
	tk := &task{def: Task{ID: "build", Member: "lead", Kind: row.Kind}, state: row.State, withdrawn: row.Withdrawn}
	for n, a := range row.Attempts {
		at := &attempt{t: tk, n: n + 1, state: ledger.AttemptFailed}
		if a.Commit != "" {
			at.submitted, at.result = &Result{Summary: "tried", Commit: a.Commit}, a.Commit
		}
		tk.attempts = append(tk.attempts, at)
	}
	r := &run{id: fmt.Sprintf("fixture-%08x", i), managed: true, created: true, revision: 7, tasks: []*task{tk}}
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

// Every row of the fixture against TaskCommands and the engine: the
// commands TaskCommands names are the row's, with the attempt an Accept
// anyway takes, and each of the three is taken by the engine's own
// precondition (the managed run, its launch recorded, not continued, then
// the task's state: what act and the command's change judge) exactly when
// it is named.
func TestTaskCommandsFollowTheFixture(t *testing.T) {
	e, _, _ := managedEngine(t)
	for i, row := range taskCommandRows(t) {
		r := rowRun(t, e, i, row)
		got, err := e.TaskCommands(r.id, "build")
		if err != nil {
			t.Fatalf("%s: %v", row.Name, err)
		}
		if !slices.Equal(got.Commands, row.Commands) || got.Revision != 7 || got.AcceptAttempt != row.Accept {
			t.Errorf("%s: TaskCommands %+v, the fixture %v (accept %d)", row.Name, got, row.Commands, row.Accept)
		}
		if row.Accept > 0 && got.AcceptCommit != row.Attempts[row.Accept-1].Commit {
			t.Errorf("%s: accept takes %q", row.Name, got.AcceptCommit)
		}
		e.mu.Lock()
		e.order = append(e.order, r)
		e.mu.Unlock()
		// The runs' list names the same commands on the task (the run
		// page's taskActions offers these; Get puts them in as List does),
		// and so does the task's own read. A legacy run's read has no tasks.
		if listed := e.List(); !slices.ContainsFunc(listed, func(l Run) bool {
			return l.ID == r.id && (len(l.Tasks) == 0 && len(row.Commands) == 0 || len(l.Tasks) == 1 && slices.Equal(append([]string{}, l.Tasks[0].Commands...), row.Commands))
		}) {
			t.Errorf("%s: the runs' list names other commands", row.Name)
		}
		if st, _, err := e.TaskOf(r.id, "build"); err != nil || !slices.Equal(append([]string{}, st.Commands...), row.Commands) {
			t.Errorf("%s: the task's read names %v (%v)", row.Name, st.Commands, err)
		}
		for _, name := range []string{TaskRetry, TaskAccept, TaskCancel} {
			err := engineTakes(e, r, name)
			if taken := err == nil; taken != slices.Contains(row.Commands, name) {
				t.Errorf("%s: the engine on %s: %v", row.Name, name, err)
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
}

// engineTakes is whether the engine would take the task command name on
// r's task build, judged in act's order: the run managed (managedRun), a
// ledger (act), the launch recorded and the run its own (commit), not
// continued (continued, in the change), then the command's own check.
func engineTakes(e *Engine, r *run, name string) error {
	if _, err := e.managedRun(r.id); err != nil {
		return err
	}
	if e.ledger == nil {
		return ErrNoLedger
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.unrecorded() {
		return errLaunching
	}
	if err := e.continued(r); err != nil {
		return err
	}
	_, err := taskCommandCheck(name, r.task("build"))
	return err
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
	if docs, _ := e.TaskCommands(run.ID, "docs"); !slices.Equal(docs.Commands, []string{TaskCancel}) {
		t.Fatalf("docs: %+v", docs)
	}
	if rc, err := e.Cancel(ctx, run.ID, "admin", Command{ID: "c1", ExpectedRevision: set.Revision}, "build"); !errors.Is(err, ErrTaskState) || rc.Status != ledger.CommandFailed {
		t.Fatalf("a cancel the set leaves out: %+v %v", rc, err)
	}
	// Every read of the live run names them on its tasks: the run page's
	// (GET /api/runs/{run} is GetWithDiffs), the list's and Get's.
	reads := map[string]Run{}
	reads["Get"], _ = e.Get(run.ID)
	reads["GetWithDiffs"], _ = e.GetWithDiffs(ctx, run.ID)
	for _, l := range e.List() {
		if l.ID == run.ID {
			reads["List"] = l
		}
	}
	for name, rd := range reads {
		if len(rd.Tasks) != 2 || !slices.Equal(rd.Tasks[0].Commands, []string{TaskRetry, TaskAccept}) || !slices.Equal(rd.Tasks[1].Commands, []string{TaskCancel}) {
			t.Fatalf("%s names %+v", name, rd.Tasks)
		}
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
