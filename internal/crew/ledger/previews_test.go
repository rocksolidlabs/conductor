package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The previews (PV1, docs/plans/browser-evidence.md, PR-7): the table and
// its migration, the writers' order, the recovery.

// putPreview runs one writer of a preview's row in a transaction of run.
func putPreview(l *Ledger, run string, write func(*Tx) error) error {
	_, err := l.Transition(context.Background(), run, write)
	return err
}

// previewOf is the candidate build/1's preview seq, as the ledger reads.
func previewOf(t *testing.T, l *Ledger, run string, seq int) (PreviewRow, bool) {
	t.Helper()
	ps, err := l.Previews(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.TaskID == "build" && p.N == 1 && p.Seq == seq {
			return p, true
		}
	}
	return PreviewRow{}, false
}

func openRow(seq int) PreviewRow {
	return PreviewRow{TaskID: "build", N: 1, Seq: seq, Dir: "/w/build.1.preview", Commit: "def", Manifest: "crew:1", Ports: map[string]int{"web": 41000}, OpenedBy: "admin", Where: WhereNative}
}

// A version 10 database is brought to version 11 as it opens: the previews
// table is added with a new database's definition and is empty, and every
// table it had keeps its rows. A migration that fails (a table of that
// name there already) leaves it at version 10, read-only, and the next
// open, the cause gone, migrates.
func TestPreviewsTableMigration(t *testing.T) {
	fresh := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	var freshSQL string
	if err := fresh.db.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'previews'").Scan(&freshSQL); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	attemptOf(t, l, run, "build", 1, AttemptSucceeded)
	finish(t, l, run)
	tables := slices.DeleteFunc(schemaNames(t, l, "table"), func(s string) bool { return s == "previews" || s == "schema_version" })
	before := map[string][]string{}
	for _, table := range tables {
		before[table] = tableRows(t, l, table)
	}
	l.Close()
	rawExec(t, dir, append(slices.Clone(version11Added), "UPDATE schema_version SET version = 10", "CREATE TABLE previews (x)")...)

	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "previews") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 10 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if ps, err := failed.Previews(context.Background(), run); err != nil || len(ps) != 0 {
		t.Fatalf("previews of a version 10 ledger: %v %v", ps, err)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE previews")

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version || Version != 11 {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	var sqlText string
	if err := again.db.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'previews'").Scan(&sqlText); err != nil || sqlText != freshSQL {
		t.Fatalf("the previews table after the migration: %q %v, a new database's %q", sqlText, err, freshSQL)
	}
	if n := countRows(t, again, "SELECT COUNT(*) FROM previews"); n != 0 {
		t.Fatalf("%d previews after the migration", n)
	}
	for _, table := range tables {
		if got := tableRows(t, again, table); !slices.Equal(got, before[table]) {
			t.Fatalf("%s after the migration:\n%v\nbefore:\n%v", table, got, before[table])
		}
	}
	// The migrated ledger takes a preview.
	if err := putPreview(again, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err != nil {
		t.Fatal(err)
	}
}

// A preview's row comes first and moves only forward: no writer moves a
// row that is not there (ErrNoPreview); OpenPreview puts it resolved,
// refuses another of the candidate while it is open (ErrPreviewOpen), a
// seq that is not after the last, a row with no where, and a candidate
// opened MaxPreviews times (ErrTooManyPreviews); PreviewReady moves only a
// resolved row; PreviewEnding clears its URL and says why; EndPreview ends
// it, and nothing moves it after.
func TestAPreviewRowIsWrittenBeforeItsSteps(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	attemptOf(t, l, run, "build", 1, AttemptSucceeded)
	ready := openRow(1)
	ready.Preview = "http://127.0.0.1:41000/"
	if err := putPreview(l, run, func(tx *Tx) error { return tx.PreviewReady(ready) }); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("ready before the open: %v", err)
	}
	noWhere := openRow(1)
	noWhere.Where = ""
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(noWhere) }); !errors.Is(err, ErrNoWhere) {
		t.Fatalf("an open with no where: %v", err)
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err != nil {
		t.Fatal(err)
	}
	if p, ok := previewOf(t, l, run, 1); !ok || p.State != PreviewResolved || p.Preview != "" || p.OpenedBy != "admin" || p.Ports["web"] != 41000 || p.OpenedAt.IsZero() || p.Where != WhereNative {
		t.Fatalf("the opened row: %+v %v", p, ok)
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(2)) }); !errors.Is(err, ErrPreviewOpen) {
		t.Fatalf("a second open while one is open: %v", err)
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.PreviewReady(ready) }); err != nil {
		t.Fatal(err)
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.PreviewReady(ready) }); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("ready twice: %v", err)
	}
	if p, _ := previewOf(t, l, run, 1); p.State != PreviewReady || p.Preview != ready.Preview {
		t.Fatalf("the ready row: %+v", p)
	}
	ending := openRow(1)
	ending.Why = "closed"
	if err := putPreview(l, run, func(tx *Tx) error { return tx.PreviewEnding(ending) }); err != nil {
		t.Fatal(err)
	}
	if p, _ := previewOf(t, l, run, 1); p.State != PreviewReady || p.Preview != "" || p.Why != "closed" {
		t.Fatalf("the row as its end began: %+v", p)
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.EndPreview(ending) }); err != nil {
		t.Fatal(err)
	}
	if p, _ := previewOf(t, l, run, 1); p.State != PreviewEnded || p.EndedAt.IsZero() || p.Interrupted {
		t.Fatalf("the ended row: %+v", p)
	}
	for name, w := range map[string]func(PreviewRow) func(*Tx) error{
		"ready":  func(r PreviewRow) func(*Tx) error { return func(tx *Tx) error { return tx.PreviewReady(r) } },
		"ending": func(r PreviewRow) func(*Tx) error { return func(tx *Tx) error { return tx.PreviewEnding(r) } },
		"end":    func(r PreviewRow) func(*Tx) error { return func(tx *Tx) error { return tx.EndPreview(r) } },
	} {
		if err := putPreview(l, run, w(ready)); !errors.Is(err, ErrNoPreview) {
			t.Fatalf("%s after the end: %v", name, err)
		}
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(1)) }); err == nil {
		t.Fatal("an open of a seq the candidate had")
	}
	// The bound: MaxPreviews opens of a candidate, each ended.
	for seq := 2; seq <= MaxPreviews; seq++ {
		if err := putPreview(l, run, func(tx *Tx) error {
			if err := tx.OpenPreview(openRow(seq)); err != nil {
				return err
			}
			return tx.EndPreview(openRow(seq))
		}); err != nil {
			t.Fatalf("open %d: %v", seq, err)
		}
	}
	if err := putPreview(l, run, func(tx *Tx) error { return tx.OpenPreview(openRow(MaxPreviews + 1)) }); !errors.Is(err, ErrTooManyPreviews) {
		t.Fatalf("an open past the bound: %v", err)
	}
}

// The recovery ends a preview the owner left resolved or ready: ended and
// interrupted, its URL cleared, with a preview.ended event of its own,
// counted in Recovered.Previews; its service is reported with its tree,
// preview, and never signalled; the run keeps its state; a prune is
// refused while a preview is open; a second recovery writes nothing.
func TestRecoverMarksAnOpenPreviewInterrupted(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const run = "team-00000001"
	newRun(t, l, run)
	attemptOf(t, l, run, "build", 1, AttemptSucceeded)
	attemptOf(t, l, run, "docs", 1, AttemptSucceeded)
	finish(t, l, run)
	if err := putPreview(l, run, func(tx *Tx) error {
		if err := tx.OpenPreview(openRow(1)); err != nil {
			return err
		}
		ready := openRow(1)
		ready.Preview = "http://127.0.0.1:41000/"
		if err := tx.PreviewReady(ready); err != nil {
			return err
		}
		docs := openRow(1)
		docs.TaskID, docs.Dir = "docs", "/w/docs.1.preview"
		if err := tx.OpenPreview(docs); err != nil {
			return err
		}
		return tx.PutCheck(CheckRow{Where: WhereNative, RunID: run, TaskID: "build", N: 1, ID: "web", K: 3, Phase: PhaseService, Manifest: "crew:1", Dir: "/w/build.1.preview/app",
			Argv: []string{"npm", "start"}, State: CheckRunning, PID: 4242, Group: 4242, StartedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, run); !errors.Is(err, ErrNotPrunable) || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("a prune with a preview open: %v", err)
	}
	rec, err := l.Recover(ctx)
	if err != nil || len(rec) != 1 || rec[0].Previews != 2 || rec[0].Interrupted || len(rec[0].Services) != 1 {
		t.Fatalf("the recovery: %+v %v", rec, err)
	}
	if sv := rec[0].Services[0]; sv.Tree != TreePreview || sv.ID != "web" || sv.Pid != 4242 || !sv.Confirmed {
		t.Fatalf("the preview's service: %+v", sv)
	}
	ps, err := l.Previews(ctx, run)
	if err != nil || len(ps) != 2 {
		t.Fatalf("the previews after the recovery: %+v %v", ps, err)
	}
	for _, p := range ps {
		if p.State != PreviewEnded || !p.Interrupted || p.Preview != "" || p.Why != previewLeftOpen || p.EndedAt.IsZero() {
			t.Fatalf("a preview after the recovery: %+v", p)
		}
	}
	evs, err := l.Events(ctx, run, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	ended := 0
	for _, ev := range evs {
		if ev.Kind == "preview.ended" {
			ended++
			if !strings.Contains(ev.Payload, `"recovered":true`) || !strings.Contains(ev.Payload, `"interrupted":true`) {
				t.Fatalf("the recovery's preview.ended: %s", ev.Payload)
			}
		}
	}
	if ended != 2 {
		t.Fatalf("%d preview.ended events", ended)
	}
	if r, _, _ := l.Run(ctx, run); r.State != RunFinished {
		t.Fatalf("the run's state after the recovery: %s", r.State)
	}
	if again, err := l.Recover(ctx); err != nil || len(again) != 0 {
		t.Fatalf("a second recovery: %+v %v", again, err)
	}
	if err := l.Prune(ctx, run); err != nil {
		t.Fatalf("a prune once the previews ended: %v", err)
	}
}
