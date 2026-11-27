package api

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// Every route reads its query string as a browser reads a form's
// (parseQuery): the parameters are separated by '&' alone, so a ';' is part
// of a value. url.ParseQuery, which r.URL.Query() calls, drops a parameter
// that holds one, and a route then answered for the value it takes when
// none is given: for a folder named a;b, the default working directory
// (#428).

// rawSemicolons escapes s as a query value with its ';' left as they are,
// as the web app's encoder (ufo's, under $fetch's query) sends one.
func rawSemicolons(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "%3B", ";")
}

// semicolonForms is s as a query value, its ';' sent as they are and
// escaped.
func semicolonForms(s string) []string {
	return []string{rawSemicolons(s), url.QueryEscape(s)}
}

// fileEntryNames returns the names a file reply's directory lists.
func fileEntryNames(file map[string]any) []string {
	names := []string{}
	raw, _ := file["entries"].([]any)
	for _, e := range raw {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	return names
}

// getNoHeader asks path with no Authorization header and returns the
// status and the body.
func (e *testEnv) getNoHeader(path string) (int, string) {
	e.t.Helper()
	resp, err := e.client.Get(e.http.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// parseQuery reads at most maxQueryParams parameters, as url.ParseQuery
// does: a query of more is refused whole, before any is read.
func TestParseQueryBoundsItsParameters(t *testing.T) {
	t.Parallel()
	at := strings.Repeat("k=v;w&", maxQueryParams-1) + "k=v;w"
	v, err := parseQuery(at)
	if err != nil || len(v["k"]) != maxQueryParams || v["k"][0] != "v;w" {
		t.Fatalf("%d parameters: %v, %d values", maxQueryParams, err, len(v["k"]))
	}
	if v, err := parseQuery(at + "&k=v"); err == nil {
		t.Fatalf("%d parameters read: %d values", maxQueryParams+1, len(v["k"]))
	}
}

// The folder a;b is listed as itself, its prefix sent with the ';' as it
// is or escaped, and an element typed with a ';' is completed; a query
// that cannot be read is refused, never listed as no prefix.
func TestPathsListAFolderWithASemicolonAsItself(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	semi := filepath.Join(root, "a;b")
	mkdirs(t, root, filepath.Join("a;b", "inner"), "a", "b")
	for _, prefix := range semicolonForms(semi + string(filepath.Separator)) {
		resp, out := e.do("GET", "/api/paths?prefix="+prefix+"&limit=10", adminToken, nil)
		if resp.StatusCode != http.StatusOK || out["dir"] != semi || !slices.Equal(entryNames(out), []string{"inner"}) {
			t.Errorf("prefix %s: %d %v", prefix, resp.StatusCode, out)
		}
	}
	for _, prefix := range semicolonForms(filepath.Join(root, "a;")) {
		resp, out := e.do("GET", "/api/paths?prefix="+prefix, adminToken, nil)
		if resp.StatusCode != http.StatusOK || out["dir"] != root || !slices.Equal(entryNames(out), []string{"a;b"}) {
			t.Errorf("prefix %s: %d %v", prefix, resp.StatusCode, out)
		}
	}
	for _, q := range []string{
		"prefix=%zz",
		"prefix=" + url.QueryEscape(semi) + "&x=%zz",
		strings.Repeat("&", maxQueryParams) + "prefix=" + url.QueryEscape(semi),
	} {
		resp, out := e.do("GET", "/api/paths?"+q, adminToken, nil)
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%.60s: %d %v", q, resp.StatusCode, out)
		}
	}
}

// The git check of a;b is of a;b itself, a repository with a commit here,
// its cwd sent with the ';' as it is or escaped, where the default
// working directory is in no repository.
func TestGitCheckChecksAFolderWithASemicolonAsItself(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	semi := filepath.Join(root, "a;b")
	gitInit(t, semi, true)
	for _, cwd := range semicolonForms(semi) {
		resp, out := e.do("GET", "/api/git/check?cwd="+cwd, adminToken, nil)
		if resp.StatusCode != http.StatusOK || out["inRepo"] != true || out["hasCommit"] != true || out["toplevel"] != semi {
			t.Errorf("cwd %s: %d %v", cwd, resp.StatusCode, out)
		}
	}
	if resp, out := e.do("GET", "/api/git/check?cwd="+url.QueryEscape(semi)+"&x=%zz", adminToken, nil); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Errorf("a query that cannot be read: %d %v", resp.StatusCode, out)
	}
}

// The roots check of a;b is of a;b itself, sent with the ';' as it is or
// escaped; a query that cannot be read is 400.
func TestRootCheckChecksAFolderWithASemicolonAsItself(t *testing.T) {
	e := newTestEnv(t, nil)
	root := realRoot(t, e.root)
	semi := filepath.Join(root, "a;b")
	mkdirs(t, root, "a;b")
	for _, path := range semicolonForms(semi) {
		resp, out := e.do("GET", "/api/roots/check?path="+path, adminToken, nil)
		if resp.StatusCode != http.StatusOK || out["state"] != "allowed" || out["real"] != semi {
			t.Errorf("path %s: %d %v", path, resp.StatusCode, out)
		}
	}
	if resp, out := e.do("GET", "/api/roots/check?path="+url.QueryEscape(semi)+"&x=%zz", adminToken, nil); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Errorf("a query that cannot be read: %d %v", resp.StatusCode, out)
	}
}

// A session's folder a;b is read as itself, its path sent with the ';' as
// it is or escaped, relative or absolute; a file in it is read raw with
// the token in the query, as the web app links one.
func TestFilesReadAFolderWithASemicolonAsItself(t *testing.T) {
	e := newTestEnv(t, nil)
	semi := filepath.Join(e.root, "a;b")
	mkdirs(t, e.root, "a;b")
	if err := os.WriteFile(filepath.Join(semi, "notes.md"), []byte("# semi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := e.createSession("cat")
	base := "/api/sessions/" + id + "/files?path="
	for _, p := range append(semicolonForms("a;b"), semicolonForms(semi)...) {
		resp, out := e.do("GET", base+p, adminToken, nil)
		file, _ := out["file"].(map[string]any)
		if resp.StatusCode != http.StatusOK || file["kind"] != "dir" || file["path"] != semi || !slices.Equal(fileEntryNames(file), []string{"notes.md"}) {
			t.Errorf("path %s: %d %v", p, resp.StatusCode, out)
		}
	}
	for _, p := range semicolonForms(filepath.Join(semi, "notes.md")) {
		if status, body := e.getNoHeader(base + p + "&raw=1&token=" + adminToken); status != http.StatusOK || body != "# semi\n" {
			t.Errorf("raw %s: %d %q", p, status, body)
		}
	}
	if resp, out := e.do("GET", base+"a;b&x=%zz", adminToken, nil); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Errorf("a query that cannot be read: %d %v", resp.StatusCode, out)
	}
}

// A number that holds a ';' is no number: the crews' page, the touched
// files' limit, a session's task page and its inbox's after refuse it,
// sent as it is or escaped, and a query that cannot be read, where each
// answered for its default before.
func TestNumbersHoldingASemicolonAreRefused(t *testing.T) {
	e := newTestEnv(t, nil)
	session := "/api/sessions/" + e.createSession("cat")
	for _, path := range []string{
		"/api/crews?offset=0;1", "/api/crews?offset=0%3B1", "/api/crews?limit=1;2", "/api/crews?x=%zz",
		"/api/touched?limit=1;2", "/api/touched?limit=1%3B2", "/api/touched?x=%zz",
		session + "/task?page=1;2", session + "/task?page=1%3B2", session + "/task?x=%zz",
		session + "/inbox?after=0;1", session + "/inbox?after=0%3B1", session + "/inbox?x=%zz",
	} {
		resp, out := e.do("GET", path, adminToken, nil)
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%s: %d %v", path, resp.StatusCode, out)
		}
	}
}

// A token in the query is read as every query value is: a ';' in it is
// part of it, sent as it is or escaped (a workbench token is the
// operator's own string), and a query that cannot be read presents none.
func TestAQueryTokenMayHoldASemicolon(t *testing.T) {
	const token = "work;bench"
	e := newTestEnv(t, func(c *config.Config) { c.WorkbenchToken = token })
	resp, out := e.do("POST", "/api/sessions", token, map[string]any{"agentId": "cat"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(t.Context())
		}
	})
	for _, q := range []string{"token=work;bench", "token=work%3Bbench", "x=a;b&token=work;bench"} {
		resp, out := e.do("GET", "/api/sessions/"+id+"?"+q, "", nil)
		if s, _ := out["session"].(map[string]any); resp.StatusCode != http.StatusOK || s["id"] != id {
			t.Errorf("%s: %d %v", q, resp.StatusCode, out)
		}
	}
	if resp, _ := e.do("GET", "/api/sessions/"+id+"?token=work%3Bbench&x=%zz", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a query that cannot be read: %d", resp.StatusCode)
	}
}

// On /ws/host no token is an open host's registration, so a query that
// cannot be read, which presents no token, is refused on a switchyard that
// admits open hosts, never registered as one: its token may be a wrong
// one, which must show. A wrong token in a query that can be read is
// still 401.
func TestHostQueryThatCannotBeReadIsRefused(t *testing.T) {
	e := openSwitchyardEnv(t, nil)
	if status, body := e.getNoHeader("/ws/host?token=wrong"); status != http.StatusUnauthorized {
		t.Errorf("a wrong token: %d %s", status, body)
	}
	for _, q := range []string{"token=wrong&x=%zz", "x=%zz", "token=%zz"} {
		if status, body := e.getNoHeader("/ws/host?" + q); status != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
			t.Errorf("%s: %d %s", q, status, body)
		}
	}
}
