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
	id       string
	l        *session.Local
	proc     *sessiontest.FakeProc
	listed   bool // in the registry
	ended    bool // its end observed (the test ended it and saw it end)
	recorded bool // its recorder was its id's as it registered
	epoch    int  // its record's epoch as it registered
	name     string
	n        int // markers sent
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
	return rc != nil && rc.d == d
}

// The record follows the session whatever the order of its callbacks: a
// session's life driven as a random sequence of what the API sees (a
// server session made, sometimes exiting before the registry holds it; a
// hosted one registered, disconnected, reconnected, let go and registered
// again; renamed, touched, ended, removed, forgotten; the shutdown), with
// every hint and stream callback queued and delivered in a shuffled order,
// some dropped, some after the shutdown. After each step no record of a
// live session is ended or forgettable; at the end every record has
// exactly one end, the one its session's final state says, with the name
// it had then, nothing of a forgotten instance was written after Forget
// returned, and a forgotten record stays gone. The seed is printed;
// CONDUCTOR_RECORD_SEED replays one.
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
	// registered notes a registration as made: recorded or not, and what
	// its record now expects.
	registered := func(r *propReg, d session.Driver) {
		r.listed = true
		r.recorded = recordedNow(e, r.id, d)
		rec := recOf(r.id)
		if r.recorded {
			rec.forgotten, rec.final = false, false
		}
		r.epoch = rec.epoch
		r.name = d.Info().Name
	}
	// last is the last reconcile of r's registration, as the test knows its
	// session: the session's observed end, else lost (host away).
	last := func(r *propReg) {
		rec := recOf(r.id)
		if !r.recorded || rec.forgotten || r.epoch != rec.epoch || rec.final {
			return
		}
		rec.final, rec.name, rec.lost = true, r.name, !r.ended
		switch {
		case r.ended:
			rec.how = "exited (exit 0)"
		default:
			rec.how = journal.EndingLost
		}
	}
	marker := func(r *propReg) string {
		r.n++
		return fmt.Sprintf("e%d-%s-%d", r.epoch, r.id[:6], r.n)
	}
	checkLive := func(step int, what string) {
		for _, r := range regs {
			if rec := recOf(r.id); !r.listed || r.ended || !r.recorded || rec.forgotten || r.epoch != rec.epoch {
				continue
			}
			got, _, err := e.srv.sessionRecords.Get(r.id)
			if err != nil || !got.EndedAt.IsZero() || !got.LostAt.IsZero() {
				t.Fatalf("seed %d, step %d (%s): the record of a live session %s: %v ended %v lost %v %q", seed, step, what, r.id, err, got.EndedAt, got.LostAt, got.Ending)
			}
		}
	}

	steps := 20 + rng.IntN(40)
	for step := range steps {
		what := ""
		switch op := rng.IntN(12); {
		case op == 0 && len(regs) < 8: // a server session made
			what = "server made"
			proc := sessiontest.NewFakeProc()
			t.Cleanup(func() { proc.End(0) })
			early := rng.IntN(3) == 0
			l := recordedLocalWith(t, e, proc, session.Options{}, func(l *session.Local) {
				if early {
					proc.End(0)
					<-l.Ended()
				}
			})
			r := &propReg{id: l.Info().ID, l: l, proc: proc, ended: early}
			regs = append(regs, r)
			registered(r, l)
			if !r.recorded {
				t.Fatalf("seed %d: a server session not recorded", seed)
			}
		case op == 2: // a server session renamed
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
		case op == 4: // a session ended
			r := pick(func(r *propReg) bool { return r.listed && !r.ended })
			if r == nil {
				continue
			}
			what = "ended"
			r.proc.End(0)
			<-r.l.Ended()
			r.ended = true
		case op == 5: // a session removed from the registry
			r := pick(func(r *propReg) bool { return r.listed })
			if r == nil {
				continue
			}
			what = "removed"
			last(r)
			r.listed = false
			e.srv.registry.Remove(r.id)
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
			err := e.srv.sessionRecords.Forget(id)
			switch {
			case err == nil:
				for _, r := range regs {
					if r.id == id && r.listed && !r.ended && r.recorded && !recOf(id).forgotten {
						t.Fatalf("seed %d, step %d: the record of a live session was forgotten", seed, step)
					}
				}
				if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")); !os.IsNotExist(err) {
					t.Fatalf("seed %d, step %d: a record forgotten is still on disk: %v", seed, step, err)
				}
				rec := recOf(id)
				rec.epoch++
				rec.forgotten = true
			case errors.Is(err, journal.ErrLive), errors.Is(err, journal.ErrNotFound):
			default:
				t.Fatalf("seed %d, step %d: forget: %v", seed, step, err)
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
	// stopped) and lets its hosts go (a running one host away).
	for _, r := range regs {
		if !r.listed {
			continue
		}
		if !r.ended {
			rec := recOf(r.id)
			if r.recorded && !rec.forgotten && r.epoch == rec.epoch {
				rec.final, rec.name, rec.lost, rec.how = true, r.name, false, "stopped"
			}
			continue
		}
		last(r)
	}
	shutdown(e)
	q.deliverSome(rng, q.len(), 5)
	for id, rec := range recs {
		path := filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")
		if rec.forgotten {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("seed %d: the forgotten record of %s was written again: %v", seed, id, err)
			}
			continue
		}
		r, raw := readRecordFile(t, e.srv.cfg.DataDir, id)
		if r.EndedAt.IsZero() == r.LostAt.IsZero() || r.Ending == "" {
			t.Fatalf("seed %d: the record of %s has not exactly one end: ended %v lost %v %q", seed, id, r.EndedAt, r.LostAt, r.Ending)
		}
		if !rec.final {
			t.Fatalf("seed %d: the test expected no end for %s, and the record has %q", seed, id, r.Ending)
		}
		if rec.lost != !r.LostAt.IsZero() || !strings.HasPrefix(r.Ending, rec.how) {
			t.Fatalf("seed %d: the end of %s: %q (lost %v), its session's final state says %q (lost %v)", seed, id, r.Ending, !r.LostAt.IsZero(), rec.how, rec.lost)
		}
		if r.Name != rec.name {
			t.Fatalf("seed %d: the name of %s: %q, its session's last %q", seed, id, r.Name, rec.name)
		}
		for _, en := range r.Entries {
			if en.Kind == journal.KindReport && !strings.HasPrefix(en.Message, fmt.Sprintf("e%d-", rec.epoch)) {
				t.Fatalf("seed %d: the record of %s (epoch %d) holds %q, a forgotten instance's:\n%s", seed, id, rec.epoch, en.Message, raw)
			}
		}
	}
}
