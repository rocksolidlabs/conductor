package api

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

func TestEventHubActivityFormat(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	h.activity("s1", session.ActivityEntry{At: at, Type: session.ActivityArtifact, ByName: "agent", Message: "PR", URL: "https://x/1"}, "")
	want := "event: activity\n" +
		`data: {"sessionId":"s1","at":"2026-09-29T12:00:00Z","type":"artifact","byName":"agent","message":"PR","url":"https://x/1"}` + "\n\n"
	select {
	case got := <-ch:
		if string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	default:
		t.Fatal("nothing queued")
	}
}

// An entry may hold line breaks; the SSE message must stay one event of one
// data line, whatever the text says.
func TestEventHubActivityStaysOneMessage(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	h.activity("s1", session.ActivityEntry{Type: session.ActivityError, Message: "a\nb\r\nc d\n\ndata: forged\nevent: session"}, "")
	msg := string(<-ch)
	if strings.Count(msg, "\n") != 3 || !strings.HasSuffix(msg, "\n\n") || !strings.HasPrefix(msg, "event: activity\ndata: {") {
		t.Fatalf("message is not one event: %q", msg)
	}
}

// A browser tab that stopped reading must not hold up the recording
// goroutine (OnActivity runs on it), must not lose its stream to a burst of
// activity, and must keep room for the session changes it cannot do without.
func TestEventHubActivityNeverBlocksOrEvictsAClientThatStoppedReading(t *testing.T) {
	h := newEventHub()
	stalled := h.subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20*eventQueue; i++ {
			h.activity("s", session.ActivityEntry{Type: session.ActivityProgress, Message: "n"}, "")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("activity blocked on a client that is not reading")
	}
	if n := len(stalled); n != activityQueueLimit {
		t.Fatalf("queue holds %d entries, want it held at %d", n, activityQueueLimit)
	}
	h.publishIf(session.Info{ID: "s", Name: "n"}, nil)
	h.removed("s")
	if n := len(stalled); n != activityQueueLimit+2 {
		t.Fatalf("session changes were not queued behind the entries: %d", n)
	}
	for i := 0; i < activityQueueLimit+2; i++ {
		select {
		case msg, ok := <-stalled:
			if !ok {
				t.Fatalf("the client was evicted after %d messages", i)
			}
			want := "event: activity"
			switch i {
			case activityQueueLimit:
				want = "event: session"
			case activityQueueLimit + 1:
				want = "event: removed"
			}
			if !strings.HasPrefix(string(msg), want) {
				t.Fatalf("message %d is %.30q, want %s", i, msg, want)
			}
		default:
			t.Fatalf("queue ran dry after %d messages", i)
		}
	}
}

func TestEventHubActivityResumesOnceTheClientCatchesUp(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	for i := 0; i < 2*eventQueue; i++ {
		h.activity("s", session.ActivityEntry{Type: session.ActivityProgress}, "")
	}
	for len(ch) > 0 {
		<-ch
	}
	h.activity("s", session.ActivityEntry{Type: session.ActivityError, Message: "after"}, "")
	select {
	case msg := <-ch:
		if !strings.Contains(string(msg), `"message":"after"`) {
			t.Fatalf("got %s", msg)
		}
	default:
		t.Fatal("a client that caught up got nothing")
	}
}

// One client that does not read costs the others nothing.
func TestEventHubActivityReachesEveryClientThatReads(t *testing.T) {
	h := newEventHub()
	stalled := h.subscribe()
	fast := h.subscribe()
	var got atomic.Int64
	go func() {
		for range fast {
			got.Add(1)
		}
	}()
	const batches, perBatch = 10, 100
	for b := 1; b <= batches; b++ {
		for i := 0; i < perBatch; i++ {
			h.activity("s", session.ActivityEntry{Type: session.ActivityProgress}, "")
		}
		deadline := time.Now().Add(5 * time.Second)
		for got.Load() < int64(b*perBatch) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got.Load() != int64(b*perBatch) {
			t.Fatalf("the reading client has %d of %d entries", got.Load(), b*perBatch)
		}
	}
	if len(stalled) != activityQueueLimit {
		t.Fatalf("stalled client holds %d", len(stalled))
	}
}

// OnActivity runs on the goroutines that record, at the same time, and the
// hub also serves clients that come and go.
func TestEventHubIsSafeForConcurrentUse(t *testing.T) {
	h := newEventHub()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch := h.subscribe()
				for i := 0; i < 10; i++ {
					select {
					case <-ch:
					default:
					}
				}
				h.unsubscribe(ch)
			}
		}()
	}
	var producers sync.WaitGroup
	for p := 0; p < 8; p++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for i := 0; i < 300; i++ {
				h.activity("s", session.ActivityEntry{Type: session.ActivityToolUse, Tool: "Bash"}, "")
				if i%50 == 0 {
					h.publishIf(session.Info{ID: "s"}, nil)
					h.removed("s")
				}
			}
		}()
	}
	producers.Wait()
	close(stop)
	wg.Wait()
}

// Sinks receive every activity entry, the session it belongs to and the
// attention state it was handed with, after the clients have the entry, whether
// or not any client listens. Clients get the entry with that state on it.
func TestEventHubSinksGetEveryEntryAfterTheClients(t *testing.T) {
	h := newEventHub()
	type got struct {
		id      string
		e       session.ActivityEntry
		state   session.AttentionState
		clients int
	}
	var sunk []got
	ch := h.subscribe()
	h.addSink(func(id string, e session.ActivityEntry, state session.AttentionState) {
		sunk = append(sunk, got{id, e, state, len(ch)})
	})
	e := session.ActivityEntry{At: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), Type: session.ActivityAttention, Message: "approve?"}
	h.activity("s1", e, session.AttentionNeedsInput)
	if msg := string(<-ch); !strings.Contains(msg, `"state":"needs_input"`) || !strings.Contains(msg, `"message":"approve?"`) {
		t.Fatalf("a client was sent %q", msg)
	}
	h.unsubscribe(ch)
	h.activity("s2", e, "")
	if len(sunk) != 2 || sunk[0] != (got{"s1", e, session.AttentionNeedsInput, 1}) || sunk[1] != (got{"s2", e, "", 0}) {
		t.Fatalf("sunk %+v", sunk)
	}
}

// An attention entry's event carries the state it records, so a client types
// it without waiting for the session change; other entries carry none, and
// neither does an attention entry with a state that is not one of the three.
func TestEventHubActivityCarriesTheAttentionState(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.activity("s1", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "Allow Bash?"}, session.AttentionNeedsInput)
	h.activity("s1", session.ActivityEntry{At: at, Type: session.ActivityProgress, Message: "1/3"}, session.AttentionNeedsInput)
	h.activity("s1", session.ActivityEntry{At: at, Type: session.ActivityAttention, Message: "x"}, "bogus")
	for _, want := range []string{
		`{"sessionId":"s1","at":"2026-10-01T09:00:00Z","type":"attention","message":"Allow Bash?","state":"needs_input"}`,
		`{"sessionId":"s1","at":"2026-10-01T09:00:00Z","type":"progress","message":"1/3"}`,
		`{"sessionId":"s1","at":"2026-10-01T09:00:00Z","type":"attention","message":"x"}`,
	} {
		if got := string(<-ch); got != "event: activity\ndata: "+want+"\n\n" {
			t.Fatalf("got %q, want data %s", got, want)
		}
	}
}

// A run event names the run and nothing more, and is dropped, never cut,
// past its bound.
func TestEventHubRunEvent(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	h.run("api-sweep-0123abcd", false)
	h.run("api-sweep-0123abcd", true)
	h.run(strings.Repeat("x", maxRunEvent), false)
	for _, want := range []string{
		"event: run\ndata: {\"id\":\"api-sweep-0123abcd\"}\n\n",
		"event: run\ndata: {\"id\":\"api-sweep-0123abcd\",\"removed\":true}\n\n",
	} {
		if got := string(<-ch); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if len(ch) != 0 {
		t.Fatalf("an event over %d bytes was sent", maxRunEvent)
	}
}

// A chat message goes to the clients as a `chat` event naming its session or
// its run, as droppable as activity and reaching no sink.
func TestEventHubChatFormat(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	var sunk atomic.Int32
	h.addSink(func(string, session.ActivityEntry, session.AttentionState) { sunk.Add(1) })
	at := time.Date(2026, 10, 6, 8, 32, 0, 0, time.UTC)
	h.chat("s1", "", session.ChatMessage{ID: "m1", At: at, Scope: "session", Kind: "message", By: session.ChatBy{ID: "v1", Name: "Nate", Role: session.RoleControl}, Text: "hello <all>", Nonce: "n1"})
	want := "event: chat\n" +
		`data: {"sessionId":"s1","t":"chat","id":"m1","at":"2026-10-06T08:32:00Z","scope":"session","kind":"message","by":{"id":"v1","name":"Nate","role":"control"},"text":"hello \u003call\u003e","nonce":"n1"}` + "\n\n"
	select {
	case got := <-ch:
		if string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	default:
		t.Fatal("nothing queued")
	}
	h.chat("", "r1", session.ChatMessage{ID: "m2", At: at, Scope: "run", Kind: "sent_to_agent", By: session.ChatBy{ID: "v1", Name: "Nate", Role: session.RoleControl}, Ref: "m1", To: "core"})
	if got := string(<-ch); !strings.HasPrefix(got, "event: chat\ndata: {\"runId\":\"r1\",") || !strings.Contains(got, `"to":"core"`) {
		t.Fatalf("run chat event %q", got)
	}
	if sunk.Load() != 0 {
		t.Fatal("a chat message reached a sink")
	}
}

// publishIf asks whether the session is listed under the hub's lock, which
// removed takes too: a removal that lands while a change is being published
// comes after it, and a change asked about after the removal is dropped. So
// no session event follows a session's removed event.
func TestEventHubPublishIfOrdersAgainstRemoved(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	var listed atomic.Bool
	listed.Store(true)
	entered, release, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(published)
		h.publishIf(session.Info{ID: "s"}, func(string) bool {
			v := listed.Load() // listed as it is asked
			close(entered)
			<-release
			return v
		})
	}()
	<-entered
	// The session leaves the registry, then its removed event goes: it
	// waits for the publication under way, and comes after it.
	listed.Store(false)
	removed := make(chan struct{})
	go func() {
		defer close(removed)
		h.removed("s")
	}()
	select {
	case <-removed:
		t.Fatal("the removed event overtook a publication under way")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-published
	<-removed
	// A change asked about after the removal is dropped.
	h.publishIf(session.Info{ID: "s"}, func(string) bool { return listed.Load() })
	var got []string
	for len(ch) > 0 {
		got = append(got, strings.SplitN(string(<-ch), "\n", 2)[0])
	}
	if strings.Join(got, ",") != "event: session,event: removed" {
		t.Fatalf("events %v, want the session's change, then its removal, then nothing", got)
	}
}

// A dispatch event says what of Dispatch's changed and nothing more: its
// seq, the entity, its id ("" for a person, whose id is its name), and
// the ticket and the project it belongs to. The longest of every entity
// (a ticket's plan revision and launch among them), seq at 19 digits,
// every id at its length, the project's key at 6 and a person's name at
// 60 characters of six encoded bytes each, fits
// maxDispatchEvent: the guard before broadcast drops what is over, and a
// dropped event would be silence.
func TestEventHubDispatchEvent(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	id, ticket := strings.Repeat("f", dispatch.IDLen), strings.Repeat("e", dispatch.IDLen)
	longest := 0
	for _, entity := range []string{dispatch.EntityProject, dispatch.EntityTicket, dispatch.EntityComment, dispatch.EntityLabel,
		dispatch.EntityMilestone, dispatch.EntityView, dispatch.EntityPerson, dispatch.EntityPlan, dispatch.EntityLaunch} {
		c := dispatch.Change{Seq: math.MaxInt64, Entity: entity, ID: id, Ticket: ticket, Project: "ABCDEF"}
		want := dispatchEvent{Seq: math.MaxInt64, Entity: entity, ID: id, Ticket: ticket, Project: "ABCDEF"}
		if entity == dispatch.EntityPerson {
			// A person's change names the person, a name of 60 characters
			// JSON writes as six bytes each; the event carries none of it.
			c.ID = strings.Repeat(string(rune(0x2028)), dispatch.MaxPersonName)
			want.ID = ""
		}
		h.dispatch([]dispatch.Change{c})
		var msg []byte
		select {
		case msg = <-ch:
		default:
			t.Fatalf("no event for a %s", entity)
		}
		data, ok := strings.CutPrefix(string(msg), "event: dispatch\ndata: ")
		if !ok || !strings.HasSuffix(data, "\n\n") || strings.Count(data, "\n") != 2 {
			t.Fatalf("a %s's event is not one event of one data line: %q", entity, msg)
		}
		data = strings.TrimSuffix(data, "\n\n")
		if len(data) > maxDispatchEvent {
			t.Fatalf("a %s's event is %d bytes, more than %d: %s", entity, len(data), maxDispatchEvent, data)
		}
		longest = max(longest, len(data))
		var got dispatchEvent
		if err := json.Unmarshal([]byte(data), &got); err != nil || got != want {
			t.Fatalf("a %s's event %s reads as %+v (%v), want %+v", entity, data, got, err, want)
		}
	}
	// The bound is the longest payload's, with room: a milestone's is 119
	// bytes, as events.go derives it.
	if longest != 119 {
		t.Fatalf("the longest event is %d bytes, not the 119 events.go derives", longest)
	}
	// Every field is there when it is empty: a person's event names no
	// ticket and no project.
	h.dispatch([]dispatch.Change{{Seq: 7, Entity: dispatch.EntityPerson, ID: "Alice"}})
	if got := string(<-ch); got != "event: dispatch\ndata: {\"seq\":7,\"entity\":\"person\",\"id\":\"\",\"ticket\":\"\",\"project\":\"\"}\n\n" {
		t.Fatalf("a person's event: %q", got)
	}
	if len(ch) != 0 {
		t.Fatalf("%d events more than the changes", len(ch))
	}
}

// dispatchData is the data of the dispatch events a queue entry holds, in
// order, failing t unless the entry is such events and nothing else, each
// one event of one data line within maxDispatchEvent.
func dispatchData(t *testing.T, msg []byte) []string {
	t.Helper()
	var out []string
	for rest := string(msg); rest != ""; {
		frame, after, ok := strings.Cut(rest, "\n\n")
		if !ok {
			t.Fatalf("an event with no blank line after it: %q", rest)
		}
		data, ok := strings.CutPrefix(frame, "event: dispatch\ndata: ")
		if !ok || strings.Contains(data, "\n") {
			t.Fatalf("not one dispatch event of one data line: %q", frame)
		}
		if len(data) > maxDispatchEvent {
			t.Fatalf("an event of %d bytes, more than %d: %s", len(data), maxDispatchEvent, data)
		}
		out = append(out, data)
		rest = after
	}
	return out
}

// hexID is the id of the nth entity of a test's commit: 16 hex
// characters, as the store's.
func hexID(n int) string { return fmt.Sprintf("%016x", n) }

// A commit's events are one entry of a client's queue, however many
// there are, in the store's order: a client nobody drains gets a commit
// of 1,000 changes, fifty tickets in each of twenty projects (none past
// maxDispatchEach, so each is an event of its own), as one entry of
// 1,000 events and keeps its stream, which an entry per event would have
// closed at the 257th; the next commit reaches it.
func TestEventHubDispatchCommitIsOneEntry(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	var changes []dispatch.Change
	for i := range 1000 {
		changes = append(changes, dispatch.Change{Seq: 42, Entity: dispatch.EntityTicket, ID: hexID(i), Ticket: hexID(i), Project: fmt.Sprintf("P%02d", i/50)})
	}
	h.dispatch(changes)
	kept := func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		_, ok := h.clients[ch]
		return ok
	}
	if !kept() || len(ch) != 1 {
		t.Fatalf("a commit of %d changes: the client kept: %v, with %d entries queued, want 1", len(changes), kept(), len(ch))
	}
	data := dispatchData(t, <-ch)
	if len(data) != len(changes) {
		t.Fatalf("%d events for %d changes", len(data), len(changes))
	}
	for i, c := range changes {
		var got dispatchEvent
		if err := json.Unmarshal([]byte(data[i]), &got); err != nil || got != (dispatchEvent{Seq: c.Seq, Entity: c.Entity, ID: c.ID, Ticket: c.Ticket, Project: c.Project}) {
			t.Fatalf("event %d is %s (%v), for the change %+v", i, data[i], err, c)
		}
	}
	h.dispatch([]dispatch.Change{{Seq: 43, Entity: dispatch.EntityTicket, ID: hexID(1), Ticket: hexID(1), Project: "CND"}})
	select {
	case msg, open := <-ch:
		if !open {
			t.Fatal("the client's stream was closed")
		}
		if data := dispatchData(t, msg); len(data) != 1 || !strings.HasPrefix(data[0], `{"seq":43,`) {
			t.Fatalf("the next commit's entry: %q", msg)
		}
	default:
		t.Fatal("the next commit queued nothing")
	}
	if !kept() {
		t.Fatal("the client was dropped")
	}
}

// Past maxDispatchEach changes of one kind in one project, a commit tells
// them in one event where the first of them was: that entity, id and
// ticket "", the project's key and the commit's seq. At maxDispatchEach
// each is an event of its own, and kinds and projects are counted apart:
// a project's tickets past the bound leave another project's tickets and
// the project's comments as they are, and a person's event is as ever.
func TestEventHubDispatchTellsALargeKindOfAProjectInOneEvent(t *testing.T) {
	ticket := func(project string, n int) dispatch.Change {
		return dispatch.Change{Seq: 9, Entity: dispatch.EntityTicket, ID: hexID(n), Ticket: hexID(n), Project: project}
	}
	event := func(c dispatch.Change) dispatchEvent {
		return dispatchEvent{Seq: c.Seq, Entity: c.Entity, ID: c.ID, Ticket: c.Ticket, Project: c.Project}
	}
	label := dispatch.Change{Seq: 9, Entity: dispatch.EntityLabel, ID: hexID(0), Project: "CND"}
	one := func(entity, project string) dispatchEvent {
		return dispatchEvent{Seq: 9, Entity: entity, Project: project}
	}
	// A label deleted on as many tickets as the bound, and on one more.
	atBound, atBoundWant := []dispatch.Change{label}, []dispatchEvent{event(label)}
	for i := 1; i <= maxDispatchEach; i++ {
		atBound = append(atBound, ticket("CND", i))
		atBoundWant = append(atBoundWant, event(ticket("CND", i)))
	}
	pastBound := append(slices.Clone(atBound), ticket("CND", maxDispatchEach+1))
	// CND's tickets past the bound between OPS's at it, CND's comments
	// past it, and three people.
	var apart []dispatch.Change
	apartWant := []dispatchEvent{one(dispatch.EntityTicket, "CND")}
	for i := 1; i <= maxDispatchEach+1; i++ {
		apart = append(apart, ticket("CND", i))
		if i <= maxDispatchEach {
			apart = append(apart, ticket("OPS", 1000+i))
			apartWant = append(apartWant, event(ticket("OPS", 1000+i)))
		}
	}
	apartWant = append(apartWant, one(dispatch.EntityComment, "CND"))
	for i := 1; i <= maxDispatchEach+1; i++ {
		apart = append(apart, dispatch.Change{Seq: 9, Entity: dispatch.EntityComment, ID: hexID(2000 + i), Ticket: hexID(1), Project: "CND"})
	}
	for _, name := range []string{"Alice", "Bob", "Carol"} {
		apart = append(apart, dispatch.Change{Seq: 9, Entity: dispatch.EntityPerson, ID: name})
		apartWant = append(apartWant, one(dispatch.EntityPerson, ""))
	}
	for _, tc := range []struct {
		name    string
		changes []dispatch.Change
		want    []dispatchEvent
	}{
		{"at the bound", atBound, atBoundWant},
		{"past the bound", pastBound, []dispatchEvent{event(label), one(dispatch.EntityTicket, "CND")}},
		{"kinds and projects apart", apart, apartWant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEventHub()
			ch := h.subscribe()
			h.dispatch(tc.changes)
			if len(ch) != 1 {
				t.Fatalf("%d entries queued, want 1", len(ch))
			}
			var got []dispatchEvent
			for _, data := range dispatchData(t, <-ch) {
				var ev dispatchEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatal(err)
				}
				got = append(got, ev)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("%d changes told as %d events %+v, want %d %+v", len(tc.changes), len(got), got, len(tc.want), tc.want)
			}
		})
	}
}

// A commit's entry is bounded whatever the commit's size: at most
// maxDispatchEach events of each kind in each project it changed, each at
// most maxDispatchEvent bytes and its framing. Measured at its worst,
// every kind in two projects with each value at its longest, at the
// bound, one past it and at 10,000 of each; and a label deleted on 10,000
// tickets is two events. The client keeps its stream throughout.
func TestEventHubDispatchEntryIsBounded(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe()
	kinds := []string{dispatch.EntityProject, dispatch.EntityTicket, dispatch.EntityComment, dispatch.EntityLabel,
		dispatch.EntityMilestone, dispatch.EntityView, dispatch.EntityPerson}
	projects := []string{"ABCDEF", "GHIJKL"}
	framing := len("event: dispatch\ndata: \n\n")
	bound := len(projects) * len(kinds) * maxDispatchEach * (maxDispatchEvent + framing)
	entry := func(what string, changes []dispatch.Change) []byte {
		t.Helper()
		h.dispatch(changes)
		if len(ch) != 1 {
			t.Fatalf("%s: %d entries queued, want 1", what, len(ch))
		}
		msg := <-ch
		if len(msg) > bound {
			t.Fatalf("%s: an entry of %d bytes, %d events, past the bound of %d", what, len(msg), len(dispatchData(t, msg)), bound)
		}
		return msg
	}
	entries := map[int][]byte{}
	for _, n := range []int{1, maxDispatchEach, maxDispatchEach + 1, 10000} {
		var changes []dispatch.Change
		for _, p := range projects {
			for _, k := range kinds {
				for i := range n {
					changes = append(changes, dispatch.Change{Seq: math.MaxInt64, Entity: k, ID: hexID(i), Ticket: strings.Repeat("e", dispatch.IDLen), Project: p})
				}
			}
		}
		entries[n] = entry(fmt.Sprintf("%d of every kind in two projects", n), changes)
	}
	// The worst is at the bound, every change an event of its own: past
	// it, a kind of a project is one event.
	worst := entries[maxDispatchEach]
	if events := len(dispatchData(t, worst)); events != len(projects)*len(kinds)*maxDispatchEach {
		t.Fatalf("at the bound: %d events, want one per change", events)
	}
	for n, msg := range entries {
		if len(msg) > len(worst) {
			t.Fatalf("%d of every kind in two projects: %d bytes, more than the %d at the bound", n, len(msg), len(worst))
		}
	}
	t.Logf("the worst entry: %d events, %d bytes, of a bound of %d", len(dispatchData(t, worst)), len(worst), bound)
	label := []dispatch.Change{{Seq: 77, Entity: dispatch.EntityLabel, ID: hexID(0), Project: "CND"}}
	for i := 1; i <= 10000; i++ {
		label = append(label, dispatch.Change{Seq: 77, Entity: dispatch.EntityTicket, ID: hexID(i), Ticket: hexID(i), Project: "CND"})
	}
	msg := entry("a label deleted on 10,000 tickets", label)
	if data := dispatchData(t, msg); len(data) != 2 {
		t.Fatalf("a label deleted on 10,000 tickets: %d events, want 2", len(data))
	}
	t.Logf("a label deleted on 10,000 tickets: %d bytes", len(msg))
	h.mu.Lock()
	_, kept := h.clients[ch]
	h.mu.Unlock()
	if !kept {
		t.Fatal("the client was dropped")
	}
}

// A dispatch event is state a page cannot do without, as a run event is:
// a client whose queue is full is dropped and its stream closed, to start
// over from a snapshot, never skipped and kept as an activity entry skips
// it; a client that reads gets the event, a commit's every event in one
// entry. Neither reaches a sink.
func TestEventHubDispatchEventDropsAClientThatCannotKeepUp(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		send        func(h *eventHub)
	}{
		{"dispatch", "dispatch", func(h *eventHub) {
			h.dispatch([]dispatch.Change{{Seq: 1, Entity: dispatch.EntityTicket, ID: "0123456789abcdef", Ticket: "0123456789abcdef", Project: "CND"}})
		}},
		{"dispatch commit", "dispatch", func(h *eventHub) {
			changes := []dispatch.Change{{Seq: 2, Entity: dispatch.EntityLabel, ID: hexID(0), Project: "CND"}}
			for i := 1; i <= eventQueue; i++ {
				changes = append(changes, dispatch.Change{Seq: 2, Entity: dispatch.EntityTicket, ID: hexID(i), Ticket: hexID(i), Project: "CND"})
			}
			h.dispatch(changes)
		}},
		{"run", "run", func(h *eventHub) { h.run("api-sweep-0123abcd", false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEventHub()
			var sunk atomic.Int32
			h.addSink(func(string, session.ActivityEntry, session.AttentionState) { sunk.Add(1) })
			full, reading := h.subscribe(), h.subscribe()
			for len(full) < eventQueue {
				full <- []byte("event: session\ndata: {}\n\n")
			}
			tc.send(h)
			select {
			case msg := <-reading:
				if !strings.HasPrefix(string(msg), "event: "+tc.event+"\n") {
					t.Fatalf("the reading client got %q", msg)
				}
			default:
				t.Fatal("the reading client got nothing")
			}
			if len(reading) != 0 {
				t.Fatalf("the reading client has %d entries more", len(reading))
			}
			h.mu.Lock()
			_, kept := h.clients[full]
			_, readerKept := h.clients[reading]
			h.mu.Unlock()
			if kept || !readerKept {
				t.Fatalf("the full client kept: %v; the reading one kept: %v", kept, readerKept)
			}
			for range eventQueue {
				<-full
			}
			if _, open := <-full; open {
				t.Fatal("the full client's stream was not closed")
			}
			if sunk.Load() != 0 {
				t.Fatalf("a %s event reached a sink", tc.event)
			}
		})
	}
}
