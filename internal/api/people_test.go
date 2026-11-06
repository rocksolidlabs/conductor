package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
// so written. The upgrade, a real restart (the first server ended, a
// second over the same data): its start makes the launch row whole
// (restoreLaunchPeople, in the ledger's recovery) and then the record
// (syncRecords), the run routes answer the people and owners, and a
// continuation carries them in its own row. A revision whose digest is
// not the package's, a launch row whose tasks differ from the revision's,
// and a revision without people leave their rows and their records as
// they were, without people.
func TestAnOlderBuildsLaunchLosesNoPeople(t *testing.T) {
	l := newLaunchTest(t, nil, peopledLaunch)
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
	// Each row's record as the older build wrote it.
	record := func(id string) {
		t.Helper()
		from, _, err := crew.SQLLedger(ld).Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.srv.records.Save(olderRecord(crew.Recovered(crew.Run{}, from))); err != nil {
			t.Fatal(err)
		}
	}
	put(runID, olderCrew(older), pkg)
	record(runID)
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
		record(id)
		revs[id] = revisionOf(id)
	}
	// The next start: the first server ended, a second over the same data
	// directory, whose ledger's recovery restores, then syncs the records.
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(sctx)
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	if ro, why := again.srv.ledger.ReadOnly(); ro {
		t.Fatalf("the second server's ledger is read-only: %s", why)
	}
	ld = again.srv.ledger
	row, _, _ := ld.Run(ctx, runID)
	if !crew.LaunchHasPeople(row.Crew) || !strings.Contains(row.Crew, `"owner":"nate"`) {
		t.Fatalf("the launch row was not made whole: %s", row.Crew)
	}
	wantRunPeople(t, "the launch restored", runOf(t, again, runID))
	for id, rev := range revs {
		row, _, _ := ld.Run(ctx, id)
		if crew.LaunchHasPeople(row.Crew) || row.Revision != rev {
			t.Fatalf("%s was written: %s at %d, was %d", id, row.Crew, row.Revision, rev)
		}
		if run := runOf(t, again, id); run["people"] != nil {
			t.Fatalf("%s's record got people: %v", id, run["people"])
		}
	}
	// A second start writes nothing more.
	rev := revisionOf(runID)
	again.srv.restoreLaunchPeople(ctx, ld)
	if revisionOf(runID) != rev {
		t.Fatal("a second restore wrote the launch row again")
	}
	next := again.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, http.StatusCreated, "")["run"].(map[string]any)
	wantRunPeople(t, "the continuation", next)
	if row, _, _ := ld.Run(ctx, next["id"].(string)); !crew.LaunchHasPeople(row.Crew) {
		t.Fatalf("the continuation's row: %s", row.Crew)
	}
}

// peopledLaunch shapes a launchTest's crew as peopledCrew: build, then
// docs, with crewPeople, build owned by nate.
func peopledLaunch(c *crew.Crew) {
	c.Members = append(c.Members, crew.Member{Name: "writer", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}})
	c.Tasks = append(c.Tasks, crew.Task{ID: "docs", Member: "writer", Kind: crew.TaskWork, Instructions: "Write it.", Needs: []string{"build"}})
	c.People = crewPeople()
	c.Tasks[0].Owner = "nate"
}

// While the server takes the ledger over (its owner lock taken late: the
// ledger writable, the one OnOwned callback's recovery, restore of the
// launches' people and reconcile not all done), a managed run's
// continuation is 503 ledger_unavailable and makes no run, as it is while
// another server holds the lock: one made then would keep a launch row
// not yet made whole in its live state and its task files. Once taken
// over, the continuation is made, from the launch row made whole.
func TestNoContinuationWhileTheLedgerIsTakenOver(t *testing.T) {
	var other *ledger.Ledger
	l := newLaunchTest(t, lateLedger(t, &other), peopledLaunch)
	ctx := t.Context()
	pkg := string(l.pendingLaunch(t, dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID()}).Package)
	older := l.crew
	older.Cwd = l.repo
	runID := l.crew.ID + "-0000000a"
	// The launch row and the record an older build wrote, in the ledger
	// the other server holds.
	if _, err := other.Transition(ctx, runID, func(tx *ledger.Tx) error {
		return tx.CreateRun(ledger.RunRow{ID: runID, CrewID: l.crew.ID, Crew: olderCrew(older), Base: l.head, State: ledger.RunStopped, Ticket: l.ticketID, Package: pkg, CreatedAt: tx.Now(), ClosedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	from, _, err := crew.SQLLedger(other).Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.srv.records.Save(olderRecord(crew.Recovered(crew.Run{}, from))); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	l.srv.launches.setSeams(func(g *launchGate) {
		g.beforeReconcile = func() {
			close(reached)
			<-release
		}
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	resume := func(status int, code string) map[string]any {
		t.Helper()
		return l.expect(t, "POST", "/api/runs/"+runID+"/resume", nil, status, code)
	}
	resume(http.StatusServiceUnavailable, "ledger_unavailable")
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	<-reached
	if ro, why := l.srv.ledger.ReadOnly(); ro {
		t.Fatalf("the ledger is read-only at the reconcile: %s", why)
	}
	if msg := resume(http.StatusServiceUnavailable, "ledger_unavailable")["error"].(map[string]any)["message"]; msg != "the server is taking over the ledger" {
		t.Fatalf("the refusal: %v", msg)
	}
	if row, _, err := l.srv.ledger.Run(ctx, runID); err != nil || row.ContinuedBy != "" {
		t.Fatalf("a continuation while the ledger was taken over: %+v %v", row, err)
	}
	close(release)
	waitUntil(t, "launches admitted", l.srv.launches.ready.Load)
	next := resume(http.StatusCreated, "")["run"].(map[string]any)
	wantRunPeople(t, "the continuation", next)
	if row, _, _ := l.srv.ledger.Run(ctx, next["id"].(string)); !crew.LaunchHasPeople(row.Crew) {
		t.Fatalf("the continuation's row: %s", row.Crew)
	}
}

// A resume from a record that read the record before a late takeover
// rewrote it (held at afterRecordRead, after its read and before its
// admission) marks the record as it is on disk once the continuation is
// made, never the snapshot it read: the people and owners syncRecords
// restored stay, and the mark is there. Two takeovers: the launch row
// whole and the record an older build's at its revision (syncRecords
// writes it again at that same revision, which an outdated mark would
// overwrite), and the launch row an older build's too (its restore moves
// the revision, which an outdated mark's save would be dropped behind,
// the mark with it).
func TestAResumeHeldOverATakeoverKeepsTheRestoredRecord(t *testing.T) {
	for _, whole := range []bool{true, false} {
		t.Run(fmt.Sprintf("launch row whole %v", whole), func(t *testing.T) {
			var other *ledger.Ledger
			l := newLaunchTest(t, lateLedger(t, &other), peopledLaunch)
			ctx := t.Context()
			pkg := string(l.pendingLaunch(t, dispatch.Op{Actor: dispatch.ActorAdmin, ID: opID()}).Package)
			launched := l.crew
			launched.Cwd = l.repo
			var crewJSON string
			if whole {
				b, err := json.Marshal(launched)
				if err != nil {
					t.Fatal(err)
				}
				crewJSON = string(b)
			} else {
				crewJSON = olderCrew(launched)
			}
			runID := l.crew.ID + "-0000000a"
			if _, err := other.Transition(ctx, runID, func(tx *ledger.Tx) error {
				return tx.CreateRun(ledger.RunRow{ID: runID, CrewID: l.crew.ID, Crew: crewJSON, Base: l.head, State: ledger.RunStopped, Ticket: l.ticketID, Package: pkg, CreatedAt: tx.Now(), ClosedAt: tx.Now()})
			}); err != nil {
				t.Fatal(err)
			}
			from, _, err := crew.SQLLedger(other).Load(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.srv.records.Save(olderRecord(crew.Recovered(crew.Run{}, from))); err != nil {
				t.Fatal(err)
			}
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			l.srv.launches.setSeams(func(g *launchGate) {
				g.afterRecordRead = func(id string) {
					if id != runID {
						return
					}
					once.Do(func() { close(reached) })
					<-release
				}
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			type answer struct {
				status int
				body   map[string]any
				err    error
			}
			answered := make(chan answer, 1)
			go func() {
				req, _ := http.NewRequest("POST", l.http.URL+"/api/runs/"+runID+"/resume", nil)
				req.Header.Set("Authorization", "Bearer "+adminToken)
				resp, err := l.client.Do(req)
				if err != nil {
					answered <- answer{err: err}
					return
				}
				defer resp.Body.Close()
				var out map[string]any
				err = json.NewDecoder(resp.Body).Decode(&out)
				answered <- answer{status: resp.StatusCode, body: out, err: err}
			}()
			<-reached
			// The resume holds the record an older build wrote; the takeover
			// rewrites it with the people and owners meanwhile.
			if err := other.Close(); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "the ledger taken over", l.srv.launches.ready.Load)
			wantRunPeople(t, "the record the takeover restored", runOf(t, l.testEnv, runID))
			close(release)
			a := <-answered
			if a.err != nil || a.status != http.StatusCreated {
				t.Fatalf("the resume: %d %v %v", a.status, a.body, a.err)
			}
			cont := a.body["run"].(map[string]any)["id"].(string)
			t.Cleanup(func() { stopEveryRun(l.srv) })
			rec, ok, err := l.srv.records.Get(runID)
			if err != nil || !ok || rec.ResumedBy != cont {
				t.Fatalf("the record's mark: %q, want %q (%v %v)", rec.ResumedBy, cont, ok, err)
			}
			src := runOf(t, l.testEnv, runID)
			wantRunPeople(t, "the source after its mark", src)
			if src["resumedBy"] != cont {
				t.Fatalf("the source's resumedBy: %v", src["resumedBy"])
			}
		})
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
	for _, query := range []string{"limit=0", "limit=0x", "limit=201", "q=" + strings.Repeat("q", 61), "after=" + strings.Repeat("a", 61), "other=1", "q=a&q=b"} {
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

// A person's work (PR-11): the tasks they own in two runs, newest first,
// one owned by the fallback to the acceptance owner; the tickets they own
// and are assigned, counted; a person removed from the saved crew after
// the launch still answered with that run's work; a name in Dispatch's
// directory alone answered; 404 for a name no source knows, matched
// exactly; and the bound of 200 tasks with more.
func TestAPersonsWork(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	saved, err := e.srv.crews.Create(peopledCrew(e.root))
	if err != nil {
		t.Fatal(err)
	}
	older := launchPeopled(t, e, saved)
	newer, release, err := e.srv.runs.LaunchNamed(t.Context(), saved, "second")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, newer.ID, "build", 1)
	defer e.expect(t, "POST", "/api/runs/"+newer.ID+"/stop", nil, http.StatusOK, "")

	e.newDispatchProject(t, "PPL", nil)
	e.newDispatchTicket(t, "PPL", "owned", map[string]any{"owner": "nate"})
	e.newDispatchTicket(t, "PPL", "assigned", map[string]any{"assignees": []string{"person:nate"}})
	e.newDispatchTicket(t, "PPL", "both", map[string]any{"owner": "nate", "assignees": []string{"person:nate"}})
	e.newDispatchTicket(t, "PPL", "another's", map[string]any{"owner": "ana2"})

	work := func(name string) personWork {
		t.Helper()
		out := e.expect(t, "GET", "/api/people/"+url.PathEscape(name), nil, http.StatusOK, "")
		b, _ := json.Marshal(out)
		var w personWork
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	tasksOf := func(w personWork) []string {
		var out []string
		for _, x := range w.Tasks {
			out = append(out, x.Run+"/"+x.Task+"/"+x.Member+"/"+x.OwnerFrom)
		}
		return out
	}

	nate := work("nate")
	if want := []string{newer.ID + "/build/lead/task", older + "/build/lead/task"}; !reflect.DeepEqual(tasksOf(nate), want) || nate.More {
		t.Fatalf("nate's tasks: %v, want %v", tasksOf(nate), want)
	}
	if nate.Tasks[0].RunLabel != "second" || nate.Tasks[1].RunLabel != saved.Name || nate.Tasks[0].State == "" {
		t.Fatalf("nate's tasks: %+v", nate.Tasks)
	}
	if want := []personCrew{{ID: saved.ID, Name: saved.Name, Responsibilities: []string{crew.RespArchitecture}}}; !reflect.DeepEqual(nate.Crews, want) || nate.CrewsMore {
		t.Fatalf("nate's crews: %+v", nate.Crews)
	}
	tk := nate.Tickets
	if tk == nil || tk.Owner != 2 || tk.Assignee != 2 || tk.OwnerHref != "/api/dispatch/tickets?owner=nate" || tk.AssigneeHref != "/api/dispatch/tickets?assignee=person%3Anate" {
		t.Fatalf("nate's tickets: %+v", tk)
	}
	// Naming nate a ticket's owner put him in Dispatch's directory.
	if !nate.Dispatch || nate.DispatchUnavailable || nate.Decisions == nil || len(nate.Decisions) != 0 {
		t.Fatalf("nate: %+v", nate)
	}
	// The links are Dispatch's own lists, which answer the counted tickets.
	if got := e.expect(t, "GET", tk.AssigneeHref, nil, http.StatusOK, "")["total"]; got != 2.0 {
		t.Fatalf("the assignee list: %v", got)
	}

	// ana leaves the saved crew; the runs launched with her keep her
	// tasks, docs hers as the acceptance owner.
	edited := saved
	edited.People = []crew.Person{{Name: "nate", Responsibilities: []string{crew.RespArchitecture}}}
	if _, err := e.srv.crews.Update(saved.ID, edited); err != nil {
		t.Fatal(err)
	}
	ana := work("ana")
	if want := []string{newer.ID + "/docs/writer/acceptance", older + "/docs/writer/acceptance"}; !reflect.DeepEqual(tasksOf(ana), want) {
		t.Fatalf("ana's tasks: %v, want %v", tasksOf(ana), want)
	}
	if len(ana.Crews) != 0 || ana.Dispatch || ana.Tickets == nil || ana.Tickets.Owner != 0 || ana.Tickets.Assignee != 0 {
		t.Fatalf("ana: %+v", ana)
	}

	// A name in Dispatch's directory alone, and one a ticket alone names.
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "zed"}, http.StatusCreated, "")
	if zed := work("zed"); !zed.Dispatch || len(zed.Tasks) != 0 || len(zed.Crews) != 0 {
		t.Fatalf("zed: %+v", zed)
	}
	if ana2 := work("ana2"); ana2.Tickets == nil || ana2.Tickets.Owner != 1 {
		t.Fatalf("ana2: %+v", ana2)
	}

	// No source knows these: the name is matched exactly.
	for _, name := range []string{"nobody", "NATE", "nat", strings.Repeat("n", 61), "na\tte"} {
		e.expect(t, "GET", "/api/people/"+url.PathEscape(name), nil, http.StatusNotFound, "not_found")
	}
	e.expect(t, "GET", "/api/people/nate?x=1", nil, http.StatusBadRequest, "invalid_request")
	if resp, _ := e.do("GET", "/api/people/nate", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}

	// 256 tasks owned in two records: the 200 of the newest first, more.
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, id := range []string{"bulk-0000000a", "bulk-0000000b"} {
		run := crew.Run{ID: id, CrewID: "bulk", Name: "bulk", Cwd: e.root, State: crew.RunStopped, StartedAt: at.Add(time.Duration(i) * time.Hour), Managed: &crew.Managed{Revision: 1},
			People: []crew.Person{{Name: "kai"}}}
		for n := range 128 {
			run.Tasks = append(run.Tasks, crew.TaskState{ID: fmt.Sprintf("t%03d", n), Member: "lead", State: ledger.TaskReady, Owner: "kai", OwnerFrom: crew.OwnerFromTask})
		}
		if err := e.srv.records.Save(run); err != nil {
			t.Fatal(err)
		}
	}
	kai := work("kai")
	if len(kai.Tasks) != maxWorkTasks || !kai.More || kai.Tasks[0].Run != "bulk-0000000b" || kai.Tasks[0].Task != "t000" || kai.Tasks[127].Run != "bulk-0000000b" || kai.Tasks[128].Run != "bulk-0000000a" || kai.Tasks[199].Task != "t071" {
		t.Fatalf("kai: %d tasks, more %v, %+v", len(kai.Tasks), kai.More, kai.Tasks[:2])
	}
}

// personSegment is the app's path segment for a person's name
// (web/app/utils/people.ts, personSegment): a "~" before "." and "..",
// which a URL resolves as steps of its path, and before a name that starts
// with "~"; the name escaped as a segment.
func personSegment(name string) string {
	if name == "." || name == ".." || strings.HasPrefix(name, "~") {
		name = "~" + name
	}
	return url.PathEscape(name)
}

// Names that are also path words reach their page through one rule: ".",
// "..", a name with a slash or a percent sign, one that starts with "~";
// "~zed" is zed's. The crews of a page are at most 100, with crewsMore.
func TestAPersonsPathAndBounds(t *testing.T) {
	e := newTestEnv(t, nil)
	names := []string{".", "..", "a/b", "50% c", "~x", "zed"}
	for _, name := range names {
		e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": name}, http.StatusCreated, "")
	}
	for _, name := range names {
		out := e.expect(t, "GET", "/api/people/"+personSegment(name), nil, http.StatusOK, "")
		if out["name"] != name || out["dispatch"] != true {
			t.Fatalf("%q: %v", name, out)
		}
	}
	if out := e.expect(t, "GET", "/api/people/~zed", nil, http.StatusOK, ""); out["name"] != "zed" {
		t.Fatalf("~zed: %v", out)
	}
	// "~x" written without its "~" is the name "x", which no one has.
	e.expect(t, "GET", "/api/people/x", nil, http.StatusNotFound, "not_found")
	e.expect(t, "GET", "/api/people/~", nil, http.StatusNotFound, "not_found")

	for i := range 101 {
		c := crew.Crew{Name: fmt.Sprintf("Crew %03d", i), Where: crew.WhereServer, Isolation: crew.IsolationNone, Members: []crew.Member{{Name: "lead", AgentID: "claude", Start: crew.Start{When: crew.StartImmediately}}}, People: []crew.Person{{Name: "kai", Responsibilities: []string{crew.RespProduct}}}}
		if _, err := e.srv.crews.Create(c); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(e.expect(t, "GET", "/api/people/kai", nil, http.StatusOK, ""))
	var kai personWork
	if err := json.Unmarshal(b, &kai); err != nil {
		t.Fatal(err)
	}
	if len(kai.Crews) != maxWorkCrews || !kai.CrewsMore || kai.Crews[0].Responsibilities[0] != crew.RespProduct {
		t.Fatalf("kai in 101 crews: %d crews, more %v", len(kai.Crews), kai.CrewsMore)
	}
}

// failingLists is Dispatch's store whose lists cannot be read.
type failingLists struct{ *dispatch.Store }

func (failingLists) List(context.Context, dispatch.Query) (dispatch.Page, error) {
	return dispatch.Page{}, fmt.Errorf("the lists cannot be read")
}

// A source that cannot be read never makes a person no one's: with
// Dispatch's lists failing, its directory still says zed is there; with
// Dispatch closed, zed and a name nobody has are answered with
// dispatchUnavailable and no tickets; with the run records unreadable, a
// person whose only work is in a record is answered with
// recordsUnavailable, the runs the engine holds listed all the same.
func TestAPersonsWorkWhenASourceCannotBeRead(t *testing.T) {
	e := readyEnv(t, nil)
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "zed"}, http.StatusCreated, "")
	if tk, in, err := personTicketsOf(t.Context(), failingLists{e.srv.dispatch}, "zed"); tk != nil || !in || err == nil {
		t.Fatalf("the lists failing: %+v %v %v", tk, in, err)
	}

	// kai's only work is in a record.
	run := crew.Run{ID: "kai-0000000a", CrewID: "kai", Name: "kai's", Cwd: e.root, State: crew.RunStopped, StartedAt: time.Now(), Managed: &crew.Managed{Revision: 1},
		Tasks: []crew.TaskState{{ID: "t1", Member: "lead", State: ledger.TaskReady, Owner: "kai", OwnerFrom: crew.OwnerFromTask}}}
	if err := e.srv.records.Save(run); err != nil {
		t.Fatal(err)
	}
	work := func(name string) map[string]any {
		t.Helper()
		return e.expect(t, "GET", "/api/people/"+url.PathEscape(name), nil, http.StatusOK, "")
	}
	if kai := work("kai"); len(kai["tasks"].([]any)) != 1 || kai["recordsUnavailable"] != nil {
		t.Fatalf("kai: %v", kai)
	}
	if os.Geteuid() == 0 {
		t.Log("root reads a directory whatever its mode: the unreadable records are not tried")
	} else {
		// The records' directory listed by no one, its files still opened by
		// name (the ledger beside them).
		runs := filepath.Join(e.srv.cfg.DataDir, "runs")
		if err := os.Chmod(runs, 0o300); err != nil {
			t.Fatal(err)
		}
		kai := work("kai")
		nobody := work("nobody")
		if err := os.Chmod(runs, 0o700); err != nil {
			t.Fatal(err)
		}
		if len(kai["tasks"].([]any)) != 0 || kai["recordsUnavailable"] != true || nobody["recordsUnavailable"] != true {
			t.Fatalf("the records unreadable: kai %v, nobody %v", kai, nobody)
		}
		e.expect(t, "GET", "/api/people/nobody", nil, http.StatusNotFound, "not_found")
	}

	if err := e.srv.dispatch.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zed", "nobody"} {
		out := work(name)
		if out["dispatchUnavailable"] != true || out["tickets"] != nil || out["dispatch"] != false || len(out["tasks"].([]any)) != 0 || len(out["crews"].([]any)) != 0 {
			t.Fatalf("%s with Dispatch closed: %v", name, out)
		}
	}
}
