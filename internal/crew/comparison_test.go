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
	"time"

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

// twoCandidates is a comparison of approach-a and approach-b in dir, each
// with its commit off base.
func twoCandidates(t *testing.T, dir string, edit func(c *Crew), a, b []edit) (Crew, string, string, string) {
	t.Helper()
	if runGit(t, dir, "status", "--porcelain") != "" {
		commitAll(t, dir, "the base")
	}
	base := head(t, dir)
	ca := commitAt(t, dir, base, "a", a...)
	cb := commitAt(t, dir, base, "b", b...)
	c := compareCrew(t, dir, 2, false)
	if edit != nil {
		edit(&c)
	}
	return c, base, ca, cb
}

// withChecks gives every candidate task the checks.
func withChecks(checks ...Check) func(c *Crew) {
	return func(c *Crew) {
		for _, cd := range c.Compare.Candidates {
			taskByID(c, cd.Task).Checks = slices.Clone(checks)
		}
	}
}

// I5: a row is comparable only with the same command, timeout, place and
// manifest, a finished, stable run of the candidate's own commit in every
// cell; each condition broken alone makes it not comparable, with its
// words.
func TestComparableNeedsTheSameCheckManifestAndWhere(t *testing.T) {
	dir := newRepo(t)
	c, base, a, b := twoCandidates(t, dir, func(c *Crew) {
		c.Manifest = &Manifest{Verify: []Check{{ID: "lint", Argv: []string{"true"}}}}
	}, []edit{put("a.txt", "a\n")}, []edit{put("b.txt", "b\n")})
	trees := func(cr *chainRead, bManifest string) {
		cr.trees[synthRun] = []ledger.TreeRow{
			{RunID: synthRun, TaskID: "approach-a", N: 1, Kind: ledger.TreeCheckout, Manifest: "crew:1"},
			{RunID: synthRun, TaskID: "approach-b", N: 1, Kind: ledger.TreeCheckout, Manifest: bManifest},
		}
	}
	cases := []struct {
		name  string
		phase string
		edit  func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow
		words []string // "" none: comparable
	}{
		{"every condition held", ledger.PhaseCheck, nil, nil},
		{"every condition held, the verify step", ledger.PhaseVerify, nil, nil},
		{"another argv", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].Argv = []string{"false"}
			return rows
		}, []string{"approach-b ran another command than the expected one", "the command: approach-a and approach-b differ"}},
		{"another timeout", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			cr.crew = cloneCrew(cr.crew)
			taskByID(&cr.crew, "approach-b").Checks[0].TimeoutSeconds = 30
			return rows
		}, []string{"the timeout: approach-a and approach-b differ"}},
		{"another place", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].Where = "host:lab"
			return rows
		}, []string{"where it ran: approach-a and approach-b differ"}},
		{"another manifest for a verify step", ledger.PhaseVerify, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[1].Manifest = "crew:2"
			return rows
		}, []string{"the manifest: approach-a and approach-b differ"}},
		{"another manifest's checkout for a check", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			trees(cr, "crew:2")
			return rows
		}, []string{"the manifest: approach-a and approach-b differ"}},
		{"not stable", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].Stable = false
			return rows
		}, []string{"approach-b: its run is not stable"}},
		{"interrupted", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].State = ledger.CheckInterrupted
			return rows
		}, []string{"approach-b: its run did not finish (interrupted)"}},
		{"cancelled", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].Killed = true
			return rows
		}, []string{"approach-b: its run did not finish (cancelled)"}},
		{"still running", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].State = ledger.CheckRunning
			return rows
		}, []string{"approach-b: its run did not finish (running)"}},
		{"no receipt", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			return rows[1:]
		}, []string{"approach-b: " + notRunNoReceiptText}},
		{"another commit checked", ledger.PhaseCheck, func(cr *chainRead, rows []ledger.CheckRow) []ledger.CheckRow {
			rows[0].Subject = base
			return rows
		}, []string{"approach-b: it checked " + base[:12] + ", not the candidate's commit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b})
			trees(&cr, "crew:1")
			if tc.edit != nil {
				key := synthRun + "/approach-b/1"
				cr.checks[key] = tc.edit(&cr, slices.Clone(cr.checks[key]))
			}
			cmp := measure(t, cr)
			rw := rowOfCheck(t, cmp, tc.phase, map[string]string{ledger.PhaseCheck: "ok", ledger.PhaseVerify: "lint"}[tc.phase])
			if len(rw.Cells) != 2 {
				t.Fatalf("cells: %+v", rw.Cells)
			}
			if tc.words == nil {
				if rw.Verdict != VerdictComparable || len(rw.Why) != 0 {
					t.Fatalf("held: %+v", rw)
				}
				return
			}
			if rw.Verdict != VerdictNotComparable {
				t.Fatalf("broken: %s %v", rw.Verdict, rw.Why)
			}
			for _, w := range tc.words {
				if !slices.Contains(rw.Why, w) {
					t.Errorf("no %q in %q", w, rw.Why)
				}
			}
		})
	}
}

// cloneCrew is c with tasks of its own, for a test's edit.
func cloneCrew(c Crew) Crew {
	c.Tasks = slices.Clone(c.Tasks)
	for i := range c.Tasks {
		c.Tasks[i].Checks = slices.Clone(c.Tasks[i].Checks)
	}
	return c
}

// fileRows are a candidate's receipts of the file-form manifest at blob:
// its verify steps' ids, each passing, after the task's check ok.
func fileRows(task, commit, blob string, verify ...string) []ledger.CheckRow {
	rows := []ledger.CheckRow{{RunID: synthRun, TaskID: task, N: 1, ID: "ok", K: 1, Phase: ledger.PhaseCheck, Argv: []string{"true"}, Subject: commit, State: ledger.CheckFinished, Stable: true, Where: "native"}}
	for i, id := range verify {
		rows = append(rows, ledger.CheckRow{RunID: synthRun, TaskID: task, N: 1, ID: id, K: i + 2, Phase: ledger.PhaseVerify, Manifest: "file:" + blob, Argv: []string{"true"},
			Subject: commit, State: ledger.CheckFinished, Stable: true, Where: "native"})
	}
	return rows
}

// I5: a candidate that edits the file-form manifest makes its verify rows
// and its task checks not comparable, and says it changed it.
func TestAChangedManifestIsNotComparable(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, manifestFile), `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}]}`)
	c, base, a, b := twoCandidates(t, dir, func(c *Crew) { c.Manifest = &Manifest{File: true} },
		[]edit{put("a.txt", "a\n")}, []edit{put(manifestFile, `{"schema": 1, "verify": [{"id": "lint", "argv": ["true", "again"]}]}`)})
	was := runGit(t, dir, "rev-parse", base+":"+manifestFile)
	now := runGit(t, dir, "rev-parse", b+":"+manifestFile)
	cr := synthetic(c, base, synthCand{task: "approach-a", commit: a, rows: fileRows("approach-a", a, was, "lint")},
		synthCand{task: "approach-b", commit: b, rows: fileRows("approach-b", b, now, "lint")})
	cmp := measure(t, cr)
	if !column(t, cmp, "approach-b").ManifestChanged || column(t, cmp, "approach-a").ManifestChanged {
		t.Fatalf("ManifestChanged: %+v", cmp.Candidates)
	}
	for _, rw := range []ComparedCheck{rowOfCheck(t, cmp, ledger.PhaseVerify, "lint"), rowOfCheck(t, cmp, ledger.PhaseCheck, "ok")} {
		if rw.Verdict != VerdictNotComparable || !slices.Contains(rw.Why, "approach-b changed the project manifest") {
			t.Fatalf("%s: %s %q", rw.ID, rw.Verdict, rw.Why)
		}
	}
	if rw := rowOfCheck(t, cmp, ledger.PhaseVerify, "lint"); !slices.Contains(rw.Why, "the manifest: approach-a and approach-b differ") {
		t.Fatalf("the verify row: %q", rw.Why)
	}
}

// I5: a check whose script a candidate edited is not comparable.
func TestAChangedCheckScriptIsNotComparable(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("scripts/test.sh", "exit 0\n")})
	c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: []string{"sh", "scripts/test.sh"}}),
		[]edit{put("scripts/test.sh", "exit 0 # always\n")}, []edit{put("b.txt", "b\n")})
	cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}))
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "test")
	if rw.Verdict != VerdictNotComparable || !slices.Contains(rw.Why, "approach-a changed scripts/test.sh, which the check runs") {
		t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
	}
}

// I5: a candidate that edits a test file its check runs makes the row
// unknown, the file listed under it and none under the other.
func TestAChangedTestFileIsShown(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("internal/cache/bench_test.go", "package cache\n")})
	c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: []string{"go", "test", "./..."}}),
		[]edit{put("internal/cache/bench_test.go", "package cache\n\n// faster\n")}, []edit{put("internal/cache/cache.go", "package cache\n")})
	cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}))
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "test")
	want := "comparable only if their test changes are fair: approach-a changed internal/cache/bench_test.go, a test file; approach-b changed none"
	if rw.Verdict != VerdictUnknown || !slices.Equal(rw.Why, []string{want}) {
		t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
	}
	if vf := column(t, cmp, "approach-a").VerifyFiles; vf == nil || !slices.Equal(vf.Files, []ListedFile{{Path: "internal/cache/bench_test.go", Kind: FileTest}}) || vf.Total != 1 {
		t.Fatalf("approach-a's files: %+v", vf)
	}
	if vf := column(t, cmp, "approach-b").VerifyFiles; vf != nil {
		t.Fatalf("approach-b's files: %+v", vf)
	}
}

// I5: a check whose definition is a build file's (make's target, npm's
// scripts) is unknown when a candidate edits it; package.json's
// dependencies alone leave it comparable.
func TestIndirectDefinitionsMakeARowUnknown(t *testing.T) {
	pkg := `{"scripts": {"test": "vitest run"}, "dependencies": {"vue": "3.4.0"}}`
	cases := []struct {
		name           string
		argv           []string
		b              []edit
		verdict, words string
	}{
		{"make test, the Makefile edited", []string{"make", "test"}, []edit{put("Makefile", "test:\n\ttrue # skip\n")},
			VerdictUnknown, "comparable only if their test changes are fair: approach-a changed none; approach-b changed Makefile, a build file"},
		{"npm test, the scripts edited", []string{"npm", "test"}, []edit{put("package.json", `{"scripts": {"test": "true"}, "dependencies": {"vue": "3.4.0"}}`)},
			VerdictUnknown, "comparable only if their test changes are fair: approach-a changed none; approach-b changed package.json, a build file"},
		{"npm test, the dependencies alone edited", []string{"npm", "test"}, []edit{put("package.json", `{"scripts": {"test": "vitest run"}, "dependencies": {"vue": "3.5.0"}}`)},
			VerdictComparable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			applyEdits(t, dir, []edit{put("Makefile", "test:\n\tgo test ./...\n"), put("package.json", pkg)})
			c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: tc.argv}), []edit{put("a.txt", "a\n")}, tc.b)
			cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}))
			rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "test")
			if rw.Verdict != tc.verdict || (tc.words == "") != (len(rw.Why) == 0) || (tc.words != "" && !slices.Contains(rw.Why, tc.words)) {
				t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
			}
		})
	}
}

// I5: every expected check has a row, its receipts there or not: a test
// check the candidates' failed lint stopped is there, each cell not run
// for that reason, the row not comparable; the base's verify steps a
// candidate's manifest removed are there, its cells not run for that.
func TestEveryExpectedCheckHasARow(t *testing.T) {
	t.Run("an earlier check failed", func(t *testing.T) {
		dir := newRepo(t)
		c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "lint", Argv: []string{"false"}}, Check{ID: "test", Argv: []string{"true"}}),
			[]edit{put("a.txt", "a\n")}, []edit{put("b.txt", "b\n")})
		lintOnly := func(task, commit string) []ledger.CheckRow {
			rows := passingRows(c, task, commit)[:1]
			rows[0].Exit = 1
			return rows
		}
		cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a, rows: lintOnly("approach-a", a), state: ledger.TaskFailed},
			synthCand{task: "approach-b", commit: b, rows: lintOnly("approach-b", b), state: ledger.TaskFailed}))
		if len(cmp.Checks) != 2 {
			t.Fatalf("rows: %+v", cmp.Checks)
		}
		rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "test")
		if rw.Verdict != VerdictNotComparable || !slices.Contains(rw.Why, "approach-a: "+notRunEarlierText) || !slices.Contains(rw.Why, "approach-b: "+notRunEarlierText) {
			t.Fatalf("the test row: %s %q", rw.Verdict, rw.Why)
		}
		for _, cell := range rw.Cells {
			if cell.State != CellNotRun || cell.NotRun != NotRunEarlier || cell.Exit != nil {
				t.Fatalf("a cell: %+v", cell)
			}
		}
		if lint := rowOfCheck(t, cmp, ledger.PhaseCheck, "lint"); lint.Verdict != VerdictComparable {
			t.Fatalf("the lint row, failed alike: %s %q", lint.Verdict, lint.Why)
		}
	})
	t.Run("its manifest has no such step", func(t *testing.T) {
		dir := newRepo(t)
		writeFile(t, filepath.Join(dir, manifestFile), `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}, {"id": "unit", "argv": ["true"]}]}`)
		c, base, a, b := twoCandidates(t, dir, func(c *Crew) { c.Manifest = &Manifest{File: true} },
			[]edit{put("a.txt", "a\n")}, []edit{put(manifestFile, `{"schema": 1}`)})
		was := runGit(t, dir, "rev-parse", base+":"+manifestFile)
		now := runGit(t, dir, "rev-parse", b+":"+manifestFile)
		cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a, rows: fileRows("approach-a", a, was, "lint", "unit")},
			synthCand{task: "approach-b", commit: b, rows: fileRows("approach-b", b, now)}))
		for _, id := range []string{"lint", "unit"} {
			rw := rowOfCheck(t, cmp, ledger.PhaseVerify, id)
			if rw.Verdict != VerdictNotComparable || !slices.Contains(rw.Why, "approach-b: "+notRunNoStepText) || rw.Cells[1].NotRun != NotRunNoSuchStep || rw.Cells[0].State != ledger.CheckFinished {
				t.Fatalf("%s: %s %q %+v", id, rw.Verdict, rw.Why, rw.Cells)
			}
		}
	})
}

// A git that fails for one candidate (its commit not in the repository)
// leaves it unavailable with git's words and the others and the checks
// table whole; a measure past its 20 s budget, the clock moved through
// the git seam, likewise; neither is kept, so the next read measures
// again.
func TestAGitFailureLeavesTheRestWhole(t *testing.T) {
	dir := newRepo(t)
	c, base, _, b := twoCandidates(t, dir, nil, []edit{put("a.txt", "a\n")}, []edit{put("b.txt", "b\n")})
	gone := strings.Repeat("d", 40)
	cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: gone}, synthCand{task: "approach-b", commit: b}))
	ca, cb := column(t, cmp, "approach-a"), column(t, cmp, "approach-b")
	if !strings.HasPrefix(ca.Unavailable, "not measured: git diff") || ca.Size != nil {
		t.Fatalf("approach-a: %+v", ca)
	}
	if cb.Unavailable != "" || cb.Size == nil || statOf(t, cb.Size, "b.txt").Added != 1 {
		t.Fatalf("approach-b: %+v", cb)
	}
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "ok")
	if len(rw.Cells) != 2 || rw.Verdict != VerdictUnknown || !whyHas(rw, "approach-a's change could not be read") {
		t.Fatalf("the row: %+v", rw)
	}

	// The budget: the clock passes 20 s during approach-b's diff.
	a := commitAt(t, dir, base, "a", put("a.txt", "a\n"))
	e := &Engine{}
	now := time.Unix(1000, 0)
	var diffs atomic.Int32
	e.cmpMeasure.now = func() time.Time { return now }
	e.cmpMeasure.git = func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		if args[0] == "diff" && diffs.Add(1) == 2 {
			now = now.Add(compareBudget + time.Second)
		}
		return gitcliRunBounded(ctx, dir, limit, args...)
	}
	cr := synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b})
	late := e.measureComparison(t.Context(), cr)
	if la, lb := column(t, late, "approach-a"), column(t, late, "approach-b"); la.Size == nil || lb.Unavailable != measureLate || len(late.Checks) != 1 {
		t.Fatalf("past the budget: a %+v, b %+v", la, lb)
	}
	again := e.measureComparison(t.Context(), cr)
	if ga, gb := column(t, again, "approach-a"), column(t, again, "approach-b"); ga.Unavailable != "" || gb.Unavailable != "" || gb.Size == nil {
		t.Fatalf("measured again: a %+v, b %+v", ga, gb)
	}
	if n := diffs.Load(); n != 4 {
		t.Fatalf("diffs: %d, want the two again", n)
	}
	// Whole, it is kept: a third read asks git nothing.
	e.measureComparison(t.Context(), cr)
	if n := diffs.Load(); n != 4 {
		t.Fatalf("diffs after a kept measure: %d", n)
	}
}

// The comparison of a continued chain: either run answers the same, the
// launched run as Run, an imported candidate with its source run, the lane
// that had not ended when the run stopped re-run in the continuation; the
// reply's size does not grow with more continuations.
func TestTheComparisonWalksTheChain(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	e.compareOpen = true
	run := launchManaged(t, e, compareCrew(t, dir, 2, false))
	submitEdits(t, e, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning), "a", put("a.txt", "a\n"))
	waitTask(t, e, run.ID, "approach-a", ledger.TaskSucceeded, 1)
	waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	b := waitTaskWhere(t, e, next.ID, "approach-b", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	submitEdits(t, e, b, "b", put("b.txt", "b\n"))
	waitTask(t, e, next.ID, "approach-b", ledger.TaskSucceeded, 2)

	first := comparisonOf(t, e, run.ID)
	second := comparisonOf(t, e, next.ID)
	ja, _ := json.Marshal(first)
	jb, _ := json.Marshal(second)
	if string(ja) != string(jb) {
		t.Fatalf("the two runs answer apart:\n%s\n%s", ja, jb)
	}
	ca, cb := column(t, first, "approach-a"), column(t, first, "approach-b")
	if first.Run != run.ID || ca.Run != run.ID || ca.Attempt != 1 || !ca.Selectable || cb.Run != next.ID || cb.Attempt != 2 || cb.Attempts != 2 || !cb.Selectable {
		t.Fatalf("the chain's comparison: %+v", first)
	}
	if rw := rowOfCheck(t, first, ledger.PhaseCheck, "ok"); rw.Verdict != VerdictComparable || rw.Cells[0].Receipt != fmt.Sprintf("check:%s/approach-a/1/ok/%d", run.ID, rw.Cells[0].K) {
		t.Fatalf("the row across the chain: %+v", rw)
	}

	// Two more continuations, every result imported: the same size.
	last := next.ID
	stopRun(t, e, last)
	for range 2 {
		more, err := e.ContinueRun(t.Context(), last, "")
		if err != nil {
			t.Fatal(err)
		}
		waitTask(t, e, more.ID, "approach-b", ledger.TaskSucceeded, 2)
		stopRun(t, e, more.ID)
		last = more.ID
	}
	later := comparisonOf(t, e, last)
	jl, _ := json.Marshal(later)
	if len(jl) != len(ja) || later.Run != run.ID || column(t, later, "approach-b").Run != next.ID {
		t.Fatalf("after more continuations: %d bytes, was %d: %s", len(jl), len(ja), jl)
	}
}

// The fullest comparison fits MaxComparison: every part at its bound,
// every string of '<' (six bytes encoded), 48 rows (four manifests whose
// verify ids all differ from the base's), each argv at 8 KiB cut to its
// preview with its digest.
func TestTheFullestComparisonFitsItsBound(t *testing.T) {
	lt := func(n int) string { return strings.Repeat("<", n) }
	argv := []string{"sh"}
	for len(argvBytes(argv)) < 8<<10 {
		argv = append(argv, lt(255))
	}
	var checks, verify []Check
	for i := range 8 {
		checks = append(checks, Check{ID: fmt.Sprintf("check-%d", i), Argv: argv, TimeoutSeconds: 600})
		verify = append(verify, Check{ID: fmt.Sprintf("verify-%d", i), Argv: argv})
	}
	c := compareCrew(t, "/repo", 4, false)
	c.Goal = lt(maxGoal)
	c.Manifest = &Manifest{Verify: verify}
	c.Compare.Criteria = criteriaOf(maxCompareCriteria, lt(maxCriterionText))
	for i := range c.Compare.Candidates {
		c.Compare.Candidates[i].Approach = lt(maxApproach)
	}
	for i := range c.Members {
		c.Members[i].AgentID = lt(300)
		c.Members[i].Args = slices.Repeat([]string{lt(300)}, maxCompareArgs+1)
	}
	withChecks(checks...)(&c)
	base := strings.Repeat("b", 40)
	var cands []synthCand
	for i, cd := range c.Compare.Candidates {
		commit := strings.Repeat(string(rune('0'+i)), 40)
		rows := passingRows(c, cd.Task, commit)
		for j := range 8 {
			rows = append(rows, ledger.CheckRow{ID: fmt.Sprintf("own-%d-%d", i, j), K: len(rows) + 1, Phase: ledger.PhaseVerify, Manifest: "file:" + strings.Repeat("f", 64), Argv: argv})
		}
		for j := range rows {
			rows[j].RunID, rows[j].TaskID, rows[j].N, rows[j].Subject = synthRun, cd.Task, 1, commit
			rows[j].Where, rows[j].State, rows[j].Signal, rows[j].LogSHA256 = lt(ledger.MaxWhere), ledger.CheckInterrupted, "SIGKILL", strings.Repeat("a", 64)
		}
		cands = append(cands, synthCand{task: cd.Task, commit: commit, rows: rows})
	}
	cr := synthetic(c, base, cands...)
	// The measure kept as git would have made it at every bound.
	m := &measured{cands: map[string]candidateMeasure{}, named: map[string][]string{}}
	var stats []FileStat
	for i := range maxDiffList + 1 {
		stats = append(stats, FileStat{Path: fmt.Sprintf("%d", i) + lt(400), Status: "M", Added: 1 << 20, Removed: 1 << 20})
	}
	key := base
	for _, cd := range cands {
		key += "|" + cd.task + "=" + cd.commit
		dc := &DepChanges{Files: slices.Repeat([]string{cutEncoded(lt(400), maxComparePath)}, maxDepFiles), Total: 1000, ModulesTotal: 1000}
		for range maxDepModules {
			dc.Modules = append(dc.Modules, ModuleChange{File: cutEncoded(lt(400), maxComparePath), Name: cutEncoded(lt(400), maxComparePath), From: cutEncoded(lt(100), maxDepVersion), To: cutEncoded(lt(100), maxDepVersion)})
		}
		fl := &FileList{Total: 1000}
		for range maxVerifyFiles {
			fl.Files = append(fl.Files, ListedFile{Path: cutEncoded(lt(400), maxComparePath), Kind: FileBuild})
		}
		var shared, equivalent []SharedCommits
		for _, other := range cands {
			if other.task != cd.task {
				shared = append(shared, SharedCommits{With: other.task, Count: maxSharedCount, Commits: slices.Repeat([]string{strings.Repeat("c", 40)}, maxSharedList)})
				equivalent = append(equivalent, SharedCommits{With: other.task, Count: maxCherryCommits, Commits: slices.Repeat([]string{strings.Repeat("e", 40)}, maxSharedList), Note: cherryWords})
			}
		}
		m.cands[cd.task] = candidateMeasure{size: sizeOf(stats), deps: dc, verify: fl, shared: shared, equivalent: equivalent, paths: map[string]bool{}}
	}
	rows := addReceiptRows(expectedRows(t.Context(), c, base, nil), (&Engine{}).columnsOf(cr), cr)
	for _, rw := range rows {
		m.named[rw.digest] = []string{}
	}
	e := &Engine{}
	e.cmpMeasure.cache = map[string]*measured{key: m}
	e.cmpMeasure.order = []string{key}
	e.cmpMeasure.git = func(context.Context, string, int, ...string) (string, error) {
		t.Fatal("git asked: the measure is kept")
		return "", nil
	}
	cmp := e.measureComparison(t.Context(), cr)
	b, err := json.Marshal(cmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxComparison {
		t.Fatalf("the fullest comparison is %d bytes, more than %d", len(b), MaxComparison)
	}
	if len(cmp.Checks) != maxCompareRows {
		t.Fatalf("%d rows, want %d", len(cmp.Checks), maxCompareRows)
	}
	for _, rw := range cmp.Checks {
		if !rw.ArgvCut || len(argvBytes(rw.Argv)) > maxArgvPreview || rw.ArgvDigest != argvDigest(argv) || len(rw.Why) > maxWhy || len(rw.Cells) != 4 {
			t.Fatalf("row %s: cut %v, %d bytes, %d reasons, %d cells", rw.ID, rw.ArgvCut, len(argvBytes(rw.Argv)), len(rw.Why), len(rw.Cells))
		}
		for _, w := range rw.Why {
			if len(argvBytes([]string{w})) > maxWhyBytes+4 {
				t.Fatalf("a reason of %d bytes", len(argvBytes([]string{w})))
			}
		}
	}
	for _, cc := range cmp.Candidates {
		if len(cc.Args) != maxCompareArgs || len(argvBytes(cc.Args[:1])) > maxCompareArg+4 || cc.Size.Total != maxDiffList+1 || len(cc.Size.List) != maxDiffList {
			t.Fatalf("%s: %d args, %d of %d files", cc.Task, len(cc.Args), len(cc.Size.List), cc.Size.Total)
		}
	}
	t.Logf("the fullest comparison: %d bytes", len(b))
}

// argvBytes is argv as encoded.
func argvBytes(argv []string) []byte {
	b, _ := json.Marshal(argv)
	return b
}
