package crew

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// compareSpec is a comparison of n candidates in dir, each with its own
// approach (the third an independent try), one check and two criteria,
// with or without reviews.
func compareSpec(dir string, n int, reviews bool) CompareSpec {
	approaches := []string{"key the cache by locale", "drop the cache and memoise in the handler", "", "a cache per tenant"}
	spec := CompareSpec{Name: "Compare the cache", Cwd: dir, Goal: "Make the user list fast.", Instructions: "Make GET /v1/users answer from a cache.",
		Criteria: []Criterion{{ID: "no-deps", Text: "No new dependency."}, {ID: "p99", Text: "p99 of the cache read under 2 ms in bench_test.go."}},
		Checks:   []Check{{ID: "ok", Argv: []string{"true"}}}}
	for i := range n {
		spec.Candidates = append(spec.Candidates, CandidateSpec{AgentID: "claude", Approach: approaches[i]})
	}
	if reviews {
		spec.Reviews, spec.ReviewAgent = true, "claude"
	}
	return spec
}

// compareCrew is CompareTemplate's crew of compareSpec, with an id.
func compareCrew(t *testing.T, dir string, n int, reviews bool) Crew {
	t.Helper()
	c, err := CompareTemplate(compareSpec(dir, n, reviews))
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "api-sweep"
	return c
}

// compareEngine is managedEngine whose launches take a compare crew.
func compareEngine(t *testing.T) (*Engine, *fakeLauncher, *fakeLedger) {
	t.Helper()
	e, fl, l := managedEngine(t)
	e.compareOpen = true
	return e, fl, l
}

// taskByID is c's task id, for an edit.
func taskByID(c *Crew, id string) *Task {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return &c.Tasks[i]
		}
	}
	panic("no task " + id)
}

func TestCompareValidates(t *testing.T) {
	wide := strings.Repeat(" ", maxGoal)
	cases := []struct {
		name  string
		edit  func(c *Crew)
		words string // "" for a crew taken
	}{
		{"the template's crew", func(c *Crew) {}, ""},
		{"one candidate", func(c *Crew) { c.Compare.Candidates = c.Compare.Candidates[:1] }, "2 to 4 candidates, not 1"},
		{"five candidates", func(c *Crew) {
			c.Compare.Candidates = append(c.Compare.Candidates, Candidate{Task: "approach-d"}, Candidate{Task: "approach-e"})
		}, "2 to 4 candidates, not 5"},
		{"a candidate with a need", func(c *Crew) { taskByID(c, "approach-b").Needs = []string{"approach-a"} }, `"approach-b" needs "approach-a": a candidate starts at the run's base`},
		{"a review as a candidate", func(c *Crew) { c.Compare.Candidates[2].Task = "review-c" }, `"review-c" is a review: a candidate is a work task`},
		{"a candidate that is no task", func(c *Crew) { c.Compare.Candidates[2].Task = "approach-z" }, `"approach-z" is not a task of this crew`},
		{"a candidate twice", func(c *Crew) { c.Compare.Candidates[2].Task = "approach-a" }, `"approach-a" is a candidate twice`},
		{"other instructions", func(c *Crew) { taskByID(c, "approach-c").Instructions = "Something else." }, `"approach-a" and "approach-c" have different instructions`},
		{"another check id", func(c *Crew) { taskByID(c, "approach-b").Checks[0].ID = "other" }, "different checks"},
		{"another check argv", func(c *Crew) { taskByID(c, "approach-b").Checks[0].Argv = []string{"false"} }, "different checks"},
		{"another check timeout", func(c *Crew) { taskByID(c, "approach-b").Checks[0].TimeoutSeconds = 30 }, "different checks"},
		{"one more check", func(c *Crew) {
			b := taskByID(c, "approach-b")
			b.Checks = append(b.Checks, Check{ID: "two", Argv: []string{"true"}})
		}, "different checks"},
		{"no check and no manifest", noChecks(nil), "nothing to compare: the candidates have no checks and the crew no project manifest"},
		{"no check and a manifest without a verify step", noChecks(&Manifest{Setup: []Check{{ID: "deps", Argv: []string{"npm", "ci"}}}}), "nothing to compare: the candidates have no checks and the project manifest no verify step"},
		{"no check and a manifest with a verify step", noChecks(&Manifest{Verify: []Check{{ID: "lint", Argv: []string{"make", "lint"}}}}), ""},
		{"no check and the file-form manifest", noChecks(&Manifest{File: true}), ""},
		{"a member in two lanes", func(c *Crew) {
			taskByID(c, "approach-b").Member = "cand-a"
			c.Members = slices.DeleteFunc(c.Members, func(m Member) bool { return m.Name == "cand-b" })
		}, `member "cand-a" does tasks in two lanes ("approach-a" and "approach-b")`},
		{"a reviewer in two lanes", func(c *Crew) {
			taskByID(c, "review-b").Member = "rev-a"
			c.Members = slices.DeleteFunc(c.Members, func(m Member) bool { return m.Name == "rev-b" })
		}, `member "rev-a" does tasks in two lanes`},
		{"a task in no lane", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "extra", Member: "cand-a", Kind: TaskWork, Instructions: "More."})
		}, `task "extra" is in no lane`},
		{"a task building on a candidate", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "polish", Member: "cand-a", Kind: TaskWork, Instructions: "Polish.", Needs: []string{"approach-a"}})
		}, `task "polish" is in no lane`},
		{"a task needing two candidates", func(c *Crew) {
			c.Tasks = append(c.Tasks, Task{ID: "merge", Member: "cand-a", Kind: TaskWork, Instructions: "Merge.", Needs: []string{"approach-a", "approach-b"}})
		}, `task "merge" needs "approach-a" and "approach-b": a comparison never builds on two candidates`},
		{"two reviews of one candidate", func(c *Crew) { taskByID(c, "review-b").Needs = []string{"approach-a"} }, `tasks "review-a" and "review-b" both review "approach-a"`},
		{"a review of a review", func(c *Crew) { taskByID(c, "review-b").Needs = []string{"review-a"} }, "a review needs exactly one work task"},
		{"no criterion", func(c *Crew) { c.Compare.Criteria = nil }, "1 to 12 criteria, not 0"},
		{"12 criteria", criteria(12), ""},
		{"13 criteria", criteria(13), "1 to 12 criteria, not 13"},
		{"a criterion of 500 characters", func(c *Crew) { c.Compare.Criteria[0].Text = strings.Repeat("é", maxCriterionText) }, ""},
		{"a criterion of 501 characters", func(c *Crew) { c.Compare.Criteria[0].Text = strings.Repeat("é", maxCriterionText+1) }, `criterion "no-deps": the text is 1 to 500 characters`},
		{"a blank criterion", func(c *Crew) { c.Compare.Criteria[0].Text = "  " }, `criterion "no-deps": the text is 1 to 500 characters`},
		{"a criterion over two lines", func(c *Crew) { c.Compare.Criteria[0].Text = "No new\ndependency." }, `criterion "no-deps": the text is one line`},
		{"a criterion named twice", func(c *Crew) { c.Compare.Criteria[1].ID = "no-deps" }, `criterion "no-deps" is named twice`},
		{"a criterion's id", func(c *Crew) { c.Compare.Criteria[1].ID = "P99" }, "criteria[1]: id"},
		{"an approach of 2,000 characters", func(c *Crew) { c.Compare.Candidates[0].Approach = strings.Repeat("é", maxApproach) }, ""},
		{"an approach of 2,001 characters", func(c *Crew) { c.Compare.Candidates[0].Approach = strings.Repeat("é", maxApproach+1) }, `candidate "approach-a": the approach is at most 2000 characters`},
		{"an approach with NUL", func(c *Crew) { c.Compare.Candidates[0].Approach = "a\x00b" }, "the approach contains NUL"},
		{"a selector", func(c *Crew) { c.Compare.Selector = "role:architecture" }, "selector is not taken yet"},
		{"a criterion with a line separator", func(c *Crew) { c.Compare.Criteria[0].Text = "No new dependency." }, "the text is one line"},
		{"the comparison past the never-cut bound", func(c *Crew) {
			// 12 KiB of goal, 8 KiB of instructions, 24 KiB of criteria:
			// each part within its bound, the task file's never-cut part not.
			c.Goal = wide
			for i := range c.Tasks {
				if c.Tasks[i].Kind == TaskWork {
					c.Tasks[i].Instructions = strings.Repeat("x", maxInstructions)
				}
			}
			c.Compare.Criteria = criteriaOf(maxCompareCriteria, strings.Repeat("\U0001D11E", maxCriterionText))
		}, "goal, role, instructions and comparison are"},
		{"the comparison within the never-cut bound", func(c *Crew) {
			c.Goal = wide
			c.Compare.Criteria = criteriaOf(maxCompareCriteria, strings.Repeat("\U0001D11E", maxCriterionText))
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := compareCrew(t, "/work/repo", 3, true)
			tc.edit(&c)
			err := c.ValidateSave()
			if tc.words == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words) {
				t.Fatalf("got %v, want %q", err, tc.words)
			}
		})
	}
	// Validate, which reads a crew saved before, holds the block too; the
	// never-cut bound is a save's.
	c := compareCrew(t, "/work/repo", 2, false)
	c.Compare.Selector = "person:ana"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "selector") {
		t.Fatalf("Validate: %v", err)
	}
	// A block on a crew without tasks.
	legacy := testCrew(immediate("lead", "go"))
	legacy.Compare = &Compare{Criteria: criteriaOf(1, "x"), Candidates: []Candidate{{Task: "a"}, {Task: "b"}}}
	if err := legacy.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "only set on a crew with tasks") {
		t.Fatalf("a crew without tasks: %v", err)
	}
}

// noChecks takes every candidate's checks away and gives the crew m as its
// manifest.
func noChecks(m *Manifest) func(c *Crew) {
	return func(c *Crew) {
		for i := range c.Tasks {
			c.Tasks[i].Checks = nil
		}
		c.Manifest = m
	}
}

// criteriaOf is n criteria c1, c2, … each of text.
func criteriaOf(n int, text string) []Criterion {
	var out []Criterion
	for i := range n {
		out = append(out, Criterion{ID: "c" + string(rune('a'+i)), Text: text})
	}
	return out
}

func criteria(n int) func(c *Crew) {
	return func(c *Crew) { c.Compare.Criteria = criteriaOf(n, "A criterion.") }
}

func TestCompareTemplateMakesAValidCrew(t *testing.T) {
	for n := minCandidates; n <= maxCandidates; n++ {
		for _, reviews := range []bool{false, true} {
			spec := compareSpec("/work/repo", n, reviews)
			yolo := true
			spec.Yolo = &yolo
			c, err := CompareTemplate(spec)
			if err != nil {
				t.Fatalf("%d candidates, reviews %v: %v", n, reviews, err)
			}
			if err := c.ValidateSave(); err != nil {
				t.Fatal(err)
			}
			if c.Isolation != IsolationWorktree || c.Where != WhereServer || c.Goal != spec.Goal || c.Cwd != spec.Cwd || c.Yolo == nil || !*c.Yolo || c.Yolo == spec.Yolo {
				t.Fatalf("the crew: %+v", c)
			}
			var members, tasks []string
			for _, m := range c.Members {
				members = append(members, m.Name)
			}
			for _, tk := range c.Tasks {
				tasks = append(tasks, tk.ID)
			}
			var wantM, wantT []string
			for i := range n {
				l := string(rune('a' + i))
				wantM, wantT = append(wantM, "cand-"+l), append(wantT, "approach-"+l)
			}
			if reviews {
				for i := range n {
					l := string(rune('a' + i))
					wantM, wantT = append(wantM, "rev-"+l), append(wantT, "review-"+l)
				}
			}
			if !slices.Equal(members, wantM) || !slices.Equal(tasks, wantT) {
				t.Fatalf("members %v, tasks %v", members, tasks)
			}
			for i, cd := range c.Compare.Candidates {
				l := string(rune('a' + i))
				task := *taskByID(&c, cd.Task)
				if cd.Task != "approach-"+l || cd.Approach != spec.Candidates[i].Approach || task.Kind != TaskWork || len(task.Needs) != 0 ||
					task.Instructions != spec.Instructions || !slices.EqualFunc(task.Checks, spec.Checks, func(a, b Check) bool { return a.ID == b.ID && slices.Equal(a.Argv, b.Argv) }) {
					t.Fatalf("candidate %d: %+v %+v", i, cd, task)
				}
				if p := c.Members[i].Prompt; !strings.Contains(p, "You are candidate "+strings.ToUpper(l)+" of ") || strings.Contains(p, "approach-") {
					t.Fatalf("cand-%s's role: %q", l, p)
				}
				if reviews {
					rv := *taskByID(&c, "review-"+l)
					if rv.Kind != TaskReview || !slices.Equal(rv.Needs, []string{cd.Task}) || rv.Member != "rev-"+l {
						t.Fatalf("review-%s: %+v", l, rv)
					}
				}
			}
			// The crew shares no slice with the spec.
			spec.Criteria[0].Text, spec.Checks[0].Argv[0] = "changed", "changed"
			if c.Compare.Criteria[0].Text == "changed" || c.Tasks[0].Checks[0].Argv[0] == "changed" {
				t.Fatal("the crew shares the spec's slices")
			}
		}
	}
	// Each bound of the spec at its edge.
	edge := func(edit func(s *CompareSpec)) error {
		s := compareSpec("/work/repo", 2, false)
		edit(&s)
		_, err := CompareTemplate(s)
		return err
	}
	for _, tc := range []struct {
		name  string
		edit  func(s *CompareSpec)
		words string
	}{
		{"one candidate", func(s *CompareSpec) { s.Candidates = s.Candidates[:1] }, "2 to 4 candidates, not 1"},
		{"five candidates", func(s *CompareSpec) {
			s.Candidates = slices.Repeat([]CandidateSpec{{AgentID: "claude"}}, 5)
		}, "2 to 4 candidates, not 5"},
		{"12 criteria", func(s *CompareSpec) { s.Criteria = criteriaOf(12, "A criterion.") }, ""},
		{"13 criteria", func(s *CompareSpec) { s.Criteria = criteriaOf(13, "A criterion.") }, "1 to 12 criteria, not 13"},
		{"a criterion of 500 characters", func(s *CompareSpec) { s.Criteria[0].Text = strings.Repeat("x", 500) }, ""},
		{"a criterion of 501 characters", func(s *CompareSpec) { s.Criteria[0].Text = strings.Repeat("x", 501) }, "1 to 500 characters"},
		{"an approach of 2,000 characters", func(s *CompareSpec) { s.Candidates[1].Approach = strings.Repeat("x", 2000) }, ""},
		{"an approach of 2,001 characters", func(s *CompareSpec) { s.Candidates[1].Approach = strings.Repeat("x", 2001) }, "at most 2000 characters"},
		{"8 checks", func(s *CompareSpec) { s.Checks = checksOf(maxChecks) }, ""},
		{"9 checks", func(s *CompareSpec) { s.Checks = checksOf(maxChecks + 1) }, "too many checks"},
		{"instructions of 8,000 characters", func(s *CompareSpec) { s.Instructions = strings.Repeat("x", maxInstructions) }, ""},
		{"instructions of 8,001 characters", func(s *CompareSpec) { s.Instructions = strings.Repeat("x", maxInstructions+1) }, "instructions must be at most 8000 characters"},
		{"reviews without their agent", func(s *CompareSpec) { s.Reviews = true }, "reviews need a reviewAgent"},
		{"a review agent without reviews", func(s *CompareSpec) { s.ReviewAgent = "codex" }, "reviews need a reviewAgent"},
		{"no name", func(s *CompareSpec) { s.Name = "" }, "name must not be empty"},
		{"an agent id", func(s *CompareSpec) { s.Candidates[0].AgentID = "Not An Agent" }, "is not an agent id"},
		{"no check and no manifest", func(s *CompareSpec) { s.Checks = nil }, "nothing to compare"},
		{"no check and the file-form manifest", func(s *CompareSpec) { s.Checks, s.Manifest = nil, &Manifest{File: true} }, ""},
	} {
		err := edge(tc.edit)
		if tc.words == "" && err != nil || tc.words != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words)) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.words)
		}
	}
}

func checksOf(n int) []Check {
	var out []Check
	for i := range n {
		out = append(out, Check{ID: "c" + string(rune('a'+i)), Argv: []string{"true"}})
	}
	return out
}

// In this row every launch of a compare crew is refused before a run
// exists, with its words, and so is a continuation of one: nothing reaches
// the ledger.
func TestACompareCrewNeedsATicket(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	c := compareCrew(t, dir, 2, true)
	ctx := t.Context()
	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "comparisons arrive with their launch statement") {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := e.Launch(ctx, c)
	refused("Launch", err)
	_, release, err := e.LaunchNamed(ctx, c, "named")
	release()
	refused("LaunchNamed", err)
	_, release, err = e.LaunchHeld(ctx, c)
	release()
	refused("LaunchHeld", err)
	p := planned(t, "api-sweep-0000000a", head(t, dir))
	_, release, err = e.LaunchPlanned(ctx, c, "CND-12", p)
	release()
	refused("LaunchPlanned", err)
	if rows, err := ld.Runs(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("the ledger's runs: %d %v", len(rows), err)
	}
	if len(e.List()) != 0 {
		t.Fatalf("the engine holds %d runs", len(e.List()))
	}

	// A continuation of a compare run (made here through the test's seam)
	// is refused the same way, and makes no run.
	e.compareOpen = true
	run := launchManaged(t, e, c)
	stopRun(t, e, run.ID)
	e.compareOpen = false
	_, err = e.ContinueRun(ctx, run.ID, "")
	refused("ContinueRun", err)
	if rows, err := ld.Runs(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("the ledger's runs after the refused continuation: %d %v", len(rows), err)
	}
}

// The task.md of a comparison's attempt, launched from its ticket, tells
// it to say what matters with task_blocked or in its result's summary,
// and names no comment anywhere; an ordinary run launched from the same
// ticket offers a comment on the ticket. (The test's name, which its
// temporary folders and so the worktree's path carry, says no "comment".)
func TestAComparisonsTaskFileSaysBlockedOrTheSummary(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	e, _ := sqlEngine(t)
	e.compareOpen = true
	run := launchPlanned(t, e, compareCrew(t, dir, 2, true), planned(t, "api-sweep-0000000a", base))
	defer stopRun(t, e, run.ID)
	md, f := taskFileOf(t, waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning))
	if f.Ticket == nil || !strings.Contains(md, "If one matters to your task, say so: `task_blocked` with why, or in your result's summary.\n\n") || strings.Contains(strings.ToLower(md), "comment") {
		t.Fatalf("a comparison's task.md:\n%s", md)
	}
	other, _ := sqlEngine(t)
	plain := launchPlanned(t, other, repoCrew(dir, work("build", "lead")), planned(t, "api-sweep-0000000b", base))
	defer stopRun(t, other, plain.ID)
	md, f = taskFileOf(t, waitAttempt(t, other, plain.ID, "build", ledger.AttemptRunning))
	if f.Ticket == nil || !strings.Contains(md, "If one matters to your task, say so: `task_blocked` with why, or a comment on the ticket.\n\n") {
		t.Fatalf("an ordinary run's task.md:\n%s", md)
	}
}

// I1, door 1: every candidate's first attempt starts at the planned base,
// in a worktree and on a branch of its own, while HEAD has moved; a
// candidate needs nothing, so none waits for another.
func TestEveryCandidateStartsAtTheBase(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	writeFile(t, dir+"/README", "moved on\n")
	commitAll(t, dir, "after the revision")
	if head(t, dir) == base {
		t.Fatal("HEAD did not move")
	}
	e, ld := sqlEngine(t)
	e.compareOpen = true
	c := compareCrew(t, dir, 3, true)
	run := launchPlanned(t, e, c, planned(t, "api-sweep-0000000a", base))
	defer stopRun(t, e, run.ID)
	for _, cd := range c.Compare.Candidates {
		ts := waitAttempt(t, e, run.ID, cd.Task, ledger.AttemptRunning)
		want := dir + "/.conductor/worktrees/" + run.ID + "/" + cd.Task + ".1"
		if ts.Attempt.N != 1 || ts.Attempt.Start != base || ts.Attempt.Worktree != want || ts.Attempt.Branch != "crew/"+run.ID+"/"+cd.Task+".1" {
			t.Fatalf("%s: %+v", cd.Task, ts.Attempt)
		}
		if got := strings.TrimSpace(runGit(t, ts.Attempt.Worktree, "rev-parse", "HEAD")); got != base {
			t.Fatalf("%s's worktree is at %s, not the base %s", cd.Task, got, base)
		}
		_, f := taskFileOf(t, ts)
		if f.Start != base || f.StartFrom != "the run's base" || len(f.Inputs) != 0 {
			t.Fatalf("%s's task file: start %s (%s), inputs %+v", cd.Task, f.Start, f.StartFrom, f.Inputs)
		}
	}
	for i := range 3 {
		if rv := taskOf(t, e, run.ID, "review-"+string(rune('a'+i))); rv.State != ledger.TaskBlocked {
			t.Fatalf("a review before its candidate's result: %+v", rv)
		}
	}
	if row, ok, err := ld.Run(t.Context(), run.ID); err != nil || !ok || row.Base != base {
		t.Fatalf("the run's row: %+v %v %v", row, ok, err)
	}
}

// I2: the comparison is the launched one. The crew changed after the
// launch changes neither the run's crew as the ledger keeps it, nor its
// attempts' task files, nor a continuation's.
func TestTheComparisonIsTheLaunchedOne(t *testing.T) {
	dir := newRepo(t)
	e, _, l := compareEngine(t)
	c := compareCrew(t, dir, 2, false)
	launchedText, launchedApproach := c.Compare.Criteria[0].Text, c.Compare.Candidates[0].Approach
	run := launchManaged(t, e, c)
	defer stopRun(t, e, run.ID)
	// The saved crew edited: its criteria and approach, through the very
	// block the launch was given.
	c.Compare.Criteria[0].Text, c.Compare.Candidates[0].Approach = "changed after the launch", "changed approach"
	kept := func(what, crewJSON string) {
		t.Helper()
		var rc Crew
		if err := json.Unmarshal([]byte(crewJSON), &rc); err != nil || rc.Compare == nil {
			t.Fatalf("%s: %v %+v", what, err, rc.Compare)
		}
		if rc.Compare.Criteria[0].Text != launchedText || rc.Compare.Candidates[0].Approach != launchedApproach {
			t.Fatalf("%s: %+v", what, rc.Compare)
		}
	}
	readFile := func(ts TaskState) {
		t.Helper()
		md, f := taskFileOf(t, ts)
		if f.Compare == nil || f.Compare.Criteria[0].Text != launchedText || f.Compare.Approach != launchedApproach ||
			!strings.Contains(md, launchedText) || strings.Contains(md, "changed") {
			t.Fatalf("the task file: %+v\n%s", f.Compare, md)
		}
	}
	a := waitAttempt(t, e, run.ID, "approach-a", ledger.AttemptRunning)
	waitAttempt(t, e, run.ID, "approach-b", ledger.AttemptRunning)
	readFile(a)
	l.mu.Lock()
	row := l.runs[run.ID]
	l.mu.Unlock()
	kept("the run's row", row.Crew)

	// The server ends with both candidates at work: the continuation reads
	// the crew from the run's row, and its attempts' task files are the
	// launched comparison's.
	interrupt(l, run.ID, map[string][2]string{"approach-a/1": {ledger.AttemptInterrupted, "running: the owner stopped"}, "approach-b/1": {ledger.AttemptInterrupted, "running: the owner stopped"}})
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	e2.compareOpen = true
	next, err := e2.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stopRun(t, e2, next.ID)
	a2 := waitAttempt(t, e2, next.ID, "approach-a", ledger.AttemptRunning)
	readFile(a2)
	l.mu.Lock()
	row = l.runs[next.ID]
	l.mu.Unlock()
	kept("the continuation's row", row.Crew)
}

// I9, door 15: through act, a compare run takes pause, unpause, cancel,
// retry and accept, and refuses any other intent (request_changes and
// recheck among them) before anything is written: no command row, no
// receipt, no event. An ordinary run takes the same intents.
func TestACompareRunTakesOnlyItsListedCommands(t *testing.T) {
	ctx := context.Background()
	refusedOn := func(t *testing.T, e *Engine, l *fakeLedger, runID string, compare bool) {
		t.Helper()
		// The run writes its own events meanwhile; a command's are its own kinds.
		accepted := l.count(runID, "command.accepted")
		_, rqErr := e.RequestChanges(ctx, runID, "admin", Command{ID: "rq1"}, RepairRequest{Task: "approach-a", Attempt: 1, Commit: strings.Repeat("a", 40), Request: personsRequest("rv-0001")})
		_, ckErr := e.Recheck(ctx, runID, "admin", Command{ID: "ck1"}, RecheckRequest{Task: "approach-a", Attempt: 1, Commit: strings.Repeat("a", 40)})
		for id, err := range map[string]error{"rq1": rqErr, "ck1": ckErr} {
			_, found, lerr := l.Receipt(ctx, runID, "admin", id)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if compare {
				if !errors.Is(err, ErrNotSelected) || !errors.Is(err, ErrTaskState) || found {
					t.Fatalf("%s on a comparison: %v, receipt %v", id, err, found)
				}
			} else if !found || errors.Is(err, ErrNotSelected) {
				t.Fatalf("%s on an ordinary run: %v, receipt %v", id, err, found)
			}
		}
		if n := l.count(runID, "command.accepted"); compare && n != accepted || !compare && n != accepted+2 {
			t.Fatalf("command.accepted %d, was %d", n, accepted)
		}
		for _, kind := range []string{"command.request_changes", "command.recheck"} {
			if n := l.count(runID, kind); compare && n != 0 {
				t.Fatalf("%s written for a refused command", kind)
			}
		}
	}
	listed := func(t *testing.T, e *Engine, l *fakeLedger, runID string) {
		t.Helper()
		cmd := func(id string) Command { return Command{ID: id} }
		if rc, err := e.Pause(ctx, runID, "admin", cmd("p1"), true); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("pause: %+v %v", rc, err)
		}
		if rc, err := e.Pause(ctx, runID, "admin", cmd("p2"), false); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("unpause: %+v %v", rc, err)
		}
		waitAttempt(t, e, runID, "approach-b", ledger.AttemptRunning)
		if rc, err := e.Cancel(ctx, runID, "admin", cmd("c1"), "approach-b"); err != nil || rc.Task == nil || rc.Task.State != ledger.TaskCancelled {
			t.Fatalf("cancel: %+v %v", rc, err)
		}
		if rc, err := e.Retry(ctx, runID, "admin", cmd("r1"), "approach-b"); err != nil || rc.Task == nil || rc.Task.State == ledger.TaskCancelled {
			t.Fatalf("retry: %+v %v", rc, err)
		}
		// Accept of a task that has not failed: taken, its receipt the refusal.
		if rc, err := e.Accept(ctx, runID, "admin", cmd("a1"), "approach-a"); !errors.Is(err, ErrTaskState) || errors.Is(err, ErrNotSelected) || rc.Status != ledger.CommandFailed {
			t.Fatalf("accept: %+v %v", rc, err)
		}
		for _, id := range []string{"p1", "p2", "c1", "r1", "a1"} {
			if _, found, err := l.Receipt(ctx, runID, "admin", id); err != nil || !found {
				t.Fatalf("no receipt of %s: %v", id, err)
			}
		}
	}
	t.Run("compare", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := compareEngine(t)
		run := launchManaged(t, e, compareCrew(t, dir, 2, false))
		defer stopRun(t, e, run.ID)
		refusedOn(t, e, l, run.ID, true)
		listed(t, e, l, run.ID)
	})
	t.Run("ordinary", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		build := work("approach-a", "lead")
		build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
		other := work("approach-b", "other")
		run := launchManaged(t, e, repoCrew(dir, build, other))
		defer stopRun(t, e, run.ID)
		refusedOn(t, e, l, run.ID, false)
		listed(t, e, l, run.ID)
	})
	if !slices.Equal(compareIntents, []string{"pause", "unpause", TaskCancel, TaskRetry, TaskAccept}) {
		t.Fatalf("compareIntents: %v", compareIntents)
	}
}
