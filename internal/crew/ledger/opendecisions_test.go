package ledger

import (
	"context"
	"path/filepath"
	"slices"
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
	nOf := func(rows []DecisionRow) []int64 {
		out := []int64{}
		for _, d := range rows {
			out = append(out, d.N)
		}
		return out
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
	// The bytes' bound: the row whose text would pass it is not handed.
	old := openScanBytes
	t.Cleanup(func() { openScanBytes = old })
	openScanBytes = decisionText(got[0]) + decisionText(got[1])
	if got, more := read(0, 0); !slices.Equal(nOf(got), open[:2]) || !more {
		t.Fatalf("at the bytes' bound: %v more %v", nOf(got), more)
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
