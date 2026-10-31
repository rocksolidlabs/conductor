package crew

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// exampleIDs are the ids of the example crews, in the order Examples lists them.
var exampleIDs = []string{"example-todo-app", "example-todo-app-tasks", "example-test-fixer", "example-docs-writer", "example-dependency-upgrade"}

// Every example is a crew the store would accept: the five ids in order,
// claude and codex members with role prompts, the server's working
// directory, a worktree per member. A prompt typed into the member uses the
// goal; a role in a crew with tasks need not, its task file says the goal.
func TestExamplesAreValidCrews(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	list := Examples("/srv/work", now)
	var ids []string
	for _, c := range list {
		ids = append(ids, c.ID)
		if err := c.validateWithID(); err != nil {
			t.Errorf("%s: %v", c.ID, err)
		}
		if c.Cwd != "/srv/work" || c.Isolation != IsolationWorktree || c.Where != WhereServer || !c.OpenAfterLaunch || !c.CreatedAt.Equal(now) || !c.UpdatedAt.Equal(now) {
			t.Errorf("%s: %+v", c.ID, c)
		}
		if len(c.Members) < 2 || !strings.HasPrefix(c.Name, "Example: ") {
			t.Errorf("%s: %d members, name %q", c.ID, len(c.Members), c.Name)
		}
		for _, m := range c.Members {
			if m.AgentID != "claude" && m.AgentID != "codex" {
				t.Errorf("%s/%s: agent %q", c.ID, m.Name, m.AgentID)
			}
			if len(c.Tasks) == 0 && !strings.Contains(m.Prompt, "$GOAL") {
				t.Errorf("%s/%s: the prompt does not use $GOAL", c.ID, m.Name)
			}
		}
	}
	if !slices.Equal(ids, exampleIDs) {
		t.Fatalf("ids: %v", ids)
	}
}

// The todo app: a lead that plans, two builders after the lead, a tester
// after cli (a start condition names one member), told to merge the lead's
// and cli's branches, wait for core's and merge it too. Its goal says it is
// meant for a fresh repository, not the project the server works in.
func TestTodoAppStartsLeadThenBuildersThenTester(t *testing.T) {
	todo := Examples("/w", time.Now())[0]
	if !strings.Contains(todo.Goal, "a fresh repository with one commit") {
		t.Errorf("the goal does not say the crew wants a fresh repository: %s", todo.Goal)
	}
	want := map[string]Start{
		"lead":   {When: StartImmediately},
		"core":   {When: StartAfter, Member: "lead"},
		"cli":    {When: StartAfter, Member: "lead"},
		"tester": {When: StartAfter, Member: "cli"},
	}
	if len(todo.Members) != len(want) {
		t.Fatalf("members: %+v", todo.Members)
	}
	for _, m := range todo.Members {
		if m.Start != want[m.Name] {
			t.Errorf("%s: %+v", m.Name, m.Start)
		}
	}
	tester := todo.Members[3].Prompt
	for _, part := range []string{
		"git merge --no-edit crew/$CONDUCTOR_RUN/lead",
		"git merge --no-edit crew/$CONDUCTOR_RUN/cli",
		"wait until crew/$CONDUCTOR_RUN/core",
		"git merge --no-edit crew/$CONDUCTOR_RUN/core",
		`"${CONDUCTOR_BIN:-conductor}" notify --event handoff`,
	} {
		if !strings.Contains(tester, part) {
			t.Errorf("the tester's prompt does not say %q: %s", part, tester)
		}
	}
}

// The todo app with tasks: the same app as the todo example, run by its
// tasks. A plan, two parts built from it at once, a task that needs both
// (its worktree starts at core's result and merges cli's), a review of that
// one; every work task checked, the plan by its file and the rest by the
// suite as PLAN.md lays it out, every check an argv run with no shell. cli
// is told to build and test without core, which it does not have; the task
// a review reopens is told it owns the repairs, in either part; the plan
// commits the .gitignore that keeps a tested worktree clean to submit.
func TestTodoAppWithTasks(t *testing.T) {
	list := Examples("/w", time.Now())
	todo, c := list[0], list[1]
	if c.ID != "example-todo-app-tasks" || c.Name != "Example: todo app, with tasks" {
		t.Fatalf("second example: %s %q", c.ID, c.Name)
	}
	if !strings.HasPrefix(c.Goal, strings.TrimSuffix(todo.Goal, ", with tests")) || !strings.Contains(c.Goal, "TODO_FILE") {
		t.Errorf("the goal is not the todo app's with TODO_FILE: %s", c.Goal)
	}
	if len(c.Setup) != 0 || c.Isolation != IsolationWorktree {
		t.Errorf("setup %v, isolation %s", c.Setup, c.Isolation)
	}
	// Its project manifest (EN1): one prerequisite, python3 --version, and
	// nothing else, so the first real run shows a receipt and a host
	// without Python pauses before an attempt is spent.
	if m := c.Manifest; m == nil || len(m.Prerequisites) != 1 || m.Prerequisites[0].ID != "python" || !slices.Equal(m.Prerequisites[0].Argv, []string{"python3", "--version"}) ||
		m.File || len(m.Env) != 0 || len(m.Ports) != 0 || len(m.Setup) != 0 || len(m.Services) != 0 || len(m.Ready) != 0 || m.Preview != "" || len(m.Verify) != 0 || len(m.Cleanup) != 0 {
		t.Errorf("the manifest: %+v", c.Manifest)
	}
	for _, other := range list {
		if other.ID != c.ID && other.Manifest != nil {
			t.Errorf("%s has a manifest: %+v", other.ID, other.Manifest)
		}
	}
	agents := map[string]string{"lead": "claude", "core": "claude", "cli": "codex", "reviewer": "codex"}
	if len(c.Members) != len(agents) {
		t.Fatalf("members: %+v", c.Members)
	}
	for _, m := range c.Members {
		if m.AgentID != agents[m.Name] || m.Start != (Start{When: StartTask}) {
			t.Errorf("%s: agent %s, start %+v", m.Name, m.AgentID, m.Start)
		}
	}
	suite := []string{"python3", "-m", "unittest", "discover", "-s", "tests", "-t", "."}
	want := []struct {
		id, member, kind string
		needs            []string
		check            []string
	}{
		{"plan", "lead", TaskWork, nil, []string{"test", "-s", "PLAN.md"}},
		{"core", "core", TaskWork, []string{"plan"}, suite},
		{"cli", "cli", TaskWork, []string{"plan"}, suite},
		{"tests", "core", TaskWork, []string{"core", "cli"}, suite},
		{"review", "reviewer", TaskReview, []string{"tests"}, nil},
	}
	if len(c.Tasks) != len(want) {
		t.Fatalf("tasks: %+v", c.Tasks)
	}
	for i, w := range want {
		got := c.Tasks[i]
		if got.ID != w.id || got.Member != w.member || got.Kind != w.kind || !slices.Equal(got.Needs, w.needs) {
			t.Errorf("task %d: %s by %s, %s, needs %v; want %+v", i, got.ID, got.Member, got.Kind, got.Needs, w)
		}
		switch {
		case w.check == nil && len(got.Checks) != 0:
			t.Errorf("%s: checks %+v", got.ID, got.Checks)
		case w.check != nil && (len(got.Checks) != 1 || !slices.Equal(got.Checks[0].Argv, w.check)):
			t.Errorf("%s: checks %+v, want %v", got.ID, got.Checks, w.check)
		}
	}
	says := map[string][]string{
		"plan":   {"main(argv, store)", "tests/test_*.py", "-s tests -t .", "TODO_FILE", ".gitignore", "todo/__init__.py", "tests/__init__.py"},
		"cli":    {"fake store", "import nothing from todo/store.py", "not in your worktree"},
		"tests":  {"git merge --no-edit", "Inputs", "tests/test_end_to_end.py", "in either part", "When the review asks for changes"},
		"review": {"PLAN.md", "TODO_FILE", "file and line"},
	}
	for _, tk := range c.Tasks {
		for _, part := range says[tk.ID] {
			if !strings.Contains(tk.Instructions, part) {
				t.Errorf("%s: the instructions do not say %q", tk.ID, part)
			}
		}
	}
}

// A member's worktree starts at the checkout's HEAD, so what an earlier
// member committed is on that member's branch only, crew/<run>/<member>:
// every member that starts after another begins by merging its branch, the
// run being in the member's environment as CONDUCTOR_RUN, which the agent's
// shell expands (ExpandPrompt expands only $GOAL). Every prompt is one line.
func TestExampleMembersMergeTheBranchTheyStartAfter(t *testing.T) {
	afters := 0
	for _, c := range Examples("/w", time.Now()) {
		for _, m := range c.Members {
			if strings.ContainsAny(m.Prompt, "\r\n\t") {
				t.Errorf("%s/%s: the prompt is not one line", c.ID, m.Name)
			}
			// The goal goes in where the prompt puts its own punctuation after
			// it: a goal that ended in a period would type "tests.. Plan".
			typed := typedPrompt(m.Prompt, c.Goal)
			for _, p := range []string{"..", ".:", ".,"} {
				if strings.Contains(typed, p) {
					t.Errorf("%s/%s: the typed prompt has %q: %s", c.ID, m.Name, p, typed)
				}
			}
			if m.Start.When != StartAfter {
				continue
			}
			afters++
			merge := "git merge --no-edit crew/$CONDUCTOR_RUN/" + m.Start.Member
			if !strings.HasPrefix(m.Prompt, "Start by merging") || !strings.Contains(ExpandPrompt(m.Prompt, c.Goal), merge) {
				t.Errorf("%s/%s: the prompt does not begin by merging %s's branch (%s): %s", c.ID, m.Name, m.Start.Member, merge, m.Prompt)
			}
		}
	}
	// core, cli, tester, fixer, writer, upgrader.
	if afters != 6 {
		t.Fatalf("%d members start after another", afters)
	}
}

// Seed adds what is missing and leaves what exists alone: a second seed adds
// nothing and an edit survives it; a deleted example comes back.
func TestSeedAddsOnceAndLeavesEditsAlone(t *testing.T) {
	s, _ := newStore(t)
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	added, skipped, err := s.Seed(Examples("/w", now))
	if err != nil || !slices.Equal(added, exampleIDs) || len(skipped) != 0 {
		t.Fatalf("first seed: %v %v %v", added, skipped, err)
	}
	c, err := s.Get("example-todo-app")
	if err != nil {
		t.Fatal(err)
	}
	c.Goal = "edited"
	if _, err := s.Update(c.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("example-docs-writer"); err != nil {
		t.Fatal(err)
	}
	added, skipped, err = s.Seed(Examples("/w", now))
	if err != nil || !slices.Equal(added, []string{"example-docs-writer"}) || !slices.Equal(skipped, []string{"example-todo-app", "example-todo-app-tasks", "example-test-fixer", "example-dependency-upgrade"}) {
		t.Fatalf("second seed: %v %v %v", added, skipped, err)
	}
	if got, err := s.Get("example-todo-app"); err != nil || got.Goal != "edited" {
		t.Fatalf("the edit was lost: %q %v", got.Goal, err)
	}
}

// Someone who loaded the four examples before the todo app with tasks, and
// edited one, gets it and only it by loading them again; the edit stays, and
// a third load adds nothing.
func TestSeedAddsTheTaskExampleToAnEarlierLoad(t *testing.T) {
	s, _ := newStore(t)
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	before := slices.DeleteFunc(Examples("/w", now), func(c Crew) bool { return c.ID == "example-todo-app-tasks" })
	if added, _, err := s.Seed(before); err != nil || len(added) != 4 {
		t.Fatalf("the earlier load: %v %v", added, err)
	}
	c, err := s.Get("example-todo-app")
	if err != nil {
		t.Fatal(err)
	}
	c.Goal = "edited"
	if _, err := s.Update(c.ID, c); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := s.Seed(Examples("/w", now))
	if err != nil || !slices.Equal(added, []string{"example-todo-app-tasks"}) || len(skipped) != 4 {
		t.Fatalf("loading again: %v %v %v", added, skipped, err)
	}
	if got, err := s.Get("example-todo-app-tasks"); err != nil || len(got.Tasks) != 5 {
		t.Fatalf("the task example as saved: %+v %v", got.Tasks, err)
	}
	if got, err := s.Get("example-todo-app"); err != nil || got.Goal != "edited" {
		t.Fatalf("the edit was lost: %q %v", got.Goal, err)
	}
	if added, _, err := s.Seed(Examples("/w", now)); err != nil || len(added) != 0 {
		t.Fatalf("a third load: %v %v", added, err)
	}
}

// The task example is seeded with its project manifest at revision 1, so
// its receipts say crew:1; a copy loaded before it had one keeps no
// manifest after a later load (Seed skips a crew loaded before), so
// nobody's saved crew changes under them.
func TestSeedGivesTheTaskExampleItsManifestAndLeavesAnEarlierCopyAlone(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s, _ := newStore(t)
	if _, _, err := s.Seed(Examples("/w", now)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("example-todo-app-tasks")
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest == nil || len(got.Manifest.Prerequisites) != 1 || got.Manifest.Prerequisites[0].ID != "python" || got.ManifestRevision != 1 {
		t.Fatalf("seeded: %+v, revision %d", got.Manifest, got.ManifestRevision)
	}
	if other, err := s.Get("example-todo-app"); err != nil || other.Manifest != nil || other.ManifestRevision != 0 {
		t.Fatalf("the rules example: %+v %d %v", other.Manifest, other.ManifestRevision, err)
	}

	old, _ := newStore(t)
	before := Examples("/w", now)
	for i := range before {
		before[i].Manifest = nil
	}
	if _, _, err := old.Seed(before); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := old.Seed(Examples("/w", now))
	if err != nil || len(added) != 0 || !slices.Contains(skipped, "example-todo-app-tasks") {
		t.Fatalf("loading again: %v %v %v", added, skipped, err)
	}
	if kept, err := old.Get("example-todo-app-tasks"); err != nil || kept.Manifest != nil || kept.ManifestRevision != 0 {
		t.Fatalf("the earlier copy changed: %+v %d %v", kept.Manifest, kept.ManifestRevision, err)
	}
}

// A file with an example's id that the store cannot read is skipped and left
// as it is, never overwritten by the seed.
func TestSeedLeavesAnUnreadableFileAlone(t *testing.T) {
	s, st := newStore(t)
	path := filepath.Join(st.Dir(), "crews", "example-test-fixer.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := s.Seed(Examples("/w", time.Now()))
	if err != nil || !slices.Equal(skipped, []string{"example-test-fixer"}) || len(added) != 4 {
		t.Fatalf("%v %v %v", added, skipped, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatalf("the unreadable file was overwritten: %s", b)
	}
}

// A link with an example's id, to a file or to nothing, is skipped and left
// as it is: the seed neither writes through it nor makes what it names.
func TestSeedLeavesLinksAlone(t *testing.T) {
	s, st := newStore(t)
	dir := filepath.Join(st.Dir(), "crews")
	outside := t.TempDir()
	target := filepath.Join(outside, "target.json")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(outside, "missing.json")
	if err := os.Symlink(target, filepath.Join(dir, "example-test-fixer.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(missing, filepath.Join(dir, "example-docs-writer.json")); err != nil {
		t.Fatal(err)
	}
	added, skipped, err := s.Seed(Examples("/w", time.Now()))
	if err != nil || !slices.Equal(added, []string{"example-todo-app", "example-todo-app-tasks", "example-dependency-upgrade"}) || !slices.Equal(skipped, []string{"example-test-fixer", "example-docs-writer"}) {
		t.Fatalf("%v %v %v", added, skipped, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatalf("the seed wrote through the link: %q %v", b, err)
	}
	if des, err := os.ReadDir(outside); err != nil || len(des) != 1 {
		t.Fatalf("the seed made what the dangling link names: %v %v", des, err)
	}
	for _, name := range []string{"example-test-fixer.json", "example-docs-writer.json"} {
		if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a link: %v %v", name, fi, err)
		}
	}
}

// A crew the store refuses stops the seed, naming it; the ones before it stay.
func TestSeedStopsAtAnInvalidCrew(t *testing.T) {
	s, _ := newStore(t)
	list := Examples("/w", time.Now())
	fixer := slices.IndexFunc(list, func(c Crew) bool { return c.ID == "example-test-fixer" })
	list[fixer].Cwd = strings.Repeat("x", maxCwd+1)
	added, _, err := s.Seed(list)
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "example-test-fixer") || !slices.Equal(added, exampleIDs[:fixer]) {
		t.Fatalf("%v %v", added, err)
	}
	if _, err := s.Get("example-docs-writer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("seeding went on past the error: %v", err)
	}
}
