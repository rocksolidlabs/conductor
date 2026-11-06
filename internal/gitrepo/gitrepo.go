// Package gitrepo reads a repository's history with go-git (design round
// 12, F5): the commits on the checked-out branch since a time or a base,
// and one commit's files with their added and removed lines against its
// first parent. No git binary is needed; a crew's linked worktree opens
// through its common directory. The working tree's status and a file at a
// revision stay with internal/gitcli, which the git binary does faster.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
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

func open(dir string) (*git.Repository, string, error) {
	r, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, "", ErrNotRepo
		}
		return nil, "", err
	}
	top := dir
	if w, err := r.Worktree(); err == nil {
		top = w.Filesystem.Root()
	}
	return r, top, nil
}

// Commits lists the commits on HEAD newest first: those after base's merge
// base with HEAD when base names a revision, else those committed at or
// after since. A repository with no commit yet has none.
func Commits(ctx context.Context, dir, base string, since time.Time) (Log, error) {
	r, top, err := open(dir)
	if err != nil {
		return Log{}, err
	}
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
	r, top, err := open(dir)
	if err != nil {
		return Detail{}, err
	}
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
	r, top, err := open(dir)
	if err != nil {
		return Detail{}, err
	}
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
// most max bytes of it. A link is its target's text, as git keeps it,
// never followed. ErrNoCommit (wrapped) for an id the repository has no
// commit of, ErrNoFile for a path the commit has no file at.
func FileAt(ctx context.Context, dir, id, path string, max int) (File, error) {
	r, _, err := open(dir)
	if err != nil {
		return File{}, err
	}
	c, err := commitByID(r, id)
	if err != nil {
		return File{}, err
	}
	tree, err := c.Tree()
	if err != nil {
		return File{}, err
	}
	f, err := tree.File(path)
	if errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, plumbing.ErrObjectNotFound) {
		return File{}, ErrNoFile
	}
	if err != nil {
		return File{}, err
	}
	if !f.Mode.IsFile() {
		return File{}, ErrNoFile
	}
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	out := File{Size: f.Size}
	if out.Binary, err = f.IsBinary(); err != nil {
		return File{}, err
	}
	if out.Binary {
		return out, nil
	}
	rd, err := f.Reader()
	if err != nil {
		return File{}, err
	}
	defer rd.Close()
	b, err := io.ReadAll(io.LimitReader(rd, int64(max)+1))
	if err != nil {
		return File{}, err
	}
	if len(b) > max {
		b, out.Truncated = b[:max], true
	}
	out.Content = b
	return out, nil
}

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
	return c, err
}

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
