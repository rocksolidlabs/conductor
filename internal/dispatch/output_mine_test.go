package dispatch

import (
	"context"
	"testing"
)

// The marks of a process's own pending rows go with the rows' pending
// state: rows another process settled, read again by this one, leave
// nothing marked; its own settled rows leave nothing either.
func TestOwnPendingMarksGoWhenTheRowsAreNoLongerPending(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	s.SetOutputCheck(func(context.Context, string, bool) string { return VerdictPending })
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "A ticket written pending")
	comment(t, s, "c1", key, "a comment written pending, long enough")
	marked := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.pendingMine)
	}
	if marked() == 0 {
		t.Fatal("no row marked as this process's own")
	}
	other := openStore(t, dir)
	rows, err := other.PendingRows(ctx, MaxPendingPage)
	if err != nil || len(rows) == 0 {
		t.Fatalf("the rows as another process reads them: %d %v", len(rows), err)
	}
	for _, r := range rows {
		if r.Mine {
			t.Fatalf("row %d is the other process's own", r.Seq)
		}
		if err := other.SettleVerdicts(ctx, r.Seq, VerdictOutput, VerdictOutput); err != nil {
			t.Fatal(err)
		}
	}
	if left, err := s.PendingRows(ctx, MaxPendingPage); err != nil || len(left) != 0 {
		t.Fatalf("pending after the other's settle: %d %v", len(left), err)
	}
	if n := marked(); n != 0 {
		t.Fatalf("%d marks kept of rows no longer pending", n)
	}
}
