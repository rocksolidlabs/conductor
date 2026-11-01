package dispatch

import (
	"context"
	"slices"
)

// The sync seam's hook (docs/plans/ext.md, C6): an observer told, in
// commit order, of what every committed operation changed, and the log's
// head, the position a replay names.

// OnCommit gives the store fn, called once after every commit with what
// it changed (one Change per entity, as OnChange is handed them), under
// the lock that orders the store's writers, so the calls come in commit
// order and each Change's Seq is greater than the last call's; never for
// an operation that failed, was refused or was answered from its receipt.
// fn is handed a copy of its own; it must only queue what it is told: it
// holds every writer of the store while it runs, and it must not call the
// store. OnChange, which feeds the pages, is called after it, outside the
// lock, as before. Set it before the first commit it is to be told of; a
// later call replaces it.
func (s *Store) OnCommit(fn func([]Change)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCommit = fn
}

// committed tells OnCommit's fn what a commit changed. The caller holds
// mu.
func (s *Store) committed(changes []Change) {
	if s.afterCommit != nil {
		s.afterCommit()
	}
	if s.onCommit != nil {
		s.onCommit(slices.Clone(changes))
	}
}

// Head answers the position of the log's last change (its seq), 0 for an
// empty log: every change committed after it has a greater one.
func (s *Store) Head(ctx context.Context) (int64, error) {
	var seq int64
	err := s.read(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM changes").Scan(&seq)
	})
	return seq, err
}
