package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A preview of a run the engine no longer holds (docs/plans/browser-
// evidence.md, "A run the engine no longer holds"; PV1 PR-7b): a run
// evicted past its class's cap, or one finished under an earlier server of
// this ledger. Get and TaskOf read the engine's runs alone, but the tree's
// machinery (resolveTree, the Runtime, the teardown, nextK) takes a *run
// and an *attempt; so OpenPreview builds a preview-only context of the run
// from its durable records (loadForPreview) and opens the preview in it as
// in a run it holds.
//
// The context is a *run that schedules nothing. It is kept in
// e.previewRuns, never in e.runs, so Get, TaskOf, List, the scheduler,
// evict, forget, a stop and ContinueRun never see it; it has no member,
// session or attempt of its own to start, and its context (ctx) is ended
// from the start. Its transactions write what its previews write (their
// rows, events and receipts) and never a row of its run, tasks or
// attempts (commitHeld). Its teardown is its own, beneath the engine's
// root, so the shutdown's cut-off ends it, and it is cancelled once the
// context is dropped: when its last preview has been let go
// (dropPreview), or when no preview of it was reserved.
//
// What it holds, read once as it is made: the crew as launched (its
// tasks, its project manifest's source, its revision, cwd), the prefix of
// cwd in its repository read again from the repository as a launch reads
// it, each attempt that submitted a result with its worktree and commit,
// and the Runtime chosen for the run's id by runtimeFor as a run's is
// (recorded in each preview's row as where_ran, and in its receipts). Read
// once a preview of an attempt is reserved, before anything is made: the
// attempt's receipt numbers (each id's largest k, so nextK goes on after
// them) and the seq of its last preview (previewSeeds). The refusals of
// the ledger's row are judged again in the preview's first transaction
// (preview.putRow): a run continued since it was read is refused there.

// previewReads is what a preview-only context reads of the ledger beyond
// Load: an attempt's receipts, without their logs, and the run's previews.
// The ledger's (sqlLedger) has both; a ledger without them opens no
// preview of a run the engine does not hold.
type previewReads interface {
	AttemptReceipts(ctx context.Context, runID, taskID string, n int) ([]ledger.CheckRow, error)
	Previews(ctx context.Context, runID string) ([]ledger.PreviewRow, error)
}

func (s sqlLedger) AttemptReceipts(ctx context.Context, runID, taskID string, n int) ([]ledger.CheckRow, error) {
	return s.l.Checks(ctx, runID, taskID, n, false)
}

func (s sqlLedger) Previews(ctx context.Context, runID string) ([]ledger.PreviewRow, error) {
	return s.l.Previews(ctx, runID)
}

// runRowTx is a transaction that reads its run's row (ledger.Tx.Run).
type runRowTx interface {
	Run() (ledger.RunRow, bool, error)
}

// errNoPreviewReads refuses a preview of a run the engine does not hold
// over a ledger that cannot read what the preview's context needs.
var errNoPreviewReads = errors.New("the ledger cannot read a run the engine does not hold for its preview")

// previewRun is the run a preview of runID opens in: the managed run the
// engine holds, as it is; else the preview-only context of it the engine
// keeps for a preview open now; else one built from the ledger
// (loadForPreview), kept from here (e.previewRuns) and dropped again when
// no preview of it is reserved (forgetPreviewRun). A run the ledger has no
// row of, or a legacy run, is ErrRunNotFound.
func (e *Engine) previewRun(ctx context.Context, runID string) (*run, error) {
	e.mu.Lock()
	if r := e.runs[runID]; r != nil {
		e.mu.Unlock()
		if !r.managed {
			return nil, ErrRunNotFound
		}
		return r, nil
	}
	if r := e.previewRuns[runID]; r != nil {
		e.mu.Unlock()
		return r, nil
	}
	e.mu.Unlock()
	made, err := e.loadForPreview(ctx, runID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.previewRuns[runID]; r != nil {
		made.teardownCancel() // another open made it first
		return r, nil
	}
	if r := e.runs[runID]; r != nil {
		made.teardownCancel()
		if !r.managed {
			return nil, ErrRunNotFound
		}
		return r, nil
	}
	if e.previewRuns == nil {
		e.previewRuns = map[string]*run{}
	}
	e.previewRuns[runID] = made
	return made, nil
}

// forgetPreviewRun drops r, a preview-only context, once no preview of it
// is reserved: out of e.previewRuns, its teardown cancelled (every tree of
// it has ended by then). Nothing for a run the engine holds, or one with a
// preview. The caller holds e.mu.
func (e *Engine) forgetPreviewRun(r *run) {
	if !r.previewOnly || r.previews > 0 {
		return
	}
	if e.previewRuns[r.id] == r {
		delete(e.previewRuns, r.id)
	}
	r.teardownCancel()
}

// previewHeld judges the context a preview is reserved or opened in, as
// e.continued judges a run the engine holds: a preview-only context the
// engine no longer keeps is ErrRunNotFound (dropped as the open looked it
// up: the open looks again), and its continuation is the ledger's to
// judge, in the preview's first transaction. The caller holds e.mu.
func (e *Engine) previewHeld(r *run) error {
	if !r.previewOnly {
		return e.continued(r)
	}
	if e.previewRuns[r.id] != r {
		return ErrRunNotFound
	}
	return nil
}

// loadForPreview builds the preview-only context of runID from the
// ledger: refused with nothing kept for a run the ledger has no row of
// (ErrRunNotFound), one it still has running (no other server's run is
// this one's to preview: ErrRunNotFound), one stopped before it finished
// (ErrRunStopped, as a stopped run the engine holds refuses), one
// continued (ErrRunContinued), a legacy crew (ErrRunNotFound), and a crew
// whose JSON or repository cannot be read. A run stopped once it had
// finished (its outcome kept) is previewed: a server's shutdown stops
// every run it holds, finished ones among them, so every run an earlier
// server finished is so.
func (e *Engine) loadForPreview(ctx context.Context, runID string) (*run, error) {
	if e.ledger == nil {
		return nil, ErrRunNotFound
	}
	if _, ok := e.ledger.(previewReads); !ok {
		return nil, errNoPreviewReads
	}
	from, ok, err := e.ledger.Load(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("the ledger could not read run %s: %w", runID, err)
	}
	if !ok {
		return nil, ErrRunNotFound
	}
	row := from.Run
	switch {
	case row.ContinuedBy != "":
		return nil, ErrRunContinued
	case row.State == ledger.RunRunning:
		return nil, ErrRunNotFound
	case row.State == ledger.RunStopped && row.Outcome == "":
		return nil, ErrRunStopped
	}
	var c Crew
	if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
		return nil, fmt.Errorf("the run's crew as launched cannot be read: %w", err)
	}
	if !c.Managed() {
		return nil, ErrRunNotFound
	}
	prefix := ""
	if c.Isolation == IsolationWorktree {
		if err := checkGit(); err != nil {
			return nil, err
		}
		if prefix, err = repoPrefix(ctx, c.Cwd); err != nil {
			return nil, fmt.Errorf("the run's repository %s: %w", c.Cwd, err)
		}
	}
	r := &run{managed: true, created: true, previewOnly: true, id: runID, crewID: c.ID, name: c.Name, goal: c.Goal, cwd: c.Cwd, isolation: c.Isolation, prefix: prefix,
		plan: slices.Clone(c.Tasks), base: row.Base, tasks: newTasks(c.Tasks), crewJSON: row.Crew, manifest: c.effectiveManifest(), manifestRev: c.ManifestRevision,
		revision: row.Revision, outcome: row.Outcome, changed: e.OnRunChange, startedAt: row.CreatedAt, prereq: prereqPassed, prereqK: map[string]int{}, prereqRows: map[string]prereqReceipt{}}
	// Nothing starts under its context: ended from the start.
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.cancel()
	root := e.teardownRoot
	if root == nil {
		root = context.Background()
	}
	r.teardown, r.teardownCancel = context.WithCancel(root)
	r.rt, r.taken = e.runtimeFor(ownLogins(c.Members), runID)
	where := r.rt.Where()
	// Each attempt that submitted a result, with its worktree: the
	// candidates. Their receipts run where the context's Runtime runs them.
	for _, ar := range from.Attempts {
		t := r.task(ar.TaskID)
		if t == nil || ar.ResultCommit == "" || ar.Worktree == "" {
			continue
		}
		closed := ar.ClosedAt
		if closed.IsZero() {
			closed = time.Now().UTC()
		}
		t.attempts = append(t.attempts, &attempt{t: t, n: ar.N, state: ar.State, stage: ar.Stage, worktree: ar.Worktree, branch: ar.Branch, start: ar.StartCommit,
			result: ar.ResultCommit, reservedAt: ar.ReservedAt, launchedAt: ar.LaunchedAt, closedAt: closed, where: where})
	}
	for _, t := range r.tasks {
		slices.SortFunc(t.attempts, func(x, y *attempt) int { return x.n - y.n })
	}
	return r, nil
}

// previewSeeds reads, for the attempt (taskID, n) of a run the engine does
// not hold, the largest k of each receipt id it has and the seq of its
// last preview: a preview's receipts are numbered after them, and its seq
// after its last, so no row of the ledger's is written over.
func (e *Engine) previewSeeds(ctx context.Context, runID, taskID string, n int) (map[string]int, int, error) {
	pr, ok := e.ledger.(previewReads)
	if !ok {
		return nil, 0, errNoPreviewReads
	}
	rows, err := pr.AttemptReceipts(ctx, runID, taskID, n)
	if err != nil {
		return nil, 0, fmt.Errorf("the ledger could not read the attempt's receipts: %w", err)
	}
	ks := map[string]int{}
	for _, c := range rows {
		if c.TaskID == taskID && c.N == n {
			ks[c.ID] = max(ks[c.ID], c.K)
		}
	}
	previews, err := pr.Previews(ctx, runID)
	if err != nil {
		return nil, 0, fmt.Errorf("the ledger could not read the run's previews: %w", err)
	}
	seq := 0
	for _, p := range previews {
		if p.TaskID == taskID && p.N == n {
			seq = max(seq, p.Seq)
		}
	}
	return ks, seq, nil
}

// seedPreview gives p's attempt, in a preview-only context, its receipt
// numbers and its last seq as the ledger has them (previewSeeds), read
// once p is reserved, so no preview of the attempt is being made or ended
// meanwhile: an earlier one's last receipt is written before its end is,
// and its reservation is let go only after that. It refuses an attempt
// opened ledger.MaxPreviews times (ErrPreviewLimit).
func (e *Engine) seedPreview(ctx context.Context, p *preview) error {
	ks, seq, err := e.previewSeeds(ctx, p.r.id, p.key.task, p.key.n)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := p.a
	if a.ks == nil {
		a.ks = map[string]int{}
	}
	for id, k := range ks {
		a.ks[id] = max(a.ks[id], k)
	}
	a.previewSeq = max(a.previewSeq, seq)
	if a.previewSeq >= ledger.MaxPreviews {
		return ErrPreviewLimit
	}
	return nil
}

// judgeLedgerRun judges, in a preview-only context's first transaction of
// a preview, the run's row as the ledger has it now: one continued since
// the context was read is refused (ErrRunContinued), as a run the engine
// holds is refused by its mark. A transaction that cannot read its run's
// row refuses the preview.
func judgeLedgerRun(ltx LedgerTx) error {
	rt, ok := ltx.(runRowTx)
	if !ok {
		return errNoPreviewReads
	}
	row, found, err := rt.Run()
	switch {
	case err != nil:
		return err
	case !found:
		return ErrRunNotFound
	case row.ContinuedBy != "":
		return ErrRunContinued
	}
	return nil
}
