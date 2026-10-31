package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// interrupt leaves the ledger's rows as the owner's recovery would after
// the server's end: the run interrupted, the attempts given their states.
func interrupt(l *fakeLedger, runID string, attempts map[string][2]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row := l.runs[runID]
	row.State = ledger.RunInterrupted
	l.runs[runID] = row
	for key, st := range attempts {
		a := l.attempts[runID+"/"+key]
		a.State, a.Stage, a.ClosedAt = st[0], st[1], time.Now().UTC()
		l.attempts[runID+"/"+key] = a
	}
}

// A continuation after the server's end (row 8 of #334): the results that
// stood are kept by reference, an attempt interrupted mid-work gets a
// fresh attempt numbered after it, the old run names the new, and the
// rest runs to an outcome.
func TestAContinuationKeepsResultsAndTakesUpTheRest(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -e built.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer", "build"), work("site", "web", "docs")))
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
	first := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	// The server ends with docs' attempt running: its recovery concludes the rows.
	interrupt(l, run.ID, map[string][2]string{"docs/1": {ledger.AttemptInterrupted, "running: the owner stopped"}})
	ctx := context.Background()
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == run.ID || next.ResumedFrom != run.ID || next.Managed == nil || next.State != RunRunning {
		t.Fatalf("the continuation: %+v", next)
	}
	// build is kept by reference: no attempt of its own, its result the old commit.
	if b := taskOf(t, e2, next.ID, "build"); b.State != ledger.TaskSucceeded || b.Attempts != 1 || b.Attempt != nil || b.Result == nil || b.Result.Commit != first.Result.Commit || b.Result.Attempt != 1 || b.Result.Summary != "built" || b.ImportedFrom != run.ID+"/build/1" {
		t.Fatalf("build in the continuation: %+v", b)
	}
	// docs: a fresh attempt numbered after the interrupted one, at build's result, its task file naming the old run's branch as the input.
	d := waitTaskWhere(t, e2, next.ID, "docs", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if d.Attempts != 2 || d.Attempt.Start != first.Result.Commit || d.Attempt.Branch != "crew/"+next.ID+"/docs.2" {
		t.Fatalf("docs in the continuation: %+v %+v", d, d.Attempt)
	}
	if md, f := taskFileOf(t, d); f.Attempt != 2 || len(f.Inputs) != 1 || f.Inputs[0].Branch != "crew/"+run.ID+"/build.1" || !strings.Contains(md, "crew/"+run.ID+"/build.1") {
		t.Fatalf("docs' task file: %+v\n%s", f.Inputs, md)
	}
	// The ledger: each run's row names the other; the rows count the attempts before.
	from, ok, err := l.Load(ctx, next.ID)
	if err != nil || !ok || from.Run.ContinuedFrom != run.ID || from.Run.Base != first.Attempt.Start {
		t.Fatalf("the continuation's row: %+v %v %v", from.Run, ok, err)
	}
	if old, _, _ := l.Load(ctx, run.ID); old.Run.ContinuedBy != next.ID {
		t.Fatalf("the old run's row: %+v", old.Run)
	}
	for _, row := range from.Tasks {
		switch row.ID {
		case "build":
			if row.ImportedFrom != run.ID+"/build/1" || row.Attempts != 1 || row.ResultCommit != first.Result.Commit {
				t.Fatalf("build's row: %+v", row)
			}
		case "docs":
			if row.Attempts != 2 || row.ImportedFrom != "" {
				t.Fatalf("docs' row: %+v", row)
			}
		}
	}
	if !slices.Contains(l.kinds(next.ID), "run.continued") {
		t.Fatalf("events: %v", l.kinds(next.ID))
	}
	// The rest: docs, then site at docs' result; the continuation succeeds.
	commitResult(t, e2, d, "docs.txt", "d\n", "docs")
	docs := waitTask(t, e2, next.ID, "docs", ledger.TaskSucceeded, 2)
	site := waitAttempt(t, e2, next.ID, "site", ledger.AttemptRunning)
	if site.Attempt.Start != docs.Result.Commit {
		t.Fatalf("site starts at %s, docs' result is %s", site.Attempt.Start, docs.Result.Commit)
	}
	commitResult(t, e2, site, "site.txt", "s\n", "site")
	waitOutcome(t, e2, next.ID, "succeeded")
	// The old run is continued once.
	var resumed *RunResumedError
	if _, err := e2.ContinueRun(ctx, run.ID, ""); !errors.As(err, &resumed) || resumed.By != next.ID {
		t.Fatalf("a second continuation: %v", err)
	}
	stopRun(t, e2, next.ID)
}

// A result that was being verified when the server ended is verified
// again without a session, and an attempt whose launch was never known
// waits for a retry.
func TestAContinuationVerifiesACandidateAgainAndWaitsOnAnUncertainLaunch(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	mark := filepath.Join(t.TempDir(), "passed")
	build := work("build", "lead")
	// Until the mark exists the check takes long: the server ends during it.
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -e " + mark + " || sleep 30"}}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built", Issues: []string{"none"}})
	verifying := waitAttempt(t, e, run.ID, "build", ledger.AttemptVerifying)
	candidate := verifying.Attempt.Result
	// The ledger has the result as verifying before the server ends.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if row := l.attemptRow(t, run.ID, "build", 1); row.State == ledger.AttemptVerifying && row.Result != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the result was not recorded as verifying")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The first engine is stopped before the ledger is rewritten and the
	// mark made: a check still going would pass on the mark and record its
	// outcome over the interrupted row.
	stopRun(t, e, run.ID)
	interrupt(l, run.ID, map[string][2]string{
		"build/1": {ledger.AttemptInterrupted, "verifying: the owner stopped; the candidate is verified again"},
		"docs/1":  {ledger.AttemptUncertain, "reserved: the owner stopped before the launch was known"},
	})
	if err := os.WriteFile(mark, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	next, err := e2.ContinueRun(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	// build: verified again as attempt 2, no session, the candidate its result and attempt 1's start its start, not counted against the task.
	b := waitTask(t, e2, next.ID, "build", ledger.TaskSucceeded, 2)
	if b.Attempt == nil || b.Attempt.N != 2 || b.Attempt.SessionID != "" || b.Attempt.Start != a.Attempt.Start || b.Result == nil || b.Result.Commit != candidate || b.Result.Attempt != 2 || b.Result.Summary != "built" || !slices.Equal(b.Result.Issues, []string{"none"}) {
		t.Fatalf("build verified again: %+v %+v %+v", b, b.Attempt, b.Result)
	}
	// The engine's view is one change ahead of the ledger while that change
	// commits: attempt 2's row and the events are waited for in the ledger.
	if row := l.attemptWhen(t, next.ID, "build", 2, succeededRow); row.SessionID != "" || row.ResultCommit != candidate || !strings.Contains(row.Result, "built") {
		t.Fatalf("attempt 2's row: %+v", row)
	}
	waitFake(t, "the events result.reverified and check.started", func() bool {
		kinds := l.kinds(next.ID)
		return slices.Contains(kinds, "result.reverified") && slices.Contains(kinds, "check.started")
	})
	// docs waits for a person: failed with why; a retry allows attempt 2.
	d := taskOf(t, e2, next.ID, "docs")
	if d.State != ledger.TaskFailed || !strings.Contains(d.Reason, "attempt 1's launch is uncertain") || d.Attempts != 1 {
		t.Fatalf("docs in the continuation: %+v", d)
	}
	if o, _ := e2.Get(next.ID); o.Outcome != "failed" {
		t.Fatalf("the continuation's outcome: %q", o.Outcome)
	}
	if err := e2.RetryTask(ctx, next.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	d2 := waitTaskWhere(t, e2, next.ID, "docs", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	commitResult(t, e2, d2, "docs.txt", "d\n", "docs")
	waitOutcome(t, e2, next.ID, "succeeded")
	stopRun(t, e2, next.ID)
}

// A run still going is not continued; a run the engine holds stopped is,
// once; a legacy run is not; an unknown run is not found.
func TestAContinuationIsRefusedWhereItMustBe(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ctx := context.Background()
	if _, err := e.ContinueRun(ctx, run.ID, ""); !errors.Is(err, ErrRunRunning) {
		t.Fatalf("a live run: %v", err)
	}
	if _, err := e.ContinueRun(ctx, "nope-00000000", ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(ctx, run.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	if next.Label != "again" || next.ResumedFrom != run.ID {
		t.Fatalf("the continuation: %+v", next)
	}
	if old, _ := e.Get(run.ID); old.ResumedBy != next.ID {
		t.Fatalf("the old run: resumedBy %q", old.ResumedBy)
	}
	var resumed *RunResumedError
	if _, err := e.ContinueRun(ctx, run.ID, ""); !errors.As(err, &resumed) || resumed.By != next.ID {
		t.Fatalf("a second continuation: %v", err)
	}
	// The stopped attempt left the task ready: the continuation makes attempt 2.
	waitTaskWhere(t, e, next.ID, "build", "at attempt 2", func(ts TaskState) bool { return ts.Attempt != nil && ts.Attempt.N == 2 })
	stopRun(t, e, next.ID)
	legacy := repoCrew(dir)
	legacy.Tasks = nil
	legacy.Members = []Member{immediate("lead", "go")}
	lr := launchManaged(t, e, legacy)
	stopRun(t, e, lr.ID)
	if _, err := e.ContinueRun(ctx, lr.ID, ""); !errors.Is(err, ErrTaskState) {
		t.Fatalf("a legacy run: %v", err)
	}
}

// Recovered rewrites a managed run's record as the ledger has it after a
// recovery: interrupted, each task as its rows say, the sessions ended.
func TestRecoveredRecordSaysWhatTheLedgerConcluded(t *testing.T) {
	now := time.Now().UTC()
	rec := Run{ID: "r-00000001", State: RunRunning, NeedsInput: 1, Cleaning: 2,
		Manifest: &RunManifest{Source: "crew", Revision: 1, Prerequisites: []PrerequisiteState{{ID: "node", K: 1, State: "passed"}, {ID: "tools", K: 1, State: "running"}}},
		Members:  []MemberState{{Name: "lead", Status: MemberRunning, NeedsInput: true}, {Name: "writer", Status: MemberPending}},
		Tasks:    []TaskState{{ID: "build", State: ledger.TaskSucceeded, Attempts: 1}, {ID: "docs", State: ledger.TaskRunning, Attempts: 1}, {ID: "site", State: ledger.TaskVerifying, Attempts: 2}, {ID: "late", State: ledger.TaskBlocked}}}
	from := Loaded{Run: ledger.RunRow{ID: rec.ID, State: ledger.RunInterrupted, Revision: 9, Paused: true, PausedWhy: "the prerequisite tools failed: tools exited 1"},
		Tasks: []ledger.TaskRow{{ID: "build", State: ledger.TaskSucceeded, Attempts: 1, ResultCommit: "abc"}, {ID: "docs", State: ledger.TaskRunning, Attempts: 1}, {ID: "site", State: ledger.TaskVerifying, Attempts: 2}, {ID: "late", State: ledger.TaskBlocked, Reason: "needs site"}},
		Attempts: []ledger.AttemptRow{
			{TaskID: "build", N: 1, State: ledger.AttemptSucceeded, ResultCommit: "abc", LaunchedAt: now},
			{TaskID: "docs", N: 1, State: ledger.AttemptUncertain, Stage: "reserved: the owner stopped before the launch was known", ClosedAt: now},
			{TaskID: "site", N: 1, State: ledger.AttemptFailed}, {TaskID: "site", N: 2, State: ledger.AttemptInterrupted, Stage: "verifying: the owner stopped; the candidate is verified again", ResultCommit: "def", ClosedAt: now},
		},
		// node passed; tools was under way, and the recovery interrupted it.
		Prerequisites: []ledger.PrerequisiteRow{
			{ID: "node", K: 1, State: ledger.CheckFinished, StartedAt: now.Add(-2 * time.Second), FinishedAt: now.Add(-time.Second)},
			{ID: "tools", K: 1, State: ledger.CheckInterrupted, StartedAt: now.Add(-time.Second), FinishedAt: now},
		}}
	out := Recovered(rec, from)
	if out.State != RunInterrupted || out.Managed == nil || out.Managed.Revision != 9 || !out.Paused || out.NeedsInput != 0 {
		t.Fatalf("the run: %+v", out)
	}
	// Nothing runs after the server's end: no tree is cleaning, a
	// prerequisite left running was interrupted; the pause keeps its words.
	if p := out.Manifest.Prerequisites; out.Cleaning != 0 || out.PausedWhy != from.Run.PausedWhy || p[0].State != "passed" || p[1].State != "interrupted" {
		t.Fatalf("the run: cleaning %d, paused %q, prerequisites %+v", out.Cleaning, out.PausedWhy, p)
	}
	if m := out.Members[0]; m.Status != MemberEnded || m.NeedsInput || m.Ended == nil {
		t.Fatalf("lead: %+v", m)
	}
	if m := out.Members[1]; m.Status != MemberPending {
		t.Fatalf("writer: %+v", m)
	}
	byID := map[string]TaskState{}
	for _, ts := range out.Tasks {
		byID[ts.ID] = ts
	}
	if b := byID["build"]; b.State != ledger.TaskSucceeded || b.Attempt == nil || b.Attempt.State != ledger.AttemptSucceeded || b.Attempt.StartedAt == nil {
		t.Fatalf("build: %+v", b)
	}
	if d := byID["docs"]; d.State != ledger.TaskFailed || !strings.Contains(d.Reason, "attempt 1's launch is uncertain") || d.Attempt.State != ledger.AttemptUncertain || d.Attempt.EndedAt == nil {
		t.Fatalf("docs: %+v", d)
	}
	if s := byID["site"]; s.State != ledger.TaskReady || !strings.Contains(s.Reason, "attempt 2 was interrupted") || s.Attempt.N != 2 || s.Attempt.Result != "def" || s.Attempts != 2 {
		t.Fatalf("site: %+v", s)
	}
	if l := byID["late"]; l.State != ledger.TaskBlocked || l.Reason != "needs site" || l.Attempt != nil {
		t.Fatalf("late: %+v", l)
	}
}

// prereqWords is a run's prerequisites in words, for a comparison.
func prereqWords(ps []PrerequisiteState) string {
	var out []string
	for _, p := range ps {
		exit := "-"
		if p.Exit != nil {
			exit = fmt.Sprint(*p.Exit)
		}
		out = append(out, fmt.Sprintf("%s k%d %s exit %s %q %dms", p.ID, p.K, p.State, exit, p.Error, p.Duration))
	}
	return strings.Join(out, "; ")
}

// A recovered record's prerequisites are their receipts', not the
// record's, which may lag them (Codex's review of #422): a record that
// says passed after a second generation failed reads failed at k 2, the
// prerequisite that generation never reached pending with its earlier k;
// one that says running for a receipt that finished before the server's
// end reads passed; and a run with no record gets its manifest from its
// ledger row, the crew's at its revision or the file's blob, with every
// prerequisite in its order: one no generation reached is pending, and a
// file with none still has its blob (round 2), which no receipt names. A
// version 4 row, which keeps no manifest, gets the crew's as launched.
// The record given is not changed.
func TestARecoveredRecordsPrerequisitesAreTheirReceipts(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Minute)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	// Generation 1: tools and node passed. Generation 2, an unpause's:
	// tools failed, and node was not reached.
	rows := func(manifest string) []ledger.PrerequisiteRow {
		return []ledger.PrerequisiteRow{
			{ID: "tools", K: 1, State: ledger.CheckFinished, Manifest: manifest, StartedAt: at(0), FinishedAt: at(1)},
			{ID: "node", K: 1, State: ledger.CheckFinished, Manifest: manifest, StartedAt: at(1), FinishedAt: at(3)},
			{ID: "tools", K: 2, State: ledger.CheckFinished, Manifest: manifest, Exit: 127, Error: "tools exited 127: tools: not found", StartedAt: at(10), FinishedAt: at(11)},
		}
	}
	crewJSON := func(m *Manifest) string {
		b, err := json.Marshal(Crew{ID: "c", Name: "c", Tasks: []Task{{ID: "build", Member: "lead", Kind: TaskWork}}, Members: []Member{{Name: "lead", AgentID: "ready-cat"}}, ManifestRevision: 3, Manifest: m})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	crewForm := &Manifest{Prerequisites: []Check{{ID: "tools", Argv: []string{"tools"}}, {ID: "node", Argv: []string{"node"}}}}
	loaded := func(m *Manifest, row string, prereqs []ledger.PrerequisiteRow) Loaded {
		return Loaded{Run: ledger.RunRow{ID: "r-00000002", Crew: crewJSON(m), Manifest: row, State: ledger.RunInterrupted, Revision: 12, Paused: true, PausedWhy: "the prerequisite tools failed: tools exited 127: tools: not found"}, Prerequisites: prereqs}
	}
	crewRow := `{"source":"crew","revision":3,"prerequisites":["tools","node"]}`
	fileRow := `{"source":"file","digest":"0a1b2c","prerequisites":["tools","node"]}`
	failed := "tools k2 failed exit 127 \"tools exited 127: tools: not found\" 1000ms; node k1 pending exit - \"\" 0ms"

	lagging := Run{ID: "r-00000002", State: RunRunning, Managed: &Managed{Revision: 7},
		Manifest: &RunManifest{Source: sourceCrew, Revision: 3, Prerequisites: []PrerequisiteState{{ID: "tools", K: 1, State: "passed"}, {ID: "node", K: 1, State: "passed"}}}}
	out := Recovered(lagging, loaded(crewForm, crewRow, rows("crew:3")))
	if got := prereqWords(out.Manifest.Prerequisites); got != failed || out.Manifest.Source != sourceCrew || out.Manifest.Revision != 3 || !out.Paused {
		t.Fatalf("a record that lags a failed generation: %s (%+v)", got, out.Manifest)
	}
	if lagging.Manifest.Prerequisites[0].State != "passed" {
		t.Fatalf("the record given was changed: %+v", lagging.Manifest)
	}

	running := Run{ID: "r-00000002", State: RunRunning, Managed: &Managed{Revision: 5},
		Manifest: &RunManifest{Source: sourceCrew, Revision: 3, Prerequisites: []PrerequisiteState{{ID: "tools", K: 1, State: "passed"}, {ID: "node", K: 1, State: "running"}}}}
	out = Recovered(running, loaded(crewForm, crewRow, rows("crew:3")[:2]))
	if got := prereqWords(out.Manifest.Prerequisites); got != "tools k1 passed exit 0 \"\" 1000ms; node k1 passed exit 0 \"\" 2000ms" {
		t.Fatalf("a receipt that finished before the server's end: %s", got)
	}

	for name, row := range map[string]string{"its row": crewRow, "a version 4 row": ""} {
		out = Recovered(Run{}, loaded(crewForm, row, rows("crew:3")))
		if m := out.Manifest; m == nil || m.Source != sourceCrew || m.Revision != 3 || m.Digest != "" || prereqWords(m.Prerequisites) != failed {
			t.Fatalf("no record, the crew's manifest from %s: %+v", name, m)
		}
	}
	out = Recovered(Run{}, loaded(&Manifest{File: true}, fileRow, rows("file:0a1b2c")))
	if m := out.Manifest; m == nil || m.Source != sourceFile || m.Revision != 0 || m.Digest != "0a1b2c" || prereqWords(m.Prerequisites) != failed {
		t.Fatalf("no record, the file's manifest: %+v", m)
	}
	// The file's first prerequisite failed before the second ever ran: the
	// second is pending, at no k.
	firstFailed := []ledger.PrerequisiteRow{{ID: "tools", K: 1, State: ledger.CheckFinished, Manifest: "file:0a1b2c", Exit: 127, Error: "tools exited 127: tools: not found", StartedAt: at(0), FinishedAt: at(1)}}
	out = Recovered(Run{}, loaded(&Manifest{File: true}, fileRow, firstFailed))
	if m := out.Manifest; m == nil || m.Digest != "0a1b2c" || prereqWords(m.Prerequisites) != `tools k1 failed exit 127 "tools exited 127: tools: not found" 1000ms; node k0 pending exit - "" 0ms` {
		t.Fatalf("no record, the file's first prerequisite failed: %+v", m)
	}
	// A file with setup steps alone: its blob, and no prerequisite.
	out = Recovered(Run{}, loaded(&Manifest{File: true}, `{"source":"file","digest":"0a1b2c"}`, nil))
	if m := out.Manifest; m == nil || m.Source != sourceFile || m.Digest != "0a1b2c" || len(m.Prerequisites) != 0 {
		t.Fatalf("no record, a file with no prerequisite: %+v", m)
	}
	if out := Recovered(Run{}, loaded(nil, "", nil)); out.Manifest != nil {
		t.Fatalf("no record, no manifest: %+v", out.Manifest)
	}
}

// The ledger's Load carries the run's prerequisites' receipts, without
// their logs: a recovered record of a run with none reads them passed.
func TestTheLedgersLoadCarriesThePrerequisites(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Prerequisites: []Check{shStep("tools", "echo tools-ok")}}, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	from, ok, err := SQLLedger(ld).Load(context.Background(), run.ID)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if p := from.Prerequisites; len(p) != 1 || p[0].ID != "tools" || p[0].K != 1 || p[0].State != ledger.CheckFinished || p[0].Log != nil || p[0].LogBytes != int64(len("tools-ok\n")) {
		t.Fatalf("the prerequisites loaded: %+v", p)
	}
	if m := Recovered(Run{}, from).Manifest; m == nil || prereqWords(m.Prerequisites) == "" || m.Prerequisites[0].State != "passed" || m.Prerequisites[0].K != 1 {
		t.Fatalf("the run read with no record: %+v", m)
	}
}

// The run's ledger row keeps the manifest at its base, so a run with no
// record names it whole (Codex's review of #422, round 2): a file whose
// first prerequisite failed has both, the one never run pending, and a
// file of setup steps alone its blob, which no receipt names.
func TestARunWithNoRecordNamesTheManifestItsRowKeeps(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"the first prerequisite failed", `{"schema": 1, "prerequisites": [{"id": "tools", "argv": ["sh", "-c", "exit 1"]}, {"id": "node", "argv": ["true"]}]}`},
		{"setup steps alone", `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			blob := commitManifestFile(t, dir, tc.file)
			e, ld := sqlEngine(t)
			run := launchManaged(t, e, fileCrew(dir, work("build", "lead")))
			if strings.Contains(tc.file, "prerequisites") {
				waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
			} else {
				waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			}
			stopRun(t, e, run.ID)
			from, ok, err := SQLLedger(ld).Load(context.Background(), run.ID)
			if err != nil || !ok {
				t.Fatalf("load: %v %v", ok, err)
			}
			m := Recovered(Run{}, from).Manifest
			if m == nil || m.Source != sourceFile || m.Digest != blob || m.Revision != 0 {
				t.Fatalf("the run read with no record: %+v (its row %s)", m, from.Run.Manifest)
			}
			if !strings.Contains(tc.file, "prerequisites") {
				if len(m.Prerequisites) != 0 {
					t.Fatalf("prerequisites of a file with none: %+v", m.Prerequisites)
				}
				return
			}
			if p := m.Prerequisites; len(p) != 2 || p[0].ID != "tools" || p[0].K != 1 || p[0].State != "failed" || p[1].ID != "node" || p[1].K != 0 || p[1].State != "pending" {
				t.Fatalf("the prerequisites read with no record: %s", prereqWords(p))
			}
		})
	}
}

// A stop that lands as an attempt's line is being recorded (#367): the
// run's context ends under the transaction, the ledger refuses it, and
// the refusal must not pause the run, or the pause is written into its
// row and its continuation starts paused, with the task that waits on the
// kept result ready and never dispatched. The ledger here holds the
// record of docs' line until the stop is under way, then refuses it.
func TestAStopUnderAnAttemptsLineLeavesItsContinuationUnpaused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	// docs' line, once typed, is recorded in a transaction that waits here.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "attempt.prompted", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	commitResult(t, e, build, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	select {
	case <-l.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("docs' line was not recorded within 15 s")
	}
	// The stop lands: the run's context ends, and the stop waits for the dispatch, which waits for its record.
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background(), run.ID) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		cancelled := e.runs[run.ID].ctx.Err() != nil
		e.mu.Unlock()
		if cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stop did not cancel the run within 10 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The record now fails with the cancellation, as a transaction on a context that is done does (the fake ledger's).
	close(l.release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	paused := l.runs[run.ID].Paused
	l.mu.Unlock()
	if paused {
		t.Fatal("the refused record paused the stopped run, for its continuation to inherit")
	}
	if got, _ := e.Get(run.ID); got.Paused {
		t.Fatalf("the stopped run reads paused: %+v", got)
	}
	// Its continuation dispatches docs at build's result.
	next, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.Paused {
		t.Fatalf("the continuation starts paused: %+v", next)
	}
	d := waitTaskWhere(t, e, next.ID, "docs", "at attempt 2, running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if d.Attempts != 2 {
		t.Fatalf("docs in the continuation: %+v", d)
	}
	stopRun(t, e, next.ID)
}

// A refusal that is no cancellation, under a stop, is the ledger failing
// for real: the stopped run is paused as ever, its continuation starts
// paused for a person to look at, and dispatches once unpaused (Codex's
// review of #367).
func TestARealLedgerFailureUnderAStopStillPauses(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "attempt.prompted", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	commitResult(t, e, build, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	select {
	case <-l.blocked:
	case <-time.After(15 * time.Second):
		t.Fatal("docs' line was not recorded within 15 s")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background(), run.ID) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		cancelled := e.runs[run.ID].ctx.Err() != nil
		e.mu.Unlock()
		if cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stop did not cancel the run within 10 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The disk fails as the record lands: no cancellation, a real refusal.
	l.mu.Lock()
	l.failOn, l.failOnce = "attempt.prompted", true
	l.mu.Unlock()
	close(l.release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Get(run.ID); !got.Paused {
		t.Fatalf("a real ledger failure under the stop left the run unpaused: %+v", got)
	}
	next, err := e.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !next.Paused {
		t.Fatalf("the continuation of a run paused by a real failure starts unpaused: %+v", next)
	}
	if err := e.PauseRun(context.Background(), next.ID, false); err != nil {
		t.Fatal(err)
	}
	waitTaskWhere(t, e, next.ID, "docs", "at attempt 2, running, once unpaused", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	stopRun(t, e, next.ID)
}
