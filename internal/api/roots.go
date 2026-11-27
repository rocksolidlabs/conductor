package api

import (
	"cmp"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The folders agents may use (the allowed roots) are held in one place that
// can change while the server runs, set from the configuration's
// allowedRoots when the server is made. Every check of a session's working
// directory reads them here (resolveCwd, openCwd), and so do the listing's
// refusal and the reads of a run's repository; nothing else reads the
// configuration's list once the server is made (TestOnlyRootsReadsTheConfiguredRoots).
// A running session is never touched by a change: its folder was opened at
// its launch.
//
// GET /api/roots/check says of one folder whether agents may use it and, if
// not, why not, by trying what a launch tries (its open included).

// A folder's state, as the check says it.
const (
	folderAllowed      = "allowed"
	folderOutside      = "outside"
	folderMissing      = "missing"
	folderNotDirectory = "not_directory"
	folderUnreadable   = "unreadable"
)

// Why a folder outside the roots is never allowed from the app's screens.
const (
	refusedWholeDisk     = "whole_disk"
	refusedConductorData = "conductor_data"
	refusedName          = "name"
)

// How wide a folder outside the roots is.
const (
	wideHome      = "home"
	wideAboveHome = "above_home"
	wideHoldsData = "holds_data"
)

// allowedRoot is one folder agents may use, its path as configured.
type allowedRoot struct {
	path string
}

// resolve is the root's path with its links resolved now.
func (r allowedRoot) resolve() (string, bool) {
	real, err := filepath.EvalSymlinks(r.path)
	if err != nil {
		return "", false
	}
	return real, true
}

// rootList holds the allowed roots: a list never changed in place, behind an
// atomic pointer, so a reader takes it whole without a lock; a writer holds
// mu and stores a new list. stores counts the lists stored, the first one
// included.
type rootList struct {
	mu     sync.Mutex
	held   atomic.Pointer[[]allowedRoot]
	stores atomic.Int64
}

// newRootList holds the configuration's allowed roots.
func newRootList(cfg *config.Config) *rootList {
	l := &rootList{}
	roots := make([]allowedRoot, 0, len(cfg.AllowedRoots))
	for _, p := range cfg.AllowedRoots {
		roots = append(roots, allowedRoot{path: p})
	}
	l.store(roots)
	return l
}

// store makes roots the list held; the caller holds mu, or is newRootList.
func (l *rootList) store(roots []allowedRoot) {
	l.held.Store(&roots)
	l.stores.Add(1)
}

// list is the roots as they are now. The caller must not change it.
func (l *rootList) list() []allowedRoot {
	if l == nil {
		return nil
	}
	if p := l.held.Load(); p != nil {
		return *p
	}
	return nil
}

// paths is the roots' paths as configured.
func (l *rootList) paths() []string {
	roots := l.list()
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.path
	}
	return out
}

// with stores the roots with r added at their end; the caller holds mu.
func (l *rootList) with(r allowedRoot) {
	l.store(append(slices.Clone(l.list()), r))
}

// folderVerdict is what the check finds of a folder.
type folderVerdict struct {
	path    string // as asked, absolute and clean
	real    string // its links resolved; "" when not known
	state   string
	refused string
	wide    string
}

// judgeFolder says of path whether agents may use it under roots, by what a
// launch does: an allowed folder is one the launch's own open succeeds in
// (openCwdIn, closed at once). A folder outside the roots is opened as it
// would be once allowed (as its own root), then said refused or wide. The
// stat comes first, as the listing's (resolveDir): a chain of links the
// kernel refuses is never resolved.
func (s *Server) judgeFolder(path string, roots []allowedRoot) folderVerdict {
	abs, err := filepath.Abs(path)
	if err != nil {
		return folderVerdict{path: path, state: folderUnreadable}
	}
	v := folderVerdict{path: abs}
	fi, err := os.Stat(abs)
	if err != nil {
		v.state = lookState(err)
		return v
	}
	if !fi.IsDir() {
		v.state = folderNotDirectory
		return v
	}
	f, err := openCwdIn(abs, roots)
	if err == nil {
		v.real, v.state = f.Path(), folderAllowed
		f.Close()
		return v
	}
	if !errors.Is(err, errOutsideRoots) {
		v.state = lookState(err)
		return v
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		v.state = lookState(err)
		return v
	}
	v.real = real
	want, err := os.Stat(real)
	if err == nil && !want.IsDir() {
		err = errNotADirectory
	}
	if err != nil {
		v.state = lookState(err)
		return v
	}
	g, err := session.OpenFolderIn(real, ".", want)
	if err != nil {
		v.state = lookState(err)
		return v
	}
	g.Close()
	v.state = folderOutside
	v.refused = s.refusedKind(real)
	if v.refused == "" {
		v.wide = s.wideKind(real)
	}
	return v
}

// lookState is the state a look's error says: not there (a missing element,
// or one that is a file on the way), not a directory, or anything else, a
// folder that cannot be opened.
func lookState(err error) string {
	switch {
	case errors.Is(err, errNotADirectory):
		return folderNotDirectory
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return folderMissing
	}
	return folderUnreadable
}

// refusedKind is why real is never allowed from the app's screens: the file
// system's root; a name the saved list cannot hold as one folder; a folder
// the server's deny list refuses (Conductor's own data, the app's, an
// account's sign-in folder). "" for none.
func (s *Server) refusedKind(real string) string {
	switch {
	case filepath.Dir(real) == real:
		return refusedWholeDisk
	case unsavableName(real):
		return refusedName
	case session.DeniedBy(real, s.denyList()):
		return refusedConductorData
	}
	return ""
}

// unsavableName reports a path the saved list of folders cannot hold as one
// folder: the list travels as one comma-joined variable whose parts are
// trimmed, and Settings shows it one folder per line. So a comma, a control
// or formatting character (a line break, a tab, a direction mark), a line or
// paragraph separator, a space at either end, or bytes that are not UTF-8.
func unsavableName(p string) bool {
	if !utf8.ValidString(p) || strings.ContainsRune(p, ',') {
		return true
	}
	for _, r := range p {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	first, _ := utf8.DecodeRuneInString(p)
	last, _ := utf8.DecodeLastRuneInString(p)
	return unicode.IsSpace(first) || unicode.IsSpace(last)
}

// wideKind is how wide real is: the server user's home folder, a folder
// the home folder is inside, or one an entry of the deny list is inside (a
// name entry, `<dir>/*name*`, by its directory). "" for a folder of none of
// these kinds.
func (s *Server) wideKind(real string) string {
	if s.home != "" {
		home := resolvedPath(s.home)
		switch {
		case real == home:
			return wideHome
		case isUnder(home, real):
			return wideAboveHome
		}
	}
	for _, e := range s.denyList() {
		if n := filepath.Base(e); len(n) > 1 && strings.HasPrefix(n, "*") && strings.HasSuffix(n, "*") {
			e = filepath.Dir(e)
		}
		if isUnder(resolvedPath(e), real) {
			return wideHoldsData
		}
	}
	return ""
}

// denyList is the server's deny list as it stands now (fileDeny).
func (s *Server) denyList() []string {
	if s.fileDeny == nil {
		return nil
	}
	return s.fileDeny()
}

// resolvedPath is p with its links resolved, or p cleaned when it cannot be.
func resolvedPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// isUnder reports whether p is strictly inside dir.
func isUnder(p, dir string) bool {
	if dir == string(filepath.Separator) {
		return p != dir && strings.HasPrefix(p, dir)
	}
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

// rootCheckReply is GET /api/roots/check's answer.
type rootCheckReply struct {
	Path    string `json:"path"`
	Real    string `json:"real,omitempty"`
	State   string `json:"state"`
	Link    bool   `json:"link,omitempty"`
	Refused string `json:"refused,omitempty"`
	Wide    string `json:"wide,omitempty"`
}

// handleCheckRoot answers GET /api/roots/check?path=<p> (the server's
// default folder for an empty one). Where the folder leads (real, and
// whether a link was followed) is said for an allowed folder, and for any
// other only when the server lists every folder (paths.browse any).
func (s *Server) handleCheckRoot(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	path, ok := pathQuery(w, q, "path")
	if !ok {
		return
	}
	v := s.judgeFolder(cmp.Or(path, s.cfg.DefaultCwd), s.roots.list())
	reply := rootCheckReply{Path: v.path, State: v.state, Refused: v.refused, Wide: v.wide}
	if v.real != "" && (v.state == folderAllowed || s.cfg.Paths.Browse == config.BrowseAny) {
		reply.Real, reply.Link = v.real, v.real != v.path
	}
	writeJSON(w, http.StatusOK, reply)
}
