package hostagent

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A viewer that came with a link is judged by the publisher's own link
// store too (Decisions.LinkEnded), at its attach and at each act: a link
// the publisher holds as ended is refused revoked, with the words a revoke
// gives, and nothing of its viewer's is written. A host with no Decisions
// (`conductor host`) asks nothing, and a viewer with no link is never
// asked about.

// endedHost is a host of a session on a FakeProc whose publisher answers
// with d (nil: none), its server heard from all along. viewer attaches a
// viewer over a data channel of its own, joined through linkID ("" for
// one of the owner's windows); join only connects one and says its hello,
// the refusal of its attach coming on refused.
func endedHost(t *testing.T, d Decisions) (proc *sessiontest.FakeProc, viewer func(id, linkID string) *submitViewer, join func(id, linkID string) *submitViewer, refused <-chan proto.ViewerError) {
	t.Helper()
	a, proc, begun := newHostOf(t, editOpts(), 0, 0)
	a.opts.Decisions = d
	r := &signalRouter{to: map[string]chan any{}}
	errs := make(chan proto.ViewerError, 4)
	a.sendHook = func(v any) {
		if m, ok := v.(proto.ViewerError); ok {
			errs <- m
			return
		}
		r.send(v)
	}
	heardFromServer(t, a)
	viewer = func(id, linkID string) *submitViewer {
		t.Helper()
		return submitViewerOn(t, a, proc, begun, id, linkID, r.add(id))
	}
	join = func(id, linkID string) *submitViewer {
		t.Helper()
		p := newPeer(a, id, session.RoleControl, linkID, "", false)
		p.cred = a.creds.of(linkID)
		a.mu.Lock()
		a.peers[id] = p
		a.mu.Unlock()
		if err := p.startWebRTC(nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		dc, frames := loopbackViewer(t, p, r.add(id))
		v := &submitViewer{t: t, p: p, proc: proc, dc: dc, frames: frames, begun: begun}
		v.send(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24, Name: "Nate"})
		return v
	}
	return proc, viewer, join, errs
}

// A viewer that joins with a link the publisher holds as ended is refused
// at its attach, revoked with a revoke's words, the publisher asked; it is
// acted on no more while the switchyard closes it, and the process's next
// write is the owner's.
func TestALinkThePublisherHoldsEndedIsRefusedAtTheAttach(t *testing.T) {
	d := &fakeDecisions{}
	d.ended.Store("l1", true)
	proc, viewer, join, refused := endedHost(t, d)
	guest := join("0123456789abcdef", "l1")
	select {
	case m := <-refused:
		if m.ViewerID != guest.p.id || m.Code != proto.ErrCodeRevoked || m.Message != goneWords {
			t.Fatalf("the attach was refused with %+v, want revoked %q", m, goneWords)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the attach was never refused")
	}
	if d.endedAsks.Load() == 0 {
		t.Fatal("the publisher was not asked at the attach")
	}
	guest.key("g")
	owner := viewer("fedcba9876543210", "")
	owner.key("o")
	if got := typedNext(t, proc); got != "o" {
		t.Fatalf("the process was written %q before the owner's keys", got)
	}
}

// A viewer attached while its link stood is refused at its next act once
// the publisher holds the link as ended: its keys are refused revoked,
// with a revoke's words, and never written; the owner's go on.
func TestALinkThePublisherHoldsEndedIsRefusedAtTheNextAct(t *testing.T) {
	d := &fakeDecisions{}
	proc, viewer, _, _ := endedHost(t, d)
	guest := viewer("0123456789abcdef", "l1")
	owner := viewer("fedcba9876543210", "")
	guest.key("a")
	if got := typedNext(t, proc); got != "a" {
		t.Fatalf("the standing link's keys were written as %q", got)
	}
	asked := d.endedAsks.Load()
	d.ended.Store("l1", true)
	guest.key("b")
	refusedFor(t, guest.first(func(m map[string]any) bool { return m["t"] == proto.CtlError }), proto.ErrCodeRevoked, goneWords)
	if d.endedAsks.Load() <= asked {
		t.Fatal("the publisher was not asked at the act")
	}
	owner.key("o")
	if got := typedNext(t, proc); got != "o" {
		t.Fatalf("the process was written %q before the owner's keys", got)
	}
}

// A host with no Decisions asks nothing: a link's viewer attaches and
// types as the switchyard's word has it. With Decisions, a viewer with no
// link is never asked about.
func TestOnlyALinksViewerIsAskedAboutAndOnlyWithDecisions(t *testing.T) {
	t.Run("no decisions", func(t *testing.T) {
		proc, viewer, _, _ := endedHost(t, nil)
		guest := viewer("0123456789abcdef", "l1")
		guest.key("a")
		if got := typedNext(t, proc); got != "a" {
			t.Fatalf("the link's keys were written as %q", got)
		}
	})
	t.Run("no link", func(t *testing.T) {
		d := &fakeDecisions{}
		proc, viewer, _, _ := endedHost(t, d)
		owner := viewer("fedcba9876543210", "")
		owner.key("o")
		if got := typedNext(t, proc); got != "o" {
			t.Fatalf("the owner's keys were written as %q", got)
		}
		if n := d.endedAsks.Load(); n != 0 {
			t.Fatalf("the publisher was asked %d times about a viewer with no link", n)
		}
	})
}
