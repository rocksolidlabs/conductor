package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The decision tables (DR1, docs/plans/decisions.md, PR-1): the schema and
// its migration, the writers and their bounds, the readers, the prune's
// refusal and hand-back, the recovery and the snapshot.

// version8Added is what version 8 added, dropped: a database of this
// version made a version 7 one (children before the table they cite).
var version8Added = []string{
	"DROP TABLE decision_refunds",
	"DROP TABLE decision_holds",
	"DROP TABLE decision_history",
	"DROP TABLE decision_responses",
	"DROP TABLE decisions",
}

const (
	localPolicy   = `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`
	workbenchAuth = `{"grant":"admin","via":"workbench","boot":"boot-1"}`
	identityAuth  = `{"grant":"admin","via":"identity","subject":"ana","boot":"boot-1"}`
	routerAuth    = `{"via":"router","boot":"boot-1"}`
)

// request is a request of type typ as its column keeps it.
func request(typ string) string {
	switch typ {
	case DecisionApproval:
		return `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"authorizes":"deploy may start","subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`
	case DecisionAssignment:
		return `{"question":"Run the migration on staging","subject":{"kind":"commit","commit":"abc123"}}`
	}
	return `{"question":"Which table?","context":"two fit","subject":{"kind":"commit","commit":"abc123"}}`
}

// agentDecision is a clarification attempt n of task asks in run.
func agentDecision(run, task string, n int, requestID string) DecisionRow {
	return DecisionRow{RunID: run, Origin: OriginAgent, Type: DecisionClarification, TaskID: task, Attempt: n, Member: "lead",
		RequestID: requestID, Fingerprint: "fp-" + requestID, Request: request(DecisionClarification), Policy: localPolicy}
}

// gateDecision is a plan decision gate of run.
func gateDecision(run, gate string) DecisionRow {
	return DecisionRow{RunID: run, Origin: OriginPlan, Type: DecisionApproval, Gate: gate, Plan: "pl-1", PlanDigest: "d1",
		Fingerprint: "fp-" + gate, Request: request(DecisionApproval), Policy: localPolicy}
}

// in runs fn in a transition of run, failing the test on its error.
func in(t *testing.T, l *Ledger, run string, fn func(tx *Tx) error) {
	t.Helper()
	if _, err := l.Transition(context.Background(), run, fn); err != nil {
		t.Fatal(err)
	}
}

// try runs fn in a transition of run and answers its error.
func try(l *Ledger, run string, fn func(tx *Tx) error) error {
	_, err := l.Transition(context.Background(), run, fn)
	return err
}

// open opens d in a transition of its run and answers its n.
func open(t *testing.T, l *Ledger, d DecisionRow) int64 {
	t.Helper()
	var n int64
	in(t, l, d.RunID, func(tx *Tx) error {
		got, created, err := tx.OpenDecision(d)
		if err == nil && !created {
			err = fmt.Errorf("%s was not created", got.ID())
		}
		n = got.N
		return err
	})
	return n
}

// tasksOf puts tasks of run, each with attempt 1.
func tasksOf(t *testing.T, l *Ledger, run string, tasks ...string) {
	t.Helper()
	for _, task := range tasks {
		attemptOf(t, l, run, task, 1, AttemptRunning)
	}
}

// continuation makes run to, the continuation of from (finished first),
// with the tasks given.
func continuation(t *testing.T, l *Ledger, from, to string, tasks ...string) {
	t.Helper()
	in(t, l, to, func(tx *Tx) error {
		if err := tx.CreateRun(RunRow{ID: to, CrewID: "team", Crew: `{"id":"team"}`, Base: "abc123", State: RunRunning, ContinuedFrom: from, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Continued(from)
	})
	tasksOf(t, l, to, tasks...)
}

// answer responds to decision n with the accepted response id under auth
// and answers it at its rev, in a transition of run.
func answer(t *testing.T, l *Ledger, run string, n int64, id, auth string) {
	t.Helper()
	in(t, l, run, func(tx *Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		if _, err := tx.Respond(n, ResponseRow{Actor: "admin", Authority: auth, ResponseID: id, Fingerprint: "fp-" + id, Status: ResponseAccepted, Receipt: `{"words":"yes"}`}); err != nil {
			return err
		}
		if ok, err := tx.Answer(n, run, d.Rev); err != nil || !ok {
			return fmt.Errorf("the answer of %s: %v %v", d.ID(), ok, err)
		}
		return nil
	})
}

// setState puts decision n in state, its rev bumped, in a transition of
// run.
func setState(t *testing.T, l *Ledger, run string, n int64, state, applied string) {
	t.Helper()
	in(t, l, run, func(tx *Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		d.State, d.Rev, d.Applied = state, d.Rev+1, applied
		return tx.PutDecision(d)
	})
}

func record(t *testing.T, l *Ledger, n int64) DecisionRecord {
	t.Helper()
	rec, ok, err := l.Decision(context.Background(), n)
	if err != nil || !ok {
		t.Fatalf("decision %s: %v %v", DecisionID(n), ok, err)
	}
	return rec
}

func countRows(t *testing.T, l *Ledger, query string, args ...any) int {
	t.Helper()
	var n int
	if err := l.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// tableRows is every row of a table as text, in rowid order.
func tableRows(t *testing.T, l *Ledger, table string) []string {
	t.Helper()
	rows, err := l.db.Query(fmt.Sprintf("SELECT * FROM %q ORDER BY rowid", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
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
		out = append(out, fmt.Sprintf("%v", vals))
	}
	return out
}

func schemaNames(t *testing.T, l *Ledger, kind string) []string {
	t.Helper()
	rows, err := l.db.Query("SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name", kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

var v7Tables = []string{"attempts", "checks", "commands", "events", "messages", "prerequisites", "runs", "schema_version", "tasks", "trees"}

// version7Like makes dir a data directory whose database is a version 7
// one as main's code made it: a fresh file of the version 7 statements
// (the schema without the decision tables, so no sqlite_sequence either),
// holding the version 7 tables' rows of src's database.
func version7Like(t *testing.T, src, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(filepath.Join(dir, File), false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the ATTACH and the pragma are the connection's
	stmts := slices.Clone(schema[:len(schema)-len(decisionTables)-len(decisionsRequestV9)])
	// The rows are copied as they are, in no parent-first order.
	stmts = append(stmts, "INSERT INTO schema_version (version) VALUES (7)", "PRAGMA foreign_keys = OFF", "ATTACH DATABASE '"+filepath.Join(src, File)+"' AS src")
	for _, table := range v7Tables {
		if table != "schema_version" {
			stmts = append(stmts, fmt.Sprintf("INSERT INTO main.%q SELECT * FROM src.%q", table, table))
		}
	}
	for _, stmt := range append(stmts, "DETACH DATABASE src") {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// rawExec runs statements on dir's database through a connection of its
// own, the ledger closed.
func rawExec(t *testing.T, dir string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(filepath.Join(dir, File), false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// A version 7 database, made from main's schema, is brought to version 8
// as it opens: five tables and four indexes are added (and the
// AUTOINCREMENT's sqlite_sequence, made inside the migration), every
// table it had keeps its rows as they were, and the new tables are empty.
// A migration that fails leaves the database at version 7 as it was,
// read-only, with none of the version's tables, and the next open, the
// cause gone, migrates.
func TestMigrationSevenToEightAddsOnlyTables(t *testing.T) {
	seeded := filepath.Join(t.TempDir(), "seed")
	l := openLedger(t, seeded)
	seedWhere(t, l, WhereNative)
	finish(t, l, "team-00000001")
	l.Close()
	dir := filepath.Join(t.TempDir(), "runs")
	version7Like(t, seeded, dir)

	v7 := openReadOnly(t, dir)
	if v := dbVersion(t, v7); v != 7 {
		t.Fatalf("the fixture is version %d", v)
	}
	if got := schemaNames(t, v7, "table"); !slices.Equal(got, v7Tables) {
		t.Fatalf("the version 7 fixture's tables: %v", got)
	}
	if got := schemaNames(t, v7, "index"); len(got) != 0 {
		t.Fatalf("the version 7 fixture's indexes: %v", got)
	}
	if n := countRows(t, v7, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'sqlite_sequence'"); n != 0 {
		t.Fatal("the version 7 fixture has a sqlite_sequence")
	}
	// Every table's rows but the version's own.
	kept := slices.DeleteFunc(slices.Clone(v7Tables), func(s string) bool { return s == "schema_version" })
	before := map[string][]string{}
	for _, table := range kept {
		before[table] = tableRows(t, v7, table)
		if len(before[table]) == 0 && table != "commands" && table != "messages" && table != "trees" {
			t.Fatalf("the fixture's %s holds no rows", table)
		}
	}
	v7.Close()

	// A table of the version's name already there: the migration fails,
	// and nothing of it is kept.
	rawExec(t, dir, "CREATE TABLE decision_holds (x)")
	failed := openLedger(t, dir)
	if ro, why := failed.ReadOnly(); !ro || !strings.Contains(why, "decision_holds") {
		t.Fatalf("a failed migration: read-only %v: %s", ro, why)
	}
	if v := dbVersion(t, failed); v != 7 {
		t.Fatalf("version %d after a failed migration", v)
	}
	if got := schemaNames(t, failed, "table"); !slices.Equal(got, slices.Sorted(slices.Values(append(slices.Clone(v7Tables), "decision_holds")))) {
		t.Fatalf("tables after a failed migration: %v", got)
	}
	if _, ok, err := failed.Decision(context.Background(), 1); ok || err != nil {
		t.Fatalf("a decision after a failed migration: %v %v", ok, err)
	}
	failed.Close()
	rawExec(t, dir, "DROP TABLE decision_holds")

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != Version {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	wantTables := slices.Sorted(slices.Values(append(slices.Clone(v7Tables), "decisions", "decision_responses", "decision_history", "decision_holds", "decision_refunds")))
	if got := schemaNames(t, again, "table"); !slices.Equal(got, wantTables) {
		t.Fatalf("tables after the migration: %v", got)
	}
	if got, want := schemaNames(t, again, "index"), []string{"decision_holds_decision", "decision_ops", "decisions_current", "decisions_request"}; !slices.Equal(got, want) {
		t.Fatalf("indexes after the migration: %v", got)
	}
	if n := countRows(t, again, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'sqlite_sequence'"); n != 1 {
		t.Fatal("no sqlite_sequence after the migration")
	}
	for _, table := range kept {
		if got := tableRows(t, again, table); !slices.Equal(got, before[table]) {
			t.Fatalf("%s after the migration:\n%v\nbefore:\n%v", table, got, before[table])
		}
	}
	for _, table := range []string{"decisions", "decision_responses", "decision_history", "decision_holds", "decision_refunds"} {
		if n := countRows(t, again, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%s holds %d rows after the migration", table, n)
		}
	}
	// The tables work: a decision of the migrated run's own is dr-1.
	newRun(t, again, "team-00000002")
	tasksOf(t, again, "team-00000002", "build")
	if n := open(t, again, agentDecision("team-00000002", "build", 1, "q1")); n != 1 {
		t.Fatalf("the first decision is %s", DecisionID(n))
	}
}

// openReadOnly opens dir's ledger as a reader: the owner lock another's,
// not waited for, so it never migrates.
func openReadOnly(t *testing.T, dir string) *Ledger {
	t.Helper()
	old := takeLock
	takeLock = func(string) (*ownerLock, string, bool, error) {
		return nil, "another server owns the ledger (test)", false, nil
	}
	defer func() { takeLock = old }()
	l := openLedger(t, dir)
	if ro, _ := l.ReadOnly(); !ro {
		t.Fatal("the reader opened as the owner")
	}
	return l
}

// A decision's n is never given again, not even once its run is pruned.
func TestDecisionIDsAreNeverReused(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	newRun(t, l, "team-00000001")
	n1 := open(t, l, gateDecision("team-00000001", "g1"))
	setState(t, l, "team-00000001", n1, DecisionCancelled, "")
	finish(t, l, "team-00000001")
	if err := l.Prune(context.Background(), "team-00000001"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Decision(context.Background(), n1); ok {
		t.Fatal("the pruned run's decision stayed")
	}
	newRun(t, l, "team-00000002")
	if n2 := open(t, l, gateDecision("team-00000002", "g1")); n2 <= n1 {
		t.Fatalf("the next decision is %s after %s", DecisionID(n2), DecisionID(n1))
	}
	for _, id := range []string{"dr-1", "dr-42"} {
		if n, ok := ParseDecisionID(id); !ok || DecisionID(n) != id {
			t.Fatalf("%s parsed as %d %v", id, n, ok)
		}
	}
	for _, id := range []string{"", "dr-", "dr-0", "dr-01", "dr-1x", "DR-1", "dr--1"} {
		if _, ok := ParseDecisionID(id); ok {
			t.Fatalf("%q parsed", id)
		}
	}
}

// A request id the member sent before for the task is the same decision:
// the row it opened, nothing written; another body is a conflict; another
// member's, and the same member's for another task, are decisions of
// their own, read back by DecisionByRequest only for their own task.
func TestARepeatedRequestIDIsTheSameDecision(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	in(t, l, run, func(tx *Tx) error {
		again, created, err := tx.OpenDecision(agentDecision(run, "build", 1, "q1"))
		if err != nil || created || again.N != n || again.State != DecisionOpen || again.Rev != 1 {
			return fmt.Errorf("the repeat: %+v %v %v", again, created, err)
		}
		other := agentDecision(run, "build", 1, "q1")
		other.Fingerprint = "fp-other"
		if _, _, err := tx.OpenDecision(other); !errors.Is(err, ErrDecisionConflict) {
			return fmt.Errorf("another body: %v", err)
		}
		mate := agentDecision(run, "build", 1, "q1")
		mate.Member = "mate"
		if d, created, err := tx.OpenDecision(mate); err != nil || !created || d.N == n {
			return fmt.Errorf("another member's: %+v %v %v", d, created, err)
		}
		docs := agentDecision(run, "docs", 1, "q1")
		docs.Fingerprint = "fp-other"
		if d, created, err := tx.OpenDecision(docs); err != nil || !created || d.N == n {
			return fmt.Errorf("the member's for another task: %+v %v %v", d, created, err)
		}
		return nil
	})
	if c := countRows(t, l, "SELECT COUNT(*) FROM decisions"); c != 3 {
		t.Fatalf("%d decisions", c)
	}
	for task, want := range map[string]int64{"build": n, "docs": n + 2, "lint": 0} {
		row, ok, err := l.DecisionByRequest(context.Background(), run, "lead", task, "q1")
		if err != nil || ok != (want != 0) || row.N != want {
			t.Fatalf("task %s's q1: %+v %v %v", task, row, ok, err)
		}
	}
}

// A response id answers its receipt, accepted or refused alike, and
// another body under it is a conflict; another actor's id is its own.
func TestAResponseIDAnswersItsReceipt(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	refused := ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r1", Fingerprint: "fp-r1", Status: ResponseRefused, Receipt: `{"code":"stale_decision"}`}
	accepted := ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r2", Fingerprint: "fp-r2", Status: ResponseAccepted, Receipt: `{"words":"approved"}`}
	in(t, l, run, func(tx *Tx) error {
		for i, r := range []ResponseRow{refused, accepted} {
			got, err := tx.Respond(n, r)
			if err != nil || got.Repeat || !got.Kept || got.Row.K != i+1 {
				return fmt.Errorf("response %s: %+v %v", r.ResponseID, got, err)
			}
		}
		return nil
	})
	in(t, l, run, func(tx *Tx) error {
		for i, r := range []ResponseRow{refused, accepted} {
			again := r
			again.Receipt = `{"words":"sent again"}`
			got, err := tx.Respond(n, again)
			if err != nil || !got.Repeat || got.Row.K != i+1 || got.Row.Receipt != r.Receipt || got.Row.Status != r.Status {
				return fmt.Errorf("response %s again: %+v %v", r.ResponseID, got, err)
			}
			again.Fingerprint = "fp-other"
			if _, err := tx.Respond(n, again); !errors.Is(err, ErrDecisionConflict) {
				return fmt.Errorf("response %s with another body: %v", r.ResponseID, err)
			}
		}
		other := refused
		other.Actor = "user:ana"
		if got, err := tx.Respond(n, other); err != nil || got.Repeat || got.Row.K != 3 {
			return fmt.Errorf("another actor's r1: %+v %v", got, err)
		}
		// A second acceptance: the decision accepts one in its life.
		second := accepted
		second.ResponseID, second.Fingerprint = "r3", "fp-r3"
		if _, err := tx.Respond(n, second); !errors.Is(err, ErrDecisionState) {
			return fmt.Errorf("a second acceptance: %v", err)
		}
		return nil
	})
	if rec := record(t, l, n); len(rec.Responses) != 3 || rec.Responses[1].Status != ResponseAccepted {
		t.Fatalf("responses: %+v", rec.Responses)
	}
}

// A run that opened or holds a decision that is not final is not pruned;
// once the decision is final, it goes with the run.
func TestAnOpenDecisionBlocksThePrune(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "deploy")
	n := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error {
		if err := tx.Hold(run, "deploy", n); err != nil {
			return err
		}
		_, _, err := tx.History(n, HistoryRow{Kind: HistoryOpened, Detail: `{"by":"plan"}`})
		return err
	})
	finish(t, l, run)
	if err := l.Prune(ctx, run); !errors.Is(err, ErrNotPrunable) || !strings.Contains(err.Error(), "dr-1 is open") {
		t.Fatalf("an open decision's run pruned: %v", err)
	}
	answer(t, l, run, n, "r1", workbenchAuth)
	if err := l.Prune(ctx, run); !errors.Is(err, ErrNotPrunable) || !strings.Contains(err.Error(), "is answered") {
		t.Fatalf("an answered decision's run pruned: %v", err)
	}
	in(t, l, run, func(tx *Tx) error {
		if _, err := tx.Release(run, "deploy", n); err != nil {
			return err
		}
		d, _, _ := tx.Decision(n)
		d.State, d.Rev, d.Applied = DecisionApplied, d.Rev+1, `{"how":"released"}`
		return tx.PutDecision(d)
	})
	if err := l.Prune(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"decisions", "decision_responses", "decision_history", "decision_holds"} {
		if c := countRows(t, l, "SELECT COUNT(*) FROM "+table); c != 0 {
			t.Fatalf("%s holds %d rows after the prune", table, c)
		}
	}
}

// A final decision a continuation holds and an ancestor opened is handed
// back to the run the pruned one continued, its responses and history
// kept, the pruned run's hold rows gone and the ancestor's released rows
// kept; the ancestor takes it when it is pruned. A chain A, B, C pruned
// from C back hands it back one run at a time.
func TestPruningAContinuationHandsItsDecisionsBack(t *testing.T) {
	ctx := context.Background()
	t.Run("A and B", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const a, b = "team-00000001", "team-00000002"
		newRun(t, l, a)
		tasksOf(t, l, a, "deploy")
		n := open(t, l, gateDecision(a, "g1"))
		in(t, l, a, func(tx *Tx) error { return tx.Hold(a, "deploy", n) })
		finish(t, l, a)
		continuation(t, l, a, b, "deploy")
		in(t, l, b, func(tx *Tx) error {
			if _, err := tx.MoveDecisions(a, b); err != nil {
				return err
			}
			_, err := tx.MoveHolds(a, b, tx.Now())
			return err
		})
		answer(t, l, b, n, "r1", workbenchAuth)
		in(t, l, b, func(tx *Tx) error {
			if _, err := tx.Release(b, "deploy", n); err != nil {
				return err
			}
			d, _, _ := tx.Decision(n)
			d.State, d.Rev, d.Applied = DecisionApplied, d.Rev+1, `{"how":"released"}`
			if err := tx.PutDecision(d); err != nil {
				return err
			}
			_, _, err := tx.History(n, HistoryRow{Kind: HistoryApplied})
			return err
		})
		finish(t, l, b)
		if err := l.Prune(ctx, a); !errors.Is(err, ErrNotPrunable) {
			t.Fatalf("a cited run pruned: %v", err)
		}
		if err := l.Prune(ctx, b); err != nil {
			t.Fatal(err)
		}
		rec := record(t, l, n)
		if rec.CurrentRun != a || rec.RunID != a || rec.State != DecisionApplied || len(rec.Responses) != 1 || len(rec.History) != 1 {
			t.Fatalf("after B's prune: %+v", rec)
		}
		if hs, _ := l.Holds(ctx, b); len(hs) != 0 {
			t.Fatalf("B's holds stayed: %+v", hs)
		}
		if hs, _ := l.Holds(ctx, a); len(hs) != 1 || hs[0].ReleasedAt.IsZero() {
			t.Fatalf("A's holds: %+v", hs)
		}
		// An operation on it is a transition of a run that exists.
		in(t, l, a, func(tx *Tx) error {
			_, _, err := tx.History(n, HistoryRow{Kind: HistoryCommented, By: "admin", Detail: `{"text":"noted"}`, OpID: "op-1", Fingerprint: "fp-op-1"})
			return err
		})
		if err := l.Prune(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := l.Decision(ctx, n); ok {
			t.Fatal("A's decision stayed after A's prune")
		}
	})
	t.Run("A, B and C", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const a, b, c = "team-00000001", "team-00000002", "team-00000003"
		newRun(t, l, a)
		tasksOf(t, l, a, "deploy")
		n := open(t, l, gateDecision(a, "g1"))
		finish(t, l, a)
		for _, pair := range [][2]string{{a, b}, {b, c}} {
			continuation(t, l, pair[0], pair[1], "deploy")
			in(t, l, pair[1], func(tx *Tx) error {
				moved, err := tx.MoveDecisions(pair[0], pair[1])
				if err == nil && (len(moved) != 1 || moved[0].CurrentRun != pair[1]) {
					err = fmt.Errorf("moved to %s: %+v", pair[1], moved)
				}
				return err
			})
			finish(t, l, pair[1])
		}
		setState(t, l, c, n, DecisionCancelled, "")
		for _, step := range []struct{ prune, holder string }{{c, b}, {b, a}} {
			if err := l.Prune(ctx, step.prune); err != nil {
				t.Fatal(err)
			}
			if rec := record(t, l, n); rec.CurrentRun != step.holder || rec.State != DecisionCancelled {
				t.Fatalf("after %s's prune: held by %s, %s", step.prune, rec.CurrentRun, rec.State)
			}
		}
		if err := l.Prune(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := l.Decision(ctx, n); ok {
			t.Fatal("the chain's decision stayed")
		}
	})
}

// Every bound of the store, checked in the transaction that would pass
// it: 4 open per attempt, 64 open per run, 1,000 per run in all, 32
// responses, 32 operations, the receipts' and the request's bounds; an
// answered decision with an identity's response counts as open and as a
// place.
func TestDecisionBounds(t *testing.T) {
	ctx := context.Background()
	t.Run("open per attempt", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const run = "team-00000001"
		newRun(t, l, run)
		tasksOf(t, l, run, "build")
		var ns []int64
		for i := range MaxOpenPerAttempt {
			ns = append(ns, open(t, l, agentDecision(run, "build", 1, fmt.Sprintf("q%d", i))))
		}
		fifth := func(id string) error {
			return try(l, run, func(tx *Tx) error {
				_, _, err := tx.OpenDecision(agentDecision(run, "build", 1, id))
				return err
			})
		}
		if err := fifth("q-over"); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("a fifth open of the attempt: %v", err)
		}
		open(t, l, agentDecision(run, "build", 2, "q-other")) // another attempt's
		answer(t, l, run, ns[3], "r-id", identityAuth)
		if err := fifth("q-over"); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("an identity's answer did not count as open: %v", err)
		}
		setState(t, l, run, ns[0], DecisionCancelled, "")
		if err := fifth("q-room"); err != nil {
			t.Fatalf("a place freed: %v", err)
		}
		answer(t, l, run, ns[2], "r-wb", workbenchAuth)
		if err := fifth("q-room-2"); err != nil {
			t.Fatalf("a workbench answer counted as open: %v", err)
		}
		if err := fifth("q-over-2"); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("past the attempt's bound again: %v", err)
		}
	})
	t.Run("open per run", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const run = "team-00000001"
		newRun(t, l, run)
		in(t, l, run, func(tx *Tx) error {
			for i := range MaxOpenPerRun {
				if _, _, err := tx.OpenDecision(gateDecision(run, fmt.Sprintf("g%d", i))); err != nil {
					return err
				}
			}
			return nil
		})
		if err := try(l, run, func(tx *Tx) error {
			_, _, err := tx.OpenDecision(gateDecision(run, "g-over"))
			return err
		}); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("a 65th open: %v", err)
		}
	})
	t.Run("in all per run, a replacement's place", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const run = "team-00000001"
		newRun(t, l, run)
		// 998 decisions of the run, final, put as the writers would leave
		// them (the writers' own 998 openings take half a minute under the
		// race detector).
		g := gateDecision(run, "g")
		if _, err := l.db.Exec(`WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < ?)
			INSERT INTO decisions (run_id, current_run, origin, type, gate, plan, plan_digest, fingerprint, request, policy, state, rev, created_at, closed_at)
			SELECT ?, ?, ?, ?, 'g' || i, ?, ?, ?, ?, ?, ?, 2, ?, ? FROM k`,
			MaxDecisionsPerRun-2, run, run, g.Origin, g.Type, g.Plan, g.PlanDigest, g.Fingerprint, g.Request, g.Policy, DecisionCancelled, stamp(time.Now()), stamp(time.Now())); err != nil {
			t.Fatal(err)
		}
		if c := countRows(t, l, "SELECT COUNT(*) FROM decisions WHERE run_id = ?", run); c != MaxDecisionsPerRun-2 {
			t.Fatalf("%d decisions put", c)
		}
		x := open(t, l, gateDecision(run, "x")) // the 999th
		openOne := func(gate string) error {
			return try(l, run, func(tx *Tx) error {
				_, _, err := tx.OpenDecision(gateDecision(run, gate))
				return err
			})
		}
		// An identity's answer holds a place: 999 and the place are 1,000.
		answer(t, l, run, x, "r-id", identityAuth)
		if err := openOne("over"); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("an opening past the place: %v", err)
		}
		// Its replacement takes the place it held, in the order
		// OpenDecision says, in one transition: the replaced one made
		// final, the replacement opened, superseded_by set. Opened first,
		// it finds the place taken.
		if err := try(l, run, func(tx *Tx) error {
			_, _, err := tx.OpenDecision(gateDecision(run, "replacement"))
			return err
		}); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("the replacement opened before the replaced one is final: %v", err)
		}
		var rn int64
		in(t, l, run, func(tx *Tx) error {
			d, _, err := tx.Decision(x)
			if err != nil {
				return err
			}
			d.State, d.Rev, d.Why = DecisionSuperseded, d.Rev+1, "the answer's identity could not be rechecked"
			if err := tx.PutDecision(d); err != nil {
				return err
			}
			r, created, err := tx.OpenDecision(gateDecision(run, "replacement"))
			if err != nil || !created {
				return fmt.Errorf("the replacement: %v %v", created, err)
			}
			rn, d.SupersededBy = r.N, r.ID()
			return tx.PutDecision(d)
		})
		if rec := record(t, l, x); rec.State != DecisionSuperseded || rec.SupersededBy != DecisionID(rn) {
			t.Fatalf("the replaced one: %s, superseded by %q", rec.State, rec.SupersededBy)
		}
		if err := openOne("over-2"); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("a 1,001st: %v", err)
		}
		// An identity's answer needs a place there is no room for; the
		// workbench's needs none.
		ds, err := l.Decisions(ctx, DecisionFilter{CurrentRun: run, States: []string{DecisionOpen}})
		if err != nil || len(ds) != 1 {
			t.Fatalf("the open one: %+v %v", ds, err)
		}
		r := ds[0].N
		if err := try(l, run, func(tx *Tx) error {
			_, err := tx.Respond(r, ResponseRow{Actor: "user:ana", Authority: identityAuth, ResponseID: "r1", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`})
			return err
		}); !errors.Is(err, ErrDecisionLimit) {
			t.Fatalf("an identity's answer with no room: %v", err)
		}
		answer(t, l, run, r, "r-wb", workbenchAuth)
	})
	t.Run("responses, operations and sizes", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		const run = "team-00000001"
		newRun(t, l, run)
		n := open(t, l, gateDecision(run, "g1"))
		in(t, l, run, func(tx *Tx) error {
			for i := range MaxDecisionRefusals {
				if got, err := tx.Respond(n, ResponseRow{Actor: "router", Authority: routerAuth, ResponseID: fmt.Sprintf("r%d", i), Fingerprint: "fp", Status: ResponseRefused, Receipt: `{}`}); err != nil || !got.Kept {
					return fmt.Errorf("refusal %d: %+v %v", i, got, err)
				}
			}
			if got, err := tx.Respond(n, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "yes", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`}); err != nil || got.Row.K != MaxDecisionResponses {
				return fmt.Errorf("the answer: %+v %v", got, err)
			}
			if got, err := tx.Respond(n, ResponseRow{Actor: "router", Authority: routerAuth, ResponseID: "late", Fingerprint: "fp", Status: ResponseRefused, Receipt: `{}`}); err != nil || got.Kept {
				return fmt.Errorf("a refusal past the bound: %+v %v", got, err)
			}
			for i := range MaxDecisionOperations {
				kind := []string{HistoryCommented, HistoryInfoRequested, HistoryRouted}[i%3]
				if _, _, err := tx.History(n, HistoryRow{Kind: kind, By: "admin", OpID: fmt.Sprintf("op%d", i), Fingerprint: "fp"}); err != nil {
					return fmt.Errorf("operation %d: %v", i, err)
				}
			}
			for _, kind := range []string{HistoryCommented, HistoryInfoRequested, HistoryRouted} {
				if _, _, err := tx.History(n, HistoryRow{Kind: kind, By: "admin", OpID: "op-over-" + kind, Fingerprint: "fp"}); !errors.Is(err, ErrDecisionLimit) {
					return fmt.Errorf("a 33rd operation (%s): %v", kind, err)
				}
			}
			// A routing at the opening is no operation: it has its own two.
			for i := range 2 {
				if _, _, err := tx.History(n, HistoryRow{Kind: HistoryRouted, Detail: fmt.Sprintf(`{"rule":%d}`, i)}); err != nil {
					return err
				}
			}
			return nil
		})
		if c := countRows(t, l, "SELECT COUNT(*) FROM decision_responses WHERE decision = ?", n); c != MaxDecisionResponses {
			t.Fatalf("%d responses kept", c)
		}
		n2 := open(t, l, gateDecision(run, "g2"))
		for name, fn := range map[string]func(tx *Tx) error{
			"a receipt past 8 KiB": func(tx *Tx) error {
				_, err := tx.Respond(n2, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "big", Fingerprint: "fp", Status: ResponseRefused,
					Receipt: `{"words":"` + strings.Repeat("x", MaxDecisionReceipt) + `"}`})
				return err
			},
			"an application receipt past 4 KiB": func(tx *Tx) error {
				d, _, _ := tx.Decision(n2)
				d.Applied = `{"words":"` + strings.Repeat("x", MaxDecisionApplied) + `"}`
				return tx.PutDecision(d)
			},
			"a detail past 2 KiB": func(tx *Tx) error {
				_, _, err := tx.History(n2, HistoryRow{Kind: HistoryNotified, Detail: `"` + strings.Repeat("x", MaxHistoryDetail) + `"`})
				return err
			},
			"a request past 16 KiB": func(tx *Tx) error {
				d := gateDecision(run, "g-big")
				d.Request = `{"question":"q","context":"` + strings.Repeat("x", MaxDecisionRequest) + `","options":[{"label":"a"},{"label":"b"}],"subject":{"kind":"commit","commit":"c"}}`
				_, _, err := tx.OpenDecision(d)
				return err
			},
		} {
			if err := try(l, run, fn); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		// Applied only with its receipt.
		answer(t, l, run, n2, "r-wb", workbenchAuth)
		if err := try(l, run, func(tx *Tx) error {
			d, _, _ := tx.Decision(n2)
			d.State, d.Rev = DecisionApplied, d.Rev+1
			return tx.PutDecision(d)
		}); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("applied with no receipt: %v", err)
		}
		// An authority with a closing bracket after it is no authority.
		n3 := open(t, l, gateDecision(run, "g3"))
		if err := try(l, run, func(tx *Tx) error {
			_, err := tx.Respond(n3, ResponseRow{Actor: "admin", Authority: workbenchAuth + "]", ResponseID: "r", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`})
			return err
		}); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("an authority with text after it: %v", err)
		}
		for name, req := range map[string]string{
			"a closing bracket after it":   `{"question":"q","subject":{"kind":"commit","commit":"c"}}]`,
			"a closing brace after it":     `{"question":"q","subject":{"kind":"commit","commit":"c"}}}`,
			"a second value after it":      `{"question":"q","subject":{"kind":"commit","commit":"c"}} {}`,
			"a question of 501 characters": `{"question":"` + strings.Repeat("é", MaxDecisionQuestion+1) + `","subject":{"kind":"commit","commit":"c"}}`,
			"a question of two lines":      `{"question":"a\nb","subject":{"kind":"commit","commit":"c"}}`,
			"a context of 4,001":           `{"question":"q","context":"` + strings.Repeat("x", MaxDecisionContext+1) + `","subject":{"kind":"commit","commit":"c"}}`,
			"21 evidence refs":             `{"question":"q","evidence":[` + strings.TrimSuffix(strings.Repeat(`"ticket:CND-1",`, MaxDecisionEvidence+1), ",") + `],"subject":{"kind":"commit","commit":"c"}}`,
			"seven options":                `{"question":"q","options":[` + strings.TrimSuffix(strings.Repeat(`{"label":"a"},`, MaxDecisionOptions+1), ",") + `],"subject":{"kind":"commit","commit":"c"}}`,
			"five expertise labels":        `{"question":"q","expertise":["a","b","c","d","e"],"subject":{"kind":"commit","commit":"c"}}`,
			"a recommendation of nothing":  `{"question":"q","recommendation":0,"subject":{"kind":"commit","commit":"c"}}`,
			"no subject":                   `{"question":"q"}`,
			"a subject of no kind":         `{"question":"q","subject":{"kind":"file"}}`,
			"a candidate of no attempt":    `{"question":"q","subject":{"kind":"candidate","run":"r","task":"t","commit":"c"}}`,
			"a field it does not know":     `{"question":"q","subject":{"kind":"commit","commit":"c"},"priority":1}`,
		} {
			d := agentDecision(run, "build", 1, "bad")
			d.Request = req
			if err := try(l, run, func(tx *Tx) error { _, _, err := tx.OpenDecision(d); return err }); !errors.Is(err, ErrInvalidDecision) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		// An approval has two to six options, an assignment none.
		for typ, req := range map[string]string{DecisionApproval: request(DecisionClarification), DecisionAssignment: request(DecisionApproval)} {
			d := agentDecision(run, "build", 1, "bad")
			d.Type, d.Request = typ, req
			if err := try(l, run, func(tx *Tx) error { _, _, err := tx.OpenDecision(d); return err }); !errors.Is(err, ErrInvalidDecision) {
				t.Fatalf("a %s's options: %v", typ, err)
			}
		}
		// The baseline is one entry per ref, in order, or none yet.
		d := agentDecision(run, "build", 1, "ev")
		d.Request = `{"question":"q","evidence":["ticket:CND-1","commit:abc"],"subject":{"kind":"commit","commit":"c"}}`
		d.Evidence = `[{"ref":"commit:abc","seen":{}},{"ref":"ticket:CND-1","seen":{}}]`
		if err := try(l, run, func(tx *Tx) error { _, _, err := tx.OpenDecision(d); return err }); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("a baseline out of order: %v", err)
		}
		d.Evidence = `[{"ref":"ticket:CND-1","seen":{"state":"open","rev":3}},{"ref":"commit:abc","seen":{"state":"unavailable"}}]`
		if err := try(l, run, func(tx *Tx) error { _, _, err := tx.OpenDecision(d); return err }); err != nil {
			t.Fatalf("a baseline: %v", err)
		}
	})
}

// No run of refusals takes the answer's place: 31 refusals kept, a 32nd
// answered with no row (kept false), then the operator's answer accepted,
// applied, and its gate's task released. A rev nobody has read is never
// answered; a comment that brings the rev up to it lets the unkept
// refusal's retry be judged again as a new response, and accepted; a
// kept refusal's retry stays the refusal it was.
// TestAnAuthorityIsKeptInOneForm answers decisions with every spelling of
// an authority the decoder accepts and finds each kept as the one
// canonical text, so the SQL that counts the places replacements hold
// reads what the Go policy read: an identity's answer spelled {"Via":...}
// once kept its text, was an identity's to Respond and nobody's to
// placeCond, and held no place for its replacement (#664, review round 3).
func TestAnAuthorityIsKeptInOneForm(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	spellings := []struct {
		name, auth, want string
	}{
		{"canonical", identityAuth, identityAuth},
		{"a capital key", `{"grant":"admin","Via":"identity","subject":"ana","boot":"boot-1"}`, identityAuth},
		{"every key upper case", `{"GRANT":"admin","VIA":"identity","SUBJECT":"ana","BOOT":"boot-1"}`, identityAuth},
		{"an escaped key", `{"grant":"admin","\u0076ia":"identity","subject":"ana","boot":"boot-1"}`, identityAuth},
		{"an escaped value", `{"grant":"admin","via":"identit\u0079","subject":"ana","boot":"boot-1"}`, identityAuth},
		{"keys out of order", `{"boot":"boot-1","subject":"ana","via":"identity","grant":"admin"}`, identityAuth},
		{"space between", " { \"grant\" : \"admin\" ,\n\t\"via\" : \"identity\" , \"subject\" : \"ana\" , \"boot\" : \"boot-1\" } ", identityAuth},
		{"a key twice, the identity last", `{"grant":"admin","via":"workbench","Via":"identity","subject":"ana","boot":"boot-1"}`, identityAuth},
		{"a key twice, the workbench last", `{"grant":"admin","via":"identity","via":"workbench","boot":"boot-1"}`, workbenchAuth},
		{"the workbench, capital", `{"Grant":"admin","Via":"workbench","Boot":"boot-1"}`, workbenchAuth},
	}
	identities := 0
	for i, sp := range spellings {
		n := open(t, l, gateDecision(run, fmt.Sprintf("g%d", i)))
		id := fmt.Sprintf("r%d", i)
		var got Responded
		in(t, l, run, func(tx *Tx) error {
			d, _, err := tx.Decision(n)
			if err != nil {
				return err
			}
			if got, err = tx.Respond(n, ResponseRow{Actor: "admin", Authority: sp.auth, ResponseID: id, Fingerprint: "fp-" + id, Status: ResponseAccepted, Receipt: `{}`}); err != nil {
				return err
			}
			if ok, err := tx.Answer(n, run, d.Rev); err != nil || !ok {
				return fmt.Errorf("the answer of %s: %v %v", d.ID(), ok, err)
			}
			return nil
		})
		if got.Row.Authority != sp.want {
			t.Errorf("%s: answered as %s, want %s", sp.name, got.Row.Authority, sp.want)
		}
		var kept string
		if err := l.db.QueryRow("SELECT authority FROM decision_responses WHERE decision = ?", n).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept != sp.want {
			t.Errorf("%s: kept as %s, want %s", sp.name, kept, sp.want)
		}
		if sp.want == identityAuth {
			identities++
		}
		// The place count (room's) reads each kept row as the policy did.
		if c := countRows(t, l, "SELECT COUNT(*) FROM decisions d WHERE d.current_run = ? AND "+placeCond, run); c != identities {
			t.Errorf("%s: %d decisions hold a place, want %d", sp.name, c, identities)
		}
	}
	// A spelling of a field the shape does not have is still refused.
	n := open(t, l, gateDecision(run, "g-unknown"))
	if err := try(l, run, func(tx *Tx) error {
		_, err := tx.Respond(n, ResponseRow{Actor: "admin", Authority: `{"via":"identity","boot":"b","vía":"x"}`, ResponseID: "r-x", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`})
		return err
	}); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("an authority with an unknown field: %v", err)
	}
}

func TestRefusalsNeverTakeTheAnswersPlace(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "deploy")
	n := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error { return tx.Hold(run, "deploy", n) })
	stale := func(id string) ResponseRow {
		return ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: id, Fingerprint: "fp-" + id, Status: ResponseRefused, Receipt: `{"code":"stale_decision"}`}
	}
	in(t, l, run, func(tx *Tx) error {
		for i := range MaxDecisionRefusals {
			got, err := tx.Respond(n, stale(fmt.Sprintf("s%d", i+1)))
			if err != nil || !got.Kept || got.Row.K != i+1 {
				return fmt.Errorf("refusal %d: %+v %v", i+1, got, err)
			}
			if _, _, err := tx.History(n, HistoryRow{Kind: HistoryResponded, Detail: fmt.Sprintf(`{"k":%d,"status":"refused"}`, got.Row.K)}); err != nil {
				return err
			}
		}
		got, err := tx.Respond(n, stale("s32"))
		if err != nil || got.Kept || got.Repeat || got.Row.K != 0 {
			return fmt.Errorf("the 32nd refusal: %+v %v", got, err)
		}
		return nil
	})
	if rec := record(t, l, n); len(rec.Responses) != MaxDecisionRefusals || rec.Rev != 1 || rec.State != DecisionOpen {
		t.Fatalf("after the refusals: %d responses, rev %d, %s", len(rec.Responses), rec.Rev, rec.State)
	}
	// s32's retry, made against rev 2, which nobody has read: it changes
	// nothing.
	retry := stale("s32")
	retry.Status, retry.Receipt = ResponseAccepted, `{"words":"approved","rev":2}`
	if err := try(l, run, func(tx *Tx) error {
		if _, err := tx.Respond(n, retry); err != nil {
			return err
		}
		if ok, err := tx.Answer(n, run, 2); err != nil || ok {
			return fmt.Errorf("an answer at a rev nobody has read: %v %v", ok, err)
		}
		return errors.New("refused invalid")
	}); err == nil || err.Error() != "refused invalid" {
		t.Fatal(err)
	}
	// A comment brings the rev up to 2.
	in(t, l, run, func(tx *Tx) error {
		if _, _, err := tx.History(n, HistoryRow{Kind: HistoryCommented, By: "admin", Detail: `{"text":"see the logs"}`, OpID: "c1", Fingerprint: "fp-c1"}); err != nil {
			return err
		}
		d, _, _ := tx.Decision(n)
		d.Rev++
		return tx.PutDecision(d)
	})
	// The same id and body again: a new response, accepted, applied.
	in(t, l, run, func(tx *Tx) error {
		got, err := tx.Respond(n, retry)
		if err != nil || got.Repeat || !got.Kept || got.Row.K != MaxDecisionResponses || got.Row.Status != ResponseAccepted {
			return fmt.Errorf("the retry: %+v %v", got, err)
		}
		if ok, err := tx.Answer(n, run, 2); err != nil || !ok {
			return fmt.Errorf("the answer: %v %v", ok, err)
		}
		return nil
	})
	in(t, l, run, func(tx *Tx) error {
		d, _, _ := tx.Decision(n)
		if d.State != DecisionAnswered || d.Rev != 3 {
			return fmt.Errorf("answered: %+v", d)
		}
		d.State, d.Rev, d.Applied = DecisionApplied, d.Rev+1, `{"by":"yardmaster","how":"released","tasks":["deploy"]}`
		if err := tx.PutDecision(d); err != nil {
			return err
		}
		if ok, err := tx.Release(run, "deploy", n); err != nil || !ok {
			return fmt.Errorf("the release: %v %v", ok, err)
		}
		return nil
	})
	if hs, err := l.Holds(ctx, run); err != nil || len(hs) != 1 || hs[0].ReleasedAt.IsZero() {
		t.Fatalf("deploy's hold: %+v %v", hs, err)
	}
	// A kept refusal's retry stays what it was.
	in(t, l, run, func(tx *Tx) error {
		got, err := tx.Respond(n, stale("s1"))
		if err != nil || !got.Repeat || got.Row.Status != ResponseRefused || got.Row.Receipt != `{"code":"stale_decision"}` {
			return fmt.Errorf("s1 again: %+v %v", got, err)
		}
		return nil
	})
	if rec := record(t, l, n); rec.State != DecisionApplied || len(rec.Responses) != MaxDecisionResponses {
		t.Fatalf("at the end: %s, %d responses", rec.State, len(rec.Responses))
	}
}

// An operation's id is one operation of its author on one decision: sent
// again it answers its row, with another body it is a conflict; another
// decision's or another author's is its own.
func TestAnOperationIDIsUniquePerDecision(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n1 := open(t, l, gateDecision(run, "g1"))
	n2 := open(t, l, gateDecision(run, "g2"))
	op := HistoryRow{Kind: HistoryCommented, By: "admin", Detail: `{"text":"why?"}`, OpID: "o1", Fingerprint: "fp-o1"}
	var first HistoryRow
	in(t, l, run, func(tx *Tx) error {
		var err error
		first, _, err = tx.History(n1, op)
		return err
	})
	in(t, l, run, func(tx *Tx) error {
		again, repeat, err := tx.History(n1, op)
		if err != nil || !repeat || again.Seq != first.Seq || again.Detail != first.Detail {
			return fmt.Errorf("again: %+v %v %v", again, repeat, err)
		}
		other := op
		other.Fingerprint = "fp-other"
		if _, _, err := tx.History(n1, other); !errors.Is(err, ErrDecisionConflict) {
			return fmt.Errorf("another body: %v", err)
		}
		if _, repeat, err := tx.History(n2, op); err != nil || repeat {
			return fmt.Errorf("another decision's: %v %v", repeat, err)
		}
		mate := op
		mate.By = "user:ana"
		if _, repeat, err := tx.History(n1, mate); err != nil || repeat {
			return fmt.Errorf("another author's: %v %v", repeat, err)
		}
		return nil
	})
	if c := countRows(t, l, "SELECT COUNT(*) FROM decision_history"); c != 3 {
		t.Fatalf("%d history rows", c)
	}
}

// Every kind is written to its own bound and refused past it; the
// notified rows take the rest of 256 and past it are not written,
// counted as dropped in the next row written that has room for the count
// within its 2 KiB (a row of the full 2 KiB is written as sent, and the
// count goes in the next); a decision's history never passes 256.
func TestTheHistoryCapHolds(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	write := func(h HistoryRow) (HistoryRow, error) {
		var got HistoryRow
		err := try(l, run, func(tx *Tx) error {
			var err error
			got, _, err = tx.History(n, h)
			return err
		})
		return got, err
	}
	rest := MaxDecisionHistory - mandatoryHistory
	in(t, l, run, func(tx *Tx) error {
		for i := range rest {
			if h, _, err := tx.History(n, HistoryRow{Kind: HistoryNotified, Detail: fmt.Sprintf(`{"k":%d}`, i)}); err != nil || h.Seq != i+1 {
				return fmt.Errorf("notified %d: %+v %v", i, h, err)
			}
		}
		return nil
	})
	for range 2 {
		if h, err := write(HistoryRow{Kind: HistoryNotified}); err != nil || h.Seq != 0 {
			t.Fatalf("a notified row past the rest: %+v %v", h, err)
		}
	}
	if _, err := write(HistoryRow{Kind: HistoryEscalated}); !errors.Is(err, ErrDecisionLimit) {
		t.Fatalf("a provider's row past the rest: %v", err)
	}
	full := `{"by":"plan","pad":"` + strings.Repeat("x", MaxHistoryDetail-len(`{"by":"plan","pad":""}`)) + `"}`
	opened, err := write(HistoryRow{Kind: HistoryOpened, Detail: full})
	if err != nil || len(full) != MaxHistoryDetail {
		t.Fatalf("an opened row of %d bytes: %v", len(full), err)
	}
	if opened.Detail != full {
		t.Fatalf("a row of the full 2 KiB took the count: %d bytes", len(opened.Detail))
	}
	routed, err := write(HistoryRow{Kind: HistoryRouted, Detail: `{"rule":"policy"}`})
	if err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(routed.Detail), &detail); err != nil || detail["dropped"] != float64(2) || detail["rule"] != "policy" {
		t.Fatalf("the next row's detail: %s %v", routed.Detail, err)
	}
	bounds := []struct {
		kinds []string
		max   int
		op    bool
	}{
		{[]string{HistoryRouted}, 1, false}, // the second of two: the first took the count
		{[]string{HistoryCommented, HistoryInfoRequested, HistoryRouted}, MaxDecisionOperations, true},
		{[]string{HistoryResponded}, MaxDecisionResponses, false},
		{[]string{HistoryApplied}, 1, false},
		{[]string{HistorySuperseded, HistoryCancelled}, 1, false},
		{[]string{HistoryRefunded}, 1, false},
		{[]string{HistoryDeliveryTyping, HistoryDeliveryEntered, HistoryDeliveryUntyped, HistoryDeliveryUncertain, HistoryDeliveryTaskFile}, 3, false},
	}
	k := 0
	row := func(kind string, op bool) HistoryRow {
		k++
		h := HistoryRow{Kind: kind}
		if op {
			h.By, h.OpID, h.Fingerprint = "admin", fmt.Sprintf("op%d", k), "fp"
		}
		return h
	}
	for _, b := range bounds {
		for i := range b.max {
			if _, err := write(row(b.kinds[i%len(b.kinds)], b.op)); err != nil {
				t.Fatalf("%v %d: %v", b.kinds, i, err)
			}
		}
		for _, kind := range b.kinds {
			if _, err := write(row(kind, b.op)); !errors.Is(err, ErrDecisionLimit) {
				t.Fatalf("%s past its bound: %v", kind, err)
			}
		}
	}
	if _, err := write(HistoryRow{Kind: HistoryOpened}); !errors.Is(err, ErrDecisionLimit) {
		t.Fatalf("a second opened: %v", err)
	}
	if h, err := write(HistoryRow{Kind: HistoryNotified}); err != nil || h.Seq != 0 {
		t.Fatalf("a notified row at the cap: %+v %v", h, err)
	}
	rec := record(t, l, n)
	if len(rec.History) != MaxDecisionHistory {
		t.Fatalf("%d history rows", len(rec.History))
	}
	for _, h := range rec.History {
		if len(h.Detail) > MaxHistoryDetail {
			t.Fatalf("a %s row of %d bytes", h.Kind, len(h.Detail))
		}
	}
	for i, h := range rec.History {
		if h.Seq != i+1 {
			t.Fatalf("row %d has seq %d", i+1, h.Seq)
		}
	}
	if _, err := write(HistoryRow{Kind: "reopened"}); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("a kind it does not know: %v", err)
	}
}

// Answer changes the row only while the run named holds the decision, it
// is open at the rev named and has an accepted response: once a
// continuation moved it, the old run's answer changes nothing.
func TestAnAnswerIsConditionalOnItsRun(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const a, b = "team-00000001", "team-00000002"
	newRun(t, l, a)
	n := open(t, l, gateDecision(a, "g1"))
	in(t, l, a, func(tx *Tx) error {
		if ok, err := tx.Answer(n, a, 1); err != nil || ok {
			return fmt.Errorf("an answer with no accepted response: %v %v", ok, err)
		}
		_, err := tx.Respond(n, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r1", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`})
		return err
	})
	finish(t, l, a)
	continuation(t, l, a, b)
	in(t, l, b, func(tx *Tx) error {
		_, err := tx.MoveDecisions(a, b)
		return err
	})
	in(t, l, a, func(tx *Tx) error {
		if ok, err := tx.Answer(n, a, 1); err != nil || ok {
			return fmt.Errorf("the old run's answer: %v %v", ok, err)
		}
		return nil
	})
	in(t, l, b, func(tx *Tx) error {
		if ok, err := tx.Answer(n, b, 2); err != nil || ok {
			return fmt.Errorf("an answer at another rev: %v %v", ok, err)
		}
		if ok, err := tx.Answer(n, b, 1); err != nil || !ok {
			return fmt.Errorf("the continuation's answer: %v %v", ok, err)
		}
		if ok, err := tx.Answer(n, b, 2); err != nil || ok {
			return fmt.Errorf("a second answer: %v %v", ok, err)
		}
		return nil
	})
	if rec := record(t, l, n); rec.State != DecisionAnswered || rec.Rev != 2 || rec.CurrentRun != b || rec.AnsweredAt.IsZero() {
		t.Fatalf("answered: %+v", rec.DecisionRow)
	}
}

// MoveDecisions moves every open or answered decision of the run
// continued, answers them as moved, their revs unchanged, and leaves the
// final ones where they are.
func TestMoveDecisionsAnswersTheMovedRows(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const a, b = "team-00000001", "team-00000002"
	newRun(t, l, a)
	open1 := open(t, l, gateDecision(a, "g1"))
	answered := open(t, l, gateDecision(a, "g2"))
	answer(t, l, a, answered, "r1", workbenchAuth)
	applied := open(t, l, gateDecision(a, "g3"))
	answer(t, l, a, applied, "r1", workbenchAuth)
	setState(t, l, a, applied, DecisionApplied, `{"how":"noted"}`)
	cancelled := open(t, l, gateDecision(a, "g4"))
	setState(t, l, a, cancelled, DecisionCancelled, "")
	finish(t, l, a)
	continuation(t, l, a, b)
	in(t, l, b, func(tx *Tx) error {
		moved, err := tx.MoveDecisions(a, b)
		if err != nil {
			return err
		}
		if len(moved) != 2 || moved[0].N != open1 || moved[1].N != answered {
			return fmt.Errorf("moved: %+v", moved)
		}
		for _, d := range moved {
			if d.CurrentRun != b || d.RunID != a {
				return fmt.Errorf("a moved row: %+v", d)
			}
		}
		if moved[0].Rev != 1 || moved[1].Rev != 2 || moved[1].State != DecisionAnswered {
			return fmt.Errorf("the moved revs: %+v", moved)
		}
		again, err := tx.MoveDecisions(a, b)
		if err == nil && len(again) != 0 {
			err = fmt.Errorf("moved twice: %+v", again)
		}
		return err
	})
	for n, want := range map[int64]string{open1: b, answered: b, applied: a, cancelled: a} {
		if rec := record(t, l, n); rec.CurrentRun != want {
			t.Fatalf("%s is held by %s, not %s", rec.ID(), rec.CurrentRun, want)
		}
	}
	if ds, err := l.Decisions(ctx, DecisionFilter{CurrentRun: b}); err != nil || len(ds) != 2 || ds[0].N != answered {
		t.Fatalf("the continuation's, newest first: %+v %v", ds, err)
	}
}

// A released hold keeps its row; only the run that holds the decision
// holds or releases, and a final decision holds nothing.
func TestAReleasedHoldKeepsItsRow(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const run, other = "team-00000001", "team-00000002"
	newRun(t, l, run)
	newRun(t, l, other)
	tasksOf(t, l, run, "deploy")
	tasksOf(t, l, other, "deploy")
	n := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error {
		if err := tx.Hold(run, "deploy", n); err != nil {
			return err
		}
		if err := tx.Hold(other, "deploy", n); !errors.Is(err, ErrNotCurrent) {
			return fmt.Errorf("a hold under another run: %v", err)
		}
		return nil
	})
	if hs, _ := l.Holds(ctx, run); len(hs) != 1 || !hs[0].ReleasedAt.IsZero() || hs[0].Decision != n || hs[0].TaskID != "deploy" {
		t.Fatalf("the hold: %+v", hs)
	}
	in(t, l, run, func(tx *Tx) error {
		if ok, err := tx.Release(run, "deploy", n); err != nil || !ok {
			return fmt.Errorf("the release: %v %v", ok, err)
		}
		if ok, err := tx.Release(run, "deploy", n); err != nil || ok {
			return fmt.Errorf("a second release: %v %v", ok, err)
		}
		if _, err := tx.Release(other, "deploy", n); !errors.Is(err, ErrNotCurrent) {
			return fmt.Errorf("a release under another run: %v", err)
		}
		return nil
	})
	if hs, _ := l.Holds(ctx, run); len(hs) != 1 || hs[0].ReleasedAt.IsZero() {
		t.Fatalf("the released hold: %+v", hs)
	}
	setState(t, l, run, n, DecisionCancelled, "")
	if err := try(l, run, func(tx *Tx) error { return tx.Hold(run, "deploy", n) }); !errors.Is(err, ErrDecisionState) {
		t.Fatalf("a final decision's hold: %v", err)
	}
	// A final decision moves on only from applied to superseded.
	if err := try(l, run, func(tx *Tx) error {
		d, _, _ := tx.Decision(n)
		d.State = DecisionOpen
		return tx.PutDecision(d)
	}); !errors.Is(err, ErrDecisionState) {
		t.Fatalf("a cancelled decision reopened: %v", err)
	}
}

// MoveHolds writes the run continued's holds under the continuation as
// they are and releases the active ones of the run continued, at the
// move's time: one active row, under the decision's current run.
func TestMoveHoldsLeavesOneActiveRow(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const a, b = "team-00000001", "team-00000002"
	newRun(t, l, a)
	tasksOf(t, l, a, "build", "deploy")
	n := open(t, l, gateDecision(a, "g1"))
	in(t, l, a, func(tx *Tx) error {
		if err := tx.Hold(a, "build", n); err != nil {
			return err
		}
		if err := tx.Hold(a, "deploy", n); err != nil {
			return err
		}
		_, err := tx.Release(a, "deploy", n)
		return err
	})
	released := map[string]time.Time{}
	hs, _ := l.Holds(ctx, a)
	for _, h := range hs {
		released[h.TaskID] = h.ReleasedAt
	}
	finish(t, l, a)
	continuation(t, l, a, b, "build", "deploy")
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := try(l, b, func(tx *Tx) error { _, err := tx.MoveHolds(a, b, at); return err }); !errors.Is(err, ErrNotCurrent) {
		t.Fatalf("holds moved before their decision: %v", err)
	}
	in(t, l, b, func(tx *Tx) error {
		if _, err := tx.MoveDecisions(a, b); err != nil {
			return err
		}
		moved, err := tx.MoveHolds(a, b, at)
		if err == nil && moved != 1 {
			err = fmt.Errorf("%d active holds moved", moved)
		}
		return err
	})
	ha, _ := l.Holds(ctx, a)
	hb, _ := l.Holds(ctx, b)
	if len(ha) != 2 || !ha[0].ReleasedAt.Equal(at) || !ha[1].ReleasedAt.Equal(released["deploy"]) {
		t.Fatalf("under the run continued: %+v", ha)
	}
	if len(hb) != 2 || !hb[0].ReleasedAt.IsZero() || !hb[1].ReleasedAt.Equal(released["deploy"]) {
		t.Fatalf("under the continuation: %+v", hb)
	}
	if c := countRows(t, l, "SELECT COUNT(*) FROM decision_holds WHERE decision = ? AND released_at = ''", n); c != 1 {
		t.Fatalf("%d active holds", c)
	}
	in(t, l, a, func(tx *Tx) error {
		if err := tx.Hold(a, "build", n); !errors.Is(err, ErrNotCurrent) {
			return fmt.Errorf("a hold under the run continued: %v", err)
		}
		if _, err := tx.Release(a, "build", n); !errors.Is(err, ErrNotCurrent) {
			return fmt.Errorf("a release under the run continued: %v", err)
		}
		return nil
	})
}

// MoveHolds copies only the holds of the decisions MoveDecisions moved: a
// final decision stays with its run, and so do its released holds, through
// a chain of continuations (a, b, c), while a moved decision's holds go
// with it at each step.
func TestMoveHoldsLeavesAFinalDecisionsHoldsBehind(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const a, b, c = "team-00000001", "team-00000002", "team-00000003"
	newRun(t, l, a)
	tasksOf(t, l, a, "build", "deploy")
	cancelled := open(t, l, gateDecision(a, "g1"))
	kept := open(t, l, gateDecision(a, "g2"))
	in(t, l, a, func(tx *Tx) error {
		if err := tx.Hold(a, "build", cancelled); err != nil {
			return err
		}
		if _, err := tx.Release(a, "build", cancelled); err != nil {
			return err
		}
		return tx.Hold(a, "deploy", kept)
	})
	setState(t, l, a, cancelled, DecisionCancelled, "")
	before, _ := l.Holds(ctx, a)
	move := func(from, to string) {
		t.Helper()
		in(t, l, to, func(tx *Tx) error {
			ds, err := tx.MoveDecisions(from, to)
			if err == nil && (len(ds) != 1 || ds[0].N != kept) {
				err = fmt.Errorf("moved %+v", ds)
			}
			if err != nil {
				return err
			}
			moved, err := tx.MoveHolds(from, to, tx.Now())
			if err == nil && moved != 1 {
				err = fmt.Errorf("%d active holds moved", moved)
			}
			return err
		})
	}
	finish(t, l, a)
	continuation(t, l, a, b, "build", "deploy")
	move(a, b)
	finish(t, l, b)
	continuation(t, l, b, c, "build", "deploy")
	move(b, c)
	ha, _ := l.Holds(ctx, a)
	if len(ha) != 2 || ha[0].Decision != cancelled || !ha[0].ReleasedAt.Equal(before[0].ReleasedAt) || ha[1].Decision != kept || ha[1].ReleasedAt.IsZero() {
		t.Fatalf("under the run that holds the cancelled decision: %+v", ha)
	}
	for _, run := range []string{b, c} {
		hs, _ := l.Holds(ctx, run)
		if len(hs) != 1 || hs[0].Decision != kept || hs[0].TaskID != "deploy" || hs[0].ReleasedAt.IsZero() != (run == c) {
			t.Fatalf("under %s: %+v", run, hs)
		}
	}
	if n := countRows(t, l, "SELECT COUNT(*) FROM decision_holds WHERE decision = ?", cancelled); n != 1 {
		t.Fatalf("%d hold rows of the cancelled decision", n)
	}
	if rec := record(t, l, cancelled); rec.CurrentRun != a {
		t.Fatalf("the cancelled decision is held by %s", rec.CurrentRun)
	}
}

// An attempt is given back once, whatever decision it waited on.
func TestARefundIsOncePerAttempt(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	d1 := open(t, l, agentDecision(run, "build", 1, "q1"))
	d2 := open(t, l, agentDecision(run, "build", 1, "q2"))
	in(t, l, run, func(tx *Tx) error {
		if ok, err := tx.Refund(run, "build", 1, d1); err != nil || !ok {
			return fmt.Errorf("the refund: %v %v", ok, err)
		}
		for _, d := range []int64{d1, d2} {
			if ok, err := tx.Refund(run, "build", 1, d); err != nil || ok {
				return fmt.Errorf("a second refund for %s: %v %v", DecisionID(d), ok, err)
			}
		}
		return nil
	})
	if c := countRows(t, l, "SELECT COUNT(*) FROM decision_refunds WHERE run_id = ? AND task_id = 'build' AND n = 1 AND decision = ?", run, d1); c != 1 {
		t.Fatalf("%d refund rows", c)
	}
	if c := countRows(t, l, "SELECT COUNT(*) FROM decision_refunds"); c != 1 {
		t.Fatalf("%d refund rows in all", c)
	}
}

// The recovery changes no decision: open stays open, answered answered,
// their holds as they were.
func TestRecoverChangesNoDecision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "deploy")
	asked := open(t, l, agentDecision(run, "build", 1, "q1"))
	gate := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error {
		if err := tx.Hold(run, "build", asked); err != nil {
			return err
		}
		return tx.Hold(run, "deploy", gate)
	})
	answer(t, l, run, gate, "r1", workbenchAuth)
	before := []DecisionRecord{record(t, l, asked), record(t, l, gate)}
	holds, _ := l.Holds(ctx, run)
	l.Close()
	again := openLedger(t, dir)
	recovered, err := again.Recover(ctx)
	if err != nil || len(recovered) != 1 || !recovered[0].Interrupted {
		t.Fatalf("recovered: %+v %v", recovered, err)
	}
	for i, n := range []int64{asked, gate} {
		if got := record(t, again, n); !reflect.DeepEqual(got, before[i]) {
			t.Fatalf("%s after the recovery:\n%+v\nbefore:\n%+v", DecisionID(n), got, before[i])
		}
	}
	if got, _ := again.Holds(ctx, run); !reflect.DeepEqual(got, holds) {
		t.Fatalf("holds after the recovery: %+v, before %+v", got, holds)
	}
}

// A snapshot carries the decision tables as they are, from the open
// ledger and from the file with no server running.
func TestSnapshotCarriesDecisions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	in(t, l, run, func(tx *Tx) error {
		if err := tx.Hold(run, "build", n); err != nil {
			return err
		}
		if _, err := tx.Refund(run, "build", 1, n); err != nil {
			return err
		}
		_, _, err := tx.History(n, HistoryRow{Kind: HistoryOpened, Detail: `{"by":"lead"}`})
		return err
	})
	answer(t, l, run, n, "r1", workbenchAuth)
	want := record(t, l, n)
	wantHolds, _ := l.Holds(ctx, run)
	wantRefunds := tableRows(t, l, "decision_refunds")
	check := func(t *testing.T, copyDir string) {
		t.Helper()
		c := openLedger(t, copyDir)
		if got := record(t, c, n); !reflect.DeepEqual(got, want) {
			t.Fatalf("the copy's decision:\n%+v\nwant:\n%+v", got, want)
		}
		if got, _ := c.Holds(ctx, run); !reflect.DeepEqual(got, wantHolds) {
			t.Fatalf("the copy's holds: %+v", got)
		}
		if got := tableRows(t, c, "decision_refunds"); !slices.Equal(got, wantRefunds) {
			t.Fatalf("the copy's refunds: %v", got)
		}
	}
	// The snapshot's directory is the caller's to make.
	live, cold := filepath.Join(t.TempDir(), "runs"), filepath.Join(t.TempDir(), "runs")
	for _, d := range []string{live, cold} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Snapshot(ctx, filepath.Join(live, File)); err != nil {
		t.Fatal(err)
	}
	check(t, live)
	l.Close()
	if err := SnapshotFile(ctx, filepath.Join(dir, File), filepath.Join(cold, File)); err != nil {
		t.Fatal(err)
	}
	check(t, cold)
}

// A reader of a database before version 8, opened read-only and not
// migrated, finds no decision rather than an error.
func TestAnUnmigratedLedgerHasNoDecisions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	for _, stmt := range append(slices.Clone(version8Added), "UPDATE schema_version SET version = 7") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	l.mu.Lock()
	l.version, l.readOnly = 7, true
	l.mu.Unlock()
	ctx := context.Background()
	if _, ok, err := l.Decision(ctx, 1); ok || err != nil {
		t.Fatalf("a decision: %v %v", ok, err)
	}
	if ds, err := l.Decisions(ctx, DecisionFilter{}); len(ds) != 0 || err != nil {
		t.Fatalf("decisions: %+v %v", ds, err)
	}
	if hs, err := l.Holds(ctx, "team-00000001"); len(hs) != 0 || err != nil {
		t.Fatalf("holds: %+v %v", hs, err)
	}
}

// A reader that attached to a version 7 database, read-only and never
// migrating, sees the decisions another server, the owner, writes once
// it has migrated the database: the readers read the version with their
// rows, not the one kept at attach.
func TestADecisionReaderSeesAnotherOwnersMigration(t *testing.T) {
	seeded := filepath.Join(t.TempDir(), "seed")
	l := openLedger(t, seeded)
	newRun(t, l, "team-00000001")
	l.Close()
	dir := filepath.Join(t.TempDir(), "runs")
	version7Like(t, seeded, dir)

	ctx := context.Background()
	reader := openReadOnly(t, dir)
	if v := dbVersion(t, reader); v != 7 {
		t.Fatalf("version %d before the owner's migration", v)
	}
	if _, ok, err := reader.Decision(ctx, 1); ok || err != nil {
		t.Fatalf("a decision before the migration: %v %v", ok, err)
	}

	owner := openLedger(t, dir)
	if ro, why := owner.ReadOnly(); ro {
		t.Fatalf("the owner opened read-only: %s", why)
	}
	const run = "team-00000001"
	tasksOf(t, owner, run, "build")
	n := open(t, owner, agentDecision(run, "build", 1, "q1"))
	in(t, owner, run, func(tx *Tx) error { return tx.Hold(run, "build", n) })

	if rec, ok, err := reader.Decision(ctx, n); !ok || err != nil || rec.State != DecisionOpen {
		t.Fatalf("the reader's decision after the owner's migration: %+v %v %v", rec, ok, err)
	}
	if ds, err := reader.Decisions(ctx, DecisionFilter{}); len(ds) != 1 || err != nil {
		t.Fatalf("the reader's decisions: %+v %v", ds, err)
	}
	if hs, err := reader.Holds(ctx, run); len(hs) != 1 || err != nil {
		t.Fatalf("the reader's holds: %+v %v", hs, err)
	}
}

// Decision reads the row, its responses and its history of one commit: a
// transition that answers the decision, committed by another server
// between the reader's row and its responses, is not seen in part (an
// open row beside an accepted response), and the next read sees it whole.
func TestADecisionIsReadInOneSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	owner := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, owner, run)
	n := open(t, owner, gateDecision(run, "g1"))
	reader := openReadOnly(t, dir)

	ctx := context.Background()
	ran := false
	reader.inDecision = func() {
		ran = true
		answer(t, owner, run, n, "yes", workbenchAuth)
		in(t, owner, run, func(tx *Tx) error {
			_, _, err := tx.History(n, HistoryRow{Kind: HistoryResponded, Detail: `{"k":1,"status":"accepted"}`})
			return err
		})
	}
	rec, ok, err := reader.Decision(ctx, n)
	reader.inDecision = nil
	if !ran || !ok || err != nil {
		t.Fatalf("the read: ran %v, %v %v", ran, ok, err)
	}
	if rec.State != DecisionOpen || rec.Rev != 1 || len(rec.Responses) != 0 || len(rec.History) != 0 {
		t.Fatalf("a torn record: %s at rev %d, %d responses, %d history rows", rec.State, rec.Rev, len(rec.Responses), len(rec.History))
	}
	rec = record(t, reader, n)
	if rec.State != DecisionAnswered || rec.Rev != 2 || len(rec.Responses) != 1 || len(rec.History) != 1 {
		t.Fatalf("the record after: %s at rev %d, %d responses, %d history rows", rec.State, rec.Rev, len(rec.Responses), len(rec.History))
	}
}

// A cancellation keeps its command id and fingerprint as an operation
// does: sent again it answers its row, with another body it is a
// conflict. It is no operation of the 32: it has the final row's place,
// after 32 operations too, and a second final row is refused.
func TestACancellationKeepsItsCommandID(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	n := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error {
		for i := range MaxDecisionOperations {
			if _, _, err := tx.History(n, HistoryRow{Kind: HistoryCommented, By: "admin", OpID: fmt.Sprintf("op%d", i), Fingerprint: "fp"}); err != nil {
				return err
			}
		}
		return nil
	})
	cancel := HistoryRow{Kind: HistoryCancelled, By: "admin", Detail: `{"why":"not needed","rev":1}`, OpID: "c1", Fingerprint: "fp-c1"}
	var first HistoryRow
	in(t, l, run, func(tx *Tx) error {
		var err error
		first, _, err = tx.History(n, cancel)
		return err
	})
	if first.Seq != MaxDecisionOperations+1 || first.OpID != "c1" {
		t.Fatalf("the cancellation: %+v", first)
	}
	in(t, l, run, func(tx *Tx) error {
		again, repeat, err := tx.History(n, cancel)
		if err != nil || !repeat || again.Seq != first.Seq {
			return fmt.Errorf("again: %+v %v %v", again, repeat, err)
		}
		other := cancel
		other.Fingerprint = "fp-other"
		if _, _, err := tx.History(n, other); !errors.Is(err, ErrDecisionConflict) {
			return fmt.Errorf("another body: %v", err)
		}
		second := cancel
		second.OpID = "c2"
		if _, _, err := tx.History(n, second); !errors.Is(err, ErrDecisionLimit) {
			return fmt.Errorf("a second final row: %v", err)
		}
		return nil
	})
	if rec := record(t, l, n); rec.History[len(rec.History)-1].OpID != "c1" || rec.History[len(rec.History)-1].Fingerprint != "fp-c1" {
		t.Fatalf("the kept row: %+v", rec.History[len(rec.History)-1])
	}
}

// A version 8 database is brought to version 9 as it opens: its request
// index is keyed by the task too, every row it held is kept as it was,
// and the member's request id, taken for one task, is then free for
// another.
func TestMigrationEightToNineKeysARequestByItsTask(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build", "docs")
	n := open(t, l, agentDecision(run, "build", 1, "q1"))
	before := tableRows(t, l, "decisions")
	l.Close()
	rawExec(t, dir, "DROP INDEX decisions_request",
		"CREATE UNIQUE INDEX decisions_request ON decisions(run_id, member, request_id) WHERE request_id != ''",
		"UPDATE schema_version SET version = 8")

	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	if v := dbVersion(t, again); v != 9 || Version != 9 {
		t.Fatalf("version %d after the migration, Version %d", v, Version)
	}
	var sqlText string
	if err := again.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'decisions_request'").Scan(&sqlText); err != nil || !strings.Contains(sqlText, "(run_id, member, task_id, request_id)") {
		t.Fatalf("the request index after the migration: %q %v", sqlText, err)
	}
	if got := tableRows(t, again, "decisions"); !slices.Equal(got, before) {
		t.Fatalf("decisions after the migration:\n%v\nbefore:\n%v", got, before)
	}
	docs := open(t, again, agentDecision(run, "docs", 1, "q1"))
	if docs == n {
		t.Fatalf("docs' q1 is build's %s", DecisionID(n))
	}
}
