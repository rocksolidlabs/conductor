package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Capacity's routes, event and readers (docs/plans/capacity.md, PR-3).
// No real agent tool runs here: the readers are fakes in the shape of
// internal/agents' (withUsageReaders), whose readings are invented, and a
// Claude Code fake "starts its program" only once its read is admitted.

// withUsageReaders has the server read usage through readers, by adapter.
func withUsageReaders(readers map[string]*agents.UsageReader) Option {
	return func(o *options) { o.usageReaders = readers }
}

// fakeUsage is a reader of one adapter. Its version is the catalog
// command's second word ("2.0.0" when none), the version a real reader
// would find; for Claude Code it asks Admit first and starts (counts a
// start, then tells Done its outcome) only when admitted.
type fakeUsage struct {
	adapter string
	mu      sync.Mutex
	calls   int
	starts  map[string]int // by version
	outcome agents.Outcome
	report  agents.UsageReport
	env     []string
	gate    chan struct{}
	called  chan struct{}
}

func newFakeUsage(adapter string) *fakeUsage {
	// A week ahead of the cache's clock (the real one here), so no
	// reading of the test passes its reset.
	reset := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	off := false
	return &fakeUsage{adapter: adapter, starts: map[string]int{}, outcome: agents.Local, report: agents.UsageReport{
		State: agents.UsageOK, Plan: "max",
		Windows: []agents.UsageWindow{{Kind: "weekly_all", Name: "Weekly, all models", Percent: 40, ResetsAt: &reset, Severity: "normal"}},
		Credits: []agents.UsageCredit{{Kind: "extra_usage", Enabled: &off}},
	}}
}

func (f *fakeUsage) reader() *agents.UsageReader {
	return &agents.UsageReader{Qualified: "2.0.0", MinVersion: "2.0.0", Read: f.read}
}

func (f *fakeUsage) set(fn func(f *fakeUsage)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeUsage) count() (calls int, starts map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, maps.Clone(f.starts)
}

func (f *fakeUsage) read(ctx context.Context, in agents.UsageInput) agents.UsageReport {
	version := "2.0.0"
	if len(in.Command) > 1 {
		version = in.Command[1]
	}
	f.mu.Lock()
	f.calls++
	f.env = slices.Clone(in.Env)
	gate, called, outcome, rep := f.gate, f.called, f.outcome, f.report
	f.mu.Unlock()
	if called != nil {
		called <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return agents.UsageReport{State: agents.UsageError, Words: "the read was stopped"}
		}
	}
	if f.adapter == "claude" {
		if in.Admit == nil {
			return agents.UsageReport{State: agents.UsageUnqualified, Words: "no admission", Version: version}
		}
		adm, err := in.Admit(version)
		if err != nil {
			return agents.UsageReport{State: agents.UsageUnqualified, Words: err.Error(), Version: version}
		}
		f.mu.Lock()
		f.starts[version]++
		f.mu.Unlock()
		adm.Done(outcome)
		switch outcome {
		case agents.Turned:
			return agents.UsageReport{State: agents.UsageUnqualified, Words: "a model turn was taken", Version: version, Outcome: outcome}
		case agents.Inconclusive:
			return agents.UsageReport{State: agents.UsageError, Words: "the check ended without an answer", Version: version, Outcome: outcome}
		}
	} else {
		f.mu.Lock()
		f.starts[version]++
		f.mu.Unlock()
	}
	rep.Version, rep.Outcome = version, outcome
	return rep
}

// capAgents are the catalog entries of the capacity tests: a Claude Code
// and a Codex entry on /bin/cat (installed), and one not installed.
var capAgents = []catalog.Agent{
	{ID: "claude-a", Name: "Claude A", Adapter: "claude", Command: []string{"/bin/cat"}},
	{ID: "codex-a", Name: "Codex A", Adapter: "codex", Command: []string{"/bin/cat"}},
	{ID: "ghost", Name: "Ghost", Adapter: "claude", Command: []string{"/nonexistent/claude"}},
}

// capEnv is a server over the fakes, with agents beside capAgents.
func capEnv(t *testing.T, mutate func(*config.Config), log *slog.Logger, claude, codex *fakeUsage, extra []catalog.Agent, opts ...Option) *testEnv {
	t.Helper()
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := newTestEnvAgents(t, mutate, log, append(slices.Clone(capAgents), extra...), append([]Option{capReaders(claude, codex)}, opts...)...)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	return e
}

func capReaders(claude, codex *fakeUsage) Option {
	return withUsageReaders(map[string]*agents.UsageReader{"claude": claude.reader(), "codex": codex.reader()})
}

// accountsRev is the accounts' rev as GET /api/accounts answers it.
func accountsRev(t *testing.T, e *testEnv) int64 {
	t.Helper()
	return int64(e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")["rev"].(float64))
}

// addAccount adds an account of adapter on a new folder: its id.
func addAccount(t *testing.T, e *testEnv, adapter, label string) string {
	t.Helper()
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": adapter, "label": label, "folder": realRoot(t, t.TempDir()), "expectedRev": accountsRev(t, e)}, http.StatusCreated, "")
	return out["account"].(map[string]any)["id"].(string)
}

// readNow asks a read of agent on account and answers the view.
func readNow(t *testing.T, e *testEnv, agent, account string, check bool) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/capacity/read", map[string]any{"agent": agent, "account": account, "check": check}, http.StatusAccepted, "")
}

func viewOfKey(t *testing.T, e *testEnv, query string) map[string]any {
	t.Helper()
	return e.expect(t, "GET", "/api/capacity?"+query, nil, http.StatusOK, "")
}

// capacityRoutes is every route of the row, as a switchyard is asked them.
var capacityRoutes = []struct{ method, path, body string }{
	{"GET", "/api/accounts", ""}, {"POST", "/api/accounts", `{"adapter":"claude","label":"x","folder":"/tmp","expectedRev":0}`},
	{"PATCH", "/api/accounts/a-aaaaaaaaaaaa", `{"label":"y","expectedRev":0}`}, {"POST", "/api/accounts/a-aaaaaaaaaaaa/retire", `{"expectedRev":0}`},
	{"DELETE", "/api/accounts/a-aaaaaaaaaaaa?expectedRev=0", ""}, {"PUT", "/api/accounts/defaults/claude", `{"account":"default","expectedRev":0}`},
	{"GET", "/api/capacity?agent=claude-a", ""}, {"GET", "/api/capacity", ""}, {"POST", "/api/capacity/read", `{"agent":"codex-a"}`},
}

// rowBodies are the row's routes by their guide's method and path, each
// asked as the admin would ask it with success: id a Claude Code account
// the document holds, at the accounts' rev rev, and a folder the server's
// user owns.
func rowBodies(t *testing.T, id string, rev int64) map[string]struct{ path, body string } {
	t.Helper()
	r := strconv.FormatInt(rev, 10)
	folder, _ := json.Marshal(realRoot(t, t.TempDir()))
	return map[string]struct{ path, body string }{
		"GET /api/accounts":                    {"/api/accounts", ""},
		"POST /api/accounts":                   {"/api/accounts", `{"adapter":"claude","label":"x","folder":` + string(folder) + `,"expectedRev":` + r + `}`},
		"PATCH /api/accounts/{id}":             {"/api/accounts/" + id, `{"label":"y","expectedRev":` + r + `}`},
		"POST /api/accounts/{id}/retire":       {"/api/accounts/" + id + "/retire", `{"expectedRev":` + r + `}`},
		"DELETE /api/accounts/{id}":            {"/api/accounts/" + id + "?expectedRev=" + r, ""},
		"PUT /api/accounts/defaults/{adapter}": {"/api/accounts/defaults/claude", `{"account":"` + id + `","expectedRev":` + r + `}`},
		"GET /api/capacity":                    {"/api/capacity?agent=claude-a&account=" + id, ""},
		"POST /api/capacity/read":              {"/api/capacity/read", `{"agent":"claude-a","account":"` + id + `"}`},
	}
}

// Every route is the admin's: a session's token, a link's, a host's, a
// Brakeman turn's on its own session and none, each asking with a body
// that would pass and a real account in the path, are refused 401, start no
// read and leave the accounts document byte for byte; a switchyard answers
// 403 switchyard.
func TestAccountsRoutesAdminOnly(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), append(bkAgents(), capAgents...), capReaders(claude, codex))
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	bk := bkEnvOf(t, e)
	id := addAccount(t, e, "claude", "Work")
	sid := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": "control"})
	link, _ := lo["token"].(string)
	if link == "" {
		t.Fatalf("no link: %v", lo)
	}
	bk.enable(t, "bk", nil)
	bsid, btok, _ := bk.turnSession(t, bk.briefing(t))
	routes := []struct{ method, path, body string }{{"GET", "/api/capacity", ""}, {"POST", "/api/capacity/read", `{"agent":"codex-a"}`}}
	for route, r := range rowBodies(t, id, accountsRev(t, e)) {
		method, _, _ := strings.Cut(route, " ")
		routes = append(routes, struct{ method, path, body string }{method, r.path, r.body})
	}
	before := accountsOnDisk(t, e)
	for name, tok := range map[string]string{"a session's token": e.agentToken(sid), "a link's": link, "a host's": "test-host-token", "none": "", "a Brakeman turn's": btok} {
		for _, r := range routes {
			status, data := 0, []byte(nil)
			if tok == btok {
				status, data, _ = e.callAsTurn(t, tok, bsid, r.method, r.path, []byte(r.body))
			} else {
				status, data, _ = e.callAs(t, tok, r.method, r.path, []byte(r.body))
			}
			if status != http.StatusUnauthorized {
				t.Errorf("%s on %s %s: %d %.200s", name, r.method, r.path, status, data)
			}
		}
	}
	if calls, _ := codex.count(); calls != 0 {
		t.Fatalf("a refused read was made: %d", calls)
	}
	if calls, _ := claude.count(); calls != 0 {
		t.Fatalf("a refused Claude Code read was made: %d", calls)
	}
	if after := accountsOnDisk(t, e); !bytes.Equal(before, after) {
		t.Fatalf("a refused write changed the accounts: %s", after)
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, r := range capacityRoutes {
		if status, data, out := yard.callAs(t, adminToken, r.method, r.path, []byte(r.body)); status != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Errorf("a switchyard's %s %s: %d %.200s", r.method, r.path, status, data)
		}
	}
}

// Each write: an add under a minted id (an id in the body refused), a
// rename (a folder refused), a retire, a default (a retired account
// refused), a remove; a stale rev refused every time.
func TestAccountsWrites(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	dir := realRoot(t, t.TempDir())
	e.expect(t, "POST", "/api/accounts", map[string]any{"id": "a-aaaaaaaaaaaa", "adapter": "claude", "label": "Work", "folder": dir, "expectedRev": 0}, http.StatusBadRequest, "invalid_account")
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Work", "folder": dir, "expectedRev": 3}, http.StatusConflict, "stale_rev")
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "goose", "label": "Work", "folder": dir, "expectedRev": 0}, http.StatusBadRequest, "invalid_account")
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Work", "folder": dir, "expectedRev": 0}, http.StatusCreated, "")
	id := out["account"].(map[string]any)["id"].(string)
	if !regexp.MustCompile(`^a-[a-z2-7]{12}$`).MatchString(id) || out["rev"].(float64) != 1 {
		t.Fatalf("the add: %v", out)
	}
	_, data, _ := e.callAs(t, adminToken, "PATCH", "/api/accounts/"+id, []byte(`{"label":"Home","folder":"/elsewhere","expectedRev":1}`))
	if !strings.Contains(string(data), `"field":"folder"`) {
		t.Fatalf("a folder in a rename: %s", data)
	}
	e.expect(t, "PATCH", "/api/accounts/"+id, map[string]any{"label": "Home", "expectedRev": 0}, http.StatusConflict, "stale_rev")
	if got := e.expect(t, "PATCH", "/api/accounts/"+id, map[string]any{"label": "Home", "expectedRev": 1}, http.StatusOK, ""); got["account"].(map[string]any)["folder"] != dir {
		t.Fatalf("the rename: %v", got)
	}
	e.expect(t, "PUT", "/api/accounts/defaults/claude", map[string]any{"account": id, "expectedRev": 2}, http.StatusOK, "")
	if v := viewOfKey(t, e, "agent=claude-a"); v["account"].(map[string]any)["id"] != id {
		t.Fatalf("the default's view: %v", v)
	}
	e.expect(t, "POST", "/api/accounts/"+id+"/retire", map[string]any{"expectedRev": 3}, http.StatusOK, "")
	e.expect(t, "PUT", "/api/accounts/defaults/claude", map[string]any{"account": id, "expectedRev": 4}, http.StatusBadRequest, "invalid_account")
	e.expect(t, "PUT", "/api/accounts/defaults/goose", map[string]any{"account": "default", "expectedRev": 4}, http.StatusBadRequest, "invalid_account")
	list := e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")
	if list["owner"] != true || len(list["accounts"].([]any)) != 1 || list["adapters"].([]any)[0].(map[string]any)["default"] != "default" {
		t.Fatalf("the list: %v", list)
	}
	e.expect(t, "DELETE", "/api/accounts/"+id+"?expectedRev=3", nil, http.StatusConflict, "stale_rev")
	if status, data, _ := e.callAs(t, adminToken, "DELETE", "/api/accounts/"+id+"?expectedRev=4", nil); status != http.StatusNoContent {
		t.Fatalf("the remove: %d %s", status, data)
	}
	e.expect(t, "GET", "/api/capacity?agent=claude-a&account="+id, nil, http.StatusBadRequest, "invalid_account")
}

// Two servers over one data directory: the second does not own capacity/,
// says so, refuses every write and starts no Claude Code read; the
// first's write is the one kept.
func TestAccountsNotOwner(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	second := serveSecond(t, e, claude, codex)
	got := second.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")
	if got["owner"] != false || !strings.Contains(got["why"].(string), "owner.lock") {
		t.Fatalf("the second's accounts: %v", got)
	}
	second.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Theirs", "folder": realRoot(t, t.TempDir()), "expectedRev": 0}, http.StatusConflict, capacity.CodeNotOwner)
	id := addAccount(t, e, "claude", "Ours")
	got = second.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")
	if as := got["accounts"].([]any); len(as) != 1 || as[0].(map[string]any)["id"] != id || got["rev"].(float64) != 1 {
		t.Fatalf("the second reads: %v", got)
	}
	if v := readNow(t, second, "claude-a", "", false); v["state"] != capacity.StateNotOwner {
		t.Fatalf("the second's Claude Code read: %v", v)
	}
	if calls, _ := claude.count(); calls != 0 {
		t.Fatalf("the second read Claude Code: %d", calls)
	}
}

// serveSecond serves a second server over e's store, with the same
// readers; it ends before e (cleanups run last in, first out).
func serveSecond(t *testing.T, e *testEnv, claude, codex *fakeUsage) *testEnv {
	t.Helper()
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store, capReaders(claude, codex))
	if err != nil {
		t.Fatal(err)
	}
	second := e.serve(srv)
	second.srv.limiter = newRateLimiter(1e6, 1e6)
	return second
}

// No GET reads: the view, the listing and the accounts answer what is
// kept, and only POST /api/capacity/read starts a read; a second inside
// MinInterval answers the kept reading with nextReadAfter.
func TestCapacityGetStartsNoRead(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	viewOfKey(t, e, "agent=claude-a")
	viewOfKey(t, e, "agent=codex-a&launch=managed")
	viewOfKey(t, e, "limit=2")
	e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")
	if a, b := claude.count(); a != 0 || len(b) != 0 {
		t.Fatalf("a GET read: %d %v", a, b)
	}
	if c, _ := codex.count(); c != 0 {
		t.Fatalf("a GET read Codex: %d", c)
	}
	if v := readNow(t, e, "codex-a", "", false); v["state"] != "ok" || v["fresh"] != "fresh" {
		t.Fatalf("the read: %v", v)
	}
	if c, _ := codex.count(); c != 1 {
		t.Fatalf("the read was made %d times", c)
	}
}

func TestCapacityMinInterval(t *testing.T) {
	codex := newFakeUsage("codex")
	e := capEnv(t, nil, nil, newFakeUsage("claude"), codex, nil)
	readNow(t, e, "codex-a", "", false)
	again := readNow(t, e, "codex-a", "", false)
	if c, _ := codex.count(); c != 1 || again["nextReadAfter"] == nil || again["state"] != "ok" {
		t.Fatalf("a second read inside MinInterval: %d reads, %v", c, again)
	}
}

// The views of states no read gives, each with its words, and a reading
// made with another environment than a read would be now is stale.
func TestCapacityViewStates(t *testing.T) {
	yolo := catalog.Agent{ID: "claude-yolo", Name: "Claude Yolo", Adapter: "claude", Command: []string{"/bin/cat"}, Yolo: &catalog.Yolo{Env: map[string]string{"ANTHROPIC_API_KEY": "sk-invented"}}}
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), []catalog.Agent{yolo})
	for query, state := range map[string]string{
		"agent=cat":                    capacity.StateUnsupported,
		"agent=ghost":                  capacity.StateNotInstalled,
		"agent=claude-a":               capacity.StateNeverRead,
		"agent=claude-a&launch=remote": capacity.StateRemote,
		"agent=claude-yolo&yolo=1":     capacity.StateEnvSet,
		"agent=claude-yolo":            capacity.StateNeverRead,
	} {
		v := viewOfKey(t, e, query)
		if v["state"] != state || v["words"] == "" || v["windows"] == nil {
			t.Errorf("%s: %v", query, v)
		}
		if b, _ := json.Marshal(v); strings.Contains(string(b), "sk-invented") {
			t.Errorf("%s: the view carries a key's value", query)
		}
	}
	if v := readNow(t, e, "codex-a", "", false); v["fresh"] != capacity.FreshNow {
		t.Fatalf("the read: %v", v)
	}
	cat := e.srv.Catalog()
	a, _ := cat.Get("codex-a")
	a.Env = map[string]string{"CODEX_MARK": "changed"}
	changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: replaceAgent(cat.List(), a)})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(changed)
	if v := viewOfKey(t, e, "agent=codex-a"); v["fresh"] != capacity.FreshStale || v["words"] != capacity.EnvWords {
		t.Fatalf("a changed environment: %v", v)
	}
}

func replaceAgent(list []catalog.Agent, a catalog.Agent) []catalog.Agent {
	for i := range list {
		if list[i].ID == a.ID {
			list[i] = a
		}
	}
	return list
}

// Where a launch would run: a session and a legacy member natively, with
// the server's name; a managed run on the agent's own login undetermined
// once a Runtime is registered, native with none; a managed run of an
// entry pinned to a folder native with one.
func TestCapacityHostByLaunch(t *testing.T) {
	pinned := catalog.Agent{ID: "claude-pinned", Name: "Pinned", Adapter: "claude", Command: []string{"/bin/cat"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": "/srv/work"}}
	host, _ := os.Hostname()
	plain := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), []catalog.Agent{pinned})
	box := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), []catalog.Agent{pinned}, withRuntime(&sessiontest.FakeRuntime{Place: "boxcar", Base: local.Native()}))
	for _, c := range []struct {
		e           *testEnv
		query, kind string
	}{
		{plain, "agent=claude-a", capacity.HostNative}, {plain, "agent=claude-a&launch=legacy", capacity.HostNative},
		{plain, "agent=claude-a&launch=managed", capacity.HostNative}, {box, "agent=claude-a&launch=session", capacity.HostNative},
		{box, "agent=claude-a&launch=managed", capacity.HostUndetermined}, {box, "agent=claude-pinned&launch=managed", capacity.HostNative},
		{box, "agent=claude-a&launch=remote", capacity.HostRemote},
	} {
		h := viewOfKey(t, c.e, c.query)["host"].(map[string]any)
		if h["kind"] != c.kind || (c.kind == capacity.HostNative && h["name"] != host) || (c.kind == capacity.HostUndetermined && !strings.Contains(h["words"].(string), "its usage there is unknown")) {
			t.Errorf("%s: %v", c.query, h)
		}
	}
	if v := viewOfKey(t, box, "agent=claude-pinned"); v["state"] != capacity.StateNeverRead {
		t.Fatalf("the pinned entry's default: %v", v)
	}
	id := addAccount(t, box, "claude", "Work")
	if v := viewOfKey(t, box, "agent=claude-pinned&account="+id); v["state"] != capacity.StateEnvSet {
		t.Fatalf("another account of a pinned entry: %v", v)
	}
}

// A read runs with the environment a session of the agent gets: a
// server whose passthrough names CLAUDE_CONFIG_DIR and https_proxy hands
// both to the reader as to the session, so the default account reads the
// folder the session uses.
func TestUsageEnvMatchesLaunch(t *testing.T) {
	folder, proxy := realRoot(t, t.TempDir()), "http://proxy.invalid:3128"
	t.Setenv("CLAUDE_CONFIG_DIR", folder)
	t.Setenv("https_proxy", proxy)
	out := filepath.Join(t.TempDir(), "env")
	agent := catalog.Agent{ID: "claude-env", Name: "Claude env", Adapter: "claude", Command: []string{"/bin/sh", "-c", `env > "$0.tmp" && mv "$0.tmp" "$0"; exec cat`, out}}
	claude := newFakeUsage("claude")
	e := capEnv(t, func(c *config.Config) { c.EnvPassthrough = []string{"CLAUDE_CONFIG_DIR", "https_proxy"} }, nil, claude, newFakeUsage("codex"), []catalog.Agent{agent})
	e.createSession("claude-env")
	var sessionEnv []string
	until(t, "the session's environment written", func() bool {
		b, err := os.ReadFile(out)
		sessionEnv = strings.Split(string(b), "\n")
		return err == nil
	})
	readNow(t, e, "claude-env", "", false)
	claude.mu.Lock()
	readerEnv := claude.env
	claude.mu.Unlock()
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "https_proxy"} {
		s, r := lookupLine(sessionEnv, k), lookupLine(readerEnv, k)
		if s == "" || s != r {
			t.Errorf("%s: the session's %q, the reader's %q", k, s, r)
		}
	}
	if got := lookupLine(readerEnv, "CLAUDE_CONFIG_DIR"); got != folder {
		t.Fatalf("the reader reads %q, not the session's folder", got)
	}
}

func lookupLine(env []string, k string) string {
	v := ""
	for _, kv := range env {
		if key, val, ok := strings.Cut(kv, "="); ok && key == k {
			v = val
		}
	}
	return v
}

// A reading kept sends the capacity event, and the view read on it is the
// reading: the reader answers only once the test opens its gate.
func TestCapacityReadThenEvent(t *testing.T) {
	codex := newFakeUsage("codex")
	gate, called := make(chan struct{}), make(chan struct{}, 1)
	codex.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
	e := capEnv(t, nil, nil, newFakeUsage("claude"), codex, nil)
	events := e.sse(t)
	done := make(chan map[string]any, 1)
	go func() {
		_, _, out := e.callAs(t, adminToken, "POST", "/api/capacity/read", []byte(`{"agent":"codex-a"}`))
		done <- out
	}()
	<-called
	if v := viewOfKey(t, e, "agent=codex-a"); v["state"] != capacity.StateReading {
		t.Fatalf("while the read runs: %v", v)
	}
	close(gate)
	var ev string
	for ev == "" {
		select {
		case s := <-events:
			if strings.HasPrefix(s, "capacity ") {
				ev = strings.TrimPrefix(s, "capacity ")
			}
		case <-t.Context().Done():
			t.Fatal("no capacity event")
		}
	}
	var got capacityEvent
	if err := json.Unmarshal([]byte(ev), &got); err != nil || got.Agent != "codex-a" || got.Account != capacity.DefaultID || got.State != "ok" || got.ReadAt.IsZero() {
		t.Fatalf("the event: %s %v", ev, err)
	}
	if v := viewOfKey(t, e, "agent=codex-a"); v["state"] != "ok" || v["readAt"] == nil {
		t.Fatalf("the view on the event: %v", v)
	}
	<-done
}

// The event at its largest fits its bound, and one past it is dropped,
// never cut.
func TestCapacityEventBounded(t *testing.T) {
	at := time.Date(2026, 11, 10, 23, 59, 59, 999999999, time.FixedZone("x", 14*3600))
	k := capacity.Key{Agent: strings.Repeat("a", 32), Account: "a-abcdefghijkl"}
	b, err := json.Marshal(capacityEvent{Agent: k.Agent, Account: k.Account, State: capacity.StateUnqualified, ReadAt: at})
	if err != nil || len(b) > maxCapacityEvent {
		t.Fatalf("the largest event is %d bytes: %v", len(b), err)
	}
	h := newEventHub()
	ch := h.subscribe()
	h.capacity(capacity.Key{Agent: strings.Repeat("a", maxCapacityEvent), Account: k.Account}, "ok", at)
	h.capacity(k, capacity.StateUnqualified, at)
	if got := string(<-ch); got != "event: capacity\ndata: "+string(b)+"\n\n" {
		t.Fatalf("the event sent: %q", got)
	}
	select {
	case more := <-ch:
		t.Fatalf("an event past the bound was sent: %q", more)
	default:
	}
}

// The replies at their largest fit their bounds: the accounts with every
// account the document holds and a reading of each, and one view with the
// most windows and credits and the longest strings a reading keeps.
func TestCapacityRepliesBounded(t *testing.T) {
	claude := newFakeUsage("claude")
	long := strings.Repeat("<", capacity.MaxLabel)
	claude.set(func(f *fakeUsage) { f.report = largestReport() })
	e := capEnv(t, nil, nil, claude, newFakeUsage("codex"), nil)
	base := realRoot(t, t.TempDir())
	var ids []string
	for i := range capacity.MaxAccounts {
		// The folder as long as the system makes one, up to ten names of
		// 200 bytes (about 2,000, within the product's MaxFolder): macOS
		// refuses a path past 1,024 bytes, Linux one past 4,096.
		dir := filepath.Join(base, fmt.Sprintf("%02d", i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, c := range "fghijklmno" {
			next := filepath.Join(dir, strings.Repeat(string(c), 200))
			if len(next) > capacity.MaxFolder || os.Mkdir(next, 0o700) != nil {
				break
			}
			dir = next
		}
		if dir == filepath.Join(base, fmt.Sprintf("%02d", i)) {
			t.Fatalf("no folder of a 200-byte name could be made in %s", dir)
		}
		status, data, out := e.callAs(t, adminToken, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": long, "folder": dir, "expectedRev": accountsRev(t, e)})
		if status != http.StatusCreated {
			if !strings.Contains(string(data), "would pass") && !strings.Contains(string(data), "at most") {
				t.Fatalf("add %d: %d %s", i, status, data)
			}
			break
		}
		ids = append(ids, out["account"].(map[string]any)["id"].(string))
	}
	for _, id := range append(ids, capacity.DefaultID) {
		readNow(t, e, "claude-a", id, false)
	}
	_, data, _ := e.callAs(t, adminToken, "GET", "/api/accounts", nil)
	if len(data) > maxAccountsReply || !strings.Contains(string(data), `"reading"`) {
		t.Fatalf("the accounts' reply is %d bytes (%d accounts)", len(data), len(ids))
	}
	_, data, out := e.callAs(t, adminToken, "GET", "/api/capacity?agent=claude-a&account="+ids[0], nil)
	if len(data) > maxCapacityView || len(out["windows"].([]any)) != capacity.MaxWindows {
		t.Fatalf("the view is %d bytes: %.300s", len(data), data)
	}
}

func largestReport() agents.UsageReport {
	rep := agents.UsageReport{State: agents.UsageOK, Plan: strings.Repeat("p", capacity.MaxPlan)}
	fig := strings.Repeat("9", capacity.MaxFigure)
	on := true
	for i := range capacity.MaxWindows {
		at := time.Now().Add(7*24*time.Hour + time.Duration(i)).UTC()
		rep.Windows = append(rep.Windows, agents.UsageWindow{Kind: strings.Repeat("k", capacity.MaxShort), Name: strings.Repeat("n", capacity.MaxName), Minutes: 10080, Percent: 100, ResetsAt: &at, Severity: "critical", Active: &on, Limit: strings.Repeat("l", capacity.MaxShort)})
	}
	for range capacity.MaxCredits {
		rep.Credits = append(rep.Credits, agents.UsageCredit{Kind: "credits", Enabled: &on, Used: &fig, Limit: &fig, Balance: &fig, Unlimited: true, Currency: "USD"})
	}
	return rep
}

// The listing at its most: the most accounts over several catalog
// entries, each view with the most windows: every page within its bound,
// and next walks every view once.
func TestCapacityListingPaged(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	claude.set(func(f *fakeUsage) { f.report = largestReport() })
	codex.set(func(f *fakeUsage) { f.report = largestReport() })
	var extra []catalog.Agent
	for i := range 2 {
		extra = append(extra, catalog.Agent{ID: fmt.Sprintf("claude-%d", i), Name: "Claude", Adapter: "claude", Command: []string{"/bin/cat"}}, catalog.Agent{ID: fmt.Sprintf("codex-%d", i), Name: "Codex", Adapter: "codex", Command: []string{"/bin/cat"}})
	}
	e := capEnv(t, nil, nil, claude, codex, extra)
	want := map[string]bool{}
	for i := range capacity.MaxAccounts {
		adapter := "claude"
		if i%4 == 0 {
			adapter = "codex"
		}
		addAccount(t, e, adapter, fmt.Sprintf("%02d", i)+strings.Repeat("é", capacity.MaxLabel-2))
	}
	doc, _ := e.srv.capacity.st.Accounts()
	for _, a := range e.srv.Catalog().List() {
		if a.ID == "ghost" {
			continue
		}
		want[a.ID+"/default"] = true
		for _, acct := range doc.Accounts {
			if acct.Adapter == a.Adapter && a.Adapter != "" {
				want[a.ID+"/"+acct.ID] = true
			}
		}
	}
	if len(want) > capacity.MaxKeys {
		t.Fatalf("%d keys: more than the cache keeps", len(want))
	}
	for k := range want {
		agent, account, _ := strings.Cut(k, "/")
		if a, _ := e.srv.Catalog().Get(agent); a.Adapter != "" {
			readNow(t, e, agent, account, false)
		}
	}
	seen, after, pages := map[string]int{}, "", 0
	for {
		_, data, out := e.callAs(t, adminToken, "GET", "/api/capacity?after="+after, nil)
		if len(data) > maxCapacityPage {
			t.Fatalf("a page of %d bytes", len(data))
		}
		pages++
		views := out["views"].([]any)
		if len(views) == 0 || len(views) > maxCapacityLimit {
			t.Fatalf("a page of %d views", len(views))
		}
		for _, raw := range views {
			v := raw.(map[string]any)
			seen[v["agent"].(map[string]any)["id"].(string)+"/"+v["account"].(map[string]any)["id"].(string)]++
		}
		next, _ := out["next"].(string)
		if next == "" {
			break
		}
		after = next
	}
	for k := range want {
		if seen[k] != 1 {
			t.Errorf("%s seen %d times", k, seen[k])
		}
	}
	if len(seen) != len(want) || pages < 2 {
		t.Fatalf("%d views in %d pages, want %d", len(seen), pages, len(want))
	}
}

// The guide lists the routes, the admin's, with no MCP tool, so the main
// tools list is unchanged.
func TestGuideListsCapacityRoutes(t *testing.T) {
	want := map[string]string{"accounts_list": "GET /api/accounts", "accounts_add": "POST /api/accounts", "accounts_rename": "PATCH /api/accounts/{id}",
		"accounts_retire": "POST /api/accounts/{id}/retire", "accounts_remove": "DELETE /api/accounts/{id}", "accounts_default": "PUT /api/accounts/defaults/{adapter}",
		"capacity_view": "GET /api/capacity", "capacity_read": "POST /api/capacity/read"}
	for _, op := range Operations() {
		if route, ok := want[op.Name]; ok {
			if op.Method+" "+op.Path != route || op.Scope != "admin" || op.MCP != "" {
				t.Errorf("%s: %s %s %s %q", op.Name, op.Method, op.Path, op.Scope, op.MCP)
			}
			delete(want, op.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("not in the guide: %v", want)
	}
}

// The engine never reads capacity: no file of internal/crew or its ledger
// imports internal/capacity.
func TestEngineImportsNoCapacity(t *testing.T) {
	for _, dir := range []string{"../crew", "../crew/ledger"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range files {
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); strings.HasSuffix(p, "/internal/capacity") {
					t.Errorf("%s imports %s", f, p)
				}
			}
		}
	}
}

// versioned is a Claude Code entry whose fake program answers version v.
func versioned(id, v string) catalog.Agent {
	return catalog.Agent{ID: id, Name: id, Adapter: "claude", Command: []string{"/bin/cat", v}}
}

// restartOver shuts e's server down and serves another over its store,
// with the same readers.
func restartOver(t *testing.T, e *testEnv, claude, codex *fakeUsage) *testEnv {
	t.Helper()
	shutdown(e)
	return serveSecond(t, e, claude, codex)
}

// A check whose run took a model turn records the version turned, on
// disk: no read and no check of it starts the program again, on this
// server or the next over the same data directory.
func TestCapacityTurnedSurvivesRestart(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	claude.set(func(f *fakeUsage) { f.outcome = agents.Turned })
	e := capEnv(t, nil, nil, claude, codex, []catalog.Agent{versioned("claude-new", "2.1.0")})
	a, b := addAccount(t, e, "claude", "A"), addAccount(t, e, "claude", "B")
	if v := readNow(t, e, "claude-new", "", false); v["state"] != capacity.StateUnqualified {
		t.Fatalf("a newer version without a check: %v", v)
	}
	if _, s := claude.count(); s["2.1.0"] != 0 {
		t.Fatalf("started without a check: %v", s)
	}
	readNow(t, e, "claude-new", a, true)
	if _, s := claude.count(); s["2.1.0"] != 1 || e.srv.capacity.st.Versions().State("claude", "2.1.0") != capacity.VersionTurned {
		t.Fatalf("the check: %v, recorded %q", s, e.srv.capacity.st.Versions().State("claude", "2.1.0"))
	}
	readNow(t, e, "claude-new", b, true)
	next := restartOver(t, e, claude, codex)
	readNow(t, next, "claude-new", "", false)
	readNow(t, next, "claude-new", a, true)
	if _, s := claude.count(); s["2.1.0"] != 1 {
		t.Fatalf("a turned version was started again: %v", s)
	}
}

// Checks of three new versions that end without an answer (the reader's
// Inconclusive: killed at its timeout, its output ended before the result
// line, past its bound) record each interrupted: the dialog's next read of
// each starts nothing, on this server and the next.
func TestCapacityInconclusiveCheckSurvivesRestart(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	claude.set(func(f *fakeUsage) { f.outcome = agents.Inconclusive })
	vs := []string{"2.1.1", "2.1.2", "2.1.3"}
	var extra []catalog.Agent
	for i, v := range vs {
		extra = append(extra, versioned(fmt.Sprintf("claude-v%d", i), v))
	}
	e := capEnv(t, nil, nil, claude, codex, extra)
	a, b := addAccount(t, e, "claude", "A"), addAccount(t, e, "claude", "B")
	for i, v := range vs {
		id := fmt.Sprintf("claude-v%d", i)
		if got := readNow(t, e, id, "", true); got["state"] != capacity.StateError {
			t.Fatalf("%s's check: %v", v, got)
		}
		if st := e.srv.capacity.st.Versions().State("claude", v); st != capacity.VersionInterrupted {
			t.Fatalf("%s recorded %q", v, st)
		}
		if got := readNow(t, e, id, a, false); got["state"] != capacity.StateUnqualified || !strings.Contains(got["words"].(string), "check it again") {
			t.Fatalf("%s's next read: %v", v, got)
		}
	}
	next := restartOver(t, e, claude, codex)
	for i := range vs {
		readNow(t, next, fmt.Sprintf("claude-v%d", i), b, false)
	}
	if _, s := claude.count(); s["2.1.1"] != 1 || s["2.1.2"] != 1 || s["2.1.3"] != 1 {
		t.Fatalf("an interrupted version was started again: %v", s)
	}
}

// Two servers over one data directory: only the owner starts Claude Code,
// a check on the other is refused 409 accounts_not_owner, a Codex read is
// made there; the owner's read of a checked version that now turns records
// it turned, and neither server, nor a third after both, starts it again.
func TestCapacityCheckNotOwner(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, []catalog.Agent{versioned("claude-new", "2.1.5")})
	a := addAccount(t, e, "claude", "A")
	readNow(t, e, "claude-new", "", true)
	if st := e.srv.capacity.st.Versions().State("claude", "2.1.5"); st != capacity.VersionChecked {
		t.Fatalf("the check: %q", st)
	}
	claude.set(func(f *fakeUsage) { f.outcome = agents.Turned })
	second := serveSecond(t, e, claude, codex)
	calls, _ := claude.count()
	for _, agent := range []string{"claude-new", "claude-a"} {
		if v := readNow(t, second, agent, a, false); v["state"] != capacity.StateNotOwner {
			t.Fatalf("the second's read of %s: %v", agent, v)
		}
	}
	second.expect(t, "POST", "/api/capacity/read", map[string]any{"agent": "claude-new", "account": a, "check": true}, http.StatusConflict, capacity.CodeNotOwner)
	if now, _ := claude.count(); now != calls {
		t.Fatalf("the second started Claude Code: %d reads, then %d", calls, now)
	}
	if v := readNow(t, second, "codex-a", "", false); v["state"] != "ok" {
		t.Fatalf("the second's Codex read: %v", v)
	}
	readNow(t, e, "claude-new", a, false)
	if st := e.srv.capacity.st.Versions().State("claude", "2.1.5"); st != capacity.VersionTurned {
		t.Fatalf("the turn: %q", st)
	}
	shutdown(second)
	shutdown(e)
	third := serveSecond(t, e, claude, codex)
	if owned, why := third.srv.capacity.ownership(); !owned {
		t.Fatalf("the third does not own capacity/: %s", why)
	}
	readNow(t, third, "claude-new", "", false)
	readNow(t, third, "claude-new", a, true)
	if _, s := claude.count(); s["2.1.5"] != 2 {
		t.Fatalf("a turned version was started again: %v", s)
	}
}

// What an account's view holds stays the admin's: a planted label, folder
// and figure reach no route a session's token, a link's or a host's
// reaches with the cat session's id, nor one a Brakeman turn reaches with
// its own session's id (its read tools' routes among them), whatever the
// method; no such call starts a read, a session or a write of the
// accounts; and no line of the server's log carries them, an add the
// folder check refuses and a read that ends in an error included.
func TestCapacityStaysWithTheAdmin(t *testing.T) {
	const label, figure = "Zebra Label 7731", "Quokka window 7731"
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	claude.set(func(f *fakeUsage) { f.report.Windows[0].Name = figure })
	var logged bytes.Buffer
	var logMu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{&logged, &logMu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	off := false
	walker := catalog.Agent{ID: "claude-w", Name: "Claude W", Adapter: "claude", Command: []string{"/bin/sh", "-c", "exec /bin/cat"}, Probe: &off}
	bkKey := bkAgents()[0]
	bkKey.ID, bkKey.Name, bkKey.Env = "bk-key", "bk-key", map[string]string{"ANTHROPIC_API_KEY": "made-up-key-7731"}
	e := newTestEnvAgents(t, nil, log, append(append(bkAgents(), capAgents...), walker, bkKey), capReaders(claude, codex))
	bk := bkEnvOf(t, e)
	id := addAccount(t, e, "claude", label)
	folderOf := func(id string) string {
		doc, _ := e.srv.capacity.st.Accounts()
		for _, a := range doc.Accounts {
			if a.ID == id {
				return a.Folder
			}
		}
		t.Fatalf("no account %s", id)
		return ""
	}
	folder := folderOf(id)
	// An account on the tool's own folder in the server's home, which the
	// stubs run under too: the skill is installed in that home at the first
	// launch, and no line of the log names the folder (#856).
	t.Setenv("HOME", e.srv.home)
	homeFolder := filepath.Join(e.srv.home, ".claude")
	if err := os.MkdirAll(homeFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": label + " home", "folder": homeFolder, "expectedRev": accountsRev(t, e)}, http.StatusCreated, "")
	// A launch whose entry pins another account's folder, naming none, and
	// a Brakeman turn on a third's folder beside a key its entry sets: each
	// bound pinned, each the first launch in its folder, so the skill is
	// installed there, and no line of the log names the folder.
	pinnedFolder, bkFolder := folderOf(addAccount(t, e, "claude", label+" pinned")), folderOf(addAccount(t, e, "claude", label+" brakeman"))
	pinnedEntry := catalog.Agent{ID: "claude-in", Name: "Claude In", Adapter: "claude", Command: []string{"/bin/sh", "-c", "exec /bin/cat"}, Probe: &off, Env: map[string]string{"CLAUDE_CONFIG_DIR": pinnedFolder}}
	withPinned, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append(e.srv.Catalog().List(), pinnedEntry)})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(withPinned)
	pinnedSid := e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-in"}, http.StatusCreated, "")["id"].(string)
	t.Cleanup(func() { e.stopAndWait(pinnedSid) })
	if pl, _ := e.srv.lookupLocal(pinnedSid); pl.Launched().Account.Source != session.SourcePinned || pl.Launched().Account.Folder != pinnedFolder {
		t.Fatalf("the pinned launch's binding: %+v", pl.Launched().Account)
	}
	// A session and a run's member bound to the account (#784): what
	// reaches the walk's principals of them is the account's id and
	// source, never its label, its folder or its fingerprint.
	boundSid := e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-w", "account": id}, http.StatusCreated, "")["id"].(string)
	t.Cleanup(func() { e.stopAndWait(boundSid) })
	boundRun := e.launchCrew(t, "Bound", map[string]any{"name": "lead", "agentId": "claude-w", "prompt": "", "account": id, "start": map[string]any{"when": "immediately"}})
	memberSid := e.waitRunning(t, boundRun, "lead")
	t.Cleanup(func() { e.stopAndWait(memberSid) })
	bl, _ := e.srv.lookupLocal(boundSid)
	fingerprint := bl.Launched().Account.Login
	if v := readNow(t, e, "claude-a", id, false); !strings.Contains(fmt.Sprint(v), figure) {
		t.Fatalf("the admin's view: %v", v)
	}
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": label + " again", "folder": folder + "-gone", "expectedRev": accountsRev(t, e)}, http.StatusBadRequest, capacity.CodeInvalidAccount)
	failing := addAccount(t, e, "claude", label+" failing")
	claude.set(func(f *fakeUsage) {
		f.report = agents.UsageReport{State: agents.UsageError, Words: "the read failed: " + figure}
	})
	if v := readNow(t, e, "claude-a", failing, false); v["state"] != capacity.StateError {
		t.Fatalf("the failing read: %v", v)
	}
	sid := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": "control"})
	link, _ := lo["token"].(string)
	bk.enable(t, "bk-key", func(c map[string]any) { c["account"] = bkFolder })
	bsid, btok, bl := bk.turnSession(t, bk.briefing(t))
	if b := bl.Launched().Account; b.Source != session.SourcePinned || b.Folder != bkFolder {
		t.Fatalf("the Brakeman turn's binding: %+v", b)
	}
	wild := regexp.MustCompile(`\{[a-z]+\}`)
	_, blo := e.do("POST", "/api/sessions/"+boundSid+"/links", adminToken, map[string]any{"role": "control"})
	boundLink, _ := blo["token"].(string)
	leaked := func(who, path string, data []byte) {
		for _, s := range []string{label, folder, pinnedFolder, bkFolder, homeFolder, figure, fingerprint} {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s's %s carries %q", who, path, s)
			}
		}
	}
	accountsFile := filepath.Join(e.srv.store.Dir(), capacity.Dir, capacity.AccountsFile)
	before, _ := os.ReadFile(accountsFile)
	reads, _ := claude.count()
	codexReads, _ := codex.count()
	sessions := len(e.srv.registry.List())
	// The row's own routes are asked as the admin would ask them with
	// success, a real account in the path; every other with {} for a body.
	row := rowBodies(t, id, accountsRev(t, e))
	for _, op := range Operations() {
		for _, p := range []struct{ who, tok, sid string }{{"a session", e.agentToken(sid), sid}, {"a link", link, sid}, {"a host", "test-host-token", sid}, {"a Brakeman turn", btok, bsid},
			{"a bound session", e.agentToken(boundSid), boundSid}, {"a bound session's link", boundLink, boundSid}, {"a bound run's member", e.agentToken(memberSid), memberSid}} {
			path := wild.ReplaceAllStringFunc(op.Path, func(w string) string {
				switch w {
				case "{id}":
					return p.sid
				case "{run}":
					return boundRun
				}
				return "x"
			})
			var body []byte
			if op.Method != http.MethodGet {
				body = []byte(`{}`)
			}
			r, ours := row[op.Method+" "+op.Path]
			if ours {
				path, body = r.path, []byte(r.body)
			}
			var status int
			var data []byte
			if p.sid == bsid {
				status, data, _ = e.callAsTurn(t, p.tok, bsid, op.Method, path, body)
			} else {
				status, data, _ = e.callAs(t, p.tok, op.Method, path, body)
			}
			if ours && status != http.StatusUnauthorized {
				t.Errorf("%s's %s %s: %d %.200s", p.who, op.Method, path, status, data)
			}
			leaked(p.who, op.Method+" "+path, data)
		}
	}
	if after, _ := os.ReadFile(accountsFile); !bytes.Equal(before, after) {
		t.Errorf("the walk wrote the accounts: %s", after)
	}
	now, _ := claude.count()
	codexNow, _ := codex.count()
	if now != reads || codexNow != codexReads || len(e.srv.registry.List()) != sessions {
		t.Errorf("the walk started something: reads %d then %d, %d then %d; sessions %d then %d", reads, now, codexReads, codexNow, sessions, len(e.srv.registry.List()))
	}
	// The accounts made unreadable and readable again around a file
	// request each: the deny list's two lines are logged, and pass too.
	writeFile(t, accountsFile, []byte("not json"))
	e.getFile(sid, link, "notes.md", "raw")
	writeFile(t, accountsFile, before)
	e.getFile(sid, link, "notes.md", "raw")
	// The home's skill a file of the user's own, then one that cannot be
	// read, each at a launch the server installs it again at, and the
	// integration's install failing on it: no line names a path under the
	// account's folder.
	ad, _ := agents.Get("claude")
	skill := filepath.Join(e.srv.home, filepath.FromSlash(ad.SkillPath))
	launchAgain := func() {
		e.srv.skillMu.Lock()
		delete(e.srv.skillDone, "claude")
		e.srv.skillMu.Unlock()
		e.stopAndWait(e.expect(t, "POST", "/api/sessions", map[string]any{"agentId": "claude-w"}, http.StatusCreated, "")["id"].(string))
	}
	writeFile(t, skill, []byte("a skill of the user's own\n"))
	launchAgain()
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	launchAgain()
	e.expect(t, "POST", "/api/integrations/claude/install", nil, http.StatusInternalServerError, "install_failed")
	logMu.Lock()
	defer logMu.Unlock()
	for _, words := range []string{denyAllWords, denyEndWords} {
		if !bytes.Contains(logged.Bytes(), []byte(words)) {
			t.Errorf("the log lacks %q", words)
		}
	}
	if n := bytes.Count(logged.Bytes(), []byte("skill installed in the agent's configuration directory")); n < 2 {
		t.Errorf("the skill's line was written %d times: not for the pinned launch and the turn", n)
	}
	for _, line := range []string{`msg="skill installed" adapter=claude`, `holds a SKILL.md of the user's own, which stays; conductor skill prints Conductor's" adapter=claude`,
		`msg="skill install failed" adapter=claude`, `msg="integration install failed" integration=claude`} {
		if !bytes.Contains(logged.Bytes(), []byte(line)) {
			t.Errorf("the log lacks %s", line)
		}
	}
	leaked("the log", "", logged.Bytes())
}

type lockedWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// fastLockTick has a waiting server try for capacity/owner.lock every
// 10 ms for the test.
func fastLockTick(t *testing.T) {
	old := capacityLockTick
	capacityLockTick = 10 * time.Millisecond
	t.Cleanup(func() { capacityLockTick = old })
}

func owns(e *testEnv) func() bool {
	return func() bool { owned, _ := e.srv.capacity.ownership(); return owned }
}

// accountsOnDisk is capacity/accounts.json as it is.
func accountsOnDisk(t *testing.T, e *testEnv) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), capacity.Dir, capacity.AccountsFile))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

// A server that lets capacity/owner.lock go writes nothing after: a second
// server waits at the lock, the first closes its desk and says owner false,
// and each of its write routes answers 409 accounts_not_owner, the
// document byte for byte what it was.
func TestAccountsWritesEndWithTheLock(t *testing.T) {
	fastLockTick(t)
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	id := addAccount(t, e, "claude", "Kept")
	second := serveSecond(t, e, claude, codex)
	e.srv.capacity.close()
	until(t, "the second owns capacity/", owns(second))
	if got := e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, ""); got["owner"] != false {
		t.Fatalf("the first after its close: %v", got)
	}
	before := accountsOnDisk(t, e)
	for _, w := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Late", "folder": realRoot(t, t.TempDir()), "expectedRev": 1}},
		{"PATCH", "/api/accounts/" + id, map[string]any{"label": "Renamed", "expectedRev": 1}},
		{"POST", "/api/accounts/" + id + "/retire", map[string]any{"expectedRev": 1}},
		{"DELETE", "/api/accounts/" + id + "?expectedRev=1", nil},
		{"PUT", "/api/accounts/defaults/claude", map[string]any{"account": id, "expectedRev": 1}},
	} {
		if status, data, out := e.callAs(t, adminToken, w.method, w.path, w.body); status != http.StatusConflict || errorCode(out) != capacity.CodeNotOwner {
			t.Errorf("%s %s after the lock went: %d %.300s", w.method, w.path, status, data)
		}
	}
	if after := accountsOnDisk(t, e); !bytes.Equal(before, after) {
		t.Fatalf("the document changed: %s, then %s", before, after)
	}
}

// A write under way in the store when the desk closes ends before the
// lock goes: the close waits for it, the second server owns only after
// the close, the document holds that write once, and of the two writes
// naming one rev across the hand-over (the first's, held; the second's,
// once it owns) exactly one is taken.
func TestAnAccountWriteUnderWayEndsBeforeTheLockGoes(t *testing.T) {
	fastLockTick(t)
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	second := serveSecond(t, e, claude, codex)
	entered, gate, closing, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.capacity.seam = func() { once.Do(func() { close(entered); <-gate }) }
	e.srv.capacity.closing = func() { close(closing) }
	add := func(on *testEnv, label string) int {
		status, _, _ := on.callAs(t, adminToken, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": label, "folder": realRoot(t, t.TempDir()), "expectedRev": 0})
		return status
	}
	first := make(chan int, 1)
	go func() { first <- add(e, "First") }()
	<-entered
	go func() { e.srv.capacity.close(); close(closed) }()
	secondStatus := 0
	select {
	case <-closed:
		t.Errorf("the desk closed while a write was under way in the store")
		until(t, "the second owns capacity/", owns(second))
		secondStatus = add(second, "Second")
	case <-closing:
		lock, _, held, err := store.TakeOwnerLock(filepath.Join(e.srv.store.Dir(), capacity.Dir, "owner.lock"))
		if lock != nil {
			lock.Release()
		}
		if err != nil || !held || owns(second)() {
			t.Errorf("capacity/owner.lock went while a write was under way: held %t, %v", held, err)
		}
	}
	close(gate)
	firstStatus := <-first
	<-closed
	until(t, "the second owns capacity/", owns(second))
	if secondStatus == 0 {
		secondStatus = add(second, "Second")
	}
	if (firstStatus == http.StatusCreated) == (secondStatus == http.StatusCreated) {
		t.Errorf("of two writes naming rev 0, the first answered %d and the second %d", firstStatus, secondStatus)
	}
	labels := map[string]int{}
	for _, a := range second.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")["accounts"].([]any) {
		labels[a.(map[string]any)["label"].(string)]++
	}
	if (firstStatus == http.StatusCreated && labels["First"] != 1) || (secondStatus == http.StatusCreated && labels["Second"] != 1) || len(labels) != 1 {
		t.Errorf("the document holds %v, the writes answered %d and %d", labels, firstStatus, secondStatus)
	}
	if status := add(e, "After"); status != http.StatusConflict {
		t.Errorf("the first's write after its close: %d", status)
	}
}

// capacityEventOf matches the capacity event of agent on account.
func capacityEventOf(agent, account string) func(string) bool {
	return func(ev string) bool {
		var got capacityEvent
		data, ok := strings.CutPrefix(ev, "capacity ")
		return ok && json.Unmarshal([]byte(data), &got) == nil && got.Agent == agent && got.Account == account
	}
}

// awaitEvent reads events until match takes one, bounded by the test's own
// timeout.
func awaitEvent(t *testing.T, events <-chan string, match func(string) bool) {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if match(ev) {
				return
			}
		case <-t.Context().Done():
			t.Fatal("the event never came")
		}
	}
}

// readAndAwait reads agent on account and, when a read was made (the fake
// counted one), waits for its capacity event, which OnRead sends once it
// has noted the reading: the view the read answered.
func readAndAwait(t *testing.T, e *testEnv, f *fakeUsage, events <-chan string, agent, account string) map[string]any {
	t.Helper()
	before, _ := f.count()
	v := readNow(t, e, agent, account, false)
	if now, _ := f.count(); now != before {
		awaitEvent(t, events, capacityEventOf(agent, v["account"].(map[string]any)["id"].(string)))
	}
	return v
}

// listedView is the listing's view of agent on account for query, walked
// page by page; nil when no page holds it.
func listedView(t *testing.T, e *testEnv, query, agent, account string) map[string]any {
	t.Helper()
	for after := ""; ; {
		out := viewOfKey(t, e, query+"&after="+after)
		for _, raw := range out["views"].([]any) {
			if v := raw.(map[string]any); v["agent"].(map[string]any)["id"] == agent && v["account"].(map[string]any)["id"] == account {
				return v
			}
		}
		next, _ := out["next"].(string)
		if next == "" {
			return nil
		}
		after = next
	}
}

// One question, one answer: for each kind of entry (plain; pinning its
// login folder; setting a login variable; a yolo recipe that sets one) and
// each state of the accounts (none; a registered account the adapter's
// default), the view with no account and with the one it answered, with
// and without yolo, the listing's view of that key under the same query,
// and the read all answer one account, one state and one host; and GET
// /api/accounts' own-login reading comes only from an entry whose
// environment neither pins the folder nor sets a login variable.
func TestCapacityOneQuestionOneAnswer(t *testing.T) {
	kinds := []catalog.Agent{
		{ID: "q-plain", Name: "Plain", Adapter: "claude", Command: []string{"/bin/cat"}},
		{ID: "q-yolo", Name: "Yolo", Adapter: "claude", Command: []string{"/bin/cat"}, Yolo: &catalog.Yolo{Env: map[string]string{"ANTHROPIC_API_KEY": "sk-invented"}}},
		{ID: "q-pinned", Name: "Pinned", Adapter: "claude", Command: []string{"/bin/cat"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": realRoot(t, t.TempDir())}},
		{ID: "q-keyed", Name: "Keyed", Adapter: "claude", Command: []string{"/bin/cat"}, Env: map[string]string{"ANTHROPIC_API_KEY": "sk-invented"}},
		{ID: "q-token", Name: "Token", Adapter: "claude", Command: []string{"/bin/cat"}, Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "invented"}},
	}
	claude := newFakeUsage("claude")
	e := capEnv(t, nil, nil, claude, newFakeUsage("codex"), kinds, withRuntime(&sessiontest.FakeRuntime{Place: "boxcar", Base: local.Native()}))
	events := e.sse(t)
	answer := func(v map[string]any) string {
		if v == nil {
			return "absent"
		}
		return fmt.Sprintf("%v %v %v", v["account"].(map[string]any)["id"], v["state"], v["host"].(map[string]any)["kind"])
	}
	for _, docState := range []string{"no account", "a registered default"} {
		if docState == "a registered default" {
			id := addAccount(t, e, "claude", "Work")
			e.expect(t, "PUT", "/api/accounts/defaults/claude", map[string]any{"account": id, "expectedRev": accountsRev(t, e)}, http.StatusOK, "")
		}
		for _, a := range kinds {
			read := readAndAwait(t, e, claude, events, a.ID, "")
			for _, yolo := range []string{"0", "1"} {
				q := "launch=managed&yolo=" + yolo
				one := viewOfKey(t, e, "agent="+a.ID+"&"+q)
				acct := one["account"].(map[string]any)["id"].(string)
				forms := map[string]map[string]any{
					"the view naming " + acct: viewOfKey(t, e, "agent="+a.ID+"&account="+acct+"&"+q),
					"the listing":             listedView(t, e, q, a.ID, acct),
				}
				for form, v := range forms {
					if answer(v) != answer(one) {
						t.Errorf("%s, %s, %s: the view answers %s, %s %s", docState, a.ID, q, answer(one), form, answer(v))
					}
				}
				if got := fmt.Sprintf("%v %v", read["account"].(map[string]any)["id"], read["state"]); yolo == "0" && got != fmt.Sprintf("%v %v", acct, one["state"]) {
					t.Errorf("%s, %s: the read answers %s, the view %s", docState, a.ID, got, answer(one))
				}
			}
		}
		for _, raw := range e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")["adapters"].([]any) {
			if ad := raw.(map[string]any); ad["adapter"] == "claude" {
				if r, _ := ad["reading"].(map[string]any); r == nil || (r["agent"] != "q-plain" && r["agent"] != "q-yolo") {
					t.Errorf("%s: Claude Code's own login's reading is %v", docState, ad["reading"])
				}
			}
		}
	}
}

// A reading of an account that is no longer in the document is not kept,
// and one of an account that is is not dropped for want of room: every
// account read, one read held at the fake's gate, that account removed,
// the gate opened; an account added in its place and read shows its
// reading, and nothing names the removed id.
func TestAccountsReadingsNameWhatTheDocumentHolds(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	events := e.sse(t)
	var ids []string
	for i := range capacity.MaxAccounts {
		ids = append(ids, addAccount(t, e, "codex", fmt.Sprintf("Codex %02d", i)))
	}
	for _, id := range append(ids[1:], capacity.DefaultID) {
		readAndAwait(t, e, codex, events, "codex-a", id)
	}
	readAndAwait(t, e, claude, events, "claude-a", "")
	gate, called := make(chan struct{}), make(chan struct{}, 1)
	codex.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _, _ = e.callAs(t, adminToken, "POST", "/api/capacity/read", []byte(`{"agent":"codex-a","account":"`+ids[0]+`"}`))
	}()
	<-called
	codex.set(func(f *fakeUsage) { f.called = nil })
	if status, data, _ := e.callAs(t, adminToken, "DELETE", "/api/accounts/"+ids[0]+"?expectedRev="+strconv.FormatInt(accountsRev(t, e), 10), nil); status != http.StatusNoContent {
		t.Fatalf("the remove: %d %s", status, data)
	}
	close(gate)
	awaitEvent(t, events, capacityEventOf("codex-a", ids[0]))
	<-held
	added := addAccount(t, e, "codex", "In its place")
	readAndAwait(t, e, codex, events, "codex-a", added)
	_, data, out := e.callAs(t, adminToken, "GET", "/api/accounts", nil)
	for _, raw := range out["accounts"].([]any) {
		if a := raw.(map[string]any); a["id"] == added && a["reading"] == nil {
			t.Errorf("the account added in the removed one's place shows no reading: %v", a)
		}
	}
	e.srv.capacity.mu.Lock()
	_, kept := e.srv.capacity.lastRead[ids[0]]
	e.srv.capacity.mu.Unlock()
	if kept || bytes.Contains(data, []byte(ids[0])) {
		t.Fatalf("the removed account's reading is kept")
	}
}

// Every refusal is bounded, and names nothing a request said outside its
// form: each route asked with a 40 KiB value in each place a value goes
// answers 400 within the route's bound, without the value.
func TestCapacityRefusalsBounded(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	long := strings.Repeat("q", 40<<10)
	inBody := strings.Repeat("q", 900) // what a read's 1 KiB body holds
	for _, c := range []struct {
		method, path, body string
		bound              int
	}{
		{"GET", "/api/capacity?agent=" + long, "", maxCapacityView},
		{"GET", "/api/capacity?agent=claude-a&account=" + long, "", maxCapacityView},
		{"GET", "/api/capacity?agent=claude-a&model=" + long, "", maxCapacityView},
		{"GET", "/api/capacity?agent=claude-a&launch=" + long, "", maxCapacityView},
		{"GET", "/api/capacity?after=" + long, "", maxCapacityPage},
		{"PATCH", "/api/accounts/" + long, `{"label":"x","expectedRev":0}`, maxAccountsReply},
		{"POST", "/api/accounts/" + long + "/retire", `{"expectedRev":0}`, maxAccountsReply},
		{"DELETE", "/api/accounts/" + long + "?expectedRev=0", "", maxAccountsReply},
		{"PUT", "/api/accounts/defaults/" + long, `{"account":"default","expectedRev":0}`, maxAccountsReply},
		{"PUT", "/api/accounts/defaults/claude", `{"account":"` + inBody + `","expectedRev":0}`, maxAccountsReply},
		{"POST", "/api/capacity/read", `{"agent":"codex-a","account":"` + inBody + `"}`, maxCapacityView},
		{"POST", "/api/capacity/read", `{"agent":"` + inBody + `"}`, maxCapacityView},
	} {
		status, data, _ := e.callAs(t, adminToken, c.method, c.path, []byte(c.body))
		if status != http.StatusBadRequest || len(data) > c.bound || bytes.Contains(data, []byte("qqqqqqqqqqqqqqqq")) {
			t.Errorf("%s %.80s: %d, %d bytes: %.200s", c.method, c.path, status, len(data), data)
		}
	}
}

// A registered account's folder is left out of what a session's file
// routes and frames serve, as the data directory is: inside a session's
// folder, a file in it is refused (denied) to the admin and to a link's
// viewer and controller, over HTTP and in-band, read, listed or saved; the
// file beside it is served; once the account is removed it stays refused,
// as every entry of the list is kept once named, until the server stops.
func TestAccountFolderLeftOutOfSessionFiles(t *testing.T) {
	const secret = "login-file-7731"
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	folder := filepath.Join(e.root, "logins", "work")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{filepath.Join(folder, "auth.json"): secret, filepath.Join(e.root, "logins", "notes.md"): "# beside\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": folder, "expectedRev": 0}, http.StatusCreated, "")
	id := out["account"].(map[string]any)["id"].(string)
	sid := e.createSession("cat")
	tokens := map[string]string{"the admin": adminToken}
	for _, role := range []string{"view", "control"} {
		_, lo := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": role})
		tokens["a "+role+" link"] = lo["token"].(string)
	}
	denied := []string{"logins/work/auth.json", filepath.Join(folder, "auth.json"), "logins/work", "logins/work/none.json"}
	for who, tok := range tokens {
		for _, p := range denied {
			for _, mode := range []string{"raw", "stat", ""} {
				if status, got, code := e.getFile(sid, tok, p, mode); status != http.StatusForbidden || code != "denied" || strings.Contains(got, secret) {
					t.Errorf("%s, HTTP %q (%q): %d %.200s", who, p, mode, status, got)
				}
			}
		}
		if status, got, _ := e.getFile(sid, tok, "logins/notes.md", "raw"); status != http.StatusOK || got != "# beside\n" {
			t.Errorf("%s, the file beside: %d %q", who, status, got)
		}
		c := dialViewer(t, e, sid, tok)
		c.hello(80, 24)
		c.expectControl(proto.CtlReady)
		for i, p := range denied {
			reqID := fmt.Sprintf("d%d", i)
			c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p}))
			if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
				t.Errorf("%s, file_get %q: %+v %q", who, p, h, b)
			}
		}
		if who != "a view link" {
			save, err := proto.EncodeFileWrite(proto.FileWrite{ReqID: "w1", Path: "logins/work/new.json", Total: 2, Force: true}, []byte("{}"))
			if err != nil {
				t.Fatal(err)
			}
			c.send(save)
			if h, _ := c.expectFile("w1"); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" {
				t.Errorf("%s, a save: %+v", who, h)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "new.json")); err == nil {
		t.Fatal("a save reached the account's folder")
	}
	e.expect(t, "DELETE", "/api/accounts/"+id+"?expectedRev=1", nil, http.StatusNoContent, "")
	if status, got, code := e.getFile(sid, tokens["a view link"], "logins/work/auth.json", "raw"); status != http.StatusForbidden || code != "denied" || strings.Contains(got, secret) {
		t.Fatalf("the folder once the account is removed: %d %q", status, got)
	}
}

// An account added and removed with no file request between still has its
// folder refused: the add has the server name it in its deny list, which
// keeps it until the server stops.
func TestAccountFolderAddedAndRemovedUnseenStaysRefused(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	folder := loginsIn(t, e.root)
	sid, tok := viewOfFiles(t, e)
	out := e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": folder, "expectedRev": 0}, http.StatusCreated, "")
	id := out["account"].(map[string]any)["id"].(string)
	e.expect(t, "DELETE", "/api/accounts/"+id+"?expectedRev=1", nil, http.StatusNoContent, "")
	filesServed(t, e, sid, tok, "added and removed with no file request between", true)
}

// loginsIn makes logins/work, holding auth.json, and logins/notes.md
// beside it in root: the folder and the file the next tests ask for.
func loginsIn(t *testing.T, root string) string {
	t.Helper()
	folder := filepath.Join(root, "logins", "work")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{filepath.Join(folder, "auth.json"): "login-file-7731", filepath.Join(root, "logins", "notes.md"): "# beside\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

// viewOfFiles is a new cat session's id on e and a view link's token for it.
func viewOfFiles(t *testing.T, e *testEnv) (string, string) {
	t.Helper()
	// On the tool's own login, which needs no account read: a launch that
	// names none takes the adapter's default, which an unreadable
	// document cannot say (TestLaunchRefusedWhileTheAccountsCannotBeRead).
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "account": "default"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, out)
	}
	sid := out["id"].(string)
	t.Cleanup(func() { e.stopAndWait(sid) })
	_, lo := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": "view"})
	tok, _ := lo["token"].(string)
	if tok == "" {
		t.Fatalf("no link: %v", lo)
	}
	return sid, tok
}

// filesServed checks what a view link of sid is served: the account's
// folder never, the file beside it only when beside is true.
func filesServed(t *testing.T, e *testEnv, sid, tok, when string, beside bool) {
	t.Helper()
	if status, got, code := e.getFile(sid, tok, "logins/work/auth.json", "raw"); status != http.StatusForbidden || code != "denied" || strings.Contains(got, "login-file-7731") {
		t.Errorf("%s, the account's folder: %d %.200s", when, status, got)
	}
	status, got, code := e.getFile(sid, tok, "logins/notes.md", "raw")
	if beside && (status != http.StatusOK || got != "# beside\n") {
		t.Errorf("%s, the file beside: %d %.200s", when, status, got)
	}
	if !beside && (status != http.StatusForbidden || code != "denied") {
		t.Errorf("%s, the file beside, served while the accounts cannot be read: %d %.200s", when, status, got)
	}
}

// accountsFileOf is e's capacity/accounts.json.
func accountsFileOf(e *testEnv) string {
	return filepath.Join(e.srv.store.Dir(), capacity.Dir, capacity.AccountsFile)
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An account's folder is refused from the accounts document itself: a
// server over a record of versions that cannot be read (so the routes
// answer capacity_unavailable) still refuses it to a view link, and serves
// the file beside it.
func TestAccountFoldersNeedNoRecordOfVersions(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	folder := loginsIn(t, e.root)
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": folder, "expectedRev": 0}, http.StatusCreated, "")
	shutdown(e)
	writeFile(t, filepath.Join(e.srv.store.Dir(), capacity.Dir, capacity.VersionsFile), []byte("{oops"))
	next := serveSecond(t, e, claude, codex)
	next.expect(t, "GET", "/api/accounts", nil, http.StatusServiceUnavailable, "capacity_unavailable")
	sid, tok := viewOfFiles(t, next)
	filesServed(t, next, sid, tok, "the record of versions unreadable at the start", true)
}

// While the accounts document cannot be read, no file of any session is
// served, at the start and later: one that is not JSON, and JSON the Store
// does not read as the document (null, {}, accounts null or not a list, an
// account without a folder). Once it can be read again the file beside the
// folder is served, the folder still refused. The log says each change
// once, not at each request.
func TestEveryFileRefusedWhileTheAccountsCannotBeRead(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	var logged bytes.Buffer
	var logMu sync.Mutex
	e := capEnv(t, nil, slog.New(slog.NewTextHandler(lockedWriter{&logged, &logMu}, nil)), claude, codex, nil)
	folder := loginsIn(t, e.root)
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": folder, "expectedRev": 0}, http.StatusCreated, "")
	good := accountsOnDisk(t, e)
	sid, tok := viewOfFiles(t, e)
	filesServed(t, e, sid, tok, "the accounts read", true)
	var doc map[string]any
	if err := json.Unmarshal(good, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["accounts"].([]any)[0].(map[string]any), "folder")
	noFolder, _ := json.Marshal(doc)
	for _, b := range []string{"not json", "null", "{}", `{"rev":1,"accounts":null}`, `{"rev":1,"accounts":{}}`, string(noFolder)} {
		writeFile(t, accountsFileOf(e), []byte(b))
		filesServed(t, e, sid, tok, "the accounts made "+b+" later", false)
	}
	writeFile(t, accountsFileOf(e), good)
	filesServed(t, e, sid, tok, "the accounts readable again", true)
	logMu.Lock()
	for _, words := range []string{denyAllWords, denyEndWords} {
		if n := strings.Count(logged.String(), words); n != 1 {
			t.Errorf("the log says %q %d times", words, n)
		}
	}
	logMu.Unlock()
	for _, b := range []string{"not json", `{"rev":1,"accounts":null}`} {
		shutdown(e)
		writeFile(t, accountsFileOf(e), []byte(b))
		next := serveSecond(t, e, claude, codex)
		sid, tok = viewOfFiles(t, next)
		filesServed(t, next, sid, tok, "the accounts "+b+" at the start", false)
		writeFile(t, accountsFileOf(e), good)
		filesServed(t, next, sid, tok, "the accounts readable again after the start", true)
		e = next
	}
}

// An accounts document with a field this version does not know (one a
// later version writes) still names its folders: right after an add, with
// no file request between, and at the start; the file beside is served.
func TestAccountFoldersReadWhateverTheDocumentAdds(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	sid, tok := viewOfFiles(t, e)
	folder := loginsIn(t, e.root)
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": folder, "expectedRev": 0}, http.StatusCreated, "")
	var doc map[string]any
	if err := json.Unmarshal(accountsOnDisk(t, e), &doc); err != nil {
		t.Fatal(err)
	}
	doc["bindings"] = map[string]any{"s-1": "a-aaaaaaaaaaaa"}
	doc["accounts"].([]any)[0].(map[string]any)["pinned"] = true
	later, _ := json.Marshal(doc)
	writeFile(t, accountsFileOf(e), later)
	filesServed(t, e, sid, tok, "a later version's document right after the add", true)
	shutdown(e)
	next := serveSecond(t, e, claude, codex)
	sid, tok = viewOfFiles(t, next)
	filesServed(t, next, sid, tok, "a later version's document at the start", true)
}

// A folder is an account's only when the deny list, given that folder,
// refuses it, by its own path and where its links lead: one that is not is
// refused invalid_account on the folder at registration, and written into
// the document by hand it is the document's error, so no file is served,
// through the link's target or beside it.
func TestAnAccountFolderIsOneTheDenyListRefuses(t *testing.T) {
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, nil)
	folder := loginsIn(t, e.root)
	link := filepath.Join(e.root, "*work*")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatal(err)
	}
	status, body, out := e.callAs(t, adminToken, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Work", "folder": link, "expectedRev": 0})
	if status != http.StatusBadRequest || errorCode(out) != capacity.CodeInvalidAccount || !strings.Contains(string(body), `"field":"folder"`) {
		t.Errorf("the folder %s: %d %.300s", link, status, body)
	}
	e.expect(t, "POST", "/api/accounts", map[string]any{"adapter": "codex", "label": "Home", "folder": realRoot(t, t.TempDir()), "expectedRev": accountsRev(t, e)}, http.StatusCreated, "")
	var doc map[string]any
	if err := json.Unmarshal(accountsOnDisk(t, e), &doc); err != nil {
		t.Fatal(err)
	}
	doc["accounts"].([]any)[0].(map[string]any)["folder"] = link
	planted, _ := json.Marshal(doc)
	writeFile(t, accountsFileOf(e), planted)
	sid, tok := viewOfFiles(t, e)
	filesServed(t, e, sid, tok, "the folder written in by hand", false)
}

// An account's folder and the server's data directory do not overlap: a
// folder inside it, one that holds it, and a link to either are refused
// invalid_account on the folder, and the export names none of them.
func TestAccountFolderApartFromTheDataDirectory(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	data := realRoot(t, e.srv.store.Dir())
	inside := filepath.Join(data, "logins", "work")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	holds := filepath.Dir(data)
	linkIn, linkOut := filepath.Join(e.root, "in"), filepath.Join(e.root, "out")
	for link, to := range map[string]string{linkIn: inside, linkOut: holds} {
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	folders := []string{data, inside, holds, linkIn, linkOut}
	for _, f := range folders {
		status, body, out := e.callAs(t, adminToken, "POST", "/api/accounts", map[string]any{"adapter": "claude", "label": "Work", "folder": f, "expectedRev": 0})
		if status != http.StatusBadRequest || errorCode(out) != capacity.CodeInvalidAccount || !strings.Contains(string(body), `"field":"folder"`) {
			t.Errorf("the folder %s: %d %.300s", f, status, body)
		}
	}
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the export: %d %.200s", resp.StatusCode, b)
	}
	for _, f := range folders {
		if name := archiveHolding(t, b, f); name != "" {
			t.Errorf("the export's %s names %s", name, f)
		}
	}
}

// A read, a check and the view take the command the server resolved, the
// identity probe's: a command given as a path relative to a working folder
// is never looked up nor run, its view not_installed in fixed words, by
// either adapter's own reader, the listing leaving it out as it leaves out
// what is not installed.
func TestCapacityReadsNoRelativeCommand(t *testing.T) {
	dir := realRoot(t, t.TempDir())
	marker := filepath.Join(dir, "ran")
	writeFile(t, filepath.Join(dir, "tool"), []byte("#!/bin/sh\necho \"$0 $*\" >> "+marker+"\necho not-json\n"))
	if err := os.Chmod(filepath.Join(dir, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	rel := []catalog.Agent{{ID: "rel-claude", Name: "Rel Claude", Adapter: "claude", Command: []string{"./tool"}}, {ID: "rel-codex", Name: "Rel Codex", Adapter: "codex", Command: []string{"./tool"}}}
	cl, _ := agents.Get("claude")
	cx, _ := agents.Get("codex")
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), rel, withUsageReaders(map[string]*agents.UsageReader{"claude": cl.Usage, "codex": cx.Usage}))
	for _, a := range rel {
		if v := viewOfKey(t, e, "agent="+a.ID); v["state"] != capacity.StateNotInstalled || v["words"] == "" {
			t.Errorf("%s's view: %v", a.ID, v)
		}
		if v := readNow(t, e, a.ID, "", false); v["state"] != capacity.StateNotInstalled {
			t.Errorf("%s's read: %v", a.ID, v)
		}
		if v := listedView(t, e, "launch=session", a.ID, capacity.DefaultID); v != nil {
			t.Errorf("%s listed: %v", a.ID, v)
		}
	}
	if v := readNow(t, e, "rel-claude", "", true); v["state"] != capacity.StateNotInstalled {
		t.Errorf("the check: %v", v)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("a program in the server's working folder ran: %s", b)
	}
}

// A reading is filed by what its read used, decided as the read starts: a
// pinned entry's read held while the entry is edited to pin nothing, and
// one held while its adapter changes (with its environment or alone), file
// nothing as an adapter's own login's reading; and the entry's capacity
// answer is stale, the reading made by what the entry no longer is.
func TestAReadingIsFiledByWhatItsReadUsed(t *testing.T) {
	pinned := func(id string) catalog.Agent {
		return catalog.Agent{ID: id, Name: id, Adapter: "claude", Command: []string{"/bin/cat"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": realRoot(t, t.TempDir())}}
	}
	claude, codex := newFakeUsage("claude"), newFakeUsage("codex")
	e := capEnv(t, nil, nil, claude, codex, []catalog.Agent{pinned("q-unpinned"), pinned("q-moved"), pinned("q-adapter")})
	events := e.sse(t)
	for _, c := range []struct {
		id      string
		edit    func(a *catalog.Agent)
		adapter string
	}{
		{"q-unpinned", func(a *catalog.Agent) { a.Env = nil }, "claude"},
		{"q-moved", func(a *catalog.Agent) { a.Adapter, a.Env = "codex", nil }, "codex"},
		{"q-adapter", func(a *catalog.Agent) { a.Adapter = "codex" }, "codex"},
	} {
		gate, called := make(chan struct{}), make(chan struct{}, 1)
		claude.set(func(f *fakeUsage) { f.gate, f.called = gate, called })
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = e.callAs(t, adminToken, "POST", "/api/capacity/read", []byte(`{"agent":"`+c.id+`"}`))
		}()
		<-called
		claude.set(func(f *fakeUsage) { f.gate, f.called = nil, nil })
		cat := e.srv.Catalog()
		a, _ := cat.Get(c.id)
		c.edit(&a)
		changed, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: replaceAgent(cat.List(), a)})
		if err != nil {
			t.Fatal(err)
		}
		e.setCatalog(changed)
		close(gate)
		awaitEvent(t, events, capacityEventOf(c.id, capacity.DefaultID))
		<-done
		for _, raw := range e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, "")["adapters"].([]any) {
			if ad := raw.(map[string]any); ad["reading"] != nil {
				t.Errorf("%s: %s's own login's reading is %v", c.id, ad["adapter"], ad["reading"])
			}
		}
		if v := viewOfKey(t, e, "agent="+c.id); v["fresh"] != capacity.FreshStale || v["words"] != capacity.EnvWords || v["agent"].(map[string]any)["adapter"] != c.adapter {
			t.Errorf("%s: the capacity answer after the change: %v", c.id, v)
		}
	}
}

// Every route of the row that takes a query reads it as the repository's
// bounded parser does: a raw ';', an escape that is not one and a query past
// the parameter bound are refused 400 in fixed words, never answered as
// another question (an account dropped from the query is never the
// default's view).
func TestCapacityQueriesReadAsSent(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	id := addAccount(t, e, "claude", "Work")
	many := strings.Repeat("x=1&", maxQueryParams)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/capacity?agent=claude-a&account=default;"},
		{"GET", "/api/capacity?agent=claude-a&account=" + id + ";x"},
		{"GET", "/api/capacity?agent=claude-a&account=%zz"},
		{"GET", "/api/capacity?agent=claude-a&x=%zz"},
		{"GET", "/api/capacity?" + many + "agent=claude-a"},
		{"GET", "/api/capacity?limit=2;"},
		{"GET", "/api/capacity?" + many},
		{"DELETE", "/api/accounts/" + id + "?expectedRev=1;"},
		{"DELETE", "/api/accounts/" + id + "?expectedRev=%zz"},
		{"DELETE", "/api/accounts/" + id + "?" + many + "expectedRev=1"},
	} {
		status, data, _ := e.callAs(t, adminToken, c.method, c.path, nil)
		if status != http.StatusBadRequest || len(data) > maxCapacityView || bytes.Contains(data, []byte(id)) {
			t.Errorf("%s %.80s: %d %.200s", c.method, c.path, status, data)
		}
	}
	if got := e.expect(t, "GET", "/api/accounts", nil, http.StatusOK, ""); len(got["accounts"].([]any)) != 1 {
		t.Fatalf("a refused remove removed: %v", got)
	}
}

// An outcome whose save failed is saved once more before capacity/owner.lock
// goes, by either way a server stops: with the save working again at the
// stop, the next owner holds the version turned and starts nothing of it;
// with the save still failing, the record is byte for byte what it was.
// The save fails as a disk refuses it: capacity/ made read-only.
func TestAnOutcomeNotKeptIsSavedBeforeTheLockGoes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder made read-only")
	}
	fastLockTick(t)
	for _, stop := range []struct {
		name string
		fn   func(e *testEnv)
	}{
		{"Shutdown", shutdown},
		{"WaitBackground", func(e *testEnv) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			e.srv.WaitBackground(ctx)
		}},
	} {
		for _, works := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, the save working at the stop %t", stop.name, works), func(t *testing.T) {
				admitted, gate, told := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				var mu sync.Mutex
				starts := 0
				turning := &agents.UsageReader{Qualified: "2.0.0", MinVersion: "2.0.0", Read: func(ctx context.Context, in agents.UsageInput) agents.UsageReport {
					adm, err := in.Admit("2.1.7")
					if err != nil {
						return agents.UsageReport{State: agents.UsageUnqualified, Words: err.Error(), Version: "2.1.7"}
					}
					mu.Lock()
					starts++
					mu.Unlock()
					first := false
					once.Do(func() { first = true; close(admitted); <-gate })
					adm.Done(agents.Turned)
					if first {
						close(told)
					}
					return agents.UsageReport{State: agents.UsageUnqualified, Words: "a model turn was taken", Version: "2.1.7", Outcome: agents.Turned}
				}}
				readers := withUsageReaders(map[string]*agents.UsageReader{"claude": turning, "codex": newFakeUsage("codex").reader()})
				e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), capAgents, readers)
				dir := filepath.Join(e.srv.store.Dir(), capacity.Dir)
				versions := filepath.Join(dir, capacity.VersionsFile)
				go func() {
					_, _, _ = e.callAs(t, adminToken, "POST", "/api/capacity/read", []byte(`{"agent":"claude-a","check":true}`))
				}()
				<-admitted
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				close(gate)
				<-told
				if st := e.srv.capacity.st.Versions().State("claude", "2.1.7"); st != capacity.VersionTurned {
					t.Fatalf("in memory after the failed save: %q", st)
				}
				before, _ := os.ReadFile(versions)
				if works {
					if err := os.Chmod(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				stop.fn(e)
				if after, _ := os.ReadFile(versions); !works && !bytes.Equal(before, after) {
					t.Fatalf("the record changed with its save failing: %s, then %s", before, after)
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store, readers)
				if err != nil {
					t.Fatal(err)
				}
				second := e.serve(srv)
				until(t, "the second owns capacity/", owns(second))
				if !works {
					return
				}
				if st := second.srv.capacity.st.Versions().State("claude", "2.1.7"); st != capacity.VersionTurned {
					t.Fatalf("the next owner holds %q", st)
				}
				for _, check := range []string{"false", "true"} {
					second.callAs(t, adminToken, "POST", "/api/capacity/read", []byte(`{"agent":"claude-a","check":`+check+`}`))
				}
				mu.Lock()
				defer mu.Unlock()
				if starts != 1 {
					t.Fatalf("the turned version was started %d times", starts)
				}
			})
		}
	}
}

// jsonEscaped is s as a JSON string with every byte written as an escape,
// the longest spelling JSON allows: a byte of ASCII as \u00XX, a character
// past the BMP as its surrogate pair.
func jsonEscaped(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r > 0xffff {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	b.WriteByte('"')
	return b.String()
}

// The body bound of an add and of a rename is the largest valid body: each
// text field at its limit in its most escaped spelling. A folder at its
// limit so spelled reaches the folder's checks (not a body too large), one
// byte past it is refused as a folder, and a label at its limit so spelled
// is renamed.
func TestAccountBodiesBoundTheirLargestSpelling(t *testing.T) {
	e := capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), nil)
	id := addAccount(t, e, "claude", "Work")
	label := strings.Repeat("\U0001F600", capacity.MaxLabel)
	base := realRoot(t, t.TempDir())
	at := base + "/" + strings.Repeat("d", capacity.MaxFolder-len(base)-1)
	for _, c := range []struct {
		folder string
		past   bool // past the folder's limit: refused for its length
	}{{at, false}, {at + "d", true}} {
		body := `{"adapter":` + jsonEscaped("claude") + `,"label":` + jsonEscaped(label) + `,"folder":` + jsonEscaped(c.folder) + `,"expectedRev":` + strconv.FormatInt(accountsRev(t, e), 10) + `}`
		status, data, out := e.callAs(t, adminToken, "POST", "/api/accounts", []byte(body))
		if status != http.StatusBadRequest || errorCode(out) != capacity.CodeInvalidAccount || !strings.Contains(string(data), `"field":"folder"`) || strings.Contains(string(data), "at most") != c.past {
			t.Errorf("a folder of %d bytes in a %d-byte body: %d %.300s", len(c.folder), len(body), status, data)
		}
	}
	rev := strconv.FormatInt(accountsRev(t, e), 10)
	body := `{"label":` + jsonEscaped(label) + `,"expectedRev":` + rev + `}`
	if status, data, _ := e.callAs(t, adminToken, "PATCH", "/api/accounts/"+id, []byte(body)); status != http.StatusOK {
		t.Errorf("a label at its limit in a %d-byte body: %d %.300s", len(body), status, data)
	}
}
