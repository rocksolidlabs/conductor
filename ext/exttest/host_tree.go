package exttest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

// fakeTrees is what a FakeHost holds of the repositories a context
// provider reads: a file system per run and commit, the deny list, and
// every read made.
type fakeTrees struct {
	mu    sync.Mutex
	trees map[string]fs.FS // run + "\x00" + commit
	runs  map[string]bool
	deny  func(path string) bool
	reads []TreeRead
}

// TreeRead is one read a provider made through a FakeHost: the method
// (ReadTree, ReadBlob or Denied), the run, the commit ("" for Denied) and
// the paths asked about (a blob's one, Denied's every one).
type TreeRead struct {
	Method string
	Run    string
	Commit string
	Paths  []string
}

// SetTree has the commit's tree in the run's repository be fsys: its
// regular files (100755 for one with an execute bit, 100644 otherwise)
// and its symbolic links (fs.ReadLink answers a link's target), as
// ReadTree lists them and ReadBlob reads them. A run with no tree set is
// one the server does not hold (ext.ErrNotFound).
func (h *FakeHost) SetTree(run, commit string, fsys fs.FS) {
	t := &h.trees
	t.mu.Lock()
	if t.trees == nil {
		t.trees, t.runs = map[string]fs.FS{}, map[string]bool{}
	}
	t.trees[run+"\x00"+commit] = fsys
	t.runs[run] = true
	t.mu.Unlock()
	h.tell()
}

// SetDenied has the deny list refuse each path (slash-separated from the
// repository's top) deny answers true for, from the next call on: as the
// server's list, asked again at each call.
func (h *FakeHost) SetDenied(deny func(path string) bool) {
	t := &h.trees
	t.mu.Lock()
	t.deny = deny
	t.mu.Unlock()
	h.tell()
}

// Reads answers every read made so far, in order.
func (h *FakeHost) Reads() []TreeRead {
	t := &h.trees
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TreeRead, len(t.reads))
	for i, r := range t.reads {
		r.Paths = slices.Clone(r.Paths)
		out[i] = r
	}
	return out
}

// read records r and answers the tree of its run and commit, whether the
// run is held, and the deny list now.
func (t *fakeTrees) read(r TreeRead) (fsys fs.FS, run bool, deny func(string) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r.Paths = slices.Clone(r.Paths)
	t.reads = append(t.reads, r)
	deny = t.deny
	if deny == nil {
		deny = func(string) bool { return false }
	}
	return t.trees[r.Run+"\x00"+r.Commit], t.runs[r.Run], deny
}

func fakeRefused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ext.ErrRefused}, args...)...)
}

func fakeNotFound(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ext.ErrNotFound}, args...)...)
}

// blobID is the id git gives a blob of b (SHA-1).
func blobID(b []byte) string {
	sum := sha1.Sum(append(fmt.Appendf(nil, "blob %d\x00", len(b)), b...))
	return hex.EncodeToString(sum[:])
}

// ReadTree lists the tree SetTree set for r's run and commit, as the
// server's Host does: a request ext.TreeRequest.Valid refuses is
// ext.ErrRefused; a run or a commit with no tree set ext.ErrNotFound; a
// path the deny list refuses counted in Denied and left out; at most
// ext.MaxTreeEntries entries (Truncated past them); with Roots, the
// commit itself as its history's one root.
func (h *FakeHost) ReadTree(ctx context.Context, r ext.TreeRequest) (ext.Tree, error) {
	if err := h.enter(ctx); err != nil {
		return ext.Tree{}, err
	}
	fsys, run, deny := h.trees.read(TreeRead{Method: "ReadTree", Run: r.Run, Commit: r.Commit})
	if err := r.Valid(); err != nil {
		return ext.Tree{}, fakeRefused("%v", err)
	}
	if !run {
		return ext.Tree{}, fakeNotFound("no such run")
	}
	if fsys == nil {
		return ext.Tree{}, fakeNotFound("the run's repository holds no commit %s", r.Commit)
	}
	t := ext.Tree{Commit: r.Commit, Entries: []ext.TreeEntry{}}
	all := sha1.New()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		e, err := fakeEntry(fsys, p, d)
		if err != nil {
			return err
		}
		if deny(p) {
			t.Denied++
			return nil
		}
		if len(t.Entries) == ext.MaxTreeEntries {
			t.Truncated = true
			return fs.SkipAll
		}
		fmt.Fprintf(all, "%s %s\x00", e.Path, e.Object)
		t.Entries = append(t.Entries, e)
		return nil
	})
	if err != nil {
		return ext.Tree{}, fakeNotFound("the commit's tree cannot be read")
	}
	t.ID = hex.EncodeToString(all.Sum(nil))
	if r.Roots {
		t.Roots = []string{r.Commit}
	}
	return t, nil
}

// fakeEntry is the tree entry of the file p of fsys.
func fakeEntry(fsys fs.FS, p string, d fs.DirEntry) (ext.TreeEntry, error) {
	if d.Type()&fs.ModeSymlink != 0 {
		target, err := fs.ReadLink(fsys, p)
		if err != nil {
			return ext.TreeEntry{}, err
		}
		return ext.TreeEntry{Path: p, Mode: "120000", Kind: ext.EntryLink, Object: blobID([]byte(target)), Size: int64(len(target))}, nil
	}
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return ext.TreeEntry{}, err
	}
	info, err := d.Info()
	if err != nil {
		return ext.TreeEntry{}, err
	}
	mode := "100644"
	if info.Mode().Perm()&0o111 != 0 {
		mode = "100755"
	}
	return ext.TreeEntry{Path: p, Mode: mode, Kind: ext.EntryFile, Object: blobID(b), Size: int64(len(b))}, nil
}

// ReadBlob answers the file r.Path of the tree SetTree set, as the
// server's Host does: a request ext.BlobRequest.Valid refuses, a path the
// deny list refuses and a file past ext.MaxBlob are ext.ErrRefused; a run
// or a commit with no tree set, and a path with no file,
// ext.ErrNotFound; a link answers its target.
func (h *FakeHost) ReadBlob(ctx context.Context, r ext.BlobRequest) ([]byte, error) {
	if err := h.enter(ctx); err != nil {
		return nil, err
	}
	fsys, run, deny := h.trees.read(TreeRead{Method: "ReadBlob", Run: r.Run, Commit: r.Commit, Paths: []string{r.Path}})
	if err := r.Valid(); err != nil {
		return nil, fakeRefused("%v", err)
	}
	if !run {
		return nil, fakeNotFound("no such run")
	}
	if deny(r.Path) {
		return nil, fakeRefused("the path is one the server's deny list refuses")
	}
	if fsys == nil {
		return nil, fakeNotFound("the run's repository holds no commit %s", r.Commit)
	}
	info, err := fs.Lstat(fsys, r.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist) || err == nil && info.IsDir():
		return nil, fakeNotFound("the commit has no file at the path")
	case err != nil:
		return nil, err
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := fs.ReadLink(fsys, r.Path)
		if err != nil {
			return nil, err
		}
		return []byte(target), nil
	case info.Size() > ext.MaxBlob:
		return nil, fakeRefused("the file is too large: %d bytes, past %d", info.Size(), ext.MaxBlob)
	}
	return fs.ReadFile(fsys, r.Path)
}

// Denied answers whether the deny list refuses each of r.Paths, as the
// server's Host does: a request ext.DeniedRequest.Valid refuses is
// ext.ErrRefused, a run with no tree set ext.ErrNotFound.
func (h *FakeHost) Denied(ctx context.Context, r ext.DeniedRequest) ([]bool, error) {
	if err := h.enter(ctx); err != nil {
		return nil, err
	}
	_, run, deny := h.trees.read(TreeRead{Method: "Denied", Run: r.Run, Paths: r.Paths})
	if err := r.Valid(); err != nil {
		return nil, fakeRefused("%v", err)
	}
	if !run {
		return nil, fakeNotFound("no such run")
	}
	out := make([]bool, len(r.Paths))
	for i, p := range r.Paths {
		out[i] = deny(p)
	}
	return out, nil
}

// Tree checks a context provider that reads its run's repository through
// its Host (it must be an ext.Starter): started with a FakeHost that holds
// fsys as the tree of q.Start (and of q.Base, when the query names one) in
// q.Run, its deny list refusing each path deny answers true for, then
// asked Context for q with the network open. Every read it made names
// q.Run and a commit the query names (its start, its base, an input's
// commit or start), no file it read is one the deny list refuses, and its
// blocks are valid: its reads of the fake tree stay inside it.
func Tree(t T, n *Network, p ext.ContextProvider, q ext.ContextQuery, fsys fs.FS, deny func(path string) bool) {
	t.Helper()
	c, ok := begin(t, n, "Tree", p)
	if !ok {
		return
	}
	defer c.done()
	s, ok := p.(ext.Starter)
	if !ok {
		c.errorf("the provider is no ext.Starter: it has no Host to read its repository through")
		return
	}
	h := NewFakeHost(t)
	commits := map[string]bool{}
	for _, cm := range []string{q.Start, q.Base} {
		if cm != "" {
			commits[cm] = true
			h.SetTree(q.Run, cm, fsys)
		}
	}
	for _, in := range q.Inputs {
		for _, cm := range []string{in.Commit, in.Start} {
			if cm != "" {
				commits[cm] = true
			}
		}
	}
	if deny != nil {
		h.SetDenied(deny)
	}
	life, end := context.WithCancel(context.Background())
	defer func() {
		end()
		h.Close()
	}()
	if err, ok := run(c, "Start", ext.StartWait, open, func(context.Context) error { return s.Start(life, h) }); !ok || err != nil {
		if ok {
			c.errorf("Start failed: %v", err)
		}
		c.flush()
		return
	}
	bs, returned := run(c, "Context", ext.ContextWait, open, func(ctx context.Context) []ext.ContextBlock {
		bs, _ := p.Context(ctx, q)
		return bs
	})
	if returned {
		for i, b := range bs {
			if err := b.Valid(); err != nil {
				c.found("Context's block %d is not valid: %v", i, err)
			}
		}
	}
	for _, r := range h.Reads() {
		switch {
		case r.Run != q.Run:
			c.found("%s read the repository of run %q, not the query's %q", r.Method, r.Run, q.Run)
		case r.Method != "Denied" && !commits[r.Commit]:
			c.found("%s read commit %q, which the query does not name", r.Method, r.Commit)
		case r.Method == "ReadBlob" && deny != nil && len(r.Paths) == 1 && deny(r.Paths[0]):
			c.found("ReadBlob asked for %q, a path the deny list refuses (its listing left it out)", r.Paths[0])
		}
	}
}
