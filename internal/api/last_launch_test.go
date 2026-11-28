package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// lastLaunchOf reads GET /api/launch/last as the admin.
func lastLaunchOf(t *testing.T, e *testEnv) lastLaunch {
	t.Helper()
	req, _ := http.NewRequest("GET", e.http.URL+"/api/launch/last", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/launch/last: %d", resp.StatusCode)
	}
	var d lastLaunch
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// launchFor launches agent in folder with model and ends the session at the test's end.
func launchFor(t *testing.T, e *testEnv, body map[string]any) {
	t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch %v: %d %v", body, resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(t.Context())
		}
	})
}

// The record answers empty before any launch, and after one holds its agent, its folder and its model, never its arguments, its
// name, its task, its account or yolo.
func TestLastLaunchKeepsTheAgentTheFolderAndTheModel(t *testing.T) {
	e := newTestEnv(t, nil)
	if d := lastLaunchOf(t, e); d.V != 1 || d.Agent != "" || d.Folder != "" || len(d.Models) != 0 {
		t.Fatalf("before any launch: %+v", d)
	}
	sub := filepath.Join(e.root, "work")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	launchFor(t, e, map[string]any{"agentId": "sh", "cwd": sub, "args": []string{"-c", "sleep 30 # secret-key"}, "name": "named launch", "yolo": true})
	d := lastLaunchOf(t, e)
	if d.Agent != "sh" || d.Folder != sub || !reflect.DeepEqual(d.Models["sh"], lastLaunchModel{Model: "", Names: []string{}, Way: "terminal"}) {
		t.Fatalf("after a launch: %+v", d)
	}
	raw, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "launch", lastLaunchDoc))
	if err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{"secret-key", "named launch", "yolo", "args", "account", "message"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("the record holds %q: %s", never, raw)
		}
	}
	// A launch that names no folder is recorded with none: the server's default.
	launchFor(t, e, map[string]any{"agentId": "cat"})
	if d := lastLaunchOf(t, e); d.Agent != "cat" || d.Folder != "" || !reflect.DeepEqual(d.Order, []string{"sh", "cat"}) {
		t.Fatalf("a launch in the default folder: %+v", d)
	}
}

// A refused launch records nothing.
func TestLastLaunchIsNotKeptForARefusedLaunch(t *testing.T) {
	e := newTestEnv(t, nil)
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "sh", "cwd": "/"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a folder outside the roots: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "nobody"}); resp.StatusCode == http.StatusCreated {
		t.Fatal("an agent the catalog does not hold launched")
	}
	if d := lastLaunchOf(t, e); d.Agent != "" || len(d.Models) != 0 {
		t.Fatalf("a refused launch was kept: %+v", d)
	}
}

// The record outlasts the server: another over the same data directory, as `conductor serve` comes up again, reads it back.
func TestLastLaunchOutlastsARestart(t *testing.T) {
	e := newTestEnv(t, nil)
	launchFor(t, e, map[string]any{"agentId": "sh", "args": []string{"-c", "sleep 30"}})
	if d := lastLaunchOf(t, e.restart()); d.Agent != "sh" {
		t.Fatalf("after a restart: %+v", d)
	}
}

// Each agent keeps its last model and the last five names, newest first, each once; the built-in shell is never the agent to start
// with; the agents kept are bounded, the one launched longest ago let go first.
func TestLastLaunchAfter(t *testing.T) {
	d := emptyLastLaunch()
	for _, m := range []string{"a", "b", "c", "a", "d", "e", "f"} {
		d = d.after("claude", "/w", m, "")
	}
	if got := d.Models["claude"]; got.Model != "f" || !reflect.DeepEqual(got.Names, []string{"f", "e", "d", "a", "c"}) {
		t.Fatalf("names: %+v", got)
	}
	d = d.after("claude", "/w", " ", "")
	if got := d.Models["claude"]; got.Model != "" || len(got.Names) != lastLaunchNames {
		t.Fatalf("a launch with no model keeps the names: %+v", got)
	}
	d = d.after(lastLaunchShell, "/x", "", "")
	if d.Agent != "claude" || d.Folder != "/x" {
		t.Fatalf("the shell: %+v", d)
	}
	for i := 0; i <= lastLaunchAgents; i++ {
		d = d.after("agent-"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "", "", "")
	}
	if len(d.Models) != lastLaunchAgents || len(d.Order) != lastLaunchAgents || !d.sound() {
		t.Fatalf("bounded: %d models, %d in order", len(d.Models), len(d.Order))
	}
	if _, ok := d.Models["claude"]; ok {
		t.Fatal("the agent launched longest ago was kept past the bound")
	}
}

// A record that does not read (broken, past its bound, of another shape) is read as none, and the next launch writes a whole one.
func TestLastLaunchReadsABrokenRecordAsNone(t *testing.T) {
	e := newTestEnv(t, nil)
	dir := filepath.Join(e.srv.store.Dir(), "launch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"not json", `{"v":2,"models":{}}`, `{"v":1,"models":{"a":{"model":"","names":[]}},"order":[]}`, `{"v":1,"models":{},"extra":true}`, `{"v":1,"models":{}}` + strings.Repeat(" ", maxLastLaunch)} {
		if err := os.WriteFile(filepath.Join(dir, lastLaunchDoc), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if d := lastLaunchOf(t, e); !reflect.DeepEqual(d, emptyLastLaunch()) {
			t.Fatalf("%.40q read as %+v", body, d)
		}
	}
	launchFor(t, e, map[string]any{"agentId": "cat"})
	if d := lastLaunchOf(t, e); d.Agent != "cat" || !d.sound() {
		t.Fatalf("after a launch over a broken record: %+v", d)
	}
}

// Only the admin reads it.
func TestLastLaunchIsTheAdminsAlone(t *testing.T) {
	e := newTestEnv(t, nil)
	if resp, _ := e.do("GET", "/api/launch/last", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/launch/last", "test-host-token", nil); resp.StatusCode == http.StatusOK {
		t.Fatalf("a host token read it: %d", resp.StatusCode)
	}
}

// Each agent keeps the way it was last launched: a launch as a chat is kept as "protocol", any other as "terminal"; a way the
// record does not know reads it as none.
func TestLastLaunchKeepsEachAgentsWay(t *testing.T) {
	d := emptyLastLaunch().after("claude", "", "", "protocol").after("codex", "", "", "").after("aider", "", "", "anything")
	for id, want := range map[string]string{"claude": "protocol", "codex": "terminal", "aider": "terminal"} {
		if got := d.Models[id].Way; got != want {
			t.Errorf("%s: way %q, want %q", id, got, want)
		}
	}
	d = d.after("claude", "", "", "terminal")
	if d.Models["claude"].Way != "terminal" {
		t.Fatalf("the last way wins: %+v", d.Models["claude"])
	}
	bad := d
	bad.Models = map[string]lastLaunchModel{"claude": {Names: []string{}, Way: "elsewhere"}, "codex": d.Models["codex"], "aider": d.Models["aider"]}
	if bad.sound() {
		t.Fatal("a record with a way it does not know is sound")
	}
	// A launch over the route keeps its way; a model or a folder past its bound is kept as none.
	e := newTestEnv(t, nil)
	launchFor(t, e, map[string]any{"agentId": "sh", "args": []string{"-c", "sleep 30"}})
	if got := lastLaunchOf(t, e).Models["sh"].Way; got != "terminal" {
		t.Fatalf("a terminal launch kept the way %q", got)
	}
	long := emptyLastLaunch().after("claude", "/"+strings.Repeat("f", lastLaunchText), strings.Repeat("m", lastLaunchModelLen+1), "")
	if long.Folder != "" || long.Models["claude"].Model != "" || len(long.Models["claude"].Names) != 0 {
		t.Fatalf("past its bounds: %+v", long)
	}
}

// A trial keeps its agents, its folder and, for each of the last lastTrialFolders folders, its Check line's words, the newest first;
// a folder or a line past its bound is kept as none; a launch after it keeps the trial, and the trial keeps the launches.
func TestLastLaunchKeepsTheLastTrial(t *testing.T) {
	d := emptyLastLaunch().after("claude", "/w", "opus", "protocol")
	d = d.afterTrial([]string{"claude", "codex", "", "aider", "pi", "amp"}, " /repo ", []string{"npm", "test"})
	if !reflect.DeepEqual(d.Trial, &lastTrial{Agents: []string{"claude", "codex", "aider", "pi"}, Folder: "/repo", Checks: []lastTrialCheck{{Folder: "/repo", Check: []string{"npm", "test"}}}}) {
		t.Fatalf("a trial: %+v", d.Trial)
	}
	if d.Models["claude"].Way != "protocol" || d.Agent != "claude" {
		t.Fatalf("the trial kept the launches: %+v", d)
	}
	d = d.after("codex", "", "", "")
	if d.Trial == nil || d.Trial.Folder != "/repo" {
		t.Fatalf("a launch dropped the trial: %+v", d)
	}
	// The same folder again: its line replaced, first. Others in front of it, bounded, the oldest let go.
	d = d.afterTrial([]string{"claude", "codex"}, "/repo", nil)
	if c := d.Trial.Checks; len(c) != 1 || c[0].Folder != "/repo" || len(c[0].Check) != 0 {
		t.Fatalf("a trial with no Check line: %+v", c)
	}
	for i := range lastTrialFolders + 3 {
		d = d.afterTrial([]string{"claude", "codex"}, fmt.Sprintf("/f%d", i), []string{"make", fmt.Sprintf("t%d", i)})
	}
	if c := d.Trial.Checks; len(c) != lastTrialFolders || c[0].Folder != fmt.Sprintf("/f%d", lastTrialFolders+2) || !d.sound() {
		t.Fatalf("bounded: %d folders, first %+v", len(c), c[0])
	}
	d = d.afterTrial([]string{"claude", "codex"}, "/"+strings.Repeat("f", lastTrialFolderLen), []string{"true"})
	if d.Trial.Folder != "" || !d.sound() {
		t.Fatalf("a folder past its bound: %q", d.Trial.Folder)
	}
	d = d.afterTrial([]string{"claude", "codex"}, "/r", []string{strings.Repeat("x", lastTrialCheckLen)})
	if c := d.Trial.Checks[0]; c.Folder != "/r" || len(c.Check) != 0 || !d.sound() {
		t.Fatalf("a line past its bound: %+v", c)
	}
	over := d
	over.Trial = &lastTrial{Agents: []string{"a", "b", "c", "d", "e"}, Checks: []lastTrialCheck{}}
	if over.sound() {
		t.Fatal("a trial of five agents is sound")
	}
}

// The template's route keeps the trial it took: its agents, its folder and its Check line, never its task, what the agents were
// told, their approaches or arguments; a template refused keeps nothing.
func TestLastLaunchKeepsATrialTheServerTook(t *testing.T) {
	p := newCompareTicket(t)
	body := compareSpecBody("Compare the lookup", p.repo, 2, false, map[string]any{"checks": []map[string]any{{"id": "check", "argv": []string{"go", "test", "./..."}}, {"id": "more", "argv": []string{"make", "lint"}}}})
	body["candidates"] = []map[string]any{{"agentId": "ready-cat", "approach": "the secret approach", "args": []string{"--secret-arg"}}, {"agentId": "ready-cat"}}
	p.expect(t, "POST", "/api/crews/templates/compare", body, http.StatusCreated, "")
	p.srv.lastLaunchMu.Lock()
	d := p.srv.readLastLaunch()
	p.srv.lastLaunchMu.Unlock()
	if !reflect.DeepEqual(d.Trial, &lastTrial{Agents: []string{"ready-cat", "ready-cat"}, Folder: p.repo, Checks: []lastTrialCheck{{Folder: p.repo, Check: []string{"go", "test", "./..."}}}}) {
		t.Fatalf("the trial kept: %+v", d.Trial)
	}
	raw, err := os.ReadFile(filepath.Join(p.srv.store.Dir(), "launch", lastLaunchDoc))
	if err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{"Make the lookup fast", "Make it fast", "secret approach", "--secret-arg", "lint", "Compare the lookup"} {
		if strings.Contains(string(raw), never) {
			t.Errorf("the record holds %q: %s", never, raw)
		}
	}
}

// POST /api/launch/chat-noted marks the note on Chat's terms drawn, for every later page, after a restart too; its body is {} and
// nothing else, bounded; only the admin may.
func TestLastLaunchChatNoted(t *testing.T) {
	e := newTestEnv(t, nil)
	if lastLaunchOf(t, e).ChatNoted {
		t.Fatal("noted before any note")
	}
	for _, body := range []any{map[string]any{"noted": true}, "x", strings.Repeat(" ", maxChatNotedBody) + "{}"} {
		if resp, _ := e.do("POST", "/api/launch/chat-noted", adminToken, body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%.30v: %d", body, resp.StatusCode)
		}
	}
	if resp, _ := e.do("POST", "/api/launch/chat-noted", "test-host-token", map[string]any{}); resp.StatusCode == http.StatusNoContent {
		t.Fatal("a host token marked it")
	}
	if lastLaunchOf(t, e).ChatNoted {
		t.Fatal("a refused mark was kept")
	}
	if resp, _ := e.do("POST", "/api/launch/chat-noted", adminToken, map[string]any{}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the mark: %d", resp.StatusCode)
	}
	launchFor(t, e, map[string]any{"agentId": "cat"})
	if d := lastLaunchOf(t, e.restart()); !d.ChatNoted || d.Agent != "cat" {
		t.Fatalf("after a launch and a restart: %+v", d)
	}
}
