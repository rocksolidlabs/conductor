package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch bound to its account and model (docs/plans/capacity.md, PR-4).
// No real agent tool runs: every agent here is a shell stub that writes the
// login its environment names (the folder variable and a key, made up) to
// a file of its session's and waits; every folder is the test's own.

// loginProbe is a stub's command: the login its environment names, then
// cat until it is stopped.
const loginProbe = `printf 'D=[%s] K=[%s] H=[%s]\n' "$CLAUDE_CONFIG_DIR" "$ANTHROPIC_API_KEY" "$CODEX_HOME" > "$PROBE_DIR/$CONDUCTOR_SESSION_ID"; exec /bin/cat`

// bindAgents are the stubs: a plain Claude Code entry, one whose env pins
// CLAUDE_CONFIG_DIR to pin, one whose yolo recipe sets a key, a plain
// Codex entry and an entry of no adapter.
func bindAgents(probes, pin string) []catalog.Agent {
	off := false
	stub := func(id, adapter string, env map[string]string) catalog.Agent {
		e := map[string]string{"PROBE_DIR": probes}
		for k, v := range env {
			e[k] = v
		}
		return catalog.Agent{ID: id, Name: id, Adapter: adapter, Command: []string{"/bin/sh", "-c", loginProbe}, Env: e, AllowArgs: true, Probe: &off,
			Session: &catalog.SessionRecipe{IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^conv-[a-z]+$"}}
	}
	yolo := stub("claude-yolo", "claude", nil)
	yolo.Yolo = &catalog.Yolo{Env: map[string]string{"ANTHROPIC_API_KEY": "made-up-key-1"}}
	return []catalog.Agent{
		stub("claude-p", "claude", nil),
		stub("claude-pin", "claude", map[string]string{"CLAUDE_CONFIG_DIR": pin}),
		yolo,
		stub("codex-p", "codex", nil),
		stub("plain", "", nil),
	}
}

// bindEnv is a capacity server over the stubs, with the folder their
// probes land in and the folder claude-pin pins.
type bindEnv struct {
	*testEnv
	probes, pin string
}

func newBindEnv(t *testing.T, mutate func(*config.Config)) *bindEnv {
	t.Helper()
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := capEnv(t, mutate, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin))
	return &bindEnv{testEnv: e, probes: probes, pin: pin}
}

// account adds an account of adapter on a new folder: its id and folder.
func (e *bindEnv) account(t *testing.T, adapter, label string) (string, string) {
	t.Helper()
	folder := realRoot(t, t.TempDir())
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": adapter, "label": label, "folder": folder, "expectedRev": accountsRev(t, e.testEnv)}, http.StatusCreated, "")
	return out["account"].(map[string]any)["id"].(string), folder
}

func (e *bindEnv) setDefault(t *testing.T, adapter, id string) {
	t.Helper()
	e.expect(t, "PUT", "/api/accounts/defaults/"+adapter, map[string]any{"account": id, "expectedRev": accountsRev(t, e.testEnv)}, http.StatusOK, "")
}

// launch launches agent with body's fields over agentId: the session's id.
func (e *bindEnv) launch(t *testing.T, agent string, body map[string]any) string {
	t.Helper()
	b := map[string]any{"agentId": agent}
	for k, v := range body {
		b[k] = v
	}
	out := e.expect(t, "POST", "/api/sessions", b, http.StatusCreated, "")
	id := out["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })
	return id
}

// probe waits for the login session sid's process was started with.
func (e *bindEnv) probe(t *testing.T, sid string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(e.probes, sid))
		if err == nil && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s wrote no login: %v %q", sid, err, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// bound is session sid's binding as its Launched holds it.
func (e *bindEnv) bound(t *testing.T, sid string) session.BoundAccount {
	t.Helper()
	l, ok := e.srv.lookupLocal(sid)
	if !ok {
		t.Fatalf("no session %s", sid)
	}
	return l.Launched().Account
}

// resume stops sid and resumes it: the new session's id.
func (e *bindEnv) resume(t *testing.T, sid string) string {
	t.Helper()
	e.stopAndWait(sid)
	out := e.expect(t, "POST", "/api/sessions/"+sid+"/resume", nil, http.StatusCreated, "")
	id := out["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })
	return id
}

func login(folder string) string { return "D=[" + folder + "] K=[] H=[]" }

// A launch naming an account is started with its folder as the adapter's
// folder variable, and holds the binding (its id, label, folder, source and
// the fingerprint of that environment); the admin's launch block names the
// account by id, label and source, never its folder; a link's holder reads
// no launch; the session counts on the account.
func TestLaunchBindsAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	id, folder := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", map[string]any{"account": id})
	if got := e.probe(t, sid); got != login(folder) {
		t.Fatalf("the process's login: %s", got)
	}
	want := session.BoundAccount{ID: id, Label: "Work", Folder: folder, Source: session.SourceAccount, Login: keptLogin("claude", folder)}
	if b := e.bound(t, sid); b != want {
		t.Fatalf("the binding %+v, want %+v", b, want)
	}
	_, data, out := e.call(t, "GET", "/api/sessions/"+sid, nil)
	acct, _ := out["launch"].(map[string]any)["account"].(map[string]any)
	if acct["id"] != id || acct["label"] != "Work" || acct["source"] != "account" || len(acct) != 3 || strings.Contains(string(data), folder) || strings.Contains(string(data), want.Login) {
		t.Fatalf("the admin's session: %s", data)
	}
	_, lo := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": "control"})
	_, data, out = e.callAs(t, lo["token"].(string), "GET", "/api/sessions/"+sid, nil)
	if out["launch"] != nil || strings.Contains(string(data), id) || strings.Contains(string(data), folder) {
		t.Fatalf("a link's session: %s", data)
	}
	if v := viewOfKey(t, e.testEnv, "agent=claude-p&account="+id); v["counts"].(map[string]any)["running"] != float64(1) {
		t.Fatalf("the account's counts: %v", v["counts"])
	}
}

// A launch naming none takes the adapter's default at that moment, once:
// a change of default afterwards moves nothing that runs, and the next
// launch takes the new one.
func TestLaunchResolvesDefaultOnce(t *testing.T) {
	e := newBindEnv(t, nil)
	id, folder := e.account(t, "claude", "Work")
	e.setDefault(t, "claude", id)
	first := e.launch(t, "claude-p", nil)
	if got := e.probe(t, first); got != login(folder) {
		t.Fatalf("the first launch's login: %s", got)
	}
	e.setDefault(t, "claude", capacity.DefaultID)
	if b := e.bound(t, first); b.ID != id || b.Folder != folder {
		t.Fatalf("the running session's binding moved: %+v", b)
	}
	next := e.launch(t, "claude-p", nil)
	if got, b := e.probe(t, next), e.bound(t, next); got != login("") || b.ID != capacity.DefaultID || b.Source != session.SourceOwn {
		t.Fatalf("the next launch: %s %+v", got, b)
	}
}

// A retired account, one not in Accounts, one of another adapter and an
// id out of form are refused invalid_account; a default that names a
// retired account reads as the tool's own login.
func TestLaunchRefusesRetired(t *testing.T) {
	e := newBindEnv(t, nil)
	id, _ := e.account(t, "claude", "Work")
	codex, _ := e.account(t, "codex", "Other")
	e.setDefault(t, "claude", id)
	e.expect(t, "POST", "/api/accounts/"+id+"/retire", map[string]any{"expectedRev": accountsRev(t, e.testEnv)}, http.StatusOK, "")
	for _, account := range []string{id, "a-zzzzzzzzzzzz", codex, "Work", "unlisted"} {
		e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": account}, http.StatusBadRequest, "invalid_account")
	}
	sid := e.launch(t, "claude-p", nil)
	if b := e.bound(t, sid); b.ID != capacity.DefaultID || b.Source != session.SourceOwn {
		t.Fatalf("a retired default: %+v", b)
	}
}

// A model goes through the adapter's flag after the launch's arguments; an
// agent whose adapter takes none, a model out of form and one beside
// arguments that choose a model already are refused.
func TestLaunchModel(t *testing.T) {
	e := newBindEnv(t, nil)
	out := e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "model": "opus", "args": []string{"--verbose"}}, http.StatusCreated, "")
	sid := out["id"].(string)
	t.Cleanup(func() { e.stopAndWait(sid) })
	var argv []string
	for _, a := range out["command"].([]any) {
		argv = append(argv, a.(string))
	}
	if i := slices.Index(argv, "--verbose"); i < 0 || i+1 >= len(argv) || argv[i+1] != "--model=opus" {
		t.Fatalf("the command: %q", argv)
	}
	if l, _ := e.srv.lookupLocal(sid); l.Launched().Model != "opus" {
		t.Fatalf("the launch's model: %q", l.Launched().Model)
	}
	for _, body := range []map[string]any{
		{"agentId": "plain", "model": "opus"},
		{"agentId": "claude-p", "model": "a model"},
		{"agentId": "claude-p", "model": "opus", "args": []string{"--model=sonnet"}},
		{"agentId": "claude-p", "model": "opus", "args": []string{"--model", "sonnet"}},
	} {
		e.expect(t, "POST", "/api/sessions", body, http.StatusBadRequest, "invalid_request")
	}
}

// An entry whose environment pins the folder variable has that login
// alone: a launch naming none takes it (bound pinned to its folder),
// whatever the adapter's default; one naming an account is refused, naming
// where the login is set. A launch whose own additions set a login
// variable (a yolo recipe's key) is bound launch_env and refused an
// account too.
func TestLaunchPinnedEntry(t *testing.T) {
	e := newBindEnv(t, nil)
	id, _ := e.account(t, "claude", "Work")
	e.setDefault(t, "claude", id)
	sid := e.launch(t, "claude-pin", nil)
	if got, b := e.probe(t, sid), e.bound(t, sid); got != login(e.pin) || b.ID != capacity.DefaultID || b.Source != session.SourcePinned || b.Folder != e.pin {
		t.Fatalf("the pinned launch: %s %+v", got, b)
	}
	if out := e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-pin", "account": id}, http.StatusBadRequest, "invalid_account"); !strings.Contains(errorMessage(out).(string), "CLAUDE_CONFIG_DIR") {
		t.Fatalf("the refusal's words: %v", out)
	}
	yolo := e.launch(t, "claude-yolo", map[string]any{"yolo": true})
	if got, b := e.probe(t, yolo), e.bound(t, yolo); got != "D=[] K=[made-up-key-1] H=[]" || b.ID != capacity.DefaultID || b.Source != session.SourceLaunchEnv || b.Folder != "" {
		t.Fatalf("the launch_env launch: %s %+v", got, b)
	}
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-yolo", "yolo": true, "account": id}, http.StatusBadRequest, "invalid_account")
}

// The server's passthrough of the folder variable pins the login of every
// entry of the adapter: another account refused, default taken pinned.
func TestLaunchPinnedByPassthrough(t *testing.T) {
	passed := realRoot(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", passed)
	e := newBindEnv(t, func(c *config.Config) { c.EnvPassthrough = append(c.EnvPassthrough, "CLAUDE_CONFIG_DIR") })
	id, _ := e.account(t, "claude", "Work")
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": id}, http.StatusBadRequest, "invalid_account")
	sid := e.launch(t, "claude-p", nil)
	if got, b := e.probe(t, sid), e.bound(t, sid); got != login(passed) || b.Source != session.SourcePinned || b.Folder != passed {
		t.Fatalf("the passed-through launch: %s %+v", got, b)
	}
}

// A resume starts under the binding its session was launched with, the
// default changed since; one whose account was removed since (another
// added under its label) is refused account_gone and starts nothing; so is
// a resume from the session's record.
func TestResumeKeepsAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	b, _ := e.account(t, "claude", "Other")
	e.setDefault(t, "claude", a)
	sid := e.launch(t, "claude-p", map[string]any{"model": "opus"})
	e.setDefault(t, "claude", b)
	next := e.resume(t, sid)
	if got, bound := e.probe(t, next), e.bound(t, next); got != login(folderA) || bound.ID != a {
		t.Fatalf("the resume: %s %+v", got, bound)
	}
	if l, _ := e.srv.lookupLocal(next); l.Launched().Model != "opus" {
		t.Fatalf("the resume's model: %q", l.Launched().Model)
	}
}

func TestRemovedAndReaddedRefused(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", map[string]any{"account": a})
	e.probe(t, sid)
	e.stopAndWait(sid)
	e.expect(t, "DELETE", "/api/accounts/"+a+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil, http.StatusNoContent, "")
	e.account(t, "claude", "Work")
	before := len(e.srv.registry.List())
	e.expect(t, "POST", "/api/sessions/"+sid+"/resume", nil, http.StatusConflict, "account_gone")
	if n := len(e.srv.registry.List()); n != before {
		t.Fatalf("a refused resume listed a session: %d, then %d", before, n)
	}
}

// A resume whose login would differ from the bound one is refused
// account_gone, for every source: a pinned entry's folder changed, a yolo
// recipe's key changed.
func TestBindingLoginChanged(t *testing.T) {
	e := newBindEnv(t, nil)
	pinned := e.launch(t, "claude-pin", nil)
	yolo := e.launch(t, "claude-yolo", map[string]any{"yolo": true})
	e.probe(t, pinned)
	e.probe(t, yolo)
	cat := e.srv.Catalog()
	pin, _ := cat.Get("claude-pin")
	pin.Env = map[string]string{"PROBE_DIR": e.probes, "CLAUDE_CONFIG_DIR": realRoot(t, t.TempDir())}
	y, _ := cat.Get("claude-yolo")
	y.Yolo = &catalog.Yolo{Env: map[string]string{"ANTHROPIC_API_KEY": "made-up-key-2"}}
	changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: replaceAgent(replaceAgent(cat.List(), pin), y)})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(changed)
	for _, sid := range []string{pinned, yolo} {
		e.stopAndWait(sid)
		e.expect(t, "POST", "/api/sessions/"+sid+"/resume", nil, http.StatusConflict, "account_gone")
	}
}

// The fingerprint is the adapter and the login variables' values: the
// same environment gives the same one, another key, folder or base URL
// another; it holds no value, and a binding's JSON holds none of a key.
func TestBindingFingerprint(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	base := loginOf("claude", env("CLAUDE_CONFIG_DIR", "/a", "ANTHROPIC_API_KEY", "made-up-key-1"))
	if base != loginOf("claude", env("ANTHROPIC_API_KEY", "made-up-key-1", "CLAUDE_CONFIG_DIR", "/a", "OTHER", "x")) {
		t.Fatal("the same login gave another fingerprint")
	}
	for _, other := range []func(string) string{
		env("CLAUDE_CONFIG_DIR", "/a", "ANTHROPIC_API_KEY", "made-up-key-2"),
		env("CLAUDE_CONFIG_DIR", "/b", "ANTHROPIC_API_KEY", "made-up-key-1"),
		env("CLAUDE_CONFIG_DIR", "/a", "ANTHROPIC_API_KEY", "made-up-key-1", "ANTHROPIC_BASE_URL", "http://example.test"),
		env("CLAUDE_CONFIG_DIR", "/a"),
	} {
		if loginOf("claude", other) == base {
			t.Fatal("another login gave the same fingerprint")
		}
	}
	if loginOf("codex", env("CLAUDE_CONFIG_DIR", "/a", "ANTHROPIC_API_KEY", "made-up-key-1")) == base {
		t.Fatal("another adapter gave the same fingerprint")
	}
	b, _ := json.Marshal(session.BoundAccount{ID: "default", Source: session.SourceLaunchEnv, Login: base})
	if strings.Contains(string(b), "made-up-key") || strings.Contains(base, "/a") {
		t.Fatalf("a binding holds a value: %s", b)
	}
}

// Remove is refused while a session running is bound to the account, or a
// run that has not ended, naming them; once they end it is not.
func TestRemoveAccountInUse(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", map[string]any{"account": a})
	e.setDefault(t, "claude", a)
	run := e.launchCrew(t, "Bound", map[string]any{"name": "lead", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "immediately"}})
	member := e.waitRunning(t, run, "lead")
	e.setDefault(t, "claude", capacity.DefaultID)
	out := e.expect(t, "DELETE", "/api/accounts/"+a+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil, http.StatusConflict, "account_in_use")
	names := strings.Join(namesOf(out), "|")
	if !strings.Contains(names, "session ") || !strings.Contains(names, "run "+run) {
		t.Fatalf("what holds it: %v", out)
	}
	e.stopAndWait(sid)
	e.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	e.stopAndWait(member)
	e.expect(t, "DELETE", "/api/accounts/"+a+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil, http.StatusNoContent, "")
}

func namesOf(out map[string]any) []string {
	var names []string
	if er, ok := out["error"].(map[string]any); ok {
		for _, n := range er["names"].([]any) {
			names = append(names, n.(string))
		}
	}
	return names
}

// A launch between its account's resolution and its session's
// registration holds the account: Remove is refused; a launch that fails
// after its resolution lets it go.
func TestRemoveWhileLaunchHeld(t *testing.T) {
	e := newBindEnv(t, func(c *config.Config) { c.MaxSessions = 1 })
	a, _ := e.account(t, "claude", "Work")
	reached, release := make(chan struct{}), make(chan struct{})
	hook := func(stage string) {
		if stage == "checked" {
			close(reached)
			<-release
		}
	}
	launchHookFn.Store(&hook)
	t.Cleanup(func() { launchHookFn.Store(nil) })
	launched := make(chan string, 1)
	go func() {
		_, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "claude-p", "account": a})
		id, _ := out["id"].(string)
		launched <- id
	}()
	<-reached
	launchHookFn.Store(nil)
	out := e.expect(t, "DELETE", "/api/accounts/"+a+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil, http.StatusConflict, "account_in_use")
	if names := strings.Join(namesOf(out), "|"); !strings.Contains(names, "launch") {
		t.Fatalf("what holds it: %v", out)
	}
	close(release)
	sid := <-launched
	if sid == "" {
		t.Fatal("the held launch did not start")
	}
	// At the cap: a second launch resolves the account and fails at its
	// registration, which lets the account go.
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": a}, http.StatusConflict, "too_many_sessions")
	e.stopAndWait(sid)
	e.expect(t, "DELETE", "/api/accounts/"+a+"?expectedRev="+itoa(int(accountsRev(t, e.testEnv))), nil, http.StatusNoContent, "")
}

// A server that does not own the accounts binds no registered account: a
// launch naming one is refused accounts_not_owner, the tool's own login
// launched.
func TestNotOwnerBindsDefaultOnly(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	second := &bindEnv{testEnv: serveSecond(t, e.testEnv, newFakeUsage("claude"), newFakeUsage("codex")), probes: e.probes, pin: e.pin}
	second.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": a}, http.StatusConflict, "accounts_not_owner")
	sid := second.launch(t, "claude-p", map[string]any{"account": "default"})
	if b := second.bound(t, sid); b.Source != session.SourceOwn {
		t.Fatalf("the second server's launch: %+v", b)
	}
}

// An agent never picks an account: form_crew and add_member naming one are
// refused invalid_account.
func TestSelfCrewRefusesAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	a, _ := e.account(t, "claude", "Work")
	sid := e.launch(t, "claude-p", nil)
	tok := e.agentToken(sid)
	crewBody := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "helper", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "manual"}}
		for k, v := range extra {
			m[k] = v
		}
		return map[string]any{"name": "Mine", "goal": "g", "self": "me", "members": []any{map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}}, m}}
	}
	e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", crewBody(map[string]any{"account": a}), http.StatusBadRequest, "invalid_account")
	e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", crewBody(nil), http.StatusCreated, "")
	e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/run/members", map[string]any{"name": "more", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "manual"}, "account": a}, http.StatusBadRequest, "invalid_account")
}

// An agent's own crew: the session adopted keeps its own binding (the
// default changed since), a member of its adapter takes its login,
// compared by effective login, a member of another adapter that adapter's
// default; a member whose entry uses another login is refused, on
// form_crew and add_member; no reply of the run carries a folder or a
// fingerprint.
func TestSelfCrewAdoptsSessionBinding(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	e.setDefault(t, "claude", a)
	sid := e.launch(t, "claude-p", nil)
	e.setDefault(t, "claude", capacity.DefaultID)
	tok := e.agentToken(sid)
	member := func(name, agent string) map[string]any {
		return map[string]any{"name": name, "agentId": agent, "prompt": "", "start": map[string]any{"when": "immediately"}}
	}
	self := map[string]any{"name": "me", "agentId": "", "prompt": "", "start": map[string]any{"when": "manual"}}
	e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "Pinned", "goal": "g", "self": "me", "members": []any{self, member("pin", "claude-pin")}}, http.StatusBadRequest, "invalid_account")
	out := e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/crew", map[string]any{"name": "Mine", "goal": "g", "self": "me", "members": []any{self, member("twin", "claude-p"), member("other", "codex-p")}}, http.StatusCreated, "")
	run := out["run"].(map[string]any)["id"].(string)
	accounts := map[string]any{}
	for _, m := range out["run"].(map[string]any)["members"].([]any) {
		accounts[m.(map[string]any)["name"].(string)] = m.(map[string]any)["account"]
	}
	if acct(accounts["me"]) != a+"/account" || acct(accounts["twin"]) != a+"/account" || acct(accounts["other"]) != "default/own" {
		t.Fatalf("the members' accounts: %v", accounts)
	}
	twin := e.waitRunning(t, run, "twin")
	t.Cleanup(func() { e.stopAndWait(twin) })
	if got := e.probe(t, twin); got != login(folderA) {
		t.Fatalf("the twin's login: %s", got)
	}
	other := e.waitRunning(t, run, "other")
	t.Cleanup(func() { e.stopAndWait(other) })
	e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/run/members", member("pin2", "claude-pin"), http.StatusBadRequest, "invalid_account")
	added := e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/run/members", map[string]any{"name": "late", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "manual"}}, http.StatusCreated, "")
	// No reply of the run carries a folder or a fingerprint.
	l, _ := e.srv.lookupLocal(sid)
	fp := l.Launched().Account.Login
	replies := [][]byte{[]byte(mustJSON(out)), []byte(mustJSON(added))}
	for _, path := range []string{"/api/runs", "/api/runs/" + run} {
		_, data, _ := e.call(t, "GET", path, nil)
		replies = append(replies, data)
	}
	_, data, _ := e.callAs(t, tok, "GET", "/api/sessions/"+sid+"/run", nil)
	replies = append(replies, data)
	for _, b := range replies {
		if strings.Contains(string(b), folderA) || strings.Contains(string(b), fp) || strings.Contains(string(b), `"bound"`) || strings.Contains(string(b), e.pin) {
			t.Fatalf("a run reply carries a binding: %.400s", b)
		}
	}
	// A session of the pinned entry: a plain member of its adapter is
	// refused, a pinned one takes its login.
	pinned := e.launch(t, "claude-pin", nil)
	ptok := e.agentToken(pinned)
	e.expectAs(t, ptok, "POST", "/api/sessions/"+pinned+"/crew", map[string]any{"name": "P", "goal": "g", "self": "me", "members": []any{self, member("plain", "claude-p")}}, http.StatusBadRequest, "invalid_account")
	pout := e.expectAs(t, ptok, "POST", "/api/sessions/"+pinned+"/crew", map[string]any{"name": "P2", "goal": "g", "self": "me", "members": []any{self, member("pin", "claude-pin")}}, http.StatusCreated, "")
	prun := pout["run"].(map[string]any)["id"].(string)
	pin := e.waitRunning(t, prun, "pin")
	t.Cleanup(func() { e.stopAndWait(pin) })
	if got := e.probe(t, pin); got != login(e.pin) {
		t.Fatalf("the pinned member's login: %s", got)
	}
}

func acct(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	return m["id"].(string) + "/" + m["source"].(string)
}

// The admin's added member takes the account it names or the adapter's
// default at the add, and keeps it when it starts later, the default
// changed between.
func TestAddMemberBindsDefaultNow(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	run := e.launchCrew(t, "Later", map[string]any{"name": "lead", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "immediately"}})
	lead := e.waitRunning(t, run, "lead")
	t.Cleanup(func() { e.stopAndWait(lead) })
	e.setDefault(t, "claude", a)
	out := e.expect(t, "POST", "/api/runs/"+run+"/members", map[string]any{"name": "later", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "manual"}}, http.StatusCreated, "")
	if got := acct(runMember(t, out["run"].(map[string]any), "later")["account"]); got != a+"/account" {
		t.Fatalf("the added member's account: %s", got)
	}
	e.setDefault(t, "claude", capacity.DefaultID)
	e.expect(t, "POST", "/api/runs/"+run+"/members/later/start", nil, http.StatusOK, "")
	later := e.waitRunning(t, run, "later")
	t.Cleanup(func() { e.stopAndWait(later) })
	if got := e.probe(t, later); got != login(folderA) {
		t.Fatalf("the member started later: %s", got)
	}
}

// A crew launch names each member's account: a member naming one is
// launched on it, one naming none on its adapter's default at the launch,
// a crew saved with a binding refused.
func TestCrewLaunchBindsAccounts(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	run := e.launchCrew(t, "Named",
		map[string]any{"name": "named", "agentId": "claude-p", "prompt": "", "account": a, "model": "opus", "start": map[string]any{"when": "immediately"}},
		map[string]any{"name": "plain", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "immediately"}})
	named, plain := e.waitRunning(t, run, "named"), e.waitRunning(t, run, "plain")
	t.Cleanup(func() { e.stopAndWait(named); e.stopAndWait(plain) })
	if got := e.probe(t, named); got != login(folderA) {
		t.Fatalf("the named member: %s", got)
	}
	if got := e.probe(t, plain); got != login("") {
		t.Fatalf("the plain member: %s", got)
	}
	if l, _ := e.srv.lookupLocal(named); l.Launched().Model != "opus" {
		t.Fatalf("the named member's model: %q", l.Launched().Model)
	}
	e.expect(t, "POST", "/api/crews", map[string]any{"name": "Forged", "goal": "g", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "x", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "manual"}, "bound": map[string]any{"id": a, "source": "account", "folder": "/elsewhere", "login": "x"}}}}, http.StatusBadRequest, "invalid_crew")
}

// A resume from a session's record after a restart keeps its binding:
// a session of a registered account resumes on it; one whose login its
// entry set is refused login_not_kept, which its record cannot show is
// the same.
func TestRecordResumeKeepsAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	b, _ := e.account(t, "claude", "Other")
	sid := e.launch(t, "claude-p", map[string]any{"account": a})
	pinned := e.launch(t, "claude-pin", nil)
	for i, id := range []string{sid, pinned} {
		e.probe(t, id)
		// A turn its agent reported: a conversation to take up.
		l, _ := e.srv.lookupLocal(id)
		l.ReportAgentSession("conv-"+string(rune('a'+i)), session.PolicyLatest, true)
		deadline := time.Now().Add(10 * time.Second)
		for {
			_, out := e.do("GET", "/api/history/sessions/"+id, adminToken, nil)
			if as, _ := out["record"].(map[string]any)["agent"].(map[string]any)["session"].(map[string]any); as["resumable"] == true {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the record of %s holds no conversation: %v", id, out)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	e.setDefault(t, "claude", b)
	shutdown(e.testEnv)
	next := &bindEnv{testEnv: serveSecond(t, e.testEnv, newFakeUsage("claude"), newFakeUsage("codex")), probes: e.probes, pin: e.pin}
	next.expect(t, "POST", "/api/history/sessions/"+pinned+"/resume", map[string]any{"commandId": "c-1", "confirmLost": true}, http.StatusConflict, "login_not_kept")
	out := next.expect(t, "POST", "/api/history/sessions/"+sid+"/resume", map[string]any{"commandId": "c-2", "confirmLost": true}, http.StatusCreated, "")
	id := out["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() { next.stopAndWait(id) })
	if got, bound := next.probe(t, id), next.bound(t, id); got != login(folderA) || bound.ID != a {
		t.Fatalf("the record's resume: %s %+v", got, bound)
	}
}

// Brakeman's turn is bound from its configured folder: none is the tool's
// own login, never the adapter's default; a folder a registered account
// holds binds that account; any other binds as unlisted. The process's
// environment and the binding name the same folder.
func TestBrakemanTurnBinding(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	e.setDefault(t, "claude", a)
	other := realRoot(t, t.TempDir())
	for _, tc := range []struct {
		folder, id, source string
	}{{"", capacity.DefaultID, session.SourceOwn}, {folderA, a, session.SourceAccount}, {other, session.SourceUnlisted, session.SourceUnlisted}} {
		agent, _ := e.srv.Catalog().Get("claude-p")
		// As brakemanLaunch hands a turn's member to the one launch.
		var req createSessionRequest
		req.Account, req.login = capacity.DefaultID, loginWant{folder: tc.folder}
		req.AgentID, req.ctx = agent.ID, context.Background()
		local, aerr := e.srv.createLocalSession(req, nil)
		if aerr != nil {
			t.Fatalf("%q: %v", tc.folder, aerr)
		}
		sid := local.Info().ID
		t.Cleanup(func() { e.stopAndWait(sid) })
		got, bound := e.probe(t, sid), e.bound(t, sid)
		if got != login(tc.folder) || bound.ID != tc.id || bound.Source != tc.source || bound.Folder != tc.folder {
			t.Errorf("%q: %s %+v", tc.folder, got, bound)
		}
	}
}
