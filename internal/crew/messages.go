package crew

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Messages (#334, row 9): a member of a managed run sends another member
// a message, kept in the ledger and typed into the recipient's session as
// one line ("Message from <from>: <text>") once the recipient's attempt
// has had its task line, through the handoff queue (handoff.go); the
// recipient reads its inbox and acknowledges what it has read. A
// recipient keeps at most maxInbox messages not acknowledged. In a
// managed run a legacy handoff is a message to the member it names.

const (
	maxInbox        = 50
	maxMessageChars = 4000
)

// Message errors.
var (
	ErrInboxFull  = errors.New("the member has 50 messages not acknowledged")
	ErrBadMessage = errors.New("a message is one to 4000 characters")
)

// message is one message of a run, as the engine keeps it.
type message struct {
	seq         int64
	from, to    string
	text        string
	createdAt   time.Time
	deliveredAt time.Time
	ackedAt     time.Time
	// typingAt is when the engine set out to type the message (the intent
	// recorded before the line), deliveredAt when the line was entered.
	typingAt time.Time
	// queued says the message waits in the recipient's handoff queue;
	// uncertain that a continuation found it set out to be typed and not
	// entered before the server ended: it is not typed again (the
	// recipient reads it in its inbox), and says so.
	queued, uncertain bool
}

// Message is a message as the API answers it.
type Message struct {
	Seq         int64      `json:"seq"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Text        string     `json:"text"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	AckedAt     *time.Time `json:"ackedAt,omitempty"`
	// Uncertain says the server ended as the message was being typed: it
	// may or may not have reached the recipient's terminal, and is not
	// typed again.
	Uncertain bool `json:"uncertain,omitempty"`
}

func (m *message) api() Message {
	out := Message{Seq: m.seq, From: m.from, To: m.to, Text: m.text, CreatedAt: m.createdAt, Uncertain: m.uncertain}
	if !m.deliveredAt.IsZero() {
		at := m.deliveredAt
		out.DeliveredAt = &at
	}
	if !m.ackedAt.IsZero() {
		at := m.ackedAt
		out.AckedAt = &at
	}
	return out
}

func (r *run) messageRow(m *message) ledger.MessageRow {
	return ledger.MessageRow{RunID: r.id, Seq: m.seq, From: m.from, To: m.to, Text: m.text, CreatedAt: m.createdAt, TypingAt: m.typingAt, DeliveredAt: m.deliveredAt, AckedAt: m.ackedAt}
}

// messageText is the line a message types, less its carriage return.
func messageText(m *message) string {
	return strings.TrimSpace(fmt.Sprintf("Message from %s (#%d): %s", m.from, m.seq, oneLine.Replace(m.text)))
}

// prompted reports whether the member's session has had its task line: a
// message is typed after it, never before. The attempt in flight decides
// when there is one; with none (its task succeeded, failed or was
// cancelled), the member's session, still alive and idle for inspection,
// takes a message when the attempt it ran was prompted, as an attempt's
// session is (#352: a message sent after the recipient's task succeeded
// waited for a next session that never came). The caller holds e.mu.
func (r *run) prompted(m *member) bool {
	for _, t := range r.tasks {
		if a := t.active(); a != nil && a.m == m {
			return !a.promptedAt.IsZero()
		}
	}
	if m.state.SessionID == "" {
		return false
	}
	for _, t := range r.tasks {
		for _, a := range t.attempts {
			if a.m == m && a.sessionID == m.state.SessionID && !a.promptedAt.IsZero() {
				return true
			}
		}
	}
	return false
}

// unacked counts the messages to the member not acknowledged. The caller
// holds e.mu.
func (r *run) unacked(to string) int {
	n := 0
	for _, m := range r.messages {
		if m.to == to && m.ackedAt.IsZero() {
			n++
		}
	}
	return n
}

// SendMessage sends text from the session's member to the member named,
// in a managed run: kept, then typed into the recipient's session once it
// has its task line. ErrInboxFull once the recipient has maxInbox
// messages not acknowledged; ErrLane, nothing written, when the two are in
// two lanes of a comparison.
func (e *Engine) SendMessage(ctx context.Context, sessionID, to, text string) (Message, error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return Message{}, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	return e.send(ctx, sm.r, sm.m, to, text)
}

// send is SendMessage from a member the engine knows.
func (e *Engine) send(ctx context.Context, r *run, from *member, to, text string) (Message, error) {
	// Cleaned and bounded, in characters and in the bytes the ledger
	// keeps, before anything changes: the line is typed into a terminal.
	text = cleanText(text)
	if text == "" || utf8.RuneCountInString(text) > maxMessageChars || len(text) > ledger.MaxMessageText {
		return Message{}, ErrBadMessage
	}
	e.mu.Lock()
	if e.runs[r.id] != r {
		e.mu.Unlock()
		return Message{}, ErrRunNotFound
	}
	if !r.managed {
		e.mu.Unlock()
		return Message{}, fmt.Errorf("%w: a legacy run passes work by handoffs", ErrTaskState)
	}
	toM := r.member(to)
	switch {
	case toM == nil:
		e.mu.Unlock()
		return Message{}, ErrMemberNotFound
	case toM == from:
		e.mu.Unlock()
		return Message{}, fmt.Errorf("%w: a message to oneself", ErrBadMessage)
	case r.laneOf(from) != r.laneOf(toM):
		e.mu.Unlock()
		return Message{}, fmt.Errorf("%w: %s is in another lane of this comparison: a message stays in its lane", ErrLane, to)
	case r.stoppedAt != nil:
		e.mu.Unlock()
		return Message{}, ErrRunStopped
	}
	e.mu.Unlock()
	var m *message
	err := e.transact(ctx, r, func() tx {
		if r.unacked(to) >= maxInbox {
			return tx{}
		}
		var last int64
		for _, x := range r.messages {
			last = max(last, x.seq)
		}
		m = &message{seq: last + 1, from: from.def.Name, to: to, text: text, createdAt: time.Now().UTC()}
		r.messages = append(r.messages, m)
		r.note(session.ActivityStatus, "message %d from %s to %s: %s", m.seq, m.from, m.to, oneLine.Replace(shortText(text)))
		w := ev("message.sent", map[string]any{"seq": m.seq, "from": m.from, "to": m.to})
		w.more = func(ltx LedgerTx) error { return ltx.PutMessage(r.messageRow(m)) }
		return w
	}, func() {
		if m != nil {
			if i := len(r.messages) - 1; i >= 0 && r.messages[i] == m {
				r.messages = r.messages[:i]
			}
		}
	})
	if err != nil {
		e.afterTransact(r, err)
		return Message{}, err
	}
	if m == nil {
		return Message{}, ErrInboxFull
	}
	// The message as sent, read under e.mu before its delivery starts: a
	// recipient that takes it at once (#352) records it delivered meanwhile.
	e.mu.Lock()
	out := m.api()
	e.mu.Unlock()
	e.deliverMessages(r, toM)
	return out, nil
}

// shortText is text cut to a note's length.
func shortText(text string) string {
	if len(text) > 120 {
		return text[:117] + "…"
	}
	return text
}

// deliverMessages queues the recipient's messages not yet delivered in
// its handoff queue and starts their delivery into its session, when it
// has one (the member's attempt launched); else they wait for the next
// dispatch, which calls this again. The caller does not hold e.mu.
func (e *Engine) deliverMessages(r *run, to *member) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runs[r.id] != r || r.stopping || r.ctx.Err() != nil {
		return
	}
	var local *session.Local
	if to.state.SessionID != "" && to.state.Status != MemberEnded {
		if l, ok := e.lookup(to.state.SessionID); ok && !l.Info().Status.Ended() {
			local = l
		}
	}
	if local == nil {
		return
	}
	queued := 0
	for _, m := range r.messages {
		if m.to != to.def.Name || !m.deliveredAt.IsZero() || m.queued || m.uncertain {
			continue
		}
		if len(to.handoffs) >= maxHandoffs {
			break // the rest once the queue drains: deliver asks again
		}
		m.queued = true
		to.handoffs = append(to.handoffs, handoff{from: m.from, text: messageText(m), seq: m.seq})
		queued++
	}
	if queued == 0 {
		return
	}
	if to.delivering.Load() {
		to.poke()
		return
	}
	to.delivering.Store(true)
	r.deliveries.Add(1)
	go e.deliver(r, to, local)
}

// typing records that the engine sets out to type the message into its
// recipient's session (deliver), before the line: the intent, so that a
// line the server's end cuts off is told from one never typed. It reports
// whether the record is in; refused, the message waits for the next
// delivery. The caller does not hold e.mu.
func (e *Engine) typing(r *run, seq int64) bool {
	var set *message
	err := e.transact(context.Background(), r, func() tx {
		m := r.messageBySeq(seq)
		if m == nil || !m.deliveredAt.IsZero() {
			return tx{}
		}
		if !m.typingAt.IsZero() {
			return tx{} // set out before, within this run: typed again as a handoff is
		}
		m.typingAt = time.Now().UTC()
		set = m
		w := ev("message.typing", map[string]any{"seq": m.seq, "from": m.from, "to": m.to})
		w.more = func(ltx LedgerTx) error { return ltx.PutMessage(r.messageRow(m)) }
		return w
	}, func() {
		// Not recorded: the intent is not set, so the next delivery records
		// it before it types.
		if set != nil {
			set.typingAt = time.Time{}
		}
	})
	if err != nil {
		e.mu.Lock()
		r.unqueued(seq)
		e.mu.Unlock()
		e.afterTransact(r, err)
		return false
	}
	return true
}

// delivered records that the message was typed into its recipient's
// session, once the line is entered (deliver). The caller does not hold
// e.mu.
func (e *Engine) delivered(r *run, seq int64) {
	err := e.transact(context.Background(), r, func() tx {
		m := r.messageBySeq(seq)
		if m == nil || !m.deliveredAt.IsZero() {
			return tx{}
		}
		m.deliveredAt, m.queued = time.Now().UTC(), false
		r.note(session.ActivityStatus, "message %d from %s delivered to %s", m.seq, m.from, m.to)
		w := ev("message.delivered", map[string]any{"seq": m.seq, "from": m.from, "to": m.to})
		w.more = func(ltx LedgerTx) error { return ltx.PutMessage(r.messageRow(m)) }
		return w
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
	}
}

// untyped records that the line was not typed after all: the recipient's
// session ended under it, or refused it, before any of it went in. The
// typing record is withdrawn, so that a continuation does not hold the
// message uncertain, and with the session gone (requeue) the message waits
// for the recipient's next session, queued there as its dispatch asks. The
// caller does not hold e.mu.
func (e *Engine) untyped(r *run, seq int64, requeue bool) {
	to := ""
	err := e.transact(context.Background(), r, func() tx {
		m := r.messageBySeq(seq)
		if m == nil || !m.deliveredAt.IsZero() || m.typingAt.IsZero() {
			return tx{}
		}
		m.typingAt = time.Time{}
		if requeue {
			m.queued, to = false, m.to
		}
		w := ev("message.untyped", map[string]any{"seq": m.seq, "from": m.from, "to": m.to})
		w.more = func(ltx LedgerTx) error { return ltx.PutMessage(r.messageRow(m)) }
		return w
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
		return
	}
	if to == "" {
		return
	}
	// The recipient's next session may be up already (its dispatch asked
	// before this record): the message is queued into it now, else when
	// that dispatch asks.
	e.mu.Lock()
	m := r.member(to)
	e.mu.Unlock()
	if m != nil {
		e.deliverMessages(r, m)
	}
}

// unqueued takes a message back out of the queue it waited in without
// being typed (the recipient's session ended, the run stopped): it waits
// for the recipient's next session. The caller holds e.mu.
func (r *run) unqueued(seq int64) {
	if m := r.messageBySeq(seq); m != nil {
		m.queued = false
	}
}

func (r *run) messageBySeq(seq int64) *message {
	for _, m := range r.messages {
		if m.seq == seq {
			return m
		}
	}
	return nil
}

// Inbox is the session's member's messages after seq, at most maxInbox,
// in order, delivered or not.
func (e *Engine) Inbox(sessionID string, after int64) ([]Message, error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return nil, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !sm.r.managed {
		return nil, fmt.Errorf("%w: a legacy run has no inbox", ErrTaskState)
	}
	out := []Message{}
	for _, m := range sm.r.messages {
		if m.to != sm.m.def.Name || m.seq <= after {
			continue
		}
		out = append(out, m.api())
		if len(out) == maxInbox {
			break
		}
	}
	return out, nil
}

// Ack acknowledges the session's member's messages up to seq: they count
// no more against its inbox. The number acknowledged now is returned.
func (e *Engine) Ack(ctx context.Context, sessionID string, upTo int64) (int, error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return 0, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	r := sm.r
	e.mu.Lock()
	managed := r.managed
	e.mu.Unlock()
	if !managed {
		return 0, fmt.Errorf("%w: a legacy run has no inbox", ErrTaskState)
	}
	n := 0
	var acked []*message
	err := e.transact(ctx, r, func() tx {
		now := time.Now().UTC()
		for _, m := range r.messages {
			if m.to == sm.m.def.Name && m.seq <= upTo && m.ackedAt.IsZero() {
				m.ackedAt = now
				acked = append(acked, m)
			}
		}
		n = len(acked)
		if n == 0 {
			return tx{}
		}
		w := ev("message.acked", map[string]any{"member": sm.m.def.Name, "upTo": upTo, "count": n})
		w.more = func(ltx LedgerTx) error {
			for _, m := range acked {
				if err := ltx.PutMessage(r.messageRow(m)); err != nil {
					return err
				}
			}
			return nil
		}
		return w
	}, func() {
		for _, m := range acked {
			m.ackedAt = time.Time{}
		}
	})
	if err != nil {
		e.afterTransact(r, err)
		return 0, err
	}
	return n, nil
}

// MessagesOf is a run's messages, for a run read (the API) and tests.
func (e *Engine) MessagesOf(runID string) ([]Message, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return nil, ErrRunNotFound
	}
	out := []Message{}
	for _, m := range r.messages {
		out = append(out, m.api())
	}
	return out, nil
}
