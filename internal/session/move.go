package session

import (
	"context"
	"errors"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The move between ways (docs/plans/chat-or-terminal.md, design 9): a
// person's own server session that runs its agent in its terminal moves, in
// place, to the agent's own protocol (a chat), and back. Only when a turn
// has ended: pressed during one, the move is noted and made at its end. The
// session, its id, links, viewers, people's chat, record and listing stay;
// the old process and its group are proven gone before the new one starts
// (I1), and a new start that fails tries once to go back the way it came.
// Every session starts in its terminal (I21): a move from a terminal
// session that has had a turn end is the only way into a chat.

// The move's words (the plan's, design 13).
const (
	FirstReplyWords      = "Chat opens after the agent's first reply."
	MovesToChatWords     = "Moves to Chat when this turn ends."
	MovesToTerminalWords = "Moves to the terminal when this turn ends."
	RequestWaitsWords    = "A request is waiting for an answer."
	BackgroundWords      = "Background work is still running."
	CrewStaysWords       = "Crew members stay in their terminals."
	CannotMoveWords      = "This agent cannot move its conversation yet."
	HostedStaysWords     = "Sessions on another machine stay in their terminal."
	MovingWords          = "The session is moving; try again in a moment."
	MovedToTerminalWords = "Moved to the terminal."
	MovedToChatWords     = "Moved to Chat. Earlier terminal messages are not shown here, and commands left running in the terminal were stopped."
	// AfterFirstReplyWords is a move to Chat noted at the launch (NoteWay)
	// while the agent's first reply has not ended: the plan has no sentence
	// for a move noted before a turn (issue 1174).
	AfterFirstReplyWords = "Moves to Chat after the agent's first reply."

	// The move's own words where the plan gives none.
	OldNotGoneWords     = "The agent's earlier process did not end; the session ended."
	ChatNotStartedWords = "The chat did not start."
	TermNotStartedWords = "The terminal did not start."
	noticeMoved         = "moved"
	noticeMoveFailed    = "move_failed"
	moveStopWait        = 5 * time.Second
	moveGroupWait       = ext.StopKillWait
	moveGroupPoll       = 10 * time.Millisecond
)

// ErrMoving refuses a submission, an answer or an interrupt while the
// session moves between ways.
var ErrMoving error = &wayWords{MovingWords}

// ErrMoveRefused is what every refused move is (errors.Is): its words say
// why (MoveRefusal).
var ErrMoveRefused = errors.New("session: the move was refused")

// MoveRefusal is a move refused, in one of the plan's sentences.
type MoveRefusal struct{ Words string }

func (e *MoveRefusal) Error() string        { return e.Words }
func (e *MoveRefusal) Is(target error) bool { return target == ErrMoveRefused }

func moveRefused(words string) error { return &MoveRefusal{Words: words} }

// MoveStarts is what a move starts the other way with, and the way back
// with when that start fails: the server's, built from the session's own
// launch (the same Runtime, folder, binding and environment).
type MoveStarts struct {
	// Terminal starts the agent's terminal resuming conversation, on a
	// terminal of cols by rows (the session's size).
	Terminal func(ctx context.Context, conversation string, cols, rows uint16) (Process, error)
	// Protocol starts a chat's child and its driver on the conversation.
	Protocol ProtocolStart
	// Gone proves that what is left of the process group group (a
	// process's ProcessGroup, as its Runtime reported it) has ended, within
	// ctx: nil once no process is in it. A move whose old process reports
	// a group refuses to begin without it.
	Gone func(ctx context.Context, group int) error
	// Words is a failed start's sentence for a person (the driver's own,
	// "Claude Code did not start a chat."); the session's when nil or "".
	Words func(err error) string
	// AfterOldExit, a test's seam, runs once the old process has exited and
	// its group's end is proven, before the new one starts.
	AfterOldExit func()
}

// Grouped is a process that knows its process group, as its Runtime
// reported it (ext.ProcessInfo.Group): a move proves it gone (MoveStarts.Gone).
type Grouped interface{ ProcessGroup() int }

// ProcessGroup is the terminal's group.
func (p runtimeProcess) ProcessGroup() int { return p.Terminal.Process().Group }

// ProcessGroup is the child's group.
func (c pipesChild) ProcessGroup() int { return c.p.Process().Group }

func groupOf(p any) int {
	if g, ok := p.(Grouped); ok {
		return g.ProcessGroup()
	}
	return 0
}

// MoveState is where a session stands with the move after SwapWay: the way
// it runs, the way a noted move goes to and why it waits, and whether a
// move is under way.
type MoveState struct {
	Way    string `json:"way"`
	Noted  string `json:"noted,omitempty"`
	Why    string `json:"why,omitempty"`
	Moving bool   `json:"moving,omitempty"`
}

// SwapWay moves the session to the way to (WayTerminal or WayProtocol) with
// m's starts: at once when the session is at rest (the move is then under
// way when SwapWay returns, and the way message tells every viewer as it
// ends), else noted and made once it is (at the turn's end, or when what
// held it ends), its sentence saying why it waits. A press to the way the
// session runs takes a noted move back. Refused (MoveRefusal) for a crew
// member, a hosted session, an agent the server cannot move, a
// conversation not known yet and, to Chat, before the terminal's first
// turn has ended; ErrMoving while a move is under way; ErrSessionEnded.
func (s *Local) SwapWay(to string, m MoveStarts) (MoveState, error) {
	if to != WayTerminal && to != WayProtocol {
		return MoveState{}, errors.New("session: no such way")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.wayLocked()
	st := MoveState{Way: from, Noted: s.noted, Why: s.notedWhy}
	switch {
	case s.info.Status.Ended():
		return st, ErrSessionEnded
	case s.moveDone != nil:
		st.Moving = true
		return st, ErrMoving
	case to == from:
		// Pressed away: the noted move goes.
		if s.noted != "" {
			s.clearNotedLocked()
			s.broadcastWayLocked()
		}
		return MoveState{Way: from}, nil
	case s.info.Crew != nil:
		return st, moveRefused(CrewStaysWords)
	case s.info.Kind != KindServer:
		return st, moveRefused(HostedStaysWords)
	case to == WayProtocol && m.Protocol == nil, to == WayTerminal && m.Terminal == nil:
		return st, moveRefused(CannotMoveWords)
	case to == WayProtocol && !s.info.FirstTurn:
		return st, moveRefused(FirstReplyWords)
	case s.info.AgentSession == nil || s.info.AgentSession.ID == "" || !s.info.AgentSession.Resumable:
		return st, moveRefused(CannotMoveWords)
	}
	if why, ok := s.atRestLocked(); !ok {
		// Noted (one at a time: a second press replaces it), made at the
		// turn's end or once what holds it ends (tryNoted).
		s.noted, s.notedWhy, s.notedStarts = to, why, m
		s.hasNoted.Store(true)
		s.broadcastWayLocked()
		return MoveState{Way: from, Noted: to, Why: why}, nil
	}
	s.clearNotedLocked()
	s.beginMoveLocked()
	go s.move(from, to, m, "")
	return MoveState{Way: from, Moving: true}, nil
}

// NoteWay notes, at a launch, a move to the way to (the Launch agent
// dialog's Run as Chat, issue 1174), with m's starts. Nothing starts here:
// the session runs in its terminal (I21), and the noted move's own path
// (tryNoted) makes the move at the first moment all of design 9's conditions
// hold, judged then (atRestLocked): the agent's first reply ended, nothing
// open or held, the conversation known and resumable. Until then its
// sentence says why it waits; a press to the way the session runs takes it
// back (SwapWay). Refused (MoveRefusal) for a crew member, a hosted
// session and starts missing, as SwapWay refuses them; ErrMoving;
// ErrSessionEnded.
func (s *Local) NoteWay(to string, m MoveStarts) (MoveState, error) {
	if to != WayTerminal && to != WayProtocol {
		return MoveState{}, errors.New("session: no such way")
	}
	s.mu.Lock()
	from := s.wayLocked()
	st := MoveState{Way: from, Noted: s.noted, Why: s.notedWhy}
	var err error
	switch {
	case s.info.Status.Ended():
		err = ErrSessionEnded
	case s.moveDone != nil:
		st.Moving, err = true, ErrMoving
	case to == from:
		st = MoveState{Way: from}
	case s.info.Crew != nil:
		err = moveRefused(CrewStaysWords)
	case s.info.Kind != KindServer:
		err = moveRefused(HostedStaysWords)
	case to == WayProtocol && m.Protocol == nil, to == WayTerminal && m.Terminal == nil:
		err = moveRefused(CannotMoveWords)
	}
	if err != nil || to == from {
		s.mu.Unlock()
		return st, err
	}
	why, rest := s.atRestLocked()
	s.noted, s.notedWhy, s.notedStarts = to, why, m
	s.hasNoted.Store(true)
	s.broadcastWayLocked()
	s.mu.Unlock()
	if rest {
		s.tryNoted()
	}
	return MoveState{Way: from, Noted: to, Why: why}, nil
}

// wayLocked is Way under the lock.
func (s *Local) wayLocked() string {
	if s.way.Load() != nil {
		return WayProtocol
	}
	return WayTerminal
}

// Moving says a move between ways is under way.
func (s *Local) Moving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.moveDone != nil
}

// Noted is the way a noted move goes to and why it waits ("" when none).
func (s *Local) Noted() (to, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noted, s.notedWhy
}

// atRestLocked says whether the session is at rest for a move now, and if
// not, the sentence that says why the move waits. The caller holds s.mu.
// A chat: no turn under way, no message waiting to be written or for its
// acknowledgement, no permission request open, no work the agent reported
// still running. A terminal: the attention a done from a source that
// reports turns, no question open, nothing typed held, no submission under
// way.
func (s *Local) atRestLocked() (string, bool) {
	if p := s.way.Load(); p != nil {
		switch {
		case len(p.perms) > 0 || p.requests > 0:
			// A request open, waiting for its call or on its way back: no
			// move while any is (design 9).
			return RequestWaitsWords, false
		case !p.isReady || p.restarting || p.stopping || p.restartAtEnd || p.busyLocked() || len(p.queue) > 0:
			return MovesToTerminalWords, false
		case p.background:
			return BackgroundWords, false
		}
		return "", true
	}
	// A move to Chat, judged again at the moment of the move (design 9):
	// one noted at the launch (NoteWay) waits for the first reply's end
	// and for the launch's first message to be typed, and every noted one
	// for a session that became a crew member meanwhile or whose
	// conversation is not known and resumable yet.
	att := s.info.Attention
	as := s.info.AgentSession
	switch {
	case s.info.Crew != nil:
		return CrewStaysWords, false
	case !s.info.FirstTurn || s.info.FirstMessage == "waiting":
		return AfterFirstReplyWords, false
	case att.State == AttentionNeedsInput:
		return RequestWaitsWords, false
	case att.State != AttentionDone || !ReportsTurn(att.State, att.Source) || s.held != nil || len(s.submitting) > 0:
		return MovesToChatWords, false
	case as == nil || as.ID == "" || !as.Resumable:
		return CannotMoveWords, false
	}
	return "", true
}

// clearNotedLocked forgets the noted move. The caller holds s.mu.
func (s *Local) clearNotedLocked() {
	s.noted, s.notedWhy, s.notedStarts = "", "", MoveStarts{}
	s.hasNoted.Store(false)
}

// beginMoveLocked marks the session moving. The caller holds s.mu.
func (s *Local) beginMoveLocked() { s.moveDone = make(chan struct{}) }

// wayFrameLocked is the way message as the session stands, expands its
// word to the operator's own chat connection (Way.Expands). The caller
// holds s.mu.
func (s *Local) wayFrameLocked(why string, expands bool) []byte {
	m := proto.Way{Way: s.wayLocked(), Noted: s.noted, Expands: expands}
	if p := s.way.Load(); p != nil {
		m.Protocol = p.name
	}
	if s.noted != "" {
		why = s.notedWhy
	}
	m.Why = cutAt(CleanMessage(why), proto.MaxWayWhy)
	f, err := proto.EncodeWay(m)
	if err != nil {
		m.Protocol, m.Why = "", ""
		f, _ = proto.EncodeWay(m)
	}
	return f
}

// broadcastWayLocked tells every viewer the way and the noted move. The
// caller holds s.mu.
func (s *Local) broadcastWayLocked() { s.sendWayLocked("") }

// standingChangedLocked tells every viewer of a chat its way again after
// the session's standing changed (a crew formed around it): the way
// message's expand as it now is, to each connection. The caller holds s.mu.
func (s *Local) standingChangedLocked() { s.sendWayLocked("") }

// sendWayLocked tells every viewer the way, the noted move and why, and,
// on a chat whose driver has a mark, the operator's own chat connection
// alone that its typed line goes for the agent to expand (Way.Expands,
// I19): said again after a move, so its page never stands in with a word
// of its own; on a chat, that connection is sent the agent's commands
// after it. The caller holds s.mu.
func (s *Local) sendWayLocked(why string) {
	plain := s.wayFrameLocked(why, false)
	p := s.way.Load()
	if p == nil {
		s.hub.Broadcast(plain)
		return
	}
	own := s.wayFrameLocked(why, p.verbatim)
	// The agent's own commands follow the way to a chat, to the operator's
	// own chat connection alone (commands.go).
	commands := s.commandsFrameLocked(p)
	s.hub.Each(func(sub *Subscription) {
		if s.operatorChatLocked(sub) {
			sub.send(own)
			if commands != nil {
				sub.send(commands)
			}
		} else {
			sub.send(plain)
		}
	})
}

// tryNoted makes the noted move once the session is at rest, or tells
// viewers the new reason it waits: called after the events that may end
// what holds it (a turn's end, a done, a request closed, work ended).
func (s *Local) tryNoted() {
	if !s.hasNoted.Load() {
		return
	}
	s.mu.Lock()
	if s.noted == "" || s.moveDone != nil || s.info.Status.Ended() {
		s.mu.Unlock()
		return
	}
	why, ok := s.atRestLocked()
	if !ok {
		if why != s.notedWhy {
			s.notedWhy = why
			s.broadcastWayLocked()
		}
		s.mu.Unlock()
		return
	}
	from, to, m := s.wayLocked(), s.noted, s.notedStarts
	s.clearNotedLocked()
	s.beginMoveLocked()
	s.mu.Unlock()
	go s.move(from, to, m, "")
}

// move is the move itself, on a goroutine of its own, the session marked
// moving: the old process stopped and proven gone with its group, the
// conversation read as it stands, the other way started on it; a start
// that fails has its child's group proven gone too and goes back the way
// it came, once; both failing end the session with both failures' words.
// cause, when set, is why the move was made (a chat whose agent is not
// signed in going back to its terminal): the way message's sentence. A
// Stop cancels what the move waits on (moveCancel) and nothing is started
// after it: the session ends stopped.
func (s *Local) move(from, to string, m MoveStarts, cause string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.moveCancel = cancel
	if s.stopRequested {
		cancel()
	}
	s.mu.Unlock()
	ended, why := false, cause
	defer func() {
		cancel()
		s.mu.Lock()
		done := s.moveDone
		s.moveDone, s.moveCancel = nil, nil
		if !ended && !s.info.Status.Ended() {
			s.sendWayLocked(why)
		}
		p := s.way.Load()
		s.mu.Unlock()
		if done != nil {
			close(done)
		}
		if p != nil {
			// What the chat refused while it moved is sent again by its
			// senders; the sender looks once more.
			p.wake()
		}
		s.notifyChange()
	}()
	stopped := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stopRequested
	}
	// endStopped ends a move a Stop cut short: nothing more starts.
	endStopped := func() {
		s.markEnded(StatusStopped)
		ended = true
	}
	group, err := s.stopForMove(ctx, from)
	if err == nil {
		err = s.proveGone(ctx, m, group)
	}
	if s.isEnded() {
		// The process ended by itself meanwhile, or the session was
		// stopped: nothing starts.
		ended = true
		return
	}
	if stopped() {
		endStopped()
		return
	}
	if err != nil {
		s.log.Warn("a move's old process was not proven gone", "err", err)
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
	left, err := s.startWay(ctx, to, m)
	if err == nil {
		if to == WayProtocol {
			// Kept for the way back the chat may take by itself (a
			// signed-out agent, readProtocol).
			s.mu.Lock()
			s.movedWith = m
			s.mu.Unlock()
		}
		s.moved(to, cause)
		return
	}
	if stopped() {
		endStopped()
		return
	}
	first := moveWords(m, to, err)
	s.log.Warn("a move's start failed; going back", "to", to, "err", err)
	// The child of the start that failed is proven gone as the old one was
	// (I1): the way back starts on the same conversation.
	if gerr := s.proveGone(ctx, m, left); gerr != nil {
		s.log.Warn("a failed start's child was not proven gone", "err", gerr)
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
	if _, err := s.startWay(ctx, from, m); err != nil {
		s.log.Warn("the way back did not start either", "to", from, "err", err)
		if stopped() {
			endStopped()
			return
		}
		s.endMove(first, moveWords(m, from, err))
		ended = true
		return
	}
	why = first
	s.moved(from, first)
}

// proveGone proves a stopped process's group gone (MoveStarts.Gone), within
// the move's bound; a group of 0 is none to prove.
func (s *Local) proveGone(ctx context.Context, m MoveStarts, group int) error {
	if group <= 0 {
		return nil
	}
	if m.Gone == nil {
		return errors.New("session: nothing proves the old process's group gone")
	}
	gctx, cancel := context.WithTimeout(ctx, moveGroupWait)
	defer cancel()
	return m.Gone(gctx, group)
}

// moveWords is a failed start's sentence.
func moveWords(m MoveStarts, to string, err error) string {
	if m.Words != nil {
		if w := m.Words(err); w != "" {
			return w
		}
	}
	if to == WayProtocol {
		return ChatNotStartedWords
	}
	return TermNotStartedWords
}

// stopForMove stops the way the session runs for a move and waits for its
// process's exit and its output's end; it returns the process's group, to
// be proven gone. Called with no lock held: a stop that hangs holds no
// request and no lock.
func (s *Local) stopForMove(mctx context.Context, from string) (int, error) {
	ctx, cancel := context.WithTimeout(mctx, s.opts.StopGrace+ext.StopKillWait)
	defer cancel()
	if from == WayProtocol {
		return s.leaveProtocol(ctx)
	}
	s.mu.Lock()
	// The pump of the terminal stopped here finds a later terminal and
	// does not end the session.
	s.termGen.Add(1)
	old := s.term.get()
	done := s.pumpDone.Load()
	s.mu.Unlock()
	for _, w := range []*PatternWatcher{s.pattern, s.trust, s.asked, s.shot} {
		if w != nil {
			w.Stop()
		}
	}
	err := old.Stop(ctx, s.opts.StopGrace)
	select {
	case <-old.Done():
	case <-ctx.Done():
		return 0, errors.New("session: the terminal's process did not exit")
	}
	if done != nil {
		select {
		case <-*done:
		case <-time.After(moveStopWait):
			return 0, errors.New("session: the terminal's output did not end")
		}
	}
	_ = err
	return groupOf(old), nil
}

// leaveProtocol stops a chat's child for a move: its input closed, then
// Stop; its read waited for to its end. Nothing it sends from then on is
// applied, and its end is not the session's.
func (s *Local) leaveProtocol(ctx context.Context) (int, error) {
	s.mu.Lock()
	p := s.way.Load()
	if p == nil {
		s.mu.Unlock()
		return 0, nil
	}
	p.gen++
	p.stopping = true
	child, d, readDone := p.child, p.driver, p.readDone
	for _, t := range p.texts {
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	// Nothing waits on the way that is left: a message queued or written
	// and not yet acknowledged (one that came as the move began) is told
	// the session is moving, its claim on a question let go, and none of
	// it is written to the child that ends.
	var out applied
	out.event = "left"
	pending := append([]*queuedMessage(nil), p.queue...)
	if p.inflight != nil {
		pending = append(pending, p.inflight)
	}
	p.queue, p.inflight = nil, nil
	for _, q := range pending {
		if q.timer != nil {
			q.timer.Stop()
		}
		state := PromptWithdrawn
		if q.state == PromptWritten {
			state = PromptUnconfirmed
		}
		q.state = state
		s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State = state }, &out)
		if q.claim != 0 {
			out.release = append(out.release, questionClaim{id: q.sub.Question, token: q.claim})
		}
		out.finish(q, SubmitResult{}, ErrMoving)
	}
	s.mu.Unlock()
	s.sendApplied(out)
	_ = d.CloseInput()
	_ = child.Stop(ctx, s.opts.StopGrace)
	select {
	case <-child.Done():
	case <-ctx.Done():
		return 0, errors.New("session: the chat's child did not exit")
	}
	select {
	case <-readDone:
	case <-time.After(moveStopWait):
		return 0, errors.New("session: the chat's output did not end")
	}
	p.cancel()
	return groupOf(child), nil
}

// startWay starts the way to on the conversation as it stands now, within
// the move's ctx (a Stop cancels it). A chat's start that failed answers its
// child's group, to be proven gone before the way back.
func (s *Local) startWay(mctx context.Context, to string, m MoveStarts) (int, error) {
	s.mu.Lock()
	conv := ""
	if s.info.AgentSession != nil {
		conv = s.info.AgentSession.ID
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(mctx, protocolReadyCap)
	defer cancel()
	if to == WayProtocol {
		s.mu.Lock()
		if p := s.way.Load(); p != nil {
			// The chat's child the session came from is gone: its way is
			// left behind before another is entered.
			s.way.Store(nil)
		}
		s.mu.Unlock()
		return s.enterProtocolGroup(ctx, m.Protocol)
	}
	// A terminal still running (one a test left quiet as it entered the
	// protocol way) is stopped first: one pump, one writer.
	if done := s.pumpDone.Load(); done != nil {
		select {
		case <-*done:
		default:
			s.termGen.Add(1)
			_ = s.term.get().Stop(ctx, s.opts.StopGrace)
			select {
			case <-*done:
			case <-ctx.Done():
				return 0, errors.New("session: the terminal before did not end")
			}
		}
	}
	s.mu.Lock()
	cols, rows := s.info.Cols, s.info.Rows
	s.mu.Unlock()
	term, err := m.Terminal(ctx, conv, cols, rows)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.info.Status.Ended() || s.stopRequested {
		// Stopped while it started: the session never runs it.
		s.mu.Unlock()
		_ = term.Stop(context.Background(), s.opts.StopGrace)
		return groupOf(term), ErrSessionEnded
	}
	s.renewTerminalLocked()
	s.way.Store(nil)
	s.startTerminal(term)
	s.mu.Unlock()
	return 0, nil
}

// renewTerminalLocked gives a terminal started again its ring and screens
// empty, and its watchers watching again. The caller holds s.mu; the old
// terminal's pump has returned.
func (s *Local) renewTerminalLocked() {
	s.ring.Reset()
	cols, rows := int(s.info.Cols), int(s.info.Rows)
	for _, w := range []*PatternWatcher{s.pattern, s.asked, s.shot} {
		if w != nil {
			w.Renew(cols, rows)
		}
	}
	s.scanner = Scanner{}
	s.scanBegun = 0
	s.paste.state.Store(0)
	s.paste.tail = nil
	s.lastOutput = time.Time{}
}

// moved says the move to to is made: the boundary's notice in the
// transcript, the activity's line, and (from defer in move) the way
// message; why, when set, is the sentence of a move that went back.
func (s *Local) moved(to, why string) {
	var out applied
	out.event = "moved"
	s.mu.Lock()
	words := MovedToTerminalWords
	if to == WayProtocol {
		words = MovedToChatWords
	}
	turns := uint32(0)
	if p := s.way.Load(); p != nil {
		turns = p.turns
	}
	s.addEntryLocked(TranscriptEntry{Kind: EntryNotice, Turn: turns, Code: noticeMoved, Words: words}, &out)
	if why != "" {
		s.addEntryLocked(TranscriptEntry{Kind: EntryNotice, Turn: turns, Code: noticeMoveFailed, Words: cutAt(CleanMessage(why), MaxNoticeWords)}, &out)
	}
	s.mu.Unlock()
	s.sendApplied(out)
	line := words
	if why != "" {
		line = why + " " + words
	}
	s.Record(ActivityEntry{Type: ActivityStatus, Message: line})
}

// endMove ends the session after a move neither way could start: both
// failures' words in its activity and its transcript. The conversation
// stays resumable with Resume.
func (s *Local) endMove(first, second string) {
	words := first
	if second != "" {
		words = first + " " + second
	}
	var out applied
	out.event = "move_failed"
	s.mu.Lock()
	s.addEntryLocked(TranscriptEntry{Kind: EntryNotice, Code: noticeMoveFailed, Words: cutAt(CleanMessage(words), MaxNoticeWords)}, &out)
	s.mu.Unlock()
	s.sendApplied(out)
	s.Record(ActivityEntry{Type: ActivityStatus, Message: words})
	s.markEnded(StatusExited)
}

// GroupGone proves the native process group group ended: nothing left in
// it that can run, what the old process left there sent SIGKILL once
// (proc.GroupGone, the runner's: the session signals no process itself).
func GroupGone(ctx context.Context, group int) error { return proc.GroupGone(ctx, group) }
