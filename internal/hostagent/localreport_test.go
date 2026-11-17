package hostagent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// socketBase is a folder for a test's socket folders, short enough for a
// socket's path whatever the test's name.
func socketBase(t *testing.T) string {
	t.Helper()
	parent := os.TempDir()
	if len(parent) > 40 {
		parent = "/tmp"
	}
	d, err := os.MkdirTemp(parent, "hr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// routeCount counts the requests that reach the server's attention route
// of any session.
type routeCount struct{ n atomic.Int64 }

func (c *routeCount) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/attention") {
			c.n.Add(1)
		}
		h.ServeHTTP(w, r)
	})
}

// reportingHost is a host's session served by a test server, its agent
// token the host's and the server's both, its socket listening, and the
// target its agent's sender reports to.
type reportingHost struct {
	hs     *signal.HostedSession
	a      *agent
	local  *session.Local
	proc   *sessiontest.FakeProc
	l      *reportListener
	target notify.Target
	route  *routeCount
	base   string
	conn   *websocket.Conn
	served <-chan error
	// back serves the host's next connection, a resume.
	back func()
}

func newReportingHost(t *testing.T) *reportingHost {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	cfg.AllowedRoots = []string{t.TempDir()}
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, catalog.Default(), discardLog, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	route := &routeCount{}
	ts := httptest.NewServer(route.wrap(srv.Handler()))
	t.Cleanup(ts.Close)
	tok, _ := share.NewToken()
	a := &agent{opts: Options{ServerURL: ts.URL, Token: hostToken, Name: "reports", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", Log: discardLog}, peers: map[string]*peer{}, log: discardLog, agentToken: tok}
	a.dir = t.TempDir()
	if a.wsURL, err = controlURL(ts.URL); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	conn, registered, err := a.dialAndRegister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	local := session.NewLocal(session.Info{ID: registered.SessionID, Cwd: a.dir, Cols: 80, Rows: 24}, proc, session.Options{OnChange: a.noteChange, Log: discardLog, FileEdit: "off"})
	a.mu.Lock()
	a.local = local
	a.mu.Unlock()
	l, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	l.setSession(local)
	serve := func(c *websocket.Conn) <-chan error {
		served := make(chan error, 1)
		go func() { served <- a.serveConn(ctx, c) }()
		return served
	}
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	h := &reportingHost{hs: d.(*signal.HostedSession), a: a, local: local, proc: proc, l: l, route: route, base: ts.URL, conn: conn,
		target: notify.Target{URL: attentionURL(ts.URL, registered.SessionID), Token: tok, Socket: l.path}}
	h.served = serve(conn)
	h.back = func() {
		t.Helper()
		conn2, again, err := a.dialAndRegister(ctx)
		if err != nil || !again.Resumed {
			t.Fatalf("resume: %v %+v", err, again)
		}
		t.Cleanup(func() { conn2.CloseNow() })
		serve(conn2)
	}
	return h
}

// away ends the host's connection and waits for the server's copy to show
// its host gone.
func (h *reportingHost) away(t *testing.T) {
	t.Helper()
	h.conn.CloseNow()
	<-h.served
	untilListed(t, h.hs, "the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
}

func (h *reportingHost) send(t *testing.T, req notify.Request) {
	t.Helper()
	if err := h.target.Send(t.Context(), req); err != nil {
		t.Fatalf("send %+v: %v", req, err)
	}
}

// postSocket posts body to path on the socket with token, once: the status
// and the reply.
func postSocket(t *testing.T, socket, path, token, body string) (int, string) {
	t.Helper()
	return requestSocket(t, socket, http.MethodPost, path, token, strings.NewReader(body))
}

func requestSocket(t *testing.T, socket, method, path, token string, body io.Reader) (int, string) {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://host"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A turn's done reported on the socket while the host's connection is
// down is the host session's at once, and the server's copy shows it, its
// words and the host's id, once the host is back. The server's route is
// never asked.
func TestADoneReportedWhileTheHostIsAwayIsToldOnItsReturn(t *testing.T) {
	h := newReportingHost(t)
	h.away(t)
	const words = "Refactored the parser."
	h.send(t, notify.Request{State: "done", Message: words, Kind: session.KindDone, TurnID: "turn-1"})
	got := h.local.Info().Attention
	if got.State != session.AttentionDone || got.Message != words || got.Source != session.SourceAPI {
		t.Fatalf("the host's session shows %+v", got)
	}
	if a := h.hs.Info().Attention; a.State == session.AttentionDone {
		t.Fatalf("the copy changed while the host was away: %+v", a)
	}
	h.back()
	untilListed(t, h.hs, "the copy", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.ID == got.ID && i.Attention.Message == words && i.Attention.State == session.AttentionDone
	})
	// The same turn's end told again is that report again: told once.
	h.send(t, notify.Request{State: "done", Message: "again", Kind: session.KindDone, TurnID: "turn-1"})
	if a := h.local.Info().Attention; a.ID != got.ID || a.Message != words {
		t.Fatalf("a turn's end told twice was taken again: %+v", a)
	}
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's route was asked %d times", n)
	}
}

// The agent's own question asked on the socket while the host is away is
// the copy's on the host's return, its choices with it, and a page's
// answer to it through the server reaches the agent.
func TestAQuestionAskedWhileTheHostIsAwayIsAnsweredFromAPage(t *testing.T) {
	h := newReportingHost(t)
	h.away(t)
	h.send(t, notify.Request{State: "needs_input", Message: "Ship it?", Ask: true, Kind: "prompt", Options: []notify.Option{{Label: "yes", Input: "yes\r"}, {Label: "no", Input: "no\r"}}})
	q := h.local.Info().Attention
	if q.State != session.AttentionNeedsInput || !q.Asked() || len(q.Options) != 2 {
		t.Fatalf("the host's session shows %+v", q)
	}
	h.back()
	untilListed(t, h.hs, "the copy's question", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.ID == q.ID && i.Attention.Message == "Ship it?" && len(i.Attention.Options) == 2
	})
	v := relayController(t, h.base, h.hs.Info().ID)
	v.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "r1", Question: q.ID, Input: "yes\r"}))
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(strings.Join(writes(h.proc), ""), "yes"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the agent was typed %q", writes(h.proc))
		}
	}
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's route was asked %d times", n)
	}
}

// A working, then a done, reported while the host is away: the host's
// session records them in that order and shows the done, and so does the
// copy once the host is back.
func TestAWorkingThenADoneWhileAwayKeepTheirOrder(t *testing.T) {
	h := newReportingHost(t)
	h.away(t)
	h.send(t, notify.Request{State: "working", Message: "on it", Turn: true})
	if a := h.local.Info().Attention; a.State != session.AttentionWorking {
		t.Fatalf("the host's session shows %+v", a)
	}
	h.send(t, notify.Request{State: "done", Message: "finished", Kind: session.KindDone})
	var order []string
	for _, e := range h.local.Activity() {
		if e.Type == session.ActivityAttention {
			order = append(order, e.Message)
		}
	}
	if !slices.Equal(order, []string{"on it", "finished"}) {
		t.Fatalf("the host's session recorded %q", order)
	}
	h.back()
	untilListed(t, h.hs, "the copy's done", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.State == session.AttentionDone && i.Attention.Message == "finished"
	})
}

// A report that comes before the host's session is made waits for it and
// is then applied; one whose wait runs out is refused 503, and is not sent
// to the server instead.
func TestAReportBeforeTheSessionWaitsForIt(t *testing.T) {
	tok, _ := share.NewToken()
	l, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	waiting := make(chan struct{}, 1)
	l.waiting = func() { waiting <- struct{}{} }
	server := &routeCount{}
	ts := httptest.NewServer(server.wrap(http.NotFoundHandler()))
	t.Cleanup(ts.Close)
	target := notify.Target{URL: ts.URL + "/api/sessions/s/attention", Token: tok, Socket: l.path}
	sent := make(chan error, 1)
	go func() { sent <- target.Send(context.Background(), notify.Request{State: "done", Message: "early"}) }()
	select {
	case <-waiting:
	case err := <-sent:
		t.Fatalf("sent before the session was made: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the report never waited")
	}
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	local := session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{Log: discardLog, FileEdit: "off"})
	l.setSession(local)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if a := local.Info().Attention; a.State != session.AttentionDone || a.Message != "early" {
		t.Fatalf("the session shows %+v", a)
	}

	late, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(late.Close)
	late.wait = 20 * time.Millisecond
	target.Socket = late.path
	err = target.Send(context.Background(), notify.Request{State: "done", Message: "too early"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("a report past the wait: %v", err)
	}
	if n := server.n.Load(); n != 0 {
		t.Fatalf("the server's route was asked %d times", n)
	}
}

// Only the session's own agent token is taken: another session's, a
// token of no session, none and a token sent another way are refused 401,
// nothing applied and nothing sent to the server; an empty token is never
// one a listener checks against.
func TestTheSocketTakesTheSessionsOwnTokenAlone(t *testing.T) {
	h := newReportingHost(t)
	other := newReportingHost(t)
	before := h.local.Info().Attention
	for name, tok := range map[string]string{
		"another session's": other.target.Token,
		"of no session":     "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9v",
		"empty":             "",
	} {
		target := h.target
		target.Token = tok
		err := target.Send(t.Context(), notify.Request{State: "done", Message: name})
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("%s token: %v", name, err)
		}
	}
	// The token as a basic credential, or as the query's.
	for _, auth := range []string{"Basic " + h.target.Token, h.target.Token, ""} {
		req, _ := http.NewRequest(http.MethodPost, "http://host/attention?token="+h.target.Token, strings.NewReader(`{"state":"done"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.l.serveAttention(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("authorization %q: %d", auth, rec.Code)
		}
	}
	if a := h.local.Info().Attention; a.State != before.State || a.Message != before.Message {
		t.Fatalf("a refused report was applied: %+v", a)
	}
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's route was asked %d times", n)
	}
	if _, err := listenReports([]string{socketBase(t)}, "", discardLog); err == nil {
		t.Fatal("a listener was made with no token to check")
	}
}

// The body is the attention route's: at most 64 KiB, one object of its
// fields alone, nothing after it, within its bounds; anything else is
// refused 400 and nothing is applied. Another path or method is no route.
func TestTheSocketsBodyIsBoundedAndStrict(t *testing.T) {
	h := newReportingHost(t)
	tok := h.target.Token
	long := func(n int) string { return strings.Repeat("a", n) }
	opts := make([]map[string]string, 33)
	for i := range opts {
		opts[i] = map[string]string{"label": "x", "input": "x"}
	}
	manyOpts, _ := json.Marshal(map[string]any{"state": "needs_input", "options": opts})
	for name, body := range map[string]string{
		"past 64 KiB":             `{"state":"done","message":"` + long(64<<10) + `"}`,
		"an unknown field":        `{"state":"done","extra":1}`,
		"an event's field":        `{"state":"done","event":"progress"}`,
		"trailing data":           `{"state":"done"}{"state":"working"}`,
		"trailing text":           `{"state":"done"} x`,
		"no object":               `[]`,
		"no state":                `{"message":"m"}`,
		"an unknown state":        `{"state":"sleeping"}`,
		"a message past 4096":     `{"state":"done","message":"` + long(4097) + `"}`,
		"an unknown kind":         `{"state":"done","kind":"other"}`,
		"33 options":              string(manyOpts),
		"an agentSession past it": `{"state":"done","agentSession":"` + long(session.MaxAgentSessionID+1) + `"}`,
		"a plan past 1024":        `{"state":"needs_input","plan":"/` + long(proto.MaxPlanPath) + `"}`,
	} {
		code, reply := postSocket(t, h.l.path, notify.HostReportPath, tok, body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, reply)
		}
	}
	if code, _ := requestSocket(t, h.l.path, http.MethodGet, notify.HostReportPath, tok, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", code)
	}
	if code, _ := postSocket(t, h.l.path, "/events", tok, `{"type":"progress"}`); code != http.StatusNotFound {
		t.Errorf("another path: %d", code)
	}
	if a := h.local.Info().Attention; a.State != session.AttentionNone {
		t.Fatalf("a refused report was applied: %+v", a)
	}
	// A name of its turn out of its bound is left out, the report taken.
	if code, reply := postSocket(t, h.l.path, notify.HostReportPath, tok, `{"state":"done","message":"kept","turnId":"`+long(proto.MaxTurnID+1)+`"}`); code != http.StatusOK {
		t.Fatalf("an over-long turn name: %d %s", code, reply)
	}
	if a := h.local.Info().Attention; a.Message != "kept" {
		t.Fatalf("the session shows %+v", a)
	}
}

// A flood of reports on the socket spends the session's bucket: past it
// each is refused 429 and none applied, so at most the bucket's burst and
// what it earned meanwhile are taken.
func TestAFloodOfReportsSpendsTheSessionsBucket(t *testing.T) {
	h := newReportingHost(t)
	start := time.Now()
	taken, refused := 0, 0
	for i := range 200 {
		state := "working"
		if i%2 == 1 {
			state = "done"
		}
		switch code, reply := postSocket(t, h.l.path, notify.HostReportPath, h.target.Token, `{"state":"`+state+`"}`); code {
		case http.StatusOK:
			taken++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("report %d: %d %s", i, code, reply)
		}
	}
	earned := int(time.Since(start).Seconds()*session.EventRatePerSecond) + 1
	if refused == 0 || taken > session.EventBurst+earned {
		t.Fatalf("taken %d, refused %d, the bucket holds %d and earned %d", taken, refused, session.EventBurst, earned)
	}
}

// The socket's folder is made only this user's to enter, the socket in it
// this user's own, and both go when the listener closes.
func TestTheSocketsFolderIsTheUsersAlone(t *testing.T) {
	base := socketBase(t)
	tok, _ := share.NewToken()
	l, err := listenReports([]string{base}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(l.path) != l.dir || filepath.Dir(l.dir) != base || len(l.path) > notify.MaxHostSocketPath {
		t.Fatalf("socket %s in %s", l.path, l.dir)
	}
	fi, err := os.Lstat(l.dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v %v", fi, err)
	}
	if err := notify.CheckHostSocket(l.path); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := os.Lstat(l.dir); !os.IsNotExist(err) {
		t.Fatalf("the folder is left: %v", err)
	}
	// A base whose path leaves no room for the socket's is passed over.
	deep := filepath.Join(base, strings.Repeat("d", 90))
	if err := os.Mkdir(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	l2, err := listenReports([]string{deep, base}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l2.Close)
	if filepath.Dir(l2.dir) != base {
		t.Fatalf("made in %s", l2.dir)
	}
	if _, err := listenReports([]string{deep}, tok, discardLog); err == nil {
		t.Fatal("a socket was made past its path's bound")
	}
}

// leftSocket makes in base a socket folder as a host that was killed
// leaves it: its socket bound and no longer listening, or still listening
// (a live host's), made old.
func leftSocket(t *testing.T, base string, listening bool) string {
	t.Helper()
	dir, err := os.MkdirTemp(base, reportDirPrefix)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, reportSocketName)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if listening {
		t.Cleanup(func() { ln.Close() })
	} else {
		ln.SetUnlinkOnClose(false)
		ln.Close()
	}
	old := time.Now().Add(-2 * staleReportSocket)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A host's start removes the socket folder a host that ended without
// closing its own left behind, nothing listening on it, and makes its own:
// a live host's folder, a fresh one and anything not a host's folder stay.
func TestAHostsStartRemovesTheSocketAHostLeft(t *testing.T) {
	base := socketBase(t)
	dead := leftSocket(t, base, false)
	live := leftSocket(t, base, true)
	fresh := leftSocket(t, base, false)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(fresh, reportSocketName), now, now); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, reportDirPrefix+"open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	tok, _ := share.NewToken()
	l, err := listenReports([]string{base}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	if _, err := os.Lstat(dead); !os.IsNotExist(err) {
		t.Fatalf("the dead host's folder stays: %v", err)
	}
	for _, d := range []string{live, fresh, open, l.dir} {
		if _, err := os.Lstat(d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
}

// A report answered by the host is never sent to the server as well; only
// a socket that cannot be reached (gone, nothing listening, not this
// user's alone) sends it to the server's route, where it is applied as an
// older host's would be.
func TestTheSenderFallsBackOnlyWhenTheSocketCannotBeReached(t *testing.T) {
	h := newReportingHost(t)
	refused := h.target
	refused.Token = "wrong"
	if err := refused.Send(t.Context(), notify.Request{State: "done"}); err == nil {
		t.Fatal("a refused report was taken")
	}
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("a refusal was sent to the server: %d", n)
	}
	h.l.Close()
	h.send(t, notify.Request{State: "done", Message: "through the server"})
	if n := h.route.n.Load(); n != 1 {
		t.Fatalf("the server's route was asked %d times", n)
	}
	untilListed(t, h.local, "the server's report", func(i session.Info) bool { return i.Attention.Message == "through the server" })
}
