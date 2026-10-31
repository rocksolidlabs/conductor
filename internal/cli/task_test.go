package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTaskServer answers the task routes of session s1: a task of two
// pages, and a result route that refuses a summary of "refuse" as the
// server refuses a dirty worktree.
func fakeTaskServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/s1/task", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "page": r.URL.Query().Get("page"), "auth": r.Header.Get("Authorization")})
		switch r.URL.Query().Get("page") {
		case "", "1":
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"task": map[string]any{"id": "build"}}, "markdown": "# Task build\n\nBuild it.\n", "page": 1, "pages": 2, "bytes": 30})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"task": map[string]any{"id": "build"}}, "markdown": "More.\n", "page": 2, "pages": 2, "bytes": 30})
		default:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"the task has 2 pages"}}`))
		}
	})
	mux.HandleFunc("POST /api/sessions/s1/task/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "auth": r.Header.Get("Authorization"), "body": body})
		if body["summary"] == "refuse" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"dirty_worktree","message":"the worktree has changes not committed: commit them, then submit (a.txt)"}}`))
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "build", "state": "verifying"}})
	})
	mux.HandleFunc("POST /api/sessions/s1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "auth": r.Header.Get("Authorization"), "body": body})
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"seq": 7, "from": "lead", "to": body["to"], "text": body["text"]}})
	})
	mux.HandleFunc("GET /api/sessions/s1/inbox", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "after": r.URL.Query().Get("after"), "auth": r.Header.Get("Authorization")})
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{
			map[string]any{"seq": 1, "from": "lead", "to": "docs", "text": "the API is final", "deliveredAt": "2026-10-23T10:00:00Z"},
			map[string]any{"seq": 2, "from": "critic", "to": "docs", "text": "one more thing"},
		}})
	})
	mux.HandleFunc("POST /api/sessions/s1/inbox/ack", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "body": body})
		_ = json.NewEncoder(w).Encode(map[string]any{"acked": 2})
	})
	mux.HandleFunc("POST /api/sessions/s1/task/blocked", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "body": body})
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "build", "blocker": body}})
	})
	mux.HandleFunc("POST /api/sessions/s1/task/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"method": r.Method, "path": r.URL.Path, "body": body})
		_ = json.NewEncoder(w).Encode(map[string]any{"attempt": map[string]any{"n": 1, "checkpoint": map[string]any{"commit": "0123456789abcdef0123456789abcdef01234567"}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

// task send, inbox, ack, blocked and checkpoint post what their flags say
// and print the server's answer; their usage errors send nothing.
func TestTaskMessagesBlockedAndCheckpoint(t *testing.T) {
	srv, seen := fakeTaskServer(t)
	inSession(t, srv)
	code, out, err := task(t, "", "send", "--to", "docs", "the", "API", "is", "final")
	if code != 0 || err != nil || out != "sent #7 to docs\n" {
		t.Fatalf("send: %d %v %q", code, err, out)
	}
	if body := (*seen)[0]["body"].(map[string]any); body["to"] != "docs" || body["text"] != "the API is final" || (*seen)[0]["auth"] != "Bearer agent-tok" {
		t.Fatalf("send's body: %v", (*seen)[0])
	}
	code, out, err = task(t, "", "inbox", "--after", "1")
	if code != 0 || err != nil || (*seen)[1]["after"] != "1" {
		t.Fatalf("inbox: %d %v %v", code, err, (*seen)[1])
	}
	if want := "#1 from lead (typed into your session): the API is final\n#2 from critic (waiting to be typed into your session): one more thing\n"; out != want {
		t.Fatalf("inbox:\n%q\nwant\n%q", out, want)
	}
	if code, out, _ := task(t, "", "inbox", "--json"); code != 0 || !strings.Contains(out, `"seq": 2`) {
		t.Fatalf("inbox --json: %d %q", code, out)
	}
	if code, out, err := task(t, "", "ack", "2"); code != 0 || err != nil || out != "acknowledged 2\n" || (*seen)[3]["body"].(map[string]any)["upTo"] != float64(2) {
		t.Fatalf("ack: %d %v %q %v", code, err, out, (*seen)[3])
	}
	if code, out, err := task(t, "", "blocked", "--reason", "no token", "--message", "set NPM_TOKEN"); code != 0 || err != nil || !strings.Contains(out, "task build shows as blocked") {
		t.Fatalf("blocked: %d %v %q", code, err, out)
	}
	if body := (*seen)[4]["body"].(map[string]any); body["reason"] != "no token" || body["message"] != "set NPM_TOKEN" {
		t.Fatalf("blocked's body: %v", body)
	}
	if code, out, err := task(t, "", "checkpoint", "--done", "handlers", "--remaining", "docs", "--notes", "see docs/api.md"); code != 0 || err != nil || out != "checkpoint saved at 0123456789abcdef0123456789abcdef01234567\n" {
		t.Fatalf("checkpoint: %d %v %q", code, err, out)
	}
	if body := (*seen)[5]["body"].(map[string]any); body["done"] != "handlers" || body["remaining"] != "docs" || body["notes"] != "see docs/api.md" {
		t.Fatalf("checkpoint's body: %v", body)
	}
	for _, args := range [][]string{
		{"send", "hello"},             // no --to
		{"send", "--to", "docs"},      // no text
		{"ack"},                       // no number
		{"ack", "x"},                  // not a number
		{"blocked"},                   // no reason
		{"checkpoint", "--done", "d"}, // no remaining
		{"inbox", "extra"},
	} {
		if code, _, _ := task(t, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if n := len(*seen); n != 6 {
		t.Fatalf("%d requests after the usage errors, want 6", n)
	}
}

func task(t *testing.T, stdin string, args ...string) (int, string, error) {
	t.Helper()
	var out strings.Builder
	code, err := runTask(context.Background(), args, strings.NewReader(stdin), &out, io.Discard)
	return code, out.String(), err
}

// task context prints every page of the task in order, one page when
// asked, or the task's JSON, with the session's token.
func TestTaskContextPrintsTheTask(t *testing.T) {
	srv, seen := fakeTaskServer(t)
	inSession(t, srv)
	code, out, err := task(t, "", "context")
	if code != 0 || err != nil || out != "# Task build\n\nBuild it.\nMore.\n" {
		t.Fatalf("context: %d %v %q", code, err, out)
	}
	if len(*seen) != 2 || (*seen)[0]["auth"] != "Bearer agent-tok" || (*seen)[1]["page"] != "2" {
		t.Fatalf("seen %v", *seen)
	}
	if code, out, _ := task(t, "", "context", "--page", "2"); code != 0 || out != "More.\n" {
		t.Fatalf("page 2: %d %q", code, out)
	}
	if code, out, _ := task(t, "", "context", "--json"); code != 0 || !strings.Contains(out, `"id": "build"`) {
		t.Fatalf("json: %d %q", code, out)
	}
	if code, _, err := task(t, "", "context", "--page", "3"); code != 1 || err == nil || !strings.Contains(err.Error(), "task: the task has 2 pages (invalid_request)") {
		t.Fatalf("page 3: %d %v", code, err)
	}
}

// task result posts the flags as the result, findings parsed from their
// words, or the JSON of --file; the server's refusal is the exit.
func TestTaskResultSubmitsTheFlagsOrTheFile(t *testing.T) {
	srv, seen := fakeTaskServer(t)
	inSession(t, srv)
	commit := strings.Repeat("ab", 20)
	code, out, err := task(t, "", "result", "--summary", "did it", "--issue", "a", "--issue", "b", "--commit", commit)
	if code != 0 || err != nil || out != "submitted: task build is verifying\n" {
		t.Fatalf("result: %d %v %q", code, err, out)
	}
	body := (*seen)[0]["body"].(map[string]any)
	if body["summary"] != "did it" || body["commit"] != commit || len(body["issues"].([]any)) != 2 || body["verdict"] != nil || body["findings"] != nil {
		t.Fatalf("body %v", body)
	}
	code, _, err = task(t, "", "result", "--verdict", "needs_changes", "--finding", "major:x.go:12 fix it", "--finding", "nit say more", "--finding", "minor:README no line")
	if code != 0 || err != nil {
		t.Fatalf("review: %d %v", code, err)
	}
	body = (*seen)[1]["body"].(map[string]any)
	findings := body["findings"].([]any)
	if body["verdict"] != "needs_changes" || body["summary"] != nil || len(findings) != 3 {
		t.Fatalf("body %v", body)
	}
	if f := findings[0].(map[string]any); f["severity"] != "major" || f["path"] != "x.go" || f["line"] != float64(12) || f["message"] != "fix it" {
		t.Fatalf("finding 1: %v", f)
	}
	if f := findings[1].(map[string]any); f["severity"] != "nit" || f["path"] != nil || f["line"] != nil || f["message"] != "say more" {
		t.Fatalf("finding 2: %v", f)
	}
	if f := findings[2].(map[string]any); f["severity"] != "minor" || f["path"] != "README" || f["line"] != nil {
		t.Fatalf("finding 3: %v", f)
	}
	code, _, err = task(t, `{"summary":"from the file","issues":["x"]}`, "result", "--file", "-")
	if code != 0 || err != nil {
		t.Fatalf("file: %d %v", code, err)
	}
	if body = (*seen)[2]["body"].(map[string]any); body["summary"] != "from the file" {
		t.Fatalf("body %v", body)
	}
	// Usage errors, before anything is sent.
	for _, args := range [][]string{
		{"result", "--file", "-", "--summary", "both"},
		{"result", "--finding", "nowords"},
		{"result", "--finding", "huge:x.go:1 severity unknown"},
		{"result", "--finding", "major:x.go:zero bad line"},
		{"result", "--file", "-"}, // stdin empty below
		{"result", "extra"},
		{"nope"},
		{},
	} {
		if code, _, _ := task(t, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, err := task(t, `{"summary":"x","nope":1}`, "result", "--file", "-"); code != 2 || err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown field: %d %v", code, err)
	}
	if n := len(*seen); n != 3 {
		t.Fatalf("%d requests after the usage errors, want 3", n)
	}
	// The server's refusal is exit 1 with its words.
	if code, _, err := task(t, "", "result", "--summary", "refuse"); code != 1 || err == nil || !strings.Contains(err.Error(), "task: the worktree has changes not committed: commit them, then submit (a.txt) (dirty_worktree)") {
		t.Fatalf("refused: %d %v", code, err)
	}
	if code, _, _ := task(t, "", "-h"); code != 0 {
		t.Fatal("-h")
	}
}

// Outside a Conductor session the task commands say so and exit 1: an
// agent that calls them by mistake must see it, unlike crew's silence.
func TestTaskSaysSoOutsideASession(t *testing.T) {
	for _, k := range []string{"CONDUCTOR_SESSION_ID", "CONDUCTOR_NOTIFY_URL", "CONDUCTOR_NOTIFY_TOKEN"} {
		t.Setenv(k, "")
	}
	for _, args := range [][]string{{"context"}, {"result", "--summary", "x"}} {
		code, out, err := task(t, "", args...)
		if code != 1 || out != "" || err == nil || !strings.Contains(err.Error(), "not in a Conductor session") {
			t.Errorf("%v: %d %q %v", args, code, out, err)
		}
	}
}

// inSessionOf sets the environment of session sid of the server at url,
// with its agent token, as Conductor injects it.
func inSessionOf(t *testing.T, url, sid, token string) {
	t.Helper()
	t.Setenv("CONDUCTOR_SESSION_ID", sid)
	t.Setenv("CONDUCTOR_NOTIFY_URL", url+"/api/sessions/"+sid+"/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", token)
}

// conductor task ticket against a real server, with the session's own
// token: no ticket before the run is linked to one; the ticket once it
// is, as Markdown and as JSON; a comment as the run's member, sent once
// more with the same operation id when its reply is lost, and taken once;
// no ticket again once the ticket is deleted.
func TestTaskTicketAgainstTheServer(t *testing.T) {
	a := newDispatchAPI(t)
	run, sid, token := a.launchMember(t)
	inSessionOf(t, a.hs.URL, sid, token)
	if code, out, err := task(t, "", "ticket"); code != 1 || out != "" || err == nil || !strings.HasSuffix(err.Error(), "(no_ticket)") {
		t.Fatalf("before a link: %d %q %v", code, out, err)
	}
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	a.mustDispatch(t, "create", "--project", "CND", "--title", "Ship the <thing>")
	a.mustDispatch(t, "link", "CND-1", "--kind", "run", "--ref", run)
	code, out, err := task(t, "", "ticket")
	if code != 0 || err != nil || !strings.HasPrefix(out, "# CND-1: Ship the <thing>\n\n- status: backlog\n") || !strings.Contains(out, "## Links (1)\n\n- run "+run+"\n") {
		t.Fatalf("task ticket: %d %v\n%s", code, err, out)
	}
	var st sessionTicket
	if code, out, err := task(t, "", "ticket", "--json"); code != 0 || err != nil || json.Unmarshal([]byte(out), &st) != nil || st.Ticket.Key != "CND-1" || st.Labels == nil {
		t.Fatalf("task ticket --json: %d %v %q", code, err, out)
	}
	if code, out, err := task(t, "", "ticket", "comment", "seen", "<it>"); code != 0 || err != nil || out != "comment #1 on your ticket\n" {
		t.Fatalf("task ticket comment: %d %v %q", code, err, out)
	}
	before := a.count()
	a.loseNext("POST", "/api/sessions/"+sid+"/ticket/comments", 1)
	if code, out, err := task(t, "", "ticket", "comment", "once"); code != 0 || err != nil || out != "comment #2 on your ticket\n" {
		t.Fatalf("a comment whose reply was lost: %d %v %q", code, err, out)
	}
	if posts := a.requests(before); len(posts) != 2 || posts[0].reply != -1 || posts[0].body["opId"] != posts[1].body["opId"] {
		t.Fatalf("the comment's posts: %+v", posts)
	}
	cs := a.ticket(t, "CND-1").Comments
	if len(cs) != 2 || cs[0].Author != "agent:a" || cs[0].Body != "seen <it>" || cs[1].Author != "agent:a" || cs[1].Body != "once" {
		t.Fatalf("the ticket's comments: %+v", cs)
	}
	for _, args := range [][]string{{"ticket", "nope"}, {"ticket", "comment"}, {"ticket", "comment", " "}} {
		if code, _, err := task(t, "", args...); code != 2 || err == nil {
			t.Errorf("%q: %d %v, want a usage error", args, code, err)
		}
	}
	a.admin(t, "POST", "/api/dispatch/tickets/CND-1/delete", map[string]any{"opId": "d1"}, http.StatusOK)
	if code, _, err := task(t, "", "ticket"); code != 1 || err == nil || !strings.HasSuffix(err.Error(), "(no_ticket)") {
		t.Fatalf("after the delete: %d %v", code, err)
	}
	if code, _, err := task(t, "", "ticket", "comment", "late"); code != 1 || err == nil || !strings.HasSuffix(err.Error(), "(no_ticket)") {
		t.Fatalf("a comment after the delete: %d %v", code, err)
	}
}

// conductor task ticket takes the largest reply the session's read can
// make, the largest ticket with its labels and milestone padded to the
// server's bound, through the session client's limit, and prints it
// whole; a reply past the limit is refused with words.
func TestTaskTicketPrintsTheLargestTicketWhole(t *testing.T) {
	clearConductorEnv(t)
	l := largestFor(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/s1/ticket", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(l.session) })
	mux.HandleFunc("GET /api/sessions/s2/ticket", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte(" "), maxSessionReply+1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	inSessionOf(t, srv.URL, "s1", "agent-tok")
	code, out, err := task(t, "", "ticket")
	if code != 0 || err != nil {
		t.Fatalf("task ticket: %d %v", code, err)
	}
	checkWhole(t, out, l.view, l.labels, l.milestone)
	inSessionOf(t, srv.URL, "s2", "agent-tok")
	if code, _, err := task(t, "", "ticket"); code != 1 || err == nil || err.Error() != "task: the server's reply is larger than 4 MiB" {
		t.Fatalf("a reply past the limit: %d %v", code, err)
	}
}

// A comment made whose reply carries no comment, the server unable to
// read it back after its commit (comment null beside a done receipt), is
// reported made, without a number it does not have.
func TestTaskTicketCommentWithoutTheCommentRead(t *testing.T) {
	clearConductorEnv(t)
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
	inSessionOf(t, srv.URL, "s1", "agent-tok")
	if code, out, err := task(t, "", "ticket", "comment", "seen"); code != 0 || err != nil || out != "commented on your ticket\n" {
		t.Fatalf("a comment with no comment read: %d %v %q", code, err, out)
	}
}
