package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// conductor dispatch, conductor task ticket and the ticket tools
// (docs/plans/dispatch.md, PR-12): against a real server, which takes
// what the routes take and nothing else, and against stand-ins for what a
// real one cannot make cheaply (a reply at its bound, two full pages).

// dispatchToken is the workbench token of the tests' real servers.
const dispatchToken = "dispatch-admin-token"

// seenRequest is a request a test server saw: its method, path and query,
// its body decoded, and how many bytes its reply was (-1 for one lost).
type seenRequest struct {
	method, path, query string
	body                map[string]any
	reply               int
}

// dispatchAPI is a real server for the CLI's tests: its Dispatch store
// open in a data directory of the test's, the workbench token
// dispatchToken, a catalog of one agent, cat, that runs /bin/cat. Its
// handler records every request and the size of every reply, and loses
// the next losing replies of the request lose names ("METHOD path"): the
// server acts on each whole, and the connection is then cut before the
// reply, as a network that fails after the server acted.
type dispatchAPI struct {
	srv    *api.Server
	h      http.Handler
	hs     *httptest.Server
	root   string
	mu     sync.Mutex
	seen   []seenRequest
	lose   string
	losing int
}

// countingWriter counts the bytes of a reply.
type countingWriter struct {
	http.ResponseWriter
	n int
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += n
	return n, err
}

// newDispatchAPI starts a real server for t, ended with it.
func newDispatchAPI(t *testing.T) *dispatchAPI {
	t.Helper()
	clearConductorEnv(t)
	root := realTempDir(t)
	cfg := config.Defaults()
	cfg.WorkbenchToken = dispatchToken
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, cat, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	a := &dispatchAPI{srv: srv, h: srv.Handler(), root: root}
	a.hs = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(func() {
		a.hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		srv.WaitBackground(ctx)
	})
	return a
}

// serve records the request and serves it, or loses its reply when it is
// the one lose names. A request is recorded before its client can act on
// the answer: a lost one before its connection is closed, which wakes the
// client to resend, and a served one before the handler returns, which
// the reply's end waits for, so the records are in the order the client
// sent them.
func (a *dispatchAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	a.mu.Lock()
	lost := a.losing > 0 && a.lose == r.Method+" "+r.URL.Path
	if lost {
		a.losing--
	}
	a.mu.Unlock()
	seen := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: decoded}
	record := func() {
		a.mu.Lock()
		a.seen = append(a.seen, seen)
		a.mu.Unlock()
	}
	if lost {
		a.h.ServeHTTP(httptest.NewRecorder(), r)
		seen.reply = -1
		record()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
		return
	}
	cw := &countingWriter{ResponseWriter: w}
	a.h.ServeHTTP(cw, r)
	seen.reply = cw.n
	record()
}

// loseNext arms the loss of the next n replies to "METHOD path".
func (a *dispatchAPI) loseNext(method, path string, n int) {
	a.mu.Lock()
	a.lose, a.losing = method+" "+path, n
	a.mu.Unlock()
}

// requests is what the server saw, from the n-th request on.
func (a *dispatchAPI) requests(from int) []seenRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen[from:])
}

// count is how many requests the server saw.
func (a *dispatchAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.seen)
}

// call sends a request to the server with the token given, its body as
// JSON, and fails the test unless the reply has the status; it returns
// the reply decoded.
func (a *dispatchAPI) call(t *testing.T, token, method, path string, body any, status int) map[string]any {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.hs.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s: %d %.300s, want %d", method, path, resp.StatusCode, raw, status)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return out
}

// admin is call with the workbench token.
func (a *dispatchAPI) admin(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	return a.call(t, dispatchToken, method, path, body, status)
}

// ticket reads a ticket through the admin's route.
func (a *dispatchAPI) ticket(t *testing.T, key string) dispatch.TicketView {
	t.Helper()
	b, err := json.Marshal(a.admin(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK)["ticket"])
	if err != nil {
		t.Fatal(err)
	}
	var v dispatch.TicketView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// dispatchCmd runs conductor dispatch sub against the server, its server
// and token given as flags after the command, the rest after them.
func (a *dispatchAPI) dispatchCmd(t *testing.T, stdin string, sub string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{sub, "--server", a.hs.URL, "--token", dispatchToken}, args...)
	code, err := runDispatch(t.Context(), full, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String(), err
}

// mustDispatch runs dispatchCmd and fails the test unless it exits 0.
func (a *dispatchAPI) mustDispatch(t *testing.T, sub string, args ...string) string {
	t.Helper()
	code, out, errOut, err := a.dispatchCmd(t, "", sub, args...)
	if code != 0 || err != nil {
		t.Fatalf("dispatch %s %q: exit %d %v\n%s", sub, args, code, err, errOut)
	}
	return out
}

// launchMember launches a crew of one member, a, running cat, in the
// server's root, and waits for its session: the run, the session, and an
// agent token the session is given, as its process gets one.
func (a *dispatchAPI) launchMember(t *testing.T) (run, sid, token string) {
	t.Helper()
	crew := a.admin(t, "POST", "/api/crews", map[string]any{
		"name": "Ticketed", "goal": "ship", "cwd": a.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "a", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "immediately"}}},
	}, http.StatusCreated)["crew"].(map[string]any)["id"].(string)
	run = a.admin(t, "POST", "/api/crews/"+crew+"/launch", nil, http.StatusCreated)["run"].(map[string]any)["id"].(string)
	// The member's session exists once the launch has answered; its id
	// is on the run as soon as the engine records it, which the test
	// waits for, as long as the test may take.
	for sid == "" {
		members, _ := a.admin(t, "GET", "/api/runs/"+run, nil, http.StatusOK)["run"].(map[string]any)["members"].([]any)
		if len(members) == 1 {
			sid, _ = members[0].(map[string]any)["sessionId"].(string)
		}
		if sid == "" {
			if t.Context().Err() != nil {
				t.Fatal("the member's session never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	d, ok := a.srv.Registry().Get(sid)
	local, isLocal := d.(*session.Local)
	if !ok || !isLocal {
		t.Fatalf("session %s is not a server session", sid)
	}
	token = "agent-token-" + sid
	local.SetAgentToken(token)
	return run, sid, token
}

// The read and write commands against a real server: tickets made with
// their fields, labels and the milestone by name; listed whole, filtered,
// as JSON; edited in one operation, adding and removing members and
// clearing fields; commented on, accepted, linked; shown whole; the
// projects listed with their counts; a refusal's words; and the server's
// pages followed to the end, a page of one at a time.
func TestDispatchCommandsAgainstTheServer(t *testing.T) {
	a := newDispatchAPI(t)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	labels := map[string]string{}
	for _, name := range []string{"api", "docs"} {
		labels[name] = a.admin(t, "POST", "/api/dispatch/projects/CND/labels", map[string]any{"opId": "l-" + name, "name": name, "color": "blue"}, http.StatusCreated)["label"].(map[string]any)["id"].(string)
	}
	milestone := a.admin(t, "POST", "/api/dispatch/projects/CND/milestones", map[string]any{"opId": "m1", "name": "1.0"}, http.StatusCreated)["milestone"].(map[string]any)["id"].(string)

	// create: the description from a file (HTML escaping off on the way),
	// the priority by its word, a label by its name; prints the key.
	desc := filepath.Join(t.TempDir(), "desc.md")
	if err := os.WriteFile(desc, []byte("the <b>work</b> & more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := a.count()
	if out := a.mustDispatch(t, "create", "--project", "CND", "--title", "Try the CLI", "--description-file", desc, "--priority", "high", "--owner", "Alice", "--label", "api"); out != "CND-1\n" {
		t.Fatalf("create: %q", out)
	}
	sent := a.requests(before)
	var create seenRequest
	for _, r := range sent {
		if r.method == "POST" {
			create = r
		}
	}
	if b := create.body; create.path != "/api/dispatch/tickets" || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(fmt.Sprint(b["opId"])) ||
		b["project"] != "CND" || b["title"] != "Try the CLI" || b["description"] != "the <b>work</b> & more\n" || b["priority"] != 3.0 ||
		b["owner"] != "Alice" || fmt.Sprint(b["labels"]) != "["+labels["api"]+"]" {
		t.Fatalf("create's body: %+v", create)
	}
	if out := a.mustDispatch(t, "create", "--title", "Second", "--project", "CND"); out != "CND-2\n" {
		t.Fatalf("second create: %q", out)
	}

	// tickets: the whole list, a line each; filtered; as JSON.
	lines := strings.Split(strings.TrimSpace(a.mustDispatch(t, "tickets")), "\n")
	slices.Sort(lines)
	if want := []string{"CND-1\tbacklog\thigh\tAlice\tTry the CLI", "CND-2\tbacklog\tnone\t-\tSecond"}; !slices.Equal(lines, want) {
		t.Fatalf("tickets:\n%q\nwant\n%q", lines, want)
	}
	if out := a.mustDispatch(t, "tickets", "--project", "CND", "--status", "backlog", "--status", "ready", "--q", "Try"); out != "CND-1\tbacklog\thigh\tAlice\tTry the CLI\n" {
		t.Fatalf("tickets filtered: %q", out)
	}
	var listed []dispatch.TicketSummary
	if err := json.Unmarshal([]byte(a.mustDispatch(t, "tickets", "--json", "--q", "Second")), &listed); err != nil || len(listed) != 1 || listed[0].Key != "CND-2" {
		t.Fatalf("tickets --json: %+v %v", listed, err)
	}
	if out := a.mustDispatch(t, "tickets", "--status", "done"); out != "no tickets\n" {
		t.Fatalf("no tickets: %q", out)
	}

	// edit: every field and set in one operation, names to ids.
	if out := a.mustDispatch(t, "edit", "CND-1", "--status", "in_progress", "--priority", "4", "--owner", "Bob", "--assignee", "+Carol", "--assignee", "+agent:claude",
		"--label", "+docs", "--label", "-api", "--milestone", "1.0", "--horizon", "now"); out != "CND-1\tin_progress\turgent\tBob\tTry the CLI\n" {
		t.Fatalf("edit: %q", out)
	}
	tk := a.ticket(t, "CND-1")
	if tk.Status != "in_progress" || tk.Priority != 4 || tk.Owner != "Bob" || !slices.Equal(tk.Assignees, []string{"agent:claude", "person:Carol"}) ||
		!slices.Equal(tk.Labels, []string{labels["docs"]}) || tk.MilestoneID != milestone || tk.Horizon != "now" || tk.Rev != 2 {
		t.Fatalf("after the edit: %+v", tk.Ticket)
	}
	// accept, while it has an owner; comment; link.
	if out := a.mustDispatch(t, "accept", "CND-1", "--note", "checked by hand"); out != "CND-1 accepted for Bob\n" {
		t.Fatalf("accept: %q", out)
	}
	if out := a.mustDispatch(t, "comment", "CND-1", "the", "fix", "is", "<in>"); out != "comment #1 on CND-1\n" {
		t.Fatalf("comment: %q", out)
	}
	if out := a.mustDispatch(t, "link", "CND-1", "--kind", "delivery", "--url", "https://example.com/pull/1"); out != "CND-1 linked: delivery https://example.com/pull/1\n" {
		t.Fatalf("link: %q", out)
	}
	// show: the ticket whole, its labels and milestone by name.
	shown := a.mustDispatch(t, "show", "CND-1")
	for _, want := range []string{"# CND-1: Try the CLI\n", "- status: in_progress\n", "- priority: urgent\n", "- horizon: now\n", "- owner: Bob\n",
		"- assignees: agent:claude, person:Carol\n", "- labels: docs\n", "- milestone: 1.0\n", "- to close: acceptance, met\n", "- accepted: by admin for Bob, ",
		"## Description\n\nthe <b>work</b> & more\n", "## Acceptance note\n\nchecked by hand\n", "## Links (1)\n\n- delivery https://example.com/pull/1\n",
		"## Comments (1)\n\n### #1 admin, ", "the fix is <in>\n"} {
		if !strings.Contains(shown, want) {
			t.Errorf("show does not say %q:\n%s", want, shown)
		}
	}
	// Clearing: an assignee removed, the milestone, horizon and owner
	// cleared; the acceptance stays recorded and is unmet, so done is
	// refused with the store's words.
	a.mustDispatch(t, "edit", "CND-1", "--assignee", "-Carol", "--milestone", "", "--horizon", "", "--owner", "")
	if tk := a.ticket(t, "CND-1"); !slices.Equal(tk.Assignees, []string{"agent:claude"}) || tk.MilestoneID != "" || tk.Horizon != "" || tk.Owner != "" {
		t.Fatalf("after clearing: %+v", tk.Ticket)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "edit", "CND-1", "--status", "done"); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "close_policy: CND-1 is done only once") {
		t.Fatalf("done refused: %d %v", code, err)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "link", "CND-1", "--kind", "run", "--ref", "nobody-0000dead"); code != 1 || err == nil || err.Error() != "not_found: no run nobody-0000dead" {
		t.Fatalf("a run nobody has: %d %v", code, err)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "edit", "CND-1", "--label", "+nope"); code != 1 || err == nil || err.Error() != `CND has no label named "nope"` {
		t.Fatalf("a label nobody has: %d %v", code, err)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "show", "CND-9"); code != 1 || err == nil || err.Error() != "not_found: no such ticket" {
		t.Fatalf("a ticket nobody has: %d %v", code, err)
	}
	// projects: a line each, with the counts.
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p2", "key": "EXT", "name": "Elsewhere", "tracker": "external", "trackerUrl": "https://example.com/issues"}, http.StatusCreated)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p3", "key": "ZED", "name": "Last"}, http.StatusCreated)
	wantProjects := "CND\tConductor\t2 open, 0 done\nEXT\tElsewhere\t0 open, 0 done\texternal\nZED\tLast\t0 open, 0 done\n"
	if out := a.mustDispatch(t, "projects"); out != wantProjects {
		t.Fatalf("projects:\n%s", out)
	}
	// A page of one at a time: the server's cursors followed to the end.
	defer func(n int) { dispatchPage = n }(dispatchPage)
	dispatchPage = 1
	before = a.count()
	if out := a.mustDispatch(t, "projects"); out != wantProjects {
		t.Fatalf("projects a page at a time:\n%s", out)
	}
	if out := a.mustDispatch(t, "tickets", "--project", "CND"); strings.Count(out, "\n") != 2 {
		t.Fatalf("tickets a page at a time:\n%s", out)
	}
	var pages []string
	for _, r := range a.requests(before) {
		pages = append(pages, r.path+"?"+regexp.MustCompile(`cursor=[^&]+`).ReplaceAllString(r.query, "cursor=C"))
	}
	if want := []string{
		"/api/dispatch/projects?limit=1", "/api/dispatch/projects?cursor=C&limit=1", "/api/dispatch/projects?cursor=C&limit=1",
		"/api/dispatch/tickets?limit=1&project=CND", "/api/dispatch/tickets?cursor=C&limit=1&project=CND",
	}; !slices.Equal(pages, want) {
		t.Fatalf("the pages asked for:\n%q\nwant\n%q", pages, want)
	}
}

// A write whose reply is lost is sent once more with the same operation
// id, which the server answers from its receipt: one comment, not two;
// a create the same, one ticket and its key printed.
func TestDispatchAsksOnceMoreOnALostReply(t *testing.T) {
	a := newDispatchAPI(t)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	a.mustDispatch(t, "create", "--project", "CND", "--title", "First")
	for _, c := range []struct {
		path string
		args []string
		want string
	}{
		{"/api/dispatch/tickets/CND-1/comments", []string{"comment", "CND-1", "once"}, "comment #1 on CND-1\n"},
		{"/api/dispatch/tickets", []string{"create", "--project", "CND", "--title", "Second"}, "CND-2\n"},
	} {
		before := a.count()
		a.loseNext("POST", c.path, 1)
		if out := a.mustDispatch(t, c.args[0], c.args[1:]...); out != c.want {
			t.Fatalf("%s after a lost reply: %q", c.path, out)
		}
		var posts []seenRequest
		for _, r := range a.requests(before) {
			if r.method == "POST" {
				posts = append(posts, r)
			}
		}
		if len(posts) != 2 || posts[0].reply != -1 || posts[1].reply <= 0 || posts[0].body["opId"] != posts[1].body["opId"] {
			t.Fatalf("%s: the posts %+v, want the same operation twice, the first's reply lost", c.path, posts)
		}
	}
	if cs := a.ticket(t, "CND-1").Comments; len(cs) != 1 {
		t.Fatalf("%d comments after one lost reply", len(cs))
	}
	if out := a.mustDispatch(t, "tickets"); strings.Count(out, "\n") != 2 {
		t.Fatalf("tickets after a lost create's reply:\n%s", out)
	}
	// A second loss is the error; the operation was taken once all the
	// same, and its second sending changed nothing.
	a.loseNext("POST", "/api/dispatch/tickets/CND-1/comments", 2)
	if code, _, _, err := a.dispatchCmd(t, "", "comment", "CND-1", "twice"); code != 1 || err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("two lost replies: %d %v", code, err)
	}
	if cs := a.ticket(t, "CND-1").Comments; len(cs) != 2 || cs[1].Body != "twice" {
		t.Fatalf("the comments after two lost replies: %+v", cs)
	}
}

// What conductor dispatch refuses before any request: an unknown
// command, a key that is no ticket's, a missing or unknown argument, a
// value outside its vocabulary, a set's member without its sign, a write
// that changes nothing; and the server's address is the crews commands'
// (a plain http server on another machine refused).
func TestDispatchUsageErrors(t *testing.T) {
	a := newDispatchAPI(t)
	for _, args := range [][]string{
		{"nope"},
		{"show"}, {"show", "cnd-1"}, {"show", "CND-1", "extra"}, {"projects", "extra"}, {"tickets", "extra"},
		{"tickets", "--status", "nope"}, {"tickets", "--project", "cnd"},
		{"create", "--title", "x"}, {"create", "--project", "CND"}, {"create", "--project", "CND", "--title", "x", "--priority", "huge"},
		{"create", "--project", "CND", "--title", "x", "--description-file", filepath.Join(t.TempDir(), "missing.md")},
		{"edit", "CND-1"}, {"edit", "CND-1", "--assignee", "Carol"}, {"edit", "CND-1", "--label", "api"}, {"edit", "CND-1", "--status", "nope"},
		{"edit", "CND-1", "--priority", "9"}, {"edit", "CND-1", "--horizon", "soon"}, {"edit", "CND-1", "--assignee", "+"},
		{"comment", "CND-1"}, {"comment", "CND-1", " "},
		{"link", "CND-1"}, {"link", "CND-1", "--kind", "nope"}, {"link", "CND-1", "--kind", "run"}, {"link", "CND-1", "--kind", "delivery"},
		{"accept"}, {"edit", "CND-1", "--bogus", "x"},
	} {
		if code, _, _, err := a.dispatchCmd(t, "", args[0], args[1:]...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want a usage error", args, code, err)
		}
	}
	if n := a.count(); n != 0 {
		t.Fatalf("the usage errors sent %d requests", n)
	}
	// A description past its bound is refused here, before it is sent.
	long := filepath.Join(t.TempDir(), "long.md")
	if err := os.WriteFile(long, bytes.Repeat([]byte("x"), dispatch.MaxTicketDescription+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "create", "--project", "CND", "--title", "x", "--description-file", long); code != 2 || err == nil || !strings.Contains(err.Error(), "at most 64 KiB") {
		t.Fatalf("a long description: %d %v", code, err)
	}
	// A plain http server on another machine is refused, as conductor
	// crews refuses it.
	var out, errb bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"projects", "--server", "http://example.com:8080", "--token", "t"}, strings.NewReader(""), &out, &errb); code != 2 || err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("a plain server elsewhere: %d %v", code, err)
	}
	for _, args := range [][]string{nil, {"-h"}, {"show", "-h"}} {
		var errOut bytes.Buffer
		code, err := runDispatch(t.Context(), args, strings.NewReader(""), io.Discard, &errOut)
		if want := map[bool]int{true: 2, false: 0}[args == nil]; code != want || err != nil || !strings.Contains(errOut.String(), "conductor dispatch show KEY-n") {
			t.Fatalf("%q: %d %v\n%s", args, code, err, errOut.String())
		}
	}
	if !strings.Contains(usage, "conductor dispatch projects|tickets|show|create|edit|comment|accept|link") {
		t.Fatal("the root usage does not name conductor dispatch")
	}
}

// maxDispatchReply is the smallest whole number of MiB above the largest
// reply conductor dispatch's reads can carry, a ticket's and a project's,
// and the session client's limit holds a ticket's.
func TestDispatchReplyLimitsAreDerivedFromTheServersBounds(t *testing.T) {
	largest := max(api.MaxTicketReply, api.MaxProjectReply)
	if maxDispatchReply%(1<<20) != 0 || maxDispatchReply <= largest || maxDispatchReply-(1<<20) > largest {
		t.Fatalf("maxDispatchReply is %d, the largest reply %d", maxDispatchReply, largest)
	}
	if maxSessionReply%(1<<20) != 0 || maxSessionReply <= api.MaxTicketReply {
		t.Fatalf("maxSessionReply is %d, the largest ticket's reply %d", maxSessionReply, api.MaxTicketReply)
	}
}

// The fillers of the largest ticket: a text bounded in bytes of '"' and
// '\', two bytes each as JSON; one bounded in characters of U+2028 and
// U+2029, three bytes each and six as JSON whatever the escaping. Each
// starts with a label of its own, so every one is told apart in a
// printout.
func worstBytes(label string, n int) string {
	return label + strings.Repeat(`"\`, (n-len(label))/2) + strings.Repeat(`"`, (n-len(label))%2)
}

func worstChars(label string, n int) string {
	return label + strings.Repeat(string([]rune{0x2028, 0x2029}), (n-len(label))/2) + strings.Repeat(string(rune(0x2028)), (n-len(label))%2)
}

// largestTicketView is the largest ticket the server's read can give, in
// the shape it gives it: every field and set at its bound and every text
// at its escaped worst (fifty comments of 16 KiB, the description of 64
// KiB, the criteria of 16 KiB, fifty links, fifty attachments, a hundred
// relations, fifty children, twenty labels, eight assignees), with its
// labels' and its milestone's rows.
func largestTicketView() (dispatch.TicketView, []dispatch.Label, dispatch.Milestone) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := func(i int) string { return fmt.Sprintf("%016x", i) }
	v := dispatch.TicketView{Ticket: dispatch.Ticket{
		ID: id(1), ProjectID: id(2), Number: 1, Key: "BIG-1", Title: worstChars("title ", dispatch.MaxTicketTitle),
		Description: worstBytes("description ", dispatch.MaxTicketDescription), Acceptance: worstBytes("criteria ", dispatch.MaxAcceptance),
		Status: dispatch.TicketInProgress, Priority: dispatch.MaxPriority, Horizon: dispatch.HorizonLater, Owner: worstChars("owner ", dispatch.MaxPersonName),
		ParentID: id(3), MilestoneID: id(4), AcceptedAt: at, AcceptedBy: worstChars("by ", dispatch.MaxByName), AcceptedFor: worstChars("owner ", dispatch.MaxPersonName),
		AcceptedCriteria: strings.Repeat("ab", 32), AcceptedNote: worstChars("note ", dispatch.MaxAcceptedNote), Source: "native",
		CreatedAt: at, CreatedBy: worstChars("by ", dispatch.MaxByName), UpdatedAt: at, Rev: 400,
	}}
	for i := range dispatch.MaxAssignees {
		v.Assignees = append(v.Assignees, "person:"+worstChars(fmt.Sprintf("p%d ", i), dispatch.MaxPersonName))
	}
	var labels []dispatch.Label
	for i := range dispatch.MaxTicketLabels {
		l := dispatch.Label{ID: id(100 + i), ProjectID: v.ProjectID, Name: worstChars(fmt.Sprintf("label%02d ", i), dispatch.MaxLabelName), Color: "yellow", CreatedAt: at, Rev: 1}
		labels = append(labels, l)
		v.Labels = append(v.Labels, l.ID)
	}
	milestone := dispatch.Milestone{ID: v.MilestoneID, ProjectID: v.ProjectID, Name: worstChars("milestone ", dispatch.MaxMilestoneName),
		Description: worstBytes("milestone description ", dispatch.MaxMilestoneDescription), State: "open", CreatedAt: at, UpdatedAt: at, Rev: 2}
	v.Parent = &dispatch.RelatedTicket{ID: v.ParentID, Key: "BIG-2", Title: worstChars("parent ", dispatch.MaxTicketTitle), Status: dispatch.TicketReady}
	for i := range dispatch.MaxTicketChildren {
		v.Children.Tickets = append(v.Children.Tickets, dispatch.RelatedTicket{ID: id(200 + i), Key: fmt.Sprintf("BIG-%d", 3+i), Title: worstChars(fmt.Sprintf("child%02d ", i), dispatch.MaxTicketTitle), Status: dispatch.TicketInProgress})
	}
	v.Children.Total = dispatch.MaxTicketChildren
	for i := range dispatch.MaxRelations {
		other := dispatch.RelatedTicket{ID: id(300 + i), Key: fmt.Sprintf("BIG-%d", 60+i), Title: worstChars(fmt.Sprintf("relation%03d ", i), dispatch.MaxTicketTitle), Status: dispatch.TicketInProgress}
		v.Relations = append(v.Relations, dispatch.TicketRelation{Relation: dispatch.Relation{ID: id(400 + i), FromID: v.ID, ToID: other.ID, Kind: dispatch.RelationRelates,
			CreatedAt: at, CreatedBy: worstChars("by ", dispatch.MaxByName)}, Other: other})
	}
	v.RelationsTotal = dispatch.MaxRelations
	for i := range dispatch.MaxLinks {
		v.Links = append(v.Links, dispatch.Link{ID: id(500 + i), Kind: dispatch.LinkExternal, Ref: worstBytes(fmt.Sprintf("ref%02d ", i), dispatch.MaxLinkRef),
			URL: "https://h/" + worstBytes(fmt.Sprintf("url%02d ", i), dispatch.MaxLinkURL-len("https://h/")), Note: worstChars(fmt.Sprintf("linknote%02d ", i), dispatch.MaxLinkNote),
			CreatedAt: at, CreatedBy: worstChars("by ", dispatch.MaxByName)})
	}
	for i := range dispatch.MaxAttachments {
		v.Attachments = append(v.Attachments, dispatch.Attachment{ID: id(600 + i), TicketID: v.ID, Name: worstChars(fmt.Sprintf("file%02d ", i), dispatch.MaxAttachmentName/3),
			Size: dispatch.MaxAttachmentSize, SHA256: strings.Repeat("0123456789abcdef", 4), ContentType: worstBytes(fmt.Sprintf("type%02d ", i), dispatch.MaxContentType),
			CreatedAt: at, CreatedBy: worstChars("by ", dispatch.MaxByName)})
	}
	for i := range dispatch.MaxTicketComments {
		v.Comments = append(v.Comments, dispatch.Comment{ID: id(700 + i), TicketID: v.ID, N: 1950 + i, Author: "admin", By: worstChars("by ", dispatch.MaxByName),
			Body: worstBytes(fmt.Sprintf("comment%02d ", i), dispatch.MaxCommentBody), CreatedAt: at, EditedAt: at, Rev: 2})
	}
	v.Close = dispatch.CloseState{Requires: []string{dispatch.RequireAcceptance, dispatch.RequireDelivery}, Met: dispatch.CloseMet{Acceptance: true}, Missing: []string{dispatch.RequireDelivery}}
	return v, labels, milestone
}

// paddedReply is v as the server writes it (HTML escaping off), and a
// field no client reads added to make it limit-1 bytes: the largest reply
// a client must take whole. Its size before the padding is the second
// result; v at limit or past it is an error.
func paddedReply(v map[string]any, limit int) ([]byte, int, error) {
	b, err := encodeJSON(v)
	if err != nil {
		return nil, 0, err
	}
	if len(b) >= limit {
		return nil, len(b), fmt.Errorf("the largest ticket's reply is %d bytes, not under %d", len(b), limit)
	}
	pad := limit - 1 - len(b) - len(`,"pad":""`)
	return append(append(slices.Clip(b[:len(b)-1]), `,"pad":"`+strings.Repeat("x", pad)+`"`...), '}'), len(b), nil
}

// largest is the largest ticket (largestTicketView) and its two replies,
// the admin's read and the session's, each padded to the server's bound.
type largest struct {
	view                 dispatch.TicketView
	labels               []dispatch.Label
	milestone            dispatch.Milestone
	admin, session       []byte
	adminSize, sessionSz int
	err                  error
}

// largestReplies makes the largest ticket's replies once for the tests
// that read it: encoding three megabytes takes seconds under the race
// detector.
var largestReplies = sync.OnceValue(func() largest {
	var l largest
	l.view, l.labels, l.milestone = largestTicketView()
	var err error
	if l.admin, l.adminSize, l.err = paddedReply(map[string]any{"ticket": l.view}, api.MaxTicketReply); l.err != nil {
		return l
	}
	l.session, l.sessionSz, err = paddedReply(map[string]any{"ticket": l.view, "labels": l.labels, "milestone": l.milestone}, api.MaxTicketReply)
	l.err = err
	return l
})

// largestFor is the largest ticket's replies, the test failed when they
// could not be made.
func largestFor(t *testing.T) largest {
	t.Helper()
	l := largestReplies()
	if l.err != nil {
		t.Fatal(l.err)
	}
	t.Logf("the largest ticket's replies: the admin's read %d bytes, the session's %d, each padded to %d", l.adminSize, l.sessionSz, api.MaxTicketReply-1)
	return l
}

// checkWhole fails the test unless the printout holds every part of the
// largest ticket: its title, owner, assignees, labels and milestone by
// name, description, criteria, acceptance note, parent, children,
// relations, links with their notes, attachments, and every comment with
// its author, and says the comments are the last of more.
func checkWhole(t *testing.T, out string, v dispatch.TicketView, labels []dispatch.Label, milestone dispatch.Milestone) {
	t.Helper()
	parts := []string{"# BIG-1: " + v.Title + "\n", v.Owner, v.Description, v.Acceptance, v.AcceptedNote, v.Parent.Title, milestone.Name,
		fmt.Sprintf("## Comments (the last %d of %d)", len(v.Comments), v.Comments[len(v.Comments)-1].N)}
	parts = append(parts, v.Assignees...)
	for _, l := range labels {
		parts = append(parts, l.Name)
	}
	for _, c := range v.Children.Tickets {
		parts = append(parts, c.Title)
	}
	for _, r := range v.Relations {
		parts = append(parts, "- relates to "+r.Other.Key+": "+r.Other.Title)
	}
	for _, l := range v.Links {
		parts = append(parts, "- external "+l.Ref+" "+l.URL+"\n  "+l.Note+"\n")
	}
	for _, a := range v.Attachments {
		parts = append(parts, "- "+a.Name+" (16.0 MiB, "+a.ContentType+")")
	}
	for _, c := range v.Comments {
		parts = append(parts, fmt.Sprintf("### #%d %s, 2026-10-30 12:00 UTC, edited\n\n%s\n", c.N, c.By, c.Body))
	}
	for i, p := range parts {
		if !strings.Contains(out, p) {
			t.Fatalf("part %d of the largest ticket is not printed whole: %.80q", i, p)
		}
	}
}

// conductor dispatch show takes the largest reply a ticket's read can
// make, the largest ticket padded to the server's bound, through its
// limit, and prints the ticket whole, its labels and milestone by name
// from the project's read; a reply past its limit is refused with words.
func TestDispatchShowPrintsTheLargestTicketWhole(t *testing.T) {
	clearConductorEnv(t)
	l := largestFor(t)
	v, labels, milestone := l.view, l.labels, l.milestone
	project, err := encodeJSON(dispatch.ProjectView{Project: dispatch.Project{ID: v.ProjectID, Key: "BIG", Name: "Big"}, Labels: labels, Milestones: []dispatch.Milestone{milestone}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/tickets/BIG-1", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(l.admin) })
	mux.HandleFunc("GET /api/dispatch/tickets/BIG-2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte(" "), maxDispatchReply+1))
	})
	mux.HandleFunc("GET /api/dispatch/projects/BIG", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(project) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out, errb bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"show", "BIG-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, &errb); code != 0 || err != nil {
		t.Fatalf("show: %d %v", code, err)
	}
	checkWhole(t, out.String(), v, labels, milestone)
	if code, err := runDispatch(t.Context(), []string{"show", "BIG-2", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), io.Discard, io.Discard); code != 1 || err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") {
		t.Fatalf("a reply past the limit: %d %v", code, err)
	}
}

// pagingServer stands in for the two list routes as the server pages
// them, strict as they are: any parameter but theirs is 400
// invalid_request; a page of at most limit (1 to 200) after the cursor,
// next the cursor of the page after it; every summary at its longest,
// every text at its escaped worst. A project's or a ticket's description
// is in none of them: a page carries summaries. It records each page's
// size.
type pagingServer struct {
	projects []dispatch.ProjectSummary
	tickets  []dispatch.TicketSummary
	mu       sync.Mutex
	pages    []int
}

func (p *pagingServer) page(w http.ResponseWriter, r *http.Request, total int, params []string, items func(from, to int) any) {
	q := r.URL.Query()
	for k := range q {
		if !slices.Contains(params, k) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"this route takes no parameter ` + k + `"}}`))
			return
		}
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > 200 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"limit is 1 to 200"}}`))
		return
	}
	from, _ := strconv.Atoi(q.Get("cursor"))
	to := min(from+limit, total)
	out := map[string]any{"total": total}
	if to < total {
		out["next"] = strconv.Itoa(to)
	}
	switch v := items(from, to).(type) {
	case []dispatch.ProjectSummary:
		out["projects"] = v
	case []dispatch.TicketSummary:
		out["tickets"] = v
	}
	b, _ := encodeJSON(out)
	p.mu.Lock()
	p.pages = append(p.pages, len(b))
	p.mu.Unlock()
	_, _ = w.Write(b)
}

// conductor dispatch projects and tickets list 201 of each, at their
// longest, whole, through two pages of 200 and 1, each page under the
// limit. (A page carries summaries, so a description, at its bound or
// not, is in none: TestDispatchProjectsArePagedUnderTheirBound in
// internal/api proves the server's pages stay under their bound with
// them.)
func TestDispatchListsEveryPage(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	p := &pagingServer{}
	for i := range 201 {
		p.projects = append(p.projects, dispatch.ProjectSummary{ID: fmt.Sprintf("%016x", i), Key: fmt.Sprintf("P%04d", i), Name: worstChars(fmt.Sprintf("project%03d ", i), dispatch.MaxProjectName),
			Tracker: dispatch.TrackerNative, Open: i, Done: 1_000_000, Rev: 1 << 40, UpdatedAt: at})
		tk := dispatch.TicketSummary{ID: fmt.Sprintf("%016x", i), ProjectID: fmt.Sprintf("%016x", 1), Number: int64(i + 1), Key: fmt.Sprintf("BIG-%d", i+1),
			Title: worstChars(fmt.Sprintf("ticket%03d ", i), dispatch.MaxTicketTitle), Status: dispatch.TicketInReview, Priority: 2, Horizon: dispatch.HorizonNext,
			Owner: worstChars("owner ", dispatch.MaxPersonName), MilestoneID: fmt.Sprintf("%016x", 2), ParentID: fmt.Sprintf("%016x", 3), Blocked: true,
			CreatedAt: at, UpdatedAt: at, Rev: 1 << 40}
		for j := range dispatch.MaxAssignees {
			tk.Assignees = append(tk.Assignees, "person:"+worstChars(fmt.Sprintf("p%d ", j), dispatch.MaxPersonName))
		}
		for j := range dispatch.MaxTicketLabels {
			tk.Labels = append(tk.Labels, fmt.Sprintf("%016x", 100+j))
		}
		p.tickets = append(p.tickets, tk)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/projects", func(w http.ResponseWriter, r *http.Request) {
		p.page(w, r, len(p.projects), []string{"archived", "limit", "cursor"}, func(from, to int) any { return p.projects[from:to] })
	})
	mux.HandleFunc("GET /api/dispatch/tickets", func(w http.ResponseWriter, r *http.Request) {
		p.page(w, r, len(p.tickets), []string{"project", "status", "priority", "label", "owner", "assignee", "milestone", "horizon", "parent", "blocked", "q", "sort", "desc", "deleted", "limit", "cursor"},
			func(from, to int) any { return p.tickets[from:to] })
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code, err := runDispatch(t.Context(), append(args, "--server", srv.URL, "--token", "t"), strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
			t.Fatalf("%q: %d %v", args, code, err)
		}
		return out.String()
	}
	checkPages := func(what string) {
		t.Helper()
		p.mu.Lock()
		pages := p.pages
		p.pages = nil
		p.mu.Unlock()
		if len(pages) != 2 {
			t.Fatalf("%s: %d pages, want 2", what, len(pages))
		}
		for i, n := range pages {
			t.Logf("%s, page %d: %d bytes of %d", what, i+1, n, maxDispatchReply)
			if n >= maxDispatchReply {
				t.Fatalf("%s: page %d is %d bytes, not under %d", what, i+1, n, maxDispatchReply)
			}
		}
	}
	lines := strings.Split(strings.TrimSuffix(run("projects"), "\n"), "\n")
	if len(lines) != 201 || lines[0] != "P0000\t"+p.projects[0].Name+"\t0 open, 1000000 done" || lines[200] != "P0200\t"+p.projects[200].Name+"\t200 open, 1000000 done" {
		t.Fatalf("projects: %d lines, the first %.60q", len(lines), lines[0])
	}
	checkPages("projects")
	lines = strings.Split(strings.TrimSuffix(run("tickets", "--project", "BIG"), "\n"), "\n")
	if len(lines) != 201 || lines[200] != "BIG-201\tin_review\tmedium\t"+p.tickets[200].Owner+"\t"+p.tickets[200].Title {
		t.Fatalf("tickets: %d lines, the last %.60q", len(lines), lines[len(lines)-1])
	}
	checkPages("tickets")
}

// A comment's text is every word after its key, as written: a word that
// starts with '-', a Markdown list item or "-1", is the comment's and no
// flag, and the flags go before the key.
func TestDispatchCommentTextMayStartWithADash(t *testing.T) {
	a := newDispatchAPI(t)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	a.mustDispatch(t, "create", "--project", "CND", "--title", "Dashes")
	for i, words := range [][]string{{"-", "fixed", "the", "cache"}, {"-1", "on", "this"}, {"--", "--server", "x"}} {
		want := fmt.Sprintf("comment #%d on CND-1\n", i+1)
		if out := a.mustDispatch(t, "comment", append([]string{"CND-1"}, words...)...); out != want {
			t.Fatalf("comment %q: %q", words, out)
		}
	}
	cs := a.ticket(t, "CND-1").Comments
	if len(cs) != 3 || cs[0].Body != "- fixed the cache" || cs[1].Body != "-1 on this" || cs[2].Body != "-- --server x" {
		t.Fatalf("the comments: %+v", cs)
	}
}
