package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/history"
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
	return registerSession(t, e, signal.NewHostConn(), proto.HostSession{AgentID: agent, Command: []string{agent}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: localID, AgentToken: token, HostReport: rep})
}

// registerSession registers s from instance-r's host on conn.
func registerSession(t *testing.T, e *testEnv, conn *signal.HostConn, s proto.HostSession) *signal.HostedSession {
	t.Helper()
	hs, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac", User: "ada", Instance: "instance-r"}, Session: s}, conn)
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
	// Past a bound, and a field with a character that changes how it is
	// drawn: the register refused alike.
	for _, bad := range []proto.HostReport{{StartCommit: "HEAD"}, {AgentVersion: "2.1.304" + string(rune(0x202e))}, {Repo: "/Users/ada/" + string(rune(0x200b)) + "app"}} {
		if _, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac"},
			Session: proto.HostSession{AgentID: "sh", Command: []string{"sh"}, Cwd: "/w", HostReport: bad}}, signal.NewHostConn()); !errors.Is(err, signal.ErrBadRegister) {
			t.Fatalf("a register reporting %+v: %v", bad, err)
		}
	}
	hs := registerReporting(t, e, "1", "sh", "", rep)
	id := hs.Info().ID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hs.HostActivity(proto.Activity{Type: "progress", Message: "built", At: now}, "")
	hs.HostActivity(proto.Activity{Type: "file", Op: "edit", Path: "main.go", At: now}, "")
	hs.HostChat(proto.ChatMessage{ID: "m1", Kind: proto.ChatKindMessage, By: proto.ChatBy{Role: string(session.RoleControl), Name: "ada"}, Text: "done", At: now})
	past := `{"t":"session_info","endCommit":"` + strings.Repeat("c", 40) + `"}` + strings.Repeat(" ", proto.MaxSessionInfo)
	if !e.srv.handleHostMessage(hs, &hostConnState{}, proto.HostSessionInfo, []byte(past)) {
		t.Fatal("a session_info past its bound closed the connection")
	}
	if !e.srv.handleHostMessage(hs, &hostConnState{}, proto.HostSessionInfo, []byte(`{"t":"session_info","endCommit":"`+end+`"}`)) {
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

// The agent session a hosted record captured is the record's: taken up
// again after a restart, and after the hub let its session go past the
// grace, the recipe's policy goes on from what the record holds (Codex's
// lowest kept over a later higher id, the same id again without a turn
// changing nothing).
func TestAReopenedRecordKeepsItsAgentSession(t *testing.T) {
	codex := catalog.Agent{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	low, high := "0000aaaa-0001", "ffff0000-0002"
	for _, path := range []string{"a restart", "the grace"} {
		t.Run(path, func(t *testing.T) {
			e := newTestEnvAgents(t, nil, nil, []catalog.Agent{codex})
			tok := "hosted-agent-token-0123456789abcdef"
			s := proto.HostSession{AgentID: "codex-like", Command: []string{"codex"}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: "1", AgentToken: tok}
			conn := signal.NewHostConn()
			id := registerSession(t, e, conn, s).Info().ID
			report := func(e *testEnv, as string, turn bool) {
				t.Helper()
				if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "done", "agentSession": as, "turn": turn}); resp.StatusCode != http.StatusOK {
					t.Fatalf("the hook's report: %d %v", resp.StatusCode, out)
				}
			}
			report(e, low, true)
			if path == "a restart" {
				e = restartServer(t, e)
			} else {
				hs, _ := e.srv.registry.Get(id)
				hs.(*signal.HostedSession).HostDisconnected(conn)
				e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
				until(t, "the hub let the session go", func() bool { _, ok := e.srv.registry.Get(id); return !ok })
			}
			if again := registerSession(t, e, signal.NewHostConn(), s).Info().ID; again != id {
				t.Fatalf("the id came back as %s", again)
			}
			report(e, high, true)
			report(e, low, false)
			r, _, err := e.srv.sessionRecords.Get(id)
			if err != nil || r.Reopened != 1 {
				t.Fatalf("the record taken up again: %+v %v", r, err)
			}
			if as := r.Agent.Session; as == nil || as.ID != low || !as.Resumable || as.Source != session.AgentSessionHook {
				t.Fatalf("the record's agent session after %s: %+v", path, as)
			}
		})
	}
}

// A hook that comes while a registration is still putting the session's
// recorder in place (the registry holds the session, the hub's OnAdded has
// not returned) is not dropped: the registration held at a gate after its
// record is taken up, a hook sent inside it, a lower id or the same id
// with a turn, waits, and once the gate opens the record holds it.
func TestAHookDuringARegistrationReachesTheRecord(t *testing.T) {
	codex := catalog.Agent{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	low, high := "0000aaaa-0001", "ffff0000-0002"
	for _, c := range []struct {
		name      string
		first     string
		firstTurn bool
	}{{"a lower id", high, true}, {"a turn of the same id", low, false}} {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnvAgents(t, nil, nil, []catalog.Agent{codex})
			tok := "hosted-agent-token-0123456789abcdef"
			s := proto.HostSession{AgentID: "codex-like", Command: []string{"codex"}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: "1", AgentToken: tok}
			conn := signal.NewHostConn()
			hs := registerSession(t, e, conn, s)
			id := hs.Info().ID
			report := func(as string, turn bool) (*http.Response, map[string]any) {
				return e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "done", "agentSession": as, "turn": turn})
			}
			if resp, out := report(c.first, c.firstTurn); resp.StatusCode != http.StatusOK {
				t.Fatalf("the first report: %d %v", resp.StatusCode, out)
			}
			hs.HostDisconnected(conn)
			e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
			until(t, "the hub let the session go", func() bool { _, ok := e.srv.registry.Get(id); return !ok })
			begun, gate, waits := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
			hook := func(stage, sid string) {
				switch {
				case sid != id:
				case stage == "hostedBegun":
					close(begun)
					<-gate
				case stage == "hostedCaptureWaits":
					waits <- struct{}{}
				}
			}
			e.srv.recordHook.Store(&hook)
			t.Cleanup(func() { e.srv.recordHook.Store(nil) })
			registered := make(chan error, 1)
			go func() {
				_, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac", User: "ada", Instance: "instance-r"}, Session: s}, signal.NewHostConn())
				registered <- err
			}()
			<-begun
			answered := make(chan int, 1)
			go func() {
				resp, _ := report(low, true)
				answered <- resp.StatusCode
			}()
			select {
			case <-waits:
			case code := <-answered:
				answered <- code
			}
			close(gate)
			if err := <-registered; err != nil {
				t.Fatal(err)
			}
			if code := <-answered; code != http.StatusOK {
				t.Fatalf("the hook inside the registration: %d", code)
			}
			r, _, err := e.srv.sessionRecords.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if as := r.Agent.Session; as == nil || as.ID != low || !as.Resumable {
				t.Fatalf("the record's agent session: %+v", as)
			}
		})
	}
}

// A hook whose wait for the registration runs out on its bound is refused,
// 503 registering, never answered 200 with its agent session lost: the
// registration held at its gate past the wait (the server's captureWait,
// so only the bound can end it), the report is refused, its attention
// applied and nothing captured; once the gate opens the same report, sent
// again, is answered 200 and the record holds its id.
func TestAHookWhoseRegistrationWaitRunsOutIsRefused(t *testing.T) {
	codex := catalog.Agent{ID: "codex-like", Name: "codex-like", Command: []string{"/bin/cat"},
		Session: &catalog.SessionRecipe{IDFrom: "hook", IDPolicy: session.PolicyLowest, ResumeArgs: []string{"resume", "{id}"}, IDPattern: "^[0-9a-f-]+$"}}
	e := newTestEnvAgents(t, nil, nil, []catalog.Agent{codex})
	tok := "hosted-agent-token-0123456789abcdef"
	s := proto.HostSession{AgentID: "codex-like", Command: []string{"codex"}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: "1", AgentToken: tok}
	conn := signal.NewHostConn()
	hs := registerSession(t, e, conn, s)
	id := hs.Info().ID
	hs.HostDisconnected(conn)
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
	until(t, "the hub let the session go", func() bool { _, ok := e.srv.registry.Get(id); return !ok })
	begun, gate := make(chan struct{}), make(chan struct{})
	hook := func(stage, sid string) {
		if sid == id && stage == "hostedBegun" {
			close(begun)
			<-gate
		}
	}
	e.srv.recordHook.Store(&hook)
	t.Cleanup(func() { e.srv.recordHook.Store(nil) })
	e.srv.captureWait.Store(int64(time.Millisecond))
	registered := make(chan error, 1)
	go func() {
		_, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "mac", User: "ada", Instance: "instance-r"}, Session: s}, signal.NewHostConn())
		registered <- err
	}()
	<-begun
	report := func() (*http.Response, map[string]any) {
		return e.do("POST", "/api/sessions/"+id+"/attention", tok, map[string]any{"state": "done", "agentSession": "0000aaaa-0001", "turn": true})
	}
	resp, out := report()
	refusal, _ := out["error"].(map[string]any)
	if code, _ := refusal["code"].(string); resp.StatusCode != http.StatusServiceUnavailable || code != "registering" {
		close(gate)
		t.Fatalf("the hook past the wait: %d %v", resp.StatusCode, out)
	}
	if d, ok := e.srv.registry.Get(id); !ok || d.Info().Attention.State != session.AttentionDone {
		close(gate)
		t.Fatalf("the refused report's attention was not applied")
	}
	r, _, err := e.srv.sessionRecords.Get(id)
	if err != nil || r.Agent.Session != nil {
		close(gate)
		t.Fatalf("the refused report captured: %+v %v", r.Agent.Session, err)
	}
	close(gate)
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if resp, out := report(); resp.StatusCode != http.StatusOK {
		t.Fatalf("the report sent again: %d %v", resp.StatusCode, out)
	}
	if r, _, err = e.srv.sessionRecords.Get(id); err != nil || r.Agent.Session == nil || r.Agent.Session.ID != "0000aaaa-0001" {
		t.Fatalf("the record after the report sent again: %+v %v", r.Agent.Session, err)
	}
}

// A hosted session's availability follows its host: live while it is
// connected, host away while it is disconnected inside the grace, live
// again once it has connected again, and its record's host away once the
// hub let the session go, as before.
func TestAHostedSessionsAvailabilityFollowsItsHost(t *testing.T) {
	e := newTestEnv(t, nil)
	s := proto.HostSession{AgentID: "codex", Command: []string{"codex"}, Cwd: "/Users/ada/app", Cols: 80, Rows: 24, LocalID: "1"}
	conn := signal.NewHostConn()
	hs := registerSession(t, e, conn, s)
	id := hs.Info().ID
	state := func() string {
		return e.srv.newAvailability(t.Context(), nil, e.srv.ledger).session(id, true, history.Entry{Hosted: true, Outcome: "running"}).State
	}
	if st := state(); st != availLive {
		t.Fatalf("connected: %s", st)
	}
	hs.HostDisconnected(conn)
	if st := state(); st != availHostAway {
		t.Fatalf("disconnected, inside the grace: %s", st)
	}
	again := signal.NewHostConn()
	if back := registerSession(t, e, again, s); back != hs {
		t.Fatalf("the host connected again to a new session %s", back.Info().ID)
	}
	if st := state(); st != availLive {
		t.Fatalf("connected again: %s", st)
	}
	hs.HostDisconnected(again)
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
	until(t, "the hub let the session go", func() bool { _, ok := e.srv.registry.Get(id); return !ok })
	if st := state(); st != availHostAway {
		t.Fatalf("let go: %s", st)
	}
}

// A hosted record takes nothing from this server's machine: its folder,
// on this machine a repository with a commit, gives it no repository and
// no commit; its agent, whose version this server's own probe holds, no
// version; it says where it ran only as its host said it (here nothing:
// not reported). A path its host reports is its host's: one that names,
// on this machine, a place this server refuses its own sessions is kept
// as its host gave it.
func TestAHostedRecordBorrowsNothingFromTheServer(t *testing.T) {
	probed := catalog.Agent{ID: "probed", Name: "probed", Command: []string{"/bin/cat"}, Adapter: "claude"}
	e := newTestEnvAgents(t, nil, nil, []catalog.Agent{probed})
	e.srv.probes.mu.Lock()
	e.srv.probes.run = func(context.Context, []string, agents.Probe, map[string]string) agents.ProbeResult {
		return agents.ProbeResult{Ran: true, Identified: true, Version: "9.9.9"}
	}
	e.srv.probes.mu.Unlock()
	a, _ := e.srv.Catalog().Get("probed")
	path, _, _ := e.srv.lookups.found(t.Context(), "/bin/cat")
	e.srv.identityOf(t.Context(), a, path, true)
	if id, ok := e.srv.cachedIdentity(a); !ok || id.Version != "9.9.9" {
		t.Fatalf("this server's probe holds no version of the agent: %+v %v", id, ok)
	}
	repo := filepath.Join(e.root, "repo")
	gitInit(t, repo, true)
	denied := filepath.Join(e.srv.cfg.DataDir, "notes.txt")
	if !session.DeniedBy(denied, e.srv.fileDeny()) {
		t.Fatalf("%s is not refused to this server's own sessions", denied)
	}
	hs := registerSession(t, e, signal.NewHostConn(), proto.HostSession{AgentID: "probed", Command: []string{"/bin/cat"}, Cwd: repo, Cols: 80, Rows: 24, LocalID: "1"})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hs.HostActivity(proto.Activity{Type: "file", Op: "edit", Path: denied, At: now}, "")
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	r := endedRecord(t, e, hs.Info().ID)
	if r.Where != "" || r.Agent.Version != "" || r.Repo != "" || r.RepoCommon != "" || r.Start != "" || r.StartFrom != "" || r.End != "" {
		t.Fatalf("the hosted record took this machine's facts: %+v", r)
	}
	if len(r.Files) != 1 || r.Files[0].Path != denied {
		t.Fatalf("the host's path: %+v", r.Files)
	}
}

// A Brakeman turn's history read draws no row of a hosted session's
// record, whatever its grant: the default scope (every session), one
// naming the hosted session, one of a repository. A server session's
// record beside it is served as before; the hosted one is in no answer,
// no read the turn's log keeps and no row the read draws (none at all
// under the scope that names it), and so it stays with hosted rows of the
// same words planted in the index: each read serves the same and draws
// none of them. The admin's read of the route holds the hosted record.
func TestABrakemanTurnReadsNoHostedRecord(t *testing.T) {
	b := brakemanEnv(t, nil)
	dir := filepath.Join(b.root, "inside")
	gitInit(t, dir, true)
	resp, out := b.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir, "name": "turnstile server"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	server := out["id"].(string)
	b.stopAndWait(server)
	rec := endedRecord(t, b.testEnv, server)
	hs := registerSession(t, b.testEnv, signal.NewHostConn(), proto.HostSession{Name: "turnstile hosted", AgentID: "sh", Command: []string{"sh"}, Cwd: dir, Cols: 80, Rows: 24, LocalID: "1",
		HostReport: proto.HostReport{Where: "native", Repo: rec.Repo, RepoCommon: rec.RepoCommon}})
	hosted := hs.Info().ID
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	endedRecord(t, b.testEnv, hosted)
	histSettle(t, b.testEnv)
	if keys := hsKeys(hsSearch(t, b.testEnv, "turnstile")); !slices.Contains(keys, "session:"+server) || !slices.Contains(keys, "session:"+hosted) {
		t.Fatalf("the admin's search: %v", keys)
	}
	f := &obsFixture{bkEnv: b}
	cases := []struct {
		name  string
		scope map[string]any
		want  []string
	}{
		{"every session", map[string]any{}, []string{"session:" + server}},
		{"the hosted session named", map[string]any{"sessions": []string{hosted}}, []string{}},
		{"its repository", map[string]any{"repos": []string{rec.Repo}}, []string{"session:" + server}},
	}
	turns := 0
	drawn := func(phase string) (out []int) {
		for _, c := range cases {
			mod := func(cfg map[string]any) {
				cfg["scope"] = c.scope
				cfg["visibility"] = map[string]any{"runs": true, "logs": true, "dispatch": true, "sessions": true, "touched": true}
			}
			if turns++; turns == 1 {
				f.turn(t, mod)
			} else {
				f.again(t, mod)
			}
			var keys []string
			ix := f.srv.history.index()
			ix.SetDrawSeam(func(_ int64, key string) { keys = append(keys, key) })
			got := f.expectRead(t, searchPath("turnstile"), http.StatusOK, "")
			ix.SetDrawSeam(nil)
			if refs := historyRefs(got); !slices.Equal(refs, c.want) || got["cut"] != nil {
				t.Errorf("%s, %s: the turn's history %v (cut %v), want %v", phase, c.name, refs, got["cut"], c.want)
			}
			if _, ok := f.readOf(t, "session:"+hosted); ok {
				t.Errorf("%s, %s: the hosted record is in the turn's read log", phase, c.name)
			}
			if i := slices.IndexFunc(keys, func(k string) bool { return strings.HasPrefix(k, "session:"+hosted) }); i >= 0 {
				t.Errorf("%s, %s: the read drew the hosted row %s, of %d drawn", phase, c.name, keys[i], len(keys))
			}
			out = append(out, len(keys))
		}
		return out
	}
	if before := drawn("its record alone"); before[1] != 0 {
		t.Errorf("the scope naming the hosted session drew %d rows", before[1])
	}
	var rows []history.Entry
	for i := range maxHistoryTurnExamine + 1 {
		rows = append(rows, history.Entry{Key: fmt.Sprintf("session:%s#planted/%05d", hosted, i), Kind: "session.typed", Ref: "session:" + hosted, Anchor: "session:" + hosted,
			Tree: rec.Repo, Repo: rec.Repo, Hosted: true, At: time.Now().Add(time.Hour + time.Duration(i)*time.Millisecond), Title: "turnstile typed", Body: "turnstile typed"})
	}
	plantRows(t, f, "hosted", rows)
	if after := drawn("with newer hosted rows"); after[1] != 0 {
		t.Errorf("the scope naming the hosted session drew %d rows", after[1])
	}
	resp, body := f.raw(searchPath("turnstile"), adminToken, nil)
	var admin map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &admin) != nil || !slices.Contains(historyRefs(admin), "session:"+hosted) {
		t.Errorf("the admin's read of the route: %d %s", resp.StatusCode, body)
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
