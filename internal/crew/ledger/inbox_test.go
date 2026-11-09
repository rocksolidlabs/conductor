package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The decision inbox's reads (docs/plans/decision-inbox.md, PR-3): the
// decisions in the inbox's states oldest first by (created_at, ref) with
// their count, and those closed since a time, latest first.

// inboxOrder orders rows as the inbox does: by created_at, then ref as text.
func inboxOrder(a, b DecisionRow) int {
	if n := a.CreatedAt.Compare(b.CreatedAt); n != 0 {
		return n
	}
	return strings.Compare(DecisionRef(a.N), DecisionRef(b.N))
}

// 230 decisions over three runs, opened in turn, open to each run's bound
// and the rest answered, beside closed ones: read page by page from the
// cursor, each once, in order, the count right on every page, the closed
// ones on none. Stamps of whole seconds and of one fraction, and two
// decisions of one time, sort as the times and then the refs do.
func TestInboxDecisions(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	runs := []string{"team-00000001", "team-00000002", "team-00000003"}
	for _, r := range runs {
		newRun(t, l, r)
	}
	var want []int64
	opened := map[string]int{}
	for i := range 230 {
		run := runs[i%3]
		n := open(t, l, gateDecision(run, fmt.Sprintf("g%d", i)))
		opened[run]++
		if opened[run] > MaxOpenPerRun-4 {
			answer(t, l, run, n, fmt.Sprintf("a%d", i), workbenchAuth)
		}
		want = append(want, n)
	}
	for i, r := range runs {
		// Closed ones, never listed.
		n := open(t, l, gateDecision(r, fmt.Sprintf("closed%d", i)))
		setState(t, l, r, n, []string{DecisionCancelled, DecisionSuperseded, DecisionExpired}[i], "")
	}
	// Times the text of a stamp misorders: a whole second after its own
	// half, and two decisions at one time, dr-10 before dr-9 as refs.
	for n, at := range map[int64]string{1: "2026-11-08T10:00:00.5Z", 2: "2026-11-08T10:00:00Z", 9: "2026-11-08T09:00:00Z", 10: "2026-11-08T09:00:00Z"} {
		if _, err := l.db.Exec("UPDATE decisions SET created_at = ? WHERE n = ?", at, n); err != nil {
			t.Fatal(err)
		}
	}
	var rows []DecisionRow
	for _, n := range want {
		rows = append(rows, record(t, l, n).DecisionRow)
	}
	slices.SortFunc(rows, inboxOrder)
	if rows[0].N != 10 || rows[1].N != 9 || rows[2].N != 2 || rows[3].N != 1 {
		t.Fatalf("the order the inbox needs begins %s %s %s %s", rows[0].ID(), rows[1].ID(), rows[2].ID(), rows[3].ID())
	}
	states := []string{DecisionOpen, DecisionAnswered}
	var got []InboxDecision
	after := InboxCursor{}
	for pages := 0; ; pages++ {
		page, next, total, tracking, err := l.InboxDecisions(ctx, states, after, 50)
		if err != nil {
			t.Fatal(err)
		}
		if total != 230 || tracking != 230-3*(MaxOpenPerRun-4) {
			t.Fatalf("page %d: total %d, %d answered", pages, total, tracking)
		}
		got = append(got, page...)
		if !next {
			if len(page) > 50 || pages != 4 {
				t.Fatalf("the last page %d holds %d", pages, len(page))
			}
			break
		}
		if len(page) != 50 {
			t.Fatalf("page %d holds %d", pages, len(page))
		}
		last := page[len(page)-1]
		after = InboxCursor{Since: last.CreatedAt, Ref: DecisionRef(last.N)}
	}
	if len(got) != len(rows) {
		t.Fatalf("read %d of %d", len(got), len(rows))
	}
	answered := 0
	for i := range rows {
		if got[i].N != rows[i].N {
			t.Fatalf("at %d: %s, want %s", i, got[i].ID(), rows[i].ID())
		}
		if got[i].State == DecisionAnswered {
			answered++
		}
	}
	if answered != 230-3*(MaxOpenPerRun-4) {
		t.Fatalf("%d answered", answered)
	}
	// A cursor of another kind's ref at a decision's time: a ref before
	// "decision:" leaves the decision on the page, one after it does not.
	at := rows[5].CreatedAt
	for _, c := range []struct {
		ref  string
		with bool
	}{{"brief:br-1/0", true}, {"question:0123456789abcdef/q1", false}} {
		page, _, _, _, err := l.InboxDecisions(ctx, states, InboxCursor{Since: at.Add(-time.Nanosecond), Ref: "zzz"}, 1)
		if err != nil || len(page) != 1 || page[0].N != rows[5].N {
			t.Fatalf("just before %s: %v %v", rows[5].ID(), page, err)
		}
		page, _, _, _, err = l.InboxDecisions(ctx, states, InboxCursor{Since: at, Ref: c.ref}, 1)
		if err != nil || (len(page) == 1 && page[0].N == rows[5].N) != c.with {
			t.Fatalf("after %s at its time: %v %v", c.ref, page, err)
		}
	}
	if page, next, total, tracking, err := l.InboxDecisions(ctx, []string{DecisionCancelled}, InboxCursor{}, 0); err != nil || len(page) != 1 || next || total != 1 || tracking != 0 {
		t.Fatalf("the cancelled: %v %v %d %d %v", page, next, total, tracking, err)
	}
}

// A decision link's scope: the run's decisions in every state, a task's
// (asked by it or holding it) when one is named, each with what it holds
// and its responses and history counted in the same read; a comment and a
// refused response, which a rev does not always mark, move the counts.
func TestScopeDecisions(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	const run, other = "team-00000001", "team-00000002"
	newRun(t, l, run)
	newRun(t, l, other)
	tasksOf(t, l, run, "build", "deploy")
	asked := open(t, l, agentDecision(run, "build", 1, "q1"))
	gate := open(t, l, gateDecision(run, "g1"))
	in(t, l, run, func(tx *Tx) error { return tx.Hold(run, "deploy", gate) })
	closed := open(t, l, gateDecision(run, "g2"))
	setState(t, l, run, closed, DecisionCancelled, "")
	open(t, l, gateDecision(other, "g1"))
	ns := func(got []InboxDecision) []int64 {
		var out []int64
		for _, d := range got {
			out = append(out, d.N)
		}
		return out
	}
	got, err := l.ScopeDecisions(ctx, run, "")
	if err != nil || !slices.Equal(ns(got), []int64{asked, gate, closed}) || got[2].State != DecisionCancelled {
		t.Fatalf("the run's: %v %v", ns(got), err)
	}
	if got[0].Held != nil || !slices.Equal(got[1].Held, []string{"deploy"}) || got[2].Held != nil {
		t.Fatalf("what each holds: %+v", got)
	}
	if got, err := l.ScopeDecisions(ctx, run, "build"); err != nil || !slices.Equal(ns(got), []int64{asked}) {
		t.Fatalf("build's: %v %v", ns(got), err)
	}
	if got, err := l.ScopeDecisions(ctx, run, "deploy"); err != nil || !slices.Equal(ns(got), []int64{gate}) {
		t.Fatalf("deploy's, by its hold: %v %v", ns(got), err)
	}
	before, err := l.ScopeDecisions(ctx, run, "deploy")
	if err != nil || before[0].Responses != 0 || before[0].History != 0 {
		t.Fatalf("before: %+v %v", before, err)
	}
	in(t, l, run, func(tx *Tx) error {
		if _, _, err := tx.History(gate, HistoryRow{Kind: HistoryCommented, By: "admin", Detail: `{"text":"noted"}`, OpID: "c1", Fingerprint: "fp-c1"}); err != nil {
			return err
		}
		_, err := tx.Respond(gate, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r1", Fingerprint: "fp-r1", Status: ResponseRefused, Receipt: `{"words":"stale"}`})
		return err
	})
	after, err := l.ScopeDecisions(ctx, run, "deploy")
	rec := record(t, l, gate)
	if err != nil || after[0].Responses != len(rec.Responses) || after[0].History != len(rec.History) || after[0].Responses != 1 || after[0].History != 1 {
		t.Fatalf("after a comment and a refusal: %+v, the record %d %d, %v", after, len(rec.Responses), len(rec.History), err)
	}
	if got, err := l.ScopeDecisions(ctx, "", ""); err != nil || len(got) != 0 {
		t.Fatalf("no run: %v %v", got, err)
	}
}

// The decisions closed since a time, the latest closed first; the ones
// closed before it, and the open, are not among them.
func TestClosedDecisions(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	var ns []int64
	for i := range 4 {
		n := open(t, l, gateDecision(run, fmt.Sprintf("g%d", i)))
		ns = append(ns, n)
	}
	setState(t, l, run, ns[0], DecisionCancelled, "")
	setState(t, l, run, ns[1], DecisionCancelled, "")
	setState(t, l, run, ns[2], DecisionCancelled, "")
	for n, at := range map[int64]string{ns[0]: "2026-11-07T09:00:00Z", ns[1]: "2026-11-08T10:00:00Z", ns[2]: "2026-11-08T10:00:00.5Z"} {
		if _, err := l.db.Exec("UPDATE decisions SET closed_at = ? WHERE n = ?", at, n); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.ClosedDecisions(context.Background(), time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), 0)
	if err != nil || len(got) != 2 || got[0].N != ns[2] || got[1].N != ns[1] {
		t.Fatalf("closed since: %v %v", got, err)
	}
	if got, err := l.ClosedDecisions(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), 1); err != nil || len(got) != 1 || got[0].N != ns[2] {
		t.Fatalf("one: %v %v", got, err)
	}
}
