package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// GET /api/brakeman/since (brakeman_since.go): Conductor's own counts
// since the last briefing kept. The fixtures write the ledger's runs and
// the run records with the times the test needs, on a desk clock of the
// test's, and make Dispatch's changes through its route.

// since reads GET /api/brakeman/since with query as the admin.
func (b *bkEnv) since(t *testing.T, query string) brakemanSince {
	t.Helper()
	status, data, _ := b.callAs(t, adminToken, "GET", "/api/brakeman/since"+query, nil)
	if status != http.StatusOK {
		t.Fatalf("since%s: %d %.300s", query, status, data)
	}
	if len(data) > maxBrakemanSince+1 {
		t.Fatalf("a reply of %d bytes", len(data))
	}
	var out brakemanSince
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return out
}

// sinceRun writes a managed run to the ledger, closed at closed (zero:
// open), with its tasks' last attempts closed at the times given, each
// task failed.
func (b *bkEnv) sinceRun(t *testing.T, id string, closed time.Time, failed map[string][]time.Time) {
	t.Helper()
	state := ledger.RunRunning
	if !closed.IsZero() {
		state = ledger.RunStopped
	}
	crewJSON, _ := json.Marshal(map[string]string{"id": crewOfRun(id), "cwd": b.root})
	if _, err := b.srv.ledger.Transition(t.Context(), id, func(tx *ledger.Tx) error {
		if err := tx.PutRun(ledger.RunRow{ID: id, CrewID: crewOfRun(id), Crew: string(crewJSON), State: state, CreatedAt: closed.Add(-3 * time.Hour), ClosedAt: closed}); err != nil {
			return err
		}
		for task, ats := range failed {
			if err := tx.PutTask(ledger.TaskRow{ID: task, Member: "lead", Kind: "work", State: ledger.TaskFailed, Attempts: len(ats)}); err != nil {
				return err
			}
			for i, at := range ats {
				if err := tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: task, N: i + 1, Member: "lead", State: ledger.AttemptFailed, ReservedAt: at, ClosedAt: at}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// sinceRecord saves a run record with no ledger, stopped at stopped.
func (b *bkEnv) sinceRecord(t *testing.T, id, class string, stopped time.Time) {
	t.Helper()
	if err := b.srv.records.Save(crew.Run{ID: id, CrewID: crewOfRun(id), Name: crewOfRun(id), Cwd: b.root, State: crew.RunStopped, StartedAt: stopped.Add(-time.Hour), StoppedAt: &stopped, Class: class}); err != nil {
		t.Fatal(err)
	}
}

// sinceTicket makes a ticket of project CND and sets its status.
func (b *bkEnv) sinceTicket(t *testing.T, title, status string) string {
	t.Helper()
	key := doneOf(t)(b.srv.dispatch.CreateTicket(t.Context(), adminOp("Ann"), dispatch.NewTicket{Project: "CND", Title: title})).Key
	b.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": status}}, http.StatusOK, "")
	return key
}

// sinceBriefing keeps a briefed briefing at the desk's clock now.
func (b *bkEnv) sinceBriefing(t *testing.T) brakeman.Request {
	t.Helper()
	id := (&obsFixture{bkEnv: b}).keepEarlier(t, brakeman.DefaultConfig(b.root), []brakeman.Item{fact("it ran", ref(t, "run:early-0000aaa1"))}, "It ran.", nil)
	r, err := b.desk.st.Get(id)
	if err != nil || r.State != brakeman.StateBriefed {
		t.Fatalf("the briefing %s: %v", r.State, err)
	}
	return r
}

// The counts start at the last briefing kept: a run that ended before it
// is not counted and one after is (from the ledger, and a run of no
// ledger from its record); a task counts when it is failed and its last
// attempt closed after it; a ticket made ready after it counts, one made
// ready before it or made in progress does not; a question open now
// counts with when it was asked. Read again within five seconds the same
// counts are answered; after them, the new ones.
func TestSinceCountsFromTheLastBriefing(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	doneOf(t)(b.srv.dispatch.CreateProject(t.Context(), adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	before := b.sinceTicket(t, "Ready before", dispatch.TicketReady)
	c := b.fakeClock()
	brief := b.sinceBriefing(t)
	at := brief.BriefAt
	b.sinceRun(t, "early-0000aaa1", at.Add(-time.Hour), map[string][]time.Time{"build": {at.Add(-time.Hour)}})
	b.sinceRun(t, "ended-0000aaa2", at.Add(time.Minute), map[string][]time.Time{"build": {at.Add(-2 * time.Hour), at.Add(time.Minute)}})
	b.sinceRun(t, "open-0000aaa3", time.Time{}, map[string][]time.Time{"lint": {at.Add(-30 * time.Minute)}, "test": {at.Add(2 * time.Minute)}})
	b.sinceRecord(t, "legacy-0000aaa5", "", at.Add(4*time.Minute))
	b.sinceRecord(t, "legacy-0000aaa6", "", at.Add(-4*time.Minute))
	ready := b.sinceTicket(t, "Ready after", dispatch.TicketReady)
	b.sinceTicket(t, "Started after", dispatch.TicketInProgress)
	sid := b.createSession("cat")
	d, _ := b.srv.registry.Get(sid)
	l := d.(*session.Local)
	l.SetAttentionFull(session.AttentionNeedsInput, "Proceed?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Yes", Input: "y"}})
	asked := l.Info().Attention

	got := b.since(t, "")
	if got.Since.From != sinceFromBriefing || got.Since.Briefing != brief.ID || !got.Since.At.Equal(at) {
		t.Fatalf("the start %+v, the briefing %s at %s", got.Since, brief.ID, at)
	}
	want := func(name string, c sinceCount, refs ...string) {
		t.Helper()
		if c.Count != len(refs) || strings.Join(c.Refs, " ") != strings.Join(refs, " ") {
			t.Errorf("%s: %+v, want %v", name, c, refs)
		}
	}
	want("runs ended", got.RunsEnded, "run:legacy-0000aaa5", "run:ended-0000aaa2")
	want("tasks failed", got.TasksFailed, "task:open-0000aaa3/test", "task:ended-0000aaa2/build")
	want("tickets made ready", got.TicketsReady, "ticket:"+ready)
	want("questions open", got.QuestionsOpen, "question:"+sid+"/"+asked.ID)
	if got.QuestionsOpen.Oldest == nil || !got.QuestionsOpen.Oldest.Equal(*asked.Since) || got.Truncated {
		t.Fatalf("the oldest question %v, asked %v; truncated %v", got.QuestionsOpen.Oldest, asked.Since, got.Truncated)
	}
	if strings.Contains(strings.Join(got.TicketsReady.Refs, " "), before) {
		t.Fatalf("a ticket made ready before the briefing: %v", got.TicketsReady)
	}

	// A run record written with no event told: within five seconds of
	// the desk's clock the same reply is answered, and after them the
	// new count. Once the run's event is told, a read at once, the clock
	// not moved, has the new count (TestSinceIsReadAgainAfterAnEvent).
	b.sinceRecord(t, "late-0000aaa7", "", at.Add(5*time.Minute))
	c.add(sinceCacheFor - time.Millisecond)
	if again := b.since(t, ""); again.RunsEnded.Count != 2 {
		t.Fatalf("within five seconds, nothing told: %+v", again.RunsEnded)
	}
	b.srv.events.run("late-0000aaa7", false)
	want("runs ended, its event told", b.since(t, "").RunsEnded, "run:late-0000aaa7", "run:legacy-0000aaa5", "run:ended-0000aaa2")
	b.sinceRecord(t, "later-0000aaa8", "", at.Add(6*time.Minute))
	c.add(sinceCacheFor)
	want("runs ended, five seconds on", b.since(t, "").RunsEnded, "run:later-0000aaa8", "run:late-0000aaa7", "run:legacy-0000aaa5", "run:ended-0000aaa2")

	// A time given is where the counts start, once it is not after now.
	c.add(10 * time.Minute)
	given := b.since(t, "?since="+url.QueryEscape(at.Add(3*time.Minute).Format(time.RFC3339Nano)))
	if given.Since.From != sinceFromGiven || given.Since.Briefing != "" {
		t.Fatalf("a given start %+v", given.Since)
	}
	want("runs ended since a time given", given.RunsEnded, "run:later-0000aaa8", "run:late-0000aaa7", "run:legacy-0000aaa5")
	want("tasks failed since a time given", given.TasksFailed)
}

// A read made after the server has told an event the page reads since
// again on (run, dispatch, brakeman, session, removed) is never answered
// with a reply computed before it, the desk's clock never moved: each
// event is waited for on the stream before the read. A reply computed
// while an event passed is not kept as fresh either: the cache marks a
// reply with what had been told before it was computed.
func TestSinceIsReadAgainAfterAnEvent(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	doneOf(t)(b.srv.dispatch.CreateProject(t.Context(), adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	c := b.fakeClock()
	events := b.sse(t)
	told := func(prefix string) {
		t.Helper()
		b.waitEvent(t, events, func(ev string) bool { return strings.HasPrefix(ev, prefix) })
	}
	first := b.since(t, "")
	if first.Since.From != sinceFromDay || first.RunsEnded.Count != 0 || first.TicketsReady.Count != 0 || first.QuestionsOpen.Count != 0 {
		t.Fatalf("the first read: %+v", first)
	}

	// run: the engine tells it once the run's end is readable.
	b.sinceRecord(t, "ended-0000aaa1", "", c.now().Add(-time.Minute))
	b.srv.events.run("ended-0000aaa1", false)
	told(`run {"id":"ended-0000aaa1"`)
	if got := b.since(t, ""); got.RunsEnded.Count != 1 {
		t.Fatalf("after a run event: %+v", got.RunsEnded)
	}

	// dispatch: a ticket made ready through its route.
	key := b.sinceTicket(t, "Ready now", dispatch.TicketReady)
	told("dispatch ")
	if got := b.since(t, ""); got.TicketsReady.Count != 1 || got.TicketsReady.Refs[0] != "ticket:"+key {
		t.Fatalf("after a dispatch event: %+v", got.TicketsReady)
	}

	// session: a question asked.
	sid := b.createSession("cat")
	d, _ := b.srv.registry.Get(sid)
	d.(*session.Local).SetAttentionFull(session.AttentionNeedsInput, "Proceed?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Yes", Input: "y"}})
	b.waitEvent(t, events, func(ev string) bool {
		return strings.HasPrefix(ev, "session ") && strings.Contains(ev, sid) && strings.Contains(ev, string(session.AttentionNeedsInput))
	})
	if got := b.since(t, ""); got.QuestionsOpen.Count != 1 {
		t.Fatalf("after a session event: %+v", got.QuestionsOpen)
	}

	// removed: the session stopped, then gone, its question with it.
	b.expect(t, "DELETE", "/api/sessions/"+sid, nil, http.StatusOK, "")
	if !waitFor(30*time.Second, func() bool { return d.Info().Status.Ended() }) {
		t.Fatal("the session did not end")
	}
	if resp, _ := b.do("DELETE", "/api/sessions/"+sid, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the removal: %d", resp.StatusCode)
	}
	told(`removed {"id":"` + sid + `"}`)
	if got := b.since(t, ""); got.QuestionsOpen.Count != 0 {
		t.Fatalf("after a removed event: %+v", got.QuestionsOpen)
	}

	// brakeman: a briefing kept is the new start.
	brief := b.sinceBriefing(t)
	b.desk.publish(brief.ID)
	told(`brakeman {"id":"` + brief.ID + `"`)
	if got := b.since(t, ""); got.Since.From != sinceFromBriefing || got.Since.Briefing != brief.ID {
		t.Fatalf("after a brakeman event: %+v", got.Since)
	}

	// An event told while a reply is computed: that reply is answered
	// once, and the next read computes again.
	var once sync.Once
	during := func() {
		once.Do(func() {
			b.sinceRecord(t, "during-0000aaa2", "", c.now())
			b.srv.events.run("during-0000aaa2", false)
		})
	}
	b.desk.live.sinceCounted.Store(&during)
	t.Cleanup(func() { b.desk.live.sinceCounted.Store(nil) })
	c.add(time.Millisecond)
	query := "?since=" + url.QueryEscape(c.now().Add(-time.Hour).Format(time.RFC3339Nano))
	if got := b.since(t, query); got.RunsEnded.Count != 1 {
		t.Fatalf("the reply computed before the event: %+v", got.RunsEnded)
	}
	told(`run {"id":"during-0000aaa2"`)
	if got := b.since(t, query); got.RunsEnded.Count != 2 {
		t.Fatalf("a reply computed while an event passed was kept as fresh: %+v", got.RunsEnded)
	}
}

// With no briefing kept, the counts are of the last day: a briefing
// still running is none.
func TestSinceWithNoBriefingIsTheLastDay(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	c := b.fakeClock()
	b.briefing(t)
	b.sinceRecord(t, "day-0000aaa1", "", c.now().Add(-23*time.Hour))
	b.sinceRecord(t, "older-0000aaa2", "", c.now().Add(-25*time.Hour))
	got := b.since(t, "")
	if got.Since.From != sinceFromDay || got.Since.Briefing != "" || !got.Since.At.Equal(c.now().Add(-defaultBriefingWindow).UTC()) {
		t.Fatalf("the start %+v at %s", got.Since, c.now())
	}
	if got.RunsEnded.Count != 1 || got.RunsEnded.Refs[0] != "run:day-0000aaa1" || len(got.TasksFailed.Refs) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Brakeman's own runs are left out: its turn's run once stopped, and its
// session's question.
func TestSinceLeavesOutBrakemansRuns(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.turnLocal.SetAttentionFull(session.AttentionNeedsInput, "Trust this folder?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Yes", Input: "y"}})
	got := f.since(t, "?since="+url.QueryEscape(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)))
	if got.QuestionsOpen.Count != 1 || got.QuestionsOpen.Refs[0] != "question:"+f.plain+"/"+f.question {
		t.Fatalf("the questions %+v: the turn's own is counted, or the plain session's is not", got.QuestionsOpen)
	}
	run := f.runOf(t, f.sid)
	f.endTurn(t)
	f.sinceRecord(t, "people-0000ccc1", "", time.Now().UTC())
	got = f.since(t, "?since="+url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)))
	if strings.Contains(strings.Join(got.RunsEnded.Refs, " "), run) || !strings.Contains(strings.Join(got.RunsEnded.Refs, " "), "run:people-0000ccc1") {
		t.Fatalf("the runs ended %+v: Brakeman's %s counted, or a person's not", got.RunsEnded, run)
	}
}

// Each kind names at most five refs beside its whole count, and a read
// whose window of Dispatch's changes starts after the start says it is
// truncated.
func TestSinceIsBounded(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	doneOf(t)(b.srv.dispatch.CreateProject(t.Context(), adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	start := time.Now().Add(-time.Minute).UTC()
	for i := range 7 {
		b.sinceTicket(t, fmt.Sprintf("Ready %d", i), dispatch.TicketReady)
		b.sinceRecord(t, fmt.Sprintf("r%d-0000aaa%d", i, i), "", start.Add(time.Duration(i+1)*time.Second))
	}
	query := "?since=" + url.QueryEscape(start.Format(time.RFC3339Nano))
	got := b.since(t, query)
	if got.TicketsReady.Count != 7 || len(got.TicketsReady.Refs) != sinceRefs || got.RunsEnded.Count != 7 || len(got.RunsEnded.Refs) != sinceRefs || got.Truncated {
		t.Fatalf("%+v", got)
	}
	if got.RunsEnded.Refs[0] != "run:r6-0000aaa6" {
		t.Fatalf("the newest is not first: %v", got.RunsEnded.Refs)
	}
	old := changesWindow
	changesWindow = 3
	t.Cleanup(func() { changesWindow = old })
	// Another query: the reply above is kept for five seconds.
	got = b.since(t, "?since="+url.QueryEscape(start.Add(-time.Nanosecond).Format(time.RFC3339Nano)))
	if !got.Truncated || got.TicketsReady.Count > 3 {
		t.Fatalf("a window of 3 changes: %+v", got)
	}
}

// Only since= is taken, once, an RFC 3339 time not after now.
func TestSinceRefusesOtherParameters(t *testing.T) {
	t.Parallel()
	b := brakemanEnv(t, nil)
	now := time.Now().UTC()
	for _, q := range []string{"?other=1", "?since=yesterday", "?since=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339)),
		"?since=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)) + "&since=" + url.QueryEscape(now.Add(-2*time.Hour).Format(time.RFC3339)), "?since="} {
		b.expect(t, "GET", "/api/brakeman/since"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	b.since(t, "?since="+url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)))
}

// The since read is the admin's alone: no token, a turn's own token and a
// session's decision link are refused.
func TestSinceIsTheAdminsAlone(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	_, link := f.decisionLink(t, "/api/sessions/"+f.plain+"/links", "phone", 0)
	for name, tok := range map[string]string{"the turn's token": f.tok, "a decision link": link, "no token": ""} {
		resp, body := f.raw("/api/brakeman/since", tok, map[string]string{brakemanSessionHeader: f.sid})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %.200s", name, resp.StatusCode, body)
		}
	}
	f.since(t, "")
}

// The largest reply fits its 8 KiB: five of the longest ref of each kind.
func TestSinceFitsItsBound(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 17, 5, 8, 0, 123456789, time.UTC)
	run := strings.Repeat("a", 64) + "-0123abcd"
	refs := func(r string) sinceCount {
		c := sinceCount{Count: 1 << 30, Oldest: &at}
		for range sinceRefs {
			c.Refs = append(c.Refs, r)
		}
		return c
	}
	out := brakemanSince{Since: sinceStart{From: sinceFromBriefing, Briefing: "br-" + strings.Repeat("9", 18), At: at},
		RunsEnded: refs("run:" + run), TasksFailed: refs("task:" + run + "/" + strings.Repeat("t", 40)),
		TicketsReady: refs("ticket:ABCDEF-" + strings.Repeat("9", 18)), QuestionsOpen: refs("question:" + strings.Repeat("f", 16) + "/" + strings.Repeat("f", 16)), Truncated: true}
	for _, c := range []sinceCount{out.RunsEnded, out.TasksFailed, out.TicketsReady, out.QuestionsOpen} {
		if _, err := brakeman.ParseRef(c.Refs[0]); err != nil {
			t.Fatalf("%s: %v", c.Refs[0], err)
		}
	}
	b, err := json.Marshal(out)
	if err != nil || len(b) > maxBrakemanSince {
		t.Fatalf("the largest reply is %d bytes: %v", len(b), err)
	}
}
