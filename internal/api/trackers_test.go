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
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// Tokens GitHub's fake takes and answers with a login made of them: in
// lower case, less its last byte, and its base64. Their replies and lists
// are an account's form and no more; no log line holds any of their forms
// (echoForms). They are not among plantedAll.
const (
	plantedEchoLower = "ghp_PLANTEDechoLOWERtoken0123456789abWXYZ"
	plantedEchoLess  = "ghp_PLANTEDechoLESStoken0123456789abcWXYZ"
	plantedEchoB64   = "ghp_PLANTEDechoBASEtoken0123456789abcWXYZ"
)

// echoLogin is the login the fake answers for an echo token.
func echoLogin(tok string) string {
	switch tok {
	case plantedEchoLower:
		return strings.ToLower(tok)
	case plantedEchoLess:
		return tok[:len(tok)-1]
	}
	return base64.RawURLEncoding.EncodeToString([]byte(tok))
}

// leakForms is every form of tok a leak could take: the token, its first
// twelve characters after its type's prefix, its SHA-256 in hex (and the
// first 12 of those digits alone) and in base64, and its own base64.
func leakForms(tok string) []string {
	sum := sha256.Sum256([]byte(tok))
	return []string{tok, tok[4:16], hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:6]), base64.StdEncoding.EncodeToString(sum[:]),
		base64.StdEncoding.EncodeToString([]byte(tok)), base64.RawURLEncoding.EncodeToString([]byte(tok))}
}

// echoForms is leakForms and the echo's own: in lower case, less a byte.
func echoForms(tok string) []string {
	return append(leakForms(tok), strings.ToLower(tok), tok[:len(tok)-1], strings.ToLower(tok[4:16]))
}

// tokenMark is what a person is shown of tok: its mark, keyed by this
// process's own secret.
func tokenMark(tok string) string { return tracker.Mark(tok) }

// kept is a token held as an entry holds it.
func kept(tok string) func() string { return func() string { return tok } }

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
		case tok == plantedEchoLower || tok == plantedEchoLess || tok == plantedEchoB64:
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = io.WriteString(w, `{"login":"`+echoLogin(tok)+`","id":9000003}`)
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

func (f *fakeGH) allow(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accept[tok] = true
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
	if status != http.StatusOK || got["login"] != "mona-example" || got["fingerprint"] != tokenMark(plantedToken) || got["source"] != "settings" || got["state"] != "ok" || got["host"] != "github.com" {
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
		if l := e.trackersListed(t); len(l) < 1 || l[0]["fingerprint"] != tokenMark(plantedToken) || l[0]["state"] != "refused" || l[0]["checkedAt"] != refusedAt {
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
	if l := e.trackersListed(t); len(l) != 1 || l[0]["state"] != "ok" || l[0]["fingerprint"] != tokenMark(plantedToken2) {
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
	if row := rowOf(e.trackersListed(t), "github.com"); row["fingerprint"] != tokenMark(plantedToken2) || row["state"] != "refused" || row["checkedAt"] != refusedAt {
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
	if l := env.trackersListed(t); len(l) != 1 || l[0]["source"] != "environment" || l[0]["state"] != "unchecked" || l[0]["fingerprint"] != tokenMark(plantedEnv) {
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
	if err != nil || c.Fingerprint() != tokenMark(plantedToken) {
		t.Fatalf("credentialFor: %v %v", c, err)
	}
	if _, err := e.srv.trackers.gh.Whoami(t.Context(), c); err != nil {
		t.Fatalf("the credential at GitHub: %v", err)
	}
	// A body is one JSON object, and a remove names its host: null, a
	// number, a string, a list, no body and {} change nothing.
	before, _ := os.ReadFile(e.credentialsFile())
	for _, route := range []string{"/api/trackers/github", "/api/trackers/github/remove"} {
		for _, body := range []string{"null", "1", `"github.com"`, `["github.com"]`, "", "{}"} {
			if route == "/api/trackers/github" && body == "{}" {
				continue // a save's {} is a token of no form
			}
			status, out := e.rawPost(t, route, body)
			want, code := http.StatusBadRequest, "invalid_request"
			if body == "{}" {
				code = "invalid_host"
			}
			if msg, _ := out["error"].(map[string]any); status != want || codeOf(out) != code || (code == "invalid_request" && msg["message"] != trackerBodyWords) {
				t.Errorf("POST %s %q: %d %v", route, body, status, out)
			}
		}
	}
	if after, _ := os.ReadFile(e.credentialsFile()); !bytes.Equal(before, after) {
		t.Fatal("a body that is not the route's changed the file")
	}
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK || out["removed"] != true {
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
	if err != nil || c.Fingerprint() != tokenMark(plantedEnv) {
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

// rawPost is a POST of body as it is, as the admin.
func (e *testEnv) rawPost(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("POST", e.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
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
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK || out["removed"] != true {
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
		t.Fatalf("a token not kept is recorded: %v", s.fingerprint == tokenMark(plantedToken2))
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
	if len(l) != 1 || l[0]["host"] != "ghe-2.example.com" || l[0]["fingerprint"] != tokenMark(plantedToken2) {
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
		if c, err := e.srv.credentialFor(t.Context(), ref); err != nil || c.Fingerprint() != tokenMark(plantedEnv) {
			t.Fatalf("%s, credentialFor: %v %v", when, c, err)
		}
	}
	used("before the remove")
	l := e.trackersListed(t)
	if len(l) != 2 || l[0]["source"] != "settings" || l[0]["state"] != "unused" || l[0]["fingerprint"] != tokenMark(plantedToken) || l[1]["source"] != "environment" || l[1]["fingerprint"] != tokenMark(plantedEnv) {
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

// writeTrackerFile puts f in place of k's file as a change does (change,
// write).
func writeTrackerFile(k *trackerKeep, f trackerFile) error {
	var err error
	if ae := k.change(true, func(d *os.File, _ trackerFile) *apiError { err = k.writeTokens(d, f); return nil }); ae != nil {
		return ae
	}
	return err
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
			Host: host, keptSecret: kept(strings.Repeat(`"`, maxTrackerToken)),
			Login: strings.Repeat("l", tracker.MaxLogin), AccountID: tracker.RemoteID(strings.Repeat("9", tracker.MaxRemoteID)),
			Scopes: scopes, SavedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		})
		if err := (tracker.Account{Login: full.GitHub[i].Login, ID: full.GitHub[i].AccountID, Scopes: scopes}).Check(); err != nil {
			t.Fatalf("not an account at its bounds: %v", err)
		}
	}
	if err := writeTrackerFile(k, full); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.credentialsFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the fullest file: %d bytes, read takes %d", len(b), maxTrackerFile)
	if len(b) > maxTrackerFile*3/4 {
		t.Fatalf("the fullest file is %d bytes, more than three quarters of %d", len(b), maxTrackerFile)
	}
	if got, err := k.read(); err != nil || len(got.GitHub) != maxTrackerHosts {
		t.Fatalf("the fullest file read back: %d %v", len(got.GitHub), err)
	}
	before := b
	past := trackerFile{GitHub: slices.Clone(full.GitHub)}
	past.GitHub[0].Login = strings.Repeat("l", maxTrackerFile)
	if err := writeTrackerFile(k, past); !errors.Is(err, errTrackerFileFull) {
		t.Fatalf("a file past read's bound written: %v", err)
	}
	if after, _ := os.ReadFile(e.credentialsFile()); !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}
	if l := e.trackersListed(t); len(l) != maxTrackerHosts {
		t.Fatalf("listed after a refused write: %d", len(l))
	}
}

// One small piece of code keeps the token: the file's types are named in
// trackers.go alone among the package's code, and so is the one read of
// the environment's (Secret.Reveal); the module's other packages name
// neither but the configuration, which reads it from the environment.
func TestTrackerTokenKeptByOneFile(t *testing.T) {
	names := regexp.MustCompile(`\b(keptSecret|trackerEntry|trackerFile|trackerWire|trackerWireEntry|GitHubToken\.Reveal)\b`)
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
// registers whose handler reaches a token where it leaves the value that
// holds it (a kept entry's keptSecret, the environment's Secret.Reveal),
// or a function of the package that does, read from the source as the mux
// is given them. TestTrackerTokenSavedNeverServed must walk each: a route
// added later that reaches the token fails it until it is in the walk.
func muxRoutesTouchingTheToken(t *testing.T) []string {
	t.Helper()
	return routesTouching(t, "keptSecret", "Reveal")
}

// heldSave starts a save on e held at the fake's gate for host, and waits
// until GitHub was asked: it answers on the channel once the gate opens.
func heldSave(t *testing.T, e *testEnv, f *fakeGH, host, tok string) (*githubtest.Gate, <-chan string) {
	t.Helper()
	apiHost, path := host, "/api/v3/user"
	if host == "github.com" {
		apiHost, path = "api.github.com", "/user"
	}
	gate := f.Hold("GET", apiHost, path)
	done := make(chan string, 1)
	go func() {
		status, out, err := e.post("/api/trackers/github", map[string]any{"host": host, "token": tok})
		done <- fmt.Sprint(status, " ", codeOf(out), " ", err)
	}()
	<-gate.Reached()
	return gate, done
}

// hostsKept is the hosts the file holds, read from the file itself.
func hostsKept(t *testing.T, e *testEnv) []string {
	t.Helper()
	f, err := e.srv.trackers.read()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range f.GitHub {
		out = append(out, x.Host)
	}
	return out
}

// Two servers over one data directory: every change is made under the
// file's own lock from what the file holds then. A save held at GitHub on
// one while the other removes its host is not kept; two saves of two hosts
// at once, one on each, are both kept; a save of a host while the other
// replaces that host's token: the replace is kept, the save answers 409
// changed, and both servers list the one kept.
func TestTrackerTwoServersOneFile(t *testing.T) {
	a, f := trackerEnv(t, nil)
	b := newTestEnv(t, func(c *config.Config) { c.DataDir = a.srv.store.Dir() })
	b.srv.limiter = newRateLimiter(1e6, 1e6)
	useFake(b.srv, f)
	if status, data, _ := a.saveToken(t, "github.com", plantedToken); status != http.StatusOK {
		t.Fatalf("save on A: %d %s", status, data)
	}
	gate, held := heldSave(t, b, f, "github.com", plantedToken2)
	if status, data, out := a.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove on A: %d %s", status, data)
	}
	gate.Open()
	if got := <-held; got != "409 changed <nil>" {
		t.Fatalf("B's save across A's remove: %s", got)
	}
	if h := hostsKept(t, a); len(h) != 0 {
		t.Fatalf("the removed host is kept again: %v", h)
	}
	if _, err := a.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: "github.com"}); err == nil {
		t.Fatal("A hands out the token its admin removed")
	}

	// Two changes at once, one on each server: A's has read the file and
	// not yet written it when B's save comes to the lock (B is then told
	// it is busy, and waits), or, were there no lock, has answered.
	busy, bDone := make(chan struct{}), make(chan struct{})
	var once sync.Once
	b.srv.trackers.lockFile = func(l *os.File) error {
		err := lockTrackerFile(l)
		if errors.Is(err, errTrackerBusy) {
			once.Do(func() { close(busy) })
		}
		return err
	}
	var bAnswer string
	ka := a.srv.trackers
	if ae := ka.change(true, func(d *os.File, file trackerFile) *apiError {
		go func() {
			status, out, err := b.post("/api/trackers/github", map[string]any{"host": "ghe-2.example.com", "token": plantedToken2})
			bAnswer = fmt.Sprint(status, " ", codeOf(out), " ", err)
			close(bDone)
		}()
		select {
		case <-busy:
		case <-bDone:
		}
		file.GitHub = append(file.GitHub, trackerEntry{Host: "ghe-1.example.com", keptSecret: kept(plantedToken), Login: "mona", AccountID: "1"})
		if err := ka.writeTokens(d, file); err != nil {
			return newAPIError(http.StatusInternalServerError, "store_failed", err.Error())
		}
		return nil
	}); ae != nil {
		t.Fatal(ae)
	}
	<-bDone
	if h := hostsKept(t, b); bAnswer != "200  <nil>" || !slices.Equal(h, []string{"ghe-1.example.com", "ghe-2.example.com"}) {
		t.Fatalf("two changes at once: B answered %s, the file holds %v", bAnswer, h)
	}

	if status, data, _ := a.saveToken(t, "ghe-3.example.com", plantedToken); status != http.StatusOK {
		t.Fatalf("save on A: %d %s", status, data)
	}
	gate, held = heldSave(t, b, f, "ghe-3.example.com", plantedToken2)
	// B's check stays at its gate; A's goes through one already open.
	f.Hold("GET", "ghe-3.example.com", "/api/v3/user").Open()
	if status, data, _ := a.saveToken(t, "ghe-3.example.com", plantedEnv); status != http.StatusOK {
		t.Fatalf("the replace on A: %d %s", status, data)
	}
	gate.Open()
	if got := <-held; got != "409 changed <nil>" {
		t.Fatalf("B's save across A's replace: %s", got)
	}
	for _, e := range []*testEnv{a, b} {
		if row := rowOf(e.trackersListed(t), "ghe-3.example.com"); row["fingerprint"] != tokenMark(plantedEnv) {
			t.Fatalf("listed: %v, not the replace", row)
		}
	}
}

// Nothing but the one file keeps a token: what a change cut short left
// under the temporary file's one name goes at the next save or remove,
// and at a start; after each, the directory holds the file and the lock
// alone. On a file system without locks every change is refused in fixed
// words and nothing is written, and the list still answers.
func TestTrackerOneFileHoldsTheToken(t *testing.T) {
	e, f := trackerEnv(t, nil)
	dir := filepath.Dir(e.credentialsFile())
	names := func(when string) {
		t.Helper()
		var got []string
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range entries {
			got = append(got, x.Name())
		}
		if !slices.Equal(got, []string{githubFile, trackerLock}) {
			t.Fatalf("after %s, credentials/ holds %v", when, got)
		}
	}
	leftover := func() {
		t.Helper()
		b, _ := store.Encode(trackerWire{GitHub: []trackerWireEntry{{Host: "ghe-1.example.com", Token: plantedToken2, Login: "mona", AccountID: "1"}}})
		if err := os.WriteFile(filepath.Join(dir, trackerTemp), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if status, data, _ := e.saveToken(t, "github.com", plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	names("a save")
	leftover()
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "ghe-2.example.com"}); status != http.StatusOK || out["removed"] != false {
		t.Fatalf("a remove of nothing: %d %s", status, data)
	}
	names("a remove of nothing")
	leftover()
	if status, data, _ := e.saveToken(t, "ghe-1.example.com", plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	names("a save over a leftover")
	leftover()
	stopEnv(t, e)
	e = e.restart()
	useFake(e.srv, f)
	names("a restart")
	if status, data, out := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove: %d %s", status, data)
	}
	names("a remove")
	if err := filepath.WalkDir(e.srv.store.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(plantedToken2)) {
			t.Errorf("%s holds a token no entry keeps", p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	before, _ := os.ReadFile(e.credentialsFile())
	e.srv.trackers.lockFile = func(*os.File) error { return errTrackerNoLocks }
	for _, c := range []struct {
		path string
		body map[string]any
	}{{"/api/trackers/github", map[string]any{"host": "ghe-2.example.com", "token": plantedToken}}, {"/api/trackers/github/remove", map[string]any{"host": "ghe-1.example.com"}}} {
		if status, data, out := e.callAs(t, adminToken, "POST", c.path, c.body); status != http.StatusInternalServerError || codeOf(out) != "credentials_unusable" || !strings.Contains(string(data), "no file locks") {
			t.Errorf("%s without locks: %d %s", c.path, status, data)
		}
	}
	if after, _ := os.ReadFile(e.credentialsFile()); !bytes.Equal(before, after) {
		t.Fatal("a change without the lock wrote the file")
	}
	if l := e.trackersListed(t); len(l) != 1 || l[0]["host"] != "ghe-1.example.com" {
		t.Fatalf("the list without locks: %v", l)
	}
}

// A kept token prints nothing by any path: an entry and a whole file
// through a JSON and a text log handler, as an attribute and inside a
// struct; through the formatter's every verb, alone, by a pointer and
// inside a struct with unexported fields; through the JSON encoder.
func TestTrackerKeptTokenPrintsNothing(t *testing.T) {
	const tok = "ghp_PLANTEDprintedTOKEN0123456789abcdWXYZ"
	e := trackerEntry{Host: "github.com", keptSecret: kept(tok), Login: "mona", AccountID: "1", Scopes: []string{"repo"}}
	f := trackerFile{GitHub: []trackerEntry{e}}
	type holder struct {
		F     trackerFile
		inner trackerFile
		e     trackerEntry
		P     *trackerEntry
	}
	h := holder{f, f, e, &e}
	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		for _, v := range []any{e, &e, f, &f, h, &h, []trackerEntry{e}, map[string]trackerEntry{"x": e}} {
			out = append(out, fmt.Sprintf(verb, v))
		}
	}
	for _, mk := range []func(io.Writer) slog.Handler{
		func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) },
		func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) },
	} {
		var b bytes.Buffer
		slog.New(mk(&b)).Info("x", "entry", e, "file", f, "held", h, "ptr", &e, slog.Any("group", slog.GroupValue(slog.Any("f", f))))
		out = append(out, b.String())
	}
	for _, v := range []any{e, f, h} {
		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}
	for _, s := range out {
		for _, form := range []string{tok, tok[4:16]} {
			if strings.Contains(s, form) {
				t.Fatalf("printed: %s", s)
			}
		}
	}
}

// What is shown is the latest answer by when GitHub was asked: a check
// that accepts the kept token asked first, one that refuses it asked
// second, the second recorded first: after both, the list shows refused,
// with the second's time.
func TestTrackerLatestAnswerShown(t *testing.T) {
	e, f := trackerEnv(t, nil)
	var tick atomic.Int64
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	e.srv.trackers.now = func() time.Time { return base.Add(time.Duration(tick.Add(1)) * time.Second) }
	const host = "ghe-2.example.com"
	if status, data, _ := e.saveToken(t, host, plantedToken); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, data)
	}
	gate, first := heldSave(t, e, f, host, plantedToken)
	f.deny(plantedToken)
	f.Hold("GET", host, "/api/v3/user").Open()
	if status, data, _ := e.saveToken(t, host, plantedToken); status != http.StatusUnprocessableEntity {
		t.Fatalf("the second check: %d %s", status, data)
	}
	row := rowOf(e.trackersListed(t), host)
	if row["state"] != "refused" {
		t.Fatalf("after the second: %v", row)
	}
	f.allow(plantedToken)
	gate.Open()
	if got := <-first; got != "200  <nil>" {
		t.Fatalf("the first check: %s", got)
	}
	if after := rowOf(e.trackersListed(t), host); after["state"] != "refused" || after["checkedAt"] != row["checkedAt"] {
		t.Fatalf("after both: %v, not refused at %v", after, row["checkedAt"])
	}
}

// A host is a name of at least two labels: the two fields of a save
// swapped, and a remove naming one label, are refused before anything is
// asked, and no line of the log holds the value.
func TestTrackerHostIsAName(t *testing.T) {
	const hexTok = "d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0d"
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	f := fakeGitHub(t)
	useFake(e.srv, f)
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/api/trackers/github", map[string]any{"host": hexTok, "token": "github.com"}},
		{"/api/trackers/github", map[string]any{"host": plantedToken, "token": "github.com"}},
		{"/api/trackers/github/remove", map[string]any{"host": hexTok}},
		{"/api/trackers/github/remove", map[string]any{"host": "ghe"}},
	} {
		if status, data, out := e.callAs(t, adminToken, "POST", c.path, c.body); status != http.StatusBadRequest || codeOf(out) != "invalid_host" || strings.Contains(string(data), hexTok[:12]) {
			t.Errorf("%s %v: %d %s", c.path, c.body, status, data)
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Fatalf("GitHub was asked %d times for a host of one label", n)
	}
	if out := logs.String(); strings.Contains(out, hexTok[:12]) || strings.Contains(out, plantedToken[4:16]) || strings.Contains(out, `"host":"ghe"`) {
		t.Fatalf("the log holds a refused host:\n%s", out)
	}
}
