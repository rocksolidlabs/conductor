package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// RestorePeople writes a launch row's crew with the people an older build
// left out (docs/plans/decisions.md, HM1): once, while the row's crew
// lists none; a second call, and a call on a row that lists people, change
// nothing, the revision included.
func TestRestorePeopleWritesOnce(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const without = `{"id":"api","name":"API","members":[],"tasks":[{"id":"build","member":"lead","kind":"work"}]}`
	const with = `{"id":"api","name":"API","members":[],"tasks":[{"id":"build","member":"lead","kind":"work","owner":"nate"}],"people":[{"name":"nate","responsibilities":["architecture"]}]}`
	for _, id := range []string{"api-00000001", "api-00000002"} {
		crew := without
		if id == "api-00000002" {
			crew = with
		}
		if _, err := l.Transition(ctx, id, func(tx *Tx) error {
			return tx.CreateRun(RunRow{ID: id, CrewID: "api", Crew: crew, State: RunFinished, CreatedAt: tx.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	restore := func(id string) error {
		_, err := l.Transition(ctx, id, func(tx *Tx) error { return tx.RestorePeople(with) })
		return err
	}
	read := func(id string) RunRow {
		t.Helper()
		row, ok, err := l.Run(ctx, id)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", id, ok, err)
		}
		return row
	}
	if err := restore("api-00000001"); err != nil {
		t.Fatal(err)
	}
	first := read("api-00000001")
	if first.Crew != with || first.Revision != 2 {
		t.Fatalf("restored: %s at %d", first.Crew, first.Revision)
	}
	// Again: refused, the row and its revision as they were.
	if err := restore("api-00000001"); !errors.Is(err, ErrPeopleListed) {
		t.Fatalf("a second restore: %v", err)
	}
	if again := read("api-00000001"); again.Crew != with || again.Revision != first.Revision {
		t.Fatalf("after a second restore: %s at %d", again.Crew, again.Revision)
	}
	// A row whose crew lists people is never written over.
	other := `{"id":"api","name":"API","members":[],"people":[{"name":"ana"}]}`
	if _, err := l.Transition(ctx, "api-00000002", func(tx *Tx) error { return tx.RestorePeople(other) }); !errors.Is(err, ErrPeopleListed) {
		t.Fatalf("a row with people: %v", err)
	}
	if row := read("api-00000002"); row.Crew != with || row.Revision != 1 {
		t.Fatalf("a row with people after a restore: %s at %d", row.Crew, row.Revision)
	}
	// No such run.
	if _, err := l.Transition(ctx, "api-00000009", func(tx *Tx) error { return tx.RestorePeople(with) }); !errors.Is(err, ErrNoRun) {
		t.Fatalf("no run: %v", err)
	}
}
