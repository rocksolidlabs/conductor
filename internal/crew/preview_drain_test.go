package crew

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// drained reports whether the engine's Drain has begun.
func drained(e *Engine) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.drained
}

// holdOnce makes *hook, the first time it runs, tell reached and wait for
// release; later runs pass. release is closed at the test's end too.
func holdOnce(t *testing.T, hook *func()) (reached <-chan struct{}, release func()) {
	t.Helper()
	at, gate := make(chan struct{}), make(chan struct{})
	var once, opened sync.Once
	*hook = func() {
		first := false
		once.Do(func() { first = true })
		if first {
			close(at)
			<-gate
		}
	}
	release = func() { opened.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return at, release
}

// noCheckout fails the test when the candidate's checkout was made.
func noCheckout(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the checkout %s was made: %v", dir, err)
	}
}

// Once the shutdown's Drain has begun, no preview is reserved: a command
// that looked its run up before the drain (held there) and reaches the
// reservation after it is refused as a stopped run's is, its receipt
// failed run_stopped, nothing of the preview written or made, no context
// kept; a run the engine holds is refused the same.
func TestNoPreviewOpensOnceTheDrainHasBegun(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, ts := evicted(t, e, fl, l, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.afterLookup = func(string) { e.Drain(ctx) }
	rc, _, err := e.PreviewCandidate(t.Context(), run.ID, "build", 1, "admin", Command{ID: "open-1"})
	e.afterLookup = nil
	if !errors.Is(err, ErrRunStopped) || rc.Status != ledger.CommandFailed || rc.Refusal != RefusalRunStopped {
		t.Fatalf("an open reaching its reservation after the drain: %+v %v", rc, err)
	}
	if _, kept, ok := l.commandRow(t, run.ID, "admin", "open-1"); !ok || kept.Refusal != RefusalRunStopped {
		t.Fatalf("the open's receipt: %+v %v", kept, ok)
	}
	if _, ok := l.previewRow(run.ID, "build", 1, 1); ok || previewHeld(e, run.ID, "build", 1) || previewContext(e, run.ID) != nil {
		t.Fatal("the refused open left a row, a preview or a context")
	}
	noCheckout(t, ts.Attempt.Worktree+".preview")
	// A run the engine holds: refused the same after the drain.
	held, hts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "c")))
	if _, err := e.OpenPreview(t.Context(), held.ID, "build", 1, "admin"); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("a held run's preview after the drain: %v", err)
	}
	noCheckout(t, hts.Attempt.Worktree+".preview")
}

// An open reserved before the drain and held before its first transaction
// is waited for by Drain, which returns only once the open has ended,
// refused: no row, no checkout, nothing of it left.
func TestTheDrainWaitsForAnOpenReservedBeforeIt(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, ts := evicted(t, e, fl, l, dir)
	reserved, release := holdOnce(t, &e.afterReserve)
	opened := make(chan error, 1)
	go func() {
		_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
		opened <- err
	}()
	await(t, "the open's reservation", reserved)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		e.Drain(ctx)
		done <- previewHeld(e, run.ID, "build", 1)
	}()
	waitFake(t, "the drain begun", func() bool { return drained(e) })
	select {
	case <-done:
		t.Fatal("Drain returned while an open it must wait for was under way")
	default:
	}
	release()
	if err := await(t, "the open's end", opened); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("the open under way at the drain: %v", err)
	}
	if held := await(t, "the drain's end", done); held {
		t.Fatal("Drain returned with the preview held")
	}
	if _, ok := l.previewRow(run.ID, "build", 1, 1); ok {
		t.Fatal("the open under way at the drain wrote its row")
	}
	noCheckout(t, ts.Attempt.Worktree+".preview")
}

// An open whose first transaction committed and whose checkout is being
// made as the drain begins is ended, why shutdown, and waited for: Drain
// returns once its row is ended, and its service never ran.
func TestTheDrainEndsAnOpenWhoseCheckoutIsBeingMade(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, ts := evicted(t, e, fl, l, dir)
	made, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.checkoutMade = func(string) {
		once.Do(func() {
			close(made)
			<-release
		})
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	opened := make(chan error, 1)
	go func() {
		_, err := e.OpenPreview(context.Background(), run.ID, "build", 1, "admin")
		opened <- err
	}()
	await(t, "the checkout made", made)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		e.Drain(ctx)
		close(done)
	}()
	waitFake(t, "the drain begun", func() bool { return drained(e) })
	select {
	case <-done:
		t.Fatal("Drain returned while the open it ended was under way")
	default:
	}
	close(release)
	<-opened
	await(t, "the drain's end", done)
	row, _ := l.previewRow(run.ID, "build", 1, 1)
	if row.State != ledger.PreviewEnded || row.Why != whyShutdown {
		t.Fatalf("the preview once Drain returned: %+v", row)
	}
	if _, err := os.Stat(stateFile(ts.Attempt.Worktree+".preview", "web.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the preview's service ran: %v", err)
	}
}

// A run whose records cannot make a context now (its crew as launched
// unreadable, its folder no longer a work tree) is refused as such, in
// words that name no git message, with no context kept.
func TestAnUnreadableRunIsRefusedInWords(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run, _ := evicted(t, e, fl, l, dir)
	l.mu.Lock()
	row := l.runs[run.ID]
	l.mu.Unlock()
	var c map[string]any
	if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	c["cwd"] = elsewhere
	moved, _ := json.Marshal(c)
	for name, crewJSON := range map[string]string{"a crew not read": "{", "a folder not a work tree": string(moved)} {
		l.mu.Lock()
		r := l.runs[run.ID]
		r.Crew = crewJSON
		l.runs[run.ID] = r
		l.mu.Unlock()
		_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
		if !errors.Is(err, ErrRunUnreadable) || strings.Contains(err.Error(), "fatal") || previewContext(e, run.ID) != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if previewRefusal(err) != RefusalRunUnreadable {
			t.Fatalf("%s: refusal %q", name, previewRefusal(err))
		}
	}
}
