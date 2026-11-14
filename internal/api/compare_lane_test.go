package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// laneDoors is every door a session of a comparison's lane must have, as
// it calls each, written out here and not read from compareDoors: the
// guide, its run and its ticket, its task, its inbox, a message to its
// lane, the inbox's acknowledgement, a report, ask, a link opened for the
// person, a checkpoint, a blocker and, last, the task's result.
var laneDoors = []struct {
	pattern string
	body    any
}{
	{"GET /api/sessions/{id}/guide", nil},
	{"GET /api/sessions/{id}/run", nil},
	{"GET /api/sessions/{id}/ticket", nil},
	{"GET /api/sessions/{id}/task", nil},
	{"GET /api/sessions/{id}/inbox", nil},
	{"POST /api/sessions/{id}/messages", map[string]any{"to": "rev-b", "text": "ready soon"}},
	{"POST /api/sessions/{id}/inbox/ack", map[string]any{"upTo": 1}},
	{"POST /api/sessions/{id}/events", map[string]any{"type": "progress", "message": "halfway"}},
	{"POST /api/sessions/{id}/attention", map[string]any{"state": "needs_input", "message": "Which index?", "ask": true}},
	{"POST /api/sessions/{id}/open", map[string]any{"url": "https://example.test/"}},
	{"POST /api/sessions/{id}/task/checkpoint", map[string]any{"done": "the index", "remaining": "the tests"}},
	{"POST /api/sessions/{id}/task/blocked", map[string]any{"reason": "the data is missing", "message": "Where is it?"}},
	{"POST /api/sessions/{id}/task/result", map[string]any{"summary": "indexed"}},
}

// Every door a lane must have is served to its own session's token, each
// called with a body it takes and answered 2xx, and the identity provider
// is asked about no token on the way: neither on these nor on a door the
// table refuses. The table opens these doors and the ticket's comment and
// no other.
func TestALaneIsServedEveryDoorItMustHave(t *testing.T) {
	idp := &tokenIdentity{}
	ce := newCompareEnv(t, compareOpts{identity: idp})
	asked := len(idp.askedTokens())
	open := []string{laneCommentPattern}
	for _, d := range laneDoors {
		method, path, _ := strings.Cut(strings.ReplaceAll(d.pattern, "{id}", ce.b), " ")
		status, raw, _ := ce.callAs(t, ce.bTok, method, path, d.body)
		if status < 200 || status > 299 {
			t.Errorf("%s: approach-b's own token: %d %.300s", d.pattern, status, raw)
		}
		open = append(open, d.pattern)
	}
	for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
		if compareDoors[p] == doorRefused {
			ce.laneRefusedDoor(t, ce.bTok, p, ce.b)
		}
	}
	if got := idp.askedTokens()[asked:]; len(got) != 0 {
		t.Fatalf("the provider was asked %d times", len(got))
	}
	var served []string
	for p, rule := range compareDoors {
		if rule != doorRefused {
			served = append(served, p)
		}
	}
	slices.Sort(served)
	slices.Sort(open)
	if !slices.Equal(served, slices.Compact(open)) {
		t.Fatalf("the table opens %v to a lane, not %v", served, slices.Compact(open))
	}
}

// laneRefusedDoor calls the pattern with the token for the session sid,
// as callDoor does, and fails t unless it is refused 409 lane.
func (ce *compareEnv) laneRefusedDoor(t *testing.T, tok, pattern, sid string) {
	t.Helper()
	status, raw, out := ce.callDoor(t, tok, pattern, sid)
	laneRefused(t, pattern, status, raw, out)
}

// bodySource is a request's body that records every read made of it and
// gives nothing: a read waits until the request's context ends, which
// closes the source.
type bodySource struct {
	ctx     context.Context
	reading chan struct{}
	once    sync.Once
	reads   atomic.Int64
}

func (b *bodySource) Read([]byte) (int, error) {
	b.reads.Add(1)
	b.once.Do(func() { close(b.reading) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

// A door closed to a lane answers its token 409 lane without reading the
// body: a body past every bound is not refused as too large, and the
// handler answers with nothing read from the body's source. The crew
// route answers the admin's token on a lane's session the same way,
// before its body is read. A handler that reads fails the test at its
// first read; the request's context then ends, closing the source, so it
// returns.
func TestALaneIsRefusedBeforeItsBodyIsRead(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	big := bytes.Repeat([]byte(" "), 8<<20)
	for _, path := range []string{"/decisions", "/crew", "/ticket/comments"} {
		status, raw, out := ce.callAs(t, ce.bTok, "POST", "/api/sessions/"+ce.b+path, big)
		if path == "/ticket/comments" {
			if status != http.StatusConflict || errorCode(out) != "lane" {
				t.Errorf("%s past its bound: %d %.300s", path, status, raw)
			}
			continue
		}
		laneRefused(t, path+" past its bound", status, raw, out)
	}
	for _, c := range []struct{ who, tok, path string }{
		{"the lane's token", ce.bTok, "/decisions"},
		{"the lane's token", ce.bTok, "/crew"},
		{"the lane's token", ce.bTok, "/ticket/comments"},
		{"the admin's token", adminToken, "/crew"},
	} {
		path := c.path + " to " + c.who
		ctx, cancel := context.WithCancel(t.Context())
		src := &bodySource{ctx: ctx, reading: make(chan struct{})}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/sessions/"+ce.b+c.path, src)
		req.ContentLength = 1 << 20
		req.Header.Set("Authorization", "Bearer "+c.tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		served := make(chan struct{})
		go func() {
			defer close(served)
			ce.http.Config.Handler.ServeHTTP(rec, req)
		}()
		select {
		case <-served:
		case <-src.reading:
			t.Errorf("%s: the handler read the body before it answered", path)
		}
		cancel()
		<-served
		if n := src.reads.Load(); n != 0 {
			t.Errorf("%s: %d reads of the body", path, n)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if c.path == "/ticket/comments" {
			if rec.Code != http.StatusConflict || errorCode(out) != "lane" {
				t.Errorf("%s with its body unread: %d %.300s", path, rec.Code, rec.Body)
			}
			continue
		}
		laneRefused(t, path+" with its body unread", rec.Code, rec.Body.Bytes(), out)
	}
}

// While a comparison's launch is held, after its session is made and
// before the engine records it, the admin's token asks to form a crew
// around the lane's session: 409 lane, no crew saved, no run made, the
// session's lane as it was; once the launch goes on, the lane's own token
// reads its lane's run and ticket, and is refused a crew in turn.
func TestALaneIsNotAdoptedWhileItsLaunchIsHeld(t *testing.T) {
	held := make(chan string, 1)
	release := make(chan struct{})
	ce := newCompareEnv(t, compareOpts{launched: func(spec crew.LaunchSpec, id string) {
		if spec.Ref.Attempt == 1 && spec.Ref.Task == "approach-b" {
			held <- id
			<-release
		}
	}})
	var once bool
	unhold := func() {
		if !once {
			once = true
			close(release)
		}
	}
	t.Cleanup(unhold)
	b := <-held
	d, _ := ce.srv.registry.Get(b)
	mark := *d.Info().Crew
	if mark.Lane == nil || mark.Lane.Candidate != "approach-b" {
		t.Fatalf("the held session's reference: %+v", mark)
	}
	_, crews, err := ce.srv.crews.List(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	runs := len(ce.srv.runs.List())
	manual := map[string]any{"when": "manual"}
	body := map[string]any{"name": "Mine", "goal": "g", "self": "me", "members": []any{map[string]any{"name": "me", "start": manual}, map[string]any{"name": "x", "agentId": "ready-cat", "start": manual}}}
	status, raw, out := ce.callAs(t, adminToken, "POST", "/api/sessions/"+b+"/crew", body)
	laneRefused(t, "the admin's crew on the held session", status, raw, out)
	if _, n, err := ce.srv.crews.List(0, 1000); err != nil || n != crews {
		t.Fatalf("crews: %d, not %d (%v)", n, crews, err)
	}
	if n := len(ce.srv.runs.List()); n != runs {
		t.Fatalf("runs: %d, not %d", n, runs)
	}
	if got := d.Info().Crew; got == nil || !reflect.DeepEqual(*got, mark) || *got.Lane != *mark.Lane {
		t.Fatalf("the session's reference: %+v, not %+v", got, mark)
	}
	unhold()
	runningAttempt(t, ce.testEnv, ce.run, "approach-b", 1)
	tok := ce.agentToken(b)
	_, raw, out = ce.callAs(t, tok, "GET", "/api/sessions/"+b+"/run", nil)
	if run, _ := out["run"].(map[string]any); run == nil || !slices.Equal(objectKeys(run), []string{"compare", "goal", "id", "members", "name", "tasks"}) ||
		!slices.Equal(fieldsOf(run["tasks"], "id"), []string{"approach-b", "review-b"}) {
		t.Fatalf("the lane's run: %s", raw)
	}
	if _, out := ce.laneTicketOf(t, tok, b); outsideWhitelist(out) != nil {
		t.Fatalf("the lane's ticket: %v", out)
	}
	for _, who := range []string{tok, adminToken} {
		status, raw, out := ce.callAs(t, who, "POST", "/api/sessions/"+b+"/crew", body)
		laneRefused(t, "a crew on the running lane", status, raw, out)
	}
	if got := d.Info().Crew; got == nil || !reflect.DeepEqual(*got, mark) {
		t.Fatalf("the session's reference once running: %+v", got)
	}
}

// On a server whose Brakeman desk is not there, a lane's own token naming
// its session on Brakeman's shared reads and acts is answered 409 lane and
// never put to the identity provider; an ordinary run's member's token is
// answered as on main, 401, and as a live token of the server's own is
// never put to the provider either (ownToken).
func TestALaneIsRefusedWhereBrakemansDeskIsNotThere(t *testing.T) {
	idp := &tokenIdentity{}
	ce := newCompareEnv(t, compareOpts{identity: idp, noDesk: true})
	var patterns []string
	for _, sr := range brakemanReads {
		patterns = append(patterns, sr.pattern)
	}
	patterns = append(patterns, slices.Sorted(maps.Keys(actRoutes))...)
	asked := len(idp.askedTokens())
	for _, p := range patterns {
		ce.laneRefusedDoor(t, ce.bTok, p, ce.b)
	}
	if got := idp.askedTokens()[asked:]; len(got) != 0 {
		t.Fatalf("the provider was asked %d times", len(got))
	}
	method, path, body := ce.door("GET /api/runs", ce.member, ce.plain)
	if status, raw, _ := ce.callAsTurn(t, ce.plainTok, ce.member, method, path, body); status != http.StatusUnauthorized {
		t.Fatalf("an ordinary member's token on a shared read: %d %.300s", status, raw)
	}
	if got := idp.askedTokens()[asked:]; len(got) != 0 {
		t.Fatalf("the provider was asked %d times about an ordinary member's token", len(got))
	}
}

// A lane's view of its run, read after the whole run has ended, holds no
// state or outcome of the run: its keys are the lane view's.
func TestALanesViewOfAnEndedRunHasNoOutcome(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	if err := ce.srv.runs.Stop(t.Context(), ce.run); err != nil {
		t.Fatal(err)
	}
	// Stop returns once the run has ended.
	if r, ok := ce.srv.runs.Get(ce.run); !ok || r.State == crew.RunRunning {
		t.Fatalf("the stopped run: %+v", r)
	}
	for _, tok := range []string{ce.bTok, adminToken} {
		status, raw, out := ce.callAs(t, tok, "GET", "/api/sessions/"+ce.b+"/run", nil)
		run, _ := out["run"].(map[string]any)
		if status != http.StatusOK || !slices.Equal(objectKeys(run), []string{"compare", "goal", "id", "members", "name", "tasks"}) || strings.Contains(string(raw), `"outcome"`) {
			t.Fatalf("the lane's view of the ended run: %d %s", status, raw)
		}
	}
}

// laneLimitRoutes is the route each limit of the guide's overview names,
// written out here and not read from limitRoutes: a limit with no entry
// names no route.
var laneLimitRoutes = map[string]string{
	"report_message": "POST /api/sessions/{id}/attention",
	"self_crews":     "POST /api/sessions/{id}/crew",
	"self_links":     "POST /api/sessions/{id}/links/agent",
	"message":        "POST /api/sessions/{id}/messages",
	"inbox":          "GET /api/sessions/{id}/inbox",
	"result_summary": "POST /api/sessions/{id}/task/result",
	"result_issues":  "POST /api/sessions/{id}/task/result",
	"comment":        "POST /api/sessions/{id}/ticket/comments",
}

// A lane's guide names no door the table refuses, in its operations or
// in its limits, and not the ticket's comment; its overview names only
// the limits of routes it is served, each limit's route as
// laneLimitRoutes has it; its ticket_read, run_status, message_send and
// inbox_read say what each gives a lane. A comparison's task.md offers
// no comment on the ticket. An ordinary run's member's guide names the
// decision tools, its own link, its limits and a body's bound, and says
// of each operation what it always has.
func TestALanesGuideAndTaskNameOnlyItsDoors(t *testing.T) {
	if !maps.Equal(limitRoutes, laneLimitRoutes) {
		t.Fatalf("the limits' routes: %v, want %v", limitRoutes, laneLimitRoutes)
	}
	ce := newCompareEnv(t, compareOpts{})
	ops := map[string]string{}
	for _, op := range operationsTable() {
		ops[op.Name] = op.Method + " " + op.Path
	}
	ov := entityOf(t, ce.expectAs(t, ce.bTok, "GET", "/api/sessions/"+ce.b+"/guide", nil, http.StatusOK, ""), "overview")
	for _, v := range ov["may"].([]any) {
		if p := ops[v.(string)]; compareDoors[p] == doorRefused || p == laneCommentPattern {
			t.Errorf("the lane's guide lists %s (%s)", v, p)
		}
	}
	for _, v := range ov["limits"].([]any) {
		name, _, _ := strings.Cut(v.(string), ":")
		if route, ok := laneLimitRoutes[name]; !ok || compareDoors[route] == doorRefused || route == laneCommentPattern {
			t.Errorf("the lane's guide names the limit %q, of no route it is served", v)
		}
	}
	summaries := func(tok, sid string) map[string]any {
		out := map[string]any{}
		section := ce.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide?section=operations", nil, http.StatusOK, "")
		for _, op := range section["operations"].([]any) {
			m := op.(map[string]any)
			out[m["name"].(string)] = m["summary"]
		}
		return out
	}
	lane, ordinary := summaries(ce.bTok, ce.b), summaries(ce.plainTok, ce.member)
	for name, words := range laneSummaries {
		if lane[name] != words || len(words) > maxOpSummary {
			t.Errorf("the lane's %s: %v, want %q", name, lane[name], words)
		}
		if s, ok := ordinary[name]; !ok || s == words {
			t.Errorf("an ordinary member's %s: %v", name, s)
		}
	}
	plain := entityOf(t, ce.expectAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/guide", nil, http.StatusOK, ""), "overview")
	limits := fmt.Sprint(plain["limits"])
	if !strings.Contains(fmt.Sprint(plain["may"]), "decision_request") || !strings.Contains(limits, "self_links:") || !strings.Contains(limits, "comment:") || !strings.Contains(limits, "body:") {
		t.Fatalf("an ordinary member's guide: %v", plain)
	}
	worktree := runningAttempt(t, ce.testEnv, ce.run, "approach-b", 1)["worktree"].(string)
	md, err := os.ReadFile(filepath.Join(worktree, ".conductor", "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "`task_blocked` with why, or in your result's summary.") || strings.Contains(strings.ToLower(string(md)), "comment") {
		t.Fatalf("approach-b's task.md:\n%s", md)
	}
}

// A lane's ticket reply, as the server answers it for a comparison
// launched from CND-1, has no status in its ticket, whichever token asks;
// an ordinary run's member's reply has one, for a run linked to CND-1 and
// for one launched from CND-2. The CLI tells a lane's reply by that.
func TestALanesTicketReplyHasNoStatus(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	for _, tok := range []string{ce.bTok, adminToken} {
		raw, out := ce.laneTicketOf(t, tok, ce.b)
		if tk, _ := out["ticket"].(map[string]any); out["launch"] == nil || slices.Contains(objectKeys(tk), "status") {
			t.Fatalf("the lane's ticket has a status: %s", raw)
		}
	}
	ce.newDispatchTicket(t, "CND", "Slow lookup", map[string]any{"description": "Keep it simple."})
	saved, err := ce.srv.crews.Create(managedCrew(filepath.Join(ce.root, "repo")))
	if err != nil {
		t.Fatal(err)
	}
	plan := entityOf(t, ce.expect(t, "POST", "/api/dispatch/tickets/CND-2/plans", map[string]any{"opId": opID(), "crew": saved.ID}, http.StatusCreated, ""), "plan")["id"].(string)
	ce.expect(t, "POST", "/api/dispatch/plans/"+plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	launched := entityOf(t, ce.expect(t, "POST", "/api/dispatch/tickets/CND-2/launch", map[string]any{"opId": opID(), "plan": plan}, http.StatusCreated, ""), "launch")
	sid, tok := ce.session(t, launched["runId"].(string), "build")
	for _, s := range []struct{ sid, tok, what string }{
		{ce.member, ce.plainTok, "linked"}, {ce.member, adminToken, "linked"},
		{sid, tok, "launched"}, {sid, adminToken, "launched"},
	} {
		status, raw, out := ce.callAs(t, s.tok, "GET", "/api/sessions/"+s.sid+"/ticket", nil)
		tk, _ := out["ticket"].(map[string]any)
		if st, _ := tk["status"].(string); status != http.StatusOK || st == "" || (out["launch"] != nil) != (s.what == "launched") {
			t.Fatalf("a member's ticket of an ordinary run %s: %d %.300s", s.what, status, raw)
		}
	}
}

// A lane is listed ticket_read when its run was launched from a ticket,
// and only then: the lane of a comparison launched without one is not
// listed it once a person links its run to the ticket, and its read
// answers 409 lane; the launched lane is listed it.
func TestALaneIsListedTicketReadForItsLaunchAlone(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	c := compareCrewIn(t, filepath.Join(ce.root, "repo"))
	c.ID = "unlaunched"
	run, release, err := ce.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/links", map[string]any{"opId": opID(), "kind": "run", "ref": run.ID}, http.StatusCreated, "")
	sid, tok := ce.session(t, run.ID, "approach-b")
	may := func(tok, sid string) string {
		return fmt.Sprint(entityOf(t, ce.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide", nil, http.StatusOK, ""), "overview")["may"])
	}
	if got := may(tok, sid); strings.Contains(got, "ticket_read") {
		t.Errorf("the linked lane's guide lists ticket_read: %s", got)
	}
	status, raw, out := ce.callAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil)
	laneRefused(t, "the linked lane's ticket read", status, raw, out)
	if got := may(ce.bTok, ce.b); !strings.Contains(got, "ticket_read") {
		t.Fatalf("the launched lane's guide: %s", got)
	}
}
