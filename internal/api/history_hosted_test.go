package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// A hosted session's record (docs/plans/history.md, PR-12): what its host
// reports of it, nothing of this server's machine.

// registerReporting registers the hosted session localID of agent whose
// host reports rep, its agent's hooks holding token.
func registerReporting(t *testing.T, e *testEnv, localID, agent, token string, rep proto.HostReport) *signal.HostedSession {
	t.Helper()
	hs, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host:    proto.HostInfo{Name: "mac", User: "ada", Instance: "instance-r"},
		Session: proto.HostSession{AgentID: agent, Command: []string{agent}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: localID, AgentToken: token, HostReport: rep}}, signal.NewHostConn())
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

// The record takes where the session runs, its agent's version, its
// repository and common directory and its start commit as the host
// reported them as it registered (StartFrom host), its end commit from the
// session_info sent before its end (one past its bound dropped, the
// connection kept), and its streams; a register past a bound is refused.
func TestAHostedRecordTakesWhatItsHostReports(t *testing.T) {
	e := newTestEnv(t, nil)
	start, end := strings.Repeat("a", 40), strings.Repeat("b", 40)
	rep := proto.HostReport{Where: "native", AgentVersion: "2.1.304", Repo: "/Users/ada/app", RepoCommon: "/Users/ada/app/.git", StartCommit: start}
	bad := rep
	bad.StartCommit = "HEAD"
	if _, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac"},
		Session: proto.HostSession{AgentID: "sh", Command: []string{"sh"}, Cwd: "/w", HostReport: bad}}, signal.NewHostConn()); !errors.Is(err, signal.ErrBadRegister) {
		t.Fatalf("a register past its report's bounds: %v", err)
	}
	hs := registerReporting(t, e, "1", "sh", "", rep)
	id := hs.Info().ID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hs.HostActivity(proto.Activity{Type: "progress", Message: "built", At: now}, "")
	hs.HostActivity(proto.Activity{Type: "file", Op: "edit", Path: "main.go", At: now}, "")
	hs.HostChat(proto.ChatMessage{ID: "m1", Kind: proto.ChatKindMessage, By: proto.ChatBy{Role: string(session.RoleControl), Name: "ada"}, Text: "done", At: now})
	past := `{"t":"session_info","endCommit":"` + strings.Repeat("c", 40) + `"}` + strings.Repeat(" ", proto.MaxSessionInfo)
	if !e.srv.handleHostMessage(hs, &hostConnState{}, []byte(past)) {
		t.Fatal("a session_info past its bound closed the connection")
	}
	if !e.srv.handleHostMessage(hs, &hostConnState{}, []byte(`{"t":"session_info","endCommit":"`+end+`"}`)) {
		t.Fatal("a session_info closed the connection")
	}
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	r := endedRecord(t, e, id)
	if r.Kind != "hosted" || r.Host != (journal.Host{Name: "mac", User: "ada", Owner: hs.Owner(), Hosted: true}) || r.Where != "native" || r.Agent.Version != "2.1.304" {
		t.Fatalf("the record's head: %+v", r)
	}
	if r.Repo != rep.Repo || r.RepoCommon != rep.RepoCommon || r.Start != start || r.StartFrom != journal.StartFromHost || r.End != end || r.Ending != "exited (exit 0)" || r.Launch != nil {
		t.Fatalf("the record's launch and end: %+v", r)
	}
	if got := messagesOf(r); !slices.Contains(got, "built") || !slices.Contains(got, "done") || len(r.Files) != 1 || r.Files[0].Path != "/Users/ada/app/main.go" {
		t.Fatalf("the record's streams: %q %+v", got, r.Files)
	}
	if resp, got := e.do("POST", "/api/history/sessions/"+id+"/resume", adminToken, map[string]any{"commandId": "c1"}); resp.StatusCode != http.StatusBadRequest || got["error"].(map[string]any)["code"] != "hosted_session" {
		t.Fatalf("a hosted record resumed from the server: %d %v", resp.StatusCode, got)
	}
}

// An older host reports nothing: its record's where, version, repository
// and commits are empty, not reported, never this server's facts and never
// native.
func TestAnOldHostsRecordSaysNotReported(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	r := endedRecord(t, e, hs.Info().ID)
	if !r.Host.Hosted || r.Where != "" || r.Agent.Version != "" || r.Repo != "" || r.RepoCommon != "" || r.Start != "" || r.StartFrom != "" || r.End != "" {
		t.Fatalf("an old host's record: %+v", r)
	}
	if r.Cwd != e.root || r.Branch != "main" {
		t.Fatalf("what an old host does report: %+v", r)
	}
}

// A hosted agent's own session is captured from its hook's report to this
// server, by the catalog's recipe for the agent: Codex's lowest id kept
// over a later higher one, an id not of the pattern dropped; it reaches
// the record and never the hosted session's Info.
func TestAHostedAgentSessionIsCapturedFromItsHook(t *testing.T) {
	codex := catalog.Agent{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	e := newTestEnvAgents(t, nil, nil, []catalog.Agent{codex})
	tok := "hosted-agent-token-0123456789abcdef"
	hs := registerReporting(t, e, "1", "codex-like", tok, proto.HostReport{})
	id := hs.Info().ID
	report := func(as string) {
		t.Helper()
		if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "done", "agentSession": as, "turn": true}); resp.StatusCode != http.StatusOK {
			t.Fatalf("the hook's report: %d %v", resp.StatusCode, out)
		}
	}
	recorded := func() *journal.AgentSession {
		r, _, err := e.srv.sessionRecords.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		return r.Agent.Session
	}
	low, high := "0000aaaa-0001", "ffff0000-0002"
	report(high)
	if as := recorded(); as == nil || as.ID != high || as.Source != session.AgentSessionHook || !as.Resumable {
		t.Fatalf("the first id: %+v", as)
	}
	report(low)
	report(high)
	report("NOT-OF-THE-PATTERN")
	if as := recorded(); as == nil || as.ID != low {
		t.Fatalf("the record's agent session after lower, higher and a bad id: %+v", as)
	}
	if hs.Info().AgentSession != nil {
		t.Fatalf("the hosted Info took an agent session: %+v", hs.Info().AgentSession)
	}
	// An agent with no recipe captures nothing.
	plain := registerReporting(t, e, "2", "sh", tok+"2", proto.HostReport{})
	e.do("POST", "/api/sessions/"+plain.Info().ID+"/attention", tok+"2", map[string]any{"state": "done", "agentSession": low, "turn": true})
	if r, _, _ := e.srv.sessionRecords.Get(plain.Info().ID); r.Agent.Session != nil {
		t.Fatalf("an agent with no recipe: %+v", r.Agent.Session)
	}
}

// A hosted record ends host away when the server stops observing it (a
// restart, the hub letting its session go past the grace), never lost and
// never ended, and the same host's next registration takes the same record
// up again, its entries kept.
func TestAHostedRecordIsTakenUpAgainByItsHost(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	hs.HostActivity(proto.Activity{Type: "progress", Message: "first", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	e2 := restartServer(t, e)
	if r, _ := recordFile(t, e2, id); r.Ending != journal.EndingHostAway || r.LostAt.IsZero() || !r.EndedAt.IsZero() {
		t.Fatalf("after the restart: %+v", r)
	}
	hs2, conn := registerHost(t, e2, "instance-a", "1")
	if hs2.Info().ID != id {
		t.Fatalf("the host's id came back as %s, not %s", hs2.Info().ID, id)
	}
	hs2.HostActivity(proto.Activity{Type: "progress", Message: "second", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	r, _ := recordFile(t, e2, id)
	if r.Reopened != 1 || r.Ending != "" || !slices.Equal(messagesOf(r), []string{"first", journal.Reopening, "second"}) {
		t.Fatalf("taken up after the restart: %+v", r)
	}
	hs2.HostDisconnected(conn)
	e2.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
	until(t, "the hub let the session go", func() bool { _, ok := e2.srv.registry.Get(id); return !ok })
	if r, _ := recordFile(t, e2, id); r.Ending != journal.EndingHostAway || r.Reopened != 1 {
		t.Fatalf("let go past the grace: %+v", r)
	}
	registerHost(t, e2, "instance-a", "1")
	if r, _ := recordFile(t, e2, id); r.Reopened != 2 || r.Ending != "" {
		t.Fatalf("taken up after the grace: %+v", r)
	}
}

// messagesOf is a record's entries' messages, in order.
func messagesOf(r journal.Record) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Message)
	}
	return out
}
