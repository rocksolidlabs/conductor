package session

import (
	"context"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Who answers a chat's permission request (docs/plans/chat-or-terminal.md,
// design 7, "Who may answer"): the operator alone, through
// permission_answer on the session's socket. Every other answer path
// refuses a permission request's question (ErrPermissionCard: Submit, an
// answer's keys or line, AnswerAs), and this one refuses every connection
// but the operator's own (ErrNotAnswerer).

// The words of a permission answer's refusals (the plan's, design 13).
const (
	NotAnswererWords  = "Only the person who runs this server can answer."
	StaleRequestWords = "This request changed or was answered."
	// OperatorDeniedWords are what the agent is told with the operator's
	// deny when the operator wrote no message of their own.
	OperatorDeniedWords = "The operator denied this call."
)

var (
	// ErrNotAnswerer refuses a permission answer from anyone but the
	// operator: nothing is answered.
	ErrNotAnswerer error = &wayWords{NotAnswererWords}
	// ErrStaleRequest refuses a permission answer whose call, digest or
	// claim no longer matches the open request, or whose call has a
	// result: the request stays open.
	ErrStaleRequest error = &wayWords{StaleRequestWords}
)

// operatorAnswers says by is the operator's own connection: the
// workbench's, or an identity provider's principal holding admin
// (AttachOptions.Owner, with no link), as a controller. A link's viewer of
// any role, a decision link's, a relayed one, Brakeman and the agent have
// none.
func operatorAnswers(by *Subscription) bool {
	return by != nil && by.owner && by.LinkID == "" && by.Role == RoleControl
}

// operatorChatLocked says by is the operator's own chat connection
// (docs/plans/chat-or-terminal.md, I19): the operator's own connection
// (operatorAnswers), attached by the server's own viewer socket
// (AttachOptions.ViewerSocket), not quiet, on a session that is no crew
// member's. Only such a connection's typed line goes without the
// as-written mark, and only it is told so (Welcome.Expands, Way.Expands).
// A peer over the host link or a switchyard, a link of any role, a run's
// chat and Conductor itself never pass it. The caller holds s.mu.
func (s *Local) operatorChatLocked(by *Subscription) bool {
	return operatorAnswers(by) && by.viewerSocket && !by.quiet && s.info.Crew == nil
}

// AnswerPermission is the operator's answer to an open permission request
// (permission_answer): by must be the operator's own connection (the
// workbench's, or an identity provider's principal holding admin), every
// other answerer refused ErrNotAnswerer. It claims the request; then, at
// the hand-over, under the session's lock, checks that it is still open,
// that this claim holds it, that the call and digest are the request's and
// that the call has no result (ErrStaleRequest otherwise, the request left
// open); records who answered (the question's kept result, the entry's
// byName, an activity entry) before it hands the verdict over; and
// returns nil once the verdict was delivered (the POST's reply written, or
// the driver's Reply returned), the entry then allowed or denied. A
// verdict that could not be delivered is recorded withdrawn and answered
// ErrQuestionAnswered; so is a request answered, withdrawn or denied.
func (s *Local) AnswerPermission(ctx context.Context, by *Subscription, a proto.PermissionAnswer) error {
	if s.Moving() {
		return ErrMoving
	}
	p := s.way.Load()
	if p == nil {
		return ErrNotProtocol
	}
	if !operatorAnswers(by) {
		return ErrNotAnswerer
	}
	if !a.Valid() {
		return ErrBadAnswer
	}
	allow := a.Choice == proto.PermissionAllowOnce
	name := by.answerer()

	s.mu.Lock()
	r, err := s.matchLocked(p, a)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if r.claim != 0 {
		s.mu.Unlock()
		return ErrQuestionAnswered
	}
	s.claimSeq++
	token := s.claimSeq
	r.claim = token
	s.mu.Unlock()
	if seam := s.permissionClaimed.Load(); seam != nil {
		(*seam)()
	}

	// The hand-over: the same checks, at the moment the verdict is handed.
	var out applied
	out.event = "permission_answered"
	s.mu.Lock()
	if err := func() error {
		if !s.listedLocked(p, r) || r.claim != token || p.stopping {
			// A stop under way answers every open request itself.
			return ErrQuestionAnswered
		}
		if _, err := s.matchLocked(p, a); err != nil {
			r.claim = 0
			return err
		}
		return nil
	}(); err != nil {
		s.mu.Unlock()
		return err
	}
	// Who answered, recorded first.
	s.keepResultLocked(r.question, &Answer{By: by.ID, ByName: name, At: time.Now().UTC(), Message: permissionWords(r.plain), ReqID: a.ReqID})
	s.reviseLocked(r.seq, func(e *TranscriptEntry) { e.ByName = oneLine(name, maxPromptName) }, &out)
	// No longer open: no end, bound or timer reaches it from here.
	s.removePermissionLocked(p, r, &out)
	r.handed = true
	d := p.driver
	s.mu.Unlock()
	s.sendApplied(out)
	how := "Denied"
	if allow {
		how = "Allowed once"
	}
	s.RecordServer(ActivityEntry{Type: ActivityInput, By: by.ID, ByName: name, Message: how + ": " + permissionWords(r.plain)})

	v := Verdict{Allow: allow}
	if !allow {
		v.Message = CleanMessage(a.Message)
		if v.Message == "" {
			v.Message = OperatorDeniedWords
		}
	}
	if r.held != nil {
		r.held.verdict <- heldVerdict{v: v, operator: true}
		select {
		case <-r.held.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		delivered := r.held.delivered
		s.mu.Unlock()
		if !delivered {
			return ErrQuestionAnswered
		}
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.StopGrace+5*time.Second)
	err = d.Reply(rctx, r.ref, v)
	cancel()
	out = applied{event: "permission_delivered"}
	s.mu.Lock()
	state := PermissionDenied
	if allow {
		state = PermissionAllowed
	}
	if err != nil {
		state = PermissionWithdrawn
	}
	s.reviseLocked(r.seq, func(e *TranscriptEntry) {
		e.State = state
		if err != nil {
			e.Why = RequestLostWords
		}
	}, &out)
	s.unpinLocked(r)
	s.mu.Unlock()
	s.sendApplied(out)
	if err != nil {
		s.RecordServer(ActivityEntry{Type: ActivityStatus, Message: NotDeliveredWords})
		return ErrQuestionAnswered
	}
	return nil
}

// matchLocked is the open request a answers, checked as the hand-over
// checks it: ErrQuestionAnswered when none is open by its question,
// ErrStaleRequest when its call or digest differ or its call has a
// result. The caller holds s.mu.
func (s *Local) matchLocked(p *protocolWay, a proto.PermissionAnswer) (*openPermission, error) {
	for _, r := range p.perms {
		if r.question != a.Question {
			continue
		}
		if a.Call != r.call || a.Sha256 != r.digest {
			return nil, ErrStaleRequest
		}
		if r.call != "" && p.resulted[r.call] {
			return nil, ErrStaleRequest
		}
		// The call as it was shown: a call the session no longer holds, or
		// holds announced again with another input, is not the one asked
		// about.
		if t := p.tools[r.call]; r.call != "" && (t == nil || t.digest != r.digest) {
			return nil, ErrStaleRequest
		}
		return r, nil
	}
	return nil, ErrQuestionAnswered
}

// PermissionAnswerWord is the owner's word on the permission_answer reqID
// answered err: answer_ok when the verdict was delivered, else the error
// naming it (not_answerer, stale_request, question_answered, moving,
// not_protocol, bad_frame).
func PermissionAnswerWord(reqID string, err error) []byte {
	if err == nil {
		if !proto.ValidAnswerReqID(reqID) {
			reqID = ""
		}
		return proto.MustControl(proto.AnswerOK{T: proto.CtlAnswerOK, RequestID: reqID})
	}
	return chatWord(reqID, err)
}
