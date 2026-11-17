package cli

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/notify"
)

// hostSocket listens as a session's host does, on a socket in a folder of
// mode 0700, handing each body it reads (with its path and authorization)
// to the channel it returns and answering 200, and points the session's
// environment at it.
func hostSocket(t *testing.T) <-chan map[string]any {
	t.Helper()
	parent := os.TempDir()
	if len(parent) > 40 {
		parent = "/tmp"
	}
	dir, err := os.MkdirTemp(parent, "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "report.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan map[string]any, 16)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		m["auth"], m["path"] = r.Header.Get("Authorization"), r.URL.Path
		got <- m
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv(notify.EnvHostSocket, path)
	return got
}

// With the host's socket named, conductor notify reports a state, the
// agent's own question and a hook's mapped state to the host, never to the
// server; an event still goes to the server's events route.
func TestNotifyReportsToTheHostsSocket(t *testing.T) {
	ns := startNotifyServer(t)
	host := hostSocket(t)
	next := func(what string) map[string]any {
		t.Helper()
		select {
		case m := <-host:
			if m["auth"] != "Bearer agent-token" || m["path"] != notify.HostReportPath {
				t.Fatalf("%s: the host read %v", what, m)
			}
			return m
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the host read nothing", what)
			return nil
		}
	}
	if code, _, err := runNotifyWith(t, "", "--quiet=false", "--state", "done", "--message", "finished"); code != 0 || err != nil {
		t.Fatalf("state: %d %v", code, err)
	}
	if m := next("a state"); m["state"] != "done" || m["message"] != "finished" {
		t.Fatalf("a state: %v", m)
	}
	if code, _, err := runNotifyWith(t, "", "--quiet=false", "--ask", "--message", "Ship it?", "--choices", "yes|no"); code != 0 || err != nil {
		t.Fatalf("ask: %d %v", code, err)
	}
	if m := next("a question"); m["state"] != "needs_input" || m["ask"] != true || len(m["options"].([]any)) != 2 {
		t.Fatalf("a question: %v", m)
	}
	if code, _, err := runNotifyWith(t, `{"hook_event_name":"Stop","last_assistant_message":"All done.","session_id":"abc"}`, "--quiet=false", "--claude-hook"); code != 0 || err != nil {
		t.Fatalf("hook: %d %v", code, err)
	}
	if m := next("a hook"); m["state"] != "done" || m["message"] != "All done." || m["agentSession"] != "abc" {
		t.Fatalf("a hook: %v", m)
	}
	if ns.calls != 0 {
		t.Fatalf("the server was sent %d reports", ns.calls)
	}
	if code, _, err := runNotifyWith(t, "", "--quiet=false", "--event", "progress", "--message", "step"); code != 0 || err != nil {
		t.Fatalf("event: %d %v", code, err)
	}
	if ns.calls != 1 || ns.path != "/api/sessions/s1/events" {
		t.Fatalf("the server was sent %d, last %s", ns.calls, ns.path)
	}
	select {
	case m := <-host:
		t.Fatalf("the host read an event: %v", m)
	default:
	}
}

// The MCP tools that report attention (set_state, ask) report to the
// host's socket when the session's environment names one.
func TestMcpAttentionToolsReportToTheHostsSocket(t *testing.T) {
	ns := startNotifyServer(t)
	host := hostSocket(t)
	env := map[string]string{
		"CONDUCTOR_NOTIFY_URL":   os.Getenv("CONDUCTOR_NOTIFY_URL"),
		"CONDUCTOR_NOTIFY_TOKEN": "agent-token",
		notify.EnvHostSocket:     os.Getenv(notify.EnvHostSocket),
	}
	s := &mcpServer{out: io.Discard, getenv: func(k string) string { return env[k] }}
	for name, raw := range map[string]string{"set_state": `{"state":"done","message":"through the tool"}`, "ask": `{"message":"Which one?","choices":["a","b"]}`} {
		if text, err := s.call(t.Context(), name, json.RawMessage(raw)); err != nil || text != "reported" {
			t.Fatalf("%s: %q %v", name, text, err)
		}
		select {
		case m := <-host:
			if m["auth"] != "Bearer agent-token" {
				t.Fatalf("%s: %v", name, m)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the host read nothing", name)
		}
	}
	if ns.calls != 0 {
		t.Fatalf("the server was sent %d reports", ns.calls)
	}
}

// With the host's socket taking events (CONDUCTOR_HOST_EVENTS=1),
// conductor notify --event reports to it, not to the server; without it
// (a host of an earlier release), to the server's events route.
func TestNotifyEventsGoToTheHostWhenItTakesThem(t *testing.T) {
	ns := startNotifyServer(t)
	host := hostSocket(t)
	t.Setenv(notify.EnvHostEvents, "1")
	if code, _, err := runNotifyWith(t, "", "--quiet=false", "--event", "progress", "--message", "step"); code != 0 || err != nil {
		t.Fatalf("event: %d %v", code, err)
	}
	select {
	case m := <-host:
		if m["path"] != notify.HostEventsPath || m["type"] != "progress" || m["message"] != "step" {
			t.Fatalf("the host read %v", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the host read nothing")
	}
	if ns.calls != 0 {
		t.Fatalf("the server was sent %d", ns.calls)
	}
	t.Setenv(notify.EnvHostEvents, "")
	if code, _, err := runNotifyWith(t, "", "--quiet=false", "--event", "progress", "--message", "step"); code != 0 || err != nil {
		t.Fatalf("event: %d %v", code, err)
	}
	if ns.calls != 1 || ns.path != "/api/sessions/s1/events" {
		t.Fatalf("the server was sent %d, last %s", ns.calls, ns.path)
	}
}
