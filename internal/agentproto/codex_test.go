package agentproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// fixtureThread is the thread the Codex fixtures are of.
const fixtureThread = "019a0000-0000-7000-8000-000000000001"

func codexFixture(name string) string { return filepath.Join("testdata", "codex", name) }

// codexReplay feeds a Codex fixture's lines to a driver's Read, as if the
// driver had written initialize (id 1), thread/resume (id 2) and the turn's
// message (id 3, ref m1) on the fixture's thread; answer, when set, is the
// session's verdict on each approval request, given as it is handed over.
// It returns what the session was told, what the driver wrote, and how Read
// ended.
func codexReplay(t *testing.T, name string, answer func(session.PermissionRequest) session.Verdict) (*recorder, *discard, error) {
	t.Helper()
	f, err := os.Open(codexFixture(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &discard{}
	c := NewCodex(w, f, CodexOptions{Resume: CodexResume{Thread: fixtureThread}})
	c.thread = fixtureThread
	for id, call := range map[string]*codexCall{
		"1": {method: "initialize"}, "2": {method: "thread/resume"}, "3": {method: "turn/start", ref: "m1"},
	} {
		call.answer = make(chan codexAnswer, 1)
		c.calls[id] = call
	}
	c.turning = true
	rec := &recorder{}
	if answer != nil {
		rec.after = func(e event) {
			if e.kind == "permission" {
				if err := c.Reply(context.Background(), e.ref, answer(e.perm)); err != nil {
					t.Errorf("Reply: %v", err)
				}
			}
		}
	}
	err = c.Read(context.Background(), rec)
	return rec, w, err
}

// fixtureVerdict allows a file change and denies anything else.
func fixtureVerdict(r session.PermissionRequest) session.Verdict {
	return session.Verdict{Allow: r.Tool == "fileChange"}
}

// Each hand-built fixture fed to Read gives its expected events (a turn's
// words streamed and closed, a command with its exit and output, a file
// change asked about with its diffs and counts, a declined command, an
// interrupted turn, an MCP tool's call, another thread's items and
// unknown methods ignored, the requests a chat does not ask declined, a
// turn's usage counted from the one before, a limit), and the answers the
// driver wrote.
func TestCodexFixturesReplay(t *testing.T) {
	for _, name := range []string{"hand-built-command", "hand-built-file-change", "hand-built-declined-and-interrupt", "hand-built-mcp-and-others", "hand-built-limit"} {
		t.Run(name, func(t *testing.T) {
			rec, w, err := codexReplay(t, name+".jsonl", fixtureVerdict)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Read: %v", err)
			}
			got := rec.render() + "--- written\n" + w.String()
			path := codexFixture(name + ".events")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("events\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// codexSurface is the extract of the stable and experimental schemas of
// CodexMinVersion the driver was written to (testdata/codex/README.md).
type codexSurface struct {
	Version string `json:"version"`
	Stable  struct {
		ClientRequests      map[string][]string `json:"clientRequests"`
		ClientNotifications []string            `json:"clientNotifications"`
		ClientInfo          []string            `json:"clientInfo"`
		TextUserInput       []string            `json:"textUserInput"`
	} `json:"stable"`
	Answers map[string]map[string][]string `json:"answers"`
}

func loadSurface(t *testing.T) codexSurface {
	t.Helper()
	b, err := os.ReadFile(codexFixture("surface-" + CodexMinVersion + ".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s codexSurface
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Version != CodexMinVersion {
		t.Fatalf("the surface is of %s, the driver of %s", s.Version, CodexMinVersion)
	}
	return s
}

// stableLine says what is wrong with one line the driver wrote, against the
// stable schema: a method or a field it does not name, the experimental API
// declared, an answer it does not take, or one the first cut never gives.
func (s codexSurface) stableLine(line string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &m) != nil {
		return "not an object"
	}
	var method string
	_ = json.Unmarshal(m["method"], &method)
	params := object(m["params"])
	switch {
	case method != "":
		fields, request := s.Stable.ClientRequests[method]
		if !request {
			if !slices.Contains(s.Stable.ClientNotifications, method) {
				return "the method " + method + " is not of the stable surface"
			}
			if len(params) > 0 {
				return "the notification " + method + " carries params"
			}
			return ""
		}
		for k := range params {
			if !slices.Contains(fields, k) {
				return method + "'s field " + k + " is not of the stable surface"
			}
		}
		switch method {
		case "initialize":
			if _, ok := params["capabilities"]; ok {
				return "initialize declares capabilities"
			}
			for k := range object(params["clientInfo"]) {
				if !slices.Contains(s.Stable.ClientInfo, k) {
					return "clientInfo's field " + k + " is not of the stable surface"
				}
			}
		case "turn/start":
			var input []map[string]json.RawMessage
			_ = json.Unmarshal(params["input"], &input)
			for _, in := range input {
				var kind string
				_ = json.Unmarshal(in["type"], &kind)
				if kind != "text" {
					return "an input item of type " + kind
				}
				for k := range in {
					if !slices.Contains(s.Stable.TextUserInput, k) {
						return "a text input's field " + k + " is not of the stable surface"
					}
				}
			}
		}
	case m["error"] != nil:
		var e struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(m["error"], &e) != nil || e.Code != -32601 {
			return "an error answer other than method-not-found"
		}
	case m["result"] != nil:
		r := object(m["result"])
		for k, v := range r {
			var value string
			_ = json.Unmarshal(v, &value)
			ok := false
			for _, ans := range s.Answers {
				if slices.Contains(ans[k], value) {
					ok = true
				}
			}
			if !ok {
				return "an answer " + k + "=" + value + " the schema does not take"
			}
			if value != "accept" && value != "decline" {
				return "an answer " + k + "=" + value + " the first cut never gives"
			}
		}
	default:
		return "neither a request, a notification nor an answer"
	}
	return ""
}

// codexServer is a fake app server on a driver's pipes: it answers the
// driver's requests as the stable schema shapes them, and a test writes
// what the server says besides.
type codexServer struct {
	t   *testing.T
	out *io.PipeWriter
	mu  sync.Mutex
	// lines is every line the driver wrote; got wakes a wait for one.
	lines []string
	got   chan struct{}
	// init is the initialize answer's result; resume refuses thread/resume
	// when "refuse", and noAnswer leaves initialize unanswered.
	init     string
	resume   string
	noAnswer bool
	turns    int
	// wmu holds a write against the cleanup, closed says the test ended.
	wmu    sync.Mutex
	closed bool
	// queue holds the fake's answers for its writer; done ends both.
	queue chan string
	done  chan struct{}
}

// newCodexServer is a driver on a fake app server, its Read running into
// rec.
func newCodexServer(t *testing.T, rec *recorder, opts CodexOptions) (*Codex, *codexServer, chan error) {
	t.Helper()
	read := make(chan error, 1)
	// Registered first, run last: once codexPipes' cleanup has ended the
	// pipes, Read has returned.
	t.Cleanup(func() { <-read })
	c, s := codexPipes(t, opts)
	go func() { read <- c.Read(context.Background(), rec) }()
	return c, s, read
}

// codexPipes is a driver on a fake app server, its Read left to the caller
// (a session's). The cleanup ends the driver's input and the server's
// output, so that a Read running ends.
func codexPipes(t *testing.T, opts CodexOptions) (*Codex, *codexServer) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &codexServer{t: t, out: outW, got: make(chan struct{}, 1024), queue: make(chan string, 1024), done: make(chan struct{}),
		init: `{"userAgent":"codex_cli_rs/` + CodexMinVersion + ` (test)","codexHome":"/codex-home","platformFamily":"unix","platformOs":"linux"}`}
	if opts.Resume.Thread == "" {
		opts.Resume.Thread = fixtureThread
	}
	c := NewCodex(inW, outR, opts)
	go s.serve(inR)
	go s.writer()
	t.Cleanup(func() {
		_ = c.CloseInput()
		close(s.done)
		s.wmu.Lock()
		s.closed = true
		_ = outW.Close()
		s.wmu.Unlock()
	})
	return c, s
}

// answer queues v for the fake's writer: the fake keeps reading what the
// driver writes while its answers wait to be read, as a real server does.
// Answering from its read loop, it waited for the driver's Read, which
// waited for it to read a line the driver wrote from Read's own goroutine
// (an MCP server's form declined).
func (s *codexServer) answer(v string) {
	select {
	case s.queue <- v:
	case <-s.done:
	}
}

// writer writes the queued answers in order until the test ends.
func (s *codexServer) writer() {
	for {
		select {
		case v := <-s.queue:
			s.write(v)
		case <-s.done:
			return
		}
	}
}

// write writes v as one line, unless the test has ended: its writes are
// held against the cleanup's close by wmu, so that an answer the fake was
// still giving as the test ended is dropped, never written to a closed
// pipe.
func (s *codexServer) write(v string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return
	}
	if _, err := io.WriteString(s.out, v+"\n"); err != nil {
		s.t.Errorf("the fake server's write: %v", err)
	}
}

func (s *codexServer) serve(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		s.mu.Lock()
		s.lines = append(s.lines, line)
		s.mu.Unlock()
		s.got <- struct{}{}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(line), &m)
		if m.ID == nil || m.Method == "" {
			continue
		}
		id := string(m.ID)
		switch m.Method {
		case "initialize":
			if !s.noAnswer {
				s.answer(`{"id":` + id + `,"result":` + s.init + `}`)
			}
		case "thread/resume":
			if s.resume == "refuse" {
				s.answer(`{"id":` + id + `,"error":{"code":-32600,"message":"no rollout found for thread id"}}`)
				continue
			}
			s.answer(`{"id":` + id + `,"result":{"thread":{"id":"` + fixtureThread + `","status":{"type":"idle"},"turns":[]},"model":"gpt-test","cwd":"/work"}}`)
		case "turn/start":
			s.mu.Lock()
			s.turns++
			turn := "turn-test-" + string(rune('0'+s.turns))
			s.mu.Unlock()
			s.answer(`{"id":` + id + `,"result":{"turn":{"id":"` + turn + `","items":[],"status":"inProgress"}}}`)
			s.answer(`{"method":"turn/started","params":{"threadId":"` + fixtureThread + `","turn":{"id":"` + turn + `","items":[],"status":"inProgress"}}}`)
		case "turn/interrupt":
			s.answer(`{"id":` + id + `,"result":{}}`)
			s.answer(`{"method":"turn/completed","params":{"threadId":"` + fixtureThread + `","turn":{"id":"x","items":[],"status":"interrupted"}}}`)
		}
	}
}

// written is every line the driver has written so far.
func (s *codexServer) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

// await waits for a line the driver wrote that holds want, and returns it.
func (s *codexServer) await(want string) string {
	s.t.Helper()
	deadline := time.After(time.Until(testDeadline(s.t)))
	for {
		for _, l := range s.written() {
			if strings.Contains(l, want) {
				return l
			}
		}
		select {
		case <-s.got:
		case <-deadline:
			s.t.Fatalf("the driver wrote no %q; it wrote\n%s", want, strings.Join(s.written(), "\n"))
		}
	}
}

// waitEvent waits for the recorder to hold an event of kind, and returns
// them all.
func waitEvent(t *testing.T, rec *recorder, kind string, n int) []event {
	t.Helper()
	deadline := testDeadline(t)
	for {
		if evs := rec.kinds(kind); len(evs) >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %d %s events in\n%s", n, kind, rec.render())
		}
		time.Sleep(time.Millisecond)
	}
}

// The driver speaks the stable surface alone: every line it writes across a
// start, two messages, an interrupt, approvals answered both ways and the
// requests a chat declines names a method and fields of the stable schema of
// CodexMinVersion, never declares the experimental API (no capabilities at
// all), and answers an approval accept or decline alone. The check itself
// breaks on an experimental field, a declared experimental API and an answer
// for the session.
func TestCodexStableSurfaceOnly(t *testing.T) {
	surf := loadSurface(t)
	for _, bad := range []string{
		`{"id":1,"method":"thread/resume","params":{"threadId":"x","history":[]}}`,
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"conductor","version":"1"},"capabilities":{"experimentalApi":true}}}`,
		`{"id":1,"method":"thread/memory/read","params":{}}`,
		`{"id":1,"result":{"decision":"acceptForSession"}}`,
		`{"id":1,"result":{"decision":"cancel"}}`,
		`{"id":1,"method":"turn/start","params":{"threadId":"x","input":[{"type":"mention","name":"a","path":"b"}]}}`,
	} {
		if surf.stableLine(bad) == "" {
			t.Fatalf("the check takes %s", bad)
		}
	}
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{Resume: CodexResume{Thread: fixtureThread, Model: "gpt-test", ApprovalPolicy: "never", Sandbox: "danger-full-access", Cwd: "/work"}})
	rec.after = func(e event) {
		if e.kind == "permission" {
			// On a goroutine of its own, as the session replies: a reply
			// written from Read's own goroutine waits for the fake to read
			// it while the fake waits for Read to take what it writes.
			go func() { _ = c.Reply(context.Background(), e.ref, session.Verdict{Allow: e.perm.Call == "call-1"}) }()
		}
	}
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, session.Message{Ref: "r1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, rec, "accepted", 1)
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"commandExecution","id":"call-1","command":"ls","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
	s.write(`{"id":40,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-1","command":"ls","cwd":"/work","startedAtMs":1}}`)
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"fileChange","id":"call-2","changes":[{"path":"a.txt","kind":{"type":"add"},"diff":"x\n"}],"status":"inProgress"}}}`)
	s.write(`{"id":"41","method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-2","startedAtMs":1}}`)
	s.write(`{"id":42,"method":"mcpServer/elicitation/request","params":{"threadId":"` + fixtureThread + `","serverName":"x","mode":"form","message":"?","requestedSchema":{}}}`)
	s.write(`{"id":43,"method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`)
	s.write(`{"id":44,"method":"item/tool/requestUserInput","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"q","questions":[]}}`)
	s.await(`"id":40,"result"`)
	s.await(`"id":"41","result"`)
	s.await(`"id":42,"result"`)
	s.await(`"id":43}`)
	s.await(`"id":44}`)
	if err := c.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, rec, "turn_ended", 1)
	if err := c.Send(ctx, session.Message{Ref: "r2", Text: "/help @notes.txt"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, rec, "accepted", 2)
	lines := s.written()
	for _, want := range []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/resume"`, `"method":"turn/start"`, `"method":"turn/interrupt"`, `"decision":"accept"`, `"decision":"decline"`, `"action":"decline"`} {
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
			t.Fatalf("no line holds %s:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	for _, l := range lines {
		if why := surf.stableLine(l); why != "" {
			t.Errorf("%s: %s", why, l)
		}
	}
}

// An approval request is the session's question: handed over with the
// step it names (its item), the tool, the input the step was announced with
// and the folder the request names; the reply carries the verdict for that
// request's own id alone (a number or a string, as the server gave it),
// once, and a second request's reply is its own.
func TestCodexApprovalThroughThePermissionPath(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"commandExecution","id":"call-1","command":"make test","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
	s.write(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-1","cwd":"/work/sub","startedAtMs":1}}`)
	s.write(`{"id":"seven","method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-1","command":"make lint","cwd":"/work","startedAtMs":1}}`)
	perms := waitEvent(t, rec, "permission", 2)
	tools := rec.kinds("tool_started")
	if len(tools) != 1 || tools[0].ref != "call-1" || tools[0].tool.Name != "commandExecution" {
		t.Fatalf("the step %+v", tools)
	}
	a, b := perms[0].perm, perms[1].perm
	if a.Call != "call-1" || a.Tool != "commandExecution" || a.Cwd != "/work/sub" || string(a.Input) != `{"command":"make test","cwd":"/work/sub"}` {
		t.Fatalf("the first request %+v %s", a, a.Input)
	}
	if b.Call != "call-1" || string(b.Input) != `{"command":"make lint","cwd":"/work"}` || a.Ref == b.Ref {
		t.Fatalf("the second request %+v %s", b, b.Input)
	}
	if err := c.Reply(ctx, b.Ref, session.Verdict{Message: "no"}); err != nil {
		t.Fatal(err)
	}
	if got := s.await(`"id":"seven"`); got != `{"id":"seven","result":{"decision":"decline"}}` {
		t.Fatalf("the second's answer %s", got)
	}
	if err := c.Reply(ctx, a.Ref, session.Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.await(`"id":7,`); got != `{"id":7,"result":{"decision":"accept"}}` {
		t.Fatalf("the first's answer %s", got)
	}
	// Each once: a second reply writes nothing.
	n := len(s.written())
	_ = c.Reply(ctx, a.Ref, session.Verdict{Allow: true})
	_ = c.Reply(ctx, "codex-req-404", session.Verdict{Allow: true})
	// The driver's writes go one at a time down one pipe: a line written
	// after the replies, once read, says whatever they wrote is read too.
	syncLine(t, c, s)
	for _, l := range s.written()[n:] {
		if strings.Contains(l, `"result"`) {
			t.Fatalf("a request answered twice: %s", l)
		}
	}
	// A file change is handed over with the changes it was announced with,
	// and the root it asks to write under beside them; one about a change
	// never shown is handed over with no changes, for the session to deny
	// (it names no call the stream announced); a request of another thread
	// is declined at once, never handed over.
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"fileChange","id":"call-2","changes":[{"path":"a.txt","kind":{"type":"update","move_path":null},"diff":"-a\n+b\n"}],"status":"inProgress"}}}`)
	s.write(`{"id":8,"method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-2","grantRoot":"/work","startedAtMs":1}}`)
	s.write(`{"id":9,"method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-never","startedAtMs":1}}`)
	s.write(`{"id":10,"method":"item/commandExecution/requestApproval","params":{"threadId":"019a0000-0000-7000-8000-0000000000ff","turnId":"t","itemId":"call-1","command":"rm -rf /","startedAtMs":1}}`)
	perms = waitEvent(t, rec, "permission", 4)
	if f := perms[2].perm; f.Tool != "fileChange" || f.Call != "call-2" || string(f.Input) != `{"changes":[{"diff":"-a\n+b\n","kind":{"type":"update"},"path":"a.txt"}],"grantRoot":"/work"}` {
		t.Fatalf("the file change's request %+v %s", f, f.Input)
	}
	if f := perms[3].perm; f.Tool != "fileChange" || f.Call != "call-never" || string(f.Input) != `{"changes":[]}` {
		t.Fatalf("a change never shown %+v %s", f, f.Input)
	}
	if got := s.await(`"id":10,`); got != `{"id":10,"result":{"decision":"decline"}}` {
		t.Fatalf("another thread's: %s", got)
	}
	if len(rec.kinds("permission")) != 4 || len(rec.kinds("tool_started")) != 2 {
		t.Fatalf("another thread's request was handed over, or a step announced by a request:\n%s", rec.render())
	}
	// A withdrawal the session makes (the turn's end) is a decline, never
	// Codex's cancel, which would stop the turn too.
	_ = c.Reply(ctx, perms[2].ref, session.Verdict{Cancel: true, Message: session.AnsweredByAgentWords})
	if got := s.await(`"id":8,`); got != `{"id":8,"result":{"decision":"decline"}}` {
		t.Fatalf("a withdrawal: %s", got)
	}
}

// The server's serverRequest/resolved for a request still open is the
// agent taking it back (PermissionWithdrawn), and the session's later reply
// writes nothing; for a request already answered it is only the
// confirmation, and withdraws nothing.
func TestCodexResolvedWithdraws(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"commandExecution","id":"call-1","command":"ls","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
	s.write(`{"id":5,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-1","startedAtMs":1}}`)
	s.write(`{"id":6,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-1","startedAtMs":1}}`)
	perms := waitEvent(t, rec, "permission", 2)
	if err := c.Reply(ctx, perms[1].ref, session.Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	s.await(`"id":6,"result"`)
	s.write(`{"method":"serverRequest/resolved","params":{"threadId":"` + fixtureThread + `","requestId":6}}`)
	s.write(`{"method":"serverRequest/resolved","params":{"threadId":"` + fixtureThread + `","requestId":5}}`)
	w := waitEvent(t, rec, "permission_withdrawn", 1)
	if len(w) != 1 || w[0].ref != perms[0].ref {
		t.Fatalf("withdrawn %+v, want %s alone", w, perms[0].ref)
	}
	n := len(s.written())
	if err := c.Reply(ctx, perms[0].ref, session.Verdict{Allow: true}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("a verdict on a resolved request: %v, want ErrNoRequest", err)
	}
	syncLine(t, c, s)
	for _, l := range s.written()[n:] {
		if strings.Contains(l, `"result"`) {
			t.Fatalf("a resolved request was answered: %s", l)
		}
	}
}

// syncLine has the driver write a line of its own and waits for the fake to
// have read it: every line written before it has been read too.
func syncLine(t *testing.T, c *Codex, s *codexServer) {
	t.Helper()
	if _, _, err := c.request("thread/loaded/list", nil, ""); err != nil {
		t.Fatal(err)
	}
	s.await(`"thread/loaded/list"`)
}

// Codex's Verbatim is its declaration (codexVerbatim): false until a
// by-hand reading of what the app server does with a message that starts
// with / or holds @path, so the chat's box draws "Messages starting with /
// may run as the agent's commands." and never "Sent as written". A sent
// /-leading message, and one holding @path, reach the server as one text
// input with the text unchanged: no mention, skill or text_elements added.
func TestCodexVerbatimIsDeclared(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	if c.Verbatim() != codexVerbatim || codexVerbatim {
		t.Fatalf("Verbatim %v, declared %v: a chat of Codex claims no as-written mark until it is read by hand", c.Verbatim(), codexVerbatim)
	}
	ctx := context.Background()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"/help", "read @/data/conductor/x and say <b>hi</b>"} {
		ref := "r" + string(rune('1'+i))
		if err := c.Send(ctx, session.Message{Ref: ref, Text: text}); err != nil {
			t.Fatal(err)
		}
		waitEvent(t, rec, "accepted", i+1)
		var sent struct {
			Params struct {
				Input []map[string]any `json:"input"`
			} `json:"params"`
		}
		lines := s.written()
		var line string
		for _, l := range lines {
			if strings.Contains(l, `"turn/start"`) {
				line = l
			}
		}
		if err := json.Unmarshal([]byte(line), &sent); err != nil {
			t.Fatal(err)
		}
		if len(sent.Params.Input) != 1 || len(sent.Params.Input[0]) != 2 || sent.Params.Input[0]["type"] != "text" || sent.Params.Input[0]["text"] != text {
			t.Fatalf("sent %s", line)
		}
		if !strings.Contains(line, `<b>hi</b>`) && i == 1 {
			t.Fatalf("HTML characters escaped: %s", line)
		}
	}
}

// The gate: a version below CodexMinVersion in the initialize answer is
// ErrCodexNotQualified; an answer without a userAgent, no answer (well
// inside the session's bound, the case's own InitWait), and a thread the
// server will not resume are ErrCodexNotStarted; a user agent that names no version passes (the probe
// checked it); the probed version is qualified at CodexMinVersion and not
// below it. A first turn that says the agent is not signed in ends Read with
// ErrCodexNotSignedIn, which goes back to the terminal.
func TestCodexGate(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*codexServer)
		want error
	}{
		{"an older Codex", func(s *codexServer) { s.init = `{"userAgent":"codex_cli_rs/0.160.0 (x)"}` }, ErrCodexNotQualified},
		{"no user agent", func(s *codexServer) { s.init = `{"platformOs":"linux"}` }, ErrCodexNotStarted},
		{"an answer that is no object", func(s *codexServer) { s.init = `"hello"` }, ErrCodexNotStarted},
		{"no answer", func(s *codexServer) { s.noAnswer = true }, ErrCodexNotStarted},
		{"a thread not resumed", func(s *codexServer) { s.resume = "refuse" }, ErrCodexNotStarted},
		{"a user agent with no version", func(s *codexServer) { s.init = `{"userAgent":"codex"}` }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{}
			opts := CodexOptions{}
			if c.name == "no answer" {
				// The client's own bound is what this case is about; the
				// others wait by the product's, which only bounds a hang.
				opts.InitWait = 300 * time.Millisecond
			}
			d, s, _ := newCodexServer(t, rec, opts)
			c.edit(s)
			start := time.Now()
			err := d.Begin(context.Background())
			if c.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("Begin: %v, want %v", err, c.want)
			}
			if c.name == "no answer" && time.Since(start) > 30*time.Second {
				t.Fatalf("Begin took %v", time.Since(start))
			}
			if StartWords(err) == "" {
				t.Fatalf("no sentence for %v", err)
			}
		})
	}
	for v, ok := range map[string]bool{CodexMinVersion: true, "0.167.1": false, "0.168.0": true, "": false, "1.0.0": true} {
		if Qualified(CodexAppServer, v) != ok {
			t.Errorf("Qualified(%q) = %v", v, !ok)
		}
	}
	rec, _, err := codexReplay(t, "hand-built-not-signed-in.jsonl", nil)
	if !errors.Is(err, ErrCodexNotSignedIn) || !IsNotSignedIn(err) {
		t.Fatalf("Read: %v", err)
	}
	var back interface{ BackToTerminal() bool }
	if !errors.As(err, &back) || !back.BackToTerminal() {
		t.Fatal("a signed-out Codex does not go back to its terminal")
	}
	if n := rec.kinds("notice"); len(n) != 1 || n[0].code != NoticeNotSignedIn || n[0].text != CodexNotSignedInWords {
		t.Fatalf("notices %+v", n)
	}
	if len(rec.kinds("turn_ended")) != 0 {
		t.Fatal("a signed-out start's turn was told")
	}
	if c := rec.kinds("conversation"); len(c) != 1 || c[0].ref != fixtureThread {
		t.Fatalf("the conversation %+v", c)
	}
}

// What the stream holds that the fixtures lack changes nothing: an unknown
// method, an unknown item type, a field of another type; a line past
// session.MaxProtocolLine is skipped to its end with a notice, the step
// whose completion it was ended cut, and the read goes on; a line that is
// not a JSON object ends Read with ErrCodexUnreadable.
func TestCodexUnknownEventsAndLineBound(t *testing.T) {
	var in bytes.Buffer
	in.WriteString(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"commandExecution","id":"call-1","command":"cat big","cwd":"/work","status":"inProgress","commandActions":[]}}}` + "\n")
	in.WriteString(`{"method":"future/thing","params":{"threadId":"` + fixtureThread + `"}}` + "\n")
	in.WriteString(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","item":{"type":"futureItem","id":"x"}}}` + "\n")
	in.WriteString(`{"method":"item/agentMessage/delta","params":{"threadId":"` + fixtureThread + `","itemId":7,"delta":"x"}}` + "\n")
	in.WriteString(`{"method":"item/completed","params":{"threadId":"` + fixtureThread + `","item":{"type":"commandExecution","id":"call-1","status":"completed","aggregatedOutput":"` + strings.Repeat("a", session.MaxProtocolLine) + `"}}}` + "\n")
	in.WriteString(`{"method":"turn/completed","params":{"threadId":"` + fixtureThread + `","turn":{"id":"t","status":"completed","items":[]}}}` + "\n")
	in.WriteString("not json\n")
	c := NewCodex(&discard{}, &in, CodexOptions{})
	c.thread = fixtureThread
	rec := &recorder{}
	err := c.Read(context.Background(), rec)
	if !errors.Is(err, ErrCodexUnreadable) {
		t.Fatalf("Read: %v", err)
	}
	got := rec.render()
	want := "tool_started call-1 parent=\"\" commandExecution summary=\"cat big\" input={\"command\":\"cat big\",\"cwd\":\"/work\"}\n" +
		"notice too_large \"A step's output was too large to show.\"\n" +
		"tool_ended call-1 ok exit=none cut=true output=\"\"\n" +
		"turn_ended done in=0 out=0 cost=0.00000000\n"
	if got != want {
		t.Fatalf("events\n%s\nwant\n%s", got, want)
	}
}

// The argv of a chat's child of Codex, element for element: the command,
// the app server on stdio, the protocol's arguments, the terminal's
// configuration overrides and Conductor's MCP server; the model, the
// approval policy and the sandbox the terminal chose go to the thread's
// resume, a yolo launch's never asking and no sandbox; an argument a chat
// cannot carry is refused, by name.
func TestCodexArgvIsTheDocumentedOne(t *testing.T) {
	conv := fixtureThread
	mcp := []string{"-c", `mcp_servers.conductor.command="/opt/conductor"`}
	cases := []struct {
		name string
		l    CodexLaunch
		argv []string
		res  CodexResume
	}{
		{"a chat", CodexLaunch{Command: []string{"codex"}, Conversation: conv, Model: []string{"--model=o3"}, LaunchArgs: []string{"-c", "model_reasoning_effort=high", "--no-alt-screen"}, MCP: mcp, Dir: "/work"},
			[]string{"codex", "app-server", "--listen", "stdio://", "-c", "model_reasoning_effort=high", "-c", `mcp_servers.conductor.command="/opt/conductor"`},
			CodexResume{Thread: conv, Model: "o3", Cwd: "/work"}},
		{"a yolo session", CodexLaunch{Command: []string{"/opt/codex"}, Protocol: []string{"--enable", "x"}, Conversation: conv, Yolo: true, YoloArgs: []string{"--dangerously-bypass-approvals-and-sandbox"}, Dir: "/work"},
			[]string{"/opt/codex", "app-server", "--listen", "stdio://", "--enable", "x"},
			CodexResume{Thread: conv, ApprovalPolicy: "never", Sandbox: "danger-full-access", Cwd: "/work"}},
		{"its own choices", CodexLaunch{Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"-s", "read-only", "--ask-for-approval=untrusted", "-mgpt-5", "--config=x=1", "--disable", "y", "--strict-config"}},
			[]string{"codex", "app-server", "--listen", "stdio://", "--config", "x=1", "--disable", "y", "--strict-config"},
			CodexResume{Thread: conv, Model: "gpt-5", ApprovalPolicy: "untrusted", Sandbox: "read-only"}},
		{"a yolo recipe left out of a launch that is not yolo", CodexLaunch{Command: []string{"codex"}, Conversation: conv, YoloArgs: []string{"--dangerously-bypass-approvals-and-sandbox"}},
			[]string{"codex", "app-server", "--listen", "stdio://"}, CodexResume{Thread: conv}},
	}
	for _, c := range cases {
		argv, res, err := CodexArgs(c.l)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(argv, c.argv) || res != c.res {
			t.Errorf("%s:\n got %q %+v\nwant %q %+v", c.name, argv, res, c.argv, c.res)
		}
		if n := count(argv, "app-server"); n != 1 {
			t.Errorf("%s: app-server %d times", c.name, n)
		}
	}
	for flag, l := range map[string]CodexLaunch{
		"--search":   {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"--search"}},
		"--profile":  {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"--profile", "work"}},
		"-C":         {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"-C", "/srv"}},
		"a prompt":   {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"fix the bug"}},
		"--sandbox":  {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"--sandbox", "everything"}},
		"-a":         {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"-a"}},
		"--listen":   {Command: []string{"codex"}, Conversation: conv, Protocol: []string{"--listen", "ws://0.0.0.0:1"}},
		"--remote":   {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"--remote", "ws://x"}},
		"-c's value": {Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"-c"}},
	} {
		_, _, err := CodexArgs(l)
		var arg *CodexArgError
		if !errors.As(err, &arg) {
			t.Errorf("%s: %v", flag, err)
		}
	}
	if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: conv, LaunchArgs: []string{"--add-dir", "/srv"}}); !errors.Is(err, ErrAddedFolder) {
		t.Errorf("an added folder: %v", err)
	}
	for _, bad := range []CodexLaunch{{Conversation: conv}, {Command: []string{"codex"}}, {Command: []string{"codex"}, Conversation: "--last"}} {
		if argv, _, err := CodexArgs(bad); err == nil {
			t.Errorf("%+v made %q", bad, argv)
		}
	}
	if w := CodexArgWords("--search"); !strings.Contains(w, "--search") || !strings.HasSuffix(w, "it stays in its terminal.") {
		t.Errorf("the words %q", w)
	}
}

// No Codex fixture holds an account, an email-shaped string or a path of a
// person's machine (accountIn, TestFixturesHoldNoAccount's check), nor a
// token the server could ask for.
func TestCodexFixturesHoldNoAccount(t *testing.T) {
	var files []string
	for _, pattern := range []string{"*.jsonl", "*.events"} {
		f, _ := filepath.Glob(codexFixture(pattern))
		files = append(files, f...)
	}
	if len(files) < 11 {
		t.Fatalf("%d fixture files", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if found := accountIn(b, strings.HasSuffix(f, ".jsonl")); len(found) > 0 {
			t.Errorf("%s holds %q", f, found)
		}
		for _, bad := range []string{`"accessToken"`, `"chatgptAccountId"`, `"planType"`} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s holds %s", f, bad)
			}
		}
	}
}

// A file change that moves a file is shown, bound and recorded with its
// destination: the step's input and the request's carry the kind's
// move_path, and the change is recorded at its destination with the file it
// left deleted.
func TestCodexAMoveIsShownWithItsDestination(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := `{"type":"fileChange","id":"call-m","changes":[{"path":"/work/notes.txt","kind":{"type":"update","move_path":"/work/moved.txt"},"diff":"-a\n+b\n"}],"status":"inProgress"}`
	s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":` + item + `}}`)
	s.write(`{"id":30,"method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"call-m","startedAtMs":1}}`)
	p := waitEvent(t, rec, "permission", 1)[0].perm
	want := `{"changes":[{"diff":"-a\n+b\n","kind":{"move_path":"/work/moved.txt","type":"update"},"path":"/work/notes.txt"}]}`
	if string(p.Input) != want || string(rec.kinds("tool_started")[0].tool.Input) != want {
		t.Fatalf("the move's input %s, its step's %s", p.Input, rec.kinds("tool_started")[0].tool.Input)
	}
	s.write(`{"method":"item/completed","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":` + strings.Replace(item, `"inProgress"`, `"completed"`, 1) + `}}`)
	files := waitEvent(t, rec, "file_changed", 2)
	if files[0].file.Path != "/work/moved.txt" || files[0].file.Op != session.ChangeEdit || files[1].file.Path != "/work/notes.txt" || files[1].file.Op != session.ChangeDelete {
		t.Fatalf("recorded %+v %+v", files[0].file, files[1].file)
	}
}

// What the driver does not read is declined, never asked without it: an
// approval about an item holding a field beyond the ones the driver reads
// (in the item, a change, or a change's kind, or a kind of change it does
// not know); a command approval asking for more than its command (a network
// context, added permissions, a sub-approval's id, a reason, another
// environment, another kind, a field it does not read); a file change's
// asking for a reason; one naming no item; and a file change's whose item
// announced no changes. Each is answered decline at once, with a notice,
// and none reaches the session.
func TestCodexWhatTheDriverDoesNotReadIsDeclined(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := func(item string) {
		s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":` + item + `}}`)
	}
	cmd := func(id, extra string) string {
		return `{"type":"commandExecution","id":"` + id + `","command":"ls","cwd":"/work","status":"inProgress","commandActions":[]` + extra + `}`
	}
	change := func(id, ch string) string {
		return `{"type":"fileChange","id":"` + id + `","changes":[` + ch + `],"status":"inProgress"}`
	}
	ask := func(n int, method, item, extra string) {
		s.write(`{"id":` + itoa(100+n) + `,"method":"item/` + method + `/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"` + item + `","startedAtMs":1` + extra + `}}`)
	}
	started(cmd("i1", `,"newField":{"x":1}`))
	started(change("i2", `{"path":"a","kind":{"type":"update","newKindField":"x"},"diff":""}`))
	started(change("i3", `{"path":"a","kind":{"type":"update"},"diff":"","newChangeField":1}`))
	started(change("i4", `{"path":"a","kind":{"type":"rename"},"diff":""}`))
	started(`{"type":"fileChange","id":"i5","changes":[],"status":"inProgress"}`)
	started(cmd("i6", ""))
	started(change("i7", `{"path":"a","kind":{"type":"update"},"diff":""}`))
	cases := []struct {
		n             int
		method, item  string
		extra, reason string
	}{
		{1, "commandExecution", "i1", "", "an item field"},
		{2, "fileChange", "i2", "", "a kind's field"},
		{3, "fileChange", "i3", "", "a change's field"},
		{4, "fileChange", "i4", "", "a kind of change"},
		{5, "fileChange", "i5", "", "no changes announced"},
		{6, "commandExecution", "i6", `,"networkApprovalContext":{"host":"example.com","protocol":"https"}`, "a network context"},
		{7, "commandExecution", "i6", `,"additionalPermissions":{"network":true}`, "added permissions"},
		{8, "commandExecution", "i6", `,"approvalId":"sub-1"`, "a sub-approval"},
		{9, "commandExecution", "i6", `,"reason":"needs network access"`, "a reason"},
		{10, "commandExecution", "i6", `,"environmentId":"remote-1"`, "another environment"},
		{11, "commandExecution", "i6", `,"kind":"stdin"`, "another kind"},
		{12, "commandExecution", "i6", `,"somethingNew":true`, "a field it does not read"},
		{13, "fileChange", "i7", `,"reason":"wider write access"`, "a file change's reason"},
		{14, "commandExecution", "", "", "no item"},
	}
	for _, tc := range cases {
		ask(tc.n, tc.method, tc.item, tc.extra)
	}
	for _, tc := range cases {
		id := itoa(100 + tc.n)
		if got := s.await(`"id":` + id + `,`); got != `{"id":`+id+`,"result":{"decision":"decline"}}` {
			t.Fatalf("%s: answered %s", tc.reason, got)
		}
	}
	// Each decline's notice follows its answer: once all are told, every
	// request has been handled.
	if n := len(waitEvent(t, rec, "notice", len(cases))); n != len(cases) {
		t.Fatalf("%d notices for %d declines", n, len(cases))
	}
	if n := len(rec.kinds("permission")); n != 0 {
		t.Fatalf("%d declined requests reached the session:\n%s", n, rec.render())
	}
	// Nulls are no fields: an ordinary approval with them is asked.
	ask(15, "commandExecution", "i6", `,"approvalId":null,"reason":null,"networkApprovalContext":null,"kind":"command","proposedExecpolicyAmendment":["ls"]`)
	if p := waitEvent(t, rec, "permission", 1)[0].perm; p.Call != "i6" {
		t.Fatalf("the ordinary approval %+v", p)
	}
}

// A request named by the id of one still open closes the open one's card
// (PermissionWithdrawn) before the new one is asked, and a verdict on the
// old one reaches nobody (ErrNoRequest).
func TestCodexAReusedRequestIDClosesTheOldCard(t *testing.T) {
	rec := &recorder{}
	c, s, _ := newCodexServer(t, rec, CodexOptions{})
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		s.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"t","item":{"type":"commandExecution","id":"` + id + `","command":"ls","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
		s.write(`{"id":50,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"t","itemId":"` + id + `","startedAtMs":1}}`)
	}
	perms := waitEvent(t, rec, "permission", 2)
	w := waitEvent(t, rec, "permission_withdrawn", 1)
	if w[0].ref != perms[0].ref {
		t.Fatalf("withdrawn %s, want the first %s", w[0].ref, perms[0].ref)
	}
	if err := c.Reply(context.Background(), perms[0].ref, session.Verdict{Allow: true}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("the old card's verdict: %v", err)
	}
	if err := c.Reply(context.Background(), perms[1].ref, session.Verdict{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.await(`"id":50,`); got != `{"id":50,"result":{"decision":"accept"}}` {
		t.Fatalf("answered %s", got)
	}
}

// testDeadline is how long a test's wait for a step may run: until shortly
// before the test binary's own timeout, which bounds a hang; the steps
// themselves are waited on by what ends them. Without a timeout, ten
// minutes.
func testDeadline(t *testing.T) time.Time {
	if d, ok := t.Deadline(); ok {
		return d.Add(-5 * time.Second)
	}
	return time.Now().Add(10 * time.Minute)
}
