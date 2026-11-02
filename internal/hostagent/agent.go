// Package hostagent implements `conductor host`: it runs a command in a local
// PTY, registers the session with a conductor server over a control
// WebSocket, and serves browser viewers over WebRTC data channels or, as a
// fallback, through the server relay.
package hostagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// Options configure a host run.
type Options struct {
	// ServerURL is the server's address alone, http(s):// or ws(s)://, with
	// a path when it serves under one: Run and Uplink.Publish refuse one with
	// user information, a query or a fragment (controlURL); the token is
	// Token.
	ServerURL string
	Token     string
	Name      string // session name shown in the UI
	HostName  string // machine label
	AgentID   string
	Argv      []string
	Dir       string
	RelayOnly bool
	// Insecure allows a ServerURL that is plain (http or ws) on another
	// machine (config.PlainOffMachine), and a redirect to one: what passes
	// between the host and that server then travels in the clear and
	// unverified, and Run logs a warning saying so. Without it, Run refuses
	// such a server before anything is dialed, and the dial refuses such a
	// redirect (PlainServerError). A server on this machine needs nothing.
	Insecure bool
	// ServerIsOwners says the server this host reports to is the owner's
	// own, on this machine (conductor host --server-is-mine, for a server on
	// localhost): a viewer it sends with no link is then one of the owner's
	// own windows, offered the editor's Neovim, and the connection is held
	// to this machine (ownersServerClient). Off by default: a loopback
	// address alone could be a forward to someone else's server. Never so
	// for a switchyard, whose workbench is its operator's: the uplink leaves
	// it off.
	ServerIsOwners bool
	// LocalAttach connects Stdin/Stdout to the PTY as a controller.
	LocalAttach bool
	Stdin       *os.File
	Stdout      *os.File
	// ICEServers override the servers handed out by the conductor server.
	ICEServers []proto.ICEServer
	// ICE is how the peers gather: one UDP port and an address to advertise
	// (a forwarder's), or pion's defaults.
	ICE             ICE
	ScrollbackBytes int
	MaxViewers      int
	FileView        string
	// FileEdit is whether the control role may edit files through the
	// editor's Neovim on this machine: "control" (the default), "off".
	FileEdit string
	// FileDeny gives, for each file request, what no file read, save or
	// editor of the session may reach, even inside its working directory
	// (session.Options.FileDenyFunc): conductor host passes
	// config.LocalFileDenyFunc's, the data directory, the config file and the
	// catalog file of the server on this machine. Nil refuses nothing more.
	FileDeny func() []string
	Log      *slog.Logger
	// Pattern, when set, is matched against the last line of the terminal after
	// 500 ms without output; a match marks the session needs_input. See
	// session.Options.Pattern.
	Pattern *regexp.Regexp
	// Adapter names the hook adapter of the command (`conductor host --agent`).
	// When it is one with a launch route, the hook assets are written to
	// HooksDir and the command is started with the adapter's flags and
	// environment, as the server starts an agent whose signal is "hook". An
	// adapter without one (its agent reads hooks only from its own config)
	// and any other name change nothing and write nothing.
	Adapter string
	// HooksDir is where the host writes the hook assets; empty means
	// agents.HostHooksDir().
	HooksDir string
	// ReconnectMax bounds the reconnect backoff.
	ReconnectMax time.Duration
	// Registered is called once the first registration succeeds (tests, CLI
	// banner), and again when the server lost the session (a restarted
	// switchyard) and the host registered afresh under a new id.
	Registered func(sessionID, shareBaseURL string)
	// Link, view or control, has the server mint a share link to the
	// session once it is registered, over the host's own connection (`link`),
	// good for LinkTTL: how the owner shares a session hosted on a
	// switchyard, whose workbench token mints none. LinkMinted is told the
	// link, or why there is none. Each receive on LinkAgain mints another
	// the same way: the owner's way to a new one once the first expired or
	// could not be had (conductor host takes SIGUSR1 for it).
	Link       string
	LinkTTL    time.Duration
	LinkMinted func(proto.LinkCreated, error)
	LinkAgain  <-chan struct{}
	// Switchyard says the server is a switchyard, which someone else may
	// run, rather than the owner's own Conductor: it is told what it needs
	// to list the session and introduce its viewers, and no more. No chat
	// message and no activity entry goes to it, and an attention change goes
	// with its state, kind and source but without its words or its choices.
	// Its registration names the session, its agent, the last element of
	// its folder and the machine, and leaves out the command line, the OS
	// user and the git branch (registration). Its viewers get all of it
	// from the session itself, over their own connection. A stop it sends
	// never stops the process: it ends the Uplink's publication. An
	// attention change or an event it sends is not taken: the session's
	// agent reports to its own server, never to the switchyard, so only the
	// switchyard could send one, and an attention's choices are keys the
	// owner types with one click. Nor is it told the host takes routed
	// answers (answer_as) or what the session keeps, and an answer_as it
	// sends is dropped unread. The Uplink sets it; `conductor host`
	// publishes to the owner's own server, which shows the session in its
	// workbench, stops it and forwards its agent's reports, and leaves it off.
	Switchyard bool

	// transport, when set (tests), is the control connection's transport in
	// place of the standard one: a test's names dialed to its own servers.
	transport *http.Transport
}

// Result reports how the hosted process ended.
type Result struct {
	SessionID string
	ExitCode  int
}

// Run hosts opts.Argv until the process exits or ctx is cancelled.
func Run(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Argv) == 0 {
		return Result{}, errors.New("host: no command given")
	}
	// Before anything is written or dialed.
	if err := plainRefused(opts.ServerURL, opts.Insecure); err != nil {
		return Result{}, err
	}
	wsURL, err := controlURL(opts.ServerURL)
	if err != nil {
		return Result{}, err
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Insecure && config.PlainOffMachine(opts.ServerURL) {
		opts.Log.Warn("--insecure: the server is plain http and not localhost or a loopback address; what passes between it and this host travels in the clear and unverified", "server", serverHost(opts.ServerURL))
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = 30 * time.Second
	}
	if opts.HostName == "" {
		opts.HostName, _ = os.Hostname()
	}
	if opts.AgentID == "" {
		opts.AgentID = filepath.Base(opts.Argv[0])
	}
	// The flags are part of the command from the start, so the server lists
	// the command as it runs.
	var adapterEnv map[string]string
	opts.Argv, adapterEnv = injectHooks(opts)
	dir := opts.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}
	cols, rows := uint16(80), uint16(24)
	if opts.LocalAttach && opts.Stdin != nil {
		if c, r, err := terminalSize(opts.Stdin); err == nil {
			cols, rows = c, r
		}
	}
	// The agent token lets hooks inside the PTY report attention straight to
	// the server. Register first so the session ID is known before the
	// process starts and can be placed in its environment.
	agentToken, _ := share.NewToken()
	// An instance of its own, so a switchyard that restarts gives the session its id back.
	instance, _ := share.NewToken()
	a := &agent{opts: opts, wsURL: wsURL, dir: dir, cols: cols, rows: rows, peers: map[string]*peer{}, log: opts.Log, agentToken: agentToken, instance: instance, localID: "1"}
	a.activity = newActivityForwarder(a.sendActivity, a.sendChat, opts.Log)
	firstConn, registered, err := a.dialAndRegister(ctx)
	if err != nil {
		return Result{}, err
	}
	notifyURL := attentionURL(opts.ServerURL, registered.SessionID)
	// The binary the hooks run (the one the assets name when injectHooks
	// wrote them), for what the agent runs itself (the skill).
	bin, _ := agents.Binary()
	env := pty.Inject(registered.SessionID, notifyURL, agentToken, bin)
	for k, v := range adapterEnv {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	// The folder is opened once: the process starts in it and the
	// session's file reads and saves go through it.
	folder, err := session.OpenFolder(dir)
	if err != nil {
		a.dropConn(firstConn)
		firstConn.Close(websocket.StatusNormalClosure, "start failed")
		return Result{}, err
	}
	dirFile, err := folder.Dir()
	if err != nil {
		folder.Close()
		a.dropConn(firstConn)
		firstConn.Close(websocket.StatusNormalClosure, "start failed")
		return Result{}, err
	}
	procEnv := hostEnv(env)
	proc, err := pty.Start(pty.Spec{Argv: opts.Argv, Dir: dir, DirFile: dirFile, Env: procEnv, Cols: cols, Rows: rows})
	dirFile.Close()
	if err != nil {
		folder.Close()
		a.dropConn(firstConn)
		firstConn.Close(websocket.StatusNormalClosure, "start failed")
		return Result{}, err
	}
	info := session.Info{
		ID:        registered.SessionID,
		Name:      opts.Name,
		Kind:      session.KindHosted,
		AgentID:   opts.AgentID,
		Command:   opts.Argv,
		Cwd:       dir,
		Status:    session.StatusRunning,
		Cols:      cols,
		Rows:      rows,
		HostName:  opts.HostName,
		HostUser:  currentUser(),
		Branch:    session.GitBranch(dir),
		CreatedAt: time.Now().UTC(),
	}
	local := session.NewLocal(info, proc, session.Options{
		ScrollbackBytes: opts.ScrollbackBytes,
		MaxViewers:      opts.MaxViewers,
		FileView:        opts.FileView,
		FileEdit:        opts.FileEdit,
		FileDenyFunc:    opts.FileDeny,
		Folder:          folder,
		// Where the agent keeps its plans outside the folder (#289), from
		// its adapter and the environment the process was given.
		Plans:      agents.PlansDir(opts.Adapter, pty.Lookup(procEnv), dir),
		Transport:  proto.TransportWebRTC,
		Log:        opts.Log,
		OnChange:   a.noteChange,
		OnActivity: a.onLocalActivity,
		OnChat:     a.onLocalChat,
		Pattern:    opts.Pattern,
		WatchGit:   true,
	})
	a.mu.Lock()
	a.local, a.proc = local, proc
	a.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go a.activity.run(runCtx)

	if opts.LocalAttach && opts.Stdin != nil && opts.Stdout != nil {
		restore, err := a.attachLocal(runCtx)
		if err != nil {
			opts.Log.Warn("local attach unavailable", "err", err)
		} else {
			defer restore()
		}
	}

	go a.controlLoop(runCtx, firstConn)
	if opts.Link != "" {
		go a.mintLink(runCtx)
	}

	select {
	case <-local.Ended():
	case <-ctx.Done():
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = local.Stop(stopCtx)
		stopCancel()
	}
	// Give the control connection a moment to deliver the final status, the
	// last activity entries and what the viewers are still to be sent, unless
	// it is gone already: ctx is cancelled when the host is stopped from
	// outside.
	a.settle(ctx)
	cancel()
	a.closeAllPeers()
	// The viewers' editors end apart from them: none outlives the host.
	local.WaitEditors()
	exit := proc.Exit()
	return Result{SessionID: a.sessionID(), ExitCode: exit.Code}, nil
}

type agent struct {
	opts  Options
	wsURL string
	dir   string
	cols  uint16
	rows  uint16
	local *session.Local
	proc  *pty.Process
	log   *slog.Logger

	mu     sync.Mutex
	sessID string
	secret string
	ice    []proto.ICEServer
	// out writes on the current control connection (controlWriter); nil
	// while there is none.
	out     *controlWriter
	peers   map[string]*peer
	flushed chan struct{}

	agentToken    string
	lastAttention session.Attention
	// lastKept is what the session kept as lastAttention was told
	// (Info.Kept, to the owner's own server alone: toServerKept), the pair
	// sent and resent together. Guarded by mu.
	lastKept []string
	// attentionUntold says the last attention change was not sent for want
	// of a connection, or a connection has started since the last was told
	// (retellAttention): the next change sends it, whatever it is.
	// attentionRetry says a retry of one that had no turn in time is under
	// way (retryAttention); attentionOf is the session the last change was
	// of. Guarded by mu.
	attentionUntold bool
	attentionRetry  bool
	attentionOf     string
	// changeNext is the session's latest change not yet handed to
	// onLocalChange, nil for none; forwarding says forwardChanges runs;
	// retell says a connection started since (retellAttention): the next
	// change forwarded is the session as it is then. Guarded by mu.
	changeNext *session.Info
	retell     bool
	forwarding bool
	// instance and localID fix the session's id on the server across its
	// restarts (proto.HostInfo.Instance); instance is a secret, never logged.
	instance, localID string
	// held is what the server last said it holds of the links minted for
	// the session (registered.links, then link_created and link_revoked);
	// heldKnown is false until a server said. Guarded by mu.
	held      map[string]bool
	heldKnown bool
	// links are the link requests waiting for the server's answer, by
	// request id. Guarded by mu.
	links map[string]chan linkAnswer

	// creds are the links the viewers joined through (linkCreds), and the
	// credentials of the viewers that came as an identity provider's
	// principal (linkCreds.principal): one the server says no longer opens
	// the session (link_gone), or every one as the connection to it is
	// lost, withdraws what its viewers asked for and is still under way
	// (peer.authorize). Its lock is held for nothing
	// else: the session asks under its own locks.
	creds linkCreds
	// live is what the host knows of its server on the current control
	// connection (connLive), nil while there is none: what a link's viewer
	// sends is acted on only while a ping the host sent lately was answered
	// there (vouches).
	live atomic.Pointer[connLive]

	// activity carries the local session's activity entries to the server;
	// statusQueued says the session's final (status) entry is in it.
	activity     *activityForwarder
	statusQueued atomic.Bool

	// unpublish ends the publication when a switchyard sends a stop
	// (Options.Switchyard): the Uplink's, which leaves the session running
	// here. Set before the control loop starts; nil ignores the stop.
	unpublish func()

	// sendHook replaces the control connection in tests.
	sendHook func(v any)
	// submitWait replaces submitTimeout in tests.
	submitWait time.Duration
	// relayWait replaces relayTurnWait, and stallWait writeStall, in tests.
	relayWait, stallWait time.Duration
	// controlWait replaces controlTurnWait in tests.
	controlWait time.Duration
	// inputWait replaces session.InputWait in tests.
	inputWait time.Duration
	// viewersWait replaces viewersSettle in tests.
	viewersWait time.Duration
	// liveWithin replaces linkHeardWithin, and livePing livePingEvery, in
	// tests.
	liveWithin, livePing time.Duration
	// answers applies the routed answers (answer_as) of the current control
	// connection, off its read loop (openAnswers); nil while there is
	// none. Guarded by mu.
	answers *routedAnswers
	// beforeSave, when set (tests), runs as a viewer's save part has its
	// turn on the viewer's saving queue, before the session takes it.
	beforeSave atomic.Pointer[func(proto.FileWrite)]
	// beforeAnswer, when set (tests), runs as an offer's answer begins, off
	// the read loop (peer.handleOffer); beforePing as a ping reserved goes
	// out (pingOn).
	beforeAnswer atomic.Pointer[func()]
	beforePing   atomic.Pointer[func()]
	// beforeConnLost, when set (tests), runs as a control connection's
	// end begins letting its writer and viewers go (connLost).
	beforeConnLost atomic.Pointer[func()]
	// ticks counts keepHearing's ticks done, each counted after its ping
	// is reserved, or not: two more ticks after a link went means one
	// began after it went and reserved none (tests wait on that before
	// they lapse the vouching).
	ticks atomic.Int64
}

// inputDeadline is when keystrokes arriving now must be written by (a
// viewer's keys queue, peer.keyIn): their wait in the queue counts, so
// those that waited out their time behind keystrokes the program does not
// take are refused, never written late.
func (a *agent) inputDeadline() time.Time {
	d := session.InputWait
	if a.inputWait > 0 {
		d = a.inputWait
	}
	return time.Now().Add(d)
}

// submitDeadline is when a submission arriving now must be done by: its
// wait in its viewer's queue counts, so one that waited out its time behind
// a submission the process does not take is refused, never typed late.
func (a *agent) submitDeadline() time.Time {
	d := submitTimeout
	if a.submitWait > 0 {
		d = a.submitWait
	}
	return time.Now().Add(d)
}

func (a *agent) sessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessID
}

// controlURL is the control connection's URL for a server's: ws or wss,
// the server's host and path, then /ws/host. A server URL is its address
// alone: one with user information, a query or a fragment is refused
// (config.ServerURLExtra). No error repeats any of the URL.
func controlURL(server string) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("host: the server URL cannot be read, or names no host")
	}
	if config.ServerURLExtra(server) {
		return "", errors.New("host: the server URL must carry no user name, password, query or fragment: the host token is a setting of its own")
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", errors.New("host: the server URL must be http, https, ws or wss")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws/host"
	return u.String(), nil
}

// attentionURL is where a hosted session's hooks report its attention, its
// process's CONDUCTOR_NOTIFY_URL: the server's host and path
// (config.ShownURL), then the session's route, over http or https, which
// is what `conductor notify` posts with: a ws server's is http and a wss
// server's https, the reverse of controlURL.
func attentionURL(server, sessionID string) string {
	shown := config.ShownURL(server)
	if rest, ok := strings.CutPrefix(shown, "ws://"); ok {
		shown = "http://" + rest
	} else if rest, ok := strings.CutPrefix(shown, "wss://"); ok {
		shown = "https://" + rest
	}
	return strings.TrimRight(shown, "/") + "/api/sessions/" + sessionID + "/attention"
}

// currentUser returns the OS user name for "hosted by", bounded and never
// empty-on-error: an unknown user is simply omitted.
func currentUser() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if i := strings.LastIndexAny(name, `\/`); i >= 0 { // DOMAIN\user on Windows
		name = name[i+1:]
	}
	if len(name) > proto.MaxHostUser {
		name = name[:proto.MaxHostUser]
	}
	return name
}

// hostEnv forwards the developer's full environment (agents need their own
// credentials) minus conductor tokens and loader overrides, then adds the
// per-session variables, which win over what the environment says.
func hostEnv(inject map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CONDUCTOR_") || k == "LD_PRELOAD" || k == "LD_LIBRARY_PATH" {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range inject {
		out = append(out, k+"="+v)
	}
	out = append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
	return out
}

// controlLoop serves the initial connection, then keeps a registered control
// connection alive with backoff.
func (a *agent) controlLoop(ctx context.Context, first *websocket.Conn) {
	backoff := time.Second
	c := first
	for {
		if c == nil {
			var err error
			c, _, err = a.dialAndRegister(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				a.log.Warn("re-registration failed; retrying", "err", err, "in", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, a.opts.ReconnectMax)
				continue
			}
			backoff = time.Second
		}
		err := a.serveConn(ctx, c)
		c = nil
		if ctx.Err() != nil {
			return
		}
		select {
		case <-a.local.Ended():
			return
		default:
		}
		a.log.Warn("control connection lost; reconnecting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, a.opts.ReconnectMax)
	}
}

// dialAndRegister opens the control connection and registers (or resumes)
// the session. On success the connection is stored as the active one.
func (a *agent) dialAndRegister(ctx context.Context) (*websocket.Conn, proto.Registered, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	// No token, no header: an open host on a switchyard that admits them.
	header := map[string][]string{}
	if a.opts.Token != "" {
		header["Authorization"] = []string{"Bearer " + a.opts.Token}
	}
	base := &http.Client{}
	if a.opts.ServerIsOwners {
		base = ownersServerClient()
	} else if a.opts.transport != nil {
		base.Transport = a.opts.transport
	}
	// A redirect the dial follows is held to the rule the URL given was
	// (Run, Uplink.Publish): plain to another machine only with Insecure.
	base.CheckRedirect = a.redirectRule(base.CheckRedirect)
	// The connection's socket is a pacedConn (controlWriter). Whatever the
	// host reads on the connection, a frame's part included, as its TLS
	// gives it, it notes as heard (hearingTransport), for its give-up alone:
	// only its own pings answered vouch for the server (liveness.go).
	live := newConnLive()
	c, pc, resp, err := pacedDial(dialCtx, a.wsURL, header, base, a.stallWait, live.hear)
	cancel()
	if err != nil {
		if refused := refusal(resp); refused != nil {
			return nil, proto.Registered{}, refused
		}
		// A redirect the policy refused (redirectRule): the refusal's words
		// alone, which name the host and nothing else of the URL the
		// redirect gave (the client's wrapping repeats that URL whole).
		var refusedRedirect *redirectRefused
		if errors.As(err, &refusedRedirect) {
			return nil, proto.Registered{}, refusedRedirect.err
		}
		return nil, proto.Registered{}, dialFailure(err, resp, serverHost(a.wsURL))
	}
	c.SetReadLimit(proto.MaxHostMessage + proto.MaxFrame)

	cols, rows := a.cols, a.rows
	a.mu.Lock()
	if a.local != nil {
		info := a.local.Info()
		cols, rows = info.Cols, info.Rows
	}
	reg := a.registration(cols, rows)
	if a.sessID != "" {
		reg.Resume = &proto.HostResume{SessionID: a.sessID, Secret: a.secret}
	}
	a.mu.Unlock()
	fail := func(err error) (*websocket.Conn, proto.Registered, error) {
		c.CloseNow()
		return nil, proto.Registered{}, err
	}
	if err := writeJSON(ctx, c, reg); err != nil {
		return fail(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	typ, data, err := c.Read(rctx)
	rcancel()
	if err != nil {
		if reg.Resume != nil && websocket.CloseStatus(err) == websocket.StatusCode(proto.CloseNotFound) {
			// The server no longer knows the session (it restarted and keeps
			// hosted sessions in memory): resuming would fail forever, so the
			// next attempt registers afresh, under the same instance and local
			// id, which give the same session id on a server that keeps them.
			a.mu.Lock()
			old := a.sessID
			a.sessID, a.secret = "", ""
			a.mu.Unlock()
			return fail(fmt.Errorf("registration: the server no longer knows session %s; registering afresh", old))
		}
		return fail(fmt.Errorf("registration: %w", err))
	}
	var registered proto.Registered
	if typ != websocket.MessageText || json.Unmarshal(data, &registered) != nil || registered.T != proto.HostRegistered {
		return fail(errors.New("registration rejected"))
	}
	live.hear()
	a.mu.Lock()
	first := a.sessID == ""
	a.sessID = registered.SessionID
	a.secret = registered.Secret
	a.ice = registered.ICEServers
	if registered.Links != nil {
		a.held, a.heldKnown = map[string]bool{}, true
		for _, id := range registered.Links {
			a.held[id] = true
		}
	}
	if len(a.opts.ICEServers) > 0 {
		a.ice = a.opts.ICEServers
	}
	a.mu.Unlock()
	a.useConnHearing(c, pc, live)
	a.log.Info("registered with conductor", "session", registered.SessionID, "resumed", registered.Resumed)
	if first && a.opts.Registered != nil {
		a.opts.Registered(registered.SessionID, registered.ShareBaseURL)
	}
	return c, registered, nil
}

// registration is the register message: the session as the owner's own
// server lists it, or, to a switchyard (Options.Switchyard), as little as
// lists it there and introduces its viewers. A switchyard gets the
// session's name and agent, the machine's name and the last element of the
// folder; for a command line, the one word older switchyards require (the
// agent's id, else the program's name, never an argument); no OS user and
// no branch. Its viewers have the folder in full from the session's welcome.
func (a *agent) registration(cols, rows uint16) proto.Register {
	reg := proto.Register{
		T:     proto.HostRegister,
		Proto: proto.ProtoVersion,
		Host:  proto.HostInfo{Name: a.opts.HostName, Version: version.Version, User: currentUser(), Instance: a.instance},
		Session: proto.HostSession{
			Name: a.opts.Name, AgentID: a.opts.AgentID, Command: a.opts.Argv, Cwd: a.dir, Cols: cols, Rows: rows,
			RelayOnly: a.opts.RelayOnly, AgentToken: a.agentToken, LocalID: a.localID,
		},
	}
	if !a.opts.Switchyard {
		reg.Session.Branch = session.GitBranch(a.dir)
		// The owner's own server may route the session's questions: it is
		// told what the session keeps, and its answers are taken.
		reg.Host.AnswerAs = true
		return reg
	}
	reg.Host.User = ""
	program := a.opts.AgentID
	if program == "" && len(a.opts.Argv) > 0 {
		program = filepath.Base(a.opts.Argv[0])
	}
	if program == "" || program == "." || program == string(filepath.Separator) {
		program = "agent"
	}
	reg.Session.Command = []string{program}
	reg.Session.Cwd = lastElement(a.dir)
	return reg
}

// lastElement is the last element of a folder's path, "" for none.
func lastElement(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Base(dir)
}

// noteChange is the local session's OnChange hook: it hands the change to
// forwardChanges and returns at once, so that what changed the session (a
// keystroke that answers a prompt, on a viewer's keys queue; a viewer's
// hello, on the frame loop) never waits for the control connection's turn
// (onLocalChange may wait up to controlTurnWait). Only the latest change
// waits, and one goroutine forwards them, in order. The Info it is handed
// says only that the session changed: the session reads it before calling
// the hook, and its calls are not ordered among themselves, so one holding
// an older snapshot may come after one holding a newer (forwardChanges).
func (a *agent) noteChange(info session.Info) {
	a.mu.Lock()
	a.changeNext = &info
	running := a.forwarding
	a.forwarding = true
	a.mu.Unlock()
	if !running {
		go a.forwardChanges()
	}
}

// forwardChanges hands the changes noteChange noted to onLocalChange, one at
// a time, until none is left: for each, the session as it is when the
// forwarder gets to it (Local.Info, read here, on the one goroutine), which
// is at least as new as the change noted, and a change made after this
// read is noted after it. The snapshot a change was noted with is not
// forwarded: a hook's call holding one older than a change already told
// (the session read it, then another call read and told a question, then
// this one was noted) would tell the server the question gone while it
// waits, and its router would call it resolved (#482). Nor is a snapshot
// taken as the connection started (retellAttention), which could be older
// than a change noted since. Only an agent with no session (tests) forwards
// what it was handed.
func (a *agent) forwardChanges() {
	for {
		a.mu.Lock()
		info, retell, local := a.changeNext, a.retell, a.local
		a.changeNext, a.retell = nil, false
		if info == nil && !retell {
			a.forwarding = false
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		if local != nil {
			cur := local.Info()
			info = &cur
		}
		if info == nil {
			continue
		}
		a.onLocalChange(*info)
	}
}

// onLocalChange forwards attention changes of the local session to the
// server so listings stay current for hosted sessions.
func (a *agent) onLocalChange(info session.Info) {
	att := a.toServer(info.Attention)
	// What the session keeps goes with what it shows, from the one Info:
	// a change of either is told, the two together.
	kept := a.toServerKept(info.Kept)
	a.mu.Lock()
	changed := a.attentionUntold || !sameAttention(att, a.lastAttention) || !slices.Equal(kept, a.lastKept)
	// The mark is taken here, under the lock that set it, not after the
	// send: one set while a send was under way would be cleared by that
	// send's end and its re-tell lost; taken here, it makes the next
	// change tell, at worst the same attention once more.
	a.attentionUntold = false
	a.lastAttention, a.lastKept, a.attentionOf = att, kept, info.ID
	retrying := a.attentionRetry
	a.mu.Unlock()
	if !changed || retrying {
		// A retry under way sends the latest when its turn comes.
		return
	}
	err := a.send(hostAttentionMsg(info.ID, att, kept))
	switch {
	case err == nil:
	case errors.Is(err, errConnDown):
		// No connection: the session's next change tells it, and a new
		// connection is told the current attention as it starts.
		a.mu.Lock()
		a.attentionUntold = true
		a.mu.Unlock()
	default:
		// No turn in time on a connection the server reads slowly.
		a.retryAttention()
	}
}

// retellAttention has the server told the session's attention as a control
// connection starts, whatever the last connection was told: the server
// keeps the attention it had when the host went (its listings show it
// meanwhile), so one that changed while the host was away, to the empty
// state too (a question answered at the host's own terminal), would stay in
// its listings until the session's next change. The attention goes in its
// turn among the session's changes, read as the forwarder gets to it
// (forwardChanges), so that a change made meanwhile is never overtaken by
// an older attention.
func (a *agent) retellAttention() {
	a.mu.Lock()
	a.attentionUntold = true
	a.retell = true
	running := a.forwarding
	a.forwarding = true
	a.mu.Unlock()
	if !running {
		go a.forwardChanges()
	}
}

// retryAttention sends the latest attention as soon as the connection takes
// it, however long that waits while the connection lasts, and again while
// it changed meanwhile: one retry at a time, and while it runs the changes
// are only noted (onLocalChange), so the server is told them in order. A
// retry whose connection is replaced under it, whether its send went
// through there or not (a paced write proves no delivery), carries on to
// the connection that has taken its place, or leaves the attention untold
// for the next connection's start when none has yet: that start's retell
// is left to a retry under way (onLocalChange).
func (a *agent) retryAttention() {
	a.mu.Lock()
	if a.attentionRetry {
		a.mu.Unlock()
		return
	}
	a.attentionRetry = true
	a.mu.Unlock()
	go func() {
		for {
			a.mu.Lock()
			att, kept, id, on := a.lastAttention, a.lastKept, a.attentionOf, a.out
			a.mu.Unlock()
			err := a.tell(hostAttentionMsg(id, att, kept), nil)
			a.mu.Lock()
			if a.out != on {
				// The connection the attention went on is not the host's
				// any more: told on its replacement, or left for the
				// next connection's start.
				if a.out != nil {
					a.mu.Unlock()
					continue
				}
				a.attentionRetry = false
				a.attentionUntold = true
				a.mu.Unlock()
				return
			}
			if err != nil || sameAttention(att, a.lastAttention) && slices.Equal(kept, a.lastKept) {
				a.attentionRetry = false
				a.attentionUntold = err != nil
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
		}
	}()
}

// sameAttention says whether x and y tell the server the same: a question
// asked again with the same words is another question (its ID), which the
// server's listings must name for an answer from them to reach it.
func sameAttention(x, y session.Attention) bool {
	// The source too: a switchyard is told no words, and a held turn
	// (source submit) differs from a bell's wait by its source alone there.
	// The plan too (#289): a plan set on the question showing (the dialog's
	// footer read after the hook's report) changes nothing else of it. The
	// mark of a question answered in the terminal too (Answered): keys
	// typed there change nothing else of the question shown, and the
	// server's router resolves a routed question from that mark alone
	// while the program stays quiet.
	return x.State == y.State && x.Message == y.Message && x.Source == y.Source && x.Kind == y.Kind && len(x.Options) == len(y.Options) && x.ID == y.ID && x.Asked() == y.Asked() && x.Plan == y.Plan && x.Answered == y.Answered
}

// toServer is an attention change as the server is told it: in full to the
// owner's own server, and to a switchyard (Options.Switchyard) its state,
// kind and source alone (with its stamp and id), never its words (a
// prompt's text, the start of the agent's reply a "done" carries), its
// choices or its mark as the agent's own question (Attention.Asked).
func (a *agent) toServer(att session.Attention) session.Attention {
	if a.opts.Switchyard {
		return session.Attention{State: att.State, Source: att.Source, Kind: att.Kind, Since: att.Since, ID: att.ID}
	}
	return att
}

// hostAttentionMsg encodes an attention change for the control connection,
// with its mark as the agent's own question, which the server's copy keeps
// through a done it forwards (signal.HostedSession.Ask), and what the
// session keeps without showing it, read with it (toServerKept).
func hostAttentionMsg(sessionID string, att session.Attention, kept []string) proto.HostAttentionMsg {
	m := proto.HostAttentionMsg{T: proto.HostAttention, SessionID: sessionID, State: string(att.State), Message: att.Message, Source: att.Source, Kind: att.Kind, ID: att.ID, Ask: att.Asked(), Answered: att.Answered, Plan: att.Plan, Kept: kept}
	for _, o := range att.Options {
		m.Options = append(m.Options, proto.AttentionOption{Label: o.Label, Input: o.Input, Mode: o.Mode})
	}
	return m
}

// onLocalActivity is the local session's OnActivity hook: it queues the entry,
// with the attention state an attention entry records, for the server, whose
// admin stream shows every entry of every session. The session calls it on the
// goroutine that recorded the entry, so it never waits for the connection.
// Entries recorded while the connection is down are lost to the stream; the
// session log has them.
// A switchyard (Options.Switchyard) is sent no entry: the session's own
// viewers have its log over their connection.
func (a *agent) onLocalActivity(_ string, e session.ActivityEntry, state session.AttentionState) {
	if !a.opts.Switchyard {
		a.activity.push(e, state)
	}
	if e.Type == session.ActivityStatus {
		a.statusQueued.Store(true)
	}
}

// onLocalChat is the local session's OnChat hook: it queues the message for
// the server, whose admin stream shows it to the browsers' unread counts.
// The session calls it on the goroutine that kept the message, so it never
// waits for the connection; a message kept while the connection is down is
// lost to the stream, and the session's chat keeps it. A switchyard
// (Options.Switchyard) is sent none: the people on the session have the chat
// over their own connection.
func (a *agent) onLocalChat(_ string, m session.ChatMessage) {
	if a.opts.Switchyard {
		return
	}
	a.activity.pushChat(m)
}

// sendChat delivers one chat message on the control connection.
func (a *agent) sendChat(m session.ChatMessage) {
	a.send(proto.HostChatMsg{T: proto.HostChat, SessionID: a.sessionID(), Message: session.ChatToProto(m)})
}

// sendActivity delivers one entry, and the attention state it records, on the
// control connection.
func (a *agent) sendActivity(e session.ActivityEntry, state session.AttentionState) {
	m := hostActivityMsg(a.sessionID(), e)
	m.State = string(state)
	a.send(m)
}

// settle gives the control connection a moment to deliver what the session
// leaves behind when it ends: the final status message, the activity entries
// queued for the server, the final status entry among them, and the frames
// queued for the viewers, whose final status is the last thing a viewer hears
// before its connection closes. It gives up at once when ctx is cancelled. That
// is how the host is stopped from outside (SIGINT, SIGTERM), and it ends the
// control connection and the forwarder before the session records its last
// entry, so there is nothing left to wait for.
func (a *agent) settle(ctx context.Context) {
	a.flushStatus(ctx)
	a.flushActivity(ctx)
	a.flushViewers(ctx)
}

// viewersSettle bounds how long a session that ended waits for its viewers
// to have what it left them (flushViewers).
const viewersSettle = 2 * time.Second

// flushViewers waits for the viewers to be handed the frames queued for them,
// and for the data channels of the WebRTC viewers to have what they were given
// acknowledged. A relay viewer's frames go out on the control connection, which
// closes once settle returns (and drops what its socket has not taken yet), and
// a data channel drops what it still holds when
// its connection is closed: a frame that is queued or held by then is lost, and
// the viewer is left without the status that says the session ended. Run it
// after flushActivity: the session announces its end, and only then queues the
// status entry for its viewers. It waits viewersSettle at most.
func (a *agent) flushViewers(ctx context.Context) {
	wait := viewersSettle
	if a.viewersWait > 0 {
		wait = a.viewersWait
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if a.local.Drained() && !a.channelsBuffered() && a.unsent() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// unsent is what the control connection holds that its socket has not
// taken yet (pacedConn), which closing the connection drops.
func (a *agent) unsent() int {
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	if w == nil {
		return 0
	}
	return w.unsent()
}

// channelsBuffered reports whether the data channel of a WebRTC viewer still
// holds bytes that the viewer has not acknowledged.
func (a *agent) channelsBuffered() bool {
	a.mu.Lock()
	peers := make([]*peer, 0, len(a.peers))
	for _, p := range a.peers {
		peers = append(peers, p)
	}
	a.mu.Unlock()
	for _, p := range peers {
		if p.buffered() > 0 {
			return true
		}
	}
	return false
}

// flushActivity gives the entries the local session recorded a moment to
// reach the server before the connection closes. Once the process has ended
// that includes the final status entry, which the session records just after
// it announces the end. See settle for ctx.
func (a *agent) flushActivity(ctx context.Context) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if (!a.local.Info().Status.Ended() || a.statusQueued.Load()) && a.activity.idle() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// hostActivityMsg wraps an entry in the host `activity` message. The host
// names its session; the server ignores it, and takes the connection's.
func hostActivityMsg(sessionID string, e session.ActivityEntry) proto.HostActivityMsg {
	return proto.HostActivityMsg{T: proto.HostActivity, SessionID: sessionID, Entry: session.EntryToProto(e)}
}

// useConn makes c, whose socket is pc, the control connection the host
// writes on, through a controlWriter of its own, which ends with the
// connection (dropConn). A connection dialed without a pacedConn (nil) is
// written to as its socket takes it.
func (a *agent) useConn(c *websocket.Conn, pc *pacedConn) *controlWriter {
	return a.useConnHearing(c, pc, nil)
}

// useConnHearing is useConn of a connection whose hearing and pings are
// live (one heard from now, no ping answered, when nil), which becomes the
// host's (vouches) until the connection is dropped.
func (a *agent) useConnHearing(c *websocket.Conn, pc *pacedConn, live *connLive) *controlWriter {
	if live == nil {
		live = newConnLive()
	}
	w := newControlWriter(c, pc)
	w.controlTurn = a.controlWait
	w.live = live
	a.mu.Lock()
	old := a.out
	a.out = w
	a.live.Store(live)
	a.mu.Unlock()
	if old != nil {
		old.stop()
		if old.live != nil {
			old.live.end()
		}
	}
	return w
}

// writerFor is the writer of c, made now when c has none.
func (a *agent) writerFor(c *websocket.Conn) *controlWriter {
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	if w != nil && w.c == c {
		return w
	}
	return a.useConn(c, nil)
}

// dropConn ends the writer of c, if it is the current one: what waits to be
// written on c is answered errConnDown.
func (a *agent) dropConn(c *websocket.Conn) {
	a.mu.Lock()
	w := a.out
	if w != nil && w.c == c {
		a.out = nil
		a.live.CompareAndSwap(w.live, nil)
	} else {
		w = nil
	}
	a.mu.Unlock()
	if w != nil {
		w.stop()
		if w.live != nil {
			w.live.end()
		}
	}
}

// serveConn processes messages on an established control connection until
// it fails. Peers are closed when the connection ends.
func (a *agent) serveConn(ctx context.Context, c *websocket.Conn) error {
	defer c.CloseNow()
	w := a.writerFor(c)
	// The routed answers this connection brings end with it, withdrawn
	// first: the rest of its end (its viewers let go, connLost) may wait,
	// and no answer of a connection gone is typed meanwhile. Their
	// goroutine is waited for after.
	withdrawAnswers, answersDone := a.openAnswers(ctx)
	defer func() {
		withdrawAnswers()
		a.connLost(c)
		answersDone()
	}()
	// The server is told the session's attention as the connection starts,
	// the empty state included (retellAttention).
	a.retellAttention()

	// Report the current status in case the process ended while disconnected.
	go a.watchStatus(ctx, w)
	// Ping the server while a link's viewer may need it.
	go a.keepHearing(ctx, c, w.live, w.done)

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		w.live.hear()
		if typ == websocket.MessageBinary {
			f, err := proto.Decode(data)
			if err != nil || f.Type != proto.TypeRelay {
				continue
			}
			viewerID, inner, err := proto.DecodeRelay(f.Payload)
			if err != nil {
				continue
			}
			if p := a.peer(viewerID); p != nil {
				p.handleFrame(inner)
			}
			continue
		}
		if err := a.handleControl(ctx, data); err != nil {
			a.log.Warn("host control message", "err", err)
		}
	}
}

func (a *agent) watchStatus(ctx context.Context, w *controlWriter) {
	select {
	case <-ctx.Done():
		return
	case <-w.done:
		// The connection ended: the next one's watcher reports.
		return
	case <-a.local.Ended():
	}
	a.handViewersTheEnd(ctx, w.done)
	info := a.local.Info()
	if b, err := json.Marshal(proto.HostStatusMsg{T: proto.HostStatus, SessionID: info.ID, Status: string(info.Status), ExitCode: info.ExitCode}); err == nil {
		_ = w.sendControl(websocket.MessageText, b)
	}
	a.mu.Lock()
	if a.flushed == nil {
		a.flushed = make(chan struct{})
		close(a.flushed)
	}
	a.mu.Unlock()
}

// endDrain bounds how long the session's end waits for the viewers to be
// handed their last frames before the server is told of it
// (handViewersTheEnd).
const endDrain = 2 * time.Second

// endLinger is how long a publication outlasts its session's end: as long
// as the rendezvous may still write the viewers of the session's links what
// it relayed them last (the api's closeWait), during which a revoke of one
// of those links is still carried there (Published.Revoke).
const endLinger = 5 * time.Second

// handViewersTheEnd waits, at most endDrain, until the session's last
// frames for its viewers, its final status entry among them, have been
// handed to their transports, and the WebRTC viewers' data channels have
// had them acknowledged: those of the relayed viewers then go out on the
// control connection ahead of what is handed to it after, the session's end
// among it, and those of the WebRTC viewers have reached them. The server
// ends a hosted session's links when it hears the session ended, and with
// them the viewers attached through them: a relayed one is written what
// reached the server for it first, but a browser on WebRTC lets go of its
// data channel as its connection to the server closes. The host's own
// messages otherwise go ahead of relayed frames (controlWriter). done, when
// not nil, ends the wait (the connection's writer ending).
func (a *agent) handViewersTheEnd(ctx context.Context, done <-chan struct{}) {
	deadline := time.Now().Add(endDrain)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if a.statusQueued.Load() && a.local.Drained() && !a.channelsBuffered() {
			return
		}
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// flushStatus waits briefly for the final status message to be sent, the
// viewers' last frames handed first (handViewersTheEnd). It gives up at once
// when ctx is cancelled: watchStatus stops then without sending it.
func (a *agent) flushStatus(ctx context.Context) {
	deadline := time.After(endDrain + 2*time.Second)
	for {
		a.mu.Lock()
		done := a.flushed
		a.mu.Unlock()
		if done != nil {
			return
		}
		select {
		case <-deadline:
			return
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (a *agent) handleControl(ctx context.Context, data []byte) error {
	t, err := proto.ParseHeader(data)
	if err != nil {
		return err
	}
	switch t {
	case proto.HostViewerJoin:
		var m proto.ViewerJoin
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		role := session.Role(m.Role)
		if !role.Valid() || len(m.ViewerID) != proto.ViewerIDLen {
			return errors.New("invalid viewer_join")
		}
		a.addPeerAs(m.ViewerID, role, m.LinkID, m.LinkLabel, m.Principal)
	case proto.HostLinkGone:
		var m proto.LinkGone
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if m.LinkID == "" || len(m.LinkID) > proto.MaxLinkID {
			return errors.New("invalid link_gone")
		}
		a.creds.gone(m.LinkID)
	case proto.HostOffer:
		var m proto.ViewerSDP
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		// Answered off this loop (handleOffer): the answer's wait for its
		// turn on the writer holds nothing the loop reads.
		if p := a.peer(m.ViewerID); p != nil {
			if err := p.handleOffer(m.SDP); err != nil {
				// The viewer falls back to the relay on its own timeout;
				// one whose first offer is under way has it answered.
				a.log.Debug("offer ignored", "viewer", m.ViewerID, "err", err)
			}
		}
	case proto.HostICE:
		var m proto.ViewerICE
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if p := a.peer(m.ViewerID); p != nil {
			p.handleICE(m.Candidate)
		}
	case proto.HostRelayStart:
		var m proto.ViewerRef
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if p := a.peer(m.ViewerID); p != nil {
			p.startRelay()
		}
	case proto.HostViewerLeave:
		var m proto.ViewerLeave
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		// Its link no longer opens the session: what the viewer has under
		// way is withdrawn, not finished. An older server gives no reason,
		// and one this host does not know is none.
		var why error
		switch proto.LeaveReason(m.Reason) {
		case proto.LeaveRevoked:
			why = session.ErrRevoked
		case proto.LeaveExpired:
			why = session.ErrExpired
		}
		a.removePeerFor(m.ViewerID, why)
	case proto.HostViewerBehind:
		var m proto.ViewerBehind
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if len(m.ViewerID) != proto.ViewerIDLen {
			return errors.New("invalid viewer_behind")
		}
		// The viewer's relayed frames wait while it is behind (relaySink);
		// one that has left needs nothing.
		if p := a.peer(m.ViewerID); p != nil {
			p.room.set(m.Behind)
		}
	case proto.HostAttention:
		if a.opts.Switchyard {
			return nil // see Options.Switchyard
		}
		var m proto.HostAttentionMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		source := m.Source
		if source == "" {
			source = session.SourceAPI
		}
		var opts []session.Option
		for _, o := range m.Options {
			opts = append(opts, session.Option{Label: o.Label, Input: o.Input, Mode: o.Mode})
		}
		// The server's listing took this report without an id (the id is
		// the session's, given as it applies it), and one the session
		// takes as changing nothing (the same as it shows), or records and
		// does not show (its turn held for a typed text: it shows
		// session.HeldWords), makes no change to tell: the host tells its
		// attention, with its id, all the same, in its turn among its
		// changes, so that the server never holds an id or a change the
		// session does not show, or none. The mark is set before the
		// report is applied, so that the session's own change and the
		// re-tell below make one message between them, whichever is
		// forwarded first (the other finds the same attention told).
		a.mu.Lock()
		a.attentionUntold = true
		a.mu.Unlock()
		switch {
		case m.Ask && session.AttentionState(m.State) == session.AttentionNeedsInput:
			// The agent's own question (the server's Ask): the session
			// keeps it through its turn's done.
			a.local.Ask(m.Message, source, m.Kind, opts)
		case m.Plan != "":
			// The plan the question is about (#289), set with it.
			a.local.SetAttentionPlan(session.AttentionState(m.State), m.Message, source, m.Kind, opts, m.Plan)
		default:
			a.local.SetAttentionFull(session.AttentionState(m.State), m.Message, source, m.Kind, opts)
		}
		a.noteChange(a.local.Info())
	case proto.HostAnswerAs:
		if a.opts.Switchyard {
			// A switchyard has no authority over the terminal: never
			// sent one, what arrives anyway is dropped unread.
			a.log.Warn("an answer_as from the switchyard was dropped unread")
			return nil
		}
		a.answerAs(data)
	case proto.HostActivity:
		if a.opts.Switchyard {
			return nil // see Options.Switchyard
		}
		// An event an agent reported through the API for this session. It is
		// recorded like any other entry, so the people watching the host see
		// it, and the OnActivity hook reports it back to the server.
		var m proto.HostActivityMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if !session.ValidEventType(m.Entry.Type) {
			return errors.New("activity of an unknown type")
		}
		a.local.Record(session.EntryFromProto(m.Entry))
	case proto.HostStop:
		if a.opts.Switchyard {
			// A switchyard ends the publication; the process is the owner's.
			a.log.Info("the switchyard ended the publication; the session goes on here")
			if a.unpublish != nil {
				go a.unpublish()
			}
			return nil
		}
		a.log.Info("stop requested by server")
		go func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = a.local.Stop(stopCtx)
		}()
	case proto.HostLinkCreated:
		var m proto.LinkCreated
		if json.Unmarshal(data, &m) == nil {
			a.noteHeld(m.LinkID, true)
			a.answerLink(m.RequestID, m, nil)
		}
	case proto.HostLinkRevoked:
		var m proto.LinkRevoked
		if json.Unmarshal(data, &m) == nil {
			a.noteHeld(m.LinkID, false)
			a.answerLink(m.RequestID, proto.LinkCreated{LinkID: m.LinkID}, nil)
		}
	case proto.HostRunLinkUpdated:
		var m proto.RunLinkUpdated
		if json.Unmarshal(data, &m) == nil {
			a.answerLink(m.RequestID, proto.LinkCreated{RunID: m.RunID}, nil)
		}
	case proto.HostError:
		var m proto.ErrorMsg
		_ = json.Unmarshal(data, &m)
		if m.RequestID != "" {
			a.answerLink(m.RequestID, proto.LinkCreated{}, fmt.Errorf("%s: %s", m.Code, m.Message))
			return nil
		}
		a.log.Warn("server error", "code", m.Code, "message", m.Message)
	}
	return nil
}

// linkAttemptWait is how long one try of --link's request waits for the
// server's answer; linkAttempts how many tries it makes.
var (
	linkAttemptWait = 15 * time.Second
	linkAttempts    = 5
)

// mintLink asks the server for the link Options.Link names and tells
// Options.LinkMinted, then again at each receive on Options.LinkAgain, until
// ctx ends.
func (a *agent) mintLink(ctx context.Context) {
	for {
		a.mintLinkOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-a.opts.LinkAgain: // nil: never
		}
	}
}

// mintLinkOnce is one link of mintLink's. A try that gets no answer (the
// connection dropped before link_created, or was down) is made again once
// the control loop has had time to reconnect, up to linkAttempts tries; a
// refusal is final.
func (a *agent) mintLinkOnce(ctx context.Context) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		lctx, cancel := context.WithTimeout(ctx, linkAttemptWait)
		res, err := a.requestLink(lctx, a.opts.Link, a.opts.LinkTTL, "conductor host")
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil || !errors.Is(err, context.DeadlineExceeded) || attempt >= linkAttempts {
			if a.opts.LinkMinted != nil {
				a.opts.LinkMinted(res, err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, a.opts.ReconnectMax)
	}
}

// linkAnswer is what a link request waits for.
type linkAnswer struct {
	link proto.LinkCreated
	err  error
}

// requestLink asks the server for a share link to the session and waits
// for its answer (link_created, or an error naming the request), at most
// until ctx ends.
func (a *agent) requestLink(ctx context.Context, role string, ttl time.Duration, label string) (proto.LinkCreated, error) {
	return a.ask(ctx, func(id string) any {
		return proto.HostLinkMsg{T: proto.HostLink, RequestID: id, Role: role, TTLSeconds: int(ttl / time.Second), Label: label}
	})
}

// requestRevoke asks the server to revoke a link it minted for the session
// and waits for the answer; a link the server does not know answers
// "not_found: …". While the session runs the request goes at once, ahead
// of relayed frames; once it has ended, after the viewers' last frames
// (handViewersTheEnd), which the revocation would drop.
func (a *agent) requestRevoke(ctx context.Context, linkID string) error {
	if a.local != nil {
		select {
		case <-a.local.Ended():
			a.handViewersTheEnd(ctx, nil)
		default:
		}
	}
	_, err := a.ask(ctx, func(id string) any {
		return proto.HostLinkRevokeMsg{T: proto.HostLinkRevoke, RequestID: id, LinkID: linkID}
	})
	return err
}

// requestRunLink asks the server for one link to the sessions of a run's
// members (proto.HostRunLinkMsg) and waits for link_created.
func (a *agent) requestRunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error) {
	return a.ask(ctx, func(id string) any {
		return proto.HostRunLinkMsg{T: proto.HostRunLink, RequestID: id, Role: role, TTLSeconds: int(ttl / time.Second), Label: label, Run: run}
	})
}

// requestRunUpdate tells the server a run's members now and waits for
// link_run_updated; a run without links there answers "not_found: …".
func (a *agent) requestRunUpdate(ctx context.Context, run proto.RunGroup) error {
	_, err := a.ask(ctx, func(id string) any {
		return proto.HostRunLinkUpdateMsg{T: proto.HostRunLinkUpdate, RequestID: id, Run: run}
	})
	return err
}

// ask sends the request build makes with a fresh request id and waits for
// the server's answer to it (answerLink), or for ctx.
func (a *agent) ask(ctx context.Context, build func(requestID string) any) (proto.LinkCreated, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return proto.LinkCreated{}, err
	}
	id := hex.EncodeToString(b[:])
	ch := make(chan linkAnswer, 1)
	a.mu.Lock()
	if a.links == nil {
		a.links = map[string]chan linkAnswer{}
	}
	a.links[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.links, id)
		a.mu.Unlock()
	}()
	a.send(build(id))
	select {
	case ans := <-ch:
		return ans.link, ans.err
	case <-ctx.Done():
		return proto.LinkCreated{}, ctx.Err()
	}
}

// answerLink hands the server's answer to the request waiting for it.
func (a *agent) answerLink(id string, link proto.LinkCreated, err error) {
	a.mu.Lock()
	ch := a.links[id]
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- linkAnswer{link, err}:
		default:
		}
	}
}

// send delivers a JSON message on the current control connection, ahead of
// the relayed frames waiting (controlWriter); one that waits longer than
// controlTurnWait is dropped. The error says it was not sent; most callers
// need not know.
func (a *agent) send(v any) error {
	if a.sendHook != nil {
		a.sendHook(v)
		return nil
	}
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	if w == nil {
		return errConnDown
	}
	b, err := json.Marshal(v)
	if err == nil {
		err = w.sendControl(websocket.MessageText, b)
	}
	if err != nil {
		a.log.Debug("control send failed", "err", err)
	}
	return err
}

// sendRelay delivers a relayed terminal frame for a viewer, in its turn
// among the other viewers' (controlWriter): session.ErrSlowConsumer when the
// turn does not come within relayTurnWait, errViewerGone when gone closes
// first. Neither touches the connection.
func (a *agent) sendRelay(viewerID string, frame []byte, gone <-chan struct{}) error {
	return a.sendRelayWithin(viewerID, frame, gone, a.relayTurn())
}

// sessionEnded is closed once the session has ended; nil (never) for an
// agent without one.
func (a *agent) sessionEnded() <-chan struct{} {
	if a.local == nil {
		return nil
	}
	return a.local.Ended()
}

// relayTurn is how long a frame relayed to a viewer may wait to be sent:
// relayTurnWait, or a test's relayWait.
func (a *agent) relayTurn() time.Duration {
	if a.relayWait > 0 {
		return a.relayWait
	}
	return relayTurnWait
}

// sendRelayWithin is sendRelay with the frame's turn to come within turn:
// session.ErrSlowConsumer at once when turn is not above zero.
func (a *agent) sendRelayWithin(viewerID string, frame []byte, gone <-chan struct{}, turn time.Duration) error {
	// A frame the server would not relay (past its type's bound) would cost
	// the connection, every viewer's: it is this viewer's error alone, and
	// never written.
	if _, err := proto.Decode(frame); err != nil {
		return err
	}
	if turn <= 0 {
		return session.ErrSlowConsumer
	}
	env, err := proto.EncodeRelay(viewerID, frame)
	if err != nil {
		return err
	}
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	if w == nil {
		return errConnDown
	}
	return w.sendRelay(env, turn, gone)
}

// sendViewerError tells the server a viewer's error, which closes the viewer
// there: on a goroutine of its own, for its caller may be the one reading
// the connection, and waiting as long as the connection lasts and the
// viewer is there (tell), the first of a viewer's alone, for the first
// closes it. One for a viewer that has left goes nowhere.
// It reports whether the server is told (or was, by the viewer's first):
// false for a viewer this agent does not know, a paste invite's peer, which
// no server serves and which tells and lets the viewer go itself.
func (a *agent) sendViewerError(viewerID, code, msg string) bool {
	p := a.peer(viewerID)
	if p == nil {
		return false
	}
	if !p.erred.CompareAndSwap(false, true) {
		return true
	}
	go func() {
		_ = a.tell(proto.ViewerError{T: proto.HostViewerError, ViewerID: viewerID, Code: code, Message: msg}, p.left)
	}()
	return true
}

// tell is send for a message the server must have while the connection
// lasts (a viewer's close or error, which lets the viewer go there too): it
// waits for its turn as long as the connection does, or until gone closes
// (the viewer left, and the server let it go itself).
func (a *agent) tell(v any, gone <-chan struct{}) error {
	if a.sendHook != nil {
		a.sendHook(v)
		return nil
	}
	a.mu.Lock()
	w := a.out
	a.mu.Unlock()
	if w == nil {
		return errConnDown
	}
	b, err := json.Marshal(v)
	if err == nil {
		err = w.sendControlWhole(websocket.MessageText, b, gone)
	}
	if err != nil {
		a.log.Debug("control send failed", "err", err)
	}
	return err
}

func (a *agent) addPeer(id string, role session.Role, linkID, linkLabel string) {
	a.addPeerAs(id, role, linkID, linkLabel, false)
}

// addPeerAs is addPeer for a viewer that came, when principal, as an
// identity provider's principal (proto.ViewerJoin.Principal): with no link,
// it holds a cred of its own (linkCreds.principal), which the server's
// revoked leave or the loss of the connection to it (connLost), which may
// have kept that leave from coming, marks gone, so that what it has under
// way is withdrawn rather than finished; and what it sends is acted on only
// while the host vouches for its server, as a link's viewer's is.
func (a *agent) addPeerAs(id string, role session.Role, linkID, linkLabel string, principal bool) {
	a.removePeer(id)
	// A switchyard's viewer is never the owner's window, whatever else says
	// so: its workbench is its operator's (Options.Switchyard).
	p := newPeer(a, id, role, linkID, linkLabel, linkID == "" && a.opts.ServerIsOwners && !a.opts.Switchyard)
	p.cred = a.creds.of(linkID)
	if p.cred == nil && principal {
		p.cred = a.creds.principal()
	}
	if p.cred != nil {
		// Before its hello can arrive: its first line waits for the answer
		// when the host does not vouch for its server yet (awaitVouch).
		a.pingNow()
	}
	a.mu.Lock()
	a.peers[id] = p
	ice := a.ice
	a.mu.Unlock()
	if !a.opts.RelayOnly {
		if err := p.startWebRTC(ice); err != nil {
			a.log.Warn("webrtc unavailable for viewer; relay only", "viewer", id, "err", err)
		}
	}
}

func (a *agent) peer(id string) *peer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peers[id]
}

func (a *agent) removePeer(id string) { a.removePeerFor(id, nil) }

// connLost is the end of the control connection c: its writer goes, every
// viewer is let go, and every link the viewers joined through counts as
// gone (linkCreds.lost) before them, for a link's going is not heard of
// until the connection is back: what a link's viewer asked for and is
// still under way is withdrawn rather than finished. So is what a
// provider's principal asked for (linkCreds.principal): its revocation's
// leave may be what the connection did not carry.
func (a *agent) connLost(c *websocket.Conn) {
	if f := a.beforeConnLost.Load(); f != nil {
		(*f)()
	}
	a.dropConn(c)
	a.creds.lost()
	a.closeAllPeers()
}

// removePeerFor lets the viewer id go, for why (peer.closeFor).
func (a *agent) removePeerFor(id string, why error) {
	a.mu.Lock()
	p := a.peers[id]
	delete(a.peers, id)
	a.mu.Unlock()
	if p != nil {
		p.closeFor(why)
	}
}

func (a *agent) closeAllPeers() {
	a.mu.Lock()
	list := make([]*peer, 0, len(a.peers))
	for _, p := range a.peers {
		list = append(list, p)
	}
	a.peers = map[string]*peer{}
	a.mu.Unlock()
	for _, p := range list {
		p.close()
	}
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}

// discard is used when no stdout is configured.
var _ io.Writer = io.Discard

// noteHeld keeps held in step with a link the server minted or revoked.
func (a *agent) noteHeld(linkID string, on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.heldKnown || linkID == "" {
		return
	}
	if on {
		a.held[linkID] = true
	} else {
		delete(a.held, linkID)
	}
}

// heldLinks is what the server last said it holds, and whether it said.
func (a *agent) heldLinks() ([]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.heldKnown {
		return nil, false
	}
	out := make([]string, 0, len(a.held))
	for id := range a.held {
		out = append(out, id)
	}
	return out, true
}

// RefusedError is the rendezvous answering a registration with an HTTP
// error rather than the WebSocket: its status, the code and words of its
// JSON error, and how long it asked to wait (Retry-After), when it said.
type RefusedError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *RefusedError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("the switchyard refused it (%d %s): %s", e.Status, e.Code, msg)
	}
	return fmt.Sprintf("the switchyard refused it (%d): %s", e.Status, msg)
}

// Retryable says whether the refusal passes with time: 429, a rate or a
// limit of live sessions per address (round 14: a crew launched from a home
// running other sessions met the switchyard's limits and was never
// published), and how long to wait first, when the rendezvous said.
func (e *RefusedError) Retryable() (bool, time.Duration) {
	return e.Status == http.StatusTooManyRequests, e.RetryAfter
}

// refusal reads a refused handshake's response (the WebSocket library keeps
// the first 1024 bytes of its body) into a RefusedError; nil for none.
func refusal(resp *http.Response) *RefusedError {
	if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols || resp.StatusCode < 400 {
		return nil
	}
	e := &RefusedError{Status: resp.StatusCode}
	if resp.Body != nil {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if b, err := io.ReadAll(io.LimitReader(resp.Body, 1024)); err == nil && json.Unmarshal(b, &body) == nil {
			e.Code, e.Message = body.Error.Code, body.Error.Message
		}
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(min(s, 3600)) * time.Second
	}
	return e
}

// ownersServerClient is the HTTP client for a server taken for the owner's
// (Options.ServerIsOwners): its no-link viewers are the owner's windows only
// while it is this machine's, so the connection never leaves it. No proxy,
// a dial only to a loopback address (checked on the address resolved, so no
// spelling of a name gets past it), and a redirect elsewhere refused.
func ownersServerClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return &offMachine{address}
		}
		return nil
	}}
	t.DialContext = dialer.DialContext
	return &http.Client{Transport: t, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if !config.LoopbackHost(req.URL.Hostname()) {
			return fmt.Errorf("host: refusing a redirect off this machine to %s", req.URL.Host)
		}
		return nil
	}}
}

// LoopbackServer reports whether a server URL names this machine (localhost
// or a loopback address): its workbench is the owner's, so a viewer it sends
// with no link is one of the owner's own windows (Options.ServerIsOwners).
func LoopbackServer(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && config.LoopbackHost(u.Hostname())
}

// PlainServerError refuses a server, or a redirect's target, that is plain
// (http or ws) on another machine without Options.Insecure (Uplink.Insecure):
// what passes between the host and it would travel in the clear and
// unverified. Host is the server's host and port, nothing else of its URL.
type PlainServerError struct {
	Host string
}

func (e *PlainServerError) Error() string {
	return fmt.Sprintf("host: %s is a plain http server, and not localhost or a loopback address: what passes between it and this host would travel in the clear and unverified; use https, or allow it with --insecure (rendezvous.insecure for a server's switchyard)", e.Host)
}

// redirectRefused is a redirect the dial's policy refused (redirectRule):
// err is why, in words that name the redirect's host at most. The HTTP
// client wraps it with the whole URL the redirect gave, which the dial
// drops (dialAndRegister).
type redirectRefused struct{ err error }

func (r *redirectRefused) Error() string { return r.err.Error() }
func (r *redirectRefused) Unwrap() error { return r.err }

// plainRefused is the rule a server URL, or a redirect's, is held to:
// a PlainServerError for one plain on another machine unless insecure.
func plainRefused(server string, insecure bool) error {
	if insecure || !config.PlainOffMachine(server) {
		return nil
	}
	return &PlainServerError{Host: serverHost(server)}
}

// serverHost is a server URL's host and port alone, for a message or the
// log: never its user information, path or query.
func serverHost(server string) string {
	if u, err := url.Parse(server); err == nil {
		return u.Host
	}
	return ""
}

// redirectRule is the control connection's dial's redirect policy: no dial
// follows more than ten; next (the owner's client's, which keeps to this
// machine) has its say; then a redirect is refused as the URL it leads to
// would be (plainRefused), and one followed to a plain server on another
// machine with Insecure is logged.
// Each refusal is a redirectRefused, whose words name the host at most: the
// dial returns those words alone (dialAndRegister).
func (a *agent) redirectRule(next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return &redirectRefused{errors.New("host: stopped after 10 redirects")}
		}
		if next != nil {
			if err := next(req, via); err != nil {
				return &redirectRefused{err}
			}
		}
		if err := plainRefused(req.URL.String(), a.opts.Insecure); err != nil {
			return &redirectRefused{err}
		}
		if config.PlainOffMachine(req.URL.String()) {
			a.log.Warn("--insecure: following a redirect to a plain http server that is not localhost or a loopback address; what passes between it and this host travels in the clear and unverified", "server", req.URL.Host)
		}
		return nil
	}
}
