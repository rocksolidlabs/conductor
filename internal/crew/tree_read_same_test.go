package crew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// storeListing is every file of repo's object store with its size.
func storeListing(t *testing.T, repo string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(repo, ".git", "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, fmt.Sprintf("%s %d", p, fi.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A read of a tree with a new file of 1 MiB and a changed file writes
// trees alone into the index's folder, and the repository's store holds
// exactly the files it held, each of its size; the lists of paths the read
// staged are gone with it.
func TestAReadWritesTreesAlone(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	if err := os.WriteFile(filepath.Join(wt, "big.bin"), bytes.Repeat([]byte("0123456789abcdef"), 1<<16), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	before := storeListing(t, repo)
	read, err := TreeRevision(t.Context(), tr, ix)
	if err != nil || read.Changed != 2 {
		t.Fatalf("the read: %+v %v", read, err)
	}
	if after := storeListing(t, repo); !slices.Equal(after, before) {
		t.Fatalf("the store after the read: %v, before %v", after, before)
	}
	kinds := folderObjects(t, wt, ix.Objects())
	if len(kinds) == 0 || slices.ContainsFunc(kinds, func(k string) bool { return k != "tree" }) {
		t.Fatalf("the index's folder holds %v", kinds)
	}
	for _, name := range []string{".gone", ".paths"} {
		if _, err := os.Lstat(ix.Path + name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the read's list %s after the read: %v", name, err)
		}
	}
	// plainContent writes into the store: it runs once the store was
	// compared.
	if read.Content != plainContent(t, wt) {
		t.Fatalf("the read's content %s is not plain git's", read.Content)
	}
}

// The lists of paths a read stages are taken whole at their bound, and
// one byte past it fails the read with the commit alone, nothing staged:
// git's listing is stood in for at the runner's seam (diff-files lists one
// changed path whose bytes and NUL are the size, ls-files nothing), and
// update-index too, which counts what each of its calls with a list is
// given.
func TestTheListOfPathsToStageIsBounded(t *testing.T) {
	for _, c := range []struct {
		name string
		size int
		fail bool
	}{{"at the bound", maxStagePaths, false}, {"one byte past it", maxStagePaths + 1, true}} {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			wt := linkedTree(t, repo, "build.1")
			tr, err := OpenTree(repo, wt)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
			var staged []int
			run := treeGit
			t.Cleanup(SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
				switch {
				case args[0] == "diff-files":
					_, err := w.Write(append(append([]byte("M\x00"), bytes.Repeat([]byte("x"), c.size-1)...), 0))
					return err
				case args[0] == "ls-files":
					return nil
				case args[0] == "update-index" && in != nil:
					b, err := io.ReadAll(in)
					staged = append(staged, len(b))
					return err
				}
				return run(ctx, workTree, objects, env, in, w, args...)
			}))
			read, err := TreeRevision(t.Context(), tr, ix)
			if c.fail && (err == nil || read.Commit == "" || read.Content != "" || staged != nil) {
				t.Fatalf("the read: %+v %v, staged %v", read, err, staged)
			}
			if !c.fail && (err != nil || read.Content == "" || !slices.Equal(staged, []int{0, c.size})) {
				t.Fatalf("the read: %+v %v, staged %v", read, err, staged)
			}
			for _, name := range []string{".gone", ".paths"} {
				if _, err := os.Lstat(ix.Path + name); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the read's list %s after the read: %v", name, err)
				}
			}
		})
	}
}

// The listed paths are sorted by their kind, wherever the writes of the
// listings are cut: a path gone (D) is only removed, one of another type
// (T) removed and staged again, a changed one (M) and every new one only
// staged; a path git lists with a trailing slash (a repository inside the
// tree) is staged without it.
func TestAListedPathIsSortedByItsKind(t *testing.T) {
	l := &stageList{}
	for i := range l.f {
		f, err := os.Create(filepath.Join(t.TempDir(), "list"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		l.f[i] = f
	}
	write := func(chunks ...string) {
		t.Helper()
		for _, w := range chunks {
			if n, err := l.Write([]byte(w)); n != len(w) || err != nil {
				t.Fatalf("write %q: %d %v", w, n, err)
			}
		}
	}
	l.status = true
	write("M\x00a/b", "\x00D", "\x00gone\x00T\x00", "t\x00M\x00c/", "/d\x00")
	l.status, l.kind, l.cur = false, nil, nil
	write("new/", "nested/", "\x00c/", "/e\x00")
	var got [2]string
	for i, f := range l.f {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		got[i] = string(b)
	}
	if want := [2]string{"gone\x00t\x00", "a/b\x00t\x00c//d\x00new/nested\x00c//e\x00"}; l.err != nil || got != want {
		t.Fatalf("the lists: %q %v, want %q", got, l.err, want)
	}
}

// A read whose folder of objects is removed between two of its commands
// (the runner's seam, before update-index) is refused by the runner
// (gitcli.ErrNoObjects) and gives the commit alone.
func TestAReadWhoseFolderOfObjectsGoesIsRefused(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	writeFile(t, filepath.Join(wt, "README"), "edited\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	recordTreeGit(t, func(c gitCall) (bool, error) {
		if c.args[0] == "update-index" {
			if err := os.RemoveAll(c.objects); err != nil {
				t.Error(err)
			}
		}
		return false, nil
	})
	read, err := TreeRevision(t.Context(), tr, ix)
	if !errors.Is(err, gitcli.ErrNoObjects) || read.Commit == "" || read.Content != "" {
		t.Fatalf("the read: %+v %v, want gitcli.ErrNoObjects", read, err)
	}
}

// plainGit runs git -C dir args as runGit does and returns its standard
// output whole; a failure fails the test.
func plainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %q: %v", args, err)
	}
	return string(out)
}

// copyTree copies the folder src to dst: each folder, each file with its
// mode bits and each link as a link.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case d.IsDir():
			return os.MkdirAll(to, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, b, 0o600); err != nil {
			return err
		}
		return os.Chmod(to, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// standaloneTree makes a repository of its own at root/r-1/tree with one
// commit holding README and the files commit writes, and returns root and
// the tree's path.
func standaloneTree(t *testing.T, commit func(dir string)) (root, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, "r-1", "tree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "README"), "one\ntwo\nthree\n")
	if commit != nil {
		commit(dir)
	}
	commitAll(t, dir, "init")
	return root, dir
}

// subRepo makes a repository with a commit at dir/name, holding one file
// of body.
func subRepo(t *testing.T, dir, name, body string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, sub, "init", "-q")
	writeFile(t, filepath.Join(sub, "inside.txt"), body)
	commitAll(t, sub, "sub")
}

// mustDo fails the test when err is not nil.
func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// replaceWith removes the tracked path name of dir, whatever it is, and
// puts a link to target in its place.
func replaceWith(t *testing.T, dir, name, target string) {
	t.Helper()
	mustDo(t, os.RemoveAll(filepath.Join(dir, name)))
	mustDo(t, os.Symlink(target, filepath.Join(dir, name)))
}

// longTree writes 70 folders of 1,000 files each into dir, every path
// 254 bytes long: listed whole with their NULs, 17,850,000 bytes, past
// maxStagePaths.
func longTree(t *testing.T, dir string) {
	t.Helper()
	for d := range 70 {
		folder := filepath.Join(dir, fmt.Sprintf("long-%02d", d))
		mustDo(t, os.Mkdir(folder, 0o755))
		for f := range 1000 {
			name := fmt.Sprintf("%05d-%s.txt", d*1000+f, strings.Repeat("n", 236))
			mustDo(t, os.WriteFile(filepath.Join(folder, name), []byte(name[:5]+"\n"), 0o644))
		}
	}
}

// settled gives every file and folder of dir outside its .git a
// modification time an hour back, so that none is changed in the same
// moment as an index git writes (git then hashes the file again, its
// times not telling it apart), and the lists a read stages hold only the
// paths that differ. It changes every file's stat data: it runs before
// the first read alone.
func settled(t *testing.T, dir string) {
	t.Helper()
	back := time.Now().Add(-time.Hour)
	mustDo(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.Name() == ".git" && d.IsDir():
			return fs.SkipDir
		case d.Name() == ".git":
			return nil
		}
		return os.Chtimes(p, back, back)
	}))
}

// A read names what git add -A and git write-tree make of the tree, run
// plainly in a copy of it, and the paths plain git lists against the
// commit (diff --cached --name-only --no-renames), for every kind of
// change the table holds; a name the file system refuses skips its case.
// The case of 70,000 files is read through a new index, then one file is
// changed and the tree read again in the same index, each read compared;
// each read's lists (update-index's inputs: the paths to remove, the
// paths to stage) are what it must be: empty at the first, the one path
// at the second. The rows of a stated difference are read twice in one
// index, each read giving the same answer: a folder that is a repository
// with no commit in place of a tracked file (add refuses the tree) is
// left out, the tree plain git makes with the file's entry removed; a
// tracked link whose target holds a carriage return, replaced by a file
// with CRLF under core.autocrlf, is the tree plain git makes with the
// link's entry removed before its add, and git add -A's id differs from
// it; a tracked folder replaced by a link the read cannot follow (to
// itself, in a loop, to a folder it cannot search) is the commit alone.
func TestATreesReadIsWhatAddMakesOfIt(t *testing.T) {
	type tc struct {
		name   string
		commit func(t *testing.T, dir string)
		change func(t *testing.T, dir string) (skip string)
	}
	const longName = "70,000 tracked files with long names, clean, then one changed"
	longChanged := filepath.Join("long-42", fmt.Sprintf("42000-%s.txt", strings.Repeat("n", 236)))
	again := map[string]func(t *testing.T, dir string){longName: func(t *testing.T, dir string) {
		writeFile(t, filepath.Join(dir, longChanged), "changed, and longer\n")
		back := time.Now().Add(-time.Hour)
		mustDo(t, os.Chtimes(filepath.Join(dir, longChanged), back, back))
	}}
	wantLists := map[string][][2]string{longName: {{"", ""}, {"", longChanged + "\x00"}}}
	trackedFolder := func(t *testing.T, dir string) {
		mustDo(t, os.Mkdir(filepath.Join(dir, "d"), 0o755))
		writeFile(t, filepath.Join(dir, "d", "x"), "inside\n")
		writeFile(t, filepath.Join(dir, "d", "y"), "inside too\n")
	}
	envFiles := func(t *testing.T, dir string) {
		writeFile(t, filepath.Join(dir, ".gitattributes"), "*.env text eol=lf\n")
		writeFile(t, filepath.Join(dir, ".env"), "A=one\n")
	}
	// The stated differences, each read twice in one index: where git's add
	// refuses the tree, the read's content is what stated makes in a copy
	// of the tree instead of add -A; where the read does not label (alone),
	// each read answers the commit alone.
	const crlfLink = "a tracked link whose target holds a carriage return replaced by a file with CRLF, under core.autocrlf"
	stated := map[string]func(t *testing.T, cp string){
		"a tracked file replaced by a folder that is a repository with no commit": func(t *testing.T, cp string) {
			plainGit(t, cp, "update-index", "--force-remove", "a")
		},
		// Difference (d): git's add judges the new file's line endings with
		// the link's old entry, whose target holds a carriage return, still
		// in the index, and keeps them as they are; the read removes that
		// entry first, and the file is converted as a new path is (p\nq\n).
		// git add -A's id differs from the read's (differs below). The same
		// paths with no carriage return in the old entry give plain git's id
		// (the next row, and a link turned into a file).
		crlfLink: func(t *testing.T, cp string) {
			plainGit(t, cp, "update-index", "--force-remove", "l")
			plainGit(t, cp, "add", "-A")
		},
	}
	differs := map[string]bool{crlfLink: true}
	crlfLinkCommit := func(target string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			runGit(t, dir, "config", "core.autocrlf", "true")
			mustDo(t, os.Symlink(target, filepath.Join(dir, "l")))
		}
	}
	crlfFile := func(t *testing.T, dir string) string {
		mustDo(t, os.Remove(filepath.Join(dir, "l")))
		writeFile(t, filepath.Join(dir, "l"), "p\r\nq\r\n")
		return ""
	}
	alone := map[string]bool{
		"a tracked folder replaced by a link to itself":                    true,
		"a tracked folder replaced by a link in a loop":                    true,
		"a tracked folder replaced by a link to a folder it cannot search": true,
	}
	cases := []tc{
		{"a clean tree", nil, func(t *testing.T, dir string) string { return "" }},
		{"a changed file", nil, func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "README"), "edited\n")
			return ""
		}},
		{"a new file", nil, func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "new.txt"), "new\n")
			return ""
		}},
		{"a deleted file", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "gone.txt"), "gone\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "gone.txt")))
				return ""
			}},
		{"an ignored file", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n") },
			func(t *testing.T, dir string) string {
				writeFile(t, filepath.Join(dir, "x.log"), "log\n")
				return ""
			}},
		{"a mode change alone", nil, func(t *testing.T, dir string) string {
			if !modesKept(t.TempDir()) {
				return "the file system keeps no executable bit"
			}
			mustDo(t, os.Chmod(filepath.Join(dir, "README"), 0o755))
			return ""
		}},
		{"a new link", nil, func(t *testing.T, dir string) string {
			mustDo(t, os.Symlink("README", filepath.Join(dir, "link")))
			return ""
		}},
		{"a link turned into a file", func(t *testing.T, dir string) { mustDo(t, os.Symlink("README", filepath.Join(dir, "link"))) },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "link")))
				writeFile(t, filepath.Join(dir, "link"), "a file now\n")
				return ""
			}},
		{"a tracked file replaced by a plain folder with files", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "a"), "a file\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "a")))
				mustDo(t, os.Mkdir(filepath.Join(dir, "a"), 0o755))
				writeFile(t, filepath.Join(dir, "a", "x"), "inside\n")
				writeFile(t, filepath.Join(dir, "a", "y"), "inside too\n")
				return ""
			}},
		{"a tracked file replaced by a folder that is a repository with a commit", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "a"), "a file\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "a")))
				subRepo(t, dir, "a", "a repository now\n")
				return ""
			}},
		{"a tracked folder replaced by a link to a folder", trackedFolder, func(t *testing.T, dir string) string {
			mustDo(t, os.Mkdir(filepath.Join(dir, "e"), 0o755))
			writeFile(t, filepath.Join(dir, "e", "z"), "elsewhere\n")
			replaceWith(t, dir, "d", "e")
			return ""
		}},
		{"a tracked folder replaced by a link to a file", trackedFolder, func(t *testing.T, dir string) string {
			replaceWith(t, dir, "d", "README")
			return ""
		}},
		{"a tracked folder replaced by a link to nothing", trackedFolder, func(t *testing.T, dir string) string {
			replaceWith(t, dir, "d", "nowhere")
			return ""
		}},
		{"a tracked file replaced by a folder that is a repository with no commit", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "a"), "a file\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "a")))
				mustDo(t, os.Mkdir(filepath.Join(dir, "a"), 0o755))
				runGit(t, filepath.Join(dir, "a"), "init", "-q")
				writeFile(t, filepath.Join(dir, "a", "inside.txt"), "no commit\n")
				return ""
			}},
		{"a tracked folder replaced by a link to itself", trackedFolder, func(t *testing.T, dir string) string {
			replaceWith(t, dir, "d", "d")
			return ""
		}},
		{"a tracked folder replaced by a link in a loop", trackedFolder, func(t *testing.T, dir string) string {
			replaceWith(t, dir, "d", "e")
			mustDo(t, os.Symlink("d", filepath.Join(dir, "e")))
			return ""
		}},
		{"a tracked folder replaced by a link to a folder it cannot search", trackedFolder, func(t *testing.T, dir string) string {
			if os.Geteuid() == 0 {
				return "root searches every folder"
			}
			mustDo(t, os.Mkdir(filepath.Join(dir, "e"), 0o755))
			replaceWith(t, dir, "d", "e")
			mustDo(t, os.Chmod(filepath.Join(dir, "e"), 0))
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "e"), 0o755) })
			return ""
		}},
		{"an attributes file changed", envFiles, func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, ".gitattributes"), "*.env -text\n")
			writeFile(t, filepath.Join(dir, ".env"), "A=two\r\n")
			return ""
		}},
		{"a new attributes file", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, ".env"), "A=one\n") },
			func(t *testing.T, dir string) string {
				writeFile(t, filepath.Join(dir, ".gitattributes"), "*.env text eol=lf\n")
				writeFile(t, filepath.Join(dir, ".env"), "A=two\r\n")
				return ""
			}},
		{crlfLink, crlfLinkCommit("t\r\nu"), crlfFile},
		{"a tracked link whose target holds a line feed alone replaced by a file with CRLF, under core.autocrlf", crlfLinkCommit("t\nu"), crlfFile},
		{"a tracked link replaced by a folder with files", func(t *testing.T, dir string) { mustDo(t, os.Symlink("README", filepath.Join(dir, "link"))) },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "link")))
				mustDo(t, os.Mkdir(filepath.Join(dir, "link"), 0o755))
				writeFile(t, filepath.Join(dir, "link", "x"), "inside\n")
				writeFile(t, filepath.Join(dir, "link", "y"), "inside too\n")
				return ""
			}},
		{"a tracked submodule replaced by a file", func(t *testing.T, dir string) { subRepo(t, dir, "sub", "first\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.RemoveAll(filepath.Join(dir, "sub")))
				writeFile(t, filepath.Join(dir, "sub"), "a file now\n")
				return ""
			}},
		{"a file moved to another path", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "old.txt"), "moved\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Mkdir(filepath.Join(dir, "to"), 0o755))
				mustDo(t, os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "to", "new.txt")))
				return ""
			}},
		{longName, longTree, func(t *testing.T, dir string) string {
			// One pack instead of 70,000 loose objects: the copies are
			// made faster.
			runGit(t, dir, "repack", "-a", "-d", "-q")
			runGit(t, dir, "prune-packed")
			settled(t, dir)
			return ""
		}},
		{"a folder turned into a file", func(t *testing.T, dir string) {
			mustDo(t, os.Mkdir(filepath.Join(dir, "d"), 0o755))
			writeFile(t, filepath.Join(dir, "d", "x"), "inside\n")
			writeFile(t, filepath.Join(dir, "d", "y"), "inside too\n")
		}, func(t *testing.T, dir string) string {
			mustDo(t, os.RemoveAll(filepath.Join(dir, "d")))
			writeFile(t, filepath.Join(dir, "d"), "a file now\n")
			return ""
		}},
		{"a file with CRLF under an eol attribute", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, ".gitattributes"), "*.txt text eol=crlf\n")
			writeFile(t, filepath.Join(dir, "c.txt"), "one\r\ntwo\r\n")
		}, func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "c.txt"), "one\r\ntwo\r\nthree\r\n")
			writeFile(t, filepath.Join(dir, "d.txt"), "new\r\nlines\r\n")
			return ""
		}},
		{"an untracked folder that is a repository with a commit", nil, func(t *testing.T, dir string) string {
			subRepo(t, dir, "nested", "nested\n")
			return ""
		}},
		{"a repository with a commit inside a new folder", nil, func(t *testing.T, dir string) string {
			mustDo(t, os.Mkdir(filepath.Join(dir, "new"), 0o755))
			writeFile(t, filepath.Join(dir, "new", "beside.txt"), "beside\n")
			subRepo(t, filepath.Join(dir, "new"), "nested", "nested\n")
			return ""
		}},
		{"a tracked submodule moved to another commit", func(t *testing.T, dir string) { subRepo(t, dir, "sub", "first\n") },
			func(t *testing.T, dir string) string {
				sub := filepath.Join(dir, "sub")
				writeFile(t, filepath.Join(sub, "inside.txt"), "second\n")
				commitAll(t, sub, "moved")
				return ""
			}},
		{"a path that holds a line break", nil, func(t *testing.T, dir string) string {
			if err := os.WriteFile(filepath.Join(dir, "line\nbreak.txt"), []byte("x\n"), 0o644); err != nil {
				return "the file system takes no line break in a name: " + err.Error()
			}
			return ""
		}},
		{"a path that starts with a dash", nil, func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "-dash.txt"), "x\n")
			writeFile(t, filepath.Join(dir, "--all"), "x\n")
			return ""
		}},
		{"a path with bytes that are not UTF-8", nil, func(t *testing.T, dir string) string {
			if err := os.WriteFile(filepath.Join(dir, "bad\xff\xfe.txt"), []byte("x\n"), 0o644); err != nil {
				return "the file system takes no such name: " + err.Error()
			}
			return ""
		}},
		{"5,000 new files", nil, func(t *testing.T, dir string) string {
			for d := range 10 {
				folder := filepath.Join(dir, fmt.Sprintf("many-%d", d))
				mustDo(t, os.Mkdir(folder, 0o755))
				for f := range 500 {
					writeFile(t, filepath.Join(folder, fmt.Sprintf("f%03d.txt", f)), fmt.Sprintf("%d %d\n", d, f))
				}
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, dir := standaloneTree(t, func(dir string) {
				if c.commit != nil {
					c.commit(t, dir)
				}
			})
			if c.name == "a tracked submodule moved to another commit" {
				// commitAll added sub as a gitlink: the tree tracks it.
				if mode := strings.Fields(runGit(t, dir, "ls-files", "-s", "sub")); len(mode) == 0 || mode[0] != "160000" {
					t.Fatalf("sub is not tracked as a submodule: %v", mode)
				}
			}
			if skip := c.change(t, dir); skip != "" {
				t.Skip(skip)
			}
			var lists [2]string
			run := treeGit
			t.Cleanup(SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
				if args[0] == "update-index" && in != nil {
					b, err := io.ReadAll(in)
					if err != nil {
						return err
					}
					i := 1
					if slices.Contains(args, "--force-remove") {
						i = 0
					}
					lists[i], in = string(b), bytes.NewReader(b)
				}
				return run(ctx, workTree, objects, env, in, w, args...)
			}))
			ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
			for n := 0; n < 2; n++ {
				if n == 1 {
					if again[c.name] == nil && stated[c.name] == nil && !alone[c.name] {
						break
					}
					if again[c.name] != nil {
						again[c.name](t, dir)
					}
				}
				lists = [2]string{}
				tr, err := OpenTree(root, dir)
				if err != nil {
					t.Fatal(err)
				}
				read, err := TreeRevision(t.Context(), tr, ix)
				tr.Close()
				if alone[c.name] {
					if head := strings.TrimSpace(plainGit(t, dir, "rev-parse", "HEAD")); err == nil || read.Commit != head || read.Content != "" || read.Changed != 0 {
						t.Fatalf("read %d: %+v %v, want the commit alone (%s)", n+1, read, err, head)
					}
					t.Logf("read %d: the commit alone: %v", n+1, err)
					continue
				}
				if err != nil {
					t.Fatalf("read %d: %v", n+1, err)
				}
				cp := filepath.Join(t.TempDir(), "copy")
				copyTree(t, dir, cp)
				if f := stated[c.name]; f != nil {
					added := plainAdd(t, dir)
					t.Logf("read %d: git add -A: %s", n+1, added)
					if differs[c.name] && (added == read.Content || strings.HasPrefix(added, "refused")) {
						t.Fatalf("read %d: git add -A made %s and the read %s; the two must differ", n+1, added, read.Content)
					}
					f(t, cp)
				} else {
					plainGit(t, cp, "add", "-A")
				}
				want := strings.TrimSpace(plainGit(t, cp, "write-tree"))
				listed := strings.Split(strings.TrimSuffix(plainGit(t, cp, "diff", "--cached", "--name-only", "-z", "--no-renames", "HEAD"), "\x00"), "\x00")
				if len(listed) == 1 && listed[0] == "" {
					listed = nil
				}
				if read.Commit != strings.TrimSpace(plainGit(t, cp, "rev-parse", "HEAD")) || read.Content != want {
					t.Fatalf("read %d: commit %s content %s; plain git: content %s", n+1, read.Commit, read.Content, want)
				}
				if read.Changed != len(listed) || len(read.Paths) > len(listed) || !slices.Equal(read.Paths, listed[:len(read.Paths)]) ||
					(len(listed) <= treeReadPaths && len(read.Paths) != len(listed)) {
					t.Fatalf("read %d's paths: %d %q; plain git: %d %q", n+1, read.Changed, read.Paths, len(listed), listed)
				}
				if w := wantLists[c.name]; w != nil && lists != w[n] {
					t.Fatalf("read %d's lists: %d and %d bytes %.300q, want %q", n+1, len(lists[0]), len(lists[1]), lists, w[n])
				}
				t.Logf("read %d: %s, plain git %s, %d paths; lists of %d and %d bytes", n+1, read.Content, want, read.Changed, len(lists[0]), len(lists[1]))
			}
		})
	}
}

// eachLongFile calls f with the path of every file longTree wrote in dir.
func eachLongFile(t *testing.T, dir string, f func(p string) error) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "long-*", "*.txt"))
	if err != nil || len(files) != 70000 {
		t.Fatalf("the long tree's files: %d %v", len(files), err)
	}
	for _, p := range files {
		mustDo(t, f(p))
	}
}

// A read whose lists of paths pass their bound, in an index it did not
// make anew, brings the index's stat data up to date once (update-index
// -q --refresh) and lists the tree again; lists still past the bound fail
// the read with the commit alone; never more than one refresh in a read,
// and nothing of it is remembered from one read to the next. On the
// 70,000 files of the table, whose paths together pass the bound (the
// bound has no seam), each read's commands counted at the runner's seam:
// the first read (a new index) refreshes once and is labelled; after
// every file's times are changed, the next read refreshes once, lists
// twice and names the same content, and the read after it lists nothing
// and refreshes nothing; the same after every file's group-write bit is
// changed (the test changes no time); once every file's content changes,
// each read refreshes once, lists twice and fails with the commit alone,
// and a read in a new index refreshes once, lists once and fails so too.
func TestAReadWhoseListsPassTheBoundRefreshesItsIndexOnce(t *testing.T) {
	root, dir := standaloneTree(t, func(dir string) { longTree(t, dir) })
	runGit(t, dir, "repack", "-a", "-d", "-q")
	runGit(t, dir, "prune-packed")
	settled(t, dir)
	calls := recordTreeGit(t, nil)
	tr, err := OpenTree(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	kept := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	type counted struct {
		read                TreeRead
		err                 error
		refreshes, listings int
		listed              int
	}
	read := func(ix *TreeIndex) counted {
		t.Helper()
		*calls = nil
		var c counted
		c.read, c.err = TreeRevision(t.Context(), tr, ix)
		for _, g := range *calls {
			switch {
			case slices.Equal(g.args, []string{"update-index", "-q", "--refresh"}):
				c.refreshes++
			case g.args[0] == "diff-files":
				c.listings++
				c.listed = len(g.out)
			}
		}
		return c
	}
	first := read(kept)
	if first.err != nil || first.read.Content == "" || first.refreshes != 1 || first.listings != 1 {
		t.Fatalf("the first read: %+v", first)
	}
	labelledAgain := func(what string) {
		t.Helper()
		if c := read(kept); c.err != nil || c.read.Content != first.read.Content || c.refreshes != 1 || c.listings != 2 || c.listed != 0 {
			t.Fatalf("the read after %s: %v %s, %d refreshes, %d listings, the last of %d bytes", what, c.err, c.read.Content, c.refreshes, c.listings, c.listed)
		}
		if c := read(kept); c.err != nil || c.read.Content != first.read.Content || c.refreshes != 0 || c.listings != 1 || c.listed != 0 {
			t.Fatalf("the read after that: %v %s, %d refreshes, %d listings, the last of %d bytes", c.err, c.read.Content, c.refreshes, c.listings, c.listed)
		}
	}
	back := time.Now().Add(-2 * time.Hour)
	eachLongFile(t, dir, func(p string) error { return os.Chtimes(p, back, back) })
	labelledAgain("every file's times changed")
	eachLongFile(t, dir, func(p string) error { return os.Chmod(p, 0o664) })
	labelledAgain("every file's group-write bit changed")
	eachLongFile(t, dir, func(p string) error {
		if err := os.WriteFile(p, []byte("changed, and longer\n"), 0o664); err != nil {
			return err
		}
		return os.Chtimes(p, back, back)
	})
	for n, ix := range []*TreeIndex{kept, kept, {Path: filepath.Join(t.TempDir(), "index")}} {
		c := read(ix)
		listings := 2
		if n == 2 {
			listings = 1
		}
		if c.err == nil || c.read.Commit != first.read.Commit || c.read.Content != "" || c.refreshes != 1 || c.listings != listings {
			t.Fatalf("read %d once every file changed: %+v %v, %d refreshes, %d listings", n+1, c.read, c.err, c.refreshes, c.listings)
		}
	}
}

// readTwice reads the tree at dir twice in one new index and gives the
// content, which must be the same at both reads.
func readTwice(t *testing.T, root, dir string) string {
	t.Helper()
	tr, err := OpenTree(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	var ids []string
	for range 2 {
		read, err := TreeRevision(t.Context(), tr, ix)
		if err != nil || read.Content == "" {
			t.Fatalf("the read: %+v %v", read, err)
		}
		ids = append(ids, read.Content)
	}
	if ids[0] != ids[1] {
		t.Fatalf("two reads: %v", ids)
	}
	return ids[0]
}

// plainAdd is the tree id plain git makes of a copy of dir with add -A
// run with args before it (a setting), or what it says when add fails.
func plainAdd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cp := filepath.Join(t.TempDir(), "copy")
	copyTree(t, dir, cp)
	cmd := exec.Command("git", append(append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", cp}, args...), "add", "-A")...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("refused (%v): %s", err, bytes.TrimSpace(out))
	}
	return strings.TrimSpace(plainGit(t, cp, "write-tree"))
}

// In a repository set to ignore case, a new path under a folder whose
// case differs from the index's (src/a tracked, SRC/b new) is staged under
// the tree's own spelling, SRC/b: the read's content is the tree plain
// git makes with the case kept (core.ignoreCase false), the same at two
// reads. git add -A, which spells the folder as the index does, is
// logged. On a file system that folds the case itself (a Mac's, as it is
// made by default) SRC is src, the new file is src/b for git and the read
// alike, and there is no difference to pin: the case is skipped there.
func TestInARepositoryThatIgnoresCaseANewPathKeepsItsFoldersSpelling(t *testing.T) {
	root, dir := standaloneTree(t, func(dir string) {
		mustDo(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
		writeFile(t, filepath.Join(dir, "src", "a"), "a\n")
	})
	if _, err := os.Lstat(filepath.Join(dir, "SRC")); err == nil {
		t.Skip("the file system folds the case: SRC is src, and the read stages src/b as git add does")
	}
	runGit(t, dir, "config", "core.ignoreCase", "true")
	mustDo(t, os.Mkdir(filepath.Join(dir, "SRC"), 0o755))
	writeFile(t, filepath.Join(dir, "SRC", "b"), "b\n")
	got := readTwice(t, root, dir)
	if want := plainAdd(t, dir, "-c", "core.ignoreCase=false"); got != want {
		t.Fatalf("the read: %s, the tree with the case kept: %s", got, want)
	}
	t.Logf("the read %s; git add -A with the case ignored: %s", got, plainAdd(t, dir))
}

// Where git's add refuses a file's conversion, the read still answers,
// the file hashed as it is converted without that check, the same id at
// two reads: a line-ending check (core.safecrlf true, a file under eol=crlf
// with a line that ends in LF alone: the content is the tree plain git
// makes with the check off) and an encoding git cannot read (UTF-16 with
// no byte order mark and an odd count of bytes: the file is hashed as its
// bytes are, as hash-object --no-filters hashes them). What git add -A
// says is logged.
func TestAConversionGitsAddRefusesIsReadWithoutTheCheck(t *testing.T) {
	t.Run("a line-ending check", func(t *testing.T) {
		root, dir := standaloneTree(t, func(dir string) { writeFile(t, filepath.Join(dir, ".gitattributes"), "*.txt text eol=crlf\n") })
		runGit(t, dir, "config", "core.safecrlf", "true")
		writeFile(t, filepath.Join(dir, "m.txt"), "a\nb\r\n")
		got := readTwice(t, root, dir)
		if want := plainAdd(t, dir, "-c", "core.safecrlf=false"); got != want {
			t.Fatalf("the read: %s, the tree with the check off: %s", got, want)
		}
		t.Logf("the read %s; git add -A: %s", got, plainAdd(t, dir))
	})
	t.Run("an encoding git cannot read", func(t *testing.T) {
		root, dir := standaloneTree(t, func(dir string) {
			writeFile(t, filepath.Join(dir, ".gitattributes"), "*.txt text working-tree-encoding=UTF-16\n")
		})
		mustDo(t, os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("A\x00B"), 0o644))
		got := readTwice(t, root, dir)
		cp := filepath.Join(t.TempDir(), "copy")
		copyTree(t, dir, cp)
		blob := strings.TrimSpace(plainGit(t, cp, "hash-object", "-w", "--no-filters", "bad.txt"))
		plainGit(t, cp, "update-index", "--add", "--cacheinfo", "100644,"+blob+",bad.txt")
		if want := strings.TrimSpace(plainGit(t, cp, "write-tree")); got != want {
			t.Fatalf("the read: %s, the tree with the file's bytes: %s", got, want)
		}
		t.Logf("the read %s; git add -A: %s", got, plainAdd(t, dir))
	})
}

// A tree whose attributes file is gone, or is of another type than the
// index has, and is not committed is answered with the commit alone
// (errTreeAttributes): the read does not guess which rules git's add
// would apply to which file. The commit holds an attributes file
// ("*.env text eol=lf") and the files it governs, .env and z.env, each
// "A=one\n"; then the attributes file is deleted (at the top with .env
// changed, at the top with z.env changed, and in a folder), or replaced
// by a link or by a folder, and the governed file is changed to
// "A=two\r\n". Each tree is read twice in one index: each read gives the
// commit alone and that error, no update-index is given a path to stage
// or remove, the second read runs no update-index at all, and the index's
// bytes after it are those after the first.
func TestATreeWhoseAttributesFileIsGoneIsAnsweredWithTheCommitAlone(t *testing.T) {
	cases := []struct {
		name, folder, file string
		change             func(t *testing.T, attrs string)
	}{
		{"deleted, .env changed", "", ".env", func(t *testing.T, attrs string) { mustDo(t, os.Remove(attrs)) }},
		{"deleted, z.env changed", "", "z.env", func(t *testing.T, attrs string) { mustDo(t, os.Remove(attrs)) }},
		{"deleted in a folder", "sub", ".env", func(t *testing.T, attrs string) { mustDo(t, os.Remove(attrs)) }},
		{"replaced by a link", "", ".env", func(t *testing.T, attrs string) {
			mustDo(t, os.Remove(attrs))
			mustDo(t, os.Symlink("README", attrs))
		}},
		{"replaced by a folder", "", ".env", func(t *testing.T, attrs string) {
			mustDo(t, os.Remove(attrs))
			mustDo(t, os.Mkdir(attrs, 0o755))
			writeFile(t, filepath.Join(attrs, "inside"), "*.env -text\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, dir := standaloneTree(t, func(dir string) {
				folder := filepath.Join(dir, c.folder)
				mustDo(t, os.MkdirAll(folder, 0o755))
				writeFile(t, filepath.Join(folder, ".gitattributes"), "*.env text eol=lf\n")
				writeFile(t, filepath.Join(folder, ".env"), "A=one\n")
				writeFile(t, filepath.Join(folder, "z.env"), "A=one\n")
			})
			c.change(t, filepath.Join(dir, c.folder, ".gitattributes"))
			writeFile(t, filepath.Join(dir, c.folder, c.file), "A=two\r\n")
			settled(t, dir)
			head := strings.TrimSpace(plainGit(t, dir, "rev-parse", "HEAD"))
			calls := recordTreeGit(t, nil)
			tr, err := OpenTree(root, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
			var index []byte
			for n := range 2 {
				*calls = nil
				read, err := TreeRevision(t.Context(), tr, ix)
				if !errors.Is(err, errTreeAttributes) || read.Commit != head || read.Content != "" {
					t.Fatalf("read %d: %+v %v, want the commit alone; git add -A makes %s", n+1, read, err, plainAdd(t, dir))
				}
				for _, g := range *calls {
					if g.args[0] == "update-index" && (n == 1 || slices.Contains(g.args, "--stdin")) {
						t.Fatalf("read %d ran %q", n+1, g.args)
					}
				}
				b, err := os.ReadFile(ix.Path)
				mustDo(t, err)
				if n == 1 && !bytes.Equal(b, index) {
					t.Fatalf("the index's bytes changed at the second read: %d bytes, %d before", len(b), len(index))
				}
				index = b
			}
			t.Logf("the commit alone at two reads: %v; git add -A makes %s", errTreeAttributes, plainAdd(t, dir))
		})
	}
}
