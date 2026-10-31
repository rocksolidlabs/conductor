package session

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A name a person gives a session loses its control characters (C0, DEL and
// C1) and the space around it, and is cut to proto.MaxSessionName bytes at a
// character boundary, never inside one, with no space left at the end;
// nothing left is "".
func TestCleanSessionName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  docs sweep \t", "docs sweep"},
		{"a\x00b\x1bc\x7fd\u0085e\u009bf\ng\rh", "abcdefgh"},
		{" \t\n\x1b\u0085 ", ""},
		{"", ""},
		{"日本語の名前", "日本語の名前"},
		{strings.Repeat("a", 200), strings.Repeat("a", proto.MaxSessionName)},
		// Two bytes each: sixty fit exactly.
		{strings.Repeat("é", 100), strings.Repeat("é", proto.MaxSessionName/2)},
		// Three bytes each after one: the fortieth would end at byte 121.
		{"a" + strings.Repeat("名", 50), "a" + strings.Repeat("名", 39)},
		// Four bytes each: thirty fit exactly.
		{strings.Repeat("😀", 40), strings.Repeat("😀", proto.MaxSessionName/4)},
		// The cut ends at a space: it goes too.
		{strings.Repeat("a", 119) + " b", strings.Repeat("a", 119)},
	} {
		got := CleanSessionName(tc.in)
		if got != tc.want || len(got) > proto.MaxSessionName || !utf8.ValidString(got) {
			t.Errorf("CleanSessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A rename is the session's name from then on and the name a resume of it
// starts under (Launched), the rest of the launch as it was; it is told once
// through OnChange, a rename to the name it has tells nothing, and an ended
// session is renamed as a running one is.
func TestRenameIsTheNameAResumeStartsUnder(t *testing.T) {
	var mu sync.Mutex
	var told []string
	names := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(told)
	}
	p := newFakeProc()
	t.Cleanup(p.exit)
	launched := Launched{AgentID: "claude", Name: "Claude Code #1", Cwd: "/work", Args: []string{"--verbose"}, Env: map[string]string{"GOAL": "ship"}, Yolo: true}
	s := NewLocal(Info{ID: "sess", Name: "Claude Code #1", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, p, Options{
		Launched: launched,
		OnChange: func(i Info) {
			mu.Lock()
			told = append(told, i.Name)
			mu.Unlock()
		},
	})
	if got := s.Launched(); got.Name != "Claude Code #1" {
		t.Fatalf("before a rename a resume starts under %q", got.Name)
	}

	s.Rename("docs sweep")
	if got := s.Info().Name; got != "docs sweep" {
		t.Fatalf("Info().Name = %q", got)
	}
	got := s.Launched()
	want := launched
	want.Name = "docs sweep"
	if got.AgentID != want.AgentID || got.Name != want.Name || got.Cwd != want.Cwd || !slices.Equal(got.Args, want.Args) || got.Env["GOAL"] != "ship" || len(got.Env) != 1 || got.Yolo != want.Yolo {
		t.Fatalf("Launched() = %+v, want %+v", got, want)
	}
	if n := names(); !slices.Equal(n, []string{"docs sweep"}) {
		t.Fatalf("told %q, want the rename once", n)
	}
	s.Rename("docs sweep")
	if n := names(); len(n) != 1 {
		t.Fatalf("a rename to the same name told %q", n)
	}

	p.exit()
	<-s.Ended()
	s.Rename("after the end")
	if s.Info().Name != "after the end" || s.Launched().Name != "after the end" || !slices.Contains(names(), "after the end") {
		t.Fatalf("an ended session's rename: %q, %q, told %q", s.Info().Name, s.Launched().Name, names())
	}
}

// Renames, listings and a resume's read of the launch may all come at once.
func TestRenameIsSafeForConcurrentUse(t *testing.T) {
	s, _ := newLocalWith(t, Options{Launched: Launched{Name: "first"}})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(3)
		go func() { defer wg.Done(); s.Rename(fmt.Sprintf("name %d", i)) }()
		go func() { defer wg.Done(); _ = s.Info().Name }()
		go func() { defer wg.Done(); _ = s.Launched().Name }()
	}
	wg.Wait()
	if got, launched := s.Info().Name, s.Launched().Name; got != launched || !strings.HasPrefix(got, "name ") {
		t.Fatalf("after the renames the name is %q and a resume's %q", got, launched)
	}
}
