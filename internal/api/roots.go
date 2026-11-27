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
	"github.com/rocksolidlabs/conductor/internal/share"
)

// The folders agents may use (the allowed roots) are held in one place that
// can change while the server runs: the configuration's allowedRoots when
// the server is made, and every folder the desktop app that started the
// server has allowed since (POST /api/roots). Every check of a session's working
// directory reads them here (resolveCwd, openCwd), and so do the listing's
// refusal and the reads of a run's repository; nothing else reads the
// configuration's list once the server is made (TestOnlyRootsReadsTheConfiguredRoots).
// A running session is never touched by a change: its folder was opened at
// its launch.
//
// GET /api/roots/check says of one folder whether agents may use it and, if
// not, why not, by trying what a launch tries (its open included). POST
// /api/roots allows one more, for the desktop app alone: the workbench token
// and the key the app handed the server it started on its stdin,
// both; nothing is written to disk (the app keeps the saved list).

const (
	// maxRoots bounds the folders held: past it, nothing more is added.
	maxRoots = 64
	// maxRootPath bounds the path of a folder to allow, in bytes.
	maxRootPath = 1024
	// maxRootBody bounds the body of POST /api/roots.
	maxRootBody = 8 << 10
	// desktopKeyHeader carries the key the desktop app gave the server it
	// started (serve --secrets-stdin).
	desktopKeyHeader = "X-Conductor-Desktop-Key"
)

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
	refusedOwner         = "owner"
)

// How wide a folder outside the roots is.
const (
	wideHome      = "home"
	wideAboveHome = "above_home"
	wideHoldsData = "holds_data"
)

// allowedRoot is one folder agents may use: its path as configured or as
// added, and the folder it was when it was allowed (as the server started,
// or as it was added): where its links led then (real) and what that folder
// is (id: its device and number, owner and birth time). A folder typed in
// the configuration that is not there at the start has none, and is taken
// wherever its path leads at each use, as the person wrote it; a folder
// allowed by the press always has one (takePressed).
type allowedRoot struct {
	path string
	real string
	id   folderID
	// ident is how a later look tells what the folder is (identify, or a
	// test's stand-in: rootList.identity).
	ident func(path string, fi os.FileInfo) folderID
}

// pinRoot is the folder path is now, kept as the one allowed: a path that
// is a link on purpose keeps the folder it leads to now.
func pinRoot(path string) allowedRoot {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return allowedRoot{path: path}
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return allowedRoot{path: path}
	}
	return allowedRoot{path: path, real: real, id: identify(real, fi)}
}

// resolve is where the root's path leads now, and the judgement of the
// folder an open there must find: used only while it is still the folder it
// was when it was allowed, the same place and the same folder (identify). A
// link put at its path or above it later, or another folder put in its
// place, leads nowhere. An open through it (session.OpenFolderInWith)
// refuses a folder swapped in after this look.
func (r allowedRoot) resolve() (string, func(os.FileInfo) bool, bool) {
	real, err := filepath.EvalSymlinks(r.path)
	if err != nil {
		return "", nil, false
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", nil, false
	}
	if !r.id.known() {
		return real, func(got os.FileInfo) bool { return os.SameFile(got, fi) }, true
	}
	ident := r.ident
	if ident == nil {
		ident = identify
	}
	if real != r.real || !ident(real, fi).same(r.id) {
		return "", nil, false
	}
	return real, r.id.sameStat, true
}

// rootList holds the allowed roots: a list never changed in place, behind an
// atomic pointer, so a reader takes it whole without a lock; a writer holds
// mu and stores a new list. stores counts the lists stored, the first one
// included.
type rootList struct {
	mu     countedMutex
	held   atomic.Pointer[[]allowedRoot]
	stores atomic.Int64
	// stat, when a test sets it before its request, stands in for os.Stat
	// where a folder is judged by what it is (identities): a second name
	// for a folder is made so without a mount. mounts stands in for the
	// system's mount points (systemMounts).
	stat   func(path string) (os.FileInfo, error)
	mounts func() []string
	// beforeAdd, when a test sets it before its request, runs under mu
	// between a folder's judgement and the last look before it is added;
	// beforeLock runs as an add asks for mu, before anything is judged;
	// afterJudge once the add has read the list and judged the folder.
	// identity stands in for identify in a root's later looks and in the
	// last look before an add.
	beforeAdd  func(path string)
	beforeLock func(path string)
	afterJudge func(path string)
	identity   func(path string, fi os.FileInfo) folderID
}

// countedMutex is a mutex that counts the lockers waiting for it, so that
// a test can wait for an add to be at the lock rather than guess.
type countedMutex struct {
	sync.Mutex
	waiting atomic.Int32
}

// Lock takes the mutex, counted among the waiting while it waits.
func (m *countedMutex) Lock() {
	m.waiting.Add(1)
	defer m.waiting.Add(-1)
	m.Mutex.Lock()
}

// newRootList holds the configuration's allowed roots (the folders typed in
// it or in the app's Settings), each the folder it is as the server starts
// (pinRoot). The folders the app's press allowed come after, judged again
// (takePressed).
func newRootList(cfg *config.Config) *rootList {
	l := &rootList{}
	roots := make([]allowedRoot, 0, len(cfg.AllowedRoots))
	for _, p := range cfg.AllowedRoots {
		roots = append(roots, pinRoot(p))
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

// paths is the roots' paths as configured or added.
func (l *rootList) paths() []string {
	roots := l.list()
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.path
	}
	return out
}

// idOf is how the holder tells what a folder is: identify, or the test's
// stand-in.
func (l *rootList) idOf() func(string, os.FileInfo) folderID {
	if l != nil && l.identity != nil {
		return l.identity
	}
	return identify
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
	// id is the folder as the check opened it (an outside folder's), for
	// the last look before an add (stillTheFolder).
	id folderID
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
	v.state, v.id = folderOutside, s.roots.idOf()(real, want)
	v.refused = s.refusedKind(real)
	if v.refused == "" && !ownedByServer(v.id) {
		v.refused = refusedOwner
	}
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
// system's root, by what it is (whatever name reaches it); a name the saved
// list cannot hold as one folder; a folder the server's deny list refuses
// (Conductor's own data, the app's, an account's sign-in folder), which the
// deny list judges by identity too. "" for none.
func (s *Server) refusedKind(real string) string {
	switch {
	case filepath.Dir(real) == real || s.sameFolder(real, string(filepath.Separator)) || s.holdsBelow(real, s.statsOf(string(filepath.Separator))):
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

// wideKind is how wide real is, by what the folder is, whatever name
// reaches it: the server user's home folder, a folder the home folder is
// inside, or one an entry of the deny list is inside (a name entry,
// `<dir>/*name*`, by its directory). "" for a folder of none of these
// kinds.
func (s *Server) wideKind(real string) string {
	fi, err := s.statOf(real)
	if err != nil {
		return ""
	}
	if s.home != "" {
		home := resolvedPath(s.home)
		if hf, err := s.statOf(home); err == nil && os.SameFile(fi, hf) {
			return wideHome
		}
		if s.holds(fi, home) || s.holdsBelow(real, s.above(home)) {
			return wideAboveHome
		}
	}
	for _, e := range s.denyList() {
		if n := filepath.Base(e); len(n) > 1 && strings.HasPrefix(n, "*") && strings.HasSuffix(n, "*") {
			e = filepath.Dir(e)
		}
		// The entry itself is refused (refusedKind); a folder above it is wide,
		// and so is one that holds a second name of it, or of a folder above
		// it, below.
		if s.holds(fi, resolvedPath(e)) || s.holdsBelow(real, s.above(resolvedPath(e))) {
			return wideHoldsData
		}
	}
	return ""
}

// statOf is os.Stat, or the test's stand-in (rootList.stat).
func (s *Server) statOf(p string) (os.FileInfo, error) {
	if s.roots != nil && s.roots.stat != nil {
		return s.roots.stat(p)
	}
	return os.Stat(p)
}

// sameFolder reports whether a and b are one folder, by identity.
func (s *Server) sameFolder(a, b string) bool {
	fa, err := s.statOf(a)
	if err != nil {
		return false
	}
	fb, err := s.statOf(b)
	return err == nil && os.SameFile(fa, fb)
}

// holds reports whether the folder fi is above p (p itself not counted),
// by identity.
func (s *Server) holds(fi os.FileInfo, p string) bool {
	self, err := s.statOf(p)
	for _, a := range s.above(p) {
		if err == nil && os.SameFile(a, self) {
			continue
		}
		if os.SameFile(fi, a) {
			return true
		}
	}
	return false
}

// statsOf is the identity of p alone, as a list for holdsBelow; none when p
// cannot be looked at.
func (s *Server) statsOf(p string) []os.FileInfo {
	if fi, err := s.statOf(p); err == nil {
		return []os.FileInfo{fi}
	}
	return nil
}

// holdsBelow reports whether a mount point below real (real itself not
// counted) is one of the folders of ids, by identity: a folder that holds a
// second name of the whole disk, of the home folder or of a folder above it
// mounted somewhere below it holds that folder.
func (s *Server) holdsBelow(real string, ids []os.FileInfo) bool {
	if len(ids) == 0 {
		return false
	}
	mounts := systemMounts
	if s.roots != nil && s.roots.mounts != nil {
		mounts = s.roots.mounts
	}
	prefix := strings.TrimSuffix(real, string(filepath.Separator)) + string(filepath.Separator)
	for _, m := range mounts() {
		if !strings.HasPrefix(m, prefix) {
			continue
		}
		fi, err := s.statOf(m)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(ids, func(id os.FileInfo) bool { return os.SameFile(fi, id) }) {
			return true
		}
	}
	return false
}

// maxAbove bounds the folders above that are looked at.
const maxAbove = 256

// above is the identity of p and of every folder above it, p's first when
// it is there: by p's own parents (p, its directory, ..., the file system's
// root), and by the system's own way up from p (p/.., p/../.., ...), which
// a folder reached by a second name (a mount of it elsewhere, another
// letter case, a firmlink) takes as well. A part of p not there is left
// out; for a p not there, the first is its nearest folder that is.
func (s *Server) above(p string) []os.FileInfo {
	var out []os.FileInfo
	add := func(fi os.FileInfo) {
		if !slices.ContainsFunc(out, func(o os.FileInfo) bool { return os.SameFile(o, fi) }) {
			out = append(out, fi)
		}
	}
	first := ""
	for q := filepath.Clean(p); len(out) < maxAbove; q = filepath.Dir(q) {
		if fi, err := s.statOf(q); err == nil && fi.IsDir() {
			if first == "" {
				first = q
			}
			add(fi)
		}
		if filepath.Dir(q) == q {
			break
		}
	}
	if first == "" {
		return out
	}
	prev, err := s.statOf(first)
	for up, i := first, 0; err == nil && i < maxAbove; i++ {
		up += string(filepath.Separator) + ".."
		fi, err2 := s.statOf(up)
		if err2 != nil || os.SameFile(fi, prev) {
			break
		}
		add(fi)
		prev = fi
	}
	return out
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

// foldersFrom is whoami's folders: "app" when the desktop app that started
// the server may allow a folder while it runs (it gave the server its key),
// "config" when the folders are the configuration's alone.
func (s *Server) foldersFrom() string {
	if s.cfg.DesktopKey.Set() {
		return "app"
	}
	return "config"
}

// fromTheApp reports whether r comes from the desktop app that started the
// server: the workbench token of this server (no identity provider's
// principal) and the key the app gave the server, both present and equal.
// With no key held, nobody is: two empty strings are never compared.
func (s *Server) fromTheApp(r *http.Request) bool {
	p := principalOf(r)
	if !p.admin || p.yard || p.subject != "" {
		return false
	}
	want := s.cfg.DesktopKey.Reveal()
	got := r.Header.Get(desktopKeyHeader)
	if want == "" || got == "" {
		return false
	}
	return share.Equal(got, want)
}

// addRootRequest is POST /api/roots's body.
type addRootRequest struct {
	Path      string `json:"path"`
	Confirmed string `json:"confirmed,omitempty"`
}

// rootRefusal is an add's refusal, with the one field its code carries.
type rootRefusal struct {
	status        int
	code, message string
	key, value    string
}

// handleAddRoot answers POST /api/roots {path, confirmed?}: the desktop app
// that started the server allows one more folder, from the next request
// on. Nothing is written to disk and no session is touched. A request
// without the app's key is refused before its body is read, and counts
// against its address's limit of refused requests.
func (s *Server) handleAddRoot(w http.ResponseWriter, r *http.Request) {
	if !s.fromTheApp(r) {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusForbidden, "not_the_app", "only the Conductor app that started this server may allow a folder")
		return
	}
	var req addRootRequest
	if err := decodeJSONLimit(w, r, &req, maxRootBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "the body is more than 8 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch req.Confirmed {
	case "", wideHome, wideAboveHome, wideHoldsData:
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "confirmed must be home, above_home or holds_data")
		return
	}
	if len(req.Path) > maxRootPath || !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path || strings.ContainsRune(req.Path, 0) {
		writeError(w, http.StatusBadRequest, "invalid_request", "path must be an absolute path written clean (no . or .. element, no doubled or trailing separator), at most 1024 bytes")
		return
	}
	root, added, no := s.allowRoot(req.Path, req.Confirmed)
	if no != nil {
		e := map[string]any{"code": no.code, "message": no.message}
		if no.key != "" {
			e[no.key] = no.value
		}
		writeJSON(w, no.status, map[string]any{"error": e})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"root": root, "added": added})
}

// allowRoot adds path to the roots under the holder's lock, judged again
// there as the check judges it: only a folder that is its own real path, can
// be opened, is not refused and, when wide, was confirmed as that kind.
// Just before the add the folder is looked at once more, by identity.
func (s *Server) allowRoot(path, confirmed string) (string, bool, *rootRefusal) {
	l := s.roots
	if l.beforeLock != nil {
		l.beforeLock(path)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	roots := l.list()
	v := s.judgeFolder(path, roots)
	if l.afterJudge != nil {
		l.afterJudge(path)
	}
	switch v.state {
	case folderAllowed:
		return v.real, false, nil
	case folderMissing:
		return "", false, &rootRefusal{status: http.StatusConflict, code: "folder_missing", message: "the folder does not exist"}
	case folderNotDirectory:
		return "", false, &rootRefusal{status: http.StatusConflict, code: "folder_not_directory", message: "the path is a file, not a folder"}
	case folderUnreadable:
		return "", false, &rootRefusal{status: http.StatusConflict, code: "folder_unreadable", message: "Conductor cannot open the folder"}
	}
	if v.real != path {
		return "", false, &rootRefusal{status: http.StatusConflict, code: "folder_is_link", message: "the path goes through a link: only the folder it leads to can be allowed", key: "real", value: v.real}
	}
	if v.refused != "" {
		return "", false, &rootRefusal{status: http.StatusForbidden, code: "folder_refused", message: refusedFolderWords[v.refused], key: "reason", value: v.refused}
	}
	if v.wide != "" && confirmed != v.wide {
		return "", false, &rootRefusal{status: http.StatusConflict, code: "folder_wide", message: "the folder is wide: allow it with confirmed " + v.wide, key: "wide", value: v.wide}
	}
	if len(roots) >= maxRoots {
		return "", false, &rootRefusal{status: http.StatusConflict, code: "too_many_roots", message: "agents may use at most 64 folders"}
	}
	if l.beforeAdd != nil {
		l.beforeAdd(path)
	}
	if no := stillTheFolder(path, v.id, l.idOf()); no != nil {
		return "", false, no
	}
	l.with(allowedRoot{path: path, real: path, id: v.id, ident: l.identity})
	s.log.Info("a folder agents may use was added", "folder", path, "wide", v.wide)
	if s.cfg.OverlapsDataDir(path) {
		s.log.Warn(config.DataDirOverlapWords, "dataDir", s.cfg.DataDir, "allowedRoot", path)
	}
	return path, true, nil
}

// takePressed adds the folders the app's press allowed before (the app
// keeps them apart from the ones typed in Settings), each judged again as
// the press judged it: the folder its path names, its own real path, owned
// by the server's user, neither refused nor wider than the kind it was
// confirmed as. One that fails, or is not there, is not used for this run,
// and never followed by its name; a log line says so. One the typed roots
// hold already is not added again.
func (s *Server) takePressed(pressed []config.PressedRoot) {
	l := s.roots
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range pressed {
		if len(l.list()) >= maxRoots {
			s.log.Warn("a folder allowed by the press is not used this run", "folder", p.Path, "why", "agents may use at most 64 folders")
			continue
		}
		v := s.judgeFolder(p.Path, l.list())
		if v.state == folderAllowed {
			continue
		}
		if used, why := pressedVerdict(v, p); !used {
			s.log.Warn("a folder allowed by the press is not used this run", "folder", p.Path, "why", why)
			continue
		}
		l.with(allowedRoot{path: p.Path, real: p.Path, id: v.id, ident: l.identity})
	}
}

// pressedVerdict is whether a pressed folder judged at the start (v, not
// allowed already) is used, and why not: the judgement decides, as the
// press's does, and its words are for the log line alone, the reason's
// code where it has none. A kind the server does not know is not used.
func pressedVerdict(v folderVerdict, p config.PressedRoot) (bool, string) {
	switch {
	case p.Confirmed != "" && p.Confirmed != wideHome && p.Confirmed != wideAboveHome && p.Confirmed != wideHoldsData:
		return false, "it was allowed as a kind this server does not know"
	case v.state != folderOutside:
		return false, "it is " + strings.ReplaceAll(v.state, "_", " ")
	case v.real != p.Path:
		return false, "its path goes through a link"
	case v.refused != "":
		return false, cmp.Or(refusedFolderWords[v.refused], v.refused)
	case v.wide != "" && v.wide != p.Confirmed:
		return false, "it is wider than it was allowed as (" + v.wide + ")"
	}
	return true, ""
}

// refusedFolderWords are a refused folder's words.
var refusedFolderWords = map[string]string{
	refusedWholeDisk:     "agents cannot use the whole disk",
	refusedConductorData: "Conductor keeps its own data in this folder",
	refusedName:          "the folder's name holds a comma, a control character or a space at an end, which the saved list of folders cannot hold",
	refusedOwner:         "the folder belongs to another user",
}

// stillTheFolder is the last look before an add: path is still its own
// real path, and still the directory the judgement opened.
func stillTheFolder(path string, id folderID, ident func(string, os.FileInfo) folderID) *rootRefusal {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return &rootRefusal{status: http.StatusConflict, code: "folder_missing", message: "the folder does not exist"}
	}
	if real != path {
		return &rootRefusal{status: http.StatusConflict, code: "folder_is_link", message: "the path goes through a link: only the folder it leads to can be allowed", key: "real", value: real}
	}
	fi, err := os.Stat(path)
	if err != nil || !ident(path, fi).same(id) {
		return &rootRefusal{status: http.StatusConflict, code: "folder_changed", message: "the folder changed while it was checked: check it again"}
	}
	return nil
}
