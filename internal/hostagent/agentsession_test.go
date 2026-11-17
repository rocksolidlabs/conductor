package hostagent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
// connection's start sends the log again from its start, in order.
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
	a.mu.Lock()
	log := slices.Clone(a.asLog)
	a.mu.Unlock()
	want := []agentSessionReport{{"a", true}, {"b", false}, {"a", false}}
	if !slices.Equal(log, want) {
		t.Fatalf("the log %+v", log)
	}
	// The sender has stopped before it is judged.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		a.mu.Lock()
		flushing := a.asFlushing
		a.mu.Unlock()
		if !flushing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sender never stopped")
		}
	}
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
