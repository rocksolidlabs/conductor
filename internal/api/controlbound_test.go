package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// pastControl are a submit and an answer whose lines are within
// proto.MaxSubmit and pass proto.MaxControl once encoded: MaxSubmit
// quotation marks (two bytes each), and MaxSubmit control characters (six).
func pastControl() [][]byte {
	return [][]byte{
		proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: "s1", Text: strings.Repeat(`"`, proto.MaxSubmit)}),
		proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: "q", Text: strings.Repeat("\x01", proto.MaxSubmit)}),
	}
}

// expectTooLarge reads until w's next error and requires the words of a
// control past its bound: bad_frame, ControlTooLargeWords, no request named
// (the frame is not read).
func expectTooLarge(t *testing.T, w *wsClient) {
	t.Helper()
	if m := w.expectControl(proto.CtlError); m["code"] != proto.ErrCodeBadFrame || m["message"] != proto.ControlTooLargeWords || m["requestId"] != nil {
		t.Fatalf("a line past the control bound: %v", m)
	}
}

// A line past MaxControl as encoded, to a server's own session, is told
// why in words, nothing of it typed, and the connection goes on: a line
// that fits is typed and entered after it as before.
func TestALinePastTheControlBoundIsToldInWords(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	a := dialViewer(t, e, id, adminToken)
	a.hello(0, 0)
	a.expectControl(proto.CtlReady)
	watch := dialViewer(t, e, id, adminToken)
	watch.hello(0, 0)
	watch.expectControl(proto.CtlReady)
	for _, f := range pastControl() {
		a.send(f)
		expectTooLarge(t, a)
	}
	a.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: "s2", Text: "fits " + strings.Repeat(`"`, 100)}))
	if m := a.verdict("s2"); m["t"] != proto.CtlSubmitOK {
		t.Fatalf("a line that fits, after: %v", m)
	}
	if out := watch.outputUntil("fits"); strings.Contains(out, strings.Repeat(`"`, 200)) {
		t.Fatalf("the line past the bound was typed: %q", out)
	}
}

// A relayed viewer of a hosted session is told the same, by the server,
// and nothing of the frame reaches the host; a line that fits is relayed
// after it as before.
func TestARelayedLinePastTheControlBoundIsToldInWords(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	host := dialFakeHost(t, e, "hosted-agent-token")
	tok := host.link("control", "")
	tap := tapRelays(t, host)
	v := dialViewer(t, e, host.sessionID, tok)
	vid := joinRelayed(t, host, v)
	for _, f := range pastControl() {
		v.send(f)
		expectTooLarge(t, v)
	}
	v.send(proto.MustControl(proto.Submit{T: proto.CtlSubmit, ReqID: "s2", Text: "fits"}))
	var got []proto.Submit
	if !waitFor(10*time.Second, func() bool {
		got = got[:0]
		for _, f := range tap.from(vid) {
			var m proto.Submit
			if f.Type == proto.TypeControl && json.Unmarshal(f.Payload, &m) == nil && m.T == proto.CtlSubmit {
				got = append(got, m)
			}
		}
		return len(got) > 0
	}) {
		t.Fatal("the line that fits was not relayed")
	}
	if len(got) != 1 || got[0].ReqID != "s2" || got[0].Text != "fits" {
		t.Fatalf("relayed to the host: %+v", got)
	}
}

// A relayed viewer's control past its bound is charged to its address's
// relay budget at the length read, before it is answered, as a frame
// within its bound is charged before it is relayed: with the bucket's
// clock held, each takes exactly its own length.
func TestARelayedLinePastTheControlBoundIsChargedToTheBudget(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true; viewerBudget(c) })
	host := dialFakeHost(t, e, "hosted-agent-token")
	tok := host.link("control", "")
	tap := tapRelays(t, host)
	v := dialViewer(t, e, host.sessionID, tok)
	vid := joinRelayed(t, host, v)
	b := budgetOf(t, e, "127.0.0.1")
	b.mu.Lock()
	still := b.last
	b.now = func() time.Time { return still }
	b.mu.Unlock()
	held := func() float64 {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.refill(b.clock())
		return b.tokens
	}
	before := held()
	past := pastControl()[0]
	v.send(past)
	expectTooLarge(t, v)
	if got := before - held(); got != float64(len(past)) {
		t.Fatalf("a control of %d bytes past its bound took %v of the budget", len(past), got)
	}
	// An INPUT frame as long, within its own bound, takes the same as it is
	// read, and the relay envelope it brings past that as it is relayed.
	before = held()
	keys := proto.Encode(proto.TypeInput, []byte(strings.Repeat("k", len(past)-1)))
	v.send(keys)
	if !waitFor(10*time.Second, func() bool { return tap.inputBytes(vid) == len(keys)-1 }) {
		t.Fatal("the keys were not relayed")
	}
	if got, want := before-held(), float64(len(keys)+1+proto.ViewerIDLen); got != want {
		t.Fatalf("an input of %d bytes took %v of the budget, not %v", len(keys), got, want)
	}
}
