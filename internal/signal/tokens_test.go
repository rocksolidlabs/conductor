package signal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// tokenWait bounds a wait that only a hang would spend.
const tokenWait = 10 * time.Second

// tokenSet is a session.TokenSet counted as the server's is, which keeps
// every count each hash reached and calls after, when set, after each
// change (with the session's lock held, so it must not take it).
type tokenSet struct {
	mu    sync.Mutex
	held  map[[32]byte]int
	seen  map[[32]byte][]int
	after func()
}

func (c *tokenSet) change(h [32]byte, by int) {
	c.mu.Lock()
	if c.held == nil {
		c.held, c.seen = map[[32]byte]int{}, map[[32]byte][]int{}
	}
	if by < 0 && c.held[h] == 0 {
		c.mu.Unlock()
		return
	}
	c.held[h] += by
	if c.held[h] == 0 {
		delete(c.held, h)
	}
	c.seen[h] = append(c.seen[h], c.held[h])
	after := c.after
	c.mu.Unlock()
	if after != nil {
		after()
	}
}

func (c *tokenSet) Add(h [32]byte)    { c.change(h, 1) }
func (c *tokenSet) Remove(h [32]byte) { c.change(h, -1) }

// size is the number of distinct hashes held, as the server's set counts.
func (c *tokenSet) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.held)
}

func (c *tokenSet) holds(tok string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[sha256.Sum256([]byte(tok))] > 0
}

// counts is every count tok's hash reached, in order.
func (c *tokenSet) counts(tok string) []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.seen[sha256.Sum256([]byte(tok))]...)
}

// hostToken is a token of the length the server mints, made of c.
func hostToken(c string) string { return strings.Repeat(c, share.TokenLen) }

// tokenHub is a hub whose registry retires a session it removes, as the
// server's does, with its set of live tokens.
func tokenHub(t *testing.T, max int, log *slog.Logger) (*Hub, *session.Registry, *tokenSet) {
	t.Helper()
	reg := session.NewRegistry(max)
	reg.OnRemove = func(_ string, d session.Driver) {
		if hs, ok := d.(*HostedSession); ok {
			hs.Retire()
		}
	}
	set := &tokenSet{}
	hub := NewHub(reg, log)
	hub.Tokens = set
	return hub, reg, set
}

// registerToken registers a session with agent token tok, under instance
// and local id 1 when instance is set.
func registerToken(hub *Hub, instance, tok string, conn *HostConn) (*HostedSession, bool, error) {
	localID := ""
	if instance != "" {
		localID = "1"
	}
	return hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host: proto.HostInfo{Name: "laptop", Instance: instance}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: localID, AgentToken: tok}}, conn)
}

// resumeToken resumes hs with its secret and agent token tok.
func resumeToken(hub *Hub, hs *HostedSession, tok string, conn *HostConn) (*HostedSession, bool, error) {
	return hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"},
		Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, AgentToken: tok},
		Resume:  &proto.HostResume{SessionID: hs.Info().ID, Secret: hs.secret}}, conn)
}

func wantSize(t *testing.T, set *tokenSet, n int, when string) {
	t.Helper()
	if got := set.size(); got != n {
		t.Fatalf("the set holds %d tokens %s, not %d", got, when, n)
	}
}

// A hosted session's token is in the set from its registration until it
// leaves the registry (Retire), whichever comes first: the registration
// let through and then the retirement, 1 then 0; the retirement made
// while the registration is held after the registry took the session
// (OnAdded), 0 at once and 0 once the registration returns.
func TestAHostedTokenIsKnownFromRegistrationToRetirement(t *testing.T) {
	t.Run("registration first", func(t *testing.T) {
		hub, reg, set := tokenHub(t, 4, nil)
		tok := hostToken("a")
		hs, _, err := registerToken(hub, "", tok, NewHostConn())
		if err != nil {
			t.Fatal(err)
		}
		wantSize(t, set, 1, "once the session registered")
		if !set.holds(tok) {
			t.Fatal("the set does not hold the session's token")
		}
		reg.Remove(hs.Info().ID)
		wantSize(t, set, 0, "once the session left the registry")
	})
	t.Run("retirement first", func(t *testing.T) {
		hub, reg, set := tokenHub(t, 4, nil)
		added, gate := make(chan *HostedSession, 1), make(chan struct{})
		hub.OnAdded = func(hs *HostedSession) {
			added <- hs
			<-gate
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := registerToken(hub, "", hostToken("a"), NewHostConn())
			done <- err
		}()
		var hs *HostedSession
		select {
		case hs = <-added:
		case <-time.After(tokenWait):
			t.Fatal("the registration never reached OnAdded")
		}
		wantSize(t, set, 1, "while the registration is held, the registry holding it")
		reg.Remove(hs.Info().ID)
		wantSize(t, set, 0, "once the held registration's session left the registry")
		close(gate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		wantSize(t, set, 0, "once the registration of a retired session returned")
	})
}

// A resume that carries another token swaps the old one for it with no
// moment where neither is held, while the session lives; its end after
// removes the new one. Ended first, the resume adds none.
func TestAResumeWithAnotherTokenSwapsWithoutAGap(t *testing.T) {
	first, second := hostToken("a"), hostToken("b")
	t.Run("resume, then the status", func(t *testing.T) {
		hub, _, set := tokenHub(t, 4, nil)
		conn := NewHostConn()
		hs, _, err := registerToken(hub, "", first, conn)
		if err != nil {
			t.Fatal(err)
		}
		hs.HostDisconnected(conn)
		wantSize(t, set, 1, "once the host went")
		var gap []string
		set.after = func() {
			if !set.holds(first) && !set.holds(second) {
				gap = append(gap, "neither token held")
			}
		}
		if _, resumed, err := resumeToken(hub, hs, second, NewHostConn()); err != nil || !resumed {
			t.Fatalf("resume: %v %v", err, resumed)
		}
		set.after = nil
		if len(gap) != 0 {
			t.Fatalf("the swap left a moment with %v", gap)
		}
		if set.holds(first) || !set.holds(second) {
			t.Fatalf("after the resume the set holds the first token %v, the second %v; want false, true", set.holds(first), set.holds(second))
		}
		wantSize(t, set, 1, "once the session resumed with another token")
		hs.HostStatus(session.StatusExited, nil)
		wantSize(t, set, 0, "once the resumed session's host said it exited")
	})
	t.Run("the status, then resume", func(t *testing.T) {
		hub, _, set := tokenHub(t, 4, nil)
		conn := NewHostConn()
		hs, _, err := registerToken(hub, "", first, conn)
		if err != nil {
			t.Fatal(err)
		}
		hs.HostStatus(session.StatusExited, nil)
		wantSize(t, set, 0, "once the host said the session exited")
		hs.HostDisconnected(conn)
		if _, resumed, err := resumeToken(hub, hs, second, NewHostConn()); err != nil || !resumed {
			t.Fatalf("resume: %v %v", err, resumed)
		}
		wantSize(t, set, 0, "once the ended session resumed with another token")
		if !hs.AgentTokenOK(second) {
			t.Fatal("the ended session does not take the resume's token at its own routes")
		}
	})
}

// A host that comes back with the token its session holds keeps it held
// throughout: its count goes 1, 2, 1, never 0.
func TestAReconnectWithTheSameTokenNeverDropsIt(t *testing.T) {
	hub, _, set := tokenHub(t, 4, nil)
	tok := hostToken("a")
	conn := NewHostConn()
	hs, _, err := registerToken(hub, "instance-a", tok, conn)
	if err != nil {
		t.Fatal(err)
	}
	hs.HostDisconnected(conn)
	if again, resumed, err := registerToken(hub, "instance-a", tok, NewHostConn()); err != nil || !resumed || again != hs {
		t.Fatalf("registering again: %v %v", err, resumed)
	}
	if got := set.counts(tok); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("the token's counts were %v, not [1 2 1]", got)
	}
}

// An end is final for the entry: an attach that carries a token adds none
// to a session that ended or left the registry, and a running after an
// exited makes the token known no more.
func TestAnEndedOrRetiredSessionTakesNoToken(t *testing.T) {
	t.Run("ended", func(t *testing.T) {
		hub, _, set := tokenHub(t, 4, nil)
		conn := NewHostConn()
		hs, _, err := registerToken(hub, "instance-a", hostToken("a"), conn)
		if err != nil {
			t.Fatal(err)
		}
		hs.HostStatus(session.StatusExited, nil)
		hs.HostDisconnected(conn)
		if _, resumed, err := registerToken(hub, "instance-a", hostToken("b"), NewHostConn()); err != nil || !resumed {
			t.Fatalf("registering again: %v %v", err, resumed)
		}
		wantSize(t, set, 0, "once an ended session was attached with a token")
		hs.HostStatus(session.StatusRunning, nil)
		wantSize(t, set, 0, "once an ended session's host said running")
	})
	t.Run("retired", func(t *testing.T) {
		hub, reg, set := tokenHub(t, 4, nil)
		conn := NewHostConn()
		hs, _, err := registerToken(hub, "instance-a", hostToken("a"), conn)
		if err != nil {
			t.Fatal(err)
		}
		hs.HostDisconnected(conn)
		reg.Remove(hs.Info().ID)
		wantSize(t, set, 0, "once the session left the registry")
		if again, resumed, err := registerToken(hub, "instance-a", hostToken("b"), NewHostConn()); err != nil || !resumed || again != hs {
			t.Fatalf("registering again while the hub still holds it: %v %v", err, resumed)
		}
		wantSize(t, set, 0, "once a retired session was attached with a token")
	})
	t.Run("running after exited", func(t *testing.T) {
		hub, _, set := tokenHub(t, 4, nil)
		hs, _, err := registerToken(hub, "", hostToken("a"), NewHostConn())
		if err != nil {
			t.Fatal(err)
		}
		hs.HostStatus(session.StatusExited, nil)
		hs.HostStatus(session.StatusRunning, nil)
		wantSize(t, set, 0, "once a host said running after exited")
	})
}

// A registration the registry refuses leaves no entry, and no mark that
// refuses the same instance once a place is free.
func TestAFailedRegistrationLeavesNoToken(t *testing.T) {
	hub, _, set := tokenHub(t, 1, nil)
	first, _, err := registerToken(hub, "", hostToken("a"), NewHostConn())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registerToken(hub, "instance-b", hostToken("b"), NewHostConn()); !errors.Is(err, session.ErrTooManySessions) {
		t.Fatalf("a registration past the registry's cap: %v", err)
	}
	if set.holds(hostToken("b")) {
		t.Fatal("a refused registration left its token in the set")
	}
	wantSize(t, set, 1, "once a registration was refused")
	first.HostStatus(session.StatusExited, nil)
	if _, _, err := registerToken(hub, "instance-b", hostToken("b"), NewHostConn()); err != nil {
		t.Fatalf("the same instance once a place is free: %v", err)
	}
	wantSize(t, set, 1, "once the instance registered")
}

// No entry is made for a token the set's one read never matches: none
// given, or one of another length than the server mints; one that
// replaces a held token takes the held one's entry with it.
func TestNoEntryForAnEmptyOrWrongLengthToken(t *testing.T) {
	hub, _, set := tokenHub(t, 8, nil)
	for _, tok := range []string{"", strings.Repeat("s", share.TokenLen-1), strings.Repeat("l", share.TokenLen+1)} {
		if _, _, err := registerToken(hub, "", tok, NewHostConn()); err != nil {
			t.Fatal(err)
		}
		wantSize(t, set, 0, fmt.Sprintf("once a session registered with a token %d long", len(tok)))
	}
	conn := NewHostConn()
	hs, _, err := registerToken(hub, "instance-a", hostToken("a"), conn)
	if err != nil {
		t.Fatal(err)
	}
	wantSize(t, set, 1, "once a session registered with a token of the minted length")
	hs.HostDisconnected(conn)
	short := strings.Repeat("s", share.TokenLen-1)
	if _, resumed, err := registerToken(hub, "instance-a", short, NewHostConn()); err != nil || !resumed {
		t.Fatalf("registering again: %v %v", err, resumed)
	}
	wantSize(t, set, 0, "once the session took a token of another length")
	if !hs.AgentTokenOK(short) {
		t.Fatal("the session does not take its own token at its routes")
	}
}

// A desk carries no agent token and holds no entry.
func TestADeskHoldsNoToken(t *testing.T) {
	hub, _, set := tokenHub(t, 4, nil)
	if _, _, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn()); err != nil {
		t.Fatal(err)
	}
	wantSize(t, set, 0, "once a desk registered")
}
