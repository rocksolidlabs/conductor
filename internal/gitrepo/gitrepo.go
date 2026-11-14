// Package gitrepo reads a repository's history with go-git (design round
// 12, F5): the commits on the checked-out branch since a time or a base,
// and one commit's files with their added and removed lines against its
// first parent. The log needs no git binary; a crew's linked worktree
// opens through its common directory. A commit's files, a comparison's
// files and a file's content are found with go-git and read through the
// git binary, their sizes and their contents (sizes, blobs, which say
// why); git is handed only ids the opened repository's own store lists,
// in its pack indexes and its loose files (holds), and reads, itself, that
// store and whatever stores it names. The working tree's status and a
// file at a revision stay with internal/gitcli, which the git binary does
// faster.
package gitrepo

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"github.com/go-git/go-git/v5/storage/memory"
	godiff "github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// Bounds of what one read returns.
const (
	MaxCommits = 200     // commits in a log
	MaxScan    = 5000    // commits walked to find them
	MaxFiles   = 500     // files of one commit
	MaxBody    = 4096    // bytes of a commit message's body
	MaxDiffed  = 1 << 20 // a file larger than this is listed without its lines
	// MaxDiffLines, MaxFileDiff and MaxCompareDiff bound a comparison's
	// line diffs (lineDiff). A file's diff is made on what is left once
	// the lines its two sides share at their start, and then at their
	// end, are set aside: when neither side has more than MaxDiffLines
	// lines left, within its time, the less of MaxFileDiff and what is
	// left of the comparison's MaxCompareDiff. A side with no line left
	// is counted without a diff. A file past MaxDiffLines, one whose diff
	// did not finish within its time, and every file after the
	// comparison's time is spent are listed without their lines. The
	// slowest diffs measured at MaxDiffLines lines a side took 1.6 s at a
	// load average of 7 on 64 cores; a busier machine takes longer, and a
	// file whose diff took its time is listed without its lines. The
	// slowest file of this repository's 3,860 commits took 45 ms, and its
	// slowest commit's files together 0.2 s.
	MaxDiffLines   = 20000
	MaxFileDiff    = 2 * time.Second
	MaxCompareDiff = 10 * time.Second
	// MaxRenamed and MaxRenamedBytes bound the look for renames
	// (withRenames). The files are compared first with no look for
	// renames; renames are then looked for, by go-git's own search, whole,
	// only when that comparison adds at most MaxRenamed files and deletes
	// at most MaxRenamed, those files together at most MaxRenamedBytes as
	// git records their sizes, and the search's answer is
	// taken only when it holds every path of the comparison. Otherwise
	// every file is listed as added or deleted, with its lines, and
	// Detail.RenamesCut says so. The search hashes each deleted ordinary
	// file once and each added one once for each deleted file of a size
	// near its own, from memory, read by git. Its time is beside the
	// comparison's time for line diffs (MaxCompareDiff), not inside it:
	// it runs before them, and it ends with the read's context, and at the
	// comparison's cap, at its next read of a file (fromMemory).
	MaxRenamed      = 100
	MaxRenamedBytes = 1 << 20
	shortLen        = 7
)

// clock is the time a file's line diff takes is read by (lineDiff); a
// test sets it.
var clock = time.Now

// ErrNotRepo says the directory is not inside a repository.
var ErrNotRepo = errors.New("not a git repository")

// ErrNoCommit says a commit Compare or FileAt was given is not in the
// repository (pruned, or never there); ErrNoFile that the commit has no
// file at the path (none, a folder, a submodule).
var (
	ErrNoCommit = errors.New("no such commit in the repository")
	ErrNoFile   = errors.New("no such file at the commit")
)

// Commit is one commit of a log, or the commit of a Detail.
type Commit struct {
	Sha     string
	Short   string
	Subject string
	Body    string // Detail only, at most MaxBody bytes
	Author  string
	Email   string
	At      time.Time // the committer's time
	Parent  string    // the first parent's sha, empty for a root commit
	Parents int
}

// Log is the commits Log found, newest first.
type Log struct {
	Top       string // the working tree's root
	Branch    string // the branch checked out, empty when detached
	Base      string // the base's short id when a base was asked for and found
	Commits   []Commit
	Truncated bool
}

// Change is one file a commit touched.
type Change struct {
	Path    string // relative to the top
	From    string // a rename's old path
	Status  string // A, M, D, R
	Added   int
	Removed int
	Binary  bool
}

// Detail is one commit and its files.
type Detail struct {
	Top       string
	Commit    Commit
	Changes   []Change
	Added     int
	Removed   int
	Truncated bool
	// RenamesCut says a file listed as deleted may have moved: renames
	// were not looked for (MaxRenamed, MaxRenamedBytes), or not told
	// apart, among the files added and deleted (renamesCut).
	RenamesCut bool
}

// open is the repository at dir or the nearest folder above it, a crew's
// linked worktree through its common directory, as go-git's
// PlainOpenWithOptions finds it with DetectDotGit and
// EnableDotGitCommonDir (gitDirs, commonDir), every file of it opened
// through careOS: the one way the package opens a repository. The
// discovery looks above a folder only when the folder has no .git entry;
// a .git that leads to nothing that can be read is refused. ErrNotRepo
// when there is none; ErrUnreadable (wrapped) when one of its files was
// refused, whatever go-git made of the refusal; ctx's error once ctx has
// ended (ended); seen keeps those of the reads that follow.
func open(ctx context.Context, dir string) (r *git.Repository, top string, seen *refusal, err error) {
	seen = &refusal{ctx: ctx}
	r, top, err = openOver(dir, seen)
	if refused := seen.first(); refused != nil {
		err = refused
	} else if errors.Is(err, git.ErrRepositoryNotExists) {
		err = ErrNotRepo
	}
	if err != nil {
		seen.close()
		return nil, "", nil, err
	}
	return r, top, seen, nil
}

func openOver(dir string, seen *refusal) (*git.Repository, string, error) {
	dot, wt, err := gitDirs(dir, seen)
	if err != nil {
		return nil, "", err
	}
	if _, err := dot.Stat(""); err != nil {
		if os.IsNotExist(err) {
			return nil, "", git.ErrRepositoryNotExists
		}
		return nil, "", err
	}
	common, err := commonDir(dot, seen)
	if err != nil {
		return nil, "", err
	}
	// A storage that reads an object past largeObject as a stream from
	// its file, a delta applied as it is read, never whole in memory:
	// go-git's own opening reads every object whole, however large (a
	// blob of a gigabyte, a gigabyte of heap, and a delta's base besides).
	files := dotgit.NewRepositoryFilesystem(dot, common)
	st := filesystem.NewStorageWithOptions(files, ending{cache.NewObjectLRUDefault(), seen}, filesystem.Options{LargeObjectThreshold: largeObject})
	r, err := git.Open(st, wt)
	if err != nil {
		return nil, "", err
	}
	top := dir
	if w, err := r.Worktree(); err == nil {
		top = w.Filesystem.Root()
	}
	return r, top, nil
}

// ending is an opening's object cache: it finds nothing once the read's
// context has ended (ended), so the step that looked reads the object from
// its file, which careOS refuses.
type ending struct {
	cache.Object
	seen *refusal
}

func (c ending) Get(h plumbing.Hash) (plumbing.EncodedObject, bool) {
	if c.seen.ended() != nil {
		return nil, false
	}
	return c.Object.Get(h)
}

// read is f over the repository at dir, opened for it, and f once more
// over a second opening when the first finds an object, a commit or a file
// gone: go-git lists a repository's packs once, at its first read from one,
// and git repacks a repository by itself meanwhile (every commit may start
// `git maintenance run --auto`, which from Git 2.5x on packs a few hundred
// loose objects and deletes them, and drops packs it merged), so a read
// begun before the repack looks for an object in a pack it never listed or
// in a pack since removed. The second opening lists them again. A commit,
// a file or an object the store does not list (holds) that truly is not
// there is looked for twice. Once ctx has
// ended, as read is about to answer (an opening failed, or f returned, on
// either attempt), ctx's error is the answer, whatever the opening kept
// and whatever f answered; before that, a file refused on the way
// (careOS) is the answer, whatever f made of it.
func read[T any](ctx context.Context, dir string, f func(r *git.Repository, top string, seen *refusal) (T, error)) (T, error) {
	var none T
	r, top, seen, err := open(ctx, dir)
	if err != nil {
		return none, cmp.Or(ctx.Err(), err)
	}
	out, err := f(r, top, seen)
	seen.close()
	if refused := cmp.Or(ctx.Err(), seen.first()); refused != nil {
		return none, refused
	}
	if !gone(err) {
		return out, err
	}
	if r, top, seen, err = open(ctx, dir); err != nil {
		return out, cmp.Or(ctx.Err(), err)
	}
	seen.again = true
	out, err = f(r, top, seen)
	seen.close()
	if refused := cmp.Or(ctx.Err(), seen.first()); refused != nil {
		return none, refused
	}
	return out, err
}

// gone reports whether err says an object was not found where go-git
// looked for it (read).
func gone(err error) bool {
	return errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, dotgit.ErrPackfileNotFound) ||
		errors.Is(err, ErrNoCommit) || errors.Is(err, ErrNoFile)
}

// largeObject is the size past which an object go-git reads (a commit, a
// tree) is read as a stream rather than whole. No blob is read through
// go-git (blobs).
const largeObject = 256 << 10

// Commits lists the commits on HEAD newest first: those after base's merge
// base with HEAD when base names a revision, else those committed at or
// after since. A repository with no commit yet has none.
func Commits(ctx context.Context, dir, base string, since time.Time) (Log, error) {
	return read(ctx, dir, func(r *git.Repository, top string, _ *refusal) (Log, error) { return commits(ctx, r, top, base, since) })
}

func commits(ctx context.Context, r *git.Repository, top, base string, since time.Time) (Log, error) {
	out := Log{Top: top}
	head, err := r.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			if ref, err := r.Reference(plumbing.HEAD, false); err == nil && ref.Target().IsBranch() {
				out.Branch = ref.Target().Short()
			}
			return out, nil
		}
		return out, err
	}
	if head.Name().IsBranch() {
		out.Branch = head.Name().Short()
	}
	headCommit, err := r.CommitObject(head.Hash())
	if err != nil {
		return out, err
	}
	var stop plumbing.Hash
	useBase := false
	if base != "" {
		if h, err := r.ResolveRevision(plumbing.Revision(base)); err == nil {
			out.Base = h.String()[:shortLen]
			if bc, err := r.CommitObject(*h); err == nil {
				if mb, err := headCommit.MergeBase(bc); err == nil && len(mb) > 0 {
					stop, useBase = mb[0].Hash, true
				}
			}
		}
	}
	// Git keeps whole seconds: a commit made in the second the session
	// started counts.
	since = since.Truncate(time.Second)
	it, err := r.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return out, err
	}
	defer it.Close()
	scanned := 0
	err = it.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if useBase && c.Hash == stop {
			return storer.ErrStop
		}
		if !useBase && c.Committer.When.Before(since) {
			return storer.ErrStop
		}
		scanned++
		if len(out.Commits) == MaxCommits || scanned > MaxScan {
			out.Truncated = true
			return storer.ErrStop
		}
		out.Commits = append(out.Commits, summary(c, false))
		return nil
	})
	return out, err
}

// CommitDetail is the commit rev names and its files against its first
// parent (every file added, for a root commit), renames found.
func CommitDetail(ctx context.Context, dir, rev string) (Detail, error) {
	return read(ctx, dir, func(r *git.Repository, top string, seen *refusal) (Detail, error) {
		return commitDetail(ctx, seen, r, top, rev)
	})
}

func commitDetail(ctx context.Context, seen *refusal, r *git.Repository, top, rev string) (Detail, error) {
	h, err := r.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return Detail{}, fmt.Errorf("unknown revision %q", rev)
	}
	c, err := r.CommitObject(*h)
	if err != nil {
		return Detail{}, err
	}
	out := Detail{Top: top, Commit: summary(c, true)}
	tree, err := c.Tree()
	if err != nil {
		return out, err
	}
	var parentTree *object.Tree
	if c.NumParents() > 0 {
		p, err := c.Parent(0)
		if err != nil {
			return out, err
		}
		if parentTree, err = p.Tree(); err != nil {
			return out, err
		}
	}
	return diffTrees(ctx, seen, r.Storer, top, out, parentTree, tree)
}

// Compare is the files that differ between the trees of the commits from
// and to, both full object ids read by id (no revision is resolved, so no
// reference names another commit), renames found, each with its added and
// removed lines, as CommitDetail answers for one commit against its
// parent; Commit is to's. At most MaxFiles files, Truncated past them; a
// file past MaxDiffed, or whose line diff was not made (past MaxDiffLines)
// or did not finish within its time (lineDiff), is listed without its
// lines. ErrNoCommit (wrapped) for an id the repository has no commit of.
func Compare(ctx context.Context, dir, from, to string) (Detail, error) {
	return read(ctx, dir, func(r *git.Repository, top string, seen *refusal) (Detail, error) {
		return compare(ctx, seen, r, top, from, to)
	})
}

func compare(ctx context.Context, seen *refusal, r *git.Repository, top, from, to string) (Detail, error) {
	fc, err := commitByID(r, from)
	if err != nil {
		return Detail{}, err
	}
	tc, err := commitByID(r, to)
	if err != nil {
		return Detail{}, err
	}
	out := Detail{Top: top, Commit: summary(tc, true)}
	fromTree, err := fc.Tree()
	if err != nil {
		return out, err
	}
	toTree, err := tc.Tree()
	if err != nil {
		return out, err
	}
	return diffTrees(ctx, seen, r.Storer, top, out, fromTree, toTree)
}

// File is a file's bytes at a commit (FileAt): at most the bound asked,
// Truncated past it; Binary when git would call it so (a NUL in its first
// 8,000 bytes), with no content then; Size its whole size.
type File struct {
	Content   []byte
	Truncated bool
	Binary    bool
	Size      int64
}

// FileAt reads the file at path (relative to the repository's top, with
// '/') in the tree of the commit id (a full object id, read by id), at
// most limit bytes of it, the memory it takes bounded by about that,
// however large the file and however it is stored (blobs). A link is its
// target's text, as git keeps it, never followed. ErrNoCommit (wrapped)
// for an id the repository has no commit of, ErrNoFile for a path the
// commit has no file at.
func FileAt(ctx context.Context, dir, id, path string, limit int) (File, error) {
	return read(ctx, dir, func(r *git.Repository, top string, seen *refusal) (File, error) {
		return fileAt(ctx, seen, r, top, id, path, limit)
	})
}

func fileAt(ctx context.Context, seen *refusal, r *git.Repository, top, id, path string, limit int) (File, error) {
	c, err := commitByID(r, id)
	if err != nil {
		return File{}, err
	}
	tree, err := c.Tree()
	if err != nil {
		return File{}, err
	}
	e, err := entryAt(r, tree, path)
	if err != nil {
		return File{}, err
	}
	if ok, err := holds(seen, r.Storer, e.Hash); err != nil {
		return File{}, err
	} else if !ok {
		return File{}, ErrNoFile
	}
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	// The size, then the object read once, at most what the reply and
	// git's binary sniff need of it (blobs), by the git binary, which
	// opens the repository's files itself: it is ended at its cap,
	// whatever ctx allows.
	ctx, cancel := capped(ctx, gitCap)
	defer cancel()
	size, err := sizes(ctx, top, []plumbing.Hash{e.Hash})
	if err == nil && size[0] < 0 {
		err = fmt.Errorf("git cat-file --batch-check: no blob %s", e.Hash)
	}
	if err != nil {
		return File{}, atCap(ctx, err)
	}
	out := File{Size: size[0]}
	var b []byte
	head := blob{e.Hash, size[0], min(max(int64(limit)+1, sniffLen), size[0])}
	if err := blobs(ctx, top, []blob{head}, func(_ int, got []byte) error { b = got; return nil }); err != nil {
		return File{}, atCap(ctx, err)
	}
	if binary(b) {
		out.Binary = true
		return out, nil
	}
	if len(b) > limit {
		b, out.Truncated = b[:limit], true
	}
	out.Content = b
	return out, nil
}

// maxGitRead is the cap on FileAt's git binary: half a minute, above the
// 20 s the session gives one git read (session.GitTimeout), so a caller
// that bounds its read meets its own bound first.
const maxGitRead = 30 * time.Second

// maxCompareRead is the cap on a comparison's git binary, its runs
// together from the first (diffTrees): two minutes. The largest
// comparison the bounds allow (MaxFiles files of MaxDiffed bytes a side,
// packed, a line changed in each) took 3.3 s at a load average of 7 on 64
// cores and 5.6 s on four cores shared with six busy loops; the cap is ten
// times that with the comparison's own time bounds (MaxCompareDiff, the
// search for renames) beside it, above the 20 s the session gives one git
// read, as maxGitRead is.
const maxCompareRead = 2 * time.Minute

// gitCap and compareCap are maxGitRead and maxCompareRead, shortened by a
// test.
var gitCap, compareCap = maxGitRead, maxCompareRead

// ErrGitCap says the git binary of a read (FileAt's, a comparison's) was
// ended at its cap (capped).
var ErrGitCap = errors.New("the git binary of the read was ended at its cap")

// capped is ctx ended at the cap d, its cause ErrGitCap with d: the one way
// a read caps its git binary.
func capped(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	cause := fmt.Errorf("%w of %v", ErrGitCap, d)
	ctx, cancel := context.WithTimeoutCause(ctx, d, cause)
	if capping == nil {
		return ctx, cancel
	}
	ctx, end := context.WithCancelCause(ctx)
	capping(func() { end(cause) })
	return ctx, func() { end(nil); cancel() }
}

// capping, set by a test, is handed, as a read takes its cap, a function
// that ends the read's gits as the cap does, at a moment of the test's.
var capping func(end func())

// ErrNoTemp says a git of the read could not be started: the folder of its
// own it is given (runGit) could not be made in the temporary directory.
var ErrNoTemp = errors.New("the git binary of the read could not be started: no folder could be made for it in the temporary directory")

// atCap is err, or the cap's error when the cap ended ctx (capped).
func atCap(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, ErrGitCap) {
		return cause
	}
	return err
}

// holds reports whether the repository's own object store (st, the one
// the opener opened) lists the object h, in its pack indexes or its loose
// files, reading no content: the one look a read makes before it hands an
// id to git (heldSizes, fileAt), so git is handed only ids that store
// lists. git itself reads that store and whatever stores it names, so an
// object the store lists and cannot give is read where git finds it. The
// store is looked at as the read opened it, and a repack meanwhile moves
// objects into a pack it does not list: on the read's first opening an
// object it does not list is an object not found, and the read opens the
// repository again and asks once more (read); on the second, false.
func holds(seen *refusal, st storer.EncodedObjectStorer, h plumbing.Hash) (bool, error) {
	err := st.HasEncodedObject(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) && seen.again {
		return false, nil
	}
	return err == nil, err
}

// heldSizes is sizes of hs, -1 for one the repository does not hold
// (holds), whose id git is never handed; with every, nil, and no git run,
// when one of hs is not held. Each of hs is asked before git runs.
func heldSizes(ctx context.Context, seen *refusal, st storer.EncodedObjectStorer, top string, hs []plumbing.Hash, every bool) ([]int64, error) {
	out, asked, at := make([]int64, len(hs)), []plumbing.Hash(nil), []int(nil)
	for i, h := range hs {
		ok, err := holds(seen, st, h)
		if err != nil {
			return nil, err
		}
		if !ok && every {
			return nil, nil
		}
		if out[i] = -1; ok {
			asked, at = append(asked, h), append(at, i)
		}
	}
	got, err := sizes(ctx, top, asked)
	for k, i := range at[:len(got)] {
		out[i] = got[k]
	}
	return out, err
}

// entryAt is the entry of the file at path in tree, found by reading the
// trees on the way to it and never a blob: go-git's Tree.File reads the
// blob's size, which for a blob packed as a delta inflates the delta's
// whole instruction stream into memory first (go-git v5.19.3's
// getObjectSize; megabytes of changed lines are megabytes of heap, and
// LargeObjectThreshold does not bound it), and its FindEntry reads an
// entry on the way as a tree whatever its mode (a/b.txt/c reads b.txt).
// ErrNoFile when there is no such entry, a folder on the way is not one,
// or the entry is not a file (a folder, a submodule).
func entryAt(r *git.Repository, tree *object.Tree, path string) (object.TreeEntry, error) {
	parts := strings.Split(path, "/")
	for i, name := range parts {
		k := slices.IndexFunc(tree.Entries, func(e object.TreeEntry) bool { return e.Name == name })
		if name == "" || k < 0 {
			return object.TreeEntry{}, ErrNoFile
		}
		e := tree.Entries[k]
		if i == len(parts)-1 {
			if !e.Mode.IsFile() {
				return object.TreeEntry{}, ErrNoFile
			}
			return e, nil
		}
		if e.Mode != filemode.Dir {
			return object.TreeEntry{}, ErrNoFile
		}
		next, err := object.GetTree(r.Storer, e.Hash)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return object.TreeEntry{}, ErrNoFile
		}
		if err != nil {
			return object.TreeEntry{}, err
		}
		tree = next
	}
	return object.TreeEntry{}, ErrNoFile
}

// runGit is how the package runs git: gitcli's RunTree, in the working
// tree the read opened (top) with its own .git, the read's context its
// end, with an empty folder of its own as the folder RunTree has git
// write objects to (the reader's gits write none), removed once git has
// ended (ErrNoTemp, and no git run, when it cannot be made); a test sets
// it.
var runGit = func(ctx context.Context, top string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
	objects, err := os.MkdirTemp("", "gitrepo-objects-")
	if err != nil {
		return ErrNoTemp
	}
	defer os.RemoveAll(objects)
	return gitcli.RunTree(ctx, top, objects, env, in, w, args...)
}

// sizes is the size of each blob of hs as git records it, -1 for one git
// cannot say (not there, not a blob), from one git (git cat-file
// --batch-check, the ids in full on its input, as readEnv sets it): git, a
// process of its own, works a delta's size out of the delta's first bytes,
// which go-git does by inflating the delta whole in this process
// (entryAt). What git answers is kept up to checkLine bytes an id, and
// taken only as one line an id, in their order, each naming its id.
func sizes(ctx context.Context, top string, hs []plumbing.Hash) ([]int64, error) {
	out := make([]int64, len(hs))
	if len(hs) == 0 {
		return out, nil
	}
	w := &headWriter{b: make([]byte, 0, len(hs)*checkLine)}
	if err := runGit(ctx, top, readEnv, ids(hs), w, "cat-file", "--batch-check"); err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(w.b), "\n")
	if len(lines) != len(hs)+1 || lines[len(hs)] != "" {
		return nil, fmt.Errorf("git cat-file --batch-check answered %d lines for %d ids", len(lines)-1, len(hs))
	}
	for i, line := range lines[:len(hs)] {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != hs[i].String() {
			return nil, fmt.Errorf("git cat-file --batch-check answered %q for %s", line, hs[i])
		}
		out[i] = -1
		if n, err := strconv.ParseInt(f[len(f)-1], 10, 64); err == nil && n >= 0 && len(f) == 3 && f[1] == "blob" {
			out[i] = n
		}
	}
	return out, nil
}

// checkLine bounds a line of git cat-file --batch-check: an id, a type, a
// size of twenty digits, two spaces and a newline.
const checkLine = 40 + 6 + 20 + 3

// ids is hs, an id in full a line, for git's input.
func ids(hs []plumbing.Hash) io.Reader {
	var b strings.Builder
	for _, h := range hs {
		b.WriteString(h.String() + "\n")
	}
	return strings.NewReader(b.String())
}

// blob is a blob blobs reads: its id, its size (sizes) and how much of it
// is kept, n, at most its size.
type blob struct {
	h       plumbing.Hash
	size, n int64
}

// blobs reads the blobs of want, in their order, from one git (git
// cat-file --batch, the ids in full on its input, as readEnv sets it),
// and hands each to each as it is read; an error of each ends the read
// with it. Each answer is checked: its header names the id, the type blob
// and the size asked, exactly that many bytes and a newline follow, and
// nothing more and nothing less comes; the bytes are what git's object
// store holds under that id (git does not check a stored object against
// its id as it reads it, nor did go-git). A blob kept in part (n below
// its size) is the last read: git is ended once its n bytes are in. git,
// a process of its own that ends with the read, resolves a delta and its
// base, whatever their sizes, within the two settings readEnv sets: one
// object held whole, a blob past its size streamed, and the bases of
// deltas in its cache within its limit. This process holds
// what each keeps and the pipe's buffers, never a blob past its n, its
// delta or its base: go-git reads a delta's base whole (a packed blob
// stored as a delta of a 32 MiB one, 165 MiB allocated for its first MiB,
// measured with go-git v5.19.3), and a delta's instructions whole for its
// size, whatever the blob's size.
func blobs(ctx context.Context, top string, want []blob, each func(i int, b []byte) error) error {
	if len(want) == 0 {
		return nil
	}
	hs := make([]plumbing.Hash, len(want))
	for i, w := range want {
		hs[i] = w.h
	}
	r := &batch{want: want, each: each}
	err := runGit(ctx, top, readEnv, ids(hs), r, "cat-file", "--batch")
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case r.err != nil:
		return r.err
	case r.i < len(want):
		return cmp.Or(err, fmt.Errorf("git cat-file --batch answered %d blobs of %d", r.i, len(want)))
	}
	return nil
}

// batch is git cat-file --batch's output as blobs reads it: for each blob,
// its header, its bytes and a newline.
type batch struct {
	want []blob
	each func(int, []byte) error
	i    int    // the blob being read; len(want) once all are, or one in part
	at   int64  // the bytes of its header and its content read
	b    []byte // what is kept of it
	err  error  // what ended the read, git ended at a blob kept in part aside
}

func (r *batch) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if r.i == len(r.want) {
			r.err = errors.New("git cat-file --batch answered more than was asked")
			return n - len(p), r.err
		}
		w := r.want[r.i]
		head := fmt.Sprintf("%s blob %d\n", w.h, w.size)
		switch end := int64(len(head)) + w.size; {
		case r.at < int64(len(head)):
			k := min(int64(len(p)), int64(len(head))-r.at)
			if string(p[:k]) != head[r.at:r.at+k] {
				r.err = fmt.Errorf("git cat-file --batch answered other than %q", head[:len(head)-1])
				return n - len(p), r.err
			}
			if r.at, p = r.at+k, p[k:]; r.at == int64(len(head)) {
				r.b = make([]byte, 0, w.n)
			}
		case r.at < end:
			k := min(int64(len(p)), end-r.at)
			r.b = append(r.b, p[:min(k, w.n-int64(len(r.b)))]...)
			r.at, p = r.at+k, p[k:]
		case p[0] != '\n':
			r.err = fmt.Errorf("git cat-file --batch answered no newline after %s", w.h)
			return n - len(p), r.err
		default:
			p = p[1:]
			if r.err = r.each(r.i, r.b); r.err != nil {
				return n - len(p), r.err
			}
			r.i, r.at, r.b = r.i+1, 0, nil
			continue
		}
		if r.at >= int64(len(head)) && int64(len(r.b)) == w.n && w.n < w.size {
			r.i, r.err = len(r.want), r.each(r.i, r.b)
			return n - len(p), cmp.Or(r.err, errHeadFull)
		}
	}
	return n, nil
}

// readEnv is the reader's git's environment beside gitcli's: replacement
// objects (refs/replace) off, so the blob read is the one the tree
// records, as go-git reads it (go-git knows no replacements), never one
// put in its place; and the two settings that decide what git holds for
// one object at git's own defaults, the limit of its cache of deltas'
// bases (core.deltaBaseCacheLimit, 96 MiB) and the size past which it
// streams a blob rather than holding it whole (core.bigFileThreshold, 512
// MiB), set as command-line configuration, which outranks the
// repository's files. What git maps of a pack still follows the
// repository.
var readEnv = map[string]string{
	"GIT_NO_REPLACE_OBJECTS": "1",
	"GIT_CONFIG_COUNT":       "2",
	"GIT_CONFIG_KEY_0":       "core.deltaBaseCacheLimit",
	"GIT_CONFIG_VALUE_0":     "96m",
	"GIT_CONFIG_KEY_1":       "core.bigFileThreshold",
	"GIT_CONFIG_VALUE_1":     "512m",
}

// headWriter keeps what is written to it up to its buffer's capacity and
// then refuses more (errHeadFull), which ends the git writing to it.
type headWriter struct{ b []byte }

var errHeadFull = errors.New("the head is read")

func (w *headWriter) Write(p []byte) (int, error) {
	room := cap(w.b) - len(w.b)
	if len(p) <= room {
		w.b = append(w.b, p...)
		return len(p), nil
	}
	w.b = append(w.b, p[:room]...)
	return room, errHeadFull
}

// sniffLen is how much of a file git reads to call it binary (a NUL in its
// first 8,000 bytes).
const sniffLen = 8000

// binary reports whether git would call a file whose first bytes are b
// binary.
func binary(b []byte) bool { return bytes.IndexByte(b[:min(len(b), sniffLen)], 0) >= 0 }

// commitByID is the commit whose full object id is id: ErrNoCommit
// (wrapped) when the repository has none, an error for an id that is not
// one in full.
func commitByID(r *git.Repository, id string) (*object.Commit, error) {
	if !fullID(id) {
		return nil, fmt.Errorf("%q is not a commit id in full", id)
	}
	c, err := r.CommitObject(plumbing.NewHash(id))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrNoCommit, id)
	}
	if err == nil && afterCommit != nil {
		afterCommit()
	}
	return c, err
}

// afterCommit, set by a test, runs once commitByID has read a commit: a
// repack between that read and the next (read).
var afterCommit func()

// fullID reports whether s is a full object id: 40 lowercase hex digits
// (go-git reads SHA-1 repositories).
func fullID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// diffTrees is out with the files that differ between the trees from (nil
// for none: every file added) and to, renames found where they are looked
// for (withRenames), each with its counts (lineDiff), the read's context
// asked by each step that reads an object, CommitDetail's and Compare's
// walk, and between two files (ended). The sizes of the files' sides come
// from one git and their contents from one more, in top (heldSizes,
// blobs): a file with a side that is not a file (a submodule), that the
// repository does not hold, past MaxDiffed or whose size git cannot say
// is listed without its lines, its content not read. git is handed at
// most two ids a file, of at most MaxFiles files. Every git the
// comparison runs, the search for renames' too, ends at one cap
// (compareCap) taken before the first, with the cap's error (atCap).
func diffTrees(ctx context.Context, seen *refusal, st storer.EncodedObjectStorer, top string, out Detail, from, to *object.Tree) (Detail, error) {
	plain, err := object.DiffTreeContext(ctx, from, to)
	if err != nil {
		return out, err
	}
	ctx, cancel := capped(ctx, compareCap)
	defer cancel()
	changes, looked, dropped, err := withRenames(ctx, seen, st, top, plain)
	if err != nil {
		return out, atCap(ctx, err)
	}
	out.RenamesCut = renamesCut(plain, changes, looked, dropped)
	if len(changes) > MaxFiles {
		changes, out.Truncated = changes[:MaxFiles], true
	}
	var hs []plumbing.Hash
	var of []int // the file of each of hs
	for i, ch := range changes {
		fc := Change{}
		switch {
		case ch.From.Name == "":
			fc.Status, fc.Path = "A", ch.To.Name
		case ch.To.Name == "":
			fc.Status, fc.Path = "D", ch.From.Name
		case ch.From.Name != ch.To.Name:
			fc.Status, fc.Path, fc.From = "R", ch.To.Name, ch.From.Name
		default:
			fc.Status, fc.Path = "M", ch.To.Name
		}
		sides := slices.DeleteFunc([]object.ChangeEntry{ch.From, ch.To}, func(e object.ChangeEntry) bool { return e.Name == "" })
		fc.Binary = slices.ContainsFunc(sides, func(e object.ChangeEntry) bool { return !e.TreeEntry.Mode.IsFile() })
		for _, e := range sides {
			if !fc.Binary {
				hs, of = append(hs, e.TreeEntry.Hash), append(of, i)
			}
		}
		out.Changes = append(out.Changes, fc)
	}
	if err := seen.ended(); err != nil {
		return out, err
	}
	size, err := heldSizes(ctx, seen, st, top, hs, false)
	if err != nil {
		return out, atCap(ctx, err)
	}
	for j, n := range size {
		if n < 0 || n > MaxDiffed {
			out.Changes[of[j]].Binary = true // shown without its lines
		}
	}
	var want []blob
	var wantOf []int
	for j, h := range hs {
		if !out.Changes[of[j]].Binary {
			want, wantOf = append(want, blob{h, size[j], size[j]}), append(wantOf, of[j])
		}
	}
	left := MaxCompareDiff
	var got [][]byte // the sides read of the file being read
	err = blobs(ctx, top, want, func(k int, b []byte) error {
		if got = append(got, b); k+1 < len(want) && wantOf[k+1] == wantOf[k] {
			return nil
		}
		if err := seen.ended(); err != nil {
			return err
		}
		ch, fc := changes[wantOf[k]], &out.Changes[wantOf[k]]
		from, to := got[0], got[len(got)-1]
		if ch.From.Name == "" {
			from = nil
		} else if ch.To.Name == "" {
			to = nil
		}
		fc.Added, fc.Removed, fc.Binary = lineDiff(from, to, &left)
		got = nil
		return nil
	})
	if err != nil {
		return out, atCap(ctx, err)
	}
	for _, fc := range out.Changes {
		out.Added += fc.Added
		out.Removed += fc.Removed
	}
	return out, nil
}

// withRenames is plain, a comparison without renames, with its renames
// found by go-git's own search (detectRenames), whole (no limit of its
// own) and with the score go-git's defaults use, when plain adds a file
// and deletes one, at most MaxRenamed of each, those files together at
// most MaxRenamedBytes as git records their sizes (heldSizes, each asked
// whether the repository holds it before any git runs; one not held, or
// whose size cannot be had: not looked for). The search reads those files
// from memory, read by git (blobs, fromMemory), and its answer's changes are
// used by their names, modes and ids. looked says the search ran; dropped
// that its answer was not taken, because it failed or does not hold every
// path of plain (accounts): plain is answered then, and whenever the
// search did not run, sorted by path as the search sorts its answer. err
// is a git's, or ctx's once it has ended.
func withRenames(ctx context.Context, seen *refusal, st storer.EncodedObjectStorer, top string, plain object.Changes) (changes object.Changes, looked, dropped bool, err error) {
	sort.Stable(plain)
	var moved []plumbing.Hash
	added, deleted := 0, 0
	for _, ch := range plain {
		switch {
		case ch.From.Name == "":
			added++
			moved = append(moved, ch.To.TreeEntry.Hash)
		case ch.To.Name == "":
			deleted++
			moved = append(moved, ch.From.TreeEntry.Hash)
		}
	}
	if added == 0 || deleted == 0 || added > MaxRenamed || deleted > MaxRenamed {
		return plain, false, false, nil
	}
	size, err := heldSizes(ctx, seen, st, top, moved, true)
	if err != nil || size == nil {
		return plain, false, false, err
	}
	want, total := make([]blob, len(moved)), int64(0)
	for i, n := range size {
		if total += n; n < 0 || total > MaxRenamedBytes {
			return plain, false, false, nil
		}
		want[i] = blob{moved[i], n, n}
	}
	mem := inMemory{&memory.NewStorage().ObjectStorage, ctx}
	err = blobs(ctx, top, want, func(_ int, b []byte) error {
		o := &plumbing.MemoryObject{}
		o.SetType(plumbing.BlobObject)
		if _, err := o.Write(b); err != nil {
			return err
		}
		_, err := mem.SetEncodedObject(o)
		return err
	})
	if err != nil {
		return plain, false, false, err
	}
	in, err := fromMemory(mem, plain)
	if err != nil {
		return plain, false, false, err
	}
	answer, err := detectRenames(in, &object.DiffTreeOptions{DetectRenames: true, RenameScore: 60})
	if ctx.Err() != nil {
		return plain, false, false, ctx.Err()
	}
	if err != nil || !accounts(in, answer) {
		return plain, true, true, nil
	}
	return answer, true, false, nil
}

// fromMemory is a copy of each change of plain whose sides' trees are one
// empty tree of mem, so that go-git's search reads every file it compares
// from mem, by the id the change records; a file mem does not hold fails
// the search, as does every read once mem's context has ended.
func fromMemory(mem inMemory, plain object.Changes) (object.Changes, error) {
	o := mem.NewEncodedObject()
	if err := (&object.Tree{}).Encode(o); err != nil {
		return nil, err
	}
	tree, err := object.DecodeTree(mem, o)
	if err != nil {
		return nil, err
	}
	in := make(object.Changes, len(plain))
	for i, ch := range plain {
		c := *ch
		for _, e := range []*object.ChangeEntry{&c.From, &c.To} {
			if e.Name != "" {
				e.Tree = tree
			}
		}
		in[i] = &c
	}
	return in, nil
}

// inMemory is the objects the search for renames reads (withRenames),
// each stored under the id of its bytes, none once ctx has ended.
type inMemory struct {
	*memory.ObjectStorage
	ctx context.Context
}

func (s inMemory) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if o, ok := s.Objects[h]; ok && (t == plumbing.AnyObject || o.Type() == t) {
		return o, nil
	}
	return nil, plumbing.ErrObjectNotFound
}

// detectRenames is go-git's search for renames (withRenames); a test sets
// it.
var detectRenames = object.DetectRenames

// accounts reports whether answer, go-git's search over plain, holds every
// path of plain once: each file plain adds as added or as where a rename
// went, each it deletes as deleted or as where a rename came from, and
// every other entry as plain has it.
func accounts(plain, answer object.Changes) bool {
	left := map[[2]string]object.Change{}
	for _, ch := range plain {
		left[[2]string{ch.From.Name, ch.To.Name}] = *ch
	}
	take := func(k [2]string, want object.Change) bool {
		got, ok := left[k]
		delete(left, k)
		return ok && got == want
	}
	for _, ch := range answer {
		if ch.From.Name == "" || ch.To.Name == "" || ch.From.Name == ch.To.Name {
			if !take([2]string{ch.From.Name, ch.To.Name}, *ch) {
				return false
			}
		} else if !take([2]string{"", ch.To.Name}, object.Change{To: ch.To}) || !take([2]string{ch.From.Name, ""}, object.Change{From: ch.From}) {
			return false
		}
	}
	return len(left) == 0
}

// renamesCut reports whether a file listed as deleted may have moved: plain
// adds a file and deletes one, and renames were not looked for, or the
// search's answer was not taken (withRenames), or changes, the files
// listed, leave one content (a blob id) both deleted and added, or leave a
// file that is not an ordinary one (an executable, a link) deleted while
// another such file is added. go-git's search pairs files of one content
// only by their names and modes, and other files by their content only
// when both are ordinary ones.
func renamesCut(plain, changes object.Changes, looked, dropped bool) bool {
	if !slices.ContainsFunc(plain, func(ch *object.Change) bool { return ch.From.Name == "" }) ||
		!slices.ContainsFunc(plain, func(ch *object.Change) bool { return ch.To.Name == "" }) {
		return false
	}
	if !looked || dropped {
		return true
	}
	deletedAs, other := map[plumbing.Hash]bool{}, false
	for _, ch := range changes {
		if ch.To.Name == "" {
			deletedAs[ch.From.TreeEntry.Hash] = true
			other = other || ch.From.TreeEntry.Mode != filemode.Regular
		}
	}
	return slices.ContainsFunc(changes, func(ch *object.Change) bool {
		return ch.From.Name == "" && (deletedAs[ch.To.TreeEntry.Hash] || (other && ch.To.TreeEntry.Mode != filemode.Regular))
	})
}

// lineDiff is the lines added and removed between the files whose bytes
// are from and to (nil for none), counted as go-git's patch counts them
// (Change.PatchContext): binary, with no lines, when a side is binary as
// git judges it (binary) or the diff has nothing to compare. The lines the two sides share at their
// start, and then at their end, are set aside first (setAside), as the
// diff sets them aside itself, so they add nothing to either count. The
// diff, go-git's own (diffLines), is given what is left when neither side
// has more than MaxDiffLines lines of it, and the less of MaxFileDiff and
// left, what is left of the comparison's time, which it spends; a file
// past MaxDiffLines, and one whose diff took its time, finished or not,
// is listed without its lines. Equal texts, and two empty ones, are
// judged whole, before anything is set aside.
func lineDiff(from, to []byte, left *time.Duration) (added, removed int, isBinary bool) {
	if binary(from) || binary(to) {
		return 0, 0, true
	}
	return counts(string(from), string(to), left)
}

// counts is lineDiff's counts of the texts a and b: a side with no line
// left once the shared lines are set aside is counted without the diff.
func counts(a, b string, left *time.Duration) (added, removed int, binary bool) {
	limit := min(MaxFileDiff, *left)
	if limit <= 0 {
		return 0, 0, true
	}
	if a == b {
		return 0, 0, a == ""
	}
	a, b = setAside(a, b)
	if a == "" || b == "" {
		return lineCount(b), lineCount(a), false
	}
	if lineCount(a) > MaxDiffLines || lineCount(b) > MaxDiffLines {
		return 0, 0, true
	}
	start := clock()
	diffs := diffLines(a, b, limit)
	took := clock().Sub(start)
	*left -= took
	if took >= limit || len(diffs) == 0 {
		return 0, 0, true
	}
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			added += lineCount(d.Text)
		case diffmatchpatch.DiffDelete:
			removed += lineCount(d.Text)
		}
	}
	return added, removed, false
}

// diffLines is the line diff counts hands what it compares to: go-git's
// own (diff.DoWithTimeout); a test sets it.
var diffLines = godiff.DoWithTimeout

// setAside is a and b without the whole lines they share at their start
// and then, of what is left, without those they share at their end: what
// the line diff itself sets aside, in that order, before it compares.
func setAside(a, b string) (string, string) {
	for {
		end := strings.IndexByte(a, '\n') + 1
		if end == 0 || !strings.HasPrefix(b, a[:end]) {
			break
		}
		a, b = a[end:], b[end:]
	}
	for a != "" && b != "" {
		line := a[strings.LastIndexByte(a[:len(a)-1], '\n')+1:]
		rest := len(b) - len(line)
		if !strings.HasSuffix(b, line) || (rest > 0 && b[rest-1] != '\n') {
			break
		}
		a, b = a[:len(a)-len(line)], b[:rest]
	}
	return a, b
}

func summary(c *object.Commit, withBody bool) Commit {
	subject, body, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	out := Commit{Sha: c.Hash.String(), Short: c.Hash.String()[:shortLen], Subject: strings.TrimSpace(subject), Author: c.Author.Name, Email: c.Author.Email, At: c.Committer.When.UTC(), Parents: c.NumParents()}
	if len(c.ParentHashes) > 0 {
		out.Parent = c.ParentHashes[0].String()
	}
	if withBody {
		body = strings.TrimSpace(body)
		if len(body) > MaxBody {
			cut := MaxBody
			for cut > 0 && body[cut]&0xC0 == 0x80 {
				cut--
			}
			body = body[:cut]
		}
		out.Body = body
	}
	return out
}

// lineCount counts the lines of a chunk: its newlines, and a last line
// without one.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
