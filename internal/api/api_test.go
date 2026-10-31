package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

const adminToken = "test-admin-token"

type testEnv struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	root   string
	client *http.Client
}

func newTestEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	return newTestEnvLogging(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// newTestEnvLogging is newTestEnv with the server logging to log.
func newTestEnvLogging(t *testing.T, mutate func(*config.Config), log *slog.Logger) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, mutate, log, nil)
}

// newTestEnvAgents is newTestEnvLogging with extra agents in the catalog.
func newTestEnvAgents(t *testing.T, mutate func(*config.Config), log *slog.Logger, extra []catalog.Agent) *testEnv {
	t.Helper()
	// Resolved, as the server reports a session's cwd (resolveCwd): on
	// macOS a temporary directory is under /var, a link to /private/var.
	root := realRoot(t, t.TempDir())
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{"test-host-token"}
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.PublicURL = "http://example.test"
	cfg.Dev = true
	if mutate != nil {
		mutate(cfg)
	}
	// The store opens at cfg.DataDir, as in `conductor serve`; unless a test
	// places it, that is a directory outside the session root, and the
	// server's Dispatch store there starts from a copy of a new one
	// (seedDispatch): a test that places its own, as the Dispatch tests do,
	// has the server make it.
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		if !cfg.Switchyard.Enabled {
			seedDispatch(t, cfg.DataDir)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append([]catalog.Agent{
		{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}},
		{ID: "sh", Name: "sh", Command: []string{"/bin/sh"}, AllowArgs: true},
		{ID: "exit", Name: "exit", Command: []string{"/bin/sh", "-c", "exit 4"}},
	}, extra...)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, cat, log, nil, st)
	if err != nil {
		t.Fatal(err)
	}
	// A launch installs the skill in the server user's home: a test's home
	// is its own (one with no known home keeps none).
	if srv.home != "" {
		srv.home = t.TempDir()
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
	})
	endAtCleanup(t, srv)
	return &testEnv{t: t, srv: srv, http: hs, root: root, client: hs.Client()}
}

// endAtCleanup ends what srv started on its own when the test is over.
// Webhooks are delivered by goroutines of their own: they stop before the
// endpoints a test started for them close; and what the server started on
// its own (a run's record) lands before the test's directories go, its
// records, its ledger and its Dispatch store closed after it
// (WaitBackground). Every server a test makes over a store is ended so, a
// second one over the first's store (serve, restart, a New of the test's
// own) as much as the first: each opens a Dispatch store, whose pruning
// goroutine runs until the store is closed (TestMain fails a run that
// leaves one open).
func endAtCleanup(t *testing.T, srv *Server) {
	t.Helper()
	t.Cleanup(func() {
		srv.webhooks.close(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.WaitBackground(ctx)
	})
}

func (e *testEnv) do(method, path, token string, body any) (*http.Response, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	if len(data) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(data, &out)
	}
	return resp, out
}

func (e *testEnv) createSession(agent string) string {
	e.t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": agent})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create session: %d %v", resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	e.t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(e.t.Context())
		}
	})
	return id
}

// agentToken gives a server session a known agent token, as the process in it
// receives one, and returns it.
func (e *testEnv) agentToken(id string) string {
	e.t.Helper()
	d, ok := e.srv.registry.Get(id)
	local, isLocal := d.(*session.Local)
	if !ok || !isLocal {
		e.t.Fatalf("session %s is not a server session", id)
	}
	tok := "agent-token-" + id
	local.SetAgentToken(tok)
	return tok
}

// sse opens GET /api/events as the admin and returns what it streams as
// "<event> <data>" strings, the snapshot first. The subscription is in place
// when sse returns, and the stream is read for as long as the test runs, so a
// test that posts a great many events cannot stall it.
func (e *testEnv) sse(t *testing.T) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", e.http.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events stream: %d", resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	events := make(chan string, 4096)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				select {
				case events <- name + " " + strings.TrimPrefix(line, "data: "):
				case <-t.Context().Done():
					return
				}
			}
		}
	}()
	return events
}

// waitEvent reads events until match accepts one and returns it. Events
// before it are discarded.
func (e *testEnv) waitEvent(t *testing.T, events <-chan string, match func(string) bool) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("event not observed")
		}
	}
}

// activityPayload decodes the data of an "activity <json>" event.
func activityPayload(t *testing.T, ev string) map[string]any {
	t.Helper()
	data, ok := strings.CutPrefix(ev, "activity ")
	if !ok {
		t.Fatalf("not an activity event: %.200s", ev)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("activity data: %v in %.200s", err, data)
	}
	return m
}

func isActivity(typ, sessionID string) func(string) bool {
	return func(ev string) bool {
		return strings.HasPrefix(ev, "activity ") && strings.Contains(ev, `"type":"`+typ+`"`) && strings.Contains(ev, sessionID)
	}
}

// waitEnded blocks until the server session has ended.
func (e *testEnv) waitEnded(id string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := e.srv.registry.Get(id); ok && d.Info().Status.Ended() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("session %s did not end", id)
}

func errorCode(out map[string]any) any {
	if m, ok := out["error"].(map[string]any); ok {
		return m["code"]
	}
	return nil
}

func TestAuthAndHealth(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, out := e.do("GET", "/api/health", "", nil)
	if resp.StatusCode != 200 || out["ok"] != true {
		t.Fatalf("health %d %v", resp.StatusCode, out)
	}
	resp, _ = e.do("GET", "/api/sessions", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	resp, _ = e.do("GET", "/api/sessions", "wrong", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", resp.StatusCode)
	}
	resp, out = e.do("GET", "/api/catalog", adminToken, nil)
	if resp.StatusCode != 200 || len(out["agents"].([]any)) != 3 {
		t.Fatalf("catalog %d %v", resp.StatusCode, out)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers missing: %v", resp.Header)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	cases := []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"agentId": "nope"}, "invalid_agent"},
		{map[string]any{"agentId": "cat", "args": []string{"x"}}, "args_not_allowed"},
		{map[string]any{"agentId": "cat", "cwd": "/"}, "invalid_cwd"},
		{map[string]any{"agentId": "cat", "cwd": filepath.Join(e.root, "missing")}, "invalid_cwd"},
		{map[string]any{"agentId": "cat", "cols": 9999}, "invalid_request"},
		{map[string]any{"agentId": "cat", "extra": true}, "invalid_request"},
	}
	for i, c := range cases {
		resp, out := e.do("POST", "/api/sessions", adminToken, c.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("case %d: status %d %v", i, resp.StatusCode, out)
		}
		if got := out["error"].(map[string]any)["code"]; got != c.code {
			t.Fatalf("case %d: code %v want %s", i, got, c.code)
		}
	}
	// symlink escaping the root is rejected even though it resolves
	outside := t.TempDir()
	link := filepath.Join(e.root, "escape")
	os.Symlink(outside, link)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": link})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink escape: %d %v", resp.StatusCode, out)
	}
}

func TestSessionLifecycle(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.MaxSessions = 2 })
	id := e.createSession("cat")
	resp, out := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get: %d %v", resp.StatusCode, out)
	}
	sess := out["session"].(map[string]any)
	if sess["status"] != "running" || sess["kind"] != "server" || sess["cwd"] != e.root || out["role"] != "control" {
		t.Fatalf("session %v", out)
	}
	if _, ok := out["links"]; !ok {
		t.Fatal("admin view must include links")
	}
	resp, out = e.do("GET", "/api/sessions", adminToken, nil)
	if resp.StatusCode != 200 || len(out["sessions"].([]any)) != 1 {
		t.Fatalf("list %v", out)
	}
	e.createSession("cat")
	resp, out = e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("limit: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("DELETE", "/api/sessions/"+id, adminToken, nil)
	if resp.StatusCode != 200 || out["status"] != "stopped" {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	resp, _ = e.do("DELETE", "/api/sessions/"+id, adminToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	resp, _ = e.do("GET", "/api/sessions/"+id, adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after remove: %d", resp.StatusCode)
	}
}

func TestExitedSessionReportsCode(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("exit")
	d, _ := e.srv.registry.Get(id)
	select {
	case <-d.(*session.Local).Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit")
	}
	info := d.Info()
	if info.Status != session.StatusExited || info.ExitCode == nil || *info.ExitCode != 4 {
		t.Fatalf("info %+v", info)
	}
}

func TestLinksAndJoin(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	resp, _ := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "admin"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad role: %d", resp.StatusCode)
	}
	resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "qa"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create link: %d %v", resp.StatusCode, out)
	}
	token := out["token"].(string)
	link := out["link"].(map[string]any)
	if !strings.HasPrefix(out["url"].(string), "http://example.test/join/") {
		t.Fatalf("url %v", out["url"])
	}
	_, out = e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
	links := out["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["label"] != "qa" {
		t.Fatalf("list links %v", out)
	}
	if _, has := links[0].(map[string]any)["token"]; has {
		t.Fatal("token must not be listed")
	}
	resp, out = e.do("GET", "/api/join/"+token, "", nil)
	if resp.StatusCode != 200 || out["role"] != "view" || out["session"].(map[string]any)["id"] != id {
		t.Fatalf("join %d %v", resp.StatusCode, out)
	}
	// share token can read its own session but nothing else
	resp, out = e.do("GET", "/api/sessions/"+id, token, nil)
	if resp.StatusCode != 200 || out["role"] != "view" {
		t.Fatalf("share get %d %v", resp.StatusCode, out)
	}
	if _, has := out["links"]; has {
		t.Fatal("share token must not see links")
	}
	resp, _ = e.do("GET", "/api/sessions", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("share token listing: %d", resp.StatusCode)
	}
	other := e.createSession("cat")
	resp, _ = e.do("GET", "/api/sessions/"+other, token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("share token cross-session: %d", resp.StatusCode)
	}
	resp, _ = e.do("DELETE", "/api/sessions/"+other+"/links/"+link["id"].(string), adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke via wrong session: %d", resp.StatusCode)
	}
	resp, _ = e.do("DELETE", "/api/sessions/"+id+"/links/"+link["id"].(string), adminToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	resp, out = e.do("GET", "/api/join/"+token, "", nil)
	if resp.StatusCode != http.StatusNotFound || out["error"].(map[string]any)["code"] != "revoked" {
		t.Fatalf("join after revoke %d %v", resp.StatusCode, out)
	}
}

func TestFilesEndpoint(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.FileView = config.FileViewControl })
	os.WriteFile(filepath.Join(e.root, "notes.md"), []byte("# hi\n"), 0o600)
	id := e.createSession("cat")
	resp, out := e.do("GET", "/api/sessions/"+id+"/files?path=notes.md", adminToken, nil)
	if resp.StatusCode != 200 || out["content"] != "# hi\n" {
		t.Fatalf("file %d %v", resp.StatusCode, out)
	}
	resp, _ = e.do("GET", "/api/sessions/"+id+"/files?path=../etc/passwd", adminToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("escape: %d", resp.StatusCode)
	}
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	resp, _ = e.do("GET", "/api/sessions/"+id+"/files?path=notes.md", lo["token"].(string), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("view role under control policy: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.http.URL+"/api/sessions/"+id+"/files?path=notes.md&raw=1", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	raw, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(raw.Body)
	raw.Body.Close()
	if string(body) != "# hi\n" || !strings.HasPrefix(raw.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("raw %q %s", body, raw.Header.Get("Content-Type"))
	}
}

// The data directory holds catalog.json, whose env values are secrets. When it
// sits inside a session's working directory, as it did by default in the
// Docker image and with conductor.example.json, no file read may reach it: not
// the HTTP route and not the in-band file_get, not through a view link and not
// with the admin token. The rest of the working directory stays readable.
func TestFileReadsNeverReachTheDataDirectory(t *testing.T) {
	const secret = "sk-live-do-not-share"
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = filepath.Join(c.DefaultCwd, "conductor.d") })
	body := agentBody("keyed")
	body["env"] = map[string]string{"OPENAI_API_KEY": secret}
	e.save(body)
	if err := os.WriteFile(filepath.Join(e.root, "notes.md"), []byte("# hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the working directory is no way around the rule.
	if err := os.Symlink(filepath.Join(e.root, "conductor.d"), filepath.Join(e.root, "state")); err != nil {
		t.Fatal(err)
	}
	id := e.createSession("keyed")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)

	denied := []string{
		"conductor.d/catalog.json",
		filepath.Join(e.root, "conductor.d", "catalog.json"),
		"state/catalog.json",
		"conductor.d",
		"conductor.d/no-such-file.json", // denied, not not_found: nothing about its contents leaks
	}
	for _, token := range []string{view, adminToken} {
		for _, p := range denied {
			for _, mode := range []string{"raw", "stat", ""} {
				if status, got, code := e.getFile(id, token, p, mode); status != http.StatusForbidden || code != "denied" || strings.Contains(got, secret) {
					t.Errorf("HTTP %q (mode %q): %d %s", p, mode, status, got)
				}
			}
		}
		if status, got, _ := e.getFile(id, token, "notes.md", "raw"); status != http.StatusOK || got != "# hi\n" {
			t.Fatalf("ordinary file over HTTP: %d %q", status, got)
		}
	}

	c := dialViewer(t, e, id, view)
	c.hello(80, 24)
	c.expectControl(proto.CtlReady)
	for i, p := range denied {
		for _, stat := range []bool{false, true} {
			reqID := fmt.Sprintf("d%d-%t", i, stat)
			c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p, Stat: stat}))
			if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
				t.Errorf("file_get %q (stat %t): %+v %q", p, stat, h, b)
			}
		}
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "ok", Path: "notes.md"}))
	if h, b := c.expectFile("ok"); h.Kind != "file" || string(b) != "# hi\n" {
		t.Fatalf("ordinary file in-band: %+v %q", h, b)
	}
}

// getFile reads path from session id over HTTP with mode "raw", "stat" or ""
// (JSON) and returns the status, the body and, for a JSON reply, the error
// code.
func (e *testEnv) getFile(id, token, path, mode string) (int, string, string) {
	e.t.Helper()
	q := url.Values{"path": {path}}
	if mode != "" {
		q.Set(mode, "1")
	}
	req, _ := http.NewRequest("GET", e.http.URL+"/api/sessions/"+id+"/files?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		File proto.FileHeader `json:"file"`
	}
	code := ""
	if json.Unmarshal(b, &out) == nil && out.File.Error != nil {
		code = out.File.Error.Code
	}
	return resp.StatusCode, string(b), code
}

// The config file holds the admin token and host tokens, and a catalogPath
// file can hold agents' env secrets. `make run` serves from the directory that
// holds conductor.example.json, an allowed root, so a view link could read the
// admin token with GET /api/sessions/{id}/files?path=conductor.example.json.
// Like the data directory, neither file is readable over HTTP or in-band, by
// name, by absolute path or through a symlink; the files beside them are.
func TestFileReadsNeverReachTheConfigOrCatalogFile(t *testing.T) {
	const secret = "sk-live-from-the-catalog-file"
	var configFile, catalogFile string
	e := newTestEnv(t, func(c *config.Config) {
		// What config.Load and Validate leave for a config file and a catalog
		// file inside the session root.
		configFile = filepath.Join(c.DefaultCwd, "conductor.json")
		catalogFile = filepath.Join(c.DefaultCwd, "agents.json")
		c.Path, c.CatalogPath = configFile, catalogFile
	})
	for path, body := range map[string]string{
		configFile:  fmt.Sprintf(`{"workbenchToken": %q, "hostTokens": ["test-host-token"], "catalogPath": "agents.json"}`, adminToken),
		catalogFile: fmt.Sprintf(`{"agents": [{"id": "keyed", "name": "keyed", "command": ["/bin/cat"], "env": {"OPENAI_API_KEY": %q}}]}`, secret),
		filepath.Join(e.root, "conductor.example.json"): `{"listen": ":8080"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(configFile, filepath.Join(e.root, "settings.json")); err != nil {
		t.Fatal(err)
	}
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)

	denied := []string{"conductor.json", configFile, "settings.json", "agents.json", catalogFile}
	leaks := func(body string) bool {
		return strings.Contains(body, adminToken) || strings.Contains(body, "test-host-token") || strings.Contains(body, secret)
	}
	for _, token := range []string{view, adminToken} {
		for _, p := range denied {
			for _, mode := range []string{"raw", "stat", ""} {
				if status, got, code := e.getFile(id, token, p, mode); status != http.StatusForbidden || code != "denied" || leaks(got) {
					t.Errorf("HTTP %q (mode %q): %d %s", p, mode, status, got)
				}
			}
		}
		if status, got, _ := e.getFile(id, token, "conductor.example.json", "raw"); status != http.StatusOK || got != `{"listen": ":8080"}` {
			t.Fatalf("the sibling file over HTTP: %d %q", status, got)
		}
	}

	c := dialViewer(t, e, id, view)
	c.hello(80, 24)
	c.expectControl(proto.CtlReady)
	for i, p := range denied {
		for _, stat := range []bool{false, true} {
			reqID := fmt.Sprintf("d%d-%t", i, stat)
			c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p, Stat: stat}))
			if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
				t.Errorf("file_get %q (stat %t): %+v %q", p, stat, h, b)
			}
		}
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "ok", Path: "conductor.example.json"}))
	if h, b := c.expectFile("ok"); h.Kind != "file" || string(b) != `{"listen": ":8080"}` {
		t.Fatalf("the sibling file in-band: %+v %q", h, b)
	}
}

// The copies an editor leaves beside the config file and the catalog file
// (.bak, ~, .orig, and swap and backup files that wrap the name, such as
// .conductor.json.swp and #conductor.json#) are as secret as the files: no read
// reaches them, over HTTP or in-band, whole or stat-only. The example config
// beside them still reads.
func TestFileReadsNeverReachCopiesOfTheConfigOrCatalogFile(t *testing.T) {
	var configFile, catalogFile string
	e := newTestEnv(t, func(c *config.Config) {
		configFile = filepath.Join(c.DefaultCwd, "conductor.json")
		catalogFile = filepath.Join(c.DefaultCwd, "agents.json")
		c.Path, c.CatalogPath = configFile, catalogFile
	})
	copies := []string{
		"conductor.json.bak", "conductor.json~", "CONDUCTOR.json.orig", "conductor.jſon.bak",
		".conductor.json.swp", "#conductor.json#", "agents.json.bak", "agents.json~", ".agents.json.swp",
	}
	for _, name := range append(copies, "conductor.example.json") {
		if err := os.WriteFile(filepath.Join(e.root, name), []byte(`{"workbenchToken": "`+adminToken+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)
	for _, p := range copies {
		for _, mode := range []string{"raw", "stat", ""} {
			if status, got, code := e.getFile(id, view, p, mode); status != http.StatusForbidden || code != "denied" || strings.Contains(got, adminToken) {
				t.Errorf("HTTP %q (mode %q): %d %s", p, mode, status, got)
			}
		}
	}
	if status, _, _ := e.getFile(id, view, "conductor.example.json", "raw"); status != http.StatusOK {
		t.Fatalf("the example config: %d", status)
	}
	c := dialViewer(t, e, id, view)
	c.hello(80, 24)
	c.expectControl(proto.CtlReady)
	for i, p := range copies {
		for _, stat := range []bool{false, true} {
			reqID := fmt.Sprintf("c%d-%t", i, stat)
			c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p, Stat: stat}))
			if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
				t.Errorf("file_get %q (stat %t): %+v %q", p, stat, h, b)
			}
		}
	}
}

// A config file that is a symbolic link to a file in another directory is
// edited in place or beside its target, so the copies beside the target, under
// the target's name, are as secret as those beside the link.
func TestFileReadsNeverReachCopiesBesideASymlinkedConfigTarget(t *testing.T) {
	var configFile string
	var target string
	e := newTestEnv(t, func(c *config.Config) {
		real := filepath.Join(c.DefaultCwd, "real")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		target = filepath.Join(real, "prod.json")
		configFile = filepath.Join(c.DefaultCwd, "conductor.json")
		if err := os.WriteFile(target, []byte(`{"workbenchToken": "`+adminToken+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, configFile); err != nil {
			t.Fatal(err)
		}
		c.Path = configFile
	})
	copies := []string{"real/prod.json", "real/prod.json.bak", "real/.prod.json.swp", "real/#prod.json#", "conductor.json.bak", ".conductor.json.swp"}
	for _, name := range append(copies[1:], "real/notes.txt") {
		if err := os.WriteFile(filepath.Join(e.root, name), []byte(`{"workbenchToken": "`+adminToken+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)
	for _, p := range copies {
		if status, got, code := e.getFile(id, view, p, "raw"); status != http.StatusForbidden || code != "denied" || strings.Contains(got, adminToken) {
			t.Errorf("HTTP %q: %d %s", p, status, got)
		}
	}
	if status, _, _ := e.getFile(id, view, "real/notes.txt", "raw"); status != http.StatusOK {
		t.Fatalf("a file beside the target: %d", status)
	}
}

// git's settings and credentials often hold access tokens: a remote URL with
// one in it, an extra header, a credential store. A view link reads nothing
// of a repository's .git (its config, config.worktree, the rest of it), nor
// a .git-credentials or .gitconfig in the folder, nor one a revision keeps
// (show): it is answered file_denied, over HTTP and in-band. The owner still
// reads them all, and the rest of the folder still reads.
func TestRepositorySettingsAreForControllers(t *testing.T) {
	const secret = "ghp-token-in-the-remote-url"
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	appendFile := func(p, s string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(e.root, p), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	appendFile(".git/config", "[remote \"origin\"]\n\turl = https://me:"+secret+"@example.com/r.git\n")
	appendFile(".git/config.worktree", "[http]\n\textraHeader = Authorization: Bearer "+secret+"\n")
	appendFile(".git-credentials", "https://me:"+secret+"@example.com\n")
	appendFile(".gitconfig", "[url \"https://"+secret+"@example.com/\"]\n\tinsteadOf = https://example.com/\n")
	for _, args := range [][]string{{"add", ".gitconfig"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", "settings"}} {
		cmd := exec.Command("git", append([]string{"-C", e.root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)
	held := []string{".git/config", ".git/config.worktree", ".git-credentials", ".gitconfig", filepath.Join(e.root, ".git", "config"), ".git/HEAD", ".git"}
	for _, p := range held {
		for _, mode := range []string{"raw", "stat", ""} {
			if status, got, code := e.getFile(id, view, p, mode); status != http.StatusForbidden || code != proto.ErrCodeFileDenied || strings.Contains(got, secret) {
				t.Errorf("view link, HTTP %q (mode %q): %d %s", p, mode, status, got)
			}
		}
		if status, got, _ := e.getFile(id, adminToken, p, "raw"); status != http.StatusOK || (!strings.HasPrefix(p, ".git/HEAD") && p != ".git" && !strings.Contains(got, secret)) {
			t.Errorf("owner, HTTP %q: %d %q", p, status, got)
		}
	}
	if status, _, _ := e.getFile(id, view, "README", "raw"); status != http.StatusOK {
		t.Errorf("view link, HTTP README: %d", status)
	}
	c := dialViewer(t, e, id, view)
	c.hello(80, 24)
	c.expectControl(proto.CtlReady)
	for i, p := range held {
		reqID := fmt.Sprintf("g%d", i)
		c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p}))
		if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != proto.ErrCodeFileDenied || len(b) != 0 {
			t.Errorf("view link, file_get %q: %+v %q", p, h, b)
		}
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "show", Op: proto.FileOpShow, Rev: "HEAD", Path: ".gitconfig"}))
	if h, b := c.expectFile("show"); h.Kind != "error" || h.Error == nil || h.Error.Code != proto.ErrCodeFileDenied || len(b) != 0 {
		t.Errorf("view link, .gitconfig at HEAD: %+v %q", h, b)
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "readme", Op: proto.FileOpShow, Rev: "HEAD", Path: "README"}))
	if h, b := c.expectFile("readme"); h.Kind != "show" || string(b) != "hello\n" {
		t.Errorf("view link, README at HEAD: %+v %q", h, b)
	}
}

// What the other Conductors on the machine keep is no more readable than the
// server's own files, for a session in the home directory (the desktop app's
// default root): the desktop app's directory under both names, its settings
// (which hold its switchyard token) included, the default data directory
// ~/.conductor while the server keeps its own elsewhere, and the directory
// the app names for the server it starts, which for the app on Windows is
// its folder in the Windows profile, under /mnt inside WSL. Over HTTP and
// in-band, through a view link and with the admin token; the rest of the
// home still reads.
func TestFileReadsNeverReachAnotherConductorsFiles(t *testing.T) {
	const secret = "switchyard-token-of-the-app"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	windowsApp := filepath.Join(home, "mnt", "c", "Users", "me", "AppData", "Roaming", "conductor-desktop")
	appSettings := `{"switchyardToken": "` + secret + `"}`
	for path, body := range map[string]string{
		filepath.Join(configDir, "conductor-desktop", "settings.json"):    appSettings,
		filepath.Join(configDir, "conductor-desktop", "logs", "main.log"): secret,
		filepath.Join(configDir, "Conductor", "settings.json"):            appSettings,
		filepath.Join(home, ".conductor", "catalog.json"):                 secret,
		filepath.Join(windowsApp, "settings.json"):                        appSettings,
		filepath.Join(home, "notes.md"):                                   "# hi\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEnv(t, func(c *config.Config) {
		c.AllowedRoots, c.DefaultCwd = []string{home}, home
		c.DesktopDir = windowsApp
	})
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	view := lo["token"].(string)
	rel := func(p string) string {
		r, err := filepath.Rel(home, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	denied := []string{
		rel(filepath.Join(configDir, "conductor-desktop", "settings.json")),
		rel(filepath.Join(configDir, "conductor-desktop", "logs", "main.log")),
		rel(filepath.Join(configDir, "conductor-desktop")),
		rel(filepath.Join(configDir, "Conductor", "settings.json")),
		".conductor/catalog.json",
		".conductor",
		rel(filepath.Join(windowsApp, "settings.json")),
		rel(windowsApp),
	}
	for _, token := range []string{view, adminToken} {
		for _, p := range denied {
			for _, mode := range []string{"raw", "stat", ""} {
				if status, got, code := e.getFile(id, token, p, mode); status != http.StatusForbidden || code != "denied" || strings.Contains(got, secret) {
					t.Errorf("HTTP %q (mode %q): %d %s", p, mode, status, got)
				}
			}
		}
		if status, got, _ := e.getFile(id, token, "notes.md", "raw"); status != http.StatusOK || got != "# hi\n" {
			t.Fatalf("an ordinary file in the home: %d %q", status, got)
		}
	}
	c := dialViewer(t, e, id, view)
	c.hello(80, 24)
	c.expectControl(proto.CtlReady)
	for i, p := range denied {
		reqID := fmt.Sprintf("m%d", i)
		c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: reqID, Path: p}))
		if h, b := c.expectFile(reqID); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
			t.Errorf("file_get %q: %+v %q", p, h, b)
		}
	}
	// The desktop app moves its server's data into the home while this
	// server runs: it is refused from the next request on.
	moved := filepath.Join(home, "app-data")
	if err := os.MkdirAll(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, "catalog.json"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "conductor-desktop", "settings.json"), []byte(`{"dataDir": "`+moved+`", "switchyardToken": "`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, got, code := e.getFile(id, view, "app-data/catalog.json", "raw"); status != http.StatusForbidden || code != "denied" || strings.Contains(got, secret) {
		t.Errorf("HTTP, a data directory the app moved to: %d %s", status, got)
	}
	c.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "moved", Path: "app-data/catalog.json"}))
	if h, b := c.expectFile("moved"); h.Kind != "error" || h.Error == nil || h.Error.Code != "denied" || len(b) != 0 {
		t.Errorf("file_get, a data directory the app moved to: %+v %q", h, b)
	}
}

func TestUnknownRoutesAndNoUI(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, _ := e.do("GET", "/api/nope", adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("api 404: %d", resp.StatusCode)
	}
	resp, _ = e.do("GET", "/anything", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing UI: %d", resp.StatusCode)
	}
}

func TestAttentionKindAndOptions(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	opts := []map[string]any{}
	for i := 0; i < 7; i++ {
		opts = append(opts, map[string]any{"label": "Option " + strings.Repeat("x", i), "input": strings.Repeat("1", i+1)})
	}
	resp, out := e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "Allow Bash?", "kind": "permission", "options": opts})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set attention: %d %v", resp.StatusCode, out)
	}
	_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	att := got["session"].(map[string]any)["attention"].(map[string]any)
	if att["kind"] != "permission" {
		t.Fatalf("kind %v", att)
	}
	if list, _ := att["options"].([]any); len(list) != 6 {
		t.Fatalf("options capped at 6, got %d", len(list))
	}
	resp, out = e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "needs_input", "kind": "bogus"})
	if resp.StatusCode != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_kind" {
		t.Fatalf("bogus kind: %d %v", resp.StatusCode, out)
	}
}

// A question the agent asked and carried on from gives way while the agent
// works: its hooks' report of a tool about to run (every agent's pre-tool
// event maps to working, #195) shows the session working, not Needs you,
// until the turn ends or the agent asks again.
func TestAWorkingReportSetsAnAskedQuestionAside(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	attention := func() map[string]any {
		_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		return got["session"].(map[string]any)["attention"].(map[string]any)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "Which branch?", "kind": "prompt", "ask": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("ask: %d %v", resp.StatusCode, out)
	}
	if att := attention(); att["state"] != "needs_input" {
		t.Fatalf("asked: %v", att)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", adminToken, map[string]any{"state": "working"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("working: %d %v", resp.StatusCode, out)
	}
	if att := attention(); att["state"] != "working" {
		t.Fatalf("after the agent's tool report: %v, want working", att)
	}
}

// An agent whose signal is a pattern gets the detector when it is launched: a
// prompt left on the last line marks its session needs_input. The same prompt
// from an agent with no pattern does nothing.
func TestSessionUsesTheAgentsSignalPattern(t *testing.T) {
	e := newTestEnv(t, nil)
	script := []string{"/bin/sh", "-c", `printf 'Proceed? (y/n) '; exec /bin/cat`}
	watched := agentBody("watched")
	watched["command"] = script
	watched["signal"] = map[string]any{"kind": "pattern", "pattern": `\(y/n\) $`}
	e.save(watched)
	plain := agentBody("plain")
	plain["command"] = script
	e.save(plain)
	watchedID := e.createSession("watched")
	plainID := e.createSession("plain")

	attention := func(id string) map[string]any {
		_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		att, _ := got["session"].(map[string]any)["attention"].(map[string]any)
		return att
	}
	deadline := time.Now().Add(5 * time.Second)
	for attention(watchedID)["state"] != "needs_input" {
		if time.Now().After(deadline) {
			t.Fatalf("attention %v, want needs_input", attention(watchedID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	att := attention(watchedID)
	if att["source"] != "pattern" || att["kind"] != "prompt" || att["message"] != "prompt: Proceed? (y/n)" {
		t.Fatalf("attention %v", att)
	}
	time.Sleep(300 * time.Millisecond) // the other session has been as quiet as long
	if att := attention(plainID); att["state"] != nil && att["state"] != "" {
		t.Fatalf("an agent with no pattern got attention %v", att)
	}
}

// launch starts an agent with the given arguments and returns the session.
func (e *testEnv) launch(agentID string, args []string) map[string]any {
	e.t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": agentID, "args": args})
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("launch %s: %d %v", agentID, resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	e.t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(e.t.Context())
		}
	})
	return out
}

// An agent with a hook adapter and the hook signal is launched with the
// adapter's flags after its command and the user's arguments: Claude Code
// reads Conductor's hooks from the hooks directory in the data directory, the
// set with tool events when the signal asks for them. With another signal the
// same agent gets nothing.
func TestCreateSessionInjectsTheAdapter(t *testing.T) {
	e := newTestEnv(t, nil)
	hooks := filepath.Join(e.srv.cfg.DataDir, "hooks")
	script := []string{"/bin/sh", "-c", `printf 'ARGS[%s]\n' "$*"; exec /bin/cat`, "sh"}
	for _, tc := range []struct {
		id     string
		signal map[string]any
		args   []string
		extra  []string
	}{
		{"hooked", map[string]any{"kind": "hook"}, []string{"--resume"}, []string{"--settings", filepath.Join(hooks, "claude.json")}},
		{"chatty", map[string]any{"kind": "hook", "toolEvents": true}, nil, []string{"--settings", filepath.Join(hooks, "claude-tools.json")}},
		{"belled", map[string]any{"kind": "bell"}, nil, nil},
		{"unset", nil, nil, nil},
	} {
		body := agentBody(tc.id)
		body["command"] = script
		body["allowArgs"] = true
		body["adapter"] = "claude"
		if tc.signal != nil {
			body["signal"] = tc.signal
		}
		e.save(body)
		info := e.launch(tc.id, tc.args)
		// Every launch of an adapter that takes an MCP server registers Conductor's, whatever the signal.
		want := append(append(append(slices.Clone(script), tc.args...), tc.extra...), "--mcp-config", filepath.Join(hooks, "claude-mcp.json"))
		var got []string
		for _, a := range info["command"].([]any) {
			got = append(got, a.(string))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: command %q, want %q", tc.id, got, want)
		}
		c := dialViewer(t, e, info["id"].(string), adminToken)
		c.hello(80, 24)
		c.expectOutput("ARGS[" + strings.Join(append(append(slices.Clone(tc.args), tc.extra...), "--mcp-config", filepath.Join(hooks, "claude-mcp.json")), " ") + "]")
	}
}

// A Codex session is launched with the notifications Codex rings the bell
// for, as a -c override after its command: an approval or a form, a question
// that stops it and the turn's end, never a question Codex asks while its
// turn goes on (#186). The override is the launch's alone: the person's own
// ~/.codex/config.toml, which sets notifications of its own, is byte for byte
// what it was once the session runs.
func TestCreateSessionCodexRingsOnlyForWhatWaits(t *testing.T) {
	e := newTestEnv(t, nil)
	home := t.TempDir()
	e.srv.home = home
	config := filepath.Join(home, ".codex", "config.toml")
	own := []byte("model = \"o3\"\n\n[tui]\nnotifications = true\nnotification_method = \"osc9\"\n")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, own, 0o600); err != nil {
		t.Fatal(err)
	}
	body := agentBody("codexish")
	body["command"] = []string{"/bin/sh", "-c", `printf 'ARGS[%s]\n' "$*"; exec /bin/cat`, "sh"}
	body["adapter"] = "codex"
	body["signal"] = map[string]any{"kind": "hook"}
	e.save(body)
	info := e.launch("codexish", nil)
	const want = `tui.notifications=["approval-requested","plan-mode-prompt","agent-turn-complete"]`
	var got []string
	for _, a := range info["command"].([]any) {
		got = append(got, a.(string))
	}
	if i := slices.Index(got, want); i < 1 || got[i-1] != "-c" {
		t.Fatalf("command %q has no -c %s", got, want)
	}
	c := dialViewer(t, e, info["id"].(string), adminToken)
	c.hello(200, 24)
	c.expectOutput("-c " + want)
	if b, err := os.ReadFile(config); err != nil || !bytes.Equal(b, own) {
		t.Fatalf("the person's config.toml changed: %v %q", err, b)
	}
}

// A Codex session is launched with the window title Codex then writes, its
// activity and run state, as a -c override after its command, and with the
// catalog's working title: the title Codex writes through a turn ("⠙
// Working", the bytes Codex 0.161 wrote) shows the session working, from
// its title (#186). The person's ~/.codex/config.toml, which names title
// items of its own, is byte for byte what it was. An agent whose signal has
// no working title writing the same title shows nothing.
func TestCreateSessionCodexWorksByItsTitle(t *testing.T) {
	e := newTestEnv(t, nil)
	home := t.TempDir()
	e.srv.home = home
	config := filepath.Join(home, ".codex", "config.toml")
	own := []byte("model = \"o3\"\n\n[tui]\nterminal_title = [\"project-name\"]\n")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, own, 0o600); err != nil {
		t.Fatal(err)
	}
	script := []string{"/bin/sh", "-c", `printf 'ARGS[%s]\n' "$*"; printf '\033]0;\342\240\231 Working\007'; exec /bin/cat`, "sh"}
	body := agentBody("codexish")
	body["command"] = script
	body["adapter"] = "codex"
	body["signal"] = map[string]any{"kind": "hook", "workingTitle": catalog.CodexWorkingTitle}
	e.save(body)
	plain := agentBody("plain")
	plain["command"] = script
	plain["adapter"] = "codex"
	plain["signal"] = map[string]any{"kind": "hook"}
	e.save(plain)

	info := e.launch("codexish", nil)
	const want = `tui.terminal_title=["activity","run-state"]`
	var got []string
	for _, a := range info["command"].([]any) {
		got = append(got, a.(string))
	}
	if i := slices.Index(got, want); i < 1 || got[i-1] != "-c" {
		t.Fatalf("command %q has no -c %s", got, want)
	}
	plainID := e.launch("plain", nil)["id"].(string)
	attention := func(id string) map[string]any {
		_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		att, _ := got["session"].(map[string]any)["attention"].(map[string]any)
		return att
	}
	id := info["id"].(string)
	if !waitFor(5*time.Second, func() bool { return attention(id)["state"] == "working" }) {
		t.Fatalf("attention %v, want working", attention(id))
	}
	if att := attention(id); att["source"] != "title" {
		t.Fatalf("attention %v, want working from the title", att)
	}
	time.Sleep(300 * time.Millisecond) // the other session has written its title as long
	if att := attention(plainID); att["state"] != nil && att["state"] != "" {
		t.Fatalf("an agent without a working title got attention %v", att)
	}
	if b, err := os.ReadFile(config); err != nil || !bytes.Equal(b, own) {
		t.Fatalf("the person's config.toml changed: %v %q", err, b)
	}
}

// The adapter's environment reaches the session, under the agent's own: a
// variable the agent sets itself keeps its value.
func TestCreateSessionAdapterEnvYieldsToTheAgent(t *testing.T) {
	t.Setenv("AIDER_NOTIFICATIONS", "from-the-server")
	e := newTestEnv(t, nil)
	_, env := agents.InjectFor("aider", filepath.Join(e.srv.cfg.DataDir, "hooks"), catalog.Signal{Kind: "hook"}, false)
	command := env["AIDER_NOTIFICATIONS_COMMAND"]
	if !strings.Contains(command, " notify --state done") {
		t.Fatalf("aider's command %q", command)
	}
	script := []string{"/bin/sh", "-c", `echo "N=[$AIDER_NOTIFICATIONS] C=[$AIDER_NOTIFICATIONS_COMMAND] END"; exec /bin/cat`}
	for _, tc := range []struct {
		id   string
		env  map[string]string
		want string
	}{
		{"aider-plain", nil, "N=[true] C=[" + command + "] END"},
		{"aider-own", map[string]string{"AIDER_NOTIFICATIONS": "false"}, "N=[false] C=[" + command + "] END"},
	} {
		body := agentBody(tc.id)
		body["command"] = script
		body["adapter"] = "aider"
		body["signal"] = map[string]any{"kind": "hook"}
		if tc.env != nil {
			body["env"] = tc.env
		}
		e.save(body)
		id := e.createSession(tc.id)
		c := dialViewer(t, e, id, adminToken)
		c.hello(80, 24)
		c.expectOutput(tc.want)
	}
}

// An adapter adds flags, not a new way to fail: a command the server does not
// have still answers start_failed.
func TestCreateSessionWithAnAdapterAndAMissingCommand(t *testing.T) {
	e := newTestEnv(t, nil)
	e.save(map[string]any{"id": "ghost", "name": "Ghost", "command": []string{"definitely-not-a-real-binary-xyz"}, "adapter": "claude", "signal": map[string]any{"kind": "hook"}})
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "ghost"})
	if resp.StatusCode != http.StatusBadGateway || errorCode(out) != "start_failed" {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
}

func TestLinksListReportsActiveViewers(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "standup"})
	linkID := lo["link"].(map[string]any)["id"].(string)

	_, before := e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
	if l := before["links"].([]any)[0].(map[string]any); l["active"] != float64(0) {
		t.Fatalf("active before join: %v", l)
	}

	guest := dialViewer(t, e, id, lo["token"].(string))
	guest.hello(80, 24)
	guest.expectControl(proto.CtlReady)

	// The viewer joins the session's roster just after its replay, which
	// `ready` ends: under load the list was read in between (round 14), so
	// the count is waited for, as the Share dialog's next refresh would.
	var l map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, after := e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
		l = nil
		for _, raw := range after["links"].([]any) {
			if m := raw.(map[string]any); m["id"] == linkID {
				l = m
			}
		}
		if l == nil {
			t.Fatalf("link missing: %v", after)
		}
		if l["active"] == float64(1) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if l["active"] != float64(1) || l["label"] != "standup" {
		t.Fatalf("active after join: %v", l)
	}
}

func TestCreateSessionRecordsGitBranch(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := os.MkdirAll(filepath.Join(e.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := e.createSession("cat")
	_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	if got["session"].(map[string]any)["branch"] != "main" {
		t.Fatalf("branch: %v", got["session"])
	}
}

func TestWhoAmIReportsServerUser(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, _ := e.do("GET", "/api/whoami", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous whoami: %d", resp.StatusCode)
	}
	resp, out := e.do("GET", "/api/whoami", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: %d %v", resp.StatusCode, out)
	}
	if u, _ := out["user"].(string); u == "" || len(u) > 64 {
		t.Fatalf("user %q", u)
	}
	if home, err := os.UserHomeDir(); err == nil && out["home"] != home {
		t.Fatalf("home %v, want %s", out["home"], home)
	}
}

func TestCatalogEditingPersistsOverlay(t *testing.T) {
	e := newTestEnv(t, nil) // newTestEnv opens a store in t.TempDir()
	body := map[string]any{"id": "aider", "name": "Aider", "command": []string{"aider", "--no-auto-commits"}, "allowArgs": true, "signal": map[string]any{"kind": "pattern", "pattern": `^> $`}}
	resp, out := e.do("POST", "/api/catalog", adminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %v", resp.StatusCode, out)
	}
	_, list := e.do("GET", "/api/catalog", adminToken, nil)
	var found bool
	for _, raw := range list["agents"].([]any) {
		a := raw.(map[string]any)
		if a["id"] == "aider" && a["signal"].(map[string]any)["kind"] == "pattern" {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved agent missing from catalog: %v", list)
	}
	// Persisted: the overlay file names the agent.
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 1 || ov.Agents[0].ID != "aider" {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	// Hiding a built-in.
	if resp, _ := e.do("DELETE", "/api/catalog/cat", adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hide: %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hidden agent still launchable: %d", resp.StatusCode)
	}
	// Validation and unknown fields.
	if resp, out := e.do("POST", "/api/catalog", adminToken, map[string]any{"id": "bad id", "name": "x", "command": []string{"x"}}); resp.StatusCode != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_agent" {
		t.Fatalf("bad id: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("POST", "/api/catalog", adminToken, map[string]any{"id": "x", "name": "x", "command": []string{"x"}, "bogus": 1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", resp.StatusCode)
	}
	// Auth.
	if resp, _ := e.do("POST", "/api/catalog", "", body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous save: %d", resp.StatusCode)
	}
}

func TestCatalogCheckCommand(t *testing.T) {
	e := newTestEnv(t, nil)
	_, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"/bin/sh", "-c", "x"}})
	if out["found"] != true || out["path"] != "/bin/sh" {
		t.Fatalf("sh: %v", out)
	}
	_, out = e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"definitely-not-a-real-binary-xyz"}})
	if out["found"] != false {
		t.Fatalf("missing: %v", out)
	}
	if resp, _ := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty argv: %d", resp.StatusCode)
	}
}

// GET /api/catalog says which agents are installed on this server: cat is,
// an agent whose program does not exist is not, by the check POST
// /api/catalog/check runs; site travels with the agent. An agent read there,
// available included, saves back as it is.
func TestCatalogReportsAvailability(t *testing.T) {
	e := newTestEnv(t, nil)
	ghost := agentBody("ghost")
	ghost["command"] = []string{"definitely-not-a-real-binary-xyz"}
	ghost["site"] = "https://example.com/ghost"
	if out := e.save(ghost); out["agent"].(map[string]any)["available"] != false {
		t.Fatalf("save reply: %v", out)
	}
	if a := e.catalogAgent("cat"); a["available"] != true {
		t.Fatalf("cat: %v", a)
	}
	listed := e.catalogAgent("ghost")
	if listed["available"] != false || listed["site"] != "https://example.com/ghost" {
		t.Fatalf("ghost: %v", listed)
	}
	listed["description"] = "edited"
	e.save(listed)
	bad := agentBody("badsite")
	bad["site"] = "http://example.com"
	if resp, out := e.do("POST", "/api/catalog", adminToken, bad); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_agent" {
		t.Fatalf("http site: %d %v", resp.StatusCode, out)
	}
}

// serve exposes srv over HTTP next to e's server, sharing its temp root,
// and ends srv with the test as e's own server is (endAtCleanup).
func (e *testEnv) serve(srv *Server) *testEnv {
	e.t.Helper()
	hs := httptest.NewServer(srv.Handler())
	e.t.Cleanup(hs.Close)
	endAtCleanup(e.t, srv)
	return &testEnv{t: e.t, srv: srv, http: hs, root: e.root, client: hs.Client()}
}

// setCatalog replaces the server's catalog as an edit publishes one: under
// the lock its readers take.
func (e *testEnv) setCatalog(cat catalog.Catalog) {
	e.srv.catalogMu.Lock()
	e.srv.catalog = cat
	e.srv.catalogMu.Unlock()
}

// restart starts a second server over the same store and configured catalog,
// the way `conductor serve` comes up again after a restart.
func (e *testEnv) restart() *testEnv {
	e.t.Helper()
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.serve(srv)
}

// catalogList returns the agents GET /api/catalog lists, in order.
func (e *testEnv) catalogList() []any {
	e.t.Helper()
	resp, out := e.do("GET", "/api/catalog", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("catalog: %d %v", resp.StatusCode, out)
	}
	return out["agents"].([]any)
}

// catalogAgent returns the agent GET /api/catalog lists under id, or nil.
func (e *testEnv) catalogAgent(id string) map[string]any {
	e.t.Helper()
	for _, raw := range e.catalogList() {
		if a := raw.(map[string]any); a["id"] == id {
			return a
		}
	}
	return nil
}

// catalogIDs returns the agent IDs GET /api/catalog lists, in order.
func (e *testEnv) catalogIDs() []string {
	e.t.Helper()
	var ids []string
	for _, raw := range e.catalogList() {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	return ids
}

// catalogHidden returns the IDs GET /api/catalog lists as hidden. The list
// must be there, as an array, even when nothing is hidden.
func (e *testEnv) catalogHidden() []string {
	e.t.Helper()
	resp, out := e.do("GET", "/api/catalog", adminToken, nil)
	raw, ok := out["hidden"].([]any)
	if resp.StatusCode != http.StatusOK || !ok {
		e.t.Fatalf("catalog hidden list: %d %v", resp.StatusCode, out)
	}
	ids := []string{}
	for _, id := range raw {
		ids = append(ids, id.(string))
	}
	return ids
}

// overlayFile returns the text of catalog.json in the data directory.
func (e *testEnv) overlayFile() string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "catalog.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// save posts an agent to /api/catalog and requires it to be accepted.
func (e *testEnv) save(body map[string]any) map[string]any {
	e.t.Helper()
	resp, out := e.do("POST", "/api/catalog", adminToken, body)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("save %v: %d %v", body["id"], resp.StatusCode, out)
	}
	return out
}

// del sends DELETE /api/catalog/{id} and returns the status.
func (e *testEnv) del(id string) int {
	e.t.Helper()
	resp, _ := e.do("DELETE", "/api/catalog/"+id, adminToken, nil)
	return resp.StatusCode
}

func agentBody(id string) map[string]any {
	return map[string]any{"id": id, "name": id, "command": []string{"/bin/cat"}}
}

func TestCatalogCheckLooksUpWithoutRunning(t *testing.T) {
	e := newTestEnv(t, nil)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "agent")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	cases := []struct {
		name    string
		command []string
		found   bool
		path    string
	}{
		{"absolute path, later elements ignored", []string{script, "--no-such-flag", "no-such-binary"}, true, script},
		{"bare name found through PATH", []string{"agent"}, true, script},
		{"file that is not executable", []string{plain}, false, ""},
		{"bare name of a file that is not executable", []string{"plain"}, false, ""},
		{"missing absolute path", []string{filepath.Join(dir, "missing")}, false, ""},
	}
	for _, tc := range cases {
		resp, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": tc.command})
		path, hasPath := out["path"]
		if resp.StatusCode != http.StatusOK || out["found"] != tc.found || (tc.found && path != tc.path) || (!tc.found && hasPath) {
			t.Errorf("%s: %d %v", tc.name, resp.StatusCode, out)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the check ran the command")
	}
	if resp, _ := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"sh"}, "bogus": 1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", resp.StatusCode)
	}
}

func TestCatalogSaveRejectsInvalidAgents(t *testing.T) {
	e := newTestEnv(t, nil)
	with := func(k string, v any) map[string]any {
		b := agentBody("ok")
		b[k] = v
		return b
	}
	cases := []struct {
		name string
		body any
		code string
		msg  string // part of the message
	}{
		{"bad id", with("id", "Bad Id"), "invalid_agent", "must match"},
		{"blank name", with("name", "  "), "invalid_agent", "name must not be empty"},
		{"name over 60 characters", with("name", strings.Repeat("n", 61)), "invalid_agent", "at most 60"},
		{"empty command", with("command", []string{}), "invalid_agent", "at least one element"},
		{"33 command elements", with("command", slices.Repeat([]string{"x"}, 33)), "invalid_agent", "at most 32"},
		{"unknown signal kind", with("signal", map[string]any{"kind": "smoke"}), "invalid_agent", "unknown kind"},
		{"pattern on a bell signal", with("signal", map[string]any{"kind": "bell", "pattern": "x"}), "invalid_agent", "pattern only applies"},
		{"pattern that does not compile", with("signal", map[string]any{"kind": "pattern", "pattern": "("}), "invalid_agent", "error parsing regexp"},
		{"bad envPassthrough name", with("envPassthrough", []string{"NOT OK"}), "invalid_agent", "envPassthrough"},
		{"unknown field", with("bogus", 1), "invalid_request", `"bogus"`},
		{"no body", nil, "invalid_request", ""},
		{"body over 64 KiB", with("description", strings.Repeat("d", 70<<10)), "invalid_request", "too large"},
	}
	for _, tc := range cases {
		resp, out := e.do("POST", "/api/catalog", adminToken, tc.body)
		apiErr, _ := out["error"].(map[string]any)
		msg, _ := apiErr["message"].(string)
		if resp.StatusCode != http.StatusBadRequest || apiErr["code"] != tc.code || !strings.Contains(msg, tc.msg) {
			t.Errorf("%s: %d %v", tc.name, resp.StatusCode, out)
		}
	}
	// A rejected agent is not persisted.
	if ok, err := e.srv.store.Load("catalog.json", &catalog.Overlay{}); ok || err != nil {
		t.Fatalf("overlay written for invalid agents: %v %v", ok, err)
	}
}

func TestCatalogRoutesNeedTheWorkbenchToken(t *testing.T) {
	e := newTestEnv(t, nil)
	routes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/catalog", agentBody("intruder")},
		{"DELETE", "/api/catalog/cat", nil},
		{"POST", "/api/catalog/check", map[string]any{"command": []string{"sh"}}},
		{"POST", "/api/catalog/cat/unhide", nil},
		{"POST", "/api/catalog/cat/clone", agentBody("intruder")},
	}
	for _, r := range routes {
		for _, token := range []string{"", "wrong", "test-host-token"} {
			if resp, _ := e.do(r.method, r.path, token, r.body); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: %d", r.method, r.path, token, resp.StatusCode)
			}
		}
	}
	if ids := e.catalogIDs(); !slices.Equal(ids, []string{"cat", "sh", "exit"}) {
		t.Fatalf("an unauthorized call changed the catalog: %v", ids)
	}
	if ok, err := e.srv.store.Load("catalog.json", &catalog.Overlay{}); ok || err != nil {
		t.Fatalf("an unauthorized call wrote the overlay: %v %v", ok, err)
	}
}

// A running server and one started from the saved overlay must list the same
// agents in the same order, including after a built-in was hidden and saved again.
func TestCatalogEditsMatchARestart(t *testing.T) {
	e := newTestEnv(t, nil)
	// Both servers wait for every lookup's answer: past lookupWait an
	// unanswered lookup counts as installed, and on a loaded machine one
	// server's lookup of aider (not installed, a PATH walk) answered in time
	// and the other's did not, so the two listings differed.
	e.srv.lookups.stubWait(hangWait)
	snapshot := e.srv.Catalog()

	e.save(agentBody("aider"))
	e.save(map[string]any{"id": "aider", "name": "Aider v2", "command": []string{"aider"}}) // replaces, does not repeat
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("hide: %d", c)
	}
	e.save(map[string]any{"id": "cat", "name": "Cat again", "command": []string{"/bin/cat"}}) // back, replacing the built-in
	e.save(map[string]any{"id": "sh", "name": "Shell", "command": []string{"/bin/sh"}})       // replaced in place

	if got, want := e.catalogIDs(), []string{"cat", "sh", "exit", "aider"}; !slices.Equal(got, want) {
		t.Fatalf("live order %v, want %v", got, want)
	}
	if a := e.catalogAgent("aider"); a["name"] != "Aider v2" {
		t.Fatalf("aider: %v", a)
	}
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 3 || len(ov.Hidden) != 0 {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}

	again := e.restart()
	again.srv.lookups.stubWait(hangWait)
	if live, restarted := e.catalogList(), again.catalogList(); !reflect.DeepEqual(live, restarted) {
		t.Fatalf("restart differs:\n live      %v\n restarted %v", live, restarted)
	}
	// Snapshots are never edited, so readers need no lock.
	if got := snapshot.List(); len(got) != 3 || got[0].Name != "cat" {
		t.Fatalf("snapshot taken before the edits changed: %+v", got)
	}
}

func TestCatalogDeleteRestoresOrHides(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")

	// The first edit ever is a hide: the file still holds "agents": [], not null.
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("hide: %d", c)
	}
	if raw := e.overlayFile(); !strings.Contains(raw, `"agents": []`) || strings.Contains(raw, "null") || !strings.Contains(raw, `"hidden"`) {
		t.Fatalf("overlay after hiding: %s", raw)
	}
	if e.catalogAgent("cat") != nil {
		t.Fatal("hidden agent still listed")
	}
	// Hiding does not stop a session that is already running.
	if resp, out := e.do("GET", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK || out["session"].(map[string]any)["status"] != "running" {
		t.Fatalf("running session after hide: %d %v", resp.StatusCode, out)
	}

	// Saving the ID again shows it again, replacing the built-in.
	e.save(map[string]any{"id": "cat", "name": "Cat mk2", "command": []string{"/bin/cat"}})
	if a := e.catalogAgent("cat"); a == nil || a["name"] != "Cat mk2" {
		t.Fatalf("saved over the hidden built-in: %v", a)
	}
	if strings.Contains(e.overlayFile(), `"hidden"`) {
		t.Fatalf("saved agent is still hidden: %s", e.overlayFile())
	}

	// Deleting the override brings the built-in back and leaves an empty list.
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("remove override: %d", c)
	}
	if a := e.catalogAgent("cat"); a == nil || a["name"] != "cat" {
		t.Fatalf("built-in not restored: %v", a)
	}
	if raw := e.overlayFile(); !strings.Contains(raw, `"agents": []`) || strings.Contains(raw, "null") || strings.Contains(raw, `"hidden"`) {
		t.Fatalf("overlay after removing the override: %s", raw)
	}

	// With no override left the next delete hides it, and then it is unknown.
	if c := e.del("cat"); c != http.StatusNoContent || e.catalogAgent("cat") != nil {
		t.Fatalf("hide again: %d", c)
	}
	for _, unknown := range []string{"cat", "no-such-agent"} {
		resp, out := e.do("DELETE", "/api/catalog/"+unknown, adminToken, nil)
		if resp.StatusCode != http.StatusNotFound || out["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("delete %s: %d %v", unknown, resp.StatusCode, out)
		}
	}
}

// Hiding is reversible: GET /api/catalog lists what is hidden, and unhiding
// an ID brings its agent back as it was, launchable and in its old place,
// also after a restart.
func TestCatalogUnhideRestoresAHiddenAgent(t *testing.T) {
	e := newTestEnv(t, nil)
	if hidden := e.catalogHidden(); len(hidden) != 0 {
		t.Fatalf("hidden before any edit: %v", hidden)
	}
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("hide: %d", c)
	}
	if hidden := e.catalogHidden(); !slices.Equal(hidden, []string{"cat"}) {
		t.Fatalf("hidden after hiding: %v", hidden)
	}
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hidden agent launched: %d", resp.StatusCode)
	}

	resp, out := e.do("POST", "/api/catalog/cat/unhide", adminToken, nil)
	agent, _ := out["agent"].(map[string]any)
	if resp.StatusCode != http.StatusOK || agent["id"] != "cat" || agent["name"] != "cat" || !reflect.DeepEqual(agent["command"], []any{"/bin/cat"}) {
		t.Fatalf("unhide: %d %v", resp.StatusCode, out)
	}
	if hidden := e.catalogHidden(); len(hidden) != 0 {
		t.Fatalf("hidden after unhiding: %v", hidden)
	}
	if ids := e.catalogIDs(); !slices.Equal(ids, []string{"cat", "sh", "exit"}) {
		t.Fatalf("catalog after unhiding: %v", ids)
	}
	e.createSession("cat")
	if raw := e.overlayFile(); strings.Contains(raw, `"hidden"`) {
		t.Fatalf("catalog.json still hides it: %s", raw)
	}
	if hidden := e.restart().catalogHidden(); len(hidden) != 0 {
		t.Fatalf("hidden after a restart: %v", hidden)
	}

	// Only a hidden ID can be unhidden; anything else is 404 and changes nothing.
	before := e.overlayFile()
	for _, id := range []string{"cat", "no-such-agent"} {
		resp, out := e.do("POST", "/api/catalog/"+id+"/unhide", adminToken, nil)
		if resp.StatusCode != http.StatusNotFound || out["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("unhide %s: %d %v", id, resp.StatusCode, out)
		}
	}
	if got := e.overlayFile(); got != before {
		t.Fatalf("a refused unhide changed catalog.json:\n%s\nwas:\n%s", got, before)
	}
}

// A hand-edited catalog.json may hide an ID that no agent has, or repeat one.
// The list shows each ID once, and unhiding such an ID clears it with no agent
// in the reply.
func TestCatalogUnhideClearsAnIDWithNoAgent(t *testing.T) {
	e := newTestEnv(t, nil)
	file := `{"agents": [], "hidden": ["ghost", "sh", "ghost"]}`
	if err := os.WriteFile(filepath.Join(e.srv.store.Dir(), "catalog.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	e = e.restart()
	if hidden := e.catalogHidden(); !slices.Equal(hidden, []string{"ghost", "sh"}) {
		t.Fatalf("hidden: %v", hidden)
	}
	resp, out := e.do("POST", "/api/catalog/ghost/unhide", adminToken, nil)
	if _, has := out["agent"]; resp.StatusCode != http.StatusOK || has {
		t.Fatalf("unhide ghost: %d %v", resp.StatusCode, out)
	}
	if hidden := e.catalogHidden(); !slices.Equal(hidden, []string{"sh"}) {
		t.Fatalf("hidden after clearing ghost: %v", hidden)
	}
	if ids := e.catalogIDs(); !slices.Equal(ids, []string{"cat", "exit"}) {
		t.Fatalf("catalog: %v", ids)
	}
}

func TestCatalogEditingNeedsAStore(t *testing.T) {
	e := newTestEnv(t, nil)
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ro := e.serve(srv)
	for _, r := range []struct {
		method, path string
		body         any
	}{{"POST", "/api/catalog", agentBody("x")}, {"DELETE", "/api/catalog/cat", nil}, {"POST", "/api/catalog/cat/unhide", nil}, {"POST", "/api/catalog/cat/clone", agentBody("x")}} {
		resp, out := ro.do(r.method, r.path, adminToken, r.body)
		if resp.StatusCode != http.StatusServiceUnavailable || out["error"].(map[string]any)["code"] != "store_unavailable" {
			t.Errorf("%s %s: %d %v", r.method, r.path, resp.StatusCode, out)
		}
	}
	if ids := ro.catalogIDs(); !slices.Equal(ids, []string{"cat", "sh", "exit"}) {
		t.Fatalf("read-only catalog: %v", ids)
	}
	if hidden := ro.catalogHidden(); len(hidden) != 0 {
		t.Fatalf("read-only catalog hides %v", hidden)
	}
	if resp, out := ro.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"/bin/sh"}}); resp.StatusCode != http.StatusOK || out["found"] != true {
		t.Fatalf("check needs no store: %d %v", resp.StatusCode, out)
	}
}

// A catalog.json that cannot be used must stop startup: if the server ran with
// the configured catalog alone, the next save would overwrite the file.
func TestNewRefusesAnUnusableOverlay(t *testing.T) {
	cases := []struct{ name, file, want string }{
		{"not JSON", `{oops`, "parse catalog.json"},
		{"empty file", ``, "empty document"},
		{"unknown field", `{"agents": [], "bogus": true}`, `"bogus"`},
		{"invalid agent", `{"agents": [{"id": "Bad Id", "name": "x", "command": ["x"]}]}`, "agents[0]"},
		{"invalid signal", `{"agents": [{"id": "ok", "name": "x", "command": ["x"], "signal": {"kind": "smoke"}}]}`, "unknown kind"},
		{"unknown adapter", `{"agents": [{"id": "ok", "name": "x", "command": ["x"], "adapter": "gemini"}]}`, `"gemini"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			path := filepath.Join(e.srv.store.Dir(), "catalog.json")
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store)
			if err == nil || srv != nil {
				t.Fatalf("New accepted the file: %v", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should name %s and contain %q", err, path, tc.want)
			}
			if b, _ := os.ReadFile(path); string(b) != tc.file {
				t.Fatalf("New changed the file: %q", b)
			}
		})
	}
}

func TestCatalogFailedSaveChangesNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	e.save(agentBody("kept"))
	if c := e.del("sh"); c != http.StatusNoContent { // hidden, to try unhiding it below
		t.Fatalf("hide: %d", c)
	}
	before := e.catalogIDs()

	// The data directory disappears, so no save can succeed.
	dir := e.srv.store.Dir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	attempts := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/catalog", agentBody("lost")},
		{"DELETE", "/api/catalog/cat", nil},
		{"DELETE", "/api/catalog/kept", nil},
		{"POST", "/api/catalog/sh/unhide", nil},
	}
	for _, a := range attempts {
		resp, out := e.do(a.method, a.path, adminToken, a.body)
		apiErr, _ := out["error"].(map[string]any)
		if resp.StatusCode != http.StatusInternalServerError || apiErr["code"] != "store_failed" || strings.Contains(fmt.Sprint(apiErr["message"]), dir) {
			t.Errorf("%s %s: %d %v", a.method, a.path, resp.StatusCode, out)
		}
	}
	if got := e.catalogIDs(); !slices.Equal(got, before) {
		t.Fatalf("a failed save changed the catalog: %v, want %v", got, before)
	}
	if hidden := e.catalogHidden(); !slices.Equal(hidden, []string{"sh"}) {
		t.Fatalf("a failed unhide changed the hidden list: %v", hidden)
	}

	// Once the directory is back, the next save writes what the server still
	// holds plus the new agent, and nothing from the failed attempts.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.save(agentBody("back"))
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 2 || ov.Agents[0].ID != "kept" || ov.Agents[1].ID != "back" || !slices.Equal(ov.Hidden, []string{"sh"}) {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
}

func TestCatalogConcurrentEditsAreAllKept(t *testing.T) {
	e := newTestEnv(t, nil)
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			b, _ := json.Marshal(agentBody(fmt.Sprintf("agent-%d", i)))
			req, _ := http.NewRequest("POST", e.http.URL+"/api/catalog", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+adminToken)
			resp, err := e.client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("agent-%d: status %d", i, resp.StatusCode)
			}
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	// Meanwhile readers see whole catalogs: no duplicates and no torn lists,
	// and launches keep working.
	launched := false
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		ids := e.catalogIDs()
		if unique := slices.Compact(slices.Sorted(slices.Values(ids))); len(unique) != len(ids) || len(ids) < 3 {
			t.Fatalf("inconsistent catalog: %v", ids)
		}
		if !launched {
			e.createSession("cat")
			launched = true
		}
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != writers {
		t.Fatalf("overlay keeps %d of %d agents: %v %v", len(ov.Agents), writers, ok, err)
	}
	if ids := e.catalogIDs(); len(ids) != 3+writers {
		t.Fatalf("catalog lists %d agents, want %d: %v", len(ids), 3+writers, ids)
	}
}

// Readers and launches take the catalog lock only for the snapshot: an edit
// writing catalog.json (it holds the editor lock across the fsync) makes none
// of them wait.
func TestCatalogReadersDoNotWaitForAnEdit(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.catalogEditMu.Lock() // an edit in the middle of its write
	defer e.srv.catalogEditMu.Unlock()
	statuses := make(chan int, 2)
	go func() {
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/catalog", nil},
			{"POST", "/api/sessions", map[string]any{"agentId": "cat"}},
		} {
			var rd io.Reader
			if r.body != nil {
				b, _ := json.Marshal(r.body)
				rd = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(r.method, e.http.URL+r.path, rd)
			req.Header.Set("Authorization", "Bearer "+adminToken)
			resp, err := e.client.Do(req)
			if err != nil {
				statuses <- 0
				continue
			}
			resp.Body.Close()
			statuses <- resp.StatusCode
		}
	}()
	for _, want := range []int{http.StatusOK, http.StatusCreated} {
		select {
		case got := <-statuses:
			if got != want {
				t.Fatalf("status %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a reader waited for the editor lock")
		}
	}
	e.stopEverything(t)
}

// The catalog lock is not held across the write of catalog.json: while an
// edit's write is held, a list answers at once with the catalog as it was,
// and the edit publishes once its write completes.
func TestCatalogListAnswersWhileAnEditWrites(t *testing.T) {
	e := newTestEnv(t, nil)
	writing, release := make(chan struct{}), make(chan struct{})
	// Every way out of the test lets the held write go, a failure before the
	// write was reached included, so the handler never stays blocked. This
	// cleanup runs before the server's, which waits for its handlers.
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	write := e.srv.writeCatalog
	e.srv.writeCatalog = func(ov catalog.Overlay) error {
		close(writing)
		<-release
		return write(ov)
	}
	request := func(method, path string, body any) <-chan int {
		status := make(chan int, 1)
		go func() {
			var rd io.Reader
			if body != nil {
				b, _ := json.Marshal(body)
				rd = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(method, e.http.URL+path, rd)
			req.Header.Set("Authorization", "Bearer "+adminToken)
			resp, err := e.client.Do(req)
			if err != nil {
				status <- 0
				return
			}
			resp.Body.Close()
			status <- resp.StatusCode
		}()
		return status
	}
	saved := request("POST", "/api/catalog", agentBody("slow"))
	select {
	case <-writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the edit never reached its write")
	}
	select {
	case got := <-request("GET", "/api/catalog", nil):
		if got != http.StatusOK {
			t.Fatalf("list during the write: status %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a list waited for an edit's write")
	}
	if ids := e.catalogIDs(); slices.Contains(ids, "slow") {
		t.Fatalf("listed before its write completed: %v", ids)
	}
	unblock()
	if got := <-saved; got != http.StatusOK {
		t.Fatalf("save: status %d", got)
	}
	if ids := e.catalogIDs(); !slices.Contains(ids, "slow") {
		t.Fatalf("not listed once its write completed: %v", ids)
	}
}

func TestCatalogSavedAgentsRun(t *testing.T) {
	e := newTestEnv(t, nil)
	body := agentBody("mycat")
	body["env"] = map[string]string{"TOKEN": "s3cret"}
	out := e.save(body)
	// The response and the listing mask env values; the overlay keeps them so
	// the agent still gets its real environment.
	if env := out["agent"].(map[string]any)["env"].(map[string]any); env["TOKEN"] != "***" {
		t.Fatalf("save response env: %v", env)
	}
	if env := e.catalogAgent("mycat")["env"].(map[string]any); env["TOKEN"] != "***" {
		t.Fatalf("catalog env: %v", env)
	}
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || ov.Agents[0].Env["TOKEN"] != "s3cret" {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	e.createSession("mycat")

	// A command that is missing on the server can still be saved (a host may
	// have it); launching it here fails with the usual start_failed.
	e.save(map[string]any{"id": "ghost", "name": "Ghost", "command": []string{"definitely-not-a-real-binary-xyz"}})
	if _, out := e.do("POST", "/api/catalog/check", adminToken, map[string]any{"command": []string{"definitely-not-a-real-binary-xyz"}}); out["found"] != false {
		t.Fatalf("check: %v", out)
	}
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "ghost"})
	if resp.StatusCode != http.StatusBadGateway || out["error"].(map[string]any)["code"] != "start_failed" {
		t.Fatalf("launch of a missing command: %d %v", resp.StatusCode, out)
	}
}

// A saved agent's envPassthrough lets its sessions inherit those server
// variables on top of the server-wide list. Other agents do not get them, and
// CONDUCTOR_* variables stay out even when listed. Every session gets
// CONDUCTOR_BIN, the binary the hooks run, from Conductor itself: neither the
// server's environment nor the agent's env can set it.
func TestCatalogEnvPassthroughReachesTheSession(t *testing.T) {
	t.Setenv("PASS_PROBE_AGENT", "from-agent-list")
	t.Setenv("PASS_PROBE_SERVER", "from-server-list")
	t.Setenv("PASS_PROBE_NONE", "never-listed")
	t.Setenv("CONDUCTOR_PROBE_SECRET", "server-only")
	t.Setenv("CONDUCTOR_BIN", "/from/the/server/env")
	e := newTestEnv(t, func(c *config.Config) { c.EnvPassthrough = []string{"PASS_PROBE_SERVER", "CONDUCTOR_BIN"} })
	bin, err := agents.Binary()
	if err != nil || !filepath.IsAbs(bin) {
		t.Fatalf("binary %q %v", bin, err)
	}
	script := `echo "A=[$PASS_PROBE_AGENT] S=[$PASS_PROBE_SERVER] N=[$PASS_PROBE_NONE] C=[$CONDUCTOR_PROBE_SECRET] B=[$CONDUCTOR_BIN] END"; exec /bin/cat`
	with := agentBody("with")
	with["command"] = []string{"/bin/sh", "-c", script}
	with["envPassthrough"] = []string{"PASS_PROBE_AGENT", "CONDUCTOR_PROBE_SECRET", "CONDUCTOR_BIN"}
	with["env"] = map[string]string{"CONDUCTOR_BIN": "/from/the/agent"}
	e.save(with)
	without := agentBody("without")
	without["command"] = []string{"/bin/sh", "-c", script}
	e.save(without)

	// "without" runs after "with", so a launch that grew the server-wide list
	// would show here.
	for _, tc := range []struct{ agent, want string }{
		{"with", "A=[from-agent-list] S=[from-server-list] N=[] C=[] B=[" + bin + "] END"},
		{"without", "A=[] S=[from-server-list] N=[] C=[] B=[" + bin + "] END"},
	} {
		id := e.createSession(tc.agent)
		c := dialViewer(t, e, id, adminToken)
		c.hello(80, 24)
		c.expectOutput(tc.want)
	}
}

// GET /api/catalog masks env values, so a client that edits an agent it read
// there sends the mask back to mean "unchanged". Saving that must keep the
// stored value and never write the mask into catalog.json.
func TestCatalogSaveKeepsMaskedEnvValues(t *testing.T) {
	e := newTestEnv(t, nil)
	body := agentBody("keyed")
	body["env"] = map[string]string{"API_KEY": "secret", "REGION": "eu"}
	e.save(body)

	// The listing masks every value.
	listed := e.catalogAgent("keyed")
	if env := listed["env"].(map[string]any); len(env) != 2 || env["API_KEY"] != "***" || env["REGION"] != "***" {
		t.Fatalf("listed env: %v", env)
	}
	// wantEnv checks the stored overlay, the running catalog and the file.
	wantEnv := func(want map[string]string) {
		t.Helper()
		var ov catalog.Overlay
		if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 1 || !reflect.DeepEqual(ov.Agents[0].Env, want) {
			t.Fatalf("stored env, want %v: %v %v %+v", want, ok, err, ov)
		}
		if a, _ := e.srv.Catalog().Get("keyed"); !reflect.DeepEqual(a.Env, want) {
			t.Fatalf("running env, want %v: %v", want, a.Env)
		}
		if strings.Contains(e.overlayFile(), "***") {
			t.Fatalf("the mask reached catalog.json: %s", e.overlayFile())
		}
	}

	// Edit the description and send the agent back exactly as it was read.
	listed["description"] = "edited"
	out := e.save(listed)
	if env := out["agent"].(map[string]any)["env"].(map[string]any); env["API_KEY"] != "***" || env["REGION"] != "***" {
		t.Fatalf("save response env: %v", env)
	}
	wantEnv(map[string]string{"API_KEY": "secret", "REGION": "eu"})
	if a, _ := e.srv.Catalog().Get("keyed"); a.Description != "edited" {
		t.Fatalf("the edit was not saved: %+v", a)
	}

	// A real value replaces the old one, a masked one keeps its own, and a key
	// that is left out is dropped.
	listed["env"] = map[string]any{"API_KEY": "rotated", "REGION": "***"}
	e.save(listed)
	wantEnv(map[string]string{"API_KEY": "rotated", "REGION": "eu"})
	listed["env"] = map[string]any{"API_KEY": "***"}
	e.save(listed)
	wantEnv(map[string]string{"API_KEY": "rotated"})

	// A masked value has nothing to keep for a key the agent never had, or for
	// an agent that is not stored yet: nothing is saved, and with several such
	// keys the first by name is reported. The requests are repeated because a
	// wrong answer here would depend on map order.
	withEnv := func(id string, env map[string]any) map[string]any {
		b := agentBody(id)
		b["env"] = env
		return b
	}
	cases := []struct {
		name string
		body map[string]any
		key  string
	}{
		{"new key on a stored agent", withEnv("keyed", map[string]any{"API_KEY": "***", "NEW_KEY": "***"}), "NEW_KEY"},
		{"agent that is not stored yet", withEnv("fresh", map[string]any{"API_KEY": "***"}), "API_KEY"},
		{"several keys", withEnv("keyed", map[string]any{"ZED": "***", "MID": "***", "ALPHA": "***", "OMEGA": "***", "BETA": "***", "GAMMA": "***", "DELTA": "***", "SIGMA": "***"}), "ALPHA"},
	}
	before := e.overlayFile()
	for range 20 {
		for _, tc := range cases {
			resp, out := e.do("POST", "/api/catalog", adminToken, tc.body)
			apiErr, _ := out["error"].(map[string]any)
			if resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "invalid_agent" || apiErr["message"] != "env "+tc.key+": value is redacted; set a real value" {
				t.Fatalf("%s: %d %v", tc.name, resp.StatusCode, out)
			}
		}
	}
	if got := e.overlayFile(); got != before {
		t.Fatalf("a rejected save changed catalog.json:\n%s\nwas:\n%s", got, before)
	}
	wantEnv(map[string]string{"API_KEY": "rotated"})
	if e.catalogAgent("fresh") != nil {
		t.Fatal("a rejected agent was added")
	}
}

// A hand-edited catalog.json may repeat an ID; the last entry wins at startup,
// so a save has to replace every copy or a restart would bring an old one back.
func TestCatalogSaveCollapsesDuplicateOverlayEntries(t *testing.T) {
	e := newTestEnv(t, nil)
	dup := `{"agents": [
  {"id": "dup", "name": "first", "command": ["a"]},
  {"id": "dup", "name": "second", "command": ["b"]}
]}`
	if err := os.WriteFile(filepath.Join(e.srv.store.Dir(), "catalog.json"), []byte(dup), 0o600); err != nil {
		t.Fatal(err)
	}
	e = e.restart()
	if a := e.catalogAgent("dup"); a == nil || a["name"] != "second" {
		t.Fatalf("startup: %v", a)
	}
	e.save(map[string]any{"id": "dup", "name": "third", "command": []string{"c"}})
	if a := e.catalogAgent("dup"); a["name"] != "third" {
		t.Fatalf("after save: %v", a)
	}
	if a := e.restart().catalogAgent("dup"); a["name"] != "third" {
		t.Fatalf("after restart: %v", a)
	}
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 1 {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
}

// The Agents page stores, for an agent that replaces a built-in or configured
// one, only the env values the admin changed; the others stay the mask, read
// as the replaced agent's value. So a secret rotated in the config reaches the
// agent at the next start, a value the admin set stays, and a key the admin
// removed stays removed. The adapter and signal left out come from the
// replaced agent too.
func TestCatalogOverrideFollowsTheBaseEnv(t *testing.T) {
	e := newTestEnv(t, nil)
	start := func(secret string) *testEnv {
		t.Helper()
		base, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
			{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}},
			{ID: "keyed", Name: "keyed", Command: []string{"/bin/cat"}, Adapter: "claude",
				Signal: &catalog.Signal{Kind: catalog.SignalPattern, Pattern: `^> $`},
				Env:    map[string]string{"API_KEY": secret, "REGION": "eu", "DEBUG": "1"}},
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
	listed := v1.catalogAgent("keyed")
	if listed["source"] != "config" {
		t.Fatalf("listed %v", listed)
	}
	// The admin edits the description and REGION, leaves API_KEY as read,
	// removes DEBUG, and sends neither adapter nor signal. source travels back
	// as it was read.
	listed["description"] = "edited"
	listed["env"] = map[string]any{"API_KEY": "***", "REGION": "us"}
	delete(listed, "adapter")
	delete(listed, "signal")
	out := v1.save(listed)
	if a := out["agent"].(map[string]any); a["source"] != "saved" || a["replaces"] != "config" {
		t.Fatalf("saved %v", a)
	}
	var ov catalog.Overlay
	if ok, err := v1.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 1 ||
		!reflect.DeepEqual(ov.Agents[0].Env, map[string]string{"API_KEY": "***", "REGION": "us"}) || ov.Agents[0].Adapter != "" || ov.Agents[0].Signal != nil {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	if strings.Contains(v1.overlayFile(), `"v1"`) {
		t.Fatalf("the secret was copied into catalog.json:\n%s", v1.overlayFile())
	}
	check := func(env *testEnv, secret string) {
		t.Helper()
		a, _ := env.srv.Catalog().Get("keyed")
		if a.Description != "edited" || !reflect.DeepEqual(a.Env, map[string]string{"API_KEY": secret, "REGION": "us"}) ||
			a.Adapter != "claude" || a.Signal == nil || a.Signal.Kind != catalog.SignalPattern {
			t.Fatalf("keyed runs with %+v (signal %+v)", a, a.Signal)
		}
	}
	check(v1, "v1")
	// The config rotates the secret: the next start reads it, through the same catalog.json.
	check(start("v2"), "v2")
}

// A value sent equal to the configured one, typed or kept as read, is stored
// as the mask, as ApplyOverlay reads it: a later rotation reaches the agent,
// while a value of the admin's own stays.
func TestCatalogSaveMasksValuesEqualToTheBase(t *testing.T) {
	e := newTestEnv(t, nil)
	start := func(secret string) *testEnv {
		t.Helper()
		srv, err := New(e.srv.cfg, keyedBase(t, secret), e.srv.log, nil, e.srv.store)
		if err != nil {
			t.Fatal(err)
		}
		return e.serve(srv)
	}
	v1 := start("v1")
	listed := v1.catalogAgent("keyed")
	listed["env"] = map[string]any{"API_KEY": "v1", "REGION": "other", "TOKEN": "***"}
	v1.save(listed)
	var ov catalog.Overlay
	if ok, err := v1.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 1 ||
		!reflect.DeepEqual(ov.Agents[0].Env, map[string]string{"API_KEY": "***", "REGION": "other", "TOKEN": "***"}) {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	if raw := v1.overlayFile(); strings.Contains(raw, `"v1"`) || strings.Contains(raw, `"t1"`) {
		t.Fatalf("a configured value was copied into catalog.json:\n%s", raw)
	}
	keyedRunsWith(t, v1.srv, map[string]string{"API_KEY": "v1", "REGION": "other", "TOKEN": "t1"})
	// The config rotates the secret: the next start reads it; REGION stays the admin's.
	keyedRunsWith(t, start("v2").srv, map[string]string{"API_KEY": "v2", "REGION": "other", "TOKEN": "t1"})
}

// keyedBase is a configured catalog of "cat" and "keyed", whose API_KEY is
// secret.
func keyedBase(t *testing.T, secret string) catalog.Catalog {
	t.Helper()
	base, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
		{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}},
		{ID: "keyed", Name: "keyed", Command: []string{"/bin/cat"}, Env: map[string]string{"API_KEY": secret, "REGION": "eu", "TOKEN": "t1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// keyedRunsWith requires srv's catalog to give "keyed" the env want.
func keyedRunsWith(t *testing.T, srv *Server, want map[string]string) {
	t.Helper()
	if a, _ := srv.Catalog().Get("keyed"); !reflect.DeepEqual(a.Env, want) {
		t.Fatalf("keyed runs with %v, want %v", a.Env, want)
	}
}

// An override an earlier version saved holds its env values in full. At start
// the server stores the mask for every value equal to the configured agent's,
// once, and says which agents it changed: a secret rotated after the upgrade
// reaches the agent with no save, while a value of the admin's own, and an
// agent that replaces nothing, stay as they are.
func TestNewMasksConfiguredValuesInSavedOverrides(t *testing.T) {
	e := newTestEnv(t, nil)
	path := filepath.Join(e.srv.store.Dir(), "catalog.json")
	earlier := `{"agents": [
  {"id": "keyed", "name": "keyed", "command": ["/bin/cat"], "env": {"API_KEY": "v1", "REGION": "other", "TOKEN": "t1"}},
  {"id": "mine", "name": "mine", "command": ["/bin/cat"], "env": {"API_KEY": "v1"}}
], "hidden": ["cat"]}`
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	srv, err := New(e.srv.cfg, keyedBase(t, "v1"), slog.New(slog.NewTextHandler(&logs, nil)), nil, e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	endAtCleanup(t, srv)
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 2 ||
		!reflect.DeepEqual(ov.Agents[0].Env, map[string]string{"API_KEY": "***", "REGION": "other", "TOKEN": "***"}) ||
		!reflect.DeepEqual(ov.Agents[1].Env, map[string]string{"API_KEY": "v1"}) || !slices.Equal(ov.Hidden, []string{"cat"}) {
		t.Fatalf("catalog.json after the start: %v %v %+v", ok, err, ov)
	}
	if n := strings.Count(logs.String(), "agents=[keyed]"); n != 1 {
		t.Fatalf("%d log lines name keyed:\n%s", n, logs.String())
	}
	keyedRunsWith(t, srv, map[string]string{"API_KEY": "v1", "REGION": "other", "TOKEN": "t1"})
	if ids := idsOf(srv.Catalog()); ids != "keyed,mine" {
		t.Fatalf("catalog %s", ids)
	}
	// The config rotates the secret: the next start reads it, and has nothing
	// left to mask, so it writes nothing.
	before, _ := os.ReadFile(path)
	again, err := New(e.srv.cfg, keyedBase(t, "v2"), e.srv.log, nil, e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	endAtCleanup(t, again)
	keyedRunsWith(t, again, map[string]string{"API_KEY": "v2", "REGION": "other", "TOKEN": "t1"})
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatalf("a start with nothing to mask rewrote catalog.json:\n%s", after)
	}
}

// idsOf lists a catalog's agent IDs in order, comma separated.
func idsOf(c catalog.Catalog) string {
	var ids []string
	for _, a := range c.List() {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ",")
}

// An override whose values are its own is left as it is: the start does not
// write catalog.json.
func TestNewLeavesAnOverrideOfItsOwnAlone(t *testing.T) {
	e := newTestEnv(t, nil)
	path := filepath.Join(e.srv.store.Dir(), "catalog.json")
	// Compact, as no save writes it: a rewrite would change the bytes.
	own := `{"agents":[{"id":"keyed","name":"keyed","command":["/bin/cat"],"env":{"API_KEY":"other","REGION":"***"}}]}`
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	srv, err := New(e.srv.cfg, keyedBase(t, "v1"), e.srv.log, nil, e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	endAtCleanup(t, srv)
	keyedRunsWith(t, srv, map[string]string{"API_KEY": "other", "REGION": "eu"})
	fi, err := os.Stat(path)
	if b, _ := os.ReadFile(path); err != nil || string(b) != own || !fi.ModTime().Equal(old) {
		t.Fatalf("catalog.json was written: %v %s %v", err, b, fi.ModTime())
	}
}

// A start that has values to mask but cannot save catalog.json stops, with
// the store's error, and leaves the file as it was.
func TestNewFailsWhenTheMaskedOverlayCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory of mode 0500")
	}
	e := newTestEnv(t, nil)
	dir := e.srv.store.Dir()
	path := filepath.Join(dir, "catalog.json")
	earlier := `{"agents": [{"id": "keyed", "name": "keyed", "command": ["/bin/cat"], "env": {"API_KEY": "v1"}}]}`
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	srv, err := New(e.srv.cfg, keyedBase(t, "v1"), e.srv.log, nil, e.srv.store)
	if err == nil || srv != nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), path) {
		t.Fatalf("New: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != earlier {
		t.Fatalf("catalog.json changed: %s", b)
	}
}

func TestCatalogListsWhereEachAgentComesFrom(t *testing.T) {
	e := newTestEnv(t, nil) // cat, sh and exit come from the config
	e.save(map[string]any{"id": "cat", "name": "Cat mk2", "command": []string{"/bin/cat"}})
	e.save(agentBody("aider"))
	for id, want := range map[string][2]any{"cat": {"saved", "config"}, "sh": {"config", nil}, "exit": {"config", nil}, "aider": {"saved", nil}} {
		if a := e.catalogAgent(id); a["source"] != want[0] || a["replaces"] != want[1] {
			t.Errorf("%s: source %v, replaces %v; want %v", id, a["source"], a["replaces"], want)
		}
	}
	if c := e.del("sh"); c != http.StatusNoContent {
		t.Fatalf("hide: %d", c)
	}
	resp, out := e.do("POST", "/api/catalog/sh/unhide", adminToken, nil)
	if a, _ := out["agent"].(map[string]any); resp.StatusCode != http.StatusOK || a["source"] != "config" {
		t.Fatalf("unhide: %d %v", resp.StatusCode, out)
	}
	// An agent the catalog no longer lists has no source, rather than "".
	if b, err := json.Marshal(entry(catalog.Agent{ID: "gone", Name: "gone", Command: []string{"x"}}, e.srv.Catalog(), e.srv.base, func(string) bool { return false }, nil)); err != nil || strings.Contains(string(b), `"source"`) {
		t.Fatalf("entry of an unlisted agent: %s %v", b, err)
	}
}

// A built-in that a saved agent replaces launches as saved, with the signal
// the saved agent left out taken from the built-in.
func TestCatalogLaunchesAnOverriddenBuiltIn(t *testing.T) {
	e := newTestEnv(t, nil)
	srv, err := New(e.srv.cfg, catalog.Default(), e.srv.log, nil, e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	b := e.serve(srv)
	argv := []string{"/bin/sh", "-c", "echo OVERRIDE-RAN; exec /bin/cat"}
	b.save(map[string]any{"id": "shell", "name": "Shell", "command": argv})
	if a := b.catalogAgent("shell"); a["source"] != "saved" || a["replaces"] != "built-in" {
		t.Fatalf("listed %v", a)
	}
	if a, _ := b.srv.Catalog().Get("shell"); a.Signal == nil || a.Signal.Kind != catalog.SignalNone {
		t.Fatalf("the override lost the built-in's signal: %+v", a.Signal)
	}
	id := b.createSession("shell")
	c := dialViewer(t, b, id, adminToken)
	c.hello(80, 24)
	c.expectOutput("OVERRIDE-RAN")
	if d, _ := b.srv.registry.Get(id); !slices.Equal(d.Info().Command, argv) {
		t.Fatalf("command %q", d.Info().Command)
	}
}

// Every agent can be hidden: the catalog is then empty, launches are refused
// as for an unknown agent, a restart comes up empty, and Restore brings one back.
func TestCatalogHidesDownToNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, id := range []string{"cat", "sh", "exit"} {
		if c := e.del(id); c != http.StatusNoContent {
			t.Fatalf("hide %s: %d", id, c)
		}
	}
	if ids := e.catalogIDs(); len(ids) != 0 {
		t.Fatalf("listed %v", ids)
	}
	if hidden := e.catalogHidden(); !slices.Equal(hidden, []string{"cat", "sh", "exit"}) {
		t.Fatalf("hidden %v", hidden)
	}
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"})
	if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_agent" {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	again := e.restart()
	if ids := again.catalogIDs(); len(ids) != 0 {
		t.Fatalf("after a restart: %v", ids)
	}
	if resp, _ := again.do("POST", "/api/catalog/sh/unhide", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unhide: %d", resp.StatusCode)
	}
	if ids := again.catalogIDs(); !slices.Equal(ids, []string{"sh"}) {
		t.Fatalf("after unhiding sh: %v", ids)
	}
}

// An agent the Agents page added is deleted outright: not hidden, gone from
// catalog.json, and unknown to a second delete.
func TestCatalogDeletesAnAgentItAdded(t *testing.T) {
	e := newTestEnv(t, nil)
	e.save(agentBody("aider"))
	if c := e.del("aider"); c != http.StatusNoContent {
		t.Fatalf("delete: %d", c)
	}
	if e.catalogAgent("aider") != nil || slices.Contains(e.catalogHidden(), "aider") {
		t.Fatal("aider is still listed or hidden")
	}
	var ov catalog.Overlay
	if ok, err := e.srv.store.Load("catalog.json", &ov); !ok || err != nil || len(ov.Agents) != 0 || len(ov.Hidden) != 0 {
		t.Fatalf("overlay: %v %v %+v", ok, err, ov)
	}
	if c := e.del("aider"); c != http.StatusNotFound {
		t.Fatalf("second delete: %d", c)
	}
}

func TestEventsRouteRecordsAndStreams(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	events := e.sse(t) // the admin stream, subscribed before anything is posted
	resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "artifact", "message": "PR opened", "url": "https://github.com/x/y/pull/1"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if out["accepted"] != true {
		t.Fatalf("reply %v", out)
	}
	e.waitEvent(t, events, func(ev string) bool {
		return strings.HasPrefix(ev, "activity ") && strings.Contains(ev, `"type":"artifact"`) && strings.Contains(ev, id)
	})
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "bogus"}); resp.StatusCode != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_type" {
		t.Fatalf("bogus: %d %v", resp.StatusCode, out)
	}
	// attention types still update attention
	e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "needs_input", "message": "?"})
	_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	if got["session"].(map[string]any)["attention"].(map[string]any)["state"] != "needs_input" {
		t.Fatal("needs_input via /events did not apply")
	}
}

// A hook that floods the route costs the session its excess (the bucket
// answers 429) and nobody else anything: the admin stream stays connected
// and keeps delivering.
func TestEventsFloodKeepsSSEClient(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	events := e.sse(t)
	limited := 0
	for i := 0; i < 1000; i++ {
		resp, _ := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "progress", "message": "n"})
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("1000 events in a row were never rate limited")
	}
	// The client must still receive a later event. The flood emptied the
	// session's bucket, which earns a token every 50 ms, so the next event is
	// admitted after a moment.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "error", "message": "last"})
		if resp.StatusCode == http.StatusAccepted {
			break
		}
		if resp.StatusCode != http.StatusTooManyRequests || time.Now().After(deadline) {
			t.Fatalf("later event: %d %v", resp.StatusCode, out)
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.waitEvent(t, events, func(ev string) bool { return strings.Contains(ev, `"message":"last"`) })
}

func TestEventsRouteAnswersRateLimitedWhenTheBucketIsEmpty(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	d, _ := e.srv.registry.Get(id)
	local := d.(*session.Local)
	var last map[string]any
	for i := 0; i < 5*session.EventBurst; i++ {
		resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "tool_use", "tool": "Bash"})
		if resp.StatusCode == http.StatusTooManyRequests {
			last = out
			break
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("event %d: %d %v", i, resp.StatusCode, out)
		}
	}
	if errorCode(last) != "rate_limited" {
		t.Fatalf("never limited, or not as rate_limited: %v", last)
	}
	if local.Dropped() == 0 {
		t.Fatal("the session did not count the dropped event")
	}
}

// A server session's attention words spend the token a hosted session's do
// (TestAttentionWordsAreLimitedOnBothRoutesForAHostedSession): through either
// route, a word the bucket has no token for is refused whole with a 429, and
// leaves neither its state nor its entry behind. Every word accepted leaves
// exactly one entry.
func TestAttentionWordsAreLimitedOnBothRoutesForAServerSession(t *testing.T) {
	routes := []struct {
		name, suffix, field string
		ok                  int
	}{
		{"attention route", "/attention", "state", http.StatusOK},
		{"events route", "/events", "type", http.StatusAccepted},
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			id := e.createSession("cat")
			d, _ := e.srv.registry.Get(id)
			local := d.(*session.Local)
			agent := e.agentToken(id)
			path := "/api/sessions/" + id + r.suffix
			states := []string{"working", "done"}

			accepted := 0
			var refused map[string]any
			for i := 0; i < 5*session.EventBurst; i++ {
				resp, out := e.do("POST", path, agent, map[string]any{r.field: states[i%2], "message": fmt.Sprint("n", i)})
				if resp.StatusCode == http.StatusTooManyRequests {
					refused = out
					break
				}
				if resp.StatusCode != r.ok {
					t.Fatalf("word %d: %d %v", i, resp.StatusCode, out)
				}
				accepted++
			}
			if errorCode(refused) != "rate_limited" {
				t.Fatalf("%d attention words in a row were never refused as rate_limited: %v", accepted, refused)
			}
			if accepted < session.EventBurst || accepted > session.EventBurst+session.EventRatePerSecond {
				t.Fatalf("accepted %d attention words before the first 429, want about %d", accepted, session.EventBurst)
			}
			if att := d.Info().Attention; att.Message != fmt.Sprint("n", accepted-1) {
				t.Fatalf("attention %+v: the refused word must not be applied", att)
			}
			n := 0
			for _, en := range local.Activity() {
				if en.Type != session.ActivityAttention {
					continue
				}
				if en.Message == fmt.Sprint("n", accepted) {
					t.Fatalf("the refused word left its entry %+v", en)
				}
				n++
			}
			if n != accepted {
				t.Fatalf("%d attention entries for %d accepted words, want one each", n, accepted)
			}
		})
	}
}

// Claude Code with tool events on spends the burst on a fast subagent, then
// asks for permission. The report is refused whole, with a 429 on either
// route, until the bucket has a token for it; then its state and its entry
// arrive together, in the session's log and on the admin stream, which feeds
// the Events page and the webhooks. A badge never shows a prompt they missed.
func TestAToolBurstCannotHideThePromptAfterIt(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	d, _ := e.srv.registry.Get(id)
	local := d.(*session.Local)
	agent := e.agentToken(id)
	events := e.sse(t)
	base := "/api/sessions/" + id

	burst := func() {
		t.Helper()
		for i := 0; ; i++ {
			resp, out := e.do("POST", base+"/events", agent, map[string]any{"type": "tool_use", "tool": "Bash"})
			if resp.StatusCode == http.StatusTooManyRequests {
				return
			}
			if resp.StatusCode != http.StatusAccepted || i > 5*session.EventBurst {
				t.Fatalf("tool event %d: %d %v", i, resp.StatusCode, out)
			}
		}
	}
	attentionEntries := func() []session.ActivityEntry {
		var entries []session.ActivityEntry
		for _, en := range local.Activity() {
			if en.Type == session.ActivityAttention {
				entries = append(entries, en)
			}
		}
		return entries
	}
	// The bucket refills continuously, so the report that follows a burst may
	// find the token the round trip earned. Either answer keeps the invariant
	// this test guards: a refused report changes nothing, and an accepted one
	// applies its state and records its entry together. A state without its
	// entry is the failure.
	var applied []string
	for _, r := range []struct{ suffix, field string }{{"/attention", "state"}, {"/events", "type"}} {
		burst()
		message := "via " + r.suffix
		before := len(applied)
		resp, out := e.do("POST", base+r.suffix, agent, map[string]any{r.field: "needs_input", "message": message, "kind": "permission"})
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			if errorCode(out) != "rate_limited" {
				t.Fatalf("needs_input via %s after a burst: %d %v, want rate_limited", r.suffix, resp.StatusCode, out)
			}
			if att := d.Info().Attention; att.Message == message {
				t.Fatalf("a report refused via %s was applied: %+v", r.suffix, att)
			}
		case http.StatusOK, http.StatusAccepted:
			applied = append(applied, message)
			if att := d.Info().Attention; att.State != session.AttentionNeedsInput || att.Message != message || att.Kind != session.KindPermission {
				t.Fatalf("a report accepted via %s did not apply: %+v", r.suffix, att)
			}
		default:
			t.Fatalf("needs_input via %s after a burst: %d %v", r.suffix, resp.StatusCode, out)
		}
		if entries := attentionEntries(); len(entries) != len(applied) || (len(applied) > before && entries[len(entries)-1].Message != message) {
			t.Fatalf("after the report via %s the attention entries are %+v, want one per applied report %v", r.suffix, entries, applied)
		}
	}

	// The bucket earns a token every 50 ms.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, out := e.do("POST", base+"/attention", agent, map[string]any{"state": "needs_input", "message": "Allow Bash?", "kind": "permission"})
		if resp.StatusCode == http.StatusOK {
			break
		}
		if resp.StatusCode != http.StatusTooManyRequests || time.Now().After(deadline) {
			t.Fatalf("needs_input once the bucket refills: %d %v", resp.StatusCode, out)
		}
		time.Sleep(25 * time.Millisecond)
	}
	applied = append(applied, "Allow Bash?")
	if att := d.Info().Attention; att.State != session.AttentionNeedsInput || att.Message != "Allow Bash?" || att.Kind != session.KindPermission {
		t.Fatalf("attention %+v", att)
	}
	entries := attentionEntries()
	if len(entries) != len(applied) {
		t.Fatalf("attention entries %+v, want one per applied report %v", entries, applied)
	}
	for i, en := range entries {
		if en.Message != applied[i] {
			t.Fatalf("attention entry %d is %+v, want %q", i, en, applied[i])
		}
	}
	// The stream carries exactly the applied reports, in order: the refused
	// ones sent none.
	for _, want := range applied {
		if got := activityPayload(t, e.waitEvent(t, events, isActivity("attention", id))); got["message"] != want {
			t.Fatalf("the admin stream's next attention entry is %v, want %q", got, want)
		}
	}
}

func TestEventsRouteAuthentication(t *testing.T) {
	e := newTestEnv(t, nil)
	id, other := e.createSession("cat"), e.createSession("cat")
	agent, otherAgent := e.agentToken(id), e.agentToken(other)
	_, lo := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	linkToken := lo["token"].(string)
	body := map[string]any{"type": "progress", "message": "1/7"}
	path := "/api/sessions/" + id + "/events"

	for name, token := range map[string]string{
		"no token":             "",
		"wrong token":          "nope",
		"another session's":    otherAgent,
		"a host token":         "test-host-token",
		"a share link's token": linkToken,
	} {
		resp, out := e.do("POST", path, token, body)
		if resp.StatusCode != http.StatusUnauthorized || errorCode(out) != "unauthorized" {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	// The credential is read from the Authorization header alone.
	req, _ := http.NewRequest("POST", e.http.URL+path+"?token="+agent, strings.NewReader(`{"type":"progress"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := e.client.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token in the query: %v %v", err, resp)
	}
	for name, token := range map[string]string{"the session's agent token": agent, "the admin token": adminToken} {
		if resp, out := e.do("POST", path, token, body); resp.StatusCode != http.StatusAccepted {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	if resp, out := e.do("POST", "/api/sessions/nosuchsession0000/events", adminToken, body); resp.StatusCode != http.StatusNotFound || errorCode(out) != "not_found" {
		t.Errorf("unknown session: %d %v", resp.StatusCode, out)
	}
}

func TestEventsRouteRejectsWhatItDoesNotAccept(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	path := "/api/sessions/" + id + "/events"

	// Only what an agent reports: the entries a session records itself skip
	// the rate bucket, so an agent must not be able to write them.
	for _, typ := range []string{"", "bogus", "PROGRESS", "attention", "input", "join", "leave", "link", "status", "state"} {
		resp, out := e.do("POST", path, adminToken, map[string]any{"type": typ})
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_type" {
			t.Errorf("type %q: %d %v", typ, resp.StatusCode, out)
		}
	}
	for name, body := range map[string]any{
		"an unknown field":         map[string]any{"type": "progress", "state": "done"},
		"a missing type":           map[string]any{"message": "no type"},
		"a state, not a type":      map[string]any{"state": "needs_input"},
		"a type of the wrong kind": map[string]any{"type": 7},
	} {
		if resp, out := e.do("POST", path, adminToken, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	req, _ := http.NewRequest("POST", e.http.URL+path, strings.NewReader(`{"type":"progress"} {"type":"progress"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	if resp, err := e.client.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("two objects: %v %v", err, resp)
	}

	// Attention types are held to what /attention holds them to.
	tooMany := make([]map[string]any, 33)
	for i := range tooMany {
		tooMany[i] = map[string]any{"label": "x", "input": "1"}
	}
	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"an unknown kind":    {map[string]any{"type": "needs_input", "kind": "bogus"}, "invalid_kind"},
		"too many options":   {map[string]any{"type": "needs_input", "options": tooMany}, "invalid_request"},
		"a message too long": {map[string]any{"type": "done", "message": strings.Repeat("m", 4097)}, "invalid_request"},
	} {
		resp, out := e.do("POST", path, adminToken, c.body)
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != c.code {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
}

func TestEventsRouteRefusesAnEndedSession(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("exit")
	e.waitEnded(id)
	for _, typ := range []string{"progress", "needs_input"} {
		resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": typ})
		if resp.StatusCode != http.StatusConflict || errorCode(out) != "session_ended" {
			t.Errorf("%s on an ended session: %d %v", typ, resp.StatusCode, out)
		}
	}
}

// /events and /attention are two doors to one room for the attention types:
// the same reports leave the same attention behind, whoever makes them.
func TestEventsRouteAttentionTypesMatchTheAttentionRoute(t *testing.T) {
	e := newTestEnv(t, nil)
	viaEvents, viaAttention := e.createSession("cat"), e.createSession("cat")
	tokens := map[string][2]string{ // who reports: the token on each session
		"admin":       {adminToken, adminToken},
		"agent token": {e.agentToken(viaEvents), e.agentToken(viaAttention)},
	}
	options := []map[string]any{{"label": "Yes", "input": "1"}, {"label": "No, explain", "input": "3"}}
	steps := []struct {
		state string
		body  map[string]any
	}{
		{"needs_input", map[string]any{"message": "Allow Bash?", "kind": "permission", "options": options}},
		{"working", map[string]any{"message": "on it"}},
		{"done", map[string]any{"message": "finished", "kind": "done"}},
		{"needs_input", map[string]any{"message": "again", "kind": "prompt"}},
		{"clear", map[string]any{}},
	}
	attention := func(id string) map[string]any {
		_, got := e.do("GET", "/api/sessions/"+id, adminToken, nil)
		att := got["session"].(map[string]any)["attention"].(map[string]any)
		// The stamp and the id are each change's own.
		delete(att, "since")
		delete(att, "id")
		return att
	}
	for who, tok := range tokens {
		for _, st := range steps {
			evBody, atBody := map[string]any{"type": st.state}, map[string]any{"state": st.state}
			for k, v := range st.body {
				evBody[k], atBody[k] = v, v
			}
			if resp, out := e.do("POST", "/api/sessions/"+viaEvents+"/events", tok[0], evBody); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("%s %s via /events: %d %v", who, st.state, resp.StatusCode, out)
			}
			if resp, out := e.do("POST", "/api/sessions/"+viaAttention+"/attention", tok[1], atBody); resp.StatusCode != http.StatusOK {
				t.Fatalf("%s %s via /attention: %d %v", who, st.state, resp.StatusCode, out)
			}
			if a, b := attention(viaEvents), attention(viaAttention); !reflect.DeepEqual(a, b) {
				t.Fatalf("%s %s: /events left %v, /attention left %v", who, st.state, a, b)
			}
		}
	}
	// And the source says who reported.
	e.do("POST", "/api/sessions/"+viaEvents+"/events", tokens["agent token"][0], map[string]any{"type": "working"})
	if src := attention(viaEvents)["source"]; src != session.SourceAPI {
		t.Fatalf("agent token source %v", src)
	}
	e.do("POST", "/api/sessions/"+viaEvents+"/events", adminToken, map[string]any{"type": "done"})
	if src := attention(viaEvents)["source"]; src != session.SourceAdmin {
		t.Fatalf("admin source %v", src)
	}
}

// What the route stores and streams is bounded and clean, and says the agent
// reported it.
func TestEventsRouteRecordsCleanedEntriesReportedByTheAgent(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	events := e.sse(t)
	resp, out := e.do("POST", "/api/sessions/"+id+"/events", e.agentToken(id), map[string]any{
		"type":    "handoff",
		"message": "to\x00 review\nplease " + strings.Repeat("m", 4000),
		"url":     "https://github.com/x/y/pull/1\x07",
		"to":      strings.Repeat("t", 100),
		"tool":    strings.Repeat("k", 300),
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	got := activityPayload(t, e.waitEvent(t, events, isActivity("handoff", id)))
	msg, _ := got["message"].(string)
	if got["sessionId"] != id || got["byName"] != "agent" || got["by"] != nil {
		t.Fatalf("attribution: %v", got)
	}
	if len(msg) != session.MaxAttentionMessage || !strings.HasPrefix(msg, "to review\nplease m") {
		t.Fatalf("message %d bytes %.40q", len(msg), msg)
	}
	if got["url"] != "https://github.com/x/y/pull/1" || len([]rune(got["to"].(string))) != session.MaxEventTo || len(got["tool"].(string)) != session.MaxEventTool {
		t.Fatalf("fields %v", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, got["at"].(string)); err != nil {
		t.Fatalf("at %v", got["at"])
	}
	// The session keeps the same entry in its own log, so viewers replay it.
	d, _ := e.srv.registry.Get(id)
	log := d.(*session.Local).Activity()
	last := log[len(log)-1]
	if last.Type != session.ActivityHandoff || last.ByName != "agent" || last.Message != msg || last.To != got["to"] {
		t.Fatalf("session log ends with %+v", last)
	}
}

// Every entry a server session records reaches the admin stream, not just the
// ones sent to the events route.
func TestSessionOwnEntriesReachTheEventStream(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	events := e.sse(t)
	c := dialViewer(t, e, id, adminToken)
	c.helloNamed(80, 24, "Ada")
	c.expectControl(proto.CtlReady)
	got := activityPayload(t, e.waitEvent(t, events, isActivity("join", id)))
	if got["byName"] != "Ada" || got["sessionId"] != id {
		t.Fatalf("join entry %v", got)
	}
	c.c.CloseNow()
	e.waitEvent(t, events, isActivity("leave", id))
}

// A server session's attention entry reaches the event stream with the state
// it records.
func TestAttentionEntriesCarryTheirStateOnTheEventStream(t *testing.T) {
	e := newTestEnv(t, nil)
	events := e.sse(t)
	id := e.createSession("cat")
	tok := e.agentToken(id)
	if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "needs_input", "message": "Allow Bash?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("attention: %d %v", resp.StatusCode, out)
	}
	ev := e.waitEvent(t, events, isActivity("attention", id))
	if m := activityPayload(t, ev); m["state"] != "needs_input" || m["message"] != "Allow Bash?" {
		t.Fatalf("activity %v", m)
	}
}

// integrations returns what GET /api/integrations lists, keyed by id, and the
// ids in their order.
func (e *testEnv) integrations() (map[string]map[string]any, []string) {
	e.t.Helper()
	resp, out := e.do("GET", "/api/integrations", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("list integrations: %d %v", resp.StatusCode, out)
	}
	raw, ok := out["integrations"].([]any)
	if !ok {
		e.t.Fatalf("integrations: %v", out)
	}
	byID := map[string]map[string]any{}
	var ids []string
	for _, r := range raw {
		it := r.(map[string]any)
		id, _ := it["id"].(string)
		byID[id] = it
		ids = append(ids, id)
	}
	return byID, ids
}

// GET /api/integrations lists every adapter with what it reports, whether it
// is wired at launch, whether its install brings the Conductor skill and
// whether its hooks are installed in the server user's home, and names the
// machine the server runs on; POST /api/integrations/{id}/install puts them
// there, once. An agent whose hooks cannot be installed from a file answers
// no_file_route, with the snippet to do it by hand.
func TestIntegrationsListAndInstall(t *testing.T) {
	e := newTestEnv(t, nil)
	home := t.TempDir()
	e.srv.home = home

	resp, out := e.do("GET", "/api/integrations", adminToken, nil)
	if host, _ := os.Hostname(); resp.StatusCode != http.StatusOK || out["host"] != host {
		t.Fatalf("host %v, want %q", out["host"], host)
	}
	list, ids := e.integrations()
	var want []string
	for _, a := range agents.All() {
		want = append(want, a.ID)
	}
	if len(ids) != 12 || !slices.Equal(ids, want) {
		t.Fatalf("integrations %v, want %v", ids, want)
	}
	fields := []string{"events", "experimental", "id", "installable", "installed", "installsSkill", "launchInjection", "name", "snippet", "where"}
	skillReaders := []string{"claude", "codex", "agy", "copilot", "cursor", "opencode", "pi", "omp", "goose", "amp", "dsh"}
	for _, id := range ids {
		it := list[id]
		if keys := slices.Sorted(maps.Keys(it)); !slices.Equal(keys, fields) {
			t.Errorf("%s: fields %v, want %v", id, keys, fields)
		}
		a, _ := agents.Get(id)
		if it["name"] != a.Name || it["launchInjection"] != (a.Inject != nil) || it["experimental"] != a.Experimental || it["installed"] != false {
			t.Errorf("%s: %v", id, it)
		}
		if it["installsSkill"] != slices.Contains(skillReaders, id) {
			t.Errorf("%s: installsSkill %v", id, it["installsSkill"])
		}
		if events, _ := it["events"].([]any); len(events) != len(a.Events) || events[0] != a.Events[0] {
			t.Errorf("%s: events %v, want %v", id, it["events"], a.Events)
		}
		if s, _ := it["snippet"].(string); s == "" {
			t.Errorf("%s: no snippet", id)
		}
	}
	copilotFile := filepath.Join(home, ".copilot", "hooks", "conductor.json")
	if c := list["copilot"]; c["where"] != copilotFile || !strings.Contains(c["snippet"].(string), " notify --copilot-hook") || c["launchInjection"] != false {
		t.Fatalf("copilot: %v", c)
	}
	if c := list["claude"]; c["launchInjection"] != true || c["where"] != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("claude: %v", c)
	}
	// Nothing to install for aider (its environment is set at launch) or
	// dsh (by hand): no place to report.
	for _, id := range []string{"aider", "dsh"} {
		if list[id]["where"] != "" {
			t.Fatalf("%s: %v", id, list[id])
		}
	}
	if list["dsh"]["experimental"] != true {
		t.Fatalf("dsh: %v", list["dsh"])
	}

	resp, out = e.do("POST", "/api/integrations/copilot/install", adminToken, nil)
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(out["changed"], []any{copilotFile, filepath.Join(home, ".agents", "skills", "conductor", "SKILL.md")}) {
		t.Fatalf("install: %d %v", resp.StatusCode, out)
	}
	if b, err := os.ReadFile(copilotFile); err != nil || !strings.Contains(string(b), " notify --copilot-hook") {
		t.Fatalf("%s: %v\n%s", copilotFile, err, b)
	}
	resp, out = e.do("POST", "/api/integrations/copilot/install", adminToken, nil)
	if changed, ok := out["changed"].([]any); resp.StatusCode != http.StatusOK || !ok || len(changed) != 0 {
		t.Fatalf("second install: %d %v", resp.StatusCode, out)
	}
	if list, _ = e.integrations(); list["copilot"]["installed"] != true || list["copilot"]["where"] != copilotFile {
		t.Fatalf("copilot after install: %v", list["copilot"])
	}

	for _, tc := range []struct{ id, message, snippet string }{
		{"dsh", "developer preview", "permission-requested"},
		{"aider", "at launch", "AIDER_NOTIFICATIONS=true"},
	} {
		resp, out = e.do("POST", "/api/integrations/"+tc.id+"/install", adminToken, nil)
		apiErr, _ := out["error"].(map[string]any)
		msg, _ := apiErr["message"].(string)
		snippet, _ := apiErr["snippet"].(string)
		if resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "no_file_route" || !strings.Contains(msg, tc.message) || !strings.Contains(snippet, tc.snippet) {
			t.Fatalf("%s: %d %v", tc.id, resp.StatusCode, out)
		}
	}
	resp, out = e.do("POST", "/api/integrations/gemini/install", adminToken, nil)
	if resp.StatusCode != http.StatusNotFound || errorCode(out) != "not_found" {
		t.Fatalf("unknown integration: %d %v", resp.StatusCode, out)
	}
	// Only the server user's home was written, and only copilot's file and
	// the skill it reads.
	var written []string
	filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			written = append(written, p)
		}
		return err
	})
	if !slices.Equal(written, []string{filepath.Join(home, ".agents", "skills", "conductor", "SKILL.md"), copilotFile}) {
		t.Fatalf("home holds %q", written)
	}
}

// What Install does before a step it leaves to the admin is kept and
// reported with no_file_route; an install that fails answers install_failed.
func TestIntegrationsInstallByHandAndFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	home := t.TempDir()
	e.srv.home = home
	hooksJSON := filepath.Join(home, ".codex", "hooks.json")
	os.MkdirAll(filepath.Dir(hooksJSON), 0o700)
	os.WriteFile(hooksJSON, []byte(`{"hooks":{}}`), 0o600)
	resp, out := e.do("POST", "/api/integrations/codex/install", adminToken, nil)
	apiErr, _ := out["error"].(map[string]any)
	msg, _ := apiErr["message"].(string)
	snippet, _ := apiErr["snippet"].(string)
	config := filepath.Join(home, ".codex", "config.toml")
	skill := filepath.Join(home, ".codex", "skills", "conductor", "SKILL.md")
	if resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "no_file_route" || !strings.Contains(msg, hooksJSON) ||
		!strings.Contains(snippet, "# >>> conductor") || !reflect.DeepEqual(apiErr["changed"], []any{config, skill}) {
		t.Fatalf("codex: %d %v", resp.StatusCode, out)
	}
	if b, _ := os.ReadFile(hooksJSON); string(b) != `{"hooks":{}}` {
		t.Fatalf("hooks.json changed: %s", b)
	}

	// Only the skill left by hand: the reply carries no snippet.
	home2 := t.TempDir()
	e.srv.home = home2
	ownSkill := filepath.Join(home2, ".claude", "skills", "conductor", "SKILL.md")
	os.MkdirAll(filepath.Dir(ownSkill), 0o700)
	os.WriteFile(ownSkill, []byte("# mine\n"), 0o600)
	resp, out = e.do("POST", "/api/integrations/claude/install", adminToken, nil)
	if apiErr, _ := out["error"].(map[string]any); resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "no_file_route" || apiErr["snippet"] != nil {
		t.Fatalf("skill only: %d %v", resp.StatusCode, out)
	}
	// pi, whose skill in the shared skills directory is the only step left:
	// no snippet either.
	sharedSkill := filepath.Join(home2, ".agents", "skills", "conductor", "SKILL.md")
	os.MkdirAll(filepath.Dir(sharedSkill), 0o700)
	os.WriteFile(sharedSkill, []byte("# mine\n"), 0o600)
	resp, out = e.do("POST", "/api/integrations/pi/install", adminToken, nil)
	if apiErr, _ := out["error"].(map[string]any); resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "no_file_route" || apiErr["snippet"] != nil {
		t.Fatalf("pi, skill only: %d %v", resp.StatusCode, out)
	}
	// Claude Code's settings left by hand as well: the snippet comes back.
	settings := filepath.Join(home2, ".claude", "settings.json")
	os.Remove(settings)
	os.Symlink(filepath.Join(t.TempDir(), "elsewhere.json"), settings)
	resp, out = e.do("POST", "/api/integrations/claude/install", adminToken, nil)
	if apiErr, _ := out["error"].(map[string]any); resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "no_file_route" || !strings.Contains(fmt.Sprint(apiErr["snippet"]), "notify --claude-hook") {
		t.Fatalf("claude, settings by hand: %d %v", resp.StatusCode, out)
	}

	// A home that is a file cannot hold the agent's directories.
	file := filepath.Join(t.TempDir(), "home")
	os.WriteFile(file, nil, 0o600)
	e.srv.home = file
	resp, out = e.do("POST", "/api/integrations/copilot/install", adminToken, nil)
	if resp.StatusCode != http.StatusInternalServerError || errorCode(out) != "install_failed" {
		t.Fatalf("install into a file: %d %v", resp.StatusCode, out)
	}
	if list, _ := e.integrations(); list["copilot"]["installed"] != false {
		t.Fatalf("copilot: %v", list["copilot"])
	}
}

func TestIntegrationsRoutesNeedTheWorkbenchToken(t *testing.T) {
	e := newTestEnv(t, nil)
	home := t.TempDir()
	e.srv.home = home
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/integrations"},
		{"POST", "/api/integrations/copilot/install"},
	} {
		for _, token := range []string{"", "wrong", "test-host-token"} {
			if resp, _ := e.do(r.method, r.path, token, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: %d", r.method, r.path, token, resp.StatusCode)
			}
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("an unauthorized call wrote %v", entries)
	}
}

// An agent's adapter must be one Conductor has: every registered adapter
// saves, and an unknown one is rejected with a message that names it.
func TestCatalogSaveChecksTheAdapter(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, a := range agents.All() {
		body := agentBody("with-" + a.ID)
		body["adapter"] = a.ID
		e.save(body)
	}
	body := agentBody("ghost")
	body["adapter"] = "gemini"
	resp, out := e.do("POST", "/api/catalog", adminToken, body)
	apiErr, _ := out["error"].(map[string]any)
	msg, _ := apiErr["message"].(string)
	if resp.StatusCode != http.StatusBadRequest || apiErr["code"] != "invalid_agent" || !strings.Contains(msg, `"gemini"`) {
		t.Fatalf("unknown adapter: %d %v", resp.StatusCode, out)
	}
	if slices.Contains(e.catalogIDs(), "ghost") {
		t.Fatal("an agent with an unknown adapter was saved")
	}
}

// A home that is not an absolute path is no home: nothing is checked or
// written there, and the install says the home is unknown.
func TestIntegrationsWithoutAKnownHome(t *testing.T) {
	t.Setenv("HOME", "relative/home")
	e := newTestEnv(t, nil)
	if e.srv.home != "" {
		t.Fatalf("home %q", e.srv.home)
	}
	list, _ := e.integrations()
	if c := list["copilot"]; c["installed"] != false || c["where"] != "" {
		t.Fatalf("copilot: %v", c)
	}
	if c := list["aider"]; c["installable"] != false || c["launchInjection"] != true {
		t.Fatalf("aider: %v", c)
	}
	if c := list["copilot"]; c["installable"] != true {
		t.Fatalf("copilot: %v", c)
	}
	// DeepSeek Harness has an Install, but it only ever leaves the plugin to
	// the user: there is nothing to install either.
	if c := list["dsh"]; c["installable"] != false || c["launchInjection"] != false {
		t.Fatalf("dsh: %v", c)
	}
	resp, out := e.do("POST", "/api/integrations/copilot/install", adminToken, nil)
	apiErr, _ := out["error"].(map[string]any)
	if msg, _ := apiErr["message"].(string); resp.StatusCode != http.StatusInternalServerError || apiErr["code"] != "install_failed" || !strings.Contains(msg, "unknown") {
		t.Fatalf("install: %d %v", resp.StatusCode, out)
	}
}

// Closing the viewers of a forgotten run's links is the server's own work:
// Shutdown waits for it, or for its context.
func TestShutdownWaitsForWhatTheServerStarted(t *testing.T) {
	e := newTestEnv(t, nil)
	release := make(chan struct{})
	e.srv.track(func() { <-release })
	done := make(chan struct{})
	go func() {
		e.srv.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Shutdown returned while the server's own goroutine ran")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}

// A file event names its op and path (design 4e); without them it is
// refused; it reaches the session's activity with both and the tool.
func TestFileEventsNameTheirOpAndPath(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "file", "op": "edit", "path": "internal/api/users.go", "tool": "Edit"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("file event: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "file", "op": "burn", "path": "x"}); resp.StatusCode != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("an unknown op: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, map[string]any{"type": "file", "op": "edit"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no path: %d %v", resp.StatusCode, out)
	}
	d, _ := e.srv.registry.Get(id)
	for _, a := range d.(*session.Local).Activity() {
		if a.Type == session.ActivityFile && a.Op == "edit" && a.Path == "internal/api/users.go" && a.Tool == "Edit" {
			return
		}
	}
	t.Fatal("the file event is not in the activity")
}

// A managed run's attempt reserves its place among the sessions before it
// makes anything (crew.Launcher.Reserve): the place counts as a session
// does, and is let go when the launch does not follow.
func TestAReservationHoldsAPlaceAmongTheSessions(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.MaxSessions = 1 })
	res, err := e.srv.Reserve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a session over a held place: %d %v", resp.StatusCode, out)
	}
	if _, err := e.srv.Reserve(t.Context()); !errors.Is(err, session.ErrTooManySessions) {
		t.Fatalf("a second place at the cap: %v", err)
	}
	res.Release()
	e.createSession("cat")
	if _, err := e.srv.Reserve(t.Context()); !errors.Is(err, session.ErrTooManySessions) {
		t.Fatalf("a place beside a session at the cap: %v", err)
	}
}
