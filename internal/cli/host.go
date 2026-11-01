package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// runHostAgent is hostagent.Run: a test replaces it to see the options.
var runHostAgent = hostagent.Run

// hostingBanner starts the line the host prints on stderr once it has read the
// server's registration reply; the session ID and its URL follow.
const hostingBanner = "conductor: hosting session "

// linkBanner starts the line that gives the link --link asked for: its role
// and URL, or why there is none.
const linkBanner = "conductor: share link "

func runHost(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "conductor server URL, its address alone (env CONDUCTOR_SERVER, default "+defaultServer+")")
	token := fs.String("token", "", "host token (env CONDUCTOR_HOST_TOKEN); none for a switchyard that admits open hosts")
	name := fs.String("name", "", "session name shown in the UI")
	hostName := fs.String("host-name", "", "machine label (default: hostname)")
	agentID := fs.String("agent", "", "agent id shown in the UI (default: command name); the id of an adapter with a launch route (claude, codex, pi, aider) also wires Conductor's hooks into the command at launch; other adapters install by hand (conductor hooks install)")
	cwd := fs.String("cwd", "", "working directory for the command (default: current)")
	relayOnly := fs.Bool("relay-only", false, "never use WebRTC; relay through the server")
	serverIsMine := fs.Bool("server-is-mine", false, "the server is your own, on this machine: a window its workbench opens with no link is yours and gets the editor's Neovim")
	insecure := fs.Bool("insecure", false, "allow a plain http:// (or ws://) server that is not localhost or a loopback address (127.0.0.0/8, ::1), which is refused without it: what passes between this host and the server, the session's output and its viewers' keys among it, then travels in the clear and unverified")
	noLocal := fs.Bool("no-local", false, "do not attach this terminal to the session")
	stun := fs.String("stun", "", "comma separated ICE server URLs overriding the server's list")
	iceUDPPort := fs.Int("ice-udp-port", envInt("CONDUCTOR_ICE_UDP_PORT"), "one UDP port for every WebRTC connection (env CONDUCTOR_ICE_UDP_PORT); 0 lets each connection pick its own")
	icePublicIP := fs.String("ice-public-ip", envOr("CONDUCTOR_ICE_PUBLIC_IP", ""), "the address advertised as this host's own (env CONDUCTOR_ICE_PUBLIC_IP): a forwarder's, such as the desktop app's on Windows in front of WSL")
	scrollback := fs.Int("scrollback", 256<<10, "scrollback bytes replayed to late viewers")
	fileView := fs.String("file-view", "view", "which roles may read files: view, control, off")
	fileEdit := fs.String("file-edit", "control", "whether the control role may edit files through the editor's Neovim on this machine: control, off")
	serverConfig := fs.String("server-config", "", "the config file of a conductor serve on this machine: the Files tab refuses it, its dataDir and its catalogPath (absolute paths), as that server's own sessions do (the data directory CONDUCTOR_DATA_DIR names, ~/.conductor, the desktop app's directories and the catalog file CONDUCTOR_CATALOG_PATH names are refused without it)")
	signalPattern := fs.String("signal-pattern", "", "regular expression (RE2, at most 200 bytes, not matching an empty line) for the last line of the terminal: a match after 500 ms without output marks the session as needing input")
	link := fs.String("link", "", "once registered, have the server mint a share link to the session, good for two hours, and print it: view or control (the way to share a session hosted on a switchyard); SIGUSR1 mints another")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: conductor host [flags] -- <command...>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fs.Usage()
		return 2, errors.New("a command is required after --")
	}
	// The registry is closed in host mode too, so no provider registers
	// mid-run; nothing of it is used here until the Runtime seam (C10).
	_ = extensions()
	// The environment's server is read once the flags are, as conductor up
	// reads it: never a flag's default, which usage messages print.
	*server = cmp.Or(*server, os.Getenv("CONDUCTOR_SERVER"), defaultServer)
	// Before anything is dialed or printed: a server is named by its address
	// alone (config.ServerURLExtra).
	if config.ServerURLExtra(*server) {
		return 2, errors.New("--server (CONDUCTOR_SERVER) must carry no user name, password, query or fragment: a host token goes in --token (CONDUCTOR_HOST_TOKEN)")
	}
	// Before anything is dialed: a plain server on another machine only on
	// the person's word, and then said out loud (hostagent.Run logs it too).
	if config.PlainOffMachine(*server) {
		const what = "what passes between it and this host, the session's output and its viewers' keys among it,"
		if !*insecure {
			return 2, plainServerRefusal(*server, what)
		}
		fmt.Fprintln(stderr, "conductor host: "+plainServerWarning(*server, what))
	}
	if *token == "" {
		*token = os.Getenv("CONDUCTOR_HOST_TOKEN")
	}
	// No token is fine on a switchyard that admits open hosts; elsewhere
	// the server answers 401 and says so.
	switch *fileView {
	case "view", "control", "off":
	default:
		return 2, fmt.Errorf("invalid --file-view %q", *fileView)
	}
	if *fileEdit != "control" && *fileEdit != "off" {
		return 2, fmt.Errorf("invalid --file-edit %q", *fileEdit)
	}
	if *link != "" && *link != "view" && *link != "control" {
		return 2, fmt.Errorf("invalid --link %q: view or control", *link)
	}
	// The Files tab of a hosted session refuses what a session of the
	// server on this machine refuses: its data directory, config file and
	// catalog file, and the copies beside the two files, followed while the
	// session runs.
	deny, err := config.LocalFileDenyFunc(*serverConfig)
	if err != nil {
		return 2, fmt.Errorf("the files of the server on this machine, which the Files tab refuses: %w", err)
	}
	var pattern *regexp.Regexp
	if *signalPattern != "" {
		re, err := catalog.CompilePattern(*signalPattern)
		if err != nil {
			return 2, fmt.Errorf("invalid --signal-pattern: %w", err)
		}
		pattern = re
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return 2, fmt.Errorf("invalid log level %q", *logLevel)
	}
	// Logs go to stderr; when the local terminal is attached they would
	// corrupt the PTY output, so keep them at warn unless asked otherwise.
	logOut := stderr
	if !*noLocal && *logLevel == "info" {
		level = slog.LevelWarn
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))

	var ice []proto.ICEServer
	if *stun != "" {
		for _, u := range strings.Split(*stun, ",") {
			if u = strings.TrimSpace(u); u != "" {
				ice = append(ice, proto.ICEServer{URLs: []string{u}})
			}
		}
	}
	if err := serverIsMineOK(*serverIsMine, *server); err != nil {
		return 2, err
	}
	opts := hostagent.Options{
		ICE:             hostagent.ICE{UDPPort: *iceUDPPort, PublicIP: *icePublicIP},
		ServerURL:       *server,
		Token:           *token,
		Name:            *name,
		HostName:        *hostName,
		AgentID:         *agentID,
		Argv:            argv,
		Dir:             *cwd,
		RelayOnly:       *relayOnly,
		Insecure:        *insecure,
		ServerIsOwners:  *serverIsMine,
		LocalAttach:     !*noLocal,
		ICEServers:      ice,
		ScrollbackBytes: *scrollback,
		FileView:        *fileView,
		FileEdit:        *fileEdit,
		FileDeny:        deny,
		Pattern:         pattern,
		Adapter:         *agentID,
		Log:             log,
		Registered: func(sessionID, base string) {
			fmt.Fprintf(stderr, "%s%s at %s/sessions/%s\r\n", hostingBanner, sessionID, base, sessionID)
		},
		Link:    *link,
		LinkTTL: 2 * time.Hour,
		// The link goes to the terminal, never to the log: it is the credential.
		LinkMinted: func(l proto.LinkCreated, err error) {
			if err != nil {
				fmt.Fprintf(stderr, "%sno link: %v (kill -USR1 %d to ask again)\r\n", linkBanner, err, os.Getpid())
				return
			}
			fmt.Fprintf(stderr, "%s%s %s (kill -USR1 %d for another)\r\n", linkBanner, l.Role, l.URL, os.Getpid())
		},
	}
	if *link != "" {
		// SIGUSR1 from the owner mints another link: the first lasts two
		// hours, and the server's workbench token may mint none (a switchyard).
		again := make(chan struct{}, 1)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGUSR1)
		defer func() {
			signal.Stop(sig)
			close(sig)
		}()
		go func() {
			for range sig {
				select {
				case again <- struct{}{}:
				default:
				}
			}
		}()
		opts.LinkAgain = again
	}
	if f, ok := stdin.(*os.File); ok {
		opts.Stdin = f
	}
	if f, ok := stdout.(*os.File); ok {
		opts.Stdout = f
	}
	res, err := runHostAgent(ctx, opts)
	if err != nil {
		return 1, err
	}
	return res.ExitCode, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt reads an integer from the environment; unset or unreadable is 0.
func envInt(key string) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return 0
	}
	return n
}

// serverIsMineOK refuses --server-is-mine for a server that is not on this
// machine: a window a server sends with no link is taken for the owner's only
// on the person's word and only for a server here (the connection is then
// held to this machine; hostagent.Options.ServerIsOwners).
func serverIsMineOK(mine bool, server string) error {
	if mine && !hostagent.LoopbackServer(server) {
		return fmt.Errorf("--server-is-mine needs a server on this machine (localhost), not %s", cmp.Or(urlHost(server), "this one"))
	}
	return nil
}

// plainServerRefusal is the error for a server that is plain http on another
// machine (config.PlainOffMachine) without --insecure: what would travel in
// the clear, and the flag that allows it. It names the server's host alone:
// the URL may carry a password.
func plainServerRefusal(server, what string) error {
	return fmt.Errorf("the server %s is plain http and not localhost or a loopback address: %s would travel in the clear and unverified; use https, or pass --insecure to go ahead", urlHost(server), what)
}

// plainServerWarning is what is printed on stderr when --insecure lets such
// a server through.
func plainServerWarning(server, what string) string {
	return fmt.Sprintf("warning: --insecure: the server %s is plain http and not localhost or a loopback address: %s travels in the clear and unverified", urlHost(server), what)
}

// urlHost is a URL's host and port, nothing else of it; "" for one that does
// not parse.
func urlHost(server string) string {
	if u, err := url.Parse(server); err == nil {
		return u.Host
	}
	return ""
}
