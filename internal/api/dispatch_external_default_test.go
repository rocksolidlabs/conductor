package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/gitrepo"
)

// The tests of a short form's default repository (projectItem): the
// project's folder's origin, read by gitrepo.Remote. Every folder is made
// under the test's own folder (e.root is a t.TempDir), its git data
// written as git keeps it; no git process runs.

// gitFolder is a repository at e.root/name whose origin is origin, and a
// linked worktree of it at e.root/name-wt: the folder, the worktree, and
// the paths of the configuration, the worktree's .git file and its
// commondir.
type gitFolder struct{ dir, wt, config, dotgit, commondir string }

func newGitFolder(t *testing.T, e *testEnv, name, origin string) gitFolder {
	t.Helper()
	g := gitFolder{dir: filepath.Join(e.root, name), wt: filepath.Join(e.root, name+"-wt")}
	gitdir := filepath.Join(g.dir, ".git", "worktrees", "wt")
	for _, d := range []string{filepath.Join(g.dir, ".git", "objects"), filepath.Join(g.dir, ".git", "refs", "heads"), gitdir, g.wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g.config, g.dotgit, g.commondir = filepath.Join(g.dir, ".git", "config"), filepath.Join(g.wt, ".git"), filepath.Join(gitdir, "commondir")
	for p, b := range map[string]string{filepath.Join(g.dir, ".git", "HEAD"): "ref: refs/heads/main\n", g.dotgit: "gitdir: " + gitdir + "\n",
		g.commondir: "../..\n", filepath.Join(gitdir, "HEAD"): "ref: refs/heads/wt\n"} {
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g.origin(t, origin)
	return g
}

// origin writes the folder's configuration with origin as its remote.
func (g gitFolder) origin(t *testing.T, origin string) {
	t.Helper()
	_ = os.Remove(g.config)
	if err := os.WriteFile(g.config, []byte("[remote \"origin\"]\n\turl = "+origin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bounded is call made with the test's own wait above it, which only
// bounds a hang: past it the named pipe at pipe (when given) is opened for
// writing until the call returns, so a held read goes, and the test fails.
func bounded(t *testing.T, pipe string, call func() map[string]any) map[string]any {
	t.Helper()
	done := make(chan map[string]any, 1)
	go func() { done <- call() }()
	select {
	case out := <-done:
		return out
	case <-time.After(testWait):
	}
	for pipe != "" {
		if f, err := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		select {
		case <-done:
			pipe = ""
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("the route is held by the project's folder")
	return nil
}

// A whole address never asks the project's folder: the import, the link
// and the preview of one, with the folder's configuration a named pipe,
// answer, and the reader of the folder's remote is called for none of
// them; a short form asks it once, and with the configuration unread is
// refused in the fixed words, asking GitHub nothing.
func TestWholeAddressReadsNoFolder(t *testing.T) {
	e, g := externalEnv(t)
	g.set(145, issue("I_kwDOwhole145", "Whole", t0))
	g.set(146, issue("I_kwDOwhole146", "Whole link", t0))
	f := newGitFolder(t, e, "whole", "git@github.com:octo/app.git")
	e.newDispatchProject(t, "WHL", map[string]any{"cwd": f.dir})
	var reads atomic.Int32
	e.srv.external.remote = func(ctx context.Context, dir, name string) (string, string, error) {
		reads.Add(1)
		return gitrepo.Remote(ctx, dir, name)
	}
	if err := os.Remove(f.config); err != nil || syscall.Mkfifo(f.config, 0o644) != nil {
		t.Fatal(err)
	}
	bounded(t, f.config, func() map[string]any {
		return e.importItem(t, "WHL", "https://github.com/octo/app/issues/145", opID(), http.StatusCreated, map[string]any{"images": false})
	})
	own := e.newDispatchTicket(t, "WHL", "Own", nil)
	bounded(t, f.config, func() map[string]any {
		return e.expect(t, "POST", "/api/dispatch/tickets/"+own["key"].(string)+"/external", map[string]any{"opId": opID(), "url": "https://github.com/octo/app/issues/146"}, http.StatusOK, "")
	})
	bounded(t, f.config, func() map[string]any {
		return e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "https://github.com/octo/app/issues/145", "project": "WHL"}, http.StatusOK, "")
	})
	if n := reads.Load(); n != 0 {
		t.Fatalf("a whole address read the folder's remote %d times", n)
	}
	before := len(g.f.Requests())
	out := bounded(t, f.config, func() map[string]any {
		return e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "#145", "project": "WHL"}, http.StatusBadRequest, "not_a_tracker_url")
	})
	if reads.Load() != 1 || apiMessage(out) != errNoDefault.Message || len(g.f.Requests()) != before {
		t.Fatalf("a short form with the configuration unread: %d reads, %v, %d requests", reads.Load(), out, len(g.f.Requests())-before)
	}
}

// A folder's git data that is not what git keeps there (the
// configuration, a linked worktree's .git file and its commondir, each a
// named pipe, a link to /dev/zero, a directory, or a regular file past its
// bound) refuses a short form in the fixed words, and the route answers;
// GitHub is asked nothing. The folder as git keeps it reads the item.
func TestShortFormFolderUnread(t *testing.T) {
	e, g := externalEnv(t)
	g.set(147, issue("I_kwDOshort147", "Short", t0))
	kinds := map[string]func(t *testing.T, p string){
		"a named pipe":        func(t *testing.T, p string) { must(t, syscall.Mkfifo(p, 0o644)) },
		"a link to /dev/zero": func(t *testing.T, p string) { must(t, os.Symlink("/dev/zero", p)) },
		"a directory":         func(t *testing.T, p string) { must(t, os.Mkdir(p, 0o755)) },
		"past its bound": func(t *testing.T, p string) {
			n := 4 << 10
			if filepath.Base(p) == "config" {
				n = 1 << 20
			}
			must(t, os.WriteFile(p, []byte(strings.Repeat("\n", n+1)), 0o644))
		},
	}
	i := 0
	for what, put := range kinds {
		for _, at := range []string{"config", ".git", "commondir"} {
			if what == "a directory" && at == ".git" {
				continue // a .git directory is a repository's own
			}
			i++
			key := "UN" + string(rune('A'+i))
			f := newGitFolder(t, e, strings.ToLower(key), "git@github.com:octo/app.git")
			p, cwd := map[string]string{"config": f.config, ".git": f.dotgit, "commondir": f.commondir}[at], f.wt
			if at == "config" {
				cwd = f.dir
			}
			e.newDispatchProject(t, key, map[string]any{"cwd": cwd})
			read := func() map[string]any {
				return e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "#147", "project": key}, http.StatusOK, "")
			}
			if pv := entityOf(t, bounded(t, "", read), "preview"); pv["title"] != "Short" {
				t.Fatalf("%s as git keeps it: %v", at, pv)
			}
			must(t, os.Remove(p))
			put(t, p)
			pipe := ""
			if what == "a named pipe" {
				pipe = p
			}
			before := len(g.f.Requests())
			out := bounded(t, pipe, func() map[string]any {
				_, _, out := e.call(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "#147", "project": key})
				return out
			})
			if errorCode(out) != "not_a_tracker_url" || apiMessage(out) != errNoDefault.Message || len(g.f.Requests()) != before {
				t.Errorf("%s as %s: %v, %d requests", at, what, out, len(g.f.Requests())-before)
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Every form of an origin Remote reads, through the project's folder and
// the parse, reads #n and owner/repo#n on the remote's own host and
// repository: github.com's forms there, an Enterprise Server's (with its
// token saved) on its host, the scp-like form without a user among them.
// An origin in a form Remote does not read, or on a host with no saved
// token, refuses both short forms, and GitHub is asked nothing, on any
// host.
func TestShortFormOnRemotesHost(t *testing.T) {
	e, g := externalEnv(t)
	g.set(148, issue("I_kwDOforms148", "Forms", t0))
	if status, data, _ := e.saveToken(t, gheHost, plantedToken); status != http.StatusOK {
		t.Fatalf("the Enterprise token: %d %s", status, data)
	}
	g.f.Reply("GET", gheHost, "/api/v3/repos/octo/app", http.StatusNotFound, map[string]any{"message": "Not Found"})
	f := newGitFolder(t, e, "forms", "git@github.com:octo/app.git")
	e.newDispatchProject(t, "FRM", map[string]any{"cwd": f.dir})
	lookups := func(host, path string) int { return g.count(host, path) }
	for _, c := range []struct{ origin, host, lookup string }{
		{"git@github.com:octo/app.git", "api.github.com", "/repos/octo/app"},
		{"github.com:octo/app.git", "api.github.com", "/repos/octo/app"},
		{"git@GitHub.com:octo/app", "api.github.com", "/repos/octo/app"},
		{"ssh://git@github.com:2222/octo/app.git", "api.github.com", "/repos/octo/app"},
		{"ssh://github.com/octo/app", "api.github.com", "/repos/octo/app"},
		{"https://github.com/octo/app.git", "api.github.com", "/repos/octo/app"},
		{"https://mona@github.com/octo/app/", "api.github.com", "/repos/octo/app"},
		{"http://github.com/octo/app.git", "api.github.com", "/repos/octo/app"},
		{"git://github.com:9418/octo/app.git", "api.github.com", "/repos/octo/app"},
		{"git@" + gheHost + ":octo/app.git", gheHost, "/api/v3/repos/octo/app"},
		{gheHost + ":octo/app.git", gheHost, "/api/v3/repos/octo/app"},
		{"https://" + gheHost + "/octo/app.git", gheHost, "/api/v3/repos/octo/app"},
	} {
		f.origin(t, c.origin)
		for _, short := range []string{"#148", "octo/app#148"} {
			before, all := lookups(c.host, c.lookup), len(g.f.Requests())
			_, data, _ := e.call(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": short, "project": "FRM"})
			others := 0
			for _, r := range g.f.Requests()[all:] {
				if r.Host != c.host {
					others++
				}
			}
			if lookups(c.host, c.lookup) != before+1 || others != 0 {
				t.Errorf("%s with origin %s: %d lookups on %s, %d requests elsewhere: %.200s", short, c.origin, lookups(c.host, c.lookup)-before, c.host, others, data)
			}
		}
	}
	for _, origin := range []string{"https::https://github.com/octo/app.git", "/srv/octo/app.git", "git@gitlab.example.com:octo/app.git", "git@github.com:octo/group/app.git"} {
		f.origin(t, origin)
		for _, short := range []string{"#148", "octo/app#148"} {
			before := len(g.f.Requests())
			_, data, out := e.call(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": short, "project": "FRM"})
			if errorCode(out) != "not_a_tracker_url" || len(g.f.Requests()) != before {
				t.Errorf("%s with origin %s: %d requests: %.200s", short, origin, len(g.f.Requests())-before, data)
			}
		}
	}
}
