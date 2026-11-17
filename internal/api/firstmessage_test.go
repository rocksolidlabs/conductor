package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch's first message (P19): POST /api/sessions takes message, at
// most proto.MaxSubmit bytes, names a nameless session by its first
// words, and has it typed once the session is ready (the server's
// firstWait, a gate here), on a goroutine the server waits for.

// promptedAgent draws a prompt the server knows (its prompt shape: a
// row "> ", the line typed after it), then echoes what it is given: an
// agent that takes a first message.
var promptedAgent = catalog.Agent{ID: "prompted", Name: "prompted", Command: []string{"/bin/sh", "-c", "printf '> '; exec cat"}, PromptShape: `(?m)^>( .*)?$`}

// promptedEnv is a test server whose catalog has promptedAgent beside cat,
// which has no prompt shape.
func promptedEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, nil, nil, []catalog.Agent{promptedAgent})
}

// gateFirst makes e's first messages wait for the gate it returns to
// close, or for the server to stop.
func gateFirst(e *testEnv) chan struct{} {
	gate := make(chan struct{})
	e.srv.firstWait = func(ctx context.Context, _ *session.Local, _ func(string)) error {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return gate
}

// A message of 4096 bytes is taken; one byte more, or 2049 two-byte
// characters, is refused before any process starts.
func TestAFirstMessagePastItsBoundIsRefused(t *testing.T) {
	e := promptedEnv(t)
	gateFirst(e)
	before := len(e.srv.registry.List())
	for _, msg := range []string{strings.Repeat("a", proto.MaxSubmit+1), strings.Repeat("é", 2049)} {
		resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": msg})
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" || errorMessage(out) != "message: at most 4096 bytes" {
			t.Fatalf("a message of %d bytes: %d %v", len(msg), resp.StatusCode, out)
		}
	}
	if n := len(e.srv.registry.List()); n != before {
		t.Fatalf("%d sessions after the refusals, %d before", n, before)
	}
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": strings.Repeat("a", proto.MaxSubmit)})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a message at its bound: %d %v", resp.StatusCode, out)
	}
	stopCreated(e, out)
}

// stopCreated stops the session a 201 reply describes.
func stopCreated(e *testEnv, out map[string]any) {
	e.t.Helper()
	id, _ := out["id"].(string)
	e.t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(e.t.Context())
		}
	})
}

// A nameless launch with a message is named by its first words, cut at a
// word with an ellipsis; a name given wins; a message of spaces is none,
// and the session is named as any launch is.
func TestASessionIsNamedByItsFirstMessage(t *testing.T) {
	e := promptedEnv(t)
	gateFirst(e)
	for _, tc := range []struct {
		body map[string]any
		want string
		wait bool
	}{
		{map[string]any{"agentId": "prompted", "message": "fix the flaky vim spec on the mac runner please"}, "fix the flaky vim spec on the mac…", true},
		{map[string]any{"agentId": "prompted", "name": "parser", "message": "fix the parser"}, "parser", true},
		{map[string]any{"agentId": "prompted", "message": " \n\t "}, "prompted #", false},
	} {
		resp, out := e.do("POST", "/api/sessions", adminToken, tc.body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%v: %d %v", tc.body, resp.StatusCode, out)
		}
		stopCreated(e, out)
		name, _ := out["name"].(string)
		if !strings.HasPrefix(name, tc.want) || tc.want != "prompted #" && name != tc.want {
			t.Errorf("%v: named %q, want %q", tc.body, name, tc.want)
		}
		if waiting := out["firstMessage"] == "waiting"; waiting != tc.wait {
			t.Errorf("%v: firstMessage %v", tc.body, out["firstMessage"])
		}
	}
}

// The reply and the session's read say a first message waits; once the
// session is ready (the gate opened) it is typed, once, as Conductor's own
// line by launch, and the session says no more that it waits. Its text is
// in the session's Activity alone: never in its launch.
func TestAFirstMessageIsTypedOnceItsSessionIsReady(t *testing.T) {
	e := promptedEnv(t)
	gate := gateFirst(e)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": "fix the parser"})
	if resp.StatusCode != http.StatusCreated || out["firstMessage"] != "waiting" {
		t.Fatalf("the launch: %d %v", resp.StatusCode, out)
	}
	stopCreated(e, out)
	id, _ := out["id"].(string)
	read := func() map[string]any {
		t.Helper()
		resp, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the read: %d %v", resp.StatusCode, got)
		}
		return got
	}
	if s, _ := read()["session"].(map[string]any); s["firstMessage"] != "waiting" {
		t.Fatalf("the read before the gate: %v", s)
	}
	close(gate)
	// The submission's own waits (its pause, its Enter) bound this.
	until(t, "the first message typed", func() bool {
		s, _ := read()["session"].(map[string]any)
		return s["firstMessage"] == nil
	})
	var typed []session.ActivityEntry
	for _, en := range e.local(id).Activity() {
		if en.Type == session.ActivityInput && en.Message == "fix the parser" {
			typed = append(typed, en)
		}
	}
	if len(typed) != 1 || typed[0].Op != session.OpTyped || typed[0].ByName != "launch" {
		t.Fatalf("the message's entries: %+v", typed)
	}
	if launch, _ := read()["launch"].(map[string]any); strings.Contains(stringify(launch), "fix the parser") {
		t.Fatalf("the launch holds the message: %v", launch)
	}
}

// stringify is v's words, for a search through it.
func stringify(v map[string]any) string {
	var b strings.Builder
	for k, x := range v {
		b.WriteString(k)
		b.WriteString("=")
		if s, ok := x.(string); ok {
			b.WriteString(s)
		}
		if l, ok := x.([]any); ok {
			for _, a := range l {
				if s, ok := a.(string); ok {
					b.WriteString(s + " ")
				}
			}
		}
		b.WriteString(";")
	}
	return b.String()
}

// A first message still waiting when the server's background is waited
// for ends with it: WaitBackground returns within its context, the
// message untyped and the session no longer saying it waits.
func TestAFirstMessageEndsWithTheServer(t *testing.T) {
	e := promptedEnv(t)
	gateFirst(e)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": "fix the parser"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the launch: %d %v", resp.StatusCode, out)
	}
	stopCreated(e, out)
	id, _ := out["id"].(string)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e.srv.WaitBackground(ctx)
	if ctx.Err() != nil {
		t.Fatal("WaitBackground waited out its context")
	}
	l := e.local(id)
	if fm := l.Info().FirstMessage; fm != "" {
		t.Fatalf("the session says its first message is %q", fm)
	}
	for _, en := range l.Activity() {
		if en.Type == session.ActivityInput {
			t.Fatalf("typed %+v", en)
		}
	}
}

// The server's stop ends a first message still waiting: Shutdown returns
// within its context, the message untyped, also once the readiness it
// waited for comes after the stop. A launch's first message given after
// the stop, its session ready at once, is typed by no one either: its
// typer returns, the session no longer saying it waits, nothing typed.
func TestTheServersStopEndsItsFirstMessages(t *testing.T) {
	e := promptedEnv(t)
	gate := gateFirst(e)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": "fix the parser"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the launch: %d %v", resp.StatusCode, out)
	}
	stopCreated(e, out)
	before, _ := out["id"].(string)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	if ctx.Err() != nil {
		t.Fatal("Shutdown waited out its context")
	}
	close(gate)
	untyped := func(l *session.Local, which string) {
		t.Helper()
		until(t, "the typer of "+which+" returned", func() bool { return l.Info().FirstMessage == "" })
		for _, en := range l.Activity() {
			if en.Type == session.ActivityInput {
				t.Fatalf("%s typed %+v", which, en)
			}
		}
	}
	untyped(e.local(before), "the launch before the stop")
	resp, out = e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "prompted", "message": "after the stop"})
	if resp.StatusCode != http.StatusCreated {
		return
	}
	stopCreated(e, out)
	after, _ := out["id"].(string)
	untyped(e.local(after), "the launch after the stop")
}

// The clone route keeps its own body: a message there is an unknown field.
func TestTheCloneRouteTakesNoMessage(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	resp, out := e.do("POST", "/api/sessions/"+id+"/clone", adminToken, map[string]any{"name": "copy", "message": "fix the parser"})
	if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("a clone with a message: %d %v", resp.StatusCode, out)
	}
}

// An agent whose prompt the server does not know takes no first message:
// a launch of it with one is refused before any process starts, and the
// same launch with no message starts it as ever.
func TestAFirstMessageForAnAgentWithoutAKnownPromptIsRefused(t *testing.T) {
	e := promptedEnv(t)
	gateFirst(e)
	before := len(e.srv.registry.List())
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "message": "fix the parser"})
	if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "first_message_unsupported" {
		t.Fatalf("a message for cat: %d %v", resp.StatusCode, out)
	}
	if n := len(e.srv.registry.List()); n != before {
		t.Fatalf("%d sessions after the refusal, %d before", n, before)
	}
	resp, out = e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "message": "  "})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cat with spaces for a message: %d %v", resp.StatusCode, out)
	}
	stopCreated(e, out)
}

// The catalog's listing says which agents take a first message: one with
// a prompt shape does, one without does not (the key left out, as an
// older server leaves it out for every agent).
func TestTheCatalogSaysWhichAgentsTakeAFirstMessage(t *testing.T) {
	e := promptedEnv(t)
	resp, out := e.do("GET", "/api/catalog?wait=0", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the catalog: %d %v", resp.StatusCode, out)
	}
	agents, _ := out["agents"].([]any)
	seen := map[string]any{}
	for _, a := range agents {
		m, _ := a.(map[string]any)
		id, _ := m["id"].(string)
		v, has := m["firstMessage"]
		if !has {
			v = "absent"
		}
		seen[id] = v
	}
	if seen["prompted"] != true || seen["cat"] != "absent" {
		t.Fatalf("firstMessage by agent: %v", seen)
	}
	// An agent read there is sent back as it is: the key is taken and
	// ignored, as the listing's others are.
	for _, a := range agents {
		if m, _ := a.(map[string]any); m["id"] == "prompted" {
			m["id"], m["name"] = "prompted-copy", "prompted copy"
			if resp, out := e.do("POST", "/api/catalog", adminToken, m); resp.StatusCode != http.StatusOK {
				t.Fatalf("the agent sent back: %d %v", resp.StatusCode, out)
			}
		}
	}
}
