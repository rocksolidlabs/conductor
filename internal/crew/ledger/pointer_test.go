package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// told keeps what a ledger's observers were told, each with whether the
// change was committed when it was: the observer reads the database on a
// connection of its own, which sees only what is committed.
type told struct {
	mu      sync.Mutex
	commits []RunPointer
	prunes  []string
}

func (w *told) commit(p RunPointer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commits = append(w.commits, p)
}

func (w *told) prune(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prunes = append(w.prunes, id)
}

func (w *told) revisions() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []int64
	for _, p := range w.commits {
		out = append(out, p.Revision)
	}
	return out
}

// The observer is told once per committed transition, after its commit
// (what it is told is what a reader of the database then reads), before
// the next transition commits, and never of one whose commit failed.
func TestOnCommitAfterCommit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	ctx := context.Background()
	var w told
	// The observer runs under the ledger's lock, so it reads the database
	// on a connection of its own: what is committed and nothing else.
	reader, err := l.open(ctx, filepath.Join(dir, File), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	var bad []string
	l.OnCommit(func(p RunPointer) {
		var rev int64
		var state string
		if err := reader.QueryRow("SELECT revision, state FROM runs WHERE id = ?", p.ID).Scan(&rev, &state); err != nil || rev != p.Revision || state != p.State {
			bad = append(bad, p.ID)
		}
		w.commit(p)
	})
	newRun(t, l, "team-00000001")
	l.beforeCommit = func() error { return errors.New("disk full") }
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("x", nil) }); err == nil {
		t.Fatal("a failing commit succeeded")
	}
	l.beforeCommit = nil
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		r, _, err := tx.Run()
		if err != nil {
			return err
		}
		r.State, r.Outcome, r.Paused, r.ClosedAt = RunFinished, OutcomeSucceeded, true, tx.Now()
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("told of %v before it was committed", bad)
	}
	if got := w.revisions(); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("told revisions %v, want 1 and 2 (the failed commit never)", got)
	}
	p := w.commits[1]
	if p.ID != "team-00000001" || p.CrewID != "team" || p.State != RunFinished || p.Outcome != OutcomeSucceeded || !p.Paused || p.ClosedAt.IsZero() || p.CreatedAt.IsZero() {
		t.Fatalf("pointer %+v", p)
	}
	// Pointer reads what the observer was told.
	if got, ok, err := l.Pointer(ctx, "team-00000001"); err != nil || !ok || got != p {
		t.Fatalf("Pointer: %+v %v %v, want %+v", got, ok, err, p)
	}
	// A continuation's transition tells its run, then the run it marked,
	// whose revision moves with the mark: a state of its own, after the
	// one told before it (review round 1 on #553).
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		if err := tx.CreateRun(RunRow{ID: "team-00000002", CrewID: "team", Crew: "{}", State: RunRunning, ContinuedFrom: "team-00000001", CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Continued("team-00000001")
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(w.commits); n != 4 || w.commits[2].ID != "team-00000002" || w.commits[3].ID != "team-00000001" ||
		w.commits[3].ContinuedBy != "team-00000002" || w.commits[3].Revision != 3 || w.commits[2].ContinuedFrom != "team-00000001" {
		t.Fatalf("a continuation told %+v", w.commits[2:])
	}
	// Recover's transitions are told too.
	if _, err := l.Transition(ctx, "team-00000003", func(tx *Tx) error {
		return tx.CreateRun(RunRow{ID: "team-00000003", CrewID: "team", Crew: "{}", State: RunRunning, CreatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	last := w.commits[len(w.commits)-1]
	if last.ID != "team-00000003" || last.State != RunInterrupted || last.Revision != 2 {
		t.Fatalf("Recover told %+v", last)
	}
	if len(bad) > 0 {
		t.Fatalf("told of %v before it was committed", bad)
	}
	// Every state of a run told has a revision of its own, each after the
	// one told before it, so a store that keeps the greater drops none.
	prev := map[string]int64{}
	for _, p := range w.commits {
		if p.Revision <= prev[p.ID] {
			t.Fatalf("run %s told at revision %d after %d: %+v", p.ID, p.Revision, prev[p.ID], w.commits)
		}
		prev[p.ID] = p.Revision
	}
}

// A ledger with no database file holds no run (NoDatabase); one whose file
// is there but will not open, or that is open, is not an empty one.
func TestNoDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := takeLock
	takeLock = func(string) (*ownerLock, string, bool, error) { return nil, "", false, errors.New("no locks here") }
	none, err := Open(dir)
	takeLock = old
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Pointers(context.Background(), "", 10); !errors.Is(err, ErrUnavailable) || !none.NoDatabase() {
		t.Fatalf("no file: %v, NoDatabase %v", err, none.NoDatabase())
	}
	none.Close()
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	if l.NoDatabase() {
		t.Fatal("an open ledger has no database")
	}
	l.Close()
	f, err := os.OpenFile(filepath.Join(dir, File), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 100)), 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	damaged, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer damaged.Close()
	if _, err := damaged.Pointers(context.Background(), "", 10); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a damaged file's pointers: %v", err)
	}
	if damaged.NoDatabase() {
		t.Fatal("a damaged file read as no database")
	}
}

// Two transitions, the first held by a seam after its commit while the
// second is under way: the observer is told in commit order, the
// revisions increasing.
func TestOnCommitInOrder(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	var w told
	l.OnCommit(w.commit)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	l.afterCommit = func() {
		first := false
		once.Do(func() { first = true })
		if first {
			close(held)
			<-release
		}
	}
	bump := func() error {
		_, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("x", nil) })
		return err
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := bump(); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatal("the first transition never committed")
	}
	wg.Go(func() {
		if err := bump(); err != nil {
			t.Error(err)
		}
	})
	// The second waits for the first's call: the lock is the first's still.
	if l.mu.TryLock() {
		l.mu.Unlock()
		t.Fatal("the first transition let its lock go before its observer was told")
	}
	close(release)
	wg.Wait()
	if got := w.revisions(); !slices.Equal(got, []int64{2, 3}) {
		t.Fatalf("told revisions %v, want 2 then 3", got)
	}
}

// A committed prune is told with the run's id; a prune refused, or of a
// run with no row, is not.
func TestOnPrune(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	var w told
	l.OnPrune(w.prune)
	newRun(t, l, "team-00000001")
	if err := l.Prune(ctx, "team-00000001"); !errors.Is(err, ErrNotPrunable) {
		t.Fatalf("an open run pruned: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		r, _, err := tx.Run()
		if err != nil {
			return err
		}
		r.State, r.Outcome, r.ClosedAt = RunFinished, OutcomeSucceeded, tx.Now()
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000009"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.prunes, []string{"team-00000001"}) {
		t.Fatalf("told prunes %v, want the one", w.prunes)
	}
	if _, ok, err := l.Pointer(ctx, "team-00000001"); ok || err != nil {
		t.Fatalf("a pruned run's pointer: %v %v", ok, err)
	}
}

// Pointers pages through every run in id order.
func TestPointersPaged(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	for _, id := range []string{"team-00000003", "team-00000001", "team-00000002"} {
		newRun(t, l, id)
	}
	var ids []string
	after := ""
	for {
		page, err := l.Pointers(ctx, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			ids = append(ids, p.ID)
		}
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1].ID
	}
	if !slices.Equal(ids, []string{"team-00000001", "team-00000002", "team-00000003"}) {
		t.Fatalf("paged %v", ids)
	}
	for _, n := range []int{0, MaxPointers + 1} {
		if _, err := l.Pointers(ctx, "", n); err == nil {
			t.Fatalf("a page of %d taken", n)
		}
	}
}
