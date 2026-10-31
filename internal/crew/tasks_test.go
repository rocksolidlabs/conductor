package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// taskMember is a member of a managed crew: started by its tasks.
func taskMember(name string) Member {
	return Member{Name: name, AgentID: "claude", Prompt: "Do your part.", Start: Start{When: StartTask}}
}

// managedCrew is a crew with tasks: a builder and a reviewer, the review
// needing the build, a check on the build.
func managedCrew() Crew {
	c := testCrew(taskMember("builder"), taskMember("reviewer"))
	c.Isolation = IsolationWorktree
	c.Tasks = []Task{
		{ID: "build", Member: "builder", Kind: TaskWork, Instructions: "Build it.", Checks: []Check{{ID: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600}}},
		{ID: "review", Member: "reviewer", Kind: TaskReview, Instructions: "Review it.", Needs: []string{"build"}},
	}
	c.Setup = []Check{{ID: "deps", Argv: []string{"npm", "ci"}}}
	return c
}

func TestValidateAcceptsACrewWithTasks(t *testing.T) {
	c := managedCrew()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !c.Managed() || testCrew(immediate("lead", "x")).Managed() {
		t.Fatal("Managed is wrong")
	}
	// A crew with tasks launches, as a managed run (#334, row 6).
	if err := c.Launchable(); err != nil {
		t.Fatalf("launchable: %v", err)
	}
	if err := testCrew(immediate("lead", "x")).Launchable(); err != nil {
		t.Fatalf("a legacy crew: %v", err)
	}
}

func TestValidateRejectsBadTasks(t *testing.T) {
	long := strings.Repeat("x", maxInstructions+1)
	cases := []struct {
		name  string
		edit  func(c *Crew)
		words string
	}{
		{"a member not started by its tasks", func(c *Crew) { c.Members[0].Start = Start{When: StartImmediately} }, `start.when must be "task"`},
		{"a task-started member in a legacy crew", func(c *Crew) { c.Tasks, c.Setup = nil, nil }, `only for a crew with tasks`},
		{"setup without tasks", func(c *Crew) {
			c.Tasks = nil
			c.Members[0].Start, c.Members[1].Start = Start{When: StartImmediately}, Start{When: StartImmediately}
		}, "setup is only set with tasks"},
		{"no worktree isolation", func(c *Crew) { c.Isolation = IsolationNone }, `needs isolation "worktree"`},
		{"too many tasks", func(c *Crew) {
			for i := range maxTasks {
				c.Tasks = append(c.Tasks, Task{ID: "t" + strings.Repeat("a", i%30) + string(rune('a'+i%26)), Member: "builder", Kind: TaskWork})
			}
		}, "too many tasks"},
		{"a bad task id", func(c *Crew) { c.Tasks[0].ID = "Build.1" }, "id must match"},
		{"a task id used twice", func(c *Crew) { c.Tasks[1].ID = "build" }, "used twice"},
		{"a bad kind", func(c *Crew) { c.Tasks[0].Kind = "chore" }, `kind "chore" must be`},
		{"an unknown member", func(c *Crew) { c.Tasks[0].Member = "ghost" }, "not a member of this crew"},
		{"instructions too long", func(c *Crew) { c.Tasks[0].Instructions = long }, "at most 8000 characters"},
		{"NUL in instructions", func(c *Crew) { c.Tasks[0].Instructions = "a\x00b" }, "contain NUL"},
		{"too many checks", func(c *Crew) {
			for i := range maxChecks {
				c.Tasks[0].Checks = append(c.Tasks[0].Checks, Check{ID: "c" + string(rune('a'+i)), Argv: []string{"true"}})
			}
		}, "too many checks"},
		{"a review with checks", func(c *Crew) { c.Tasks[1].Checks = []Check{{ID: "x", Argv: []string{"true"}}} }, "a review has no checks"},
		{"a check with no program", func(c *Crew) { c.Tasks[0].Checks[0].Argv = nil }, "argv must name a program"},
		{"a check id used twice", func(c *Crew) {
			c.Tasks[0].Checks = append(c.Tasks[0].Checks, Check{ID: "unit", Argv: []string{"true"}})
		}, "check id \"unit\" is used twice"},
		{"a check timeout past the bound", func(c *Crew) { c.Tasks[0].Checks[0].TimeoutSeconds = maxCheckTimeout + 1 }, "timeoutSeconds must be between 1 and 3600"},
		{"a check argv with NUL", func(c *Crew) { c.Tasks[0].Checks[0].Argv = []string{"go", "te\x00st"} }, "argv[1] contains NUL"},
		{"a setup entry with a bad id", func(c *Crew) { c.Setup[0].ID = "Deps" }, "setup[0]: id"},
		{"too many setup entries", func(c *Crew) {
			for i := range maxChecks {
				c.Setup = append(c.Setup, Check{ID: "s" + string(rune('a'+i)), Argv: []string{"true"}})
			}
		}, "too many setup commands"},
		{"a need of itself", func(c *Crew) { c.Tasks[0].Needs = []string{"build"} }, "cannot need itself"},
		{"an unknown need", func(c *Crew) { c.Tasks[0].Needs = []string{"ghost"} }, `needs "ghost", which is not a task`},
		{"a review needing two tasks", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "docs", Member: "builder", Kind: TaskWork, Instructions: "Write the docs."})
			c.Tasks[1].Needs = []string{"build", "docs"}
		}, "a review needs exactly one work task"},
		{"a review needing a review", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "rereview", Member: "reviewer", Kind: TaskReview, Instructions: "Again.", Needs: []string{"review"}})
		}, "a review needs exactly one work task"},
		{"a cycle", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "docs", Member: "builder", Kind: TaskWork, Instructions: "Docs.", Needs: []string{"build"}})
			c.Tasks[0].Needs = []string{"docs"}
		}, "go round in a cycle"},
		{"a member with no task", func(c *Crew) { c.Members = append(c.Members, taskMember("idle")) }, `member "idle": has no task`},
	}
	for _, tc := range cases {
		c := managedCrew()
		tc.edit(&c)
		err := c.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words) {
			t.Errorf("%s: %v (want %q)", tc.name, err, tc.words)
		}
	}
}

// A save holds a task's goal, role and instructions to maxTaskText as its
// task.json keeps them (taskTextBytes): U+2028 and U+2029 escaped, six
// bytes each, and a lone surrogate, which the web's JSON.stringify sends as
// an escape, decoded as U+FFFD, three bytes. web/app/utils/crews.test.ts
// holds the same cases for taskSaveProblems.
func TestATasksTextIsCountedAsItsTaskFileKeepsIt(t *testing.T) {
	// sized is managedCrew with a goal, the builder's role and the build's
	// instructions of 1500, 3000 and 5500 four-byte characters, last after
	// the instructions: 40,006 bytes as the task file keeps them before it.
	wide := "\U0001D11E"
	sized := func(last string) Crew {
		c := managedCrew()
		c.Goal = strings.Repeat(wide, 1500)
		c.Members[0].Prompt = strings.Repeat(wide, 3000)
		c.Tasks[0].Instructions = strings.Repeat(wide, 5500) + last
		return c
	}
	// held checks that the build's text in c is size bytes as its task file
	// keeps it, and that a save of c is refused for it exactly when that is
	// past the bound.
	held := func(name string, c Crew, size int) {
		t.Helper()
		if n := taskTextBytes(c.Goal, c.Members[0].Prompt, c.Tasks[0].Instructions); n != size {
			t.Fatalf("%s: %d bytes as the task file keeps them (want %d)", name, n, size)
		}
		err := c.ValidateSave()
		if size <= maxTaskText {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			return
		}
		want := fmt.Sprintf(`task "build": its goal, role and instructions are %d bytes as its task file keeps them, more than %d`, size, maxTaskText)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", name, err, want)
		}
	}
	ls, ps := string(rune(0x2028)), string(rune(0x2029))
	held("ASCII at the bound", sized(strings.Repeat("y", 954)), maxTaskText)
	held("ASCII past it", sized(strings.Repeat("y", 955)), maxTaskText+1)
	held("U+2028 at the bound", sized(strings.Repeat("y", 948)+ls), maxTaskText)
	held("U+2028 past it", sized(strings.Repeat("y", 949)+ls), maxTaskText+1)
	held("U+2029 past it", sized(strings.Repeat("y", 949)+ps), maxTaskText+1)
	// A lone surrogate comes as the web sends it, an escape in the body,
	// and is decoded as U+FFFD.
	for _, tc := range []struct{ ys, size int }{{951, maxTaskText}, {952, maxTaskText + 1}} {
		body, err := json.Marshal(sized(strings.Repeat("y", tc.ys) + "@"))
		if err != nil {
			t.Fatal(err)
		}
		var c Crew
		if err := json.Unmarshal([]byte(strings.Replace(string(body), `@"`, `\`+`ud800"`, 1)), &c); err != nil {
			t.Fatal(err)
		}
		if got, want := c.Tasks[0].Instructions, strings.Repeat(wide, 5500)+strings.Repeat("y", tc.ys)+string(rune(0xfffd)); got != want {
			t.Fatalf("the escape decoded as %q", strings.TrimPrefix(got, strings.Repeat(wide, 5500)+strings.Repeat("y", tc.ys)))
		}
		held(fmt.Sprintf("a lone surrogate after %d bytes", tc.ys), c, tc.size)
	}
	// A backslash before u, d, 8, 0, 0 is text, not a lone surrogate's
	// escape: seven bytes as JSON.
	text := `\` + "ud800"
	held("a backslash and ud800 at the bound", sized(strings.Repeat("y", 947)+text), maxTaskText)
	held("a backslash and ud800 past it", sized(strings.Repeat("y", 948)+text), maxTaskText+1)
}

// A crew with tasks goes through the store whole; a crew without encodes
// as it did, with no tasks, setup, manifest or manifestRevision key.
func TestACrewWithTasksRoundTrips(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, problems, err := NewStore(st)
	if err != nil || len(problems) != 0 {
		t.Fatal(err, problems)
	}
	c := managedCrew()
	created, err := s.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 2 || got.Tasks[0].Checks[0].TimeoutSeconds != 600 || got.Tasks[1].Needs[0] != "build" || len(got.Setup) != 1 || got.Setup[0].Argv[1] != "ci" {
		t.Fatalf("%+v", got)
	}
	legacy, err := json.Marshal(testCrew(immediate("lead", "x")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "tasks") || strings.Contains(string(legacy), "setup") || strings.Contains(string(legacy), "manifest") {
		t.Fatalf("a legacy crew's JSON names tasks: %s", legacy)
	}
	if !strings.Contains(string(legacy), `"when":"immediately"`) {
		t.Fatalf("legacy JSON: %s", legacy)
	}
}

// A dense graph (every task needing every task before it) is valid and
// checked in the time its edges take, not its paths.
func TestValidateChecksADenseGraphQuickly(t *testing.T) {
	c := managedCrew()
	c.Tasks = nil
	for i := range maxTasks - 1 { // 127 work tasks, each needing every one before it, and the review
		tk := Task{ID: fmt.Sprintf("t%03d", i), Member: "builder", Kind: TaskWork, Instructions: "x"}
		for j := range i {
			tk.Needs = append(tk.Needs, fmt.Sprintf("t%03d", j))
		}
		c.Tasks = append(c.Tasks, tk)
	}
	c.Tasks = append(c.Tasks, Task{ID: "review", Member: "reviewer", Kind: TaskReview, Instructions: "x", Needs: []string{"t000"}})
	start := time.Now()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a dense graph took %v", d)
	}
	// With one edge back, the cycle is named from where it closes.
	c.Tasks[0].Needs = []string{"t005"}
	err := c.Validate()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "task \"t000\": the needs go round in a cycle") || !strings.Contains(err.Error(), "t005 needs t000") {
		t.Fatalf("the cycle: %v", err)
	}
}

// A refused operation on a managed run leaves the engine usable: the lock
// it looked the run up under is released.
func TestAManagedRunsRefusalLeavesTheEngineUsable(t *testing.T) {
	e, _ := newEngine(t)
	c := managedCrew()
	r := e.add(c, prepared{}, "")
	e.launched(r)
	ctx := t.Context()
	if err := e.StartMember(ctx, r.id, "builder"); !errors.Is(err, ErrManaged) {
		t.Fatalf("start: %v", err)
	}
	if err := e.AddMember(ctx, r.id, immediate("extra", "x")); !errors.Is(err, ErrManaged) {
		t.Fatalf("add: %v", err)
	}
	if _, err := e.ResumeMember(ctx, r.id, "builder", ""); !errors.Is(err, ErrManaged) {
		t.Fatalf("resume member: %v", err)
	}
	if _, err := e.ResumeRun(ctx, r.id); !errors.Is(err, ErrManaged) {
		t.Fatalf("resume run: %v", err)
	}
	done := make(chan struct{})
	go func() {
		e.List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine is locked after the refusals")
	}
}

// A member started by its tasks is not added to a legacy run: it would
// never start by a rule, and would make the run read as managed.
func TestATaskMemberIsNotAddedToALegacyRun(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "x")))
	if err != nil {
		t.Fatal(err)
	}
	err = e.AddMember(t.Context(), run.ID, taskMember("worker"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `only for a crew with tasks`) {
		t.Fatalf("add: %v", err)
	}
	if got, _ := e.Get(run.ID); len(got.Members) != 1 || got.managed() {
		t.Fatalf("the run after the refusal: %+v", got)
	}
}
