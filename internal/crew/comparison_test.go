package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// gitcliRunBounded is the measure's own git, for a seam that wraps it.
var gitcliRunBounded = gitcli.RunBounded

// edit is one file a test commit writes (content) or removes (gone).
type edit struct {
	path, content string
	gone          bool
}

// put and rm are a commit's edits.
func put(path, content string) edit { return edit{path: path, content: content} }
func rm(path string) edit           { return edit{path: path, gone: true} }

// applyEdits writes and removes the files of edits under dir.
func applyEdits(t *testing.T, dir string, edits []edit) {
	t.Helper()
	for _, ed := range edits {
		p := filepath.Join(dir, ed.path)
		if ed.gone {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, ed.content)
	}
}

// submitEdits commits edits in the attempt's worktree and submits the
// commit as its result.
func submitEdits(t *testing.T, e *Engine, ts TaskState, summary string, edits ...edit) {
	t.Helper()
	applyEdits(t, ts.Attempt.Worktree, edits)
	commitAll(t, ts.Attempt.Worktree, summary)
	submit(t, e, ts.Attempt.SessionID, Result{Summary: summary})
}

// commitAt commits edits on a detached HEAD at from in dir and returns
// the commit, leaving dir there.
func commitAt(t *testing.T, dir, from, message string, edits ...edit) string {
	t.Helper()
	runGit(t, dir, "checkout", "-q", "--detach", from)
	applyEdits(t, dir, edits)
	commitAll(t, dir, message)
	return head(t, dir)
}

// comparisonOf is the run's comparison, which must be read.
func comparisonOf(t *testing.T, e *Engine, runID string) Comparison {
	t.Helper()
	cmp, err := e.Comparison(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return cmp
}

// column is the candidate of the comparison by its task.
func column(t *testing.T, cmp Comparison, task string) ComparedCandidate {
	t.Helper()
	for _, cc := range cmp.Candidates {
		if cc.Task == task {
			return cc
		}
	}
	t.Fatalf("no column %s in %+v", task, cmp.Candidates)
	return ComparedCandidate{}
}

// rowOfCheck is the comparison's row of the phase and id.
func rowOfCheck(t *testing.T, cmp Comparison, phase, id string) ComparedCheck {
	t.Helper()
	for _, rw := range cmp.Checks {
		if rw.Phase == phase && rw.ID == id {
			return rw
		}
	}
	t.Fatalf("no row %s %s in %+v", phase, id, cmp.Checks)
	return ComparedCheck{}
}

// statOf is the diff's entry of the path.
func statOf(t *testing.T, ds *DiffSize, path string) FileStat {
	t.Helper()
	if ds == nil {
		t.Fatal("no size")
	}
	for _, st := range ds.List {
		if st.Path == path {
			return st
		}
	}
	t.Fatalf("no %s in %+v", path, ds.List)
	return FileStat{}
}

// whyHas reports whether a row's words include one with want in it.
func whyHas(rw ComparedCheck, want string) bool {
	return slices.ContainsFunc(rw.Why, func(w string) bool { return strings.Contains(w, want) })
}

// The measure of each candidate: line counts told apart by each path's raw
// status (a file added with 5 lines beside 5 lines appended, an empty file
// added beside one deleted), a rename counted as one deleted and one
// added whatever diff.renames says, a binary file, the list cut at 100
// with its total, and a diff past 4 MiB not measured.
func TestComparisonMeasuresEachCandidate(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("five.txt", "1\n2\n3\n"), put("empty-old", ""), put("old.txt", "a\nb\nc\nd\ne\nf\n")})
	commitAll(t, dir, "the base")
	runGit(t, dir, "config", "diff.renames", "true")
	e, _ := sqlEngine(t)
	e.compareOpen = true
	// The diff's bound made 64 KiB here, as a real one's 4 MiB: a diff
	// whose answer passes it is not measured.
	e.cmpMeasure.git = func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		if args[0] == "diff" {
			limit = 64 << 10
		}
		return gitcliRunBounded(ctx, dir, limit, args...)
	}
	run := launchManaged(t, e, compareCrew(t, dir, 3, false))
	defer stopRun(t, e, run.ID)

	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning), "a",
		put("added.txt", "1\n2\n3\n4\n5\n"), put("five.txt", "1\n2\n3\n4\n5\n6\n7\n8\n"),
		put("empty-new", ""), rm("empty-old"), rm("old.txt"), put("renamed.txt", "a\nb\nc\nd\ne\nf\n"),
		put("pic.bin", "\x00\x01\x02\x03"))
	var many []edit
	for i := range 120 {
		many = append(many, put(fmt.Sprintf("many/f%03d.txt", i), "x\n"))
	}
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning), "b", many...)
	var wide []edit
	for i := range 600 {
		wide = append(wide, put(fmt.Sprintf("wide/%03d-%s.txt", i, strings.Repeat("w", 100)), "x\n"))
	}
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-c", ledger.AttemptRunning), "c", wide...)
	for _, task := range []string{"approach-a", "approach-b", "approach-c"} {
		waitTask(t, e, run.ID, task, ledger.TaskSucceeded, 1)
	}

	cmp := comparisonOf(t, e, run.ID)
	if cmp.Run != run.ID || cmp.Base == "" || cmp.Objective != "Make the user list fast." || len(cmp.Criteria) != 2 || len(cmp.Candidates) != 3 {
		t.Fatalf("the comparison: %+v", cmp)
	}
	a := column(t, cmp, "approach-a")
	if a.State != LaneReady || !a.Selectable || a.Attempt != 1 || a.Run != run.ID || a.Commit == "" || a.Start != cmp.Base || a.Member != "cand-a" || a.Agent != "claude" || a.Approach != "key the cache by locale" || a.Unavailable != "" {
		t.Fatalf("approach-a's column: %+v", a)
	}
	for path, want := range map[string]FileStat{
		"added.txt":   {Status: "A", Added: 5},
		"five.txt":    {Status: "M", Added: 5},
		"empty-new":   {Status: "A"},
		"empty-old":   {Status: "D"},
		"old.txt":     {Status: "D", Removed: 6},
		"renamed.txt": {Status: "A", Added: 6},
		"pic.bin":     {Status: "A", Binary: true},
	} {
		want.Path = path
		if got := statOf(t, a.Size, path); got != want {
			t.Errorf("%s: %+v, want %+v", path, got, want)
		}
	}
	if s := a.Size; s.Files != 7 || s.Total != 7 || s.FilesAdded != 4 || s.FilesDeleted != 2 || s.Added != 16 || s.Removed != 6 || s.NotMeasured != "" {
		t.Fatalf("approach-a's size: %+v", s)
	}
	if b := column(t, cmp, "approach-b"); b.Size == nil || len(b.Size.List) != maxDiffList || b.Size.Total != 120 || b.Size.Files != 120 || b.Size.Added != 120 {
		t.Fatalf("approach-b's size: %+v", b.Size)
	}
	if c := column(t, cmp, "approach-c"); c.Size == nil || c.Size.NotMeasured != notMeasuredDiff || len(c.Size.List) != 0 || c.Unavailable != "" {
		t.Fatalf("approach-c's size: %+v %q", c.Size, c.Unavailable)
	}
	// Every candidate ran the one check at its own commit: comparable, but
	// for the change that could not be measured.
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "ok")
	if len(rw.Cells) != 3 || rw.Verdict != VerdictUnknown || !whyHas(rw, "approach-c's change was not measured") {
		t.Fatalf("the check's row: %+v", rw)
	}
	for _, cell := range rw.Cells {
		if cell.State != ledger.CheckFinished || cell.Exit == nil || *cell.Exit != 0 || !cell.Stable || cell.K < 1 ||
			cell.Receipt != fmt.Sprintf("check:%s/%s/1/ok/%d", run.ID, cell.Task, cell.K) {
			t.Fatalf("a cell: %+v", cell)
		}
	}
	if !cmp.Settled {
		t.Fatal("every lane ended, and the comparison is not settled")
	}
	b, err := json.Marshal(cmp)
	if err != nil || len(b) > MaxComparison {
		t.Fatalf("encoded: %d bytes, %v", len(b), err)
	}
}

// A lane whose attempts all failed their checks shows its last submitted
// commit: its diff and its failed receipts, not selectable, its cells
// taking part in the verdicts.
func TestAFailedLaneShowsItsSubmission(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	e.compareOpen = true
	c := compareCrew(t, dir, 3, false)
	for _, cd := range c.Compare.Candidates {
		taskByID(&c, cd.Task).Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	}
	run := launchManaged(t, e, c)
	defer stopRun(t, e, run.ID)
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning), "a", put("a.txt", "a\n"))
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning), "b", put("b.txt", "b\n"))
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := waitTaskWhere(t, e, run.ID, "approach-c", fmt.Sprintf("attempt %d running", n), func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
		})
		submitEdits(t, e, ts, fmt.Sprintf("c%d", n), put("broken.txt", fmt.Sprintf("try %d\n", n)))
	}
	waitTask(t, e, run.ID, "approach-c", ledger.TaskFailed, maxAttemptsPerTask)
	waitTask(t, e, run.ID, "approach-a", ledger.TaskSucceeded, 1)
	waitTask(t, e, run.ID, "approach-b", ledger.TaskSucceeded, 1)

	cmp := comparisonOf(t, e, run.ID)
	cc := column(t, cmp, "approach-c")
	if cc.State != LaneFailed || cc.Selectable || cc.Attempt != maxAttemptsPerTask || cc.Attempts != maxAttemptsPerTask || cc.Commit == "" || cc.AcceptedAnyway {
		t.Fatalf("the failed lane: %+v", cc)
	}
	if st := statOf(t, cc.Size, "broken.txt"); st.Status != "A" || st.Added != 1 {
		t.Fatalf("its diff: %+v", st)
	}
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "ok")
	cell := rw.Cells[2]
	if cell.Task != "approach-c" || cell.State != ledger.CheckFinished || cell.Exit == nil || *cell.Exit == 0 || cell.Attempt != maxAttemptsPerTask {
		t.Fatalf("its cell: %+v", cell)
	}
	// Its failed run is finished, stable, and of its commit: the row
	// stands comparable, the failure being a result of the check.
	if rw.Verdict != VerdictComparable || len(rw.Why) != 0 {
		t.Fatalf("the row with the failed lane: %+v", rw)
	}
	if !cmp.Settled {
		t.Fatal("not settled with every lane ended")
	}
	for _, task := range []string{"approach-a", "approach-b"} {
		if cc := column(t, cmp, task); !cc.Selectable || cc.State != LaneReady {
			t.Fatalf("%s: %+v", task, cc)
		}
	}
}

// A lane with no submission at all makes every row not comparable, and the
// comparison is not settled while a lane runs.
func TestALaneWithNoResultMakesEveryRowNotComparable(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	e.compareOpen = true
	run := launchManaged(t, e, compareCrew(t, dir, 2, false))
	defer stopRun(t, e, run.ID)
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning), "a", put("a.txt", "a\n"))
	waitTask(t, e, run.ID, "approach-a", ledger.TaskSucceeded, 1)
	waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning)
	cmp := comparisonOf(t, e, run.ID)
	if b := column(t, cmp, "approach-b"); b.State != LaneRunning || b.Commit != "" || b.Size != nil || b.Selectable {
		t.Fatalf("the running lane: %+v", b)
	}
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "ok")
	if rw.Verdict != VerdictNotComparable || !whyHas(rw, "approach-b has no result") || len(rw.Cells) != 1 || cmp.Settled {
		t.Fatalf("the row: %+v, settled %v", rw, cmp.Settled)
	}
}

// A run of no comparison, and a run the ledger has not, are not found.
func TestAComparisonOfNoComparisonIsNotFound(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	defer stopRun(t, e, run.ID)
	for _, id := range []string{run.ID, "run-0000000000000000", "not a run"} {
		if _, err := e.Comparison(t.Context(), id); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	bare, _ := readyEngine(t)
	if _, err := bare.Comparison(t.Context(), run.ID); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("no ledger: %v", err)
	}
}

// The comparison reads one revision: a change committed between the reads
// (a lane retried, then the run continued) makes it read again, so a
// reply is all before or all after; a change at every try is
// ErrRunChanging.
func TestTheComparisonReadsOneRevision(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	e.compareOpen = true
	c := compareCrew(t, dir, 2, false)
	for _, cd := range c.Compare.Candidates {
		taskByID(&c, cd.Task).Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	}
	run := launchManaged(t, e, c)
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning), "a", put("a.txt", "a\n"))
	waitTask(t, e, run.ID, "approach-a", ledger.TaskSucceeded, 1)
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := waitTaskWhere(t, e, run.ID, "approach-b", fmt.Sprintf("attempt %d running", n), func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
		})
		submitEdits(t, e, ts, "b", put("broken.txt", fmt.Sprintf("b%d\n", n)))
	}
	waitTask(t, e, run.ID, "approach-b", ledger.TaskFailed, maxAttemptsPerTask)

	// The retry committed between the reads: the reply is the retried one,
	// its column running, never a failed column beside a row of the retry.
	var once atomic.Bool
	e.cmpMeasure.afterRead = func(string) {
		if once.CompareAndSwap(false, true) {
			if _, err := e.Retry(context.Background(), run.ID, "admin", Command{ID: "r1"}, "approach-b"); err != nil {
				t.Error(err)
			}
		}
	}
	cmp := comparisonOf(t, e, run.ID)
	if b := column(t, cmp, "approach-b"); b.State != LaneRunning || cmp.Settled {
		t.Fatalf("after the retry between the reads: %+v, settled %v", b, cmp.Settled)
	}
	e.cmpMeasure.afterRead = nil

	// A change at every try: the run paused and unpaused between each.
	var n atomic.Int32
	e.cmpMeasure.afterRead = func(string) {
		i := n.Add(1)
		if _, err := e.Pause(context.Background(), run.ID, "admin", Command{ID: fmt.Sprintf("p%d", i)}, i%2 == 1); err != nil {
			t.Error(err)
		}
	}
	if _, err := e.Comparison(t.Context(), run.ID); !errors.Is(err, ErrRunChanging) || n.Load() != compareReadTries {
		t.Fatalf("a change at every try: %v after %d tries", err, n.Load())
	}
	e.cmpMeasure.afterRead = nil

	// The run continued between the reads: the reply is the chain's, the
	// same from either run.
	waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	var next *Run
	var cont atomic.Bool
	e.cmpMeasure.afterRead = func(string) {
		if cont.CompareAndSwap(false, true) {
			var err error
			if next, err = e.ContinueRun(context.Background(), run.ID, ""); err != nil {
				t.Error(err)
			}
		}
	}
	before := comparisonOf(t, e, run.ID)
	e.cmpMeasure.afterRead = nil
	if next == nil {
		t.Fatal("not continued")
	}
	defer stopRun(t, e, next.ID)
	after := comparisonOf(t, e, next.ID)
	if before.Run != run.ID || after.Run != run.ID || column(t, before, "approach-a").Run != run.ID || column(t, after, "approach-a").Run != run.ID {
		t.Fatalf("the chain's comparison: before %+v, after %+v", before.Candidates, after.Candidates)
	}
}
