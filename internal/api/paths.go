package api

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The working-directory pickers (the crew editor and the Launch dialog)
// complete what is typed from GET /api/paths and show whether a crew with
// worktrees could launch there from GET /api/git/check. Both say what exists
// under the allowed roots and nothing else: a prefix is resolved as a
// session's working directory is (resolveCwd), so a symbolic link out of the
// roots leads nowhere. Each path is stat'ed before it is resolved (resolveDir),
// and the work of one request ends with its context: at its 5 s deadline, or
// when the client goes away.
//
// With paths.browse "any" (the desktop app sets it for its own server) a
// listing may ask for scope=any: every directory the server's user can stat,
// for the settings picker, which chooses the roots themselves and the data
// directory. The stat still comes first, and links are still resolved.

const (
	// maxPathEntries bounds one listing: a page for a picker, not a file system.
	maxPathEntries = 50
	// maxPathQuery bounds the prefix and cwd query values, as a crew's cwd is bounded.
	maxPathQuery = 4096
	// gitCheckTimeout bounds the git calls of one request, which run one at a time.
	gitCheckTimeout = 5 * time.Second
	// pathReadChunk is how many directory entries one read takes.
	pathReadChunk = 256
	// maxSkippedLinks bounds the symbolic links one listing resolves in vain.
	maxSkippedLinks = 50
)

// maxPathScan bounds the directory entries one listing reads, before they
// are sorted and cut to the limit: a directory of a million entries costs
// no more than one of maxPathScan. A test lowers it.
var maxPathScan = 2000

// gitState is crew.GitState, which a test replaces to count the calls.
var gitState = crew.GitState

// pathGit marks a listed directory: in a git working tree, and one whose
// HEAD is a commit, which a crew with worktrees needs.
type pathGit struct {
	Repo    bool `json:"repo"`
	Commits bool `json:"commits"`
}

type pathEntry struct {
	Name string  `json:"name"`
	Path string  `json:"path"`
	Git  pathGit `json:"git"`
}

type pathsReply struct {
	Dir       string      `json:"dir"`
	Entries   []pathEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
	// More is how many directories matching what was typed were left out
	// past the limit, and MoreUnknown that there may be others besides (the
	// directory held more entries than a listing reads, or the listing
	// stopped early): the picker says "30 more folders here" rather than
	// leaving a cut list to look whole (round 14).
	More        int  `json:"more,omitempty"`
	MoreUnknown bool `json:"moreUnknown,omitempty"`
}

type gitCheckReply struct {
	InRepo    bool   `json:"inRepo"`
	Toplevel  string `json:"toplevel,omitempty"`
	HasCommit bool   `json:"hasCommit"`
	Message   string `json:"message"`
}

// errNoAllowedDir says that no leading part of a prefix is a directory under
// the allowed roots. The reply's message names the roots (noAllowedDir).
var errNoAllowedDir = errors.New("no directory in the prefix is under the allowed roots")

// noAllowedDir is the message of the listing's invalid_cwd, which the picker
// shows as it is: errNoAllowedDir and the allowed roots, so that whoever
// typed a path outside them sees where to start. The route is the admin's,
// who configured the roots.
func (s *Server) noAllowedDir() string {
	return errNoAllowedDir.Error() + ": " + strings.Join(s.roots.paths(), ", ")
}

// resolveDir is resolveCwd behind a stat, with resolveCwd's errors. The
// kernel answers a symbolic link loop, or a chain too long to follow (ELOOP,
// past 40 links), in milliseconds, where resolveCwd's filepath.EvalSymlinks
// walks up to 255 links, each down its whole path again, for seconds: a path
// that does not stat as a directory is never resolved.
func (s *Server) resolveDir(path string) (string, error) {
	return s.resolveDirIn(path, scopeRoots)
}

// pathScope says which directories a listing may show.
type pathScope int

const (
	scopeRoots pathScope = iota // under the allowed roots, as resolveCwd checks a session's directory
	scopeAny                    // any directory the server's user can stat (paths.browse "any")
)

// resolveDirIn is resolveDir for a scope: the stat first in both, then
// resolveCwd under scopeRoots, or the symbolic links resolved and a second
// stat alone under scopeAny.
func (s *Server) resolveDirIn(path string, scope pathScope) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid working directory")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", errors.New("working directory does not exist")
	}
	if !fi.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	if scope == scopeAny {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", errors.New("working directory does not exist")
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			return "", errors.New("working directory is not a directory")
		}
		return real, nil
	}
	return s.resolveCwd(abs)
}

// splitPrefix finds the longest leading part of prefix that resolveDir
// accepts (an existing directory under an allowed root, symbolic links
// resolved) and the path element typed after it, "" when prefix names such
// a directory itself. A lone "." after the last separator is the start of a
// hidden name, which filepath.Clean would drop. An empty prefix is the
// server's default working directory. A prefix no part of which qualifies is
// errNoAllowedDir; the error is ctx's once ctx is done.
func (s *Server) splitPrefix(ctx context.Context, prefix string, scope pathScope) (dir, seg string, err error) {
	raw := cmp.Or(prefix, s.cfg.DefaultCwd)
	if strings.HasSuffix(raw, string(filepath.Separator)+".") {
		dir, seg, err := s.splitPrefix(ctx, strings.TrimSuffix(raw, "."), scope)
		if err == nil && seg == "" {
			seg = "."
		}
		return dir, seg, err
	}
	cur := filepath.Clean(raw)
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if real, err := s.resolveDirIn(cur, scope); err == nil {
			return real, seg, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", errNoAllowedDir
		}
		seg = filepath.Base(cur)
		cur = parent
	}
}

// candidate is a directory entry that may be listed: its name matches what
// is typed, and it is a directory or a symbolic link.
type candidate struct {
	name string
	link bool
}

// readCandidates reads at most maxPathScan entries of dir, pathReadChunk at a
// time, and keeps the directories and symbolic links whose names start with
// seg, hidden ones only when seg starts with a dot. capped reports that the
// directory has entries it did not read. dir is opened as a directory only
// (openDir).
func readCandidates(dir, seg string) (out []candidate, capped bool, err error) {
	f, err := openDir(dir)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	for read := 0; read < maxPathScan; {
		des, err := f.ReadDir(min(pathReadChunk, maxPathScan-read))
		read += len(des)
		for _, e := range des {
			name := e.Name()
			if !strings.HasPrefix(name, seg) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(seg, ".")) {
				continue
			}
			switch {
			case e.IsDir():
				out = append(out, candidate{name: name})
			case e.Type()&os.ModeSymlink != 0:
				out = append(out, candidate{name: name, link: true})
			}
		}
		if errors.Is(err, io.EOF) {
			return out, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	// maxPathScan entries read: one more says whether there are others.
	if _, err := f.ReadDir(1); errors.Is(err, io.EOF) {
		return out, false, nil
	}
	return out, true, nil
}

// listPaths lists the child directories of the directory prefix names (see
// splitPrefix) whose names start with the element typed after it: at most
// limit of them in name order, from at most maxPathScan entries read, hidden
// ones only when that element starts with a dot, a symbolic link only when it
// leads under an allowed root. A directory the server cannot read lists
// nothing. A child with a .git of its own, or a link, is asked git for its
// marks (a link's are those of the directory it leads to); the others carry
// their parent's, asked once and only when needed, so one listing runs
// GitState at most limit times, one after another.
//
// The work ends with ctx. Past its end, or once maxSkippedLinks links have led
// nowhere usable, no link is resolved any more: the listing stops at the next
// one, truncated. Past its end git is not asked either, and the listing keeps
// its entries, those not yet marked left unmarked, and is truncated too: a
// mark left out is not a directory outside git.
func (s *Server) listPaths(ctx context.Context, prefix string, limit int, scope pathScope) (pathsReply, error) {
	dir, seg, err := s.splitPrefix(ctx, prefix, scope)
	if err != nil {
		return pathsReply{}, err
	}
	out := pathsReply{Dir: dir, Entries: []pathEntry{}}
	cands, capped, err := readCandidates(dir, seg)
	if err != nil {
		s.log.Debug("list paths: cannot read the directory", "dir", dir, "err", err)
		return out, nil
	}
	out.Truncated = capped
	out.MoreUnknown = capped
	slices.SortFunc(cands, func(a, b candidate) int { return strings.Compare(a.name, b.name) })
	var parent *pathGit
	skipped := 0
	for i, c := range cands {
		path := filepath.Join(dir, c.name)
		own := "" // the directory git is asked about for the entry's own marks
		if c.link {
			if ctx.Err() != nil || skipped == maxSkippedLinks {
				out.Truncated = true
				out.More, out.MoreUnknown = len(cands)-i, true
				break
			}
			real, err := s.resolveDirIn(path, scope)
			if err != nil {
				skipped++
				continue
			}
			own = real
		}
		if len(out.Entries) == limit {
			out.Truncated = true
			out.More = len(cands) - i
			break
		}
		var mark pathGit
		unmarked := ctx.Err() != nil // git not asked, or cut off
		if !unmarked {
			if own == "" {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					own = path
				}
			}
			if own != "" {
				if mark, unmarked, err = gitMark(ctx, own); err != nil {
					return pathsReply{}, err
				}
			} else {
				if parent == nil {
					m, cut, err := gitMark(ctx, dir)
					if err != nil {
						return pathsReply{}, err
					}
					if cut {
						unmarked = true
					} else {
						parent = &m
					}
				}
				if parent != nil {
					mark = *parent
				}
			}
		}
		if unmarked {
			out.Truncated = true
		}
		out.Entries = append(out.Entries, pathEntry{Name: c.name, Path: path, Git: mark})
	}
	return out, nil
}

// gitMark is the git state of dir as the picker shows it. Without git on the
// server nothing is marked. When ctx ends during the call nothing is marked
// either, and cut says so: the listing keeps the entry, unmarked, and is
// truncated.
func gitMark(ctx context.Context, dir string) (mark pathGit, cut bool, err error) {
	st, err := gitState(ctx, dir)
	switch {
	case errors.Is(err, crew.ErrNoGit):
		return pathGit{}, false, nil
	case err != nil && ctx.Err() != nil:
		return pathGit{}, true, nil
	case err != nil:
		return pathGit{}, false, err
	}
	return pathGit{Repo: st.InRepo, Commits: st.HasCommit}, false, nil
}

// pathQuery reads a bounded path from the query q, or answers the request.
func pathQuery(w http.ResponseWriter, q url.Values, key string) (string, bool) {
	v := q.Get(key)
	if len(v) > maxPathQuery || strings.ContainsRune(v, 0) {
		writeError(w, http.StatusBadRequest, "invalid_request", key+" must be at most 4096 bytes without NUL")
		return "", false
	}
	return v, true
}

// handleListPaths answers GET /api/paths?prefix=<path>&limit=<n>. What a
// client asks about is logged at debug only.
func (s *Server) handleListPaths(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	prefix, ok := pathQuery(w, q, "prefix")
	if !ok {
		return
	}
	limit := maxPathEntries
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = min(n, maxPathEntries)
	}
	scope := scopeRoots
	switch q.Get("scope") {
	case "", config.BrowseRoots:
	case config.BrowseAny:
		if s.cfg.Paths.Browse != config.BrowseAny {
			writeError(w, http.StatusForbidden, "browse_off", "listing outside the allowed roots is off: set paths.browse to any (CONDUCTOR_PATHS_BROWSE=any)")
			return
		}
		scope = scopeAny
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "scope must be roots or any")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitCheckTimeout)
	defer cancel()
	reply, err := s.listPaths(ctx, prefix, limit, scope)
	switch {
	case errors.Is(err, errNoAllowedDir):
		writeError(w, http.StatusBadRequest, "invalid_cwd", s.noAllowedDir())
		return
	case err != nil:
		s.log.Debug("list paths failed", "prefix", prefix, "err", err)
		writeError(w, http.StatusInternalServerError, "list_failed", "could not list the directory")
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

// handleGitCheck answers GET /api/git/check?cwd=<path>: whether a crew with
// worktrees could launch in cwd (the default working directory when empty),
// by the launch's own rules (cwd resolved as resolveDir does). A preview:
// the launch's 409 is the authority. The top of the repository is named only when it is under the allowed
// roots: git names it as the repository says (core.worktree can name any
// directory), and a root may lie inside a repository.
func (s *Server) handleGitCheck(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	cwdInput, ok := pathQuery(w, q, "cwd")
	if !ok {
		return
	}
	cwd, err := s.resolveDir(cmp.Or(cwdInput, s.cfg.DefaultCwd))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cwd", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitCheckTimeout)
	defer cancel()
	st, err := gitState(ctx, cwd)
	switch {
	case errors.Is(err, crew.ErrNoGit):
		writeJSON(w, http.StatusOK, gitCheckReply{Message: crew.ErrNoGit.Error()})
		return
	case err != nil:
		s.log.Debug("git check failed", "cwd", cwd, "err", err)
		writeError(w, http.StatusInternalServerError, "git_failed", "could not run git")
		return
	}
	reply := gitCheckReply{InRepo: st.InRepo, HasCommit: st.HasCommit, Message: st.Message}
	if st.Toplevel != "" {
		if top, err := s.resolveDir(st.Toplevel); err == nil {
			reply.Toplevel = top
		}
	}
	writeJSON(w, http.StatusOK, reply)
}
