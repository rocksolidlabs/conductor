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
// end cuts; each step is a receipt before its process starts; ready is
// committed to the row of its seq (preview.ready) before the preview is
// offered, its URL shown from that commit on; an end's first transaction
// (the URL cleared, the preview ending and its tree's end counted:
// beginEnd) is committed before any of it is signalled, so nothing offers
// a URL a teardown is ending and no late step of that seq writes its row,
// and a refused one leaves the preview as it was; the ports are let go
// once the end's last transaction (preview.ended) is committed. The run is
// held against eviction (r.previews) from the reservation to that last
// transaction, so nothing but the preview's own end stops its services.
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
// why it ends; read, when it was last read (ReadPreview). commit is the
// result's commit as the reservation took it, the one commit of the
// preview. ctx is what its steps run under, beneath the run's teardown,
// cancelled once its end's first transaction is committed.
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

// ready offers the preview's URL only once its readiness is committed
// (the change's done), never in the change itself: a reader is never
// shown a URL whose readiness the ledger may yet refuse.
func (p *preview) ready(t *tree, on bool) func() {
	if !on {
		return nil
	}
	return func() { p.state, p.url = ledger.PreviewReady, t.preview }
}

// serviceLost ends the preview: its URL withdrawn in the change that
// records the service's end, its end begun once that is committed.
func (p *preview) serviceLost(r *run, t *tree) (string, func()) {
	if p.closed() {
		return "", nil
	}
	p.url = ""
	return "; the preview is ended", func() { _ = p.e.closePreview(p, whyServiceExited) }
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
// commit every later step uses; a checkout at <worktree>.preview that is
// there already taken only when it is the repository's own worktree by
// the repository's word, at the commit with no tracked change
// (previewReusable), and refused otherwise with its path and what is
// wrong (ErrPreviewRefused), nothing of it removed; the run's manifest
// resolved for the commit with the preview as the tree's owner
// (resolveTree), whose transaction is the open's first (preview.opened,
// its row resolved), refused, nothing written, for a run continued or
// stopping or a result that is no longer the commit (refused); then the
// checkout added detached at the commit when it was not there, with the
// crew's directory and its state directory, under previewCheckoutTimeout,
// which no end cuts; then its phases (prepareTree: setup, services, ready,
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
		e.dropPreview(p)
		e.mu.Unlock()
		p.cancel()
		return PreviewState{}, err
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
	mk, cancel := context.WithTimeout(context.Background(), previewCheckoutTimeout)
	err = e.makePreviewCheckout(mk, r, p, reused)
	cancel()
	if err != nil {
		tr.busy.Done()
		words := session.CleanMessage(err.Error())
		_ = e.closePreview(p, "the checkout could not be made: "+words)
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
	p := &preview{e: e, key: key, r: r, a: a, by: by, commit: a.result, dir: dir, cwd: filepath.Join(dir, r.prefix), read: e.now()}
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
// is: none (false), or the repository's own worktree by the repository's
// word (previewGitDir), at commit with no tracked change, compared with
// its git directory and the folder as its work tree named outright
// (checkoutIntactBy, gitcli.RunAt), so nothing the folder's .git names
// and no work tree a configuration names takes part (true); any other is
// refused with its path and what is wrong, and nothing of it is removed.
// Untracked files, a dev server's caches, are left as they are. What is
// judged is the checkout as it is now: the preview serves its files as
// they are, and natively the same user may change them later.
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
	gitDir, err := previewGitDir(ctx, repo, dir)
	if err != nil {
		return false, err
	}
	told := func(env map[string]string, args ...string) (string, error) {
		return gitcli.RunAt(ctx, gitDir, dir, env, args...)
	}
	moved, changed, _, err := checkoutIntactBy(dir, told, commit)
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

// previewGitDir is the git directory of the checkout found at dir when the
// repository at repo says it is one of its worktrees, judged before any
// git runs in or about dir: an entry of the repository's worktrees folder
// whose gitdir file names dir's .git, found from the repository's side,
// and dir's .git a regular file, opened without following a link, that
// names that entry, each compared as the file it is, not by its text.
// Otherwise the checkout is refused in fixed words that name dir.
func previewGitDir(ctx context.Context, repo, dir string) (string, error) {
	notOurs := fmt.Errorf("%w: %s is there and is not a worktree of the repository; nothing of it is removed", ErrPreviewRefused, dir)
	_, common, err := gitcli.RepoOf(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("%w: the repository could not be read: %s", ErrPreviewRefused, session.CleanMessage(err.Error()))
	}
	f, err := os.OpenFile(filepath.Join(dir, ".git"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", notOurs
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", notOurs
	}
	named, ok := strings.CutPrefix(firstLine(f), "gitdir: ")
	if !ok {
		return "", notOurs
	}
	if !filepath.IsAbs(named) {
		named = filepath.Join(dir, named)
	}
	entries, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, ent := range entries {
		admin := filepath.Join(common, "worktrees", ent.Name())
		back, err := os.Open(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		gi, err := os.Lstat(firstLine(back))
		back.Close()
		if err != nil || !os.SameFile(fi, gi) {
			continue
		}
		ai, err := os.Stat(admin)
		ni, nerr := os.Stat(named)
		if err != nil || nerr != nil || !os.SameFile(ai, ni) {
			return "", notOurs
		}
		return admin, nil
	}
	return "", notOurs
}

// firstLine is the first line of what r holds, within 4 KiB.
func firstLine(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSuffix(line, "\r")
}

// previewCheckoutTimeout bounds the making of a preview's checkout, under
// a context of its own that no end cancels: an end that comes meanwhile
// waits for it (the tree is held busy), so the server never leaves the
// checkout half made between the add's two git commands.
const previewCheckoutTimeout = 10 * time.Minute

// makePreviewCheckout makes p's checkout usable to its steps: added
// detached at the commit unless reused, checked at it, then, inside the
// checkout's folder opened once, the crew's directory walked and made
// (memberDirIn) and its state directory with .conductor's .gitignore
// (makeStateDirIn).
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
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	member, err := memberDirIn(root, r.prefix, e.memberDirChecked)
	if err != nil {
		return err
	}
	defer member.Close()
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
// judged as the end is made (beginEnd); one that could not be made ready
// is ended with where and why. An end the ledger refused is tried again
// at the next idle end. It runs counted in r.checks, which the run's stop
// waits for once it has begun the end of its previews.
func (e *Engine) servePreview(p *preview, tr *tree) {
	defer p.r.checks.Done()
	stage, err := e.prepareTree(p.ctx, p.r, p.a, tr)
	tr.busy.Done()
	why := whyPreviewIdle
	if err != nil {
		why = fmt.Sprintf("not ready at %s: %s", stage, session.CleanMessage(err.Error()))
		if e.closePreview(p, why) == nil {
			return
		}
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
		var err error
		if left, err = e.beginEnd(p, why, true); err != nil {
			left = previewIdle
		} else if left <= 0 {
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

// closePreview begins p's end for why (beginEnd, judged on nothing else).
func (e *Engine) closePreview(p *preview, why string) error {
	_, err := e.beginEnd(p, why, false)
	return err
}

// beginEnd begins p's end, once, for why. Its first transaction's change,
// under e.mu: nothing for a preview whose end has begun; for an idle end
// (idle), nothing for one read less than previewIdle ago, the time left
// returned; else p marked ending, its URL cleared, and, once its tree is
// its own, its tree's end counted in r.trees (which Drain and the run's
// stop wait for) in that same step, so nothing sees it ending with no end
// in flight, and its row's PreviewEnding written. Only once that is
// committed is anything of it signalled: its steps' context ends, and its
// tree's end (finishPreview: endTree stops its services, runs its cleanup
// and records preview.ended, its ports let go after that commit; then it
// is dropped). Refused, the change is undone: the preview as it was, its
// end not counted, nothing signalled, and the refusal returned. A preview
// whose first transaction has not committed has nothing to end: marked
// ending, its open refuses and drops it.
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
		go e.finishPreview(p)
	}
	return 0, nil
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
// nil r) with why, and returns once each end's first transaction is in,
// with the refusals.
func (e *Engine) endPreviews(r *run, why string) error {
	e.mu.Lock()
	var ps []*preview
	for _, p := range e.previews {
		if r == nil || p.r == r {
			ps = append(ps, p)
		}
	}
	e.mu.Unlock()
	var errs []error
	for _, p := range ps {
		errs = append(errs, e.closePreview(p, why))
	}
	return errors.Join(errs...)
}
