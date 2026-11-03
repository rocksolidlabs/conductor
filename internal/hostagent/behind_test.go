package hostagent

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// behindAgent is an agent on a cat session, connected to a server that
// reads everything it is sent (s.got), with two relayed viewers attached:
// a controller (typist) and a watcher. relayWait is the agent's.
func behindAgent(t *testing.T, relayWait time.Duration) (a *agent, s *heldServer, typist, watcher *peer) {
	t.Helper()
	a, s, typist, watcher, _ = behindAgentProc(t, relayWait)
	return a, s, typist, watcher
}

// behindAgentProc is behindAgent, and the cat's process, which nothing of
// the agent's holds but its session.
func behindAgentProc(t *testing.T, relayWait time.Duration) (a *agent, s *heldServer, typist, watcher *peer, proc *pty.Process) {
	t.Helper()
	s, c, pc := dialHeld(t, 0)
	close(s.read)
	dir := t.TempDir()
	var err error
	proc, err = pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: dir, Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(context.Background(), time.Second) })
	a = &agent{opts: Options{RelayOnly: true}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1", relayWait: relayWait}
	a.local = session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{})
	w := a.useConn(c, pc)
	t.Cleanup(w.stop)
	hello := proto.Hello{T: proto.CtlHello, Proto: proto.ProtoVersion, Cols: 80, Rows: 24}
	for _, v := range []struct {
		id   string
		role session.Role
	}{{"typist0123456789", session.RoleControl}, {"watcher012345678", session.RoleView}} {
		a.addPeer(v.id, v.role, "", "")
		p := a.peer(v.id)
		p.startRelay()
		p.attach(hello, nil)
		p.mu.Lock()
		attached := p.sub != nil
		p.mu.Unlock()
		if !attached {
			t.Fatalf("%s never attached", v.id)
		}
	}
	t.Cleanup(a.closeAllPeers)
	return a, s, a.peer("typist0123456789"), a.peer("watcher012345678"), proc
}

// behind tells a, as the server does, that viewer id is behind or caught up.
func behind(t *testing.T, a *agent, id string, b bool) {
	t.Helper()
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerBehind{T: proto.HostViewerBehind, ViewerID: id, Behind: b})); err != nil {
		t.Fatal(err)
	}
}

// relayedOutput is what s gets relayed to viewer id as output, up to want
// in all, or until wait passes with nothing more for it.
func relayedOutput(t *testing.T, s *heldServer, id string, want string, wait time.Duration) string {
	t.Helper()
	var got []byte
	for !bytes.Contains(got, []byte(want)) {
		select {
		case data := <-s.got:
			if len(data) < 1+proto.ViewerIDLen || data[0] != proto.TypeRelay {
				continue
			}
			viewer, inner, err := proto.DecodeRelay(data[1:])
			if err == nil && viewer == id && inner.Type == proto.TypeOutput {
				got = append(got, inner.Payload...)
			}
		case <-time.After(wait):
			return string(got)
		}
	}
	return string(got)
}

// A relayed viewer the server says is behind is sent nothing more on the
// host's connection: its frames wait for it, while the other viewers' go
// as before. Once the server says it caught up, what waited goes, in order.
func TestAViewerTheServerSaysIsBehindIsSentNothingUntilItCatchesUp(t *testing.T) {
	a, s, typist, watcher := behindAgent(t, time.Minute)
	behind(t, a, watcher.id, true)
	if err := a.local.Input(typist.sub, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	// The typist is sent the echo; the watcher nothing, however long.
	var typistGot, watcherGot []byte
	deadline := time.After(10 * time.Second)
	for !bytes.Contains(typistGot, []byte("one")) {
		select {
		case data := <-s.got:
			if data[0] != proto.TypeRelay {
				continue
			}
			viewer, inner, err := proto.DecodeRelay(data[1:])
			if err != nil || inner.Type != proto.TypeOutput {
				continue
			}
			switch viewer {
			case typist.id:
				typistGot = append(typistGot, inner.Payload...)
			case watcher.id:
				watcherGot = append(watcherGot, inner.Payload...)
			}
		case <-deadline:
			t.Fatalf("the typist was never sent the echo (got %q)", typistGot)
		}
	}
	if len(watcherGot) != 0 {
		t.Fatalf("the watcher, behind, was sent %q", watcherGot)
	}
	if got := relayedOutput(t, s, watcher.id, "one", 300*time.Millisecond); got != "" {
		t.Fatalf("the watcher, behind, was sent %q", got)
	}
	if r := watcher.sub.Reason(); r != nil {
		t.Fatalf("the watcher was let go: %v", r)
	}
	// Caught up: what waited goes, the typed line among it.
	behind(t, a, watcher.id, false)
	if got := relayedOutput(t, s, watcher.id, "one", 10*time.Second); !bytes.Contains([]byte(got), []byte("one")) {
		t.Fatalf("the watcher, caught up, was sent %q", got)
	}
	// And the next output goes to both as it comes.
	if err := a.local.Input(typist.sub, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if got := relayedOutput(t, s, watcher.id, "two", 10*time.Second); !bytes.Contains([]byte(got), []byte("two")) {
		t.Fatalf("the watcher was sent %q", got)
	}
}

// A viewer that stays behind longer than a relayed frame may wait is let go
// alone, as a slow consumer: the server is told why and that it is closed,
// whether it is behind or not, and the other viewers are sent the output.
func TestAViewerBehindLongerThanAFrameMayWaitIsLetGoAlone(t *testing.T) {
	a, s, typist, watcher := behindAgent(t, 300*time.Millisecond)
	behind(t, a, watcher.id, true)
	if err := a.local.Input(typist.sub, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watcher.sub.Done():
		if !errors.Is(watcher.sub.Reason(), session.ErrSlowConsumer) {
			t.Fatalf("the watcher ended: %v", watcher.sub.Reason())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher was never let go")
	}
	if r := typist.sub.Reason(); r != nil {
		t.Fatalf("the typist was let go: %v", r)
	}
	var told, closed bool
	deadline := time.After(10 * time.Second)
	for !closed {
		select {
		case data := <-s.got:
			if data[0] == proto.TypeRelay {
				if viewer, inner, err := proto.DecodeRelay(data[1:]); err == nil && viewer == watcher.id && bytes.Contains(inner.Payload, []byte(proto.ErrCodeSlowConsumer)) {
					told = true
				}
				continue
			}
			if bytes.Contains(data, []byte(`"viewer_closed"`)) && bytes.Contains(data, []byte(watcher.id)) {
				closed = true
			}
		case <-deadline:
			t.Fatalf("the server was never told the watcher is closed (told why: %v)", told)
		}
	}
	if !told {
		t.Fatal("the watcher was never told why")
	}
	if err := a.local.Input(typist.sub, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if got := relayedOutput(t, s, typist.id, "two", 10*time.Second); !bytes.Contains([]byte(got), []byte("two")) {
		t.Fatalf("the typist was sent %q", got)
	}
}

// The session's last output goes to a viewer that is behind as to any:
// once the session has ended, what waited for the viewer to catch up goes,
// as it went before the host held a viewer behind, and the end's drain
// bounds it.
func TestTheSessionsLastOutputGoesToAViewerBehind(t *testing.T) {
	a, s, typist, watcher, proc := behindAgentProc(t, time.Minute)
	behind(t, a, watcher.id, true)
	if err := a.local.Input(typist.sub, []byte("last words\n")); err != nil {
		t.Fatal(err)
	}
	if got := relayedOutput(t, s, typist.id, "last words", 10*time.Second); !bytes.Contains([]byte(got), []byte("last words")) {
		t.Fatalf("the typist was sent %q", got)
	}
	if got := relayedOutput(t, s, watcher.id, "last words", 300*time.Millisecond); got != "" {
		t.Fatalf("the watcher, behind, was sent %q", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := proc.Stop(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := relayedOutput(t, s, watcher.id, "last words", 10*time.Second); !bytes.Contains([]byte(got), []byte("last words")) {
		t.Fatalf("the watcher, behind as the session ended, was sent %q", got)
	}
}

// viewer_behind names one viewer: one of another length is a protocol
// error; one the host does not have (it left) changes nothing.
func TestViewerBehindNamesAViewer(t *testing.T) {
	a := &agent{opts: Options{RelayOnly: true}, peers: map[string]*peer{}, log: discardLog}
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerBehind{T: proto.HostViewerBehind, ViewerID: "short", Behind: true})); err == nil {
		t.Fatal("a viewer_behind with a short id was taken")
	}
	if err := a.handleControl(t.Context(), []byte(`{"t":"viewer_behind","viewerId":"0123456789abcdef","behind":"yes"}`)); err == nil {
		t.Fatal("an unreadable viewer_behind was taken")
	}
	if err := a.handleControl(t.Context(), mustJSON(proto.ViewerBehind{T: proto.HostViewerBehind, ViewerID: "0123456789abcdef", Behind: true})); err != nil {
		t.Fatalf("a viewer_behind for a viewer that left: %v", err)
	}
}

// The room a viewer's frames wait for: set and caught up again any number
// of times, each the same as once; a wait ends as the viewer catches up,
// leaves, or at its time.
func TestRelayRoom(t *testing.T) {
	var r relayRoom
	if err := r.wait(time.Now().Add(time.Second), nil, nil); err != nil {
		t.Fatalf("a viewer never behind waited: %v", err)
	}
	r.set(true)
	r.set(true)
	if err := r.wait(time.Now().Add(50*time.Millisecond), nil, nil); !errors.Is(err, session.ErrSlowConsumer) {
		t.Fatalf("behind past its time: %v", err)
	}
	gone := make(chan struct{})
	close(gone)
	if err := r.wait(time.Now().Add(time.Minute), gone, nil); !errors.Is(err, errViewerGone) {
		t.Fatalf("behind and gone: %v", err)
	}
	ended := make(chan struct{})
	close(ended)
	if err := r.wait(time.Now().Add(time.Minute), nil, ended); err != nil {
		t.Fatalf("behind as the session ended: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- r.wait(time.Now().Add(time.Minute), nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	r.set(false)
	r.set(false)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("caught up: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait went on once the viewer caught up")
	}
	r.set(true)
	r.set(false)
	if err := r.wait(time.Now().Add(time.Second), nil, nil); err != nil {
		t.Fatalf("caught up again: %v", err)
	}
}
