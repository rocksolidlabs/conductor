// Package gitrepo reads a repository's history with go-git (design round
// 12, F5): the commits on the checked-out branch since a time or a base,
// and one commit's files with their added and removed lines against its
// first parent. No git binary is needed for them; a crew's linked worktree
// opens through its common directory. FileAt finds a file's blob with
// go-git and reads its size and head through the git binary (blobAt says
// why). The working tree's status
// and a file at a revision stay with internal/gitcli, which the git binary
// does faster.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// Bounds of what one read returns.
const (
	MaxCommits = 200     // commits in a log
	MaxScan    = 5000    // commits walked to find them
	MaxFiles   = 500     // files of one commit
	MaxBody    = 4096    // bytes of a commit message's body
	MaxDiffed  = 1 << 20 // a file larger than this is listed without its lines
	shortLen   = 7
)

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
}

// open is the repository at dir or the nearest folder above it, a crew's
// linked worktree through its common directory, as go-git's
// PlainOpenWithOptions finds it with DetectDotGit and
// EnableDotGitCommonDir (gitDirs, commonDir), every file of it opened
// through careOS: the one way the package opens a repository. ErrNotRepo
// when there is none; ErrUnreadable (wrapped) when one of its files was
// refused, whatever go-git made of the refusal; seen keeps those of the
// reads that follow.
func open(dir string) (r *git.Repository, top string, seen *refusal, err error) {
	seen = &refusal{}
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
	st := filesystem.NewStorageWithOptions(files, cache.NewObjectLRUDefault(), filesystem.Options{LargeObjectThreshold: largeObject})
	r, err := git.Open(st, wt)
	if err != nil {
		return nil, "", err
	}
	// The small files go-git's opening has not read (it read HEAD and
	// config) and a read may, go-git's or the git binary's (FileAt): each
	// opened once through careOS, which notes one refused. Any other error
	// is the read's to meet, as before.
	for _, name := range []string{"packed-refs", "shallow", filepath.Join("objects", "info", "alternates")} {
		if f, err := files.Open(name); err == nil {
			f.Close()
		}
	}
	top := dir
	if w, err := r.Worktree(); err == nil {
		top = w.Filesystem.Root()
	}
	return r, top, nil
}

// read is f over the repository at dir, opened for it, and f once more
// over a second opening when the first finds an object, a commit or a file
// gone: go-git lists a repository's packs once, at its first read from one,
// and git repacks a repository by itself meanwhile (every commit may start
// `git maintenance run --auto`, which from Git 2.5x on packs a few hundred
// loose objects and deletes them, and drops packs it merged), so a read
// begun before the repack looks for an object in a pack it never listed or
// in a pack since removed. The second opening lists them again. A commit
// or a file that truly is not there is looked for twice. A file refused
// on the way (careOS) is the answer, whatever f made of it.
func read[T any](dir string, f func(r *git.Repository, top string) (T, error)) (T, error) {
	var none T
	r, top, seen, err := open(dir)
	if err != nil {
		return none, err
	}
	out, err := f(r, top)
	seen.close()
	if refused := seen.first(); refused != nil {
		return none, refused
	}
	if !gone(err) {
		return out, err
	}
	if r, top, seen, err = open(dir); err != nil {
		return out, err
	}
	out, err = f(r, top)
	if seen.close(); seen.first() != nil {
		return none, seen.first()
	}
	return out, err
}

// gone reports whether err says an object was not found where go-git
// looked for it (read).
func gone(err error) bool {
	return errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, dotgit.ErrPackfileNotFound) ||
		errors.Is(err, ErrNoCommit) || errors.Is(err, ErrNoFile)
}

// largeObject is the size past which an object is read as a stream rather
// than whole (a commit's and a comparison's files, diffTrees). FileAt
// reads no blob through go-git (blobAt).
const largeObject = 256 << 10

// Commits lists the commits on HEAD newest first: those after base's merge
// base with HEAD when base names a revision, else those committed at or
// after since. A repository with no commit yet has none.
func Commits(ctx context.Context, dir, base string, since time.Time) (Log, error) {
	return read(dir, func(r *git.Repository, top string) (Log, error) { return commits(ctx, r, top, base, since) })
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
	return read(dir, func(r *git.Repository, top string) (Detail, error) { return commitDetail(ctx, r, top, rev) })
}

func commitDetail(ctx context.Context, r *git.Repository, top, rev string) (Detail, error) {
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
	return diffTrees(ctx, out, parentTree, tree)
}

// Compare is the files that differ between the trees of the commits from
// and to, both full object ids read by id (no revision is resolved, so no
// reference names another commit), renames found, each with its added and
// removed lines, as CommitDetail answers for one commit against its
// parent; Commit is to's. At most MaxFiles files, Truncated past them; a
// file past MaxDiffed is listed without its lines. ErrNoCommit (wrapped)
// for an id the repository has no commit of.
func Compare(ctx context.Context, dir, from, to string) (Detail, error) {
	return read(dir, func(r *git.Repository, top string) (Detail, error) { return compare(ctx, r, top, from, to) })
}

func compare(ctx context.Context, r *git.Repository, top, from, to string) (Detail, error) {
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
	return diffTrees(ctx, out, fromTree, toTree)
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
// however large the file and however it is stored (blobAt). A link is its
// target's text, as git keeps it, never followed. ErrNoCommit (wrapped)
// for an id the repository has no commit of, ErrNoFile for a path the
// commit has no file at.
func FileAt(ctx context.Context, dir, id, path string, limit int) (File, error) {
	return read(dir, func(r *git.Repository, _ string) (File, error) { return fileAt(ctx, r, dir, id, path, limit) })
}

func fileAt(ctx context.Context, r *git.Repository, dir, id, path string, limit int) (File, error) {
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
	if err := r.Storer.HasEncodedObject(e.Hash); errors.Is(err, plumbing.ErrObjectNotFound) {
		return File{}, ErrNoFile
	} else if err != nil {
		return File{}, err
	}
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	// The size, then the object read once, at most what the reply and
	// git's binary sniff need of it (blobAt).
	size, err := blobSize(ctx, dir, e.Hash)
	if err != nil {
		return File{}, err
	}
	out := File{Size: size}
	b, err := blobAt(ctx, dir, e.Hash, min(max(int64(limit)+1, sniffLen), size))
	if err != nil {
		return File{}, err
	}
	if bytes.IndexByte(b[:min(len(b), sniffLen)], 0) >= 0 {
		out.Binary = true
		return out, nil
	}
	if len(b) > limit {
		b, out.Truncated = b[:limit], true
	}
	out.Content = b
	return out, nil
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

// blobSize is the size of the blob h as git records it (git cat-file -s,
// through gitcli, replacements off): git, a process of its own, works the
// size out of a delta, which go-git does by inflating the delta whole in
// this process (entryAt).
func blobSize(ctx context.Context, dir string, h plumbing.Hash) (int64, error) {
	out, err := gitcli.RunEnv(ctx, dir, noReplace, "cat-file", "-s", h.String())
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("git cat-file -s %s: %q is not a size", h, out)
	}
	return n, nil
}

// blobAt is the first n bytes of the blob h (n at most its size, from
// blobSize), read by the git binary (git cat-file blob, its id in full,
// through gitcli), its output stopped once n bytes are in: git, a process
// of its own that ends with the read, resolves a delta and its base,
// whatever their sizes, with replacements off (noReplace). This process
// holds the n bytes and the pipe's buffers, never the blob, its delta or
// its base: go-git reads a delta's base whole (a packed blob stored as a
// delta of a 32 MiB one, 165 MiB allocated for its first MiB, measured
// with go-git v5.19.3), and a delta's instructions whole for its size,
// whatever the blob's size.
func blobAt(ctx context.Context, dir string, h plumbing.Hash, n int64) ([]byte, error) {
	w := &headWriter{b: make([]byte, 0, n)}
	if n == 0 {
		return w.b, nil
	}
	err := gitcli.RunToEnv(ctx, dir, noReplace, w, "cat-file", "blob", h.String())
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil && !w.full() {
		return nil, err
	}
	return w.b, nil
}

// noReplace turns off git's replacement objects (refs/replace) for the
// read: the blob read is the one the tree records, as go-git reads it
// (go-git knows no replacements), never one put in its place.
var noReplace = map[string]string{"GIT_NO_REPLACE_OBJECTS": "1"}

// headWriter keeps what is written to it up to its buffer's capacity and
// then refuses more (errHeadFull), which ends the git writing to it.
type headWriter struct{ b []byte }

var errHeadFull = errors.New("the head is read")

func (w *headWriter) full() bool { return len(w.b) == cap(w.b) }

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
// for none: every file added) and to, renames found, each with its counts
// (CommitDetail's and Compare's walk).
func diffTrees(ctx context.Context, out Detail, from, to *object.Tree) (Detail, error) {
	changes, err := object.DiffTreeWithOptions(ctx, from, to, object.DefaultDiffTreeOptions)
	if err != nil {
		return out, err
	}
	if len(changes) > MaxFiles {
		changes, out.Truncated = changes[:MaxFiles], true
	}
	for _, ch := range changes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
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
		from, to, err := ch.Files()
		if err != nil {
			return out, err
		}
		if (from != nil && from.Size > MaxDiffed) || (to != nil && to.Size > MaxDiffed) {
			fc.Binary = true // too large to count; shown without its lines
		} else if p, err := ch.PatchContext(ctx); err == nil {
			for _, fp := range p.FilePatches() {
				if fp.IsBinary() {
					fc.Binary = true
					continue
				}
				for _, chunk := range fp.Chunks() {
					n := lineCount(chunk.Content())
					switch chunk.Type() {
					case diff.Add:
						fc.Added += n
					case diff.Delete:
						fc.Removed += n
					}
				}
			}
		} else {
			return out, err
		}
		out.Added += fc.Added
		out.Removed += fc.Removed
		out.Changes = append(out.Changes, fc)
	}
	return out, nil
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
