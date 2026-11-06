package api

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// callbackQueue holds the hints and stream callbacks of the recorded
// sessions (recordDeliver) for the test to deliver late, in any order, or
// not at all.
type callbackQueue struct {
	mu sync.Mutex
	q  []func()
}

// holdCallbacks has every hint and stream callback of a recorded session
// queued until the test delivers it, from now until the test ends.
func holdCallbacks(t *testing.T, e *testEnv) *callbackQueue {
	t.Helper()
	q := &callbackQueue{}
	f := func(fn func()) {
		q.mu.Lock()
		q.q = append(q.q, fn)
		q.mu.Unlock()
	}
	e.srv.recordDeliver.Store(&f)
	t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
	return q
}

// deliverSome takes up to n callbacks off the queue in a random order,
// running each or dropping it (one in drop), on the test's goroutine.
func (q *callbackQueue) deliverSome(rng *rand.Rand, n, drop int) {
	for range n {
		q.mu.Lock()
		if len(q.q) == 0 {
			q.mu.Unlock()
			return
		}
		i := rng.IntN(len(q.q))
		fn := q.q[i]
		q.q = append(q.q[:i], q.q[i+1:]...)
		q.mu.Unlock()
		if drop > 0 && rng.IntN(drop) == 0 {
			continue
		}
		fn()
	}
}

func (q *callbackQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.q)
}

// propReg is a server session the property test made.
type propReg struct {
	id         string
	l          *session.Local
	proc       *sessiontest.FakeProc
	stubborn   bool // its process refuses a stop
	stopFailed bool // a stop was asked and failed
	listed     bool // in the registry
	ended      bool // its process ended (the test ended it and saw it)
	recorded   bool // its recorder was its id's as it registered
	epoch      int  // its record's epoch as it registered
	name       string
	n          int // markers sent
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

// propRecord is what the test expects of the record of an id: its epoch
// (bumped by each Forget that removed it), whether it is forgotten now,
// and, once its last registration has had its last reconcile, its end
// (how, and whether lost) and its name.
type propRecord struct {
	epoch     int
	forgotten bool
	final     bool
	how       string
	lost      bool
	name      string
}

// recordedNow says the recorder registered for id is d's.
func recordedNow(e *testEnv, id string, d session.Driver) bool {
	e.srv.recMu.Lock()
	defer e.srv.recMu.Unlock()
	rc := e.srv.recCur[id]
	return rc != nil && rc.d != nil && session.Driver(rc.d) == d
}

// The record follows the session whatever the order of its callbacks
// (docs/plans/history.md, "Tests per surface", the invariants I1 to I8): a
// random sequence of what the API sees of several server sessions at once
// (a session made, sometimes exiting before the registry holds it, its
// process sometimes refusing a stop; renamed, touched, ended, a stop that
// fails, removed, forgotten one by one or all at once; a launch refused
// by the registry; launches caught in flight by the shutdown, before
// their session object or before their registration, let go after the
// journal's Close), with every hint and stream callback queued and
// delivered in a shuffled order, some dropped, some after the shutdown;
// then the next start. After each step the record of a registered session
// whose process runs is open and Forget refuses it (I1); at the end every
// record has exactly one end, the one its session's final read shows,
// with the name it had then (I2, I3); nothing of a forgotten or refused
// record is written (I6); after the shutdown only an in-flight launch's
// record with no session object is open on disk, and the next start marks
// it lost (I7); a record's Seq never goes back across the restart (I8).
// The seed is printed; CONDUCTOR_RECORD_SEED replays one.
func TestTheRecordFollowsTheSession(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	if v := os.Getenv("CONDUCTOR_RECORD_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	t.Logf("seed %d (CONDUCTOR_RECORD_SEED=%d replays it)", seed, seed)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	e := newTestEnv(t, nil)
	q := holdCallbacks(t, e)
	var regs []*propReg
	var refused []string
	var inflight []*launch // launches the shutdown catches: begun, made or not
	recs := map[string]*propRecord{}
	recOf := func(id string) *propRecord {
		if recs[id] == nil {
			recs[id] = &propRecord{}
		}
		return recs[id]
	}
	pick := func(ok func(*propReg) bool) *propReg {
		var c []*propReg
		for _, r := range regs {
			if ok(r) {
				c = append(c, r)
			}
		}
		if len(c) == 0 {
			return nil
		}
		return c[rng.IntN(len(c))]
	}
	registered := func(r *propReg, d session.Driver) {
		r.listed = true
		r.recorded = recordedNow(e, r.id, d)
		rec := recOf(r.id)
		r.epoch = rec.epoch
		r.name = d.Info().Name
	}
	// last is the final reconcile of r's record as the test knows its
	// session: the end its final read shows.
	last := func(r *propReg) {
		rec := recOf(r.id)
		if !r.recorded || rec.forgotten || r.epoch != rec.epoch || rec.final {
			return
		}
		rec.final, rec.name = true, r.name
		rec.how, rec.lost = r.how()
	}
	marker := func(r *propReg) string {
		r.n++
		return fmt.Sprintf("e%d-%s-%d", r.epoch, r.id[:6], r.n)
	}
	recordPath := func(id string) string {
		return filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")
	}
	// I1: a registered session whose process runs has an open record that
	// Forget refuses.
	checkLive := func(step int, what string) {
		for _, r := range regs {
			if rec := recOf(r.id); !r.listed || r.ended || !r.recorded || rec.forgotten || r.epoch != rec.epoch {
				continue
			}
			got, _, err := e.srv.sessionRecords.Get(r.id)
			if err != nil || !got.EndedAt.IsZero() || !got.LostAt.IsZero() {
				t.Fatalf("seed %d, step %d (%s): I1, the record of a live session %s: %v ended %v lost %v %q", seed, step, what, r.id, err, got.EndedAt, got.LostAt, got.Ending)
			}
		}
	}
	forgot := func(step int, id string) {
		for _, r := range regs {
			if r.id == id && r.listed && !r.ended && r.recorded && !recOf(id).forgotten {
				t.Fatalf("seed %d, step %d: I1, the record of a live session was forgotten", seed, step)
			}
		}
		if _, err := os.Stat(recordPath(id)); !os.IsNotExist(err) {
			t.Fatalf("seed %d, step %d: a record forgotten is still on disk: %v", seed, step, err)
		}
		rec := recOf(id)
		rec.epoch++
		rec.forgotten = true
	}

	steps := 20 + rng.IntN(40)
	for step := range steps {
		what := ""
		switch op := rng.IntN(13); {
		case op == 0 && len(regs) < 8: // a server session made
			what = "made"
			proc := sessiontest.NewFakeProc()
			t.Cleanup(func() { proc.End(0) })
			early := rng.IntN(3) == 0
			stubborn := rng.IntN(3) == 0
			var p session.Process = proc
			if stubborn {
				p = stubbornProc{proc}
			}
			l := recordedLocalWith(t, e, p, session.Options{}, func(l *session.Local) {
				if early {
					proc.End(0)
					<-l.Ended()
				}
			})
			r := &propReg{id: l.Info().ID, l: l, proc: proc, ended: early, stubborn: stubborn}
			regs = append(regs, r)
			registered(r, l)
			if !r.recorded {
				t.Fatalf("seed %d: a server session not recorded", seed)
			}
		case op == 1: // a stop that fails
			r := pick(func(r *propReg) bool { return r.listed && r.stubborn && !r.ended && !r.stopFailed })
			if r == nil {
				continue
			}
			what = "a failed stop"
			if err := r.l.Stop(t.Context()); err == nil {
				t.Fatalf("seed %d: the stubborn stop did not fail", seed)
			}
			r.stopFailed = true
		case op == 2: // renamed
			r := pick(func(r *propReg) bool { return true })
			if r == nil {
				continue
			}
			what = "renamed"
			name := fmt.Sprintf("name %d", step)
			r.l.Rename(name)
			if r.listed {
				r.name = name
			}
		case op == 3: // evidence: a report, a file
			r := pick(func(r *propReg) bool { return true })
			if r == nil {
				continue
			}
			what = "touched"
			m := marker(r)
			r.l.RecordServer(session.ActivityEntry{Type: session.ActivityProgress, Message: m})
			r.l.RecordServer(session.ActivityEntry{Type: session.ActivityFile, Op: "edit", Path: m + ".go"})
		case op == 4: // a process ended
			r := pick(func(r *propReg) bool { return !r.ended })
			if r == nil {
				continue
			}
			what = "ended"
			r.proc.End(0)
			<-r.l.ProcessDone()
			<-r.l.Ended()
			r.ended = true
		case op == 5: // removed from the registry
			r := pick(func(r *propReg) bool { return r.listed })
			if r == nil {
				continue
			}
			what = "removed"
			last(r)
			r.listed = false
			e.srv.registry.Remove(r.id)
		case op == 6 && len(inflight) < 3: // a launch the shutdown will catch
			what = "in flight"
			lc := beginLaunch(t, e)
			if rng.IntN(2) == 0 {
				lc.make()
				if rng.IntN(2) == 0 {
					lc.exit()
				}
			}
			inflight = append(inflight, lc)
		case op == 7: // a launch the registry refuses
			what = "refused"
			lc := beginLaunch(t, e).make()
			lc.l.RecordServer(session.ActivityEntry{Type: session.ActivityProgress, Message: "refused"})
			e.srv.abandon(lc.rc)
			refused = append(refused, lc.id)
		case op == 8: // a record forgotten
			if len(recs) == 0 {
				continue
			}
			ids := make([]string, 0, len(recs))
			for id := range recs {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			id := ids[rng.IntN(len(ids))]
			what = "forgotten"
			switch err := e.srv.sessionRecords.Forget(id); {
			case err == nil:
				forgot(step, id)
			case errors.Is(err, journal.ErrLive), errors.Is(err, journal.ErrNotFound):
			default:
				t.Fatalf("seed %d, step %d: forget: %v", seed, step, err)
			}
		case op == 9: // every record forgotten that may be
			what = "forgotten all"
			if _, _, err := e.srv.sessionRecords.ForgetAll(); err != nil {
				t.Fatalf("seed %d, step %d: forget all: %v", seed, step, err)
			}
			for id := range recs {
				if _, err := os.Stat(recordPath(id)); os.IsNotExist(err) && !recOf(id).forgotten {
					forgot(step, id)
				}
			}
		default: // callbacks delivered, in any order, some dropped
			what = "delivered"
			q.deliverSome(rng, rng.IntN(q.len()+1), 5)
		}
		checkLive(step, what)
	}
	q.deliverSome(rng, q.len(), 5)
	checkLive(steps, "all delivered")
	// The shutdown: the server stops its sessions (a running one ends
	// stopped; a stubborn one's stop fails and it runs on: lost), then the
	// final pass settles every recorder with its session object.
	for _, r := range regs {
		if !r.listed {
			continue
		}
		if !r.ended && !r.stubborn {
			r.ended, r.stopFailed = true, true // stopped by the shutdown
		}
		if !r.ended && r.stubborn {
			r.stopFailed = true
		}
		last(r)
	}
	shutdown(e)
	q.deliverSome(rng, q.len(), 5)
	// The in-flight launches let go after Close write nothing.
	seqs := map[string]int64{}
	for _, lc := range inflight {
		before, _ := os.ReadFile(recordPath(lc.id))
		if lc.l == nil {
			lc.make()
		}
		lc.register()
		lc.l.RecordServer(session.ActivityEntry{Type: session.ActivityProgress, Message: "after Close"})
		if after, _ := os.ReadFile(recordPath(lc.id)); string(after) != string(before) {
			t.Fatalf("seed %d: I7, a launch let go after Close wrote its record", seed)
		}
	}
	for _, id := range refused {
		if _, err := os.Stat(recordPath(id)); !os.IsNotExist(err) {
			t.Fatalf("seed %d: I6, a refused launch's record is on disk: %v", seed, err)
		}
	}
	check := func(id string, rec *propRecord, open bool) {
		r, raw := readRecordFile(t, e.srv.cfg.DataDir, id)
		seqs[id] = r.Seq
		if open {
			if !r.EndedAt.IsZero() || !r.LostAt.IsZero() {
				t.Fatalf("seed %d: I7, a launch caught before its session object has an end on disk: %q", seed, r.Ending)
			}
			return
		}
		if r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Ending == "" {
			t.Fatalf("seed %d: I2, the record of %s has not exactly one end: ended %v lost %v %q", seed, id, r.EndedAt, r.LostAt, r.Ending)
		}
		if rec == nil {
			return
		}
		if !rec.final {
			t.Fatalf("seed %d: the test expected no end for %s, and the record has %q", seed, id, r.Ending)
		}
		if rec.lost != !r.LostAt.IsZero() || !strings.HasPrefix(r.Ending, rec.how) {
			t.Fatalf("seed %d: I2, the end of %s: %q (lost %v), its session's final read shows %q (lost %v)", seed, id, r.Ending, !r.LostAt.IsZero(), rec.how, rec.lost)
		}
		if r.Name != rec.name {
			t.Fatalf("seed %d: I3, the name of %s: %q, its session's last %q", seed, id, r.Name, rec.name)
		}
		for _, en := range r.Entries {
			if en.Kind == journal.KindReport && !strings.HasPrefix(en.Message, fmt.Sprintf("e%d-", rec.epoch)) {
				t.Fatalf("seed %d: I6, the record of %s (epoch %d) holds %q, a forgotten instance's:\n%s", seed, id, rec.epoch, en.Message, raw)
			}
		}
	}
	for id, rec := range recs {
		if rec.forgotten {
			if _, err := os.Stat(recordPath(id)); !os.IsNotExist(err) {
				t.Fatalf("seed %d: I6, the forgotten record of %s was written again: %v", seed, id, err)
			}
			continue
		}
		check(id, rec, false)
	}
	for _, lc := range inflight {
		check(lc.id, nil, !lc.made)
	}
	// The next start: an in-flight launch's record left open is lost, and
	// no Seq goes back.
	e2 := e.restart()
	for id, seq := range seqs {
		r, _, err := e2.srv.sessionRecords.Get(id)
		if err != nil || r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Seq < seq {
			t.Fatalf("seed %d: I7/I8, %s at the next start: %+v %v (seq before %d)", seed, id, r, err, seq)
		}
	}
}
