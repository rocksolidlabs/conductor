package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// handleHostWS accepts a `conductor host` control connection.
func (s *Server) handleHostWS(w http.ResponseWriter, r *http.Request) {
	// An open host presents no token at all, on a switchyard that admits
	// them, and lives under the per-address limits; a wrong token is refused
	// as ever (a typo must show), a right one is trusted and unlimited. A
	// query that cannot be read presents no token, so it is refused here,
	// never read as an open host's: its token may be a wrong one.
	if r.Header.Get("Authorization") == "" {
		if _, err := parseQuery(r.URL.RawQuery); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	tok := presentedToken(r, true)
	open := tok == "" && s.cfg.Switchyard.Enabled && s.cfg.Switchyard.OpenHosts
	if !open && !s.hostAllowed(r, tok) {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "host token required")
		return
	}
	addr := clientKey(r)
	if open {
		if !s.hostLimiter.allow(addr) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many registrations from this address; ask the operator for a host token")
			return
		}
		if !s.openHosts.acquire(addr, s.cfg.Switchyard.OpenHostSessions) {
			writeError(w, http.StatusTooManyRequests, "open_host_limit", "this address holds as many open sessions as this switchyard allows; ask the operator for a host token")
			return
		}
		defer s.openHosts.release(addr)
	}
	c, err := websocket.Accept(w, r, s.acceptOptions())
	if err != nil {
		return
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Registration must arrive first.
	rctx, rcancel := context.WithTimeout(ctx, helloTimeout)
	typ, data, err := c.Read(rctx)
	rcancel()
	if err != nil || typ != websocket.MessageText {
		c.Close(proto.CloseProtocolError, "register expected")
		return
	}
	var reg proto.Register
	if json.Unmarshal(data, &reg) != nil || reg.T != proto.HostRegister {
		c.Close(proto.CloseProtocolError, "register expected")
		return
	}
	if reg.Session.RelayOnly && s.cfg.Switchyard.Enabled && !s.cfg.SwitchyardRelay() {
		c.Close(proto.CloseProtocolError, "relay_off: this switchyard does not relay")
		return
	}
	conn := signal.NewHostConn()
	if s.viewerSeam != nil && s.viewerSeam.waited != nil {
		conn.OnWait = s.viewerSeam.waited
	}
	hs, resumed, err := s.hosts.Register(reg, conn)
	if err != nil {
		msg := "registration rejected"
		code := websocket.StatusCode(proto.CloseProtocolError)
		if errors.Is(err, session.ErrTooManySessions) {
			msg, code = "session limit reached", proto.CloseTooManyViewers
		} else if errors.Is(err, signal.ErrBadResume) {
			msg, code = "cannot resume session", proto.CloseNotFound
		}
		c.Close(code, msg)
		return
	}
	info := hs.Info()
	hc := &hostConnState{base: s.publicBase(r), ctx: ctx, relays: !s.cfg.Switchyard.Enabled || s.cfg.SwitchyardRelay()}
	if open {
		hc.addr = addr
	}
	if s.cfg.Switchyard.Enabled {
		switch {
		case open && s.cfg.Switchyard.OpenHostRelayKBps > 0:
			// One bucket for every open host of the address: the bound is the address's, not each connection's.
			rate := s.cfg.Switchyard.OpenHostRelayKBps * 1024
			hc.relay = s.openRelay.acquire(addr, rate, s.relayBurst(rate))
			defer s.openRelay.release(addr)
		case s.cfg.Switchyard.RelayKBps > 0:
			rate := s.cfg.Switchyard.RelayKBps * 1024
			hc.relay = newByteBucket(rate, s.relayBurst(rate))
		}
	}
	s.log.Info("host registered", "session", info.ID, "host", info.HostName, "resumed", resumed, "open", open)
	defer func() {
		hs.HostDisconnected(conn)
		s.log.Info("host disconnected", "session", info.ID)
	}()
	// The host's kept links wait for it while it is connected, from before
	// it is told which the switchyard holds; its time away starts as this
	// connection ends (before the session shows it gone).
	defer s.hostUp(hs.Owner())()

	ice := make([]proto.ICEServer, 0, len(s.cfg.ICEServers))
	for _, srv := range s.cfg.ICEServers {
		ice = append(ice, proto.ICEServer{URLs: srv.URLs, Username: srv.Username, Credential: srv.Credential})
	}
	registered, _ := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: info.ID, Secret: hs.Secret(), ShareBaseURL: s.publicBase(r), Resumed: resumed, ICEServers: ice, Links: s.liveLinkIDs(info.ID)})
	if err := writeText(ctx, c, registered); err != nil {
		return
	}

	// Writer goroutine: the server's own messages first, then the viewers'
	// (signal.HostConn.Next).
	go func() {
		for {
			if s.viewerSeam != nil && s.viewerSeam.taking != nil {
				s.viewerSeam.taking(ctx)
			}
			o, ok := conn.Next(ctx)
			if !ok {
				select {
				case <-conn.Done():
					c.Close(websocket.StatusPolicyViolation, "connection closed")
					cancel()
				default:
				}
				return
			}
			var err error
			if o.Text != nil {
				err = writeText(ctx, c, o.Text)
			} else {
				wctx, wc := context.WithTimeout(ctx, writeTimeout)
				err = c.Write(wctx, websocket.MessageBinary, o.Binary)
				wc()
			}
			if err != nil {
				conn.Close()
				cancel()
				return
			}
		}
	}()
	go hostKeepalive(ctx, c, s.pingEvery, s.hostPingEvery, hs.LinkStands)

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		// A switchyard's bound on what the host sends: its relayed output,
		// and its messages too, an answer or a candidate for a viewer among
		// them. The host waits for it.
		if hc.relay != nil {
			if err := hc.relay.take(ctx, len(data)); err != nil {
				return
			}
		}
		if typ == websocket.MessageBinary {
			f, err := proto.Decode(data)
			if err != nil || f.Type != proto.TypeRelay {
				c.Close(proto.CloseProtocolError, "relay frame expected")
				return
			}
			viewerID, inner, err := proto.DecodeRelay(f.Payload)
			if err != nil {
				c.Close(proto.CloseProtocolError, "bad relay envelope")
				return
			}
			s.relayed.add(len(data))
			if inner.Type == proto.TypeInput {
				continue // hosts never send input to viewers
			}
			hs.HostRelayFrame(viewerID, inner)
			continue
		}
		if !s.handleHostMessage(hs, hc, data) {
			c.Close(proto.CloseProtocolError, "bad host message")
			return
		}
	}
}

// hostKeepalive pings a host's control connection every `every` until ctx
// ends, and every oftenEvery while often says so: while a link a viewer
// joined the session through still stands (HostedSession.LinkStands). An
// older host types a line of a viewer who joined through a link, also one
// that has left with its line under way, only while it has heard from the
// server lately, and a ping of the server's reaches it whatever the server
// reads; a newer host vouches for the server on its own pings answered
// alone (docs/protocol.md, the host control connection), which the server
// answers as it reads the connection, behind what the host sent before
// them. A pong that does not come in time ends nothing, for the reader
// that takes it may be held so (a relay bound pauses the reading, the
// host's own or the one it shares with the other open hosts of its
// address), and the pings go on; one that cannot be written ends them, as
// a write's failure ends the connection.
func hostKeepalive(ctx context.Context, c *websocket.Conn, every, oftenEvery time.Duration, often func() bool) {
	if every <= 0 {
		every = defaultPingEvery // a Server made without New
	}
	if oftenEvery <= 0 {
		oftenEvery = defaultHostPingEvery
	}
	tick := min(every, oftenEvery)
	t := time.NewTicker(tick)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(last) < every-tick/2 && !often() {
			continue
		}
		last = time.Now()
		// The library gives a ping's write 5 s at most, and closes the
		// connection when that runs out; this bounds the wait for the pong.
		pctx, cancel := context.WithTimeout(ctx, min(writeTimeout, tick))
		err := c.Ping(pctx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return
		}
	}
}

func writeText(ctx context.Context, c *websocket.Conn, b []byte) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}

// hostConnState is what one host connection keeps between its messages:
// the base its links take, when it last asked for links, and the bucket
// what it sends drains (nil without a bound).
type hostConnState struct {
	base string
	// addr is an open host's address, for the bound on the links it keeps; "" for a tokened host.
	addr      string
	linkTimes []time.Time
	// updateTimes bounds link_run_update (proto.RunLinkUpdatesPerMinute).
	updateTimes []time.Time
	relay       *byteBucket
	// ctx is the connection's: a wait on relay ends with it.
	ctx context.Context
	// relays says the server relays a viewer whose WebRTC attempt fails
	// (every server but a switchyard with switchyard.relay false), for what
	// a viewer is left with when a frame for it is refused (forwardSignal).
	relays bool
}

// chargeForward charges, before a viewer is sent it, what a host's message
// brings the viewer (n bytes) past what it was charged as read: a message
// encoded anew can be larger than the host sent it. False when the
// connection ends meanwhile, and the viewer is sent nothing.
func (hc *hostConnState) chargeForward(n, read int) bool {
	if hc.relay == nil || n <= read {
		return true
	}
	return hc.relay.take(hc.ctx, n-read) == nil
}

// forwardSignal queues for a viewer the SIGNAL frame made of a host's
// message of read bytes (f and err from signal.AnswerFrame or ICEFrame),
// charged first (chargeForward). One the server could not make within what
// a SIGNAL frame holds (proto.ErrTooLarge: the host's message encoded anew
// passed the limit a reader checks) is never sent; the viewer is sent a
// CONTROL error in its place (signal.RefusedMessage), charged the same.
// Its WebRTC attempt cannot succeed without the answer: where the viewer
// can fall back to the relay it is kept, its timer sending it there; on a
// switchyard that does not relay it is closed with the error, since the
// attempt has nowhere to go. The host's connection stays either way: its
// message was within its own bound.
func (hc *hostConnState) forwardSignal(hs *signal.HostedSession, viewerID, what string, read int, f []byte, err error) {
	if errors.Is(err, proto.ErrTooLarge) {
		msg := signal.RefusedMessage(what, err)
		f, err = proto.NewError(proto.ErrCodeBadFrame, msg), nil
		if !hc.relays {
			if hc.chargeForward(len(f), read) {
				hs.HostViewerError(viewerID, proto.ErrCodeBadFrame, msg)
			}
			return
		}
	}
	if err == nil && hc.chargeForward(len(f), read) {
		hs.HostSignal(viewerID, f)
	}
}

// byteBucket is a token bucket of bytes: rate a second, burst at most (a
// relay bound's is Server.relayBurst).
// take charges n bytes at once, all of them, and then waits, as long as ctx
// allows, until the bucket has refilled what it owes, so a host (or a
// viewer) that streams more than the bound is slowed rather than cut off:
// the server reads its connection no faster than the bucket refills, and TCP
// does the rest.
// It is safe to share: the open hosts of one address take from one bucket,
// and so do the viewers of one address (Config.ViewerRelayKBps). Since each
// take is charged as it comes, takes are served in their order, whatever
// their sizes: a large one never waits behind small ones that keep coming.
// One that is cancelled while it waits stays charged: its bytes were read.
type byteBucket struct {
	mu          sync.Mutex
	rate, burst float64
	// tokens is what may be taken without waiting; below zero, what the
	// takes so far owe.
	tokens float64
	last   time.Time
	// waits counts the takes that had to wait: a test's way to tell what
	// the bound slowed from what a busy machine did.
	waits int
}

func newByteBucket(bytesPerSecond, burst int) *byteBucket {
	return &byteBucket{rate: float64(bytesPerSecond), burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// relayBurstFloor is the least burst a relay bound allows: what a person's
// tabs ask for or send together, at most, passes at once whatever the rate,
// for the browser gives up on a file request after 20 s (and calls a save
// slow then; docs/protocol.md, File saves). That is
// three of the largest frames, a file of proto.MaxFileBytes read or saved
// with its header, and room besides. A server takes it when it is made
// (Server.relayBurstFloor), so a test may lower it for the servers it makes.
var relayBurstFloor = 4 << 20

// relayBurst is the burst of a relay bound of rate bytes a second: twice the
// rate, and never less than the floor. The rate is what the bound allows
// over time; the burst, what a person may ask for at once.
func (s *Server) relayBurst(rate int) int {
	return max(2*rate, s.relayBurstFloor)
}

// refill brings tokens up to now. Called with mu held.
func (b *byteBucket) refill(now time.Time) {
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
}

func (b *byteBucket) take(ctx context.Context, n int) error {
	b.mu.Lock()
	b.refill(time.Now())
	b.tokens -= float64(n)
	owed := -b.tokens
	if owed > 0 {
		b.waits++
	}
	b.mu.Unlock()
	if owed <= 0 {
		return nil
	}
	t := time.NewTimer(time.Duration(owed / b.rate * float64(time.Second)))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// untilFull is how long the bucket takes, from now, to hold its burst again.
func (b *byteBucket) untilFull(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	return time.Duration((b.burst - b.tokens) / b.rate * float64(time.Second))
}

// handleHostMessage dispatches a JSON message from the host; false is fatal.
func (s *Server) handleHostMessage(hs *signal.HostedSession, hc *hostConnState, data []byte) bool {
	t, err := proto.ParseHeader(data)
	if err != nil {
		return false
	}
	switch t {
	case proto.HostAnswer:
		var m proto.ViewerSDP
		if json.Unmarshal(data, &m) != nil || m.SDP == "" {
			return false
		}
		f, err := signal.AnswerFrame(m.SDP)
		hc.forwardSignal(hs, m.ViewerID, "answer", len(data), f, err)
	case proto.HostICE:
		var m proto.ViewerICE
		if json.Unmarshal(data, &m) != nil || len(m.Candidate.Candidate) > proto.MaxICECandidate {
			return false
		}
		f, err := signal.ICEFrame(m.Candidate)
		hc.forwardSignal(hs, m.ViewerID, "ice", len(data), f, err)
	case proto.HostStatus:
		var m proto.HostStatusMsg
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		st := session.Status(m.Status)
		switch st {
		case session.StatusRunning, session.StatusExited, session.StatusStopped, session.StatusStarting:
		default:
			return false
		}
		hs.HostStatus(st, m.ExitCode)
	case proto.HostResize:
		var m proto.HostResizeMsg
		if json.Unmarshal(data, &m) != nil || !proto.ValidDimension(m.Cols) || !proto.ValidDimension(m.Rows) {
			return false
		}
		hs.HostResize(m.Cols, m.Rows)
	case proto.HostViewerError:
		var m proto.ViewerError
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		if hc.chargeForward(len(proto.NewError(m.Code, m.Message)), len(data)) {
			hs.HostViewerError(m.ViewerID, m.Code, m.Message)
		}
	case proto.HostViewerClose:
		var m proto.ViewerRef
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		hs.HostViewerClosed(m.ViewerID)
	case proto.HostAttention:
		var m proto.HostAttentionMsg
		if json.Unmarshal(data, &m) != nil || !session.AttentionState(m.State).Valid() {
			return false
		}
		// The host's own report is not forwarded, so it is never limited;
		// its question keeps the host's id, and its mark as the agent's own.
		hs.HostAttention(session.AttentionState(m.State), m.Message, m.Source, m.Kind, signal.OptionsFromProto(m.Options), m.ID, m.Ask, m.Answered, m.Plan)
	case proto.HostActivity:
		var m proto.HostActivityMsg
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		// A switchyard keeps no activity of the sessions published there:
		// nobody on it has a role on them, and their viewers have the log
		// from the session itself. (A Conductor publishing to it sends none.)
		if s.cfg.Switchyard.Enabled {
			break
		}
		// The session is the connection's, whatever m.SessionID says; the
		// entry and its state are checked and cleaned by HostActivity, and an
		// entry of a type this server does not know (a newer host's) is
		// dropped, not fatal.
		hs.HostActivity(m.Entry, m.State)
	case proto.HostChat:
		var m proto.HostChatMsg
		if json.Unmarshal(data, &m) != nil {
			return false
		}
		// Nor any chat, for the same reasons.
		if s.cfg.Switchyard.Enabled {
			break
		}
		// The session is the connection's, whatever m.SessionID says; the
		// message is checked and cleaned by HostChat, and one of a kind this
		// server does not know is dropped, not fatal.
		hs.HostChat(m.Message)
	case proto.HostLink:
		var m proto.HostLinkMsg
		if json.Unmarshal(data, &m) != nil || m.RequestID == "" || len(m.RequestID) > proto.MaxLinkRequestID {
			return false
		}
		s.hostLink(hs, hc, m)
	case proto.HostLinkRevoke:
		var m proto.HostLinkRevokeMsg
		if json.Unmarshal(data, &m) != nil || m.RequestID == "" || len(m.RequestID) > proto.MaxLinkRequestID || m.LinkID == "" || len(m.LinkID) > proto.MaxLinkID {
			return false
		}
		// A revoke is never counted against the link bucket: it must always go through.
		found, _ := s.links.Revoke(hs.Info().ID, m.LinkID)
		if !found && hs.Owner() != "" {
			found, _ = s.links.RevokeGroup(hs.Owner(), m.LinkID)
		}
		// One the switchyard no longer holds whose file waits to be
		// deleted is still its host's to revoke: the delete is tried again.
		// A session that ended took its links with it, and its viewers may
		// still be written what its end owed them: the revoke stops that,
		// whatever the switchyard still keeps of the link.
		if hs.Info().Status.Ended() {
			hs.DisconnectLink(m.LinkID)
		}
		if !found {
			found = s.retryUndeleted(m.LinkID, hs.Owner())
		}
		switch {
		case !found:
			_ = hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: "not_found", Message: "no such link", RequestID: m.RequestID})
		case s.linkFileUndeleted(m.LinkID):
			// Revoked here, but what the switchyard keeps of it on disk is
			// not deleted yet: the host is told so, keeps its record, and
			// its next revoke tries the delete again.
			_ = hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: "store_unavailable", Message: "the link is revoked, but the switchyard could not yet delete what it keeps of it; revoke it again", RequestID: m.RequestID})
		default:
			s.log.Info("link revoked by its host", "session", hs.Info().ID, "link", m.LinkID)
			_ = hs.Tell(proto.LinkRevoked{T: proto.HostLinkRevoked, RequestID: m.RequestID, LinkID: m.LinkID})
		}
	case proto.HostRunLink:
		var m proto.HostRunLinkMsg
		if json.Unmarshal(data, &m) != nil || m.RequestID == "" || len(m.RequestID) > proto.MaxLinkRequestID {
			return false
		}
		s.hostRunLink(hs, hc, m)
	case proto.HostRunLinkUpdate:
		var m proto.HostRunLinkUpdateMsg
		if json.Unmarshal(data, &m) != nil || m.RequestID == "" || len(m.RequestID) > proto.MaxLinkRequestID {
			return false
		}
		s.hostRunLinkUpdate(hs, hc, m)
	case proto.HostRegister:
		return false
	default:
		s.log.Debug("ignoring unknown host message", "t", t)
	}
	return true
}

// hostLink mints a share link to the host's session on its request and
// answers link_created, or error{requestId} for a refused one: a role that
// is not view or control, a label over proto.MaxLinkLabel bytes, a lifetime
// over a day, or more than proto.LinkRequestsPerMinute in a minute.
func (s *Server) hostLink(hs *signal.HostedSession, hc *hostConnState, m proto.HostLinkMsg) {
	refuse := func(code, msg string) {
		_ = hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: code, Message: msg, RequestID: m.RequestID})
	}
	now := time.Now()
	hc.linkTimes = slices.DeleteFunc(hc.linkTimes, func(t time.Time) bool { return now.Sub(t) > time.Minute })
	if len(hc.linkTimes) >= proto.LinkRequestsPerMinute {
		refuse("rate_limited", "too many link requests; wait a minute")
		return
	}
	role := session.Role(m.Role)
	if !role.Valid() {
		refuse("invalid_role", "role must be view or control")
		return
	}
	if len(m.Label) > proto.MaxLinkLabel {
		refuse("invalid_request", "label too long")
		return
	}
	if m.TTLSeconds < 0 || m.TTLSeconds > proto.MaxLinkTTLSeconds {
		refuse("invalid_request", "ttlSeconds out of range: a day at most")
		return
	}
	hc.linkTimes = append(hc.linkTimes, now)
	ttl := time.Duration(m.TTLSeconds) * time.Second
	link, token, err := s.links.Create(hs.Info().ID, role, m.Label, ttl)
	if err != nil {
		refuse("link_refused", err.Error())
		return
	}
	if err := s.keepLink(link, token, hs.Owner(), hc.addr); err != nil {
		refuse("link_refused", err.Error())
		return
	}
	s.log.Info("link minted for a host", "session", hs.Info().ID, "role", role, "label", link.Label)
	out := proto.LinkCreated{T: proto.HostLinkCreated, RequestID: m.RequestID, URL: hc.base + "/join/" + url.PathEscape(token), Invite: inviteFor(hc.base, token), LinkID: link.ID, Role: string(link.Role), Label: link.Label}
	if link.ExpiresAt != nil {
		out.ExpiresAt = link.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_ = hs.Tell(out)
}

// liveLinkIDs are the ids of the links that still open a hosted session here
// (not revoked, not expired), and of those revoked whose revoke is not done
// (their file waits to be deleted). Never nil, so registered always carries
// the list.
func (s *Server) liveLinkIDs(sessionID string) []string {
	ids := []string{}
	now := time.Now()
	for _, l := range append(s.links.ListBySession(sessionID), s.links.ListNaming(sessionID)...) {
		if l.Revoked || (l.ExpiresAt != nil && !now.Before(*l.ExpiresAt)) {
			continue
		}
		ids = append(ids, l.ID)
	}
	// A revoked link whose file on a switchyard waits to be deleted is
	// still the host's to revoke again (store_unavailable until it goes),
	// so the host keeps its record of it.
	for _, id := range s.undeletedLinks() {
		if l, ok := s.links.Get(id); ok && l.Revoked && ((l.RunID == "" && l.SessionID == sessionID) || l.Group.Names(sessionID)) {
			ids = append(ids, id)
		}
	}
	return ids
}

// groupOf checks a host's run group: within its bounds, and every member
// session named one this host registered here (the same instance). It
// returns the group as the link store keeps it, or the code and words of a
// refusal.
func (s *Server) groupOf(hs *signal.HostedSession, g proto.RunGroup) (share.Group, string, string) {
	if hs.Owner() == "" {
		return share.Group{}, "no_instance", "a run link needs a host that registers with an instance"
	}
	if err := g.Validate(); err != nil {
		return share.Group{}, "invalid_request", err.Error()
	}
	out := share.Group{Name: g.Name, Revision: g.Revision}
	for _, m := range g.Members {
		if m.SessionID != "" {
			other, ok := s.hosts.Get(m.SessionID)
			if !ok || other.Owner() != hs.Owner() {
				return share.Group{}, "invalid_request", "session " + m.SessionID + " is not this host's"
			}
		}
		out.Members = append(out.Members, share.GroupMember{Name: m.Name, SessionID: m.SessionID, AgentID: m.AgentID, Status: m.Status})
	}
	return out, "", ""
}

// hostRunLink mints one link to the sessions of a host's crew run and
// answers link_created with the run's id.
func (s *Server) hostRunLink(hs *signal.HostedSession, hc *hostConnState, m proto.HostRunLinkMsg) {
	refuse := func(code, msg string) {
		_ = hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: code, Message: msg, RequestID: m.RequestID})
	}
	now := time.Now()
	hc.linkTimes = slices.DeleteFunc(hc.linkTimes, func(t time.Time) bool { return now.Sub(t) > time.Minute })
	if len(hc.linkTimes) >= proto.LinkRequestsPerMinute {
		refuse("rate_limited", "too many link requests; wait a minute")
		return
	}
	role := session.Role(m.Role)
	if !role.Valid() {
		refuse("invalid_role", "role must be view or control")
		return
	}
	if len(m.Label) > proto.MaxLinkLabel || m.TTLSeconds < 0 || m.TTLSeconds > proto.MaxLinkTTLSeconds {
		refuse("invalid_request", "label or ttlSeconds out of range")
		return
	}
	g, code, msg := s.groupOf(hs, m.Run)
	if code != "" {
		refuse(code, msg)
		return
	}
	hc.linkTimes = append(hc.linkTimes, now)
	link, token, err := s.links.CreateGroupLink(m.Run.ID, hs.Owner(), g, role, m.Label, time.Duration(m.TTLSeconds)*time.Second)
	if errors.Is(err, share.ErrStaleGroup) {
		// Members the host sent before those the run's links hold: no link
		// is made with them.
		refuse("stale", "the run's links hold members the host sent after these")
		return
	}
	if err != nil {
		refuse("link_refused", err.Error())
		return
	}
	if err := s.keepLink(link, token, hs.Owner(), hc.addr); err != nil {
		refuse("link_refused", err.Error())
		return
	}
	s.log.Info("run link minted for a host", "run", m.Run.ID, "members", len(g.Members), "role", role)
	out := proto.LinkCreated{T: proto.HostLinkCreated, RequestID: m.RequestID, URL: hc.base + "/join/" + url.PathEscape(token), Invite: inviteFor(hc.base, token), LinkID: link.ID, Role: string(link.Role), Label: link.Label, RunID: m.Run.ID}
	if link.ExpiresAt != nil {
		out.ExpiresAt = link.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_ = hs.Tell(out)
}

// hostRunLinkUpdate gives the links of a host's run its members now and
// answers link_run_updated, not_found when the run has none here, or stale
// when its links hold members the host sent after these (a revision as
// high or higher): an update that reaches the switchyard after a later one,
// over another member's connection or the connection before a reconnect,
// changes nothing.
func (s *Server) hostRunLinkUpdate(hs *signal.HostedSession, hc *hostConnState, m proto.HostRunLinkUpdateMsg) {
	refuse := func(code, msg string) {
		_ = hs.Tell(proto.ErrorMsg{T: proto.HostError, Code: code, Message: msg, RequestID: m.RequestID})
	}
	now := time.Now()
	hc.updateTimes = slices.DeleteFunc(hc.updateTimes, func(t time.Time) bool { return now.Sub(t) > time.Minute })
	if len(hc.updateTimes) >= proto.RunLinkUpdatesPerMinute {
		refuse("rate_limited", "too many run updates; wait a minute")
		return
	}
	hc.updateTimes = append(hc.updateTimes, now)
	g, code, msg := s.groupOf(hs, m.Run)
	if code != "" {
		refuse(code, msg)
		return
	}
	changed, left, err := s.links.SetGroup(hs.Owner(), m.Run.ID, g)
	if err != nil {
		refuse("stale", "the run's links hold members the host sent after these")
		return
	}
	if len(changed) == 0 {
		refuse("not_found", "no links for this run")
		return
	}
	// A session the run no longer names is no longer opened by its links:
	// whoever is attached to it through one of them goes. The links name
	// the new group first, so one attaching meanwhile is refused.
	for _, sid := range left {
		if d, ok := s.registry.Get(sid); ok {
			for _, lid := range changed {
				d.DisconnectLink(lid)
			}
		}
	}
	s.keepGroup(hs.Owner(), m.Run.ID)
	_ = hs.Tell(proto.RunLinkUpdated{T: proto.HostRunLinkUpdated, RequestID: m.RequestID, RunID: m.Run.ID, Links: len(changed)})
}
