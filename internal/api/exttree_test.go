package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

// A context provider's reads of a run's repository through its Host
// (docs/plans/context-seams.md, CT-1, exttree.go).

// readsRun is the run the read tests name.
const readsRun = "reads-0000aaaa"

// treeEnv is a server whose context provider is "graph", with a git
// repository at its root (README committed), mutate shaping its config.
func treeEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	e := newTestEnvAgents(t, mutate, quietLog(), nil, withContext(&fakeContext{name: "graph"}))
	e.gitRepo(t)
	return e
}

// quietLog is a logger that writes nowhere.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// putRun writes a run row of a crew working in cwd into e's ledger.
func putRun(t *testing.T, e *testEnv, runID, cwd string) {
	t.Helper()
	raw, err := json.Marshal(crew.Crew{ID: strings.TrimSuffix(runID, runID[strings.LastIndexByte(runID, '-'):]), Name: "Reads", Goal: "g", Cwd: cwd, Where: crew.WhereServer, Isolation: crew.IsolationWorktree})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.ledger.Transition(t.Context(), runID, func(tx *ledger.Tx) error {
		return tx.CreateRun(ledger.RunRow{ID: runID, CrewID: "reads", Crew: string(raw), State: ledger.RunRunning, CreatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
}

// graphHost is the Host of the context provider "graph", its try ending
// with the test.
func graphHost(t *testing.T, e *testEnv) *extHost {
	return testHost(e.srv, t.Context(), "graph")
}

// gitLog puts a git first on PATH that writes each command it runs into
// the file it answers, after the two variables of the reads' environment
// as it finds them ("NR=<no replace> LP=<literal pathspecs>"), then runs
// the real one.
func gitLog(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as git")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "git.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"NR=$GIT_NO_REPLACE_OBJECTS LP=$GIT_LITERAL_PATHSPECS $*\" >> '%s'\nexec '%s' \"$@\"\n", log, real)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// ran is what the logged git ran, one command a line.
func ran(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// A commit's tree is listed as git keeps it: files with their modes, a
// link as a link, a submodule's entry as one, each with its object and
// size; the root tree's id; the history's root; and an older commit's
// own tree, never HEAD's.
func TestReadTreeIsTheCommitsTree(t *testing.T) {
	e := treeEnv(t, nil)
	first := gitIn(t, e.root, "rev-parse", "HEAD")
	commitFile(t, e.root, "cmd/main.go", "package main\n")
	if err := os.WriteFile(filepath.Join(e.root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README", filepath.Join(e.root, "latest")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.root, "add", "-A")
	gitIn(t, e.root, "update-index", "--add", "--cacheinfo", "160000,"+first+",vendor/mod")
	gitRun(t, e.root, "commit", "-q", "-m", "more")
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	putRun(t, e, readsRun, e.root)
	h := graphHost(t, e)
	tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c, Roots: true})
	if err != nil {
		t.Fatal(err)
	}
	blob := func(p string) string { return gitIn(t, e.root, "rev-parse", c+":"+p) }
	want := []ext.TreeEntry{
		{Path: "README", Mode: "100644", Kind: ext.EntryFile, Object: blob("README"), Size: 6},
		{Path: "cmd/main.go", Mode: "100644", Kind: ext.EntryFile, Object: blob("cmd/main.go"), Size: 13},
		{Path: "latest", Mode: "120000", Kind: ext.EntryLink, Object: blob("latest"), Size: 6},
		{Path: "run.sh", Mode: "100755", Kind: ext.EntryFile, Object: blob("run.sh"), Size: 10},
		{Path: "vendor/mod", Mode: "160000", Kind: ext.EntrySubmodule, Object: first, Size: -1},
	}
	if !reflect.DeepEqual(tr.Entries, want) || tr.Commit != c || tr.ID != gitIn(t, e.root, "rev-parse", c+"^{tree}") ||
		!slices.Equal(tr.Roots, []string{first}) || tr.Shallow || tr.Truncated || tr.Denied != 0 {
		t.Fatalf("the tree:\n%+v\nwant\n%+v", tr, want)
	}
	old, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: first})
	if err != nil || len(old.Entries) != 1 || old.Entries[0].Path != "README" || old.Roots != nil {
		t.Fatalf("the first commit's tree: %+v %v", old, err)
	}
	// The submodule's entry is listed, never read; a link's target is
	// its blob, never followed.
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "vendor/mod"}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("the submodule: %v", err)
	}
}

// A commit is named by its full id alone: a ref, HEAD, an abbreviated id,
// an id in upper case or an expression is refused before git is asked;
// an id of an object that is no commit (a tree, a blob) and one the
// repository does not hold are not found, as is a run the server does
// not hold.
func TestReadTreeRefusesARevisionExpression(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	h := graphHost(t, e)
	log := gitLog(t)
	for _, rev := range []string{"HEAD", "main", "refs/heads/main", c[:12], strings.ToUpper(c), c + "^", c + ":README", c + "^{tree}"} {
		if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: rev}); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%q: %v", rev, err)
		}
	}
	if s := ran(t, log); s != "" {
		t.Fatalf("git ran for a refused request: %s", s)
	}
	for _, id := range []string{gitIn(t, e.root, "rev-parse", "HEAD^{tree}"), gitIn(t, e.root, "rev-parse", "HEAD:README"), strings.Repeat("1", 40)} {
		if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: id}); !errors.Is(err, ext.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: "other-0000bbbb", Commit: c}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("a run the server does not hold: %v", err)
	}
}

// The reads run no program the repository's configuration names: hooks,
// a file system monitor, filters, text conversion and the rest leave no
// mark.
func TestReadTreeRunsNoRepositoryProgram(t *testing.T) {
	var r *gitclitest.Repo
	e := newTestEnvAgents(t, func(cfg *config.Config) {
		r = gitclitest.New(t, gitclitest.Options{HooksPath: true})
		cfg.AllowedRoots = append(cfg.AllowedRoots, r.Dir)
	}, quietLog(), nil, withContext(&fakeContext{name: "graph"}))
	putRun(t, e, readsRun, r.Dir)
	c := gitIn(t, r.Dir, "rev-parse", "HEAD")
	h := graphHost(t, e)
	tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c, Roots: true})
	if err != nil || len(tr.Entries) != len(gitclitest.Committed) {
		t.Fatalf("the tree: %d entries, %v", len(tr.Entries), err)
	}
	for path, body := range gitclitest.Committed {
		if b, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: path}); err != nil || string(b) != body {
			t.Fatalf("%s: %q %v", path, b, err)
		}
	}
	if _, err := h.Denied(t.Context(), ext.DeniedRequest{Run: readsRun, Paths: []string{"README"}}); err != nil {
		t.Fatal(err)
	}
	r.NoneFired(t, "the reads")
}

// A replacement object stands in for nothing: a refs/replace entry for
// the commit and one for a blob, and the reads see the originals.
func TestReadTreeIgnoresReplaceObjects(t *testing.T) {
	e := treeEnv(t, nil)
	c1 := gitIn(t, e.root, "rev-parse", "HEAD")
	readme := gitIn(t, e.root, "rev-parse", "HEAD:README")
	c2 := commitFile(t, e.root, "other.txt", "other\n")
	other := gitIn(t, e.root, "rev-parse", "HEAD:other.txt")
	gitIn(t, e.root, "replace", c1, c2)
	gitIn(t, e.root, "replace", readme, other)
	if got := gitIn(t, e.root, "cat-file", "-p", "HEAD:README"); got != "other" {
		t.Fatalf("git's own read of the replaced blob: %q", got)
	}
	putRun(t, e, readsRun, e.root)
	h := graphHost(t, e)
	tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c1})
	if err != nil || len(tr.Entries) != 1 || tr.Entries[0].Path != "README" || tr.Entries[0].Object != readme {
		t.Fatalf("the replaced commit: %+v %v", tr, err)
	}
	for _, c := range []string{c1, c2} {
		if b, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "README"}); err != nil || string(b) != "hello\n" {
			t.Fatalf("the replaced blob at %s: %q %v", c, b, err)
		}
	}
}

// A shallow repository says so.
func TestReadTreeReportsShallow(t *testing.T) {
	e := treeEnv(t, nil)
	commitFile(t, e.root, "two.txt", "2\n")
	clone := filepath.Join(e.root, "clone")
	gitIn(t, e.root, "clone", "-q", "--depth", "1", "file://"+e.root, clone)
	putRun(t, e, readsRun, clone)
	c := gitIn(t, clone, "rev-parse", "HEAD")
	tr, err := graphHost(t, e).ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c, Roots: true})
	if err != nil || !tr.Shallow || !slices.Equal(tr.Roots, []string{c}) {
		t.Fatalf("the shallow clone: %+v %v", tr, err)
	}
}

// mkCommit makes a commit of a tree of the given entry names, each the
// same blob, through git's plumbing, and answers its id.
func mkCommit(t *testing.T, dir string, names func(yield func(string) bool)) string {
	t.Helper()
	b := gitIn(t, dir, "rev-parse", "HEAD:README")
	var in bytes.Buffer
	for n := range names {
		fmt.Fprintf(&in, "100644 blob %s\t%s\n", b, n)
	}
	cmd := exec.Command("git", "-C", dir, "mktree")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return gitIn(t, dir, "commit-tree", "-m", "wide", strings.TrimSpace(string(out)))
}

// A listing stops at its bounds: a tree of MaxTreeEntries + 1 entries
// keeps MaxTreeEntries; one whose listing passes MaxTreeBytes in fewer
// entries keeps what was read within it; each says it is truncated.
func TestReadTreeStopsAtItsBounds(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	h := graphHost(t, e)
	many := mkCommit(t, e.root, func(yield func(string) bool) {
		for i := range ext.MaxTreeEntries + 1 {
			if !yield(fmt.Sprintf("f%06d", i)) {
				return
			}
		}
	})
	tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: many})
	if err != nil || !tr.Truncated || len(tr.Entries) != ext.MaxTreeEntries || tr.Entries[ext.MaxTreeEntries-1].Path != fmt.Sprintf("f%06d", ext.MaxTreeEntries-1) {
		t.Fatalf("past the entries' bound: %d entries, truncated %v, %v", len(tr.Entries), tr.Truncated, err)
	}
	long := strings.Repeat("n", ext.MaxTreePath-8)
	n := ext.MaxTreeBytes/len(long) + 16
	wide := mkCommit(t, e.root, func(yield func(string) bool) {
		for i := range n {
			if !yield(fmt.Sprintf("%s%06d", long, i)) {
				return
			}
		}
	})
	tr, err = h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: wide})
	held := 0
	for _, en := range tr.Entries {
		held += len(en.Path)
	}
	if err != nil || !tr.Truncated || len(tr.Entries) == 0 || len(tr.Entries) >= n || held > ext.MaxTreeBytes {
		t.Fatalf("past the bytes' bound: %d of %d entries, %d bytes, truncated %v, %v", len(tr.Entries), n, held, tr.Truncated, err)
	}
}

// The deny list holds for a committed tree's paths, judged where they
// would be on disk: below the repository's top, never the crew's working
// directory. A run working in a subdirectory of a repository that has a
// copy of the server's data directory committed sees that copy counted
// and left out, its file refused, and Denied says so; a path outside
// every allowed root is left out and refused as well.
func TestReadTreeLeavesDeniedPathsOut(t *testing.T) {
	top := realRoot(t, t.TempDir())
	sub := filepath.Join(top, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	e := treeEnv(t, func(cfg *config.Config) {
		cfg.DataDir = filepath.Join(cfg.AllowedRoots[0], ".conductor-data")
		cfg.AllowedRoots = append(cfg.AllowedRoots, sub)
	})
	if err := os.MkdirAll(filepath.Join(e.root, ".conductor-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, e.root, "work/main.go", "package main\n")
	if err := os.WriteFile(filepath.Join(e.root, ".conductor-data", "kept.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.root, "add", "-f", ".conductor-data/kept.json")
	gitRun(t, e.root, "commit", "-q", "-m", "a copy of the data")
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	putRun(t, e, readsRun, filepath.Join(e.root, "work"))
	h := graphHost(t, e)
	tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c})
	paths := []string{}
	for _, en := range tr.Entries {
		paths = append(paths, en.Path)
	}
	// Every file committed under the data directory (the server's own, a
	// commit of the whole folder took them too) is counted and left out.
	inData := len(strings.Split(gitIn(t, e.root, "ls-tree", "-r", "--name-only", c, "--", ".conductor-data"), "\n"))
	if err != nil || inData < 1 || tr.Denied != inData || slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, ".conductor-data/") }) || !slices.Contains(paths, "work/main.go") {
		t.Fatalf("the tree: %v, %d denied, %v", paths, tr.Denied, err)
	}
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: ".conductor-data/kept.json"}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("the copy's file: %v", err)
	}
	if got, err := h.Denied(t.Context(), ext.DeniedRequest{Run: readsRun, Paths: []string{".conductor-data/kept.json", "work/main.go"}}); err != nil || !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("denied: %v %v", got, err)
	}

	// A repository whose top is above the allowed root it was launched in:
	// what lies outside the root is left out and refused.
	gitIn(t, top, "init", "-q")
	commitFile(t, top, "outside.txt", "out\n")
	inner := commitFile(t, top, "sub/inside.txt", "in\n")
	putRun(t, e, "reads-0000bbbb", sub)
	tr, err = h.ReadTree(t.Context(), ext.TreeRequest{Run: "reads-0000bbbb", Commit: inner})
	if err != nil || tr.Denied != 1 || len(tr.Entries) != 1 || tr.Entries[0].Path != "sub/inside.txt" {
		t.Fatalf("a top above the root: %+v %v", tr, err)
	}
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: "reads-0000bbbb", Commit: inner, Path: "outside.txt"}); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("outside the root: %v", err)
	}
}

// A read names a file inside the tree, literally: a denied path is
// refused, so is pathspec magic (":(top)", ":/") and anything that could
// name a path outside the tree, before git is asked; a directory and a
// path the commit has no file at are not found; and the literal-pathspec
// environment the reads run in is what keeps magic from matching.
func TestReadBlobRefusesDeniedAndOutsidePaths(t *testing.T) {
	e := treeEnv(t, nil)
	commitFile(t, e.root, "docs/a.md", "a\n")
	c := commitFile(t, e.root, "secret/key.txt", "k\n")
	if err := os.MkdirAll(filepath.Join(e.root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	putRun(t, e, readsRun, e.root)
	e.srv.fileDeny = func() []string { return []string{filepath.Join(e.root, "secret")} }
	h := graphHost(t, e)
	for _, p := range []string{"secret/key.txt", "secret", ":(top)secret/key.txt", ":/secret/key.txt", "../README", "/etc/passwd", "secret/../README"} {
		if b, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: p}); !errors.Is(err, ext.ErrRefused) || b != nil {
			t.Errorf("%q: %q %v", p, b, err)
		}
	}
	for _, p := range []string{"docs", "missing.txt"} {
		if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: p}); !errors.Is(err, ext.ErrNotFound) {
			t.Errorf("%q: %v", p, err)
		}
	}
	if b, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "README"}); err != nil || string(b) != "hello\n" {
		t.Fatalf("README: %q %v", b, err)
	}
	// The one helper refuses magic too, and git under its environment reads
	// ":(top)…" from a subdirectory as a name, matching nothing, where git
	// without it would answer the denied file's entry.
	if _, err := treeEach(t.Context(), e.root, 1<<10, func([]byte) bool { return true }, "ls-tree", c, "--", ":(top)secret/key.txt"); err == nil {
		t.Fatal("the helper ran a magic path")
	}
	listed := func(env map[string]string) int {
		n := 0
		if _, err := gitcli.RunEach(t.Context(), filepath.Join(e.root, "work"), env, gitcli.Limits{Out: 1 << 10}, func([]byte) bool { n++; return true },
			"ls-tree", "-z", "--full-tree", c, "--", ":(top)secret/key.txt"); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if magic, literal := listed(nil), listed(treeReadEnv); magic != 1 || literal != 0 {
		t.Fatalf("git with magic %d entries, with the reads' environment %d", magic, literal)
	}
}

// A file past MaxBlob is refused by its size in the listing, before any
// of it is read: no cat-file runs.
func TestReadBlobRefusesLargeBeforeReading(t *testing.T) {
	e := treeEnv(t, nil)
	c := commitFile(t, e.root, "big.bin", strings.Repeat("b", 2<<20))
	putRun(t, e, readsRun, e.root)
	log := gitLog(t)
	if _, err := graphHost(t, e).ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "big.bin"}); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("2 MiB: %v", err)
	}
	if s := ran(t, log); !strings.Contains(s, "ls-tree") || strings.Contains(s, "cat-file") {
		t.Fatalf("git ran:\n%s", s)
	}
}

// A link's read answers its target as git keeps it, never what it points
// at.
func TestReadBlobGivesALinksTarget(t *testing.T) {
	e := treeEnv(t, nil)
	if err := os.Symlink("../../outside", filepath.Join(e.root, "away")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.root, "add", "away")
	gitRun(t, e.root, "commit", "-q", "-m", "a link")
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	putRun(t, e, readsRun, e.root)
	if b, err := graphHost(t, e).ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "away"}); err != nil || string(b) != "../../outside" {
		t.Fatalf("the link: %q %v", b, err)
	}
}

// Denied asks the list as it stands at each call, and reads no object.
func TestDeniedFollowsTheListAsItChanges(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	var mu sync.Mutex
	list := []string{}
	e.srv.fileDeny = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(list)
	}
	h := graphHost(t, e)
	log := gitLog(t)
	ask := func() []bool {
		got, err := h.Denied(t.Context(), ext.DeniedRequest{Run: readsRun, Paths: []string{"README", "notes/a.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ask(); !slices.Equal(got, []bool{false, false}) {
		t.Fatalf("an empty list: %v", got)
	}
	mu.Lock()
	list = []string{filepath.Join(e.root, "notes")}
	mu.Unlock()
	if got := ask(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("notes denied: %v", got)
	}
	if s := ran(t, log); strings.Contains(s, "ls-tree") || strings.Contains(s, "cat-file") || strings.Contains(s, "rev-list") {
		t.Fatalf("Denied read objects:\n%s", s)
	}
}

// A read of a try that has ended answers context.Canceled, and one under
// way as the try ends is ended with it.
func TestReadsEndWithTheTry(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	ended, end := context.WithCancel(t.Context())
	end()
	h := testHost(e.srv, ended, "graph")
	if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c}); !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended try: %v", err)
	}
	try, give := context.WithCancel(t.Context())
	h = testHost(e.srv, try, "graph")
	held := func(string) { give() }
	extReading.Store(&held)
	defer extReading.Store(nil)
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "README"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a read under way: %v", err)
	}
}

// Only a provider in the context slot reads: a provider's Host of another
// slot and Brakeman's desk's answer ErrUnsupported (conductor host's:
// internal/hostagent's TestHostReadsNoRepository).
func TestReadsOnlyForTheContextSlot(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	hosts := map[string]ext.Host{
		"a router's": testHost(e.srv, t.Context(), "switchman"),
		"the desk's": &extHost{s: e.srv, name: "graph", ctx: t.Context(), desk: true},
	}
	for who, h := range hosts {
		if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c}); !errors.Is(err, ext.ErrUnsupported) {
			t.Errorf("%s ReadTree: %v", who, err)
		}
		if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "README"}); !errors.Is(err, ext.ErrUnsupported) {
			t.Errorf("%s ReadBlob: %v", who, err)
		}
		if _, err := h.Denied(t.Context(), ext.DeniedRequest{Run: readsRun, Paths: []string{"README"}}); !errors.Is(err, ext.ErrUnsupported) {
			t.Errorf("%s Denied: %v", who, err)
		}
	}
	if _, err := graphHost(t, e).ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c}); err != nil {
		t.Fatalf("the context provider's: %v", err)
	}
}

// The fake Host's reads refuse and answer as the server's do, request by
// request, over the same committed tree.
func TestFakeTreeRefusesAsTheServer(t *testing.T) {
	e := treeEnv(t, nil)
	commitFile(t, e.root, "secret/key.txt", "k\n")
	c := commitFile(t, e.root, "big.bin", strings.Repeat("b", ext.MaxBlob+1))
	putRun(t, e, readsRun, e.root)
	e.srv.fileDeny = func() []string { return []string{filepath.Join(e.root, "secret")} }
	fake := exttest.NewFakeHost(t)
	fake.SetTree(readsRun, c, fstest.MapFS{
		"README":         {Data: []byte("hello\n")},
		"secret/key.txt": {Data: []byte("k\n")},
		"big.bin":        {Data: []byte(strings.Repeat("b", ext.MaxBlob+1))},
	})
	fake.SetDenied(func(p string) bool { return p == "secret" || strings.HasPrefix(p, "secret/") })
	real := graphHost(t, e)
	class := func(err error) string {
		for _, k := range []error{ext.ErrRefused, ext.ErrNotFound, ext.ErrUnsupported, context.Canceled} {
			if errors.Is(err, k) {
				return k.Error()
			}
		}
		if err != nil {
			return "other: " + err.Error()
		}
		return "ok"
	}
	for _, p := range []string{"README", "secret/key.txt", "big.bin", "missing", "secret", ":(top)README", "../README"} {
		r := ext.BlobRequest{Run: readsRun, Commit: c, Path: p}
		_, ferr := fake.ReadBlob(t.Context(), r)
		_, rerr := real.ReadBlob(t.Context(), r)
		if class(ferr) != class(rerr) {
			t.Errorf("ReadBlob %q: the fake %v, the server %v", p, ferr, rerr)
		}
	}
	for _, r := range []ext.TreeRequest{{Run: readsRun, Commit: c}, {Run: readsRun, Commit: "HEAD"}, {Run: readsRun, Commit: strings.Repeat("1", 40)}, {Run: "other-0000bbbb", Commit: c}} {
		ft, ferr := fake.ReadTree(t.Context(), r)
		rt, rerr := real.ReadTree(t.Context(), r)
		if class(ferr) != class(rerr) || ft.Denied != rt.Denied || len(ft.Entries) != len(rt.Entries) {
			t.Errorf("ReadTree %+v: the fake %d entries %d denied %v, the server %d entries %d denied %v", r, len(ft.Entries), ft.Denied, ferr, len(rt.Entries), rt.Denied, rerr)
		}
	}
	dr := ext.DeniedRequest{Run: readsRun, Paths: []string{"README", "secret/key.txt"}}
	fd, ferr := fake.Denied(t.Context(), dr)
	rd, rerr := real.Denied(t.Context(), dr)
	if ferr != nil || rerr != nil || !slices.Equal(fd, rd) {
		t.Fatalf("Denied: the fake %v %v, the server %v %v", fd, ferr, rd, rerr)
	}
}

// Every git a read runs, through every read, has the reads' environment
// (no replacement object, literal pathspecs), takes no global option of
// its own, and names every path after "--".
func TestHostReadsUseTheReadEnvironment(t *testing.T) {
	e := treeEnv(t, nil)
	putRun(t, e, readsRun, e.root)
	c := gitIn(t, e.root, "rev-parse", "HEAD")
	h := graphHost(t, e)
	log := gitLog(t)
	if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: c, Roots: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: readsRun, Commit: c, Path: "README"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Denied(t.Context(), ext.DeniedRequest{Run: readsRun, Paths: []string{"README"}}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(ran(t, log)), "\n")
	subs := map[string]bool{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "NR=1 LP=1 ") || strings.Contains(l, "--no-replace-objects") || strings.Contains(l, "--literal-pathspecs") {
			t.Errorf("a git run without the reads' environment: %s", l)
		}
		for _, sub := range []string{" rev-parse ", " ls-tree ", " rev-list ", " cat-file "} {
			if strings.Contains(l, sub) {
				subs[strings.TrimSpace(sub)] = true
			}
		}
		if strings.Contains(l, " ls-tree ") && !strings.Contains(l, " -r ") && !strings.HasSuffix(l, " -- README") {
			t.Errorf("a path not after --: %s", l)
		}
	}
	if len(subs) != 4 {
		t.Fatalf("the reads ran %v:\n%s", subs, strings.Join(lines, "\n"))
	}
}

// A history of more roots than the read of their list holds (120, past
// its 4 KiB) is no failure: the tree is listed, with MaxTreeRoots roots,
// sorted, each a root of the history, and the answer is kept for the next
// read.
func TestReadTreeRootsPastTheirBound(t *testing.T) {
	e := treeEnv(t, nil)
	all := map[string]bool{gitIn(t, e.root, "rev-parse", "HEAD"): true}
	parents := []string{"HEAD"}
	tree := gitIn(t, e.root, "rev-parse", "HEAD^{tree}")
	for i := range 119 {
		c := gitIn(t, e.root, "commit-tree", tree, "-m", fmt.Sprintf("root %d", i))
		all[c] = true
		parents = append(parents, c)
	}
	args := []string{"commit-tree", tree, "-m", "many roots"}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	merge := gitIn(t, e.root, args...)
	if len(all) != 120 {
		t.Fatalf("%d roots made", len(all))
	}
	putRun(t, e, readsRun, e.root)
	h := graphHost(t, e)
	for range 2 {
		tr, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: readsRun, Commit: merge, Roots: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Roots) != ext.MaxTreeRoots || !slices.IsSorted(tr.Roots) || len(tr.Entries) != 1 {
			t.Fatalf("the roots: %v, entries %d", tr.Roots, len(tr.Entries))
		}
		for _, r := range tr.Roots {
			if !all[r] {
				t.Fatalf("%s is no root of the history", r)
			}
		}
	}
	e.srv.treeRoots.mu.Lock()
	kept := slices.ContainsFunc(e.srv.treeRoots.order, func(k string) bool { return strings.HasSuffix(k, "\x00"+merge) })
	e.srv.treeRoots.mu.Unlock()
	if !kept {
		t.Fatal("the roots are not kept")
	}
}
