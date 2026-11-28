package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The agent's own commands on a chat (docs/plans/chat-or-terminal.md,
// row 12): cleaned and bounded, kept with the chat, sent to the operator's
// own chat connection alone.

// commandsChat is a chat over a driver with a mark, on opts.
func commandsChat(t *testing.T, opts Options) (*Local, *sessiontest.FakeProtocol) {
	t.Helper()
	opts.ScrollbackBytes = 4096
	s, _ := newLocalWith(t, opts)
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, f
}

// commandsSent is every commands message sink was sent, in order.
func commandsSent(t *testing.T, sink *chanSink) []proto.TranscriptCommandsMsg {
	t.Helper()
	var out []proto.TranscriptCommandsMsg
	for i := range sink.count() {
		f, err := proto.Decode(sink.frame(i))
		if err != nil || f.Type != proto.TypeTranscript {
			continue
		}
		var h struct {
			T string `json:"t"`
		}
		if json.Unmarshal(f.Payload, &h) != nil || h.T != proto.TranscriptCommandsT {
			continue
		}
		m, err := proto.DecodeCommands(f.Payload)
		if err != nil {
			t.Fatalf("a commands message past its bounds: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// waitCommands waits until sink holds n commands messages; the bound only
// bounds a hang.
func waitCommands(t *testing.T, sink *chanSink, n int) []proto.TranscriptCommandsMsg {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got := commandsSent(t, sink); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d commands messages, have %d", n, len(commandsSent(t, sink)))
		}
		select {
		case <-sink.writeCh:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// drained waits until every frame the session has queued for sub is
// handed to its sink.
func drainedSub(t *testing.T, sub *Subscription) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !sub.drained() {
		if time.Now().After(deadline) {
			t.Fatal("the viewer was never handed what it was sent")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCommandsBoundedAndCleaned(t *testing.T) {
	var list []Command
	list = append(list,
		Command{Name: "fix issue"}, Command{Name: "two\nlines"}, Command{Name: "a@b"}, Command{Name: "x" + fromCode(0x202E) + "y"},
		Command{Name: "-dash"}, Command{Name: "a/b"}, Command{Name: strings.Repeat("n", 65)}, Command{Name: ""},
		Command{Name: "compact", Words: strings.Repeat("é", 1000), Hint: "<a>\n<b>\t" + fromCode(0x200B) + fromCode(0x2028) + strings.Repeat("h", 100),
			Aliases: []string{"compact", "squash", "sq uash", "c", "d", strings.Repeat("a", 33)}},
		Command{Name: "compact", Words: "the second of the name"},
		Command{Name: "words", Words: "one\x1b[31m two" + fromCode(0x2066) + " three\r\n\u0000four\xff"},
	)
	for i := range 10_000 {
		list = append(list, Command{Name: fmt.Sprintf("c%05d", i), Words: strings.Repeat("w", 2000)})
	}
	kept, more := cleanCommands(list)
	if len(kept) != proto.MaxCommands || !more {
		t.Fatalf("%d kept, more %v", len(kept), more)
	}
	if kept[0].Name != "compact" || kept[1].Name != "words" || kept[2].Name != "c00000" {
		t.Fatalf("the first kept: %q %q %q", kept[0].Name, kept[1].Name, kept[2].Name)
	}
	c := kept[0]
	if !utf8.ValidString(c.Words) || len(c.Words) > proto.MaxCommandWords || !strings.HasSuffix(c.Words, "…") {
		t.Fatalf("the words cut %q (%d bytes)", c.Words, len(c.Words))
	}
	if len(c.Hint) > proto.MaxCommandHint || strings.ContainsAny(c.Hint, "\n\t"+fromCode(0x200B)+fromCode(0x2028)) || !strings.HasPrefix(c.Hint, "<a> <b> hhh") {
		t.Fatalf("the hint %q", c.Hint)
	}
	if strings.Join(c.Aliases, ",") != "squash,c" {
		t.Fatalf("the aliases %q", c.Aliases)
	}
	// An alias that is another command's name, or an alias already given,
	// is dropped: a name and an alias never mean two commands.
	clash, _ := cleanCommands([]Command{
		{Name: "fix", Aliases: []string{"compact", "f"}},
		{Name: "compact", Aliases: []string{"f", "squash"}},
		{Name: "deploy", Aliases: []string{"fix", "squash", "d"}},
	})
	var got []string
	for _, c := range clash {
		got = append(got, c.Name+":"+strings.Join(c.Aliases, ","))
	}
	if strings.Join(got, " ") != "fix:f compact:squash deploy:d" {
		t.Fatalf("aliases across commands: %q", got)
	}
	if w := kept[1].Words; w != "one[31m two three four" {
		t.Fatalf("the words cleaned %q", w)
	}
	m := proto.TranscriptCommandsMsg{T: proto.TranscriptCommandsT, Rev: 1, Commands: kept, More: more}
	if !m.Valid() {
		t.Fatal("the kept list is past the message's bounds")
	}
	if _, err := proto.EncodeCommands(m); err != nil {
		t.Fatalf("the kept list does not fit a frame: %v", err)
	}
	if kept, more := cleanCommands(list[:12]); len(kept) != 3 || more {
		t.Fatalf("a short list: %d, more %v", len(kept), more)
	}
}

// Sent to the operator's own chat connection alone: after the replay as it
// attaches, on a change; never to a control link, a view link, the owner
// without the server's socket (a host's peer, a switchyard's) or a quiet
// connection; and not in Info.
func TestCommandsGoToTheOperatorAlone(t *testing.T) {
	s, f := commandsChat(t, Options{})
	op, own := attachAs(t, s, operatorChat)
	others := map[string]AttachOptions{
		"a control link": {Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true},
		"a view link":    {Role: RoleView, LinkID: "link-2", Name: "Viewer", ViewerSocket: true},
		"a peer":         {Role: RoleControl, Name: "Ada", Owner: true},
		"a quiet one":    {Role: RoleControl, Name: "Ada", Owner: true, ViewerSocket: true, ChatOnly: true},
		"not the owner":  {Role: RoleControl, Name: "Bo", ViewerSocket: true},
	}
	sinks := map[string]*chanSink{}
	subs := map[string]*Subscription{}
	for who, o := range others {
		subs[who], sinks[who] = attachAs(t, s, o)
	}
	f.Commands([]Command{{Name: "compact", Words: "Shorten the conversation"}, {Name: "fix-issue", Hint: "<number>"}})
	got := waitCommands(t, own, 1)
	if got[0].Rev != 1 || len(got[0].Commands) != 2 || got[0].Commands[1].Hint != "<number>" {
		t.Fatalf("the operator was sent %+v", got)
	}
	// A later one, attached after the list came: sent after its replay.
	_, late := attachAs(t, s, operatorChat)
	if got := waitCommands(t, late, 1); got[0].Rev != 1 {
		t.Fatalf("the late operator was sent %+v", got)
	}
	ll, lateLink := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-3", Name: "Late", ViewerSocket: true})
	drainedSub(t, op)
	drainedSub(t, ll)
	for who, sub := range subs {
		drainedSub(t, sub)
		if got := commandsSent(t, sinks[who]); len(got) != 0 {
			t.Fatalf("%s was sent the commands: %+v", who, got)
		}
	}
	if got := commandsSent(t, lateLink); len(got) != 0 {
		t.Fatalf("a late link was sent the commands: %+v", got)
	}
	raw, _ := json.Marshal(s.Info())
	if strings.Contains(string(raw), "fix-issue") {
		t.Fatalf("Info holds a command: %s", raw)
	}
	for _, e := range s.Activity() {
		if strings.Contains(e.Message, "fix-issue") {
			t.Fatalf("the activity holds a command: %+v", e)
		}
	}
}

// A changed list is sent at once when a second has passed since the last
// send, else once it has, the last list alone; an unchanged one is not
// sent again; a held send for a chat that went is never sent.
func TestCommandsChangedIsCoalesced(t *testing.T) {
	clock, timers := newStepClock(), &fakeTimers{}
	s, f := commandsChat(t, Options{Now: clock.now, AfterFunc: timers.AfterFunc})
	_, own := attachAs(t, s, operatorChat)
	f.Commands([]Command{{Name: "one"}})
	waitCommands(t, own, 1)
	clock.add(400 * time.Millisecond)
	f.Commands([]Command{{Name: "one"}, {Name: "two"}})
	f.Commands([]Command{{Name: "one"}, {Name: "two"}, {Name: "three"}})
	if n := timers.pending(600 * time.Millisecond); n != 1 {
		t.Fatalf("%d sends held, want one", n)
	}
	if got := commandsSent(t, own); len(got) != 1 {
		t.Fatalf("sent within the second: %+v", got)
	}
	clock.add(600 * time.Millisecond)
	timers.fire(600 * time.Millisecond)
	got := waitCommands(t, own, 2)
	if len(got) != 2 || got[1].Rev != 3 || len(got[1].Commands) != 3 {
		t.Fatalf("the held send: %+v", got)
	}
	// Unchanged: nothing.
	clock.add(5 * time.Second)
	f.Commands([]Command{{Name: "one"}, {Name: "two"}, {Name: "three"}})
	f.Text("m1", "words", true)
	_, after := attachAs(t, s, operatorChat)
	waitCommands(t, after, 1)
	if got := commandsSent(t, own); len(got) != 2 {
		t.Fatalf("an unchanged list was sent again: %+v", got)
	}
	// A second past the last send: at once.
	f.Commands([]Command{{Name: "four"}})
	if got := waitCommands(t, own, 3); got[2].Rev != 4 || got[2].Commands[0].Name != "four" {
		t.Fatalf("a change after the second: %+v", got)
	}
}

// The list is the chat's: a session moved back to its terminal holds none,
// and a held send of the chat that went is never sent.
func TestCommandsGoneWithTheChat(t *testing.T) {
	timers, clock := &fakeTimers{}, newStepClock()
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Kind: KindServer, Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24,
		AgentSession: &AgentSession{ID: "conv-1", Resumable: true}}, p, Options{ScrollbackBytes: 4096, Now: clock.now, AfterFunc: timers.AfterFunc})
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		p.exit()
	})
	s.SetAttention(AttentionDone, "a turn of its own", SourceAPI)
	op, own := attachAs(t, s, operatorChat)
	ln, link := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	f.IsVerbatim = true
	toChat(t, s, f, ts)
	f.Commands([]Command{{Name: "compact"}})
	waitCommands(t, own, 1)
	clock.add(100 * time.Millisecond)
	f.Commands([]Command{{Name: "compact"}, {Name: "clear"}})
	if timers.pending(900*time.Millisecond) != 1 {
		t.Fatal("no send held")
	}
	if _, err := s.SwapWay(WayTerminal, MoveStarts{Terminal: ts.start}); err != nil {
		t.Fatal(err)
	}
	settled(t, s)
	timers.fire(900 * time.Millisecond)
	bk, back := attachAs(t, s, operatorChat)
	drainedSub(t, op)
	drainedSub(t, ln)
	drainedSub(t, bk)
	if got := commandsSent(t, back); len(got) != 0 {
		t.Fatalf("a terminal session sent the commands: %+v", got)
	}
	if got := commandsSent(t, own); len(got) != 1 {
		t.Fatalf("the chat's held send went after it: %+v", got)
	}
	if got := commandsSent(t, link); len(got) != 0 {
		t.Fatalf("a link was sent the commands: %+v", got)
	}
}

// After a move to Chat, the operator's own chat connection is sent the
// commands after the way message; a link is not.
func TestCommandsFollowTheMoveToTheOperatorAlone(t *testing.T) {
	s, _ := movable(t, 0)
	_, own := attachAs(t, s, operatorChat)
	ln, link := attachAs(t, s, AttachOptions{Role: RoleControl, LinkID: "link-1", Name: "Guest", Owner: true, ViewerSocket: true})
	ts := &terminals{}
	ts.cleanup(t)
	f := sessiontest.NewFakeProtocol()
	f.IsVerbatim = true
	// The list comes as the new child begins, applied before the move ends
	// (its Begin waits for it): the change path's send reaches the operator
	// before the way message, and the list must follow the way message too.
	start := f.Start()
	listed := func(ctx context.Context, conv string) (Child, Protocol, error) {
		c, d, err := start(ctx, conv)
		return c, listsAtBegin{Protocol: d, f: f}, err
	}
	st, err := s.SwapWay(WayProtocol, MoveStarts{Protocol: listed, Terminal: ts.start, Gone: GroupGone})
	if err != nil || !st.Moving {
		t.Fatalf("SwapWay = %+v, %v", st, err)
	}
	settled(t, s)
	waitCommands(t, own, 2)
	// In order: a commands message after the way message to the chat.
	way, after := -1, false
	for i := range own.count() {
		fr, err := proto.Decode(own.frame(i))
		if err != nil {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(fr.Payload, &m)
		switch {
		case fr.Type == proto.TypeControl && m["t"] == proto.CtlWay && m["way"] == proto.WayProtocol:
			way = i
		case fr.Type == proto.TypeTranscript && m["t"] == proto.TranscriptCommandsT && way >= 0:
			after = true
		}
	}
	if way < 0 || !after {
		t.Fatalf("no commands message after the way message (way at %d)", way)
	}
	drainedSub(t, ln)
	if got := commandsSent(t, link); len(got) != 0 {
		t.Fatalf("a link was sent the commands: %+v", got)
	}
}

// listsAtBegin is a driver whose Begin hands the session the agent's
// commands and returns once they are applied: a list that comes before the
// move to its chat ends.
type listsAtBegin struct {
	Protocol
	f *sessiontest.FakeProtocol
}

func (d listsAtBegin) Begin(ctx context.Context) error {
	d.f.Commands([]Command{{Name: "compact"}})
	return d.Protocol.Begin(ctx)
}

// A connection that may no longer expand (a crew formed around the
// session) is sent an empty list after the way message that says so.
func TestCommandsWithdrawnWhenTheStandingChanges(t *testing.T) {
	s, f := commandsChat(t, Options{})
	op, own := attachAs(t, s, operatorChat)
	f.Commands([]Command{{Name: "compact"}})
	waitCommands(t, own, 1)
	s.SetCrew(CrewRef{RunID: "run-1", Member: "lead"})
	got := waitCommands(t, own, 2)
	if got[1].Rev <= got[0].Rev || len(got[1].Commands) != 0 {
		t.Fatalf("after the change: %+v", got)
	}
	f.Commands([]Command{{Name: "compact"}, {Name: "clear"}})
	drainedSub(t, op)
	if got := commandsSent(t, own); len(got) != 2 {
		t.Fatalf("a crew member's chat was sent a later list: %+v", got)
	}
}

// fromCode is the character of code point n, built from its number so that
// no character a person cannot see sits in the source as itself.
func fromCode(n rune) string { return string(n) }

func TestAConversationTakenUpAgainSaysTheAgentForgot(t *testing.T) {
	s, _ := commandsChat(t, Options{})
	const a, b = "0d9b7c2e-1f3a-4b5c-8d6e-7f8091a2b3c4", "5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f70819"
	s.ReportAgentSession(a, PolicyLatest, true)
	for _, e := range allEntries(t, s) {
		if e.Code == noticeCleared {
			t.Fatalf("the first conversation is no clear: %+v", e)
		}
	}
	// The same conversation again: nothing.
	s.ReportAgentSession(a, PolicyLatest, true)
	// Another one in its place (the agent's /clear): one notice, in its words.
	s.ReportAgentSession(b, PolicyLatest, false)
	if e := lastEntry(t, s); e.Kind != EntryNotice || e.Code != noticeCleared || e.Words != ClearedWords {
		t.Fatalf("last entry %+v, want the cleared notice", e)
	}
	n := 0
	for _, e := range allEntries(t, s) {
		if e.Code == noticeCleared {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d cleared notices, want 1", n)
	}
	// An id the lowest policy does not take changes nothing.
	s.ReportAgentSession("ffffffff-ffff-4fff-bfff-ffffffffffff", PolicyLowest, false)
	if e := lastEntry(t, s); e.Words != ClearedWords {
		t.Fatalf("last entry %+v after an id not taken", e)
	}
}
