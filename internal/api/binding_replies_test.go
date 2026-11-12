package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent's member of its own tool is compared with its session by
// effective login: a session whose launch set its login adds no member of
// its tool; two entries sharing one folder with different keys are two
// logins, so a session of one adds a member of its own entry and none of
// the other; on form_crew and add_member both. (A session on a registered
// account adding a plain member, refused a pinned one, and a session of a
// pinned entry adding a pinned member, refused a plain one:
// TestSelfCrewAdoptsSessionBinding.)
func TestSelfCrewEffectiveLogin(t *testing.T) {
	e := newBindEnv(t, nil)
	shared := realRoot(t, t.TempDir())
	e.addAgents(t, map[string]map[string]string{
		"claude-k1": {"CLAUDE_CONFIG_DIR": shared, "ANTHROPIC_API_KEY": "made-up-key-5"},
		"claude-k2": {"CLAUDE_CONFIG_DIR": shared, "ANTHROPIC_API_KEY": "made-up-key-6"},
	})
	member := func(name, agent string) map[string]any {
		return map[string]any{"name": name, "agentId": agent, "prompt": "", "start": map[string]any{"when": "manual"}}
	}
	self := map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}}
	crewOf := func(name string, members ...any) map[string]any {
		return map[string]any{"name": name, "goal": "g", "self": "me", "members": append([]any{self}, members...)}
	}
	// A session whose launch set its login.
	set := e.launch(t, "claude-yolo", map[string]any{"yolo": true})
	stok := e.agentToken(set)
	e.expectAs(t, stok, "POST", "/api/sessions/"+set+"/crew", crewOf("Set", member("twin", "claude-p")), http.StatusBadRequest, "invalid_account")
	e.expectAs(t, stok, "POST", "/api/sessions/"+set+"/crew", crewOf("Set2", member("other", "codex-p")), http.StatusCreated, "")
	e.expectAs(t, stok, "POST", "/api/sessions/"+set+"/run/members", member("twin", "claude-p"), http.StatusBadRequest, "invalid_account")
	// One folder, two keys.
	k1 := e.launch(t, "claude-k1", nil)
	ktok := e.agentToken(k1)
	e.expectAs(t, ktok, "POST", "/api/sessions/"+k1+"/crew", crewOf("K", member("k2", "claude-k2")), http.StatusBadRequest, "invalid_account")
	out := e.expectAs(t, ktok, "POST", "/api/sessions/"+k1+"/crew", crewOf("K2", member("k1", "claude-k1")), http.StatusCreated, "")
	if got := acct(runMember(t, out["run"].(map[string]any), "k1")["account"]); got != "default/pinned" {
		t.Fatalf("the member of its own entry: %s", got)
	}
	e.expectAs(t, ktok, "POST", "/api/sessions/"+k1+"/run/members", member("k2", "claude-k2"), http.StatusBadRequest, "invalid_account")
}

// A run answered from its record, its members on a registered account and
// on a pinned entry, carries no folder, label, fingerprint or binding in
// any reply: the admin's GET /api/runs and GET /api/runs/{run}, and
// Brakeman's reads of both (observe runs, observe run).
func TestRunRepliesCarryNoFolder(t *testing.T) {
	f := newObsFixture(t)
	path := filepath.Join(f.srv.store.Dir(), "runs", f.run+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["members"] = []any{
		map[string]any{"name": "lead", "agentId": "cat", "start": map[string]any{"when": "task"}, "status": "ended", "account": map[string]any{"id": "a-aaaaaaaaaaaa", "source": "account"}},
		map[string]any{"name": "pin", "agentId": "cat", "start": map[string]any{"when": "task"}, "status": "ended", "account": map[string]any{"id": "default", "source": "pinned"}},
	}
	doc["bound"] = map[string]any{
		"lead": map[string]any{"id": "a-aaaaaaaaaaaa", "label": "Zq-Label", "folder": "/zq/folder-a", "source": "account", "login": "zqfingerprint-a"},
		"pin":  map[string]any{"id": "default", "folder": "/zq/folder-pin", "source": "pinned", "login": "zqfingerprint-pin"},
	}
	if err := os.WriteFile(path+".new", []byte(mustJSON(doc)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.run}} })
	var replies []string
	for _, p := range []string{"/api/runs", "/api/runs/" + f.run} {
		_, data, _ := f.call(t, "GET", p, nil)
		replies = append(replies, string(data))
		replies = append(replies, mustJSON(f.expectRead(t, p, http.StatusOK, "")))
	}
	if !strings.Contains(replies[2], "a-aaaaaaaaaaaa") {
		t.Fatalf("the run from its record names no account: %.600s", replies[2])
	}
	for _, r := range replies {
		for _, leak := range []string{"Zq-Label", "/zq/", "zqfingerprint", `"bound"`} {
			if strings.Contains(r, leak) {
				t.Fatalf("a reply carries %q: %.600s", leak, r)
			}
		}
	}
}
