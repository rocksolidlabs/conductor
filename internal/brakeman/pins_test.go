package brakeman

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A capture's ref (Program item PV1): the live capture and a send's
// frozen document parse, print back as written, and hold their parts;
// any other shape is refused.
func TestACaptureRefParsesAndPrints(t *testing.T) {
	id, send := "0123456789abcdef", "fedcba9876543210"
	for s, want := range map[string]Ref{
		"capture:" + id:              {Kind: RefCapture, Capture: id},
		"capture:" + id + "/" + send: {Kind: RefCapture, Capture: id, Send: send},
	} {
		r, err := ParseRef(s)
		if err != nil || r != want || r.String() != s || r.Valid() != nil {
			t.Errorf("%s: %+v %v, printed %q", s, r, err, r.String())
		}
		b, err := r.MarshalText()
		var back Ref
		if err != nil || back.UnmarshalText(b) != nil || back != r {
			t.Errorf("%s through text: %s %v %+v", s, b, err, back)
		}
	}
	for _, s := range []string{"capture:", "capture:" + strings.ToUpper(id), "capture:" + id[:15], "capture:" + id + "/", "capture:" + id + "/" + send + "/1",
		"capture:" + id + "/" + send[:15], "capture:" + id + "//" + send} {
		if _, err := ParseRef(s); err == nil || !strings.Contains(err.Error(), "want capture:<id> or capture:<id>/<send>") {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := (Ref{Kind: RefCapture, Capture: id, Run: "fix-login-3f2a91c0"}).Valid(); err == nil {
		t.Error("a capture ref holding a run is valid")
	}
	// An ask takes it among its subjects, as any ref.
	in := Request{OpID: "op-1", Kind: KindAsk, Note: "why is it red?", Subjects: []Ref{{Kind: RefCapture, Capture: id, Send: send}}}
	if err := in.checkAsked(); err != nil {
		t.Fatal(err)
	}
}

// askPinned is a pinned ask under op.
func askPinned(op string) Request {
	return Request{OpID: op, Kind: KindAsk, Note: "what broke?", Pin: true}
}

// A pinned op id outlives the window of MaxOps: past a thousand later
// requests a repeat of it finds its request and never makes a second;
// released, it is trimmed with the others at the next write.
func TestAPinnedOpOutlivesTheWindow(t *testing.T) {
	f := newFixture(t)
	first, created, err := f.s.Create(askPinned("send-1"))
	if err != nil || !created {
		t.Fatal(created, err)
	}
	unpinned := f.create("plain-0")
	// A thousand later requests' op ids, as state.json keeps them, then
	// one request more made: its write trims the op ids to the window.
	f.s.mu.Lock()
	st, err := f.s.loadState()
	if err == nil {
		for i := range MaxOps {
			st.Last++
			st.Ops = append(st.Ops, opRef{OpID: fmt.Sprintf("later-%d", i), ID: requestID(st.Last), Sum: fmt.Sprintf("%064x", i)})
		}
		err = f.s.saveState(st)
	}
	f.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.create("latest")
	if _, found, err := f.s.ByOp(unpinned.OpID); found || err != nil {
		t.Fatalf("an unpinned op id past the window: %v %v", found, err)
	}
	f.reopen()
	r, found, err := f.s.ByOp("send-1")
	if !found || err != nil || r.ID != first.ID {
		t.Fatalf("the pinned op id: %s %v %v", r.ID, found, err)
	}
	if again, created, err := f.s.Create(askPinned("send-1")); err != nil || created || again.ID != first.ID {
		t.Fatalf("the repeat: %s %v %v", again.ID, created, err)
	}
	if pins, err := f.s.Pinned(); err != nil || len(pins) != 1 || pins[0] != (PinnedOp{OpID: "send-1", ID: first.ID}) {
		t.Fatalf("pinned: %+v %v", pins, err)
	}
	if err := f.s.Release("send-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Release("send-1"); err != nil {
		t.Fatalf("a second release: %v", err)
	}
	if pins, err := f.s.Pinned(); err != nil || len(pins) != 0 {
		t.Fatalf("pinned after the release: %+v %v", pins, err)
	}
	if _, found, err := f.s.ByOp("send-1"); found || err != nil {
		t.Fatalf("a released op id past the window: %v %v", found, err)
	}
}

// A pinned op id whose request Prune removed answers ErrPruned with the
// request's number, and a repeat of it is refused pruned: the request is
// never made twice.
func TestAPinnedPrunedOpAnswersItsNumber(t *testing.T) {
	keepNewest = 0
	t.Cleanup(func() { keepNewest = Keep })
	f := newFixture(t)
	first, _, err := f.s.Create(askPinned("send-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.close(first.ID, StateCancelled)
	if n, err := f.s.Prune(); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	f.reopen()
	r, found, err := f.s.ByOp("send-1")
	if found || !errors.Is(err, ErrPruned) || r.ID != first.ID {
		t.Fatalf("the pinned pruned op id: %+v %v %v", r, found, err)
	}
	if _, created, err := f.s.Create(askPinned("send-1")); created || !errors.Is(err, ErrPruned) {
		t.Fatalf("the repeat: %v %v", created, err)
	}
	if pins, err := f.s.Pinned(); err != nil || len(pins) != 1 || !pins[0].Pruned {
		t.Fatalf("pinned: %+v %v", pins, err)
	}
	// An unpinned one pruned is not found, as before the pins.
	plain := f.create("plain-1")
	f.close(plain.ID, StateCancelled)
	if _, err := f.s.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.s.ByOp("plain-1"); found || err != nil {
		t.Fatalf("an unpinned pruned op id: %v %v", found, err)
	}
}

// At MaxPinnedOps pinned op ids a new pinned request is refused
// pins_full and nothing is written; a repeat of a pinned one is still
// answered, an unpinned request still made, and one released makes room.
func TestPinsAreBounded(t *testing.T) {
	f := newFixture(t)
	f.s.mu.Lock()
	st, err := f.s.loadState()
	if err == nil {
		st.Last = MaxPinnedOps
		for i := range MaxPinnedOps {
			st.Ops = append(st.Ops, opRef{OpID: fmt.Sprintf("pin-%d", i), ID: requestID(int64(i + 1)), Sum: fmt.Sprintf("%064x", i), Pruned: true, Pinned: true})
		}
		err = f.s.saveState(st)
	}
	f.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.s.Create(askPinned("send-new")); created || !errors.Is(err, ErrPinsFull) {
		t.Fatalf("a pin past the bound: %v %v", created, err)
	}
	if _, found, err := f.s.ByOp("send-new"); found || err != nil {
		t.Fatalf("the refused pin wrote its op id: %v %v", found, err)
	}
	if page, _, err := f.s.List("", MaxPage); err != nil || len(page) != 0 {
		t.Fatalf("a request was made: %v %v", ids(page), err)
	}
	if _, _, err := f.s.ByOp("pin-7"); !errors.Is(err, ErrPruned) {
		t.Fatalf("a pinned op id at the bound: %v", err)
	}
	f.create("plain-1")
	if err := f.s.Release("pin-7"); err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.s.Create(askPinned("send-new")); err != nil || !created {
		t.Fatalf("a pin once one was released: %v %v", created, err)
	}
	if pins, err := f.s.Pinned(); err != nil || len(pins) != MaxPinnedOps {
		t.Fatalf("%d pinned, %v", len(pins), err)
	}
}
