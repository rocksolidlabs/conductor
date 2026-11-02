package crew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

func newRecords(t *testing.T) (*Records, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	// Closed before the directory goes (cleanups run last in, first out, and
	// TempDir's was registered first): a record write still in flight ends,
	// and a later one is a no-op, so the directory is empty when removed.
	t.Cleanup(rs.Close)
	return rs, filepath.Join(dir, "runs")
}

// waitRecord waits for the record of runID to be there with the state.
func waitRecord(t *testing.T, rs *Records, runID, state string) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, ok, err := rs.Get(runID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && r.State == state {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("no record of %s as %s (%v %+v)", runID, state, ok, r)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A run that is stopped, and one whose last member's session ends, are
// recorded as GET /api/runs/{run} answers them; a stop of a reopened run
// records it again.
func TestRunRecordIsWrittenWhenARunEnds(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	rs, dir := newRecords(t)
	e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, nil)
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	if _, ok, _ := rs.Get(run.ID); ok {
		t.Fatal("recorded before it ended")
	}
	stopRun(t, e, run.ID)
	rec := waitRecord(t, rs, run.ID, RunStopped)
	if rec.ID != run.ID || rec.CrewID != run.CrewID || len(rec.Members) != 2 || rec.StoppedAt == nil || !logged(rec, "stopped") {
		t.Fatalf("record %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(dir, run.ID+".json")); err != nil {
		t.Fatal(err)
	}

	// Finished: the last member's session ending ends the run.
	run2, err := e.Launch(t.Context(), testCrew(immediate("solo", "Do it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run2.ID, "solo", MemberRunning, 5*time.Second)
	l, p := fl.member("solo")
	p.End(0)
	<-l.Ended()
	fl.change(l.Info())
	rec2 := waitRecord(t, rs, run2.ID, RunFinished)
	if rec2.StoppedAt != nil || rec2.Members[0].Status != MemberEnded {
		t.Fatalf("record %+v", rec2)
	}
	if list, err := rs.List(run.CrewID); err != nil || len(list) != 2 || list[0].ID != run2.ID || list[1].ID != run.ID {
		t.Fatalf("list %v %v", list, err)
	}
	if list, _ := rs.List("other"); len(list) != 0 {
		t.Fatalf("another crew's list %v", list)
	}
}

// Past 500 records the oldest by start time go.
func TestRunRecordsAreBounded(t *testing.T) {
	rs, dir := newRecords(t)
	base := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	for i := range maxRecords + 2 {
		r := Run{ID: fmt.Sprintf("team-%08x", i), CrewID: "team", Name: "team", StartedAt: base.Add(time.Duration(i) * time.Minute), State: RunFinished, Members: []MemberState{}, Log: []session.ActivityEntry{}}
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != maxRecords {
		t.Fatalf("%d records kept", len(entries))
	}
	for _, i := range []int{0, 1} {
		if _, ok, _ := rs.Get(fmt.Sprintf("team-%08x", i)); ok {
			t.Fatalf("record %d, the oldest, kept", i)
		}
	}
	if _, ok, _ := rs.Get("team-00000002"); !ok {
		t.Fatal("record 2 gone")
	}
	list, err := rs.List("")
	if err != nil || len(list) != maxRecords || list[0].ID != fmt.Sprintf("team-%08x", maxRecords+1) {
		t.Fatalf("list %d %v", len(list), err)
	}
	// Bad names are refused; a foreign file is left out.
	if err := rs.Save(Run{ID: "../x"}); err == nil {
		t.Fatal("a bad id was saved")
	}
	for _, id := range []string{"team-1234567", "team-1234567g", "Team-12345678", "-12345678", ""} {
		if ValidRunID(id) {
			t.Errorf("%q passes as a run id", id)
		}
	}
}

// A record holds the run as the API answers it and nothing else: no field
// beyond the Run shape, so never a terminal's output.
func TestRunRecordsNeverHoldTerminalOutput(t *testing.T) {
	rs, dir := newRecords(t)
	r := Run{ID: "team-0badf00d", CrewID: "team", Name: "team", StartedAt: time.Now().UTC(), State: RunStopped, Yolo: true,
		Members: []MemberState{{Name: "lead", AgentID: "claude", SessionID: "s1", Status: MemberEnded}},
		Log:     []session.ActivityEntry{{At: time.Now().UTC(), Type: session.ActivityStatus, Message: "stopped"}}}
	if err := rs.Save(r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "team-0badf00d.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	rt := reflect.TypeOf(Run{})
	for i := range rt.NumField() {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			want[tag] = true
		}
	}
	for k := range doc {
		if !want[k] {
			t.Errorf("record holds %q, which the Run shape does not", k)
		}
	}
	for _, word := range []string{"output", "scrollback", "screen", "\\u001b"} {
		if strings.Contains(string(raw), word) {
			t.Errorf("record holds %q", word)
		}
	}
	got, ok, err := rs.Get("team-0badf00d")
	if err != nil || !ok || got.ID != r.ID || got.Members[0].SessionID != "s1" || len(got.Log) != 1 {
		t.Fatalf("read back %v %v %+v", ok, err, got)
	}
}

// A run being stopped is recorded by its stop: its members' sessions ending
// on the way never record it as finished first (that record, saved on a
// goroutine of its own, could land after the stop's and win).
func TestAStoppedRunIsNeverRecordedAsFinished(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	var mu sync.Mutex
	var ends []string
	e.OnEnd = func(r Run) {
		mu.Lock()
		ends = append(ends, r.ID+" "+r.State)
		mu.Unlock()
	}
	for range 25 {
		run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
		if err != nil {
			t.Fatal(err)
		}
		waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
		waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
		stopRun(t, e, run.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, end := range ends {
		if strings.HasSuffix(end, " "+RunFinished) {
			t.Fatalf("a stopped run was told about as finished: %v", ends)
		}
	}
	if len(ends) < 25 {
		t.Fatalf("%d ends told for 25 stops: %v", len(ends), ends)
	}
}

// The ends of one run are saved in the order they came, whatever order
// their goroutines run in: a late save of an older end is dropped.
func TestAnOlderEndNeverOverwritesANewerRecord(t *testing.T) {
	e, _ := newEngine(t)
	rs, _ := newRecords(t)
	var queued []func()
	e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, func(f func()) { queued = append(queued, f) })
	base := Run{ID: "team-0000000a", CrewID: "team", Name: "team", StartedAt: time.Now().UTC(), Members: []MemberState{}, Log: []session.ActivityEntry{}}
	finished, stopped := base, base
	finished.State = RunFinished
	stopped.State = RunStopped
	e.OnEnd(finished)
	e.OnEnd(stopped)
	// The newer first, then the older.
	queued[1]()
	queued[0]()
	if r, ok, err := rs.Get(base.ID); err != nil || !ok || r.State != RunStopped {
		t.Fatalf("record %+v %v %v", r, ok, err)
	}
	// The run ended again later: that end is saved.
	e.OnEnd(finished)
	queued[2]()
	if r, _, _ := rs.Get(base.ID); r.State != RunFinished {
		t.Fatalf("the later end was dropped: %+v", r)
	}
}

// The records open with the runs still running in them (journaled from
// their launch, never ended: the server stopped while they ran) marked
// interrupted, their running and starting members ended with why; a record
// of a run that ended is left as it is (#153).
func TestARecordOfARunStillRunningIsInterruptedWhenTheRecordsOpen(t *testing.T) {
	rs, runsDir := newRecords(t)
	running := Run{ID: "crew-0123abcd", CrewID: "crew", Name: "Crew", State: RunNeedsInput, NeedsInput: 1, StartedAt: time.Now().UTC(),
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberRunning, NeedsInput: true}, {Name: "b", AgentID: "cat", Status: MemberStarting}, {Name: "c", AgentID: "cat", Status: MemberPending}, {Name: "d", AgentID: "cat", Status: MemberEnded}}}
	if err := rs.Save(running); err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now().UTC()
	stopped := Run{ID: "crew-4567cdef", CrewID: "crew", Name: "Crew", State: RunStopped, StartedAt: stoppedAt.Add(-time.Minute), StoppedAt: &stoppedAt,
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberEnded}}}
	if err := rs.Save(stopped); err != nil {
		t.Fatal(err)
	}
	rs.Close()
	st, err := store.Open(filepath.Dir(runsDir))
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	got, ok, err := again.Get(running.ID)
	if err != nil || !ok {
		t.Fatalf("the running run's record: %v %v", ok, err)
	}
	if got.State != RunInterrupted || got.NeedsInput != 0 || got.StoppedAt != nil {
		t.Fatalf("not interrupted: %+v", got)
	}
	for _, m := range got.Members {
		switch m.Name {
		case "a", "b":
			if m.Status != MemberEnded || m.NeedsInput || !strings.Contains(m.Err, "interrupted") {
				t.Fatalf("member %s: %+v", m.Name, m)
			}
		case "c":
			if m.Status != MemberPending {
				t.Fatalf("member c: %+v", m)
			}
		case "d":
			if m.Status != MemberEnded || m.Err != "" {
				t.Fatalf("member d: %+v", m)
			}
		}
	}
	if got, ok, _ := again.Get(stopped.ID); !ok || got.State != RunStopped || got.StoppedAt == nil {
		t.Fatalf("the stopped run's record changed: %+v", got)
	}
}

// The records open with a source not yet marked by its continuation (the
// server ended between the continuation's record and the source's mark)
// repaired: the source names it, so it is not resumed twice (#153).
func TestARecordsOpenMarksASourceWithItsContinuation(t *testing.T) {
	rs, runsDir := newRecords(t)
	at := time.Now().UTC()
	source := Run{ID: "crew-0000aaaa", CrewID: "crew", Name: "Crew", State: RunStopped, StartedAt: at.Add(-time.Hour), StoppedAt: &at,
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberEnded}}}
	next := Run{ID: "crew-0000bbbb", CrewID: "crew", Name: "Crew", State: RunStopped, StartedAt: at, StoppedAt: &at, ResumedFrom: source.ID,
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberEnded}}}
	for _, r := range []Run{source, next} {
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	rs.Close()
	st, err := store.Open(filepath.Dir(runsDir))
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	if got, ok, _ := again.Get(source.ID); !ok || got.ResumedBy != next.ID {
		t.Fatalf("the source after the open: %v %+v", ok, got)
	}
}

// The records open with a source naming a continuation no record holds
// (the server ended between the source's mark and the continuation's
// record) cleared of the mark, so that it can be resumed (#153).
func TestARecordsOpenClearsAMarkNamingNoRecord(t *testing.T) {
	rs, runsDir := newRecords(t)
	at := time.Now().UTC()
	source := Run{ID: "crew-0000cccc", CrewID: "crew", Name: "Crew", State: RunStopped, StartedAt: at.Add(-time.Hour), StoppedAt: &at, ResumedBy: "crew-0000dddd",
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberEnded}}}
	if err := rs.Save(source); err != nil {
		t.Fatal(err)
	}
	rs.Close()
	st, err := store.Open(filepath.Dir(runsDir))
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	if got, ok, _ := again.Get(source.ID); !ok || got.ResumedBy != "" {
		t.Fatalf("the source after the open: %v %q", ok, got.ResumedBy)
	}
}

// A record the engine still holds is never pruned, nor counted among the
// 500: a run's record follows it from its launch on, so a long run's would
// otherwise be the oldest and go, written again at the run's next change,
// and a server ending in between would have no record of it (#335). The
// saves run inline here, so the one a forget makes (journalForgotten) prunes
// under the engine's lock: the held check must not take that lock.
func TestAHeldRunsRecordSurvivesThePrune(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	rs, dir := newRecords(t)
	e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, func(f func()) { f() })
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	// Its record as the journal writes it while it runs, older than every
	// other, then 501 records of runs that ended.
	base := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	held := Run{ID: run.ID, CrewID: run.CrewID, Name: run.Name, StartedAt: base.Add(-time.Hour), State: RunRunning, Members: []MemberState{}, Log: []session.ActivityEntry{}}
	if err := rs.Save(held); err != nil {
		t.Fatal(err)
	}
	for i := range maxRecords + 1 {
		r := Run{ID: fmt.Sprintf("team-%08x", i), CrewID: "team", Name: "team", StartedAt: base.Add(time.Duration(i) * time.Minute), State: RunFinished, Members: []MemberState{}, Log: []session.ActivityEntry{}}
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != maxRecords+1 {
		t.Fatalf("%d records kept, not the held run's and %d", len(entries), maxRecords)
	}
	if _, ok, _ := rs.Get(run.ID); !ok {
		t.Fatal("the held run's record, the oldest, was pruned")
	}
	if _, ok, _ := rs.Get("team-00000000"); ok {
		t.Fatal("the oldest ended run's record was kept")
	}
	if _, ok, _ := rs.Get("team-00000001"); !ok {
		t.Fatal("the second oldest ended run's record was pruned")
	}

	// Forgotten (its record written once more, inline, under the engine's
	// lock), the run's record counts: saved again as the oldest, it goes.
	stopRun(t, e, run.ID)
	e.ForgetForTest(run.ID)
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("still held")
	}
	if err := rs.Save(held); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := rs.Get(run.ID); ok {
		t.Fatal("the forgotten run's record, the oldest, was kept")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != maxRecords {
		t.Fatalf("%d records kept", len(entries))
	}
}

// A managed run's record is the ledger's cache: never counted among the
// 500 nor pruned by the records, never marked interrupted when the records
// open (the ledger concludes what became of it), never written back to an
// older revision, and deleted only when asked (Delete).
func TestAManagedRecordIsTheLedgersToKeep(t *testing.T) {
	rs, dir := newRecords(t)
	base := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	managed := Run{ID: "team-0000aaaa", CrewID: "team", Name: "team", StartedAt: base.Add(-time.Hour), State: RunRunning, Members: []MemberState{}, Log: []session.ActivityEntry{}, Managed: &Managed{Revision: 7}}
	if err := rs.Save(managed); err != nil {
		t.Fatal(err)
	}
	for i := range maxRecords + 1 {
		r := Run{ID: fmt.Sprintf("team-%08x", i), CrewID: "team", Name: "team", StartedAt: base.Add(time.Duration(i) * time.Minute), State: RunFinished, Members: []MemberState{}, Log: []session.ActivityEntry{}}
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != maxRecords+1 {
		t.Fatalf("%d records kept, not the managed run's and %d", len(entries), maxRecords)
	}
	if _, ok, _ := rs.Get("team-0000aaaa"); !ok {
		t.Fatal("the managed run's record, the oldest, was pruned")
	}
	if _, ok, _ := rs.Get("team-00000000"); ok {
		t.Fatal("the oldest ended run's record was kept")
	}
	// An older revision never lands over a newer one; a newer one does.
	older := managed
	older.Managed = &Managed{Revision: 6}
	older.Name = "older"
	if err := rs.Save(older); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := rs.Get("team-0000aaaa"); r.Managed.Revision != 7 || r.Name != "team" {
		t.Fatalf("an older revision landed: %+v", r.Managed)
	}
	newer := managed
	newer.Managed = &Managed{Revision: 8}
	newer.Name = "newer"
	if err := rs.Save(newer); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := rs.Get("team-0000aaaa"); r.Managed.Revision != 8 || r.Name != "newer" {
		t.Fatalf("a newer revision did not land: %+v", r.Managed)
	}
	// A legacy snapshot of the same run (a journal save queued before the
	// run was known managed) never lands over it either.
	unversioned := managed
	unversioned.Managed = nil
	unversioned.Name = "legacy"
	if err := rs.Save(unversioned); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := rs.Get("team-0000aaaa"); r.Managed == nil || r.Managed.Revision != 8 || r.Name != "newer" {
		t.Fatalf("an unversioned snapshot landed: %+v", r)
	}
	// A record is written to fit the listing's bound: a snapshot with a
	// chat and a log past it is trimmed, newest kept, and stays a managed
	// record an older or unversioned snapshot cannot replace.
	big := managed
	big.Managed = &Managed{Revision: 9}
	big.Chat = []proto.ChatMessage{{ID: "old", Text: strings.Repeat("x", 3<<20)}, {ID: "new", Text: "kept"}}
	big.Log = []session.ActivityEntry{{Type: session.ActivityStatus, Message: strings.Repeat("y", 5<<20)}}
	if err := rs.Save(big); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "team-0000aaaa.json")); err != nil || fi.Size() > maxRecordSize {
		t.Fatalf("the record was not trimmed to the bound: %v %v", fi, err)
	}
	if r, _, err := rs.Get("team-0000aaaa"); err != nil || r.Managed == nil || r.Managed.Revision != 9 || len(r.Chat) != 1 || r.Chat[0].ID != "new" || len(r.Log) != 0 {
		t.Fatalf("the trimmed record: %v %+v", err, r)
	}
	if err := rs.Save(unversioned); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := rs.Get("team-0000aaaa"); r.Managed == nil || r.Managed.Revision != 9 {
		t.Fatalf("an unversioned snapshot replaced the trimmed record: %+v", r)
	}
	// A record over the bound on disk (an older Conductor's) is neither
	// replaced by an older or unversioned snapshot nor pruned: Save reads
	// it with a wider bound for its revision, and the prune keeps what it
	// cannot read for size.
	huge := managed
	huge.Managed = &Managed{Revision: 10}
	huge.Log = []session.ActivityEntry{{Type: session.ActivityStatus, Message: strings.Repeat("z", 5<<20)}}
	b, err := store.Encode(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team-0000aaaa.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(unversioned); err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(managed); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "team-0000aaaa.json")); err != nil || fi.Size() < 5<<20 {
		t.Fatalf("the oversized managed record was written over or pruned: %v %v", fi, err)
	}
	// One that cannot be read at all is not written over either.
	if err := os.WriteFile(filepath.Join(dir, "team-0000aaaa.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(unversioned); err == nil {
		t.Fatal("a record that cannot be read was written over")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "team-0000aaaa.json")); string(b) != "{not json" {
		t.Fatalf("the unreadable record changed: %q", b)
	}
	if err := os.Remove(filepath.Join(dir, "team-0000aaaa.json")); err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(newer); err != nil {
		t.Fatal(err)
	}
	// The records open: the managed run, still running, is left to the
	// ledger; a legacy run still running is interrupted.
	legacy := Run{ID: "team-0000bbbb", CrewID: "team", Name: "team", StartedAt: base.Add(24 * time.Hour), State: RunRunning, Members: []MemberState{{Name: "lead", Status: MemberRunning}}, Log: []session.ActivityEntry{}}
	if err := rs.Save(legacy); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	if r, _, _ := again.Get("team-0000aaaa"); r.State != RunRunning || r.Managed == nil || r.Managed.Revision != 8 {
		t.Fatalf("the managed run was touched: %+v", r)
	}
	if r, _, _ := again.Get("team-0000bbbb"); r.State != RunInterrupted {
		t.Fatalf("the legacy run was not interrupted: %+v", r)
	}
	// Deleted when asked, and nothing when gone.
	if err := again.Delete("team-0000aaaa"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := again.Get("team-0000aaaa"); ok {
		t.Fatal("still there after Delete")
	}
	if err := again.Delete("team-0000aaaa"); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
	if err := again.Delete("../x"); err == nil {
		t.Fatal("a bad id was deleted")
	}
}

// The records' marks are every record's place in its chain, so a run
// whose record is gone is found through the record naming it; a file
// that is no run's record is none, and a record that cannot be read is
// the error, naming it, since what it names is unknown.
func TestRecordMarksNameEveryRecordsChain(t *testing.T) {
	rs, runsDir := newRecords(t)
	at := time.Now().UTC()
	next := Run{ID: "crew-0000eeee", CrewID: "crew", Name: "Crew", State: RunStopped, StartedAt: at, StoppedAt: &at, ResumedFrom: "crew-0000ffff",
		Members: []MemberState{{Name: "a", AgentID: "cat", Status: MemberEnded}}}
	if err := rs.Save(next); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "notes.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	marks, err := rs.Marks()
	if err != nil || len(marks) != 1 || marks[0].ID != next.ID || marks[0].ResumedFrom != "crew-0000ffff" || marks[0].ResumedBy != "" || !marks[0].StartedAt.Equal(at) {
		t.Fatalf("the marks: %+v %v", marks, err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "crew-00001111.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if marks, err := rs.Marks(); err == nil || !strings.Contains(err.Error(), "crew-00001111.json") {
		t.Fatalf("the marks with a record unreadable: %+v %v", marks, err)
	}
}
