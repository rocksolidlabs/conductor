package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/capacity"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// A capacity reading that warns wakes Brakeman's background role
// (docs/plans/capacity.md, PR-8): the wake names the agent and the account
// by their ids alone, is admitted by the Capacity class alone, and a
// reading of the key Brakeman's own turns launch on wakes nothing. No
// agent's tool runs: the readers are the capacity tests' fakes, the
// readings' clock the test's own, the role's clock the desk's, each moved
// by the test.

// warnAt sets the fake's one window to severity at percent used.
func warnAt(f *fakeUsage, severity string, percent int) {
	f.set(func(f *fakeUsage) {
		w := f.report.Windows[0]
		w.Severity, w.Percent = severity, percent
		f.report.Windows = []agents.UsageWindow{w}
	})
}

// readWarned reads agent on account once the readings' clock has passed
// the key's MinInterval, and waits for the reading's capacity event: the
// desk's capacityRead, made before the event, has noted it by then.
func (ct *capTurn) readWarned(t *testing.T, events <-chan string, agent, account string) capacity.View {
	t.Helper()
	ct.clock.add(capacity.MinInterval)
	f := ct.claude
	if a, ok := ct.srv.Catalog().Get(agent); ok && a.Adapter == "codex" {
		f = ct.codex
	}
	readAndAwait(t, ct.testEnv, f, events, agent, account)
	v, state := ct.srv.capacity.cache.Latest(capacity.Key{Agent: agent, Account: account})
	if state == "" || v.ReadAt == nil {
		t.Fatalf("no reading of %s on %s", agent, account)
	}
	return v
}

// capacityRows is the role's log's rows of the capacity source.
func (ct *capTurn) capacityRows(t *testing.T) []crew.RoleLogRow {
	t.Helper()
	var out []crew.RoleLogRow
	for _, row := range ct.roleRecord(t).Log {
		if row.Source == crew.WakeCapacity {
			out = append(out, row)
		}
	}
	return out
}

// awaitCapacityRows waits for the role's log to hold n rows of the
// capacity source.
func (ct *capTurn) awaitCapacityRows(t *testing.T, n int) []crew.RoleLogRow {
	t.Helper()
	var rows []crew.RoleLogRow
	if !waitFor(30*time.Second, func() bool {
		ct.desk.role.Poke()
		rows = ct.capacityRows(t)
		return len(rows) >= n
	}) {
		t.Fatalf("%d capacity wakes in the log, want %d: %+v", len(rows), n, ct.roleRecord(t).Log)
	}
	return rows
}

// noCapacityWake fails t when the role, once its loop has passed over
// what was noted, logs a capacity wake past the n it had.
func (ct *capTurn) noCapacityWake(t *testing.T, n int, what string) {
	t.Helper()
	ct.roleSettle(t)
	if rows := ct.capacityRows(t); len(rows) != n {
		t.Fatalf("%s woke the role: %+v", what, rows)
	}
}

// TestCapacityWarningWakesRole: a reading at the provider's warning fires
// one wake, its subject the key by its ids and its key the reading's time;
// a second reading of the key inside the batch window (at 90% used, its
// severity normal) coalesces with it, counted two; a reading at normal
// fires none; and the turn the batch fires reads the wake as its ids
// alone, no label, folder or figure, and not at all once the Capacity
// class is off.
func TestCapacityWarningWakesRole(t *testing.T) {
	ct := newCapTurn(t)
	events := ct.sse(t)
	c := ct.background(t, "bk", nil)

	ct.readWarned(t, events, "claude-a", ct.account)
	ct.noCapacityWake(t, 0, "a reading at normal")

	warnAt(ct.claude, "warning", 60)
	first := ct.readWarned(t, events, "claude-a", ct.account)
	rows := ct.awaitCapacityRows(t, 1)
	subject := "capacity:claude-a/" + ct.account
	if r := rows[0]; r.Subject != subject || r.Key != first.ReadAt.UTC().Format(time.RFC3339Nano) || r.Batch == "" {
		t.Fatalf("the wake: %+v", r)
	}
	ct.noCapacityWake(t, 1, "the same reading again")

	warnAt(ct.claude, "normal", 90)
	ct.readWarned(t, events, "claude-a", ct.account)
	rows = ct.awaitCapacityRows(t, 2)
	r := ct.roleRecord(t)
	if r.Open == nil || len(r.Open.Wakes) != 1 || r.Open.Wakes[0].Subject != subject || r.Open.Wakes[0].Count != 2 || rows[1].Batch != r.Open.ID {
		t.Fatalf("the second reading did not coalesce: %+v", r.Open)
	}

	// The batch fires: its turn reads the wake by its ids alone.
	batch := r.Open.ID
	c.add(time.Duration(brakeman.MinWindowSeconds) * time.Second)
	var id string
	if !waitFor(30*time.Second, func() bool {
		ct.desk.role.Poke()
		for _, row := range ct.roleRecord(t).Log {
			if row.Batch == batch && row.Request != "" {
				id = row.Request
			}
		}
		return id != ""
	}) {
		t.Fatalf("the batch %s never fired: %+v", batch, ct.roleRecord(t))
	}
	sid, tok, _ := ct.turnSession(t, id)
	ct.waitState(t, id, brakeman.StateRunning)
	read := func() (string, []byte) {
		t.Helper()
		status, data, out := ct.callAs(t, tok, "GET", "/api/sessions/"+sid+"/brakeman/request", nil)
		b, _ := entityOf(t, out, "request")["batch"].(map[string]any)
		if status != http.StatusOK || b == nil {
			t.Fatalf("%d %s", status, data)
		}
		w, _ := json.Marshal(b["wakes"])
		return string(w), data
	}
	got, data := read()
	if !strings.Contains(got, `"subject":"`+subject+`"`) || !strings.Contains(got, `"source":"capacity"`) || !strings.Contains(got, `"count":2`) {
		t.Fatalf("the turn's batch: %s", got)
	}
	ct.leaks(t, "the turn's request", data)
	for _, figure := range []string{"Work", "max", "Weekly", `"percent"`, "warning"} {
		if strings.Contains(got, figure) {
			t.Fatalf("the turn's batch holds %q: %s", figure, got)
		}
	}
	ct.configure(t, capacityOff)
	if got, _ := read(); strings.Contains(got, "capacity") {
		t.Fatalf("the turn's batch with Capacity not visible: %s", got)
	}
}

// TestBrakemansOwnReadingWakesNothing: a warning reading of the agent and
// account Brakeman's own turns launch on wakes nothing, and one of another
// key wakes the role: with no account configured, its agent's own login;
// with an account's folder configured, that account.
func TestBrakemansOwnReadingWakesNothing(t *testing.T) {
	ct := newCapTurn(t)
	events := ct.sse(t)
	ct.background(t, "claude-a", nil)
	warnAt(ct.claude, "critical", 97)

	ct.readWarned(t, events, "claude-a", capacity.DefaultID)
	ct.noCapacityWake(t, 0, "a reading of Brakeman's own agent on its own login")
	ct.readWarned(t, events, "claude-a", ct.account)
	if rows := ct.awaitCapacityRows(t, 1); rows[0].Subject != "capacity:claude-a/"+ct.account {
		t.Fatalf("the wake: %+v", rows)
	}

	ct.configure(t, func(c map[string]any) { c["account"] = ct.folder })
	ct.readWarned(t, events, "claude-a", ct.account)
	ct.noCapacityWake(t, 1, "a reading of Brakeman's own agent on the account its folder is")
	ct.readWarned(t, events, "claude-a", capacity.DefaultID)
	if rows := ct.awaitCapacityRows(t, 2); rows[1].Subject != "capacity:claude-a/"+capacity.DefaultID {
		t.Fatalf("the wake: %+v", rows)
	}
}

// TestACapacityWarningWakesOnlyWhereCapacityIsSeen: a warning reading
// wakes nothing while the configuration's Capacity class is off, nor while
// the role is not in the background; under a scope narrowed to Brakeman's
// own crew it wakes the role by its class alone.
func TestACapacityWarningWakesOnlyWhereCapacityIsSeen(t *testing.T) {
	ct := newCapTurn(t)
	events := ct.sse(t)
	ct.background(t, "bk", capacityOff)
	warnAt(ct.claude, "warning", 50)
	ct.readWarned(t, events, "claude-a", ct.account)
	ct.noCapacityWake(t, 0, "a reading with Capacity not visible")

	ct.configure(t, func(c map[string]any) {
		c["visibility"].(map[string]any)["capacity"] = true
		c["wake"].(map[string]any)["mode"] = brakeman.WakeInvoked
	})
	ct.readWarned(t, events, "claude-a", ct.account)
	ct.noCapacityWake(t, 0, "a reading with the role not in the background")

	ct.configure(t, func(c map[string]any) {
		c["wake"].(map[string]any)["mode"] = brakeman.WakeBackground
		c["scope"] = map[string]any{"crews": []any{brakemanMember}}
	})
	ct.roleSettle(t)
	ct.readWarned(t, events, "claude-a", ct.account)
	if rows := ct.awaitCapacityRows(t, 1); rows[0].Subject != "capacity:claude-a/"+ct.account {
		t.Fatalf("the wake: %+v", rows)
	}
}

// TestACapacitySubjectIsAdmittedByItsClassAlone: under a scope that
// narrows, a capacity wake's subject is admitted by the Capacity class
// alone and refused without it, and no other kind of subject is admitted
// so; a batch's overflow with no tally by source (kept by an older
// server), which names no subject, is read only by a grant that sees
// every class a wake may be of, Capacity among them.
func TestACapacitySubjectIsAdmittedByItsClassAlone(t *testing.T) {
	b := brakemanEnv(t, nil)
	all := brakeman.Visibility{Runs: true, Dispatch: true, Sessions: true, Capacity: true}
	narrow := brakeman.Scope{Crews: []string{brakemanMember}}
	resolver := func(vis brakeman.Visibility, sc brakeman.Scope) *refResolver {
		return b.srv.wakeResolver(b.desk.st, brakeman.Config{Visibility: vis, Scope: sc})
	}
	ctx := context.Background()
	for _, tc := range []struct {
		vis     brakeman.Visibility
		subject string
		want    bool
	}{
		{all, "capacity:claude-a/default", true},
		{all, "capacity:claude-a/a-aaaaaaaaaaaa", true},
		{brakeman.Visibility{Runs: true, Dispatch: true, Sessions: true}, "capacity:claude-a/default", false},
		{all, "run:team-0a1b2c3d", false},
		{all, "task:team-0a1b2c3d/build", false},
		{all, "capacityx:claude-a/default", false},
	} {
		if ok, _ := resolver(tc.vis, narrow).admitsSubject(ctx, tc.subject); ok != tc.want {
			t.Errorf("%s under %+v and a narrowed scope: admitted %v, want %v", tc.subject, tc.vis, ok, tc.want)
		}
	}
	overflowed := crew.Batch{ID: "wb-1", Wakes: []crew.BatchWake{}, Overflow: &crew.Overflow{Count: 3}}
	if got := resolver(all, brakeman.Scope{}).visibleBatch(ctx, overflowed); got.Overflow == nil || got.Overflow.Count != 3 {
		t.Fatalf("the overflow under a grant that sees every class: %+v", got.Overflow)
	}
	if got := resolver(brakeman.Visibility{Runs: true, Dispatch: true, Sessions: true}, brakeman.Scope{}).visibleBatch(ctx, overflowed); got.Overflow != nil {
		t.Fatalf("the overflow read without Capacity: %+v", got.Overflow)
	}
	if got := batchClasses(&overflowed); !slices.Contains(got, brakeman.ClassCapacity) {
		t.Fatalf("an overflow's classes: %v", got)
	}
}

// TestAnOverflowIsReadBySourceTheGrantSees: under a scope that narrows
// nothing, a turn is told of the overflow's wakes of the sources whose
// class its grant sees, counted with their own times, and its footprint
// holds those classes alone; a configuration with Capacity off (every one
// saved before Capacity was a class) still reads the count of its runs',
// sessions' and Dispatch's wakes. An overflow whose tally cannot be
// trusted (none, kept by an older server; one short of its count; a
// source this build does not know) is read whole by a grant that sees
// every class a wake may be of, and by no other.
func TestAnOverflowIsReadBySourceTheGrantSees(t *testing.T) {
	b := brakemanEnv(t, nil)
	ctx := context.Background()
	read := func(vis brakeman.Visibility, sc brakeman.Scope, o crew.Overflow) *crew.Overflow {
		batch := crew.Batch{ID: "wb-1", Wakes: []crew.BatchWake{}, Overflow: &o}
		return b.srv.wakeResolver(b.desk.st, brakeman.Config{Visibility: vis, Scope: sc}).visibleBatch(ctx, batch).Overflow
	}
	t0 := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	part := func(n, first, last int) crew.OverflowPart {
		return crew.OverflowPart{Count: n, First: at(first), Last: at(last)}
	}
	tallied := crew.Overflow{Count: 7, First: at(1), Last: at(9), Sources: map[string]crew.OverflowPart{
		crew.WakeTaskFailed:   part(2, 3, 4),
		crew.WakeQuestionOpen: part(1, 5, 5),
		crew.WakeCapacity:     part(3, 1, 9),
		crew.WakeBriefing:     part(1, 6, 6),
	}}
	all := brakeman.Visibility{Runs: true, Dispatch: true, Sessions: true, Capacity: true}
	beforeCapacity := brakeman.Visibility{Runs: true, Dispatch: true, Sessions: true}

	got := read(beforeCapacity, brakeman.Scope{}, tallied)
	if got == nil || got.Count != 4 || !got.First.Equal(at(3)) || !got.Last.Equal(at(6)) || !got.Tallied() {
		t.Fatalf("Capacity off: %+v, want the runs', sessions' and the briefing's 4 between 3 s and 6 s", got)
	}
	if _, ok := got.Sources[crew.WakeCapacity]; ok {
		t.Fatalf("Capacity off: the capacity wakes' part is told: %+v", got.Sources)
	}
	if classes := batchClasses(&crew.Batch{Overflow: got}); !slices.Equal(classes, []string{brakeman.ClassRuns, brakeman.ClassSessions}) {
		t.Fatalf("Capacity off, the footprint: %v", classes)
	}
	if got := read(all, brakeman.Scope{}, tallied); got == nil || got.Count != 7 || !got.First.Equal(at(1)) || !got.Last.Equal(at(9)) {
		t.Fatalf("every class seen: %+v", got)
	} else if classes := batchClasses(&crew.Batch{Overflow: got}); !slices.Equal(classes, []string{brakeman.ClassRuns, brakeman.ClassSessions, brakeman.ClassCapacity}) {
		t.Fatalf("every class seen, the footprint: %v", classes)
	}
	if got := read(brakeman.Visibility{Runs: true}, brakeman.Scope{}, tallied); got == nil || got.Count != 3 {
		t.Fatalf("runs alone: %+v, want the failures' 2 and the briefing's 1", got)
	}
	runsOnly := crew.Overflow{Count: 2, First: at(3), Last: at(4), Sources: map[string]crew.OverflowPart{crew.WakeTaskFailed: part(2, 3, 4)}}
	if got := read(brakeman.Visibility{Dispatch: true, Capacity: true}, brakeman.Scope{}, runsOnly); got != nil {
		t.Fatalf("no source's class seen: %+v", got)
	}
	if got := read(all, brakeman.Scope{}, runsOnly); got == nil || slices.Contains(batchClasses(&crew.Batch{Overflow: got}), brakeman.ClassCapacity) {
		t.Fatalf("an overflow of runs alone marks Capacity: %+v", got)
	}
	if got := read(all, brakeman.Scope{Crews: []string{brakemanMember}}, tallied); got != nil {
		t.Fatalf("a narrowed scope: %+v", got)
	}

	// No tally to trust: whole under every class, nothing under fewer, and the footprint every class.
	short := tallied
	short.Count = 8
	unknown := crew.Overflow{Count: 1, First: at(1), Last: at(1), Sources: map[string]crew.OverflowPart{"later_source": part(1, 1, 1)}}
	for name, o := range map[string]crew.Overflow{"none": {Count: 3, First: at(1), Last: at(2)}, "short": short, "unknown": unknown} {
		if got := read(beforeCapacity, brakeman.Scope{}, o); got != nil {
			t.Errorf("%s: read with Capacity off: %+v", name, got)
		}
		got := read(all, brakeman.Scope{}, o)
		if got == nil || got.Count != o.Count || got.Sources != nil {
			t.Errorf("%s: under every class: %+v", name, got)
			continue
		}
		if classes := batchClasses(&crew.Batch{Overflow: got}); !slices.Equal(classes, []string{brakeman.ClassRuns, brakeman.ClassDispatch, brakeman.ClassSessions, brakeman.ClassCapacity}) {
			t.Errorf("%s: the footprint: %v", name, classes)
		}
	}
}

// TestCapacityWarns: the readings the panel warns at, and only those,
// wake the role.
func TestCapacityWarns(t *testing.T) {
	yes, no := true, false
	ok := func(windows ...capacity.Window) capacity.View {
		return capacity.View{State: capacity.StateOK, Windows: windows}
	}
	at := func(severity string, percent int) capacity.Window {
		return capacity.Window{Severity: severity, Percent: percent}
	}
	for _, tc := range []struct {
		name string
		v    capacity.View
		want bool
	}{
		{"no windows", ok(), false},
		{"normal at 89%", ok(at("normal", 89)), false},
		{"no severity at 89%", ok(at("", 89)), false},
		{"normal at 90%", ok(at("normal", 90)), true},
		{"warning", ok(at("warning", 10)), true},
		{"critical", ok(at("critical", 10)), true},
		{"one window of two at warning", ok(at("normal", 10), at("warning", 20)), true},
		{"a limit reached", capacity.View{State: capacity.StateOK, Limits: capacity.Limits{Reached: "five_hour"}}, true},
		{"spend control reached", capacity.View{State: capacity.StateOK, Limits: capacity.Limits{SpendControl: &yes}}, true},
		{"ordinary use not allowed", capacity.View{State: capacity.StateOK, Limits: capacity.Limits{OrdinaryAllowed: &no}}, true},
		{"ordinary use allowed, no spend control", capacity.View{State: capacity.StateOK, Limits: capacity.Limits{OrdinaryAllowed: &yes, SpendControl: &no}}, false},
		{"signed out", capacity.View{State: capacity.StateSignedOut, Windows: []capacity.Window{at("critical", 100)}}, false},
		{"an error", capacity.View{State: capacity.StateError, Limits: capacity.Limits{Reached: "x"}}, false},
	} {
		if got := capacityWarns(tc.v); got != tc.want {
			t.Errorf("%s: warns %v, want %v", tc.name, got, tc.want)
		}
	}
}
