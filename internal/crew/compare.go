package crew

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// A compare crew (docs/plans/compare-crew.md, Program item CP1): an
// ordinary crew with tasks whose compare block names two to four candidate
// tasks for one objective, each starting at the run's base with the same
// instructions and the same checks, judged on the same criteria. Each
// candidate is a lane: the candidate task, the review task that reviews it
// (when there is one) and the members who do them. Lanes are a rule of the
// engine, made in one place (compareLanes) from the block and the tasks,
// and kept by the run as launched (run.lanes): a message stays in its lane
// (send), a compare run takes only the commands listed for it
// (compareIntents, in act), its attempts ask no context provider
// (askContext), and each attempt's task file names its own lane only
// (compareFor).

// Compare is a crew's compare block (Crew.Compare).
type Compare struct {
	// Criteria are what the candidates' results are compared on, 1 to
	// maxCompareCriteria, in their order.
	Criteria []Criterion `json:"criteria"`
	// Candidates are the candidate tasks, minCandidates to maxCandidates,
	// in their column order.
	Candidates []Candidate `json:"candidates"`
	// Brakeman asks Brakeman for a comparison draft once every lane is
	// done (a later row of the plan queues it).
	Brakeman bool `json:"brakeman,omitempty"`
	// Selector is whom the selection is for; empty (the operator) until
	// the plan's row that admits people's labels.
	Selector string `json:"selector,omitempty"`
}

// Criterion is one thing the candidates are compared on: an id as a
// task's, and one line of at most maxCriterionText characters.
type Criterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Candidate is one candidate of a comparison: a work task of the crew and
// the approach it is to take, at most maxApproach characters ("" for an
// independent try of the objective).
type Candidate struct {
	Task     string `json:"task"`
	Approach string `json:"approach,omitempty"`
}

// The compare block's bounds.
const (
	maxCompareCriteria = 12
	maxCriterionText   = 500 // characters
	minCandidates      = 2
	// maxCandidates is the attempts a run runs at once, so every
	// candidate's first attempt starts together.
	maxCandidates = maxActiveAttempts
	maxApproach   = 2000 // characters
)

// ErrLane refuses a message from one lane of a comparison to another:
// nothing is written. The API answers it 409 lane.
var ErrLane = errors.New("another lane of this comparison")

// ErrNotSelected refuses a command a compare run does not take
// (compareIntents) before anything is written: no command row, no
// receipt. It is a task_state refusal to the API.
var ErrNotSelected = fmt.Errorf("%w: this command is not open to a comparison's candidates yet", ErrTaskState)

// compareIntents are the commands a compare run takes, through act, the
// one path of every task and run command. Any other, whatever row or plan
// adds it, is refused (ErrNotSelected) until its rule for a comparison is
// written here.
var compareIntents = []string{"pause", "unpause", TaskCancel, TaskRetry, TaskAccept}

// errComparisonLaunch refuses the launch of a compare crew, every launch
// and continuation alike, until the launch's statement exists (the plan's
// PR-3 lifts it). prepare, which every launch and continuation calls,
// refuses it before a run exists.
var errComparisonLaunch = invalidf("comparisons arrive with their launch statement: a crew with a compare block is not launched yet")

// clone returns a copy of cmp that shares no slice with it.
func (cmp Compare) clone() Compare {
	cmp.Criteria = slices.Clone(cmp.Criteria)
	cmp.Candidates = slices.Clone(cmp.Candidates)
	return cmp
}

// compareLanes is the lane of every task of a comparison: the candidate
// task it belongs to (a candidate its own; a review the candidate it
// needs), or "" for a task in no lane. It is the one place a lane is made,
// for the validation and for the run.
func compareLanes(cmp *Compare, tasks []Task) map[string]string {
	lanes := make(map[string]string, len(tasks))
	for _, cd := range cmp.Candidates {
		lanes[cd.Task] = cd.Task
	}
	for _, t := range tasks {
		if t.Kind != TaskReview || len(t.Needs) != 1 {
			continue
		}
		if _, ok := lanes[t.ID]; ok {
			continue // a candidate; the validation refuses a review as one
		}
		if slices.ContainsFunc(cmp.Candidates, func(cd Candidate) bool { return cd.Task == t.Needs[0] }) {
			lanes[t.ID] = t.Needs[0]
		}
	}
	return lanes
}

// laneOf is the lane of the run's member m: the lane of the tasks it does
// (the validation keeps a member in one), "" in a run of no comparison.
// The caller holds e.mu or knows r is not shared yet.
func (r *run) laneOf(m *member) string {
	if r.lanes == nil {
		return ""
	}
	for _, t := range r.plan {
		if t.Member == m.def.Name {
			return r.lanes[t.ID]
		}
	}
	return ""
}

// crewRef is the crew reference of a session the run launches for its
// member with the name, at the task's attempt n (none for a member's own
// session): in a comparison's run, it carries the member's lane (laneOf),
// decided here, once, as the session is launched, so the session holds it
// from its creation (session.CrewRef.Lane). r.lanes and r.plan are set as
// the run is made and never changed, so no lock is needed.
func (r *run) crewRef(m *member, task string, n int) session.CrewRef {
	ref := session.CrewRef{RunID: r.id, CrewID: r.crewID, Member: m.def.Name, Task: task, Attempt: n}
	if r.lanes != nil {
		ref.Lane = &session.LaneMark{Candidate: r.laneOf(m)}
	}
	return ref
}

// Lane is a comparison's lane as one of its sessions works in it
// (Engine.LaneOf): the run, the session's member and its lane (the lane's
// candidate task), with the lane of every member and task of the run as
// the run holds them (laneOf, run.lanes). The API's doors ask it and
// nothing else which lane a member or a task is in.
type Lane struct {
	Run, Member, Candidate string
	cmp                    Compare
	members, tasks         map[string]string
}

// LaneOf is the lane of a comparison's session, from its crew reference
// (the lane the engine gave it at its launch, crewRef): ok is false for a
// reference that carries no lane, and for a run the engine no longer holds
// or that holds no lanes, whose lanes cannot be told now.
func (e *Engine) LaneOf(ref session.CrewRef) (Lane, bool) {
	if ref.Lane == nil {
		return Lane{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[ref.RunID]
	if !ok || r.lanes == nil {
		return Lane{}, false
	}
	l := Lane{Run: r.id, Member: ref.Member, Candidate: ref.Lane.Candidate, cmp: r.compare.clone(), members: map[string]string{}, tasks: maps.Clone(r.lanes)}
	for _, m := range r.members {
		l.members[m.def.Name] = r.laneOf(m)
	}
	l.cmp.Candidates = slices.DeleteFunc(l.cmp.Candidates, func(cd Candidate) bool { return cd.Task != l.Candidate || l.Candidate == "" })
	return l, true
}

// own reports whether lane is l's own: never for a lane of no name, which
// the validation leaves to no member.
func (l Lane) own(lane string) bool { return l.Candidate != "" && lane == l.Candidate }

// OwnMember reports whether the run's member with the name is in l's lane.
func (l Lane) OwnMember(name string) bool {
	lane, ok := l.members[name]
	return ok && l.own(lane)
}

// OwnTask reports whether the run's task with the id is in l's lane.
func (l Lane) OwnTask(id string) bool {
	lane, ok := l.tasks[id]
	return ok && l.own(lane)
}

// Compare is the run's compare block with l's own candidate alone.
func (l Lane) Compare() Compare { return l.cmp.clone() }

// SetCompareOpenForTest lets the engine launch a crew with a compare block
// (compareOpen), for the tests of another package of what a comparison's
// run answers, before the plan's row that opens the launch. It is called
// before the engine launches any run: prepare reads it without a lock.
func (e *Engine) SetCompareOpenForTest(open bool) { e.compareOpen = open }

// validate checks the compare block of c, a crew with tasks whose tasks
// have passed their own rules (checkTasks): its bounds, its candidates
// (work tasks of the crew, each once, with no needs, alike in instructions
// and checks, with something to check), its lanes (every task a candidate
// or the one review of one, every member in one lane) and its selector.
func (cmp *Compare) validate(c Crew) error {
	if n := len(cmp.Candidates); n < minCandidates || n > maxCandidates {
		return invalidf("compare: a comparison has %d to %d candidates, not %d", minCandidates, maxCandidates, n)
	}
	if n := len(cmp.Criteria); n == 0 || n > maxCompareCriteria {
		return invalidf("compare: a comparison has 1 to %d criteria, not %d", maxCompareCriteria, n)
	}
	var ids []string
	for i, cr := range cmp.Criteria {
		if !taskIDPattern.MatchString(cr.ID) {
			return invalidf("compare: criteria[%d]: id %s must match %s", i, quote(cr.ID), taskIDPattern)
		}
		if slices.Contains(ids, cr.ID) {
			return invalidf("compare: criterion %s is named twice", quote(cr.ID))
		}
		ids = append(ids, cr.ID)
		if strings.TrimSpace(cr.Text) == "" || utf8.RuneCountInString(cr.Text) > maxCriterionText {
			return invalidf("compare: criterion %s: the text is 1 to %d characters", quote(cr.ID), maxCriterionText)
		}
		if strings.ContainsFunc(cr.Text, func(r rune) bool { return unicode.IsControl(r) || r == ' ' || r == ' ' }) {
			return invalidf("compare: criterion %s: the text is one line, with no control character", quote(cr.ID))
		}
	}
	byID := map[string]Task{}
	for _, t := range c.Tasks {
		byID[t.ID] = t
	}
	var first Task
	for i, cd := range cmp.Candidates {
		t, ok := byID[cd.Task]
		switch {
		case !ok:
			return invalidf("compare: candidate %s is not a task of this crew", quote(cd.Task))
		case slices.ContainsFunc(cmp.Candidates[:i], func(x Candidate) bool { return x.Task == cd.Task }):
			return invalidf("compare: task %s is a candidate twice", quote(cd.Task))
		case t.Kind != TaskWork:
			return invalidf("compare: task %s is a review: a candidate is a work task", quote(cd.Task))
		case len(t.Needs) > 0:
			return invalidf("compare: task %s needs %s: a candidate starts at the run's base and needs nothing", quote(cd.Task), quote(t.Needs[0]))
		case utf8.RuneCountInString(cd.Approach) > maxApproach:
			return invalidf("compare: candidate %s: the approach is at most %d characters", quote(cd.Task), maxApproach)
		case strings.ContainsRune(cd.Approach, 0):
			return invalidf("compare: candidate %s: the approach contains NUL", quote(cd.Task))
		}
		if i == 0 {
			first = t
			continue
		}
		if t.Instructions != first.Instructions {
			return invalidf("compare: candidates %s and %s have different instructions: a comparison gives every candidate the same", quote(first.ID), quote(t.ID))
		}
		if !slices.EqualFunc(t.Checks, first.Checks, func(a, b Check) bool {
			return a.ID == b.ID && slices.Equal(a.Argv, b.Argv) && a.TimeoutSeconds == b.TimeoutSeconds
		}) {
			return invalidf("compare: candidates %s and %s have different checks: a comparison checks every candidate alike", quote(first.ID), quote(t.ID))
		}
	}
	// Something to compare: the candidates' checks, or the project
	// manifest's verify steps. A file-form manifest's steps are read at
	// the launch's base, where the launch requires them.
	if len(first.Checks) == 0 {
		switch {
		case !c.hasManifest():
			return invalidf("compare: nothing to compare: the candidates have no checks and the crew no project manifest")
		case !c.Manifest.File && len(c.Manifest.Verify) == 0:
			return invalidf("compare: nothing to compare: the candidates have no checks and the project manifest no verify step")
		}
	}
	lanes := compareLanes(cmp, c.Tasks)
	for _, t := range c.Tasks {
		if _, ok := lanes[t.ID]; ok {
			continue
		}
		var cands []string
		for _, n := range t.Needs {
			if lanes[n] == n {
				cands = append(cands, n)
			}
		}
		if len(cands) > 1 {
			return invalidf("compare: task %s needs %s and %s: a comparison never builds on two candidates", quote(t.ID), quote(cands[0]), quote(cands[1]))
		}
		return invalidf("compare: task %s is in no lane: every task of a comparison is a candidate or the review of one", quote(t.ID))
	}
	reviewOf := map[string]string{}
	for _, t := range c.Tasks {
		if t.Kind != TaskReview {
			continue
		}
		if other, ok := reviewOf[lanes[t.ID]]; ok {
			return invalidf("compare: tasks %s and %s both review %s: a candidate has at most one review", quote(other), quote(t.ID), quote(lanes[t.ID]))
		}
		reviewOf[lanes[t.ID]] = t.ID
	}
	laneOf := map[string]string{}
	for _, t := range c.Tasks {
		lane := lanes[t.ID]
		if was, ok := laneOf[t.Member]; ok && was != lane {
			return invalidf("compare: member %s does tasks in two lanes (%s and %s): each member works in one lane", quote(t.Member), quote(was), quote(lane))
		}
		laneOf[t.Member] = lane
	}
	if cmp.Selector != "" {
		return invalidf("compare: selector is not taken yet: a comparison's selection is the operator's")
	}
	return nil
}

// TaskCompare is the comparison part of an attempt's task file
// (task.json's compare): its own lane's candidate (the task itself, or
// the one a review reviews), how many candidates there are, the criteria
// and its candidate's approach. It names no other lane's task, member,
// branch, path or approach. Never cut.
type TaskCompare struct {
	Candidate string      `json:"candidate"`
	Of        int         `json:"of"`
	Criteria  []Criterion `json:"criteria"`
	Approach  string      `json:"approach,omitempty"`
	// base is the run's base, which task.md names.
	base string
}

// compareFor is the comparison part of the task file of task id in a
// comparison (nil without one): the one place a lane's view of its
// comparison is made, for the engine's task files and the measure of a
// repair's (worstRepairFile).
func compareFor(cmp *Compare, tasks []Task, id, base string) *TaskCompare {
	if cmp == nil {
		return nil
	}
	lane := compareLanes(cmp, tasks)[id]
	i := slices.IndexFunc(cmp.Candidates, func(cd Candidate) bool { return cd.Task == lane })
	if i < 0 {
		return nil
	}
	return &TaskCompare{Candidate: lane, Of: len(cmp.Candidates), Criteria: slices.Clone(cmp.Criteria), Approach: cmp.Candidates[i].Approach, base: base}
}

// compareTextBytes is how many bytes the comparison part's own text takes
// in task.json (taskTextBytes), counted with the goal, the role and the
// instructions in maxTaskText.
func compareTextBytes(tc *TaskCompare) int {
	if tc == nil {
		return 0
	}
	n := taskTextBytes(tc.Candidate, tc.Approach)
	for _, cr := range tc.Criteria {
		n += taskTextBytes(cr.ID, cr.Text)
	}
	return n
}

// writeCompare writes task.md's comparison section: whose lane this is,
// that it works alone, its approach and the criteria. Nothing for a task
// of no comparison.
func writeCompare(b *strings.Builder, f TaskFile) {
	tc := f.Compare
	if tc == nil {
		return
	}
	b.WriteString("## A comparison\n\n")
	if f.Kind == TaskReview {
		fmt.Fprintf(b, "You review %s, one of %d candidates for this objective, each of which started at %s and worked alone. Judge its result against the objective and the criteria below and change nothing. Do not read, fetch or merge another candidate's branch or worktree: you review this one alone.\n\n", tc.Candidate, tc.Of, short(tc.base))
	} else {
		fmt.Fprintf(b, "You are %s, one of %d candidates for this objective, each starting at %s and working alone. Do not read, fetch or merge another candidate's branch or worktree; nothing of theirs is an input of yours.\n\n", tc.Candidate, tc.Of, short(tc.base))
	}
	approach := strings.TrimSpace(tc.Approach)
	if approach == "" {
		approach = "none given: an independent try of the objective."
	}
	if f.Kind == TaskReview {
		fmt.Fprintf(b, "Its approach: %s\n\n", approach)
		b.WriteString("The criteria it is compared on:\n\n")
	} else {
		fmt.Fprintf(b, "Your approach: %s\n\n", approach)
		b.WriteString("The criteria your result is compared on:\n\n")
	}
	for _, cr := range tc.Criteria {
		fmt.Fprintf(b, "- %s: %s\n", cr.ID, cr.Text)
	}
	b.WriteString("\n")
}

// noDerivedContext is task.md's context section for an attempt of a
// comparison, whose attempts ask no context provider (askContext).
const noDerivedContext = "## Context\n\nNo derived context: a comparison's candidates and reviews read their task, the base and their own work.\n\n"

// CompareSpec is what the compare template makes a crew of
// (CompareTemplate): the crew's name, working directory and goal (the
// objective), the instructions every candidate gets, the criteria, the
// checks every candidate's result runs (or a project manifest's verify
// steps), the candidates (each an agent, its arguments and an optional
// approach), whether each candidate gets an agent's review and by which
// agent, whether Brakeman drafts the comparison, and the yolo choice.
type CompareSpec struct {
	Name         string          `json:"name"`
	Cwd          string          `json:"cwd"`
	Goal         string          `json:"goal"`
	Instructions string          `json:"instructions"`
	Criteria     []Criterion     `json:"criteria"`
	Checks       []Check         `json:"checks,omitempty"`
	Manifest     *Manifest       `json:"manifest,omitempty"`
	Candidates   []CandidateSpec `json:"candidates"`
	Reviews      bool            `json:"reviews,omitempty"`
	ReviewAgent  string          `json:"reviewAgent,omitempty"`
	Brakeman     bool            `json:"brakeman,omitempty"`
	Yolo         *bool           `json:"yolo,omitempty"`
}

// CandidateSpec is one candidate of a CompareSpec: its agent, the
// agent's arguments and the approach it is to take.
type CandidateSpec struct {
	AgentID  string   `json:"agentId"`
	Args     []string `json:"args,omitempty"`
	Approach string   `json:"approach,omitempty"`
}

// CompareTemplate makes the crew of a comparison from spec: members
// cand-a to cand-d (and rev-a to rev-d with reviews), tasks approach-a to
// approach-d (work, the shared instructions and checks, no needs) and
// review-a to review-d (each reviewing its candidate and changing
// nothing), each member's role the template's, isolation worktree, on the
// server, the compare block filled. The crew is held to ValidateSave, so
// the spec's bounds are the crew's; the error matches ErrInvalid.
func CompareTemplate(spec CompareSpec) (Crew, error) {
	n := len(spec.Candidates)
	if n < minCandidates || n > maxCandidates {
		return Crew{}, invalidf("compare: a comparison has %d to %d candidates, not %d", minCandidates, maxCandidates, n)
	}
	if spec.Reviews != (spec.ReviewAgent != "") {
		return Crew{}, invalidf("compare: reviews need a reviewAgent, and a reviewAgent is only set with reviews")
	}
	c := Crew{Name: spec.Name, Goal: spec.Goal, Cwd: spec.Cwd, Where: WhereServer, Isolation: IsolationWorktree,
		Compare: &Compare{Criteria: slices.Clone(spec.Criteria), Brakeman: spec.Brakeman}}
	if spec.Yolo != nil {
		yolo := *spec.Yolo
		c.Yolo = &yolo
	}
	if spec.Manifest != nil {
		m := spec.Manifest.clone()
		c.Manifest = &m
	}
	byTask := Start{When: StartTask}
	var reviewers []Member
	var reviews []Task
	for i, cs := range spec.Candidates {
		letter := string(rune('a' + i))
		upper := strings.ToUpper(letter)
		task := "approach-" + letter
		c.Members = append(c.Members, Member{Name: "cand-" + letter, AgentID: cs.AgentID, Args: slices.Clone(cs.Args), Start: byTask,
			Prompt: fmt.Sprintf("You are candidate %s of %d for this objective. You work alone, from the run's base, in a worktree of your own: another candidate's work is never an input of yours.", upper, n)})
		c.Tasks = append(c.Tasks, Task{ID: task, Member: "cand-" + letter, Kind: TaskWork, Instructions: spec.Instructions, Checks: cloneChecks(spec.Checks)})
		c.Compare.Candidates = append(c.Compare.Candidates, Candidate{Task: task, Approach: cs.Approach})
		if spec.Reviews {
			reviewers = append(reviewers, Member{Name: "rev-" + letter, AgentID: spec.ReviewAgent, Start: byTask,
				Prompt: fmt.Sprintf("You review candidate %s of %d: you judge its result against the objective and the criteria, and change nothing.", upper, n)})
			reviews = append(reviews, Task{ID: "review-" + letter, Member: "rev-" + letter, Kind: TaskReview, Needs: []string{task},
				Instructions: "Review this candidate's result at HEAD against the objective and each criterion your task file lists; run what you need to judge it, and change nothing. Approve when it meets the objective and the criteria; otherwise ask for changes, with a finding for each criterion it misses and each problem, naming its file and line."})
		}
	}
	c.Members = append(c.Members, reviewers...)
	c.Tasks = append(c.Tasks, reviews...)
	if err := c.ValidateSave(); err != nil {
		return Crew{}, err
	}
	return c, nil
}
