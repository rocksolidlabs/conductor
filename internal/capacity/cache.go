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
	// most MaxReads at once, never once Close has begun, with a context
	// Close cancels, and with the Intent of the askers of this read.
	Read func(ctx context.Context, k Key, in Intent) Reading
	// OnRead, when set, is called after a reading of k is kept, so what
	// it tells sees the reading in the cache. It is not called once Close
	// has begun, and Close waits for a call under way.
	OnRead func(k Key)
}

// ReadOptions are what one asker wants of a read.
type ReadOptions struct {
	// Check is the admin's explicit check of a version newer than the
	// reader's qualified one (Qualification), carried to the reader by
	// Intent.Check.
	Check bool
}

// Intent is what the askers of one read want of it, as its reader asks.
type Intent struct {
	c *Cache
	f *flight
}

// Check reports whether an explicit check is asked of this read: true
// when any asker that joined it until this call asked one, so a check is
// never lost by joining a read without one. The reader asks it at the
// moment it asks Versions.Admit, the one decision a check changes; the
// answer is fixed then, and a check asked of the read after that is
// answered as one asked just after it ended (the kept reading, with
// NextRead).
func (in Intent) Check() bool {
	if in.c == nil {
		return false
	}
	in.c.mu.Lock()
	defer in.c.mu.Unlock()
	in.f.decided = true
	return in.f.check
}

// ClosedWords are the words of a Read asked once Close has begun.
const ClosedWords = "the server is shutting down: no read is started"

// Cache keeps the readings per key in memory only, so a restart starts at
// never_read. It reads only when Read is called; View answers from what
// is kept; nothing polls. What crosses its boundary is a copy: no slice
// or pointer of a kept reading is the reader's or a caller's.
type Cache struct {
	clock  Clock
	read   func(ctx context.Context, k Key, in Intent) Reading
	onRead func(k Key)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}

	// The tests' seams: beforeAdmit runs as Read is entered, before it
	// takes mu; afterClosed runs in Close once the cache is marked closed,
	// before the reads under way are cancelled.
	beforeAdmit, afterClosed func()

	mu      sync.Mutex
	closed  bool // Close has begun: no read starts
	tick    uint64
	keys    map[Key]*kept
	flights map[Key]*flight
	queued  int // flights waiting for a slot, each holding a place in cool
	// cool is each key's last read's start (the moment its reader was
	// called), kept apart from the readings until MinInterval has passed,
	// so a key dropped from keys is still not read again early.
	cool map[Key]time.Time
}

type kept struct {
	readings []Reading // oldest first, at most KeepPerKey
	asked    uint64
}

// flight is a read asked and not yet kept; done is closed once it is kept
// (or given up). check is the strongest intent of its askers, decided
// once its reader asks (Intent.Check).
type flight struct {
	done    chan struct{}
	check   bool
	decided bool
}

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

// Close is the cache's end. Once it has begun no read starts: a Read
// during or after it is answered ClosedWords, and a read waiting for a
// slot gives up. It cancels the reads under way and returns only once
// nothing it started can still run, keep a reading or call OnRead.
func (c *Cache) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.afterClosed != nil {
		c.afterClosed()
	}
	c.cancel()
	c.wg.Wait()
}

// closedView is the View a Read asked once Close has begun answers.
func closedView(k Key) View {
	v := emptyView(k)
	v.State, v.Words = StateError, ClosedWords
	return v
}

// Read asks a read of k and answers its View: joined to the read asked
// of k when there is one (o.Check carried to it while its reader has not
// decided), else a read started unless k was read less than MinInterval
// ago or the cooldown table is full of unexpired keys and queued reads
// (then nothing is read and the View says when a read can be, NextRead).
// It waits at most Wait (or until ctx ends) and otherwise answers the
// last reading with the state reading. Once Close has begun it starts
// nothing and answers ClosedWords.
func (c *Cache) Read(ctx context.Context, k Key, o ReadOptions) View {
	if c.beforeAdmit != nil {
		c.beforeAdmit()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return closedView(k)
	}
	now := c.clock.Now()
	c.touch(k)
	f := c.flights[k]
	if f == nil {
		for key, t := range c.cool {
			if !now.Before(t.Add(MinInterval)) {
				delete(c.cool, key)
			}
		}
		if _, ok := c.cool[k]; ok || len(c.cool)+c.queued >= MaxCooldown {
			v := c.viewLocked(k, now)
			if v.NextRead == nil {
				next := c.soonestLocked(now)
				v.NextRead = &next
			}
			c.mu.Unlock()
			return v
		}
		f = &flight{done: make(chan struct{})}
		c.flights[k] = f
		c.queued++
		c.wg.Add(1)
		go c.run(k, f)
	}
	if o.Check && !f.decided {
		f.check = true
	}
	c.mu.Unlock()
	select {
	case <-f.done:
	case <-c.clock.After(Wait):
	case <-ctx.Done():
	}
	return c.View(k)
}

// soonestLocked is the end of the cooldown entry that ends first, or
// MinInterval from now when only queued reads fill the table.
func (c *Cache) soonestLocked(now time.Time) time.Time {
	t := now
	first := true
	for _, s := range c.cool {
		if first || s.Before(t) {
			t, first = s, false
		}
	}
	return t.Add(MinInterval)
}

// run makes one read in a slot, keeps it, then tells OnRead. The key's
// cooldown starts as its reader is called, not as it was asked, so a read
// that waited for a slot is still the only one in its MinInterval.
func (c *Cache) run(k Key, f *flight) {
	defer c.wg.Done()
	giveUp := func() {
		delete(c.flights, k)
		c.queued--
		c.mu.Unlock()
		close(f.done)
	}
	select {
	case c.slots <- struct{}{}:
	case <-c.ctx.Done():
		c.mu.Lock()
		giveUp()
		return
	}
	c.mu.Lock()
	if c.closed { // the slot and Close's cancel were both ready
		<-c.slots
		giveUp()
		return
	}
	c.queued--
	c.cool[k] = c.clock.Now()
	c.mu.Unlock()
	r := c.read(c.ctx, k, Intent{c: c, f: f})
	<-c.slots
	r = r.kept(k, c.clock.Now())
	c.mu.Lock()
	c.keep(k, r)
	delete(c.flights, k)
	tell := !c.closed && c.onRead != nil
	c.mu.Unlock()
	close(f.done)
	if tell {
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
