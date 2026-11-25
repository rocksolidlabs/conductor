package agentproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

var update = flag.Bool("update", false, "write the fixtures' expected events")

// The argv of a chat's child, element for element, as design 4 lists it:
// the command, print mode with stream-json, the protocol's arguments, the
// resumed conversation, the model, --permission-mode default (a yolo
// session's yolo arguments in its place), the terminal's arguments and
// Conductor's MCP config; with a permission host, the permission tool and
// exactly one --settings, the floor. Never --bare, --setting-sources,
// --strict-mcp-config or the SDK's stdio prompt route; an argument of the
// terminal's that would set one of the chat's own flags is refused.
func TestClaudeArgvIsTheDocumentedOne(t *testing.T) {
	hooks := "/data/hooks"
	conv := "11111111-2222-4333-8444-555555555555"
	stream := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--replay-user-messages"}
	join := func(parts ...[]string) []string { return slices.Concat(parts...) }
	cases := []struct {
		name string
		l    ClaudeLaunch
		want []string
	}{
		{"a chat", ClaudeLaunch{Command: []string{"claude"}, Conversation: conv, Model: []string{"--model=opus"}, LaunchArgs: []string{"--add-dir=/srv/docs"}, HooksDir: hooks},
			join([]string{"claude"}, stream, []string{"--resume", conv, "--model=opus", "--permission-mode", "default", "--add-dir=/srv/docs", "--mcp-config", hooks + "/claude-protocol-mcp.json"})},
		{"a yolo session", ClaudeLaunch{Command: []string{"claude"}, Protocol: []string{"--effort=high"}, Conversation: conv, Yolo: true, YoloArgs: []string{"--dangerously-skip-permissions"}, HooksDir: hooks},
			join([]string{"claude"}, stream, []string{"--effort=high", "--resume", conv, "--dangerously-skip-permissions", "--mcp-config", hooks + "/claude-protocol-mcp.json"})},
		{"a chat with its permission host", ClaudeLaunch{Command: []string{"/opt/claude", "--x"}, Conversation: conv, HooksDir: hooks, PermissionHost: true},
			join([]string{"/opt/claude", "--x"}, stream, []string{"--resume", conv, "--permission-mode", "default", "--mcp-config", hooks + "/claude-protocol-mcp.json", "--permission-prompt-tool", "mcp__conductor__permission", "--settings", hooks + "/claude-protocol.json"})},
		{"a yolo session with its permission host", ClaudeLaunch{Command: []string{"claude"}, Conversation: conv, Yolo: true, YoloArgs: []string{"--dangerously-skip-permissions"}, HooksDir: hooks, PermissionHost: true},
			join([]string{"claude"}, stream, []string{"--resume", conv, "--dangerously-skip-permissions", "--mcp-config", hooks + "/claude-protocol-mcp.json", "--permission-prompt-tool", "mcp__conductor__permission", "--settings", hooks + "/claude-protocol-yolo.json"})},
		{"a yolo launch with no recipe", ClaudeLaunch{Command: []string{"claude"}, Conversation: conv, Yolo: true, HooksDir: hooks},
			join([]string{"claude"}, stream, []string{"--resume", conv, "--permission-mode", "default", "--mcp-config", hooks + "/claude-protocol-mcp.json"})},
	}
	for _, c := range cases {
		got, err := ClaudeArgs(c.l)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		for _, never := range []string{"--bare", "--setting-sources", "--strict-mcp-config", "stdio"} {
			if slices.ContainsFunc(got, func(a string) bool { return a == never || strings.HasPrefix(a, never+"=") }) {
				t.Errorf("%s: %s in %q", c.name, never, got)
			}
		}
		if n := count(got, "--settings"); n > 1 || (n == 1) != c.l.PermissionHost {
			t.Errorf("%s: --settings %d times", c.name, n)
		}
		if !c.l.Yolo && count(got, "--permission-mode") != 1 {
			t.Errorf("%s: --permission-mode missing", c.name)
		}
	}

	for _, bad := range []ClaudeLaunch{
		{Conversation: conv, HooksDir: hooks},
		{Command: []string{"claude"}, HooksDir: hooks},
		{Command: []string{"claude"}, Conversation: "--continue", HooksDir: hooks},
		{Command: []string{"claude"}, Conversation: "a b", HooksDir: hooks},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: "hooks"},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, LaunchArgs: []string{"--settings", "/x.json"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, LaunchArgs: []string{"--settings=/x.json"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, LaunchArgs: []string{"--bare"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, LaunchArgs: []string{"--setting-sources=local"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, Protocol: []string{"--strict-mcp-config"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, Model: []string{"--permission-mode=plan"}},
		{Command: []string{"claude"}, Conversation: conv, HooksDir: hooks, LaunchArgs: []string{"--dangerously-skip-permissions"}},
	} {
		if got, err := ClaudeArgs(bad); err == nil {
			t.Errorf("%+v made %q", bad, got)
		}
	}
}

func count(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			n++
		}
	}
	return n
}

// written is each line the driver wrote, decoded.
func written(t *testing.T, d *discard) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(d.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// I19: a message reaches the child's input as written, with
// client_composed, so that Claude Code expands no @path in it and runs no
// /command: the text unchanged, nothing escaped that the JSON did not need.
func TestClaudeSendIsVerbatim(t *testing.T) {
	in := &discard{}
	c := NewClaude(in, strings.NewReader(""), ClaudeOptions{})
	texts := []string{"@/home/someone/.conductor/x", "/help", "a <b> & \"c\"\nline two\t\u00e9"}
	for i, text := range texts {
		if err := c.Send(context.Background(), session.Message{Ref: "r" + string(rune('0'+i)), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	raw := strings.Split(strings.TrimSpace(in.String()), "\n")
	if len(raw) != len(texts) {
		t.Fatalf("%d lines for %d messages", len(raw), len(texts))
	}
	for i, l := range raw {
		var m struct {
			Type            string  `json:"type"`
			UUID            string  `json:"uuid"`
			ClientComposed  *bool   `json:"client_composed"`
			ParentToolUseID *string `json:"parent_tool_use_id"`
			Message         struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}
		dec := json.NewDecoder(strings.NewReader(l))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		if m.Type != "user" || m.Message.Role != "user" || m.ParentToolUseID != nil || !uuidShape.MatchString(m.UUID) {
			t.Fatalf("message %d: %q", i, l)
		}
		if m.ClientComposed == nil || !*m.ClientComposed {
			t.Fatalf("message %d without client_composed: %q", i, l)
		}
		if m.Message.Content != texts[i] {
			t.Fatalf("message %d changed: %q, want %q", i, m.Message.Content, texts[i])
		}
		if strings.Contains(l, `\u003c`) || strings.Contains(l, `\u0026`) {
			t.Fatalf("message %d escaped: %q", i, l)
		}
	}
	if !c.Verbatim() || c.Name() != "claude-stream-json" {
		t.Fatal("the driver does not say it sends as written")
	}
	if c.Written() != int64(len(in.String())) {
		t.Fatalf("counted %d bytes, wrote %d", c.Written(), len(in.String()))
	}
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// I4: the replay of a written message is that message's acknowledgement,
// by the uuid the driver wrote (the recorded run's replays carry it), in
// whatever order the replays come; a replay that names a message the
// driver never wrote accepts nothing; and where a replay names none,
// replays are matched to writes in order. Never by time.
func TestClaudeReplayNamesTheRef(t *testing.T) {
	in := &discard{}
	pr, pw := io.Pipe()
	c := NewClaude(in, pr, ClaudeOptions{})
	rec := &recorder{}
	done := make(chan error, 1)
	go func() { done <- c.Read(context.Background(), rec) }()
	for _, ref := range []string{"a", "b", "c", "d"} {
		if err := c.Send(context.Background(), session.Message{Ref: ref, Text: ref}); err != nil {
			t.Fatal(err)
		}
	}
	msgs := written(t, in)
	uuidOf := func(i int) string { return msgs[i]["uuid"].(string) }
	replayLine := func(uuid string) string {
		m := map[string]any{"type": "user", "isReplay": true, "session_id": "s", "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": "x"}}
		if uuid != "" {
			m["uuid"] = uuid
		}
		b, _ := json.Marshal(m)
		return string(b) + "\n"
	}
	for _, l := range []string{
		replayLine(uuidOf(1)),                              // b first, by its uuid
		replayLine("99999999-9999-4999-8999-999999999999"), // nobody's
		replayLine(uuidOf(0)),                              // then a
		replayLine(""),                                     // none named: the oldest written, c
		replayLine(""),                                     // then d
		replayLine(""),                                     // nothing left
	} {
		if _, err := io.WriteString(pw, l); err != nil {
			t.Fatal(err)
		}
	}
	pw.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("Read: %v", err)
	}
	var got []string
	for _, e := range rec.kinds("accepted") {
		got = append(got, e.ref)
	}
	if want := []string{"b", "a", "c", "d"}; !slices.Equal(got, want) {
		t.Fatalf("accepted %q, want %q", got, want)
	}

	// The recorded run: each replay names the message written.
	rec, _, err := replay(t, bytes.NewReader(readFixture(t, "read-and-command.jsonl")))
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	got = nil
	for _, e := range rec.kinds("accepted") {
		got = append(got, e.ref)
	}
	if want := []string{"m1", "m2"}; !slices.Equal(got, want) {
		t.Fatalf("the fixture accepted %q, want %q", got, want)
	}
}

// Every fixture fed to Read gives the events written beside it
// (<fixture>.events, made with -update and read by a person), and each
// holds what its turn is for: words, a tool step with its result, a file
// edit with its counts, background work, a result with usage.
func TestClaudeFixturesReplay(t *testing.T) {
	names, _ := filepath.Glob(fixture("*.jsonl"))
	if len(names) != 6 {
		t.Fatalf("%d fixtures", len(names))
	}
	got := map[string]*recorder{}
	for _, path := range names {
		name := filepath.Base(path)
		rec, _, err := replay(t, bytes.NewReader(readFixture(t, name)))
		wantErr := io.EOF
		if name == "hand-built-not-signed-in.jsonl" {
			wantErr = ErrNotSignedIn
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("%s: Read ended with %v, want %v", name, err, wantErr)
		}
		got[name] = rec
		events := strings.TrimSuffix(path, ".jsonl") + ".events"
		if *update {
			if err := os.WriteFile(events, []byte(rec.render()), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(events)
		if err != nil {
			t.Fatal(err)
		}
		if rec.render() != string(want) {
			t.Errorf("%s gave\n%s\nwant\n%s", name, rec.render(), want)
		}
	}

	// A turn with words, a tool step with its result, a result with usage.
	r := got["read-and-command.jsonl"].render()
	for _, want := range []string{
		"conversation 00000000-0000-4000-8000-000000000001\n",
		"accepted m1\nturn_started\n",
		`tool_started toolu_01Fixture000000000000001 parent="" Read summary="/work/notes.txt"`,
		`tool_ended toolu_01Fixture000000000000001 ok exit=none cut=false output="1\tone"`,
		"final=true \"The first line of `notes.txt` is:\\n\\n```\\none\\n```\"",
		`tool_started toolu_01Fixture000000000000002 parent="" Bash summary="wc -l notes.txt"`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("read-and-command: no %q in\n%s", want, r)
		}
	}
	ends := got["read-and-command.jsonl"].kinds("turn_ended")
	if len(ends) != 2 || ends[0].out != "done" || ends[0].use.InputTokens == 0 || ends[0].use.OutputTokens != 113 || ends[0].use.CostUSD != 0 || ends[1].use.CostUSD <= 0 {
		t.Errorf("read-and-command's turns ended %+v", ends)
	}
	// A denied command.
	if d := got["denied-and-command.jsonl"].kinds("tool_ended"); len(d) != 2 || d[0].res.Status != session.ToolDenied || d[1].res.Status != session.ToolOK {
		t.Errorf("denied-and-command:\n%s", got["denied-and-command.jsonl"].render())
	}
	// A file edit with its counts and its diff.
	files := got["edit.jsonl"].kinds("file_changed")
	if len(files) != 1 || files[0].file.Path != "/work/notes.txt" || files[0].file.Op != session.ChangeEdit || files[0].file.Added != 1 || files[0].file.Removed != 1 ||
		string(files[0].file.Diff) != "@@ -1,3 +1,3 @@\n one\n-two\n+2\n three\n" {
		t.Errorf("edit's file changes: %+v", files)
	}
	// Background work: on with the backgrounded command, off once it ended;
	// an ambient task is none of the session's.
	var bg []bool
	for _, e := range got["hand-built-background.jsonl"].kinds("background") {
		bg = append(bg, e.bg)
	}
	if !slices.Equal(bg, []bool{true, false}) {
		t.Errorf("background %v", bg)
	}
	b := got["hand-built-background.jsonl"].render()
	if strings.Index(b, "background false") < strings.Index(b, "turn_ended done") {
		t.Errorf("the background work ended before the turn:\n%s", b)
	}
	// An MCP call ended by the idle check, one that waited, an interrupt.
	m := got["mcp-and-interrupt.jsonl"]
	var outcomes []string
	for _, e := range m.kinds("turn_ended") {
		outcomes = append(outcomes, e.out)
	}
	if !slices.Equal(outcomes, []string{"done", "done", "interrupted"}) {
		t.Errorf("mcp-and-interrupt's turns ended %q", outcomes)
	}
	if r := m.render(); !strings.Contains(r, `mcp__plain__wait summary=""`) || !strings.Contains(r, `output="waited 150 seconds"`) || !regexp.MustCompile(`tool_ended \S+ error exit=none cut=false output="MCP server \\"plain\\"`).MatchString(r) {
		t.Errorf("mcp-and-interrupt:\n%s", r)
	}
	// Not signed in: the start's first answer says so.
	n := got["hand-built-not-signed-in.jsonl"]
	if notices := n.kinds("notice"); len(notices) != 1 || notices[0].code != NoticeNotSignedIn || notices[0].text != NotSignedInWords || len(n.kinds("turn_ended")) != 0 {
		t.Errorf("not signed in:\n%s", n.render())
	}
}

// An event type and fields the fixtures lack change nothing: every object
// of every recorded line given a field of its own, and lines of event
// types no version has, give the same events.
func TestClaudeUnknownEventsIgnored(t *testing.T) {
	for _, name := range []string{"read-and-command.jsonl", "edit.jsonl", "mcp-and-interrupt.jsonl", "hand-built-background.jsonl"} {
		raw := readFixture(t, name)
		want, _, err := replay(t, bytes.NewReader(raw))
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		var b bytes.Buffer
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var v any
			if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			addField(v)
			line, _ := json.Marshal(v)
			b.Write(line)
			b.WriteString("\n")
			b.WriteString(`{"type":"zz_new_event","subtype":"init","session_id":"other","uuid":"x"}` + "\n")
			b.WriteString(`{"type":"system","subtype":"zz_new_subtype","task_id":"t9","tasks":[{"task_id":"t9"}]}` + "\n")
			b.WriteString(`{"no_type":true}` + "\n")
		}
		got, _, err := replay(t, &b)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("%s: %v", name, err)
		}
		if got.render() != want.render() {
			t.Errorf("%s with unknown events and fields gave\n%s\nwant\n%s", name, got.render(), want.render())
		}
	}
}

// addField gives every object in v a field no version has, but a tool
// call's input, which the session is handed whole.
func addField(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k != "input" {
				addField(e)
			}
		}
		x["zz_unknown_field"] = map[string]any{"type": "text", "text": "never shown", "tool_use_id": "nobody"}
	case []any:
		for _, e := range x {
			addField(e)
		}
	}
}

// A line of session.MaxProtocolLine bytes is read whole; one byte more is
// skipped to its end, without being kept: the step it is the result of
// ends with its output cut, a notice says so, and the read goes on to the
// next line. A line that is not JSON ends Read with its error.
func TestClaudeLineBound(t *testing.T) {
	tool := `{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_big","name":"Bash","input":{"command":"cat big"}}]},"parent_tool_use_id":null}` + "\n"
	result := func(size int) string {
		head := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_big","content":"`
		tail := `"}]},"parent_tool_use_id":null}`
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail + "\n"
	}
	end := `{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":1,"output_tokens":2}}` + "\n"

	at := result(session.MaxProtocolLine)
	if len(at) != session.MaxProtocolLine+1 {
		t.Fatalf("the line is %d bytes", len(at)-1)
	}
	rec, _, err := replay(t, strings.NewReader(tool+at+end))
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	ended := rec.kinds("tool_ended")
	if len(ended) != 1 || ended[0].res.Cut || len(ended[0].res.Output) != session.MaxProtocolLine-len(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_big","content":"`)-len(`"}]},"parent_tool_use_id":null}`) || len(rec.kinds("notice")) != 0 {
		t.Fatalf("a line at the bound: %d results, cut %v, %d notices", len(ended), len(ended) > 0 && ended[0].res.Cut, len(rec.kinds("notice")))
	}

	past := result(session.MaxProtocolLine + 1)
	rec, _, err = replay(t, strings.NewReader(tool+past+end))
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	ended = rec.kinds("tool_ended")
	notices := rec.kinds("notice")
	if len(ended) != 1 || !ended[0].res.Cut || len(ended[0].res.Output) != 0 || len(notices) != 1 || notices[0].code != NoticeTooLarge || notices[0].text != TooLargeWords {
		t.Fatalf("a line past the bound: %+v %+v", ended, notices)
	}
	if len(rec.kinds("turn_ended")) != 1 {
		t.Fatal("the read did not go on past the long line")
	}

	// A line that is not the protocol's.
	for _, bad := range []string{"not json\n", "[1,2]\n", `"a string"` + "\n", "{\"type\":\"user\"\n"} {
		rec, _, err = replay(t, strings.NewReader(tool+bad+end))
		if !errors.Is(err, ErrUnreadable) || len(rec.kinds("turn_ended")) != 0 {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// stub is a child whose output the test writes, answering the driver's
// initialize request with answer (nothing for "").
type stub struct {
	c    *Claude
	out  *io.PipeWriter
	in   *io.PipeReader
	read chan error
	rec  *recorder
}

func newStub(t *testing.T, answer string, rest ...string) *stub {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &stub{c: NewClaude(inW, outR, ClaudeOptions{}), out: outW, in: inR, read: make(chan error, 1), rec: &recorder{}}
	go func() { s.read <- s.c.Read(context.Background(), s.rec) }()
	go func() {
		br := bufio.NewReader(inR)
		first := true
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if first {
				first = false
				if !strings.Contains(l, `"subtype":"initialize"`) {
					outW.CloseWithError(errors.New("the first line was not the initialize request: " + l))
					return
				}
				if answer != "" {
					io.WriteString(outW, answer+"\n")
				}
				for _, r := range rest {
					io.WriteString(outW, r+"\n")
				}
			}
		}
	}()
	t.Cleanup(func() { outW.Close(); inR.Close() })
	return s
}

// The gate, as far as it needs no session: a probed version below
// ClaudeMinVersion, or none, is not qualified; a child that answers no
// initialize, answers it with a refusal, or fails the shape check fails
// Begin with its words, by the context's end or the output's at once; the
// recorded (account-free) answer passes; a start whose first answer says
// the agent is not signed in ends Read with ErrNotSignedIn, and the same
// error after an answered turn is the turn's error, not a sign-in's.
func TestClaudeGate(t *testing.T) {
	for v, ok := range map[string]bool{
		"": false, "2.1.330": false, "2.1.248": false, "2.0.999": false, "2.1.331": true, "2.1.331 (Claude Code)": true,
		"2.1.332": true, "2.2.0": true, "3.0": true, "2.1": false, "x.y.z": false, "2.1.331.": false, "1234567.1.1": false,
	} {
		if got := Qualified(ClaudeStreamJSON, v); got != ok {
			t.Errorf("Qualified(%q) = %v", v, got)
		}
	}
	if Qualified("acp-v1", "9.9.9") {
		t.Error("a protocol with no driver is qualified")
	}
	if !versionAtLeast(ClaudeMinVersion, "2.1.248") {
		t.Fatalf("ClaudeMinVersion %s is below the version client_composed needs", ClaudeMinVersion)
	}
	if err := Check(&catalog.ProtocolRecipe{Name: "claude-stream-json"}); err != nil {
		t.Fatal(err)
	}
	if err := Check(&catalog.ProtocolRecipe{}); err != nil {
		t.Fatal(err)
	}
	if err := Check(nil); err != nil {
		t.Fatal(err)
	}
	if err := Check(&catalog.ProtocolRecipe{Name: "codex-app-server"}); err == nil {
		t.Fatal("a protocol Conductor does not speak yet was taken")
	}

	recorded := strings.SplitN(string(readFixture(t, "read-and-command.jsonl")), "\n", 2)[0]
	answer := func(edit func(map[string]any)) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(recorded), &m)
		edit(m)
		b, _ := json.Marshal(m)
		return string(b)
	}
	payload := func(m map[string]any) map[string]any {
		return m["response"].(map[string]any)["response"].(map[string]any)
	}
	if strings.Contains(recorded, `"account"`) {
		t.Fatal("the recorded answer holds an account")
	}
	cases := []struct {
		name   string
		answer string
		want   error
	}{
		{"the recorded answer", recorded, nil},
		{"an older version", answer(func(m map[string]any) { payload(m)["claude_code_version"] = "2.1.330" }), ErrNotQualified},
		{"no version", answer(func(m map[string]any) { delete(payload(m), "claude_code_version") }), ErrNotQualified},
		{"no pending list", answer(func(m map[string]any) { delete(m["response"].(map[string]any), "pending_permission_requests") }), ErrNotStarted},
		{"a refusal", `{"type":"control_response","response":{"subtype":"error","request_id":"conductor-init-1","error":"no"}}`, ErrNotStarted},
		{"no payload", `{"type":"control_response","response":{"subtype":"success","request_id":"conductor-init-1","pending_permission_requests":[]}}`, ErrNotStarted},
	}
	for _, c := range cases {
		s := newStub(t, c.answer)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.c.Begin(ctx)
		cancel()
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: Begin %v, want %v", c.name, err, c.want)
		}
	}
	// An answer to another request is not the initialize's.
	s := newStub(t, strings.Replace(recorded, "conductor-init-1", "conductor-init-9", 1))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := s.c.Begin(ctx); !errors.Is(err, ErrNotStarted) {
		t.Errorf("another request's answer: %v", err)
	}
	cancel()
	// No answer: Begin returns by its context's end.
	s = newStub(t, "")
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := s.c.Begin(ctx); !errors.Is(err, ErrNotStarted) || !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("no answer: %v", err)
	}
	cancel()
	// The output ends unanswered: Begin returns at once, whatever its context.
	s = newStub(t, "")
	s.out.Close()
	if err := s.c.Begin(context.Background()); !errors.Is(err, ErrNotStarted) {
		t.Errorf("the output ended: %v", err)
	}

	// Not signed in: a retry for 401 before the first answer, or the
	// hand-built first result.
	for _, lines := range []string{
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":500,"error_status":401,"error":"authentication_failed"}`,
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":500,"error_status":null,"error":"authentication_failed"}`,
		string(readFixture(t, "hand-built-not-signed-in.jsonl")),
	} {
		rec, _, err := replay(t, strings.NewReader(lines+"\n"))
		if !errors.Is(err, ErrNotSignedIn) || len(rec.kinds("notice")) != 1 {
			t.Errorf("not signed in: %v %s", err, rec.render())
		}
	}
	// A retry for another reason, or after an answered turn, is no sign-in's.
	after := string(readFixture(t, "read-and-command.jsonl")) + string(readFixture(t, "hand-built-not-signed-in.jsonl"))
	for _, lines := range []string{
		`{"type":"system","subtype":"api_retry","attempt":1,"error_status":529,"error":"overloaded"}`,
		after,
	} {
		rec, _, err := replay(t, strings.NewReader(lines+"\n"))
		if !errors.Is(err, io.EOF) || len(rec.kinds("notice")) != 0 {
			t.Errorf("no sign-in's: %v %s", err, rec.render())
		}
	}
}

// Interrupt writes the interrupt control request when the child's
// system/init offered the receipt, and otherwise writes nothing and
// answers session.ErrNoInterrupt (the session restarts the child on the
// same conversation); it never signals the child, which it has no handle
// on. The recorded interrupt's result ends its turn interrupted.
func TestClaudeInterruptWithoutReceipt(t *testing.T) {
	init := func(caps string) string {
		return `{"type":"system","subtype":"init","session_id":"11111111-2222-4333-8444-555555555555","claude_code_version":"2.1.331","capabilities":[` + caps + `]}` + "\n"
	}
	for _, c := range []struct {
		name string
		caps string
		want error
	}{
		{"no receipt", `"interrupt_cancel_queued_v1","msg_lifecycle_v1"`, session.ErrNoInterrupt},
		{"no capabilities", ``, session.ErrNoInterrupt},
		{"the receipt", `"msg_lifecycle_v1","interrupt_receipt_v1"`, nil},
	} {
		in := &discard{}
		c2 := NewClaude(in, strings.NewReader(init(c.caps)), ClaudeOptions{})
		if err := c2.Read(context.Background(), &recorder{}); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		err := c2.Interrupt(context.Background())
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", c.name, err, c.want)
		}
		lines := written(t, in)
		switch {
		case c.want != nil && len(lines) != 0:
			t.Fatalf("%s: wrote %v", c.name, lines)
		case c.want == nil && (len(lines) != 1 || lines[0]["type"] != "control_request" || lines[0]["request"].(map[string]any)["subtype"] != "interrupt"):
			t.Fatalf("%s: wrote %v", c.name, lines)
		}
	}
	// Before any system/init, the receipt is not known: no interrupt.
	in := &discard{}
	if err := NewClaude(in, strings.NewReader(""), ClaudeOptions{}).Interrupt(context.Background()); !errors.Is(err, session.ErrNoInterrupt) || in.String() != "" {
		t.Fatalf("before the init: %v %q", err, in.String())
	}
}

// Past MaxClaudeInput written into the child, the next turn's end calls
// Restart, once, before the session is told of that end (so the session
// restarts at it, writing nothing queued to the child that ends): the
// caller ends the child and resumes the same conversation. At the bound,
// no restart.
func TestClaudeInputCountResumes(t *testing.T) {
	turn := `{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":1,"output_tokens":1}}` + "\n"
	pr, pw := io.Pipe()
	restarts := make(chan int, 4)
	rec := &recorder{}
	in := &discard{}
	c := NewClaude(in, pr, ClaudeOptions{Restart: func() { restarts <- len(rec.kinds("turn_ended")) }})
	done := make(chan error, 1)
	go func() { done <- c.Read(context.Background(), rec) }()
	ended := make(chan struct{}, 8)
	rec.after = func(e event) {
		if e.kind == "turn_ended" {
			ended <- struct{}{}
		}
	}
	endTurn := func() {
		t.Helper()
		if _, err := io.WriteString(pw, turn); err != nil {
			t.Fatal(err)
		}
		<-ended
	}

	// What Send writes is counted.
	if err := c.Send(context.Background(), session.Message{Ref: "r", Text: strings.Repeat("y", 1<<20)}); err != nil {
		t.Fatal(err)
	}
	if c.Written() != int64(len(in.String())) || c.Written() <= 1<<20 {
		t.Fatalf("counted %d of %d", c.Written(), len(in.String()))
	}
	// At the bound: no restart.
	c.written.Store(MaxClaudeInput)
	endTurn()
	// One byte past it: the next turn's end restarts, once.
	c.written.Add(1)
	endTurn()
	endTurn()
	pw.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	close(restarts)
	var got []int
	for n := range restarts {
		got = append(got, n)
	}
	if !slices.Equal(got, []int{1}) {
		t.Fatalf("restarts after turn ends %v, want one as the second ends, before it is told", got)
	}
	if MaxClaudeInput != 8<<20 {
		t.Fatalf("MaxClaudeInput %d", MaxClaudeInput)
	}
}

// No fixture holds an email-shaped string, an account, organisation or
// sign-in field, or a path of the machine it was recorded on.
func TestFixturesHoldNoAccount(t *testing.T) {
	names, _ := filepath.Glob(fixture("*"))
	if len(names) < 7 {
		t.Fatalf("%d fixture files", len(names))
	}
	for _, path := range names {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if found := accountIn(b, strings.HasSuffix(path, ".jsonl")); len(found) > 0 {
			t.Errorf("%s holds %q", path, found)
		}
	}
	// The check finds what it looks for.
	for _, bad := range []string{
		`{"type":"control_response","response":{"response":{"account":{"subscriptionType":"x"}}}}`,
		`{"type":"system","note":"mail someone@example.com"}`,
		`{"type":"system","apiKeySource":"none"}`,
		`{"type":"result","credential_expires_at":"2027-01-01"}`,
		`{"type":"system","cwd":"/home/someone/work"}`,
		`{"type":"system","organizationName":"x"}`,
	} {
		if len(accountIn([]byte(bad+"\n"), true)) == 0 {
			t.Errorf("not found in %s", bad)
		}
	}
}

var (
	emailShape = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// accountKeys are the fields that describe an account, its
	// organisation, its plan or its sign-in.
	accountKeys = []string{"account", "email", "emailAddress", "organization", "organizationName", "organization_uuid", "subscriptionType", "apiKeySource", "credential_expires_at", "rate_limit_info", "tokenSource"}
	machinePath = regexp.MustCompile(`/(home|Users)/[^/"\s]+|/tmp/|/private/`)
)

// accountIn is what of an account b holds: an email-shaped string, a path
// of a home folder or a temporary one, and (for JSON lines) any field
// named in accountKeys, at any depth.
func accountIn(b []byte, lines bool) []string {
	var found []string
	found = append(found, emailShape.FindAllString(string(b), -1)...)
	for _, p := range machinePath.FindAllString(string(b), -1) {
		if p != "/home/user" {
			found = append(found, p)
		}
	}
	if !lines {
		return found
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if slices.Contains(accountKeys, k) {
					found = append(found, k)
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var v any
		if json.Unmarshal(l, &v) != nil {
			found = append(found, "a line that is not JSON")
			continue
		}
		walk(v)
	}
	return found
}

// Every list the driver keeps is bounded: the messages written and not yet
// acknowledged (the oldest forgotten past maxPendingSends), the calls a
// permission_denied named, the calls followed to their results, the
// background tasks counted and the streamed texts open, each at its bound
// and one past it.
func TestClaudeListBounds(t *testing.T) {
	in := &discard{}
	var lines strings.Builder
	c := NewClaude(in, nil, ClaudeOptions{})
	for i := range maxPendingSends + 1 {
		if err := c.Send(context.Background(), session.Message{Ref: "r" + strconv.Itoa(i), Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	msgs := written(t, in)
	replayOf := func(i int) string {
		return `{"type":"user","isReplay":true,"uuid":"` + msgs[i]["uuid"].(string) + `"}` + "\n"
	}
	lines.WriteString(replayOf(0) + replayOf(1) + replayOf(maxPendingSends))
	for i := range maxDenied + 1 {
		lines.WriteString(`{"type":"system","subtype":"permission_denied","tool_use_id":"d` + strconv.Itoa(i) + `"}` + "\n")
	}
	for i := range maxOpenTools + 1 {
		lines.WriteString(`{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"d` + strconv.Itoa(i) + `","name":"Bash","input":{}}]},"parent_tool_use_id":null}` + "\n")
	}
	for _, id := range []string{"d0", "d1"} {
		lines.WriteString(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":true,"content":"no"}]},"parent_tool_use_id":null}` + "\n")
	}
	for i := range maxBackground + 1 {
		lines.WriteString(`{"type":"system","subtype":"task_started","task_id":"b` + strconv.Itoa(i) + `"}` + "\n")
	}
	lines.WriteString(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"s"}},"parent_tool_use_id":null}` + "\n")
	for i := range maxStreamed + 1 {
		lines.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","index":` + strconv.Itoa(i) + `,"delta":{"type":"text_delta","text":"w"}},"parent_tool_use_id":null}` + "\n")
	}
	c.out = strings.NewReader(lines.String())
	rec := &recorder{}
	if err := c.Read(context.Background(), rec); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var accepted []string
	for _, e := range rec.kinds("accepted") {
		accepted = append(accepted, e.ref)
	}
	if want := []string{"r1", "r" + strconv.Itoa(maxPendingSends)}; !slices.Equal(accepted, want) {
		t.Errorf("accepted %q, want %q (the oldest forgotten)", accepted, want)
	}
	ended := rec.kinds("tool_ended")
	if len(ended) != 2 || ended[0].res.Status != session.ToolError || ended[1].res.Status != session.ToolDenied {
		t.Errorf("results %+v: d0's denial should be forgotten, d1's kept", ended)
	}
	if len(c.r.denied) > maxDenied || len(c.r.deniedQ) > maxDenied {
		t.Errorf("%d denials kept", len(c.r.denied))
	}
	if len(c.r.tools) > maxOpenTools || len(c.r.toolSeq) > maxOpenTools {
		t.Errorf("%d calls followed", len(c.r.tools))
	}
	if len(c.r.bg) != maxBackground {
		t.Errorf("%d background tasks counted", len(c.r.bg))
	}
	if n := len(rec.kinds("text")); n != maxStreamed || len(c.r.streamed) != maxStreamed {
		t.Errorf("%d texts streamed, %d open", n, len(c.r.streamed))
	}
}
