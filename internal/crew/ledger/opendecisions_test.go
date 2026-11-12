package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The decide list's one read (docs/plans/decide-grant.md, The decide
// list): OpenDecisionsNewest hands the open rows alone, newest first, each
// as stored, an undecodable request among them, within its two bounds.
func TestOpenDecisionsNewest(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	var ns []int64
	for _, g := range []string{"g1", "g2", "g3", "g4", "g5"} {
		ns = append(ns, open(t, l, gateDecision(run, g)))
	}
	setState(t, l, run, ns[1], DecisionCancelled, "")
	// A request no decoder takes, as only a write past checkRequest leaves
	// one: handed as it is kept.
	if _, err := l.db.Exec("UPDATE decisions SET request = '{' WHERE n = ?", ns[3]); err != nil {
		t.Fatal(err)
	}
	read := func(limit int, stop int64) ([]DecisionRow, bool) {
		t.Helper()
		var got []DecisionRow
		more, err := l.OpenDecisionsNewest(ctx, limit, func(d DecisionRow) bool {
			got = append(got, d)
			return d.N != stop
		})
		if err != nil {
			t.Fatal(err)
		}
		return got, more
	}
	open := []int64{ns[4], ns[3], ns[2], ns[0]}
	got, more := read(0, 0)
	if !slices.Equal(nOf(got), open) || more {
		t.Fatalf("the open rows %v more %v, want %v", nOf(got), more, open)
	}
	if got[1].Request != "{" || got[0] != record(t, l, ns[4]).DecisionRow {
		t.Fatalf("not handed as stored: %+v", got[:2])
	}
	if got, more := read(0, ns[3]); !slices.Equal(nOf(got), open[:2]) || more {
		t.Fatalf("stopped by visit: %v more %v", nOf(got), more)
	}
	if got, more := read(2, 0); !slices.Equal(nOf(got), open[:2]) || !more {
		t.Fatalf("at a limit of 2: %v more %v", nOf(got), more)
	}
	if got, more := read(4, 0); !slices.Equal(nOf(got), open) || more {
		t.Fatalf("at a limit of 4, every open row: %v more %v", nOf(got), more)
	}
	// The bytes' bound counts every text the row's scan reads, the three
	// times as stored among them: its size here is the stored texts' own,
	// summed by SQLite. At the first two rows' size both are handed; one
	// byte less, only the first.
	stored := func(n int64) int {
		t.Helper()
		var size int
		cols := []string{"run_id", "current_run", "origin", "type", "gate", "task_id", "session_id", "member", "ticket", "plan", "plan_digest",
			"request_id", "fingerprint", "request", "evidence", "policy", "state", "why", "superseded_by", "applied", "created_at", "answered_at", "closed_at"}
		for i, c := range cols {
			cols[i] = "length(CAST(" + c + " AS BLOB))"
		}
		if err := l.db.QueryRow("SELECT "+strings.Join(cols, " + ")+" FROM decisions WHERE n = ?", n).Scan(&size); err != nil {
			t.Fatal(err)
		}
		return size
	}
	old := openScanBytes
	t.Cleanup(func() { openScanBytes = old })
	openScanBytes = stored(open[0]) + stored(open[1])
	if got, more := read(0, 0); !slices.Equal(nOf(got), open[:2]) || !more {
		t.Fatalf("at the bytes' bound: %v more %v", nOf(got), more)
	}
	openScanBytes--
	if got, more := read(0, 0); !slices.Equal(nOf(got), open[:1]) || !more {
		t.Fatalf("one byte under the bytes' bound: %v more %v", nOf(got), more)
	}
	openScanBytes = old

	// A database older than the decision tables hands none.
	seeded := filepath.Join(t.TempDir(), "seed")
	s := openLedger(t, seeded)
	newRun(t, s, run)
	s.Close()
	dir := filepath.Join(t.TempDir(), "runs")
	version7Like(t, seeded, dir)
	reader := openReadOnly(t, dir)
	handed := 0
	if more, err := reader.OpenDecisionsNewest(ctx, 0, func(DecisionRow) bool { handed++; return true }); err != nil || more || handed != 0 {
		t.Fatalf("a version 7 ledger: %d handed, more %v, %v", handed, more, err)
	}
}

// nOf is the numbers of rows, in their order.
func nOf(rows []DecisionRow) []int64 {
	out := []int64{}
	for _, d := range rows {
		out = append(out, d.N)
	}
	return out
}

// openPlan is SQLite's plan of the read's own statement, one detail a line.
func openPlan(t *testing.T, l *Ledger) []string {
	t.Helper()
	rows, err := l.db.Query("EXPLAIN QUERY PLAN "+openDecisionsNewest, MaxOpenScan+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}

// closedCopies writes count copies of decision n, cancelled, newer than
// every row there is, by a statement that knows nothing of the index.
func closedCopies(t *testing.T, l *Ledger, n int64, count int) {
	t.Helper()
	const cols = `run_id, current_run, origin, type, gate, task_id, attempt, session_id, member, ticket, plan, plan_digest,
		request_id, fingerprint, request, evidence, policy, rev, why, superseded_by, applied, created_at, answered_at, closed_at`
	if _, err := l.db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < ?)
		INSERT INTO decisions (`+cols+`, state) SELECT `+cols+`, 'cancelled' FROM decisions, c WHERE n = ?`, count, n); err != nil {
		t.Fatal(err)
	}
}

// The read walks the open decisions' index and nothing else: its plan is
// one line over decisions_open, with no scan of the table and no
// temporary b-tree, whatever number of closed decisions are newer than the
// open ones; with the index gone it answers its error and hands nothing.
func TestOpenDecisionsWalkTheirIndex(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	var open []int64
	for i := range 3 {
		open = append(open, open1(t, l, run, fmt.Sprintf("g%d", i)))
	}
	slices.Reverse(open)
	walk := func(why string) {
		t.Helper()
		plan := openPlan(t, l)
		if len(plan) != 1 || !strings.Contains(plan[0], "USING INDEX decisions_open") || !strings.HasPrefix(plan[0], "SCAN d") && !strings.HasPrefix(plan[0], "SEARCH d") ||
			strings.Contains(plan[0], "TEMP B-TREE") {
			t.Fatalf("%s: the read's plan %q walks no index of the open decisions alone", why, plan)
		}
		t.Logf("%s: the plan %q", why, plan)
		var got []DecisionRow
		if more, err := l.OpenDecisionsNewest(ctx, 0, func(d DecisionRow) bool { got = append(got, d); return true }); err != nil || more || !slices.Equal(nOf(got), open) {
			t.Fatalf("%s: handed %v more %v %v, want %v", why, nOf(got), more, err, open)
		}
	}
	walk("no closed decision")
	closedCopies(t, l, open[0], 300)
	walk("300 closed decisions newer")
	closedCopies(t, l, open[0], 2700)
	walk("3000 closed decisions newer")

	if _, err := l.db.Exec("DROP INDEX decisions_open"); err != nil {
		t.Fatal(err)
	}
	handed := 0
	if _, err := l.OpenDecisionsNewest(ctx, 0, func(DecisionRow) bool { handed++; return true }); !errors.Is(err, ErrUnavailable) || handed != 0 {
		t.Fatalf("with the index dropped: %d handed, %v", handed, err)
	}
}

// open1 opens a gate decision of run, answering its number.
func open1(t *testing.T, l *Ledger, run, gate string) int64 {
	t.Helper()
	return open(t, l, gateDecision(run, gate))
}

// hasOpenIndex says the database l reads holds decisions_open.
func hasOpenIndex(t *testing.T, l *Ledger) bool {
	t.Helper()
	var n int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'decisions_open'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// The index is the owner's to make: a database as the version before left
// it (no such index) gets it when the owner opens it, and has none read
// only, where the read answers its error. One whose index cannot be made
// is still the owner's, every other read and write working; only the read
// that names the index answers why.
func TestTheOwnerMakesTheOpenIndex(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	const run = "team-00000001"
	l := openLedger(t, dir)
	newRun(t, l, run)
	n := open1(t, l, run, "g1")
	l.Close()
	rawExec(t, dir, "DROP INDEX decisions_open")

	reader := openReadOnly(t, dir)
	handed := 0
	if _, err := reader.OpenDecisionsNewest(ctx, 0, func(DecisionRow) bool { handed++; return true }); !errors.Is(err, ErrUnavailable) || handed != 0 || hasOpenIndex(t, reader) {
		t.Fatalf("read only, with no index: %d handed, %v", handed, err)
	}
	reader.Close()
	owner := openLedger(t, dir)
	var got []int64
	if _, err := owner.OpenDecisionsNewest(ctx, 0, func(d DecisionRow) bool { got = append(got, d.N); return true }); err != nil || !hasOpenIndex(t, owner) || !slices.Equal(got, []int64{n}) {
		t.Fatalf("opened by the owner: %v %v", got, err)
	}
	owner.Close()

	old := accessPaths
	t.Cleanup(func() { accessPaths = old })
	accessPaths = []accessPath{{"decisions_open", "CREATE INDEX decisions_open ON no_such_table(n)"}}
	failed := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	if ro, why := failed.ReadOnly(); ro {
		t.Fatalf("an index not made made the ledger read-only: %s", why)
	}
	newRun(t, failed, run)
	m := open1(t, failed, run, "g1")
	setState(t, failed, run, m, DecisionCancelled, "")
	if rec := record(t, failed, m); rec.State != DecisionCancelled {
		t.Fatalf("a write after an index not made: %+v", rec.DecisionRow)
	}
	if _, err := failed.OpenDecisionsNewest(ctx, 0, func(DecisionRow) bool { return true }); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "could not be made") {
		t.Fatalf("the read of an index not made: %v", err)
	}
}

// An index of the open decisions' name made over another expression, or
// over another state, before the owner prepares the database: the ledger
// is the owner's and writable, every other read and write works, and the
// read answers its error and hands nothing. The definition the owner makes
// is the one SQLite keeps.
func TestTheOwnerReadsTheOpenIndexBack(t *testing.T) {
	ctx := context.Background()
	const run = "team-00000001"
	for _, other := range []string{
		"CREATE INDEX decisions_open ON decisions(n)",
		"CREATE INDEX decisions_open ON decisions(n) WHERE state = 'cancelled'",
	} {
		dir := filepath.Join(t.TempDir(), "runs")
		l := openLedger(t, dir)
		newRun(t, l, run)
		n := open1(t, l, run, "g1")
		var def string
		if err := l.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'decisions_open'").Scan(&def); err != nil || def != accessPaths[0].def {
			t.Fatalf("the index the owner made is kept as %q: %v", def, err)
		}
		l.Close()
		rawExec(t, dir, "DROP INDEX decisions_open")
		rawExec(t, dir, other)

		owner := openLedger(t, dir)
		if ro, why := owner.ReadOnly(); ro {
			t.Fatalf("%s: the ledger opened read-only: %s", other, why)
		}
		handed := 0
		if _, err := owner.OpenDecisionsNewest(ctx, 0, func(DecisionRow) bool { handed++; return true }); !errors.Is(err, ErrUnavailable) || handed != 0 {
			t.Fatalf("%s: the read handed %d: %v", other, handed, err)
		}
		m := open1(t, owner, run, "g2")
		setState(t, owner, run, n, DecisionCancelled, "")
		if rec := record(t, owner, n); rec.State != DecisionCancelled {
			t.Fatalf("%s: a write: %+v", other, rec.DecisionRow)
		}
		if rec := record(t, owner, m); rec.State != DecisionOpen {
			t.Fatalf("%s: a read: %+v", other, rec.DecisionRow)
		}
		owner.Close()
	}
}

// Rows written and changed by statements that know nothing of the index,
// as a binary without it writes: the read hands exactly what a plain
// select of the open rows, told to use no index, gives.
func TestOpenIndexUnderPlainWrites(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	var ns []int64
	for i := range 6 {
		ns = append(ns, open1(t, l, run, fmt.Sprintf("g%d", i)))
	}
	setState(t, l, run, ns[0], DecisionCancelled, "")
	setState(t, l, run, ns[2], DecisionSuperseded, "")
	closedCopies(t, l, ns[1], 20)
	for _, stmt := range []string{
		"UPDATE decisions SET state = 'open' WHERE n IN (SELECT n FROM decisions WHERE state = 'cancelled' ORDER BY n LIMIT 7 OFFSET 3)",
		fmt.Sprintf("UPDATE decisions SET state = 'applied' WHERE n = %d", ns[4]),
		fmt.Sprintf("DELETE FROM decisions WHERE n = %d", ns[5]),
		fmt.Sprintf("UPDATE decisions SET why = 'moved' WHERE n = %d", ns[3]),
	} {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rows, err := l.db.Query("SELECT " + decisionCols + " FROM decisions NOT INDEXED WHERE state = 'open' ORDER BY n DESC")
	if err != nil {
		t.Fatal(err)
	}
	var want []DecisionRow
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, d)
	}
	rows.Close()
	var got []DecisionRow
	if _, err := l.OpenDecisionsNewest(ctx, 0, func(d DecisionRow) bool { got = append(got, d); return true }); err != nil || !slices.Equal(got, want) || len(want) < 8 {
		t.Fatalf("handed %v, the plain select %v: %v", nOf(got), nOf(want), err)
	}
}
