package crew

import (
	"encoding/json"
	"errors"
	"fmt"
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
	refuse   *RoleRefused
	// saveFails makes the next SaveRole fail, as a crash would.
	saveFails bool
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

func (f *fakeRecords) RoleSettings() RoleSettings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

func (f *fakeRecords) Fire(b Batch) (string, error) {
	f.mu.Lock()
	if f.refuse != nil {
		defer f.mu.Unlock()
		return "", f.refuse
	}
	id, ok := f.requests[b.ID]
	if !ok {
		f.fires++
		id = fmt.Sprintf("br-%d", f.fires)
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
	return f.outcome[id], nil
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
	if r := f.state(RoleUnavailable); !strings.Contains(r.Why, "240 of 240 minutes") {
		t.Fatalf("%+v", r)
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

// TestACrashBetweenTheRequestAndTheRoleFiresOnce: the batch's request is
// made and the role's write after it fails, as a crash would leave it;
// the restarted role, still gathering, fires the batch again and finds
// the request made, so one turn runs.
func TestACrashBetweenTheRequestAndTheRoleFiresOnce(t *testing.T) {
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
	f.restart()
	if r := f.record(); r.State != RoleGathering {
		t.Fatalf("after the crash %+v", r)
	}
	r := f.state(RoleBriefing)
	if f.rec.fired() != 1 || r.Request != "br-1" {
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
	if next := f.step(); next != 0 {
		// The first look marks the moment and fires nothing.
		t.Fatalf("first step: %s", next)
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

// TestAPersonsRequestTriesAgain: an unavailable role (its agent could not
// start) holds its wakes in a batch and starts nothing by itself; a
// person's own request, logged and joined to no batch, tries again.
func TestAPersonsRequestTriesAgain(t *testing.T) {
	f := newRoleFixture(t)
	f.failed("build", 1)
	f.clock.add(2 * time.Minute)
	f.state(RoleBriefing)
	f.rec.close("br-1", RoleOutcome{Unavailable: "the turn could not start: the agent exited before it read its request: is it logged in?"})
	r := f.state(RoleUnavailable)
	if !strings.Contains(r.Why, "is it logged in?") || !r.RetryAt.IsZero() {
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
	if f.rec.fired() != 2 || r.Fired.Wakes[0].Subject != "task:run-1/lint" {
		t.Fatalf("%+v", r)
	}
	if row := r.Log[len(r.Log)-1]; row.Source != WakeRequest || row.Batch != "" || row.Held == "" {
		t.Fatalf("the person's request in the log: %+v", row)
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
