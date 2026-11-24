package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A chat's wire inside the session (docs/plans/chat-or-terminal.md, row 3).

// wireFrame is one frame a viewer was written, read for the test.
type wireFrame struct {
	typ     byte
	t       string // a control's or a TRANSCRIPT message's
	msg     map[string]any
	entries []TranscriptEntry // a page's, or an entry message's one
	more    bool
}

func readWire(t *testing.T, frame []byte) wireFrame {
	t.Helper()
	f, err := proto.Decode(frame)
	if err != nil {
		t.Fatalf("a frame the viewer was written: %v", err)
	}
	w := wireFrame{typ: f.Type}
	if f.Type != proto.TypeControl && f.Type != proto.TypeTranscript {
		return w
	}
	if err := json.Unmarshal(f.Payload, &w.msg); err != nil {
		t.Fatalf("a frame of 0x%02x: %v", f.Type, err)
	}
	w.t, _ = w.msg["t"].(string)
	w.more, _ = w.msg["more"].(bool)
	if f.Type == proto.TypeTranscript {
		var m struct {
			Entry   *TranscriptEntry  `json:"entry"`
			Entries []TranscriptEntry `json:"entries"`
		}
		if err := json.Unmarshal(f.Payload, &m); err != nil {
			t.Fatalf("a TRANSCRIPT frame: %v", err)
		}
		w.entries = m.Entries
		if m.Entry != nil {
			w.entries = []TranscriptEntry{*m.Entry}
		}
	}
	return w
}

// wireOf is every frame sink was written once sub has been handed all
// that was sent to it.
func wireOf(t *testing.T, sub *Subscription, sink *chanSink) []wireFrame {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !sub.drained() {
		if time.Now().After(deadline) {
			t.Fatal("the viewer was not handed what was sent to it")
		}
		select {
		case <-sink.writeCh:
		case <-time.After(time.Millisecond):
		}
	}
	sink.mu.Lock()
	frames := append([][]byte(nil), sink.frames...)
	sink.mu.Unlock()
	out := make([]wireFrame, 0, len(frames))
	for _, f := range frames {
		out = append(out, readWire(t, f))
	}
	return out
}

// I20 and the resume point: a viewer attaching with the mark it holds is
// sent, after its ready and the activity and chat replays, every open
// request's entry first, then every entry changed after the mark in mark
// order (a request withdrawn and a tool that ended while it was away drawn
// ended), then live; a point the ring no longer covers gets the newest
// within the budget with more; an open request's entry is sent whatever
// the point and the budget.
func TestReplayAfterResumePoint(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	owner := attachRole(t, s, RoleControl)
	if _, err := s.Chat(owner, proto.ChatPost{T: proto.CtlChat, Text: "before the drop"}); err != nil {
		t.Fatal(err)
	}
	f.TurnStarted("")
	f.Permission(PermissionRequest{Ref: "p-open", Call: "c-open", Tool: "Bash", Input: []byte(`{"command":"make"}`)})
	f.Permission(PermissionRequest{Ref: "p-gone", Call: "c-gone", Tool: "Bash", Input: []byte(`{"command":"rm -rf build"}`)})
	f.ToolStarted(ToolStep{Ref: "c-run", Name: "Bash", Input: []byte(`{"command":"go test"}`)})
	for i := range 30 {
		f.Text(fmt.Sprintf("m%d", i), "words", true)
	}
	s.mu.Lock()
	point := s.transcript.mark
	s.mu.Unlock()
	// While the viewer is away: a request withdrawn, a tool ended, words.
	f.PermissionWithdrawn("p-gone")
	f.ToolEnded("c-run", ToolResult{Status: ToolOK, Output: []byte("ok")})
	f.Text("after", "after the drop", true)

	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Ada", TranscriptAfter: point}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	frames := wireOf(t, sub, sink)
	if frames[0].t != proto.CtlWelcome || frames[0].msg["way"] != WayProtocol || frames[0].msg["interrupt"] != true || frames[0].msg["scrollbackBytes"] != float64(0) {
		t.Fatalf("the welcome %v", frames[0].msg)
	}
	tr, _ := frames[0].msg["transcript"].(map[string]any)
	if tr == nil || tr["mark"] == nil || tr["first"] != float64(1) {
		t.Fatalf("the welcome's transcript %v", frames[0].msg["transcript"])
	}
	var ready, lastActivity, history, firstPage = -1, -1, -1, -1
	for i, w := range frames {
		switch {
		case w.typ == proto.TypeScrollback:
			t.Fatal("a chat sent SCROLLBACK")
		case w.t == proto.CtlReady:
			ready = i
		case w.t == proto.CtlActivity && firstPage < 0:
			lastActivity = i
		case w.t == proto.CtlChatHistory:
			history = i
		case w.typ == proto.TypeTranscript && firstPage < 0:
			firstPage = i
		}
	}
	if ready < 0 || history < 0 || firstPage < 0 || !(ready < lastActivity && lastActivity < history && history < firstPage) {
		t.Fatalf("ready %d, the activity's last %d, the chat %d, the transcript %d", ready, lastActivity, history, firstPage)
	}
	var got []TranscriptEntry
	for _, w := range frames[firstPage:] {
		if w.typ != proto.TypeTranscript {
			continue
		}
		if w.t != proto.TranscriptPageT || w.msg["reqId"] != nil || w.more {
			t.Fatalf("a replay page %v", w.msg)
		}
		got = append(got, w.entries...)
	}
	if len(got) == 0 || got[0].Kind != EntryPermission || got[0].Ref != "p-open" || got[0].State != PermissionOpen {
		t.Fatalf("the open request is not first: %+v", got)
	}
	want := 0
	for _, e := range allEntries(t, s) {
		if e.Mark > point {
			want++
		}
	}
	rest := got[1:]
	if len(rest) != want {
		t.Fatalf("%d entries replayed after the open request, %d changed after the mark", len(rest), want)
	}
	var withdrawn, ended, words bool
	for i, e := range rest {
		if e.Mark <= point || i > 0 && e.Mark <= rest[i-1].Mark {
			t.Fatalf("entry %d (mark %d) out of the mark's order or not after %d", e.Seq, e.Mark, point)
		}
		switch {
		case e.Ref == "p-gone":
			withdrawn = e.State == PermissionWithdrawn
		case e.Ref == "c-run":
			ended = e.Status == ToolOK
		case e.Ref == "after":
			words = e.Text == "after the drop"
		}
	}
	if !withdrawn || !ended || !words {
		t.Fatalf("withdrawn %v, ended %v, the words %v: %+v", withdrawn, ended, words, rest)
	}
	// Then live.
	n := len(frames)
	f.Text("live", "now", true)
	frames = wireOf(t, sub, sink)
	if len(frames) != n+1 || frames[n].t != proto.TranscriptEntryT || frames[n].entries[0].Ref != "live" {
		t.Fatalf("the live entry: %+v", frames[n:])
	}

	// A viewer holding everything is sent the open request alone.
	s.mu.Lock()
	now := s.transcript.mark
	s.mu.Unlock()
	sink = newChanSink(false)
	sub2, err := s.AttachWith(AttachOptions{Role: RoleView, TranscriptAfter: now}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub2) })
	got = nil
	for _, w := range wireOf(t, sub2, sink) {
		got = append(got, w.entries...)
	}
	if len(got) != 1 || got[0].Ref != "p-open" {
		t.Fatalf("a viewer up to date was sent %+v", got)
	}

	// Past the ring: 4 KiB words until the ring has dropped entries; the
	// point it no longer covers gets the newest within the budget, with
	// more, the open request first and outside the budget.
	big := strings.Repeat("w", MaxEntryText)
	for i := range 700 {
		f.Text(fmt.Sprintf("big%d", i), big, true)
	}
	s.mu.Lock()
	dropped, newest := s.transcript.droppedMark, s.transcript.mark
	s.mu.Unlock()
	if dropped <= point {
		t.Fatalf("the ring dropped up to mark %d only", dropped)
	}
	for _, after := range []uint64{point, 0, newest + 5} {
		sink = newChanSink(false)
		sub3, err := s.AttachWith(AttachOptions{Role: RoleView, TranscriptAfter: after}, sink)
		if err != nil {
			t.Fatal(err)
		}
		var pages []wireFrame
		for _, w := range wireOf(t, sub3, sink) {
			if w.typ == proto.TypeTranscript {
				pages = append(pages, w)
			}
		}
		s.Detach(sub3)
		got, size := []TranscriptEntry(nil), 0
		for _, w := range pages {
			if !w.more {
				t.Fatalf("after %d: a page without more", after)
			}
			got = append(got, w.entries...)
		}
		if len(got) < 2 || got[0].Ref != "p-open" {
			t.Fatalf("after %d: the open request is not first: %d entries", after, len(got))
		}
		for _, e := range got[1:] {
			size += len(entryJSON(e))
		}
		if size > proto.TranscriptReplayBytes || size <= proto.TranscriptReplayBytes-proto.MaxTranscriptEntry {
			t.Fatalf("after %d: %d bytes replayed", after, size)
		}
		if last := got[len(got)-1]; last.Mark != newest {
			t.Fatalf("after %d: the newest replayed is mark %d of %d", after, last.Mark, newest)
		}
	}
}

// A ring that no longer holds every change after a viewer's mark says so
// (more) though all it keeps fits the budget; a viewer whose mark it
// covers is sent the changes alone, more not said.
func TestReplaySaysWhatTheRingDropped(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	for i := range MaxTranscriptEntries + 1 {
		f.Notice("n", fmt.Sprint(i))
	}
	s.mu.Lock()
	dropped, mark := s.transcript.droppedMark, s.transcript.mark
	s.mu.Unlock()
	if dropped == 0 {
		t.Fatal("the ring dropped nothing")
	}
	for _, c := range []struct {
		after uint64
		n     int
		more  bool
	}{{0, MaxTranscriptEntries, true}, {dropped - 1, MaxTranscriptEntries, true}, {dropped, MaxTranscriptEntries, false}, {mark - 2, 2, false}} {
		sink := newChanSink(false)
		sub, err := s.AttachWith(AttachOptions{Role: RoleView, TranscriptAfter: c.after}, sink)
		if err != nil {
			t.Fatal(err)
		}
		var got []TranscriptEntry
		for _, w := range wireOf(t, sub, sink) {
			if w.typ == proto.TypeTranscript {
				if w.more != c.more {
					t.Fatalf("after %d: more %v", c.after, w.more)
				}
				got = append(got, w.entries...)
			}
		}
		s.Detach(sub)
		if len(got) != c.n {
			t.Fatalf("after %d: %d entries, want %d", c.after, len(got), c.n)
		}
	}
}

// largestEntries are an entry of each kind at the session's bounds, every
// word of characters JSON writes as two bytes, every number at its widest.
func largestEntries() []TranscriptEntry {
	q := func(n int) string { return strings.Repeat(`"`, n) }
	ls := strings.Repeat(" ", maxPromptName)
	at := time.Date(9999, 12, 31, 23, 59, 59, 999e6, time.UTC)
	exit, ms := math.MinInt, int64(math.MinInt64)
	base := func(kind string) TranscriptEntry {
		return TranscriptEntry{Seq: math.MaxUint64, Mark: math.MaxUint64, Rev: math.MaxUint32, At: at, Kind: kind, Turn: math.MaxUint32,
			Ref: q(MaxEntryRef), Parent: q(MaxEntryRef)}
	}
	text := base(EntryText)
	text.Text, text.Final = q(MaxEntryText), true
	prompt := base(EntryPrompt)
	prompt.By, prompt.ByName, prompt.Text, prompt.State, prompt.Answers = "conductor", ls, q(MaxEntryText), PromptUnconfirmed, q(proto.MaxQuestionID)
	tool := base(EntryTool)
	tool.Tool, tool.Summary, tool.InputBytes, tool.InputSha256 = q(MaxEventTool), q(MaxEntrySummary), math.MaxInt, strings.Repeat("f", 64)
	tool.Status, tool.Exit, tool.DurationMs, tool.Measured, tool.OutputBytes = ToolInterrupted, &exit, &ms, "conductor", math.MaxInt
	file := base(EntryFile)
	file.Path, file.Outside, file.Op, file.Added, file.Removed, file.Tool = q(MaxEventPath), true, ChangeCreate, math.MaxInt, math.MaxInt, q(MaxEntryRef)
	perm := base(EntryPermission)
	perm.Question, perm.Call, perm.Tool, perm.Plain, perm.Summary, perm.Cwd = q(proto.MaxQuestionID), q(MaxEntryRef), q(MaxEventTool), "command", q(MaxEntrySummary), q(MaxEventPath)
	perm.InputBytes, perm.InputSha256, perm.Choices, perm.State, perm.ByName = math.MaxInt, strings.Repeat("f", 64), []string{"allow_once", "deny"}, PermissionWithdrawn, ls
	perm.Why = TooManyRequestsWords
	turn := base(EntryTurn)
	turn.Phase, turn.Outcome, turn.DurationMs = "end", "interrupted", &ms
	turn.Usage = &TurnUsage{InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64, CostUSD: -math.MaxFloat64}
	notice := base(EntryNotice)
	notice.Code, notice.Words = q(MaxNoticeCode), q(MaxNoticeWords)
	return []TranscriptEntry{text, prompt, tool, file, perm, turn, notice}
}

// The largest entry the session sends is the committed fixture
// (internal/proto's TestTranscriptFrameBounds reads it): every kind at its
// bounds fits proto.MaxTranscriptEntry with nothing cut, and a word that
// would not fit is cut to fit, never sent past the bound. Run with
// CONDUCTOR_WRITE_FIXTURE=1 to write the fixture after a bound changes.
func TestTheLargestEntryIsTheFixture(t *testing.T) {
	var largest []byte
	for _, e := range largestEntries() {
		whole, err := proto.MarshalRaw(wireEntry(e))
		if err != nil {
			t.Fatal(err)
		}
		b := entryJSON(e)
		if !bytes.Equal(b, whole) {
			t.Fatalf("a %s entry at its bounds was cut: %d bytes of %d", e.Kind, len(b), len(whole))
		}
		f := entryFrame(e)
		if len(f)-1 > proto.MaxTranscriptEntry {
			t.Fatalf("a %s entry's message is %d bytes", e.Kind, len(f)-1)
		}
		if len(f) > len(largest) {
			largest = f
		}
	}
	const fixture = "../proto/testdata/transcript-max.json"
	if os.Getenv("CONDUCTOR_WRITE_FIXTURE") == "1" {
		if err := os.WriteFile(fixture, append(bytes.Clone(largest[1:]), '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(raw, "\n"), largest[1:]) {
		t.Fatalf("the fixture is not the largest entry (%d bytes, the fixture %d): run with CONDUCTOR_WRITE_FIXTURE=1", len(largest)-1, len(raw))
	}

	// Words past every bound, of control characters and broken bytes: cut
	// to fit, cleaned, still the entry.
	over := TranscriptEntry{Seq: 7, Mark: 9, Kind: EntryText, Text: strings.Repeat("\"\x01\xff", 3*proto.MaxTranscriptEntry), Ref: "r\x00\x1b[31m"}
	f := entryFrame(over)
	if len(f)-1 > proto.MaxTranscriptEntry {
		t.Fatalf("an entry past its bounds went as %d bytes", len(f)-1)
	}
	w := readWire(t, f)
	if len(w.entries) != 1 || w.entries[0].Seq != 7 || w.entries[0].Text == "" || strings.ContainsAny(w.entries[0].Text, "\x01�") || w.entries[0].Ref != "r[31m" {
		t.Fatalf("the entry sent %+v", w.entries)
	}
}

// Reads: after a seq, before one, a part's pieces; a range the ring no
// longer keeps is transcript_gone; a terminal session has no chat; at most
// four replies of a connection wait to be written to it.
func TestTranscriptGetReads(t *testing.T) {
	s, _, f := protocolSession(t, Options{})
	for i := range 10 {
		f.Text(fmt.Sprintf("m%d", i), fmt.Sprintf("words %d", i), true)
	}
	out := bytes.Repeat([]byte("0123456789"), 20<<10)
	f.ToolStarted(ToolStep{Ref: "c1", Name: "Bash", Input: []byte(`{"command":"seq"}`)})
	f.ToolEnded("c1", ToolResult{Status: ToolOK, Output: out})
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleView, TranscriptAfter: math.MaxUint64}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Detach(sub) })
	n := len(wireOf(t, sub, sink))
	ask := func(m proto.TranscriptGet) wireFrame {
		t.Helper()
		m.T = proto.CtlTranscriptGet
		if err := s.TranscriptGet(sub, m); err != nil {
			t.Fatalf("%+v: %v", m, err)
		}
		frames := wireOf(t, sub, sink)
		if len(frames) != n+1 {
			t.Fatalf("%+v: %d frames", m, len(frames)-n)
		}
		n++
		return frames[n-1]
	}
	if p := ask(proto.TranscriptGet{ReqID: "a", After: 3, Limit: 4}); p.t != proto.TranscriptPageT || p.msg["reqId"] != "a" || len(p.entries) != 4 || p.entries[0].Seq != 4 || !p.more {
		t.Fatalf("after 3: %+v", p)
	}
	if p := ask(proto.TranscriptGet{ReqID: "b", Before: 6, Limit: 3}); len(p.entries) != 3 || p.entries[0].Seq != 3 || p.entries[2].Seq != 5 || !p.more {
		t.Fatalf("before 6: %+v", p)
	}
	if p := ask(proto.TranscriptGet{ReqID: "c", Before: 3}); len(p.entries) != 2 || p.entries[0].Seq != 1 || p.more {
		t.Fatalf("before 3: %+v", p)
	}
	var whole []byte
	for off := int64(0); ; {
		p := ask(proto.TranscriptGet{ReqID: "p", Part: &proto.TranscriptPartRef{Seq: 11, Name: proto.TranscriptPartOutput, Offset: off}})
		var m proto.TranscriptPartMsg
		raw, _ := json.Marshal(p.msg)
		_ = json.Unmarshal(raw, &m)
		if m.T != proto.TranscriptPartT || m.Total != int64(len(out)) || m.Kept != int64(len(out)) || len(m.Data) > proto.MaxTranscriptData {
			t.Fatalf("a piece: %+v", p.msg)
		}
		whole = append(whole, m.Data...)
		off += int64(len(m.Data))
		if off >= m.Kept {
			break
		}
	}
	if !bytes.Equal(whole, out) {
		t.Fatalf("the part read in pieces: %d bytes of %d", len(whole), len(out))
	}
	if err := s.TranscriptGet(sub, proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "x", Part: &proto.TranscriptPartRef{Seq: 4, Name: proto.TranscriptPartInput}}); !errors.Is(err, ErrTranscriptGone) {
		t.Fatalf("a part never kept: %v", err)
	}
	if w := readWire(t, TranscriptRefused("x", ErrTranscriptGone)); w.msg["code"] != proto.ErrCodeTranscriptGone || w.msg["requestId"] != "x" || w.msg["message"] != TranscriptGoneWords {
		t.Fatalf("the refusal %v", w.msg)
	}

	// Four replies wait to be written to a connection that does not take them; the fifth is refused.
	held := newChanSink(true)
	slow, err := s.AttachWith(AttachOptions{Role: RoleView, TranscriptAfter: math.MaxUint64}, held)
	if err != nil {
		t.Fatal(err)
	}
	for i := range proto.MaxTranscriptInFlight {
		if err := s.TranscriptGet(slow, proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: fmt.Sprint(i), After: 1}); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if err := s.TranscriptGet(slow, proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "5", After: 1}); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("the fifth read: %v", err)
	}
	close(held.block)
	deadline := time.Now().Add(30 * time.Second)
	for !slow.drained() || slow.transcriptReads.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the replies written did not free their places")
		}
		<-time.After(time.Millisecond)
	}
	if err := s.TranscriptGet(slow, proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "6", After: 1}); err != nil {
		t.Fatalf("a read once the four were written: %v", err)
	}
	s.Detach(slow)

	term, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	tsub := attachRole(t, term, RoleControl)
	if err := term.TranscriptGet(tsub, proto.TranscriptGet{T: proto.CtlTranscriptGet, ReqID: "t"}); !errors.Is(err, ErrNoChat) {
		t.Fatalf("a terminal session's transcript: %v", err)
	}
	if w := readWire(t, TranscriptRefused("t", ErrNoChat)); w.msg["code"] != proto.ErrCodeNotProtocol {
		t.Fatalf("its refusal %v", w.msg)
	}
}
