package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The committed fixture is the largest entry a session sends, as it sends
// it (internal/session's TestTheLargestEntryIsTheFixture makes the same
// bytes): it fits MaxTranscriptEntry; a page of as many of it as fit the
// page's room, the largest header with them, fits MaxTranscript, and so
// does a piece of a part at its bounds; one byte past MaxTranscript is
// refused by Decode.
func TestTranscriptFrameBounds(t *testing.T) {
	raw, err := os.ReadFile("testdata/transcript-max.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimRight(raw, "\n")
	if len(raw) > MaxTranscriptEntry {
		t.Fatalf("the largest entry is %d bytes, past %d", len(raw), MaxTranscriptEntry)
	}
	f, err := Decode(Encode(TypeTranscript, raw))
	if err != nil || f.Type != TypeTranscript {
		t.Fatalf("the fixture's frame: %v", err)
	}
	var m TranscriptEntryMsg
	if err := json.Unmarshal(f.Payload, &m); err != nil || m.T != TranscriptEntryT || len(m.Entry) == 0 {
		t.Fatalf("the fixture: %+v %v", m, err)
	}

	// A page: the entries as many as the room takes, the header at its largest.
	page := TranscriptPageMsg{T: TranscriptPageT, ReqID: strings.Repeat("r", MaxTranscriptReqID), First: ^uint64(0), Last: ^uint64(0), More: true}
	used := -1
	for used+1+len(m.Entry) <= TranscriptPageRoom {
		page.Entries = append(page.Entries, m.Entry)
		used += 1 + len(m.Entry)
	}
	frame, err := EncodeTranscript(page)
	if err != nil {
		t.Fatalf("the largest page: %v", err)
	}
	if _, err := Decode(frame); err != nil {
		t.Fatalf("the largest page decoded: %v", err)
	}
	// The room is exact: a page whose entries take it all fits, one byte more does not.
	pad := func(n int) TranscriptPageMsg {
		p := page
		p.Entries = []json.RawMessage{json.RawMessage(`"` + strings.Repeat("x", n-2) + `"`)}
		return p
	}
	if _, err := EncodeTranscript(pad(TranscriptPageRoom)); err != nil {
		t.Fatalf("a page that fills its room: %v", err)
	}
	if _, err := EncodeTranscript(pad(TranscriptPageRoom + 1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a page one byte past its room: %v", err)
	}

	// A part's piece at its bounds.
	part := TranscriptPartMsg{T: TranscriptPartT, ReqID: strings.Repeat("r", MaxTranscriptReqID), Seq: ^uint64(0), Name: TranscriptPartOutput,
		Offset: 1 << 40, Total: 1 << 40, Kept: 1 << 40, Data: bytes.Repeat([]byte{0xff}, MaxTranscriptData), Cut: true}
	frame, err = EncodeTranscript(part)
	if err != nil {
		t.Fatalf("the largest piece: %v", err)
	}
	if _, err := Decode(frame); err != nil {
		t.Fatalf("the largest piece decoded: %v", err)
	}

	if _, err := Decode(Encode(TypeTranscript, bytes.Repeat([]byte(" "), MaxTranscript))); err != nil {
		t.Fatalf("a frame at the bound: %v", err)
	}
	if _, err := Decode(Encode(TypeTranscript, bytes.Repeat([]byte(" "), MaxTranscript+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte past: %v", err)
	}
}

// transcript_get and interrupt name a reqId of 1 to 32 printable bytes; a
// read asks for at most 100 entries, a part by a name of the closed set,
// alone; both are read strictly.
func TestTranscriptGetBounds(t *testing.T) {
	id := strings.Repeat("r", MaxTranscriptReqID)
	ok := []string{
		`{"t":"transcript_get","reqId":"` + id + `"}`,
		`{"t":"transcript_get","reqId":"r","after":40}`,
		`{"t":"transcript_get","reqId":"r","before":41,"after":3,"limit":100}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":3,"name":"input"}}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":3,"name":"output","offset":97280}}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":3,"name":"diff"}}`,
	}
	for _, s := range ok {
		if _, err := DecodeTranscriptGet([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := []string{
		`{"t":"transcript_get"}`,
		`{"t":"transcript_get","reqId":""}`,
		`{"t":"transcript_get","reqId":"` + id + `r"}`,
		`{"t":"transcript_get","reqId":"a b"}`,
		`{"t":"transcript_get","reqId":"r","limit":101}`,
		`{"t":"transcript_get","reqId":"r","limit":-1}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":3,"name":"stderr"}}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":0,"name":"input"}}`,
		`{"t":"transcript_get","reqId":"r","part":{"seq":3,"name":"input","offset":-1}}`,
		`{"t":"transcript_get","reqId":"r","after":2,"part":{"seq":3,"name":"input"}}`,
		`{"t":"transcript_get","reqId":"r","after":-1}`,
		`{"t":"transcript_get","reqId":"r","later":true}`,
		`{"t":"transcript_get","reqId":"r"}]`,
		`{"t":"interrupt","reqId":"r"}`,
	}
	for _, s := range bad {
		if _, err := DecodeTranscriptGet([]byte(s)); err == nil {
			t.Errorf("%s was taken", s)
		}
	}
	if _, err := DecodeInterrupt([]byte(`{"t":"interrupt","reqId":"` + id + `"}`)); err != nil {
		t.Errorf("an interrupt at its bound: %v", err)
	}
	for _, s := range []string{`{"t":"interrupt"}`, `{"t":"interrupt","reqId":"` + id + `r"}`, `{"t":"interrupt","reqId":"a\u0001"}`, `{"t":"interrupt","reqId":"r","now":true}`, `{"t":"transcript_get","reqId":"r"}`} {
		if _, err := DecodeInterrupt([]byte(s)); err == nil {
			t.Errorf("%s was taken", s)
		}
	}
	if got := RequestIDOf([]byte(`{"t":"interrupt","reqId":"r1","now":true}`)); got != "r1" {
		t.Errorf("the request named by a refused interrupt: %q", got)
	}
	if got := RequestIDOf([]byte(`{"t":"interrupt","reqId":"` + id + `r"}`)); got != "" {
		t.Errorf("a request id past its bound is named: %q", got)
	}
}

// A relay envelope may carry a TRANSCRIPT frame, from a publisher to the
// switchyard and from it to the viewer, within its bound.
func TestTranscriptRelayable(t *testing.T) {
	inner, err := EncodeTranscript(TranscriptEntryMsg{T: TranscriptEntryT, Entry: json.RawMessage(`{"seq":1,"mark":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	env, err := EncodeRelay("0123456789abcdef", inner)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(env)
	if err != nil || f.Type != TypeRelay {
		t.Fatalf("the envelope: %v", err)
	}
	viewer, got, err := DecodeRelay(f.Payload)
	if err != nil || viewer != "0123456789abcdef" || got.Type != TypeTranscript || !bytes.Equal(got.Payload, inner[1:]) {
		t.Fatalf("relayed: %q %+v %v", viewer, got, err)
	}
	big := Encode(TypeTranscript, bytes.Repeat([]byte(" "), MaxTranscript+1))
	env, _ = EncodeRelay("0123456789abcdef", big)
	if _, _, err := DecodeRelay(env[1:]); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a relayed frame past its bound: %v", err)
	}
}

// An older server's registered is byte for byte what it was: the word is
// absent unless the server says it, and then it is the last field.
func TestRegisteredOmitsTranscript(t *testing.T) {
	r := Registered{T: HostRegistered, SessionID: "s1", Secret: "x", ShareBaseURL: "https://yard.example.net", Resumed: true,
		Links: []string{"l1"}, Decisions: true, SessionInfo: true, AgentSessions: true, ActivitySeq: true, Renews: true}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	const before = `{"t":"registered","sessionId":"s1","secret":"x","shareBaseUrl":"https://yard.example.net","resumed":true,"links":["l1"],"decisions":true,"sessionInfo":true,"agentSessions":true,"activitySeq":true,"renews":true}`
	if string(b) != before {
		t.Fatalf("without the word\n got %s\nwant %s", b, before)
	}
	r.Transcript = true
	b, _ = json.Marshal(r)
	if want := strings.TrimSuffix(before, "}") + `,"transcript":true}`; string(b) != want {
		t.Fatalf("with the word\n got %s\nwant %s", b, want)
	}
	var old Registered
	if err := json.Unmarshal([]byte(before), &old); err != nil || old.Transcript {
		t.Fatalf("an older server's registered read: %+v %v", old, err)
	}
}

// A hello that holds no transcript, and a terminal session's welcome, are
// byte for byte what they were; the chat's words are the welcome's last.
func TestHelloAndWelcomeOmitTheChat(t *testing.T) {
	b, _ := json.Marshal(Hello{T: CtlHello, Proto: ProtoVersion, Cols: 80, Rows: 24, Client: "web/1"})
	if want := `{"t":"hello","proto":1,"cols":80,"rows":24,"client":"web/1"}`; string(b) != want {
		t.Fatalf("hello\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(Hello{T: CtlHello, Proto: ProtoVersion, TranscriptAfter: 40})
	if want := `{"t":"hello","proto":1,"cols":0,"rows":0,"transcriptAfter":40}`; string(b) != want {
		t.Fatalf("hello with a mark\n got %s\nwant %s", b, want)
	}
	w := Welcome{T: CtlWelcome, Proto: ProtoVersion, SessionID: "s1", Role: "view", Status: "running", Transport: TransportWS}
	b, _ = json.Marshal(w)
	const terminal = `{"t":"welcome","proto":1,"sessionId":"s1","role":"view","cols":0,"rows":0,"status":"running","scrollbackBytes":0,"transport":"ws","fileView":false}`
	if string(b) != terminal {
		t.Fatalf("a terminal's welcome\n got %s\nwant %s", b, terminal)
	}
	w.Way, w.Verbatim, w.Transcript, w.Interrupt = "protocol", true, &WelcomeTranscript{First: 2, Last: 9, Mark: 14}, true
	b, _ = json.Marshal(w)
	if want := strings.TrimSuffix(terminal, "}") + `,"way":"protocol","verbatim":true,"transcript":{"first":2,"last":9,"mark":14},"interrupt":true}`; string(b) != want {
		t.Fatalf("a chat's welcome\n got %s\nwant %s", b, want)
	}
}
