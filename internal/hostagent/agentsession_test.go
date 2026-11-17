package hostagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// codexLike is an agent whose recipe takes its session ids from its hooks,
// the lowest kept (Codex's main thread over its title thread).
func codexLike(t *testing.T) catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(catalog.File{Agents: []catalog.Agent{{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// recordedAgentSession is the agent session the server's record of the
// hosted session holds, read through History.
func (h *reportingHost) recordedAgentSession(t *testing.T) *journal.AgentSession {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.base+"/api/history/sessions/"+h.hs.Info().ID, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct{ Record journal.Record }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the record: %d %v", resp.StatusCode, err)
	}
	return got.Record.Agent.Session
}

// untilRecorded waits for the record's agent session to be want.
func (h *reportingHost) untilRecorded(t *testing.T, what string, want journal.AgentSession) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if as := h.recordedAgentSession(t); as != nil && *as == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the record holds %+v, want %+v", what, h.recordedAgentSession(t), want)
		}
	}
}

// A hosted agent whose hooks report through the host's socket has its own
// session id kept in the server's record as the attention route keeps it
// (the server's TestAHostedAgentSessionIsCapturedFromItsHook, through the
// socket): by the recipe, the lowest kept over a later higher one, an id
// not of the pattern dropped, the turn marking it resumable; in the
// record, never the hosted Info. One reported while the host's connection
// is down reaches the record as the host is back. A turn's end told twice
// changes nothing the second time, neither the attention nor the record.
// The server's route is never asked.
func TestAnAgentSessionReportedOnTheSocketIsKeptInTheRecord(t *testing.T) {
	h := newReportingHostOf(t, codexLike(t), "codex-like", true)
	low, high, lower := "0000aaaa-0001", "ffff0000-0002", "00000000-0000"
	h.send(t, notify.Request{State: "working", AgentSession: high})
	h.untilRecorded(t, "the first id, no turn", journal.AgentSession{ID: high, Source: session.AgentSessionHook})
	h.send(t, notify.Request{State: "done", AgentSession: high, Turn: true, TurnID: "t1"})
	h.untilRecorded(t, "its turn", journal.AgentSession{ID: high, Source: session.AgentSessionHook, Resumable: true})
	h.send(t, notify.Request{State: "working", AgentSession: low, Turn: true})
	h.send(t, notify.Request{State: "done", AgentSession: high, Turn: true})
	h.send(t, notify.Request{State: "done", AgentSession: "NOT-OF-THE-PATTERN", Turn: true})
	want := journal.AgentSession{ID: low, Source: session.AgentSessionHook, Resumable: true}
	h.untilRecorded(t, "lower, higher and a bad id", want)
	if h.hs.Info().AgentSession != nil {
		t.Fatalf("the hosted Info took an agent session: %+v", h.hs.Info().AgentSession)
	}

	// A turn's end told twice: the second changes nothing.
	h.send(t, notify.Request{State: "working", AgentSession: low, Turn: true})
	h.send(t, notify.Request{State: "done", Message: "once", Kind: session.KindDone, AgentSession: low, Turn: true, TurnID: "t9"})
	first := h.local.Info().Attention
	h.send(t, notify.Request{State: "done", Message: "twice", Kind: session.KindDone, AgentSession: low, Turn: true, TurnID: "t9"})
	if a := h.local.Info().Attention; a.ID != first.ID || a.Message != "once" {
		t.Fatalf("a turn's end told twice was taken again: %+v", a)
	}

	h.away(t)
	h.send(t, notify.Request{State: "done", AgentSession: lower, Turn: true})
	if as := h.recordedAgentSession(t); as == nil || *as != want {
		t.Fatalf("the record changed while the host was away: %+v", as)
	}
	h.back()
	h.untilRecorded(t, "the id reported while away", journal.AgentSession{ID: lower, Source: session.AgentSessionHook, Resumable: true})
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's route was asked %d times", n)
	}
}

// Reports of the same id one after another are kept as one, its turn if
// either was of a turn; the log keeps its order and its bound, the oldest
// going first; nothing is sent while the server takes none; and a
// connection's start sends what is not known taken, in order.
func TestTheAgentSessionLogKeepsItsOrderAndBound(t *testing.T) {
	a, _, _, _ := sentAttention(t)
	got := make(chan proto.HostAgentSessionMsg, 256)
	a.sendHook = func(v any) {
		if m, ok := v.(proto.HostAgentSessionMsg); ok {
			got <- m
		}
	}
	a.noteAgentSession("a", false)
	a.noteAgentSession("a", true)
	a.noteAgentSession("b", false)
	a.noteAgentSession("a", false)
	want := []agentSessionReport{{id: "a", turn: true}, {id: "b"}, {id: "a"}}
	if log := logOf(a); !slices.Equal(log, want) {
		t.Fatalf("the log %+v", log)
	}
	untilSenderStopped(t, a)
	if len(got) != 0 {
		t.Fatalf("sent to a server that takes none: %+v", <-got)
	}
	a.agentSessions.Store(true)
	a.replayAgentSessions()
	for i, w := range want {
		select {
		case m := <-got:
			if m.T != proto.HostAgentSession || m.AgentSession != w.id || m.Turn != w.turn {
				t.Fatalf("sent %d: %+v, want %+v", i, m, w)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("sent %d: nothing", i)
		}
	}
	a.agentSessions.Store(false)
	for i := range maxAgentSessionLog + 5 {
		a.noteAgentSession(strings.Repeat("x", i+1), false)
	}
	a.mu.Lock()
	n, oldest := len(a.asLog), a.asLog[0].id
	a.mu.Unlock()
	if n != maxAgentSessionLog || oldest != strings.Repeat("x", 6) {
		t.Fatalf("the log holds %d, the oldest %q", n, oldest)
	}
}

// A server that does not say it takes agent_session (one of an earlier
// release) is never sent one, and its host names no socket to its agent:
// the agent reports to the server's route, which keeps the id, as before.
func TestAnOlderServersHostNamesNoSocket(t *testing.T) {
	srv := newStubServer(t)
	out := filepath.Join(t.TempDir(), "env")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Options{ServerURL: srv.url, Token: hostToken, Name: "older", RelayOnly: true, Log: discardLog,
			Argv: []string{"/bin/sh", "-c", `printf '%s\n' "${CONDUCTOR_HOST_SOCKET-unset}" > "$0"; exec /bin/cat`, out}})
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("the host did not stop")
		}
	})
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(out); err == nil && strings.HasSuffix(string(b), "\n") {
			if string(b) != "unset\n" {
				t.Fatalf("the agent was handed a socket under an older server: %q", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent never printed its environment")
		}
	}
	for {
		select {
		case data := <-srv.got:
			if strings.Contains(string(data), `"agent_session"`) {
				t.Fatalf("an older server was sent %s", data)
			}
			continue
		default:
		}
		break
	}
}

// A host whose later server does not say it takes agent_session stops
// listening on its socket, which goes: the agent's sender then reports to
// the server's route.
func TestALaterOlderServerEndsTheSocket(t *testing.T) {
	srv := newStubServer(t)
	tok, _ := share.NewToken()
	l, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	a := &agent{opts: Options{ServerURL: srv.url, Token: hostToken, Name: "later", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", Log: discardLog}, peers: map[string]*peer{}, log: discardLog, agentToken: tok, reports: l}
	a.agentSessions.Store(true)
	a.dir = t.TempDir()
	if a.wsURL, err = controlURL(srv.url); err != nil {
		t.Fatal(err)
	}
	conn, _, err := a.dialAndRegister(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if a.agentSessions.Load() {
		t.Fatal("the host still holds that its server takes agent_session")
	}
	if _, err := os.Lstat(l.path); !os.IsNotExist(err) {
		t.Fatalf("the socket stays: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reports != nil {
		t.Fatal("the host still holds its socket")
	}
}

// logOf is a's log, each entry's id and turn.
func logOf(a *agent) []agentSessionReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]agentSessionReport, 0, len(a.asLog))
	for _, r := range a.asLog {
		out = append(out, agentSessionReport{id: r.id, turn: r.turn})
	}
	return out
}

// untilSenderStopped waits for a's sender of agent session ids to stop:
// what it sends is judged after.
func untilSenderStopped(t *testing.T, a *agent) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		a.mu.Lock()
		flushing := a.asFlushing
		a.mu.Unlock()
		if !flushing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the sender never stopped")
		}
	}
}

// agentSessionSender is an agent whose agent_session sends go to sent,
// each answered by answer (nil: sent), and whose pings are answered as
// confirmed says.
func agentSessionSender(t *testing.T, confirmed bool) (a *agent, sent chan proto.HostAgentSessionMsg, answer *func(n int) error) {
	t.Helper()
	a, _, _, _ = sentAttention(t)
	sent = make(chan proto.HostAgentSessionMsg, 256)
	var mu sync.Mutex
	n := 0
	ans := func(int) error { return nil }
	answer = &ans
	a.asSend = func(m proto.HostAgentSessionMsg) error {
		mu.Lock()
		n++
		k, f := n, *answer
		mu.Unlock()
		err := f(k)
		if err == nil {
			sent <- m
		}
		return err
	}
	a.asConfirm = func() bool { return confirmed }
	a.agentSessions.Store(true)
	return a, sent, answer
}

// drained is what sent holds, once the sender has stopped.
func drained(t *testing.T, a *agent, sent chan proto.HostAgentSessionMsg) []string {
	t.Helper()
	untilSenderStopped(t, a)
	var out []string
	for len(sent) > 0 {
		out = append(out, (<-sent).AgentSession)
	}
	return out
}

// An id the server is known to have taken (a ping answered after it on
// its connection) is never sent again: two connections' starts with
// nothing new send nothing.
func TestAReconnectSendsNoTakenIDAgain(t *testing.T) {
	a, sent, _ := agentSessionSender(t, true)
	a.noteAgentSession("a", false)
	a.noteAgentSession("b", true)
	if got := drained(t, a, sent); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("sent %q", got)
	}
	a.replayAgentSessions()
	a.replayAgentSessions()
	if got := drained(t, a, sent); len(got) != 0 {
		t.Fatalf("sent again %q", got)
	}
	// The same id again, no turn: nothing new. A turn set on the last
	// entry since it was taken is that entry changed: sent, with its turn.
	a.noteAgentSession("b", false)
	a.noteAgentSession("c", false)
	if got := drained(t, a, sent); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("sent %q", got)
	}
	a.noteAgentSession("c", true)
	untilSenderStopped(t, a)
	if len(sent) != 1 {
		t.Fatalf("sent %d for the turn", len(sent))
	}
	if m := <-sent; m.AgentSession != "c" || !m.Turn {
		t.Fatalf("sent %+v for the turn", m)
	}
}

// A replay cut short (its connection gone after its first entry) is taken
// up at the next connection's start from the first entry not known taken,
// and ends at the newest id.
func TestAReplayCutShortEndsAtTheNewest(t *testing.T) {
	a, sent, answer := agentSessionSender(t, false)
	a.agentSessions.Store(false)
	for _, id := range []string{"a", "b", "c"} {
		a.noteAgentSession(id, true)
	}
	a.agentSessions.Store(true)
	*answer = func(n int) error {
		if n >= 2 {
			return errConnDown
		}
		return nil
	}
	a.replayAgentSessions()
	if got := drained(t, a, sent); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("the cut replay sent %q", got)
	}
	*answer = func(int) error { return nil }
	a.replayAgentSessions()
	got := drained(t, a, sent)
	if len(got) == 0 || got[len(got)-1] != "c" || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("the replay taken up sent %q", got)
	}
}

// A send that finds no turn while its connection stays is tried again
// until it goes, with no report after it.
func TestAnUnsentAgentSessionIsTriedAgain(t *testing.T) {
	a, sent, answer := agentSessionSender(t, true)
	*answer = func(n int) error {
		if n == 1 {
			return session.ErrSlowConsumer
		}
		return nil
	}
	a.noteAgentSession("last", true)
	if got := drained(t, a, sent); !slices.Equal(got, []string{"last"}) {
		t.Fatalf("sent %q", got)
	}
}

// After the socket was dropped for a server that took no ids, the route
// recorded a newer id; the host's next connection to a server that takes
// them sends none of what it logged before, an id it had not sent among
// them: the record keeps the route's newest id. Through a real host agent and the server's own capture.
func TestARouteRecordedIDIsNotSteppedBackByTheHost(t *testing.T) {
	cat, err := catalog.Load(catalog.File{Agents: []catalog.Agent{{ID: "claude-like", Name: "claude-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLatest, ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := newReportingHostOf(t, cat, "claude-like", true)
	idA, idB, idC := "aaaa-0001", "bbbb-0002", "cccc-0003"
	h.send(t, notify.Request{State: "working", AgentSession: idA, Turn: true})
	h.untilRecorded(t, "A", journal.AgentSession{ID: idA, Source: session.AgentSessionHook, Resumable: true})
	// B is reported while the host is away: not sent yet.
	h.away(t)
	h.send(t, notify.Request{State: "done", AgentSession: idB, Turn: true})

	// The host comes back to a server that takes no ids: it drops its
	// socket, and with it what it had not sent.
	h.a.mu.Lock()
	secret := h.a.secret
	h.a.mu.Unlock()
	older := olderServerKeeping(t, h.hs.Info().ID, secret)
	real := h.a.wsURL
	if h.a.wsURL, err = controlURL(older); err != nil {
		t.Fatal(err)
	}
	oc, _, err := h.a.dialAndRegister(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(h.l.path); !os.IsNotExist(err) {
		t.Fatalf("the socket stays: %v", err)
	}
	oc.CloseNow()
	// The agent's sender, the socket gone, reports C to the server's route.
	h.send(t, notify.Request{State: "done", AgentSession: idC, Turn: true})
	wantC := journal.AgentSession{ID: idC, Source: session.AgentSessionHook, Resumable: true}
	h.untilRecorded(t, "C through the route", wantC)

	// Back to a server that takes ids.
	h.a.wsURL = real
	h.back()
	untilListed(t, h.hs, "the host back", func(i session.Info) bool { return i.Status == session.StatusRunning })
	untilSenderStopped(t, h.a)
	// A change the host tells after its connection's start reaches the
	// copy after anything sent before it on that connection.
	h.a.local.SetAttentionFull(session.AttentionNeedsInput, "marker", session.SourceAdmin, "", nil)
	untilListed(t, h.hs, "the marker", func(i session.Info) bool { return i.Attention.Message == "marker" })
	if as := h.recordedAgentSession(t); as == nil || *as != wantC {
		t.Fatalf("the record holds %+v, want %+v", as, wantC)
	}
}

// olderServerKeeping is a server of an earlier release that keeps the
// session's id and secret: it registers the host as id and says nothing
// of agent_session.
func olderServerKeeping(t *testing.T, id, secret string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if _, _, err := c.Read(r.Context()); err != nil {
			return
		}
		reg, _ := json.Marshal(map[string]any{"t": proto.HostRegistered, "sessionId": id, "secret": secret, "shareBaseUrl": "http://older.invalid", "resumed": true})
		if c.Write(r.Context(), websocket.MessageText, reg) != nil {
			return
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A report still under way on the socket as the socket is dropped (it was
// past the session's wait before the drop) logs nothing: when a server
// that takes ids is connected next, nothing is sent and the log is empty.
func TestAReportUnderWayAsTheSocketIsDroppedLogsNothing(t *testing.T) {
	a, sent, _ := agentSessionSender(t, true)
	a.agentSessions.Store(false)
	tok, _ := share.NewToken()
	l, err := listenReports([]string{socketBase(t)}, tok, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	a.mu.Lock()
	a.reports = l
	a.mu.Unlock()
	a.dropReports()
	// The report the handler was applying as the socket went.
	a.noteAgentSession("old", true)
	a.agentSessions.Store(true)
	a.replayAgentSessions()
	if got := drained(t, a, sent); len(got) != 0 {
		t.Fatalf("sent %q after the socket was dropped", got)
	}
	if log := logOf(a); len(log) != 0 {
		t.Fatalf("the log holds %+v", log)
	}
}
