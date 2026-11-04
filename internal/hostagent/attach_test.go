package hostagent

import (
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// joinGate holds the first join the session records, and with it the attach
// that records it (the subscription registered, the attach not finished),
// until release is closed; held is closed once it waits.
type joinGate struct {
	held    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newJoinGate() *joinGate {
	return &joinGate{held: make(chan struct{}), release: make(chan struct{})}
}

func (g *joinGate) hook(_ string, e session.ActivityEntry, _ session.AttentionState) {
	if e.Type != session.ActivityJoin {
		return
	}
	first := false
	g.once.Do(func() { first = true })
	if first {
		close(g.held)
		<-g.release
	}
}

func (g *joinGate) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.held:
	case <-time.After(10 * time.Second):
		t.Fatal("the attach never recorded its join")
	}
}

// relayAgent is a host whose control connection is up and read, so that a
// relayed viewer's frames go out, and whose session runs cat with the
// join gate's hook and maxViewers (the default when 0).
func relayAgent(t *testing.T, gate *joinGate, maxViewers int) *agent {
	t.Helper()
	srv, conn, pc := dialHeld(t, 0)
	close(srv.read)
	go func() {
		for {
			select {
			case <-srv.got:
			case <-t.Context().Done():
				return
			}
		}
	}()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	local := session.NewLocal(session.Info{ID: "s", Cols: 80, Rows: 24}, proc, session.Options{OnActivity: gate.hook, Log: discardLog, MaxViewers: maxViewers})
	a := &agent{opts: Options{}, local: local, peers: map[string]*peer{}, log: discardLog, sessID: "s"}
	a.useConn(conn, pc)
	return a
}

// waitViewers waits until the session holds want viewers, and fails with
// how many it holds when it does not within a second.
func waitViewers(t *testing.T, local *session.Local, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for local.Viewers() != want {
		if time.Now().After(deadline) {
			t.Fatalf("the session holds %d viewers, want %d", local.Viewers(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func helloFrame(name string) proto.Frame {
	return proto.Frame{Type: proto.TypeControl, Payload: mustJSON(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24, Name: name})}
}

// A viewer whose hello over its data channel is still attaching when it
// moves to the relay and says hello there: the relay's attach is the
// viewer's, in the slot the channel's held (at the cap, one viewer), the
// channel's lets go of what it attached, and once the viewer leaves the
// session holds nothing of it.
func TestAnAttachUnderWayOnTheChannelGivesWayToTheRelays(t *testing.T) {
	gate := newJoinGate()
	a := relayAgent(t, gate, 1)
	out := make(chan any, 64)
	var mu sync.Mutex
	var refused []string
	a.sendHook = func(v any) {
		if e, ok := v.(proto.ViewerError); ok {
			mu.Lock()
			refused = append(refused, e.Code+": "+e.Message)
			mu.Unlock()
			return
		}
		out <- v
	}
	const id = "0123456789abcdef"
	p := newPeer(a, id, session.RoleControl, "", "", true)
	a.peers[id] = p
	if err := p.startWebRTC(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	dc, _ := loopbackViewer(t, p, out)
	if err := dc.Send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 80, Rows: 24, Name: "v"})); err != nil {
		t.Fatal(err)
	}
	gate.waitHeld(t)

	// The viewer moves to the relay meanwhile, and says hello there.
	p.startRelay()
	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		p.handleFrame(helloFrame("v"))
	}()
	select {
	case <-relayed:
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)
	select {
	case <-relayed:
	case <-time.After(10 * time.Second):
		t.Fatal("the relay's hello was never handled")
	}
	// Time for the channel's attach to finish after it.
	time.Sleep(300 * time.Millisecond)
	waitViewers(t, a.local, 1)
	if !holdsSub(p) {
		t.Fatal("the viewer holds no subscription on the relay")
	}
	mu.Lock()
	if len(refused) != 0 {
		t.Errorf("the relay's attach was refused: %q", refused)
	}
	mu.Unlock()

	p.close()
	waitViewers(t, a.local, 0)
}

// A viewer that leaves while its attach is under way leaves nothing
// attached: the attach finds it gone and lets go of what it attached.
func TestAViewerThatLeavesWhileItsAttachIsUnderWayLeavesNothingAttached(t *testing.T) {
	gate := newJoinGate()
	a := relayAgent(t, gate, 0)
	a.sendHook = func(any) {}
	const id = "0123456789abcdef"
	p := newPeer(a, id, session.RoleControl, "", "", true)
	a.peers[id] = p
	p.startRelay()
	attached := make(chan struct{})
	go func() {
		defer close(attached)
		p.handleFrame(helloFrame("v"))
	}()
	gate.waitHeld(t)
	// What the session sent the viewer as it attached has gone out, so the
	// relay going does not end the subscription with a failed write.
	deadline := time.Now().Add(5 * time.Second)
	for !a.local.Drained() {
		if time.Now().After(deadline) {
			t.Fatal("the attach's frames never went out")
		}
		time.Sleep(5 * time.Millisecond)
	}

	p.close() // the viewer leaves (viewer_leave)
	close(gate.release)
	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("the attach never finished")
	}
	waitViewers(t, a.local, 0)
	if holdsSub(p) {
		t.Fatal("the viewer that left holds a subscription")
	}
}
