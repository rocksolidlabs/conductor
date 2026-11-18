package signal

import (
	"context"
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
		reg2 := proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop", Instance: "instance-a"},
			Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: "1", AgentToken: hostToken("b")}}
		if hs.attach(reg2, NewHostConn()) {
			t.Fatal("a retired session took a host")
		}
		wantSize(t, set, 0, "once a retired session was offered a host with a token")
		hs.mu.Lock()
		hs.setAgentToken(hostToken("b"))
		hs.mu.Unlock()
		wantSize(t, set, 0, "once a retired session was handed a token")
		again, resumed, err := registerToken(hub, "instance-a", hostToken("b"), NewHostConn())
		if err != nil || resumed || again == hs {
			t.Fatalf("registering again while the hub still holds the retired session: %v %v", err, resumed)
		}
		if !set.holds(hostToken("b")) || set.size() != 1 {
			t.Fatal("the set does not hold the new session's token alone")
		}
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

// slowDriver is a session in the registry whose Info waits at gate once
// armed: a registry Add counts the sessions by their Info, so a
// registration is held inside its Add, after its look-up of the id.
type slowDriver struct {
	mu    sync.Mutex
	armed bool
	at    chan struct{}
	gate  chan struct{}
}

func (d *slowDriver) Info() session.Info {
	d.mu.Lock()
	armed := d.armed
	d.armed = false
	d.mu.Unlock()
	if armed {
		close(d.at)
		<-d.gate
	}
	return session.Info{ID: "slow", Kind: session.KindHosted, Status: session.StatusRunning}
}
func (*slowDriver) Stop(context.Context) error  { return nil }
func (*slowDriver) DisconnectLink(string)       {}
func (*slowDriver) EndLink(string)              {}
func (*slowDriver) LinkViewers() map[string]int { return nil }

// Two first registrations of one instance make one session: the second,
// made while the first is held inside the registry's Add, is refused at
// once and makes nothing; the hub and the registry hold the first, and
// the set its token alone.
func TestTwoFirstRegistrationsOfOneInstanceMakeOneSession(t *testing.T) {
	hub, reg, set := tokenHub(t, 4, nil)
	slow := &slowDriver{at: make(chan struct{}), gate: make(chan struct{})}
	if err := reg.Add(slow); err != nil {
		t.Fatal(err)
	}
	slow.mu.Lock()
	slow.armed = true
	slow.mu.Unlock()
	type result struct {
		hs  *HostedSession
		err error
	}
	firstDone, secondDone := make(chan result, 1), make(chan result, 1)
	go func() {
		hs, _, err := registerToken(hub, "instance-a", hostToken("a"), NewHostConn())
		firstDone <- result{hs, err}
	}()
	select {
	case <-slow.at:
	case <-time.After(tokenWait):
		t.Fatal("the first registration never reached the registry's Add")
	}
	go func() {
		hs, _, err := registerToken(hub, "instance-a", hostToken("b"), NewHostConn())
		secondDone <- result{hs, err}
	}()
	var second result
	select {
	case second = <-secondDone:
	case <-time.After(tokenWait):
		// Waiting behind the first: let both through, and judge them.
		close(slow.gate)
		second = <-secondDone
		<-firstDone
		t.Fatalf("the second registration waited behind the first and made %v (%v)", second.hs != nil, second.err)
	}
	if !errors.Is(second.err, ErrBadRegister) || second.hs != nil {
		t.Fatalf("the second first registration: %v %v", second.hs, second.err)
	}
	close(slow.gate)
	first := <-firstDone
	if first.err != nil {
		t.Fatal(first.err)
	}
	id := HostedID("instance-a", "1")
	if got, ok := hub.Get(id); !ok || got != first.hs {
		t.Fatal("the hub does not hold the first registration's session")
	}
	if d, ok := reg.Get(id); !ok || d != session.Driver(first.hs) {
		t.Fatal("the registry does not hold the first registration's session")
	}
	if reg.Count() != 2 {
		t.Fatalf("the registry holds %d sessions, not the slow one and the first", reg.Count())
	}
	if !set.holds(hostToken("a")) || set.holds(hostToken("b")) {
		t.Fatal("the set does not hold the first registration's token alone")
	}
	wantSize(t, set, 1, "once both registrations returned")
}

// holdingHandler is a logger's handler that holds the record named msg at
// gate once, after saying so on at: what the hub logs between two steps.
type holdingHandler struct {
	msg      string
	once     sync.Once
	at, gate chan struct{}
}

func (h *holdingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *holdingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.once.Do(func() {
			close(h.at)
			<-h.gate
		})
	}
	return nil
}
func (h *holdingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *holdingHandler) WithGroup(string) slog.Handler      { return h }

// A session expired as the same instance registers again leaves exactly
// the live session's token known, whichever comes first: the expired
// session's entry goes as it leaves the hub's table, and the registry
// loses the expired session alone. Held between the table and the
// registry, a registration in between makes a new session, which stays
// registered, unretired, its token held; the expired one is retired.
func TestAnExpiryBesideARegistrationLeavesTheLiveToken(t *testing.T) {
	for _, held := range []bool{true, false} {
		name := "registration after the expiry"
		if held {
			name = "registration inside the expiry"
		}
		t.Run(name, func(t *testing.T) {
			log := &holdingHandler{msg: "hosted session expired", at: make(chan struct{}), gate: make(chan struct{})}
			hub, reg, set := tokenHub(t, 4, slog.New(log))
			old, fresh := hostToken("o"), hostToken("n")
			conn := NewHostConn()
			gone, _, err := registerToken(hub, "instance-a", old, conn)
			if err != nil {
				t.Fatal(err)
			}
			gone.HostDisconnected(conn)
			wantSize(t, set, 1, "once the host went")
			expired := make(chan struct{})
			if !held {
				close(log.gate)
			}
			go func() {
				hub.Expire(time.Now().Add(DisconnectGrace + time.Second))
				close(expired)
			}()
			select {
			case <-log.at:
			case <-time.After(tokenWait):
				t.Fatal("the expiry never reached the registry")
			}
			if !held {
				<-expired
			}
			wantSize(t, set, 0, "once the session left the hub's table")
			live, resumed, err := registerToken(hub, "instance-a", fresh, NewHostConn())
			if err != nil || resumed || live == gone {
				t.Fatalf("registering again: %v %v", err, resumed)
			}
			wantSize(t, set, 1, "once the instance registered again")
			if held {
				close(log.gate)
				<-expired
			}
			if d, ok := reg.Get(live.Info().ID); !ok || d != session.Driver(live) {
				t.Fatal("the registry does not hold the live session")
			}
			live.mu.Lock()
			retired := live.retired
			live.mu.Unlock()
			gone.mu.Lock()
			goneRetired := gone.retired
			gone.mu.Unlock()
			if retired || !goneRetired {
				t.Fatalf("the live session retired %v, the expired one %v; want false, true", retired, goneRetired)
			}
			if set.holds(old) || !set.holds(fresh) {
				t.Fatalf("the set holds the expired token %v, the live one %v; want false, true", set.holds(old), set.holds(fresh))
			}
			wantSize(t, set, 1, "once the expiry returned")
		})
	}
}

// attachedTo says whether hs holds conn as its host's connection, and
// whether it was retired.
func attachedTo(hs *HostedSession, conn *HostConn) (attached, retired bool) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.conn == conn, hs.retired
}

// A registration whose look-up found the session just before an expiry
// took it out of the hub's table never puts its host on that session.
// The expiry is held between its table delete and its registry removal
// (the hub's logger holds its "hosted session expired" record); there the
// registration's step after its look-up, the attach, is made as Register
// makes it, and refused; the expiry let go, the session is retired, no
// host on it; the instance registering again makes a new session, which
// the hub and the registry both hold, its host on it.
func TestARegistrationMeetingAnExpiryNeverAttachesToTheExpiredSession(t *testing.T) {
	log := &holdingHandler{msg: "hosted session expired", at: make(chan struct{}), gate: make(chan struct{})}
	hub, reg, set := tokenHub(t, 4, slog.New(log))
	conn := NewHostConn()
	gone, _, err := registerToken(hub, "instance-a", hostToken("o"), conn)
	if err != nil {
		t.Fatal(err)
	}
	gone.HostDisconnected(conn)
	expired := make(chan struct{})
	go func() {
		hub.Expire(time.Now().Add(DisconnectGrace + time.Second))
		close(expired)
	}()
	select {
	case <-log.at:
	case <-time.After(tokenWait):
		t.Fatal("the expiry never reached the registry")
	}
	late := NewHostConn()
	again := proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host: proto.HostInfo{Name: "laptop", Instance: "instance-a"}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: "1", AgentToken: hostToken("o")}}
	took := gone.attach(again, late)
	close(log.gate)
	<-expired
	if attached, retired := attachedTo(gone, late); took || attached || !retired {
		t.Fatalf("the expired session took the late host %v (on it %v), retired %v; want false, false, true", took, attached, retired)
	}
	wantSize(t, set, 0, "once the session expired")
	live, resumed, err := hub.Register(again, NewHostConn())
	if err != nil || resumed || live == gone {
		t.Fatalf("registering again: %v %v", err, resumed)
	}
	id := HostedID("instance-a", "1")
	if h, ok := hub.Get(id); !ok || h != live {
		t.Fatal("the hub does not hold the new session")
	}
	if d, ok := reg.Get(id); !ok || d != session.Driver(live) {
		t.Fatal("the registry does not hold the new session")
	}
	wantSize(t, set, 1, "once the instance registered again")
}

// A session that left the registry while the hub still holds it (its
// removal comes before the hub forgets it) is never taken back: the same
// instance registering again makes a new session, which the hub and the
// registry both hold and an expiry leaves alone; a resume with the old
// session's secret is refused.
func TestARegistrationNeverTakesBackASessionTheRegistryLetGo(t *testing.T) {
	hub, reg, set := tokenHub(t, 4, nil)
	conn := NewHostConn()
	old, _, err := registerToken(hub, "instance-a", hostToken("o"), conn)
	if err != nil {
		t.Fatal(err)
	}
	old.HostDisconnected(conn)
	id := old.Info().ID
	reg.Remove(id)
	if _, _, err := resumeToken(hub, old, hostToken("o"), NewHostConn()); !errors.Is(err, ErrBadResume) {
		t.Fatalf("a resume of a session the registry let go: %v", err)
	}
	fresh := NewHostConn()
	live, resumed, err := registerToken(hub, "instance-a", hostToken("n"), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if resumed || live == old {
		t.Fatalf("the instance took back a session the registry let go (resumed %v)", resumed)
	}
	if live.secret == old.secret {
		t.Fatal("the session registered anew kept the old session's secret")
	}
	if attached, _ := attachedTo(old, fresh); attached {
		t.Fatal("the host is on the session the registry let go")
	}
	hub.Expire(time.Now())
	if h, ok := hub.Get(id); !ok || h != live {
		t.Fatal("the hub does not hold the new session after an expiry")
	}
	if d, ok := reg.Get(id); !ok || d != session.Driver(live) {
		t.Fatal("the registry does not hold the new session")
	}
	if attached, retired := attachedTo(live, fresh); !attached || retired {
		t.Fatalf("the new session holds its host %v, retired %v; want true, false", attached, retired)
	}
	if set.holds(hostToken("o")) || !set.holds(hostToken("n")) {
		t.Fatal("the set does not hold the new session's token alone")
	}
}

// A desk its host ended is forgotten in the same hold of the hub's lock
// that takes it out of the table, under its own lock: no moment comes
// where the table has let it go and a host can still be put on it. The
// moment has no seam (EndDesk's table delete and its retirement follow
// each other with nothing between), so the test reads the mark the hold
// leaves: forgotten as it leaves the table, and refusing a host after.
func TestAnEndedDeskIsForgottenAsItLeavesTheTable(t *testing.T) {
	hub, reg, _ := tokenHub(t, 4, nil)
	desk, _, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn())
	if err != nil {
		t.Fatal(err)
	}
	desk.EndDesk()
	if _, ok := hub.Desk(desk.Info().ID); ok {
		t.Fatal("the hub still holds an ended desk")
	}
	desk.mu.Lock()
	forgotten, retired := desk.forgotten, desk.retired
	desk.mu.Unlock()
	if !forgotten || !retired {
		t.Fatalf("an ended desk forgotten %v, retired %v; want true, true", forgotten, retired)
	}
	if desk.attach(proto.Register{Host: proto.HostInfo{Name: "box", Instance: "inst-a"}}, NewHostConn()) {
		t.Fatal("an ended desk took a host")
	}
	if n := reg.Count(); n != 0 {
		t.Fatalf("the registry holds %d sessions for a desk", n)
	}
}

// A desk registration that meets a desk that left (retired, or forgotten)
// registers the desk anew, as a session's registration does: the hub
// holds the new desk, its host on it, and the old desk's place among the
// sessions is given back; a resume of the old desk is refused.
func TestADeskRegistrationMeetingARetiredDeskRegistersItAnew(t *testing.T) {
	hub, reg, _ := tokenHub(t, 2, nil)
	conn := NewHostConn()
	old, _, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), conn)
	if err != nil {
		t.Fatal(err)
	}
	old.HostDisconnected(conn)
	// The state an end in the moment after a look-up leaves: retired,
	// and still in the table.
	old.Retire()
	resume := deskOf("inst-a", "run-1")
	resume.Resume = &proto.HostResume{SessionID: old.Info().ID, Secret: old.secret}
	if _, _, err := hub.RegisterDesk(resume, NewHostConn()); !errors.Is(err, ErrBadResume) {
		t.Fatalf("a resume of a retired desk: %v", err)
	}
	fresh := NewHostConn()
	desk, resumed, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), fresh)
	if err != nil {
		t.Fatalf("registering the desk again: %v", err)
	}
	if resumed || desk == old {
		t.Fatalf("the retired desk was taken back (resumed %v)", resumed)
	}
	if h, ok := hub.Desk(desk.Info().ID); !ok || h != desk {
		t.Fatal("the hub does not hold the new desk")
	}
	if attached, retired := attachedTo(desk, fresh); !attached || retired {
		t.Fatalf("the new desk holds its host %v, retired %v; want true, false", attached, retired)
	}
	// Two places: the new desk holds one, the old one's is free again.
	if _, err := reg.Reserve(); err != nil {
		t.Fatalf("the old desk's place was not given back: %v", err)
	}
}

// A session and a desk never take each other's id: a desk registration
// that finds a session under the desk's id is refused, as is a session's
// registration that finds a desk under its id; neither attaches.
func TestASessionAndADeskNeverTakeEachOthersID(t *testing.T) {
	hub, _, _ := tokenHub(t, 4, nil)
	conn := NewHostConn()
	sess, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "box", Instance: "inst-a"},
		Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: proto.DeskLocalID("run-1")}}, conn)
	if err != nil {
		t.Fatal(err)
	}
	sess.HostDisconnected(conn)
	if _, _, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("a desk registration over a session's id: %v", err)
	}
	dconn := NewHostConn()
	desk, _, err := hub.RegisterDesk(deskOf("inst-a", "run-2"), dconn)
	if err != nil {
		t.Fatal(err)
	}
	desk.HostDisconnected(dconn)
	if _, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "box", Instance: "inst-a"},
		Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: proto.DeskLocalID("run-2")}}, NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("a session registration over a desk's id: %v", err)
	}
	for _, hs := range []*HostedSession{sess, desk} {
		hs.mu.Lock()
		attached := hs.conn != nil
		hs.mu.Unlock()
		if attached {
			t.Fatal("a registration of the other kind put its host on it")
		}
	}
}
