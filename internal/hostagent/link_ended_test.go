package hostagent

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A viewer that came with a link is judged by the publisher's own link
// store too (Decisions.LinkEnded), at its attach and at each act, a resize
// among them: a link the publisher holds as ended is refused revoked or
// expired, as the store says, in the words a page shows, and nothing of
// its viewer's is written; one it holds is granted until its expiry there,
// the session ending the viewer then. A host with no Decisions (`conductor
// host`) asks nothing, a viewer with no link is never asked about, and an
// attach refused for anything else is answered as it always was.

// endedHost is a host of a session on a FakeProc whose publisher answers
// with d (nil: none), its server heard from all along, its session taking
// opts. viewer attaches a viewer over a data channel of its own, joined
// through linkID ("" for one of the owner's windows); peerOf only connects
// one, whose hello is the test's to send (hello), the refusal of its
// attach coming on refused.
type endedHost struct {
	t       *testing.T
	a       *agent
	proc    *sessiontest.FakeProc
	viewer  func(id, linkID string) *submitViewer
	refused chan proto.ViewerError
	r       *signalRouter
}

func newEndedHost(t *testing.T, d Decisions, opts session.Options) *endedHost {
	t.Helper()
	a, proc, begun := newHostOf(t, opts, 0, 0)
	a.opts.Decisions = d
	h := &endedHost{t: t, a: a, proc: proc, refused: make(chan proto.ViewerError, 4), r: &signalRouter{to: map[string]chan any{}}}
	a.sendHook = func(v any) {
		if m, ok := v.(proto.ViewerError); ok {
			h.refused <- m
			return
		}
		h.r.send(v)
	}
	heardFromServer(t, a)
	h.viewer = func(id, linkID string) *submitViewer {
		t.Helper()
		return submitViewerOn(t, a, proc, begun, id, linkID, h.r.add(id))
	}
	return h
}

// peerOf connects viewer id, joined through linkID with the credential
// cred makes, and says no hello.
func (h *endedHost) peerOf(id, linkID string, cred func() *linkCred) *submitViewer {
	h.t.Helper()
	p := newPeer(h.a, id, session.RoleControl, linkID, "", false)
	p.cred = cred()
	h.a.mu.Lock()
	h.a.peers[id] = p
	h.a.mu.Unlock()
	if err := p.startWebRTC(nil); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(p.close)
	dc, frames := loopbackViewer(h.t, p, h.r.add(id))
	return &submitViewer{t: h.t, p: p, proc: h.proc, dc: dc, frames: frames}
}

// join connects viewer id through linkID and says its hello.
func (h *endedHost) join(id, linkID string) *submitViewer {
	h.t.Helper()
	v := h.peerOf(id, linkID, func() *linkCred { return h.a.creds.of(linkID) })
	v.hello()
	return v
}

// hello says the viewer's hello.
func (v *submitViewer) hello() {
	v.t.Helper()
	v.send(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24, Name: "Nate"})
}

// refusal is the refusal of an attach, said to the server.
func (h *endedHost) refusal(of *submitViewer) proto.ViewerError {
	h.t.Helper()
	select {
	case m := <-h.refused:
		if m.ViewerID != of.p.id {
			h.t.Fatalf("a refusal for %s, want %s", m.ViewerID, of.p.id)
		}
		return m
	case <-time.After(10 * time.Second):
		h.t.Fatal("the attach was never refused")
	}
	return proto.ViewerError{}
}

// The store's two answers for a link ended there, and their words.
var endedHere = []struct {
	name        string
	why         error
	code, words string
}{
	{"revoked", session.ErrRevoked, proto.ErrCodeRevoked, goneWords},
	{"expired", session.ErrExpired, proto.ErrCodeExpired, "this link has expired"},
}

// A viewer that joins with a link the publisher holds as ended is refused
// at its attach, in the store's word, the publisher asked; it is acted on
// no more while the switchyard closes it, and the process's next write is
// the owner's.
func TestALinkThePublisherHoldsEndedIsRefusedAtTheAttach(t *testing.T) {
	for _, c := range endedHere {
		t.Run(c.name, func(t *testing.T) {
			d := &fakeDecisions{}
			d.ended.Store("l1", c.why)
			h := newEndedHost(t, d, editOpts())
			guest := h.join("0123456789abcdef", "l1")
			if m := h.refusal(guest); m.Code != c.code || m.Message != c.words {
				t.Fatalf("the attach was refused with %+v, want %s %q", m, c.code, c.words)
			}
			if d.endedAsks.Load() == 0 {
				t.Fatal("the publisher was not asked at the attach")
			}
			guest.key("g")
			owner := h.viewer("fedcba9876543210", "")
			owner.key("o")
			if got := typedNext(t, h.proc); got != "o" {
				t.Fatalf("the process was written %q before the owner's keys", got)
			}
		})
	}
}

// A viewer attached while its link stood is refused at its next act once
// the publisher holds the link as ended, in the store's word: its keys are
// never written, and a resize it asks for (Fit to my window) leaves the
// process's size as it was, refused as its keys are; the owner's go on.
func TestALinkThePublisherHoldsEndedIsRefusedAtTheNextAct(t *testing.T) {
	for _, c := range endedHere {
		t.Run(c.name, func(t *testing.T) {
			d := &fakeDecisions{}
			h := newEndedHost(t, d, editOpts())
			guest := h.viewer("0123456789abcdef", "l1")
			owner := h.viewer("fedcba9876543210", "")
			guest.key("a")
			if got := typedNext(t, h.proc); got != "a" {
				t.Fatalf("the standing link's keys were written as %q", got)
			}
			guest.send(proto.Resize{T: proto.CtlResize, Cols: 90, Rows: 30, Take: true})
			guest.sync()
			if cols, rows := h.proc.Size(); cols != 90 || rows != 30 {
				t.Fatalf("the standing link's resize made %dx%d", cols, rows)
			}
			asked := d.endedAsks.Load()
			d.ended.Store("l1", c.why)
			guest.key("b")
			refusedFor(t, guest.first(func(m map[string]any) bool { return m["t"] == proto.CtlError }), c.code, c.words)
			if d.endedAsks.Load() <= asked {
				t.Fatal("the publisher was not asked at the act")
			}
			guest.send(proto.Resize{T: proto.CtlResize, Cols: 99, Rows: 33, Take: true})
			refusedFor(t, guest.first(func(m map[string]any) bool { return m["t"] == proto.CtlError }), c.code, c.words)
			if cols, rows := h.proc.Size(); cols != 90 || rows != 30 {
				t.Fatalf("the process was sized %dx%d by a viewer whose link ended", cols, rows)
			}
			owner.key("o")
			if got := typedNext(t, h.proc); got != "o" {
				t.Fatalf("the process was written %q before the owner's keys", got)
			}
		})
	}
}

// A link the publisher holds is granted until its expiry there: a viewer
// admitted while the store still held it (its clock behind the session's)
// is ended by the session at that expiry, told expired over its transport,
// and its keys are never written.
func TestALinkThePublisherHoldsIsGrantedUntilItsExpiry(t *testing.T) {
	d := &fakeDecisions{}
	d.until.Store("l1", time.Now().Add(-time.Second))
	h := newEndedHost(t, d, editOpts())
	guest := h.join("0123456789abcdef", "l1")
	refusedFor(t, guest.first(func(m map[string]any) bool { return m["t"] == proto.CtlError }), proto.ErrCodeExpired, session.ErrExpired.Error())
	// Its data channel closed after the word, or about to: keys it sends
	// now go nowhere, whether its channel takes them or not.
	_ = guest.dc.Send(proto.Encode(proto.TypeInput, []byte("g")))
	owner := h.viewer("fedcba9876543210", "")
	owner.key("o")
	if got := typedNext(t, h.proc); got != "o" {
		t.Fatalf("the process was written %q before the owner's keys", got)
	}
}

// A host with no Decisions asks nothing: a link's viewer attaches and
// types as the switchyard's word has it. With Decisions, a viewer with no
// link is never asked about.
func TestOnlyALinksViewerIsAskedAboutAndOnlyWithDecisions(t *testing.T) {
	t.Run("no decisions", func(t *testing.T) {
		h := newEndedHost(t, nil, editOpts())
		guest := h.viewer("0123456789abcdef", "l1")
		guest.key("a")
		if got := typedNext(t, h.proc); got != "a" {
			t.Fatalf("the link's keys were written as %q", got)
		}
	})
	t.Run("no link", func(t *testing.T) {
		d := &fakeDecisions{}
		h := newEndedHost(t, d, editOpts())
		owner := h.viewer("fedcba9876543210", "")
		owner.key("o")
		if got := typedNext(t, h.proc); got != "o" {
			t.Fatalf("the owner's keys were written as %q", got)
		}
		if n := d.endedAsks.Load(); n != 0 {
			t.Fatalf("the publisher was asked %d times about a viewer with no link", n)
		}
	})
}

// An attach the publisher's store did not refuse is answered as it always
// was, code and words: a link the switchyard said gone, on a host with no
// Decisions and on one whose store does not hold it; a principal the
// connection's loss let go; a session with no room.
func TestAnAttachTheStoreDidNotRefuseIsAnsweredAsBefore(t *testing.T) {
	cases := []struct {
		name        string
		d           Decisions
		opts        session.Options
		linkID      string
		before      func(h *endedHost)
		code, words string
	}{
		{"no decisions, link gone", nil, editOpts(), "l1", func(h *endedHost) { linkGone(t, h.a, "l1") }, "attach_failed", "session: link revoked"},
		{"a link the store does not hold, gone", &fakeDecisions{}, editOpts(), "l1", func(h *endedHost) { linkGone(t, h.a, "l1") }, "attach_failed", "session: link revoked"},
		{"a principal let go", &fakeDecisions{}, editOpts(), "", func(h *endedHost) { h.a.creds.lost() }, "attach_failed", "session: link revoked"},
		{"no room", &fakeDecisions{}, session.Options{MaxViewers: 1, Log: discardLog}, "l1", func(h *endedHost) { h.viewer("fedcba9876543210", "") }, "attach_failed", session.ErrTooManyViewers.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newEndedHost(t, c.d, c.opts)
			guest := h.peerOf("0123456789abcdef", c.linkID, func() *linkCred {
				if c.linkID == "" {
					return h.a.creds.principal()
				}
				return h.a.creds.of(c.linkID)
			})
			c.before(h)
			guest.hello()
			if m := h.refusal(guest); m.Code != c.code || m.Message != c.words {
				t.Fatalf("the attach was refused with %+v, want %s %q", m, c.code, c.words)
			}
		})
	}
}
