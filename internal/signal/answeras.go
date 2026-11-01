package signal

import (
	"errors"
	"fmt"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Routed answers for a hosted session (the decision seam, Program item C5):
// a host whose register said answerAs takes answer_as, an answer a
// decision router or a provider holds for the question its session
// shows, and sends with its attention what its session keeps (kept), so
// the server's router tells a question set aside from one that ended. The
// host applies the answer under its own claim, the first answer winning;
// the server only carries it, in its own lane, after registered.

// HostAttentionKept is HostAttention with what the host's session keeps
// without showing it (proto.HostAttentionMsg.Kept, at most proto.MaxKept
// valid question ids, checked by the caller): taken with the attention
// under the one lock, so that Info answers the two from one message, and
// only from a host that said answerAs (else none is kept). A report equal
// to the attention shown with a new kept takes the kept and records
// nothing else.
func (h *HostedSession) HostAttentionKept(state session.AttentionState, message, source, kind string, options []session.Option, id string, ask bool, answered, plan string, kept []string) {
	if !proto.ValidQuestionID(id) {
		id = ""
	}
	_ = h.setAttention(state, message, source, kind, options, false, id, ask, answered, plan, kept)
}

// HostView is the session as its host last said it: its Info, with the
// attention of the host's last report and what that report said the
// session keeps, the two from one message, read under one lock. A change
// the server forwards shows in Info at once, before the host has applied
// it (its question set aside, say, while the host has not yet said it
// keeps it): the decision router judges from what the host said alone.
func (h *HostedSession) HostView() session.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	info := h.info
	info.Viewers = len(h.viewers)
	info.Attention = h.reported
	if info.Status.Ended() {
		info.Attention = h.info.Attention
	}
	return info
}

// TakesAnswers reports whether the session's host said answerAs as it
// registered (proto.HostInfo.AnswerAs): a routed answer reaches it, and
// its Info.Kept is the host's.
func (h *HostedSession) TakesAnswers() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.answerAs
}

// AnswerAs sends the host the answer m in the server's own lane (Tell):
// ext.ErrUnsupported when the host did not say answerAs (an older host,
// which would drop it unread), ErrHostGone without a host, ErrSlowHost
// when the lane is full. Sent is not applied: the host applies it under
// the question's claim, and its next attention says whether it was.
func (h *HostedSession) AnswerAs(m proto.HostAnswerAsMsg) error {
	m.T = proto.HostAnswerAs
	if !m.Valid() {
		return errors.New("signal: answer_as out of its bounds")
	}
	if !h.TakesAnswers() {
		return fmt.Errorf("%w: the session's host takes no routed answer", ext.ErrUnsupported)
	}
	return h.Tell(m)
}

// ForwardStatus sends the host a status entry of the server's own (a
// route's "routed: ..." line), which the host records in its session's
// activity and reports back (HostActivity), as a server session records
// its own status entries (session.Local.Record): it spends no token of the
// session's bucket, which the reports an agent makes through the API
// share (ForwardActivity, SetAttentionFull), so that a busy session's
// reports never lose it. What sends it bounds it: the router sends one per
// question it routes, never twice, and the host's lane keeps its own
// bound (HostConn.send). It returns ErrHostGone when no host is connected
// and ErrSlowHost when the host's queue is full.
func (h *HostedSession) ForwardStatus(e session.ActivityEntry) error {
	e.Type = session.ActivityStatus
	return h.toHost(hostActivityMsg(session.CleanEntry(e)))
}

// setAnswerAs keeps what a register says of answer_as: a host that no
// longer says it (an older one, registering again) keeps nothing.
func (h *HostedSession) setAnswerAs(on bool) {
	h.answerAs = on
	if !on {
		h.info.Kept = nil
	}
}
