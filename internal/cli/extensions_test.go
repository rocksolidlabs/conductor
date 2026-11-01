package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// cloudIdentity and pushSink are providers as another module's would be;
// the tests hand them to the composition root by replacing extensions,
// never by registering into the process's registry.
type cloudIdentity struct{}

func (cloudIdentity) Name() string { return "cloud" }
func (cloudIdentity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}

type pushSink struct{}

func (pushSink) Name() string                                   { return "push" }
func (pushSink) Events() []string                               { return nil }
func (pushSink) Notify(context.Context, ext.Notification) error { return nil }

// handed records what runServe hands newServer, and what the server it
// made holds of it, through the real newServer.
type handed struct {
	providers  ext.Providers
	registered []ext.Registration
}

func watchNewServer(t *testing.T) *handed {
	t.Helper()
	old := newServer
	t.Cleanup(func() { newServer = old })
	h := &handed{}
	newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers) (*api.Server, error) {
		h.providers = p
		srv, err := old(cfg, cat, log, ui, st, p)
		if srv != nil {
			h.registered = srv.Registered()
		}
		return srv, err
	}
	return h
}

// serve reads the registry once, at start, and names what it holds in one
// line; a plain build, which registers nothing, says nothing of it. What
// it read reaches the server filled (local.Fill): the providers where
// something is registered, the free defaults elsewhere.
func TestServeReadsTheRegistryOnce(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, filepath.Join(dir, "state")))
	old := extensions
	t.Cleanup(func() { extensions = old })
	var reads atomic.Int32
	extensions = func() ext.Providers {
		reads.Add(1)
		return ext.Providers{
			Identity:      cloudIdentity{},
			Notifications: []ext.NotificationSink{pushSink{}},
			All: []ext.Entry{
				{Kind: ext.KindIdentity, Name: "cloud", Value: cloudIdentity{}},
				{Kind: ext.KindNotifications, Name: "push", Value: pushSink{}},
			},
		}
	}
	h := watchNewServer(t)
	logs := serveUntilListening(t, "--config", cfg)
	if n := reads.Load(); n != 1 {
		t.Fatalf("serve read the registry %d times, want once", n)
	}
	p := h.providers
	if p.Identity != (cloudIdentity{}) || len(p.Notifications) != 1 || p.Notifications[0] != (pushSink{}) {
		t.Fatalf("serve did not hand the server what the registry holds: %+v", p)
	}
	if p.Router != (local.Router{}) || p.Continuity != (local.Continuity{}) || p.Media != (local.Media{}) {
		t.Fatalf("serve handed the server slots local.Fill did not fill: %+v", p)
	}
	if want := []ext.Registration{{Kind: ext.KindIdentity, Name: "cloud"}, {Kind: ext.KindNotifications, Name: "push"}}; !slices.Equal(h.registered, want) {
		t.Fatalf("the server holds %+v, want %+v", h.registered, want)
	}
	if lines := logLines(logs, "level=INFO", `msg="extensions: identity cloud, notifications push"`); len(lines) != 1 {
		t.Fatalf("no line naming the providers:\n%s", logs)
	}
	if strings.Index(logs, "extensions: ") > strings.Index(logs, "conductor serving") {
		t.Fatalf("the providers named after the server serves:\n%s", logs)
	}

	// The process's own registry, as a plain build's: nothing registered,
	// nothing said, the server as before.
	reads.Store(0)
	extensions = func() ext.Providers {
		reads.Add(1)
		return old()
	}
	logs = serveUntilListening(t, "--config", cfg)
	if n := reads.Load(); n != 1 {
		t.Fatalf("serve read the registry %d times, want once", n)
	}
	if strings.Contains(logs, "extensions") {
		t.Fatalf("a plain build logged its extensions:\n%s", logs)
	}
	if p := h.providers; p.Identity != (local.Identity{}) || p.Router != (local.Router{}) || len(p.Notifications) != 0 || len(h.registered) != 0 {
		t.Fatalf("a plain build handed the server %+v, holding %+v: want the free defaults alone", p, h.registered)
	}
}

// A start that fails once the server is made (here its listener, on a
// port that is taken) shuts the server down: the drain of a provider that
// is no Starter, which began in New on the server's own context, ends
// with runServe, so a process that goes on keeps nothing of the run.
func TestServeFailedStartEndsTheDrains(t *testing.T) {
	clearConductorEnv(t)
	t.Cleanup(agents.ForgetBinary())
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, filepath.Join(dir, "state")))
	old := extensions
	t.Cleanup(func() { extensions = old })
	extensions = func() ext.Providers {
		return ext.Providers{
			Notifications: []ext.NotificationSink{pushSink{}},
			All:           []ext.Entry{{Kind: ext.KindNotifications, Name: "push", Value: pushSink{}}},
		}
	}
	before := drainGoroutines()
	oldNew := newServer
	t.Cleanup(func() { newServer = oldNew })
	deadline, wait := startDeadline(t)
	newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers) (*api.Server, error) {
		srv, err := oldNew(cfg, cat, log, ui, st, p)
		if err != nil {
			return srv, err
		}
		// The sink's drain runs before runServe goes on: the test sees it.
		for drainGoroutines() <= before {
			select {
			case <-deadline:
				t.Errorf("the sink's drain did not begin in %s", wait)
				return srv, nil
			case <-time.After(10 * time.Millisecond):
			}
		}
		return srv, nil
	}
	var logs syncBuffer
	code, err := runServe(t.Context(), []string{"--config", cfg, "--listen", taken.Addr().String()}, strings.NewReader(""), io.Discard, &logs)
	if code == 0 || err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("serve on a taken port exited %d, %v; want the listener's error\n%s", code, err, logs.String())
	}
	// Shutdown waits for the drains (10 s at most); a goroutine whose wait
	// is done may show a moment longer. The bound is reached only by a
	// drain that never ends.
	end := time.Now().Add(15 * time.Second)
	for n := drainGoroutines(); n > before; n = drainGoroutines() {
		if time.Now().After(end) {
			t.Fatalf("%d drain(s) still run after serve failed to start", n-before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drainGoroutines counts the goroutines draining a provider's queue, from
// every goroutine's stack.
func drainGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("internal/api.(*extQueue[...]).run("))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// host closes the registry too, before it runs anything, and reads it once.
func TestHostClosesTheRegistry(t *testing.T) {
	clearConductorEnv(t)
	old := extensions
	t.Cleanup(func() { extensions = old; runHostAgent = hostagent.Run })
	var order []string
	extensions = func() ext.Providers {
		order = append(order, "registry")
		return ext.Providers{}
	}
	runHostAgent = func(context.Context, hostagent.Options) (hostagent.Result, error) {
		order = append(order, "host")
		return hostagent.Result{}, nil
	}
	if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 0 || err != nil {
		t.Fatalf("exit %d, %v, %s", code, err, stderr)
	}
	if want := []string{"registry", "host"}; !slices.Equal(order, want) {
		t.Fatalf("host did %q, want %q", order, want)
	}
}

// startingIdentity is an identity provider that is a Starter: each Start
// hands its context and Host over, and what serve had logged by then.
type startingIdentity struct {
	began chan startedWith
	logs  *syncBuffer
}

type startedWith struct {
	ctx    context.Context
	h      ext.Host
	logged string
}

func (startingIdentity) Name() string { return "cloud" }
func (startingIdentity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (s startingIdentity) Start(ctx context.Context, h ext.Host) error {
	s.began <- startedWith{ctx, h, s.logs.String()}
	return nil
}

// heldWriter is a stdout whose writes wait until it is opened.
type heldWriter struct {
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	b       bytes.Buffer
}

func (w *heldWriter) open() { w.once.Do(func() { close(w.release) }) }

func (w *heldWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// serve starts the providers that need the server once it is made, with a
// Host whose directory is the provider's own in the data directory, on a
// context that serve's end ends; the try's Host refuses after it. The
// switchyard's publisher is in place by then, so a session the provider
// launches at once is published: the start-up work after the providers'
// start (here the handshake line) is held until the provider has started.
func TestServeStartsProviders(t *testing.T) {
	clearConductorEnv(t)
	// Publishing on, to a switchyard nothing is ever published to (no
	// session is made).
	t.Setenv("CONDUCTOR_RENDEZVOUS", "1")
	t.Cleanup(agents.ForgetBinary())
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "rendezvous": {"server": "http://127.0.0.1:1"}}`, work, work, state))
	var logs syncBuffer
	p := startingIdentity{began: make(chan startedWith, 1), logs: &logs}
	old := extensions
	t.Cleanup(func() { extensions = old })
	extensions = func() ext.Providers {
		return ext.Providers{Identity: p, All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: p}}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := &heldWriter{release: make(chan struct{})}
	defer held.open()
	done := make(chan error, 1)
	go func() {
		code, err := runServe(ctx, []string{"--config", cfg, "--listen", "127.0.0.1:0", "--print-listen"}, strings.NewReader(""), held, &logs)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit code %d", code)
		}
		done <- err
	}()
	deadline, wait := startDeadline(t)
	var got startedWith
	select {
	case got = <-p.began:
	case err := <-done:
		t.Fatalf("serve returned before starting the provider: %v\n%s", err, logs.String())
	case <-deadline:
		t.Fatalf("the provider was not started in %s:\n%s", wait, logs.String())
	}
	if !strings.Contains(got.logged, "sessions are published to the switchyard") {
		t.Fatalf("the provider started before the switchyard's publisher was set:\n%s", got.logged)
	}
	held.open()
	if d, err := got.h.DataDir(); err != nil || d != filepath.Join(state, "ext", "cloud") {
		t.Fatalf("the provider's directory: %s %v", d, err)
	}
	if got.ctx.Err() != nil {
		t.Fatal("the started provider's context ended while serve runs")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
	if got.ctx.Err() == nil {
		t.Fatal("serve ended and the provider's context did not")
	}
	if _, err := got.h.DataDir(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the host acts after serve ended: %v", err)
	}
}
