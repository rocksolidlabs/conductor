package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
)

// What a capture lets in (Program item PV1, docs/plans/browser-evidence.md,
// "The evidence rules"): judged in one form, every relation the rules
// state the store's own, each refused in checkCapture with a row below.

// refusedAs is checkCapture's answer as a refusal's code, "" for taken.
func refusedAs(t *testing.T, n NewCapture, f CaptureFacts, image bool) string {
	t.Helper()
	_, _, err := checkCapture(n, f, image)
	var ref *Refusal
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ref):
		return ref.Code
	}
	t.Fatalf("not a refusal: %v", err)
	return ""
}

// linkCapture is a capture of a link: no run, no tree, no scope.
func linkCapture(image bool) (NewCapture, CaptureFacts) {
	n, _ := testCapture(image)
	n.Label = TreeRead{}
	moment(&n)
	return n, CaptureFacts{Origin: OriginURL}
}

// candidateCapture is a capture of a candidate's preview checkout.
func candidateCapture() (NewCapture, CaptureFacts) {
	n, f := testCapture(true)
	f.Origin, f.Tree, f.Mutable = OriginCandidate, TreePreview, false
	f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, f.Commit)
	return n, f
}

// The privacy rule compares names in one form: an element's tag and an
// attribute's name are refused unless they are of the forms the rule
// allows (TestANameIsWhatTheListOfNamesAllows walks their alphabet), and
// an input's (its local name, without regard to case, with a prefix or
// none) value, or a password field's, is never kept; legal names of every
// case are kept as given.
func TestThePrivacyRuleComparesNamesInOneForm(t *testing.T) {
	type attr = [2]string
	value := attr{"value", "hunter2"}
	var cases []struct {
		tag   string
		attrs [][2]string
		taken bool
	}
	add := func(taken bool, tag string, attrs ...[2]string) {
		cases = append(cases, struct {
			tag   string
			attrs [][2]string
			taken bool
		}{tag, attrs, taken})
	}
	// Not names: white space of each kind before, inside and after.
	for _, ws := range []string{" ", "\t", "\n", "\r", " ", " ", "　", "\x00", "\x1b"} {
		add(false, ws+"input", value)
		add(false, "input"+ws, value)
		add(false, "in"+ws+"put", value)
		add(false, "div", attr{ws + "value", "v"})
		add(false, "div", attr{"value" + ws, "v"})
		add(false, "div", attr{"va" + ws + "lue", "v"})
	}
	add(false, "", value)
	// Not names: a delimiter, a trailing colon, two prefixes, a start that
	// no name has; refused for the name itself, whatever it holds.
	for _, tag := range []string{"input/", "input:", "input>", `input"`, "input=", "a:b:Input", ":input", "-input", "1input", "a:b:div", "svg:", "div/"} {
		add(false, tag, value)
		add(false, tag, attr{"title", "x"})
	}
	for _, name := range []string{"value/", "value:", "value=", `value"`, "value'", "value>", "x:", ":", "-value", "1value"} {
		add(false, "input", attr{name, "hunter2"})
		add(false, "div", attr{name, "v"})
	}
	// An input in any case and with any prefix keeps no value, whatever
	// its type says (missing, misspelt, a password in any case), the value
	// named in any case, with a prefix, or twice.
	for _, tag := range []string{"input", "INPUT", "Input", "iNpUt", "html:input", "x:INPUT", "svg:Input"} {
		for _, name := range []string{"value", "VALUE", "VaLuE", "xml:value", "x:VALUE", ":value", "a:b:value", "@x:value"} {
			add(false, tag, attr{name, "hunter2"})
		}
		add(false, tag, attr{"type", "passwrod"}, value)
		add(false, tag, attr{"TYPE", "PassWord"}, value)
		add(false, tag, value, attr{"VALUE", "again"})
		add(true, tag, attr{"type", "text"}, attr{"data-value", "x"}, attr{"valueAsNumber", "1"})
	}
	// A password field that is no input: its type in any case and spelling
	// of its name, its value refused.
	for _, typ := range []attr{{"type", "password"}, {"TYPE", "PASSWORD"}, {"Type", " Password "}, {"html:type", "password"},
		{"type", "pass-word"}, {"type", "pass" + string(rune(0x200B)) + "word"}, {"type", "password" + string(rune(0xAD))}, {"type", "pass word!"}} {
		add(false, "div", typ, value)
		add(false, "custom-field", value, typ)
	}
	// Legal names are kept as the page has them: a value on no secret
	// field, an SVG element's mixed-case attribute, a prefixed one, a
	// framework's binding and event, a custom element.
	add(true, "div", attr{"type", "text"}, attr{"value", "shown"})
	add(true, "svg", attr{"viewBox", "0 0 1 1"}, attr{"xlink:href", "#a"})
	add(true, "svg:foreignObject", attr{"xml:lang", "en"})
	add(true, "my-element.v2", attr{":model-value", "x"}, attr{"@click", "go"}, attr{"_x", "y"})
	add(true, "div", attr{"type", "passwords"}, attr{"value", "shown"})
	for _, c := range cases {
		n, f := testCapture(false)
		e := element(&n)
		e.Tag, e.Attributes = c.tag, c.attrs
		moment(&n)
		got := refusedAs(t, n, f, false)
		if c.taken != (got == "") || (!c.taken && got != RefusalInvalid) {
			t.Errorf("tag %q attributes %q: refused %q, taken wanted %v", c.tag, c.attrs, got, c.taken)
		}
	}
	// Through the store: nothing refused is kept anywhere.
	s := openStore(t, storeDir(t))
	for i, c := range cases {
		if c.taken {
			continue
		}
		n, f := testCapture(false)
		e := element(&n)
		e.Tag, e.Attributes = c.tag, c.attrs
		moment(&n)
		rc, err := create(t, s, opID("secret", i), n, f, nil)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_annotations WHERE element LIKE '%hunter2%'"); n != 0 {
		t.Fatalf("%d annotations hold a secret field's value", n)
	}
}

// alphabet is the runes the walk of the names puts in them: every ASCII
// character, the first beyond ASCII of each Unicode category the standard
// library tells (a lone surrogate as the bytes that would encode it), and
// invisible ones by name: zero-width spaces and joiners, the word joiner,
// the byte order mark, the soft hyphen, a direction override, the
// Mongolian vowel separator and the combining grapheme joiner.
func alphabet() []string {
	var out []string
	for r := rune(0); r < 0x80; r++ {
		out = append(out, string(r))
	}
	cats := make([]string, 0, len(unicode.Categories))
	for name := range unicode.Categories {
		cats = append(cats, name)
	}
	sort.Strings(cats)
	for _, name := range cats {
		r, ok := firstBeyondASCII(unicode.Categories[name])
		switch {
		case !ok:
		case unicode.Is(unicode.Cs, r):
			out = append(out, "\xed\xa0\x80")
		default:
			out = append(out, string(r))
		}
	}
	for _, r := range []rune{0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF, 0x00AD, 0x202E, 0x180E, 0x034F} {
		out = append(out, string(r))
	}
	return out
}

// firstBeyondASCII is the first rune of the table past ASCII.
func firstBeyondASCII(tab *unicode.RangeTable) (rune, bool) {
	for _, rg := range tab.R16 {
		for r := rune(rg.Lo); r <= rune(rg.Hi); r += rune(rg.Stride) {
			if r >= 0x80 {
				return r, true
			}
		}
	}
	for _, rg := range tab.R32 {
		for r := rune(rg.Lo); r <= rune(rg.Hi); r += rune(rg.Stride) {
			if r >= 0x80 {
				return r, true
			}
		}
	}
	return 0, false
}

// A name is what the list of names allows and nothing else, walked
// through the alphabet: each rune at the start, in the middle and at the
// end of an input's tag, of the value attribute's name, of the type
// attribute's name, and of a type's password. A rune outside the form's
// alphabet refuses the report; a spelling of the form whose local name
// is still the one the rule compares (without regard to ASCII case) keeps
// no value; any other spelling of the form is plainly another name, kept.
// A type's value is judged by its ASCII letters alone: any rune but a
// letter leaves a password.
func TestANameIsWhatTheListOfNamesAllows(t *testing.T) {
	type attr = [2]string
	tagForm := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*(:[A-Za-z][A-Za-z0-9._-]*)?$`)
	attributeForm := regexp.MustCompile(`^([A-Za-z_@]|[A-Za-z_:@][A-Za-z0-9_.:@-]*[A-Za-z0-9_.@-])$`)
	tagRunes, attributeRunes := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-:", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.:@-"
	local := func(s string) string { return strings.ToLower(s[strings.LastIndexByte(s, ':')+1:]) }
	spell := func(word, x string) []string {
		mid := len(word) / 2
		return []string{x + word, word[:mid] + x + word[mid:], word + x}
	}
	judge := func(what, tag string, attrs []attr, refused bool) {
		t.Helper()
		n, f := testCapture(false)
		e := element(&n)
		e.Tag, e.Attributes = tag, attrs
		moment(&n)
		got := refusedAs(t, n, f, false)
		if refused != (got == RefusalInvalid) || (!refused && got != "") {
			t.Errorf("%s: tag %q attributes %q: refused %q, refused wanted %v", what, tag, attrs, got, refused)
		}
	}
	for _, x := range alphabet() {
		ofTag := len(x) == 1 && strings.Contains(tagRunes, x)
		ofAttribute := len(x) == 1 && strings.Contains(attributeRunes, x)
		for _, s := range spell("input", x) {
			judge("an input's tag", s, []attr{{"value", "hunter2"}}, !ofTag || !tagForm.MatchString(s) || local(s) == "input")
		}
		for _, s := range spell("value", x) {
			judge("the value's name", "input", []attr{{s, "hunter2"}}, !ofAttribute || !attributeForm.MatchString(s) || local(s) == "value")
		}
		for _, s := range spell("type", x) {
			judge("the type's name", "div", []attr{{s, "password"}, {"value", "hunter2"}}, !ofAttribute || !attributeForm.MatchString(s) || local(s) == "type")
		}
		letter := len(x) == 1 && unicode.IsLetter(rune(x[0]))
		for _, s := range spell("password", x) {
			judge("a type's password", "div", []attr{{"type", s}, {"value", "hunter2"}}, !letter)
		}
	}
}

// Every relation the evidence rules state is the store's, refused in
// checkCapture: stability only with pixels and never in motion; a link's
// capture with no run, no tree and no scope; the attempt's and the
// candidate's origins; one capture one moment, each of its parts; the
// drift's own words; a changed path by its field rule; the frame's scroll
// at what a browser scrolls to; how the screenshot was taken, the client's
// word, checked; and a note's bound through EditCapture.
func TestEveryRelationOfTheEvidenceIsTheStores(t *testing.T) {
	yes, no := true, false
	type mutate func(n *NewCapture, f *CaptureFacts)
	rows := []struct {
		name  string
		base  func() (NewCapture, CaptureFacts)
		image bool
		set   mutate
		code  string
	}{
		// Stability.
		{"stable with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &yes }, RefusalInvalid},
		{"unstable with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &no }, RefusalInvalid},
		{"stable in motion", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Stable, element(n).State.Motion = &yes, 1
		}, RefusalInvalid},
		{"unstable in motion", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Stable, element(n).State.Motion = &no, 1
		}, ""},
		{"stable and still", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &yes }, ""},
		// A link's capture.
		{"a link", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true, func(*NewCapture, *CaptureFacts) {}, ""},
		{"a link with a crew", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Crew = "crew" }, RefusalInvalid},
		{"a link with a repository", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "/repo" }, RefusalInvalid},
		{"a link with a project", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Project = "PRJ" }, RefusalInvalid},
		{"a link naming a run", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-0123abcd" }, RefusalInvalid},
		{"a link naming a tree", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreeWorktree }, RefusalInvalid},
		// The attempt's and the candidate's origins.
		{"an attempt with a candidate", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, f.Commit)
		}, RefusalInvalid},
		{"an attempt of a preview", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreePreview }, RefusalInvalid},
		{"an attempt not mutable", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Mutable = false }, RefusalInvalid},
		{"a candidate", candidateCapture, true, func(*NewCapture, *CaptureFacts) {}, ""},
		{"a candidate of a worktree", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreeWorktree }, RefusalInvalid},
		{"a candidate of another attempt", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt+1, f.Commit)
		}, RefusalInvalid},
		{"a candidate of another commit", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, strings.Repeat("b", 40))
		}, RefusalInvalid},
		// One capture, one moment: each part on its own.
		{"observed at another URL", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.URL += "?b" }, RefusalInvalid},
		{"observed in another viewport", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Viewport.Width++ }, RefusalInvalid},
		{"observed at another scroll", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Viewport.ScrollY = 9 }, RefusalInvalid},
		{"observed at another epoch", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Epoch++ }, RefusalInvalid},
		{"observed at another revision", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Revision = "abcdef0" }, RefusalInvalid},
		{"observed at another commit", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Label.Commit = strings.Repeat("b", 40)
		}, RefusalInvalid},
		{"observed at another content", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Label.Content = strings.Repeat("d", 40)
		}, RefusalInvalid},
		// The drift's own words.
		{"a read counting fewer than none", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.Changed = -1 }, RefusalInvalid},
		{"a drift kept with no time", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Content, n.Label.ReadAt = strings.Repeat("d", 40), time.Time{}
			moment(n)
		}, RefusalInvalid},
		{"no drift and no time", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.ReadAt = time.Time{} }, ""},
		// A changed path, by its field rule.
		{"a changed path with an escape", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{"web/\x1b[31mred.vue"} }, RefusalInvalid},
		{"a changed path with a NUL", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{"web/a\x00.vue"} }, RefusalInvalid},
		{"an empty changed path", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{""} }, RefusalInvalid},
		{"a changed path at its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{rep("p", MaxChangedPathsBytes)} }, ""},
		{"a changed path past its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.ChangedPaths = []string{rep("p", MaxChangedPathsBytes+1)}
		}, RefusalBound},
		// The frame's scroll: what a browser scrolls to.
		{"a scroll past a rectangle's bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollY = MaxRectCoord * 4; moment(n) }, ""},
		{"a scroll at its bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollY = MaxScroll; moment(n) }, ""},
		{"a scroll past its bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollX = -MaxScroll - 1; moment(n) }, RefusalInvalid},
		// How the screenshot was taken.
		{"a tab's screenshot from the desktop", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via = CaptureViaDesktop }, ""},
		{"the desktop's from the desktop", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via, n.ImageHow = CaptureViaDesktop, ImageDesktop }, ""},
		{"the desktop's from the web", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageDesktop }, RefusalInvalid},
		{"none with a screenshot", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageNone }, RefusalInvalid},
		{"no word with a screenshot", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = "" }, RefusalInvalid},
		{"a tab's with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageTab }, RefusalInvalid},
		{"no word with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = "" }, RefusalInvalid},
		// A link has no tree: no sources, and a selection of no commit and no
		// content.
		{"a link with sources", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources = [][]Source{{{Path: "a.vue", How: "vue", Found: true, Class: "tracked"}}, nil}
		}, RefusalInvalid},
		{"a link whose selection names a commit", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Commit = capCommit
			moment(n)
		}, RefusalInvalid},
		{"a link whose selection names content", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Content = contentA
			moment(n)
		}, RefusalInvalid},
		// Every time one the store writes and reads back; the selection's read
		// not after the capture.
		{"a selection past year 9999", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.SelectedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, RefusalInvalid},
		{"a selection before year 0", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.SelectedAt = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC) }, RefusalInvalid},
		{"a selection at year 9999's end", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.SelectedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
		}, ""},
		{"a kept read past year 9999", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Content, n.Label.ReadAt = strings.Repeat("d", 40), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			moment(n)
		}, RefusalInvalid},
		{"a read past year 9999, not kept", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.ReadAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, RefusalInvalid},
		{"a read after now", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.ReadAt = time.Now().Add(time.Hour) }, RefusalInvalid},
		{"a kept read before year 0", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Content, n.Label.ReadAt = strings.Repeat("d", 40), time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
			moment(n)
		}, RefusalInvalid},
		{"an element observed past year 9999", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}, RefusalInvalid},
		{"an element observed in a zone whose year is past 9999", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.At = time.Date(10000, 1, 1, 1, 0, 0, 0, time.FixedZone("ahead", 2*3600))
		}, RefusalInvalid},
		{"an element observed in another zone", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.At = momentAt.In(time.FixedZone("east", 5*3600+1800))
		}, ""},
		// A candidate names its candidate.
		{"a candidate naming none", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) { f.Candidate = "" }, RefusalInvalid},
		{"a candidate of another task", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, "test", f.Attempt, f.Commit)
		}, RefusalInvalid},
		{"a candidate of another run", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", "crew-89abcdef", f.Task, f.Attempt, f.Commit)
		}, RefusalInvalid},
		{"a candidate not of the candidate's form", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) { f.Candidate = "build@" + f.Commit }, RefusalInvalid},
		// The attempt's and the candidate's tree, each fact on its own.
		{"an attempt of no run", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Run = "" }, RefusalInvalid},
		{"an attempt of no run's id", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew 0123" }, RefusalInvalid},
		{"an attempt of no task", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Task = "" }, RefusalInvalid},
		{"an attempt of no task's id", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Task = "Build Task" }, RefusalInvalid},
		{"attempt 0", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Attempt = 0 }, RefusalInvalid},
		{"an attempt at its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Attempt = MaxFindingLine }, ""},
		{"an attempt past its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Attempt = MaxFindingLine + 1 }, RefusalInvalid},
		{"a candidate of attempt 0", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Attempt = 0
			f.Candidate = fmt.Sprintf("%s/%s/0@%s", f.Run, f.Task, f.Commit)
		}, RefusalInvalid},
		{"a tree with no commit", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Commit = "" }, RefusalInvalid},
		{"a tree's commit cut short", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Commit = capCommit[:39] }, RefusalInvalid},
		{"a tree's content not hex", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Content = rep("z", 40) }, RefusalInvalid},
		{"a tree's content in upper case", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Content = strings.ToUpper(rep("c", 40)) }, RefusalInvalid},
		{"a tree read with no content", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Content = "" }, ""},
		{"no where", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Where = "" }, RefusalInvalid},
		{"a where of no runtime's name", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Where = "Native Box!" }, RefusalInvalid},
		{"paths beyond their count", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Changed = 0 }, RefusalInvalid},
		{"a count beyond its paths", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Changed = 9 }, ""},
		{"a candidate's paths beyond their count", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) { f.Changed = 0 }, RefusalInvalid},
		{"an unknown origin", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Origin = "file" }, RefusalInvalid},
		{"no origin", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Origin = "" }, RefusalInvalid},
		{"sources of fewer annotations", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Sources = f.Sources[:1] }, RefusalInvalid},
		{"sources of more annotations", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Sources = append(f.Sources, nil) }, RefusalInvalid},
		{"no sources at all", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Sources = nil }, ""},
		// Regions: drawn on a screenshot, with no element's report.
		{"a region with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false, func(n *NewCapture, f *CaptureFacts) {
			n.Annotations = append(n.Annotations, NewAnnotation{Kind: AnnotationRegion, Note: "here", Rect: Rect{0, 0, 1, 1}})
			f.Sources = append(f.Sources, nil)
		}, RefusalInvalid},
		{"a region with an element's report", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[1].Element = &ElementReport{Tag: "div", Selector: "div"}
		}, RefusalInvalid},
		{"a region with a moment", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[1].Observed = n.Annotations[0].Observed
		}, RefusalInvalid},
		{"a region with sources", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[1] = []Source{{Path: "a.vue", How: "vue", Found: true, Class: "tracked"}}
		}, RefusalInvalid},
		{"a region's rectangle not a number", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[1].Rect.X = math.NaN() }, RefusalInvalid},
		{"a region's rectangle of a negative size", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[1].Rect.Height = -1 }, RefusalInvalid},
		{"an annotation of no kind", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[1].Kind = "arrow" }, RefusalInvalid},
		{"an element with no report", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Element = nil }, RefusalInvalid},
		{"an element with no moment", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed = nil }, RefusalInvalid},
		// The smaller rules, each on its own.
		{"the selection's commit not hex", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.Commit = rep("g", 40); moment(n) }, RefusalInvalid},
		{"the selection's content not hex", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.Content = rep("g", 40); moment(n) }, RefusalInvalid},
		{"an observed title with an escape", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Title = "a\x1bb" }, RefusalInvalid},
		{"an observed title past its bound", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Title = rep("é", MaxCaptureTitle+1)
		}, RefusalBound},
		{"an element of no moment", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.At = time.Time{} }, RefusalInvalid},
		{"a via of neither", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via = "phone" }, RefusalInvalid},
		{"no via", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via = "" }, RefusalInvalid},
		{"no moment of selection", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.SelectedAt = time.Time{} }, RefusalInvalid},
		{"a retake of no capture's id", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.RetakeOf = "not-a-capture" }, RefusalInvalid},
		{"a retake of a capture's id", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.RetakeOf = newID() }, ""},
		{"a negative epoch", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Epoch = -1; moment(n) }, RefusalInvalid},
		{"no screenshot and no why", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageWhy = "" }, RefusalInvalid},
		{"a screenshot and a why", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageWhy = "declined" }, RefusalInvalid},
		{"a why with a NUL", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageWhy = "no\x00" }, RefusalInvalid},
		{"a screenshot of no scale", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageScale = 0 }, RefusalInvalid},
		{"a screenshot's scale at 64", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageScale = 64 }, ""},
		{"a screenshot's scale past 64", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageScale = 64.5 }, RefusalInvalid},
		{"a scale with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageScale = 1 }, RefusalInvalid},
		{"a colour scheme of neither", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ColorScheme = "sepia"; moment(n) }, RefusalInvalid},
		{"the dark scheme", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ColorScheme = "dark"; moment(n) }, ""},
		{"a viewport of no mode", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.Mode = "watch"; moment(n) }, RefusalInvalid},
		{"the phone's viewport", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.Mode = "phone"; moment(n) }, ""},
		{"sources out of their order", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "b.vue", How: "vue", Class: "untracked"}, {Path: "a.vue", How: "vue", Class: "tracked"}}
		}, RefusalInvalid},
		{"sources in their order", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "tracked"}, {Path: "a.vue", How: "vue", Class: "tracked"}, {Path: "v.js", How: "vue", Class: "vendor"}}
		}, ""},
		{"a source of no class", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "mine"}}
		}, RefusalInvalid},
		{"a source outside the tree found in it", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "/elsewhere/a.vue", How: "vue", Outside: true, Found: true, Class: "tracked"}}
		}, RefusalInvalid},
		{"a source outside the tree", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "/elsewhere/a.vue", How: "vue", Outside: true, Class: "tracked"}}
		}, ""},
		{"a source of no path", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Sources[0] = []Source{{How: "vue", Class: "tracked"}} }, RefusalInvalid},
	}
	for _, r := range rows {
		base := r.base
		if base == nil {
			base = func() (NewCapture, CaptureFacts) { return testCapture(true) }
		}
		n, f := base()
		r.set(&n, &f)
		if got := refusedAs(t, n, f, r.image); got != r.code {
			t.Errorf("%s: refused %q, want %q", r.name, got, r.code)
		}
	}
	// A note's bound through an edit, at its edge and one past.
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 30))
	edits := kept(captureOf(t, s, id))
	edits[0].Note = rep("é", MaxAnnotationNote+1)
	rc, err := reedit(t, s, "past", id, 1, edits)
	want(t, rc, err, StatusFailed, RefusalBound)
	edits[0].Note = rep("é", MaxAnnotationNote)
	rc, err = reedit(t, s, "at", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	edits = kept(captureOf(t, s, id))
	edits[1].Note = "a\x1bb"
	rc, err = reedit(t, s, "control", id, 2, edits)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A time the store cannot keep is refused through the store as invalid,
	// never answered as an internal error, and leaves nothing.
	far := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, set := range []func(n *NewCapture){
		func(n *NewCapture) { n.SelectedAt = far },
		func(n *NewCapture) { n.Label.Content, n.Label.ReadAt = rep("d", 40), far; moment(n) },
		func(n *NewCapture) { n.Annotations[0].Observed.At = far },
	} {
		n, f := testCapture(true)
		set(&n)
		img := pngOf(t, 8, 8, byte(60+i))
		// A digest of its own: such a time has no JSON to digest.
		rc, err := s.CreateCapture(context.Background(), testOp(opID("far", i)), fmt.Sprintf("%064d", i), n, f, bytes.NewReader(img))
		want(t, rc, err, StatusFailed, RefusalInvalid)
		noUploads(t, s)
		if exists(t, contentAt(s, sumOf(img))) {
			t.Fatalf("a far time (%d) left its screenshot", i)
		}
	}
}

// The serving state and the drift are the store's reading of the facts:
// serving declared only for the tree's commit (and its content when it has
// changes) over content that was read, stale for another, unconfirmed for
// none, a commit alone over changes, content not read, or a link; the
// drift kept when the selection's commit or content differs from the
// save's or either was not read, and never for a link.
func TestTheServingStateAndTheDriftFromTheFacts(t *testing.T) {
	commit7, content7 := capCommit[:7], contentA[:7]
	other := strings.Repeat("b", 40)
	for _, c := range []struct {
		name    string
		page    string
		content string
		changed int
		link    bool
		want    string
	}{
		{"a commit over a clean tree", commit7, contentA, 0, false, ServingDeclared},
		{"a commit and its content", commit7 + "+" + content7, contentA, 1, false, ServingDeclared},
		{"the full ids", capCommit + "+" + contentA, contentA, 1, false, ServingDeclared},
		{"another commit", other[:7], contentA, 0, false, ServingStale},
		{"another commit with content", other[:7] + "+" + content7, contentA, 1, false, ServingStale},
		{"another content", commit7 + "+" + other[:7], contentA, 1, false, ServingStale},
		{"a commit sharing its first character", "a" + other[:6], contentA, 0, false, ServingStale},
		{"a commit sharing all but its last", commit7[:6] + "b", contentA, 0, false, ServingStale},
		{"a content sharing its first character", commit7 + "+c" + other[:6], contentA, 1, false, ServingStale},
		{"a commit alone over changes", commit7, contentA, 1, false, ServingUnconfirmed},
		{"a commit and content not read", commit7 + "+" + content7, "", 1, false, ServingUnconfirmed},
		{"a commit alone over content not read", commit7, "", 0, false, ServingUnconfirmed},
		{"nothing declared", "", contentA, 0, false, ServingUnconfirmed},
		{"a link", commit7, "", 0, true, ServingUnconfirmed},
	} {
		n, f := testCapture(true)
		f.Content, f.Changed, f.ChangedPaths = c.content, c.changed, nil
		if c.link {
			n, f = linkCapture(true)
		}
		n.Revision = c.page
		moment(&n)
		got, _, err := checkCapture(n, f, true)
		if err != nil || got.Origin.Serving != (Serving{State: c.want, Page: c.page}) {
			t.Errorf("%s: %+v %v, want %s", c.name, got.Origin.Serving, err, c.want)
		}
	}
	readAt := momentAt.Add(-time.Minute)
	for _, c := range []struct {
		name            string
		commit, content string
		saveContent     string
		link            bool
		kept            bool
	}{
		{"all equal", capCommit, contentA, contentA, false, false},
		{"the commit differs", other, contentA, contentA, false, true},
		{"the content differs", capCommit, other, contentA, false, true},
		{"the selection read no content", capCommit, "", contentA, false, true},
		{"the save read no content", capCommit, contentA, "", false, true},
		{"neither read content", capCommit, "", "", false, true},
		{"a link", "", "", "", true, false},
	} {
		n, f := testCapture(true)
		f.Content = c.saveContent
		if c.link {
			n, f = linkCapture(true)
		}
		n.Label = TreeRead{Commit: c.commit, Content: c.content, Changed: 3, ReadAt: readAt}
		moment(&n)
		got, _, err := checkCapture(n, f, true)
		want := (*TreeRead)(nil)
		if c.kept {
			want = &TreeRead{Commit: c.commit, Content: c.content, Changed: 3, ReadAt: readAt}
		}
		if err != nil || (got.Origin.Drift == nil) != (want == nil) || (want != nil && *got.Origin.Drift != *want) {
			t.Errorf("%s: %+v %v, want %+v", c.name, got.Origin.Drift, err, want)
		}
	}
}

// The document carries the drift as it was stored: a capture whose
// selection's read and the save's differ is read back through Document
// and Capture with its whole drift (the selection's commit, content,
// count and time, in UTC), and again after an annotation's edit.
func TestTheDocumentCarriesTheDrift(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	readAt := time.Date(2026, 11, 10, 13, 59, 30, 123456789, time.FixedZone("east", 2*3600))
	n.Label = TreeRead{Commit: rep("b", 40), Content: rep("d", 40), Changed: 7, ReadAt: readAt}
	moment(&n)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 31))
	same := func(d *TreeRead) bool {
		return d != nil && d.Commit == rep("b", 40) && d.Content == rep("d", 40) && d.Changed == 7 && d.ReadAt.Equal(readAt) &&
			d.ReadAt.Location() == time.UTC
	}
	check := func(rev int64) {
		t.Helper()
		raw, err := s.Document(ctx, id, rev)
		var d CaptureDocument
		if err != nil || json.Unmarshal(raw, &d) != nil || !same(d.Origin.Drift) || d.Rev != rev {
			t.Fatalf("the document at r%d: %v %s", rev, err, raw)
		}
		if !strings.Contains(string(raw), `"drift":{"commit":"`+rep("b", 40)+`","content":"`+rep("d", 40)+`","changed":7,"readAt":"2026-11-10T11:59:30.123456789Z"}`) {
			t.Fatalf("the document's drift as written: %s", raw)
		}
		if c := captureOf(t, s, id); !same(c.Origin.Drift) || c.Rev != rev {
			t.Fatalf("the capture at r%d: %+v", rev, c.Origin.Drift)
		}
	}
	check(1)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "edited"
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	check(2)
	// Sent, the document frozen carries it, and the new ticket's description
	// warns of it first.
	newProject(t, s, "CND")
	sid := mustSend(t, s, "s1", id, 2, Destination{Kind: SendTicket, Project: "CND", Title: "Drifted"}, SendFacts{})
	var d CaptureDocument
	if err := json.Unmarshal(sendFile(t, s, sid, sendOf(t, s, sid).Digest), &d); err != nil || !same(d.Origin.Drift) || d.Rev != 2 {
		t.Fatalf("the document sent: %v %+v", err, d.Origin.Drift)
	}
	desc := ticketPage(t, s, sendOf(t, s, sid).Target).Description
	if !strings.HasPrefix(desc, "> **The tree changed between the selection and the save.**\n") ||
		!strings.Contains(desc, "\nthe selection read: bbbbbbb+ddddddd\nthe save read: aaaaaaa+ccccccc\n") {
		t.Fatalf("the description: %s", desc)
	}
}

// Two addresses are of one origin when their scheme, their host without
// regard to case and their effective port agree.
func TestPageOriginsInOneForm(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"http://localhost/a", "http://localhost:80/b", true},
		{"https://Example.COM/a", "https://example.com:443/", true},
		{"HTTP://h:080/", "http://H/x?y#z", true},
		{"http://[::1]:5173/", "http://[::1]:5173/x", true},
		{"http://localhost:5173/", "http://localhost:5174/", false},
		{"http://localhost/", "https://localhost/", false},
		{"http://localhost:443/", "https://localhost/", false},
		{"http://a.example/", "http://b.example/", false},
	} {
		if got := pageOrigin(c.a) == pageOrigin(c.b) && pageOrigin(c.a) != ""; got != c.same {
			t.Errorf("%s and %s (%q, %q): one origin %v, want %v", c.a, c.b, pageOrigin(c.a), pageOrigin(c.b), got, c.same)
		}
	}
}

// A retake is related by the store: run for the same run and task, repo
// for the same repository; a link's retakes a link's of the same origin
// (written in another form among them) and records no relation; a deleted
// capture, another repository's, another origin's and a link and a run's
// retaking each other are refused, nothing written.
func TestARetakeIsRelatedByTheStore(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(false)
	first := mustCreate(t, s, "first", n, f, nil)
	ln, lf := linkCapture(false)
	ln.URL = "http://Localhost:80/page"
	moment(&ln)
	link := mustCreate(t, s, "link", ln, lf, nil)
	// A link's of the run's own page origin, and a run's of no repository.
	ln.URL = "http://127.0.0.1:5173/other"
	moment(&ln)
	sameOrigin := mustCreate(t, s, "same-origin", ln, lf, nil)
	bn, bf := testCapture(false)
	bf.Scope.Repo = ""
	bare := mustCreate(t, s, "bare", bn, bf, nil)
	gone := mustCreate(t, s, "gone", n, f, nil)
	rc, err := s.DeleteCapture(ctx, testOp("delete"), gone)
	want(t, rc, err, StatusDone, "")
	retake := func(opID, of string, n NewCapture, f CaptureFacts) (Receipt, error) {
		n.RetakeOf = of
		return create(t, s, opID, n, f, nil)
	}
	for i, c := range []struct {
		name string
		of   string
		set  func(n *NewCapture, f *CaptureFacts)
		via  string
		code string
	}{
		{"the same run and task", first, func(*NewCapture, *CaptureFacts) {}, RetakeRun, ""},
		{"another attempt of the task", first, func(_ *NewCapture, f *CaptureFacts) { f.Attempt = 3 }, RetakeRun, ""},
		{"another task", first, func(_ *NewCapture, f *CaptureFacts) { f.Task = "test" }, RetakeRepo, ""},
		{"another run of the repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-89abcdef" }, RetakeRepo, ""},
		{"another repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "/other" }, "", RefusalInvalid},
		{"no repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "" }, "", RefusalInvalid},
		{"a deleted capture", gone, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"no capture", newID(), func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"a run's retaking a link's", link, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"a run's retaking a link's of its own page origin", sameOrigin, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"both of no repository", bare, func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "" }, "", RefusalInvalid},
		{"a repository's retaking one of none", bare, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
	} {
		n, f := testCapture(false)
		c.set(&n, &f)
		t.Log(c.name)
		rc, err := retake(opID("retake", i), c.of, n, f)
		if c.code != "" {
			want(t, rc, err, StatusFailed, c.code)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if got := captureOf(t, s, rc.ID); got.RetakeOf != c.of || got.RetakeVia != c.via {
			t.Errorf("%s: retake of %s via %q, want %q", c.name, got.RetakeOf, got.RetakeVia, c.via)
		}
	}
	for i, c := range []struct {
		name, url, of, code string
	}{
		{"a link of the same origin", "http://localhost/other?x#y", link, ""},
		{"a link of another port", "http://localhost:8080/page", link, RefusalInvalid},
		{"a link of another scheme", "https://localhost/page", link, RefusalInvalid},
		{"a link's retaking a run's", "http://localhost/page", first, RefusalInvalid},
		{"a link's retaking a run's of its own page origin", "http://127.0.0.1:5173/again", first, RefusalInvalid},
		{"a link's retaking a link's of its own", "http://127.0.0.1:5173/again", sameOrigin, ""},
	} {
		n, f := linkCapture(false)
		n.URL = c.url
		moment(&n)
		t.Log(c.name)
		rc, err := retake(opID("link-retake", i), c.of, n, f)
		if c.code != "" {
			want(t, rc, err, StatusFailed, c.code)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if got := captureOf(t, s, rc.ID); got.RetakeOf != c.of || got.RetakeVia != "" {
			t.Errorf("%s: retake of %s via %q", c.name, got.RetakeOf, got.RetakeVia)
		}
	}
}
