package api

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// The record's lifecycle, row by row of the plan's table (docs/plans/
// history.md, "Events, orderings and invariants"), each driven through the
// seams the code offers (recordSeam), never a clock.

// launch is a server session's launch taken step by step as
// createLocalSession takes it: its record begun (begin), its session made
// (make), the registry holding it (add), its registration complete
// (register); a test stops between any two.
type launch struct {
	e    *testEnv
	id   string
	info session.Info
	rc   *recorder
	o    session.Options
	proc *sessiontest.FakeProc
	l    *session.Local
	made bool // its session object made before the final pass (make)
}

func beginLaunch(t *testing.T, e *testEnv) *launch {
	t.Helper()
	id := session.NewID()
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	info := session.Info{ID: id, Name: id, Kind: session.KindServer, AgentID: "cat", Cwd: e.root, Cols: 80, Rows: 24, Status: session.StatusRunning, CreatedAt: time.Now().UTC()}
	lc := &launch{e: e, id: id, info: info, proc: proc}
	lc.rc = e.srv.beginServerRecord(info, session.Launched{AgentID: "cat", Cwd: e.root}, "", "cat", "", ext.WhereNative, gitcli.Revision{}, nil)
	lc.o = session.Options{ScrollbackBytes: 4096, Log: slog.New(slog.DiscardHandler), OnChange: e.srv.localChange, OnActivity: e.srv.localActivity, OnChat: e.srv.localChat}
	e.srv.recordHooks(lc.rc, &lc.o)
	return lc
}

func (lc *launch) make() *launch {
	lc.l = session.NewLocal(lc.info, lc.proc, lc.o)
	lc.made = !lc.e.srv.recordsAreClosed()
	lc.e.srv.made(lc.rc, lc.l)
	return lc
}

// recordsAreClosed says the final pass's mark is set.
func (s *Server) recordsAreClosed() bool {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return s.recordsClosed
}

func (lc *launch) add(t *testing.T) *launch {
	t.Helper()
	if err := lc.e.srv.registry.Add(lc.l); err != nil {
		t.Fatal(err)
	}
	return lc
}

func (lc *launch) register() *launch {
	lc.e.srv.registered(lc.rc)
	return lc
}

// exit ends the session's process and waits for the session to publish it.
func (lc *launch) exit() {
	lc.proc.End(0)
	<-lc.l.Ended()
}

// onRecordStage runs fn once when the record of id reaches stage
// (recordSeam), for as long as the test runs.
func onRecordStage(t *testing.T, e *testEnv, stage, id string, fn func()) {
	t.Helper()
	var once sync.Once
	hook := func(s, sid string) {
		if s == stage && sid == id {
			once.Do(fn)
		}
	}
	e.srv.recordHook.Store(&hook)
	t.Cleanup(func() { e.srv.recordHook.Store(nil) })
}

// hold is a gate a seam waits at: reached closes once it is there.
type hold struct {
	reached, gate chan struct{}
	once          sync.Once
}

func newHold(t *testing.T) *hold {
	h := &hold{reached: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(h.release)
	return h
}

func (h *hold) wait()    { close(h.reached); <-h.gate }
func (h *hold) release() { h.once.Do(func() { close(h.gate) }) }

func noRecordFile(t *testing.T, e *testEnv, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json"))
	return os.IsNotExist(err)
}

// S4: removed between the registry's add and registered. The removal's
// reconcile finds the registration incomplete and does nothing; the
// registration's reads membership absent, final, and the state: the exit.
func TestARemovalBeforeTheRegistrationCompletes(t *testing.T) {
	e := newTestEnv(t, nil)
	lc := beginLaunch(t, e).make().add(t)
	lc.exit()
	e.srv.registry.Remove(lc.id)
	lc.register()
	r, _ := recordFile(t, e, lc.id)
	if r.Ending != "exited (exit 0)" || r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the record: %+v", r)
	}
	e.srv.recMu.Lock()
	defer e.srv.recMu.Unlock()
	if e.srv.recCur[lc.id] != nil {
		t.Fatal("the recorder was not let go")
	}
}

// S5, S8: a launch whose session is made and not yet registered at the
// shutdown is settled final from its session: its process gone, its exit;
// running, lost. Let go after Close, its registration writes nothing.
func TestALaunchInFlightAtShutdownIsSettled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		added bool
		exits bool
		want  string
	}{
		{"spawned, exited", false, true, "exited (exit 0)"},
		{"spawned, running", false, false, journal.EndingLost},
		{"added, exited", true, true, "exited (exit 0)"},
		// In the registry, the shutdown's stops end it first.
		{"added, running", true, false, "stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			lc := beginLaunch(t, e).make()
			if tc.added {
				lc.add(t)
			}
			if tc.exits {
				lc.exit()
			}
			shutdown(e)
			r, before := readRecordFile(t, e.srv.cfg.DataDir, lc.id)
			if !strings.HasPrefix(r.Ending, tc.want) || (tc.want == journal.EndingLost) != r.EndedAt.IsZero() {
				t.Fatalf("the record after the shutdown: %+v", r)
			}
			lc.register()
			lc.l.Record(session.ActivityEntry{Type: session.ActivityProgress, Message: "after the close"})
			if _, after := readRecordFile(t, e.srv.cfg.DataDir, lc.id); string(after) != string(before) {
				t.Fatalf("a launch let go after Close wrote:\n%s", after)
			}
		})
	}
}

// S5: a final read whose session's process is gone before the session has
// published its status records an exit with no exit code, never lost.
func TestAFinalReadBeforeTheStatusIsPublishedRecordsAnExit(t *testing.T) {
	e := newTestEnv(t, nil)
	lc := beginLaunch(t, e)
	proc := &unpublishedProc{FakeProc: lc.proc, done: make(chan struct{})}
	lc.l = session.NewLocal(lc.info, proc, lc.o)
	e.srv.made(lc.rc, lc.l)
	close(proc.done) // the process reaped; its output still open, so no status yet
	if lc.l.Info().Status.Ended() {
		t.Fatal("the status was published")
	}
	shutdown(e)
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, lc.id)
	if r.Ending != string(session.StatusExited) || r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the record: %+v", r)
	}
}

// unpublishedProc is a process whose end (Done) comes while its output
// stays open: the session has not published its status.
type unpublishedProc struct {
	*sessiontest.FakeProc
	done chan struct{}
}

func (p *unpublishedProc) Done() <-chan struct{} { return p.done }

// S6, S8: a launch whose record is begun and whose session is not made at
// the shutdown is left open on disk; let go after Close it writes nothing;
// the next start reads it lost.
func TestALaunchBeforeItsSessionAtShutdownIsLostAtTheNextStart(t *testing.T) {
	e := newTestEnv(t, nil)
	lc := beginLaunch(t, e)
	shutdown(e)
	r, before := readRecordFile(t, e.srv.cfg.DataDir, lc.id)
	if !r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the record after the shutdown: %+v", r)
	}
	lc.make().register()
	if _, after := readRecordFile(t, e.srv.cfg.DataDir, lc.id); string(after) != string(before) {
		t.Fatalf("the launch let go wrote:\n%s", after)
	}
	e2 := e.restart()
	if r, _, err := e2.srv.sessionRecords.Get(lc.id); err != nil || r.Ending != journal.EndingLost || !r.EndedAt.IsZero() {
		t.Fatalf("the next start: %+v %v", r, err)
	}
}

// S7: a launch after the mark begins nothing; a Begin held at afterBegin
// across the mark writes its record open and puts no recorder in place.
func TestABeginAfterTheFinalPassRecordsNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	id := session.NewID()
	h := newHold(t)
	onRecordStage(t, e, "afterBegin", id, h.wait)
	got := make(chan *recorder, 1)
	go func() {
		info := session.Info{ID: id, Name: id, Kind: session.KindServer, AgentID: "cat", Cwd: e.root, Status: session.StatusRunning, CreatedAt: time.Now().UTC()}
		got <- e.srv.beginServerRecord(info, session.Launched{AgentID: "cat"}, "", "cat", "", ext.WhereNative, gitcli.Revision{}, nil)
	}()
	<-h.reached
	shutdown(e)
	h.release()
	if rc := <-got; rc != nil {
		t.Fatal("a recorder was put in place after the mark")
	}
	if r, _ := readRecordFile(t, e.srv.cfg.DataDir, id); !r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the record begun across the mark: %+v", r)
	}
	if lc := beginLaunch(t, e); lc.rc != nil || !noRecordFile(t, e, lc.id) {
		t.Fatal("a launch after the mark has a record")
	}
}

// S9: a refused launch has no record; its session's hints and streams
// after write nothing.
func TestARefusedLaunchLeavesNoRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	lc := beginLaunch(t, e).make()
	e.srv.abandon(lc.rc)
	lc.l.Record(session.ActivityEntry{Type: session.ActivityProgress, Message: "after the refusal"})
	lc.exit()
	e.srv.sessionRecords.Flush()
	if !noRecordFile(t, e, lc.id) {
		t.Fatal("a refused launch has a record")
	}
	if _, _, err := e.srv.sessionRecords.Get(lc.id); err == nil {
		t.Fatal("a refused launch's record is read")
	}
}

// S10: an abandonment held at beforeAbandon while the shutdown runs: the
// final pass waits for it, and the record is removed (were the journal
// closed first, its delete would be refused and the file would stay). And
// one that begins after the final pass settled its recorder removes
// nothing: the record as settled.
func TestARefusedLaunchAtTheFinalPass(t *testing.T) {
	e := newTestEnv(t, nil)
	lc := beginLaunch(t, e).make()
	h := newHold(t)
	onRecordStage(t, e, "beforeAbandon", lc.id, h.wait)
	abandoned := make(chan struct{})
	go func() {
		e.srv.abandon(lc.rc)
		close(abandoned)
	}()
	<-h.reached
	shut := make(chan struct{})
	go func() {
		shutdown(e)
		close(shut)
	}()
	h.release()
	<-abandoned
	<-shut
	if !noRecordFile(t, e, lc.id) {
		t.Fatal("the refused launch's record outlived the shutdown")
	}

	e2 := newTestEnv(t, nil)
	late := beginLaunch(t, e2).make()
	late.exit()
	shutdown(e2)
	e2.srv.abandon(late.rc)
	if r, _ := readRecordFile(t, e2.srv.cfg.DataDir, late.id); r.Ending != "exited (exit 0)" {
		t.Fatalf("the record the final pass settled: %+v", r)
	}
}

// S14: every hint dropped; the removal's reconcile reads the end.
func TestALostHintIsReadByTheRemoval(t *testing.T) {
	e := newTestEnv(t, nil)
	drop := func(func()) {}
	e.srv.recordDeliver.Store(&drop)
	t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
	lc := beginLaunch(t, e).make().add(t).register()
	lc.exit()
	if r, _ := recordFile(t, e, lc.id); !r.EndedAt.IsZero() {
		t.Fatalf("ended with every hint dropped: %+v", r)
	}
	e.srv.registry.Remove(lc.id)
	if r, _ := recordFile(t, e, lc.id); r.Ending != "exited (exit 0)" {
		t.Fatalf("the removal's read: %+v", r)
	}
}

// S15, S16: a hint's reconcile held between its membership read (a
// member) and its state read while the process ends and the removal is
// made: it reads the end, not final, and the removal's reconcile, after
// it, keeps that end, never lost. Then the other order: the removal first,
// the hint's membership read after it, final.
func TestAnEndBeforeTheRemovalIsNeverReadStale(t *testing.T) {
	for _, removalFirst := range []bool{false, true} {
		e := newTestEnv(t, nil)
		lc := beginLaunch(t, e).make().add(t).register()
		q := "q-" + lc.id
		e.srv.recordChat(lc.rc, session.ChatMessage{ID: q, Kind: "question", Text: "Which?", By: session.ChatBy{Role: session.RoleAgent}, At: time.Now()})
		drop := func(func()) {}
		e.srv.recordDeliver.Store(&drop)
		if removalFirst {
			lc.exit()
			h := newHold(t)
			onRecordStage(t, e, "beforeRemovedReconcile", lc.id, h.wait)
			removed := make(chan struct{})
			go func() {
				e.srv.registry.Remove(lc.id)
				close(removed)
			}()
			<-h.reached
			e.srv.reconcile(lc.rc) // the hint's membership read after the delete: final
			h.release()
			<-removed
		} else {
			h := newHold(t)
			onRecordStage(t, e, "betweenReads", lc.id, h.wait)
			hinted := make(chan struct{})
			go func() {
				e.srv.reconcile(lc.rc)
				close(hinted)
			}()
			<-h.reached
			lc.exit()
			removed := make(chan struct{})
			go func() {
				e.srv.registry.Remove(lc.id)
				close(removed)
			}()
			until(t, "the registry's delete", func() bool { _, ok := e.srv.registry.Get(lc.id); return !ok })
			h.release()
			<-hinted
			<-removed
		}
		r, _ := recordFile(t, e, lc.id)
		if r.Ending != "exited (exit 0)" || !r.LostAt.IsZero() || !withdrawnOnce(r, q) {
			t.Fatalf("removal first %v: %+v", removalFirst, r)
		}
	}
}

// S21: a stop that fails, the stopped session removed with OnRemove held
// at beforeRemovedReconcile while its process exits: the final read finds
// it gone, stopped. And the reconcile let go first, the process exiting
// after: lost, still lost after a Flush.
func TestTheFinalReadDecidesTheEnd(t *testing.T) {
	for _, exitsFirst := range []bool{true, false} {
		e := newTestEnv(t, nil)
		l, proc := stubbornSession(t, e, session.Options{})
		id := l.Info().ID
		if err := l.Stop(t.Context()); err == nil {
			t.Fatal("the stop did not fail")
		}
		h := newHold(t)
		onRecordStage(t, e, "beforeRemovedReconcile", id, h.wait)
		removed := make(chan struct{})
		go func() {
			e.srv.registry.Remove(id)
			close(removed)
		}()
		<-h.reached
		if exitsFirst {
			proc.End(0)
			<-l.ProcessDone()
		}
		h.release()
		<-removed
		if !exitsFirst {
			proc.End(0)
			<-l.Ended()
		}
		r, _ := recordFile(t, e, id)
		switch {
		case exitsFirst && (!strings.HasPrefix(r.Ending, "stopped") || r.EndedAt.IsZero()):
			t.Fatalf("exited before the final read: %+v", r)
		case !exitsFirst && (r.Ending != journal.EndingLost || !r.EndedAt.IsZero()):
			t.Fatalf("exited after the final read: %+v", r)
		}
	}
}

// S13: a hint carries no truth: hints handed an older Info last change
// nothing; the record holds the session's own state at the last read.
func TestAHintCarriesNoTruth(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	var queued []func()
	q := func(f func()) { mu.Lock(); queued = append(queued, f); mu.Unlock() }
	e.srv.recordDeliver.Store(&q)
	t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
	lc := beginLaunch(t, e).make().add(t).register()
	lc.l.Rename("first")
	lc.l.Rename("second")
	mu.Lock()
	hints := queued
	queued = nil
	mu.Unlock()
	for i := len(hints) - 1; i >= 0; i-- { // the older hint last
		hints[i]()
	}
	if r, _ := recordFile(t, e, lc.id); r.Name != "second" {
		t.Fatalf("the name %q", r.Name)
	}
}

// S17: a question, the session's end settled, then the choice's answered
// line delivered: dropped and counted, the question withdrawn by the end.
func TestAnAnswerAfterTheEndReadsWithdrawn(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	var queued []func()
	q := func(f func()) { mu.Lock(); queued = append(queued, f); mu.Unlock() }
	e.srv.recordDeliver.Store(&q)
	t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
	lc := beginLaunch(t, e).make().add(t).register()
	qid := "q-" + lc.id
	e.srv.recordChat(lc.rc, session.ChatMessage{ID: qid, Kind: "question", Text: "Which?", By: session.ChatBy{Role: session.RoleAgent}, At: time.Now()})
	lc.exit()
	e.srv.reconcile(lc.rc) // the end settled
	e.srv.recordChat(lc.rc, session.ChatMessage{ID: "a-" + lc.id, Kind: "system", Event: "answered", Ref: qid, Text: "Postgres", At: time.Now().Add(-time.Minute)})
	r, raw := recordFile(t, e, lc.id)
	if r.Ending != "exited (exit 0)" || !withdrawnOnce(r, qid) || r.Dropped != 1 || strings.Contains(string(raw), "Postgres") {
		t.Fatalf("the record:\n%s", raw)
	}
}
