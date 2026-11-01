package signal

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// registerAnswering registers a session whose host says answerAs (or not).
func registerAnswering(t *testing.T, hub *Hub, answerAs bool) (*HostedSession, *HostConn) {
	t.Helper()
	conn := NewHostConn()
	hs, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host: proto.HostInfo{Name: "laptop", AnswerAs: answerAs}, Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24}}, conn)
	if err != nil {
		t.Fatal(err)
	}
	return hs, conn
}

// An answer_as goes to a host that said answerAs, in the server's own lane,
// whole; a host that did not (an older one) is sent nothing, and the
// answer is unsupported there; a host gone answers ErrHostGone.
func TestHostedAnswerAs(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	m := proto.HostAnswerAsMsg{Question: "0123456789abcdef", Input: "y", ByName: "Ada", BySubject: "u-1"}

	older, oconn := registerAnswering(t, hub, false)
	if older.TakesAnswers() {
		t.Fatal("an older host takes answers")
	}
	if err := older.AnswerAs(m); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("to an older host: %v", err)
	}
	if o, ok := takeWithin(oconn, 50*time.Millisecond); ok {
		t.Fatalf("an older host was sent %q", o.Text)
	}

	hs, conn := registerAnswering(t, hub, true)
	if !hs.TakesAnswers() {
		t.Fatal("a host that said answerAs takes none")
	}
	if err := hs.AnswerAs(m); err != nil {
		t.Fatal(err)
	}
	o, ok := takeWithin(conn, time.Second)
	if !ok || !o.own {
		t.Fatalf("the answer was not sent in the server's lane: %+v %v", o, ok)
	}
	var got proto.HostAnswerAsMsg
	if err := json.Unmarshal(o.Text, &got); err != nil || got.T != proto.HostAnswerAs || got.Question != m.Question || got.Input != "y" || got.ByName != "Ada" || got.BySubject != "u-1" || !got.Valid() {
		t.Fatalf("sent %s (%v)", o.Text, err)
	}
	if err := hs.AnswerAs(proto.HostAnswerAsMsg{Question: "q", Input: "y", Text: "y", ByName: "Ada"}); err == nil {
		t.Fatal("an answer out of its bounds was sent")
	}
	if o, ok := takeWithin(conn, 50*time.Millisecond); ok {
		t.Fatalf("sent besides: %q", o.Text)
	}
	hs.HostDisconnected(conn)
	if err := hs.AnswerAs(m); !errors.Is(err, ErrHostGone) {
		t.Fatalf("with the host gone: %v", err)
	}
	// Registering again as an older host takes the flag back, and what
	// it kept.
	hs.HostAttentionKept(session.AttentionWorking, "", session.SourceAPI, "", nil, "", false, "", "", []string{"aaaaaaaaaaaaaaaa"})
	reconn := NewHostConn()
	if _, resumed, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"},
		Session: proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24}, Resume: &proto.HostResume{SessionID: hs.Info().ID, Secret: hs.Secret()}}, reconn); err != nil || !resumed {
		t.Fatalf("resume: %v %v", err, resumed)
	}
	if hs.TakesAnswers() || hs.Info().Kept != nil {
		t.Fatalf("an older host's resume: takes %v, keeps %v", hs.TakesAnswers(), hs.Info().Kept)
	}
}

// A host's attention equal to the one shown, with a new kept, has the kept
// taken and nothing else changed (its stamp, its id), and the change told
// (OnChange) for the router; a new attention takes its kept with it; an
// older host's kept is never taken.
func TestHostedKept(t *testing.T) {
	var mu sync.Mutex
	var changes []session.Info
	hub := NewHub(session.NewRegistry(4), nil)
	hub.OnChange = func(info session.Info) {
		mu.Lock()
		changes = append(changes, info)
		mu.Unlock()
	}
	changed := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(changes)
	}
	hs, _ := registerAnswering(t, hub, true)
	const a, b = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	opts := []session.Option{{Label: "Yes", Input: "y"}}
	hs.HostAttentionKept(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, opts, b, false, "", "", []string{a})
	shown := hs.Info()
	if shown.Attention.ID != b || !slices.Equal(shown.Kept, []string{a}) {
		t.Fatalf("B shown, A kept: %+v %v", shown.Attention, shown.Kept)
	}
	before := changed()
	// The same B, A no longer kept: only kept changes.
	hs.HostAttentionKept(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, opts, b, false, "", "", nil)
	now := hs.Info()
	if now.Kept != nil {
		t.Fatalf("kept %v", now.Kept)
	}
	if now.Attention.ID != b || !now.Attention.Since.Equal(*shown.Attention.Since) || now.Attention.Message != "Allow?" {
		t.Fatalf("the attention changed with only kept: %+v, was %+v", now.Attention, shown.Attention)
	}
	if changed() != before+1 {
		t.Fatalf("%d changes told for kept alone", changed()-before)
	}
	// A new attention takes its kept with it.
	hs.HostAttentionKept(session.AttentionWorking, "", session.SourceAPI, "", nil, "", false, "", "", []string{a})
	if info := hs.Info(); info.Attention.State != session.AttentionWorking || !slices.Equal(info.Kept, []string{a}) {
		t.Fatalf("working with A kept: %+v %v", info.Attention, info.Kept)
	}
	// A forwarded change leaves what the host said it keeps.
	if err := hs.SetAttentionFull(session.AttentionDone, "", session.SourceAPI, session.KindDone, nil, true); err != nil {
		t.Fatal(err)
	}
	if info := hs.Info(); !slices.Equal(info.Kept, []string{a}) {
		t.Fatalf("a forwarded change dropped kept: %v", info.Kept)
	}
	// What an older host says it keeps is never taken.
	older, _ := registerAnswering(t, hub, false)
	older.HostAttentionKept(session.AttentionWorking, "", session.SourceAPI, "", nil, "", false, "", "", []string{a})
	if info := older.Info(); info.Kept != nil {
		t.Fatalf("an older host's kept taken: %v", info.Kept)
	}
}

// HostView is what the host last said: a change the server forwards shows
// in Info at once, and in HostView only once the host tells it back, so a
// question the host still shows is never read as set aside before the host
// says it keeps it.
func TestHostViewIsTheHostsReport(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := registerAnswering(t, hub, true)
	const a = "aaaaaaaaaaaaaaaa"
	hs.HostAttentionKept(session.AttentionNeedsInput, "Which database?", session.SourceAPI, session.KindPrompt, nil, a, true, "", "", nil)
	if err := hs.SetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil, true); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostAttention)
	if got := hs.Info().Attention; got.State != session.AttentionWorking {
		t.Fatalf("the listing: %+v", got)
	}
	if v := hs.HostView(); v.Attention.ID != a || v.Attention.State != session.AttentionNeedsInput || v.Kept != nil {
		t.Fatalf("before the host applied the change: %+v %v", v.Attention, v.Kept)
	}
	hs.HostAttentionKept(session.AttentionWorking, "", session.SourceAPI, "", nil, "bbbbbbbbbbbbbbbb", false, "", "", []string{a})
	if v := hs.HostView(); v.Attention.State != session.AttentionWorking || !slices.Equal(v.Kept, []string{a}) {
		t.Fatalf("once the host told it: %+v %v", v.Attention, v.Kept)
	}
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	if v := hs.HostView(); !v.Status.Ended() || v.Attention.State != hs.Info().Attention.State {
		t.Fatalf("ended: %+v", v)
	}
}
