package crew

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
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
// The order: the reservation, under the run's txMu, takes the result's
// commit, one the ledger holds, and every later step uses that copy; the
// open's first transaction (preview.opened, the row resolved) judges again
// that the result is that commit and the run neither continued nor
// stopping, and leases the tree's ports before the checkout is added or
// any step runs; the checkout is made under a bound of its own, which no
// end cuts, and made as the checks' checkout is, by its path
// (makePreviewCheckout), a found one reused only as a worktree of the
// run's repository (previewReusable); each step is a receipt before its process
// starts; ready is committed to the row of its seq (preview.ready) before
// the preview is offered, its URL shown from that commit on. A Close while
// the open is under way is answered busy (ErrPreviewOpening). An end is of
// two kinds. One a person can ask again (a Close, the idle end) commits
// its first transaction (the URL cleared, the preview ending and its
// tree's end counted: beginEnd) before any of it is signalled, and a
// refused one leaves the preview as it was. Every other (an open that
// failed, a service lost, the run's stop, the server's drain) happens
// whatever the ledger answers (endNow): the preview ended in memory at
// once, its teardown begun, its first write tried once, its last, refused,
// owed and tried again until it is written, the engine ends or the ledger
// is closed (finishPreview). The ports are let go once the end's last
// transaction (preview.ended) is committed, or the owed write is no longer
// tried. The run is held against eviction (r.previews) from the
// reservation until then, so nothing but the preview's own end stops its
// services.
//
// A run the engine holds is previewed as it is; one it no longer holds
// (evicted, or an earlier server's) in a preview-only context built from
// the ledger (preview_context.go), which schedules nothing.

// The bounds of the previews: at most maxPreviews open on the server, one
// per candidate at a time, ledger.MaxPreviews opens of a candidate, and
// each ended once previewIdle has passed with no read of it (ReadPreview).
// previewRetry is how long an owed write of a preview's end waits before
// it is tried again (finishPreview), until the engine ends.
const (
	maxPreviews  = 2
	previewIdle  = 30 * time.Minute
	previewRetry = 10 * time.Second
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
	// ErrPreviewOpening answers a Close of a preview whose open is under
	// way: nothing of it changes, and it can be closed once it is open.
	ErrPreviewOpening = errors.New("the preview is still opening: close it once it is open")
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
// opened it, where it runs, its run's crew, and its tree's secret, which
// only the label route puts into the admin's URL.
type PreviewState struct {
	PreviewRef
	State, Preview               string
	Root, Dir, Commit, By, Where string
	Crew                         string
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
// why it ends; read, when it was last read (ReadPreview). commit is the
// result's commit as the reservation took it, the one commit of the
// preview. ctx is what its steps run under, beneath the run's teardown,
// cancelled once its end has begun. Under e.mu too: opening, from the
// reservation until the open hands the preview over, while an end that
// must happen leaves the start of its teardown to the open (handoff, its
// first write's try); after, the reason of such an end that came while an
// asked end's first write was in flight (beginEnd).
type preview struct {
	e        *Engine
	key      previewKey
	r        *run
	a        *attempt
	by       string
	commit   string
	dir, cwd string
	seq      int
	t        *tree
	state    string
	url, why string
	read     time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	opening  bool
	handoff  chan struct{}
	after    string
}

// The preview as its tree's owner (treeOwner). The caller holds e.mu.

func (p *preview) closed() bool { return p.state == previewEnding || p.state == previewEnded }

// refused judges the open's first transaction again, in its change: the
// run continued or being so (as a command's change reads it), stopping, or
// the attempt's result no longer the commit the reservation took.
func (p *preview) refused() error {
	switch err := p.e.previewHeld(p.r); {
	case err != nil:
		return err
	case p.r.stopping:
		return fmt.Errorf("%w: the run was stopped as the preview opened", ErrRunStopped)
	case p.a.result != p.commit:
		return fmt.Errorf("%w: its result is no longer %s", ErrNoCandidate, short(p.commit))
	}
	return nil
}

func (p *preview) own(t *tree) {
	p.t = t
	p.a.previewSeq++
	p.seq, p.state = p.a.previewSeq, ledger.PreviewResolved
}

func (p *preview) disown(t *tree) {
	if p.t == t {
		p.t, p.seq = nil, 0
		if !p.closed() {
			p.state = ""
		}
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
		if p.r.previewOnly {
			// The run's row judged again in this transaction: a run the
			// engine does not hold is continued by no mark of its own.
			return func(ltx LedgerTx) error {
				if err := judgeLedgerRun(ltx); err != nil {
					return err
				}
				return ltx.OpenPreview(pr)
			}
		}
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

// ready offers the preview's URL only once its readiness is committed
// (the change's done), never in the change itself: a reader is never
// shown a URL whose readiness the ledger may yet refuse, nor one whose end
// began meanwhile.
func (p *preview) ready(t *tree, on bool) func() {
	if !on {
		return nil
	}
	return func() {
		if !p.closed() {
			p.state, p.url = ledger.PreviewReady, t.preview
		}
	}
}

// serviceLost ends the preview in the change that records the service's
// end, whatever the ledger answers (endNow).
func (p *preview) serviceLost(r *run, t *tree) string {
	if p.closed() {
		return ""
	}
	p.e.endNow(p, whyServiceExited)
	return "; the preview is ended"
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
	ps := PreviewState{PreviewRef: PreviewRef{Run: p.key.run, Task: p.key.task, N: p.key.n, Seq: p.seq}, Root: p.r.cwd, Dir: p.dir, Commit: p.commit, By: p.by, Where: string(p.r.rt.Where()), Crew: p.r.crewID}
	if pv := p.previewing(); pv != nil {
		ps.State, ps.Preview = pv.State, pv.Preview
	}
	if p.t != nil {
		ps.Secret = p.t.secret
	}
	return ps
}

// OpenPreview opens a preview checkout of attempt n of the task, which
// submitted a result whose commit is known, in a managed run that is not
// stopped or continued (ErrRunContinued), whose project manifest serves
// it: one the engine holds, or one the ledger has, finished or interrupted,
// in a preview-only context (previewRun; its attempt's receipt numbers and
// last seq read from the ledger once reserved, seedPreview). In order: the
// candidate reserved, under the
// run's txMu, so a change under way commits or is undone first, within the
// bounds (one per candidate, ErrPreviewOpen; maxPreviews on the server, a
// PreviewsBusyError naming the open ones; ledger.MaxPreviews opens of it,
// ErrPreviewLimit), the result's commit copied into the preview, the one
// commit every later step uses. Its checkout, <worktree>.preview, is used
// by its path under the run's worktrees folder: the worktrees folder
// judged as an attempt's dispatch judges it (checkWorktreesDir); a
// checkout there already taken only as a worktree of the run's
// repository, at its own top, at the commit with no tracked change
// (previewReusable), and refused otherwise with its path and what is
// wrong (ErrPreviewRefused), nothing of it removed; the run's manifest
// resolved for the commit with the preview as the tree's owner
// (resolveTree), whose transaction is the open's first (preview.opened,
// its row resolved), refused, nothing written, for a run continued or
// stopping or a result that is no longer the commit (refused); then the
// checkout added detached at the commit when it was not there, and the
// crew's directory and its state directory made in it
// (makePreviewCheckout), under previewCheckoutTimeout, which no end cuts;
// a checkout that cannot be made ends the preview whatever the ledger
// answers (endNow); then its phases (prepareTree: setup, services, ready,
// its readiness written to its row) and its idle end, on a goroutine the
// run's stop waits for. It returns the preview as it is once its checkout
// is made: resolved, its readiness to come.
func (e *Engine) OpenPreview(ctx context.Context, runID, taskID string, n int, by string) (PreviewState, error) {
	var r *run
	var p *preview
	// A run evicted, or a preview-only context dropped, between its lookup
	// and the reservation is looked up again: from the ledger, the second
	// time.
	for try := 1; ; try++ {
		var err error
		if r, err = e.previewRun(ctx, runID); err != nil {
			return PreviewState{}, err
		}
		if e.afterLookup != nil {
			e.afterLookup(runID)
		}
		r.txMu.Lock()
		e.mu.Lock()
		p, err = e.reservePreview(r, taskID, n, by)
		if err != nil {
			e.forgetPreviewRun(r)
		}
		e.mu.Unlock()
		r.txMu.Unlock()
		if errors.Is(err, ErrRunNotFound) && try < 3 {
			continue
		}
		if err != nil {
			return PreviewState{}, err
		}
		break
	}
	a := p.a
	unreserve := func(err error) (PreviewState, error) {
		e.mu.Lock()
		if p.handoff != nil {
			r.trees.done() // counted by an end begun as its first transaction was refused
		}
		e.dropPreview(p)
		e.mu.Unlock()
		p.cancel()
		return PreviewState{}, err
	}
	if r.previewOnly {
		if err := e.seedPreview(ctx, p); err != nil {
			return unreserve(err)
		}
	}
	if err := checkWorktreesDir(r.cwd); err != nil {
		return unreserve(err)
	}
	reused, err := previewReusable(ctx, r.cwd, p.dir, p.commit)
	if err != nil {
		return unreserve(err)
	}
	tr, err := e.resolveTree(ctx, r, a, p, ledger.TreePreview, p.dir, p.cwd, p.commit)
	switch {
	case errors.Is(err, errAttemptClosed):
		return unreserve(fmt.Errorf("%w: the run was stopped as the preview opened", ErrRunStopped))
	case errors.Is(err, ErrRunContinued), errors.Is(err, ErrRunNotFound), errors.Is(err, ErrRunStopped), errors.Is(err, ErrNoCandidate):
		return unreserve(err) // refused in the first transaction's change
	case err != nil:
		return unreserve(fmt.Errorf("the preview's manifest: %w", err))
	}
	// The preview has its row from here: every way out ends it.
	bound := e.checkoutBound
	if bound == nil {
		bound = func() (context.Context, context.CancelFunc) {
			return context.WithTimeoutCause(context.Background(), previewCheckoutTimeout, errCheckoutBound)
		}
	}
	mk, cancel := bound()
	err = e.makePreviewCheckout(mk, r, p, reused)
	if cause := context.Cause(mk); err != nil && cause != nil {
		err = cause // the bound's own words, not those of the git it cut
	}
	cancel()
	if e.checkoutMade != nil {
		e.checkoutMade(p.dir)
	}
	e.mu.Lock()
	if err != nil {
		e.endNow(p, "the checkout could not be made: "+session.CleanMessage(err.Error()))
	} else {
		tr.unmade = false
	}
	p.opening = false
	if p.handoff != nil {
		go e.finishPreview(p, p.handoff) // an end that came during the open
	}
	serve := err == nil && !r.stopping && !p.closed()
	if serve {
		r.checks.Add(1)
	}
	ps := p.stateOf()
	e.mu.Unlock()
	if !serve {
		tr.busy.Done() // its end, begun, goes on
	}
	switch {
	case err != nil:
		return PreviewState{}, fmt.Errorf("the preview's checkout %s: %w", p.dir, err)
	case serve:
		go e.servePreview(p, tr)
	}
	return ps, nil
}

// reservePreview reserves the candidate's preview of r, within the
// bounds, with the result's commit as the run's transactions have left it,
// and counts it on its run (r.previews), which holds the run against
// eviction until the preview is dropped. The caller holds r.txMu and e.mu.
func (e *Engine) reservePreview(r *run, taskID string, n int, by string) (*preview, error) {
	if err := e.previewHeld(r); err != nil {
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
	key := previewKey{r.id, taskID, n}
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
	p := &preview{e: e, key: key, r: r, a: a, by: by, commit: a.result, dir: dir, cwd: filepath.Join(dir, r.prefix), read: e.now(), opening: true}
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
	e.forgetPreviewRun(p.r)
}

// previewReusable reports whether a checkout at dir is there to be taken
// as it is: false for none; true for one that is, in this order, a
// directory (looked at before any git runs in it, never a link), the top
// of its own work tree, a worktree of the repository at repo (its common
// git directory that repository's, each folder compared by identity), and
// at commit with no tracked change (checkoutIntact). Any other is refused
// with its path and what is wrong, and nothing of it is removed.
// Untracked files, a dev server's caches, are left as they are.
func previewReusable(ctx context.Context, repo, dir, commit string) (bool, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	refuse := func(what string) (bool, error) {
		return false, fmt.Errorf("%w: %s %s; nothing of it is removed", ErrPreviewRefused, dir, what)
	}
	if err != nil || !fi.IsDir() {
		return refuse("is there and is not a directory")
	}
	top, common, err := gitcli.RepoOf(ctx, dir)
	_, own, oerr := gitcli.RepoOf(ctx, repo)
	switch {
	case err != nil || !sameFolder(top, dir):
		return refuse("is there and is not the top of a git work tree")
	case oerr != nil || !sameFolder(common, own):
		return refuse("is there and is not a worktree of the run's repository")
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

// sameFolder reports whether the paths a and b name one folder, by its
// identity, whatever links their spellings go through.
func sameFolder(a, b string) bool {
	x, err := os.Stat(a)
	y, err2 := os.Stat(b)
	return err == nil && err2 == nil && os.SameFile(x, y)
}

// previewCheckoutTimeout bounds the making of a preview's checkout, under
// a context of its own that no end cancels: an end that comes meanwhile
// waits for it (the tree is held busy), so the server never leaves the
// checkout half made between the add's git commands. One that outlives it
// ends the open with errCheckoutBound.
const previewCheckoutTimeout = 10 * time.Minute

var errCheckoutBound = fmt.Errorf("the checkout was not made within %s", previewCheckoutTimeout)

// makePreviewCheckout makes p's checkout usable to its steps as verify
// makes the checks' checkout: added detached at the commit unless reused,
// under the repository's lock, and its HEAD read; the crew's directory in
// it as in the worktree (checkMemberDir), made; and its state directory
// with .conductor's .gitignore (makeStateDir).
func (e *Engine) makePreviewCheckout(ctx context.Context, r *run, p *preview, reused bool) error {
	if !reused {
		e.gitMu.Lock()
		err := AddDetachedWorktree(ctx, r.cwd, p.dir, p.commit)
		e.gitMu.Unlock()
		if err != nil {
			return err
		}
		if head, err := headCommit(ctx, p.dir); err != nil || head != p.commit {
			return fmt.Errorf("the checkout is at %s, not the result %s", short(head), short(p.commit))
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
// tree, and then ends p once previewIdle has passed with no read of it,
// judged as the end is made (beginEnd), an idle end the ledger refused
// tried again a previewIdle later; one that could not be made ready is
// ended with where and why, whatever the ledger answers (endNow). It runs
// counted in r.checks, which the run's stop waits for once it has begun
// the end of its previews.
func (e *Engine) servePreview(p *preview, tr *tree) {
	defer p.r.checks.Done()
	stage, err := e.prepareTree(p.ctx, p.r, p.a, tr)
	tr.busy.Done()
	if err != nil {
		e.mu.Lock()
		e.endNow(p, fmt.Sprintf("not ready at %s: %s", stage, session.CleanMessage(err.Error())))
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	left := previewIdle - e.now().Sub(p.read)
	e.mu.Unlock()
	for {
		select {
		case <-e.previewTimer(left):
		case <-p.ctx.Done():
			return
		}
		if e.beforeIdleEnd != nil {
			e.beforeIdleEnd()
		}
		var err error
		if left, err = e.beginEnd(p, whyPreviewIdle, true); err != nil {
			left = previewIdle
		} else if left <= 0 {
			return
		}
	}
}

// previewTimer is the timer of the idle end and of an owed write's next
// try: time.After, or a test's.
func (e *Engine) previewTimer(d time.Duration) <-chan time.Time {
	if e.idleTimer != nil {
		return e.idleTimer(d)
	}
	return time.After(d)
}

// closePreview begins p's end for why as a person's Close (beginEnd,
// judged on nothing else).
func (e *Engine) closePreview(p *preview, why string) error {
	_, err := e.beginEnd(p, why, false)
	return err
}

// beginEnd begins an end of p a person can ask again (a Close, the idle
// end), once, for why. Its first transaction's change, under e.mu:
// nothing for a preview whose end has begun; for an idle end (idle),
// nothing for one read less than previewIdle ago, the time left returned;
// else p marked ending, its URL cleared, and, once its tree is its own,
// its tree's end counted in r.trees (which Drain and the run's stop wait
// for) in that same step, so nothing sees it ending with no end in
// flight, and its row's PreviewEnding written. Only once that is
// committed is anything of it signalled: its steps' context ends, and its
// tree's end (finishPreview). Refused, the change is undone: the preview
// as it was, its end not counted, nothing signalled, and the refusal
// returned; an end that must happen that came meanwhile (after) begins
// then. A preview whose first transaction has not committed has nothing
// to end: marked ending, its open refuses and drops it.
func (e *Engine) beginEnd(p *preview, why string, idle bool) (time.Duration, error) {
	r := p.r
	var left time.Duration
	began, counted := false, false
	var state, url, was string
	err := e.transact(context.Background(), r, func() tx {
		if p.closed() {
			return tx{}
		}
		if idle {
			if left = previewIdle - e.now().Sub(p.read); left > 0 {
				return tx{}
			}
		}
		began, state, url, was = true, p.state, p.url, p.why
		p.state, p.url, p.why = previewEnding, "", why
		if p.t == nil {
			return tx{}
		}
		counted = true
		r.trees.add()
		row := ledger.PreviewRow{TaskID: p.a.t.def.ID, N: p.a.n, Seq: p.seq, Why: session.CleanMessage(why)}
		return tx{more: func(ltx LedgerTx) error { return ltx.PreviewEnding(row) }}
	}, func() {
		if began {
			p.state, p.url, p.why = state, url, was
			if after := p.after; after != "" {
				p.after = ""
				e.endNow(p, after)
			}
		}
		if counted {
			r.trees.done()
		}
		began, counted = false, false
	})
	if err != nil || !began {
		return left, err
	}
	p.cancel()
	if counted {
		go e.finishPreview(p, nil)
	}
	return 0, nil
}

// endNow begins p's end for why whatever the ledger answers: an open that
// failed, a service lost, the run's stop, the server's drain. At once, in
// the engine's memory: p ending, its URL cleared, so no reader offers it
// from here; its steps' context ended; once its tree is its own, its
// tree's end counted in r.trees, which a stop and Drain wait for, its
// first write (PreviewEnding) tried once on its own, and its teardown
// (finishPreview) begun after that try, by the open when it is opening
// (handoff). A preview whose end has begun keeps that end and its reason:
// the reason recorded is the one of the end that began first; one whose
// asked end's first write is in flight keeps why for it (after). The
// caller holds e.mu.
func (e *Engine) endNow(p *preview, why string) {
	if p.closed() {
		if p.after == "" {
			p.after = why
		}
		return
	}
	p.state, p.url, p.why = previewEnding, "", why
	p.cancel()
	if p.t == nil {
		return // no first transaction: its open refuses and drops it
	}
	p.r.trees.add()
	tried := make(chan struct{})
	row := ledger.PreviewRow{TaskID: p.a.t.def.ID, N: p.a.n, Seq: p.seq, Why: session.CleanMessage(why)}
	go func() {
		defer close(tried)
		_ = e.transact(context.Background(), p.r, func() tx {
			return tx{more: func(ltx LedgerTx) error { return ltx.PreviewEnding(row) }}
		}, giveUp)
	}()
	if p.opening {
		p.handoff = tried
		return
	}
	go e.finishPreview(p, tried)
}

// finishPreview ends p's tree once the end's first write has been tried
// (tried; nil for one committed): endTree stops its services, runs its
// cleanup, tries its last write (EndPreview and preview.ended, its ports
// let go after that commit) and counts the teardown done. A last write the
// ledger refused is owed: this goroutine alone tries it again every
// previewRetry, whatever reads the preview, until it is written, the
// engine ends (Drain) or the ledger answers that it is closed; the row is
// then the next start's to end (ledger.Recover). Only then is p dropped,
// its ports let go: it stays counted (e.previews, its run held) until
// then.
func (e *Engine) finishPreview(p *preview, tried <-chan struct{}) {
	if tried != nil {
		<-tried
	}
	e.mu.Lock()
	h := treeEnd{t: p.t, why: p.why}
	e.mu.Unlock()
	again := e.endTree(p.r, h)
	// No next try is armed once the engine has ended.
	for again != nil && e.life.Err() == nil {
		select {
		case <-e.previewTimer(previewRetry):
			if err := again(); err == nil || errors.Is(err, ledger.ErrClosed) {
				again = nil
			}
		case <-e.life.Done():
		}
	}
	e.mu.Lock()
	e.release(h.t)
	e.dropPreview(p)
	e.mu.Unlock()
	if e.previewFinished != nil {
		e.previewFinished()
	}
}

// ClosePreview ends the preview seq of attempt n of the task (Close): its
// end begun, the rest of it on its own (closePreview). A first
// transaction the ledger refused is the error, the preview left as it was.
// While its open is under way (opening, until the open has handed it to
// its serving or ended it) a Close is answered ErrPreviewOpening at once,
// and nothing of the preview changes.
func (e *Engine) ClosePreview(runID, taskID string, n, seq int) error {
	e.mu.Lock()
	p := e.previews[previewKey{runID, taskID, n}]
	ok := p != nil && p.seq == seq && !p.closed()
	opening := ok && p.opening
	e.mu.Unlock()
	switch {
	case !ok:
		return ErrPreviewNotFound
	case opening:
		return ErrPreviewOpening
	}
	return e.closePreview(p, whyPreviewClosed)
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
// nil r) for why, whatever the ledger answers (endNow). The caller holds
// e.mu.
func (e *Engine) endPreviews(r *run, why string) {
	for _, p := range e.previews {
		if r == nil || p.r == r {
			e.endNow(p, why)
		}
	}
}
