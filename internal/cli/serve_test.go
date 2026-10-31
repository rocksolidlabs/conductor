package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
)

// syncBuffer collects what the server logs while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// terminalStderr is the stderr of a server a test started from a terminal:
// the test's terminalOf answers it with the terminal, and a plain syncBuffer
// (a server started from none) with nil.
type terminalStderr struct{ *syncBuffer }

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// clearConductorEnv keeps the CONDUCTOR_* variables of whoever runs the tests
// out of the configuration: config.Load ignores empty values.
func clearConductorEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "CONDUCTOR_") {
			t.Setenv(k, "")
		}
	}
	// A home of the test's own: nothing a test starts reads or writes the
	// user's ~/.conductor.
	t.Setenv("HOME", realTempDir(t))
	// No STUN or gateway traffic from a test unless it asks for it, and no
	// publishing to the public switchyard: a test's sessions stay here.
	t.Setenv("CONDUCTOR_REACH", "off")
	t.Setenv("CONDUCTOR_RENDEZVOUS", "0")
	// The system's programs only, as CI has: a server warms its catalog as
	// it starts, running each agent found on PATH with --version, and a real
	// agent of this machine's (Copilot unpacks itself into HOME) went on
	// writing into the test's HOME after the test ended, so its cleanup
	// failed (round 14).
	t.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
}

// realTempDir is a temporary directory of the test's with its symbolic
// links resolved: on macOS it is under /var, a link to /private/var, and a
// deny list built from the unresolved path would hold the resolved one
// beside it (session.Leads), where Linux holds one entry per path.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeServeConfig writes body as conductor.json in dir and returns its path.
func writeServeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "conductor.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serveStartWait bounds how long a test waits for a server it started to
// say it is serving: a bound only a hang reaches. Starting opens the
// ledger's and Dispatch's stores, each open and the ledger's recovery
// capped at 30 s by the product, and a machine loaded with other suites
// takes a race build well past any few seconds there (10 s was overrun at
// a load of 77 to 160, #281); the test binary's own timeout stays above
// it (startDeadline).
const serveStartWait = 3 * time.Minute

// startDeadline fires when a test has waited serveStartWait for a server
// to start, or shortly before the test binary's -timeout would end the
// run, so that a hang fails with the server's log rather than a panic. It
// returns the wait it set as well. The margin before -timeout is 10 s, or
// half of what is left when less than 20 s is, so that the wait is never
// zero or negative and a short -timeout still gives the server time.
func startDeadline(t *testing.T) (<-chan time.Time, time.Duration) {
	t.Helper()
	wait := serveStartWait
	if end, ok := t.Deadline(); ok {
		wait = min(wait, startWaitBefore(time.Until(end)))
	}
	return time.After(wait), wait
}

// startWaitBefore is the start wait that leaves the test binary room to
// report a hang before -timeout ends it, given how long is left.
func startWaitBefore(left time.Duration) time.Duration {
	return max(left-10*time.Second, left/2)
}

// A short -timeout still leaves a server time to start: the margin before
// it shrinks to half of what is left rather than making the wait negative,
// which fired at once (-timeout 10s failed in 0.01 s, #281).
func TestStartWaitBeforeIsNeverNegative(t *testing.T) {
	for _, c := range []struct{ left, want time.Duration }{
		{30 * time.Minute, 30*time.Minute - 10*time.Second},
		{30 * time.Second, 20 * time.Second},
		{20 * time.Second, 10 * time.Second},
		{10 * time.Second, 5 * time.Second},
		{2 * time.Second, time.Second},
	} {
		if got := startWaitBefore(c.left); got != c.want {
			t.Errorf("startWaitBefore(%s) = %s, want %s", c.left, got, c.want)
		}
	}
}

// serveUntilListening runs `conductor serve` on a free loopback port until it
// reports that it is serving, stops it, and returns everything it logged.
func serveUntilListening(t *testing.T, args ...string) string {
	t.Helper()
	// Serve records the binary it writes the hook assets for, for the whole
	// process: forget it when the test ends.
	t.Cleanup(agents.ForgetBinary())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs syncBuffer
	done := make(chan error, 1)
	go func() {
		code, err := runServe(ctx, append([]string{"--listen", "127.0.0.1:0"}, args...), strings.NewReader(""), io.Discard, &logs)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit code %d", code)
		}
		done <- err
	}()
	deadline, wait := startDeadline(t)
	for !strings.Contains(logs.String(), "conductor serving") {
		select {
		case err := <-done:
			t.Fatalf("serve returned before serving: %v\n%s", err, logs.String())
		case <-deadline:
			t.Fatalf("serve did not start in %s:\n%s", wait, logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
	return logs.String()
}

// logLines returns the lines of logs that contain every one of parts.
func logLines(logs string, parts ...string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		match := true
		for _, p := range parts {
			match = match && strings.Contains(line, p)
		}
		if match {
			out = append(out, line)
		}
	}
	return out
}

func TestServeLogsTheDataDirectory(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "conductor serving", "dataDir="+data); len(lines) != 1 {
		t.Fatalf("the serving line does not name the data directory:\n%s", logs)
	}
	if fi, err := os.Stat(data); err != nil || !fi.IsDir() {
		t.Fatalf("data directory not created: %v", err)
	}
	// Outside every allowed root, so no warning about it.
	if lines := logLines(logs, "level=WARN", "dataDir="); len(lines) != 0 {
		t.Fatalf("unexpected data directory warning: %v", lines)
	}
}

// Without dataDir the directory is ~/.conductor. Here the home is the
// allowed root: agents work there, so the server says so.
func TestServeWarnsWhenTheDataDirOverlapsAnAllowedRoot(t *testing.T) {
	clearConductorEnv(t)
	home := os.Getenv("HOME")
	cfg := writeServeConfig(t, t.TempDir(), fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q}`, home, home))
	logs := serveUntilListening(t, "--config", cfg)
	data := filepath.Join(home, ".conductor")
	if lines := logLines(logs, "level=WARN", "dataDir="+data, "allowedRoot="+home); len(lines) != 1 {
		t.Fatalf("no warning that %s is inside the allowed root %s:\n%s", data, home, logs)
	}
}

// A data directory that cannot be made stops the server, which says which
// settings choose another.
func TestServeNamesTheSettingWhenTheDataDirIsNotUsable(t *testing.T) {
	clearConductorEnv(t)
	cfg := writeServeConfig(t, t.TempDir(), `{"workbenchToken": "t"}`)
	data := filepath.Join(os.Getenv("HOME"), ".conductor")
	// A file where the directory should be defeats MkdirAll even for root.
	if err := os.WriteFile(data, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A server that starts anyway is stopped by the deadline, so the test
	// fails instead of waiting for the package timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var logs syncBuffer
	code, err := runServe(ctx, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &logs)
	if strings.Contains(logs.String(), "conductor serving") {
		t.Fatalf("serve listened with an unusable data directory:\n%s", logs.String())
	}
	if code != 1 || err == nil {
		t.Fatalf("serve started with an unusable data directory: %d %v", code, err)
	}
	for _, want := range []string{data, "dataDir", "CONDUCTOR_DATA_DIR", "not a directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An upgraded server whose data directory an older Conductor put next to the
// config keeps using it while ~/.conductor holds no server data, and says
// once, at warn, where it is, where the default is and how to move.
func TestServeKeepsAnOldDataDirectoryWithANotice(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work, old := filepath.Join(dir, "work"), filepath.Join(dir, "conductor.d")
	for _, d := range []string{work, old} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q}`, work, work))
	logs := serveUntilListening(t, "--config", cfg)
	def := filepath.Join(os.Getenv("HOME"), ".conductor")
	if lines := logLines(logs, "level=WARN", old, def); len(lines) != 1 {
		t.Fatalf("no notice naming %s and %s:\n%s", old, def, logs)
	}
	if lines := logLines(logs, "conductor serving", "dataDir="+old); len(lines) != 1 {
		t.Fatalf("the server does not use %s:\n%s", old, logs)
	}
	if _, err := os.Stat(def); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s was made: %v", def, err)
	}
}

// Without dataDir and without an old directory the server makes ~/.conductor,
// 0700, names it on its serving line and warns about nothing it holds. (The
// test binary embeds no web UI, which is a warning of its own.)
func TestServeUsesTheHomeDataDirectory(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q}`, work, work))
	logs := serveUntilListening(t, "--config", cfg)
	data := filepath.Join(os.Getenv("HOME"), ".conductor")
	if lines := logLines(logs, "conductor serving", "dataDir="+data); len(lines) != 1 {
		t.Fatalf("the serving line does not name %s:\n%s", data, logs)
	}
	if fi, err := os.Stat(data); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%s: %v %v", data, fi, err)
	}
	if lines := logLines(logs, "level=WARN", ".conductor"); len(lines) != 0 {
		t.Fatalf("unexpected data directory warnings: %v", lines)
	}
}

// The server writes the hook assets into its data directory at startup, naming
// its own binary, before it serves: a launch that injects them finds them.
func TestServeWritesTheHookAssets(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	serveUntilListening(t, "--config", cfg)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(data, "hooks", "claude.json"))
	if err != nil || !strings.Contains(string(b), exe+" notify --claude-hook") {
		t.Fatalf("claude.json: %v\n%s", err, b)
	}
	if fi, err := os.Stat(filepath.Join(data, "hooks")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("hooks dir: %v %v", fi.Mode(), err)
	}
}

// A mode WriteAssets cannot set (agents.ModeError: the assets are in place)
// does not stop the server: it warns, naming the file, and serves. Any other
// failure to write the assets still stops it.
func TestServeGoesOnWhenItCannotSetTheModesOfTheHookAssets(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	asset := filepath.Join(data, "hooks", "claude.json")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	old := writeAssets
	t.Cleanup(func() { writeAssets = old })
	writeAssets = func(hooksDir, bin string) error {
		if err := old(hooksDir, bin); err != nil {
			return err
		}
		return &agents.ModeError{Errs: []error{&fs.PathError{Op: "chmod", Path: asset, Err: fs.ErrPermission}}}
	}
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "level=WARN", asset, "goes on"); len(lines) != 1 {
		t.Fatalf("no warning naming %s:\n%s", asset, logs)
	}

	writeAssets = func(string, string) error { return errors.New("disk full") }
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var out syncBuffer
	code, err := runServe(ctx, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &out)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "disk full") || strings.Contains(out.String(), "conductor serving") {
		t.Fatalf("serve with assets it could not write: %d %v\n%s", code, err, out.String())
	}
}

// The same holds for the mode of a new asset, set on the temporary file
// that is renamed over it: on a first start whose file system keeps no
// modes, the real WriteAssets puts every asset in place, and the server
// warns, naming the asset, and serves.
func TestServeGoesOnWhenItCannotSetTheModeOfANewHookAsset(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	asset := filepath.Join(data, "hooks", "claude.json")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	t.Cleanup(agents.ReplaceChmod(func(p string, m fs.FileMode) error {
		if strings.HasPrefix(filepath.Base(p), ".claude.json.conductor-") {
			return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrPermission}
		}
		return os.Chmod(p, m)
	}))
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "level=WARN", asset+":", "goes on"); len(lines) != 1 {
		t.Fatalf("no warning naming %s:\n%s", asset, logs)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(asset); err != nil || !strings.Contains(string(b), exe+" notify --claude-hook") {
		t.Fatalf("claude.json: %v\n%s", err, b)
	}
}

// When the conductor on PATH is this binary through a link, as package
// managers install it, the assets name the link: it survives an upgrade that
// replaces the binary it points to.
func TestServeNamesTheConductorOnPATH(t *testing.T) {
	clearConductorEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "conductor")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	serveUntilListening(t, "--config", cfg)
	b, err := os.ReadFile(filepath.Join(data, "hooks", "claude.json"))
	if err != nil || !strings.Contains(string(b), `"`+filepath.Join(bin, "conductor")+` notify --claude-hook"`) {
		t.Fatalf("claude.json: %v\n%s", err, b)
	}
}

// A webhook host that does not resolve while the server starts is not a
// reason to refuse to start: the server logs a warning and checks the host
// again before every delivery. A host that resolves to a private address
// still refuses the start.
func TestServeWarnsAboutAWebhookHostThatDoesNotResolve(t *testing.T) {
	clearConductorEnv(t)
	old := config.LookupWebhookHost
	config.LookupWebhookHost = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "internal.example" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { config.LookupWebhookHost = old })
	dir := t.TempDir()
	body := fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q,
		"webhooks": [{"url": "https://nowhere.example/hook?token=t0k3n", "events": ["error"], "secret": "s3cret"}]}`, dir, dir, filepath.Join(t.TempDir(), "data"))
	logs := serveUntilListening(t, "--config", writeServeConfig(t, t.TempDir(), body))
	if lines := logLines(logs, "level=WARN", "webhooks[0]", "nowhere.example did not resolve"); len(lines) != 1 {
		t.Fatalf("no warning about the webhook host:\n%s", logs)
	}
	if strings.Contains(logs, "t0k3n") || strings.Contains(logs, "s3cret") {
		t.Fatalf("the log holds the query or the secret:\n%s", logs)
	}

	private := strings.Replace(body, "nowhere.example", "internal.example", 1)
	var stderr bytes.Buffer
	code, err := runServe(t.Context(), []string{"--listen", "127.0.0.1:0", "--config", writeServeConfig(t, t.TempDir(), private)}, strings.NewReader(""), io.Discard, &stderr)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "10.0.0.7 is a private address") {
		t.Fatalf("a private webhook host: exit %d %v\n%s", code, err, stderr.String())
	}
}

// --examples (or CONDUCTOR_EXAMPLES=1) seeds the four example crews into the
// data directory once, one file each: the second start adds nothing and says
// so, and without either nothing is seeded.
func TestServeSeedsTheExamplesOnce(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, data))
	logs := serveUntilListening(t, "--config", cfg, "--examples")
	if lines := logLines(logs, "example crews", `added="[example-todo-app example-todo-app-tasks example-test-fixer example-docs-writer example-dependency-upgrade]"`, "skipped=[]"); len(lines) != 1 {
		t.Fatalf("first start:\n%s", logs)
	}
	for _, id := range []string{"example-todo-app", "example-todo-app-tasks", "example-test-fixer", "example-docs-writer", "example-dependency-upgrade"} {
		if _, err := os.Stat(filepath.Join(data, "crews", id+".json")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CONDUCTOR_EXAMPLES", "1")
	logs = serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "example crews", "added=[]"); len(lines) != 1 {
		t.Fatalf("second start:\n%s", logs)
	}
	t.Setenv("CONDUCTOR_EXAMPLES", "")
	logs = serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "example crews"); len(lines) != 0 {
		t.Fatalf("without the flag nothing is seeded:\n%s", logs)
	}
}

func TestServeLogsTheReachResult(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "reach": {"mode": "off"}}`, dir, dir, data))
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "reach: off"); len(lines) != 1 {
		t.Fatalf("off is not logged:\n%s", logs)
	}
	// Auto without a TLS listener only looks the address up; the lookup goes
	// to the STUN server named, here one that answers nothing on loopback.
	t.Setenv("CONDUCTOR_REACH", "auto")
	cfg = writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "reach": {"mode": "auto", "stunServer": "stun:127.0.0.1:9"}}`, dir, dir, data))
	logs = serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "reach: auto finds the public address"); len(lines) != 1 {
		t.Fatalf("auto without TLS is not explained:\n%s", logs)
	}
}

// writeTestCert writes a self-signed certificate and key for 127.0.0.1 into
// dir and returns their paths.
func writeTestCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "conductor test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

// serving is a `conductor serve` a test runs in a goroutine. stop cancels
// it and waits for it once, whoever calls first, and is registered as a
// cleanup at the start: a t.Fatalf on the way (a startup that never logged
// its line) leaves no server running into the cleanups registered before
// it, the restore of terminalOf among them, nor into the next test.
type serving struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

// startServing runs `conductor serve` with args in a goroutine; a nonzero
// exit code with no error is an error.
func startServing(t *testing.T, args []string, stdin io.Reader, stdout, stderr io.Writer) *serving {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sv := &serving{cancel: cancel, done: make(chan error, 1)}
	go func() {
		code, err := runServe(ctx, args, stdin, stdout, stderr)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit code %d", code)
		}
		sv.done <- err
	}()
	t.Cleanup(func() { sv.stop() })
	return sv
}

// stop cancels the server and returns what it returned; once stopped, or
// seen returned, it returns the same again.
func (sv *serving) stop() error {
	sv.once.Do(func() {
		sv.cancel()
		sv.err = <-sv.done
	})
	return sv.err
}

// returned reports whether the server has returned on its own, and what it
// returned.
func (sv *serving) returned() (error, bool) {
	select {
	case err := <-sv.done:
		sv.once.Do(func() { sv.err = err })
		return err, true
	default:
		return nil, false
	}
}

// serveWhile runs `conductor serve` until marker is logged, calls during
// with the logs so far while the server runs, then stops it and returns
// everything logged.
func serveWhile(t *testing.T, marker string, during func(logs string), args ...string) string {
	t.Helper()
	t.Cleanup(agents.ForgetBinary())
	var logs syncBuffer
	sv := startServing(t, append([]string{"--listen", "127.0.0.1:0"}, args...), strings.NewReader(""), io.Discard, &logs)
	deadline, _ := startDeadline(t)
	for !strings.Contains(logs.String(), marker) {
		if err, ok := sv.returned(); ok {
			t.Fatalf("serve returned before %q: %v\n%s", marker, err, logs.String())
		}
		select {
		case <-deadline:
			t.Fatalf("serve never logged %q:\n%s", marker, logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if during != nil {
		during(logs.String())
	}
	if err := sv.stop(); err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
	return logs.String()
}

// tlsAddr is the address the "tls listening" line names.
func tlsAddr(t *testing.T, logs string) string {
	t.Helper()
	lines := logLines(logs, "tls listening")
	if len(lines) != 1 {
		t.Fatalf("no tls listening line:\n%s", logs)
	}
	for _, f := range strings.Fields(lines[0]) {
		if v, ok := strings.CutPrefix(f, "listen="); ok {
			return v
		}
	}
	t.Fatalf("no listen= in %q", lines[0])
	return ""
}

func TestServeListensWithCertificateFiles(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir)
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "tls": {"listen": "127.0.0.1:0", "certFile": %q, "keyFile": %q}}`, dir, dir, data, certPath, keyPath))
	logs := serveWhile(t, "tls listening", func(logs string) {
		addr := tlsAddr(t, logs)
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		resp, err := client.Get("https://" + addr + "/api/health")
		if err != nil {
			t.Fatalf("https: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.TLS == nil || resp.TLS.PeerCertificates[0].IPAddresses[0].String() != "127.0.0.1" {
			t.Fatalf("https health: %d %v", resp.StatusCode, resp.TLS)
		}
		if resp.ProtoMajor != 1 {
			t.Fatalf("protocol %s: HTTP/2 must not be offered while the WebSocket routes are HTTP/1.1", resp.Proto)
		}
	}, "--config", cfg)
	if lines := logLines(logs, "tls listening", "mode=files", "ready=true"); len(lines) != 1 {
		t.Fatalf("tls line:\n%s", logs)
	}
}

func TestServeServesNothingOnTLSBeforeTheFirstCertificate(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	// ACME for the public address with reach manual: the address lookup goes
	// to a STUN server that answers nothing, so no order can start, and the
	// listener must refuse every handshake rather than serve something made up.
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "tls": {"listen": "127.0.0.1:0", "acme": {"email": "me@example.net"}}, "reach": {"mode": "manual", "stunServer": "stun:127.0.0.1:9"}}`, dir, dir, data))
	t.Setenv("CONDUCTOR_REACH", "manual")
	logs := serveWhile(t, "tls listening", func(logs string) {
		addr := tlsAddr(t, logs)
		d := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			conn.Close()
			t.Fatal("a handshake succeeded before any certificate was issued")
		}
	}, "--config", cfg)
	if lines := logLines(logs, "tls listening", "mode=acme", "ready=false"); len(lines) != 1 {
		t.Fatalf("tls line:\n%s", logs)
	}
	if fi, err := os.Stat(filepath.Join(data, "tls")); err != nil || !fi.IsDir() {
		t.Fatalf("the tls directory was not made: %v", err)
	}
}

func TestLocalPublicURLFollowsTheBoundPort(t *testing.T) {
	bound := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 43123}
	cases := map[string]string{
		"http://localhost:8080":      "http://localhost:43123",
		"http://127.0.0.1:8080":      "http://127.0.0.1:43123",
		"http://localhost":           "http://localhost:43123",
		"https://team.example.net":   "https://team.example.net",
		"https://team.example.net:1": "https://team.example.net:1",
	}
	for in, want := range cases {
		local := strings.Contains(in, "localhost") || strings.Contains(in, "127.0.0.1")
		if got := localPublicURL(in, local, bound); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if got := localPublicURL("http://localhost:8080", true, nil); got != "http://localhost:8080" {
		t.Errorf("no listener: %s", got)
	}
}

func TestServePrintsTheListenHandshake(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
	t.Cleanup(agents.ForgetBinary())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs syncBuffer
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		_, err := runServe(ctx, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen"}, strings.NewReader(""), &out, &logs)
		done <- err
	}()
	deadline, _ := startDeadline(t)
	for !strings.Contains(out.String(), "\n") {
		select {
		case err := <-done:
			t.Fatalf("serve returned: %v\n%s", err, logs.String())
		case <-deadline:
			t.Fatalf("no handshake:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	var h handshake
	if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &h); err != nil {
		t.Fatalf("handshake %q: %v", out.String(), err)
	}
	host, port, err := net.SplitHostPort(h.Listen)
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("listen %q", h.Listen)
	}
	if h.PublicURL != "http://localhost:"+port || h.PID != os.Getpid() || h.Version == "" || len(h.WorkbenchToken) < 32 || h.TLSListen != "" {
		t.Fatalf("handshake %+v (port %s)", h, port)
	}
	if lines := logLines(logs.String(), "conductor serving", "publicUrl=http://localhost:"+port); len(lines) != 1 {
		t.Fatalf("publicUrl did not follow the port:\n%s", logs.String())
	}
	cancel()
	<-done
	// The log names the file that holds the generated token, never the token.
	if strings.Contains(logs.String(), h.WorkbenchToken) {
		t.Fatalf("the generated token is in the log:\n%s", logs.String())
	}
	if lines := logLines(logs.String(), "level=WARN", "generated one for this run", "file="+filepath.Join(data, "workbench-token")); len(lines) != 1 {
		t.Fatalf("no line naming the generated token's file:\n%s", logs.String())
	}
}

// The terminal the server was started from gets one plain line naming the
// file that holds a generated workbench token; neither it nor the logger
// gets the value, which only the --print-listen line carries.
func TestServeNamesTheGeneratedTokensFileOnTheTerminal(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
	t.Cleanup(agents.ForgetBinary())
	var tty syncBuffer
	old := terminalOf
	t.Cleanup(func() { terminalOf = old })
	terminalOf = func(io.Writer) io.Writer { return &tty }
	var logs, out syncBuffer
	sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen"}, strings.NewReader(""), &out, &logs)
	deadline, _ := startDeadline(t)
	for !strings.Contains(out.String(), "\n") {
		if err, ok := sv.returned(); ok {
			t.Fatalf("serve returned: %v\n%s", err, logs.String())
		}
		select {
		case <-deadline:
			t.Fatalf("no handshake:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := sv.stop(); err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
	var h handshake
	if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &h); err != nil {
		t.Fatalf("handshake %q: %v", out.String(), err)
	}
	if len(h.WorkbenchToken) < 32 {
		t.Fatalf("handshake %+v", h)
	}
	path := filepath.Join(data, "workbench-token")
	want := "Workbench token: generated for this run, in " + path + " (set CONDUCTOR_WORKBENCH_TOKEN to choose your own)\n"
	if tty.String() != want {
		t.Fatalf("the terminal got:\n%q\nwant:\n%q", tty.String(), want)
	}
	if strings.Contains(tty.String(), h.WorkbenchToken) {
		t.Fatalf("the token is on the terminal:\n%s", tty.String())
	}
	if strings.Contains(logs.String(), h.WorkbenchToken) {
		t.Fatalf("the generated token went through the logger:\n%s", logs.String())
	}
	if lines := logLines(logs.String(), "level=WARN", "generated one for this run", "file="+filepath.Join(data, "workbench-token")); len(lines) != 1 {
		t.Fatalf("no line naming the generated token's file:\n%s", logs.String())
	}
}

// readTokenFile returns the workbench token file's contents, failing unless
// it is a regular file of mode 0600.
func readTokenFile(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", fi.Mode())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A generated workbench token is kept in the data directory while the server
// runs (0600, the directory made 0700), rewritten by every start that
// generates one and removed when the server stops, unless another has
// replaced it meanwhile; the log names the file, never the token.
func TestServeKeepsAGeneratedTokenInTheDataDirectory(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	path := filepath.Join(data, "workbench-token")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))

	var first string
	logs := serveWhile(t, "conductor serving", func(string) {
		first = strings.TrimSuffix(readTokenFile(t, path), "\n")
		if len(first) < 32 || strings.ContainsAny(first, " \n") {
			t.Fatalf("token file holds %q", first)
		}
		if fi, err := os.Stat(data); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("data directory mode %v %v", fi.Mode(), err)
		}
	}, "--config", cfg)
	if strings.Contains(logs, first) {
		t.Fatalf("the generated token is in the log:\n%s", logs)
	}
	if lines := logLines(logs, "level=WARN", "generated one for this run", "file="+path, "CONDUCTOR_WORKBENCH_TOKEN"); len(lines) != 1 {
		t.Fatalf("no line naming the token's file:\n%s", logs)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the token file outlived its server: %v", err)
	}

	// A file left by an earlier run is rewritten; one another server puts
	// there while this one runs is left alone when this one stops.
	if err := os.WriteFile(path, []byte("old-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serveWhile(t, "conductor serving", func(string) {
		second := strings.TrimSuffix(readTokenFile(t, path), "\n")
		if second == "old-token" || second == first || len(second) < 32 {
			t.Fatalf("token file holds %q (first run %q)", second, first)
		}
		other := filepath.Join(data, "other.tmp")
		if err := os.WriteFile(other, []byte("another server's\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}
	}, "--config", cfg)
	if got := readTokenFile(t, path); got != "another server's\n" {
		t.Fatalf("token file %q after the server stopped", got)
	}
}

// A configured workbench token removes a token file an earlier run left, with
// or without the lock file a server that stopped leaves beside it.
func TestServeRemovesAStaleTokenFileWhenATokenIsConfigured(t *testing.T) {
	for _, withLock := range []bool{false, true} {
		t.Run(fmt.Sprintf("lock-file-%v", withLock), func(t *testing.T) {
			clearConductorEnv(t)
			dir := t.TempDir()
			data := filepath.Join(dir, "state")
			path := filepath.Join(data, "workbench-token")
			if err := os.MkdirAll(data, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("stale-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if withLock {
				if err := os.WriteFile(filepath.Join(data, "workbench-token.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
			t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", "configured-token")
			logs := serveWhile(t, "conductor serving", func(string) {
				if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("the stale token file is still there: %v", err)
				}
			}, "--config", cfg)
			if strings.Contains(logs, "generated one") || strings.Contains(logs, "workbench-token") {
				t.Fatalf("a configured token logged a generated one:\n%s", logs)
			}
		})
	}
}

// A server started on a data directory where another running server keeps
// the token file leaves that file as it is: one that generates its own token
// (which starts only with --print-listen, a terminal being no place for its
// value), one that cannot listen, and one with a configured token.
func TestServeLeavesTheTokenFileOfAnotherRunningServer(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	path := filepath.Join(data, "workbench-token")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
	cdir := filepath.Join(dir, "configured")
	if err := os.Mkdir(cdir, 0o700); err != nil {
		t.Fatal(err)
	}
	configured := writeServeConfig(t, cdir, fmt.Sprintf(`{"workbenchToken": "configured-token", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))

	// The second servers' terminal. serve.go reads terminalOf once, at a
	// startup that goes on after the "conductor serving" line serveWhile
	// waits for, so the first server may still be about to read it while
	// the closure runs: swapping it there raced that read. The swap happens
	// once, before any server starts, and tells the servers apart by their
	// stderr: a *terminalStderr was started from a terminal, this tty; any
	// other writer (the first server's logs) from none.
	var tty syncBuffer
	old := terminalOf
	t.Cleanup(func() { terminalOf = old })
	terminalOf = func(w io.Writer) io.Writer {
		if _, ok := w.(*terminalStderr); ok {
			return &tty
		}
		return nil
	}

	serveWhile(t, "conductor serving", func(firstLogs string) {
		first := readTokenFile(t, path)

		// One that generates its token, with nowhere to keep it, does not
		// start, with a terminal or without.
		for _, withTTY := range []bool{false, true} {
			var errOut syncBuffer
			var stderr io.Writer = &errOut
			if withTTY {
				stderr = &terminalStderr{&errOut}
			}
			code, err := runServe(context.Background(), []string{"--listen", "127.0.0.1:0", "--config", cfg}, strings.NewReader(""), io.Discard, stderr)
			if code != 1 || err == nil || !strings.Contains(err.Error(), "keeps the workbench token file") {
				t.Fatalf("a second server with its token nowhere (terminal %v): %d %v\n%s", withTTY, code, err, errOut.String())
			}
			if got := readTokenFile(t, path); got != first {
				t.Fatalf("a second server replaced the token file: %q, was %q", got, first)
			}
		}
		if tty.String() != "" {
			t.Fatalf("a server that did not start wrote to the terminal:\n%s", tty.String())
		}

		// With --print-listen it starts: its token is the handshake's alone,
		// the terminal and the log say so, and the file stays the first's.
		var out, logs syncBuffer
		sv := startServing(t, []string{"--listen", "127.0.0.1:0", "--config", cfg, "--print-listen"}, strings.NewReader(""), &out, &terminalStderr{&logs})
		deadline, _ := startDeadline(t)
		for !strings.Contains(out.String(), "\n") {
			if err, ok := sv.returned(); ok {
				t.Fatalf("serve returned: %v\n%s", err, logs.String())
			}
			select {
			case <-deadline:
				t.Fatalf("no handshake:\n%s", logs.String())
			case <-time.After(10 * time.Millisecond):
			}
		}
		if got := readTokenFile(t, path); got != first {
			t.Fatalf("a second server replaced the token file: %q, was %q", got, first)
		}
		if err := sv.stop(); err != nil {
			t.Fatalf("serve: %v\n%s", err, logs.String())
		}
		var h handshake
		if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &h); err != nil || len(h.WorkbenchToken) < 32 || h.WorkbenchToken+"\n" == first {
			t.Fatalf("handshake %q: %v", out.String(), err)
		}
		if lines := logLines(logs.String(), "level=WARN", "--print-listen line only", "file="+path); len(lines) != 1 {
			t.Fatalf("the second server does not say its token is not in the file:\n%s", logs.String())
		}
		if !strings.Contains(tty.String(), "--print-listen line only") || !strings.Contains(tty.String(), path) {
			t.Fatalf("the second server's terminal:\n%s", tty.String())
		}
		for _, tok := range []string{h.WorkbenchToken, strings.TrimSpace(first)} {
			if strings.Contains(logs.String(), tok) || strings.Contains(tty.String(), tok) {
				t.Fatalf("a token value on the terminal or in the log:\n%s\n%s", tty.String(), logs.String())
			}
		}
		if got := readTokenFile(t, path); got != first {
			t.Fatalf("the second server's stop changed the token file: %q, was %q", got, first)
		}

		// A server that cannot listen: on the first one's address.
		var addr string
		for _, f := range strings.Fields(strings.Join(logLines(firstLogs, "conductor serving"), " ")) {
			if v, ok := strings.CutPrefix(f, "listen="); ok {
				addr = v
			}
		}
		if addr == "" {
			t.Fatalf("no listen address:\n%s", firstLogs)
		}
		if _, err := runServe(context.Background(), []string{"--listen", addr, "--config", cfg}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("a second server listened on %s", addr)
		}
		if got := readTokenFile(t, path); got != first {
			t.Fatalf("a server that could not listen changed the token file: %q, was %q", got, first)
		}

		clogs := serveWhile(t, "conductor serving", nil, "--config", configured)
		if lines := logLines(clogs, "belongs to another server", "file="+path); len(lines) != 1 {
			t.Fatalf("the configured server does not say the file stays:\n%s", clogs)
		}
		if got := readTokenFile(t, path); got != first {
			t.Fatalf("a configured server changed the token file: %q, was %q", got, first)
		}
	}, "--config", cfg)
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the token file outlived its server: %v", err)
	}
}

func TestServeExitsWhenStdinCloses(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
	t.Cleanup(agents.ForgetBinary())
	pr, pw := io.Pipe()
	var logs syncBuffer
	done := make(chan int, 1)
	go func() {
		code, _ := runServe(context.Background(), []string{"--listen", "127.0.0.1:0", "--config", cfg, "--exit-on-stdin-close"}, pr, io.Discard, &logs)
		done <- code
	}()
	deadline, _ := startDeadline(t)
	for !strings.Contains(logs.String(), "conductor serving") {
		select {
		case <-done:
			t.Fatalf("serve returned early:\n%s", logs.String())
		case <-deadline:
			t.Fatalf("serve did not start:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = pw.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not stop after stdin closed:\n%s", logs.String())
	}
	if lines := logLines(logs.String(), "stdin closed"); len(lines) != 1 {
		t.Fatalf("not logged:\n%s", logs.String())
	}
}

// conductor switchyard is serve with --switchyard: the server says so as it
// starts, and its health route reports the mode.
func TestSwitchyardCommandServes(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, `{"workbenchToken": "t", "dataDir": "`+filepath.Join(dir, "state")+`"}`)
	logs := serveUntilListening(t, "--config", cfg, "--switchyard")
	if lines := logLines(logs, "switchyard: coordinating hosted sessions", "relay=true"); len(lines) != 1 {
		t.Fatalf("no switchyard line:\n%s", logs)
	}
	var stderr bytes.Buffer
	if code, err := Run(t.Context(), []string{"switchyard", "-h"}, strings.NewReader(""), io.Discard, &stderr); code != 0 || err != nil || !strings.Contains(stderr.String(), "-switchyard") {
		t.Fatalf("switchyard -h: %d %v\n%s", code, err, stderr.String())
	}
}

// Without the harness's CONDUCTOR_RENDEZVOUS=0 the server says it publishes
// to the public switchyard, and dials nothing until a session is launched;
// with it, it says so and names nothing.
func TestServeLogsThePublicSwitchyard(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	if logs := serveUntilListening(t, "--config", cfg); len(logLines(logs, "not published to a switchyard")) != 1 || strings.Contains(logs, "switchyard.rslabs.net") {
		t.Fatalf("with CONDUCTOR_RENDEZVOUS=0:\n%s", logs)
	}
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	if logs := serveUntilListening(t, "--config", cfg); len(logLines(logs, "published to the switchyard")) != 1 || !strings.Contains(logs, "switchyard.rslabs.net") || !strings.Contains(logs, "hostToken=false") {
		t.Fatalf("by default:\n%s", logs)
	}
}

// A config that names a plain switchyard on another machine without
// rendezvous.insecure stops conductor serve and conductor switchyard at
// start, with the words why, before anything listens; the file is left as
// it was.
func TestServeRefusesAPlainSwitchyardOffThisMachine(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	body := fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "rendezvous": {"server": "http://yard.example:8080"}}`, dir, dir, filepath.Join(dir, "state"))
	cfg := writeServeConfig(t, dir, body)
	for _, args := range [][]string{{"serve"}, {"switchyard"}} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var logs syncBuffer
		code, err := Run(ctx, append(args, "--config", cfg, "--listen", "127.0.0.1:0"), strings.NewReader(""), io.Discard, &logs)
		cancel()
		if code != 1 || err == nil || !strings.Contains(err.Error(), "rendezvous.server yard.example:8080 is plain http and not localhost or a loopback address") || !strings.Contains(err.Error(), "CONDUCTOR_RENDEZVOUS_INSECURE=1") {
			t.Errorf("%s: exit %d, %v", args[0], code, err)
		}
		if strings.Contains(logs.String(), "conductor serving") {
			t.Errorf("%s served:\n%s", args[0], logs.String())
		}
	}
	if got, err := os.ReadFile(cfg); err != nil || string(got) != body {
		t.Fatalf("the config file changed: %q %v", got, err)
	}
}

// A switchyard named with more than its address (user information, a
// query, a fragment) stops conductor serve at start, from the file and the
// environment, in words that repeat nothing of it, before anything listens;
// the file is left as it was.
func TestServeRefusesASwitchyardURLThatCarriesMore(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	dir := t.TempDir()
	const want = "rendezvous.server must carry no user name, password, query or fragment"
	body := fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "rendezvous": {"server": "https://me:SECRET@yard.example/yard"}}`, dir, dir, filepath.Join(dir, "state"))
	cfg := writeServeConfig(t, dir, body)
	bare := writeServeConfig(t, t.TempDir(), fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state2")))
	for _, tc := range []struct{ name, cfg, env string }{
		{"the file", cfg, ""},
		{"the environment", bare, "https://yard.example/?token=SECRET"},
	} {
		t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", tc.env)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var logs syncBuffer
		code, err := Run(ctx, []string{"serve", "--config", tc.cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &logs)
		cancel()
		if code != 1 || err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "SECRET") || strings.Contains(logs.String(), "SECRET") {
			t.Errorf("%s: exit %d, %v\n%s", tc.name, code, err, logs.String())
		}
		if strings.Contains(logs.String(), "conductor serving") {
			t.Errorf("%s: served:\n%s", tc.name, logs.String())
		}
	}
	if got, err := os.ReadFile(cfg); err != nil || string(got) != body {
		t.Fatalf("the config file changed: %q %v", got, err)
	}
}

// The switchyard is logged as it starts, and handed to the links' replies, as
// its scheme, host and path (config.ShownURL).
func TestServeShowsTheSwitchyardAsItsAddress(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", "https://yard.example/team/")
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "published to the switchyard"); len(lines) != 1 || !strings.Contains(lines[0], "server=https://yard.example/team ") {
		t.Fatalf("want one line naming the switchyard:\n%s", logs)
	}
	p := uplinkPublisher{&hostagent.Uplink{ServerURL: "https://me:SECRET@yard.example/team?t=SECRET#SECRET"}}
	if got := p.Server(); got != "https://yard.example/team" {
		t.Fatalf("Server() = %q", got)
	}
}

// A server on a free port publishes to a switchyard on this machine's
// default port, as the desktop app's does (--listen 127.0.0.1:0, no
// publicUrl, which is http://localhost:8080 until the bind): the switchyard
// is not this server. It dials nothing until a session is launched.
func TestServePublishesToASwitchyardOnThisMachinesDefaultPort(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", "http://localhost:8080")
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, filepath.Join(dir, "state")))
	logs := serveUntilListening(t, "--config", cfg)
	if lines := logLines(logs, "published to the switchyard", "server=http://localhost:8080 "); len(lines) != 1 {
		t.Fatalf("want one line naming the switchyard:\n%s", logs)
	}
}

// A switchyard at a port this server listens on, at a host that listener
// takes, is this server: conductor serve stops at start with the words why,
// before it writes or binds anything (no port need be free).
func TestServeRefusesASwitchyardOnItsOwnPort(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	t.Cleanup(agents.ForgetBinary())
	dir := t.TempDir()
	data := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, dir, dir, data))
	for _, tc := range []struct{ listen, server string }{
		{"127.0.0.1:9", "http://localhost:9"},
		{":9", "http://127.0.0.1:9/yard"},
	} {
		t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", tc.server)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var logs syncBuffer
		code, err := Run(ctx, []string{"serve", "--config", cfg, "--listen", tc.listen}, strings.NewReader(""), io.Discard, &logs)
		cancel()
		if code != 1 || !errors.Is(err, config.ErrPublishesToItself) || !strings.Contains(err.Error(), tc.server+" is this server") || !strings.Contains(err.Error(), "CONDUCTOR_RENDEZVOUS=0") {
			t.Errorf("--listen %s, switchyard %s: exit %d, %v", tc.listen, tc.server, code, err)
		}
		if strings.Contains(logs.String(), "conductor serving") {
			t.Errorf("--listen %s, switchyard %s: served:\n%s", tc.listen, tc.server, logs.String())
		}
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the data directory was made: %v", err)
	}
}

// conductor switchyard publishes nowhere: a public URL that is the public
// switchyard's, as the public switchyard's own is, does not stop it.
func TestSwitchyardServesAtThePublicSwitchyardsURL(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	t.Setenv("CONDUCTOR_PUBLIC_URL", config.DefaultRendezvousServer)
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, `{"workbenchToken": "t", "dataDir": "`+filepath.Join(dir, "state")+`"}`)
	logs := serveUntilListening(t, "--config", cfg, "--switchyard")
	if lines := logLines(logs, "switchyard: coordinating hosted sessions"); len(lines) != 1 {
		t.Fatalf("no switchyard line:\n%s", logs)
	}
}

// With rendezvous.insecure the server starts and says once that what it
// publishes there travels in the clear; it dials nothing until a session is
// launched. The environment's 0 over the file's true refuses it again.
func TestServeWarnsOnceForAnInsecureSwitchyard(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_RENDEZVOUS", "")
	dir := t.TempDir()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "rendezvous": {"server": "http://yard.example:8080/yard", "insecure": true}}`, dir, dir, filepath.Join(dir, "state")))
	logs := serveUntilListening(t, "--config", cfg)
	// It names the switchyard's host and port.
	if lines := logLines(logs, "level=WARN", "rendezvous.insecure", "travels in the clear and unverified"); len(lines) != 1 || !strings.Contains(lines[0], "server=yard.example:8080") || strings.Contains(lines[0], "/yard") {
		t.Fatalf("want one warning naming the host alone:\n%s", logs)
	}
	t.Setenv("CONDUCTOR_RENDEZVOUS_INSECURE", "0")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if code, err := runServe(ctx, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, io.Discard); code != 1 || err == nil || !strings.Contains(err.Error(), "plain http and not localhost or a loopback address") {
		t.Fatalf("CONDUCTOR_RENDEZVOUS_INSECURE=0 over the file: exit %d, %v", code, err)
	}
}
