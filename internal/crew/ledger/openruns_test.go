package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// openWaiting opens a second ledger on dir, whose owner lock another
// holds, waiting as w says; closed at the test's end.
func openWaiting(t *testing.T, dir string, w waitSeams) *Ledger {
	t.Helper()
	l, err := openWith(dir, w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// putRunAt writes a run's row created at the time given.
func putRunAt(t *testing.T, l *Ledger, r RunRow, continues string) {
	t.Helper()
	if _, err := l.Transition(context.Background(), r.ID, func(tx *Tx) error {
		if err := tx.PutRun(r); err != nil {
			return err
		}
		if continues != "" {
			return tx.Continued(continues)
		}
		return nil
	}); err != nil {
		t.Fatalf("%s: %v", r.ID, err)
	}
}

func openIDs(rows []OpenRun) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// OpenRuns lists the open rows alone (running, or interrupted and not
// continued), newest first, at most n and never past MaxOpenRuns, with the
// count of the rest, on a writable ledger and on one that reads the same
// database read-only; a ledger with nothing to read says so.
func TestOpenRunsBounded(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	at := time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC)
	row := func(id, state string, i int) RunRow {
		return RunRow{ID: id, CrewID: "team", Crew: `{"id":"team","name":"Team"}`, Base: "abc123", State: state, CreatedAt: at.Add(time.Duration(i) * time.Minute)}
	}
	running := row("team-00000001", RunRunning, 1)
	running.Paused = true
	finished := row("team-00000002", RunFinished, 2)
	finished.ClosedAt = finished.CreatedAt
	stopped := row("team-00000003", RunStopped, 3)
	stopped.ClosedAt = stopped.CreatedAt
	interrupted := row("team-00000004", RunInterrupted, 4)
	interrupted.Crew = `{"id":"team"` // a crew that cannot be read: no name
	continued := row("team-00000005", RunInterrupted, 5)
	continuation := row("team-00000006", RunRunning, 6)
	continuation.ContinuedFrom = continued.ID
	for _, r := range []RunRow{running, finished, stopped, interrupted, continued} {
		putRunAt(t, l, r, "")
	}
	putRunAt(t, l, continuation, continued.ID)

	want := []OpenRun{
		{ID: "team-00000006", CrewID: "team", CrewName: "Team", State: RunRunning},
		{ID: "team-00000004", CrewID: "team", CrewName: "", State: RunInterrupted},
		{ID: "team-00000001", CrewID: "team", CrewName: "Team", State: RunRunning, Paused: true},
	}
	check := func(t *testing.T, l *Ledger) {
		t.Helper()
		rows, rest, err := l.OpenRuns(ctx, 10)
		if err != nil || rest != 0 || !slices.Equal(rows, want) {
			t.Fatalf("open runs: %+v rest %d %v, want %+v", rows, rest, err, want)
		}
		rows, rest, err = l.OpenRuns(ctx, 2)
		if err != nil || rest != 1 || !slices.Equal(rows, want[:2]) {
			t.Fatalf("at most 2: %v rest %d %v", openIDs(rows), rest, err)
		}
		for _, n := range []int{0, -1} {
			rows, rest, err = l.OpenRuns(ctx, n)
			if err != nil || rest != 3 || len(rows) != 0 {
				t.Fatalf("at most %d: %v rest %d %v", n, openIDs(rows), rest, err)
			}
		}
	}
	t.Run("writable", func(t *testing.T) { check(t, l) })
	t.Run("read-only", func(t *testing.T) {
		r := openWaiting(t, dir, waitSeams{lockWait: time.Hour, ticks: make(chan time.Time)})
		if ro, why := r.ReadOnly(); !ro {
			t.Fatalf("the second ledger writes: %s", why)
		}
		check(t, r)
	})
	t.Run("never past the cap", func(t *testing.T) {
		const more = MaxOpenRuns + 7
		if _, err := l.db.Exec(fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < %d)
			INSERT INTO runs (id, crew_id, crew, base, state, created_at) SELECT printf('bulk-%%08d', i), 'bulk', '{}', 'abc123', 'running', printf('2027-01-01T00:00:%%02dZ', i %% 60) FROM n`, more)); err != nil {
			t.Fatal(err)
		}
		rows, rest, err := l.OpenRuns(ctx, MaxOpenRuns*4)
		if err != nil || len(rows) != MaxOpenRuns || rest != more+3-MaxOpenRuns {
			t.Fatalf("asked for %d: %d rows, rest %d, %v", MaxOpenRuns*4, len(rows), rest, err)
		}
		if rows[0].CrewID != "bulk" || slices.Contains(openIDs(rows), "team-00000006") {
			t.Fatalf("not newest first: %v", openIDs(rows)[:3])
		}
	})
	t.Run("nothing to read", func(t *testing.T) {
		old := takeLock
		takeLock = func(path string) (*ownerLock, string, bool, error) {
			return nil, "the filesystem of " + path + " does not support locks (test)", false, nil
		}
		t.Cleanup(func() { takeLock = old })
		empty := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		if _, _, err := empty.OpenRuns(ctx, 10); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a ledger with nothing to read: %v", err)
		}
	})
}
