package capacity

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// t0 is the fake clocks' start, an invented moment.
var t0 = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeFacts answers as a catalog of three adapters would: two that move
// their login folder and one that cannot.
type fakeFacts struct {
	mu     sync.Mutex
	refuse map[string]string   // folder -> why CheckAccountDir refuses it
	bound  map[string][]string // id -> what Bound names
}

func newFacts() *fakeFacts {
	return &fakeFacts{refuse: map[string]string{}, bound: map[string][]string{}}
}

func (f *fakeFacts) ConfigDirEnv(adapter string) (string, bool) {
	env, ok := map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "copilot": ""}[adapter]
	return env, ok
}

func (f *fakeFacts) CheckAccountDir(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if why, ok := f.refuse[dir]; ok {
		return errors.New(why)
	}
	return nil
}

func (f *fakeFacts) Bound(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bound[id]
}

// openAt opens capacity/ in dir and owns it, as the server holding
// capacity/owner.lock does.
func openAt(t *testing.T, dir string, o Options) *Store {
	t.Helper()
	s := openNotOwned(t, dir, o)
	if err := s.Own(); err != nil {
		t.Fatal(err)
	}
	return s
}

// openNotOwned opens capacity/ in dir as a server without the owner lock.
func openNotOwned(t *testing.T, dir string, o Options) *Store {
	t.Helper()
	data, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(data, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func clockAt(at time.Time) func() time.Time { return func() time.Time { return at } }

// code is the Error code of err, "" for none.
func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// fakeClock is the cache's clock: Now moves only by Advance, and After's
// channels fire when Advance passes their deadline. Waiters says how many
// are waiting, so a test sees an asker waiting rather than guessing it.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	handed  int           // channels After has handed out, fired or not
	changed chan struct{} // closed and replaced at every new waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newClock() *fakeClock { return &fakeClock{now: t0, changed: make(chan struct{})} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	c.handed++
	close(c.changed)
	c.changed = make(chan struct{})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

// awaitWaiters waits until After has handed out n channels in all, so
// that many askers have come to their wait.
func (c *fakeClock) awaitWaiters(t *testing.T, n int) {
	t.Helper()
	for {
		c.mu.Lock()
		got, ch := c.handed, c.changed
		c.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-ch:
		case <-time.After(time.Minute): // bounds a hang only
			t.Fatalf("%d askers waiting, want %d", got, n)
		}
	}
}
