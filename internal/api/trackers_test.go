package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/store"
	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// The planted tokens: each distinctive, so that a search finds any copy.
const (
	plantedToken   = "ghp_PLANTEDsettingsTOKEN0123456789abcdWXYZ"
	plantedToken2  = "ghp_PLANTEDsecondTOKEN0123456789abcdefWXYZ"
	plantedEnv     = "ghp_PLANTEDenvironTOKEN0123456789abcdeWXYZ"
	plantedWrong   = "ghp_PLANTEDrefusedTOKEN0123456789abcdeWXYZ"
	plantedMoved   = "ghp_PLANTEDredirectTOKEN0123456789abcdWXYZ"
	plantedBusy    = "ghp_PLANTEDunavailTOKEN0123456789abcdeWXYZ"
	plantedLimited = "ghp_PLANTEDlimitedTOKEN0123456789abcdeWXYZ"
	// Tokens whose answers are no account (a field that holds the token
	// sent, a login of 20 KiB), and one a 403 that names permissions
	// answers.
	plantedLoginForm = "PLANTEDechoLOGIN0123456789abcdefWXYZ"
	plantedScopeForm = "ghp_plantedscope_echo_token_0123"
	plantedPermNames = "ghp_plantedpermission_echo_token"
	plantedBig       = "ghp_PLANTEDbigANSWERtoken0123456789WXYZ"
	// gheHost is an Enterprise Server's host the fake answers as.
	gheHost = "ghe.example.com"
)

var plantedAll = []string{plantedToken, plantedToken2, plantedEnv, plantedWrong, plantedMoved, plantedBusy, plantedLimited, plantedLoginForm, plantedScopeForm, plantedPermNames, plantedBig}

// leakForms is every form of tok a leak could take: the token, its first
// twelve characters after its type's prefix, its SHA-256 in hex and in
// base64, and its own base64. The fingerprint (the hash's first 12 hex
// digits) is what a person is shown, and is not among them.
func leakForms(tok string) []string {
	sum := sha256.Sum256([]byte(tok))
	return []string{tok, tok[4:16], hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:]),
		base64.StdEncoding.EncodeToString([]byte(tok)), base64.RawURLEncoding.EncodeToString([]byte(tok))}
}

func fingerprintOf(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:6])
}

// leaks names the planted tokens whose any form b holds.
func leaks(b []byte) []string {
	var out []string
	for _, tok := range plantedAll {
		for _, f := range leakForms(tok) {
			if bytes.Contains(b, []byte(f)) {
				out = append(out, f)
			}
		}
	}
	return out
}

// fakeGitHub is the adapter's fake GitHub answering GET /user on
// github.com's API and on gheHost (and ghe-<n>.example.com) as GitHub does
// for the tokens it accepts, and as it might for the rest: a refusal that
// echoes the Authorization it was sent in its body and in a header,
// plantedMoved a redirect to another host, plantedBusy a 503,
// plantedLimited a rate limit, and the last four answers that are no
// account or a 403. Deny makes it refuse a token it accepted.
type fakeGH struct {
	*githubtest.Server
	mu     sync.Mutex
	accept map[string]bool
}

func fakeGitHub(t *testing.T) *fakeGH {
	t.Helper()
	f := &fakeGH{Server: githubtest.New(t), accept: map[string]bool{plantedToken: true, plantedToken2: true, plantedEnv: true}}
	user := func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(auth, "Bearer ")
		f.mu.Lock()
		ok := f.accept[tok]
		f.mu.Unlock()
		switch {
		case ok:
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(githubtest.Fixture("user.json"))
		case tok == plantedLoginForm || tok == plantedBig:
			login := tok
			if tok == plantedBig {
				login += strings.Repeat("a", 20<<10)
			}
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = io.WriteString(w, `{"login":"`+login+`","id":9000002}`)
		case tok == plantedScopeForm:
			w.Header().Set("X-OAuth-Scopes", "repo, "+tok)
			_, _ = w.Write(githubtest.Fixture("user.json"))
		case tok == plantedPermNames:
			w.Header().Set("X-Accepted-GitHub-Permissions", tok+"=read")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
		case tok == plantedMoved:
			http.Redirect(w, r, "https://collector.example/take?token="+tok, http.StatusFound)
		case tok == plantedBusy:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"message":"unavailable for `+auth+`"}`)
		case tok == plantedLimited:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded for `+tok+`"}`)
		default:
			w.Header().Set("X-Echo", auth)
			w.Header().Set("WWW-Authenticate", `Bearer error="`+tok+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Bad credentials: `+auth+`"}`)
		}
	}
	f.Handle("GET", "api.github.com", "/user", user)
	for _, h := range []string{gheHost, "ghe-1.example.com", "ghe-2.example.com", "ghe-3.example.com", "ghe-4.example.com"} {
		f.Handle("GET", h, "/api/v3/user", user)
	}
	return f
}

func (f *fakeGH) deny(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.accept, tok)
}

// userCalls counts the fake's GET /user, everywhere.
func (f *fakeGH) userCalls() int {
	n := 0
	for _, r := range f.Requests() {
		if strings.HasSuffix(r.Path, "/user") {
			n++
		}
	}
	return n
}

// useFake points srv's adapter at the fake.
func useFake(srv *Server, f *fakeGH) { srv.trackers.gh = srv.trackers.adapter(f.Transport()) }

// trackerEnv is a test server whose adapter reaches the fake.
func trackerEnv(t *testing.T, mutate func(*config.Config)) (*testEnv, *fakeGH) {
	t.Helper()
	e := newTestEnv(t, mutate)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	f := fakeGitHub(t)
	useFake(e.srv, f)
	return e, f
}

func (e *testEnv) saveToken(t *testing.T, host, tok string) (int, []byte, map[string]any) {
	t.Helper()
	return e.callAs(t, adminToken, "POST", "/api/trackers/github", map[string]any{"host": host, "token": tok})
}

func (e *testEnv) trackersListed(t *testing.T) []map[string]any {
	t.Helper()
	status, data, out := e.callAs(t, adminToken, "GET", "/api/trackers", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/trackers: %d %s", status, data)
	}
	var list []map[string]any
	for _, v := range out["github"].([]any) {
		list = append(list, v.(map[string]any))
	}
	return list
}

// rowOf is the listed row of host's settings token, or nil.
func rowOf(l []map[string]any, host string) map[string]any {
	for _, r := range l {
		if r["host"] == host && r["source"] == "settings" {
			return r
		}
	}
	return nil
}

func (e *testEnv) credentialsFile() string {
	return filepath.Join(e.srv.store.Dir(), trackerDir, githubFile)
}

// A token is checked with GitHub before it is kept: one of no token's
// form, or for a host no tracker rule admits, is refused before GitHub is
// asked; one GitHub refuses is kept nowhere; one it takes is kept with
// its account and shown by its fingerprint; the same token again is
// checked again and changes nothing (share.Equal, the file's bytes as
// they were); the kept token GitHub refuses later is shown refused and
// stays; at most four hosts; the environment's host is not saved here;
// and a file or a directory another user may read is not used.
func TestTrackerTokenChecked(t *testing.T) {
	e, f := trackerEnv(t, nil)
	for _, tok := range []string{"", "has space", strings.Repeat("a", maxTrackerToken+1), "tokén", "tab\there"} {
		status, data, out := e.saveToken(t, "", tok)
		if status != http.StatusBadRequest || codeOf(out) != "invalid_token" || (tok != "" && bytes.Contains(data, []byte(tok))) {
			t.Errorf("token %.12q: %d %s", tok, status, data)
		}
	}
	for _, host := range []string{"GitHub.com", "127.0.0.1", "github.com:443", "localhost"} {
		if status, data, out := e.saveToken(t, host, plantedToken); status != http.StatusBadRequest || codeOf(out) != "invalid_host" {
			t.Errorf("host %q: %d %s", host, status, data)
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Fatalf("GitHub was asked %d times for tokens and hosts refused by their form", n)
	}

	if status, data, out := e.saveToken(t, "", plantedWrong); status != http.StatusUnprocessableEntity || codeOf(out) != "token_refused" {
		t.Fatalf("a refused token: %d %s", status, data)
	}
	if l := e.trackersListed(t); len(l) != 0 {
		t.Fatalf("a refused token is listed: %v", l)
	}
	if _, err := os.Lstat(e.credentialsFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused token made the file: %v", err)
	}

	status, data, out := e.saveToken(t, "", plantedToken)
	got, _ := out["tracker"].(map[string]any)
	if status != http.StatusOK || got["login"] != "mona-example" || got["fingerprint"] != fingerprintOf(plantedToken) || got["source"] != "settings" || got["state"] != "ok" || got["host"] != "github.com" {
		t.Fatalf("a saved token: %d %s", status, data)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{e.credentialsFile(): 0o600, filepath.Dir(e.credentialsFile()): 0o700} {
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %v %v, not %04o", path, fi.Mode(), err, want)
			}
		}
	}
	before, err := os.ReadFile(e.credentialsFile())
	if err != nil || !bytes.Contains(before, []byte(plantedToken)) {
		t.Fatalf("the file: %v", err)
	}
	calls := f.userCalls()
	status, data, out = e.saveToken(t, "github.com", plantedToken)
	if after, _ := os.ReadFile(e.credentialsFile()); status != http.StatusOK || out["unchanged"] != true || !bytes.Equal(before, after) || f.userCalls() != calls+1 {
		t.Fatalf("the same token again: %d %s (checked %d times)", status, data, f.userCalls()-calls)
	}

	f.deny(plantedToken)
	if status, data, out := e.saveToken(t, "", plantedToken); status != http.StatusUnprocessableEntity || codeOf(out) != "token_refused" {
		t.Fatalf("the kept token, refused now: %d %s", status, data)
	}
	l := e.trackersListed(t)
	if len(l) != 1 || l[0]["state"] != "refused" || l[0]["checkedAt"] == nil {
		t.Fatalf("a kept token GitHub refuses is not shown refused: %v", l)
	}
	if after, _ := os.ReadFile(e.credentialsFile()); !bytes.Equal(before, after) {
		t.Fatal("a refused check changed the kept token")
	}
	// What is shown of the kept token is what GitHub last said of it: a
	// check of another token, refused or accepted and then not kept,
	// changes nothing.
	refusedAt := l[0]["checkedAt"]
	stillRefused := func(after string) {
		t.Helper()
		if l := e.trackersListed(t); len(l) < 1 || l[0]["fingerprint"] != fingerprintOf(plantedToken) || l[0]["state"] != "refused" || l[0]["checkedAt"] != refusedAt {
			t.Fatalf("after %s, the kept token: %v", after, l)
		}
	}
	if status, data, _ := e.saveToken(t, "", plantedWrong); status != http.StatusUnprocessableEntity {
		t.Fatalf("another token, refused: %d %s", status, data)
	}
	stillRefused("another token refused")
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		dir := filepath.Dir(e.credentialsFile())
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		status, data, out := e.saveToken(t, "", plantedToken2)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusInternalServerError || codeOf(out) != "store_failed" {
			t.Fatalf("another token, accepted and not written: %d %s", status, data)
		}
		stillRefused("another token accepted and not written")
	}
	if status, data, _ := e.saveToken(t, "", plantedToken2); status != http.StatusOK {
		t.Fatalf("a new token: %d %s", status, data)
	}
	if l := e.trackersListed(t); len(l) != 1 || l[0]["state"] != "ok" || l[0]["fingerprint"] != fingerprintOf(plantedToken2) {
		t.Fatalf("the new token: %v", l)
	}

	for i := 1; i <= 3; i++ {
		if status, data, _ := e.saveToken(t, "ghe-"+string(rune('0'+i))+".example.com", plantedToken2); status != http.StatusOK {
			t.Fatalf("host %d: %d %s", i, status, data)
		}
	}
	if status, data, out := e.saveToken(t, "ghe-4.example.com", plantedToken2); status != http.StatusConflict || codeOf(out) != "too_many_hosts" {
		t.Fatalf("a fifth host: %d %s", status, data)
	}
	if l := e.trackersListed(t); len(l) != maxTrackerHosts {
		t.Fatalf("%d hosts listed", len(l))
	}
	// The kept token refused, then a token accepted for a fifth host and
	// not kept: the refusal stays.
	f.deny(plantedToken2)
	if status, data, _ := e.saveToken(t, "", plantedToken2); status != http.StatusUnprocessableEntity {
		t.Fatalf("the kept token, refused now: %d %s", status, data)
	}
	row := rowOf(e.trackersListed(t), "github.com")
	if row["state"] != "refused" {
		t.Fatalf("the kept token, refused: %v", row)
	}
	refusedAt = row["checkedAt"]
	if status, data, out := e.saveToken(t, "ghe-4.example.com", plantedEnv); status != http.StatusConflict || codeOf(out) != "too_many_hosts" {
		t.Fatalf("a fifth host: %d %s", status, data)
	}
	if row := rowOf(e.trackersListed(t), "github.com"); row["fingerprint"] != fingerprintOf(plantedToken2) || row["state"] != "refused" || row["checkedAt"] != refusedAt {
		t.Fatalf("after a fifth host's token was not kept: %v", row)
	}
	// A 403 is answered in the server's words: no permission's name.
	if status, data, out := e.saveToken(t, "ghe-1.example.com", plantedPermNames); status != http.StatusUnprocessableEntity || codeOf(out) != "token_refused" || strings.Contains(string(data), "plantedpermission") || strings.Contains(string(data), "=read") {
		t.Fatalf("a 403: %d %s", status, data)
	}

	if runtime.GOOS != "windows" {
		for _, c := range []struct {
			path string
			mode os.FileMode
		}{{e.credentialsFile(), 0o644}, {filepath.Dir(e.credentialsFile()), 0o750}} {
			if err := os.Chmod(c.path, c.mode); err != nil {
				t.Fatal(err)
			}
			if status, data, out := e.callAs(t, adminToken, "GET", "/api/trackers", nil); status != http.StatusInternalServerError || codeOf(out) != "credentials_unusable" || !strings.Contains(string(data), "open to its group or others") {
				t.Errorf("%s at %04o: %d %s", c.path, c.mode, status, data)
			}
			if _, err := e.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: "github.com"}); err == nil {
				t.Errorf("%s at %04o: a token was used from it", c.path, c.mode)
			}
			if err := os.Chmod(c.path, c.mode&0o700); err != nil {
				t.Fatal(err)
			}
		}
	}

	env, _ := trackerEnv(t, func(c *config.Config) { c.GitHubToken, c.GitHubHost = config.NewSecret(plantedEnv), gheHost })
	if status, data, out := env.saveToken(t, gheHost, plantedToken); status != http.StatusConflict || codeOf(out) != "environment" {
		t.Fatalf("the environment's host: %d %s", status, data)
	}
	if status, data, out := env.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": gheHost}); status != http.StatusOK || out["removed"] != false || out["inUse"] != "environment" {
		t.Fatalf("the environment's host removed: %d %s", status, data)
	}
	if l := env.trackersListed(t); len(l) != 1 || l[0]["source"] != "environment" || l[0]["state"] != "unchecked" || l[0]["fingerprint"] != fingerprintOf(plantedEnv) {
		t.Fatalf("the environment's token: %v", l)
	}
}

// Removing a host's token takes effect at the next call: credentialFor
// reads the file each time, and with no entry answers 412 no_token; a
// remove of nothing says so; the environment's token is credentialFor's
// for its host, and the credential it gives reaches GitHub.
func TestTrackerRemoveTakesEffect(t *testing.T) {
	e, f := trackerEnv(t, nil)
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	ref := tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: "octo/repo", Kind: "issue", ID: "1"}
	c, err := e.srv.credentialFor(t.Context(), ref)
	if err != nil || c.Fingerprint() != fingerprintOf(plantedToken) {
		t.Fatalf("credentialFor: %v %v", c, err)
	}
	if _, err := e.srv.trackers.gh.Whoami(t.Context(), c); err != nil {
		t.Fatalf("the credential at GitHub: %v", err)
	}
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{}); status != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove: %d %s", status, data)
	}
	var ae *apiError
	if _, err := e.srv.credentialFor(t.Context(), ref); !errors.As(err, &ae) || ae.status != http.StatusPreconditionFailed || ae.Code != "no_token" {
		t.Fatalf("after the remove: %v", err)
	}
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK || out["removed"] != false {
		t.Fatalf("remove again: %d %s", status, data)
	}
	if l := e.trackersListed(t); len(l) != 0 {
		t.Fatalf("listed after the remove: %v", l)
	}
	if _, err := e.srv.credentialFor(t.Context(), tracker.Ref{System: "gitlab", Host: "gitlab.com"}); !errors.As(err, &ae) || ae.Code != "not_a_tracker_url" {
		t.Fatalf("another system: %v", err)
	}

	env, _ := trackerEnv(t, func(c *config.Config) { c.GitHubToken, c.GitHubHost = config.NewSecret(plantedEnv), gheHost })
	useFake(env.srv, f)
	c, err = env.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: gheHost})
	if err != nil || c.Fingerprint() != fingerprintOf(plantedEnv) {
		t.Fatalf("the environment's: %v %v", c, err)
	}
	if acct, err := env.srv.trackers.gh.Whoami(t.Context(), c); err != nil || acct.Login != "mona-example" {
		t.Fatalf("the environment's credential at GitHub: %v %v", acct, err)
	}
	if _, err := env.srv.credentialFor(t.Context(), ref); !errors.As(err, &ae) || ae.Code != "no_token" {
		t.Fatalf("another host than the environment's: %v", err)
	}
}

// The three routes are the admin's alone: no token, a share link of each
// role, a session's token, a host token and a Brakeman turn are refused,
// GitHub is never asked and nothing is kept; a switchyard answers 403.
func TestTrackerRoutesAdminOnly(t *testing.T) {
	b := brakemanEnv(t, nil)
	f := fakeGitHub(t)
	useFake(b.srv, f)
	sid := b.createSession("cat")
	tokens := map[string]string{"none": "", "session": b.agentToken(sid), "host": "test-host-token"}
	for _, role := range []string{"view", "control"} {
		status, data, out := b.callAs(t, adminToken, "POST", "/api/sessions/"+sid+"/links", map[string]any{"role": role})
		if status != http.StatusCreated {
			t.Fatalf("a %s link: %d %s", role, status, data)
		}
		tokens["link "+role] = out["token"].(string)
	}
	b.enable(t, "bk", nil)
	bsid, btok, _ := b.turnSession(t, b.briefing(t))
	routes := []struct {
		method, path string
		body         any
	}{{"GET", "/api/trackers", nil}, {"POST", "/api/trackers/github", map[string]any{"token": plantedToken}}, {"POST", "/api/trackers/github/remove", map[string]any{}}}
	for _, r := range routes {
		for who, tok := range tokens {
			if status, data, _ := b.callAs(t, tok, r.method, r.path, r.body); status != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: %d %s", r.method, r.path, who, status, data)
			}
		}
		raw, _ := json.Marshal(r.body)
		if status, data, _ := b.callAsTurn(t, btok, bsid, r.method, r.path, raw); status != http.StatusUnauthorized {
			t.Errorf("%s %s as a Brakeman turn: %d %s", r.method, r.path, status, data)
		}
	}
	if len(f.Requests()) != 0 {
		t.Fatal("GitHub was asked for a caller that is not the admin")
	}
	if _, err := os.Lstat(filepath.Join(b.srv.store.Dir(), trackerDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a caller that is not the admin made credentials/: %v", err)
	}

	yard, _ := trackerEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, r := range routes {
		if status, data, out := yard.callAs(t, adminToken, r.method, r.path, r.body); status != http.StatusForbidden || codeOf(out) != "switchyard" {
			t.Errorf("%s %s on a switchyard: %d %s", r.method, r.path, status, data)
		}
	}
}

// post is a raw POST for a goroutine of the test's: its status and its
// body, or the error, never a t.Fatal.
func (e *testEnv) post(path string, body any) (int, map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", e.http.URL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	err = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, err
}

// A save held in its check (on the fake's gate) while the host's token is
// removed is refused once released (409 changed) and keeps nothing, so a
// check that finishes after a remove never brings the token back; two
// hosts saved at once are both kept, and a remove of one beside a save of
// the other leaves each as its last act left it.
func TestTrackerSaveRaceRemove(t *testing.T) {
	e, f := trackerEnv(t, nil)
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	gate := f.Hold("GET", "api.github.com", "/user")
	type answer struct {
		status int
		out    map[string]any
		err    error
	}
	held := make(chan answer, 1)
	go func() {
		status, out, err := e.post("/api/trackers/github", map[string]any{"token": plantedToken2})
		held <- answer{status, out, err}
	}()
	<-gate.Reached()
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{}); status != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove: %d %s", status, data)
	}
	gate.Open()
	if a := <-held; a.err != nil || a.status != http.StatusConflict || codeOf(a.out) != "changed" {
		t.Fatalf("the held save: %d %v %v", a.status, a.out, a.err)
	}
	if l := e.trackersListed(t); len(l) != 0 {
		t.Fatalf("the removed host is kept again: %v", l)
	}
	// A token checked and not kept leaves no record of what GitHub said.
	e.srv.trackers.mu.Lock()
	s, recorded := e.srv.trackers.seen["github.com"]
	e.srv.trackers.mu.Unlock()
	if recorded {
		t.Fatalf("a token not kept is recorded: %v", s.fingerprint == fingerprintOf(plantedToken2))
	}

	acts := func(fs ...func() (int, map[string]any, error)) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, len(fs))
		for _, fn := range fs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if status, out, err := fn(); err != nil || status != http.StatusOK {
					errs <- errors.New(http.StatusText(status) + " " + codeOf(out))
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	}
	save := func(host, tok string) func() (int, map[string]any, error) {
		return func() (int, map[string]any, error) {
			return e.post("/api/trackers/github", map[string]any{"host": host, "token": tok})
		}
	}
	acts(save("ghe-1.example.com", plantedToken), save("ghe-2.example.com", plantedToken))
	acts(func() (int, map[string]any, error) {
		return e.post("/api/trackers/github/remove", map[string]any{"host": "ghe-1.example.com"})
	}, save("ghe-2.example.com", plantedToken2))
	l := e.trackersListed(t)
	if len(l) != 1 || l[0]["host"] != "ghe-2.example.com" || l[0]["fingerprint"] != fingerprintOf(plantedToken2) {
		t.Fatalf("after the two acts at once: %v", l)
	}
}

// A token kept in Settings for a host, then given for that host by the
// environment after a restart: both are listed, the kept one unused; the
// environment's is credentialFor's before and after a remove, which drops
// the file's entry and says the environment's is still in use; a save for
// that host stays refused.
func TestTrackerEnvironmentHostKeptInSettings(t *testing.T) {
	e, f := trackerEnv(t, nil)
	if status, data, _ := e.saveToken(t, gheHost, plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	stopEnv(t, e)
	e.srv.cfg.GitHubToken, e.srv.cfg.GitHubHost = config.NewSecret(plantedEnv), gheHost
	e = e.restart()
	useFake(e.srv, f)
	ref := tracker.Ref{System: tracker.GitHub, Host: gheHost}
	used := func(when string) {
		t.Helper()
		if c, err := e.srv.credentialFor(t.Context(), ref); err != nil || c.Fingerprint() != fingerprintOf(plantedEnv) {
			t.Fatalf("%s, credentialFor: %v %v", when, c, err)
		}
	}
	used("before the remove")
	l := e.trackersListed(t)
	if len(l) != 2 || l[0]["source"] != "settings" || l[0]["state"] != "unused" || l[0]["fingerprint"] != fingerprintOf(plantedToken) || l[1]["source"] != "environment" || l[1]["fingerprint"] != fingerprintOf(plantedEnv) {
		t.Fatalf("listed: %v", l)
	}
	status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": gheHost})
	if status != http.StatusOK || out["removed"] != true || out["inUse"] != "environment" || !strings.Contains(fmt.Sprint(out["message"]), "still the one in use") {
		t.Fatalf("remove: %d %s", status, data)
	}
	if b, err := os.ReadFile(e.credentialsFile()); err != nil || bytes.Contains(b, []byte(plantedToken)) {
		t.Fatalf("the file after the remove: %v, holds the token: %v", err, bytes.Contains(b, []byte(plantedToken)))
	}
	used("after the remove")
	if l := e.trackersListed(t); len(l) != 1 || l[0]["source"] != "environment" {
		t.Fatalf("listed after the remove: %v", l)
	}
	if status, data, out := e.saveToken(t, gheHost, plantedToken); status != http.StatusConflict || codeOf(out) != "environment" {
		t.Fatalf("a save for the environment's host: %d %s", status, data)
	}
}

// Four hosts' entries with every field at its bound (the longest host, a
// token of 255 bytes each of which the encoding doubles, an account at the
// bounds of tracker.Account.Check) fit well inside what read takes; a
// write past it is refused, the old file left whole and still read.
func TestTrackerFileBounds(t *testing.T) {
	e, _ := trackerEnv(t, nil)
	k := e.srv.trackers
	scopes := make([]string, tracker.MaxScopes)
	for i := range scopes {
		scopes[i] = strings.Repeat("s", tracker.MaxScope)
	}
	var full trackerFile
	for i := range maxTrackerHosts {
		l := strings.Repeat(string(rune('a'+i)), 63)
		host := l + "." + l + "." + l + "." + strings.Repeat("z", 61)
		if tracker.CheckHost(host, false) != nil || len(host) != 253 {
			t.Fatalf("not the longest host: %d", len(host))
		}
		full.GitHub = append(full.GitHub, trackerEntry{
			Host: host, Token: keptToken(strings.Repeat(`"`, maxTrackerToken)), Fingerprint: strings.Repeat("f", 12),
			Login: strings.Repeat("l", tracker.MaxLogin), AccountID: tracker.RemoteID(strings.Repeat("9", tracker.MaxRemoteID)),
			Scopes: scopes, SavedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		})
		if err := (tracker.Account{Login: full.GitHub[i].Login, ID: full.GitHub[i].AccountID, Scopes: scopes}).Check(); err != nil {
			t.Fatalf("not an account at its bounds: %v", err)
		}
	}
	b, err := store.Encode(full)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the fullest file: %d bytes, read takes %d", len(b), maxTrackerFile)
	if len(b) > maxTrackerFile*3/4 {
		t.Fatalf("the fullest file is %d bytes, more than three quarters of %d", len(b), maxTrackerFile)
	}
	k.mu.Lock()
	err = k.write(full)
	k.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k.read(); err != nil || len(got.GitHub) != maxTrackerHosts {
		t.Fatalf("the fullest file read back: %d %v", len(got.GitHub), err)
	}
	before, _ := os.ReadFile(e.credentialsFile())
	past := trackerFile{GitHub: slices.Clone(full.GitHub)}
	past.GitHub[0].Login = strings.Repeat("l", maxTrackerFile)
	k.mu.Lock()
	err = k.write(past)
	k.mu.Unlock()
	if !errors.Is(err, errTrackerFileFull) {
		t.Fatalf("a file past read's bound written: %v", err)
	}
	if after, _ := os.ReadFile(e.credentialsFile()); !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}
	if l := e.trackersListed(t); len(l) != maxTrackerHosts {
		t.Fatalf("listed after a refused write: %d", len(l))
	}
}

// The read refuses, naming only the file, everything that is not the
// private regular file it keeps, and never waits on what is at the name:
// one table, a row per refusal. A rename between its look and its open
// (afterLook) is read through: the new file is answered; a link or a named
// pipe put there is refused at once.
func TestTrackerReadRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes, links and named pipes")
	}
	const secret = "ghp_PLANTEDinTHEfileTOKEN0123456789abWXYZ"
	valid := func(host string) []byte {
		b, err := store.Encode(trackerFile{GitHub: []trackerEntry{{Host: host, Token: keptToken(secret), Fingerprint: fingerprintOf(secret), Login: "mona", AccountID: "1"}}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// fresh is a keep over a new data directory with credentials/ at 0700.
	fresh := func(t *testing.T) (*trackerKeep, string) {
		t.Helper()
		dir := t.TempDir()
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		creds := filepath.Join(dir, trackerDir)
		if err := os.Mkdir(creds, 0o700); err != nil {
			t.Fatal(err)
		}
		return newTrackers(&config.Config{}, st, slog.New(slog.DiscardHandler)), creds
	}
	put := func(t *testing.T, path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// replace renames a new file of b over path, as a save does.
	replace := func(t *testing.T, path string, b []byte) {
		t.Helper()
		put(t, path+".new", b)
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	// readBounded is k.read under k.mu, as a remove makes it, failing by a
	// bound of its own when it waits; the lock is free once it answers.
	readBounded := func(t *testing.T, k *trackerKeep, unblock func()) (trackerFile, error) {
		t.Helper()
		type answer struct {
			f   trackerFile
			err error
		}
		done := make(chan answer, 1)
		go func() {
			k.mu.Lock()
			f, err := k.read()
			k.mu.Unlock()
			done <- answer{f, err}
		}()
		select {
		case a := <-done:
			if !k.mu.TryLock() {
				t.Fatal("the read's lock is held after it answered")
			}
			k.mu.Unlock()
			return a.f, a.err
		case <-time.After(testWait):
			if unblock != nil {
				unblock()
			}
			select {
			case <-done:
			case <-time.After(testWait):
			}
			t.Fatalf("the read waited %s on what is at the file's name, under the keep's lock", testWait)
			return trackerFile{}, nil
		}
	}
	// unblockPipe opens a named pipe for writing, which lets a read that
	// waits on it go on.
	unblockPipe := func(path string) func() {
		return func() {
			if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
	}
	for _, c := range []struct {
		name  string
		set   func(t *testing.T, creds, file string) func()
		words string
	}{
		{"a link", func(t *testing.T, creds, file string) func() {
			put(t, filepath.Join(creds, "real.json"), valid("github.com"))
			return nil
		}, "is not a regular file"},
		{"a named pipe", func(t *testing.T, creds, file string) func() {
			if err := syscall.Mkfifo(file, 0o600); err != nil {
				t.Fatal(err)
			}
			return unblockPipe(file)
		}, "is not a regular file"},
		{"a directory", func(t *testing.T, creds, file string) func() {
			if err := os.Mkdir(file, 0o700); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "is not a regular file"},
		{"credentials a file", func(t *testing.T, creds, file string) func() {
			if err := os.Remove(creds); err != nil {
				t.Fatal(err)
			}
			put(t, creds, valid("github.com"))
			return nil
		}, "is not a directory"},
		{"credentials a link", func(t *testing.T, creds, file string) func() {
			real := creds + "-real"
			if err := os.Rename(creds, real); err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(real, githubFile), valid("github.com"))
			if err := os.Symlink(real, creds); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "is not a directory"},
		{"one byte past 16 KiB", func(t *testing.T, creds, file string) func() {
			b := valid("github.com")
			put(t, file, append(b, bytes.Repeat([]byte(" "), maxTrackerFile+1-len(b))...))
			return nil
		}, "is more than 16384 bytes"},
		{"a host's entry twice", func(t *testing.T, creds, file string) func() {
			b, _ := store.Encode(trackerFile{GitHub: []trackerEntry{{Host: "github.com", Token: "a"}, {Host: "github.com", Token: "b"}}})
			put(t, file, b)
			return nil
		}, "holds a host's entry twice"},
		{"an unknown member", func(t *testing.T, creds, file string) func() {
			put(t, file, []byte(`{"github":[],"`+secret+`":1}`))
			return nil
		}, "cannot be read as a token file"},
		{"a file that does not decode", func(t *testing.T, creds, file string) func() {
			put(t, file, []byte(`{"github":[{"host":"github.com","token":"`+secret+"\x01"))
			return nil
		}, "cannot be read as a token file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, creds := fresh(t)
			file := filepath.Join(creds, githubFile)
			unblock := c.set(t, creds, file)
			if c.name == "a link" {
				if err := os.Symlink("real.json", file); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readBounded(t, k, unblock)
			if err == nil || !strings.Contains(err.Error(), c.words) || strings.Contains(err.Error(), secret[4:16]) {
				t.Fatalf("%v, not %q", err, c.words)
			}
		})
	}
	// Each bit of the group's and the others' on the directory (0700) and
	// on the file (0600).
	for _, bit := range []os.FileMode{0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		for _, which := range []string{"directory", "file"} {
			mode := 0o700 | bit
			if which == "file" {
				mode = 0o600 | bit
			}
			t.Run(fmt.Sprintf("the %s at %04o", which, mode), func(t *testing.T) {
				k, creds := fresh(t)
				file := filepath.Join(creds, githubFile)
				put(t, file, valid("github.com"))
				path := creds
				if which == "file" {
					path = file
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
				if _, err := readBounded(t, k, nil); err == nil || !strings.Contains(err.Error(), "open to its group or others") {
					t.Fatalf("%04o: %v", mode, err)
				}
			})
		}
	}
	t.Run("16 KiB", func(t *testing.T) {
		k, creds := fresh(t)
		b := valid("github.com")
		put(t, filepath.Join(creds, githubFile), append(b, bytes.Repeat([]byte(" "), maxTrackerFile-len(b))...))
		if f, err := readBounded(t, k, nil); err != nil || len(f.GitHub) != 1 {
			t.Fatalf("a file of 16 KiB: %v %v", f, err)
		}
	})

	// Between the look and the open, through the seam.
	t.Run("a rename between the look and the open", func(t *testing.T) {
		k, creds := fresh(t)
		file := filepath.Join(creds, githubFile)
		put(t, file, valid("github.com"))
		looks := 0
		k.afterLook = func() {
			if looks++; looks == 1 {
				replace(t, file, valid(gheHost))
			}
		}
		if f, err := readBounded(t, k, nil); err != nil || len(f.GitHub) != 1 || f.GitHub[0].Host != gheHost {
			t.Fatalf("the read across a rename: %v %v", f, err)
		}
	})
	t.Run("a rename at every look", func(t *testing.T) {
		k, creds := fresh(t)
		file := filepath.Join(creds, githubFile)
		put(t, file, valid("github.com"))
		looks := 0
		k.afterLook = func() { looks++; replace(t, file, valid(gheHost)) }
		if _, err := readBounded(t, k, nil); err == nil || looks != trackerReadTries {
			t.Fatalf("%d looks: %v", looks, err)
		}
	})
	for _, swap := range []string{"a link", "a named pipe"} {
		t.Run(swap+" put there between the look and the open", func(t *testing.T) {
			k, creds := fresh(t)
			file := filepath.Join(creds, githubFile)
			put(t, file, valid("github.com"))
			put(t, filepath.Join(creds, "other.json"), valid(gheHost))
			looks := 0
			k.afterLook = func() {
				if looks++; looks > 1 {
					return
				}
				if err := os.Remove(file); err != nil {
					t.Error(err)
				}
				var err error
				if swap == "a link" {
					err = os.Symlink("other.json", file)
				} else {
					err = syscall.Mkfifo(file, 0o600)
				}
				if err != nil {
					t.Error(err)
				}
			}
			var unblock func()
			if swap == "a named pipe" {
				unblock = unblockPipe(file)
			}
			_, err := readBounded(t, k, unblock)
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Fatalf("%v", err)
			}
			if swap == "a named pipe" && looks != 1 {
				t.Fatalf("a named pipe opened is refused at once, not after %d looks", looks)
			}
		})
	}
}

// One small piece of code keeps the token: the file's types are named in
// trackers.go alone among the package's code, and so is the one read of
// the environment's (Secret.Reveal); the module's other packages name
// neither but the configuration, which reads it from the environment.
func TestTrackerTokenKeptByOneFile(t *testing.T) {
	names := regexp.MustCompile(`\b(keptToken|trackerEntry|trackerFile|GitHubToken\.Reveal)\b`)
	err := filepath.WalkDir("../..", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != "../.." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "web" || d.Name() == "desktop") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), "../../")
		if m := names.Find(src); m != nil && rel != "internal/api/trackers.go" && !(rel == "internal/config/config.go" && string(m) == "GitHubToken.Reveal") {
			t.Errorf("%s names %s", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// muxRoutesTouchingTheToken is every route pattern the package's code
// registers whose handler reaches the token: one that names credentialFor
// or the server's trackers, or a function of the package that does, read
// from the source as the mux is given them. TestTrackerTokenSavedNeverServed
// must walk each: a route added later that reaches the token fails it
// until it is in the walk.
func muxRoutesTouchingTheToken(t *testing.T) []string {
	t.Helper()
	return routesTouching(t, "credentialFor", "trackers")
}
