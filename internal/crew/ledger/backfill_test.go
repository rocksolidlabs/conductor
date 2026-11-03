package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a run as a version 3 Conductor left it.
type fixture struct {
	id, from string
	crew     string
	tasks    []TaskRow
	attempts []AttemptRow
	events   []string // kinds; "task.reopened:<task>" carries the task
}

// put writes the fixture's rows in one transition.
func (f fixture) put(t *testing.T, l *Ledger) {
	t.Helper()
	if _, err := l.Transition(context.Background(), f.id, func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: f.id, CrewID: "team", Crew: f.crew, Base: "base0", State: RunStopped, ContinuedFrom: f.from, CreatedAt: tx.Now(), ClosedAt: tx.Now()}); err != nil {
			return err
		}
		for _, r := range f.tasks {
			r.RunID = f.id
			if err := tx.PutTask(r); err != nil {
				return err
			}
		}
		for _, a := range f.attempts {
			a.RunID, a.ReservedAt, a.Where = f.id, tx.Now(), WhereNative
			if a.Member == "" {
				a.Member = "m"
			}
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		for _, ev := range f.events {
			kind, task, _ := strings.Cut(ev, ":")
			if err := tx.Event(kind, map[string]any{"task": task}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// asVersion3 makes the database one a version 3 Conductor wrote, what
// versions 4 and 5 added dropped, and opens it again: the migration runs.
func asVersion3(t *testing.T, l *Ledger, dir string) *Ledger {
	t.Helper()
	for _, stmt := range append(slices.Clone(version5Added), "ALTER TABLE tasks DROP COLUMN withdrawn", "UPDATE schema_version SET version = 3") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	return again
}

// withdrawnOf is each task's withdrawn and repair in the run.
func withdrawnOf(t *testing.T, l *Ledger, runID string) (map[string]int, map[string]string) {
	t.Helper()
	rows, err := l.Tasks(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	w, rp := map[string]int{}, map[string]string{}
	for _, r := range rows {
		w[r.ID], rp[r.ID] = r.Withdrawn, r.Repair
	}
	return w, rp
}

// migratedEvents is the run's "migrated" events' payloads.
func migratedEvents(t *testing.T, l *Ledger, runID string) []string {
	t.Helper()
	evs, err := l.Events(context.Background(), runID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind == "migrated" {
			out = append(out, ev.Payload)
		}
	}
	return out
}

const reviewCrew = `{"id":"team","tasks":[{"id":"build","kind":"work"},{"id":"check","kind":"review","needs":["build"]},{"id":"docs","kind":"work","needs":["build"]},{"id":"site","kind":"work","needs":["docs"]},{"id":"lone","kind":"work"}]}`

// A version 3 database is brought to 4 with the conservative backfill:
// the reopened subject is found from whichever of the three marks version
// 3 kept (a task.reopened event; a needs_changes review attempt, when a
// failed commit saved the rows without the event; an attempt withdrawn),
// and every task resting on it, whatever its state (ready, failed,
// cancelled), takes its highest attempt as withdrawn and loses its
// repair; the subject keeps its own, and what does not rest on it is
// untouched. One migrated event says so.
func TestAVersion3DatabaseWithdrawsWhatRestedOnAReopenedTask(t *testing.T) {
	marks := map[string]fixture{
		"event": {events: []string{"task.reopened:build"}},
		"verdict": {attempts: []AttemptRow{
			{TaskID: "check", N: 1, State: AttemptSucceeded, Result: `{"verdict":"needs_changes","findings":[{"severity":"major","message":"no"}]}`},
		}},
		"withdrawn stage": {attempts: []AttemptRow{{TaskID: "site", N: 2, State: AttemptCancelled, Stage: "withdrawn"}}, events: nil},
	}
	for name, mark := range marks {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "runs")
			l := openLedger(t, dir)
			f := fixture{id: "team-00000001", crew: reviewCrew, events: mark.events,
				tasks: []TaskRow{
					{ID: "build", Member: "m", Kind: "work", State: TaskReady, Attempts: 2, Repair: `{"review":"check"}`},
					{ID: "check", Member: "m", Kind: "review", State: TaskBlocked, Attempts: 1},
					{ID: "docs", Member: "m", Kind: "work", State: TaskFailed, Attempts: 3, Repair: `{"review":"other"}`},
					{ID: "site", Member: "m", Kind: "work", State: TaskCancelled, Attempts: 2},
					{ID: "lone", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 1, ResultAttempt: 1, ResultCommit: "c1"},
				},
				attempts: append([]AttemptRow{
					{TaskID: "build", N: 1, State: AttemptSucceeded, ResultCommit: "b1"},
					{TaskID: "build", N: 2, State: AttemptRunning},
					{TaskID: "docs", N: 3, State: AttemptFailed, ResultCommit: "d3"},
					{TaskID: "lone", N: 1, State: AttemptSucceeded, ResultCommit: "c1"},
				}, mark.attempts...),
			}
			f.put(t, l)
			l = asVersion3(t, l, dir)
			w, rp := withdrawnOf(t, l, f.id)
			want := map[string]int{"build": 0, "check": 1, "docs": 3, "site": 2, "lone": 0}
			if name == "withdrawn stage" {
				// The stage names site's need, docs: build is not seen as
				// reopened, and docs, not resting on itself, is the subject.
				want = map[string]int{"build": 0, "check": 0, "docs": 0, "site": 2, "lone": 0}
			}
			for id, n := range want {
				if w[id] != n {
					t.Fatalf("withdrawn %v, want %v", w, want)
				}
			}
			if name != "withdrawn stage" && (rp["docs"] != "" || rp["build"] == "") {
				t.Fatalf("repairs after the migration: %v", rp)
			}
			evs := migratedEvents(t, l, f.id)
			if len(evs) != 1 || !strings.Contains(evs[0], `"from":3`) {
				t.Fatalf("migrated events: %v", evs)
			}
		})
	}
}

// Down a chain of continuations, a task whose ancestor's has attempts
// withdrawn has every attempt of its own withdrawn too: version 3 started
// a continuation's first attempt at the ancestor's last checkpoint, and
// its attempt 4 may rest on withdrawn work (Codex's review), through a
// continuation that made no attempt too; an ancestor retried after it was
// continued (attempts 4 and 5 there) does not reach past the child's
// count. A run with nothing to change gets no event.
func TestAVersion3ChainInheritsWithdrawnWithinTheAttemptsItTook(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	crew := `{"id":"team","tasks":[{"id":"build","kind":"work"},{"id":"docs","kind":"work","needs":["build"]}]}`
	root := fixture{id: "team-00000001", crew: crew, events: []string{"task.reopened:build"},
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "b2"},
			{ID: "docs", Member: "m", Kind: "work", State: TaskFailed, Attempts: 5},
		},
		attempts: []AttemptRow{{TaskID: "build", N: 2, State: AttemptSucceeded, ResultCommit: "b2"}, {TaskID: "docs", N: 5, State: AttemptFailed}},
	}
	// The continuation, which took 3 of docs' attempts and made attempt 4,
	// then its continuation, which made none.
	mid := fixture{id: "team-00000002", from: root.id, crew: crew,
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "b2", ImportedFrom: root.id + "/build/2"},
			{ID: "docs", Member: "m", Kind: "work", State: TaskFailed, Attempts: 4, Repair: `{"review":"x"}`},
		},
		attempts: []AttemptRow{{TaskID: "docs", N: 4, State: AttemptFailed, ResultCommit: "d4"}},
	}
	leaf := fixture{id: "team-00000003", from: mid.id, crew: crew,
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "b2", ImportedFrom: root.id + "/build/2"},
			{ID: "docs", Member: "m", Kind: "work", State: TaskFailed, Attempts: 4, Repair: `{"review":"x"}`},
		},
	}
	unrelated := fixture{id: "team-00000009", crew: crew,
		tasks:    []TaskRow{{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 1, ResultAttempt: 1}},
		attempts: []AttemptRow{{TaskID: "build", N: 1, State: AttemptSucceeded}},
	}
	for _, f := range []fixture{root, mid, leaf, unrelated} {
		f.put(t, l)
	}
	l = asVersion3(t, l, dir)
	for _, c := range []struct {
		run  string
		want int
	}{{root.id, 5}, {mid.id, 4}, {leaf.id, 4}} {
		if w, _ := withdrawnOf(t, l, c.run); w["docs"] != c.want || w["build"] != 0 {
			t.Fatalf("run %s: withdrawn %v, docs should be %d", c.run, w, c.want)
		}
	}
	for _, run := range []string{mid.id, leaf.id} {
		if _, rp := withdrawnOf(t, l, run); rp["docs"] != "" {
			t.Fatalf("run %s's docs kept its repair: %q", run, rp["docs"])
		}
	}
	if evs := migratedEvents(t, l, unrelated.id); len(evs) != 0 {
		t.Fatalf("a run with nothing withdrawn got %v", evs)
	}
	var p struct {
		Withdrawn map[string]int `json:"withdrawn"`
	}
	if evs := migratedEvents(t, l, leaf.id); len(evs) != 1 || json.Unmarshal([]byte(evs[0]), &p) != nil || p.Withdrawn["docs"] != 4 {
		t.Fatalf("the leaf's migrated event: %v", evs)
	}
}

// A run's row written after it was continued, from a copy made before (a
// stop's), keeps the link: the run is never continued twice.
func TestARunRowWrittenAfterItsContinuationKeepsTheLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	before, _, err := l.Run(ctx, "team-00000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: "team-00000002", CrewID: "team", Crew: `{"id":"team"}`, Base: "abc123", State: RunRunning, ContinuedFrom: "team-00000001", CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Continued("team-00000001")
	}); err != nil {
		t.Fatal(err)
	}
	before.State, before.ClosedAt = RunStopped, before.CreatedAt
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.PutRun(before) }); err != nil {
		t.Fatal(err)
	}
	after, _, err := l.Run(ctx, "team-00000001")
	if err != nil || after.ContinuedBy != "team-00000002" || after.State != RunStopped {
		t.Fatalf("the row after the stale write: %+v %v", after, err)
	}
	if _, err := l.Transition(ctx, "team-00000003", func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: "team-00000003", CrewID: "team", Crew: `{"id":"team"}`, Base: "abc123", State: RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Continued("team-00000001")
	}); !errors.Is(err, ErrContinued) {
		t.Fatalf("a second continuation: %v", err)
	}
}

// An ancestor's withdrawal reaches what needs the task in the
// continuation too: version 3's continuation finished docs on the
// withdrawn checkpoint and then site, which first ran there, on docs'
// result; both are withdrawn (Codex's review), build is not.
func TestAVersion3InheritedWithdrawalReachesTheChildsDependents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	crew := `{"id":"team","tasks":[{"id":"build","kind":"work"},{"id":"docs","kind":"work","needs":["build"]},{"id":"site","kind":"work","needs":["docs"]}]}`
	root := fixture{id: "team-00000001", crew: crew, events: []string{"task.reopened:build"},
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "b2"},
			{ID: "docs", Member: "m", Kind: "work", State: TaskReady, Attempts: 1},
			{ID: "site", Member: "m", Kind: "work", State: TaskBlocked},
		},
		attempts: []AttemptRow{{TaskID: "build", N: 2, State: AttemptSucceeded, ResultCommit: "b2"}, {TaskID: "docs", N: 1, State: AttemptFailed, ResultCommit: "d1", Checkpoint: `{"commit":"d1"}`}},
	}
	child := fixture{id: "team-00000002", from: root.id, crew: crew,
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "b2", ImportedFrom: root.id + "/build/2"},
			{ID: "docs", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 2, ResultAttempt: 2, ResultCommit: "d2"},
			{ID: "site", Member: "m", Kind: "work", State: TaskSucceeded, Attempts: 1, ResultAttempt: 1, ResultCommit: "s1", Repair: `{"review":"x"}`},
		},
		attempts: []AttemptRow{{TaskID: "docs", N: 2, State: AttemptSucceeded, StartCommit: "d1", ResultCommit: "d2"}, {TaskID: "site", N: 1, State: AttemptSucceeded, ResultCommit: "s1"}},
	}
	root.put(t, l)
	child.put(t, l)
	l = asVersion3(t, l, dir)
	w, rp := withdrawnOf(t, l, child.id)
	if w["build"] != 0 || w["docs"] != 2 || w["site"] != 1 || rp["site"] != "" {
		t.Fatalf("the child after the migration: withdrawn %v, repairs %v", w, rp)
	}
}

// A backfill that fails (a scan cut short, its rows closed with no error
// from Close, now read from Err) rolls the migration back whole: the
// database opens read-only at version 3 with no withdrawn column, and the
// next open migrates it, backfill and all.
func TestAFailedBackfillRollsTheMigrationBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	f := fixture{id: "team-00000001", crew: reviewCrew, events: []string{"task.reopened:build"},
		tasks: []TaskRow{
			{ID: "build", Member: "m", Kind: "work", State: TaskReady, Attempts: 1},
			{ID: "docs", Member: "m", Kind: "work", State: TaskFailed, Attempts: 2},
		},
		attempts: []AttemptRow{{TaskID: "docs", N: 2, State: AttemptFailed}},
	}
	f.put(t, l)
	for _, stmt := range append(slices.Clone(version5Added), "ALTER TABLE tasks DROP COLUMN withdrawn", "UPDATE schema_version SET version = 3") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	backfillFails = errors.New("a scan cut short")
	failed := openLedger(t, dir)
	backfillFails = nil
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "a scan cut short") {
		t.Fatalf("a failed backfill opened %v: %s", ro, why)
	}
	var version, cols int
	if err := failed.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("version after the failed migration: %d %v", version, err)
	}
	if err := failed.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'withdrawn'").Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("the withdrawn column after the failed migration: %d %v", cols, err)
	}
	failed.Close()
	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if w, _ := withdrawnOf(t, again, f.id); w["docs"] != 2 {
		t.Fatalf("withdrawn after the migration: %v", w)
	}
}
