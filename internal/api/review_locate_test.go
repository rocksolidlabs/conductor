package api

import (
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

// A located state kept with a mark is within dispatch.MaxMarkLocated, a
// renamed path at its bound among it, and none when there is none.
func TestLocatedJSONIsWithinTheMarksBound(t *testing.T) {
	if locatedJSON(nil) != nil {
		t.Fatal("a general finding's mark keeps a located state")
	}
	st := &locatedState{State: locatedRenamed, Path: strings.Repeat(string(rune(0x2028)), 1024/3), Located: &locatedState{State: locatedMoved, From: 9_999_999, To: 10_000_000}}
	if b := locatedJSON(st); b == nil || !strings.Contains(string(b), `"renamed"`) {
		t.Fatalf("a renamed state at its bound: %s", b)
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
