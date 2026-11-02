package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// peer is one viewer: either a WebRTC data channel or a relay through the
// server. Terminal frames from the viewer are dispatched to the local session
// identically for both transports.
type peer struct {
	a         *agent
	id        string
	role      session.Role
	linkID    string
	linkLabel string
	// owner is a viewer with no link from a server the person said is
	// their own (Options.ServerIsOwners): one of the owner's own windows,
	// offered the editor's Neovim. A paste invite's viewer has no link
	// either and is not, nor one a switchyard sends.
	owner bool
	// replaces is the subscription the viewer had before it moved to the
	// relay, for the relay's attach to take over its slot.
	replaces *session.Subscription
	// cred is what the host knows of the viewer's link (linkCreds), or of
	// the identity provider's principal it came as (linkCreds.principal),
	// nil for neither; set before the peer is seen.
	cred *linkCred

	// attaching runs the viewer's attaches one at a time (attach): a hello
	// over the relay waits for one over the data channel still under way,
	// which gives way to it.
	attaching sync.Mutex

	mu        sync.Mutex
	pc        *webrtc.PeerConnection
	dc        *dcSink
	relay     *relaySink
	sub       *session.Subscription
	pending   []webrtc.ICECandidateInit
	remoteSet bool
	closed    bool
	// offering says an offer of the viewer's is being answered
	// (handleOffer): another meanwhile is refused.
	offering bool

	// typing runs the viewer's submits, chats to the agent and chat_sends;
	// keys its keystrokes; editing its editor's keys, swap answers and
	// closes; saving the parts of its saves; nvimOpening counts its
	// nvim_opens running (queue.go).
	typing      requestQueue
	keys        requestQueue
	editing     requestQueue
	saving      requestQueue
	nvimOpening atomic.Int32
	// chatsLater counts the viewer's chat posts taken off the frame loop
	// while its host did not vouch for its server (chatLater) and not yet
	// finished: the posts after them follow them there, in their order.
	chatsLater atomic.Int32
	// filesWaiting counts the viewer's file reads waiting for the host to
	// vouch for its server (fileGetIn).
	filesWaiting atomic.Int32

	// room is whether the server has room for more of the viewer's relayed
	// frames (viewer_behind), whichever relay sink the viewer is on.
	room relayRoom

	// left is closed when the peer is closed (the viewer left): what waits
	// to tell the server of the viewer is withdrawn then. erred says the
	// viewer's error is on its way (sendViewerError), the first alone.
	left  chan struct{}
	erred atomic.Bool
	// letGo is why the server let the viewer go when its link no longer
	// opens the session (session.ErrRevoked, session.ErrExpired; closeFor),
	// nil before and for any other end: the credential check of each of
	// the viewer's subscriptions (authorize) says so from then on.
	letGo atomic.Pointer[error]
}

var (
	errNoWebRTC      = errors.New("webrtc disabled on this host")
	errOfferUnderWay = errors.New("an offer of this viewer's is being answered")
)

func newPeer(a *agent, id string, role session.Role, linkID, linkLabel string, owner bool) *peer {
	return &peer{
		a: a, id: id, role: role, linkID: linkID, linkLabel: linkLabel, owner: owner && linkID == "", left: make(chan struct{}),
		keys:    requestQueue{limit: maxQueuedKeys, maxBytes: maxQueuedKeyBytes},
		editing: requestQueue{limit: maxQueuedEdits},
		saving:  requestQueue{limit: maxQueuedSaveParts, maxBytes: maxQueuedSaveBytes},
	}
}

// startWebRTC prepares a peer connection that answers the viewer's offer.
func (p *peer) startWebRTC(ice []proto.ICEServer) error {
	cfg := webrtc.Configuration{}
	for _, s := range ice {
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	se := webrtc.SettingEngine{}
	if err := applyICE(&se, p.a.opts.ICE); err != nil {
		return err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return err
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		p.a.send(proto.ViewerICE{T: proto.HostICE, ViewerID: p.id, Candidate: proto.ICECandidate{
			Candidate: init.Candidate, SDPMid: init.SDPMid, SDPMLineIndex: init.SDPMLineIndex,
		}})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		p.a.log.Debug("peer connection state", "viewer", p.id, "state", st.String())
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			p.detachDataChannel()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		sink := newDCSink(dc, p.a.log)
		p.mu.Lock()
		p.dc = sink
		p.mu.Unlock()
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if msg.IsString {
				return
			}
			f, err := proto.Decode(msg.Data)
			if err != nil {
				// A frame no reader would take (a type the protocol has
				// not, a payload past its type's limit: a save part over
				// its 36 KiB) is a protocol error, as a malformed save
				// part is, not a frame to pass over: the save under way
				// would wait for its next part for good.
				p.refuse(sink, "unreadable frame: "+err.Error())
				return
			}
			p.handle(f, sink)
		})
		dc.OnClose(func() { p.detachDataChannel() })
	})
	p.mu.Lock()
	p.pc = pc
	p.mu.Unlock()
	return nil
}

// handleOffer answers the viewer's offer on a goroutine of its own and
// returns at once, so that the loop that read it (the control connection's,
// which carries every viewer's messages and the server's) waits for none of
// it: neither the answer's making nor its wait for its turn on the writer
// (up to controlTurnWait, past which it is dropped and the viewer falls
// back to the relay on its own timeout). Candidates that come before the
// offer is taken wait for it (handleICE). It refuses at once with
// errNoWebRTC without a peer connection, and with errOfferUnderWay while
// another offer of the viewer's is being answered. One that fails is the
// viewer's error (webrtc_failed), unless the peer connection it was for is
// no longer the viewer's by then (the viewer moved to the relay, or left):
// what becomes of an offer of a connection let go is nobody's concern.
func (p *peer) handleOffer(sdp string) error {
	p.mu.Lock()
	pc := p.pc
	switch {
	case pc == nil:
		p.mu.Unlock()
		return errNoWebRTC
	case p.offering:
		p.mu.Unlock()
		return errOfferUnderWay
	}
	p.offering = true
	p.mu.Unlock()
	go func() {
		if seam := p.a.beforeAnswer.Load(); seam != nil {
			(*seam)()
		}
		err := p.answerOffer(pc, sdp)
		p.mu.Lock()
		p.offering = false
		current := p.ownsPC(pc)
		p.mu.Unlock()
		if err != nil && current {
			p.a.sendViewerError(p.id, "webrtc_failed", err.Error())
		}
	}()
	return nil
}

// ownsPC says whether pc is still the viewer's peer connection. The caller
// holds p.mu.
func (p *peer) ownsPC(pc *webrtc.PeerConnection) bool { return p.pc == pc && !p.closed }

// answerOffer makes pc's answer to sdp, adds the candidates that waited
// for it, and sends it (agent.send: dropped once it has waited
// controlTurnWait for its turn), as long as pc is still the viewer's: an
// answer for a connection let go meanwhile is neither applied nor sent.
func (p *peer) answerOffer(pc *webrtc.PeerConnection, sdp string) error {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return err
	}
	p.mu.Lock()
	if !p.ownsPC(pc) {
		p.mu.Unlock()
		return nil
	}
	p.remoteSet = true
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, c := range pending {
		_ = pc.AddICECandidate(c)
	}
	p.a.send(proto.ViewerSDP{T: proto.HostAnswer, ViewerID: p.id, SDP: answer.SDP})
	return nil
}

func (p *peer) handleICE(c proto.ICECandidate) {
	init := webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: c.SDPMid, SDPMLineIndex: c.SDPMLineIndex}
	p.mu.Lock()
	pc, ready := p.pc, p.remoteSet
	if !ready {
		if len(p.pending) < 64 {
			p.pending = append(p.pending, init)
		}
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	if pc != nil {
		_ = pc.AddICECandidate(init)
	}
}

// startRelay switches the viewer to the server relay.
func (p *peer) startRelay() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	pc := p.pc
	p.pc = nil
	p.dc = nil
	if p.sub != nil {
		p.a.local.Detach(p.sub)
		// The relay's attach takes this one's slot while its transport closes.
		p.replaces = p.sub
		p.sub = nil
	}
	old := p.relay
	p.relay = newRelaySink(p.a, p.id)
	p.relay.room = &p.room
	p.mu.Unlock()
	if old != nil {
		old.let()
	}
	if pc != nil {
		_ = pc.Close()
	}
}

// buffered returns the bytes the peer's data channel holds that the viewer has
// not acknowledged; zero for a viewer on the relay or one not connected yet.
func (p *peer) buffered() uint64 {
	p.mu.Lock()
	sink := p.dc
	p.mu.Unlock()
	if sink == nil {
		return 0
	}
	return sink.buffered()
}

// handleFrame processes a terminal frame from the viewer over the relay.
func (p *peer) handleFrame(f proto.Frame) { p.handle(f, nil) }

// handle processes a terminal frame from the viewer over either transport:
// the data channel via, or the relay (nil).
func (p *peer) handle(f proto.Frame, via *dcSink) {
	p.mu.Lock()
	sub := p.sub
	closed := p.closed
	p.mu.Unlock()
	// A viewer being let go for a protocol error (refuse) is acted on no
	// more while the close reaches it.
	if closed || p.erred.Load() {
		return
	}
	if sub != nil {
		// A viewer the session has ended (dropped as a slow consumer) is
		// not acted on while its connection closes.
		select {
		case <-sub.Done():
			return
		default:
		}
	}
	switch f.Type {
	case proto.TypeControl:
		t, err := proto.ParseHeader(f.Payload)
		if err != nil {
			return
		}
		if t == proto.CtlHello {
			if sub != nil {
				return
			}
			var hello proto.Hello
			if json.Unmarshal(f.Payload, &hello) != nil || hello.Proto != proto.ProtoVersion {
				p.a.sendViewerError(p.id, proto.ErrCodeBadFrame, "unsupported hello")
				return
			}
			p.attach(hello, via)
			return
		}
		if sub == nil {
			return
		}
		switch t {
		case proto.CtlResize:
			var m proto.Resize
			if json.Unmarshal(f.Payload, &m) == nil {
				_ = p.a.local.ResizeWith(sub, m.Cols, m.Rows, m.Take)
			}
		case proto.CtlPing:
			var m proto.Ping
			_ = json.Unmarshal(f.Payload, &m)
			p.a.local.Send(sub, proto.MustControl(proto.Ping{T: proto.CtlPong, TS: m.TS}))
		case proto.CtlSubmit:
			var m proto.Submit
			if json.Unmarshal(f.Payload, &m) != nil || !m.Valid() {
				if w := session.SubmitWord(m.ReqID, session.SubmitResult{}, session.ErrBadSubmit); w != nil {
					p.a.local.Send(sub, w)
				} else {
					p.a.local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad submit message"))
				}
				return
			}
			// The owner's word (#322), as the server's viewer connection gives
			// it: submit_ok or an error naming the request, once the outcome
			// is known; a submit naming no request is told an unnamed error.
			word := func(res session.SubmitResult, err error) {
				if w := session.SubmitWord(m.ReqID, res, err); w != nil {
					p.a.local.Send(sub, w)
				} else if err != nil && m.ReqID == "" {
					p.a.local.Send(sub, submitError(err))
				}
			}
			// Off the frame loop, which also carries the relay's other
			// viewers: the submission pauses before its Enter.
			p.typeIn(sub, m.ReqID, func(ctx context.Context) error {
				res, err := p.a.local.Submit(ctx, session.Submission{Text: m.Text, By: sub, UnlessWaiting: m.UnlessWaiting, Finished: word})
				if !errors.Is(err, session.ErrSubmitPending) {
					// Accepted while the program takes it slowly: entered as
					// it reads, its word then.
					word(res, err)
				}
				return nil
			}, func(err error) { word(session.SubmitResult{}, err) })
		case proto.CtlAnswer:
			var m proto.Answer
			if json.Unmarshal(f.Payload, &m) != nil || !m.Valid() {
				p.a.local.Send(sub, session.AnswerResult(m.ReqID, session.ErrBadAnswer))
				return
			}
			p.answerIn(sub, m)
		case proto.CtlChat:
			var m proto.ChatPost
			if json.Unmarshal(f.Payload, &m) != nil {
				p.a.local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad chat message"))
				return
			}
			if p.chatLater(sub, m) {
				return
			}
			msg, err := p.a.local.Chat(sub, m)
			if err != nil {
				p.a.local.Send(sub, session.ChatErrorFrame(err, m.Nonce))
				return
			}
			if m.To != "" {
				// Typed as a submit is, in its turn among them.
				send := proto.ChatSend{T: proto.CtlChatSend, Ref: msg.ID, Scope: msg.Scope, To: m.To}
				p.typeIn(sub, msg.ID, func(ctx context.Context) error {
					return p.a.local.ChatSend(ctx, sub, send)
				}, func(err error) { p.a.local.Send(sub, session.ChatErrorFrame(err, msg.ID)) })
			}
		case proto.CtlChatSend:
			var m proto.ChatSend
			if json.Unmarshal(f.Payload, &m) != nil {
				p.a.local.Send(sub, proto.NewError(proto.ErrCodeBadFrame, "bad chat_send message"))
				return
			}
			p.typeIn(sub, m.Ref, func(ctx context.Context) error {
				return p.a.local.ChatSend(ctx, sub, m)
			}, func(err error) { p.a.local.Send(sub, session.ChatErrorFrame(err, m.Ref)) })
		case proto.CtlNvimOpen:
			var m proto.NvimOpen
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			// Side by side, as many at once as the editors a connection
			// may hold: Neovim may take seconds to load the person's config.
			if p.nvimOpening.Add(1) > proto.MaxNvimPerSub {
				p.nvimOpening.Add(-1)
				p.a.local.Send(sub, session.NvimRefused(m.ReqID, session.ErrTooManyRequests))
				return
			}
			go func() {
				defer p.nvimOpening.Add(-1)
				p.nvimOpen(sub, m)
			}()
		case proto.CtlNvimInput:
			var m proto.NvimInput
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			// Off the frame loop, in order: Neovim answers when it is
			// done with what it runs.
			p.edit(sub, len(m.Keys), func() {
				if err := p.a.local.NvimInput(sub, m); err != nil {
					p.a.local.Send(sub, session.NvimInputRefused(m.ID, m.Seq, err))
				}
			}, session.NvimInputRefused(m.ID, m.Seq, session.ErrTooManyRequests))
		case proto.CtlNvimClose:
			var m proto.NvimClose
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			// Ending Neovim waits for it to exit.
			p.edit(sub, 0, func() { p.a.local.NvimClose(sub, m) }, nil)
		case proto.CtlNvimSwap:
			var m proto.NvimSwap
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			p.edit(sub, 0, func() {
				if err := p.a.local.NvimSwap(sub, m); err != nil {
					p.a.local.Send(sub, session.NvimRefused("", err))
				}
			}, nil)
		case proto.CtlFileGet:
			var m proto.FileGet
			if json.Unmarshal(f.Payload, &m) != nil {
				return
			}
			p.fileGetIn(sub, m)
		}
	case proto.TypeFileWrite:
		// A save from the editor (design round 12, F6), written on this machine.
		if sub == nil {
			return
		}
		h, part, err := proto.DecodeFileWrite(f.Payload)
		if err != nil {
			// A part that cannot be read names no save to answer: a protocol
			// error, and the viewer is let go with it. No client of
			// Conductor's sends such a part.
			p.refuse(via, "malformed file write")
			return
		}
		p.saveIn(sub, h, part)
	case proto.TypeInput:
		if sub == nil {
			return
		}
		if sub.Role != session.RoleControl {
			p.a.local.Send(sub, inputError(session.ErrReadOnly))
			return
		}
		p.keyIn(sub, f.Payload)
	}
}

// keyIn queues keystrokes for sub on the viewer's keys queue: off the frame
// loop, written in the order sent, each within session.InputWait of its
// arrival (agent.inputDeadline), its wait in the queue included; past that
// it is refused (input_failed) and, if its write had not begun, never
// written. A full queue refuses it at once (too_many_requests). A link's
// viewer's keys wait, within that time, for the host to vouch for its
// server when it may soon (awaitVouch). data is the frame's own: neither
// transport reuses it.
func (p *peer) keyIn(sub *session.Subscription, data []byte) {
	deadline := p.a.inputDeadline()
	if !p.keys.add(request{
		size: len(data),
		run: func() {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			if err := p.awaitVouch(ctx); err != nil {
				p.a.local.Send(sub, inputError(err))
				return
			}
			if err := p.a.local.InputContext(ctx, sub, data); err != nil {
				p.a.local.Send(sub, inputError(err))
			}
		},
		live: p.attached(sub),
	}) {
		p.a.local.Send(sub, queueFull(""))
	}
}

// answerIn queues a viewer's reply to a question (proto.Answer), answering
// its reqId with the session's word (session.AnswerResult): a choice's keys
// on the keys queue, as keystrokes are (keyIn), in their order among them;
// a line on the typing queue, as a submit is (typeIn). Each is written only
// while the question it names still waits, looked at as the session's
// writer takes it, after whatever was queued before it: of two replies to
// one question, from this viewer or another, the second is refused
// (question_answered), nothing written. A view-role viewer's is refused
// (read_only), as its keystrokes are. Either is held to a submission's
// check of the viewer's credential as the session's writer takes it
// (authorize, fresh): a link's viewer's is written only while the link
// stands and the host vouches for its server, else refused (input_failed),
// nothing written.
func (p *peer) answerIn(sub *session.Subscription, m proto.Answer) {
	refuse := func(err error) {
		if f := session.AnswerResult(m.ReqID, err); f != nil {
			p.a.local.Send(sub, f)
		}
	}
	if sub.Role != session.RoleControl {
		refuse(session.ErrReadOnly)
		return
	}
	if m.Text != "" {
		p.typeIn(sub, m.ReqID, func(ctx context.Context) error {
			// Its word comes once its outcome is known: a line the program
			// takes slowly is finished later, and its word comes then.
			_, err := p.a.local.Submit(ctx, session.Submission{Text: m.Text, By: sub, Question: m.Question, Finished: func(res session.SubmitResult, err error) {
				refuse(session.AnswerWord(res, err))
			}})
			if errors.Is(err, session.ErrSubmitPending) {
				return nil
			}
			if err == nil {
				refuse(nil)
				return nil
			}
			return err
		}, refuse)
		return
	}
	deadline := p.a.inputDeadline()
	if !p.keys.add(request{
		size: len(m.Input),
		run: func() {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			if err := p.awaitVouch(ctx); err != nil {
				refuse(err)
				return
			}
			// Keys that land late (the program not reading) get their word
			// then (session.ErrSubmitPending meanwhile, no word).
			refuse(p.a.local.AnswerInputFinished(ctx, sub, m.Question, []byte(m.Input), refuse))
		},
		live: p.attached(sub),
	}) {
		p.a.local.Send(sub, queueFull(m.ReqID))
	}
}

// saveIn queues a part of a save for sub on the viewer's saving queue: off
// the frame loop (for the relay, the host's control connection, whose
// reading it would otherwise hold for the save's disk work), in the order
// sent, one at a time, at most maxQueuedSaveParts and maxQueuedSaveBytes
// waiting. A part past them is refused (a FILE error, too_many_requests,
// naming its save) and the connection stays. A link's viewer's part waits
// for the host to vouch for its server when it may soon (awaitVouch), at
// most a ping's own wait; the session asks the link's check at the part,
// and the save's last part once more before its rename. A part whose viewer
// has gone by its turn is dropped (request.live). part is the frame's own.
func (p *peer) saveIn(sub *session.Subscription, h proto.FileWrite, part []byte) {
	if !p.saving.add(request{
		size: len(part),
		run: func() {
			ctx, cancel := context.WithTimeout(context.Background(), p.a.heardWithin())
			_ = p.awaitVouch(ctx)
			cancel()
			if seam := p.a.beforeSave.Load(); seam != nil {
				(*seam)(h)
			}
			p.a.local.FileWrite(sub, h, part)
		},
		live: p.attached(sub),
	}) {
		if frame, err := proto.EncodeFile(proto.FileHeader{ReqID: h.ReqID, Path: h.Path, Kind: "error",
			Error: &proto.ErrorInfo{Code: proto.ErrCodeTooManyRequests, Message: saveQueueFullWords}}, nil); err == nil {
			p.a.local.Send(sub, frame)
		}
	}
}

// fileGetIn serves a file read for sub (session.FileGet, which reads off
// the frame loop and answers with a FILE frame), or answers why not. A read
// of a viewer who joined through a link while the host does not vouch for
// its server yet (its join's ping not answered) waits for it first, off the
// frame loop, a ping's own wait at most, as the viewer's first line does:
// at most session.MaxInflightFiles such reads wait at once, past which one
// is taken at once (and refused as the host does not vouch).
func (p *peer) fileGetIn(sub *session.Subscription, m proto.FileGet) {
	get := func() {
		if err := p.a.local.FileGet(sub, m); err != nil {
			if frame := session.FileGetRefused(m, err); frame != nil {
				p.a.local.Send(sub, frame)
			}
		}
	}
	if p.cred == nil || p.a.vouches() == nil {
		get()
		return
	}
	if p.filesWaiting.Add(1) > session.MaxInflightFiles {
		p.filesWaiting.Add(-1)
		get()
		return
	}
	go func() {
		defer p.filesWaiting.Add(-1)
		ctx, cancel := context.WithTimeout(context.Background(), p.a.heardWithin())
		_ = p.awaitVouch(ctx)
		cancel()
		get()
	}()
}

// chatLater takes a chat post of a viewer who joined through a link off the
// frame loop while the host does not vouch for its server (the first
// pong of its join's ping not in yet, as after a start, a reconnect or a
// spell with no link held), and reports whether it did: the post goes on
// the typing queue, in its turn among the viewer's submissions and chats to
// the agent, waits there for the host to vouch within its own time
// (typeIn's awaitVouch), and is then kept, and typed when it is for the
// agent, as a post taken at once would be; refused with why otherwise. So
// that the viewer's posts keep their order, those that come while one is
// still on its way take the same road. A viewer with no link, or a post
// that comes once the host vouches with none on its way, is taken at once
// (false).
func (p *peer) chatLater(sub *session.Subscription, m proto.ChatPost) bool {
	if p.cred == nil || (p.chatsLater.Load() == 0 && p.a.vouches() == nil) {
		return false
	}
	p.chatsLater.Add(1)
	p.typeInDone(sub, m.Nonce, func(ctx context.Context) error {
		msg, err := p.a.local.Chat(sub, m)
		if err != nil {
			p.a.local.Send(sub, session.ChatErrorFrame(err, m.Nonce))
			return nil
		}
		if m.To != "" {
			send := proto.ChatSend{T: proto.CtlChatSend, Ref: msg.ID, Scope: msg.Scope, To: m.To}
			if err := p.a.local.ChatSend(ctx, sub, send); err != nil {
				p.a.local.Send(sub, session.ChatErrorFrame(err, msg.ID))
			}
		}
		return nil
	}, func(err error) {
		p.a.local.Send(sub, session.ChatErrorFrame(err, m.Nonce))
	}, func() { p.chatsLater.Add(-1) })
	return true
}

// edit queues one of the editor's requests for sub (keys, a swap answer, a
// close) on the viewer's editing queue: off the frame loop, in the order
// sent. A full queue refuses it at once (too_many_requests).
func (p *peer) edit(sub *session.Subscription, size int, run func(), refused []byte) {
	if !p.editing.add(request{size: size, run: run, live: p.attached(sub)}) {
		if refused == nil {
			refused = session.NvimRefused("", session.ErrTooManyRequests)
		}
		p.a.local.Send(sub, refused)
	}
}

// inputError is the error keystrokes get for err: revoked or expired when
// the viewer's link no longer opens the session (authorize; fresh, the host
// not vouching for its server, among it), with the words the page shows.
func inputError(err error) []byte {
	if code, msg, ok := session.LinkRefusal(err); ok {
		return proto.NewError(code, msg)
	}
	switch {
	case errors.Is(err, session.ErrReadOnly):
		return proto.NewError(proto.ErrCodeReadOnly, "this link is view-only")
	case errors.Is(err, session.ErrSessionEnded):
		return proto.NewError(proto.ErrCodeSessionEnded, "the session has ended")
	case errors.Is(err, session.ErrTooManyRequests):
		return queueFull("")
	case errors.Is(err, session.ErrInputStalled), errors.Is(err, context.DeadlineExceeded):
		return proto.NewError("input_failed", session.ErrInputStalled.Error())
	}
	return proto.NewError("input_failed", "input could not be delivered")
}

// attached says whether sub is still the viewer's attachment: neither let go
// by the peer (its data channel closed, a move to the relay, the viewer
// gone), which the peer does before the session's Detach (that ends the
// viewer's editors first, and may wait for Neovim to exit), nor ended by the
// session (a revoked link, a slow consumer).
func (p *peer) attached(sub *session.Subscription) func() bool {
	return func() bool {
		select {
		case <-sub.Done():
			return false
		default:
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.sub == sub && !p.closed
	}
}

// typeIn queues what types into the agent for sub (a submit, a chat to the
// agent, a chat_send) on the viewer's typing queue. Its time runs from now
// (agent.submitDeadline): do runs in its turn within it, and refuse tells
// the viewer why it was not done, do's error or the time run out, whether
// its turn came or not. A full queue refuses it at once (too_many_requests,
// requestID naming it).
func (p *peer) typeIn(sub *session.Subscription, requestID string, do func(context.Context) error, refuse func(error)) {
	p.typeInDone(sub, requestID, do, refuse, nil)
}

// typeInDone is typeIn with done run once the request is finished,
// whichever way (request.done), or at once when the queue refused it.
func (p *peer) typeInDone(sub *session.Subscription, requestID string, do func(context.Context) error, refuse func(error), done func()) {
	deadline := p.a.submitDeadline()
	// The keys the viewer sent before it are written (or refused) first,
	// as the frame loop wrote them before it once; those sent after are
	// not held back by its pause.
	keysBefore := p.keys.after()
	// Whether it must wait for them is decided now, on the frame loop: the
	// viewer going closes keysBefore too, before or after this request
	// first looks at it.
	keysWait := false
	select {
	case <-keysBefore:
	default:
		keysWait = true
	}
	if !p.typing.add(request{
		run: func() {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			if keysWait {
				// It waits for the keys before it, as one waiting in
				// the queue does: dropped, never typed, if the viewer
				// goes meanwhile (a revoked link's leave among them).
				// One that need not wait goes on when the viewer goes,
				// as a reply box's does.
				select {
				case <-keysBefore:
				case <-ctx.Done():
				}
				if !p.attached(sub)() {
					return
				}
			}
			err := ctx.Err()
			if err == nil {
				err = p.awaitVouch(ctx)
			}
			if err == nil {
				err = do(ctx)
			}
			if err != nil {
				refuse(err)
			}
		},
		live:     p.attached(sub),
		deadline: deadline,
		expired:  func() { refuse(context.DeadlineExceeded) },
		done:     done,
	}) {
		if done != nil {
			done()
		}
		p.a.local.Send(sub, queueFull(requestID))
	}
}

// submitError is the error a submit gets for err.
func submitError(err error) []byte {
	code := "input_failed"
	switch {
	case errors.Is(err, session.ErrReadOnly):
		code = proto.ErrCodeReadOnly
	case errors.Is(err, session.ErrSessionEnded):
		code = proto.ErrCodeSessionEnded
	case errors.Is(err, session.ErrTrustQuestion), errors.Is(err, session.ErrQuestion):
		code = proto.ErrCodeNotSent
	case errors.Is(err, session.ErrTooManyRequests):
		code = proto.ErrCodeTooManyRequests
	}
	return proto.NewError(code, err.Error())
}

// nvimOpen starts the viewer's Neovim on a file.
func (p *peer) nvimOpen(sub *session.Subscription, req proto.NvimOpen) {
	ctx, cancel := context.WithTimeout(context.Background(), nvimOpenTimeout)
	defer cancel()
	if err := p.a.local.NvimOpen(ctx, sub, req); err != nil {
		p.a.local.Send(sub, session.NvimRefused(req.ReqID, err))
	}
}

// attach attaches the viewer for its hello, which came over via: the data
// channel's sink, or nil for the relay. The viewer's attaches run one at a
// time. One whose transport the viewer has left by the time it is attached
// (a move to the relay, its channel gone), or whose viewer has gone, gives
// way: the subscription it made is let go, and kept for the viewer's next
// attach to take over its slot (replaces); the peer's subscription is never
// replaced by it. A hello on a transport the viewer is no longer on, or once
// it is attached, does nothing.
func (p *peer) attach(hello proto.Hello, via *dcSink) {
	p.attaching.Lock()
	defer p.attaching.Unlock()
	p.mu.Lock()
	sink := p.transport(via)
	if sink == nil || p.sub != nil || p.closed {
		p.mu.Unlock()
		return
	}
	replaces := p.replaces
	p.mu.Unlock()
	sub, err := p.a.local.AttachWith(session.AttachOptions{
		ID: p.id, Role: p.role, LinkID: p.linkID, LinkLabel: p.linkLabel, Name: hello.Name, Cols: hello.Cols, Rows: hello.Rows,
		Owner: p.owner, Replaces: replaces, Authorize: p.authorize, Fresh: p.fresh,
	}, sink)
	if err != nil {
		p.a.sendViewerError(p.id, "attach_failed", err.Error())
		return
	}
	p.mu.Lock()
	if p.closed || p.transport(via) != sink {
		if !p.closed {
			// Its slot goes to the viewer's attach on the transport it
			// moved to, as startRelay hands on one attached before.
			p.replaces = sub
		}
		p.mu.Unlock()
		p.a.local.Detach(sub)
		return
	}
	p.sub = sub
	// Handed over: a later attach of this viewer replaces this one.
	p.replaces = nil
	p.mu.Unlock()
}

// authorize is the credential check of the viewer's subscriptions
// (session.AttachOptions.Authorize): the viewer's role, until its link no
// longer opens the session, and why then: the server let the viewer go for
// it (closeFor), said the link went (link_gone: revoked or expired, on the
// server's clock), or the connection to the server was lost
// (linkCreds.lost). The viewer attaches on it alone. The session asks it
// again, with fresh, wherever it acts on what the viewer sent (a
// submission's text and Enter, keys, a save's part and its rename), so what
// is under way of the viewer's, on any of its subscriptions (one it had
// before a move to the relay too), also once it has left, is withdrawn
// then, and a text typed without its Enter holds the session's turn for a
// person. It takes no lock.
func (p *peer) authorize() (session.Grant, error) {
	if why := p.letGo.Load(); why != nil {
		return session.Grant{}, *why
	}
	if err := p.cred.err(); err != nil {
		return session.Grant{}, err
	}
	return session.Grant{Role: p.role}, nil
}

// fresh is the rest of the check of what a viewer who joined through a link
// sends (session.AttachOptions.Fresh): it is acted on only while the host
// vouches for its server (agent.vouches: a ping it sent within the bound
// answered), which would have told it of the link going; serverQuiet, a
// kind of session.ErrRevoked, otherwise. A viewer with no link needs none.
// It takes no lock but the connection's own.
func (p *peer) fresh() error {
	if p.cred == nil {
		return nil
	}
	return p.a.vouches()
}

// awaitVouch has a request of a viewer who joined through a link wait,
// within ctx, for the host to vouch for its server when it does not yet and
// may soon (agent.awaitVouch: its join's ping under way). When ctx ends
// first and the host still does not vouch, it returns why (serverQuiet), so
// that the request is refused for that rather than for its time; nil
// otherwise, the session's own checks to come. A viewer with no link waits
// for nothing.
func (p *peer) awaitVouch(ctx context.Context) error {
	if p.cred == nil || p.cred.err() != nil || p.letGo.Load() != nil {
		// No link, or one gone: nothing to wait for.
		return nil
	}
	p.a.awaitVouch(ctx)
	if ctx.Err() != nil {
		return p.fresh()
	}
	return nil
}

// transport is the sink a hello that came over via attaches the viewer to:
// the relay's, for one over the relay (via nil) once the viewer is on it;
// the data channel's, for one over it while it is the viewer's transport;
// nil for any other. The caller holds p.mu.
func (p *peer) transport(via *dcSink) session.Sink {
	if via == nil {
		if p.relay == nil {
			return nil
		}
		return p.relay
	}
	if p.relay != nil || p.dc != via {
		return nil
	}
	return via
}

func (p *peer) detachDataChannel() {
	p.mu.Lock()
	sub := p.sub
	if p.relay == nil {
		// The channel is no longer the viewer's transport: an attach over
		// it still under way gives way.
		p.dc = nil
		p.sub = nil
		// A move to the relay after this takes over its slot while its
		// transport closes (as startRelay notes it when it detaches first).
		if sub != nil {
			p.replaces = sub
		}
	} else {
		sub = nil
	}
	p.mu.Unlock()
	if sub != nil {
		p.a.local.Detach(sub)
	}
}

// close lets the viewer go, for no reason of its link's (it left).
func (p *peer) close() { p.closeFor(nil) }

// refuse lets the viewer go for a protocol error (bad_frame, msg), once:
// through the server where there is one (sendViewerError: the server
// closes the viewer with the error and tells this host it left); for a
// peer no server serves (a paste invite's) the error is written on its
// data channel via, the channel drained of what it holds within
// dcDrainWait, so that the error is sent before the close aborts the
// connection, and the peer closed here, a save under way dropped with it.
// Frames of the viewer's from now on are passed over (handle), whichever
// way the close reaches it.
func (p *peer) refuse(via *dcSink, msg string) {
	if p.a.sendViewerError(p.id, proto.ErrCodeBadFrame, msg) {
		return
	}
	if !p.erred.CompareAndSwap(false, true) {
		return
	}
	if via != nil {
		if via.WriteFrame(proto.NewError(proto.ErrCodeBadFrame, msg)) == nil {
			via.drain(time.Now().Add(dcDrainWait))
		}
	}
	p.close()
}

// closeFor lets the viewer go: for why, its link no longer opening the
// session (session.ErrRevoked, session.ErrExpired), or nil for any other
// end. What waits of the viewer's (its keys, submissions, save parts and
// editor requests not begun) is dropped either way; with why, what is under
// way of a submission's is withdrawn too (authorize), not finished, its
// text typed or not, and a save under way is refused at its next check.
// Why is set first, before anything of the viewer's sees it go.
func (p *peer) closeFor(why error) {
	if why != nil {
		p.letGo.CompareAndSwap(nil, &why)
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	pc, sub, relay := p.pc, p.sub, p.relay
	p.pc, p.sub = nil, nil
	p.mu.Unlock()
	close(p.left)
	p.typing.close()
	p.keys.close()
	p.editing.close()
	p.saving.close()
	if sub != nil {
		p.a.local.Detach(sub)
	}
	if relay != nil {
		// A frame of the viewer's waiting for its turn on the control
		// connection is withdrawn; after the Detach, which ends the
		// subscription as the viewer leaving, not as a failed write.
		relay.let()
	}
	if pc != nil {
		_ = pc.Close()
	}
}
