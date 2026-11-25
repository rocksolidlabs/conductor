package cli

import (
	"context"
	"strings"
	"testing"
)

// In a chat's child (CONDUCTOR_PROTOCOL set) the hooks the person's
// settings run report nothing: the child's own stream says what they would
// (the permission request, the turn's end, the files a tool touched). Each
// payload is read whole, so the hook runner never writes into a closed
// pipe, and the hook exits 0. The agent's own report (--ask) is still
// sent, and outside a chat the same payloads are sent as ever.
func TestProtocolSessionIgnoresHookReports(t *testing.T) {
	ns := startNotifyServer(t)
	cases := []struct {
		stdin string
		args  []string
	}{
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`, []string{"--claude-hook"}},
		{`{"hook_event_name":"Stop","session_id":"11111111-1111-4111-8111-111111111111"}`, []string{"--claude-hook"}},
		{`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/w/a.go"}}`, []string{"--claude-hook", "--files"}},
		{`{"hook_event_name":"PermissionRequest","tool_name":"shell"}`, []string{"--codex-hook"}},
		{`{"event":"PostToolUse","tool_name":"shell"}`, []string{"--goose-hook"}},
		{``, []string{"--codex", `{"type":"agent-turn-complete"}`}},
	}
	send := func(c struct {
		stdin string
		args  []string
	}) *countingReader {
		in := &countingReader{r: strings.NewReader(c.stdin)}
		code, err := runNotify(context.Background(), c.args, in, &strings.Builder{}, &strings.Builder{})
		if code != 0 || err != nil {
			t.Fatalf("%v: exit %d %v", c.args, code, err)
		}
		return in
	}

	// Outside a chat they are reports.
	for _, c := range cases {
		send(c)
	}
	if ns.calls < len(cases) {
		t.Fatalf("outside a chat %d requests were sent for %d payloads", ns.calls, len(cases))
	}

	t.Setenv("CONDUCTOR_PROTOCOL", "1")
	ns.calls = 0
	for _, c := range cases {
		if in := send(c); in.read != len(c.stdin) && c.args[0] != "--codex" {
			t.Fatalf("%v: %d of %d bytes of the payload read", c.args, in.read, len(c.stdin))
		}
	}
	if ns.calls != 0 {
		t.Fatalf("a chat's hooks sent %d requests (the last to %s: %v)", ns.calls, ns.path, ns.body)
	}

	// The agent's own question is no hook's: it is sent.
	if code, stderr, err := runNotifyWith(t, "", "--ask", "--message", "Which one?"); code != 0 || err != nil || ns.calls != 1 {
		t.Fatalf("--ask in a chat: exit %d %v %q, %d requests", code, err, stderr, ns.calls)
	}
}
