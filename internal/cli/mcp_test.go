package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/version"
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
	if strings.Join(names, ",") != "report,set_state,ask,form_crew,add_member,run_status,link,task_context,task_result,message_send,inbox_read,inbox_ack,task_blocked,task_checkpoint,ticket_read,ticket_comment,guide,open_url" {
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

// ticket_read in an attempt of a run launched from its ticket (Program
// item DS2, PR-13) says what the run was launched at first, then what
// changed since and that the run follows the ticket as launched, as
// conductor task ticket does.
func TestMcpTicketReadSaysWhatItWasLaunchedAt(t *testing.T) {
	a := newPlannedAPI(t)
	launchedRev := a.ticket(t, "CND-1").Rev
	_, sid, token := a.launched(t)
	a.admin(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "e1", "set": map[string]any{"title": "Fix the login loop now", "acceptance": "It never loops."}}, http.StatusOK)
	a.mustDispatch(t, "comment", "CND-1", "one")
	a.mustDispatch(t, "comment", "CND-1", "two")
	now := a.ticket(t, "CND-1").Rev
	read := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ticket_read","arguments":{}}}`
	text, isErr := toolText(t, mcpRun(t, sessionEnv(a.hs.URL, sid, token), read)[0])
	want := fmt.Sprintf("As launched: CND-1 at revision %d, plan revision 1, in .conductor/ticket.md (the package: .conductor/package.json, the attachments in .conductor/attachments/)."+
		" Now at revision %d: the title and the acceptance criteria changed; 2 comments since. Your run follows the ticket as launched", launchedRev, now)
	if isErr || !strings.HasPrefix(text, want) || !strings.Contains(text, "\n\n# CND-1: Fix the login loop now\n") {
		t.Fatalf("ticket_read: %v\n%s\nwant the start\n%s", isErr, text, want)
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

// ticket_read gives the agent a ticket's text exactly as it is stored,
// through the JSON-RPC reply: a zero-width joiner and non-joiner, an
// emoji sequence, right-to-left text, and a file name that holds a joiner,
// in the description, a comment and an attachment's name.
func TestMcpTicketReadKeepsTheTicketsCharacters(t *testing.T) {
	zwj, zwnj := string(rune(0x200d)), string(rune(0x200c))
	emoji := string([]rune{0x1f469, 0x200d, 0x1f4bb})
	persian := string([]rune{0x0645, 0x06cc, 0x200c, 0x062e, 0x0648, 0x0627, 0x0647, 0x0645})
	file := "src/a" + zwj + "b.txt"
	description := "Fix " + file + " for " + emoji + "\n\n" + persian
	comment := "join" + zwj + "ed and not" + zwnj + "joined, " + persian + " " + emoji
	ticket := map[string]any{"ticket": map[string]any{"key": "CND-1", "title": "A title " + emoji, "status": "open", "description": description,
		"comments":    []any{map[string]any{"n": 1, "author": "you", "body": comment, "createdAt": time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}},
		"attachments": []any{map[string]any{"name": file, "size": 10, "contentType": "text/plain"}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/s1/ticket" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ticket)
	}))
	t.Cleanup(srv.Close)
	read := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ticket_read","arguments":{}}}`
	text, isErr := toolText(t, mcpRun(t, sessionEnv(srv.URL, "s1", "agent-tok"), read)[0])
	for _, want := range []string{"# CND-1: A title " + emoji + "\n", "## Description\n\n" + description + "\n", "\n\n" + comment + "\n", "- " + file + " ("} {
		if isErr || !strings.Contains(text, want) {
			t.Errorf("ticket_read lacks %q exactly as stored: %v\n%q", want, isErr, text)
		}
	}
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

// The golden file is main's tools/list reply before the tools were listed
// from the table (docs/plans/brakeman.md, PR-2): its sixteen tools are
// listed byte for byte as they were, in their order, and the guide after
// them, then open_url (#436). Two words of it were changed since, to say
// what the server enforces as the guide and the skill do (#703): form_crew's
// limit (2 an hour, not 2 a session) and report's message (500 bytes, not
// characters). task_result has gained a repair's addressed since, its
// schema's property and its description's sentence (RV1 PR-5).
func TestToolsListIsUnchanged(t *testing.T) {
	golden, err := os.ReadFile("testdata/tools-list-main.json")
	if err != nil {
		t.Fatal(err)
	}
	tools := func(line []byte) []json.RawMessage {
		var reply struct {
			Result struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		return reply.Result.Tools
	}
	var out bytes.Buffer
	s := &mcpServer{out: &out, getenv: func(string) string { return "" }}
	if _, err := s.serve(t.Context(), strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	was, now := tools(golden), tools(out.Bytes())
	if len(was) != 16 || len(now) != len(was)+2 {
		t.Fatalf("%d tools before, %d now", len(was), len(now))
	}
	for i := range was {
		if !bytes.Equal(was[i], now[i]) {
			t.Errorf("tool %d changed:\n was %s\n now %s", i, was[i], now[i])
		}
	}
	var guide, open mcpTool
	if err := json.Unmarshal(now[len(was)], &guide); err != nil || guide.Name != "guide" || guide.InputSchema["type"] != "object" {
		t.Fatalf("the guide %s %v", now[len(was)], err)
	}
	if err := json.Unmarshal(now[len(was)+1], &open); err != nil || open.Name != "open_url" || open.InputSchema["type"] != "object" {
		t.Fatalf("the last tool %s %v", now[len(was)+1], err)
	}
	// The whole reply is the golden's with the guide's and open_url's entries added.
	trimmed := strings.TrimRight(string(golden), "\n")
	if !strings.HasPrefix(out.String(), strings.TrimSuffix(trimmed, "]}}")+",") {
		t.Fatalf("the reply does not begin as main's did")
	}
}

// Every operation's summary and description keep their bounds, and an
// operation with a tool has a description to give it.
func TestSummaryAndDescriptionAreBounded(t *testing.T) {
	for _, op := range api.Operations() {
		if op.Summary == "" || len(op.Summary) > 300 {
			t.Errorf("%s: a summary of %d bytes", op.Name, len(op.Summary))
		}
		if len(op.Description) > 1<<10 {
			t.Errorf("%s: a description of %d bytes", op.Name, len(op.Description))
		}
		if op.MCP != "" && op.Description == "" {
			t.Errorf("%s: a tool with no description", op.Name)
		}
	}
}

// Every tool the MCP server handles is an operation of the table, listed
// under its name, and each answers outside a session that it is outside
// one, never that it is unknown: the handled set is call's.
func TestEveryToolIsAnOperation(t *testing.T) {
	byMCP := map[string]api.Operation{}
	names := map[string]bool{}
	for _, op := range api.Operations() {
		if names[op.Name] {
			t.Errorf("two operations named %s", op.Name)
		}
		names[op.Name] = true
		if op.MCP != "" {
			byMCP[op.MCP] = op
		}
	}
	listed := map[string]bool{}
	for _, tl := range append(mcpTools(false, false), mcpTools(true, true)...) {
		listed[tl.Name] = true
		if op, ok := byMCP[tl.Name]; !ok || tl.Description != op.Description {
			t.Errorf("tool %s: not the table's", tl.Name)
		}
	}
	s := &mcpServer{getenv: func(string) string { return "" }}
	for name := range mcpHandled {
		if _, ok := byMCP[name]; !ok || !listed[name] {
			t.Errorf("tool %s is handled but not the table's or not listed", name)
		}
		if _, err := s.call(t.Context(), name, nil); !errors.Is(err, errNotInSession) {
			t.Errorf("tool %s outside a session: %v", name, err)
		}
	}
	if _, err := s.call(t.Context(), "nope", nil); !errors.As(err, new(unknownTool)) {
		t.Errorf("an unknown tool: %v", err)
	}
}

// An entry of the table with an MCP name and no handler is not listed,
// in a Brakeman turn's session or any other; every tool the table names
// has its handler (Brakeman's since PR-5).
func TestAnUnhandledToolIsNotListed(t *testing.T) {
	for _, op := range api.Operations() {
		if op.MCP != "" && !mcpHandled[op.MCP] {
			t.Errorf("the table's tool %s has no handler", op.MCP)
		}
	}
	ops := append(api.Operations(), api.Operation{Name: "not_yet", MCP: "not_yet", Summary: "s", Description: "d", Input: map[string]any{"type": "object"}})
	for _, c := range []struct{ brakeman, granted bool }{{false, false}, {true, false}, {true, true}} {
		for _, tl := range mcpToolsFrom(ops, c.brakeman, c.granted) {
			if tl.Name == "not_yet" {
				t.Fatal("a tool with no handler is listed")
			}
		}
		if len(mcpToolsFrom(ops, c.brakeman, c.granted)) != len(mcpTools(c.brakeman, c.granted)) {
			t.Fatal("the listing changed")
		}
	}
}

// The guide tool reads the session's guide from a real server with the
// session's token: the overview, then a section, as text; a section the
// server does not have is the server's refusal.
func TestTheGuideToolReadsTheServer(t *testing.T) {
	a := newDispatchAPI(t)
	_, sid, token := a.launchMember(t)
	env := map[string]string{
		"CONDUCTOR_SESSION_ID":   sid,
		"CONDUCTOR_NOTIFY_URL":   a.hs.URL + "/api/sessions/" + sid + "/attention",
		"CONDUCTOR_NOTIFY_TOKEN": token,
	}
	replies := mcpRun(t, func(k string) string { return env[k] },
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"guide","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"guide","arguments":{"section":"operations"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"guide","arguments":{"section":"nope"}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || !hasAll(text, "session "+sid, "run_member", "scopes: session.", "A question goes to a person with ask;", "operations (", `call guide with {"section": NAME}`) || strings.Contains(text, "the guide is the server's") {
		t.Fatalf("overview %q %v", text, isErr)
	}
	text, isErr := toolText(t, replies[1])
	if isErr || !hasAll(text, "report [session] POST /api/sessions/{id}/events", "tool guide", "command conductor crew status") || strings.Contains(text, "task_result") {
		t.Fatalf("operations %q %v", text, isErr)
	}
	// Each operation's input and bounds, as the server gives them.
	if !hasAll(text, `input {"additionalProperties":false,"properties":{"event":{"enum":["progress",`, `"required":["event"]`, "bounds body 65536, message ") {
		t.Fatalf("operations without their inputs %q", text)
	}
	if text, isErr := toolText(t, replies[2]); !isErr || !strings.Contains(text, "no_section") {
		t.Fatalf("no section %q %v", text, isErr)
	}
}

// A server of another version: the tool says first that the guide is the
// server's.
func TestTheGuideToolSaysWhenVersionsDiffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/s1/guide" || r.Header.Get("Authorization") != "Bearer agent-tok" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"guide":1,"version":"0.0.0-other","commit":"abc","section":"limits","limits":[{"name":"inbox","max":50,"about":"x"}]}`))
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"CONDUCTOR_NOTIFY_URL": srv.URL + "/api/sessions/s1/attention", "CONDUCTOR_NOTIFY_TOKEN": "agent-tok"}
	replies := mcpRun(t, func(k string) string { return env[k] },
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"guide","arguments":{"section":"limits"}}}`)
	text, isErr := toolText(t, replies[0])
	want := "this conductor is " + version.Version + ", the server is 0.0.0-other: the guide is the server's\n\n"
	if isErr || !strings.HasPrefix(text, want) || !strings.Contains(text, `"inbox"`) {
		t.Fatalf("%q %v", text, isErr)
	}
}

// brakemanEnv is sessionEnv of a Brakeman turn's session, answering
// request br-1.
func brakemanEnv(url, sid, token string) func(string) string {
	env := sessionEnv(url, sid, token)
	return func(k string) string {
		if k == envBrakeman {
			return "br-1"
		}
		return env(k)
	}
}

// listedTools is tools/list's names in a session of env.
func listedTools(t *testing.T, env func(string) string) []string {
	t.Helper()
	res, _ := mcpRun(t, env, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)[0]["result"].(map[string]any)
	var names []string
	for _, x := range res["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

// Brakeman's three tools are listed in a Brakeman turn's session alone,
// with the table's descriptions and schemas; a worker's session lists
// none of them, and calling one there is refused before anything is sent.
func TestBrakemansToolsOnlyInABrakemanSession(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	brakemans := []string{"brakeman_request", "observe", "brief_submit"}
	worker := listedTools(t, sessionEnv(srv.URL, "s1", "tok"))
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent from a worker's session's listing", hits.Load())
	}
	// A turn's listing asks the server whether it is granted an act.
	turn := listedTools(t, brakemanEnv(srv.URL, "s1", "tok"))
	hits.Store(0)
	for _, name := range brakemans {
		if slices.Contains(worker, name) {
			t.Errorf("a worker's session lists %s", name)
		}
		if !slices.Contains(turn, name) {
			t.Errorf("a Brakeman turn's session does not list %s", name)
		}
	}
	for _, tl := range mcpTools(true, true) {
		op, ok := operation(tl.Name)
		if !ok || tl.Description != op.Description || !reflect.DeepEqual(tl.InputSchema, op.Input) {
			t.Errorf("tool %s is not the table's", tl.Name)
		}
	}
	if slices.Contains(worker, "act") {
		t.Error("a worker's session lists act")
	}
	s := &mcpServer{out: io.Discard, getenv: sessionEnv(srv.URL, "s1", "tok")}
	for _, name := range append(brakemans, "act") {
		if _, err := s.call(t.Context(), name, json.RawMessage(`{"what":"changes","brief":{}}`)); !errors.Is(err, errNotBrakeman) {
			t.Errorf("%s in a worker's session: %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent from a worker's session", hits.Load())
	}
}

// brief_submit's schema, as tools/list sends it, tells a brief's shape:
// each item kind of internal/brakeman's table a branch of the items'
// oneOf, with its example, and an example brief; a draft crew's crew is
// the body crew_create takes. The live check's agent read Conductor's
// source to learn it from a schema of {"type":"object"}.
func TestBriefSubmitsSchemaIsTheBriefsShape(t *testing.T) {
	res, _ := mcpRun(t, brakemanEnv("http://127.0.0.1:1", "s1", "tok"), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)[0]["result"].(map[string]any)
	var brief map[string]any
	for _, x := range res["tools"].([]any) {
		if tl := x.(map[string]any); tl["name"] == "brief_submit" {
			brief = tl["inputSchema"].(map[string]any)["properties"].(map[string]any)["brief"].(map[string]any)
		}
	}
	if brief == nil {
		t.Fatal("no brief_submit")
	}
	branches := brief["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)
	kinds := brakeman.ItemKinds()
	if len(branches) != len(kinds) {
		t.Fatalf("%d branches, %d kinds", len(branches), len(kinds))
	}
	for i, k := range kinds {
		br := branches[i].(map[string]any)
		props := br["properties"].(map[string]any)
		if props["kind"].(map[string]any)["const"] != k.Kind || len(props) != len(k.Fields)+1 || len(br["examples"].([]any)) != 1 {
			t.Errorf("branch %d: %v", i, br)
		}
	}
	if ex := brief["examples"].([]any); len(ex) != 1 || ex[0].(map[string]any)["schema"] != brakeman.BriefSchema {
		t.Errorf("the example brief: %v", brief["examples"])
	}
	crew := branches[8].(map[string]any)["properties"].(map[string]any)["crew"].(map[string]any)
	if crew["properties"].(map[string]any)["members"] == nil || crew["additionalProperties"] != false {
		t.Errorf("a draft crew's crew is not crew_create's body: %v", crew)
	}
}

// A Brakeman turn's session lists no tool that forms a crew, adds a
// member or reaches a ticket; it keeps the rest, the guide among them.
func TestNoCrewToolsForBrakeman(t *testing.T) {
	turn := listedTools(t, brakemanEnv("http://127.0.0.1:1", "s1", "tok"))
	for _, name := range []string{"form_crew", "add_member", "ticket_read", "ticket_comment"} {
		if slices.Contains(turn, name) {
			t.Errorf("a Brakeman turn's session lists %s", name)
		}
	}
	for _, name := range []string{"report", "ask", "set_state", "run_status", "guide"} {
		if !slices.Contains(turn, name) {
			t.Errorf("a Brakeman turn's session does not list %s", name)
		}
	}
	// Less the crew and ticket tools and the task's own, plus Brakeman's.
	if want := len(mcpTools(false, false)) - 4 - len(taskOnly) + 3; len(turn) != want {
		t.Fatalf("%d tools, want %d: %v", len(turn), want, turn)
	}
}

// observe sends each what to its own route by the table, with the
// session's token and its name in X-Conductor-Session: a path's
// parameters escaped into its path, the rest into its query, a number
// taken as its text; a check's log comes back as text after its SHA-256.
// A key the route does not take, a path parameter missing and a what
// that is none are refused before anything is sent.
func TestObservePassesItsQuery(t *testing.T) {
	type seen struct{ path, query, auth, session string }
	var (
		mu  sync.Mutex
		got []seen
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("X-Conductor-Session")})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/log") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Conductor-Log-SHA256", "abc123")
			_, _ = io.WriteString(w, "FAIL TestX\n")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"ok":true}`+"\n")
	}))
	t.Cleanup(srv.Close)
	env := brakemanEnv(srv.URL, "s1", "agent-tok")
	replies := mcpRun(t, env,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":{"what":"changes","query":{"since":"2026-11-01T18:00:00Z","limit":50}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"observe","arguments":{"what":"task","query":{"run":"r 1","task":"t1"}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"observe","arguments":{"what":"log","query":{"run":"r1","task":"t1","n":"2","id":"test","k":"0"}}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"observe","arguments":{"what":"changes","query":{"nope":"1"}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"observe","arguments":{"what":"task","query":{"run":"r1"}}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"observe","arguments":{"what":"everything"}}}`,
	)
	for i, want := range []string{`{"ok":true}`, `{"ok":true}`, "sha256 abc123\n\nFAIL TestX\n"} {
		if text, isErr := toolText(t, replies[i]); isErr || text != want {
			t.Errorf("reply %d: %q %v", i, text, isErr)
		}
	}
	for i, want := range []string{"observe changes takes cursor, limit, since", "observe task needs task", "observe: what is one of changes, status, screen"} {
		if text, isErr := toolText(t, replies[3+i]); !isErr || !strings.HasPrefix(text, want) {
			t.Errorf("reply %d: %q %v", 3+i, text, isErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []seen{
		{"/api/brakeman/changes", "limit=50&since=2026-11-01T18%3A00%3A00Z", "Bearer agent-tok", "s1"},
		{"/api/runs/r%201/tasks/t1", "", "Bearer agent-tok", "s1"},
		{"/api/runs/r1/tasks/t1/attempts/2/steps/test/0/log", "", "Bearer agent-tok", "s1"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the reads:\n got %+v\nwant %+v", got, want)
	}
}

// brief_submit's reply lost to the network is asked for once more with
// the same opId, minted by the tool when the agent gives none; one the
// agent gives is the one sent.
func TestBriefSubmitResendsTheSameOpID(t *testing.T) {
	var (
		mu    sync.Mutex
		opIDs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/brakeman/brief" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			OpID  string          `json:"opId"`
			Brief json.RawMessage `json:"brief"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body.Brief) != `{"title":"t"}` {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		opIDs = append(opIDs, body.OpID)
		n := len(opIDs)
		mu.Unlock()
		if n == 1 {
			// Lost: the connection cut before the reply.
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"request":{"id":"br-1","state":"briefed"}}`)
	}))
	t.Cleanup(srv.Close)
	replies := mcpRun(t, brakemanEnv(srv.URL, "s1", "agent-tok"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"brief_submit","arguments":{"brief":{"title":"t"}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"brief_submit","arguments":{"opId":"mine-1","brief":{"title":"t"}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"brief_submit","arguments":{"opId":"x"}}}`,
	)
	for i := range 2 {
		if text, isErr := toolText(t, replies[i]); isErr || text != "brief kept: request br-1 is briefed; your turn ends now" {
			t.Fatalf("reply %d: %q %v", i, text, isErr)
		}
	}
	if text, isErr := toolText(t, replies[2]); !isErr || !strings.Contains(text, "needs a brief") {
		t.Fatalf("no brief: %q %v", text, isErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opIDs) != 3 || len(opIDs[0]) != 16 || opIDs[0] != opIDs[1] || opIDs[2] != "mine-1" {
		t.Fatalf("the posts' opIds: %q", opIDs)
	}
}

// grantServer is a server of a Brakeman turn's session s1 whose guide
// lists the operations act as granted, answering every act's route with
// the reply of answer, its posts' paths, sessions and bodies kept in
// order; lose, when set, cuts the connection of the first post.
type grantServer struct {
	*httptest.Server
	mu           sync.Mutex
	posts        []grantPost
	requestReads int
}

type grantPost struct {
	path, auth, session string
	body                map[string]any
}

func newGrantServer(t *testing.T, act []string, lose bool) *grantServer {
	t.Helper()
	g := &grantServer{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/sessions/s1/guide" && r.URL.Query().Get("section") == "operations" {
			ops := []map[string]any{{"name": "observe", "scope": "observe"}}
			if len(act) > 0 {
				ops = append(ops, map[string]any{"name": "act", "scope": "act"})
			}
			for _, a := range act {
				ops = append(ops, map[string]any{"name": a, "scope": "act"})
			}
			b, _ := json.Marshal(map[string]any{"section": "operations", "operations": ops})
			_, _ = w.Write(b)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/sessions/s1/brakeman/request" {
			// The listing must not mark the request read.
			g.mu.Lock()
			g.requestReads++
			g.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.posts = append(g.posts, grantPost{r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("X-Conductor-Session"), body})
		n := len(g.posts)
		g.mu.Unlock()
		if lose && n == 1 {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		if strings.HasSuffix(r.URL.Path, "/launch") {
			// As the server answers an act: Dispatch's launch, never the run.
			_, _ = io.WriteString(w, `{"receipt":{"opId":"bk1","intent":"launch","status":"done","id":"0123456789abcdef"},"receiptRef":"op:brakeman/bk1","launch":{"id":"0123456789abcdef","runId":"r-1","state":"started"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"receipt":{"opId":"bk2","intent":"plan.propose","status":"done","id":"fedcba9876543210"},"plan":{"id":"fedcba9876543210","n":2,"state":"proposed"}}`)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *grantServer) sent() []grantPost {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.posts)
}

// The act tool is a Brakeman turn's alone, and listed only while its
// guide lists an operation the person granted it (the snapshot and the
// configuration now, as the server answers them): a turn granted none,
// or whose guide cannot be read, lists no act, and a worker's session
// never does. The listing reads the guide, never the request, which a
// read marks read. Its schema is the table's, its enum the operations
// the table serves.
func TestTheActToolIsListedOnlyWithAGrant(t *testing.T) {
	granted := newGrantServer(t, []string{brakeman.ActLaunchApprovedPlan}, false)
	none := newGrantServer(t, nil, false)
	if got := listedTools(t, brakemanEnv(granted.URL, "s1", "tok")); !slices.Contains(got, "act") {
		t.Errorf("a granted turn's tools: %v", got)
	}
	granted.mu.Lock()
	reads := granted.requestReads
	granted.mu.Unlock()
	if reads != 0 {
		t.Errorf("the listing read the request %d times", reads)
	}
	for name, env := range map[string]func(string) string{
		"a turn granted none":          brakemanEnv(none.URL, "s1", "tok"),
		"a turn the server can't read": brakemanEnv("http://127.0.0.1:1", "s1", "tok"),
		"a worker's session":           sessionEnv(granted.URL, "s1", "tok"),
	} {
		if got := listedTools(t, env); slices.Contains(got, "act") {
			t.Errorf("%s lists act: %v", name, got)
		}
	}
	tl := mcpTools(true, true)
	i := slices.IndexFunc(tl, func(x mcpTool) bool { return x.Name == "act" })
	op, _ := operation("act")
	if i < 0 || !reflect.DeepEqual(tl[i].InputSchema, op.Input) || tl[i].Description != op.Description {
		t.Fatalf("the act tool is not the table's: %+v", tl)
	}
	enum := op.Input["properties"].(map[string]any)["operation"].(map[string]any)["enum"]
	if fmt.Sprint(enum) != fmt.Sprint(actOperations()) || fmt.Sprint(actOperations()) != "[launch_approved_plan request_repair prepare_followup route_decision]" {
		t.Fatalf("the operations: %v, the table's %v", enum, actOperations())
	}
}

// act sends each operation to its own route by the table, with the
// session's token and its name in X-Conductor-Session, the path's
// parameters escaped into the path and the body as given with an opId
// minted when the agent gives none; an operation that is none, a
// parameter the route does not take and one it needs missing are refused
// before anything is sent.
func TestActSendsEachOperationToItsRoute(t *testing.T) {
	g := newGrantServer(t, []string{brakeman.ActLaunchApprovedPlan, brakeman.ActPrepareFollowup, brakeman.ActRequestRepair}, false)
	replies := mcpRun(t, brakemanEnv(g.URL, "s1", "agent-tok"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"act","arguments":{"operation":"launch_approved_plan","params":{"key":"CND-12"},"body":{"plan":"0123456789abcdef","expectedRev":3}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act","arguments":{"operation":"prepare_followup","params":{"key":"CND-12"},"body":{"crew":"team","basedOn":"0123456789abcdef"},"opId":"mine-1"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"act","arguments":{"operation":"ticket_close","params":{"key":"CND-12"}}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"act","arguments":{"operation":"launch_approved_plan","params":{"key":"CND-12","run":"r1"}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"act","arguments":{"operation":"launch_approved_plan","body":{"plan":"0123456789abcdef"}}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"act","arguments":{"operation":"act","params":{"key":"CND-12"}}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"act","arguments":{"operation":"request_repair","params":{"key":"CND-12"},"body":{"candidate":"c","verdict":"request_changes"},"opId":"q-1"}}}`,
	)
	for i, want := range []string{"launch_approved_plan ran (opId ", "prepare_followup ran (opId mine-1): done; its receipt is op:brakeman/bk2, the ref a brief cites it by (an outcome/1's receipt); revision 2 (fedcba9876543210) proposed"} {
		if text, isErr := toolText(t, replies[i]); isErr || !strings.HasPrefix(text, want) {
			t.Errorf("reply %d: %q %v", i, text, isErr)
		}
	}
	if text, _ := toolText(t, replies[0]); !strings.Contains(text, "done; its receipt is op:brakeman/bk1, the ref a brief cites it by (an outcome/1's receipt); launch 0123456789abcdef; run r-1 launched") || !strings.Contains(text, `"receiptRef": "op:brakeman/bk1"`) || !strings.Contains(text, `"runId": "r-1"`) {
		t.Errorf("the launch's words: %q", text)
	}
	for i, want := range []string{"act: operation is one of launch_approved_plan, request_repair, prepare_followup, route_decision", "act launch_approved_plan takes the path parameters key", "act launch_approved_plan needs key", "act: operation is one of"} {
		if text, isErr := toolText(t, replies[2+i]); !isErr || !strings.HasPrefix(text, want) {
			t.Errorf("reply %d: %q %v", 2+i, text, isErr)
		}
	}
	sent := g.sent()
	if len(sent) != 3 {
		t.Fatalf("%d posts: %+v", len(sent), sent)
	}
	launch, follow := sent[0], sent[1]
	if r := sent[2]; r.path != "/api/dispatch/tickets/CND-12/reviews" || r.body["opId"] != "q-1" || r.body["verdict"] != "request_changes" || r.session != "s1" {
		t.Errorf("the request for changes: %+v", r)
	}
	if launch.path != "/api/dispatch/tickets/CND-12/launch" || launch.auth != "Bearer agent-tok" || launch.session != "s1" ||
		launch.body["plan"] != "0123456789abcdef" || launch.body["expectedRev"] != float64(3) || len(launch.body["opId"].(string)) != 16 {
		t.Errorf("the launch: %+v", launch)
	}
	if follow.path != "/api/dispatch/tickets/CND-12/plans" || follow.body["opId"] != "mine-1" || follow.body["basedOn"] != "0123456789abcdef" || follow.session != "s1" {
		t.Errorf("the follow-up: %+v", follow)
	}
}

// An act whose reply is lost to the network is sent once more with the
// same opId and body, which the server answers from the act's receipt.
func TestActResendsTheSameOpIDAfterALostReply(t *testing.T) {
	g := newGrantServer(t, []string{brakeman.ActLaunchApprovedPlan}, true)
	replies := mcpRun(t, brakemanEnv(g.URL, "s1", "agent-tok"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"act","arguments":{"operation":"launch_approved_plan","params":{"key":"CND-12"},"body":{"plan":"0123456789abcdef"}}}}`,
	)
	if text, isErr := toolText(t, replies[0]); isErr || !strings.Contains(text, "run r-1 launched") {
		t.Fatalf("the act: %q %v", text, isErr)
	}
	sent := g.sent()
	if len(sent) != 2 || sent[0].body["opId"] != sent[1].body["opId"] || !reflect.DeepEqual(sent[0].body, sent[1].body) {
		t.Fatalf("the posts: %+v", sent)
	}
}

// actTurn is a Brakeman turn's client on url, as the tool and the
// command make it.
func actTurn(t *testing.T, url string) *brakemanClient {
	t.Helper()
	c, err := brakemanClientFromEnv(brakemanEnv(url, "s1", "agent-tok"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// An act is bounded by its own operation's wait, never by the session
// client's timeout: a launch the server takes past the session client's
// 60 s is answered, not reported failed and launched again. The session
// client's timeout is set below any request's, so an act it bounded
// would fail on every send. Nor by the agent's MCP client: Codex is told
// to wait for a tool longer than the act's whole budget.
func TestAnActIsNotBoundByTheSessionClientsTimeout(t *testing.T) {
	g := newGrantServer(t, []string{brakeman.ActLaunchApprovedPlan}, false)
	c := actTurn(t, g.URL)
	c.client.Timeout = time.Nanosecond
	text, _, _, err := c.act(context.Background(), brakeman.ActLaunchApprovedPlan, map[string]string{"key": "CND-12"}, map[string]any{"plan": "0123456789abcdef"}, "")
	if err != nil || !strings.Contains(text, "run r-1 launched") {
		t.Fatalf("the act: %q %v", text, err)
	}
	if sent := g.sent(); len(sent) != 1 {
		t.Fatalf("%d posts: %+v", len(sent), sent)
	}
	if c.client.Timeout != time.Nanosecond {
		t.Fatalf("the session client was changed: %v", c.client.Timeout)
	}
	if actRequestTimeout(brakeman.ActLaunchApprovedPlan) != launchRequestTimeout || launchRequestTimeout <= api.LaunchTimeout {
		t.Fatalf("a launch's request waits %v, the server's launch %v", actRequestTimeout(brakeman.ActLaunchApprovedPlan), api.LaunchTimeout)
	}
	// A request for changes waits as conductor dispatch review does, within
	// the act's budget.
	if actRequestTimeout(brakeman.ActRequestRepair) != reviewRequestTimeout || reviewRequestTimeout <= api.ReviewTimeout || reviewRequestTimeout > launchRequestTimeout {
		t.Fatalf("a review's request waits %v, the server's review %v", actRequestTimeout(brakeman.ActRequestRepair), api.ReviewTimeout)
	}
	if actBudget() < time.Duration(launchTries)*launchRequestTimeout {
		t.Fatalf("an act's budget %v holds fewer than %d sends", actBudget(), launchTries)
	}
	// The agent's own MCP client waits the act out too: Codex gives a
	// tool 60 s unless told otherwise, and the registration it is
	// launched with tells it agents.MCPToolTimeout, which must hold the
	// whole budget with the reply's way back beside it.
	if agents.MCPToolTimeout < actBudget()+requestTimeout {
		t.Fatalf("Codex waits %v for a tool, an act %v", agents.MCPToolTimeout, actBudget())
	}
}

// sendsTripper is a turn's transport that records each request's body as
// the request reaches it, then answers it with answer (n is the send's
// number, from 1): a send is counted the moment it is made, never only
// once a server has accepted its connection and run its handler, which a
// loaded machine can put past the request's own deadline.
type sendsTripper struct {
	mu     sync.Mutex
	bodies []map[string]any
	answer func(n int, r *http.Request) (*http.Response, error)
}

func (s *sendsTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	var b map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b)
		_ = r.Body.Close()
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, b)
	n := len(s.bodies)
	s.mu.Unlock()
	return s.answer(n, r)
}

func (s *sendsTripper) sent() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

// trippedReply is a reply of status with body, as a server would answer.
func trippedReply(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

// actThrough is a turn's client whose every request goes to tr.
func actThrough(t *testing.T, tr *sendsTripper) *brakemanClient {
	t.Helper()
	c := actTurn(t, "http://conductor.test")
	c.client.Transport = tr
	return c
}

// A launch whose every request outlasts its bound is sent launchTries
// times, the same opId and body each time, and the error names the opId
// to act with again, never advice to mint another, which would be a
// second launch. Each send is recorded as it is made and held until its
// own deadline gives it up, so every one is counted however slow the
// machine.
func TestAnUnansweredActNamesItsOpID(t *testing.T) {
	old := launchRequestTimeout
	launchRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { launchRequestTimeout = old })
	tr := &sendsTripper{answer: func(_ int, r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
	_, _, _, err := actThrough(t, tr).act(context.Background(), brakeman.ActLaunchApprovedPlan, map[string]string{"key": "CND-12"}, map[string]any{"plan": "0123456789abcdef"}, "bk-7")
	if err == nil || !strings.Contains(err.Error(), "act again with opId bk-7") || !strings.Contains(err.Error(), "another opId would be another act") {
		t.Fatalf("the error: %v", err)
	}
	bodies := tr.sent()
	if len(bodies) != launchTries {
		t.Fatalf("%d sends, want %d", len(bodies), launchTries)
	}
	for _, b := range bodies {
		if !reflect.DeepEqual(b, bodies[0]) || b["opId"] != "bk-7" {
			t.Fatalf("the sends: %+v", bodies)
		}
	}
}

// An act whose outcome no receipt has settled names its opId whatever
// ends it: a refusal of a resend after a lost reply (503
// dispatch_unavailable, a 403) says nothing of the first send, and a 5xx
// can come after the act's change was kept. A refusal that settles it
// (409 launch_failed) or one made before anything could have run (a 403
// on the first send) is the refusal alone.
func TestAnActWhoseOutcomeIsUnsureNamesItsOpID(t *testing.T) {
	const (
		unavailable = `{"error":{"code":"dispatch_unavailable","message":"Dispatch is read-only: the disk is full"}}`
		forbidden   = `{"error":{"code":"forbidden","message":"the turn is not granted launch_approved_plan"}}`
		internal    = `{"error":{"code":"internal","message":"the launch could not be answered"}}`
		failed      = `{"error":{"code":"launch_failed","message":"the run could not be started"},"receipt":{"status":"done","id":"0123456789abcdef"},"launch":{"id":"0123456789abcdef","state":"failed"}}`
	)
	lost := func(r *http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }
	refuse := func(status int, body string) func(*http.Request) (*http.Response, error) {
		return func(r *http.Request) (*http.Response, error) { return trippedReply(r, status, body), nil }
	}
	for _, tc := range []struct {
		name    string
		answers []func(*http.Request) (*http.Response, error)
		names   bool
	}{
		{"lost, then 503 dispatch_unavailable", []func(*http.Request) (*http.Response, error){lost, refuse(http.StatusServiceUnavailable, unavailable)}, true},
		{"lost, then 403", []func(*http.Request) (*http.Response, error){lost, refuse(http.StatusForbidden, forbidden)}, true},
		{"a 500 on the first send", []func(*http.Request) (*http.Response, error){refuse(http.StatusInternalServerError, internal)}, true},
		{"a 503 dispatch_unavailable on the first send", []func(*http.Request) (*http.Response, error){refuse(http.StatusServiceUnavailable, unavailable)}, true},
		{"lost, then 409 launch_failed", []func(*http.Request) (*http.Response, error){lost, refuse(http.StatusConflict, failed)}, false},
		{"a 403 on the first send", []func(*http.Request) (*http.Response, error){refuse(http.StatusForbidden, forbidden)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &sendsTripper{answer: func(n int, r *http.Request) (*http.Response, error) {
				if n > len(tc.answers) {
					t.Errorf("send %d, after the last answer", n)
					return nil, io.ErrUnexpectedEOF
				}
				return tc.answers[n-1](r)
			}}
			_, _, _, err := actThrough(t, tr).act(context.Background(), brakeman.ActLaunchApprovedPlan, map[string]string{"key": "CND-12"}, map[string]any{"plan": "0123456789abcdef"}, "")
			bodies := tr.sent()
			if err == nil || len(bodies) != len(tc.answers) {
				t.Fatalf("%d sends: %v", len(bodies), err)
			}
			opID, _ := bodies[0]["opId"].(string)
			for _, b := range bodies {
				if !reflect.DeepEqual(b, bodies[0]) || len(opID) != 16 {
					t.Fatalf("the sends: %+v", bodies)
				}
			}
			named := strings.Contains(err.Error(), "act again with opId "+opID) && strings.Contains(err.Error(), "another opId would be another act")
			if named != tc.names || !strings.HasPrefix(err.Error(), "act launch_approved_plan: ") {
				t.Fatalf("the error names the opId %v, want %v: %v", named, tc.names, err)
			}
		})
	}
}

// A launch the server answers still being started (503
// ledger_unavailable naming it pending) is sent again with the same opId
// after launchResendWait, as conductor dispatch launch sends it, and its
// later answer is the act's; still pending after launchTries, the error
// names the launch and the opId to act with again.
func TestAnActResendsALaunchStillBeingStarted(t *testing.T) {
	wait := launchResendWait
	launchResendWait = 0
	t.Cleanup(func() { launchResendWait = wait })
	for _, tc := range []struct {
		name    string
		pending int
	}{{"pending once", 1}, {"pending throughout", launchTries}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				mu.Lock()
				bodies = append(bodies, b)
				n := len(bodies)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n <= tc.pending {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"code":"ledger_unavailable","message":"the launch is still being started: send it again"},"receipt":{"status":"done","id":"0123456789abcdef"},"launch":{"id":"0123456789abcdef","state":"pending"}}`)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"receipt":{"status":"done","id":"0123456789abcdef","repeat":true},"launch":{"id":"0123456789abcdef","runId":"r-1","state":"started"}}`)
			}))
			t.Cleanup(srv.Close)
			text, _, _, err := actTurn(t, srv.URL).act(context.Background(), brakeman.ActLaunchApprovedPlan, map[string]string{"key": "CND-12"}, map[string]any{"plan": "0123456789abcdef"}, "")
			mu.Lock()
			defer mu.Unlock()
			opID, _ := bodies[0]["opId"].(string)
			for _, b := range bodies {
				if !reflect.DeepEqual(b, bodies[0]) {
					t.Fatalf("the sends: %+v", bodies)
				}
			}
			if tc.pending < launchTries {
				if err != nil || len(bodies) != tc.pending+1 || !strings.Contains(text, "run r-1 launched") {
					t.Fatalf("%d sends: %q %v", len(bodies), text, err)
				}
				return
			}
			if err == nil || len(bodies) != launchTries || !strings.Contains(err.Error(), "launch 0123456789abcdef is still being started after 3 tries") ||
				!strings.Contains(err.Error(), "act again with opId "+opID) {
				t.Fatalf("%d sends: %v", len(bodies), err)
			}
		})
	}
}

// The four decision tools are listed in an attempt's session of a managed
// run alone (CONDUCTOR_TASK set), each the table's; a session that is no
// attempt, or a Brakeman turn's, lists none of them, and the rest of its
// list is unchanged.
func TestMCPListsDecisionToolsOnlyInATask(t *testing.T) {
	decisions := []string{"decision_request", "decision_list", "decision_read", "decision_withdraw"}
	env := func(task string) func(string) string {
		return func(k string) string {
			if k == "CONDUCTOR_TASK" {
				return task
			}
			return ""
		}
	}
	in, out := listedTools(t, env("build")), listedTools(t, env(""))
	for _, name := range decisions {
		if !slices.Contains(in, name) {
			t.Errorf("an attempt's session does not list %s: %v", name, in)
		}
		if slices.Contains(out, name) {
			t.Errorf("a session that is no attempt lists %s", name)
		}
	}
	if len(in) != len(out)+len(decisions) || !slices.Equal(slices.DeleteFunc(slices.Clone(in), func(n string) bool { return taskOnly[n] }), out) {
		t.Fatalf("an attempt's list %v, another's %v", in, out)
	}
	if turn := listedTools(t, brakemanEnv("http://127.0.0.1:1", "s1", "tok")); slices.ContainsFunc(turn, func(n string) bool { return taskOnly[n] }) {
		t.Fatalf("a Brakeman turn lists a decision tool: %v", turn)
	}
}

// decision_request is one call to its route with a requestId minted when
// the agent gives none, the same id sent again when the first reply was
// lost; it answers the decision's id and state with what to do while it
// waits, never waiting for the answer.
func TestDecisionRequestToolAnswersPending(t *testing.T) {
	var (
		mu   sync.Mutex
		ids  []string
		lost = true
	)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/s1/decisions" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		id, _ := body["requestId"].(string)
		ids = append(ids, id)
		drop := lost
		lost = false
		mu.Unlock()
		if drop {
			// The first reply is lost: the connection closes unanswered.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"decision":{"id":"dr-7","state":"open","question":"Ship it?"}}`))
	}))
	defer hs.Close()
	env := map[string]string{"CONDUCTOR_SESSION_ID": "s1", "CONDUCTOR_NOTIFY_URL": hs.URL + "/api/sessions/s1/attention", "CONDUCTOR_NOTIFY_TOKEN": "tok", "CONDUCTOR_TASK": "build"}
	s := &mcpServer{getenv: func(k string) string { return env[k] }}
	text, err := s.call(t.Context(), "decision_request", json.RawMessage(`{"type":"approval","question":"Ship it?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !hasAll(text, "dr-7 open", "Pending.", "do not submit your result until its answer is applied", "Decision dr-7", "task file", "save one before you end your turn") {
		t.Fatalf("the reply: %q", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("the request ids sent: %q", ids)
	}
	// A read and a withdrawal name the decision's id, or are refused before anything is sent.
	for _, name := range []string{"decision_read", "decision_withdraw"} {
		if _, err := s.call(t.Context(), name, json.RawMessage(`{"id":"7"}`)); err == nil || !strings.Contains(err.Error(), "dr-N") {
			t.Fatalf("%s of a bad id: %v", name, err)
		}
	}
	// A repeat answers the decision as it is now, in words for its state:
	// only an open one is pending, and only an applied one's answer is the
	// agent's to go on with: an answered one is recorded, not yet applied
	// (#637, review round 2).
	for _, c := range []struct {
		decision string
		want     []string
		not      string
	}{
		{`{"id":"dr-7","state":"answered","answer":"Approve"}`, []string{"dr-7 is answered: Approve", "recorded, not yet applied", "Do not act on it yet", "do not submit your result until it is applied"}, "Go on"},
		{`{"id":"dr-7","state":"applied","answer":"Approve"}`, []string{"dr-7 is applied: Approve", "Go on with your task with this answer"}, "Pending"},
		{`{"id":"dr-7","state":"superseded","why":"dr-9 takes its place","supersededBy":"dr-9"}`, []string{"dr-7 is superseded by dr-9", "Wait for dr-9's answer"}, "Pending"},
		{`{"id":"dr-7","state":"cancelled","why":"cancelled by the operator, not approved"}`, []string{"dr-7 is cancelled: cancelled by the operator", "will not be answered"}, "Pending"},
	} {
		rs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"decision":` + c.decision + `}`))
		}))
		env := map[string]string{"CONDUCTOR_SESSION_ID": "s1", "CONDUCTOR_NOTIFY_URL": rs.URL + "/api/sessions/s1/attention", "CONDUCTOR_NOTIFY_TOKEN": "tok", "CONDUCTOR_TASK": "build"}
		rsv := &mcpServer{getenv: func(k string) string { return env[k] }}
		text, err := rsv.call(t.Context(), "decision_request", json.RawMessage(`{"requestId":"q1","type":"approval","question":"Ship it?"}`))
		rs.Close()
		if err != nil || !hasAll(text, c.want...) || strings.Contains(text, c.not) {
			t.Fatalf("the repeat of %s: %q %v", c.decision, text, err)
		}
	}
}

// decision_request takes for (docs/plans/decisions.md, HM1, PR-12): its
// schema names it, a string, and a call sends it as given, whom the
// decision is for; the server resolves it.
func TestDecisionRequestToolTakesFor(t *testing.T) {
	env := func(k string) string {
		if k == "CONDUCTOR_TASK" {
			return "build"
		}
		return ""
	}
	res, _ := mcpRun(t, env, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)[0]["result"].(map[string]any)
	var schema map[string]any
	for _, x := range res["tools"].([]any) {
		if tool := x.(map[string]any); tool["name"] == "decision_request" {
			schema = tool["inputSchema"].(map[string]any)
		}
	}
	prop, _ := schema["properties"].(map[string]any)["for"].(map[string]any)
	if prop["type"] != "string" || !strings.Contains(fmt.Sprint(prop["description"]), "role:RESP or person:NAME") {
		t.Fatalf("decision_request's for: %v", prop)
	}
	var sent atomic.Value
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent.Store(fmt.Sprint(body["for"]))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"decision":{"id":"dr-7","state":"open","question":"Ship it?"}}`))
	}))
	defer hs.Close()
	vars := map[string]string{"CONDUCTOR_SESSION_ID": "s1", "CONDUCTOR_NOTIFY_URL": hs.URL + "/api/sessions/s1/attention", "CONDUCTOR_NOTIFY_TOKEN": "tok", "CONDUCTOR_TASK": "build"}
	s := &mcpServer{getenv: func(k string) string { return vars[k] }}
	if _, err := s.call(t.Context(), "decision_request", json.RawMessage(`{"type":"clarification","question":"Which schema?","for":"role:architecture"}`)); err != nil {
		t.Fatal(err)
	}
	if got := sent.Load(); got != "role:architecture" {
		t.Fatalf("the body's for: %v", got)
	}
}
