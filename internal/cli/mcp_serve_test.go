package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// mcpHang bounds a wait for something the code does at once: it only ends
// a test that hangs, with a message, before go test's own bound.
const mcpHang = time.Minute

// mcpClient drives one serve loop over pipes, as an agent's MCP client
// does: it writes a line at a time and reads every reply as it comes.
type mcpClient struct {
	t       *testing.T
	s       *mcpServer
	in      *io.PipeWriter
	replies chan map[string]any
	done    chan struct{}
	code    int
	err     error
	// pending is the replies read but not yet asked for.
	pending []map[string]any
	once    sync.Once
	// hung is set once the loop has failed to return: a cleanup after
	// that does not wait for it again.
	hung bool
}

// startMCP starts s's serve loop; the test's cleanup ends its stdin and
// waits for it.
func startMCP(t *testing.T, s *mcpServer) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.out = outW
	c := &mcpClient{t: t, s: s, in: inW, replies: make(chan map[string]any, 256), done: make(chan struct{})}
	read := make(chan struct{})
	go func() {
		defer close(read)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				m = map[string]any{"unreadable": sc.Text()}
			}
			c.replies <- m
		}
	}()
	go func() {
		code, err := s.serve(context.Background(), inR)
		c.code, c.err = code, err
		_ = outW.Close()
		<-read
		close(c.done)
	}()
	t.Cleanup(func() { c.end() })
	return c
}

// send writes one line; it returns once the serve loop has read it.
func (c *mcpClient) send(line string) {
	c.t.Helper()
	sent := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.in, line+"\n")
		sent <- err
	}()
	select {
	case err := <-sent:
		if err != nil {
			c.t.Fatalf("send %.80s: %v", line, err)
		}
	case <-time.After(mcpHang):
		c.hung = true
		c.t.Fatalf("the serve loop did not read %.80s", line)
	}
}

// next is the next reply not yet asked for.
func (c *mcpClient) next() map[string]any {
	c.t.Helper()
	if len(c.pending) > 0 {
		m := c.pending[0]
		c.pending = c.pending[1:]
		return m
	}
	select {
	case m := <-c.replies:
		return m
	case <-time.After(mcpHang):
		c.t.Fatal("no reply came")
		return nil
	}
}

// byID is the reply to request id, the others read meanwhile kept.
func (c *mcpClient) byID(id int) map[string]any {
	c.t.Helper()
	if i := slices.IndexFunc(c.pending, func(m map[string]any) bool { return fmt.Sprint(m["id"]) == fmt.Sprint(id) }); i >= 0 {
		m := c.pending[i]
		c.pending = slices.Delete(c.pending, i, i+1)
		return m
	}
	for {
		select {
		case m := <-c.replies:
			if fmt.Sprint(m["id"]) == fmt.Sprint(id) {
				return m
			}
			c.pending = append(c.pending, m)
		case <-time.After(mcpHang):
			c.t.Fatalf("no reply to %d came", id)
			return nil
		}
	}
}

// end closes stdin and waits for the serve loop to return.
func (c *mcpClient) end() (int, error) {
	c.t.Helper()
	c.once.Do(func() { _ = c.in.Close() })
	if c.hung {
		return -1, nil
	}
	select {
	case <-c.done:
	case <-time.After(mcpHang):
		c.hung = true
		c.t.Fatal("the serve loop did not return at the end of its stdin")
	}
	return c.code, c.err
}

// rest is every reply not yet asked for, once the loop has returned.
func (c *mcpClient) rest() []map[string]any {
	out := c.pending
	c.pending = nil
	for {
		select {
		case m := <-c.replies:
			out = append(out, m)
		default:
			return out
		}
	}
}

// noReplyTo fails if any reply read so far answers id.
func (c *mcpClient) noReplyTo(id int, got ...map[string]any) {
	c.t.Helper()
	for _, m := range append(slices.Clone(c.pending), got...) {
		if fmt.Sprint(m["id"]) == fmt.Sprint(id) {
			c.t.Fatalf("a reply to the cancelled call %d: %v", id, m)
		}
	}
}

// idle waits until no call's goroutine runs: the count the serve loop
// keeps, lowered as each one's last act.
func (c *mcpClient) idle() {
	c.t.Helper()
	deadline := time.Now().Add(mcpHang)
	for c.s.running.Load() != 0 {
		if time.Now().After(deadline) {
			c.t.Fatalf("%d calls still running", c.s.running.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// heldPOST is one permission post the fake server holds until the test
// answers it or its client leaves.
type heldPOST struct {
	auth   string
	body   map[string]any
	answer chan permReply
	// gone is closed when the request's context ended before an answer.
	gone chan struct{}
}

type permReply struct {
	status int
	body   string
	// abort ends the connection with no answer at all.
	abort bool
}

func (h *heldPOST) allow() {
	h.answer <- permReply{status: http.StatusOK, body: `{"behavior":"allow"}`}
}

// ended waits for the request's context to end before any answer.
func (h *heldPOST) ended(t *testing.T) {
	t.Helper()
	select {
	case <-h.gone:
	case <-time.After(mcpHang):
		t.Fatal("the held post's request did not end")
	}
}

// permServer is a fake server of session s1: it holds each permission
// post for the test and takes every report.
type permServer struct {
	*httptest.Server
	posts   chan *heldPOST
	quit    chan struct{}
	count   atomic.Int32
	reports atomic.Int32
}

func newPermServer(t *testing.T) *permServer {
	t.Helper()
	p := &permServer{posts: make(chan *heldPOST, 64), quit: make(chan struct{})}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/s1/permission":
			raw, _ := io.ReadAll(r.Body)
			h := &heldPOST{auth: r.Header.Get("Authorization"), answer: make(chan permReply, 1), gone: make(chan struct{})}
			_ = json.Unmarshal(raw, &h.body)
			p.count.Add(1)
			p.posts <- h
			select {
			case a := <-h.answer:
				if a.abort {
					panic(http.ErrAbortHandler)
				}
				if a.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/api/sessions/s1/elsewhere")
				}
				w.WriteHeader(a.status)
				_, _ = io.WriteString(w, a.body)
			case <-r.Context().Done():
				close(h.gone)
			case <-p.quit:
				// The test has ended: a post it never answered gets none.
				panic(http.ErrAbortHandler)
			}
		case r.Method == http.MethodPost && (r.URL.Path == "/api/sessions/s1/events" || r.URL.Path == "/api/sessions/s1/attention"):
			p.reports.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			p.count.Add(1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		close(p.quit)
		p.Close()
	})
	return p
}

// held is the next post the server holds.
func (p *permServer) held(t *testing.T) *heldPOST {
	t.Helper()
	select {
	case h := <-p.posts:
		return h
	case <-time.After(mcpHang):
		t.Fatal("no permission post came")
		return nil
	}
}

// chatEnv is the environment of a chat's session s1 of the server at url.
func chatEnv(url string) func(string) string {
	env := sessionEnv(url, "s1", "agent-tok")
	return func(k string) string {
		if k == protocolEnv {
			return "1"
		}
		return env(k)
	}
}

// permissionCall is the tools/call Claude Code makes of the prompt tool.
func permissionCall(id int, call string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"permission","arguments":{"tool_name":"Bash","input":{"command":"ls -la"},"tool_use_id":%q}}}`, id, call)
}

func pingLine(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, id) }

func reportLine(id int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"report","arguments":{"event":"progress","message":"still here"}}}`, id)
}

func cancelLine(id int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":%d,"reason":"the call was decided"}}`, id)
}

// documented is the permission tool's reply as the documented result:
// one text block and nothing else, its text a verdict alone. It fails on
// any other shape.
func documented(t *testing.T, reply map[string]any) string {
	t.Helper()
	res, ok := reply["result"].(map[string]any)
	if !ok || reply["error"] != nil {
		t.Fatalf("not a result: %v", reply)
	}
	if len(res) != 1 {
		t.Fatalf("a result with more than its content: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("%d blocks: %v", len(content), res)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	if len(block) != 2 || block["type"] != "text" {
		t.Fatalf("not one text block: %v", block)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("the text is not JSON: %q", text)
	}
	switch {
	case v["behavior"] == "allow" && len(v) == 1:
	case v["behavior"] == "deny" && len(v) == 2 && v["message"] != "":
	default:
		t.Fatalf("not a documented verdict: %q", text)
	}
	return text
}

func denied(message string) string { return `{"behavior":"deny","message":"` + message + `"}` }

const allowed = `{"behavior":"allow"}`

// While the server holds a permission post, the same stdin's ping and
// report are read and answered; the held call then takes the server's
// answer, and no call's goroutine outlives the loop.
func TestMCPServesWhilePermissionHeld(t *testing.T) {
	srv := newPermServer(t)
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL)})
	c.send(permissionCall(1, "toolu_1"))
	h := srv.held(t)
	c.send(pingLine(2))
	if r := c.byID(2); r["result"] == nil {
		t.Fatalf("ping: %v", r)
	}
	c.send(reportLine(3))
	if text, isErr := toolText(t, c.byID(3)); isErr || text != "reported" {
		t.Fatalf("report while a call is held: %q %v", text, isErr)
	}
	c.noReplyTo(1)
	if h.auth != "Bearer agent-tok" || h.body["call"] != "toolu_1" || h.body["tool"] != "Bash" || fmt.Sprint(h.body["input"]) != "map[command:ls -la]" || len(h.body) != 3 {
		t.Fatalf("the post: %s %v", h.auth, h.body)
	}
	h.allow()
	if text := documented(t, c.byID(1)); text != allowed {
		t.Fatalf("the verdict: %s", text)
	}
	if code, err := c.end(); code != 0 || err != nil {
		t.Fatalf("serve: %d %v", code, err)
	}
	if n := c.s.running.Load(); n != 0 {
		t.Fatalf("%d calls outlived the loop", n)
	}
	if rest := c.rest(); len(rest) != 0 {
		t.Fatalf("replies no request asked for: %v", rest)
	}
}

// A notifications/cancelled for a held call ends its post, which the
// server sees as its request's end, and nothing answers that id: not the
// lost post's deny, not an answer the server gave as the cancellation was
// read (a late one), not a second call under the same id's first. The end
// of stdin ends every held call, with no reply either.
func TestMCPCancelledEndsTheHeldCall(t *testing.T) {
	srv := newPermServer(t)
	reached, release := make(chan struct{}), make(chan struct{})
	s := &mcpServer{getenv: chatEnv(srv.URL), beforeReply: func(id json.RawMessage) {
		if string(id) == "3" {
			close(reached)
			<-release
		}
	}}
	c := startMCP(t, s)

	// Held, then cancelled.
	c.send(permissionCall(1, "toolu_1"))
	h := srv.held(t)
	c.send(cancelLine(1))
	h.ended(t)
	c.idle()
	c.send(pingLine(2))
	c.byID(2)
	c.noReplyTo(1)

	// Answered by the server, cancelled before the answer is written.
	c.send(permissionCall(3, "toolu_3"))
	srv.held(t).allow()
	<-reached
	c.send(cancelLine(3))
	c.send(pingLine(4)) // read after the cancellation: its answer says that was read
	c.byID(4)
	close(release)
	c.idle()
	c.send(pingLine(5))
	c.byID(5)
	c.noReplyTo(3)

	// A second call under a held call's id: the first ends unanswered,
	// the second is denied, and the id has one reply.
	c.send(permissionCall(7, "toolu_7"))
	h = srv.held(t)
	c.send(permissionCall(7, "toolu_7b"))
	if text := documented(t, c.byID(7)); text != denied("Already asked.") {
		t.Fatalf("a second call under one id: %s", text)
	}
	h.ended(t)
	c.idle()
	c.send(pingLine(8))
	c.byID(8)
	c.noReplyTo(7)

	// The end of stdin.
	c.send(permissionCall(9, "toolu_9"))
	h = srv.held(t)
	if code, err := c.end(); code != 0 || err != nil {
		t.Fatalf("serve: %d %v", code, err)
	}
	h.ended(t)
	if n := s.running.Load(); n != 0 {
		t.Fatalf("%d calls outlived the loop", n)
	}
	c.noReplyTo(9, c.rest()...)
}

// The serve loop holds at most maxMCPCalls calls: past them a permission
// call is denied with the bound's words and posts nothing, any other call
// is answered an error at once, and a ping is still answered.
func TestMCPCallsPastTheBoundAreAnsweredAtOnce(t *testing.T) {
	if maxMCPCalls != 12 {
		t.Fatalf("the bound is %d, the session's %d", maxMCPCalls, session.MaxPendingPermissions)
	}
	srv := newPermServer(t)
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL)})
	var held []*heldPOST
	for i := 1; i <= maxMCPCalls; i++ {
		c.send(permissionCall(i, fmt.Sprintf("toolu_%d", i)))
		held = append(held, srv.held(t))
	}
	c.send(permissionCall(100, "toolu_100"))
	if text := documented(t, c.byID(100)); text != denied("Too many requests are waiting; this one was denied.") {
		t.Fatalf("a call past the bound: %s", text)
	}
	c.send(reportLine(101))
	if text, isErr := toolText(t, c.byID(101)); !isErr || text != callsPastTheBound {
		t.Fatalf("a report past the bound: %q %v", text, isErr)
	}
	c.send(pingLine(102))
	c.byID(102)
	if n, r := srv.count.Load(), srv.reports.Load(); n != maxMCPCalls || r != 0 {
		t.Fatalf("%d posts, %d reports", n, r)
	}
	for _, h := range held {
		h.allow()
	}
	for i := 1; i <= maxMCPCalls; i++ {
		if text := documented(t, c.byID(i)); text != allowed {
			t.Fatalf("call %d: %s", i, text)
		}
	}
}

// Thirteen permission calls, each withdrawn by the server (its answer the
// withdrawal's deny, with no cancellation from the client), then a
// report: every withdrawn call has freed its place, so the thirteenth is
// posted and the report answered, neither refused for the bound.
func TestMCPWithdrawnCallsFreeTheirSlots(t *testing.T) {
	srv := newPermServer(t)
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL)})
	withdraw := func(h *heldPOST) {
		h.answer <- permReply{status: http.StatusOK, body: denied("Withdrawn: answered elsewhere.")}
	}
	var held []*heldPOST
	for i := 1; i <= maxMCPCalls; i++ {
		c.send(permissionCall(i, fmt.Sprintf("toolu_%d", i)))
		held = append(held, srv.held(t))
	}
	for _, h := range held {
		withdraw(h)
	}
	for i := 1; i <= maxMCPCalls; i++ {
		if text := documented(t, c.byID(i)); text != denied("Withdrawn: answered elsewhere.") {
			t.Fatalf("call %d: %s", i, text)
		}
	}
	c.send(permissionCall(13, "toolu_13"))
	withdraw(srv.held(t))
	if text := documented(t, c.byID(13)); text != denied("Withdrawn: answered elsewhere.") {
		t.Fatalf("the thirteenth: %s", text)
	}
	c.send(reportLine(14))
	if text, isErr := toolText(t, c.byID(14)); isErr || text != "reported" {
		t.Fatalf("the report: %q %v", text, isErr)
	}
	if n := srv.count.Load(); n != 13 {
		t.Fatalf("%d posts", n)
	}
	c.end()
	if n := c.s.running.Load(); n != 0 {
		t.Fatalf("%d calls outlived the loop", n)
	}
}

// fakeClock runs the timers a test arms when the test moves it on.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Duration
	f    func()
	done bool
}

func (c *fakeClock) afterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.now + d, f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.done
		tm.done = true
		return was
	}
}

// advance moves the clock on by d and runs every timer due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due []func()
	for _, tm := range c.timers {
		if !tm.done && tm.at <= c.now {
			tm.done = true
			due = append(due, tm.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// armed is how long each armed timer has left.
func (c *fakeClock) armed() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, tm := range c.timers {
		if !tm.done {
			out = append(out, tm.at-c.now)
		}
	}
	return out
}

// deadlines records, for each request it carries, whether its context has
// a deadline: a client's Timeout, or a context's, puts one there.
type deadlines struct {
	mu  sync.Mutex
	had []bool
}

func (d *deadlines) RoundTrip(r *http.Request) (*http.Response, error) {
	_, ok := r.Context().Deadline()
	d.mu.Lock()
	d.had = append(d.had, ok)
	d.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

func (d *deadlines) seen() []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.had)
}

// A post the server holds past the session client's 60 s, on a fake
// clock, is still waiting, and takes the server's answer: the post's
// request carries no deadline (the session client's Timeout would give it
// one) and the call arms no timer but its own bound. That bound, once it
// passes, ends the post with the lost request's deny.
func TestPermissionHeldPastTheSessionClientsBound(t *testing.T) {
	// PermissionWait plus one minute: the server denies a minute before
	// the call's own bound, which is the 8 hours Claude Code waits for
	// Conductor's MCP server.
	want, mcpTimeout := session.PermissionWait+time.Minute, time.Duration(agents.ClaudeProtocolMCPTimeoutMs)*time.Millisecond
	if permissionCallWait != want {
		t.Fatalf("the call's bound is %v, want %v", permissionCallWait, want)
	}
	if want != mcpTimeout {
		t.Fatalf("PermissionWait plus a minute is %v, the MCP server's timeout %v", want, mcpTimeout)
	}
	srv := newPermServer(t)
	clock, rec := &fakeClock{}, &deadlines{}
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL), afterFunc: clock.afterFunc, permissionTransport: rec})
	c.send(permissionCall(1, "toolu_1"))
	h := srv.held(t)
	if armed := clock.armed(); len(armed) != 1 || armed[0] != permissionCallWait {
		t.Fatalf("timers armed: %v", armed)
	}
	if seen := rec.seen(); len(seen) != 1 || seen[0] {
		t.Fatalf("the post's request had a deadline: %v", seen)
	}
	clock.advance(61 * time.Second)
	c.send(pingLine(2))
	c.byID(2)
	c.noReplyTo(1)
	h.allow()
	if text := documented(t, c.byID(1)); text != allowed {
		t.Fatalf("the call held past 60 s: %s", text)
	}

	c.send(permissionCall(3, "toolu_3"))
	h = srv.held(t)
	clock.advance(permissionCallWait - time.Second)
	c.send(pingLine(4))
	c.byID(4)
	c.noReplyTo(3)
	clock.advance(time.Second)
	h.ended(t)
	if text := documented(t, c.byID(3)); text != denied("Conductor lost this request.") {
		t.Fatalf("past the call's bound: %s", text)
	}
}

// The tool's every reply is the documented result: one text block of an
// allow or a deny and nothing else. An allow is given only for the
// server's own; an input past 256 KiB is denied with its words and
// posts nothing; a post that ends without the server's answer, or with
// one Conductor cannot read, is the lost request's deny, never an error.
func TestPermissionToolResultIsTheDocumentedOne(t *testing.T) {
	srv := newPermServer(t)
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL)})
	id := 0
	ask := func(reply *permReply) string {
		t.Helper()
		id++
		c.send(permissionCall(id, fmt.Sprintf("toolu_%d", id)))
		if reply != nil {
			srv.held(t).answer <- *reply
		}
		return documented(t, c.byID(id))
	}
	for _, tc := range []struct {
		what  string
		reply permReply
		want  string
	}{
		{"an allow", permReply{status: 200, body: allowed}, allowed},
		{"a deny", permReply{status: 200, body: `{"behavior":"deny","message":"Not <this> one."}`}, denied("Not <this> one.")},
		{"a lost post", permReply{abort: true}, denied("Conductor lost this request.")},
		{"a server error", permReply{status: 500, body: `{"behavior":"allow"}`}, denied("Conductor lost this request.")},
		{"a refusal of the token", permReply{status: 401, body: `{"error":{"code":"unauthorized"}}`}, denied("Conductor lost this request.")},
		{"a body past the route's bound", permReply{status: 413}, denied("This call's input is too large for Conductor to show it whole.")},
		{"a redirect", permReply{status: http.StatusTemporaryRedirect}, denied("Conductor lost this request.")},
		{"an allow with more", permReply{status: 200, body: `{"behavior":"allow","updatedInput":{"command":"rm -rf /"}}`}, denied("Conductor lost this request.")},
		{"an allow with a message", permReply{status: 200, body: `{"behavior":"allow","message":"ok"}`}, denied("Conductor lost this request.")},
		{"an allow and more after it", permReply{status: 200, body: `{"behavior":"allow"} {"behavior":"allow"}`}, denied("Conductor lost this request.")},
		{"another behavior", permReply{status: 200, body: `{"behavior":"ask"}`}, denied("Conductor lost this request.")},
		{"no answer", permReply{status: 200}, denied("Conductor lost this request.")},
		{"a deny with no message", permReply{status: 200, body: `{"behavior":"deny"}`}, denied("Conductor lost this request.")},
	} {
		if got := ask(&tc.reply); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.what, got, tc.want)
		}
	}
	posts := srv.count.Load()

	// An input past 256 KiB, and a call that names no tool: denied with
	// their words, nothing posted.
	id++
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"permission","arguments":{"tool_name":"Write","input":{"content":%q},"tool_use_id":"toolu_big"}}}`, id, strings.Repeat("x", 300<<10)))
	if got := documented(t, c.byID(id)); got != denied("This call's input is too large for Conductor to show it whole.") {
		t.Errorf("a 300 KiB input: %s", got)
	}
	id++
	c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"permission","arguments":{"input":{"command":"ls"}}}}`, id))
	if got := documented(t, c.byID(id)); got != denied("Conductor cannot show this call exactly.") {
		t.Errorf("a call that names no tool: %s", got)
	}
	if n := srv.count.Load(); n != posts {
		t.Fatalf("%d posts for calls never to be posted", n-posts)
	}
	c.end()

	// Outside a session: the lost request's deny, never an error.
	out := mcpRun(t, func(string) string { return "" }, permissionCall(1, "toolu_1"))
	if got := documented(t, out[0]); got != denied("Conductor lost this request.") {
		t.Errorf("outside a session: %s", got)
	}
}

// The permission tool is listed in a session on the protocol way alone
// (CONDUCTOR_PROTOCOL set), from the table's entry as every tool is.
func TestPermissionToolListedOnlyInProtocol(t *testing.T) {
	schema := map[string]any{"type": "object"}
	ops := []api.Operation{
		{Name: "report", MCP: "report", Summary: "s", Description: "report", Input: schema},
		{Name: "permission", MCP: permissionTool, Summary: "s", Description: "Conductor's own: not for the model to call.", Input: schema},
	}
	names := func(tools []mcpTool) []string {
		var out []string
		for _, tl := range tools {
			out = append(out, tl.Name)
		}
		return out
	}
	for _, c := range []struct{ brakeman, granted, protocol bool }{{false, false, false}, {true, false, false}, {true, true, false}, {false, false, true}, {true, true, true}} {
		got := names(mcpToolsFrom(ops, c.brakeman, c.granted, c.protocol))
		if slices.Contains(got, permissionTool) != c.protocol || !slices.Contains(got, "report") {
			t.Errorf("%+v lists %v", c, got)
		}
	}
	tools := mcpToolsFrom(ops, false, false, true)
	if tools[1].Description != ops[1].Description || tools[1].InputSchema["type"] != "object" {
		t.Fatalf("not the table's: %+v", tools[1])
	}
	// The real table, through tools/list: outside a chat never; in one
	// whenever the table has the entry, which tells the model it is
	// Conductor's and not to be called.
	if slices.Contains(listedTools(t, sessionEnv("http://127.0.0.1:1", "s1", "tok")), permissionTool) {
		t.Fatal("a terminal's session lists permission")
	}
	inChat := slices.Contains(listedTools(t, chatEnv("http://127.0.0.1:1")), permissionTool)
	i := slices.IndexFunc(api.Operations(), func(op api.Operation) bool { return op.MCP == permissionTool })
	if inChat != (i >= 0) {
		t.Fatalf("a chat's session lists permission: %v, the table has it: %v", inChat, i >= 0)
	}
	if i >= 0 {
		if d := api.Operations()[i].Description; !strings.Contains(d, "Conductor") || !strings.Contains(strings.ToLower(d), "not") {
			t.Errorf("the tool's description: %q", d)
		}
	}
}
