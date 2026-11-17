package hostagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// lastEntry is the session's last entry of type typ.
func lastEntry(local *session.Local, typ string) (session.ActivityEntry, bool) {
	var out session.ActivityEntry
	found := false
	for _, e := range local.Activity() {
		if e.Type == typ {
			out, found = e, true
		}
	}
	return out, found
}

// The socket takes the events route's request as the route does: an event
// recorded in the host's session by the agent, cleaned; an attention word
// applied as the attention route applies it; 202 {accepted: true}.
func TestTheSocketTakesAnEventAsTheRouteDoes(t *testing.T) {
	h := newReportingHost(t)
	tok := h.target.Token
	code, reply := postSocket(t, h.l.path, notify.HostEventsPath, tok, `{"type":"tool_use","tool":"Bash"}`)
	if code != http.StatusAccepted || !strings.Contains(reply, `"accepted":true`) {
		t.Fatalf("an event: %d %s", code, reply)
	}
	if e, ok := lastEntry(h.local, session.ActivityToolUse); !ok || e.Tool != "Bash" || e.ByName != eventActor {
		t.Fatalf("recorded %+v", e)
	}
	if code, reply := postSocket(t, h.l.path, notify.HostEventsPath, tok, `{"type":"file","op":"edit","path":"main.go","tool":"Edit"}`); code != http.StatusAccepted {
		t.Fatalf("a file event: %d %s", code, reply)
	}
	if e, ok := lastEntry(h.local, session.ActivityFile); !ok || e.Path != "main.go" || e.Op != "edit" {
		t.Fatalf("recorded %+v", e)
	}
	if code, reply := postSocket(t, h.l.path, notify.HostEventsPath, tok, `{"type":"needs_input","message":"Ship it?","ask":true}`); code != http.StatusAccepted {
		t.Fatalf("an attention word: %d %s", code, reply)
	}
	if a := h.local.Info().Attention; a.State != session.AttentionNeedsInput || !a.Asked() || a.Message != "Ship it?" {
		t.Fatalf("the session shows %+v", a)
	}
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's routes were asked %d times", n)
	}
}

// Only the session's own token, a body of the route's fields alone within
// its bounds, a type the route takes and a file event with its op and its
// path are taken: anything else is refused, nothing recorded.
func TestTheSocketRefusesAnEventAsTheRouteDoes(t *testing.T) {
	h := newReportingHost(t)
	other := newReportingHost(t)
	before := len(h.local.Activity())
	for name, tok := range map[string]string{"another session's": other.target.Token, "of no session": "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9v", "none": ""} {
		if code, _ := postSocket(t, h.l.path, notify.HostEventsPath, tok, `{"type":"progress","message":"x"}`); code != http.StatusUnauthorized {
			t.Errorf("%s token: %d", name, code)
		}
	}
	for name, body := range map[string]string{
		"past 64 KiB":                 `{"type":"progress"` + strings.Repeat(" ", 64<<10) + `}`,
		"an unknown field":            `{"type":"progress","extra":1}`,
		"an attention route's field":  `{"type":"progress","agentSession":"a"}`,
		"trailing data":               `{"type":"progress"}{}`,
		"no type":                     `{"message":"m"}`,
		"a type the session records":  `{"type":"join"}`,
		"another session-only type":   `{"type":"status","message":"exited"}`,
		"an unknown type":             `{"type":"sleeping"}`,
		"a file event without a path": `{"type":"file","op":"edit"}`,
		"a file event without an op":  `{"type":"file","path":"a.go"}`,
		"a word's message past 4096":  `{"type":"done","message":"` + strings.Repeat("a", 4097) + `"}`,
		"a word's unknown kind":       `{"type":"done","kind":"other"}`,
	} {
		if code, reply := postSocket(t, h.l.path, notify.HostEventsPath, h.target.Token, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, reply)
		}
	}
	if got := len(h.local.Activity()); got != before {
		t.Fatalf("a refused event was recorded: %d entries, %d before", got, before)
	}
	if h.local.Info().Attention.State != session.AttentionNone {
		t.Fatalf("a refused word was applied: %+v", h.local.Info().Attention)
	}
}

// A flood of events spends the session's bucket: past it each is refused
// 429 and nothing recorded.
func TestAFloodOfEventsSpendsTheSessionsBucket(t *testing.T) {
	h := newReportingHost(t)
	start := time.Now()
	taken, refused := 0, 0
	for i := range 200 {
		switch code, reply := postSocket(t, h.l.path, notify.HostEventsPath, h.target.Token, `{"type":"progress","message":"step"}`); code {
		case http.StatusAccepted:
			taken++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("event %d: %d %s", i, code, reply)
		}
	}
	earned := int(time.Since(start).Seconds()*session.EventRatePerSecond) + 1
	if refused == 0 || taken > session.EventBurst+earned {
		t.Fatalf("taken %d, refused %d, the bucket holds %d and earned %d", taken, refused, session.EventBurst, earned)
	}
}

// An event before the host's session is made waits for it; one for an
// ended session is refused 409.
func TestAnEventWaitsForTheSessionAndAnEndedOneIsRefused(t *testing.T) {
	tok, _ := share.NewToken()
	l, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	waiting := make(chan struct{}, 1)
	l.waiting = func() { waiting <- struct{}{} }
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	target := notify.Target{URL: srv.URL + "/api/sessions/s/attention", Token: tok, Socket: l.path, Events: true}
	sent := make(chan error, 1)
	go func() {
		sent <- target.Send(context.Background(), notify.Request{Event: session.ActivityProgress, Message: "early"})
	}()
	<-waiting
	proc := sessiontest.NewFakeProc()
	local := session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog, FileEdit: "off"})
	l.setSession(local)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if e, ok := lastEntry(local, session.ActivityProgress); !ok || e.Message != "early" {
		t.Fatalf("recorded %+v", e)
	}
	proc.End(0)
	for deadline := time.Now().Add(10 * time.Second); !local.Info().Status.Ended(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the session never ended")
		}
	}
	code, reply := postSocket(t, l.path, notify.HostEventsPath, tok, `{"type":"progress","message":"late"}`)
	var m map[string]map[string]string
	_ = json.Unmarshal([]byte(reply), &m)
	if code != http.StatusConflict || m["error"]["code"] != "session_ended" {
		t.Fatalf("an event for an ended session: %d %s", code, reply)
	}
}
