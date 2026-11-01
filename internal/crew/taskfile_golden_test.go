package crew

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// updateTaskFileGoldens rewrites the goldens under testdata/taskfile from
// fitTaskFile as it is: `go test ./internal/crew -run
// TestTaskFileByteIdenticalWithoutContext -update-taskfile-goldens`. They
// were written by main's fitTaskFile before task files could hold context
// (Program item C4), so a task file with no context block stays byte for
// byte what it was; rewrite them only for a change of the task files meant
// to show in every attempt.
var updateTaskFileGoldens = flag.Bool("update-taskfile-goldens", false, "rewrite testdata/taskfile from fitTaskFile")

// goldenTaskFiles are the task files the goldens hold: a work task, a
// review, a repair a review asked for, several inputs, a checkpoint, a
// project manifest with an attempt that could not be made usable, and one
// cut to fit.
func goldenTaskFiles() map[string]TaskFile {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	base := func(task string, attempt int, kind string) TaskFile {
		return TaskFile{Run: "r-0123456789ab", Crew: "c-build", CrewName: "Build crew", Goal: "Ship the parser, tested.",
			Task: task, Attempt: attempt, MaxAttempts: maxAttemptsPerTask, Member: "lead", Role: "You lead the build.", Kind: kind,
			Instructions: "Write the parser in parse.go & test it: <input> yields a tree.",
			Worktree:     "/repo/.conductor/worktrees/r-0123456789ab/" + task + "." + strconv.Itoa(attempt), Branch: "crew/r-0123456789ab/" + task + "." + strconv.Itoa(attempt),
			Start: strings.Repeat("a", 40), StartFrom: "the run's base", WrittenAt: at}
	}
	out := map[string]TaskFile{}

	work := base("build", 2, TaskWork)
	work.Prefix = "svc"
	work.Inputs = []TaskInput{{Task: "design", Attempt: 1, Commit: strings.Repeat("d", 40), Branch: "crew/r-0123456789ab/design.1", Summary: "The grammar, in grammar.md."}}
	work.StartFrom = "task \"design\"'s result"
	work.Start = strings.Repeat("d", 40)
	work.Checks = []Check{{ID: "test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600}, {ID: "vet", Argv: []string{"go", "vet", "./..."}}}
	work.Previous = &TaskPrevious{Attempt: 1, State: ledger.AttemptFailed, Stage: "check test", Error: "exit 1: parse_test.go:12: want a tree"}
	out["work"] = work

	review := base("check", 1, TaskReview)
	review.Role = ""
	review.Inputs = []TaskInput{{Task: "build", Attempt: 2, Commit: strings.Repeat("b", 40), Branch: "crew/r-0123456789ab/build.2", Summary: "The parser and its tests."}}
	review.Start, review.StartFrom = strings.Repeat("b", 40), "task \"build\"'s result"
	out["review"] = review

	repair := base("build", 3, TaskWork)
	repair.Start, repair.StartFrom = strings.Repeat("b", 40), "the commit review \"check\" asked changes of"
	repair.Repair = &TaskRepair{Review: "check", Attempt: 1, Commit: strings.Repeat("b", 40), Summary: "Two gaps in the tests.",
		Findings: []Finding{{Severity: "major", Path: "parse.go", Line: 40, Message: "An empty input panics."}, {Severity: "nit", Message: "Name the error."}}}
	repair.Previous = &TaskPrevious{Attempt: 2, State: ledger.AttemptSucceeded}
	out["repair"] = repair

	inputs := base("merge", 1, TaskWork)
	inputs.Inputs = []TaskInput{
		{Task: "lexer", Attempt: 1, Commit: strings.Repeat("1", 40), Branch: "crew/r-0123456789ab/lexer.1", Summary: "The lexer."},
		{Task: "parser", Attempt: 2, Commit: strings.Repeat("2", 40), Branch: "crew/r-0123456789ab/parser.2"},
		{Task: "check", Attempt: 1, Commit: strings.Repeat("2", 40), Verdict: VerdictApproved, Summary: "Approved."},
	}
	inputs.Start, inputs.StartFrom = strings.Repeat("1", 40), "task \"lexer\"'s result"
	out["inputs"] = inputs

	checkpoint := base("build", 2, TaskWork)
	checkpoint.Start, checkpoint.StartFrom = strings.Repeat("c", 40), "attempt 1's checkpoint"
	checkpoint.Checkpoint = &Checkpoint{Done: "The lexer.", Remaining: "The parser.", Notes: "Keep the tokens small.", Commit: strings.Repeat("c", 40), At: at.Add(-time.Hour)}
	checkpoint.Previous = &TaskPrevious{Attempt: 1, State: ledger.AttemptFailed, Stage: "session", Error: "the session ended"}
	out["checkpoint"] = checkpoint

	manifest := base("build", 2, TaskWork)
	manifest.Manifest = &TaskManifest{Source: sourceFile, Digest: strings.Repeat("f", 40), Ports: map[string]int{"web": 41000, "api": 41001},
		Preview: "http://127.0.0.1:41000/", Services: []string{"db", "cache"}}
	manifest.Unusable = &TaskUnusable{Attempt: 1, Stage: "setup install", Error: "npm ci: exit 1"}
	out["manifest"] = manifest

	// Cut to fit: the inputs' summaries, the checkpoint and some of the
	// findings go to task-more.json.
	cut := base("build", 3, TaskWork)
	cut.Instructions = strings.Repeat("Instructions. ", 2400)
	for i := range 4 {
		cut.Inputs = append(cut.Inputs, TaskInput{Task: "in" + strconv.Itoa(i), Attempt: 1, Commit: strings.Repeat(strconv.Itoa(i), 40), Summary: strings.Repeat("s", maxSummary)})
	}
	cut.Checkpoint = &Checkpoint{Done: strings.Repeat("d", maxCheckpointField), Remaining: strings.Repeat("m", maxCheckpointField), Commit: strings.Repeat("e", 40), At: at}
	cut.Repair = &TaskRepair{Review: "check", Attempt: 2, Commit: strings.Repeat("b", 40), Summary: strings.Repeat("r", 1000)}
	for i := range 12 {
		cut.Repair.Findings = append(cut.Repair.Findings, Finding{Severity: "minor", Path: "f" + strconv.Itoa(i) + ".go", Line: i + 1, Message: strings.Repeat("x", 2000)})
	}
	out["cut"] = cut
	return out
}

// A task file with no context block is byte for byte what main wrote
// before task files could hold context (Program item C4): task.md,
// task.json and task-more.json, against the goldens.
func TestTaskFileByteIdenticalWithoutContext(t *testing.T) {
	dir := filepath.Join("testdata", "taskfile")
	files := goldenTaskFiles()
	if len(files) < 7 {
		t.Fatalf("%d task files", len(files))
	}
	sawCut := false
	for name, f := range files {
		md, js, more, err := fitTaskFile(f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := map[string][]byte{name + ".md": md, name + ".json": js}
		if more != nil {
			got[name+".more.json"] = more
			sawCut = true
		}
		if *updateTaskFileGoldens {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for file, b := range got {
				if err := os.WriteFile(filepath.Join(dir, file), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		for file, b := range got {
			want, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if !bytes.Equal(b, want) {
				t.Errorf("%s differs from its golden:\n got: %q\nwant: %q", file, b, want)
			}
		}
		// No task-more.json where the golden has none.
		if more == nil {
			if _, err := os.Stat(filepath.Join(dir, name+".more.json")); err == nil {
				t.Errorf("%s: task-more.json has a golden but none was written", name)
			}
		}
	}
	if !sawCut {
		t.Fatal("no task file was cut: the cut golden does not test a cut")
	}
}
