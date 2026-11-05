package ext

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// invalid is a Valid method's refusal: the record and what is wrong with it.
func invalid(record, format string, args ...any) error {
	return fmt.Errorf("ext: %s: "+format, append([]any{record}, args...)...)
}

// slug reports whether s is 1 to max of a-z, 0-9 and '-', beginning with a
// letter, or with a letter or a digit unless letterFirst.
func slug(s string, max int, letterFirst bool) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 && letterFirst {
				return false
			}
		case c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// printableASCII reports whether s is printable ASCII with no space.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// oneLine reports whether s is valid UTF-8 with no control character: no
// newline, no tab, no escape.
func oneLine(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// text reports whether s is valid UTF-8 whose only control characters are
// tabs and line ends.
func text(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
	})
}

// line checks a one-line field of at most max bytes; required refuses "".
func line(record, field, s string, max int, required bool) error {
	switch {
	case s == "" && required:
		return invalid(record, "%s is empty", field)
	case len(s) > max:
		return invalid(record, "%s is %d bytes, past %d", field, len(s), max)
	case !oneLine(s):
		return invalid(record, "%s is not one line of UTF-8 text", field)
	}
	return nil
}

// name checks a one-line name of 1 to max characters.
func name(record, field, s string, max int) error {
	switch {
	case s == "":
		return invalid(record, "%s is empty", field)
	case !oneLine(s):
		return invalid(record, "%s is not one line of UTF-8 text", field)
	case utf8.RuneCountInString(s) > max:
		return invalid(record, "%s is past %d characters", field, max)
	}
	return nil
}

// typed checks a line to type or an answer's text: 1 to MaxAnswerText bytes
// of UTF-8 text whose only control characters are tabs and line ends (what
// a viewer's reply box may hold; never an escape a program would read as
// keys).
func typed(record, field, s string) error {
	switch {
	case s == "":
		return invalid(record, "%s is empty", field)
	case len(s) > MaxAnswerText:
		return invalid(record, "%s is %d bytes, past %d", field, len(s), MaxAnswerText)
	case !text(s):
		return invalid(record, "%s is not UTF-8 text", field)
	}
	return nil
}

// agentID reports whether s is a catalog agent's id: 1 to MaxAgentID of
// a-z, 0-9 and '-'.
func agentID(s string) bool {
	if s == "" || len(s) > MaxAgentID {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// model reports whether s is a model's name: 1 to MaxModel of letters,
// digits and ._:/@-, beginning with a letter or a digit.
func model(s string) bool {
	if s == "" || len(s) > MaxModel {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && strings.IndexByte("._:/@-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// digits reports whether s is a number from 1 of at most max digits, with
// no leading zero.
func digits(s string, max int) bool {
	if s == "" || len(s) > max || s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// requestID reports whether s is a Brakeman request's id, br-<n>.
func requestID(s string) bool {
	n, ok := strings.CutPrefix(s, "br-")
	return ok && len(s) <= MaxRequestRef && digits(n, MaxRequestRef-3)
}

// ticketKey reports whether s is a ticket's key: a project key (an
// upper-case letter, then 1 to 5 upper-case letters or digits), a dash and
// a number of at most 18 digits.
func ticketKey(s string) bool {
	project, n, ok := strings.Cut(s, "-")
	if !ok || len(project) < 2 || len(project) > 6 || project[0] < 'A' || project[0] > 'Z' {
		return false
	}
	for i := 1; i < len(project); i++ {
		if c := project[i]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return digits(n, 18)
}

// lowerHex reports whether s is n lower-case hex digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// opID reports whether s is an operation's id: 1 to MaxOpID of letters,
// digits and ._~-.
func opID(s string) bool {
	if s == "" || len(s) > MaxOpID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._~-", c) >= 0:
		default:
			return false
		}
	}
	return true
}
