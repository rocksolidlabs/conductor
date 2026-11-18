package session

import (
	"regexp"
	"testing"
)

// The words a startup question was asked with are told apart by where
// they were read: the update dialog's heading is its own words, its first
// choice's row is not, and a question without words of its own (the
// trust question, told by what its pattern matched) never reads as a
// choice's.
func TestWordsFromChoiceTellsAChoicesRowFromTheHeading(t *testing.T) {
	s, _ := newLocalWith(t, quiet(Options{
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		Questions:    []StartupQuestion{codexUpdateQuestion()},
	}))
	for words, want := range map[string]bool{
		"Update available 0.163.0 → 0.164.0": false,
		"Update now (runs":                   true,
		"Trust this folder?":                 false,
		"something else":                     false,
	} {
		if got := s.WordsFromChoice(words); got != want {
			t.Errorf("%q: %v, want %v", words, got, want)
		}
	}
}
