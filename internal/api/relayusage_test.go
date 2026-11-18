package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The relay's usage told to an identity provider that observes it
// (relayusage.go). Every wait is on the mechanism: the periods are fired
// through the server's clock seam (usageClock), the reports' times come
// from the desk's own clock (usageTime), a host's frames are known counted
// once a message the host sent after them is answered (a link minted on
// its connection), a viewer's once the host has them (relayTap), and the
// reports are taken as the observer is handed them.

// usageObserver is credIdentity (its admissions, gates and asks) that is
// also an ext.UsageObserver: it hands each report to reports, after
// holding the call at block while one is set, and panics on the next
// report while panics is above zero.
type usageObserver struct {
	*credIdentity
	reports chan ext.Usage
	entered chan struct{} // told as a held call begins
	block   atomic.Pointer[chan struct{}]
	panics  atomic.Int32
}

func newUsageObserver() *usageObserver {
	return &usageObserver{credIdentity: newCredIdentity(), reports: make(chan ext.Usage, 4096), entered: make(chan struct{}, 1)}
}

func (o *usageObserver) Used(u ext.Usage) {
	if b := o.block.Load(); b != nil {
		o.entered <- struct{}{}
		<-*b
	}
	if o.panics.Add(-1) >= 0 {
		panic("the observer's own words: " + u.Subject)
	}
	o.reports <- u
}

// next is the next report the observer is handed.
func (o *usageObserver) next(t *testing.T) ext.Usage {
	t.Helper()
	select {
	case u := <-o.reports:
		return u
	case <-time.After(testWait):
		t.Fatal("no report came")
	}
	return ext.Usage{}
}

// none fails if a report is waiting.
func (o *usageObserver) none(t *testing.T) {
	t.Helper()
	select {
	case u := <-o.reports:
		t.Fatalf("a report came: %+v", u)
	default:
	}
}

// usageClock is the server's clock seam, its waits kept apart by length,
// so a connection's period (ext.UsageEvery) and its recheck
// (identityRecheckEvery) are fired each on its own.
type usageClock struct {
	mu   sync.Mutex
	reqs map[time.Duration]chan fakeTimer
}

func newUsageClock(srv *Server) *usageClock {
	c := &usageClock{reqs: map[time.Duration]chan fakeTimer{}}
	srv.extAfter = func(d time.Duration) <-chan time.Time {
		tm := fakeTimer{d: d, fire: make(chan time.Time, 1)}
		c.of(d) <- tm
		return tm.fire
	}
	return c
}

func (c *usageClock) of(d time.Duration) chan fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reqs[d] == nil {
		c.reqs[d] = make(chan fakeTimer, 1024)
	}
	return c.reqs[d]
}

// next is the next wait of d the server asks for.
func (c *usageClock) next(t *testing.T, d time.Duration) fakeTimer {
	t.Helper()
	select {
	case tm := <-c.of(d):
		return tm
	case <-time.After(testWait):
		t.Fatalf("the server asked for no wait of %s", d)
	}
	return fakeTimer{}
}

// tick fires the next period a connection waits for.
func (c *usageClock) tick(t *testing.T) {
	t.Helper()
	c.next(t, ext.UsageEvery).fire <- time.Now()
}

// tickAfter moves ut by d once a connection waits for its period, and
// then fires it: the period's report is read off the clock after the move,
// and the next move waits for the next period.
func (c *usageClock) tickAfter(t *testing.T, ut *usageTime, d time.Duration) {
	t.Helper()
	tm := c.next(t, ext.UsageEvery)
	ut.advance(d)
	tm.fire <- time.Now()
}

// usageTime is the desk's clock, moved by the test alone.
type usageTime struct{ at atomic.Int64 }

var usageEpoch = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func holdUsageTime(t *testing.T, srv *Server) *usageTime {
	t.Helper()
	if srv.exts.relayUsage == nil {
		t.Fatal("the server reports no usage")
	}
	ut := &usageTime{}
	now := func() time.Time { return usageEpoch.Add(time.Duration(ut.at.Load())) }
	srv.exts.relayUsage.now.Store(&now)
	return ut
}

func (ut *usageTime) advance(d time.Duration) time.Time {
	return usageEpoch.Add(time.Duration(ut.at.Add(int64(d))))
}

func (ut *usageTime) now() time.Time { return usageEpoch.Add(time.Duration(ut.at.Load())) }

// usageEnv is a switchyard admitting no open hosts (unless mutate says so)
// whose identity provider is o, its clock seam and its reports' clock the
// test's.
func usageEnv(t *testing.T, o *usageObserver, mutate func(*config.Config)) (*testEnv, *usageClock, *usageTime) {
	t.Helper()
	e := providerEnv(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
		if mutate != nil {
			mutate(c)
		}
	}, o)
	return e, newUsageClock(e.srv), holdUsageTime(t, e.srv)
}

// relayOut has h relay n bytes of output to viewer: what the relay counts
// for it, the whole message.
func relayOut(t *testing.T, h *fakeHost, viewer string, n int) int64 {
	t.Helper()
	env, err := proto.EncodeRelay(viewer, proto.Encode(proto.TypeOutput, bytes.Repeat([]byte("o"), n)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := h.c.Write(ctx, websocket.MessageBinary, env); err != nil {
		t.Fatalf("host write: %v", err)
	}
	return int64(len(env))
}

// readUpTo waits until h's connection has read what h sent before: a link
// asked for on it is answered after, minted or refused.
func readUpTo(h *fakeHost) {
	h.t.Helper()
	h.links++
	id := fmt.Sprintf("read-up-to-%d", h.links)
	h.send(proto.HostLinkMsg{T: proto.HostLink, RequestID: id, Role: "view"})
	for {
		if m := h.expect2(proto.HostLinkCreated, proto.HostError); m["requestId"] == id {
			return
		}
	}
}

// relayIn has v relay n bytes of input to its host and waits for the host
// to have them, once counted: what the relay counts for it, the payload.
func relayIn(t *testing.T, v *wsClient, tap *relayTap, id string, n int) int64 {
	t.Helper()
	before := tap.inputBytes(id)
	v.send(proto.Encode(proto.TypeInput, bytes.Repeat([]byte("i"), n)))
	if !waitFor(testWait, func() bool { return tap.inputBytes(id) >= before+n }) {
		t.Fatalf("the host has %d of %d input bytes", tap.inputBytes(id)-before, n)
	}
	return int64(n)
}

// relayedViewer joins a viewer to h's session through a control link and
// has it relay: the viewer, its id, and the tap of what h is relayed.
func relayedViewer(t *testing.T, e *testEnv, h *fakeHost) (*wsClient, string, *relayTap) {
	t.Helper()
	tok := h.link("control", "")
	tap := tapRelays(t, h)
	v := dialViewer(t, e, h.sessionID, tok)
	return v, joinRelayed(t, h, v), tap
}

// nobody is a viewer id of the envelope's length that no viewer has.
const nobody = "nobody-viewer-id"

func credHash(tok string) [32]byte { return sha256.Sum256([]byte(tok)) }

// checkReport fails unless u is a report of the connection conn ("" for
// any, which must then be a new one's id) under tok's hash with bytes.
func checkReport(t *testing.T, u ext.Usage, conn, tok string, sessions int, bytes int64) {
	t.Helper()
	if conn != "" && u.Connection != conn {
		t.Fatalf("connection %q, want %q", u.Connection, conn)
	}
	if len(u.Connection) != ext.UsageConnectionLen || strings.Trim(u.Connection, "0123456789abcdef") != "" {
		t.Fatalf("connection %q is not %d lower-case hex characters", u.Connection, ext.UsageConnectionLen)
	}
	if u.Credential != credHash(tok) {
		t.Fatalf("the report is not under %s's hash: %+v", tok, u)
	}
	if u.Subject != credHost.Subject || u.Sessions != sessions || u.RelayedBytes != bytes {
		t.Fatalf("report %+v, want subject %s, %d sessions, %d bytes", u, credHost.Subject, sessions, bytes)
	}
}

// TestRelayUsageReportedEveryPeriodAndAtClose: what a provider-admitted
// host connection relayed both ways is reported at each period and, at
// its close, in its Final: one connection id, the stretches tiling, the
// session's seconds adding up with the fraction carried.
func TestRelayUsageReportedEveryPeriodAndAtClose(t *testing.T) {
	o := newUsageObserver()
	e, clock, ut := usageEnv(t, o, nil)
	h, _ := dialCred(t, e, hostReg("1", nil), credOld)
	v, id, tap := relayedViewer(t, e, h)
	n := relayOut(t, h, id, 100) + relayOut(t, h, id, 3000)
	readUpTo(h)
	n += relayIn(t, v, tap, id, 7)
	ut.advance(60*time.Second + 500*time.Millisecond)
	clock.tick(t)
	first := o.next(t)
	checkReport(t, first, "", credOld, 1, n)
	if first.Final || !first.From.Equal(usageEpoch) || !first.To.Equal(ut.now()) || first.SessionSeconds != 60 {
		t.Fatalf("the period's report: %+v", first)
	}

	m := relayOut(t, h, id, 50)
	readUpTo(h)
	m += relayIn(t, v, tap, id, 11)
	ut.advance(29*time.Second + 500*time.Millisecond)
	h.c.CloseNow()
	last := o.next(t)
	checkReport(t, last, first.Connection, credOld, 1, m)
	if !last.Final || !last.From.Equal(first.To) || !last.To.Equal(ut.now()) || last.SessionSeconds != 30 {
		t.Fatalf("the Final: %+v (after %+v)", last, first)
	}
	// After the Final, the connection's next period reports nothing.
	clock.tick(t)
	hostAway(t, e, h.sessionID)
	o.none(t)
}

// TestRelayUsageOnePersonTwoCredentialsAtOnce: one subject on two
// credentials, two connections: two connection ids, each report under its
// own credential's hash with its own bytes.
func TestRelayUsageOnePersonTwoCredentialsAtOnce(t *testing.T) {
	o := newUsageObserver()
	e, clock, _ := usageEnv(t, o, nil)
	a, _ := dialCred(t, e, hostReg("a", nil), credOld)
	b, _ := dialCred(t, e, hostReg("b", nil), credNew)
	na := relayOut(t, a, nobody, 10)
	nb := relayOut(t, b, nobody, 20) + relayOut(t, b, nobody, 30)
	readUpTo(a)
	readUpTo(b)
	clock.tick(t)
	clock.tick(t)
	got := map[[32]byte]ext.Usage{}
	for range 2 {
		u := o.next(t)
		got[u.Credential] = u
	}
	ua, ub := got[credHash(credOld)], got[credHash(credNew)]
	checkReport(t, ua, "", credOld, 1, na)
	checkReport(t, ub, "", credNew, 1, nb)
	if ua.Connection == ub.Connection {
		t.Fatalf("two connections share the id %s", ua.Connection)
	}
}

// TestRelayUsageReconnectIsANewConnection: a host that drops and resumes
// its session is a new connection: the old one's Final with its bytes, a
// new id, nothing carried over.
func TestRelayUsageReconnectIsANewConnection(t *testing.T) {
	o := newUsageObserver()
	e, _, _ := usageEnv(t, o, nil)
	h, reg := dialCred(t, e, hostReg("1", nil), credOld)
	n := relayOut(t, h, nobody, 40)
	readUpTo(h)
	h.c.CloseNow()
	old := o.next(t)
	checkReport(t, old, "", credOld, 1, n)
	if !old.Final {
		t.Fatalf("the dropped connection's report is no Final: %+v", old)
	}
	hostAway(t, e, h.sessionID)
	again, back := dialCred(t, e, hostReg("1", &proto.HostResume{SessionID: h.sessionID, Secret: reg["secret"].(string)}), credOld)
	if back["resumed"] != true {
		t.Fatalf("not resumed: %v", back)
	}
	m := relayOut(t, again, nobody, 9)
	readUpTo(again)
	again.c.CloseNow()
	u := o.next(t)
	checkReport(t, u, "", credOld, 1, m)
	if !u.Final || u.Connection == old.Connection || u.From.Before(old.To) {
		t.Fatalf("the resumed connection's report %+v after %+v", u, old)
	}
}

// TestRelayUsageNoneForOwnTokensOpenHostsOrWithoutObserver: no report, no
// queue and no goroutine for a connection of the workbench token, a host
// token or an open host, nor for a link's viewer of one; none at all on a
// server whose identity provider is no observer, with no provider, or that
// is no switchyard.
func TestRelayUsageNoneForOwnTokensOpenHostsOrWithoutObserver(t *testing.T) {
	t.Run("the server's own tokens and an open host", func(t *testing.T) {
		o := newUsageObserver()
		e, _, _ := usageEnv(t, o, func(c *config.Config) { c.Switchyard.OpenHosts = true })
		var own []*fakeHost
		for i, tok := range []string{adminToken, "test-host-token", ""} {
			reg := hostReg(fmt.Sprintf("own-%d", i), nil)
			// Its own instance: one an open host shares mints no link with a token.
			reg.Host.Instance = fmt.Sprintf("inst-own-%d", i)
			h, _ := dialCred(t, e, reg, tok)
			own = append(own, h)
		}
		v, id, tap := relayedViewer(t, e, own[1])
		relayIn(t, v, tap, id, 9)
		for _, h := range own {
			relayOut(t, h, id, 10)
			readUpTo(h)
		}
		e.srv.exts.relayUsage.mu.RLock()
		counted := len(e.srv.exts.relayUsage.byHosted)
		e.srv.exts.relayUsage.mu.RUnlock()
		if counted != 0 {
			t.Fatalf("%d connections of the server's own counted", counted)
		}
		for _, h := range own {
			h.c.CloseNow()
			hostAway(t, e, h.sessionID)
		}
		// The queue is in order: the first report is the provider's.
		p, _ := dialCred(t, e, hostReg("provider", nil), credOld)
		p.c.CloseNow()
		if u := o.next(t); u.Credential != credHash(credOld) || !u.Final {
			t.Fatalf("the first report is not the provider-admitted connection's: %+v", u)
		}
		for _, tok := range []string{adminToken, "test-host-token"} {
			if o.timesAsked(tok) != 0 {
				t.Fatal("the provider was asked about a token of the server's own")
			}
		}
	})
	for name, env := range map[string]func(t *testing.T) *testEnv{
		"a provider that is no observer": func(t *testing.T) *testEnv {
			e, _ := credEnv(t, newCredIdentity())
			return e
		},
		"no provider": func(t *testing.T) *testEnv {
			return providerEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true }, nil)
		},
		"no switchyard": func(t *testing.T) *testEnv {
			return providerEnv(t, nil, newUsageObserver())
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := env(t)
			if e.srv.exts.relayUsage != nil {
				t.Fatal("the server reports usage")
			}
		})
	}
}

// TestRelayUsageADeskReportsNoSession: a run's desk admitted through the
// provider's principal reports its bytes, with no session and no seconds.
func TestRelayUsageADeskReportsNoSession(t *testing.T) {
	o := newUsageObserver()
	e, clock, ut := usageEnv(t, o, nil)
	d, _ := dialCred(t, e, deskReg("inst-1", "run-1"), credOld)
	n := relayOut(t, d, nobody, 12)
	// Answered after the frame is read, a link minted or refused alike.
	runLinkOver(d, "view", proto.RunGroup{ID: "run-1", Name: "Release", Desk: d.sessionID, Members: []proto.RunMember{{Name: "lead", AgentID: "cat", Status: "pending"}}})
	ut.advance(90 * time.Second)
	clock.tick(t)
	u := o.next(t)
	checkReport(t, u, "", credOld, 0, n)
	if u.SessionSeconds != 0 {
		t.Fatalf("a desk's report counts %d session seconds", u.SessionSeconds)
	}
}

// TestRelayUsageQueueFullDropsOldest: with the observer held, a
// connection's reports fill the queue: the oldest are dropped and
// counted, the newest kept, and the relay goes on meanwhile.
func TestRelayUsageQueueFullDropsOldest(t *testing.T) {
	o := newUsageObserver()
	gate := make(chan struct{})
	var release sync.Once
	o.block.Store(&gate)
	e, clock, ut := usageEnv(t, o, nil)
	// A failure before the release still lets the server's drain end.
	t.Cleanup(func() { o.block.Store(nil); release.Do(func() { close(gate) }) })
	h, _ := dialCred(t, e, hostReg("1", nil), credOld)
	v, id, _ := relayedViewer(t, e, h)
	clock.tickAfter(t, ut, ext.UsageEvery)
	receive(t, o.entered, "the observer's first call")
	const pushed = 300
	for range pushed {
		clock.tickAfter(t, ut, ext.UsageEvery)
	}
	clock.next(t, ext.UsageEvery) // the last period's report is queued
	queued, dropped := e.srv.exts.relayUsage.queue.stats()
	if queued != ext.UsageQueue || dropped != pushed-ext.UsageQueue {
		t.Fatalf("%d queued and %d dropped, want %d and %d", queued, dropped, ext.UsageQueue, pushed-ext.UsageQueue)
	}
	// The relay goes on while the observer is held.
	relayOut(t, h, id, 5)
	v.expectOutput("ooooo")
	o.block.Store(nil)
	release.Do(func() { close(gate) })
	if u := o.next(t); !u.To.Equal(usageEpoch.Add(ext.UsageEvery)) {
		t.Fatalf("the held report: %+v", u)
	}
	for i := pushed - ext.UsageQueue + 1; i <= pushed; i++ {
		if u := o.next(t); !u.To.Equal(usageEpoch.Add(time.Duration(i+1) * ext.UsageEvery)) {
			t.Fatalf("report %d of the kept ones ends at %s", i, u.To.Sub(usageEpoch))
		}
	}
	o.none(t)
}

// TestRelayUsageObserverPanicLeavesTheServerUp: a Used that panics is
// recovered: the server stays up, the next report is delivered, and the
// log holds no word of the panic.
func TestRelayUsageObserverPanicLeavesTheServerUp(t *testing.T) {
	o := newUsageObserver()
	o.panics.Store(1)
	logs := &syncLog{}
	e := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = true },
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), nil, withIdentity(o))
	clock := newUsageClock(e.srv)
	h, _ := dialCred(t, e, hostReg("1", nil), credOld)
	clock.tick(t) // panics
	n := relayOut(t, h, nobody, 10)
	readUpTo(h)
	clock.tick(t)
	checkReport(t, o.next(t), "", credOld, 1, n)
	if resp, _ := e.do(http.MethodGet, "/api/sessions", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the server answers %d after the observer's panic", resp.StatusCode)
	}
	log := logs.String()
	if strings.Contains(log, "the observer's own words") || strings.Contains(strings.ToLower(log), "panic") {
		t.Fatalf("the log holds a word of the panic: %s", log)
	}
}
