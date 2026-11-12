package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/session"
)

func runHostWith(t *testing.T, args ...string) (int, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	code, err := runHost(t.Context(), args, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	return code, stderr.String(), err
}

// The pattern is checked before the host dials anything, with the rule the
// catalog holds an agent's pattern to: RE2, at most 200 bytes.
func TestHostRejectsABadSignalPattern(t *testing.T) {
	clearConductorEnv(t)
	for _, c := range []struct{ name, pattern, want string }{
		{"not a regular expression", "(", "missing closing"},
		{"longer than 200 bytes", strings.Repeat("a", 201), "at most 200 bytes"},
		{"matches an empty line", `.*`, "must not match an empty line"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--token", "t", "--signal-pattern", c.pattern, "--", "sh")
			if code != 2 || err == nil || !strings.Contains(err.Error(), "invalid --signal-pattern") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("exit %d, error %v, want one about %q", code, err, c.want)
			}
		})
	}
}

// The flag reaches the hosted session: the prompt the process leaves on its
// last line marks the session needs_input on the server, with source pattern.
func TestHostSignalPatternReachesTheSession(t *testing.T) {
	clearConductorEnv(t)
	cfg := config.Defaults()
	cfg.WorkbenchToken = "admin-token"
	cfg.HostTokens = []string{"host-token"}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var stderr bytes.Buffer
		args := []string{"--server", hs.URL, "--token", "host-token", "--no-local", "--relay-only", "--name", "asker",
			"--signal-pattern", `\(y/n\) $`, "--", "/bin/sh", "-c", `printf 'Continue? (y/n) '; exec /bin/cat`}
		if code, err := runHost(ctx, args, strings.NewReader(""), &bytes.Buffer{}, &stderr); err != nil {
			t.Errorf("host: exit %d, %v, %s", code, err, stderr.String())
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var att session.Attention
		for _, info := range srv.Registry().List() {
			if info.Name == "asker" {
				att = info.Attention
			}
		}
		if att.State == session.AttentionNeedsInput {
			if att.Source != session.SourcePattern || att.Message != "prompt: Continue? (y/n)" {
				t.Fatalf("attention %+v", att)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hosted session never needed input: %+v", srv.Registry().List())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("host did not stop")
	}
}

// registrationWatch is a host's stderr. registered is closed once the host has
// printed hostingBanner, which it prints after reading its registration reply,
// which the server sends after listing the session.
type registrationWatch struct {
	registered chan struct{}

	mu   sync.Mutex
	buf  bytes.Buffer
	seen bool
}

func newRegistrationWatch() *registrationWatch {
	return &registrationWatch{registered: make(chan struct{})}
}

func (w *registrationWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if !w.seen && strings.Contains(w.buf.String(), hostingBanner) {
		w.seen = true
		close(w.registered)
	}
	return n, err
}

func (w *registrationWatch) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// --agent picks the adapter: the hooks go to the host's hooks dir,
// ~/.conductor/hooks (XDG_STATE_HOME holds no older one, so it is ignored),
// and the session is launched with the adapter's flags.
func TestHostAgentFlagInjectsTheAdapter(t *testing.T) {
	clearConductorEnv(t)
	// The host writes its hook assets, which records their binary for the
	// whole process: forget it when the test ends.
	t.Cleanup(agents.ForgetBinary())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	cfg := config.Defaults()
	cfg.WorkbenchToken = "admin-token"
	cfg.HostTokens = []string{"host-token"}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := newRegistrationWatch()
	done := make(chan struct{})
	go func() {
		defer close(done)
		args := []string{"--server", hs.URL, "--token", "host-token", "--no-local", "--relay-only", "--name", "hooked",
			"--agent", "claude", "--", "/bin/cat"}
		if code, err := runHost(ctx, args, strings.NewReader(""), &bytes.Buffer{}, stderr); err != nil {
			t.Errorf("host: exit %d, %v, %s", code, err, stderr)
		}
	}()
	// The server lists the session before it sends the registration reply, so
	// the listing alone does not say the host is registered: cancelled before
	// it reads the reply, the host fails. Its banner comes after the reply.
	select {
	case <-stderr.registered:
	case <-done:
		t.Fatalf("the host stopped before it registered: %s", stderr)
	case <-time.After(10 * time.Second):
		t.Fatalf("the hosted session never registered: %+v %s", srv.Registry().List(), stderr)
	}
	settings := filepath.Join(os.Getenv("HOME"), ".conductor", "hooks", "claude.json")
	var command []string
	var agentID string
	for _, info := range srv.Registry().List() {
		if info.Name == "hooked" {
			command, agentID = info.Command, info.AgentID
		}
	}
	if !slices.Equal(command, []string{"/bin/cat", "--settings", settings}) || agentID != "claude" {
		t.Fatalf("server lists %q for agent %q", command, agentID)
	}
	if b, err := os.ReadFile(settings); err != nil || !strings.Contains(string(b), "notify --claude-hook") {
		t.Fatalf("asset: %v %s", err, b)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("host did not stop")
	}
}

// No token is no longer refused up front: an open switchyard admits such a
// host, and any other server answers 401 itself. Here the dial fails, which
// is the server's answer, not the flag check's.
func TestHostNeedsNoToken(t *testing.T) {
	clearConductorEnv(t)
	code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh")
	if err == nil || code == 2 || strings.Contains(err.Error()+stderr, "host token is required") {
		t.Fatalf("code %d, err %v, stderr %q", code, err, stderr)
	}
}

// conductor host hands its session the deny list of the server on its
// machine (config.LocalFileDenyFunc): ~/.conductor and the desktop app's
// directories, and with --server-config that file, its dataDir and its
// catalogPath, the copies beside the two files included. A config file it
// cannot read or parse stops it before it dials.
func TestHostRefusesTheServersFilesToItsSession(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	home := os.Getenv("HOME")
	// The desktop app's directories are in the user's configuration
	// directory as the product finds it: ~/.config on Linux,
	// ~/Library/Application Support on macOS. The host's own state
	// directory (config.HostStateDir) is refused too.
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	local := []string{filepath.Join(home, ".conductor"), filepath.Join(home, ".local", "state", "conductor"), filepath.Join(home, ".local", "share", "conductor", "data"), filepath.Join(config, "conductor-desktop"), filepath.Join(config, "Conductor")}
	var desktopSettings []string
	for _, name := range []string{"conductor-desktop", "Conductor"} {
		desktopSettings = append(desktopSettings, filepath.Join(config, name, "settings.json"), filepath.Join(config, name, "*settings.json*"))
	}
	var got []hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = append(got, opts)
		return hostagent.Result{}, nil
	}
	t.Cleanup(func() { runHostAgent = hostagent.Run })

	if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 0 || err != nil {
		t.Fatalf("exit %d, %v, %s", code, err, stderr)
	}
	if want := slices.Concat(local, desktopSettings); len(got) != 1 || !slices.Equal(got[0].FileDeny(), want) {
		t.Fatalf("without --server-config: %+v, want the deny list %q", got, want)
	}

	dir := realTempDir(t)
	data, catalogFile := filepath.Join(dir, "state"), filepath.Join(dir, "agents.json")
	cfg := writeServeConfig(t, dir, `{"workbenchToken":"w","dataDir":"`+data+`","catalogPath":"`+catalogFile+`"}`)
	got = nil
	if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--server-config", cfg, "--", "sh"); code != 0 || err != nil {
		t.Fatalf("exit %d, %v, %s", code, err, stderr)
	}
	want := slices.Concat([]string{data}, local, []string{cfg, filepath.Join(dir, "*conductor.json*"), catalogFile, filepath.Join(dir, "*agents.json*")}, desktopSettings)
	if len(got) != 1 || !slices.Equal(got[0].FileDeny(), want) {
		t.Fatalf("with --server-config: %+v\nwant the deny list %q", got, want)
	}

	// A file it cannot read or parse, and a relative dataDir or
	// catalogPath, which serve resolves against a directory the host
	// cannot know, stop it: what they name would go unrefused.
	got = nil
	for _, c := range []struct{ path, want string }{
		{filepath.Join(dir, "missing.json"), "read config"},
		{writeServeConfig(t, t.TempDir(), `{"dataDirectory":"x"}`), "parse config"},
		{writeServeConfig(t, t.TempDir(), `{"dataDir":"state"}`), "dataDir in "},
		{writeServeConfig(t, t.TempDir(), `{"catalogPath":"agents.json"}`), "catalogPath in "},
	} {
		code, _, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--server-config", c.path, "--", "sh")
		if code != 2 || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: exit %d, %v; want an error about %q", c.path, code, err, c.want)
		}
	}
	t.Setenv("CONDUCTOR_DATA_DIR", "state")
	if code, _, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 2 || err == nil || !strings.Contains(err.Error(), "CONDUCTOR_DATA_DIR") {
		t.Errorf("a relative CONDUCTOR_DATA_DIR: exit %d, %v", code, err)
	}
	t.Setenv("CONDUCTOR_DATA_DIR", "")
	// The desktop app's settings there but not readable for their data directory.
	settings := filepath.Join(config, "conductor-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 2 || err == nil || !strings.Contains(err.Error(), settings) {
		t.Errorf("unreadable desktop settings: exit %d, %v", code, err)
	}
	if len(got) != 0 {
		t.Fatalf("hosted with server files it could not place: %+v", got)
	}
}

// A plain server on another machine (http or ws, from --server or
// CONDUCTOR_SERVER) is refused before the host runs anything or dials: exit
// 2, a message that names the server's host alone and --insecure. One on
// this machine needs nothing, and draws no warning.
func TestHostRefusesPlainServerWithoutInsecure(t *testing.T) {
	clearConductorEnv(t)
	var got []hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = append(got, opts)
		return hostagent.Result{}, nil
	}
	t.Cleanup(func() { runHostAgent = hostagent.Run })
	for _, server := range []string{"http://yard.example:8080", "ws://yard.example:8080", "http://yard.example:8080/", "HTTP://yard.example:8080"} {
		code, stderr, err := runHostWith(t, "--server", server, "--no-local", "--", "sh")
		if code != 2 || err == nil || !strings.Contains(err.Error(), "the server yard.example:8080 is plain http and not localhost or a loopback address") || !strings.Contains(err.Error(), "pass --insecure to go ahead") {
			t.Errorf("%s: exit %d, %v, %q", server, code, err, stderr)
		}
	}
	t.Setenv("CONDUCTOR_SERVER", "http://192.0.2.7:8080")
	if code, _, err := runHostWith(t, "--no-local", "--", "sh"); code != 2 || err == nil || !strings.Contains(err.Error(), "the server 192.0.2.7:8080 is plain http and not localhost or a loopback address") {
		t.Errorf("CONDUCTOR_SERVER: exit %d, %v", code, err)
	}
	// localhost in another case is not taken for this machine: a proxy the
	// environment names would carry it.
	t.Setenv("CONDUCTOR_SERVER", "")
	if code, _, err := runHostWith(t, "--server", "http://LOCALHOST:8080", "--no-local", "--", "sh"); code != 2 || err == nil || !strings.Contains(err.Error(), "the server LOCALHOST:8080 is plain http and not localhost or a loopback address") {
		t.Errorf("LOCALHOST: exit %d, %v", code, err)
	}
	if len(got) != 0 {
		t.Fatalf("hosted with a plain server on another machine: %+v", got)
	}
	t.Setenv("CONDUCTOR_SERVER", "")
	for _, server := range []string{"http://localhost:8080", "http://127.0.0.1:1", "http://[::1]:8080", "ws://localhost:8080", "https://yard.example"} {
		got = nil
		code, stderr, err := runHostWith(t, "--server", server, "--no-local", "--", "sh")
		if code != 0 || err != nil || len(got) != 1 || got[0].Insecure || strings.Contains(stderr, "warning") {
			t.Errorf("%s: exit %d, %v, %q, %+v", server, code, err, stderr, got)
		}
	}
}

// A server is named by its address alone: --server or CONDUCTOR_SERVER with
// user information, a query or a fragment is refused before the host runs
// anything or dials, in words that repeat nothing of it; --server-is-mine's
// refusal names the host alone.
func TestHostRefusesAServerURLThatCarriesMore(t *testing.T) {
	clearConductorEnv(t)
	var got []hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = append(got, opts)
		return hostagent.Result{}, nil
	}
	t.Cleanup(func() { runHostAgent = hostagent.Run })
	const want = "--server (CONDUCTOR_SERVER) must carry no user name, password, query or fragment: a host token goes in --token (CONDUCTOR_HOST_TOKEN)"
	for _, server := range []string{"https://me:SECRET@yard.example", "https://SECRET@yard.example", "https://:SECRET@yard.example", "http://@localhost:8080", "https://yard.example/?token=SECRET", "https://yard.example#SECRET", "http://me:SECRET@yard.example:8080"} {
		code, stderr, err := runHostWith(t, "--server", server, "--no-local", "--", "sh")
		if code != 2 || err == nil || err.Error() != want || strings.Contains(stderr, "SECRET") {
			t.Errorf("--server %s: exit %d, %v, %q", server, code, err, stderr)
		}
		t.Setenv("CONDUCTOR_SERVER", server)
		code, stderr, err = runHostWith(t, "--no-local", "--", "sh")
		if code != 2 || err == nil || err.Error() != want || strings.Contains(stderr, "SECRET") {
			t.Errorf("CONDUCTOR_SERVER=%s: exit %d, %v, %q", server, code, err, stderr)
		}
		t.Setenv("CONDUCTOR_SERVER", "")
	}
	if len(got) != 0 {
		t.Fatalf("hosted with a server URL that carries more: %+v", got)
	}
	if err := serverIsMineOK(true, "https://me:SECRET@yard.example/p?t=SECRET"); err == nil || err.Error() != "--server-is-mine needs a server on this machine (localhost), not yard.example" {
		t.Fatalf("--server-is-mine: %v", err)
	}
}

// The server is --server, else CONDUCTOR_SERVER, else the default, read once
// the flags are: --help, a flag it does not know and a missing command print
// the usage, which shows no server the environment names.
func TestHostReadsTheServerFromTheEnvironmentAfterItsFlags(t *testing.T) {
	clearConductorEnv(t)
	var got []hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = append(got, opts)
		return hostagent.Result{}, nil
	}
	t.Cleanup(func() { runHostAgent = hostagent.Run })
	t.Setenv("CONDUCTOR_SERVER", "https://me:SECRET@yard.example")
	for _, args := range [][]string{{"--help"}, {"--nope"}, {"--no-local"}} {
		_, stderr, err := runHostWith(t, args...)
		if !strings.Contains(stderr, "env CONDUCTOR_SERVER, default http://localhost:8080") || strings.Contains(stderr, "SECRET") || (err != nil && strings.Contains(err.Error(), "SECRET")) {
			t.Errorf("%v: %q %v", args, stderr, err)
		}
	}
	for _, tc := range []struct{ env, flag, want string }{
		{"", "", "http://localhost:8080"},
		{"https://yard.example", "", "https://yard.example"},
		{"https://yard.example", "https://other.example", "https://other.example"},
	} {
		got = nil
		t.Setenv("CONDUCTOR_SERVER", tc.env)
		args := []string{"--no-local", "--", "sh"}
		if tc.flag != "" {
			args = append([]string{"--server", tc.flag}, args...)
		}
		if code, _, err := runHostWith(t, args...); code != 0 || err != nil || len(got) != 1 || got[0].ServerURL != tc.want {
			t.Errorf("env %q, flag %q: exit %d, %v, %+v", tc.env, tc.flag, code, err, got)
		}
	}
}

// --insecure lets a plain server on another machine through and says so on
// stderr, before the host runs; the host is told (hostagent.Options.Insecure)
// and logs it too. With a server that is not plain it says nothing.
func TestHostInsecurePrintsTheWarning(t *testing.T) {
	clearConductorEnv(t)
	var got []hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = append(got, opts)
		return hostagent.Result{}, nil
	}
	t.Cleanup(func() { runHostAgent = hostagent.Run })
	code, stderr, err := runHostWith(t, "--server", "http://yard.example:8080", "--insecure", "--no-local", "--", "sh")
	if code != 0 || err != nil || len(got) != 1 || !got[0].Insecure {
		t.Fatalf("exit %d, %v, %+v", code, err, got)
	}
	const want = "conductor host: warning: --insecure: the server yard.example:8080 is plain http and not localhost or a loopback address: what passes between it and this host, the session's output and its viewers' keys among it, travels in the clear and unverified\n"
	if stderr != want {
		t.Fatalf("stderr %q, want %q", stderr, want)
	}
	got = nil
	if code, stderr, err := runHostWith(t, "--server", "https://yard.example", "--insecure", "--no-local", "--", "sh"); code != 0 || err != nil || len(got) != 1 || stderr != "" {
		t.Fatalf("https with --insecure: exit %d, %v, %q", code, err, stderr)
	}
}

// --server-is-mine is the person's word, and only for a server on this
// machine; without it no server's window is taken for the owner's.
func TestServerIsMineOnlyForAServerHere(t *testing.T) {
	for _, tc := range []struct {
		mine   bool
		server string
		ok     bool
	}{
		{false, "http://localhost:8080", true},
		{false, "https://switchyard.example", true},
		{true, "http://localhost:8080", true},
		{true, "http://127.0.0.1:8080", true},
		{true, "https://switchyard.example", false},
		{true, "https://192.168.1.20:8080", false},
	} {
		if err := serverIsMineOK(tc.mine, tc.server); (err == nil) != tc.ok {
			t.Errorf("serverIsMineOK(%v, %q) = %v", tc.mine, tc.server, err)
		}
	}
}

// --link has the server mint a share link over the host's own connection
// and prints it to the terminal: on a switchyard, whose workbench token
// mints none, the way the owner shares a session hosted there.
func TestHostPrintsALinkMintedThroughItsConnection(t *testing.T) {
	clearConductorEnv(t)
	if code, _, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--link", "admin", "--", "sh"); code != 2 || err == nil || !strings.Contains(err.Error(), "invalid --link") {
		t.Fatalf("a bad role: exit %d, %v", code, err)
	}
	cfg := config.Defaults()
	cfg.WorkbenchToken = "admin-token"
	cfg.HostTokens = []string{"host-token"}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Switchyard.Enabled = true
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stderr syncBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		args := []string{"--server", hs.URL, "--token", "host-token", "--no-local", "--relay-only", "--name", "shared", "--link", "control", "--", "/bin/cat"}
		_, _ = runHost(ctx, args, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	}()
	defer func() {
		cancel()
		<-done
	}()
	links := func() []string {
		var out []string
		for _, line := range strings.Split(stderr.String(), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), linkBanner+"control "); ok {
				out = append(out, strings.Fields(rest)[0])
			}
		}
		return out
	}
	waitLinks := func(n int) []string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for len(links()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("%d links printed, want %d: %q", len(links()), n, stderr.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
		return links()
	}
	url := waitLinks(1)[0]
	if !strings.Contains(stderr.String(), fmt.Sprintf("kill -USR1 %d for another", os.Getpid())) {
		t.Fatalf("the line does not say how to get another: %q", stderr.String())
	}
	if !strings.HasPrefix(url, hs.URL+"/join/") {
		t.Fatalf("link %q", url)
	}
	resp, err := http.Get(strings.Replace(url, "/join/", "/api/join/", 1))
	if err != nil {
		t.Fatal(err)
	}
	var join struct {
		Role    string `json:"role"`
		Label   string `json:"label"`
		Session struct {
			Name string `json:"name"`
		} `json:"session"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&join)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || join.Role != "control" || join.Session.Name != "shared" || join.Label != "conductor host" {
		t.Fatalf("join %d %+v", resp.StatusCode, join)
	}
	// SIGUSR1 mints another, while the session runs on.
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if got := waitLinks(2); got[1] == got[0] {
		t.Fatalf("the same link twice: %q", got)
	}
}
