package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// holdsNoBinding fails when doc, a record or a crew as kept, holds any
// field a binding is written as.
func holdsNoBinding(t *testing.T, what, doc string) {
	t.Helper()
	for _, k := range []string{`"account"`, `"bound"`, `"model"`, `"login"`} {
		if strings.Contains(doc, k) {
			t.Errorf("%s holds %s: %.600s", what, k, doc)
		}
	}
}

// A launch that takes no registered account and no model, on an adapter
// with no default, writes what a server from before bindings wrote: the
// session's record at that version with nothing of a login in its launch,
// the run's record and the ledger's crew with no member's account, model
// or binding, read on their bytes. After a restart each is made again as
// before: the session resumed from its record, the legacy run from its
// record, the managed run continued, each on the tool's own login.
func TestALaunchNamingNothingWritesWhatMainWrote(t *testing.T) {
	e := newBindEnv(t, nil)
	e.gitRepo(t)
	sid := e.launch(t, "claude-p", nil)
	e.probe(t, sid)
	l, _ := e.srv.lookupLocal(sid)
	l.ReportAgentSession("conv-a", session.PolicyLatest, true)
	e.recordResumable(t, sid)
	legacy := e.launchCrew(t, "Plain", map[string]any{"name": "lead", "agentId": "claude-p", "prompt": "", "start": map[string]any{"when": "immediately"}})
	e.probe(t, e.waitRunning(t, legacy, "lead"))
	out := e.expect(t, "POST", "/api/crews/"+e.managedOf(t, map[string]any{"agentId": "claude-p"})+"/launch", nil, http.StatusCreated, "")
	managed := out["run"].(map[string]any)["id"].(string)
	e.probe(t, startedAttempt(t, e.testEnv, managed, 1)["sessionId"].(string))
	row, _, err := e.srv.ledger.Run(t.Context(), managed)
	if err != nil || row.Crew == "" {
		t.Fatalf("the ledger's run: %+v %v", row, err)
	}
	holdsNoBinding(t, "the ledger's crew", row.Crew)
	_, _, hist := e.call(t, "GET", "/api/history/sessions/"+sid, nil)
	rec, _ := hist["record"].(map[string]any)
	if v, _ := rec["v"].(float64); v >= 3 {
		t.Errorf("the session's record is at version %v", v)
	}
	holdsNoBinding(t, "the session's launch", mustJSON(rec["launch"]))
	shutdown(e.testEnv)
	for _, run := range []string{legacy, managed} {
		b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "runs", run+".json"))
		if err != nil {
			t.Fatal(err)
		}
		holdsNoBinding(t, "the record of "+run, string(b))
	}
	next := &bindEnv{testEnv: serveSecond(t, e.testEnv, newFakeUsage("claude"), newFakeUsage("codex")), probes: e.probes, pin: e.pin}
	out = next.expect(t, "POST", "/api/history/sessions/"+sid+"/resume", map[string]any{"commandId": "c-1", "confirmLost": true}, http.StatusCreated, "")
	resumed := out["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() { next.stopAndWait(resumed) })
	if got, b := next.probe(t, resumed), next.bound(t, resumed); got != login("") || b.Source != session.SourceOwn {
		t.Errorf("the session resumed from its record: %s %+v", got, b)
	}
	out = next.expect(t, "POST", "/api/runs/"+legacy+"/resume", nil, http.StatusCreated, "")
	again := out["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { next.do("POST", "/api/runs/"+again+"/stop", adminToken, nil) })
	if got := next.probe(t, next.waitRunning(t, again, "lead")); got != login("") {
		t.Errorf("the legacy run resumed from its record: %s", got)
	}
	out = next.expect(t, "POST", "/api/runs/"+managed+"/resume", nil, http.StatusCreated, "")
	cont := out["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { next.do("POST", "/api/runs/"+cont+"/stop", adminToken, nil) })
	if got := next.probe(t, startedAttempt(t, next.testEnv, cont, 0)["sessionId"].(string)); got != login("") {
		t.Errorf("the managed run continued: %s", got)
	}
}

// What a sync store is handed of a run is the run as its routes answer it:
// a member on a registered account (its label, folder and fingerprint
// distinctive) and one whose launch sets a key, the run saved and
// stopped: no document the store was handed holds the label, the folder,
// the key or either fingerprint.
func TestSyncIsHandedNoBinding(t *testing.T) {
	f := newFakeSync()
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := &bindEnv{testEnv: capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin),
		WithExtensions(local.Fill(ext.Providers{Continuity: f, All: []ext.Entry{{Kind: ext.KindSync, Name: f.Name(), Value: f}}}))), probes: probes, pin: pin}
	a, folder := e.account(t, "claude", "Zq-Planted-Label")
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Planted", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "yolo": true,
		"members": []any{
			map[string]any{"name": "named", "agentId": "claude-p", "prompt": "", "account": a, "start": map[string]any{"when": "immediately"}},
			map[string]any{"name": "keyed", "agentId": "claude-yolo", "prompt": "", "start": map[string]any{"when": "immediately"}},
		}}, http.StatusCreated)["id"].(string)
	out := e.expect(t, "POST", "/api/crews/"+id+"/launch", nil, http.StatusCreated, "")
	run := out["run"].(map[string]any)["id"].(string)
	named, keyed := e.waitRunning(t, run, "named"), e.waitRunning(t, run, "keyed")
	if got := e.probe(t, keyed); got != "D=[] K=[made-up-key-1] H=[]" {
		t.Fatalf("the keyed member: %s", got)
	}
	e.probe(t, named)
	secrets := []string{"Zq-Planted-Label", folder, "made-up-key-1", e.bound(t, named).Login, e.bound(t, keyed).Login}
	e.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	e.stopAndWait(named)
	e.stopAndWait(keyed)
	k := syncKey{syncData, "runs/" + run + ".json"}
	calls := f.until(t, "the stopped run's record", func(c []syncCall) bool {
		for _, call := range callsOf(c, k) {
			var r crew.Run
			if json.Unmarshal(call.d.Data, &r) == nil && r.State == crew.RunStopped {
				return true
			}
		}
		return false
	})
	for _, c := range calls {
		for _, s := range secrets {
			if s != "" && strings.Contains(string(c.d.Data), s) {
				t.Errorf("%s %s carries %q: %.300s", c.op, c.d.Path, s, c.d.Data)
			}
		}
	}
}

// A refusal of a record whose member's account it keeps no binding for
// is its own: 409 login_not_kept, with the engine's words.
func TestALoginNotKeptIsItsOwnRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).runError(rec, "resume run", "team-00000001", fmt.Errorf("%w: member lead", crew.ErrLoginNotKept))
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || body.Error.Code != "login_not_kept" {
		t.Fatalf("%d %+v", rec.Code, body)
	}
}

// A process on a Runtime other than the native one starts under the
// tool's own login alone, judged where every process starts: a run from
// before bindings, offered to a Runtime, whose member's entry pins a
// login, fails its attempt at the launch with words, nothing started in
// the Runtime.
func TestAGuestStartsOnlyTheToolsOwnLogin(t *testing.T) {
	takesAnswered(t)
	offered := &sessiontest.FakeRuntime{Base: local.Native(), TakesRun: func(ext.ProcessRef) bool { return true }}
	probes, pin := t.TempDir(), realRoot(t, t.TempDir())
	e := &bindEnv{testEnv: capEnv(t, nil, nil, newFakeUsage("claude"), newFakeUsage("codex"), bindAgents(probes, pin), withRuntime(offered)), probes: probes, pin: pin}
	e.gitRepo(t)
	c := oneTaskCrew("old", e.root)
	c.Members[0].AgentID = "claude-pin"
	run := launchOne(t, e.testEnv, c)
	if row := ledgerAttemptIn(t, e.srv, run, "build", 1, ledger.AttemptFailed); !strings.Contains(row.Error, "Runtime cannot take a login") || row.SessionID != "" {
		t.Fatalf("the attempt on the guest: %+v", row)
	}
	if got := terminalRefs(offered); len(got) != 0 {
		t.Fatalf("the guest started %+v", got)
	}
}
