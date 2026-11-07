package share

import (
	"sync"
	"time"
)

// A link's hold (docs/plans/decision-inbox.md, PR-5; internal/api's lane,
// rule 1). The promise is the plan's: nothing is begun for a link after
// its revoke or its expiry has marked it; an act begun before may land.
// An act taken through a decision link (an answer's process write, a task
// command) is held from the check that the link stands until it has
// returned. A revoke and an expiry both end the link through one barrier
// (end): from its start no act is held (Hold refuses it, as it refuses a
// link revoked or expired), and an act held asks once more at its commit
// boundary, just before it is begun (Held.Stands), fail-closed, so one
// whose link ended while it was held is never begun. The barrier then
// waits up to the store's HoldWait for the acts begun before it and
// returns, logging how many were still under way. What a begun act does
// is never undone: a process write the program has not taken by then is
// told so (Held.Cancelled) and resolves no question of the link, but the
// bytes it hands the program still land as the program reads them, and a
// command the engine committed stays committed.

// DefaultHoldWait is how long a revoke or an expiry waits for the acts
// begun before it, when Store.HoldWait is zero.
const DefaultHoldWait = 5 * time.Second

// linkActs are a link's acts under way and its barrier.
type linkActs struct {
	mu   sync.Mutex
	held map[*Held]struct{}
	// ended is closed as a revoke or an expiry begins; why says which
	// (ErrRevoked, ErrExpired).
	ended chan struct{}
	why   error
	// idle is closed once ended and no act is held.
	idle     chan struct{}
	idleDone bool
}

func newActs() *linkActs {
	return &linkActs{held: map[*Held]struct{}{}, ended: make(chan struct{}), idle: make(chan struct{})}
}

// isEnded says whether the barrier has begun; a.mu held.
func (a *linkActs) isEnded() bool {
	select {
	case <-a.ended:
		return true
	default:
		return false
	}
}

// settle closes idle once the barrier has begun and nothing is held;
// a.mu held.
func (a *linkActs) settle() {
	if !a.idleDone && len(a.held) == 0 && a.isEnded() {
		a.idleDone = true
		close(a.idle)
	}
}

// Held is one act's hold on its link (Store.Hold): taken as the act is
// checked, let go (Release) once the act has returned.
type Held struct {
	// Link is the link as it stood when the hold was taken.
	Link *Link

	store     *Store
	acts      *linkActs
	cancelled chan struct{}
	release   sync.Once
	cancel    sync.Once
}

// Hold is the link linkID as it stands, held while an act taken through it
// is under way (a decision link's answer as the session's writer takes it
// and writes it, a task command as the engine commits it): the link is
// checked with the act, not before it. Until Release a revoke or an expiry
// of the link waits for the act, up to the store's HoldWait (end). Hold
// itself never waits: once a revoke or an expiry of the link has begun it
// is refused as after it (ErrRevoked, ErrExpired); an unknown, revoked or
// expired link is refused as Live refuses it.
func (s *Store) Hold(linkID string) (*Held, error) {
	s.mu.RLock()
	l, ok := s.byID[linkID]
	s.mu.RUnlock()
	if !ok || l.acts == nil {
		return nil, ErrUnknownLink
	}
	a := l.acts
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.isEnded() {
		return nil, a.why
	}
	link, err := s.Live(linkID)
	if err != nil {
		return nil, err
	}
	h := &Held{Link: link, store: s, acts: a, cancelled: make(chan struct{})}
	a.held[h] = struct{}{}
	return h, nil
}

// Stands is asked at the act's commit boundary, just before it is begun:
// nil while the link stands, else why it does not (ErrRevoked, ErrExpired,
// ErrUnknownLink), its barrier begun counting as ended. The act is not
// begun on an error, fail-closed. It never waits.
func (h *Held) Stands() error {
	a := h.acts
	a.mu.Lock()
	ended, why := a.isEnded(), a.why
	a.mu.Unlock()
	if ended {
		return why
	}
	_, err := h.store.Live(h.Link.ID)
	return err
}

// Cancelled is closed once the link's barrier has waited HoldWait for the
// act and returns without it: an act that can tell (a process write the
// program has not taken) lets go of its hold as it sees it and answers as
// its link's end, resolving nothing for the link; what it had begun is not
// taken back (the write's bytes land as the program reads them).
func (h *Held) Cancelled() <-chan struct{} { return h.cancelled }

// Release lets go of the hold: once; again does nothing.
func (h *Held) Release() {
	h.release.Do(func() {
		a := h.acts
		a.mu.Lock()
		delete(a.held, h)
		a.settle()
		a.mu.Unlock()
	})
}

// end is the link's barrier, the one a revoke and an expiry both go
// through: why (ErrRevoked, ErrExpired) is noted, the first time, and from
// then on no act is held (Hold refuses it, Stands says why). It then waits
// for the acts held, begun before it, up to the store's HoldWait: at once
// when none is. Those still held then are told (Held.Cancelled) and left
// to finish on their own; end returns how many they are, and logs it.
func (s *Store) end(linkID string, a *linkActs, why error) (pending int) {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	if !a.isEnded() {
		a.why = why
		close(a.ended)
	}
	a.settle()
	a.mu.Unlock()
	wait := s.HoldWait
	if wait <= 0 {
		wait = DefaultHoldWait
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-a.idle:
		return 0
	case <-t.C:
	}
	a.mu.Lock()
	for h := range a.held {
		h.cancel.Do(func() { close(h.cancelled) })
	}
	pending = len(a.held)
	a.mu.Unlock()
	if pending > 0 && s.Log != nil {
		s.Log.Warn("a link ended with acts begun before it still under way", "link", linkID, "why", why.Error(), "pending", pending, "waited", wait.String())
	}
	return pending
}

// SetNowForTest sets the store's clock, for the tests of the packages that
// serve its links; set before the links it judges are made.
func (s *Store) SetNowForTest(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
