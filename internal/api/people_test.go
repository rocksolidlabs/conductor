package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// People in a crew through the API (docs/plans/decisions.md, HM1, PR-9):
// the crew routes' people and owners, the run routes' people and owners,
// GET /api/people, a Brakeman draft's people, and the records and launch
// rows an older build wrote without them. An older build is the types
// before HM1, whose Crew, Task, Run and TaskState lack the fields HM1
// adds; every one of those fields is omitempty, so what an older build
// writes is exactly the HM1 value with them zeroed (olderCrew,
// olderRecord).

// crewPeople are the people of the API's test crews: nate the
// architecture owner, ana the acceptance owner with a note.
func crewPeople() []crew.Person {
	return []crew.Person{
		{Name: "nate", Responsibilities: []string{crew.RespArchitecture}},
		{Name: "ana", Responsibilities: []string{crew.RespAcceptance}, Note: "Signs off."},
	}
}

// peopledCrew is managedCrew (build, then docs) with crewPeople, build
// owned by nate and docs by ana as the acceptance owner.
func peopledCrew(root string) crew.Crew {
	c := managedCrew(root)
	c.People = crewPeople()
	c.Tasks[0].Owner = "nate"
	return c
}

// olderCrew is c as an older build encodes it: no people, no owners.
func olderCrew(c crew.Crew) string {
	c.People = nil
	for i := range c.Tasks {
		c.Tasks[i].Owner = ""
	}
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// olderRecord is rec as an older build writes it: no people, no owners.
func olderRecord(rec crew.Run) crew.Run {
	rec.People = nil
	rec.Tasks = append([]crew.TaskState(nil), rec.Tasks...)
	for i := range rec.Tasks {
		rec.Tasks[i].Owner, rec.Tasks[i].OwnerFrom = "", ""
	}
	return rec
}

// wantRunPeople fails t unless the run as the API answers it carries
// crewPeople, build owned by nate (task), docs by ana (acceptance).
func wantRunPeople(t *testing.T, what string, run map[string]any) {
	t.Helper()
	if got := peopleOf(t, run["people"]); !reflect.DeepEqual(got, crewPeople()) {
		t.Fatalf("%s: people %+v", what, got)
	}
	owners := map[string]string{}
	tasks, _ := run["tasks"].([]any)
	for _, x := range tasks {
		task := x.(map[string]any)
		owners[task["id"].(string)] = fmt.Sprint(task["owner"], "/", task["ownerFrom"])
	}
	if owners["build"] != "nate/task" || owners["docs"] != "ana/acceptance" {
		t.Fatalf("%s: owners %v", what, owners)
	}
}

// peopleOf is a reply's people decoded.
func peopleOf(t *testing.T, v any) []crew.Person {
	t.Helper()
	b, _ := json.Marshal(v)
	var out []crew.Person
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// launchPeopled launches peopledCrew in e's repository, stops it and
// lets the engine forget it, its record written: the run is read from its
// record and the ledger from then on.
func launchPeopled(t *testing.T, e *testEnv, c crew.Crew) string {
	t.Helper()
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	e.expect(t, "POST", "/api/runs/"+run.ID+"/stop", nil, http.StatusOK, "")
	forget(t, e, run.ID)
	return run.ID
}

// forget has the engine let go of the run and waits for its record.
func forget(t *testing.T, e *testEnv, runID string) {
	t.Helper()
	e.srv.runs.ForgetForTest(runID)
	if !e.srv.runs.WaitRecorded(t.Context()) {
		t.Fatal("the records were not written")
	}
}

// The crew name's bound is Dispatch's person.name rule: the same 60
// characters, so a crew's person and a ticket's owner are one label.
func TestPersonNameBoundIsDispatchs(t *testing.T) {
	if crew.MaxPersonName != dispatch.MaxPersonName {
		t.Fatalf("a crew's person %d characters, Dispatch's %d", crew.MaxPersonName, dispatch.MaxPersonName)
	}
	e := newTestEnv(t, nil)
	c := crew.Crew{Name: "Bound", Where: crew.WhereServer, Isolation: crew.IsolationNone, Members: []crew.Member{{Name: "lead", AgentID: "claude", Start: crew.Start{When: crew.StartImmediately}}}}
	for n, ok := range map[int]bool{dispatch.MaxPersonName: true, dispatch.MaxPersonName + 1: false} {
		name := strings.Repeat("é", n)
		c.People = []crew.Person{{Name: name}}
		crewErr := c.Validate()
		rc, err := e.srv.dispatch.AddPerson(context.Background(), dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID()}, name)
		if err != nil {
			t.Fatal(err)
		}
		if (crewErr == nil) != ok || (rc.Status == dispatch.StatusDone) != ok {
			t.Fatalf("%d characters: the crew's %v, Dispatch's %+v", n, crewErr, rc)
		}
	}
}

// POST, PUT and GET /api/crews/{id} take and answer people and a task's
// owner; the list answers each crew's people with their
// responsibilities, never the notes; a crew breaking checkPeople is 400
// invalid_crew with why.
func TestCrewRoutesCarryPeople(t *testing.T) {
	e := readyEnv(t, nil)
	body := func(people []crew.Person, owner string) map[string]any {
		c := managedCrew(e.root)
		c.Tasks[0].Owner = owner
		return map[string]any{"name": "Routes", "goal": c.Goal, "cwd": c.Cwd, "where": c.Where, "isolation": c.Isolation, "members": c.Members, "tasks": c.Tasks, "people": people}
	}
	out := e.expect(t, "POST", "/api/crews", body(crewPeople(), "nate"), http.StatusCreated, "")
	saved := out["crew"].(map[string]any)
	id := saved["id"].(string)
	got := e.expect(t, "GET", "/api/crews/"+id, nil, http.StatusOK, "")["crew"].(map[string]any)
	if people := peopleOf(t, got["people"]); !reflect.DeepEqual(people, crewPeople()) || got["tasks"].([]any)[0].(map[string]any)["owner"] != "nate" {
		t.Fatalf("read back: %+v %v", people, got["tasks"])
	}
	list := e.expect(t, "GET", "/api/crews", nil, http.StatusOK, "")["crews"].([]any)
	listed, _ := json.Marshal(list[0].(map[string]any)["people"])
	if listed := peopleOf(t, list[0].(map[string]any)["people"]); !reflect.DeepEqual(listed, []crew.Person{{Name: "nate", Responsibilities: []string{crew.RespArchitecture}}, {Name: "ana", Responsibilities: []string{crew.RespAcceptance}}}) {
		t.Fatalf("the list's people: %+v", listed)
	}
	if strings.Contains(string(listed), "note") {
		t.Fatalf("the list carries a note: %s", listed)
	}
	// An update: ana is the architecture owner too, build owned by no one.
	next := crewPeople()
	next[1].Responsibilities = []string{crew.RespAcceptance, crew.RespSecurity}
	e.expect(t, "PUT", "/api/crews/"+id, body(next, ""), http.StatusOK, "")
	got = e.expect(t, "GET", "/api/crews/"+id, nil, http.StatusOK, "")["crew"].(map[string]any)
	if p := got["people"].([]any)[1].(map[string]any); fmt.Sprint(p["responsibilities"]) != "[acceptance security]" || got["tasks"].([]any)[0].(map[string]any)["owner"] != nil {
		t.Fatalf("updated: %v %v", got["people"], got["tasks"])
	}
	// Refused: one responsibility held twice, an owner not in the crew.
	twice := crewPeople()
	twice[1].Responsibilities = []string{crew.RespArchitecture}
	out = e.expect(t, "PUT", "/api/crews/"+id, body(twice, ""), http.StatusBadRequest, "invalid_crew")
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "architecture is nate's already") {
		t.Fatalf("held twice: %s", msg)
	}
	out = e.expect(t, "POST", "/api/crews", body(crewPeople(), "cy"), http.StatusBadRequest, "invalid_crew")
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, `owner "cy" is not a person of this crew`) {
		t.Fatalf("an owner not in the crew: %s", msg)
	}
}

// The run routes answer the run's people and each task's owner and how,
// while the engine holds the run and once it reads from its record and
// the ledger.
func TestRunRoutesCarryOwners(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), peopledCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	wantRunPeople(t, "held", runOf(t, e, run.ID))
	var listed map[string]any
	for _, r := range e.expect(t, "GET", "/api/runs", nil, http.StatusOK, "")["runs"].([]any) {
		if r := r.(map[string]any); r["id"] == run.ID {
			listed = r
		}
	}
	wantRunPeople(t, "listed", listed)
	task := e.expect(t, "GET", "/api/runs/"+run.ID+"/tasks/docs", nil, http.StatusOK, "")["task"].(map[string]any)
	if task["owner"] != "ana" || task["ownerFrom"] != crew.OwnerFromAcceptance {
		t.Fatalf("docs read alone: %v", task)
	}
	e.expect(t, "POST", "/api/runs/"+run.ID+"/stop", nil, http.StatusOK, "")
	forget(t, e, run.ID)
	wantRunPeople(t, "recorded", runOf(t, e, run.ID))
	task = e.expect(t, "GET", "/api/runs/"+run.ID+"/tasks/build", nil, http.StatusOK, "")["task"].(map[string]any)
	if task["owner"] != "nate" || task["ownerFrom"] != crew.OwnerFromTask {
		t.Fatalf("build from the ledger: %v", task)
	}
}

// A managed run's record deleted, as an older build's prune would: the
// next start's syncRecords writes it again with the launch's people and
// owners, and the run routes answer them.
func TestAManagedRecordIsRebuiltWithItsPeople(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	runID := launchPeopled(t, e, peopledCrew(e.root))
	if err := e.srv.records.Delete(runID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.srv.records.Get(runID); ok {
		t.Fatal("the record is still there")
	}
	syncRecords(t.Context(), e.srv.ledger, e.srv.records, e.srv.log)
	rec, ok, err := e.srv.records.Get(runID)
	if err != nil || !ok || !reflect.DeepEqual(rec.People, crewPeople()) {
		t.Fatalf("rebuilt: %+v %v %v", rec.People, ok, err)
	}
	wantRunPeople(t, "rebuilt", runOf(t, e, runID))
}

// A record kept, written again by an older build without people and
// owners at the ledger's revision and settled: the next start's
// syncRecords writes it again with them, and the run routes answer them;
// the record of a run whose crew lists no people is left byte for byte.
func TestAnOlderBuildsRebuiltRecordGetsItsPeople(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	runID := launchPeopled(t, e, peopledCrew(e.root))
	plain := managedCrew(e.root)
	plain.ID = "plain"
	plainID := launchPeopled(t, e, plain)
	ctx := t.Context()
	rec, ok, err := e.srv.records.Get(runID)
	if err != nil || !ok {
		t.Fatalf("the record: %v %v", ok, err)
	}
	row, _, err := e.srv.ledger.Run(ctx, runID)
	if err != nil || rec.Managed == nil || rec.Managed.Revision != row.Revision {
		t.Fatalf("the record is not at the ledger's revision: %+v %d %v", rec.Managed, row.Revision, err)
	}
	if err := e.srv.records.Save(olderRecord(rec)); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := e.srv.records.Get(runID); len(again.People) != 0 {
		t.Fatalf("the older build's record has people: %+v", again.People)
	}
	plainFile := filepath.Join(e.srv.cfg.DataDir, "runs", plainID+".json")
	before, err := os.ReadFile(plainFile)
	if err != nil {
		t.Fatal(err)
	}
	syncRecords(ctx, e.srv.ledger, e.srv.records, e.srv.log)
	wantRunPeople(t, "rewritten", runOf(t, e, runID))
	if after, err := os.ReadFile(plainFile); err != nil || string(after) != string(before) {
		t.Fatalf("the record of a run without people was written again: %v", err)
	}
}

// The downgrade: a run launched with people, continued, and the
// continuation's row written again as an older build writes it (its crew
// without people and owners) and its record deleted. The upgrade: the
// next start's syncRecords rebuilds that record with the launch's people
// and owners, the run routes answer them, and a further continuation
// carries them in its own row.
func TestAnOlderBuildsContinuationLosesNoPeople(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	c := peopledCrew(e.root)
	runID := launchPeopled(t, e, c)
	ctx := t.Context()
	cont := e.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)["id"].(string)
	t.Cleanup(func() { stopEveryRun(e.srv) })
	runningAttempt(t, e, cont, "build", 2)
	e.expect(t, "POST", "/api/runs/"+cont+"/stop", nil, http.StatusOK, "")
	forget(t, e, cont)
	ld := e.srv.ledger
	if _, err := ld.Transition(ctx, cont, func(tx *ledger.Tx) error {
		row, _, err := tx.Run()
		if err != nil {
			return err
		}
		var kept crew.Crew
		if err := json.Unmarshal([]byte(row.Crew), &kept); err != nil {
			return err
		}
		row.Crew = olderCrew(kept)
		return tx.PutRun(row)
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.records.Delete(cont); err != nil {
		t.Fatal(err)
	}
	syncRecords(ctx, ld, e.srv.records, e.srv.log)
	wantRunPeople(t, "the continuation rebuilt", runOf(t, e, cont))
	next := e.expect(t, "POST", "/api/runs/"+cont+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)
	wantRunPeople(t, "a further continuation", next)
	row, ok, err := ld.Run(ctx, next["id"].(string))
	if err != nil || !ok || !crew.LaunchHasPeople(row.Crew) || !strings.Contains(row.Crew, `"owner":"nate"`) {
		t.Fatalf("the further continuation's row: %s %v %v", row.Crew, ok, err)
	}
}

// The downgrade before the first launch: a revision with people and owners
// approved, its launch row written as an older build's launchPlanned and
// LaunchPlanned write it (the revision's crew without them) and its record
// so written. The upgrade: the next start makes the launch row whole
// (restoreLaunchPeople) and syncRecords the record, the run routes answer
// the people and owners, and a continuation carries them in its own row.
// A revision whose digest is not the package's, a launch row whose tasks
// differ from the revision's, and a revision without people leave their
// rows as they were.
func TestAnOlderBuildsLaunchLosesNoPeople(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Members = append(c.Members, crew.Member{Name: "writer", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}})
		c.Tasks = append(c.Tasks, crew.Task{ID: "docs", Member: "writer", Kind: crew.TaskWork, Instructions: "Write it.", Needs: []string{"build"}})
		c.People = crewPeople()
		c.Tasks[0].Owner = "nate"
	})
	ctx := t.Context()
	ld := l.srv.ledger
	pkg := string(l.pendingLaunch(t, dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID()}).Package)
	put := func(id, crewJSON, pkg string) {
		t.Helper()
		if _, err := ld.Transition(ctx, id, func(tx *ledger.Tx) error {
			return tx.CreateRun(ledger.RunRow{ID: id, CrewID: l.crew.ID, Crew: crewJSON, Base: l.head, State: ledger.RunStopped, Ticket: l.ticketID, Package: pkg, CreatedAt: tx.Now(), ClosedAt: tx.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	revisionOf := func(id string) int64 {
		t.Helper()
		row, ok, err := ld.Run(ctx, id)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", id, ok, err)
		}
		return row.Revision
	}
	repackage := func(edit func(plan map[string]any)) string {
		var p map[string]any
		if err := json.Unmarshal([]byte(pkg), &p); err != nil {
			t.Fatal(err)
		}
		edit(p["plan"].(map[string]any))
		b, _ := json.Marshal(p)
		return string(b)
	}
	older := l.crew
	older.Cwd = l.repo
	runID := l.crew.ID + "-0000000a"
	put(runID, olderCrew(older), pkg)
	// Its record as the older build wrote it.
	from, _, err := crew.SQLLedger(ld).Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.srv.records.Save(olderRecord(crew.Recovered(crew.Run{}, from))); err != nil {
		t.Fatal(err)
	}
	// A digest that is not the package's.
	badDigest := l.crew.ID + "-0000000b"
	put(badDigest, olderCrew(older), repackage(func(plan map[string]any) { plan["digest"] = strings.Repeat("0", 64) }))
	// A launch row whose tasks are not the revision's.
	otherTasks := older
	otherTasks.Tasks = append([]crew.Task(nil), older.Tasks...)
	otherTasks.Tasks[1].ID = "site"
	badTasks := l.crew.ID + "-0000000c"
	put(badTasks, olderCrew(otherTasks), pkg)
	// A revision without people.
	plain := teamCrew("Plain", "ready-cat")
	plainSaved := l.saveCrew(t, plain)
	plainPlan := l.proposed(t, plainSaved.ID, nil)
	pv, err := l.srv.dispatch.Plan(ctx, plainPlan)
	if err != nil {
		t.Fatal(err)
	}
	plainRun := l.crew.ID + "-0000000d"
	put(plainRun, olderCrew(older), repackage(func(plan map[string]any) { plan["id"], plan["digest"] = plainPlan, pv.Digest }))
	revs := map[string]int64{}
	for _, id := range []string{badDigest, badTasks, plainRun} {
		revs[id] = revisionOf(id)
	}
	// The next start: the recovery's restore, then the records.
	l.srv.restoreLaunchPeople(ctx, ld)
	syncRecords(ctx, ld, l.srv.records, l.srv.log)
	row, _, _ := ld.Run(ctx, runID)
	if !crew.LaunchHasPeople(row.Crew) || !strings.Contains(row.Crew, `"owner":"nate"`) {
		t.Fatalf("the launch row was not made whole: %s", row.Crew)
	}
	wantRunPeople(t, "the launch restored", runOf(t, l.testEnv, runID))
	for id, rev := range revs {
		row, _, _ := ld.Run(ctx, id)
		if crew.LaunchHasPeople(row.Crew) || row.Revision != rev {
			t.Fatalf("%s was written: %s at %d, was %d", id, row.Crew, row.Revision, rev)
		}
	}
	// A second start writes nothing more.
	rev := revisionOf(runID)
	l.srv.restoreLaunchPeople(ctx, ld)
	if revisionOf(runID) != rev {
		t.Fatal("a second restore wrote the launch row again")
	}
	next := l.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)
	wantRunPeople(t, "the continuation", next)
	if row, _, _ := ld.Run(ctx, next["id"].(string)); !crew.LaunchHasPeople(row.Crew) {
		t.Fatalf("the continuation's row: %s", row.Crew)
	}
}

// GET /api/people is the union of Dispatch's directory and the crews'
// people: a name in Dispatch only, in a crew only, in both, each once
// with its crews; Dispatch unavailable, the crews' people all the same
// and dispatchUnavailable.
func TestPeopleIsTheUnionOfTheStores(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, name := range []string{"ana", "zed"} {
		e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": name}, http.StatusCreated, "")
	}
	c := crew.Crew{Name: "API", Where: crew.WhereServer, Isolation: crew.IsolationNone, Members: []crew.Member{{Name: "lead", AgentID: "claude", Start: crew.Start{When: crew.StartImmediately}}}, People: crewPeople()}
	saved, err := e.srv.crews.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	out := e.expect(t, "GET", "/api/people", nil, http.StatusOK, "")
	b, _ := json.Marshal(out["people"])
	var got []knownPerson
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := []knownPerson{
		{Name: "ana", Dispatch: true, Crews: []personCrew{{ID: saved.ID, Name: "API", Responsibilities: []string{crew.RespAcceptance}}}},
		{Name: "nate", Crews: []personCrew{{ID: saved.ID, Name: "API", Responsibilities: []string{crew.RespArchitecture}}}},
		{Name: "zed", Dispatch: true, Crews: []personCrew{}},
	}
	if !reflect.DeepEqual(got, want) || out["next"] != nil || out["dispatchUnavailable"] != nil {
		t.Fatalf("people: %+v\n%v", got, out)
	}
	// Neither store is written: Dispatch's directory still lacks nate.
	names, err := e.srv.dispatch.People(t.Context())
	if err != nil || len(names) != 2 {
		t.Fatalf("Dispatch's directory: %v %v", names, err)
	}
	if err := e.srv.dispatch.Close(); err != nil {
		t.Fatal(err)
	}
	out = e.expect(t, "GET", "/api/people", nil, http.StatusOK, "")
	if got, _ := json.Marshal(out["people"]); out["dispatchUnavailable"] != true || !strings.Contains(string(got), `"name":"nate"`) || strings.Contains(string(got), "zed") {
		t.Fatalf("Dispatch unavailable: %s %v", got, out)
	}
}

// The names come in pages of at most 200 with next; q keeps those that
// contain it, ignoring case; a person's crews are at most 20 with
// crewsMore; bad parameters are 400.
func TestPeopleIsPaged(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	for i := range 201 {
		if rc, err := e.srv.dispatch.AddPerson(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID()}, fmt.Sprintf("p%03d", i)); err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("add: %+v %v", rc, err)
		}
	}
	first := e.expect(t, "GET", "/api/people", nil, http.StatusOK, "")
	if n := len(first["people"].([]any)); n != 200 || first["next"] != "p199" {
		t.Fatalf("the first page: %d, next %v", n, first["next"])
	}
	second := e.expect(t, "GET", "/api/people?after=p199", nil, http.StatusOK, "")
	if p := second["people"].([]any); len(p) != 1 || p[0].(map[string]any)["name"] != "p200" || second["next"] != nil {
		t.Fatalf("the second page: %v", second)
	}
	small := e.expect(t, "GET", "/api/people?limit=2&after=p010", nil, http.StatusOK, "")
	if p := small["people"].([]any); len(p) != 2 || p[0].(map[string]any)["name"] != "p011" || small["next"] != "p012" {
		t.Fatalf("limit 2: %v", small)
	}
	q := e.expect(t, "GET", "/api/people?q=P19", nil, http.StatusOK, "")
	if p := q["people"].([]any); len(p) != 10 || p[0].(map[string]any)["name"] != "p190" {
		t.Fatalf("q: %v", q)
	}
	for i := range 21 {
		c := crew.Crew{Name: fmt.Sprintf("Crew %02d", i), Where: crew.WhereServer, Isolation: crew.IsolationNone, Members: []crew.Member{{Name: "lead", AgentID: "claude", Start: crew.Start{When: crew.StartImmediately}}}, People: []crew.Person{{Name: "nate"}}}
		if _, err := e.srv.crews.Create(c); err != nil {
			t.Fatal(err)
		}
	}
	nate := e.expect(t, "GET", "/api/people?q=nate", nil, http.StatusOK, "")["people"].([]any)
	if len(nate) != 1 || len(nate[0].(map[string]any)["crews"].([]any)) != 20 || nate[0].(map[string]any)["crewsMore"] != true {
		t.Fatalf("a person in 21 crews: %v", nate)
	}
	for _, query := range []string{"limit=0x", "limit=201", "q=" + strings.Repeat("q", 61), "after=" + strings.Repeat("a", 61), "other=1", "q=a&q=b"} {
		e.expect(t, "GET", "/api/people?"+query, nil, http.StatusBadRequest, "invalid_request")
	}
	if resp, _ := e.do("GET", "/api/people", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

// A Brakeman draft's crew goes through the one conversion: its check
// runs checkPeople (refused with its words), and making it keeps its
// people and owners in the crew's file.
func TestADraftsPeopleAreCheckedAndKept(t *testing.T) {
	e := readyEnv(t, nil)
	draft := func(people []crew.Person, owner string) json.RawMessage {
		c := managedCrew(e.root)
		c.Tasks[0].Owner = owner
		b, _ := json.Marshal(map[string]any{"name": "Draft", "goal": c.Goal, "cwd": c.Cwd, "where": c.Where, "isolation": c.Isolation, "members": c.Members, "tasks": c.Tasks, "people": people})
		return b
	}
	twice := crewPeople()
	twice[1].Responsibilities = []string{crew.RespArchitecture}
	if err := (brakemanFacts{s: e.srv}).CheckCrew(draft(twice, "")); err == nil || !strings.Contains(err.Error(), "architecture is nate's already") {
		t.Fatalf("a draft whose people break checkPeople: %v", err)
	}
	good := draft(crewPeople(), "nate")
	if err := (brakemanFacts{s: e.srv}).CheckCrew(good); err != nil {
		t.Fatalf("a good draft: %v", err)
	}
	c, err := crewOfBody(good)
	if err != nil {
		t.Fatal(err)
	}
	made, _, err := e.srv.crews.CreateFrom(c, "brakeman:br-1/0/op-1")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(e.srv.cfg.DataDir, "crews", made.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var kept crew.Crew
	if err := json.Unmarshal(file, &kept); err != nil || !reflect.DeepEqual(kept.People, crewPeople()) || kept.Tasks[0].Owner != "nate" {
		t.Fatalf("the made crew's file: %s %v", file, err)
	}
}
