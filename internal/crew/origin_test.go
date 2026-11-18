package crew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A run a registered provider launched (origin.go): its origin row with
// its first row, its record's origin, its continuations' copies, and its
// deadline, which the engine keeps.

// testClock is an engine clock a test moves.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 18, 9, 30, 15, 500_000_000, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// deadlineTimers is the deadline watch's timer as a test drives it: each
// wait the watch asks for is kept, with its channel, which the test fires.
type deadlineTimers struct {
	mu      sync.Mutex
	asked   []time.Duration
	chans   []chan time.Time
	stopped map[string]error
}

func (d *deadlineTimers) after(wait time.Duration) <-chan time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch := make(chan time.Time, 1)
	d.asked, d.chans = append(d.asked, wait), append(d.chans, ch)
	return ch
}

// waitAsked waits for the watch to have asked for its n-th wait, and
// answers it.
func (d *deadlineTimers) waitAsked(t *testing.T, n int) time.Duration {
	t.Helper()
	var wait time.Duration
	waitFake(t, "the deadline watch's wait", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(d.asked) < n {
			return false
		}
		wait = d.asked[n-1]
		return true
	})
	return wait
}

// fireAll ends every wait asked so far: a watch whose run has stopped
// has returned, and its channel is never read.
func (d *deadlineTimers) fireAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ch := range d.chans {
		select {
		case ch <- time.Time{}:
		default:
		}
	}
}

// fire ends the watch's n-th wait.
func (d *deadlineTimers) fire(n int) {
	d.mu.Lock()
	ch := d.chans[n-1]
	d.mu.Unlock()
	ch <- time.Time{}
}

// driven gives e the test's clock and deadline timer, and the watch's
// stops' answers, by run, as each watch acts at its deadline.
func driven(e *Engine) (*testClock, *deadlineTimers) {
	clk, timers := newTestClock(), &deadlineTimers{stopped: map[string]error{}}
	e.now, e.deadlineTimer = clk.Now, timers.after
	e.afterDeadline = func(runID string, err error) {
		timers.mu.Lock()
		defer timers.mu.Unlock()
		timers.stopped[runID] = err
	}
	return clk, timers
}

// waitActed waits for the run's watch to have acted at its deadline, and
// answers what its stop answered.
func (d *deadlineTimers) waitActed(t *testing.T, runID string) error {
	t.Helper()
	var err error
	waitFake(t, "the deadline watch's stop", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		var ok bool
		err, ok = d.stopped[runID]
		return ok
	})
	return err
}

// providerOrigin is a provider's origin with the op id and limit given.
func providerOrigin(opID string, limit int) ledger.OriginRow {
	return ledger.OriginRow{Provider: "example", OpID: opID, Ref: "example:42", Label: "Review of change 42",
		Subject: "change:git.example.com/acme/app#42@" + strings.Repeat("a", 40), SubjectURL: "https://git.example.com/acme/app/pull/42",
		Prior: "api-sweep-0000000a", LimitSeconds: limit}
}

// launchOrigin launches c as a provider's launch with origin o.
func launchOrigin(t *testing.T, e *Engine, c Crew, o ledger.OriginRow) (Run, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run, release, err := e.LaunchNamedWith(ctx, c, "", LaunchOpts{Origin: &o})
	release()
	if err != nil {
		return Run{}, err
	}
	return *run, nil
}

// originIn is the run's origin row in ld.
func originIn(t *testing.T, ld *ledger.Ledger, runID string) (ledger.OriginRow, bool) {
	t.Helper()
	row, ok, err := ld.Origin(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return row, ok
}

// runLogHas reports whether the run's log has an entry with msg.
func runLogHas(r Run, msg string) bool {
	return slices.ContainsFunc(r.Log, func(en session.ActivityEntry) bool { return en.Message == msg })
}

// waitRunStopped waits for the run, which the engine holds, to be stopped.
func waitRunStopped(t *testing.T, e *Engine, runID string) Run {
	t.Helper()
	var got Run
	waitFake(t, "the run stopped", func() bool {
		got, _ = e.Get(runID)
		return got.StoppedAt != nil && got.State == RunStopped
	})
	return got
}

// A provider's launch writes its origin with the run's first row: its
// deadline the engine's clock's now, in whole seconds, plus its limit;
// the run's record carries it, and the run itself (Get) says it. The
// deadline watch wakes at the lesser of what is left and its period, and
// a wake before the instant stops nothing; once the engine's clock has
// passed the instant, the next wake stops the run, its log saying the
// limit in minutes, its ledger row stopped. A run that finished before
// its deadline is left finished, and its watch asks for nothing more.
func TestRunStopsAtItsDeadline(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	clk, timers := driven(e)
	launched := clk.Now()
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 3600))
	if err != nil {
		t.Fatal(err)
	}
	want := launched.Truncate(time.Second).Add(time.Hour)
	row, ok := originIn(t, ld, run.ID)
	if !ok || !row.Deadline.Equal(want) || row.RunID != run.ID || row.OpID != "op-1" || row.LimitSeconds != 3600 {
		t.Fatalf("the origin row: %+v %v, want the deadline %s", row, ok, want)
	}
	if got, _ := e.Get(run.ID); got.Origin == nil || !got.Origin.Deadline.Equal(want) || got.Origin.Continued || got.Origin.Provider != "example" {
		t.Fatalf("the run's origin: %+v", got.Origin)
	}
	if wait := timers.waitAsked(t, 1); wait != deadlinePeriod {
		t.Fatalf("the watch's first wait is %s, its period %s", wait, deadlinePeriod)
	}
	// A wake before the instant: nothing stops, and the watch waits again.
	clk.set(want.Add(-30 * time.Second))
	timers.fire(1)
	if wait := timers.waitAsked(t, 2); wait != 30*time.Second {
		t.Fatalf("the watch's wait with 30 s left is %s", wait)
	}
	if got, _ := e.Get(run.ID); got.StoppedAt != nil {
		t.Fatalf("stopped before its deadline: %+v", got)
	}
	// The clock past the instant, the timer not yet due: its wake stops it.
	clk.set(want.Add(time.Second))
	timers.fire(2)
	if err := timers.waitActed(t, run.ID); err != nil {
		t.Fatalf("the watch's stop: %v", err)
	}
	stopped := waitRunStopped(t, e, run.ID)
	if !runLogHas(stopped, "stopped: its launch's time limit of 60 minutes") || runLogHas(stopped, "stopped") {
		t.Fatalf("the stopped run's log: %+v", stopped.Log)
	}
	sqlRunWhen(t, ld, run.ID, func(r ledger.RunRow) bool { return r.State == ledger.RunStopped })

	// A run that finished first is left finished.
	e2, ld2 := sqlEngine(t)
	clk2, timers2 := driven(e2)
	run2, err := launchOrigin(t, e2, repoCrew(newRepo(t), work("build", "lead")), providerOrigin("op-2", 600))
	if err != nil {
		t.Fatal(err)
	}
	timers2.waitAsked(t, 1)
	a := waitAttempt(t, e2, run2.ID, "build", ledger.AttemptRunning)
	commitResult(t, e2, a, "built.txt", "yes\n", "built")
	waitOutcome(t, e2, run2.ID, ledger.OutcomeSucceeded)
	clk2.set(clk2.Now().Add(time.Hour))
	timers2.fire(1)
	if err := timers2.waitActed(t, run2.ID); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("the watch's stop of the finished run: %v", err)
	}
	if err := e2.StopIfRunning(context.Background(), run2.ID, "again"); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("a stop of the finished run: %v", err)
	}
	got, _ := e2.Get(run2.ID)
	if got.StoppedAt != nil || got.State != RunFinished || runLogHas(got, "stopped: its launch's time limit of 10 minutes") {
		t.Fatalf("the finished run after its deadline: %s %+v", got.State, got.Log)
	}
	if row, _, _ := ld2.Run(context.Background(), run2.ID); row.State != ledger.RunFinished {
		t.Fatalf("the finished run's row: %+v", row)
	}
}

// sqlRunWhen waits for the run's row in ld to satisfy ok.
func sqlRunWhen(t *testing.T, ld *ledger.Ledger, runID string, ok func(ledger.RunRow) bool) ledger.RunRow {
	t.Helper()
	var row ledger.RunRow
	waitFake(t, "the run's row", func() bool {
		r, found, err := ld.Run(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		row = r
		return found && ok(r)
	})
	return row
}

// StopIfRunning stops a running run as Stop does, its log saying the note
// alone; a stopped run, or one the engine does not hold, changes nothing.
func TestStopIfRunningStopsOnlyARunningRun(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := e.StopIfRunning(context.Background(), run.ID, "stopped by Ada through example: superseded"); err != nil {
		t.Fatal(err)
	}
	stopped := waitRunStopped(t, e, run.ID)
	if !runLogHas(stopped, "stopped by Ada through example: superseded") || runLogHas(stopped, "stopped") {
		t.Fatalf("the log: %+v", stopped.Log)
	}
	revision := sqlRunWhen(t, ld, run.ID, func(r ledger.RunRow) bool { return r.State == ledger.RunStopped }).Revision
	if err := e.StopIfRunning(context.Background(), run.ID, "again"); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("a second stop: %v", err)
	}
	if again, _ := e.Get(run.ID); len(again.Log) != len(stopped.Log) {
		t.Fatalf("the second stop logged: %+v", again.Log)
	}
	if row, _, _ := ld.Run(context.Background(), run.ID); row.Revision != revision {
		t.Fatalf("the second stop wrote: revision %d, was %d", row.Revision, revision)
	}
	if err := e.StopIfRunning(context.Background(), "api-sweep-00000000", "x"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("a run the engine does not hold: %v", err)
	}
}

// The deadline is the ledger's: across a restart (a new engine over the
// same ledger) the row keeps the instant, the recovered record says it,
// and the new engine's continuation takes it and arms its watch.
func TestDeadlineSurvivesARestart(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, ld := sqlEngineIn(t, ldir)
	clk, _ := driven(e)
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 1800))
	if err != nil {
		t.Fatal(err)
	}
	want := clk.Now().Truncate(time.Second).Add(30 * time.Minute)
	stopRun(t, e, run.ID)
	ld.Close()

	e2, ld2 := sqlEngineIn(t, ldir)
	t.Cleanup(func() { ld2.Close() })
	clk2, timers2 := driven(e2)
	clk2.set(want.Add(-10 * time.Minute))
	row, ok := originIn(t, ld2, run.ID)
	if !ok || !row.Deadline.Equal(want) || row.LimitSeconds != 1800 {
		t.Fatalf("the row after a restart: %+v %v", row, ok)
	}
	from, ok, err := SQLLedger(ld2).Load(context.Background(), run.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if rec := Recovered(Run{}, from); rec.Origin == nil || !rec.Origin.Deadline.Equal(want) || rec.Origin.LimitSeconds != 1800 || rec.Origin.Continued {
		t.Fatalf("the recovered record's origin: %+v", rec.Origin)
	}
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.Origin == nil || !next.Origin.Deadline.Equal(want) || !next.Origin.Continued {
		t.Fatalf("the continuation's origin: %+v", next.Origin)
	}
	if wait := timers2.waitAsked(t, 1); wait != deadlinePeriod {
		t.Fatalf("the continuation's watch waits %s", wait)
	}
	// Its attempt made before the test's folders go.
	waitAttempt(t, e2, next.ID, "build", ledger.AttemptRunning)
	stopRun(t, e2, next.ID)
}

// A continuation keeps its chain's deadline: two continuations, one
// instant, each row a copy with no operation id. A continuation running
// at the instant is stopped by its own watch. Once the instant has passed
// the chain takes no continuation (ErrTimeLimitPassed), a person's
// request for changes among them, and nothing is written.
func TestContinuationsKeepTheDeadline(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	clk, timers := driven(e)
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 3600))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := originIn(t, ld, run.ID)
	stopRun(t, e, run.ID)
	ctx := context.Background()
	c1, err := e.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e, c1.ID)
	c2, err := e.ContinueRun(ctx, c1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		row, ok := originIn(t, ld, id)
		want := first
		want.RunID, want.OpID = id, ""
		if row.Deadline.Equal(want.Deadline) {
			want.Deadline = row.Deadline
		}
		if !ok || row != want {
			t.Fatalf("continuation %s's row: %+v %v\nwant %+v", id, row, ok, want)
		}
	}
	// c2 runs at the instant: its watch (the third wait asked, after the
	// launch's and c1's, whose runs stopped) stops it.
	waitAttempt(t, e, c2.ID, "build", ledger.AttemptRunning)
	timers.waitAsked(t, 3)
	clk.set(first.Deadline)
	timers.fireAll()
	if err := timers.waitActed(t, c2.ID); err != nil {
		t.Fatalf("the continuation's watch: %v", err)
	}
	stopped := waitRunStopped(t, e, c2.ID)
	if !runLogHas(stopped, "stopped: its launch's time limit of 60 minutes") {
		t.Fatalf("the continuation's log: %+v", stopped.Log)
	}
	sqlRunWhen(t, ld, c2.ID, func(r ledger.RunRow) bool { return r.State == ledger.RunStopped })

	// Past the instant: no continuation, nothing written.
	before, err := ld.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ContinueRun(ctx, c2.ID, ""); !errors.Is(err, ErrTimeLimitPassed) {
		t.Fatalf("a continuation past the deadline: %v", err)
	}
	if _, err := e.ContinueRunWith(ctx, c2.ID, "", ContinueOptions{Repair: &RepairRequest{Task: "build", Attempt: 1, Request: personsRequest("rv-0001")}, Actor: "admin", Command: Command{ID: "rq-1"}}); !errors.Is(err, ErrTimeLimitPassed) {
		t.Fatalf("a request for changes past the deadline: %v", err)
	}
	after, err := ld.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after[len(after)-1].Revision != before[len(before)-1].Revision {
		t.Fatalf("a refused continuation wrote: %d runs, were %d", len(after), len(before))
	}
	if row, _, _ := ld.Run(ctx, c2.ID); row.ContinuedBy != "" {
		t.Fatalf("the refused continuation marked its source: %+v", row)
	}
}

// The earlier run a provider's launch names is kept in its row, said by a
// record recovered from the ledger, and copied by a continuation.
func TestPriorSurvivesRestartAndContinuation(t *testing.T) {
	dir, ldir := newRepo(t), t.TempDir()
	e, ld := sqlEngineIn(t, ldir)
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e, run.ID)
	ld.Close()
	e2, ld2 := sqlEngineIn(t, ldir)
	t.Cleanup(func() { ld2.Close() })
	if row, ok := originIn(t, ld2, run.ID); !ok || row.Prior != "api-sweep-0000000a" {
		t.Fatalf("the row's prior: %+v %v", row, ok)
	}
	from, _, err := SQLLedger(ld2).Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A record that lacks the origin (written before it was read) says it
	// as the row keeps it.
	if rec := Recovered(Run{ID: run.ID, CrewID: run.CrewID}, from); rec.Origin == nil || rec.Origin.Prior != "api-sweep-0000000a" || rec.Origin.SubjectURL == "" {
		t.Fatalf("the recovered record's origin: %+v", rec.Origin)
	}
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := originIn(t, ld2, next.ID); !ok || row.Prior != "api-sweep-0000000a" || row.OpID != "" || row.Provider != "example" {
		t.Fatalf("the continuation's row: %+v %v", row, ok)
	}
	if next.Origin == nil || next.Origin.Prior != "api-sweep-0000000a" || !next.Origin.Continued {
		t.Fatalf("the continuation's origin: %+v", next.Origin)
	}
	// Its attempt made before the test's folders go.
	waitAttempt(t, e2, next.ID, "build", ledger.AttemptRunning)
	stopRun(t, e2, next.ID)
}

// A second launch with an operation id the provider's rows hold is
// refused by its first transaction (ledger.ErrOpExists) and discarded
// whole: no row of it, no record, no attempt, and the engine does not
// hold it; the first run is as it was.
func TestASecondLaunchOfAnOperationIDIsDiscardedWhole(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	var journaled []string
	var mu sync.Mutex
	e.journal = func(r Run) {
		mu.Lock()
		defer mu.Unlock()
		journaled = append(journaled, r.ID)
	}
	ids := mintedBy(e)
	first, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	before, err := ld.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), providerOrigin("op-1", 0)); !errors.Is(err, ledger.ErrOpExists) {
		t.Fatalf("a second launch of op-1: %v", err)
	}
	minted := ids()
	if len(minted) != 2 {
		t.Fatalf("minted %v", minted)
	}
	second := minted[1]
	if _, ok := e.Get(second); ok {
		t.Fatal("the engine holds the discarded run")
	}
	after, err := ld.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the ledger holds %d runs, was %d", len(after), len(before))
	}
	if _, ok := originIn(t, ld, second); ok {
		t.Fatal("the discarded run has an origin row")
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(journaled, second) {
		t.Fatal("the discarded run was recorded")
	}
	if got, ok, err := ld.RunByOp(context.Background(), "example", "op-1"); err != nil || !ok || got.RunID != first.ID {
		t.Fatalf("op-1 names %+v %v %v", got, ok, err)
	}
	// Its attempt made, and the run stopped, before the test's folders go.
	waitAttempt(t, e, first.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, first.ID)
}

// A provider's launch is of a crew with tasks: one without is refused
// before anything is made.
func TestAProvidersLaunchIsOfACrewWithTasks(t *testing.T) {
	e, _ := sqlEngine(t)
	if _, err := launchOrigin(t, e, testCrew(immediate("lead", "Plan it.")), providerOrigin("op-1", 0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a provider's launch of a crew without tasks: %v", err)
	}
}

// confirmedOrigin is a provider's origin held to a resolved launch: its
// digest, and the selections it confirmed for the agents given.
func confirmedOrigin(t *testing.T, opID string, agents map[string]string) ledger.OriginRow {
	t.Helper()
	o := providerOrigin(opID, 0)
	o.Project, o.ResolvedDigest = "CND", strings.Repeat("d", 64)
	sel, err := ConfirmedAgents(agents)
	if err != nil {
		t.Fatal(err)
	}
	o.Agents = sel
	return o
}

// Every attempt of a run whose launch was held to a resolved launch asks
// the launcher for its agent's confirmed selection, and a continuation's
// attempts do too; an agent the launch confirmed none for fails the
// attempt at "agent", uncounted, no session started, and pauses the run;
// a launcher that refuses the selection (ErrAgentChanged) does the same,
// in words that name no plan revision. A run launched with no digest asks
// for none.
func TestAConfirmedRunHoldsEachAttemptToItsAgents(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	fl := e.launcher.(*fakeLauncher)
	sel := strings.Repeat("e", 64)
	c := repoCrew(dir, work("build", "lead"))
	run, err := launchOrigin(t, e, c, confirmedOrigin(t, "op-1", map[string]string{"claude": sel}))
	if err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	fl.mu.Lock()
	calls := slices.Clone(fl.calls)
	fl.mu.Unlock()
	if len(calls) != 1 || calls[0].agentSHA256 != sel {
		t.Fatalf("the launches: %+v", calls)
	}
	if row, _ := originIn(t, ld, run.ID); row.Agents != `[{"agent":"claude","selection":"`+sel+`"}]` {
		t.Fatalf("the row's agents: %s", row.Agents)
	}
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	fl.mu.Lock()
	calls = slices.Clone(fl.calls)
	fl.mu.Unlock()
	if len(calls) != 2 || calls[1].agentSHA256 != sel || calls[1].ref.RunID != next.ID {
		t.Fatalf("the continuation's launch: %+v", calls)
	}
	if row, _ := originIn(t, ld, next.ID); row.Agents == "" || row.ResolvedDigest == "" {
		t.Fatalf("the continuation's row: %+v", row)
	}
	stopRun(t, e, next.ID)

	// An agent the launch confirmed nothing for.
	other, err := launchOrigin(t, e, repoCrew(newRepo(t), work("build", "lead")), confirmedOrigin(t, "op-2", map[string]string{"codex": sel}))
	if err != nil {
		t.Fatal(err)
	}
	words := "the agent claude is not among those this run's launch confirmed"
	row := sqlAttemptWhen(t, ld, other.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptFailed })
	if row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("an agent not confirmed: %+v", row)
	}
	if fp := failedPayload(t, ld, other.ID, "build", 1); fp["uncounted"] != true {
		t.Fatalf("attempt.failed: %v", fp)
	}
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(other.ID); return r.Paused && strings.Contains(r.PausedWhy, words) })
	stopRun(t, e, other.ID)

	// The launcher refuses the selection: the entry changed.
	fl.mu.Lock()
	fl.fail["lead · build #1"] = ErrAgentChanged
	fl.mu.Unlock()
	changed, err := launchOrigin(t, e, repoCrew(newRepo(t), work("build", "lead")), confirmedOrigin(t, "op-3", map[string]string{"claude": sel}))
	if err != nil {
		t.Fatal(err)
	}
	row = sqlAttemptWhen(t, ld, changed.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptFailed })
	if want := "the agent claude is not the one this run's launch confirmed: its catalog entry changed since"; row.Stage != "agent" || row.Error != want {
		t.Fatalf("a changed entry: %+v", row)
	}
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(changed.ID); return r.Paused })
	stopRun(t, e, changed.ID)
	fl.mu.Lock()
	delete(fl.fail, "lead · build #1")
	fl.mu.Unlock()

	// A row with a digest whose selections are gone holds to nothing: it
	// fails; one launched with no digest asks for none.
	gone := confirmedOrigin(t, "op-4", nil)
	gone.Agents = ""
	empty, err := launchOrigin(t, e, repoCrew(newRepo(t), work("build", "lead")), gone)
	if err != nil {
		t.Fatal(err)
	}
	if row := sqlAttemptWhen(t, ld, empty.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptFailed }); row.Stage != "agent" {
		t.Fatalf("a digest with no selections: %+v", row)
	}
	stopRun(t, e, empty.ID)
	plain, err := launchOrigin(t, e, repoCrew(newRepo(t), work("build", "lead")), providerOrigin("op-5", 0))
	if err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, plain.ID, "build", ledger.AttemptRunning)
	fl.mu.Lock()
	last := fl.calls[len(fl.calls)-1]
	fl.mu.Unlock()
	if last.agentSHA256 != "" || last.ref.RunID != plain.ID {
		t.Fatalf("a run with no digest asked for %q", last.agentSHA256)
	}
}

// Each attempt of a run a provider launched with a subject is handed it in
// its task files: task.json's subject and task.md's section; a run with
// none hands none.
func TestAProvidersRunHandsItsSubjectToEachAttempt(t *testing.T) {
	e, _ := sqlEngine(t)
	dir := newRepo(t)
	o := providerOrigin("op-1", 0)
	o.StartCommit = runGit(t, dir, "rev-parse", "HEAD")
	run, err := launchOrigin(t, e, repoCrew(dir, work("build", "lead")), o)
	if err != nil {
		t.Fatal(err)
	}
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	md, f := taskFileOf(t, a)
	if f.Subject == nil || f.Subject.Ref != o.Subject || f.Subject.URL != o.SubjectURL || f.Subject.Prior != o.Prior || f.Subject.StartCommit != o.StartCommit {
		t.Fatalf("task.json's subject: %+v", f.Subject)
	}
	if !strings.Contains(md, "## Subject") || !strings.Contains(md, o.Label) {
		t.Fatalf("task.md:\n%s", md)
	}
	stopRun(t, e, run.ID)
	plain := providerOrigin("op-2", 0)
	plain.Subject, plain.SubjectURL, plain.Prior = "", "", ""
	other, err := launchOrigin(t, e, repoCrew(newRepo(t), work("build", "lead")), plain)
	if err != nil {
		t.Fatal(err)
	}
	a = waitAttempt(t, e, other.ID, "build", ledger.AttemptRunning)
	if md, f := taskFileOf(t, a); f.Subject != nil || strings.Contains(md, "## Subject") {
		t.Fatalf("a run with no subject: %+v", f.Subject)
	}
}

// Past its deadline a run takes no change that would start an attempt: a
// finished run the engine still holds is refused Retry, Accept anyway,
// Check again and Request changes, each with a receipt that says the time
// limit has passed (time_limit_passed), the run as it was, no attempt
// started. Before the deadline the same run takes them.
func TestPastItsDeadlineARunTakesNoChangeThatStartsAnAttempt(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	clk, timers := driven(e)
	build := work("build", "lead")
	build.Checks = []Check{markedCheck("test", filepath.Join(t.TempDir(), "never"))}
	run, err := launchOrigin(t, e, repoCrew(dir, build), providerOrigin("op-1", 600))
	if err != nil {
		t.Fatal(err)
	}
	failed := spend(t, e, run.ID, "build", "built.txt")
	waitOutcome(t, e, run.ID, ledger.OutcomeFailed)
	row, _ := originIn(t, ld, run.ID)
	clk.set(row.Deadline.Add(time.Second))
	timers.fireAll()
	if err := timers.waitActed(t, run.ID); !errors.Is(err, ErrRunNotRunning) {
		t.Fatalf("the watch at the deadline of a finished run: %v", err)
	}
	ctx := context.Background()
	// Nothing that would start an attempt is offered either: the task's
	// commands are what act takes (TaskCommands).
	if set, err := e.TaskCommands(run.ID, "build"); err != nil || slices.Contains(set.Commands, TaskRetry) || slices.Contains(set.Commands, TaskAccept) || set.Recheck != nil {
		t.Fatalf("the commands offered past the deadline: %+v %v", set, err)
	}
	if err := e.RetryTask(ctx, run.ID, "build"); !errors.Is(err, ErrTimeLimitPassed) {
		t.Fatalf("a retry without a receipt past the deadline: %v", err)
	}
	n, commit := failed.Attempt.N, failed.Attempt.Result
	for name, try := range map[string]func() (Receipt, error){
		"retry":  func() (Receipt, error) { return e.Retry(ctx, run.ID, "admin", Command{ID: "c-retry"}, "build") },
		"accept": func() (Receipt, error) { return e.Accept(ctx, run.ID, "admin", Command{ID: "c-accept"}, "build") },
		"recheck": func() (Receipt, error) {
			return recheck(e, run.ID, "c-recheck", "build", n, commit)
		},
		"request changes": func() (Receipt, error) {
			return requestChanges(e, run.ID, "c-changes", "build", n, commit, personsRequest("rv-0001"))
		},
	} {
		rc, err := try()
		if err != nil && !errors.Is(err, ErrTimeLimitPassed) {
			t.Fatalf("%s past the deadline: %v", name, err)
		}
		if err == nil && (rc.Status != ledger.CommandFailed || rc.Refusal != RefusalTimeLimitPassed || !strings.Contains(rc.Error, "time limit has passed")) {
			t.Fatalf("%s past the deadline: %+v", name, rc)
		}
		got, _ := e.Get(run.ID)
		ts := taskOf(t, e, run.ID, "build")
		if got.Outcome != ledger.OutcomeFailed || ts.State != ledger.TaskFailed || ts.Attempts != failed.Attempts {
			t.Fatalf("%s past the deadline changed the run: %s/%s %+v", name, got.State, got.Outcome, ts)
		}
	}
	// Before the deadline the same commands are offered, and taken.
	clk.set(row.Deadline.Add(-time.Minute))
	if set, err := e.TaskCommands(run.ID, "build"); err != nil || !slices.Contains(set.Commands, TaskRetry) || set.Recheck == nil {
		t.Fatalf("the commands offered before the deadline: %+v %v", set, err)
	}
	if rc, err := e.Retry(ctx, run.ID, "admin", Command{ID: "c-retry-2"}, "build"); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("a retry before the deadline: %+v %v", rc, err)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

// An attempt doomed at "agent" is refused before anything of it is made:
// with a project manifest whose setup step leaves a mark, an agent the
// launch confirmed no selection for, or an empty one (a row's agents
// edited by hand), fails the attempt at "agent" with no setup step run,
// no receipt, no worktree and no process.
func TestAnUnconfirmedAgentFailsBeforeItsTree(t *testing.T) {
	for name, agents := range map[string]map[string]string{
		"no selection":    {"codex": strings.Repeat("e", 64)},
		"empty selection": {"claude": ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			e, ld := sqlEngine(t)
			fl := e.launcher.(*fakeLauncher)
			mark := filepath.Join(t.TempDir(), "setup-ran")
			c := repoManifestCrew(dir, Manifest{Setup: []Check{shStep("mark", "touch "+mark)}}, work("build", "lead"))
			run, err := launchOrigin(t, e, c, confirmedOrigin(t, "op-1", agents))
			if err != nil {
				t.Fatal(err)
			}
			row := sqlAttemptWhen(t, ld, run.ID, "build", 1, func(r ledger.AttemptRow) bool { return r.State == ledger.AttemptFailed })
			if row.Stage != "agent" || row.SessionID != "" || row.Worktree != "" && fileExists(row.Worktree) {
				t.Fatalf("the attempt: %+v", row)
			}
			if fileExists(mark) {
				t.Fatal("the manifest's setup step ran for an attempt doomed at agent")
			}
			if checks, err := ld.Checks(context.Background(), run.ID, "build", 1, false); err != nil || len(checks) != 0 {
				t.Fatalf("receipts of a doomed attempt: %+v %v", checks, err)
			}
			fl.mu.Lock()
			calls := len(fl.calls)
			fl.mu.Unlock()
			if calls != 0 {
				t.Fatalf("%d processes started", calls)
			}
			waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
			stopRun(t, e, run.ID)
		})
	}
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
