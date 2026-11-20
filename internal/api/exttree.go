package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A context provider's reads of a run's repository (docs/plans/
// context-seams.md, CT-1): ext.Host's ReadTree, ReadBlob and Denied, on
// the Host of a provider in the context slot alone (every other Host,
// Brakeman's desk's among them, answers ext.ErrUnsupported). They read
// committed objects by their full ids and nothing else: no ref, no
// revision expression, no file of a working tree, the run's repository
// found as the diff route finds it (the crew's working directory as
// launched, through the allowed roots). git runs only through
// internal/gitcli's safe configuration (no program of the repository's),
// with replacement objects off and literal pathspecs, every output read
// within its bound. A path the server's deny list refuses, as it stands
// at the call, or one outside every allowed root, is left out of a
// listing and refused at a read. Nothing a read answers is logged.

// treeReadEnv is set in the environment of every git the reads run: the
// objects as stored, never one refs/replace puts in their place, and a
// path asked about as it is spelt, never as pathspec magic.
var treeReadEnv = map[string]string{"GIT_NO_REPLACE_OBJECTS": "1", "GIT_LITERAL_PATHSPECS": "1"}

// readSubs and readWords are the subcommands the reads run and the words
// they give them besides object ids and paths.
var (
	readSubs  = map[string]bool{"rev-parse": true, "ls-tree": true, "rev-list": true, "cat-file": true}
	readWords = map[string]bool{"--show-toplevel": true, "--is-shallow-repository": true, "-r": true, "-z": true, "--long": true,
		"--full-tree": true, "--max-parents=0": true, "blob": true}
)

// The one way the reads run git: under internal/gitcli's safe
// configuration with treeReadEnv (no replacement object, literal
// pathspecs), through the three runs that take an environment, its args
// the subcommand first (of readSubs), words of readWords, full object ids
// (a commit's peeled with ^{commit} or ^{tree} at most) and, after "--",
// paths ext.TreePathOK takes: no global option, and any other argument
// refused before git runs (readArgs).

// treeEach hands git's NUL-separated output to each record by record,
// within out bytes (gitcli.RunEach).
func treeEach(ctx context.Context, dir string, out int64, each func([]byte) bool, args ...string) (bool, error) {
	if err := readArgs(args); err != nil {
		return false, err
	}
	return gitcli.RunEach(ctx, dir, treeReadEnv, gitcli.Limits{Out: out}, each, args...)
}

// treeSmall answers git's output whole, refusing one past limit bytes
// (gitcli.RunBoundedEnv).
func treeSmall(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	if err := readArgs(args); err != nil {
		return "", err
	}
	return gitcli.RunBoundedEnv(ctx, dir, treeReadEnv, limit, args...)
}

// treeBlob writes git's output to w, which bounds what it keeps
// (gitcli.RunToEnv).
func treeBlob(ctx context.Context, dir string, w io.Writer, args ...string) error {
	if err := readArgs(args); err != nil {
		return err
	}
	return gitcli.RunToEnv(ctx, dir, treeReadEnv, w, args...)
}

// readArgs refuses args the reads do not run.
func readArgs(args []string) error {
	if len(args) == 0 || !readSubs[args[0]] {
		return errors.New("git: not a read of committed objects")
	}
	paths := false
	for _, a := range args[1:] {
		switch {
		case paths:
			if !ext.TreePathOK(a) {
				return fmt.Errorf("git: %q is not a path inside a tree", a)
			}
		case a == "--":
			paths = true
		case readWords[a]:
		case gitObjectID(strings.TrimSuffix(strings.TrimSuffix(a, "^{commit}"), "^{tree}")):
		default:
			return fmt.Errorf("git: %q is not an object id", a)
		}
	}
	return nil
}

// The bounds of the reads' small git answers: a commit's facts, one
// entry's listing, the roots.
const (
	maxRevParse  = 4 << 10
	maxEntryList = ext.MaxTreePath + 4<<10
	maxRootsList = 4 << 10
	// maxRootsCached is how many commits' roots the server keeps, the
	// oldest dropped.
	maxRootsCached = 256
)

// extReading, when a test sets it, is told the method of every read once
// the run's repository is found, before git reads an object, and may hold
// it there; nil outside tests.
var extReading atomic.Pointer[func(method string)]

// rootsCache keeps the root commits of a repository's commits, by its top
// and the commit, at most maxRootsCached of them.
type rootsCache struct {
	mu    sync.Mutex
	m     map[string][]string
	order []string
}

func (c *rootsCache) get(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	return slices.Clone(r), ok
}

func (c *rootsCache) put(key string, roots []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]string{}
	}
	if _, ok := c.m[key]; ok {
		return
	}
	if len(c.order) == maxRootsCached {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
	c.m[key] = slices.Clone(roots)
	c.order = append(c.order, key)
}

// readsDoor answers whether h may read a repository: context.Canceled
// once its try has ended; ext.ErrUnsupported unless it is the Host of a
// provider in the context slot (never the desk's), and on a switchyard.
func (h *extHost) readsDoor() error {
	if err := h.gone(); err != nil {
		return err
	}
	if h.desk || h.name == "" || !h.s.holdsContextSlot(h.name) {
		return fmt.Errorf("%w: a repository is read by a context provider alone", ext.ErrUnsupported)
	}
	if h.s.cfg.Switchyard.Enabled {
		return fmt.Errorf("%w: this server coordinates hosted sessions and keeps no runs", ext.ErrUnsupported)
	}
	return nil
}

// holdsContextSlot reports whether the provider name is one of the
// server's context providers.
func (s *Server) holdsContextSlot(name string) bool {
	return slices.ContainsFunc(s.exts.context, func(g *guardContext) bool { return g.name == name })
}

// readCtx is ctx ending also with h's try (bound) and after ext.TreeWait.
func (h *extHost) readCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	bctx, end := h.bound(ctx)
	tctx, cancel := context.WithTimeout(bctx, ext.TreeWait)
	return tctx, func() { cancel(); end() }
}

// readErr is err as a read answers it: context.Canceled once h's try has
// ended, ctx's own error once it has.
func (h *extHost) readErr(ctx context.Context, err error) error {
	if h.gone() != nil {
		return context.Canceled
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// notFound is ext.ErrNotFound with the words why.
func notFound(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ext.ErrNotFound}, args...)...)
}

// repoTop is the top of the run's repository: the crew's working
// directory as the run was launched with it, through resolveCwd (still
// under an allowed root), and git's top of it.
func (h *extHost) repoTop(ctx context.Context, runID string) (string, error) {
	s := h.s
	if s.ledger == nil {
		return "", notFound("this server keeps no runs")
	}
	row, found, err := s.ledger.Run(ctx, runID)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("the run could not be read now")
	}
	if !found {
		return "", notFound("no such run")
	}
	// The crew's working directory as launched (crewCwd, the one reading
	// of the ledger's crew this needs).
	cwd := crewCwd(row)
	if cwd == "" {
		return "", notFound("the run's repository cannot be read here")
	}
	dir, err := s.resolveCwd(cwd)
	if err != nil {
		return "", notFound("the run's repository cannot be read here")
	}
	out, err := treeSmall(ctx, dir, maxRevParse, "rev-parse", "--show-toplevel")
	top := strings.TrimSuffix(out, "\n")
	if err != nil || top == "" || !filepath.IsAbs(top) || strings.Contains(top, "\n") {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", notFound("the run's repository cannot be read here")
	}
	return top, nil
}

// treeDenied is what the reads of the repository at top leave out and
// refuse: a path (slash-separated from top) the server's deny list
// refuses as it stands now, looked up once for the call, or one outside
// every allowed root.
func (s *Server) treeDenied(top string) func(path string) bool {
	var list []string
	if s.fileDeny != nil {
		list = s.fileDeny()
	}
	deny := session.DeniedByEach(list)
	var roots []string
	for _, r := range s.cfg.AllowedRoots {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			roots = append(roots, real)
		}
	}
	return func(path string) bool {
		abs := filepath.Join(top, filepath.FromSlash(path))
		if !slices.ContainsFunc(roots, func(r string) bool { return abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) }) {
			return true
		}
		return deny(abs)
	}
}

// commitFacts are what a read learns of its commit: whether the
// repository is shallow and the commit's root tree.
type commitFacts struct {
	shallow bool
	tree    string
}

// commitOf checks that commit names a commit of the repository at top,
// itself, and answers its facts; ext.ErrNotFound for anything else.
func commitOf(ctx context.Context, top, commit string) (commitFacts, error) {
	out, err := treeSmall(ctx, top, maxRevParse, "rev-parse", "--is-shallow-repository", commit+"^{commit}", commit+"^{tree}", "--")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if err != nil {
		if ctx.Err() != nil {
			return commitFacts{}, ctx.Err()
		}
		return commitFacts{}, notFound("the run's repository holds no commit %s", commit)
	}
	if len(lines) < 3 || lines[1] != commit || !gitObjectID(lines[2]) {
		return commitFacts{}, notFound("the run's repository holds no commit %s", commit)
	}
	return commitFacts{shallow: lines[0] == "true", tree: lines[2]}, nil
}

// gitObjectID reports whether s is a full object id: 40 or 64 lower-case
// hex.
func gitObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.IndexFunc(s, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') }) < 0
}

// readEntry parses one record of ls-tree -z --long: "<mode> <type>
// <object> <size>\t<path>". ok false for a record of another shape.
func readEntry(rec []byte) (e ext.TreeEntry, typ string, ok bool) {
	meta, path, found := strings.Cut(string(rec), "\t")
	f := strings.Fields(meta)
	if !found || len(f) != 4 || path == "" || !gitObjectID(f[2]) {
		return ext.TreeEntry{}, "", false
	}
	e = ext.TreeEntry{Path: path, Object: f[2], Size: -1}
	switch {
	case f[1] == "commit" && f[0] == "160000":
		e.Mode, e.Kind = "160000", ext.EntrySubmodule
	case f[1] == "blob" && f[0] == "120000":
		e.Mode, e.Kind = "120000", ext.EntryLink
	case f[1] == "blob" && f[0] == "100755":
		e.Mode, e.Kind = "100755", ext.EntryFile
	case f[1] == "blob":
		e.Mode, e.Kind = "100644", ext.EntryFile
	default:
		return e, f[1], true // a tree, when the listing is not recursive
	}
	if e.Kind != ext.EntrySubmodule {
		n, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil || n < 0 {
			return ext.TreeEntry{}, "", false
		}
		e.Size = n
	}
	return e, f[1], true
}

// ReadTree lists the committed tree of r.Commit in the run's repository
// (ext.Host): ls-tree's recursive listing read record by record, a path
// treeDenied refuses counted and left out, stopping at
// ext.MaxTreeEntries entries kept or ext.MaxTreeBytes read.
func (h *extHost) ReadTree(ctx context.Context, r ext.TreeRequest) (ext.Tree, error) {
	if err := h.readsDoor(); err != nil {
		return ext.Tree{}, err
	}
	if err := r.Valid(); err != nil {
		return ext.Tree{}, refused("%v", err)
	}
	rctx, end := h.readCtx(ctx)
	defer end()
	top, err := h.repoTop(rctx, r.Run)
	if err != nil {
		return ext.Tree{}, h.readErr(rctx, err)
	}
	if f := extReading.Load(); f != nil {
		(*f)("ReadTree")
	}
	facts, err := commitOf(rctx, top, r.Commit)
	if err != nil {
		return ext.Tree{}, h.readErr(rctx, err)
	}
	denied := h.s.treeDenied(top)
	t := ext.Tree{Commit: r.Commit, ID: facts.tree, Shallow: facts.shallow, Entries: []ext.TreeEntry{}}
	cut, err := treeEach(rctx, top, ext.MaxTreeBytes, func(rec []byte) bool {
		e, _, ok := readEntry(rec)
		if !ok || len(e.Path) > ext.MaxTreePath || e.Kind == "" {
			// An entry the listing cannot hand: said, never guessed at.
			t.Truncated = true
			return true
		}
		if denied(e.Path) {
			t.Denied++
			return true
		}
		if len(t.Entries) == ext.MaxTreeEntries {
			return false
		}
		t.Entries = append(t.Entries, e)
		return true
	}, "ls-tree", "-r", "-z", "--long", "--full-tree", r.Commit)
	if err != nil {
		return ext.Tree{}, h.readErr(rctx, notFound("the commit's tree cannot be read"))
	}
	t.Truncated = t.Truncated || cut
	if r.Roots {
		roots, err := h.s.rootsOf(rctx, top, r.Commit)
		if err != nil {
			return ext.Tree{}, h.readErr(rctx, err)
		}
		t.Roots = roots
	}
	if h.gone() != nil {
		return ext.Tree{}, context.Canceled
	}
	return t, nil
}

// rootsOf is the root commits of commit's history in the repository at
// top, sorted, at most ext.MaxTreeRoots, from the server's cache when it
// holds them. Past maxRootsList bytes of git's list (about a hundred
// roots), the roots are those of the complete lines read, sorted and cut:
// the list read is git's, cut at its bound, never a failure.
func (s *Server) rootsOf(ctx context.Context, top, commit string) ([]string, error) {
	key := top + "\x00" + commit
	if r, ok := s.treeRoots.get(key); ok {
		return r, nil
	}
	out, err := treeSmall(ctx, top, maxRootsList, "rev-list", "--max-parents=0", commit, "--")
	past := errors.Is(err, gitcli.ErrOutputLimit)
	if past {
		// A line the bound cut is no root: only the complete ones count.
		out = out[:strings.LastIndexByte(out, '\n')+1]
		err = nil
	}
	var roots []string
	for l := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		if gitObjectID(l) {
			roots = append(roots, l)
		}
	}
	if err != nil || (past && len(roots) == 0) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, notFound("the commit's history cannot be read")
	}
	slices.Sort(roots)
	roots = roots[:min(len(roots), ext.MaxTreeRoots)]
	s.treeRoots.put(key, roots)
	return roots, nil
}

// ReadBlob answers the bytes of the file r.Path holds at r.Commit in the
// run's repository (ext.Host): the path refused first when treeDenied
// refuses it; then its entry, listed alone, whose size refuses a blob
// past ext.MaxBlob before any of it is read; then the blob by the id the
// entry names, at most ext.MaxBlob + 1 bytes of it kept.
func (h *extHost) ReadBlob(ctx context.Context, r ext.BlobRequest) ([]byte, error) {
	if err := h.readsDoor(); err != nil {
		return nil, err
	}
	if err := r.Valid(); err != nil {
		return nil, refused("%v", err)
	}
	rctx, end := h.readCtx(ctx)
	defer end()
	top, err := h.repoTop(rctx, r.Run)
	if err != nil {
		return nil, h.readErr(rctx, err)
	}
	denied := h.s.treeDenied(top)
	if denied(r.Path) {
		return nil, refused("the path is one the server's deny list refuses")
	}
	if f := extReading.Load(); f != nil {
		(*f)("ReadBlob")
	}
	if _, err := commitOf(rctx, top, r.Commit); err != nil {
		return nil, h.readErr(rctx, err)
	}
	var entry *ext.TreeEntry
	out, err := treeSmall(rctx, top, maxEntryList, "ls-tree", "-z", "--long", "--full-tree", r.Commit, "--", r.Path)
	for rec := range strings.SplitSeq(out, "\x00") {
		e, typ, ok := readEntry([]byte(rec))
		if ok && e.Path == r.Path {
			if typ == "tree" {
				e.Kind = "tree"
			}
			entry = &e
			break
		}
	}
	switch {
	case err != nil:
		return nil, h.readErr(rctx, notFound("the commit's tree cannot be read"))
	case entry == nil || entry.Kind == "tree" || entry.Kind == "":
		return nil, h.readErr(rctx, notFound("the commit has no file at the path"))
	case entry.Kind == ext.EntrySubmodule:
		return nil, refused("the path is a submodule's entry, not a file")
	case denied(entry.Path):
		// The path git answers, judged as the path asked was.
		return nil, refused("the path is one the server's deny list refuses")
	case entry.Size > ext.MaxBlob:
		return nil, refused("the file is too large: %d bytes, past %d", entry.Size, ext.MaxBlob)
	}
	body := &gitcli.Head{N: ext.MaxBlob + 1}
	if err := treeBlob(rctx, top, body, "cat-file", "blob", entry.Object); err != nil {
		return nil, h.readErr(rctx, notFound("the file cannot be read"))
	}
	b := []byte(body.String())
	if len(b) > ext.MaxBlob {
		return nil, refused("the file is too large: past %d bytes", ext.MaxBlob)
	}
	if h.gone() != nil {
		return nil, context.Canceled
	}
	return b, nil
}

// Denied answers, for each of r.Paths, whether treeDenied refuses it now
// in the run's repository (ext.Host): no object and no file is read.
func (h *extHost) Denied(ctx context.Context, r ext.DeniedRequest) ([]bool, error) {
	if err := h.readsDoor(); err != nil {
		return nil, err
	}
	if err := r.Valid(); err != nil {
		return nil, refused("%v", err)
	}
	rctx, end := h.readCtx(ctx)
	defer end()
	top, err := h.repoTop(rctx, r.Run)
	if err != nil {
		return nil, h.readErr(rctx, err)
	}
	if f := extReading.Load(); f != nil {
		(*f)("Denied")
	}
	denied := h.s.treeDenied(top)
	out := make([]bool, len(r.Paths))
	for i, p := range r.Paths {
		out[i] = denied(p)
	}
	if h.gone() != nil {
		return nil, context.Canceled
	}
	return out, nil
}
