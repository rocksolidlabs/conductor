package session

import (
	"crypto/sha256"
	"sync"
	"testing"
)

// countedSet is a TokenSet that counts each hash's entries, as the
// server's does, and keeps every count it reached for each hash.
type countedSet struct {
	mu   sync.Mutex
	held map[[32]byte]int
	seen map[[32]byte][]int
}

func (c *countedSet) Add(h [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held, c.seen = map[[32]byte]int{}, map[[32]byte][]int{}
	}
	c.held[h]++
	c.seen[h] = append(c.seen[h], c.held[h])
}

func (c *countedSet) Remove(h [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[h] == 0 {
		return
	}
	c.held[h]--
	c.seen[h] = append(c.seen[h], c.held[h])
}

func (c *countedSet) count(tok string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[sha256.Sum256([]byte(tok))]
}

// A session handed the token it already holds, by itself or by a launch,
// takes the new entry and gives the old one back: one count before and
// after, never none between, and none once it ends.
func TestASessionTakingItsOwnTokenAgainKeepsOneCount(t *testing.T) {
	set := &countedSet{}
	s, _ := newLocalWith(t, Options{Tokens: set})
	tok := "session-token-0123456789abcdef-0123456789ab"
	s.SetAgentToken(tok)
	if n := set.count(tok); n != 1 {
		t.Fatalf("the token counts %d once set, not 1", n)
	}
	s.SetAgentToken(tok)
	if n := set.count(tok); n != 1 {
		t.Fatalf("the token counts %d once set again, not 1", n)
	}
	s.TakeAgentToken(tok, AddToken(set, sha256.Sum256([]byte(tok))))
	if n := set.count(tok); n != 1 {
		t.Fatalf("the token counts %d once handed again by a launch, not 1", n)
	}
	h := sha256.Sum256([]byte(tok))
	set.mu.Lock()
	seen := append([]int(nil), set.seen[h]...)
	set.mu.Unlock()
	for i, n := range seen {
		if n == 0 {
			t.Fatalf("the token counted none at step %d of %v while the session held it", i, seen)
		}
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := set.count(tok); n != 0 {
		t.Fatalf("the token counts %d once the session ended, not 0", n)
	}
}
