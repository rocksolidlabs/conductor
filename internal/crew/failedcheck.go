package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A failed check reaches the next attempt (DS2's PR-5,
// docs/plans/dispatch-evidence.md): the attempt after one that failed at a
// task's check or a project manifest's verify step is handed that check's
// receipt in its task.json (previous.check: the ledger's identity of the
// row, how it ended, the last of its output, the tracked paths it changed)
// and its whole log as a file beside the task files, read from the ledger.
// In the run the receipt is the one verify kept on the attempt; after a
// restart, and for a continuation, it is read back from the rows, the
// changed paths from the finished event committed with the row.

// Bounds of the handed receipt.
const (
	// maxHandedChanged is how many of the tracked paths a failed check
	// changed the next attempt is told, and maxHandedPath each one's bytes.
	maxHandedChanged = 10
	maxHandedPath    = 1024
	// previousDir is the directory of the failed check's log, in the
	// attempt's .conductor.
	previousDir = "previous"
)

// TaskCheck is the receipt of the check that failed the attempt before, as
// task.json's previous.check names it. Receipt is the ledger's identity of
// the row, "<run>/<task>/<n>/<phase>/<id>/<k>", Run the run whose ledger
// holds it (the source run's for a continuation's first attempt, never
// the continuation's), Task and Attempt the attempt that failed it, Phase
// "check" (a task's check) or "verify" (a project manifest's verify step).
// Subject is the commit it checked, the candidate it failed; Exit, Signal,
// TimedOut and Stable how it ended (Stable false when it left the checkout
// changed); Changed at most maxHandedChanged of the tracked paths it
// changed, each cut to maxHandedPath bytes; Tail the last 2 KiB of its
// output as the ledger keeps it. Log is the whole log's file, relative to
// the working directory, LogBytes its size and LogSHA256 its hash, the
// ledger's; a log the run's bound dropped is LogDropped, with no file, its
// hash and tail kept. The identity, the outcome and the log's fields are
// never cut; Tail, Changed and Argv go into task-more.json, in that order,
// before the attempt before's error does (fitTaskFile).
type TaskCheck struct {
	Receipt    string   `json:"receipt"`
	Run        string   `json:"run"`
	Task       string   `json:"task"`
	Attempt    int      `json:"attempt"`
	Phase      string   `json:"phase"`
	ID         string   `json:"id"`
	K          int      `json:"k"`
	Argv       []string `json:"argv,omitempty"`
	Subject    string   `json:"subject"`
	Exit       int      `json:"exit"`
	Signal     string   `json:"signal,omitempty"`
	TimedOut   bool     `json:"timedOut"`
	Stable     bool     `json:"stable"`
	Changed    []string `json:"changed,omitempty"`
	Tail       string   `json:"tail,omitempty"`
	LogSHA256  string   `json:"logSha256"`
	LogBytes   int64    `json:"logBytes"`
	Log        string   `json:"log,omitempty"`
	LogDropped bool     `json:"logDropped,omitempty"`
}

// failedCheck is the receipt that failed an attempt, as the next one is
// handed it: the run whose ledger holds the row, the row without its log,
// and the tracked paths its command changed (handedChanged).
type failedCheck struct {
	run     string
	row     ledger.CheckRow
	changed []string
}

// priorFailed is, in a continuation, the last counted attempt of the runs
// it continues when that attempt failed at a check or a verify step whose
// receipt the ledger has: the continuation's next attempt is handed it,
// as an attempt of the run itself would be.
type priorFailed struct {
	n                                 int
	state, stage, err, result, branch string
	check                             *failedCheck
}

// stage is the stage an attempt this receipt failed fails at.
func (fc *failedCheck) stage() string { return fc.row.Phase + " " + fc.row.ID }

// handed is the receipt as a task file names it, its log's file and size
// not read yet (previousLog).
func (fc *failedCheck) handed() *TaskCheck {
	r := fc.row
	return &TaskCheck{Receipt: fmt.Sprintf("%s/%s/%d/%s/%s/%d", fc.run, r.TaskID, r.N, r.Phase, r.ID, r.K), Run: fc.run, Task: r.TaskID, Attempt: r.N, Phase: r.Phase, ID: r.ID, K: r.K,
		Argv: append([]string(nil), r.Argv...), Subject: r.Subject, Exit: r.Exit, Signal: r.Signal, TimedOut: r.TimedOut, Stable: r.Stable,
		Changed: append([]string(nil), fc.changed...), Tail: r.Tail, LogSHA256: r.LogSHA256}
}

// failedAtCheck reports whether an attempt's row failed at a task's check
// or a verify step, and which: the stage verify wrote, "<phase> <id>".
func failedAtCheck(a ledger.AttemptRow) (phase, id string, ok bool) {
	if a.State != ledger.AttemptFailed {
		return "", "", false
	}
	phase, id, ok = strings.Cut(a.Stage, " ")
	if !ok || (phase != ledger.PhaseCheck && phase != ledger.PhaseVerify) || !ValidTaskID(id) {
		return "", "", false
	}
	return phase, id, true
}

// failingReceipt is, of an attempt's receipts (rows, by id and k), the one
// that failed it: the last finished run of the check or verify step its
// stage names. None when the attempt did not fail at one, or the ledger
// kept no finished row of it (its outcome refused).
func failingReceipt(a ledger.AttemptRow, rows []ledger.CheckRow) (ledger.CheckRow, bool) {
	phase, id, ok := failedAtCheck(a)
	if !ok {
		return ledger.CheckRow{}, false
	}
	var out ledger.CheckRow
	found := false
	for _, r := range rows {
		if r.TaskID == a.TaskID && r.N == a.N && r.ID == id && r.Phase == phase && r.State == ledger.CheckFinished && (!found || r.K > out.K) {
			out, found = r, true
		}
	}
	out.Log = nil
	return out, found
}

// handedChanged is what the next attempt is told of the paths a check
// changed: the first maxHandedChanged as the check's finished event lists
// them (bounded), each made valid UTF-8 as JSON keeps it and cut to
// maxHandedPath bytes, so the list read back from the event after a
// restart is the one kept in memory.
func handedChanged(paths []string) []string {
	var out []string
	for _, p := range paths {
		if len(out) == maxHandedChanged {
			break
		}
		out = append(out, cutBytes(strings.ToValidUTF8(p, "�"), maxHandedPath))
	}
	return out
}

// cutBytes is s cut to at most n bytes, at a character's boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// checkLog is the failed check's log as the next attempt's files hold it:
// its name in .conductor/previous, and its bytes.
type checkLog struct {
	name string
	data []byte
}

// previousLog reads the log of c's receipt from the ledger and fills in
// what c says of it: its file, its size and its hash, or that the run's
// bound dropped it (no file then, its hash and tail kept). A receipt the
// ledger cannot give, or a log that is not the one its hash names, is the
// error: the attempt is never started without the whole log it is told of.
func (e *Engine) previousLog(ctx context.Context, c *TaskCheck) (*checkLog, error) {
	if !ValidTaskID(c.ID) {
		return nil, fmt.Errorf("the failed check's id %q is not one a file can be named after", c.ID)
	}
	row, ok, err := e.ledger.CheckLog(ctx, c.Run, c.Task, c.Attempt, c.ID, c.K)
	if err != nil {
		return nil, fmt.Errorf("the ledger could not give the log of the receipt %s: %w", c.Receipt, err)
	}
	if !ok {
		return nil, fmt.Errorf("the ledger has no receipt %s", c.Receipt)
	}
	c.LogSHA256 = row.LogSHA256
	if row.LogDropped {
		c.LogDropped, c.LogBytes, c.Log = true, 0, ""
		return nil, nil
	}
	sum := sha256.Sum256(row.Log)
	if hex.EncodeToString(sum[:]) != row.LogSHA256 {
		return nil, fmt.Errorf("the log of the receipt %s is not the one its SHA-256 names", c.Receipt)
	}
	name := "check-" + c.ID + ".log"
	c.LogBytes, c.Log = int64(len(row.Log)), path.Join(taskDir, previousDir, name)
	return &checkLog{name: name, data: row.Log}, nil
}

// writeFailedCheck writes task.md's account of the check that failed the
// attempt before: which check, on which commit, how it ended, its last
// output in a fenced block, and where its whole log is.
func writeFailedCheck(b *strings.Builder, f TaskFile, c *TaskCheck) {
	what := fmt.Sprintf("The check %s", c.ID)
	if c.Phase == ledger.PhaseVerify {
		what = fmt.Sprintf("The project manifest's verify step %s", c.ID)
	}
	fmt.Fprintf(b, "%s failed on the commit %s", what, c.Subject)
	if c.Subject == f.Start {
		b.WriteString(", where you start")
	}
	switch {
	case c.TimedOut:
		b.WriteString(": it timed out")
	case c.Signal != "":
		fmt.Fprintf(b, ": it was ended by the signal %s", c.Signal)
	default:
		fmt.Fprintf(b, ": it exited %d", c.Exit)
	}
	switch {
	case len(c.Changed) > 0:
		fmt.Fprintf(b, ", and it changed tracked files: %s", strings.Join(c.Changed, ", "))
	case !c.Stable:
		b.WriteString(", and it did not leave the checkout as the commit has it")
	}
	fmt.Fprintf(b, ". Its receipt in the ledger is %s.\n\n", c.Receipt)
	switch {
	case c.Tail != "":
		tail := strings.TrimSuffix(strings.ToValidUTF8(c.Tail, "�"), "\n")
		fence := strings.Repeat("`", max(3, longestBacktickRun(tail)+1))
		fmt.Fprintf(b, "Its last output:\n\n%s\n%s\n%s\n\n", fence, tail, fence)
	case slices.Contains(f.Cut, cutCheckTail):
		b.WriteString("Its last output is in the file named under \"Cut to fit\".\n\n")
	}
	if c.LogDropped {
		fmt.Fprintf(b, "Its log was not kept: the run's logs had reached the ledger's bound. Its SHA-256 was %s.\n\n", c.LogSHA256)
		return
	}
	fmt.Fprintf(b, "Its whole log, %d bytes as the ledger keeps it (SHA-256 %s), is `%s`.\n\n", c.LogBytes, c.LogSHA256, c.Log)
}

// What fitTaskFile says it cut of the failed check.
const (
	cutCheckTail    = "the failed check's last output"
	cutCheckChanged = "the paths the failed check changed"
	cutCheckArgv    = "the failed check's command"
)
