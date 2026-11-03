// Package crew holds saved crews: named teams of agents, each member with a
// role prompt, extra arguments and a start condition, that a run launches
// together. Crews persist one file each, crews/<id>.json in the data
// directory (store.go).
package crew

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// When a member starts in a run (Start.When).
const (
	StartImmediately = "immediately"
	StartAfter       = "after" // once the member Start.Member names first reports done
	StartManual      = "manual"
	// StartTask is the start of a managed crew's members (Crew.Tasks): the
	// run starts a member for each of its tasks as the task's needs are
	// met, never by a rule of its own. Required in a managed crew, refused
	// elsewhere.
	StartTask = "task"
)

// What a task is for (Task.Kind).
const (
	TaskWork   = "work"   // the member works and submits a result, a commit
	TaskReview = "review" // the member reviews one work task's result and submits a verdict
)

// Where a crew's members run (Crew.Where).
const (
	WhereServer = "server"
	WhereHost   = "host"
)

// How members share the working directory (Crew.Isolation).
const (
	IsolationNone     = "none"     // every member works in Cwd
	IsolationWorktree = "worktree" // every member gets a git worktree of Cwd
)

// ErrInvalid is matched (errors.Is) by every error that says a crew breaks a
// rule: from Validate, CheckAgents and the store's checks. The API answers it
// with 400 invalid_crew.
var ErrInvalid = errors.New("invalid crew")

// invalidError is a rule a crew breaks. Its message is the problem alone.
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }
func (e *invalidError) Unwrap() error { return ErrInvalid }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

// maxQuoted is how many characters of a value an error message quotes.
const maxQuoted = 80

// quote quotes a value for an error message as %q does, cut to maxQuoted
// characters with "…" where it was cut: whatever a client sends, an error
// never sends it back whole.
func quote(s string) string {
	n := 0
	for i := range s {
		if n == maxQuoted {
			return strconv.Quote(s[:i] + "…")
		}
		n++
	}
	return strconv.Quote(s)
}

// Start says when a member starts in a run.
type Start struct {
	When   string `json:"when"`             // immediately | after | manual
	Member string `json:"member,omitempty"` // with when=after: the member to wait for
}

// Member is one agent of a crew.
type Member struct {
	// Name identifies the member in its crew. It becomes a branch name and a
	// worktree path, so it is held to memberNamePattern.
	Name    string   `json:"name"`
	AgentID string   `json:"agentId"`
	Prompt  string   `json:"prompt"` // typed once the member is ready; see ExpandPrompt
	Args    []string `json:"args,omitempty"`
	Start   Start    `json:"start"`
}

// Task is a unit of a managed crew's work: a member does it once the tasks
// it needs have results, in a worktree of its own made at those results,
// and submits a result the checks decide. A review task reviews one work
// task's result and submits a verdict.
type Task struct {
	ID           string   `json:"id"`     // taskIDPattern; the attempt's branch is crew/<run>/<task>.<n>
	Member       string   `json:"member"` // the member who does it
	Kind         string   `json:"kind"`   // work | review
	Instructions string   `json:"instructions"`
	Needs        []string `json:"needs,omitempty"`  // the tasks whose results it starts from; a review's one work task
	Checks       []Check  `json:"checks,omitempty"` // run against the result; none for a review
}

// Check is a command the controller runs: against a result (Task.Checks),
// or a step of a project manifest (Manifest, whose setup phase Crew.Setup was).
// An argv array, never a shell line.
type Check struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"` // 1 to maxCheckTimeout; defaultCheckTimeout when 0
}

// Crew is a saved team of agents. The server sets ID, CreatedAt and UpdatedAt.
type Crew struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Goal               string `json:"goal"`
	Cwd                string `json:"cwd"`
	Where              string `json:"where"`     // server | host
	Isolation          string `json:"isolation"` // none | worktree
	OpenAfterLaunch    bool   `json:"openAfterLaunch"`
	ViewLinkTTLSeconds int64  `json:"viewLinkTtlSeconds,omitempty"` // a launch creates a view link that lasts this long; none when 0
	// Tasks makes the crew managed (docs/plans/agent-harness-order.md): who
	// does what, after which results, checked how. A crew without tasks
	// runs by its members' start rules, as before.
	Tasks []Task `json:"tasks,omitempty"`
	// Setup is run in every new attempt worktree before its member starts,
	// each entry an argv array, never a shell line: what a fresh worktree
	// lacks (a dependency install). Only with Tasks. It is the project
	// manifest's setup phase as a crew saved before manifests holds it
	// (effectiveManifest), and is never set beside Manifest.
	Setup []Check `json:"setup,omitempty"`
	// Manifest is the crew's project manifest (manifest.go): what makes a fresh
	// tree usable, how it is checked and cleaned up. Only with Tasks.
	Manifest *Manifest `json:"manifest,omitempty"`
	// ManifestRevision is the server's count of the crew's manifest changes
	// (manifestRevision), set by the store whatever a save carries: on the
	// crew, not inside the optional Manifest, so a save that removes the
	// manifest keeps it and a number names one manifest of the crew for good.
	ManifestRevision int64 `json:"manifestRevision,omitempty"`
	// Yolo overrides the server's yolo default for the crew's runs: nil
	// follows it, false launches every member without its agent's yolo
	// recipe, true with it. A launch fixes the choice on the run.
	Yolo      *bool     `json:"yolo,omitempty"`
	Members   []Member  `json:"members"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Origin is the Brakeman draft a person made this crew from
	// ("brakeman:<request>/<item>/<op>", Store.CreateFrom), "" for every
	// other crew. Only CreateFrom sets it, Update keeps the stored one
	// whatever its body holds, and Create and Duplicate clear it, so at
	// most one crew answers a draft's make.
	Origin string `json:"origin,omitempty"`
}

// Limits enforced by Validate. There is no limit on the number of crews.
const (
	maxName      = 60   // characters (runes) in a crew name
	maxGoal      = 2000 // characters (runes)
	maxCwd       = 4096 // bytes
	maxMembers   = 12
	maxPrompt    = 4000    // characters (runes)
	maxArgs      = 32      // entries in a member's args
	maxArg       = 4096    // bytes in one entry
	maxArgsBytes = 8 << 10 // bytes in all of a member's args
	// maxLinkTTL bounds ViewLinkTTLSeconds as POST /api/sessions/{id}/links
	// bounds ttlSeconds.
	maxLinkTTL = 365 * 24 * 3600
	// The tasks of a managed crew.
	maxTasks        = 128
	maxInstructions = 8000 // characters (runes)
	// maxTaskText bounds a task's goal, role and instructions together as
	// its task.json keeps them (taskTextBytes): the part of a task file
	// never cut, within maxTaskFile with room for the rest.
	maxTaskText         = 40 << 10
	maxChecks           = 8    // per task, and setup entries per crew
	maxCheckTimeout     = 3600 // seconds
	defaultCheckTimeout = 600
)

// taskIDPattern keeps task and check ids short, lower case and free of
// dots and path separators: a task id becomes part of a branch name,
// crew/<run>/<task>.<n>, where the dot tells the attempt apart.
var taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidTaskID reports whether id is shaped as a task id.
func ValidTaskID(id string) bool { return taskIDPattern.MatchString(id) }

// MaxEncoded bounds a crew: its file, as the store writes it (store.Encode).
// Validate holds every crew to it, so the crew a create or an update sends is
// held to it too; the request body itself may be larger (the API takes up to
// twice this, since a client may indent the crew). It is a sanity bound, not
// a quota: the limits above keep a valid crew well under it, and there is no
// limit on the number of crews.
const MaxEncoded = 1 << 20

// maxEncoded is the bound Validate holds a crew to, MaxEncoded but for tests,
// which lower it to reach it.
var maxEncoded = MaxEncoded

// memberNamePattern keeps member names short, lower case and free of path
// separators: they become branch names and worktree paths. gitRefuses adds
// what git refuses in a branch name that the pattern lets through.
var memberNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// gitRefuses reports whether git check-ref-format refuses a name that matches
// memberNamePattern as a branch name.
func gitRefuses(name string) bool {
	return strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock")
}

// Validate checks c apart from its ID, which the store checks, and its agents,
// which CheckAgents checks against a catalog: the name, goal, working
// directory and options, the members' names, agent IDs, prompts (with the
// goal in them, as they are typed), arguments and start conditions, all
// within their limits, and the size of c as its file (MaxEncoded), which
// counts the ID and the times. The first problem found is returned; it
// matches ErrInvalid, and quotes at most 80 characters of a value.
// ValidateSave is Validate with the rules a crew is held to as it is
// saved, not as one saved before is read or a run of it continued (a crew
// valid when it was saved stays readable and its runs continuable): no
// task needs another twice, and a task's goal, role and instructions take
// at most maxTaskText as its task.json keeps them, the part of a task file
// never cut (fitTaskFile).
func (c Crew) ValidateSave() error {
	if err := c.Validate(); err != nil {
		return err
	}
	for _, t := range c.Tasks {
		for i, n := range t.Needs {
			if slices.Contains(t.Needs[:i], n) {
				return invalidf("task %s: needs %s twice", quote(t.ID), quote(n))
			}
		}
		prompt := ""
		for _, m := range c.Members {
			if m.Name == t.Member {
				prompt = m.Prompt
			}
		}
		if n := taskTextBytes(c.Goal, prompt, t.Instructions); n > maxTaskText {
			return invalidf("task %s: its goal, role and instructions are %d bytes as its task file keeps them, more than %d", quote(t.ID), n, maxTaskText)
		}
	}
	return nil
}

func (c Crew) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalidf("name must not be empty")
	}
	if err := nameError("name", c.Name); err != nil {
		return err
	}
	if utf8.RuneCountInString(c.Goal) > maxGoal {
		return invalidf("goal must be at most %d characters", maxGoal)
	}
	if len(c.Cwd) > maxCwd {
		return invalidf("cwd must be at most %d bytes", maxCwd)
	}
	if strings.ContainsRune(c.Cwd, 0) {
		return invalidf("cwd contains NUL")
	}
	if c.Where != WhereServer && c.Where != WhereHost {
		return invalidf("where %s must be %q or %q", quote(c.Where), WhereServer, WhereHost)
	}
	if c.Isolation != IsolationNone && c.Isolation != IsolationWorktree {
		return invalidf("isolation %s must be %q or %q", quote(c.Isolation), IsolationNone, IsolationWorktree)
	}
	if c.ViewLinkTTLSeconds < 0 || c.ViewLinkTTLSeconds > maxLinkTTL {
		return invalidf("viewLinkTtlSeconds must be between 0 and %d", maxLinkTTL)
	}
	if c.ManifestRevision < 0 {
		return invalidf("manifestRevision must not be negative")
	}
	if len(c.Members) > maxMembers {
		return invalidf("too many members (at most %d)", maxMembers)
	}
	names := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		if err := m.Validate(); err != nil {
			return err
		}
		if err := m.checkTypedPrompt(c.Goal); err != nil {
			return err
		}
		if slices.Contains(names, m.Name) {
			return invalidf("member %s: the name is used twice", quote(m.Name))
		}
		names = append(names, m.Name)
	}
	if err := c.checkStarts(names); err != nil {
		return err
	}
	if err := c.checkTasks(names); err != nil {
		return err
	}
	b, err := store.Encode(c)
	if err != nil {
		return invalidf("cannot be written as JSON: %v", err)
	}
	if len(b) > maxEncoded {
		return invalidf("the crew is %d bytes as its file, more than %d", len(b), maxEncoded)
	}
	return nil
}

// checkStarts checks the after conditions against the member names: each
// names another member, and following them from any member ends at one that
// does not start after another, so every member can start.
func (c Crew) checkStarts(names []string) error {
	after := make(map[string]string, len(c.Members)) // member -> the member it starts after
	for _, m := range c.Members {
		if m.Start.When != StartAfter {
			continue
		}
		if m.Start.Member == m.Name {
			return invalidf("member %s: cannot start after itself", quote(m.Name))
		}
		if !slices.Contains(names, m.Start.Member) {
			return invalidf("member %s: starts after %s, which is not a member of this crew", quote(m.Name), quote(m.Start.Member))
		}
		after[m.Name] = m.Start.Member
	}
	// A chain visits each member at most once before it ends, so at most
	// len(c.Members) steps: a name seen twice is a cycle.
	for _, m := range c.Members {
		chain := []string{m.Name}
		for next, ok := after[m.Name]; ok; next, ok = after[next] {
			seen := slices.Contains(chain, next)
			chain = append(chain, next)
			if seen {
				return invalidf("member %s: the start conditions go round in a cycle, so it never starts: %s", quote(m.Name), strings.Join(chain, " after "))
			}
		}
	}
	return nil
}

// checkTasks checks the tasks of a managed crew against its members, and
// that a crew without tasks has no task-started member, no setup and no
// project manifest: ids well formed and unique, at most maxTasks, every
// task's member known and its needs known with no cycle, a review needing
// one work task and having no checks, every member with a task, the checks
// and the setup within their bounds, isolation worktree, every member
// started by its tasks; then the project manifest, never beside a setup, by
// its own rules (Manifest.validate).
func (c Crew) checkTasks(names []string) error {
	if len(c.Tasks) == 0 {
		if len(c.Setup) > 0 {
			return invalidf("setup is only set with tasks")
		}
		for _, m := range c.Members {
			if m.Start.When == StartTask {
				return invalidf("member %s: start.when %q is only for a crew with tasks", quote(m.Name), StartTask)
			}
		}
		if c.hasManifest() {
			return invalidf("a project manifest is only set with tasks")
		}
		return nil
	}
	if len(c.Tasks) > maxTasks {
		return invalidf("too many tasks (at most %d)", maxTasks)
	}
	if c.Isolation != IsolationWorktree {
		return invalidf("a crew with tasks needs isolation %q: each attempt works in a worktree of its own", IsolationWorktree)
	}
	for _, m := range c.Members {
		if m.Start.When != StartTask {
			return invalidf("member %s: in a crew with tasks, start.when must be %q", quote(m.Name), StartTask)
		}
	}
	if len(c.Setup) > maxChecks {
		return invalidf("too many setup commands (at most %d)", maxChecks)
	}
	setupIDs := make([]string, 0, len(c.Setup))
	for i, ch := range c.Setup {
		if err := ch.validate(fmt.Sprintf("setup[%d]", i)); err != nil {
			return err
		}
		if slices.Contains(setupIDs, ch.ID) {
			return invalidf("setup[%d]: id %s is used twice", i, quote(ch.ID))
		}
		setupIDs = append(setupIDs, ch.ID)
	}
	ids := make([]string, 0, len(c.Tasks))
	kinds := map[string]string{}
	for _, t := range c.Tasks {
		if !taskIDPattern.MatchString(t.ID) {
			return invalidf("task %s: id must match %s", quote(t.ID), taskIDPattern)
		}
		if slices.Contains(ids, t.ID) {
			return invalidf("task %s: the id is used twice", quote(t.ID))
		}
		ids = append(ids, t.ID)
		kinds[t.ID] = t.Kind
		if t.Kind != TaskWork && t.Kind != TaskReview {
			return invalidf("task %s: kind %s must be %q or %q", quote(t.ID), quote(t.Kind), TaskWork, TaskReview)
		}
		if !slices.Contains(names, t.Member) {
			return invalidf("task %s: member %s is not a member of this crew", quote(t.ID), quote(t.Member))
		}
		if utf8.RuneCountInString(t.Instructions) > maxInstructions {
			return invalidf("task %s: instructions must be at most %d characters", quote(t.ID), maxInstructions)
		}
		if strings.ContainsRune(t.Instructions, 0) {
			return invalidf("task %s: instructions contain NUL", quote(t.ID))
		}

		if len(t.Checks) > maxChecks {
			return invalidf("task %s: too many checks (at most %d)", quote(t.ID), maxChecks)
		}
		if t.Kind == TaskReview && len(t.Checks) > 0 {
			return invalidf("task %s: a review has no checks: its verdict is its result", quote(t.ID))
		}
		checkIDs := make([]string, 0, len(t.Checks))
		for i, ch := range t.Checks {
			if err := ch.validate(fmt.Sprintf("task %s: checks[%d]", quote(t.ID), i)); err != nil {
				return err
			}
			if slices.Contains(checkIDs, ch.ID) {
				return invalidf("task %s: check id %s is used twice", quote(t.ID), quote(ch.ID))
			}
			checkIDs = append(checkIDs, ch.ID)
		}
	}
	for _, t := range c.Tasks {
		for _, n := range t.Needs {
			if n == t.ID {
				return invalidf("task %s: cannot need itself", quote(t.ID))
			}
			if !slices.Contains(ids, n) {
				return invalidf("task %s: needs %s, which is not a task of this crew", quote(t.ID), quote(n))
			}
		}
		if t.Kind == TaskReview {
			if len(t.Needs) != 1 || kinds[t.Needs[0]] != TaskWork {
				return invalidf("task %s: a review needs exactly one work task, the one it reviews", quote(t.ID))
			}
		}
	}
	// Every path from a task through its needs ends: a depth-first walk
	// that finds a task it is still visiting has a cycle, and a task it has
	// finished with is not walked again, so a dense graph costs its edges,
	// never its paths.
	needs := map[string][]string{}
	for _, t := range c.Tasks {
		needs[t.ID] = t.Needs
	}
	const visiting, done = 1, 2
	state := map[string]int{}
	var walk func(id string, path []string) error
	walk = func(id string, path []string) error {
		switch state[id] {
		case done:
			return nil
		case visiting:
			cycle := append(slices.Clone(path[slices.Index(path, id):]), id)
			return invalidf("task %s: the needs go round in a cycle, so it never starts: %s", quote(cycle[0]), strings.Join(cycle, " needs "))
		}
		state[id] = visiting
		path = append(path, id)
		for _, n := range needs[id] {
			if err := walk(n, path); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, t := range c.Tasks {
		if err := walk(t.ID, nil); err != nil {
			return err
		}
	}
	for _, m := range c.Members {
		if !slices.ContainsFunc(c.Tasks, func(t Task) bool { return t.Member == m.Name }) {
			return invalidf("member %s: has no task; every member of a crew with tasks does one", quote(m.Name))
		}
	}
	if c.hasManifest() {
		if len(c.Setup) > 0 {
			return invalidf("setup is the project manifest's setup phase: put it there")
		}
		if err := c.Manifest.validate("project manifest", taskChecks(c.Tasks), false); err != nil {
			return err
		}
	}
	return nil
}

// validate checks a check, a setup command or a manifest's step: its id,
// argv (validateArgv) and timeout. what names it in the error.
func (ch Check) validate(what string) error {
	if !taskIDPattern.MatchString(ch.ID) {
		return invalidf("%s: id %s must match %s", what, quote(ch.ID), taskIDPattern)
	}
	if err := validateArgv(what, ch.Argv); err != nil {
		return err
	}
	if ch.TimeoutSeconds < 0 || ch.TimeoutSeconds > maxCheckTimeout {
		return invalidf("%s: timeoutSeconds must be between 1 and %d (%d when left out)", what, maxCheckTimeout, defaultCheckTimeout)
	}
	return nil
}

// validateArgv checks the argv of a check or a probe: within the
// member-args bounds and never empty. what names it in the error.
func validateArgv(what string, argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return invalidf("%s: argv must name a program", what)
	}
	if len(argv) > maxArgs {
		return invalidf("%s: too many argv entries (at most %d)", what, maxArgs)
	}
	total := 0
	for i, a := range argv {
		if strings.ContainsRune(a, 0) {
			return invalidf("%s: argv[%d] contains NUL", what, i)
		}
		if len(a) > maxArg {
			return invalidf("%s: argv[%d] is longer than %d bytes", what, i, maxArg)
		}
		total += len(a)
	}
	if total > maxArgsBytes {
		return invalidf("%s: argv must be at most %d bytes in all", what, maxArgsBytes)
	}
	return nil
}

// Managed reports whether c is a managed crew: one with tasks.
func (c Crew) Managed() bool { return len(c.Tasks) > 0 }

// Validate checks what m can be checked for alone: its name, agent ID,
// prompt, arguments and start condition, within their limits. Crew.Validate
// checks it against the other members, and a run against its members when m
// joins it. The error matches ErrInvalid.
func (m Member) Validate() error {
	if !memberNamePattern.MatchString(m.Name) {
		return invalidf("member %s: name must match %s", quote(m.Name), memberNamePattern)
	}
	if gitRefuses(m.Name) {
		return invalidf(`member %s: git refuses the name for a branch: no "..", and no "." or ".lock" at the end`, quote(m.Name))
	}
	if !catalog.ValidID(m.AgentID) {
		return invalidf("member %s: agentId %s is not an agent id (a-z, 0-9 and -, 1 to 32 of them)", quote(m.Name), quote(m.AgentID))
	}
	if utf8.RuneCountInString(m.Prompt) > maxPrompt {
		return invalidf("member %s: prompt must be at most %d characters", quote(m.Name), maxPrompt)
	}
	if len(m.Args) > maxArgs {
		return invalidf("member %s: too many args (at most %d)", quote(m.Name), maxArgs)
	}
	total := 0
	for i, a := range m.Args {
		if strings.ContainsRune(a, 0) {
			return invalidf("member %s: args[%d] contains NUL", quote(m.Name), i)
		}
		if len(a) > maxArg {
			return invalidf("member %s: args[%d] is longer than %d bytes", quote(m.Name), i, maxArg)
		}
		total += len(a)
	}
	if total > maxArgsBytes {
		return invalidf("member %s: args must be at most %d bytes in all", quote(m.Name), maxArgsBytes)
	}
	switch m.Start.When {
	case StartAfter:
		if m.Start.Member == "" {
			return invalidf("member %s: start.member must name the member to start after", quote(m.Name))
		}
	case StartImmediately, StartManual, StartTask:
		if m.Start.Member != "" {
			return invalidf("member %s: start.member is only set with start.when %q", quote(m.Name), StartAfter)
		}
	default:
		return invalidf("member %s: start.when %s must be %q, %q, %q or %q", quote(m.Name), quote(m.Start.When), StartImmediately, StartAfter, StartManual, StartTask)
	}
	return nil
}

// launchable is Launchable with managed crews allowed when managed says
// so: the engine, which dispatches their tasks (#334, row 5), allows them;
// the API does not until an agent can read its task and submit its
// Launchable reports why the crew cannot be launched as a run: no members,
// or a crew that runs on a host. A crew with tasks launches as a managed
// run (#334): the API checks the server's switch and ledger before it.
func (c Crew) Launchable() error {
	if len(c.Members) == 0 {
		return invalidf("the crew has no members")
	}
	if c.Where != WhereServer {
		return invalidf("the crew runs on a host: only a crew that runs on the server can be launched")
	}
	return nil
}

// CheckAgents reports the first member whose agent cat does not list. It is
// apart from Validate because the catalog changes: the API checks it when a
// crew is saved or duplicated, and a launch checks it again. The error
// matches ErrInvalid.
func (c Crew) CheckAgents(cat catalog.Catalog) error {
	for _, m := range c.Members {
		if _, ok := cat.Get(m.AgentID); !ok {
			return invalidf("member %s: unknown agent %s", quote(m.Name), quote(m.AgentID))
		}
	}
	return nil
}

// goalRef matches $GOAL and ${GOAL}. $GOAL must end where a shell variable
// name would, so $GOALS and $GOAL_2 are left alone.
var goalRef = regexp.MustCompile(`\$(?:\{GOAL\}|GOAL\b)`)

// ExpandPrompt replaces $GOAL and ${GOAL} in a role prompt with the crew's
// goal. The goal goes in literally: nothing in it is expanded.
func ExpandPrompt(prompt, goal string) string {
	return goalRef.ReplaceAllLiteralString(prompt, goal)
}

// typedPrompt is the line a member's prompt types, less its carriage return:
// the prompt with the goal in place of $GOAL (ExpandPrompt), made one line as
// a handoff's message and a broadcast are, each line break, carriage return
// and tab a space, so that an agent that submits at a line break takes the
// whole prompt. The crew keeps the prompt as written.
func typedPrompt(prompt, goal string) string {
	return oneLine.Replace(ExpandPrompt(prompt, goal))
}

// typedPromptLen is len(typedPrompt(prompt, goal)) + 1, the bytes typing the
// prompt writes with its carriage return, counted without expanding it: a
// goal of 2000 characters in 800 references would take megabytes. Making it
// one line changes no length, so the count is exact.
func typedPromptLen(prompt, goal string) int {
	n := len(prompt) + 1
	for _, ref := range goalRef.FindAllStringIndex(prompt, -1) {
		n += len(goal) - (ref[1] - ref[0])
	}
	return n
}

// checkTypedPrompt checks that m's prompt, with goal in place of $GOAL, made
// one line (typedPrompt), fits what a session submits at once
// (session.MaxSubmitText, the paste markers around it in one INPUT frame): a
// member never fails to start for the size of its prompt. The error matches
// ErrInvalid.
func (m Member) checkTypedPrompt(goal string) error {
	if n := typedPromptLen(m.Prompt, goal) - 1; n > session.MaxSubmitText {
		return invalidf("member %s: the prompt with the goal in place of $GOAL is %d bytes, more than %d", quote(m.Name), n, session.MaxSubmitText)
	}
	return nil
}

// clone returns a copy of c that shares no slice with it.
func (c Crew) clone() Crew {
	c.Members = slices.Clone(c.Members)
	for i := range c.Members {
		c.Members[i].Args = slices.Clone(c.Members[i].Args)
	}
	if c.Manifest != nil {
		rec := c.Manifest.clone()
		c.Manifest = &rec
	}
	return c
}

// nameError checks a name the way a crew's is checked: at most maxName
// characters, no control characters. what names it in the message.
func nameError(what, s string) error {
	if utf8.RuneCountInString(s) > maxName {
		return invalidf("%s must be at most %d characters", what, maxName)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return invalidf("%s must not contain control characters", what)
	}
	return nil
}

// ValidateLabel checks a run's own name (Run.Label) as a crew name is
// checked; empty is fine: the run then has none.
func ValidateLabel(label string) error {
	return nameError("label", label)
}
