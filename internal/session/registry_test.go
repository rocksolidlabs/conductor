package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubDriver struct{ info Info }

func (d *stubDriver) Info() Info                  { return d.info }
func (d *stubDriver) Stop(context.Context) error  { return nil }
func (d *stubDriver) DisconnectLink(string)       {}
func (d *stubDriver) EndLink(string)              {}
func (d *stubDriver) LinkViewers() map[string]int { return nil }
func stub(id string, created time.Time) *stubDriver {
	return &stubDriver{Info{ID: id, CreatedAt: created, Status: StatusRunning}}
}

func TestRegistryCapacityListAndGC(t *testing.T) {
	r := NewRegistry(2)
	now := time.Now()
	a, b := stub("a", now.Add(-2*time.Minute)), stub("b", now.Add(-time.Minute))
	if err := r.Add(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(stub("c", now)); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected capacity error, got %v", err)
	}
	if list := r.List(); list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("order %v", list)
	}
	ended := now.Add(-20 * time.Minute)
	a.info.Status = StatusExited
	a.info.EndedAt = &ended
	var removed []string
	r.OnRemove = func(id string, _ Driver) { removed = append(removed, id) }
	if got := r.GC(10*time.Minute, now); len(got) != 1 || got[0] != "a" || len(removed) != 1 {
		t.Fatalf("gc %v removed %v", got, removed)
	}
	if err := r.Add(stub("c", now)); err != nil {
		t.Fatalf("capacity after gc: %v", err)
	}
	if _, ok := r.Get("a"); ok {
		t.Fatal("a should be gone")
	}
}

// infoGate is a session whose Info waits, when armed, until it is released:
// a session that cannot answer.
type infoGate struct {
	stubDriver
	armed   atomic.Bool
	release chan struct{}
	asked   chan struct{}
}

func (d *infoGate) Info() Info {
	if d.armed.Load() {
		select {
		case d.asked <- struct{}{}:
		default:
		}
		<-d.release
	}
	return d.info
}

// The registry asks its sessions for their description outside its own lock:
// one that cannot answer holds up whoever asks it (a listing, a launch
// counting the sessions, the sweep), never a lookup of another session or a
// removal.
func TestTheRegistryAsksSessionsOutsideItsLock(t *testing.T) {
	r := NewRegistry(10)
	now := time.Now()
	stuck := &infoGate{stubDriver: *stub("stuck", now), release: make(chan struct{}), asked: make(chan struct{}, 1)}
	for _, d := range []Driver{stuck, stub("other", now), stub("gone", now)} {
		if err := r.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	stuck.armed.Store(true)
	release := sync.OnceFunc(func() { close(stuck.release) })
	defer release()
	pending := map[string]chan struct{}{}
	for name, fn := range map[string]func(){
		"List": func() { _ = r.List() },
		"Add":  func() { _ = r.Add(stub("new", now)) },
		"GC":   func() { _ = r.GC(time.Minute, now) },
	} {
		done := make(chan struct{})
		pending[name] = done
		go func() { fn(); close(done) }()
		select {
		case <-stuck.asked:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never asked the session", name)
		}
	}
	time.Sleep(20 * time.Millisecond) // whatever waits for the lock is waiting by now
	if !within(time.Second, func() { _, _ = r.Get("other") }) {
		t.Error("a lookup of another session waited on the one that cannot answer")
	}
	if !within(time.Second, func() { r.Remove("gone") }) {
		t.Error("a removal waited on the session that cannot answer")
	}
	if !within(time.Second, func() { _ = r.Count() }) {
		t.Error("Count waited on the session that cannot answer")
	}
	stuck.armed.Store(false)
	release()
	for name, done := range pending {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never returned", name)
		}
	}
}

func TestAReservationHoldsAPlaceUntilASessionTakesItOrItIsLetGo(t *testing.T) {
	r := NewRegistry(2)
	live := func(id string) *stubDriver { return &stubDriver{info: Info{ID: id, Status: StatusRunning}} }
	first, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	// Two places held: a third reservation and a plain Add are refused.
	if _, err := r.Reserve(); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a third reservation: %v", err)
	}
	if err := r.Add(live("a")); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("an Add over held places: %v", err)
	}
	// Let go, the place is free; taken, it is the session's, and a later
	// Release of the same reservation frees nothing.
	first.Release()
	if err := r.Add(live("a")); err != nil {
		t.Fatal(err)
	}
	if err := r.AddReserved(live("b"), second); err != nil {
		t.Fatal(err)
	}
	second.Release()
	if _, err := r.Reserve(); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("the registry is full with two sessions: %v", err)
	}
	// A reservation of another registry, or none, is an ordinary Add.
	other, _ := NewRegistry(1).Reserve()
	if err := r.AddReserved(live("c"), other); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("another registry's reservation counted here: %v", err)
	}
	var none *Reservation
	none.Release()
	(&Reservation{}).Release()
	if err := r.AddReserved(live("c"), &Reservation{}); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("a zero reservation counted: %v", err)
	}
}

// procDriver is a session with a process of its own, which ends when done
// is closed.
type procDriver struct {
	stubDriver
	done chan struct{}
}

func (d *procDriver) ProcessDone() <-chan struct{} { return d.done }

// Holders are the sessions whose process has not ended: listed (running,
// or ended by a stop that failed) or removed while their process ran,
// which leave it once their process ends; never one without a process of
// its own, nor one whose process ended.
func TestHoldersKeepARemovedSessionUntilItsProcessEnds(t *testing.T) {
	r := NewRegistry(10)
	now := time.Now()
	running := &procDriver{*stub("running", now), make(chan struct{})}
	failed := &procDriver{*stub("failed", now), make(chan struct{})}
	failed.info.Status = StatusStopped
	gone := &procDriver{*stub("gone", now), make(chan struct{})}
	close(gone.done)
	for _, d := range []Driver{running, failed, gone, stub("hosted", now)} {
		if err := r.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	ids := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range r.Holders() {
			out[d.Info().ID] = true
		}
		return out
	}
	if got := ids(); len(got) != 2 || !got["running"] || !got["failed"] {
		t.Fatalf("listed holders: %v", got)
	}
	r.Remove("failed")
	r.Remove("gone")
	if _, ok := r.Get("failed"); ok {
		t.Fatal("removed, still listed")
	}
	if got := ids(); len(got) != 2 || !got["failed"] {
		t.Fatalf("after the removal: %v", got)
	}
	close(failed.done)
	if got := ids(); len(got) != 1 || !got["running"] {
		t.Fatalf("once its process ended: %v", got)
	}
}
