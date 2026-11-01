package crew

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// worstTicket is a task file's ticket at its bounds: a key and a plan id of
// maxTicketRef bytes, a revision past any count, and every attachment's
// file at the derived name's longest.
func worstTicket() *TaskTicket {
	p := Package{Attachments: make([]PackageAttachment, maxPackageAttachments)}
	for i := range p.Attachments {
		p.Attachments[i].File = fmt.Sprintf("%02d-%s", i+1, strings.Repeat("a", 64))
	}
	return &TaskTicket{Key: strings.Repeat("K", maxTicketRef), Rev: 1<<63 - 1, Plan: strings.Repeat("p", maxTicketRef), PlanN: 100, Digest: strings.Repeat("d", 64), Files: packageFilePaths(p)}
}

// task.json names the ticket the run was launched from, never cut however
// much else is: its key, the revision it stood at, the plan revision, the
// package's digest and its files; task.md says the same in its "Your
// ticket" section, right after the instructions, with the title cut to its
// bound on one line and the rule that the run follows the package.
func TestTaskFileNamesTheTicket(t *testing.T) {
	tk := worstTicket()
	var inputs []TaskInput
	for i := range maxTasks - 1 {
		inputs = append(inputs, TaskInput{Task: fmt.Sprintf("in%d", i), Attempt: 1, Commit: strings.Repeat("a", 40), Summary: strings.Repeat("s", maxSummary)})
	}
	f := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: strings.Repeat("<", maxGoal), Instructions: strings.Repeat("<", maxInstructions),
		Start: strings.Repeat("b", 40), Inputs: inputs, Ticket: tk, ticketTitle: cutTitle(strings.Repeat("t", 300) + "\n")}
	md, js, more, err := fitTaskFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if more == nil || len(md) > maxTaskFile || len(js) > maxTaskFile {
		t.Fatalf("task.md %d, task.json %d bytes, cut %v", len(md), len(js), more != nil)
	}
	var kept TaskFile
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept.Ticket, tk) || len(kept.Cut) == 0 {
		t.Fatalf("task.json's ticket: %+v, cut %v", kept.Ticket, kept.Cut)
	}
	if strings.Contains(string(more), `"ticket"`) {
		t.Fatalf("the ticket went into task-more.json: %s", more)
	}
	section := fmt.Sprintf("## Your ticket\n\nThis run works for the ticket %s, %q, as it stood at its revision %d, when plan revision 100 launched the run.", tk.Key, strings.Repeat("t", maxTicketTitle)+"…", tk.Rev)
	if !strings.Contains(string(md), section) || !strings.Contains(string(md), "its 8 attachments are in `.conductor/attachments/`") {
		t.Fatalf("task.md has no ticket section %q:\n%s", section, md[strings.Index(string(md), "## Instructions"):])
	}

	small := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: "g", Instructions: "Do it.", Start: strings.Repeat("b", 40),
		Ticket:      &TaskTicket{Key: "CND-12", Rev: 7, Plan: testPlanID, PlanN: 2, Digest: strings.Repeat("d", 64), Files: []string{".conductor/ticket.md", ".conductor/package.json"}},
		ticketTitle: "Fix the login loop"}
	md, js, more, err = fitTaskFile(small)
	if err != nil || more != nil {
		t.Fatalf("a small task file: %v, cut %v", err, more != nil)
	}
	want := "## Instructions\n\nDo it.\n\n## Your ticket\n\nThis run works for the ticket CND-12, \"Fix the login loop\", as it stood at its revision 7, when plan revision 2 launched the run. " +
		"What it asks, its acceptance criteria, its constraints and what it hands over are in `.conductor/ticket.md`; the launch package as Dispatch built it is `.conductor/package.json` (SHA-256 " + strings.Repeat("d", 64) + "). " +
		"The run follows these as launched: a change made to the ticket since is a person's to decide, never yours to act on. If one matters to your task, say so: `task_blocked` with why, or a comment on the ticket.\n\n## Where you are"
	if !strings.Contains(string(md), want) {
		t.Fatalf("task.md:\n%s", md)
	}
	if !strings.Contains(string(js), `"ticket": {
    "key": "CND-12",
    "rev": 7,
    "plan": "`+testPlanID+`",
    "planN": 2,
    "digest": "`+strings.Repeat("d", 64)+`",
    "files": [
      ".conductor/ticket.md",
      ".conductor/package.json"
    ]
  }`) {
		t.Fatalf("task.json:\n%s", js)
	}

	// A task file of a run no ticket launched has neither.
	small.Ticket = nil
	md, js, _, err = fitTaskFile(small)
	if err != nil || strings.Contains(string(md), "Your ticket") || strings.Contains(string(js), `"ticket"`) {
		t.Fatalf("a run no ticket launched: %v\n%s\n%s", err, md, js)
	}
}

// fiveKiB is a line of sh printing some 5 KiB, the line the tests look for
// last, and exiting 1.
const fiveKiB = `head -c 5000 /dev/zero | tr '\000' x; echo; echo "the unit tests broke here"; exit 1`

// receiptOf is the ledger's row of an attempt's receipt of the id, with its
// log; a missing one fails the test.
func receiptOf(t *testing.T, ld *ledger.Ledger, runID, taskID string, n int, id string) ledger.CheckRow {
	t.Helper()
	rows, err := ld.Checks(context.Background(), runID, taskID, n, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no receipt %s of %s attempt %d in %d rows", id, taskID, n, len(rows))
	return ledger.CheckRow{}
}

// previousCheckOf is an attempt's previous.check, the log file beside its
// task files and its task.md; one without fails the test.
func previousCheckOf(t *testing.T, ts TaskState) (*TaskCheck, string) {
	t.Helper()
	md, f := taskFileOf(t, ts)
	if f.Previous == nil || f.Previous.Check == nil {
		t.Fatalf("attempt %d's task file names no failed check: %+v", ts.Attempt.N, f.Previous)
	}
	return f.Previous.Check, md
}

// logFileOf is the failed check's log as the attempt's files hold it.
func logFileOf(t *testing.T, ts TaskState, c *TaskCheck) []byte {
	t.Helper()
	if c.Log != ".conductor/previous/check-"+c.ID+".log" {
		t.Fatalf("the log's file is %q", c.Log)
	}
	b, err := os.ReadFile(filepath.Join(ts.Attempt.Worktree, filepath.FromSlash(c.Log)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantReceipt fails the test unless c names row, of the run runID, as a
// task file hands it: its identity, its outcome, its tail and its log's
// hash and size, the ledger's.
func wantReceipt(t *testing.T, c *TaskCheck, runID string, row ledger.CheckRow) {
	t.Helper()
	want := fmt.Sprintf("%s/%s/%d/%s/%s/%d", runID, row.TaskID, row.N, row.Phase, row.ID, row.K)
	if c.Receipt != want || c.Run != runID || c.Task != row.TaskID || c.Attempt != row.N || c.Phase != row.Phase || c.ID != row.ID || c.K != row.K {
		t.Fatalf("the receipt's identity: %+v, want %s", c, want)
	}
	if !slices.Equal(c.Argv, row.Argv) || c.Subject != row.Subject || c.Exit != row.Exit || c.Signal != row.Signal || c.TimedOut != row.TimedOut || c.Stable != row.Stable {
		t.Fatalf("the receipt's outcome: %+v, the row's %+v", c, row)
	}
	if c.Tail != row.Tail || c.LogSHA256 != row.LogSHA256 || c.LogDropped != row.LogDropped || c.LogBytes != int64(len(row.Log)) {
		t.Fatalf("the receipt's tail and log: tail %d bytes (%d), sha %s (%s), dropped %v, %d bytes (%d)", len(c.Tail), len(row.Tail), c.LogSHA256, row.LogSHA256, c.LogDropped, c.LogBytes, len(row.Log))
	}
}

// failAndContinue launches a crew whose task build has the check given,
// on a ledger in a directory of its own, fails attempt 1 at it with the
// run paused (so no attempt 2 is made), stops the run and closes the
// ledger, as a server's end does; then a new engine on the ledger opened
// again continues it, unpaused, and its attempt 2 runs. It returns the
// source run's id, the receipt as the first ledger had it, the ledger
// opened again and the continuation's attempt 2.
func failAndContinue(t *testing.T, ch Check) (string, ledger.CheckRow, *ledger.Ledger, TaskState) {
	t.Helper()
	return failAndContinueIn(t, newRepo(t), ch, nil)
}

// failAndContinueIn is failAndContinue in the repository dir; between,
// when set, is given the ledger's directory once the ledger is closed and
// before it is opened again.
func failAndContinueIn(t *testing.T, dir string, ch Check, between func(ldir string)) (string, ledger.CheckRow, *ledger.Ledger, TaskState) {
	t.Helper()
	ldir := t.TempDir()
	ld, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := readyEngine(t)
	e.UseLedger(SQLLedger(ld))
	build := work("build", "lead")
	build.Checks = []Check{ch}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := e.PauseRun(t.Context(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, a, "built.txt", "b\n", "built")
	ledgerAttempt(t, ld, run.ID, "build", 1, ledger.AttemptFailed)
	row := receiptOf(t, ld, run.ID, "build", 1, ch.ID)
	stopRun(t, e, run.ID)
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	if between != nil {
		between(ldir)
	}
	again, err := ledger.Open(ldir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	e2, _ := readyEngine(t)
	e2.UseLedger(SQLLedger(again))
	next, err := e2.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.PauseRun(t.Context(), next.ID, false); err != nil {
		t.Fatal(err)
	}
	a2 := attemptN(t, e2, next.ID, "build", 2)
	t.Cleanup(func() { _ = e2.Stop(context.Background(), next.ID) })
	return run.ID, row, again, a2
}

// The attempt after one that failed at a check is handed that check's
// receipt (DS2's PR-5): task.json's previous.check names the ledger's row
// (its identity, how it ended, the last 2 KiB of its output, its log's
// SHA-256 and size), the whole log is written beside the task files byte
// for byte as the ledger keeps it, and task.md says which check failed on
// which commit, how, its last output and where its log is. A log the
// run's bound dropped is said so, its hash and tail kept; the receipt's
// large parts are cut in order, its identity and log never; and the paths
// a check changed are read back from its check.finished event after a
// restart, never from memory.
func TestAFailedCheckReachesTheNextAttempt(t *testing.T) {
	t.Run("in the run", func(t *testing.T) {
		dir := newRepo(t)
		e, ld := sqlEngine(t)
		build := work("build", "lead")
		build.Checks = []Check{shStep("unit", fiveKiB)}
		run := launchManaged(t, e, repoCrew(dir, build))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "b\n", "built")
		a2 := attemptN(t, e, run.ID, "build", 2)
		row := receiptOf(t, ld, run.ID, "build", 1, "unit")
		c, md := previousCheckOf(t, a2)
		wantReceipt(t, c, run.ID, row)
		if c.Subject != a2.Attempt.Start || len(c.Tail) != checkTail || !strings.HasSuffix(c.Tail, "the unit tests broke here\n") || c.Exit != 1 || len(row.Log) < 5000 {
			t.Fatalf("the receipt handed: subject %s (start %s), tail %d bytes, exit %d, a log of %d", c.Subject, a2.Attempt.Start, len(c.Tail), c.Exit, len(row.Log))
		}
		// The file is the log as sqlLedger reads it, its hash the ledger's.
		got := logFileOf(t, a2, c)
		viaLedger, ok, err := SQLLedger(ld).CheckLog(t.Context(), c.Run, c.Task, c.Attempt, c.ID, c.K)
		if err != nil || !ok || !bytes.Equal(got, viaLedger.Log) || !bytes.Equal(got, row.Log) || sha(got) != c.LogSHA256 {
			t.Fatalf("the log file: %d bytes, the ledger's %d (%v %v), sha ok %v", len(got), len(viaLedger.Log), ok, err, sha(got) == c.LogSHA256)
		}
		for _, want := range []string{
			fmt.Sprintf("The check unit failed on the commit %s, where you start: it exited 1. Its receipt in the ledger is %s.", c.Subject, c.Receipt),
			"Its last output:\n\n```\n" + strings.TrimSuffix(c.Tail, "\n") + "\n```\n\n",
			fmt.Sprintf("Its whole log, %d bytes as the ledger keeps it (SHA-256 %s), is `.conductor/previous/check-unit.log`.", c.LogBytes, c.LogSHA256),
		} {
			if !strings.Contains(md, want) {
				t.Fatalf("task.md does not say %q:\n%s", want, md[strings.Index(md, "## The attempt before"):])
			}
		}
		// Attempt 2 fails the same: attempt 3 is handed attempt 2's receipt.
		commitResult(t, e, a2, "built.txt", "c\n", "again")
		a3 := attemptN(t, e, run.ID, "build", 3)
		c3, _ := previousCheckOf(t, a3)
		wantReceipt(t, c3, run.ID, receiptOf(t, ld, run.ID, "build", 2, "unit"))
		stopRun(t, e, run.ID)
	})

	t.Run("past an attempt not counted", func(t *testing.T) {
		// Attempt 1 fails at its check; attempt 2's worktree cannot be made
		// usable (the setup step fails while the mark is there): attempt 3
		// is handed attempt 1's receipt, as the attempt before it counted.
		dir := newRepo(t)
		e, ld := sqlEngine(t)
		mark := filepath.Join(t.TempDir(), "broken")
		build := work("build", "lead")
		build.Checks = []Check{shStep("unit", fiveKiB)}
		run := launchManaged(t, e, repoManifestCrew(dir, Manifest{Setup: []Check{shStep("deps", `test ! -e "`+mark+`"`)}}, build))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		if err := e.PauseRun(t.Context(), run.ID, true); err != nil {
			t.Fatal(err)
		}
		commitResult(t, e, a, "built.txt", "b\n", "built")
		ledgerAttempt(t, ld, run.ID, "build", 1, ledger.AttemptFailed)
		writeFile(t, mark, "x\n")
		if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if row := ledgerAttempt(t, ld, run.ID, "build", 2, ledger.AttemptFailed); row.Stage != "setup deps" {
			t.Fatalf("attempt 2: %+v", row)
		}
		waitTrue(t, "the run paused at the setup", func() bool { r, _ := e.Get(run.ID); return r.Paused })
		if err := os.Remove(mark); err != nil {
			t.Fatal(err)
		}
		if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		a3 := attemptN(t, e, run.ID, "build", 3)
		c, _ := previousCheckOf(t, a3)
		wantReceipt(t, c, run.ID, receiptOf(t, ld, run.ID, "build", 1, "unit"))
		if _, f := taskFileOf(t, a3); f.Unusable == nil || f.Unusable.Attempt != 2 || f.Previous.Attempt != 1 {
			t.Fatalf("attempt 3's attempts before: %+v %+v", f.Previous, f.Unusable)
		}
		if got := logFileOf(t, a3, c); !bytes.Equal(got, receiptOf(t, ld, run.ID, "build", 1, "unit").Log) {
			t.Fatal("the log file is not attempt 1's")
		}
		stopRun(t, e, run.ID)
	})

	t.Run("a dropped log", func(t *testing.T) {
		dir := newRepo(t)
		e, _, l := managedEngine(t)
		l.mu.Lock()
		l.dropLogs = true
		l.mu.Unlock()
		build := work("build", "lead")
		build.Checks = []Check{shStep("unit", fiveKiB)}
		run := launchManaged(t, e, repoCrew(dir, build))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "b\n", "built")
		a2 := attemptN(t, e, run.ID, "build", 2)
		c, md := previousCheckOf(t, a2)
		if !c.LogDropped || c.Log != "" || c.LogBytes != 0 || len(c.LogSHA256) != 64 || !strings.HasSuffix(c.Tail, "the unit tests broke here\n") {
			t.Fatalf("a dropped log's receipt: %+v", c)
		}
		if _, err := os.Stat(filepath.Join(a2.Attempt.Worktree, taskDir, previousDir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a dropped log has a file: %v", err)
		}
		if want := "Its log was not kept: the run's logs had reached the ledger's bound. Its SHA-256 was " + c.LogSHA256 + "."; !strings.Contains(md, want) || !strings.Contains(md, "Its last output:") {
			t.Fatalf("task.md does not say the log was dropped:\n%s", md)
		}
		stopRun(t, e, run.ID)
	})

	t.Run("the cut order", func(t *testing.T) {
		ctl := strings.Repeat("\x01", 256)
		var argv, changed []string
		for range maxArgs {
			argv = append(argv, ctl)
		}
		for i := range maxHandedChanged {
			changed = append(changed, fmt.Sprintf("%02d", i)+strings.Repeat("\x01", maxHandedPath-2))
		}
		c := &TaskCheck{Receipt: "api-sweep-0000000a/build/1/check/unit/1", Run: "api-sweep-0000000a", Task: "build", Attempt: 1, Phase: ledger.PhaseCheck, ID: "unit", K: 1, Argv: argv,
			Subject: strings.Repeat("c", 40), Exit: 0, Stable: false, Changed: changed, Tail: strings.Repeat("\x01", checkTail), LogSHA256: strings.Repeat("f", 64), LogBytes: 1 << 20, Log: ".conductor/previous/check-unit.log"}
		f := TaskFile{Task: "build", Attempt: 2, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: "g", Instructions: "Do it.", Start: c.Subject,
			Previous: &TaskPrevious{Attempt: 1, State: ledger.AttemptFailed, Stage: "check unit", Error: strings.Repeat("e", 70<<10), Check: c}}
		md, js, more, err := fitTaskFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var kept TaskFile
		var cut TaskMore
		if json.Unmarshal(js, &kept) != nil || json.Unmarshal(more, &cut) != nil || len(md) > maxTaskFile || len(js) > maxTaskFile {
			t.Fatalf("task.md %d, task.json %d bytes", len(md), len(js))
		}
		if want := []string{cutCheckTail, cutCheckChanged, cutCheckArgv, "the rest of the attempt before's error"}; !slices.Equal(kept.Cut, want) {
			t.Fatalf("the cuts: %q, want %q", kept.Cut, want)
		}
		k := kept.Previous.Check
		if k.Tail != "" || k.Changed != nil || k.Argv != nil {
			t.Fatalf("task.json keeps the cut parts: %+v", k)
		}
		whole := *c
		whole.Tail, whole.Changed, whole.Argv = "", nil, nil
		if !reflect.DeepEqual(*k, whole) {
			t.Fatalf("task.json's receipt:\n%+v\nwant\n%+v", *k, whole)
		}
		if m := cut.Previous; m == nil || m.Error != f.Previous.Error || m.Check == nil || !reflect.DeepEqual(*m.Check, *c) {
			t.Fatalf("task-more.json's attempt before: %+v", cut.Previous)
		}
		if s := string(md); !strings.Contains(s, "Its last output is in the file named under \"Cut to fit\".") || !strings.Contains(s, "it did not leave the checkout as the commit has it") ||
			!strings.Contains(s, "Its whole log, 1048576 bytes as the ledger keeps it") {
			t.Fatalf("task.md:\n%s", s[strings.Index(s, "## The attempt before"):])
		}
		if f.Previous.Check.Tail == "" || len(f.Previous.Check.Argv) == 0 {
			t.Fatal("the caller's receipt was cut")
		}
	})

	t.Run("its changed paths after a restart", func(t *testing.T) {
		src, row, ld, a2 := failAndContinue(t, shStep("unit", `echo changed >> README`))
		c, md := previousCheckOf(t, a2)
		wantReceipt(t, c, src, row)
		if !slices.Equal(c.Changed, []string{"README"}) || c.Stable || c.Exit != 0 {
			t.Fatalf("the continuation's receipt: changed %q, stable %v, exit %d", c.Changed, c.Stable, c.Exit)
		}
		if !strings.Contains(md, "it exited 0, and it changed tracked files: README.") {
			t.Fatalf("task.md:\n%s", md[strings.Index(md, "## The attempt before"):])
		}
		// Read from the event the row's transaction committed: a new
		// engine holds nothing in memory, and the row holds no paths.
		if got, err := SQLLedger(ld).CheckChanged(t.Context(), src, "build", 1, "unit", row.K); err != nil || !slices.Equal(got, []string{"README"}) {
			t.Fatalf("the event's paths: %q %v", got, err)
		}
		if !bytes.Equal(logFileOf(t, a2, c), row.Log) {
			t.Fatal("the log file is not the row's")
		}
	})
}

// A project manifest's verify step that fails hands its receipt to the
// next attempt as a task's check does (EN1's PR-5 runs them, #397): phase
// verify, the paths it changed read from its step.finished, its log beside
// the task files byte for byte the ledger's.
func TestAFailedVerifyStepReachesTheNextAttempt(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	m := Manifest{Verify: []Check{shStep("e2e", `echo changed >> README; echo "the e2e suite broke here"; exit 3`)}}
	run := launchManaged(t, e, repoManifestCrew(dir, m, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "b\n", "built")
	a2 := attemptN(t, e, run.ID, "build", 2)
	row := receiptOf(t, ld, run.ID, "build", 1, "e2e")
	c, md := previousCheckOf(t, a2)
	wantReceipt(t, c, run.ID, row)
	if c.Phase != ledger.PhaseVerify || c.Exit != 3 || !slices.Equal(c.Changed, []string{"README"}) || c.Tail != "the e2e suite broke here\n" {
		t.Fatalf("the verify step's receipt: %+v", c)
	}
	if got, err := SQLLedger(ld).CheckChanged(t.Context(), run.ID, "build", 1, "e2e", row.K); err != nil || !slices.Equal(got, []string{"README"}) {
		t.Fatalf("the step.finished's paths: %q %v", got, err)
	}
	if got := logFileOf(t, a2, c); !bytes.Equal(got, row.Log) || sha(got) != row.LogSHA256 {
		t.Fatalf("the log file: %q", got)
	}
	if want := fmt.Sprintf("The project manifest's verify step e2e failed on the commit %s, where you start: it exited 3, and it changed tracked files: README.", c.Subject); !strings.Contains(md, want) {
		t.Fatalf("task.md does not say %q:\n%s", want, md)
	}
	stopRun(t, e, run.ID)
}

// A continuation's first attempt after a failed check is handed the
// source run's receipt: previous.check's receipt and run name the run that
// failed it, never the continuation, and resolve through sqlLedger, after
// a restart, to the original row; the log is byte for byte that row's.
func TestAContinuationsFirstAttemptGetsTheFailedCheck(t *testing.T) {
	src, row, ld, a2 := failAndContinue(t, shStep("unit", fiveKiB))
	c, md := previousCheckOf(t, a2)
	_, f := taskFileOf(t, a2)
	if f.Run == src || c.Run != src || !strings.HasPrefix(c.Receipt, src+"/build/1/check/unit/") {
		t.Fatalf("the continuation %s's receipt: run %s, %s", f.Run, c.Run, c.Receipt)
	}
	wantReceipt(t, c, src, row)
	again, ok, err := SQLLedger(ld).CheckLog(t.Context(), c.Run, c.Task, c.Attempt, c.ID, c.K)
	if err != nil || !ok || !reflect.DeepEqual(again, row) {
		t.Fatalf("the receipt resolved after the restart (%v %v):\n%+v\nthe original\n%+v", ok, err, again, row)
	}
	if got := logFileOf(t, a2, c); !bytes.Equal(got, row.Log) {
		t.Fatalf("the log file: %d bytes, the row's %d", len(got), len(row.Log))
	}
	if f.Previous.Attempt != 1 || f.Previous.Stage != "check unit" || !strings.Contains(md, "Attempt 1 failed at check unit") || !strings.Contains(md, "Its receipt in the ledger is "+c.Receipt) {
		t.Fatalf("the continuation's attempt before: %+v\n%s", f.Previous, md)
	}
}

// A receipt a version 4 database kept, which has no words of its failure
// (the migration to version 5 gives every row an empty error) and whose
// check.finished names no k, is still the one that failed its attempt: a
// continuation of the run, its ledger migrated, hands its first attempt
// the receipt and its whole log, as the failure is the row's outcome, a
// non-zero exit or a checkout left changed (Codex's review of #559, round
// 2).
func TestAContinuationHandsAFailedReceiptAVersion4DatabaseKept(t *testing.T) {
	asVersion4 := func(ldir string) {
		db, err := sql.Open("sqlite", filepath.Join(ldir, ledger.File))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// What versions 5 and 6 added dropped (the ledger package's own
		// tests keep the same list, version5Added), and k out of the
		// check.finished events, which version 4 never wrote there.
		for _, stmt := range []string{"ALTER TABLE runs DROP COLUMN ticket", "ALTER TABLE runs DROP COLUMN package", "ALTER TABLE checks DROP COLUMN phase", "ALTER TABLE checks DROP COLUMN manifest", "ALTER TABLE checks DROP COLUMN ports", "ALTER TABLE checks DROP COLUMN tries",
			"ALTER TABLE checks DROP COLUMN error", "ALTER TABLE checks DROP COLUMN url", "ALTER TABLE runs DROP COLUMN paused_why", "ALTER TABLE runs DROP COLUMN manifest", "DROP TABLE prerequisites", "DROP TABLE trees",
			"UPDATE events SET payload = json_remove(payload, '$.k') WHERE kind = 'check.finished'", "UPDATE schema_version SET version = 4"} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	for _, tc := range []struct {
		name    string
		ch      Check
		changed []string
	}{
		{"it exited non-zero", shStep("unit", fiveKiB), nil},
		{"it changed the checkout", shStep("unit", `echo changed >> README`), []string{"README"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, row, ld, a2 := failAndContinueIn(t, newRepo(t), tc.ch, asVersion4)
			if row.Error == "" {
				t.Fatalf("the receipt had no words before the migration: %+v", row)
			}
			if migrated := receiptOf(t, ld, src, "build", 1, "unit"); migrated.Error != "" || migrated.Phase != ledger.PhaseCheck || migrated.Exit != row.Exit || migrated.Stable != row.Stable {
				t.Fatalf("the migrated receipt: %+v", migrated)
			}
			c, md := previousCheckOf(t, a2)
			wantReceipt(t, c, src, row)
			if !slices.Equal(c.Changed, tc.changed) {
				t.Fatalf("the paths handed: %q, want %q", c.Changed, tc.changed)
			}
			if !bytes.Equal(logFileOf(t, a2, c), row.Log) {
				t.Fatal("the log file is not the row's")
			}
			if !strings.Contains(md, "Its receipt in the ledger is "+c.Receipt) {
				t.Fatalf("task.md:\n%s", md[strings.Index(md, "## The attempt before"):])
			}
		})
	}
}

// A receipt or a log the ledger cannot give the attempt after a failed
// check (its error, or a log that is not the one its SHA-256 names) fails
// that attempt at "receipt", not counted, and pauses the run, as a package
// that cannot be handed over does: unpaused, the attempt after it is
// handed the receipt and its whole log, with one attempt more (the
// independent review of #559).
func TestAReceiptTheLedgerCannotGivePausesUncounted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(e *Engine, l *fakeLedger)
		words string
	}{
		{"its error", func(_ *Engine, l *fakeLedger) { l.failCheckLog = errors.New("disk gone") }, "the ledger could not give the log of the receipt %s: disk gone"},
		{"a log not its hash's", func(_ *Engine, l *fakeLedger) { l.corruptLog = true }, "the log of the receipt %s is not the one its SHA-256 names"},
		// The log read, its file cannot be written (the worktree's disk
		// full): attempt 3 is handed attempt 1's receipt, never attempt 2
		// as the attempt before (Codex's review of #559, round 2).
		{"its file not written", func(e *Engine, _ *fakeLedger) {
			var once atomic.Bool
			e.beforePreviousLog = func() error {
				if once.CompareAndSwap(false, true) {
					return syscall.ENOSPC
				}
				return nil
			}
		}, "the log of the receipt %s could not be written beside the task files: no space left on device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, l := managedEngine(t)
			l.mu.Lock()
			tc.set(e, l)
			l.mu.Unlock()
			build := work("build", "lead")
			build.Checks = []Check{shStep("unit", fiveKiB)}
			run := launchManaged(t, e, repoCrew(dir, build))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			commitResult(t, e, a, "built.txt", "b\n", "built")
			unit := l.rowWhen(t, run.ID, "build", 1, "unit", 1, func(c ledger.CheckRow) bool { return c.State == ledger.CheckFinished })
			words := fmt.Sprintf(tc.words, run.ID+"/build/1/check/unit/1")
			row := l.attemptWhen(t, run.ID, "build", 2, failedRow)
			if row.Stage != receiptStage || row.Error != words || row.SessionID != "" {
				t.Fatalf("attempt 2: %+v", row)
			}
			if l.eventAt(run.ID, "attempt.failed", func(p map[string]any) bool {
				return p["attempt"] == 2 && p["stage"] == receiptStage && p["uncounted"] == true
			}) < 0 {
				t.Fatalf("attempt 2's failure is not uncounted: %v", l.payloads(run.ID, "attempt.failed"))
			}
			why := "attempt 2 could not be handed the receipt of the check that failed the attempt before: " + words
			waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
			if got := fl.launched(); len(got) != 1 {
				t.Fatalf("sessions started: %v", got)
			}
			if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			a3 := attemptN(t, e, run.ID, "build", 3)
			c, md := previousCheckOf(t, a3)
			wantReceipt(t, c, run.ID, unit)
			if got := logFileOf(t, a3, c); !bytes.Equal(got, unit.Log) {
				t.Fatalf("the log file: %d bytes, the row's %d", len(got), len(unit.Log))
			}
			_, f := taskFileOf(t, a3)
			if f.MaxAttempts != maxAttemptsPerTask+1 || f.Previous.Attempt != 1 || f.Unusable == nil || f.Unusable.Attempt != 2 || f.Unusable.Stage != receiptStage || f.Unusable.Error != words {
				t.Fatalf("attempt 3's attempts before: max %d, %+v, %+v", f.MaxAttempts, f.Previous, f.Unusable)
			}
			if !strings.Contains(md, "Attempt 2 could not start: it failed at receipt: "+words+". Conductor could not read from the ledger the receipt") {
				t.Fatalf("attempt 3's task.md:\n%s", md[strings.Index(md, "## The attempt before"):])
			}
			stopRun(t, e, run.ID)
		})
	}
}

// The paths a failed check changed reach the next attempt the same in the
// run as read back from its finished event after a restart, whatever they
// are made of (the reviews of #559): a path that is not valid UTF-8 has
// each such byte replaced as encoding/json writes it, before it is cut,
// and a list that is small in bytes but large as JSON (each "&" six bytes)
// keeps the event within the ledger's bound with its k and its paths,
// never a note that names neither.
func TestTheChangedPathsHandedAreTheEventsWhateverTheirBytes(t *testing.T) {
	t.Run("not valid UTF-8", func(t *testing.T) {
		// Kept as the run keeps them (runCheck), and through a real
		// ledger's event, read back as a continuation reads them.
		raw := []string{"lat\xff\xfen.txt", "ok.txt", "\xff" + strings.Repeat("\xfe", 2000)}
		dir := newRepo(t)
		e, ld := sqlEngine(t)
		build := work("build", "lead")
		run := launchManaged(t, e, repoCrew(dir, build))
		stopRun(t, e, run.ID)
		p := map[string]any{"task": "build", "attempt": 1, "check": "unit", "k": 1, "changed": bounded(raw), "changedCount": len(raw), "error": "changed tracked files"}
		fitFinished(p, raw)
		evs := []event{{"check.finished", p}}
		inRun := handedChanged(eventChanged(evs))
		if _, err := ld.Transition(t.Context(), run.ID, func(tx *ledger.Tx) error { return tx.Event(evs[0].kind, evs[0].payload) }); err != nil {
			t.Fatal(err)
		}
		back, err := SQLLedger(ld).CheckChanged(t.Context(), run.ID, "build", 1, "unit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if read := handedChanged(back); !slices.Equal(inRun, read) {
			t.Fatalf("in the run %q, read back %q", inRun, read)
		}
		if inRun[0] != "lat��n.txt" || len(inRun[2]) > maxHandedPath || !utf8.ValidString(inRun[2]) || !strings.HasPrefix(inRun[2], "��") {
			t.Fatalf("the paths handed: %q", inRun)
		}
	})

	t.Run("large as JSON", func(t *testing.T) {
		// 50 tracked paths and 50 untracked of 160 bytes, within the
		// lists' bounds in bytes, some 96 KiB as JSON together.
		dir := newRepo(t)
		var names []string
		for i := range maxListed {
			name := fmt.Sprintf("%02d", i) + strings.Repeat("&", 158)
			names = append(names, name)
			writeFile(t, filepath.Join(dir, name), "x\n")
		}
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-qm", "ampersands")
		want := names[:maxHandedChanged]
		script := `for f in [0-9][0-9]'&'*; do echo y >> "$f"; echo u > "u$f"; done`

		// In the run: attempt 2 is handed them as the event keeps them.
		e, ld := sqlEngine(t)
		build := work("build", "lead")
		build.Checks = []Check{shStep("amp", script)}
		run := launchManaged(t, e, repoCrew(dir, build))
		a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		commitResult(t, e, a, "built.txt", "b\n", "built")
		a2 := attemptN(t, e, run.ID, "build", 2)
		c, _ := previousCheckOf(t, a2)
		if !slices.Equal(c.Changed, want) {
			t.Fatalf("in the run: %d paths, %q", len(c.Changed), c.Changed)
		}
		row := receiptOf(t, ld, run.ID, "build", 1, "amp")
		if got, err := SQLLedger(ld).CheckChanged(t.Context(), run.ID, "build", 1, "amp", row.K); err != nil || !slices.Equal(got, names) {
			t.Fatalf("the event keeps %d paths (%v)", len(got), err)
		}
		stopRun(t, e, run.ID)

		// After a restart: a continuation's first attempt, the same.
		src, row2, ld2, b2 := failAndContinueIn(t, dir, shStep("amp", script), nil)
		c2, _ := previousCheckOf(t, b2)
		wantReceipt(t, c2, src, row2)
		if !slices.Equal(c2.Changed, want) {
			t.Fatalf("after a restart: %d paths, %q", len(c2.Changed), c2.Changed)
		}
		if got, err := SQLLedger(ld2).CheckChanged(t.Context(), src, "build", 1, "amp", row2.K); err != nil || !slices.Equal(got, names) {
			t.Fatalf("the restarted ledger's event keeps %d paths (%v)", len(got), err)
		}
	})
}
