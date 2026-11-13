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
	"net/http/httptest"
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
	// The fixtures are served by a listener of their own over the server's
	// handler, which notes, for each request, the pattern that served it
	// and the times a token left the value that holds it while it was
	// served (the keep's seam, reveal and environmentToken).
	type note struct {
		pattern string
		reached uint64
	}
	notes := make(chan note, 1)
	inner := b.srv.Handler()
	fixtures := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		before := b.srv.trackers.reached.Load()
		inner.ServeHTTP(w, r)
		notes <- note{r.Pattern, b.srv.trackers.reached.Load() - before}
	}))
	fx := &testEnv{t: t, srv: b.srv, http: fixtures, root: b.root, client: fixtures.Client()}
	// admin is a fixture of a route that reaches the token, with the answer
	// it must give: the route counts as walked once that route served a
	// call of it and reached the token meanwhile, never for being called
	// alone, nor for a call another route served.
	admin := func(pattern string, path string, body string, status int, code string) map[string]any {
		t.Helper()
		method, _, _ := strings.Cut(pattern, " ")
		got, out := call(fx, adminToken, "", method, path, []byte(body))
		n := <-notes
		if got != status || (code != "" && codeOf(out) != code) {
			t.Errorf("%s %s: %d %v, not %d %s", method, path, got, out, status, code)
		}
		if n.pattern == pattern && n.reached > 0 {
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
	admin("POST /api/trackers/github/remove", "/api/trackers/github/remove", `{"host":"github.com"}`, http.StatusOK, "")
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
	if c, err := b.srv.credentialFor(t.Context(), tracker.Ref{System: tracker.GitHub, Host: "github.com"}); err != nil || c.Fingerprint() != tokenMark(plantedToken) {
		t.Errorf("github.com's credential after the answers that hold a token: %v %v", c, err)
	}
	if out := admin("POST /api/trackers/github/remove", "/api/trackers/github/remove", `{"host":"`+answerHost+`"}`, http.StatusOK, ""); out["removed"] != false {
		t.Errorf("the remove of %s: %v", answerHost, out)
	}
	// Accounts whose login is the token sent in lower case, less a byte, or
	// its base64: what is kept and shown of them is an account's form, and
	// no line of the log holds any form of them (NeverLogged).
	const echoHost = "ghe-2.example.com"
	echoes := []string{plantedEchoLower, plantedEchoLess, plantedEchoB64}
	for _, tok := range echoes {
		save(echoHost, tok, http.StatusOK, "")
	}
	admin("POST /api/trackers/github/remove", "/api/trackers/github/remove", `{"host":"`+echoHost+`"}`, http.StatusOK, "")

	// A ticket's GitHub item (IN1a PR-4): a preview, an import with its
	// images, a link, refreshes, a field given back, a link's add and read,
	// a close whose read fails and one that reads its delivery, an import's
	// resend with an image past its bound, a refresh's second read after a
	// 304, each failure of GitHub's (its refusals echo the token they were
	// sent) and a request its client cancels, all with the settings token.
	driveExternal(t, b.testEnv, newGHItems(f), admin)

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
	fixtures.Close()
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
		if auth != "" && r.Host != "api.github.com" && r.Host != gheHost && r.Host != answerHost && r.Host != echoHost {
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
		for _, tok := range echoes {
			for _, f := range echoForms(tok) {
				if bytes.Contains(lines, []byte(f)) {
					t.Errorf("the log holds %q, a form of a token an account was answered with", f)
				}
			}
		}
		if !bytes.Contains(lines, []byte(`"msg":"tracker token saved"`)) {
			t.Error("the save's line is not in the log")
		}
		for _, line := range bytes.Split(lines, []byte("\n")) {
			if bytes.Contains(line, []byte("tracker token")) && !bytes.Contains(line, []byte(`"route":"POST /api/trackers/github`)) {
				t.Errorf("a token's line names no route: %s", line)
			}
			// What a host answers is never logged: a save's line names the
			// host the admin gave and the outcome.
			if bytes.Contains(line, []byte("tracker token")) && (bytes.Contains(line, []byte(`"login"`)) || bytes.Contains(line, []byte(`"scopes"`)) || bytes.Contains(line, []byte(`"accountId"`))) {
				t.Errorf("a token's line holds what GitHub answered: %s", line)
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
// (every direct call of a mux's Handle or HandleFunc) whose handler names
// one of names or a function of the package that does, at any depth: a
// function or method by its name, a package variable by what its
// declaration and every assignment to its name (x = v) set it to. A
// Handle or HandleFunc named outside a function declaration (in a package
// variable's value) is refused as unreadable. It does not follow a value
// through a local variable or a call's result: a field of function type
// given a function that way is not judged, and a handler field given a
// local is followed to the local and no further. A call through a field
// is not followed: a field of a function type
// written as one (func(...)) given a function of the package or a literal
// that reaches the token fails the test; one of a named function type
// (http.HandlerFunc) is not judged, nor is a function reached through a
// map, a slice, a channel or an interface value made elsewhere. A field
// registered as the handler is followed to what it is given. Every
// registration's pattern is read as a literal: one written there, a
// constant written as one, or the field of each row of a table that a
// function of the package returns as a literal and the registration
// ranges over. Its handler is read as a function or method of the
// package, a function literal (its body as written), a call of either or
// of another package's function with such arguments, the table's row's
// fields, or a field of the package given a value in it, followed to that
// value. Anything else fails the test as unreadable, so that no route is
// skipped: a pattern it cannot read so, a handler held in a local variable
// or a parameter, a field given no value in the package, a value called,
// a variable of the enclosing function a function literal names, and a
// Handle or HandleFunc taken as a value rather than called. A name is a
// method or field reached through a value (s.x) or a function of the
// package (x()); a package's own (errors.New) is not the package's.
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
	type parsed struct {
		file    *ast.File
		imports map[string]bool
	}
	var files []parsed
	consts := map[string]string{}       // a package constant written as a string literal
	fields := map[string][]string{}     // a struct type's fields, in order
	funcs := map[string]*ast.FuncDecl{} // a function of the package, by name
	recvs := map[any]bool{}             // a method's receiver, by its object
	funcFields := map[string]bool{}     // a struct field of a function type written as one
	type given struct {
		e       ast.Expr
		imports map[string]bool
	}
	assigned := map[string][]given{} // what a field is given, by its name
	vars := map[string][]given{}     // what a package variable is set to
	pkgVars := map[string]bool{}     // the package's variables, by name
	setTo := map[string][]given{}    // what a name is set to by an assignment (x = v)
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	// literal reads e as a literal: written as one, or a constant (the
	// package's, or the function's own) written as one.
	literal := func(e ast.Expr) (string, bool) {
		if s, ok := str(e); ok {
			return s, true
		}
		id, ok := e.(*ast.Ident)
		if !ok {
			return "", false
		}
		if s, ok := consts[id.Name]; ok {
			return s, true
		}
		if id.Obj != nil && id.Obj.Kind == ast.Con {
			if vs, ok := id.Obj.Decl.(*ast.ValueSpec); ok {
				for i, n := range vs.Names {
					if n.Name == id.Name && i < len(vs.Values) {
						return str(vs.Values[i])
					}
				}
			}
		}
		return "", false
	}
	// rows reads the rows of the table that fd ranges over as v, each
	// row's field f its pattern: the table is what a function of the
	// package returns, a literal slice of literal rows.
	rows := func(fd *ast.FuncDecl, v, f string) ([][]ast.Expr, []string, bool) {
		var table string
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if rs, ok := n.(*ast.RangeStmt); ok {
				if id, ok := rs.Value.(*ast.Ident); ok && id.Name == v {
					if call, ok := rs.X.(*ast.CallExpr); ok {
						switch fn := call.Fun.(type) {
						case *ast.SelectorExpr:
							table = fn.Sel.Name
						case *ast.Ident:
							table = fn.Name
						}
					}
				}
			}
			return true
		})
		src := funcs[table]
		if src == nil || src.Body == nil {
			return nil, nil, false
		}
		var out [][]ast.Expr
		var patterns []string
		ok := true
		ast.Inspect(src.Body, func(n ast.Node) bool {
			ret, isRet := n.(*ast.ReturnStmt)
			if !isRet || !ok {
				return ok
			}
			var cl *ast.CompositeLit
			var elt *ast.Ident
			if len(ret.Results) == 1 {
				cl, _ = ret.Results[0].(*ast.CompositeLit)
			}
			if cl != nil {
				if arr, isArr := cl.Type.(*ast.ArrayType); isArr {
					elt, _ = arr.Elt.(*ast.Ident)
				}
			}
			if elt == nil {
				ok = false
				return false
			}
			at := slices.Index(fields[elt.Name], f)
			for _, row := range cl.Elts {
				r, isRow := row.(*ast.CompositeLit)
				if !isRow || at < 0 {
					ok = false
					return false
				}
				var p string
				var rest []ast.Expr
				found := false
				for i, x := range r.Elts {
					val := x
					key := ""
					if kv, isKV := x.(*ast.KeyValueExpr); isKV {
						val = kv.Value
						if id, isKey := kv.Key.(*ast.Ident); isKey {
							key = id.Name
						}
					}
					if key == f || key == "" && i == at {
						p, found = literal(val)
						if !found {
							ok = false
							return false
						}
						continue
					}
					rest = append(rest, val)
				}
				if !found {
					ok = false
					return false
				}
				patterns, out = append(patterns, p), append(out, rest)
			}
			return false
		})
		return out, patterns, ok && len(patterns) > 0
	}
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
	// handler reads a registration's handler (row: the table's row
	// variable, "" for none): what it names, a field it names followed to
	// the values the package gives it; false for what it cannot resolve to
	// a function or a method of the package.
	var handler func(e ast.Expr, row string, imports map[string]bool) ([]ast.Expr, bool)
	handler = func(e ast.Expr, row string, imports map[string]bool) ([]ast.Expr, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			return nil, true
		case *ast.ParenExpr:
			return handler(x.X, row, imports)
		case *ast.Ident:
			if _, ok := literal(x); ok || x.Obj == nil && (x.Name == "true" || x.Name == "false" || x.Name == "nil") {
				return nil, true
			}
			return []ast.Expr{x}, funcs[x.Name] != nil && (x.Obj == nil || x.Obj.Kind == ast.Fun)
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && (imports[id.Name] || id.Name == row) || funcs[x.Sel.Name] != nil {
				return []ast.Expr{x}, true
			}
			out := []ast.Expr{x}
			for _, g := range assigned[x.Sel.Name] {
				out = append(out, g.e)
			}
			return out, len(out) > 1
		case *ast.CallExpr:
			out, ok := handler(x.Fun, row, imports)
			if _, lit := x.Fun.(*ast.FuncLit); !lit && len(out) > 1 {
				ok = false // a field's value called: not a function of the package
			}
			for _, a := range x.Args {
				more, aok := handler(a, row, imports)
				out, ok = append(out, more...), ok && aok
			}
			return out, ok
		case *ast.FuncLit:
			// Its body is read as written; a variable of the function
			// around it (other than a method's receiver) is not followed.
			ok := true
			ast.Inspect(x.Body, func(n ast.Node) bool {
				if id, isID := n.(*ast.Ident); isID && id.Obj != nil && id.Obj.Kind == ast.Var && !recvs[id.Obj] {
					if d, isNode := id.Obj.Decl.(ast.Node); isNode && (d.Pos() < x.Pos() || d.Pos() >= x.End()) {
						ok = false
					}
				}
				return true
			})
			return []ast.Expr{x}, ok
		}
		return []ast.Expr{e}, false
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
		files = append(files, parsed{file, imports})
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil || funcs[d.Name.Name] == nil {
					funcs[d.Name.Name] = d
				}
				if d.Recv != nil {
					for _, n := range d.Recv.List[0].Names {
						recvs[n.Obj] = true
					}
				}
			case *ast.GenDecl:
				// A registration outside a function declaration (in a
				// package variable's value) is not read: it is refused.
				ast.Inspect(d, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") {
						t.Errorf("%s: %s outside a function declaration, a registration this check cannot read", fset.Position(sel.Pos()), sel.Sel.Name)
					}
					return true
				})
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.ValueSpec:
						for i, n := range sp.Names {
							if d.Tok == token.CONST && i < len(sp.Values) {
								if s, ok := str(sp.Values[i]); ok {
									consts[n.Name] = s
								}
							}
							if d.Tok == token.VAR {
								pkgVars[n.Name] = true
							}
							if d.Tok == token.VAR && i < len(sp.Values) {
								vars[n.Name] = append(vars[n.Name], given{sp.Values[i], imports})
							}
						}
					case *ast.TypeSpec:
						if st, ok := sp.Type.(*ast.StructType); ok {
							for _, fl := range st.Fields.List {
								_, isFunc := fl.Type.(*ast.FuncType)
								for _, n := range fl.Names {
									fields[sp.Name.Name] = append(fields[sp.Name.Name], n.Name)
									funcFields[n.Name] = funcFields[n.Name] || isFunc
								}
							}
						}
					}
				}
			}
		}
		// What a field is given: in a literal (f: v) or by an assignment (x.f = v).
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok {
					assigned[id.Name] = append(assigned[id.Name], given{x.Value, imports})
				}
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && len(x.Lhs) == len(x.Rhs) {
						assigned[sel.Sel.Name] = append(assigned[sel.Sel.Name], given{x.Rhs[i], imports})
					}
					if id, ok := l.(*ast.Ident); ok && x.Tok == token.ASSIGN && len(x.Lhs) == len(x.Rhs) {
						setTo[id.Name] = append(setTo[id.Name], given{x.Rhs[i], imports})
					}
				}
			}
			return true
		})
	}
	// A package variable reaches what the value it is set to names. A
	// field is followed only as a handler (handler): followed wherever it
	// is named, the name-joined graph would reach every route; a field of
	// function type is judged by what it is given instead (below). An
	// assignment to a name of a package variable is read like its
	// initialiser (a local of that name only adds to what it reaches). The
	// blank identifier is no variable: what is assigned to it (var _ = v,
	// _ = f()) is evaluated where it is written, which reads it already, and
	// is held by no name another function could read.
	for name, gs := range setTo {
		if pkgVars[name] {
			vars[name] = append(vars[name], gs...)
		}
	}
	delete(vars, "_")
	for name, gs := range vars {
		for _, g := range gs {
			if refs[name] == nil {
				refs[name] = map[string]bool{}
			}
			for k := range named(g.e, g.imports) {
				refs[name][k] = true
			}
		}
	}
	for _, pf := range files {
		imports := pf.imports
		for _, d := range pf.file.Decls {
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
			called := map[ast.Expr]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") && !called[sel] {
					t.Errorf("%s: %s taken as a value, a registration this check cannot read", fset.Position(sel.Pos()), sel.Sel.Name)
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok {
					called[sel] = true
				}
				if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") || len(call.Args) != 2 {
					return true
				}
				// A function that registers routes is no handler: what it
				// registers is judged route by route.
				registrars[fd.Name.Name] = true
				unreadable := func(what string) {
					t.Errorf("%s: a route registered with %s this check cannot read", fset.Position(call.Pos()), what)
				}
				if s, ok := literal(call.Args[0]); ok {
					h, ok := handler(call.Args[1], "", imports)
					if !ok {
						unreadable("a handler")
					}
					sites = append(sites, site{s, h, imports})
					return true
				}
				if f, ok := call.Args[0].(*ast.SelectorExpr); ok {
					if v, ok := f.X.(*ast.Ident); ok {
						if rs, ps, ok := rows(fd, v.Name, f.Sel.Name); ok {
							base, ok := handler(call.Args[1], v.Name, imports)
							for i := range rs {
								h := slices.Clone(base)
								for _, x := range rs[i] {
									more, rok := handler(x, "", imports)
									h, ok = append(h, more...), ok && rok
								}
								sites = append(sites, site{ps[i], h, imports})
							}
							if !ok {
								unreadable("a handler")
							}
							return true
						}
					}
				}
				unreadable("a pattern")
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
	// A call through a field is not followed, so a field of function type
	// may not be given a function (a function or method of the package, or
	// a function literal) that reaches the token.
	for name := range funcFields {
		for _, g := range assigned[name] {
			fn := false
			switch x := g.e.(type) {
			case *ast.FuncLit:
				fn = true
			case *ast.Ident:
				fn = funcs[x.Name] != nil
			case *ast.SelectorExpr:
				fn = funcs[x.Sel.Name] != nil
			}
			if !fn {
				continue
			}
			for k := range named(g.e, g.imports) {
				if touch[k] {
					t.Errorf("%s: the field %s of function type is given %s, which reaches the token: a call through a field is not followed", fset.Position(g.e.Pos()), name, k)
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
