package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// ChecksOn reads one task's receipts of one check on one commit across the
// runs named (a chain), newest first, without their logs: never another
// task's, phase's, id's or commit's, nor a run's left out; at most
// MaxChecksOn of them, and no more than MaxChecksOnRuns runs.
func TestChecksOnReadsTheChainsRows(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	put := func(runID string, rows ...CheckRow) {
		t.Helper()
		if _, err := l.Transition(ctx, runID, func(tx *Tx) error {
			for _, r := range rows {
				r.RunID, r.StartedAt, r.Where, r.State = runID, tx.Now(), WhereNative, CheckFinished
				if r.Phase == "" {
					r.Phase = PhaseCheck
				}
				if err := tx.PutCheck(r); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"team-0000000a", "team-0000000b", "team-0000000c"} {
		newRun(t, l, id)
	}
	attemptOf(t, l, "team-0000000a", "build", 1, AttemptFailed)
	attemptOf(t, l, "team-0000000a", "build", 2, AttemptFailed)
	attemptOf(t, l, "team-0000000a", "docs", 1, AttemptFailed)
	attemptOf(t, l, "team-0000000b", "build", 3, AttemptFailed)
	attemptOf(t, l, "team-0000000c", "build", 1, AttemptFailed)
	argv := []string{"go", "test", "./..."}
	put("team-0000000a",
		CheckRow{TaskID: "build", N: 1, ID: "test", K: 1, Argv: argv, Subject: "c1", Exit: 1, Log: []byte("FAIL")},
		CheckRow{TaskID: "build", N: 2, ID: "test", K: 1, Argv: argv, Subject: "c2", Exit: 1},
		CheckRow{TaskID: "build", N: 2, ID: "test", K: 2, Argv: argv, Subject: "c2", Exit: 0},
		// Another phase, another id, another task: none of them is the check.
		CheckRow{TaskID: "build", N: 2, ID: "lint", K: 1, Subject: "c2"},
		CheckRow{TaskID: "build", N: 2, ID: "test", K: 3, Phase: PhaseVerify, Manifest: "crew:1", Subject: "c2"},
		CheckRow{TaskID: "docs", N: 1, ID: "test", K: 1, Subject: "c2"},
	)
	put("team-0000000b", CheckRow{TaskID: "build", N: 3, ID: "test", K: 1, Argv: argv, Subject: "c2", Exit: 1, Log: []byte("FAIL again")})
	// A run outside the chain.
	put("team-0000000c", CheckRow{TaskID: "build", N: 1, ID: "test", K: 1, Argv: argv, Subject: "c2"})
	chain := []string{"team-0000000a", "team-0000000b"}
	rows, err := l.ChecksOn(ctx, chain, "build", PhaseCheck, "test", "c2")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s/%s/%d/%s/%s/%d exit %d", r.RunID, r.TaskID, r.N, r.Phase, r.ID, r.K, r.Exit))
		if r.Log != nil {
			t.Fatalf("a row came with its log: %+v", r)
		}
		if r.Subject != "c2" || r.Argv[0] != "go" {
			t.Fatalf("a row: %+v", r)
		}
	}
	want := []string{"team-0000000b/build/3/check/test/1 exit 1", "team-0000000a/build/2/check/test/2 exit 0", "team-0000000a/build/2/check/test/1 exit 1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ChecksOn:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rows[0].LogBytes != int64(len("FAIL again")) {
		t.Fatalf("the newest row's log size: %d", rows[0].LogBytes)
	}
	// A verify step is read by its own phase.
	if rows, err := l.ChecksOn(ctx, chain, "build", PhaseVerify, "test", "c2"); err != nil || len(rows) != 1 || rows[0].Manifest != "crew:1" {
		t.Fatalf("the verify step: %+v %v", rows, err)
	}
	// Another commit, and no run.
	if rows, err := l.ChecksOn(ctx, chain, "build", PhaseCheck, "test", "c1"); err != nil || len(rows) != 1 || rows[0].N != 1 {
		t.Fatalf("on c1: %+v %v", rows, err)
	}
	if rows, err := l.ChecksOn(ctx, nil, "build", PhaseCheck, "test", "c2"); err != nil || rows != nil {
		t.Fatalf("no run: %+v %v", rows, err)
	}
	// At most MaxChecksOn, the newest.
	attemptOf(t, l, "team-0000000b", "build", 4, AttemptFailed)
	var many []CheckRow
	for k := 1; k <= MaxChecksOn+5; k++ {
		many = append(many, CheckRow{TaskID: "build", N: 4, ID: "test", K: k, Argv: argv, Subject: "c2", Exit: 1})
	}
	put("team-0000000b", many...)
	rows, err = l.ChecksOn(ctx, chain, "build", PhaseCheck, "test", "c2")
	if err != nil || len(rows) != MaxChecksOn || rows[0].N != 4 || rows[0].K != MaxChecksOn+5 || rows[MaxChecksOn-1].K != 6 {
		t.Fatalf("past the bound: %d rows, first %+v, %v", len(rows), rows[0], err)
	}
	var runs []string
	for i := 0; i <= MaxChecksOnRuns; i++ {
		runs = append(runs, fmt.Sprintf("team-%08x", i))
	}
	if _, err := l.ChecksOn(ctx, runs, "build", PhaseCheck, "test", "c2"); err == nil {
		t.Fatal("more runs than the bound were read")
	}
}
