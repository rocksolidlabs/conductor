package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A host's numbered entries are taken once each: one sent again (a
// resuming connection's) is dropped, whatever it says; an unnumbered one
// (an older host's) is taken as before. A count of entries left out is
// recorded before the entry that carries it, bounded; the record shows
// what a person reads.
func TestAHostsNumberedEntriesAreTakenOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	hs := registerReporting(t, e, "1", "sh", "", proto.HostReport{})
	id := hs.Info().ID
	at := time.Now().UTC().Format(time.RFC3339Nano)
	send := func(seq uint64, leftOut uint32, msg string) {
		t.Helper()
		b, _ := json.Marshal(proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{Type: "progress", Message: msg, At: at}, Seq: seq, LeftOut: leftOut})
		if !e.srv.handleHostMessage(hs, &hostConnState{}, proto.HostActivity, b) {
			t.Fatal("closed the connection")
		}
	}
	send(1, 0, "one")
	send(2, 0, "two")
	send(1, 0, "one again")
	send(2, 0, "two again")
	send(0, 0, "unnumbered")
	send(5, 2, "five")
	send(6, proto.MaxActivityLeftOut+9, "six")
	r, _, err := e.srv.sessionRecords.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range messagesOf(r) {
		if m != "" {
			got = append(got, m)
		}
	}
	want := []string{"one", "two", "unnumbered",
		"2 activity entries of the host's may be missing here: recorded while its connection to this server was down, and not sent", "five",
		"1048576 activity entries of the host's may be missing here: recorded while its connection to this server was down, and not sent", "six"}
	if !slices.Equal(got, want) {
		t.Fatalf("the record holds %q\nwant %q", got, want)
	}
	for _, en := range r.Entries {
		if strings.Contains(en.Message, "may be missing") && en.Kind == "report" {
			t.Fatalf("the notice reads as the agent's: %+v", en)
		}
	}
}

// A session's registered says the server takes the entries' numbers; a
// switchyard's does not (it keeps no activity).
func TestRegisteredSaysTheServerTakesActivitySeq(t *testing.T) {
	e := newTestEnv(t, nil)
	if r := registeredOf(t, e); !r.ActivitySeq {
		t.Fatalf("registered %+v", r)
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if r := registeredOf(t, yard); r.ActivitySeq || r.SessionID == "" {
		t.Fatalf("a switchyard's registered %+v", r)
	}
}

// registeredOf registers a host on e and returns what the server answered.
func registeredOf(t *testing.T, e *testEnv) proto.Registered {
	t.Helper()
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	hc, _, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-host-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hc.CloseNow() })
	reg, _ := json.Marshal(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"},
		Session: proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}})
	if err := hc.Write(t.Context(), websocket.MessageText, reg); err != nil {
		t.Fatal(err)
	}
	_, data, err := hc.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var registered proto.Registered
	if err := json.Unmarshal(data, &registered); err != nil {
		t.Fatalf("registered %s: %v", data, err)
	}
	return registered
}

// A host's late entry is recorded and offered to no sink; the retell of a
// question asked while the host was away (Retold) pages once, from the
// session's state, when its copy does not hold that question already: not
// again for the same question, not for a retell that is not one, nor for
// another state.
func TestALateEntryPagesNoOneAndARetoldQuestionPagesOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	var paged []string
	e.srv.events.addSink(func(_ string, en session.ActivityEntry, st session.AttentionState) {
		mu.Lock()
		paged = append(paged, string(st)+":"+en.Message)
		mu.Unlock()
	})
	pagedNow := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paged)
	}
	hs := registerReporting(t, e, "1", "sh", "", proto.HostReport{})
	at := time.Now().UTC().Format(time.RFC3339Nano)
	host := func(t string, v any) {
		b, _ := json.Marshal(v)
		if !e.srv.handleHostMessage(hs, &hostConnState{}, t, b) {
			panic("closed the connection")
		}
	}
	host(proto.HostActivity, proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{Type: "attention", Message: "Deploy?", At: at}, State: "needs_input", Seq: 1, Late: true})
	host(proto.HostActivity, proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{Type: "tool_denied", Tool: "Bash", Message: "late", At: at}, Seq: 2, Late: true})
	host(proto.HostActivity, proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{Type: "progress", Message: "live", At: at}, Seq: 3})
	if got := pagedNow(); !slices.Equal(got, []string{":live"}) {
		t.Fatalf("offered %q", got)
	}
	r, _, err := e.srv.sessionRecords.Get(hs.Info().ID)
	if err != nil || !slices.Contains(messagesOf(r), "late") {
		t.Fatalf("the late entry was not recorded: %q %v", messagesOf(r), err)
	}
	ask := func(id string, retold bool, state string) {
		host(proto.HostAttention, proto.HostAttentionMsg{T: proto.HostAttention, SessionID: hs.Info().ID, State: state, Message: "Ship it?", Source: session.SourceAPI, ID: id, Retold: retold})
	}
	ask("q1", true, "needs_input")
	ask("q1", true, "needs_input")
	ask("q2", false, "needs_input")
	ask("q3", true, "working")
	if got := pagedNow(); !slices.Equal(got, []string{":live", "needs_input:Ship it?"}) {
		t.Fatalf("offered %q", got)
	}
}

// The retell of a question pages once unless someone was offered that
// question live: a question the copy showed whose entry came late pages
// once on its retell, and once in all however often it is retold; one
// whose needs_input entry was offered live, before or after the copy
// showed it, is not paged again by its retell.
func TestARetoldQuestionPagesUnlessItWasOfferedLive(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	var paged []string
	e.srv.events.addSink(func(_ string, en session.ActivityEntry, st session.AttentionState) {
		mu.Lock()
		paged = append(paged, string(st)+":"+en.Message)
		mu.Unlock()
	})
	pagedNow := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paged)
	}
	hs := registerReporting(t, e, "1", "sh", "", proto.HostReport{})
	host := func(t string, v any) {
		b, _ := json.Marshal(v)
		if !e.srv.handleHostMessage(hs, &hostConnState{}, t, b) {
			panic("closed the connection")
		}
	}
	var seq uint64
	entry := func(words string, late bool) {
		seq++
		host(proto.HostActivity, proto.HostActivityMsg{T: proto.HostActivity, Entry: proto.Activity{Type: "attention", Message: words, At: time.Now().UTC().Format(time.RFC3339Nano)}, State: "needs_input", Seq: seq, Late: late})
	}
	ask := func(id, words string, retold bool) {
		host(proto.HostAttention, proto.HostAttentionMsg{T: proto.HostAttention, SessionID: hs.Info().ID, State: "needs_input", Message: words, Source: session.SourceAPI, ID: id, Retold: retold})
	}
	// Shown while connected, its entry late (made before the connection
	// that carried it): its retell pages it, once in all.
	ask("q1", "Deploy?", false)
	entry("Deploy?", true)
	ask("q1", "Deploy?", true)
	ask("q1", "Deploy?", true)
	// Its entry offered live while the copy showed it.
	ask("q2", "Ship it?", false)
	entry("Ship it?", false)
	ask("q2", "Ship it?", true)
	// Its entry offered live before the copy showed it.
	entry("Merge?", false)
	ask("q3", "Merge?", false)
	ask("q3", "Merge?", true)
	if got := pagedNow(); !slices.Equal(got, []string{"needs_input:Deploy?", "needs_input:Ship it?", "needs_input:Merge?"}) {
		t.Fatalf("offered %q", got)
	}
}
