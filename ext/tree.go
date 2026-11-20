package ext

import (
	"strings"
	"time"
)

// What a context provider may read of a run's repository through its
// Host (docs/plans/context-seams.md, CT-1): committed objects alone, by
// full object id, never a ref, a revision expression or a working tree's
// file; under the server's safe git, with replacement objects off; a path
// the server's deny list refuses left out of a listing and refused at a
// read, the list asked at each call. Only the Host of a provider in the
// context slot reads; every other Host answers ErrUnsupported.

// TreeRequest asks for the committed tree of Commit in the repository of
// the run Run; with Roots, the history's root commits too.
type TreeRequest struct {
	Run    string `json:"run"`
	Commit string `json:"commit"` // a full commit id, never a ref or an expression
	Roots  bool   `json:"roots,omitempty"`
}

// Tree is a commit's tree, listed recursively: its files, links and
// submodule entries by full path, in git's order.
type Tree struct {
	Commit string `json:"commit"`
	ID     string `json:"id"` // the root tree's object id
	// Roots is the history's root commits, sorted, at most MaxTreeRoots.
	// The server reads 4 KiB of git's list of them (about a hundred ids):
	// past it, the roots are the first MaxTreeRoots, sorted, of those it
	// read, which need not be the smallest of all of them.
	Roots     []string    `json:"roots,omitempty"`
	Shallow   bool        `json:"shallow,omitempty"`
	Entries   []TreeEntry `json:"entries"`
	Denied    int         `json:"denied,omitempty"` // entries the deny list refused, left out
	Truncated bool        `json:"truncated,omitempty"`
}

// TreeEntry is one entry of a tree.
type TreeEntry struct {
	Path   string `json:"path"`   // slash-separated from the top, at most MaxTreePath bytes
	Mode   string `json:"mode"`   // 100644 | 100755 | 120000 | 160000
	Kind   string `json:"kind"`   // file | link | submodule
	Object string `json:"object"` // the blob's id; a submodule's commit id
	Size   int64  `json:"size"`   // bytes; -1 for a submodule
}

// The kinds of a tree entry.
const (
	EntryFile      = "file"
	EntryLink      = "link"
	EntrySubmodule = "submodule"
)

// BlobRequest asks for the bytes of the file Path holds at Commit in the
// repository of the run Run.
type BlobRequest struct {
	Run    string `json:"run"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

// DeniedRequest asks whether the server's deny list refuses each of Paths
// in the repository of the run Run.
type DeniedRequest struct {
	Run   string   `json:"run"`
	Paths []string `json:"paths"` // at most MaxDeniedPaths
}

const (
	MaxTreeEntries = 100_000
	MaxTreeBytes   = 64 << 20 // git's listing, as read
	MaxTreePath    = 4096
	MaxTreeRoots   = 16
	MaxBlob        = 1 << 20
	MaxDeniedPaths = 1000
	TreeWait       = 60 * time.Second
)

// Valid checks r: a run id and a full commit id (40 or 64 lower-case hex):
// a ref, HEAD, an abbreviated id, an id in upper case or any revision
// expression is refused.
func (r TreeRequest) Valid() error {
	const rec = "tree request"
	switch {
	case !runID(r.Run):
		return invalid(rec, "run %q is not a run id", r.Run)
	case !commitID(r.Commit):
		return invalid(rec, "commit %q is not a full commit id", r.Commit)
	}
	return nil
}

// Valid checks r: a run id, a full commit id and a tree path (TreePathOK).
func (r BlobRequest) Valid() error {
	const rec = "blob request"
	switch {
	case !runID(r.Run):
		return invalid(rec, "run %q is not a run id", r.Run)
	case !commitID(r.Commit):
		return invalid(rec, "commit %q is not a full commit id", r.Commit)
	case !TreePathOK(r.Path):
		return invalid(rec, "path %q is not a path inside a tree", r.Path)
	}
	return nil
}

// Valid checks r: a run id and at most MaxDeniedPaths tree paths
// (TreePathOK).
func (r DeniedRequest) Valid() error {
	const rec = "denied request"
	switch {
	case !runID(r.Run):
		return invalid(rec, "run %q is not a run id", r.Run)
	case len(r.Paths) > MaxDeniedPaths:
		return invalid(rec, "%d paths, past %d", len(r.Paths), MaxDeniedPaths)
	}
	for i, p := range r.Paths {
		if !TreePathOK(p) {
			return invalid(rec, "path %d %q is not a path inside a tree", i, p)
		}
	}
	return nil
}

// TreePathOK reports whether p is a path inside a tree: 1 to MaxTreePath
// bytes, slash-separated from the top, with no NUL, no leading '/' or ':'
// (git's pathspec magic, ":(top)", ":/", is refused before git sees it,
// as well as read literally) and no empty, "." or ".." part.
func TreePathOK(p string) bool {
	if p == "" || len(p) > MaxTreePath || strings.IndexByte(p, 0) >= 0 || p[0] == '/' || p[0] == ':' {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
