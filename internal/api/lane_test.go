package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// A decision link's lane (lane.go; docs/plans/decision-inbox.md, PR-5):
// what one link allows is held here, each refusal at the server. The
// tests wait on the state they need (a session's attention, a task's, a
// receipt in the ledger, the seams the lane offers), never on the clock.

var _ hostagent.Decisions = (*Lane)(nil)

// laneClient is a decision link's connection as a test drives it: a
// session's viewer or a run's desk.
type laneClient struct {
	*wsClient
	n       int
	changed int // decisions_changed seen while waiting for a reply
	welcome map[string]any
	// early keeps the replies read while waiting for another's, by reqId:
	// requests under way together are answered in any order.
	early map[string]laneReply
}

// laneReply is a reply read: a decision, or a control.
type laneReply struct {
	dec *proto.Decision
	ctl map[string]any
}

// dialLane opens path (/ws/sessions/{id} or /ws/runs/{run}/desk) with
// token, says hello, and reads the welcome.
func dialLane(t *testing.T, e *testEnv, path, token string) *laneClient {
	t.Helper()
	c, err := dialWS(e, path, token)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { c.CloseNow() })
	w := &laneClient{wsClient: &wsClient{t: t, c: c}}
	w.hello(0, 0)
	w.welcome = w.expectControl(proto.CtlWelcome)
	return w
}

// dialWS dials path with token, its frames read at their bound.
func dialWS(e *testEnv, path, token string) (*websocket.Conn, error) {
	url := strings.Replace(e.http.URL, "http://", "ws://", 1) + path + "?token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(proto.MaxFrame)
	return c, nil
}

// closedWith dials path with token, says hello, and reads until the
// server closes: its close code.
func closedWith(t *testing.T, e *testEnv, path, token string) websocket.StatusCode {
	t.Helper()
	c, err := dialWS(e, path, token)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer c.CloseNow()
	_ = c.Write(t.Context(), websocket.MessageBinary, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1}))
	for {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		_, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			return websocket.CloseStatus(err)
		}
		if f, _ := proto.Decode(data); f.Type == proto.TypeControl {
			var m map[string]any
			_ = json.Unmarshal(f.Payload, &m)
			if m["t"] == proto.CtlWelcome {
				t.Fatalf("%s let in: %v", path, m)
			}
		}
	}
}

// reqID is a new request id.
func (w *laneClient) reqID() string {
	w.n++
	return "r" + strconv.Itoa(w.n)
}

// reply reads frames until the reply naming reqID: a decision frame, or
// a decide_ok or decide_refused control (ctl). It counts the
// decisions_changed it passes.
func (w *laneClient) reply(reqID string) (dec *proto.Decision, ctl map[string]any) {
	w.t.Helper()
	if r, ok := w.early[reqID]; ok {
		delete(w.early, reqID)
		return r.dec, r.ctl
	}
	if w.early == nil {
		w.early = map[string]laneReply{}
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		_, data, err := w.c.Read(ctx)
		cancel()
		if err != nil {
			w.t.Fatalf("waiting for the reply to %s: %v", reqID, err)
		}
		f, err := proto.Decode(data)
		if err != nil {
			w.t.Fatalf("a frame: %v", err)
		}
		switch f.Type {
		case proto.TypeDecision:
			var d proto.Decision
			if err := json.Unmarshal(f.Payload, &d); err != nil {
				w.t.Fatal(err)
			}
			if d.ReqID == reqID {
				return &d, nil
			}
			w.early[d.ReqID] = laneReply{dec: &d}
		case proto.TypeControl:
			var m map[string]any
			_ = json.Unmarshal(f.Payload, &m)
			if m["t"] == proto.CtlDecisionsChanged {
				w.changed++
			}
			if m["t"] == proto.CtlDecideOK || m["t"] == proto.CtlDecideRefused {
				if m["reqId"] == reqID {
					return nil, m
				}
				id, _ := m["reqId"].(string)
				w.early[id] = laneReply{ctl: m}
			}
		}
	}
}

// ask sends a lane read and reads its decision, failing t on a refusal.
func (w *laneClient) ask(v any, reqID string) *proto.Decision {
	w.t.Helper()
	w.send(proto.MustControl(v))
	d, ctl := w.reply(reqID)
	if d == nil {
		w.t.Fatalf("refused: %v", ctl)
	}
	return d
}

// refusedRead sends a lane read and answers its refusal's code.
func (w *laneClient) refusedRead(v any, reqID string) string {
	w.t.Helper()
	w.send(proto.MustControl(v))
	d, ctl := w.reply(reqID)
	if d != nil {
		w.t.Fatalf("answered: %+v", d)
	}
	return ctl["code"].(string)
}

func (w *laneClient) list(after string) laneList {
	w.t.Helper()
	id := w.reqID()
	d := w.ask(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: id, After: after}, id)
	var out laneList
	if err := json.Unmarshal(d.List, &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *laneClient) read(ref string) inboxItem {
	w.t.Helper()
	id := w.reqID()
	d := w.ask(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: id, Ref: ref}, id)
	var it inboxItem
	if err := json.Unmarshal(d.Item, &it); err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *laneClient) track(ref, receipt, attempt string) map[string]deliveryStep {
	w.t.Helper()
	id := w.reqID()
	d := w.ask(proto.DecisionTrack{T: proto.CtlDecisionTrack, ReqID: id, Ref: ref, Receipt: receipt, Attempt: attempt}, id)
	var out laneTrack
	if err := json.Unmarshal(d.Track, &out); err != nil {
		w.t.Fatal(err)
	}
	if len(out.Steps) != 4 {
		w.t.Fatalf("steps: %+v", out.Steps)
	}
	m := map[string]deliveryStep{}
	for _, s := range out.Steps {
		m[s.Step] = s
	}
	return m
}

func (w *laneClient) evidence(ref, ev string) (laneEvidence, map[string]any) {
	w.t.Helper()
	id := w.reqID()
	w.send(proto.MustControl(proto.DecisionEvidence{T: proto.CtlDecisionEvidence, ReqID: id, Ref: ref, Evidence: ev}))
	d, ctl := w.reply(id)
	var out laneEvidence
	if d != nil {
		if err := json.Unmarshal(d.Evidence, &out); err != nil {
			w.t.Fatal(err)
		}
	}
	return out, ctl
}

// sendDecide sends m as a decide, its reqId new, and answers the reqId.
func (w *laneClient) sendDecide(m proto.Decide) string {
	w.t.Helper()
	m.ReqID = w.reqID()
	f, err := proto.EncodeDecide(m)
	if err != nil {
		w.t.Fatalf("decide %+v: %v", m, err)
	}
	w.send(f)
	return m.ReqID
}

// decide sends m and reads its decide_ok or decide_refused.
func (w *laneClient) decide(m proto.Decide) map[string]any {
	w.t.Helper()
	id := w.sendDecide(m)
	_, ctl := w.reply(id)
	return ctl
}

// retried takes the task's retry through the link at the revision its
// item reads now. A run that moved meanwhile (its trees still ending, each
// a transition) refuses it stale_revision, kept as that opId's receipt,
// and the item is read again, as the page does: the opId taken and the
// Expect it was taken at.
func (w *laneClient) retried(ref, prefix string) (opID, expect string) {
	w.t.Helper()
	for i := 1; ; i++ {
		retry := actionOf(w.t, w.read(ref), crew.TaskRetry)
		id := prefix + "-" + strconv.Itoa(i)
		m := w.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: id})
		if isOK(m) {
			if m["receipt"] != id || m["ref"] != ref || m["repeat"] != nil {
				w.t.Fatalf("the retry: %v", m)
			}
			return id, retry.Expect
		}
		if !refusedWith(m, proto.DecideStaleRevision) || i == 20 {
			w.t.Fatalf("the retry: %v", m)
		}
	}
}

// waitChanged reads until a decisions_changed arrives (or one passed
// while a reply was read).
func (w *laneClient) waitChanged() {
	w.t.Helper()
	if w.changed > 0 {
		w.changed--
		return
	}
	w.expectControl(proto.CtlDecisionsChanged)
}

// failedRun is failBuild's run once it has settled: build failed, the run
// done (its outcome set), so the run's revision stays as an item reads it
// until a command moves it.
func failedRun(t *testing.T, e *testEnv) string {
	t.Helper()
	run := failBuild(t, e, failingCrew(e.root))
	until(t, "the run settled", func() bool {
		r, ok := e.srv.runs.Get(run)
		return ok && r.Outcome != ""
	})
	return run
}

// decisionLink mints a decision link at path (a session's or a run's
// links route): its id and token.
func (e *testEnv) decisionLink(t *testing.T, path, label string, ttl int64) (string, string) {
	t.Helper()
	resp, out := e.do("POST", path, adminToken, map[string]any{"role": "control", "decide": true, "label": label, "ttlSeconds": ttl})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a decision link at %s: %d %v", path, resp.StatusCode, out)
	}
	l := out["link"].(map[string]any)
	if l["decide"] != true {
		t.Fatalf("the link: %v", l)
	}
	return l["id"].(string), out["token"].(string)
}

// isOK and refusedWith read a decide's reply.
func isOK(m map[string]any) bool { return m["t"] == proto.CtlDecideOK }
func refusedWith(m map[string]any, code string) bool {
	return m["t"] == proto.CtlDecideRefused && m["code"] == code
}

// A decision link is a control link: a view one is refused 400
// bad_request, nothing made; a control one says decide in its reply and
// in its list, its creation in the activity.
func TestADecisionLinkNeedsControl(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("phone")
	newFakeSession(t, e, id, session.Options{})
	for _, path := range []string{"/api/sessions/" + id + "/links"} {
		resp, out := e.do("POST", path, adminToken, map[string]any{"role": "view", "decide": true})
		if resp.StatusCode != http.StatusBadRequest || errCodeOf(out) != "bad_request" {
			t.Fatalf("a view decision link: %d %v", resp.StatusCode, out)
		}
	}
	if links := e.srv.links.ListBySession(id); len(links) != 0 {
		t.Fatalf("made: %+v", links)
	}
	e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	resp, out := e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
	links, _ := out["links"].([]any)
	if resp.StatusCode != http.StatusOK || len(links) != 1 || links[0].(map[string]any)["decide"] != true {
		t.Fatalf("the list: %d %v", resp.StatusCode, out)
	}
	e2 := readyEnv(t, nil)
	e2.gitRepo(t)
	run, release, err := e2.srv.runs.LaunchNamed(t.Context(), managedCrew(e2.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	if resp, out := e2.do("POST", "/api/runs/"+run.ID+"/links", adminToken, map[string]any{"role": "view", "decide": true}); resp.StatusCode != http.StatusBadRequest || errCodeOf(out) != "bad_request" {
		t.Fatalf("a view run decision link: %d %v", resp.StatusCode, out)
	}
	e2.decisionLink(t, "/api/runs/"+run.ID+"/links", "", 0)
}

// A decision link's viewer is welcomed with decisions; a run's desk is
// too, with no terminal fields; a plain link's viewer is not.
func TestWelcomeSaysDecisions(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("welcome")
	newFakeSession(t, e, id, session.Options{})
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	if w := dialLane(t, e, "/ws/sessions/"+id, tok); w.welcome["decisions"] != true || w.welcome["role"] != "control" {
		t.Fatalf("the welcome: %v", w.welcome)
	}
	resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(out)
	}
	if w := dialLane(t, e, "/ws/sessions/"+id, out["token"].(string)); w.welcome["decisions"] != nil {
		t.Fatalf("a plain link's welcome: %v", w.welcome)
	}
}

// A control link without decide is welcomed without decisions, and every
// lane request it sends, read or decide, is refused not_decision_link;
// its connection stays.
func TestAControlLinkWithoutDecideReceivesNone(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("plain")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	resp, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(out)
	}
	w := dialLane(t, e, "/ws/sessions/"+id, out["token"].(string))
	if w.welcome["decisions"] != nil {
		t.Fatalf("the welcome: %v", w.welcome)
	}
	rid := w.reqID()
	if code := w.refusedRead(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: rid}, rid); code != proto.DecideNotDecisionLink {
		t.Fatalf("a list: %s", code)
	}
	if m := w.decide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "op-1"}); !refusedWith(m, proto.DecideNotDecisionLink) {
		t.Fatalf("a decide: %v", m)
	}
	// The workbench's own connection is no decision link either.
	admin := dialLane(t, e, "/ws/sessions/"+id, adminToken)
	if m := admin.decide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "op-2"}); !refusedWith(m, proto.DecideNotDecisionLink) {
		t.Fatalf("the admin's decide: %v", m)
	}
	if got := fs.written(); len(got) != 0 {
		t.Fatalf("typed: %q", got)
	}
	w.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 7}))
	if pong := w.expectControl(proto.CtlPong); pong["ts"] != float64(7) {
		t.Fatalf("the connection after: %v", pong)
	}
}

// A run's link sees the run's items and nothing else: not a session's
// question outside the run, not another run's task; a session's link its
// session's question alone. A ref outside the scope is refused
// not_in_scope, read or acted on.
func TestADecisionLinkReceivesItsScope(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	out := sessionHex("outside")
	fs := newFakeSession(t, e, out, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	_, runTok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", runTok)
	if desk.welcome["decisions"] != true || desk.welcome["sessionId"] != "" {
		t.Fatalf("the desk's welcome: %v", desk.welcome)
	}
	page := desk.list("")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Ref != "task:"+run+"/build" {
		t.Fatalf("the run's items: %+v", page)
	}
	for _, ref := range []string{questionRef(out, q), "task:other-0000abcd/build", "plan:CND-1/1"} {
		rid := desk.reqID()
		if code := desk.refusedRead(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: rid, Ref: ref}, rid); code != proto.DecideNotInScope {
			t.Fatalf("%s read: %s", ref, code)
		}
	}
	if m := desk.decide(proto.Decide{Ref: questionRef(out, q), Action: "answer", Revision: q, Input: "1", OpID: "op-1"}); !refusedWith(m, proto.DecideNotInScope) {
		t.Fatalf("an answer outside: %v", m)
	}
	_, sessTok := e.decisionLink(t, "/api/sessions/"+out+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+out, sessTok)
	if page := w.list(""); page.Total != 1 || page.Items[0].Ref != questionRef(out, q) {
		t.Fatalf("the session's items: %+v", page)
	}
	rid := w.reqID()
	if code := w.refusedRead(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: rid, Ref: "task:" + run + "/build"}, rid); code != proto.DecideNotInScope {
		t.Fatalf("the run's task through the session's link: %s", code)
	}
	if got := fs.written(); len(got) != 0 {
		t.Fatalf("typed: %q", got)
	}
}

// The lane's pages: a run of 60 failed tasks (made in memory: the paging
// is the same whoever made them) over three pages of 25, 25 and 10, total
// 60 on each, none lost or repeated; one resolved between pages absent
// from the next, one arriving later seen in its place.
func TestADecisionLinkPagesItsScope(t *testing.T) {
	at := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	var all []*inboxItem
	for i := range 60 {
		all = append(all, &inboxItem{Ref: fmt.Sprintf("task:run-1/t%02d", i), Since: at.Add(time.Duration(i%7) * time.Second)})
	}
	slices.SortFunc(all, func(a, b *inboxItem) int { return a.before(inboxCursor{b.Since, b.Ref}) })
	seen := map[string]bool{}
	var after inboxCursor
	var sizes []int
	resolved := ""
	for {
		page, next := lanePage(all, after)
		sizes = append(sizes, len(page))
		for _, it := range page {
			if seen[it.Ref] {
				t.Fatalf("%s twice", it.Ref)
			}
			if it.Ref == resolved {
				t.Fatalf("%s resolved and listed", it.Ref)
			}
			seen[it.Ref] = true
		}
		if next == "" {
			break
		}
		if after, _ = parseCursor(next); after.ref == "" {
			t.Fatalf("next %q", next)
		}
		if resolved == "" {
			// The last item of the order resolved between pages.
			resolved = all[len(all)-1].Ref
			all = all[:len(all)-1]
		}
	}
	if !slices.Equal(sizes, []int{25, 25, 9}) || len(seen) != 59 {
		t.Fatalf("pages %v, %d seen", sizes, len(seen))
	}
}

// A retry through a run's decision link is the link's: the ledger keeps
// its receipt under link:<id>, the lane's track reads that receipt, and
// the admin's track, under its own actor, keeps none of it. The same
// opId again is answered with that receipt, repeat, nothing done twice.
func TestDecideRetryRecordsTheLinkAsActor(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	op, expect := desk.retried(ref, "retry")
	row, found, err := e.srv.ledger.Receipt(t.Context(), run, "link:"+linkID, op)
	if err != nil || !found || row.Intent != crew.TaskRetry {
		t.Fatalf("the ledger's receipt: %+v %v %v", row, found, err)
	}
	if _, found, _ := e.srv.ledger.Receipt(t.Context(), run, adminActor, op); found {
		t.Fatal("kept as the admin's")
	}
	if steps := desk.track(ref, op, op); steps[stepRecorded].State != stepDone || steps[stepValidated].State != stepDone {
		t.Fatalf("the link's steps: %+v", steps)
	}
	if steps := e.trackOf(t, ref, op, ""); steps[stepRecorded].State != stepUnknown {
		t.Fatalf("the admin's steps of the link's command: %+v", steps)
	}
	runningAttempt(t, e, run, "build", 4)
	again := desk.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: expect, OpID: op})
	if !isOK(again) || again["receipt"] != op || again["repeat"] != true {
		t.Fatalf("the same opId again: %v", again)
	}
	task := taskVia(t, e, run, "build", "running", func(task map[string]any) bool { return task["attempts"] != nil })
	if task["attempts"] != float64(4) {
		t.Fatalf("attempts after a repeat: %v", task["attempts"])
	}
}

// A task retried through the link leaves the open items and stays in the
// link's Recent, its steps through to resumed once the next attempt runs.
func TestALinkKeepsItsRecent(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	_, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	op, _ := desk.retried(ref, "keep")
	runningAttempt(t, e, run, "build", 4)
	page := desk.list("")
	last := page.Recent[len(page.Recent)-1]
	if page.Total != 0 || last.Ref != ref || last.Receipt != op || last.Attempt != op {
		t.Fatalf("the list after: %+v", page)
	}
	if steps := desk.track(ref, op, op); steps[stepResumed].State != stepDone || !strings.Contains(steps[stepResumed].Words, "attempt 4") {
		t.Fatalf("the steps: %+v", steps)
	}
	// A second connection of the link reads the same Recent.
	if again := dialLane(t, e, "/ws/runs/"+run+"/desk", tok).list(""); !slices.Equal(again.Recent, page.Recent) {
		t.Fatalf("another connection's Recent: %+v", again.Recent)
	}
}

// decisions_changed comes on a receipt's move alone: a retry's next
// attempt held at the launcher's seam until the viewer has listed (the
// item gone) and tracked (resumed waiting), then let go; and a question
// answered, the agent's working held until the viewer has tracked.
func TestDecisionsChangedOnAReceiptAlone(t *testing.T) {
	t.Run("a retry", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		run := failedRun(t, e)
		hold := make(chan struct{})
		var once sync.Once
		e.srv.launches.setSeams(func(g *launchGate) {
			g.attempt = func(spec crew.LaunchSpec) {
				if spec.Ref.Task == "build" && spec.Ref.Attempt == 4 {
					<-hold
				}
			}
		})
		t.Cleanup(func() { once.Do(func() { close(hold) }) })
		_, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
		desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		ref := "task:" + run + "/build"
		op, _ := desk.retried(ref, "held")
		until(t, "the item gone", func() bool { return desk.list("").Total == 0 })
		if steps := desk.track(ref, op, op); steps[stepResumed].State != stepWaiting {
			t.Fatalf("before the attempt: %+v", steps)
		}
		once.Do(func() { close(hold) })
		// A decisions_changed after which the step reads done, the open
		// list the same: one seen before the attempt started reads
		// waiting still, and the next is waited for.
		for {
			desk.waitChanged()
			if desk.track(ref, op, op)[stepResumed].State == stepDone {
				break
			}
		}
		if page := desk.list(""); page.Total != 0 {
			t.Fatalf("the open list changed: %+v", page)
		}
	})
	t.Run("a question", func(t *testing.T) {
		e := newTestEnv(t, nil)
		id := sessionHex("changed")
		fs := newFakeSession(t, e, id, session.Options{})
		q := fs.ask(t, "Which name?", threeChoices)
		_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
		w := dialLane(t, e, "/ws/sessions/"+id, tok)
		ref := questionRef(id, q)
		if m := w.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "2", OpID: "ans-1"}); !isOK(m) || m["receipt"] != q {
			t.Fatalf("the answer: %v", m)
		}
		until(t, "the item gone", func() bool { return w.list("").Total == 0 })
		if steps := w.track(ref, q, "ans-1"); steps[stepRecorded].State != stepDone || steps[stepResumed].State != stepWaiting {
			t.Fatalf("before the work: %+v", steps)
		}
		fs.report(t, session.AttentionWorking, "")
		for {
			w.waitChanged()
			if w.track(ref, q, "ans-1")[stepResumed].State == stepDone {
				break
			}
		}
		if page := w.list(""); page.Total != 0 {
			t.Fatalf("the open list changed: %+v", page)
		}
	})
}

// A decide taken whose reply is lost with its connection (held at the
// seam, the connection closed) is answered on a new connection, the same
// opId, with the first receipt and repeat, the item closed by then; the
// keys were typed once.
func TestDecideLostReplyReplays(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("lost")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	held := make(chan []byte, 1)
	let := make(chan struct{})
	e.srv.laneSeam = &laneSeams{beforeReply: func(reqID string, reply []byte) {
		if reqID == "r1" {
			held <- reply
			<-let
		}
	}}
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	ref := questionRef(id, q)
	w.sendDecide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "1", OpID: "lost-1"})
	first := <-held
	w.c.CloseNow()
	close(let)
	var m map[string]any
	_ = json.Unmarshal(first[1:], &m)
	if !isOK(m) {
		t.Fatalf("the reply that was lost: %v", m)
	}
	until(t, "the question resolved", func() bool { return fs.l.Info().Attention.ID != q })
	again := dialLane(t, e, "/ws/sessions/"+id, tok)
	again.n = 1
	if r := again.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "1", OpID: "lost-1"}); !isOK(r) || r["repeat"] != true || r["receipt"] != q {
		t.Fatalf("the same opId again: %v", r)
	}
	if got := fs.written(); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("typed: %q", got)
	}
}

// A check's log is read through the lane by its own ref: after another
// viewer retried the task and its next attempt failed too (the item's
// evidence now leads with attempt 4's check), attempt 3's check read by
// its ref is attempt 3's, the reply naming that ref. A check of another
// run is not_in_scope; a session in the scope gives its id; an entry of
// another kind is opened on the desktop or the LAN.
func TestDecisionEvidenceByItsRef(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	_, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	first := desk.read(ref)
	var check3 string
	for _, ev := range first.Evidence {
		if strings.HasPrefix(ev.Ref, "check:"+run+"/build/3/") {
			check3 = ev.Ref
		}
	}
	if check3 == "" {
		t.Fatalf("no check of attempt 3: %+v", first.Evidence)
	}
	// Another viewer's retry, at the revision it reads (read again while
	// the run's trees still end).
	for i := 1; ; i++ {
		status, code := e.command(t, run, "build", crew.TaskRetry, "other-"+strconv.Itoa(i), e.itemOf(t, ref).Revision)
		if status == http.StatusOK {
			break
		}
		if code != "stale_revision" || i == 20 {
			t.Fatalf("another viewer's retry: %d %s", status, code)
		}
	}
	sid := runningAttempt(t, e, run, "build", 4)["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "again"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run, "build", "failed", func(task map[string]any) bool { return task["state"] == "failed" })
	now := desk.read(ref)
	if !slices.ContainsFunc(now.Evidence, func(r inboxRef) bool { return strings.HasPrefix(r.Ref, "check:"+run+"/build/4/") }) {
		t.Fatalf("the evidence now: %+v", now.Evidence)
	}
	got, ctl := desk.evidence(ref, check3)
	if ctl != nil || got.Ref != check3 || got.Kind != "log" || got.Exit != 1 {
		t.Fatalf("attempt 3's check: %+v %v", got, ctl)
	}
	if _, ctl := desk.evidence(ref, "check:other-0000abcd/build/1/never/1"); !refusedWith(ctl, proto.DecideNotInScope) {
		t.Fatalf("another run's check: %v", ctl)
	}
	if _, ctl := desk.evidence(ref, strings.Replace(check3, "/3/", "/9/", 1)); !refusedWith(ctl, proto.DecideNotFound) {
		t.Fatalf("a check that is not there: %v", ctl)
	}
	if _, ctl := desk.evidence(ref, "run:"+run); !refusedWith(ctl, proto.DecideUnsupported) {
		t.Fatalf("the run page: %v", ctl)
	}
}

// The evidence a link reads is its scope's: a session outside it is
// not_in_scope, its own session's id is given, and an item outside it is
// refused before its evidence is looked at.
func TestDecisionEvidenceIsScoped(t *testing.T) {
	e := newTestEnv(t, nil)
	in, out := sessionHex("in"), sessionHex("out")
	fs := newFakeSession(t, e, in, session.Options{})
	other := newFakeSession(t, e, out, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	oq := other.ask(t, "Which name?", threeChoices)
	_, tok := e.decisionLink(t, "/api/sessions/"+in+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+in, tok)
	ref := questionRef(in, q)
	it := w.read(ref)
	if len(it.Evidence) == 0 || it.Evidence[0].Session != in {
		t.Fatalf("the item's session entry: %+v", it.Evidence)
	}
	if got, ctl := w.evidence(ref, "session:"+in); ctl != nil || got.Session != in {
		t.Fatalf("its own session: %+v %v", got, ctl)
	}
	if _, ctl := w.evidence(ref, "session:"+out); !refusedWith(ctl, proto.DecideNotInScope) {
		t.Fatalf("another session: %v", ctl)
	}
	if _, ctl := w.evidence(questionRef(out, oq), "session:"+out); !refusedWith(ctl, proto.DecideNotInScope) {
		t.Fatalf("another session's item: %v", ctl)
	}
}

// A run's desk at the server answers with no member alive: before the
// first attempt has started (its list empty, total 0), and once the last
// member's attempt failed, the failed task listed and a retry taken.
func TestTheServersDeskServesARunWithNoLiveMember(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	hold := make(chan struct{})
	var once sync.Once
	e.srv.launches.setSeams(func(g *launchGate) {
		g.attempt = func(spec crew.LaunchSpec) {
			if spec.Ref.Attempt == 1 {
				<-hold
			}
		}
	})
	t.Cleanup(func() { once.Do(func() { close(hold) }) })
	launched := make(chan error, 1)
	go func() {
		_, release, err := e.srv.runs.LaunchNamed(context.Background(), failingCrew(e.root), "")
		release()
		launched <- err
	}()
	var run crew.Run
	until(t, "the run made", func() bool {
		runs := e.srv.runs.List()
		if len(runs) == 1 {
			run = runs[0]
		}
		return len(runs) == 1
	})
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	_, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
	if page := desk.list(""); page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("before any attempt: %+v", page)
	}
	once.Do(func() { close(hold) })
	if err := <-launched; err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		sid := runningAttempt(t, e, run.ID, "build", n)["sessionId"].(string)
		if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "try"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result %d: %d %v", n, resp.StatusCode, out)
		}
	}
	taskVia(t, e, run.ID, "build", "failed", func(task map[string]any) bool { return task["state"] == "failed" })
	for _, m := range e.srv.runs.List() {
		for _, mem := range m.Members {
			if mem.SessionID == "" {
				continue
			}
			if d, ok := e.srv.registry.Get(mem.SessionID); ok {
				if l, ok := d.(*session.Local); ok {
					_ = l.Stop(t.Context())
				}
			}
		}
	}
	ref := "task:" + run.ID + "/build"
	until(t, "the task listed", func() bool { return desk.list("").Total == 1 })
	desk.retried(ref, "desk")
	runningAttempt(t, e, run.ID, "build", 4)
}

// A decide held in the owner until its link is revoked, then let go, is
// refused, nothing typed: the link is read again as the decide is taken.
func TestDecideAfterRevokeIsRefused(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("revoked")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	at := make(chan struct{})
	let := make(chan struct{})
	replies := make(chan []byte, 1)
	e.srv.laneSeam = &laneSeams{
		beforeDecide: func(string, string) { close(at); <-let },
		beforeReply:  func(_ string, reply []byte) { replies <- reply },
	}
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "late-1"})
	<-at
	if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	close(let)
	var m map[string]any
	_ = json.Unmarshal((<-replies)[1:], &m)
	if !refusedWith(m, proto.DecideRevoked) {
		t.Fatalf("the decide after the revoke: %v", m)
	}
	if got := fs.written(); len(got) != 0 {
		t.Fatalf("typed: %q", got)
	}
	if a := fs.l.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
		t.Fatalf("the question: %+v", a)
	}
}

// At most four decides are under way on a connection: the fifth is
// refused too_many_requests at once, the four answered once let go.
func TestDecideInFlightBound(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("bound")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	var mu sync.Mutex
	waiting := 0
	let := make(chan struct{})
	e.srv.laneSeam = &laneSeams{beforeDecide: func(string, string) {
		mu.Lock()
		waiting++
		mu.Unlock()
		<-let
	}}
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	var ids []string
	for i := range proto.MaxDecideInFlight {
		ids = append(ids, w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "op-" + strconv.Itoa(i)}))
	}
	until(t, "four held", func() bool { mu.Lock(); defer mu.Unlock(); return waiting == proto.MaxDecideInFlight })
	if m := w.decide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "op-5"}); !refusedWith(m, proto.DecideTooMany) {
		t.Fatalf("the fifth: %v", m)
	}
	close(let)
	oks := 0
	for _, rid := range ids {
		_, m := w.reply(rid)
		if isOK(m) {
			oks++
		} else if !refusedWith(m, proto.DecideAnswered) {
			t.Fatalf("%s: %v", rid, m)
		}
	}
	if oks != 1 || !slices.Equal(fs.written(), []string{"1"}) {
		t.Fatalf("%d taken, typed %q", oks, fs.written())
	}
}

// A turn held for the terminal is listed with no answer, and a decide
// naming it is refused question_answered, nothing typed.
func TestDecideRefusesAHeldTurn(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("held-turn")
	fs := newFakeSession(t, e, id, session.Options{SubmitPause: time.Hour})
	b := fs.ask(t, "Which name?", nil)
	try, giveUp := context.WithCancel(t.Context())
	late := make(chan error, 1)
	go func() {
		_, err := fs.l.Submit(try, session.Submission{Text: "late", Question: b, ByName: "sched", Withdrawn: try.Done()})
		late <- err
	}()
	until(t, "the line typed", func() bool { return slices.Contains(fs.written(), "late") })
	giveUp()
	<-late
	until(t, "the hold shown", func() bool {
		att := fs.l.Info().Attention
		return fs.l.Held() && att.State == session.AttentionNeedsInput && att.Source == session.SourceSubmit
	})
	hold := fs.l.Info().Attention.ID
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	ref := questionRef(id, hold)
	page := w.list("")
	if page.Total != 1 || page.Items[0].Ref != ref || len(page.Items[0].Actions) != 0 {
		t.Fatalf("the list: %+v", page)
	}
	before := len(fs.written())
	for _, m := range []proto.Decide{
		{Ref: ref, Action: "answer", Revision: hold, Input: "1", OpID: "held-1"},
		{Ref: ref, Action: "answer", Revision: hold, Text: "go on", OpID: "held-2"},
	} {
		if r := w.decide(m); !refusedWith(r, proto.DecideAnswered) {
			t.Fatalf("an answer naming the hold: %v", r)
		}
	}
	if got := fs.written(); len(got) != before {
		t.Fatalf("typed into a held turn: %q", got[before:])
	}
}

// Two viewers of one run decision link answer one question with two
// attempts, one through the asking session's own connection (its own
// answer) and one over the desk (through the question's claim as the
// link): the first wins; the loser, on a new connection, reads "another
// answer first" by its attempt, the winner done. A line through Submit
// over the desk, the same the other way round.
func TestALinksLosingViewerReadsAnotherAnswerFirst(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	d, _ := e.srv.registry.Get(sid)
	local := d.(*session.Local)
	_, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 0)
	own := dialLane(t, e, "/ws/sessions/"+sid, tok)
	desk := dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
	if own.welcome["decisions"] != true {
		t.Fatalf("the member's welcome through the run's link: %v", own.welcome)
	}
	local.SetAttentionFull(session.AttentionNeedsInput, "Allow git push?", session.SourceAPI, session.KindPermission, threeChoices)
	q := local.Info().Attention.ID
	ref := questionRef(sid, q)
	if m := own.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "1", OpID: "own-1"}); !isOK(m) {
		t.Fatalf("the own connection's answer: %v", m)
	}
	if m := desk.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "2", OpID: "desk-1"}); !refusedWith(m, proto.DecideAnswered) {
		t.Fatalf("the desk's answer after: %v", m)
	}
	desk.c.CloseNow()
	desk = dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
	if steps := desk.track(ref, q, "desk-1"); steps[stepRecorded].State != stepRefused || !strings.Contains(steps[stepRecorded].Words, "another answer first") {
		t.Fatalf("the loser's steps: %+v", steps)
	}
	if steps := desk.track(ref, q, "own-1"); steps[stepRecorded].State != stepDone {
		t.Fatalf("the winner's steps: %+v", steps)
	}
	if r, _ := local.QuestionResult(q); r.ReqID != "own-1" {
		t.Fatalf("the kept result: %+v", r)
	}
	// A line, through Submit over the desk, then the own connection late.
	until(t, "the answer resolved", func() bool { return local.Info().Attention.ID != q })
	local.SetAttentionFull(session.AttentionNeedsInput, "Which branch?", session.SourceAPI, session.KindPrompt, nil)
	q2 := local.Info().Attention.ID
	ref2 := questionRef(sid, q2)
	if m := desk.decide(proto.Decide{Ref: ref2, Action: "answer", Revision: q2, Text: "main", OpID: "desk-2"}); !isOK(m) {
		t.Fatalf("the desk's line: %v", m)
	}
	if m := own.decide(proto.Decide{Ref: ref2, Action: "answer", Revision: q2, Text: "dev", OpID: "own-2"}); !refusedWith(m, proto.DecideAnswered) {
		t.Fatalf("the own connection's line after: %v", m)
	}
	own.c.CloseNow()
	own = dialLane(t, e, "/ws/sessions/"+sid, tok)
	if steps := own.track(ref2, q2, "own-2"); steps[stepRecorded].State != stepRefused || !strings.Contains(steps[stepRecorded].Words, "another answer first, by phone") {
		t.Fatalf("the line's loser: %+v", steps)
	}
	if r, _ := local.QuestionResult(q2); r.ReqID != "desk-2" || r.ByName != "phone" {
		t.Fatalf("the line's kept result: %+v", r)
	}
}

// What a decision link is refused, each at the server: a token changed,
// cut short, or replayed after a restart (links live in memory); the link
// expired; the link on another route (the workbench's inbox, a task
// command, another run's desk); the desk asked for terminal input or a
// file; a decision already answered; an item's action it does not list.
func TestADecisionLinksRefusals(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := "/ws/runs/" + run + "/desk"
	for name, bad := range map[string]string{"changed": tok[:len(tok)-1] + flip(tok[len(tok)-1]), "cut short": tok[:len(tok)-4], "none": ""} {
		if code := closedWith(t, e, desk, bad); code != proto.CloseUnauthorized {
			t.Fatalf("a token %s: %d", name, code)
		}
	}
	if code := closedWith(t, e, "/ws/runs/other-0000abcd/desk", tok); code != proto.CloseForbidden {
		t.Fatalf("another run's desk: %d", code)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/inbox"},
		{"GET", "/api/runs/" + run},
		{"POST", "/api/runs/" + run + "/tasks/build/retry"},
		{"GET", "/api/sessions"},
	} {
		if resp, _ := e.do(r.method, r.path, tok, map[string]any{"commandId": "x"}); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s with the link: %d", r.method, r.path, resp.StatusCode)
		}
	}
	// At most eight desks of one link at once.
	var desks []*laneClient
	for range maxDesks {
		desks = append(desks, dialLane(t, e, desk, tok))
	}
	if code := closedWith(t, e, desk, tok); code != proto.CloseTooManyViewers {
		t.Fatalf("a ninth desk: %d", code)
	}
	for _, d := range desks {
		d.c.Close(websocket.StatusNormalClosure, "")
	}
	until(t, "the desks let go", func() bool {
		e.srv.lanes.mu.Lock()
		defer e.srv.lanes.mu.Unlock()
		return len(e.srv.lanes.conns[linkID]) == 0
	})
	// A plain control link of the run opens no desk.
	resp, out := e.do("POST", "/api/runs/"+run+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(out)
	}
	if code := closedWith(t, e, desk, out["token"].(string)); code != proto.CloseForbidden {
		t.Fatalf("a plain link's desk: %d", code)
	}
	w := dialLane(t, e, desk, tok)
	ref := "task:" + run + "/build"
	it := w.read(ref)
	if m := w.decide(proto.Decide{Ref: ref, Action: crew.TaskCancel, Revision: it.Revision, OpID: "cancel-1"}); !refusedWith(m, proto.DecideStaleRevision) {
		t.Fatalf("a cancel the item does not list: %v", m)
	}
	if m := w.decide(proto.Decide{Ref: ref, Action: "approve", Revision: it.Revision, OpID: "approve-1"}); !refusedWith(m, proto.DecideInvalid) {
		t.Fatalf("an approve: %v", m)
	}
	_, expect := w.retried(ref, "retry")
	runningAttempt(t, e, run, "build", 4)
	// Already decided: the task is no longer failed, a second retry (another
	// opId) at the old revision does nothing.
	if m := w.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: expect, OpID: "again-1"}); !refusedWith(m, proto.DecideStaleRevision) {
		t.Fatalf("a retry after the decision: %v", m)
	}
	if _, found, _ := e.srv.ledger.Receipt(t.Context(), run, "link:"+linkID, "again-1"); found {
		t.Fatal("the second retry was taken")
	}
	// The desk takes the lane's messages alone: input and a file read close it.
	w.send(proto.Encode(proto.TypeInput, []byte("ls\r")))
	w.expectClose(proto.CloseProtocolError)
	w = dialLane(t, e, desk, tok)
	w.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "f1", Path: "README"}))
	w.expectClose(proto.CloseProtocolError)
}

// flip is another base64url character than c.
func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

// An expired link's desk is closed as expired, and the link opens nothing
// after; a link replayed after the server's restart opens nothing.
func TestADecisionLinkExpiresAndDiesWithTheServer(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("expiry")
	fs := newFakeSession(t, e, id, session.Options{})
	fs.ask(t, "Which name?", threeChoices)
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 1)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	until(t, "the link expired", func() bool {
		_, err := e.srv.links.Live(linkID)
		return errors.Is(err, share.ErrExpired)
	})
	e.srv.links.ExpireDue(time.Now())
	w.expectClose(proto.CloseForbidden)
	if code := closedWith(t, e, "/ws/sessions/"+id, tok); code != proto.CloseUnauthorized {
		t.Fatalf("an expired link: %d", code)
	}
	_, tok2 := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	again := e.restart()
	if code := closedWith(t, again, "/ws/sessions/"+id, tok2); code != proto.CloseUnauthorized {
		t.Fatalf("a link after the restart: %d", code)
	}
}

// A decision link's token is never logged: the request lines name the
// desk's route, a refused token included, never its query.
func TestADecisionLinksTokenIsNeverLogged(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	id := sessionHex("logged")
	newFakeSession(t, e, id, session.Options{})
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	dialLane(t, e, "/ws/sessions/"+id, tok).list("")
	closedWith(t, e, "/ws/runs/some-run/desk", tok)
	closedWith(t, e, "/ws/runs/some-run/desk", tok+"x")
	lines := requestLines(t, logs, 3)
	if strings.Contains(logs.String(), tok) {
		t.Fatalf("the token in the log:\n%s", logs.String())
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "/ws/runs/{run}/desk") }) {
		t.Fatalf("the desk's route not named:\n%s", strings.Join(lines, "\n"))
	}
}
