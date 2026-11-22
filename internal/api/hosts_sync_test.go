package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The host summary handed to a sync store (docs/plans/hosts.md, PR-5,
// I9): marked by the event hub's sends, coalesced and checked on the
// source's own clock, which each test drives; delivered as the model is
// at delivery, its Rev the summary's sequence in the epoch.

// hostsClock is the host summary's clock: each wait it asks for is handed
// to the test by its length, which fires it.
type hostsClock struct {
	coalesce, check chan chan time.Time
	gates           chan bool
}

func newHostsClock(t *testing.T, h *hostsSync) *hostsClock {
	t.Helper()
	c := &hostsClock{coalesce: make(chan chan time.Time, 64), check: make(chan chan time.Time, 64), gates: make(chan bool, 64)}
	h.mu.Lock()
	h.after = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		if d == hostsCoalesce {
			c.coalesce <- ch
		} else {
			c.check <- ch
		}
		return ch
	}
	h.checked = func(marked bool) { c.gates <- marked }
	h.mu.Unlock()
	return c
}

// fire fires the next wait of which (c.coalesce or c.check) and answers
// whether the gate it led to marked the summary.
func (c *hostsClock) fire(t *testing.T, which chan chan time.Time, what string) bool {
	t.Helper()
	select {
	case ch := <-which:
		ch <- time.Now()
	case <-time.After(testWait):
		t.Fatalf("the host summary asked for no %s wait", what)
	}
	select {
	case marked := <-c.gates:
		return marked
	case <-time.After(testWait):
		t.Fatalf("the host summary's %s led to no gate", what)
	}
	return false
}

// hostsEnv is a server over a new data directory whose sync store f is a
// Starter, the host summary's clock the test's, its delivery begun.
func hostsEnv(t *testing.T, f *fakeSync) (*testEnv, *hostsClock) {
	t.Helper()
	e := syncEnv(t, syncDataDir(t), syncStarter{f})
	if e.srv.hostsSync == nil {
		t.Fatal("no host summary source with a sync store registered")
	}
	c := newHostsClock(t, e.srv.hostsSync)
	e.srv.StartExtensions(t.Context())
	return e, c
}

// summaryCalls are the calls of hosts/summary.
func summaryCalls(calls []syncCall) []syncCall {
	return callsOf(calls, syncKey{syncHosts, syncHostsPath})
}

// hostSummaryOf decodes a call's summary strictly.
func hostSummaryOf(t *testing.T, c syncCall) ext.HostSummary {
	t.Helper()
	sum, err := ext.ParseHostSummary(c.d.Data)
	if err != nil {
		t.Fatalf("the summary delivered %s: %v", c.d.Data, err)
	}
	return sum
}

// ackedCalls are the calls the store acknowledged.
func ackedCalls(calls []syncCall) []syncCall {
	var out []syncCall
	for _, c := range calls {
		if c.err == nil {
			out = append(out, c)
		}
	}
	return out
}

// untilSummaries waits for n acknowledged summaries and answers them.
func untilSummaries(t *testing.T, f *fakeSync, n int) []syncCall {
	t.Helper()
	return ackedCalls(summaryCalls(f.until(t, "summary "+string(rune('0'+n)), func(cs []syncCall) bool { return len(ackedCalls(summaryCalls(cs))) >= n })))
}

// I9: a server session in no run, with no host connected: its question,
// its answer and its removal each deliver a summary whose counts are the
// model's, once the coalescing fires; an unchanged model delivers none.
func TestSyncDeliversHostSummary(t *testing.T) {
	f := newFakeSync()
	e, clk := hostsEnv(t, f)
	first := untilSummaries(t, f, 1)
	if first[0].d.Rev != 1 || first[0].op != "saved" {
		t.Fatalf("the replay's summary: %s", describeCalls(first))
	}
	fs := newFakeSession(t, e, sessionHex("standalone"), session.Options{})
	fs.ask(t, "Which database?", dbChoices)
	if !clk.fire(t, clk.coalesce, "coalescing") {
		t.Fatal("a question did not mark the summary")
	}
	got := untilSummaries(t, f, 2)
	if s := hostSummaryOf(t, got[1]); got[1].d.Rev != 2 || s.Sessions != (ext.HostSessions{Running: 1, Waiting: 1}) {
		t.Fatalf("after the question: rev %d %+v", got[1].d.Rev, s)
	}
	fs.report(t, session.AttentionWorking, "")
	if !clk.fire(t, clk.coalesce, "coalescing") {
		t.Fatal("an answer did not mark the summary")
	}
	got = untilSummaries(t, f, 3)
	if s := hostSummaryOf(t, got[2]); got[2].d.Rev != 3 || s.Sessions != (ext.HostSessions{Running: 1}) {
		t.Fatalf("after the answer: rev %d %+v", got[2].d.Rev, s)
	}
	e.srv.registry.Remove(sessionHex("standalone"))
	if !clk.fire(t, clk.coalesce, "coalescing") {
		t.Fatal("a removal did not mark the summary")
	}
	got = untilSummaries(t, f, 4)
	if s := hostSummaryOf(t, got[3]); got[3].d.Rev != 4 || s.Sessions != (ext.HostSessions{}) {
		t.Fatalf("after the removal: rev %d %+v", got[3].d.Rev, s)
	}
	// Marked with nothing changed: the gate marks nothing.
	e.srv.hostsSync.changed()
	if clk.fire(t, clk.coalesce, "coalescing") {
		t.Fatal("an unchanged model marked the summary")
	}
	if n := len(ackedCalls(summaryCalls(f.all()))); n != 4 {
		t.Fatalf("%d summaries after an unchanged model", n)
	}
}

// I9: within an epoch the summary's Rev rises with its bytes, a retry of
// the same bytes keeps its Rev, one retried after the model changed
// carries the new bytes under the next; a replay starts again at 1. The
// store, dropping a Rev not newer, holds the model as it last was.
func TestSyncHostSummaryRevisions(t *testing.T) {
	f := newFakeSync()
	e, clk := hostsEnv(t, f)
	backoff := newSyncClock(e.srv.syncs)
	untilSummaries(t, f, 1)
	fs := newFakeSession(t, e, sessionHex("revs"), session.Options{})
	held := func(want ext.HostSessions) {
		t.Helper()
		b := syncModel(f.all())[syncKey{syncHosts, syncHostsPath}]
		var s ext.HostSummary
		if err := json.Unmarshal(b, &s); err != nil || s.Sessions != want {
			t.Fatalf("the store holds %s, want sessions %+v", b, want)
		}
	}
	// A change, delivered: Rev 2.
	fs.ask(t, "One?", dbChoices)
	clk.fire(t, clk.coalesce, "coalescing")
	if got := untilSummaries(t, f, 2); got[1].d.Rev != 2 {
		t.Fatalf("rev %d", got[1].d.Rev)
	}
	held(ext.HostSessions{Running: 1, Waiting: 1})
	// A change whose delivery fails once: retried with the same bytes and
	// the same Rev.
	var mu sync.Mutex
	failing := 1
	f.set(func(f *fakeSync) {
		f.fail = func(op string, d ext.Doc) error {
			mu.Lock()
			defer mu.Unlock()
			if d.Store == syncHosts && failing > 0 {
				failing--
				return errors.New("the service is away")
			}
			return nil
		}
	})
	fs.report(t, session.AttentionWorking, "")
	clk.fire(t, clk.coalesce, "coalescing")
	f.until(t, "a failed summary", func(cs []syncCall) bool { return len(summaryCalls(cs)) > len(ackedCalls(summaryCalls(cs))) })
	backoff.next(t, syncRetryFirst).fire <- time.Now()
	got := untilSummaries(t, f, 3)
	all := summaryCalls(f.all())
	failed := all[len(all)-2]
	if failed.err == nil || failed.d.Rev != 3 || got[2].d.Rev != 3 || !bytes.Equal(failed.d.Data, got[2].d.Data) {
		t.Fatalf("a retry of the same bytes: %s", describeCalls(all))
	}
	held(ext.HostSessions{Running: 1})
	// A change whose delivery fails, the model changed before the retry:
	// the retry carries the new bytes under the next Rev.
	mu.Lock()
	failing = 1
	mu.Unlock()
	fs.ask(t, "Two?", dbChoices)
	clk.fire(t, clk.coalesce, "coalescing")
	f.until(t, "a second failed summary", func(cs []syncCall) bool {
		return len(summaryCalls(cs)) > len(ackedCalls(summaryCalls(cs)))+1
	})
	second := newFakeSession(t, e, sessionHex("revs-2"), session.Options{})
	second.report(t, session.AttentionWorking, "")
	if !clk.fire(t, clk.coalesce, "coalescing") {
		t.Fatal("the change before the retry did not mark the summary")
	}
	backoff.next(t, syncRetryFirst*2).fire <- time.Now()
	got = untilSummaries(t, f, 4)
	if last := got[3]; last.d.Rev != 5 || hostSummaryOf(t, last).Sessions != (ext.HostSessions{Running: 2, Waiting: 1}) {
		t.Fatalf("a retry after the model changed: %s", describeCalls(summaryCalls(f.all())))
	}
	held(ext.HostSessions{Running: 2, Waiting: 1})
	// A replay: a new epoch, from 1.
	e.srv.syncs.requestReplay()
	calls := f.until(t, "the second replay", func(cs []syncCall) bool { return replayedIn(cs, 2) })
	epoch := nthEpoch(calls, 2)
	var inEpoch []syncCall
	for _, c := range ackedCalls(summaryCalls(calls)) {
		if c.d.Epoch == epoch {
			inEpoch = append(inEpoch, c)
		}
	}
	if len(inEpoch) != 1 || inEpoch[0].d.Rev != 1 {
		t.Fatalf("the new epoch's summary: %s", describeCalls(inEpoch))
	}
	held(ext.HostSessions{Running: 2, Waiting: 1})
}

// Every replay lists hosts/summary after the run pointers and before
// Dispatch's log; a new epoch delivers it again though unchanged, as Rev 1.
func TestSyncReplayListsHostSummary(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), syncStarter{f})
	newLedgerRun(t, e.srv.ledger, "team-00000001", ledger.RunRunning)
	e.srv.StartExtensions(t.Context())
	for n := 1; n <= 2; n++ {
		if n == 2 {
			e.srv.syncs.requestReplay()
		}
		calls := f.until(t, "the replay", func(cs []syncCall) bool { return replayedIn(cs, n) })
		keys := keysOf(calls, nthEpoch(calls, n))
		at := map[string]int{}
		for i, k := range keys {
			at[k.store+"/"+k.path] = i
		}
		hosts, ok := at[syncHosts+"/"+syncHostsPath]
		if !ok || hosts < at[syncLedger+"/team-00000001"] || hosts > at[syncDispatch+"/"+syncLogPath] {
			t.Fatalf("replay %d lists %v", n, keys)
		}
		for _, c := range summaryCalls(calls) {
			if c.d.Epoch == nthEpoch(calls, n) && c.d.Rev != 1 {
				t.Fatalf("replay %d's summary: rev %d", n, c.d.Rev)
			}
		}
	}
}

// A change no event tells (a recently seen machine passing its 24 hours)
// is delivered at the next 5-minute check; an unchanged model is not.
func TestSyncHostSummaryPeriodicCheck(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), syncStarter{f})
	clk := newHostsClock(t, e.srv.hostsSync)
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	e.srv.seen.setNow(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	e.srv.seen.removed(session.Info{ID: "s1", Kind: session.KindHosted, HostName: "studio", HostKey: strings.Repeat("a", 32), HostKeyFrom: session.HostKeyMachine},
		"", func() []session.Info { return nil })
	e.srv.StartExtensions(t.Context())
	first := untilSummaries(t, f, 1)
	if s := hostSummaryOf(t, first[0]); len(s.Hosts) != 1 || s.Hosts[0].Name != "studio" || s.Hosts[0].Availability != ext.HostSeen {
		t.Fatalf("the seen machine: %+v", s)
	}
	mu.Lock()
	now = now.Add(hostsSeenFor)
	mu.Unlock()
	if !clk.fire(t, clk.check, "check") {
		t.Fatal("the check did not mark the summary")
	}
	if got := untilSummaries(t, f, 2); len(hostSummaryOf(t, got[1]).Hosts) != 0 || got[1].d.Rev != 2 {
		t.Fatalf("after 24 hours: %s", describeCalls(got))
	}
	if clk.fire(t, clk.check, "check") {
		t.Fatal("an unchanged model marked the summary")
	}
}

// A switchyard's hosts are other people's servers: no host summary, in a
// delivery or a replay, whatever sync store is registered.
func TestSyncNoHostSummaryOnASwitchyard(t *testing.T) {
	f := newFakeSync()
	providers := local.Fill(ext.Providers{Continuity: f, All: []ext.Entry{{Kind: ext.KindSync, Name: f.Name(), Value: f}}})
	e := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = true }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithExtensions(providers))
	if e.srv.hostsSync != nil {
		t.Fatal("a switchyard registered the host summary")
	}
	calls := f.until(t, "the replay", func(cs []syncCall) bool { return replayedIn(cs, 1) })
	if got := summaryCalls(calls); len(got) != 0 {
		t.Fatalf("a switchyard's summary: %s", describeCalls(got))
	}
}

// I5, I6: the summary holds counts, platforms, versions and names, never a
// session's id, its folder or branch, a run's name, an OS user or a
// machine key.
func TestHostSummaryLeavesOutTheLocal(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	b, ok := f.srv.hostSummaryBytes(context.Background())
	if !ok {
		t.Fatal("no summary")
	}
	sum, err := ext.ParseHostSummary(b)
	if err != nil || sum.Runs.Held != 1 || sum.Sessions.Running < 1 {
		t.Fatalf("the summary %s: %v", b, err)
	}
	d, _ := f.srv.registry.Get(f.sid)
	info := d.Info()
	words := []string{f.sid, info.Cwd, f.root, "Routes", f.run}
	if info.Branch != "" {
		words = append(words, info.Branch)
	}
	if k := f.srv.OwnHostKey(); k != "" {
		words = append(words, k)
	}
	for _, w := range words {
		if bytes.Contains(b, []byte(w)) {
			t.Fatalf("the summary holds %q: %s", w, b)
		}
	}
}
