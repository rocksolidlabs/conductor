package session

// WordsFromChoice reports whether words, a startup question's as the
// session asked it (Attention.Message), are the row the question's dialog
// is found by rather than its own words: a startup question with words of
// its own (StartupQuestion.Words, the catalog's words: Codex's update
// heading) whose Pattern (a choice's row: "Update now (runs") matches
// words while Words does not, as when the dialog was read off a screen
// that did not show its heading. It is information about the words alone,
// for what writes them down (a run's log): nothing the session asks,
// holds, refuses or writes depends on it.
func (s *Local) WordsFromChoice(words string) bool {
	for _, q := range s.questions {
		if q.Words != nil && q.Pattern.MatchString(words) && !q.Words.MatchString(words) {
			return true
		}
	}
	return false
}
