package gitrepo

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// deltaOfLarge is a repository whose file f.txt is 32 MiB in its first
// commit, its first MaxDiffed bytes in the second and those with a line
// changed in the third, packed (git gc --aggressive) with each of the
// two small versions stored as a delta of the 32 MiB one: the second and
// the third commits.
func deltaOfLarge(t *testing.T) (dir, small, changed string) {
	t.Helper()
	dir = scratch(t)
	var b strings.Builder
	for i := 0; b.Len() < 32<<20; i++ {
		fmt.Fprintf(&b, "a line of a file far past the bound, number %08d, and more words to make it long\n", i)
	}
	large := b.String()[:32<<20]
	write(t, dir, "f.txt", large)
	commit(t, dir, time.Time{}, "large")
	write(t, dir, "f.txt", large[:MaxDiffed])
	small = commit(t, dir, time.Time{}, "small")
	write(t, dir, "f.txt", strings.Replace(large[:MaxDiffed], "number 00000100,", "number 0000one0,", 1))
	changed = commit(t, dir, time.Time{}, "changed")
	gitIn(t, dir, time.Time{}, "gc", "-q", "--aggressive", "--prune=now")
	base := gitIn(t, dir, time.Time{}, "rev-parse", small+"~1:f.txt")
	var deltas []string
	for _, line := range strings.Split(gitIn(t, dir, time.Time{}, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objectsize) %(deltabase)"), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[1] == strconv.Itoa(MaxDiffed) && f[2] == base {
			deltas = append(deltas, f[0])
		}
	}
	for _, rev := range []string{small, changed} {
		if id := gitIn(t, dir, time.Time{}, "rev-parse", rev+":f.txt"); !slices.Contains(deltas, id) {
			t.Fatalf("f.txt at %s (%s) is not stored as a delta of the 32 MiB blob %s; the deltas of it: %v", rev[:7], id, base, deltas)
		}
	}
	return dir, small, changed
}

// comparisonAllocBound is what one comparison of a file of MaxDiffed
// bytes a side may allocate: its two sides, their texts and the diff of
// what is left of them (4.5 MiB measured; 175 MiB when go-git read the
// sides).
const comparisonAllocBound = 16 << 20

// run is one run of git through the package's seam (runGit): its
// arguments and the ids on its input, and the error it returned, once it
// has.
type run struct {
	args     []string
	ids      []string
	returned bool
	err      error
}

// watchGit records each run of git through runGit, handed on to the
// runner runGit was when watchGit was called; hold, when set, is handed
// the run's context, its arguments and its input, and answers the input
// git is given.
func watchGit(t *testing.T, hold func(ctx context.Context, args []string, in io.Reader) io.Reader) *[]*run {
	t.Helper()
	runs, was := &[]*run{}, runGit
	runGit = func(ctx context.Context, workTree string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		b, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		r := &run{args: args, ids: strings.Fields(string(b))}
		*runs = append(*runs, r)
		in = bytes.NewReader(b)
		if hold != nil {
			in = hold(ctx, args, in)
		}
		r.err = was(ctx, workTree, env, in, w, args...)
		r.returned = true
		return r.err
	}
	t.Cleanup(func() { runGit = was })
	return runs
}

// argsOf is each run's arguments, joined.
func argsOf(runs []*run) []string {
	var out []string
	for _, r := range runs {
		out = append(out, strings.Join(r.args, " "))
	}
	return out
}

// A comparison of 100 changed files runs git twice, through gitcli, which
// runs its look at the configuration before each (four processes): once
// for the sizes of the 200 sides and once for their contents, each with
// the 200 ids on its input.
func TestAComparisonOfAHundredFilesRunsGitTwice(t *testing.T) {
	dir, c1, c2 := changedFiles(t, 100)
	ctx := context.Background()
	for name, read := range map[string]func() (Detail, error){
		"Compare":      func() (Detail, error) { return Compare(ctx, dir, c1, c2) },
		"CommitDetail": func() (Detail, error) { return CommitDetail(ctx, dir, c2) },
	} {
		t.Run(name, func(t *testing.T) {
			runs := watchGit(t, nil)
			d, err := read()
			if err != nil || len(d.Changes) != 100 || d.Added != 100 || d.Removed != 100 {
				t.Fatalf("%d files +%d -%d, %v", len(d.Changes), d.Added, d.Removed, err)
			}
			want := []string{"cat-file --batch-check", "cat-file --batch"}
			if got := argsOf(*runs); !slices.Equal(got, want) || len((*runs)[0].ids) != 200 || !slices.Equal((*runs)[0].ids, (*runs)[1].ids) {
				t.Errorf("git ran %d times, %q; want %q, each with the 200 ids", len(got), got, want)
			}
		})
	}
}

// A file past MaxDiffed is listed without its lines and its content is not
// read: git is asked its sides' sizes, never their contents; a file
// beside it within the bound keeps its lines, its sides read.
func TestASidePastTheBoundIsListedWithoutItsContentRead(t *testing.T) {
	dir := scratch(t)
	big := strings.Repeat("x\n", MaxDiffed/2) + "y\n"
	write(t, dir, "big.txt", big)
	write(t, dir, "small.txt", "one\n")
	c1 := commit(t, dir, time.Time{}, "one")
	write(t, dir, "big.txt", "z\n"+big)
	write(t, dir, "small.txt", "two\n")
	c2 := commit(t, dir, time.Time{}, "two")
	runs := watchGit(t, nil)
	d, err := Compare(context.Background(), dir, c1, c2)
	want := []Change{{Path: "big.txt", Status: "M", Binary: true}, {Path: "small.txt", Status: "M", Added: 1, Removed: 1}}
	if err != nil || !slices.Equal(d.Changes, want) {
		t.Fatalf("%+v %v, want %+v", d.Changes, err, want)
	}
	id := func(rev string) string { return gitIn(t, dir, time.Time{}, "rev-parse", rev) }
	bigs, smalls := []string{id(c1 + ":big.txt"), id(c2 + ":big.txt")}, []string{id(c1 + ":small.txt"), id(c2 + ":small.txt")}
	if len(*runs) != 2 || !slices.Equal((*runs)[0].ids, append(slices.Clone(bigs), smalls...)) || !slices.Equal((*runs)[1].ids, smalls) {
		t.Errorf("git ran %q with the ids %v; want the sizes of %v and %v, then the contents of %v alone", argsOf(*runs), *runs, bigs, smalls, smalls)
	}
}

// A file whose blob the repository has not (a tree written with it
// missing) is listed without its lines, git unable to say its size; the
// file beside it keeps its lines.
func TestAFileWhoseSizeGitCannotSayIsListedWithoutItsLines(t *testing.T) {
	dir := scratch(t)
	write(t, dir, "a.txt", "one\n")
	c1 := commit(t, dir, time.Time{}, "one")
	write(t, dir, "a.txt", "two\n")
	gitIn(t, dir, time.Time{}, "add", "a.txt")
	gitIn(t, dir, time.Time{}, "update-index", "--add", "--cacheinfo", "100644,"+strings.Repeat("ab", 20)+",ghost.txt")
	tree := gitIn(t, dir, time.Time{}, "write-tree", "--missing-ok")
	c2 := gitIn(t, dir, time.Time{}, "commit-tree", tree, "-p", c1, "-m", "a blob missing")
	want := []Change{{Path: "a.txt", Status: "M", Added: 1, Removed: 1}, {Path: "ghost.txt", Status: "A", Binary: true}}
	for name, read := range map[string]func() (Detail, error){
		"Compare":      func() (Detail, error) { return Compare(context.Background(), dir, c1, c2) },
		"CommitDetail": func() (Detail, error) { return CommitDetail(context.Background(), dir, c2) },
	} {
		if d, err := read(); err != nil || !slices.Equal(d.Changes, want) {
			t.Errorf("%s: %+v %v, want %+v", name, d.Changes, err, want)
		}
	}
}

// No call of go-git that reads a blob's content, or decodes a blob to
// read it, is left in the package's code: a comparison and FileAt read a
// file's content through git (blobs). go-git's search for renames reads
// the files it compares itself (withRenames bounds it).
func TestNoBlobIsReadThroughGoGit(t *testing.T) {
	never := []string{"Contents", "IsBinary", "Reader", "Lines", "Files", "File", "TreeEntryFile", "BlobObject", "BlobObjects", "GetBlob", "DecodeBlob", "NewFile"}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains(never, sel.Sel.Name) {
					t.Errorf("%s: a call of %s", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// A file within the bound for a diffed file that a pack stores as a delta
// of a 32 MiB blob, changed in a commit, is listed with its lines by
// CommitDetail and by Compare, and neither allocates more than
// comparisonAllocBound: git reads the file, in a process of its own.
func TestAFilePackedAsADeltaOfALargeBlobIsComparedWithBoundedMemory(t *testing.T) {
	stillClock(t)
	dir, small, changed := deltaOfLarge(t)
	ctx := context.Background()
	want := []Change{{Path: "f.txt", Status: "M", Added: 1, Removed: 1}}
	for name, read := range map[string]func() (Detail, error){
		"CommitDetail": func() (Detail, error) { return CommitDetail(ctx, dir, changed) },
		"Compare":      func() (Detail, error) { return Compare(ctx, dir, small, changed) },
	} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		d, err := read()
		runtime.ReadMemStats(&after)
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %d bytes allocated", name, alloc)
		if err != nil || !slices.Equal(d.Changes, want) {
			t.Errorf("%s: %+v %v, want %+v", name, d.Changes, err, want)
		}
		if alloc > comparisonAllocBound {
			t.Errorf("%s: %d bytes allocated for a file of %d bytes a side, past %d", name, alloc, MaxDiffed, comparisonAllocBound)
		}
	}
}
