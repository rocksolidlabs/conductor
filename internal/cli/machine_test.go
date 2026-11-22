package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// serve reads this machine's id as it starts (the package's own directory
// for it, main_test.go) and keys its own row by it: the file is there
// after, and the server's own key is of a key's form. A switchyard reads
// none and keys none.
func TestServeKeysItsOwnMachine(t *testing.T) {
	idDir, err := machineDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, yard := range []bool{false, true} {
		dir := t.TempDir()
		work := filepath.Join(dir, "work")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		taken, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer taken.Close()
		extra := ""
		if yard {
			extra = `, "switchyard": {"enabled": true}`
		}
		cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q%s}`, work, work, filepath.Join(dir, "state"), extra))
		oldNew := newServer
		var made *api.Server
		newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers, notices string) (*api.Server, error) {
			srv, err := oldNew(cfg, cat, log, ui, st, p, notices)
			made = srv
			return srv, err
		}
		var logs syncBuffer
		// The port is taken: serve stops at its listener, after the id.
		code, err := runServe(t.Context(), []string{"--config", cfg, "--listen", taken.Addr().String()}, strings.NewReader(""), io.Discard, &logs)
		newServer = oldNew
		if code == 0 || err == nil || made == nil {
			t.Fatalf("serve exited %d, %v\n%s", code, err, logs.String())
		}
		own := made.OwnHostKey()
		if yard {
			if own != "" {
				t.Fatalf("a switchyard keyed its own machine %q", own)
			}
			continue
		}
		id, err := machine.ID(idDir)
		if err != nil || !proto.ValidHostMachine(own) || own == id {
			t.Fatalf("own key %q, the id's file %v", own, err)
		}
		if strings.Contains(logs.String(), id) || strings.Contains(logs.String(), own) {
			t.Fatalf("serve logged the id or its own key:\n%s", logs.String())
		}
	}
}

// host hands its agent the directory of this machine's id.
func TestHostPassesTheMachineDir(t *testing.T) {
	clearConductorEnv(t)
	t.Cleanup(func() { runHostAgent = hostagent.Run })
	var got string
	runHostAgent = func(_ context.Context, opts hostagent.Options) (hostagent.Result, error) {
		got = opts.MachineDir
		return hostagent.Result{}, nil
	}
	if code, stderr, err := runHostWith(t, "--server", "http://127.0.0.1:1", "--no-local", "--", "sh"); code != 0 || err != nil {
		t.Fatalf("exit %d, %v, %s", code, err, stderr)
	}
	if want, _ := machineDir(); got != want || got == "" {
		t.Fatalf("the agent was handed %q, want %q", got, want)
	}
}
