package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// armedProc is a FakeProc whose next write, once armed, waits at a gate:
// the program not taking it until the test lets it.
type armedProc struct {
	*sessiontest.FakeProc
	mu      sync.Mutex
	in      chan struct{}
	gate    chan struct{}
	written func()
}

// arm makes the next write wait: in is closed as it waits, and it goes on
// once gate is closed.
func (g *armedProc) arm() (in, gate chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.in, g.gate = make(chan struct{}), make(chan struct{})
	return g.in, g.gate
}

func (g *armedProc) Write(b []byte) (int, error) {
	g.mu.Lock()
	in, gate, written := g.in, g.gate, g.written
	g.in, g.gate = nil, nil
	g.mu.Unlock()
	if gate != nil {
		close(in)
		<-gate
	}
	n, err := g.FakeProc.Write(b)
	if written != nil {
		written()
	}
	return n, err
}

// newGatedSession is newFakeSession over a armedProc.
func newGatedSession(t *testing.T, e *testEnv, id string) (*fakeSession, *armedProc) {
	t.Helper()
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	gp := &armedProc{FakeProc: proc}
	o := session.Options{ScrollbackBytes: 4096, InputWait: testWait, Log: slog.New(slog.DiscardHandler), OnChange: e.srv.localChange, OnActivity: e.srv.localActivity}
	fs := &fakeSession{proc: proc}
	fs.l = session.NewLocal(session.Info{ID: id, Name: id, Cwd: e.root, Cols: 80, Rows: 24, Status: session.StatusRunning}, gp, o)
	if err := e.srv.registry.Add(fs.l); err != nil {
		t.Fatal(err)
	}
	e.srv.router.mark(id)
	return fs, gp
}

// ending waits until the barrier of the link linkID has begun: a hold
// refused while the link is still marked standing.
func ending(t *testing.T, e *testEnv, linkID string) {
	t.Helper()
	until(t, "the link's barrier under way", func() bool {
		h, err := e.srv.links.Hold(linkID)
		if err == nil {
			h.Release()
		}
		return err != nil
	})
}

// replies keeps each decide's reply (beforeReply), as the seam's.
func replies(e *testEnv, more *laneSeams) chan []byte {
	ch := make(chan []byte, 8)
	if more == nil {
		more = &laneSeams{}
	}
	more.beforeReply = func(_ string, f []byte) { ch <- f }
	e.srv.laneSeam = more
	return ch
}

// A link's hold covers its answer's write until the process write has
// returned: a revoke that comes while the program has not taken the keys
// waits for the write (the bound far off), and the link is marked and its
// end told (OnRevoke) only after it.
func TestALinksHoldCoversItsWrite(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.links.HoldWait = time.Hour
	e.srv.viewerCloseWait = 100 * time.Millisecond
	id := sessionHex("hold-write")
	fs, gp := newGatedSession(t, e, id)
	q := fs.ask(t, "Which name?", threeChoices)
	got := replies(e, nil)
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	var mu sync.Mutex
	var order []string
	note := func(v string) { mu.Lock(); order = append(order, v); mu.Unlock() }
	gp.written = func() { note("written") }
	told := e.srv.links.OnRevoke
	e.srv.links.OnRevoke = func(sessionID, linkID string) {
		note("revoked")
		told(sessionID, linkID)
	}
	in, gate := gp.arm()
	w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "w-1"})
	<-in
	revoked := make(chan int, 1)
	go func() {
		resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil)
		revoked <- resp.StatusCode
	}()
	ending(t, e, linkID)
	if _, err := e.srv.links.Live(linkID); err != nil {
		t.Fatalf("the link marked while the program had not taken its keys: %v", err)
	}
	close(gate)
	if code := <-revoked; code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"written", "revoked"}) {
		t.Fatalf("in order: %q", order)
	}
	if r := replyOf(t, <-got); !isOK(r) {
		t.Fatalf("the answer under way: %v", r)
	}
	if r, ok := fs.l.QuestionResult(q); !ok || r.ReqID != answerReqID(linkID, "w-1") {
		t.Fatalf("the answer's result: %+v %v", r, ok)
	}
}

// A link's answer held at its commit boundary, its link held and its
// write not begun, when its link ends (revoked, or expired by the clock
// with the expiry made through the same barrier) is refused there: never
// written, the question still waiting, and the revoke or the expiry
// returns only after the writer let it go.
func TestALinksAnswerAtItsCommitIsRefusedByItsEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		end  func(e *testEnv, id, linkID string, clock func(time.Duration))
	}{
		{"revoked", proto.DecideRevoked, func(e *testEnv, id, linkID string, _ func(time.Duration)) {
			e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil)
		}},
		{"expired", proto.DecideExpired, func(e *testEnv, _, linkID string, clock func(time.Duration)) {
			clock(2 * time.Hour)
			e.srv.links.Expire(linkID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			// The bound far off: the end is judged waiting for the act.
			e.srv.links.HoldWait = time.Hour
			e.srv.viewerCloseWait = 100 * time.Millisecond
			var cmu sync.Mutex
			now := time.Now().UTC()
			e.srv.links.SetNowForTest(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
			clock := func(d time.Duration) { cmu.Lock(); now = now.Add(d); cmu.Unlock() }
			id := sessionHex("commit-" + tc.name)
			fs := newFakeSession(t, e, id, session.Options{})
			q := fs.ask(t, "Which name?", threeChoices)
			at, let := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var mu sync.Mutex
			var order []string
			note := func(v string) { mu.Lock(); order = append(order, v); mu.Unlock() }
			got := replies(e, &laneSeams{afterHold: func(string) {
				once.Do(func() {
					close(at)
					<-let
					note("let")
				})
			}})
			linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 3600)
			w := dialLane(t, e, "/ws/sessions/"+id, tok)
			w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "c-1"})
			<-at
			ended := make(chan struct{})
			go func() {
				tc.end(e, id, linkID, clock)
				note("ended")
				close(ended)
			}()
			ending(t, e, linkID)
			select {
			case <-ended:
				t.Fatal("the link's end returned while its answer was held at its commit")
			default:
			}
			close(let)
			if r := replyOf(t, <-got); !refusedWith(r, tc.code) {
				t.Fatalf("the answer at its commit: %v", r)
			}
			<-ended
			mu.Lock()
			if !slices.Equal(order, []string{"let", "ended"}) {
				t.Fatalf("in order: %q", order)
			}
			mu.Unlock()
			if got := fs.written(); len(got) != 0 {
				t.Fatalf("typed: %q", got)
			}
			if a := fs.l.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
				t.Fatalf("the question: %+v", a)
			}
		})
	}
}

// A task command held between the engine's taking and its change, across
// its link's expiry (the clock past the deadline, the expiry made through
// the link's barrier), commits nothing: refused expired, its receipt
// settled withdrawn, the task as it was; the expiry returns after it.
func TestALinksCommandHeldAcrossItsExpiryCommitsNothing(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	e.srv.links.HoldWait = time.Hour
	var cmu sync.Mutex
	now := time.Now().UTC()
	e.srv.links.SetNowForTest(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
	taken, let := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.runs.SetAfterTakenForTest(func(_, id string) {
		if id == "exp-1" {
			once.Do(func() { close(taken); <-let })
		}
	})
	t.Cleanup(func() { e.srv.runs.SetAfterTakenForTest(nil) })
	got := replies(e, nil)
	e.srv.viewerCloseWait = 100 * time.Millisecond
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 3600)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	retry := actionOf(t, desk.read(ref), crew.TaskRetry)
	desk.sendDecide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "exp-1"})
	<-taken
	cmu.Lock()
	now = now.Add(2 * time.Hour)
	cmu.Unlock()
	expired := make(chan bool, 1)
	go func() { expired <- e.srv.links.Expire(linkID) }()
	close(let)
	if r := replyOf(t, <-got); !refusedWith(r, proto.DecideExpired) {
		t.Fatalf("the command held across its link's expiry: %v", r)
	}
	if !<-expired {
		t.Fatal("the link did not expire")
	}
	row, found, err := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "exp-1")
	if err != nil || !found || row.Status != "failed" {
		t.Fatalf("the command's receipt: %+v %v %v", row, found, err)
	}
	if st, _, err := e.srv.runs.TaskOf(run, "build"); err != nil || st.State != "failed" {
		t.Fatalf("the task after a command its link's expiry withdrew: %+v %v", st, err)
	}
}

// A process write the program does not take within the link's bound
// (HoldWait) was begun before the revoke, and is not taken back: the
// revoke returns once the bound has passed, the write still waiting for
// the program, and its log line says one act was still under way; the
// write then lands as the program reads it and resolves no question of
// the revoked link.
func TestALinksStalledWriteLandsAndAnswersNothingPastTheBound(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, nil)))
	e.srv.links.HoldWait = 100 * time.Millisecond
	e.srv.viewerCloseWait = 100 * time.Millisecond
	id := sessionHex("stalled")
	fs, gp := newGatedSession(t, e, id)
	q := fs.ask(t, "Which name?", threeChoices)
	replies(e, nil)
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	in, gate := gp.arm()
	w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "s-1"})
	<-in
	if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	// Returned within the bound, the write still waiting for the program,
	// and said so.
	select {
	case <-gate:
		t.Fatal("the gate opened")
	default:
	}
	if l := logs.String(); !strings.Contains(l, "link="+linkID) || !strings.Contains(l, "pending=1") {
		t.Fatalf("the revoke's log:\n%s", l)
	}
	close(gate)
	until(t, "the stalled write landed", func() bool { return len(fs.written()) == 1 })
	until(t, "the writer done with the write", func() bool { return goroutinesIn("session.(*procWriter).run(") == 0 })
	if r, ok := fs.l.QuestionResult(q); ok {
		t.Fatalf("the write resolved the revoked link's question: %+v", r)
	}
	if a := fs.l.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
		t.Fatalf("the question: %+v", a)
	}
}

// A decision link's session viewer is closed at its link's deadline
// through the link's barrier, as a desk is: the expiry marks the link,
// waits for an answer whose write was begun before it (its link asked at
// its commit boundary, the program not yet taking the keys), which lands,
// and closes the viewer as expired only after it.
func TestADecisionViewersExpiryWaitsForWhatWasBegun(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.links.HoldWait = time.Hour
	e.srv.viewerCloseWait = 100 * time.Millisecond
	var cmu sync.Mutex
	now := time.Now().UTC()
	e.srv.links.SetNowForTest(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
	id := sessionHex("viewer-lapse")
	fs, gp := newGatedSession(t, e, id)
	q := fs.ask(t, "Which name?", threeChoices)
	fires := make(chan func(), 1)
	got := replies(e, &laneSeams{lapse: func(_ string, fire func()) { fires <- fire }})
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 3600)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	fire := <-fires
	var mu sync.Mutex
	var order []string
	note := func(v string) { mu.Lock(); order = append(order, v); mu.Unlock() }
	gp.written = func() { note("written") }
	in, gate := gp.arm()
	w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "v-1"})
	<-in
	cmu.Lock()
	now = now.Add(2 * time.Hour)
	cmu.Unlock()
	expired := make(chan struct{})
	go func() {
		fire()
		note("expired")
		close(expired)
	}()
	ending(t, e, linkID)
	select {
	case <-expired:
		t.Fatal("the viewer's expiry returned while an answer begun before it was under way")
	default:
	}
	close(gate)
	<-expired
	if r := replyOf(t, <-got); !isOK(r) {
		t.Fatalf("the answer begun before the expiry: %v", r)
	}
	mu.Lock()
	if !slices.Equal(order, []string{"written", "expired"}) {
		t.Fatalf("in order: %q", order)
	}
	mu.Unlock()
	if close, code := closedAs(w.wsClient); close != proto.CloseForbidden || code != proto.ErrCodeExpired {
		t.Fatalf("the viewer closed %d with %q", close, code)
	}
}

// An answer of the asking session's own decision-link connection that
// waits behind another write and has not begun when its connection closes
// is withdrawn with it: never written once the program takes the write
// before it.
func TestAnOwnAnswerQueuedIsWithdrawnWithItsConnection(t *testing.T) {
	e := newTestEnv(t, nil)
	id := sessionHex("own-queued")
	fs, gp := newGatedSession(t, e, id)
	q := fs.ask(t, "Which name?", threeChoices)
	got := replies(e, nil)
	linkID, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", "phone", 0)
	w := dialLane(t, e, "/ws/sessions/"+id, tok)
	owner := dialViewer(t, e, id, adminToken)
	owner.hello(80, 24)
	owner.expectControl(proto.CtlWelcome)
	in, gate := gp.arm()
	owner.send(proto.Encode(proto.TypeInput, []byte("x")))
	<-in
	w.sendDecide(proto.Decide{Ref: questionRef(id, q), Action: "answer", Revision: q, Input: "1", OpID: "q-1"})
	until(t, "the answer waiting behind the write", func() bool { return goroutinesIn("session.(*Local).writeProc(") >= 2 })
	w.c.Close(websocket.StatusNormalClosure, "")
	until(t, "the lane ended", func() bool {
		e.srv.lanes.mu.Lock()
		defer e.srv.lanes.mu.Unlock()
		return e.srv.lanes.links[linkID] == nil
	})
	close(gate)
	<-got
	until(t, "the writer idle", func() bool { return goroutinesIn("session.(*procWriter).run(") == 0 })
	if got := fs.written(); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("written: %q", got)
	}
	if a := fs.l.Info().Attention; a.ID != q || a.State != session.AttentionNeedsInput {
		t.Fatalf("the question: %+v", a)
	}
}

// An answer through the asking session's own decision-link connection is
// kept under the link's label, as one through its claim of another
// session's question is, whatever name its client says or none: "a
// decision link" for a link with no label.
func TestAnOwnAnswerIsKeptUnderTheLinksLabel(t *testing.T) {
	for _, tc := range []struct{ name, hello, label, want string }{
		{"unnamed client", "", "phone", "phone"},
		{"another name", "Mallory", "phone", "phone"},
		{"unlabelled link", "Mallory", "", "a decision link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			id := sessionHex("own-name")
			fs := newFakeSession(t, e, id, session.Options{})
			_, tok := e.decisionLink(t, "/api/sessions/"+id+"/links", tc.label, 0)
			c, err := dialWS(e, "/ws/sessions/"+id, tok)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.CloseNow() })
			w := &laneClient{wsClient: &wsClient{t: t, c: c}}
			w.helloNamed(0, 0, tc.hello)
			w.welcome = w.expectControl(proto.CtlWelcome)
			for _, m := range []proto.Decide{
				{Input: "1", OpID: "n-1"},
				{Text: "Ada", OpID: "n-2"},
			} {
				q := fs.ask(t, "Which name?", threeChoices)
				m.Ref, m.Action, m.Revision = questionRef(id, q), "answer", q
				if r := w.decide(m); !isOK(r) {
					t.Fatalf("the answer: %v", r)
				}
				r, ok := fs.l.QuestionResult(q)
				if !ok || r.ByName != tc.want {
					t.Fatalf("kept as %+v %v, want by %q", r, ok, tc.want)
				}
			}
		})
	}
}

// A task command's track reads only the receipt it names, for its own
// task and its own attempt: a retry of build tracked as a docs item reads
// as none of docs' commands, and an attempt other than the receipt's
// opId is refused.
func TestATrackedCommandReadsOnlyItsOwnTaskAndAttempt(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	_, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	build := "task:" + run + "/build"
	opID, _ := desk.retried(build, "t")
	if st := desk.track(build, opID, opID); st[stepRecorded].State != stepDone {
		t.Fatalf("its own task: %+v", st)
	}
	if st := desk.track("task:"+run+"/docs", opID, opID); st[stepRecorded].State != stepRefused || st[stepValidated].State == stepDone {
		t.Fatalf("build's retry read as docs': %+v", st)
	}
	id := desk.reqID()
	if code := desk.refusedRead(proto.DecisionTrack{T: proto.CtlDecisionTrack, ReqID: id, Ref: build, Receipt: opID, Attempt: "another-op"}, id); code != proto.DecideInvalid {
		t.Fatalf("another attempt's track: %s", code)
	}
}

// A command the engine took and then failed to apply (its change's
// transaction refused after the taking) stays the link's: its opId stays
// bound to the request, so another revision under it is op_conflict, and
// the same request again is answered from its receipt.
func TestAnAcceptedCommandKeepsItsOpID(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failedRun(t, e)
	var cmu sync.Mutex
	var cancelOp context.CancelFunc
	e.srv.runs.SetAfterTakenForTest(func(_, id string) {
		if id == "acc-1" {
			cmu.Lock()
			cancel := cancelOp
			cmu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
	})
	t.Cleanup(func() { e.srv.runs.SetAfterTakenForTest(nil) })
	replies(e, &laneSeams{commandCtx: func(id string, cancel context.CancelFunc) {
		if id == "acc-1" {
			cmu.Lock()
			cancelOp = cancel
			cmu.Unlock()
		}
	}})
	linkID, tok := e.decisionLink(t, "/api/runs/"+run+"/links", "phone", 0)
	desk := dialLane(t, e, "/ws/runs/"+run+"/desk", tok)
	ref := "task:" + run + "/build"
	retry := actionOf(t, desk.read(ref), crew.TaskRetry)
	first := desk.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "acc-1"})
	if isOK(first) {
		t.Fatalf("a command whose change failed: %v", first)
	}
	if _, found, err := e.srv.ledger.Receipt(t.Context(), run, laneActor(linkID), "acc-1"); err != nil || !found {
		t.Fatalf("the engine did not take it: %v %v", found, err)
	}
	if r := desk.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect + "0", OpID: "acc-1"}); !refusedWith(r, proto.DecideOpConflict) {
		t.Fatalf("another revision under the taken opId: %v", r)
	}
	if r := desk.decide(proto.Decide{Ref: ref, Action: crew.TaskRetry, Revision: retry.Expect, OpID: "acc-1"}); refusedWith(r, proto.DecideOpConflict) {
		t.Fatalf("the same request again: %v", r)
	}
}
