package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// testDesktopKey is the key a test's desktop app gave its server.
const testDesktopKey = "test-desktop-key-0123456789abcdefABCDEF"

// withDesktopKey is a test env's mutate: the server holds the app's key.
func withDesktopKey(c *config.Config) { c.DesktopKey = config.NewSecret(testDesktopKey) }

// allowAs posts body to POST /api/roots with token and, unless key is
// "-", the key header set to key (empty included).
func (e *testEnv) allowAs(token, key string, body any) (int, map[string]any) {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		raw, _ = json.Marshal(b)
	}
	req, _ := http.NewRequest("POST", e.http.URL+"/api/roots", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "-" {
		req.Header[desktopKeyHeader] = []string{key}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

// allow is allowAs from the app: the workbench token and the key.
func (e *testEnv) allow(path string, confirmed string) (int, map[string]any) {
	e.t.Helper()
	body := map[string]any{"path": path}
	if confirmed != "" {
		body["confirmed"] = confirmed
	}
	return e.allowAs(adminToken, testDesktopKey, body)
}

// errorField is a field of an error reply's error object.
func errorField(out map[string]any, key string) any {
	e, _ := out["error"].(map[string]any)
	return e[key]
}

// Exactly the app that started the server may allow a folder: the
// workbench token AND the key it gave the server. Every other credential,
// and the token without the key, is refused with nothing added.
func TestAllowRootIsTheAppsAlone(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	id := e.createSession("cat")
	_, share, err := e.srv.links.Create(id, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := e.srv.links.CreateRunLink("r-1a2b3c4d", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := e.agentToken(id)
	folder := realRoot(t, t.TempDir())
	body := map[string]any{"path": folder}
	for _, c := range []struct {
		name, token, key string
		status           int
		code             string
	}{
		{"the token without the key", adminToken, "-", 403, "not_the_app"},
		{"the token with a wrong key", adminToken, "wrong-" + testDesktopKey, 403, "not_the_app"},
		{"the token with the key cut short", adminToken, testDesktopKey[:len(testDesktopKey)-1], 403, "not_the_app"},
		{"the token with an empty header", adminToken, "", 403, "not_the_app"},
		{"the key without a token", "", testDesktopKey, 401, "unauthorized"},
		{"a share link's token with the key", share, testDesktopKey, 401, "unauthorized"},
		{"a run link's token with the key", run, testDesktopKey, 401, "unauthorized"},
		{"a host's token with the key", "test-host-token", testDesktopKey, 401, "unauthorized"},
		{"an agent's own token with the key", agent, testDesktopKey, 401, "unauthorized"},
		{"the key as the token", testDesktopKey, testDesktopKey, 401, "unauthorized"},
	} {
		status, out := e.allowAs(c.token, c.key, body)
		if status != c.status || errorCode(out) != c.code {
			t.Errorf("%s: %d %v", c.name, status, out)
		}
	}
	if got := e.srv.roots.paths(); len(got) != 1 {
		t.Fatalf("a refusal added a folder: %v", got)
	}
	if status, out := e.allow(folder, ""); status != http.StatusOK || out["root"] != folder || out["added"] != true {
		t.Fatalf("the app: %d %v", status, out)
	}
}

// A server that holds no key (one not started by the app) lets nobody add
// a folder: the token with no header or an empty one is refused, though
// two empty strings compare equal.
func TestAllowRootNeedsAKeyHeld(t *testing.T) {
	e := newTestEnv(t, nil)
	folder := realRoot(t, t.TempDir())
	for _, key := range []string{"-", "", testDesktopKey} {
		if status, out := e.allowAs(adminToken, key, map[string]any{"path": folder}); status != http.StatusForbidden || errorCode(out) != "not_the_app" {
			t.Errorf("key %q: %d %v", key, status, out)
		}
	}
	if got := e.srv.roots.paths(); len(got) != 1 {
		t.Fatalf("a folder was added: %v", got)
	}
}

// An identity provider's principal with the admin grant is no app, with
// the key or without; a switchyard launches nothing and adds no folder.
func TestAllowRootRefusesAProvidersAdminAndASwitchyard(t *testing.T) {
	const cloudToken = "cloud-admin-token-0123456789"
	p := &tokenIdentity{known: map[string]ext.Principal{cloudToken: cloudAdmin("subject-1")}}
	e := providerEnv(t, withDesktopKey, p)
	folder := realRoot(t, t.TempDir())
	for _, key := range []string{"-", testDesktopKey} {
		if status, out := e.allowAs(cloudToken, key, map[string]any{"path": folder}); status != http.StatusForbidden || errorCode(out) != "not_the_app" {
			t.Errorf("a provider's admin, key %q: %d %v", key, status, out)
		}
	}
	if got := e.srv.roots.paths(); len(got) != 1 {
		t.Fatalf("a folder was added: %v", got)
	}
	y := newTestEnv(t, func(c *config.Config) {
		withDesktopKey(c)
		c.Switchyard.Enabled = true
	})
	if status, out := y.allowAs(adminToken, testDesktopKey, map[string]any{"path": folder}); status != http.StatusForbidden || errorCode(out) != "switchyard" {
		t.Errorf("a switchyard: %d %v", status, out)
	}
}

// Refused keys count against the address's limit, as refused tokens do:
// past it, 429.
func TestAllowRootLimitsRefusedKeys(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	folder := realRoot(t, t.TempDir())
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		status, out := e.allowAs(adminToken, "wrong", map[string]any{"path": folder})
		switch {
		case status == http.StatusTooManyRequests:
			limited = true
		case status != http.StatusForbidden:
			t.Fatalf("try %d: %d %v", i, status, out)
		}
	}
	if !limited {
		t.Fatal("60 refused keys from one address were never limited")
	}
}

// The folder as the route judges it: the body's bounds, then the folder
// again under the holder's lock, each refusal with nothing added.
func TestAllowRootJudgesTheFolder(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	home := realRoot(t, e.srv.home)
	data := realRoot(t, mkdir(t, e.srv.cfg.DataDir))
	outside := realRoot(t, t.TempDir())
	parent := realRoot(t, t.TempDir())
	link := filepath.Join(parent, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	comma := mkdir(t, filepath.Join(parent, "a,b"))
	space := mkdir(t, filepath.Join(parent, "a "))
	held := func() int { return len(e.srv.roots.paths()) }
	for _, c := range []struct {
		name   string
		body   any
		status int
		code   string
		key    string
		value  string
	}{
		{"a relative path", map[string]any{"path": "rel/dir"}, 400, "invalid_request", "", ""},
		{"a path with ..", map[string]any{"path": outside + "/../" + filepath.Base(outside)}, 400, "invalid_request", "", ""},
		{"a trailing slash", map[string]any{"path": outside + "/"}, 400, "invalid_request", "", ""},
		{"a doubled slash", map[string]any{"path": strings.Replace(outside, "/", "//", 1)}, 400, "invalid_request", "", ""},
		{"1025 bytes", map[string]any{"path": "/" + strings.Repeat("a", 1024)}, 400, "invalid_request", "", ""},
		{"a NUL", map[string]any{"path": outside + "\x00x"}, 400, "invalid_request", "", ""},
		{"an unknown field", map[string]any{"path": outside, "also": true}, 400, "invalid_request", "", ""},
		{"two objects", `{"path":"` + outside + `"}{"path":"/"}`, 400, "invalid_request", "", ""},
		{"a confirmed of no kind", map[string]any{"path": outside, "confirmed": "everything"}, 400, "invalid_request", "", ""},
		{"a body past 8 KiB", `{"path":"` + outside + `","confirmed":"` + strings.Repeat("x", 9<<10) + `"}`, 413, "body_too_large", "", ""},
		{"a link", map[string]any{"path": link}, 409, "folder_is_link", "real", outside},
		{"a folder not there", map[string]any{"path": filepath.Join(parent, "nope")}, 409, "folder_missing", "", ""},
		{"a file", map[string]any{"path": file}, 409, "folder_not_directory", "", ""},
		{"the whole disk", map[string]any{"path": "/"}, 403, "folder_refused", "reason", "whole_disk"},
		{"the data directory", map[string]any{"path": data}, 403, "folder_refused", "reason", "conductor_data"},
		{"a folder inside it", map[string]any{"path": mkdir(t, filepath.Join(data, "in"))}, 403, "folder_refused", "reason", "conductor_data"},
		{"a comma", map[string]any{"path": comma}, 403, "folder_refused", "reason", "name"},
		{"a space at an end", map[string]any{"path": space}, 403, "folder_refused", "reason", "name"},
		{"the home folder unconfirmed", map[string]any{"path": home}, 409, "folder_wide", "wide", "home"},
		{"the home folder confirmed as another kind", map[string]any{"path": home, "confirmed": "above_home"}, 409, "folder_wide", "wide", "home"},
		{"the folder above it unconfirmed", map[string]any{"path": filepath.Dir(home)}, 409, "folder_wide", "wide", "above_home"},
		{"a folder holding the data unconfirmed", map[string]any{"path": filepath.Dir(data)}, 409, "folder_wide", "wide", "holds_data"},
	} {
		status, out := e.allowAs(adminToken, testDesktopKey, c.body)
		if status != c.status || errorCode(out) != c.code || (c.key != "" && errorField(out, c.key) != c.value) {
			t.Errorf("%s: %d %v", c.name, status, out)
		}
		if held() != 1 {
			t.Fatalf("%s added a folder: %v", c.name, e.srv.roots.paths())
		}
	}
	// Confirmed as its kind, a wide folder is added.
	if status, out := e.allow(home, "home"); status != 200 || out["root"] != home || out["added"] != true {
		t.Fatalf("the home folder confirmed: %d %v", status, out)
	}
	// Allowed already: nothing added, and the root named.
	under := mkdir(t, filepath.Join(home, "proj"))
	if status, out := e.allow(under, ""); status != 200 || out["root"] != under || out["added"] != false || held() != 2 {
		t.Fatalf("allowed already: %d %v", status, out)
	}
	if status, out := e.allow(home, "home"); status != 200 || out["added"] != false || held() != 2 {
		t.Fatalf("added again: %d %v", status, out)
	}
}

// At most 64 folders are held: the 65th is refused.
func TestAllowRootHoldsAtMost64(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	for i := len(e.srv.roots.paths()); i < maxRoots; i++ {
		if status, out := e.allow(realRoot(t, t.TempDir()), ""); status != 200 {
			t.Fatalf("folder %d: %d %v", i+1, status, out)
		}
	}
	if status, out := e.allow(realRoot(t, t.TempDir()), ""); status != http.StatusConflict || errorCode(out) != "too_many_roots" {
		t.Fatalf("the 65th: %d %v", status, out)
	}
	if n := len(e.srv.roots.paths()); n != maxRoots {
		t.Fatalf("%d folders held", n)
	}
}

// Between the route's judgement and its add the folder is replaced (by a
// link to it, then by another folder at its path): the last look under the
// lock refuses it, and nothing is added. Without that look the add went
// through every time.
func TestAllowRootLooksAgainBeforeTheAdd(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	parent := realRoot(t, t.TempDir())
	elsewhere := realRoot(t, t.TempDir())
	for _, c := range []struct {
		name string
		swap func(t *testing.T, path string)
		code string
	}{
		{"a link put in its place", func(t *testing.T, path string) {
			moved := filepath.Join(elsewhere, "moved-"+filepath.Base(path))
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}
		}, "folder_is_link"},
		{"another folder put in its place", func(t *testing.T, path string) {
			if err := os.Rename(path, filepath.Join(elsewhere, "gone-"+filepath.Base(path))); err != nil {
				t.Fatal(err)
			}
			mkdir(t, path)
		}, "folder_changed"},
		{"the folder removed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, "folder_missing"},
	} {
		path := mkdir(t, filepath.Join(parent, strings.ReplaceAll(c.name, " ", "-")))
		var ran bool
		e.srv.roots.beforeAdd = func(p string) {
			ran = true
			c.swap(t, p)
		}
		status, out := e.allow(path, "")
		e.srv.roots.beforeAdd = nil
		if !ran || status != http.StatusConflict || errorCode(out) != c.code {
			t.Errorf("%s: ran %v, %d %v", c.name, ran, status, out)
		}
		if got := e.srv.roots.paths(); len(got) != 1 {
			t.Fatalf("%s: a folder was added: %v", c.name, got)
		}
	}
}

// A folder added and later replaced by a link is used no more: the link
// leads nowhere a launch may go.
func TestAnAddedRootReplacedByALinkLeadsNowhere(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	parent := realRoot(t, t.TempDir())
	elsewhere := realRoot(t, t.TempDir())
	path := mkdir(t, filepath.Join(parent, "proj"))
	if status, out := e.allow(path, ""); status != 200 {
		t.Fatalf("allow: %d %v", status, out)
	}
	if out := e.checkRoot(path); out["state"] != "allowed" {
		t.Fatalf("after the add: %v", out)
	}
	if err := os.Rename(path, filepath.Join(parent, "proj-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	if out := e.checkRoot(path); out["state"] != "outside" {
		t.Fatalf("the link in its place: %v", out)
	}
	if resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": path}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a launch through the link: %d %v", resp.StatusCode, out)
	}
}

// After a 200 a launch in the folder starts; a session started before it
// goes on with the same process; nothing the server logged or answered
// holds the key; twenty presses at once add the folder once.
func TestAnAllowedFolderLaunchesAndTouchesNoSession(t *testing.T) {
	var logs syncLog
	e := newTestEnvLogging(t, withDesktopKey, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	// A session started before, its shell's process id written where the
	// test reads it (exec keeps the id).
	pidFile := filepath.Join(t.TempDir(), "pid")
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "sh", "args": []string{"-c", "echo $$ > " + pidFile + "; exec cat"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the session before: %d %v", resp.StatusCode, out)
	}
	before, _ := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(before); ok {
			_ = d.Stop(t.Context())
		}
	})
	pid := servicePid(t, pidFile)
	folder := realRoot(t, t.TempDir())
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": folder}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a launch before: %d", resp.StatusCode)
	}
	var replies []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := e.allow(folder, "")
			b, _ := json.Marshal(out)
			mu.Lock()
			replies = append(replies, string(b))
			mu.Unlock()
			if status != 200 {
				t.Errorf("a press: %d %v", status, out)
			}
		}()
	}
	wg.Wait()
	if got := e.srv.roots.paths(); len(got) != 2 || got[1] != folder {
		t.Fatalf("twenty presses: %v", got)
	}
	resp, out = e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": folder})
	if resp.StatusCode != http.StatusCreated || out["cwd"] != folder {
		t.Fatalf("a launch after: %d %v", resp.StatusCode, out)
	}
	after, _ := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(after); ok {
			_ = d.Stop(t.Context())
		}
	})
	d, ok := e.srv.registry.Get(before)
	if !ok || d.Info().Status != session.StatusRunning || syscall.Kill(pid, 0) != nil {
		t.Fatalf("the session started before is not running as it was (pid %d): %v", pid, ok)
	}
	if again := servicePid(t, pidFile); again != pid {
		t.Fatalf("the session's process changed: %d, then %d", pid, again)
	}
	_, refused := e.allowAs(adminToken, "wrong", map[string]any{"path": folder})
	b, _ := json.Marshal(refused)
	replies = append(replies, string(b))
	for _, r := range replies {
		if strings.Contains(r, testDesktopKey) {
			t.Fatalf("a reply holds the key: %s", r)
		}
	}
	if strings.Contains(logs.String(), testDesktopKey) {
		t.Fatalf("the log holds the key:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "a folder agents may use was added") {
		t.Fatalf("no line names the folder added:\n%s", logs.String())
	}
}

// whoami says where the folders come from: the app (a key held) or the
// configuration alone.
func TestWhoAmISaysWhereTheFoldersComeFrom(t *testing.T) {
	for _, c := range []struct {
		mutate func(*config.Config)
		want   string
	}{{nil, "config"}, {withDesktopKey, "app"}} {
		e := newTestEnv(t, c.mutate)
		resp, out := e.do("GET", "/api/whoami", adminToken, nil)
		if resp.StatusCode != 200 || out["folders"] != c.want {
			t.Errorf("want %s: %d %v", c.want, resp.StatusCode, out)
		}
	}
}

// An add judges the folder under the roots' lock: a second press of the
// same folder, made while the first is held between its judgement and its
// add, finds the first holding the lock (beforeLock). The first goes on
// only once the second has either judged the folder (afterJudge) or is
// waiting at the lock (its count), whichever comes: under the lock the
// second waits, judges only after the first has added, and adds nothing;
// a judgement made anywhere outside the lock comes first, before the first
// has added, and adds the folder a second time, every run.
func TestAnAddIsJudgedUnderTheLock(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	folder := realRoot(t, t.TempDir())
	held, release := make(chan struct{}), make(chan struct{})
	var asks atomic.Int32
	e.srv.roots.beforeLock = func(string) {
		if asks.Add(1) != 2 {
			return
		}
		// The second press: the first holds the lock.
		if e.srv.roots.mu.TryLock() {
			e.srv.roots.mu.Unlock()
			t.Error("the first press does not hold the roots' lock")
		}
	}
	var judged atomic.Int32
	e.srv.roots.afterJudge = func(string) { judged.Add(1) }
	var once sync.Once
	e.srv.roots.beforeAdd = func(string) {
		once.Do(func() {
			close(held)
			<-release
		})
	}
	type answer struct {
		status int
		out    map[string]any
	}
	first, second := make(chan answer, 1), make(chan answer, 1)
	go func() {
		status, out := e.allow(folder, "")
		first <- answer{status, out}
	}()
	<-held
	go func() {
		status, out := e.allow(folder, "")
		second <- answer{status, out}
	}()
	// The first has judged (one), and holds the lock; the second either
	// judges too, or waits at the lock.
	for judged.Load() < 2 && e.srv.roots.mu.waiting.Load() < 1 {
		runtime.Gosched()
	}
	close(release)
	a, b := <-first, <-second
	if a.status != http.StatusOK || a.out["added"] != true {
		t.Fatalf("the first press: %d %v", a.status, a.out)
	}
	if b.status != http.StatusOK || b.out["added"] != false {
		t.Fatalf("the second press: %d %v", b.status, b.out)
	}
	if got := e.srv.roots.paths(); len(got) != 2 || got[1] != folder {
		t.Fatalf("the roots: %v", got)
	}
}
