package crew

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// commitManifestFile commits conductor.manifest.json with content in dir
// (a repository's working tree, or a worktree) and returns its blob id.
func commitManifestFile(t *testing.T, dir, content string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, manifestFile), content)
	commitAll(t, dir, "the manifest")
	return runGit(t, dir, "rev-parse", "HEAD:"+manifestFile)
}

// fileCrew is a managed crew in dir reading its manifest from the file.
func fileCrew(dir string, tasks ...Task) Crew {
	return repoManifestCrew(dir, Manifest{File: true}, tasks...)
}

// The file form is read at each tree's commit: the worktree's at the
// attempt's start, the checkout's at the result, so a result that changes
// the file is checked by the one it changed; each receipt names the blob
// that ran it, two identities on one attempt.
func TestTheFileManifestIsReadAtEachTreesCommit(t *testing.T) {
	dir := newRepo(t)
	first := commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "deps", "argv": ["sh", "-c", "echo first"]}]}`)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("unit", "true")}
	run := launchManaged(t, e, fileCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if rows := l.stepRows(run.ID, "build", 1); len(rows) != 1 || rows[0].ID != "deps" || rows[0].Manifest != "file:"+first || string(rows[0].Log) != "first\n" {
		t.Fatalf("the worktree's receipt:\n%s", rowWords(rows))
	}
	if _, f := taskFileOf(t, a); f.Manifest == nil || f.Manifest.Source != "file" || f.Manifest.Digest != first {
		t.Fatalf("the task file's manifest: %+v", f.Manifest)
	}
	second := commitManifestFile(t, a.Attempt.Worktree, `{"schema": 1, "setup": [{"id": "install", "argv": ["sh", "-c", "echo second"]}]}`)
	submit(t, e, a.Attempt.SessionID, Result{Summary: "a new manifest"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 3 || rows[1].ID != "install" || rows[1].Manifest != "file:"+second || string(rows[1].Log) != "second\n" || rows[2].ID != "unit" {
		t.Fatalf("the attempt's receipts:\n%s", rowWords(rows))
	}
	if r, _ := e.Get(run.ID); r.Manifest == nil || r.Manifest.Source != "file" || r.Manifest.Digest != first {
		t.Fatalf("the run's manifest: %+v", r.Manifest)
	}
	stopRun(t, e, run.ID)
}

// A result whose file the manifest's rules refuse (broken, gone, or naming
// an env name the server does not allow or the host does not have: the env rule holds in every tree,
// so a result's manifest adds no name the operator did not allow) is
// refused at manifest in its checkout, counted, the next attempt starting
// where the refused one did; the words name the file and the commit, or
// the env name.
func TestAResultWhoseManifestCannotBeUsedIsRefusedAtManifest(t *testing.T) {
	t.Setenv("EN1_NOT_ALLOWED", "set but not allowed")
	for _, tc := range []struct {
		name, content, words string
	}{
		{"a broken file", `{"schema": 1, "setup": [`, "conductor.manifest.json at "},
		{"no file", "", "does not hold conductor.manifest.json"},
		{"a name the server does not allow", `{"schema": 1, "env": ["EN1_NOT_ALLOWED"], "setup": [{"id": "deps", "argv": ["true"]}]}`, "EN1_NOT_ALLOWED is not in the server's envPassthrough"},
		{"a name the host does not have", `{"schema": 1, "env": ["EN1_UNSET"], "setup": [{"id": "deps", "argv": ["true"]}]}`,
			"the host has no EN1_UNSET: set it where the server starts, start it again and continue the run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}`)
			e, _, l := managedEngine(t)
			e.SetEnvPassthrough([]string{"EN1_UNSET"})
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", "true")}
			run := launchManaged(t, e, fileCrew(dir, build))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			if tc.content == "" {
				runGit(t, a.Attempt.Worktree, "rm", "-q", manifestFile)
				commitAll(t, a.Attempt.Worktree, "no manifest")
			} else {
				commitManifestFile(t, a.Attempt.Worktree, tc.content)
			}
			submit(t, e, a.Attempt.SessionID, Result{Summary: "changed the manifest"})
			a2 := attemptN(t, e, run.ID, "build", 2)
			row := l.attemptRow(t, run.ID, "build", 1)
			if row.State != ledger.AttemptFailed || row.Stage != "manifest" || !strings.Contains(row.Error, tc.words) || a2.Attempt.Start != a.Attempt.Start {
				t.Fatalf("attempt 1: %+v; attempt 2 starts at %s", row, short(a2.Attempt.Start))
			}
			if l.count(run.ID, "check.started") != 0 {
				t.Fatal("a check ran after the manifest was refused")
			}
			if r, _ := e.Get(run.ID); r.Paused {
				t.Fatal("a refused result paused the run")
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A launch whose base holds a broken file, or none, is refused before a run
// exists, the file and the commit named.
func TestALaunchWithABrokenFileAtTheBaseIsRefused(t *testing.T) {
	for name, content := range map[string]string{"broken": `{"schema": 2}`, "missing": ""} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			if content != "" {
				commitManifestFile(t, dir, content)
			}
			e, _, _ := managedEngine(t)
			_, err := e.Launch(t.Context(), fileCrew(dir, work("build", "lead")))
			head := runGit(t, dir, "rev-parse", "HEAD")
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), manifestFile) || !strings.Contains(err.Error(), short(head)) {
				t.Fatalf("the launch: %v", err)
			}
			if runs := e.List(); len(runs) != 0 {
				t.Fatalf("a refused launch kept a run: %+v", runs)
			}
		})
	}
}

// The base is pinned at the launch: HEAD read once, the commit the file is
// validated at, the run records and its first attempts and prerequisites
// read their manifest at, whatever HEAD does after; a continuation is
// prepared at its run's base, whatever HEAD holds then.
func TestTheBaseIsPinnedAtTheLaunch(t *testing.T) {
	dir := newRepo(t)
	first := commitManifestFile(t, dir, `{"schema": 1, "prerequisites": [{"id": "tools", "argv": ["sh", "-c", "echo pinned"]}], "setup": [{"id": "deps", "argv": ["true"]}]}`)
	base := runGit(t, dir, "rev-parse", "HEAD")
	e, _, l := managedEngine(t)
	e.afterAdd = func(string) {
		commitManifestFile(t, dir, `{"schema": 1, "setup": [{"id": "moved", "argv": ["true"]}]}`)
	}
	run := launchManaged(t, e, fileCrew(dir, work("build", "lead")))
	e.afterAdd = nil
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	l.mu.Lock()
	rowBase := l.runs[run.ID].Base
	l.mu.Unlock()
	if rowBase != base || a.Attempt.Start != base || runGit(t, dir, "rev-parse", "HEAD") == base {
		t.Fatalf("the run's base %s, attempt 1 at %s, the pinned %s", short(rowBase), short(a.Attempt.Start), short(base))
	}
	if p := l.prereqRows(run.ID); len(p) != 1 || p[0].Manifest != "file:"+first || p[0].Subject != base || string(p[0].Log) != "pinned\n" {
		t.Fatalf("the prerequisites: %+v", p)
	}
	if rows := l.stepRows(run.ID, "build", 1); len(rows) != 1 || rows[0].ID != "deps" {
		t.Fatalf("the worktree's receipts:\n%s", rowWords(rows))
	}
	stopRun(t, e, run.ID)
	// HEAD now holds a broken file, then none: the continuation is prepared
	// at its base all the same.
	for _, broken := range []bool{true, false} {
		if broken {
			commitManifestFile(t, dir, `{"schema": 1, "setup": [`)
		} else {
			runGit(t, dir, "rm", "-q", manifestFile)
			commitAll(t, dir, "no manifest")
		}
		e2, _ := readyEngine(t)
		e2.UseLedger(l)
		next, err := e2.ContinueRun(t.Context(), run.ID, "")
		if err != nil {
			t.Fatalf("a continuation with HEAD's file broken %v: %v", broken, err)
		}
		if next.Manifest == nil || next.Manifest.Digest != first {
			t.Fatalf("the continuation's manifest: %+v", next.Manifest)
		}
		stopRun(t, e2, next.ID)
		run = *next
	}
}

// A legacy crew whose setup step shares its id with a task's check still
// launches: the attempt's rows of that id are the setup in the worktree
// (k 1) and in the checkout (k 2), then the check (k 3), each with its
// phase, none overwritten.
func TestALegacySetupThatSharesACheckIDKeepsEveryRow(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{shStep("tests", "echo checked")}
	c := repoCrew(dir, build)
	c.Setup = []Check{shStep("tests", "echo set up")}
	if err := c.ValidateSave(); err != nil {
		t.Fatalf("a legacy crew whose setup shares a check's id: %v", err)
	}
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rows := l.stepRows(run.ID, "build", 1)
	want := []struct {
		phase string
		k     int
		log   string
	}{{ledger.PhaseSetup, 1, "set up\n"}, {ledger.PhaseSetup, 2, "set up\n"}, {ledger.PhaseCheck, 3, "checked\n"}}
	if len(rows) != len(want) {
		t.Fatalf("the rows of tests:\n%s", rowWords(rows))
	}
	for i, w := range want {
		if rows[i].ID != "tests" || rows[i].Phase != w.phase || rows[i].K != w.k || string(rows[i].Log) != w.log {
			t.Fatalf("row %d:\n%s", i, rowWords(rows))
		}
	}
	stopRun(t, e, run.ID)
}
