package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// registeredRuntime is the registry holding fake as its Runtime, as
// another module's init would leave it.
func registeredRuntime(fake ext.Runtime) func() ext.Providers {
	return func() ext.Providers {
		return ext.Providers{Runtime: fake, All: []ext.Entry{{Kind: ext.KindRuntime, Name: fake.Name(), Value: fake}}}
	}
}

// serve hands the server the registered Runtime, which it holds and
// offers its engine (api's TestWithExtensionsOffersTheEngineTheRuntime
// follows it from there): New takes it, and the server lists it.
func TestRunServeOffersTheRegisteredRuntime(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, filepath.Join(dir, "state")))
	fake := &sessiontest.FakeRuntime{}
	old := extensions
	t.Cleanup(func() { extensions = old })
	extensions = registeredRuntime(fake)
	h := watchNewServer(t)
	serveUntilListening(t, "--config", cfg)
	if h.providers.Runtime != ext.Runtime(fake) {
		t.Fatalf("serve handed the server the Runtime %v, not the registered one", h.providers.Runtime)
	}
	if !slices.Contains(h.registered, ext.Registration{Kind: ext.KindRuntime, Name: "fake"}) {
		t.Fatalf("the server holds %+v", h.registered)
	}
}

// host runs its command natively whatever is registered: a host runs no
// managed run, and a hosted session is a person's (decision RT7).
func TestRunHostRunsNatively(t *testing.T) {
	clearConductorEnv(t)
	old := extensions
	t.Cleanup(func() { extensions = old; runHostAgent = hostagent.Run })
	extensions = registeredRuntime(&sessiontest.FakeRuntime{})
	var got *hostagent.Options
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = &opts
		return hostagent.Result{}, nil
	}
	if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 0 || err != nil {
		t.Fatalf("exit %d, %v, %s", code, err, stderr)
	}
	if got == nil || got.Runtime != nil {
		t.Fatalf("the host's Runtime: %+v, want none (the native one)", got)
	}
}
