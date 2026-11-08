package cli

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

var _ api.DeskOpener = uplinkPublisher{}

// The serve command's uplink hands the API's lane to each publication and
// desk (hostagent's TestUplinkPassesDecisions checks the hand-on), and
// tells the API of each link the switchyard says is gone; a desk at an
// older switchyard, which keeps none, is api.ErrNoDesks, which the API
// refuses switchyard_too_old.
func TestUplinkPassesDecisions(t *testing.T) {
	clearConductorEnv(t)
	cfg := config.Defaults()
	cfg.WorkbenchToken = "admin-token"
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err == nil {
			c.Close(proto.CloseProtocolError, "register expected")
		}
	}))
	t.Cleanup(older.Close)
	cfg.Rendezvous.Token = "host-token"
	cfg.Rendezvous.Insecure = true
	u := newUplink(cfg, older.URL, srv, slog.New(slog.DiscardHandler))
	if lane, ok := u.Decisions.(*api.Lane); !ok || lane == nil || u.LinkGone == nil {
		t.Fatalf("the uplink's decisions %T, link gone set %v", u.Decisions, u.LinkGone != nil)
	}
	if u.ServerURL != older.URL || u.Token != "host-token" || !u.Insecure {
		t.Fatalf("the uplink %+v", u)
	}
	if _, err := (uplinkPublisher{u}).OpenDesk(t.Context(), proto.DeskRun{ID: "run-1", Name: "Run"}); !errors.Is(err, api.ErrNoDesks) {
		t.Fatalf("a desk at an older switchyard: %v", err)
	}
}
