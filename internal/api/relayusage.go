package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// The relay's usage, told to an identity provider that is an
// ext.UsageObserver: on a switchyard, each host connection admitted
// through the provider's principal (hostConnState.cred) keeps what the
// relay counted for it, and reports it every ext.UsageEvery, at each
// change of its credential and once at its close, through one queue of
// ext.UsageQueue that never holds the relay (extQueue: a full one drops
// its oldest). The counts are the relay's own, taken at the two places the
// switchyard's hourly figure takes them (relayMeter's add): what a host
// relays to its viewers, and what its viewers relay to it. Only the
// credential's SHA-256 crosses to the provider; the credential is in no
// report and no log line. Nothing here is stored, and nothing gates
// anything: no call is made, no queue made and no goroutine started on a
// server that is no switchyard, or whose identity provider is no
// observer, and none for a connection of the server's own tokens or an
// open host.

// kindIdentityRelayUsage is the guard's kind of an identity provider's
// usage reports: logged, as every identity kind is, by its name and count
// alone, never with the provider's words (extFailed).
const kindIdentityRelayUsage = ext.KindIdentity + "-relay-usage"

// relayUsageRenewals is how many renewals to another credential a
// connection has admitted inline (hostToken) in one period before its
// next is kept pending for the recheck (principalWatch), which adopts it:
// so a host that floods renewals cuts at most this many reports a period
// inline, and two more a minute at the recheck, and the reports of other
// connections are not pushed out of the queue by it. No renewal is
// refused and no connection closed for its pace.
const relayUsageRenewals = 2

// relayUsageDesk is the server's usage reports: the queue to the observer
// and the live connections by the hosted session they carry, which the
// viewers' relay counts against (add). Nil on a server that reports
// nothing.
type relayUsageDesk struct {
	queue *extQueue[ext.Usage]
	// now is the clock of the reports' times: time.Now, unless a test
	// holds its own.
	now atomic.Pointer[func() time.Time]

	mu       sync.RWMutex
	byHosted map[string]*relayUsageConn
}

// newRelayUsageDesk is the desk of a server whose identity provider,
// registered as name, is obs: its queue held as that provider's other
// queues are (holdQueue), and delivered through extDeliver, which bounds
// and recovers each call and logs a failure by the provider's name alone.
func (s *Server) newRelayUsageDesk(name string, obs ext.UsageObserver) *relayUsageDesk {
	d := &relayUsageDesk{byHosted: map[string]*relayUsageConn{}}
	d.queue = newExtQueue(s, kindIdentityRelayUsage, name, ext.UsageQueue, func(_ context.Context, u ext.Usage) error {
		obs.Used(u)
		return nil
	})
	s.holdQueue(obs, name, d.queue.begin)
	return d
}

// relayUsageConn is one host connection's counts since its last report:
// the bytes counted as they pass (atomic, so a viewer's frame and a cut
// never wait on each other, and each frame lands on exactly one side of a
// cut), and, under mu, the credential's hash, the report's start, the
// session's seconds carried, and how many renewals were admitted inline
// this period.
type relayUsageConn struct {
	desk     *relayUsageDesk
	id       string
	hosted   string
	subject  string
	sessions int

	bytes atomic.Int64

	mu       sync.Mutex
	cred     share.Hash
	from     time.Time
	carry    time.Duration
	renewals int
	closed   bool
}

// clock is the time of a report's edge.
func (d *relayUsageDesk) clock() time.Time {
	if f := d.now.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}

// open starts the counts of a host connection admitted through the
// provider's principal with tok, carrying the hosted session hosted (a
// desk's when desk): from now on, the viewers' frames to hosted count
// against it, in place of a connection that carried it before. Nil for a
// nil desk.
func (d *relayUsageDesk) open(hosted, subject, tok string, desk bool) *relayUsageConn {
	if d == nil {
		return nil
	}
	var b [ext.UsageConnectionLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("relay usage: crypto/rand unavailable: " + err.Error())
	}
	c := &relayUsageConn{desk: d, id: hex.EncodeToString(b[:]), hosted: hosted, subject: subject, sessions: 1, cred: share.HashToken(tok), from: d.clock()}
	if desk {
		c.sessions = 0
	}
	d.mu.Lock()
	d.byHosted[hosted] = c
	d.mu.Unlock()
	return c
}

// add counts n bytes a viewer relayed to the host of the hosted session
// hosted, against the connection that carries it now; none when its host
// is away or reports nothing. The look-up and the count are made under
// the desk's lock, so a frame counted against a connection is counted
// before the connection's close takes it out (close), and so in its Final.
func (d *relayUsageDesk) add(hosted string, n int) {
	if d == nil {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if c := d.byHosted[hosted]; c != nil {
		c.bytes.Add(int64(n))
	}
}

// add counts n bytes the host relayed to its viewers.
func (c *relayUsageConn) add(n int) {
	if c != nil {
		c.bytes.Add(int64(n))
	}
}

// cut queues the report of the stretch up to now and starts the next;
// the caller holds c.mu. Nothing once the connection's Final is queued.
func (c *relayUsageConn) cut(final bool) {
	if c.closed {
		return
	}
	to := c.desk.clock()
	if to.Before(c.from) {
		to = c.from
	}
	u := ext.Usage{Connection: c.id, Credential: c.cred, Subject: c.subject, Sessions: c.sessions,
		RelayedBytes: c.bytes.Swap(0), Final: final, From: c.from, To: to}
	if c.sessions > 0 {
		span := to.Sub(c.from) + c.carry
		u.SessionSeconds, c.carry = int64(span/time.Second), span%time.Second
	}
	c.from, c.closed = to, final
	// Queued under c.mu, so the connection's reports are queued in the
	// order they were cut; push never waits on the provider.
	c.desk.queue.push(u)
}

// period queues the report of the period that ended.
func (c *relayUsageConn) period() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renewals = 0
	c.cut(false)
}

// renewed is a renewal to tok, another credential, about to take effect:
// the stretch up to it is reported under the old credential's hash, and
// the counts start again under tok's. inline is a renewal hostToken
// admitted, counted against relayUsageRenewals; the recheck's adoption of
// a pending one is not.
func (c *relayUsageConn) renewed(tok string, inline bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut(false)
	c.cred = share.HashToken(tok)
	if inline {
		c.renewals++
	}
}

// paced says the connection has admitted relayUsageRenewals renewals to
// another credential inline this period: its next is kept pending.
func (c *relayUsageConn) paced() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.renewals >= relayUsageRenewals
}

// close takes the connection out of the desk, unless another connection
// carries its hosted session now, and queues its Final with what it
// counted: after it, no viewer's frame counts against it.
func (c *relayUsageConn) close() {
	if c == nil {
		return
	}
	c.desk.mu.Lock()
	if c.desk.byHosted[c.hosted] == c {
		delete(c.desk.byHosted, c.hosted)
	}
	c.desk.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut(true)
}

// watchRelayUsage queues the connection's report every ext.UsageEvery, on the
// server's clock seam (extAfterOr), until ctx, the connection's, ends.
func (s *Server) watchRelayUsage(ctx context.Context, c *relayUsageConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.extAfterOr(ext.UsageEvery):
		}
		if ctx.Err() != nil {
			return
		}
		c.period()
	}
}
