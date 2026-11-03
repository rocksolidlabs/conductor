package crew

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Where an attempt and its evidence ran (docs/plans/runtime.md, PR-4): an
// attempt's where is its run's Runtime's, fixed as it is made, and its
// row, every receipt of it, its state and its receipts' states say it; a
// prerequisite's row says its run's.

// runWheres is every Where the ledger and the reports give the run's
// attempts and receipts, by what said it.
func runWheres(t *testing.T, e *Engine, l *fakeLedger, runID, taskID string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	l.mu.Lock()
	for _, a := range l.attempts {
		if a.RunID == runID && a.TaskID == taskID {
			out["attempts"] = append(out["attempts"], a.Where)
		}
	}
	l.mu.Unlock()
	_, attempts, err := e.TaskOf(runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, as := range attempts {
		out["AttemptState"] = append(out["AttemptState"], as.Where)
		rows := l.stepRows(runID, taskID, as.N)
		for _, c := range rows {
			out["checks"] = append(out["checks"], c.Where)
		}
		for _, st := range StepStates(rows) {
			out["StepState"] = append(out["StepState"], st.Where)
		}
	}
	prereqs := l.prereqRows(runID)
	for _, p := range prereqs {
		out["prerequisites"] = append(out["prerequisites"], p.Where)
	}
	for _, st := range PrerequisiteStates(prereqs) {
		out["prerequisite StepState"] = append(out["prerequisite StepState"], st.Where)
	}
	return out
}

// sameWhere fails unless every report in got said want, and each kind of
// report said something.
func sameWhere(t *testing.T, got map[string][]string, want, when string) {
	t.Helper()
	for _, kind := range []string{"attempts", "AttemptState", "checks", "StepState", "prerequisites", "prerequisite StepState"} {
		if len(got[kind]) == 0 {
			t.Fatalf("%s: no %s (all: %v)", when, kind, got)
		}
		for _, w := range got[kind] {
			if w != want {
				t.Fatalf("%s: %s says %q, want %q (all: %v)", when, kind, w, want, got)
			}
		}
	}
}

// An attempt and its checks report the same where: with a fake Runtime
// that takes the run (doing the work natively under its own Where), every
// attempts, checks and prerequisites row and every AttemptState and
// StepState say fake; with the fake declining, or with nothing offered,
// native.
func TestAnAttemptAndItsChecksReportTheSameWhere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fake  *sessiontest.FakeRuntime
		where string
	}{
		{"taken", &sessiontest.FakeRuntime{Base: local.Native()}, "fake"},
		{"declined", &sessiontest.FakeRuntime{TakesRun: func(ext.ProcessRef) bool { return false }}, ledger.WhereNative},
		{"native", nil, ledger.WhereNative},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, l := managedEngine(t)
			if tc.fake != nil {
				offer(t, e, tc.fake)
			}
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", "test -f dep.txt")}
			m := Manifest{Prerequisites: []Check{shStep("tools", "echo tools")}, Setup: []Check{shStep("deps", "echo dep > dep.txt")}}
			run := launchManaged(t, e, repoManifestCrew(dir, m, build))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			if a.Attempt.Where != tc.where {
				t.Fatalf("the running attempt reports %q", a.Attempt.Where)
			}
			commitResult(t, e, a, "built.txt", "yes\n", "built")
			waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
			waitTreesEnded(t, e, run.ID)
			sameWhere(t, runWheres(t, e, l, run.ID, "build"), tc.where, tc.name)
			if w := fl.wheres(); len(w) != 1 || w[0] != tc.where {
				t.Fatalf("the attempt's session launched in %q", w)
			}
			if r, ok := e.Get(run.ID); !ok || r.Tasks[0].Attempt == nil || r.Tasks[0].Attempt.Where != tc.where {
				t.Fatalf("the run's read of the task: %+v", r.Tasks)
			}
		})
	}
}

// A run continued by an engine offered another Runtime: the rows of the
// run it continues keep where they ran, and the continuation's attempts,
// the candidate it verifies again among them, take the Runtime the
// continuation was given.
func TestAContinuationsAttemptsTakeTheirRunsNewWhere(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	first := &sessiontest.FakeRuntime{Base: local.Native(), Place: "first"}
	offer(t, e, first)
	mark := filepath.Join(t.TempDir(), "pass")
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	c := repoCrew(dir, build)
	// At the base the setup passes; at the result it waits until the mark
	// exists (the first server ends meanwhile), then passes.
	c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "test -e built.txt || exit 0; test -e " + mark + " && exit 0; sleep 30"}}}
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitFake(t, "the result verifying in the ledger", func() bool {
		row := l.attemptRow(t, run.ID, "build", 1)
		return row.State == ledger.AttemptVerifying && row.Result != ""
	})
	stopRun(t, e, run.ID)
	interrupt(l, run.ID, map[string][2]string{
		"build/1": {ledger.AttemptInterrupted, "verifying: the owner stopped; the candidate is verified again"},
	})
	if err := os.WriteFile(mark, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := l.stepRows(run.ID, "build", 1)
	if len(before) == 0 {
		t.Fatal("attempt 1 has no receipts")
	}

	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	second := &sessiontest.FakeRuntime{Base: local.Native(), Place: "second"}
	offer(t, e2, second)
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, e2, next.ID, "build", ledger.TaskSucceeded, 2)
	waitTreesEnded(t, e2, next.ID)
	if c := second.Asked("Takes"); len(c) != 1 || c[0].Ref != (ext.ProcessRef{Run: next.ID}) {
		t.Fatalf("the continuation's Takes: %+v", c)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.Where != "first" {
		t.Fatalf("the continued run's attempt now says %q", row.Where)
	}
	for _, c := range l.stepRows(run.ID, "build", 1) {
		if c.Where != "first" {
			t.Fatalf("the continued run's receipt %s/%d now says %q", c.ID, c.K, c.Where)
		}
	}
	row := l.attemptRow(t, next.ID, "build", 2)
	if row.Where != "second" || row.State != ledger.AttemptSucceeded {
		t.Fatalf("the candidate verified again: %+v", row)
	}
	steps := l.stepRows(next.ID, "build", 2)
	if len(steps) == 0 {
		t.Fatal("the candidate verified again has no receipts")
	}
	for _, c := range steps {
		if c.Where != "second" {
			t.Fatalf("the continuation's receipt %s/%d says %q", c.ID, c.K, c.Where)
		}
	}
	_, attempts, err := e2.TaskOf(next.ID, "build")
	if err != nil || len(attempts) != 1 || attempts[0].N != 2 || attempts[0].Where != "second" {
		t.Fatalf("the continuation's attempts: %+v %v", attempts, err)
	}
	if len(first.Asked("Run")) == 0 || len(second.Asked("Run")) == 0 {
		t.Fatalf("the runs: first ran %d commands, second %d", len(first.Asked("Run")), len(second.Asked("Run")))
	}
}

// A run record saved before the ledger kept where an attempt ran has no
// where on its attempts, and reads and writes back without one; the task
// read from a ledger of that time, brought to the version that keeps it,
// says native.
func TestARecordWithoutWhereReadsWithout(t *testing.T) {
	const old = `{"id":"team-0000abcd","crewId":"team","name":"Team","goal":"","cwd":"/r","isolation":"worktree","startedAt":"2026-10-30T12:00:00Z","members":[],
		"tasks":[{"id":"build","member":"lead","kind":"work","state":"succeeded","attempts":1,"attempt":{"n":1,"state":"succeeded"}}]}`
	var r Run
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 1 || r.Tasks[0].Attempt == nil || r.Tasks[0].Attempt.Where != "" {
		t.Fatalf("the old record's task: %+v", r.Tasks)
	}
	b, err := json.Marshal(r.Tasks[0].Attempt)
	if err != nil || strings.Contains(string(b), `"where"`) {
		t.Fatalf("the old record's attempt written back: %s %v", b, err)
	}

	dir := newRepo(t)
	e, _ := readyEngine(t)
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	e.UseLedger(SQLLedger(ld))
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
	ld.Close()
	// As a version 6 Conductor left it: no where_ran.
	db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{"ALTER TABLE attempts DROP COLUMN where_ran", "ALTER TABLE checks DROP COLUMN where_ran", "ALTER TABLE prerequisites DROP COLUMN where_ran", "UPDATE schema_version SET version = 6"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db.Close()
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	from, ok, err := SQLLedger(again).Load(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	_, attempts, ok := LoadedTask(from, "build")
	if !ok || len(attempts) != 1 || attempts[0].Where != ledger.WhereNative {
		t.Fatalf("the task read from the ledger: %+v %v", attempts, ok)
	}
	checks, err := again.Checks(ctx, run.ID, "build", 1, false)
	if err != nil || len(checks) == 0 {
		t.Fatalf("checks: %+v %v", checks, err)
	}
	for _, st := range StepStates(checks) {
		if st.Where != ledger.WhereNative {
			t.Fatalf("a receipt read from the ledger: %+v", st)
		}
	}
}
