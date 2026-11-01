package api

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The sync adapter (docs/plans/ext.md, C6): what the data directory, the
// ledger and Dispatch commit, told to the sync store (ext.ContinuityStore)
// after each local commit.
//
// Each of the three hooks hands its commits over in commit order, under
// the lock that orders them (the data directory's observer mutex, shared
// by the store and its Subs; the ledger's lock; Dispatch's), and the
// adapter only marks the document pending there, never waiting. What
// reaches the sync store is an allowlist: catalog.json, crews/<id>.json and
// runs/<id>.json from the data directory (the catalog masked, every env
// value catalog.RedactedValue), a run's pointer from the ledger (never the
// crew as launched, its rows or its logs) and a reference to each Dispatch
// entity a commit changed. The workbench token, links/, tls/, hooks/, ext/
// and every other file never reach it, and nothing a sync store answers is
// written back.
//
// The pending set holds one entry per document, in the order the documents
// became pending, updated in place, and never bytes: one goroutine of the
// adapter's own delivers the oldest as it is then (a data document read
// through store.OpenDoc, a run's pointer through ledger.Pointer; gone:
// Deleted), whole, in parts of ext.MaxDocData from one open file past
// that. An entry leaves the set only when its whole document is
// acknowledged (one committed again meanwhile is pending again, at the
// back); a delivery that fails stays at the head and is tried again
// whole after a backoff (syncRetryFirst doubling to syncRetryMost, reset by
// a success), so nothing behind it goes first and nothing needs a further
// local write. Every epoch begins with a replay of the whole state: a
// `replay` doc with a new random epoch, every allowlisted data document,
// every run pointer, Dispatch's `log` doc with the last change's position,
// then `replayed`, told only once all of them were acknowledged. One runs
// when the sync store's drain begins (its Start succeeded, or at once for
// one that needs none), so every server lifetime is an epoch of its own,
// and one in a new epoch when the set would pass ext.SyncPending, which it
// then is emptied for: nothing is dropped and no gap is left.

// The stores and markers of a sync doc (ext.Doc.Store).
const (
	syncData     = "data"
	syncLedger   = "ledger"
	syncDispatch = "dispatch"
	syncReplay   = "replay"
	syncReplayed = "replayed"
)

// syncLogPath is the path of the Dispatch doc that names the log's position.
const syncLogPath = "log"

// The backoff between two tries of a failed delivery.
const (
	syncRetryFirst = time.Second
	syncRetryMost  = 5 * time.Minute
)

// syncPage is how many run pointers a replay reads at a time.
const syncPage = 500

// syncDataDoc is the allowlist of the data directory's documents.
var syncDataDoc = regexp.MustCompile(`^(catalog\.json|crews/[a-z0-9-]+\.json|runs/[a-z0-9-]+\.json)$`)

// syncKey names a document of the pending set: its store and its path.
type syncKey struct{ store, path string }

// syncEntry is one pending document: what was last committed of it (a data
// document's commit number, a Dispatch entity's change and reference) and
// how often it changed while pending (ver), so a delivery acknowledged
// for an older state leaves it pending. listed is an entry a replay put in
// the set, which an overflow does not count.
type syncEntry struct {
	key    syncKey
	rev    int64
	ref    []byte
	ver    uint64
	listed bool
}

// syncAdapter is the pending set and its delivery.
type syncAdapter struct {
	s    *Server // set by attach, before the drain begins
	data *store.Store

	mu    sync.Mutex
	order *list.List // of *syncEntry, oldest first
	index map[syncKey]*list.Element
	// listed counts the entries a replay put in the set.
	listed int
	// gen counts the replays begun; due says one is wanted (an overflow
	// emptied the set); epoch is the current one's.
	gen   uint64
	due   bool
	epoch string
	wake  chan struct{}
	// limit is ext.SyncPending; after is the backoff's clock (time.After),
	// a test's own otherwise; warned is when a document that could not be
	// read was last logged.
	limit  int
	after  func(time.Duration) <-chan time.Time
	warned time.Time
	// afterReset, when a test sets it, runs as a replay has emptied the
	// set and put its marker in, before it lists anything.
	afterReset func()
}

// newSyncAdapter is an adapter with nothing pending, observing data (nil
// for a server with no data directory) from now on.
func newSyncAdapter(data *store.Store) *syncAdapter {
	a := &syncAdapter{data: data, order: list.New(), index: map[syncKey]*list.Element{}, wake: make(chan struct{}, 1), limit: ext.SyncPending}
	if data != nil {
		data.Observe(a.dataCommitted)
	}
	return a
}

// attach gives the adapter its server, once the server is made: what it
// reads the ledger and Dispatch through, and delivers to.
func (a *syncAdapter) attach(s *Server) { a.s = s }

// detach stops observing the data directory: New failed after the adapter
// began observing it.
func (a *syncAdapter) detach() {
	if a.data != nil {
		a.data.Observe(nil)
	}
}

// observeLedger installs the ledger's hooks: openLedger calls it before
// the ledger's recovery, so its transitions are told.
func (a *syncAdapter) observeLedger(ld *ledger.Ledger) {
	ld.OnCommit(func(p ledger.RunPointer) { a.mark(syncKey{syncLedger, p.ID}, p.Revision, nil) })
	ld.OnPrune(func(id string) { a.mark(syncKey{syncLedger, id}, 0, nil) })
}

// ledgerOwned asks for a replay in a new epoch: the ledger has taken the
// owner lock after its Open (openLedger, once its recovery is done), so
// what the server it waited for wrote meanwhile, to the ledger or to the
// data directory, which no hook here saw, is told too. Before the drain
// begins it changes nothing (the drain's first act is a replay).
func (a *syncAdapter) ledgerOwned() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.due = true
	a.signal()
}

// observeDispatch installs Dispatch's hook, as the store opens.
func (a *syncAdapter) observeDispatch(ds *dispatch.Store) {
	ds.OnCommit(func(cs []dispatch.Change) {
		for _, c := range cs {
			ref, _ := json.Marshal(syncRef{Kind: c.Entity, ID: c.ID, Ticket: c.Ticket, Project: c.Project})
			a.mark(syncKey{syncDispatch, c.Entity + "/" + c.ID}, c.Seq, ref)
		}
	})
}

// syncRef is a Dispatch entity's doc: which entity a commit changed, and
// the ticket and project it belongs to. Its rows are read from Dispatch
// itself (the sync item's own, CS1).
type syncRef struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Ticket  string `json:"ticket,omitempty"`
	Project string `json:"project,omitempty"`
}

// syncPointer is a run's pointer as its doc carries it.
type syncPointer struct {
	ID            string    `json:"id"`
	Crew          string    `json:"crew"`
	State         string    `json:"state"`
	Outcome       string    `json:"outcome,omitempty"`
	Paused        bool      `json:"paused,omitempty"`
	Revision      int64     `json:"revision"`
	ContinuedFrom string    `json:"continuedFrom,omitempty"`
	ContinuedBy   string    `json:"continuedBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt,omitzero"`
	ClosedAt      time.Time `json:"closedAt,omitzero"`
}

// dataCommitted is the data directory's observer: an allowlisted
// document's commit marks it pending. The store calls it under its commit
// mutex.
func (a *syncAdapter) dataCommitted(ev store.Event) {
	if syncDataDoc.MatchString(ev.Path) {
		a.mark(syncKey{syncData, ev.Path}, ev.Seq, nil)
	}
}

// mark makes the document key pending with what was committed of it: its
// entry updated in place when it is pending already, else a new one at
// the back. A new entry that would make the set pass its limit (the
// entries a replay listed aside) empties it instead, for a replay in a new
// epoch. It never waits on anything but the set's own lock.
func (a *syncAdapter) mark(key syncKey, rev int64, ref []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if el, ok := a.index[key]; ok {
		e := el.Value.(*syncEntry)
		e.rev, e.ref, e.ver = rev, ref, e.ver+1
		return
	}
	if a.order.Len()-a.listed >= a.limit {
		a.clear()
		a.due = true
		a.signal()
		return
	}
	a.index[key] = a.order.PushBack(&syncEntry{key: key, rev: rev, ref: ref})
	a.signal()
}

// clear empties the set. The caller holds mu.
func (a *syncAdapter) clear() {
	a.order.Init()
	a.index = map[syncKey]*list.Element{}
	a.listed = 0
}

// signal wakes the delivery. The caller holds mu.
func (a *syncAdapter) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// pending reports how many documents and markers wait, and the epoch.
func (a *syncAdapter) pending() (int, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.order.Len(), a.epoch
}

// begin starts the delivery on a goroutine of the server's own (track),
// ended with the server's extensions: the sync store's drain, begun once
// its Start has succeeded (or once New can no longer fail, for one that
// needs none), so its first act is the lifetime's replay.
func (a *syncAdapter) begin() {
	ctx := a.s.extCtx
	a.s.track(func() { a.run(ctx) })
}

// run replays the whole state, then delivers what is pending, oldest
// first, until ctx ends.
func (a *syncAdapter) run(ctx context.Context) {
	a.mu.Lock()
	a.due = true
	a.mu.Unlock()
	delay := syncRetryFirst
	for ctx.Err() == nil {
		a.mu.Lock()
		if a.due {
			a.mu.Unlock()
			if err := a.replay(ctx); err != nil {
				a.warn("sync: the replay could not list the state; it is tried again", err)
				if !a.backoff(ctx, &delay) {
					return
				}
			}
			continue
		}
		el := a.order.Front()
		if el == nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
			}
			continue
		}
		e := *el.Value.(*syncEntry)
		epoch := a.epoch
		a.mu.Unlock()
		if err := a.deliver(ctx, epoch, e); err != nil {
			if !a.backoff(ctx, &delay) {
				return
			}
			continue
		}
		delay = syncRetryFirst
		a.ack(el, e)
	}
}

// backoff waits delay, or until ctx ends (false), and doubles delay up to
// syncRetryMost.
func (a *syncAdapter) backoff(ctx context.Context, delay *time.Duration) bool {
	a.mu.Lock()
	after := a.after
	a.mu.Unlock()
	if after == nil {
		after = time.After
	}
	select {
	case <-ctx.Done():
		return false
	case <-after(*delay):
	}
	*delay = min(2**delay, syncRetryMost)
	return true
}

// ack takes the entry el out of the set, its document acknowledged as e
// held it, unless the set no longer holds it (a replay emptied it). One
// committed again while it was delivered became pending again then: it
// moves to the back, to be delivered as it is by then, so a document
// written faster than it is delivered never holds up those behind it.
func (a *syncAdapter) ack(el *list.Element, e syncEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.index[e.key] != el {
		return
	}
	cur := el.Value.(*syncEntry)
	if cur.listed {
		cur.listed = false
		a.listed--
	}
	if cur.ver != e.ver {
		a.order.MoveToBack(el)
		return
	}
	a.order.Remove(el)
	delete(a.index, e.key)
}

// replay empties the set and lists the whole state into it, in a new
// epoch: the `replay` marker, every allowlisted data document, every run
// pointer, Dispatch's log, then `replayed`. A document committed while it
// lists is marked as any other (one entry per document), so nothing is
// missed: the data directory's sequence is read before its directories
// are. An overflow meanwhile ends the listing, for a replay again.
func (a *syncAdapter) replay(ctx context.Context) error {
	epoch, err := newEpoch()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.clear()
	a.due = false
	a.gen++
	gen := a.gen
	a.epoch = epoch
	// The marker under the reset's lock: a document committed from here on
	// is pending behind it, in the new epoch, never delivered before the
	// marker that would then drop it from the store's named set.
	a.push(syncKey{store: syncReplay}, 0)
	hook := a.afterReset
	a.mu.Unlock()
	if hook != nil {
		hook()
	}
	if a.data != nil {
		seq := a.data.Seq()
		for _, dir := range []string{"", "crews", "runs"} {
			docs, err := a.data.ListDocs(dir)
			if err != nil {
				return a.redo(gen, err)
			}
			for _, d := range docs {
				path := d.Name
				if dir != "" {
					path = dir + "/" + d.Name
				}
				if syncDataDoc.MatchString(path) && !a.list(gen, syncKey{syncData, path}, seq) {
					return nil
				}
			}
		}
	}
	if ld := a.s.ledger; ld != nil {
		after := ""
		for {
			page, err := ld.Pointers(ctx, after, syncPage)
			if errors.Is(err, ledger.ErrUnavailable) && ld.NoDatabase() {
				break // no database file: it holds no run
			}
			if err != nil {
				// A database that is there but cannot be read (damaged, its
				// lock file failed) is not an empty one: the replay is not
				// complete, so its `replayed` would drop every run's pointer
				// the store holds. It is tried again after the backoff.
				return a.redo(gen, err)
			}
			for _, p := range page {
				if !a.list(gen, syncKey{syncLedger, p.ID}, p.Revision) {
					return nil
				}
			}
			if len(page) < syncPage {
				break
			}
			after = page[len(page)-1].ID
		}
	}
	if a.s.dispatch != nil && !a.list(gen, syncKey{syncDispatch, syncLogPath}, 0) {
		return nil
	}
	a.list(gen, syncKey{store: syncReplayed}, 0)
	return nil
}

// redo marks the replay gen due again, its listing having failed with
// err, which it answers.
func (a *syncAdapter) redo(gen uint64, err error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen == gen {
		a.clear()
		a.due = true
	}
	return err
}

// list puts the document key in the set for the replay gen, at the back
// unless it is pending already, and reports whether that replay still
// stands (no overflow, no later replay).
func (a *syncAdapter) list(gen uint64, key syncKey, rev int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen != gen || a.due {
		return false
	}
	a.push(key, rev)
	return true
}

// push puts the document key in the set for a replay, at the back unless
// it is pending already. The caller holds mu.
func (a *syncAdapter) push(key syncKey, rev int64) {
	if _, ok := a.index[key]; !ok {
		a.index[key] = a.order.PushBack(&syncEntry{key: key, rev: rev, listed: true})
		a.listed++
		a.signal()
	}
}

// newEpoch is a random epoch: 32 hex characters.
func newEpoch() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// deliver hands e's document to the sync store as it is now, in epoch: an
// error (the store's, or one reading the document here) leaves it
// pending.
func (a *syncAdapter) deliver(ctx context.Context, epoch string, e syncEntry) error {
	d := ext.Doc{Store: e.key.store, Path: e.key.path, Epoch: epoch, Rev: e.rev, At: time.Now().UTC()}
	sy := a.s.exts.sync
	switch e.key.store {
	case syncReplay, syncReplayed:
		d.Rev = 0
		return sy.Saved(ctx, d)
	case syncLedger:
		p, ok, err := a.s.ledger.Pointer(ctx, e.key.path)
		if err != nil {
			a.warn("sync: a run's pointer could not be read; it is tried again", err)
			return err
		}
		if !ok {
			return sy.Deleted(ctx, d)
		}
		d.Rev = p.Revision
		d.Data, _ = json.Marshal(syncPointer{ID: p.ID, Crew: p.CrewID, State: p.State, Outcome: p.Outcome, Paused: p.Paused, Revision: p.Revision,
			ContinuedFrom: p.ContinuedFrom, ContinuedBy: p.ContinuedBy, CreatedAt: p.CreatedAt, ClosedAt: p.ClosedAt})
		d.Size = int64(len(d.Data))
		return sy.Saved(ctx, d)
	case syncDispatch:
		if e.key.path == syncLogPath {
			head, err := a.s.dispatch.Head(ctx)
			if err != nil {
				a.warn("sync: Dispatch's log could not be read; it is tried again", err)
				return err
			}
			d.Rev = head
			return sy.Saved(ctx, d)
		}
		d.Data = e.ref
		d.Size = int64(len(d.Data))
		return sy.Saved(ctx, d)
	}
	return a.deliverData(ctx, d)
}

// deliverData hands the data document d names to the sync store: gone,
// Deleted; the catalog masked; any other whole, in parts past
// ext.MaxDocData, each read from one open file, so every part is of one
// version of it.
func (a *syncAdapter) deliverData(ctx context.Context, d ext.Doc) error {
	sy := a.s.exts.sync
	f, err := a.openDoc(d.Path)
	if errors.Is(err, os.ErrNotExist) {
		return sy.Deleted(ctx, d)
	}
	if err != nil {
		a.warn("sync: a document could not be read; it is tried again", err)
		return err
	}
	defer f.Close()
	if d.Path == catalogFile {
		b, err := io.ReadAll(f)
		if err != nil {
			a.warn("sync: the catalog could not be read; it is tried again", err)
			return err
		}
		// One that does not decode goes with no data.
		masked, _ := maskCatalogDoc(b)
		return a.parts(ctx, d, int64(len(masked)), func(off int64, p []byte) error {
			copy(p, masked[off:])
			return nil
		})
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return a.parts(ctx, d, fi.Size(), func(off int64, p []byte) error {
		_, err := f.ReadAt(p, off)
		return err
	})
}

// parts delivers d's document of size bytes as Saved docs of at most
// ext.MaxDocData bytes each, read by read at their offset, in order: one
// doc for a document that fits (none of data for an empty one). The first
// part that fails ends it, and the whole document is delivered again.
func (a *syncAdapter) parts(ctx context.Context, d ext.Doc, size int64, read func(off int64, p []byte) error) error {
	d.Size = size
	for off := int64(0); ; {
		n := min(size-off, int64(ext.MaxDocData))
		part := d
		part.Offset, part.Data = off, nil
		if n > 0 {
			// A part of its own: a store may keep what it was given.
			part.Data = make([]byte, n)
			if err := read(off, part.Data); err != nil {
				a.warn("sync: a document could not be read; it is tried again", err)
				return err
			}
		}
		if err := a.s.exts.sync.Saved(ctx, part); err != nil {
			return err
		}
		off += n
		if off >= size {
			return nil
		}
	}
}

// openDoc opens a data document through the store, again when a save
// renamed another in its place between the store's check and its open.
func (a *syncAdapter) openDoc(path string) (*os.File, error) {
	for range 3 {
		f, err := a.data.OpenDoc(path)
		if !errors.Is(err, store.ErrChanged) {
			return f, err
		}
	}
	return nil, fmt.Errorf("sync: %s kept changing as it was opened", path)
}

// warn logs a document the adapter could not read, at most once a minute.
func (a *syncAdapter) warn(msg string, err error) {
	a.mu.Lock()
	now := time.Now()
	if !a.warned.IsZero() && now.Sub(a.warned) < extLogEvery {
		a.mu.Unlock()
		return
	}
	a.warned = now
	a.mu.Unlock()
	a.s.log.Warn(msg, "err", err)
}

// maskCatalogDoc is catalog.json's bytes as a sync store is given them:
// the overlay decoded, every env value of every agent (its own and its
// yolo recipe's) catalog.RedactedValue, re-encoded as the store writes it;
// nil, with the error, for bytes that do not decode.
func maskCatalogDoc(b []byte) ([]byte, error) {
	var ov catalog.Overlay
	if err := store.DecodeStrict(b, &ov); err != nil {
		return nil, err
	}
	for i := range ov.Agents {
		ov.Agents[i].Env = maskedEnv(ov.Agents[i].Env)
		if y := ov.Agents[i].Yolo; y != nil {
			own := *y
			own.Env = maskedEnv(y.Env)
			ov.Agents[i].Yolo = &own
		}
	}
	return store.Encode(ov)
}

// maskedEnv is env with every value catalog.RedactedValue.
func maskedEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k := range env {
		out[k] = catalog.RedactedValue
	}
	return out
}
