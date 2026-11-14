package crew

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A candidate's preview checkout (docs/plans/browser-evidence.md, PV1
// PR-7): a person looks at an attempt's result served as the run's
// project manifest serves a tree, after the attempt has closed and its own
// worktree's preview went with it. The checkout is <worktree>.preview,
// detached at the result's commit, and a tree of kind preview whose owner
// is the preview (treeOwner), never the attempt: it is listed in the
// engine's previews (never a.trees), its rows are the previews table's by
// seq (never the trees'), its steps are receipts of the attempt (the
// checks table, the attempt's next k, the checkout's dir).
//
// The order: the open's first transaction (preview.opened, the row
// resolved) leases the tree's ports before the checkout is added or any
// step runs; each step is a receipt before its process starts; ready is
// committed to the row of its seq (preview.ready) before the preview is
// offered; an end's first transaction (the URL cleared, the preview
// closed: closePreview) is committed before any service is signalled, so
// nothing offers a URL a teardown is ending and no late step of that seq
// writes its row; the ports are let go once the end's last transaction
// (preview.ended) is committed. The run is held against eviction
// (r.previews) from the reservation to that last transaction, so nothing
// but the preview's own end stops its services.
//
// This row opens a preview of a run the engine holds; a run it no longer
// holds (evicted, or an earlier server's) is refused as not found.

// The bounds of the previews: at most maxPreviews open on the server, one
// per candidate at a time, ledger.MaxPreviews opens of a candidate, and
// each ended once previewIdle has passed with no read of it (ReadPreview).
const (
	maxPreviews = 2
	previewIdle = 30 * time.Minute
)

// A preview's states as reported (Previewing, PreviewState): the row's
// resolved and ready, and ending from its end's first transaction on.
const (
	previewEnding = "ending"
	previewEnded  = ledger.PreviewEnded
)

// Why a preview ended, its row's why: closed by a person, idle past
// previewIdle, a service of it that exited after it was ready, its run's
// stop, the server's shutdown; a preview that could not be made ready says
// where in words of its own (servePreview).
const (
	whyPreviewClosed = "closed"
	whyPreviewIdle   = "idle"
	whyServiceExited = "service_exited"
	whyRunStopped    = "run_stopped"
	whyShutdown      = "shutdown"
)

// Errors of the previews.
var (
	// ErrNoCandidate refuses a preview of an attempt with no result whose
	// commit is known (a review's, or one that submitted nothing).
	ErrNoCandidate = errors.New("the attempt has no result to preview")
	// ErrNoPreviewManifest refuses a preview of a run without a project
	// manifest: nothing would serve it.
	ErrNoPreviewManifest = errors.New("the run has no project manifest, and nothing serves a preview of its results")
	// ErrPreviewOpen refuses a second preview of a candidate while one is
	// open.
	ErrPreviewOpen = errors.New("the candidate's preview is open already")
	// ErrPreviewLimit refuses a candidate opened ledger.MaxPreviews times.
	ErrPreviewLimit = fmt.Errorf("the candidate was opened in a preview %d times, the most it is", ledger.MaxPreviews)
	// ErrPreviewsBusy is what a PreviewsBusyError is (errors.Is).
	ErrPreviewsBusy = fmt.Errorf("the server has %d previews open, the most it keeps", maxPreviews)
	// ErrPreviewNotFound says no such preview is open.
	ErrPreviewNotFound = errors.New("no such preview is open")
	// ErrPreviewRefused refuses a checkout that is there already but not
	// as the commit has it: Conductor removes nothing it made.
	ErrPreviewRefused = errors.New("the preview's checkout is refused")
)

// PreviewRef names a preview: its run, task, attempt and seq.
type PreviewRef struct {
	Run  string `json:"run"`
	Task string `json:"task"`
	N    int    `json:"attempt"`
	Seq  int    `json:"seq"`
}

// PreviewsBusyError refuses a preview past maxPreviews, naming the open
// ones.
type PreviewsBusyError struct{ Open []PreviewRef }

func (e *PreviewsBusyError) Error() string { return ErrPreviewsBusy.Error() }

func (e *PreviewsBusyError) Is(target error) bool { return target == ErrPreviewsBusy }

// Previewing is a preview checkout of an attempt while it is open, as
// AttemptState reports it: its seq, its state (resolved, ready, ending)
// and its URL while ready.
type Previewing struct {
	Seq     int    `json:"seq"`
	State   string `json:"state"`
	Preview string `json:"preview,omitempty"`
}

// PreviewState is a preview as the engine gives it to the server's
// routes: its name, its state and URL as Previewing says them, its
// checkout (Dir, under the run's working directory Root), its commit, who
// opened it, where it runs, and its tree's secret, which only the label
// route puts into the admin's URL.
type PreviewState struct {
	PreviewRef
	State, Preview               string
	Root, Dir, Commit, By, Where string
	Secret                       string
}

// previewKey is a candidate: one preview of it is open at a time.
type previewKey struct {
	run, task string
	n         int
}

// preview is a candidate's preview checkout as the engine holds it, from
// its reservation (OpenPreview) to its end's last commit (finishPreview).
// Under e.mu: seq, set as its first transaction makes the tree its own
// (own); t, its tree from then on; state, "" until then, resolved, ready
// once its readiness is recorded, ending from its end's first
// transaction, ended once dropped; url, its tree's preview while ready;
// why it ends; read, when it was last read (ReadPreview). ctx is what its
// steps run under, beneath the run's teardown, cancelled once its end's
// first transaction is committed.
type preview struct {
	e        *Engine
	key      previewKey
	r        *run
	a        *attempt
	by       string
	dir, cwd string
	seq      int
	t        *tree
	state    string
	url, why string
	read     time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

// The preview as its tree's owner (treeOwner). The caller holds e.mu.

func (p *preview) closed() bool { return p.state == previewEnding || p.state == previewEnded }

func (p *preview) own(t *tree) {
	p.t = t
	p.a.previewSeq++
	p.seq, p.state = p.a.previewSeq, ledger.PreviewResolved
}

func (p *preview) disown(t *tree) {
	if p.t == t {
		p.t, p.state, p.seq = nil, "", 0
		p.a.previewSeq--
	}
}

// putRow is the previews table's writer of the row's state, its seq's
// row: never PutTree, so the result checkout's trees row is never touched.
func (p *preview) putRow(row ledger.TreeRow) func(LedgerTx) error {
	pr := ledger.PreviewRow{TaskID: row.TaskID, N: row.N, Seq: p.seq, Dir: row.Dir, Commit: row.Commit, Manifest: row.Manifest, Ports: maps.Clone(row.Ports),
		Preview: row.Preview, Why: row.Why, Interrupted: row.Interrupted, EndedAt: row.EndedAt, OpenedBy: p.by, Where: string(p.r.rt.Where())}
	switch row.State {
	case ledger.TreeResolved:
		return func(ltx LedgerTx) error { return ltx.OpenPreview(pr) }
	case ledger.TreeReady:
		return func(ltx LedgerTx) error { return ltx.PreviewReady(pr) }
	}
	return func(ltx LedgerTx) error { return ltx.EndPreview(pr) }
}

func (p *preview) lifecycle(t *tree, state string, more map[string]any) []event {
	kind := map[string]string{ledger.TreeResolved: "preview.opened", ledger.TreeReady: "preview.ready", ledger.TreeEnded: "preview.ended"}[state]
	payload := map[string]any{"task": p.a.t.def.ID, "attempt": p.a.n, "seq": p.seq, "preview": "", "why": ""}
	maps.Copy(payload, more)
	if state != ledger.TreeReady {
		payload["preview"] = ""
	}
	if state == ledger.TreeResolved {
		payload["by"] = p.by
	}
	return []event{{kind, payload}}
}

func (p *preview) ready(t *tree, on bool) {
	if on {
		p.state, p.url = ledger.PreviewReady, t.preview
		return
	}
	p.state, p.url = ledger.PreviewResolved, ""
}

// serviceLost ends the preview: its URL withdrawn in the change that
// records the service's end, its end begun once that is committed.
func (p *preview) serviceLost(r *run, t *tree) (string, func()) {
	if p.closed() {
		return "", nil
	}
	p.url = ""
	return "; the preview is ended", func() { p.e.closePreview(p, whyServiceExited) }
}

// previewing is p as AttemptState reports it, nil before its first
// transaction. The caller holds e.mu.
func (p *preview) previewing() *Previewing {
	if p == nil || p.state == "" || p.state == previewEnded {
		return nil
	}
	return &Previewing{Seq: p.seq, State: p.state, Preview: p.url}
}

// stateOf is p as the routes read it. The caller holds e.mu.
func (p *preview) stateOf() PreviewState {
	ps := PreviewState{PreviewRef: PreviewRef{Run: p.key.run, Task: p.key.task, N: p.key.n, Seq: p.seq}, Root: p.r.cwd, Dir: p.dir, Commit: p.a.result, By: p.by, Where: string(p.r.rt.Where())}
	if pv := p.previewing(); pv != nil {
		ps.State, ps.Preview = pv.State, pv.Preview
	}
	if p.t != nil {
		ps.Secret = p.t.secret
	}
	return ps
}

// OpenPreview opens a preview checkout of attempt n of the task, which
// submitted a result whose commit is known, in a managed run the engine
// holds and that is not stopped or continued (ErrRunContinued), whose
// project manifest serves it. In order: the candidate reserved, within the
// bounds (one per candidate, ErrPreviewOpen; maxPreviews on the server, a
// PreviewsBusyError naming the open ones; ledger.MaxPreviews opens of it,
// ErrPreviewLimit); a checkout at <worktree>.preview that is there
// already taken only when it is at the commit with no tracked change, and
// refused otherwise with its path and the changed files named
// (ErrPreviewRefused), nothing of it removed; the run's manifest resolved
// for the commit with the preview as the tree's owner (resolveTree), whose
// transaction is the open's first (preview.opened, its row resolved);
// then the checkout added detached at the commit when it was not there,
// with its state directory; then its phases (prepareTree: setup, services,
// ready, its readiness written to its row) and its idle end, on a
// goroutine the run's stop waits for. It returns the preview as it is
// once its checkout is made: resolved, its readiness to come.
func (e *Engine) OpenPreview(ctx context.Context, runID, taskID string, n int, by string) (PreviewState, error) {
	e.mu.Lock()
	p, err := e.reservePreview(runID, taskID, n, by)
	e.mu.Unlock()
	if err != nil {
		return PreviewState{}, err
	}
	r, a := p.r, p.a
	unreserve := func(err error) (PreviewState, error) {
		e.mu.Lock()
		e.dropPreview(p)
		e.mu.Unlock()
		p.cancel()
		return PreviewState{}, err
	}
	reused, err := previewReusable(ctx, r.cwd, p.dir, a.result)
	if err != nil {
		return unreserve(err)
	}
	tr, err := e.resolveTree(ctx, r, a, p, ledger.TreePreview, p.dir, p.cwd, a.result)
	switch {
	case errors.Is(err, errAttemptClosed):
		return unreserve(fmt.Errorf("%w: the run was stopped as the preview opened", ErrRunStopped))
	case err != nil:
		return unreserve(fmt.Errorf("the preview's manifest: %w", err))
	}
	// The preview has its row from here: every way out ends it.
	if err := e.makePreviewCheckout(p.ctx, r, p, reused); err != nil {
		tr.busy.Done()
		words := session.CleanMessage(err.Error())
		e.closePreview(p, "the checkout could not be made: "+words)
		return PreviewState{}, fmt.Errorf("the preview's checkout %s: %w", p.dir, err)
	}
	e.mu.Lock()
	tr.unmade = false
	serve := !r.stopping && !p.closed()
	if serve {
		r.checks.Add(1)
	}
	ps := p.stateOf()
	e.mu.Unlock()
	if !serve {
		tr.busy.Done() // its end, begun meanwhile, goes on
		return ps, nil
	}
	go e.servePreview(p, tr)
	return ps, nil
}

// reservePreview reserves the candidate's preview, within the bounds, and
// counts it on its run (r.previews), which holds the run against eviction
// until the preview is dropped. The caller holds e.mu.
func (e *Engine) reservePreview(runID, taskID string, n int, by string) (*preview, error) {
	r := e.runs[runID]
	if r == nil || !r.managed {
		return nil, ErrRunNotFound
	}
	if err := e.continued(r); err != nil {
		return nil, err
	}
	if r.stopping || r.stoppedAt != nil {
		return nil, ErrRunStopped
	}
	t := r.task(taskID)
	if t == nil {
		return nil, ErrTaskNotFound
	}
	var a *attempt
	for _, x := range t.attempts {
		if x.n == n {
			a = x
		}
	}
	switch {
	case a == nil:
		return nil, fmt.Errorf("%w: the task has no attempt %d", ErrTaskNotFound, n)
	case a.result == "" || a.worktree == "" || t.def.Kind == TaskReview:
		return nil, ErrNoCandidate
	case !r.hasManifest():
		return nil, ErrNoPreviewManifest
	}
	key := previewKey{runID, taskID, n}
	if e.previews[key] != nil {
		return nil, ErrPreviewOpen
	}
	if len(e.previews) >= maxPreviews {
		busy := &PreviewsBusyError{}
		for _, o := range e.previews {
			busy.Open = append(busy.Open, PreviewRef{Run: o.key.run, Task: o.key.task, N: o.key.n, Seq: o.seq})
		}
		return nil, busy
	}
	if a.previewSeq >= ledger.MaxPreviews {
		return nil, ErrPreviewLimit
	}
	dir := a.worktree + ".preview"
	p := &preview{e: e, key: key, r: r, a: a, by: by, dir: dir, cwd: filepath.Join(dir, r.prefix), read: e.now()}
	p.ctx, p.cancel = context.WithCancel(r.teardown)
	if e.previews == nil {
		e.previews = map[previewKey]*preview{}
	}
	e.previews[key] = p
	r.previews++
	a.previewing = p
	r.touch()
	return p, nil
}

// dropPreview lets go of p: out of the engine's previews and its run's
// count, its attempt's no more. Once only. The caller holds e.mu.
func (e *Engine) dropPreview(p *preview) {
	if e.previews[p.key] == p {
		delete(e.previews, p.key)
		p.r.previews--
	}
	if p.a.previewing == p {
		p.a.previewing = nil
	}
	p.state = previewEnded
	p.r.touch()
}

// previewReusable reports whether a checkout is at dir to be taken as it
// is: none (false), or a worktree the repository at repo lists, at commit
// with no tracked change (true); any other is refused with its path and
// what is wrong, and nothing of it is removed. git is run in it only once
// the repository lists it, so a directory someone else made there, with a
// .git of its own, is never read. Untracked files, a dev server's caches,
// are left as they are.
func previewReusable(ctx context.Context, repo, dir, commit string) (bool, error) {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !fi.IsDir():
		return false, fmt.Errorf("%w: %s is there and is not a directory; nothing of it is removed", ErrPreviewRefused, dir)
	}
	switch listed, err := listedWorktree(ctx, repo, fi); {
	case err != nil:
		return false, fmt.Errorf("%w: the repository's worktrees could not be listed: %s", ErrPreviewRefused, session.CleanMessage(err.Error()))
	case !listed:
		return false, fmt.Errorf("%w: %s is there and is not a worktree of the repository; nothing of it is removed", ErrPreviewRefused, dir)
	}
	moved, changed, _, err := checkoutIntact(ctx, dir, commit)
	switch {
	case err != nil:
		return false, fmt.Errorf("%w: %s could not be compared with %s: %s; nothing of it is removed", ErrPreviewRefused, dir, short(commit), session.CleanMessage(err.Error()))
	case moved:
		return false, fmt.Errorf("%w: %s is not at %s; nothing of it is removed", ErrPreviewRefused, dir, short(commit))
	case len(changed) > 0:
		return false, fmt.Errorf("%w: %s has changes to tracked files: %s; nothing of it is removed", ErrPreviewRefused, dir, changedList(changed))
	}
	return true, nil
}

// listedWorktree reports whether the directory fi is a worktree git lists
// for the repository at repo (git worktree list), compared as files, not
// as paths a link could spell twice.
func listedWorktree(ctx context.Context, repo string, fi fs.FileInfo) (bool, error) {
	out, err := gitcli.Run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(out) {
		if p, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "worktree "); ok {
			if wi, err := os.Lstat(p); err == nil && os.SameFile(fi, wi) {
				return true, nil
			}
		}
	}
	return false, nil
}

// makePreviewCheckout makes p's checkout usable to its steps: added
// detached at the commit unless reused, checked at it, the crew's
// directory in it as in the worktree (checkMemberDir), and its state
// directory with .conductor's .gitignore (makeStateDir).
func (e *Engine) makePreviewCheckout(ctx context.Context, r *run, p *preview, reused bool) error {
	commit := p.a.result
	if !reused {
		e.gitMu.Lock()
		err := AddDetachedWorktree(ctx, r.cwd, p.dir, commit)
		e.gitMu.Unlock()
		if err != nil {
			return err
		}
		if head, err := headCommit(ctx, p.dir); err != nil || head != commit {
			return fmt.Errorf("the checkout is at %s, not the result %s", short(head), short(commit))
		}
	}
	if err := checkMemberDir(p.dir, r.prefix); err != nil {
		return err
	}
	if err := os.MkdirAll(p.cwd, 0o755); err != nil {
		return err
	}
	return makeStateDir(p.cwd, true)
}

// servePreview runs p's phases on its tree, held, under p.ctx (setup,
// services, ready probes, its readiness: prepareTree), lets go of the
// tree, and then ends p once previewIdle has passed with no read of it;
// one that could not be made ready is ended with where and why. It runs
// counted in r.checks, which the run's stop waits for once it has begun
// the end of its previews.
func (e *Engine) servePreview(p *preview, tr *tree) {
	defer p.r.checks.Done()
	stage, err := e.prepareTree(p.ctx, p.r, p.a, tr)
	tr.busy.Done()
	if err != nil {
		e.closePreview(p, fmt.Sprintf("not ready at %s: %s", stage, session.CleanMessage(err.Error())))
		return
	}
	for {
		e.mu.Lock()
		read := p.read
		e.mu.Unlock()
		left := previewIdle - e.now().Sub(read)
		if left <= 0 {
			e.closePreview(p, whyPreviewIdle)
			return
		}
		select {
		case <-e.previewTimer(left):
		case <-p.ctx.Done():
			return
		}
	}
}

// previewTimer is the idle end's timer: time.After, or a test's.
func (e *Engine) previewTimer(d time.Duration) <-chan time.Time {
	if e.idleTimer != nil {
		return e.idleTimer(d)
	}
	return time.After(d)
}

// closePreview begins p's end, once, for why: its first transaction clears
// its URL and closes it as its tree's owner (its row's preview cleared,
// why written), committed before anything of it is signalled; then its
// steps' context ends, and its tree's end (endTree, on a goroutine counted
// in r.trees, which Drain and the run's stop wait for) stops its
// services, runs its cleanup and records preview.ended, its ports let go
// after that commit; then it is dropped (finishPreview). A preview whose
// first transaction never committed has nothing to end and is dropped by
// its open. A first transaction the ledger refused is carried by the
// run's next commit, and the end goes on.
func (e *Engine) closePreview(p *preview, why string) {
	r := p.r
	began := false
	_ = e.transact(context.Background(), r, func() tx {
		if p.closed() {
			return tx{}
		}
		began = true
		p.state, p.url, p.why = previewEnding, "", why
		if p.t == nil {
			return tx{}
		}
		row := ledger.PreviewRow{TaskID: p.a.t.def.ID, N: p.a.n, Seq: p.seq, Why: session.CleanMessage(why)}
		return tx{more: func(ltx LedgerTx) error { return ltx.PreviewEnding(row) }}
	}, nil)
	if !began {
		return
	}
	p.cancel()
	e.mu.Lock()
	if p.t == nil {
		e.mu.Unlock()
		return // its open drops it, its first transaction refused
	}
	r.trees.add()
	e.mu.Unlock()
	go e.finishPreview(p)
}

// finishPreview ends p's tree (endTree, its owner the preview) and drops p
// once that end's last transaction has returned.
func (e *Engine) finishPreview(p *preview) {
	e.endTree(p.r, treeEnd{t: p.t, why: p.why})
	e.mu.Lock()
	e.dropPreview(p)
	e.mu.Unlock()
}

// ClosePreview ends the preview seq of attempt n of the task (Close): its
// end begun, the rest of it on its own (closePreview).
func (e *Engine) ClosePreview(runID, taskID string, n, seq int) error {
	e.mu.Lock()
	p := e.previews[previewKey{runID, taskID, n}]
	ok := p != nil && p.seq == seq && !p.closed()
	e.mu.Unlock()
	if !ok {
		return ErrPreviewNotFound
	}
	e.closePreview(p, whyPreviewClosed)
	return nil
}

// ReadPreview is the preview seq of attempt n of the task while it is
// open, for its label: a read, which puts its idle end previewIdle after
// it. ok is false for a preview not open (none, another seq, ending).
func (e *Engine) ReadPreview(runID, taskID string, n, seq int) (PreviewState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.previews[previewKey{runID, taskID, n}]
	if p == nil || p.seq != seq || seq == 0 || p.closed() || p.r.stopping {
		return PreviewState{}, false
	}
	p.read = e.now()
	return p.stateOf(), true
}

// endPreviews begins the end of every preview of r (every preview, for a
// nil r) with why, and returns once each end's first transaction is in.
func (e *Engine) endPreviews(r *run, why string) {
	e.mu.Lock()
	var ps []*preview
	for _, p := range e.previews {
		if r == nil || p.r == r {
			ps = append(ps, p)
		}
	}
	e.mu.Unlock()
	for _, p := range ps {
		e.closePreview(p, why)
	}
}
