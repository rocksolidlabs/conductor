package crew

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch's first message (POST /api/sessions with message, P19): the
// line a person launched a session with, typed into its agent only at the
// agent's own recognised prompt (its catalog entry's prompt shape), once
// the session is ready by the rule a crew member's prompt waits on
// (AwaitReady), held behind every startup question, and withdrawn by a
// person's own keys first.

// FirstMessage is what TypeFirstMessage types, and how.
type FirstMessage struct {
	// Text is the line; Submit makes its line breaks and tabs spaces.
	Text string
	// PromptShape says the session's agent has a recognised prompt
	// (catalog.Agent.PromptShape): the line is typed, and entered, only
	// while that prompt is what the screen shows (OnlyAtPrompt), before
	// the readiness cap and after it. Without one nothing is typed: the
	// server takes no first message for such an agent.
	PromptShape bool
	// Wait is the readiness wait, AwaitReady when nil.
	Wait func(ctx context.Context, l *session.Local, held func(question string)) error
	// Note records what a person should know of the line (it waits on a
	// question, it was not typed): the session's Activity, as a status
	// entry, when nil.
	Note func(string)
	// poll is how long a refused try waits before the next; readyPoll
	// when zero.
	poll time.Duration
}

// FirstMessageBy is the name a first message is typed under: Conductor's
// own line, as a crew's prompt is the crew's.
const FirstMessageBy = "launch"

// TypeFirstMessage waits until l is ready and types fm's text into it,
// with its Enter, as Conductor's own line. It returns once the text is
// typed, or will not be: the session ended (noted), a person typed in it
// first (l.PersonTyped: noted, nothing typed, or the Enter left out), or
// ctx ended (a server stopping: nothing typed, nothing noted). Every
// startup question holds it while it shows, noted once each; a question
// Submit finds first holds it too, tried again a poll later. It clears
// the session's FirstMessage as it returns.
func TypeFirstMessage(ctx context.Context, l *session.Local, fm FirstMessage) {
	// "waiting" ends as it returns, but for a line typed and not sent
	// (MarkFirstMessageTyped), which a person's key ends.
	typed := false
	defer func() {
		if !typed {
			l.SetFirstMessage("")
		}
	}()
	note := fm.Note
	if note == nil {
		note = func(m string) { l.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: m}) }
	}
	if !fm.PromptShape {
		// No prompt the server recognises: no screen is the one to type
		// into.
		note("the first message was not typed: Conductor does not know this agent's prompt")
		return
	}
	wait := fm.Wait
	if wait == nil {
		wait = AwaitReady
	}
	poll := fm.poll
	if poll == 0 {
		poll = readyPoll
	}
	// A person's keys end the wait at once, nothing typed.
	person := l.PersonTyped()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-person:
			cancel()
		case <-ctx.Done():
		}
	}()
	typedFirst := func() bool {
		select {
		case <-person:
			note("the first message was not typed: a person typed in the session first")
			return true
		default:
			return false
		}
	}
	ended := func() { note("the first message was not typed: the session ended first") }
	capped, noted, unread := false, false, false
	for {
		if !capped {
			err := wait(ctx, l, func(question string) {
				note(fmt.Sprintf("the first message waits: answer %q first", question))
			})
			switch {
			case errors.Is(err, errNotReady):
				// Past the cap: tried at each poll, at the prompt alone.
				capped = true
			case errors.Is(err, session.ErrSessionEnded):
				ended()
				return
			case err != nil:
				typedFirst()
				return
			}
		}
		res, err := l.Submit(ctx, session.Submission{Text: fm.Text, ByName: FirstMessageBy, Confirm: true, OnlyAtPrompt: true, PromptHoldsText: true, Withdrawn: person, WithdrawnByKeys: true})
		switch {
		case err == nil && !res.Entered && res.Typed && res.NotAtPrompt:
			// No question came: the screen was not the prompt holding the
			// text, and Conductor does not press Enter into what it does
			// not recognise.
			note("the first message is typed in the agent's input and was not sent: press Enter in the terminal to send it")
			l.MarkFirstMessageTyped()
			typed = true
			return
		case err == nil && !res.Entered && res.Typed:
			note("the first message was typed without its Enter: answer the question, then press Enter in the terminal")
			l.MarkFirstMessageTyped()
			typed = true
			return
		case err == nil, errors.Is(err, session.ErrSubmitPending):
			return
		case errors.Is(err, session.ErrSessionEnded):
			ended()
			return
		case res.Typed:
			// Withdrawn once its text was typed: its Enter left out.
			note("the first message was typed without its Enter: a person typed in the session first")
			return
		case typedFirst():
			return
		case errors.Is(err, session.ErrNotAtPrompt):
			if !noted {
				noted = true
				note("the first message waits for the agent's prompt: the screen is not one Conductor knows")
			}
		case errors.Is(err, session.ErrTrustQuestion), errors.Is(err, session.ErrQuestion):
			// A question the wait had not seen yet holds it: looked at
			// again a poll later, through the wait while it is not capped.
			// A dialog the session does not read whole asks nothing and
			// holds it all the same (session.Local.UnreadDialog): said
			// once, answered in the terminal.
			if !unread && l.UnreadDialog() {
				unread = true
				note("the first message waits: the agent shows a dialog Conductor does not read whole; answer it in its terminal")
			}
		default:
			return
		}
		select {
		case <-ctx.Done():
			typedFirst()
			return
		case <-l.Ended():
			ended()
			return
		case <-time.After(poll):
		}
	}
}
