package crew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// brakemanCrew is a Brakeman turn's crew, as the desk launches one: one
// member, never yolo, never managed.
func brakemanCrew() Crew {
	no := false
	c := testCrew(immediate("brakeman", "Brakeman request br-1 (briefing): read it."))
	c.ID, c.Name, c.Goal, c.Yolo = "brakeman", "Brakeman", "Brakeman request br-1", &no
	return c
}

// LaunchNamedWith stamps the run with its class and request before its
// member starts: the Launcher is handed both in the member's spec, and
// the run, its record among its reads, carries them. LaunchNamed stamps
// none, and an option of the wrong shape, or a turn whose crew is not
// Brakeman's id, refuses the launch before anything is made.
func TestLaunchNamedWithStampsClassAndRequest(t *testing.T) {
	e, fl := newEngine(t)
	var specs []LaunchSpec
	l := &specLauncher{fakeLauncher: fl, seen: func(s LaunchSpec) { specs = append(specs, s) }}
	e.launcher = l
	run, release, err := e.LaunchNamedWith(t.Context(), brakemanCrew(), "Brakeman · briefing br-1", LaunchOpts{Class: ClassBrakeman, Request: "br-1"})
	release()
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Class != ClassBrakeman || specs[0].Request != "br-1" {
		t.Fatalf("the launcher was handed %+v", specs)
	}
	if run.Class != ClassBrakeman || run.Request != "br-1" || run.Label != "Brakeman · briefing br-1" {
		t.Fatalf("run %+v", run)
	}
	got, _ := e.Get(run.ID)
	if got.Class != ClassBrakeman || got.Request != "br-1" {
		t.Fatalf("the run as read %+v", got)
	}
	// A person's run: no class, no request, the spec's empty too.
	plain, release, err := e.LaunchNamed(t.Context(), testCrew(immediate("lead", "go")), "")
	release()
	if err != nil || plain.Class != "" || plain.Request != "" || specs[1].Class != "" || specs[1].Request != "" {
		t.Fatalf("a person's run %+v %v, spec %+v", plain, err, specs[1])
	}
	for _, o := range []LaunchOpts{{Class: ClassBrakeman}, {Class: ClassBrakeman, Request: "br-0"}, {Request: "br-1"}, {Class: "other", Request: "br-1"}} {
		if _, release, err := e.LaunchNamedWith(t.Context(), brakemanCrew(), "", o); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v launched: %v", o, err)
		} else {
			release()
		}
	}
	// A turn's crew is Brakeman's own id, so its runs and records are
	// named brakeman-<n> (the records' prune counts them by that name).
	other := brakemanCrew()
	other.ID = "team"
	if _, release, err := e.LaunchNamedWith(t.Context(), other, "", LaunchOpts{Class: ClassBrakeman, Request: "br-1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a turn of crew team launched: %v", err)
	} else {
		release()
	}
	if n := len(specs); n != 2 {
		t.Fatalf("%d launches, a refused one among them", n)
	}
}

// specLauncher is a fakeLauncher that also sees each spec whole.
type specLauncher struct {
	*fakeLauncher
	seen func(LaunchSpec)
}

func (l *specLauncher) Launch(ctx context.Context, spec LaunchSpec) (*session.Local, error) {
	l.seen(spec)
	return l.fakeLauncher.Launch(ctx, spec)
}

// A run of Brakeman's is never resumed and takes no member.
func TestABrakemanRunTakesNoMember(t *testing.T) {
	e, _ := newEngine(t)
	run, release, err := e.LaunchNamedWith(t.Context(), brakemanCrew(), "", LaunchOpts{Class: ClassBrakeman, Request: "br-1"})
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddMember(t.Context(), run.ID, manual("more", "")); !errors.Is(err, ErrBrakemanRun) {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := e.ResumeMember(t.Context(), run.ID, "brakeman", ""); !errors.Is(err, ErrBrakemanRun) {
		t.Fatalf("ResumeMember: %v", err)
	}
	if err := e.Stop(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResumeRun(t.Context(), run.ID); !errors.Is(err, ErrBrakemanRun) {
		t.Fatalf("ResumeRun: %v", err)
	}
	rec, _ := e.Get(run.ID)
	if _, err := e.ResumeRecord(t.Context(), Run{ID: "brakeman-00000001", Class: ClassBrakeman, Request: "br-1", Members: rec.Members}); !errors.Is(err, ErrBrakemanRun) {
		t.Fatalf("ResumeRecord: %v", err)
	}
}

// The engine bounds each class on its own: 101 Brakeman turns beside one
// person's run keep the person's run, and keep at most maxBrakemanRuns
// turns, the oldest forgotten first.
func TestBrakemanRunsEvictNoPersonsRun(t *testing.T) {
	e, _ := newEngine(t)
	person, release, err := e.LaunchNamed(t.Context(), testCrew(manual("lead", "")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	var turns []string
	for i := range 101 {
		c := brakemanCrew()
		c.Members[0].Start = Start{When: StartManual} // nothing runs: each may be forgotten
		run, release, err := e.LaunchNamedWith(t.Context(), c, "", LaunchOpts{Class: ClassBrakeman, Request: fmt.Sprintf("br-%d", i+1)})
		release()
		if err != nil {
			t.Fatal(err)
		}
		turns = append(turns, run.ID)
	}
	if _, ok := e.Get(person.ID); !ok {
		t.Fatal("the person's run was forgotten among Brakeman's turns")
	}
	n := 0
	for _, r := range e.List() {
		if r.Class == ClassBrakeman {
			n++
		}
	}
	if n != maxBrakemanRuns {
		t.Fatalf("%d turns kept, want %d", n, maxBrakemanRuns)
	}
	if _, ok := e.Get(turns[len(turns)-1]); !ok {
		t.Fatal("the newest turn was forgotten")
	}
	if _, ok := e.Get(turns[0]); ok {
		t.Fatal("the oldest turn is still kept")
	}
	// A person's runs fill their own class: the turns do not count.
	old := maxRuns
	maxRuns = 2
	t.Cleanup(func() { maxRuns = old })
	second, release, err := e.LaunchNamed(t.Context(), testCrew(manual("lead", "")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(person.ID); !ok {
		t.Fatal("a person's second run, under its cap of 2, forgot the first")
	}
	if _, ok := e.Get(second.ID); !ok {
		t.Fatal("the second run is not kept")
	}
}

// The records bound each class on their own: past maxBrakemanRecords the
// oldest turn's record goes, and no person's record does, however many
// turns there are.
func TestBrakemanRecordsArePrunedApart(t *testing.T) {
	rs, dir := newRecords(t)
	base := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	for i := range 3 {
		r := Run{ID: fmt.Sprintf("team-%08x", i), CrewID: "team", Name: "team", StartedAt: base.Add(time.Duration(i) * time.Minute), State: RunFinished, Members: []MemberState{}, Log: []session.ActivityEntry{}}
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	for i := range maxBrakemanRecords + 5 {
		r := Run{ID: fmt.Sprintf("brakeman-%08x", i), CrewID: "brakeman", Name: "Brakeman", Class: ClassBrakeman, Request: fmt.Sprintf("br-%d", i+1),
			StartedAt: base.Add(time.Hour + time.Duration(i)*time.Minute), State: RunStopped, Members: []MemberState{}, Log: []session.ActivityEntry{}}
		if err := rs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3+maxBrakemanRecords {
		t.Fatalf("%d records kept, want the person's 3 and %d turns", len(entries), maxBrakemanRecords)
	}
	for i := range 3 {
		if _, ok, _ := rs.Get(fmt.Sprintf("team-%08x", i)); !ok {
			t.Fatalf("a person's record %d was pruned for the turns", i)
		}
	}
	for i := range 5 {
		if _, ok, _ := rs.Get(fmt.Sprintf("brakeman-%08x", i)); ok {
			t.Fatalf("turn %d, among the oldest, kept", i)
		}
	}
	got, ok, _ := rs.Get(fmt.Sprintf("brakeman-%08x", maxBrakemanRecords+4))
	if !ok || got.Class != ClassBrakeman || got.Request != fmt.Sprintf("br-%d", maxBrakemanRecords+5) {
		t.Fatalf("the newest turn's record %+v %v", got, ok)
	}
	list, _ := rs.List("team")
	if !slices.ContainsFunc(list, func(r Run) bool { return r.ID == "team-00000000" }) {
		t.Fatal("the person's oldest record is not listed")
	}
}
