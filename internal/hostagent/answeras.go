package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Routed answers (answer_as, the decision seam, Program item C5): the
// owner's own server, where a decision router or a provider holds an answer
// for the question this host's session shows, sends it over the control
// connection, and the host applies it as its own viewers' answers are
// applied: Local.AnswerAs for a choice's keys (only one of the question's
// own choices is ever written), Local.Submit with the question for a line,
// under the question's claim, the first answer winning. The message
// carries who answered, never a right to answer: a switchyard is never
// told the host takes them (registration) and what one sends is dropped
// unread (handleControl).
//
// They are applied off the control connection's read loop, on one queue
// per connection: at most answerQueue held (the one applying among them),
// a later one dropped and logged (the first answer wins, so a dropped later
// one loses nothing a person could not redo), one goroutine applying them
// in order, each within submitDeadline() from its arrival (the deadline a
// viewer's reply has), so that answers waiting behind a program that reads
// nothing hold at most answerQueue, never a goroutine each. The queue and
// its goroutine end with the connection, a message waiting then dropped.

// answerQueue bounds the routed answers a control connection holds.
const answerQueue = 4

// routedAnswers is the answer queue of one control connection.
type routedAnswers struct {
	q   requestQueue
	ctx context.Context
}

// openAnswers gives the control connection whose context is ctx its answer
// queue, and returns what ends it: the queue closed (a message waiting
// dropped), the one applying ended through its context, and its goroutine
// waited for.
func (a *agent) openAnswers(ctx context.Context) func() {
	actx, cancel := context.WithCancel(ctx)
	ra := &routedAnswers{q: requestQueue{limit: answerQueue - 1}, ctx: actx}
	a.mu.Lock()
	a.answers = ra
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		if a.answers == ra {
			a.answers = nil
		}
		a.mu.Unlock()
		ra.q.close()
		cancel()
		ra.q.wait()
	}
}

// answerAs takes an answer_as off the read loop: one out of its bounds is
// dropped and logged, a valid one queued (or dropped and logged when the
// queue is full or no connection holds one), never applied here.
func (a *agent) answerAs(data []byte) {
	var m proto.HostAnswerAsMsg
	if err := json.Unmarshal(data, &m); err != nil || !m.Valid() {
		a.log.Warn("an answer_as out of its bounds was dropped")
		return
	}
	a.mu.Lock()
	ra := a.answers
	a.mu.Unlock()
	if ra == nil {
		a.log.Warn("an answer_as was dropped: no connection to answer for", "question", m.Question)
		return
	}
	deadline := a.submitDeadline()
	if !ra.q.add(request{
		run: func() {
			ctx, cancel := context.WithDeadline(ra.ctx, deadline)
			defer cancel()
			err := a.applyAnswer(ctx, m)
			a.log.Debug("routed answer", "question", m.Question, "by", m.ByName, "err", err)
		},
		deadline: deadline,
		expired: func() {
			a.log.Debug("routed answer not applied: its time ran out while it waited", "question", m.Question)
		},
	}) {
		a.log.Warn("an answer_as was dropped: too many routed answers wait", "question", m.Question, "bound", answerQueue)
	}
}

// errBlankAnswer refuses a line blank once cleaned: typed, it would be a
// bare Enter, which into a question picks what the question highlights, as
// only a choice's keys may.
var errBlankAnswer = errors.New("the line is blank")

// applyAnswer applies m to the session, under the question's claim, as by
// m's name and subject.
func (a *agent) applyAnswer(ctx context.Context, m proto.HostAnswerAsMsg) error {
	if m.Input != "" {
		return a.local.AnswerAs(ctx, m.Question, m.Input, session.Actor{Subject: m.BySubject, Name: m.ByName})
	}
	if session.SubmitLine(m.Text) == "" {
		return errBlankAnswer
	}
	_, err := a.local.Submit(ctx, session.Submission{Text: m.Text, ByName: m.ByName, Question: m.Question})
	return err
}

// toServerKept is what the session keeps without showing it (Info.Kept) as
// the server is told it with its attention: to the owner's own server, the
// ids; to a switchyard (Options.Switchyard), which routes nothing, none.
func (a *agent) toServerKept(kept []string) []string {
	if a.opts.Switchyard || len(kept) == 0 {
		return nil
	}
	return slices.Clone(kept[:min(len(kept), proto.MaxKept)])
}
