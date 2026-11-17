package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// notifyServer records the one request `conductor notify` makes and points
// the session environment at it.
type notifyServer struct {
	path  string
	auth  string
	body  map[string]any
	calls int
}

func startNotifyServer(t *testing.T) *notifyServer {
	t.Helper()
	ns := &notifyServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns.calls++
		ns.path, ns.auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&ns.body)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-token")
	// A test run inside a hosted session reports to no host of its own.
	t.Setenv("CONDUCTOR_HOST_SOCKET", "")
	return ns
}

func runNotifyWith(t *testing.T, stdin string, args ...string) (int, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	code, err := runNotify(context.Background(), args, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)
	return code, stderr.String(), err
}

func TestNotifyEventFlagsPostToTheEventsRoute(t *testing.T) {
	ns := startNotifyServer(t)
	code, stderr, err := runNotifyWith(t, "", "--event", "artifact", "--message", "PR opened", "--url", "https://github.com/x/y/pull/1", "--to", "review", "--tool", "gh")
	if code != 0 || err != nil {
		t.Fatalf("exit %d %v %q", code, err, stderr)
	}
	if ns.path != "/api/sessions/s1/events" || ns.auth != "Bearer agent-token" {
		t.Fatalf("server saw %q %q", ns.path, ns.auth)
	}
	want := map[string]any{"type": "artifact", "message": "PR opened", "url": "https://github.com/x/y/pull/1", "to": "review", "tool": "gh"}
	if !reflect.DeepEqual(ns.body, want) {
		t.Fatalf("body %v, want %v", ns.body, want)
	}
}

func TestNotifyEventNeedsNoOtherFlag(t *testing.T) {
	ns := startNotifyServer(t)
	if code, stderr, err := runNotifyWith(t, "", "--event", "progress"); code != 0 || err != nil {
		t.Fatalf("exit %d %v %q", code, err, stderr)
	}
	if want := map[string]any{"type": "progress"}; !reflect.DeepEqual(ns.body, want) {
		t.Fatalf("body %v, want %v", ns.body, want)
	}
}

// Without --event the command reports an attention state as it always did.
func TestNotifyWithoutEventStillPostsAttention(t *testing.T) {
	ns := startNotifyServer(t)
	if code, stderr, err := runNotifyWith(t, "", "--state", "done", "--message", "finished"); code != 0 || err != nil {
		t.Fatalf("exit %d %v %q", code, err, stderr)
	}
	want := map[string]any{"state": "done", "message": "finished"}
	if ns.path != "/api/sessions/s1/attention" || !reflect.DeepEqual(ns.body, want) {
		t.Fatalf("server saw %q %v", ns.path, ns.body)
	}
}

// --event and a hook payload are two ways to say what happened; taking one
// silently would hide a wrong install. A hook runner reads exit 2 as "block
// the agent", so a hook's mistake exits 1.
func TestNotifyEventRefusesHookFlags(t *testing.T) {
	ns := startNotifyServer(t)
	for _, args := range [][]string{
		{"--event", "progress", "--claude-hook"},
		{"--event", "progress", "--codex", "{}"},
		{"--event", "progress", "--codex-hook"},
		{"--event", "progress", "--copilot-hook"},
		{"--event", "progress", "--cursor-hook"},
		{"--event", "progress", "--agy-hook"},
		{"--event", "progress", "--goose-hook"},
	} {
		code, _, err := runNotifyWith(t, `{"hook_event_name":"Stop"}`, args...)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "--event") {
			t.Fatalf("%v: exit %d, err %v", args, code, err)
		}
	}
	if ns.calls != 0 {
		t.Fatalf("%d requests were sent", ns.calls)
	}
}

// --state has a default, so an explicit one is the only way to know the
// caller meant it; giving both says two different things and one would be
// dropped without a word.
func TestNotifyEventRefusesAnExplicitState(t *testing.T) {
	ns := startNotifyServer(t)
	for _, args := range [][]string{
		{"--event", "progress", "--state", "done"},
		{"--state", "needs_input", "--event", "progress"}, // the default, said out loud, still counts
		{"--event", "progress", "--message", "m", "--state=clear"},
	} {
		code, _, err := runNotifyWith(t, "", args...)
		if code != 2 || err == nil || !strings.Contains(err.Error(), "--event") || !strings.Contains(err.Error(), "--state") {
			t.Fatalf("%v: exit %d, err %v", args, code, err)
		}
	}
	if ns.calls != 0 {
		t.Fatalf("%d requests were sent", ns.calls)
	}
	// The default alone is not a conflict.
	if code, stderr, err := runNotifyWith(t, "", "--event", "progress"); code != 0 || err != nil {
		t.Fatalf("--event alone: exit %d %v %q", code, err, stderr)
	}
	if ns.calls != 1 {
		t.Fatalf("%d requests were sent, want 1", ns.calls)
	}
}

// --url, --to and --tool say more about an event: without --event nothing
// would carry them, and dropping them without a word would hide the mistake.
// Outside a hook that is exit 2; a hook's command line exits 1, as ever.
func TestNotifyEventFieldsNeedAnEvent(t *testing.T) {
	ns := startNotifyServer(t)
	for _, c := range []struct {
		args  []string
		code  int
		flags []string
	}{
		{[]string{"--url", "https://example.com/pull/1"}, 2, []string{"--url"}},
		{[]string{"--state", "done", "--to", "review"}, 2, []string{"--to"}},
		{[]string{"--message", "m", "--tool=Bash"}, 2, []string{"--tool"}},
		{[]string{"--url", "", "--state", "working"}, 2, []string{"--url"}}, // given, if empty
		{[]string{"--tool", "t", "--to", "x", "--url", "u"}, 2, []string{"--url", "--to", "--tool"}},
		{[]string{"--claude-hook", "--tool", "Bash"}, 1, []string{"--tool"}},
		{[]string{"--codex", "--url", "u", "{}"}, 1, []string{"--url"}},
	} {
		code, _, err := runNotifyWith(t, `{"hook_event_name":"Stop"}`, c.args...)
		if code != c.code || err == nil || !strings.Contains(err.Error(), "--event") {
			t.Errorf("%v: exit %d, err %v; want exit %d and an error naming --event", c.args, code, err, c.code)
			continue
		}
		for _, f := range c.flags {
			if !strings.Contains(err.Error(), f) {
				t.Errorf("%v: the error %q does not name %s", c.args, err, f)
			}
		}
	}
	if ns.calls != 0 {
		t.Fatalf("%d requests were sent", ns.calls)
	}
	// With --event they are what the event says.
	if code, stderr, err := runNotifyWith(t, "", "--event", "tool_use", "--tool", "Bash"); code != 0 || err != nil {
		t.Fatalf("--event with --tool: exit %d %v %q", code, err, stderr)
	}
}

// Outside a session the command stays a silent no-op, --event or not.
func TestNotifyEventOutsideASessionIsSilent(t *testing.T) {
	t.Setenv("CONDUCTOR_NOTIFY_URL", "")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "")
	code, stderr, err := runNotifyWith(t, "", "--event", "progress", "--message", "x")
	if code != 0 || err != nil || stderr != "" {
		t.Fatalf("exit %d %v %q", code, err, stderr)
	}
}

// Each agent's hook flag reads the payload its hooks write to stdin, and sends
// what the payload maps to: an attention state to the attention route, an
// event to the events route.
func TestNotifyHookFlagsMapTheirPayloads(t *testing.T) {
	cases := []struct {
		flag, stdin, path string
		body              map[string]any
	}{
		{"--copilot-hook", `{"sessionId":"s","stopReason":"end_turn"}`, "/api/sessions/s1/attention", map[string]any{"state": "done", "kind": "done", "agentSession": "s", "turn": true}},
		// A file edit posts its tool call, then the file it touched (the call the server saw last).
		{"--cursor-hook", `{"hook_event_name":"afterFileEdit","file_path":"/x/a.go"}`, "/api/sessions/s1/events", map[string]any{"type": "file", "op": "edit", "path": "/x/a.go", "tool": "edit a.go"}},
		{"--agy-hook", `{"terminationReason":"completed"}`, "/api/sessions/s1/attention", map[string]any{"state": "done", "message": "completed", "kind": "done", "turn": true}},
		{"--goose-hook", `{"event":"PostToolUse","tool_name":"shell"}`, "/api/sessions/s1/events", map[string]any{"type": "tool_use", "tool": "shell"}},
		// A tool about to run is the agent working (#195), for every agent whose hooks say so.
		{"--copilot-hook", `{"sessionId":"s","toolName":"bash","toolArgs":{"command":"ls"}}`, "/api/sessions/s1/attention", map[string]any{"state": "working"}},
		{"--cursor-hook", `{"conversation_id":"c","hook_event_name":"preToolUse","tool_name":"Shell"}`, "/api/sessions/s1/attention", map[string]any{"state": "working"}},
		{"--agy-hook", `{"toolCall":{"name":"run_command","args":{}}}`, "/api/sessions/s1/attention", map[string]any{"state": "working"}},
		{"--goose-hook", `{"event":"PreToolUse","tool_name":"shell"}`, "/api/sessions/s1/attention", map[string]any{"state": "working"}},
		{"--codex-hook", `{"hook_event_name":"PermissionRequest","tool_name":"shell"}`, "/api/sessions/s1/attention", map[string]any{"state": "needs_input", "message": "Allow shell?", "kind": "permission"}},
		{"--claude-hook", `{"hook_event_name":"PermissionDenied","tool_name":"Bash"}`, "/api/sessions/s1/events", map[string]any{"type": "tool_denied", "tool": "Bash"}},
	}
	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			ns := startNotifyServer(t)
			if code, stderr, err := runNotifyWith(t, c.stdin, c.flag); code != 0 || err != nil {
				t.Fatalf("exit %d %v %q", code, err, stderr)
			}
			want := 1
			if c.flag == "--cursor-hook" && strings.Contains(c.stdin, "afterFileEdit") {
				want = 2
			}
			if ns.calls != want || ns.path != c.path || !reflect.DeepEqual(ns.body, c.body) {
				t.Fatalf("server saw %d calls, %q %v; want %d, %q %v", ns.calls, ns.path, ns.body, want, c.path, c.body)
			}
		})
	}
}

// A payload that means nothing to Conductor is not sent, and the hook still
// succeeds.
func TestNotifyHookFlagsSkipOtherPayloads(t *testing.T) {
	ns := startNotifyServer(t)
	for _, flag := range []string{"--codex-hook", "--copilot-hook", "--cursor-hook", "--agy-hook", "--goose-hook"} {
		if code, stderr, err := runNotifyWith(t, `{"unrelated":true}`, flag); code != 0 || err != nil || stderr != "" {
			t.Fatalf("%s: exit %d %v %q", flag, code, err, stderr)
		}
	}
	if ns.calls != 0 {
		t.Fatalf("%d requests were sent", ns.calls)
	}
}

// One payload, one way to read it: two hook flags are a wrong install.
func TestNotifyTakesOneHookFlag(t *testing.T) {
	ns := startNotifyServer(t)
	for _, args := range [][]string{
		{"--claude-hook", "--codex-hook"},
		{"--copilot-hook", "--codex", "{}"},
		{"--cursor-hook", "--goose-hook", "--agy-hook"},
	} {
		code, _, err := runNotifyWith(t, `{"hook_event_name":"stop"}`, args...)
		if code != 1 || err == nil || !strings.Contains(err.Error(), args[0]) || !strings.Contains(err.Error(), args[1]) {
			t.Fatalf("%v: exit %d, err %v", args, code, err)
		}
	}
	if ns.calls != 0 {
		t.Fatalf("%d requests were sent", ns.calls)
	}
}

// Claude-shaped hook runners read exit 2 as "block": a hook's usage mistake,
// even one the flag parser finds, exits 1 and says why on stderr. Outside a
// hook the command keeps exiting 2 for a usage mistake.
func TestNotifyHookModeNeverExitsTwo(t *testing.T) {
	startNotifyServer(t)
	for _, args := range [][]string{
		{"--claude-hook", "--no-such-flag"},
		{"--no-such-flag", "--copilot-hook"},
		{"--codex", "--no-such-flag", "{}"},
		{"--goose-hook=true", "--agy-hook"},
		{"--cursor-hook", "--state", "done", "--event", "progress"},
	} {
		code, stderr, err := runNotifyWith(t, "{}", args...)
		if code != 1 || err == nil {
			t.Errorf("%v: exit %d, %v", args, code, err)
		}
		_ = stderr
	}
	for _, args := range [][]string{{"--no-such-flag"}, {"--claude-hook=false", "--no-such-flag"}} {
		if code, _, _ := runNotifyWith(t, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// countingReader records how much of its payload was read.
type countingReader struct {
	r    io.Reader
	read int
	eof  bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	c.eof = c.eof || err == io.EOF
	return n, err
}

// A hook runner writes the payload whatever happens to it: outside a session
// the command still reads it, up to 1 MiB, so the runner never writes into a
// closed pipe. --codex takes its payload as an argument, and its stdin is
// Codex's own: it is not read.
func TestNotifyHookReadsItsPayloadOutsideASession(t *testing.T) {
	t.Setenv("CONDUCTOR_NOTIFY_URL", "")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "")
	run := func(payload string, args ...string) *countingReader {
		t.Helper()
		in := &countingReader{r: strings.NewReader(payload)}
		code, err := runNotify(context.Background(), args, in, &bytes.Buffer{}, &bytes.Buffer{})
		if code != 0 || err != nil {
			t.Fatalf("%v: exit %d %v", args, code, err)
		}
		return in
	}
	small := `{"hook_event_name":"Stop","pad":"` + strings.Repeat("x", 100<<10) + `"}`
	if in := run(small, "--claude-hook"); !in.eof || in.read != len(small) {
		t.Fatalf("read %d of %d bytes, eof %v", in.read, len(small), in.eof)
	}
	if in := run(strings.Repeat("x", 3<<20), "--copilot-hook"); in.read != 1<<20 {
		t.Fatalf("read %d bytes of a 3 MiB payload, want 1 MiB", in.read)
	}
	if in := run("typed by the user", "--codex", "{}"); in.read != 0 {
		t.Fatalf("--codex read %d bytes of its stdin", in.read)
	}
	if in := run("not a hook", "--state", "done"); in.read != 0 {
		t.Fatalf("a plain report read %d bytes of its stdin", in.read)
	}
}

// A terminal is never read: nothing writes a payload to one, and the keys on
// it belong to the agent. The command returns at once, in a session or not.
func TestNotifyHookDoesNotReadATerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	for _, inSession := range []bool{false, true} {
		var ns *notifyServer
		if inSession {
			ns = startNotifyServer(t)
		} else {
			t.Setenv("CONDUCTOR_NOTIFY_URL", "")
			t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "")
		}
		done := make(chan int, 1)
		go func() {
			code, _ := runNotify(context.Background(), []string{"--claude-hook"}, tty, &bytes.Buffer{}, &bytes.Buffer{})
			done <- code
		}()
		select {
		case code := <-done:
			if code != 0 || (ns != nil && ns.calls != 0) {
				t.Fatalf("in session %v: exit %d", inSession, code)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("in session %v: the command waited on the terminal", inSession)
		}
	}
}

// Every flag that maps an agent's payload makes the command a hook's, which
// exits 1 and not 2 on a mistake: hookMode reads the table the flags come from.
func TestHookModeKnowsEveryPayloadFlag(t *testing.T) {
	for _, h := range hookPayloads {
		if !hookMode([]string{"--" + h.name}) {
			t.Errorf("--%s is not a payload flag", h.name)
		}
	}
	if !hookMode([]string{"--codex", "{}"}) || hookMode([]string{"--state", "done"}) {
		t.Fatal("--codex or --state misread")
	}
	if len(payloadFlags) != len(hookPayloads)+1 {
		t.Fatalf("payloadFlags %v", payloadFlags)
	}
}

// --choices with --state needs_input posts kind prompt and one option per
// choice whose input is the choice and a CR (a click types it as a line);
// it refuses an event, a hook payload, another state, too many choices, an
// empty one and a long one, and goes along with the session's silence.
func TestNotifyChoices(t *testing.T) {
	ns := startNotifyServer(t)
	code, _, err := runNotifyWith(t, "", "--state", "needs_input", "--message", "Which database?", "--choices", "Postgres| SQLite |Keep both")
	if code != 0 || err != nil || ns.calls != 1 || ns.path != "/api/sessions/s1/attention" {
		t.Fatalf("exit %d %v, %d calls to %s", code, err, ns.calls, ns.path)
	}
	want := map[string]any{"state": "needs_input", "message": "Which database?", "kind": "prompt", "options": []any{
		map[string]any{"label": "Postgres", "input": "Postgres\r"},
		map[string]any{"label": "SQLite", "input": "SQLite\r"},
		map[string]any{"label": "Keep both", "input": "Keep both\r"},
	}}
	if !reflect.DeepEqual(ns.body, want) {
		t.Fatalf("body %v", ns.body)
	}
	// A misuse exits 2, or 1 from a hook's command line (a hook runner reads
	// 2 as "block the agent").
	for _, tc := range []struct {
		args []string
		word string
		exit int
	}{
		{[]string{"--event", "progress", "--choices", "a|b"}, "not with --event", 2},
		{[]string{"--claude-hook", "--choices", "a|b"}, "not with --claude-hook", 1},
		{[]string{"--state", "done", "--choices", "a|b"}, "not with --state done", 2},
		{[]string{"--choices", "a|b|c|d|e|f|g"}, "7 choices, at most 6", 2},
		{[]string{"--choices", "a||b"}, "an empty choice", 2},
		{[]string{"--choices", "a|" + strings.Repeat("x", 41)}, "longer than 40 bytes", 2},
		{[]string{"--choices", "a|b\tc"}, "control character", 2},
	} {
		ns.calls = 0
		code, _, err := runNotifyWith(t, "", tc.args...)
		if code != tc.exit || err == nil || !strings.Contains(err.Error(), tc.word) || ns.calls != 0 {
			t.Errorf("%v: exit %d %v, %d calls", tc.args, code, err, ns.calls)
		}
	}
	// Six choices of forty bytes fit.
	ns.calls = 0
	long := strings.Repeat("y", 40)
	if code, _, err := runNotifyWith(t, "", "--choices", strings.Join([]string{long, "b", "c", "d", "e", "f"}, "|")); code != 0 || err != nil || ns.calls != 1 {
		t.Fatalf("six of forty: exit %d %v, %d calls", code, err, ns.calls)
	}
	// Outside a session: silent, like every notify.
	t.Setenv("CONDUCTOR_NOTIFY_URL", "")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "")
	if code, _, err := runNotifyWith(t, "", "--choices", "a|b"); code != 0 || err != nil {
		t.Fatalf("outside: exit %d %v", code, err)
	}
}

// --ask reports the agent's own question (#193), with --choices or
// without: the attention route's ask, with needs_input. A plain --state
// needs_input carries no mark (OpenCode's, DeepSeek Harness's and aider's
// permission waits report that way), and no hook payload ever does. --ask
// with another state, an event or a hook flag is a misuse.
func TestNotifyAskMarksTheAgentsOwnQuestion(t *testing.T) {
	ns := startNotifyServer(t)
	for _, tc := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"--ask", "--message", "Which database?"}, map[string]any{"state": "needs_input", "message": "Which database?", "ask": true}},
		{[]string{"--state", "needs_input", "--ask", "--message", "Which database?", "--choices", "Postgres|SQLite"}, map[string]any{"state": "needs_input", "message": "Which database?", "ask": true, "kind": "prompt", "options": []any{
			map[string]any{"label": "Postgres", "input": "Postgres\r"},
			map[string]any{"label": "SQLite", "input": "SQLite\r"},
		}}},
		{[]string{"--state", "needs_input", "--message", "OpenCode asks for permission"}, map[string]any{"state": "needs_input", "message": "OpenCode asks for permission"}},
	} {
		ns.calls, ns.body = 0, nil
		if code, stderr, err := runNotifyWith(t, "", tc.args...); code != 0 || err != nil || ns.calls != 1 {
			t.Fatalf("%v: exit %d %v %q, %d calls", tc.args, code, err, stderr, ns.calls)
		}
		if ns.path != "/api/sessions/s1/attention" || !reflect.DeepEqual(ns.body, tc.want) {
			t.Fatalf("%v: %s %v, want %v", tc.args, ns.path, ns.body, tc.want)
		}
	}
	// No hook payload carries the mark, whatever it maps to.
	for _, payload := range []string{
		`{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`,
		`{"hook_event_name":"Stop","session_id":"s"}`,
	} {
		ns.calls, ns.body = 0, nil
		if code, stderr, err := runNotifyWith(t, payload, "--claude-hook"); code != 0 || err != nil || ns.calls != 1 {
			t.Fatalf("%s: exit %d %v %q", payload, code, err, stderr)
		}
		if _, marked := ns.body["ask"]; marked {
			t.Fatalf("%s: body %v", payload, ns.body)
		}
	}
	ns.calls, ns.body = 0, nil
	if code, _, err := runNotifyWith(t, "", "--codex", `{"type":"agent-turn-complete","thread-id":"t","last-assistant-message":"ok"}`); code != 0 || err != nil || ns.calls != 1 || ns.body["ask"] != nil {
		t.Fatalf("--codex: exit %d %v, body %v", code, err, ns.body)
	}
	for _, tc := range []struct {
		args []string
		word string
		exit int
	}{
		{[]string{"--ask", "--state", "done", "--message", "x"}, "not with --state done", 2},
		{[]string{"--ask", "--state", "clear"}, "not with --state clear", 2},
		{[]string{"--ask", "--event", "progress", "--message", "x"}, "not with --event", 2},
		{[]string{"--ask", "--claude-hook"}, "not with --claude-hook", 1},
		{[]string{"--ask", "--codex", "{}"}, "not with --codex", 1},
	} {
		ns.calls = 0
		code, _, err := runNotifyWith(t, "", tc.args...)
		if code != tc.exit || err == nil || !strings.Contains(err.Error(), tc.word) || ns.calls != 0 {
			t.Errorf("%v: exit %d %v, %d calls", tc.args, code, err, ns.calls)
		}
	}
}

// --files with a hook payload reports only the files the tool call touched
// (Claude Code's file-tool hook in the base settings): an Edit is one file
// event and no tool call; a call that names no file sends nothing; the flag
// needs a payload.
func TestNotifyFilesReportsOnlyTheFiles(t *testing.T) {
	ns := startNotifyServer(t)
	edit := `{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/x/a.go","old_string":"a","new_string":"b"}}`
	if code, stderr, err := runNotifyWith(t, edit, "--claude-hook", "--files"); code != 0 || err != nil {
		t.Fatalf("exit %d %v %q", code, err, stderr)
	}
	if ns.calls != 1 || ns.path != "/api/sessions/s1/events" || !reflect.DeepEqual(ns.body, map[string]any{"type": "file", "op": "edit", "path": "/x/a.go", "tool": "Edit"}) {
		t.Fatalf("server saw %d calls, %q %v", ns.calls, ns.path, ns.body)
	}
	if code, _, err := runNotifyWith(t, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, "--claude-hook", "--files"); code != 0 || err != nil || ns.calls != 1 {
		t.Fatalf("a call without a file: exit %d %v, %d calls", code, err, ns.calls)
	}
	if code, _, err := runNotifyWith(t, "", "--files"); code != 2 || err == nil || !strings.Contains(err.Error(), "--files") {
		t.Fatalf("--files alone: exit %d %v", code, err)
	}
}
