package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// relocate is utils/quote.ts's relocate, on its own fixtures
// (web/app/utils/quote.test.ts, "where the lines are now"): the lines
// where they were, moved to the nearest place, or changed; a quote cut to
// twelve lines keeps its span; a line is compared as cut to 200 bytes.
func TestRelocateIsTheQuotesRelocate(t *testing.T) {
	file := []string{"package api", "", "func A() {}", "func B() {}", "func C() {}", ""}
	want := []string{"func A() {}", "func B() {}"}
	for _, tc := range []struct {
		name  string
		lines []string
		now   []string
		from  int
		to    int
		got   locatedState
	}{
		{"where they were", want, file, 3, 4, locatedState{State: locatedSame}},
		{"moved, the nearest place", want, []string{"package api", "", "// a new comment", "// and another", "func A() {}", "func B() {}", "func C() {}"}, 3, 4,
			locatedState{State: locatedMoved, From: 5, To: 6}},
		{"gone", want, []string{"package api", "func Z() {}"}, 3, 4, locatedState{State: locatedChanged}},
		{"no lines", nil, file, 3, 4, locatedState{State: locatedSame}},
		// The nearest of two places, either way.
		{"the nearest of two", []string{"x"}, []string{"x", "a", "b", "c", "d", "e", "x", "f"}, 6, 6, locatedState{State: locatedMoved, From: 7, To: 7}},
		// A quote of twenty lines keeps twelve and its span of twenty.
		{"a cut quote keeps its span", []string{"l0", "l1"}, []string{"new", "l0", "l1"}, 1, 20, locatedState{State: locatedMoved, From: 2, To: 21}},
		// A line past 200 bytes is compared as the quote cut it.
		{"a long line", []string{strings.Repeat("é", 100)}, []string{strings.Repeat("é", 150)}, 1, 1, locatedState{State: locatedSame}},
		// Past the file's end is no line.
		{"past the end", []string{"a", "b"}, []string{"a"}, 1, 2, locatedState{State: locatedChanged}},
	} {
		if got := relocate(tc.lines, tc.from, tc.to, tc.now); !reflect.DeepEqual(got, tc.got) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.got)
		}
	}
	if cutLine(strings.Repeat("é", 150), 200) != strings.Repeat("é", 100) || cutLine("abc", 2) != "ab" {
		t.Fatal("cutLine is not the quote's cutBytes")
	}
}

// A file whose lines end in CRLF is located as one whose lines end in LF:
// a finding's lines hold no "\r" (the store refuses it), so its lines are
// the same where they were, moved where they moved, and changed only when
// they changed; a lone "\r" inside a line is the line's own.
func TestLocateReadsACRLFFileAsItsLines(t *testing.T) {
	want := []string{"one", "two"}
	for _, tc := range []struct {
		name    string
		content string
		got     locatedState
	}{
		{"unchanged", "one\r\ntwo\r\nthree\r\n", locatedState{State: locatedSame}},
		{"moved", "new\r\nnewer\r\none\r\ntwo\r\n", locatedState{State: locatedMoved, From: 3, To: 4}},
		{"mixed endings", "one\r\ntwo\nthree\n", locatedState{State: locatedSame}},
		{"changed", "one\r\ntwo!\r\n", locatedState{State: locatedChanged}},
		{"a carriage return within a line", "one\rtwo\n", locatedState{State: locatedChanged}},
	} {
		if got := relocate(want, 1, 2, fileLines([]byte(tc.content))); !reflect.DeepEqual(got, tc.got) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.got)
		}
	}
}

// A located state kept with a mark is within dispatch.MaxMarkLocated, a
// renamed path at its bound among it, and none when there is none.
func TestLocatedJSONIsWithinTheMarksBound(t *testing.T) {
	if locatedJSON(nil) != nil {
		t.Fatal("a general finding's mark keeps a located state")
	}
	moved := &locatedState{State: locatedMoved, From: 9_999_999, To: 10_000_000}
	st := &locatedState{State: locatedRenamed, Path: strings.Repeat(string(rune(0x2028)), 1024/3), Located: moved}
	if b := locatedJSON(st); b == nil || !strings.Contains(string(b), `"renamed"`) || len(b) > dispatch.MaxMarkLocated {
		t.Fatalf("a renamed state at its bound: %s", b)
	}
	// Five segments of two hundred "<" (1,004 bytes, a path the store
	// takes): six bytes each with HTML escaping, past the bound; one each
	// as kept.
	segs := make([]string, 5)
	for i := range segs {
		segs[i] = strings.Repeat("<", 200)
	}
	st = &locatedState{State: locatedRenamed, Path: strings.Join(segs, "/"), Located: moved}
	b := locatedJSON(st)
	var back locatedState
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, *st) || len(b) > dispatch.MaxMarkLocated {
		t.Fatalf("a renamed path of angle brackets: %d bytes, %v", len(b), err)
	}
	// A path that no encoding keeps within the bound (1,024 control
	// characters, six bytes each): kept as unknown, saying why, never
	// dropped.
	st = &locatedState{State: locatedRenamed, Path: strings.Repeat("\x01", dispatch.MaxFindingPath), Located: moved}
	b = locatedJSON(st)
	back = locatedState{}
	if err := json.Unmarshal(b, &back); err != nil || back.State != locatedUnknown || back.Why == "" || len(b) > dispatch.MaxMarkLocated {
		t.Fatalf("a renamed path past the bound: %s %v", b, err)
	}
}

// A finding's path that could name anything outside a commit's tree is
// located unknown before git, or the repository, is asked.
func TestLocateRefusesAPathOutsideTheTree(t *testing.T) {
	l := (&Server{}).newLocator()
	for _, p := range []string{"../x", "/etc/passwd", "a/../b", "a\x00b", "./a"} {
		st := l.locate(t.Context(), dispatch.Finding{Kind: dispatch.FindingKindFile, Path: p, AnchorCommit: strings.Repeat("a", 40)}, dispatch.Candidate{Run: "r", Commit: strings.Repeat("b", 40)})
		if st == nil || st.State != locatedUnknown {
			t.Fatalf("%q: %+v", p, st)
		}
	}
}
