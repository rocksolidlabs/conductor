package signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

func register(t *testing.T, hub *Hub) (*HostedSession, *HostConn) {
	t.Helper()
	conn := NewHostConn()
	hs, resumed, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: 1,
		Host: proto.HostInfo{Name: "laptop"}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24}}, conn)
	if err != nil || resumed {
		t.Fatalf("register: %v %v", err, resumed)
	}
	return hs, conn
}

// takeWithin is the next message conn's writer takes within d (Next): the
// server's own first, a closed-for-its-link viewer's dropped; false when
// none comes.
func takeWithin(conn *HostConn, d time.Duration) (Outbound, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return conn.Next(ctx)
}

func drainText(t *testing.T, conn *HostConn, want string) {
	t.Helper()
	o, ok := takeWithin(conn, time.Second)
	if !ok {
		t.Fatalf("no %s message", want)
	}
	if o.Text == nil {
		t.Fatalf("expected text %s, got binary", want)
	}
	if tt, _ := proto.ParseHeader(o.Text); tt != want {
		t.Fatalf("expected %s, got %s", want, tt)
	}
}

func TestRegisterResumeAndRelayRules(t *testing.T) {
	reg := session.NewRegistry(4)
	hub := NewHub(reg, nil)
	hs, conn := register(t, hub)
	if hs.Info().Name != "bash @ laptop" || hs.Info().Kind != session.KindHosted {
		t.Fatalf("info %+v", hs.Info())
	}
	if _, _, err := hub.Register(proto.Register{Proto: 1}, NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("bad register: %v", err)
	}

	v, err := hs.AddViewer("0123456789abcdef", session.RoleView, "l1", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: []byte("x")}); err == nil {
		t.Fatal("relay before relay mode must fail")
	}
	if err := hs.StartRelay(v); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostRelayStart)
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeInput, Payload: []byte("x")}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("view input: %v", err)
	}
	resize := proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: 1, Rows: 1})
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: resize[1:]}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("view resize: %v", err)
	}
	submit := proto.MustControl(proto.Submit{T: proto.CtlSubmit, Text: "x"})
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: submit[1:]}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("view submit: %v", err)
	}
	answer := proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: "0123456789abcdef", Input: "y"})
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: answer[1:]}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("view answer: %v", err)
	}
	ping := proto.MustControl(proto.Ping{T: proto.CtlPing})
	// The editor's saves and Neovim are the control role's too (design round 12, F6, F8).
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeFileWrite, Payload: []byte("x")}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("a view viewer's save: %v", err)
	}
	for _, ctl := range []any{proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r", Path: "a"}, proto.NvimInput{T: proto.CtlNvimInput, ID: "e", Keys: "x"}, proto.NvimClose{T: proto.CtlNvimClose, ID: "e"}, proto.NvimSwap{T: proto.CtlNvimSwap, ID: "e", Choice: proto.NvimSwapDelete}} {
		frame := proto.MustControl(ctl)
		if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: frame[1:]}); !errors.Is(err, session.ErrReadOnly) {
			t.Fatalf("a view viewer's %T: %v", ctl, err)
		}
	}
	if err := hs.RelayToHost(v, proto.Frame{Type: proto.TypeControl, Payload: ping[1:]}); err != nil {
		t.Fatalf("view ping: %v", err)
	}
	select {
	case o := <-conn.Send:
		if o.Binary == nil {
			t.Fatal("expected relay envelope")
		}
		id, inner, err := proto.DecodeRelay(o.Binary[1:])
		if err != nil || id != v.ID || inner.Type != proto.TypeControl {
			t.Fatalf("envelope %s %+v %v", id, inner, err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay envelope not queued")
	}
	hs.HostRelayFrame(v.ID, proto.Frame{Type: proto.TypeOutput, Payload: []byte("o")})
	select {
	case f := <-v.Out:
		if f[0] != proto.TypeOutput {
			t.Fatalf("viewer frame %v", f)
		}
	default:
		t.Fatal("viewer did not receive relayed frame")
	}
	hs.DisconnectLink("l1")
	if !errors.Is(v.Reason(), session.ErrRevoked) {
		t.Fatalf("revoke reason %v", v.Reason())
	}

	// Host drop closes viewers and marks the session; resume with the secret works once.
	v2, _ := hs.AddViewer("fedcba9876543210", session.RoleControl, "", "")
	hs.HostDisconnected(conn)
	if !errors.Is(v2.Reason(), ErrHostGone) || hs.Info().Status != session.StatusHostDisconnected {
		t.Fatalf("after disconnect: %v %s", v2.Reason(), hs.Info().Status)
	}
	if _, _, err := hub.Register(proto.Register{Proto: 1, Session: proto.HostSession{Command: []string{"bash"}},
		Resume: &proto.HostResume{SessionID: hs.Info().ID, Secret: "wrong"}}, NewHostConn()); !errors.Is(err, ErrBadResume) {
		t.Fatalf("wrong secret: %v", err)
	}
	conn2 := NewHostConn()
	hs2, resumed, err := hub.Register(proto.Register{Proto: 1, Session: proto.HostSession{Command: []string{"bash"}, Cols: 10, Rows: 10},
		Resume: &proto.HostResume{SessionID: hs.Info().ID, Secret: hs.Secret()}}, conn2)
	if err != nil || !resumed || hs2 != hs || hs.Info().Status != session.StatusRunning || hs.Info().Cols != 10 {
		t.Fatalf("resume: %v %v %+v", err, resumed, hs.Info())
	}
	if err := hs.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn2, proto.HostStop)

	// Expiry removes sessions whose host stayed away past the grace period.
	hs.HostDisconnected(conn2)
	hub.Expire(time.Now())
	if _, ok := reg.Get(hs.Info().ID); !ok {
		t.Fatal("must survive within grace")
	}
	hub.Expire(time.Now().Add(DisconnectGrace + time.Second))
	if _, ok := reg.Get(hs.Info().ID); ok {
		t.Fatal("must be removed after grace")
	}
}

// activityRecorder collects what a Hub hands to OnActivity.
type activityRecorder struct {
	mu     sync.Mutex
	ids    []string
	got    []session.ActivityEntry
	states []session.AttentionState
}

func (r *activityRecorder) hook(id string, e session.ActivityEntry, state session.AttentionState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	r.got = append(r.got, e)
	r.states = append(r.states, state)
}

func (r *activityRecorder) entries() []session.ActivityEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.ActivityEntry(nil), r.got...)
}

func TestHostActivityReachesTheHubHook(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)

	at := time.Date(2026, 9, 29, 12, 0, 0, 123456789, time.UTC)
	hs.HostActivity(proto.Activity{
		T: proto.CtlActivity, At: at.Format(time.RFC3339Nano), Type: session.ActivityArtifact, By: "0123456789abcdef", ByName: "agent",
		Message: "PR opened", URL: "https://github.com/x/y/pull/1", To: "review", Tool: "gh",
	}, "")
	got := rec.entries()
	if len(got) != 1 || rec.ids[0] != hs.Info().ID {
		t.Fatalf("hook saw %v for %v, want one entry for %s", got, rec.ids, hs.Info().ID)
	}
	want := session.ActivityEntry{
		At: at, Type: session.ActivityArtifact, By: "0123456789abcdef", ByName: "agent",
		Message: "PR opened", URL: "https://github.com/x/y/pull/1", To: "review", Tool: "gh",
	}
	if got[0] != want {
		t.Fatalf("entry %+v, want %+v", got[0], want)
	}
}

// The host's session id in the message is not used: the connection says whose
// entry it is, so one host cannot write into another session's feed.
func TestHostActivityIsAttributedToTheConnectionsSession(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	hs.HostActivity(proto.Activity{At: time.Now().Format(time.RFC3339Nano), Type: session.ActivityProgress, Message: "1/7"}, "")
	if len(rec.ids) != 1 || rec.ids[0] != hs.Info().ID {
		t.Fatalf("attributed to %v, want %s", rec.ids, hs.Info().ID)
	}
}

func TestHostActivityDropsTypesItDoesNotKnow(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	for _, typ := range []string{"", "bogus", "clear", "needs_input", "PROGRESS", strings.Repeat("x", 100000)} {
		hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: typ, Message: "m"}, "")
	}
	if got := rec.entries(); len(got) != 0 {
		t.Fatalf("unknown types reached the hook: %+v", got)
	}
	// Every type the session itself can record is welcome from a host.
	for _, typ := range []string{
		session.ActivityAttention, session.ActivityInput, session.ActivityJoin, session.ActivityLeave, session.ActivityLink, session.ActivityStatus,
		session.ActivityProgress, session.ActivityArtifact, session.ActivityHandoff, session.ActivityToolUse, session.ActivityToolDenied, session.ActivityError,
	} {
		hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: typ}, "")
	}
	if got := rec.entries(); len(got) != 12 {
		t.Fatalf("%d of 12 known types reached the hook", len(got))
	}
}

func TestHostActivityIsCleanedBeforeItIsFannedOut(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	hs.HostActivity(proto.Activity{
		T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: session.ActivityError,
		By:      strings.Repeat("b", 1000),
		ByName:  "  bob\x00\x1b[31m" + strings.Repeat("n", 100),
		Message: "line\x00 one\nline two " + strings.Repeat("m", 5000),
		URL:     "https://x/" + strings.Repeat("u", 5000),
		To:      strings.Repeat("t", 100),
		Tool:    strings.Repeat("k", 500),
	}, "")
	got := rec.entries()
	if len(got) != 1 {
		t.Fatalf("entries %+v", got)
	}
	e := got[0]
	if len(e.Message) > session.MaxAttentionMessage || strings.ContainsRune(e.Message, 0) || !strings.HasPrefix(e.Message, "line one\nline two ") {
		t.Fatalf("message %q", e.Message)
	}
	if len(e.URL) > session.MaxEventURL || len([]rune(e.To)) > session.MaxEventTo || len(e.Tool) > session.MaxEventTool {
		t.Fatalf("url %d, to %d, tool %d", len(e.URL), len([]rune(e.To)), len(e.Tool))
	}
	if len([]rune(e.ByName)) > proto.MaxNameLen || strings.ContainsAny(e.ByName, "\x00\x1b") {
		t.Fatalf("byName %q", e.ByName)
	}
	if e.By != "" {
		t.Fatalf("a subscriber id of %d bytes was kept", len(e.By))
	}
	frame, err := json.Marshal(proto.Activity{T: proto.CtlActivity, At: e.At.Format(time.RFC3339Nano), Type: e.Type, By: e.By, ByName: e.ByName, Message: e.Message, URL: e.URL, To: e.To, Tool: e.Tool})
	if err != nil || len(frame) > proto.MaxControl {
		t.Fatalf("cleaned entry is %d bytes as a control message (%v)", len(frame), err)
	}
}

// CleanEntry leaves By to the session that sets it, and a host is not trusted
// with it: it loses control characters and surrounding space as every other
// field does, and one still over 64 bytes is dropped rather than cut, since a
// cut id would name someone else.
func TestHostActivityCleansTheSubscriberID(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	cases := []struct{ by, want string }{
		{" 0123456789\x00abcdef\x1b\n", "0123456789abcdef"},
		{"\x07" + strings.Repeat("b", maxEntryBy), strings.Repeat("b", maxEntryBy)},
		{strings.Repeat("b", maxEntryBy+1), ""},
		{strings.Repeat("é", maxEntryBy/2+1), ""}, // 33 characters, 66 bytes
		{"\x00\x1b\t", ""},
	}
	for _, c := range cases {
		hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: session.ActivityJoin, By: c.by, ByName: "Ada"}, "")
	}
	got := rec.entries()
	if len(got) != len(cases) {
		t.Fatalf("%d entries reached the hook, want %d", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i].By != c.want {
			t.Errorf("by %q reached the hook as %q, want %q", c.by, got[i].By, c.want)
		}
	}
}

// Entries are stamped when the host sends them; one without a usable time
// gets the moment the server saw it.
func TestHostActivityStampsAMissingOrUnreadableTime(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	before := time.Now()
	for _, at := range []string{"", "yesterday", "0000-00-00T00:00:00Z"} {
		hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: at, Type: session.ActivityProgress}, "")
	}
	got := rec.entries()
	if len(got) != 3 {
		t.Fatalf("entries %+v", got)
	}
	for _, e := range got {
		if e.At.Before(before) || time.Since(e.At) > 5*time.Second {
			t.Fatalf("stamp %v is not the time of receipt", e.At)
		}
	}
}

func TestHostActivityWithoutAHookIsIgnored(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, _ := register(t, hub)
	hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: session.ActivityProgress}, "")
}

// The hook may be slow to reach and is called from every host's read loop:
// HostActivity itself takes no lock the hook could wait on.
func TestHostActivityIsSafeFromManyGoroutines(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: time.Now().Format(time.RFC3339Nano), Type: session.ActivityToolUse, Tool: "Bash"}, "")
			}
		}()
	}
	wg.Wait()
	if n := len(rec.entries()); n != 800 {
		t.Fatalf("%d of 800 entries reached the hook", n)
	}
}

func TestForwardActivityQueuesAnActivityMessage(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	err := hs.ForwardActivity(session.ActivityEntry{
		At: at, Type: session.ActivityHandoff, ByName: "agent", Message: "over to review", To: "reviewer", Tool: "gh", URL: "https://x/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m proto.HostActivityMsg
	select {
	case o := <-conn.Own:
		if o.Text == nil || json.Unmarshal(o.Text, &m) != nil {
			t.Fatalf("queued %+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("no activity message queued")
	}
	want := proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{
		T: proto.CtlActivity, At: at.Format(time.RFC3339Nano), Type: session.ActivityHandoff, ByName: "agent", Message: "over to review", URL: "https://x/1", To: "reviewer", Tool: "gh",
	}}
	if m != want {
		t.Fatalf("message %+v, want %+v", m, want)
	}
}

// The API does not stamp the events it forwards: the host does, when it
// records them, and its clock is the one the session log uses.
func TestForwardActivityLeavesTheTimeToTheHost(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	if err := hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: "1/7"}); err != nil {
		t.Fatal(err)
	}
	var m proto.HostActivityMsg
	if err := json.Unmarshal((<-conn.Own).Text, &m); err != nil || m.Entry.At != "" {
		t.Fatalf("message %+v %v", m, err)
	}
}

// A host reads at most MaxHostMessage; whatever the caller hands over is
// bounded before it is queued.
func TestForwardActivityCleansWhatItSends(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	err := hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: strings.Repeat("m", 50000), URL: strings.Repeat("u", 50000), Tool: strings.Repeat("t", 50000)})
	if err != nil {
		t.Fatal(err)
	}
	o := <-conn.Own
	if len(o.Text) > proto.MaxControl {
		t.Fatalf("queued %d bytes", len(o.Text))
	}
}

func TestForwardActivityNeedsAHostWithRoom(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	for i := 0; i < cap(conn.Own); i++ {
		conn.Own <- Outbound{Text: []byte("{}")}
	}
	if err := hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress}); !errors.Is(err, ErrSlowHost) {
		t.Fatalf("full queue: %v", err)
	}
	hs2, conn2 := register(t, NewHub(session.NewRegistry(4), nil))
	hs2.HostDisconnected(conn2)
	if err := hs2.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress}); !errors.Is(err, ErrHostGone) {
		t.Fatalf("no host: %v", err)
	}
}

// What one side sends the other can read: an entry forwarded to a host comes
// back through HostActivity as the same entry.
func TestActivityMessageRoundTrip(t *testing.T) {
	src, srcConn := register(t, NewHub(session.NewRegistry(4), nil))
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	dst, _ := register(t, hub)

	in := session.ActivityEntry{
		At: time.Date(2026, 9, 29, 12, 0, 0, 5, time.UTC), Type: session.ActivityToolDenied, By: "b1", ByName: "agent",
		Message: "denied", URL: "https://x/1", To: "them", Tool: "Bash",
	}
	if err := src.ForwardActivity(in); err != nil {
		t.Fatal(err)
	}
	var m proto.HostActivityMsg
	if err := json.Unmarshal((<-srcConn.Own).Text, &m); err != nil {
		t.Fatal(err)
	}
	// The entry on the wire is the shared pair's, in both directions: no
	// conversion of this package's own can drift from the session's.
	if want := session.EntryToProto(in); m.Entry != want {
		t.Fatalf("ForwardActivity queued %+v, want session.EntryToProto's %+v", m.Entry, want)
	}
	if back := session.EntryFromProto(m.Entry); back != in {
		t.Fatalf("session.EntryFromProto gave %+v, want %+v", back, in)
	}
	dst.HostActivity(m.Entry, m.State)
	if got := rec.entries(); len(got) != 1 || got[0] != in {
		t.Fatalf("round trip gave %+v, want %+v", got, in)
	}
}

// MaxHostMessage bounds a host control text frame. The entry alone is bounded
// by CleanEntry so that its control frame fits 8 KiB; the host envelope adds a
// few dozen bytes to that, far under 64 KiB, in both directions.
func TestActivityMessagesFitTheHostMessageLimit(t *testing.T) {
	// The characters JSON writes longest: 6 bytes each for < > &.
	worst := session.CleanEntry(session.ActivityEntry{
		At: time.Date(2006, 1, 2, 15, 4, 5, 999999999, time.UTC), Type: session.ActivityToolDenied, By: strings.Repeat("b", maxEntryBy),
		ByName: strings.Repeat("<", 200), Message: strings.Repeat("<", 5000), URL: strings.Repeat("a", 5000),
		To: strings.Repeat("&", 200), Tool: strings.Repeat(">", 500),
	})
	if worst.URL == "" || len(worst.Message) != session.MaxAttentionMessage || len(worst.Tool) != session.MaxEventTool {
		t.Fatalf("the test entry is not maximal: %+v", worst)
	}
	toServer := hostActivityMsg(worst)
	toServer.SessionID = session.NewID() // the host names its session; the server does not
	// The host sends the attention state an attention entry records with it:
	// needs_input is the longest.
	withState := toServer
	withState.State = string(session.AttentionNeedsInput)
	for name, msg := range map[string]proto.HostActivityMsg{"host to server": toServer, "host to server, with a state": withState, "server to host": hostActivityMsg(worst)} {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		entry, _ := json.Marshal(msg.Entry)
		if len(entry) > proto.MaxControl {
			t.Fatalf("%s: the entry is %d bytes, over the %d byte control limit", name, len(entry), proto.MaxControl)
		}
		if len(b) > proto.MaxHostMessage {
			t.Fatalf("%s: the message is %d bytes, over the %d byte host limit", name, len(b), proto.MaxHostMessage)
		}
		if extra := len(b) - len(entry); extra > 100 {
			t.Fatalf("%s: the envelope adds %d bytes, more than the headroom it is given", name, extra)
		}
	}
}

// Whatever the API sends on to a host goes down the one connection that also
// carries the input of every viewer, and closes it when its queue is full. So
// the server spends a token of the session's bucket, the same size as a
// session's own log bucket, on each report it forwards.
func TestForwardActivityIsLimitedPerSession(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	accepted := 0
	var refused error
	for i := 0; i < 3*session.EventBurst; i++ {
		if err := hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityToolUse, Tool: "Bash"}); err != nil {
			refused = err
			break
		}
		accepted++
	}
	if !errors.Is(refused, ErrRateLimited) {
		t.Fatalf("after %d events: %v, want ErrRateLimited", accepted, refused)
	}
	// A burst at once, plus what the bucket earned while the loop ran (a second's
	// worth of allowance for a slow machine; twice the burst would mean a bucket
	// of the wrong size).
	if accepted < session.EventBurst || accepted > session.EventBurst+session.EventRatePerSecond {
		t.Fatalf("accepted %d events, want the burst of %d", accepted, session.EventBurst)
	}
	if queued := len(conn.Own); queued != accepted {
		t.Fatalf("%d messages queued for the host, %d accepted: a refused event must queue nothing", queued, accepted)
	}
}

// Events and attention words share one budget, whichever route they came by.
func TestEventsAndAttentionForwardsShareOneBucket(t *testing.T) {
	hs, conn := register(t, NewHub(session.NewRegistry(4), nil))
	events, words := 0, 0
	for i := 0; i < 3*session.EventBurst; i++ {
		var err error
		if i%2 == 0 {
			if err = hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: "n"}); err == nil {
				events++
			}
		} else {
			if err = hs.SetAttentionFull(session.AttentionWorking, fmt.Sprint("n", i), session.SourceAPI, "", nil, true); err == nil {
				words++
			}
		}
		if err != nil {
			if !errors.Is(err, ErrRateLimited) {
				t.Fatal(err)
			}
			break
		}
	}
	if total := events + words; total < session.EventBurst || total > session.EventBurst+session.EventRatePerSecond {
		t.Fatalf("%d events and %d attention words were forwarded, want %d in all", events, words, session.EventBurst)
	}
	if events < session.EventBurst/3 || words < session.EventBurst/3 {
		t.Fatalf("one kind used the budget alone: %d events, %d attention words", events, words)
	}
	if queued := len(conn.Own); queued != events+words {
		t.Fatalf("%d messages queued, %d forwarded", queued, events+words)
	}
}

// A refused attention word is refused whole: the server does not keep what it
// could not pass on.
func TestSetAttentionFullChangesNothingWhenItIsLimited(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	var changes int
	hub.OnChange = func(session.Info) { changes++ }
	hs, conn := register(t, hub)
	before := changes
	last := -1
	for i := 0; i < 3*session.EventBurst; i++ {
		err := hs.SetAttentionFull(session.AttentionNeedsInput, fmt.Sprint("n", i), session.SourceAPI, "", nil, true)
		if errors.Is(err, ErrRateLimited) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = i
	}
	if last < session.EventBurst-1 || last >= 3*session.EventBurst-1 {
		t.Fatalf("limited after %d words, want about %d", last+1, session.EventBurst)
	}
	if got, want := hs.Info().Attention.Message, fmt.Sprint("n", last); got != want {
		t.Fatalf("attention %q after a refused word, want %q", got, want)
	}
	if changes-before != last+1 || len(conn.Own) != last+1 {
		t.Fatalf("%d changes announced and %d messages queued for %d accepted words", changes-before, len(conn.Own), last+1)
	}
}

// What comes through the routes (forward true) spends the session's bucket
// whether a host is connected or not: a session whose host is away cannot be
// flooded with reports while it waits for the host. The host's own reports
// come the other way and never spend one.
func TestRouteReportsAreLimitedWithOrWithoutAHost(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	for i := 0; i < 5*session.EventBurst; i++ {
		if err := hs.SetAttentionFull(session.AttentionWorking, fmt.Sprint("n", i), "bell", "", nil, false); err != nil {
			t.Fatalf("the host's own report %d was limited: %v", i, err)
		}
	}
	if len(conn.Own)+len(conn.Send) != 0 {
		t.Fatalf("%d messages went back to the host that sent them", len(conn.Own)+len(conn.Send))
	}
	hs.HostDisconnected(conn)
	accepted := 0
	for i := 0; i < 5*session.EventBurst; i++ {
		err := hs.SetAttentionFull(session.AttentionNeedsInput, fmt.Sprint("n", i), session.SourceAPI, "", nil, true)
		if errors.Is(err, ErrRateLimited) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted < session.EventBurst || accepted >= 5*session.EventBurst {
		t.Fatalf("accepted %d words for a session without a host, want about %d", accepted, session.EventBurst)
	}
	if got := hs.Info().Attention.Message; got != fmt.Sprint("n", accepted-1) {
		t.Fatalf("attention %q: a refused word was applied", got)
	}
	// An event for a session without a host is refused before it spends anything.
	if err := hs.ForwardActivity(session.ActivityEntry{Type: session.ActivityProgress}); !errors.Is(err, ErrHostGone) {
		t.Fatalf("event without a host: %v", err)
	}
}

// pumpSink records what Pump writes to it and the reason it is closed with.
type pumpSink struct {
	mu     sync.Mutex
	frames [][]byte
	reason error
	closed bool
}

func (s *pumpSink) WriteFrame(f []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	return nil
}

func (s *pumpSink) Close(reason error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.reason = true, reason
}

func (s *pumpSink) written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// A viewer closed because its host went away, or asked for it, or because
// the session ended, still gets the frames that were queued before: the
// host's last words, the session's last output and final status among
// them. Pump used to choose between the frames and the close at random, and
// the viewer was told the host was gone without why.
func TestViewerPumpDeliversWhatWasQueuedBeforeTheHostClosedIt(t *testing.T) {
	for name, reason := range map[string]error{
		"host disconnected": ErrHostGone,
		"the session ended": session.ErrSessionEnded,
		"host closed it, also when it evicted it as too slow": ErrViewerGone,
		"host reported an error for it":                       errors.New("webrtc_failed"),
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 50; i++ { // the choice between frame and close was a coin toss
				v := newViewer("v", session.RoleView, "", "")
				want := [][]byte{{1, 'a'}, {1, 'b'}, {1, 'c'}}
				for _, f := range want {
					v.push(f)
				}
				v.close(reason)
				sink := &pumpSink{}
				v.Pump(sink)
				if len(sink.frames) != len(want) {
					t.Fatalf("round %d: %d of %d frames written before the close", i, len(sink.frames), len(want))
				}
				for j := range want {
					if string(sink.frames[j]) != string(want[j]) {
						t.Fatalf("round %d: frame %d is %q, want %q", i, j, sink.frames[j], want[j])
					}
				}
				if !sink.closed || sink.reason != reason {
					t.Fatalf("round %d: closed %v with %v, want %v", i, sink.closed, sink.reason, reason)
				}
			}
		})
	}
}

// A viewer whose link was revoked, whose own queue overflowed or that has left
// is not sent what was queued for it, once the close has been seen. (A frame
// that is being written as the close lands is another matter.)
func TestViewerPumpSendsNothingMoreToAViewerThatWasCutOff(t *testing.T) {
	for name, reason := range map[string]error{
		"link revoked": session.ErrRevoked,
		"too slow":     ErrSlowViewer,
		"viewer left":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 50; i++ {
				v := newViewer("v", session.RoleView, "", "")
				v.push([]byte{1, 'a'})
				v.push([]byte{1, 'b'})
				v.close(reason)
				sink := &pumpSink{}
				v.Pump(sink)
				if sink.written() != 0 {
					t.Fatalf("round %d: %d frames written to a viewer closed for %v", i, sink.written(), reason)
				}
				if !sink.closed || sink.reason != reason {
					t.Fatalf("round %d: closed %v with %v, want %v", i, sink.closed, sink.reason, reason)
				}
			}
		})
	}
}

func TestViewerPumpWritesFramesAsTheyArrive(t *testing.T) {
	v := newViewer("v", session.RoleView, "", "")
	sink := &pumpSink{}
	done := make(chan struct{})
	go func() { v.Pump(sink); close(done) }()
	v.push([]byte{1, 'a'})
	deadline := time.Now().Add(3 * time.Second)
	for sink.written() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("a queued frame was not written")
		}
		time.Sleep(time.Millisecond)
	}
	v.push([]byte{1, 'b'})
	v.close(ErrHostGone)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pump did not return once the viewer was closed")
	}
	if sink.written() != 2 || !sink.closed {
		t.Fatalf("written %d, closed %v", sink.written(), sink.closed)
	}
}

// The host sends, with an attention entry, the attention state the entry
// records, and the hub's hook gets it with the entry. The host is not trusted
// with it: a state that is not needs_input, working or done, or one sent with
// another type of entry, is dropped and the entry kept.
func TestHostActivityCarriesTheAttentionStateOfAnAttentionEntry(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	rec := &activityRecorder{}
	hub.OnActivity = rec.hook
	hs, _ := register(t, hub)
	at := time.Now().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		typ, state string
		want       session.AttentionState
	}{
		{session.ActivityAttention, "needs_input", session.AttentionNeedsInput},
		{session.ActivityAttention, "working", session.AttentionWorking},
		{session.ActivityAttention, "done", session.AttentionDone},
		{session.ActivityAttention, "", ""},
		{session.ActivityAttention, "clear", ""},
		{session.ActivityAttention, "NEEDS_INPUT", ""},
		{session.ActivityAttention, strings.Repeat("x", 10000), ""},
		{session.ActivityProgress, "needs_input", ""},
		{session.ActivityStatus, "done", ""},
	} {
		hs.HostActivity(proto.Activity{T: proto.CtlActivity, At: at, Type: tc.typ, Message: "m"}, tc.state)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	want := []session.AttentionState{"needs_input", "working", "done", "", "", "", "", "", ""}
	if len(rec.got) != len(want) || !slices.Equal(rec.states, want) {
		t.Fatalf("the hook got %d entries with states %q, want %q", len(rec.got), rec.states, want)
	}
}

// A hosted session that ends needs nothing: the host's last attention goes
// with the status that ends it. A disconnected host's session keeps its
// state, since the host may come back.
func TestAnEndedHostedSessionNeedsNothing(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	if err := hs.SetAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI, false); err != nil {
		t.Fatal(err)
	}
	hs.HostDisconnected(conn)
	if st := hs.Info().Attention.State; st != session.AttentionNeedsInput {
		t.Fatalf("a disconnected host's session lost its prompt: %q", st)
	}
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	if info := hs.Info(); info.Status != session.StatusExited || info.Attention.State != session.AttentionNone {
		t.Fatalf("ended: %+v", info)
	}
}

// A hosted session's question keeps the id its host gave it, so that an
// answer from the server's listings names the question the host holds; an
// id that is no question's id is dropped. A change the server makes and
// forwards has none until the host reports its own, except one the host
// will not apply (the same as it shows), which keeps the one it has.
func TestAHostedQuestionKeepsItsHostsID(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	opts := []session.Option{{Label: "Yes", Input: "y"}}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceQuestion, session.KindPermission, opts, "0123456789abcdef", false, "", "")
	if a := hs.Info().Attention; a.ID != "0123456789abcdef" || a.State != session.AttentionNeedsInput {
		t.Fatalf("the host's question: %+v", a)
	}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceQuestion, session.KindPermission, opts, "has space", false, "", "")
	if a := hs.Info().Attention; a.ID != "" {
		t.Fatalf("an id that is no question's: %q", a.ID)
	}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI, "", nil, "fedcba9876543210", false, "", "")
	if err := hs.SetAttentionFull(session.AttentionNeedsInput, "Allow?", session.SourceAPI, "", nil, true); err != nil {
		t.Fatal(err)
	}
	if a := hs.Info().Attention; a.ID != "fedcba9876543210" {
		t.Fatalf("a forwarded change the host keeps: id %q", a.ID)
	}
	drainText(t, conn, proto.HostAttention)
	if err := hs.SetAttentionFull(session.AttentionNeedsInput, "Which port?", session.SourceAPI, "", nil, true); err != nil {
		t.Fatal(err)
	}
	if a := hs.Info().Attention; a.ID != "" {
		t.Fatalf("a forwarded change: id %q before the host's", a.ID)
	}
}

// A host's instance and local id fix the session's id: the same pair gives
// the same id, either input changed another.
func TestHostedIDIsStableAndDiffersByEitherInput(t *testing.T) {
	a := HostedID("instance-a", "1")
	if a != HostedID("instance-a", "1") || len(a) != session.IDLen {
		t.Fatalf("id %q", a)
	}
	if a == HostedID("instance-b", "1") || a == HostedID("instance-a", "2") || HostedID("ab", "c") == HostedID("a", "bc") {
		t.Fatal("ids collide")
	}
}

func registerAs(hub *Hub, instance, localID string, conn *HostConn) (*HostedSession, bool, error) {
	return hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host: proto.HostInfo{Name: "laptop", Instance: instance}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24, LocalID: localID}}, conn)
}

// The same instance registering again while its host is away takes the
// session back under its id; while the host is connected it is refused, as
// is another instance; one of the pair without the other is refused.
func TestRegisterUnderAnInstance(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, resumed, err := registerAs(hub, "instance-a", "1", NewHostConn())
	if err != nil || resumed || hs.Info().ID != HostedID("instance-a", "1") || hs.Owner() == "" {
		t.Fatalf("first: %v %v %+v", err, resumed, hs)
	}
	if _, _, err := registerAs(hub, "instance-a", "1", NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("while connected: %v", err)
	}
	hs.HostDisconnected(hs.conn)
	if _, _, err := registerAs(hub, "instance-b", "1", NewHostConn()); err != nil {
		t.Fatalf("another instance gets an id of its own: %v", err)
	}
	again, resumed, err := registerAs(hub, "instance-a", "1", NewHostConn())
	if err != nil || !resumed || again != hs {
		t.Fatalf("taken back: %v %v", err, resumed)
	}
	if _, _, err := registerAs(hub, "instance-a", "", NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("instance without a local id: %v", err)
	}
	if _, _, err := registerAs(hub, strings.Repeat("x", proto.MaxHostInstance+1), "1", NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("instance too long: %v", err)
	}
}

// A chat message the host reports reaches the hub's OnChat for the
// connection's session, cleaned: the scope is this session's, the names and
// ids are bounded, the text is cleaned and cut, a bad time is now, and a kind
// this server does not know is dropped.
func TestHostChatReachesTheHubHookCleaned(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	var mu sync.Mutex
	var ids []string
	var got []session.ChatMessage
	hub.OnChat = func(id string, m session.ChatMessage) {
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, id)
		got = append(got, m)
	}
	hs, _ := register(t, hub)
	at := time.Date(2026, 10, 6, 8, 32, 0, 0, time.UTC)
	hs.HostChat(proto.ChatMessage{T: "chat", ID: "0123456789abcdef", At: at.Format(time.RFC3339Nano), Scope: "run", Kind: "message",
		By: proto.ChatBy{ID: "fedcba9876543210", Name: "  Ada\x00 Lovelace ", Role: "view"}, Text: "  hi <there>\x00\r\nsecond  ", Nonce: "n1", On: "core"})
	hs.HostChat(proto.ChatMessage{ID: "bogus", Kind: "bogus", By: proto.ChatBy{Role: "view"}})
	hs.HostChat(proto.ChatMessage{ID: "norole", Kind: "message", By: proto.ChatBy{Role: "owner"}})
	hs.HostChat(proto.ChatMessage{ID: "", Kind: "message", By: proto.ChatBy{Role: "view"}})
	hs.HostChat(proto.ChatMessage{ID: "m2", At: "not a time", Kind: "sent_to_agent", By: proto.ChatBy{ID: "v", Name: "Nate", Role: "control"}, Ref: "0123456789abcdef", To: "agent"})
	hs.HostChat(proto.ChatMessage{ID: "m3", At: at.Format(time.RFC3339), Kind: "system", By: proto.ChatBy{ID: "v", Name: "Nate", Role: "control"}, Event: "leave", To: "core"})
	hs.HostChat(proto.ChatMessage{ID: "m4", At: at.Format(time.RFC3339), Kind: "message", By: proto.ChatBy{Role: "view"}, Text: strings.Repeat("x", proto.MaxChatText+100)})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 || len(ids) != 4 {
		t.Fatalf("hook saw %d messages (%v)", len(got), ids)
	}
	for _, id := range ids {
		if id != hs.Info().ID {
			t.Fatalf("attributed to %s, want %s", id, hs.Info().ID)
		}
	}
	want := session.ChatMessage{ID: "0123456789abcdef", At: at, Scope: "session", Kind: "message", By: session.ChatBy{ID: "fedcba9876543210", Name: "Ada Lovelace", Role: session.RoleView}, Text: "hi <there>\nsecond"}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("message %+v, want %+v", got[0], want)
	}
	if m := got[1]; m.ID != "m2" || m.Kind != "sent_to_agent" || m.Ref != "0123456789abcdef" || m.To != "agent" || time.Since(m.At) > time.Minute {
		t.Fatalf("marker %+v", m)
	}
	if m := got[2]; m.Event != "leave" || m.To != "" {
		t.Fatalf("system line %+v", m)
	}
	if m := got[3]; len(m.Text) != proto.MaxChatText {
		t.Fatalf("the long text was cut to %d bytes", len(m.Text))
	}
}

// The host's mark on a question keys typed in its terminal have answered
// (Attention.Answered, #258) is kept on the server's copy, so the listings
// and the stream say so; the mark on anything but a question is dropped; and
// a forwarded change of the same words is not the same once the mark differs.
func TestAHostedQuestionKeepsItsHostsAnsweredMark(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, _ := register(t, hub)
	opts := []session.Option{{Label: "Yes", Input: "y"}}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceQuestion, session.KindPermission, opts, "0123456789abcdef", false, "", "")
	if a := hs.Info().Attention; a.Answered != "" {
		t.Fatalf("asked, not answered: %+v", a)
	}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceQuestion, session.KindPermission, opts, "0123456789abcdef", false, session.AnsweredTerminal, "")
	if a := hs.Info().Attention; a.Answered != session.AnsweredTerminal || a.ID != "0123456789abcdef" || a.State != session.AttentionNeedsInput {
		t.Fatalf("answered in the host's terminal: %+v", a)
	}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceQuestion, session.KindPermission, opts, "0123456789abcdef", false, "later", "")
	if a := hs.Info().Attention; a.Answered != "" {
		t.Fatalf("a mark that is not the terminal's: %+v", a)
	}
	hs.HostAttention(session.AttentionDone, "", session.SourceAPI, "", nil, "", false, session.AnsweredTerminal, "")
	if a := hs.Info().Attention; a.Answered != "" || a.State != session.AttentionDone {
		t.Fatalf("a mark on no question: %+v", a)
	}
	a := session.Attention{State: session.AttentionNeedsInput, Message: "Allow?", Source: session.SourceQuestion, Options: opts}
	b := a
	b.Answered = session.AnsweredTerminal
	if !sameAttention(a, a) || sameAttention(a, b) {
		t.Fatal("the mark tells two attentions apart")
	}
}
