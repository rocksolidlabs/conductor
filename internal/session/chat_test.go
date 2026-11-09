package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

func TestCleanChatText(t *testing.T) {
	for in, want := range map[string]string{
		"  hello  ":                 "hello",
		"a\r\nb":                    "a\nb",
		"a\rb\x07c\x1b[31md":        "abc[31md",
		"tab\tkept":                 "tab\tkept",
		"\xff\xfe":                  "\ufffd",
		"<b>&amp;</b>":              "<b>&amp;</b>",
		"\n\n":                      "",
		"line\u2028break":           "line\u2028break",
		"zero\x00width":             "zerowidth",
		"end with a newline\n":      "end with a newline",
		"  keep inner\n\nblanks  ":  "keep inner\n\nblanks",
		"emoji 🚂 stays":             "emoji 🚂 stays",
		"delete\x7fchar":            "deletechar",
		"nbsp\u00a0stays":           "nbsp\u00a0stays",
		"combined\u0301 accents":    "combined\u0301 accents",
		"windows\r\nlines\r\n":      "windows\nlines",
		"\t\tindented":              "indented",
		"a\x1b]0;title\x07b":        "a]0;titleb",
		"ok":                        "ok",
		strings.Repeat("x", 3000):   strings.Repeat("x", 3000),
		"form\x0cfeed":              "formfeed",
		"vertical\x0btab":           "verticaltab",
		"escape\x1bonly":            "escapeonly",
		"mixed \r\n\t ok":           "mixed \n\t ok",
		"trailing tab\t":            "trailing tab",
		"the agent said: \"yes\"":   "the agent said: \"yes\"",
		"back\\slash":               "back\\slash",
		"emoji\u200dzwj":            "emoji\u200dzwj",
		"rtl\u202eoverride":         "rtl\u202eoverride",
		"soft\u00adhyphen":          "soft\u00adhyphen",
		"\u0085next line":           "next line",
		"c1\u009bcontrol":           "c1control",
		"bom\ufeffstays":            "bom\ufeffstays",
		"中文 也 可以":                   "中文 也 可以",
		"a\n\r\nb":                  "a\n\nb",
		"last\r":                    "last",
		" \t \n ":                   "",
		"keep  double  spaces":      "keep  double  spaces",
		"url https://x.example/a?b": "url https://x.example/a?b",
	} {
		if got := CleanChatText(in); got != want {
			t.Errorf("CleanChatText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChatRingKeepsTheLastMaxChat(t *testing.T) {
	var r chatRing
	for i := range MaxChat + 7 {
		r.add(ChatMessage{ID: fmt.Sprint(i), Text: fmt.Sprint(i)})
	}
	snap := r.snapshot()
	if len(snap) != MaxChat || snap[0].ID != "7" || snap[len(snap)-1].ID != fmt.Sprint(MaxChat+6) {
		t.Fatalf("ring: %d kept, first %s, last %s", len(snap), snap[0].ID, snap[len(snap)-1].ID)
	}
	if _, ok := r.find("3"); ok {
		t.Fatal("a dropped message was found")
	}
	if m, ok := r.find("100"); !ok || m.Text != "100" {
		t.Fatalf("find: %v %+v", ok, m)
	}
	snap[0].Text = "changed"
	if r.buf[0].Text == "changed" {
		t.Fatal("the snapshot shares the ring's memory")
	}
}

func TestChatBucketBoundsOneConnectionsPosts(t *testing.T) {
	var b chatBucket
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	for i := range proto.ChatBurst {
		if !b.take(now) {
			t.Fatalf("post %d refused within the burst", i)
		}
	}
	if b.take(now) {
		t.Fatal("the burst was not the bound")
	}
	if b.take(now.Add(-time.Second)) {
		t.Fatal("a clock stepping back earned a post")
	}
	n := 0
	for b.take(now.Add(time.Second)) {
		n++
	}
	if n != proto.ChatRatePerSecond {
		t.Fatalf("a second earned %d posts, want %d", n, proto.ChatRatePerSecond)
	}
	if n = 0; true {
		for b.take(now.Add(time.Hour)) {
			n++
		}
	}
	if n != proto.ChatBurst {
		t.Fatalf("an hour earned %d posts, want the burst %d", n, proto.ChatBurst)
	}
}

func chatOf(kind string) func(m map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == proto.CtlChat && m["kind"] == kind }
}

func TestChatReachesEveryViewerWhateverTheirRole(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a, b := newChanSink(false), newChanSink(false)
	subA, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	if err != nil {
		t.Fatal(err)
	}
	subB, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, b)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Chat(subA, proto.ChatPost{T: proto.CtlChat, Text: "  hello <there>\r\n", Nonce: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "hello <there>" || m.Kind != proto.ChatKindMessage || m.Scope != proto.ChatScopeSession || m.By.Name != "Nate" || m.By.Role != RoleControl || m.By.ID != subA.ID || m.Nonce != "n1" || m.ID == "" || m.At.IsZero() {
		t.Fatalf("kept %+v", m)
	}
	got := waitControl(b, chatOf(proto.ChatKindMessage))
	if got == nil || got["text"] != "hello <there>" || got["id"] != m.ID || got["nonce"] != "n1" || got["by"].(map[string]any)["role"] != "control" || got["by"].(map[string]any)["name"] != "Nate" {
		t.Fatalf("the view-only viewer got %v", got)
	}
	if own := waitControl(a, chatOf(proto.ChatKindMessage)); own == nil || own["id"] != m.ID {
		t.Fatalf("the sender's own copy %v", own)
	}
	// A view link may talk; it may not reach the agent.
	if _, err := s.Chat(subB, proto.ChatPost{T: proto.CtlChat, Text: "hi back"}); err != nil {
		t.Fatalf("a view role's post: %v", err)
	}
	if got := waitControl(a, func(x map[string]any) bool { return chatOf(proto.ChatKindMessage)(x) && x["text"] == "hi back" }); got == nil || got["by"].(map[string]any)["role"] != "view" {
		t.Fatalf("the controller got %v", got)
	}
	if _, err := s.Chat(subB, proto.ChatPost{T: proto.CtlChat, Text: "x", To: proto.ChatToAgent}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a view role to the agent: %v", err)
	}
	for _, bad := range []struct {
		post proto.ChatPost
		want error
	}{
		{proto.ChatPost{Text: "  \n "}, ErrChatEmpty},
		{proto.ChatPost{Text: strings.Repeat("y", proto.MaxChatText+1)}, ErrChatTooLong},
		{proto.ChatPost{Text: "x", Scope: "run"}, ErrNoRunChat},
		{proto.ChatPost{Text: "x", To: "core"}, ErrChatBadScope},
	} {
		if _, err := s.Chat(subA, bad.post); !errors.Is(err, bad.want) {
			t.Fatalf("%+v: %v, want %v", bad.post, err, bad.want)
		}
	}
	if _, err := s.Chat(subA, proto.ChatPost{Text: strings.Repeat("z", proto.MaxChatText)}); err != nil {
		t.Fatalf("a message of exactly the bound: %v", err)
	}
}

func TestChatReplayComesAfterTheActivityReplayAndBeforeLiveMessages(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a := newChanSink(false)
	subA, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if _, err := s.Chat(subA, proto.ChatPost{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	c := newChanSink(false)
	subC, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "Jane", Cols: 80, Rows: 24}, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(subA, proto.ChatPost{Text: "live"}); err != nil {
		t.Fatal(err)
	}
	live := waitControl(c, func(m map[string]any) bool { return chatOf(proto.ChatKindMessage)(m) && m["text"] == "live" })
	if live == nil {
		t.Fatal("no live message")
	}
	// In the new viewer's frames: ready, then the history, then the live message; the history holds the three and Nate's join.
	order := map[string]int{}
	var history map[string]any
	for i := 0; i < c.count(); i++ {
		f, err := proto.Decode(c.frame(i))
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		m := decodeControl(t, c.frame(i))
		switch {
		case m["t"] == proto.CtlReady:
			order["ready"] = i
		case m["t"] == proto.CtlChatHistory:
			order["history"] = i
			history = m
		case chatOf(proto.ChatKindMessage)(m) && m["text"] == "live":
			order["live"] = i
		}
	}
	if history == nil || !(order["ready"] < order["history"] && order["history"] < order["live"]) {
		t.Fatalf("order %v", order)
	}
	var texts, joins []string
	for _, raw := range history["messages"].([]any) {
		m := raw.(map[string]any)
		if m["kind"] == proto.ChatKindMessage {
			texts = append(texts, m["text"].(string))
		}
		if m["kind"] == proto.ChatKindSystem {
			joins = append(joins, m["by"].(map[string]any)["name"].(string)+":"+m["event"].(string))
		}
	}
	if strings.Join(texts, ",") != "one,two,three" || strings.Join(joins, ",") != "Nate:join" || history["more"] != nil || history["scope"] != proto.ChatScopeSession {
		t.Fatalf("history %v", history)
	}
	// The new viewer's own join line reached the earlier viewer, not its own history.
	if got := waitControl(a, func(m map[string]any) bool {
		return chatOf(proto.ChatKindSystem)(m) && m["event"] == "join" && m["by"].(map[string]any)["name"] == "Jane"
	}); got == nil {
		t.Fatal("no join line for Jane")
	}
	_ = subC
}

func TestChatReplayFramesFitTheControlFrame(t *testing.T) {
	var msgs []ChatMessage
	for i := range MaxChat {
		msgs = append(msgs, ChatMessage{ID: fmt.Sprintf("%016d", i), At: time.Now(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindMessage, By: ChatBy{ID: "x", Name: strings.Repeat("N", 40), Role: RoleView}, Text: strings.Repeat(`"`, proto.MaxChatText)})
	}
	frames := chatReplayFrames(proto.ChatScopeSession, msgs)
	if len(frames) < 2 {
		t.Fatalf("%d frames for %d full messages", len(frames), MaxChat)
	}
	total, ids := 0, map[string]bool{}
	for i, f := range frames {
		if len(f) > proto.MaxControl {
			t.Fatalf("frame %d is %d bytes", i, len(f))
		}
		m := decodeControl(t, f)
		more := m["more"] == true
		if more != (i < len(frames)-1) {
			t.Fatalf("frame %d more=%v", i, more)
		}
		for _, raw := range m["messages"].([]any) {
			id := raw.(map[string]any)["id"].(string)
			if ids[id] {
				t.Fatalf("message %s twice", id)
			}
			ids[id] = true
			total++
		}
	}
	// The newest that fit the replay budget, the oldest first across the frames.
	if total == 0 || total > MaxChat || !ids[msgs[len(msgs)-1].ID] {
		t.Fatalf("%d messages replayed, newest included: %v", total, ids[msgs[len(msgs)-1].ID])
	}
	first := decodeControl(t, frames[0])["messages"].([]any)[0].(map[string]any)["id"].(string)
	if first != msgs[len(msgs)-total].ID {
		t.Fatalf("the replay starts at %s, want %s", first, msgs[len(msgs)-total].ID)
	}
	if chatReplayFrames(proto.ChatScopeSession, nil) != nil {
		t.Fatal("frames for no messages")
	}
}

func TestChatSendTypesTheMessageAndMarksIt(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	a, b := newChanSink(false), newChanSink(false)
	subA, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	subB, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, b)
	m, err := s.Chat(subA, proto.ChatPost{Text: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	typed := make(chan []byte, 1)
	go func() {
		var acc []byte
		deadline := time.After(3 * time.Second)
		for {
			select {
			case in := <-p.input:
				acc = append(acc, in...)
				if bytes.Contains(acc, []byte("ship it")) && bytes.HasSuffix(acc, []byte("\r")) {
					typed <- acc
					return
				}
			case <-deadline:
				typed <- acc
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.ChatSend(ctx, subA, proto.ChatSend{T: proto.CtlChatSend, Ref: m.ID}); err != nil {
		t.Fatal(err)
	}
	if acc := <-typed; !bytes.Contains(acc, []byte("ship it")) || !bytes.HasSuffix(acc, []byte("\r")) {
		t.Fatalf("typed %q", acc)
	}
	marker := waitControl(b, chatOf(proto.ChatKindSentToAgent))
	if marker == nil || marker["ref"] != m.ID || marker["by"].(map[string]any)["name"] != "Nate" || marker["text"] != nil {
		t.Fatalf("marker %v", marker)
	}
	// Only a controller; only a kept message; never a marker.
	if err := s.ChatSend(ctx, subB, proto.ChatSend{Ref: m.ID}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("view: %v", err)
	}
	if err := s.ChatSend(ctx, subA, proto.ChatSend{Ref: "nope"}); !errors.Is(err, ErrChatUnknownRef) {
		t.Fatalf("unknown: %v", err)
	}
	if err := s.ChatSend(ctx, subA, proto.ChatSend{Ref: marker["id"].(string)}); !errors.Is(err, ErrChatUnknownRef) {
		t.Fatalf("a marker: %v", err)
	}
	if err := s.ChatSend(ctx, subA, proto.ChatSend{Ref: m.ID, Scope: "run", To: "core"}); !errors.Is(err, ErrNoRunChat) {
		t.Fatalf("run scope: %v", err)
	}
	if history := s.ChatHistory(); len(history) < 4 || history[len(history)-1].Kind != proto.ChatKindSentToAgent {
		t.Fatalf("history %+v", history)
	}
}

func TestChatOnAnEndedSessionIsRefused(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a := newChanSink(false)
	subA, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(subA, proto.ChatPost{Text: "anyone?"}); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("ended: %v", err)
	}
}

func TestChatJoinAndLeaveLinesCoalesceByName(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a, a2, b := newChanSink(false), newChanSink(false), newChanSink(false)
	subA, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	subA2, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "Nate", Cols: 80, Rows: 24}, a2)
	subB, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, b)
	lines := func() []string {
		var out []string
		for _, m := range s.ChatHistory() {
			if m.Kind == proto.ChatKindSystem {
				out = append(out, m.By.Name+":"+m.Event)
			}
		}
		return out
	}
	if got := strings.Join(lines(), ","); got != "Nate:join,Priya:join" {
		t.Fatalf("after the joins: %s", got)
	}
	s.Detach(subA)
	if got := strings.Join(lines(), ","); got != "Nate:join,Priya:join" {
		t.Fatalf("after one of Nate's two left: %s", got)
	}
	s.Detach(subA2)
	s.Detach(subB)
	if got := strings.Join(lines(), ","); got != "Nate:join,Priya:join,Nate:leave,Priya:leave" {
		t.Fatalf("after everyone left: %s", got)
	}
	// Hooks heard each line once, with the session's id.
	var heard []string
	s2, _ := newLocalWith(t, Options{OnChat: func(id string, m ChatMessage) { heard = append(heard, m.Kind+":"+m.Event+":"+m.Text) }})
	subC, _ := s2.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	s2.Chat(subC, proto.ChatPost{Text: "hi"})
	s2.Detach(subC)
	if got := strings.Join(heard, ","); got != "system:join:,message::hi,system:leave:" {
		t.Fatalf("hook heard %s", got)
	}
}

// The chat frames carry the message untouched by HTML escaping, so a
// message of the bound fits the control frame.
func TestChatFrameOfTheBoundFits(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a := newChanSink(false)
	subA, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, a)
	if _, err := s.Chat(subA, proto.ChatPost{Text: strings.Repeat("<", proto.MaxChatText)}); err != nil {
		t.Fatal(err)
	}
	got := waitControl(a, chatOf(proto.ChatKindMessage))
	if got == nil || len(got["text"].(string)) != proto.MaxChatText {
		t.Fatalf("got %v", got)
	}
}

// A run's chat crosses its members: a post over one member's connection
// reaches every member's viewers, a late viewer gets the run's history after
// the session's, and the room keeps it for the record.
func TestRunChatReachesEveryMembersViewers(t *testing.T) {
	room := NewChatRoom(nil, nil)
	a, _ := newLocalWith(t, Options{RunChat: room})
	b, _ := newLocalWith(t, Options{RunChat: room})
	room.Join(a, "core")
	room.Join(b, "tests")
	sa, sb := newChanSink(false), newChanSink(false)
	subA, _ := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sa)
	subB, _ := b.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, sb)
	if w := waitControl(sa, func(m map[string]any) bool { return m["t"] == proto.CtlWelcome }); w == nil || w["runChat"] != true {
		t.Fatalf("welcome %v", w)
	}
	m, err := a.Chat(subA, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "core is green", On: "core", Nonce: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	for name, sink := range map[string]*chanSink{"core's viewer": sa, "tests' viewer": sb} {
		got := waitControl(sink, chatOf(proto.ChatKindMessage))
		if got == nil || got["id"] != m.ID || got["scope"] != "run" || got["on"] != "core" || got["text"] != "core is green" || got["nonce"] != "n1" {
			t.Fatalf("%s got %v", name, got)
		}
	}
	// A view link posts too; an `on` that names no member is dropped.
	m2, err := b.Chat(subB, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "seen", On: "nobody"})
	if err != nil || m2.On != "" || m2.By.Role != RoleView {
		t.Fatalf("priya's post %+v %v", m2, err)
	}
	// A late viewer of tests: the session's history, then the run's.
	late := newChanSink(false)
	b.AttachWith(AttachOptions{Role: RoleControl, Name: "Jane", Cols: 80, Rows: 24}, late)
	// Frames reach a sink on the subscription's own goroutine, in the order
	// they were queued: once the run's last history frame is in, the
	// session's is too.
	if waitControl(late, func(m map[string]any) bool {
		return m["t"] == proto.CtlChatHistory && m["scope"] == "run" && m["more"] != true
	}) == nil {
		t.Fatal("the late viewer got no run history")
	}
	var scopes []string
	for i := 0; i < late.count(); i++ {
		f, err := proto.Decode(late.frame(i))
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		var h map[string]any
		if json.Unmarshal(f.Payload, &h) == nil && h["t"] == proto.CtlChatHistory {
			scopes = append(scopes, h["scope"].(string))
			if h["scope"] == "run" {
				var texts []string
				for _, raw := range h["messages"].([]any) {
					if mm := raw.(map[string]any); mm["kind"] == proto.ChatKindMessage {
						texts = append(texts, mm["text"].(string))
					}
				}
				if strings.Join(texts, "|") != "core is green|seen" {
					t.Fatalf("the run's history %v", h)
				}
			}
		}
	}
	if strings.Join(scopes, ",") != "session,run" {
		t.Fatalf("history scopes %v", scopes)
	}
	// The room keeps it; the roster lists everyone on any member, with the member they look at.
	var kept []string
	for _, m := range room.History() {
		if m.Kind == proto.ChatKindMessage {
			kept = append(kept, m.Text)
		}
	}
	if strings.Join(kept, "|") != "core is green|seen" {
		t.Fatalf("room history %v", kept)
	}
	roster := room.Roster()
	var people []string
	for _, p := range roster.List {
		people = append(people, p.Name+"@"+p.On+"/"+p.Role)
	}
	slices.Sort(people)
	if roster.Count != 3 || strings.Join(people, ",") != "Jane@tests/control,Nate@core/control,Priya@tests/view" {
		t.Fatalf("roster %+v", roster)
	}
	// A session in no run has no run chat.
	c, _ := newLocal(t, t.TempDir())
	subC, _ := c.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	if _, err := c.Chat(subC, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "x"}); !errors.Is(err, ErrNoRunChat) {
		t.Fatalf("no run: %v", err)
	}
	if err := c.ChatSend(context.Background(), subC, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: "x", To: "core"}); !errors.Is(err, ErrNoRunChat) {
		t.Fatalf("no run send: %v", err)
	}
}

// A viewer sent ready is on the run's roster from then on, before its
// attach has put it on its session's hub: a roster read meanwhile (another
// member's arrival broadcasting it) names it, once, and so does every one
// after (#281).
func TestARunRosterNamesAViewerSentReady(t *testing.T) {
	room := NewChatRoom(nil, nil)
	a, _ := newLocalWith(t, Options{RunChat: room})
	b, _ := newLocalWith(t, Options{RunChat: room})
	room.Join(a, "core")
	room.Join(b, "tests")
	if _, err := b.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, newChanSink(false)); err != nil {
		t.Fatal(err)
	}
	people := func(r proto.ChatRoster) string {
		var out []string
		for _, p := range r.List {
			out = append(out, p.Name+"@"+p.On)
		}
		slices.Sort(out)
		return fmt.Sprintf("%d %s", r.Count, strings.Join(out, ","))
	}
	var held string
	readySent = func() {
		readySent = nil
		held = people(room.Roster())
	}
	t.Cleanup(func() { readySent = nil })
	if _, err := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false)); err != nil {
		t.Fatal(err)
	}
	if held != "2 Nate@core,Priya@tests" {
		t.Fatalf("the roster read after Nate's ready: %s", held)
	}
	if got := people(room.Roster()); got != "2 Nate@core,Priya@tests" {
		t.Fatalf("the roster once he is on the hub: %s", got)
	}
}

// A run message posted on one member while a viewer attaches to another,
// once the viewer has been sent the run's replay and before it is on its
// session's hub, still reaches the viewer: in the replay or live, never
// neither. The post is let wait up to 100 ms for the attach: a room that
// takes it meanwhile sends it where the viewer is not yet.
func TestARunMessagePostedAsAViewerAttachesReachesIt(t *testing.T) {
	room := NewChatRoom(nil, nil)
	a, _ := newLocalWith(t, Options{RunChat: room})
	b, _ := newLocalWith(t, Options{RunChat: room})
	room.Join(a, "core")
	room.Join(b, "tests")
	nate, err := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	posted := make(chan error, 1)
	roomReplayed = func() {
		roomReplayed = nil
		go func() {
			_, err := a.Chat(nate, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "meanwhile"})
			posted <- err
		}()
		select {
		case err := <-posted:
			posted <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Cleanup(func() { roomReplayed = nil })
	late := newChanSink(false)
	attached := make(chan error, 1)
	go func() {
		_, err := b.AttachWith(AttachOptions{Role: RoleControl, Name: "Jane", Cols: 80, Rows: 24}, late)
		attached <- err
	}()
	for what, ch := range map[string]chan error{"the attach": attached, "the post": posted} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never ended", what)
		}
	}
	if waitControl(late, func(m map[string]any) bool {
		if m["t"] == proto.CtlChat {
			return m["text"] == "meanwhile"
		}
		msgs, _ := m["messages"].([]any)
		return m["t"] == proto.CtlChatHistory && slices.ContainsFunc(msgs, func(v any) bool {
			mm, _ := v.(map[string]any)
			return mm["text"] == "meanwhile"
		})
	}) == nil {
		t.Fatalf("the viewer never got the message posted as it attached (%d frames)", late.count())
	}
}

// The run's join and leave lines count a person once across its members,
// and a quiet connection (a run page's own) joins the run's chat but is no
// viewer of the session: no scrollback or output, not counted, no line.
func TestRunChatJoinsCoalesceAcrossMembersAndQuietConnectionsStayOut(t *testing.T) {
	room := NewChatRoom(nil, nil)
	a, pa := newLocalWith(t, Options{RunChat: room})
	b, _ := newLocalWith(t, Options{RunChat: room})
	room.Join(a, "core")
	room.Join(b, "tests")
	pa.outW.Write([]byte("early\n"))
	time.Sleep(50 * time.Millisecond)
	subA, _ := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	subB, _ := b.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	subC, _ := b.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, newChanSink(false))
	lines := func() string {
		var out []string
		for _, m := range room.History() {
			if m.Kind == proto.ChatKindSystem {
				out = append(out, m.By.Name+":"+m.Event)
			}
		}
		return strings.Join(out, ",")
	}
	if got := lines(); got != "Nate:join,Priya:join" {
		t.Fatalf("after the joins: %s", got)
	}
	a.Detach(subA)
	if got := lines(); got != "Nate:join,Priya:join" {
		t.Fatalf("after Nate left core, still on tests: %s", got)
	}
	b.Detach(subB)
	b.Detach(subC)
	if got := lines(); got != "Nate:join,Priya:join,Nate:leave,Priya:leave" {
		t.Fatalf("after everyone left: %s", got)
	}
	quiet := newChanSink(false)
	q, err := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Quiet", Cols: 80, Rows: 24, ChatOnly: true}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pa.outW.Write([]byte("later\n"))
	time.Sleep(50 * time.Millisecond)
	if v := waitControl(quiet, func(m map[string]any) bool { return m["t"] == proto.CtlViewers }); v == nil || v["count"] != float64(0) {
		t.Fatalf("the quiet connection counted itself: %v", v)
	}
	for i := 0; i < quiet.count(); i++ {
		if f, err := proto.Decode(quiet.frame(i)); err == nil && (f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback) {
			t.Fatalf("the quiet connection got %v %q", f.Type, f.Payload)
		}
	}
	if a.Viewers() != 0 || a.Info().Viewers != 0 {
		t.Fatalf("a quiet connection is a viewer: %d", a.Viewers())
	}
	for _, m := range a.ChatHistory() {
		if m.By.Name == "Quiet" {
			t.Fatalf("the session's chat has a line about the quiet connection: %+v", m)
		}
	}
	if got := lines(); got != "Nate:join,Priya:join,Nate:leave,Priya:leave,Quiet:join" {
		t.Fatalf("the run's lines: %s", got)
	}
	if r := room.Roster(); r.Count != 1 || r.List[0].Name != "Quiet" || r.List[0].On != "" {
		t.Fatalf("roster %+v", r)
	}
	// It posts and reads the run's chat like anyone.
	if _, err := a.Chat(q, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "from the run page"}); err != nil {
		t.Fatal(err)
	}
	if got := waitControl(quiet, chatOf(proto.ChatKindMessage)); got == nil || got["text"] != "from the run page" {
		t.Fatalf("the quiet connection got %v", got)
	}
	a.Detach(q)
	if got := lines(); !strings.HasSuffix(got, "Quiet:leave") {
		t.Fatalf("after the quiet connection left: %s", got)
	}
}

// A controller's send in the run's chat types the message into the member
// it names and marks it with the member; a member waiting on a prompt, not
// running or unknown is not typed into, with the broadcast's reasons.
func TestRunChatSendTypesIntoAMemberOrSaysWhyNot(t *testing.T) {
	running := map[string]bool{"core": true, "tests": true}
	var mu sync.Mutex
	room := NewChatRoom(nil, func(n string) bool { mu.Lock(); defer mu.Unlock(); return running[n] })
	a, _ := newLocalWith(t, Options{RunChat: room})
	b, pb := newLocalWith(t, Options{RunChat: room})
	room.Join(a, "core")
	room.Join(b, "tests")
	sa := newChanSink(false)
	subA, _ := a.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sa)
	subV, _ := a.AttachWith(AttachOptions{Role: RoleView, Name: "Priya", Cols: 80, Rows: 24}, newChanSink(false))
	m, err := a.Chat(subA, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "cover the 404 path"})
	if err != nil {
		t.Fatal(err)
	}
	typed := make(chan []byte, 1)
	go func() {
		var acc []byte
		deadline := time.After(3 * time.Second)
		for {
			select {
			case in := <-pb.input:
				acc = append(acc, in...)
				if bytes.Contains(acc, []byte("cover the 404 path")) && bytes.HasSuffix(acc, []byte("\r")) {
					typed <- acc
					return
				}
			case <-deadline:
				typed <- acc
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID, To: "tests"}); err != nil {
		t.Fatal(err)
	}
	if acc := <-typed; !bytes.Contains(acc, []byte("cover the 404 path")) || !bytes.HasSuffix(acc, []byte("\r")) {
		t.Fatalf("typed %q", acc)
	}
	marker := waitControl(sa, chatOf(proto.ChatKindSentToAgent))
	if marker == nil || marker["ref"] != m.ID || marker["to"] != "tests" || marker["scope"] != "run" {
		t.Fatalf("marker %v", marker)
	}
	if h := room.History(); h[len(h)-1].Kind != proto.ChatKindSentToAgent || h[len(h)-1].To != "tests" {
		t.Fatalf("room history ends with %+v", h[len(h)-1])
	}
	want := func(what string, err error, reason string) {
		t.Helper()
		var ns ErrNotSent
		if !errors.As(err, &ns) || ns.Reason != reason {
			t.Fatalf("%s: %v, want not sent %s", what, err, reason)
		}
		f, _ := proto.Decode(ChatErrorFrame(err, m.ID))
		var frame map[string]any
		json.Unmarshal(f.Payload, &frame)
		if frame["code"] != proto.ErrCodeNotSent || frame["message"] != reason || frame["requestId"] != m.ID {
			t.Fatalf("%s: frame %v", what, frame)
		}
	}
	b.SetAttention(AttentionNeedsInput, "Trust this folder?", "trust")
	want("waiting", a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID, To: "tests"}), NotSentNeedsInput)
	mu.Lock()
	running["core"] = false
	mu.Unlock()
	want("not running", a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID, To: "core"}), NotSentNotRunning)
	want("unknown", a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID, To: "docs"}), NotSentUnknown)
	if err := a.ChatSend(ctx, subV, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID, To: "tests"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a view link: %v", err)
	}
	if err := a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: m.ID}); !errors.Is(err, ErrChatBadScope) {
		t.Fatalf("no member named: %v", err)
	}
	if err := a.ChatSend(ctx, subA, proto.ChatSend{Scope: proto.ChatScopeRun, Ref: "nope", To: "tests"}); !errors.Is(err, ErrChatUnknownRef) {
		t.Fatalf("unknown ref: %v", err)
	}
	// A post with `to` from a view link is refused before anything is kept.
	if _, err := a.Chat(subV, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "x", To: "tests"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a view link's post to a member: %v", err)
	}
	// Every message told the hook, outside any lock.
	var heard []string
	room2 := NewChatRoom(func(m ChatMessage) { heard = append(heard, m.Kind) }, nil)
	c, _ := newLocalWith(t, Options{RunChat: room2})
	room2.Join(c, "solo")
	subC, _ := c.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	c.Chat(subC, proto.ChatPost{Scope: proto.ChatScopeRun, Text: "hi"})
	c.Detach(subC)
	if got := strings.Join(heard, ","); got != "system,message,system" {
		t.Fatalf("the hook heard %s", got)
	}
}

func TestRunChatRingKeepsMaxRunChat(t *testing.T) {
	r := chatRing{limit: MaxRunChat}
	for i := range MaxRunChat + 9 {
		r.add(ChatMessage{ID: fmt.Sprint(i)})
	}
	if snap := r.snapshot(); len(snap) != MaxRunChat || snap[0].ID != "9" {
		t.Fatalf("ring kept %d from %s", len(snap), snap[0].ID)
	}
}

// The agent's question goes into the chat as the session goes needs_input,
// with its choices and from the agent; the input that answers it leaves a
// line saying who did; a run's member says the same in the run's chat, on
// the member.
func TestAQuestionGoesIntoTheChatAndItsAnswerFollows(t *testing.T) {
	room := NewChatRoom(nil, nil)
	s, p := newLocalWith(t, Options{RunChat: room})
	room.Join(s, "review")
	sink := newChanSink(false)
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sink)
	s.SetAttentionFull(AttentionNeedsInput, "Trust this folder?", "trust", KindPrompt, []Option{{Label: "Yes, trust", Input: "y\r"}, {Label: "No", Input: "n\r"}})
	q := waitControl(sink, chatOf(proto.ChatKindQuestion))
	if q == nil || q["text"] != "Trust this folder?" || q["scope"] != "session" {
		t.Fatalf("question %v", q)
	}
	by := q["by"].(map[string]any)
	if by["role"] != "agent" || by["name"] != s.Info().AgentID || by["id"] != "agent" {
		t.Fatalf("the question's by %v", by)
	}
	if opts := q["options"].([]any); len(opts) != 2 || opts[0].(map[string]any)["label"] != "Yes, trust" || opts[1].(map[string]any)["input"] != "n\r" {
		t.Fatalf("options %v", q["options"])
	}
	// The run's chat has it too, on the member.
	var runQ *ChatMessage
	for _, m := range room.History() {
		if m.Kind == proto.ChatKindQuestion {
			mm := m
			runQ = &mm
		}
	}
	if runQ == nil || runQ.On != "review" || runQ.Scope != "run" || len(runQ.Options) != 2 {
		t.Fatalf("the run's copy %+v", runQ)
	}
	// Answered by a person's reply naming it: the line says who, and names the question.
	go func() {
		for range p.input {
		}
	}()
	if err := s.AnswerInput(t.Context(), sub, q["id"].(string), []byte("y\r")); err != nil {
		t.Fatal(err)
	}
	a := waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlChat && m["event"] == "answered" })
	if a == nil || a["ref"] != q["id"] || a["by"].(map[string]any)["name"] != "Nate" || a["kind"] != proto.ChatKindSystem {
		t.Fatalf("answered %v", a)
	}
	// The line says what was answered with: the choice whose keys the reply typed (#364).
	if a["text"] != "Yes, trust" {
		t.Fatalf("the answered line's words %v", a["text"])
	}
	last := room.History()[len(room.History())-1]
	if last.Event != "answered" || last.On != "review" || last.Ref != runQ.ID || last.Text != "Yes, trust" {
		t.Fatalf("the run's answered line %+v", last)
	}
	// A question without a message says the session waits; the same question twice is one line.
	s.SetAttentionFull(AttentionNeedsInput, "", "bell", "", nil)
	s.SetAttentionFull(AttentionNeedsInput, "", "bell", "", nil)
	n := 0
	for _, m := range s.ChatHistory() {
		if m.Kind == proto.ChatKindQuestion && m.Text == "Waiting for input" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d questions for one prompt", n)
	}
}

// A comment on lines of a file (design round 12, F7): the quote is kept
// cleaned and bounded (twelve lines of 200 bytes, control characters out,
// a path and a range or nothing), a quote alone is a message, a full text
// of quotes beside a full quote still fits one frame (its last lines left
// out, Cut said), and the agent is given the location, the lines and the
// words.
func TestChatQuoteIsBoundedAndTypedWithItsLocation(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, newChanSink(false))
	var lines []string
	for i := 0; i < 14; i++ {
		lines = append(lines, fmt.Sprintf("line %d\x07 %s", i, strings.Repeat("x", 250)))
	}
	m, err := s.Chat(sub, proto.ChatPost{Text: "why this?", Quote: &proto.ChatQuote{Path: "internal/api/users.go", From: 14, To: 27, Lines: lines}})
	if err != nil {
		t.Fatal(err)
	}
	q := m.Quote
	if q == nil || q.Path != "internal/api/users.go" || q.From != 14 || q.To != 27 || len(q.Lines) != proto.MaxQuoteLines || !q.Cut {
		t.Fatalf("quote %+v", q)
	}
	for _, l := range q.Lines {
		if len(l) > proto.MaxQuoteLine || strings.ContainsRune(l, '\x07') {
			t.Fatalf("a line %q", l)
		}
	}
	if pm := ChatToProto(m); pm.Quote == nil || len(pm.Quote.Lines) != len(q.Lines) {
		t.Fatalf("on the wire %+v", pm.Quote)
	}
	// A quote alone is a message; no text and no quote is not; a quote without a path or range is dropped.
	if m, err := s.Chat(sub, proto.ChatPost{Quote: &proto.ChatQuote{Path: "a.go", From: 3, To: 3, Lines: []string{"x := 1"}}}); err != nil || m.Quote == nil || m.Text != "" {
		t.Fatalf("a quote alone: %+v %v", m, err)
	}
	if _, err := s.Chat(sub, proto.ChatPost{Quote: &proto.ChatQuote{Path: "", From: 3, To: 3}}); !errors.Is(err, ErrChatEmpty) {
		t.Fatalf("a quote without a path and no text: %v", err)
	}
	if m, _ := s.Chat(sub, proto.ChatPost{Text: "hi", Quote: &proto.ChatQuote{Path: "a.go", From: 5, To: 2}}); m.Quote != nil {
		t.Fatal("a backwards range kept")
	}
	// The worst case for the frame: a full text of quotes and a full quote of backslashes.
	var heavy []string
	for i := 0; i < proto.MaxQuoteLines; i++ {
		heavy = append(heavy, strings.Repeat("\\", proto.MaxQuoteLine))
	}
	big, err := s.Chat(sub, proto.ChatPost{Text: strings.Repeat("\"", proto.MaxChatText), Quote: &proto.ChatQuote{Path: strings.Repeat("\"", proto.MaxQuotePath), From: 1, To: 12, Lines: heavy}})
	if err != nil {
		t.Fatal(err)
	}
	if raw := proto.MustControlRaw(ChatToProto(big)); len(raw) > proto.MaxControl || !big.Quote.Cut || len(big.Quote.Lines) == proto.MaxQuoteLines {
		t.Fatalf("frame %d bytes, quote %d lines, cut %v", len(raw), len(big.Quote.Lines), big.Quote.Cut)
	}
	// What the agent is given.
	one := ChatMessage{Text: "rename it", Quote: &proto.ChatQuote{Path: "a.go", From: 3, To: 3, Lines: []string{"x := 1"}}}
	if got := AgentText(one); got != "a.go:3\n> x := 1\nrename it" {
		t.Fatalf("one line: %q", got)
	}
	two := ChatMessage{Quote: &proto.ChatQuote{Path: "a.go", From: 3, To: 4, Lines: []string{"a", "b"}}}
	if got := AgentText(two); got != "a.go:3-4\n> a\n> b" {
		t.Fatalf("two lines, no words: %q", got)
	}
	if got := AgentText(ChatMessage{Text: "plain"}); got != "plain" {
		t.Fatalf("no quote: %q", got)
	}
	if got := AgentText(big); len(got) > proto.MaxSubmit {
		t.Fatalf("the agent's text is %d bytes", len(got))
	}
	// Sent to the agent, the location and the lines are typed before the words.
	typed := make(chan []byte, 1)
	go func() {
		var acc []byte
		deadline := time.After(3 * time.Second)
		for {
			select {
			case in := <-p.input:
				acc = append(acc, in...)
				if bytes.Contains(acc, []byte("why this?")) && bytes.HasSuffix(acc, []byte("\r")) {
					typed <- acc
					return
				}
			case <-deadline:
				typed <- acc
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.ChatSend(ctx, sub, proto.ChatSend{T: proto.CtlChatSend, Ref: m.ID}); err != nil {
		t.Fatal(err)
	}
	if acc := <-typed; !bytes.Contains(acc, []byte("internal/api/users.go:14-27")) || !bytes.Contains(acc, []byte("> line 0")) || !bytes.Contains(acc, []byte("why this?")) {
		t.Fatalf("typed %q", acc)
	}
}

// The chat's answered line says what the reply answered with (#364): the
// label of the choice whose keys it typed, for any question; never a line
// typed (the agent may ask for a credential, the terminal may not echo it,
// and the chat goes to every viewer), nor a digit that is no choice's keys
// (the choices shown need not be the screen's numbering).
func TestAnswerWordsNameTheChoiceOnly(t *testing.T) {
	choices := []Option{{Label: "Yes, trust", Input: "y\r"}, {Label: "No", Input: "n\r"}}
	numbered := []Option{{Label: "Yes", Input: "1"}, {Label: "Yes, always", Input: "2"}, {Label: "No", Input: "3"}}
	sparse := []Option{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}}
	for _, c := range []struct {
		name    string
		options []Option
		keys    []byte
		want    string
	}{
		{"a choice's keys", choices, []byte("n\r"), "No"},
		{"a digit that is a choice's keys", numbered, []byte("2"), "Yes, always"},
		{"a digit that is no choice's keys", sparse, []byte("2"), ""},
		{"a digit past the list", numbered, []byte("7"), ""},
		{"a line typed into the terminal", choices, []byte("hunter2\r"), ""},
		{"a line typed at a question without choices", nil, []byte("the blue one\r"), ""},
		{"a reply box's line", nil, nil, ""},
		{"an Enter alone", choices, []byte("\r"), ""},
		{"an arrow", choices, []byte("\x1b[A"), ""},
	} {
		if got := answerWords(c.options, c.keys); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A line typed into a reply box answers the agent's own question; the
// chat's answered line says who answered and never the line (#364: it
// may be a credential the agent asked for).
func TestATypedAnswerIsNotInTheChatsLine(t *testing.T) {
	s, p := newLocalWith(t, Options{})
	go func() {
		for range p.input {
		}
	}()
	sink := newChanSink(false)
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Nate", Cols: 80, Rows: 24}, sink)
	// The agent's own question (the ask tool: Ask), which a typed line answers; a dialog on the screen takes its choices only.
	s.Ask("Which database?", SourceAPI, KindPrompt, nil)
	q := waitControl(sink, chatOf(proto.ChatKindQuestion))
	if q == nil {
		t.Fatal("no question in the chat")
	}
	res, err := s.Submit(context.Background(), Submission{Text: "Postgres, the managed one", By: sub, Question: q["id"].(string)})
	if err != nil || !res.Entered {
		t.Fatalf("the reply: %+v %v", res, err)
	}
	a := waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlChat && m["event"] == "answered" })
	if a == nil || a["ref"] != q["id"] || a["by"].(map[string]any)["name"] != "Nate" {
		t.Fatalf("answered %v", a)
	}
	if _, has := a["text"]; has {
		t.Fatalf("the answered line repeats the line typed: %v", a["text"])
	}
}
