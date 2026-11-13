package dispatch

import (
	"fmt"
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

// FitChecks gives what the store's own check of a read keeps, measured by
// the one encoder both use (encodeChecks): over a hundred checks whose
// names hold 6 to 14 short scheme tokens (each kept as words, longer),
// so that the set as kept walks across MaxLinkChecksJSON, with failures
// among them or alone, over names of 0 and 39 tokens, and over more
// checks than MaxLinkChecks,
// the fitted read always passes encodeLinkRead. A set that passes it
// already is given back as it is, its reasons as FitIncomplete fits them;
// a cut set is the set ordered failures first and cut at its end, no
// shorter than fits (one more is past the bound), the cut said last and
// counted with the omitted given. A set the store's check refuses for its
// texts is given back as it is.
func TestFitChecks(t *testing.T) {
	read := func(checks []LinkCheck, in *ChecksIncomplete) LinkRead {
		return LinkRead{System: "github", Host: "github.com", Kind: ExternalChange, NodeID: "PR_fit", State: "open", HeadSha: strings.Repeat("a", 40),
			UpdatedAt: day0, Checks: checks, Incomplete: in}
	}
	set := func(n, tokens int, failed func(int) bool) []LinkCheck {
		var l []LinkCheck
		for i := range n {
			c := LinkCheck{ID: fmt.Sprintf("status:%d", 7000+i), Name: strings.Repeat("x:// ", tokens) + fmt.Sprintf("s%02d", i%100), Status: "completed",
				Conclusion: "success", URL: fmt.Sprintf("https://ci.example.com/octo/app/commit-statuses/%024d", i), App: "status", CompletedAt: day0}
			if failed(i) {
				c.Conclusion = "failure"
			}
			l = append(l, c)
		}
		return l
	}
	failures := map[string]func(int) bool{"none failed": func(int) bool { return false }, "every seventh failed": func(i int) bool { return i%7 == 3 },
		"all failed": func(int) bool { return true }}
	// The walk: a hundred checks of 6 to 14 tokens a name, across the
	// bound as kept, with failures among them or alone; then the ends (0
	// tokens, and 39, the most a name of MaxCheckName holds as read) and
	// more checks than MaxLinkChecks.
	type size struct {
		what  string
		n, k  int
		walk  bool
		fails func(int) bool
	}
	var sizes []size
	for k := 6; k <= 14; k++ {
		for _, what := range []string{"every seventh failed", "all failed"} {
			sizes = append(sizes, size{what, MaxLinkChecks, k, true, failures[what]})
		}
	}
	sizes = append(sizes, size{"none failed", MaxLinkChecks, 0, false, failures["none failed"]}, size{"none failed", MaxLinkChecks, 39, false, failures["none failed"]},
		size{"every seventh failed", MaxLinkChecks + 20, 0, false, failures["every seventh failed"]})
	rank := func(c LinkCheck) int {
		if c.Conclusion == "failure" {
			return 0
		}
		return 1
	}
	whole, cut := 0, 0 // the walk's sets within the bound and cut
	for _, s := range sizes {
		checks := set(s.n, s.k, s.fails)
		words := []string{"a reason the read gave"}
		kept, in := FitChecks(checks, words, 2)
		if _, _, err := encodeLinkRead(read(kept, in)); err != nil {
			t.Fatalf("%s, %d checks of %d tokens: the fitted read is refused: %v", s.what, s.n, s.k, err)
		}
		if _, _, err := encodeLinkRead(read(checks, nil)); err == nil {
			if s.walk {
				whole++
			}
			if !slices.Equal(kept, checks) || !slices.Equal(in.Words, words) || in.Omitted != 2 {
				t.Fatalf("%s, %d checks of %d tokens, within the bound: %d kept, %+v", s.what, s.n, s.k, len(kept), in)
			}
			continue
		}
		if s.walk {
			cut++
		}
		order := slices.Clone(checks)
		slices.SortStableFunc(order, func(a, b LinkCheck) int { return rank(a) - rank(b) })
		left := s.n - len(kept)
		enc, _ := encodeChecks(order[:len(kept)+1])
		switch {
		case left <= 0 || !slices.Equal(kept, order[:len(kept)]):
			t.Fatalf("%s, %d checks of %d tokens: %d kept, not the first of the set failures first", s.what, s.n, s.k, len(kept))
		case len(kept) < MaxLinkChecks && len(enc) <= MaxLinkChecksJSON:
			t.Fatalf("%s, %d checks of %d tokens: %d kept, and one more fits (%d bytes)", s.what, s.n, s.k, len(kept), len(enc))
		case in == nil || in.Omitted != 2+left || in.Words[0] != words[0] ||
			in.Words[len(in.Words)-1] != fmt.Sprintf("%d checks left out to keep the list within %d checks and %d KiB, failures kept first", left, MaxLinkChecks, MaxLinkChecksJSON>>10):
			t.Fatalf("%s, %d checks of %d tokens: the cut said %+v", s.what, s.n, s.k, in)
		}
	}
	if whole == 0 || cut == 0 {
		t.Fatalf("the walk did not cross the bound: %d sets within it, %d cut", whole, cut)
	}
	if kept, in := FitChecks(set(3, 0, failures["none failed"]), nil, 0); len(kept) != 3 || in != nil {
		t.Fatalf("a set within the bound with no reason: %d kept, %+v", len(kept), in)
	}
	bad := set(MaxLinkChecks, 39, failures["none failed"])
	bad[MaxLinkChecks-1].Name = strings.Repeat("n", MaxCheckName+1)
	if kept, _ := FitChecks(bad, nil, 0); !slices.Equal(kept, bad) {
		t.Fatalf("a set the store's check refuses for a text was cut to %d", len(kept))
	}
}

// FitChecks gives back a set the store's check refuses for a text as it
// is, whatever its count: 120 checks whose names grow as kept (18 short
// scheme tokens each), every seventh failed, one of them with a name past
// MaxCheckName at the start, the middle, the last place within
// MaxLinkChecks and past it, come back as the same slice with the reasons
// as they came, and the store's check refuses a read of them; the same
// 120 checks without the refused name are fitted.
func TestFitChecksGivesBackARefusedSetWhole(t *testing.T) {
	read := func(checks []LinkCheck, in *ChecksIncomplete) LinkRead {
		return LinkRead{System: "github", Host: "github.com", Kind: ExternalChange, NodeID: "PR_fit", State: "open", HeadSha: strings.Repeat("a", 40),
			UpdatedAt: day0, Checks: checks, Incomplete: in}
	}
	set := func() []LinkCheck {
		var l []LinkCheck
		for i := range MaxLinkChecks + 20 {
			c := LinkCheck{ID: fmt.Sprintf("status:%d", 7000+i), Name: strings.Repeat("x:// ", 18) + fmt.Sprintf("s%03d", i), Status: "completed",
				Conclusion: "success", URL: fmt.Sprintf("https://ci.example.com/octo/app/commit-statuses/%024d", i), App: "status", CompletedAt: day0}
			if i%7 == 3 {
				c.Conclusion = "failure"
			}
			l = append(l, c)
		}
		return l
	}
	words := []string{"a reason the read gave"}
	for _, at := range []int{5, 60, MaxLinkChecks - 1, MaxLinkChecks + 10} {
		bad := set()
		bad[at].Name = strings.Repeat("n", MaxCheckName+1)
		kept, in := FitChecks(bad, words, 2)
		if len(kept) != len(bad) || &kept[0] != &bad[0] || in == nil || !slices.Equal(in.Words, words) || in.Omitted != 2 {
			t.Errorf("the refused name at %d: %d of %d kept (the same slice: %v), the reasons %+v", at, len(kept), len(bad), len(kept) > 0 && &kept[0] == &bad[0], in)
		}
		if _, _, err := encodeLinkRead(read(kept, in)); !isRefusal(err, RefusalBound) {
			t.Errorf("the refused name at %d: the store's check of the read gave %v", at, err)
		}
		if _, err := encodeChecks(bad); !isRefusal(err, RefusalBound) || !strings.Contains(err.Error(), "a check's name") {
			t.Errorf("the refused name at %d: the store's check of its checks gave %v", at, err)
		}
	}
	checks := set()
	kept, in := FitChecks(checks, words, 2)
	if _, _, err := encodeLinkRead(read(kept, in)); err != nil || len(kept) == 0 || len(kept) > MaxLinkChecks {
		t.Fatalf("120 checks the store takes: %d kept, the read %v", len(kept), err)
	}
	order := slices.Clone(checks)
	failed := func(c LinkCheck) int {
		if c.Conclusion == "failure" {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(order, func(a, b LinkCheck) int { return failed(a) - failed(b) })
	left := len(checks) - len(kept)
	if left <= 0 || !slices.Equal(kept, order[:len(kept)]) || in == nil || in.Omitted != 2+left || in.Words[0] != words[0] ||
		in.Words[len(in.Words)-1] != fmt.Sprintf("%d checks left out to keep the list within %d checks and %d KiB, failures kept first", left, MaxLinkChecks, MaxLinkChecksJSON>>10) {
		t.Fatalf("120 checks the store takes: %d kept, not the first failures first, or the cut said %+v", len(kept), in)
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
