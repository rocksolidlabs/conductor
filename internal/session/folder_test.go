package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// linkTree makes base/root, the session's folder, and base/outside beside
// it, each with a folder sub holding file.txt (different words), and
// outside's sub also only-outside.txt. It returns root and outside, their
// links resolved.
func linkTree(t *testing.T) (root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub", "deep"), filepath.Join(outside, "sub", "deep")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "sub", "file.txt"), "inside\n")
	write(filepath.Join(root, "sub", "deep", "only-inside.txt"), "x")
	write(filepath.Join(outside, "sub", "file.txt"), "outside, longer\n")
	write(filepath.Join(outside, "sub", "only-outside.txt"), "x")
	write(filepath.Join(outside, "sub", "deep", "only-outside-deep.txt"), "x")
	return root, outside
}

// swapForLink moves dir aside (to dir.was) and puts a symbolic link to
// target where it was, as another process could while a request runs.
func swapForLink(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.Rename(dir, dir+".was"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
}

// setAfterCheck runs fn each time a request's checks are done and its path
// not used yet (afterCheck), for this test only.
func setAfterCheck(t *testing.T, fn func()) {
	t.Helper()
	afterCheckHook.Store(&fn)
	t.Cleanup(func() { afterCheckHook.Store(nil) })
}

// onceAfterCheck is setAfterCheck for the first time only.
func onceAfterCheck(t *testing.T, fn func()) {
	t.Helper()
	var once sync.Once
	setAfterCheck(t, func() { once.Do(fn) })
}

// linkKinds are the links a folder on the way may become: to the outside
// folder by its absolute path, and by a relative path that climbs out.
var linkKinds = []struct {
	name   string
	target func(outside string) string
}{
	{"absolute", func(outside string) string { return filepath.Join(outside, "sub") }},
	{"relative", func(string) string { return filepath.Join("..", "outside", "sub") }},
}

// A folder on a request's path that becomes a link out of the session's
// folder after the request was checked leads the read, the stat and the
// listing nowhere outside: each is refused, and nothing of the outside
// folder comes back.
func TestReadPathStaysInTheFolderWhenAFolderOnTheWayBecomesALink(t *testing.T) {
	for _, k := range linkKinds {
		for _, op := range []struct {
			name, path string
			stat       bool
		}{{"read", "sub/file.txt", false}, {"stat", "sub/file.txt", true}, {"listing", "sub", false}, {"listing below", "sub/deep", false}} {
			t.Run(k.name+" "+op.name, func(t *testing.T) {
				root, outside := linkTree(t)
				onceAfterCheck(t, func() { swapForLink(t, filepath.Join(root, "sub"), k.target(outside)) })
				h, body := ReadPath(root, op.path, op.stat, nil)
				if h.Kind != "error" || body != nil || h.Exists {
					t.Fatalf("%s %s after the folder became a link: %+v %q, want it refused", op.name, op.path, h, body)
				}
				for _, e := range h.Entries {
					if strings.Contains(e.Name, "outside") {
						t.Fatalf("the listing shows %s from outside the folder", e.Name)
					}
				}
			})
		}
	}
}

// The same for a save: a file written over and a new one (forced) are
// written neither outside the session's folder nor anywhere else, and no
// temporary file is left on either side.
func TestFileWriteStaysInTheFolderWhenAFolderOnTheWayBecomesALink(t *testing.T) {
	for _, k := range linkKinds {
		for _, op := range []struct {
			name, path string
		}{{"overwrite", "sub/file.txt"}, {"new file", "sub/new.txt"}, {"new file below", "sub/deep/new.txt"}} {
			t.Run(k.name+" "+op.name, func(t *testing.T) {
				root, outside := linkTree(t)
				s, _ := newLocal(t, root)
				sink := newChanSink(false)
				sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
				if err != nil {
					t.Fatal(err)
				}
				onceAfterCheck(t, func() { swapForLink(t, filepath.Join(root, "sub"), k.target(outside)) })
				save(s, sub, "w", op.path, []byte("saved\n"), 512, "", true)
				if f := fileReply(t, sink, "w"); f.Kind != "error" {
					t.Fatalf("save %s after the folder became a link: %+v, want it refused", op.path, f)
				}
				if b, _ := os.ReadFile(filepath.Join(outside, "sub", "file.txt")); string(b) != "outside, longer\n" {
					t.Fatalf("the outside file holds %q", b)
				}
				for _, dir := range []string{filepath.Join(outside, "sub"), filepath.Join(outside, "sub", "deep"), filepath.Join(root, "sub.was"), filepath.Join(root, "sub.was", "deep")} {
					entries, _ := os.ReadDir(dir)
					for _, e := range entries {
						if n := e.Name(); n == "new.txt" || strings.Contains(n, ".conductor-") {
							t.Fatalf("the refused save left %s in %s", n, dir)
						}
					}
				}
				if b, _ := os.ReadFile(filepath.Join(root, "sub.was", "file.txt")); string(b) != "inside\n" {
					t.Fatalf("the moved inside file holds %q", b)
				}
			})
		}
	}
}

// The find goes into no folder that became a link out after the folder
// above it was listed: it names nothing from outside.
func TestFindPathStaysInTheFolderWhenAFolderBecomesALink(t *testing.T) {
	for _, k := range linkKinds {
		t.Run(k.name, func(t *testing.T) {
			root, outside := linkTree(t)
			onceAfterCheck(t, func() { swapForLink(t, filepath.Join(root, "sub"), k.target(outside)) })
			h := FindPath(root, "only", nil)
			if h.Kind != "find" {
				t.Fatalf("find: %+v", h)
			}
			for _, m := range h.Matches {
				if strings.Contains(m, "outside") {
					t.Fatalf("the find names %s from outside the folder (%v)", m, h.Matches)
				}
			}
		})
	}
}

// A folder that becomes a link to a denied folder inside the session's
// folder (its data directory) after the check reaches nothing denied: the
// read, the listing and the save are refused, and the denied files stay as
// they were.
func TestFilesStayOutOfTheDenyListWhenAFolderBecomesALinkInside(t *testing.T) {
	root, _ := linkTree(t)
	state := filepath.Join(root, "state")
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "file.txt"), []byte("secret\n"), 0o600)
	deny := []string{state}
	for _, op := range []struct {
		name, path string
	}{{"read", "sub/file.txt"}, {"listing", "sub"}} {
		t.Run(op.name, func(t *testing.T) {
			os.RemoveAll(filepath.Join(root, "sub"))
			os.RemoveAll(filepath.Join(root, "sub.was"))
			os.MkdirAll(filepath.Join(root, "sub"), 0o755)
			os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("inside\n"), 0o644)
			onceAfterCheck(t, func() { swapForLink(t, filepath.Join(root, "sub"), "state") })
			h, body := ReadPath(root, op.path, false, deny)
			if h.Kind != "error" || strings.Contains(string(body), "secret") {
				t.Fatalf("%s through a link to the denied folder: %+v %q", op.name, h, body)
			}
		})
	}
	t.Run("save", func(t *testing.T) {
		os.RemoveAll(filepath.Join(root, "sub"))
		os.RemoveAll(filepath.Join(root, "sub.was"))
		os.MkdirAll(filepath.Join(root, "sub"), 0o755)
		os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("inside\n"), 0o644)
		p := newFakeProc()
		s := NewLocal(Info{ID: "sess", Cwd: root, Cols: 80, Rows: 24}, p, Options{ScrollbackBytes: 4096, FileDeny: deny})
		t.Cleanup(func() { p.exit() })
		sink := newChanSink(false)
		sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		onceAfterCheck(t, func() { swapForLink(t, filepath.Join(root, "sub"), "state") })
		save(s, sub, "w", "sub/file.txt", []byte("changed\n"), 512, "", true)
		if f := fileReply(t, sink, "w"); f.Kind != "error" {
			t.Fatalf("save through a link to the denied folder: %+v", f)
		}
		if b, _ := os.ReadFile(filepath.Join(state, "file.txt")); string(b) != "secret\n" {
			t.Fatalf("the denied file holds %q", b)
		}
	})
}

// A denied folder moved into the place of a folder on a request's path
// after the check (not a link: the folder itself) is still denied: the
// read, the stat, the listing and the save are refused, and its files stay
// as they were.
func TestFilesStayOutOfTheDenyListWhenADeniedFolderIsMovedIn(t *testing.T) {
	for _, op := range []struct {
		name, path string
		stat, save bool
	}{{"read", "sub/file.txt", false, false}, {"stat", "sub/file.txt", true, false}, {"listing", "sub", false, false}, {"save", "sub/file.txt", false, true}} {
		t.Run(op.name, func(t *testing.T) {
			root, _ := linkTree(t)
			state := filepath.Join(root, "state")
			os.MkdirAll(state, 0o700)
			os.WriteFile(filepath.Join(state, "file.txt"), []byte("secret\n"), 0o600)
			p := newFakeProc()
			s := NewLocal(Info{ID: "sess", Cwd: root, Cols: 80, Rows: 24}, p, Options{ScrollbackBytes: 4096, FileDeny: []string{state}})
			t.Cleanup(func() { p.exit() })
			onceAfterCheck(t, func() {
				sub := filepath.Join(root, "sub")
				os.Rename(sub, sub+".was")
				os.Rename(state, sub)
			})
			if op.save {
				sink := newChanSink(false)
				sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
				if err != nil {
					t.Fatal(err)
				}
				save(s, sub, "w", op.path, []byte("changed\n"), 512, "", true)
				if f := fileReply(t, sink, "w"); f.Kind != "error" || f.Error.Code != "denied" {
					t.Fatalf("save once the denied folder was moved in: %+v", f)
				}
			} else {
				h, body := s.ReadPath(op.path, op.stat, []string{state}, RoleControl)
				if h.Kind != "error" || h.Exists || strings.Contains(string(body), "secret") || len(h.Entries) > 0 {
					t.Fatalf("%s once the denied folder was moved in: %+v %q", op.name, h, body)
				}
			}
			if b, _ := os.ReadFile(filepath.Join(root, "sub", "file.txt")); string(b) != "secret\n" {
				t.Fatalf("the denied file holds %q", b)
			}
		})
	}
}

// Links that stay in the session's folder read, list and save as before,
// whether they are absolute or relative, and so does one that leaves the
// folder and comes back into it; one that leaves it is refused.
func TestLinksInsideTheFolderStillWork(t *testing.T) {
	root, outside := linkTree(t)
	os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "abs"))
	os.Symlink("sub", filepath.Join(root, "rel"))
	os.Symlink(filepath.Join(root, "sub"), filepath.Join(outside, "back"))
	os.Symlink(filepath.Join(outside, "back"), filepath.Join(root, "roundtrip"))
	os.Symlink(filepath.Join(outside, "sub"), filepath.Join(root, "out"))
	os.Symlink("file.txt", filepath.Join(root, "sub", "file-link.txt"))
	for _, p := range []string{"abs/file.txt", "rel/file.txt", "roundtrip/file.txt", "sub/file-link.txt", "rel/file-link.txt"} {
		h, body := ReadPath(root, p, false, nil)
		if h.Kind != "file" || string(body) != "inside\n" || h.Path != filepath.Join(root, "sub", "file.txt") {
			t.Errorf("read %s: %+v %q", p, h, body)
		}
	}
	for _, p := range []string{"abs", "rel", "roundtrip"} {
		h, _ := ReadPath(root, p, false, nil)
		if h.Kind != "dir" || !slices.ContainsFunc(h.Entries, func(e proto.FileEntry) bool { return e.Name == "file.txt" }) {
			t.Errorf("list %s: %+v", p, h)
		}
	}
	for _, p := range []string{"out/file.txt", "out"} {
		if h, body := ReadPath(root, p, false, nil); h.Kind != "error" || h.Error.Code != "denied" || body != nil {
			t.Errorf("read %s, a link out: %+v %q", p, h, body)
		}
	}
	s, _ := newLocal(t, root)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"abs/file.txt", "rel/file-link.txt", "roundtrip/file.txt"} {
		id := "w" + string(rune('0'+i))
		body := "saved through " + p + "\n"
		save(s, sub, id, p, []byte(body), 512, "", true)
		if f := fileReply(t, sink, id); f.Kind != "written" || f.Path != filepath.Join(root, "sub", "file.txt") {
			t.Fatalf("save %s: %+v", p, f)
		}
		if b, _ := os.ReadFile(filepath.Join(root, "sub", "file.txt")); string(b) != body {
			t.Fatalf("after the save through %s the file holds %q", p, b)
		}
		if fi, err := os.Lstat(filepath.Join(root, "sub", "file-link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the save through %s replaced the link file-link.txt", p)
		}
	}
	save(s, sub, "o", "out/file.txt", []byte("x"), 512, "", true)
	if f := fileReply(t, sink, "o"); f.Kind != "error" || f.Error.Code != "denied" {
		t.Fatalf("save through a link out: %+v", f)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "sub", "file.txt")); string(b) != "outside, longer\n" {
		t.Fatalf("the outside file holds %q", b)
	}
	h := FindPath(root, "only", nil)
	if !slices.Equal(h.Matches, []string{"sub/deep/only-inside.txt"}) {
		t.Fatalf("find: %v, want only the folder's own file (links are not walked)", h.Matches)
	}
}

// A session serves the folder it started in: when its path comes to name
// another folder (the folder moved away and a link put in its place), a
// read is refused rather than served from where the path leads now.
func TestFileGetRefusesWhenTheSessionsPathNamesAnotherFolder(t *testing.T) {
	root, outside := linkTree(t)
	s, _ := newLocal(t, root)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleView, Name: "guest"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FileGet(sub, proto.FileGet{ReqID: "r0", Path: "sub/file.txt"}); err != nil {
		t.Fatal(err)
	}
	if h := fileReply(t, sink, "r0"); h.Kind != "file" {
		t.Fatalf("before the move: %+v", h)
	}
	swapForLink(t, root, outside)
	for i, p := range []string{"sub/file.txt", "sub", "."} {
		id := "r" + string(rune('1'+i))
		if err := s.FileGet(sub, proto.FileGet{ReqID: id, Path: p}); err != nil {
			t.Fatal(err)
		}
		if h := fileReply(t, sink, id); h.Kind != "error" || h.Error.Code != "denied" {
			t.Fatalf("%s once the path names another folder: %+v", p, h)
		}
	}
	if err := s.FileGet(sub, proto.FileGet{ReqID: "f", Op: proto.FileOpFind, Path: "only"}); err != nil {
		t.Fatal(err)
	}
	if h := fileReply(t, sink, "f"); h.Kind != "error" {
		t.Fatalf("a find once the path names another folder: %+v", h)
	}
}

// A repository's .git moved into the place of the folder a save goes to,
// after the save's check, is still read only: the save is refused and the
// repository's files stay as they were. So is a bare repository moved
// into the place of a folder above the file.
func TestFileWriteStaysOutOfAGitDirMovedInAfterTheCheck(t *testing.T) {
	for _, c := range []struct {
		name, path, git string
	}{{"its own folder", "sub/config", "sub"}, {"a folder above", "sub/hooks/pre-commit", "sub"}} {
		t.Run(c.name, func(t *testing.T) {
			root, _ := linkTree(t)
			gitDir := filepath.Join(root, ".git")
			for _, d := range []string{"objects", "refs/heads", "hooks"} {
				os.MkdirAll(filepath.Join(gitDir, d), 0o755)
			}
			os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
			os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644)
			os.WriteFile(filepath.Join(gitDir, "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755)
			os.MkdirAll(filepath.Join(root, "sub", "hooks"), 0o755)
			os.WriteFile(filepath.Join(root, "sub", "config"), []byte("mine\n"), 0o644)
			os.WriteFile(filepath.Join(root, "sub", "hooks", "pre-commit"), []byte("mine\n"), 0o644)
			s, _ := newLocal(t, root)
			sink := newChanSink(false)
			sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
			if err != nil {
				t.Fatal(err)
			}
			onceAfterCheck(t, func() {
				dir := filepath.Join(root, c.git)
				os.Rename(dir, dir+".was")
				os.Rename(gitDir, dir)
				os.Symlink(c.git, gitDir)
			})
			save(s, sub, "w", c.path, []byte("[core]\n\thooksPath = /tmp/x\n"), 512, "", true)
			if f := fileReply(t, sink, "w"); f.Kind != "error" || f.Error.Code != "read_only" {
				t.Fatalf("save %s once .git was moved there: %+v %+v", c.path, f, f.Error)
			}
			for rel, body := range map[string]string{"config": "[core]\n", "hooks/pre-commit": "#!/bin/sh\n"} {
				if b, _ := os.ReadFile(filepath.Join(root, c.git, rel)); string(b) != body {
					t.Fatalf("the repository's %s holds %q", rel, b)
				}
			}
		})
	}
}

// The same for what a repository's .git holds, moved into the save's
// place after its check: its hooks folder in the place of the save's
// folder, its config in the place of the file saved.
func TestFileWriteStaysOutOfGitMetadataMovedInAfterTheCheck(t *testing.T) {
	for _, c := range []struct {
		name, path, from, to, body string
	}{
		{"hooks folder", "sub/pre-commit", ".git/hooks", "sub", "#!/bin/sh\n"},
		{"config file", "sub/config", ".git/config", "sub/config", "[core]\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, _ := linkTree(t)
			gitDir := filepath.Join(root, ".git")
			for _, d := range []string{"objects", "refs/heads", "hooks"} {
				os.MkdirAll(filepath.Join(gitDir, d), 0o755)
			}
			os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
			os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644)
			os.WriteFile(filepath.Join(gitDir, "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755)
			os.WriteFile(filepath.Join(root, "sub", "pre-commit"), []byte("mine\n"), 0o644)
			os.WriteFile(filepath.Join(root, "sub", "config"), []byte("mine\n"), 0o644)
			s, _ := newLocal(t, root)
			sink := newChanSink(false)
			sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
			if err != nil {
				t.Fatal(err)
			}
			from, to := filepath.Join(root, c.from), filepath.Join(root, c.to)
			onceAfterCheck(t, func() {
				os.Rename(to, to+".was")
				os.Rename(from, to)
			})
			save(s, sub, "w", c.path, []byte("changed\n"), 512, "", true)
			if f := fileReply(t, sink, "w"); f.Kind != "error" || f.Error.Code != "read_only" {
				t.Fatalf("save %s once %s was moved there: %+v %+v", c.path, c.from, f, f.Error)
			}
			if b, _ := os.ReadFile(filepath.Join(root, c.path)); string(b) != c.body {
				t.Fatalf("the repository's file, at %s now, holds %q", c.path, b)
			}
		})
	}
}

// A nested repository's .git whose refs and objects are absolute links
// (which a folder's handle does not follow), moved into the place of the
// save's folder after the check, is still taken for a git directory: the
// save is refused and its config stays as it was.
func TestFileWriteStaysOutOfANestedGitDirWithLinkedMarkers(t *testing.T) {
	root, _ := linkTree(t)
	nested := filepath.Join(root, "nested")
	gitDir := filepath.Join(nested, ".git")
	os.MkdirAll(gitDir, 0o755)
	for _, d := range []string{"refs-store", "objects-store"} {
		os.MkdirAll(filepath.Join(nested, d), 0o755)
	}
	os.Symlink(filepath.Join(nested, "refs-store"), filepath.Join(gitDir, "refs"))
	os.Symlink(filepath.Join(nested, "objects-store"), filepath.Join(gitDir, "objects"))
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "config"), []byte("mine\n"), 0o644)
	s, _ := newLocal(t, root)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	onceAfterCheck(t, func() {
		os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "sub.was"))
		os.Rename(gitDir, filepath.Join(root, "sub"))
	})
	save(s, sub, "w", "sub/config", []byte("[core]\n\thooksPath = /tmp/x\n"), 512, "", true)
	if f := fileReply(t, sink, "w"); f.Kind != "error" || f.Error.Code != "read_only" {
		t.Fatalf("save sub/config once the nested .git was moved there: %+v %+v", f, f.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sub", "config")); string(b) != "[core]\n" {
		t.Fatalf("the nested repository's config holds %q", b)
	}
}

// A repository whose .git/config is a link to a file not made yet
// (meta/config): the folder that file would be in, moved into the place of
// the save's folder after the check (a link left where it was, so the
// .git/config link leads there), is still read only for that name.
func TestFileWriteStaysOutOfAMetadataLinksEndMovedInAfterTheCheck(t *testing.T) {
	root, _ := linkTree(t)
	gitDir := filepath.Join(root, ".git")
	for _, d := range []string{"objects", "refs/heads"} {
		os.MkdirAll(filepath.Join(gitDir, d), 0o755)
	}
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "meta"), 0o755)
	os.Symlink(filepath.Join("..", "meta", "config"), filepath.Join(gitDir, "config"))
	s, _ := newLocal(t, root)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	onceAfterCheck(t, func() {
		os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "sub.was"))
		os.Rename(filepath.Join(root, "meta"), filepath.Join(root, "sub"))
		os.Symlink("sub", filepath.Join(root, "meta"))
	})
	save(s, sub, "w", "sub/config", []byte("[core]\n\thooksPath = /tmp/x\n"), 512, "", true)
	if f := fileReply(t, sink, "w"); f.Kind != "error" || f.Error.Code != "read_only" {
		t.Fatalf("save sub/config once meta was moved there: %+v %+v", f, f.Error)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "config")); err == nil {
		t.Fatal("the save made the file .git/config leads to")
	}
}

// A folder opened under a directory stays under it: a link out of it on
// the way, absolute or climbing out, is refused; a relative one that stays
// in it is followed.
func TestOpenFolderInStaysUnderItsDirectory(t *testing.T) {
	root, outside := linkTree(t)
	os.Symlink(filepath.Join(outside, "sub"), filepath.Join(root, "abs"))
	os.Symlink(filepath.Join("..", "outside", "sub"), filepath.Join(root, "climb"))
	os.Symlink("sub", filepath.Join(root, "rel"))
	for _, rel := range []string{"abs", "climb", filepath.Join("abs", "deep"), filepath.Join("climb", "deep")} {
		if f, err := OpenFolderIn(root, rel, nil); err == nil {
			f.Close()
			t.Errorf("%s: opened, want it refused", rel)
		}
	}
	for _, rel := range []string{".", "sub", filepath.Join("sub", "deep"), "rel"} {
		f, err := OpenFolderIn(root, rel, nil)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if f.Path() != filepath.Join(root, rel) {
			t.Errorf("%s: path %s", rel, f.Path())
		}
		f.Close()
	}
}

// A retired session's folder is closed: no file is read from it after.
func TestRetiredSessionReadsNoFile(t *testing.T) {
	root, _ := linkTree(t)
	s, _ := newLocal(t, root)
	if h, _ := s.ReadPath("sub/file.txt", false, nil, RoleControl); h.Kind != "file" {
		t.Fatalf("before: %+v", h)
	}
	s.Retire()
	if h, body := s.ReadPath("sub/file.txt", false, nil, RoleControl); h.Kind != "error" || body != nil {
		t.Fatalf("after Retire: %+v %q", h, body)
	}
}

// A folder replaced while a folder inside it is walked is not walked on:
// the find says it stopped short and names nothing of the folder put in
// its place.
func TestFindPathStopsInAFolderReplacedWhileItWasWalked(t *testing.T) {
	root, _ := linkTree(t)
	// After sub/deep, sub has a match left to look at: the find opens sub
	// again for it.
	os.WriteFile(filepath.Join(root, "sub", "only-later.txt"), []byte("x"), 0o644)
	calls := 0
	setAfterCheck(t, func() {
		// The first runs before the find goes into sub, the second before
		// it goes into sub/deep, while sub is walked.
		if calls++; calls == 2 {
			sub := filepath.Join(root, "sub")
			os.Rename(sub, sub+".was")
			os.Mkdir(sub, 0o755)
			os.WriteFile(filepath.Join(sub, "only-new.txt"), []byte("x"), 0o644)
		}
	})
	h := FindPath(root, "only", nil)
	if h.Kind != "find" || !h.Truncated {
		t.Fatalf("find: %+v", h)
	}
	for _, m := range h.Matches {
		if strings.Contains(m, "new") {
			t.Fatalf("the find names %s from the folder put in place (%v)", m, h.Matches)
		}
	}
	if !slices.Contains(h.Matches, "sub/deep/only-inside.txt") {
		t.Fatalf("the find lost what it walked before: %v", h.Matches)
	}
	if slices.Contains(h.Matches, "sub/only-later.txt") {
		t.Fatalf("the find names a file of the folder moved away: %v", h.Matches)
	}
}

// A find opens a folder again from the top only for an entry left in it
// that needs it: none on the way back up a deep chain of single folders,
// one per folder walked before an entry that is looked at. Matches keep
// their order.
func TestFindPathOpensAFolderAgainOnlyWhenAnEntryNeedsIt(t *testing.T) {
	deep := t.TempDir()
	chain := deep
	for range 300 {
		chain = filepath.Join(chain, "d")
	}
	if err := os.MkdirAll(chain, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(chain, "only-bottom.txt"), []byte("x"), 0o644)
	f, err := OpenFolder(deep)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, w := f.findWalk("only", nil)
	reopened := w.reopened
	if len(h.Matches) != 1 || !strings.HasSuffix(h.Matches[0], "d/only-bottom.txt") || h.Truncated || reopened != 0 {
		t.Fatalf("a chain 300 deep: %v truncated %v, opened again %d times", h.Matches, h.Truncated, reopened)
	}
	wide := t.TempDir()
	for _, p := range []string{"a/x-only.txt", "b/skipped.txt", "c/y-only.txt", "z-only.txt"} {
		os.MkdirAll(filepath.Join(wide, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(wide, p), []byte("x"), 0o644)
	}
	g, err := OpenFolder(wide)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	h, w = g.findWalk("only", nil)
	reopened = w.reopened
	// Opened again for b, c (folders to go into) and z-only.txt (a match).
	if !slices.Equal(h.Matches, []string{"a/x-only.txt", "c/y-only.txt", "z-only.txt"}) || reopened != 3 {
		t.Fatalf("matches %v, opened again %d times", h.Matches, reopened)
	}
}

// A folder's read counts every name it reads against its budget before it
// looks at it: it stops at the budget and says names were left, reads a
// folder within it whole, and stops when asked to.
func TestReadFolderCountsEveryName(t *testing.T) {
	root := t.TempDir()
	for i := range 10 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d", i)), []byte("x"), 0o644)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	never := func() bool { return false }
	budget := 4
	if entries, complete, err := readFolder(r, &budget, never); err != nil || len(entries) != 4 || complete || budget != 0 {
		t.Fatalf("budget 4: %d entries, complete %v, budget left %d, %v", len(entries), complete, budget, err)
	}
	budget = 10
	if entries, complete, err := readFolder(r, &budget, never); err != nil || len(entries) != 10 || !complete || budget != 0 || entries[0].fi == nil {
		t.Fatalf("budget 10: %d entries, complete %v, budget left %d, %v", len(entries), complete, budget, err)
	}
	budget = 100
	if entries, complete, _ := readFolder(r, &budget, func() bool { return true }); len(entries) != 0 || complete {
		t.Fatalf("stopped at once: %d entries, complete %v", len(entries), complete)
	}
}

// A listing reads at most maxListScan entries: a folder with more shows
// those it read, sorted, and says it is truncated; one with fewer is
// listed whole.
func TestReadPathListsAtMostItsReadBound(t *testing.T) {
	old := maxListScan
	maxListScan = 30
	t.Cleanup(func() { maxListScan = old })
	root := t.TempDir()
	for _, d := range []string{"big", "small"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	for i := range 50 {
		os.WriteFile(filepath.Join(root, "big", fmt.Sprintf("f%02d", i)), []byte("x"), 0o644)
	}
	for i := range 20 {
		os.WriteFile(filepath.Join(root, "small", fmt.Sprintf("f%02d", i)), []byte("x"), 0o644)
	}
	if h, _ := ReadPath(root, "big", false, nil); h.Kind != "dir" || !h.Truncated || len(h.Entries) != 30 || !slices.IsSortedFunc(h.Entries, func(a, b proto.FileEntry) int { return strings.Compare(a.Name, b.Name) }) {
		t.Fatalf("big: %s truncated %v, %d entries", h.Kind, h.Truncated, len(h.Entries))
	}
	if h, _ := ReadPath(root, "small", false, nil); h.Kind != "dir" || h.Truncated || len(h.Entries) != 20 || h.Entries[0].Size != 1 {
		t.Fatalf("small: %s truncated %v, %d entries", h.Kind, h.Truncated, len(h.Entries))
	}
}

// A find reads at most maxFindScan entries in all, wherever they are: a
// wide folder whose first entry is another wide folder reads the bound and
// one more look, and says it stopped short; a tree within the bound is read
// whole.
func TestFindPathReadsAtMostItsBoundInAll(t *testing.T) {
	old := maxFindScan
	maxFindScan = 30
	t.Cleanup(func() { maxFindScan = old })
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	for i := range 25 {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d-only.txt", i)), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(root, "a", fmt.Sprintf("g%02d-only.txt", i)), []byte("x"), 0o644)
	}
	f, err := OpenFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, w := f.findWalk("only", nil)
	if w.read > maxFindScan || !h.Truncated || len(h.Matches) == 0 {
		t.Fatalf("read %d entries (bound %d), truncated %v, %d matches", w.read, maxFindScan, h.Truncated, len(h.Matches))
	}
	small := t.TempDir()
	os.MkdirAll(filepath.Join(small, "a"), 0o755)
	for i := range 10 {
		os.WriteFile(filepath.Join(small, "a", fmt.Sprintf("g%02d-only.txt", i)), []byte("x"), 0o644)
	}
	g, err := OpenFolder(small)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if h, w := g.findWalk("only", nil); w.read != 11 || h.Truncated || len(h.Matches) != 10 {
		t.Fatalf("a small tree: read %d, truncated %v, %d matches", w.read, h.Truncated, len(h.Matches))
	}
}

// Lstat looks only inside the folder: a link at the last element is
// described as a link, never followed, and a path through a link out of
// the folder, or up out of it, is refused.
func TestLstatStaysInTheFolder(t *testing.T) {
	root, outside := linkTree(t)
	if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if fi, err := f.Lstat(filepath.Join("sub", "deep")); err != nil || !fi.IsDir() {
		t.Fatalf("a folder inside: %v %v", fi, err)
	}
	if fi, err := f.Lstat("out"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link: %v %v", fi, err)
	}
	for _, rel := range []string{filepath.Join("out", "only-outside.txt"), filepath.Join("..", "outside", "sub"), filepath.Join(outside, "sub")} {
		if fi, err := f.Lstat(rel); err == nil {
			t.Fatalf("%s: described as %v", rel, fi)
		}
	}
}
