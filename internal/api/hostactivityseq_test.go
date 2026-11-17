package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
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
