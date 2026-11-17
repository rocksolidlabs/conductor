package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Progress while a turn runs (brakeman_progress.go): the turn's read is
// noted as it is admitted and its count sent once it is counted, at most
// one event a second, the last count on the trailing edge. Every test
// drives the coalescer through its timer seam (progressAfter) on a clock
// of the test's, never a sleep, and judges what was sent by a marker
// event sent after it on the same stream.

// progressTimers replaces the desk's coalescer timer with timers the
// test fires: each one asked for arrives on the channel answered, with
// the wait it was asked for.
type progressTimer struct {
	wait time.Duration
	fire chan time.Time
}

func (b *bkEnv) progressTimers(t *testing.T) <-chan progressTimer {
	t.Helper()
	timers := make(chan progressTimer, 64)
	b.desk.live.mu.Lock()
	b.desk.live.progressAfter = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		timers <- progressTimer{wait: d, fire: ch}
		return ch
	}
	b.desk.live.mu.Unlock()
	t.Cleanup(func() {
		b.desk.live.mu.Lock()
		b.desk.live.progressAfter = nil
		b.desk.live.mu.Unlock()
	})
	return timers
}

// progressMark is the marker event: a stream holds every event sent
// before it once it holds the marker.
const progressMark = "event: progress-mark\ndata: {}\n\n"

// progressUntilMark sends the marker and answers the progress events of
// id the stream held before it, in order; the events of other kinds it
// passes over are answered in rest.
func (b *bkEnv) progressUntilMark(t *testing.T, events <-chan string, id string) (got []brakemanProgress, rest []string) {
	t.Helper()
	b.srv.events.broadcast([]byte(progressMark))
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev == "progress-mark {}" {
				return got, rest
			}
			if p, ok := progressEvent(t, ev); ok {
				if p.ID == id {
					got = append(got, p)
				}
				continue
			}
			rest = append(rest, ev)
		case <-deadline:
			t.Fatal("the marker never arrived")
		}
	}
}

// progressEvent reads ev as a brakeman_progress event, at most its bound.
func progressEvent(t *testing.T, ev string) (brakemanProgress, bool) {
	t.Helper()
	data, ok := strings.CutPrefix(ev, "brakeman_progress ")
	if !ok {
		return brakemanProgress{}, false
	}
	if len(data) > maxBrakemanProgress {
		t.Fatalf("a progress event of %d bytes: %.200s", len(data), data)
	}
	var p brakemanProgress
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return p, true
}

// waitProgress reads events until a progress event of id with reads at
// least n, and answers it.
func waitProgress(t *testing.T, events <-chan string, id string, n int) brakemanProgress {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev := <-events:
			if p, ok := progressEvent(t, ev); ok && p.ID == id && p.Reads >= n {
				return p
			}
		case <-deadline:
			t.Fatalf("no progress of %s at %d reads", id, n)
		}
	}
}

// taskPath is the fixture's read of team's build task.
func (f *obsFixture) taskPath() string { return "/api/runs/" + f.run + "/tasks/build" }

// A turn's read sends its progress: the read's what and ref, the count
// the record holds, its budget, its deadline and when it began; GET
// /api/brakeman carries the same. The admin's read of the same route is
// no turn's and sends nothing.
func TestAReadSendsProgress(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	events := f.sse(t)
	f.expect(t, "GET", f.taskPath(), nil, http.StatusOK, "")
	if got, _ := f.progressUntilMark(t, events, f.req); len(got) != 0 {
		t.Fatalf("the admin's read sent progress: %+v", got)
	}
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	p := waitProgress(t, events, f.req, 1)
	req, err := f.desk.st.Get(f.req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Reads != req.Reads || p.Reads != 1 || p.ReadsPerTurn != req.Config.Budget.ReadsPerTurn {
		t.Fatalf("the progress %+v, the record's reads %d of %d", p, req.Reads, req.Config.Budget.ReadsPerTurn)
	}
	if p.Reading == nil || p.Reading.What != "task" || p.Reading.Ref != "task:"+f.run+"/build" {
		t.Fatalf("the reading %+v", p.Reading)
	}
	minutes, ok := f.desk.st.Reserved(f.req)
	if !ok || p.Deadline == nil || !p.Deadline.Equal(req.Deadline) || p.StartedAt == nil || !p.StartedAt.Add(time.Duration(minutes)*time.Minute).Equal(*p.Deadline) {
		t.Fatalf("the times %v %v, the record's deadline %s, %d minutes reserved", p.StartedAt, p.Deadline, req.Deadline, minutes)
	}
	state := f.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	var got brakemanProgress
	b, _ := json.Marshal(state["progress"])
	if err := json.Unmarshal(b, &got); err != nil || got.ID != f.req || got.Reads != 1 || got.Reading == nil || *got.Reading != *p.Reading || got.StartedAt == nil || !got.StartedAt.Equal(*p.StartedAt) {
		t.Fatalf("GET /api/brakeman's progress %s, the event's %+v", b, p)
	}
	if _, ok := state["nextBriefing"]; ok {
		t.Fatalf("a role only asked has a next briefing: %v", state["nextBriefing"])
	}
}

// Fifty reads in one second of the desk's clock send two events: the
// first read's at once, then one timer for the rest of the second, whose
// event carries the fiftieth read's count. A read a second later is sent
// at once again.
func TestProgressIsCoalesced(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	c := f.fakeClock()
	timers := f.progressTimers(t)
	events := f.sse(t)
	for range 50 {
		f.expectRead(t, f.taskPath(), http.StatusOK, "")
	}
	if n := len(timers); n != 1 {
		t.Fatalf("%d timers for one second's reads, want 1", n)
	}
	tm := <-timers
	if tm.wait != brakemanProgressEvery {
		t.Fatalf("the trailing send waits %s on a clock that did not move", tm.wait)
	}
	got, _ := f.progressUntilMark(t, events, f.req)
	if len(got) != 1 || got[0].Reads != 1 {
		t.Fatalf("before the timer: %+v", got)
	}
	tm.fire <- c.now()
	p := waitProgress(t, events, f.req, 50)
	if p.Reads != 50 || p.Reading == nil || p.Reading.What != "task" {
		t.Fatalf("the trailing event %+v", p)
	}
	if !waitFor(10*time.Second, func() bool { return f.desk.live.trailed.Load() == 1 }) {
		t.Fatal("the trailing send did not finish")
	}
	if got, _ := f.progressUntilMark(t, events, f.req); len(got) != 0 || len(timers) != 0 {
		t.Fatalf("after the trailing event: %+v, %d timers", got, len(timers))
	}
	c.add(brakemanProgressEvery)
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	if p := waitProgress(t, events, f.req, 51); p.Reads != 51 || len(timers) != 0 {
		t.Fatalf("a read a second later: %+v, %d timers", p, len(timers))
	}
}

// A request cancelled during a read sends nothing for it: its count is
// refused, and the event the cancel sent is the last of the request's.
func TestNoProgressOnceClosed(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.fakeClock()
	timers := f.progressTimers(t)
	events := f.sse(t)
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	waitProgress(t, events, f.req, 1)
	var cancels, cancelStatus atomic.Int64
	seam := func(stage string) {
		if stage == "answered" && cancels.Add(1) == 1 {
			resp, _ := f.do("POST", "/api/brakeman/requests/"+f.req+"/cancel", adminToken, map[string]any{"opId": "c1"})
			cancelStatus.Store(int64(resp.StatusCode))
		}
	}
	observeSeam.Store(&seam)
	t.Cleanup(func() { observeSeam.Store(nil) })
	if status, _ := f.read(t, f.taskPath()); status == http.StatusOK {
		t.Fatal("a read whose request was cancelled under it was answered")
	}
	observeSeam.Store(nil)
	if cancelStatus.Load() != http.StatusOK {
		t.Fatalf("the cancel during the read: %d", cancelStatus.Load())
	}
	f.waitState(t, f.req, brakeman.StateCancelled)
	got, rest := f.progressUntilMark(t, events, f.req)
	if len(got) != 0 || len(timers) != 0 {
		t.Fatalf("progress after the cancel: %+v, %d timers", got, len(timers))
	}
	if !slices.Contains(rest, `brakeman {"id":"`+f.req+`","state":"cancelled"}`) {
		t.Fatalf("no cancelled event: %v", rest)
	}
}

// A trailing send whose timer fires after the request closed sends
// nothing: the record is read before sending.
func TestNoTrailingProgressOnceClosed(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	c := f.fakeClock()
	timers := f.progressTimers(t)
	events := f.sse(t)
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	waitProgress(t, events, f.req, 1)
	tm := <-timers
	f.expect(t, "POST", "/api/brakeman/requests/"+f.req+"/cancel", map[string]any{"opId": "c1"}, http.StatusOK, "")
	f.waitState(t, f.req, brakeman.StateCancelled)
	tm.fire <- c.now()
	if !waitFor(10*time.Second, func() bool { return f.desk.live.trailed.Load() == 1 }) {
		t.Fatal("the trailing send did not finish")
	}
	if got, _ := f.progressUntilMark(t, events, f.req); len(got) != 0 {
		t.Fatalf("progress after the cancel: %+v", got)
	}
	f.expectRead(t, f.taskPath(), http.StatusConflict, "turn_over")
}

// The longest event this build makes fits its bound with its ref; a ref
// that would pass the bound is left out, the read's what kept; every
// what is an observe operation's, at most 16 bytes, named by a route the
// mux serves; a ref that does not parse is left out.
func TestProgressFitsItsBound(t *testing.T) {
	t.Parallel()
	h := newEventHub()
	ch := h.subscribe()
	at := time.Date(2026, 11, 17, 5, 8, 0, 123456789, time.UTC)
	until := at.Add(time.Hour)
	run := strings.Repeat("a", 64) + "-0123abcd"
	task, check := strings.Repeat("t", 40), strings.Repeat("c", 40)
	longest := "check:" + run + "/" + task + "/" + strconv.Itoa(1<<31-1) + "/" + check + "/" + strconv.Itoa(1<<31-1)
	if _, err := brakeman.ParseRef(longest); err != nil {
		t.Fatalf("the longest check ref: %v", err)
	}
	p := brakemanProgress{ID: "br-" + strings.Repeat("9", 18), Reads: 2000, ReadsPerTurn: 2000, StartedAt: &at, Deadline: &until, Reading: &brakemanReadingNow{What: "capture_send", Ref: longest}}
	for _, ref := range []string{longest, strings.Repeat("<", brakeman.MaxRef)} {
		p.Reading.Ref = ref
		h.brakemanProgress(p)
		var msg string
		select {
		case b := <-ch:
			msg = string(b)
		default:
			t.Fatalf("a ref of %d bytes: no event sent", len(ref))
		}
		data := strings.TrimSuffix(strings.TrimPrefix(msg, "event: brakeman_progress\ndata: "), "\n\n")
		if len(data) > maxBrakemanProgress {
			t.Fatalf("%d bytes", len(data))
		}
		var got brakemanProgress
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatal(err)
		}
		want := ref
		if ref != longest {
			want = ""
		}
		if got.Reading == nil || got.Reading.What != "capture_send" || got.Reading.Ref != want {
			t.Fatalf("a ref of %d bytes: %+v", len(ref), got.Reading)
		}
	}
	served := map[string]bool{}
	for _, what := range observeWhats {
		if len(what) > maxProgressWhat {
			t.Errorf("what %q is longer than %d bytes", what, maxProgressWhat)
		}
		served[what] = false
	}
	for pattern, what := range observeWhatOf() {
		if _, ok := served[what]; !ok {
			t.Errorf("%s gives %q, no observe what", pattern, what)
		}
		served[what] = true
	}
	for what, ok := range served {
		if !ok {
			t.Errorf("no route gives what %q", what)
		}
	}
	// A shared read's route, as the mux matches it: the reading by its
	// pattern and path values.
	for _, c := range []struct{ pattern, path, what, ref string }{
		{"GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log", "/api/runs/fix-0123abcd/tasks/build/attempts/3/steps/tests/1/log", "log", "check:fix-0123abcd/build/3/tests/1"},
		{"GET /api/runs/{run}/tasks/{task}", "/api/runs/NOT_A_RUN/tasks/build", "task", ""},
		{"GET /api/brakeman/status", "/api/brakeman/status?run=fix-0123abcd", "status", "run:fix-0123abcd"},
		{"GET /api/runs", "/api/runs?limit=5", "runs", ""},
	} {
		m := http.NewServeMux()
		var got *brakemanReadingNow
		m.HandleFunc(c.pattern, func(_ http.ResponseWriter, r *http.Request) { got = readingOf(r) })
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.path, nil))
		if got == nil || got.What != c.what || got.Ref != c.ref {
			t.Errorf("%s: %+v, want %s %q", c.path, got, c.what, c.ref)
		}
	}
	m := http.NewServeMux()
	var none = &brakemanReadingNow{}
	m.HandleFunc("GET /api/brakeman", func(_ http.ResponseWriter, r *http.Request) { none = readingOf(r) })
	m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/brakeman", nil))
	if none != nil {
		t.Errorf("a route of no observe operation gives %+v", none)
	}
}

// Progress is as droppable as chat: a client whose queue is past
// activityQueueLimit misses it and keeps its stream; it reaches no sink.
func TestProgressReachesNoSink(t *testing.T) {
	t.Parallel()
	h := newEventHub()
	sunk := 0
	h.addSink(func(string, session.ActivityEntry, session.AttentionState) { sunk++ })
	fresh, full := h.subscribe(), h.subscribe()
	for range activityQueueLimit {
		full <- []byte("x")
	}
	h.brakemanProgress(brakemanProgress{ID: "br-1", Reads: 1, ReadsPerTurn: 300})
	if msg := string(<-fresh); !strings.HasPrefix(msg, "event: brakeman_progress\ndata: {\"id\":\"br-1\"") {
		t.Fatalf("the client got %q", msg)
	}
	h.mu.Lock()
	_, kept := h.clients[full]
	h.mu.Unlock()
	if len(full) != activityQueueLimit || !kept {
		t.Fatalf("the full client: %d queued, kept %v", len(full), kept)
	}
	if sunk != 0 {
		t.Fatalf("a sink got %d entries", sunk)
	}
}

// GET /api/brakeman carries the turn's progress on the server that owns
// the records (TestAReadSendsProgress) and, from them, on a second
// server over the same data directory: its reads, budget and deadline,
// never its start or its read, which only the owner knows.
func TestTheStateCarriesProgress(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	f.expectRead(t, f.taskPath(), http.StatusOK, "")
	second := f.restart()
	state := second.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, "")
	if state["readOnly"] != true {
		t.Fatalf("the second server's desk %v", state)
	}
	var got brakemanProgress
	b, _ := json.Marshal(state["progress"])
	if err := json.Unmarshal(b, &got); err != nil || got.ID != f.req || got.Reads != 1 || got.ReadsPerTurn == 0 || got.Deadline == nil || got.StartedAt != nil || got.Reading != nil {
		t.Fatalf("the read-only server's progress %s", b)
	}
	// With no turn under way, there is none.
	f.endTurn(t)
	if state := f.expect(t, "GET", "/api/brakeman", nil, http.StatusOK, ""); state["progress"] != nil {
		t.Fatalf("progress with no turn: %v", state["progress"])
	}
}

// Stop and Cancel are the admin's alone: a turn's own token and a
// session's decision link are refused, and the request stays running.
func TestCancelIsTheAdminsAlone(t *testing.T) {
	f := newObsFixture(t)
	f.turn(t, nil)
	_, link := f.decisionLink(t, "/api/sessions/"+f.plain+"/links", "phone", 0)
	for name, tok := range map[string]string{"the turn's token": f.tok, "a decision link": link, "no token": ""} {
		resp, out := f.do("POST", "/api/brakeman/requests/"+f.req+"/cancel", tok, map[string]any{"opId": "c1"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	if req, err := f.desk.st.Get(f.req); err != nil || req.State != brakeman.StateRunning {
		t.Fatalf("the request after the refusals: %s %v", req.State, err)
	}
}
