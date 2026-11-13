package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	godiff "github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// numbered is n lines, "line 0" on, the last one last.
func numbered(n int, last string) string {
	var b strings.Builder
	for i := range n - 1 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String() + last
}

// fixtureCommits builds, one commit a step, the files the package's tests
// compare (TestCommitDetail's, TestCompareListsTheFilesBetweenTwoCommits',
// the repack's and the ordinary repository's, a file of 10,000 lines and
// one of 10,001) and the edges of a line diff: a last line without a
// newline, an empty side, a mode changed alone and with its content, CRLF,
// a link, a file turned binary and back, a submodule, 503 files and a file
// of 30,000 lines with two changed. It answers the repository and its
// commits, oldest first.
func fixtureCommits(t *testing.T) (string, []string) {
	t.Helper()
	dir := scratch(t)
	long := strings.Repeat("a line that stays put\n", 20)
	steps := []func(){
		func() {
			write(t, dir, "users.go", "package api\n\nfunc A() {}\n")
			write(t, dir, "old.txt", "gone\n")
			write(t, dir, "notes/long-name-that-moves.md", strings.Repeat("a line that stays the same\n", 20))
			write(t, dir, "keep.go", "package keep\n")
			write(t, dir, "edit.go", "one\ntwo\nthree\n")
			write(t, dir, "gone.txt", "bye\n")
			write(t, dir, "old/name.go", long)
			write(t, dir, "a.txt", "one\n")
			write(t, dir, "at.txt", numbered(10000, "first\n"))
			write(t, dir, "past.txt", numbered(10001, "first\n"))
			write(t, dir, "nonl.txt", "no newline")
			write(t, dir, "end.txt", "the last line without one")
			write(t, dir, "drop.txt", "a\nb\n")
			write(t, dir, "empty.txt", "")
			write(t, dir, "empty2.txt", "")
			write(t, dir, "full.txt", "text\n")
			write(t, dir, "run.sh", "echo hi\n")
			write(t, dir, "tool.sh", "echo one\n")
			write(t, dir, "crlf.txt", "a\r\nb\r\n")
			write(t, dir, "lf.txt", "a\nb\n")
			write(t, dir, "uni.txt", "héllo wörld\n日本\n")
			write(t, dir, "order.txt", "first\nsecond\nthird\nfourth\n")
			write(t, dir, "space.txt", "a b\nc\n")
			write(t, dir, "nl.txt", "\n")
			if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			write(t, dir, "users.go", "package api\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n")
			write(t, dir, "new.go", "package api\n// one\n// two\n")
			os.Remove(filepath.Join(dir, "old.txt"))
			gitIn(t, dir, time.Time{}, "mv", "notes/long-name-that-moves.md", "notes/moved.md")
			write(t, dir, "pic.bin", string([]byte{0, 1, 2, 3, 0, 5}))
		},
		func() { write(t, dir, "edit.go", "one\n2\nthree\nfour\n") },
		func() {
			os.Remove(filepath.Join(dir, "gone.txt"))
			os.MkdirAll(filepath.Join(dir, "new"), 0o755)
			if err := os.Rename(filepath.Join(dir, "old/name.go"), filepath.Join(dir, "new/name.go")); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "added.txt", "x\ny\n")
			write(t, dir, "blob.bin", string([]byte{0, 1, 2, 0, 3}))
		},
		func() {
			write(t, dir, "a.txt", "one\ntwo\n")
			write(t, dir, "b.txt", "new\n")
		},
		func() {
			write(t, dir, "at.txt", numbered(10000, "second\n"))
			write(t, dir, "past.txt", numbered(10001, "second\n"))
		},
		func() {
			write(t, dir, "nonl.txt", "no newline\nstill none")
			write(t, dir, "end.txt", "the last line without one\n")
			write(t, dir, "drop.txt", "a\nb")
			write(t, dir, "empty.txt", "now text\n")
			write(t, dir, "full.txt", "")
			os.Remove(filepath.Join(dir, "empty2.txt"))
			write(t, dir, "nl.txt", "\n\n")
		},
		func() {
			os.Chmod(filepath.Join(dir, "run.sh"), 0o755)
			write(t, dir, "tool.sh", "echo two\n")
			os.Chmod(filepath.Join(dir, "tool.sh"), 0o755)
		},
		func() {
			write(t, dir, "crlf.txt", "a\r\nB\r\n")
			write(t, dir, "lf.txt", "a\r\nb\r\n")
			write(t, dir, "uni.txt", "héllo wörld\n日本語\n")
			write(t, dir, "order.txt", "second\nfirst\nfourth\nthird\n")
			write(t, dir, "space.txt", "a  b\nc \n")
		},
		func() {
			os.Remove(filepath.Join(dir, "link"))
			if err := os.Symlink("b.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(dir, "keep.go"))
			if err := os.Symlink("users.go", filepath.Join(dir, "keep.go")); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			write(t, dir, "pic.bin", "a picture no more\n")
			write(t, dir, "added.txt", "x\x00y\n")
		},
		func() {
			for i := range MaxFiles + 3 {
				write(t, dir, fmt.Sprintf("many/f%04d.txt", i), fmt.Sprintf("%d\n", i))
			}
		},
		func() { write(t, dir, "big.txt", numbered(30000, "the end\n")) },
		func() {
			body := numbered(30000, "the end\n")
			body = strings.Replace(body, "line 100\n", "line one hundred\n", 1)
			write(t, dir, "big.txt", strings.Replace(body, "line 20000\n", "line twenty thousand\n", 1))
		},
	}
	var commits []string
	for i, step := range steps {
		step()
		commits = append(commits, commit(t, dir, time.Time{}, fmt.Sprintf("step %d", i)))
	}
	// A submodule added, moved to another commit, and removed.
	for i, sha := range []string{commits[0], commits[1]} {
		gitIn(t, dir, time.Time{}, "update-index", "--add", "--cacheinfo", "160000,"+sha+",sub")
		gitIn(t, dir, time.Time{}, "commit", "-q", "-m", fmt.Sprintf("submodule %d", i))
		commits = append(commits, gitIn(t, dir, time.Time{}, "rev-parse", "HEAD"))
	}
	commits = append(commits, commit(t, dir, time.Time{}, "submodule gone"))
	return dir, commits
}

// patchCounts is a file's lines as go-git's patch counts them
// (Change.PatchContext), the comparison lineDiff is held to.
func patchCounts(ch *object.Change) (added, removed int, binary bool, err error) {
	p, err := ch.PatchContext(context.Background())
	if err != nil {
		return 0, 0, false, err
	}
	for _, fp := range p.FilePatches() {
		if fp.IsBinary() {
			binary = true
			continue
		}
		for _, chunk := range fp.Chunks() {
			n := lineCount(chunk.Content())
			switch chunk.Type() {
			case diff.Add:
				added += n
			case diff.Delete:
				removed += n
			}
		}
	}
	return added, removed, binary, nil
}

// pairs is every commit against its parent (the first against none) and
// against the first commit, both ways.
func pairs(commits []string) [][2]string {
	out := [][2]string{{"", commits[0]}}
	for i := 1; i < len(commits); i++ {
		out = append(out, [2]string{commits[i-1], commits[i]}, [2]string{commits[i], commits[i-1]}, [2]string{commits[0], commits[i]}, [2]string{commits[i], commits[0]})
	}
	return out
}

// patchDetail is the files of changes as diffTrees lists them, counted by
// go-git's patch (patchCounts): what Compare and CommitDetail answer.
func patchDetail(t *testing.T, changes object.Changes) Detail {
	t.Helper()
	var out Detail
	if len(changes) > MaxFiles {
		changes, out.Truncated = changes[:MaxFiles], true
	}
	for _, ch := range changes {
		fc := Change{Status: "M", Path: ch.To.Name}
		switch {
		case ch.From.Name == "":
			fc.Status = "A"
		case ch.To.Name == "":
			fc.Status, fc.Path = "D", ch.From.Name
		case ch.From.Name != ch.To.Name:
			fc.Status, fc.From = "R", ch.From.Name
		}
		from, to, err := ch.Files()
		if err != nil {
			t.Fatal(err)
		}
		if (from != nil && from.Size > MaxDiffed) || (to != nil && to.Size > MaxDiffed) {
			fc.Binary = true
		} else if fc.Added, fc.Removed, fc.Binary, err = patchCounts(ch); err != nil {
			t.Fatal(err)
		}
		out.Added += fc.Added
		out.Removed += fc.Removed
		out.Changes = append(out.Changes, fc)
	}
	return out
}

// Every file of every comparison of the fixtures gets the same lines and
// the same binary flag from lineDiff as from go-git's patch
// (Change.PatchContext), and Compare and CommitDetail answer the files,
// flags and lines go-git's patch and its rename defaults give. The clock
// stands still (stillClock): no file is out of time.
func TestALineDiffCountsAsGoGitsPatch(t *testing.T) {
	stillClock(t)
	dir, commits := fixtureCommits(t)
	ctx := context.Background()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	tree := func(id string) *object.Tree {
		t.Helper()
		if id == "" {
			return nil
		}
		c, err := r.CommitObject(plumbing.NewHash(id))
		if err != nil {
			t.Fatal(err)
		}
		tr, err := c.Tree()
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	files := 0
	for _, p := range pairs(commits) {
		changes, err := object.DiffTreeWithOptions(context.Background(), tree(p[0]), tree(p[1]), object.DefaultDiffTreeOptions)
		if err != nil {
			t.Fatal(err)
		}
		for _, ch := range changes {
			from, to, err := ch.Files()
			if err != nil {
				t.Fatal(err)
			}
			if (from != nil && from.Size > MaxDiffed) || (to != nil && to.Size > MaxDiffed) {
				continue
			}
			wa, wr, wb, werr := patchCounts(ch)
			left := MaxCompareDiff
			ga, gr, gb, gerr := lineDiff(from, to, &left)
			if ga != wa || gr != wr || gb != wb || (gerr == nil) != (werr == nil) {
				t.Errorf("%s..%s %s: +%d -%d binary %v (%v); go-git's patch: +%d -%d binary %v (%v)", short(p[0]), short(p[1]), ch, ga, gr, gb, gerr, wa, wr, wb, werr)
			}
			files++
		}
		// The files as the package lists them (withRenames); where its
		// renames are not cut, the same files as go-git's defaults list.
		plain, err := object.DiffTreeContext(context.Background(), tree(p[0]), tree(p[1]))
		if err != nil {
			t.Fatal(err)
		}
		listed, looked, dropped := withRenames(r.Storer, plain)
		want := patchDetail(t, listed)
		want.RenamesCut = renamesCut(plain, listed, looked, dropped)
		if all := patchDetail(t, changes); !want.RenamesCut && !slices.Equal(all.Changes, want.Changes) {
			t.Errorf("%s..%s: renames not cut, %+v; go-git's defaults list %+v", short(p[0]), short(p[1]), want.Changes, all.Changes)
		}
		var got []Detail
		if p[0] == "" {
			d, err := CommitDetail(ctx, dir, p[1])
			got = append(got, d)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			d, err := Compare(ctx, dir, p[0], p[1])
			got = append(got, d)
			if err != nil {
				t.Fatal(err)
			}
			if c, _ := r.CommitObject(plumbing.NewHash(p[1])); c.NumParents() > 0 && c.ParentHashes[0].String() == p[0] {
				d, err := CommitDetail(ctx, dir, p[1])
				got = append(got, d)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, d := range got {
			if !slices.Equal(d.Changes, want.Changes) || d.Added != want.Added || d.Removed != want.Removed || d.Truncated != want.Truncated || d.RenamesCut != want.RenamesCut {
				t.Errorf("%s..%s: +%d -%d truncated %v, renames cut %v, %+v;\nwant +%d -%d truncated %v, renames cut %v, %+v", short(p[0]), short(p[1]), d.Added, d.Removed, d.Truncated, d.RenamesCut, d.Changes, want.Added, want.Removed, want.Truncated, want.RenamesCut, want.Changes)
			}
		}
	}
	t.Logf("%d files of %d comparisons", files, len(pairs(commits)))
}

// counted is the lines of each side's text go-git's line diff is handed
// (diffLines), and the time it is given, call by call.
type counted struct {
	lines [][2]int
	limit []time.Duration
}

// handed records in a counted what go-git's line diff is handed, and hands
// it on to the diff diffLines was when handed was called.
func handed(t *testing.T) *counted {
	t.Helper()
	seen, diff := &counted{}, diffLines
	diffLines = func(a, b string, limit time.Duration) []diffmatchpatch.Diff {
		seen.lines = append(seen.lines, [2]int{lineCount(a), lineCount(b)})
		seen.limit = append(seen.limit, limit)
		return diff(a, b, limit)
	}
	t.Cleanup(func() { diffLines = godiff.DoWithTimeout })
	return seen
}

// shortLines is n lines, each its number, so a long file stays under
// MaxDiffed.
func shortLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%d\n", i)
	}
	return out
}

// A file of 100,000 lines with three lines close together changed keeps
// its lines, in Compare and in CommitDetail: the lines the two sides share
// at their start and their end are set aside, and go-git's line diff is
// handed the four lines left of each. The clock stands still
// (stillClock).
func TestALongFileKeepsTheLinesOfItsChange(t *testing.T) {
	stillClock(t)
	seen := handed(t)
	dir := scratch(t)
	ctx := context.Background()
	lines := shortLines(100000)
	write(t, dir, "big.txt", strings.Join(lines, ""))
	c1 := commit(t, dir, time.Time{}, "one")
	for _, i := range []int{50000, 50001, 50003} {
		lines[i] = fmt.Sprintf("line %d, changed\n", i)
	}
	write(t, dir, "big.txt", strings.Join(lines, ""))
	c2 := commit(t, dir, time.Time{}, "two")
	compared, err := Compare(ctx, dir, c1, c2)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := CommitDetail(ctx, dir, c2)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{Path: "big.txt", Status: "M", Added: 3, Removed: 3}}
	for name, d := range map[string]Detail{"Compare": compared, "CommitDetail": detail} {
		if !slices.Equal(d.Changes, want) {
			t.Errorf("%s: %+v, want %+v", name, d.Changes, want)
		}
	}
	if four := [][2]int{{4, 4}, {4, 4}}; !slices.Equal(seen.lines, four) {
		t.Errorf("go-git's line diff was handed %v lines a side; want %v", seen.lines, four)
	}
}

// A file whose sides, once the lines they share at their start and their
// end are set aside, have more than MaxDiffLines lines left is listed
// without its lines, and go-git's line diff is not handed it; one with
// MaxDiffLines lines left a side keeps its lines.
func TestAFilePastTheDiffsLinesIsListedWithoutThem(t *testing.T) {
	stillClock(t)
	seen := handed(t)
	dir := scratch(t)
	// The first and the last of n+2 lines changed, n lines between them
	// that are left on each side with the two changed.
	sides := func(n int) (string, string) {
		lines := shortLines(n + 2)
		before := strings.Join(lines, "")
		lines[0], lines[n+1] = "first, changed\n", "last, changed\n"
		return before, strings.Join(lines, "")
	}
	atBound, pastBound := [2]string{}, [2]string{}
	atBound[0], atBound[1] = sides(MaxDiffLines - 2)
	pastBound[0], pastBound[1] = sides(MaxDiffLines - 1)
	write(t, dir, "at.txt", atBound[0])
	write(t, dir, "past.txt", pastBound[0])
	c1 := commit(t, dir, time.Time{}, "one")
	write(t, dir, "at.txt", atBound[1])
	write(t, dir, "past.txt", pastBound[1])
	c2 := commit(t, dir, time.Time{}, "two")
	d, err := Compare(context.Background(), dir, c1, c2)
	want := []Change{{Path: "at.txt", Status: "M", Added: 2, Removed: 2}, {Path: "past.txt", Status: "M", Binary: true}}
	if err != nil || !slices.Equal(d.Changes, want) {
		t.Errorf("rests of %d and of %d lines: %+v, %v; want %+v", MaxDiffLines, MaxDiffLines+1, d.Changes, err, want)
	}
	if once := [][2]int{{MaxDiffLines, MaxDiffLines}}; !slices.Equal(seen.lines, once) {
		t.Errorf("go-git's line diff was handed %v lines a side; want %v, once", seen.lines, once)
	}
}

// movedFiles is a repository whose second commit moves n files from old/
// to new/, each made by body: the two commits.
func movedFiles(t *testing.T, n int, body func(i int) string, moved func(i int) string) (string, string, string) {
	t.Helper()
	dir := scratch(t)
	for i := range n {
		write(t, dir, fmt.Sprintf("old/f%04d.txt", i), body(i))
	}
	c1 := commit(t, dir, time.Time{}, "one")
	if err := os.Rename(filepath.Join(dir, "old"), filepath.Join(dir, "new")); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		write(t, dir, fmt.Sprintf("new/f%04d.txt", i), moved(i))
	}
	return dir, c1, commit(t, dir, time.Time{}, "two")
}

// renamed is the renames d lists, old path to new.
func renamed(d Detail) map[string]string {
	out := map[string]string{}
	for _, c := range d.Changes {
		if c.Status == "R" {
			out[c.From] = c.Path
		}
	}
	return out
}

// renamesAllocBound is what a comparison of 4,000 files of one content
// moved may allocate: renames not looked for (13 MiB measured).
const renamesAllocBound = 64 << 20

// A commit that moves 4,000 files of one content, past MaxRenamed, is
// compared with renames not looked for, so the comparison's memory stays
// small, and the answer says so.
func TestRenamesAreLookedForAmongABoundedNumberOfFiles(t *testing.T) {
	same := func(int) string { return "one content\n" }
	dir, c1, c2 := movedFiles(t, 4000, same, same)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, err := Compare(context.Background(), dir, c1, c2)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes allocated", alloc)
	if !d.RenamesCut || !d.Truncated || len(renamed(d)) > 1 || alloc > renamesAllocBound {
		t.Errorf("4,000 files moved: renames cut %v, truncated %v, %d renames listed, %d bytes allocated; want renames cut, truncated, at most one rename and at most %d bytes", d.RenamesCut, d.Truncated, len(renamed(d)), alloc, renamesAllocBound)
	}
}

// A commit that moves 50 files, half of them changed, has its renames
// found as go-git's defaults find them, every one.
func TestRenamesAmongFiftyFilesAreFoundAsBefore(t *testing.T) {
	body := func(i int) string { return numbered(20, fmt.Sprintf("the end of file %d\n", i)) }
	dir, c1, c2 := movedFiles(t, 50, body, func(i int) string {
		if i%2 == 0 {
			return strings.Replace(body(i), "line 3\n", "line three\n", 1)
		}
		return body(i)
	})
	d, err := Compare(context.Background(), dir, c1, c2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	var trees []*object.Tree
	for _, id := range []string{c1, c2} {
		c, err := r.CommitObject(plumbing.NewHash(id))
		if err != nil {
			t.Fatal(err)
		}
		tr, err := c.Tree()
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tr)
	}
	changes, err := object.DiffTreeWithOptions(context.Background(), trees[0], trees[1], object.DefaultDiffTreeOptions)
	if err != nil {
		t.Fatal(err)
	}
	if want := renamed(patchDetail(t, changes)); d.RenamesCut || len(renamed(d)) != 50 || !maps.Equal(renamed(d), want) {
		t.Errorf("50 files moved: renames cut %v, %d renames %v; want %d, %v", d.RenamesCut, len(renamed(d)), renamed(d), len(want), want)
	}
}

// searched counts the runs of go-git's search for renames (detectRenames),
// each run handed on to the search, its answer then changed by answer
// when answer is set.
func searched(t *testing.T, answer func(object.Changes) object.Changes) *int {
	t.Helper()
	runs := new(int)
	detectRenames = func(plain object.Changes, opts *object.DiffTreeOptions) (object.Changes, error) {
		*runs++
		got, err := object.DetectRenames(plain, opts)
		if answer != nil {
			got = answer(got)
		}
		return got, err
	}
	t.Cleanup(func() { detectRenames = object.DetectRenames })
	return runs
}

// statuses is how many files d lists of each status.
func statuses(d Detail) map[string]int {
	out := map[string]int{}
	for _, c := range d.Changes {
		out[c.Status]++
	}
	return out
}

// Renames are looked for only in a small comparison: 100 files moved,
// each its own content, are searched for once and found, every one; 101
// files moved and changed a little (past MaxRenamed), and 100 files of
// 256 KiB moved and changed a little (past MaxRenamedBytes), are listed
// deleted and added, the search not run, and the answer says renames were
// cut.
func TestRenamesAreLookedForOnlyInASmallComparison(t *testing.T) {
	own := func(i int) string { return fmt.Sprintf("the content of file %d\n", i) }
	large := func(i int) string {
		var b strings.Builder
		for line := 0; b.Len() < 256<<10; line++ {
			fmt.Fprintf(&b, "file %d, line %d\n", i, line)
		}
		return b.String()[:256<<10]
	}
	if 2*100*256<<10 <= MaxRenamedBytes {
		t.Fatalf("100 files of 256 KiB moved are within MaxRenamedBytes (%d)", MaxRenamedBytes)
	}
	for _, c := range []struct {
		name        string
		n           int
		body, moved func(i int) string
		runs        int
		want        map[string]int
		cut         bool
	}{
		{"100 files moved", 100, own, own, 1, map[string]int{"R": 100}, false},
		{"101 files moved and changed a little", 101, own, func(i int) string { return own(i) + "and a line more\n" }, 0, map[string]int{"A": 101, "D": 101}, true},
		{"100 files of 256 KiB moved and changed a little", 100, large, func(i int) string { return "a first line\n" + large(i) }, 0, map[string]int{"A": 100, "D": 100}, true},
	} {
		dir, c1, c2 := movedFiles(t, c.n, c.body, c.moved)
		runs := searched(t, nil)
		d, err := Compare(context.Background(), dir, c1, c2)
		if err != nil || *runs != c.runs || !maps.Equal(statuses(d), c.want) || d.RenamesCut != c.cut {
			t.Errorf("%s: searched %d times, %v, renames cut %v, %v; want searched %d times, %v, renames cut %v", c.name, *runs, statuses(d), d.RenamesCut, err, c.runs, c.want, c.cut)
		}
	}
}

// An answer of the search that does not hold every path of the comparison
// once (a path left out, a path twice) is not taken: the comparison is
// listed without renames, and the answer says renames were cut, though the
// two files are of different contents and ordinary ones.
func TestASearchsAnswerIsTakenOnlyWhenItHoldsEveryPath(t *testing.T) {
	dir := scratch(t)
	write(t, dir, "gone.txt", "a file that goes\n")
	c1 := commit(t, dir, time.Time{}, "one")
	os.Remove(filepath.Join(dir, "gone.txt"))
	write(t, dir, "other.txt", "1\n2\n3\n")
	c2 := commit(t, dir, time.Time{}, "two")
	want := []Change{{Path: "gone.txt", Status: "D", Removed: 1}, {Path: "other.txt", Status: "A", Added: 3}}
	for name, answer := range map[string]func(object.Changes) object.Changes{
		"as go-git answers it": nil,
		"a path left out":      func(ch object.Changes) object.Changes { return ch[:len(ch)-1] },
		"a path twice":         func(ch object.Changes) object.Changes { return append(ch, ch[0]) },
	} {
		runs := searched(t, answer)
		d, err := Compare(context.Background(), dir, c1, c2)
		if err != nil || *runs != 1 || !slices.Equal(d.Changes, want) || d.RenamesCut != (answer != nil) {
			t.Errorf("%s: searched %d times, %+v, renames cut %v, %v; want searched once, %+v, renames cut %v", name, *runs, d.Changes, d.RenamesCut, err, want, answer != nil)
		}
	}
}

// The line diff is go-git's helper that takes a limit
// (diff.DoWithTimeout), by default: counts hands it each file's time.
func TestTheLineDiffIsGoGitsHelperWithALimit(t *testing.T) {
	if got, want := reflect.ValueOf(diffLines).Pointer(), reflect.ValueOf(godiff.DoWithTimeout).Pointer(); got != want {
		t.Errorf("the line diff is %s; want go-git's diff.DoWithTimeout", runtime.FuncForPC(got).Name())
	}
}

// fakeClock sets the clock lineDiff reads: the diff numbered n (from 0)
// takes took(n), and ran(n), when set, runs as it starts. It answers how
// many diffs have started.
func fakeClock(t *testing.T, took func(n int) time.Duration, ran func(n int)) *int {
	t.Helper()
	calls, now := 0, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	started := 0
	clock = func() time.Time {
		n := calls / 2
		if calls++; calls%2 == 1 {
			started++
			if ran != nil {
				ran(n)
			}
		} else {
			now = now.Add(took(n))
		}
		return now
	}
	t.Cleanup(func() { clock = time.Now })
	return &started
}

// changedFiles is a repository whose second commit changes the line of
// each of n files, f0.txt on: the two commits.
func changedFiles(t *testing.T, n int) (string, string, string) {
	t.Helper()
	dir := scratch(t)
	for i := range n {
		write(t, dir, fmt.Sprintf("f%d.txt", i), "one\n")
	}
	c1 := commit(t, dir, time.Time{}, "one")
	for i := range n {
		write(t, dir, fmt.Sprintf("f%d.txt", i), "two\n")
	}
	return dir, c1, commit(t, dir, time.Time{}, "two")
}

// linesOf is each file's lines, or -1 for one listed without them.
func linesOf(d Detail) []int {
	var out []int
	for _, c := range d.Changes {
		if c.Binary {
			out = append(out, -1)
		} else {
			out = append(out, c.Added+c.Removed)
		}
	}
	return out
}

// A file whose diff took its time (MaxFileDiff, on the clock lineDiff
// reads) is listed without its lines, and the files after it still get
// theirs.
func TestAFileWhoseDiffTookItsTimeIsListedWithoutItsLines(t *testing.T) {
	dir, c1, c2 := changedFiles(t, 3)
	fakeClock(t, func(n int) time.Duration {
		if n == 1 {
			return MaxFileDiff
		}
		return 0
	}, nil)
	d, err := Compare(context.Background(), dir, c1, c2)
	if got, want := linesOf(d), []int{2, -1, 2}; err != nil || !slices.Equal(got, want) || d.Added != 2 || d.Removed != 2 {
		t.Errorf("the second file's diff took %v: lines %v (+%d -%d), %v; want %v", MaxFileDiff, got, d.Added, d.Removed, err, want)
	}
}

// Once the comparison's time (MaxCompareDiff) is spent, every file after
// it is listed without its lines, its diff not started. go-git's line diff
// is handed each file's time, the less of MaxFileDiff and what is left of
// the comparison's (diffLines): a diff handed any other limit, or none,
// fails the test.
func TestOnceTheComparisonsTimeIsSpentNoFileGetsItsLines(t *testing.T) {
	dir, c1, c2 := changedFiles(t, 8)
	each := MaxFileDiff - time.Millisecond
	started := fakeClock(t, func(int) time.Duration { return each }, nil)
	seen := handed(t)
	d, err := Compare(context.Background(), dir, c1, c2)
	// Five diffs end within their time; the sixth is given what is left
	// of the comparison's, 5 ms, and takes longer.
	if got, want := linesOf(d), []int{2, 2, 2, 2, 2, -1, -1, -1}; err != nil || !slices.Equal(got, want) || *started != 6 {
		t.Errorf("each diff took %v: lines %v, %d diffs started, %v; want %v and 6", each, got, *started, err, want)
	}
	want := []time.Duration{MaxFileDiff, MaxFileDiff, MaxFileDiff, MaxFileDiff, MaxFileDiff, MaxCompareDiff - 5*each}
	if !slices.Equal(seen.limit, want) {
		t.Errorf("go-git's line diff was given %v; want %v", seen.limit, want)
	}
}

// A comparison asks the read's context between two files: a context
// ended during a file's diff ends the read before the next file, with the
// context's error, in Compare and in CommitDetail; one ended during the
// last file's diff, after the read's last file was opened, is the answer
// too.
func TestAComparisonEndsWithItsContextBetweenTwoFiles(t *testing.T) {
	dir, c1, c2 := changedFiles(t, 3)
	for _, at := range []int{0, 2} {
		for name, read := range map[string]func(ctx context.Context) error{
			"Compare": func(ctx context.Context) error {
				_, err := Compare(ctx, dir, c1, c2)
				return err
			},
			"CommitDetail": func(ctx context.Context) error {
				_, err := CommitDetail(ctx, dir, c2)
				return err
			},
		} {
			ctx, cancel := context.WithCancel(context.Background())
			started := fakeClock(t, func(int) time.Duration { return 0 }, func(n int) {
				if n == at {
					cancel()
				}
			})
			if err := read(ctx); !errors.Is(err, context.Canceled) || *started != at+1 {
				t.Errorf("%s, its context ended in diff %d: %v, %d diffs started; want the context's error and %d", name, at, err, *started, at+1)
			}
			cancel()
		}
	}
}

func short(id string) string { return id[:min(len(id), 7)] }

// stillClock stands the clock lineDiff reads still, so no file's diff
// takes any of its time, and gives go-git's line diff the hour go-git's
// patch gives it (diff.Do), so a loaded machine's clock, which the line
// diff reads itself, ends none: the counts are go-git's patch's.
func stillClock(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	diffLines = func(a, b string, _ time.Duration) []diffmatchpatch.Diff { return godiff.DoWithTimeout(a, b, time.Hour) }
	t.Cleanup(func() { clock, diffLines = time.Now, godiff.DoWithTimeout })
}

// wholeCounts is the lines added and removed between a and b as go-git's
// line diff counts them on the whole texts (diff.Do, as its patch does):
// binary when it has nothing to compare.
func wholeCounts(a, b string) (added, removed int, binary bool) {
	diffs := godiff.Do(a, b)
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			added += lineCount(d.Text)
		case diffmatchpatch.DiffDelete:
			removed += lineCount(d.Text)
		}
	}
	return added, removed, len(diffs) == 0
}

// Pairs of small texts from a fixed seed, lines of a few kinds repeated
// (CRLF among them), each with a shared start and a shared end of every
// length up to five lines, either side's last line with or without its
// newline, an empty side, one side the start or the end of the other, and
// equal texts; and texts made of pieces of lines, so that the bytes the
// two sides share at their start and at their end begin or end inside a
// line, and lines that are the start or the end of one another ("a",
// "ab", "b", "ba"): the counts, with the shared lines set aside, are the
// whole texts' as go-git's line diff counts them.
func TestTheLinesSharedAtTheEndsAreSetAsideWithTheSameCounts(t *testing.T) {
	stillClock(t)
	rnd := rand.New(rand.NewPCG(874, 2))
	kinds := []string{"a\n", "b\n", "c\n", "\n", "a\r\n", "b\r\n", "a b\n", "é\n"}
	lines := func(most int) string {
		var s strings.Builder
		for range rnd.IntN(most + 1) {
			s.WriteString(kinds[rnd.IntN(len(kinds))])
		}
		return s.String()
	}
	noNewline := func(s string) string {
		if rnd.IntN(3) == 0 {
			return strings.TrimSuffix(s, "\n")
		}
		return s
	}
	var cases [][2]string
	for start := range 6 {
		for end := range 6 {
			for range 12 {
				shared, tail := "", ""
				for range start {
					shared += kinds[rnd.IntN(len(kinds))]
				}
				for range end {
					tail += kinds[rnd.IntN(len(kinds))]
				}
				cases = append(cases, [2]string{noNewline(shared + lines(5) + tail), noNewline(shared + lines(5) + tail)})
			}
		}
	}
	for range 60 {
		a, more := noNewline(lines(6)), lines(3)
		cases = append(cases, [2]string{a, a}, [2]string{a, ""}, [2]string{"", a}, [2]string{a, a + more}, [2]string{a + more, a}, [2]string{more + a, a}, [2]string{a, more + a})
	}
	parts := []string{"a", "b", "ab", "ba", " ", "\n", "\r\n", "a\n", "ab\n", "b\n", "ba\n"}
	pieces := func(most int) string {
		var s strings.Builder
		for range rnd.IntN(most + 1) {
			s.WriteString(parts[rnd.IntN(len(parts))])
		}
		return s.String()
	}
	for range 600 {
		start, end := pieces(4), pieces(4)
		a, b := start+pieces(4)+end, start+pieces(4)+end
		cases = append(cases, [2]string{a, b}, [2]string{a, a + pieces(2)}, [2]string{pieces(2) + a, a})
	}
	for _, c := range cases {
		left := MaxCompareDiff
		ga, gr, gb := counts(c[0], c[1], &left)
		if wa, wr, wb := wholeCounts(c[0], c[1]); ga != wa || gr != wr || gb != wb {
			t.Errorf("%q to %q: +%d -%d binary %v; the whole texts: +%d -%d binary %v", c[0], c[1], ga, gr, gb, wa, wr, wb)
		}
	}
	t.Logf("%d pairs", len(cases))
}
