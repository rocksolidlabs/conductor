package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// addRoot adds path to the server's roots through the holder, as a folder
// the configuration had named.
func (l *rootList) addRoot(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.with(pinRoot(path))
}

// checkRoot asks GET /api/roots/check of path as the admin.
func (e *testEnv) checkRoot(path string) map[string]any {
	e.t.Helper()
	resp, out := e.do("GET", "/api/roots/check?path="+url.QueryEscape(path), adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("check %q: %d %v", path, resp.StatusCode, out)
	}
	return out
}

// mkdir makes the folder p, its parents too.
func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The check says of each folder what a launch would meet there, and of a
// folder outside every root why it is not allowed or how wide it is.
func TestRootCheckSaysWhatALaunchWouldMeet(t *testing.T) {
	for _, browse := range []string{config.BrowseRoots, config.BrowseAny} {
		t.Run(browse, func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) { c.Paths.Browse = browse })
			root := e.root
			home := realRoot(t, e.srv.home)
			data := realRoot(t, mkdir(t, e.srv.cfg.DataDir))
			outside := realRoot(t, t.TempDir())
			under := mkdir(t, filepath.Join(root, "app"))
			file := filepath.Join(root, "notes.md")
			if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			leadsOut := filepath.Join(root, "out")
			if err := os.Symlink(outside, leadsOut); err != nil {
				t.Fatal(err)
			}
			// An ancestor that is a link: root/out/deeper is outside/deeper.
			deeper := mkdir(t, filepath.Join(outside, "deeper"))
			parent := realRoot(t, t.TempDir())
			comma := mkdir(t, filepath.Join(parent, "a,b"))
			lineBreak := mkdir(t, filepath.Join(parent, "a\nb"))
			spaceEnd := mkdir(t, filepath.Join(parent, "a "))
			tab := mkdir(t, filepath.Join(parent, "a\tb"))
			type want struct {
				state, real, refused, wide string
				link                       bool
			}
			for _, tc := range []struct {
				name, path string
				want       want
			}{
				{"the root itself", root, want{state: "allowed", real: root}},
				{"a folder under it", under, want{state: "allowed", real: under}},
				{"the same, written with a trailing slash", under + "/", want{state: "allowed", real: under}},
				{"an empty path: the default folder", "", want{state: "allowed", real: root}},
				{"a folder outside", outside, want{state: "outside", real: outside}},
				{"one not there", filepath.Join(root, "nope"), want{state: "missing"}},
				{"one under a file", filepath.Join(file, "x"), want{state: "missing"}},
				{"a file", file, want{state: "not_directory"}},
				{"a link inside a root leading out", leadsOut, want{state: "outside", real: outside, link: true}},
				{"a folder past a link on its way", filepath.Join(leadsOut, "deeper"), want{state: "outside", real: deeper, link: true}},
				{"a path that climbs out", root + "/../" + filepath.Base(outside), want{state: "outside", real: outside}},
				{"the home folder", home, want{state: "outside", real: home, wide: "home"}},
				{"the folder above it", filepath.Dir(home), want{state: "outside", real: filepath.Dir(home), wide: "above_home"}},
				{"the file system's root", "/", want{state: "outside", real: "/", refused: "whole_disk"}},
				{"the data directory", data, want{state: "outside", real: data, refused: "conductor_data"}},
				{"a folder inside it", mkdir(t, filepath.Join(data, "inside")), want{state: "outside", real: filepath.Join(data, "inside"), refused: "conductor_data"}},
				{"a folder that holds it", filepath.Dir(data), want{state: "outside", real: filepath.Dir(data), wide: "holds_data"}},
				{"a comma", comma, want{state: "outside", real: comma, refused: "name"}},
				{"a line break", lineBreak, want{state: "outside", real: lineBreak, refused: "name"}},
				{"a tab", tab, want{state: "outside", real: tab, refused: "name"}},
				{"a space at an end", spaceEnd, want{state: "outside", real: spaceEnd, refused: "name"}},
			} {
				out := e.checkRoot(tc.path)
				w := tc.want
				// Where an outside folder leads is said only where every
				// folder may be listed.
				if w.state != "allowed" && browse == config.BrowseRoots {
					w.real, w.link = "", false
				}
				got := want{state: str(out["state"]), real: str(out["real"]), refused: str(out["refused"]), wide: str(out["wide"]), link: out["link"] == true}
				if got != w {
					t.Errorf("%s (%q): %+v, want %+v (%v)", tc.name, tc.path, got, w, out)
				}
			}
		})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// A folder under a root that can be looked at but not opened is one a launch
// refuses: unreadable, never allowed. The same folder outside is unreadable
// too, never offered to be allowed.
func TestRootCheckCallsAFolderThatCannotBeOpenedUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any folder")
	}
	e := newTestEnv(t, nil)
	for _, parent := range []string{e.root, realRoot(t, t.TempDir())} {
		shut := mkdir(t, filepath.Join(parent, "shut"))
		if err := os.Chmod(shut, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(shut, 0o755) })
		if out := e.checkRoot(shut); out["state"] != "unreadable" {
			t.Errorf("%s: %v", shut, out)
		}
		resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": shut})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a launch in %s: %d %v", shut, resp.StatusCode, out)
		}
	}
}

// The check's own bounds: a path of 4097 bytes, a NUL, a query that cannot
// be read are 400; 4096 bytes are checked.
func TestRootCheckBoundsItsPath(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, q := range []string{
		"path=/" + strings.Repeat("a", 4096),
		"path=/tmp%00x",
		"path=" + url.QueryEscape(e.root) + "&x=%zz",
	} {
		resp, out := e.do("GET", "/api/roots/check?"+q, adminToken, nil)
		if resp.StatusCode != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%.40s: %d %v", q, resp.StatusCode, out)
		}
	}
	if out := e.checkRoot("/" + strings.Repeat("a", 4095)); out["state"] != "missing" && out["state"] != "unreadable" {
		t.Errorf("4096 bytes: %v", out)
	}
}

// Only the admin principal may ask: no token, a share link's, a run link's,
// a host's and an agent's own are refused.
func TestRootCheckIsTheAdmins(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, share, err := e.srv.links.Create(id, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := e.srv.links.CreateRunLink("r-1a2b3c4d", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"no token": "", "a wrong token": "nope", "a share link's": share, "a run link's": run,
		"a host's": "test-host-token", "an agent's own": e.agentToken(id),
	} {
		resp, out := e.do("GET", "/api/roots/check?path="+url.QueryEscape(e.root), tok, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
}

// A folder added to the holder is one a launch takes from the next request
// on, and the check says so; the configuration's list is not what is read.
func TestALaunchReadsTheRootsHeld(t *testing.T) {
	e := newTestEnv(t, nil)
	other := realRoot(t, t.TempDir())
	if out := e.checkRoot(other); out["state"] != "outside" {
		t.Fatalf("before: %v", out)
	}
	if resp, _ := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": other}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a launch before: %d", resp.StatusCode)
	}
	e.srv.roots.addRoot(other)
	if out := e.checkRoot(other); out["state"] != "allowed" {
		t.Fatalf("after: %v", out)
	}
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": other})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a launch after: %d %v", resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(t.Context())
		}
	})
	if len(e.srv.cfg.AllowedRoots) != 1 {
		t.Fatalf("the configuration's list changed: %v", e.srv.cfg.AllowedRoots)
	}
}

// Readers take the list whole while a writer adds to it: each read is the
// first root and some of the added ones in order, never a list torn. The
// readers stop once the holder has stored every list (its count), so the
// race detector sees the reads and the writes overlap with no sleep.
func TestRootReadersRaceAWriter(t *testing.T) {
	e := newTestEnv(t, nil)
	const adds = 40
	added := make([]string, adds)
	for i := range added {
		added[i] = mkdir(t, filepath.Join(realRoot(t, t.TempDir()), "r"))
	}
	var wg sync.WaitGroup
	var bad sync.Once
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				done := e.srv.roots.stores.Load() == adds+1
				got := e.srv.roots.paths()
				ok := len(got) >= 1 && got[0] == e.root
				for i, p := range got[1:] {
					ok = ok && p == added[i]
				}
				if !ok {
					bad.Do(func() { t.Errorf("a torn read: %v", got) })
					return
				}
				if len(got) > 1 {
					if _, err := e.srv.resolveCwd(added[len(got)-2]); err != nil {
						bad.Do(func() { t.Errorf("a root read is not allowed: %v", err) })
						return
					}
				}
				if done {
					if len(got) != adds+1 {
						bad.Do(func() { t.Errorf("the last read: %d roots", len(got)) })
					}
					return
				}
			}
		}()
	}
	for _, p := range added {
		e.srv.roots.addRoot(p)
	}
	wg.Wait()
	if _, err := e.srv.resolveCwd(added[adds-1]); err != nil {
		t.Fatalf("the last root: %v", err)
	}
}

// No file of the package but roots.go reads the configuration's
// allowedRoots: once the server is made the holder is the one list, and a
// read of the configuration's elsewhere would see a list narrower than the
// one a launch checks against.
func TestOnlyRootsReadsTheConfiguredRoots(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "roots.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "AllowedRoots" {
				t.Errorf("%s reads AllowedRoots: read the roots held (s.roots)", fset.Position(sel.Pos()))
			}
			return true
		})
	}
}

// docs/protocol.md names the roots routes.
func TestRootRoutesDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(string(doc)), " ")
	for _, row := range []string{"| `GET /api/roots/check` | admin |", "| `POST /api/roots` | admin and the desktop app's key |", "`folders` `app` when the desktop app"} {
		if !strings.Contains(flat, row) {
			t.Errorf("docs/protocol.md has no row %s", row)
		}
	}
}

// secondName makes stat answer, for each path of names, the folder it maps
// to: a second name for that folder, as a mount of it elsewhere, another
// letter case or a firmlink gives one.
func secondName(e *testEnv, names map[string]string) {
	e.srv.roots.stat = func(p string) (os.FileInfo, error) {
		if to, ok := names[p]; ok {
			return os.Stat(to)
		}
		return os.Stat(p)
	}
}

// A folder is refused, or asked about, by what it is, whatever name reaches
// it: a second name for the file system's root is the whole disk, one for
// the home folder is the home folder, one for a folder above it or above
// Conductor's data is as wide as that folder.
func TestAFolderIsRefusedByWhatItIs(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) {
		c.Paths.Browse = config.BrowseAny
		withDesktopKey(c)
	})
	home := realRoot(t, e.srv.home)
	data := realRoot(t, mkdir(t, e.srv.cfg.DataDir))
	parent := realRoot(t, t.TempDir())
	disk, homeAgain, aboveHome, aboveData := mkdir(t, filepath.Join(parent, "disk")), mkdir(t, filepath.Join(parent, "home")), mkdir(t, filepath.Join(parent, "above")), mkdir(t, filepath.Join(parent, "data"))
	secondName(e, map[string]string{disk: "/", homeAgain: home, aboveHome: filepath.Dir(home), aboveData: filepath.Dir(data)})
	for _, c := range []struct{ path, refused, wide string }{
		{disk, "whole_disk", ""},
		{homeAgain, "", "home"},
		{aboveHome, "", "above_home"},
		{aboveData, "", "holds_data"},
	} {
		out := e.checkRoot(c.path)
		if out["state"] != "outside" || str(out["refused"]) != c.refused || str(out["wide"]) != c.wide {
			t.Errorf("%s: %v, want refused %q wide %q", c.path, out, c.refused, c.wide)
		}
	}
	if status, out := e.allow(disk, ""); status != http.StatusForbidden || errorField(out, "reason") != "whole_disk" {
		t.Errorf("the disk by a second name: %d %v", status, out)
	}
	if status, out := e.allow(homeAgain, ""); status != http.StatusConflict || errorField(out, "wide") != "home" {
		t.Errorf("home by a second name, unconfirmed: %d %v", status, out)
	}
	if got := e.srv.roots.paths(); len(got) != 1 {
		t.Fatalf("a folder was added: %v", got)
	}
}
