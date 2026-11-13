package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A launch of this server holds every registered account whose folder it
// runs in, as the launch's own adapter's folder variable or home says
// where that is: decided when the launch is reserved, before its process
// starts, and kept until the process has ended. Its session is counted on
// one account, decided at its registration (#856). The binding stays what
// #845 made it; the ids held and the account counted on are kept beside
// it, in memory, and no folder is asked about again after the reservation.

// addFolderEntries adds stubs of bindAgents' kind to e's catalog (Codex's
// for an id that starts codex-, else Claude Code's), each with its entry's
// variables and its yolo recipe's (none for nil).
func (e *bindEnv) addFolderEntries(t *testing.T, entries map[string][2]map[string]string) {
	t.Helper()
	off := false
	list := e.srv.Catalog().List()
	for id, vars := range entries {
		en := map[string]string{"PROBE_DIR": e.probes}
		for k, v := range vars[0] {
			en[k] = v
		}
		adapter := "claude"
		if strings.HasPrefix(id, "codex-") {
			adapter = "codex"
		}
		a := catalog.Agent{ID: id, Name: id, Adapter: adapter, Command: []string{"/bin/sh", "-c", loginProbe}, Env: en, AllowArgs: true, Probe: &off}
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

// holdAtStart holds the next launch that reaches the seam before its
// process starts (launchHookFn's checked): wait returns once one is held
// there, and let lets it go.
func holdAtStart(t *testing.T) (wait, let func()) {
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(stage string) {
		if stage == "checked" {
			once.Do(func() {
				close(reached)
				<-release
			})
		}
	}
	launchHookFn.Store(&hook)
	var let1 sync.Once
	let = func() { let1.Do(func() { close(release) }) }
	t.Cleanup(func() { launchHookFn.Store(nil); let() })
	return func() { <-reached; launchHookFn.Store(nil) }, let
}

// inUse fails unless the remove of each of ids is refused account_in_use
// naming what.
func (e *bindEnv) inUse(t *testing.T, when, what string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		status, out := e.removeAccount(t, id)
		if status != http.StatusConflict || errorCode(out) != "account_in_use" || !strings.Contains(strings.Join(namesOf(out), "|"), what) {
			t.Errorf("%s, the remove of %s: %d %v", when, id, status, out)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

// removed fails unless the remove of each of ids is taken.
func (e *bindEnv) removed(t *testing.T, when string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if status, out := e.removeAccount(t, id); status != http.StatusNoContent {
			t.Fatalf("%s, the remove of %s: %d %v", when, id, status, out)
		}
	}
}

// removeAtEnd removes the account id when t ends, failed or not: the
// remove is asked until nothing holds the account (what t started is
// stopped by the cleanups registered after this one, which run first), and
// an account already removed is left so.
func (e *bindEnv) removeAtEnd(t *testing.T, id string) {
	t.Cleanup(func() {
		waitUntil(t, "the remove of "+id+" at the end", func() bool {
			_, out := e.removeAccount(t, id)
			return errorCode(out) != "account_in_use"
		})
	})
}

// counted fails unless agent's sessions on account are running running,
// and started started in each window.
func (e *bindEnv) counted(t *testing.T, agent, account string, running, started int) {
	t.Helper()
	c := viewOfKey(t, e.testEnv, "agent="+agent+"&account="+account)["counts"].(map[string]any)
	if c["running"] != float64(running) || c["started5h"] != float64(started) || c["started7d"] != float64(started) {
		t.Fatalf("%s on %s: the counts %v, want running %d and started %d", agent, account, c, running, started)
	}
}

// A binding is what #845 made it, for every source: the tool's own login,
// a key its entry sets alone and a login the launch's own environment sets
// (with or without the folder variable) keep no folder; an entry that pins
// the folder keeps it, as does an account. Made again under a binding in
// that form, the comparison a resume and a continuation make, each launch
// starts.
func TestABindingIsWhatMainMadeIt(t *testing.T) {
	e := newBindEnv(t, nil)
	a, folderA := e.account(t, "claude", "Work")
	other := realRoot(t, t.TempDir())
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-key": {{"ANTHROPIC_API_KEY": "made-up-key-856"}, nil}})
	for _, tc := range []struct {
		name, agent, account string
		set                  map[string]string
		source, folder       string
	}{
		{"the tool's own login", "claude-p", "default", nil, session.SourceOwn, ""},
		{"a key its entry sets alone", "claude-key", "", nil, session.SourcePinned, ""},
		{"a key its launch sets", "claude-p", "", map[string]string{"ANTHROPIC_API_KEY": "made-up-key-856"}, session.SourceLaunchEnv, ""},
		{"a folder its launch sets", "claude-p", "", map[string]string{"CLAUDE_CONFIG_DIR": other}, session.SourceLaunchEnv, ""},
		{"an entry that pins the folder", "claude-pin", "", nil, session.SourcePinned, e.pin},
		{"an account", "claude-p", a, nil, session.SourceAccount, folderA},
	} {
		sid, aerr := e.startAs(t, tc.agent, tc.account, loginWant{}, tc.set)
		if aerr != nil {
			t.Fatalf("%s: %v", tc.name, aerr)
		}
		b := e.bound(t, sid)
		was := session.BoundAccount{ID: b.ID, Label: b.Label, Folder: tc.folder, Source: tc.source, Login: b.Login}
		if _, aerr := e.startAs(t, tc.agent, "", loginWant{bound: &was}, tc.set); aerr != nil {
			t.Errorf("%s, made again under %+v: %d %s %s", tc.name, was, aerr.status, aerr.Code, aerr.Message)
		}
		if b != was {
			t.Errorf("%s: the binding %+v, want %+v", tc.name, b, was)
		}
	}
}

// A launch into a registered account's folder holds the account from
// before its process starts, whatever set the login: an entry that pins the
// folder; the same entry beside a key its yolo recipe adds; a yolo recipe
// that sets the folder variable; and the tool's own folder, an account
// registered on it, with nothing set. At the launch's seam before its
// process starts the remove is refused, a launch under way; the session
// registered, its binding is #845's and the account is among the ids it
// keeps as held, the reservation is let go and the session holds the account
// and is counted on it in both windows; its process ended, the starts stay
// counted and the remove is taken. Where a Codex account was registered on
// the same folder first, the Claude Code launch is counted on the Claude
// Code account.
func TestWhatRunsInAnAccountsFolderHoldsIt(t *testing.T) {
	e := newBindEnv(t, nil)
	folderA := realRoot(t, t.TempDir())
	e.accountAt(t, "codex", "Shared folder", folderA)
	a := e.accountAt(t, "claude", "Pinned", folderA)
	k, folderK := e.account(t, "claude", "Pinned beside a key")
	r, folderR := e.account(t, "claude", "Set by a recipe")
	o, _ := e.ownAccount(t, "Own folder")
	e.addFolderEntries(t, map[string][2]map[string]string{
		"claude-on-a":   {{"CLAUDE_CONFIG_DIR": folderA}, nil},
		"claude-on-k":   {{"CLAUDE_CONFIG_DIR": folderK}, {"ANTHROPIC_API_KEY": "made-up-key-856"}},
		"claude-sets-r": {nil, {"CLAUDE_CONFIG_DIR": folderR}},
	})
	for _, tc := range []struct {
		name, account, kept, agent, source string
		yolo                               bool
	}{
		{"an entry that pins the folder", a, folderA, "claude-on-a", session.SourcePinned, false},
		{"the entry beside a key its recipe adds", k, "", "claude-on-k", session.SourceLaunchEnv, true},
		{"a recipe that sets the folder", r, "", "claude-sets-r", session.SourceLaunchEnv, true},
		{"the tool's own folder", o, "", "claude-p", session.SourceOwn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, let := holdAtStart(t)
			launched := make(chan string, 1)
			go func() {
				_, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": tc.agent, "name": tc.name, "yolo": tc.yolo})
				id, _ := out["id"].(string)
				launched <- id
			}()
			wait()
			e.inUse(t, "before its process starts", "launch", tc.account)
			let()
			sid := <-launched
			if sid == "" {
				t.Fatal("the launch did not start")
			}
			t.Cleanup(func() { e.stopAndWait(sid) })
			l := e.local(sid).Launched()
			if b := l.Account; b.Source != tc.source || b.Folder != tc.kept || !slices.Contains(l.Holds, tc.account) {
				t.Fatalf("the binding %+v, the accounts held %v", b, l.Holds)
			}
			status, out := e.removeAccount(t, tc.account)
			if names := strings.Join(namesOf(out), "|"); status != http.StatusConflict || !strings.Contains(names, "session "+tc.name) || strings.Contains(names, "launch") {
				t.Fatalf("its session registered: %d %v", status, out)
			}
			e.counted(t, tc.agent, tc.account, 1, 1)
			e.stopAndWait(sid)
			e.counted(t, tc.agent, tc.account, 0, 1)
			e.removed(t, "its process ended", tc.account)
		})
	}
}

// A launch of an entry that pins a registered account's folder, its
// process started, holds the account until that process has ended,
// whether or not its session was registered: at the session cap its
// registration is refused after its process started and its stop fails;
// the remove is refused while the process lives (a launch under way), and
// once it has ended the remove is taken.
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
	e.inUse(t, "its registration refused, its stop failed, its process running", "launch", a)
	p.End(0)
	<-p.Done()
	// The release follows the process's end: the remove is asked until it
	// is taken, within the test's own wait, and is never anything but in
	// use before.
	waitUntil(t, "its process ended, the remove of "+a+" taken", func() bool {
		status, out := e.removeAccount(t, a)
		if status != http.StatusNoContent && status != http.StatusConflict {
			t.Fatalf("its process ended: %d %v", status, out)
		}
		return status == http.StatusNoContent
	})
}

// What holds an account is one question, asked for every account: a
// launch runs in an account's folder when it names the account or the
// folder it runs in is the account's, whatever the account's adapter. Two
// Claude Code accounts on one folder (one registered through a link to it)
// and a Codex account on it: a Claude Code launch of an entry that pins the
// folder, a Codex launch of one that pins it, and a Claude Code launch that
// names the account registered through the link each hold all three from
// before its process starts until it has ended, and are counted on one
// account, of the launch's own adapter (the one named, else the first of
// its adapter on the folder).
func TestEveryAccountOnTheFolderIsHeld(t *testing.T) {
	e := newBindEnv(t, nil)
	for _, tc := range []struct {
		name, agent, adapter, pins, named string
	}{
		{"a Claude Code entry that pins the folder", "claude-on-f", "claude", "CLAUDE_CONFIG_DIR", ""},
		{"a Codex entry that pins the folder", "codex-on-f", "codex", "CODEX_HOME", ""},
		{"a launch that names the account through the link", "claude-p", "claude", "", "linked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := realRoot(t, t.TempDir())
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(folder, link); err != nil {
				t.Fatal(err)
			}
			if tc.pins != "" {
				e.addFolderEntries(t, map[string][2]map[string]string{tc.agent: {{tc.pins: folder}, nil}})
			}
			ids := map[string]string{
				"first":  e.accountAt(t, "claude", "First", folder),
				"linked": e.accountAt(t, "claude", "Linked", link),
				"codex":  e.accountAt(t, "codex", "Codex", folder),
			}
			all := []string{ids["first"], ids["linked"], ids["codex"]}
			body := map[string]any{"agentId": tc.agent, "name": tc.name}
			if tc.named != "" {
				body["account"] = ids[tc.named]
			}
			wait, let := holdAtStart(t)
			launched := make(chan string, 1)
			go func() {
				_, out := e.do("POST", "/api/sessions", adminToken, body)
				id, _ := out["id"].(string)
				launched <- id
			}()
			wait()
			e.inUse(t, "before its process starts", "launch", all...)
			let()
			sid := <-launched
			if sid == "" {
				t.Fatal("the launch did not start")
			}
			t.Cleanup(func() { e.stopAndWait(sid) })
			e.inUse(t, "its session registered", "session "+tc.name, all...)
			on := map[string]string{"claude": ids["first"], "codex": ids["codex"]}[tc.adapter]
			if tc.named != "" {
				on = ids[tc.named]
			}
			for _, id := range all {
				if id != on && adapterOf(t, e, id) == tc.adapter {
					e.counted(t, tc.agent, id, 0, 0)
				}
			}
			e.counted(t, tc.agent, on, 1, 1)
			e.stopAndWait(sid)
			e.counted(t, tc.agent, on, 0, 1)
			e.removed(t, "its process ended", all...)
		})
	}
}

// adapterOf is the adapter of the account id.
func adapterOf(t *testing.T, e *bindEnv, id string) string {
	t.Helper()
	doc, err := e.srv.capacity.st.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range doc.Accounts {
		if a.ID == id {
			return a.Adapter
		}
	}
	t.Fatalf("no account %s", id)
	return ""
}

// A home that is not a whole path is read as the process reads it: for a
// session, under the folder it starts in, so a launch whose entry sets
// HOME to one, where that home's tool folder is a registered account's,
// holds the account from before its process starts until it has ended;
// for a crew's member, bound before it has a folder, it is refused as a
// folder variable that is not whole is, in words that say its home is not
// a whole path and name no variable and no folder.
func TestAHomeNotWholeIsReadUnderTheStartFolder(t *testing.T) {
	e := newBindEnv(t, nil)
	folder := filepath.Join(realRoot(t, e.root), "h", ".claude")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	a := e.accountAt(t, "claude", "Under a home", folder)
	e.addFolderEntries(t, map[string][2]map[string]string{
		"claude-home": {{"HOME": "h"}, nil},
		"claude-dir":  {{"CLAUDE_CONFIG_DIR": "h/.claude"}, nil},
	})
	wait, let := holdAtStart(t)
	launched := make(chan string, 1)
	go func() {
		_, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "claude-home", "name": "home", "cwd": e.root})
		id, _ := out["id"].(string)
		launched <- id
	}()
	wait()
	e.inUse(t, "before its process starts", "launch", a)
	let()
	sid := <-launched
	if sid == "" {
		t.Fatal("the launch did not start")
	}
	t.Cleanup(func() { e.stopAndWait(sid) })
	if got := e.probe(t, sid); got != "D=[] K=[] H=[]" {
		t.Fatalf("the login: %s", got)
	}
	e.inUse(t, "its session registered", "session home", a)
	e.counted(t, "claude-home", a, 1, 1)
	e.stopAndWait(sid)
	e.removed(t, "its process ended", a)
	words := map[string]string{}
	for _, agent := range []string{"claude-home", "claude-dir"} {
		id := e.sendCrew("POST", "/api/crews", map[string]any{"name": agent, "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
			"members": []any{map[string]any{"name": "m", "agentId": agent, "prompt": "", "start": map[string]any{"when": "immediately"}}}}, http.StatusCreated)["id"].(string)
		out := e.expect(t, "POST", "/api/crews/"+id+"/launch", nil, http.StatusBadRequest, "invalid_account")
		words[agent], _ = out["error"].(map[string]any)["message"].(string)
	}
	tail := ", which would be another login in each of its working folders: set a whole path"
	if !strings.HasPrefix(words["claude-home"], "member m: its home is not a whole path: ") || !strings.HasSuffix(words["claude-home"], tail) ||
		!strings.Contains(words["claude-dir"], "CLAUDE_CONFIG_DIR") || !strings.HasSuffix(words["claude-dir"], tail) {
		t.Fatalf("the member's refusals: %q and %q", words["claude-home"], words["claude-dir"])
	}
	for _, w := range words {
		if strings.Contains(w, "h/") || strings.Contains(w, e.root) {
			t.Fatalf("a refusal names a folder: %q", w)
		}
	}
	if strings.Contains(words["claude-home"], "HOME") {
		t.Fatalf("the home's refusal names a variable: %q", words["claude-home"])
	}
}

// A crew's members launched each of those ways hold the account whose
// folder each runs in from before each one's process starts: a member of
// an entry that pins a folder (its binding the folder, which the run keeps,
// so the remove names the run too), one beside a key its yolo recipe adds
// and one whose yolo recipe sets the folder (bound launch_env with no
// folder, as #845 binds them), and one on the tool's own folder (which a
// run keeps as no binding). At its seam before its process starts the
// remove is refused, a launch under way; its session running, the remove
// names it and it is counted on the account; the run stopped and its
// process ended, the starts stay counted and the remove is taken. A Codex
// account on the pinned folder is named by the run as the Claude Code
// account is. A managed run's attempt in a worktree of its own, its retry
// and a member started later on the tool's own folder are held the same
// way, each from before its process starts, and counted on the account.
// A subtest that registers an account removes it when it ends, failed or
// not, so that a later one does not meet it on the tool's own folder.
func TestCrewMembersInAnAccountsFolderHoldIt(t *testing.T) {
	e := newBindEnv(t, nil)
	e.gitRepo(t)
	p, folderP := e.account(t, "claude", "Pinned")
	pc := e.accountAt(t, "codex", "Pinned, Codex", folderP)
	k, folderK := e.account(t, "claude", "Pinned beside a key")
	r, folderR := e.account(t, "claude", "Set by a recipe")
	e.addFolderEntries(t, map[string][2]map[string]string{
		"claude-on-p":   {{"CLAUDE_CONFIG_DIR": folderP}, nil},
		"claude-on-k":   {{"CLAUDE_CONFIG_DIR": folderK}, {"ANTHROPIC_API_KEY": "made-up-key-856"}},
		"claude-sets-r": {nil, {"CLAUDE_CONFIG_DIR": folderR}},
	})
	member := func(name, agent, when string) map[string]any {
		return map[string]any{"name": name, "agentId": agent, "prompt": "", "start": map[string]any{"when": when}}
	}
	// held launches what start does, held at its seam, and answers the
	// run once start's reply is in.
	held := func(t *testing.T, account string, start func() string) string {
		t.Helper()
		wait, let := holdAtStart(t)
		launched := make(chan string, 1)
		go func() { launched <- start() }()
		wait()
		e.inUse(t, "before its process starts", "launch", account)
		let()
		return <-launched
	}
	for _, tc := range []struct {
		member, agent, account, folder, source string
	}{
		{"pins", "claude-on-p", p, folderP, session.SourcePinned},
		{"keyed", "claude-on-k", k, "", session.SourceLaunchEnv},
		{"set", "claude-sets-r", r, "", session.SourceLaunchEnv},
		{"own", "claude-p", "", "", ""},
	} {
		t.Run(tc.member, func(t *testing.T) {
			account := tc.account
			if account == "" {
				account, _ = e.ownAccount(t, "Own folder")
				e.removeAtEnd(t, account)
			}
			id := e.sendCrew("POST", "/api/crews", map[string]any{"name": tc.member, "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "yolo": true,
				"members": []any{member(tc.member, tc.agent, "immediately")}}, http.StatusCreated)["id"].(string)
			run := held(t, account, func() string {
				_, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
				r, _ := out["run"].(map[string]any)
				id, _ := r["id"].(string)
				return id
			})
			if run == "" {
				t.Fatal("the crew did not launch")
			}
			t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
			sid := e.waitRunning(t, run, tc.member)
			e.inUse(t, "its session running", "session "+e.local(sid).Info().Name, account)
			if tc.folder != "" {
				e.inUse(t, "its session running", "run "+run, account, pc)
			}
			got, _ := e.srv.runs.Get(run)
			for _, m := range got.Members {
				if b := m.Binding(); m.Name == tc.member && (b.Source != tc.source || b.Folder != tc.folder) {
					t.Fatalf("the run's binding %+v", b)
				}
			}
			e.counted(t, tc.agent, account, 1, 1)
			e.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
			e.stopAndWait(sid)
			e.counted(t, tc.agent, account, 0, 1)
			e.removed(t, "the run stopped, its process ended", account)
			if tc.folder != "" {
				e.removed(t, "the run stopped, its process ended", pc)
			}
		})
	}
	t.Run("a managed attempt and its retry", func(t *testing.T) {
		o, _ := e.ownAccount(t, "Own folder, managed")
		e.removeAtEnd(t, o)
		id := e.managedOf(t, map[string]any{"agentId": "claude-p"})
		run := held(t, o, func() string {
			_, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
			r, _ := out["run"].(map[string]any)
			id, _ := r["id"].(string)
			return id
		})
		if run == "" {
			t.Fatal("the crew did not launch")
		}
		t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
		first := startedAttempt(t, e.testEnv, run, 1)["sessionId"].(string)
		e.inUse(t, "attempt 1 running", "session ", o)
		e.counted(t, "claude-p", o, 1, 1)
		command(t, e.testEnv, run, "tasks/build/cancel")
		e.stopAndWait(first)
		e.counted(t, "claude-p", o, 0, 1)
		held(t, o, func() string {
			e.do("POST", "/api/runs/"+run+"/tasks/build/retry", adminToken, map[string]any{"commandId": opID()})
			return ""
		})
		second := startedAttempt(t, e.testEnv, run, 2)["sessionId"].(string)
		e.inUse(t, "attempt 2 running", "session ", o)
		e.counted(t, "claude-p", o, 1, 2)
		e.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
		e.stopAndWait(second)
		e.counted(t, "claude-p", o, 0, 2)
		e.removed(t, "the run stopped, its processes ended", o)
	})
	t.Run("a member started later", func(t *testing.T) {
		o, _ := e.ownAccount(t, "Own folder, later")
		e.removeAtEnd(t, o)
		run := e.launchCrew(t, "Later", member("lead", "cat", "immediately"), member("later", "claude-p", "manual"))
		t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
		lead := e.waitRunning(t, run, "lead")
		held(t, o, func() string {
			e.do("POST", "/api/runs/"+run+"/members/later/start", adminToken, nil)
			return ""
		})
		later := e.waitRunning(t, run, "later")
		e.inUse(t, "its session running", "session "+e.local(later).Info().Name, o)
		e.counted(t, "claude-p", o, 1, 1)
		e.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
		e.stopAndWait(later)
		e.stopAndWait(lead)
		e.counted(t, "claude-p", o, 0, 1)
		e.removed(t, "the run stopped, its process ended", o)
	})
}

// launchHeld launches what body asks, holds it at the seam before its
// process starts while before runs, and answers its session's id.
func (e *bindEnv) launchHeld(t *testing.T, body map[string]any, before func()) string {
	t.Helper()
	wait, let := holdAtStart(t)
	launched := make(chan string, 1)
	go func() {
		_, out := e.do("POST", "/api/sessions", adminToken, body)
		id, _ := out["id"].(string)
		launched <- id
	}()
	wait()
	before()
	let()
	sid := <-launched
	if sid == "" {
		t.Fatal("the launch did not start")
	}
	t.Cleanup(func() { e.stopAndWait(sid) })
	return sid
}

// caseFolding is a folder of the test's own on a file system that folds
// case (a folder made as acct is the one ACCT names); the test is skipped
// where its temporary folder does not.
func caseFolding(t *testing.T) string {
	t.Helper()
	dir := realRoot(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(dir, "acct"), 0o700); err != nil {
		t.Fatal(err)
	}
	lower, err := os.Stat(filepath.Join(dir, "acct"))
	if err != nil {
		t.Fatal(err)
	}
	if upper, err := os.Stat(filepath.Join(dir, "ACCT")); err != nil || !os.SameFile(lower, upper) {
		t.Skip("the test's temporary folder does not fold case: it runs with its temporary folder (both GOTMPDIR and TMPDIR) on a file system that folds case")
	}
	return dir
}

// A folder is an account's when the file system says it is the same
// directory, however it is written: an entry that names the account's
// folder in other letters' case, on a file system that folds case, holds
// the account from before its process starts until it has ended, and its
// session is counted on it.
func TestAFolderIsTheAccountsHoweverItIsWritten(t *testing.T) {
	dir := caseFolding(t)
	e := newBindEnv(t, nil)
	a := e.accountAt(t, "claude", "Lower case", filepath.Join(dir, "acct"))
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-upper": {{"CLAUDE_CONFIG_DIR": filepath.Join(dir, "ACCT")}, nil}})
	sid := e.launchHeld(t, map[string]any{"agentId": "claude-upper", "name": "upper"}, func() {
		e.inUse(t, "before its process starts", "launch", a)
	})
	e.inUse(t, "its session registered", "session upper", a)
	e.counted(t, "claude-upper", a, 1, 1)
	e.stopAndWait(sid)
	e.counted(t, "claude-upper", a, 0, 1)
	e.removed(t, "its process ended", a)
}

// A folder variable that is not a whole path is read both ways under the
// folder the session starts in: as written, links followed before its
// "..", and cleaned. With h a link to another directory, h/../h2/.claude
// holds the account on the folder the link leads to and the account on
// the cleaned path, each from before the process starts until it has
// ended.
func TestAFolderNotWholeIsReadBothWays(t *testing.T) {
	e := newBindEnv(t, nil)
	root := realRoot(t, e.root)
	physical, cleaned := filepath.Join(root, "x", "h2", ".claude"), filepath.Join(root, "h2", ".claude")
	for _, d := range []string{filepath.Join(root, "x", "sub"), physical, cleaned} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "x", "sub"), filepath.Join(root, "h")); err != nil {
		t.Fatal(err)
	}
	p := e.accountAt(t, "claude", "Where the link leads", physical)
	c := e.accountAt(t, "claude", "The cleaned path", cleaned)
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-dotdot": {{"CLAUDE_CONFIG_DIR": "h/../h2/.claude"}, nil}})
	sid := e.launchHeld(t, map[string]any{"agentId": "claude-dotdot", "name": "dotdot", "cwd": e.root}, func() {
		e.inUse(t, "before its process starts", "launch", p, c)
	})
	e.inUse(t, "its session registered", "session dotdot", p, c)
	e.stopAndWait(sid)
	e.removed(t, "its process ended", p, c)
}

// A session is counted on the account it names, else its own adapter's
// account on the folder it runs in, else the default login: a Claude Code
// entry that pins a folder only a Codex account is on holds that account
// from before its process starts until it has ended, and its session is
// counted on the default login, running and started, after its end too.
func TestASessionIsCountedOnItsOwnAdaptersAccount(t *testing.T) {
	e := newBindEnv(t, nil)
	folder := realRoot(t, t.TempDir())
	x := e.accountAt(t, "codex", "Codex alone", folder)
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-on-x": {{"CLAUDE_CONFIG_DIR": folder}, nil}})
	sid := e.launchHeld(t, map[string]any{"agentId": "claude-on-x", "name": "alone"}, func() {
		e.inUse(t, "before its process starts", "launch", x)
	})
	e.inUse(t, "its session registered", "session alone", x)
	e.counted(t, "claude-on-x", "default", 1, 1)
	e.stopAndWait(sid)
	e.counted(t, "claude-on-x", "default", 0, 1)
	e.removed(t, "its process ended", x)
}

// What a launch holds is decided at its reservation and kept: an entry
// whose folder variable is not a whole path, h/.claude, started in a folder
// where h is a link to the directory a registered account's folder is in,
// holds the account though the start folder is renamed and another
// directory put in its place after the reservation (at the seam before its
// process starts, which runs in the folder opened). The remove is refused
// there, refused once the session is registered, and taken once its
// process has ended.
func TestWhatALaunchHoldsIsKeptFromItsReservation(t *testing.T) {
	e := newBindEnv(t, nil)
	logins, start := filepath.Join(realRoot(t, t.TempDir()), "logins"), filepath.Join(e.root, "start")
	for _, d := range []string{filepath.Join(logins, ".claude"), start} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(logins, filepath.Join(start, "h")); err != nil {
		t.Fatal(err)
	}
	a := e.accountAt(t, "claude", "Through a link", filepath.Join(logins, ".claude"))
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-rel": {{"CLAUDE_CONFIG_DIR": filepath.Join("h", ".claude")}, nil}})
	sid := e.launchHeld(t, map[string]any{"agentId": "claude-rel", "name": "moved", "cwd": start}, func() {
		if err := os.Rename(start, start+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(start, 0o700); err != nil {
			t.Fatal(err)
		}
		e.inUse(t, "its start folder replaced, before its process starts", "launch", a)
	})
	e.inUse(t, "its start folder replaced, its session registered", "session moved", a)
	e.stopAndWait(sid)
	e.removed(t, "its process ended", a)
}

// An account registered on a folder after a launch into it was reserved is
// not held by that launch: an entry pins a folder a Claude Code account is
// on, and another Claude Code account is registered on the folder, through
// a link to it, at the seam before its process starts. Its session
// registered and running, the later account's remove is taken; the first
// account's is refused naming the session, which is counted on it, and
// taken once its process has ended.
func TestAnAccountRegisteredAfterTheReservationIsNotHeld(t *testing.T) {
	e := newBindEnv(t, nil)
	folder := realRoot(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatal(err)
	}
	a := e.accountAt(t, "claude", "First", folder)
	e.addFolderEntries(t, map[string][2]map[string]string{"claude-on-f": {{"CLAUDE_CONFIG_DIR": folder}, nil}})
	var late string
	sid := e.launchHeld(t, map[string]any{"agentId": "claude-on-f", "name": "early"}, func() {
		late = e.accountAt(t, "claude", "Late", link)
		e.inUse(t, "before its process starts", "launch", a)
	})
	e.removed(t, "its session running", late)
	e.inUse(t, "its session running", "session early", a)
	e.counted(t, "claude-on-f", a, 1, 1)
	e.stopAndWait(sid)
	e.removed(t, "its process ended", a)
}

// A session is counted on one account, decided at its registration and
// kept: a session of the tool's own login is started while no account is
// on the tool's own folder, then an account is registered on that folder.
// The running count and both start counts stay together on the default
// login, and the account counts none, while the session runs and after it
// has ended.
func TestASessionStaysCountedWhereItsRegistrationCountedIt(t *testing.T) {
	e := newBindEnv(t, nil)
	if err := os.MkdirAll(ownFolder(), 0o700); err != nil {
		t.Fatal(err)
	}
	sid := e.launch(t, "claude-p", map[string]any{"name": "own"})
	if b := e.bound(t, sid); b.Source != session.SourceOwn {
		t.Fatalf("the binding %+v", b)
	}
	o := e.accountAt(t, "claude", "Registered later", ownFolder())
	e.counted(t, "claude-p", capacity.DefaultID, 1, 1)
	e.counted(t, "claude-p", o, 0, 0)
	e.stopAndWait(sid)
	e.counted(t, "claude-p", capacity.DefaultID, 0, 1)
	e.counted(t, "claude-p", o, 0, 0)
	e.removed(t, "its process ended", o)
}

// The lookup that decides a binding compares a folder wanted as main does,
// clean and through its links, while what a launch holds is the same
// directory however written: on a file system that folds case, Brakeman's
// folder wanted in other letters' case than a registered Claude Code
// account's binds unlisted, the folder as wanted, and the launch holds the
// account until its process has ended.
func TestABindingComparesAWantedFolderAsMainDoes(t *testing.T) {
	dir := caseFolding(t)
	e := newBindEnv(t, nil)
	a := e.accountAt(t, "claude", "Lower case", filepath.Join(dir, "acct"))
	upper := filepath.Join(dir, "ACCT")
	sid, aerr := e.startAs(t, "claude-p", capacity.DefaultID, loginWant{folder: upper}, nil)
	if aerr != nil {
		t.Fatalf("the launch: %d %s %s", aerr.status, aerr.Code, aerr.Message)
	}
	if b := e.bound(t, sid); b.Source != session.SourceUnlisted || b.ID != session.SourceUnlisted || b.Folder != upper {
		t.Fatalf("the binding %+v, want unlisted on %s", b, upper)
	}
	e.inUse(t, "its session registered", "session ", a)
	e.stopAndWait(sid)
	e.removed(t, "its process ended", a)
}
