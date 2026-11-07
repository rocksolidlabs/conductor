package crew

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// People in a crew (docs/plans/decisions.md, HM1): the persons who own its
// decisions and its tasks' outcomes, local labels as Dispatch's people are
// (decision D4). A person is a list of its own beside the members, never
// one of them, so no loop of the engine over a crew's members (the launch,
// the start rules, Reserve, the attempts, the checks) can start a person:
// nothing reads People to start, reserve or stop anything.

// Person is a person of a crew: a local label (decision D4), never
// started. The name is the person, as in Dispatch's directory.
type Person struct {
	Name             string   `json:"name"`
	Responsibilities []string `json:"responsibilities,omitempty"`
	Note             string   `json:"note,omitempty"`
}

// The responsibilities a person may hold in a crew, a closed list, each
// held by at most one person of it (the single approver of decision D4).
const (
	RespArchitecture = "architecture"
	RespProduct      = "product"
	RespSecurity     = "security"
	RespAcceptance   = "acceptance"
)

// Responsibilities are the four, in their order.
var Responsibilities = []string{RespArchitecture, RespProduct, RespSecurity, RespAcceptance}

// ResponsibilityWords are the responsibilities in plain words, the one
// table the task file, the errors and the pages say them by.
var ResponsibilityWords = map[string]string{
	RespArchitecture: "architecture owner",
	RespProduct:      "product owner",
	RespSecurity:     "security reviewer",
	RespAcceptance:   "acceptance owner",
}

// How a task came to its owner (TaskState.OwnerFrom, TaskOwner).
const (
	OwnerFromTask       = "task"       // the task names its owner
	OwnerFromAcceptance = "acceptance" // the crew's acceptance owner, the task naming none
)

// Bounds of a crew's people.
const (
	maxPeople = 12
	// MaxPersonName is a person's name in characters: Dispatch's
	// (dispatch.MaxPersonName), so a crew's person and a ticket's owner are
	// the same label. A test in internal/api, which imports both, holds the
	// two equal.
	MaxPersonName = 60
	maxPersonNote = 500 // characters, on one line
)

// checkPersonName checks a person's name by Dispatch's person.name rule:
// 1 to MaxPersonName characters of valid UTF-8 with no control character.
func checkPersonName(i int, name string) error {
	switch {
	case name == "":
		return invalidf("people[%d]: the name is empty", i)
	case !utf8.ValidString(name):
		return invalidf("people[%d]: the name is not valid UTF-8", i)
	case utf8.RuneCountInString(name) > MaxPersonName:
		return invalidf("person %s: the name must be at most %d characters", quote(name), MaxPersonName)
	case strings.ContainsFunc(name, unicode.IsControl):
		return invalidf("person %s: the name must not contain control characters", quote(name))
	}
	return nil
}

// checkPeople checks the crew's people against its members (names): at
// most maxPeople; each name Dispatch's rule, unique in the crew and none a
// member's; each responsibility one of the four, at most once per person
// and held by one person of the crew; a note of at most maxPersonNote
// characters on one line; and every task's owner empty or a person of the
// crew. A crew without tasks may list people.
func (c Crew) checkPeople(members []string) error {
	if len(c.People) > maxPeople {
		return invalidf("too many people (at most %d)", maxPeople)
	}
	names := make([]string, 0, len(c.People))
	holder := map[string]string{}
	for i, p := range c.People {
		if err := checkPersonName(i, p.Name); err != nil {
			return err
		}
		if slices.Contains(names, p.Name) {
			return invalidf("person %s: the name is used twice", quote(p.Name))
		}
		if slices.Contains(members, p.Name) {
			return invalidf("person %s: the name is a member's; a person is never a member, so the two names differ", quote(p.Name))
		}
		names = append(names, p.Name)
		for j, r := range p.Responsibilities {
			if !slices.Contains(Responsibilities, r) {
				return invalidf("person %s: responsibility %s is not one of %s", quote(p.Name), quote(r), strings.Join(Responsibilities, ", "))
			}
			if slices.Contains(p.Responsibilities[:j], r) {
				return invalidf("person %s: holds %s twice", quote(p.Name), r)
			}
			if h, ok := holder[r]; ok {
				return invalidf("person %s: %s is %s's already; one person of a crew is its %s", quote(p.Name), r, h, ResponsibilityWords[r])
			}
			holder[r] = p.Name
		}
		if utf8.RuneCountInString(p.Note) > maxPersonNote {
			return invalidf("person %s: the note must be at most %d characters", quote(p.Name), maxPersonNote)
		}
		if strings.ContainsFunc(p.Note, unicode.IsControl) {
			return invalidf("person %s: the note must be one line, with no control characters", quote(p.Name))
		}
	}
	for _, t := range c.Tasks {
		if t.Owner != "" && !slices.Contains(names, t.Owner) {
			return invalidf("task %s: owner %s is not a person of this crew", quote(t.ID), quote(t.Owner))
		}
	}
	return nil
}

// person is the person of c named name.
func (c Crew) person(name string) (Person, bool) {
	i := slices.IndexFunc(c.People, func(p Person) bool { return p.Name == name })
	if i < 0 {
		return Person{}, false
	}
	return c.People[i], true
}

// holderOf is the person of c who holds the responsibility.
func (c Crew) holderOf(resp string) (Person, bool) {
	i := slices.IndexFunc(c.People, func(p Person) bool { return slices.Contains(p.Responsibilities, resp) })
	if i < 0 {
		return Person{}, false
	}
	return c.People[i], true
}

// TaskOwner is who owns t's outcome in c: the person the task names, or
// else the crew's acceptance owner, with why (OwnerFromTask,
// OwnerFromAcceptance); ok is false for no one. The one rule the task
// file, the run's state and a person's work use.
func TaskOwner(c Crew, t Task) (Person, string, bool) {
	if t.Owner != "" {
		if p, ok := c.person(t.Owner); ok {
			return p, OwnerFromTask, true
		}
	}
	if p, ok := c.holderOf(RespAcceptance); ok {
		return p, OwnerFromAcceptance, true
	}
	return Person{}, "", false
}

// The two kinds of label a decision may name a person by (Holder).
const (
	labelRole   = "role:"
	labelPerson = "person:"
)

// Holder is the person label names in c: role:<responsibility> its one
// holder, with the responsibility; person:<name> that person, with "". The
// error, matching ErrInvalid, says why not ("no person of crew api holds
// security"). The only function that says whom a label names.
func Holder(c Crew, label string) (Person, string, error) {
	crewName := cmp.Or(c.ID, c.Name)
	if resp, ok := strings.CutPrefix(label, labelRole); ok {
		if !slices.Contains(Responsibilities, resp) {
			return Person{}, "", invalidf("%s is not one of %s", quote(label), strings.Join(Responsibilities, ", "))
		}
		p, ok := c.holderOf(resp)
		if !ok {
			return Person{}, "", invalidf("no person of crew %s holds %s", crewName, resp)
		}
		return p, resp, nil
	}
	if name, ok := strings.CutPrefix(label, labelPerson); ok {
		p, ok := c.person(name)
		if !ok {
			return Person{}, "", invalidf("no person of crew %s is named %s", crewName, quote(name))
		}
		return p, "", nil
	}
	return Person{}, "", invalidf("label %s must be role:<responsibility> or person:<name>", quote(label))
}

// TaskPerson is a person as a task file names them: the name and the
// responsibilities held in the crew, never the note.
type TaskPerson struct {
	Name             string   `json:"name"`
	Responsibilities []string `json:"responsibilities,omitempty"`
}

// taskPeople is who answers for t's work in c as a task file names them:
// its owner and how they came to own it (TaskOwner), and the crew's
// people; nothing for a crew without people.
func taskPeople(c Crew, t Task) (owner *TaskPerson, from string, people []TaskPerson) {
	if len(c.People) == 0 {
		return nil, "", nil
	}
	for _, p := range c.People {
		people = append(people, TaskPerson{Name: p.Name, Responsibilities: slices.Clone(p.Responsibilities)})
	}
	if p, why, ok := TaskOwner(c, t); ok {
		owner = &TaskPerson{Name: p.Name, Responsibilities: slices.Clone(p.Responsibilities)}
		from = why
	}
	return owner, from, people
}

// taskPeopleBytes is how many bytes who answers for t takes in its
// task.json (taskPeople as encodeTaskJSON writes it), for the never-cut
// part ValidateSave bounds.
func taskPeopleBytes(c Crew, t Task) int {
	owner, from, people := taskPeople(c, t)
	if people == nil {
		return 0
	}
	n := taskTextBytes(from)
	for _, v := range []any{owner, people} {
		b, _ := encodeTaskJSON(v)
		n += len(b)
	}
	return n
}

// writePeople writes task.md's "Who answers for this work": the owner and
// how they came to own it, each responsibility held and its holder, the
// people who hold none, and the rule. At most twelve lines; nothing for a
// crew without people.
func writePeople(b *strings.Builder, f TaskFile) {
	if len(f.People) == 0 {
		return
	}
	b.WriteString("## Who answers for this work\n\n")
	switch {
	case f.Owner == nil:
		b.WriteString("No person owns this task's outcome.\n\n")
	case f.OwnerFrom == OwnerFromAcceptance:
		fmt.Fprintf(b, "%s owns this task's outcome, as the crew's acceptance owner: the task names no owner of its own.\n\n", f.Owner.Name)
	default:
		fmt.Fprintf(b, "%s owns this task's outcome: the task names them as its owner.\n\n", f.Owner.Name)
	}
	var none []string
	for _, r := range Responsibilities {
		for _, p := range f.People {
			if slices.Contains(p.Responsibilities, r) {
				fmt.Fprintf(b, "- %s: %s\n", ResponsibilityWords[r], p.Name)
			}
		}
	}
	for _, p := range f.People {
		if len(p.Responsibilities) == 0 {
			none = append(none, p.Name)
		}
	}
	if len(none) > 0 {
		fmt.Fprintf(b, "- in the crew, holding none of these: %s\n", strings.Join(none, ", "))
	}
	b.WriteString("\nAsk for a person's decision through Conductor; never act or answer as them.\n\n")
}

// withPeopleOf is c with the people of launch and each task's owner as
// launch has it (by the task's id): a run's people are the launch's, read
// from the crew as the chain's launch row keeps it, never a continuation's
// own row, which an older build may have written without them.
func (c Crew) withPeopleOf(launch Crew) Crew {
	c.People = slices.Clone(launch.People)
	owners := map[string]string{}
	for _, t := range launch.Tasks {
		owners[t.ID] = t.Owner
	}
	c.Tasks = slices.Clone(c.Tasks)
	for i := range c.Tasks {
		c.Tasks[i].Owner = owners[c.Tasks[i].ID]
	}
	return c
}

// maxChain bounds the walk along a run's continued_from links to its
// launch row: a sanity bound, as a link that went round would loop. A
// continuation reads its chain's decisions whole or is refused
// (ErrChainTooLong, readDecisionHistory).
const maxChain = 10000

// LaunchCrew is the crew as launched of runID's chain: the crew JSON of
// the first run of its continued_from links, the launch row, which no
// continuation rewrites and the ledger keeps while a continuation cites
// it. row reads a run's row; one the links name that it has not ends the
// walk at the last it had. An error is row's.
func LaunchCrew(runID string, row func(id string) (ledger.RunRow, bool, error)) (string, error) {
	seen := map[string]bool{}
	var last string
	for id := runID; id != "" && !seen[id] && len(seen) < maxChain; {
		seen[id] = true
		r, ok, err := row(id)
		if err != nil {
			return "", err
		}
		if !ok {
			break
		}
		last, id = r.Crew, r.ContinuedFrom
	}
	return last, nil
}

// launchCrewOf is the crew as launched of from's chain (from.Launch, else
// its own row's), and whether it could be read.
func launchCrewOf(from Loaded) (Crew, bool) {
	var c Crew
	if err := json.Unmarshal([]byte(cmp.Or(from.Launch, from.Run.Crew)), &c); err != nil {
		return Crew{}, false
	}
	return c, true
}

// LaunchHasPeople reports whether the crew as a launch row keeps it
// (JSON) lists people.
func LaunchHasPeople(crewJSON string) bool {
	var c struct {
		People []json.RawMessage `json:"people"`
	}
	return json.Unmarshal([]byte(crewJSON), &c) == nil && len(c.People) > 0
}

// ErrPeopleMismatch says a revision's crew is not the launch row's: its
// members or its tasks differ, or its people do not fit the launch's crew.
var ErrPeopleMismatch = errors.New("the revision's crew is not the launch's")

// RestoredPeople is launch, a launch row's crew an older build wrote
// without its people, with the people and the tasks' owners of revision,
// the crew of the plan revision it was launched from (its digest the
// package's, which the caller checked): only when the members' names and
// the tasks' ids are the launch's and the people fit the launch's crew
// (checkPeople). Anything else is ErrPeopleMismatch with why.
func RestoredPeople(launch, revision Crew) (Crew, error) {
	names := func(c Crew) []string {
		var out []string
		for _, m := range c.Members {
			out = append(out, m.Name)
		}
		return out
	}
	ids := func(c Crew) []string {
		var out []string
		for _, t := range c.Tasks {
			out = append(out, t.ID)
		}
		return out
	}
	if !slices.Equal(names(launch), names(revision)) {
		return Crew{}, fmt.Errorf("%w: its members are %s, the launch's %s", ErrPeopleMismatch, strings.Join(names(revision), ", "), strings.Join(names(launch), ", "))
	}
	if !slices.Equal(ids(launch), ids(revision)) {
		return Crew{}, fmt.Errorf("%w: its tasks are %s, the launch's %s", ErrPeopleMismatch, strings.Join(ids(revision), ", "), strings.Join(ids(launch), ", "))
	}
	out := launch.withPeopleOf(revision)
	if err := out.checkPeople(names(launch)); err != nil {
		return Crew{}, fmt.Errorf("%w: %v", ErrPeopleMismatch, err)
	}
	return out, nil
}

// EncodeLaunched is c as a launch row keeps the crew (crewAsLaunched).
func EncodeLaunched(c Crew) string { return crewAsLaunched(c) }

// setOwner sets st's owner and how, by TaskOwner over c, the crew as
// launched; none when no one owns the task.
func (st *TaskState) setOwner(c Crew, t Task) {
	st.Owner, st.OwnerFrom = "", ""
	if p, why, ok := TaskOwner(c, t); ok {
		st.Owner, st.OwnerFrom = p.Name, why
	}
}

// task is the task of c with the id.
func (c Crew) task(id string) (Task, bool) {
	i := slices.IndexFunc(c.Tasks, func(t Task) bool { return t.ID == id })
	if i < 0 {
		return Task{}, false
	}
	return c.Tasks[i], true
}
