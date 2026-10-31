package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// mcpRun feeds lines to the MCP server and returns its replies, one per request.
func mcpRun(t *testing.T, getenv func(string) string, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := &mcpServer{out: &out, getenv: getenv}
	if code, err := s.serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n")); code != 0 || err != nil {
		t.Fatalf("serve: %d %v", code, err)
	}
	var replies []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("reply %q: %v", l, err)
		}
		replies = append(replies, m)
	}
	return replies
}

func toolText(t *testing.T, reply map[string]any) (string, bool) {
	t.Helper()
	res, _ := reply["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", reply)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isErr, _ := res["isError"].(bool)
	return text, isErr
}

// The handshake, the tool list, and tool calls that reach the session's
// routes: a report through the notify route, a crew formed through the
// self-service route, the run read back, a link minted.
func TestMcpServesTheSkillsCommandsAsTools(t *testing.T) {
	ns := startNotifyServer(t)
	srv, seen := fakeSelf(t)
	t.Setenv("CONDUCTOR_SESSION_ID", "s1")
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-tok")
	_ = ns
	env := func(k string) string {
		switch k {
		case "CONDUCTOR_SESSION_ID":
			return "s1"
		case "CONDUCTOR_NOTIFY_URL":
			return srv.URL + "/api/sessions/s1/attention"
		case "CONDUCTOR_NOTIFY_TOKEN":
			return "agent-tok"
		}
		return ""
	}
	replies := mcpRun(t, env,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"form_crew","arguments":{"name":"review team","goal":"review","members":[{"name":"lead","agentId":"","prompt":"","start":{"when":"manual"}}],"self":"lead","open":true}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"run_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"link","arguments":{"ttlSeconds":3600,"label":"for the PR"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
		`not json`,
	)
	if len(replies) != 9 {
		t.Fatalf("%d replies: %v", len(replies), replies)
	}
	init, _ := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "conductor" || init["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize %v", replies[0])
	}
	tools, _ := replies[1]["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "report,set_state,ask,form_crew,add_member,run_status,link,task_context,task_result,message_send,inbox_read,inbox_ack,task_blocked,task_checkpoint,ticket_read,ticket_comment" {
		t.Fatalf("tools %v", names)
	}
	for _, tl := range tools {
		if tl.(map[string]any)["inputSchema"].(map[string]any)["type"] != "object" {
			t.Fatalf("tool schema %v", tl)
		}
	}
	if r, _ := replies[2]["result"].(map[string]any); r == nil {
		t.Fatalf("ping %v", replies[2])
	}
	if text, isErr := toolText(t, replies[3]); isErr || !strings.HasPrefix(text, "crew formed: run team-1a2b3c4d at http://example.test/runs/team-1a2b3c4d; you are lead") {
		t.Fatalf("form_crew %q %v", text, isErr)
	}
	body := (*seen)[0]["body"].(map[string]any)
	if body["self"] != "lead" || body["open"] != true || (*seen)[0]["auth"] != "Bearer agent-tok" {
		t.Fatalf("form_crew request %v", (*seen)[0])
	}
	if text, isErr := toolText(t, replies[4]); isErr || !strings.HasPrefix(text, "run team-1a2b3c4d (running) review team; you are lead") || !strings.Contains(text, "reviewer") {
		t.Fatalf("run_status %q %v", text, isErr)
	}
	if text, isErr := toolText(t, replies[5]); isErr || text != "http://example.test/join/tok" {
		t.Fatalf("link %q %v", text, isErr)
	}
	if e, _ := replies[6]["error"].(map[string]any); e == nil || e["code"] != -32602.0 {
		t.Fatalf("unknown tool %v", replies[6])
	}
	if e, _ := replies[7]["error"].(map[string]any); e == nil || e["code"] != -32601.0 {
		t.Fatalf("unknown method %v", replies[7])
	}
	if e, _ := replies[8]["error"].(map[string]any); e == nil || e["code"] != -32700.0 {
		t.Fatalf("parse error %v", replies[8])
	}
}

// report, set_state and ask post to the session's attention and events
// routes with the words notify would use; ask carries its choices.
func TestMcpReportsThroughNotify(t *testing.T) {
	ns := startNotifyServer(t)
	replies := mcpRun(t, os.Getenv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"message":"Which database?","choices":["Postgres","SQLite"]}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || text != "reported" {
		t.Fatalf("ask %q %v", text, isErr)
	}
	if ns.body["state"] != "needs_input" || ns.body["kind"] != "prompt" || ns.body["message"] != "Which database?" || ns.body["ask"] != true {
		t.Fatalf("ask body %v", ns.body)
	}
	opts, _ := ns.body["options"].([]any)
	if len(opts) != 2 || opts[1].(map[string]any)["input"] != "SQLite\r" {
		t.Fatalf("options %v", opts)
	}
	replies = mcpRun(t, os.Getenv,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"report","arguments":{"event":"handoff","to":"tests","message":"the handlers are in"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"set_state","arguments":{"state":"sleepy"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ask","arguments":{"message":"x","choices":["a","b","c","d","e","f","g"]}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || text != "reported" {
		t.Fatalf("report %q %v", text, isErr)
	}
	if ns.path != "/api/sessions/s1/events" || ns.body["type"] != "handoff" || ns.body["to"] != "tests" {
		t.Fatalf("report body %s %v", ns.path, ns.body)
	}
	if text, isErr := toolText(t, replies[1]); !isErr || !strings.Contains(text, "working, done or clear") {
		t.Fatalf("set_state %q %v", text, isErr)
	}
	if text, isErr := toolText(t, replies[2]); !isErr || !strings.Contains(text, "at most 6") {
		t.Fatalf("too many choices %q %v", text, isErr)
	}
	// set_state is no question of the agent's: no mark, its clear included.
	for _, state := range []string{"working", "done", "clear"} {
		ns.body = nil
		replies = mcpRun(t, os.Getenv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"set_state","arguments":{"state":"`+state+`"}}}`)
		if text, isErr := toolText(t, replies[0]); isErr || text != "reported" {
			t.Fatalf("set_state %s: %q %v", state, text, isErr)
		}
		if ns.body["state"] != state || ns.body["ask"] != nil {
			t.Fatalf("set_state %s body %v", state, ns.body)
		}
	}
}

// Outside a session every tool answers so, as an error the agent reads; the
// handshake still works, so a registration that outlives the session is
// harmless.
func TestMcpOutsideASessionSaysSo(t *testing.T) {
	replies := mcpRun(t, func(string) string { return "" },
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"report","arguments":{"event":"progress","message":"x"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_status","arguments":{}}}`,
	)
	if init, _ := replies[0]["result"].(map[string]any); init["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("initialize %v", replies[0])
	}
	for _, r := range replies[1:] {
		if text, isErr := toolText(t, r); !isErr || !strings.Contains(text, "not running inside a Conductor session") {
			t.Fatalf("%q %v", text, isErr)
		}
	}
	var stderr bytes.Buffer
	if code, err := runMcp(context.Background(), []string{"-h"}, strings.NewReader(""), io.Discard, &stderr); code != 0 || err != nil || !strings.Contains(stderr.String(), "Usage: conductor mcp") {
		t.Fatalf("mcp -h: %d %v %s", code, err, stderr.String())
	}
	if code, err := runMcp(context.Background(), []string{"extra"}, strings.NewReader(""), io.Discard, &stderr); code != 2 || err == nil {
		t.Fatalf("mcp extra: %d %v", code, err)
	}
}

// sessionEnv is the environment of session sid of the server at url, with
// its agent token, for an MCP server.
func sessionEnv(url, sid, token string) func(string) string {
	return func(k string) string {
		switch k {
		case "CONDUCTOR_SESSION_ID":
			return sid
		case "CONDUCTOR_NOTIFY_URL":
			return url + "/api/sessions/" + sid + "/attention"
		case "CONDUCTOR_NOTIFY_TOKEN":
			return token
		}
		return ""
	}
}

// ticket_read and ticket_comment against a real server, with the
// session's own token: no ticket before the run is linked to one, which
// the agent reads as an error; the ticket once it is; a comment as the
// run's member, sent once more with the same operation id when its reply
// is lost and taken once; a comment without a body refused before
// anything is sent.
func TestMcpTicketToolsAgainstTheServer(t *testing.T) {
	a := newDispatchAPI(t)
	run, sid, token := a.launchMember(t)
	env := sessionEnv(a.hs.URL, sid, token)
	read := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ticket_read","arguments":{}}}`
	if text, isErr := toolText(t, mcpRun(t, env, read)[0]); !isErr || !strings.HasSuffix(text, "(no_ticket)") {
		t.Fatalf("ticket_read before a link: %q %v", text, isErr)
	}
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	a.mustDispatch(t, "create", "--project", "CND", "--title", "Ship it", "--owner", "Alice")
	a.mustDispatch(t, "link", "CND-1", "--kind", "run", "--ref", run)
	before := a.count()
	a.loseNext("POST", "/api/sessions/"+sid+"/ticket/comments", 1)
	replies := mcpRun(t, env, read,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ticket_comment","arguments":{"body":"from the <tool>"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ticket_comment","arguments":{}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || !strings.HasPrefix(text, "# CND-1: Ship it\n\n- status: backlog\n") || !strings.Contains(text, "- owner: Alice\n") {
		t.Fatalf("ticket_read: %v\n%s", isErr, text)
	}
	if text, isErr := toolText(t, replies[1]); isErr || text != "comment #1 on your ticket" {
		t.Fatalf("ticket_comment: %q %v", text, isErr)
	}
	if text, isErr := toolText(t, replies[2]); !isErr || text != "ticket_comment needs a body" {
		t.Fatalf("ticket_comment without a body: %q %v", text, isErr)
	}
	var posts []seenRequest
	for _, r := range a.requests(before) {
		if r.method == "POST" {
			posts = append(posts, r)
		}
	}
	if len(posts) != 2 || posts[0].reply != -1 || posts[0].body["opId"] != posts[1].body["opId"] {
		t.Fatalf("the comment's posts: %+v", posts)
	}
	if cs := a.ticket(t, "CND-1").Comments; len(cs) != 1 || cs[0].Author != "agent:a" || cs[0].Body != "from the <tool>" {
		t.Fatalf("the ticket's comments: %+v", cs)
	}
}

// ticket_read takes the largest reply the session's read can make through
// the session client's limit, and gives the agent the ticket whole: the
// tool's text, as tools/call puts it in its reply (the round trip of
// three megabytes through JSON-RPC is tested on the small ticket above).
func TestMcpTicketReadGivesTheLargestTicketWhole(t *testing.T) {
	l := largestFor(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/s1/ticket", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(l.session) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := &mcpServer{out: io.Discard, getenv: sessionEnv(srv.URL, "s1", "agent-tok")}
	text, err := s.call(t.Context(), "ticket_read", nil)
	if err != nil {
		t.Fatalf("ticket_read: %v", err)
	}
	checkWhole(t, text, l.view, l.labels, l.milestone)
}

// ticket_comment reports a comment made whose reply carries no comment,
// the server unable to read it back after its commit (comment null beside
// a done receipt), as made, without a number it does not have.
func TestMcpTicketCommentWithoutTheCommentRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/ticket/comments" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"receipt":{"opId":"o1","intent":"comment.add","status":"done","rev":1,"id":"c1"},"comment":null}`)
	}))
	t.Cleanup(srv.Close)
	s := &mcpServer{out: io.Discard, getenv: sessionEnv(srv.URL, "s1", "agent-tok")}
	text, err := s.call(t.Context(), "ticket_comment", json.RawMessage(`{"body":"seen"}`))
	if err != nil || text != "commented on your ticket" {
		t.Fatalf("ticket_comment with no comment read: %q %v", text, err)
	}
}

// ticket_comment's first reply left unanswered leaves its resend time
// within the tool's budget: the first post held until the client gives it
// up, at half of what the budget had left (askTwice), the same operation
// is sent once more and its reply answers the tool. The budget here is
// the caller's deadline, under the tool's own 60 s, which equal the
// session client's timeout on one request.
func TestMcpTicketCommentResendsAfterAStalledReply(t *testing.T) {
	var (
		mu    sync.Mutex
		opIDs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/ticket/comments" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			OpID string `json:"opId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		opIDs = append(opIDs, body.OpID)
		n := len(opIDs)
		mu.Unlock()
		if n == 1 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"receipt":{"opId":"`+body.OpID+`","intent":"comment.add","status":"done","rev":1,"id":"c1"},"comment":{"id":"c1","n":1,"author":"agent:a","by":"a","body":"seen"}}`)
	}))
	t.Cleanup(srv.Close)
	s := &mcpServer{out: io.Discard, getenv: sessionEnv(srv.URL, "s1", "agent-tok")}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	text, err := s.call(ctx, "ticket_comment", json.RawMessage(`{"body":"seen"}`))
	if err != nil || text != "comment #1 on your ticket" {
		t.Fatalf("ticket_comment after a stalled reply: %q %v", text, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opIDs) != 2 || opIDs[0] == "" || opIDs[0] != opIDs[1] {
		t.Fatalf("the comment's posts: %q", opIDs)
	}
}
