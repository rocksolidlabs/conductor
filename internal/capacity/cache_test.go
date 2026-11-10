package capacity

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

func (r *fakeReader) read(ctx context.Context, k Key, in Intent) Reading {
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
	c := NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key, _ View) { r.kept <- k }})
	t.Cleanup(c.Close)
	return c, clock, r
}

func key(i int) Key { return Key{Agent: "claude", Account: fmt.Sprintf("a-%012d", i)} }

func TestCacheOneReadPerKey(t *testing.T) {
	c, clock, r := newTestCache(t, true)
	k := key(1)
	views := make([]View, 2)
	var wg sync.WaitGroup
	wg.Go(func() { views[0] = c.Read(context.Background(), k, ReadOptions{}) })
	r.awaitEntered(t)
	wg.Go(func() { views[1] = c.Read(context.Background(), k, ReadOptions{}) })
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
		wg.Go(func() { views[i] = c.Read(context.Background(), key(i), ReadOptions{}) })
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
	v := c.Read(context.Background(), k, ReadOptions{})
	if v.State != StateOK || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Fatalf("first read: %+v", v)
	}
	clock.Advance(MinInterval - time.Second)
	v = c.Read(context.Background(), k, ReadOptions{})
	if n := r.count(); n != 1 {
		t.Fatalf("%d reads inside MinInterval", n)
	}
	if v.State != StateOK || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Errorf("the kept reading with nextReadAfter: %+v", v)
	}
	clock.Advance(time.Second)
	if v = c.Read(context.Background(), k, ReadOptions{}); r.count() != 2 || !v.ReadAt.Equal(t0.Add(MinInterval)) {
		t.Errorf("after MinInterval: %d reads, read at %v", r.count(), v.ReadAt)
	}
}

func TestCacheFreshness(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	k := key(1)
	if v := c.View(k); v.Fresh != FreshNone || v.State != StateNeverRead {
		t.Fatalf("never read: %s %s", v.State, v.Fresh)
	}
	c.Read(context.Background(), k, ReadOptions{})
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
	c.Read(context.Background(), k2, ReadOptions{})
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
		c.Read(context.Background(), k, ReadOptions{})
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

// orderReader answers every read at once, but a read of the key it is told
// to hold waits at its gate (entered tells it has started), and a read of
// the key it is told to refuse answers a refusal.
type orderReader struct {
	entered chan Key
	release chan struct{}

	mu           sync.Mutex
	hold, refuse Key
}

func (r *orderReader) read(ctx context.Context, k Key, _ Intent) Reading {
	r.mu.Lock()
	hold, refuse := k == r.hold, k == r.refuse
	r.mu.Unlock()
	if hold {
		r.entered <- k
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	}
	if refuse {
		return Reading{State: StateError, Words: "an invented refusal"}
	}
	return Reading{State: StateOK}
}

// orderWalk is a full cache whose keys were asked key(0) first, key(127)
// last, and the steps that touch its order.
type orderWalk struct {
	t     *testing.T
	c     *Cache
	clock *fakeClock
	r     *orderReader
}

func newOrderWalk(t *testing.T) *orderWalk {
	clock := newClock()
	r := &orderReader{entered: make(chan Key, 1), release: make(chan struct{})}
	w := &orderWalk{t: t, c: NewCache(CacheOptions{Clock: clock, Read: r.read}), clock: clock, r: r}
	t.Cleanup(w.c.Close)
	for i := range MaxKeys {
		w.read(key(i))
	}
	return w
}

func (w *orderWalk) read(k Key) View { return w.c.Read(context.Background(), k, ReadOptions{}) }

// order is the kept keys, the least recently asked first.
func (w *orderWalk) order() []Key {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	keys := make([]Key, 0, len(w.c.keys))
	for k := range w.c.keys {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b Key) int { return cmp.Compare(w.c.keys[a].asked, w.c.keys[b].asked) })
	return keys
}

// askOthers views every kept key but skip, in their order, so the order
// holds among them and skip is behind them all.
func (w *orderWalk) askOthers(skip Key) {
	for _, k := range w.order() {
		if k != skip {
			w.c.View(k)
		}
	}
}

// start asks a read of k that its reader holds at the gate, and returns
// once the read has started and its asker waits: the asker's View comes
// on the channel, and cancel makes the asker leave.
func (w *orderWalk) start(k Key, refuse bool) (cancel func(), got <-chan View) {
	w.t.Helper()
	w.r.mu.Lock()
	w.r.hold = k
	if refuse {
		w.r.refuse = k
	}
	w.r.mu.Unlock()
	w.clock.mu.Lock()
	handed := w.clock.handed
	w.clock.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan View, 1)
	go func() { ch <- w.c.Read(ctx, k, ReadOptions{}) }()
	select {
	case <-w.r.entered:
	case <-time.After(time.Minute): // bounds a hang only
		w.t.Fatalf("the read of %v never started", k)
	}
	w.clock.awaitWaiters(w.t, handed+1)
	return cancel, ch
}

// join asks k again while its read is under way, and returns once that
// asker waits; its View comes on the channel.
func (w *orderWalk) join(k Key) <-chan View {
	w.clock.mu.Lock()
	handed := w.clock.handed
	w.clock.mu.Unlock()
	ch := make(chan View, 1)
	go func() { ch <- w.read(k) }()
	w.clock.awaitWaiters(w.t, handed+1)
	return ch
}

// settled waits until no read of k is under way: its reading kept or
// dropped.
func (w *orderWalk) settled(k Key) {
	w.t.Helper()
	for end := time.Now().Add(time.Minute); w.c.flightOf(k) != nil; time.Sleep(time.Millisecond) { // the minute bounds a hang only
		if time.Now().After(end) {
			w.t.Fatalf("the read of %v never ended", k)
		}
	}
}

// TestCacheDropsLeastAsked walks every path that touches the cache's
// order: only an ask (a Read or a View arriving, for a key kept or one
// whose read is under way) moves a key's place. Keeping a reading or a
// refusal, an asker leaving or its wait ending, a read ending after its
// asker left, a reading growing stale and a key dropped move nothing. The
// reads that must end at a given step are held at a gate; nothing waits
// on the clock. Each row leaves its subject either the most recently
// asked key (moved) or the least (not moved), and one more key read then
// drops the least.
func TestCacheDropsLeastAsked(t *testing.T) {
	first := key(MaxKeys + 1) // a key never kept before its row
	cases := []struct {
		name    string
		subject Key
		moved   bool
		step    func(w *orderWalk)
	}{
		{"a view of a kept key", key(0), true, func(w *orderWalk) { w.c.View(key(0)) }},
		{"a read started", key(0), true, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			w.read(key(0))
		}},
		{"a second request for a key whose read is under way", key(0), true, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			cancel, got := w.start(key(0), false)
			cancel()
			<-got
			w.askOthers(key(0))
			again := w.join(key(0))
			close(w.r.release)
			<-again
		}},
		{"a read refused by its key's cooldown", key(0), true, func(w *orderWalk) {
			if v := w.read(key(0)); v.NextRead == nil {
				w.t.Errorf("a read inside MinInterval made: %+v", v)
			}
		}},
		{"a read ending with its asker waiting", key(0), false, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			_, got := w.start(key(0), false)
			w.askOthers(key(0))
			close(w.r.release)
			if v := <-got; v.State != StateOK {
				w.t.Errorf("the asker's answer: %s", v.State)
			}
		}},
		{"an asker leaving after every other key was asked", key(0), false, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			cancel, got := w.start(key(0), false)
			w.askOthers(key(0))
			cancel()
			<-got
			close(w.r.release)
			w.settled(key(0))
		}},
		{"an asker's wait ending after every other key was asked", key(0), false, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			_, got := w.start(key(0), false)
			w.askOthers(key(0))
			w.clock.Advance(Wait)
			<-got
			close(w.r.release)
			w.settled(key(0))
		}},
		{"a read ending after its asker left (Codex round 3)", key(0), false, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			cancel, got := w.start(key(0), false)
			cancel()
			<-got
			w.askOthers(key(0))
			close(w.r.release)
			w.settled(key(0))
		}},
		{"a reader's refusal kept", key(0), false, func(w *orderWalk) {
			w.clock.Advance(MinInterval)
			cancel, got := w.start(key(0), true)
			cancel()
			<-got
			w.askOthers(key(0))
			close(w.r.release)
			w.settled(key(0))
			if v := w.c.viewOf(key(0)); v.State != StateError {
				w.t.Errorf("the refusal not kept: %s", v.State)
			}
		}},
		{"a reading growing stale and its cooldown ending", key(0), false, func(w *orderWalk) {
			w.clock.Advance(FreshFor)
		}},
		{"a key dropped", key(1), false, func(w *orderWalk) {
			w.read(key(MaxKeys)) // drops key(0), the least recently asked
		}},
		{"a first read ending after every kept key was asked", first, false, func(w *orderWalk) {
			cancel, got := w.start(first, false)
			cancel()
			<-got
			w.askOthers(first)
			close(w.r.release)
			w.settled(first)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrderWalk(t)
			tc.step(w)
			order := w.order()
			if len(order) != MaxKeys {
				t.Fatalf("%d keys kept, want %d", len(order), MaxKeys)
			}
			switch {
			case tc.moved && order[len(order)-1] != tc.subject:
				t.Fatalf("%v did not move to the most recently asked: %v is", tc.subject, order[len(order)-1])
			case tc.subject == first:
				// Not kept before, and the least recently asked as its read
				// ended: dropped as it ended, every kept key kept.
				if slices.Contains(order, first) || order[0] != key(0) {
					t.Fatalf("a first read ending moved its key: %v kept %v, the least recently asked %v", first, slices.Contains(order, first), order[0])
				}
			case !tc.moved && order[0] != tc.subject:
				t.Fatalf("%v moved: %v is the least recently asked, not it", tc.subject, order[0])
			}
			// One more key read drops the least recently asked: the
			// subject when it did not move.
			least, next := order[0], key(2*MaxKeys)
			w.read(next)
			after := w.order()
			if len(after) != MaxKeys || slices.Contains(after, least) || !slices.Contains(after, next) {
				t.Fatalf("one more key: %d kept, %v still kept %v, %v kept %v", len(after), least, slices.Contains(after, least), next, slices.Contains(after, next))
			}
			if slices.Contains(after, tc.subject) == !tc.moved {
				t.Errorf("%v kept %v after one more key, moved %v", tc.subject, !tc.moved, tc.moved)
			}
			if v := w.c.View(least); v.State != StateNeverRead {
				t.Errorf("a dropped key: %s", v.State)
			}
		})
	}
}

func TestCooldownOutlivesEviction(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	for i := range MaxKeys + 1 {
		c.Read(context.Background(), key(i), ReadOptions{})
	}
	if v := c.View(key(0)); v.State != StateNeverRead {
		t.Fatalf("key 0 still kept: %s", v.State)
	}
	v := c.Read(context.Background(), key(0), ReadOptions{})
	if r.count() != MaxKeys+1 || v.State != StateNeverRead || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Fatalf("read again at once: %d reads, %+v", r.count(), v)
	}
	clock.Advance(MinInterval)
	if v = c.Read(context.Background(), key(0), ReadOptions{}); r.count() != MaxKeys+2 || v.State != StateOK {
		t.Fatalf("after MinInterval: %d reads, %s", r.count(), v.State)
	}
}

func TestCooldownTableFull(t *testing.T) {
	c, clock, r := newTestCache(t, false)
	for i := range MaxCooldown {
		clock.Advance(time.Millisecond) // the first entry ends first
		c.Read(context.Background(), key(i), ReadOptions{})
	}
	v := c.Read(context.Background(), key(MaxCooldown), ReadOptions{})
	if r.count() != MaxCooldown {
		t.Fatalf("a read made with the table full: %d", r.count())
	}
	if want := t0.Add(time.Millisecond + MinInterval); v.NextRead == nil || !v.NextRead.Equal(want) || v.State != StateNeverRead {
		t.Fatalf("full: %+v, want nextReadAfter %v", v, want)
	}
	// Once the first entry has ended, there is room again.
	clock.Advance(MinInterval - time.Duration(MaxCooldown-1)*time.Millisecond)
	if v = c.Read(context.Background(), key(MaxCooldown), ReadOptions{}); r.count() != MaxCooldown+1 || v.State != StateOK {
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
	type told struct{ given, cached View }
	seen := make(chan told, 1)
	var c *Cache
	c = NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key, v View) { seen <- told{v, c.View(k)} }})
	t.Cleanup(c.Close)
	c.Read(context.Background(), key(1), ReadOptions{})
	select {
	case got := <-seen:
		for _, v := range []View{got.given, got.cached} {
			if v.State != StateOK || v.ReadAt == nil || !v.ReadAt.Equal(t0) {
				t.Fatalf("OnRead saw %+v", v)
			}
		}
	case <-time.After(time.Minute): // bounds a hang only
		t.Fatal("OnRead never called")
	}
}

func TestCacheReaderStateFailsClosed(t *testing.T) {
	clock := newClock()
	c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading {
		return Reading{State: "fine", Windows: []Window{{Kind: "weekly_all", Percent: 40}}}
	}})
	t.Cleanup(c.Close)
	v := c.Read(context.Background(), key(1), ReadOptions{})
	if v.State != StateError || v.Words == "" || v.ReadAt == nil || !v.ReadAt.Equal(t0) || len(v.Windows) != 0 {
		t.Fatalf("an unknown state: %+v", v)
	}
}

func TestCacheCloseEndsReads(t *testing.T) {
	clock := newClock()
	r := newReader(clock, true)
	c := NewCache(CacheOptions{Clock: clock, Read: r.read})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan View)
	go func() { done <- c.Read(ctx, key(1), ReadOptions{}) }()
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

// closedAnswer says whether v is the answer of a Read asked once Close has
// begun: an error in words, with no figure.
func closedAnswer(v View) bool {
	return v.State == StateError && v.Words == ClosedWords && len(v.Windows) == 0 && len(v.Credits) == 0 && v.ReadAt == nil
}

// TestCacheCloseStartsNothing: once Close has begun no read starts, in
// either order of a Read and a Close, and a read waiting for a slot gives
// up; Close returns only once every read it started has ended, and OnRead
// is not called once it has begun.
func TestCacheCloseStartsNothing(t *testing.T) {
	t.Run("a read held before its admission", func(t *testing.T) {
		clock := newClock()
		r := newReader(clock, false)
		c := NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key, _ View) { r.kept <- k }})
		entered, gate := make(chan struct{}), make(chan struct{})
		c.beforeAdmit = func() { close(entered); <-gate }
		got := make(chan View)
		go func() { got <- c.Read(context.Background(), key(1), ReadOptions{}) }()
		<-entered
		c.Close() // the Read is held before it takes the lock: nothing to wait for
		close(gate)
		if v := <-got; !closedAnswer(v) {
			t.Errorf("the Read let go after Close: %+v", v)
		}
		if n := r.count(); n != 0 {
			t.Errorf("the reader called %d times after Close", n)
		}
	})
	t.Run("a close held once marked", func(t *testing.T) {
		clock := newClock()
		r := newReader(clock, false)
		c := NewCache(CacheOptions{Clock: clock, Read: r.read})
		marked, gate := make(chan struct{}), make(chan struct{})
		c.afterClosed = func() { close(marked); <-gate }
		closed := make(chan struct{})
		go func() { c.Close(); close(closed) }()
		<-marked
		for i := range 3 {
			if v := c.Read(context.Background(), key(i), ReadOptions{Check: i == 2}); !closedAnswer(v) {
				t.Errorf("a Read during Close: %+v", v)
			}
		}
		close(gate)
		<-closed
		if n := r.count(); n != 0 {
			t.Errorf("the reader called %d times during Close", n)
		}
		if v := c.Read(context.Background(), key(9), ReadOptions{}); !closedAnswer(v) {
			t.Errorf("a Read after Close: %+v", v)
		}
	})
	t.Run("a read waiting for a slot", func(t *testing.T) {
		clock := newClock()
		r := newReader(clock, true)
		c := NewCache(CacheOptions{Clock: clock, Read: r.read, OnRead: func(k Key, _ View) { r.kept <- k }})
		var wg sync.WaitGroup
		for i := range 3 {
			wg.Go(func() { c.Read(context.Background(), key(i), ReadOptions{}) })
		}
		a, b := r.awaitEntered(t), r.awaitEntered(t)
		clock.awaitWaiters(t, 3)
		c.Close() // ends the two held at their gate through their context
		clock.Advance(Wait)
		wg.Wait()
		if n := r.count(); n != 2 {
			t.Fatalf("%d reads, the one waiting for a slot among them", n)
		}
		for i := range 3 {
			k := key(i)
			v := c.View(k)
			switch {
			case k == a || k == b:
				if v.State != StateOK {
					t.Errorf("a read under way at Close: %s", v.State)
				}
			case v.State != StateNeverRead:
				t.Errorf("the read waiting for a slot: %s", v.State)
			}
		}
		select {
		case k := <-r.kept:
			t.Errorf("OnRead called for %v once Close had begun", k)
		default:
		}
	})
}

// TestCacheCooldownFromReaderStart: a key's MinInterval starts when its
// reader is called, so a read that waited for a slot past MinInterval is
// still the only one in its MinInterval; and a read waiting for a slot
// holds its place in the cooldown table.
func TestCacheCooldownFromReaderStart(t *testing.T) {
	c, clock, r := newTestCache(t, true)
	// The table holds MaxCooldown-3 unexpired entries of other keys.
	c.mu.Lock()
	for i := range MaxCooldown - 3 {
		c.cool[Key{Agent: "codex", Account: fmt.Sprintf("a-%012d", i)}] = t0
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() { c.Read(context.Background(), key(i), ReadOptions{}) })
	}
	a, b := r.awaitEntered(t), r.awaitEntered(t)
	clock.awaitWaiters(t, 3)
	// Two entries for the reads under way, one place held by the read
	// waiting for a slot: the table is full, and a fourth key is not read.
	if v := c.Read(context.Background(), key(3), ReadOptions{}); v.State != StateNeverRead || v.NextRead == nil || !v.NextRead.Equal(t0.Add(MinInterval)) {
		t.Fatalf("a read with the table full: %+v", v)
	}
	clock.Advance(Wait)
	wg.Wait()
	// The other keys' entries end; the queued key still waits for a slot.
	clock.Advance(MinInterval)
	r.release <- struct{}{}
	queued := r.awaitEntered(t)
	if queued == a || queued == b {
		t.Fatalf("a key read twice: %v", queued)
	}
	started := clock.Now()
	close(r.release)
	for range 3 {
		r.awaitKept(t)
	}
	v := c.Read(context.Background(), queued, ReadOptions{})
	if n := r.count(); n != 3 || v.NextRead == nil || !v.NextRead.Equal(started.Add(MinInterval)) {
		t.Fatalf("the queued key asked again at once: %d reads, nextReadAfter %v, want %v", n, v.NextRead, started.Add(MinInterval))
	}
	c.mu.Lock()
	n := len(c.cool)
	c.mu.Unlock()
	if n > MaxCooldown {
		t.Errorf("%d cooldown entries", n)
	}
}

// intentReader answers what its read's Intent says, at the moment the
// test lets it ask (decide), as a usage reader asks at Versions.Admit.
type intentReader struct {
	entered chan Key
	decide  chan struct{}
	checks  chan bool
	release chan struct{}
}

func newIntentReader() *intentReader {
	return &intentReader{entered: make(chan Key, 8), decide: make(chan struct{}), checks: make(chan bool, 8), release: make(chan struct{})}
}

func (r *intentReader) read(ctx context.Context, k Key, in Intent) Reading {
	r.entered <- k
	<-r.decide
	r.checks <- in.Check()
	<-r.release
	return Reading{State: StateOK}
}

// TestCacheCheckIntent: a read carries the strongest intent of those who
// joined it until its reader asks: a check is not lost by joining a read
// without one, and a read without one does not lower a check; once the
// reader has asked, its answer stays.
func TestCacheCheckIntent(t *testing.T) {
	cases := []struct {
		name         string
		first, later bool // the first asker's check, the joiner's
		joinAfter    bool // the joiner asks once the reader has decided
		want         bool
	}{
		{"a check joins a read", false, true, false, true},
		{"a read joins a check", true, false, false, true},
		{"two reads", false, false, false, false},
		{"a check after the decision", false, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newClock()
			r := newIntentReader()
			c := NewCache(CacheOptions{Clock: clock, Read: r.read})
			t.Cleanup(c.Close)
			var wg sync.WaitGroup
			wg.Go(func() { c.Read(context.Background(), key(1), ReadOptions{Check: tc.first}) })
			<-r.entered
			clock.awaitWaiters(t, 1)
			var got bool
			if tc.joinAfter {
				r.decide <- struct{}{}
				got = <-r.checks // the reader has asked
			}
			wg.Go(func() { c.Read(context.Background(), key(1), ReadOptions{Check: tc.later}) })
			clock.awaitWaiters(t, 2) // the joiner is waiting: its intent is in
			if tc.joinAfter {
				if again := (Intent{c: c, f: c.flightOf(key(1))}).Check(); again != got {
					t.Errorf("the decided answer changed: %v, then %v", got, again)
				}
			} else {
				r.decide <- struct{}{}
				got = <-r.checks
			}
			if got != tc.want {
				t.Errorf("the reader's Check: %v, want %v", got, tc.want)
			}
			close(r.release)
			wg.Wait()
		})
	}
	// Another key's check is not this key's.
	clock := newClock()
	r := newIntentReader()
	c := NewCache(CacheOptions{Clock: clock, Read: r.read})
	t.Cleanup(c.Close)
	close(r.decide)
	close(r.release)
	c.Read(context.Background(), key(1), ReadOptions{Check: true})
	c.Read(context.Background(), key(2), ReadOptions{})
	if a, b := <-r.checks, <-r.checks; !a || b {
		t.Errorf("two keys' intents: %v, %v", a, b)
	}
}

// TestCacheKeepsCopies: no pointer or slice of a kept reading is shared
// with the reader that answered it or with a caller of View.
func TestCacheKeepsCopies(t *testing.T) {
	reset, active, enabled, spend := t0.Add(time.Hour), true, true, false
	used, limit, balance := "1.00", "2.00", "3.00"
	rd := Reading{State: StateOK,
		Windows: []Window{{Kind: "weekly_all", Name: "Weekly", Percent: 10, ResetsAt: &reset, Active: &active}},
		Credits: []Credit{{Kind: "credits", Enabled: &enabled, Used: &used, Limit: &limit, Balance: &balance}},
		Limits:  Limits{SpendControl: &spend, OrdinaryAllowed: &active}}
	c := NewCache(CacheOptions{Clock: newClock(), Read: func(context.Context, Key, Intent) Reading { return rd }})
	t.Cleanup(c.Close)
	first := c.Read(context.Background(), key(1), ReadOptions{})
	want, _ := json.Marshal(first)
	// The reader changes what it answered.
	reset, active, enabled, spend = t0.Add(-time.Hour), false, false, true
	used, limit, balance = "9", "9", strings.Repeat("9", 2*MaxFigure)
	rd.Windows[0].Percent, rd.Credits[0].Kind = 99, "changed"
	if got, _ := json.Marshal(c.View(key(1))); string(got) != string(want) {
		t.Fatalf("the reader's change reached the kept reading:\n%s\nwant\n%s", got, want)
	}
	// A caller changes what it was shown.
	v := c.View(key(1))
	*v.Windows[0].ResetsAt, *v.Windows[0].Active = t0.Add(-time.Hour), false
	*v.Credits[0].Enabled, *v.Credits[0].Used, *v.Credits[0].Limit, *v.Credits[0].Balance = false, "9", "9", strings.Repeat("9", 2*MaxFigure)
	*v.Limits.SpendControl, *v.Limits.OrdinaryAllowed = true, false
	*v.ReadAt = t0.Add(-time.Hour)
	v.ReadBy.Version = "changed"
	v.Windows[0].Percent, v.Credits[0].Kind = 99, "changed"
	v.Windows = append(v.Windows, Window{Kind: "added"})
	if got, _ := json.Marshal(c.View(key(1))); string(got) != string(want) {
		t.Fatalf("a caller's change reached the kept reading:\n%s\nwant\n%s", got, want)
	}
	if v := c.View(key(1)); v.Fresh != FreshNow {
		t.Errorf("a caller's change made the kept reading %s", v.Fresh)
	}
}

// TestCacheFailureCarriesNoFigures: a reading that is not ok holds its
// state and its words; nothing of the reader's plan, windows, credits,
// limits or versions rides beside the failure.
func TestCacheFailureCarriesNoFigures(t *testing.T) {
	yes := true
	bal := "5.00"
	for i, state := range []string{StateSignedOut, StateKeyPaid, StateUnqualified, StateError, "no such state"} {
		c := NewCache(CacheOptions{Clock: newClock(), Read: func(context.Context, Key, Intent) Reading {
			return Reading{State: state, Words: "the tool's words", Plan: "Invented Plan", Program: "Claude Code", Version: "2.1.400", Qualified: "2.1.318",
				Windows: []Window{{Kind: "weekly_all", Name: "Weekly", Percent: 100}},
				Credits: []Credit{{Kind: "credits", Balance: &bal}},
				Limits:  Limits{Reached: "weekly", OrdinaryAllowed: &yes}}
		}})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		c.Close()
		b, _ := json.Marshal(v)
		switch {
		case v.State == StateOK || v.Words == "":
			t.Errorf("%s: %s %q", state, v.State, v.Words)
		case len(v.Windows) != 0 || len(v.Credits) != 0 || v.Limits != (Limits{}) || v.Account.Plan != "" || v.ReadBy != nil || v.Agent.Version != "":
			t.Errorf("%s kept figures beside the failure: %s", state, b)
		case strings.Contains(string(b), "Invented Plan") || strings.Contains(string(b), "2.1.318") || strings.Contains(string(b), "5.00"):
			t.Errorf("%s: %s", state, b)
		}
	}
}

// TestCacheBoundsMakeUnknown: a value of an ok reading past its bound
// makes the reading error, with words naming the bound and no figure;
// nothing is cut or clamped to fit. Values at their bounds are kept as
// given; free text for people is cut, saying so.
func TestCacheBoundsMakeUnknown(t *testing.T) {
	long := func(n int) *string { s := strings.Repeat("9", n); return &s }
	over := map[string]func(r *Reading){
		"balance":           func(r *Reading) { r.Credits[0].Balance = long(MaxFigure + 1) },
		"used figure":       func(r *Reading) { r.Credits[0].Used = long(MaxFigure + 1) },
		"limit":             func(r *Reading) { r.Credits[0].Limit = long(MaxFigure + 1) },
		"currency":          func(r *Reading) { r.Credits[0].Currency = *long(MaxShort + 1) },
		"percent is -5":     func(r *Reading) { r.Windows[0].Percent = -5 },
		"percent is 101":    func(r *Reading) { r.Windows[0].Percent = 101 },
		"-1 minutes":        func(r *Reading) { r.Windows[0].Minutes = -1 },
		"window 1's kind":   func(r *Reading) { r.Windows[0].Kind = *long(MaxShort + 1) },
		"severity":          func(r *Reading) { r.Windows[0].Severity = *long(MaxShort + 1) },
		"window 1's limit":  func(r *Reading) { r.Windows[0].Limit = *long(MaxShort + 1) },
		"17 windows":        func(r *Reading) { r.Windows = make([]Window, MaxWindows+1) },
		"5 credit figures":  func(r *Reading) { r.Credits = make([]Credit, MaxCredits+1) },
		"the version":       func(r *Reading) { r.Version = *long(MaxVersion + 1) },
		"qualified version": func(r *Reading) { r.Qualified = *long(MaxVersion + 1) },
		"reached limit":     func(r *Reading) { r.Limits.Reached = *long(MaxShort + 1) },
	}
	good := func() Reading {
		return Reading{State: StateOK, Version: "2.1.318", Qualified: "2.1.318",
			Windows: []Window{{Kind: "weekly_all", Name: "Weekly", Percent: 40}},
			Credits: []Credit{{Kind: "credits", Balance: long(4)}}}
	}
	i := 0
	for words, change := range over {
		rd := good()
		change(&rd)
		c := NewCache(CacheOptions{Clock: newClock(), Read: func(context.Context, Key, Intent) Reading { return rd }})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		c.Close()
		i++
		if v.State != StateError || !strings.Contains(v.Words, words) || len(v.Windows) != 0 || len(v.Credits) != 0 || v.ReadBy != nil {
			t.Errorf("%s: %s %q, %d windows, %d credits", words, v.State, v.Words, len(v.Windows), len(v.Credits))
		}
	}
	// At the bounds: kept whole.
	rd := good()
	rd.Windows = make([]Window, MaxWindows)
	rd.Windows[0].Percent, rd.Windows[1].Percent = 0, 100
	rd.Windows[2].Name = strings.Repeat("n", MaxName+10) // for people: cut, saying so
	rd.Credits = make([]Credit, MaxCredits)
	rd.Credits[0].Balance = long(MaxFigure)
	rd.Version, rd.Plan = *long(MaxVersion), strings.Repeat("p", MaxPlan+1)
	c := NewCache(CacheOptions{Clock: newClock(), Read: func(context.Context, Key, Intent) Reading { return rd }})
	t.Cleanup(c.Close)
	v := c.Read(context.Background(), key(99), ReadOptions{})
	switch {
	case v.State != StateOK || len(v.Windows) != MaxWindows || len(v.Credits) != MaxCredits:
		t.Fatalf("at the bounds: %s %q, %d windows, %d credits", v.State, v.Words, len(v.Windows), len(v.Credits))
	case v.Windows[0].Percent != 0 || v.Windows[1].Percent != 100 || *v.Credits[0].Balance != *long(MaxFigure) || v.ReadBy.Version != *long(MaxVersion):
		t.Errorf("a value at its bound changed: %+v", v)
	case len(v.Windows[2].Name) > MaxName || !strings.HasSuffix(v.Windows[2].Name, cutMark):
		t.Errorf("a window's name: %q", v.Windows[2].Name)
	case len(v.Account.Plan) > MaxPlan || !strings.HasSuffix(v.Account.Plan, cutMark):
		t.Errorf("the plan: %q", v.Account.Plan)
	}
}

// TestCacheClockDecidesReadAt: a reading's time is the cache's clock's
// when the read returned, whatever the reader dated it, so a reader on
// another clock cannot keep a reading fresh; a reading dated ahead of the
// clock (the clock set back) is never fresh.
func TestCacheClockDecidesReadAt(t *testing.T) {
	clock := newClock()
	for i, d := range []time.Duration{24 * time.Hour, -24 * time.Hour} {
		c := NewCache(CacheOptions{Clock: clock, Read: func(context.Context, Key, Intent) Reading {
			return Reading{State: StateOK, ReadAt: clock.Now().Add(d)}
		}})
		v := c.Read(context.Background(), key(i), ReadOptions{})
		if v.ReadAt == nil || !v.ReadAt.Equal(clock.Now()) || v.Fresh != FreshNow {
			t.Errorf("dated %v: read at %v, %s", d, v.ReadAt, v.Fresh)
		}
		clock.Advance(FreshFor)
		if v := c.View(key(i)); v.Fresh != FreshStale {
			t.Errorf("dated %v, FreshFor later: %s", d, v.Fresh)
		}
		c.Close()
	}
	ahead := Reading{Key: key(1), State: StateOK, ReadAt: t0.Add(time.Hour)}
	if v := ahead.view(t0); v.Fresh != FreshStale {
		t.Errorf("a reading dated ahead of the clock: %s", v.Fresh)
	}
}

// viewOf is k's View, answered without asking for k.
func (c *Cache) viewOf(k Key) View {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewLocked(k, c.clock.Now())
}

// flightOf is the read asked of k and not yet kept, nil when none.
func (c *Cache) flightOf(k Key) *flight {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flights[k]
}
