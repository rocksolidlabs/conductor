package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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

// laneRefusedRaw is laneRefused for a reply read whole.
func laneRefusedRaw(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	laneRefused(t, what, resp.StatusCode, raw, out)
}

// laneRefusedDoor calls the pattern with the token for the session sid,
// as callDoor does, and fails t unless it is refused 409 lane.
func (ce *compareEnv) laneRefusedDoor(t *testing.T, tok, pattern, sid string) {
	t.Helper()
	status, raw, out := ce.callDoor(t, tok, pattern, sid)
	laneRefused(t, pattern, status, raw, out)
}

// A door closed to a lane answers its token 409 lane without reading the
// body: a body past every bound is not refused as too large, and a body
// the test never finishes sending is answered while the test still holds
// its writer.
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
	for _, path := range []string{"/decisions", "/crew", "/ticket/comments"} {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute) // bounds a hang
		pr, pw := io.Pipe()
		req, err := http.NewRequestWithContext(ctx, "POST", ce.http.URL+"/api/sessions/"+ce.b+path, pr)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = 1 << 20
		req.Header.Set("Authorization", "Bearer "+ce.bTok)
		req.Header.Set("Content-Type", "application/json")
		go func() { _, _ = pw.Write([]byte(`{"body":"`)) }()
		resp, err := ce.client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if path == "/ticket/comments" {
			if resp.StatusCode != http.StatusConflict {
				t.Errorf("%s unfinished: %d", path, resp.StatusCode)
			}
		} else {
			laneRefusedRaw(t, path+" unfinished", resp)
		}
		resp.Body.Close()
		pw.CloseWithError(io.ErrUnexpectedEOF)
		cancel()
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
// answered as on main, a bearer the server does not know.
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
	if got := idp.askedTokens()[asked:]; !slices.Contains(got, ce.plainTok) {
		t.Fatalf("the provider was not asked about an ordinary member's token: %d", len(got))
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

// A lane's guide names no door the table refuses, in its operations or
// in its limits, and not the ticket's comment; its ticket_read says what a
// lane's read holds. A comparison's task.md offers no comment on the
// ticket. An ordinary run's member's guide names the decision tools, its
// own link and its limits.
func TestALanesGuideAndTaskNameOnlyItsDoors(t *testing.T) {
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
		if route, ok := limitRoutes[name]; ok && (compareDoors[route] == doorRefused || route == laneCommentPattern) {
			t.Errorf("the lane's guide names the limit %q", v)
		}
	}
	section := ce.expectAs(t, ce.bTok, "GET", "/api/sessions/"+ce.b+"/guide?section=operations", nil, http.StatusOK, "")
	var read map[string]any
	for _, op := range section["operations"].([]any) {
		if m := op.(map[string]any); m["name"] == "ticket_read" {
			read = m
		}
	}
	if read == nil || read["summary"] != laneTicketReadWords || len(laneTicketReadWords) > maxOpSummary {
		t.Fatalf("the lane's ticket_read: %v", read)
	}
	plain := entityOf(t, ce.expectAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/guide", nil, http.StatusOK, ""), "overview")
	limits := fmt.Sprint(plain["limits"])
	if !strings.Contains(fmt.Sprint(plain["may"]), "decision_request") || !strings.Contains(limits, "self_links:") || !strings.Contains(limits, "comment:") {
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
