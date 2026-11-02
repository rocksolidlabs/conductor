package ledger

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// CheckChanged reads the paths from the finished event of the receipt it
// is asked of (DS2's PR-5): a task's check's check.finished naming its k,
// a verify step's step.finished naming its id and k, and, for an event
// written before check.finished named k, the latest of that task, attempt
// and check naming none; another attempt's, another check's, a started
// event and one of another k are passed over, and a receipt with no
// finished event changed nothing.
func TestCheckChangedReadsTheReceiptsFinishedEvent(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const run = "team-00000001"
	newRun(t, l, run)
	events := []struct {
		kind    string
		payload map[string]any
	}{
		{"check.started", map[string]any{"task": "build", "attempt": 1, "check": "unit", "k": 1, "changed": []string{"started"}}},
		{"check.finished", map[string]any{"task": "build", "attempt": 1, "check": "unit", "k": 1, "changed": []string{"README", "go.mod"}}},
		{"check.finished", map[string]any{"task": "build", "attempt": 1, "check": "unit", "k": 2, "changed": []string{"k2"}}},
		{"check.finished", map[string]any{"task": "build", "attempt": 2, "check": "unit", "k": 1, "changed": []string{"attempt 2"}}},
		{"check.finished", map[string]any{"task": "build", "attempt": 1, "check": "lint", "k": 1, "changed": []string{"lint"}}},
		{"step.finished", map[string]any{"phase": PhaseVerify, "id": "e2e", "k": 3, "task": "build", "attempt": 1, "tree": TreeCheckout, "changed": []string{"web/app.vue"}}},
		{"step.finished", map[string]any{"phase": PhaseSetup, "id": "e2e", "k": 1, "task": "build", "attempt": 1, "tree": TreeWorktree, "changed": []string{"setup"}}},
		// Written before k: the latest of the task, attempt and check.
		{"check.finished", map[string]any{"task": "test", "attempt": 1, "check": "unit", "changed": []string{"older"}}},
		{"check.finished", map[string]any{"task": "test", "attempt": 1, "check": "unit", "changed": []string{"old"}}},
	}
	if _, err := l.Transition(ctx, run, func(tx *Tx) error {
		for _, ev := range events {
			if err := tx.Event(ev.kind, ev.payload); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		task string
		n    int
		id   string
		k    int
		want []string
	}{
		{"build", 1, "unit", 1, []string{"README", "go.mod"}},
		{"build", 1, "unit", 2, []string{"k2"}},
		{"build", 2, "unit", 1, []string{"attempt 2"}},
		{"build", 1, "lint", 1, []string{"lint"}},
		{"build", 1, "e2e", 3, []string{"web/app.vue"}},
		{"build", 1, "e2e", 1, []string{"setup"}},
		{"test", 1, "unit", 1, []string{"old"}},
		{"build", 1, "unit", 9, nil},
		{"build", 3, "unit", 1, nil},
	} {
		got, err := l.CheckChanged(ctx, run, tc.task, tc.n, tc.id, tc.k)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%s %d %s k%d: %q %v, want %q", tc.task, tc.n, tc.id, tc.k, got, err, tc.want)
		}
	}
	if got, err := l.CheckChanged(ctx, "team-00000002", "build", 1, "unit", 1); err != nil || got != nil {
		t.Fatalf("a run the ledger has not: %q %v", got, err)
	}
}
