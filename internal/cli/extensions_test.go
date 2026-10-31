package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
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

// serve reads the registry once, at start, and names what it holds in one
// line; a plain build, which registers nothing, says nothing of it.
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
	logs := serveUntilListening(t, "--config", cfg)
	if n := reads.Load(); n != 1 {
		t.Fatalf("serve read the registry %d times, want once", n)
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
