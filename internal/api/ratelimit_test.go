package api

import (
	"strings"
	"testing"
	"time"
)

// A server made and shut down leaves no goroutine of its rate limiters:
// they start none. The subtest runs under a label of its own
// (underFixtureLabel), so only what its server started is read.
func TestAServersRateLimitersLeaveNoGoroutine(t *testing.T) {
	value := underFixtureLabel(t, "a server shut down", func(t *testing.T) {
		e := newTestEnv(t, nil)
		shutAtEnd(t, e)
		e.srv.limiter.allow("192.0.2.1")
		e.srv.hostLimiter.allow("192.0.2.1")
	})
	if _, stacks := fixtureGoroutines(value); strings.Contains(stacks, "ateLimiter") {
		t.Fatalf("a rate limiter's goroutine is left once its server was shut down:\n%s", stacks)
	}
}

// A rate limiter forgets a client idle for five minutes on its own clock,
// as it lets another through, and keeps one that came within them.
func TestARateLimiterForgetsAnIdleClient(t *testing.T) {
	rl := newRateLimiter(1, 2)
	at := time.Unix(1_000_000, 0)
	rl.now = func() time.Time { return at }
	rl.allow("idle")
	rl.allow("recent")
	at = at.Add(4 * time.Minute)
	rl.allow("recent")
	if _, ok := rl.buckets["idle"]; !ok {
		t.Fatal("a client idle for four minutes was forgotten")
	}
	at = at.Add(time.Minute + time.Second)
	rl.allow("new")
	for key, want := range map[string]bool{"idle": false, "recent": true, "new": true} {
		if _, ok := rl.buckets[key]; ok != want {
			t.Fatalf("client %q kept %v past five minutes of the idle one's, want %v", key, ok, want)
		}
	}
}

// A rate limiter looks for idle clients at most once a minute: a client
// idle for five minutes and more, less than a minute after the last look,
// is kept until the minute has passed, and forgotten at the next look.
func TestARateLimiterLooksForIdleClientsOnceAMinute(t *testing.T) {
	rl := newRateLimiter(1, 2)
	start := time.Unix(1_000_000, 0)
	at := start
	rl.now = func() time.Time { return at }
	rl.allow("idle")
	// A look, the idle client idle for less than five minutes.
	at = start.Add(4*time.Minute + 30*time.Second)
	rl.allow("other")
	for _, after := range []time.Duration{5*time.Minute + 10*time.Second, 5*time.Minute + 29*time.Second} {
		at = start.Add(after)
		rl.allow("other")
		if _, ok := rl.buckets["idle"]; !ok {
			t.Fatalf("a client idle for %v was forgotten %v after the last look", after, after-4*time.Minute-30*time.Second)
		}
	}
	at = start.Add(5*time.Minute + 30*time.Second)
	rl.allow("other")
	if _, ok := rl.buckets["idle"]; ok {
		t.Fatal("a client idle for more than five minutes was kept a minute after the last look")
	}
}
