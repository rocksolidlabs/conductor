package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A hosted agent's own session id a host sends on agent_session (its
// agent's report to the host's socket named it) is kept as the attention
// route keeps one (TestAHostedAgentSessionIsCapturedFromItsHook): by the
// catalog's recipe, Codex's lowest id kept over a later higher one, an id
// not of the pattern dropped, the turn marking it resumable, the same id
// again changing nothing; in the record, never the hosted Info. One out of
// its bounds, or not one, is dropped and the connection kept.
func TestAHostedAgentSessionIsCapturedFromItsHostsMessage(t *testing.T) {
	codex := catalog.Agent{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	e := newTestEnvAgents(t, nil, nil, []catalog.Agent{codex})
	hs := registerReporting(t, e, "1", "codex-like", "hosted-agent-token-0123456789abcdef", proto.HostReport{})
	id := hs.Info().ID
	send := func(raw string) {
		t.Helper()
		if !e.srv.handleHostMessage(hs, &hostConnState{}, proto.HostAgentSession, []byte(raw)) {
			t.Fatalf("%s closed the connection", raw)
		}
	}
	report := func(as string, turn bool) {
		t.Helper()
		b, _ := json.Marshal(proto.HostAgentSessionMsg{T: proto.HostAgentSession, SessionID: id, AgentSession: as, Turn: turn})
		send(string(b))
	}
	recorded := func() *journal.AgentSession {
		t.Helper()
		r, _, err := e.srv.sessionRecords.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return r.Agent.Session
	}
	low, high := "0000aaaa-0001", "ffff0000-0002"
	report(high, false)
	if as := recorded(); as == nil || as.ID != high || as.Source != session.AgentSessionHook || as.Resumable {
		t.Fatalf("the first id, no turn: %+v", as)
	}
	report(high, true)
	if as := recorded(); as == nil || as.ID != high || !as.Resumable {
		t.Fatalf("its turn: %+v", as)
	}
	report(low, true)
	report(high, true)
	report("NOT-OF-THE-PATTERN", true)
	want := journal.AgentSession{ID: low, Source: session.AgentSessionHook, Resumable: true}
	if as := recorded(); as == nil || *as != want {
		t.Fatalf("after lower, higher and a bad id: %+v", as)
	}
	// Past its bound, not one, an unknown field: dropped.
	for _, raw := range []string{
		`{"t":"agent_session","agentSession":"` + strings.Repeat("0", proto.MaxAgentSession+1) + `"}`,
		`{"t":"agent_session","agentSession":"0000"}` + strings.Repeat(" ", proto.MaxAgentSessionMsg),
		`{"t":"agent_session","agentSession":"0000","extra":1}`,
		`{"t":"agent_session"}`,
		`not json`,
	} {
		send(raw)
	}
	if as := recorded(); as == nil || *as != want {
		t.Fatalf("a dropped message changed the record: %+v", as)
	}
	if hs.Info().AgentSession != nil {
		t.Fatalf("the hosted Info took an agent session: %+v", hs.Info().AgentSession)
	}
	// An agent with no recipe captures nothing.
	plain := registerReporting(t, e, "2", "sh", "", proto.HostReport{})
	if !e.srv.handleHostMessage(plain, &hostConnState{}, proto.HostAgentSession, []byte(`{"t":"agent_session","agentSession":"`+low+`","turn":true}`)) {
		t.Fatal("closed the connection")
	}
	if r, _, _ := e.srv.sessionRecords.Get(plain.Info().ID); r.Agent.Session != nil {
		t.Fatalf("an agent with no recipe: %+v", r.Agent.Session)
	}
}

// A session's registered says the server takes agent_session.
func TestRegisteredSaysTheServerTakesAgentSessions(t *testing.T) {
	e := newTestEnv(t, nil)
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + "/ws/host"
	hc, _, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-host-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hc.CloseNow() })
	reg, _ := json.Marshal(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "laptop"},
		Session: proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}})
	if err := hc.Write(t.Context(), websocket.MessageText, reg); err != nil {
		t.Fatal(err)
	}
	_, data, err := hc.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var registered proto.Registered
	if err := json.Unmarshal(data, &registered); err != nil || !registered.AgentSessions {
		t.Fatalf("registered %s: %v", data, err)
	}
}
