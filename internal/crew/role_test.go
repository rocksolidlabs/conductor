package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The background role's loop (docs/plans/brakeman.md, BK3, PR-14), over
// records of the test's: every timing is the role's clock, which the test
// moves, and every step is the test's own call, never a sleep.

// roleClock is a clock the test moves.
type roleClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *roleClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *roleClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeRecords keeps the role's record as a copy of its own, as a file
// would, and its turns as requests by batch id: Fire of a batch again
// answers the request it made.
type fakeRecords struct {
	mu       sync.Mutex
	doc      *RoleRecord
	settings RoleSettings
	requests map[string]string // batch → request
	open     map[string]bool   // request → open
	outcome  map[string]RoleOutcome
	fires    int
	// turns numbers the requests made, the role's and a person's alike
	// (ask), as the store does.
	turns  int64
	refuse *RoleRefused
	// fireErr fails every Fire before its request is made.
	fireErr error
	// saveFails makes the next SaveRole fail, as a crash would;
	// settingsErr every RoleSettings, as a configuration that cannot be
	// read.
	saveFails   bool
	settingsErr error
	// afterFire runs once Fire has made its request.
	afterFire func()
}

func newFakeRecords() *fakeRecords {
	return &fakeRecords{
		settings: RoleSettings{On: true, Window: 2 * time.Minute, Cooldown: 30 * time.Minute, Location: time.UTC},
		requests: map[string]string{}, open: map[string]bool{}, outcome: map[string]RoleOutcome{},
	}
}

func (f *fakeRecords) LoadRole() (RoleRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.doc == nil {
		return RoleRecord{}, false, nil
	}
	return cloneRole(*f.doc), true, nil
}

func (f *fakeRecords) SaveRole(r RoleRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveFails {
		f.saveFails = false
		return errors.New("the disk refused the write")
	}
	c := cloneRole(r)
	f.doc = &c
	return nil
}

func (f *fakeRecords) RoleSettings() (RoleSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settingsErr != nil {
		return RoleSettings{}, f.settingsErr
	}
	return f.settings, nil
}

func (f *fakeRecords) Fire(b Batch) (string, error) {
	f.mu.Lock()
	if f.refuse != nil {
		defer f.mu.Unlock()
		return "", f.refuse
	}
	if f.fireErr != nil {
		defer f.mu.Unlock()
		return "", f.fireErr
	}
	id, ok := f.requests[b.ID]
	if !ok {
		f.fires++
		f.turns++
		id = fmt.Sprintf("br-%d", f.turns)
		f.requests[b.ID] = id
		f.open[id] = true
	}
	after := f.afterFire
	f.mu.Unlock()
	if after != nil {
		after()
	}
	return id, nil
}

func (f *fakeRecords) Outcome(id string) (RoleOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.open[id] {
		return RoleOutcome{Open: true}, nil
	}
	out := f.outcome[id]
	if out.Unavailable != "" {
		fmt.Sscanf(id, "br-%d", &out.Turn)
	}
	return out, nil
}

// ask makes a person's request, numbered after every request made
// before it, and answers its number.
func (f *fakeRecords) ask() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turns++
	return f.turns
}

// close closes a request, with what came of it.
func (f *fakeRecords) close(id string, out RoleOutcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.open, id)
	f.outcome[id] = out
}

func (f *fakeRecords) fired() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fires
}

// roleFixture is a role over fake records, on a clock of the test's.
type roleFixture struct {
	t     *testing.T
	clock *roleClock
	rec   *fakeRecords
	role  *Role
}

func newRoleFixture(t *testing.T) *roleFixture {
	f := &roleFixture{t: t, clock: &roleClock{t: time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)}, rec: newFakeRecords()}
	f.role = NewRole("brakeman", f.rec, f.clock.now)
	return f
}

// restart is a new role over the same records, as after a server's
// restart.
func (f *roleFixture) restart() {
	f.role = NewRole("brakeman", f.rec, f.clock.now)
}

// step steps the role and answers when the next thing falls due.
func (f *roleFixture) step() time.Duration {
	f.t.Helper()
	next, err := f.role.Step()
	if err != nil {
		f.t.Fatal(err)
	}
	return next
}

// record is the role as it stands.
func (f *roleFixture) record() RoleRecord {
	f.t.Helper()
	r, err := f.role.Record()
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// state steps and checks the state.
func (f *roleFixture) state(want string) RoleRecord {
	f.t.Helper()
	f.step()
	r := f.record()
	if r.State != want {
		f.t.Fatalf("the role is %s (%s), want %s", r.State, r.Why, want)
	}
	return r
}

func (f *roleFixture) failed(task string, attempts int) {
	f.t.Helper()
	if err := f.role.Wake(RoleWake{Source: WakeTaskFailed, Subject: "task:run-1/" + task, Key: fmt.Sprintf("attempts %d", attempts)}); err != nil {
		f.t.Fatal(err)
	}
}

// TestAWakeGathersThenBriefsThenCools: a task failing opens a batch (the
// role gathering, written before the window), the window's end fires one
// turn (briefing), its end cools the role for the cooldown, then idle.
func TestAWakeGathersThenBriefsThenCools(t *testing.T) {
	f := newRoleFixture(t)
	f.state(RoleIdle)
	f.failed("build", 1)
	r := f.record()
	if r.State != RoleGathering || r.Open == nil || r.Open.ID != "wb-1" || len(r.Log) != 1 || r.Log[0].Batch != "wb-1" {
		t.Fatalf("%+v", r)
	}
	if next := f.step(); next != 2*time.Minute {
		t.Fatalf("the window ends in %s", next)
	}
	f.clock.add(2 * time.Minute)
	r = f.state(RoleBriefing)
	if r.Request != "br-1" || r.Fired == nil || r.Fired.ID != "wb-1" || r.Open != nil || r.Log[0].Request != "br-1" {
		t.Fatalf("%+v", r)
	}
	f.rec.close("br-1", RoleOutcome{})
	r = f.state(RoleCooling)
	if !r.CoolUntil.Equal(f.clock.now().Add(30 * time.Minute)) {
		t.Fatalf("cools until %s", r.CoolUntil)
	}
	f.clock.add(30 * time.Minute)
	f.state(RoleIdle)
	if f.rec.fired() != 1 {
		t.Fatalf("%d turns", f.rec.fired())
	}
}

// TestWakesInTheWindowAreOneBatch: a second failure inside the window
// joins the first's batch, and one turn reads both; the same task again
// coalesces with a count.
func TestWakesInTheWindowAreOneBatch(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(time.Minute)
	f.failed("lint", 1)
	f.failed("build", 2)
	f.clock.add(time.Minute)
	r := f.state(RoleBriefing)
	if f.rec.fired() != 1 || len(r.Fired.Wakes) != 2 || r.Fired.Wakes[0].Count != 2 || r.Fired.Wakes[1].Count != 1 {
		t.Fatalf("%d turns, %+v", f.rec.fired(), r.Fired)
	}
	for _, row := range r.Log {
		if row.Batch != "wb-1" || row.Request != "br-1" {
			t.Fatalf("%+v", row)
		}
	}
	// The same firing twice (a restart reading a failed task again) is one.
	f.failed("lint", 1)
	if r := f.record(); len(r.Log) != 3 || r.Open != nil {
		t.Fatalf("a wake fired twice: %+v", r)
	}
}

// TestTheCooldownHoldsTheNextBatch: wakes while briefing and cooling
// join the next batch, which fires only once the cooldown ends.
func TestTheCooldownHoldsTheNextBatch(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	f.failed("lint", 1) // while briefing
	f.rec.close("br-1", RoleOutcome{})
	r := f.state(RoleCooling)
	if r.Open == nil || r.Open.ID != "wb-2" {
		t.Fatalf("%+v", r)
	}
	f.clock.add(10 * time.Minute)
	f.failed("test", 1) // while cooling
	if next := f.step(); next != 20*time.Minute {
		t.Fatalf("the cooldown ends in %s", next)
	}
	if f.rec.fired() != 1 {
		t.Fatal("the cooldown did not hold the next batch")
	}
	f.clock.add(20 * time.Minute)
	r = f.state(RoleBriefing)
	if f.rec.fired() != 2 || r.Fired.ID != "wb-2" || len(r.Fired.Wakes) != 2 {
		t.Fatalf("%+v", r.Fired)
	}
}

// TestTheBudgetStopsTurnsAndSaysSo: a turn the budget refuses leaves the
// role unavailable with the refusal's words, the batch kept; the next day
// it tries again by itself and fires the batch once.
func TestTheBudgetStopsTurnsAndSaysSo(t *testing.T) {
	f := newRoleFixture(t)
	tomorrow := f.clock.now().Add(3 * time.Hour)
	f.rec.refuse = &RoleRefused{Why: "the day's budget is spent: 40 of 40 turns", RetryAt: tomorrow}
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	r := f.state(RoleUnavailable)
	if r.Why != "the day's budget is spent: 40 of 40 turns" || !r.RetryAt.Equal(tomorrow) || r.Open == nil {
		t.Fatalf("%+v", r)
	}
	// A turn closed over budget at its dequeue says so too.
	if next := f.step(); next != tomorrow.Sub(f.clock.now()) {
		t.Fatalf("tries again in %s", next)
	}
	f.failed("lint", 1) // joins the batch kept
	f.rec.refuse = nil
	f.clock.add(3 * time.Hour)
	r = f.state(RoleBriefing)
	if f.rec.fired() != 1 || len(r.Fired.Wakes) != 2 {
		t.Fatalf("%d turns %+v", f.rec.fired(), r.Fired)
	}
	f.rec.close("br-1", RoleOutcome{Unavailable: "the day's budget is spent: 240 of 240 minutes", RetryAt: tomorrow.Add(24 * time.Hour)})
	r = f.state(RoleUnavailable)
	if !strings.Contains(r.Why, "240 of 240 minutes") {
		t.Fatalf("%+v", r)
	}
	// Closed over budget at its dequeue, the turn ran none: its batch is
	// kept under a new id, fired the next day.
	if r.Open == nil || r.Open.ID != "wb-2" || len(r.Open.Wakes) != 2 || r.Open.Wakes[0].Subject != "task:run-1/build" {
		t.Fatalf("the batch was not kept: %+v", r.Open)
	}
	f.clock.add(24 * time.Hour)
	if r := f.state(RoleBriefing); f.rec.fired() != 2 || r.Fired.ID != "wb-2" {
		t.Fatalf("%d turns %+v", f.rec.fired(), r.Fired)
	}
}

// TestNoModelIsPolled: with no wake, a day of steps starts no turn and
// the role asks for nothing on its own (no due time).
func TestNoModelIsPolled(t *testing.T) {
	f := newRoleFixture(t)
	for range 48 {
		if next := f.step(); next != 0 {
			t.Fatalf("an idle role has something due in %s", next)
		}
		f.clock.add(30 * time.Minute)
	}
	if f.rec.fired() != 0 || f.record().State != RoleIdle {
		t.Fatalf("%d turns without a wake", f.rec.fired())
	}
}

// TestARestartFiresAnOpenBatchOnce: a role restarted while gathering
// keeps its batch and fires it once its window has passed; restarted
// while briefing, it follows the request (interrupted at the restart) to
// cooling and fires nothing again.
func TestARestartFiresAnOpenBatchOnce(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.restart()
	if r := f.record(); r.State != RoleGathering || r.Open == nil || r.Open.ID != "wb-1" {
		t.Fatalf("read back %+v", r)
	}
	f.clock.add(5 * time.Minute)
	f.state(RoleBriefing)
	f.restart()
	f.rec.close("br-1", RoleOutcome{}) // interrupted by the restart
	f.state(RoleCooling)
	f.restart()
	f.state(RoleCooling)
	if f.rec.fired() != 1 {
		t.Fatalf("%d turns", f.rec.fired())
	}
}

// TestACrashBetweenTheRequestAndTheRoleFiresOnce: the batch is frozen
// and written as the one firing, its request is made, and the role's
// write after it fails, as a crash would leave it. A wake arriving then,
// once the storage is back and while the request may already have read
// its batch, joins the next batch, never the one firing, whether the
// role goes on with the record it holds or a restarted one reads it back;
// the next step fires the frozen batch again and finds the request made,
// so one turn runs, for the wakes it read.
func TestACrashBetweenTheRequestAndTheRoleFiresOnce(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			f := newRoleFixture(t)
			f.failed("build", 1)
			f.clock.add(2 * time.Minute)
			f.rec.afterFire = func() {
				f.rec.mu.Lock()
				f.rec.saveFails = true
				f.rec.mu.Unlock()
			}
			if _, err := f.role.Step(); err == nil {
				t.Fatal("the role's write after the request did not fail")
			}
			f.rec.afterFire = nil
			if restart {
				f.restart()
			}
			if r := f.record(); r.State != RoleBriefing || !r.Firing || r.Fired == nil || r.Fired.ID != "wb-1" || r.Open != nil {
				t.Fatalf("after the failed write %+v", r)
			}
			// The request's turn reads its batch now; a wake arriving after
			// joins the next batch.
			f.failed("lint", 1)
			r := f.record()
			if r.Open == nil || r.Open.ID != "wb-2" || len(r.Fired.Wakes) != 1 || r.Fired.Wakes[0].Subject != "task:run-1/build" {
				t.Fatalf("the wake after the request joined the batch firing: %+v", r)
			}
			r = f.state(RoleBriefing)
			if f.rec.fired() != 1 || r.Request != "br-1" || r.Firing || r.Fired.ID != "wb-1" || len(r.Fired.Wakes) != 1 || r.Open == nil || r.Open.ID != "wb-2" {
				t.Fatalf("%d turns %+v", f.rec.fired(), r)
			}
			for _, row := range r.Log {
				if want := map[string]string{"wb-1": "br-1", "wb-2": ""}[row.Batch]; row.Request != want {
					t.Fatalf("the log: %+v", r.Log)
				}
			}
			// Its turn ends and the cooldown passes: the next batch fires
			// a turn of its own.
			f.rec.close("br-1", RoleOutcome{})
			f.state(RoleCooling)
			f.clock.add(30 * time.Minute)
			if r := f.state(RoleBriefing); f.rec.fired() != 2 || r.Request != "br-2" || r.Fired.ID != "wb-2" {
				t.Fatalf("%d turns %+v", f.rec.fired(), r)
			}
		})
	}
}

// TestARefusedFireGathersAgain: the batch frozen to fire cannot reach
// its owner's store (Fire fails), so it stays frozen and a wake meanwhile
// opens the next batch; then the turn is refused (the day's budget
// spent), and the frozen batch gathers again with that wake folded in,
// under its own id, and fires once the role can start a turn.
func TestARefusedFireGathersAgain(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.rec.fireErr = errors.New("the store is unreachable")
	if _, err := f.role.Step(); err == nil {
		t.Fatal("the failed request was not said")
	}
	f.failed("lint", 1)
	if r := f.record(); !r.Firing || r.Fired.ID != "wb-1" || r.Open == nil || r.Open.ID != "wb-2" {
		t.Fatalf("the batch frozen: %+v", r)
	}
	f.rec.mu.Lock()
	f.rec.fireErr, f.rec.refuse = nil, &RoleRefused{Why: "the day's budget is spent"}
	f.rec.mu.Unlock()
	r := f.state(RoleUnavailable)
	if r.Firing || r.Fired != nil || r.Open == nil || r.Open.ID != "wb-1" || len(r.Open.Wakes) != 2 {
		t.Fatalf("the refused batch: %+v", r)
	}
	for _, row := range r.Log {
		if row.Batch != "wb-1" {
			t.Fatalf("the log: %+v", r.Log)
		}
	}
	f.rec.mu.Lock()
	f.rec.refuse = nil
	f.rec.mu.Unlock()
	if err := f.role.Asked(RoleWake{Subject: "br-9", Key: "asked"}); err != nil {
		t.Fatal(err)
	}
	r = f.state(RoleBriefing)
	if f.rec.fired() != 1 || r.Fired.ID != "wb-1" || len(r.Fired.Wakes) != 2 {
		t.Fatalf("%d turns %+v", f.rec.fired(), r)
	}
}

// TestABatchPastTwoHundredWakesOverflows: eleven failures are eleven
// wakes of one batch; 201 distinct ones keep 200 and count the last in
// the overflow, with its time.
func TestABatchPastTwoHundredWakesOverflows(t *testing.T) {
	f := newRoleFixture(t)
	for i := range 11 {
		f.failed(fmt.Sprintf("t%d", i), 1)
	}
	f.clock.add(2 * time.Minute)
	if r := f.state(RoleBriefing); len(r.Fired.Wakes) != 11 || r.Fired.Overflow != nil {
		t.Fatalf("%+v", r.Fired)
	}
	f.rec.close("br-1", RoleOutcome{})
	f.state(RoleCooling)
	first := f.clock.now()
	for i := range MaxBatchWakes + 1 {
		if i == MaxBatchWakes {
			f.clock.add(time.Second)
		}
		f.failed(fmt.Sprintf("u%d", i), 1)
	}
	r := f.record()
	if len(r.Open.Wakes) != MaxBatchWakes || r.Open.Overflow == nil || r.Open.Overflow.Count != 1 || !r.Open.Overflow.Last.Equal(first.Add(time.Second)) {
		t.Fatalf("%d wakes, overflow %+v", len(r.Open.Wakes), r.Open.Overflow)
	}
	if len(r.Log) != 11+MaxBatchWakes+1 {
		t.Fatalf("the log holds %d", len(r.Log))
	}
}

// TestBriefingTimesInTheLocalZone: a briefing at 07:00 fires at 07:00 in
// the server's zone, whatever UTC says; a role turned on after it fires
// none of the past, and the next briefing is due the next morning.
func TestBriefingTimesInTheLocalZone(t *testing.T) {
	f := newRoleFixture(t)
	zone := time.FixedZone("CST", -6*3600)
	f.rec.settings.Location, f.rec.settings.Briefings = zone, []string{"07:00"}
	f.clock.t = time.Date(2026, 11, 5, 6, 59, 0, 0, zone)
	// The first look marks the moment, fires nothing and sets the timer
	// for the next briefing: nothing else need poke the role at 07:00.
	if next := f.step(); next != time.Minute {
		t.Fatalf("first step: the briefing is due in %s, want 1m", next)
	}
	if r := f.record(); len(r.Log) != 0 || r.Briefed.IsZero() {
		t.Fatalf("the first look: %+v", r)
	}
	if next := f.step(); next != time.Minute {
		t.Fatalf("the briefing is due in %s, want 1m (07:00 local is 13:00 UTC)", next)
	}
	f.clock.add(time.Minute)
	r := f.state(RoleGathering)
	if len(r.Log) != 1 || r.Log[0].Source != WakeBriefing || !strings.HasPrefix(r.Log[0].Key, "2026-11-05T07:00-06:00") {
		t.Fatalf("%+v", r.Log)
	}
	if next := f.step(); next != 2*time.Minute {
		t.Fatalf("the window ends in %s", next)
	}
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	f.rec.close("br-1", RoleOutcome{})
	f.state(RoleCooling)
	f.clock.add(time.Hour)
	if next := f.step(); next != 22*time.Hour+58*time.Minute { // 08:02 to 07:00
		t.Fatalf("the next briefing is due in %s", next)
	}
	// A role turned on past a briefing time fires none of the past.
	g := newRoleFixture(t)
	g.rec.settings.Location, g.rec.settings.Briefings = zone, []string{"07:00"}
	g.clock.t = time.Date(2026, 11, 5, 9, 0, 0, 0, zone)
	g.step()
	g.step()
	if g.rec.fired() != 0 || len(g.record().Log) != 0 {
		t.Fatal("a briefing of the past fired")
	}
}

// TestAPausedRoleGathersNothing: paused, a wake is logged as held and
// joins no batch, and no briefing time fires; a turn running when the
// pause came runs to its end, and resuming takes up what is left.
func TestAPausedRoleGathersNothing(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	if _, err := f.role.Pause(); err != nil {
		t.Fatal(err)
	}
	f.failed("lint", 1)
	r := f.record()
	if r.State != RolePaused || r.Open != nil || r.Log[len(r.Log)-1].Held != "paused" {
		t.Fatalf("%+v", r)
	}
	f.rec.close("br-1", RoleOutcome{})
	f.state(RolePaused)
	f.clock.add(time.Hour)
	f.state(RolePaused)
	if f.rec.fired() != 1 {
		t.Fatal("a paused role fired")
	}
	if r, err := f.role.Resume(); err != nil || r.State != RoleIdle {
		t.Fatalf("%v %+v", err, r)
	}
}

// TestAPersonsRequestTriesAgain: an unavailable role (its budget spent)
// holds its wakes in a batch and starts nothing by itself; a person's own
// request, logged and joined to no batch, tries again.
func TestAPersonsRequestTriesAgain(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	f.rec.close("br-1", RoleOutcome{Unavailable: "the day's budget is spent: 240 of 240 minutes", Reason: ReasonBudget})
	r := f.state(RoleUnavailable)
	if r.Reason != ReasonBudget || !r.RetryAt.IsZero() {
		t.Fatalf("%+v", r)
	}
	f.failed("lint", 1)
	f.clock.add(time.Hour)
	f.state(RoleUnavailable)
	if f.rec.fired() != 1 {
		t.Fatal("an unavailable role started a turn by itself")
	}
	if err := f.role.Asked(RoleWake{Subject: "br-2"}); err != nil {
		t.Fatal(err)
	}
	r = f.state(RoleBriefing)
	// The batch whose turn could not start is the one that fires now,
	// with the wake gathered since: build's and lint's.
	if f.rec.fired() != 2 || r.Fired.ID != "wb-2" || len(r.Fired.Wakes) != 2 || r.Fired.Wakes[0].Subject != "task:run-1/build" || r.Fired.Wakes[1].Subject != "task:run-1/lint" {
		t.Fatalf("%+v", r.Fired)
	}
	if row := r.Log[len(r.Log)-1]; row.Source != WakeRequest || row.Batch != "" || row.Held == "" {
		t.Fatalf("the person's request in the log: %+v", row)
	}
}

// TestAnAgentThatDidNotStartIsNotFiredIntoAgain: a turn whose agent did
// not start (signed out, held at a screen) leaves the role unavailable with
// its words, the batch kept, and a retry of its own that doubles with each
// such turn in a row. A wake, a briefing time and a person's own request
// fire nothing into the same state meanwhile (the person's turn is its own
// try); its retry time does, and a turn of the agent that reads its request
// (Started, a person's) takes the batch up at once and counts from 0.
func TestAnAgentThatDidNotStartIsNotFiredIntoAgain(t *testing.T) {
	f := newRoleFixture(t)
	f.rec.settings.Briefings = []string{"21:05"}
	f.state(RoleIdle)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	words := "the turn could not start: the agent did not start in 5 minutes, no call to Conductor"
	f.rec.close("br-1", RoleOutcome{Unavailable: words, Reason: ReasonStart})
	r := f.state(RoleUnavailable)
	if r.Why != words || r.Reason != ReasonStart || r.Tries != 1 || !r.RetryAt.Equal(f.clock.now().Add(RoleStartRetry)) || r.Open == nil {
		t.Fatalf("%+v", r)
	}
	// A wake, a briefing time (21:05) and a person's request: none fires.
	f.failed("lint", 1)
	f.clock.add(RoleStartRetry - time.Minute)
	if err := f.role.Asked(RoleWake{Subject: "br-2"}); err != nil {
		t.Fatal(err)
	}
	r = f.state(RoleUnavailable)
	if f.rec.fired() != 1 || r.Why != words {
		t.Fatalf("fired into the same state: %d turns, %+v", f.rec.fired(), r)
	}
	// Its retry time: the batch kept fires, and its agent does not start
	// again: the wait doubles.
	f.clock.add(time.Minute)
	r = f.state(RoleBriefing)
	if f.rec.fired() != 2 || len(r.Fired.Wakes) != 3 {
		t.Fatalf("the retry: %d turns, %+v", f.rec.fired(), r.Fired)
	}
	f.rec.close("br-2", RoleOutcome{Unavailable: words, Reason: ReasonStart})
	r = f.state(RoleUnavailable)
	if r.Tries != 2 || !r.RetryAt.Equal(f.clock.now().Add(2*RoleStartRetry)) {
		t.Fatalf("the second: %+v", r)
	}
	// A person's turn whose agent reads its request: the batch fires.
	if err := f.role.Started(f.rec.ask()); err != nil {
		t.Fatal(err)
	}
	f.state(RoleGathering)
	f.clock.add(2 * time.Minute) // the kept batch's window
	r = f.state(RoleBriefing)
	if f.rec.fired() != 3 || r.Tries != 0 || r.Reason != "" || r.Request != "br-4" {
		t.Fatalf("after the agent started: %d turns, %+v", f.rec.fired(), r)
	}
	f.rec.close("br-4", RoleOutcome{})
	f.state(RoleCooling)
}

// TestALaterTurnStartedOutweighsAnEarlierFailureTakenUpLate: the role's
// own turn (br-1) fails, its agent not started, and stops; before the
// role's loop takes that outcome up, a person's turn made after it (br-2)
// starts and its agent reads its request. The failure, taken up then, is
// br-1's own: the role is not made unavailable, counts no try, and the
// batch br-1 could not start fires within its rules. Judged by the order
// the outcomes reached it, the role read the start while it briefed (and
// noted nothing), then the failure, and was unavailable though its agent
// had started. A failure of a turn made after the start still counts.
func TestALaterTurnStartedOutweighsAnEarlierFailureTakenUpLate(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	if r := f.state(RoleBriefing); r.Request != "br-1" {
		t.Fatalf("%+v", r)
	}
	words := "the turn could not start: the agent did not start in 5 minutes, no call to Conductor"
	f.rec.close("br-1", RoleOutcome{Unavailable: words, Reason: ReasonStart})
	person := f.rec.ask()
	if err := f.role.Started(person); err != nil {
		t.Fatal(err)
	}
	r := f.state(RoleGathering)
	if r.Why != "" || r.Reason != "" || r.Tries != 0 || !r.RetryAt.IsZero() || r.LastStarted != person || r.Open == nil || len(r.Open.Wakes) != 1 || r.Open.Wakes[0].Subject != "task:run-1/build" {
		t.Fatalf("an earlier turn's failure after a later turn started: %+v", r)
	}
	f.clock.add(2 * time.Minute)
	r = f.state(RoleBriefing)
	if f.rec.fired() != 2 || r.Request != "br-3" {
		t.Fatalf("the batch kept: %d turns, %+v", f.rec.fired(), r)
	}
	// A turn made after the start that fails: the role is unavailable.
	f.rec.close("br-3", RoleOutcome{Unavailable: words, Reason: ReasonStart})
	if r = f.state(RoleUnavailable); r.Tries != 1 || r.Reason != ReasonStart {
		t.Fatalf("a later turn's failure: %+v", r)
	}
}

// TestTheStartRetryDoublesToItsBound: the wait after the n-th turn in a
// row whose agent did not start.
func TestTheStartRetryDoublesToItsBound(t *testing.T) {
	for tries, want := range map[int]time.Duration{1: RoleStartRetry, 2: 2 * RoleStartRetry, 3: 4 * RoleStartRetry, 5: 16 * RoleStartRetry, 6: RoleStartRetryMax, 60: RoleStartRetryMax} {
		if got := startRetry(tries); got != want {
			t.Errorf("after %d: %s, want %s", tries, got, want)
		}
	}
}

// TestASaveEndsOnlyWhatItChanged: each reason a role is unavailable for,
// against each kind of save: a budget changed ends a spent budget; what the
// agent starts with changed, or the role turned on or off, ends a turn whose
// agent did not start; the role turned on or off ends its being off; nothing ends a run that did not
// stop; a save that changed nothing ends nothing. Each is read from the
// role's record after Reconfigured.
func TestASaveEndsOnlyWhatItChanged(t *testing.T) {
	saves := map[string]RoleChange{
		"nothing": {},
		"budget":  {Budget: true},
		"agent":   {Start: true},
		"on":      {On: true},
		"other":   {Other: true},
	}
	ends := map[string][]string{
		ReasonBudget: {"budget"},
		ReasonStart:  {"agent", "on"},
		ReasonOff:    {"on"},
		ReasonStuck:  nil,
		"":           {"budget", "agent", "on", "other"},
	}
	for reason, want := range ends {
		for name, ch := range saves {
			t.Run(reason+"/"+name, func(t *testing.T) {
				f := newRoleFixture(t)
				f.failed("build", 1)
				f.clock.add(2 * time.Minute)
				f.state(RoleBriefing)
				f.rec.close("br-1", RoleOutcome{Unavailable: "unavailable for " + reason, Reason: reason, RetryAt: f.clock.now().Add(24 * time.Hour)})
				f.state(RoleUnavailable)
				if err := f.role.Reconfigured(ch); err != nil {
					t.Fatal(err)
				}
				r := f.record()
				ended := slices.Contains(want, name)
				if SaveEnds(reason, ch) != ended {
					t.Fatalf("SaveEnds(%q, %+v) = %v", reason, ch, !ended)
				}
				if ended {
					if r.State == RoleUnavailable || r.Why != "" || r.Reason != "" {
						t.Fatalf("not ended: %+v", r)
					}
					f.clock.add(2 * time.Minute) // the kept batch's window
					if r := f.state(RoleBriefing); f.rec.fired() != 2 {
						t.Fatalf("the batch kept did not fire: %+v", r)
					}
					return
				}
				if r.State != RoleUnavailable || r.Why != "unavailable for "+reason || r.Reason != reason {
					t.Fatalf("ended by a save that changed nothing it weighs: %+v", r)
				}
				if f.state(RoleUnavailable); f.rec.fired() != 1 {
					t.Fatal("a turn fired")
				}
			})
		}
	}
}

// TestASaveOfTheRoleOffEndsAnAgentNotStarted (#715): the role's agent did
// not start (signed out), and a save turns the background off, clears the
// account, or both: the role is not left unavailable with the old words,
// and its count of turns not started is gone, so with the background on
// again the next turn is fired by its wake and a failure of it counts
// once. The same for a role turned off while its turn ran, by a save of
// the account cleared; a save of the role off that changed nothing it
// weighs ends nothing.
func TestASaveOfTheRoleOffEndsAnAgentNotStarted(t *testing.T) {
	words := "the agent did not start in 5 minutes, no call to Conductor (signed out?)"
	for name, c := range map[string]struct {
		offBefore bool
		ch        RoleChange
		ends      bool
	}{
		"off and the account cleared": {ch: RoleChange{On: true, Start: true}, ends: true},
		"off":                         {ch: RoleChange{On: true}, ends: true},
		"the account cleared, off":    {offBefore: true, ch: RoleChange{Start: true}, ends: true},
		"anything else, off":          {offBefore: true, ch: RoleChange{Other: true, Budget: true}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRoleFixture(t)
			f.failed("build", 1)
			f.clock.add(2 * time.Minute)
			f.state(RoleBriefing)
			// Turned off by an earlier save while the turn ran, which found
			// nothing to end then.
			f.rec.settings.On = !c.offBefore
			f.rec.close("br-1", RoleOutcome{Unavailable: words, Reason: ReasonStart})
			if r := f.state(RoleUnavailable); r.Tries != 1 {
				t.Fatalf("%+v", r)
			}
			f.rec.settings.On = false
			if err := f.role.Reconfigured(c.ch); err != nil {
				t.Fatal(err)
			}
			if !c.ends {
				if r := f.record(); r.State != RoleUnavailable || r.Why != words || r.Tries != 1 {
					t.Fatalf("ended by a save that changed nothing it weighs: %+v", r)
				}
				return
			}
			// Off: the batch the failed turn kept is dropped, never fired.
			if r := f.state(RoleIdle); r.Why != "" || r.Reason != "" || r.Tries != 0 || !r.RetryAt.IsZero() || r.Open != nil {
				t.Fatalf("after the save: %+v", r)
			}
			f.rec.settings.On = true
			if err := f.role.Reconfigured(RoleChange{On: true}); err != nil {
				t.Fatal(err)
			}
			f.failed("lint", 1)
			f.clock.add(2 * time.Minute)
			if r := f.state(RoleBriefing); f.rec.fired() != 2 || r.Request != "br-2" {
				t.Fatalf("the background on again: %d turns, %+v", f.rec.fired(), r)
			}
			f.rec.close("br-2", RoleOutcome{Unavailable: words, Reason: ReasonStart})
			if r := f.state(RoleUnavailable); r.Tries != 1 || !r.RetryAt.Equal(f.clock.now().Add(RoleStartRetry)) {
				t.Fatalf("the next failure counts once: %+v", r)
			}
		})
	}
}

// TestTheLogKeepsItsNewest: the log keeps its newest MaxRoleLog rows.
func TestTheLogKeepsItsNewest(t *testing.T) {
	f := newRoleFixture(t)
	if _, err := f.role.Pause(); err != nil {
		t.Fatal(err)
	}
	for i := range MaxRoleLog + 5 {
		f.failed(fmt.Sprintf("t%d", i), 1)
	}
	r := f.record()
	if len(r.Log) != MaxRoleLog || r.Log[0].Subject != "task:run-1/t5" {
		t.Fatalf("%d rows, first %+v", len(r.Log), r.Log[0])
	}
}

// TestNotesAreKeptAndClearedWithinTheirBound: the role's notes are kept
// to 64 KiB together and cleared at once.
func TestNotesAreKeptAndClearedWithinTheirBound(t *testing.T) {
	f := newRoleFixture(t)
	for i := range MaxRoleNotes / MaxRoleNote {
		if _, err := f.role.KeepNote(RoleNote{Text: strings.Repeat("n", MaxRoleNote), From: fmt.Sprintf("brief:br-1/%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.role.KeepNote(RoleNote{Text: "one more"}); err == nil {
		t.Fatal("a note past 64 KiB was kept")
	}
	if _, err := f.role.KeepNote(RoleNote{Text: strings.Repeat("n", MaxRoleNote+1)}); err == nil {
		t.Fatal("a note past its bound was kept")
	}
	f.restart()
	if n := len(f.record().Notes); n != MaxRoleNotes/MaxRoleNote {
		t.Fatalf("%d notes read back", n)
	}
	if r, err := f.role.ClearNotes(); err != nil || len(r.Notes) != 0 {
		t.Fatalf("%v %+v", err, r.Notes)
	}
}

// TestAChangeNotWrittenChangesNothing: a wake whose write fails is not
// taken, and the role is as it was.
func TestAChangeNotWrittenChangesNothing(t *testing.T) {
	f := newRoleFixture(t)
	f.rec.saveFails = true
	if err := f.role.Wake(RoleWake{Source: WakeTaskFailed, Subject: "task:run-1/build", Key: "attempts 1"}); err == nil {
		t.Fatal("the failed write was not said")
	}
	if r := f.record(); r.State != RoleIdle || len(r.Log) != 0 || r.Open != nil {
		t.Fatalf("%+v", r)
	}
	f.failed("build", 1)
	if r := f.record(); len(r.Log) != 1 || r.Open.ID != "wb-1" {
		t.Fatalf("%+v", r)
	}
}

// TestARoleOffTakesNoWake: with the role off nothing is logged, and a
// batch that was gathering is dropped, never fired later.
func TestARoleOffTakesNoWake(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.rec.settings.On = false
	f.failed("lint", 1)
	f.clock.add(5 * time.Minute)
	r := f.state(RoleIdle)
	if len(r.Log) != 1 || r.Open != nil {
		t.Fatalf("%+v", r)
	}
	f.rec.settings.On = true
	f.clock.add(5 * time.Minute)
	f.state(RoleIdle)
	if f.rec.fired() != 0 {
		t.Fatal("a dropped batch fired")
	}
}

// TestAHeldWakeFiresIfStillDue: a question held for its threshold wakes
// the role once its time comes, if it is still open; one answered before
// wakes nothing.
func TestAHeldWakeFiresIfStillDue(t *testing.T) {
	f := newRoleFixture(t)
	open := true
	at := f.clock.now().Add(15 * time.Minute)
	f.role.Hold(RoleWake{Source: WakeQuestionOpen, Subject: "session:a", Key: "1", At: at}, at, func() bool { return open })
	f.role.Hold(RoleWake{Source: WakeQuestionOpen, Subject: "session:a", Key: "1", At: at}, at, func() bool { return open })
	f.role.Hold(RoleWake{Source: WakeQuestionOpen, Subject: "session:b", Key: "1", At: at}, at, func() bool { return false })
	if next := f.step(); next != 15*time.Minute {
		t.Fatalf("held for %s", next)
	}
	f.clock.add(15 * time.Minute)
	r := f.state(RoleGathering)
	if len(r.Log) != 1 || r.Log[0].Subject != "session:a" {
		t.Fatalf("%+v", r.Log)
	}
}

// TestARoleRecordReadsBackAsWritten: the record a role writes decodes to
// the same record, its batches, log and notes whole, as the desk's
// role.json is read back after a restart; one of a later schema is kept
// and takes no step.
func TestARoleRecordReadsBackAsWritten(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	if _, err := f.role.KeepNote(RoleNote{Text: "the build fails on arm64", From: "brief:br-1/0"}); err != nil {
		t.Fatal(err)
	}
	r := f.record()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back RoleRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if b2, _ := json.Marshal(back); string(b2) != string(b) {
		t.Fatalf("read back\n%s\nwrote\n%s", b2, b)
	}
	later := back
	later.Schema = RoleSchema + 1
	f.rec.doc = &later
	f.restart()
	if _, err := f.role.Step(); !errors.Is(err, ErrRoleLaterSchema) {
		t.Fatalf("a later schema: %v", err)
	}
	if f.rec.doc.Schema != RoleSchema+1 {
		t.Fatal("a later schema was rewritten")
	}
}

// TestABriefingTimePassedWhilePausedOrOffFiresNone: a 07:00 briefing that
// passes while the role is paused, or while it is off, wakes nothing once
// it is resumed, or turned on, at 08:00; the next morning's fires.
func TestABriefingTimePassedWhilePausedOrOffFiresNone(t *testing.T) {
	zone := time.FixedZone("CST", -6*3600)
	for _, how := range []string{"paused", "off"} {
		t.Run(how, func(t *testing.T) {
			f := newRoleFixture(t)
			f.rec.settings.Location, f.rec.settings.Briefings = zone, []string{"07:00"}
			f.clock.t = time.Date(2026, 11, 5, 6, 0, 0, 0, zone)
			f.step()
			if how == "paused" {
				if _, err := f.role.Pause(); err != nil {
					t.Fatal(err)
				}
			} else {
				f.rec.settings.On = false
			}
			f.step()
			f.clock.add(2 * time.Hour) // 08:00
			f.step()
			if how == "paused" {
				if _, err := f.role.Resume(); err != nil {
					t.Fatal(err)
				}
			} else {
				f.rec.settings.On = true
			}
			if next := f.step(); next != 23*time.Hour {
				t.Fatalf("the next briefing is due in %s, want 23h", next)
			}
			if r := f.record(); len(r.Log) != 0 || r.Open != nil || r.State != RoleIdle {
				t.Fatalf("the briefing of the past woke the role: %+v", r)
			}
			f.clock.add(23 * time.Hour)
			if r := f.state(RoleGathering); len(r.Log) != 1 || r.Log[0].Source != WakeBriefing {
				t.Fatalf("%+v", r.Log)
			}
		})
	}
}

// TestADueHeldWakeOutlivesAFailedWrite: a held question falls due and the
// write of what it gathered fails; it is still held, and the step that
// tries again gathers it.
func TestADueHeldWakeOutlivesAFailedWrite(t *testing.T) {
	f := newRoleFixture(t)
	at := f.clock.now().Add(15 * time.Minute)
	f.role.Hold(RoleWake{Source: WakeQuestionOpen, Subject: "session:a", Key: "1", At: at}, at, func() bool { return true })
	f.clock.add(15 * time.Minute)
	f.rec.saveFails = true
	if _, err := f.role.Step(); err == nil {
		t.Fatal("the failed write was not said")
	}
	if r := f.record(); len(r.Log) != 0 {
		t.Fatalf("%+v", r)
	}
	r := f.state(RoleGathering)
	if len(r.Log) != 1 || r.Log[0].Subject != "session:a" {
		t.Fatalf("the held wake was lost with the write: %+v", r.Log)
	}
}

// TestForgetReadsTheRecordsAgain: two roles over one set of records, as
// two servers over one data directory: the one that does not hold them
// reads them as they are (Peek), and once they are its (Forget) it takes
// up the other's record, its batch, log and notes, never the record it
// read before.
func TestForgetReadsTheRecordsAgain(t *testing.T) {
	f := newRoleFixture(t)
	waiting := NewRole("brakeman", f.rec, f.clock.now)
	if r, err := waiting.Record(); err != nil || r.Last != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	f.failed("build", 1)
	if _, err := f.role.KeepNote(RoleNote{Text: "kept by the first"}); err != nil {
		t.Fatal(err)
	}
	if r, err := waiting.Peek(); err != nil || r.Open == nil || len(r.Notes) != 1 {
		t.Fatalf("a peek: %v %+v", err, r)
	}
	waiting.Forget()
	if err := waiting.Wake(RoleWake{Source: WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1"}); err != nil {
		t.Fatal(err)
	}
	r, err := waiting.Record()
	if err != nil {
		t.Fatal(err)
	}
	if r.Last != 1 || r.Open == nil || r.Open.ID != "wb-1" || len(r.Open.Wakes) != 2 || len(r.Log) != 2 || len(r.Notes) != 1 {
		t.Fatalf("the record taken up: %+v", r)
	}
}

// TestAPauseAcrossABriefingTimeFiresNoneUnstepped: the role is paused at
// 06:30 and resumed at 08:00 with no step between (its loop held up), and
// the 07:00 briefing that passed meanwhile wakes nothing: Pause and
// Resume drop the briefing times' mark themselves.
func TestAPauseAcrossABriefingTimeFiresNoneUnstepped(t *testing.T) {
	zone := time.FixedZone("CST", -6*3600)
	f := newRoleFixture(t)
	f.rec.settings.Location, f.rec.settings.Briefings = zone, []string{"07:00"}
	f.clock.t = time.Date(2026, 11, 5, 6, 0, 0, 0, zone)
	f.step()
	f.clock.add(30 * time.Minute)
	if _, err := f.role.Pause(); err != nil {
		t.Fatal(err)
	}
	f.clock.add(90 * time.Minute) // 08:00
	if _, err := f.role.Resume(); err != nil {
		t.Fatal(err)
	}
	if next := f.step(); next != 23*time.Hour {
		t.Fatalf("the next briefing is due in %s, want 23h", next)
	}
	if r := f.record(); len(r.Log) != 0 || r.Open != nil || r.State != RoleIdle {
		t.Fatalf("the briefing passed while paused woke the role: %+v", r)
	}
}

// TestUnreadableSettingsChangeNothing: a batch is gathering when the
// settings cannot be read; past its window the step says so and changes
// nothing (the batch neither dropped as if the role were off nor fired),
// a wake is refused rather than taken for the role off, and once the
// settings read again the batch fires, once.
func TestUnreadableSettingsChangeNothing(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.state(RoleGathering)
	f.rec.mu.Lock()
	f.rec.settingsErr = errors.New("config.json does not parse")
	f.rec.mu.Unlock()
	f.clock.add(3 * time.Minute)
	if _, err := f.role.Step(); err == nil {
		t.Fatal("the unreadable settings were not said")
	}
	if err := f.role.Wake(RoleWake{Source: WakeTaskFailed, Subject: "task:run-1/lint", Key: "attempts 1"}); err == nil {
		t.Fatal("a wake was taken for the role off")
	}
	if r := f.record(); r.State != RoleGathering || r.Open == nil || len(r.Open.Wakes) != 1 || f.rec.fired() != 0 {
		t.Fatalf("the unreadable settings changed the role: %+v", r)
	}
	f.rec.mu.Lock()
	f.rec.settingsErr = nil
	f.rec.mu.Unlock()
	if r := f.state(RoleBriefing); r.Fired == nil || len(r.Fired.Wakes) != 1 || f.rec.fired() != 1 {
		t.Fatalf("%+v", r)
	}
}

// TestAgainPassesUnpoked: an owner whose sources kept what a pass could
// not take (Again) has the loop pass again after the retry, with nothing
// poking it and nothing of the role's own falling due.
func TestAgainPassesUnpoked(t *testing.T) {
	f := newRoleFixture(t)
	f.role.retry = time.Millisecond
	done := make(chan struct{})
	passes := make(chan int, 4)
	n := 0
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		f.role.Run(done, func() bool {
			n++
			if n == 1 {
				f.role.Again()
			}
			passes <- n
			return true
		})
	}()
	for want := 1; want <= 2; want++ {
		select {
		case got := <-passes:
			if got != want {
				t.Fatalf("pass %d, want %d", got, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("pass %d never came", want)
		}
	}
	close(done)
	<-ended
}

// TestTurningTheRoleOffAndOnAcrossABriefingTimeFiresNoneUnstepped: the
// role is turned off at 06:30 and on at 08:00 with no step between (its
// loop held up), and the 07:00 briefing that passed meanwhile wakes
// nothing: the owner drops the briefing times' mark as it turns the role
// (Switched), before the configuration is written.
func TestTurningTheRoleOffAndOnAcrossABriefingTimeFiresNoneUnstepped(t *testing.T) {
	zone := time.FixedZone("CST", -6*3600)
	f := newRoleFixture(t)
	f.rec.settings.Location, f.rec.settings.Briefings = zone, []string{"07:00"}
	f.clock.t = time.Date(2026, 11, 5, 6, 0, 0, 0, zone)
	f.step()
	if f.record().Briefed.IsZero() {
		t.Fatal("the first look marked nothing")
	}
	f.clock.add(30 * time.Minute)
	if err := f.role.Switched(); err != nil {
		t.Fatal(err)
	}
	f.rec.settings.On = false
	f.clock.add(90 * time.Minute) // 08:00
	if err := f.role.Switched(); err != nil {
		t.Fatal(err)
	}
	f.rec.settings.On = true
	if next := f.step(); next != 23*time.Hour {
		t.Fatalf("the next briefing is due in %s, want 23h", next)
	}
	if r := f.record(); len(r.Log) != 0 || r.Open != nil || r.State != RoleIdle {
		t.Fatalf("the briefing passed while off woke the role: %+v", r)
	}
	// A mark that could not be written is said, and changes nothing.
	f.rec.saveFails = true
	if err := f.role.Switched(); err == nil {
		t.Fatal("the failed write was not said")
	}
	if f.record().Briefed.IsZero() {
		t.Fatal("a mark not written was dropped")
	}
}
