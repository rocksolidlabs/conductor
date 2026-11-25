package crew

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A Chat run's members (Crew.Way, docs/plans/chat-or-terminal.md, design
// 8) run their agents as chats, over the agent's own structured
// interface, where the agent's installed version can and the run's
// Runtime has pipes; otherwise in their terminals, the run's log saying
// why. A member's session starts in its terminal, as every session does.
// What the engine does with a member is keyed on the session's own way
// when it acts (session.Local.Way), never on the way it was launched
// with: a member launched to run as a chat may still be in its terminal.
// In a chat, the member's readiness is the session's (Local.Ready), its
// prompt is sent once and taken at the agent's acknowledgement, the
// lines the engine sends it during a turn wait for the turn's end, and a
// turn's end finishes no task.

// protocolReadyCap bounds the wait for a member's chat to be ready
// (Local.Ready): the session's own bound on its child's start.
const protocolReadyCap = 60 * time.Second

// errChatNotReady is AwaitReady giving up on a chat not ready within
// protocolReadyCap.
var errChatNotReady = errors.New("the chat was not ready")

// ChatChecker is optional beside Launcher: a Launcher that has it says
// whether a member can run as a chat, and gives the engine what a member's
// move to its chat starts with. The API server answers both from the
// agent's catalog entry, its probed version and the member's launch. A
// Launcher without it runs every member in its terminal.
type ChatChecker interface {
	// ChatCheck answers nil when a member of agentID launched with args
	// can run as a chat (its installed version qualifies, its arguments
	// name no added folder and no flag a chat sets itself), else why: a
	// *TerminalReason in the run's log's words, or an error the log
	// quotes.
	ChatCheck(ctx context.Context, agentID string, args []string) error
	// MemberMove is what the move of local, a member launched to run as a
	// chat, starts its chat with (and its terminal again, should the chat
	// not start), asked with its terminal still running: its checks, made
	// first, refuse it as ChatCheck does. fresh is a launch that resumed
	// no conversation (session.Local.MoveFresh), else one that did
	// (session.Local.MoveResumed).
	MemberMove(ctx context.Context, local *session.Local, fresh bool) (session.MoveStarts, error)
}

// TerminalReason is why a member of a Chat run stays in its terminal, in
// the run's log's words after "in its terminal: ".
type TerminalReason struct{ Words string }

func (r *TerminalReason) Error() string { return r.Words }

// reasonWords is err's words after "in its terminal: ".
func reasonWords(err error) string {
	var r *TerminalReason
	if errors.As(err, &r) {
		return r.Words
	}
	return sentence(err.Error())
}

// Why a member of a Chat run stays in its terminal, after "in its
// terminal: " in the run's log.
const (
	noPipesWords   = "the run's Runtime has no pipes."
	noCheckerWords = "this server cannot run its agent as a chat."
)

// launchWay is the Way member m's session is launched with for the
// attempt n of task (both zero for a member of a legacy run):
// WayProtocol for a member of a Chat run whose run's Runtime has pipes
// and whose agent's installed version can run as a chat, "" otherwise. A
// Chat run's member kept in its terminal is said in the run's log, with
// why; a run that is not Chat, and a Brakeman turn, asks nothing and says
// nothing. It records the way on m. The caller holds no lock of the
// engine's.
func (e *Engine) launchWay(ctx context.Context, r *run, m *member, task string, n int) string {
	way, why := "", ""
	if r.way == WayChat && r.class != ClassBrakeman {
		way = WayProtocol
		if _, ok := r.rt.(ext.PipeStarter); !ok {
			way, why = "", noPipesWords
		} else if c, ok := e.launcher.(ChatChecker); !ok {
			way, why = "", noCheckerWords
		} else if err := c.ChatCheck(ctx, m.def.AgentID, m.def.Args); err != nil {
			way, why = "", reasonWords(err)
		}
	}
	e.mu.Lock()
	m.way = way
	if why != "" {
		r.note(session.ActivityStatus, "%s: in its terminal: %s", attemptLabel(m.def.Name, task, n), why)
	}
	e.mu.Unlock()
	return way
}

// attemptLabel names a member's attempt in the run's log: the member,
// then the task and the attempt's number for a managed run's.
func attemptLabel(member, task string, n int) string {
	if task == "" {
		return member
	}
	return fmt.Sprintf("%s (task %s, attempt %d)", member, task, n)
}

// memberLabel is attemptLabel of m's current attempt. The caller holds
// e.mu.
func memberLabel(m *member) string {
	return attemptLabel(m.def.Name, m.state.Task, m.state.Attempt)
}

// sentence is words as the run's log puts them after a colon: its first
// letter lower case, unless the word it begins is all capitals, with a
// full stop at the end.
func sentence(words string) string {
	words = strings.TrimSpace(words)
	r, size := utf8.DecodeRuneInString(words)
	if next, _ := utf8.DecodeRuneInString(words[size:]); unicode.IsUpper(r) && !unicode.IsUpper(next) {
		words = string(unicode.ToLower(r)) + words[size:]
	}
	if words != "" && !strings.HasSuffix(words, ".") {
		words += "."
	}
	return words
}

// The run's log's words of a member's move (design 14's table), after the
// member's name and attempt.
const (
	// heldMoveWords follow a trust question's words (heldWords) while it
	// holds a member that moves to a chat at its prompt.
	heldMoveWords = "once answered, it moves to a chat at its prompt"
	// notPromptWords is said once while such a member's screen is
	// something other than its prompt.
	notPromptWords = "its prompt waits: its terminal shows something other than Claude Code's prompt; answer it there."
	// typedFirstWords, notStartedWords: after "in its terminal: ".
	typedFirstWords = "a person typed in it first."
	notStartedWords = "Claude Code did not start a chat."
	// resumedChatWords is a resumed member's move made.
	resumedChatWords = "runs as a chat, on its conversation."
)

// notPromptLooks is how many looks in a row, readyPoll apart, must find a
// member's terminal showing a screen that is neither the agent's prompt
// nor a question before the run's log says its prompt waits (notPromptWords):
// 8 looks, 2 s of a screen that holds.
const notPromptLooks = 8

// recordTerminalWords is what a Chat run resumed from its record alone says:
// the record keeps no way (a run's record keeps the type older builds
// read), and its members run in their terminals.
const recordTerminalWords = "resumed from its record: its members run in their terminals; the record does not keep the run's way"

// ranAsChat says rec, a run's record, ran a member as a chat: its log says
// so (the record keeps no way of its own).
func ranAsChat(rec Run) bool {
	for _, en := range rec.Log {
		if strings.HasSuffix(en.Message, ": runs as a chat.") || strings.HasSuffix(en.Message, ": "+resumedChatWords) {
			return true
		}
	}
	return false
}

// permissionAsks is a chat's permission question in the run's log's
// words, by the question's own (session.permissionWords).
var permissionAsks = map[string]string{
	"Run a command?": "asks to run a command",
	"Change a file?": "asks to change a file",
	"Read a file?":   "asks to read a file",
	"Use a tool?":    "asks to use a tool",
}

// notePermission says in the run's log that member m's chat asks a
// permission question (words, its attention's), and where it is
// answered: "<member>: asks to run a command: answer it on its session
// page." Said for a chat alone: a terminal's questions are said as they
// always were. The caller holds no lock of the engine's.
func (e *Engine) notePermission(r *run, m *member, sessionID, words string) {
	l, ok := e.lookup(sessionID)
	if !ok || !inChat(l) {
		return
	}
	asks, ok := permissionAsks[words]
	if !ok {
		asks = "asks to use a tool"
	}
	e.mu.Lock()
	r.note(session.ActivityStatus, "%s: %s: answer it on its session page.", memberLabel(m), asks)
	e.mu.Unlock()
}

// errMemberEnded says the member's session ended before its move: the
// member ended with it (endedEarly), and nothing more is done.
var errMemberEnded = errors.New("the member's session ended")

// toChat moves member m, whose session local was launched to run as a
// chat (m.way WayProtocol, a launch that resumed no conversation) and
// still runs in its terminal, to its chat (design 14): the terminal's hold
// at a trust question as today, its words saying the member moves once it
// is answered; then the wait for the agent's prompt, with no bound of its
// own; then the move. The one place the move attaches: promptWith's first
// call. It returns nil with the member in its chat or kept in its terminal
// (said in the run's log), its prompt then typed there by today's rule;
// errMemberEnded once the member ended with its session; any other error
// fails the start (both failures' words of a move neither way of which
// started). The caller holds no lock of the engine's.
func (e *Engine) toChat(ctx context.Context, r *run, m *member, local *session.Local) error {
	e.mu.Lock()
	way := m.way
	e.mu.Unlock()
	if way != WayProtocol || inChat(local) {
		return nil
	}
	c, ok := e.launcher.(ChatChecker)
	if !ok {
		return nil
	}
	lines := e.heldLines(r, m.def.Name, local, heldMoveWords)
	switch err := e.await(lines.ctx(ctx), local, lines.held); {
	case errors.Is(err, session.ErrSessionEnded):
		e.endedEarly(r, m, local, "before its prompt was typed")
		return errMemberEnded
	case err != nil && !errors.Is(err, errNotReady):
		return err
	}
	return e.moveMember(ctx, r, m, local, c, lines, true)
}

// moveMember is the move of member m's session local to its chat once its
// terminal shows the agent's prompt: it waits for the prompt with no bound
// of its own (a question shown is held, said once per question by lines;
// another screen is said once), asks the launcher for the move's starts
// with the terminal still running (its checks refuse first), and moves:
// fresh, a new conversation (Local.MoveFresh); else on the conversation
// the launch resumed (Local.MoveResumed). It says in the run's log why a
// member stays in its terminal. The caller holds no lock of the engine's.
func (e *Engine) moveMember(ctx context.Context, r *run, m *member, local *session.Local, c ChatChecker, lines *heldLines, fresh bool) error {
	e.mu.Lock()
	label := memberLabel(m)
	e.mu.Unlock()
	note := func(typ, format string, args ...any) {
		e.mu.Lock()
		r.note(typ, format, args...)
		e.mu.Unlock()
	}
	// said says the other screen's line is written; looks counts the
	// looks in a row that found another screen.
	said, looks := false, 0
	for {
		// The positive sign alone: the agent's prompt by its shape, no
		// question showing. A dialog's words being gone is never it.
		for {
			att := local.Info().Attention
			if att.State == session.AttentionNeedsInput {
				looks = 0
				if att.Source == session.SourceTrust || att.Source == session.SourceQuestion {
					lines.held(att.Message)
				}
			} else if local.AtPrompt() {
				break
			} else {
				// Another screen: said once it has held for notPromptLooks
				// looks in a row, never for a moment (the agent drawing its
				// prompt at its start).
				looks++
				if e.lookedOther != nil {
					e.lookedOther(looks)
				}
				if !said && looks >= notPromptLooks {
					said = true
					note(session.ActivityStatus, "%s: %s", label, notPromptWords)
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-local.Ended():
				e.endedEarly(r, m, local, "before its prompt was typed")
				return errMemberEnded
			case <-time.After(readyPoll):
			}
		}
		starts, err := c.MemberMove(ctx, local, fresh)
		if err != nil {
			note(session.ActivityStatus, "%s: in its terminal: %s", label, reasonWords(err))
			return nil
		}
		// A stop or a cancel during the move ends it at once: the member's
		// session is stopped, which ends the move stopped and starts
		// nothing after it. The engine holds no lock and no transaction
		// across the move: only the run's start (r.starts) for its member.
		stopAt := context.AfterFunc(ctx, func() { stopLocal(local) })
		if fresh {
			err = local.MoveFresh(starts)
		} else {
			err = local.MoveResumed(starts)
		}
		stopAt()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		var refusal *session.MoveRefusal
		switch {
		case err == nil:
			if !fresh {
				note(session.ActivityStatus, "%s: %s", label, resumedChatWords)
			}
			return nil
		case errors.As(err, &refusal) && refusal.Words == session.NotAtPromptYetWords:
			// The screen moved on between the look and the move: the wait
			// goes on.
			continue
		case errors.As(err, &refusal):
			note(session.ActivityStatus, "%s: in its terminal: %s", label, typedFirstWords)
			return nil
		case errors.Is(err, session.ErrMoveFellBack):
			note(session.ActivityStatus, "%s: in its terminal: %s", label, notStartedWords)
			return nil
		case errors.Is(err, session.ErrSessionEnded):
			e.endedEarly(r, m, local, "before its prompt was typed")
			return errMemberEnded
		}
		return fmt.Errorf("its move to a chat: %w", err)
	}
}

// resumedToChat moves member m, resumed on its conversation into session
// local launched to run as a chat, to its chat on that conversation once
// its terminal shows the prompt with nothing typed (design 9's move,
// Local.MoveResumed): no prompt is sent, as for every resumed member; a
// chat that does not start goes back to the terminal on the same
// conversation. The caller holds no lock of the engine's.
func (e *Engine) resumedToChat(ctx context.Context, r *run, m *member, local *session.Local, lines *heldLines) error {
	e.mu.Lock()
	way := m.way
	e.mu.Unlock()
	if way != WayProtocol || inChat(local) {
		return nil
	}
	c, ok := e.launcher.(ChatChecker)
	if !ok {
		return nil
	}
	return e.moveMember(ctx, r, m, local, c, lines, false)
}

// chatNotReady is what follows a chat promptChat finds not ready within
// protocolReadyCap. A member's chat is entered only by its move
// (Local.MoveFresh), which waits for its readiness within that bound and,
// past it, ends the chat and starts the member's terminal afresh, its
// prompt typed there (design 14's one fallback); a chat met unready here
// was entered otherwise. Nothing is sent to it, and the start ends with
// the failure's words. The caller holds no lock of the engine's.
func (e *Engine) chatNotReady(*run, *member, *session.Local) error {
	return sentenceError(fmt.Sprintf("%s It was not ready within %v.", notStartedWords, protocolReadyCap))
}

// sentenceError is an error whose words are sentences a person reads, as
// the run's log quotes them.
type sentenceError string

func (s sentenceError) Error() string { return string(s) }

// awaitChat is AwaitReady for a session in the protocol way: ready once
// the session's child has answered its driver's start (Local.Ready);
// errChatNotReady once chatCap's channel fires first (protocolReadyCap
// when it is nil); session.ErrSessionEnded once the session has ended.
// Nothing is ever typed into a chat "anyway".
func awaitChat(ctx context.Context, l *session.Local, chatCap func() <-chan time.Time) error {
	ready := l.Ready()
	select {
	case <-ready:
		return nil
	default:
	}
	var capped <-chan time.Time
	if chatCap != nil {
		capped = chatCap()
	} else {
		t := time.NewTimer(protocolReadyCap)
		defer t.Stop()
		capped = t.C
	}
	select {
	case <-ready:
		return nil
	case <-l.Ended():
		return session.ErrSessionEnded
	case <-ctx.Done():
		return ctx.Err()
	case <-capped:
		select {
		case <-ready:
			return nil
		default:
		}
		return errChatNotReady
	}
}

// chatSent is how a chat's prompt ended (Submission.Finished).
type chatSent struct {
	res session.SubmitResult
	err error
}

// promptChat is promptWith for a member whose session runs as a chat:
// once the session is ready (AwaitReady's protocol branch), text, the
// role prompt or the task line, is sent once through Submit and waited
// for: m is prompted, and running, at the agent's acknowledgement
// (Finished's Entered), never at the write. A prompt the agent did not
// confirm (Unconfirmed) is never sent again: m waits, said in the run's
// log, until its agent reports a turn of its own, or a person ends the
// attempt. A session that ends first ends m, as in the terminal. The
// caller holds no lock of the engine's.
func (e *Engine) promptChat(ctx context.Context, r *run, m *member, local *session.Local, text string) error {
	name := m.def.Name
	err := e.await(ctx, local, func(string) {})
	switch {
	case errors.Is(err, session.ErrSessionEnded):
		e.endedEarly(r, m, local, "before its prompt was sent")
		return nil
	case errors.Is(err, errChatNotReady):
		return e.chatNotReady(r, m, local)
	case err != nil:
		return err
	}
	// The attempt read again after the move: one cancelled or stopped
	// during it (its context ends once the cancel is committed) is sent
	// nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	if sm, ok := e.bySession.Load(local.Info().ID); ok {
		if a := sm.(sessionMember).a; a != nil && !a.closedAt.IsZero() {
			e.mu.Unlock()
			return context.Canceled
		}
	}
	r.note(session.ActivityStatus, "%s: runs as a chat.", memberLabel(m))
	e.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		e.mu.Lock()
		m.markPrompted()
		m.state.Status = MemberRunning
		r.touch()
		e.mu.Unlock()
		m.poke()
		return nil
	}
	sent := make(chan chatSent, 1)
	_, err = local.Submit(ctx, session.Submission{Text: text, ByName: typedBy, Finished: func(res session.SubmitResult, err error) {
		sent <- chatSent{res, err}
	}})
	switch {
	case errors.Is(err, session.ErrSubmitPending):
	case errors.Is(err, session.ErrSessionEnded) || isClosed(local.Ended()):
		e.endedEarly(r, m, local, "before its prompt was sent")
		return nil
	case err != nil:
		return fmt.Errorf("sending its prompt: %w", err)
	default:
		// A chat answers every message pending: the session left its chat
		// as the prompt was handed over.
		return errors.New("sending its prompt: the session no longer runs as a chat")
	}
	var got chatSent
	select {
	case got = <-sent:
	case <-ctx.Done():
		return ctx.Err()
	}
	switch {
	case got.res.Entered:
		e.mu.Lock()
		m.markPrompted()
		m.state.Status = MemberRunning
		r.note(session.ActivityStatus, "sent %s's prompt", name)
		e.mu.Unlock()
		m.poke()
		return nil
	case got.res.Unconfirmed:
		took := make(chan struct{})
		e.mu.Lock()
		m.state.Unconfirmed, m.took = local.Info().ID, took
		r.note(session.ActivityError, "sent %s's prompt, and its agent did not confirm taking it: it is never sent again; look at its chat", name)
		e.mu.Unlock()
		select {
		case <-took:
		case <-local.Ended():
			e.endedEarly(r, m, local, "before its agent took its prompt")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
		e.mu.Lock()
		m.markPrompted()
		m.state.Status = MemberRunning
		r.note(session.ActivityStatus, "%s's agent works on its prompt", name)
		e.mu.Unlock()
		m.poke()
		return nil
	case errors.Is(got.err, session.ErrSessionEnded) || isClosed(local.Ended()):
		e.endedEarly(r, m, local, "before its prompt was sent")
		return nil
	}
	return fmt.Errorf("sending its prompt: %w", got.err)
}

// tookUnconfirmed marks that the agent of m's chat reported a turn of its
// own after a prompt it did not confirm (promptChat). The caller holds
// e.mu.
func (m *member) tookUnconfirmed() {
	if m.took != nil {
		close(m.took)
		m.took = nil
	}
}

// inChat says l runs as a chat now.
func inChat(l *session.Local) bool { return l.Way() == session.WayProtocol }

// chatBusyWords is why a line for a member's chat waits, after its name in
// the run's log: lines are sent between its agent's turns.
const chatBusyWords = "is in a turn: it is sent when the turn ends"
