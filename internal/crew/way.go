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
// whether the installed version of a member's agent can run as a chat.
// The API server answers it from the agent's catalog entry and its probed
// version. A Launcher without it runs every member in its terminal.
type ChatChecker interface {
	// ChatQualified answers nil when agentID's installed version can run
	// as a chat, else an error whose words say why in one sentence (the
	// run's log quotes them).
	ChatQualified(ctx context.Context, agentID string) error
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
		} else if err := c.ChatQualified(ctx, m.def.AgentID); err != nil {
			way, why = "", sentence(err.Error())
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

// toChat is where a member whose session was launched to run as a chat
// (m.way, WayProtocol) and still runs in its terminal is moved to its chat,
// once its terminal's readiness hold at a trust question has let go: the
// one place the move attaches. Today it moves nothing, and the member
// goes on the way its session runs. The caller holds no lock of the
// engine's.
func (e *Engine) toChat(context.Context, *run, *member, *session.Local) error {
	return nil
}

// chatNotReady is what follows a member's chat that was not ready within
// protocolReadyCap: nothing is sent to it, and the attempt's start ends
// with the failure's words, which promptWith returns. The caller holds no
// lock of the engine's.
func (e *Engine) chatNotReady(*run, *member, *session.Local) error {
	return sentenceError(fmt.Sprintf("%s It was not ready within %v.", session.ChatNotStartedWords, protocolReadyCap))
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
	e.mu.Lock()
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
