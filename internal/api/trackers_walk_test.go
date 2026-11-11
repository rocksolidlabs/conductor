package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// The walk: one distinctive value of each token (planted), and the server
// driven through every route the row adds, each way it answers (saved,
// the same again, refused, a redirect to another host, GitHub unavailable,
// rate-limited, the environment's host, a body past its bound, removed and
// saved again, answers that are no account, bodies that are not the
// route's), credentialFor's credential sent to GitHub, a session
// started with the environment's token in the server's own environment,
// a Brakeman turn, every GET route of the API as the admin and as the
// turn, an export, a sync store's commit-time copies, a restart with a
// sync store's replay of the whole state (its snapshot) and the event
// stream's snapshot. Then every reply's body and header, every log line
// (the server's and the adapter's, at debug), every event, every document
// a sync store was handed, every entry and byte of the export, every file
// of the data directory (the history's, Dispatch's and the ledger's
// databases among them; a file added later is read with the rest), the
// session's environment and the text the turn could read are searched for
// every form of every planted token (leakForms): none holds one. The one
// file that keeps the token is credentials/github.json, the settings
// token alone, mode 0600 in a directory of 0700. GitHub's fake saw a
// token only in Authorization, only at the token's own API host, never in
// a URL, a query or a body, and nothing at all at the host a redirect
// named. Every route whose handler reaches the token is in the walk
// (muxRoutesTouchingTheToken), walked by a fixture of its own that reached
// the token, never by a call alone. Its parts the plan names on their own:
// TestTrackerTokenNeverLogged (the log lines) and TestTrackerTokenNotSynced
// (the sync stores' documents) are this test's subtests.
func TestTrackerTokenSavedNeverServed(t *testing.T) {
	t.Setenv("CONDUCTOR_GITHUB_TOKEN", plantedEnv)
	logs := &logBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	providers := func(p ext.ContinuityStore) Option {
		return WithExtensions(local.Fill(ext.Providers{Continuity: p, All: []ext.Entry{{Kind: ext.KindSync, Name: p.Name(), Value: p}}}))
	}
	sync1, sync2 := newFakeSync(), newFakeSync()
	dir := syncDataDir(t)
	envDump := filepath.Join(t.TempDir(), "env.txt")
	mutate := func(c *config.Config) {
		c.DataDir = dir
		c.GitHubToken, c.GitHubHost = config.NewSecret(plantedEnv), gheHost
		c.AllowedRoots = append(c.AllowedRoots, filepath.Dir(envDump))
	}
	b := bkEnvOf(t, newTestEnvAgents(t, mutate, log, bkAgents(), providers(sync1)))
	f := fakeGitHub(t)
	useFake(b.srv, f)
	events := b.sse(t)

	var seen bytes.Buffer // every reply's header and body
	walked := map[string]bool{}
	call := func(e *testEnv, token, sid, method, path string, body []byte) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if sid != "" {
			req.Header.Set(brakemanSessionHeader, sid)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Header.Write(&seen)
		seen.Write(data)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out
	}
	// admin is a fixture of a route that reaches the token, with the answer
	// it must give: the route counts as walked once a call of it has
	// reached the token (the keep's seam counts the file's reads and
	// credentialFor's calls), never for being called alone.
	admin := func(pattern string, path string, body string, status int, code string) map[string]any {
		t.Helper()
		method, _, _ := strings.Cut(pattern, " ")
		before := b.srv.trackers.reached.Load()
		got, out := call(b.testEnv, adminToken, "", method, path, []byte(body))
		if got != status || (code != "" && codeOf(out) != code) {
			t.Errorf("%s %s: %d %v, not %d %s", method, path, got, out, status, code)
		}
		if b.srv.trackers.reached.Load() > before {
			walked[pattern] = true
		}
		return out
	}
	save := func(host, tok string, status int, code string) {
		t.Helper()
		admin("POST /api/trackers/github", "/api/trackers/github", `{"host":"`+host+`","token":"`+tok+`"}`, status, code)
	}
	save("", plantedToken, http.StatusOK, "")
	save("github.com", plantedToken, http.StatusOK, "")
	save("", plantedWrong, http.StatusUnprocessableEntity, "token_refused")
	save("", plantedMoved, http.StatusBadGateway, "tracker_unavailable")
	save("", plantedBusy, http.StatusBadGateway, "tracker_unavailable")
	save("", plantedLimited, http.StatusTooManyRequests, "rate_limited")
	save(gheHost, plantedToken, http.StatusConflict, "environment")
	admin("POST /api/trackers/github", "/api/trackers/github", `{"token":"`+plantedToken+strings.Repeat(" ", maxTrackerBody)+`"}`, http.StatusRequestEntityTooLarge, "")
	admin("POST /api/trackers/github/remove", "/api/trackers/github/remove", `{}`, http.StatusOK, "")
	save("", plantedToken, http.StatusOK, "")
	admin("GET /api/trackers", "/api/trackers", "", http.StatusOK, "")

	// Answers that are no account (a field that holds the token sent, a
	// login of 20 KiB) and a 403 that names permissions: each is refused,
	// then the list and the other hosts' credentials still answer, and a
	// remove of that host works.
	const answerHost = "ghe-1.example.com"
	save(answerHost, plantedLoginForm, http.StatusBadGateway, "tracker_unavailable")
	save(answerHost, plantedScopeForm, http.StatusBadGateway, "tracker_unavailable")
	save(answerHost, plantedPermNames, http.StatusUnprocessableEntity, "token_refused")
	save(answerHost, plantedBig, http.StatusBadGateway, "tracker_unavailable")
	if out := admin("GET /api/trackers", "/api/trackers", "", http.StatusOK, ""); len(trackerRows(out)) != 2 {
		t.Errorf("listed after the answers that hold a token: %v", out)
	}
	if c, err := b.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: "github.com"}); err != nil || c.Fingerprint() != fingerprintOf(plantedToken) {
		t.Errorf("github.com's credential after the answers that hold a token: %v %v", c, err)
	}
	if out := admin("POST /api/trackers/github/remove", "/api/trackers/github/remove", `{"host":"`+answerHost+`"}`, http.StatusOK, ""); out["removed"] != false {
		t.Errorf("the remove of %s: %v", answerHost, out)
	}

	// Bodies that are not the route's: one fixed sentence, whatever the
	// body holds of a token (a member's name, a control byte, a bad escape,
	// a value unquoted, a second member).
	for _, route := range []string{"/api/trackers/github", "/api/trackers/github/remove"} {
		for _, body := range []string{
			`{"` + plantedToken + `":"x"}`,
			`{"token":"` + plantedToken + "\x01" + `x"}`,
			`{"token":"` + plantedToken + `\qx"}`,
			`{"token":` + plantedToken + `}`,
			`{"host":"github.com","` + plantedToken + `":"x"}`,
		} {
			status, out := call(b.testEnv, adminToken, "", "POST", route, []byte(body))
			msg, _ := out["error"].(map[string]any)
			if status != http.StatusBadRequest || codeOf(out) != "invalid_request" || msg["message"] != trackerBodyWords {
				t.Errorf("POST %s with a body not its own: %d %v", route, status, out)
			}
		}
	}

	// credentialFor's credentials, the file's and the environment's, at
	// GitHub, and the kept one refused there.
	for _, host := range []string{"github.com", gheHost} {
		c, err := b.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: host})
		if err != nil {
			t.Fatalf("credentialFor %s: %v", host, err)
		}
		if _, err := b.srv.trackers.gh.Whoami(t.Context(), c); err != nil {
			t.Fatalf("%s's credential at GitHub: %v", host, err)
		}
		log.Info("a credential", "credential", c, "printed", c.String())
	}

	// A session started while the server's own environment holds the
	// environment's token.
	// The dump is written under another name and renamed when whole.
	b.launch("sh", []string{"-c", "env > " + envDump + ".part && mv " + envDump + ".part " + envDump})
	if !waitFor(testWait, func() bool { _, err := os.Stat(envDump); return err == nil }) {
		t.Fatal("the session wrote no environment")
	}
	dump, _ := os.ReadFile(envDump)

	// A Brakeman turn, and every GET route as the admin and as the turn.
	b.enable(t, "bk", nil)
	bsid, btok, _ := b.turnSession(t, b.briefing(t))
	wild := regexp.MustCompile(`\{[a-zA-Z]+\}`)
	for _, p := range slices.Compact(slices.Sorted(slices.Values(muxPatterns(t, "/api/")))) {
		method, route, _ := strings.Cut(p, " ")
		if method != http.MethodGet || route == "/api/events" {
			continue
		}
		path := strings.ReplaceAll(route, "{id}", bsid)
		path = wild.ReplaceAllString(path, "x")
		call(b.testEnv, adminToken, "", "GET", path, nil)
		call(b.testEnv, btok, bsid, "GET", path, nil)
	}
	for _, q := range []string{"token", "ghp", "PLANTED", "github"} {
		call(b.testEnv, adminToken, "", "GET", "/api/history?q="+q, nil)
	}

	// An export, read entry by entry.
	resp, err := b.client.Do(mustRequest(t, b.http.URL+"/api/export"))
	if err != nil {
		t.Fatal(err)
	}
	archive, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d %.300s", resp.StatusCode, archive)
	}
	var exported bytes.Buffer
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, zf := range zr.File {
		if strings.Contains(strings.ToLower(zf.Name), trackerDir) {
			t.Errorf("the export holds %s", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		exported.WriteString(zf.Name + "\n")
		_, _ = io.Copy(&exported, rc)
		rc.Close()
	}

	// The sync store has every commit up to a catalog save made after the
	// tokens' (delivered in commit order, so the token's file had its turn).
	b.save(agentBody("walk"))
	rev := b.srv.store.Seq()
	sync1.until(t, "the catalog's save", func(calls []syncCall) bool {
		return slices.ContainsFunc(calls, func(c syncCall) bool { return c.d.Path == catalogFile && c.d.Rev >= rev && c.err == nil })
	})

	// The event stream up to a session started last.
	last := b.createSession("cat")
	var streamed bytes.Buffer
	deadline := time.After(testWait)
	for !strings.Contains(streamed.String(), last) {
		select {
		case ev := <-events:
			streamed.WriteString(ev + "\n")
		case <-deadline:
			t.Fatal("the event stream never told the last session")
		}
	}

	// A restart, with a sync store's replay of the whole state: the
	// stream's connection ended first, so that the server's close does
	// not wait on its handler.
	b.http.CloseClientConnections()
	stopEnv(t, b.testEnv)
	srv2, err := New(b.srv.cfg, b.srv.base, log, nil, b.srv.store, providers(sync2))
	if err != nil {
		t.Fatal(err)
	}
	e2 := b.serve(srv2)
	useFake(srv2, f)
	events2 := e2.sse(t)
	if status, out := call(e2, adminToken, "", "GET", "/api/trackers", nil); status != http.StatusOK || len(trackerRows(out)) != 2 {
		t.Fatalf("after the restart: %d %v", status, out)
	}
	sync2.until(t, "the replay", func(calls []syncCall) bool { return replayedIn(calls, 1) })
	select {
	case ev := <-events2:
		streamed.WriteString(ev + "\n")
	case <-time.After(testWait):
		t.Fatal("no snapshot on the restarted server's stream")
	}

	// The data directory, file by file.
	kept := filepath.Join(dir, trackerDir, githubFile)
	var files bytes.Buffer
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if p == kept {
			// The token itself, and so its first characters: nothing else
			// of any planted token.
			if l := leaks(data); !slices.Equal(l, []string{plantedToken, plantedToken[4:16]}) {
				t.Errorf("the kept file holds %v", l)
			}
			return nil
		}
		files.WriteString(p + "\n")
		files.Write(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{kept: 0o600, filepath.Dir(kept): 0o700} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s: %v %v, not %04o", p, fi.Mode(), err, want)
			}
		}
	}

	// GitHub saw a token only where it goes.
	for _, r := range f.Requests() {
		if r.Host == "collector.example" {
			t.Errorf("a request reached the host a redirect named: %s %s", r.Method, r.Path)
		}
		auth := r.Header.Get("Authorization")
		h := r.Header.Clone()
		h.Del("Authorization")
		var rest bytes.Buffer
		_ = h.Write(&rest)
		rest.WriteString(r.Method + " " + r.Host + r.Path + "?" + r.RawQuery + "\n")
		rest.Write(r.Body)
		if l := leaks(rest.Bytes()); len(l) > 0 {
			t.Errorf("a request to %s carried %v outside its Authorization", r.Host, l)
		}
		if auth != "" && r.Host != "api.github.com" && r.Host != gheHost && r.Host != answerHost {
			t.Errorf("a token was sent to %s", r.Host)
		}
	}

	for _, c := range []struct {
		what string
		b    []byte
	}{
		{"a reply", seen.Bytes()}, {"the event stream", streamed.Bytes()}, {"the export", exported.Bytes()},
		{"the data directory", files.Bytes()}, {"the session's environment", dump},
	} {
		if l := leaks(c.b); len(l) > 0 {
			t.Errorf("%s holds %v", c.what, l)
		}
	}
	t.Run("NeverLogged", func(t *testing.T) {
		lines := []byte(logs.String())
		if l := leaks(lines); len(l) > 0 {
			t.Errorf("the log holds %v", l)
		}
		if !bytes.Contains(lines, []byte("tracker token saved")) || !bytes.Contains(lines, []byte(`"credential":"token(`+fingerprintOf(plantedToken)+`)"`)) {
			t.Error("the save's line, with the credential's printed form, is not in the log")
		}
		for _, line := range bytes.Split(lines, []byte("\n")) {
			if bytes.Contains(line, []byte("tracker token")) && !bytes.Contains(line, []byte(`"route":"POST /api/trackers/github`)) {
				t.Errorf("a token's line names no route: %s", line)
			}
			if i := bytes.Index(line, []byte("https://")); i >= 0 && bytes.ContainsRune(line[i:], '?') {
				t.Errorf("a line carries a URL's query: %s", line)
			}
		}
	})
	t.Run("NotSynced", func(t *testing.T) {
		for _, c := range append(sync1.all(), sync2.all()...) {
			if strings.HasPrefix(strings.ToLower(c.d.Path), trackerDir) {
				t.Errorf("the sync store was handed %s", c.d.Path)
			}
			if l := leaks(c.d.Data); len(l) > 0 {
				t.Errorf("%s handed to the sync store holds %v", c.d.Path, l)
			}
		}
	})

	touching := muxRoutesTouchingTheToken(t)
	if len(touching) < 3 {
		t.Fatalf("the routes that reach the token, read from the source: %v", touching)
	}
	for _, p := range touching {
		if !walked[p] {
			t.Errorf("%s reaches the token and is not in the walk", p)
		}
	}
}

// trackerRows is a list's rows, none for an answer that is no list.
func trackerRows(out map[string]any) []any {
	l, _ := out["github"].([]any)
	return l
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	return req
}

// routesTouching is every route pattern the package's code registers
// (a call or a composite literal whose first value is a pattern) whose
// handler names one of names or a function of the package that does, at
// any depth. A name is a method or field reached through a value (s.x)
// or a function of the package (x()); a package's own (errors.New) is
// not the package's.
func routesTouching(t *testing.T, names ...string) []string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	refs := map[string]map[string]bool{}
	type site struct {
		pattern string
		h       []ast.Expr
		imports map[string]bool
	}
	var sites []site
	registrars := map[string]bool{}
	pattern := regexp.MustCompile(`^"(GET|POST|PUT|PATCH|DELETE) /`)
	var named func(n ast.Node, imports map[string]bool) map[string]bool
	named = func(n ast.Node, imports map[string]bool) map[string]bool {
		out := map[string]bool{}
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				// A field named in a literal is not a use of it.
				if _, ok := x.Key.(*ast.Ident); ok {
					for k := range named(x.Value, imports) {
						out[k] = true
					}
					return false
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && imports[id.Name] {
					return false
				}
				out[x.Sel.Name] = true
			case *ast.Ident:
				out[x.Name] = true
			}
			return true
		})
		return out
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		imports := map[string]bool{}
		for _, im := range file.Imports {
			name := path.Base(strings.Trim(im.Path.Value, `"`))
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = true
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// Methods of one name on two types are one name here: their
			// uses are joined, which can only add a route.
			if refs[fd.Name.Name] == nil {
				refs[fd.Name.Name] = map[string]bool{}
			}
			for k := range named(fd.Body, imports) {
				refs[fd.Name.Name][k] = true
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				var vals []ast.Expr
				switch x := n.(type) {
				case *ast.CallExpr:
					vals = x.Args
				case *ast.CompositeLit:
					vals = x.Elts
				}
				if len(vals) > 1 {
					if lit, ok := vals[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && pattern.MatchString(lit.Value) {
						s, _ := strconv.Unquote(lit.Value)
						sites = append(sites, site{s, vals[1:], imports})
						// A function that registers routes is no handler:
						// what it registers is judged route by route.
						registrars[fd.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	touch := map[string]bool{}
	for _, n := range names {
		touch[n] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, rs := range refs {
			if touch[fn] || registrars[fn] {
				continue
			}
			for r := range rs {
				if touch[r] {
					touch[fn], changed = true, true
					break
				}
			}
		}
	}
	var out []string
	for _, s := range sites {
	handlers:
		for _, h := range s.h {
			for n := range named(h, s.imports) {
				if touch[n] {
					out = append(out, s.pattern)
					break handlers
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
