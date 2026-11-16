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
