package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// A folder allowed by the press is judged again at every start, as the
// press judged it: the folder its path names, its own real path, owned by
// the server's user, neither refused nor wider than it was confirmed as.
// One that fails, or is not there, is not used for that run and never
// followed by its name.
func TestAPressedFolderIsJudgedAgainAtEveryStart(t *testing.T) {
	base := realRoot(t, t.TempDir())
	ok := mkdir(t, filepath.Join(base, "ok"))
	missing := filepath.Join(base, "missing")
	target := mkdir(t, filepath.Join(base, "target"))
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pressed := []config.PressedRoot{{Path: ok}, {Path: missing}, {Path: link}, {Path: file}, {Path: "/"}}
	var logs syncLog
	e := newTestEnvLogging(t, func(c *config.Config) { c.PressedRoots = pressed }, slog.New(slog.NewTextHandler(&logs, nil)))
	if out := e.checkRoot(ok); out["state"] != "allowed" {
		t.Fatalf("the folder that is still the one allowed: %v", out)
	}
	if s := e.launchAt(ok); s != http.StatusCreated {
		t.Fatalf("a launch in it: %d", s)
	}
	for _, p := range []string{link, target, file, "/"} {
		if out := e.checkRoot(p); out["state"] == "allowed" {
			t.Errorf("%s is used: %v", p, out)
		}
	}
	// The one not there at the start is never followed by its name: made
	// afterwards, it is not allowed.
	mkdir(t, missing)
	if out := e.checkRoot(missing); out["state"] != "outside" {
		t.Fatalf("a pressed folder made after the start: %v", out)
	}
	if s := e.launchAt(missing); s != http.StatusBadRequest {
		t.Fatalf("a launch in it: %d", s)
	}
	for _, p := range []string{missing, link, file} {
		if !strings.Contains(logs.String(), "a folder allowed by the press is not used this run") || !strings.Contains(logs.String(), p) {
			t.Errorf("no line says %s is not used:\n%s", p, logs.String())
		}
	}
}

// A pressed folder that is wide is used only as the kind it was confirmed
// as; one that belongs to another user is not used at all.
func TestAPressedFolderKeepsItsKindAndItsOwner(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		t.Skip("no home folder")
	}
	home = realRoot(t, home)
	pressed := []config.PressedRoot{{Path: home}}
	e := newTestEnv(t, func(c *config.Config) { c.PressedRoots = pressed })
	if out := e.checkRoot(home); out["state"] == "allowed" {
		t.Fatalf("home pressed with no kind is used: %v", out)
	}
	e2 := newTestEnv(t, func(c *config.Config) { c.PressedRoots = []config.PressedRoot{{Path: home, Confirmed: "home"}} })
	if out := e2.checkRoot(home); out["state"] != "allowed" {
		t.Fatalf("home pressed as home: %v", out)
	}
	if os.Geteuid() == 0 {
		return
	}
	for _, other := range []string{"/usr", "/etc"} {
		fi, err := os.Stat(other)
		if err != nil || identify(other, fi).uid == os.Getuid() {
			continue
		}
		e3 := newTestEnv(t, func(c *config.Config) { c.PressedRoots = []config.PressedRoot{{Path: other}} })
		if out := e3.checkRoot(other); out["state"] == "allowed" {
			t.Fatalf("%s, another user's, pressed: %v", other, out)
		}
		if out := e3.checkRoot(other); out["refused"] != "owner" && out["refused"] != "conductor_data" {
			t.Fatalf("%s is not said to be another user's: %v", other, out)
		}
		break
	}
}

// What a folder is holds its owner and its birth time beside its device
// and number: the same number with another owner or another birth is
// another folder.
func TestAFolderIsItsNumberOwnerAndBirth(t *testing.T) {
	dir := t.TempDir()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := identify(dir, fi)
	if !id.same(id) || !id.sameStat(fi) {
		t.Fatal("a folder is not itself")
	}
	other := id
	other.uid = id.uid + 1
	if id.same(other) || other.sameStat(fi) {
		t.Fatal("another owner is the same folder")
	}
	born := id
	born.birth = id.birth + 1
	if id.birth != 0 && id.same(born) {
		t.Fatal("another birth time is the same folder")
	}
	if !id.same(folderID{fi: fi, uid: id.uid}) {
		t.Fatal("a birth time the system does not give decides")
	}
}

// A folder typed in the configuration keeps its meaning: one not there at
// the start is taken wherever its path leads once it is, as the person
// wrote it.
func TestATypedFolderNotThereAtTheStartIsTakenOnceItIs(t *testing.T) {
	later := filepath.Join(realRoot(t, t.TempDir()), "later")
	e := newTestEnv(t, func(c *config.Config) { c.AllowedRoots = append(c.AllowedRoots, later) })
	mkdir(t, later)
	if out := e.checkRoot(later); out["state"] != "allowed" {
		t.Fatalf("a typed folder made after the start: %v", out)
	}
}
