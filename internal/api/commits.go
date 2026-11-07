package api

import (
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The commit hooks shared (docs/plans/history.md, PR-3). The data
// directory's observer (store.Observe), the ledger's OnCommit and OnPrune
// and Dispatch's OnCommit each hold one function; the sync adapter (C6)
// and the history's indexer both need all of them. commitFan installs the
// one function each hook takes and calls, in the order they were added,
// every observer registered with it: the sync adapter first, unchanged,
// then the indexer. Each observer only marks something pending and never
// waits, as the hooks require (they run under the stores' commit locks).

// commitObserver is what one observer takes of the commits; a nil field
// is a hook it does not want.
type commitObserver struct {
	data     func(store.Event)
	commit   func(ledger.RunPointer)
	prune    func(runID string, revision int64)
	dispatch func([]dispatch.Change)
}

// commitFan is the observers, in the order they were added. The list is
// replaced whole on add (copy on write), so a hook reads it without a lock.
type commitFan struct {
	observers atomic.Pointer[[]commitObserver]
}

func newCommitFan() *commitFan {
	f := &commitFan{}
	f.observers.Store(&[]commitObserver{})
	return f
}

// add registers o after every observer added before it.
func (f *commitFan) add(o commitObserver) {
	for {
		old := f.observers.Load()
		next := append(append([]commitObserver(nil), (*old)...), o)
		if f.observers.CompareAndSwap(old, &next) {
			return
		}
	}
}

func (f *commitFan) each(fn func(commitObserver)) {
	for _, o := range *f.observers.Load() {
		fn(o)
	}
}

// observeData installs the data directory's observer (nil st: none).
func (f *commitFan) observeData(st *store.Store) {
	if st != nil {
		st.Observe(f.dataCommitted)
	}
}

// detachData takes it off again: New failed after it was installed.
func (f *commitFan) detachData(st *store.Store) {
	if st != nil {
		st.Observe(nil)
	}
}

// observeLedger installs the ledger's hooks: openLedger calls it before
// the ledger's recovery, so its transitions are told.
func (f *commitFan) observeLedger(ld *ledger.Ledger) {
	ld.OnCommit(f.ledgerCommitted)
	ld.OnPrune(f.ledgerPruned)
}

// observeDispatch installs Dispatch's hook, as the store opens.
func (f *commitFan) observeDispatch(ds *dispatch.Store) { ds.OnCommit(f.dispatchCommitted) }

func (f *commitFan) dataCommitted(ev store.Event) {
	f.each(func(o commitObserver) {
		if o.data != nil {
			o.data(ev)
		}
	})
}

func (f *commitFan) ledgerCommitted(p ledger.RunPointer) {
	f.each(func(o commitObserver) {
		if o.commit != nil {
			o.commit(p)
		}
	})
}

func (f *commitFan) ledgerPruned(id string, revision int64) {
	f.each(func(o commitObserver) {
		if o.prune != nil {
			o.prune(id, revision)
		}
	})
}

func (f *commitFan) dispatchCommitted(cs []dispatch.Change) {
	f.each(func(o commitObserver) {
		if o.dispatch != nil {
			// Each observer its own copy, as Dispatch hands one.
			o.dispatch(append([]dispatch.Change(nil), cs...))
		}
	})
}
