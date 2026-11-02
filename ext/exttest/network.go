package exttest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Network is the transport a provider under test reaches its service
// through: build the provider with DialContext or Transport in place of
// its own, and hand the check functions the same Network. It records every
// dial and request with the context it was given, and it is how the suite
// checks article 8 without a clock:
//
//   - A dial or request under the context of one of the suite's calls (a
//     call on a request's path: every method but Start's goroutines and
//     Restore) is refused with ErrOnCallPath and fails the check, however
//     fast it would have answered.
//   - While the suite holds the network blocked (Block), every dial and
//     request waits until it is let go (Unblock) or its context ends, and
//     no byte crosses a connection DialContext made, however long before
//     it was made: a Read or Write on it waits until Unblock or the
//     connection's Close (a deadline does not let it go). So a call that
//     waits on its service under a context of its own, or over a
//     connection it holds (gRPC, a websocket), does not return, and is
//     caught when the guard's wait for that method ends.
//
// DialContext gates each new connection and every byte on it; Transport
// gates every request, pooled connections or not, so route HTTP through
// Transport. A connection carries no context, so traffic on one held from
// before a call is seen only while the network is blocked, never under
// the call's mark. A Network is safe for concurrent use; the zero value is
// not ready, use NewNetwork.
type Network struct {
	dialer net.Dialer
	inner  *http.Transport

	mu       sync.Mutex
	shut     chan struct{} // non-nil while blocked; closed by Unblock
	dials    []Dial        // the last MaxRecorded
	dropped  int
	breaches []Dial
	waiting  map[string]int           // the dials, requests and connections' reads and writes waiting on the gate, by their wait label
	watch    map[string]chan struct{} // closed once one with that label waits
	quiet    chan struct{}            // closed once nothing waits on the gate (settled)
	held     int                      // how many wait on the gate now, every label
}

// MaxRecorded bounds the dials and requests a Network keeps for Dials: past
// it, the oldest go. The refusals on a call's path, which fail a check, are
// never dropped.
const MaxRecorded = 4096

// ErrOnCallPath is a dial's or a request's error when it was made under the
// context of a call of the suite's: the call reached for its service on its
// own path, which the check reports.
var ErrOnCallPath = errors.New("exttest: no dial or request on a call's path (article 8: a call answers from what the provider holds)")

// Dial is one dial or request a Network was asked for.
type Dial struct {
	Ctx     context.Context // the context it was made under
	Network string          // a dial's network ("tcp", …); "" for a request
	Addr    string          // the address dialled, or the request's host
	Method  string          // a request's method; "" for a dial
	URL     string          // a request's scheme, host and path, never its user information, query or fragment (a token may ride there); "" for a dial
	Call    string          // the suite's call whose context it was made under; "" for none
}

// shownURL is u as a Dial records it: its scheme, host and path only, so
// that no token a provider sends in a query string or as a URL's user
// information reaches a check's words or the test's log.
func shownURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// what says what d was, for a check's words.
func (d Dial) what() string {
	if d.Method != "" {
		return "sent " + d.Method + " " + d.URL
	}
	return "dialled " + d.Network + " " + d.Addr
}

// NewNetwork is an open Network: dials and requests go through to the
// addresses they name (an httptest server, the provider's service) until
// Block.
func NewNetwork() *Network {
	n := &Network{waiting: map[string]int{}, watch: map[string]chan struct{}{}}
	n.inner = &http.Transport{
		Proxy:             nil, // a test's network is what it names, never the environment's proxy
		DialContext:       n.dialer.DialContext,
		ForceAttemptHTTP2: true,
	}
	return n
}

// DialContext dials addr as net.Dialer does, once the network lets it: it
// refuses a dial under a call's context (ErrOnCallPath) and, while the
// network is blocked, waits until it is let go or ctx ends (ctx's error).
// The connection it answers lets no byte cross while the network is
// blocked: a Read or Write waits until Unblock or Close (net.ErrClosed),
// and bytes a Read took from the wire are handed over only once the gate
// opens.
func (n *Network) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := n.pass(ctx, Dial{Network: network, Addr: addr}); err != nil {
		return nil, err
	}
	conn, err := n.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	label, _ := ctx.Value(waitKey{}).(string)
	return &heldConn{Conn: conn, n: n, label: label, closed: make(chan struct{})}, nil
}

// heldConn is a connection DialContext made, gated as the network is.
type heldConn struct {
	net.Conn
	n      *Network
	label  string // the wait label of the context it was dialled under
	once   sync.Once
	closed chan struct{} // closed by Close
}

// gate waits while the network is blocked, until it opens (nil) or the
// connection is closed (net.ErrClosed).
func (c *heldConn) gate() error {
	if !c.n.await(c.label, c.closed) {
		return net.ErrClosed
	}
	return nil
}

func (c *heldConn) Read(p []byte) (int, error) {
	if err := c.gate(); err != nil {
		return 0, err
	}
	k, err := c.Conn.Read(p)
	if gerr := c.gate(); gerr != nil { // the network blocked while the Read waited on the wire
		return 0, gerr
	}
	return k, err
}

func (c *heldConn) Write(p []byte) (int, error) {
	if err := c.gate(); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *heldConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// Transport is an http.RoundTripper that lets each request through as
// DialContext lets a dial (refused on a call's path, waiting while the
// network is blocked), then sends it on connections of its own.
func (n *Network) Transport() http.RoundTripper { return roundTripper{n} }

type roundTripper struct{ n *Network }

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := r.n.pass(req.Context(), Dial{Addr: req.URL.Host, Method: req.Method, URL: shownURL(req.URL)}); err != nil {
		if req.Body != nil {
			req.Body.Close() // a RoundTripper closes the body, even on an error
		}
		return nil, err
	}
	return r.n.inner.RoundTrip(req)
}

// CloseIdleConnections closes the transport's idle connections, as
// http.Client.CloseIdleConnections asks of it.
func (r roundTripper) CloseIdleConnections() { r.n.inner.CloseIdleConnections() }

// Block shuts the network's gate: from now on every dial and request waits
// until Unblock or its context's end. Blocking a blocked network does
// nothing; a nil Network has no gate.
func (n *Network) Block() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.shut == nil {
		n.shut = make(chan struct{})
	}
}

// Unblock opens the gate and lets every waiting dial and request go.
func (n *Network) Unblock() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.shut != nil {
		close(n.shut)
		n.shut = nil
	}
}

// Dials answers the dials and requests made so far, the oldest first (the
// last MaxRecorded of them), and how many older ones were let go.
func (n *Network) Dials() (dials []Dial, dropped int) {
	if n == nil {
		return nil, 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Dial(nil), n.dials...), n.dropped
}

// The context keys the suite marks its contexts with.
type (
	callKey struct{} // a call on a request's path: its label
	waitKey struct{} // a call that may use the network (Restore): its label
)

// onCall is ctx marked as the suite's call label.
func onCall(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, callKey{}, label)
}

// mayWait is ctx labelled, so that the network can tell the suite when a
// dial under it waits on the gate.
func mayWait(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, waitKey{}, label)
}

// pass records d under ctx and answers whether it may go: an error under a
// call's context, or once ctx ends while it waits on a shut gate.
func (n *Network) pass(ctx context.Context, d Dial) error {
	d.Ctx = ctx
	d.Call, _ = ctx.Value(callKey{}).(string)
	label, _ := ctx.Value(waitKey{}).(string)
	n.mu.Lock()
	if len(n.dials) == MaxRecorded {
		n.dials = append(n.dials[:0], n.dials[1:]...)
		n.dropped++
	}
	n.dials = append(n.dials, d)
	if d.Call != "" {
		n.breaches = append(n.breaches, d)
		n.mu.Unlock()
		return fmt.Errorf("%w (%s)", ErrOnCallPath, d.Call)
	}
	n.mu.Unlock()
	if !n.await(label, ctx.Done()) {
		return ctx.Err()
	}
	return nil
}

// await waits, counted under label, while the gate is shut: true once it
// opens (at once when it is open), false once gone closes first.
func (n *Network) await(label string, gone <-chan struct{}) bool {
	n.mu.Lock()
	shut := n.shut
	if shut == nil {
		n.mu.Unlock()
		return true
	}
	n.waiting[label]++
	n.held++
	if ch, ok := n.watch[label]; ok {
		close(ch)
		delete(n.watch, label)
	}
	n.mu.Unlock()
	opened := true
	select {
	case <-shut:
	case <-gone:
		opened = false
	}
	n.mu.Lock()
	n.waiting[label]--
	n.held--
	if n.held == 0 && n.quiet != nil {
		close(n.quiet)
		n.quiet = nil
	}
	n.mu.Unlock()
	return opened
}

// settled is closed once nothing waits on the gate: at once if nothing
// does. A nil Network is always settled.
func (n *Network) settled() <-chan struct{} {
	if n == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.held == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if n.quiet == nil {
		n.quiet = make(chan struct{})
	}
	return n.quiet
}

// whenWaiting is closed once a dial or request made under a context
// labelled label (mayWait) waits on the shut gate; at once if one does.
func (n *Network) whenWaiting(label string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.waiting[label] > 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if ch, ok := n.watch[label]; ok {
		return ch
	}
	ch := make(chan struct{})
	n.watch[label] = ch
	return ch
}

// takeBreaches answers, and forgets, the refusals made under a call whose
// label begins with prefix.
func (n *Network) takeBreaches(prefix string) []Dial {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var out, kept []Dial
	for _, d := range n.breaches {
		if strings.HasPrefix(d.Call, prefix) {
			out = append(out, d)
		} else {
			kept = append(kept, d)
		}
	}
	n.breaches = kept
	return out
}
