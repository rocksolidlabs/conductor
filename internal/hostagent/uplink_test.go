package hostagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A session that already runs (a server's local session) is published to a
// rendezvous through the host protocol: listed there as hosted, typed into
// through its relay, its attention state forwarded, and gone from there
// when the publication stops.
func TestPublishServesALocalSessionOverTheRelay(t *testing.T) {
	srv, hs := startServer(t)
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	var pub *Published
	info := session.Info{ID: "local-1", Name: "published cat", Kind: session.KindServer, AgentID: "cat", Command: []string{"/bin/cat"}, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24, CreatedAt: time.Now().UTC()}
	local := session.NewLocal(info, proc, session.Options{
		Transport: proto.TransportWS,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnChange: func(i session.Info) {
			if pub != nil {
				pub.OnChange(i)
			}
		},
		OnActivity: func(id string, e session.ActivityEntry, st session.AttentionState) {
			if pub != nil {
				pub.OnActivity(id, e, st)
			}
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = local.Stop(ctx)
	})
	u := &Uplink{ServerURL: hs.URL, Token: hostToken, HostName: "office", RelayOnly: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pub, err = u.Publish(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if pub.SessionID == "" || pub.SessionID == "local-1" || pub.ShareBaseURL == "" {
		t.Fatalf("published %+v", pub)
	}
	d, ok := srv.Registry().Get(pub.SessionID)
	if !ok || d.Info().Kind != session.KindHosted || d.Info().Name != "published cat" || d.Info().HostName != "office" || d.Info().AgentID != "cat" {
		t.Fatalf("not listed at the rendezvous: %v %+v", ok, d)
	}

	v := dialViewer(t, hs.URL, pub.SessionID, adminToken)
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	relay, _ := proto.EncodeJSON(proto.TypeSignal, proto.RelayRequest{T: proto.SigRelay, Reason: "forced"})
	v.send(relay)
	v.expectJSON(proto.TypeSignal, proto.SigRelayOK)
	v.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Cols: 90, Rows: 25}))
	v.expectJSON(proto.TypeControl, proto.CtlWelcome)
	v.expectJSON(proto.TypeControl, proto.CtlReady)
	v.send(proto.Encode(proto.TypeInput, []byte("through the rendezvous\n")))
	v.expectOutput("through the rendezvous")

	// Attention set on the local session reaches the rendezvous: its state,
	// not its words.
	local.SetAttention(session.AttentionNeedsInput, "pick one", session.SourceAPI)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && d.Info().Attention.State != session.AttentionNeedsInput {
		time.Sleep(20 * time.Millisecond)
	}
	if att := d.Info().Attention; att.State != session.AttentionNeedsInput || att.Message != "" {
		t.Fatalf("attention at the rendezvous %+v", att)
	}

	// Stopping the publication takes the session off the rendezvous while
	// the local session goes on.
	pub.Stop()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if i := d.Info(); i.Status.Ended() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if i := d.Info(); !i.Status.Ended() {
		t.Fatalf("rendezvous still shows %+v", i.Status)
	}
	if local.Info().Status != session.StatusRunning {
		t.Fatalf("the local session was stopped by the publication's end: %s", local.Info().Status)
	}
}

// A rendezvous that answers the registration with an HTTP error rather than
// the WebSocket: Publish says why as a RefusedError, its code, words and
// Retry-After read from the reply, and a 429 is one that passes with time
// (round 14: the switchyard's per-address limits refused a crew's members,
// and nothing said which limit or tried again).
func TestPublishSaysWhyTheRendezvousRefused(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "40")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"open_host_limit","message":"this address holds as many open sessions as this switchyard allows; ask the operator for a host token"}}`))
	}))
	defer hs.Close()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	local := session.NewLocal(session.Info{ID: "local-2", Name: "refused", Kind: session.KindServer, Command: []string{"/bin/cat"}, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24}, proc, session.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() { _ = local.Stop(context.Background()) })
	u := &Uplink{ServerURL: hs.URL, HostName: "home", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err = u.Publish(t.Context(), local)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("not a refusal: %v", err)
	}
	if refused.Status != 429 || refused.Code != "open_host_limit" || refused.RetryAfter != 40*time.Second || !strings.Contains(refused.Message, "open sessions") {
		t.Fatalf("refusal %+v", refused)
	}
	if again, after := refused.Retryable(); !again || after != 40*time.Second {
		t.Fatalf("retryable %v %v", again, after)
	}
	if !strings.Contains(err.Error(), "open_host_limit") {
		t.Fatalf("words: %v", err)
	}
	// A 401 does not pass with time.
	if again, _ := (&RefusedError{Status: 401}).Retryable(); again {
		t.Fatal("a 401 retried")
	}
}
