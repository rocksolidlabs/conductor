package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
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
