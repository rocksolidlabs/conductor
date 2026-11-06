package crew

import (
	"errors"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// A handoff a member of a run reports (a session.ActivityHandoff entry) is
// typed into the member of the run it names, as one line, once that member
// runs and does not wait for input. One goroutine per member types the
// handoffs waiting for it (deliver). It looks again when the member's session
// records an entry (OnActivity), changes (OnChange: a prompt cleared records
// no entry) or the member starts running, never on a timer.

// maxHandoffs bounds the handoffs waiting for a member: another drops the
// oldest.
const maxHandoffs = 10

// handoff is a handoff waiting to be typed into a member: from names the
// member that reported it, text is the line typed, less its carriage return.
// queued is set once the run log says it waits.
type handoff struct {
	from, text string
	queued     bool
	// seq is the message the handoff carries in a managed run (messages.go),
	// 0 for a legacy handoff: entered, the message is recorded delivered;
	// dropped, it waits for the recipient's next session.
	seq int64
}

// oneLine makes one line of what the engine types, a handoff's message or a
// role prompt: the session keeps line breaks and tabs in a message, a crew
// keeps them in a prompt as written, and a line break or a carriage return
// typed would end the line early. Each becomes one space, so the line is as
// long as the text.
var oneLine = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")

// handoffText is the line a handoff types, less its carriage return. The
// message is at most session.MaxAttentionMessage bytes, as the session that
// recorded it cleaned it, so the line fits what a session takes at once.
func handoffText(from, message string) string {
	return strings.TrimSpace("Handoff from " + from + ": " + oneLine.Replace(message))
}

// OnChange takes the changes of every server session, as the server's change
// hook hands them. The change of a member's session wakes the handoffs
// waiting for that member: a prompt cleared, by the agent or an admin,
// records no entry, only a change. A session outside the runs costs nothing.
// It also keeps the agent session the change carries on the member.
func (e *Engine) OnChange(info session.Info) {
	if info.Crew == nil {
		return
	}
	v, ok := e.bySession.Load(info.ID)
	if !ok {
		return
	}
	sm := v.(sessionMember)
	if info.AgentSession != nil {
		// Kept on the member, for a resume once the session has left the
		// server.
		e.mu.Lock()
		if sm.m.state.SessionID == info.ID {
			as := *info.AgentSession
			was := sm.m.state.AgentSession
			sm.m.state.AgentSession = &as
			if was == nil || *was != as {
				// A change of the run: its record follows the conversation
				// a resume from it would continue (the journal).
				sm.r.touch()
			}
		}
		e.mu.Unlock()
	}
	sm.m.poke()
	// The last member's session ending finishes the run.
	if info.Status.Ended() {
		e.noteEnd(sm.r.id)
	}
}

// poke wakes the goroutine typing m's handoffs, if one runs. It takes no lock.
func (m *member) poke() {
	if !m.delivering.Load() {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// handoff takes a handoff that from, a member of r, reports in entry: it
// waits for the member entry.To names and is typed into it (deliver). At most
// maxHandoffs wait for a member: another drops the oldest. A handoff to a name
// no member of r has, to from itself, or to a member with no session or whose
// session has ended, is only noted. A stopping run takes none. The caller
// holds e.mu; it reads the target's session, which never calls into the engine
// while it holds its lock.
func (e *Engine) handoff(r *run, from *member, entry session.ActivityEntry) {
	if r.stopping {
		return
	}
	to := r.member(entry.To)
	switch {
	case to == nil:
		r.note(session.ActivityError, "handoff to unknown member %s from %s", quote(entry.To), from.def.Name)
		return
	case to == from:
		r.note(session.ActivityError, "handoff to itself from %s", from.def.Name)
		return
	}
	var local *session.Local
	if to.state.SessionID != "" && to.state.Status != MemberEnded {
		if l, ok := e.lookup(to.state.SessionID); ok && !l.Info().Status.Ended() {
			local = l
		}
	}
	if local == nil {
		r.note(session.ActivityError, "handoff to a member that is not running, from %s to %s", from.def.Name, to.def.Name)
		return
	}
	if len(to.handoffs) >= maxHandoffs {
		r.dropOldest(to)
	}
	to.handoffs = append(to.handoffs, handoff{from: from.def.Name, text: handoffText(from.def.Name, entry.Message)})
	if to.delivering.Load() {
		to.poke()
		return
	}
	to.delivering.Store(true)
	r.deliveries.Add(1)
	go e.deliver(r, to, local)
}

// dropOldest drops the oldest handoff waiting for m, to make room. The caller
// holds e.mu.
func (r *run) dropOldest(m *member) {
	r.note(session.ActivityError, "handoff dropped from %s to %s: %d already waiting", m.handoffs[0].from, m.def.Name, maxHandoffs)
	m.handoffs = slices.Delete(m.handoffs, 0, 1)
}

// noteQueued notes, once each, the handoffs waiting for m that the log has
// not said wait, with why they wait. The caller holds e.mu.
func (r *run) noteQueued(m *member, why string) {
	for i := range m.handoffs {
		if h := &m.handoffs[i]; !h.queued {
			h.queued = true
			r.note(session.ActivityStatus, "handoff queued from %s to %s: %s %s", h.from, m.def.Name, m.def.Name, why)
		}
	}
}

// deliver submits the handoffs waiting for m into l, its session, in order,
// once m runs, one at a time with session.Local.Submit (UnlessWaiting: a
// paste, then Enter): a handoff l takes while it waits for input
// goes back to the head of the queue, and every handoff waiting is noted as
// queued. Otherwise it waits to be woken (poke). It ends once none waits;
// when the run stops, or l or m ends, the handoffs left are dropped, each
// noted. It never holds e.mu while it types.
func (e *Engine) deliver(r *run, m *member, l *session.Local) {
	defer r.deliveries.Done()
	for {
		e.mu.Lock()
		if len(m.handoffs) == 0 && len(m.answers) == 0 {
			m.stopDelivering()
			e.mu.Unlock()
			// Messages past the queue's room, if any, are queued now
			// (deliverMessages starts a delivery of its own for them).
			e.deliverMessages(r, m)
			return
		}
		why := ""
		switch {
		case r.ctx.Err() != nil:
			why = "the run is stopped"
		case m.state.Status == MemberEnded || isClosed(l.Ended()):
			why = m.def.Name + " is not running"
		}
		if why != "" {
			for _, h := range m.handoffs {
				if h.seq > 0 {
					// A message waits for the recipient's next session.
					r.unqueued(h.seq)
					continue
				}
				r.note(session.ActivityError, "handoff dropped from %s to %s: %s", h.from, m.def.Name, why)
			}
			for _, al := range m.answers {
				r.note(session.ActivityError, "decision %s's answer was not typed into %s: %s", al.d.id(), m.def.Name, why)
			}
			m.handoffs, m.answers = nil, nil
			m.stopDelivering()
			e.mu.Unlock()
			return
		}
		if m.state.Status != MemberRunning || (r.managed && !r.prompted(m)) {
			r.noteQueued(m, "has not had its prompt yet")
			e.mu.Unlock()
			waitWake(r, m, l)
			continue
		}
		if len(m.answers) > 0 {
			// A decision's answer goes ahead of every handoff: the one whose
			// mirror is the question shown first, then the others in their
			// order, each typed past another decision's mirror
			// (decisions.go: nextAnswer, typeAnswer).
			e.mu.Unlock()
			if e.beforeAnswer != nil {
				e.beforeAnswer(m.def.Name)
			}
			if al, mirror := e.nextAnswer(r, m, l); al != nil {
				if e.afterLook != nil {
					e.afterLook(m.def.Name, mirror)
				}
				if e.typeAnswer(r, m, l, al, mirror) {
					waitWake(r, m, l)
				}
			}
			continue
		}
		h := m.handoffs[0]
		m.handoffs = slices.Delete(m.handoffs, 0, 1)
		e.mu.Unlock()
		if h.seq > 0 && !e.typing(r, h.seq) {
			continue // not recorded: the message waits for the next delivery
		}

		res, err := l.Submit(r.ctx, session.Submission{Text: h.text, ByName: typedBy, UnlessWaiting: true, Finished: func(res session.SubmitResult, err error) {
			// A handoff accepted while the program took it slowly
			// (session.ErrSubmitPending) is told here how it ended, as
			// one that ends at once is below.
			e.mu.Lock()
			defer e.mu.Unlock()
			r.noteHandoffEnd(h, m.def.Name, res, err)
			switch {
			case h.seq > 0 && res.Entered:
				go e.delivered(r, h.seq)
			case h.seq > 0 && !res.Typed:
				go e.untyped(r, h.seq, gone(r, l, err))
			}
		}})
		if e.tried != nil {
			e.tried(m.def.Name, res.Typed)
		}
		e.mu.Lock()
		switch {
		case errors.Is(err, session.ErrSubmitPending):
			r.note(session.ActivityStatus, "handoff from %s to %s waits for %s to take its input: it is entered once it has", h.from, m.def.Name, m.def.Name)
		case err != nil && !res.Typed:
			r.note(session.ActivityError, "handoff dropped from %s to %s: %v", h.from, m.def.Name, err)
			if h.seq > 0 {
				go e.untyped(r, h.seq, gone(r, l, err))
			}
		case err != nil:
			r.note(session.ActivityError, "handoff from %s to %s typed without its Enter: %v", h.from, m.def.Name, err)
		case res.Entered:
			r.noteHandoff(h.from, m.def.Name)
			if h.seq > 0 {
				go e.delivered(r, h.seq)
			}
		case res.Typed:
			// A question came up during the pause: the text waits in the
			// agent's input, and is never typed again.
			r.note(session.ActivityError, "handoff from %s to %s typed without its Enter: %s waits on a question; answer it, then press Enter in its terminal", h.from, m.def.Name, m.def.Name)
		default:
			// Waiting for input: back to the head, the oldest again.
			m.handoffs = slices.Insert(m.handoffs, 0, h)
			if len(m.handoffs) > maxHandoffs {
				r.dropOldest(m)
			}
			r.noteQueued(m, "is waiting for input")
		}
		e.mu.Unlock()
		if err == nil && !res.Typed {
			waitWake(r, m, l)
		}
	}
}

// gone says the session l a line was not typed into is no longer one to
// type into: it ended, the submission said so, or the run is stopping. A
// message dropped so waits for the recipient's next session; one a live
// session refused is dropped as a handoff is, and noted.
func gone(r *run, l *session.Local, err error) bool {
	return isClosed(l.Ended()) || errors.Is(err, session.ErrSessionEnded) || r.ctx.Err() != nil
}

// noteHandoffEnd notes how the handoff h to the member named to ended once
// its submission, accepted while the program took it slowly, has finished:
// delivered with its Enter, typed without it, or not typed. The caller
// holds e.mu.
func (r *run) noteHandoffEnd(h handoff, to string, res session.SubmitResult, err error) {
	switch {
	case res.Entered:
		r.noteHandoff(h.from, to)
	case !res.Typed:
		r.note(session.ActivityError, "handoff dropped from %s to %s: %v", h.from, to, err)
	case err != nil:
		r.note(session.ActivityError, "handoff from %s to %s typed without its Enter: %v", h.from, to, err)
	default:
		r.note(session.ActivityError, "handoff from %s to %s typed without its Enter: %s waits on a question; answer it, then press Enter in its terminal", h.from, to, to)
	}
}

// stopDelivering marks that no goroutine types m's handoffs any more, and
// forgets a wake meant for it. The caller holds e.mu.
func (m *member) stopDelivering() {
	m.delivering.Store(false)
	select {
	case <-m.wake:
	default:
	}
}

// waitWake waits until m's handoffs may go: a wake, the run stopping or l
// ending.
func waitWake(r *run, m *member, l *session.Local) {
	select {
	case <-r.ctx.Done():
	case <-l.Ended():
	case <-m.wake:
	}
}

// isClosed reports whether c is closed, without waiting.
func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
