package session

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// CleanSessionName holds a name a person gives a session (a rename, PATCH
// /api/sessions/{id}) to what its listings show: its control characters
// dropped (C0, DEL and C1), the space around it trimmed, and cut to
// proto.MaxSessionName bytes at a character boundary. "" when nothing is
// left, which a rename refuses.
func CleanSessionName(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	return cutBytes(s, proto.MaxSessionName)
}

// CutSessionName holds a launch's name (POST /api/sessions, a crew member's)
// to the bound a rename's has: the space around it trimmed and cut to
// proto.MaxSessionName bytes at a character boundary, never inside one,
// which JSON would turn into U+FFFD and make longer than the bound.
func CutSessionName(s string) string {
	return cutBytes(strings.TrimSpace(s), proto.MaxSessionName)
}

// Rename gives the session name, cleaned already (CleanSessionName): its
// Info's from now on, and the one a resume of it starts under (Launched). A
// name that changes notifies OnChange, which takes it to the listings and
// the events stream.
func (s *Local) Rename(name string) {
	s.mu.Lock()
	changed := s.info.Name != name
	s.info.Name = name
	s.renamed = true
	s.mu.Unlock()
	if changed {
		s.notifyChange()
	}
}

// CutAtWord is the one rule for a text cut at a word (a session named by
// its first message, a ticket's launch label): s whole when it is at most
// chars characters and, when bytes is not 0, at most bytes bytes; else cut
// to chars - 1 characters (and bytes - 3 bytes) at the last space after
// the first keep characters, the spaces before the cut trimmed, and an
// ellipsis (U+2026) added, so that the result is inside both bounds. A
// first word past the bound (no space after keep) is cut at a character.
// testdata/cut-at-word.json holds its cases, for this copy, dispatch's
// and the app's.
func CutAtWord(s string, keep, chars, bytes int) string {
	if utf8.RuneCountInString(s) <= chars && (bytes == 0 || len(s) <= bytes) {
		return s
	}
	r := []rune(s)
	r = r[:min(len(r), max(chars-1, 0))]
	if bytes > 0 {
		for len(r) > 0 && len(string(r)) > bytes-len("…") {
			r = r[:len(r)-1]
		}
	}
	for i := len(r) - 1; i > keep; i-- {
		if r[i] == ' ' {
			r = r[:i]
			break
		}
	}
	return strings.TrimRight(string(r), " ") + "…"
}

// NameFromMessage is the name of a session launched with a first message
// and no name of its own: the message's first words, its control
// characters made spaces and each run of spaces one, cut at a word to
// maxNameChars characters and proto.MaxSessionName bytes (CutAtWord). ""
// when nothing is left: the launch's own "<agent> #N" then.
func NameFromMessage(msg string) string {
	msg = strings.Join(strings.FieldsFunc(msg, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	return CutAtWord(msg, 0, maxNameChars, proto.MaxSessionName)
}

// maxNameChars bounds a name made from a first message, in characters:
// what a sidebar row shows of one.
const maxNameChars = 40
