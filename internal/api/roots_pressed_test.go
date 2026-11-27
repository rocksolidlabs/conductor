package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// The start's judgement of a pressed folder is the press's, and fails
// closed: any refusal, with words or without, leaves the folder unused,
// and so does a kind the server does not know.
func TestTheStartsJudgementIsThePresss(t *testing.T) {
	ok := folderVerdict{path: "/srv/a", real: "/srv/a", state: folderOutside}
	for _, c := range []struct {
		name string
		v    folderVerdict
		p    config.PressedRoot
		used bool
	}{
		{"a plain folder", ok, config.PressedRoot{Path: "/srv/a"}, true},
		{"a refusal with no words", folderVerdict{path: "/srv/a", real: "/srv/a", state: folderOutside, refused: "a-reason-of-no-words"}, config.PressedRoot{Path: "/srv/a"}, false},
		{"a kind the server does not know", ok, config.PressedRoot{Path: "/srv/a", Confirmed: "everything"}, false},
		{"wide, confirmed as it", folderVerdict{path: "/srv/a", real: "/srv/a", state: folderOutside, wide: wideHome}, config.PressedRoot{Path: "/srv/a", Confirmed: wideHome}, true},
		{"wide, confirmed as another", folderVerdict{path: "/srv/a", real: "/srv/a", state: folderOutside, wide: wideHome}, config.PressedRoot{Path: "/srv/a", Confirmed: wideAboveHome}, false},
		{"missing", folderVerdict{path: "/srv/a", state: folderMissing}, config.PressedRoot{Path: "/srv/a"}, false},
		{"through a link", folderVerdict{path: "/srv/a", real: "/srv/b", state: folderOutside}, config.PressedRoot{Path: "/srv/a"}, false},
	} {
		used, why := pressedVerdict(c.v, c.p)
		if used != c.used || (!used && why == "") {
			t.Errorf("%s: used %v, why %q", c.name, used, why)
		}
	}
	// Through the start: an unknown kind is not used.
	plain := mkdir(t, filepath.Join(realRoot(t, t.TempDir()), "plain"))
	e := newTestEnv(t, func(c *config.Config) { c.PressedRoots = []config.PressedRoot{{Path: plain, Confirmed: "everything"}} })
	if out := e.checkRoot(plain); out["state"] == "allowed" {
		t.Fatalf("a pressed folder of an unknown kind is used: %v", out)
	}
}

// After the start, a pressed folder moved away and replaced at its path,
// first by a new folder and then by a link, leads nowhere: the check does
// not say allowed, and a launch there is refused.
func TestAPressedFolderReplacedAfterTheStartLeadsNowhere(t *testing.T) {
	base := realRoot(t, t.TempDir())
	elsewhere := realRoot(t, t.TempDir())
	ok := mkdir(t, filepath.Join(base, "ok"))
	e := newTestEnv(t, func(c *config.Config) { c.PressedRoots = []config.PressedRoot{{Path: ok}} })
	if s := e.launchAt(ok); s != http.StatusCreated {
		t.Fatalf("a launch before: %d", s)
	}
	if err := os.Rename(ok, ok+".was"); err != nil {
		t.Fatal(err)
	}
	mkdir(t, ok)
	if out := e.checkRoot(ok); out["state"] == "allowed" {
		t.Fatalf("a new folder in its place: %v", out)
	}
	if s := e.launchAt(ok); s != http.StatusBadRequest {
		t.Fatalf("a launch in the new folder: %d", s)
	}
	if err := os.Remove(ok); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, ok); err != nil {
		t.Fatal(err)
	}
	if out := e.checkRoot(ok); out["state"] == "allowed" {
		t.Fatalf("a link in its place: %v", out)
	}
	if s := e.launchAt(ok); s != http.StatusBadRequest {
		t.Fatalf("a launch through the link: %d", s)
	}
}

// A later look that finds the same device and number with another owner,
// or another birth time, finds another folder: the root leads nowhere, and
// an add says the folder changed.
func TestALaterLookByOwnerAndBirth(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(folderID) folderID
	}{
		{"another owner", func(id folderID) folderID { id.uid++; return id }},
		{"another birth time", func(id folderID) folderID { id.birth += 1e9; return id }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t, withDesktopKey)
			folder := mkdir(t, filepath.Join(realRoot(t, t.TempDir()), "proj"))
			var changed atomic.Bool
			e.srv.roots.identity = func(p string, fi os.FileInfo) folderID {
				id := identify(p, fi)
				if id.birth == 0 {
					id.birth = 1
				}
				if changed.Load() && p == folder {
					return c.change(id)
				}
				return id
			}
			// The judgement's own identity, with a birth time to compare.
			if status, out := e.allow(folder, ""); status != http.StatusOK {
				t.Fatalf("allow: %d %v", status, out)
			}
			changed.Store(true)
			if s := e.launchAt(folder); s != http.StatusBadRequest {
				t.Fatalf("a launch in the folder seen as another: %d", s)
			}
			// The last look before an add.
			other := mkdir(t, filepath.Join(realRoot(t, t.TempDir()), "next"))
			changed.Store(false)
			e.srv.roots.beforeAdd = func(string) { changed.Store(true) }
			folder = other
			if status, out := e.allow(other, ""); status != http.StatusConflict || errorCode(out) != "folder_changed" {
				t.Fatalf("an add whose folder is seen as another at the last look: %d %v", status, out)
			}
		})
	}
}
