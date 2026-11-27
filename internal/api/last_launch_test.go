package api

import (
	"encoding/json"
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
	if d.Agent != "sh" || d.Folder != sub || !reflect.DeepEqual(d.Models["sh"], lastLaunchModel{Model: "", Names: []string{}}) {
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
		d = d.after("claude", "/w", m)
	}
	if got := d.Models["claude"]; got.Model != "f" || !reflect.DeepEqual(got.Names, []string{"f", "e", "d", "a", "c"}) {
		t.Fatalf("names: %+v", got)
	}
	d = d.after("claude", "/w", " ")
	if got := d.Models["claude"]; got.Model != "" || len(got.Names) != lastLaunchNames {
		t.Fatalf("a launch with no model keeps the names: %+v", got)
	}
	d = d.after(lastLaunchShell, "/x", "")
	if d.Agent != "claude" || d.Folder != "/x" {
		t.Fatalf("the shell: %+v", d)
	}
	for i := 0; i <= lastLaunchAgents; i++ {
		d = d.after("agent-"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "", "")
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
