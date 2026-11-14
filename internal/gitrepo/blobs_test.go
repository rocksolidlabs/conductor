package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// deltaOfLarge is a repository whose file f.txt is large bytes in its
// first commit, its first small bytes in the second, and those with a line
// changed in the third, at the path to (f.txt moved there when to is
// another), packed (git gc --aggressive) with each small version stored as
// a delta of the large one when the file stays at f.txt, and one of them
// at least when it moved (git may keep the other whole): the second and
// the third commits.
func deltaOfLarge(t *testing.T, large, small int, to string) (dir, second, third string) {
	t.Helper()
	dir = scratch(t)
	var b strings.Builder
	for i := 0; b.Len() < large; i++ {
		fmt.Fprintf(&b, "a line of a file far past the bound, number %08d, and more words to make it long\n", i)
	}
	whole := b.String()[:large]
	write(t, dir, "f.txt", whole)
	commit(t, dir, time.Time{}, "large")
	write(t, dir, "f.txt", whole[:small])
	second = commit(t, dir, time.Time{}, "small")
	if to != "f.txt" {
		gitIn(t, dir, time.Time{}, "rm", "-q", "f.txt")
	}
	write(t, dir, to, strings.Replace(whole[:small], "number 00000100,", "number 0000one0,", 1))
	third = commit(t, dir, time.Time{}, "changed")
	gitIn(t, dir, time.Time{}, "gc", "-q", "--aggressive", "--prune=now")
	base := gitIn(t, dir, time.Time{}, "rev-parse", second+"~1:f.txt")
	deltaOf := map[string]string{}
	for _, line := range strings.Split(gitIn(t, dir, time.Time{}, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objectsize) %(deltabase)"), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[1] == strconv.Itoa(small) {
			deltaOf[f[0]] = f[2]
		}
	}
	// Each small version's chain of deltas: a delta of the large one,
	// directly or through the other small one.
	ofLarge := 0
	for _, at := range []string{second + ":f.txt", third + ":" + to} {
		k := deltaOf[gitIn(t, dir, time.Time{}, "rev-parse", at)]
		if k != "" && k != base {
			k = deltaOf[k]
		}
		if k == base {
			ofLarge++
		}
	}
	if ofLarge < 2 && (to == "f.txt" || ofLarge == 0) {
		t.Fatalf("%d of the small versions are stored as a delta of the %d-byte blob %s: %v", ofLarge, large, base, deltaOf)
	}
	return dir, second, third
}

// comparisonAllocBound is what one comparison of a file of MaxDiffed
// bytes a side may allocate: its two sides, their texts and the diff of
// what is left of them (4.5 MiB measured; 175 MiB when go-git read the
// sides).
const comparisonAllocBound = 16 << 20

// run is one run of git through the package's seam (runGit): its
// arguments, the environment it is given and the ids on its input, and
// the error it returned, once it has.
type run struct {
	args     []string
	env      map[string]string
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
		r := &run{args: args, env: env, ids: strings.Fields(string(b))}
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
// runs its look for the object store and at the configuration before each
// (six processes): once
// for the sizes of the 200 sides and once for their contents, each with
// the 200 ids on its input, and each with replacements off and the cache
// of deltas' bases at git's own default limit, as command-line
// configuration (readEnv).
func TestAComparisonOfAHundredFilesRunsGitTwice(t *testing.T) {
	want := map[string]string{"GIT_NO_REPLACE_OBJECTS": "1", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.deltaBaseCacheLimit", "GIT_CONFIG_VALUE_0": "96m"}
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
			args := []string{"cat-file --batch-check", "cat-file --batch"}
			if got := argsOf(*runs); !slices.Equal(got, args) || len((*runs)[0].ids) != 200 || !slices.Equal((*runs)[0].ids, (*runs)[1].ids) {
				t.Fatalf("git ran %d times, %q; want %q, each with the 200 ids", len(got), got, args)
			}
			for _, r := range *runs {
				if !maps.Equal(r.env, want) {
					t.Errorf("git %q was given %v, want %v", r.args, r.env, want)
				}
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

// No call of go-git that reads a blob's content or its size, or decodes a
// blob to read it, is left in the package's code: a comparison and FileAt
// read a file's size and content through git (sizes, blobs), and go-git's
// search for renames reads the files git read, from memory (withRenames).
func TestNoBlobIsReadThroughGoGit(t *testing.T) {
	never := []string{"Contents", "IsBinary", "Reader", "Lines", "Files", "File", "TreeEntryFile", "BlobObject", "BlobObjects", "GetBlob", "DecodeBlob", "NewFile",
		"EncodedObjectSize", "EncodedObject", "Patch", "PatchContext", "Stats"}
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
	dir, small, changed := deltaOfLarge(t, 32<<20, MaxDiffed, "f.txt")
	comparedWithin(t, dir, small, changed, []Change{{Path: "f.txt", Status: "M", Added: 1, Removed: 1}})
}

// A file moved and changed a little, a blob of its two stored by the pack
// as a small delta of a blob of 15 MiB, is found as a rename, with its
// lines,
// by CommitDetail and by Compare, and neither allocates more than
// comparisonAllocBound: the search for renames reads the two files git
// read, from memory.
func TestAMovedFilePackedAsADeltaOfALargeBlobIsFoundWithBoundedMemory(t *testing.T) {
	stillClock(t)
	dir, small, moved := deltaOfLarge(t, 15<<20, 500<<10, "g.txt")
	comparedWithin(t, dir, small, moved, []Change{{Path: "g.txt", From: "f.txt", Status: "R", Added: 1, Removed: 1}})
}

// comparedWithin checks that CommitDetail of the commit to, the child of
// from, and Compare of from and to answer the files want, each allocating
// at most comparisonAllocBound.
func comparedWithin(t *testing.T, dir, from, to string, want []Change) {
	t.Helper()
	ctx := context.Background()
	for name, read := range map[string]func() (Detail, error){
		"CommitDetail": func() (Detail, error) { return CommitDetail(ctx, dir, to) },
		"Compare":      func() (Detail, error) { return Compare(ctx, dir, from, to) },
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
		if d.RenamesCut || alloc > comparisonAllocBound {
			t.Errorf("%s: renames cut %v, %d bytes allocated, past %d", name, d.RenamesCut, alloc, comparisonAllocBound)
		}
	}
}

// A file whose blob only another object store holds, one the repository
// names (objects/info/alternates), is listed without its lines by Compare
// and CommitDetail, FileAt finds no file there, and its id never reaches
// git: the repository's own store is asked first (holds).
func TestABlobOnlyAnotherStoreHoldsIsNotHandedToGit(t *testing.T) {
	other := scratch(t)
	write(t, other, "there.txt", "only there\n")
	elsewhere := gitIn(t, other, time.Time{}, "hash-object", "-w", "there.txt")
	dir := scratch(t)
	write(t, dir, "a.txt", "one\n")
	c1 := commit(t, dir, time.Time{}, "one")
	if err := os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), []byte(filepath.Join(other, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.txt", "two\n")
	gitIn(t, dir, time.Time{}, "add", "a.txt")
	gitIn(t, dir, time.Time{}, "update-index", "--add", "--cacheinfo", "100644,"+elsewhere+",b.txt")
	tree := gitIn(t, dir, time.Time{}, "write-tree")
	c2 := gitIn(t, dir, time.Time{}, "commit-tree", tree, "-p", c1, "-m", "a blob only another store holds")
	runs := watchGit(t, nil)
	ctx := context.Background()
	want := []Change{{Path: "a.txt", Status: "M", Added: 1, Removed: 1}, {Path: "b.txt", Status: "A", Binary: true}}
	for name, read := range map[string]func() (Detail, error){
		"Compare":      func() (Detail, error) { return Compare(ctx, dir, c1, c2) },
		"CommitDetail": func() (Detail, error) { return CommitDetail(ctx, dir, c2) },
	} {
		if d, err := read(); err != nil || !slices.Equal(d.Changes, want) {
			t.Errorf("%s: %+v %v, want %+v", name, d.Changes, err, want)
		}
	}
	if _, err := FileAt(ctx, dir, c2, "b.txt", 100); !errors.Is(err, ErrNoFile) {
		t.Errorf("FileAt: %v, want %v", err, ErrNoFile)
	}
	for _, r := range *runs {
		if slices.Contains(r.ids, elsewhere) {
			t.Errorf("git %q was handed %s, a blob only another store holds", r.args, elsewhere)
		}
	}
}

// playGit makes the seam (runGit) play git's part: it reads its input and
// answers out to its writer, whole or a byte a write, and returns the
// writer's refusal, as gitcli returns once its writer refuses what git
// writes.
func playGit(t *testing.T, out string, byByte bool) {
	t.Helper()
	was := runGit
	runGit = func(_ context.Context, _ string, _ map[string]string, in io.Reader, w io.Writer, _ ...string) error {
		if _, err := io.ReadAll(in); err != nil {
			return err
		}
		for p := out; p != ""; {
			k := len(p)
			if byByte {
				k = 1
			}
			if _, err := w.Write([]byte(p[:k])); err != nil {
				return err
			}
			p = p[k:]
		}
		return nil
	}
	t.Cleanup(func() { runGit = was })
}

// The reader takes git's answer only as the answer asked for: of blobs,
// each object's header with its id, the type blob and its size, that many
// bytes and a newline, the objects in the order asked and nothing more;
// of sizes, one line an id, in their order, each naming its id. Every
// other answer, written whole or a byte a write, is refused, or (sizes)
// answers an object whose size git did not say as one without a size,
// and never hands on another object's bytes or size.
func TestTheReaderTakesOnlyTheAnswerAsked(t *testing.T) {
	h1, h2, h3 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	obj := func(h, s string) string { return fmt.Sprintf("%s blob %d\n%s\n", h, len(s), s) }
	asked := obj(h1, "one\n") + obj(h2, "two\n")
	contents := []string{"one\n", "two\n"}
	for _, c := range []struct {
		name, out string
		ok        bool
	}{
		{"the answer asked", asked, true},
		{"another id", obj(h2, "two\n") + obj(h1, "one\n"), false},
		{"an id not asked", obj(h3, "one\n") + obj(h2, "two\n"), false},
		{"another size", h1 + " blob 5\none\n!\n" + obj(h2, "two\n"), false},
		{"another type", h1 + " tree 4\none\n\n" + obj(h2, "two\n"), false},
		{"missing", h1 + " missing\n" + obj(h2, "two\n"), false},
		{"no newline after the bytes", h1 + " blob 4\none\n!" + obj(h2, "two\n"), false},
		{"fewer bytes than told", h1 + " blob 4\non", false},
		{"more than was asked", asked + obj(h3, "x\n"), false},
		{"an object twice", obj(h1, "one\n") + asked, false},
		{"an object missing", obj(h1, "one\n"), false},
		{"a size that is no number", h1 + " blob four\none\n\n" + obj(h2, "two\n"), false},
		{"a negative size", h1 + " blob -4\none\n\n" + obj(h2, "two\n"), false},
	} {
		for _, byByte := range []bool{false, true} {
			t.Run(fmt.Sprintf("blobs, %s, a byte a write %v", c.name, byByte), func(t *testing.T) {
				playGit(t, c.out, byByte)
				var got []string
				err := blobs(context.Background(), t.TempDir(), []blob{{plumbing.NewHash(h1), 4, 4}, {plumbing.NewHash(h2), 4, 4}}, func(i int, b []byte) error {
					if string(b) != contents[i] {
						t.Errorf("handed on %q as object %d, want %q", b, i, contents[i])
					}
					got = append(got, string(b))
					return nil
				})
				if (err == nil) != c.ok || (c.ok && !slices.Equal(got, contents)) {
					t.Errorf("%q, %v; want refused: %v", got, err, !c.ok)
				}
			})
		}
	}
	line := func(h string, n int) string { return fmt.Sprintf("%s blob %d\n", h, n) }
	for _, c := range []struct {
		name, out string
		want      []int64 // nil: refused
	}{
		{"the answer asked", line(h1, 4) + line(h2, 5), []int64{4, 5}},
		{"another id", line(h2, 5) + line(h1, 4), nil},
		{"an id not asked", line(h3, 4) + line(h2, 5), nil},
		{"another type", h1 + " tree 4\n" + line(h2, 5), []int64{-1, 5}},
		{"missing", h1 + " missing\n" + line(h2, 5), []int64{-1, 5}},
		{"a field more", h1 + " blob 4 x\n" + line(h2, 5), []int64{-1, 5}},
		{"no newline after the last line", line(h1, 4) + h2 + " blob 5", nil},
		{"fewer bytes than a line", line(h1, 4) + h2 + " bl", nil},
		{"more than was asked", line(h1, 4) + line(h2, 5) + line(h3, 1), nil},
		{"a line past its bound", h1 + " blob " + strings.Repeat("9", 200) + "\n" + line(h2, 5), nil},
		{"a line twice", line(h1, 4) + line(h1, 4) + line(h2, 5), nil},
		{"a line missing", line(h1, 4), nil},
		{"a size that is no number", h1 + " blob four\n" + line(h2, 5), []int64{-1, 5}},
		{"a negative size", h1 + " blob -4\n" + line(h2, 5), []int64{-1, 5}},
	} {
		for _, byByte := range []bool{false, true} {
			t.Run(fmt.Sprintf("sizes, %s, a byte a write %v", c.name, byByte), func(t *testing.T) {
				playGit(t, c.out, byByte)
				got, err := sizes(context.Background(), t.TempDir(), []plumbing.Hash{plumbing.NewHash(h1), plumbing.NewHash(h2)})
				if (err == nil) != (c.want != nil) || !slices.Equal(got, c.want) {
					t.Errorf("%v, %v; want %v (nil: refused)", got, err, c.want)
				}
			})
		}
	}
}

// counting counts the bytes written through it to w, those w refuses too.
type counting struct {
	w io.Writer
	n int
}

func (c *counting) Write(p []byte) (int, error) {
	c.n += len(p)
	return c.w.Write(p)
}

// FileAt of a file past its limit ends the git writing the blob once the
// bytes it keeps are in, before git wrote the whole blob: the seam's
// writer counts what git wrote.
func TestFileAtEndsGitOnceTheHeadIsIn(t *testing.T) {
	dir := scratch(t)
	var b strings.Builder
	for i := 0; b.Len() < 4<<20; i++ {
		fmt.Fprintf(&b, "line %08d\n", i)
	}
	write(t, dir, "big.txt", b.String())
	c := commit(t, dir, time.Time{}, "big")
	wrote, was := map[string]int{}, runGit
	runGit = func(ctx context.Context, workTree string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		cw := &counting{w: w}
		err := was(ctx, workTree, env, in, cw, args...)
		wrote[strings.Join(args, " ")] += cw.n
		return err
	}
	t.Cleanup(func() { runGit = was })
	f, err := FileAt(context.Background(), dir, c, "big.txt", 100)
	if err != nil || !f.Truncated || string(f.Content) != b.String()[:100] || f.Size != int64(b.Len()) {
		t.Fatalf("%d bytes, truncated %v, size %d, %v; want the first 100 of %d", len(f.Content), f.Truncated, f.Size, err, b.Len())
	}
	if n := wrote["cat-file --batch"]; n == 0 || n >= b.Len() {
		t.Errorf("git wrote %d bytes of a blob of %d; want it ended once the head was in", n, b.Len())
	}
}
