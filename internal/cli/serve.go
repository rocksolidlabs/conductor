package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/certs"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/reach"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/store"
	"github.com/rocksolidlabs/conductor/internal/version"
	"github.com/rocksolidlabs/conductor/internal/web"
)

// writeAssets is agents.WriteAssets: a test replaces it to make a mode fail.
var writeAssets = agents.WriteAssets

// extensions is ext.Freeze: the composition root (runServe, runHost) reads
// the registry through it, once, at start, which closes it. A test replaces
// it instead of registering into the process's registry.
var extensions = ext.Freeze

// newServer is api.New given the providers the composition root read: a
// test replaces it to see what runServe hands the server, as it replaces
// extensions.
var newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers) (*api.Server, error) {
	return api.New(cfg, cat, log, ui, st, api.WithExtensions(p))
}

// extensionsLine names what the registry holds, kind and name, in its
// order ("identity cloud, notifications push"); "" when nothing is
// registered, as in a plain build.
func extensionsLine(p ext.Providers) string {
	parts := make([]string, 0, len(p.All))
	for _, e := range p.All {
		parts = append(parts, e.Kind+" "+e.Name)
	}
	return strings.Join(parts, ", ")
}

// terminalOf is w when it is a terminal, where the person who started the
// server reads it, and nil otherwise (a file, or a pipe to a parent process,
// a service manager or a container's log): where a plain line says where a
// generated workbench token is. A test replaces it.
var terminalOf = func(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return f
	}
	return nil
}

func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to a JSON config file")
	listen := fs.String("listen", "", "listen address (overrides config)")
	dev := fs.Bool("dev", false, "allow the Nuxt dev server origin and CORS from localhost")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	examples := fs.Bool("examples", false, "seed the example crews once (env CONDUCTOR_EXAMPLES=1); a crew whose id exists is left alone")
	yolo := fs.Bool("yolo", false, "launch every agent with its yolo recipe, skipping its permission prompts, unless a launch or a crew says otherwise (env CONDUCTOR_YOLO=1)")
	printListen := fs.Bool("print-listen", false, "print one JSON line to stdout once listening: {listen, publicUrl, pid, version, workbenchToken?, tlsListen?} (for a parent process such as the desktop app)")
	exitOnStdinClose := fs.Bool("exit-on-stdin-close", false, "shut down when stdin closes (a parent process that dies takes the server with it)")
	switchyard := fs.Bool("switchyard", false, "coordinate hosted sessions and launch nothing: no sessions, crews, runs or catalog of its own (env CONDUCTOR_SWITCHYARD=1; conductor switchyard)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return 2, fmt.Errorf("invalid log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return 1, err
	}
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}
	notice, err := cfg.ResolveDataDir(*configPath)
	if err != nil {
		return 1, err
	}
	if notice != "" {
		log.Warn(notice)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *dev {
		cfg.Dev = true
	}
	if *examples {
		cfg.Examples = true
	}
	if *yolo {
		cfg.Yolo = true
	}
	if *switchyard {
		cfg.Switchyard.Enabled = true
	}
	// With the flags applied, before anything is written, started or bound.
	if cfg.PublishesToItself() {
		return 1, fmt.Errorf("%w: %s is this server, by its publicUrl or a port it listens on; name the switchyard in rendezvous.server (CONDUCTOR_RENDEZVOUS_SERVER), or turn publishing off (rendezvous.enabled false, CONDUCTOR_RENDEZVOUS=0)", config.ErrPublishesToItself, config.ShownURL(cfg.RendezvousServer()))
	}
	if cfg.Yolo {
		log.Warn("yolo is on: agents launch with their yolo recipes and skip their permission prompts (Codex's also drops its sandbox)")
	}
	if cfg.WorkbenchToken == "" {
		tok, _ := share.NewToken()
		cfg.WorkbenchToken = tok
		cfg.GeneratedWorkbenchToken = true
	}
	cat, err := cfg.LoadCatalog()
	if err != nil {
		return 1, err
	}
	// The data directory reserved before anything is written into it, and
	// let go once New has returned, by when the ledger holds its owner lock
	// and the start-up's writes are made: a restore and a starting server
	// exclude each other (internal/export, restore.lock), and a restore
	// refuses a running server by its ledger and its data.
	res, err := export.Reserve(cfg.DataDir)
	if errors.Is(err, export.ErrReserved) {
		return 1, fmt.Errorf("the data directory %s is not served: %w", cfg.DataDir, err)
	}
	if err != nil {
		return 1, fmt.Errorf("data directory %s is not usable (%w); set dataDir in the config or CONDUCTOR_DATA_DIR to a writable directory", cfg.DataDir, err)
	}
	defer res.Release()
	if serveReserved != nil {
		serveReserved(cfg.DataDir)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return 1, fmt.Errorf("data directory %s is not usable (%w); set dataDir in the config or CONDUCTOR_DATA_DIR to a writable directory", cfg.DataDir, err)
	}
	// A generated workbench token is kept in the data directory while this
	// server runs (tokenfile.go); a configured one leaves no file there, but
	// one another running server keeps stays.
	tokenFile := filepath.Join(st.Dir(), workbenchTokenFile)
	tokenKept := false
	if cfg.GeneratedWorkbenchToken {
		forget, err := keepWorkbenchToken(st, cfg.WorkbenchToken)
		switch {
		case errors.Is(err, errTokenFileHeld):
			// The token is not in the file, and its value is never printed:
			// without a parent to hand it to (--print-listen), nobody could
			// sign in to this server.
			if !*printListen {
				return 1, fmt.Errorf("another server running on the data directory %s keeps the workbench token file, so this one's generated token would be nowhere; set CONDUCTOR_WORKBENCH_TOKEN (or workbenchToken in the config), or give this server a dataDir of its own", st.Dir())
			}
			// Otherwise said below, with the token's own line.
		case err != nil:
			return 1, fmt.Errorf("write the generated workbench token to %s (%w); set CONDUCTOR_WORKBENCH_TOKEN (or workbenchToken in the config) to choose one", tokenFile, err)
		default:
			tokenKept = true
			defer forget()
		}
	} else if err := dropStaleWorkbenchToken(st); errors.Is(err, errTokenFileHeld) {
		log.Info("the workbench token file belongs to another server running on this data directory and stays", "file", tokenFile)
	} else if err != nil {
		log.Warn("the workbench token file of an earlier run could not be removed", "file", tokenFile, "err", err)
	}
	// Launches inject flags that name the hook assets in the data directory,
	// and the assets run this binary: the server does not start without them.
	exe, err := agents.BinaryPath()
	if err != nil {
		return 1, fmt.Errorf("locate the conductor binary for the hook assets: %w", err)
	}
	if err := writeAssets(agents.HooksDir(cfg.DataDir), exe); err != nil {
		var me *agents.ModeError
		if !errors.As(err, &me) {
			return 1, fmt.Errorf("write the hook assets to %s: %w", agents.HooksDir(cfg.DataDir), err)
		}
		log.Warn("serving with hook assets whose modes could not be set", "dir", agents.HooksDir(cfg.DataDir), "err", err)
	}
	if root := cfg.DataDirOverlap(); root != "" {
		log.Warn("the data directory overlaps an allowed root: agents working there can read it and commit its secrets, and the file viewer refuses it; keep it outside allowedRoots (the default is ~/.conductor), or set dataDir or CONDUCTOR_DATA_DIR to a directory outside them", "dataDir", cfg.DataDir, "allowedRoot", root)
	}

	ui := web.Handler()
	if ui == nil {
		log.Warn("web UI is not embedded; run `make web-build` before building, API only")
	}
	// The registry, read once: every slot nothing is registered in holds
	// its free default (local.Fill, the one place that asks), and the
	// server holds every provider behind its guard from before its first
	// write. A plain build registers nothing and says nothing of it.
	providers := local.Fill(extensions())
	if line := extensionsLine(providers); line != "" {
		log.Info("extensions: " + line)
	}
	srv, err := newServer(cfg, cat, log, ui, st, providers)
	res.Release()
	if err != nil {
		return 1, err
	}
	// The server now runs goroutines of its own (a provider's queue drains
	// on the server's context, not ctx): a start that fails below, or a
	// Serve that ends, shuts it down as shutdown does, so a failed run in a
	// process that goes on leaves nothing of it running.
	shutDown := false
	defer func() {
		if shutDown {
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if cfg.Switchyard.Enabled {
		log.Info("switchyard: coordinating hosted sessions and launching nothing", "relay", cfg.SwitchyardRelay(), "origins", strings.Join(cfg.SwitchyardOrigins(), ","))
	}
	// Paste invites: the session's side of a WebRTC connection with no
	// server between the two, answered by the host agent's peer code.
	srv.SetPasteAnswerer(func(ctx context.Context, local *session.Local, offer string, role session.Role, label string, ice []proto.ICEServer) (api.PastePeer, string, error) {
		return hostagent.AnswerPaste(ctx, local, offer, hostagent.PasteOptions{Role: role, Name: label, ICE: hostagent.ICE{UDPPort: cfg.ICE.UDPPort, PublicIP: cfg.ICE.PublicIP}, ICEServers: ice, Log: log})
	})
	if cfg.Examples {
		added, skipped, err := srv.SeedExampleCrews()
		if err != nil {
			return 1, fmt.Errorf("seed the example crews: %w", err)
		}
		log.Info("example crews", "added", added, "skipped", skipped)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		// No HTTP/2 on the TLS listener: the WebSocket routes are HTTP/1.1
		// ones, and a browser that negotiated h2 would need the extended
		// CONNECT of RFC 8441 for them. Set before any Serve.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return 1, fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	// A local publicUrl follows the port the listener got (--listen :0, the
	// desktop app): the agents' notify URL and local links reach the server.
	// The API reads cfg live, so the change lands before any session starts.
	cfg.PublicURL = localPublicURL(cfg.PublicURL, cfg.PublicURLIsLocal(), ln.Addr())
	log.Info("conductor serving", "version", version.String(), "listen", ln.Addr().String(), "publicUrl", cfg.PublicURL, "agents", len(srv.Catalog().List()), "dataDir", cfg.DataDir)
	if cfg.PublicURLIsLocal() {
		log.Info("share links take the address the workbench is opened at; set publicUrl (CONDUCTOR_PUBLIC_URL) for a fixed one")
	}
	log.Debug("environment", "path", os.Getenv("PATH"))
	if cfg.GeneratedWorkbenchToken {
		// The value is printed nowhere, not even to a terminal, whose output
		// a container or a session recorder may keep as a log: the terminal
		// and the log name the file that holds it. A parent process has the
		// value in the --print-listen line.
		tty := terminalOf(stderr)
		if tokenKept {
			if tty != nil {
				fmt.Fprintf(tty, "Workbench token: generated for this run, in %s (set CONDUCTOR_WORKBENCH_TOKEN to choose your own)\n", tokenFile)
			}
			log.Warn("no workbench token configured; generated one for this run, kept in file while the server runs; set CONDUCTOR_WORKBENCH_TOKEN (or workbenchToken in the config) to choose your own", "file", tokenFile)
		} else {
			if tty != nil {
				fmt.Fprintf(tty, "Workbench token: generated for this run, in the --print-listen line only: another server on this data directory keeps %s (set CONDUCTOR_WORKBENCH_TOKEN to choose your own)\n", tokenFile)
			}
			log.Warn("no workbench token configured; generated one for this run, in the --print-listen line only: another server running on this data directory keeps the token file; set CONDUCTOR_WORKBENCH_TOKEN (or workbenchToken in the config), or give this server a dataDir of its own", "file", tokenFile)
		}
	}
	if cfg.WorkbenchTokenRenamed {
		log.Warn("adminToken and CONDUCTOR_ADMIN_TOKEN are the old names of the workbench token; use workbenchToken or CONDUCTOR_WORKBENCH_TOKEN")
	}

	ctx, stdinCancel := context.WithCancel(ctx)
	defer stdinCancel()
	mctx, mcancel := context.WithCancel(ctx)
	go srv.RunMaintenance(mctx)
	// The publisher is in place before a provider starts: a session a
	// provider's Start launches at once is published as any other is.
	if server := cfg.RendezvousServer(); server != "" {
		rv := cfg.Rendezvous
		srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: server, Token: rv.Token, HostName: rv.HostName, RelayOnly: rv.RelayOnly, Insecure: rv.Insecure, ICE: hostagent.ICE{UDPPort: cfg.ICE.UDPPort, PublicIP: cfg.ICE.PublicIP}, Log: log}})
		log.Info("sessions are published to the switchyard", "server", config.ShownURL(server), "hostToken", rv.Token != "")
		if rv.Insecure && config.PlainOffMachine(server) {
			// Once per start: the config allowed it (rendezvous.insecure).
			log.Warn("rendezvous.insecure: the switchyard is plain http and not localhost or a loopback address; what this server publishes there travels in the clear and unverified", "server", urlHost(server))
		}
	} else if !cfg.Switchyard.Enabled {
		log.Info("sessions are not published to a switchyard; share links work where this server is reachable")
	}
	// The providers that need the server start now, before the listeners
	// serve, on the maintenance context, which shutdown ends: Start
	// returns at once, and one that fails is tried again with backoff
	// while its slots answer their free defaults (a plain build starts
	// nothing).
	srv.StartExtensions(mctx)
	// The agents' lookups and probes, so the first Launch dialog lists them at once.
	go srv.WarmCatalog(mctx)

	errCh := make(chan error, 2)
	go func() { errCh <- httpSrv.Serve(ln) }()
	var certMgr *certs.Manager
	var tlsLn net.Listener
	if cfg.TLSEnabled() {
		certMgr, tlsLn, err = startTLS(mctx, cfg, log)
		if err != nil {
			mcancel()
			_ = ln.Close()
			return 1, err
		}
		go func() { errCh <- httpSrv.Serve(tlsLn) }()
	}
	mapper := startReach(mctx, cfg, srv, certMgr, portOf(ln), portOf(tlsLn), log)
	shutdown := func() {
		shutDown = true
		mcancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if mapper != nil {
			closeCtx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
			mapper.Close(closeCtx)
			ccancel()
		}
		srv.Shutdown(shutdownCtx)
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("http shutdown", "err", err)
		}
	}
	if *printListen {
		if err := printHandshake(stdout, cfg, ln, tlsLn); err != nil {
			if cfg.GeneratedWorkbenchToken && !tokenKept {
				// The handshake was the one place of this server's token:
				// without it, nobody could sign in.
				shutdown()
				return 1, fmt.Errorf("print-listen: %w; this server's generated workbench token is nowhere else (another server keeps %s), so it stops", err, tokenFile)
			}
			log.Warn("print-listen", "err", err)
		}
	}
	if *exitOnStdinClose {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			log.Info("stdin closed; shutting down")
			stdinCancel()
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		mcancel()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return 1, err
		}
		return 0, nil
	}
	shutdown()
	return 0, nil
}

// uplinkPublisher adapts hostagent.Uplink to api.Publisher.
type uplinkPublisher struct{ u *hostagent.Uplink }

func (p uplinkPublisher) Publish(ctx context.Context, local *session.Local) (api.PublishedSession, error) {
	return p.u.Publish(ctx, local)
}

// Server is the switchyard as a reply shows it (the links' fallback): its
// scheme, host and path (config.ShownURL).
func (p uplinkPublisher) Server() string { return config.ShownURL(p.u.ServerURL) }

// handshake is the JSON line --print-listen writes once the listeners are
// bound: what a parent process needs to open the workbench and sign in.
type handshake struct {
	Listen         string `json:"listen"`
	PublicURL      string `json:"publicUrl"`
	PID            int    `json:"pid"`
	Version        string `json:"version"`
	WorkbenchToken string `json:"workbenchToken,omitempty"`
	TLSListen      string `json:"tlsListen,omitempty"`
}

// printHandshake writes the handshake line. The workbench token goes only when
// the server generated it for this run (a configured one is the parent's
// already); the line is for the parent's pipe, never a log.
func printHandshake(w io.Writer, cfg *config.Config, ln, tlsLn net.Listener) error {
	h := handshake{Listen: ln.Addr().String(), PublicURL: cfg.PublicURL, PID: os.Getpid(), Version: version.String()}
	if cfg.GeneratedWorkbenchToken {
		h.WorkbenchToken = cfg.WorkbenchToken
	}
	if tlsLn != nil {
		h.TLSListen = tlsLn.Addr().String()
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// localPublicURL is publicUrl with the port the listener got, when publicUrl
// names this machine (local) and the listener was bound to another port
// (--listen :0 took a free one): http://localhost:8080 with a listener on
// 127.0.0.1:43123 becomes http://localhost:43123. A publicUrl that names
// another machine, or one that cannot be parsed, is returned as it is.
func localPublicURL(publicURL string, local bool, bound net.Addr) string {
	if !local || bound == nil {
		return publicURL
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return publicURL
	}
	_, port, err := net.SplitHostPort(bound.String())
	if err != nil || port == "" || port == "0" {
		return publicURL
	}
	host := u.Hostname()
	if host == "" {
		host = "localhost"
	}
	u.Host = net.JoinHostPort(host, port)
	return u.String()
}

// startTLS makes the certificate manager and the TLS listener: a certificate
// from ACME (for the public address, or the configured domains) or from
// files, served with TLS 1.2 or later, HTTP/1.1 and the acme-tls/1 protocol
// of the tls-alpn-01 challenge. HTTP/2 is not offered: the WebSocket routes
// are HTTP/1.1 ones. Nothing is served before the first issuance.
func startTLS(ctx context.Context, cfg *config.Config, log *slog.Logger) (*certs.Manager, net.Listener, error) {
	o := certs.Options{Dir: filepath.Join(cfg.DataDir, "tls"), CertFile: cfg.TLS.CertFile, KeyFile: cfg.TLS.KeyFile, Log: log}
	if a := cfg.TLS.ACME; a != nil {
		o.Identifiers = a.Domains
		o.Profile = a.Profile
		o.Challenge = a.Challenge
		o.ACME = &certs.ACMEOptions{Email: a.Email, Directory: a.CADirectory, Challenge: a.Challenge, DNSProvider: a.DNSProvider, DNSEnv: a.DNSEnv}
	}
	m, err := certs.New(o)
	if err != nil {
		return nil, nil, err
	}
	raw, err := net.Listen("tcp", cfg.TLS.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("tls listen %s: %w", cfg.TLS.Listen, err)
	}
	tcfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1", "acme-tls/1"}, GetCertificate: m.GetCertificate}
	go m.Run(ctx)
	st := m.Status()
	log.Info("tls listening", "listen", raw.Addr().String(), "mode", st.Mode, "identifiers", st.Identifiers, "challenge", st.Challenge, "ready", st.Ready)
	return m, tls.NewListener(raw, tcfg), nil
}

// startReach starts the reach mapper the config asks for and gives the server
// its status: in auto, the public address by STUN and, when there is a TLS
// listener, its port on the gateway (reachPorts); in manual, the address
// only. Off starts nothing. With a certificate for the public address to
// obtain, the manager learns the address from the mapper once the port is
// mapped (or forwarded by hand). The mapper runs until ctx ends; the caller
// closes it to delete the mappings.
func startReach(ctx context.Context, cfg *config.Config, srv *api.Server, certMgr *certs.Manager, plainPort, tlsPort uint16, log *slog.Logger) *reach.Mapper {
	var cs certSourceOrNil = nil
	if certMgr != nil {
		cs = certMgr
	}
	if cfg.Reach.Mode == config.ReachOff {
		srv.SetReach(nil, cs)
		log.Info("reach: off; share links take the address the workbench is opened at")
		return nil
	}
	ports := reachPorts(cfg, plainPort, tlsPort)
	m := reach.New(reach.Options{
		Mode:       cfg.Reach.Mode,
		Ports:      ports,
		STUNServer: cfg.STUNServer(),
		Verify:     reach.Verifier{Instance: srv.Instance()}.Verify,
		Log:        log,
	})
	srv.SetReach(m, cs)
	if cfg.Reach.Mode == config.ReachAuto && len(ports) == 0 {
		log.Info("reach: auto finds the public address; nothing is mapped without a TLS listener, and share links take the address the workbench is opened at")
	}
	if certMgr != nil && cfg.TLS.ACME != nil && len(cfg.TLS.ACME.Domains) == 0 {
		m.OnChange(func(st reach.Status) {
			if st.ExternalIP.IsValid() && (st.Mapped || st.Method == reach.MethodManual) {
				certMgr.SetIdentifiers([]string{st.ExternalIP.String()})
			} else {
				certMgr.SetIdentifiers(nil)
			}
		})
	}
	go m.Run(ctx)
	return m
}

// certSourceOrNil lets a nil *certs.Manager reach SetReach as a nil interface.
type certSourceOrNil interface {
	Ready() bool
	Status() certs.Status
	HTTP01(token string) (string, bool)
}

// reachPorts are the ports reach maps or reports: the public port to the
// TLS listener, and 80 to the plain listener when reach.publicPort80 asks
// for it (http-01). Plain http is never mapped on its own.
func reachPorts(cfg *config.Config, plainPort, tlsPort uint16) []reach.PortMap {
	if tlsPort == 0 {
		return nil
	}
	ports := []reach.PortMap{{External: uint16(cfg.Reach.PublicPort), Internal: tlsPort}}
	if cfg.Reach.PublicPort80 && plainPort != 0 {
		ports = append(ports, reach.PortMap{External: 80, Internal: plainPort})
	}
	return ports
}

// portOf is the port a listener is bound to (0 for none).
func portOf(ln net.Listener) uint16 {
	if ln == nil {
		return 0
	}
	if ap, err := netip.ParseAddrPort(ln.Addr().String()); err == nil {
		return ap.Port()
	}
	if _, p, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return uint16(n)
		}
	}
	return 0
}

// exitCodeFromEnv is a helper for tests that want to run serve in-process.
var _ = os.Getenv
