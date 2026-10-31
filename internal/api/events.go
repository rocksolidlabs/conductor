package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

const (
	eventQueue     = 256
	eventPingEvery = 20 * time.Second
	// activityQueueLimit is how full a client's queue may be before activity
	// entries skip it. Session changes and removals carry state a client
	// cannot do without, and a client that misses one is dropped and starts
	// over from a snapshot; the entries are a live feed a client may miss
	// some of. So a burst of entries never uses the last quarter of the queue.
	activityQueueLimit = eventQueue - eventQueue/4
)

// eventHub fans session changes out to Server-Sent Events clients, and
// activity entries to its sinks as well.
type eventHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	// sinks receive every activity entry after the clients: the webhooks and
	// the crew runs.
	sinks []activitySink
}

// activitySink receives an activity entry of a session and, for an attention
// entry, the attention state it records, which comes with the entry: set with
// the entry's stamp on a server session, sent by the host on a hosted one; ""
// when it is not known (an older host sends none, and a state that is not one
// of the three is dropped).
type activitySink func(sessionID string, e session.ActivityEntry, state session.AttentionState)

func newEventHub() *eventHub { return &eventHub{clients: map[chan []byte]struct{}{}} }

func (h *eventHub) subscribe() chan []byte {
	ch := make(chan []byte, eventQueue)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// publishIf queues a session event, for a session that listed still says
// the server lists (nil: always), asked under the hub's lock, which removed
// takes too; a client that cannot keep up is dropped.
// A session leaves the registry before its removed event, so a change
// computed before it left (a viewer leaving it) cannot land after that
// event and bring it back to the browsers' lists.
func (h *eventHub) publishIf(info session.Info, listed func(id string) bool) {
	b, err := json.Marshal(info)
	if err != nil {
		return
	}
	msg := []byte("event: session\ndata: " + string(b) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	if listed != nil && !listed(info.ID) {
		return
	}
	h.sendSessionLocked(msg)
}

// publishLatest queues a server session's change as the session is now: its
// snapshot is taken under the hub's lock (current, which says false for a
// session the server no longer lists, as publishIf's listed does), so one
// session's events leave in the order their snapshots were taken. A
// snapshot taken before a change (a viewer's, read as a rename lands) and
// handed over after it can then never go out after the change's and undo it
// in the browsers, which hold the last session event they heard. current
// takes the session's lock under the hub's: a session calls its hooks
// without its lock held, and the registry never calls the hub under its
// own.
func (h *eventHub) publishLatest(id string, current func(id string) (session.Info, bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	info, ok := current(id)
	if !ok {
		return
	}
	b, err := json.Marshal(info)
	if err != nil {
		return
	}
	h.sendSessionLocked([]byte("event: session\ndata: " + string(b) + "\n\n"))
}

// sendSessionLocked queues a session event for every client, under h.mu; a
// client that cannot keep up is dropped.
func (h *eventHub) sendSessionLocked(msg []byte) {
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
}

// activityEvent is the data of an `activity` event: the entry, the session it
// belongs to and, for an attention entry, the attention state it records.
type activityEvent struct {
	SessionID string `json:"sessionId"`
	session.ActivityEntry
	// State is the attention state an attention entry records: needs_input,
	// working or done. It is absent for any other entry, and for an attention
	// entry that came without one: from an older host, which sends no state,
	// or from a host whose state was not one of the three, which the server
	// drops.
	State session.AttentionState `json:"state,omitempty"`
}

// eventState is what an activity event says an entry records: state, for an
// attention entry and one of the three states (isAttentionState); nothing
// otherwise.
func eventState(e session.ActivityEntry, state session.AttentionState) session.AttentionState {
	if e.Type == session.ActivityAttention && isAttentionState(state) {
		return state
	}
	return ""
}

// addSink makes f receive every activity entry, after the clients have it.
// f runs where activity does, on the goroutine that recorded the entry: it
// must never wait, and must be safe for concurrent use.
func (h *eventHub) addSink(f activitySink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sinks = append(h.sinks, f)
}

// activity queues an activity entry of a session for every client, with the
// attention state an attention entry records (eventState), then hands it to
// every sink with state ("" when it is not known). It is the OnActivity hook
// of every session, server and hosted alike, so it runs on the goroutines that
// record, concurrently and out of order (each entry says when it happened),
// and it never waits: a client whose queue is past activityQueueLimit misses
// the entry and keeps its stream.
func (h *eventHub) activity(sessionID string, e session.ActivityEntry, state session.AttentionState) {
	b, err := json.Marshal(activityEvent{SessionID: sessionID, ActivityEntry: e, State: eventState(e, state)})
	h.mu.Lock()
	if err == nil {
		msg := []byte("event: activity\ndata: " + string(b) + "\n\n")
		for ch := range h.clients {
			if len(ch) >= activityQueueLimit {
				continue
			}
			select {
			case ch <- msg:
			default:
			}
		}
	}
	sinks := h.sinks
	h.mu.Unlock()
	for _, f := range sinks {
		f(sessionID, e, state)
	}
}

// removed announces that a session left the registry.
// chatEvent is the data of a `chat` event: a chat message as kept
// (proto.ChatMessage) and the session or the run whose chat it is.
type chatEvent struct {
	SessionID string `json:"sessionId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	proto.ChatMessage
}

// chat queues a chat message of a session (sessionID) or a run (runID) for
// every client, as droppable as activity: a client past activityQueueLimit
// misses it and keeps its stream. It is the OnChat hook of every server
// session and the OnRunChat hook of the run engine, so it runs on the posting
// goroutines and never waits. Chat reaches no sink: no webhook or feed.
func (h *eventHub) chat(sessionID, runID string, m session.ChatMessage) {
	b, err := json.Marshal(chatEvent{SessionID: sessionID, RunID: runID, ChatMessage: session.ChatToProto(m)})
	if err != nil {
		return
	}
	msg := []byte("event: chat\ndata: " + string(b) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		if len(ch) >= activityQueueLimit {
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}

func (h *eventHub) removed(id string) {
	h.broadcast([]byte("event: removed\ndata: " + fmt.Sprintf("{\"id\":%q}", id) + "\n\n"))
}

// runEvent is the data of a `run` event: the run to read again, or, with
// Removed, the run the server forgot. It carries no more than the ID, so it
// is at most maxRunEvent bytes whatever happened to the run.
type runEvent struct {
	ID      string `json:"id"`
	Removed bool   `json:"removed,omitempty"`
}

// maxRunEvent bounds a run event's data: a run ID is a crew ID (at most 40
// characters, crew.ValidID) and 9 more.
const maxRunEvent = 128

// run announces that a run changed in a way no session change carries
// (crew.Engine.OnRunChange), or, removed, that the engine forgot it
// (OnForget). Like a session change it is state a client cannot do without: a
// client that cannot keep up is dropped and starts over from a snapshot. It
// runs under the engine's lock and never waits.
func (h *eventHub) run(id string, removed bool) {
	b, err := json.Marshal(runEvent{ID: id, Removed: removed})
	if err != nil || len(b) > maxRunEvent {
		return
	}
	h.broadcast([]byte("event: run\ndata: " + string(b) + "\n\n"))
}

// dispatchEvent is the data of a `dispatch` event: something of Dispatch's
// changed (dispatch.Change), for a page to read again. seq is the commit's
// last change in the store's log; entity what changed (project, ticket,
// comment, label, milestone, view, person, plan or launch: a ticket's
// plan revision or launch) and id its id, "" for a
// person, whose id is its name: the directory is small and its pickers
// read it again whole, so the event says only that it changed; ticket the
// id of the ticket the change belongs to and project its project's key,
// "" where there is none. The one event for a commit's changes of a kind
// in a project past maxDispatchEach has id and ticket "" too.
type dispatchEvent struct {
	Seq     int64  `json:"seq"`
	Entity  string `json:"entity"`
	ID      string `json:"id"`
	Ticket  string `json:"ticket"`
	Project string `json:"project"`
}

// maxDispatchEvent bounds a dispatch event's data. The longest is 119
// bytes: seq at most 19 digits, entity at most 9 (milestone; a plan's
// and a launch's are shorter), id and ticket 16 hex characters each,
// project a key of at most 6, and the envelope's 53; every value is
// digits, a word of the code's, hex or a key, never escaped. A person's
// name, which could be 60 characters of six encoded bytes each, never
// travels (id is "" for a person).
const maxDispatchEvent = 160

// maxDispatchEach is the most changes of one kind in one project that a
// commit's events tell one by one. Past it they are told in one event,
// where the first of them was: that entity, id and ticket "", the
// project's key and the commit's seq, which a page takes as it takes any
// event of its project that names something else, reading again what it
// shows of the project (docs/protocol.md).
//
// It bounds a commit's queue entry, which otherwise grows with the
// commit: a label deleted on 10,000 tickets was 10,001 events, over a
// megabyte, in every client's queue. An entry holds at most
// maxDispatchEach events of each kind in each project the commit
// changed, each at most maxDispatchEvent bytes of data and 24 of
// framing: at most 11,776 bytes a kind and project, whatever the
// commit's size. An operation of the store changes entities of one
// project at most, beside the people its tickets name, so nine kinds at
// most (a plan revision's and a launch's among them), and its entry is at
// most 105,984 bytes; the label deleted on 10,000 tickets is two events.
const maxDispatchEach = 64

// dispatch announces a commit of Dispatch's store, what it changed
// (dispatch.Store.OnChange): one event per entity, in the store's order,
// but one event for a kind's changes in a project past maxDispatchEach;
// all of it queued for every client as one entry. So a client gets a
// commit's events together and in order, and its queue counts commits,
// not entities: it is dropped when it is eventQueue entries behind, never
// because one commit changed more entities than that (a label's delete
// leaves every ticket that had it). It goes the run event's way: state a
// page cannot do without, so a client that cannot keep up is dropped and
// starts over from a snapshot, never skipped and kept as activity is;
// and to the clients alone, never a sink's (no webhook, no run engine).
// It runs on the committing goroutine, after the commit, and never
// waits. An event past maxDispatchEvent is dropped, never cut: none is,
// the bound being the longest one's.
func (h *eventHub) dispatch(changes []dispatch.Change) {
	type kind struct{ entity, project string }
	counts := map[kind]int{}
	for _, c := range changes {
		counts[kind{c.Entity, c.Project}]++
	}
	told := map[kind]bool{}
	var msg []byte
	for _, c := range changes {
		k := kind{c.Entity, c.Project}
		ev := dispatchEvent{Seq: c.Seq, Entity: c.Entity, ID: c.ID, Ticket: c.Ticket, Project: c.Project}
		switch {
		case counts[k] > maxDispatchEach:
			if told[k] {
				continue
			}
			told[k] = true
			ev.ID, ev.Ticket = "", ""
		case c.Entity == dispatch.EntityPerson:
			ev.ID = ""
		}
		b, err := json.Marshal(ev)
		if err != nil || len(b) > maxDispatchEvent {
			continue
		}
		msg = append(msg, "event: dispatch\ndata: "...)
		msg = append(msg, b...)
		msg = append(msg, "\n\n"...)
	}
	if len(msg) > 0 {
		h.broadcast(msg)
	}
}

// broadcast queues msg, one event or a commit's several, as one entry for
// every client; a client that cannot keep up is dropped.
func (h *eventHub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
}

// handleEvents streams session changes as Server-Sent Events. The admin
// token must arrive in the Authorization header (the browser reads the
// stream with fetch, not EventSource) so it never appears in a URL.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authenticate(r, false).admin {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "workbench token required")
		return
	}
	// The middleware wraps the writer; ResponseController reaches the flusher
	// through Unwrap.
	rc := http.NewResponseController(w)
	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	snapshot, _ := json.Marshal(s.registry.List())
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snapshot); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	ping := time.NewTicker(eventPingEvery)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			_ = rc.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}
