package crew

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A launch prepared at a start commit (prepared.go): the commit is the
// run's base, and every later attempt starts by the engine's own rule.

// launchAt prepares c at base and launches what was prepared.
func launchAt(t *testing.T, e *Engine, c Crew, base string) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := e.PrepareLaunch(ctx, c, base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base() != base {
		t.Fatalf("prepared at %s, asked %s", p.Base(), base)
	}
	run, release, err := e.LaunchPrepared(ctx, c, p, "", LaunchOpts{})
	release()
	if err != nil {
		t.Fatal(err)
	}
	return *run
}

// With a start commit that is not the folder's HEAD, the run's base is
// that commit: a task with no input starts there, the ledger's row keeps
// it; a dependent task starts at its need's result, a review at its
// candidate, and an attempt after a failed check at that attempt's
// result, as without one; a continuation starts from the same base.
func TestStartCommitKeepsTaskInputs(t *testing.T) {
	dir := newRepo(t)
	start := runGit(t, dir, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(dir, "later.txt"), "later\n")
	commitAll(t, dir, "later")
	head := runGit(t, dir, "rev-parse", "HEAD")
	mark := filepath.Join(t.TempDir(), "passes")
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", mark)}
	e, ld := sqlEngine(t)
	run := launchAt(t, e, repoCrew(dir, build, work("docs", "lead", "build"), review("review", "critic", "build")), start)
	if row, _, _ := ld.Run(context.Background(), run.ID); row.Base != start || start == head {
		t.Fatalf("the run's base is %s, the start commit %s (HEAD %s)", row.Base, start, head)
	}
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a1.Attempt.Start != start {
		t.Fatalf("build's first attempt starts at %s, not the start commit %s", a1.Attempt.Start, start)
	}
	failed := commitResult(t, e, a1, "built.txt", "one\n", "built once")
	a2 := waitTaskWhere(t, e, run.ID, "build", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	first := sqlAttemptWhen(t, ld, run.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.ResultCommit != "" })
	if a2.Attempt.Start != first.ResultCommit {
		t.Fatalf("the attempt after a failed check starts at %s, not its result %s (%+v)", a2.Attempt.Start, first.ResultCommit, failed)
	}
	writeFile(t, mark, "")
	commitResult(t, e, a2, "built.txt", "two\n", "built")
	built := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2).Result.Commit
	docs := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	if docs.Attempt.Start != built {
		t.Fatalf("docs starts at %s, its need's result %s", docs.Attempt.Start, built)
	}
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	if rev.Attempt.Start != built {
		t.Fatalf("the review starts at %s, its candidate %s", rev.Attempt.Start, built)
	}
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if row, _, _ := ld.Run(context.Background(), next.ID); row.Base != start {
		t.Fatalf("the continuation's base is %s, not %s", row.Base, start)
	}
}

// A start commit is a full commit id the folder's repository holds: a
// short id is refused (ErrInvalid), one it does not hold
// ErrStartCommitMissing; a launch of another crew than the one prepared is
// refused; a crew without tasks takes none.
func TestPrepareLaunchHoldsTheStartCommit(t *testing.T) {
	dir := newRepo(t)
	head := runGit(t, dir, "rev-parse", "HEAD")
	e, _ := sqlEngine(t)
	c := repoCrew(dir, work("build", "lead"))
	ctx := context.Background()
	if _, err := e.PrepareLaunch(ctx, c, head[:12]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a short id: %v", err)
	}
	if _, err := e.PrepareLaunch(ctx, c, strings.Repeat("a", 40)); !errors.Is(err, ErrStartCommitMissing) {
		t.Fatalf("a commit the repository lacks: %v", err)
	}
	if _, err := e.PrepareLaunch(ctx, testCrew(immediate("lead", "x")), head); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a start commit for a crew without tasks: %v", err)
	}
	p, err := e.PrepareLaunch(ctx, c, "")
	if err != nil || p.Base() != head {
		t.Fatalf("prepared with no start commit: %v %v", p, err)
	}
	if src, _, _ := p.Manifest(); src != ManifestNone {
		t.Fatalf("a crew with no manifest prepared with %s", src)
	}
	other := c
	other.ID = "another"
	if _, _, err := e.LaunchPrepared(ctx, other, p, "", LaunchOpts{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a launch of another crew than prepared: %v", err)
	}
}
