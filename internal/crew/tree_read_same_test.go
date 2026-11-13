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
// exactly the files it held, each of its size; the list of paths the read
// staged is gone with it.
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
	if _, err := os.Lstat(ix.Path + ".paths"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the read's list of paths after the read: %v", err)
	}
	// plainContent writes into the store: it runs once the store was
	// compared.
	if read.Content != plainContent(t, wt) {
		t.Fatalf("the read's content %s is not plain git's", read.Content)
	}
}

// The list of paths a read stages is taken whole at its bound, and one
// byte past it fails the read with the commit alone, nothing staged: git's
// listing is stood in for at the runner's seam (diff-files writes the
// bytes, ls-files nothing), and update-index too, which counts what it is
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
				switch args[0] {
				case "diff-files":
					_, err := w.Write(bytes.Repeat([]byte("x"), c.size))
					return err
				case "ls-files":
					return nil
				case "update-index":
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
			if !c.fail && (err != nil || read.Content == "" || !slices.Equal(staged, []int{c.size})) {
				t.Fatalf("the read: %+v %v, staged %v", read, err, staged)
			}
			if _, err := os.Lstat(ix.Path + ".paths"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the read's list of paths after the read: %v", err)
			}
		})
	}
}

// A path git lists with a trailing slash (a repository inside the tree)
// is staged without it, wherever the writes of the listing are cut.
func TestAListedFolderIsStagedWithoutItsSlash(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "paths"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := &stageList{f: f}
	for _, w := range []string{"a/b\x00new/", "nested/", "\x00c/", "/d\x00"} {
		if n, err := l.Write([]byte(w)); n != len(w) || err != nil {
			t.Fatalf("write %q: %d %v", w, n, err)
		}
	}
	got, err := os.ReadFile(f.Name())
	if err != nil || l.err != nil || string(got) != "a/b\x00new/nested\x00c//d\x00" {
		t.Fatalf("the list: %q %v %v", got, err, l.err)
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

// A read names what git add -A and git write-tree make of the tree, run
// plainly in a copy of it, and the paths plain git lists against the
// commit (diff --cached --name-only --no-renames), for every kind of
// change the table holds; a name the file system refuses skips its case.
func TestATreesReadIsWhatAddMakesOfIt(t *testing.T) {
	type tc struct {
		name   string
		commit func(t *testing.T, dir string)
		change func(t *testing.T, dir string) (skip string)
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
		{"a file turned into a folder", func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "a"), "a file\n") },
			func(t *testing.T, dir string) string {
				mustDo(t, os.Remove(filepath.Join(dir, "a")))
				mustDo(t, os.Mkdir(filepath.Join(dir, "a"), 0o755))
				writeFile(t, filepath.Join(dir, "a", "x"), "inside\n")
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
			tr, err := OpenTree(root, dir)
			if err != nil {
				t.Fatal(err)
			}
			read, err := TreeRevision(t.Context(), tr, &TreeIndex{Path: filepath.Join(t.TempDir(), "index")})
			tr.Close()
			if err != nil {
				t.Fatalf("the read: %v", err)
			}
			cp := filepath.Join(t.TempDir(), "copy")
			copyTree(t, dir, cp)
			plainGit(t, cp, "add", "-A")
			want := strings.TrimSpace(plainGit(t, cp, "write-tree"))
			listed := strings.Split(strings.TrimSuffix(plainGit(t, cp, "diff", "--cached", "--name-only", "-z", "--no-renames", "HEAD"), "\x00"), "\x00")
			if len(listed) == 1 && listed[0] == "" {
				listed = nil
			}
			if read.Commit != strings.TrimSpace(plainGit(t, cp, "rev-parse", "HEAD")) || read.Content != want {
				t.Fatalf("the read: commit %s content %s; plain git: content %s", read.Commit, read.Content, want)
			}
			if read.Changed != len(listed) || len(read.Paths) > len(listed) || !slices.Equal(read.Paths, listed[:len(read.Paths)]) ||
				(len(listed) <= treeReadPaths && len(read.Paths) != len(listed)) {
				t.Fatalf("the read's paths: %d %q; plain git: %d %q", read.Changed, read.Paths, len(listed), listed)
			}
			t.Logf("the read %s, plain git %s, %d paths", read.Content, want, read.Changed)
		})
	}
}
