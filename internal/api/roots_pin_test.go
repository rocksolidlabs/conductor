package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// launchAt asks for a session of cat in cwd and stops it at the test's end;
// it returns the status.
func (e *testEnv) launchAt(cwd string) int {
	e.t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": cwd})
	if id, _ := out["id"].(string); id != "" {
		e.t.Cleanup(func() {
			if d, ok := e.srv.registry.Get(id); ok {
				_ = d.Stop(e.t.Context())
			}
		})
	}
	return resp.StatusCode
}

// A configured folder stays the folder it was when the server started: a
// link put in its place, or another folder, leads nowhere a launch may go,
// while a configured folder that is a link on purpose keeps the folder it
// led to, and only that one.
func TestAConfiguredFolderStaysTheFolderItWas(t *testing.T) {
	base := realRoot(t, t.TempDir())
	elsewhere := realRoot(t, t.TempDir())
	byLink := mkdir(t, filepath.Join(base, "target"))
	link := filepath.Join(base, "link")
	if err := os.Symlink(byLink, link); err != nil {
		t.Fatal(err)
	}
	other := mkdir(t, filepath.Join(base, "other"))
	e := newTestEnv(t, func(c *config.Config) { c.AllowedRoots = append(c.AllowedRoots, link, other) })
	mkdir(t, filepath.Join(byLink, "proj"))
	mkdir(t, filepath.Join(other, "proj"))
	mkdir(t, filepath.Join(elsewhere, "proj"))
	if s := e.launchAt(filepath.Join(link, "proj")); s != http.StatusCreated {
		t.Fatalf("a folder under a configured link: %d", s)
	}
	if s := e.launchAt(filepath.Join(other, "proj")); s != http.StatusCreated {
		t.Fatalf("a folder under a configured folder: %d", s)
	}
	// The link pointed elsewhere: refused.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if s := e.launchAt(filepath.Join(link, "proj")); s != http.StatusBadRequest {
		t.Fatalf("the configured link pointed elsewhere: %d", s)
	}
	// The folder replaced by a link: refused.
	if err := os.Rename(other, other+".was"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, other); err != nil {
		t.Fatal(err)
	}
	if s := e.launchAt(filepath.Join(other, "proj")); s != http.StatusBadRequest {
		t.Fatalf("the configured folder replaced by a link: %d", s)
	}
	// The folder replaced by another folder of the same name: refused.
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(other, "proj"))
	if s := e.launchAt(filepath.Join(other, "proj")); s != http.StatusBadRequest {
		t.Fatalf("the configured folder replaced by another folder: %d", s)
	}
	if out := e.checkRoot(filepath.Join(other, "proj")); out["state"] == "allowed" {
		t.Fatalf("the check of a folder under the replaced one: %v", out)
	}
	// Put back, it is the folder it was.
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other+".was", other); err != nil {
		t.Fatal(err)
	}
	if s := e.launchAt(filepath.Join(other, "proj")); s != http.StatusCreated {
		t.Fatalf("the configured folder put back: %d", s)
	}
}

// A folder added stays the folder that was allowed: another folder made at
// its path afterwards is not it.
func TestAnAddedFolderStaysTheFolderThatWasAllowed(t *testing.T) {
	e := newTestEnv(t, withDesktopKey)
	parent := realRoot(t, t.TempDir())
	path := mkdir(t, filepath.Join(parent, "proj"))
	if status, out := e.allow(path, ""); status != http.StatusOK {
		t.Fatalf("allow: %d %v", status, out)
	}
	if s := e.launchAt(path); s != http.StatusCreated {
		t.Fatalf("a launch there: %d", s)
	}
	if err := os.Rename(path, path+".was"); err != nil {
		t.Fatal(err)
	}
	mkdir(t, path)
	if s := e.launchAt(path); s != http.StatusBadRequest {
		t.Fatalf("another folder made at its path: %d", s)
	}
	if out := e.checkRoot(path); out["state"] != "outside" {
		t.Fatalf("the check of the other folder: %v", out)
	}
}

// The folder a launch opens is the one allowed, through the open's own
// look: the root swapped for another folder of the same contents just
// after the roots were read is refused, and nothing starts there.
func TestALaunchOpensTheFolderThatWasAllowed(t *testing.T) {
	e := newTestEnv(t, nil)
	proj := mkdir(t, filepath.Join(e.root, "proj"))
	root := e.root
	onLaunch(t, "root", func() {
		if err := os.Rename(root, root+".was"); err != nil {
			t.Error(err)
		}
		mkdir(t, filepath.Join(root, "proj"))
	})
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
		_ = os.Rename(root+".was", root)
	})
	if s := e.launchAt(proj); s != http.StatusBadRequest {
		t.Fatalf("a launch whose root was swapped as it opened: %d", s)
	}
}
