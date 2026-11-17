package crew

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A folder in a check's argv (".", "tests/", "src") names no file: a
// candidate's change of a file in it leaves the row as its runs judge it,
// while a file the argv names, written "./scripts/test.sh", still counts.
func TestAFolderInTheArgvNamesNoFile(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		a    edit
		runs bool // the row says approach-a changed what it runs
	}{
		{"go test .", []string{"go", "test", "."}, put("main.go", "package main // a\n"), false},
		{"pytest tests/", []string{"pytest", "tests/"}, put("tests/x.py", "x = 2\n"), false},
		{"go vet src", []string{"go", "vet", "src"}, put("src/y.go", "package src // a\n"), false},
		{"sh ./scripts/test.sh", []string{"sh", "./scripts/test.sh"}, put("scripts/test.sh", "exit 0 # a\n"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			applyEdits(t, dir, []edit{put("main.go", "package main\n"), put("tests/x.py", "x = 1\n"), put("src/y.go", "package src\n"), put("scripts/test.sh", "exit 0\n")})
			c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: tc.argv}), []edit{tc.a}, []edit{put("b.txt", "b\n")})
			rw := rowOfCheck(t, measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b})), ledger.PhaseCheck, "test")
			if got := whyHas(rw, "which the check runs"); got != tc.runs || (tc.runs != (rw.Verdict == VerdictNotComparable)) {
				t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
			}
		})
	}
}

// An argv entry above the crew's directory is passed over: the files it
// names inside it are still looked up, so an edited script still counts.
func TestAnArgvEntryAboveTheFolderIsPassedOver(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("scripts/test.sh", "exit 0\n")})
	c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: []string{"sh", "scripts/test.sh", "../fixtures"}}),
		[]edit{put("scripts/test.sh", "exit 0 # always\n")}, []edit{put("b.txt", "b\n")})
	rw := rowOfCheck(t, measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b})), ledger.PhaseCheck, "test")
	if rw.Verdict != VerdictNotComparable || !slices.Contains(rw.Why, "approach-a changed scripts/test.sh, which the check runs") {
		t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
	}
}

// A row whose named files could not be looked up is unknown, never
// comparable, and the measure is not kept.
func TestARowWhoseNamedFilesWereNotReadIsUnknown(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("scripts/test.sh", "exit 0\n")})
	c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "test", Argv: []string{"sh", "scripts/test.sh"}}),
		[]edit{put("a.txt", "a\n")}, []edit{put("b.txt", "b\n")})
	e := &Engine{}
	e.cmpMeasure.git = func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		if args[0] == "ls-tree" {
			return "", errors.New("git ls-tree: failed")
		}
		return gitcliRunBounded(ctx, dir, limit, args...)
	}
	cmp := e.measureComparison(t.Context(), synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}))
	rw := rowOfCheck(t, cmp, ledger.PhaseCheck, "test")
	if rw.Verdict != VerdictUnknown || !whyHas(rw, "the files its command names could not be read") {
		t.Fatalf("the row: %s %q", rw.Verdict, rw.Why)
	}
	if len(e.cmpMeasure.cache) != 0 {
		t.Fatal("a measure that could not look the files up was kept")
	}
}

// A cell of an attempt that ended before the check (failed at its setup,
// or interrupted while its checks ran, its running check killed) says so,
// never that an earlier check failed.
func TestACellOfAnAttemptThatEndedBeforeItsChecksSaysSo(t *testing.T) {
	cases := []struct {
		name, state, stage string
		rows               func(c Crew, commit string) []ledger.CheckRow
		words              string
	}{
		{"failed at its setup", ledger.AttemptFailed, "setup", func(Crew, string) []ledger.CheckRow { return []ledger.CheckRow{} },
			"approach-b: " + notRunEndedText + " (failed, at setup)"},
		{"interrupted while verifying", ledger.AttemptInterrupted, "verifying", func(c Crew, commit string) []ledger.CheckRow {
			rows := passingRows(c, "approach-b", commit)[:1]
			rows[0].Killed, rows[0].Exit, rows[0].Signal = true, -1, "SIGKILL"
			return rows
		}, "approach-b: " + notRunEndedText + " (interrupted, at verifying)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			c, base, a, b := twoCandidates(t, dir, withChecks(Check{ID: "lint", Argv: []string{"true"}}, Check{ID: "test", Argv: []string{"true"}}),
				[]edit{put("a.txt", "a\n")}, []edit{put("b.txt", "b\n")})
			cr := synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b, rows: tc.rows(c, b), state: ledger.TaskFailed})
			for i, at := range cr.attempts[synthRun] {
				if at.TaskID == "approach-b" {
					cr.attempts[synthRun][i].State, cr.attempts[synthRun][i].Stage = tc.state, tc.stage
				}
			}
			rw := rowOfCheck(t, measure(t, cr), ledger.PhaseCheck, "test")
			if rw.Cells[1].NotRun != NotRunEnded || whyHas(rw, notRunEarlierText) || !slices.Contains(rw.Why, tc.words) || rw.Verdict != VerdictNotComparable {
				t.Fatalf("the row: %+v", rw)
			}
		})
	}
}

// A package.json that does not parse at one side is listed without
// modules, never as every module removed.
func TestAPackageJSONThatDoesNotParseListsNoModules(t *testing.T) {
	dir := newRepo(t)
	applyEdits(t, dir, []edit{put("web/package.json", `{"dependencies":{"vue":"3.4.0","react":"18.0.0"}}`)})
	c, base, a, b := twoCandidates(t, dir, nil,
		[]edit{put("web/package.json", `{"dependencies":{"vue":"3.4.0",}}`)}, []edit{put("web/package.json", `{"dependencies":{"vue":{"v":1}}}`)})
	cmp := measure(t, synthetic(c, base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}))
	for _, task := range []string{"approach-a", "approach-b"} {
		d := column(t, cmp, task).Deps
		if d == nil || !slices.Equal(d.Files, []string{"web/package.json"}) || len(d.Modules) != 0 || d.ModulesTotal != 0 {
			t.Fatalf("%s: %+v", task, d)
		}
	}
}

// A candidate's manifest whose blob git could not read leaves it
// unavailable, and the measure is not kept: the next read finds the steps
// it removed.
func TestAManifestNotReadLeavesTheCandidateUnavailable(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, manifestFile), `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}]}`)
	c, base, a, b := twoCandidates(t, dir, func(c *Crew) { c.Manifest = &Manifest{File: true} },
		[]edit{put("a.txt", "a\n")}, []edit{put(manifestFile, `{"schema": 1}`)})
	was := runGit(t, dir, "rev-parse", base+":"+manifestFile)
	now := runGit(t, dir, "rev-parse", b+":"+manifestFile)
	cr := synthetic(c, base, synthCand{task: "approach-a", commit: a, rows: fileRows("approach-a", a, was, "lint")},
		synthCand{task: "approach-b", commit: b, rows: fileRows("approach-b", b, now)})
	e := &Engine{}
	fail := true
	e.cmpMeasure.git = func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		if fail && args[0] == "cat-file" && args[len(args)-1] == now {
			return "", errors.New("git cat-file: failed")
		}
		return gitcliRunBounded(ctx, dir, limit, args...)
	}
	first := e.measureComparison(t.Context(), cr)
	if cb := column(t, first, "approach-b"); !strings.Contains(cb.Unavailable, "git cat-file: failed") {
		t.Fatalf("approach-b: %+v", cb)
	}
	fail = false
	again := e.measureComparison(t.Context(), cr)
	if rw := rowOfCheck(t, again, ledger.PhaseVerify, "lint"); column(t, again, "approach-b").Unavailable != "" || rw.Cells[1].NotRun != NotRunNoSuchStep {
		t.Fatalf("measured again: %+v", rw)
	}
}
