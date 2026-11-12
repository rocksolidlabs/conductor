package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests of a short form's default repository (projectItem): the
// project's tracker address and nothing else.

// shortForm sends raw to the preview, the import or the link (route) in
// project: the status, the answer, and the hosts the fake was asked.
func shortForm(t *testing.T, e *testEnv, g *ghItems, project, route, raw string) (int, map[string]any, []string) {
	t.Helper()
	before := len(g.f.Requests())
	var status int
	var out map[string]any
	switch route {
	case "preview":
		status, _, out = e.call(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": raw, "project": project})
	case "import":
		status, _, out = e.call(t, "POST", "/api/dispatch/projects/"+project+"/import", map[string]any{"opId": opID(), "url": raw})
	case "link":
		own := e.newDispatchTicket(t, project, "Own", nil)["key"].(string)
		before = len(g.f.Requests())
		status, _, out = e.call(t, "POST", "/api/dispatch/tickets/"+own+"/external", map[string]any{"opId": opID(), "url": raw})
	}
	var hosts []string
	for _, r := range g.f.Requests()[before:] {
		hosts = append(hosts, r.Host)
	}
	return status, out, hosts
}

// A short form's default is the project's tracker address alone, one of
// three, the same for the preview, the import and the link: with no
// tracker address, #n is refused in its fixed words and asks nothing,
// owner/repo#n is read on github.com; with one naming a repository on a
// host the server reads, both are read there; with one set and unusable (a
// host with no saved token, or a form the parser does not read), both are
// refused in their fixed words and no host is asked. A whole address is
// read in every project.
func TestShortFormDefault(t *testing.T) {
	e, g := externalEnv(t)
	n := 160
	for _, c := range []struct {
		project, tracker string
		outcome          int
	}{
		{"NOT", "", 1},
		{"HAS", "https://github.com/octo/app", 2},
		{"GHE", "https://" + gheHost + "/octo/app", 3},
		{"BAD", "https://github.com/octo/app/issues", 3},
	} {
		fields := map[string]any{}
		if c.tracker != "" {
			fields = map[string]any{"tracker": "external", "trackerUrl": c.tracker}
		}
		e.newDispatchProject(t, c.project, fields)
		for _, form := range []string{"#%d", "octo/app#%d", "https://github.com/octo/app/issues/%d"} {
			for _, route := range []string{"preview", "import", "link"} {
				n++
				it := issue(fmt.Sprintf("I_kwDOshort%d", n), "Short", t0)
				it.Images = nil
				g.set(n, it)
				raw := fmt.Sprintf(form, n)
				status, out, hosts := shortForm(t, e, g, c.project, route, raw)
				var refused *apiError
				switch {
				case strings.Contains(raw, "://"):
				case c.outcome == 1 && strings.HasPrefix(raw, "#"):
					refused = errNoTracker
				case c.outcome == 3:
					refused = errNoDefault
				}
				if refused != nil {
					if status != http.StatusBadRequest || apiMessage(out) != refused.Message || len(hosts) != 0 {
						t.Errorf("%s, %s %s: %d %v, hosts asked %v", c.project, route, raw, status, out, hosts)
					}
					continue
				}
				read := g.count("api.github.com", itemsRepo+"/issues/"+fmt.Sprint(n))
				if status >= 300 || read == 0 || slices.ContainsFunc(hosts, func(h string) bool { return h != "api.github.com" }) {
					t.Errorf("%s, %s %s: %d %v, the item read %d times, hosts asked %v", c.project, route, raw, status, out, read, hosts)
				}
			}
		}
	}
}

// A project with no tracker address whose folder is a repository with an
// origin has no default all the same: #n is refused in its fixed words,
// and the fake is asked nothing.
func TestShortFormIgnoresFolder(t *testing.T) {
	e, g := externalEnv(t)
	g.set(180, issue("I_kwDOfolder180", "Folder", t0))
	dir := filepath.Join(e.root, "folder")
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, b := range map[string]string{"HEAD": "ref: refs/heads/main\n", "config": "[remote \"origin\"]\n\turl = git@github.com:octo/app.git\n"} {
		if err := os.WriteFile(filepath.Join(dir, ".git", p), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.newDispatchProject(t, "FLD", map[string]any{"cwd": dir})
	for _, route := range []string{"preview", "import", "link"} {
		if status, out, hosts := shortForm(t, e, g, "FLD", route, "#180"); status != http.StatusBadRequest || apiMessage(out) != errNoTracker.Message || len(hosts) != 0 {
			t.Errorf("%s #180: %d %v, hosts asked %v", route, status, out, hosts)
		}
	}
}
