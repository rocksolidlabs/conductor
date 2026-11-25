package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
)

// A crew member's move to its chat (docs/plans/chat-or-terminal.md, design
// 14): a member of a Chat run starts in its terminal, as every session
// does, and a person answers its folder's trust there. Once its terminal
// shows the agent's own prompt with nothing typed into it, the engine moves
// it: a fresh launch to a chat that starts a new conversation (MoveFresh),
// a launch that resumed a conversation to a chat on that conversation
// (MoveResumed, design 9's move). Both are the engine's alone: no route
// reaches them. Both are refused unless nothing was typed, and the session
// stays in its running terminal then.

// The member's move's words where the plan gives the run's log its own.
const (
	// TypedFirstWords refuses a member's move once a line was submitted to
	// the session, a person's keys reached its prompt, or a turn was
	// reported.
	TypedFirstWords = "A person typed in it first."
	// NotAtPromptYetWords refuses a member's move while its screen is not the
	// agent's prompt by its shape, or a question shows.
	NotAtPromptYetWords = "Its terminal does not show the agent's prompt."
	// MemberMovedWords is the notice of a member's move to its chat.
	MemberMovedWords = "Runs as a chat for its run."
)

// ErrMoveFellBack is a member's move whose chat did not start, or was not
// ready within its bound: the session runs in a terminal again (a fresh
// one for MoveFresh, the conversation's for MoveResumed), its words in
// MoveFellBack.
var ErrMoveFellBack = errors.New("session: the chat did not start; the session runs in its terminal")

// ErrMoveEnded is a member's move neither way of which could start: the
// session ended, both failures' words in MoveEnded.
var ErrMoveEnded = errors.New("session: neither the chat nor the terminal started; the session ended")

// MoveFellBack is ErrMoveFellBack with the failure's words.
type MoveFellBack struct{ Words string }

func (e *MoveFellBack) Error() string        { return e.Words }
func (e *MoveFellBack) Is(target error) bool { return target == ErrMoveFellBack }

// MoveEnded is ErrMoveEnded with both failures' words.
type MoveEnded struct{ Words string }

func (e *MoveEnded) Error() string        { return e.Words }
func (e *MoveEnded) Is(target error) bool { return target == ErrMoveEnded }

// AtPrompt says the terminal's screen, read whole and uncut, is the agent's
// prompt by its shape (Options.PromptShape) with no dialog's numbered
// choice on it: atPrompt, the reading every line Conductor types is judged
// by. It is false for an agent with no prompt shape, and for a session that
// runs as a chat: the sign is positive, never a dialog's words absent.
func (s *Local) AtPrompt() bool {
	if s.way.Load() != nil || s.opts.PromptShape == nil {
		return false
	}
	return s.atPrompt()
}

// MoveFresh moves a crew member's session launched to run as a chat
// (Options.MemberWay WayProtocol) that resumed no conversation from its
// terminal to a chat that starts a new one, and returns once the move has
// ended: nil once it runs as a chat. It is refused (MoveRefusal, nothing
// stopped) unless the session is such a member's, not ended and not
// moving, its screen is the agent's prompt (AtPrompt) with no question
// showing, and nothing was typed: no line submitted to it, no key of a
// person's written while its screen was the prompt, no turn reported. A
// startup question's answer through its choices, and keys at another
// screen, leave it allowed. The terminal is stopped and its group proven
// gone; the chat's child is started on a new conversation id (m.Protocol,
// given that id: the recipe's start, never --resume), which becomes
// Info.AgentSession; the terminal's own id is never resumed. A chat that
// does not start, or is not ready within m.ReadyCap (protocolReadyCap),
// has its child proven gone and a terminal started afresh on another new
// id (m.Terminal, given it), its startup watch watching again
// (ErrMoveFellBack); both failing end the session with both failures'
// words (ErrMoveEnded). The engine's alone: no route calls it.
func (s *Local) MoveFresh(m MoveStarts) error { return s.moveMember(m, true) }

// MoveResumed is design 9's move for a crew member's session launched to
// run as a chat that resumed a conversation (Options.MemberResumes): at
// its prompt with nothing typed, the conversation it resumed standing for
// a first turn that ended, to a chat on that conversation; a chat that
// does not start goes back to its terminal on the same conversation
// (ErrMoveFellBack). Refused as MoveFresh is, and for a conversation not
// known. The engine's alone.
func (s *Local) MoveResumed(m MoveStarts) error { return s.moveMember(m, false) }

// moveMember is MoveFresh (fresh) and MoveResumed.
func (s *Local) moveMember(m MoveStarts, fresh bool) error {
	at := s.AtPrompt()
	s.mu.Lock()
	var refused error
	switch {
	case s.info.Status.Ended():
		refused = ErrSessionEnded
	case s.moveDone != nil:
		refused = ErrMoving
	case s.info.Crew == nil || s.info.Kind != KindServer || s.opts.MemberWay != WayProtocol || s.opts.MemberResumes == fresh || s.way.Load() != nil:
		refused = moveRefused(CannotMoveWords)
	case m.Protocol == nil || m.Terminal == nil:
		refused = moveRefused(CannotMoveWords)
	case s.submitted.Load() || s.typedAtPrompt.Load() || s.held != nil || len(s.submitting) > 0 || fresh && s.firstTurn:
		refused = moveRefused(TypedFirstWords)
	case !at || s.info.Attention.State == AttentionNeedsInput:
		refused = moveRefused(NotAtPromptYetWords)
	case !fresh && (s.info.AgentSession == nil || s.info.AgentSession.ID == ""):
		refused = moveRefused(CannotMoveWords)
	}
	if refused != nil {
		s.mu.Unlock()
		return refused
	}
	s.clearNotedLocked()
	s.beginMoveLocked()
	s.lastMoveWhy = ""
	done := s.moveDone
	s.mu.Unlock()
	if fresh {
		go s.moveFresh(m)
	} else {
		go s.move(WayTerminal, WayProtocol, m, "")
	}
	<-done
	s.mu.Lock()
	way, why, ended := s.wayLocked(), s.lastMoveWhy, s.info.Status.Ended()
	s.mu.Unlock()
	switch {
	case way == WayProtocol && !ended:
		return nil
	case ended:
		return &MoveEnded{Words: why}
	}
	return &MoveFellBack{Words: why}
}

// moveFresh is MoveFresh's move, on a goroutine of its own, the session
// marked moving: the terminal stopped and proven gone with its group, the
// chat's child started on a new conversation; a start that fails has its
// child's group proven gone too and a terminal started afresh on another
// new conversation; both failing end the session with both failures'
// words. A Stop cancels what it waits on, and nothing starts after it.
func (s *Local) moveFresh(m MoveStarts) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.moveCancel = cancel
	if s.stopRequested {
		cancel()
	}
	s.mu.Unlock()
	ended, why := false, ""
	defer func() {
		cancel()
		s.mu.Lock()
		done := s.moveDone
		s.moveDone, s.moveCancel = nil, nil
		if !ended && !s.info.Status.Ended() {
			s.hub.Broadcast(s.wayFrameLocked(why))
			s.lastMoveWhy = why
		}
		p := s.way.Load()
		s.mu.Unlock()
		if done != nil {
			close(done)
		}
		if p != nil {
			p.wake()
		}
		s.notifyChange()
	}()
	stopped := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stopRequested
	}
	endStopped := func() {
		s.markEnded(StatusStopped)
		ended = true
	}
	group, err := s.stopForMove(ctx, WayTerminal)
	if err == nil {
		err = s.proveGone(ctx, m, group)
	}
	if s.isEnded() {
		ended = true
		return
	}
	if stopped() {
		endStopped()
		return
	}
	if err != nil {
		s.log.Warn("a member's terminal was not proven gone", "err", err)
		s.endMove(OldNotGoneWords, "")
		ended = true
		return
	}
	if m.AfterOldExit != nil {
		m.AfterOldExit()
	}
	if stopped() {
		endStopped()
		return
	}
	s.freshConversation()
	left, err := s.startWay(ctx, WayProtocol, m, false)
	if err == nil {
		s.mu.Lock()
		s.movedWith = m
		s.mu.Unlock()
		s.movedMember()
		return
	}
	if stopped() {
		endStopped()
		return
	}
	first := moveWords(m, WayProtocol, err)
	s.log.Warn("a member's chat did not start; its terminal is started afresh", "err", err)
	if gerr := s.proveGone(ctx, m, left); gerr != nil {
		s.log.Warn("a failed chat's child was not proven gone", "err", gerr)
		if stopped() {
			endStopped()
			return
		}
		s.endMove(first, OldNotGoneWords)
		ended = true
		return
	}
	if stopped() {
		endStopped()
		return
	}
	// No conversation exists to resume: a terminal started afresh on a new
	// one, its startup watch watching again.
	s.freshConversation()
	if _, err := s.startWay(ctx, WayTerminal, m, true); err != nil {
		s.log.Warn("a member's terminal did not start afresh either", "err", err)
		if stopped() {
			endStopped()
			return
		}
		s.endMove(first, moveWords(m, WayTerminal, err))
		ended = true
		return
	}
	why = first
	s.moved(WayTerminal, first)
}

// freshConversation gives the session a new conversation id, which the
// next start takes (startWay reads Info.AgentSession): chosen by Conductor,
// not yet resumable.
func (s *Local) freshConversation() {
	id := newConversationID()
	s.mu.Lock()
	s.info.AgentSession = &AgentSession{ID: id, Source: AgentSessionSet}
	s.mu.Unlock()
}

// movedMember says a member's move is made: the boundary's notice in the
// transcript and the activity's line.
func (s *Local) movedMember() {
	var out applied
	out.event = "moved"
	s.mu.Lock()
	s.addEntryLocked(TranscriptEntry{Kind: EntryNotice, Code: noticeMoved, Words: MemberMovedWords}, &out)
	s.mu.Unlock()
	s.sendApplied(out)
	s.Record(ActivityEntry{Type: ActivityStatus, Message: MemberMovedWords})
}

// newConversationID is a new agent conversation id: a random UUID, the
// shape Claude Code's --session-id takes.
func newConversationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("session: crypto/rand unavailable: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
