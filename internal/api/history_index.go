package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The indexer (docs/plans/history.md, "The indexer", PR-3): what feeds
// the history's index from the session records and the runs. The index is
// opened off the server's start path, once the journal holds
// history/owner.lock (one writer: the lock's holder; a server without it
// opens the index as a reader), and its owner's Open reads no more than
// its schema's version; an index its last owner did not close is checked
// (history.Index.Check) on a goroutine of its own beside the indexing. A
// dirty set of source keys, filled by the commit hooks (commits.go) and
// bounded, is drained by one goroutine, one source at a time, each read as
// it is now through its store's own reads, its mark read before it. A
// catch-up at open, after a reindex and after an overflow compares every
// source's mark with the index's; a reconcile every reconcileEvery finds
// what another server wrote to the ledger and the run records, which no
// hook of this process sees. The index decides nothing.
//
// A search never holds up an index write: the index reads through
// connections of its own beside its one writer (internal/history), so a
// write waits for no search, only for another write; the indexer is the
// only writer and holds the index for one source's Replace at a time.

// The indexer's bounds and clocks.
const (
	// maxHistoryDirty bounds the dirty set: past it the set is emptied and
	// a full catch-up asked instead, so nothing is dropped.
	maxHistoryDirty = 10_000
	// minRunInterval: a run source is read at most once in it, however
	// many transitions marked it; the last change is always read after.
	minRunInterval = 2 * time.Second
	// historyReconcileEvery is the reconcile's period (reconcileNow drives
	// it in tests).
	historyReconcileEvery = 30 * time.Second
	// A failed read or Replace is retried after a backoff, doubling.
	historyBackoffMin = time.Second
	historyBackoffMax = time.Minute
	// maxRunChain bounds how far back along its continuations a run's
	// pass reads for its handed and passed_after links.
	maxRunChain = 32
	// maxRunRecord bounds a run record read (crew's own bound).
	maxRunRecord = 4 << 20
	// maxRepoCache bounds the folders whose repository is remembered.
	maxRepoCache = 1024
)

// The indexing states, as GET /api/history's indexing.state says them.
const (
	indexOpening    = "opening"     // the index not open yet
	indexCatchingUp = "catching_up" // a catch-up under way: behind says how far
	indexChecking   = "checking"    // the index owed a Check: searches answer no rows
	indexReady      = "ready"
	indexReadOnly   = "read_only"   // another server owns history/owner.lock
	indexFailed     = "unavailable" // the index could not be opened (retried)
)

// indexLedger is what the indexer reads of the ledger: *ledger.Ledger, a
// read-only one too (a reader's connection), or a test's wrapper.
type indexLedger interface {
	Pointer(ctx context.Context, id string) (ledger.RunPointer, bool, error)
	Pointers(ctx context.Context, after string, n int) ([]ledger.RunPointer, error)
	Run(ctx context.Context, id string) (ledger.RunRow, bool, error)
	Tasks(ctx context.Context, runID string) ([]ledger.TaskRow, error)
	Attempts(ctx context.Context, runID string) ([]ledger.AttemptRow, error)
	Commands(ctx context.Context, runID string) ([]ledger.CommandRow, error)
	Messages(ctx context.Context, runID string) ([]ledger.MessageRow, error)
	Checks(ctx context.Context, runID, taskID string, n int, withLog bool) ([]ledger.CheckRow, error)
	Check(ctx context.Context, runID, taskID string, n int, id string, k int) (ledger.CheckRow, bool, error)
}

// historyIndexer feeds the index. Its fields after mu are the hooks'
// and the routes' (under mu); the rest belong to its goroutine.
type historyIndexer struct {
	data     *store.Store
	journal  *journal.Journal
	ledger   func() indexLedger // nil: no ledger in this process
	dispatch *dispatch.Store
	path     string
	host     string
	log      *slog.Logger

	// Seams: the clock, the timer, a pause between a source's mark and
	// its read, the repository read, and the reconcile's period.
	now           func() time.Time
	after         func(time.Duration) <-chan time.Time
	afterMark     func(key string)
	repoOf        func(ctx context.Context, dir string) (top, common string, err error)
	reconcileTick time.Duration

	mu       sync.Mutex
	queue    []string
	dirty    map[string]bool
	overflow bool // the set passed its bound: a full catch-up
	sessions bool // a project changed: every session source
	reindex  bool
	projects bool // Dispatch's projects changed: read them again
	behind   int  // a catch-up's sources not read yet
	state    string
	checking bool
	noLedger bool
	ix       *history.Index
	requests []chan struct{} // settle: answered once nothing is left to do
	wake     chan struct{}
	owned    chan struct{}
	ownOnce  sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
	bg       sync.WaitGroup

	// The goroutine's own.
	due      map[string]time.Time // a source deferred to a time: its run read too lately, a retry
	readAt   map[string]time.Time // when a run source was last read
	backoff  time.Duration
	pm       *projectMap
	repos    map[string]repoIdent
	runStats map[string]fileStat // the reconcile's last look at each run record
}

type repoIdent struct{ top, common string }

type fileStat struct {
	size int64
	mod  time.Time
}

// projectMap is Dispatch's projects as a pass reads them: by their
// folders' repository identity, and their digest, which a session
// source's mark carries (P).
type projectMap struct {
	digest string
	byRepo map[string]string
}

// newHistoryIndexer is an indexer over the journal's data directory; it
// does nothing until start.
func newHistoryIndexer(data *store.Store, j *journal.Journal, ld func() indexLedger, ds *dispatch.Store, host string, log *slog.Logger) *historyIndexer {
	return &historyIndexer{
		data: data, journal: j, ledger: ld, dispatch: ds, host: host, log: log,
		path:          filepath.Join(data.Dir(), journal.Dir, history.File),
		now:           time.Now,
		after:         time.After,
		afterMark:     func(string) {},
		repoOf:        gitcli.RepoOf,
		reconcileTick: historyReconcileEvery,
		dirty:         map[string]bool{},
		state:         indexOpening,
		wake:          make(chan struct{}, 1),
		owned:         make(chan struct{}),
		done:          make(chan struct{}),
		due:           map[string]time.Time{},
		readAt:        map[string]time.Time{},
		repos:         map[string]repoIdent{},
		runStats:      map[string]fileStat{},
	}
}

// observer is what the indexer takes of the commits (commits.go): each
// only marks a source dirty, never waiting.
func (h *historyIndexer) observer() commitObserver {
	return commitObserver{
		data: func(ev store.Event) {
			if id, ok := docID(ev.Path, "runs/"); ok {
				h.mark(hRunRef(id))
			} else if id, ok := docID(ev.Path, journal.Dir+"/"+journal.SessionsDir+"/"); ok {
				h.mark(hSessionRef(id))
			}
		},
		commit: func(p ledger.RunPointer) { h.mark(hRunRef(p.ID)) },
		prune:  func(id string, _ int64) { h.mark(hRunRef(id)) },
		dispatch: func(cs []dispatch.Change) {
			if slices.ContainsFunc(cs, func(c dispatch.Change) bool { return c.Entity == dispatch.EntityProject }) {
				h.mu.Lock()
				h.projects, h.sessions = true, true
				h.mu.Unlock()
				h.signal()
			}
		},
	}
}

// docID is the id of the document at the slash path p directly in dir
// ("runs/<id>.json"): false for anything else.
func docID(p, dir string) (string, bool) {
	rest, ok := strings.CutPrefix(p, dir)
	if !ok || strings.Contains(rest, "/") {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ".json")
	return id, ok && id != ""
}

// mark makes the source dirty: queued at the back unless it is there
// already; past maxHistoryDirty the set is emptied for a full catch-up.
func (h *historyIndexer) mark(key string) {
	h.mu.Lock()
	switch {
	case h.dirty[key] || h.overflow:
	case len(h.dirty) >= maxHistoryDirty:
		h.queue, h.dirty, h.overflow = nil, map[string]bool{}, true
	default:
		h.queue = append(h.queue, key)
		h.dirty[key] = true
	}
	h.mu.Unlock()
	h.signal()
}

func (h *historyIndexer) signal() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// takeOwnership is the journal's OnOwned: the lock is this process's, and
// the indexer may open the index as its owner.
func (h *historyIndexer) takeOwnership() {
	h.ownOnce.Do(func() { close(h.owned) })
}

// start runs the indexer on a goroutine of its own: nothing on the
// caller's path opens or checks the index.
func (h *historyIndexer) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	h.mu.Unlock()
	h.journal.OnOwned(h.takeOwnership)
	if ro, _ := h.journal.ReadOnly(); !ro {
		// Owned already (OnOwned runs its function once per journal).
		h.takeOwnership()
	}
	go h.run(ctx)
}

// stop ends the indexer and its check, then closes the index (which waits
// for a write in flight and marks it closed, so the next start owes no
// check). Before the stores it reads are closed; a second call waits for
// the first.
func (h *historyIndexer) stop() {
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-h.done
	h.bg.Wait()
	h.mu.Lock()
	ix := h.ix
	h.ix = nil
	h.mu.Unlock()
	if ix != nil {
		if err := ix.Close(); err != nil {
			h.log.Warn("the history's index did not close cleanly", "err", err)
		}
	}
}

// index is the open index, nil before it opens.
func (h *historyIndexer) index() *history.Index {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ix
}

// indexing is the state a reply reports: {state, behind?, ledger?}.
func (h *historyIndexer) indexing() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.state
	if h.checking && state == indexReady {
		state = indexChecking
	}
	out := map[string]any{"state": state}
	if n := h.behind + len(h.queue); n > 0 && state != indexReadOnly {
		out["behind"] = n
	}
	if h.noLedger {
		out["ledger"] = "unavailable"
	}
	return out
}

func (h *historyIndexer) setState(s string) {
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
}

// settle returns once the indexer has nothing left to do (every dirty
// source read, no catch-up under way, none deferred), with reconcile
// first when it is set; or once ctx is done.
func (h *historyIndexer) settle(ctx context.Context, reconcile bool) error {
	done := make(chan struct{})
	h.mu.Lock()
	if reconcile {
		h.requests = append(h.requests, nil) // a reconcile, before the settle after it
	}
	h.requests = append(h.requests, done)
	h.mu.Unlock()
	h.signal()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reconcileNow reconciles against the stores at once and returns once
// what it found is indexed: the seam the reconcile's tests drive.
func (h *historyIndexer) reconcileNow(ctx context.Context) error { return h.settle(ctx, true) }

// askReindex has the goroutine clear the index and catch up from
// nothing.
func (h *historyIndexer) askReindex() {
	h.mu.Lock()
	h.reindex = true
	h.mu.Unlock()
	h.signal()
}

// run is the indexer's goroutine.
func (h *historyIndexer) run(ctx context.Context) {
	defer close(h.done)
	select {
	case <-h.owned:
	default:
		// Another server owns the history: this one reads the index as it
		// is, writes nothing, and takes the lock as the journal does.
		h.setState(indexReadOnly)
		h.openReader(ctx)
		select {
		case <-ctx.Done():
			return
		case <-h.owned:
		}
		h.mu.Lock()
		ix := h.ix
		h.ix, h.state = nil, indexOpening
		h.mu.Unlock()
		if ix != nil {
			_ = ix.Close()
		}
	}
	if !h.openOwner(ctx) {
		return
	}
	h.catchUp(ctx, false)
	h.loop(ctx)
}

// openReader opens the index read-only for the searches; a failure is
// logged and leaves none.
func (h *historyIndexer) openReader(ctx context.Context) {
	ix, err := history.Open(ctx, h.path, history.Options{ReadOnly: true, Logger: h.log})
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("the history's index could not be opened to read", "err", err)
		}
		return
	}
	h.mu.Lock()
	h.ix = ix
	h.mu.Unlock()
}

// openOwner opens the index as its owner, trying again with a backoff;
// an index owed a check is checked on a goroutine of its own.
func (h *historyIndexer) openOwner(ctx context.Context) bool {
	wait := historyBackoffMin
	for {
		ix, err := history.Open(ctx, h.path, history.Options{Logger: h.log})
		if err == nil {
			h.mu.Lock()
			h.ix, h.state = ix, indexCatchingUp
			h.mu.Unlock()
			if unchecked, err := ix.Unchecked(ctx); err == nil && unchecked {
				h.mu.Lock()
				h.checking = true
				h.mu.Unlock()
				h.bg.Go(func() { h.check(ctx, ix) })
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		h.log.Warn("the history's index could not be opened; trying again", "err", err, "in", wait)
		h.setState(indexFailed)
		select {
		case <-ctx.Done():
			return false
		case <-h.after(wait):
		}
		wait = min(2*wait, historyBackoffMax)
	}
}

// check runs the index's Check, beside the indexing: a cleared or moved
// aside index is caught up from nothing; a failure is retried.
func (h *historyIndexer) check(ctx context.Context, ix *history.Index) {
	wait := historyBackoffMin
	for {
		outcome, err := ix.Check(ctx)
		if err == nil {
			h.mu.Lock()
			h.checking = false
			if outcome != history.CheckSound {
				h.overflow = true // a catch-up from nothing
			}
			h.mu.Unlock()
			if outcome != history.CheckSound {
				h.log.Warn("the history's index failed its check and is rebuilt", "outcome", outcome)
				h.signal()
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("the history's index could not be checked; trying again", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-h.after(wait):
		}
		wait = min(2*wait, historyBackoffMax)
	}
}

// loop drains the dirty set, one source at a time, and reconciles.
func (h *historyIndexer) loop(ctx context.Context) {
	tick := h.after(h.reconcileTick)
	for ctx.Err() == nil {
		h.mu.Lock()
		step := ""
		switch {
		case h.reindex:
			step, h.reindex, h.overflow = "reindex", false, false
		case h.overflow:
			step, h.overflow, h.sessions = "catch-up", false, false
		case h.sessions:
			step, h.sessions = "sessions", false
		case len(h.requests) > 0 && h.requests[0] == nil:
			step, h.requests = "reconcile", h.requests[1:]
		}
		var req chan struct{}
		h.mu.Unlock()
		switch step {
		case "reindex":
			h.doReindex(ctx)
			continue
		case "catch-up":
			h.catchUp(ctx, false)
			continue
		case "sessions":
			h.catchUp(ctx, true)
			continue
		case "reconcile":
			h.reconcile(ctx)
			continue
		}
		if key, ok := h.take(); ok {
			h.pass(ctx, key)
			continue
		}
		// Nothing ready: a settle with nothing deferred is answered.
		h.mu.Lock()
		if len(h.due) == 0 && len(h.requests) > 0 && h.requests[0] != nil {
			req = h.requests[0]
			h.requests = h.requests[1:]
		}
		h.mu.Unlock()
		if req != nil {
			close(req)
			continue
		}
		var timer <-chan time.Time
		if next, ok := h.nextDue(); ok {
			timer = h.after(max(0, next.Sub(h.now())))
		}
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-timer:
		case <-tick:
			h.reconcile(ctx)
			tick = h.after(h.reconcileTick)
		}
	}
}

// take is the next source to read: one deferred whose time has come, else
// the oldest dirty one not deferred.
func (h *historyIndexer) take() (string, bool) {
	now := h.now()
	for key, t := range h.due {
		if !now.Before(t) {
			delete(h.due, key)
			return key, true
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for len(h.queue) > 0 {
		key := h.queue[0]
		h.queue = h.queue[1:]
		delete(h.dirty, key)
		if _, deferred := h.due[key]; deferred {
			continue // read at its time
		}
		return key, true
	}
	return "", false
}

func (h *historyIndexer) nextDue() (time.Time, bool) {
	var next time.Time
	for _, t := range h.due {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next, !next.IsZero()
}

// pass reads one source as it is now; a failure leaves it dirty, read
// again after a backoff.
func (h *historyIndexer) pass(ctx context.Context, key string) {
	ix := h.index()
	if ix == nil || ctx.Err() != nil {
		return
	}
	kind, id, _ := strings.Cut(key, ":")
	var err error
	switch kind {
	case "session":
		err = h.indexSession(ctx, ix, id)
	case "run":
		err = h.indexRun(ctx, ix, id)
	}
	if err == nil {
		h.backoff = 0
		return
	}
	if ctx.Err() != nil {
		return
	}
	h.backoff = min(max(2*h.backoff, historyBackoffMin), historyBackoffMax)
	h.due[key] = h.now().Add(h.backoff)
	h.log.Warn("a history source could not be indexed; trying again", "source", key, "err", err, "in", h.backoff)
}

// currentLedger is the ledger to read, nil for none in this process.
func (h *historyIndexer) currentLedger() indexLedger {
	if h.ledger == nil {
		return nil
	}
	return h.ledger()
}

// projectsOf is Dispatch's projects as this pass reads them, read again
// when Dispatch committed a project's change.
func (h *historyIndexer) projectsOf(ctx context.Context) *projectMap {
	h.mu.Lock()
	stale := h.projects
	h.projects = false
	h.mu.Unlock()
	if h.pm != nil && !stale {
		return h.pm
	}
	pm := &projectMap{byRepo: map[string]string{}}
	var lines []string
	if h.dispatch != nil {
		next := ""
		for {
			page, err := h.dispatch.Projects(ctx, true, 0, next)
			if err != nil {
				h.log.Warn("Dispatch's projects could not be read for the history", "err", err)
				break
			}
			for _, ps := range page.Projects {
				v, err := h.dispatch.Project(ctx, ps.Key)
				if err != nil {
					continue
				}
				id := h.repoIdent(ctx, v.Project.Cwd)
				archived := !v.Project.ArchivedAt.IsZero()
				lines = append(lines, fmt.Sprintf("%s\x00%s\x00%t", ps.Key, id.common, archived))
				if id.common != "" && !archived {
					if _, taken := pm.byRepo[id.common]; !taken {
						pm.byRepo[id.common] = ps.Key
					}
				}
			}
			if page.Next == "" {
				break
			}
			next = page.Next
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	pm.digest = hex.EncodeToString(sum[:8])
	h.pm = pm
	return pm
}

// repoIdent is a folder's repository (gitcli.RepoOf: its top and its
// common git directory, one for every linked worktree), remembered once
// read; none for a folder not in one.
func (h *historyIndexer) repoIdent(ctx context.Context, dir string) repoIdent {
	if dir == "" {
		return repoIdent{}
	}
	if id, ok := h.repos[dir]; ok {
		return id
	}
	rctx, cancel := context.WithTimeout(ctx, revisionTimeout)
	defer cancel()
	top, common, err := h.repoOf(rctx, dir)
	if err != nil {
		return repoIdent{}
	}
	if len(h.repos) >= maxRepoCache {
		clear(h.repos)
	}
	id := repoIdent{top: top, common: common}
	h.repos[dir] = id
	return id
}

// indexSession reads a session record: its mark S<Seq>/P<projects>, and
// L<revision> for a crew member (its start commit is its attempt's).
func (h *historyIndexer) indexSession(ctx context.Context, ix *history.Index, id string) error {
	key := hSessionRef(id)
	rec, _, err := h.journal.Get(id)
	if errors.Is(err, journal.ErrNotFound) || errors.Is(err, journal.ErrBadID) {
		return ix.Remove(ctx, key)
	}
	if err != nil {
		return err
	}
	pm := h.projectsOf(ctx)
	mark := fmt.Sprintf("S%d/P%s", rec.Seq, pm.digest)
	ld := h.currentLedger()
	member := rec.Crew != nil && rec.Crew.RunID != ""
	if member && ld != nil {
		if p, ok, err := ld.Pointer(ctx, rec.Crew.RunID); err == nil && ok {
			mark += fmt.Sprintf("/L%d", p.Revision)
		}
	}
	if old, ok, err := ix.Mark(ctx, key); err != nil {
		return err
	} else if ok && old == mark {
		return nil
	}
	h.afterMark(key)
	var place sessionPlace
	if member {
		if ld != nil && rec.Crew.Task != "" {
			if as, err := ld.Attempts(ctx, rec.Crew.RunID); err == nil {
				if i := slices.IndexFunc(as, func(a ledger.AttemptRow) bool { return a.TaskID == rec.Crew.Task && a.N == rec.Crew.Attempt }); i >= 0 {
					place.commit = as[i].StartCommit
				}
			}
		}
		// A crew member's project is its run's.
		if cwd := h.runCwd(ctx, ld, rec.Crew.RunID); cwd != "" {
			place.project = pm.byRepo[h.repoIdent(ctx, cwd).common]
		}
	} else if !rec.Host.Hosted && rec.RepoCommon != "" {
		place.project = pm.byRepo[rec.RepoCommon]
	}
	entries, links := sessionEntries(rec, place)
	return ix.Replace(ctx, history.Source{Key: key, Mark: mark}, entries, links)
}

// runCwd is a run's folder: its record's, else its crew's as the ledger
// keeps it.
func (h *historyIndexer) runCwd(ctx context.Context, ld indexLedger, run string) string {
	if b, ok, _ := h.readRunRecord(run); ok {
		var r crew.Run
		if json.Unmarshal(b, &r) == nil && r.Cwd != "" {
			return r.Cwd
		}
	}
	if ld != nil {
		if row, ok, err := ld.Run(ctx, run); err == nil && ok {
			var c crew.Crew
			if json.Unmarshal([]byte(row.Crew), &c) == nil {
				return c.Cwd
			}
		}
	}
	return ""
}

// readRunRecord is the bytes of runs/<id>.json, false when there is none.
func (h *historyIndexer) readRunRecord(id string) ([]byte, bool, error) {
	if !crew.ValidRunID(id) {
		return nil, false, nil
	}
	f, err := h.data.OpenDoc("runs/" + id + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRunRecord+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxRunRecord {
		return nil, false, fmt.Errorf("the run record %s is past %d bytes", id, maxRunRecord)
	}
	return b, true, nil
}

// indexRun reads a run: its mark L<revision>/R<sha256> (the ledger's
// revision read before the rows, the record file's hash as read), its own
// source read without any log, each finished receipt not indexed yet read
// once (its log by ledger.Check, for that one row), and its members'
// session sources marked, since their marks carry its revision.
func (h *historyIndexer) indexRun(ctx context.Context, ix *history.Index, id string) error {
	key := hRunRef(id)
	now := h.now()
	if last, ok := h.readAt[key]; ok && now.Before(last.Add(minRunInterval)) {
		h.due[key] = last.Add(minRunInterval)
		return nil
	}
	ld := h.currentLedger()
	var ptr ledger.RunPointer
	inLedger := false
	if ld != nil {
		var err error
		ptr, inLedger, err = ld.Pointer(ctx, id)
		switch {
		case errors.Is(err, ledger.ErrUnavailable):
			ld, inLedger = nil, false
			h.setNoLedger(true)
		case err != nil:
			return err
		}
	}
	recBytes, hasRec, err := h.readRunRecord(id)
	if err != nil {
		return err
	}
	if !inLedger && !hasRec {
		delete(h.readAt, key)
		return h.removeRun(ctx, ix, id)
	}
	var marks []string
	if inLedger {
		marks = append(marks, "L"+strconv.FormatInt(ptr.Revision, 10))
	}
	if hasRec {
		sum := sha256.Sum256(recBytes)
		marks = append(marks, "R"+hex.EncodeToString(sum[:]))
	}
	mark := strings.Join(marks, "/")
	if old, ok, err := ix.Mark(ctx, key); err != nil {
		return err
	} else if ok && old == mark {
		return nil
	}
	h.afterMark(key)
	h.readAt[key] = now
	var rec *crew.Run
	if hasRec {
		var r crew.Run
		if json.Unmarshal(recBytes, &r) == nil && r.ID == id {
			rec = &r
		}
	}
	var rows *runRows
	var chain []runRows
	if inLedger {
		r, err := readRunRows(ctx, ld, id)
		if err != nil {
			return err
		}
		rows = &r
		seen := map[string]bool{id: true}
		for from := r.row.ContinuedFrom; from != "" && !seen[from] && len(chain) < maxRunChain; {
			seen[from] = true
			c, err := readRunRows(ctx, ld, from)
			if errors.Is(err, errNoRun) {
				break
			}
			if err != nil {
				return err
			}
			chain = append(chain, c)
			from = c.row.ContinuedFrom
		}
	}
	p := h.runPlaceOf(ctx, id, rec, rows)
	entries, links := runSource(p, rec, rows, chain)
	if rows != nil {
		if err := h.indexReceipts(ctx, ix, ld, p, rows); err != nil {
			return err
		}
	}
	if err := ix.Replace(ctx, history.Source{Key: key, Mark: mark}, entries, links); err != nil {
		return err
	}
	// The members' sessions: their marks carry the run's revision.
	if rows != nil {
		for _, a := range rows.attempts {
			if a.SessionID != "" {
				h.mark(hSessionRef(a.SessionID))
			}
		}
	}
	if rec != nil {
		for _, m := range rec.Members {
			if m.SessionID != "" {
				h.mark(hSessionRef(m.SessionID))
			}
		}
	}
	return nil
}

func (h *historyIndexer) setNoLedger(gone bool) {
	h.mu.Lock()
	h.noLedger = gone
	h.mu.Unlock()
}

// errNoRun is a run the ledger has no row of.
var errNoRun = errors.New("no such run")

// readRunRows reads a run's rows, its checks without their logs.
func readRunRows(ctx context.Context, ld indexLedger, id string) (runRows, error) {
	var r runRows
	row, ok, err := ld.Run(ctx, id)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, errNoRun
	}
	r.row = row
	if r.tasks, err = ld.Tasks(ctx, id); err != nil {
		return r, err
	}
	if r.attempts, err = ld.Attempts(ctx, id); err != nil {
		return r, err
	}
	if r.commands, err = ld.Commands(ctx, id); err != nil {
		return r, err
	}
	if r.messages, err = ld.Messages(ctx, id); err != nil {
		return r, err
	}
	if r.checks, err = ld.Checks(ctx, id, "", 0, false); err != nil {
		return r, err
	}
	return r, nil
}

// runPlaceOf is what a run's entries take from beyond its rows.
func (h *historyIndexer) runPlaceOf(ctx context.Context, id string, rec *crew.Run, rows *runRows) runPlace {
	p := runPlace{run: id, host: hField(h.host), kinds: map[string]string{}, agents: map[string]string{}, agentSessions: map[string]string{}}
	var c crew.Crew
	if rows != nil {
		_ = json.Unmarshal([]byte(rows.row.Crew), &c)
		p.crew, p.fallback = rows.row.CrewID, rows.row.CreatedAt
	}
	cwd := c.Cwd
	if rec != nil {
		p.crew, cwd = cmp.Or(p.crew, rec.CrewID), cmp.Or(rec.Cwd, cwd)
		if p.fallback.IsZero() {
			p.fallback = rec.StartedAt
		}
		if rec.Ticket != nil {
			p.ticket = rec.Ticket.Key
		}
		for _, m := range rec.Members {
			p.agents["member:"+m.Name] = m.AgentID
			if m.AgentSession != nil && m.Task != "" {
				p.agentSessions[m.Task+"/"+strconv.Itoa(m.Attempt)] = m.AgentSession.ID
			}
		}
		for _, t := range rec.Tasks {
			p.kinds[t.ID] = cmp.Or(p.kinds[t.ID], t.Kind)
		}
	}
	for _, m := range c.Members {
		p.agents["member:"+m.Name] = cmp.Or(p.agents["member:"+m.Name], m.AgentID)
	}
	for _, t := range c.Tasks {
		p.kinds[t.ID] = cmp.Or(t.Kind, p.kinds[t.ID])
		p.agents[t.ID] = p.agents["member:"+t.Member]
	}
	if rows != nil {
		for _, t := range rows.tasks {
			p.kinds[t.ID] = cmp.Or(t.Kind, p.kinds[t.ID])
			p.agents[t.ID] = cmp.Or(p.agents[t.ID], p.agents["member:"+t.Member])
		}
	}
	if repo := h.repoIdent(ctx, cwd); repo.common != "" {
		p.repo, p.tree = hFitID(repo.common), hFitID(repo.top)
		p.project = h.projectsOf(ctx).byRepo[repo.common]
	}
	return p
}

// indexReceipts indexes each finished receipt the index does not hold
// yet as its own source, its log read by ledger.Check for that one row:
// a receipt recorded never changes once finished, so it is read once.
func (h *historyIndexer) indexReceipts(ctx context.Context, ix *history.Index, ld indexLedger, p runPlace, rows *runRows) error {
	held, err := checkSources(ctx, ix, p.run)
	if err != nil {
		return err
	}
	branch := map[string]string{}
	for _, a := range rows.attempts {
		branch[a.TaskID+"/"+strconv.Itoa(a.N)] = a.Branch
	}
	for _, r := range rows.checks {
		key := hCheckRef(p.run, r.TaskID, r.N, r.ID, r.K)
		if !receiptFinished(r) || held[key] {
			continue
		}
		full := r
		if historyOutcome(r) == "failed" && !r.LogDropped {
			row, ok, err := ld.Check(ctx, p.run, r.TaskID, r.N, r.ID, r.K)
			if err != nil {
				return err
			}
			if ok {
				full = row
			}
		}
		e := checkEntry(p, full, branch[r.TaskID+"/"+strconv.Itoa(r.N)])
		if err := ix.Replace(ctx, history.Source{Key: key, Mark: ledger.CheckFinished}, []history.Entry{e}, nil); err != nil {
			return err
		}
	}
	return nil
}

// checkSources is the receipts of run the index holds as sources.
func checkSources(ctx context.Context, ix *history.Index, run string) (map[string]bool, error) {
	prefix := "check:" + run + "/"
	held := map[string]bool{}
	after := prefix
	for {
		page, err := ix.Sources(ctx, after, history.MaxSourcesPage)
		if err != nil {
			return nil, err
		}
		for _, s := range page {
			if !strings.HasPrefix(s.Key, prefix) {
				return held, nil
			}
			held[s.Key] = true
		}
		if len(page) < history.MaxSourcesPage {
			return held, nil
		}
		after = page[len(page)-1].Key
	}
}

// removeRun removes a run that is gone (pruned, its record deleted) with
// every receipt it owned.
func (h *historyIndexer) removeRun(ctx context.Context, ix *history.Index, id string) error {
	held, err := checkSources(ctx, ix, id)
	if err != nil {
		return err
	}
	for key := range held {
		if err := ix.Remove(ctx, key); err != nil {
			return err
		}
	}
	return ix.Remove(ctx, hRunRef(id))
}

// catchUp compares every source's mark with the index's: every source the
// stores hold is read (its pass reads nothing past its mark when the two
// agree), and every source the index holds that no store does is
// removed. sessionsOnly reads the session sources alone (a project's
// change).
func (h *historyIndexer) catchUp(ctx context.Context, sessionsOnly bool) {
	ix := h.index()
	if ix == nil {
		return
	}
	h.setState(indexCatchingUp)
	keys, complete, err := h.storeSources(ctx, sessionsOnly)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("the history's catch-up could not list the sources; trying again", "err", err)
			h.mu.Lock()
			if sessionsOnly {
				h.sessions = true
			} else {
				h.overflow = true
			}
			h.mu.Unlock()
			h.backoff = min(max(2*h.backoff, historyBackoffMin), historyBackoffMax)
			select {
			case <-ctx.Done():
			case <-h.after(h.backoff):
			}
		}
		return
	}
	has := map[string]bool{}
	for _, k := range keys {
		has[k] = true
	}
	// What the index holds that the stores do not (when every store was
	// listed): a receipt's run read, which removes it with its receipts.
	after := ""
	for complete {
		page, err := ix.Sources(ctx, after, history.MaxSourcesPage)
		if err != nil {
			h.log.Warn("the history's catch-up could not list the index", "err", err)
			break
		}
		for _, s := range page {
			k := s.Key
			if rest, ok := strings.CutPrefix(k, "check:"); ok {
				run, _, _ := strings.Cut(rest, "/")
				k = hRunRef(run)
			}
			if sessionsOnly && !strings.HasPrefix(k, "session:") {
				continue
			}
			if (strings.HasPrefix(k, "session:") || strings.HasPrefix(k, "run:")) && !has[k] {
				has[k] = true
				keys = append(keys, k)
			}
		}
		if len(page) < history.MaxSourcesPage {
			break
		}
		after = page[len(page)-1].Key
	}
	h.mu.Lock()
	h.behind = len(keys)
	h.mu.Unlock()
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		if strings.HasPrefix(k, "run:") {
			delete(h.readAt, k) // a catch-up reads every run at once
		}
		h.pass(ctx, k)
		h.mu.Lock()
		h.behind--
		h.mu.Unlock()
	}
	h.setState(indexReady)
}

// storeSources lists the sources the stores hold: every session record,
// every run the ledger has (when there is one to read) and every run
// record. complete is false when a store could not be listed whole (no
// ledger in this process is not that: its runs are then the records').
func (h *historyIndexer) storeSources(ctx context.Context, sessionsOnly bool) ([]string, bool, error) {
	var keys []string
	for after := ""; ; {
		page, err := h.journal.List(after, journal.MaxPage)
		if err != nil {
			return nil, false, err
		}
		for _, r := range page.Records {
			keys = append(keys, hSessionRef(r.ID))
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if sessionsOnly {
		return keys, true, nil
	}
	runs := map[string]bool{}
	ld := h.currentLedger()
	h.setNoLedger(ld == nil)
	if ld != nil {
		for after := ""; ; {
			ps, err := ld.Pointers(ctx, after, ledger.MaxPointers)
			if errors.Is(err, ledger.ErrUnavailable) {
				h.setNoLedger(true)
				break
			}
			if err != nil {
				return nil, false, err
			}
			for _, p := range ps {
				runs[p.ID] = true
			}
			if len(ps) < ledger.MaxPointers {
				break
			}
			after = ps[len(ps)-1].ID
		}
	}
	docs, err := h.data.ListDocs("runs")
	if err != nil {
		return nil, false, err
	}
	for _, d := range docs {
		if id, ok := strings.CutSuffix(d.Name, ".json"); ok && crew.ValidRunID(id) {
			runs[id] = true
			// The reconcile's first look: what the catch-up reads now.
			h.runStats[hRunRef(id)] = fileStat{size: d.Size, mod: d.ModTime}
		}
	}
	for id := range runs {
		keys = append(keys, hRunRef(id))
	}
	slices.Sort(keys)
	return keys, true, nil
}

// reconcile finds what another server wrote, which no hook of this
// process saw: the ledger's run revisions against the indexed marks, and
// the run records' files by size and modification time (a file whose
// stat changed is read, and its hash compared by its pass). Dispatch's
// projects are read again, a change of them reading every session.
func (h *historyIndexer) reconcile(ctx context.Context) {
	ix := h.index()
	if ix == nil {
		return
	}
	marks := map[string]string{}
	for after := "run:"; ; {
		page, err := ix.Sources(ctx, after, history.MaxSourcesPage)
		if err != nil {
			h.log.Warn("the history's reconcile could not read the index", "err", err)
			return
		}
		done := len(page) < history.MaxSourcesPage
		for _, s := range page {
			if !strings.HasPrefix(s.Key, "run:") {
				done = true
				break
			}
			marks[s.Key] = s.Mark
		}
		if done {
			break
		}
		after = page[len(page)-1].Key
	}
	if ld := h.currentLedger(); ld != nil {
		for after := ""; ; {
			ps, err := ld.Pointers(ctx, after, ledger.MaxPointers)
			if err != nil {
				break
			}
			for _, p := range ps {
				key := hRunRef(p.ID)
				if rev, ok := ledgerRevOf(marks[key]); !ok || rev != p.Revision {
					h.mark(key)
				}
			}
			if len(ps) < ledger.MaxPointers {
				break
			}
			after = ps[len(ps)-1].ID
		}
	}
	if docs, err := h.data.ListDocs("runs"); err == nil {
		seen := map[string]bool{}
		for _, d := range docs {
			id, ok := strings.CutSuffix(d.Name, ".json")
			if !ok || !crew.ValidRunID(id) {
				continue
			}
			key := hRunRef(id)
			seen[key] = true
			st := fileStat{size: d.Size, mod: d.ModTime}
			_, indexed := marks[key]
			if old, ok := h.runStats[key]; !ok || old != st || !indexed {
				h.runStats[key] = st
				h.mark(key)
			}
		}
		for key := range h.runStats {
			if !seen[key] {
				delete(h.runStats, key)
				h.mark(key)
			}
		}
	}
	old := ""
	if h.pm != nil {
		old = h.pm.digest
	}
	h.mu.Lock()
	h.projects = true
	h.mu.Unlock()
	if pm := h.projectsOf(ctx); old != "" && pm.digest != old {
		h.mu.Lock()
		h.sessions = true
		h.mu.Unlock()
	}
}

// ledgerRevOf is the ledger's revision a run's mark carries (L<rev>).
func ledgerRevOf(mark string) (int64, bool) {
	l, ok := strings.CutPrefix(mark, "L")
	if !ok {
		return 0, false
	}
	l, _, _ = strings.Cut(l, "/")
	rev, err := strconv.ParseInt(l, 10, 64)
	return rev, err == nil
}

// doReindex clears the index in place and catches up from nothing.
func (h *historyIndexer) doReindex(ctx context.Context) {
	ix := h.index()
	if ix == nil {
		return
	}
	if err := ix.Clear(ctx); err != nil {
		if ctx.Err() == nil {
			h.log.Warn("the history's index could not be cleared for a reindex", "err", err)
		}
		return
	}
	clear(h.readAt)
	h.catchUp(ctx, false)
}

// handleHistoryReindex answers POST /api/history/reindex: the index
// cleared in place and caught up from nothing, on the indexer's goroutine.
func (s *Server) handleHistoryReindex(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	if ro, _ := s.sessionRecords.ReadOnly(); ro || s.history == nil {
		writeError(w, http.StatusConflict, "history_read_only", "another server owns the history")
		return
	}
	s.history.askReindex()
	writeJSON(w, http.StatusAccepted, map[string]any{"indexing": s.history.indexing()})
}

// openHistoryIndexer makes the indexer of a server with session records
// (none without: the index's writer is the journal's lock holder).
func (s *Server) openHistoryIndexer(fan *commitFan) {
	if s.sessionRecords == nil || s.store == nil {
		return
	}
	host, _ := os.Hostname()
	s.history = newHistoryIndexer(s.store, s.sessionRecords, func() indexLedger {
		if s.ledger == nil {
			return nil
		}
		return s.ledger
	}, s.dispatch, host, s.log)
	fan.add(s.history.observer())
}

// stopHistory ends the indexer before the stores it reads close.
func (s *Server) stopHistory() {
	if s.history != nil {
		s.history.stop()
	}
}
