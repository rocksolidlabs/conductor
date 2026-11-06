package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor"
	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The holder refuses an empty text, one over MaxNotices, a second text
// and a text after take, each changing nothing; a text of MaxNotices bytes
// is taken; take answers the same every time.
func TestNoticesHolderBounds(t *testing.T) {
	var h noticesHolder
	if err := h.set(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("set(empty) = %v, want refused as empty", err)
	}
	over := strings.Repeat("n", MaxNotices+1)
	if err := h.set(over); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("over the limit of %d", MaxNotices)) {
		t.Fatalf("set(%d bytes) = %v, want refused over the limit", len(over), err)
	}
	at := over[:MaxNotices]
	if err := h.set(at); err != nil {
		t.Fatalf("set(%d bytes) = %v, want taken", len(at), err)
	}
	if err := h.set("another"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("a second set = %v, want refused", err)
	}
	if got := h.take(); got != at {
		t.Fatalf("take answered %d bytes, want the %d handed in", len(got), len(at))
	}
	if err := h.set("late"); !errors.Is(err, errNoticesTaken) {
		t.Fatalf("set after take = %v, want %v", err, errNoticesTaken)
	}
	if got := h.take(); got != at {
		t.Fatalf("a second take answered %d bytes, want the same %d", len(got), len(at))
	}

	var none noticesHolder
	if got := none.take(); got != "" {
		t.Fatalf("take of an empty holder = %q, want none", got)
	}
	if err := none.set("late"); !errors.Is(err, errNoticesTaken) {
		t.Fatalf("set after take = %v, want %v", err, errNoticesTaken)
	}
	if got := none.take(); got != "" {
		t.Fatalf("a refused set changed the holder: %q", got)
	}
}

// serve reads the notices a main handed in (SetNotices) once, at start,
// and its server serves them; with none handed in it serves the embedded
// text. A text handed in once the server started is refused.
func TestServeServesTheNoticesAMainHandedIn(t *testing.T) {
	const own = "THIRD-PARTY NOTICES of a build that registers providers\n"
	for _, tc := range []struct {
		name, set, want string
	}{
		{"own", own, own},
		{"none", "", conductor.ThirdPartyNotices},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearConductorEnv(t)
			dir := t.TempDir()
			work := filepath.Join(dir, "work")
			if err := os.Mkdir(work, 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, work, work, filepath.Join(dir, "state")))
			oldHolder := notices
			t.Cleanup(func() { notices = oldHolder })
			notices = &noticesHolder{}
			if tc.set != "" {
				if err := SetNotices(tc.set); err != nil {
					t.Fatal(err)
				}
			}
			oldNew := newServer
			t.Cleanup(func() { newServer = oldNew })
			var handedIn, served string
			calls := 0
			newServer = func(cfg *config.Config, cat catalog.Catalog, log *slog.Logger, ui http.Handler, st *store.Store, p ext.Providers, text string) (*api.Server, error) {
				calls++
				handedIn = text
				srv, err := oldNew(cfg, cat, log, ui, st, p, text)
				if err == nil {
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/third-party-notices.txt", nil))
					if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
						t.Errorf("the notices route answered %d %q", rec.Code, rec.Header().Get("Content-Type"))
					}
					served = rec.Body.String()
				}
				return srv, err
			}
			serveUntilListening(t, "--config", cfg)
			if calls != 1 || handedIn != tc.set {
				t.Fatalf("serve made %d servers, handed %d bytes, want one with the %d handed in", calls, len(handedIn), len(tc.set))
			}
			if served != tc.want {
				t.Fatalf("the server served %d bytes (%.80q), want %d", len(served), served, len(tc.want))
			}
			if err := SetNotices("THIRD-PARTY NOTICES handed in late\n"); !errors.Is(err, errNoticesTaken) {
				t.Fatalf("SetNotices after the server started = %v, want %v", err, errNoticesTaken)
			}
			if got := notices.take(); got != tc.set {
				t.Fatalf("the refused text changed what was handed in: %q", got)
			}
		})
	}
}
