package crew

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// People in a crew (docs/plans/decisions.md, HM1, PR-9): local labels
// beside the members, never started; a task's owner; the run's people as
// launched; the task file's "Who answers for this work".

// threePeople are a crew's people: nate the architecture and product
// owner, ana the acceptance owner, bo holding nothing.
func threePeople() []Person {
	return []Person{
		{Name: "nate", Responsibilities: []string{RespArchitecture, RespProduct}, Note: "Owns the schema."},
		{Name: "ana", Responsibilities: []string{RespAcceptance}},
		{Name: "bo"},
	}
}

// peopleCrew is a managed crew in dir with threePeople: build owned by
// nate, docs by no one of its own (ana, by fallback).
func peopleCrew(dir string) Crew {
	build := work("build", "lead")
	build.Owner = "nate"
	c := repoCrew(dir, build, work("docs", "writer", "build"))
	c.People = threePeople()
	return c
}

func TestValidateTakesPeople(t *testing.T) {
	managed := managedCrew()
	managed.People = []Person{
		{Name: "nate", Responsibilities: []string{RespArchitecture}},
		{Name: "Ana Lima", Responsibilities: []string{RespProduct, RespSecurity}, Note: "Ask her first."},
		{Name: "bo", Responsibilities: []string{RespAcceptance}},
	}
	managed.Tasks[0].Owner = "Ana Lima"
	if err := managed.ValidateSave(); err != nil {
		t.Fatalf("a managed crew with people: %v", err)
	}
	rules := testCrew(immediate("lead", "Plan it."), after("core", "Build it.", "lead"))
	rules.People = threePeople()
	if err := rules.ValidateSave(); err != nil {
		t.Fatalf("a crew with start rules and people: %v", err)
	}
	// At the bounds: twelve people, a name of 60 characters, a note of 500.
	full := testCrew(immediate("lead", "Plan it."))
	for i := range maxPeople {
		full.People = append(full.People, Person{Name: strings.Repeat("é", MaxPersonName-2) + string(rune('a'+i)) + "!", Note: strings.Repeat("n", maxPersonNote)})
	}
	if err := full.ValidateSave(); err != nil {
		t.Fatalf("a crew at the people's bounds: %v", err)
	}
}

func TestValidateRefusesBadPeople(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape func(c *Crew)
		want  string
	}{
		{"thirteen people", func(c *Crew) {
			for i := range maxPeople + 1 {
				c.People = append(c.People, Person{Name: "p" + string(rune('a'+i))})
			}
		}, "too many people (at most 12)"},
		{"a name of 61 characters", func(c *Crew) { c.People = []Person{{Name: strings.Repeat("n", 61)}} }, "the name must be at most 60 characters"},
		{"an empty name", func(c *Crew) { c.People = []Person{{Name: ""}} }, "people[0]: the name is empty"},
		{"a control character", func(c *Crew) { c.People = []Person{{Name: "na\tte"}} }, "the name must not contain control characters"},
		{"not UTF-8", func(c *Crew) { c.People = []Person{{Name: "na\xffte"}} }, "the name is not valid UTF-8"},
		{"a name used twice", func(c *Crew) { c.People = []Person{{Name: "nate"}, {Name: "nate"}} }, `person "nate": the name is used twice`},
		{"a person named as a member", func(c *Crew) { c.People = []Person{{Name: "builder"}} }, `person "builder": the name is a member's`},
		{"an unknown responsibility", func(c *Crew) { c.People = []Person{{Name: "nate", Responsibilities: []string{"legal"}}} }, `responsibility "legal" is not one of architecture, product, security, acceptance`},
		{"one held twice", func(c *Crew) {
			c.People = []Person{{Name: "nate", Responsibilities: []string{RespSecurity, RespSecurity}}}
		}, `person "nate": holds security twice`},
		{"one held by two people", func(c *Crew) {
			c.People = []Person{{Name: "nate", Responsibilities: []string{RespArchitecture}}, {Name: "ana", Responsibilities: []string{RespArchitecture}}}
		}, `person "ana": architecture is nate's already; one person of a crew is its architecture owner`},
		{"a note of 501 characters", func(c *Crew) { c.People = []Person{{Name: "nate", Note: strings.Repeat("n", 501)}} }, "the note must be at most 500 characters"},
		{"a note of two lines", func(c *Crew) { c.People = []Person{{Name: "nate", Note: "one\ntwo"}} }, "the note must be one line"},
		{"a task owner not in the crew", func(c *Crew) {
			c.People = []Person{{Name: "nate"}}
			c.Tasks[0].Owner = "ana"
		}, `task "build": owner "ana" is not a person of this crew`},
		{"a task owner with no people", func(c *Crew) { c.Tasks[0].Owner = "nate" }, `task "build": owner "nate" is not a person of this crew`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := managedCrew()
			tc.shape(&c)
			err := c.Validate()
			if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTaskOwnerFallsBackToTheAcceptanceOwner(t *testing.T) {
	c := peopleCrew("/repo")
	build, docs := c.Tasks[0], c.Tasks[1]
	if p, why, ok := TaskOwner(c, build); !ok || p.Name != "nate" || why != OwnerFromTask {
		t.Fatalf("build's owner: %v %q %v", p, why, ok)
	}
	if p, why, ok := TaskOwner(c, docs); !ok || p.Name != "ana" || why != OwnerFromAcceptance {
		t.Fatalf("docs' owner: %v %q %v", p, why, ok)
	}
	// No acceptance owner: docs is owned by no one, build still by nate.
	c.People[1].Responsibilities = nil
	if p, why, ok := TaskOwner(c, docs); ok || why != "" || p.Name != "" {
		t.Fatalf("docs with no acceptance owner: %v %q %v", p, why, ok)
	}
	if _, why, ok := TaskOwner(c, build); !ok || why != OwnerFromTask {
		t.Fatalf("build with no acceptance owner: %q %v", why, ok)
	}
	if _, _, ok := TaskOwner(Crew{}, docs); ok {
		t.Fatal("a crew without people owns a task")
	}
}

// Holder is the one rule of whom a label names: role:<responsibility> its
// one holder, person:<name> that person; why not otherwise.
func TestHolderNamesTheLabelsPerson(t *testing.T) {
	c := peopleCrew("/repo")
	c.ID = "api"
	for label, want := range map[string][2]string{
		"role:architecture": {"nate", RespArchitecture},
		"role:product":      {"nate", RespProduct},
		"role:acceptance":   {"ana", RespAcceptance},
		"person:bo":         {"bo", ""},
		"person:nate":       {"nate", ""},
	} {
		p, resp, err := Holder(c, label)
		if err != nil || p.Name != want[0] || resp != want[1] {
			t.Fatalf("%s: %v %q %v", label, p, resp, err)
		}
	}
	for label, want := range map[string]string{
		"role:security": "no person of crew api holds security",
		"role:legal":    `"role:legal" is not one of architecture, product, security, acceptance`,
		"person:cy":     `no person of crew api is named "cy"`,
		"nate":          `label "nate" must be role:<responsibility> or person:<name>`,
	} {
		if _, _, err := Holder(c, label); err == nil || !errors.Is(err, ErrInvalid) || err.Error() != want {
			t.Fatalf("%s: %v, want %q", label, err, want)
		}
	}
}

// Nothing reads a crew's people to start anything: a crew of two members
// and three people, run managed and not, makes the members' sessions
// alone, no worktree or branch under a person's name, and reservations
// for the members' attempts only.
func TestAPersonIsNeverStarted(t *testing.T) {
	people := threePeople()
	isPerson := func(s string) bool {
		return slices.ContainsFunc(people, func(p Person) bool { return strings.Contains(s, p.Name) })
	}
	t.Run("managed", func(t *testing.T) {
		dir := newRepo(t)
		e, fl, _ := managedEngine(t)
		run := launchManaged(t, e, peopleCrew(dir))
		commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
		waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
		waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
		stopRun(t, e, run.ID)
		fl.mu.Lock()
		calls, reserves := slices.Clone(fl.calls), fl.reserves
		fl.mu.Unlock()
		var names []string
		for _, c := range calls {
			names = append(names, c.name)
		}
		if !slices.Equal(names, []string{"lead · build #1", "writer · docs #1"}) || reserves != 2 {
			t.Fatalf("sessions %v, %d reservations", names, reserves)
		}
		entries, err := os.ReadDir(filepath.Join(dir, ".conductor", "worktrees", run.ID))
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range entries {
			if isPerson(en.Name()) {
				t.Fatalf("a worktree for a person: %s", en.Name())
			}
		}
		branches := runGit(t, dir, "branch", "--list", "crew/*")
		for _, p := range people {
			if strings.Contains(branches, p.Name) {
				t.Fatalf("a branch for %s: %s", p.Name, branches)
			}
		}
	})
	t.Run("start rules", func(t *testing.T) {
		e, fl := readyEngine(t)
		c := testCrew(immediate("lead", "Plan it."), immediate("core", "Build it."))
		c.People = people
		run, err := e.Launch(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		waitStatus(t, e, run.ID, "lead", MemberRunning, 10*time.Second)
		waitStatus(t, e, run.ID, "core", MemberRunning, 10*time.Second)
		stopRun(t, e, run.ID)
		fl.mu.Lock()
		defer fl.mu.Unlock()
		var names []string
		for _, c := range fl.calls {
			names = append(names, c.name)
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"core", "lead"}) || fl.reserves != 0 {
			t.Fatalf("sessions %v, %d reservations", names, fl.reserves)
		}
	})
}

// wantPeople fails t unless r carries the launch's people (threePeople)
// and build's owner nate (task), docs' ana (acceptance).
func wantPeople(t *testing.T, what string, r Run) {
	t.Helper()
	if !reflect.DeepEqual(r.People, threePeople()) {
		t.Fatalf("%s: people %+v", what, r.People)
	}
	owners := map[string]string{}
	for _, ts := range r.Tasks {
		owners[ts.ID] = ts.Owner + "/" + ts.OwnerFrom
	}
	if owners["build"] != "nate/task" || owners["docs"] != "ana/acceptance" {
		t.Fatalf("%s: owners %v", what, owners)
	}
}

// A run's people and owners are the crew's as launched: an edit of the
// crew after the launch changes nothing of the run, its record rebuilt
// from the ledger or its continuation, whose ledger row keeps them too.
func TestTheRunsPeopleAreTheLaunchs(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	c := peopleCrew(dir)
	run := launchManaged(t, e, c)
	wantPeople(t, "the launch", run)
	// The attempt's task file names its owner and the people.
	if _, f := taskFileOf(t, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)); f.Owner == nil || f.Owner.Name != "nate" || f.OwnerFrom != OwnerFromTask || len(f.People) != 3 {
		t.Fatalf("build's task file: %+v %q %+v", f.Owner, f.OwnerFrom, f.People)
	}
	// The saved crew edited: ana leaves, bo owns build.
	c.People[1] = Person{Name: "cy", Responsibilities: []string{RespAcceptance}}
	c.Tasks[0].Owner = "bo"
	got, _ := e.Get(run.ID)
	wantPeople(t, "after the edit", got)
	stopRun(t, e, run.ID)
	ctx := context.Background()
	from, ok, err := SQLLedger(ld).Load(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	wantPeople(t, "the record rebuilt", Recovered(Run{}, from))
	// A record without them, as an older build writes it.
	old, _ := e.Get(run.ID)
	old.People = nil
	for i := range old.Tasks {
		old.Tasks[i].Owner, old.Tasks[i].OwnerFrom = "", ""
	}
	wantPeople(t, "an older record recovered", Recovered(old, from))
	// The task file a later attempt reads: LoadedTask names the owner.
	if st, _, ok := LoadedTask(from, "docs"); !ok || st.Owner != "ana" || st.OwnerFrom != OwnerFromAcceptance {
		t.Fatalf("docs loaded: %+v", st)
	}
	e2, _ := readyEngine(t)
	e2.UseLedger(SQLLedger(ld))
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e2.Stop(context.Background(), next.ID) })
	wantPeople(t, "the continuation", *next)
	row, ok, err := ld.Run(ctx, next.ID)
	if err != nil || !ok {
		t.Fatalf("the continuation's row: %v %v", ok, err)
	}
	var kept Crew
	if err := json.Unmarshal([]byte(row.Crew), &kept); err != nil || !reflect.DeepEqual(kept.People, threePeople()) || kept.Tasks[0].Owner != "nate" {
		t.Fatalf("the continuation's row keeps %+v (%v)", kept.People, err)
	}
}

// The task file names who answers for the work, never cut: the owner and
// how, each responsibility's holder, the people holding none, the rule;
// a golden, and the existing goldens (TestTaskFileByteIdenticalWithoutContext)
// unchanged for a crew without people.
func TestTaskFileNamesWhoAnswers(t *testing.T) {
	c := peopleCrew("/repo")
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	files := map[string]Task{"people": c.Tasks[0], "people-fallback": c.Tasks[1]}
	dir := filepath.Join("testdata", "taskfile")
	for name, task := range files {
		f := TaskFile{Run: "r-0123456789ab", Crew: "c-build", CrewName: "Build crew", Goal: "Ship the parser, tested.", Task: task.ID, Attempt: 1, MaxAttempts: maxAttemptsPerTask,
			Member: task.Member, Kind: task.Kind, Instructions: task.Instructions, Worktree: "/repo/.conductor/worktrees/r-0123456789ab/" + task.ID + ".1",
			Branch: "crew/r-0123456789ab/" + task.ID + ".1", Start: strings.Repeat("a", 40), StartFrom: "the run's base", WrittenAt: at}
		f.Owner, f.OwnerFrom, f.People = taskPeople(c, task)
		md, js, more, err := fitTaskFile(f)
		if err != nil || more != nil {
			t.Fatalf("%s: %v, cut %v", name, err, more != nil)
		}
		got := map[string][]byte{name + ".md": md, name + ".json": js}
		for file, b := range got {
			path := filepath.Join(dir, file)
			if *updateTaskFileGoldens {
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if !bytes.Equal(b, want) {
				t.Errorf("%s differs from its golden:\n got: %q\nwant: %q", file, b, want)
			}
		}
	}
	// The section, in task.md, after the instructions; at most twelve lines.
	f := TaskFile{Task: "docs", Attempt: 1, MaxAttempts: 3, Member: "writer", Kind: TaskWork, Goal: "g", Instructions: "Do docs.", Start: strings.Repeat("b", 40)}
	f.Owner, f.OwnerFrom, f.People = taskPeople(c, c.Tasks[1])
	md := taskMarkdown(f)
	want := "## Instructions\n\nDo docs.\n\n## Who answers for this work\n\nana owns this task's outcome, as the crew's acceptance owner: the task names no owner of its own.\n\n" +
		"- architecture owner: nate\n- product owner: nate\n- acceptance owner: ana\n- in the crew, holding none of these: bo\n\n" +
		"Ask for a person's decision through Conductor; never act or answer as them.\n\n## Where you are"
	if !strings.Contains(md, want) {
		t.Fatalf("task.md:\n%s", md)
	}
	section := md[strings.Index(md, "## Who answers"):strings.Index(md, "## Where you are")]
	if n := strings.Count(section, "\n"); n > 12 {
		t.Fatalf("the section is %d lines", n)
	}
	// Never cut: a task file cut to fit keeps them in task.json.
	big := f
	big.Instructions = strings.Repeat("Instructions. ", 2400)
	for range 16 {
		big.Inputs = append(big.Inputs, TaskInput{Task: "in", Attempt: 1, Commit: strings.Repeat("1", 40), Summary: strings.Repeat("s", maxSummary)})
	}
	_, js, more, err := fitTaskFile(big)
	if err != nil || more == nil {
		t.Fatalf("not cut: %v", err)
	}
	var kept TaskFile
	if err := json.Unmarshal(js, &kept); err != nil || kept.Owner == nil || kept.Owner.Name != "ana" || len(kept.People) != 3 || bytes.Contains(more, []byte(`"people"`)) {
		t.Fatalf("task.json cut: %+v %v", kept.People, err)
	}
	// A crew without people writes none of it.
	none := f
	none.Owner, none.OwnerFrom, none.People = taskPeople(Crew{}, c.Tasks[1])
	if md := taskMarkdown(none); strings.Contains(md, "Who answers") {
		t.Fatalf("a crew without people: %s", md)
	}
	// The never-cut part counts the people: ValidateSave refuses a task
	// whose text and people pass maxTaskText.
	tight := peopleCrew("/repo")
	tight.Goal, tight.Members[0].Prompt = "g", "p"
	// Instructions that bring the text to 5 bytes under maxTaskText: k
	// control characters (six bytes each in JSON) and the rest one byte.
	rest := maxTaskText - 5 - taskTextBytes(tight.Goal, tight.Members[0].Prompt) - 2
	k := (rest - maxInstructions + 4) / 5
	tight.Tasks[0].Instructions = strings.Repeat("\x01", k) + strings.Repeat("x", rest-6*k)
	withoutPeople := tight
	withoutPeople.People, withoutPeople.Tasks = nil, slices.Clone(tight.Tasks)
	withoutPeople.Tasks[0].Owner = ""
	text := taskTextBytes(tight.Goal, tight.Members[0].Prompt, tight.Tasks[0].Instructions)
	if people := taskPeopleBytes(tight, tight.Tasks[0]); people == 0 || text+people <= maxTaskText || text > maxTaskText {
		t.Fatalf("the bounds do not straddle maxTaskText: text %d, people %d", text, people)
	}
	if err := withoutPeople.ValidateSave(); err != nil {
		t.Fatalf("the task alone: %v", err)
	}
	if err := tight.ValidateSave(); err == nil || !strings.Contains(err.Error(), "its goal, role, instructions and people are") {
		t.Fatalf("the task with its people: %v", err)
	}
}

// A crew's people round-trip through the store, and its summary lists
// them with their responsibilities, never their notes; a crew without
// people is written byte for byte as before.
func TestStoreRoundTripsPeople(t *testing.T) {
	s, st := newStore(t)
	c := validCrew("api", "API")
	c.People = threePeople()
	if err := s.Put(c); err != nil {
		t.Fatal(err)
	}
	got, err := reopen(t, st).Get("api")
	if err != nil || !reflect.DeepEqual(got.People, threePeople()) {
		t.Fatalf("read back: %+v %v", got.People, err)
	}
	sums, _, err := s.List(0, 10)
	if err != nil || len(sums) != 1 {
		t.Fatalf("list: %v %v", sums, err)
	}
	want := []TaskPerson{{Name: "nate", Responsibilities: []string{RespArchitecture, RespProduct}}, {Name: "ana", Responsibilities: []string{RespAcceptance}}, {Name: "bo"}}
	if !reflect.DeepEqual(sums[0].People, want) {
		t.Fatalf("the summary's people: %+v", sums[0].People)
	}
	if b, _ := json.Marshal(sums[0]); bytes.Contains(b, []byte("Owns the schema")) {
		t.Fatalf("the summary carries a note: %s", b)
	}
	// A legacy crew: no people key, in its file or its summary.
	legacy := validCrew("web", "Web")
	if err := s.Put(legacy); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(st.Dir(), "crews", "web.json"))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := store.Encode(legacy.canonical())
	if !bytes.Equal(file, enc) || bytes.Contains(file, []byte(`"people"`)) || bytes.Contains(file, []byte(`"owner"`)) {
		t.Fatalf("a legacy crew's file: %s", file)
	}
	sums, _, _ = s.List(0, 10)
	for _, sum := range sums {
		if b, _ := json.Marshal(sum); sum.ID == "web" && bytes.Contains(b, []byte(`"people"`)) {
			t.Fatalf("a legacy crew's summary: %s", b)
		}
	}
}

// A run without tasks keeps its people while the engine holds it and
// writes none into its record: its JSON has no people key, which every
// older build reads as before.
func TestARecordWithoutTasksKeepsNoPeople(t *testing.T) {
	e, _ := readyEngine(t)
	c := testCrew(immediate("lead", "Plan it."))
	c.People = threePeople()
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 10*time.Second)
	stopRun(t, e, run.ID)
	got, _ := e.Get(run.ID)
	if !reflect.DeepEqual(got.People, threePeople()) {
		t.Fatalf("the run the engine holds: %+v", got.People)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rs.Close)
	if err := rs.Save(got); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(st.Dir(), "runs", run.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["people"]; ok {
		t.Fatalf("the record of a run without tasks has people: %s", b)
	}
}
