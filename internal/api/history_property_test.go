package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// propCall is a hint or stream callback of a recorded session, queued
// (recordDeliver): its session's id, what it carries (a
// session.ActivityEntry, a session.ChatMessage, nil for a hint), and run,
// which delivers it.
type propCall struct {
	id   string
	what any
	run  func()
}

// callbackQueue holds the hints and stream callbacks of the recorded
// sessions (recordDeliver) for the test to deliver late, in any order, or
// not at all.
type callbackQueue struct {
	mu sync.Mutex
	q  []propCall
}

// holdCallbacks has every hint and stream callback of a recorded session
// queued until the test delivers it, from now until the test ends.
func holdCallbacks(t *testing.T, e *testEnv) *callbackQueue {
	t.Helper()
	q := &callbackQueue{}
	f := func(id string, what any, run func()) {
		q.mu.Lock()
		q.q = append(q.q, propCall{id: id, what: what, run: run})
		q.mu.Unlock()
	}
	e.srv.recordDeliver.Store(&f)
	t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
	return q
}

// take takes a call off the queue, at random (rng) or the oldest (rng
// nil): false when the queue is empty.
func (q *callbackQueue) take(rng *rand.Rand) (propCall, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.q) == 0 {
		return propCall{}, false
	}
	i := 0
	if rng != nil {
		i = rng.IntN(len(q.q))
	}
	c := q.q[i]
	q.q = slices.Delete(q.q, i, i+1)
	return c, true
}

func (q *callbackQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.q)
}

// propReg is a server session the property test made.
type propReg struct {
	id         string
	rc         *recorder
	l          *session.Local
	proc       *sessiontest.FakeProc
	stubborn   bool // its process refuses a stop
	stopFailed bool // a stop was asked and failed
	listed     bool // in the registry
	ended      bool // its process ended (the test ended it and saw it)
	recorded   bool // its recorder was its id's as it registered
	epoch      int  // its record's epoch as it registered
	name       string
	n          int      // markers sent
	asked      []string // questions asked whose ending the test has not made
	// unread: the registry's delete made, its removal's final read not yet
	// made (a removal while a reconcile is held): what changes until that
	// read is what it reads.
	unread bool
	// What the test last read (refresh): its recorder held (in recCur, not
	// leaving, the records not closed) and its record open.
	held, open bool
}

// how is the end a final read of r's session shows now (the snapshot
// rule): its process gone, the status it published; running, lost.
func (r *propReg) how() (string, bool) {
	switch {
	case !r.ended:
		return journal.EndingLost, true
	case r.stopFailed:
		return "stopped", false
	default:
		return "exited (exit 0)", false
	}
}

// propLedger is what reached the journal for one record (I4, I5): the
// evidence of a kept kind delivered while the record was open and its
// recorder held, by its key (propKey), the touches the same way, the
// evidence delivered once its end was recorded (dropped and counted), and
// the outcome of each question's ending delivered while it was open.
type propLedger struct {
	kept    map[string]int
	touches map[string]int
	after   int
	endings map[string]string
}

// recordedNow says the recorder registered for id is d's.
func recordedNow(e *testEnv, id string, d session.Driver) bool {
	e.srv.recMu.Lock()
	defer e.srv.recMu.Unlock()
	rc := e.srv.recCur[id]
	return rc != nil && rc.d != nil && session.Driver(rc.d) == d
}

// propRecord is what the test expects of the record of an id: its epoch
// (bumped by each Forget that removed it), whether it is forgotten now,
// and, once its last registration has had its last reconcile, its end
// (how, and whether lost) and its name; what reached it (led); and, for a
// record whose delete failed (detached), the file it came back from.
type propRecord struct {
	epoch     int
	forgotten bool
	final     bool
	how       string
	lost      bool
	name      string
	led       propLedger
	detached  bool
	snap      journal.Record
}

// propHook is the property test's recordHook: a reconcile of holdID held
// at betweenReads until gate closes (reached closed once it is there), and
// each removal's reconcile told as it is reached (removing).
type propHook struct {
	mu       sync.Mutex
	holdID   string
	reached  chan struct{}
	gate     chan struct{}
	removing map[string]chan struct{}
}

func (h *propHook) at(stage, id string) {
	h.mu.Lock()
	switch {
	case stage == "betweenReads" && id == h.holdID && h.reached != nil:
		reached, gate := h.reached, h.gate
		h.holdID, h.reached = "", nil
		h.mu.Unlock()
		close(reached)
		<-gate
		return
	case stage == "beforeRemovedReconcile":
		if ch := h.removing[id]; ch != nil {
			delete(h.removing, id)
			close(ch)
		}
	}
	h.mu.Unlock()
}

// propModel is one run of the property test: the server, its queued
// callbacks, the sessions it made and what it expects of their records.
type propModel struct {
	t        *testing.T
	seed     uint64
	rng      *rand.Rand
	e        *testEnv
	q        *callbackQueue
	hook     *propHook
	regs     []*propReg
	refused  []string
	inflight []*launch // launches the shutdown catches: begun, made or not
	recs     map[string]*propRecord
	// ro: the sessions' folder refuses writes and deletes (setRO), roOK:
	// this platform lets the test make it so; holding: a reconcile is held
	// at betweenReads, so recMu is held; closed: the shutdown has run.
	ro, roOK, holding, closed bool
	bursts                    int
}

func newPropModel(t *testing.T, seed uint64) *propModel {
	t.Helper()
	m := &propModel{t: t, seed: seed, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), recs: map[string]*propRecord{}}
	m.e = newTestEnv(t, nil)
	m.q = holdCallbacks(t, m.e)
	m.hook = &propHook{removing: map[string]chan struct{}{}}
	hook := m.hook.at
	m.e.srv.recordHook.Store(&hook)
	t.Cleanup(func() { m.e.srv.recordHook.Store(nil) })
	dir := m.sessionsDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o500); err == nil {
			probe := filepath.Join(dir, ".probe")
			if err := os.WriteFile(probe, nil, 0o600); err != nil {
				m.roOK = true
			} else {
				os.Remove(probe)
			}
			os.Chmod(dir, 0o700)
		}
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	return m
}

func (m *propModel) fatalf(format string, args ...any) {
	m.t.Helper()
	m.t.Fatalf("seed %d: "+format, append([]any{m.seed}, args...)...)
}

func (m *propModel) sessionsDir() string {
	return filepath.Join(m.e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir)
}

func (m *propModel) recordPath(id string) string {
	return filepath.Join(m.sessionsDir(), id+".json")
}

func (m *propModel) recOf(id string) *propRecord {
	if m.recs[id] == nil {
		m.recs[id] = &propRecord{led: propLedger{kept: map[string]int{}, touches: map[string]int{}, endings: map[string]string{}}}
	}
	return m.recs[id]
}

func (m *propModel) reg(id string) *propReg {
	for _, r := range m.regs {
		if r.id == id {
			return r
		}
	}
	return nil
}

func (m *propModel) pick(ok func(*propReg) bool) *propReg {
	var c []*propReg
	for _, r := range m.regs {
		if ok(r) {
			c = append(c, r)
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c[m.rng.IntN(len(c))]
}

// follows says r's record is the one its registration made, neither
// forgotten nor come back from a failed delete.
func (m *propModel) follows(r *propReg) bool {
	rec := m.recOf(r.id)
	return r.recorded && !rec.forgotten && !rec.detached && r.epoch == rec.epoch
}

// refresh reads what decides where a stream goes: each session's
// recorder held, and its record open (the journal's list). Only a
// reconcile changes either, and every reconcile is the test's to run.
func (m *propModel) refresh() {
	if m.holding {
		return
	}
	open := map[string]bool{}
	if !m.closed {
		after := ""
		for {
			page, err := m.e.srv.sessionRecords.List(after, journal.MaxPage)
			if err != nil {
				m.fatalf("list: %v", err)
			}
			for _, s := range page.Records {
				open[s.ID] = s.EndedAt.IsZero() && s.LostAt.IsZero()
			}
			if page.Next == "" {
				break
			}
			after = page.Next
		}
	}
	m.e.srv.recMu.Lock()
	defer m.e.srv.recMu.Unlock()
	for _, r := range m.regs {
		rc := m.e.srv.recCur[r.id]
		r.held = rc != nil && rc == r.rc && !rc.leaving && !m.e.srv.recordsClosed
		r.open = open[r.id]
	}
}

// propKey is the key of what reached a record, by the table of what is
// kept (journal.Note, journal.Chat): an entry's, or a touch's (touch
// true); ok false for what is not kept, or kept only as a count (a tool).
func propKey(cwd string, what any) (key string, touch, ok bool) {
	switch w := what.(type) {
	case session.ActivityEntry:
		switch w.Type {
		case session.ActivityInput:
			if w.Op == "typed" && w.By == "" {
				return propEntryKey(journal.KindTyped, w.At, w.Message), false, true
			}
		case session.ActivityProgress, session.ActivityArtifact, session.ActivityHandoff, session.ActivityError, session.ActivityToolDenied:
			return propEntryKey(journal.KindReport, w.At, w.Message), false, true
		case session.ActivityStatus:
			return propEntryKey(journal.KindStatus, w.At, w.Message), false, true
		case session.ActivityFile:
			if w.Op != "" && w.Path != "" {
				return session.TouchedAbs(cwd, w.Path) + "|" + w.Op, true, true
			}
		}
	case session.ChatMessage:
		if w.Scope != "" && w.Scope != proto.ChatScopeSession {
			return "", false, false
		}
		switch w.Kind {
		case proto.ChatKindQuestion:
			return "question|" + w.ID, false, true
		case proto.ChatKindMessage:
			return propEntryKey(journal.KindChat, w.At, w.Text), false, true
		case "sent_to_agent":
			return "sent|" + w.Ref + "|" + propStamp(w.At), false, true
		case proto.ChatKindSystem:
			if w.Event == "answered" || w.Event == "withdrawn" {
				return "answer|" + w.Ref + "|" + propStamp(w.At), false, true
			}
		}
	}
	return "", false, false
}

// propOutcome is the outcome a question's ending line gives it.
func propOutcome(w session.ChatMessage) string {
	switch {
	case w.Event == "answered":
		return journal.OutcomeAnswered
	case w.Text == session.ChatLeftTerminal:
		return journal.OutcomeAnsweredInTerminal
	}
	return journal.OutcomeWithdrawn
}

func propStamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

func propEntryKey(kind string, at time.Time, message string) string {
	if len(message) > 64 {
		message = message[:64]
	}
	return kind + "|" + propStamp(at) + "|" + message
}

// propKeyOf is the key of an entry of a record (propKey's).
func propKeyOf(en journal.Entry) string {
	switch {
	case en.Kind == journal.KindQuestion:
		return "question|" + en.ID
	case en.Kind == journal.KindAnswer:
		return "answer|" + en.Ref + "|" + propStamp(en.At)
	case en.Kind == journal.KindChat && en.Event == "sent_to_agent":
		return "sent|" + en.Ref + "|" + propStamp(en.At)
	}
	return propEntryKey(en.Kind, en.At, en.Message)
}

// deliver runs c and notes what reached a record: the evidence of a kept
// kind delivered while its record is open and its recorder held is the
// record's (I4) or counted by the bounds; once its end is recorded, it is
// dropped and counted; with no recorder held, neither. A hint may change
// either, and is read again after it.
func (m *propModel) deliver(c propCall) {
	r := m.reg(c.id)
	var rec *propRecord
	held, open := false, false
	if r != nil && m.follows(r) {
		rec = m.recOf(r.id)
		held, open = r.held, r.open
	}
	c.run()
	if c.what == nil {
		m.refresh()
		return
	}
	key, touch, ok := propKey(m.e.root, c.what)
	if !ok || rec == nil || !held {
		return
	}
	if a, isEntry := c.what.(session.ActivityEntry); isEntry && a.At.IsZero() && !touch {
		m.fatalf("an entry of %s reached its record with no time: %+v", c.id, a)
	}
	switch {
	case !open:
		rec.led.after++
	case touch:
		rec.led.touches[key]++
	default:
		rec.led.kept[key]++
		if w, isChat := c.what.(session.ChatMessage); isChat && w.Kind == proto.ChatKindSystem {
			rec.led.endings[w.Ref] = propOutcome(w)
		}
	}
}

// deliverSome takes up to n callbacks off the queue in a random order,
// running each or dropping it (one in drop).
func (m *propModel) deliverSome(n, drop int) {
	for range n {
		c, ok := m.q.take(m.rng)
		if !ok {
			return
		}
		if drop > 0 && m.rng.IntN(drop) == 0 {
			continue
		}
		m.deliver(c)
	}
}

// deliverAll delivers every callback queued, in its order, none dropped;
// with hints false, every hint is dropped instead.
func (m *propModel) deliverAll(hints bool) {
	for {
		c, ok := m.q.take(nil)
		if !ok {
			return
		}
		if c.what == nil && !hints {
			continue
		}
		m.deliver(c)
	}
}

// make makes a server session, recorded and registered; early: its
// process exits before the registry holds it.
func (m *propModel) make(early, stubborn bool) *propReg {
	proc := sessiontest.NewFakeProc()
	m.t.Cleanup(func() { proc.End(0) })
	var p session.Process = proc
	if stubborn {
		p = stubbornProc{proc}
	}
	l := recordedLocalWith(m.t, m.e, p, session.Options{}, func(l *session.Local) {
		if early {
			proc.End(0)
			<-l.Ended()
		}
	})
	r := &propReg{id: l.Info().ID, l: l, proc: proc, ended: early, stubborn: stubborn}
	m.e.srv.recMu.Lock()
	r.rc = m.e.srv.recCur[r.id]
	m.e.srv.recMu.Unlock()
	m.regs = append(m.regs, r)
	r.listed = true
	r.recorded = recordedNow(m.e, r.id, l)
	r.epoch = m.recOf(r.id).epoch
	r.name = l.Info().Name
	if !r.recorded {
		m.fatalf("a server session not recorded")
	}
	m.refresh()
	return r
}

// last is the final reconcile of r's record as the test knows its
// session: the end its final read shows.
func (m *propModel) last(r *propReg) {
	rec := m.recOf(r.id)
	if !m.follows(r) || rec.final {
		return
	}
	rec.final, rec.name = true, r.name
	rec.how, rec.lost = r.how()
}

func (m *propModel) marker(r *propReg) string {
	r.n++
	return fmt.Sprintf("e%d-%s-%d", r.epoch, r.id[:6], r.n)
}

func (m *propModel) rename(r *propReg, name string) {
	r.l.Rename(name)
	if r.listed || r.unread {
		r.name = name
	}
}

// touch has r's agent report a step and a file.
func (m *propModel) touch(r *propReg) {
	mk := m.marker(r)
	at := time.Now().UTC()
	r.l.RecordServer(session.ActivityEntry{At: at, Type: session.ActivityProgress, Message: mk})
	r.l.RecordServer(session.ActivityEntry{At: at, Type: session.ActivityFile, Op: "edit", Path: mk + ".go"})
}

// burst has r's agent report n steps of 450 bytes each: past MaxRecord
// at 150.
func (m *propModel) burst(r *propReg, n int) {
	for range n {
		mk := m.marker(r)
		r.l.RecordServer(session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: mk + " " + strings.Repeat("x", 450-len(mk))})
	}
}

// files has r's agent report n files, each its own row: past MaxFiles at
// 501.
func (m *propModel) files(r *propReg, n int) {
	for range n {
		r.l.RecordServer(session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityFile, Op: "read", Path: m.marker(r) + ".txt"})
	}
}

// chatLine hands the record a line of r's chat as its session's OnChat
// does (recordHooks).
func (m *propModel) chatLine(r *propReg, c session.ChatMessage) {
	m.e.srv.deliver(r.id, c, func() { m.e.srv.recordChat(r.rc, c) })
}

// ask has r's agent ask a question in the chat.
func (m *propModel) ask(r *propReg) string {
	id := "q-" + m.marker(r)
	m.chatLine(r, session.ChatMessage{ID: id, At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindQuestion, By: session.ChatBy{ID: "agent", Name: "cat", Role: session.RoleAgent}, Text: "which " + id, Options: []session.Option{{Label: "yes", Input: "1"}}})
	r.asked = append(r.asked, id)
	return id
}

// endQuestion ends r's question id as the session says it ended: answered
// through Conductor, answered in the terminal, or withdrawn.
func (m *propModel) endQuestion(r *propReg, id, how string) {
	r.asked = slices.DeleteFunc(r.asked, func(q string) bool { return q == id })
	c := session.ChatMessage{ID: session.NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindSystem, Ref: id, By: session.ChatBy{Name: "Conductor", Role: session.RoleControl}}
	switch how {
	case journal.OutcomeAnswered:
		c.Event, c.Text = "answered", "yes"
	case journal.OutcomeAnsweredInTerminal:
		c.Event, c.Text = "withdrawn", session.ChatLeftTerminal
	default:
		c.Event, c.Text = "withdrawn", session.ChatLeftAgent
	}
	m.chatLine(r, c)
}

// chat is a step of r's chat: a question, or how one asked ended.
func (m *propModel) chat(r *propReg) {
	if len(r.asked) == 0 || m.rng.IntN(3) == 0 {
		m.ask(r)
		return
	}
	hows := []string{journal.OutcomeAnswered, journal.OutcomeAnsweredInTerminal, journal.OutcomeWithdrawn}
	m.endQuestion(r, r.asked[m.rng.IntN(len(r.asked))], hows[m.rng.IntN(len(hows))])
}

// end ends r's process and waits for its session to publish it.
func (m *propModel) end(r *propReg) {
	r.proc.End(0)
	<-r.l.ProcessDone()
	<-r.l.Ended()
	r.ended = true
}

// endRecorded delivers r's callbacks as they come, every one, until its
// record's end is recorded: the session tells its last change after its
// Ended closes, so the hint that reads the end can come after end returns.
func (m *propModel) endRecorded(r *propReg) {
	until(m.t, "the end of "+r.id+" recorded", func() bool {
		m.deliverAll(true)
		return !r.open
	})
}

func (m *propModel) failedStop(r *propReg) {
	if err := r.l.Stop(m.t.Context()); err == nil {
		m.fatalf("the stubborn stop did not fail")
	}
	r.stopFailed = true
}

// remove removes r from the registry, its final reconcile made at once.
func (m *propModel) remove(r *propReg) {
	m.last(r)
	r.listed = false
	m.e.srv.registry.Remove(r.id)
	m.refresh()
}

// forgot notes id forgotten: never a live session's record, and its file
// gone.
func (m *propModel) forgot(id string) {
	m.t.Helper()
	for _, r := range m.regs {
		if r.id == id && r.listed && !r.ended && m.follows(r) {
			m.fatalf("I1, the record of a live session %s was forgotten", id)
		}
	}
	if _, err := os.Stat(m.recordPath(id)); !os.IsNotExist(err) {
		m.fatalf("a record forgotten is still on disk: %v", err)
	}
	rec := m.recOf(id)
	rec.epoch++
	rec.forgotten, rec.detached = true, false
}

// deleteFailed notes that id's delete failed (S11, S35, S36): its row
// comes back from its file, with no instance, and nothing of the instance
// it held is written after; a file that was never written leaves nothing.
func (m *propModel) deleteFailed(id string) {
	raw, err := os.ReadFile(m.recordPath(id))
	if os.IsNotExist(err) {
		m.forgot(id)
		return
	}
	if err != nil {
		m.fatalf("reading %s after its failed delete: %v", id, err)
	}
	rec := m.recOf(id)
	if err := json.Unmarshal(raw, &rec.snap); err != nil {
		m.fatalf("%s after its failed delete: %v", id, err)
	}
	rec.detached = true
}

// ended says id's record is ended or lost as the journal holds it: what
// a removal chooses.
func (m *propModel) ended(id string) bool {
	g, _, err := m.e.srv.sessionRecords.Get(id)
	return err == nil && (!g.EndedAt.IsZero() || !g.LostAt.IsZero())
}

func (m *propModel) forget(id string) {
	switch err := m.e.srv.sessionRecords.Forget(id); {
	case err == nil:
		m.forgot(id)
	case errors.Is(err, journal.ErrLive), errors.Is(err, journal.ErrNotFound):
	case m.ro:
		m.deleteFailed(id)
	default:
		m.fatalf("forget %s: %v", id, err)
	}
}

func (m *propModel) forgetAll() {
	chosen := map[string]bool{}
	for id, rec := range m.recs {
		if !rec.forgotten && m.ended(id) {
			chosen[id] = true
		}
	}
	if _, _, err := m.e.srv.sessionRecords.ForgetAll(); err != nil && !m.ro {
		m.fatalf("forget all: %v", err)
	}
	for id := range chosen {
		if _, err := os.Stat(m.recordPath(id)); os.IsNotExist(err) {
			m.forgot(id)
		} else if m.ro {
			m.deleteFailed(id)
		} else {
			m.fatalf("a record forget all chose is still on disk: %v", err)
		}
	}
}

// setRO has the sessions' folder refuse writes and deletes, or take them
// again.
func (m *propModel) setRO(on bool) {
	if !m.roOK || m.ro == on {
		return
	}
	mode := os.FileMode(0o700)
	if on {
		mode = 0o500
	}
	if err := os.Chmod(m.sessionsDir(), mode); err != nil {
		m.fatalf("chmod: %v", err)
	}
	m.ro = on
}

// checkLive is I1: the record of a registered session whose process runs
// is open.
func (m *propModel) checkLive(what string) {
	for _, r := range m.regs {
		if !r.listed || r.ended || !m.follows(r) {
			continue
		}
		got, _, err := m.e.srv.sessionRecords.Get(r.id)
		if err != nil || !got.EndedAt.IsZero() || !got.LostAt.IsZero() {
			m.fatalf("%s: I1, the record of a live session %s: %v ended %v lost %v %q", what, r.id, err, got.EndedAt, got.LostAt, got.Ending)
		}
	}
}

// held runs steps inside a reconcile of r held at betweenReads, recMu
// held, so the steps are those that do not wait for it: a removal is made
// up to its reconcile, which reads after the hold, and is waited for
// after. False when the reconcile did not reach the seam.
func (m *propModel) held(r *propReg, steps func(removal func(*propReg))) bool {
	reached, gate := make(chan struct{}), make(chan struct{})
	m.hook.mu.Lock()
	m.hook.holdID, m.hook.reached, m.hook.gate = r.id, reached, gate
	m.hook.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.e.srv.reconcile(r.rc)
		close(done)
	}()
	select {
	case <-reached:
	case <-done:
		m.hook.mu.Lock()
		m.hook.holdID, m.hook.reached = "", nil
		m.hook.mu.Unlock()
		return false
	}
	m.holding = true
	var unread []*propReg
	var removals []chan struct{}
	steps(func(r2 *propReg) {
		at, fin := make(chan struct{}), make(chan struct{})
		m.hook.mu.Lock()
		m.hook.removing[r2.id] = at
		m.hook.mu.Unlock()
		go func() {
			m.e.srv.registry.Remove(r2.id)
			close(fin)
		}()
		<-at
		r2.listed, r2.unread = false, true
		unread = append(unread, r2)
		removals = append(removals, fin)
	})
	// What the removals' final reads will read: nothing changes from here
	// until they have.
	for _, r2 := range unread {
		m.last(r2)
		r2.unread = false
	}
	close(gate)
	<-done
	for _, fin := range removals {
		<-fin
	}
	m.holding = false
	m.refresh()
	return true
}

// heldStep is a random held reconcile with up to three steps inside it.
func (m *propModel) heldStep() {
	r := m.pick(func(r *propReg) bool { return r.listed && r.held && m.follows(r) })
	if r == nil {
		return
	}
	m.held(r, func(removal func(*propReg)) {
		for range 1 + m.rng.IntN(3) {
			every := func(*propReg) bool { return true }
			switch m.rng.IntN(9) {
			case 0:
				if x := m.pick(every); x != nil {
					m.rename(x, fmt.Sprintf("held %d", m.rng.IntN(1000)))
				}
			case 1:
				if x := m.pick(every); x != nil {
					m.touch(x)
				}
			case 2:
				if x := m.pick(every); x != nil {
					m.chat(x)
				}
			case 3:
				if x := m.pick(func(x *propReg) bool { return !x.ended }); x != nil {
					m.end(x)
				}
			case 4:
				if x := m.pick(func(x *propReg) bool { return x.listed && x.stubborn && !x.ended && !x.stopFailed }); x != nil {
					m.failedStop(x)
				}
			case 5:
				if x := m.pick(func(x *propReg) bool { return x.listed }); x != nil {
					removal(x)
				}
			case 6:
				m.forgetRandom()
			case 7:
				m.forgetAll()
			default:
				m.e.srv.sessionRecords.Flush()
			}
			m.checkLive("inside a held reconcile")
		}
	})
}

func (m *propModel) forgetRandom() {
	if len(m.recs) == 0 {
		return
	}
	ids := make([]string, 0, len(m.recs))
	for id := range m.recs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	m.forget(ids[m.rng.IntN(len(ids))])
}

// random runs n random steps, two sessions made first so that the steps
// that need one (a held reconcile among them) have one from the start.
func (m *propModel) random(n int) {
	m.make(m.rng.IntN(3) == 0, m.rng.IntN(3) == 0)
	m.make(m.rng.IntN(3) == 0, m.rng.IntN(3) == 0)
	roLeft := 0
	for step := range n {
		what := ""
		every := func(*propReg) bool { return true }
		op := m.rng.IntN(17)
		switch {
		case op == 0 && len(m.regs) < 8:
			what = "made"
			m.make(m.rng.IntN(3) == 0, m.rng.IntN(3) == 0)
		case op == 1:
			what = "a failed stop"
			if r := m.pick(func(r *propReg) bool { return r.listed && r.stubborn && !r.ended && !r.stopFailed }); r != nil {
				m.failedStop(r)
			}
		case op == 2:
			what = "renamed"
			if r := m.pick(every); r != nil {
				m.rename(r, fmt.Sprintf("name %d", step))
			}
		case op == 3:
			what = "touched"
			if r := m.pick(every); r != nil {
				m.touch(r)
			}
		case op == 4:
			what = "ended"
			if r := m.pick(func(r *propReg) bool { return !r.ended }); r != nil {
				m.end(r)
			}
		case op == 5:
			what = "removed"
			if r := m.pick(func(r *propReg) bool { return r.listed }); r != nil {
				m.remove(r)
			}
		case op == 6 && len(m.inflight) < 3:
			what = "in flight"
			lc := beginLaunch(m.t, m.e)
			if m.rng.IntN(2) == 0 {
				lc.make()
				if m.rng.IntN(2) == 0 {
					lc.exit()
				}
			}
			m.inflight = append(m.inflight, lc)
		case op == 7 && !m.ro:
			// A refusal while the folder refuses deletes is S11's, its own
			// test's (TestAFailedAbandonIsListedLost).
			what = "refused"
			lc := beginLaunch(m.t, m.e).make()
			lc.l.RecordServer(session.ActivityEntry{Type: session.ActivityProgress, Message: "refused"})
			m.e.srv.abandon(lc.rc)
			m.refused = append(m.refused, lc.id)
		case op == 8:
			what = "forgotten"
			m.forgetRandom()
		case op == 9:
			what = "forgotten all"
			m.forgetAll()
		case op == 10:
			what = "chat"
			if r := m.pick(every); r != nil {
				m.chat(r)
			}
		case op == 11:
			what = "a held reconcile"
			m.heldStep()
		case op == 12 && m.roOK && !m.ro:
			what = "the disk refuses"
			m.setRO(true)
			roLeft = 1 + m.rng.IntN(4)
		case op == 13 && m.bursts < 1 && m.rng.IntN(3) == 0:
			what = "a burst past the record's bound"
			if r := m.pick(every); r != nil {
				m.bursts++
				m.burst(r, 160)
			}
		default:
			what = "delivered"
			m.deliverSome(m.rng.IntN(m.q.len()+1), 5)
		}
		if m.ro && op != 12 {
			if roLeft--; roLeft <= 0 {
				m.setRO(false)
			}
		}
		m.refresh()
		m.checkLive(fmt.Sprintf("step %d (%s)", step, what))
	}
	m.setRO(false)
	m.deliverSome(m.q.len(), 5)
	m.checkLive("all delivered")
}

// finish runs the shutdown, lets the in-flight launches go after the
// journal's Close, checks every record against the model (I2 to I7), and
// then the next start (I7, I8). It answers the records as the shutdown
// left them.
func (m *propModel) finish() map[string]journal.Record {
	m.setRO(false)
	// The shutdown: the server stops its sessions (a running one ends
	// stopped; a stubborn one's stop fails and it runs on: lost), then the
	// final pass settles every recorder with its session object.
	for _, r := range m.regs {
		if !r.listed {
			continue
		}
		if !r.ended && !r.stubborn {
			r.ended, r.stopFailed = true, true // stopped by the shutdown
		}
		if !r.ended && r.stubborn {
			r.stopFailed = true
		}
		m.last(r)
	}
	shutdown(m.e)
	m.closed = true
	m.refresh()
	m.deliverSome(m.q.len(), 5)
	// The in-flight launches let go after Close write nothing.
	for _, lc := range m.inflight {
		before, _ := os.ReadFile(m.recordPath(lc.id))
		if lc.l == nil {
			lc.make()
		}
		lc.register()
		lc.l.RecordServer(session.ActivityEntry{Type: session.ActivityProgress, Message: "after Close"})
		if after, _ := os.ReadFile(m.recordPath(lc.id)); string(after) != string(before) {
			m.fatalf("I7, a launch let go after Close wrote its record")
		}
	}
	for _, id := range m.refused {
		if _, err := os.Stat(m.recordPath(id)); !os.IsNotExist(err) {
			m.fatalf("I6, a refused launch's record is on disk: %v", err)
		}
	}
	out := map[string]journal.Record{}
	for id, rec := range m.recs {
		if rec.forgotten {
			if _, err := os.Stat(m.recordPath(id)); !os.IsNotExist(err) {
				m.fatalf("I6, the forgotten record of %s was written again: %v", id, err)
			}
			continue
		}
		out[id] = m.check(id, rec)
	}
	for _, lc := range m.inflight {
		r, _ := readRecordFile(m.t, m.e.srv.cfg.DataDir, lc.id)
		out[lc.id] = r
		if !lc.made {
			if !r.EndedAt.IsZero() || !r.LostAt.IsZero() {
				m.fatalf("I7, a launch caught before its session object has an end on disk: %q", r.Ending)
			}
		} else if r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Ending == "" {
			m.fatalf("I2, the record of %s has not exactly one end: %q", lc.id, r.Ending)
		}
	}
	// The next start: an in-flight launch's record left open is lost, and
	// no Seq goes back.
	e2 := m.e.restart()
	for id, before := range out {
		r, _, err := e2.srv.sessionRecords.Get(id)
		if err != nil || r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Seq < before.Seq {
			m.fatalf("I7/I8, %s at the next start: %+v %v (seq before %d)", id, r, err, before.Seq)
		}
	}
	return out
}

// check reads id's record after the shutdown and holds it to the model.
func (m *propModel) check(id string, rec *propRecord) journal.Record {
	r, raw := readRecordFile(m.t, m.e.srv.cfg.DataDir, id)
	if r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Ending == "" {
		m.fatalf("I2, the record of %s has not exactly one end: ended %v lost %v %q", id, r.EndedAt, r.LostAt, r.Ending)
	}
	m.checkQuestions(id, r, raw)
	if rec.detached {
		// I6: what came back from the file is what the file held, and
		// nothing after: its end as written, or lost when it had none.
		s := rec.snap
		same := func(a, b any) bool {
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			return string(x) == string(y)
		}
		if r.Name != s.Name || r.Dropped != s.Dropped || !same(r.Entries, s.Entries) || !same(r.Files, s.Files) {
			m.fatalf("I6, the record of %s changed after its failed delete:\n%s", id, raw)
		}
		if s.EndedAt.IsZero() && s.LostAt.IsZero() {
			if r.Ending != journal.EndingLost || !r.EndedAt.IsZero() {
				m.fatalf("I2, the record of %s whose failed delete left it open: %q", id, r.Ending)
			}
		} else if !r.EndedAt.Equal(s.EndedAt) || !r.LostAt.Equal(s.LostAt) || r.Ending != s.Ending {
			m.fatalf("I2, the end of %s changed after its failed delete: %q, written %q", id, r.Ending, s.Ending)
		}
		return r
	}
	if !rec.final {
		m.fatalf("the test expected no end for %s, and the record has %q", id, r.Ending)
	}
	if rec.lost != !r.LostAt.IsZero() || !strings.HasPrefix(r.Ending, rec.how) {
		m.fatalf("I2, the end of %s: %q (lost %v), its session's final read shows %q (lost %v)", id, r.Ending, !r.LostAt.IsZero(), rec.how, rec.lost)
	}
	if r.Name != rec.name {
		m.fatalf("I3, the name of %s: %q, its session's last %q", id, r.Name, rec.name)
	}
	for _, en := range r.Entries {
		if en.Kind == journal.KindReport && !strings.HasPrefix(en.Message, fmt.Sprintf("e%d-", rec.epoch)) {
			m.fatalf("I6, the record of %s (epoch %d) holds %q, a forgotten instance's:\n%s", id, rec.epoch, en.Message, raw)
		}
	}
	m.checkRetention(id, r, rec, raw)
	return r
}

// checkRetention is I4: each entry of the record reached it while it was
// open, at most once, in order of At then arrival; each file row is
// touches that reached it so; and what reached it and is not there, or
// reached it after its end, is counted in Dropped, exactly. The only
// entries no delivery explains are the withdrawals an observed end made
// (I5's).
func (m *propModel) checkRetention(id string, r journal.Record, rec *propRecord, raw []byte) {
	kept := map[string]int{}
	sent := 0
	for k, n := range rec.led.kept {
		kept[k] = n
		sent += n
	}
	present := 0
	for i, en := range r.Entries {
		if i > 0 && r.Entries[i-1].At.After(en.At) {
			m.fatalf("I4, the entries of %s are out of the order of their time at %d:\n%s", id, i, raw)
		}
		if k := propKeyOf(en); kept[k] > 0 {
			kept[k]--
			present++
			continue
		}
		if en.Kind == journal.KindAnswer && en.Outcome == journal.OutcomeWithdrawn && !r.EndedAt.IsZero() {
			if _, ended := rec.led.endings[en.Ref]; !ended {
				continue // the end's withdrawal (I5)
			}
		}
		m.fatalf("I4, the record of %s holds an entry nothing delivered while it was open (or twice): %+v\n%s", id, en, raw)
	}
	touched, rows := 0, 0
	for _, n := range rec.led.touches {
		touched += n
	}
	for _, f := range r.Files {
		k := f.Path + "|" + f.Op
		if rec.led.touches[k] < f.Count {
			m.fatalf("I4, the record of %s counts %d of %s, %d reached it:\n%s", id, f.Count, k, rec.led.touches[k], raw)
		}
		rows += f.Count
	}
	if want := rec.led.after + (sent - present) + (touched - rows); r.Dropped != want {
		m.fatalf("I4, the record of %s counts %d dropped; %d reached it after its end, %d of %d entries and %d of %d touches are not in it:\n%s", id, r.Dropped, rec.led.after, sent-present, sent, touched-rows, touched, raw)
	}
}

// checkQuestions is I5: in a record with an observed end every question
// has exactly one ending, after it: the one that reached the record while
// it was open, else the end's withdrawal; a lost record adds none, so each
// of its questions has at most one.
func (m *propModel) checkQuestions(id string, r journal.Record, raw []byte) {
	rec := m.recs[id]
	for i, en := range r.Entries {
		if en.Kind != journal.KindQuestion {
			continue
		}
		var endings []journal.Entry
		for j, a := range r.Entries {
			if a.Kind == journal.KindAnswer && a.Ref == en.ID {
				if j < i {
					m.fatalf("I5, the ending of %s's question %s comes before it:\n%s", id, en.ID, raw)
				}
				endings = append(endings, a)
			}
		}
		if r.EndedAt.IsZero() {
			if len(endings) > 1 {
				m.fatalf("I5, the lost record of %s holds %d endings of %s:\n%s", id, len(endings), en.ID, raw)
			}
			continue
		}
		if len(endings) != 1 {
			m.fatalf("I5, the question %s of %s has %d endings:\n%s", en.ID, id, len(endings), raw)
		}
		if rec == nil || rec.detached {
			continue
		}
		want, ok := rec.led.endings[en.ID]
		if !ok {
			want = journal.OutcomeWithdrawn
		}
		if endings[0].Outcome != want {
			m.fatalf("I5, the question %s of %s ended %q, the record was told %q:\n%s", en.ID, id, endings[0].Outcome, want, raw)
		}
	}
}

// The record follows the session whatever the order of its callbacks
// (docs/plans/history.md, "Tests per surface", the invariants I1 to I8): a
// random sequence of what the API sees of several server sessions at once
// (a session made, sometimes exiting before the registry holds it, its
// process sometimes refusing a stop; renamed, touched, its agent's
// questions asked and answered or withdrawn, a burst past the record's
// bound; ended, a stop that fails, removed, forgotten one by one or all
// at once; a launch refused by the registry; launches caught in flight by
// the shutdown, before their session object or before their
// registration, let go after the journal's Close; steps inside a
// reconcile held at betweenReads; the sessions' folder refusing writes and
// deletes for a few steps), with every hint and stream callback queued and
// delivered in a shuffled order, some dropped, some after the shutdown;
// then the next start. After each step the record of a registered session
// whose process runs is open and Forget refuses it (I1); at the end every
// record has exactly one end, the one its session's final read shows, with
// the name it had then (I2, I3); every entry and touch that reached it
// while it was open is in it once, in order, or counted in Dropped with
// what reached it after its end, exactly (I4); every question in a record
// with an observed end has one ending, the one it was told while open or
// the end's withdrawal (I5); nothing of a forgotten or refused record is
// written, and one whose delete failed comes back as its file held it (I6);
// after the shutdown only an in-flight launch's record with no session
// object is open on disk, and the next start marks it lost (I7); a
// record's Seq never goes back across the restart (I8). The fixed examples
// run first. The seed is printed; CONDUCTOR_RECORD_SEED replays one.
func TestTheRecordFollowsTheSession(t *testing.T) {
	t.Run("an answer after the end", func(t *testing.T) { // S17, I4, I5
		m := newPropModel(t, 1)
		r := m.make(false, false)
		q := m.ask(r)
		m.deliverAll(true)
		m.end(r)
		m.endRecorded(r)
		m.endQuestion(r, q, journal.OutcomeAnswered)
		m.deliverAll(true)
		if rec := m.recOf(r.id); rec.led.after != 1 {
			t.Fatalf("the answer after the end reached the record %d times", rec.led.after)
		}
		got := m.finish()[r.id]
		if !withdrawnOnce(got, q) || got.Dropped != 1 {
			t.Fatalf("the record: %+v", got)
		}
	})
	t.Run("a record past its bounds", func(t *testing.T) { // I4, I5
		m := newPropModel(t, 2)
		r := m.make(false, false)
		m.touch(r)
		q := m.ask(r)
		m.endQuestion(r, q, journal.OutcomeAnswered)
		open := m.ask(r)
		m.deliverAll(true)
		m.burst(r, 160)
		m.files(r, journal.MaxFiles+10)
		m.deliverAll(true)
		m.e.srv.sessionRecords.Flush()
		m.end(r)
		m.endRecorded(r)
		got := m.finish()[r.id]
		for _, en := range got.Entries {
			if en.ID == q || en.Ref == q || strings.HasSuffix(en.Message, "-1") {
				t.Fatalf("an early entry outlived the bound: %+v", en)
			}
		}
		if got.Dropped < 10 || len(got.Files) > journal.MaxFiles || withdrawnOnce(got, open) {
			t.Fatalf("the bounds: dropped %d, %d files, the open question's withdrawal kept %v", got.Dropped, len(got.Files), withdrawnOnce(got, open))
		}
	})
	t.Run("a failed stop settled lost by the final pass", func(t *testing.T) { // S25, I1, I2
		m := newPropModel(t, 3)
		r := m.make(false, true)
		m.failedStop(r)
		m.deliverAll(true)
		m.checkLive("the failed stop")
		if got := m.finish()[r.id]; got.Ending != journal.EndingLost {
			t.Fatalf("the record: %+v", got)
		}
	})
	t.Run("a hint lost and the end read by the removal", func(t *testing.T) { // S14, I2
		m := newPropModel(t, 4)
		r := m.make(false, false)
		q := m.ask(r)
		m.end(r)
		m.deliverAll(false)
		m.remove(r)
		if got := m.finish()[r.id]; got.Ending != "exited (exit 0)" || !withdrawnOnce(got, q) {
			t.Fatalf("the record: %+v", got)
		}
	})
	t.Run("a launch caught before its session object", func(t *testing.T) { // S6, I7
		m := newPropModel(t, 5)
		m.inflight = append(m.inflight, beginLaunch(t, m.e))
		m.finish()
	})
	t.Run("an end and a removal inside a held reconcile", func(t *testing.T) { // S15, I2, I5
		m := newPropModel(t, 6)
		r := m.make(false, false)
		q := m.ask(r)
		m.deliverAll(true)
		if !m.held(r, func(removal func(*propReg)) {
			m.end(r)
			removal(r)
		}) {
			t.Fatal("the reconcile did not reach betweenReads")
		}
		if got := m.finish()[r.id]; got.Ending != "exited (exit 0)" || !got.LostAt.IsZero() || !withdrawnOnce(got, q) {
			t.Fatalf("the record: %+v", got)
		}
	})
	t.Run("a failed delete of an unwritten end", func(t *testing.T) { // S35, I6, I7
		m := newPropModel(t, 7)
		if !m.roOK {
			t.Skip("this platform lets a folder that refuses writes be written (root, or Windows)")
		}
		r := m.make(false, false)
		m.touch(r)
		m.deliverAll(true)
		m.e.srv.sessionRecords.Flush()
		m.setRO(true)
		m.end(r)
		m.endRecorded(r)
		m.e.srv.sessionRecords.Flush()
		m.forget(r.id)
		if !m.recOf(r.id).detached {
			t.Fatal("the delete did not fail")
		}
		m.setRO(false)
		if got := m.finish()[r.id]; got.Ending != journal.EndingLost || !got.EndedAt.IsZero() {
			t.Fatalf("the record: %+v", got)
		}
	})

	seed := uint64(time.Now().UnixNano())
	if v := os.Getenv("CONDUCTOR_RECORD_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	t.Logf("seed %d (CONDUCTOR_RECORD_SEED=%d replays it)", seed, seed)
	m := newPropModel(t, seed)
	m.random(20 + m.rng.IntN(40))
	m.finish()
}
