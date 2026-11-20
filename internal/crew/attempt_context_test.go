package crew

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The context each attempt was given, recorded (docs/plans/context-seams.md,
// CT-3): its rows with the attempt's workspace, its text in the data
// directory's context file, every write of an attempt's context one at a
// time.

// contextEngine is a managed engine over a fake ledger that records the
// context each attempt is given in a folder of the test's, and that
// answers blocks for every attempt.
func contextEngine(t *testing.T, blocks []TaskContext) (*Engine, *fakeLauncher, *fakeLedger, string) {
	t.Helper()
	e, fl, l := managedEngine(t)
	dir := useContextFiles(t, e)
	e.SetContext(func(context.Context, ext.ContextQuery) []TaskContext { return slices.Clone(blocks) })
	return e, fl, l, dir
}

// useContextFiles gives e a place for the context files in a folder of
// the test's: an engine with none asks no context.
func useContextFiles(t *testing.T, e *Engine) string {
	t.Helper()
	dir := t.TempDir()
	cf, err := OpenContextFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cf.Close() })
	e.UseContextFiles(cf)
	return dir
}

// contextFileOf is the context file of an attempt, as written.
func contextFileOf(t *testing.T, dir, run, task string, n int) contextFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "context", run, task+"."+strconv.Itoa(n)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f contextFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// rowsOf is the committed rows of an attempt.
func rowsOf(l *fakeLedger, run, task string, n int) []ledger.AttemptContextRow {
	got, _ := l.AttemptContext(context.Background(), run, task)
	return got[n]
}

var twoBlocks = []TaskContext{
	{Source: "graph", Title: "Callers of Build", Ref: "build.go", Body: "main calls Build.", Pack: "p-4f2c-1"},
	{Source: "graph", Title: "Recent changes", Body: "build.go changed twice."},
}

// An attempt's context is recorded before its agent starts: held at its
// launch, the attempt's rows are committed and its context file holds
// every block's text, each row's digest the text's.
func TestAttemptContextRecordedBeforeTheAgentStarts(t *testing.T) {
	dir := newRepo(t)
	e, fl, l, cdir := contextEngine(t, twoBlocks)
	hold, held := make(chan struct{}), make(chan *session.Local, 1)
	fl.mu.Lock()
	fl.launchHold, fl.launchHeld = hold, held
	fl.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	<-held
	rows := rowsOf(l, run.ID, "build", 1)
	f := contextFileOf(t, cdir, run.ID, "build", 1)
	if len(rows) != 2 || len(f.Blocks) != 2 {
		t.Fatalf("at the launch: %d rows, %d bodies", len(rows), len(f.Blocks))
	}
	for i, r := range rows {
		b := f.Blocks[i]
		if r.Seq != i+1 || b.Seq != r.Seq || r.Phase != ledger.PhaseTask || r.Source != "graph" || r.Title != twoBlocks[i].Title || r.Pack != twoBlocks[i].Pack ||
			r.SHA256 != digest(b.Body) || r.Bytes != len(b.Body) || b.Body != twoBlocks[i].Body {
			t.Fatalf("block %d: row %+v, body %+v", i, r, b)
		}
	}
	close(hold)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// The rows go with the attempt's workspace: a workspace transaction that
// fails records none, and the attempt does not start (the stage
// worktree); its file's bodies, with no row, are never shown and are
// dropped by the next write of the attempt's context.
func TestAttemptContextInTheWorkspaceTransaction(t *testing.T) {
	dir := newRepo(t)
	e, fl, l, cdir := contextEngine(t, twoBlocks)
	l.mu.Lock()
	l.failOn, l.failOnce = "attempt.workspace", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ts := waitTaskWhere(t, e, run.ID, "build", "attempt 1 failed at the worktree", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N >= 1 && (ts.Attempt.N > 1 || ts.Attempt.State == ledger.AttemptFailed)
	})
	_ = ts
	if rows := rowsOf(l, run.ID, "build", 1); len(rows) != 0 {
		t.Fatalf("attempt 1's rows: %+v", rows)
	}
	fl.mu.Lock()
	for _, c := range fl.calls {
		if strings.Contains(c.name, "#1") {
			fl.mu.Unlock()
			t.Fatal("attempt 1's session was launched")
		}
	}
	fl.mu.Unlock()
	if body, ok, err := e.contextFiles.Body(run.ID, "build", 1, 1, digest(twoBlocks[0].Body)); err != nil || !ok || body != twoBlocks[0].Body {
		t.Fatalf("the orphan body is in the file: %q %v %v", body, ok, err)
	}
	// A late write of attempt 1 (as a lookup's) drops the orphans first.
	e.mu.Lock()
	r := e.runs[run.ID]
	a := r.task("build").attempts[0]
	e.mu.Unlock()
	a.ctxMu.Lock()
	rows, err := e.recordContext(context.Background(), r, a, ledger.PhaseLookup, twoBlocks[:1])
	a.ctxMu.Unlock()
	if err != nil || len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("the next write: %+v %v", rows, err)
	}
	if f := contextFileOf(t, cdir, run.ID, "build", 1); len(f.Blocks) != 1 || f.Blocks[0].Phase != ledger.PhaseLookup {
		t.Fatalf("the file after the next write: %+v", f.Blocks)
	}
	stopRun(t, e, run.ID)
}

// Blocks the task file moved to task-more.json are recorded with the
// rest: the context file holds every block the attempt was given.
func TestContextFileHoldsTheMovedBlocks(t *testing.T) {
	dir := newRepo(t)
	var blocks []TaskContext
	for i := range 3 {
		blocks = append(blocks, TaskContext{Source: "graph", Title: "Block " + string(rune('A'+i)), Body: strings.Repeat(string(rune('a'+i)), ext.MaxContextBody)})
	}
	e, _, l, cdir := contextEngine(t, blocks)
	c := repoCrew(dir, work("build", "lead"))
	c.Tasks[0].Instructions = strings.Repeat("i", 7000)
	c.Goal = strings.Repeat("g", 1900)
	run := launchManaged(t, e, c)
	ts := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, tf := taskFileOf(t, ts)
	if f := contextFileOf(t, cdir, run.ID, "build", 1); len(f.Blocks) != 3 || len(rowsOf(l, run.ID, "build", 1)) != 3 {
		t.Fatalf("the file holds %d blocks; the task file %d (more %q)", len(f.Blocks), len(tf.Context), tf.More)
	}
	stopRun(t, e, run.ID)
}

// A lookup's late pack is recorded against the attempt after its task's
// blocks, in a transaction of its own: its rows' seqs follow, phase
// lookup, the file holding both.
func TestLatePackRecordedAgainstTheAttempt(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, twoBlocks)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	pack := []ext.ContextBlock{{Title: "The pack", Body: "a block of the pack", Pack: "p-9"}}
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", pack); err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(l, run.ID, "build", 1)
	if len(rows) != 3 || rows[2].Seq != 3 || rows[2].Phase != ledger.PhaseLookup || rows[2].Pack != "p-9" || rows[2].SHA256 != digest("a block of the pack") {
		t.Fatalf("the rows: %+v", rows)
	}
	if f := contextFileOf(t, cdir, run.ID, "build", 1); len(f.Blocks) != 3 || f.Blocks[2].Body != "a block of the pack" {
		t.Fatalf("the file: %+v", f.Blocks)
	}
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 9, "graph", pack); !errors.Is(err, ErrContextNotRecorded) {
		t.Fatalf("an attempt that is not: %v", err)
	}
	stopRun(t, e, run.ID)
}

// Two lookups of one attempt are written one after the other: the first
// held between its read of the file and its write, the second waits for
// it, and both answers' blocks are in the file and the ledger.
func TestConcurrentLookupsKeepEveryBlock(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, twoBlocks)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	entered, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.mu.Lock()
	e.contextRead = func(phase string) {
		if phase == ledger.PhaseLookup {
			once.Do(func() {
				close(entered)
				<-gate
			})
		}
	}
	e.mu.Unlock()
	errs := make(chan error, 2)
	go func() {
		errs <- e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", []ext.ContextBlock{{Title: "First", Body: "one", Pack: "p-1"}})
	}()
	<-entered
	go func() {
		errs <- e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", []ext.ContextBlock{{Title: "Second", Body: "two", Pack: "p-2"}})
	}()
	close(gate)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	rows := rowsOf(l, run.ID, "build", 1)
	f := contextFileOf(t, cdir, run.ID, "build", 1)
	if len(rows) != 4 || len(f.Blocks) != 4 {
		t.Fatalf("%d rows, %d bodies", len(rows), len(f.Blocks))
	}
	for i, r := range rows {
		if f.Blocks[i].Seq != r.Seq || digest(f.Blocks[i].Body) != r.SHA256 {
			t.Fatalf("row %d %+v, body %+v", i, r, f.Blocks[i])
		}
	}
	stopRun(t, e, run.ID)
}

// A crash between the file's rename and the rows' commit leaves a body
// with no row: never shown, and dropped by the next write, every
// committed row keeping its body.
func TestCrashBetweenContextFileAndRows(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, twoBlocks)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	e.mu.Lock()
	e.contextCommit = func() error { return errors.New("the process stopped here") }
	e.mu.Unlock()
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", []ext.ContextBlock{{Title: "Lost", Body: "never recorded", Pack: "p-x"}}); !errors.Is(err, ErrContextNotRecorded) {
		t.Fatalf("the crashed write: %v", err)
	}
	if f := contextFileOf(t, cdir, run.ID, "build", 1); len(f.Blocks) != 3 || len(rowsOf(l, run.ID, "build", 1)) != 2 {
		t.Fatalf("after the crash: %d bodies, %d rows", len(f.Blocks), len(rowsOf(l, run.ID, "build", 1)))
	}
	e.mu.Lock()
	e.contextCommit = nil
	e.mu.Unlock()
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", []ext.ContextBlock{{Title: "Kept", Body: "recorded", Pack: "p-y"}}); err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(l, run.ID, "build", 1)
	f := contextFileOf(t, cdir, run.ID, "build", 1)
	if len(rows) != 3 || len(f.Blocks) != 3 || f.Blocks[2].Body != "recorded" || rows[2].Seq != 3 {
		t.Fatalf("after the next write: rows %+v, bodies %+v", rows, f.Blocks)
	}
	for i, r := range rows {
		if body, ok, err := e.contextFiles.Body(run.ID, "build", 1, r.Seq, r.SHA256); err != nil || !ok || digest(body) != r.SHA256 {
			t.Fatalf("row %d's body: %v %v", i, ok, err)
		}
	}
	stopRun(t, e, run.ID)
}

// The 65th block of an attempt's lookups is not recorded, and so not
// given: nothing of its answer is written. An attempt that has closed is
// given no block either.
func TestLookupContextFullAnswersNoBlocks(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, nil)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ts := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	eight := make([]ext.ContextBlock, ledger.MaxContextLookupOne)
	for i := range eight {
		eight[i] = ext.ContextBlock{Title: "B", Body: "b", Pack: "p"}
	}
	for i := range maxLookupContext / ledger.MaxContextLookupOne {
		if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", eight); err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
	}
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", eight[:1]); !errors.Is(err, ErrContextNotRecorded) {
		t.Fatalf("the 65th block: %v", err)
	}
	if rows := rowsOf(l, run.ID, "build", 1); len(rows) != maxLookupContext {
		t.Fatalf("%d rows", len(rows))
	}
	if f := contextFileOf(t, cdir, run.ID, "build", 1); len(f.Blocks) != maxLookupContext {
		t.Fatalf("%d bodies", len(f.Blocks))
	}
	submit(t, e, ts.Attempt.SessionID, Result{Summary: "done"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", eight[:1]); !errors.Is(err, ErrContextNotRecorded) {
		t.Fatalf("a closed attempt's block: %v", err)
	}
}

// A context file at its largest stays within maxContextFile: every task
// block and every lookup block at its bound, every byte of every text one
// JSON escapes in two (a quote), every number at its widest.
func TestContextFileBound(t *testing.T) {
	esc := func(n int) string { return strings.Repeat("\"", n) }
	var f contextFile
	f.V = contextFileVersion
	seq := 1 << 30
	add := func(phase string, body int) {
		f.Blocks = append(f.Blocks, contextEntry{Seq: seq, Phase: phase, Source: strings.Repeat("s", ext.MaxProviderName), Title: esc(ext.MaxContextTitle),
			Ref: esc(ext.MaxContextRef), Pack: esc(ext.MaxContextPack), Body: esc(body)})
	}
	// The task's blocks together are at most MaxTaskContext as a task file
	// keeps them: one block holds it, the rest the least a block holds.
	add(ledger.PhaseTask, ext.MaxTaskContext)
	for range ledger.MaxContextTaskRows - 1 {
		add(ledger.PhaseTask, 1)
	}
	for range ledger.MaxContextLookupRows {
		add(ledger.PhaseLookup, ext.MaxContextBody)
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxContextFile {
		t.Fatalf("the largest file is %d bytes, past %d", len(b), maxContextFile)
	}
	// One byte more than the bound is refused before it is written.
	cf, err := OpenContextFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	f.Blocks = append(f.Blocks, contextEntry{Seq: seq, Phase: ledger.PhaseLookup, Source: "s", Title: "t", Body: strings.Repeat("x", maxContextFile)})
	if err := cf.write("team-00000001", "build", 1, f); err == nil || !strings.Contains(err.Error(), "past") {
		t.Fatalf("a file past its bound: %v", err)
	}
}

// With no context given, no file and no row: an attempt of an engine that
// records context but was given none writes nothing.
func TestNoContextNoRows(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, nil)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := rowsOf(l, run.ID, "build", 1); len(rows) != 0 {
		t.Fatalf("rows: %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(cdir, "context")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the context folder: %v", err)
	}
	stopRun(t, e, run.ID)
}

// Around the workspace transaction: an attempt whose transaction is not
// committed has no row and never starts, and the task's next attempt is
// given its context afresh, from its own seq 1, in a file of its own;
// once committed, the rows stay with the attempt through the run's stop.
func TestCrashAroundTheWorkspaceTransaction(t *testing.T) {
	dir := newRepo(t)
	e, _, l, cdir := contextEngine(t, twoBlocks)
	l.mu.Lock()
	l.failOn, l.failOnce = "attempt.workspace", true
	l.mu.Unlock()
	c := repoCrew(dir, work("build", "lead"))
	run := launchManaged(t, e, c)
	waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if rows := rowsOf(l, run.ID, "build", 1); len(rows) != 0 {
		t.Fatalf("attempt 1's rows: %+v", rows)
	}
	rows := rowsOf(l, run.ID, "build", 2)
	if len(rows) != 2 || rows[0].Seq != 1 || contextFileOf(t, cdir, run.ID, "build", 2).Blocks[0].Body != twoBlocks[0].Body {
		t.Fatalf("attempt 2's rows: %+v", rows)
	}
	stopRun(t, e, run.ID)
	if rows := rowsOf(l, run.ID, "build", 2); len(rows) != 2 {
		t.Fatalf("after the stop: %+v", rows)
	}
}

// An engine built with nowhere to record the context gives an attempt
// none, whoever built it: no provider is asked, the task file holds no
// block, and a lookup's pack is not recorded, so the route withholds it.
func TestAnEngineWithNowhereToRecordGivesNoContext(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	var asked atomic.Int32
	e.SetContext(func(context.Context, ext.ContextQuery) []TaskContext {
		asked.Add(1)
		return slices.Clone(twoBlocks)
	})
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	ts := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if _, tf := taskFileOf(t, ts); len(tf.Context) != 0 || tf.More != "" {
		t.Fatalf("the task file's context: %+v %q", tf.Context, tf.More)
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("the provider was asked %d times", n)
	}
	if rows := rowsOf(l, run.ID, "build", 1); len(rows) != 0 {
		t.Fatalf("rows: %+v", rows)
	}
	pack := []ext.ContextBlock{{Title: "The pack", Body: "b", Pack: "p-1"}}
	if err := e.RecordLookupContext(context.Background(), run.ID, "build", 1, "graph", pack); !errors.Is(err, ErrContextNotRecorded) {
		t.Fatalf("a pack with nowhere to record it: %v", err)
	}
	stopRun(t, e, run.ID)
}
