package brakeman

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// reqPath is a document's path under the fixture's requests/.
func (f *fixture) reqPath(name string) string {
	return filepath.Join(f.data.Dir(), Dir, RequestsDir, name)
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// failWrite makes the write of the document name fail, as a crash there.
func failWrite(t *testing.T, name string) {
	t.Helper()
	beforeWrite = func(n string) error {
		if n == name {
			return errors.New("the test's crash")
		}
		return nil
	}
	t.Cleanup(func() { beforeWrite = nil })
}

// TestANumberIsNeverReused: the number is written to state.json before
// the request's file, so a request whose file failed leaves its number
// spent; a pruned request's number is never given again either.
func TestANumberIsNeverReused(t *testing.T) {
	f := newFixture(t)
	failWrite(t, RequestsDir+"/br-1.json")
	if _, _, err := f.s.Create(Request{OpID: "op-1", Kind: KindBriefing}); err == nil {
		t.Fatal("the request's write failed and Create said nothing")
	}
	beforeWrite = nil
	if exists(t, f.reqPath("br-1.json")) {
		t.Fatal("br-1.json was written")
	}
	f.reopen()
	// The op id again: its request never landed, so it is a new request,
	// under the next number.
	r := f.create("op-1")
	if r.ID != "br-2" {
		t.Fatalf("after a failed br-1, the next request is %s", r.ID)
	}
	keepNewest = 0
	t.Cleanup(func() { keepNewest = Keep })
	f.close(r.ID, StateCancelled)
	if n, err := f.s.Prune(); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	f.reopen()
	if r := f.create("op-3"); r.ID != "br-3" {
		t.Fatalf("after br-2 was pruned, the next request is %s", r.ID)
	}
}

// TestAnOpIDNamesOneRequest: the same op id and content answer the
// request; other content is op_conflict.
func TestAnOpIDNamesOneRequest(t *testing.T) {
	f := newFixture(t)
	in := Request{OpID: "op-1", Kind: KindAsk, Note: "why?", Subjects: []Ref{ref(t, "run:"+runA)}}
	first, created, err := f.s.Create(in)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	again, created, err := f.s.Create(in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("a repeat: %s %v %v", again.ID, created, err)
	}
	in.Note = "why not?"
	if _, _, err := f.s.Create(in); !errors.Is(err, ErrOpConflict) {
		t.Fatalf("other content: %v", err)
	}
	if r, ok, err := f.s.ByOp("op-1"); err != nil || !ok || r.ID != first.ID {
		t.Fatalf("by op: %v %v %v", r.ID, ok, err)
	}
}

// TestListPagesNewestFirst: pages of at most the limit, newest first, the
// next cursor leading on.
func TestListPagesNewestFirst(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.create(fmt.Sprintf("op-%d", i))
	}
	page, next, err := f.s.List("", 2)
	if err != nil || len(page) != 2 || page[0].ID != "br-5" || page[1].ID != "br-4" || next != "br-4" {
		t.Fatalf("first page: %v %s %v", ids(page), next, err)
	}
	page, next, err = f.s.List(next, 2)
	if err != nil || len(page) != 2 || page[0].ID != "br-3" || next != "br-2" {
		t.Fatalf("second page: %v %s %v", ids(page), next, err)
	}
	page, next, err = f.s.List(next, 2)
	if err != nil || len(page) != 1 || page[0].ID != "br-1" || next != "" {
		t.Fatalf("last page: %v %s %v", ids(page), next, err)
	}
}

func ids(rs []Request) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// TestPruneKeepsTheNewest500AndEveryOpenOne: past the newest 500, a
// closed request goes with its siblings; an open one stays.
func TestPruneKeepsTheNewest500AndEveryOpenOne(t *testing.T) {
	// Parallel with the other test of hundreds of requests only: the
	// sequential tests, which set the package's seams, have ended.
	t.Parallel()
	f := newFixture(t)
	queued := f.create("op-0")  // br-1, stays queued
	starting := f.start("op-1") // br-2, starting
	gone := f.start("op-2")     // br-3, briefed, then pruned
	if _, err := f.s.KeepBrief(gone.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < Keep+5; i++ {
		f.close(f.create(fmt.Sprintf("op-%d", i)).ID, StateCancelled)
	}
	// 505 requests: the oldest five are past the newest 500.
	n, err := f.s.Prune()
	if err != nil || n != 3 {
		t.Fatalf("prune removed %d: %v", n, err)
	}
	for id, want := range map[string]bool{queued.ID: true, starting.ID: true, gone.ID: false, "br-4": false, "br-5": false, "br-6": true} {
		if _, err := f.s.Get(id); (err == nil) != want {
			t.Errorf("%s kept %v: %v", id, err == nil, err)
		}
	}
	if exists(t, f.reqPath(briefDoc(gone.ID))) || exists(t, f.reqPath(refsDoc(gone.ID))) {
		t.Error("a pruned request's brief stayed")
	}
	page, _, _ := f.s.List("", MaxPage)
	if page[0].ID != fmt.Sprintf("br-%d", Keep+5) {
		t.Errorf("the newest is %s", page[0].ID)
	}
}

// TestPruneKeepsAPinnedRequest: each pin keeps a closed request past the
// newest: a pending made claim, an edge whose two ends are tickets, a
// pending act; an edge still waiting for its other end pins nothing.
func TestPruneKeepsAPinnedRequest(t *testing.T) {
	keepNewest = 0
	t.Cleanup(func() { keepNewest = Keep })
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	made := func(item int, state, key string) Made {
		return Made{Item: item, OpID: fmt.Sprintf("m-%d", item), Op: fmt.Sprintf("bk%030d", item), Input: strings.Repeat("0", 64), State: state, Key: key, At: now}
	}
	cases := []struct {
		name   string
		pinned bool
		fn     func(*Request)
	}{
		{"a pending claim", true, func(r *Request) { r.Made = []Made{made(0, MadePending, "")} }},
		{"a made claim", false, func(r *Request) { r.Made = []Made{made(0, MadeDone, "CND-1")} }},
		{"a refused claim", false, func(r *Request) { r.Made = []Made{made(0, MadeRefused, "")} }},
		{"an edge of two tickets", true, func(r *Request) {
			r.Made = []Made{made(0, MadeDone, "CND-1"), made(1, MadeDone, "CND-2")}
			r.Edges = []Edge{{From: 0, To: 1, Op: "bke", State: EdgePending}}
		}},
		{"an edge recorded", false, func(r *Request) {
			r.Made = []Made{made(0, MadeDone, "CND-1"), made(1, MadeDone, "CND-2")}
			r.Edges = []Edge{{From: 0, To: 1, Op: "bke", State: EdgeMade}}
		}},
		{"an edge waiting for its other end", false, func(r *Request) {
			r.Made = []Made{made(0, MadeDone, "CND-1")}
			r.Edges = []Edge{{From: 0, To: 1, Op: "bke", State: EdgePending}}
		}},
	}
	for _, c := range cases {
		f := newFixture(t)
		r := f.start("op-1")
		f.close(r.ID, StateBriefed)
		if _, err := f.s.Update(r.ID, func(r *Request) error { c.fn(r); return nil }); err != nil {
			t.Fatal(c.name, err)
		}
		if _, err := f.s.Prune(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Get(r.ID); (err == nil) != c.pinned {
			t.Errorf("%s: kept %v (%v)", c.name, err == nil, err)
		}
	}
	// A pending act, written while the turn ran (BK2's acts are taken
	// only then), pins its request once closed.
	f := newFixture(t)
	r := f.start("op-1")
	if _, err := f.s.Update(r.ID, func(r *Request) error {
		r.Acts = []Act{{OpID: "a-1", Operation: "task_retry", Target: ref(t, "task:"+runA+"/build"), Run: runA, Op: "bka", Args: strings.Repeat("0", 64), State: "pending", At: now}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.close(r.ID, StateEnded)
	if _, err := f.s.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Get(r.ID); err != nil {
		t.Errorf("a pending act: %v", err)
	}
}

// TestOpenInterruptsWhatWasStartingOrRunning: a restart turns a starting
// or running request into interrupted, with why, and leaves a queued one
// queued.
func TestOpenInterruptsWhatWasStartingOrRunning(t *testing.T) {
	f := newFixture(t)
	queued := f.create("op-0")
	starting := f.start("op-1")
	running := f.start("op-2")
	if _, err := f.s.Update(running.ID, func(r *Request) error {
		r.RunID, r.SessionID = runA, sessA
		return r.To(StateRunning, "", f.clock.now())
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.add(time.Minute)
	f.reopen()
	for id, want := range map[string]string{queued.ID: StateQueued, starting.ID: StateInterrupted, running.ID: StateInterrupted} {
		r, err := f.s.Get(id)
		if err != nil || r.State != want {
			t.Fatalf("%s: %s %v", id, r.State, err)
		}
		if want == StateInterrupted && (!strings.Contains(r.Why, "server stopped") || !r.ClosedAt.Equal(f.clock.now())) {
			t.Errorf("%s: why %q at %v", id, r.Why, r.ClosedAt)
		}
	}
}

// TestALaterSchemaIsSkippedNotRewritten: a request of a later schema is
// left out of List and every walk, said once, refused to Update, and its
// bytes never change.
func TestALaterSchemaIsSkippedNotRewritten(t *testing.T) {
	f := newFixture(t)
	f.create("op-1")
	later := []byte(`{"schema": 2, "id": "br-7", "state": "running", "whatever": {"comes": "next"}}` + "\n")
	if err := os.WriteFile(f.reqPath("br-7.json"), later, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.reqPath("br-7-brief.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	page, _, err := f.s.List("", MaxPage)
	if err != nil || len(page) != 1 || page[0].ID != "br-1" {
		t.Fatalf("list: %v %v", ids(page), err)
	}
	f.s.List("", MaxPage)
	if _, err := f.s.Get("br-7"); !errors.Is(err, ErrLaterSchema) {
		t.Fatalf("get: %v", err)
	}
	if _, err := f.s.Update("br-7", func(*Request) error { return nil }); !errors.Is(err, ErrLaterSchema) {
		t.Fatalf("update: %v", err)
	}
	keepNewest = 0
	t.Cleanup(func() { keepNewest = Keep })
	if _, err := f.s.Prune(); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("pending: %v %v", p, err)
	}
	b, err := os.ReadFile(f.reqPath("br-7.json"))
	if err != nil || !bytes.Equal(b, later) {
		t.Fatalf("the later request was rewritten: %q %v", b, err)
	}
	if !exists(t, f.reqPath("br-7-brief.json")) {
		t.Fatal("a later request's sibling was removed")
	}
	if n := strings.Count(f.logged(), "later schema"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, f.logged())
	}
	// A later state.json leaves every write refused, and is not rewritten.
	state := []byte(`{"schema": 2, "last": 9}` + "\n")
	if err := os.WriteFile(filepath.Join(f.data.Dir(), Dir, StateFile), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Create(Request{OpID: "op-2", Kind: KindBriefing}); !errors.Is(err, ErrLaterSchema) {
		t.Fatalf("create over a later state: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.data.Dir(), Dir, StateFile)); !bytes.Equal(b, state) {
		t.Fatal("the later state was rewritten")
	}
}

// TestTurnsTodayCountsByTheLocalDay: a turn counts in the server's local
// day it started, on a fake clock across midnight.
func TestTurnsTodayCountsByTheLocalDay(t *testing.T) {
	f := newFixture(t)
	late := time.Date(2026, 11, 1, 23, 50, 0, 0, zone) // 05:50 UTC the next day
	f.clock.set(late)
	f.start("op-1")
	f.start("op-2")
	if n, _ := f.s.TurnsToday(late); n != 2 {
		t.Fatalf("before midnight: %d", n)
	}
	f.clock.add(20 * time.Minute)
	after := f.clock.now()
	if n, _ := f.s.TurnsToday(after); n != 0 {
		t.Fatalf("after local midnight: %d", n)
	}
	f.start("op-3")
	if n, _ := f.s.TurnsToday(after); n != 1 {
		t.Fatalf("a turn after midnight: %d", n)
	}
	if n, _ := f.s.TurnsToday(late); n != 2 {
		t.Fatalf("the day before, after midnight: %d", n)
	}
	// The day's turns spent, a start closes the request over_budget.
	c := onConfig()
	c.Budget.TurnsPerDay = 1
	r := f.create("op-4")
	if r, err := f.s.Start(r.ID, c, Stamp{}); !errors.Is(err, ErrOverBudget) || r.State != StateOverBudget {
		t.Fatalf("past the day's turns: %s %v", r.State, err)
	}
	if n, _ := f.s.TurnsToday(after); n != 1 {
		t.Fatalf("an over-budget request was charged: %d", n)
	}
	// Charges older than ChargeDays local days are dropped.
	f.clock.add(ChargeDays * 24 * time.Hour)
	f.start("op-5")
	if n, _ := f.s.TurnsToday(late); n != 0 {
		t.Fatalf("a day past the window still counts %d", n)
	}
}

// TestTheDaysCountSurvivesPruneAndCancel: charges are kept apart from the
// requests, so 600 cancelled requests and a prune lower nothing.
func TestTheDaysCountSurvivesPruneAndCancel(t *testing.T) {
	// Parallel with the other test of hundreds of requests only: the
	// sequential tests, which set the package's seams, have ended.
	t.Parallel()
	f := newFixture(t)
	for i := range 3 {
		f.close(f.start(fmt.Sprintf("t-%d", i)).ID, StateCancelled)
	}
	for i := range 600 {
		f.close(f.create(fmt.Sprintf("c-%d", i)).ID, StateCancelled)
	}
	if n, err := f.s.Prune(); err != nil || n != 103 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if _, err := f.s.Get("br-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the first turn's request is still there: %v", err)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 3 {
		t.Fatalf("the day's turns after prune and cancel: %d", n)
	}
	f.reopen()
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 3 {
		t.Fatalf("after a restart: %d", n)
	}
}

// TestUpdateIsAtomicUnderConcurrency: 32 goroutines changing one request
// lose no change.
func TestUpdateIsAtomicUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if _, err := f.s.Update(r.ID, func(r *Request) error { r.Reads++; return nil }); err != nil {
					t.Error(err)
				}
			}
			if _, err := f.s.Update(r.ID, func(r *Request) error {
				r.Made = append(r.Made, Made{Item: g, OpID: fmt.Sprintf("m-%d", g), Op: "bk", State: MadePending, At: time.Now()})
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := f.s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reads != 160 || len(got.Made) != 32 {
		t.Fatalf("reads %d, made %d", got.Reads, len(got.Made))
	}
	items := map[int]bool{}
	for _, m := range got.Made {
		items[m.Item] = true
	}
	if len(items) != 32 {
		t.Fatalf("made items %v", items)
	}
}

// TestASecondOpenIsReadOnlyUntilItHoldsTheLock: two stores on one data
// directory: the second refuses writes, interrupts nothing, and takes the
// desk once the first closes, concluding then.
func TestASecondOpenIsReadOnlyUntilItHoldsTheLock(t *testing.T) {
	old := LockWait
	LockWait = time.Minute
	t.Cleanup(func() { LockWait = old })
	f := newFixture(t)
	r := f.start("op-1")
	second := f.open()
	ro, why := second.ReadOnly()
	if !ro || !strings.HasPrefix(why, "waiting for the owner lock: another server holds") {
		t.Fatalf("the second store: read-only %v: %s", ro, why)
	}
	if _, _, err := second.Create(Request{OpID: "op-2", Kind: KindBriefing}); !errors.Is(err, ErrNotOwner) || !strings.Contains(err.Error(), "another server holds") {
		t.Fatalf("a write to the second store: %v", err)
	}
	if _, err := second.SaveConfig(onConfig(), goodFacts()); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("a configuration saved by the second store: %v", err)
	}
	// Reads are served from the files.
	if got, err := second.Get(r.ID); err != nil || got.State != StateStarting {
		t.Fatalf("a read from the second store: %s %v", got.State, err)
	}
	if got, _ := f.s.Get(r.ID); got.State != StateStarting {
		t.Fatalf("the second store interrupted the first's turn: %s", got.State)
	}
	owned := make(chan struct{})
	second.OnOwned(func() { close(owned) })
	f.s.Close()
	select {
	case <-owned:
	case <-time.After(30 * time.Second):
		ro, why := second.ReadOnly()
		t.Fatalf("the lock was not taken once let go: %v %s", ro, why)
	}
	if ro, why := second.ReadOnly(); ro {
		t.Fatalf("still read-only: %s", why)
	}
	if got, _ := second.Get(r.ID); got.State != StateInterrupted {
		t.Fatalf("the new owner did not conclude the last one's turn: %s", got.State)
	}
	if _, _, err := second.Create(Request{OpID: "op-2", Kind: KindBriefing}); err != nil {
		t.Fatalf("a write once owned: %v", err)
	}
}

// TestPendingListsUnfinishedMakes: a pending claim and an edge between
// two tickets are listed for the desk; Open completes neither.
func TestPendingListsUnfinishedMakes(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil {
		t.Fatal(err)
	}
	now := f.clock.now()
	if _, err := f.s.Update(r.ID, func(r *Request) error {
		r.Made = []Made{
			{Item: 0, OpID: "m-0", Op: "bk0", State: MadePending, At: now},
			{Item: 1, OpID: "m-1", Op: "bk1", State: MadeDone, Key: "CND-1", At: now},
			{Item: 2, OpID: "m-2", Op: "bk2", State: MadeDone, Key: "CND-2", At: now},
		}
		r.Edges = []Edge{{From: 1, To: 2, Op: "bke12", State: EdgePending}, {From: 0, To: 1, Op: "bke01", State: EdgePending}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pending := func() []string {
		p, err := f.s.Pending()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range p {
			switch {
			case m.Made != nil:
				out = append(out, fmt.Sprintf("%s made %d %s", m.Request, m.Made.Item, m.Made.Op))
			case m.Edge != nil:
				out = append(out, fmt.Sprintf("%s edge %d-%d %s", m.Request, m.Edge.From, m.Edge.To, m.Edge.Op))
			}
		}
		return out
	}
	want := []string{r.ID + " made 0 bk0", r.ID + " edge 1-2 bke12"}
	if got := pending(); !slices.Equal(got, want) {
		t.Fatalf("pending %q, want %q", got, want)
	}
	f.reopen()
	if got := pending(); !slices.Equal(got, want) {
		t.Fatalf("after Open, pending %q, want %q", got, want)
	}
}

// TestEveryDocumentFitsItsLoadLimit: each document at its largest is
// written, and read back after a restart.
func TestEveryDocumentFitsItsLoadLimit(t *testing.T) {
	f := newFixture(t)
	// The configuration at its largest.
	c, err := f.s.Config()
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled, c.AgentID, c.Model = true, "claude", "m"+strings.Repeat("x", 99)
	c.Account, c.Cwd = longPath(MaxPath), longPath(MaxPath)
	for i := range MaxSelector {
		c.Scope.Repos = append(c.Scope.Repos, fmt.Sprintf("/r%03d", i)+strings.Repeat("q", 150))
	}
	c, err = f.s.SaveConfig(c, goodFacts())
	if err != nil {
		t.Fatal(err)
	}
	// A brief near its bound, its resolutions at their largest, a read log
	// past its bound (cut to fit), a make's input near its bound.
	r := f.start("op-1")
	b := goodBrief(t)
	for range 10 {
		it := draftTicket(t, "CND", "Big")
		it.DraftTicket.Description, it.DraftTicket.Acceptance = pad("d", 16<<10), pad("a", 8<<10)
		b.Items = append(b.Items, it)
	}
	if err := check(b); err != nil {
		t.Fatal(err)
	}
	var refs []Resolution
	big := RefState{URL: "/" + strings.Repeat(`\`, MaxURL-1), State: pad("s", MaxRefState), Rev: pad("r", MaxRevision), SHA256: strings.Repeat("f", 64), At: f.clock.now()}
	for i := range MaxBriefRefs {
		head := fmt.Sprintf("file:%s//%d/", sessA, i)
		refs = append(refs, Resolution{Ref: ref(t, head+strings.Repeat(`\`, MaxRef-len(head))), Seen: big, Now: big})
	}
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: b, Refs: refs}); err != nil {
		t.Fatal(err)
	}
	r2 := f.start("op-2")
	// state.json at its largest: MaxOps op ids, ChargeDays of the most turns.
	f.s.mu.Lock()
	st, err := f.s.loadState()
	if err == nil {
		st.Last = 1 << 50
		st.Days = map[string]map[string]Charge{}
		for d := range ChargeDays {
			day := f.s.day(f.clock.now().AddDate(0, 0, -d))
			if st.Days[day] == nil {
				st.Days[day] = map[string]Charge{}
			}
			for i := 0; len(st.Days[day]) < MaxTurnsPerDay; i++ {
				m := MaxTurnMinutes
				st.Days[day][requestID(int64(1<<49+d*1000+i))] = Charge{Reserved: m, Settled: &m}
			}
		}
		for i := range MaxOps {
			st.Ops = append(st.Ops, opRef{OpID: fmt.Sprintf("%032d", i), ID: requestID(int64(1<<49 + i))})
		}
		err = f.s.saveState(st)
	}
	f.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var reads []Read
	for i := range MaxReadLog + 10 {
		head := fmt.Sprintf("file:%s//%d/", sessA, i)
		reads = append(reads, Read{Ref: ref(t, head+strings.Repeat("x", MaxRef-len(head))), Class: ClassTouched, Rev: pad("r", MaxRevision), State: pad("s", MaxRefState), At: f.clock.now()})
	}
	log, err := f.s.NoteReads(r2.ID, reads...)
	if err != nil {
		t.Fatal(err)
	}
	if log.Dropped == 0 || len(log.Reads)+log.Dropped != len(reads) || log.Reads[len(log.Reads)-1].Ref != reads[len(reads)-1].Ref {
		t.Fatalf("the read log kept %d and dropped %d", len(log.Reads), log.Dropped)
	}
	input := json.RawMessage(`{"description":"` + strings.Repeat("i", MaxMakeDoc-64) + `"}`)
	if err := f.s.PutMakeInput(r2.ID, 3, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Update(r2.ID, func(r *Request) error {
		r.Made = []Made{{Item: 3, OpID: "m-3", Op: "bk3", State: MadePending, At: f.clock.now()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, limit := range map[string]int{
		filepath.Join(f.data.Dir(), Dir, ConfigFile): MaxConfigDoc,
		filepath.Join(f.data.Dir(), Dir, StateFile):  MaxStateDoc,
		f.reqPath(briefDoc(r.ID)):                    MaxBrief,
		f.reqPath(refsDoc(r.ID)):                     MaxRefsDoc,
		f.reqPath(readsDoc(r2.ID)):                   MaxReadsDoc,
		f.reqPath(makeDoc(r2.ID, 3)):                 MaxMakeDoc,
	} {
		fi, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > int64(limit) || fi.Size() < int64(limit)/4 {
			t.Errorf("%s is %d bytes against its bound %d", filepath.Base(name), fi.Size(), limit)
		}
	}
	f.reopen()
	if got, err := f.s.Config(); err != nil || got.Rev != c.Rev {
		t.Fatalf("config: %v", err)
	}
	if n, err := f.s.TurnsToday(f.clock.now()); err != nil || n != MaxTurnsPerDay {
		t.Fatalf("state: %d %v", n, err)
	}
	if kb, kr, err := f.s.Brief(r.ID); err != nil || len(kb.Items) != 11 || len(kr) != MaxBriefRefs {
		t.Fatalf("brief: %d items, %d refs, %v", len(kb.Items), len(kr), err)
	}
	if got, err := f.s.Reads(r2.ID); err != nil || len(got.Reads) != len(log.Reads) {
		t.Fatalf("reads: %v", err)
	}
	got, err := f.s.MakeInput(r2.ID, 3)
	var gotC, wantC bytes.Buffer
	if err != nil || json.Compact(&gotC, got) != nil || json.Compact(&wantC, input) != nil || !bytes.Equal(gotC.Bytes(), wantC.Bytes()) {
		t.Fatalf("make input: %d bytes back, %v", len(got), err)
	}
	// One past: a make's input over its bound is refused, nothing written.
	over := json.RawMessage(`{"description":"` + strings.Repeat("i", MaxMakeDoc) + `"}`)
	if err := f.s.PutMakeInput(r2.ID, 4, over); !errors.Is(err, ErrBound) {
		t.Fatalf("a make over its bound: %v", err)
	}
	if exists(t, f.reqPath(makeDoc(r2.ID, 4))) {
		t.Fatal("a refused make input was written")
	}
}

// TestARequestWithEveryListFullStillLoads: every field at its largest and
// every list full, the request is written, and read back after a restart.
func TestARequestWithEveryListFullStillLoads(t *testing.T) {
	f := newFixture(t)
	var subjects []Ref
	for i := range MaxSubjects {
		head := fmt.Sprintf("file:%s//%d/", sessA, i)
		subjects = append(subjects, ref(t, head+strings.Repeat(`"`, MaxRef-len(head))))
	}
	r, _, err := f.s.Create(Request{OpID: strings.Repeat("o", 32), Kind: KindAsk, Note: strings.Repeat(`"`, MaxNote), Subjects: subjects, By: strings.Repeat(`"`, MaxBy)})
	if err != nil {
		t.Fatal(err)
	}
	c := onConfig()
	for i := 0; ; i++ {
		repo := fmt.Sprintf(`/%03d`, i) + strings.Repeat(`"`, 130)
		next := c.Scope
		next.Repos = append(slices.Clone(next.Repos), repo)
		if len(next.Repos) > MaxSelector || next.validate() != nil {
			break
		}
		c.Scope = next
	}
	for i := range MaxSelector {
		c.Scope.Sessions = append(c.Scope.Sessions, fmt.Sprintf("%016x", i))
	}
	if c.Scope.validate() != nil {
		c.Scope.Sessions = c.Scope.Sessions[:10]
	}
	stamp := Stamp{AgentID: pad("a", 32), Adapter: pad("b", 32), Version: strings.Repeat(`"`, 128), Model: pad("m", 100), Account: true}
	if _, err := f.s.Start(r.ID, c, stamp); err != nil {
		t.Fatal(err)
	}
	now := f.clock.now()
	full := func(r *Request) error {
		r.Why = strings.Repeat(`"`, MaxWhy)
		r.RunID, r.SessionID, r.StrayRun = pad("r", maxID), pad("s", maxID), pad("x", maxID)
		r.Reads = 1 << 40
		r.Footprint = &Footprint{Classes: slices.Clone(Classes)}
		r.Coverage = &Coverage{Dispatch: &Watermark{ID: pad("d", maxID), Seq: 1 << 60, At: now, Partial: true, New: true}}
		for i := range MaxWatermarks {
			w := Watermark{ID: strings.Repeat(`"`, maxID-4) + fmt.Sprintf("%04d", i), Seq: 1 << 60, At: now, Partial: true, New: true}
			r.Coverage.Runs = append(r.Coverage.Runs, w)
			r.Coverage.Sessions = append(r.Coverage.Sessions, w)
		}
		for i := range MaxMade {
			m := Made{Item: i, OpID: strings.Repeat("o", 32), Op: strings.Repeat("p", 32), Input: strings.Repeat("f", 64), State: MadeRefused, By: strings.Repeat(`"`, MaxBy), Key: "ABCDEF-1234567890", Crew: strings.Repeat("c", 64), Rev: 1 << 60, At: now}
			for entrySize(m) < MaxMadeBytes-1 {
				m.Why += `"`
			}
			r.Made = append(r.Made, m)
		}
		for i := range MaxEdges {
			r.Edges = append(r.Edges, Edge{From: 99 - i%100, To: i % 100, Op: strings.Repeat("p", 32), State: EdgePending})
		}
		for range MaxActs {
			a := Act{OpID: strings.Repeat("o", 32), Operation: pad("op", 64), Target: ref(t, "task:"+runA+"/build"), Run: pad("r", 73), Op: strings.Repeat("p", 32), Args: strings.Repeat("f", 64), State: "pending", Receipt: "", At: now}
			for entrySize(a) < MaxActBytes-1 {
				a.Why += `"`
			}
			r.Acts = append(r.Acts, a)
		}
		r.Deadline = now
		return r.To(StateRunning, "", now)
	}
	if _, err := f.s.Update(r.ID, full); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(f.reqPath(requestDoc(r.ID)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the full request is %d bytes of %d", fi.Size(), MaxRequestDoc)
	f.reopen() // which interrupts it: one more write, still within its bound
	got, err := f.s.Get(r.ID)
	if err != nil || len(got.Made) != MaxMade || len(got.Edges) != MaxEdges || len(got.Acts) != MaxActs || got.State != StateInterrupted {
		t.Fatalf("read back: %d made, %d edges, %d acts, %s, %v", len(got.Made), len(got.Edges), len(got.Acts), got.State, err)
	}
	// One entry more of any list, or one byte more of an entry, is refused
	// with nothing written.
	before, _ := os.ReadFile(f.reqPath(requestDoc(r.ID)))
	for name, fn := range map[string]func(*Request){
		"a made entry":         func(r *Request) { r.Made = append(r.Made, Made{Item: 100}) },
		"an edge":              func(r *Request) { r.Edges = append(r.Edges, Edge{}) },
		"a made entry's bytes": func(r *Request) { r.Made[0].Why += `"""` },
	} {
		if _, err := f.s.Update(r.ID, func(r *Request) error { fn(r); return nil }); !errors.Is(err, ErrBound) {
			t.Errorf("%s past its bound: %v", name, err)
		}
	}
	if after, _ := os.ReadFile(f.reqPath(requestDoc(r.ID))); !bytes.Equal(before, after) {
		t.Fatal("a refused change was written")
	}
}

// TestABriefWhoseRefsPassTheirBoundIsRefusedUnwritten: 100 panels of 50
// distinct refs pass the brief's 500, and resolutions past theirs are
// refused, with nothing written either way.
func TestABriefWhoseRefsPassTheirBoundIsRefusedUnwritten(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	b := goodBrief(t)
	b.Items = nil
	for p := range 100 {
		refs := make([]Ref, 50)
		for i := range refs {
			refs[i] = ref(t, fmt.Sprintf("event:%s/%d", runA, p*50+i+1))
		}
		b.Items = append(b.Items, Item{Kind: ItemEvidence, Evidence: &EvidencePanel{Title: fmt.Sprintf("Panel %d", p), Refs: refs}})
	}
	inv := wantInvalid(t, check(b), "items")
	if !strings.Contains(inv.Why, "5000 distinct refs, at most 500") {
		t.Errorf("why: %s", inv.Why)
	}
	// The store refuses as much from a caller that did not check.
	var res []Resolution
	for i := range MaxBriefRefs + 1 {
		res = append(res, Resolution{Ref: ref(t, fmt.Sprintf("event:%s/%d", runA, i+1))})
	}
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t), Refs: res}); !errors.Is(err, ErrBound) {
		t.Fatalf("501 resolutions: %v", err)
	}
	res = res[:2]
	res[1].Now.URL = strings.Repeat("u", MaxURL+1)
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t), Refs: res}); !errors.Is(err, ErrBound) {
		t.Fatalf("a url past its bound: %v", err)
	}
	for _, name := range []string{briefDoc(r.ID), refsDoc(r.ID)} {
		if exists(t, f.reqPath(name)) {
			t.Errorf("%s was written", name)
		}
	}
	if got, _ := f.s.Get(r.ID); got.HasBrief || got.State != StateStarting {
		t.Fatalf("the request moved: %+v", got)
	}
	// A brief kept answers its op id again, and refuses any other.
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t), RunID: runA, SessionID: sessA}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil || got.State != StateBriefed || got.RunID != runA {
		t.Fatalf("the same brief again: %+v %v", got, err)
	}
	other := goodBrief(t)
	other.Title = "Another"
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: other}); !errors.Is(err, ErrBriefExists) {
		t.Fatalf("another brief: %v", err)
	}
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-2", Brief: goodBrief(t)}); !errors.Is(err, ErrBriefExists) {
		t.Fatalf("another op id: %v", err)
	}
	q := f.create("op-2")
	if _, err := f.s.KeepBrief(q.ID, Kept{OpID: "b-3", Brief: goodBrief(t)}); !errors.Is(err, ErrNotTurn) {
		t.Fatalf("a brief for a queued request: %v", err)
	}
}

// TestOrphanSiblingsAreRemovedAtOpen: siblings a request does not name (a
// crash between their write and the request's) go at Open; the ones it
// names stay.
func TestOrphanSiblingsAreRemovedAtOpen(t *testing.T) {
	f := newFixture(t)
	crashed := f.start("op-1")
	failWrite(t, RequestsDir+"/"+requestDoc(crashed.ID))
	if _, err := f.s.KeepBrief(crashed.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err == nil {
		t.Fatal("the request's write failed and KeepBrief said nothing")
	}
	beforeWrite = nil
	if !exists(t, f.reqPath(briefDoc(crashed.ID))) || !exists(t, f.reqPath(refsDoc(crashed.ID))) {
		t.Fatal("the crash seam wrote no sibling")
	}
	kept := f.start("op-2")
	if _, err := f.s.KeepBrief(kept.ID, Kept{OpID: "b-2", Brief: goodBrief(t)}); err != nil {
		t.Fatal(err)
	}
	reading := f.start("op-3")
	if _, err := f.s.NoteReads(reading.ID, Read{Ref: ref(t, "run:"+runA), Class: ClassRuns, At: f.clock.now()}); err != nil {
		t.Fatal(err)
	}
	// A make's input with its claim, and one whose claim never landed.
	if err := f.s.PutMakeInput(kept.ID, 0, json.RawMessage(`{"title":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Update(kept.ID, func(r *Request) error {
		r.Made = []Made{{Item: 0, OpID: "m-0", Op: "bk0", State: MadePending, At: f.clock.now()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutMakeInput(kept.ID, 1, json.RawMessage(`{"title":"b"}`)); err != nil {
		t.Fatal(err)
	}
	// Siblings of a request that is not there at all (pruned between its
	// document and its siblings).
	for _, name := range []string{"br-90-brief.json", "br-90-refs.json", "br-90-reads.json", "br-90-make-2.json"} {
		if err := os.WriteFile(f.reqPath(name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.reopen()
	for name, want := range map[string]bool{
		briefDoc(crashed.ID):   false,
		refsDoc(crashed.ID):    false,
		readsDoc(crashed.ID):   false,
		briefDoc(kept.ID):      true,
		refsDoc(kept.ID):       true,
		makeDoc(kept.ID, 0):    true,
		makeDoc(kept.ID, 1):    false,
		readsDoc(reading.ID):   true,
		"br-90-brief.json":     false,
		"br-90-refs.json":      false,
		"br-90-reads.json":     false,
		"br-90-make-2.json":    false,
		requestDoc(crashed.ID): true,
	} {
		if got := exists(t, f.reqPath(name)); got != want {
			t.Errorf("%s there %v, want %v", name, got, want)
		}
	}
	if r, _ := f.s.Get(crashed.ID); r.HasBrief || r.State != StateInterrupted {
		t.Errorf("the crashed request: %+v", r)
	}
}

// TestDocumentNamesAreTheStoresOwn: every name the package writes is one
// internal/store takes, and List reads only the requests' own.
func TestDocumentNamesAreTheStoresOwn(t *testing.T) {
	storeName := regexp.MustCompile(`^[a-z0-9-]+\.json$`)
	listed := regexp.MustCompile(`^br-[0-9]+\.json$`)
	for _, id := range []string{"br-1", "br-42", "br-999999999999999999"} {
		names := []string{requestDoc(id), briefDoc(id), refsDoc(id), readsDoc(id), makeDoc(id, 0), makeDoc(id, MaxItems-1)}
		for i, name := range names {
			if !storeName.MatchString(name) {
				t.Errorf("%s is not a name the store takes", name)
			}
			if listed.MatchString(name) != (i == 0) {
				t.Errorf("%s listed %v", name, listed.MatchString(name))
			}
			if _, ok := requestName(name); ok != (i == 0) {
				t.Errorf("%s read as a request %v", name, ok)
			}
			if _, _, ok := siblingName(name); ok == (i == 0) {
				t.Errorf("%s read as a sibling %v", name, ok)
			}
		}
	}
	for _, name := range []string{ConfigFile, StateFile} {
		if !storeName.MatchString(name) {
			t.Errorf("%s is not a name the store takes", name)
		}
	}
	f := newFixture(t)
	r := f.start("op-1")
	if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil {
		t.Fatal(err)
	}
	if page, _, err := f.s.List("", MaxPage); err != nil || len(page) != 1 {
		t.Fatalf("list: %v %v", ids(page), err)
	}
	fi, err := os.Stat(filepath.Join(f.data.Dir(), Dir))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("brakeman/ is %v: %v", fi.Mode().Perm(), err)
	}
	if !exists(t, filepath.Join(f.data.Dir(), Dir, Lock)) {
		t.Fatal("no owner lock")
	}
}

// TestAChargeIsWrittenBeforeTheStart: a crash between the charge and the
// start leaves one charge and the request queued; the next start of it
// counts nothing again.
func TestAChargeIsWrittenBeforeTheStart(t *testing.T) {
	f := newFixture(t)
	r := f.create("op-1")
	afterCharge = func() error { return errors.New("the test's crash") }
	t.Cleanup(func() { afterCharge = nil })
	if _, err := f.s.Start(r.ID, onConfig(), Stamp{}); err == nil {
		t.Fatal("a crash after the charge was not said")
	}
	afterCharge = nil
	if got, _ := f.s.Get(r.ID); got.State != StateQueued {
		t.Fatalf("the request is %s", got.State)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 1 {
		t.Fatalf("the charge: %d turns", n)
	}
	f.reopen()
	got, err := f.s.Start(r.ID, onConfig(), Stamp{})
	if err != nil || got.State != StateStarting {
		t.Fatalf("the next start: %s %v", got.State, err)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 1 {
		t.Fatalf("after the next start: %d turns", n)
	}
	if want := f.clock.now().UTC().Add(DefaultTurnMinutes * time.Minute); !got.Deadline.Equal(want) {
		t.Fatalf("deadline %v, want %v", got.Deadline, want)
	}
}

// TestAChargeIsCountedOncePerRequest: a request is charged once, however
// often its start is tried; two requests are two.
func TestAChargeIsCountedOncePerRequest(t *testing.T) {
	f := newFixture(t)
	r := f.start("op-1")
	if _, err := f.s.Start(r.ID, onConfig(), Stamp{}); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("a second start: %v", err)
	}
	f.close(r.ID, StateCancelled)
	if _, err := f.s.Start(r.ID, onConfig(), Stamp{}); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("a start of a closed request: %v", err)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 1 {
		t.Fatalf("%d turns", n)
	}
	f.start("op-2")
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 2 {
		t.Fatalf("%d turns", n)
	}
}

// TestAnUnsettledChargeCountsItsReservation: a crash before the settled
// minutes are written leaves the reservation as the charge; a settled
// turn counts the minutes it ran, rounded up.
func TestAnUnsettledChargeCountsItsReservation(t *testing.T) {
	f := newFixture(t)
	crashed := f.start("op-1")
	f.clock.add(2 * time.Minute)
	failWrite(t, StateFile)
	if _, err := f.s.Update(crashed.ID, func(r *Request) error { return r.To(StateEnded, "", f.clock.now()) }); err == nil {
		t.Fatal("the settle's crash was not said")
	}
	beforeWrite = nil
	if got, _ := f.s.Get(crashed.ID); got.State != StateEnded {
		t.Fatalf("the request is %s", got.State)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != DefaultTurnMinutes {
		t.Fatalf("an unsettled turn counts %d minutes", m)
	}
	settled := f.start("op-2")
	f.clock.add(2*time.Minute + time.Second)
	f.close(settled.ID, StateEnded)
	if m, _ := f.s.MinutesToday(f.clock.now()); m != DefaultTurnMinutes+3 {
		t.Fatalf("with a settled turn of 2m1s: %d minutes", m)
	}
	// A brief settles its turn too, never past its reservation.
	long := f.start("op-3")
	f.clock.add(time.Hour)
	if _, err := f.s.KeepBrief(long.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 2*DefaultTurnMinutes+3 {
		t.Fatalf("with a turn past its reservation: %d minutes", m)
	}
}

// TestAChargeBelongsToItsStartDay: a turn across midnight is charged
// wholly to the day it started; midnight neither splits nor refunds it.
func TestAChargeBelongsToItsStartDay(t *testing.T) {
	f := newFixture(t)
	start := time.Date(2026, 11, 1, 23, 55, 0, 0, zone)
	f.clock.set(start)
	c := onConfig()
	c.Budget.TurnMinutes = MaxTurnMinutes
	r, err := f.s.Start(f.create("op-1").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.add(25 * time.Minute)
	f.close(r.ID, StateEnded)
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 0 {
		t.Fatalf("the next day counts %d turns", n)
	}
	if m, _ := f.s.MinutesToday(f.clock.now()); m != 0 {
		t.Fatalf("the next day counts %d minutes", m)
	}
	if n, _ := f.s.TurnsToday(start); n != 1 {
		t.Fatalf("the start day counts %d turns", n)
	}
	if m, _ := f.s.MinutesToday(start); m != 25 {
		t.Fatalf("the start day counts %d minutes", m)
	}
}

// TestADamagedRequestIsSkippedByEveryWalk: a request document that is
// empty, cut short or of the wrong shape is skipped by Open, List, Pending
// and Prune, kept as it is with its siblings and said once; a document the
// disk will not give is said, never skipped.
func TestADamagedRequestIsSkippedByEveryWalk(t *testing.T) {
	f := newFixture(t)
	f.create("op-1")
	damaged := map[string]string{
		"br-5.json": "",
		"br-6.json": `{"schema": 1, "id": "br-6", "st`,
		"br-7.json": `{"schema": 1, "id": "br-7", "nope": true}`,
	}
	for name, body := range damaged {
		if err := os.WriteFile(f.reqPath(name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.reqPath("br-6-brief.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen() // Open concludes past them
	page, _, err := f.s.List("", MaxPage)
	if err != nil || len(page) != 1 || page[0].ID != "br-1" {
		t.Fatalf("list: %v %v", ids(page), err)
	}
	if p, err := f.s.Pending(); err != nil || len(p) != 0 {
		t.Fatalf("pending: %v %v", p, err)
	}
	keepNewest = 0
	t.Cleanup(func() { keepNewest = Keep })
	if _, err := f.s.Prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for name, body := range damaged {
		id := strings.TrimSuffix(name, ".json")
		if _, err := f.s.Get(id); !errors.Is(err, ErrDamaged) {
			t.Errorf("get %s: %v", id, err)
		}
		if b, err := os.ReadFile(f.reqPath(name)); err != nil || string(b) != body {
			t.Errorf("%s was rewritten: %q %v", name, b, err)
		}
	}
	if !exists(t, f.reqPath("br-6-brief.json")) {
		t.Error("a damaged request's sibling was removed")
	}
	if n := strings.Count(f.logged(), "does not parse"); n != len(damaged) {
		t.Errorf("said %d times:\n%s", n, f.logged())
	}
	if os.Geteuid() == 0 {
		return // root reads a file of mode 0
	}
	r := f.create("op-2")
	if err := os.Chmod(f.reqPath(requestDoc(r.ID)), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f.reqPath(requestDoc(r.ID)), 0o600) })
	if _, err := f.s.Get(r.ID); err == nil || skippable(err) {
		t.Fatalf("an unreadable request: %v", err)
	}
	if _, _, err := f.s.List("", MaxPage); err == nil {
		t.Fatal("a walk passed over a request the disk would not give")
	}
}

// TestCloseWaitsForTheOwnedCallback: the recovery OnOwned runs keeps the
// owner lock until it returns, though Close was called meanwhile; and a
// function set after Close never runs.
func TestCloseWaitsForTheOwnedCallback(t *testing.T) {
	old := LockWait
	LockWait = time.Minute
	t.Cleanup(func() { LockWait = old })
	f := newFixture(t)
	second := f.open()
	started, gate := make(chan struct{}), make(chan struct{})
	second.OnOwned(func() {
		close(started)
		<-gate
	})
	f.s.Close()
	<-started
	closed := make(chan struct{})
	go func() {
		second.Close()
		close(closed)
	}()
	// Close has begun once a write is refused as closed.
	for {
		if _, err := second.Prune(); err != nil && strings.Contains(err.Error(), "closed") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	lockPath := filepath.Join(f.data.Dir(), Dir, Lock)
	if lock, why, held, err := store.TakeOwnerLock(lockPath); lock != nil || !held || err != nil {
		if lock != nil {
			lock.Release()
		}
		t.Fatalf("the lock was let go while the recovery ran: %s %v", why, err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned while the recovery ran")
	default:
	}
	close(gate)
	<-closed
	lock, why, _, err := store.TakeOwnerLock(lockPath)
	if lock == nil || err != nil {
		t.Fatalf("the lock was kept after Close: %s %v", why, err)
	}
	lock.Release()

	// A store that took the lock with no function set, then closed: a
	// function set now never runs.
	f = newFixture(t)
	third := f.open()
	f.s.Close()
	for {
		if ro, _ := third.ReadOnly(); !ro {
			break
		}
		time.Sleep(time.Millisecond)
	}
	third.Close()
	ran := false
	third.OnOwned(func() { ran = true })
	if ran {
		t.Fatal("OnOwned ran its function after Close")
	}
}

// TestALockThePlatformCannotTakeLeavesTheStoreReadOnly: where the owner
// lock is unsupported the store serves reads, refuses every write with
// why, and waits for no lock.
func TestALockThePlatformCannotTakeLeavesTheStoreReadOnly(t *testing.T) {
	f := newFixture(t)
	r := f.create("op-1")
	f.s.Close()
	takeLock = func(string) (*store.OwnerLock, string, bool, error) {
		return nil, "the owner lock is not supported on this platform", false, nil
	}
	t.Cleanup(func() { takeLock = store.TakeOwnerLock })
	s := f.open()
	if ro, why := s.ReadOnly(); !ro || why != "the owner lock is not supported on this platform" {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if got, err := s.Get(r.ID); err != nil || got.State != StateQueued {
		t.Fatalf("a read: %s %v", got.State, err)
	}
	if _, _, err := s.Create(Request{OpID: "op-2", Kind: KindBriefing}); !errors.Is(err, ErrNotOwner) || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("a write: %v", err)
	}
	ran := false
	s.OnOwned(func() { ran = true })
	if ran {
		t.Fatal("a store without the lock ran OnOwned's function")
	}
}

// TestAStartPastABoundChargesNothing: a start whose request would pass a
// bound is refused before the charge, the request still queued.
func TestAStartPastABoundChargesNothing(t *testing.T) {
	f := newFixture(t)
	r := f.create("op-1")
	if _, err := f.s.Start(r.ID, onConfig(), Stamp{AgentID: "claude", Version: strings.Repeat("v", 129)}); !errors.Is(err, ErrBound) {
		t.Fatalf("a start with a stamp past its bound: %v", err)
	}
	if got, _ := f.s.Get(r.ID); got.State != StateQueued {
		t.Fatalf("the request is %s", got.State)
	}
	if n, _ := f.s.TurnsToday(f.clock.now()); n != 0 {
		t.Fatalf("a refused start charged %d turns", n)
	}
	if got, err := f.s.Start(r.ID, onConfig(), Stamp{AgentID: "claude"}); err != nil || got.State != StateStarting {
		t.Fatalf("the start within the bound: %s %v", got.State, err)
	}
}

// TestAChargeFromBeforeMidnightCountsOnTheDayItStarts: a request charged
// before midnight and started after it (a crash between the two writes)
// must fit the new day's turns, and its charge moves to that day.
func TestAChargeFromBeforeMidnightCountsOnTheDayItStarts(t *testing.T) {
	day1 := time.Date(2026, 11, 1, 23, 59, 0, 0, zone)
	day2 := time.Date(2026, 11, 2, 0, 5, 0, 0, zone)
	crashBeforeMidnight := func(f *fixture) Request {
		f.clock.set(day1)
		r := f.create("op-1")
		afterCharge = func() error { return errors.New("the test's crash") }
		defer func() { afterCharge = nil }()
		if _, err := f.s.Start(r.ID, onConfig(), Stamp{}); err == nil {
			t.Fatal("a crash after the charge was not said")
		}
		f.clock.set(day2)
		return r
	}
	t.Cleanup(func() { afterCharge = nil })
	c := onConfig()
	c.Budget.TurnsPerDay = 1

	// The new day's one turn is spent: the request is over budget.
	f := newFixture(t)
	r := crashBeforeMidnight(f)
	other, err := f.s.Start(f.create("op-2").ID, c, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	f.close(other.ID, StateEnded)
	if got, err := f.s.Start(r.ID, c, Stamp{}); !errors.Is(err, ErrOverBudget) || got.State != StateOverBudget {
		t.Fatalf("a start past the new day's turns: %s %v", got.State, err)
	}
	if n, _ := f.s.TurnsToday(day2); n != 1 {
		t.Fatalf("the new day counts %d turns", n)
	}

	// The new day has room: the turn starts and is counted there alone.
	f = newFixture(t)
	r = crashBeforeMidnight(f)
	got, err := f.s.Start(r.ID, c, Stamp{})
	if err != nil || got.State != StateStarting {
		t.Fatalf("the start after midnight: %s %v", got.State, err)
	}
	if n, _ := f.s.TurnsToday(day2); n != 1 {
		t.Fatalf("the day it started counts %d turns", n)
	}
	if n, _ := f.s.TurnsToday(day1); n != 0 {
		t.Fatalf("the day of the crash still counts %d turns", n)
	}
	if _, err := f.s.Start(f.create("op-2").ID, c, Stamp{}); !errors.Is(err, ErrOverBudget) {
		t.Fatalf("a second turn on a day of one: %v", err)
	}
}

// TestAKeptBriefRecordsTheFootprintOfItsReads: the footprint is read off
// the read log in the briefed write, so a read noted just before the
// brief is in it; a log that dropped reads counts every class the
// snapshot had on; the coverage given is kept beside it.
func TestAKeptBriefRecordsTheFootprintOfItsReads(t *testing.T) {
	f := newFixture(t)
	c := onConfig()
	c.Visibility.Terminals, c.Visibility.Touched = true, false
	now := f.clock.now()
	start := func(op string) Request {
		r, err := f.s.Start(f.create(op).ID, c, Stamp{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := start("op-1")
	if _, err := f.s.NoteReads(r.ID, Read{Ref: ref(t, "run:"+runA), Class: ClassRuns, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.NoteReads(r.ID, Read{Ref: ref(t, "session:"+sessA), Class: ClassTerminals, At: now}); err != nil {
		t.Fatal(err)
	}
	cov := &Coverage{Runs: []Watermark{{ID: runA, Seq: 7}}, Dispatch: &Watermark{Seq: 3}}
	kept, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t), Coverage: cov})
	if err != nil {
		t.Fatal(err)
	}
	f.reopen()
	got, err := f.s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []Request{kept, got} {
		if x.Footprint == nil || !slices.Equal(x.Footprint.Classes, []string{ClassRuns, ClassTerminals}) {
			t.Fatalf("the footprint: %+v", x.Footprint)
		}
		if x.Coverage == nil || len(x.Coverage.Runs) != 1 || x.Coverage.Runs[0].Seq != 7 || x.Coverage.Dispatch == nil || x.Coverage.Dispatch.Seq != 3 {
			t.Fatalf("the coverage: %+v", x.Coverage)
		}
	}

	// A log that dropped its oldest reads: every class the snapshot had on.
	r = start("op-2")
	reads := make([]Read, MaxReadLog+1)
	for i := range reads {
		reads[i] = Read{Ref: ref(t, fmt.Sprintf("event:%s/%d", runA, i+1)), Class: ClassRuns, At: now}
	}
	if log, err := f.s.NoteReads(r.ID, reads...); err != nil || log.Dropped == 0 {
		t.Fatalf("the read log: dropped %d, %v", log.Dropped, err)
	}
	kept, err = f.s.KeepBrief(r.ID, Kept{OpID: "b-2", Brief: goodBrief(t)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{ClassRuns, ClassLogs, ClassDispatch, ClassSessions, ClassTerminals}; kept.Footprint == nil || !slices.Equal(kept.Footprint.Classes, want) {
		t.Fatalf("the footprint of a log that dropped reads: %+v, want %v", kept.Footprint, want)
	}

	// No reads: an empty footprint, which no class is outside of.
	r = start("op-3")
	if kept, err = f.s.KeepBrief(r.ID, Kept{OpID: "b-3", Brief: goodBrief(t)}); err != nil || kept.Footprint == nil || len(kept.Footprint.Classes) != 0 {
		t.Fatalf("a brief with no reads: %+v %v", kept.Footprint, err)
	}
}

// TestABriefWhoseDocumentsAreGoneIsNotFound: a request read as having a
// brief whose brief or resolutions are gone (another server pruned it
// after the read) answers ErrNotFound, never an empty brief.
func TestABriefWhoseDocumentsAreGoneIsNotFound(t *testing.T) {
	for _, gone := range []string{"br-1-brief.json", "br-1-refs.json"} {
		f := newFixture(t)
		r := f.start("op-1")
		if _, err := f.s.KeepBrief(r.ID, Kept{OpID: "b-1", Brief: goodBrief(t)}); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.reqPath(gone)); err != nil {
			t.Fatal(err)
		}
		if b, refs, err := f.s.Brief(r.ID); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), gone) {
			t.Fatalf("%s gone: %+v %v %v", gone, b, refs, err)
		}
	}
}
