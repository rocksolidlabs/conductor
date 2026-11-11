package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/store"
	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
)

// The person's GitHub token (docs/plans/tracker-linking.md, PR-3). One
// file keeps it, credentials/github.json in the data directory: the
// directory 0700, the file 0600, both the server's user's, written by the
// store's atomic rename (write) and read back, per call, by read alone,
// which refuses a file or a directory another user owns or may read; the
// file's types (keptToken, trackerEntry, trackerFile) are named in this
// file only (TestTrackerTokenKeptByOneFile). The environment's token
// (CONDUCTOR_GITHUB_TOKEN) is held by the configuration as a
// config.Secret, whose every print is "set", and never written. Every
// route reaches a token through credentialFor, which wraps it at once in a
// tracker.Credential (whose every print is token(<fingerprint>)); the
// save's check (Whoami) and the list's fingerprint of the environment's
// are the only others. The export leaves credentials/ out and a restore
// refuses it (internal/export, leftout.go); the sync adapter's allowlist
// does not name it (sync.go); a session's file reads never reach the
// data directory (config.FileDeny). A token goes to GitHub only through
// the credential's transport: its own API address, in Authorization, at
// every hop (tracker.Credential.Transport), a redirect elsewhere handed
// back unfollowed (internal/tracker/github).

// The token's bounds.
const (
	trackerDir      = "credentials"
	githubFile      = "github.json"
	maxTrackerHosts = 4
	// maxTrackerBody bounds a save's or a remove's body.
	maxTrackerBody = 4 << 10
	// maxTrackerToken bounds a token: 1 to 255 bytes of visible ASCII
	// (config.TrackerTokenForm).
	maxTrackerToken = config.MaxTrackerToken
	// maxTrackerFile bounds the file read back, and so the file written
	// (write): four entries with every field at its bound (a token's, a
	// host's and an account's, tracker.Account.Check) fit well inside it
	// (TestTrackerFileBounds).
	maxTrackerFile = 16 << 10
	// trackerReadTries bounds the looks a read makes when a save's rename
	// crosses it (read).
	trackerReadTries = 3
	// trackerCheckTimeout bounds a save's check of the token with GitHub
	// (Whoami): the adapter's own request's bound.
	trackerCheckTimeout = github.RequestTimeout
)

// keptToken is a token as the file holds it. Its every print but the
// file's encoding is "token(kept)": a kept entry logged or formatted
// whole carries none of it.
type keptToken string

func (keptToken) String() string               { return "token(kept)" }
func (t keptToken) GoString() string           { return t.String() }
func (t keptToken) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(t.String())) }
func (t keptToken) LogValue() slog.Value       { return slog.StringValue(t.String()) }
func (t keptToken) same(presented string) bool { return share.Equal(presented, string(t)) }
func (t keptToken) credential(host string, loopback bool) (tracker.Credential, error) {
	return tracker.Token(host, string(t), loopback)
}

// trackerEntry is one host's token as credentials/github.json keeps it.
// It never leaves this file: a reply is a trackerView.
type trackerEntry struct {
	Host        string           `json:"host"`
	Token       keptToken        `json:"token"`
	Fingerprint string           `json:"fingerprint"`
	Login       string           `json:"login"`
	AccountID   tracker.RemoteID `json:"accountId"`
	Scopes      []string         `json:"scopes,omitempty"`
	SavedAt     time.Time        `json:"savedAt"`
}

// trackerFile is credentials/github.json.
type trackerFile struct {
	GitHub []trackerEntry `json:"github"`
}

func (f trackerFile) entry(host string) (trackerEntry, bool) {
	i := slices.IndexFunc(f.GitHub, func(e trackerEntry) bool { return e.Host == host })
	if i < 0 {
		return trackerEntry{}, false
	}
	return f.GitHub[i], true
}

// trackerSeen is what GitHub last said of the token in use for a host, by
// its fingerprint, since the server started (record).
type trackerSeen struct {
	fingerprint, state string
	at                 time.Time
}

// trackerKeep is the server's part of the tokens: the lock every change
// of the file is made under (the plan's trackerMu), each host's
// generation, the hosts being checked, what GitHub last said, and the
// adapter.
type trackerKeep struct {
	cfg *config.Config
	st  *store.Store
	log *slog.Logger
	// mu is held over a change's read, its change and its rename, and
	// guards gen, checking and seen. A check with GitHub is never made
	// under it.
	mu       sync.Mutex
	gen      map[string]uint64
	checking map[string]int
	seen     map[string]trackerSeen
	gh       *github.Adapter
	now      func() time.Time
	// reached counts the file's reads and credentialFor's calls, and
	// afterLook runs between a read's look at the file and its open: the
	// tests' seams, unset in the product.
	reached   atomic.Uint64
	afterLook func()
}

func newTrackers(cfg *config.Config, st *store.Store, log *slog.Logger) *trackerKeep {
	k := &trackerKeep{cfg: cfg, st: st, log: log, gen: map[string]uint64{}, checking: map[string]int{}, seen: map[string]trackerSeen{}, now: time.Now}
	k.gh = k.adapter(nil)
	return k
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

// errNoTrackerStore: a server without a data directory keeps no token.
var errNoTrackerStore = errors.New("this server has no data directory to keep a token in")

// read is the file, read whole through the directory as a root: the
// directory and the file must be the server's user's and open to no one
// else, the file a regular one (a link is refused), at most
// maxTrackerFile bytes, decoded strictly, each host's entry once. No file
// is no entry. A read never waits on what is at the file's name (readOnce),
// and one that crosses a save's rename reads the new file.
func (k *trackerKeep) read() (trackerFile, error) {
	k.reached.Add(1)
	var f trackerFile
	if k.st == nil {
		return f, nil
	}
	dir := filepath.Join(k.st.Dir(), trackerDir)
	dfi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if !dfi.IsDir() {
		return f, fmt.Errorf("%s is not a directory", trackerDir)
	}
	if err := private(trackerDir, dfi); err != nil {
		return f, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return f, err
	}
	defer root.Close()
	name := trackerDir + "/" + githubFile
	var b []byte
	for try := 1; ; try++ {
		var found, again bool
		b, found, again, err = k.readOnce(root, name)
		if again && try < trackerReadTries {
			continue
		}
		if err != nil || !found {
			return f, err
		}
		break
	}
	if err := store.DecodeStrict(b, &f); err != nil {
		// The decoder's words may quote the file: only its name is said.
		return trackerFile{}, fmt.Errorf("%s cannot be read as a token file", name)
	}
	for i, e := range f.GitHub {
		if slices.ContainsFunc(f.GitHub[:i], func(x trackerEntry) bool { return x.Host == e.Host }) {
			return trackerFile{}, fmt.Errorf("%s holds a host's entry twice", name)
		}
	}
	return f, nil
}

// readOnce looks at the file, refuses at once what is not a regular file,
// opens it so that the open cannot wait on what the name has become since
// (non-blocking: a named pipe answers at once), and reads it when the file
// opened is a regular, private file and the one looked at. again: it was
// another such file, a save's rename between the look and the open.
func (k *trackerKeep) readOnce(root *os.Root, name string) (b []byte, found, again bool, err error) {
	lfi, err := root.Lstat(githubFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, false, false, fmt.Errorf("%s is not a regular file", name)
	}
	if k.afterLook != nil {
		k.afterLook()
	}
	file, err := root.OpenFile(githubFile, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return nil, false, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, false, fmt.Errorf("%s is not a regular file", name)
	}
	if err := private(name, fi); err != nil {
		return nil, false, false, err
	}
	if !os.SameFile(lfi, fi) {
		return nil, false, true, fmt.Errorf("%s was replaced while it was read: try again", name)
	}
	b, err = io.ReadAll(io.LimitReader(file, maxTrackerFile+1))
	if err != nil {
		return nil, false, false, err
	}
	if len(b) > maxTrackerFile {
		return nil, false, false, fmt.Errorf("%s is more than %d bytes", name, maxTrackerFile)
	}
	return b, true, false, nil
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

// errTrackerFileFull is write's refusal of a file read would refuse.
var errTrackerFileFull = fmt.Errorf("%s/%s would be more than %d bytes", trackerDir, githubFile, maxTrackerFile)

// write puts f in place of the file: the directory made 0700 and held to
// private, the file by the store's temp file of mode 0600 and rename; one
// past read's bound is refused and the old file left whole. The caller
// holds k.mu.
func (k *trackerKeep) write(f trackerFile) error {
	if k.st == nil {
		return errNoTrackerStore
	}
	sub, err := k.st.Sub(trackerDir)
	if err != nil {
		return err
	}
	dfi, err := os.Lstat(sub.Dir())
	if err != nil {
		return err
	}
	if err := private(trackerDir, dfi); err != nil {
		return err
	}
	slices.SortFunc(f.GitHub, func(a, b trackerEntry) int { return strings.Compare(a.Host, b.Host) })
	if b, err := store.Encode(f); err != nil || len(b) > maxTrackerFile {
		return cmp.Or(err, errTrackerFileFull)
	}
	return sub.Save(githubFile, f)
}

// errNoToken is credentialFor's answer for a host with no token.
func errNoToken(host string) *apiError {
	return newAPIError(http.StatusPreconditionFailed, "no_token", "no GitHub token is saved for "+host+": save one in Settings")
}

// credentialFor is the one place every tracker route gets its credential:
// the token for ref's host, the environment's when it gives that host,
// else the file's, read now (a remove takes effect at the next call),
// wrapped at once in a tracker.Credential. 412 no_token without one. It
// takes the whole ref so that a later credential source chooses inside
// it and no route changes.
func (s *Server) credentialFor(_ context.Context, ref tracker.Ref) (tracker.Credential, error) {
	k := s.trackers
	k.reached.Add(1)
	if ref.System != tracker.GitHub {
		return tracker.Credential{}, newAPIError(http.StatusBadRequest, "not_a_tracker_url", "no tracker this server reads")
	}
	if k.cfg.GitHubToken.Set() && ref.Host == k.cfg.GitHubHost {
		return tracker.Token(ref.Host, k.cfg.GitHubToken.Reveal(), k.cfg.TrackerLoopback)
	}
	f, err := k.read()
	if err != nil {
		return tracker.Credential{}, newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
	}
	e, ok := f.entry(ref.Host)
	if !ok {
		return tracker.Credential{}, errNoToken(ref.Host)
	}
	return e.Token.credential(ref.Host, k.cfg.TrackerLoopback)
}

// record notes what GitHub said of the token of fingerprint fp for host
// (ok, or refused: a token that stopped working) when it is the token in
// use there, the environment's for its host, else the one f keeps. A check
// of any other token changes nothing that is shown. The caller holds k.mu.
func (k *trackerKeep) record(f trackerFile, host, fp, state string) {
	inUse := ""
	if c, ok := k.environment(); ok && c.Host() == host {
		inUse = c.Fingerprint()
	} else if e, ok := f.entry(host); ok {
		inUse = e.Fingerprint
	}
	if inUse != "" && inUse == fp {
		k.seen[host] = trackerSeen{fingerprint: fp, state: state, at: k.now().UTC()}
	}
}

// environment is the credential of the environment's token, if it gives
// one.
func (k *trackerKeep) environment() (tracker.Credential, bool) {
	if !k.cfg.GitHubToken.Set() {
		return tracker.Credential{}, false
	}
	c, err := tracker.Token(k.cfg.GitHubHost, k.cfg.GitHubToken.Reveal(), k.cfg.TrackerLoopback)
	return c, err == nil
}

// trackerView is a token as a person is shown it: never the token.
type trackerView struct {
	Host        string           `json:"host"`
	Login       string           `json:"login,omitempty"`
	AccountID   tracker.RemoteID `json:"accountId,omitempty"`
	Fingerprint string           `json:"fingerprint"`
	Scopes      []string         `json:"scopes"`
	SavedAt     *time.Time       `json:"savedAt,omitempty"`
	// Source is settings (the file) or environment.
	Source string `json:"source"`
	// State is ok, refused (GitHub refused it at CheckedAt: save a new
	// one), unchecked (the environment's, before any call) or unused (the
	// file's for the host the environment gives a token for: the
	// environment's is used).
	State     string     `json:"state"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

type trackersReply struct {
	GitHub []trackerView `json:"github"`
}

// view is e as shown, with what GitHub said of it since.
func (k *trackerKeep) view(e trackerEntry) trackerView {
	saved := e.SavedAt
	v := trackerView{Host: e.Host, Login: e.Login, AccountID: e.AccountID, Fingerprint: e.Fingerprint, Scopes: append([]string{}, e.Scopes...), SavedAt: &saved, Source: "settings", State: "ok", CheckedAt: &saved}
	k.seenOf(&v)
	return v
}

func (k *trackerKeep) seenOf(v *trackerView) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s, ok := k.seen[v.Host]; ok && s.fingerprint == v.Fingerprint {
		at := s.at
		v.State, v.CheckedAt = s.state, &at
	}
}

// list is every token kept or given, as shown: the file's by host, then
// the environment's. The file's for the environment's host is unused.
func (k *trackerKeep) list() ([]trackerView, error) {
	f, err := k.read()
	if err != nil {
		return nil, err
	}
	out := []trackerView{}
	env, given := k.environment()
	for _, e := range f.GitHub {
		v := k.view(e)
		if given && e.Host == env.Host() {
			v.State = "unused"
		}
		out = append(out, v)
	}
	if given {
		v := trackerView{Host: env.Host(), Fingerprint: env.Fingerprint(), Scopes: []string{}, Source: "environment", State: "unchecked"}
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
	views, err := s.trackers.list()
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

// trackerRemoveBody is a remove's body.
type trackerRemoveBody struct {
	Host string `json:"host,omitempty"`
}

// trackerBodyWords is the answer to a body that does not decode into the
// route's members: the server's own words, never the decoder's.
const trackerBodyWords = "the body is one JSON object of the route's members alone: host and token for a save, host for a remove"

// trackerBody decodes a save's or a remove's body and holds its host to
// the tracker's rule; false once it has answered.
func (s *Server) trackerBody(w http.ResponseWriter, r *http.Request, v any, host *string) bool {
	if err := decodeJSONLimit(w, r, v, maxTrackerBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", errBodyTooLarge.Error())
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", trackerBodyWords)
		}
		return false
	}
	if *host == "" {
		*host = tracker.GitHubHost
	}
	if err := tracker.CheckHost(*host, s.cfg.TrackerLoopback); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_host", "the host is github.com or a GitHub Enterprise Server's name, in lower case, https only")
		return false
	}
	return true
}

// environmentWords says the environment gives host's token.
func environmentWords(host string) string {
	return "the token for " + host + " is given in the server's environment (CONDUCTOR_GITHUB_TOKEN): change it there"
}

// handleTrackerSave checks a token with GitHub (Whoami), then keeps it.
// The check runs before the lock; the save then writes only if the host's
// entry is still the one it saw before the check (its generation), so a
// check that finishes after a remove never brings the removed token back.
// A token equal to the one kept (share.Equal) changes nothing. A token
// GitHub refuses is kept nowhere, and the one already kept stays.
func (s *Server) handleTrackerSave(w http.ResponseWriter, r *http.Request) {
	k := s.trackers
	var b trackerSaveBody
	if !s.trackerBody(w, r, &b, &b.Host) {
		return
	}
	host := b.Host
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
	k.mu.Lock()
	gen := k.gen[host]
	k.checking[host]++
	k.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), trackerCheckTimeout)
	acct, err := k.gh.Whoami(ctx, cred)
	cancel()
	k.mu.Lock()
	if k.checking[host]--; k.checking[host] == 0 {
		delete(k.checking, host)
	}
	k.mu.Unlock()
	if errors.Is(err, tracker.ErrUnauthorized) {
		k.mu.Lock()
		if f, rerr := k.read(); rerr == nil {
			k.record(f, host, cred.Fingerprint(), "refused")
		}
		k.mu.Unlock()
	}
	if err != nil {
		e := trackerRefusal(err, host)
		var rl *tracker.ErrRateLimited
		if errors.As(err, &rl) {
			wait := 60
			if !rl.Reset.IsZero() {
				wait = max(1, int(time.Until(rl.Reset).Seconds()+0.999))
			}
			w.Header().Set("Retry-After", strconv.Itoa(wait))
		}
		s.log.Info("tracker token not saved", "route", r.Pattern, "host", host, "status", e.status, "code", e.Code, "credential", cred)
		writeAPIError(w, e)
		return
	}
	e := trackerEntry{Host: host, Token: keptToken(b.Token), Fingerprint: cred.Fingerprint(), Login: acct.Login, AccountID: acct.ID, Scopes: acct.Scopes, SavedAt: k.now().UTC().Truncate(time.Second)}
	kept, unchanged, refused := k.keep(gen, e, b.Token)
	if refused != nil {
		lvl := slog.LevelInfo
		if refused.status >= 500 {
			lvl = slog.LevelError
		}
		s.log.Log(r.Context(), lvl, "tracker token not saved", "route", r.Pattern, "host", host, "status", refused.status, "code", refused.Code, "credential", cred)
		writeAPIError(w, refused)
		return
	}
	s.log.Info("tracker token saved", "route", r.Pattern, "host", host, "status", http.StatusOK, "credential", cred, "login", acct.Login, "unchanged", unchanged)
	reply := map[string]any{"tracker": k.view(kept)}
	if unchanged {
		reply["unchanged"] = true
	}
	writeJSON(w, http.StatusOK, reply)
}

// keep writes e under k.mu if its host's generation is still gen: the
// entry kept, and whether it was kept already (the same token, by
// share.Equal: nothing written, GitHub's acceptance recorded), or the
// refusal, which records nothing. A token kept in place of another starts
// its own record.
func (k *trackerKeep) keep(gen uint64, e trackerEntry, presented string) (trackerEntry, bool, *apiError) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gen[e.Host] != gen {
		return e, false, newAPIError(http.StatusConflict, "changed", "the token for "+e.Host+" was changed or removed while this one was checked: save it again")
	}
	f, err := k.read()
	if err != nil {
		return e, false, newAPIError(http.StatusInternalServerError, "credentials_unusable", err.Error())
	}
	if kept, ok := f.entry(e.Host); ok && kept.Token.same(presented) {
		k.record(f, e.Host, kept.Fingerprint, "ok")
		return kept, true, nil
	}
	i := slices.IndexFunc(f.GitHub, func(x trackerEntry) bool { return x.Host == e.Host })
	switch {
	case i >= 0:
		f.GitHub[i] = e
	case len(f.GitHub) >= maxTrackerHosts:
		return e, false, newAPIError(http.StatusConflict, "too_many_hosts", fmt.Sprintf("tokens are kept for at most %d hosts: remove one first", maxTrackerHosts))
	default:
		f.GitHub = append(f.GitHub, e)
	}
	if err := k.write(f); err != nil {
		k.log.Error("tracker token not written", "host", e.Host, "err", err)
		return e, false, newAPIError(http.StatusInternalServerError, "store_failed", "the token could not be kept: the server's log says why")
	}
	k.gen[e.Host]++
	delete(k.seen, e.Host)
	return e, false, nil
}

// handleTrackerRemove drops a host's entry. A call already under way
// finishes with the token it read; every later one is 412 no_token. It
// stops Conductor's use of the token only: the token stays valid on
// GitHub until the person revokes it there. For the host the environment
// gives a token for, the file's entry goes as well, and the answer says
// that the environment's token is still the one in use.
func (s *Server) handleTrackerRemove(w http.ResponseWriter, r *http.Request) {
	k := s.trackers
	var b trackerRemoveBody
	if !s.trackerBody(w, r, &b, &b.Host) {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gen[b.Host]++
	f, err := k.read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credentials_unusable", err.Error())
		return
	}
	n := len(f.GitHub)
	f.GitHub = slices.DeleteFunc(f.GitHub, func(e trackerEntry) bool { return e.Host == b.Host })
	removed := len(f.GitHub) < n
	env, given := k.environment()
	given = given && env.Host() == b.Host
	if removed {
		if err := k.write(f); err != nil {
			s.log.Error("tracker token not removed", "route", r.Pattern, "host", b.Host, "status", http.StatusInternalServerError, "err", err)
			writeError(w, http.StatusInternalServerError, "store_failed", "the token could not be removed: the server's log says why")
			return
		}
		if !given {
			delete(k.seen, b.Host)
		}
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
