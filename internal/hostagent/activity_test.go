package hostagent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func entryMessage(msg string) session.ActivityEntry {
	return session.ActivityEntry{Type: session.ActivityProgress, Message: msg}
}

func TestActivityForwarderSendsInOrder(t *testing.T) {
	var mu sync.Mutex
	var got []string
	f := newActivityForwarder(func(e session.ActivityEntry, _ session.AttentionState) {
		mu.Lock()
		got = append(got, e.Message)
		mu.Unlock()
	}, func(session.ChatMessage) {}, discardLog)
	go f.run(t.Context())
	want := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		msg := strings.Repeat("x", i)
		want = append(want, msg)
		if !f.push(entryMessage(msg), "") {
			t.Fatalf("entry %d refused", i)
		}
	}
	waitIdle(t, f)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sent %d entries out of order or short: %d", len(got), len(got))
	}
}

func waitIdle(t *testing.T, f *activityForwarder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f.idle() {
		if time.Now().After(deadline) {
			t.Fatal("the forwarder did not drain")
		}
		time.Sleep(time.Millisecond)
	}
}

// OnActivity runs on the goroutine that records, which may be the one reading
// the process: a connection that takes seconds to write must not hold it.
func TestActivityForwarderNeverBlocksTheRecorder(t *testing.T) {
	release := make(chan struct{})
	f := newActivityForwarder(func(session.ActivityEntry, session.AttentionState) { <-release }, func(session.ChatMessage) {}, discardLog)
	go f.run(t.Context())
	defer close(release)

	accepted := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < 3*activityQueue; i++ {
			if f.push(entryMessage("n"), "") {
				n++
			}
		}
		accepted <- n
	}()
	select {
	case n := <-accepted:
		// The queue holds activityQueue entries, and one more is in the sender's hands.
		if n < activityQueue || n > activityQueue+1 {
			t.Fatalf("accepted %d of %d, want about %d", n, 3*activityQueue, activityQueue)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked behind a stalled send")
	}
	if f.idle() {
		t.Fatal("a forwarder holding entries reports idle")
	}
}

func TestActivityForwarderIsSafeFromManyGoroutines(t *testing.T) {
	var sent sync.WaitGroup
	sent.Add(800)
	f := newActivityForwarder(func(session.ActivityEntry, session.AttentionState) { sent.Done() }, func(session.ChatMessage) {}, discardLog)
	go f.run(t.Context())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				for !f.push(entryMessage("n"), "") { // the queue holds 256; the sender drains it
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	done := make(chan struct{})
	go func() { sent.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("not every entry was sent")
	}
	waitIdle(t, f)
}

func TestActivityForwarderStopsWithItsContext(t *testing.T) {
	f := newActivityForwarder(func(session.ActivityEntry, session.AttentionState) {}, func(session.ChatMessage) {}, discardLog)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

// activityTestAgent is an agent whose control connection is a channel, over a
// real session, wired the way Run wires it. With delayStatus set, the session's
// final status entry reaches the hook that long after the session announced
// its end, which makes the gap between the two easy to hit.
func activityTestAgent(t *testing.T, delayStatus time.Duration) (*agent, chan any) {
	t.Helper()
	dir := t.TempDir()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: dir, Env: hostEnv(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(t.Context(), time.Second) })
	out := make(chan any, 64)
	a := &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "sess-1"}
	a.sendHook = func(v any) { out <- v }
	a.activity = newActivityForwarder(a.sendActivity, a.sendChat, a.log)
	go a.activity.run(t.Context())
	hook := a.onLocalActivity
	if delayStatus > 0 {
		hook = func(id string, e session.ActivityEntry, state session.AttentionState) {
			if e.Type == session.ActivityStatus {
				time.Sleep(delayStatus)
			}
			a.onLocalActivity(id, e, state)
		}
	}
	local := session.NewLocal(session.Info{ID: "sess-1", Cwd: dir, Cols: 80, Rows: 24}, proc, session.Options{OnActivity: hook})
	a.mu.Lock()
	a.local = local
	a.mu.Unlock()
	return a, out
}

func nextHostActivity(t *testing.T, out <-chan any) proto.HostActivityMsg {
	t.Helper()
	for {
		select {
		case v := <-out:
			if m, ok := v.(proto.HostActivityMsg); ok {
				return m
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the host sent no activity message")
		}
	}
}

// The hook runs on the goroutine that records the entry; while the
// connection to the server stalls, recording goes on.
func TestHostRecordingNeverWaitsForTheConnection(t *testing.T) {
	a, _ := activityTestAgent(t, 0)
	release := make(chan struct{})
	a.sendHook = func(any) { <-release }
	defer close(release)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3*activityQueue; i++ {
			a.onLocalActivity("sess-1", entryMessage("n"), "")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hook waited for a stalled connection")
	}
}

// Every entry the local session records goes to the server, the ones it
// makes itself and the ones it was sent alike.
func TestHostForwardsWhatTheLocalSessionRecords(t *testing.T) {
	a, out := activityTestAgent(t, 0)
	a.local.Record(session.ActivityEntry{Type: session.ActivityToolDenied, By: "b1", ByName: "Ada", Message: "no", URL: "https://x/1", To: "them", Tool: "Bash"})
	m := nextHostActivity(t, out)
	if m.T != proto.HostActivity || m.SessionID != "sess-1" {
		t.Fatalf("message %+v", m)
	}
	e := m.Entry
	if want := session.EntryToProto(a.local.Activity()[0]); e != want {
		t.Fatalf("entry %+v is not the shared conversion's %+v", e, want)
	}
	if e.T != proto.CtlActivity || e.Type != session.ActivityToolDenied || e.By != "b1" || e.ByName != "Ada" || e.Message != "no" || e.URL != "https://x/1" || e.To != "them" || e.Tool != "Bash" {
		t.Fatalf("entry %+v", e)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.At); err != nil {
		t.Fatalf("at %q: %v", e.At, err)
	}
	// One the session makes on its own: a viewer arriving.
	sub, err := a.local.AttachWith(session.AttachOptions{ID: "0123456789abcdef", Role: session.RoleView, Name: "Bob", Cols: 80, Rows: 24}, discardSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.local.Detach(sub)
	if m := nextHostActivity(t, out); m.Entry.Type != session.ActivityJoin || m.Entry.ByName != "Bob" {
		t.Fatalf("join entry %+v", m.Entry)
	}
}

type discardSink struct{}

func (discardSink) Transport() string       { return "test" }
func (discardSink) WriteFrame([]byte) error { return nil }
func (discardSink) Close(error)             {}

// An event the server sends for this session is recorded like any other, so
// the people watching the host see it, and is reported back so the server's
// admin stream has it.
func TestHostRecordsWhatTheServerSends(t *testing.T) {
	a, out := activityTestAgent(t, 0)
	msg := proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{
		T: proto.CtlActivity, Type: session.ActivityArtifact, ByName: "agent", Message: "PR opened\x00", URL: "https://github.com/x/y/pull/1", To: "review",
	}}
	data, _ := json.Marshal(msg)
	if err := a.handleControl(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	log := a.local.Activity()
	if len(log) != 1 {
		t.Fatalf("session log %+v", log)
	}
	e := log[0]
	if e.Type != session.ActivityArtifact || e.ByName != "agent" || e.Message != "PR opened" || e.URL != "https://github.com/x/y/pull/1" || e.To != "review" || e.At.IsZero() {
		t.Fatalf("recorded %+v", e)
	}
	back := nextHostActivity(t, out)
	if back.Entry.Type != session.ActivityArtifact || back.Entry.Message != "PR opened" || back.Entry.At == "" {
		t.Fatalf("reported back %+v", back)
	}
}

func TestHostRefusesActivityOfATypeItDoesNotKnow(t *testing.T) {
	a, out := activityTestAgent(t, 0)
	data, _ := json.Marshal(proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{T: proto.CtlActivity, Type: "bogus", Message: "x"}})
	if err := a.handleControl(t.Context(), data); err == nil {
		t.Fatal("an entry of an unknown type was accepted")
	}
	if log := a.local.Activity(); len(log) != 0 {
		t.Fatalf("recorded %+v", log)
	}
	select {
	case v := <-out:
		t.Fatalf("reported %+v", v)
	case <-time.After(100 * time.Millisecond):
	}
}

// A server newer than this host may send messages it has never heard of.
func TestHostIgnoresMessagesFromANewerServer(t *testing.T) {
	a, _ := activityTestAgent(t, 0)
	if err := a.handleControl(t.Context(), []byte(`{"t":"from_the_future","n":1}`)); err != nil {
		t.Fatalf("unknown message: %v", err)
	}
}

// The session announces its end, then records its final status entry: for a
// moment the process is over and nothing is queued. Run must not take that
// moment for "nothing left to send" before it closes the control connection,
// or the last entry of a hosted session never reaches the admin stream.
func TestSettleWaitsForTheFinalStatusEntry(t *testing.T) {
	a, out := activityTestAgent(t, 150*time.Millisecond)
	a.flushed = make(chan struct{}) // no watchStatus here to report the status message
	close(a.flushed)
	go a.local.Stop(t.Context())
	select {
	case <-a.local.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	a.settle(t.Context())
	for {
		select {
		case v := <-out:
			if m, ok := v.(proto.HostActivityMsg); ok && m.Entry.Type == session.ActivityStatus {
				if !strings.HasPrefix(m.Entry.Message, "stopped") && !strings.HasPrefix(m.Entry.Message, "exited") {
					t.Fatalf("status entry %+v", m.Entry)
				}
				return
			}
		default:
			t.Fatal("settle returned before the final status entry was sent")
		}
	}
}

// gatedSink is a viewer whose transport is slow to take the final status: it
// records every frame and holds the status until gate is closed, as a relay
// write that takes a while would.
type gatedSink struct {
	gate chan struct{}
	mu   sync.Mutex
	got  []string
}

func (*gatedSink) Transport() string { return "test" }
func (g *gatedSink) WriteFrame(frame []byte) error {
	f, err := proto.Decode(frame)
	if err != nil || f.Type != proto.TypeControl {
		return nil
	}
	t, _ := proto.ParseHeader(f.Payload)
	if t == proto.CtlStatus {
		<-g.gate
	}
	g.mu.Lock()
	g.got = append(g.got, t)
	g.mu.Unlock()
	return nil
}
func (*gatedSink) Close(error) {}

func (g *gatedSink) saw(t string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Contains(g.got, t)
}

// The session announces its end to its viewers by queueing a status frame for
// each; the viewer's own goroutine writes it, which for a relay viewer means
// on the control connection. Run closes that connection once settle returns,
// so settle must wait for the viewers to be handed the frame, or a relay
// viewer is told the host is gone without ever being told why.
func TestSettleWaitsForTheViewersToGetTheFinalStatus(t *testing.T) {
	a, _ := activityTestAgent(t, 0)
	a.flushed = make(chan struct{}) // no watchStatus here to report the status message
	close(a.flushed)
	sink := &gatedSink{gate: make(chan struct{})}
	if _, err := a.local.Attach("v1", session.RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	go a.local.Stop(t.Context())
	select {
	case <-a.local.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	time.AfterFunc(200*time.Millisecond, func() { close(sink.gate) })
	a.settle(t.Context())
	if !sink.saw(proto.CtlStatus) {
		t.Fatal("settle returned before the viewer was handed the final status")
	}
}

// stuckSink is a viewer whose transport never takes a frame.
type stuckSink struct{ release chan struct{} }

func (*stuckSink) Transport() string { return "test" }
func (s *stuckSink) WriteFrame([]byte) error {
	<-s.release
	return nil
}
func (*stuckSink) Close(error) {}

// A viewer that will not take its frames must not hold up a host that has been
// stopped from outside either: there is no connection left to send them on.
func TestSettleDoesNotWaitForViewersOnceCancelled(t *testing.T) {
	a, _ := activityTestAgent(t, 0)
	a.flushed = make(chan struct{})
	close(a.flushed)
	stuck := &stuckSink{release: make(chan struct{})}
	defer close(stuck.release)
	if _, err := a.local.Attach("v1", session.RoleView, "", 80, 24, stuck); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	a.settle(ctx)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("settle took %v after the host was cancelled", took)
	}
}

// When the host is stopped with SIGINT or SIGTERM the control connection goes
// first and the forwarder with it, so the final status message and entry have
// nowhere to go. settle must not wait for them: each of its two waits runs to
// a 2 s deadline otherwise, and the host takes 4 s to exit.
func TestSettleDoesNotWaitOnceCancelled(t *testing.T) {
	a, _ := activityTestAgent(t, 0)
	release := make(chan struct{})
	a.sendHook = func(any) { <-release }
	defer close(release)
	a.activity.push(entryMessage("stuck"), "") // the forwarder takes it and stalls in send: never idle
	// a.flushed is nil, as it is when watchStatus returned on the cancel.

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	a.settle(ctx)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("settle took %v after the host was cancelled", took)
	}
}

// An attention entry goes to the server with the attention state it records,
// the one the session is in when it records the entry; other entries carry
// none.
func TestHostSendsTheStateAnAttentionEntryRecords(t *testing.T) {
	a, out := activityTestAgent(t, 0)
	a.local.SetAttentionFull(session.AttentionNeedsInput, "approve?", session.SourceAPI, session.KindPermission, nil)
	if m := nextHostActivity(t, out); m.Entry.Type != session.ActivityAttention || m.Entry.Message != "approve?" || m.State != string(session.AttentionNeedsInput) {
		t.Fatalf("attention entry %+v", m)
	}
	a.local.SetAttention(session.AttentionDone, "", session.SourceAPI)
	if m := nextHostActivity(t, out); m.Entry.Type != session.ActivityAttention || m.Entry.Message != "done" || m.State != string(session.AttentionDone) {
		t.Fatalf("attention entry without a message %+v", m)
	}
	a.local.Record(session.ActivityEntry{Type: session.ActivityProgress, Message: "1/2"})
	if m := nextHostActivity(t, out); m.Entry.Type != session.ActivityProgress || m.State != "" {
		t.Fatalf("progress entry %+v", m)
	}
}

// The state comes with the entry, handed to OnActivity by the goroutine that
// records it, not read when the connection gets to send it: a change made in
// between belongs to the next entry, not to the one waiting.
func TestHostTakesTheStateWhenTheEntryIsRecorded(t *testing.T) {
	a, out := activityTestAgent(t, 0)
	release := make(chan struct{})
	a.sendHook = func(v any) {
		if _, ok := v.(proto.HostActivityMsg); ok {
			<-release
		}
		out <- v
	}
	a.local.SetAttentionFull(session.AttentionNeedsInput, "approve?", session.SourceAPI, "", nil)
	a.local.SetAttentionFull(session.AttentionWorking, "compiling", session.SourceAPI, "", nil)
	close(release)
	first, second := nextHostActivity(t, out), nextHostActivity(t, out)
	if first.Entry.Message != "approve?" || first.State != string(session.AttentionNeedsInput) {
		t.Fatalf("first entry %+v", first)
	}
	if second.Entry.Message != "compiling" || second.State != string(session.AttentionWorking) {
		t.Fatalf("second entry %+v", second)
	}
}
