package capacity

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeReader answers every read at the clock's now with one weekly
// window; when gated, each read waits for a release, and entered is told
// of each read as it starts.
type fakeReader struct {
	clock *fakeClock
	gated bool

	mu      sync.Mutex
	calls   int
	in, max int
	release chan struct{}
	entered chan Key
	kept    chan Key // told by the cache's OnRead
	reset   *time.Time
}

func newReader(c *fakeClock, gated bool) *fakeReader {
	return &fakeReader{clock: c, gated: gated, release: make(chan struct{}), entered: make(chan Key, 2048), kept: make(chan Key, 2048)}
}

func (r *fakeReader) read(ctx context.Context, k Key) Reading {
	r.mu.Lock()
	r.calls++
	r.in++
	r.max = max(r.max, r.in)
	reset := r.reset
	r.mu.Unlock()
	r.entered <- k
	if r.gated {
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	}
	r.mu.Lock()
	r.in--
	r.mu.Unlock()
	return Reading{State: StateOK, Plan: "Invented Plan", Program: "Claude Code", Version: "2.1.318", Qualified: "2.1.318", ReadAt: r.clock.Now(),
		Windows: []Window{{Kind: "weekly_all", Name: "Weekly, all models", Percent: 40, ResetsAt: reset, Severity: "normal"}}}
}

func (r *fakeReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeReader) awaitEntered(t *testing.T) Key {
	t.Helper()
	select {
	case k := <-r.entered:
		return k
	case <-time.After(time.Minute): // bounds a hang only
		t.Fatal("no read started")
		return Key{}
	}
}

func (r *fakeReader) awaitKept(t *testing.T) {
	t.Helper()
	select {
	case <-r.kept:
	case <-time.After(time.Minute): // bounds a hang only
		t.Fatal("no reading kept")
	}
}

func newTestCache(t *testing.T, gated bool) (*Cache, *fakeClock, *fakeReader) {
	clock := newClock()
	r := newReader(clock, gated)
	c := NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key) { r.kept <- k }})
	t.Cleanup(c.Close)
	return c, clock, r
}

func key(i int) Key { return Key{Agent: "claude", Account: fmt.Sprintf("a-%012d", i)} }

func TestCacheOneReadPerKey(t *testing.T) {
	c, clock, r := newTestCache(t, true)
	k := key(1)
	views := make([]View, 2)
	var wg sync.WaitGroup
	wg.Go(func() { views[0] = c.Read(context.Background(), k) })
	r.awaitEntered(t)
	wg.Go(func() { views[1] = c.Read(context.Background(), k) })
	// Both askers wait (each took its timer) before the read may answer.
	clock.awaitWaiters(t, 2)
	if v := c.View(k); v.State != StateReading {
		t.Fatalf("while the read runs: %s", v.State)
	}
	close(r.release)
	wg.Wait()
	if n := r.count(); n != 1 {
		t.Fatalf("%d reads for two askers", n)
	}
	for i, v := range views {
		if v.State != StateOK || len(v.Windows) != 1 || v.ReadBy == nil || v.ReadBy.Version != "2.1.318" {
			t.Errorf("asker %d: %+v", i, v)
		}
	}
}

func TestCacheTwoAtOnce(t *testing.T) {
	c, clock, r := newTestCache(t, true)
	views := make([]View, 3)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() { views[i] = c.Read(context.Background(), key(i)) })
	}
	r.awaitEntered(t)
	r.awaitEntered(t)
	clock.awaitWaiters(t, 3)
	// Two reads hold both places: the third waits for one.
	if n := r.count(); n != 2 {
		t.Fatalf("%d reads at once", n)
	}
	// No asker waits past Wait: each gets the state reading.
	clock.Advance(Wait)
	wg.Wait()
	for i, v := range views {
		if v.State != StateReading || v.Fresh != FreshNone {
			t.Errorf("asker %d after the wait: %s, %s", i, v.State, v.Fresh)
		}
	}
	r.release <- struct{}{}
	r.awaitEntered(t)
	close(r.release)
	for range 3 {
		r.awaitKept(t)
	}
	for i := range 3 {
		if v := c.View(key(i)); v.State != StateOK {
			t.Errorf("key %d once kept: %s", i, v.State)
		}
	}
	r.mu.Lock()
	most := r.max
	r.mu.Unlock()
	if most > MaxReads {
		t.Errorf("%d reads at once, at most %d", most, MaxReads)
	}
}

func TestCacheMinInterval(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	k := key(1)
	v := c.Read(context.Background(), k)
	if v.State != StateOK || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Fatalf("first read: %+v", v)
	}
	clock.Advance(MinInterval - time.Second)
	v = c.Read(context.Background(), k)
	if n := r.count(); n != 1 {
		t.Fatalf("%d reads inside MinInterval", n)
	}
	if v.State != StateOK || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Errorf("the kept reading with nextReadAfter: %+v", v)
	}
	clock.Advance(time.Second)
	if v = c.Read(context.Background(), k); r.count() != 2 || !v.ReadAt.Equal(t0.Add(MinInterval)) {
		t.Errorf("after MinInterval: %d reads, read at %v", r.count(), v.ReadAt)
	}
}

func TestCacheFreshness(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	k := key(1)
	if v := c.View(k); v.Fresh != FreshNone || v.State != StateNeverRead {
		t.Fatalf("never read: %s %s", v.State, v.Fresh)
	}
	c.Read(context.Background(), k)
	clock.Advance(FreshFor - time.Second)
	if v := c.View(k); v.Fresh != FreshNow {
		t.Errorf("before FreshFor: %s", v.Fresh)
	}
	clock.Advance(time.Second)
	if v := c.View(k); v.Fresh != FreshStale || v.State != StateOK {
		t.Errorf("at FreshFor: %s %s", v.State, v.Fresh)
	}
	// A reset behind the clock is reset_passed, whatever the age.
	k2 := key(2)
	reset := clock.Now().Add(time.Minute)
	r.mu.Lock()
	r.reset = &reset
	r.mu.Unlock()
	c.Read(context.Background(), k2)
	if v := c.View(k2); v.Fresh != FreshNow {
		t.Errorf("before the reset: %s", v.Fresh)
	}
	clock.Advance(time.Minute)
	if v := c.View(k2); v.Fresh != FreshResetPassed {
		t.Errorf("at the reset: %s", v.Fresh)
	}
}

func TestCacheKeepsEight(t *testing.T) {
	c, clock, _ := newTestCache(t, false)
	k := key(1)
	for range KeepPerKey + 2 {
		c.Read(context.Background(), k)
		clock.Advance(MinInterval)
	}
	c.mu.Lock()
	got := c.keys[k].readings
	c.mu.Unlock()
	if len(got) != KeepPerKey || !got[0].ReadAt.Equal(t0.Add(2*MinInterval)) || !got[KeepPerKey-1].ReadAt.Equal(t0.Add((KeepPerKey+1)*MinInterval)) {
		t.Fatalf("kept %d, from %v to %v", len(got), got[0].ReadAt, got[len(got)-1].ReadAt)
	}
	if v := c.View(k); !v.ReadAt.Equal(got[KeepPerKey-1].ReadAt) {
		t.Errorf("the view is not the newest reading: %v", v.ReadAt)
	}
}

func TestCacheDropsLeastAsked(t *testing.T) {
	c, _, _ := newTestCache(t, false)
	for i := range MaxKeys {
		c.Read(context.Background(), key(i))
	}
	c.View(key(0)) // asked again: now key(1) is the least recently asked
	c.Read(context.Background(), key(MaxKeys))
	c.mu.Lock()
	n, has0, has1 := len(c.keys), c.keys[key(0)] != nil, c.keys[key(1)] != nil
	c.mu.Unlock()
	if n != MaxKeys || !has0 || has1 {
		t.Fatalf("%d keys, key 0 kept %v, key 1 kept %v", n, has0, has1)
	}
	if v := c.View(key(1)); v.State != StateNeverRead {
		t.Errorf("a dropped key: %s", v.State)
	}
}

func TestCooldownOutlivesEviction(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	for i := range MaxKeys + 1 {
		c.Read(context.Background(), key(i))
	}
	if v := c.View(key(0)); v.State != StateNeverRead {
		t.Fatalf("key 0 still kept: %s", v.State)
	}
	v := c.Read(context.Background(), key(0))
	if r.count() != MaxKeys+1 || v.State != StateNeverRead || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Fatalf("read again at once: %d reads, %+v", r.count(), v)
	}
	clock.Advance(MinInterval)
	if v = c.Read(context.Background(), key(0)); r.count() != MaxKeys+2 || v.State != StateOK {
		t.Fatalf("after MinInterval: %d reads, %s", r.count(), v.State)
	}
}

func TestCooldownTableFull(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	for i := range MaxCooldown {
		clock.Advance(time.Millisecond) // the first entry ends first
		c.Read(context.Background(), key(i))
	}
	v := c.Read(context.Background(), key(MaxCooldown))
	if r.count() != MaxCooldown {
		t.Fatalf("a read made with the table full: %d", r.count())
	}
	if want := t0.Add(time.Millisecond + MinInterval); v.NextRead == nil || !v.NextRead.Equal(want) || v.State != StateNeverRead {
		t.Fatalf("full: %+v, want nextReadAfter %v", v, want)
	}
	// Once the first entry has ended, there is room again.
	clock.Advance(MinInterval - time.Duration(MaxCooldown-1)*time.Millisecond)
	if v = c.Read(context.Background(), key(MaxCooldown)); r.count() != MaxCooldown+1 || v.State != StateOK {
		t.Errorf("with room: %d reads, %s", r.count(), v.State)
	}
}

func TestViewReadsNothing(t *testing.T) {
	c, _, r := newTestCache(t, false)
	for i := range 3 {
		if v := c.View(key(i)); v.State != StateNeverRead || v.Words == "" || v.Windows == nil || v.Credits == nil {
			t.Errorf("never read: %+v", v)
		}
	}
	if n := r.count(); n != 0 {
		t.Fatalf("View read %d times", n)
	}
}

func TestOnReadAfterKept(t *testing.T) {
	clock := newClock()
	r := newReader(clock, false)
	seen := make(chan View, 1)
	var c *Cache
	c = NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key) { seen <- c.View(k) }})
	t.Cleanup(c.Close)
	c.Read(context.Background(), key(1))
	select {
	case v := <-seen:
		if v.State != StateOK || v.ReadAt == nil || !v.ReadAt.Equal(t0) {
			t.Fatalf("OnRead saw %+v", v)
		}
	case <-time.After(time.Minute): // bounds a hang only
		t.Fatal("OnRead never called")
	}
}

func TestCacheReaderStateFailsClosed(t *testing.T) {
	clock := newClock()
	c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key) Reading {
		return Reading{State: "fine", Windows: []Window{{Kind: "weekly_all", Percent: 250}}}
	}})
	t.Cleanup(c.Close)
	v := c.Read(context.Background(), key(1))
	if v.State != StateError || v.Words == "" || v.ReadAt == nil || !v.ReadAt.Equal(t0) || v.Windows[0].Percent != 100 {
		t.Fatalf("an unknown state: %+v", v)
	}
}

func TestCacheCloseEndsReads(t *testing.T) {
	clock := newClock()
	r := newReader(clock, true)
	c := NewCache(CacheOptions{Clock: clock, Read: r.read})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan View)
	go func() { done <- c.Read(ctx, key(1)) }()
	r.awaitEntered(t)
	cancel() // the asker leaves; the read goes on
	if v := <-done; v.State != StateReading {
		t.Fatalf("the asker gone: %s", v.State)
	}
	c.Close() // ends the read held at its gate through its context
	if v := c.View(key(1)); v.State != StateOK {
		t.Fatalf("after Close: %s", v.State)
	}
}
