package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/agents"
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

// The switchyard the command builds says in its registered that it carries
// a decision link's lane (docs/plans/decision-inbox.md, PR-7): its join
// page has the Decide tab. So a publisher's run desk there says decisions,
// a decision link is minted over it, and the switchyard's join of that link
// names the desk its Decide tab joins.
func TestTheSwitchyardSaysDecisions(t *testing.T) {
	yard, desk := switchyardDesk(t, http.NotFoundHandler())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if !desk.Decisions() {
		t.Fatalf("the switchyard's registered says nothing of decisions")
	}
	made, err := desk.RunLink(ctx, "control", time.Hour, "phone", proto.RunGroup{ID: "run-1", Name: "Run", Members: []proto.RunMember{}, Desk: desk.CurrentID()})
	if err != nil || made.LinkID == "" || !strings.HasPrefix(made.URL, yard+"/join/") {
		t.Fatalf("the decision link minted over the desk: %+v %v", made, err)
	}
	token := strings.TrimPrefix(made.URL, yard+"/join/")
	resp, err := http.Get(yard + "/api/join/" + token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var joined struct {
		Run struct {
			ID   string `json:"id"`
			Desk string `json:"desk"`
		} `json:"run"`
		Role string `json:"role"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&joined) != nil || joined.Run.ID != "run-1" || joined.Run.Desk != desk.CurrentID() || joined.Role != "control" {
		t.Fatalf("the switchyard's join of the link: %d %+v (desk %s)", resp.StatusCode, joined, desk.CurrentID())
	}
}

// A build with no embedded join page (API only) serves no Decide tab, so
// the switchyard it is says nothing of decisions in its registered, and a
// publisher mints no decision link there (docs/plans/decision-inbox.md,
// Version skew: no build advertises what its served page lacks).
func TestAnAPIOnlySwitchyardSaysNoDecisions(t *testing.T) {
	_, desk := switchyardDesk(t, nil)
	if desk.Decisions() {
		t.Fatal("a switchyard with no join page says it carries decisions")
	}
}

// switchyardDesk starts the serve command as a switchyard whose embedded web
// build is ui (nil: none, API only) and opens a publisher's run desk there,
// stopped at the test's end. It returns the switchyard's address and the
// desk.
func switchyardDesk(t *testing.T, ui http.Handler) (string, api.Desk) {
	t.Helper()
	clearConductorEnv(t)
	old := webUI
	t.Cleanup(func() { webUI = old })
	webUI = func() http.Handler { return ui }
	dir := realTempDir(t)
	yardCfg := writeServeConfig(t, dir, fmt.Sprintf(`{"workbenchToken": "yard-admin", "hostTokens": ["host-token"], "dataDir": %q}`, filepath.Join(dir, "yard")))
	t.Cleanup(agents.ForgetBinary())
	var out, logs syncBuffer
	sv := startServing(t, []string{"--switchyard", "--listen", "127.0.0.1:0", "--config", yardCfg, "--print-listen"}, strings.NewReader(""), &out, &logs)
	deadline, wait := startDeadline(t)
	for !strings.Contains(out.String(), "\n") {
		if err, done := sv.returned(); done {
			t.Fatalf("the switchyard returned: %v\n%s", err, logs.String())
		}
		select {
		case <-deadline:
			t.Fatalf("the switchyard did not start in %s:\n%s", wait, logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	var h handshake
	if err := json.Unmarshal([]byte(strings.SplitN(out.String(), "\n", 2)[0]), &h); err != nil {
		t.Fatalf("handshake %q: %v", out.String(), err)
	}
	yard := "http://" + h.Listen

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
	// The publisher's server ends with the test (its workers started with it): after the desk's stop, registered below and so run
	// first, and before the folders it writes to are removed, registered above.
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		srv.Shutdown(sctx)
		srv.WaitBackground(sctx)
	})
	cfg.Rendezvous.Token = "host-token"
	cfg.Rendezvous.Insecure = true
	u := newUplink(cfg, yard, srv, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	desk, err := (uplinkPublisher{u}).OpenDesk(ctx, proto.DeskRun{ID: "run-1", Name: "Run"})
	if err != nil {
		t.Fatalf("the run's desk at the switchyard: %v\n%s", err, logs.String())
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = desk.Stop(sctx)
	})
	return yard, desk
}
