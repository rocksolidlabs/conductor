package capacity

import (
	"context"
	"slices"
	"sync"
	"time"
)

// The cache's bounds and times (docs/plans/capacity.md, Owner decisions 5).
const (
	KeepPerKey  = 8                // readings kept per key
	MaxKeys     = 128              // keys kept, the least recently asked dropped
	MaxReads    = 2                // reads at once across the server
	Wait        = 2 * time.Second  // what an asker waits for a read
	MinInterval = 5 * time.Minute  // a key is read at most once in it, whoever asks
	FreshFor    = 15 * time.Minute // a reading is fresh for it, then stale
	MaxCooldown = 1024             // keys whose last read's time is kept
)

// Clock is the cache's time: the real one by default, a fake in tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// CacheOptions are what NewCache takes.
type CacheOptions struct {
	Clock Clock // nil is the real clock
	// Read reads one key's usage. It is called only by Cache.Read, at
	// most MaxReads at once, with a context Close cancels.
	Read func(ctx context.Context, k Key) Reading
	// OnRead, when set, is called after a reading of k is kept, so what
	// it tells sees the reading in the cache.
	OnRead func(k Key)
}

// Cache keeps the readings per key in memory only, so a restart starts at
// never_read. It reads only when Read is called; View answers from what
// is kept; nothing polls.
type Cache struct {
	clock  Clock
	read   func(ctx context.Context, k Key) Reading
	onRead func(k Key)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}

	mu      sync.Mutex
	tick    uint64
	keys    map[Key]*kept
	flights map[Key]*flight
	// cool is each key's last read's start, kept apart from the readings
	// until MinInterval has passed, so a key dropped from keys is still
	// not read again early.
	cool map[Key]time.Time
}

type kept struct {
	readings []Reading // oldest first, at most KeepPerKey
	asked    uint64
}

// flight is a read under way; done is closed once it is kept (or given up).
type flight struct{ done chan struct{} }

// NewCache makes a cache over its reader. Close ends the reads under way.
func NewCache(o CacheOptions) *Cache {
	c := &Cache{clock: o.Clock, read: o.Read, onRead: o.OnRead, slots: make(chan struct{}, MaxReads),
		keys: map[Key]*kept{}, flights: map[Key]*flight{}, cool: map[Key]time.Time{}}
	if c.clock == nil {
		c.clock = realClock{}
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// Close cancels the reads under way and waits for them to end.
func (c *Cache) Close() {
	c.cancel()
	c.wg.Wait()
}

// Read asks a read of k and answers its View: joined to the read under
// way for k when there is one, else a read started unless k was read less
// than MinInterval ago or the cooldown table is full of unexpired keys
// (then nothing is read and the View says when a read can be, NextRead).
// It waits at most Wait (or until ctx ends) and otherwise answers the
// last reading with the state reading.
func (c *Cache) Read(ctx context.Context, k Key) View {
	c.mu.Lock()
	now := c.clock.Now()
	c.touch(k)
	f := c.flights[k]
	if f == nil {
		for key, t := range c.cool {
			if !now.Before(t.Add(MinInterval)) {
				delete(c.cool, key)
			}
		}
		if _, ok := c.cool[k]; ok || len(c.cool) >= MaxCooldown {
			v := c.viewLocked(k, now)
			if v.NextRead == nil {
				next := c.soonestLocked()
				v.NextRead = &next
			}
			c.mu.Unlock()
			return v
		}
		c.cool[k] = now
		f = &flight{done: make(chan struct{})}
		c.flights[k] = f
		c.wg.Add(1)
		go c.run(k, f)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
	case <-c.clock.After(Wait):
	case <-ctx.Done():
	}
	return c.View(k)
}

// soonestLocked is the end of the cooldown entry that ends first.
func (c *Cache) soonestLocked() time.Time {
	var t time.Time
	for _, s := range c.cool {
		if t.IsZero() || s.Before(t) {
			t = s
		}
	}
	return t.Add(MinInterval)
}

// run makes one read in a slot, keeps it, then tells OnRead.
func (c *Cache) run(k Key, f *flight) {
	defer c.wg.Done()
	select {
	case c.slots <- struct{}{}:
	case <-c.ctx.Done():
		c.mu.Lock()
		delete(c.flights, k)
		c.mu.Unlock()
		close(f.done)
		return
	}
	r := c.read(c.ctx, k)
	<-c.slots
	r.Key = k
	switch r.State {
	case StateOK, StateSignedOut, StateKeyPaid, StateUnqualified, StateError:
	default:
		r.State, r.Words = StateError, "the reader answered no state a reading has"
	}
	if r.ReadAt.IsZero() {
		r.ReadAt = c.clock.Now()
	}
	r = r.bounded()
	c.mu.Lock()
	c.keep(k, r)
	delete(c.flights, k)
	c.mu.Unlock()
	close(f.done)
	if c.onRead != nil {
		c.onRead(k)
	}
}

// touch marks k as asked now; the caller holds c.mu.
func (c *Cache) touch(k Key) {
	if e := c.keys[k]; e != nil {
		c.tick++
		e.asked = c.tick
	}
}

// keep adds r to k's readings, dropping its oldest past KeepPerKey and,
// for a new key past MaxKeys, the least recently asked key; the caller
// holds c.mu.
func (c *Cache) keep(k Key, r Reading) {
	e := c.keys[k]
	if e == nil {
		if len(c.keys) >= MaxKeys {
			var least Key
			var at uint64
			first := true
			for key, o := range c.keys {
				if first || o.asked < at {
					least, at, first = key, o.asked, false
				}
			}
			delete(c.keys, least)
		}
		e = &kept{}
		c.keys[k] = e
	}
	c.tick++
	e.asked = c.tick
	e.readings = append(e.readings, r)
	if n := len(e.readings) - KeepPerKey; n > 0 {
		e.readings = slices.Delete(e.readings, 0, n)
	}
}

// View answers k's View from what is kept, reading nothing: the newest
// reading and its freshness, never_read when none is kept, the state
// reading while a read is under way, and NextRead while k was read less
// than MinInterval ago.
func (c *Cache) View(k Key) View {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touch(k)
	return c.viewLocked(k, c.clock.Now())
}

func (c *Cache) viewLocked(k Key, now time.Time) View {
	var v View
	if e := c.keys[k]; e != nil {
		v = e.readings[len(e.readings)-1].view(now)
		v.Windows, v.Credits = slices.Clone(v.Windows), slices.Clone(v.Credits)
	} else {
		v = emptyView(k)
	}
	if c.flights[k] != nil {
		v.State, v.Words = StateReading, StateWords(StateReading)
	}
	if t, ok := c.cool[k]; ok && now.Before(t.Add(MinInterval)) {
		next := t.Add(MinInterval)
		v.NextRead = &next
	}
	return v
}
