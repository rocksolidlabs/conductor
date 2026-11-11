package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// UplinkCredential is the one guarded door to an identity provider's
// OwnsHostToken and UplinkToken (ext.UplinkIdentity), for the server's
// uplink and for conductor host alike (docs/plans/identity-uplink.md). Its
// guard is the server's: each ask bounded by ext.IdentityWait, a panic
// recovered, the default on any failure, nothing remembered, failures
// logged at most once a minute by kind and count, never the provider's
// words or a token. Concurrent asks of one method share the call in
// flight, and an ask while that call is late answers the default at once.
// Safe for concurrent use.
type UplinkCredential struct {
	p                  ext.UplinkIdentity
	name               string
	server, configured string
	started            func(context.Context) bool
	log                *slog.Logger
	// after is the clock of the guard's waits and Present's deadline (a
	// test's own; nil is time.After).
	after func(time.Duration) <-chan time.Time

	owns, token flight

	mu     sync.Mutex
	logged map[string]time.Time
	failed map[string]int
}

// NewUplinkCredential is the credential the uplink to server presents for
// configured, the host token, through p, registered as name; started
// answers whether the provider has started, waiting for its first try
// (api.Server.ProviderStarted, StartIdentity's). Nil when p is no
// ext.UplinkIdentity: the configured token is presented, as before.
func NewUplinkCredential(p ext.IdentityProvider, name, server, configured string, started func(context.Context) bool, log *slog.Logger) *UplinkCredential {
	u, ok := p.(ext.UplinkIdentity)
	if !ok {
		return nil
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &UplinkCredential{p: u, name: name, server: server, configured: configured, started: started, log: log}
}

// presentWait bounds Present, whatever its caller's context: the first
// try of Start, then the two asks.
const presentWait = ext.StartWait + 2*ext.IdentityWait

// Present is what a registration presents (docs/plans/identity-uplink.md,
// the table under Events): UplinkToken's token when it answers in time one
// ext.ValidHostToken takes and, for a configured token the provider owns or
// may own (OwnsHostToken true, late, panicking or busy), other than it;
// else the configured token when the provider does not own it; else
// nothing, withheld. The provider is asked for a token only once it has
// started. It returns within presentWait.
func (c *UplinkCredential) Present(ctx context.Context) (token string, withheld bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.wait(presentWait):
			cancel()
		case <-ctx.Done():
		}
	}()
	owned := c.owned(ctx)
	if tok, ok := c.renewed(ctx, owned); ok {
		return tok, false
	}
	if owned {
		return "", true
	}
	return c.configured, false
}

// Renewed is UplinkToken's token when the provider has started and answers
// in time one ext.ValidHostToken takes, never the configured token while
// it may be the provider's own.
func (c *UplinkCredential) Renewed(ctx context.Context) (string, bool) {
	return c.renewed(ctx, c.owned(ctx))
}

// owned is OwnsHostToken's answer, true when it could not be had.
func (c *UplinkCredential) owned(ctx context.Context) bool {
	v, ok := c.ask(ctx, &c.owns, "owns", func() credAnswer { return credAnswer{owned: c.p.OwnsHostToken(c.configured)} })
	return !ok || v.owned
}

func (c *UplinkCredential) renewed(ctx context.Context, owned bool) (string, bool) {
	if c.started != nil && !c.started(ctx) {
		return "", false
	}
	v, ok := c.ask(ctx, &c.token, "token", func() credAnswer {
		tok, ok := c.p.UplinkToken(c.server, c.configured)
		return credAnswer{token: tok, ok: ok}
	})
	if !ok || !v.ok || !ext.ValidHostToken(v.token) {
		return "", false
	}
	if owned && share.Equal(v.token, c.configured) {
		c.fail("token", "the configured token answered back")
		return "", false
	}
	return v.token, true
}

func (c *UplinkCredential) wait(d time.Duration) <-chan time.Time {
	if c.after != nil {
		return c.after(d)
	}
	return time.After(d)
}

// credAnswer is a provider's answer to one of the two asks.
type credAnswer struct {
	owned bool
	token string
	ok    bool
}

// flight is the call of one method in flight: shared by the asks that
// come while it runs, and answered at once (ok false) by those that come
// once it is late, until it returns.
type flight struct {
	mu  sync.Mutex
	cur *credCall
}

type credCall struct {
	done chan struct{} // closed once answered, or late
	v    credAnswer
	ok   bool
}

// ask asks fn through the guard, sharing the call in flight; ok false is
// the default: the call failed, was late or busy, or ctx ended first.
func (c *UplinkCredential) ask(ctx context.Context, f *flight, kind string, fn func() credAnswer) (credAnswer, bool) {
	f.mu.Lock()
	cl := f.cur
	if cl == nil {
		cl = &credCall{done: make(chan struct{})}
		f.cur = cl
		go c.run(f, cl, kind, fn)
	} else {
		select {
		case <-cl.done:
			// Late and not yet returned: busy.
			f.mu.Unlock()
			c.fail(kind, "busy")
			return credAnswer{}, false
		default:
		}
	}
	f.mu.Unlock()
	select {
	case <-cl.done:
		return cl.v, cl.ok
	case <-ctx.Done():
		return credAnswer{}, false
	}
}

// run makes cl's call of fn, within ext.IdentityWait.
func (c *UplinkCredential) run(f *flight, cl *credCall, kind string, fn func() credAnswer) {
	type result struct {
		v        credAnswer
		panicked bool
	}
	res := make(chan result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				res <- result{panicked: true}
			}
		}()
		res <- result{v: fn()}
	}()
	finish := func() {
		f.mu.Lock()
		f.cur = nil
		f.mu.Unlock()
	}
	select {
	case r := <-res:
		if r.panicked {
			c.fail(kind, "panic")
		}
		cl.v, cl.ok = r.v, !r.panicked
		finish()
		close(cl.done)
	case <-c.wait(ext.IdentityWait):
		c.fail(kind, "late")
		close(cl.done)
		<-res
		finish()
	}
}

// firstCredentialWait bounds how long conductor host's first registration,
// refused 401 for a withheld credential, is tried again while its provider
// signs in (Run).
const firstCredentialWait = 2 * time.Minute

// uplinkTokenEvery is how often a connection asks its provider for a
// replacement credential (renewToken).
const uplinkTokenEvery = 30 * time.Second

// withheldRefusal says err is a registration refused 401 that presented
// nothing for a token the provider owns.
func withheldRefusal(err error) bool {
	var r *RefusedError
	return errors.As(err, &r) && r.Withheld && r.Status == http.StatusUnauthorized
}

// registerWhileWithheld tries the first registration again, refused with
// err, with backoff (1 s doubling to ReconnectMax), each try asking
// Present anew, for at most firstCredentialWait; past it, or once a try
// is refused otherwise, it answers that try's refusal.
func (a *agent) registerWhileWithheld(ctx context.Context, err error) (*websocket.Conn, proto.Registered, error) {
	giveUp := a.afterOr(firstCredentialWait)
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return nil, proto.Registered{}, err
		case <-giveUp:
			return nil, proto.Registered{}, err
		case <-a.afterOr(backoff):
		}
		backoff = min(2*backoff, a.opts.ReconnectMax)
		c, reg, again := a.dialAndRegister(ctx)
		if !withheldRefusal(again) {
			return c, reg, again
		}
		err = again
	}
}

func (a *agent) afterOr(d time.Duration) <-chan time.Time {
	if a.opts.after != nil {
		return a.opts.after(d)
	}
	return time.After(d)
}

// renewStep is what one step of renewToken did.
type renewStep string

const (
	renewSent      renewStep = "sent"      // host_token sent, and taken as the connection's
	renewUnchanged renewStep = "unchanged" // the same credential again: nothing sent
	renewNone      renewStep = "none"      // nothing to send here
	renewClosed    renewStep = "closed"    // the connection closed 1000 for its reconnect
	renewEnded     renewStep = "ended"     // the connection ended first: nothing done
)

// renewToken asks the provider every uplinkTokenEvery for the credential
// the connection of w should hold, on w's context, which ends as the
// connection is lost, so neither a tick nor a late answer outlives it. On
// a connection whose registered said renews, a changed credential is sent
// as host_token in the host's own lane, and becomes the connection's once
// the send has returned success; a send withdrawn or failed closes the
// connection 1000 and the reconnect presents it: nothing is retried in
// place. On an open host's connection (it presented nothing) a credential
// closes it 1000, and the reconnect presents it. Elsewhere nothing is
// sent: the next reconnect presents what Present answers then. Ending a
// credential is the switchyard's recheck's to do.
func (a *agent) renewToken(w *controlWriter) {
	cred := a.opts.Credential
	if cred == nil {
		return
	}
	step := func(s renewStep) {
		if a.opts.renewed != nil {
			a.opts.renewed(s)
		}
	}
	closeFor := func(why string) {
		w.c.Close(websocket.StatusNormalClosure, why)
		step(renewClosed)
	}
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-a.afterOr(uplinkTokenEvery):
		}
		tok, ok := cred.Renewed(w.ctx)
		switch {
		case w.ctx.Err() != nil:
			step(renewEnded)
			return
		case !ok:
			step(renewNone)
		case w.presented == "":
			closeFor("credential ready")
			return
		case !w.renews:
			step(renewNone)
		case tok == w.presented:
			step(renewUnchanged)
		default:
			b, _ := json.Marshal(proto.HostTokenMsg{T: proto.HostTokenType, Token: tok})
			if err := w.sendControl(websocket.MessageText, b); err != nil {
				closeFor("renewal not sent")
				return
			}
			w.presented = tok
			step(renewSent)
		}
	}
}

// fail logs a failed ask of kind, at most once a minute with how many
// failed since: never the provider's words, nor a token.
func (c *UplinkCredential) fail(kind, why string) {
	c.mu.Lock()
	if c.logged == nil {
		c.logged, c.failed = map[string]time.Time{}, map[string]int{}
	}
	c.failed[kind]++
	now := time.Now()
	if last, ok := c.logged[kind]; ok && now.Sub(last) < time.Minute {
		c.mu.Unlock()
		return
	}
	n := c.failed[kind]
	c.failed[kind], c.logged[kind] = 0, now
	c.mu.Unlock()
	c.log.Warn("identity provider's uplink credential failed: its default answered", "provider", c.name, "ask", kind, "failure", why, "failures", n)
}
