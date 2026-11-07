package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Resume from a record (docs/plans/history.md, PR-5).

// resumeAgents are agents with a session recipe that names the session at
// launch and resumes it, and a yolo recipe: talker, and strict, which
// resumes only in the folder it ran in. Each waits on its input.
func resumeAgents() []catalog.Agent {
	recipe := func(needsCwd bool) *catalog.SessionRecipe {
		return &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, IDFrom: "hook", ResumeArgs: []string{"--resume", "{id}"},
			IDPattern: `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, ResumeNeedsCwd: needsCwd}
	}
	cmd := []string{"/bin/sh", "-c", "exec /bin/cat", "sh"}
	return []catalog.Agent{
		{ID: "talker", Name: "Talker", Command: cmd, AllowArgs: true, Session: recipe(false), Yolo: &catalog.Yolo{Args: []string{"--yolo"}}},
		{ID: "strict", Name: "Strict", Command: cmd, AllowArgs: true, Session: recipe(true)},
	}
}

// resumeEnv is a server with the resume agents, made with opts.
func resumeEnv(t *testing.T, opts ...Option) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), resumeAgents(), opts...)
}

// restartWith shuts e's server down and starts another over its store,
// made with opts (restartServer's, with a Runtime or a seam).
func restartWith(t *testing.T, e *testEnv, opts ...Option) *testEnv {
	t.Helper()
	shutdown(e)
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return e.serve(srv)
}

// talk gives the session id's agent a turn: its hook reports the
// conversation it named at launch, which makes it resumable; it answers
// that conversation once the record holds it resumable.
func talk(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	as := e.local(id).Info().AgentSession
	if as == nil {
		t.Fatalf("session %s names no conversation", id)
	}
	if resp, out := e.do("POST", "/api/sessions/"+id+"/attention", e.agentToken(id), map[string]any{"state": "done", "agentSession": as.ID, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the turn's report: %d %v", resp.StatusCode, out)
	}
	until(t, "the record's conversation resumable", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && r.Agent.Session != nil && r.Agent.Session.ID == as.ID && r.Agent.Session.Resumable
	})
	return as.ID
}

// talked launches agent with body's fields, gives it a turn and ends it,
// its end in its record: the session's id and its conversation.
func talked(t *testing.T, e *testEnv, body map[string]any) (string, string) {
	t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	conv := talk(t, e, id)
	e.stopAndWait(id)
	endedRecord(t, e, id)
	return id, conv
}

// resumeRecord sends the record route for id with body.
func resumeRecord(t *testing.T, e *testEnv, id string, body map[string]any) (int, map[string]any) {
	t.Helper()
	resp, out := e.do("POST", "/api/history/sessions/"+id+"/resume", adminToken, body)
	if resp.StatusCode == http.StatusCreated {
		next := out["session"].(map[string]any)["id"].(string)
		t.Cleanup(func() {
			if d, ok := e.srv.registry.Get(next); ok {
				_ = d.Stop(context.Background())
			}
		})
	}
	return resp.StatusCode, out
}

// resumeReceipt is the receipt of cmd in the record of id, as the journal
// holds it.
func resumeReceipt(t *testing.T, e *testEnv, id, cmd string) journal.Resume {
	t.Helper()
	r, _, err := e.srv.sessionRecords.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range r.Resumes {
		if x.CommandID == cmd {
			return x
		}
	}
	t.Fatalf("no receipt of %s in %+v", cmd, r.Resumes)
	return journal.Resume{}
}

// sessionCount is how many sessions the registry lists.
func sessionCount(e *testEnv) int { return len(e.srv.registry.List()) }

// writeRecordFile writes r as a record file of e's data directory, as a
// server writes one: the next start's journal reads it.
func writeRecordFile(t *testing.T, e *testEnv, r journal.Record) {
	t.Helper()
	st, err := store.Open(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir))
	if err != nil {
		t.Fatal(err)
	}
	r.V = journal.V
	if r.Entries == nil {
		r.Entries = []journal.Entry{}
	}
	if err := st.Save(r.ID+".json", &r); err != nil {
		t.Fatal(err)
	}
}

// After a restart a record's session is resumed from the record alone:
// the new session's arguments are the recipe's resume arguments with the
// recorded conversation, then the launch's own, its folder and its yolo
// choice the record's; the receipt is started, naming it, and the new
// session's record says what it resumed from.
func TestResumeFromARecordAfterARestart(t *testing.T) {
	e := resumeEnv(t)
	dir := filepath.Join(e.root, "work")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id, conv := talked(t, e, map[string]any{"agentId": "talker", "cwd": dir, "args": []string{"--verbose"}, "yolo": true, "name": "the talk"})
	e2 := restartServer(t, e)
	if _, ok := e2.srv.registry.Get(id); ok {
		t.Fatal("the session is listed after the restart")
	}
	status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c1"})
	if status != http.StatusCreated || out["resumed"] != true || out["notice"] != nil {
		t.Fatalf("resume: %d %v", status, out)
	}
	next := out["session"].(map[string]any)
	cmd := commandOf(next)
	if !slices.Equal(cmd[4:7], []string{"--resume", conv, "--verbose"}) || !slices.Contains(cmd, "--yolo") || next["yolo"] != true ||
		next["cwd"] != dir || next["resumedFrom"] != id || next["name"] != "the talk" {
		t.Fatalf("the resumed session: %q %v", cmd, next)
	}
	receipt := out["receipt"].(map[string]any)
	if receipt["state"] != journal.ResumeStarted || receipt["session"] != next["id"] || receipt["commandId"] != "c1" || receipt["actor"] != adminActor {
		t.Fatalf("the receipt: %v", receipt)
	}
	if r, _ := recordFile(t, e2, id); len(r.Resumes) != 1 || r.Resumes[0].State != journal.ResumeStarted || r.Resumes[0].Session != next["id"] {
		t.Fatalf("the record's receipts: %+v", r.Resumes)
	}
	if r, _ := recordFile(t, e2, next["id"].(string)); r.ResumedFrom != id || r.Agent.Session == nil || r.Agent.Session.ID != conv {
		t.Fatalf("the new session's record: %+v", r)
	}
}

// The same command id answers its receipt and starts nothing: sent again
// while the session it started runs, and again once it has ended, for a
// session the registry still lists and for a record after a restart.
func TestTheSameCommandIDAnswersItsReceipt(t *testing.T) {
	e := resumeEnv(t)
	check := func(e *testEnv, id, cmd string) {
		t.Helper()
		before := sessionCount(e)
		status, out := resumeRecord(t, e, id, map[string]any{"commandId": cmd})
		if status != http.StatusCreated {
			t.Fatalf("resume: %d %v", status, out)
		}
		first := out["receipt"].(map[string]any)
		next := out["session"].(map[string]any)["id"].(string)
		status, again := resumeRecord(t, e, id, map[string]any{"commandId": cmd})
		if status != http.StatusOK || !jsonEqual(again["receipt"], first) || again["session"].(map[string]any)["id"] != next {
			t.Fatalf("again while it runs: %d %v, first %v", status, again, first)
		}
		e.stopAndWait(next)
		status, again = resumeRecord(t, e, id, map[string]any{"commandId": cmd})
		if status != http.StatusOK || !jsonEqual(again["receipt"], first) || again["session"].(map[string]any)["status"] == "running" {
			t.Fatalf("again once it ended: %d %v", status, again)
		}
		if n := sessionCount(e); n != before+1 {
			t.Fatalf("%d sessions made, want 1", n-before)
		}
	}
	listed, _ := talked(t, e, map[string]any{"agentId": "talker"})
	check(e, listed, "listed-1")
	e2 := restartServer(t, e)
	check(e2, listed, "record-1")
	// The listed one's receipt still answers after the restart, and names
	// a session the registry no longer lists.
	status, out := resumeRecord(t, e2, listed, map[string]any{"commandId": "listed-1"})
	if status != http.StatusOK || out["session"] != nil || out["receipt"].(map[string]any)["state"] != journal.ResumeStarted {
		t.Fatalf("the listed one's receipt after the restart: %d %v", status, out)
	}
}

// jsonEqual compares two values as JSON decodes them.
func jsonEqual(a, b any) bool {
	var x, y any
	for _, c := range []struct {
		v   any
		out *any
	}{{a, &x}, {b, &y}} {
		raw, _ := json.Marshal(c.v)
		_ = json.Unmarshal(raw, c.out)
	}
	return reflect.DeepEqual(x, y)
}

// A record whose session the resume routes' rules or the record's own
// refuse is refused in their words, nothing launched, its receipt settled
// failed with the refusal's code: a hosted session, a managed attempt, a
// Brakeman turn, an agent with no recipe, a conversation not resumable,
// one a live session holds, a folder gone that the agent needs, arguments
// not kept, a Runtime this server lacks, variables without withoutEnv, a
// lost record without confirmLost, and a legacy run's member, whose run's
// member state is unchanged.
func TestResumeFromARecordRefuses(t *testing.T) {
	e := resumeEnv(t)
	conv := "0a1b2c3d-0000-4000-8000-0000000000"
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	rec := func(n int, edit func(r *journal.Record)) string {
		id := "00000000000000" + string("0123456789abcdef"[n/16]) + string("0123456789abcdef"[n%16])
		r := journal.Record{ID: id, Name: "crafted", Kind: "server", Where: "native", Cwd: e.root, StartedAt: at, EndedAt: at.Add(time.Minute), Ending: "stopped",
			Agent:  journal.Agent{ID: "talker", Name: "Talker", Session: &journal.AgentSession{ID: conv + string("0123456789abcdef"[n/16]) + string("0123456789abcdef"[n%16]), Source: "set", Resumable: true}},
			Launch: &journal.Launch{ArgsKept: true}}
		edit(&r)
		writeRecordFile(t, e, r)
		return id
	}
	cases := []struct {
		name, code string
		status     int
		id         string
		body       map[string]any
	}{
		{"hosted", "hosted_session", http.StatusBadRequest, rec(1, func(r *journal.Record) { r.Host = journal.Host{Name: "lab-box", Hosted: true} }), nil},
		{"managed", "managed_run", http.StatusConflict, rec(2, func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000027", Task: "build", Attempt: 1} }), nil},
		{"Brakeman", "brakeman_run", http.StatusConflict, rec(3, func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "brakeman-00000002", Brakeman: "br-2"} }), nil},
		{"no recipe", "not_resumable", http.StatusConflict, rec(4, func(r *journal.Record) { r.Agent.ID, r.Agent.Name = "cat", "cat" }), nil},
		{"not resumable", "not_resumable", http.StatusConflict, rec(5, func(r *journal.Record) { r.Agent.Session.Resumable = false }), nil},
		{"folder gone", "invalid_cwd", http.StatusBadRequest, rec(6, func(r *journal.Record) { r.Agent.ID, r.Cwd = "strict", filepath.Join(e.root, "gone") }), nil},
		{"arguments not kept", "args_not_kept", http.StatusConflict, rec(7, func(r *journal.Record) {
			r.Launch = &journal.Launch{ArgCount: 64, ArgsSHA256: strings.Repeat("a", 64)}
		}), nil},
		{"a Runtime gone", "runtime_gone", http.StatusConflict, rec(8, func(r *journal.Record) { r.Where = "boxcar" }), nil},
		{"variables", "env_not_kept", http.StatusConflict, rec(9, func(r *journal.Record) {
			r.Launch.EnvNames, r.Launch.EnvCount, r.Launch.EnvSHA256 = []string{"GOAL"}, 1, strings.Repeat("b", 64)
		}), nil},
		{"lost", "lost_unconfirmed", http.StatusConflict, rec(10, func(r *journal.Record) { r.EndedAt, r.LostAt, r.Ending = time.Time{}, at, journal.EndingLost }), nil},
		{"a legacy run's member", "crew_member", http.StatusConflict, rec(11, func(r *journal.Record) { r.Crew = &journal.Crew{RunID: "hist-00000028", Member: "lead"} }), nil},
	}
	held := rec(12, func(r *journal.Record) { r.Agent.Session.ID = conv + "09" })
	e2 := restartServer(t, e)
	for i, c := range cases {
		cmd := "refused-" + string(rune('a'+i))
		before := sessionCount(e2)
		status, out := resumeRecord(t, e2, c.id, map[string]any{"commandId": cmd})
		if status != c.status || errorCode(out) != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, status, out, c.status, c.code)
			continue
		}
		receipt := resumeReceipt(t, e2, c.id, cmd)
		if receipt.State != journal.ResumeFailed || receipt.Code != c.code || receipt.Error == "" || !jsonEqual(out["receipt"], receipt) {
			t.Errorf("%s: the receipt %+v, answered %v", c.name, receipt, out["receipt"])
		}
		if sessionCount(e2) != before {
			t.Errorf("%s: a session was launched", c.name)
		}
	}
	if _, out := resumeRecord(t, e2, cases[8].id, map[string]any{"commandId": "env-again"}); !strings.Contains(out["error"].(map[string]any)["message"].(string), "GOAL") {
		t.Fatalf("the variables' refusal names none: %v", out)
	}
	// With the person's yes, without its variables: launched, and said so.
	status, out := resumeRecord(t, e2, cases[8].id, map[string]any{"commandId": "env-yes", "withoutEnv": true})
	if status != http.StatusCreated || !strings.Contains(out["notice"].(string), "GOAL") {
		t.Fatalf("withoutEnv: %d %v", status, out)
	}
	// Its conversation is held by the session that runs now.
	status, out = resumeRecord(t, e2, held, map[string]any{"commandId": "held"})
	if status != http.StatusConflict || errorCode(out) != "already_resumed" || resumeReceipt(t, e2, held, "held").State != journal.ResumeFailed {
		t.Fatalf("held by a live session: %d %v", status, out)
	}

	// A run's member, its record the journal's own: refused, its run's
	// member route resumes it, and the run's member state is unchanged.
	runID := e2.launchCrew(t, "Talk", map[string]any{"name": "a", "agentId": "talker", "prompt": "", "start": map[string]any{"when": "immediately"}})
	sid := e2.waitRunning(t, runID, "a")
	talk(t, e2, sid)
	e2.stopAndWait(sid)
	endedRecord(t, e2, sid)
	_, run := e2.do("GET", "/api/runs/"+runID, adminToken, nil)
	member := runMember(t, run["run"].(map[string]any), "a")
	before := sessionCount(e2)
	status, out = resumeRecord(t, e2, sid, map[string]any{"commandId": "member"})
	if status != http.StatusConflict || errorCode(out) != "crew_member" || sessionCount(e2) != before {
		t.Fatalf("a run's member: %d %v", status, out)
	}
	_, run = e2.do("GET", "/api/runs/"+runID, adminToken, nil)
	if now := runMember(t, run["run"].(map[string]any), "a"); !jsonEqual(now, member) {
		t.Fatalf("the member's state changed: %v, was %v", now, member)
	}
}

// A refusal whose words pass the receipt's bound (a launch with many long
// variables) is answered the first time as the record keeps it: the same
// command id sent again, after a restart too, answers the same error and
// the same receipt, and the words' cut still leaves the way on
// (withoutEnv) in them.
func TestALongRefusalIsAnsweredAsItsReceiptKeepsIt(t *testing.T) {
	e := resumeEnv(t)
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	var names []string
	for i := range 12 {
		names = append(names, fmt.Sprintf("SOME_LONGISH_VARIABLE_%c_%s", 'A'+i, strings.Repeat("N", 24)))
	}
	id := "0000000000000a01"
	writeRecordFile(t, e, journal.Record{ID: id, Name: "many variables", Kind: "server", Where: "native", Cwd: e.root, StartedAt: at, EndedAt: at.Add(time.Minute), Ending: "stopped",
		Agent:  journal.Agent{ID: "talker", Name: "Talker", Session: &journal.AgentSession{ID: "0a1b2c3d-0000-4000-8000-000000000a01", Source: "set", Resumable: true}},
		Launch: &journal.Launch{ArgsKept: true, EnvNames: names, EnvCount: len(names), EnvSHA256: strings.Repeat("b", 64)}})
	e2 := restartServer(t, e)
	status, first := resumeRecord(t, e2, id, map[string]any{"commandId": "long-1"})
	words := first["error"].(map[string]any)["message"].(string)
	full := "its launch had variables, whose values no record keeps; resume without them (withoutEnv): " + strings.Join(names, ", ")
	if status != http.StatusConflict || errorCode(first) != "env_not_kept" || len(full) <= 200 || len(words) > 200 || !strings.HasPrefix(full, words) ||
		!strings.Contains(words, "(withoutEnv)") || first["receipt"].(map[string]any)["error"] != words {
		t.Fatalf("the first reply: %d %v", status, first)
	}
	if receipt := resumeReceipt(t, e2, id, "long-1"); !jsonEqual(first["receipt"], receipt) {
		t.Fatalf("answered %v, the record keeps %+v", first["receipt"], receipt)
	}
	again := func(e *testEnv, when string) {
		t.Helper()
		status, again := resumeRecord(t, e, id, map[string]any{"commandId": "long-1"})
		if status != http.StatusConflict || !jsonEqual(again, first) {
			t.Fatalf("the same id again %s: %d %v, first %v", when, status, again, first)
		}
	}
	again(e2, "at once")
	again(restartServer(t, e2), "after a restart")
}

// outliving is a Runtime whose terminals outlive a stop: Stop fails and
// the process runs on, as a guest's terminal may outlive the server.
type outliving struct {
	ext.Runtime
	procs chan *sessiontest.FakeProc
}

type outlivingTerminal struct{ ext.Terminal }

func (outlivingTerminal) Stop(context.Context, time.Duration) error {
	return io.ErrClosedPipe
}

func (o *outliving) StartTerminal(context.Context, ext.TerminalSpec) (ext.Terminal, error) {
	p := sessiontest.NewFakeProc()
	o.procs <- p
	return outlivingTerminal{sessiontest.FakeTerminal(p)}, nil
}

// A Runtime's terminal that keeps running when it is stopped and when the
// server stops: while it is listed, its stop failed and its status
// stopped, Resume is refused still_running and not offered, nothing
// launched; the server's stop leaves its record lost, with no end: after
// the restart its availability says its process's state is not known,
// Resume is refused lost_unconfirmed, and with the person's second yes
// (confirmLost) it launches, there.
func TestARuntimeTerminalThatOutlivesTheServer(t *testing.T) {
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 4)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	rt := &sessiontest.FakeRuntime{Place: "boxcar", Base: terms}
	e := resumeEnv(t, withRuntime(rt))
	l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "talker", where: "boxcar"}, nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	id := l.Info().ID
	conv := talk(t, e, id)
	if err := l.Stop(context.Background()); err == nil || !l.Info().Status.Ended() {
		t.Fatalf("the stop: %v, the status %s", err, l.Info().Status)
	}
	select {
	case <-l.ProcessDone():
		t.Fatal("the process ended")
	default:
	}
	if offer := e.srv.resumeOfRecord(id); offer.Offered != answerNo || !strings.Contains(offer.Why, "process is still running") {
		t.Fatalf("the offer after a failed stop: %+v", offer)
	}
	starts := len(rt.Asked("StartTerminal"))
	status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c0"})
	if status != http.StatusConflict || errorCode(out) != "still_running" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "stop it again") ||
		len(rt.Asked("StartTerminal")) != starts || resumeReceipt(t, e, id, "c0").State != journal.ResumeFailed {
		t.Fatalf("after a failed stop: %d %v", status, out)
	}
	e2 := restartWith(t, e, withRuntime(rt))
	resp, got := e2.do("GET", "/api/history/sessions/"+id, adminToken, nil)
	record := got["record"].(map[string]any)
	if resp.StatusCode != http.StatusOK || record["endedAt"] != nil || record["lostAt"] == nil || record["ending"] != journal.EndingLost || record["where"] != "boxcar" {
		t.Fatalf("the record after the restart: %d %v", resp.StatusCode, got)
	}
	if offer := e2.srv.resumeOfRecord(id); offer.Offered != answerYes || !offer.Confirm || !strings.Contains(offer.Why, "its process may still be running") {
		t.Fatalf("the offer: %+v", offer)
	}
	starts = len(rt.Asked("StartTerminal"))
	status, out = resumeRecord(t, e2, id, map[string]any{"commandId": "c1"})
	if status != http.StatusConflict || errorCode(out) != "lost_unconfirmed" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "on boxcar") ||
		len(rt.Asked("StartTerminal")) != starts {
		t.Fatalf("without confirmLost: %d %v", status, out)
	}
	status, out = resumeRecord(t, e2, id, map[string]any{"commandId": "c2", "confirmLost": true})
	if status != http.StatusCreated {
		t.Fatalf("with confirmLost: %d %v", status, out)
	}
	calls := rt.Asked("StartTerminal")
	spec := calls[len(calls)-1].Spec.(ext.TerminalSpec)
	if len(calls) != starts+1 || !slices.Contains(spec.Argv, conv) || spec.Ref.Session != out["session"].(map[string]any)["id"] {
		t.Fatalf("the start: %d calls, %+v", len(calls), spec)
	}
}

// outlivingEnv is a resume server with the Runtime boxcar, whose
// terminals outlive a stop (outliving); every process left is ended at
// the test's end.
func outlivingEnv(t *testing.T) (*testEnv, *outliving, *sessiontest.FakeRuntime) {
	t.Helper()
	terms := &outliving{procs: make(chan *sessiontest.FakeProc, 8)}
	t.Cleanup(func() {
		for {
			select {
			case p := <-terms.procs:
				p.End(0)
			default:
				return
			}
		}
	})
	rt := &sessiontest.FakeRuntime{Place: "boxcar", Base: terms}
	return resumeEnv(t, withRuntime(rt)), terms, rt
}

// heldResume sends the record route for id with body while its launch is
// held at the stage reserved, runs between there, and answers the reply.
func heldResume(t *testing.T, e *testEnv, id string, body map[string]any, between func()) (int, map[string]any) {
	t.Helper()
	reached, release := holdResume(e, "reserved", id)
	t.Cleanup(release)
	type reply struct {
		status int
		out    map[string]any
	}
	done := make(chan reply, 1)
	go func() {
		resp, out := e.do("POST", "/api/history/sessions/"+id+"/resume", adminToken, body)
		done <- reply{resp.StatusCode, out}
	}()
	<-reached
	between()
	release()
	r := <-done
	return r.status, r.out
}

// A resume launches only on what it reads as it launches, never on the
// reservation's snapshot. A ended, resumed as B, which then names a
// conversation of its own: B is of A's conversation by its chain alone.
// Held after its reservation, a second resume of A meets B's stop, which
// fails with B's process running: refused still_running, naming B,
// nothing launched, its receipt failed, and not offered. Once B's
// process has ended, A resumes.
func TestASuccessorWhoseStopFailedHoldsItsPredecessorsConversation(t *testing.T) {
	e, terms, rt := outlivingEnv(t)
	a, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "talker", where: "boxcar"}, nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	pa := <-terms.procs
	idA := a.Info().ID
	talk(t, e, idA)
	pa.End(0)
	<-a.ProcessDone()
	endedRecord(t, e, idA)
	status, out := resumeRecord(t, e, idA, map[string]any{"commandId": "c1"})
	if status != http.StatusCreated {
		t.Fatalf("the first resume: %d %v", status, out)
	}
	idB := out["session"].(map[string]any)["id"].(string)
	pb := <-terms.procs
	b := e.local(idB)
	other := newAgentSessionID("")
	if resp, out := e.do("POST", "/api/sessions/"+idB+"/attention", e.agentToken(idB), map[string]any{"state": "done", "agentSession": other, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("B's turn: %d %v", resp.StatusCode, out)
	}
	until(t, "B's own conversation", func() bool {
		as := b.Info().AgentSession
		return as != nil && as.ID == other
	})
	starts := 0
	status, out = heldResume(t, e, idA, map[string]any{"commandId": "c2"}, func() {
		if err := b.Stop(context.Background()); err == nil || !b.Info().Status.Ended() {
			t.Errorf("B's stop: %v, its status %s", err, b.Info().Status)
		}
		starts = len(rt.Asked("StartTerminal"))
	})
	if status != http.StatusConflict || errorCode(out) != "still_running" || !strings.Contains(out["error"].(map[string]any)["message"].(string), idB) ||
		len(rt.Asked("StartTerminal")) != starts || resumeReceipt(t, e, idA, "c2").State != journal.ResumeFailed {
		t.Fatalf("A's resume with B's process running: %d %v", status, out)
	}
	if offer := e.srv.resumeOfRecord(idA); offer.Offered != answerNo || !strings.Contains(offer.Why, "stop it again") {
		t.Fatalf("the offer: %+v", offer)
	}
	pb.End(0)
	<-b.ProcessDone()
	if status, out := resumeRecord(t, e, idA, map[string]any{"commandId": "c3"}); status != http.StatusCreated {
		t.Fatalf("once B's process ended: %d %v", status, out)
	}
}

// A session whose stop failed, removed while a resume of it is held after
// its reservation (whose snapshot read its record open, neither ended nor
// lost): the launch reads the removed session's process, still running,
// and refuses still_running, nothing launched. Once that process has
// ended, the record the removal left lost needs the person's second yes
// (lost_unconfirmed), and launches with it.
func TestASourceRemovedAfterItsReservationIsReadAtTheLaunch(t *testing.T) {
	e, terms, rt := outlivingEnv(t)
	a, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "talker", where: "boxcar"}, nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	pa := <-terms.procs
	id := a.Info().ID
	talk(t, e, id)
	if err := a.Stop(context.Background()); err == nil || !a.Info().Status.Ended() {
		t.Fatalf("the stop: %v, the status %s", err, a.Info().Status)
	}
	if r, _, err := e.srv.sessionRecords.Get(id); err != nil || !r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the record before the resume: %+v %v", r, err)
	}
	starts := 0
	status, out := heldResume(t, e, id, map[string]any{"commandId": "c1"}, func() {
		if resp, out := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Errorf("the removal: %d %v", resp.StatusCode, out)
		}
		if _, ok := e.srv.registry.Get(id); ok {
			t.Error("still listed")
		}
		starts = len(rt.Asked("StartTerminal"))
	})
	if status != http.StatusConflict || errorCode(out) != "still_running" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "stop it again") ||
		len(rt.Asked("StartTerminal")) != starts || resumeReceipt(t, e, id, "c1").State != journal.ResumeFailed {
		t.Fatalf("removed with its process running: %d %v", status, out)
	}
	pa.End(0)
	<-a.ProcessDone()
	status, out = resumeRecord(t, e, id, map[string]any{"commandId": "c2"})
	if status != http.StatusConflict || errorCode(out) != "lost_unconfirmed" || len(rt.Asked("StartTerminal")) != starts {
		t.Fatalf("without confirmLost: %d %v", status, out)
	}
	if status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c3", "confirmLost": true}); status != http.StatusCreated {
		t.Fatalf("with confirmLost: %d %v", status, out)
	}
}

// longFolder makes, under root, a folder whose path's JSON form, escapes
// included, is journal.MaxCwd bytes exactly, of components of quotes (two
// bytes each), and in it the folder x: the session's folder, whose
// record's cut is its parent. Made a component at a time through os.Root,
// so no single call takes a path past a system's own bound.
func longFolder(t *testing.T, root string) (parent, child string) {
	t.Helper()
	size := len(root) + strings.Count(root, `"`) + strings.Count(root, `\`)
	var comps []string
	for left := journal.MaxCwd - size; left > 0; {
		n := min(left, 201)
		if left-n == 1 {
			n -= 2
		}
		comps = append(comps, strings.Repeat(`"`, (n-1)/2)+strings.Repeat("a", (n-1)%2))
		left -= n
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range append(comps, "x") {
		if err := r.Mkdir(c, 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := r.OpenRoot(c)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r = next
	}
	r.Close()
	parent = root + "/" + strings.Join(comps, "/")
	return parent, parent + "/x"
}

// A record keeps a working directory past its bound cut, which can be the
// session's parent folder, there on disk: a resume from that record is
// refused cwd_not_kept and not offered, nothing launched, even for an
// agent that resumes only where it ran, whose folder check the parent
// would pass.
func TestAFolderTheRecordCutIsNeverResumedIn(t *testing.T) {
	e := resumeEnv(t)
	parent, child := longFolder(t, e.root)
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	id := "0000000000000c01"
	in, err := e.srv.sessionRecords.Begin(journal.Record{ID: id, Name: "long folder", Kind: "server", Where: "native", Cwd: child, StartedAt: at,
		Agent: journal.Agent{ID: "strict", Name: "Strict"}, Launch: &journal.Launch{ArgsKept: true}})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.sessionRecords.Settle(in, journal.State{Name: "long folder", Final: true, End: &journal.Ending{At: at.Add(time.Minute), How: "stopped"},
		AgentSession: &journal.AgentSession{ID: "0a1b2c3d-0000-4000-8000-000000000c01", Source: "set", Resumable: true}})
	e.srv.sessionRecords.Flush()
	if r, _ := recordFile(t, e, id); r.Cwd != parent || r.EndedAt.IsZero() || !journal.CwdMayBeCut(r.Cwd) {
		t.Fatalf("the record keeps %d bytes of the folder, ended %v", len(r.Cwd), r.EndedAt)
	}
	if offer := e.srv.resumeOfRecord(id); offer.Offered != answerNo || !strings.Contains(offer.Why, "too long for its record to keep whole") {
		t.Fatalf("the offer: %+v", offer)
	}
	before := sessionCount(e)
	status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c1"})
	if status != http.StatusConflict || errorCode(out) != "cwd_not_kept" || sessionCount(e) != before || resumeReceipt(t, e, id, "c1").Code != "cwd_not_kept" {
		t.Fatalf("resume: %d %v", status, out)
	}
}

// Sixteen receipts, refused ones among them, fill a record: a
// seventeenth command id is refused resume_limit with nothing reserved or
// launched; after a restart the oldest id answers its first receipt and
// starts nothing.
func TestTheSeventeenthResumeIsRefusedAndTheOldestStillAnswers(t *testing.T) {
	e := resumeEnv(t)
	id, _ := talked(t, e, map[string]any{"agentId": "talker"})
	status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c0"})
	if status != http.StatusCreated {
		t.Fatalf("the first: %d %v", status, out)
	}
	first := out["receipt"].(map[string]any)
	for i := 1; i < journal.MaxResumes; i++ {
		if status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c" + strings.Repeat("x", i)}); status != http.StatusConflict || errorCode(out) != "already_resumed" {
			t.Fatalf("receipt %d: %d %v", i, status, out)
		}
	}
	before := sessionCount(e)
	status, out = resumeRecord(t, e, id, map[string]any{"commandId": "c-17"})
	if status != http.StatusConflict || errorCode(out) != "resume_limit" || out["receipt"] != nil || sessionCount(e) != before {
		t.Fatalf("the seventeenth: %d %v", status, out)
	}
	if r, _ := recordFile(t, e, id); len(r.Resumes) != journal.MaxResumes {
		t.Fatalf("%d receipts", len(r.Resumes))
	}
	e2 := restartServer(t, e)
	status, out = resumeRecord(t, e2, id, map[string]any{"commandId": "c0"})
	if status != http.StatusOK || !jsonEqual(out["receipt"], first) || sessionCount(e2) != 0 {
		t.Fatalf("the oldest after the restart: %d %v, %d sessions", status, out, sessionCount(e2))
	}
}

// holdResume holds a resume of the record id at stage until release,
// reached closed once it holds it.
func holdResume(e *testEnv, stage, id string) (reached <-chan struct{}, release func()) {
	in, gate := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	hook := func(s, rid string) bool {
		if s == stage && rid == id && once.CompareAndSwap(false, true) {
			close(in)
			<-gate
		}
		return false
	}
	e.srv.resumeHook.Store(&hook)
	var let atomic.Bool
	return in, func() {
		if let.CompareAndSwap(false, true) {
			close(gate)
		}
	}
}

// A record whose resume is being started is kept: the launch held after
// its receipt's reservation, Forget answers resume_pending and ForgetAll
// keeps and counts it; let go, the receipt settles started on its record,
// and Forget then removes it.
func TestForgetWaitsOutAPendingResume(t *testing.T) {
	e := resumeEnv(t)
	id, _ := talked(t, e, map[string]any{"agentId": "talker"})
	reached, release := holdResume(e, "reserved", id)
	t.Cleanup(release)
	type reply struct {
		status int
		out    map[string]any
	}
	done := make(chan reply, 1)
	go func() {
		resp, out := e.do("POST", "/api/history/sessions/"+id+"/resume", adminToken, map[string]any{"commandId": "c1"})
		done <- reply{resp.StatusCode, out}
	}()
	<-reached
	if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusConflict || errorCode(out) != "resume_pending" {
		t.Fatalf("forget while pending: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("DELETE", "/api/history/sessions", adminToken, nil); resp.StatusCode != http.StatusOK || out["kept"] != float64(1) {
		t.Fatalf("forget all while pending: %d %v", resp.StatusCode, out)
	}
	if _, _, err := e.srv.sessionRecords.Get(id); err != nil {
		t.Fatalf("the record after forget all: %v", err)
	}
	release()
	r := <-done
	if r.status != http.StatusCreated {
		t.Fatalf("the resume: %d %v", r.status, r.out)
	}
	next := r.out["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() { e.stopAndWait(next) })
	if got := resumeReceipt(t, e, id, "c1"); got.State != journal.ResumeStarted || got.Session != next {
		t.Fatalf("the receipt: %+v", got)
	}
	if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget once settled: %d %v", resp.StatusCode, out)
	}
}

// stopResume has a resume of the record id stop at stage, as a server
// stopped there would: its receipt left as the stop leaves it.
func stopResume(e *testEnv, stage, id string) {
	hook := func(s, rid string) bool { return s == stage && rid == id }
	e.srv.resumeHook.Store(&hook)
}

// A receipt a stop left pending is settled at the next start by what the
// data directory shows: stopped before the process was made, uncertain,
// nothing claimed, the same id answering it and a new one free to try;
// stopped after the new session's record was begun, started, naming it.
func TestAPendingReceiptAfterAStop(t *testing.T) {
	e := resumeEnv(t)
	id, _ := talked(t, e, map[string]any{"agentId": "talker"})
	stopResume(e, "reserved", id)
	if status, out := resumeRecord(t, e, id, map[string]any{"commandId": "c1"}); status != http.StatusServiceUnavailable {
		t.Fatalf("stopped: %d %v", status, out)
	}
	pending := resumeReceipt(t, e, id, "c1")
	if pending.State != journal.ResumePending || pending.Session == "" {
		t.Fatalf("the receipt before the restart: %+v", pending)
	}
	e2 := restartServer(t, e)
	got := resumeReceipt(t, e2, id, "c1")
	if got.State != journal.ResumeUncertain || got.Error == "" || got.Session != pending.Session {
		t.Fatalf("the receipt after the restart: %+v", got)
	}
	if _, _, err := e2.srv.sessionRecords.Get(pending.Session); err == nil {
		t.Fatal("a record of the session never made")
	}
	if status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c1"}); status != http.StatusConflict || errorCode(out) != "resume_uncertain" || sessionCount(e2) != 0 {
		t.Fatalf("the same id: %d %v", status, out)
	}
	status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c2"})
	if status != http.StatusCreated {
		t.Fatalf("a new id: %d %v", status, out)
	}
	e2.stopAndWait(out["session"].(map[string]any)["id"].(string))

	// Stopped once the session was made: its record is there, so started.
	stopResume(e2, "launched", id)
	if status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c3"}); status != http.StatusServiceUnavailable {
		t.Fatalf("stopped after the launch: %d %v", status, out)
	}
	pending = resumeReceipt(t, e2, id, "c3")
	e3 := restartServer(t, e2)
	if got := resumeReceipt(t, e3, id, "c3"); got.State != journal.ResumeStarted || got.Session != pending.Session {
		t.Fatalf("the receipt after the restart: %+v", got)
	}
	if r, _, err := e3.srv.sessionRecords.Get(pending.Session); err != nil || r.ResumedFrom != id {
		t.Fatalf("the started session's record: %+v %v", r, err)
	}
}

// The session route and the record route make one launch: a listed
// session resumed by its own route, by its record while listed, and by
// its record after a restart starts with the same arguments, folder, name
// and yolo choice.
func TestAListedSessionAndARecordShareOneLaunch(t *testing.T) {
	e := resumeEnv(t)
	dir := filepath.Join(e.root, "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id, _ := talked(t, e, map[string]any{"agentId": "talker", "cwd": dir, "args": []string{"-v"}, "yolo": true})
	same := func(e *testEnv, a, b map[string]any) {
		t.Helper()
		for _, k := range []string{"agentId", "name", "cwd", "yolo", "resumedFrom", "agentSession"} {
			if !jsonEqual(a[k], b[k]) {
				t.Fatalf("%s: %v and %v", k, a[k], b[k])
			}
		}
		if ca, cb := commandOf(a), commandOf(b); !slices.Equal(ca[:len(ca)-2], cb[:len(cb)-2]) {
			t.Fatalf("the commands: %q and %q", ca, cb)
		}
	}
	resp, out := e.do("POST", "/api/sessions/"+id+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated || out["resumed"] != true {
		t.Fatalf("the session route: %d %v", resp.StatusCode, out)
	}
	byRoute := out["session"].(map[string]any)
	e.stopAndWait(byRoute["id"].(string))
	status, out := resumeRecord(t, e, id, map[string]any{"commandId": "listed"})
	if status != http.StatusCreated {
		t.Fatalf("the record route, listed: %d %v", status, out)
	}
	byRecord := out["session"].(map[string]any)
	same(e, byRoute, byRecord)
	e.stopAndWait(byRecord["id"].(string))
	e2 := restartServer(t, e)
	status, out = resumeRecord(t, e2, id, map[string]any{"commandId": "record"})
	if status != http.StatusCreated {
		t.Fatalf("the record route after a restart: %d %v", status, out)
	}
	same(e2, byRoute, out["session"].(map[string]any))
}

// A record of a session that ran in another Runtime resumes there: its
// folder opened on this server and lent to the Runtime as any launch's is,
// and no git read of it here; the folder gone and the agent needing it,
// invalid_cwd before any start.
func TestANonNativeRecordResumesInItsRuntime(t *testing.T) {
	rt := &sessiontest.FakeRuntime{Place: "boxcar", Base: local.Native()}
	e := resumeEnv(t, withRuntime(rt))
	dir, gone := filepath.Join(e.root, "guest"), filepath.Join(e.root, "gone")
	for _, d := range []string{dir, gone} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	launch := func(agent, cwd string) string {
		l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: agent, Cwd: cwd, where: "boxcar"}, nil)
		if aerr != nil {
			t.Fatal(aerr)
		}
		id := l.Info().ID
		talk(t, e, id)
		e.stopAndWait(id)
		endedRecord(t, e, id)
		return id
	}
	id, strict := launch("talker", dir), launch("strict", gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	e2 := restartWith(t, e, withRuntime(rt))
	var reads atomic.Int32
	e2.srv.readRevision = func(ctx context.Context, d string) (gitcli.Revision, error) {
		reads.Add(1)
		return gitcli.RevisionOf(ctx, d)
	}
	starts := len(rt.Asked("StartTerminal"))
	status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c1"})
	if status != http.StatusCreated {
		t.Fatalf("resume: %d %v", status, out)
	}
	next := out["session"].(map[string]any)["id"].(string)
	calls := rt.Asked("StartTerminal")
	spec := calls[len(calls)-1].Spec.(ext.TerminalSpec)
	if len(calls) != starts+1 || spec.Dir != dir || spec.DirFile == nil || spec.Ref.Session != next {
		t.Fatalf("the start: %d calls, %+v", len(calls)-starts, spec)
	}
	if r, _ := recordFile(t, e2, next); r.Where != "boxcar" || r.Start != "" || reads.Load() != 0 {
		t.Fatalf("the new session's record %+v, %d git reads", r, reads.Load())
	}
	status, out = resumeRecord(t, e2, strict, map[string]any{"commandId": "c1"})
	if status != http.StatusBadRequest || errorCode(out) != "invalid_cwd" || len(rt.Asked("StartTerminal")) != starts+1 {
		t.Fatalf("the folder gone: %d %v", status, out)
	}
}

// A resume from a record starts through the server's Runtime, as every
// session does: the native Runtime's seam records the start.
func TestResumeFromARecordStartsThroughTheRuntime(t *testing.T) {
	seam := nativeSeam()
	e := resumeEnv(t, withNativeRuntime(seam))
	id, conv := talked(t, e, map[string]any{"agentId": "talker"})
	e2 := restartWith(t, e, withNativeRuntime(seam))
	starts := len(seam.Asked("StartTerminal"))
	status, out := resumeRecord(t, e2, id, map[string]any{"commandId": "c1"})
	if status != http.StatusCreated {
		t.Fatalf("resume: %d %v", status, out)
	}
	calls := seam.Asked("StartTerminal")
	spec := calls[len(calls)-1].Spec.(ext.TerminalSpec)
	if len(calls) != starts+1 || !slices.Contains(spec.Argv, conv) || spec.Ref.Session != out["session"].(map[string]any)["id"] {
		t.Fatalf("the start: %+v", spec)
	}
}

// The guide lists the history's operations to the admin, with their
// command lines and bounds, and to no session; a switchyard, which keeps
// no history, lists none.
func TestTheGuideListsHistoryForTheAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	_, _, out := guideOf(t, e, adminToken, "/api/guide?section=operations")
	names := opNames(t, out)
	for _, want := range []string{"history_search", "history_entry", "history_similar", "history_sessions", "history_session", "history_resume", "history_forget", "history_forget_all", "history_reindex"} {
		if !slices.Contains(names, want) {
			t.Errorf("the admin's guide lists no %s", want)
		}
	}
	for _, op := range Operations() {
		if strings.HasPrefix(op.Name, "history_") && (op.Scope != "admin" || op.MCP != "" || len(op.CLI) < 3 || op.CLI[1] != "history") {
			t.Errorf("%s: %+v", op.Name, op)
		}
		if op.Name == "history_resume" && (op.Bounds["body"] != maxHistoryResumeBody || op.Bounds["receipts"] != journal.MaxResumes) {
			t.Errorf("history_resume's bounds: %v", op.Bounds)
		}
	}
	sid := e.createSession("cat")
	_, _, out = guideOf(t, e, e.agentToken(sid), "/api/sessions/"+sid+"/guide?section=operations")
	for _, n := range opNames(t, out) {
		if strings.HasPrefix(n, "history_") {
			t.Errorf("a session's guide lists %s", n)
		}
	}
	sy := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	_, _, out = guideOf(t, sy, adminToken, "/api/guide?section=operations")
	for _, n := range opNames(t, out) {
		if strings.HasPrefix(n, "history_") {
			t.Errorf("a switchyard's guide lists %s", n)
		}
	}
}
