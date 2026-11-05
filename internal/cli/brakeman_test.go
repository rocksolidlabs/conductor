package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// conductor brakeman (docs/plans/brakeman.md, PR-5) against a real
// server whose catalog holds bk: a shell under Claude Code's adapter, so
// Brakeman's configuration takes it (an MCP registration and a read-only
// mode that holds), that says it is ready and echoes what is typed, as
// the API's tests' scripted agent does.

// newBrakemanAPI is a real server with bk in its catalog and Brakeman on
// with it, in the server's root. Its desk takes no turn until the test
// starts it (StartBrakeman), as a server does once it listens.
func newBrakemanAPI(t *testing.T) *dispatchAPI {
	t.Helper()
	a := newDispatchAPIWith(t, catalog.Agent{ID: "bk", Name: "bk", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}, Adapter: "claude", AllowArgs: true})
	return a
}

// enableBrakeman turns Brakeman on with bk, at the revision read.
func (a *dispatchAPI) enableBrakeman(t *testing.T) {
	t.Helper()
	cfg := a.admin(t, "GET", "/api/brakeman", nil, http.StatusOK)["config"].(map[string]any)
	rev := cfg["rev"]
	cfg["enabled"], cfg["agentId"], cfg["cwd"] = true, "bk", a.root
	a.admin(t, "PUT", "/api/brakeman/config", map[string]any{"rev": rev, "config": cfg}, http.StatusOK)
}

// brakemanCmd runs conductor brakeman with args; stdin is its input.
func brakemanCmd(t *testing.T, stdin string, args ...string) (int, string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := Run(t.Context(), append([]string{"brakeman"}, args...), strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String(), err
}

// ask, status, list and show with the workbench token: a request refused
// while Brakeman is off with the server's words; once on, one kept with
// its kind, its subject, the note read from stdin and since read as the
// last 18:00, its reply lost once and asked for again with the same
// operation id, which the server keeps once; the queue and today's
// turns; the requests newest first; a request whole, with no brief yet;
// a usage error before anything is sent.
func TestBrakemanAskWithTheWorkbenchToken(t *testing.T) {
	a := newBrakemanAPI(t)
	was := brakemanNow
	brakemanNow = func() time.Time { return time.Date(2026, 11, 2, 9, 30, 0, 0, time.Local) }
	t.Cleanup(func() { brakemanNow = was })
	admin := []string{"--server", a.hs.URL, "--token", dispatchToken}
	if code, _, _, err := brakemanCmd(t, "", append([]string{"ask", "briefing"}, admin...)...); code != 1 || err == nil || !strings.Contains(err.Error(), "brakeman_off") {
		t.Fatalf("ask while off: %d %v", code, err)
	}
	a.enableBrakeman(t)
	before := a.count()
	a.loseNext("POST", "/api/brakeman/requests", 1)
	code, out, errOut, err := brakemanCmd(t, "", append([]string{"ask", "briefing", "--since", "18:00"}, admin...)...)
	if code != 0 || err != nil || out != "br-1 queued (briefing): conductor brakeman show br-1 once it is briefed\n" {
		t.Fatalf("ask: %d %v %q %q", code, err, out, errOut)
	}
	posts := a.requests(before)
	since := time.Date(2026, 11, 1, 18, 0, 0, 0, time.Local).Format(time.RFC3339)
	if len(posts) != 2 || posts[0].reply != -1 || posts[0].body["opId"] != posts[1].body["opId"] || posts[1].body["since"] != since || posts[1].body["kind"] != "briefing" {
		t.Fatalf("the ask's posts: %+v", posts)
	}
	code, out, _, err = brakemanCmd(t, "what overlaps CND-1?", append([]string{"ask", "ask", "--subject", "ticket:CND-1", "--note-file", "-"}, admin...)...)
	if code != 0 || err != nil || !strings.HasPrefix(out, "br-2 queued (ask)") {
		t.Fatalf("ask with a note: %d %v %q", code, err, out)
	}
	if code, out, _, err = brakemanCmd(t, "", append([]string{"status"}, admin...)...); code != 0 || err != nil ||
		!hasAll(out, "Brakeman is on: agent bk, model its default, in "+a.root, "turn: none", "queued: br-1 queued briefing", "queued: br-2 queued ask", "today: 0 of 40 turns") {
		t.Fatalf("status: %d %v\n%s", code, err, out)
	}
	code, out, _, err = brakemanCmd(t, "", append([]string{"list"}, admin...)...)
	if lines := strings.Split(strings.TrimSpace(out), "\n"); code != 0 || err != nil || len(lines) != 2 || !strings.HasPrefix(lines[0], "br-2 queued ask") || !strings.HasPrefix(lines[1], "br-1 queued briefing") {
		t.Fatalf("list: %d %v\n%s", code, err, out)
	}
	if code, out, _, err = brakemanCmd(t, "", append([]string{"show", "br-2"}, admin...)...); code != 0 || err != nil || !hasAll(out, "br-2 queued ask", "note: what overlaps CND-1?", "no brief yet") {
		t.Fatalf("show: %d %v\n%s", code, err, out)
	}
	code, out, _, err = brakemanCmd(t, "", append([]string{"show", "br-2", "--json"}, admin...)...)
	var whole struct {
		Request struct {
			Subjects []string `json:"subjects"`
		} `json:"request"`
	}
	if code != 0 || err != nil || json.Unmarshal([]byte(out), &whole) != nil || len(whole.Request.Subjects) != 1 || whole.Request.Subjects[0] != "ticket:CND-1" {
		t.Fatalf("show --json: %d %v\n%s", code, err, out)
	}
	n := a.count()
	for _, args := range [][]string{{"ask", "nope"}, {"ask"}, {"ask", "briefing", "--since", "soon"}, {"show", "br-0"}, {"show"}, {"list", "extra"}, {"nope"}, {}} {
		if code, _, _, _ := brakemanCmd(t, "", append(args, admin...)...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if code, _, _, err := brakemanCmd(t, "", "status", "--server", a.hs.URL); code != 2 || err == nil || !strings.Contains(err.Error(), "workbench token is required") {
		t.Errorf("status without a token: %d %v", code, err)
	}
	if a.count() != n {
		t.Fatalf("%d requests sent by usage errors", a.count()-n)
	}
}

// A turn's session, as an agent without MCP drives it: the request read,
// what changed observed, and its brief submitted from a file, its fact
// citing the turn's own session; the brief then read whole with the
// workbench token. A second brief, from stdin, is refused with the
// server's words.
func TestBrakemanBriefFromAFile(t *testing.T) {
	a := newBrakemanAPI(t)
	a.enableBrakeman(t)
	a.srv.StartBrakeman()
	id := a.admin(t, "POST", "/api/brakeman/requests", map[string]any{"opId": "ask-1", "kind": "briefing"}, http.StatusCreated)["request"].(map[string]any)["id"].(string)
	sid, local := a.turnSessionOf(t, id)
	token := "agent-token-" + sid
	local.SetAgentToken(token)
	t.Setenv("CONDUCTOR_NOTIFY_URL", a.hs.URL+"/api/sessions/"+sid+"/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", token)
	t.Setenv(envBrakeman, id)
	code, out, _, err := brakemanCmd(t, "", "request")
	if code != 0 || err != nil || !hasAll(out, "Brakeman request "+id+" (briefing), ", `"conductor brakeman brief --file F"`, `"visibility"`) {
		t.Fatalf("request: %d %v\n%s", code, err, out)
	}
	if code, out, _, err = brakemanCmd(t, "", "observe", "changes", "--limit=10"); code != 0 || err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("observe changes: %d %v\n%s", code, err, out)
	}
	file := filepath.Join(t.TempDir(), "brief.json")
	brief := `{"schema":"brakeman.brief/1","title":"Quiet night","summary":"Nothing failed.","items":[{"kind":"fact/1","text":"This turn ran.","evidence":["session:` + sid + `"]}]}`
	if err := os.WriteFile(file, []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _, err = brakemanCmd(t, "", "brief", "--file", file); code != 0 || err != nil || out != "brief kept: request "+id+" is briefed; your turn ends now\n" {
		t.Fatalf("brief: %d %v %q", code, err, out)
	}
	if code, _, _, err = brakemanCmd(t, brief, "brief", "--file", "-"); code != 1 || err == nil || !strings.Contains(err.Error(), "brief_exists") {
		t.Fatalf("a second brief: %d %v", code, err)
	}
	if code, out, _, err = brakemanCmd(t, "", "show", id, "--server", a.hs.URL, "--token", dispatchToken); code != 0 || err != nil ||
		!hasAll(out, id+" briefed briefing", "# Quiet night", "Nothing failed.", "0. fact/1: This turn ran.", "evidence: session:"+sid) {
		t.Fatalf("show: %d %v\n%s", code, err, out)
	}
}

// turnSessionOf waits for request id's session, in the registry once
// the desk has launched the turn. A request that closes before it (a
// launch refused or failed ends it) fails t with its state and why, and
// so does the test's own deadline drawing near, with the request as it
// stands, rather than go test's panic.
func (a *dispatchAPI) turnSessionOf(t *testing.T, id string) (string, *session.Local) {
	t.Helper()
	deadline, bounded := t.Deadline()
	for {
		var sid string
		var local *session.Local
		a.srv.Registry().Each(func(d session.Driver) {
			if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id {
				sid, local = d.Info().ID, d.(*session.Local)
			}
		})
		if sid != "" {
			return sid, local
		}
		r := a.admin(t, "GET", "/api/brakeman/requests/"+id, nil, http.StatusOK)["request"].(map[string]any)
		state, _ := r["state"].(string)
		if brakeman.Closed(state) {
			t.Fatalf("request %s is %s before its session started: %v", id, state, r["why"])
		}
		if bounded && time.Until(deadline) < 30*time.Second {
			t.Fatalf("request %s's session never started: it is %s", id, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// list follows the pages to the end: with a page of 3, seven requests
// are asked for in three pages, each after the one before's next, and
// every request is listed once, newest first.
func TestBrakemanListFollowsEveryPage(t *testing.T) {
	a := newBrakemanAPI(t)
	a.enableBrakeman(t)
	was := brakemanListPage
	brakemanListPage = 3
	t.Cleanup(func() { brakemanListPage = was })
	for i := range 7 {
		a.admin(t, "POST", "/api/brakeman/requests", map[string]any{"opId": fmt.Sprintf("ask-%d", i), "kind": "briefing"}, http.StatusCreated)
	}
	before := a.count()
	code, out, _, err := brakemanCmd(t, "", "list", "--server", a.hs.URL, "--token", dispatchToken)
	if code != 0 || err != nil {
		t.Fatalf("list: %d %v", code, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 {
		t.Fatalf("%d lines:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if want := fmt.Sprintf("br-%d queued briefing ", 7-i); !strings.HasPrefix(l, want) {
			t.Fatalf("line %d is %q, want %q…", i, l, want)
		}
	}
	gets := a.requests(before)
	if len(gets) != 3 {
		t.Fatalf("%d requests, want 3 pages: %+v", len(gets), gets)
	}
	next := ""
	for i, g := range gets {
		want := "limit=3"
		if next != "" {
			want += "&after=" + url.QueryEscape(next)
		}
		if g.method != "GET" || g.path != "/api/brakeman/requests" || g.query != want {
			t.Fatalf("page %d: %s %s?%s, want ?%s", i, g.method, g.path, g.query, want)
		}
		// The page as the server answers it, for the next one's after.
		next, _ = a.admin(t, "GET", "/api/brakeman/requests?"+g.query, nil, http.StatusOK)["next"].(string)
		if i < 2 && next == "" {
			t.Fatalf("page %d has no next", i)
		}
	}
	if next != "" {
		t.Fatalf("the last page's next is %q", next)
	}
}

// show prints every item whole: its kind and the words it leads with,
// then each other field in the brief's order, so a draft crew's crew and
// gates, an overlap's paths and a decision's alternatives,
// recommendation and what it authorizes are all there.
func TestBrakemanShowPrintsEveryItemWhole(t *testing.T) {
	clearConductorEnv(t)
	reply := `{"request":{"id":"br-1","kind":"decision","state":"briefed","createdAt":"2026-11-02T09:00:00Z","agent":{"agentId":"claude","version":"2.1.304"}},
"brief":{"title":"Morning","summary":"Two things.","items":[
{"kind":"draft_crew/1","crew":{"name":"Fix <it>","members":[{"name":"a"}]},"gates":[{"task":"t1","decision":"merge?"}],"from":["ticket:CND-1"]},
{"kind":"overlap/1","paths":["a/b.go","c.go"],"between":["attempt:r1/t1/1","session:s1"]},
{"kind":"decision/1","about":"question:q1","happened":"It asked.","whyHuman":"Only you know.","question":"Ship?","alternatives":[{"label":"Yes","tradeoffs":"fast"},{"label":"No","tradeoffs":"safe"}],"recommendation":1,"authorizes":"merging it","evidence":["session:s1"]},
{"kind":"fact/1","text":"It ran.","evidence":["session:s1"]}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/brakeman/requests/br-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	code, out, _, err := brakemanCmd(t, "", "show", "br-1", "--server", srv.URL, "--token", dispatchToken)
	if code != 0 || err != nil {
		t.Fatalf("show: %d %v", code, err)
	}
	for _, want := range []string{
		"\n0. draft_crew/1\n   crew: {\"name\":\"Fix <it>\",\"members\":[{\"name\":\"a\"}]}\n   gates:\n     - {\"task\":\"t1\",\"decision\":\"merge?\"}\n   from: ticket:CND-1\n",
		"\n1. overlap/1\n   paths: a/b.go, c.go\n   between: attempt:r1/t1/1, session:s1\n",
		"\n2. decision/1: Ship?\n   about: question:q1\n   happened: It asked.\n   whyHuman: Only you know.\n   alternatives:\n     - {\"label\":\"Yes\",\"tradeoffs\":\"fast\"}\n     - {\"label\":\"No\",\"tradeoffs\":\"safe\"}\n   recommendation: 1\n   authorizes: merging it\n   evidence: session:s1\n",
		"\n3. fact/1: It ran.\n   evidence: session:s1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
}

// A failure to reach the server names its host and cause, never the
// read's URL: its query is the turn's (here a search), as the command's
// error and the MCP tool's text.
func TestObserveNamesNoQueryWhenTheServerCannotBeReached(t *testing.T) {
	clearConductorEnv(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	gone := srv.URL
	srv.Close()
	const marker = "review-query-marker"
	replies := mcpRun(t, brakemanEnv(gone, "s1", "agent-tok"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":{"what":"tickets","query":{"q":"`+marker+`"}}}}`)
	if text, isErr := toolText(t, replies[0]); !isErr || strings.Contains(text, marker) || strings.Contains(text, "/api/") || !strings.Contains(text, "observe: cannot reach") {
		t.Errorf("the tool's text: %q %v", text, isErr)
	}
	t.Setenv("CONDUCTOR_NOTIFY_URL", gone+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-tok")
	t.Setenv(envBrakeman, "br-1")
	code, out, errOut, err := brakemanCmd(t, "", "observe", "tickets", "--q", marker)
	if code != 1 || err == nil || strings.Contains(err.Error()+out+errOut, marker) || strings.Contains(err.Error(), "/api/") || !strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("the command: %d %v %q %q", code, err, out, errOut)
	}
}

// observe reads a reply up to the bound the table gives its what, in
// whole MiB above it (a shared read's 4 MiB, which the server holds a
// turn's answer to), and refuses one past it.
func TestObserveReadsUpToItsTableBound(t *testing.T) {
	limit := wholeMiB(opBound("observe_messages", "reply"))
	if limit <= maxSessionReply {
		t.Fatalf("observe_messages reads at most %d bytes, no more than the session client's %d", limit, maxSessionReply)
	}
	var size atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(size.Load())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"messages":[],"pad":"`+strings.Repeat("x", n-len(`{"messages":[],"pad":""}`))+`"}`)
	}))
	t.Cleanup(srv.Close)
	c, err := brakemanClientFromEnv(brakemanEnv(srv.URL, "s1", "agent-tok"))
	if err != nil {
		t.Fatal(err)
	}
	size.Store(int64(maxSessionReply + 1))
	if text, err := c.observe(t.Context(), "messages", map[string]string{"run": "r1"}); err != nil || len(text) != maxSessionReply+1 {
		t.Fatalf("a reply past 4 MiB within the bound: %d bytes, %v", len(text), err)
	}
	size.Store(int64(limit + 1))
	if _, err := c.observe(t.Context(), "messages", map[string]string{"run": "r1"}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a reply past the bound: %v", err)
	}
}

// A check's log is printed as the server gave it, so it matches the
// SHA-256 printed above it: one with no final newline gains none, one
// with several keeps them all, and the MCP tool's text is the same; a
// JSON answer ends with one newline.
func TestObservePrintsALogsBytesAsTheyCame(t *testing.T) {
	clearConductorEnv(t)
	var log atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/log") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"runs":[]}`+"\n\n")
			return
		}
		b := log.Load().(string)
		sum := sha256.Sum256([]byte(b))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Conductor-Log-SHA256", hex.EncodeToString(sum[:]))
		_, _ = io.WriteString(w, b)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "agent-tok")
	t.Setenv(envBrakeman, "br-1")
	keys := []string{"--run", "r1", "--task", "build", "--n", "1", "--id", "test", "--k", "1"}
	for _, b := range []string{"ok", "FAIL\n\n\n", "\n", ""} {
		log.Store(b)
		sum := sha256.Sum256([]byte(b))
		want := "sha256 " + hex.EncodeToString(sum[:]) + "\n\n" + b
		code, out, _, err := brakemanCmd(t, "", append([]string{"observe", "log"}, keys...)...)
		if code != 0 || err != nil || out != want {
			t.Errorf("the log %q printed: %d %v %q, want %q", b, code, err, out, want)
		}
		got, _ := strings.CutPrefix(out, "sha256 "+hex.EncodeToString(sum[:])+"\n\n")
		if s := sha256.Sum256([]byte(got)); s != sum {
			t.Errorf("the log %q printed does not match its SHA-256", b)
		}
		replies := mcpRun(t, brakemanEnv(srv.URL, "s1", "agent-tok"),
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe","arguments":{"what":"log","query":{"run":"r1","task":"build","n":"1","id":"test","k":"1"}}}}`)
		if text, isErr := toolText(t, replies[0]); isErr || text != want {
			t.Errorf("the tool's text for %q: %q %v", b, text, isErr)
		}
	}
	if code, out, _, err := brakemanCmd(t, "", "observe", "runs"); code != 0 || err != nil || out != `{"runs":[]}`+"\n" {
		t.Errorf("a JSON answer printed: %d %v %q", code, err, out)
	}
}

// The session commands outside a session, and in a session that is no
// Brakeman turn's, say so and exit 1, sending nothing; a usage error is
// 2.
func TestBrakemanOutsideASessionIsLoud(t *testing.T) {
	clearConductorEnv(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "brief.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmds := [][]string{{"request"}, {"observe", "changes"}, {"brief", "--file", file}}
	for _, args := range cmds {
		if code, _, _, err := brakemanCmd(t, "", args...); code != 1 || err == nil || !strings.Contains(err.Error(), "not in a Conductor session") {
			t.Errorf("%q outside a session: %d %v", args, code, err)
		}
	}
	t.Setenv("CONDUCTOR_NOTIFY_URL", srv.URL+"/api/sessions/s1/attention")
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "tok")
	for _, args := range cmds {
		if code, _, _, err := brakemanCmd(t, "", args...); code != 1 || err == nil || !strings.Contains(err.Error(), "not a Brakeman turn's") {
			t.Errorf("%q in a worker's session: %d %v", args, code, err)
		}
	}
	t.Setenv(envBrakeman, "br-1")
	for _, args := range [][]string{{"brief"}, {"observe"}, {"observe", "--run", "r1"}, {"observe", "run", "--run"}, {"observe", "run", "run"}, {"observe", "run", "--run", "a", "--run", "b"}, {"request", "extra"}} {
		if code, _, _, _ := brakemanCmd(t, "", args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

// conductor brakeman role (docs/plans/brakeman.md, PR-15), with the
// workbench token: on sets the background mode at the revision read and
// says that Brakeman off wakes nothing; status says the role's state;
// pause and resume are the role's own route's; off sets the mode back;
// an action of none is a usage error before anything is sent.
func TestBrakemanRoleWithTheWorkbenchToken(t *testing.T) {
	a := newBrakemanAPI(t)
	admin := []string{"--server", a.hs.URL, "--token", dispatchToken}
	before := a.count()
	if code, _, _, err := brakemanCmd(t, "", append([]string{"role", "sleep"}, admin...)...); code != 2 || err == nil {
		t.Fatalf("an unknown action: %d %v", code, err)
	}
	if a.count() != before {
		t.Fatal("a usage error sent a request")
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "on"}, admin...)...); code != 0 || err != nil || !strings.Contains(out, "Brakeman is off: nothing wakes it") {
		t.Fatalf("on while off: %d %v %q", code, err, out)
	}
	a.enableBrakeman(t)
	cfg := a.admin(t, "GET", "/api/brakeman", nil, http.StatusOK)["config"].(map[string]any)
	if cfg["wake"].(map[string]any)["mode"] != "background" {
		t.Fatalf("%v", cfg["wake"])
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "on"}, admin...)...); code != 0 || err != nil || out != "the background role is on: a window of 120 s, a cooldown of 30 min, 40 turns and 240 minutes a day\n" {
		t.Fatalf("on: %d %v %q", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "status"}, admin...)...); code != 0 || err != nil || !hasAll(out, "role: idle", "today: 0 minutes") {
		t.Fatalf("status: %d %v\n%s", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "pause"}, admin...)...); code != 0 || err != nil || out != "the background role is paused\n" {
		t.Fatalf("pause: %d %v %q", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "status", "--json"}, admin...)...); code != 0 || err != nil || !strings.Contains(out, `"state":"paused"`) {
		t.Fatalf("status --json: %d %v %q", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "status"}, admin...)...); code != 0 || err != nil || !strings.Contains(out, "role: paused: paused by a person") {
		t.Fatalf("status paused: %d %v\n%s", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "resume"}, admin...)...); code != 0 || err != nil || out != "the background role is idle\n" {
		t.Fatalf("resume: %d %v %q", code, err, out)
	}
	if code, out, _, err := brakemanCmd(t, "", append([]string{"role", "off"}, admin...)...); code != 0 || err != nil || !strings.Contains(out, "runs only when asked") {
		t.Fatalf("off: %d %v %q", code, err, out)
	}
	// --json prints each action's reply as its route gave it.
	for _, step := range []struct{ action, want string }{
		{"on", `"mode":"background"`}, {"pause", `"state":"paused"`}, {"resume", `"state":"idle"`}, {"off", `"mode":"invoked"`},
	} {
		code, out, _, err := brakemanCmd(t, "", append([]string{"role", step.action, "--json"}, admin...)...)
		var reply map[string]any
		if code != 0 || err != nil || json.Unmarshal([]byte(out), &reply) != nil || !strings.Contains(out, step.want) {
			t.Fatalf("%s --json: %d %v %q", step.action, code, err, out)
		}
	}
	cfg = a.admin(t, "GET", "/api/brakeman", nil, http.StatusOK)["config"].(map[string]any)
	if cfg["wake"].(map[string]any)["mode"] != "invoked" {
		t.Fatalf("%v", cfg["wake"])
	}
}
