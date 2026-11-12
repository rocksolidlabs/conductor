package api

import (
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// An agent's crew: the members an agent sends name no account, the one it
// becomes included, in every spelling of the key the decoder takes; the
// body is refused before anything is saved.
func TestSelfMemberNamesNoAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", nil)
	tok := e.agentToken(sid)
	for _, key := range []string{"account", "Account", "ACCOUNT"} {
		name := "Mine " + key
		self := map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}, key: a}
		other := map[string]any{"name": "x", "agentId": "codex-p", "prompt": "", "start": map[string]any{"when": "manual"}}
		e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": name, "goal": "g", "self": "me", "members": []any{self, other}}, http.StatusBadRequest, "invalid_account")
		sums, _, err := e.srv.crews.Summaries()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sums {
			if s.Name == name {
				t.Fatalf("%s: a crew was saved: %+v", key, s)
			}
		}
	}
}

// A member of the calling session's tool takes the session's login, read
// from what the session was started as: its catalog entry deleted or moved
// to another adapter since changes nothing, and the adapter's default is
// never taken in its place.
func TestAgentMemberTakesItsSessionsLogin(t *testing.T) {
	for _, change := range []string{"deleted", "another adapter"} {
		t.Run(change, func(t *testing.T) {
			e := newBindEnv(t, nil)
			a, _ := e.account(t, "claude", "Work")
			first := e.launch(t, "claude-yolo", nil)
			second := e.launch(t, "claude-yolo", nil)
			ftok, stok := e.agentToken(first), e.agentToken(second)
			member := func(name, agent string) map[string]any {
				return map[string]any{"name": name, "agentId": agent, "prompt": "", "start": map[string]any{"when": "manual"}}
			}
			self := map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}}
			out := e.expectAs(t, ftok, "POST", "/api/sessions/"+first+"/crew", map[string]any{"name": "Mine", "goal": "g", "self": "me", "members": []any{self, member("x", "codex-p")}}, http.StatusCreated, "")
			run := out["run"].(map[string]any)["id"].(string)
			e.setDefault(t, "claude", a)
			cat := e.srv.Catalog()
			list := cat.List()
			if change == "deleted" {
				var kept []catalog.Agent
				for _, ag := range list {
					if ag.ID != "claude-yolo" {
						kept = append(kept, ag)
					}
				}
				list = kept
			} else {
				y, _ := cat.Get("claude-yolo")
				y.Adapter, y.Yolo = "codex", nil
				list = replaceAgent(list, y)
			}
			changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: list})
			if err != nil {
				t.Fatal(err)
			}
			e.setCatalog(changed)
			// add_member: bound to the session's own login, or refused.
			status, data, added := e.callAs(t, ftok, "POST", "/api/sessions/"+first+"/run/members", member("twin", "claude-p"))
			if status == http.StatusCreated {
				if got := acct(runMember(t, added["run"].(map[string]any), "twin")["account"]); got == a+"/account" {
					t.Fatalf("add_member: the twin took the adapter's default: %s", data)
				}
				e.expect(t, "POST", "/api/runs/"+run+"/members/twin/start", nil, http.StatusOK, "")
				twin := e.waitRunning(t, run, "twin")
				t.Cleanup(func() { e.stopAndWait(twin) })
				if got := e.probe(t, twin); got != login("") {
					t.Fatalf("add_member: the twin's login: %s", got)
				}
			} else if status != http.StatusBadRequest {
				t.Fatalf("add_member: %d %s", status, data)
			}
			// form_crew: the same.
			status, data, formed := e.callAs(t, stok, "POST", "/api/sessions/"+second+"/crew", map[string]any{"name": "Theirs", "goal": "g", "self": "me", "members": []any{self, member("twin", "claude-p")}})
			if status == http.StatusCreated {
				run2 := formed["run"].(map[string]any)["id"].(string)
				if got := acct(runMember(t, formed["run"].(map[string]any), "twin")["account"]); got == a+"/account" {
					t.Fatalf("form_crew: the twin took the adapter's default: %s", data)
				}
				e.expect(t, "POST", "/api/runs/"+run2+"/members/twin/start", nil, http.StatusOK, "")
				twin := e.waitRunning(t, run2, "twin")
				t.Cleanup(func() { e.stopAndWait(twin) })
				if got := e.probe(t, twin); got != login("") {
					t.Fatalf("form_crew: the twin's login: %s", got)
				}
			} else if status != http.StatusBadRequest {
				t.Fatalf("form_crew: %d %s", status, data)
			}
		})
	}
}

// The member an agent's crew adopts is its session, model included: a self
// member naming another model is refused; one naming none has the
// session's, and a resume of it starts with that model.
func TestAdoptedMemberKeepsItsModel(t *testing.T) {
	e := newBindEnv(t, nil)
	sid := e.launch(t, "claude-p", map[string]any{"model": "opus"})
	e.probe(t, sid)
	tok := e.agentToken(sid)
	self := func(model string) map[string]any {
		m := map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}}
		if model != "" {
			m["model"] = model
		}
		return m
	}
	other := map[string]any{"name": "x", "agentId": "codex-p", "prompt": "", "start": map[string]any{"when": "manual"}}
	if status, data, _ := e.callAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "Other", "goal": "g", "self": "me", "members": []any{self("sonnet"), other}}); status != http.StatusBadRequest {
		t.Fatalf("a self member naming another model: %d %s", status, data)
	}
	out := e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "Mine", "goal": "g", "self": "me", "members": []any{self(""), other}}, http.StatusCreated, "")
	if m := runMember(t, out["run"].(map[string]any), "me"); m["model"] != "opus" {
		t.Fatalf("the adopted member: %v", m)
	}
	next := e.resume(t, sid)
	if l, _ := e.srv.lookupLocal(next); l.Launched().Model != "opus" {
		t.Fatalf("the adopted member's resume: model %q", l.Launched().Model)
	}
}
