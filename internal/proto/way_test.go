package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// The way message: its two ways and its noted move of the closed set, its
// protocol and its words within their bounds (the largest within MaxControl),
// one past either refused; the welcome's noted absent while none is.
func TestWayMessageBounds(t *testing.T) {
	ok := []Way{
		{T: CtlWay, Way: WayTerminal},
		{T: CtlWay, Way: WayProtocol, Protocol: "claude-stream-json"},
		{T: CtlWay, Way: WayTerminal, Noted: WayProtocol, Why: "Moves to Chat when this turn ends."},
		{T: CtlWay, Way: WayProtocol, Protocol: strings.Repeat("p", 32), Noted: WayTerminal, Why: strings.Repeat("é", 100)},
	}
	for _, m := range ok {
		if !m.Valid() {
			t.Errorf("%+v refused", m)
		}
		f, err := EncodeWay(m)
		if err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
		if len(f) > MaxControl {
			t.Fatalf("a way frame of %d bytes", len(f))
		}
		var back Way
		if err := json.Unmarshal(f[1:], &back); err != nil || back != m {
			t.Fatalf("read back %+v (%v), want %+v", back, err, m)
		}
	}
	bad := []Way{
		{T: CtlWay, Way: ""},
		{T: CtlWay, Way: "chat"},
		{T: "status", Way: WayTerminal},
		{T: CtlWay, Way: WayTerminal, Noted: "chat"},
		{T: CtlWay, Way: WayProtocol, Protocol: strings.Repeat("p", 33)},
		{T: CtlWay, Way: WayProtocol, Protocol: "two words"},
		{T: CtlWay, Way: WayTerminal, Why: strings.Repeat("w", 201)},
		{T: CtlWay, Way: WayTerminal, Why: "a line\nbreak"},
		{T: CtlWay, Way: WayTerminal, Why: "\xff"},
	}
	for _, m := range bad {
		if m.Valid() {
			t.Errorf("%+v taken", m)
		}
		if m.T == CtlWay {
			if _, err := EncodeWay(m); err == nil {
				t.Errorf("%+v encoded", m)
			}
		}
	}
	b, _ := json.Marshal(Way{T: CtlWay, Way: WayTerminal})
	if want := `{"t":"way","way":"terminal"}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	// The welcome: nothing noted, nothing said (an older owner's shape).
	w := Welcome{T: CtlWelcome, Proto: ProtoVersion, SessionID: "s1", Role: "view", Status: "running", Transport: TransportWS}
	b, _ = json.Marshal(w)
	if strings.Contains(string(b), "noted") {
		t.Fatalf("a welcome with nothing noted: %s", b)
	}
	w.Noted = WayProtocol
	b, _ = json.Marshal(w)
	if !strings.HasSuffix(string(b), `"fileView":false,"noted":"protocol"}`) {
		t.Fatalf("a welcome with a move noted: %s", b)
	}
}
