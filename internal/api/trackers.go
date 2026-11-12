package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/store"
	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
)

// The person's GitHub token (docs/plans/tracker-linking.md, PR-3). The
// server writes a token to one file and sends it in one header to one
// address; nothing that can be computed from a token alone leaves the
// process; what the token's own host answers of an account is that
// host's data, kept and shown to the admin in a bounded form, never logged.
//
// The one file is credentials/github.json in the data directory: the
// directory 0700, the file 0600, both the server's user's. Every
// operation opens the directory once, without following a link or
// waiting (openDir), judges what it opened, and makes every later step
// relative to it: the file's open, the lock every change holds (change),
// the next file written under one fixed name and renamed into place
// (writeTokens). The file's types (trackerEntry, trackerFile, trackerWire) are
// named in this file only (TestTrackerTokenKeptByOneFile); a kept token is
// held behind a function value (trackerEntry.keptSecret), as the
// environment's token is (config.Secret), and leaves it at two places
// alone (reveal, environmentToken). Every route reaches a token through
// credentialFor, which wraps it at once in a tracker.Credential, whose
// every print is fixed words. The export leaves credentials/ out and a
// restore refuses it (internal/export, leftout.go); the sync adapter's
// allowlist does not name it (sync.go); a session's file reads never
// reach the data directory (config.FileDeny). A token goes to GitHub only
// through the credential's transport: its own API address, in
// Authorization, at every hop (tracker.Credential.Transport), a redirect
// elsewhere handed back unfollowed (internal/tracker/github).

// The token's bounds and names.
const (
	trackerDir = "credentials"
	githubFile = "github.json"
	// trackerTemp is the one name the next file is written under before
	// its rename, and trackerLock the file whose lock every change holds:
	// with githubFile, the directory's only names.
	trackerTemp     = "github.json.tmp"
	trackerLock     = "github.json.lock"
	maxTrackerHosts = 4
	// maxTrackerBody bounds a save's or a remove's body.
	maxTrackerBody = 4 << 10
	// maxTrackerToken bounds a token: 1 to 255 bytes of visible ASCII
	// (config.TrackerTokenForm).
	maxTrackerToken = config.MaxTrackerToken
	// maxTrackerFile bounds the file read back, and so the file written
	// (writeTokens): four entries with every field at its bound (a token's, a
	// host's and an account's, tracker.Account.Check) fit well inside it
	// (TestTrackerFileBounds).
	maxTrackerFile = 16 << 10
	// trackerLockWait bounds a change's wait for the file's lock, which
	// another change holds over a read, a save's check and a rename.
	trackerLockWait = 5 * time.Second
	// trackerCheckTimeout bounds a save's check of the token with GitHub
	// (Whoami): the adapter's own request's bound.
	trackerCheckTimeout = github.RequestTimeout
)

// trackerEntry is one host's token as the file keeps it, in memory: the
// token behind a function value, so that no print, encoding or reflection
// of an entry or of a value holding one reaches a byte of it. It never
// leaves this file: a reply is a trackerView.
type trackerEntry struct {
	Host       string
	keptSecret func() string
	Login      string
	AccountID  tracker.RemoteID
	Scopes     []string
	SavedAt    time.Time
}

// trackerFile is credentials/github.json, read.
type trackerFile struct{ GitHub []trackerEntry }

func (f trackerFile) entry(host string) (trackerEntry, bool) {
	i := slices.IndexFunc(f.GitHub, func(e trackerEntry) bool { return e.Host == host })
	if i < 0 {
		return trackerEntry{}, false
	}
	return f.GitHub[i], true
}

// trackerWire is the file's encoding: built inside writeTokens and decoded
// inside readIn, the one value that holds a token as a string field.
type trackerWire struct {
	GitHub []trackerWireEntry `json:"github"`
}

type trackerWireEntry struct {
	Host      string           `json:"host"`
	Token     string           `json:"token"`
	Login     string           `json:"login"`
	AccountID tracker.RemoteID `json:"accountId"`
	Scopes    []string         `json:"scopes,omitempty"`
	SavedAt   time.Time        `json:"savedAt"`
}

// trackerSeen is what GitHub last said of the token kept for a host, by
// its mark, and when this server asked, since it started (recordCheck).
type trackerSeen struct {
	fingerprint, state string
	at                 time.Time
}

// trackerKeep is the server's part of the tokens: the hosts being checked,
// what GitHub last said, and the adapter. Nothing of the file is
// remembered across requests: every route reads it.
type trackerKeep struct {
	cfg *config.Config
	st  *store.Store
	log *slog.Logger
	// mu guards checking and seen, never held over a read of the file or a
	// check: the file's own lock orders the changes (change).
	mu       sync.Mutex
	checking map[string]int
	seen     map[string]trackerSeen
	gh       *github.Adapter
	now      func() time.Time
	// The seams: reached counts the times a token leaves the value that
	// holds it (reveal, environmentToken); afterDirOpen runs between an
	// operation's open of the directory and its open of the file, unset in
	// the product; lockFile tries once for a file's lock, the platform's.
	reached      atomic.Uint64
	afterDirOpen func()
	lockFile     func(*os.File) error
}

func newTrackers(cfg *config.Config, st *store.Store, log *slog.Logger) *trackerKeep {
	k := &trackerKeep{cfg: cfg, st: st, log: log, checking: map[string]int{}, seen: map[string]trackerSeen{}, now: time.Now, lockFile: lockTrackerFile}
	k.gh = k.adapter(nil)
	k.sweep()
	return k
}

// sweep removes, once at start and only if the file's lock is free at
// once, what a change cut short may have left under trackerTemp; with
// nothing there it makes nothing.
func (k *trackerKeep) sweep() {
	d, err := k.openDir(false)
	if d == nil || err != nil {
		return
	}
	defer d.Close()
	t, err := openTrackerAt(d, trackerTemp, os.O_RDONLY)
	if err != nil {
		return
	}
	t.Close()
	if l, e := k.lock(d, 0); e == nil {
		_ = removeTrackerAt(d, trackerTemp)
		l.Close()
	}
}

// adapter is the GitHub adapter over base (nil: the standard transport;
// tests give a fake's). Its Enterprise Server hosts are the hosts a token
// is kept or given for, and those being checked.
func (k *trackerKeep) adapter(base http.RoundTripper) *github.Adapter {
	return github.New(github.Options{Hosts: k.hosts, Loopback: k.cfg.TrackerLoopback, Transport: base, Logger: k.log})
}

func (k *trackerKeep) hosts() []string {
	var out []string
	if f, err := k.read(); err == nil {
		for _, e := range f.GitHub {
			out = append(out, e.Host)
		}
	}
	if k.cfg.GitHubToken.Set() {
		out = append(out, k.cfg.GitHubHost)
	}
	k.mu.Lock()
	for h := range k.checking {
		out = append(out, h)
	}
	k.mu.Unlock()
	return out
}

// reveal is e's token: with environmentToken, the one place a token
// leaves the value that holds it, counted (reached).
func (k *trackerKeep) reveal(e trackerEntry) string {
	k.reached.Add(1)
	return e.keptSecret()
}

// environmentToken is the token the environment gives for host, if it
// gives one for that host (reveal's twin, counted the same way).
func (k *trackerKeep) environmentToken(host string) (string, bool) {
	if !k.cfg.GitHubToken.Set() || host != k.cfg.GitHubHost {
		return "", false
	}
	k.reached.Add(1)
	return k.cfg.GitHubToken.Reveal(), true
}

// errNoTrackerStore: a server without a data directory keeps no token.
var errNoTrackerStore = errors.New("this server has no data directory to keep a token in")

// The platform's refusals (trackers_unix.go): what is at a name is not
// what the step opens (a link, or no directory where one is opened), the
// lock is another change's, or the file system has no locks.
var (
	errTrackerNotThere = errors.New("not what the step opens")
	errTrackerBusy     = errors.New("the lock is held")
	errTrackerNoLocks  = errors.New("the file system of the data directory has no file locks: a token cannot be kept there")
)

// openDir opens credentials/ once (made 0700 first when create), without
// following a link or waiting on what is at its name, and judges the
// directory it opened: a directory, the server's user's, open to no one
// else. Every later step of the operation is made relative to it. No
// directory is nil with no error.
func (k *trackerKeep) openDir(create bool) (*os.File, error) {
	if k.st == nil {
		if create {
			return nil, errNoTrackerStore
		}
		return nil, nil
	}
	d, err := openTrackerDir(filepath.Join(k.st.Dir(), trackerDir), create)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !create:
		return nil, nil
	case errors.Is(err, errTrackerNotThere):
		return nil, fmt.Errorf("%s is not a directory", trackerDir)
	case err != nil:
		return nil, err
	}
	fi, err := d.Stat()
	if err == nil && !fi.IsDir() {
		err = fmt.Errorf("%s is not a directory", trackerDir)
	}
	if err == nil {
		err = private(trackerDir, fi)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// read is the file as it is now (readIn). No directory or no file is no
// entry.
func (k *trackerKeep) read() (trackerFile, error) {
	d, err := k.openDir(false)
	if d == nil {
		return trackerFile{}, err
	}
	defer d.Close()
	return k.readIn(d)
}

// readIn is the file of the directory d, read whole: opened relative to
// d without following a link or waiting (a named pipe answers at once),
// and judged as it was opened: a regular file, the server's user's, open
// to no one else, at most maxTrackerFile bytes, decoded strictly, and
// what a save writes: at most maxTrackerHosts entries, each host's once,
// each a host, a token and an account in their forms (the account's own
// check, tracker.Account.Check) and a saved-at that is not zero. No file
// is no entry.
func (k *trackerKeep) readIn(d *os.File) (trackerFile, error) {
	if k.afterDirOpen != nil {
		k.afterDirOpen()
	}
	name := trackerDir + "/" + githubFile
	file, err := openTrackerAt(d, githubFile, os.O_RDONLY)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return trackerFile{}, nil
	case errors.Is(err, errTrackerNotThere):
		return trackerFile{}, fmt.Errorf("%s is not a regular file", name)
	case err != nil:
		return trackerFile{}, err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return trackerFile{}, err
	}
	if !fi.Mode().IsRegular() {
		return trackerFile{}, fmt.Errorf("%s is not a regular file", name)
	}
	if err := private(name, fi); err != nil {
		return trackerFile{}, err
	}
	b, err := io.ReadAll(io.LimitReader(file, maxTrackerFile+1))
	if err != nil {
		return trackerFile{}, err
	}
	if len(b) > maxTrackerFile {
		return trackerFile{}, fmt.Errorf("%s is more than %d bytes", name, maxTrackerFile)
	}
	var w trackerWire
	if err := store.DecodeStrict(b, &w); err != nil {
		// The decoder's words may quote the file: only its name is said.
		return trackerFile{}, fmt.Errorf("%s cannot be read as a token file", name)
	}
	var f trackerFile
	for i, x := range w.GitHub {
		if _, twice := f.entry(x.Host); twice {
			return trackerFile{}, fmt.Errorf("%s holds a host's entry twice", name)
		}
		if i >= maxTrackerHosts || !config.TrackerTokenForm(x.Token) || tracker.CheckHost(x.Host, k.cfg.TrackerLoopback) != nil ||
			(tracker.Account{Login: x.Login, ID: x.AccountID, Scopes: x.Scopes}).Check() != nil || x.SavedAt.IsZero() {
			return trackerFile{}, fmt.Errorf("%s holds what a save would not have written", name)
		}
		tok := x.Token
		f.GitHub = append(f.GitHub, trackerEntry{Host: x.Host, keptSecret: func() string { return tok }, Login: x.Login, AccountID: x.AccountID, Scopes: x.Scopes, SavedAt: x.SavedAt})
	}
	return f, nil
}

// private refuses what another user owns or may read (where the file
// system has Unix owners), as the hooks directory is checked.
func private(name string, fi os.FileInfo) error {
	owner, ok := agents.FileOwner(fi)
	if !ok {
		return nil
	}
	if owner != os.Geteuid() {
		return fmt.Errorf("%s belongs to uid %d, not to the user running conductor", name, owner)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to its group or others (mode %04o)", name, fi.Mode().Perm())
	}
	return nil
}

// lock is the file's lock in the directory d, a private regular file
// opened relative to it, taken within wait (one try for 0). Every change
// holds it, on this server and on every other over the data directory;
// closing the file lets it go.
func (k *trackerKeep) lock(d *os.File, wait time.Duration) (*os.File, *apiError) {
	l, err := openTrackerAt(d, trackerLock, os.O_RDWR|os.O_CREATE)
	var fi os.FileInfo
	if err == nil {
		if fi, err = l.Stat(); err == nil && !fi.Mode().IsRegular() {
			err = fmt.Errorf("%s/%s is not a regular file", trackerDir, trackerLock)
		}
		if err == nil {
			err = private(trackerDir+"/"+trackerLock, fi)
		}
		if err != nil {
			l.Close()
		}
	}
	if errors.Is(err, errTrackerNotThere) {
		err = fmt.Errorf("%s/%s is not a regular file", trackerDir, trackerLock)
	}
	if err != nil {
		return nil, newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
	}
	deadline := time.Now().Add(wait)
	for {
		err := k.lockFile(l)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, errTrackerBusy) || !time.Now().Before(deadline) {
			l.Close()
			if errors.Is(err, errTrackerBusy) {
				return nil, newAPIError(http.StatusServiceUnavailable, "credentials_busy", "the token file is being changed by another request or server: try again")
			}
			return nil, newAPIError(http.StatusInternalServerError, "credentials_unusable", errTrackerNoLocks.Error())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// change makes one change of the file. Holding its lock, in the one
// directory it opened (made when create), it removes what a change cut
// short may have left under trackerTemp, reads the file as it is then,
// and hands both to fn, which decides from that alone and writes (writeTokens)
// or not. Without create, no directory is an empty file and no lock.
func (k *trackerKeep) change(create bool, fn func(d *os.File, f trackerFile) *apiError) *apiError {
	d, err := k.openDir(create)
	if err != nil {
		return newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
	}
	if d == nil {
		return fn(nil, trackerFile{})
	}
	defer d.Close()
	l, ae := k.lock(d, trackerLockWait)
	if ae != nil {
		return ae
	}
	defer l.Close()
	if err := removeTrackerAt(d, trackerTemp); err != nil {
		k.log.Error("tracker token file not changed", "err", err)
		return newAPIError(http.StatusInternalServerError, "store_failed", "the token file could not be changed: the server's log says why")
	}
	f, err := k.readIn(d)
	if err != nil {
		return newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
	}
	return fn(d, f)
}

// errTrackerFileFull is writeTokens' refusal of a file read would refuse.
var errTrackerFileFull = fmt.Errorf("%s/%s would be more than %d bytes", trackerDir, githubFile, maxTrackerFile)

// writeTokens puts f in place of the file, in the directory d, the caller
// holding the lock (change): encoded, refused past read's bound with the
// old file left whole, written under trackerTemp (created, never
// followed, mode 0600), synced, and renamed into place.
func (k *trackerKeep) writeTokens(d *os.File, f trackerFile) error {
	slices.SortFunc(f.GitHub, func(a, b trackerEntry) int { return strings.Compare(a.Host, b.Host) })
	w := trackerWire{GitHub: []trackerWireEntry{}}
	for _, e := range f.GitHub {
		w.GitHub = append(w.GitHub, trackerWireEntry{Host: e.Host, Token: k.reveal(e), Login: e.Login, AccountID: e.AccountID, Scopes: e.Scopes, SavedAt: e.SavedAt})
	}
	b, err := store.Encode(w)
	if err != nil || len(b) > maxTrackerFile {
		return cmp.Or(err, errTrackerFileFull)
	}
	t, err := openTrackerAt(d, trackerTemp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	_, err = t.Write(b)
	if err == nil {
		err = t.Sync()
	}
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameTrackerAt(d, trackerTemp, githubFile)
	}
	if err != nil {
		_ = removeTrackerAt(d, trackerTemp)
		return err
	}
	_ = d.Sync()
	return nil
}

// errNoToken is credentialFor's answer for an accepted host with no token.
func errNoToken(host string) *apiError {
	return newAPIError(http.StatusPreconditionFailed, "no_token", "no GitHub token is saved for "+host+": save one in Settings")
}

// credentialFor is the one place every tracker route gets its credential:
// the token for ref's host, the environment's when it gives that host,
// else the file's, read now (a remove takes effect at the next call),
// wrapped at once in a tracker.Credential, never one without a token (412
// no_token); a host out of its form is refused in fixed words. It takes
// the whole ref so that a later credential source chooses inside it and
// no route changes. It takes no lock.
func (s *Server) credentialFor(_ context.Context, ref tracker.Ref) (tracker.Credential, error) {
	k := s.trackers
	if ref.System != tracker.GitHub {
		return tracker.Credential{}, newAPIError(http.StatusBadRequest, "not_a_tracker_url", "no tracker this server reads")
	}
	if tracker.CheckHost(ref.Host, k.cfg.TrackerLoopback) != nil {
		return tracker.Credential{}, newAPIError(http.StatusBadRequest, "invalid_host", trackerHostWords)
	}
	tok, ok := k.environmentToken(ref.Host)
	if !ok {
		f, err := k.read()
		if err != nil {
			return tracker.Credential{}, newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
		}
		if e, kept := f.entry(ref.Host); kept {
			tok = k.reveal(e)
		}
	}
	c, err := tracker.Token(ref.Host, tok, k.cfg.TrackerLoopback)
	if err == nil && c.IsZero() {
		return tracker.Credential{}, errNoToken(ref.Host)
	}
	return c, err
}

// recordCheck notes what GitHub said (ok, or refused: a token that
// stopped working), asked at asked, of the token of mark m for host, when
// it is the one f keeps there. A save calls it holding the file's lock,
// f read under it (change), so checks are recorded in the order they
// were asked and the latest is shown. A save for the environment's host
// is refused before it asks.
func (k *trackerKeep) recordCheck(f trackerFile, host, m, state string, asked time.Time) {
	if e, ok := f.entry(host); !ok || tracker.Mark(k.reveal(e)) != m {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen[host] = trackerSeen{fingerprint: m, state: state, at: asked}
}

// trackerView is a token as a person is shown it: never the token.
type trackerView struct {
	Host      string           `json:"host"`
	Login     string           `json:"login,omitempty"`
	AccountID tracker.RemoteID `json:"accountId,omitempty"`
	// Fingerprint tells this server's tokens apart until it restarts
	// (tracker.Mark): nothing that can be computed from a token.
	Fingerprint string     `json:"fingerprint"`
	Scopes      []string   `json:"scopes"`
	SavedAt     *time.Time `json:"savedAt,omitempty"`
	// Source is settings (the file) or environment.
	Source string `json:"source"`
	// State is ok, refused (GitHub refused it when it was asked at
	// CheckedAt: save a new one), unchecked (the environment's, before any
	// call) or unused (the file's for the host the environment gives a
	// token for: the environment's is used). CheckedAt is when this server
	// asked, absent when it has not since it started or asked before the
	// entry's SavedAt (seenOf).
	State     string     `json:"state"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

type trackersReply struct {
	GitHub []trackerView `json:"github"`
}

// tokenView is e as shown, with what GitHub said of it when this server asked.
func (k *trackerKeep) tokenView(e trackerEntry) trackerView {
	saved := e.SavedAt
	v := trackerView{Host: e.Host, Login: e.Login, AccountID: e.AccountID, Fingerprint: tracker.Mark(k.reveal(e)), Scopes: append([]string{}, e.Scopes...), SavedAt: &saved, Source: "settings", State: "ok"}
	k.seenOf(&v)
	return v
}

// seenOf adds to v what this server last heard of its token: only a check
// asked at or after the entry was saved, since an older one is not that
// save's (another server may have removed the host and saved it again).
func (k *trackerKeep) seenOf(v *trackerView) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s, ok := k.seen[v.Host]; ok && s.fingerprint == v.Fingerprint && (v.SavedAt == nil || !s.at.Before(*v.SavedAt)) {
		at := s.at
		v.State, v.CheckedAt = s.state, &at
	}
}

// listTokens is every token kept or given, as shown: the file's by host,
// then the environment's. The file's for the environment's host is unused.
func (k *trackerKeep) listTokens() ([]trackerView, error) {
	f, err := k.read()
	if err != nil {
		return nil, err
	}
	out := []trackerView{}
	envHost := k.cfg.GitHubHost
	tok, given := k.environmentToken(envHost)
	for _, e := range f.GitHub {
		v := k.tokenView(e)
		if given && e.Host == envHost {
			v.State = "unused"
		}
		out = append(out, v)
	}
	if given {
		v := trackerView{Host: envHost, Fingerprint: tracker.Mark(tok), Scopes: []string{}, Source: "environment", State: "unchecked"}
		k.seenOf(&v)
		out = append(out, v)
	}
	return out, nil
}

// routeTrackers adds the three routes, the admin's alone and a
// switchyard's 403 (launching): a share link, a session's token, a host
// token and a Brakeman turn are refused.
func (s *Server) routeTrackers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/trackers", s.launching(s.handleTrackers))
	mux.HandleFunc("POST /api/trackers/github", s.launching(s.handleTrackerSave))
	mux.HandleFunc("POST /api/trackers/github/remove", s.launching(s.handleTrackerRemove))
}

func (s *Server) handleTrackers(w http.ResponseWriter, _ *http.Request) {
	views, err := s.trackers.listTokens()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credentials_unusable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, trackersReply{GitHub: views})
}

// trackerSaveBody is a save's body: the host (github.com when not given)
// and the token, 1 to 255 bytes of visible ASCII.
type trackerSaveBody struct {
	Host  string `json:"host,omitempty"`
	Token string `json:"token"`
}

// trackerRemoveBody is a remove's body: the host it names.
type trackerRemoveBody struct {
	Host string `json:"host"`
}

// trackerBodyWords is the answer to a body that is not one JSON object of
// the route's members: the server's own words, never the decoder's.
const trackerBodyWords = "the body is one JSON object of the route's members alone: host and token for a save, host for a remove"

// trackerBody decodes a save's or a remove's body: one JSON object (null,
// a number, a string or a list is not one) of the route's members alone,
// at most maxTrackerBody bytes; false once it has answered.
func trackerBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var b *T
	err := decodeJSONLimit(w, r, &b, maxTrackerBody)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", errBodyTooLarge.Error())
	case err != nil || b == nil:
		writeError(w, http.StatusBadRequest, "invalid_request", trackerBodyWords)
	default:
		return *b, true
	}
	var zero T
	return zero, false
}

// trackerHostWords answer a host out of its form, quoting nothing of it.
const trackerHostWords = "the host is github.com or a GitHub Enterprise Server's name of at least two labels, in lower case, https only"

// trackerHost holds host to the tracker's rule; false once it has
// answered (trackerHostWords).
func (s *Server) trackerHost(w http.ResponseWriter, host string) bool {
	if tracker.CheckHost(host, s.cfg.TrackerLoopback) != nil {
		writeError(w, http.StatusBadRequest, "invalid_host", trackerHostWords)
		return false
	}
	return true
}

// environmentWords says the environment gives host's token.
func environmentWords(host string) string {
	return "the token for " + host + " is given in the server's environment (CONDUCTOR_GITHUB_TOKEN): change it there"
}

// handleTrackerSave checks a token with GitHub (Whoami) and keeps it, all
// holding the file's lock (change): the file read, GitHub asked, the token
// written or not and what GitHub said recorded before the lock is let go,
// so saves and removes are made one at a time, on this server and every
// other over the data directory. A token equal to the one kept
// (share.Equal) changes nothing. A token GitHub refuses is kept nowhere,
// and the one already kept stays.
func (s *Server) handleTrackerSave(w http.ResponseWriter, r *http.Request) {
	k := s.trackers
	b, ok := trackerBody[trackerSaveBody](w, r)
	if !ok {
		return
	}
	host := cmp.Or(b.Host, tracker.GitHubHost)
	if !s.trackerHost(w, host) {
		return
	}
	if s.cfg.GitHubToken.Set() && host == s.cfg.GitHubHost {
		writeError(w, http.StatusConflict, "environment", environmentWords(host))
		return
	}
	if !config.TrackerTokenForm(b.Token) {
		writeError(w, http.StatusBadRequest, "invalid_token", "a token is 1 to 255 bytes of visible ASCII, with no space")
		return
	}
	if k.st == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", errNoTrackerStore.Error())
		return
	}
	cred, err := tracker.Token(host, b.Token, s.cfg.TrackerLoopback)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_host", "not a host Conductor reads")
		return
	}
	tok := b.Token
	kept, unchanged := trackerEntry{}, false
	refused := k.change(true, func(d *os.File, f trackerFile) *apiError {
		k.mu.Lock()
		k.checking[host]++
		k.mu.Unlock()
		asked := k.now().UTC()
		ctx, cancel := context.WithTimeout(r.Context(), trackerCheckTimeout)
		acct, err := k.gh.Whoami(ctx, cred)
		cancel()
		k.mu.Lock()
		if k.checking[host]--; k.checking[host] == 0 {
			delete(k.checking, host)
		}
		k.mu.Unlock()
		if err != nil {
			if errors.Is(err, tracker.ErrUnauthorized) {
				k.recordCheck(f, host, cred.Fingerprint(), "refused", asked)
			}
			var rl *tracker.ErrRateLimited
			if errors.As(err, &rl) {
				wait := 60
				if !rl.Reset.IsZero() {
					wait = max(1, int(time.Until(rl.Reset).Seconds()+0.999))
				}
				w.Header().Set("Retry-After", strconv.Itoa(wait))
			}
			return trackerRefusal(err, host)
		}
		cur, has := f.entry(host)
		if has && share.Equal(tok, k.reveal(cur)) {
			kept, unchanged = cur, true
			k.recordCheck(f, host, cred.Fingerprint(), "ok", asked)
			return nil
		}
		// Saved at its check's time, to the second, so that check is shown with it (seenOf).
		kept = trackerEntry{Host: host, keptSecret: func() string { return tok }, Login: acct.Login, AccountID: acct.ID, Scopes: acct.Scopes, SavedAt: asked.Truncate(time.Second)}
		switch i := slices.IndexFunc(f.GitHub, func(x trackerEntry) bool { return x.Host == host }); {
		case i >= 0:
			f.GitHub[i] = kept
		case len(f.GitHub) >= maxTrackerHosts:
			return newAPIError(http.StatusConflict, "too_many_hosts", fmt.Sprintf("tokens are kept for at most %d hosts: remove one first", maxTrackerHosts))
		default:
			f.GitHub = append(f.GitHub, kept)
		}
		if err := k.writeTokens(d, f); err != nil {
			k.log.Error("tracker token not written", "host", host, "err", err)
			return newAPIError(http.StatusInternalServerError, "store_failed", "the token could not be kept: the server's log says why")
		}
		// A token kept in place of another starts its own record.
		k.mu.Lock()
		k.seen[host] = trackerSeen{fingerprint: cred.Fingerprint(), state: "ok", at: asked}
		k.mu.Unlock()
		return nil
	})
	if refused != nil {
		lvl := slog.LevelInfo
		if refused.status >= 500 && refused.status != http.StatusBadGateway { // a 502 is GitHub's
			lvl = slog.LevelError
		}
		s.log.Log(r.Context(), lvl, "tracker token not saved", "route", r.Pattern, "host", host, "status", refused.status, "code", refused.Code)
		writeAPIError(w, refused)
		return
	}
	s.log.Info("tracker token saved", "route", r.Pattern, "host", host, "status", http.StatusOK, "unchanged", unchanged)
	reply := map[string]any{"tracker": k.tokenView(kept)}
	if unchanged {
		reply["unchanged"] = true
	}
	writeJSON(w, http.StatusOK, reply)
}

// handleTrackerRemove drops the entry of the host it names (no default),
// holding the file's lock (change). A call already under way finishes
// with the token it read; every later one is 412 no_token. It stops
// Conductor's use of the token only: the token stays valid on GitHub
// until the person revokes it there. For the host the environment gives a
// token for, the file's entry goes as well, and the answer says that the
// environment's token is still the one in use.
func (s *Server) handleTrackerRemove(w http.ResponseWriter, r *http.Request) {
	k := s.trackers
	b, ok := trackerBody[trackerRemoveBody](w, r)
	if !ok || !s.trackerHost(w, b.Host) {
		return
	}
	removed := false
	given := k.cfg.GitHubToken.Set() && k.cfg.GitHubHost == b.Host
	if refused := k.change(false, func(d *os.File, f trackerFile) *apiError {
		n := len(f.GitHub)
		f.GitHub = slices.DeleteFunc(f.GitHub, func(e trackerEntry) bool { return e.Host == b.Host })
		if removed = len(f.GitHub) < n; !removed {
			return nil
		}
		if err := k.writeTokens(d, f); err != nil {
			s.log.Error("tracker token not removed", "route", r.Pattern, "host", b.Host, "status", http.StatusInternalServerError, "err", err)
			return newAPIError(http.StatusInternalServerError, "store_failed", "the token could not be removed: the server's log says why")
		}
		if !given {
			k.mu.Lock()
			delete(k.seen, b.Host)
			k.mu.Unlock()
		}
		return nil
	}); refused != nil {
		writeAPIError(w, refused)
		return
	}
	s.log.Info("tracker token removed", "route", r.Pattern, "host", b.Host, "status", http.StatusOK, "removed", removed)
	reply := map[string]any{"removed": removed}
	if given {
		reply["inUse"], reply["message"] = "environment", "the token the server's environment gives for "+b.Host+" (CONDUCTOR_GITHUB_TOKEN) is still the one in use: change it there"
	}
	writeJSON(w, http.StatusOK, reply)
}

// trackerRefusal is a failed check's answer, in fixed words: a typed
// error and its status, never a word GitHub sent (which could echo what
// the request carried), a permission's name among them. Nothing is saved.
func trackerRefusal(err error, host string) *apiError {
	status := ""
	var re *tracker.RequestError
	if errors.As(err, &re) && re.Status != 0 {
		status = " (" + strconv.Itoa(re.Status) + ")"
	}
	var rl *tracker.ErrRateLimited
	switch {
	case errors.As(err, &rl):
		return newAPIError(http.StatusTooManyRequests, "rate_limited", "GitHub is limiting this token's requests"+status+": try again later; nothing was saved")
	case errors.Is(err, tracker.ErrUnauthorized):
		return newAPIError(http.StatusUnprocessableEntity, "token_refused", "GitHub refused this token for "+host+status+": it is not valid, has expired or was revoked; nothing was saved")
	case errors.Is(err, tracker.ErrForbidden):
		return newAPIError(http.StatusUnprocessableEntity, "token_refused", "GitHub refused to name this token's account"+status+": the token may lack a permission; nothing was saved")
	case errors.Is(err, tracker.ErrBound):
		return newAPIError(http.StatusBadGateway, "tracker_bound", "GitHub's answer was past its bound; nothing was saved")
	case errors.Is(err, tracker.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return newAPIError(http.StatusBadGateway, "tracker_unavailable", "GitHub could not be reached"+status+"; nothing was saved")
	}
	return newAPIError(http.StatusBadGateway, "tracker_unavailable", "GitHub's answer could not be read as an account"+status+"; nothing was saved")
}
