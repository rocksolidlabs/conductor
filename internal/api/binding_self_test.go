package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Brakeman's draft crew is an agent's bytes too, decoded by the one
// decoder of an agent's members: a member that names an account or a
// binding, in each spelling of the key the decoder takes (the last of two
// keys deciding), is refused at the draft's check (the brief's) and at its
// making, and no crew is saved.
func TestBrakemanDraftNamesNoAccount(t *testing.T) {
	f := newObsFixture(t)
	for i, member := range []string{
		`"account":"a-aaaaaaaaaaaa"`, `"Account":"a-aaaaaaaaaaaa"`, `"ACCOUNT":"default"`, `"account":"","ACCOUNT":"a-aaaaaaaaaaaa"`,
		`"bound":{"id":"default","source":"own"}`, `"BOUND":{"id":"default","source":"own"}`,
	} {
		body := json.RawMessage(`{"name":"Fixers","goal":"fix the build","cwd":` + strconv.Quote(f.root) + `,"where":"server","isolation":"none",` +
			`"members":[{"name":"lead","agentId":"cat","start":{"when":"immediately"},` + member + `}]}`)
		if err := (brakemanFacts{s: f.srv}).CheckCrew(body); err == nil {
			t.Errorf("the check of a draft member with %s: accepted", member)
		}
		req := f.drafts(t, brakeman.Item{Kind: brakeman.ItemDraftCrew, DraftCrew: &brakeman.DraftCrew{Crew: body}})
		if status, out := f.make(t, req, 0, "crew-"+itoa(i), nil); status != http.StatusBadRequest || errorCode(out) != "invalid_draft" {
			t.Errorf("the making of a draft member with %s: %d %v", member, status, out)
		}
	}
	if _, total, _ := f.srv.crews.List(0, 100); total != 0 {
		t.Fatalf("%d crews saved", total)
	}
}

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

// A clone takes the source's account and model for an entry of the
// adapter the source was started as (its Launched), whatever became of the
// source's entry: removed from the catalog, or moved to another adapter,
// before the clone, the clone onto another entry of the source's adapter
// is started on the source's account with its model, never on the
// adapter's default of the moment.
func TestCloneKeepsTheSourcesLogin(t *testing.T) {
	for _, change := range []string{"removed", "another adapter"} {
		t.Run(change, func(t *testing.T) {
			e := newBindEnv(t, nil)
			a, folderA := e.account(t, "claude", "Work")
			b, _ := e.account(t, "claude", "Other")
			e.addAgents(t, map[string]map[string]string{"claude-q": nil})
			sid := e.launch(t, "claude-p", map[string]any{"account": a, "model": "opus"})
			e.probe(t, sid)
			e.setDefault(t, "claude", b)
			cat := e.srv.Catalog()
			list := cat.List()
			if change == "removed" {
				var kept []catalog.Agent
				for _, ag := range list {
					if ag.ID != "claude-p" {
						kept = append(kept, ag)
					}
				}
				list = kept
			} else {
				p, _ := cat.Get("claude-p")
				p.Adapter = "codex"
				list = replaceAgent(list, p)
			}
			changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: list})
			if err != nil {
				t.Fatal(err)
			}
			e.setCatalog(changed)
			cid := e.expect(t, "POST", "/api/sessions/"+sid+"/clone", map[string]any{"name": "copy", "agentId": "claude-q"}, http.StatusCreated, "")["id"].(string)
			t.Cleanup(func() { e.stopAndWait(cid) })
			if got := e.probe(t, cid); got != login(folderA) {
				t.Fatalf("the clone's login: %s", got)
			}
			if l, _ := e.srv.lookupLocal(cid); l.Launched().Account.ID != a || l.Launched().Model != "opus" {
				t.Fatalf("the clone's binding and model: %+v %q", l.Launched().Account, l.Launched().Model)
			}
		})
	}
}

// The member an agent's crew adopts is its session, model included: a self
// member naming another model is refused, and so is one whose own
// arguments choose a model beside the session's, judged as every member's
// are (it could never be resumed); one naming none has the session's, and
// a resume of it starts with that model.
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
	chooses := self("")
	chooses["args"] = []string{"--model", "sonnet"}
	if status, data, out := e.callAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "Args", "goal": "g", "self": "me", "members": []any{chooses, other}}); status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("a self member whose arguments choose a model: %d %s", status, data)
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
