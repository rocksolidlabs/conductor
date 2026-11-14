package crew

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
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
// end cuts, in folders the server opened once and holds (previewFolder),
// and no step starts in a folder its path no longer names (tree.moved);
// each step is a receipt before its process starts; ready is committed to
// the row of its seq (preview.ready) before the preview is offered, its
// URL shown from that commit on. An end is of two kinds. One a person can
// ask again (a Close, the idle end) commits its first transaction (the URL
// cleared, the preview ending and its tree's end counted: beginEnd) before
// any of it is signalled, and a refused one leaves the preview as it was.
// Every other (an open that failed, a service lost, the run's stop, the
// server's drain) happens whatever the ledger answers (endNow): the
// preview ended in memory at once, its teardown begun, its first write
// tried once, its last, refused, owed and tried again until it is written
// (finishPreview). The ports are let go once the end's last transaction
// (preview.ended) is committed. The run is held against eviction
// (r.previews) from the reservation until the preview's row is ended, so
// nothing but the preview's own end stops its services.
//
// This row opens a preview of a run the engine holds; a run it no longer
// holds (evicted, or an earlier server's) is refused as not found.

// The bounds of the previews: at most maxPreviews open on the server, one
// per candidate at a time, ledger.MaxPreviews opens of a candidate, and
// each ended once previewIdle has passed with no read of it (ReadPreview).
// previewRetry is how long an owed write of a preview's end waits before
// it is tried again (finishPreview).
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
// why it ends; read, when it was last read (ReadPreview). commit is the
// result's commit as the reservation took it, the one commit of the
// preview. ctx is what its steps run under, beneath the run's teardown,
// cancelled once its end has begun. Under e.mu too: opening, from the
// reservation until the open hands the preview over, while an end that
// must happen leaves the start of its teardown to the open (handoff, its
// first write's try); after, the reason of such an end that came while an
// asked end's first write was in flight (beginEnd); held, its folders.
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
	held     *previewFolder
}

// The preview as its tree's owner (treeOwner). The caller holds e.mu.

func (p *preview) closed() bool { return p.state == previewEnding || p.state == previewEnded }

// refused judges the open's first transaction again, in its change: the
// run continued or being so (as a command's change reads it), stopping, or
// the attempt's result no longer the commit the reservation took.
func (p *preview) refused() error {
	switch err := p.e.continued(p.r); {
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
	ps := PreviewState{PreviewRef: PreviewRef{Run: p.key.run, Task: p.key.task, N: p.key.n, Seq: p.seq}, Root: p.r.cwd, Dir: p.dir, Commit: p.commit, By: p.by, Where: string(p.r.rt.Where())}
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
// project manifest serves it. In order: the candidate reserved, under the
// run's txMu, so a change under way commits or is undone first, within the
// bounds (one per candidate, ErrPreviewOpen; maxPreviews on the server, a
// PreviewsBusyError naming the open ones; ledger.MaxPreviews opens of it,
// ErrPreviewLimit), the result's commit copied into the preview, the one
// commit every later step uses; the run's worktrees folder opened from its
// working directory with no link on the way, and a checkout at
// <worktree>.preview that is there already opened in it once and taken
// only when it is the repository's own worktree by the repository's word,
// at the commit with no tracked change (previewReusable), and refused
// otherwise with its path and what is wrong (ErrPreviewRefused), nothing
// of it removed; the run's manifest
// resolved for the commit with the preview as the tree's owner
// (resolveTree), whose transaction is the open's first (preview.opened,
// its row resolved), refused, nothing written, for a run continued or
// stopping or a result that is no longer the commit (refused); then the
// checkout added detached at the commit when it was not there, opened and
// judged, with the crew's directory and its state directory made in the
// folder as opened (makePreviewCheckout), under previewCheckoutTimeout,
// which no end cuts; a checkout that cannot be made ends the preview
// whatever the ledger answers (endNow); then its phases (prepareTree: setup, services, ready,
// its readiness written to its row) and its idle end, on a goroutine the
// run's stop waits for. It returns the preview as it is once its checkout
// is made: resolved, its readiness to come.
func (e *Engine) OpenPreview(ctx context.Context, runID, taskID string, n int, by string) (PreviewState, error) {
	e.mu.Lock()
	r := e.runs[runID]
	e.mu.Unlock()
	if r == nil || !r.managed {
		return PreviewState{}, ErrRunNotFound
	}
	if e.afterLookup != nil {
		e.afterLookup(runID)
	}
	r.txMu.Lock()
	e.mu.Lock()
	p, err := e.reservePreview(r, taskID, n, by)
	e.mu.Unlock()
	r.txMu.Unlock()
	if err != nil {
		return PreviewState{}, err
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
	held, err := previewReusable(ctx, r.cwd, p.dir, p.commit)
	if err != nil {
		return unreserve(err)
	}
	e.mu.Lock()
	p.held = held
	e.mu.Unlock()
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
	err = e.makePreviewCheckout(mk, r, p, held)
	if cause := context.Cause(mk); err != nil && cause != nil {
		err = cause // the bound's own words, not those of the git it cut
	}
	cancel()
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
	if p.held != nil {
		p.held.Close()
		p.held = nil
	}
	p.state = previewEnded
	p.r.touch()
}

// previewFolder is a preview's folders as the server opened them, each
// once and without following a link, held open until the preview is
// dropped, so no identity it compares can pass to a folder made again at
// the path: the run's worktrees folder (parent), reached from the run's
// working directory with no link on the way (openUnder), as the
// attempts' dispatch judges it; the checkout's folder in it (folder, nil
// until a fresh add has made it) with its git directory, the repository's
// entry for it (admin); and the crew's directory in the checkout
// (member), where its steps run.
type previewFolder struct {
	parent, folder, member *os.Root
	admin                  string
}

// Close lets go of the folders.
func (f *previewFolder) Close() {
	for _, d := range []*os.Root{f.parent, f.folder, f.member} {
		if d != nil {
			d.Close()
		}
	}
}

// open opens the checkout dir in the parent (openIn: a name not there is
// the look's error) and judges it from that opened folder
// (previewGitDir), its git directory kept.
func (f *previewFolder) open(ctx context.Context, repo, dir string) error {
	folder, err := openIn(f.parent, filepath.Base(dir))
	if err != nil {
		return err
	}
	f.folder = folder
	f.admin, err = previewGitDir(ctx, repo, dir, folder)
	return err
}

// previewReusable opens the folders of a checkout at dir and reports
// whether one is there to be taken as it is: none (f.folder nil), or the
// repository's own worktree by the repository's word (previewGitDir), at
// commit with no tracked change, compared with its git directory and the
// folder as its work tree named outright (checkoutIntactBy,
// gitcli.RunAt), so nothing the folder's .git names and no work tree a
// configuration names takes part; any other is refused with its path and
// what is wrong, and nothing of it is removed, as is a way to it that is
// not the run's own folders. Untracked files, a dev server's caches, are
// left as they are. What is judged is the checkout as it is now: the
// preview serves its files as they are, and natively the same user may
// change them later.
func previewReusable(ctx context.Context, repo, dir, commit string) (*previewFolder, error) {
	parent, err := openUnder(repo, filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("%w: the way to %s from the run's working directory is not the run's own folders (a link, a file or nothing on it); nothing of it is removed", ErrPreviewRefused, dir)
	}
	f := &previewFolder{parent: parent}
	switch err = f.open(ctx, repo, dir); {
	case errors.Is(err, fs.ErrNotExist):
		return f, nil
	case errors.Is(err, ErrTreeMoved):
		err = fmt.Errorf("%w: %s is there and is not a directory; nothing of it is removed", ErrPreviewRefused, dir)
	case err == nil:
		told := func(env map[string]string, args ...string) (string, error) {
			return gitcli.RunAt(ctx, f.admin, dir, env, args...)
		}
		moved, changed, _, cerr := checkoutIntactBy(dir, told, commit)
		switch {
		case cerr != nil:
			err = fmt.Errorf("%w: %s could not be compared with %s: %s; nothing of it is removed", ErrPreviewRefused, dir, short(commit), session.CleanMessage(cerr.Error()))
		case moved:
			err = fmt.Errorf("%w: %s is not at %s; nothing of it is removed", ErrPreviewRefused, dir, short(commit))
		case len(changed) > 0:
			err = fmt.Errorf("%w: %s has changes to tracked files: %s; nothing of it is removed", ErrPreviewRefused, dir, changedList(changed))
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// previewEntryRead, when a test sets it, is called with each entry of the
// repository's worktrees folder that previewGitDir reads.
var previewEntryRead func(name string)

// previewGitDir is the git directory of the checkout folder opened at dir
// when the repository at repo says it is one of its worktrees, judged from
// that opened folder before any git runs in or about it, in one read
// whatever the repository holds: the folder's .git, a regular file in it
// (readSmall), names an entry, taken by its name alone in the repository's
// worktrees folder, opened without following a link (openIn); that entry's
// gitdir, a regular file (readSmall), names the .git inside the opened
// folder, the folder it is in compared with the opened folder as the
// directory it is. Anything else is refused in fixed words that name dir.
func previewGitDir(ctx context.Context, repo, dir string, folder *os.Root) (string, error) {
	notOurs := fmt.Errorf("%w: %s is there and is not a worktree of the repository; nothing of it is removed", ErrPreviewRefused, dir)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, common, err := gitcli.RepoOf(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("%w: the repository could not be read: %s", ErrPreviewRefused, session.CleanMessage(err.Error()))
	}
	named, ok := strings.CutPrefix(readSmall(folder, ".git"), "gitdir: ")
	name := filepath.Base(named)
	if !ok || !filepath.IsLocal(name) {
		return "", notOurs
	}
	top, err := os.OpenRoot(common)
	if err != nil {
		return "", notOurs
	}
	defer top.Close()
	worktrees, err := openIn(top, "worktrees")
	if err != nil {
		return "", notOurs
	}
	defer worktrees.Close()
	entry, err := openIn(worktrees, name)
	if err != nil {
		return "", notOurs
	}
	defer entry.Close()
	if previewEntryRead != nil {
		previewEntryRead(name)
	}
	admin := filepath.Join(common, "worktrees", name)
	back := readSmall(entry, "gitdir")
	if !filepath.IsAbs(back) {
		back = filepath.Join(admin, back)
	}
	in, err := os.Stat(filepath.Dir(back))
	self, serr := folder.Stat(".")
	if filepath.Base(back) != ".git" || err != nil || serr != nil || !os.SameFile(in, self) {
		return "", notOurs
	}
	return admin, nil
}

// readSmall is the first line of the file name in dir when it is a regular
// file of at most 4 KiB, looked at without following a link, opened
// without blocking and the very file looked at; "" otherwise.
func readSmall(dir *os.Root, name string) string {
	fi, err := dir.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4<<10 {
		return ""
	}
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if got, err := f.Stat(); err != nil || !os.SameFile(fi, got) {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4<<10))
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSuffix(line, "\r")
}

// previewCheckoutTimeout bounds the making of a preview's checkout, under
// a context of its own that no end cancels: an end that comes meanwhile
// waits for it (the tree is held busy), so the server never leaves the
// checkout half made between the add's git commands. One that outlives it
// ends the open with errCheckoutBound.
const previewCheckoutTimeout = 10 * time.Minute

var errCheckoutBound = fmt.Errorf("the checkout was not made within %s", previewCheckoutTimeout)

// makePreviewCheckout makes p's checkout usable to its steps, in the
// folders f holds: added detached at the commit unless reused, then opened
// in the run's worktrees folder held since before the add and judged
// (previewFolder.open), and its HEAD read with that git directory named
// outright; then, inside the checkout's folder as opened, the crew's
// directory walked and made (memberDirIn) and held, and its state
// directory with .conductor's .gitignore (makeStateDirIn).
func (e *Engine) makePreviewCheckout(ctx context.Context, r *run, p *preview, f *previewFolder) error {
	if f.folder == nil {
		e.gitMu.Lock()
		err := AddDetachedWorktree(ctx, r.cwd, p.dir, p.commit)
		e.gitMu.Unlock()
		if err != nil {
			return err
		}
		if err := f.open(ctx, r.cwd, p.dir); err != nil {
			return fmt.Errorf("the checkout just added could not be opened: %w", err)
		}
		head, err := gitcli.RunAt(ctx, f.admin, p.dir, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
		if head = strings.TrimSpace(head); err != nil || head != p.commit {
			return fmt.Errorf("the checkout is at %s, not the result %s", short(head), short(p.commit))
		}
	}
	if e.checkoutJudged != nil {
		e.checkoutJudged(p.dir)
	}
	member, err := memberDirIn(f.folder, r.prefix, e.memberDirChecked)
	if err != nil {
		return err
	}
	f.member = member
	return makeStateDirIn(member, true)
}

// memberDirIn walks the crew's directory rel inside root, making each part
// that is missing, and opens it: every part is taken through root, never
// by a path resolved again, so a part that is or becomes a link cannot
// send the making outside the checkout (os.Root refuses a way out), and a
// part that is a link or a file is refused as checkMemberDir refuses it.
// checked, when set, is called with each part once it is taken.
func memberDirIn(root *os.Root, rel string, checked func(part string)) (*os.Root, error) {
	at := "."
	for part := range strings.SplitSeq(filepath.ToSlash(filepath.Clean(rel)), "/") {
		if part == "" || part == "." {
			continue
		}
		at = path.Join(at, part)
		fi, err := root.Lstat(at)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			err = root.Mkdir(at, 0o755)
		case err != nil:
		case fi.Mode()&fs.ModeSymlink != 0:
			err = invalidf("%s in the member's worktree is a symbolic link: the member's directory must stay inside the worktree", quote(at))
		case !fi.IsDir():
			err = invalidf("%s in the member's worktree is a file, not a directory: the member's directory must be one", quote(at))
		}
		if err != nil {
			return nil, err
		}
		if checked != nil {
			checked(at)
		}
	}
	return root.OpenRoot(at)
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
// previewRetry, whatever reads the preview, until it is written. Only then
// is p dropped: it stays counted (e.previews, its run held) until its row
// is ended.
func (e *Engine) finishPreview(p *preview, tried <-chan struct{}) {
	if tried != nil {
		<-tried
	}
	e.mu.Lock()
	h := treeEnd{t: p.t, why: p.why}
	e.mu.Unlock()
	again := e.endTree(p.r, h)
	for again != nil {
		<-e.previewTimer(previewRetry)
		if again() == nil {
			again = nil
		}
	}
	e.mu.Lock()
	e.dropPreview(p)
	e.mu.Unlock()
}

// ClosePreview ends the preview seq of attempt n of the task (Close): its
// end begun, the rest of it on its own (closePreview). A first
// transaction the ledger refused is the error, the preview left as it was.
func (e *Engine) ClosePreview(runID, taskID string, n, seq int) error {
	e.mu.Lock()
	p := e.previews[previewKey{runID, taskID, n}]
	ok := p != nil && p.seq == seq && !p.closed()
	e.mu.Unlock()
	if !ok {
		return ErrPreviewNotFound
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
