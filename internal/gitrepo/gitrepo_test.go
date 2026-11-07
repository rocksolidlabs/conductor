package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gitIn runs the git binary in dir for the test's setup (the package itself
// reads with go-git), with a fixed committer time when at is set.
func gitIn(t *testing.T, dir string, at time.Time, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C")
	if !at.IsZero() {
		stamp := at.Format(time.RFC3339)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func scratch(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed (the test builds its repository with it)")
	}
	dir := t.TempDir()
	gitIn(t, dir, time.Time{}, "init", "-q", "-b", "main")
	gitIn(t, dir, time.Time{}, "config", "user.name", "Ada")
	gitIn(t, dir, time.Time{}, "config", "user.email", "ada@example.invalid")
	gitIn(t, dir, time.Time{}, "config", "commit.gpgsign", "false")
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir string, at time.Time, msg string) string {
	t.Helper()
	gitIn(t, dir, at, "add", "-A")
	gitIn(t, dir, at, "commit", "-q", "-m", msg)
	return gitIn(t, dir, time.Time{}, "rev-parse", "HEAD")
}

// The log lists the commits since a time, newest first, with their subject,
// author and time; before the first commit it is empty; outside a
// repository it says so.
func TestCommitsSinceATime(t *testing.T) {
	dir := scratch(t)
	if log, err := Commits(context.Background(), dir, "", time.Time{}); err != nil || len(log.Commits) != 0 || log.Branch != "main" {
		t.Fatalf("an empty repository: %+v %v", log, err)
	}
	start := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	write(t, dir, "README.md", "# repo\n")
	commit(t, dir, start.Add(-time.Hour), "before the session")
	write(t, dir, "a.go", "package a\n")
	commit(t, dir, start.Add(time.Minute), "users: the handler\n\nThe body says why.")
	write(t, dir, "b.go", "package b\n")
	last := commit(t, dir, start.Add(2*time.Minute), "users: tests")
	log, err := Commits(context.Background(), dir, "", start)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Commits) != 2 || log.Commits[0].Sha != last || log.Commits[0].Subject != "users: tests" || log.Commits[1].Subject != "users: the handler" {
		t.Fatalf("log: %+v", log.Commits)
	}
	c := log.Commits[0]
	if c.Short != last[:7] || c.Author != "Ada" || !c.At.Equal(start.Add(2*time.Minute)) || c.Parent == "" || c.Body != "" {
		t.Fatalf("a commit: %+v", c)
	}
	if real, _ := filepath.EvalSymlinks(dir); log.Top != dir && log.Top != real {
		t.Fatalf("top %q, want %q", log.Top, dir)
	}
	if _, err := Commits(context.Background(), t.TempDir(), "", start); err != ErrNotRepo {
		t.Fatalf("outside a repository: %v", err)
	}
}

// With a base the log is what HEAD has past their merge base, whatever the
// times; a linked worktree (a crew member's) opens through its common
// directory and lists its own branch's commits.
func TestCommitsAfterABaseInALinkedWorktree(t *testing.T) {
	dir := scratch(t)
	t0 := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	write(t, dir, "README.md", "# repo\n")
	base := commit(t, dir, t0, "base")
	wt := filepath.Join(t.TempDir(), "member")
	gitIn(t, dir, time.Time{}, "worktree", "add", "-q", "-b", "crew/core", wt, "main")
	write(t, wt, "core.go", "package core\n")
	commit(t, wt, t0.Add(-48*time.Hour), "core: an old-dated commit still counts")
	write(t, dir, "main-only.txt", "x\n")
	commit(t, dir, t0.Add(time.Hour), "main moves on")
	log, err := Commits(context.Background(), wt, "main", t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if log.Branch != "crew/core" || len(log.Commits) != 1 || log.Commits[0].Subject != "core: an old-dated commit still counts" || log.Base == "" {
		t.Fatalf("worktree log: %+v", log)
	}
	if log2, _ := Commits(context.Background(), wt, base, time.Time{}); len(log2.Commits) != 1 {
		t.Fatalf("by sha: %+v", log2.Commits)
	}
}

// A commit's files against its first parent: added, modified with its
// lines, deleted, renamed with its old path, binary without lines; the
// totals; the body; a root commit lists every file as added; an unknown
// revision is an error.
func TestCommitDetail(t *testing.T) {
	dir := scratch(t)
	t0 := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	write(t, dir, "users.go", "package api\n\nfunc A() {}\n")
	write(t, dir, "old.txt", "gone\n")
	write(t, dir, "notes/long-name-that-moves.md", strings.Repeat("a line that stays the same\n", 20))
	root := commit(t, dir, t0, "init")
	if d, err := CommitDetail(context.Background(), dir, root); err != nil || len(d.Changes) != 3 || d.Commit.Parent != "" || d.Changes[0].Status != "A" {
		t.Fatalf("a root commit: %+v %v", d, err)
	}
	write(t, dir, "users.go", "package api\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n")
	write(t, dir, "new.go", "package api\n// one\n// two\n")
	os.Remove(filepath.Join(dir, "old.txt"))
	gitIn(t, dir, time.Time{}, "mv", "notes/long-name-that-moves.md", "notes/moved.md")
	write(t, dir, "pic.bin", string([]byte{0, 1, 2, 3, 0, 5}))
	sha := commit(t, dir, t0.Add(time.Minute), "users: B and C\n\nTwo more handlers.")
	d, err := CommitDetail(context.Background(), dir, sha[:9])
	if err != nil {
		t.Fatal(err)
	}
	if d.Commit.Sha != sha || d.Commit.Subject != "users: B and C" || d.Commit.Body != "Two more handlers." || d.Commit.Parent != root {
		t.Fatalf("the commit: %+v", d.Commit)
	}
	got := map[string]Change{}
	for _, c := range d.Changes {
		got[c.Path] = c
	}
	if c := got["users.go"]; c.Status != "M" || c.Added != 2 || c.Removed != 0 {
		t.Fatalf("modified: %+v", c)
	}
	if c := got["new.go"]; c.Status != "A" || c.Added != 3 {
		t.Fatalf("added: %+v", c)
	}
	if c := got["old.txt"]; c.Status != "D" || c.Removed != 1 {
		t.Fatalf("deleted: %+v", c)
	}
	if c := got["notes/moved.md"]; c.Status != "R" || c.From != "notes/long-name-that-moves.md" {
		t.Fatalf("renamed: %+v (all %+v)", c, d.Changes)
	}
	if c := got["pic.bin"]; c.Status != "A" || !c.Binary {
		t.Fatalf("binary: %+v", c)
	}
	if d.Added != 5 || d.Removed != 1 {
		t.Fatalf("totals +%d -%d", d.Added, d.Removed)
	}
	if _, err := CommitDetail(context.Background(), dir, "nope"); err == nil {
		t.Fatal("an unknown revision")
	}
}

// Compare lists the files between two commits, however many commits lie
// between them: added, modified, deleted and renamed, with their counts;
// a binary file without lines; past MaxFiles the list ends, truncated. A
// commit the repository has not is ErrNoCommit, and an id not in full is
// refused before anything is read.
func TestCompareListsTheFilesBetweenTwoCommits(t *testing.T) {
	dir := scratch(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	long := strings.Repeat("a line that stays put\n", 20)
	write(t, dir, "keep.go", "package keep\n")
	write(t, dir, "edit.go", "one\ntwo\nthree\n")
	write(t, dir, "gone.txt", "bye\n")
	write(t, dir, "old/name.go", long)
	base := commit(t, dir, at, "base")
	write(t, dir, "edit.go", "one\n2\nthree\nfour\n")
	commit(t, dir, at.Add(time.Minute), "between")
	os.Remove(filepath.Join(dir, "gone.txt"))
	os.MkdirAll(filepath.Join(dir, "new"), 0o755)
	if err := os.Rename(filepath.Join(dir, "old/name.go"), filepath.Join(dir, "new/name.go")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "added.txt", "x\ny\n")
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	head := commit(t, dir, at.Add(2*time.Minute), "candidate")
	d, err := Compare(ctx, dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Change{}
	for _, c := range d.Changes {
		by[c.Path] = c
	}
	want := map[string]Change{
		"edit.go":     {Path: "edit.go", Status: "M", Added: 2, Removed: 1},
		"gone.txt":    {Path: "gone.txt", Status: "D", Removed: 1},
		"new/name.go": {Path: "new/name.go", From: "old/name.go", Status: "R"},
		"added.txt":   {Path: "added.txt", Status: "A", Added: 2},
		"blob.bin":    {Path: "blob.bin", Status: "A", Binary: true},
	}
	if len(by) != len(want) || d.Commit.Sha != head || d.Truncated {
		t.Fatalf("compare: %+v", d)
	}
	for p, w := range want {
		if by[p] != w {
			t.Errorf("%s: %+v, want %+v", p, by[p], w)
		}
	}
	// The other way round, the same files the other way.
	back, err := Compare(ctx, dir, head, base)
	if err != nil || len(back.Changes) != len(want) || back.Commit.Sha != base {
		t.Fatalf("back: %+v %v", back, err)
	}
	// Past MaxFiles: the first MaxFiles, truncated.
	for i := range MaxFiles + 3 {
		write(t, dir, fmt.Sprintf("many/f%04d.txt", i), fmt.Sprintf("%d\n", i))
	}
	many := commit(t, dir, at.Add(3*time.Minute), "many")
	d, err = Compare(ctx, dir, head, many)
	if err != nil || len(d.Changes) != MaxFiles || !d.Truncated {
		t.Fatalf("many: %d %v %v", len(d.Changes), d.Truncated, err)
	}
	gone := strings.Repeat("ab", 20)
	if _, err := Compare(ctx, dir, base, gone); !errors.Is(err, ErrNoCommit) {
		t.Fatalf("a commit not there: %v", err)
	}
	for _, id := range []string{"HEAD", "main", base[:12], strings.ToUpper(base), base + "^"} {
		if _, err := Compare(ctx, dir, id, head); err == nil || errors.Is(err, ErrNoCommit) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if _, err := Compare(ctx, t.TempDir(), base, head); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("outside a repository: %v", err)
	}
}

// FileAt reads a file at a commit, by id: its bytes up to the bound
// (truncated past it), a binary file without them, ErrNoFile for a path
// the commit has no file at (none, a folder), ErrNoCommit for a commit
// the repository has not.
func TestFileAtReadsAFileOfACommit(t *testing.T) {
	dir := scratch(t)
	ctx := context.Background()
	write(t, dir, "a/b.txt", "hello\n")
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte{'x', 0, 'y'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	c := commit(t, dir, time.Time{}, "files")
	f, err := FileAt(ctx, dir, c, "a/b.txt", 1<<20)
	if err != nil || string(f.Content) != "hello\n" || f.Truncated || f.Binary || f.Size != 6 {
		t.Fatalf("a/b.txt: %+v %v", f, err)
	}
	if f, err = FileAt(ctx, dir, c, "a/b.txt", 3); err != nil || string(f.Content) != "hel" || !f.Truncated {
		t.Fatalf("cut: %+v %v", f, err)
	}
	if f, err = FileAt(ctx, dir, c, "bin", 1<<20); err != nil || !f.Binary || f.Content != nil {
		t.Fatalf("bin: %+v %v", f, err)
	}
	if f, err = FileAt(ctx, dir, c, "link", 1<<20); err != nil || string(f.Content) != "../../etc/passwd" {
		t.Fatalf("a link is its target's text: %+v %v", f, err)
	}
	for _, p := range []string{"a", "nope.txt", "a/b.txt/c"} {
		if _, err := FileAt(ctx, dir, c, p, 1<<20); !errors.Is(err, ErrNoFile) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := FileAt(ctx, dir, strings.Repeat("cd", 20), "a/b.txt", 1<<20); !errors.Is(err, ErrNoCommit) {
		t.Fatalf("a commit not there: %v", err)
	}
}

// FileAt's memory is bounded, not only its reply: a file far past the
// bound, committed twice with one byte changed, is read at each commit
// with its loose objects and again once packed, the older then stored as a
// delta of the newer (git gc --aggressive), and no read allocates more
// than fileAtAllocBound, whatever the file's size.
func TestFileAtMemoryIsBounded(t *testing.T) {
	dir := scratch(t)
	ctx := context.Background()
	body := strings.Repeat("a line of a file far past the bound\n", (32<<20)/36)
	write(t, dir, "big.txt", body)
	older := commit(t, dir, time.Time{}, "big")
	write(t, dir, "big.txt", "L"+body[1:])
	newer := commit(t, dir, time.Time{}, "big again")
	read := func(state, c string) {
		t.Helper()
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f, err := FileAt(ctx, dir, c, "big.txt", 1<<20)
		runtime.ReadMemStats(&after)
		if err != nil || len(f.Content) != 1<<20 || !f.Truncated || f.Binary || f.Size != int64(len(body)) {
			t.Fatalf("%s, %s: %d bytes, %+v %v", state, c, len(f.Content), f.Truncated, err)
		}
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s, %s: %d bytes allocated", state, c[:7], alloc)
		if alloc > fileAtAllocBound {
			t.Fatalf("%s, %s: %d bytes allocated for a read of a %d-byte file, past %d", state, c[:7], alloc, len(body), fileAtAllocBound)
		}
	}
	read("loose", older)
	read("loose", newer)
	gitIn(t, dir, time.Time{}, "gc", "-q", "--aggressive", "--prune=now")
	deltas := 0
	for _, line := range strings.Split(gitIn(t, dir, time.Time{}, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype) %(deltabase)"), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[1] == "blob" && strings.Trim(f[2], "0") != "" {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatal("no blob is stored as a delta after the gc")
	}
	read("packed", older)
	read("packed", newer)

	// Read by the git binary: a file under the bound is whole, a NUL in
	// the first 8,000 bytes is binary.
	mid := body[:600<<10]
	write(t, dir, "mid.txt", mid)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), append([]byte("head\x00"), body...), 0o644); err != nil {
		t.Fatal(err)
	}
	c := commit(t, dir, time.Time{}, "more")
	if f, err := FileAt(ctx, dir, c, "mid.txt", 1<<20); err != nil || string(f.Content) != mid || f.Truncated || f.Binary || f.Size != int64(len(mid)) {
		t.Fatalf("mid.txt: %d bytes, %v %v %v", len(f.Content), f.Truncated, f.Binary, err)
	}
	if f, err := FileAt(ctx, dir, c, "big.bin", 1<<20); err != nil || !f.Binary || f.Content != nil || f.Size != int64(len(body)+5) {
		t.Fatalf("big.bin: %d bytes, %v %v %v", len(f.Content), f.Binary, f.Size, err)
	}
}

// FileAt's memory is bounded for a blob packed as a delta of megabytes of
// changed lines, not only of one byte: a file whose second commit
// rewrites 5 MiB of it, each commit's lines its own, is packed with the
// one stored as a delta of the other (git gc --aggressive), a delta whose
// instructions are megabytes of literal bytes, and no read of either
// commit allocates more than fileAtAllocBound (go-git, which inflates the
// whole delta to learn the blob's size, allocated it all, #625 review
// round 3).
func TestFileAtMemoryIsBoundedForALargeDelta(t *testing.T) {
	dir := scratch(t)
	ctx := context.Background()
	const at, span = 4 << 20, 5 << 20
	rnd := rand.New(rand.NewChaCha8([32]byte{6, 2, 5}))
	changed := func() string {
		var b strings.Builder
		for b.Len() < span {
			fmt.Fprintf(&b, "%016x%016x%016x\n", rnd.Uint64(), rnd.Uint64(), rnd.Uint64())
		}
		return b.String()[:span]
	}
	base := strings.Repeat("a line of a file far past the bound\n", (16<<20)/36)
	older := base[:at] + changed() + base[at+span:]
	newer := base[:at] + changed() + base[at+span:]
	write(t, dir, "big.txt", older)
	first := commit(t, dir, time.Time{}, "big")
	write(t, dir, "big.txt", newer)
	second := commit(t, dir, time.Time{}, "big, rewritten")
	gitIn(t, dir, time.Time{}, "gc", "-q", "--aggressive", "--prune=now")
	idx, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(idx) != 1 {
		t.Fatalf("the pack: %q %v", idx, err)
	}
	// verify-pack -v: id, type, size (a delta's, its instructions
	// inflated), size in the pack, offset, depth, base.
	biggest := int64(0)
	for _, line := range strings.Split(gitIn(t, dir, time.Time{}, "verify-pack", "-v", idx[0]), "\n") {
		if f := strings.Fields(line); len(f) == 7 && f[1] == "blob" {
			n, _ := strconv.ParseInt(f[2], 10, 64)
			biggest = max(biggest, n)
		}
	}
	if biggest < 4<<20 {
		t.Fatalf("the largest blob delta is %d bytes, not megabytes", biggest)
	}
	t.Logf("a delta of %d bytes", biggest)
	for _, c := range []struct{ id, body string }{{first, older}, {second, newer}} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f, err := FileAt(ctx, dir, c.id, "big.txt", 1<<20)
		runtime.ReadMemStats(&after)
		if err != nil || string(f.Content) != c.body[:1<<20] || !f.Truncated || f.Binary || f.Size != int64(len(c.body)) {
			t.Fatalf("%s: %d bytes, %+v %v", c.id[:7], len(f.Content), f.Truncated, err)
		}
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %d bytes allocated", c.id[:7], alloc)
		if alloc > fileAtAllocBound {
			t.Fatalf("%s: %d bytes allocated for a read of a %d-byte file packed beside a %d-byte delta, past %d", c.id[:7], alloc, len(c.body), biggest, fileAtAllocBound)
		}
	}
}

// A blob, read by the git binary, is the one the commit's tree records even when refs/replace puts another in its place: the
// replacement's bytes never come back under the original's size, as
// go-git, which knows no replacements, reads a smaller file and the diff.
func TestFileAtIgnoresReplacedBlobs(t *testing.T) {
	dir := scratch(t)
	ctx := context.Background()
	orig := strings.Repeat("the original's line\n", (512<<10)/20)
	write(t, dir, "big.txt", orig)
	c := commit(t, dir, time.Time{}, "big")
	write(t, dir, "other.txt", "REPLACED "+orig[:300<<10])
	repl := gitIn(t, dir, time.Time{}, "hash-object", "-w", "other.txt")
	blob := gitIn(t, dir, time.Time{}, "rev-parse", c+":big.txt")
	gitIn(t, dir, time.Time{}, "replace", blob, repl)
	if got := gitIn(t, dir, time.Time{}, "cat-file", "-p", blob); !strings.HasPrefix(got, "REPLACED ") {
		t.Fatalf("the replacement is not in place: %.20q", got)
	}
	f, err := FileAt(ctx, dir, c, "big.txt", 1<<20)
	if err != nil || string(f.Content) != orig || f.Truncated || f.Binary || f.Size != int64(len(orig)) {
		t.Fatalf("big.txt: %d bytes starting %.20q, %+v %v", len(f.Content), f.Content, f.Truncated, err)
	}
}

// fileAtAllocBound is what one FileAt of a 1 MiB head may allocate: the
// head and the buffers of the read (about 1.3 MiB measured).
const fileAtAllocBound = 4 << 20
