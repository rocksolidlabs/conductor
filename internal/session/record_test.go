package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

func TestActivityBroadcastAndReplay(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	for i := 0; i < 60; i++ {
		s.Record(ActivityEntry{Type: "link", Message: "x"})
	}
	sink := newChanSink(false)
	sub, err := s.Attach("", RoleView, "", 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	// The viewer's own join entry is recorded once it is attached, as a live
	// entry, behind the replay on the viewer's queue: once it has come,
	// every activity frame the viewer is sent has.
	if waitControl(sink, func(m map[string]any) bool {
		return m["t"] == proto.CtlActivity && m["type"] == ActivityJoin && m["by"] == sub.ID
	}) == nil {
		t.Fatalf("the viewer's own join entry never came (%d frames)", sink.count())
	}
	replayed := 0
	for i := 0; i < sink.count(); i++ {
		f, err := proto.Decode(sink.frame(i))
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == proto.CtlActivity {
			replayed++
			if m["at"] == nil || m["type"] == nil {
				t.Fatalf("activity frame missing fields: %v", m)
			}
		}
	}
	// ActivityReplay history entries plus this viewer's own join entry.
	if replayed != ActivityReplay+1 {
		t.Fatalf("replayed %d, want %d", replayed, ActivityReplay+1)
	}
	// A live entry is broadcast to attached viewers.
	before := sink.count()
	s.Record(ActivityEntry{Type: "link", Message: "live"})
	sink.waitFrames(t, before+1)
	if m := decodeControl(t, sink.frame(before)); m["t"] != proto.CtlActivity || m["message"] != "live" {
		t.Fatalf("live entry: %v", m)
	}
}

// RecordServer records an entry the server makes of an action it bounds
// itself whatever the event bucket holds: with no token left, and none to be
// earned (the bucket's last refill is ahead of the clock), Record drops an
// artifact entry and RecordServer records it, broadcast and handed to
// OnActivity as Record's would be (#436, an agent's link opened).
func TestRecordServerSpendsNoToken(t *testing.T) {
	var (
		mu     sync.Mutex
		hooked []ActivityEntry
	)
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, OnActivity: func(_ string, e ActivityEntry, _ AttentionState) {
		if e.Type == ActivityArtifact {
			mu.Lock()
			hooked = append(hooked, e)
			mu.Unlock()
		}
	}})
	s.mu.Lock()
	s.events = EventBucket{tokens: 0, last: time.Now().Add(time.Hour)}
	s.mu.Unlock()
	if s.Record(ActivityEntry{Type: ActivityArtifact, Message: "an agent's own"}) {
		t.Fatal("an artifact entry recorded with the bucket empty")
	}
	s.RecordServer(ActivityEntry{Type: ActivityArtifact, Message: "link opened in edge", URL: "https://example.test/"})
	var got []ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityArtifact {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].Message != "link opened in edge" || got[0].URL != "https://example.test/" {
		t.Fatalf("the artifact entries %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hooked) != 1 || hooked[0].Message != "link opened in edge" || s.Dropped() != 1 {
		t.Fatalf("OnActivity was told %+v, dropped %d", hooked, s.Dropped())
	}
}

func TestRecordRateLimitsPerSession(t *testing.T) {
	var got int
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, OnActivity: func(string, ActivityEntry, AttentionState) { got++ }})
	accepted := 0
	for i := 0; i < 200; i++ {
		if s.Record(ActivityEntry{Type: ActivityProgress, Message: "x"}) {
			accepted++
		}
	}
	// The bucket earns tokens while the loop runs: allow 200 ms of stall.
	if accepted > EventBurst+EventRatePerSecond/5 || accepted < EventBurst/2 || s.Dropped() != uint64(200-accepted) || got != accepted {
		t.Fatalf("accepted %d dropped %d hooks %d", accepted, s.Dropped(), got)
	}
	time.Sleep(1100 * time.Millisecond)
	if !s.Record(ActivityEntry{Type: ActivityProgress}) {
		t.Fatal("bucket did not refill")
	}
}

// syncBuffer is a log destination the test can read while the session writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRecordDropsAreNotStoredBroadcastOrReported(t *testing.T) {
	var hmu sync.Mutex
	hooked := map[string]bool{}
	logs := &syncBuffer{}
	s, _ := newLocalWith(t, Options{
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		OnActivity: func(_ string, e ActivityEntry, _ AttentionState) {
			hmu.Lock()
			hooked[e.Message] = true
			hmu.Unlock()
		},
	})
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	dropped := map[string]bool{}
	for i := 0; i < 200; i++ {
		msg := fmt.Sprintf("p%d", i)
		if !s.Record(ActivityEntry{Type: ActivityProgress, Message: msg}) {
			dropped[msg] = true
		}
	}
	if len(dropped) < 100 || s.Dropped() != uint64(len(dropped)) {
		t.Fatalf("%d entries refused, Dropped() = %d", len(dropped), s.Dropped())
	}

	// A fresh bucket is full: this stands in for waiting a second. The sink
	// receives frames in the order they were queued, so a dropped entry that
	// had been broadcast would already be in front of the sentinel.
	s.mu.Lock()
	s.events = EventBucket{}
	s.mu.Unlock()
	if !s.Record(ActivityEntry{Type: ActivityProgress, Message: "sentinel"}) {
		t.Fatal("a fresh bucket refused an entry")
	}
	if waitControl(sink, func(m map[string]any) bool { return m["message"] == "sentinel" }) == nil {
		t.Fatal("sentinel not broadcast")
	}
	for i := 0; i < sink.count(); i++ {
		if f, err := proto.Decode(sink.frame(i)); err == nil && f.Type == proto.TypeControl {
			var m map[string]any
			if json.Unmarshal(f.Payload, &m) == nil && dropped[fmt.Sprint(m["message"])] {
				t.Fatalf("dropped entry %v was broadcast", m["message"])
			}
		}
	}
	for _, e := range s.Activity() {
		if dropped[e.Message] {
			t.Fatalf("dropped entry %q was stored", e.Message)
		}
	}
	hmu.Lock()
	for msg := range dropped {
		if hooked[msg] {
			t.Fatalf("OnActivity was called for dropped entry %q", msg)
		}
	}
	hmu.Unlock()

	// A flood must not become a log flood: the first drop and every 100th.
	if n := strings.Count(logs.String(), "event rate limit"); n != 2 {
		t.Fatalf("%d debug lines for %d drops, want 2:\n%s", n, len(dropped), logs.String())
	}
}

// drainBucket empties the session's event bucket and stops it refilling for an
// hour, so a test can rely on every bucketed entry being dropped however
// slowly it runs.
func drainBucket(s *Local) {
	s.mu.Lock()
	s.events = EventBucket{last: time.Now().Add(time.Hour)}
	s.mu.Unlock()
}

// The session and the server produce join, leave, input, link and status
// entries themselves; a chatty hook must never starve the roster rows or the
// final status row.
func TestRecordSessionEntriesBypassTheBucket(t *testing.T) {
	var hmu sync.Mutex
	hooked := map[string]bool{}
	s, _ := newLocalWith(t, Options{OnActivity: func(_ string, e ActivityEntry, _ AttentionState) {
		hmu.Lock()
		hooked[e.Message] = true
		hmu.Unlock()
	}})
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}

	// They do not spend tokens: after sixty of them the bucket holds a full burst.
	for i := 0; i < 60; i++ {
		s.Record(ActivityEntry{Type: ActivityLink, Message: "warm-up"})
	}
	accepted := 0
	for i := 0; i < 200; i++ { // a chatty hook empties the bucket
		if s.Record(ActivityEntry{Type: ActivityProgress, Message: "flood"}) {
			accepted++
		}
	}
	if accepted < EventBurst {
		t.Fatalf("only %d of the first entries an agent reported were accepted: session entries spent tokens", accepted)
	}
	drainBucket(s) // and it stays empty however slowly this test runs
	dropped := s.Dropped()

	// What an agent reports is still refused, and so is a type nobody knows ...
	agent := []string{ActivityAttention, ActivityProgress, ActivityArtifact, ActivityHandoff, ActivityToolUse, ActivityToolDenied, ActivityError, "mystery"}
	for _, typ := range agent {
		if s.Record(ActivityEntry{Type: typ, Message: "agent " + typ}) {
			t.Errorf("a %s entry got past an empty bucket", typ)
		}
	}
	// ... but what the session and the server produce themselves is not.
	own := []string{ActivityStatus, ActivityJoin, ActivityLeave, ActivityInput, ActivityLink}
	for _, typ := range own {
		if !s.Record(ActivityEntry{Type: typ, Message: "own " + typ}) {
			t.Errorf("a %s entry was refused by an empty bucket", typ)
		}
	}
	if got, want := s.Dropped(), dropped+uint64(len(agent)); got != want {
		t.Errorf("Dropped() = %d, want %d: only the agent entries count", got, want)
	}

	// Frames reach the sink in the order they were queued, so once the last
	// entry has arrived every earlier one has too.
	if waitControl(sink, func(m map[string]any) bool { return m["message"] == "own link" }) == nil {
		t.Fatal("the last session entry was not broadcast")
	}
	stored := map[string]bool{}
	for _, e := range s.Activity() {
		stored[e.Message] = true
	}
	broadcast := map[string]bool{}
	for i := 0; i < sink.count(); i++ {
		if f, err := proto.Decode(sink.frame(i)); err == nil && f.Type == proto.TypeControl {
			var m map[string]any
			if json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlActivity {
				broadcast[fmt.Sprint(m["message"])] = true
			}
		}
	}
	hmu.Lock()
	defer hmu.Unlock()
	for _, typ := range own {
		msg := "own " + typ
		if !stored[msg] || !broadcast[msg] || !hooked[msg] {
			t.Errorf("%s: stored %v, broadcast %v, passed to OnActivity %v; want all three", typ, stored[msg], broadcast[msg], hooked[msg])
		}
	}
	for _, typ := range agent {
		msg := "agent " + typ
		if stored[msg] || broadcast[msg] || hooked[msg] {
			t.Errorf("%s: stored %v, broadcast %v, passed to OnActivity %v; want none", typ, stored[msg], broadcast[msg], hooked[msg])
		}
	}
}

// attentionEntries returns the attention entries in the session's log.
func attentionEntries(s *Local) []ActivityEntry {
	var out []ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityAttention {
			out = append(out, e)
		}
	}
	return out
}

// attentionHook is an OnActivity hook that keeps the attention entries it is
// passed.
type attentionHook struct {
	mu  sync.Mutex
	got []ActivityEntry
}

func (h *attentionHook) hook(_ string, e ActivityEntry, _ AttentionState) {
	if e.Type != ActivityAttention {
		return
	}
	h.mu.Lock()
	h.got = append(h.got, e)
	h.mu.Unlock()
}

func (h *attentionHook) entries() []ActivityEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ActivityEntry(nil), h.got...)
}

// wait returns the attention entries passed so far once there are at least
// n, or fails the test after 3 s: the hook runs after the broadcast, on the
// goroutine that recorded the entry.
func (h *attentionHook) wait(t *testing.T, n int) []ActivityEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := h.entries(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d attention entries passed to OnActivity, want %d", len(h.entries()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The session's own observations, the bell, an OSC notification and a prompt
// the screen pattern finds, spend no token of the event bucket, and nor does
// the attention entry of a change the session applies. So a hook that has
// emptied the bucket with tool events cannot leave a badge showing
// needs_input whose entry the activity log, the Events feed and the webhooks
// never got. A host applies what the server forwards (SetAttentionFull: the
// server spent a token of its own on it) the same way.
func TestAttentionChangesAreRecordedWhateverTheBucketHolds(t *testing.T) {
	cases := []struct {
		name            string
		pattern         *regexp.Regexp
		apply           func(s *Local, p *fakeProc)
		source, message string
	}{
		{"bell", nil, func(_ *Local, p *fakeProc) { p.outW.Write([]byte("\a")) }, SourceBell, "terminal bell"},
		{"osc", nil, func(_ *Local, p *fakeProc) { p.outW.Write([]byte("\x1b]9;need approval\a")) }, SourceOSC, "need approval"},
		{"pattern", regexp.MustCompile(`\? $`), func(_ *Local, p *fakeProc) { p.outW.Write([]byte("Allow Bash? ")) }, SourcePattern, "prompt: Allow Bash?"},
		{"forwarded by the server", nil, func(s *Local, _ *fakeProc) {
			s.SetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, nil)
		}, SourceAPI, "Allow Bash?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var h attentionHook
			s, p := newLocalWith(t, Options{Pattern: c.pattern, OnActivity: h.hook})
			sink := newChanSink(false)
			if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
				t.Fatal(err)
			}
			// A burst of tool events spends the bucket, which then stays empty
			// however slowly the test runs.
			for i := 0; i < EventBurst; i++ {
				s.Record(ActivityEntry{Type: ActivityToolUse, Tool: "Bash"})
			}
			drainBucket(s)
			if s.Record(ActivityEntry{Type: ActivityToolUse, Tool: "Bash"}) {
				t.Fatal("a tool event got past an empty bucket")
			}
			dropped := s.Dropped()

			c.apply(s, p)
			att := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput })
			if att.Source != c.source || att.Message != c.message {
				t.Fatalf("attention %+v", att)
			}
			if m := waitControl(sink, func(m map[string]any) bool {
				return m["t"] == proto.CtlActivity && m["type"] == ActivityAttention
			}); m == nil || m["message"] != c.message {
				t.Fatalf("the attention entry was not broadcast: %v", m)
			}
			if got := attentionEntries(s); len(got) != 1 || got[0].Message != c.message {
				t.Fatalf("the log holds the attention entries %+v, want the one for %q", got, c.message)
			}
			if got := h.wait(t, 1); len(got) != 1 || got[0].Message != c.message {
				t.Fatalf("OnActivity got the attention entries %+v", got)
			}
			if got := s.Dropped(); got != dropped {
				t.Fatalf("Dropped() went from %d to %d: the change asked the bucket for a token", dropped, got)
			}
		})
	}
}

// The attention entry of a change is stamped with the time of the change, the
// Since of the attention it records, taken as the state is set: whoever sees
// the state sees it no earlier than the entry's At, so an entry that reaches
// OnActivity late still says when its state began (a crew's run engine tells
// a done from before a member's prompt by it).
func TestAttentionEntryIsStampedAtTheChange(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	check := func(what string) {
		t.Helper()
		entries := attentionEntries(s)
		since := s.Info().Attention.Since
		if len(entries) == 0 || since == nil || !entries[len(entries)-1].At.Equal(*since) {
			t.Fatalf("%s: the entry %+v, the attention since %v", what, entries, since)
		}
	}
	s.SetAttention(AttentionDone, "idle", SourceAPI)
	check("SetAttention")
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "what next?", SourceAPI, "", nil); err != nil {
		t.Fatal(err)
	}
	check("TrySetAttentionFull")
}

// A report from outside the session, an agent's or an admin's through the
// API, pays for itself: TrySetAttentionFull spends one token before it changes
// anything, whether or not the report changes anything, and the entry of a
// change it applies spends no second one. With no token left the report is
// refused whole (no state, no broadcast, no entry, no OnActivity, no
// OnChange) and counted in Dropped.
func TestTrySetAttentionFullSpendsOneTokenOrChangesNothing(t *testing.T) {
	var h attentionHook
	var changes atomic.Int32
	s, _ := newLocalWith(t, Options{OnActivity: h.hook, OnChange: func(Info) { changes.Add(1) }})
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	// Two tokens, and none earned while the test runs.
	s.mu.Lock()
	s.events = EventBucket{tokens: 2, last: time.Now().Add(time.Hour)}
	s.mu.Unlock()
	left := func() float64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.events.tokens
	}

	options := []Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}}
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, options); err != nil {
		t.Fatalf("a report with a token for it: %v", err)
	}
	if n := left(); n != 1 {
		t.Fatalf("%v of 2 tokens left after one report, want 1", n)
	}
	if att := s.Info().Attention; att.State != AttentionNeedsInput || att.Message != "Allow Bash?" || att.Source != SourceAPI || att.Kind != KindPermission || len(att.Options) != 2 {
		t.Fatalf("attention %+v", att)
	}
	if got := attentionEntries(s); len(got) != 1 || got[0].Message != "Allow Bash?" {
		t.Fatalf("the log holds the attention entries %+v, want the report's one", got)
	}
	if got := h.wait(t, 1); len(got) != 1 {
		t.Fatalf("OnActivity got %+v", got)
	}

	// The same report again changes nothing, and pays all the same.
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, KindPermission, options); err != nil {
		t.Fatalf("a repeated report with a token for it: %v", err)
	}
	if n := left(); n != 0 {
		t.Fatalf("%v tokens left after the repeated report, want 0", n)
	}
	if got := attentionEntries(s); len(got) != 1 {
		t.Fatalf("a report that changed nothing left an entry: %+v", got)
	}

	// No token left: refused whole.
	before, changed, dropped := s.Info().Attention, changes.Load(), s.Dropped()
	if err := s.TrySetAttentionFull(AttentionWorking, "on it", SourceAPI, "", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a report with no token for it: %v, want ErrRateLimited", err)
	}
	if after := s.Info().Attention; after.State != before.State || after.Message != before.Message || after.Since != before.Since {
		t.Fatalf("a refused report changed the attention from %+v to %+v", before, after)
	}
	if got := attentionEntries(s); len(got) != 1 {
		t.Fatalf("a refused report left an entry: %+v", got)
	}
	if changes.Load() != changed {
		t.Fatal("a refused report called OnChange")
	}
	if got := s.Dropped(); got != dropped+1 {
		t.Fatalf("Dropped() = %d, want %d: the refusal counts", got, dropped+1)
	}
	// Frames arrive in the order they were queued: once an entry recorded after
	// the refusal has arrived, whatever the refusal broadcast would have too.
	s.Record(ActivityEntry{Type: ActivityLink, Message: "sentinel"})
	if waitControl(sink, func(m map[string]any) bool { return m["message"] == "sentinel" }) == nil {
		t.Fatal("sentinel not broadcast")
	}
	for i := 0; i < sink.count(); i++ {
		if f, err := proto.Decode(sink.frame(i)); err == nil && f.Type == proto.TypeControl {
			var m map[string]any
			if json.Unmarshal(f.Payload, &m) == nil && (m["state"] == string(AttentionWorking) || m["message"] == "on it") {
				t.Fatalf("the refused report was broadcast: %v", m)
			}
		}
	}
	if got := h.entries(); len(got) != 1 {
		t.Fatalf("OnActivity got %+v after the refusal", got)
	}
}

func TestOnActivityRunsAfterTheBroadcastAndOutsideTheLock(t *testing.T) {
	type seen struct {
		id        string
		entry     ActivityEntry
		stored    ActivityEntry
		info      Info
		broadcast bool
	}
	sink := newChanSink(false)
	got := make(chan seen, 1)
	var s *Local
	s, _ = newLocalWith(t, Options{OnActivity: func(id string, e ActivityEntry, _ AttentionState) {
		if e.Type != ActivityArtifact {
			return
		}
		log := s.Activity()
		got <- seen{
			id:     id,
			entry:  e,
			stored: log[len(log)-1],
			info:   s.Info(), // takes the session lock: hangs if Record still holds it
			// The viewer's frame is already queued, so it arrives without Record's help.
			broadcast: waitControl(sink, func(m map[string]any) bool { return m["type"] == ActivityArtifact }) != nil,
		}
	}})
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		done <- s.Record(ActivityEntry{Type: ActivityArtifact, Message: "PR opened\x1b", URL: " https://example.com/pull/1 "})
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Record refused the entry")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Record did not return: the hook ran under the session lock")
	}
	var c seen
	select {
	case c = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("OnActivity was not called")
	}
	if c.id != "sess" || c.info.ID != "sess" {
		t.Errorf("hook session id %q (info %q), want sess", c.id, c.info.ID)
	}
	if c.entry.Message != "PR opened" || c.entry.URL != "https://example.com/pull/1" || c.entry.At.IsZero() {
		t.Errorf("hook got an entry that is not the cleaned, stamped one: %+v", c.entry)
	}
	if c.stored != c.entry {
		t.Errorf("hook entry %+v differs from the stored %+v", c.entry, c.stored)
	}
	if !c.broadcast {
		t.Error("the hook ran before the entry was broadcast")
	}
}

// The attention state an attention entry records reaches OnActivity with the
// entry, as a value: a hook held while the session moves on (it runs outside
// the session lock, so the session can) goes on with its entry's state,
// though the session shows another by then. Every other entry comes with
// none.
func TestOnActivityGetsTheStateTheEntryRecords(t *testing.T) {
	type call struct {
		typ, msg string
		state    AttentionState
	}
	var mu sync.Mutex
	var calls []call
	entered, release := make(chan struct{}), make(chan struct{})
	letGo := sync.OnceFunc(func() { close(release) })
	t.Cleanup(letGo) // a failed test does not leave the hook held
	var s *Local
	var shown AttentionState // what the session shows when the held hook goes on
	s, _ = newLocalWith(t, Options{OnActivity: func(_ string, e ActivityEntry, state AttentionState) {
		if e.Message == "Allow Bash?" {
			close(entered)
			<-release
			shown = s.Info().Attention.State
		}
		mu.Lock()
		calls = append(calls, call{e.Type, e.Message, state})
		mu.Unlock()
	}})
	held := make(chan struct{})
	go func() {
		defer close(held)
		s.SetAttention(AttentionNeedsInput, "Allow Bash?", SourceAPI)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OnActivity was not called for the first entry")
	}
	s.SetAttention(AttentionDone, "", SourceAPI)
	s.SetAttention(AttentionWorking, "compiling", SourceAPI)
	s.Record(ActivityEntry{Type: ActivityProgress, Message: "1/3"})
	letGo()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the held hook did not return")
	}
	if shown != AttentionWorking {
		t.Fatalf("the session showed %q when the held hook went on, want working", shown)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []call{
		{ActivityAttention, "done", AttentionDone},
		{ActivityAttention, "compiling", AttentionWorking},
		{ActivityProgress, "1/3", ""},
		{ActivityAttention, "Allow Bash?", AttentionNeedsInput},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("OnActivity got %+v, want %+v", calls, want)
	}
}

func TestActivityFramesCarryEventFields(t *testing.T) {
	s, _ := newLocalWith(t, Options{})
	sink := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	for _, e := range []ActivityEntry{
		{Type: ActivityHandoff, Message: "review please", To: "Marco"},
		{Type: ActivityArtifact, URL: "https://example.com/pull/1"},
		{Type: ActivityToolDenied, Tool: "Bash"},
		{Type: ActivityProgress, Message: "3/7"},
	} {
		if !s.Record(e) {
			t.Fatalf("Record refused %+v", e)
		}
	}
	entry := func(sink *chanSink, typ string) map[string]any {
		return waitControl(sink, func(m map[string]any) bool { return m["t"] == proto.CtlActivity && m["type"] == typ })
	}
	check := func(who string, sink *chanSink) {
		if m := entry(sink, ActivityHandoff); m == nil || m["to"] != "Marco" || m["message"] != "review please" {
			t.Errorf("%s handoff: %v", who, m)
		}
		if m := entry(sink, ActivityArtifact); m == nil || m["url"] != "https://example.com/pull/1" {
			t.Errorf("%s artifact: %v", who, m)
		}
		if m := entry(sink, ActivityToolDenied); m == nil || m["tool"] != "Bash" {
			t.Errorf("%s tool_denied: %v", who, m)
		}
		// Fields an entry does not use are left out of the JSON, not sent empty.
		m := entry(sink, ActivityProgress)
		if m == nil {
			t.Fatalf("%s progress entry missing", who)
		}
		for _, k := range []string{"url", "to", "tool", "by", "byName"} {
			if _, present := m[k]; present {
				t.Errorf("%s progress entry carries %q: %v", who, k, m)
			}
		}
	}
	check("live", sink)

	late := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, late); err != nil {
		t.Fatal(err)
	}
	check("replayed", late)
}
