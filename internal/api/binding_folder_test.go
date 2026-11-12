package api

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// What runs in an account's folder holds that account and is counted on
// it, from its start to its end, whatever set its login (#856): the
// binding keeps the folder its process runs in, and the launch reserves
// the account whose folder that is before its process starts, as a
// launch that names the account does.

// addFolderEntries adds Claude Code stubs of bindAgents' kind to e's
// catalog, each with its entry's variables and its yolo recipe's (none for
// nil).
func (e *bindEnv) addFolderEntries(t *testing.T, entries map[string][2]map[string]string) {
	t.Helper()
	off := false
	list := e.srv.Catalog().List()
	for id, vars := range entries {
		en := map[string]string{"PROBE_DIR": e.probes}
		for k, v := range vars[0] {
			en[k] = v
		}
		a := catalog.Agent{ID: id, Name: id, Adapter: "claude", Command: []string{"/bin/sh", "-c", loginProbe}, Env: en, AllowArgs: true, Probe: &off}
		if vars[1] != nil {
			a.Yolo = &catalog.Yolo{Env: vars[1]}
		}
		list = append(list, a)
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: list})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)
}

// accountAt registers an account of adapter on folder: its id.
func (e *bindEnv) accountAt(t *testing.T, adapter, label, folder string) string {
	t.Helper()
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": adapter, "label": label, "folder": folder, "expectedRev": accountsRev(t, e.testEnv)}, http.StatusCreated, "")
	return out["account"].(map[string]any)["id"].(string)
}

// ownAccount registers an account on Claude Code's own folder under the
// stubs' home: its id and folder.
func (e *bindEnv) ownAccount(t *testing.T, label string) (string, string) {
	t.Helper()
	folder := ownFolder()
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return e.accountAt(t, "claude", label, folder), folder
}

// A launch into a registered account's folder holds the account from
// before its process starts, as one naming it does, whatever set the
// login: an entry that pins the folder; the same entry beside a key its
// yolo recipe adds; a yolo recipe that sets the folder variable; and the
// tool's own folder, an account registered on it, with nothing set. At the
// launch's seam before its process starts the remove is refused, a launch
// under way; the session registered, the binding keeps the folder (its
// source what set the login), the reservation is let go and the session
// holds the account and is counted on it; its process ended, the remove is
// taken. Where a Codex account was registered on the same folder first,
// the Claude Code launch holds and is counted on the Claude Code account.
func TestWhatRunsInAnAccountsFolderHoldsIt(t *testing.T) {
	e := newBindEnv(t, nil)
	folderA := realRoot(t, t.TempDir())
	e.accountAt(t, "codex", "Shared folder", folderA)
	a := e.accountAt(t, "claude", "Pinned", folderA)
	k, folderK := e.account(t, "claude", "Pinned beside a key")
	r, folderR := e.account(t, "claude", "Set by a recipe")
	o, folderO := e.ownAccount(t, "Own folder")
	e.addFolderEntries(t, map[string][2]map[string]string{
		"claude-on-a":   {{"CLAUDE_CONFIG_DIR": folderA}, nil},
		"claude-on-k":   {{"CLAUDE_CONFIG_DIR": folderK}, {"ANTHROPIC_API_KEY": "made-up-key-856"}},
		"claude-sets-r": {nil, {"CLAUDE_CONFIG_DIR": folderR}},
	})
	for _, tc := range []struct {
		name, account, folder, agent, source string
		yolo                                 bool
	}{
		{"an entry that pins the folder", a, folderA, "claude-on-a", session.SourcePinned, false},
		{"the entry beside a key its recipe adds", k, folderK, "claude-on-k", session.SourceLaunchEnv, true},
		{"a recipe that sets the folder", r, folderR, "claude-sets-r", session.SourceLaunchEnv, true},
		{"the tool's own folder", o, folderO, "claude-p", session.SourceOwn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached, release := make(chan struct{}), make(chan struct{})
			hook := func(stage string) {
				if stage == "checked" {
					close(reached)
					<-release
				}
			}
			launchHookFn.Store(&hook)
			launched := make(chan string, 1)
			go func() {
				_, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": tc.agent, "name": tc.name, "yolo": tc.yolo})
				id, _ := out["id"].(string)
				launched <- id
			}()
			<-reached
			launchHookFn.Store(nil)
			status, out := e.removeAccount(t, tc.account)
			close(release)
			sid := <-launched
			if sid == "" {
				t.Fatal("the launch did not start")
			}
			t.Cleanup(func() { e.stopAndWait(sid) })
			if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), "launch") {
				t.Fatalf("before its process starts: %d %v", status, out)
			}
			if b := e.bound(t, sid); b.Source != tc.source || b.Folder != tc.folder {
				t.Fatalf("the binding %+v", b)
			}
			status, out = e.removeAccount(t, tc.account)
			if names := strings.Join(namesOf(out), "|"); status != http.StatusConflict || !strings.Contains(names, "session "+tc.name) || strings.Contains(names, "launch") {
				t.Fatalf("its session registered: %d %v", status, out)
			}
			if v := viewOfKey(t, e.testEnv, "agent="+tc.agent+"&account="+tc.account); v["counts"].(map[string]any)["running"] != float64(1) {
				t.Fatalf("the account's counts %v", v["counts"])
			}
			e.stopAndWait(sid)
			if status, out := e.removeAccount(t, tc.account); status != http.StatusNoContent {
				t.Fatalf("its process ended: %d %v", status, out)
			}
		})
	}
}

// A launch of an entry that pins a registered account's folder, its
// process started, holds the account until that process has ended,
// whether or not its session was registered: at the session cap its
// registration is refused after its process started and its stop fails;
// the remove is refused while the process lives (a launch under way), the
// next launch into that folder is refused as one naming the account is,
// and once every such process has ended the remove is taken.
func TestFolderHeldByAProcessNeverRegistered(t *testing.T) {
	t.Setenv("HOME", realRoot(t, t.TempDir()))
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 8)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := &bindEnv{testEnv: capEnv(t, func(c *config.Config) { c.MaxSessions = 1 }, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin),
		withNativeRuntime(&sessiontest.FakeRuntime{Place: "seam", Base: terms})), probes: probes, pin: pin}
	a, folder := e.account(t, "claude", "Work")
	e.addAgents(t, map[string]map[string]string{"claude-on-a": {"CLAUDE_CONFIG_DIR": folder}})
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-p", "account": "default"}, http.StatusCreated, "")
	first := <-terms.procs
	t.Cleanup(func() { first.End(0) })
	e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-on-a"}, http.StatusConflict, "too_many_sessions")
	p := <-terms.procs
	status, out := e.removeAccount(t, a)
	if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), "launch") {
		t.Fatalf("its registration refused, its stop failed, its process running: %d %v", status, out)
	}
	for _, body := range []map[string]any{{"agentId": "claude-on-a"}, {"agentId": "claude-p", "account": a}} {
		e.expect(t, "POST", "/api/sessions", body, http.StatusConflict, "too_many_sessions")
		(<-terms.procs).End(0)
	}
	p.End(0)
	// The release follows the process's end: the remove is asked until it
	// is taken, and is never anything but in use before.
	for {
		status, out = e.removeAccount(t, a)
		if status == http.StatusNoContent {
			break
		}
		if status != http.StatusConflict {
			t.Fatalf("its process ended: %d %v", status, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A crew's members launched each of those ways hold the account whose
// folder each runs in: a member of an entry that pins a folder beside a key
// its yolo recipe adds, and one whose yolo recipe sets the folder, are
// bound launch_env to that folder in the run (a source the run offers no
// Runtime, TestBoundRunNotOffered), and the remove names the run and the
// member's session; a member on the tool's own folder, an account
// registered on it, is bound own (which a run keeps as no binding) and the
// remove names its session. Each is counted on its account.
func TestCrewMembersInAnAccountsFolderHoldIt(t *testing.T) {
	e := newBindEnv(t, nil)
	k, folderK := e.account(t, "claude", "Pinned beside a key")
	r, folderR := e.account(t, "claude", "Set by a recipe")
	o, _ := e.ownAccount(t, "Own folder")
	e.addFolderEntries(t, map[string][2]map[string]string{
		"claude-on-k":   {{"CLAUDE_CONFIG_DIR": folderK}, {"ANTHROPIC_API_KEY": "made-up-key-856"}},
		"claude-sets-r": {nil, {"CLAUDE_CONFIG_DIR": folderR}},
	})
	member := func(name, agent string) map[string]any {
		return map[string]any{"name": name, "agentId": agent, "prompt": "", "start": map[string]any{"when": "immediately"}}
	}
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Folders", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "yolo": true,
		"members": []any{member("keyed", "claude-on-k"), member("set", "claude-sets-r"), member("own", "claude-p")}}, http.StatusCreated)["id"].(string)
	out := e.expect(t, "POST", "/api/crews/"+id+"/launch", nil, http.StatusCreated, "")
	run := out["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
	for _, tc := range []struct {
		member, agent, account, folder, source string
	}{
		{"keyed", "claude-on-k", k, folderK, session.SourceLaunchEnv},
		{"set", "claude-sets-r", r, folderR, session.SourceLaunchEnv},
		{"own", "claude-p", o, "", ""},
	} {
		t.Run(tc.member, func(t *testing.T) {
			sid := e.waitRunning(t, run, tc.member)
			status, out := e.removeAccount(t, tc.account)
			names := strings.Join(namesOf(out), "|")
			if status != http.StatusConflict || !strings.Contains(names, "session "+e.local(sid).Info().Name) || (tc.source != "") != strings.Contains(names, "run "+run) {
				t.Fatalf("the remove: %d %v", status, out)
			}
			got, _ := e.srv.runs.Get(run)
			var b session.BoundAccount
			for _, m := range got.Members {
				if m.Name == tc.member {
					b = m.Binding()
				}
			}
			if b.Source != tc.source || b.Folder != tc.folder {
				t.Fatalf("the run's binding %+v", b)
			}
			if v := viewOfKey(t, e.testEnv, "agent="+tc.agent+"&account="+tc.account); v["counts"].(map[string]any)["running"] != float64(1) {
				t.Fatalf("the account's counts %v", v["counts"])
			}
		})
	}
}
