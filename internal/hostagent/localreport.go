package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/netutil"

	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// The channel on the host's own machine that its agent's hooks report to
// (docs/protocol.md, The host's socket): a unix-domain socket in a folder
// only this user can enter, made before the agent's process starts, its path
// handed to the process in notify.EnvHostSocket. It takes the attention
// route's request with the session's agent token, which the host minted,
// applies it to the host's session as the server's route applies a report to
// a session of its own, and the host tells the server through the frames it
// already sends (onLocalChange), now or at its next connection.
const (
	// reportDirPrefix names the socket's folders, one per host, in the
	// first base of reportBases that takes one.
	reportDirPrefix = "conductor-host-"
	// reportSocketName is the socket's name in its folder.
	reportSocketName = "report.sock"
	// maxReportBody bounds a report's body, as the server's routes bound
	// theirs (64 KiB); maxReportMessage, maxReportOptions are the attention
	// route's bounds of the message and the choices.
	maxReportBody    = 64 << 10
	maxReportMessage = 4096
	maxReportOptions = 32
	// maxReportConns bounds the connections served at once: more wait in
	// the listener's queue.
	maxReportConns = 8
	// maxReportHeader bounds a request's header.
	maxReportHeader = 8 << 10
	// reportWait bounds a report's wait for the host's session, which is
	// made once its process has started: as the server's route waits for
	// a launch under way (10 s).
	reportWait = 10 * time.Second
	// reportHeaderWait and reportBodyWait bound a connection that sends
	// slowly; a report's answer is written within reportWait and
	// reportAnswerWait after its body.
	reportHeaderWait = 5 * time.Second
	reportBodyWait   = 10 * time.Second
	reportAnswerWait = 10 * time.Second
	// staleReportSocket is how old a socket nothing listens on must be
	// before a host's start removes it with its folder: one a host has
	// just bound, not listening yet, is younger.
	staleReportSocket = time.Minute
)

// checkReportSocket checks the socket a host made before it is named to
// the agent (notify.CheckHostSocket); tests replace it.
var checkReportSocket = notify.CheckHostSocket

// reportListener is the host's socket and what serves it.
type reportListener struct {
	path, dir string
	token     string
	log       *slog.Logger
	srv       *http.Server
	// wait replaces reportWait in tests; waiting, when set (tests), is
	// told each report that waits for the session, as its wait begins.
	wait    time.Duration
	waiting func()
	// agentSession, when set, is told the agent's own session id each
	// applied report names, and whether the report was of a turn, in the
	// order the reports were applied (agent.noteAgentSession).
	agentSession func(id string, turn bool)

	mu    sync.Mutex
	local *session.Local
	// ready is closed once the session is set (setSession); closed once
	// the listener is closed.
	ready     chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	served    chan struct{}
}

// reportBases are the folders a host's socket folder may be made in, in
// order: the user's runtime folder when it is one only the user can enter,
// the temporary folder, then /tmp, for a temporary folder whose path leaves
// no room for the socket's.
func reportBases() []string {
	var out []string
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) && notify.CheckHostSocketDir(d) == nil {
		out = append(out, d)
	}
	return append(out, os.TempDir(), lastReportBase(runtime.GOOS))
}

// lastReportBase is the shared temporary folder of goos by its own path:
// on macOS /tmp is a link to /private/tmp, and a link is never a base.
func lastReportBase(goos string) string {
	if goos == "darwin" {
		return "/private/tmp"
	}
	return "/tmp"
}

// listenReports makes the host's socket in a folder of its own under the
// first of bases that takes one, removing first what a host that ended
// without closing its own left there (sweepReportDirs), and serves it:
// reports wait for setSession. token is the session's agent token; an empty
// one is refused, as nothing could be checked against it.
func listenReports(bases []string, token string, log *slog.Logger) (*reportListener, error) {
	if token == "" {
		return nil, errors.New("no agent token to check reports against")
	}
	var errs []error
	for _, base := range bases {
		l, err := listenReportsIn(base, token, log)
		if err == nil {
			return l, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func listenReportsIn(base, token string, log *slog.Logger) (*reportListener, error) {
	if err := checkReportBase(base); err != nil {
		return nil, err
	}
	// The longest name MkdirTemp makes: the prefix and ten digits.
	if len(filepath.Join(base, reportDirPrefix+"0123456789", reportSocketName)) > notify.MaxHostSocketPath {
		return nil, fmt.Errorf("%s: the socket's path would be longer than %d bytes", base, notify.MaxHostSocketPath)
	}
	sweepReportDirs(base, log)
	dir, err := os.MkdirTemp(base, reportDirPrefix)
	if err != nil {
		return nil, err
	}
	// MkdirTemp made it 0700 less the umask: 0700 exactly, then checked.
	if err := os.Chmod(dir, 0o700); err != nil {
		os.Remove(dir)
		return nil, err
	}
	if err := notify.CheckHostSocketDir(dir); err != nil {
		os.Remove(dir)
		return nil, err
	}
	path := filepath.Join(dir, reportSocketName)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	// The folder is what keeps others out (macOS does not check a socket's
	// mode); the socket's own mode says so too where it is checked.
	err = os.Chmod(path, 0o600)
	if err == nil {
		err = checkReportSocket(path)
	}
	if err != nil {
		ln.Close()
		os.Remove(path)
		os.Remove(dir)
		return nil, err
	}
	l := &reportListener{path: path, dir: dir, token: token, log: log, wait: reportWait, ready: make(chan struct{}), closed: make(chan struct{}), served: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+notify.HostReportPath, l.serveAttention)
	mux.HandleFunc("POST "+notify.HostEventsPath, l.serveEvent)
	l.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: reportHeaderWait,
		ReadTimeout:       reportBodyWait,
		WriteTimeout:      reportBodyWait + reportWait + reportAnswerWait,
		IdleTimeout:       reportHeaderWait,
		MaxHeaderBytes:    maxReportHeader,
		// A client's mistakes are not the host's to log.
		ErrorLog: newDiscardLogger(),
	}
	go func() {
		defer close(l.served)
		_ = l.srv.Serve(netutil.LimitListener(ln, maxReportConns))
	}()
	return l, nil
}

// startReports starts the host's socket for its agent's reports, checked
// against the session's agent token, and names it in env, the variables
// the agent's process is given. A socket that cannot be made is logged and
// named nowhere: the agent's sender then reports to the server, as it does
// under a host of an earlier release. The listener it returns, nil then, is
// handed the session once it is made (setSession) and closed as the host
// ends (Close); both take a nil one.
func (a *agent) startReports(env map[string]string) *reportListener {
	l, err := listenReports(reportBases(), a.agentToken, a.log)
	if err != nil {
		a.log.Warn("the agent's reports go to the server alone: the host's socket could not be made", "err", err)
		return nil
	}
	l.agentSession = a.noteAgentSession
	env[notify.EnvHostSocket] = l.path
	env[notify.EnvHostEvents] = "1"
	return l
}

// checkReportBase says whether base may hold the host's socket folder: a
// folder (not a link to one) of this user's own or root's, which no one
// else can write, or only with the sticky bit set (a shared temporary
// folder), so that no one else can rename or replace the folder the host
// makes in it.
func checkReportBase(base string) error {
	fi, err := os.Lstat(base)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: not a folder", base)
	}
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%s: others can write it, and its sticky bit is not set", base)
	}
	if uid, ok := ownerOf(fi); !ok || (uid != os.Getuid() && uid != 0) {
		return fmt.Errorf("%s: neither this user's nor root's", base)
	}
	return nil
}

// ownerOf is the user id that owns what fi describes.
func ownerOf(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// sweepReportDirs removes from base the socket folders of hosts that ended
// without removing their own (a host killed, a machine that lost power): a
// folder of this user's own, only this user's to enter (the sender's own
// check), holding a socket of this user's own older than staleReportSocket
// on which nothing listens. Anything else is left as it is.
func sweepReportDirs(base string, log *slog.Logger) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), reportDirPrefix) {
			continue
		}
		dir := filepath.Join(base, e.Name())
		path := filepath.Join(dir, reportSocketName)
		if len(path) > notify.MaxHostSocketPath || notify.CheckHostSocket(path) != nil {
			continue
		}
		fi, err := os.Lstat(path)
		if err != nil || time.Since(fi.ModTime()) < staleReportSocket {
			continue
		}
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			c.Close()
			continue
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			continue
		}
		if os.Remove(path) == nil && os.Remove(dir) == nil {
			log.Debug("removed the socket a host left", "dir", dir)
		}
	}
}

// setSession hands the reports the host's session: those waiting for it
// are applied, in the order the session takes them.
func (l *reportListener) setSession(local *session.Local) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.local != nil {
		return
	}
	l.local = local
	close(l.ready)
}

// session is the host's session, waited for within l.wait; nil when it was
// not set in time, the listener closed or the report's request ended.
func (l *reportListener) session(ctx context.Context) *session.Local {
	select {
	case <-l.ready:
	default:
		if l.waiting != nil {
			l.waiting()
		}
	}
	t := time.NewTimer(l.wait)
	defer t.Stop()
	select {
	case <-l.ready:
	case <-l.closed:
	case <-ctx.Done():
	case <-t.C:
	}
	// A session that is set is the report's, whichever of the waits
	// ended first: a listener closing as the session is set refuses
	// nothing the session could take.
	select {
	case <-l.ready:
	default:
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.local
}

// Close stops taking reports, ends those under way and removes the socket
// and its folder.
func (l *reportListener) Close() {
	if l == nil {
		return
	}
	l.closeOnce.Do(func() {
		close(l.closed)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := l.srv.Shutdown(ctx); err != nil {
			l.srv.Close()
		}
		<-l.served
		os.Remove(l.path)
		os.Remove(l.dir)
	})
}

// localAttention is the attention route's request (internal/api
// attentionRequest), field for field, decoded as strictly. AgentSession
// and Turn are taken and bounded as there; the host keeps neither: the
// agent's own session id is the server's to keep, and reaches it on the
// server's route alone.
type localAttention struct {
	State        string           `json:"state"`
	Message      string           `json:"message"`
	Kind         string           `json:"kind"`
	Options      []session.Option `json:"options"`
	AgentSession string           `json:"agentSession"`
	Turn         bool             `json:"turn"`
	Ask          bool             `json:"ask"`
	Plan         string           `json:"plan"`
	TurnID       string           `json:"turnId"`
}

// serveAttention takes one attention report: the session's own agent token
// first (401 for any other, nothing read), the body (at most maxReportBody,
// one JSON object of the route's fields and nothing after it), the
// session's bounds, then the session itself, waited for, and the report
// applied as the server's route applies one to a session of its own,
// spending the session's bucket (429 with none left). It answers as that
// route does: 200 {attention}, or {error:{code,message}}.
func (l *reportListener) serveAttention(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(w, r) {
		return
	}
	var req localAttention
	if err := decodeReport(w, r, &req); err != nil {
		writeReportError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state, ok := reportState(req.State)
	if !ok {
		writeReportError(w, http.StatusBadRequest, "invalid_state", "state must be needs_input, working, done or clear")
		return
	}
	switch {
	case len(req.AgentSession) > session.MaxAgentSessionID:
		writeReportError(w, http.StatusBadRequest, "invalid_request", "agentSession is longer than 128 bytes")
		return
	case len(req.Plan) > proto.MaxPlanPath:
		writeReportError(w, http.StatusBadRequest, "invalid_request", "plan is longer than 1024 bytes")
		return
	case !checkAttention(w, req.Message, req.Kind, req.Options):
		return
	}
	local := l.sessionOrRefuse(w, r)
	if local == nil {
		return
	}
	if !applyAttention(w, local, state, req.Message, req.Kind, req.Options, req.Ask, req.Plan, req.TurnID) {
		return
	}
	// As the server's route keeps the id once the report is applied.
	if req.AgentSession != "" && l.agentSession != nil {
		l.agentSession(req.AgentSession, req.Turn)
	}
	writeReportJSON(w, http.StatusOK, map[string]any{"attention": local.Info().Attention})
}

// localEvent is the events route's request (internal/api eventRequest),
// field for field, decoded as strictly.
type localEvent struct {
	Type    string           `json:"type"`
	Message string           `json:"message"`
	URL     string           `json:"url"`
	To      string           `json:"to"`
	Tool    string           `json:"tool"`
	Kind    string           `json:"kind"`
	Options []session.Option `json:"options"`
	Op      string           `json:"op"`
	Path    string           `json:"path"`
	Ask     bool             `json:"ask"`
}

// eventActor is the display name on an entry an agent reported: the agent
// said it, whoever holds the token (the events route's own).
const eventActor = "agent"

// serveEvent takes one event as the events route takes one: the session's
// own agent token first, the body as strictly and within the same bound as
// an attention report's, then the session, waited for. An attention word
// (needs_input, working, done, clear) is applied as the attention route
// applies it, with no agent session, plan or turn; one of the agent's event
// types (progress, artifact, handoff, tool_use, tool_denied, error, file)
// is recorded in the host's session, cleaned as the route cleans it,
// spending the session's bucket (429 with none left), and reaches the
// server as any entry the session records (sendActivity), now or, kept,
// when the host's connection is back. It answers as the route does: 202
// {accepted: true}, or {error: {code, message}}.
func (l *reportListener) serveEvent(w http.ResponseWriter, r *http.Request) {
	if !l.authorized(w, r) {
		return
	}
	var req localEvent
	if err := decodeReport(w, r, &req); err != nil {
		writeReportError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state, isAttention := reportState(req.Type)
	if !isAttention && !agentEventType(req.Type) {
		writeReportError(w, http.StatusBadRequest, "invalid_type", "type must be progress, artifact, handoff, tool_use, tool_denied, error, file, needs_input, working, done or clear")
		return
	}
	if isAttention && !checkAttention(w, req.Message, req.Kind, req.Options) {
		return
	}
	entry := session.CleanEntry(session.ActivityEntry{Type: req.Type, Message: req.Message, URL: req.URL, To: req.To, Tool: req.Tool, Op: req.Op, Path: req.Path, ByName: eventActor})
	if !isAttention && req.Type == session.ActivityFile && (entry.Path == "" || entry.Op == "") {
		writeReportError(w, http.StatusBadRequest, "invalid_request", "a file event names its op (read, edit, write or delete) and its path")
		return
	}
	local := l.sessionOrRefuse(w, r)
	if local == nil {
		return
	}
	if isAttention {
		if !applyAttention(w, local, state, req.Message, req.Kind, req.Options, req.Ask, "", "") {
			return
		}
	} else if !local.Record(entry) {
		writeReportError(w, http.StatusTooManyRequests, "rate_limited", "too many reports for this session")
		return
	}
	writeReportJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// agentEventType says whether t is one of the agent's event types the
// events route takes (internal/api eventType): the entries a session
// records itself (join, leave, input, link, status, attention) are not,
// for they skip its bucket.
func agentEventType(t string) bool {
	switch t {
	case session.ActivityProgress, session.ActivityArtifact, session.ActivityHandoff,
		session.ActivityToolUse, session.ActivityToolDenied, session.ActivityError, session.ActivityFile:
		return true
	}
	return false
}

// authorized says whether r presents the session's own agent token, in
// its Authorization header as a bearer token; it answers 401 otherwise,
// before anything of the body is read.
func (l *reportListener) authorized(w http.ResponseWriter, r *http.Request) bool {
	tok, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer || !share.Equal(strings.TrimSpace(tok), l.token) {
		writeReportError(w, http.StatusUnauthorized, "unauthorized", "the session's agent token required")
		return false
	}
	return true
}

// checkAttention holds an attention report to the attention route's
// bounds: its message, its kind and its choices; it answers 400 past them.
func checkAttention(w http.ResponseWriter, message, kind string, options []session.Option) bool {
	switch {
	case len(message) > maxReportMessage:
		writeReportError(w, http.StatusBadRequest, "invalid_request", "message too long")
	case !session.ValidKind(kind):
		writeReportError(w, http.StatusBadRequest, "invalid_kind", "kind must be permission, prompt, done or empty")
	case len(options) > maxReportOptions:
		writeReportError(w, http.StatusBadRequest, "invalid_request", "too many options")
	default:
		return true
	}
	return false
}

// sessionOrRefuse is the host's session, waited for (session), or nil
// with 503 when it was not made in time and 409 when it has ended.
func (l *reportListener) sessionOrRefuse(w http.ResponseWriter, r *http.Request) *session.Local {
	local := l.session(r.Context())
	if local == nil {
		writeReportError(w, http.StatusServiceUnavailable, "starting", "the host's session has not started")
		return nil
	}
	if local.Info().Status.Ended() {
		writeReportError(w, http.StatusConflict, "session_ended", "the session has ended")
		return nil
	}
	return local
}

// applyAttention applies an attention report to local as the server's
// route applies one to a session of its own, spending the session's
// bucket: the agent's own question with ask (needs_input alone), the turn
// it names, the plan its question is about (needs_input, not ask), or the
// state alone. It answers 429 with no token left, and false then.
func applyAttention(w http.ResponseWriter, local *session.Local, state session.AttentionState, message, kind string, options []session.Option, ask bool, plan, turn string) bool {
	ask = ask && state == session.AttentionNeedsInput
	if state != session.AttentionNeedsInput || ask {
		plan = ""
	}
	if !proto.ValidTurnID(turn) {
		turn = ""
	}
	var err error
	switch {
	case ask:
		err = local.TryAsk(message, session.SourceAPI, kind, options)
	case turn != "":
		err = local.TrySetAttentionTurn(state, message, session.SourceAPI, kind, options, plan, turn)
	case plan != "":
		err = local.TrySetAttentionPlan(state, message, session.SourceAPI, kind, options, plan)
	default:
		err = local.TrySetAttentionFull(state, message, session.SourceAPI, kind, options)
	}
	if err != nil {
		if errors.Is(err, session.ErrRateLimited) {
			writeReportError(w, http.StatusTooManyRequests, "rate_limited", "too many reports for this session")
			return false
		}
		writeReportError(w, http.StatusInternalServerError, "internal", "the report could not be applied")
		return false
	}
	return true
}

// reportState is the attention route's reading of a state's word: clear is
// no state at all.
func reportState(word string) (session.AttentionState, bool) {
	if word == "clear" {
		return session.AttentionNone, true
	}
	state := session.AttentionState(word)
	return state, word != "" && state.Valid()
}

// decodeReport reads one JSON object of v's fields from the body, at most
// maxReportBody bytes, with nothing after it.
func decodeReport(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxReportBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var mbe *http.MaxBytesError
	if err := dec.Decode(v); err != nil {
		if errors.As(err, &mbe) {
			return errors.New("request body too large")
		}
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if errors.As(err, &mbe) {
			return errors.New("request body too large")
		}
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func writeReportError(w http.ResponseWriter, status int, code, message string) {
	writeReportJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeReportJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newDiscardLogger() *log.Logger { return log.New(io.Discard, "", 0) }
