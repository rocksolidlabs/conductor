package api

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// addAgents adds stubs of bindAgents' kind to e's catalog, each with env
// over its probe's folder.
func (e *bindEnv) addAgents(t *testing.T, envs map[string]map[string]string) {
	t.Helper()
	off := false
	list := e.srv.Catalog().List()
	for id, env := range envs {
		en := map[string]string{"PROBE_DIR": e.probes}
		for k, v := range env {
			en[k] = v
		}
		list = append(list, catalog.Agent{ID: id, Name: id, Adapter: "claude", Command: []string{"/bin/sh", "-c", loginProbe}, Env: en, AllowArgs: true, Probe: &off,
			Session: &catalog.SessionRecipe{IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^conv-[a-z]+$"}})
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: list})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
}

// recordResumable waits for session sid's record to hold a conversation
// to take up: the record's reconcile is what it waits on, and the test's
// timeout bounds it.
func (e *bindEnv) recordResumable(t *testing.T, sid string) {
	t.Helper()
	for {
		_, out := e.do("GET", "/api/history/sessions/"+sid, adminToken, nil)
		rec, _ := out["record"].(map[string]any)
		agent, _ := rec["agent"].(map[string]any)
		if as, _ := agent["session"].(map[string]any); as["resumable"] == true {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startAs starts agent as the one launch does, with the account and the
// login asked and the launch's own variables set: the session's id, or the
// refusal.
func (e *bindEnv) startAs(t *testing.T, agent, account string, want loginWant, set map[string]string) (string, *apiError) {
	t.Helper()
	req := createSessionRequest{AgentID: agent, Env: set, ctx: context.Background()}
	req.Account, req.login = account, want
	local, aerr := e.srv.createLocalSession(req, nil)
	if aerr != nil {
		return "", aerr
	}
	sid := local.Info().ID
	t.Cleanup(func() { e.stopAndWait(sid) })
	return sid, nil
}

// One judgement decides every launch's binding from the environment its
// process gets. Every kind of want (an account named, Brakeman's folder
// held by an account, Brakeman's folder held by none, the tool's own login,
// none named while the adapter's default is an account) against every
// environment (nothing set, the folder set by the entry, a key set by the
// entry, by the server's passthrough, by the launch's own variables): the
// source says what the environment holds, and account and unlisted appear
// only where the folder is the one login variable.
func TestOneJudgementForEveryWant(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "made-up-key-9")
	type server struct {
		e                      *bindEnv
		a, folderA, b, folderB string
		other                  string
	}
	mk := func(pass bool) *server {
		var mutate func(*config.Config)
		if pass {
			mutate = func(c *config.Config) { c.EnvPassthrough = append(c.EnvPassthrough, "ANTHROPIC_API_KEY") }
		}
		s := &server{e: newBindEnv(t, mutate), other: realRoot(t, t.TempDir())}
		s.e.addAgents(t, map[string]map[string]string{"claude-key": {"ANTHROPIC_API_KEY": "made-up-key-3"}})
		s.a, s.folderA = s.e.account(t, "claude", "Work")
		s.b, s.folderB = s.e.account(t, "claude", "Default")
		s.e.setDefault(t, "claude", s.b)
		return s
	}
	plain, passed := mk(false), mk(true)
	envs := []struct {
		name  string
		srv   *server
		agent string
		set   map[string]string
		key   string
	}{
		{"nothing", plain, "claude-p", nil, ""},
		{"the folder by the entry", plain, "claude-pin", nil, ""},
		{"a key by the entry", plain, "claude-key", nil, "made-up-key-3"},
		{"a key by the passthrough", passed, "claude-p", nil, "made-up-key-9"},
		{"a key by the launch", plain, "claude-p", map[string]string{"ANTHROPIC_API_KEY": "made-up-key-4"}, "made-up-key-4"},
	}
	for _, env := range envs {
		s := env.srv
		wants := []struct {
			name    string
			account string
			want    loginWant
			// what the cell must give: the source ("" for a refusal), its
			// id, the folder the process gets and the binding keeps.
			source, id, folder string
		}{
			{"an account named", s.a, loginWant{}, session.SourceAccount, s.a, s.folderA},
			{"Brakeman's folder an account holds", capacity.DefaultID, loginWant{folder: s.folderA}, session.SourceAccount, s.a, s.folderA},
			{"Brakeman's folder no account holds", capacity.DefaultID, loginWant{folder: s.other}, session.SourceUnlisted, session.SourceUnlisted, s.other},
			{"the tool's own login", capacity.DefaultID, loginWant{}, session.SourceOwn, capacity.DefaultID, ""},
			{"none named", "", loginWant{}, session.SourceAccount, s.b, s.folderB},
		}
		for _, w := range wants {
			source, id, folder := w.source, w.id, w.folder
			switch {
			case env.key != "" && w.account == s.a:
				source = ""
			case env.key != "":
				source, id = session.SourcePinned, capacity.DefaultID
				if w.want.folder == "" {
					folder = ""
				}
				if env.set != nil {
					source = session.SourceLaunchEnv
				}
			case env.agent == "claude-pin" && w.account == s.a:
				source = ""
			case env.agent == "claude-pin" && w.want.folder == "":
				source, id, folder = session.SourcePinned, capacity.DefaultID, s.e.pin
			}
			cell := w.name + " / " + env.name
			sid, aerr := s.e.startAs(t, env.agent, w.account, w.want, env.set)
			if source == "" {
				if aerr == nil || aerr.Code != "invalid_account" {
					t.Errorf("%s: %v, want invalid_account", cell, aerr)
				}
				continue
			}
			if aerr != nil {
				t.Errorf("%s: refused %v", cell, aerr)
				continue
			}
			got, b := s.e.probe(t, sid), s.e.bound(t, sid)
			// The binding keeps the folder the process runs in: the tool's
			// own under its home where no folder is set.
			if want := "D=[" + folder + "] K=[" + env.key + "] H=[]"; got != want || b.Source != source || b.ID != id || b.Folder != cmp.Or(folder, ownFolder()) {
				t.Errorf("%s: %s %+v, want %s source %s id %s", cell, got, b, want, source, id)
			}
			if (b.Source == session.SourceAccount || b.Source == session.SourceUnlisted) && !strings.Contains(got, "K=[]") {
				t.Errorf("%s: bound %s beside a key", cell, b.Source)
			}
		}
	}
}

// A new launch binds no retired account, by Brakeman's folder either: a
// turn whose folder is a retired account's is refused with words.
func TestBrakemanFolderOfARetiredAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	e.expect(t, "POST", "/api/accounts/"+a+"/retire", map[string]any{"expectedRev": accountsRev(t, e.testEnv)}, http.StatusOK, "")
	sid, aerr := e.startAs(t, "claude-p", capacity.DefaultID, loginWant{folder: folderA}, nil)
	if aerr == nil || aerr.Code != "invalid_account" || !strings.Contains(aerr.Message, "retired") {
		var b session.BoundAccount
		if aerr == nil {
			b = e.bound(t, sid)
		}
		t.Fatalf("a turn on a retired account's folder: %v %+v", aerr, b)
	}
}

// A folder variable that is not whole names the folder under the one the
// process starts in, and the binding keeps that folder: started again in
// another folder it is another login, refused; a crew member of such an
// entry, bound before it has a folder, is refused at its admission.
func TestARelativeLoginFolderIsTheWorkingFolders(t *testing.T) {
	e := newBindEnv(t, nil)
	e.addAgents(t, map[string]map[string]string{"claude-rel": {"CLAUDE_CONFIG_DIR": ".login"}})
	work := filepath.Join(e.root, "w1")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := e.launch(t, "claude-rel", map[string]any{"cwd": work})
	e.probe(t, sid)
	if b := e.bound(t, sid); b.Source != session.SourcePinned || b.Folder != filepath.Join(work, ".login") {
		t.Fatalf("the binding: %+v", b)
	}
	e.stopAndWait(sid)
	if err := os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	if status, data, out := e.call(t, "POST", "/api/sessions/"+sid+"/resume", nil); status != http.StatusConflict || errorCode(out) != "account_gone" {
		t.Fatalf("a resume in another folder: %d %s", status, data)
	}
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Rel", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "lead", "agentId": "claude-rel", "prompt": "", "start": map[string]any{"when": "immediately"}}}}, http.StatusCreated)["id"].(string)
	if status, data, out := e.call(t, "POST", "/api/crews/"+id+"/launch", nil); status != http.StatusBadRequest || errorCode(out) != "invalid_account" {
		t.Fatalf("a crew of it: %d %s", status, data)
	}
}

// A resume from a session's record is compared with the adapter the record
// keeps: the agent id moved to another adapter since is refused, and no
// process starts.
func TestRecordResumeKeepsItsAdapter(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", map[string]any{"account": a})
	e.probe(t, sid)
	l, _ := e.srv.lookupLocal(sid)
	l.ReportAgentSession("conv-a", session.PolicyLatest, true)
	e.recordResumable(t, sid)
	shutdown(e.testEnv)
	next := &bindEnv{testEnv: serveSecond(t, e.testEnv, newFakeUsage("claude"), newFakeUsage("codex")), probes: e.probes, pin: e.pin}
	cat := next.srv.Catalog()
	p, _ := cat.Get("claude-p")
	p.Adapter = "codex"
	changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: replaceAgent(cat.List(), p)})
	if err != nil {
		t.Fatal(err)
	}
	next.setCatalog(changed)
	if status, data, out := next.call(t, "POST", "/api/history/sessions/"+sid+"/resume", map[string]any{"commandId": "c-1", "confirmLost": true}); status == http.StatusCreated || errorCode(out) != "account_gone" {
		t.Fatalf("a resume under another adapter: %d %s", status, data)
	}
}
