package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// What Brakeman reads (docs/plans/brakeman.md, PR-4): a turn's session
// token, with X-Conductor-Session naming its session, reads the shared
// routes as the principal brakeman, filtered by its grant and counted,
// and Brakeman's own reads under /api/brakeman/. The fixture is a server
// with Brakeman's scripted agent, two managed runs in the ledger (team's,
// whose build failed on a check, and other's), their records, two
// Dispatch projects with a ticket each (CND's linked to team's run, with
// a comment), and a plain session asking a question; the turn is asked
// for once the records are there.

// obsFixture is the server, its records and the turn.
type obsFixture struct {
	*bkEnv
	run, other  string
	logSHA      string
	log         []byte
	ticket      string
	otherTicket string
	comment     string
	plain       string
	plainLocal  *session.Local
	question    string
	req, sid    string
	tok         string
	turnLocal   *session.Local
}

func newObsFixture(t *testing.T) *obsFixture {
	t.Helper()
	f := &obsFixture{bkEnv: brakemanEnv(t, nil), run: "team-0000aaaa", other: "other-0000bbbb"}
	f.seed(t)
	return f
}

// seed writes the runs, the tickets and the plain session.
func (f *obsFixture) seed(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	e := f.testEnv
	f.log = []byte("FAIL: TestIt\n")
	sum := sha256.Sum256(f.log)
	f.logSHA = hex.EncodeToString(sum[:])
	for _, run := range []string{f.run, f.other} {
		crewID := crewOfRun(run)
		// team's run still runs (its deploy waits on its build); other's
		// is stopped.
		state, recState := ledger.RunRunning, crew.RunRunning
		if run == f.other {
			state, recState = ledger.RunStopped, crew.RunStopped
		}
		crewJSON, _ := json.Marshal(map[string]string{"id": crewID, "cwd": e.root})
		if _, err := e.srv.ledger.Transition(ctx, run, func(tx *ledger.Tx) error {
			now := tx.Now()
			for _, err := range []error{
				tx.PutRun(ledger.RunRow{ID: run, CrewID: crewID, Crew: string(crewJSON), State: state, CreatedAt: now}),
				tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: "work", State: ledger.TaskFailed, Attempts: 1}),
				tx.PutTask(ledger.TaskRow{ID: "deploy", Member: "lead", Kind: "work", State: ledger.TaskBlocked}),
				tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptFailed, ReservedAt: now, ClosedAt: now}),
				tx.PutCheck(ledger.CheckRow{TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, State: ledger.CheckFinished, Exit: 1, Log: f.log, LogSHA256: f.logSHA, StartedAt: now, FinishedAt: now}),
				tx.Event("task.failed", map[string]string{"task": "build"}),
				tx.PutMessage(ledger.MessageRow{Seq: 1, From: "lead", To: "lead", Text: "noted", CreatedAt: now, DeliveredAt: now}),
			} {
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.srv.records.Save(crew.Run{ID: run, CrewID: crewID, Name: crewID, Cwd: e.root, State: recState, Managed: &crew.Managed{Revision: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	ds := e.srv.dispatch
	done := doneOf(t)
	done(ds.CreateProject(ctx, adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	done(ds.CreateProject(ctx, adminOp("Ann"), dispatch.NewProject{Key: "OTH", Name: "Other"}))
	f.ticket = done(ds.CreateTicket(ctx, adminOp("Ann"), dispatch.NewTicket{Project: "CND", Title: "The build fails"})).Key
	f.otherTicket = done(ds.CreateTicket(ctx, adminOp("Ann"), dispatch.NewTicket{Project: "OTH", Title: "Elsewhere"})).Key
	f.comment = done(ds.AddComment(ctx, adminOp("Ann"), f.ticket, "It fails on main too.")).ID
	done(ds.AddLink(ctx, adminOp("Ann"), f.ticket, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: f.run}))
	done(ds.AddLink(ctx, adminOp("Ann"), f.otherTicket, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: f.other}))

	f.plain = e.createSession("cat")
	d, _ := e.srv.registry.Get(f.plain)
	f.plainLocal = d.(*session.Local)
	f.plainLocal.SetAttentionFull(session.AttentionNeedsInput, "Proceed?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Yes", Input: "y"}})
	f.question = f.plainLocal.Info().Attention.ID
	if f.question == "" {
		t.Fatal("no question")
	}
}

// turn turns Brakeman on with mod applied and asks for a briefing, then
// waits for its session: the turn reads as it from then on.
func (f *obsFixture) turn(t *testing.T, mod func(c map[string]any)) {
	t.Helper()
	f.enable(t, "bk", mod)
	f.req = f.briefing(t)
	f.sid, f.tok, f.turnLocal = f.turnSession(t, f.req)
	f.waitState(t, f.req, brakeman.StateRunning)
}

// again ends the turn (its run stopped, the slot given up) and asks for
// the next with mod applied to the configuration.
func (f *obsFixture) again(t *testing.T, mod func(c map[string]any)) {
	t.Helper()
	f.endTurn(t)
	if mod != nil {
		f.configure(t, mod)
	}
	f.req = f.briefing(t)
	f.sid, f.tok, f.turnLocal = f.turnSession(t, f.req)
	f.waitState(t, f.req, brakeman.StateRunning)
}

// endTurn stops the turn's run and waits for the slot.
func (f *obsFixture) endTurn(t *testing.T) {
	t.Helper()
	run := f.runOf(t, f.sid)
	f.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	f.slotFree(t)
}

// read reads path as the turn: its token, its session named.
func (f *obsFixture) read(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	resp, body := f.readRaw(t, path)
	var out map[string]any
	if len(body) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s: %v %.200s", path, err, body)
		}
	}
	return resp.StatusCode, out
}

// readRaw is read's reply and body.
func (f *obsFixture) readRaw(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	return f.raw(path, f.tok, map[string]string{brakemanSessionHeader: f.sid})
}

// expectRead reads path as the turn, failing t unless it answers status.
func (f *obsFixture) expectRead(t *testing.T, path string, status int, code string) map[string]any {
	t.Helper()
	got, out := f.read(t, path)
	if got != status || (code != "" && errorCode(out) != code) {
		t.Fatalf("%s: %d %v, want %d %s", path, got, out, status, code)
	}
	return out
}

// event adds an event to a run of the ledger, naming task when it is not
// empty, and answers its seq.
func (f *obsFixture) event(t *testing.T, run, kind, task string) {
	t.Helper()
	payload := map[string]string{}
	if task != "" {
		payload["task"] = task
	}
	if _, err := f.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error { return tx.Event(kind, payload) }); err != nil {
		t.Fatal(err)
	}
}

// readLog is the turn's read log.
func (f *obsFixture) readLog(t *testing.T) brakeman.ReadLog {
	t.Helper()
	log, err := f.desk.st.Reads(f.req)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

// readOf is the newest read of ref in the turn's log.
func (f *obsFixture) readOf(t *testing.T, ref string) (brakeman.Read, bool) {
	t.Helper()
	log := f.readLog(t)
	for i := len(log.Reads) - 1; i >= 0; i-- {
		if log.Reads[i].Ref.String() == ref {
			return log.Reads[i], true
		}
	}
	return brakeman.Read{}, false
}

// refsOf is the ref of each object of list.
func refsOf(t *testing.T, list any) []string {
	t.Helper()
	var out []string
	items, _ := list.([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		ref, _ := m["ref"].(string)
		out = append(out, ref)
	}
	return out
}

// The admin's answers of the shared reads are the routes' own, with no
// ref added and nothing counted: the principal's filter is the turn's
// alone.
func TestTheAdminsReadsAreUnchanged(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	paths := []string{
		"/api/runs", "/api/runs/" + f.run, "/api/runs/" + f.run + "/tasks/build", "/api/runs/" + f.run + "/tasks/build/attempts/1/steps",
		"/api/dispatch/projects", "/api/dispatch/tickets", "/api/dispatch/tickets/" + f.ticket, "/api/dispatch/tickets/" + f.ticket + "/history",
		"/api/dispatch/tickets/" + f.ticket + "/comments", "/api/sessions", "/api/touched",
	}
	before, _ := f.desk.st.Get(f.req)
	added := regexp.MustCompile(`"ref":"(run|task|attempt|check|command|message|ticket|comment|session|question|file):`)
	for _, p := range paths {
		_, data, _ := f.call(t, "GET", p, nil)
		if added.Match(data) {
			t.Errorf("%s: the admin's answer carries refs: %.200s", p, data)
		}
	}
	resp, body := f.raw("/api/runs/"+f.run+"/tasks/build/attempts/1/steps/test/1/log", adminToken, nil)
	if resp.StatusCode != http.StatusOK || string(body) != string(f.log) || resp.Header.Get(logHashHeader) != f.logSHA {
		t.Fatalf("the admin's log: %d %q %q", resp.StatusCode, body, resp.Header.Get(logHashHeader))
	}
	if after, _ := f.desk.st.Get(f.req); after.Reads != before.Reads {
		t.Fatalf("the admin's reads were counted: %d then %d", before.Reads, after.Reads)
	}
}

// The principal brakeman reads exactly the shared reads and Brakeman's
// own: every other route of the mux answers its token as it answers no
// token at all.
func TestBrakemansPrincipalReadsOnlyTheNamedRoutes(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	own := []string{"GET /api/brakeman/changes", "GET /api/brakeman/status", "GET /api/brakeman/screen", "GET /api/brakeman/overlaps", "GET /api/brakeman/briefs"}
	wild := strings.NewReplacer("{run}", f.run, "{task}", "build", "{n}", "1", "{id}", "test", "{k}", "1", "{key}", f.ticket, "{command}", "c1")
	for _, pattern := range muxPatterns(t, "/api/") {
		method, path, _ := strings.Cut(pattern, " ")
		if strings.HasPrefix(path, "/api/sessions/{id}") || path == "/api/events" {
			// The session's own routes take its token already; the
			// events stream is not a read of the shared table.
			continue
		}
		concrete := wild.Replace(path)
		concrete = strings.NewReplacer("{i}", "0", "{linkId}", "x", "{name}", "x", "{opId}", "x", "{token}", "x").Replace(concrete)
		var body []byte
		if method != http.MethodGet {
			body = []byte("{}")
		}
		req, _ := http.NewRequest(method, f.http.URL+concrete, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+f.tok)
		req.Header.Set(brakemanSessionHeader, f.sid)
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		anon, _ := http.NewRequest(method, f.http.URL+concrete, strings.NewReader(string(body)))
		aresp, err := f.client.Do(anon)
		if err != nil {
			t.Fatal(err)
		}
		aresp.Body.Close()
		_, shared := sharedReadSpec(pattern)
		switch {
		case shared || slices.Contains(own, pattern):
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s: the principal is refused (%d)", pattern, resp.StatusCode)
			}
		case resp.StatusCode != aresp.StatusCode:
			t.Errorf("%s: the principal gets %d, no token %d", pattern, resp.StatusCode, aresp.StatusCode)
		}
	}
}

// A shared read is the route's own answer filtered by the grant: the runs
// a scope of runs names, each with its ref; a run outside it refused.
func TestASharedReadIsTheRoutesOwnAnswerFiltered(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.run}} })
	admin := f.expect(t, "GET", "/api/runs", nil, http.StatusOK, "")
	if len(admin["runs"].([]any)) == 0 {
		t.Fatal("the admin lists no run (the turn's own at least)")
	}
	mine := f.expectRead(t, "/api/runs", http.StatusOK, "")
	if got := refsOf(t, mine["runs"]); len(got) != 0 {
		t.Fatalf("the engine's runs in a scope of %s: %v", f.run, got)
	}
	run := f.expectRead(t, "/api/runs/"+f.run, http.StatusOK, "")
	r := entityOf(t, run, "run")
	if r["ref"] != "run:"+f.run || r["id"] != f.run || run["record"] != true {
		t.Fatalf("the run, the route's own record with its ref: %v", run)
	}
	if got := refsOf(t, r["tasks"]); len(got) != 0 {
		// The record holds no tasks of its own: nothing is added.
		t.Fatalf("tasks of a record that has none: %v", got)
	}
	f.expectRead(t, "/api/runs/"+f.other, http.StatusForbidden, "outside_scope")
	task := f.expectRead(t, "/api/runs/"+f.run+"/tasks/build", http.StatusOK, "")
	if entityOf(t, task, "task")["ref"] != "task:"+f.run+"/build" {
		t.Fatalf("the task's ref: %v", task)
	}
	attempts := task["attempts"].([]any)
	if len(attempts) != 1 || attempts[0].(map[string]any)["ref"] != "attempt:"+f.run+"/build/1" {
		t.Fatalf("the attempts' refs: %v", attempts)
	}
	steps := attempts[0].(map[string]any)["steps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["ref"] != "check:"+f.run+"/build/1/test/1" || steps[0].(map[string]any)["logSha256"] != f.logSHA {
		t.Fatalf("the receipts' refs: %v", steps)
	}
	if rd, ok := f.readOf(t, "check:"+f.run+"/build/1/test/1"); !ok || rd.State != "exit:1" {
		t.Fatalf("the receipt noted: %+v %v", rd, ok)
	}
	tickets := f.expectRead(t, "/api/dispatch/tickets", http.StatusOK, "")
	if got := refsOf(t, tickets["tickets"]); !slices.Equal(got, []string{"ticket:" + f.ticket}) {
		t.Fatalf("the tickets linked to %s: %v", f.run, got)
	}
	ticket := entityOf(t, f.expectRead(t, "/api/dispatch/tickets/"+f.ticket, http.StatusOK, ""), "ticket")
	if ticket["ref"] != "ticket:"+f.ticket || !slices.Equal(refsOf(t, ticket["comments"]), []string{"comment:" + f.ticket + "/" + f.comment}) {
		t.Fatalf("the ticket and its comment: %v", ticket)
	}
	f.expectRead(t, "/api/dispatch/tickets/"+f.otherTicket, http.StatusForbidden, "outside_scope")
}

// Every read keeps to the scope: a scope of one crew's runs leaves out the
// other crew's run, its ticket, the plain session in no run, and their
// changes.
func TestObserveKeepsToTheScope(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} })
	sessions := f.expectRead(t, "/api/sessions", http.StatusOK, "")
	if slices.Contains(refsOf(t, sessions["sessions"]), "session:"+f.plain) {
		t.Fatal("a session in no run under a scope of crews")
	}
	tickets := f.expectRead(t, "/api/dispatch/tickets", http.StatusOK, "")
	if got := refsOf(t, tickets["tickets"]); !slices.Equal(got, []string{"ticket:" + f.ticket}) {
		t.Fatalf("tickets: %v", got)
	}
	changes := f.expectRead(t, "/api/brakeman/changes?since="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), http.StatusOK, "")
	for _, c := range changes["changes"].([]any) {
		ref := c.(map[string]any)["ref"].(string)
		if strings.Contains(ref, f.other) || strings.Contains(ref, f.otherTicket) || strings.Contains(ref, f.plain) {
			t.Fatalf("a change outside the scope: %s", ref)
		}
	}
	f.expectRead(t, "/api/brakeman/status?run="+f.other, http.StatusForbidden, "outside_scope")
	f.expectRead(t, "/api/brakeman/status?session="+f.plain, http.StatusForbidden, "outside_scope")
}

// A check's log reaches a turn as the route answers it, its SHA-256 in
// X-Conductor-Log-SHA256 equal to the receipt's, and is noted as a read
// of the logs.
func TestALogTailCarriesItsSHA(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	resp, body := f.readRaw(t, "/api/runs/"+f.run+"/tasks/build/attempts/1/steps/test/1/log")
	if resp.StatusCode != http.StatusOK || string(body) != string(f.log) || resp.Header.Get(logHashHeader) != f.logSHA {
		t.Fatalf("the log: %d %q %q", resp.StatusCode, body, resp.Header.Get(logHashHeader))
	}
	if rd, ok := f.readOf(t, "check:"+f.run+"/build/1/test/1"); !ok || rd.Class != brakeman.ClassLogs {
		t.Fatalf("the log's read: %+v %v", rd, ok)
	}
	// Without logs visible, the log is refused and its receipt still read.
	f.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["logs"] = false })
	f.expectRead(t, "/api/runs/"+f.run+"/tasks/build/attempts/1/steps/test/1/log", http.StatusForbidden, "not_visible")
	f.expectRead(t, "/api/runs/"+f.run+"/tasks/build/attempts/1/steps", http.StatusOK, "")
}

// Each answer counts one read; past the turn's reads the next is refused
// 429 reads_spent, and its items are noted in the read log.
func TestReadsAreCountedAndBounded(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, func(c map[string]any) { c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MinReadsPerTurn })
	for i := range brakeman.MinReadsPerTurn {
		if status, out := f.read(t, "/api/dispatch/tickets"); status != http.StatusOK {
			t.Fatalf("read %d: %d %v", i, status, out)
		}
	}
	f.expectRead(t, "/api/dispatch/tickets", http.StatusTooManyRequests, "reads_spent")
	r, _ := f.desk.st.Get(f.req)
	if r.Reads != brakeman.MinReadsPerTurn {
		t.Fatalf("%d reads counted", r.Reads)
	}
	if log := f.readLog(t); len(log.Reads) < brakeman.MinReadsPerTurn {
		t.Fatalf("%d reads noted", len(log.Reads))
	}
	// The turn's request still answers: it is its own, not a read.
	if status, _ := f.readRequest(t, f.sid, f.tok); status != http.StatusOK {
		t.Fatalf("its request: %d", status)
	}
}

// A session's screen is read only with terminals visible.
func TestTheScreenNeedsTerminals(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.expectRead(t, "/api/brakeman/screen?session="+f.plain, http.StatusForbidden, "not_visible")
	f.plainLocal.Record(session.ActivityEntry{At: time.Now(), Type: session.ActivityProgress, Message: "x"})
	f.again(t, func(c map[string]any) { c["visibility"].(map[string]any)["terminals"] = true })
	out := f.expectRead(t, "/api/brakeman/screen?session="+f.plain+"&lines=5", http.StatusOK, "")
	if out["session"] != "session:"+f.plain {
		t.Fatalf("the screen: %v", out)
	}
	if rd, ok := f.readOf(t, "session:"+f.plain); !ok || rd.Class != brakeman.ClassTerminals {
		t.Fatalf("the screen's read: %+v %v", rd, ok)
	}
	f.expectRead(t, "/api/brakeman/screen?session="+f.plain+"&lines=201", http.StatusBadRequest, "invalid_request")
}

// A hosted session's screen is its host's: unavailable, never empty.
func TestAHostedSessionsScreenIsUnavailable(t *testing.T) {
	f := newObsFixture(t)
	h := &hostedDriver{info: session.Info{ID: session.NewID(), Name: "hosted", Kind: session.KindHosted, Cwd: f.root, Status: session.StatusRunning}}
	if err := f.srv.registry.Add(h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.srv.registry.Remove(h.info.ID) })
	f.turn(t, func(c map[string]any) { c["visibility"].(map[string]any)["terminals"] = true })
	f.expectRead(t, "/api/brakeman/screen?session="+h.info.ID, http.StatusConflict, "unavailable")
}

// A hosted session's quiet time is unknown, never quiet.
func TestAHostedSessionIsNeverQuiet(t *testing.T) {
	f := newObsFixture(t)
	h := &hostedDriver{info: session.Info{ID: session.NewID(), Name: "hosted", Kind: session.KindHosted, Cwd: f.root, Status: session.StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}}
	if err := f.srv.registry.Add(h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.srv.registry.Remove(h.info.ID) })
	at := time.Now().Add(-time.Hour)
	seam := func(id string) (time.Time, bool) { return at, true }
	outputAtSeam.Store(&seam)
	t.Cleanup(func() { outputAtSeam.Store(nil) })
	f.turn(t, nil)
	st := entityOf(t, f.expectRead(t, "/api/brakeman/status?session="+h.info.ID, http.StatusOK, ""), "status")
	if st["status"] != statusRunning || st["quiet"] != "unknown" || st["proof"] != proofNone {
		t.Fatalf("a hosted session's status: %v", st)
	}
}

// A server session whose terminal has written nothing for ten minutes is
// quiet, with no proof of anything: never failed.
func TestAQuietSessionIsNotAFailure(t *testing.T) {
	f := newObsFixture(t)
	f.plainLocal.SetAttention(session.AttentionNone, "", session.SourceAPI)
	clk := f.fakeClock()
	last := clk.now().Add(-11 * time.Minute)
	seam := func(id string) (time.Time, bool) { return last, id == f.plain }
	outputAtSeam.Store(&seam)
	t.Cleanup(func() { outputAtSeam.Store(nil) })
	f.turn(t, nil)
	st := entityOf(t, f.expectRead(t, "/api/brakeman/status?session="+f.plain, http.StatusOK, ""), "status")
	if st["status"] != statusQuiet || st["proof"] != proofNone || st["quietSeconds"].(float64) < 600 {
		t.Fatalf("a quiet session: %v", st)
	}
	last = clk.now().Add(-time.Minute)
	st = entityOf(t, f.expectRead(t, "/api/brakeman/status?session="+f.plain, http.StatusOK, ""), "status")
	if st["status"] != statusRunning {
		t.Fatalf("a session that wrote a minute ago: %v", st)
	}
}

// A session asking is waiting on a person, its question the proof.
func TestAnOpenQuestionIsWaitingOnAPerson(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	st := entityOf(t, f.expectRead(t, "/api/brakeman/status?session="+f.plain, http.StatusOK, ""), "status")
	q := "question:" + f.plain + "/" + f.question
	if st["status"] != statusWaitingOnPerson || st["question"] != q || st["proof"] != q {
		t.Fatalf("a session asking: %v", st)
	}
	sessions := f.expectRead(t, "/api/sessions", http.StatusOK, "")
	if !slices.Contains(refsOf(t, sessions["sessions"]), "session:"+f.plain) {
		t.Fatal("the plain session under an empty scope")
	}
	if rd, ok := f.readOf(t, q); !ok || rd.State != questionOpen {
		t.Fatalf("the question noted: %+v %v", rd, ok)
	}
}

// A failed task carries the receipt that proves it: its last attempt's
// failing check; a task waiting on its needs is blocked.
func TestAFailedTaskCarriesItsReceipt(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	out := f.expectRead(t, "/api/brakeman/status?run="+f.run, http.StatusOK, "")
	byRef := map[string]map[string]any{}
	for _, x := range out["tasks"].([]any) {
		m := x.(map[string]any)
		byRef[m["ref"].(string)] = m
	}
	build := byRef["task:"+f.run+"/build"]
	if build["status"] != statusFailed || build["proof"] != "check:"+f.run+"/build/1/test/1" || !strings.Contains(build["words"].(string), "exit:1") {
		t.Fatalf("the failed task: %v", build)
	}
	if deploy := byRef["task:"+f.run+"/deploy"]; deploy["status"] != statusBlocked || deploy["proof"] != proofNone {
		t.Fatalf("the blocked task: %v", deploy)
	}
	// Its changes carry the same status.
	changes := f.expectRead(t, "/api/brakeman/changes?since="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), http.StatusOK, "")
	found := false
	for _, c := range changes["changes"].([]any) {
		m := c.(map[string]any)
		if m["task"] == "task:"+f.run+"/build" {
			found = true
			if st, _ := m["status"].(map[string]any); st == nil || st["status"] != statusFailed || st["proof"] != "check:"+f.run+"/build/1/test/1" {
				t.Fatalf("the change's status: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("the failure is not among the changes: %v", changes)
	}
}

// A comment on an old ticket, which moves nothing of the ticket's own, is
// among the changes.
func TestACommentOnlyChangeIsSeen(t *testing.T) {
	f := newObsFixture(t)
	since := time.Now().UTC()
	c := doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Bob"), f.otherTicket, "a word on the old one"))
	f.turn(t, nil)
	out := f.expectRead(t, "/api/brakeman/changes?since="+since.Format(time.RFC3339Nano), http.StatusOK, "")
	want := "comment:" + f.otherTicket + "/" + c.ID
	for _, ch := range out["changes"].([]any) {
		m := ch.(map[string]any)
		if m["ref"] == want && m["ticket"] == "ticket:"+f.otherTicket && m["part"] == partDispatch {
			return
		}
	}
	t.Fatalf("the comment is not among the changes: %v", out["changes"])
}

// Read since a time, a run's newest events past the window are marked
// truncated, and so is Dispatch's feed.
func TestChangesSinceATimeAreBoundedAndSayTruncated(t *testing.T) {
	f := newObsFixture(t)
	old := changesWindow
	changesWindow = 3
	t.Cleanup(func() { changesWindow = old })
	since := time.Now().Add(-time.Hour).UTC()
	for i := range 5 {
		f.event(t, f.run, "tick", fmt.Sprintf("t%d", i))
		doneOf(t)(f.srv.dispatch.AddComment(t.Context(), adminOp("Bob"), f.ticket, fmt.Sprintf("comment %d", i)))
	}
	f.turn(t, nil)
	out := f.expectRead(t, "/api/brakeman/changes?since="+since.Format(time.RFC3339), http.StatusOK, "")
	parts := map[string]map[string]any{}
	for _, p := range out["parts"].([]any) {
		m := p.(map[string]any)
		parts[m["part"].(string)] = m
	}
	if p := parts["run:"+f.run]; p == nil || p["truncated"] != true {
		t.Fatalf("the run's part: %v", parts)
	}
	if p := parts[partDispatch]; p == nil || p["truncated"] != true {
		t.Fatalf("Dispatch's part: %v", parts)
	}
	n := 0
	for _, c := range out["changes"].([]any) {
		if strings.HasPrefix(c.(map[string]any)["ref"].(string), "event:"+f.run+"/") {
			n++
		}
	}
	if n > 3 {
		t.Fatalf("%d of the run's events, past the window of 3", n)
	}
}

// changesOf reads every page of the turn's changes from its own start
// (no since), limit a page, and answers the refs.
func (f *obsFixture) changesOf(t *testing.T, limit int) []string {
	t.Helper()
	var refs []string
	cursor := ""
	for {
		out := f.expectRead(t, fmt.Sprintf("/api/brakeman/changes?limit=%d&cursor=%s", limit, cursor), http.StatusOK, "")
		for _, c := range out["changes"].([]any) {
			refs = append(refs, c.(map[string]any)["ref"].(string))
		}
		cursor, _ = out["next"].(string)
		if cursor == "" {
			return refs
		}
	}
}

// submitFact submits a brief of one fact citing ref and waits for it.
func (f *obsFixture) submitFact(t *testing.T, ref string) {
	t.Helper()
	if status, out := f.submit(t, f.sid, f.tok, fmt.Sprintf("b-%d", bkOps.Add(1)), factBrief(ref)); status != http.StatusCreated {
		t.Fatalf("the brief: %d %v", status, out)
	}
}

// The next briefing starts from the last one's watermarks: what the
// earlier turn read is not read again; what happened since is.
func TestChangesSinceTheLastBriefUseItsWatermarks(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	first := f.changesOf(t, 200)
	if !slices.Contains(first, "event:"+f.run+"/1") {
		t.Fatalf("the first briefing's changes: %v", first)
	}
	f.submitFact(t, "run:"+f.run)
	r, _ := f.desk.st.Get(f.req)
	if r.Coverage == nil || len(r.Coverage.Runs) == 0 || r.Coverage.Dispatch == nil {
		t.Fatalf("the brief's coverage: %+v", r.Coverage)
	}
	f.event(t, f.run, "task.retried", "build")
	f.again(t, nil)
	out := f.expectRead(t, "/api/brakeman/changes", http.StatusOK, "")
	if out["from"] != "watermarks" || out["briefing"] != r.ID {
		t.Fatalf("the second briefing's start: %v %v", out["from"], out["briefing"])
	}
	second := refsOf(t, out["changes"])
	if slices.Contains(second, "event:"+f.run+"/1") || !slices.Contains(second, "event:"+f.run+"/2") {
		t.Fatalf("the second briefing's changes: %v", second)
	}
}

// What a briefing covers is the server's record: a page the turn never
// read, a truncated run and a window with a gap leave their parts
// partial, and the next briefing reads them again.
func TestCoverageIsTheServers(t *testing.T) {
	f := newObsFixture(t)
	old := changesWindow
	changesWindow = 3
	t.Cleanup(func() { changesWindow = old })
	for i := range 4 {
		f.event(t, f.other, "tick", fmt.Sprintf("t%d", i))
	}
	f.turn(t, nil)
	page := f.expectRead(t, "/api/brakeman/changes?limit=1", http.StatusOK, "")
	if page["next"] == "" {
		t.Fatalf("one page of many: %v", page)
	}
	first := refsOf(t, page["changes"])
	f.submitFact(t, "run:"+f.run)
	r, _ := f.desk.st.Get(f.req)
	partial := 0
	for _, w := range slices.Concat(r.Coverage.Runs, r.Coverage.Sessions) {
		if w.Partial {
			partial++
		}
	}
	if partial == 0 {
		t.Fatalf("an unread page and a truncated run leave nothing partial: %+v", r.Coverage)
	}
	f.again(t, nil)
	second := f.changesOf(t, 200)
	if len(second) == 0 {
		t.Fatal("the next briefing reads nothing again")
	}
	for _, ref := range first {
		if slices.Contains(second, ref) {
			t.Fatalf("%s was delivered and is read again: %v", ref, second)
		}
	}
	// The run's events past the first page are read again.
	again := 0
	for _, ref := range second {
		if strings.HasPrefix(ref, "event:"+f.other+"/") {
			again++
		}
	}
	if again == 0 {
		t.Fatalf("the truncated run's events are not read again: %v", second)
	}
}

// A run newly in scope has no watermark: it starts at the earlier
// briefing's reads, labelled newly in scope.
func TestAWidenedScopeStartsAtTheLastBriefing(t *testing.T) {
	f := newObsFixture(t)
	f.event(t, f.other, "before", "")
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} })
	f.changesOf(t, 200)
	f.submitFact(t, "run:"+f.run)
	f.event(t, f.other, "after", "")
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{} })
	out := f.expectRead(t, "/api/brakeman/changes", http.StatusOK, "")
	var part map[string]any
	for _, p := range out["parts"].([]any) {
		if m := p.(map[string]any); m["part"] == "run:"+f.other {
			part = m
		}
	}
	if part == nil || part["new"] != true || part["from"] != "new" {
		t.Fatalf("the run newly in scope: %v", out["parts"])
	}
	kinds := map[string]bool{}
	for _, c := range out["changes"].([]any) {
		m := c.(map[string]any)
		if m["part"] == "run:"+f.other {
			kinds[m["kind"].(string)] = true
		}
	}
	if !kinds["after"] || kinds["before"] {
		t.Fatalf("the new run's changes: %v", kinds)
	}
}

// What the turn read of a ref is kept beside what it is as the brief is
// kept.
func TestASeenRevisionIsKeptBesideNow(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.expectRead(t, "/api/dispatch/tickets/"+f.ticket, http.StatusOK, "")
	seen, ok := f.readOf(t, "ticket:"+f.ticket)
	if !ok || seen.Rev == "" {
		t.Fatalf("the ticket's read: %+v %v", seen, ok)
	}
	title := "The build fails on main"
	doneOf(t)(f.srv.dispatch.EditTicket(t.Context(), adminOp("Bob"), f.ticket, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &title}}))
	f.submitFact(t, "ticket:"+f.ticket)
	_, refs, err := f.desk.st.Brief(f.req)
	if err != nil || len(refs) != 1 {
		t.Fatalf("the brief's refs: %v %v", refs, err)
	}
	if refs[0].Seen.Rev != seen.Rev || refs[0].Now.Rev == seen.Rev || refs[0].Now.Rev == "" {
		t.Fatalf("seen %q, now %q: %+v", refs[0].Seen.Rev, refs[0].Now.Rev, refs[0])
	}
}

// A decision about a ticket that names a revision other than the one the
// turn read is refused, saying which it read.
func TestAStaleSubjectRevisionIsRefused(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.expectRead(t, "/api/dispatch/tickets/"+f.ticket, http.StatusOK, "")
	seen, _ := f.readOf(t, "ticket:"+f.ticket)
	rec := 0
	decision := func(rev string) map[string]any {
		return map[string]any{"schema": brakeman.BriefSchema, "title": "A decision", "summary": "One to make.", "items": []any{map[string]any{
			"kind": brakeman.ItemDecision, "about": "ticket:" + f.ticket, "happened": "the build failed", "whyHuman": "only you can say",
			"question": "Fix it now?", "alternatives": []any{map[string]any{"label": "Now", "tradeoffs": "costs today"}, map[string]any{"label": "Later", "tradeoffs": "risks tomorrow"}},
			"recommendation": rec, "authorizes": "a retry", "evidence": []string{"ticket:" + f.ticket}, "subjectRevision": rev,
		}}}
	}
	status, out := f.submit(t, f.sid, f.tok, "stale", decision(seen.Rev+"1"))
	if status != http.StatusUnprocessableEntity || errorCode(out) != "bad_refs" || !strings.Contains(fmt.Sprint(out), "you read revision "+seen.Rev) {
		t.Fatalf("a stale revision: %d %v", status, out)
	}
	if status, out := f.submit(t, f.sid, f.tok, "fresh", decision(seen.Rev)); status != http.StatusCreated {
		t.Fatalf("the revision read: %d %v", status, out)
	}
}

// Each of Brakeman's own reads is held to its bound with its inputs at
// their largest.
func TestEachReplyIsBounded(t *testing.T) {
	f := newObsFixture(t)
	ctx := t.Context()
	long := strings.Repeat("x", 900)
	for i := range 300 {
		doneOf(t)(f.srv.dispatch.AddComment(ctx, adminOp(long[:50]), f.ticket, fmt.Sprintf("comment %d", i)))
	}
	// Earlier briefs at their largest: 4 KiB summaries and 100 refs each.
	for i := range maxBriefsPage + 5 {
		var refs []brakeman.Ref
		for k := range 50 {
			refs = append(refs, ref(t, fmt.Sprintf("ticket:CND-%d", k+1)))
		}
		items := []brakeman.Item{panel(refs...), panel(refs...)}
		f.keepEarlier(t, brakeman.DefaultConfig(f.root), items, strings.Repeat("s", brakeman.MaxSummary), nil)
		_ = i
	}
	for range maxScreenLines {
		f.plainLocal.Record(session.ActivityEntry{At: time.Now(), Type: session.ActivityProgress, Message: "x"})
	}
	f.turn(t, func(c map[string]any) {
		c["visibility"].(map[string]any)["terminals"] = true
		c["budget"].(map[string]any)["readsPerTurn"] = brakeman.MaxReadsPerTurn
	})
	for _, p := range []string{
		"/api/brakeman/changes?limit=200&since=" + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"/api/brakeman/briefs",
		"/api/brakeman/screen?session=" + f.plain + "&lines=200",
		"/api/brakeman/overlaps",
		"/api/brakeman/status?run=" + f.run,
	} {
		resp, body := f.readRaw(t, p)
		if resp.StatusCode != http.StatusOK || len(body) > maxObserveReply {
			t.Fatalf("%s: %d, %d bytes", p, resp.StatusCode, len(body))
		}
	}
	briefs := f.expectRead(t, "/api/brakeman/briefs", http.StatusOK, "")
	if n := len(briefs["briefs"].([]any)); n > maxBriefsPage || n == 0 || briefs["next"] == "" {
		t.Fatalf("a page of %d briefs, next %v", n, briefs["next"])
	}
}

// keepEarlier keeps an earlier briefing through Brakeman's store, begun
// under cfg, with its reads (its footprint) and a brief of items: a
// briefed request as a turn leaves one, older than the turn asked next.
func (f *obsFixture) keepEarlier(t *testing.T, cfg brakeman.Config, items []brakeman.Item, summary string, reads []brakeman.Read) string {
	t.Helper()
	st := f.desk.st
	r, _, err := st.Create(brakeman.Request{OpID: fmt.Sprintf("early-%d", bkOps.Add(1)), Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(r.ID, cfg, brakeman.Stamp{AgentID: "bk"}); err != nil {
		t.Fatal(err)
	}
	if len(reads) > 0 {
		if _, err := st.NoteReads(r.ID, reads...); err != nil {
			t.Fatal(err)
		}
	}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "Overnight", Summary: summary, Items: items}
	if _, err := st.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// briefsOf is the turn's page of earlier briefs, by id.
func (f *obsFixture) briefsOf(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, b := range f.expectRead(t, "/api/brakeman/briefs", http.StatusOK, "")["briefs"].([]any) {
		m := b.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

// whole reports whether a brief line is given whole: its prose, not
// withheld.
func whole(b map[string]any) bool { return b != nil && b["summary"] != nil && b["withheld"] == nil }

// An earlier brief made under a wider scope is withheld from a turn whose
// scope is narrower: its id, kind and the refs the scope admits now, none
// of its prose.
func TestAnEarlierBriefUnderAWiderScopeIsWithheld(t *testing.T) {
	f := newObsFixture(t)
	wide := f.keepEarlier(t, brakeman.DefaultConfig(f.root), []brakeman.Item{fact("both runs failed", ref(t, "run:"+f.run), ref(t, "run:"+f.other))}, "Both failed.", nil)
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"crews": []string{"team"}} })
	got := f.briefsOf(t)[wide]
	if got == nil || whole(got) || got["withheld"] == nil {
		t.Fatalf("a brief under a wider scope: %v", got)
	}
	if refs := got["refs"].([]any); len(refs) != 1 || refs[0] != "run:"+f.run {
		t.Fatalf("the refs the scope admits now: %v", refs)
	}
}

// A brief whose turn read screens is withheld once terminals are off; the
// same for check logs.
func TestTurningTerminalsOffWithholdsBriefsThatReadScreens(t *testing.T) {
	f := newObsFixture(t)
	cfg := brakeman.DefaultConfig(f.root)
	cfg.Visibility.Terminals = true
	screens := f.keepEarlier(t, cfg, []brakeman.Item{fact("the screen said so", ref(t, "session:"+f.plain))}, "From a screen.",
		[]brakeman.Read{{Ref: ref(t, "session:"+f.plain), Class: brakeman.ClassTerminals, At: time.Now()}})
	logs := f.keepEarlier(t, brakeman.DefaultConfig(f.root), []brakeman.Item{fact("the log said so", ref(t, "run:"+f.run))}, "From a log.",
		[]brakeman.Read{{Ref: ref(t, "check:"+f.run+"/build/1/test/1"), Class: brakeman.ClassLogs, At: time.Now()}})
	f.turn(t, func(c map[string]any) { c["visibility"].(map[string]any)["logs"] = false })
	got := f.briefsOf(t)
	if whole(got[screens]) || whole(got[logs]) {
		t.Fatalf("terminals off and logs off: %v / %v", got[screens], got[logs])
	}
}

// A brief within the turn's grant is given whole, and reading it carries
// its footprint into the turn's.
func TestABriefWithinTheGrantIsGivenWhole(t *testing.T) {
	f := newObsFixture(t)
	early := f.keepEarlier(t, brakeman.DefaultConfig(f.root), []brakeman.Item{fact("the build failed", ref(t, "run:"+f.run))}, "One run failed.",
		[]brakeman.Read{{Ref: ref(t, "run:"+f.run), Class: brakeman.ClassRuns, At: time.Now()}})
	f.turn(t, nil)
	got := f.briefsOf(t)[early]
	if !whole(got) || got["summary"] != "One run failed." || got["title"] != "Overnight" {
		t.Fatalf("a brief within the grant: %v", got)
	}
	if rd, ok := f.readOf(t, "brief:"+early+"/0"); !ok || rd.Class != brakeman.ClassRuns {
		t.Fatalf("the brief's footprint carried: %+v %v", rd, ok)
	}
}

// A brief whose turn read a standalone session its scope named is
// withheld once the scope no longer names it.
func TestRemovingAStandaloneSessionWithholdsItsBrief(t *testing.T) {
	f := newObsFixture(t)
	cfg := brakeman.DefaultConfig(f.root)
	cfg.Scope = brakeman.Scope{Runs: []string{f.run}, Sessions: []string{f.plain}}
	early := f.keepEarlier(t, cfg, []brakeman.Item{fact("the session asked", ref(t, "session:"+f.plain))}, "It asked.",
		[]brakeman.Read{{Ref: ref(t, "session:"+f.plain), Class: brakeman.ClassSessions, At: time.Now()}})
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.run}} })
	if got := f.briefsOf(t)[early]; whole(got) {
		t.Fatalf("the session removed from the scope: %v", got)
	}
}

// Overlaps see what two clean work trees committed: the same file
// changed in both, each tree named.
func TestOverlapsSeeCommittedChanges(t *testing.T) {
	repo, a, b, base := twoWorktrees(t, t.TempDir(), false)
	trees := []overlapTree{{ref: "attempt:team-0000aaaa/build/1", dir: a, base: base}, {ref: "attempt:team-0000aaaa/fix/1", dir: b, base: base}}
	got := map[string][]string{}
	for _, tr := range trees {
		r, paths, _, err := treePaths(t.Context(), tr)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(r, repo) {
			t.Fatalf("the repository %q of %s", r, tr.dir)
		}
		for _, p := range paths {
			got[p] = append(got[p], tr.ref)
		}
	}
	if refs := got["shared.txt"]; len(refs) != 2 {
		t.Fatalf("the committed file in both clean trees: %v", got)
	}
	if refs := got["only-a.txt"]; len(refs) != 1 {
		t.Fatalf("a file of one tree: %v", got)
	}
}

// twoWorktrees makes a repository and two worktrees of it, each with a
// commit changing shared.txt (and one file of its own), clean, or with
// the change left uncommitted when dirty.
func twoWorktrees(t *testing.T, parent string, dirty bool) (repo, a, b, base string) {
	t.Helper()
	repo = filepath.Join(parent, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "shared.txt")
	gitIn(t, repo, "commit", "-q", "-m", "base")
	base = gitIn(t, repo, "rev-parse", "HEAD")
	a, b = filepath.Join(parent, "a"), filepath.Join(parent, "b")
	for i, dir := range []string{a, b} {
		gitIn(t, repo, "worktree", "add", "-q", "-b", fmt.Sprintf("w%d", i), dir, base)
		if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte(fmt.Sprintf("changed in %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := os.WriteFile(filepath.Join(dir, "only-a.txt"), []byte("a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if !dirty {
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "-m", "work")
		}
	}
	return repo, a, b, base
}

// Overlaps name both live sessions' trees and the path they both
// changed, read through the route.
func TestOverlapsNameBothTreesAndThePath(t *testing.T) {
	f := newObsFixture(t)
	_, a, b, _ := twoWorktrees(t, f.root, true)
	var ids []string
	for _, dir := range []string{a, b} {
		resp, out := f.launchIn(dir, filepath.Join(dir, ".marker"))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("a session in %s: %d %v", dir, resp.StatusCode, out)
		}
		ids = append(ids, out["id"].(string))
	}
	f.turn(t, nil)
	out := f.expectRead(t, "/api/brakeman/overlaps", http.StatusOK, "")
	for _, o := range out["overlaps"].([]any) {
		m := o.(map[string]any)
		if m["path"] != "shared.txt" {
			continue
		}
		between := fmt.Sprint(m["between"])
		if strings.Contains(between, "session:"+ids[0]) && strings.Contains(between, "session:"+ids[1]) {
			return
		}
	}
	t.Fatalf("shared.txt between both sessions: %v", out)
}

// The derived op ids fit Dispatch's and the ledger's patterns whatever
// the parts, and are the same for the same parts.
func TestDerivedOpIDsFitTheStoresPatterns(t *testing.T) {
	long := strings.Repeat("A", 32)
	for _, op := range []string{makeOp("br-123456789012345678", 99, long), edgeOp("br-1", 0, 19), deriveOp("act", "br-1", long)} {
		if len(op) != 32 || !dispatch.OpIDPattern.MatchString(op) || !crew.CommandIDPattern.MatchString(op) || !strings.HasPrefix(op, "bk") {
			t.Fatalf("%q", op)
		}
	}
}

// One brief's op id in two requests makes two relations: an edge's op id
// is its request's own.
func TestTwoRequestsWithOneBriefOpIDMakeTwoRelations(t *testing.T) {
	if edgeOp("br-1", 0, 1) == edgeOp("br-2", 0, 1) {
		t.Fatal("two requests' edges share an op id")
	}
	if makeOp("br-1", 0, "same") == makeOp("br-2", 0, "same") {
		t.Fatal("two requests' makes share an op id")
	}
}

// A make asked again derives the same op, whichever end of an edge comes
// last.
func TestARepeatedMakeDerivesTheSameOp(t *testing.T) {
	again := makeOp("br-1", 3, "op")
	if makeOp("br-1", 3, "op") != again || makeOp("br-1", 3, "op") == makeOp("br-1", 3, "op2") || makeOp("br-1", 3, "op") == makeOp("br-1", 4, "op") {
		t.Fatal("a make's op is not its parts'")
	}
	b := brakeman.Brief{Items: []brakeman.Item{
		{Kind: brakeman.ItemDraftTicket, DraftTicket: &brakeman.DraftTicket{Project: "CND", Title: "A", Blocks: []int{1}}},
		{Kind: brakeman.ItemDraftTicket, DraftTicket: &brakeman.DraftTicket{Project: "CND", Title: "B"}},
	}}
	if e := briefEdges("br-1", b); len(e) != 1 || e[0].Op != edgeOp("br-1", 0, 1) || e[0].From != 0 || e[0].To != 1 {
		t.Fatalf("the edges: %+v", e)
	}
}
