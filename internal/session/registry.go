package session

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned for unknown session IDs.
var ErrNotFound = errors.New("session: not found")

// ErrTooManySessions is returned when the registry is full.
var ErrTooManySessions = errors.New("session: too many sessions")

// Registry indexes live and recently ended sessions. It asks a session for
// its description (Info) holding none of its own locks, from a copy of its
// index: a session that is slow to answer holds up whoever asks it, never a
// lookup or the removal of another.
type Registry struct {
	mu    sync.RWMutex
	max   int
	items map[string]Driver
	// addMu makes Add's count and insert one step for the other Adds: only
	// an Add raises the count.
	addMu sync.Mutex
	// OnRemove is called after a session leaves the registry (e.g. to delete
	// links), with the session as it was.
	OnRemove func(id string, d Driver)
	// reserved counts the places held (Reserve) and not yet taken.
	reserved int
	// lingering holds the sessions removed while their process ran (a
	// stop that failed), until it ends: no longer listed, they are still
	// what a resume must not run beside (Holders). Under mu.
	lingering map[string]Driver
}

// NewRegistry creates a registry holding at most max sessions.
func NewRegistry(max int) *Registry {
	return &Registry{max: max, items: map[string]Driver{}}
}

// Add inserts d, enforcing the capacity limit against non-ended sessions
// and the places reserved (Reserve).
func (r *Registry) Add(d Driver) error { return r.AddReserved(d, nil) }

// AddReserved is Add for a session made with a place reserved for it:
// the reservation is consumed and no capacity check is made, since the
// reservation was it.
func (r *Registry) AddReserved(d Driver, res *Reservation) error {
	id := d.Info().ID
	r.addMu.Lock()
	defer r.addMu.Unlock()
	if res != nil && res.r == r && res.consume() {
		r.reserved--
	} else if r.active()+r.reserved >= r.max {
		return ErrTooManySessions
	}
	r.mu.Lock()
	r.items[id] = d
	r.mu.Unlock()
	return nil
}

// active counts the sessions not ended. The caller holds addMu.
func (r *Registry) active() int {
	n := 0
	for _, it := range r.snapshot() {
		if !it.Info().Status.Ended() {
			n++
		}
	}
	return n
}

// Reservation is a place held among a registry's sessions (Reserve), for
// a session made later: consumed by AddReserved, or let go by Release. A
// zero Reservation (a test's) holds nothing.
type Reservation struct {
	r    *Registry
	once sync.Once
	done bool
}

// Reserve holds a place for one session, counted against the capacity
// as a session is, or refuses with ErrTooManySessions: a managed run's
// attempt asks before it makes anything (crew.Launcher).
func (r *Registry) Reserve() (*Reservation, error) {
	r.addMu.Lock()
	defer r.addMu.Unlock()
	if r.active()+r.reserved >= r.max {
		return nil, ErrTooManySessions
	}
	r.reserved++
	return &Reservation{r: r}, nil
}

// consume marks the reservation used, once.
func (res *Reservation) consume() bool {
	used := false
	res.once.Do(func() { used = true; res.done = true })
	return used
}

// Release lets the place go, unless a session took it (AddReserved).
func (res *Reservation) Release() {
	if res == nil || res.r == nil {
		return
	}
	if res.consume() {
		res.r.addMu.Lock()
		res.r.reserved--
		res.r.addMu.Unlock()
	}
}

// snapshot copies the index's sessions.
func (r *Registry) snapshot() []Driver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Driver, 0, len(r.items))
	for _, d := range r.items {
		list = append(list, d)
	}
	return list
}

// Get returns the session with the given ID.
func (r *Registry) Get(id string) (Driver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.items[id]
	return d, ok
}

// List returns session descriptions, newest first.
func (r *Registry) List() []Info {
	list := r.snapshot()
	out := make([]Info, 0, len(list))
	for _, d := range list {
		out = append(out, d.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Remove deletes a session from the index; one whose process still runs
// is kept among Holders until it ends.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	d, ok := r.items[id]
	delete(r.items, id)
	if ok {
		r.lingerLocked(id, d)
	}
	r.mu.Unlock()
	if ok && r.OnRemove != nil {
		r.OnRemove(id, d)
	}
}

// removeIf deletes the session id from the index if it is still d.
func (r *Registry) removeIf(id string, d Driver) bool {
	r.mu.Lock()
	ok := r.items[id] == d
	if ok {
		delete(r.items, id)
		r.lingerLocked(id, d)
	}
	r.mu.Unlock()
	if ok && r.OnRemove != nil {
		r.OnRemove(id, d)
	}
	return ok
}

// lingerLocked keeps d, removed as id, among Holders while its process
// runs. The caller holds mu.
func (r *Registry) lingerLocked(id string, d Driver) {
	if !processRuns(d) {
		return
	}
	if r.lingering == nil {
		r.lingering = map[string]Driver{}
	}
	r.lingering[id] = d
}

// processRuns reports whether d is a session with a process of its own
// (a Local) that has not ended.
func processRuns(d Driver) bool {
	p, ok := d.(interface{ ProcessDone() <-chan struct{} })
	if !ok {
		return false
	}
	select {
	case <-p.ProcessDone():
		return false
	default:
		return true
	}
}

// Holders answers, as one read under the index's lock, every session
// whose own process has not ended: those listed (running, or ended by a
// stop that failed) and those removed while their process ran. A resume
// reads it as it launches: none of them may hold the conversation it
// takes up. A removed session leaves it once its process has ended.
func (r *Registry) Holders() []Driver {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Driver
	for id, d := range r.lingering {
		if !processRuns(d) {
			delete(r.lingering, id)
			continue
		}
		out = append(out, d)
	}
	for _, d := range r.items {
		if processRuns(d) {
			out = append(out, d)
		}
	}
	return out
}

// Count returns the number of indexed sessions.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// Each calls fn for every driver.
func (r *Registry) Each(fn func(Driver)) {
	for _, d := range r.snapshot() {
		fn(d)
	}
}

// GC removes ended sessions older than retention. It returns the removed IDs.
func (r *Registry) GC(retention time.Duration, now time.Time) []string {
	var expired []string
	for _, d := range r.snapshot() {
		info := d.Info()
		if info.Status.Ended() && info.EndedAt != nil && now.Sub(*info.EndedAt) >= retention && r.removeIf(info.ID, d) {
			expired = append(expired, info.ID)
		}
	}
	return expired
}
