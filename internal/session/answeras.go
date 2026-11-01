package session

import (
	"context"
	"errors"
)

// An answer with no connection behind it (the decision seam, Program item
// C5): a decision router, or a provider through its ext.Host, answers a
// question with one of its choices as a viewer's reply does (AnswerInput),
// through the same writer, guards and claim, so the first answer still
// wins, a held turn still refuses, and no keys but the question's own
// choices are ever written for it.

// MaxKept bounds Info.Kept: the agent's own question set aside and the
// question a held turn took the place of.
const MaxKept = 2

// Actor is who answers a question with no connection to the session: a
// principal of a provider's, by its subject and display name. Withdrawn,
// when set, is closed once the actor no longer stands (an extension's Host
// whose try was given up): as a revoked link's does, it withdraws keys
// not yet written.
type Actor struct {
	Subject   string
	Name      string
	Withdrawn <-chan struct{}
}

// ErrNotAChoice refuses keys AnswerAs is given that are not one of the
// question's choices (Attention.Options, Input): nothing was written.
var ErrNotAChoice = errors.New("session: the keys are not one of the question's choices: nothing was sent")

// AnswerAs answers the question named question with input, the keys of
// one of its choices, by the actor by: as AnswerInput does for a viewer,
// through the same writer, guards and claim (questionWaits, stillOnScreen,
// claimQuestion, resolve). The keys are written only while that question
// is the one shown, nobody is answering it and nobody answered it in the
// terminal, and only when they equal the Input of one of its choices,
// looked at as they are queued and again as the writer takes them;
// otherwise nothing is written: ErrQuestionAnswered for a question
// answered, gone, held over by a held turn or answered in the terminal,
// ErrNotAChoice for other keys. Written, they resolve the question as by's
// name (Answer.ByName, the input entry, the chat's answered line), with no
// subscriber behind it (Answer.By empty). A line answering a question is
// Submit with Question and ByName.
func (s *Local) AnswerAs(ctx context.Context, question, input string, by Actor) error {
	if question == "" {
		return ErrQuestionAnswered
	}
	who := typist{actor: true, name: CleanName(by.Name), gone: by.Withdrawn}
	return s.input(ctx, who, question, []byte(input), writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait}, nil)
}

// typist is who types keys into the session (input): a viewer's
// subscription, or an actor with no connection (AnswerAs), which no link
// withdraws and the roster does not show.
type typist struct {
	sub   *Subscription
	actor bool
	name  string
	gone  <-chan struct{}
}

// viewer is the typist sub.
func viewer(sub *Subscription) typist { return typist{sub: sub} }

// controls reports whether the typist may type: a controller, or an actor.
func (t typist) controls() bool { return t.actor || t.sub.Role == RoleControl }

// id is the subscriber's id, "" for an actor.
func (t typist) id() string {
	if t.actor {
		return ""
	}
	return t.sub.ID
}

// displayName is the subscriber's display name, or the actor's.
func (t typist) displayName() string {
	if t.actor {
		return t.name
	}
	return t.sub.Name
}

// typesSomething reports whether data types anything, a terminal's own
// report aside: a viewer's carries what its last write left of one.
func (t typist) typesSomething(data []byte) bool {
	if t.actor {
		typed, _ := typesSomething(nil, data)
		return typed
	}
	return t.sub.typesSomething(data)
}

// mayType reports whether the typist may write data as the answer to the
// question named question: a viewer may write any keys (its reply's
// choices are the page's); an actor only the Input of one of the choices
// of that question, shown (ErrNotAChoice otherwise).
func (t typist) mayType(s *Local, question string, data []byte) error {
	if !t.actor {
		return nil
	}
	s.mu.Lock()
	att := s.info.Attention
	s.mu.Unlock()
	if att.ID != question {
		return ErrQuestionAnswered
	}
	if !isChoice(att, data) {
		return ErrNotAChoice
	}
	return nil
}

// keptLocked is the ids of the questions the session keeps without
// showing them (Info.Kept): the agent's own question while it is not the
// one shown (set aside, or held over), and the question the hold of the
// turn took the place of (heldOver), each once, at most MaxKept. The
// caller holds s.mu.
func (s *Local) keptLocked(shown string) []string {
	var kept []string
	for _, id := range [MaxKept]string{s.own.ID, s.heldOver.ID} {
		if id != "" && id != shown && (len(kept) == 0 || kept[0] != id) {
			kept = append(kept, id)
		}
	}
	return kept
}
