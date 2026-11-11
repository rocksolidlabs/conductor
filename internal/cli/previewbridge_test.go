package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// serve hands the server the addresses its listeners are bound to, the
// plain one's and the TLS one's (both configured with port 0), so the
// preview bridge names their ports and never the configured text; and it
// says once, in fixed words, that a publicUrl not written as a browser
// writes an origin is left out.
func TestServeHandsTheBridgeTheAddressesItIsBoundTo(t *testing.T) {
	clearConductorEnv(t)
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir)
	cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q, "publicUrl": "https://Conductor.example", "tls": {"listen": "127.0.0.1:0", "certFile": %q, "keyFile": %q}}`,
		dir, dir, filepath.Join(dir, "state"), certPath, keyPath))
	oldNew := newServer
	t.Cleanup(func() { newServer = oldNew })
	newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers, notices string) (*api.Server, error) {
		return api.New(cfg, cat, log, ui, st, api.WithExtensions(p), api.WithNotices(notices), api.WithPreviewBridge([]byte("start(conductorWorkbenchOrigins);")))
	}
	logs := serveWhile(t, "tls listening", func(logs string) {
		var plain string
		for _, f := range strings.Fields(logLines(logs, "conductor serving")[0]) {
			if v, ok := strings.CutPrefix(f, "listen="); ok {
				plain = v
			}
		}
		_, plainPort, _ := net.SplitHostPort(plain)
		_, tlsPort, _ := net.SplitHostPort(tlsAddr(t, logs))
		if plainPort == "" || plainPort == "0" || tlsPort == "" || tlsPort == "0" {
			t.Fatalf("ports %q and %q", plainPort, tlsPort)
		}
		// The TLS listener's address is handed over once it listens: wait
		// for it in what the route serves.
		want := []string{"http://127.0.0.1:" + plainPort, "http://localhost:" + plainPort, "http://[::1]:" + plainPort,
			"https://127.0.0.1:" + tlsPort, "https://localhost:" + tlsPort, "https://[::1]:" + tlsPort}
		deadline, wait := startDeadline(t)
		for {
			got := servedOrigins(t, "http://"+plain+"/preview-bridge.js")
			if slices.Equal(got, want) {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("origins %q after %s, want %q", got, wait, want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}, "--config", cfg)
	if lines := logLines(logs, "publicUrl is not written as a browser writes an origin"); len(lines) != 1 {
		t.Fatalf("the publicUrl line, %d times:\n%s", len(lines), logs)
	}
}

// servedOrigins is the origins the bridge at u is called with.
func servedOrigins(t *testing.T, u string) []string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, err)
	}
	body := string(b)
	i := strings.LastIndex(body, "\n})(")
	data, ok := strings.CutSuffix(body[i+len("\n})("):], ");\n")
	var out []string
	if i < 0 || !ok || json.Unmarshal([]byte(data), &out) != nil {
		t.Fatalf("no origins in %q", body)
	}
	return out
}
