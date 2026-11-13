package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitrepo"
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

// A file the candidate's commit has no longer, in a comparison whose
// renames were cut (gitrepo.Detail.RenamesCut), is located unknown,
// saying why; where they were not, it is gone.
func TestLocateSaysNotKnownWhereRenamesWereNotLookedFor(t *testing.T) {
	from, to := strings.Repeat("a", 40), strings.Repeat("b", 40)
	f := dispatch.Finding{Kind: dispatch.FindingKindFile, Path: "x.txt", AnchorCommit: from}
	for _, cut := range []bool{true, false} {
		l := (&Server{}).newLocator()
		l.repos["r"] = locatorRepo{dir: "repo"}
		l.files[[2]string{to, "x.txt"}] = locatorFile{err: gitrepo.ErrNoFile}
		l.diffs[[3]string{"repo", from, to}] = gitrepo.Detail{Changes: []gitrepo.Change{{Path: "x.txt", Status: "D"}}, RenamesCut: cut}
		st := l.locate(t.Context(), f, dispatch.Candidate{Run: "r", Commit: to})
		want := locatedState{State: locatedFileGone}
		if cut {
			want = locatedState{State: locatedUnknown, Why: "more files changed than a rename is looked for in"}
		}
		if st == nil || *st != want {
			t.Errorf("renames cut %v: %+v, want %+v", cut, st, want)
		}
	}
}

// gitFile is a file of a commit gitHistory makes: its mode as git's index
// keeps it, and its body.
type gitFile struct{ mode, body string }

// gitHistory is a repository whose commits hold, one each, the files
// given, path to file, put in git's index by their modes alone (no file
// system modes or links, so the Mac makes the same): the repository and
// its commits.
func gitHistory(t *testing.T, steps ...map[string]gitFile) (string, []string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	gitInit(t, dir, false)
	var ids []string
	for _, files := range steps {
		gitRun(t, dir, "read-tree", "--empty")
		paths := slices.Sorted(maps.Keys(files))
		bodies := t.TempDir()
		var names []string
		for i, p := range paths {
			name := filepath.Join(bodies, strconv.Itoa(i))
			if err := os.WriteFile(name, []byte(files[p].body), 0o644); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			blobs := strings.Fields(gitIn(t, dir, append([]string{"hash-object", "-w", "--"}, names...)...))
			var index strings.Builder
			for i, p := range paths {
				fmt.Fprintf(&index, "%s %s\t%s\n", files[p].mode, blobs[i], p)
			}
			cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--index-info")
			cmd.Stdin = strings.NewReader(index.String())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git update-index: %v\n%s", err, out)
			}
		}
		gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "step")
		ids = append(ids, gitIn(t, dir, "rev-parse", "HEAD"))
	}
	return dir, ids
}

// Files moved, deleted and added between two commits of a real repository,
// compared by gitrepo.Compare and located by the locator: the comparison
// holds every path that left the first commit and every path that came
// into the second, and a file moved is located renamed to where it went,
// or not known where renames were cut, and never gone; a file deleted is
// gone where renames were not cut. Many files of one content moved with
// more of it deleted, files of one content and one mode moved (ordinary,
// executable, links), and an ordinary file moved and changed a little are
// found renamed. Not known: an executable file moved and changed a
// little, an executable file moved unchanged as an ordinary one, one
// content deleted twice and added once under an unrelated name, and 101
// files moved and changed a little. A file deleted while an unrelated one
// is added, and a file deleted alone, are gone.
func TestLocateNeverSaysAMovedFileIsGone(t *testing.T) {
	ctx := t.Context()
	const cutWhy = "more files changed than a rename is looked for in"
	// files is n files under folder, from the from'th, of one mode, each
	// holding body(i).
	files := func(folder string, from, n int, mode string, body func(i int) string) map[string]gitFile {
		out := map[string]gitFile{}
		for i := from; i < from+n; i++ {
			out[fmt.Sprintf("%s/f%03d", folder, i)] = gitFile{mode, body(i)}
		}
		return out
	}
	one := func(int) string { return "one content\n" }
	own := func(i int) string { return fmt.Sprintf("the content of file %d\n", i) }
	var lines strings.Builder
	for i := range 20 {
		fmt.Fprintf(&lines, "line %d\n", i)
	}
	text, changed := lines.String(), strings.Replace(lines.String(), "line 7\n", "line seven\n", 1)
	// movedFrom is the place a file of old/ went to in new/, its name
	// kept, for each file from first on; the others are gone.
	movedFrom := func(first string) func(p string) string {
		return func(p string) string {
			if p < first {
				return locatedFileGone
			}
			return "new/" + path.Base(p)
		}
	}
	always := func(state string) func(string) string { return func(string) string { return state } }
	for _, c := range []struct {
		name          string
		before, after map[string]gitFile
		cut           bool
		// want is where each file of before that is not in after is
		// located on the second commit: gone, unknown, or the path it
		// was renamed to.
		want func(p string) string
	}{
		{"9 files of one content moved and 3 more of it deleted", files("old", 0, 12, "100644", one), files("new", 3, 9, "100644", one), false, movedFrom("old/f003")},
		{"11 files of one content moved and 10 more of it deleted", files("old", 0, 21, "100644", one), files("new", 10, 11, "100644", one), false, movedFrom("old/f010")},
		{"20 ordinary files of one content moved", files("old", 0, 20, "100644", one), files("new", 0, 20, "100644", one), false, movedFrom("")},
		{"20 executable files of one content moved", files("old", 0, 20, "100755", one), files("new", 0, 20, "100755", one), false, movedFrom("")},
		{"20 links of one content moved", files("old", 0, 20, "120000", one), files("new", 0, 20, "120000", one), false, movedFrom("")},
		{"an ordinary file moved and changed a little", map[string]gitFile{"old/notes.md": {"100644", text}}, map[string]gitFile{"new/notes.md": {"100644", changed}}, false, movedFrom("")},
		{"an executable file moved and changed a little", map[string]gitFile{"old/run.sh": {"100755", text}}, map[string]gitFile{"new/run.sh": {"100755", changed}}, true, always(locatedUnknown)},
		{"an executable file moved unchanged as an ordinary one", map[string]gitFile{"old/run.sh": {"100755", text}}, map[string]gitFile{"new/run.txt": {"100644", text}}, true, always(locatedUnknown)},
		{"one content deleted twice and added once under an unrelated name", map[string]gitFile{"a.txt": {"100644", text}, "b.txt": {"100644", text}}, map[string]gitFile{"zz/q.md": {"100644", text}}, true, always(locatedUnknown)},
		{"101 files moved and changed a little", files("old", 0, 101, "100644", own), files("new", 0, 101, "100644", func(i int) string { return own(i) + "and a line more\n" }), true, always(locatedUnknown)},
		{"a file deleted while an unrelated one is added", map[string]gitFile{"gone.txt": {"100644", "a file that goes\n"}}, map[string]gitFile{"other.txt": {"100644", "1\n2\n3\n"}}, false, always(locatedFileGone)},
		{"a file deleted alone", map[string]gitFile{"gone.txt": {"100644", "a file that goes\n"}, "keep.txt": {"100644", "kept\n"}}, map[string]gitFile{"keep.txt": {"100644", "kept\n"}}, false, always(locatedFileGone)},
	} {
		dir, ids := gitHistory(t, c.before, c.after)
		d, err := gitrepo.Compare(ctx, dir, ids[0], ids[1])
		if err != nil || d.RenamesCut != c.cut {
			t.Errorf("%s: renames cut %v, %v; want %v", c.name, d.RenamesCut, err, c.cut)
			continue
		}
		// Every path that left and every path that came, once.
		left, came := map[string]int{}, map[string]int{}
		renames := 0
		for _, ch := range d.Changes {
			switch ch.Status {
			case "D":
				left[ch.Path]++
			case "A":
				came[ch.Path]++
			case "R":
				left[ch.From]++
				came[ch.Path]++
				renames++
			}
		}
		for p := range c.before {
			if _, kept := c.after[p]; !kept && left[p] != 1 {
				t.Errorf("%s: %s is listed as gone or moved %d times, want once", c.name, p, left[p])
			}
		}
		for p := range c.after {
			if _, kept := c.before[p]; !kept && came[p] != 1 {
				t.Errorf("%s: %s is listed as added or moved to %d times, want once", c.name, p, came[p])
			}
		}
		// A locator for each file, so that none reads past its
		// maxLocateFiles, sharing the comparison it made first.
		diffs := map[[3]string]gitrepo.Detail{}
		states := map[string]int{}
		for p := range c.before {
			if _, kept := c.after[p]; kept {
				continue
			}
			l := (&Server{}).newLocator()
			l.repos["r"], l.diffs = locatorRepo{dir: dir}, diffs
			st := l.locate(ctx, dispatch.Finding{Kind: dispatch.FindingKindFile, Path: p, AnchorCommit: ids[0]}, dispatch.Candidate{Run: "r", Commit: ids[1]})
			if st == nil {
				t.Fatalf("%s, %s: no state", c.name, p)
			}
			states[st.State]++
			switch want := c.want(p); {
			case want == locatedUnknown && *st == locatedState{State: locatedUnknown, Why: cutWhy}:
			case want == locatedFileGone && *st == locatedState{State: locatedFileGone}:
			case st.State == locatedRenamed && st.Path == want:
			default:
				t.Errorf("%s, %s: %+v; want %s", c.name, p, st, want)
			}
		}
		t.Logf("%s: renames cut %v, %d of %d files listed renamed, located %v", c.name, d.RenamesCut, renames, len(d.Changes), states)
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
