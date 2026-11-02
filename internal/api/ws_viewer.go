package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

func (s *Server) acceptOptions() *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{OriginPatterns: append([]string{}, s.cfg.AllowedOrigins...)}
	if s.cfg.Dev {
		opts.OriginPatterns = append(opts.OriginPatterns, "localhost:*", "127.0.0.1:*")
	}
	// A switchyard takes the desktop app's own workbench as a viewer.
	if s.cfg.Switchyard.Enabled {
		opts.OriginPatterns = append(opts.OriginPatterns, s.cfg.SwitchyardOrigins()...)
	}
	return opts
}

// handleViewerWS attaches a browser to a session. Authentication failures are
// reported through WebSocket close codes because browsers cannot read the
// HTTP status of a failed upgrade.
func (s *Server) handleViewerWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := s.authenticate(r, true)
	role := p.role(id)
	if role == "" && !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	// The session is found before the upgrade is answered: a client let in
	// while the session was on the server is told what became of it (4410
	// once it has left), never that there is no such session.
	d, ok := s.registry.Get(id)
	hk := &hijackKeeper{ResponseWriter: w}
	c, err := websocket.Accept(hk, r, s.acceptOptions())
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return
	}
	c.SetReadLimit(readLimit)
	if role == "" {
		c.Close(proto.CloseUnauthorized, "unauthorized")
		return
	}
	if !ok {
		c.Close(proto.CloseNotFound, "no such session")
		return
	}
	// The role above lets the connection in; the link is checked again as
	// the viewer attaches, under the lock a revoke takes to close the link's
	// viewers, since the hello may come seconds later.
	check := s.attachCheck(p, id)
	// An identity provider's principal is asked about again while it is
	// attached, and closed as revoked once the provider no longer admits
	// it (principalWatch); nil for the server's own tokens and links.
	watch := s.principalWatch(p, presentedToken(r, true))
	switch drv := d.(type) {
	case *session.Local:
		s.serveLocalViewer(r.Context(), c, hk.conn, drv, role, p.linkID(), p.admin, check, watch)
	case *signal.HostedSession:
		s.serveHostedViewer(r.Context(), c, hk.conn, drv, role, p.linkID(), check, watch, clientKey(r))
	default:
		c.Close(websocket.StatusInternalError, "unsupported session kind")
	}
}

// owner is the workbench's own connection (the admin token), the only one
// offered the editor's Neovim. raw is c's socket (nil when not known).
// watch, when not nil, runs while the viewer is attached and ends it as
// revoked (principalWatch, Subscription.Revoke).
func (s *Server) serveLocalViewer(ctx context.Context, c *websocket.Conn, raw net.Conn, local *session.Local, role session.Role, linkID string, owner bool, check func() (session.Grant, error), watch func(context.Context, func())) {
	hello, err := s.readHello(ctx, c)
	if err != nil {
		return
	}
	sink := s.viewerSink(c, raw)
	sub, err := local.AttachWith(session.AttachOptions{
		Role: role, LinkID: linkID, LinkLabel: s.linkLabel(linkID), Name: hello.Name, Cols: hello.Cols, Rows: hello.Rows, ChatOnly: hello.ChatOnly,
		Authorize: check,
		Owner:     owner,
	}, sink)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrRevoked), errors.Is(err, session.ErrSessionEnded):
			// Said as a revoke (or the session's end) after the attach says
			// it: an error frame, then 4403 (or 4410).
			sink.Close(err)
		case errors.Is(err, session.ErrTooManyViewers):
			c.Close(proto.CloseTooManyViewers, "too many viewers")
		default:
			c.Close(websocket.StatusInternalError, "attach failed")
		}
		return
	}
	defer local.Detach(sub)
	if watch != nil {
		// Revoked as a revoked link's viewer is: its transport closed, and
		// what it has under way (a submission waiting for its turn or its
		// Enter, keys, a save) refused, not finished.
		watching, endWatch := context.WithCancel(sink.ctx)
		defer endWatch()
		go watch(watching, func() { sub.Revoke(errPrincipalRevoked) })
	}
	go keepalive(sink.ctx, c, s.pingEvery)
	var held heldTyping
	// The viewer's slot stays taken until what it asked to type is done.
	defer held.wait()
	for {
		f, err := readFrame(sink.ctx, c, 0)
		if err != nil {
			if !errors.Is(err, context.Canceled) && websocket.CloseStatus(err) == -1 && sub.Reason() == nil {
				sink.Close(nil)
			}
			return
		}
		// A client the session has ended (revoked, expired, dropped as a
		// slow consumer) is not acted on while its connection closes.
		select {
		case <-sub.Done():
			return
		default:
		}
		switch f.Type {
		case proto.TypeInput:
			if err := local.Input(sub, f.Payload); err != nil {
				s.sendKeysError(sub, local, err)
			}
		case proto.TypeControl:
			if !s.handleLocalControl(sub, local, &held, f.Payload) {
				sink.Close(nil)
				return
			}
		case proto.TypeFileWrite:
			// A save from the editor (design round 12, F6): its answer is a FILE frame.
			h, part, err := proto.DecodeFileWrite(f.Payload)
			if err != nil {
				// A part that cannot be read names no save to answer: a
				// protocol error, which closes the connection as an
				// unexpected frame does, the error written on the sink first
				// so that it is on the wire before the close. No client of
				// Conductor's sends one.
				_ = sink.WriteFrame(proto.NewError(proto.ErrCodeBadFrame, "malformed file write"))
				c.Close(proto.CloseProtocolError, "malformed file write")
				return
			}
			local.FileWrite(sub, h, part)
		default:
			local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "unexpected frame type"))
			c.Close(proto.CloseProtocolError, "unexpected frame type")
			return
		}
	}
}

// submitTimeout bounds a submit message's submission: its turn, the pause
// and its Enter.
const submitTimeout = 10 * time.Second

// sendKeysError answers keystrokes (INPUT) refused with err: revoked or
// expired when the viewer's link no longer opens the session, else as
// sendInputError.
func (s *Server) sendKeysError(sub *session.Subscription, local *session.Local, err error) {
	if code, msg, ok := session.LinkRefusal(err); ok {
		local.Send(sub, proto.NewError(code, msg))
		return
	}
	s.sendInputError(sub, local, err)
}

func (s *Server) sendInputError(sub *session.Subscription, local *session.Local, err error) {
	switch {
	case errors.Is(err, session.ErrReadOnly):
		local.Send(sub, proto.NewError(proto.ErrCodeReadOnly, "this link is view-only"))
	case errors.Is(err, session.ErrSessionEnded):
		local.Send(sub, proto.NewError(proto.ErrCodeSessionEnded, "the session has ended"))
	case errors.Is(err, session.ErrTrustQuestion):
		local.Send(sub, proto.NewError(proto.ErrCodeNotSent, session.TrustQuestionWords))
	case errors.Is(err, session.ErrQuestion):
		local.Send(sub, proto.NewError(proto.ErrCodeNotSent, session.QuestionWords))
	case errors.Is(err, session.ErrInputStalled):
		local.Send(sub, proto.NewError("input_failed", session.ErrInputStalled.Error()))
	case errors.Is(err, session.ErrTooManyRequests):
		local.Send(sub, proto.NewError(proto.ErrCodeTooManyRequests, "too many lines wait for the session's turn: try again once they are done"))
	default:
		local.Send(sub, proto.NewError("input_failed", "input could not be delivered"))
	}
}

// heldTyping runs what a server viewer asked to type into the agent (a
// submit, a chat to the agent, a chat_send): the read loop waits for it, so
// that what the client sends next comes after it, but no longer once the
// session's turn is held for a submission's text typed without its Enter
// (session.Local.Holding), whether it was as the request came or became so
// while it waited: it then waits until a person types into the terminal,
// and the viewer's own keys, sent after it, must reach the session to do
// that. One at a time per connection: another while one still waits is
// refused (too_many_requests) and the connection stays. A connection that
// closes meanwhile keeps its slot among the session's viewers until the
// request is done (wait), as one whose read loop waited for it does: what
// waits for the turn stays bounded by the viewers a session takes.
type heldTyping struct {
	busy atomic.Bool
	// done is closed once the request under way is done; nil before the
	// first. Only the read loop's goroutine sets it.
	done chan struct{}
}

// typeIn runs do as above, and refuse with its error; requestID names the
// request a too_many_requests refuses (a chat's id or a chat_send's ref,
// none for a submit).
func (h *heldTyping) typeIn(sub *session.Subscription, local *session.Local, requestID string, do func() error, refuse func(error)) {
	if !h.busy.CompareAndSwap(false, true) {
		local.Send(sub, proto.MustControl(proto.ErrorMsg{T: proto.CtlError, Code: proto.ErrCodeTooManyRequests,
			Message: "one line already waits for the terminal on this connection", RequestID: requestID}))
		return
	}
	done := make(chan struct{})
	h.done = done
	go func() {
		defer close(done)
		defer h.busy.Store(false)
		if err := do(); err != nil {
			refuse(err)
		}
	}()
	select {
	case <-done:
	case <-local.Holding():
	}
}

// wait returns once the request under way, if any, is done. Called on the
// read loop's goroutine as it ends.
func (h *heldTyping) wait() {
	if h.done != nil {
		<-h.done
	}
}

// handleLocalControl dispatches a control message; false means protocol error.
func (s *Server) handleLocalControl(sub *session.Subscription, local *session.Local, held *heldTyping, payload []byte) bool {
	t, err := proto.ParseHeader(payload)
	if err != nil {
		local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad control message"))
		return false
	}
	switch t {
	case proto.CtlResize:
		var m proto.Resize
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if err := local.ResizeWith(sub, m.Cols, m.Rows, m.Take); err != nil {
			switch {
			case errors.Is(err, session.ErrReadOnly), errors.Is(err, session.ErrNotSizer):
				// view-only clients, and every viewer but the sizer, follow the size silently
			case errors.Is(err, session.ErrBadDimension):
				local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "invalid terminal size"))
			}
		}
	case proto.CtlPing:
		var m proto.Ping
		_ = json.Unmarshal(payload, &m)
		local.Send(sub, proto.MustControl(proto.Ping{T: proto.CtlPong, TS: m.TS}))
	case proto.CtlFileGet:
		var m proto.FileGet
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if err := local.FileGet(sub, m); err != nil {
			if frame := session.FileGetRefused(m, err); frame != nil {
				local.Send(sub, frame)
			}
		}
	case proto.CtlNvimOpen:
		var m proto.NvimOpen
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), nvimOpenTimeout)
		err := local.NvimOpen(ctx, sub, m)
		cancel()
		if err != nil {
			local.Send(sub, session.NvimRefused(m.ReqID, err))
		}
	case proto.CtlNvimInput:
		var m proto.NvimInput
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if err := local.NvimInput(sub, m); err != nil {
			local.Send(sub, session.NvimInputRefused(m.ID, m.Seq, err))
		}
	case proto.CtlNvimClose:
		var m proto.NvimClose
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		local.NvimClose(sub, m)
	case proto.CtlNvimSwap:
		var m proto.NvimSwap
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if err := local.NvimSwap(sub, m); err != nil {
			local.Send(sub, session.NvimRefused("", err))
		}
	case proto.CtlSubmit:
		var m proto.Submit
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if !m.Valid() {
			if f := session.SubmitWord(m.ReqID, session.SubmitResult{}, session.ErrBadSubmit); f != nil {
				local.Send(sub, f)
			} else {
				local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "submit text too long"))
			}
			return true
		}
		// The owner's word (#322): one naming its request gets submit_ok or
		// an error naming it, once the outcome is known (a line the program
		// takes slowly is told through Finished); one naming none is told
		// nothing it was not told before (an unnamed error).
		word := func(res session.SubmitResult, err error) {
			if f := session.SubmitWord(m.ReqID, res, err); f != nil {
				local.Send(sub, f)
			} else if err != nil && m.ReqID == "" {
				s.sendInputError(sub, local, err)
			}
		}
		// The submission goes on when the client goes: a reply box closes its
		// connection once it has sent, and a line cut in its pause would wait
		// without its Enter. The read loop waits for it, so what the client
		// sends next comes after it (but while the turn is held: typeIn).
		held.typeIn(sub, local, m.ReqID, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
			defer cancel()
			res, err := local.Submit(ctx, session.Submission{Text: m.Text, By: sub, UnlessWaiting: m.UnlessWaiting, Finished: word})
			// A line the program takes slowly is accepted, and entered as it
			// reads (ErrSubmitPending); its word comes then.
			if !errors.Is(err, session.ErrSubmitPending) {
				word(res, err)
			}
			return nil
		}, func(err error) { word(session.SubmitResult{}, err) })
	case proto.CtlAnswer:
		var m proto.Answer
		if json.Unmarshal(payload, &m) != nil {
			return false
		}
		if !m.Valid() {
			local.Send(sub, session.AnswerResult(m.ReqID, session.ErrBadAnswer))
			return true
		}
		reply := func(err error) {
			if f := session.AnswerResult(m.ReqID, err); f != nil {
				local.Send(sub, f)
			}
		}
		if m.Text != "" {
			// A line is a submission: it waits for the session's turn as a
			// submit does, on the read loop but no longer once the turn is
			// held for a person (typeIn); the owner's word names it, once
			// its outcome is known (a line the program takes slowly is
			// finished later, and its word comes then).
			held.typeIn(sub, local, m.ReqID, func() error {
				if err := s.answer(sub, local, m, reply); !errors.Is(err, session.ErrSubmitPending) {
					reply(err)
				}
				return nil
			}, reply)
			return true
		}
		// A choice's keys on the read loop, as a keystroke is, so what the
		// client sends next comes after them; the owner's word names them,
		// once they have landed.
		if err := s.answer(sub, local, m, reply); !errors.Is(err, session.ErrSubmitPending) {
			reply(err)
		}
	case proto.CtlChat:
		var m proto.ChatPost
		if json.Unmarshal(payload, &m) != nil {
			local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad chat message"))
			return true
		}
		msg, err := local.Chat(sub, m)
		if err != nil {
			local.Send(sub, session.ChatErrorFrame(err, m.Nonce))
			return true
		}
		if m.To != "" {
			// Typed into the agent (or the run's member named) as a submit is, on the read loop, so what the client sends next comes after it.
			send := proto.ChatSend{T: proto.CtlChatSend, Ref: msg.ID, Scope: msg.Scope, To: m.To}
			held.typeIn(sub, local, msg.ID, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
				defer cancel()
				return local.ChatSend(ctx, sub, send)
			}, func(err error) { local.Send(sub, session.ChatErrorFrame(err, msg.ID)) })
		}
	case proto.CtlChatSend:
		var m proto.ChatSend
		if json.Unmarshal(payload, &m) != nil {
			local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad chat_send message"))
			return true
		}
		held.typeIn(sub, local, m.Ref, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
			defer cancel()
			return local.ChatSend(ctx, sub, m)
		}, func(err error) { local.Send(sub, session.ChatErrorFrame(err, m.Ref)) })
	case proto.CtlHello:
		// duplicate hello is harmless
	default:
		local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "unknown control message"))
		return false
	}
	return true
}

// answer types a viewer's reply to a question (proto.Answer): a choice's
// keys as a keystroke is written, a line as a submit is, each only while
// the question it names still waits (ErrQuestionAnswered otherwise). The
// word is the error returned, but for one accepted whose outcome is not
// known yet (session.ErrSubmitPending): finished gets it then, on another
// goroutine (nil once the keys landed or the line was entered,
// session.ErrLineStranded for a line typed and not entered).
func (s *Server) answer(sub *session.Subscription, local *session.Local, m proto.Answer, finished func(error)) error {
	if m.Input != "" {
		return local.AnswerInputFinished(context.Background(), sub, m.Question, []byte(m.Input), finished)
	}
	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()
	_, err := local.Submit(ctx, session.Submission{Text: m.Text, By: sub, Question: m.Question, Finished: func(res session.SubmitResult, err error) {
		finished(session.AnswerWord(res, err))
	}})
	return err
}

func (s *Server) readHello(ctx context.Context, c *websocket.Conn) (proto.Hello, error) {
	f, err := readFrame(ctx, c, helloTimeout)
	if err != nil {
		c.Close(proto.CloseProtocolError, "hello expected")
		return proto.Hello{}, err
	}
	var hello proto.Hello
	if f.Type != proto.TypeControl || json.Unmarshal(f.Payload, &hello) != nil || hello.T != proto.CtlHello {
		c.Close(proto.CloseProtocolError, "hello expected")
		return proto.Hello{}, errors.New("hello expected")
	}
	if hello.Proto != proto.ProtoVersion {
		c.Close(proto.CloseProtocolError, "unsupported protocol version")
		return proto.Hello{}, errors.New("unsupported protocol version")
	}
	if len(hello.Name) > 4*proto.MaxNameLen {
		c.Close(proto.CloseProtocolError, "name too long")
		return proto.Hello{}, errors.New("name too long")
	}
	return hello, nil
}

// linkLabel returns the label of a share link for the viewers roster.
func (s *Server) linkLabel(linkID string) string {
	if linkID == "" {
		return ""
	}
	if l, ok := s.links.Get(linkID); ok {
		return l.Label
	}
	return ""
}

// serveHostedViewer brokers WebRTC signaling for a hosted session and relays
// frames through the host connection when the viewer asks for it. What the
// viewer sends is read under its address's budget (cfg.ViewerRelayKBps),
// one bucket for every hosted viewer of the address, as the open hosts of
// one address share theirs: past it the viewer is slowed, its connection
// read no faster than the bucket refills. Each frame is charged the larger
// of what the viewer sent and what it sends the host (a relayed frame in its
// envelope, a message encoded anew).
//
// What the viewer sent before its connection closed reaches the host before
// the viewer's leave: the read loop reads the connection until it closes, or
// for drainTimeout once it has failed (a write to the viewer, a ping), and
// the viewer is let go (RemoveViewer, which tells the host it left) when the
// read loop is done. The connection's failure ends the read loop's waits
// (the budget, the join, room in the host's queue): the frame that waited is
// dropped, and with it whatever the viewer sent after it.
func (s *Server) serveHostedViewer(ctx context.Context, c *websocket.Conn, raw net.Conn, hs *signal.HostedSession, role session.Role, linkID string, check func() (session.Grant, error), watch func(context.Context, func()), addr string) {
	// What the viewer has not taken waits in its queue, where the server
	// sees it, not in its socket's buffer (hostedViewerUnsent).
	if raw != nil {
		limitUnsent(raw, hostedViewerUnsent)
	}
	sink := s.viewerSink(c, raw)
	defer sink.cancel()
	// waits is what the read loop's waits wait under: it ends when the
	// viewer's connection fails, or its writer ends.
	waits, endWaits := context.WithCancel(sink.ctx)
	defer endWaits()
	var budget *byteBucket
	if kbps := s.cfg.ViewerRelayKBps; kbps > 0 {
		budget = s.viewerRelay.acquire(addr, kbps*1024, s.relayBurst(kbps*1024))
		defer s.viewerRelay.release(addr)
	}
	// credit is what the frame in hand was charged as it was read and what
	// it sends the host has not used yet: a message to the host is charged
	// only what it brings past it. Read and charged on the read loop alone,
	// which is where the viewer sends the host anything.
	var credit int
	var charge func(n int) error
	// chargeJoin charges the viewer's join and leave, on the join's own
	// goroutine (the read loop's credit is not its), until the viewer closes.
	var chargeJoin func(stop <-chan struct{}, n int) error
	if budget != nil {
		charge = func(n int) error {
			use := min(credit, n)
			credit -= use
			if n == use {
				return nil
			}
			return budget.take(waits, n-use)
		}
		chargeJoin = func(stop <-chan struct{}, n int) error {
			ctx, cancel := context.WithCancel(waits)
			defer cancel()
			go func() {
				select {
				case <-stop:
					cancel()
				case <-ctx.Done():
				}
			}()
			return budget.take(ctx, n)
		}
	}
	viewerID := session.NewID()
	v, err := hs.AddViewerWith(signal.ViewerOptions{ID: viewerID, Role: role, LinkID: linkID, LinkLabel: s.linkLabel(linkID), Authorize: check, Charge: charge, ChargeJoin: chargeJoin, Principal: watch != nil})
	if err != nil {
		switch {
		case errors.Is(err, session.ErrRevoked), errors.Is(err, session.ErrSessionEnded):
			sink.Close(err)
		case errors.Is(err, signal.ErrTooManyViewer):
			c.Close(proto.CloseTooManyViewers, "too many viewers")
		default:
			c.Close(proto.CloseSessionEnded, "host disconnected")
		}
		return
	}
	defer hs.RemoveViewer(v)
	if watch != nil {
		// Revoked as a revoked link's viewer is: nothing of its still
		// queued for the host is written, and the host is told at once
		// that it was revoked, not once its connection has closed, so
		// that it withdraws what the viewer has under way
		// (HostedSession.RevokeViewer).
		watching, endWatch := context.WithCancel(sink.ctx)
		defer endWatch()
		go watch(watching, func() { hs.RevokeViewer(v, errPrincipalRevoked) })
	}

	info := hs.Info()
	// The media provider is asked once the viewer is attached, so a revoke
	// or the expiry of its link, the session's end or the host's going
	// that lands meanwhile closes it: the ask is cut short, and a viewer
	// closed by then is closed with its reason, never sent the welcome or
	// the servers in it. A viewer of an open host's session is handed the
	// configured servers alone (iceServersFor).
	lookup, endLookup := context.WithCancel(sink.ctx)
	go func() {
		select {
		case <-v.Done():
			endLookup()
		case <-lookup.Done():
		}
	}()
	ice := s.iceServersFor(lookup, ext.ICERequest{Purpose: icePurposeViewer, Session: info.ID, Role: string(v.Role), Viewer: viewerID}, v.HostOpen())
	endLookup()
	select {
	case <-v.Done():
		sink.Close(v.Reason())
		return
	default:
	}
	welcome := proto.MustControl(fitWelcome(proto.Welcome{
		T:              proto.CtlWelcome,
		Proto:          proto.ProtoVersion,
		SessionID:      info.ID,
		Role:           string(v.Role),
		ViewerID:       viewerID,
		Cols:           info.Cols,
		Rows:           info.Rows,
		Status:         string(info.Status),
		Transport:      proto.TransportWebRTC,
		ICEServers:     ice,
		RelayTimeoutMs: s.cfg.RelayTimeoutMs,
		RelayOnly:      hs.RelayOnly(),
	}, len(s.cfg.ICEServers)))
	if err := sink.WriteFrame(welcome); err != nil {
		return
	}
	// The read loop reads under reading: the connection's own until it
	// closes, and at most drainTimeout past its failure (waits).
	reading, endReading := context.WithCancel(sink.ctx)
	defer endReading()
	stopDrain := context.AfterFunc(waits, func() {
		t := time.NewTimer(drainTimeout)
		defer t.Stop()
		select {
		case <-t.C:
			endReading()
		case <-reading.Done():
		}
	})
	defer stopDrain()
	// cut says a message the viewer sent was dropped (err) once its
	// connection had failed, its wait cut short (the budget, the join, room
	// in the host's queue): nothing the viewer sent after it goes, and the
	// read loop ends. The failure is the viewer's own (Failed), set before
	// any of those waits ends for it. A viewer the server let go is not cut:
	// its connection closes as Pump ends, with what the viewer is owed.
	cut := func(err error) bool {
		return err != nil && v.Failed()
	}
	// Writer: drain frames queued by the host side. pumped closes when Pump
	// has closed the sink, or given up on a connection that failed; the read
	// loop's waits end with it, and its reads soon after.
	pumped := make(chan struct{})
	var out session.Sink = sink
	if s.viewerSeam != nil && s.viewerSeam.write != nil {
		out = seamSink{sink, s.viewerSeam.write}
	}
	go func() {
		defer close(pumped)
		v.Pump(out)
		if s.viewerSeam != nil && s.viewerSeam.ended != nil {
			s.viewerSeam.ended()
		}
		endWaits()
	}()
	// Once closed, the viewer is written what it is owed, its error and its
	// close within closeWait, or cut off (wsSink.Closing).
	go func() {
		select {
		case <-v.Done():
			sink.Closing()
		case <-pumped:
		}
	}()
	// A failed connection is left open for the read loop, the writer having
	// given up on it: it is closed once the read loop is done. (A connection
	// that has not failed is closed by the writer as it ends, which letting
	// the viewer go makes it do.)
	defer func() {
		if s.viewerSeam != nil && s.viewerSeam.left != nil {
			s.viewerSeam.left()
		}
		if v.Failed() {
			c.CloseNow()
		}
	}()
	// A connection that fails while nothing is written to it is found by
	// its pings, though the read loop is held: its waits end, and the
	// viewer is let go as the read loop ends.
	go keepaliveUntilGone(sink.ctx, c, s.pingEvery, func() {
		v.ConnFailed()
		endWaits()
	})
	for {
		if s.viewerSeam != nil && s.viewerSeam.read != nil {
			s.viewerSeam.read(pumped)
		}
		f, err := readFrame(reading, c, 0)
		if err != nil {
			return
		}
		// Every frame is counted, whatever becomes of it, before the next
		// is read; a frame waits for the host's queue in its turn after
		// (signal.HostConn's share for viewers), so neither wait holds up
		// the host's connection or another viewer's.
		if budget != nil {
			credit = 1 + len(f.Payload)
			if budget.take(waits, credit) != nil {
				return
			}
		}
		switch f.Type {
		case proto.TypeSignal:
			if !s.handleViewerSignal(sink, pumped, hs, v, f.Payload, cut) {
				return
			}
		case proto.TypeInput, proto.TypeControl, proto.TypeFileWrite:
			if !v.Relay() {
				_ = sink.WriteFrame(proto.NewError(proto.ErrCodeBadFrame, "terminal frames require relay mode on this connection"))
				continue
			}
			s.relayed.add(len(f.Payload))
			err := hs.RelayToHost(v, f)
			if err != nil {
				switch {
				case errors.Is(err, session.ErrReadOnly):
					_ = sink.WriteFrame(proto.NewError(proto.ErrCodeReadOnly, "this link is view-only"))
				case errors.Is(err, signal.ErrHostGone):
					hostGone(sink, pumped, s.hostGoneWait)
					return
				case cut(err):
					return
				}
			}
		default:
			c.Close(proto.CloseProtocolError, "unexpected frame type")
			return
		}
	}
}

// handleViewerSignal forwards signaling; false ends the connection, as a
// message cut short does (serveHostedViewer).
func (s *Server) handleViewerSignal(sink *wsSink, pumped <-chan struct{}, hs *signal.HostedSession, v *signal.Viewer, payload []byte, cut func(error) bool) bool {
	t, err := proto.ParseHeader(payload)
	if err != nil {
		return false
	}
	fail := func(err error) bool {
		switch {
		case errors.Is(err, signal.ErrHostGone):
			hostGone(sink, pumped, s.hostGoneWait)
			return false
		case cut(err):
			return false
		}
		return true
	}
	switch t {
	case proto.SigOffer:
		var m proto.SDP
		if json.Unmarshal(payload, &m) != nil || m.SDP == "" {
			return false
		}
		return fail(hs.ForwardOffer(v, m.SDP))
	case proto.SigICE:
		var m proto.ICE
		if json.Unmarshal(payload, &m) != nil || len(m.Candidate.Candidate) > proto.MaxICECandidate {
			return false
		}
		return fail(hs.ForwardICE(v, m.Candidate))
	case proto.SigRelay:
		if s.cfg.Switchyard.Enabled && !s.cfg.SwitchyardRelay() {
			// A switchyard without a relay: the viewer is told, and stays
			// on its WebRTC attempt.
			_ = sink.WriteFrame(proto.NewError(proto.ErrCodeRelayOff, "this switchyard does not relay: the terminal connects peer to peer or not at all"))
			return true
		}
		if err := hs.StartRelay(v); err != nil {
			return fail(err)
		}
		if f, err := proto.EncodeJSON(proto.TypeSignal, proto.Simple{T: proto.SigRelayOK}); err == nil {
			_ = sink.WriteFrame(f)
		}
		return true
	}
	return false
}

// drainTimeout bounds how long a hosted viewer's connection is still read
// once it has failed: what the viewer sent before it closed is in the
// socket already.
const drainTimeout = 2 * time.Second

// nvimOpenTimeout bounds the start of an editor (Neovim loading a config).
const nvimOpenTimeout = 15 * time.Second
