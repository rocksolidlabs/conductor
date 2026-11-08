package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// The lane's three rules (lane.go) and its bounds, each held at the
// server: a link's act is written only while the link stands, checked with
// the write; an opId names one operation; the lane reads only the
// decisions' own records. The tests wait on seams the lane offers and on
// the state they need, never on the clock.

// replyOf decodes a decide's reply frame held at the beforeReply seam.
func replyOf(t *testing.T, f []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(f[1:], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// closedAs reads w until its close: the close code, and the error frame's
// code before it ("" for none).
func closedAs(w *wsClient) (websocket.StatusCode, string) {
	w.t.Helper()
	code := ""
	for {
		f, err := w.read()
		if err != nil {
			return websocket.CloseStatus(err), code
		}
		if f.Type == proto.TypeControl {
			var m map[string]any
			_ = json.Unmarshal(f.Payload, &m)
			if m["t"] == proto.CtlError {
				code, _ = m["code"].(string)
			}
		}
	}
}

// parallelCrew is managedCrew with docs needing nothing: build's and
// docs' first attempts run together, a member each.
func parallelCrew(root string) crew.Crew {
	c := managedCrew(root)
	c.Tasks[1].Needs = nil
	return c
}

// An opId names one operation (rule 2): after a retry taken through the
// link, the same opId naming a task the run does not have, a sibling
// task, another action or another revision is op_conflict from either
// connection of the link, nothing done; the same request again is the
// first receipt, repeat. An answer's the same: another choice, a line,
// under one opId are op_conflict, the keys typed once. (Links live in
// memory: after a restart no link of before stands, and its opIds with
// it, TestADecisionLinkExpiresAndDiesWithTheServer.)
func TestAnOpIDNamesOneOperation(t *testing.T) {
	t.Run("a task command", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		run := failedRun(t, e)
		linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
		desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		other := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		ref := "task:" + run + "/build"
		op, expect := desk.retried(ref, "retry")
		runningAttempt(t, e, run, "build", 4)
		for name, m := range map[string]proto.Decide{
			"a task the run has not": {Ref: "task:" + run + "/deploy", Action: crew.TaskRetry, Revision: expect, OpID: op},
			"a sibling task":         {Ref: "task:" + run + "/docs", Action: crew.TaskRetry, Revision: expect, OpID: op},
			"another action":         {Ref: ref, Action: crew.TaskAccept, Revision: expect, OpID: op},
			"another revision":       {Ref: ref, Action: crew.TaskRetry, Revision: expect + "0", OpID: op},
		} {
			for i, w := range []*laneClient{desk, other} {
				if r := w.decide(m); !refusedWith(r, proto.DecideOpConflict) {
					t.Fatalf("%s from connection %d: %v", name, i, r)
				}
			}
		}
		for i, w := range []*laneClient{desk, other} {
			if r := w.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: expect, OpID: op}); !isOK(r) || r["receipt"] != op || r["ref"] != ref || r["repeat"] != true {
				t.Fatalf("the same request from connection %d: %v", i, r)
			}
		}
		row, found, err := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), op)
		if err != nil || !found || row.Intent != crew.TaskRetry {
			t.Fatalf("the receipt: %+v %v %v", row, found, err)
		}
		if task := taskVia(t, e, run, "build", "running", func(task map[string]any) bool { return task["attempts"] != nil }); task["attempts"] != float64(4) {
			t.Fatalf("attempts: %v", task["attempts"])
		}
		if task := taskVia(t, e, run, "docs", "as it was", func(task map[string]any) bool { return true }); task["state"] == "running" {
			t.Fatalf("docs: %v", task)
		}
	})
	t.Run("an answer", func(t *testing.T) {
		e := newTestEnv(t, nil)
		id := sessionHex("opid")
		fs := newFakeSession(t, e, id, session.Options{})
		q := fs.ask(t, "Which name?", threeChoices)
		_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
		w := dialLane(t, e, "/ws/sessions/"+id, tok)
		other := dialLane(t, e, "/ws/sessions/"+id, tok)
		ref := questionRef(id, q)
		first := proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "1", OpID: "ans-1"}
		if r := w.decide(first); !isOK(r) || r["repeat"] != nil {
			t.Fatalf("the answer: %v", r)
		}
		for name, m := range map[string]proto.Decide{
			"another choice": {Ref: ref, Action: "answer", Revision: q, Input: "2", OpID: "ans-1"},
			"a line":         {Ref: ref, Action: "answer", Revision: q, Text: "one", OpID: "ans-1"},
		} {
			for i, c := range []*laneClient{w, other} {
				if r := c.decide(m); !refusedWith(r, proto.DecideOpConflict) {
					t.Fatalf("%s from connection %d: %v", name, i, r)
				}
			}
		}
		if r := other.decide(first); !isOK(r) || r["repeat"] != true || r["receipt"] != q {
			t.Fatalf("the same answer again: %v", r)
		}
		if got := fs.written(); !slices.Equal(got, []string{"1"}) {
			t.Fatalf("typed: %q", got)
		}
	})
}

// Two connections of one link sending one opId at once (a reconnect while
// the first is under way): the second waits for the first, held before its
// operation, and is answered with its outcome, repeat; the keys are typed
// once.
func TestALinksTwinsShareOneOutcome(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("twins")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	at, let, waits := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first, twin sync.Once
	e.srv.laneSeam = &laneSeams{
		beforeOperation: func(string, string) { first.Do(func() { close(at); <-let }) },
		twinWaits:       func(string, string) { twin.Do(func() { close(waits) }) },
	}
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	a := dialLane(t, e, "/ws/sessions/"+id, tok)
	b := dialLane(t, e, "/ws/sessions/"+id, tok)
	m := proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "3", OpID: "twin-1"}
	ra := a.sendDecide(m)
	<-at
	rb := b.sendDecide(m)
	<-waits
	close(let)
	if _, r := a.reply(ra); !isOK(r) || r["repeat"] != nil || r["receipt"] != q {
		t.Fatalf("the first: %v", r)
	}
	if _, r := b.reply(rb); !isOK(r) || r["repeat"] != true || r["receipt"] != q {
		t.Fatalf("the twin: %v", r)
	}
	if got := fs.written(); !slices.Equal(got, []string{"3"}) {
		t.Fatalf("typed: %q", got)
	}
}

// A question's kept result is read by the link's own request id: the
// workbench's answer under a request id equal to a link's opId is never
// the link's. The link's track with that attempt reads another answer
// first, and its decide under that opId is refused question_answered,
// never a repeat.
func TestALinkReadsOnlyItsOwnAnswers(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("own-answers")
	fs := newFakeSession(t, e, id, session.Options{})
	q := fs.ask(t, "Which name?", threeChoices)
	admin := dialLane(t, e, "/ws/sessions/"+id, adminToken)
	admin.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "same-1", Question: q, Input: "1"}))
	until(t, "the workbench's answer kept", func() bool {
		r, ok := fs.l.QuestionResult(q)
		return ok && r.ReqID == "same-1"
	})
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	ref := questionRef(id, q)
	if steps := w.track(ref, q, "same-1"); steps[stepRecorded].State != stepRefused || !strings.Contains(steps[stepRecorded].Words, "another answer first") {
		t.Fatalf("the link's steps of the workbench's answer: %+v", steps)
	}
	if r := w.decide(proto.Decide{Ref: ref, Action: "answer", Revision: q, Input: "1", OpID: "same-1"}); !refusedWith(r, proto.DecideAnswered) {
		t.Fatalf("the link's decide under the workbench's id: %v", r)
	}
	if got := fs.written(); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("typed: %q", got)
	}
}

// A link's answer is written only while the link stands, checked with the
// write (rule 1): an answer the session's writer takes after the link was
// revoked or expired, through the desk (the question's claim as the link)
// or through the asking session's own connection, is refused, nothing
// typed, the question still waiting. The seam holds the writer as it
// takes the answer, before the link is held.
func TestALinksAnswerIsWrittenOnlyWhileItStands(t *testing.T) {
	// held arms the seam: the first answer the writer takes waits at it
	// until let; replies keeps each decide's reply.
	held := func(e *testEnv) (at, let chan struct{}, replies chan []byte) {
		at, let, replies = make(chan struct{}), make(chan struct{}), make(chan []byte, 4)
		var once sync.Once
		e.srv.viewerCloseWait = 100 * time.Millisecond
		e.srv.laneSeam = &laneSeams{
			atHold:      func(string) { once.Do(func() { close(at); <-let }) },
			beforeReply: func(_ string, f []byte) { replies <- f },
		}
		return at, let, replies
	}
	t.Run("over the desk, revoked", func(t *testing.T) {
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
		at, let, replies := held(e)
		linkID, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 0)
		desk := dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
		local.SetAttentionFull(session.AttentionNeedsInput, "Allow git push?", session.SourceAPI, session.KindPermission, threeChoices)
		q := local.Info().Attention.ID
		desk.sendDecide(proto.Decide{Ref: questionRef(sid, q), Action: "answer", Revision: q, Input: "1", OpID: "late-1"})
		<-at
		if resp, _ := e.do("DELETE", "/api/runs/"+run.ID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: %d", resp.StatusCode)
		}
		close(let)
		if r := replyOf(t, <-replies); !refusedWith(r, proto.DecideRevoked) {
			t.Fatalf("the answer taken after the revoke: %v", r)
		}
		if _, ok := local.QuestionResult(q); ok || local.Info().Attention.ID != q {
			t.Fatalf("answered: %+v", local.Info().Attention)
		}
	})
	t.Run("its own connection, revoked", func(t *testing.T) {
		e := newTestEnv(t, nil)
		id := sessionHex("own-late")
		fs := newFakeSession(t, e, id, session.Options{})
		q := fs.ask(t, "Which name?", threeChoices)
		at, let, replies := held(e)
		linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
		w := dialLane(t, e, "/ws/sessions/"+id, tok)
		w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "late-1"})
		<-at
		if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: %d", resp.StatusCode)
		}
		close(let)
		if r := replyOf(t, <-replies); !refusedWith(r, proto.DecideRevoked) {
			t.Fatalf("the answer taken after the revoke: %v", r)
		}
		if got := fs.written(); len(got) != 0 {
			t.Fatalf("typed: %q", got)
		}
		if a := fs.l.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
			t.Fatalf("the question: %+v", a)
		}
	})
	t.Run("its own connection, expired", func(t *testing.T) {
		e := newTestEnv(t, nil)
		id := sessionHex("own-expired")
		fs := newFakeSession(t, e, id, session.Options{})
		q := fs.ask(t, "Which name?", threeChoices)
		// The deadline far off, the clock the test's, and the viewer's
		// expiry made by the test once the answer is held.
		var cmu sync.Mutex
		now := time.Now().UTC()
		e.srv.links.SetNowForTest(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
		at, let, replies := held(e)
		fires := make(chan func(), 1)
		e.srv.laneSeam.lapse = func(_ string, fire func()) { fires <- fire }
		linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 3600)
		w := dialLane(t, e, "/ws/sessions/"+id, tok)
		fire := <-fires
		w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "late-1"})
		<-at
		cmu.Lock()
		now = now.Add(2 * time.Hour)
		cmu.Unlock()
		fire()
		if _, err := e.srv.links.Live(linkID); !errors.Is(err, share.ErrExpired) {
			t.Fatalf("the link after its expiry: %v", err)
		}
		close(let)
		if r := replyOf(t, <-replies); !refusedWith(r, proto.DecideExpired) {
			t.Fatalf("the answer taken after the expiry: %v", r)
		}
		if got := fs.written(); len(got) != 0 {
			t.Fatalf("typed: %q", got)
		}
	})
}

// A task command through a link is taken only while the link stands,
// checked with the engine's command (rule 1): a revoke between the item's
// judgement and the operation refuses it, no receipt kept; a revoke that
// lands while the engine takes a command (taken, its change not yet
// made) waits for the command, which asks again at its commit, finds its
// link ended and commits nothing (its receipt settled withdrawn, the task
// as it was), and returns after it.
func TestALinksCommandIsHeldAgainstItsRevoke(t *testing.T) {
	t.Run("revoked before the operation", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		run := failedRun(t, e)
		at, let, replies := make(chan struct{}), make(chan struct{}), make(chan []byte, 1)
		e.srv.viewerCloseWait = 100 * time.Millisecond
		e.srv.laneSeam = &laneSeams{
			beforeOperation: func(string, string) { close(at); <-let },
			beforeReply:     func(_ string, f []byte) { replies <- f },
		}
		linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
		desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		ref := "task:" + run + "/build"
		retry := actionOf(t, desk.read(ref), crew.TaskRetry)
		desk.sendDecide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "gap-1"})
		<-at
		if resp, _ := e.do("DELETE", "/api/runs/"+run+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: %d", resp.StatusCode)
		}
		close(let)
		if r := replyOf(t, <-replies); !refusedWith(r, proto.DecideRevoked) {
			t.Fatalf("the retry after the revoke: %v", r)
		}
		if _, found, _ := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "gap-1"); found {
			t.Fatal("a receipt kept for a retry after the revoke")
		}
	})
	t.Run("a revoke waits for the command under way, which commits nothing", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		run := failedRun(t, e)
		taken, let := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.srv.runs.SetAfterTakenForTest(func(_, id string) {
			if id == "held-1" {
				once.Do(func() { close(taken); <-let })
			}
		})
		t.Cleanup(func() { e.srv.runs.SetAfterTakenForTest(nil) })
		replies := make(chan []byte, 1)
		// The bound far off: the revoke is judged waiting for the command.
		e.srv.links.HoldWait = time.Hour
		e.srv.viewerCloseWait = 100 * time.Millisecond
		e.srv.laneSeam = &laneSeams{beforeReply: func(_ string, f []byte) { replies <- f }}
		linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
		desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		ref := "task:" + run + "/build"
		retry := actionOf(t, desk.read(ref), crew.TaskRetry)
		desk.sendDecide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "held-1"})
		<-taken
		revoked := make(chan int, 1)
		go func() {
			resp, _ := e.do("DELETE", "/api/runs/"+run+"/links/"+linkID, adminToken, nil)
			revoked <- resp.StatusCode
		}()
		// The revoke is under way (a hold is refused) and waits for the
		// command the engine is taking.
		until(t, "the revoke under way", func() bool {
			h, err := e.srv.links.Hold(linkID)
			if err == nil {
				h.Release()
			}
			return err != nil
		})
		// Not marked yet: the mark waits for the command's commit.
		if _, err := e.srv.links.Live(linkID); err != nil {
			t.Fatalf("the link marked revoked while the engine took its command: %v", err)
		}
		select {
		case code := <-revoked:
			t.Fatalf("the revoke returned (%d) while the engine took the link's command", code)
		default:
		}
		close(let)
		if r := replyOf(t, <-replies); !refusedWith(r, proto.DecideRevoked) {
			t.Fatalf("the command under way: %v", r)
		}
		if code := <-revoked; code != http.StatusNoContent {
			t.Fatalf("revoke: %d", code)
		}
		row, found, err := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "held-1")
		if err != nil || !found || row.Status != "failed" || !strings.Contains(row.Receipt, crew.RefusalWithdrawn) {
			t.Fatalf("the command's receipt: %+v %v %v", row, found, err)
		}
		if st, _, err := e.srv.runs.TaskOf(run, "build"); err != nil || st.State != "failed" {
			t.Fatalf("the task after a command its link's revoke withdrew: %+v %v", st, err)
		}
	})
}

// A decide whose connection closed before it began is never begun: held
// as it begins, its connection closed and the lane ended (its watch gone
// first), then let go, nothing is taken.
func TestALaneEndsWhatItHasNotBegun(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	at, let := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.laneSeam = &laneSeams{beforeDecide: func(string, string) { once.Do(func() { close(at); <-let }) }}
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	retry := actionOf(t, desk.read(ref), crew.TaskRetry)
	desk.sendDecide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "gone-1"})
	<-at
	desk.c.Close(websocket.StatusNormalClosure, "")
	until(t, "the lane ended", func() bool {
		e.srv.lanes.mu.Lock()
		defer e.srv.lanes.mu.Unlock()
		return e.srv.lanes.links[linkID] == nil
	})
	close(let)
	until(t, "the desk let go", func() bool {
		e.srv.lanes.mu.Lock()
		defer e.srv.lanes.mu.Unlock()
		return len(e.srv.lanes.conns[linkID]) == 0
	})
	if _, found, _ := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "gone-1"); found {
		t.Fatal("a decide whose connection closed before it began was taken")
	}
}

// A desk is closed with its link: revoked (an error revoked, 4403),
// expired at the link's own deadline with no sweep (expired, 4403), its
// run forgotten (revoked, 4403), and the server's shutdown (1001).
func TestADeskClosesWithItsLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   func(e *testEnv, run, linkID string, expire func())
		close websocket.StatusCode
		code  string
	}{
		{"revoked", func(e *testEnv, run, linkID string, _ func()) {
			e.do("DELETE", "/api/runs/"+run+"/links/"+linkID, adminToken, nil)
		}, proto.CloseForbidden, proto.ErrCodeRevoked},
		{"expired", func(_ *testEnv, _, _ string, expire func()) { expire() }, proto.CloseForbidden, proto.ErrCodeExpired},
		{"its run forgotten", func(e *testEnv, run, _ string, _ func()) { e.srv.runs.ForgetForTest(run) }, proto.CloseForbidden, proto.ErrCodeRevoked},
		{"the server's shutdown", func(e *testEnv, _, _ string, _ func()) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			e.srv.Shutdown(ctx)
		}, websocket.StatusGoingAway, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := readyEnv(t, nil)
			e.gitRepo(t)
			run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
			release()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
			e.srv.viewerCloseWait = 200 * time.Millisecond
			// The deadline far off and the clock the test's: the desk's
			// expiry is made once the desk is open, the clock past it.
			var cmu sync.Mutex
			now := time.Now().UTC()
			e.srv.links.SetNowForTest(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
			fires := make(chan func(), 1)
			e.srv.laneSeam = &laneSeams{lapse: func(_ string, fire func()) { fires <- fire }}
			linkID, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 3600)
			desk := dialLane(t, e, "/ws/runs/"+run.ID+"/desk", tok)
			fire := <-fires
			tc.end(e, run.ID, linkID, func() {
				cmu.Lock()
				now = now.Add(2 * time.Hour)
				cmu.Unlock()
				fire()
			})
			if close, code := closedAs(desk.wsClient); close != tc.close || code != tc.code {
				t.Fatalf("closed %d with %q, want %d with %q", close, code, tc.close, tc.code)
			}
		})
	}
}

// A desk whose handler has read its hello and not yet registered as the
// server shuts down is refused as it registers, its connection closed
// (1001): the shutdown closes admission before it lists the desks it
// closes, so no desk outlives it.
func TestADeskRegisteringAsTheServerShutsDownIsRefused(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	e.srv.viewerCloseWait = 200 * time.Millisecond
	at, let := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.laneSeam = &laneSeams{beforeDeskAdd: func(string) { once.Do(func() { close(at); <-let }) }}
	_, tok := e.decisionLink(t, "/api/runs/"+run.ID+"/links", "phone", 0)
	c, err := dialWS(e, "/ws/runs/"+run.ID+"/desk", tok)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	w := &wsClient{t: t, c: c}
	w.hello(0, 0)
	<-at
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	close(let)
	if close, _ := closedAs(w); close != websocket.StatusGoingAway {
		t.Fatalf("the desk registering after the shutdown closed %d", close)
	}
	e.srv.lanes.mu.Lock()
	n := len(e.srv.lanes.conns)
	e.srv.lanes.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d links hold desks after the shutdown", n)
	}
}

// Closing a link's desks takes one closeWait at most, whatever their
// number: eight desks whose peers read nothing are each cut at closeWait,
// all at once, not one after another.
func TestClosingDesksTakesOneCloseWait(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	e.srv.viewerCloseWait = 300 * time.Millisecond
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	for range maxDesks {
		dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	}
	// Each close's own cap is the one closeWait (cut) and, without it, the
	// close handshake's 5 s: eight one after another take 40 s, together
	// one closeWait. The bound is well above the one and below the eight.
	start := time.Now()
	e.srv.closeDesks(errShuttingDown, linkID)
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("closing eight desks took %v", took)
	}
}

// A session decision link of a run's member covers that member's question
// and its own task, nothing else of the run: another member's question,
// the run's other task (read or acted on) are not_in_scope, and the run's
// desk is refused it.
func TestADecisionLinkOfARunMemberReceivesItsScope(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), parallelCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	a := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	b := runningAttempt(t, e, run.ID, "docs", 1)["sessionId"].(string)
	ask := func(sid, words string) string {
		d, _ := e.srv.registry.Get(sid)
		l := d.(*session.Local)
		l.SetAttentionFull(session.AttentionNeedsInput, words, session.SourceAPI, session.KindPermission, threeChoices)
		return l.Info().Attention.ID
	}
	qa, qb := ask(a, "Allow git push?"), ask(b, "Allow rm?")
	_, tok := e.decisionLink(t, "/api/sessions/"+a+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+a, tok)
	if page := w.list(""); page.Total != 1 || page.Items[0].Ref != questionRef(a, qa) {
		t.Fatalf("the member's items: %+v", page)
	}
	rid := w.reqID()
	w.send(proto.MustControl(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: rid, Ref: "task:" + run.ID + "/build"}))
	if d, ctl := w.reply(rid); d == nil {
		t.Fatalf("its own task: %v", ctl)
	}
	for _, ref := range []string{questionRef(b, qb), "task:" + run.ID + "/docs"} {
		rid := w.reqID()
		if code := w.refusedRead(proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: rid, Ref: ref}, rid); code != proto.DecideNotInScope {
			t.Fatalf("%s read: %s", ref, code)
		}
	}
	if r := w.decide(proto.Decide{Ref: questionRef(b, qb), Action: "answer", Revision: qb, Input: "1", OpID: "b-1"}); !refusedWith(r, proto.DecideNotInScope) {
		t.Fatalf("the other member's question: %v", r)
	}
	if r := w.decide(proto.Decide{Ref: "task:" + run.ID + "/docs", Action: crew.TaskCancel, Revision: "1", OpID: "docs-1"}); !refusedWith(r, proto.DecideNotInScope) {
		t.Fatalf("the run's other task: %v", r)
	}
	if code := closedWith(t, e, "/ws/runs/"+run.ID+"/desk", tok); code != proto.CloseForbidden {
		t.Fatalf("the run's desk: %d", code)
	}
}

// At most four reads are under way on a connection, apart from its
// decides: the fifth is refused too_many_requests at once, the four
// answered once let go.
func TestALanesReadsInFlightBound(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("reads")
	newFakeSession(t, e, id, session.Options{})
	var mu sync.Mutex
	waiting := 0
	let := make(chan struct{})
	e.srv.laneSeam = &laneSeams{beforeRead: func(string) {
		mu.Lock()
		waiting++
		mu.Unlock()
		<-let
	}}
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	var ids []string
	for range proto.MaxDecisionReadsInFlight {
		rid := w.reqID()
		w.send(proto.MustControl(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: rid}))
		ids = append(ids, rid)
	}
	until(t, "four held", func() bool { mu.Lock(); defer mu.Unlock(); return waiting == proto.MaxDecisionReadsInFlight })
	rid := w.reqID()
	if code := w.refusedRead(proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: rid}, rid); code != proto.DecideTooMany {
		t.Fatalf("the fifth: %s", code)
	}
	close(let)
	for _, rid := range ids {
		if d, ctl := w.reply(rid); d == nil {
			t.Fatalf("%s: %v", rid, ctl)
		}
	}
}

// A DR1 record is no item before DR1's records join the inbox: its read is
// refused unsupported, by the owner.
func TestDecisionRecordIsUnsupportedBeforeDR1(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("record")
	newFakeSession(t, e, id, session.Options{})
	_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	rid := w.reqID()
	if code := w.refusedRead(proto.DecisionRecord{T: proto.CtlDecisionRecord, ReqID: rid, Ref: "decision:d1", Part: proto.RecordHead}, rid); code != proto.DecideUnsupported {
		t.Fatalf("a record's head: %s", code)
	}
}

// A decision link is not minted where it would not be served: at a
// switchyard (a published session's link, a published run's) 409
// switchyard_too_old, a hosted session's 409 unsupported; nothing made.
func TestADecisionLinkIsRefusedWhereItIsNotServed(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	id := local.createSession("cat")
	var hosted string
	until(t, "the session hosted at the rendezvous", func() bool {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i.ID
			}
		})
		return hosted != ""
	})
	decide := map[string]any{"role": "control", "decide": true}
	if resp, out := local.do("POST", "/api/sessions/"+id+"/links", adminToken, decide); resp.StatusCode != http.StatusConflict || errCodeOf(out) != "switchyard_too_old" {
		t.Fatalf("a published session's decision link: %d %v", resp.StatusCode, out)
	}
	if resp, out := rendezvous.do("POST", "/api/sessions/"+hosted+"/links", adminToken, decide); resp.StatusCode != http.StatusConflict || errCodeOf(out) != "unsupported" {
		t.Fatalf("a hosted session's decision link: %d %v", resp.StatusCode, out)
	}
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	if resp, out := local.do("POST", "/api/runs/"+runID+"/links", adminToken, decide); resp.StatusCode != http.StatusConflict || errCodeOf(out) != "switchyard_too_old" {
		t.Fatalf("a published run's decision link: %d %v", resp.StatusCode, out)
	}
	for _, l := range append(local.srv.links.ListBySession(id), local.srv.links.ListByRun(runID)...) {
		if l.Decide {
			t.Fatalf("made: %+v", l)
		}
	}
	if hosts := rendezvous.srv.links.ListBySession(hosted); len(hosts) != 0 {
		t.Fatalf("made at the rendezvous: %+v", hosts)
	}
}

// A hosted session's relayed viewer carries the lane to its host as it
// carries its terminal frames (docs/plans/decision-inbox.md, PR-6): the
// host answers it (this publisher serves no decisions: unsupported). The
// viewer is welcomed without decisions, and the connection and the
// host's session stay.
func TestARelayedViewersLaneReachesItsHost(t *testing.T) {
	rendezvous := newTestEnv(t, func(c *config.Config) { c.PublicURL = "https://rendezvous.example.net" })
	local := newTestEnv(t, nil)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: rendezvous.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true}})
	local.createSession("cat")
	var hosted string
	until(t, "the session hosted at the rendezvous", func() bool {
		rendezvous.srv.Registry().Each(func(d session.Driver) {
			if i := d.Info(); i.Kind == session.KindHosted && i.HostName == "office-server" {
				hosted = i.ID
			}
		})
		return hosted != ""
	})
	v := dialViewer(t, rendezvous, hosted, adminToken)
	f, err := proto.EncodeDecide(proto.Decide{ReqID: "r1", Ref: "task:a/b", Action: "retry", Revision: "1", OpID: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	if w := joinedRelay(t, v); w["decisions"] != nil {
		t.Fatalf("a hosted session's welcome: %v", w)
	}
	v.send(f)
	for {
		g, err := v.read()
		if err != nil {
			t.Fatalf("waiting for the host's refusal: %v", err)
		}
		var m map[string]any
		if g.Type == proto.TypeControl && json.Unmarshal(g.Payload, &m) == nil && m["t"] == proto.CtlDecideRefused {
			if m["reqId"] != "r1" || m["code"] != proto.DecideUnsupported {
				t.Fatalf("the refusal %v", m)
			}
			break
		}
	}
	if _, ok := rendezvous.srv.Registry().Get(hosted); !ok {
		t.Fatal("the host's session went with the frame")
	}
	v.send(proto.Encode(proto.TypeInput, []byte("still here\n")))
	v.expectOutput("still here")
}

// A link's connections share one watch, which reads the scope once per
// settle for all of them: three desks of a link are one watch of three,
// and it goes with the last.
func TestALinksConnectionsShareOneWatch(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	watchers := func() (watches, subs int) {
		e.srv.lanes.mu.Lock()
		defer e.srv.lanes.mu.Unlock()
		if lw := e.srv.lanes.links[linkID]; lw != nil {
			return len(e.srv.lanes.links), len(lw.subs)
		}
		return len(e.srv.lanes.links), 0
	}
	var desks []*laneClient
	for range 3 {
		d := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
		// A request answered: the desk's lane, and its watch's
		// registration, made (the welcome comes before them).
		d.list("")
		desks = append(desks, d)
	}
	if watches, subs := watchers(); watches != 1 || subs != 3 {
		t.Fatalf("%d watches of %d connections", watches, subs)
	}
	// Each still told: a retry through one tells all three.
	desks[0].retried("task:"+run+"/build", "shared")
	for _, d := range desks {
		d.waitChanged()
	}
	for _, d := range desks {
		d.c.Close(websocket.StatusNormalClosure, "")
	}
	until(t, "the watch gone with its last connection", func() bool { w, _ := watchers(); return w == 0 })
}

// A connection's watch makes its first read of the scope before the
// connection serves a request: Watch makes it on its caller's goroutine
// and returns after it, so a change after that read is told
// (decisions_changed) and none is taken as the watch's baseline unseen.
// The seam holds a first read made on any other goroutine until Watch has
// returned, so a watch that read on one of its own would return before
// its baseline every time; the caller's own read is never held.
func TestTheWatchReadsBeforeTheFirstRequest(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("first-read")
	newFakeSession(t, e, id, session.Options{})
	linkID, _ := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	caller := goroutineID()
	returned := make(chan struct{})
	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	var first sync.Once
	e.srv.laneSeam = &laneSeams{
		hashing: func(string) {
			first.Do(func() {
				if goroutineID() != caller {
					<-returned
				}
				note("the watch's first read")
			})
		},
	}
	stop := e.srv.Lane().Watch(linkID, id, func() {})
	note("Watch returned")
	close(returned)
	defer stop()
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"the watch's first read", "Watch returned"}) {
		t.Fatalf("in order: %q", order)
	}
}

// goroutineID is the id of the goroutine it runs on, from the first line
// of its stack ("goroutine <id> [...]").
func goroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	f := strings.Fields(string(buf))
	if len(f) < 2 {
		return ""
	}
	return f[1]
}
