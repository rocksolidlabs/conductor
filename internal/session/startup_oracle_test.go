package session

import "strings"

// The two guards a line had while the startup questions' watch lasted,
// before one rule took their place (startupLine), and atPrompt's rule as
// it was: kept here, as they were, as the oracles the rule is held to
// (TestTheStartupLineRefusesWhereEitherGuardDid). They are never to be
// changed to make another test pass.

// startupOnScreen is the guard a line's text had: a whole frame (DrawnAt)
// of the screen the questions are read from shows a startup question.
func (s *Local) startupOnScreen() bool {
	if s.trust == nil || !s.trust.Watching() || s.asked == nil {
		return false
	}
	whole, _, _, _ := s.asked.Whole()
	uncut, _, _ := s.asked.DrawnAt()
	return uncut && s.lastStartup(whole) != ""
}

// oracleMayShow is the guard a line's Enter had (startupMayShow as it
// was): the frame shows a startup question or a numbered choice, or is not
// whole while the output's tail saw a startup question drawn.
func (s *Local) oracleMayShow() bool {
	if s.trust == nil || !s.trust.Watching() || s.asked == nil {
		return false
	}
	whole, _, uncut, _, _ := s.asked.Frame()
	if !uncut {
		return s.lastStartup(s.trust.Text()) != ""
	}
	if s.lastStartup(whole) != "" {
		return true
	}
	return oracleChoiceShown(whole)
}

// oracleChoiceShown is startupChoiceShown as it was.
func oracleChoiceShown(whole string) bool {
	for _, row := range strings.Split(whole, "\n") {
		if choiceRow.MatchString(row) {
			return true
		}
	}
	return false
}

// oracleAtPrompt is atPrompt as it was: a whole frame of the screen a
// submission's prompt is looked for on, the agent's prompt by its shape
// when it has one, and no numbered choice, read with plain spaces.
func (s *Local) oracleAtPrompt() bool {
	text, _, uncut, _, _ := s.shot.Frame()
	if !uncut {
		return false
	}
	text = plainSpaces(text)
	if s.opts.PromptShape != nil && !s.opts.PromptShape.MatchString(text) {
		return false
	}
	return !oracleChoiceShown(text)
}
