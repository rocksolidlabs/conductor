package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	var sid string
	var local *session.Local
	for sid == "" {
		a.srv.Registry().Each(func(d session.Driver) {
			if ref := d.Info().Crew; ref != nil && ref.Brakeman != nil && ref.Brakeman.Request == id {
				sid, local = d.Info().ID, d.(*session.Local)
			}
		})
		if sid == "" {
			if t.Context().Err() != nil {
				t.Fatal("the turn's session never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
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
