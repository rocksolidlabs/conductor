package api

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// keyedSource saves the agent a clone test copies: everything the Agents
// page has no control for, and a stored value.
func keyedSource(e *testEnv) {
	e.t.Helper()
	src := agentBody("keyed")
	src["name"] = "Keyed"
	src["description"] = "the source"
	src["site"] = "https://example.test"
	src["env"] = map[string]string{"API_KEY": "secret", "REGION": "eu"}
	src["envPassthrough"] = []string{"HTTP_PROXY"}
	src["adapter"] = "claude"
	src["probe"] = false
	src["cwd"] = e.root
	src["icon"] = "i-lucide-sparkles"
	src["signal"] = map[string]any{"kind": "hook", "toolEvents": true, "workingTitle": "^Working"}
	src["yolo"] = map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO": "1"}}
	src["trustPrompt"] = `Trust\?`
	src["promptShape"] = `(?m)^› .*\z`
	src["trustAnswers"] = []map[string]any{{"label": "Yes", "input": "y"}, {"label": "No", "input": "n"}}
	src["questions"] = []map[string]any{{"prompt": "Hooks need review", "answers": []map[string]any{{"label": "Allow", "input": "1"}}}}
	src["session"] = map[string]any{"startArgs": []string{"--session-id", "{id}"}, "idFrom": "hook", "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]+$"}
	e.save(src)
}

// clone posts a copy of from to /api/catalog/{from}/clone and returns the
// status and the reply.
func (e *testEnv) clone(from string, body map[string]any) (int, map[string]any) {
	e.t.Helper()
	resp, out := e.do("POST", "/api/catalog/"+from+"/clone", adminToken, body)
	return resp.StatusCode, out
}

// A clone (#291) is a saved agent of its own, made from what the form sends
// (name, id, command, the environment with a stored value masked and one of
// its own) and everything the form has no control for, taken from the
// source: its adapter, signal, working directory, icon, yolo and session
// recipes, trust prompt and answers, startup questions and probe. The masked
// value is the source's, copied and never shown; what the form left out
// (a description, a site, a passthrough name, another stored value) stays
// out. The source is untouched, and the copy can be copied again.
func TestCatalogCloneCopiesTheWholeAgent(t *testing.T) {
	e := newTestEnv(t, nil)
	keyedSource(e)

	body := map[string]any{"id": "keyed-work", "name": "Keyed (work)", "command": []string{"/bin/cat"}, "allowArgs": true, "env": map[string]string{"API_KEY": "***", "CLAUDE_CONFIG_DIR": "/tmp/work"}}
	status, out := e.clone("keyed", body)
	if status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	got := out["agent"].(map[string]any)
	if env := got["env"].(map[string]any); len(env) != 2 || env["API_KEY"] != "***" || env["CLAUDE_CONFIG_DIR"] != "***" {
		t.Fatalf("the reply's env: %v", env)
	}
	if got["source"] != "saved" || got["replaces"] != nil || got["adapter"] != "claude" || got["name"] != "Keyed (work)" || got["id"] != "keyed-work" {
		t.Fatalf("the reply: %v", got)
	}
	if got["description"] != nil || got["site"] != nil || got["envPassthrough"] != nil {
		t.Fatalf("the form's fields came from the source: %v", got)
	}

	a, ok := e.srv.Catalog().Get("keyed-work")
	if !ok {
		t.Fatal("the copy is not in the catalog")
	}
	want := catalog.Agent{
		ID: "keyed-work", Name: "Keyed (work)", Command: []string{"/bin/cat"}, AllowArgs: true,
		Env:          map[string]string{"API_KEY": "secret", "CLAUDE_CONFIG_DIR": "/tmp/work"},
		Cwd:          e.root,
		Icon:         "i-lucide-sparkles",
		Adapter:      "claude",
		Signal:       &catalog.Signal{Kind: catalog.SignalHook, ToolEvents: true, WorkingTitle: "^Working"},
		Yolo:         &catalog.Yolo{Args: []string{"--yes"}, Env: map[string]string{"YOLO": "1"}},
		TrustPrompt:  `Trust\?`,
		PromptShape:  `(?m)^› .*\z`,
		TrustAnswers: []catalog.Answer{{Label: "Yes", Input: "y"}, {Label: "No", Input: "n"}},
		Questions:    []catalog.Question{{Prompt: "Hooks need review", Answers: []catalog.Answer{{Label: "Allow", Input: "1"}}}},
		Session:      &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"},
	}
	off := false
	want.Probe = &off
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("the copy runs as\n%+v\nwant\n%+v", a, want)
	}
	if e.srv.Catalog().Source("keyed-work") != catalog.SourceSaved || e.srv.base.Source("keyed-work") != "" {
		t.Fatalf("the copy's source: %q replaces %q", e.srv.Catalog().Source("keyed-work"), e.srv.base.Source("keyed-work"))
	}
	if f := e.overlayFile(); strings.Contains(f, "***") || !strings.Contains(f, `"keyed-work"`) {
		t.Fatalf("catalog.json:\n%s", f)
	}
	if src, _ := e.srv.Catalog().Get("keyed"); src.Description != "the source" || !reflect.DeepEqual(src.Env, map[string]string{"API_KEY": "secret", "REGION": "eu"}) {
		t.Fatalf("the source changed: %+v", src)
	}
	if ids := e.catalogIDs(); !slices.Equal(ids, []string{"cat", "sh", "exit", "keyed", "keyed-work"}) {
		t.Fatalf("listed %v", ids)
	}

	// The copy copied: the stored value travels on, the recipe with it.
	status, out = e.clone("keyed-work", map[string]any{"id": "keyed-home", "name": "Keyed (home)", "command": []string{"/bin/cat"}, "env": map[string]string{"API_KEY": "***"}})
	if status != http.StatusCreated {
		t.Fatalf("clone of the copy: %d %v", status, out)
	}
	if b, _ := e.srv.Catalog().Get("keyed-home"); b.Env["API_KEY"] != "secret" || b.Adapter != "claude" || b.Session == nil || b.Session.IDFrom != "hook" {
		t.Fatalf("the second copy runs as %+v", b)
	}

	// A body that sends the fields keeps its own: nothing of the source
	// overrides what the copy says, and an explicit empty recipe is none.
	status, out = e.clone("keyed", map[string]any{"id": "keyed-plain", "name": "Keyed (plain)", "command": []string{"/bin/sh"}, "description": "mine", "site": "https://mine.test", "signal": map[string]any{"kind": "bell"}, "yolo": map[string]any{}, "session": map[string]any{}, "trustPrompt": "Mine", "probe": true})
	if status != http.StatusCreated {
		t.Fatalf("clone with its own fields: %d %v", status, out)
	}
	c, _ := e.srv.Catalog().Get("keyed-plain")
	if c.Description != "mine" || c.Site != "https://mine.test" || c.Signal == nil || c.Signal.Kind != catalog.SignalBell || c.Signal.WorkingTitle != "^Working" || !c.Yolo.Empty() || c.Yolo == nil || c.Session == nil || !c.Session.Empty() || c.TrustPrompt != "Mine" || c.Probe == nil || !*c.Probe || len(c.Env) != 0 || c.Adapter != "claude" {
		t.Fatalf("the copy with its own fields runs as %+v (signal %+v)", c, c.Signal)
	}
}

// A copy of an override that follows the configured agent's value (the mask
// stored for it) gets that value as it is now, copied: the copy replaces
// nothing and follows nothing, so a later rotation in the config reaches the
// override and not the copy.
func TestCatalogCloneTakesTheConfiguredValueAndStopsFollowingIt(t *testing.T) {
	e := newTestEnv(t, nil)
	start := func(secret string) *testEnv {
		t.Helper()
		base, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
			{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}},
			{ID: "keyed", Name: "keyed", Command: []string{"/bin/cat"}, Adapter: "claude", Probe: new(bool),
				Env: map[string]string{"API_KEY": secret, "REGION": "eu"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		srv, err := New(e.srv.cfg, base, e.srv.log, nil, e.srv.store)
		if err != nil {
			t.Fatal(err)
		}
		return e.serve(srv)
	}
	v1 := start("v1")
	// The admin overrides keyed, keeping API_KEY as read (the mask: the
	// config's value) and setting a region of their own.
	listed := v1.catalogAgent("keyed")
	listed["env"] = map[string]any{"API_KEY": "***", "REGION": "us"}
	v1.save(listed)
	status, out := v1.clone("keyed", map[string]any{"id": "keyed-work", "name": "Keyed (work)", "command": []string{"/bin/cat"}, "env": map[string]string{"API_KEY": "***", "REGION": "***", "EXTRA": "x"}})
	if status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	if a, _ := v1.srv.Catalog().Get("keyed-work"); !reflect.DeepEqual(a.Env, map[string]string{"API_KEY": "v1", "REGION": "us", "EXTRA": "x"}) || a.Adapter != "claude" {
		t.Fatalf("the copy runs with %+v", a)
	}
	if got := v1.catalogAgent("keyed-work"); got["source"] != "saved" || got["replaces"] != nil {
		t.Fatalf("listed %v", got)
	}
	var ov catalog.Overlay
	if ok, err := v1.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 2 || savedAgent(ov.Agents, "keyed-work").Env["API_KEY"] != "v1" || savedAgent(ov.Agents, "keyed").Env["API_KEY"] != "***" {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	// The config rotates the secret: the override follows, the copy keeps
	// what it copied.
	v2 := start("v2")
	if a, _ := v2.srv.Catalog().Get("keyed"); a.Env["API_KEY"] != "v2" {
		t.Fatalf("the override runs with %v", a.Env)
	}
	if a, _ := v2.srv.Catalog().Get("keyed-work"); a.Env["API_KEY"] != "v1" {
		t.Fatalf("the copy runs with %v", a.Env)
	}
}

// What a clone refuses, with nothing saved: an unknown source (404), an id
// that is the source's, another agent's, a saved one's, a hidden one's or a
// hidden built-in's (409 id_taken), a masked value the source has none for,
// a bad id, an unknown adapter (400 invalid_agent), and a body with a field
// no agent has (400 invalid_request).
func TestCatalogCloneRefusals(t *testing.T) {
	e := newTestEnv(t, nil)
	keyedSource(e)
	if status := e.del("exit"); status != http.StatusNoContent {
		t.Fatalf("hide exit: %d", status)
	}
	before := e.overlayFile()
	cases := []struct {
		name, from string
		body       map[string]any
		status     int
		code, msg  string
	}{
		{"unknown source", "nobody", agentBody("fresh"), http.StatusNotFound, "not_found", "no such agent"},
		{"the source's id", "keyed", agentBody("keyed"), http.StatusConflict, "id_taken", "an agent with the id keyed exists"},
		{"a configured id", "keyed", agentBody("sh"), http.StatusConflict, "id_taken", "sh exists"},
		{"a hidden configured id", "keyed", agentBody("exit"), http.StatusConflict, "id_taken", "exit exists"},
		{"a bad id", "keyed", agentBody("Bad Id"), http.StatusBadRequest, "invalid_agent", "id"},
		{"a masked value the source lacks", "keyed", map[string]any{"id": "fresh", "name": "fresh", "command": []string{"/bin/cat"}, "env": map[string]string{"API_KEY": "***", "ZED": "***", "NOPE": "***"}}, http.StatusBadRequest, "invalid_agent", "env NOPE: value is redacted, and the agent copied has none for it; set a real value"},
		{"an unknown adapter", "keyed", map[string]any{"id": "fresh", "name": "fresh", "command": []string{"/bin/cat"}, "adapter": "gemini"}, http.StatusBadRequest, "invalid_agent", "gemini"},
		{"a field no agent has", "keyed", map[string]any{"id": "fresh", "name": "fresh", "command": []string{"/bin/cat"}, "bogus": true}, http.StatusBadRequest, "invalid_request", "bogus"},
	}
	for _, tc := range cases {
		status, out := e.clone(tc.from, tc.body)
		apiErr, _ := out["error"].(map[string]any)
		if status != tc.status || apiErr["code"] != tc.code || !strings.Contains(apiErr["message"].(string), tc.msg) {
			t.Errorf("%s: %d %v, want %d %s %q", tc.name, status, out, tc.status, tc.code, tc.msg)
		}
	}
	if got := e.overlayFile(); got != before {
		t.Fatalf("a refused clone changed catalog.json:\n%s\nwas:\n%s", got, before)
	}
	if ids := e.catalogIDs(); !slices.Equal(ids, []string{"cat", "sh", "keyed"}) {
		t.Fatalf("listed %v", ids)
	}
	// A saved id, once a copy exists.
	if status, out := e.clone("keyed", agentBody("keyed-work")); status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	if status, out := e.clone("cat", agentBody("keyed-work")); status != http.StatusConflict || out["error"].(map[string]any)["code"] != "id_taken" {
		t.Fatalf("a saved id: %d %v", status, out)
	}
	// Without a store, as every edit.
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status, out := e.serve(srv).clone("cat", agentBody("fresh")); status != http.StatusServiceUnavailable || out["error"].(map[string]any)["code"] != "store_unavailable" {
		t.Fatalf("without a store: %d %v", status, out)
	}
}

// A launch of the copy runs with the copy's environment, the source's stored
// value included, and a copy of Claude Code under a CLAUDE_CONFIG_DIR of its
// own gets the Conductor skill there as well as under the home, once per
// directory since the server started; a relative directory gets nothing.
func TestCatalogCloneLaunchesWithItsEnvAndSkill(t *testing.T) {
	e := newTestEnv(t, nil)
	home := e.srv.home
	if home == "" || home == os.Getenv("HOME") {
		t.Fatalf("the test server's home is %q", home)
	}
	src := agentBody("claude-like")
	src["command"] = []string{"/bin/sh", "-c", `echo "K=[$API_KEY] D=[$CLAUDE_CONFIG_DIR] END"; exec /bin/cat`}
	src["env"] = map[string]string{"API_KEY": "secret"}
	src["adapter"] = "claude"
	src["probe"] = false
	e.save(src)
	cfg := filepath.Join(t.TempDir(), "work")
	if status, out := e.clone("claude-like", map[string]any{"id": "claude-work", "name": "Claude (work)", "command": src["command"], "env": map[string]string{"API_KEY": "***", "CLAUDE_CONFIG_DIR": cfg}}); status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	id := e.createSession("claude-work")
	c := dialViewer(t, e, id, adminToken)
	c.hello(80, 24)
	c.expectOutput("K=[secret] D=[" + cfg + "] END")
	skill := filepath.Join(cfg, "skills", "conductor", "SKILL.md")
	if b, err := os.ReadFile(skill); err != nil || string(b) != agents.Skill {
		t.Fatalf("the skill under the copy's configuration directory: %v\n%s", err, b)
	}
	if fi, err := os.Stat(cfg); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the configuration directory made: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "conductor", "SKILL.md")); err != nil || string(b) != agents.Skill {
		t.Fatalf("the skill under the home: %v\n%s", err, b)
	}
	// Once per directory since the server started, as under the home.
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	e.createSession("claude-work")
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatalf("the skill came back within the same server start: %v", err)
	}
	// A relative directory is the agent's to resolve: nothing is made.
	if status, out := e.clone("claude-like", map[string]any{"id": "claude-rel", "name": "Claude (relative)", "command": src["command"], "env": map[string]string{"CLAUDE_CONFIG_DIR": "work"}}); status != http.StatusCreated {
		t.Fatalf("clone: %d %v", status, out)
	}
	e.createSession("claude-rel")
	if _, err := os.Stat(filepath.Join(e.root, "work")); !os.IsNotExist(err) {
		t.Fatalf("a relative configuration directory was made: %v", err)
	}
}

// The browser an agent's links open in (#436) is saved, shown unmasked in
// the catalog, kept by a clone whose body says nothing of it (a page older
// than its control), and replaced by one that does ({} the default); a bad
// one is refused.
func TestCatalogBrowserIsSavedShownAndCloned(t *testing.T) {
	e := newTestEnv(t, nil)
	src := agentBody("work")
	src["browser"] = map[string]any{"name": "edge", "profile": "Profile 2"}
	e.save(src)
	if b, _ := e.catalogAgent("work")["browser"].(map[string]any); b["name"] != "edge" || b["profile"] != "Profile 2" {
		t.Fatalf("the catalog's agent: %v", e.catalogAgent("work"))
	}
	status, out := e.clone("work", map[string]any{"id": "work-copy", "name": "work copy", "command": []string{"/bin/cat"}})
	if b, _ := out["agent"].(map[string]any)["browser"].(map[string]any); status != http.StatusCreated || b["name"] != "edge" || b["profile"] != "Profile 2" {
		t.Fatalf("a clone that says nothing of the browser: %d %v", status, out)
	}
	status, out = e.clone("work", map[string]any{"id": "work-fox", "name": "work fox", "command": []string{"/bin/cat"}, "browser": map[string]any{"name": "firefox"}})
	if b, _ := out["agent"].(map[string]any)["browser"].(map[string]any); status != http.StatusCreated || b["name"] != "firefox" || b["profile"] != nil {
		t.Fatalf("a clone with a browser of its own: %d %v", status, out)
	}
	status, out = e.clone("work", map[string]any{"id": "work-default", "name": "work default", "command": []string{"/bin/cat"}, "browser": map[string]any{}})
	if a, _ := e.srv.Catalog().Get("work-default"); status != http.StatusCreated || a.Browser.Named() {
		t.Fatalf("a clone with the default browser: %d %v", status, out)
	}
	bad := agentBody("bad")
	bad["browser"] = map[string]any{"name": "firefox", "profile": "work"}
	if resp, out := e.do("POST", "/api/catalog", adminToken, bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a profile for firefox: %d %v", resp.StatusCode, out)
	}
	if status, out := e.clone("work", map[string]any{"id": "work-bad", "name": "work bad", "command": []string{"/bin/cat"}, "browser": map[string]any{"name": "lynx"}}); status != http.StatusBadRequest {
		t.Fatalf("a clone with an unknown browser: %d %v", status, out)
	}
}
