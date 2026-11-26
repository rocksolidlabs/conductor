package session

import (
	"context"
	"errors"
)

// A choice's keys on a chat (#1150). A session in the protocol way has no
// terminal, so keys are refused there (ErrNotTerminal); but a page, a link's
// lane or a provider that does not know how the session runs answers a
// question with a choice's keys, as it would a terminal's. A reply that names
// the question shown and carries exactly the keys of one of its choices is
// that choice: its words are sent as the person's answer, as a line answering
// the question is (Submit with Question), through the same guards, claim,
// move lock and record. Any other keys stay refused.

// chatChoice answers question, on a session in the protocol way, with the
// choice whose keys data are, typed by who: ErrReadOnly for a typist that
// may not type; the question's own refusal when it no longer waits
// (waitsLocked: answered, claimed, gone, a decision's); ErrNotTerminal for
// keys that are not exactly one choice's of the question shown, as for any
// keys on a chat. Taken, the choice's label is Submit's text, as by who (a
// viewer's subscription, or an actor's name), with who's request id,
// withdrawal and hold; the reply is the line's (AnswerWord): a viewer is
// answered ErrSubmitPending at once and told its word through finished, as
// its line would be; an actor (AnswerAs) waits for the word within ctx, as
// a link's line is waited for, and is answered ErrSubmitPending when ctx
// ends first.
func (s *Local) chatChoice(ctx context.Context, who typist, question string, data []byte, finished func(error)) error {
	if !who.controls() {
		return ErrReadOnly
	}
	s.mu.Lock()
	err := s.waitsLocked(question, "")
	label, ok := "", false
	if err == nil {
		label, ok = choiceLabel(s.info.Attention, question, data)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotTerminal
	}
	words := make(chan error, 1)
	sub := Submission{Text: label, Question: question, ReqID: who.reqID, Withdrawn: who.gone, Hold: who.hold,
		Finished: func(res SubmitResult, err error) {
			w := AnswerWord(res, err)
			if finished != nil {
				finished(w)
			}
			words <- w
		}}
	if who.actor {
		sub.ByName = who.name
	} else {
		sub.By = who.sub
	}
	if _, err := s.Submit(ctx, sub); !errors.Is(err, ErrSubmitPending) {
		return err
	}
	if !who.actor {
		return ErrSubmitPending
	}
	select {
	case w := <-words:
		return w
	case <-ctx.Done():
		return ErrSubmitPending
	}
}

// choiceLabel is the label of the one choice of att, the question named
// question and shown, whose keys are exactly data; false when att is
// another question, when no choice or more than one has those keys, or
// when the choice's label says nothing once made a line.
func choiceLabel(att Attention, question string, data []byte) (string, bool) {
	if att.ID != question || len(data) == 0 {
		return "", false
	}
	label, n := "", 0
	for _, o := range att.Options {
		if o.Input != "" && o.Input == string(data) {
			label = o.Label
			n++
		}
	}
	if n != 1 || ProtocolLine(label) == "" {
		return "", false
	}
	return label, true
}
