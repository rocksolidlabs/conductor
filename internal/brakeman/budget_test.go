package brakeman

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The day's minutes (docs/plans/brakeman.md, PR-14): a turn's reservation
// at its start is the least of TurnMinutes and what MinutesPerDay has
// left today, whatever started the turn; under MinReservation left closes
// the request over_budget; the charge is settled at the close, a crash
// before that keeping the reservation; a turn is charged to the day it
// started.

// minutesConfig is onConfig with the day's minutes.
func minutesConfig(perDay int) Config {
	c := onConfig()
	c.Budget.MinutesPerDay = perDay
	return c
}

// spend charges the day minutes through a turn that ran them.
func (f *fixture) spend(op string, c Config, ran time.Duration) {
	f.t.Helper()
	r, err := f.s.Start(f.create(op).ID, c, Stamp{})
	if err != nil {
		f.t.Fatal(err)
	}
	f.clock.add(ran)
	f.close(r.ID, StateEnded)
}

// TestATurnIsBoundedByTheDaysMinutesLeft: with 5 of the day's minutes
// left, a turn of 15 is reserved 5 and its deadline is 5 minutes on.
func TestATurnIsBoundedByTheDaysMinutesLeft(t *testing.T) {
	f := newFixture(t)
	c := minutesConfig(MinMinutesPerDay + 10) // 20
	f.spend("op-1", c, 15*time.Minute)
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 15 {
		t.Fatalf("spent %d", m)
	}
	r, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Deadline.Sub(f.clock.now()); got != 5*time.Minute {
		t.Fatalf("the deadline is %s on, want 5m", got)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 20 {
		t.Fatalf("with the reservation: %d", m)
	}
}

// TestTooFewMinutesLeftIsOverBudget: one minute left never buys a turn:
// the request is closed over_budget with words, and nothing is charged.
func TestTooFewMinutesLeftIsOverBudget(t *testing.T) {
	f := newFixture(t)
	c := minutesConfig(MinMinutesPerDay) // 10
	c.Budget.TurnMinutes = 9
	f.spend("op-1", c, 9*time.Minute)
	r, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if !errors.Is(err, ErrOverBudget) || r.State != StateOverBudget || !strings.Contains(r.Why, "minutes are spent: 1 of 10 left") {
		t.Fatalf("%v %+v", err, r)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 1 {
		t.Fatalf("an over-budget request was charged: %d turns", n)
	}
	// Two left buy a turn of two.
	c2 := minutesConfig(MinMinutesPerDay + 1)
	c2.Budget.TurnMinutes = 9
	r, err = f.s.Start(f.create("op-3").ID, c2, Stamp{})
	if err != nil || r.Deadline.Sub(f.clock.now()) != 2*time.Minute {
		t.Fatalf("%v %+v", err, r)
	}
	// A turn of one minute is a turn: the least is held against the day's
	// minutes left, never against TurnMinutes.
	one := minutesConfig(DefaultMinutesPerDay)
	one.Budget.TurnMinutes = MinTurnMinutes
	r, err = f.s.Start(f.create("op-4").ID, one, Stamp{})
	if err != nil || r.Deadline.Sub(f.clock.now()) != time.Minute {
		t.Fatalf("a turn of a minute: %v %+v", err, r)
	}
}

// TestAChargeIsSettledAtTheClose: a turn reserved 15 that ran 4 counts 4
// once closed, and the day's minutes left grow by what it did not use.
func TestAChargeIsSettledAtTheClose(t *testing.T) {
	f := newFixture(t)
	c := minutesConfig(30)
	r, err := f.s.Start(f.create("op-1").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 15 {
		t.Fatalf("reserved %d", m)
	}
	f.clock.add(3*time.Minute + time.Second)
	f.close(r.ID, StateTimedOut)
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 4 {
		t.Fatalf("settled %d, want 4", m)
	}
	next, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if err != nil || next.Deadline.Sub(f.clock.now()) != 15*time.Minute {
		t.Fatalf("%v %+v", err, next)
	}
}

// TestARestartKeepsAnUnsettledReservation: a server that stops while a
// turn runs leaves its reservation as the day's charge; the turn is
// interrupted at the next start and settled then, never refunded below
// what it reserved by a crash.
func TestARestartKeepsAnUnsettledReservation(t *testing.T) {
	f := newFixture(t)
	c := minutesConfig(30)
	r, err := f.s.Start(f.create("op-1").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.add(time.Minute)
	// The close's settle crashes: the reservation stands.
	failWrite(t, StateFile)
	if _, err := f.s.Update(r.ID, func(r *Request) error { return r.To(StateEnded, "", f.clock.now()) }); err == nil {
		t.Fatal("the settle's crash was not said")
	}
	beforeWrite = nil
	f.reopen()
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 15 {
		t.Fatalf("after the restart the day counts %d minutes, want the reservation's 15", m)
	}
	// What the day has left is what the reservation leaves.
	next, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if err != nil || next.Deadline.Sub(f.clock.now()) != 15*time.Minute {
		t.Fatalf("%v %+v", err, next)
	}
	if _, err := f.s.Start(f.create("op-3").ID, c, Stamp{}); !errors.Is(err, ErrOverBudget) {
		t.Fatalf("the day's 30 minutes are reserved: %v", err)
	}
}

// TestMidnightLeavesAnInFlightChargeOnItsDay: a turn started at 23:55
// keeps its minutes on its start day, and the new day's allowance is
// whole at midnight.
func TestMidnightLeavesAnInFlightChargeOnItsDay(t *testing.T) {
	f := newFixture(t)
	c := minutesConfig(20)
	start := time.Date(2026, 11, 1, 23, 55, 0, 0, zone)
	f.clock.set(start)
	f.spend("op-0", c, 5*time.Minute)
	f.clock.set(start.Add(4 * time.Minute))
	r, err := f.s.Start(f.create("op-1").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Deadline.Sub(f.clock.now()); got != 15*time.Minute {
		t.Fatalf("the deadline is %s on", got)
	}
	f.clock.add(2 * time.Minute) // past midnight, the turn still running
	next, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if err != nil || next.Deadline.Sub(f.clock.now()) != 15*time.Minute {
		t.Fatalf("the new day's allowance: %v %+v", err, next)
	}
	f.clock.add(5 * time.Minute)
	f.close(r.ID, StateEnded)
	if m, _ := f.s.MinutesToday(start); m != 5+7 {
		t.Fatalf("the start day counts %d minutes, want 12", m)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 15 {
		t.Fatalf("the new day counts %d minutes, want the second turn's 15", m)
	}
}

// TestAWakeRequestNamesItsBatch: a wake request names its batch and
// nothing else names one; its op id names it again.
func TestAWakeRequestNamesItsBatch(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.s.Create(Request{OpID: "wake-wb-1", Kind: KindWake}); err == nil {
		t.Fatal("a wake request without its batch was kept")
	}
	if _, _, err := f.s.Create(Request{OpID: "op-b", Kind: KindBriefing, Batch: "wb-1"}); err == nil {
		t.Fatal("a briefing with a batch was kept")
	}
	r, created, err := f.s.Create(Request{OpID: "wake-wb-1", Kind: KindWake, Batch: "wb-1"})
	if err != nil || !created || r.Batch != "wb-1" {
		t.Fatalf("%v %v %+v", err, created, r)
	}
	again, created, err := f.s.Create(Request{OpID: "wake-wb-1", Kind: KindWake, Batch: "wb-1"})
	if err != nil || created || again.ID != r.ID {
		t.Fatalf("the batch's op id again: %v %v %+v", err, created, again)
	}
	if _, _, err := f.s.Create(Request{OpID: "wake-wb-1", Kind: KindWake, Batch: "wb-2"}); !errors.Is(err, ErrOpConflict) {
		t.Fatalf("another batch under the op id: %v", err)
	}
	f.reopen()
	if got, err := f.s.Get(r.ID); err != nil || got.Batch != "wb-1" || got.Kind != KindWake {
		t.Fatalf("read back: %v %+v", err, got)
	}
	// A wake turn's brief is a briefing's: it holds a fact.
	if err := CheckAnswers(Brief{Schema: BriefSchema, Title: "t", Summary: "s"}, KindWake); err == nil {
		t.Fatal("a wake brief without a fact was taken")
	}
}
