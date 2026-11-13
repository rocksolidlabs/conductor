package dispatch

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Storable and the store's own check of a read's text agree: whatever
// Storable makes of one line, over every byte and a sample of runes (the
// controls, the line's reorderings, a replacement, a separator, a letter
// of two, three and four bytes) and each cut at every bound, checkText
// accepts; nothing of one line keeps a reordering character, and a text of
// lines keeps only \n and \t of the controls, and its reorderings.
func TestStorableAgreesWithCheck(t *testing.T) {
	var samples []string
	for b := range 256 {
		samples = append(samples, string([]byte{byte(b)}))
	}
	for _, r := range []rune{0x7f, 0x80, 0x85, 0x9f, 0x200b, 0x200e, 0x2028, 0x2029, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069,
		0xfeff, 0xfffd, 'é', '中', 0x1f600} {
		samples = append(samples, string(r))
	}
	reorders := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 })
	}
	for _, x := range samples {
		s := "a" + x + "b" + strings.Repeat(x, 3)
		for n := 1; n <= len(s)+1; n++ {
			one := Storable(s, n, false)
			if err := checkText("a text", one, n); err != nil || reorders(one) || !utf8.ValidString(one) {
				t.Errorf("%q, %d: %q %v", s, n, one, err)
			}
			many := Storable(s, n, true)
			if len(many) > n || !utf8.ValidString(many) || strings.ContainsFunc(many, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
				t.Errorf("%q, %d, lines: %q", s, n, many)
			}
		}
	}
	const rlo = "\xe2\x80\xae" // U+202E
	if got := Storable("line"+rlo+"one\nand\ttwo\x1b[2J", 100, true); got != "line"+rlo+"one\nand\ttwo[2J" {
		t.Errorf("lines: %q", got)
	}
	if got := Storable("line"+rlo+"one\nand\ttwo\x1b[2J", 100, false); got != "lineoneandtwo[2J" {
		t.Errorf("one line: %q", got)
	}
	for _, lines := range []bool{false, true} {
		if got := Storable("a\U000E0000\U000E0041\U000E007Fb", 100, lines); got != "ab" {
			t.Errorf("tag characters, lines %v: %q", lines, got)
		}
	}
}

// FitIncomplete gives what the store's own check keeps, whatever the
// reasons: eight of quotes, backslashes or short scheme:// tokens at their
// bound, more than eight, none, or all emptied by Storable; a set it cut
// ends saying so.
func TestFitIncomplete(t *testing.T) {
	long := func(s string) []string {
		return slices.Repeat([]string{strings.Repeat(s, MaxIncompleteWord)}, MaxIncompleteWords)
	}
	for what, words := range map[string][]string{"quotes": long(`"`), "backslashes": long(`\`), "schemes": long("x:// "), "nine": slices.Repeat([]string{"w"}, 9),
		"none": nil, "emptied": {"\x1b", "\u202e"}} {
		in := FitIncomplete(words, 3)
		if _, err := encodeIncomplete(*in); err != nil {
			t.Errorf("%s: %v", what, err)
		}
		if len(in.Words) < len(words) && in.Words[len(in.Words)-1] != incompleteRest {
			t.Errorf("%s: %d of %d kept, ending %q", what, len(in.Words), len(words), in.Words[len(in.Words)-1])
		}
	}
}
