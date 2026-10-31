package crew

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The ticket, plan revision and launch the tests' packages are of.
const (
	testTicketID = "0123456789abcdef"
	testPlanID   = "fedcba9876543210"
	testLaunchID = "00112233aabbccdd"
)

// testPackage is a launch package of run runID at base, encoded indented
// with a trailing newline: bytes no re-encoding would give, so a row that
// holds them was kept as given.
func testPackage(t *testing.T, runID, base string) []byte {
	t.Helper()
	p := Package{
		Version: PackageVersion,
		Ticket:  PackageTicket{ID: testTicketID, Key: "CND-12", Rev: 7, Title: "Fix the login loop", Acceptance: "It logs in.", Status: "open"},
		Plan: PackagePlan{ID: testPlanID, N: 2, Digest: strings.Repeat("d", 64), Crew: PackageCrew{ID: "api-sweep", Name: "API sweep"}, Base: base,
			Agents: []PackageAgent{{ID: "claude", SHA256: strings.Repeat("e", 64)}}},
		Attachments: []PackageAttachment{{ID: "a1", Name: "Spec.png", File: "01-Spec.png", Size: int64(len(testSpecPNG)), SHA256: sha256Hex(testSpecPNG), ContentType: "image/png"}},
		Launch:      PackageLaunch{ID: testLaunchID, Run: runID, Label: "CND-12 Fix the login loop"},
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// planned is a planned launch of run runID at base with testPackage.
func planned(t *testing.T, runID, base string) Planned {
	t.Helper()
	pkg := testPackage(t, runID, base)
	return Planned{RunID: runID, Base: base, Digest: packageDigest(pkg), Package: pkg}
}

// launchPlanned launches c as p plans it and returns its run.
func launchPlanned(t *testing.T, e *Engine, c Crew, p Planned) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run, release, err := e.LaunchPlanned(ctx, c, "CND-12 Fix the login loop", p)
	release()
	if err != nil {
		t.Fatal(err)
	}
	return *run
}

// ledgerState is what the ledger holds of a run: its row, its events, its
// tasks and its attempts, compared whole.
type ledgerState struct {
	Row      ledger.RunRow
	Found    bool
	Events   []ledger.EventRow
	Tasks    []ledger.TaskRow
	Attempts []ledger.AttemptRow
}

func ledgerOf(t *testing.T, ld *ledger.Ledger, runID string) ledgerState {
	t.Helper()
	ctx := context.Background()
	var s ledgerState
	var err error
	if s.Row, s.Found, err = ld.Run(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if s.Events, err = ld.Events(ctx, runID, 0, 10000); err != nil {
		t.Fatal(err)
	}
	if s.Tasks, err = ld.Tasks(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if s.Attempts, err = ld.Attempts(ctx, runID); err != nil {
		t.Fatal(err)
	}
	return s
}

// otherRun writes, through the ledger itself (a handle other than the
// engine's), the row of a run that is not the engine's: another launch's.
func otherRun(t *testing.T, ld *ledger.Ledger, runID string) {
	t.Helper()
	if _, err := ld.Transition(context.Background(), runID, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: runID, CrewID: "api-sweep", Crew: `{"id":"api-sweep","name":"another launch"}`, Base: strings.Repeat("c", 40),
			State: ledger.RunRunning, CreatedAt: tx.Now(), Ticket: "ffffffffffffffff", Package: `{"version":1,"another":"launch"}`}); err != nil {
			return err
		}
		return tx.Event("run.created", map[string]any{"by": "another launch"})
	}); err != nil {
		t.Fatal(err)
	}
}

// head is the commit HEAD names in dir.
func head(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "HEAD")
}

// A planned run starts at the revision's base, whatever HEAD became since:
// its row and run.created say the base, its first attempt starts there,
// and run.created names the ticket and the package's digest. A base that
// is no commit of the repository is refused ErrInvalid before a run
// exists, a crew whose manifest is the file form too (nothing is read at
// the base before it is known to be a commit).
func TestLaunchPlannedStartsAtTheBase(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	writeFile(t, dir+"/README", "moved on\n")
	commitAll(t, dir, "after the revision")
	if head(t, dir) == base {
		t.Fatal("HEAD did not move")
	}
	e, ld := sqlEngine(t)
	c := repoCrew(dir, work("build", "lead"))
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e, c, p)
	if run.ID != p.RunID {
		t.Fatalf("the run is %s, not the planned %s", run.ID, p.RunID)
	}
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if b.Attempt.Start != base {
		t.Fatalf("the first attempt starts at %s, not the revision's base %s", b.Attempt.Start, base)
	}
	got := ledgerOf(t, ld, run.ID)
	if got.Row.Base != base || got.Row.Ticket != testTicketID || got.Row.Package != string(p.Package) {
		t.Fatalf("the run's row: base %s ticket %s package %d bytes", got.Row.Base, got.Row.Ticket, len(got.Row.Package))
	}
	if len(got.Events) == 0 || got.Events[0].Kind != "run.created" {
		t.Fatalf("the run's first event: %+v", got.Events)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(got.Events[0].Payload), &created); err != nil {
		t.Fatal(err)
	}
	if created["base"] != base || created["ticket"] != "CND-12" || created["digest"] != p.Digest {
		t.Fatalf("run.created: %s", got.Events[0].Payload)
	}
	stopRun(t, e, run.ID)

	missing := planned(t, "api-sweep-0000000b", strings.Repeat("0", 40))
	if _, release, err := e.LaunchPlanned(t.Context(), c, "", missing); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not a commit") {
		release()
		t.Fatalf("a base not in the repository: %v", err)
	} else {
		release()
	}
	if s := ledgerOf(t, ld, missing.RunID); s.Found {
		t.Fatalf("a refused launch left a row: %+v", s.Row)
	}

	fdir := newRepo(t)
	commitManifestFile(t, fdir, `{"schema": 1}`)
	if _, release, err := e.LaunchPlanned(t.Context(), fileCrew(fdir, work("build", "lead")), "", missing); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not a commit") {
		release()
		t.Fatalf("a base not in the repository of a file-form manifest's crew: %v", err)
	} else {
		release()
	}
	if s := ledgerOf(t, ld, missing.RunID); s.Found {
		t.Fatalf("a refused launch left a row: %+v", s.Row)
	}
}

// A requested run id is taken once: one the engine holds, one whose row
// the ledger has, and one whose row another ledger handle writes after the
// engine's own check (afterAdd) and before the run's first transaction are
// each refused ErrRunExists, before any attempt, the row there untouched,
// nothing of the refused run recorded or told as forgotten, and the
// browsers, told of it as it launched, told the id changed once the engine
// let go of it, so they read the run that is the id's. So is a launch whose
// stop, between the engine's check and its first transaction, found the
// row there: the refused run is let go, never kept stopped. An id not of
// the crew's shape is ErrInvalid.
func TestARequestedRunIDIsTakenOnce(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	c := repoCrew(dir, work("build", "lead"))

	t.Run("held by the engine", func(t *testing.T) {
		e, ld := sqlEngine(t)
		fl := e.launcher.(*fakeLauncher)
		p := planned(t, "api-sweep-0000000a", base)
		first := launchPlanned(t, e, c, p)
		waitAttempt(t, e, first.ID, "build", ledger.AttemptRunning)
		launches := len(fl.launched())
		_, release, err := e.LaunchPlanned(t.Context(), c, "", p)
		release()
		if !errors.Is(err, ErrRunExists) {
			t.Fatalf("an id the engine holds: %v", err)
		}
		if n := len(fl.launched()); n != launches {
			t.Fatalf("the refused launch started %d sessions", n-launches)
		}
		if r, ok := e.Get(first.ID); !ok || !r.StartedAt.Equal(first.StartedAt) || r.Ticket == nil {
			t.Fatalf("the run held: %+v %v", r, ok)
		}
		if runs, _ := ld.Runs(t.Context()); len(runs) != 1 {
			t.Fatalf("the ledger has %d runs", len(runs))
		}
		stopRun(t, e, first.ID)
	})

	refused := func(t *testing.T, e *Engine, ld *ledger.Ledger, p Planned, before ledgerState, forgotten *[]string) {
		t.Helper()
		fl := e.launcher.(*fakeLauncher)
		_, release, err := e.LaunchPlanned(t.Context(), c, "", p)
		release()
		if !errors.Is(err, ErrRunExists) || !errors.Is(err, ledger.ErrRunExists) {
			t.Fatalf("an id the ledger has: %v", err)
		}
		if n := len(fl.launched()); n != 0 {
			t.Fatalf("the refused launch started %d sessions", n)
		}
		if _, ok := e.Get(p.RunID); ok {
			t.Fatal("the engine kept the refused run")
		}
		if after := ledgerOf(t, ld, p.RunID); !reflect.DeepEqual(after, before) {
			t.Fatalf("the row there changed:\n%+v\nwas\n%+v", after, before)
		}
		if len(*forgotten) != 0 {
			t.Fatalf("the refused run was told as forgotten: %v", *forgotten)
		}
	}
	// changes keeps, for each OnRunChange, the id and whether the engine
	// held a run of it then (OnRunChange runs under e.mu).
	type change struct {
		id   string
		held bool
	}
	var changesMu sync.Mutex
	changes := func(e *Engine) *[]change {
		var got []change
		e.OnRunChange = func(id string) {
			changesMu.Lock()
			got = append(got, change{id, e.runs[id] != nil})
			changesMu.Unlock()
		}
		return &got
	}
	// toldGone holds that the last change told of id came once the
	// engine let go of its refused run.
	toldGone := func(t *testing.T, got *[]change, id string) {
		t.Helper()
		changesMu.Lock()
		defer changesMu.Unlock()
		var last *change
		for i := range *got {
			if (*got)[i].id == id {
				last = &(*got)[i]
			}
		}
		if last == nil || last.held {
			t.Fatalf("the last change told of %s: %+v (all: %+v)", id, last, *got)
		}
	}
	recorded := func(t *testing.T, e *Engine) (*Records, *[]string) {
		rs, _ := newRecords(t)
		e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, nil)
		var mu sync.Mutex
		forgotten := []string{}
		e.OnForget = func(r Run) {
			mu.Lock()
			forgotten = append(forgotten, r.ID)
			mu.Unlock()
		}
		return rs, &forgotten
	}

	t.Run("the ledger has its row", func(t *testing.T) {
		e, ld := sqlEngine(t)
		rs, forgotten := recorded(t, e)
		got := changes(e)
		p := planned(t, "api-sweep-0000000b", base)
		otherRun(t, ld, p.RunID)
		refused(t, e, ld, p, ledgerOf(t, ld, p.RunID), forgotten)
		toldGone(t, got, p.RunID)
		if !e.WaitRecorded(t.Context()) {
			t.Fatal("the records did not settle")
		}
		if _, ok, _ := rs.Get(p.RunID); ok {
			t.Fatal("the refused run's record was written")
		}
	})

	t.Run("stopped before its first transaction", func(t *testing.T) {
		e, ld := sqlEngine(t)
		rs, forgotten := recorded(t, e)
		got := changes(e)
		p := planned(t, "api-sweep-0000000f", base)
		otherRun(t, ld, p.RunID)
		before := ledgerOf(t, ld, p.RunID)
		var stopErr error
		var heldAfterStop bool
		e.afterAdd = func(runID string) {
			// The stop is the run's first transaction: its row is refused,
			// and the stop lets go of the run itself, whatever the launch
			// does next.
			stopErr = e.Stop(t.Context(), runID)
			_, heldAfterStop = e.Get(runID)
		}
		_, release, err := e.LaunchPlanned(t.Context(), c, "", p)
		release()
		e.afterAdd = nil
		if !errors.Is(stopErr, ErrRunExists) || heldAfterStop {
			t.Fatalf("the stop: %v, the run held after it %v", stopErr, heldAfterStop)
		}
		if !errors.Is(err, ErrRunExists) {
			t.Fatalf("a launch stopped before its first transaction, the id the ledger's: %v", err)
		}
		if _, ok := e.Get(p.RunID); ok {
			t.Fatal("the engine kept the refused run, stopped")
		}
		if after := ledgerOf(t, ld, p.RunID); !reflect.DeepEqual(after, before) {
			t.Fatalf("the row there changed:\n%+v\nwas\n%+v", after, before)
		}
		if n := len(e.launcher.(*fakeLauncher).launched()); n != 0 || len(*forgotten) != 0 {
			t.Fatalf("the refused run: %d sessions, told forgotten %v", n, *forgotten)
		}
		toldGone(t, got, p.RunID)
		if !e.WaitRecorded(t.Context()) {
			t.Fatal("the records did not settle")
		}
		if _, ok, _ := rs.Get(p.RunID); ok {
			t.Fatal("the refused run's record was written")
		}
		// Let go, the id is free in the engine: the same launch is
		// refused by the ledger again, not by a run kept.
		refused(t, e, ld, p, before, forgotten)
	})

	t.Run("written after the engine's check", func(t *testing.T) {
		e, ld := sqlEngine(t)
		rs, forgotten := recorded(t, e)
		got := changes(e)
		p := planned(t, "api-sweep-0000000c", base)
		var before ledgerState
		e.afterAdd = func(runID string) {
			// The engine holds the run now and has checked its own map;
			// its first transaction is next.
			otherRun(t, ld, runID)
			before = ledgerOf(t, ld, runID)
		}
		_, release, err := e.LaunchPlanned(t.Context(), c, "", p)
		release()
		e.afterAdd = nil
		if !errors.Is(err, ErrRunExists) {
			t.Fatalf("a row written before the first transaction: %v", err)
		}
		if !before.Found {
			t.Fatal("afterAdd did not run")
		}
		if after := ledgerOf(t, ld, p.RunID); !reflect.DeepEqual(after, before) {
			t.Fatalf("the row there changed:\n%+v\nwas\n%+v", after, before)
		}
		if n := len(e.launcher.(*fakeLauncher).launched()); n != 0 {
			t.Fatalf("the refused launch started %d sessions", n)
		}
		if _, ok := e.Get(p.RunID); ok || len(*forgotten) != 0 {
			t.Fatalf("the refused run: held %v, told forgotten %v", ok, *forgotten)
		}
		toldGone(t, got, p.RunID)
		if !e.WaitRecorded(t.Context()) {
			t.Fatal("the records did not settle")
		}
		if _, ok, _ := rs.Get(p.RunID); ok {
			t.Fatal("the refused run's record was written")
		}
		// The id refused, the same launch is still refused; another is
		// taken.
		refused(t, e, ld, p, before, forgotten)
		run := launchPlanned(t, e, c, planned(t, "api-sweep-0000000d", base))
		waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		stopRun(t, e, run.ID)
	})

	t.Run("not the crew's", func(t *testing.T) {
		e, ld := sqlEngine(t)
		for _, id := range []string{"api-sweep-xyz", "api-sweep-0000000A", "api-sweep", "other-0000000a", "api-sweep-0000000a0"} {
			p := planned(t, "api-sweep-0000000e", base)
			p.RunID = id
			_, release, err := e.LaunchPlanned(t.Context(), c, "", p)
			release()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("the id %q: %v", id, err)
			}
		}
		if runs, _ := ld.Runs(t.Context()); len(runs) != 0 {
			t.Fatalf("refused launches left %d runs", len(runs))
		}
	})
}

// The package is the one its digest names, as given: bytes whose SHA-256
// is not the digest, a package that is not the launch of the run, one of
// another version, one that is not JSON, one with a field the engine's
// reading does not name, one naming an attachment file not of the derived
// shape, one past the bound and one whose plan was approved at another
// base than the one given are each ErrInvalid with no run made; the
// package taken is kept byte for byte.
func TestThePackageIsCheckedAgainstItsDigest(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	c := repoCrew(dir, work("build", "lead"))
	e, ld := sqlEngine(t)
	const id = "api-sweep-0000000a"
	edit := func(f func(*Package)) []byte {
		var p Package
		if err := json.Unmarshal(testPackage(t, id, base), &p); err != nil {
			t.Fatal(err)
		}
		f(&p)
		b, _ := json.Marshal(p)
		return b
	}
	good := planned(t, id, base)
	// unknown is the package with a field of the ticket's under a name the
	// engine's reading does not have, as a builder that named it otherwise
	// would write it.
	unknown := func() []byte {
		var m map[string]any
		if err := json.Unmarshal(good.Package, &m); err != nil {
			t.Fatal(err)
		}
		m["ticket"].(map[string]any)["acceptanceCriteria"] = "It logs in."
		b, _ := json.Marshal(m)
		return b
	}()
	for _, tc := range []struct {
		name  string
		p     Planned
		words string
	}{
		{"another digest", Planned{RunID: id, Base: base, Digest: packageDigest([]byte("other")), Package: good.Package}, "SHA-256"},
		{"no digest", Planned{RunID: id, Base: base, Package: good.Package}, "SHA-256"},
		{"another run's launch", planned(t, "api-sweep-0000000b", base), "not of"},
		{"another version", withPackage(id, base, edit(func(p *Package) { p.Version = 2 })), "version 2"},
		{"not JSON", withPackage(id, base, []byte("{")), "cannot be read"},
		{"a field not read", withPackage(id, base, unknown), "unknown field"},
		{"another base", withPackage(id, base, edit(func(p *Package) { p.Plan.Base = strings.Repeat("1", 40) })), "approved at the base"},
		{"no ticket", withPackage(id, base, edit(func(p *Package) { p.Ticket.ID = "" })), "ticket.id"},
		{"a display name as a file", withPackage(id, base, edit(func(p *Package) { p.Attachments[0].File = "Spec.png" })), "derived name"},
		{"a file ending in a dot", withPackage(id, base, edit(func(p *Package) { p.Attachments[0].File = "01-spec." })), "derived name"},
		{"past the bound", withPackage(id, base, append(good.Package, []byte(strings.Repeat(" ", ledger.MaxPackage))...)), "more than"},
	} {
		if tc.name == "another run's launch" {
			tc.p.RunID = id
		}
		_, release, err := e.LaunchPlanned(t.Context(), c, "", tc.p)
		release()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words) {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if s := ledgerOf(t, ld, id); s.Found {
			t.Fatalf("%s: a row was made", tc.name)
		}
	}
	run := launchPlanned(t, e, c, good)
	if row := ledgerOf(t, ld, id).Row; row.Package != string(good.Package) || packageDigest([]byte(row.Package)) != good.Digest {
		t.Fatalf("the package as kept:\n%s\nas given:\n%s", row.Package, good.Package)
	}
	if run.Ticket == nil || run.Ticket.Digest != good.Digest {
		t.Fatalf("the run's ticket: %+v", run.Ticket)
	}
	stopRun(t, e, run.ID)
}

// withPackage is a planned launch of id at base with the package given and
// its own digest.
func withPackage(id, base string, pkg []byte) Planned {
	return Planned{RunID: id, Base: base, Digest: packageDigest(pkg), Package: pkg}
}

// A planned run's read, its place in the list, its JSON and its record say
// the ticket and plan revision it was launched from; so does the record
// rebuilt from the ledger when the run's own is missing. A run no ticket
// launched has none, and its JSON no ticket key.
func TestRunTicketOnTheRunAndItsRecord(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	c := repoCrew(dir, work("build", "lead"))
	e, ld := sqlEngine(t)
	rs, _ := newRecords(t)
	e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, nil)
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e, c, p)
	want := &RunTicket{ID: testTicketID, Key: "CND-12", Rev: 7, Plan: testPlanID, PlanN: 2, Launch: testLaunchID, Digest: p.Digest}
	if got, _ := e.Get(run.ID); !reflect.DeepEqual(got.Ticket, want) {
		t.Fatalf("the run's read: %+v", got.Ticket)
	}
	if list := e.List(); len(list) != 1 || !reflect.DeepEqual(list[0].Ticket, want) {
		t.Fatalf("the run in the list: %+v", list)
	}
	b, _ := json.Marshal(run)
	if wantJSON := `"ticket":{"id":"` + testTicketID + `","key":"CND-12","rev":7,"plan":"` + testPlanID + `","planN":2,"launch":"` + testLaunchID + `","digest":"` + p.Digest + `"}`; !strings.Contains(string(b), wantJSON) {
		t.Fatalf("the run's JSON has no %s: %s", wantJSON, b)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	if rec := waitRecord(t, rs, run.ID, RunStopped); !reflect.DeepEqual(rec.Ticket, want) {
		t.Fatalf("the run's record: %+v", rec.Ticket)
	}
	from, ok, err := SQLLedger(ld).Load(t.Context(), run.ID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if rebuilt := Recovered(Run{}, from); !reflect.DeepEqual(rebuilt.Ticket, want) {
		t.Fatalf("the record rebuilt from the ledger: %+v", rebuilt.Ticket)
	}
	if rec, _, _ := rs.Get(run.ID); !reflect.DeepEqual(Recovered(rec, from).Ticket, want) {
		t.Fatalf("the record recovered: %+v", Recovered(rec, from).Ticket)
	}

	plain := launchManaged(t, e, c)
	if plain.Ticket != nil {
		t.Fatalf("a run no ticket launched: %+v", plain.Ticket)
	}
	if b, _ := json.Marshal(plain); strings.Contains(string(b), `"ticket"`) {
		t.Fatalf("a run no ticket launched has a ticket key: %s", b)
	}
	if row := ledgerOf(t, ld, plain.ID).Row; row.Ticket != "" || row.Package != "" {
		t.Fatalf("a run no ticket launched: its row %+v", row)
	}
	waitAttempt(t, e, plain.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, plain.ID)
}

// A continuation of a planned run carries its package: its row's package
// and ticket are the source's byte for byte, and its read's ticket is the
// source's, the digest the same; so is a continuation of the continuation.
func TestAContinuationCarriesThePackage(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	c := repoCrew(dir, work("build", "lead"))
	e, ld := sqlEngine(t)
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e, c, p)
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	src := ledgerOf(t, ld, run.ID).Row
	from := run.ID
	for range 2 {
		next, err := e.ContinueRun(t.Context(), from, "")
		if err != nil {
			t.Fatal(err)
		}
		row := ledgerOf(t, ld, next.ID).Row
		if row.Package != src.Package || row.Package != string(p.Package) || row.Ticket != src.Ticket || row.ContinuedFrom != from || row.Base != base {
			t.Fatalf("the continuation's row: ticket %s, continued from %s, base %s, package %d bytes", row.Ticket, row.ContinuedFrom, row.Base, len(row.Package))
		}
		if !reflect.DeepEqual(next.Ticket, run.Ticket) || next.Ticket.Digest != p.Digest {
			t.Fatalf("the continuation's ticket: %+v, the source's %+v", next.Ticket, run.Ticket)
		}
		waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
		stopRun(t, e, next.ID)
		from = next.ID
	}
}

// forcedIDs is a newRunID seam that gives the ids listed, in turn, and the
// last of them from then on, counting the ids it gave.
type forcedIDs struct {
	mu    sync.Mutex
	ids   []string
	given int
}

func (f *forcedIDs) next(string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.given++
	id := f.ids[0]
	if len(f.ids) > 1 {
		f.ids = f.ids[1:]
	}
	return id
}

func (f *forcedIDs) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.given
}

// A planned run's row in the ledger and a new engine, as after a restart,
// that does not hold it: an ordinary launch and a continuation whose minted
// id is the planned run's each take another, and the planned row stays as
// it was, its crew, base, ticket and package with it. Given that id every
// time, each is refused ErrRunExists after three ids, no attempt made, the
// row untouched, and the continued run continued by nothing.
func TestAnOrdinaryLaunchNeverOverwritesAPlannedRun(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	c := repoCrew(dir, work("build", "lead"))
	e1, ld := sqlEngine(t)
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e1, c, p)
	waitAttempt(t, e1, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e1, run.ID)
	before := ledgerOf(t, ld, run.ID)

	e2, fl2 := readyEngine(t)
	e2.UseLedger(SQLLedger(ld))
	if _, ok := e2.Get(run.ID); ok {
		t.Fatal("the new engine holds the planned run")
	}
	untouched := func(what string) {
		t.Helper()
		if after := ledgerOf(t, ld, run.ID); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s changed the planned run's rows:\n%+v\nwas\n%+v", what, after, before)
		}
	}

	ids := &forcedIDs{ids: []string{run.ID, "api-sweep-0000000b"}}
	e2.newRunID = ids.next
	ordinary := launchManaged(t, e2, c)
	if ordinary.ID != "api-sweep-0000000b" || ids.count() != 2 {
		t.Fatalf("the ordinary launch took %s after %d ids", ordinary.ID, ids.count())
	}
	untouched("an ordinary launch")
	if row := ledgerOf(t, ld, ordinary.ID).Row; row.Ticket != "" || row.Package != "" || row.ContinuedFrom != "" {
		t.Fatalf("the ordinary run's row: %+v", row)
	}
	waitAttempt(t, e2, ordinary.ID, "build", ledger.AttemptRunning)
	stopRun(t, e2, ordinary.ID)

	ids = &forcedIDs{ids: []string{run.ID, "api-sweep-0000000c"}}
	e2.newRunID = ids.next
	next, err := e2.ContinueRun(t.Context(), ordinary.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "api-sweep-0000000c" || ids.count() != 2 || next.ResumedFrom != ordinary.ID {
		t.Fatalf("the continuation took %s after %d ids, from %s", next.ID, ids.count(), next.ResumedFrom)
	}
	untouched("a continuation")
	if row := ledgerOf(t, ld, ordinary.ID).Row; row.ContinuedBy != next.ID {
		t.Fatalf("the continued run is continued by %q", row.ContinuedBy)
	}
	waitAttempt(t, e2, next.ID, "build", ledger.AttemptRunning)
	stopRun(t, e2, next.ID)

	// The planned run's id every time.
	launches := len(fl2.launched())
	ids = &forcedIDs{ids: []string{run.ID}}
	e2.newRunID = ids.next
	if _, release, err := e2.LaunchNamed(t.Context(), c, ""); !errors.Is(err, ErrRunExists) || ids.count() != maxMints || maxMints != 3 {
		release()
		t.Fatalf("an ordinary launch given the planned id every time: %v after %d ids", err, ids.count())
	} else {
		release()
	}
	untouched("a refused ordinary launch")
	ids = &forcedIDs{ids: []string{run.ID}}
	e2.newRunID = ids.next
	if _, err := e2.ContinueRun(t.Context(), next.ID, ""); !errors.Is(err, ErrRunExists) || ids.count() != maxMints {
		t.Fatalf("a continuation given the planned id every time: %v after %d ids", err, ids.count())
	}
	untouched("a refused continuation")
	if n := len(fl2.launched()); n != launches {
		t.Fatalf("the refused launch and continuation started %d sessions", n-launches)
	}
	if row := ledgerOf(t, ld, next.ID).Row; row.ContinuedBy != "" {
		t.Fatalf("the refused continuation marked its run continued by %q", row.ContinuedBy)
	}
	if _, ok := e2.Get(run.ID); ok {
		t.Fatal("the engine kept a refused run under the planned id")
	}
	// The refusal let go of the run: it is continued once the ids are its
	// own again.
	e2.newRunID = nil
	again, err := e2.ContinueRun(t.Context(), next.ID, "")
	if err != nil {
		t.Fatalf("the continuation after the refusal: %v", err)
	}
	waitAttempt(t, e2, again.ID, "build", ledger.AttemptRunning)
	stopRun(t, e2, again.ID)
	untouched("the continuation after the refusal")
}

// A managed run takes no change before its launch is recorded: a command
// given while the engine holds the run and before its first transaction
// (afterAdd) is refused ErrTaskState, nothing of it written, and the
// launch's run.created is the run's first event, its tasks dispatched as
// the launch armed them; so for a continuation, before its history is
// installed.
func TestACommandWaitsForTheLaunchToBeRecorded(t *testing.T) {
	dir := newRepo(t)
	c := repoCrew(dir, work("build", "lead"), work("docs", "writer"))
	e, ld := sqlEngine(t)
	var cmdErr error
	e.afterAdd = func(runID string) {
		_, cmdErr = e.Cancel(t.Context(), runID, "admin", Command{ID: "c1"}, "docs")
	}
	run := launchManaged(t, e, c)
	e.afterAdd = nil
	if !errors.Is(cmdErr, ErrTaskState) {
		t.Fatalf("a command before the launch was recorded: %v", cmdErr)
	}
	accepted := func(events []ledger.EventRow) bool {
		return slices.ContainsFunc(events, func(ev ledger.EventRow) bool { return strings.HasPrefix(ev.Kind, "command.") })
	}
	got := ledgerOf(t, ld, run.ID)
	if len(got.Events) == 0 || got.Events[0].Kind != "run.created" || accepted(got.Events) {
		t.Fatalf("the run's events: %+v", got.Events)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	stopRun(t, e, run.ID)

	cmdErr = nil
	e.afterAdd = func(runID string) {
		_, cmdErr = e.Cancel(t.Context(), runID, "admin", Command{ID: "c2"}, "docs")
	}
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	e.afterAdd = nil
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cmdErr, ErrTaskState) {
		t.Fatalf("a command before the continuation was recorded: %v", cmdErr)
	}
	if got := ledgerOf(t, ld, next.ID); accepted(got.Events) || len(got.Events) == 0 || got.Events[0].Kind != "run.continued" {
		t.Fatalf("the continuation's events: %+v", got.Events)
	}
	waitAttempt(t, e, next.ID, "docs", ledger.AttemptRunning)
	stopRun(t, e, next.ID)
}

// A launch whose first transaction fails for a reason of the ledger's own
// (not an id another run has), its stop failing too, is let go as on any
// failed launch: told as forgotten, so the browsers drop it, and its
// record written.
func TestALaunchWhoseFirstTransactionFailsIsForgotten(t *testing.T) {
	e, _, l := managedEngine(t)
	rs, _ := newRecords(t)
	e.RecordEnds(rs, func(runID string, err error) { t.Errorf("%s: %v", runID, err) }, nil)
	var mu sync.Mutex
	var forgotten []string
	e.OnForget = func(r Run) {
		mu.Lock()
		forgotten = append(forgotten, r.ID)
		mu.Unlock()
	}
	l.mu.Lock()
	l.failOn = "run.created,run.stopped"
	l.mu.Unlock()
	_, release, err := e.LaunchNamed(t.Context(), repoCrew(newRepo(t), work("build", "lead")), "")
	release()
	if err == nil || errors.Is(err, ErrRunExists) {
		t.Fatalf("a launch the ledger failed: %v", err)
	}
	mu.Lock()
	gone := slices.Clone(forgotten)
	mu.Unlock()
	if len(gone) != 1 {
		t.Fatalf("told forgotten: %v", gone)
	}
	if _, ok := e.Get(gone[0]); ok {
		t.Fatal("the engine kept the failed run")
	}
	if !e.WaitRecorded(t.Context()) {
		t.Fatal("the records did not settle")
	}
	if _, ok, _ := rs.Get(gone[0]); !ok {
		t.Fatal("the failed run's record was not written")
	}
}

// A command refused because the launch was not recorded yet, whose
// refusal is handled only once the launch has committed (afterRefused),
// pauses nothing: the refusal is no failure of the ledger's, so the run
// launched whole is not paused, no run.paused is written, and its tasks
// are dispatched as the launch armed them.
func TestACommandRefusedBeforeTheLaunchPausesNothing(t *testing.T) {
	dir := newRepo(t)
	c := repoCrew(dir, work("build", "lead"), work("docs", "writer"))
	e, ld := sqlEngine(t)
	refused := make(chan error, 1)
	handle := make(chan struct{})
	done := make(chan error, 1)
	e.afterRefused = func(runID string, err error) {
		refused <- err
		<-handle
	}
	var refusal error
	e.afterAdd = func(runID string) {
		go func() {
			_, err := e.Cancel(context.Background(), runID, "admin", Command{ID: "c1"}, "docs")
			done <- err
		}()
		// Refused while the launch is not recorded; handled after it is.
		refusal = <-refused
	}
	run := launchManaged(t, e, c)
	e.afterAdd = nil
	close(handle)
	cmdErr := <-done
	e.afterRefused = nil
	if !errors.Is(refusal, errLaunching) || !errors.Is(cmdErr, ErrTaskState) {
		t.Fatalf("the refusal %v, the command's error %v", refusal, cmdErr)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	if r, ok := e.Get(run.ID); !ok || r.Paused {
		t.Fatalf("the run after the refusal: %+v %v", r, ok)
	}
	got := ledgerOf(t, ld, run.ID)
	if got.Row.Paused || slices.ContainsFunc(got.Events, func(ev ledger.EventRow) bool { return ev.Kind == "run.paused" }) {
		t.Fatalf("the refusal paused the run: %+v", got.Events)
	}
	stopRun(t, e, run.ID)
}

// A run stopped before its launch was recorded, its stop's first
// transaction refused once by the ledger, holds the stop for the ledger;
// once the ledger takes transactions again, a continuation writes it
// first, as the run's first transaction (its row through CreateRun), and
// goes on from it: never ErrRunUnsettled for a stop it could write. The
// write still refuses an id the ledger has come to hold meanwhile: the
// held run, another's, is let go, nothing of it written, and the
// continuation is the ledger's run's to judge.
func TestAnEarlyStopTheLedgerRefusedIsWrittenForAContinuation(t *testing.T) {
	stoppedEarly := func(t *testing.T) (*Engine, *fakeLedger, string) {
		t.Helper()
		e, _, l := managedEngine(t)
		c := repoCrew(newRepo(t), work("build", "lead"))
		l.mu.Lock()
		l.failOn, l.failOnce = "run.stopped", true
		l.mu.Unlock()
		var id string
		var stopErr error
		e.afterAdd = func(runID string) {
			id = runID
			stopErr = e.Stop(t.Context(), runID)
		}
		_, release, err := e.LaunchNamed(t.Context(), c, "")
		release()
		e.afterAdd = nil
		if stopErr == nil || !errors.Is(err, ErrRunStopped) {
			t.Fatalf("the stop: %v, the launch: %v", stopErr, err)
		}
		if r, ok := e.Get(id); !ok || r.StoppedAt == nil {
			t.Fatalf("the stopped run: %+v %v", r, ok)
		}
		if _, found, _ := l.Load(t.Context(), id); found {
			t.Fatal("the refused stop wrote the run's row")
		}
		return e, l, id
	}

	t.Run("written", func(t *testing.T) {
		e, l, id := stoppedEarly(t)
		next, err := e.ContinueRun(t.Context(), id, "")
		if err != nil {
			t.Fatalf("the continuation of a run whose first stop was refused: %v", err)
		}
		from, found, err := l.Load(t.Context(), id)
		if err != nil || !found || from.Run.State == ledger.RunRunning || from.Run.ContinuedBy != next.ID {
			t.Fatalf("the stopped run's row: %+v %v %v", from.Run, found, err)
		}
		if kinds := l.kinds(id); !slices.Contains(kinds, "run.stopped") {
			t.Fatalf("the stopped run's events: %v", kinds)
		}
		waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
		stopRun(t, e, next.ID)
	})

	t.Run("the id another run's meanwhile", func(t *testing.T) {
		e, l, id := stoppedEarly(t)
		other := ledger.RunRow{ID: id, CrewID: "api-sweep", Crew: `{"id":"api-sweep","name":"another launch"}`, State: ledger.RunRunning}
		l.mu.Lock()
		l.runs[id] = other
		l.mu.Unlock()
		_, err := e.ContinueRun(t.Context(), id, "")
		if !errors.Is(err, ErrRunRunning) {
			t.Fatalf("the continuation of an id another run holds: %v", err)
		}
		if _, ok := e.Get(id); ok {
			t.Fatal("the engine kept the run whose id is another's")
		}
		from, found, _ := l.Load(t.Context(), id)
		if !found || !reflect.DeepEqual(from.Run, other) || len(l.kinds(id)) != 0 {
			t.Fatalf("the other run's row %+v, its events %v", from.Run, l.kinds(id))
		}
	})
}
