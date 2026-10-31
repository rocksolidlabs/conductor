package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// rawRow is the row of table where key = id as the database holds it,
// every column's value quoted: a row compared byte for byte.
func rawRow(t *testing.T, l *Ledger, table, key, id string) string {
	t.Helper()
	db, err := l.conn()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", table, key), id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts = append(parts, fmt.Sprintf("%s=%q", cols[i], fmt.Sprint(v)))
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// plannedRow is a run launched from a ticket: its ticket's id and its
// package beside the crew.
func plannedRow(id, pkg string) RunRow {
	return RunRow{ID: id, CrewID: "team", Crew: `{"id":"team"}`, Base: strings.Repeat("a", 40), State: RunRunning, Ticket: "0123456789abcdef", Package: pkg}
}

// A version 5 database (no ticket, no package on its runs) is brought to
// version 6 as it opens: every run, attempt and check kept as it was, each
// run's ticket and package empty, which is what a run no ticket launched
// has; nothing is derived. A database of the version this Conductor
// writes opens read-only under one that writes an earlier version, as
// every newer database does: the rule prepare applies to any version
// above its own, shown here with this version's columns read back.
func TestMigrationAddsTheRunsPackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	newRun(t, l, "team-00000002")
	attemptOf(t, l, "team-00000001", "build", 1, AttemptRunning)
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"go", "test"}, State: CheckFinished, Exit: 0, StartedAt: tx.Now(), FinishedAt: tx.Now(), Log: []byte("ok"), LogSHA256: "h"})
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range append(version6Added, "UPDATE schema_version SET version = 5") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	before := map[string]string{}
	for _, q := range [][3]string{{"runs", "id", "team-00000001"}, {"runs", "id", "team-00000002"}, {"attempts", "run_id", "team-00000001"}, {"checks", "run_id", "team-00000001"}, {"events", "run_id", "team-00000001"}} {
		before[strings.Join(q[:], "/")] = rawRow(t, l, q[0], q[1], q[2])
	}
	l.Close()

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	var version int
	if err := again.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 6 || Version != 6 {
		t.Fatalf("version %d (%v), Version %d", version, err, Version)
	}
	for k, was := range before {
		q := strings.SplitN(k, "/", 3)
		now := rawRow(t, again, q[0], q[1], q[2])
		if q[0] == "runs" {
			// The two columns added, at the end of the row, empty.
			was += ` ticket="" package=""`
		}
		if now != was {
			t.Fatalf("%s after the migration:\n%s\nwas\n%s", k, now, was)
		}
	}
	runs, err := again.Runs(ctx)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after the migration: %v %v", runs, err)
	}
	for _, r := range runs {
		if r.Ticket != "" || r.Package != "" || r.Crew != `{"id":"team"}` || r.Base != "abc123" {
			t.Fatalf("a run after the migration: %+v", r)
		}
	}
	// A run launched from a ticket, then the database as a later
	// Conductor's: read-only, its rows read with this version's columns.
	if _, err := again.Transition(ctx, "team-00000003", func(tx *Tx) error {
		return tx.CreateRun(plannedRow("team-00000003", `{"version":1}`))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := again.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	again.Close()
	newer := openLedger(t, dir)
	if ro, why := newer.ReadOnly(); !ro || !strings.Contains(why, "newer") {
		t.Fatalf("a later version's database: read-only %v: %s", ro, why)
	}
	if r, ok, err := newer.Run(ctx, "team-00000003"); err != nil || !ok || r.Ticket != "0123456789abcdef" || r.Package != `{"version":1}` {
		t.Fatalf("the planned run read from a later version's database: %+v %v %v", r, ok, err)
	}
	if _, err := newer.Transition(ctx, "team-00000003", func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write to a later version's database: %v", err)
	}
}

// PutRun and CreateRun refuse a package past MaxPackage, as they refuse a
// crew past MaxCrew, and nothing of the transaction is kept; a package at
// the bound is kept byte for byte. A row that exists keeps its ticket and
// package whatever a later PutRun says: they are its input, written once.
func TestPutRunBoundsThePackage(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	at := `{"pad":"` + strings.Repeat("x", MaxPackage-len(`{"pad":""}`)) + `"}`
	if len(at) != MaxPackage {
		t.Fatalf("the fixture is %d bytes", len(at))
	}
	past := at + " "
	for _, put := range []struct {
		name string
		put  func(*Tx, RunRow) error
	}{{"PutRun", (*Tx).PutRun}, {"CreateRun", (*Tx).CreateRun}} {
		t.Run(put.name, func(t *testing.T) {
			id := "team-" + map[string]string{"PutRun": "0000000a", "CreateRun": "0000000b"}[put.name]
			_, err := l.Transition(ctx, id, func(tx *Tx) error {
				if err := put.put(tx, plannedRow(id, past)); err != nil {
					return err
				}
				return tx.Event("run.created", nil)
			})
			if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "package") {
				t.Fatalf("a package past the bound: %v", err)
			}
			if _, ok, err := l.Run(ctx, id); err != nil || ok {
				t.Fatalf("a refused row was kept: %v %v", ok, err)
			}
			if evs, _ := l.Events(ctx, id, 0, 10); len(evs) != 0 {
				t.Fatalf("a refused transaction's events were kept: %+v", evs)
			}
			if _, err := l.Transition(ctx, id, func(tx *Tx) error { return put.put(tx, plannedRow(id, at)) }); err != nil {
				t.Fatalf("a package at the bound: %v", err)
			}
			if r, ok, err := l.Run(ctx, id); err != nil || !ok || r.Package != at || r.Ticket != "0123456789abcdef" {
				t.Fatalf("the package at the bound read back: %v %v (%d bytes)", ok, err, len(r.Package))
			}
			// A later transition's row, saying another ticket and no
			// package: the run keeps its own.
			if _, err := l.Transition(ctx, id, func(tx *Tx) error {
				r, _, err := tx.Run()
				if err != nil {
					return err
				}
				r.State, r.Ticket, r.Package = RunStopped, "fedcba9876543210", ""
				return tx.PutRun(r)
			}); err != nil {
				t.Fatal(err)
			}
			if r, _, _ := l.Run(ctx, id); r.State != RunStopped || r.Package != at || r.Ticket != "0123456789abcdef" {
				t.Fatalf("a later PutRun changed the run's input: state %s ticket %s, %d bytes", r.State, r.Ticket, len(r.Package))
			}
		})
	}
	if _, err := l.Transition(ctx, "team-0000000c", func(tx *Tx) error {
		r := plannedRow("team-0000000c", "")
		r.Ticket = strings.Repeat("0", maxTicketID+1)
		return tx.CreateRun(r)
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a ticket id past its bound: %v", err)
	}
}

// CreateRun refuses an id the ledger has, with ErrRunExists, and the row
// there stays byte for byte as it was, its events too: the refused
// transaction is undone whole. An id it has not is created.
func TestCreateRunRefusesAnIDItHas(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		if err := tx.CreateRun(plannedRow("team-00000001", `{"launch":"first"}`)); err != nil {
			return err
		}
		return tx.Event("run.created", map[string]any{"ticket": "CND-1"})
	}); err != nil {
		t.Fatal(err)
	}
	row, events := rawRow(t, l, "runs", "id", "team-00000001"), rawRow(t, l, "events", "run_id", "team-00000001")
	for _, other := range []RunRow{
		plannedRow("team-00000001", `{"launch":"second"}`),
		{ID: "team-00000001", CrewID: "team", Crew: `{"id":"team","name":"other"}`, Base: strings.Repeat("b", 40), State: RunRunning},
	} {
		_, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
			if err := tx.Event("run.created", map[string]any{"ticket": "CND-2"}); err != nil {
				return err
			}
			return tx.CreateRun(other)
		})
		if !errors.Is(err, ErrRunExists) {
			t.Fatalf("an id the ledger has: %v", err)
		}
		if now := rawRow(t, l, "runs", "id", "team-00000001"); now != row {
			t.Fatalf("the row changed:\n%s\nwas\n%s", now, row)
		}
		if now := rawRow(t, l, "events", "run_id", "team-00000001"); now != events {
			t.Fatalf("the events changed:\n%s\nwere\n%s", now, events)
		}
	}
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error { return tx.CreateRun(plannedRow("team-00000002", "")) }); err != nil {
		t.Fatalf("a new id: %v", err)
	}
	if r, ok, _ := l.Run(ctx, "team-00000002"); !ok || r.Revision != 1 {
		t.Fatalf("the new run: %+v %v", r, ok)
	}
}
