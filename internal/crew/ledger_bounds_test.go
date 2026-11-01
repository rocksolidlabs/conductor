package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// sqlEngine is an engine on the real ledger, in a directory of the
// test's: the ledger's own bounds apply, as they do on a server.
func sqlEngine(t *testing.T) (*Engine, *ledger.Ledger) {
	t.Helper()
	e, _ := readyEngine(t)
	ld, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ld.Close() })
	if ro, why := ld.ReadOnly(); ro {
		t.Fatalf("the ledger opened read-only: %s", why)
	}
	e.UseLedger(SQLLedger(ld))
	return e, ld
}

// sqlKinds is the kinds of the run's events in the real ledger, in order.
func sqlKinds(t *testing.T, ld *ledger.Ledger, runID string) []string {
	t.Helper()
	evs, err := ld.Events(context.Background(), runID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

// A change whose commit fails with no undo keeps its state ahead and its
// events with it: the pause's commit writes both, so the ledger never
// holds a reopened review's rows without the task.reopened that says so.
func TestAFailedCommitsEventsAreWrittenByTheNextCommit(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	c := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	l.mu.Lock()
	l.failOn, l.failOnce = "task.reopened", true
	l.mu.Unlock()
	submit(t, e, c.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: []Finding{{Severity: "major", Message: "fix it"}}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		kinds := l.kinds(run.ID)
		ro, rp := slices.Index(kinds, "task.reopened"), slices.Index(kinds, "run.paused")
		if ro >= 0 && rp >= 0 {
			if ro > rp {
				t.Fatalf("the reopen's event after the pause's: %v", kinds)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reopen's event was not written with the pause: %v", kinds)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r, _ := e.Get(run.ID); !r.Paused {
		t.Fatal("the run's dispatch is not paused after the refused commit")
	}
	stopRun(t, e, run.ID)
}

// A change undone after its commit failed takes its events with it: they
// are not written by a later commit.
func TestAnUndoneChangesEventsAreNotWritten(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ctx := context.Background()
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.failOn, l.failOnce = "run.paused[paused=false]", true
	l.mu.Unlock()
	if err := e.PauseRun(ctx, run.ID, false); err == nil {
		t.Fatal("an unpause the ledger refused was taken")
	}
	stopRun(t, e, run.ID)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range l.events {
		if p, ok := ev.payload.(map[string]any); ok && ev.runID == run.ID && ev.kind == "run.paused" && p["paused"] == false {
			t.Fatalf("the undone unpause's event was written: %+v", ev)
		}
	}
}

// An event the ledger would refuse for its size becomes a note of it, so
// it cannot fail every commit after it; a path list is bounded by bytes.
func TestAnOversizedEventBecomesANote(t *testing.T) {
	big := strings.Repeat("<", ledger.MaxPayload)
	out := writable([]event{{"check.finished", map[string]any{"task": "build", "attempt": 2, "check": "ok", "untracked": []string{big}}}, {"small", map[string]any{"task": "x"}}})
	note := out[0].payload
	if out[0].kind != "check.finished" || note["oversized"] == nil || note["task"] != "build" || note["attempt"] != 2 || note["check"] != "ok" || note["untracked"] != nil {
		t.Fatalf("the oversized event: %+v", out[0])
	}
	if out[1].payload["task"] != "x" {
		t.Fatalf("the small event: %+v", out[1])
	}
	paths := make([]string, 50)
	for i := range paths {
		paths[i] = fmt.Sprintf("%d/%s", i, strings.Repeat("p", 1450))
	}
	got := bounded(paths)
	size := 0
	for _, p := range got[:len(got)-1] {
		size += len(p)
	}
	if size > maxListedBytes || !strings.HasPrefix(got[len(got)-1], "… and ") || !strings.HasSuffix(got[len(got)-1], " more") {
		t.Fatalf("bounded: %d paths, %d bytes, last %q", len(got), size, got[len(got)-1])
	}
	if b, _ := json.Marshal(map[string]any{"changed": got, "untracked": got}); len(b) > ledger.MaxPayload {
		t.Fatalf("two bounded lists are %d bytes", len(b))
	}
}

// A check that leaves 150 untracked paths of 606 bytes, on the real
// ledger: its check.finished commits (the lists cut short), and the task
// succeeds; before, the event was refused and so was the result. Each
// path stays short enough that the worktree under macOS's long temporary
// directory keeps it below the 1,024 bytes of the Mac's PATH_MAX.
func TestACheckThatLeavesManyLongPathsIsRecorded(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	seg := strings.Repeat("d", 200)
	long := filepath.Join(seg, seg, strings.Repeat("f", 200)) // 602 bytes, and the number
	build := work("build", "lead")
	// The check's timeout is the code's bound on the check: 120 s here,
	// well inside the test binary's 10 minutes, so a check that hangs
	// fails the test by it.
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", `for i in $(seq 100 249); do mkdir -p "$(dirname "$i$1")" && : > "$i$1"; done`, "sh", "/" + long}, TimeoutSeconds: 120}}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, a, "built.txt", "b\n", "built")
	// The check starts some 300 processes, a dirname and a mkdir for each
	// path, and the verify then reads the checkout back: on a loaded
	// machine that takes longer than waitTask's 10 s, which is no cap of
	// the code's. So the test waits on the mechanism, as the run's stop
	// does: the run's checks, on which the submit registered the verify
	// before it returned. The task is decided before the verify is done.
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	r.checks.Wait()
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	// The event keeps its lists, cut short, not the oversized note that
	// stands in for an event too large.
	evs, err := ld.Events(context.Background(), run.ID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var fin *ledger.EventRow
	for i := range evs {
		if evs[i].Kind == "check.finished" {
			fin = &evs[i]
		}
	}
	if fin == nil {
		t.Fatalf("events: %v", sqlKinds(t, ld, run.ID))
	}
	var p struct {
		Untracked      []string
		UntrackedCount int
		Oversized      *int
	}
	if err := json.Unmarshal([]byte(fin.Payload), &p); err != nil || p.Oversized != nil || p.UntrackedCount != 150 || len(p.Untracked) == 0 || len(p.Untracked) >= 150 {
		t.Fatalf("check.finished (%v): %d listed of %d, oversized %v", err, len(p.Untracked), p.UntrackedCount, p.Oversized)
	}
	stopRun(t, e, run.ID)
}

// What an agent sends is measured as the ledger keeps it: a review's
// findings of 1,000 "<" each pass their character bounds and are six
// times that as JSON; the result is refused at submission with nothing
// changed, a smaller one is taken, and the run's commits go on after.
func TestAResultTooLargeAsTheLedgerKeepsItIsRefusedAtSubmission(t *testing.T) {
	dir := newRepo(t)
	e, ld := sqlEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	c := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	var findings []Finding
	for range 20 {
		findings = append(findings, Finding{Severity: "major", Message: strings.Repeat("<", maxFindingText)})
	}
	ctx := context.Background()
	if _, err := e.SubmitResult(ctx, c.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: findings}); !errors.Is(err, ErrBadResult) || !strings.Contains(err.Error(), "as the ledger keeps them") {
		t.Fatalf("the oversized review: %v", err)
	}
	if ts := taskOf(t, e, run.ID, "check"); ts.Attempt == nil || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("the review after the refusal: %+v", ts)
	}
	// Fifty findings of 1,000 "<" with 1,024-byte paths: more than the
	// attempt's row keeps, whatever the repair.
	var fifty []Finding
	for range maxFindings {
		fifty = append(fifty, Finding{Severity: "nit", Path: strings.Repeat("<", maxFindingPath), Message: strings.Repeat("<", maxFindingText)})
	}
	if _, err := e.SubmitResult(ctx, c.Attempt.SessionID, Result{Verdict: VerdictApproved, Findings: fifty}); !errors.Is(err, ErrBadResult) || !strings.Contains(err.Error(), "as the ledger keeps it") {
		t.Fatalf("the oversized approval: %v", err)
	}
	submit(t, e, c.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: findings[:2]})
	waitTaskWhere(t, e, run.ID, "build", "reopened", func(ts TaskState) bool { return ts.Attempts == 2 })
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatalf("a pause after: %v", err)
	}
	if !slices.Contains(sqlKinds(t, ld, run.ID), "task.reopened") {
		t.Fatalf("events: %v", sqlKinds(t, ld, run.ID))
	}
	stopRun(t, e, run.ID)
}

// The task file is cut to fit: with every part at its largest, task.md
// and task.json keep within maxTaskFile, everything cut is in
// task-more.json in full, and the crew's own text is never cut.
func TestTheTaskFileIsCutToFit(t *testing.T) {
	lt := strings.Repeat("<", 1)
	var findings []Finding
	for i := range maxFindings {
		findings = append(findings, Finding{Severity: "major", Path: fmt.Sprintf("p%d/%s", i, strings.Repeat("x", 1000)), Message: strings.Repeat(lt, maxFindingText)})
	}
	var inputs []TaskInput
	for i := range maxTasks - 1 {
		inputs = append(inputs, TaskInput{Task: fmt.Sprintf("in%d-%s", i, strings.Repeat("i", 30)), Attempt: 3, Commit: strings.Repeat("a", 40), Branch: fmt.Sprintf("crew/run-%s/in%d.3", strings.Repeat("r", 20), i), Summary: strings.Repeat("s", maxSummary)})
	}
	var checks []Check
	for i := range maxChecks {
		var argv []string
		for range maxArgs {
			argv = append(argv, strings.Repeat("a", maxArgsBytes/maxArgs))
		}
		checks = append(checks, Check{ID: fmt.Sprintf("c%d", i), Argv: argv, TimeoutSeconds: 600})
	}
	// The manifest's section at its largest (a file's blob of a SHA-256
	// repository, eight ports of the longest names at five digits each, a
	// preview of maxPreview bytes, four services of the longest ids), and
	// the uncounted attempt before at its largest: never cut, and the
	// manifest's at most 2 KiB as the sum of its parts.
	manifest := &TaskManifest{Source: sourceFile, Digest: strings.Repeat("f", 64), Revision: 1<<63 - 1, Ports: map[string]int{}}
	for i := range maxManifestPorts {
		manifest.Ports[fmt.Sprintf("p%d%s", i, strings.Repeat("o", 14))] = maxPort - i
	}
	manifest.Preview = "http://127.0.0.1:65535/" + strings.Repeat("p", maxPreview-len("http://127.0.0.1:65535/"))
	for i := range maxManifestServices {
		manifest.Services = append(manifest.Services, fmt.Sprintf("s%d%s", i, strings.Repeat("v", 38)))
	}
	if len(manifest.Preview) != maxPreview || len(manifest.Services[0]) != 40 {
		t.Fatalf("a preview of %d bytes, an id of %d", len(manifest.Preview), len(manifest.Services[0]))
	}
	if b, _ := json.Marshal(manifest); len(b) > 2<<10 {
		t.Fatalf("the manifest's section is %d bytes", len(b))
	}
	unusable := &TaskUnusable{Attempt: 3, Stage: "setup " + strings.Repeat("s", 40), Error: strings.Repeat("u", 500)}
	f := TaskFile{Task: "build", Attempt: 4, MaxAttempts: 4, Member: "lead", Kind: TaskWork,
		Goal: strings.Repeat("<", maxGoal), Role: strings.Repeat("<", maxPrompt), Instructions: strings.Repeat("<", maxInstructions),
		Start: strings.Repeat("b", 40), Inputs: inputs, Checks: checks, Manifest: manifest, Unusable: unusable, Ticket: worstTicket(), ticketTitle: strings.Repeat("<", maxTicketTitle),
		Previous:   &TaskPrevious{Attempt: 1, State: ledger.AttemptFailed, Stage: "check c0", Error: strings.Repeat("e", 40<<10), Checkpoint: strings.Repeat("c", 64), Branch: "crew/run/build.1"},
		Repair:     &TaskRepair{Review: "check", Attempt: 1, Commit: strings.Repeat("c", 40), Summary: strings.Repeat("r", maxSummary), Findings: findings},
		Checkpoint: &Checkpoint{Done: strings.Repeat("d", maxCheckpointField), Remaining: strings.Repeat("m", maxCheckpointField), Commit: strings.Repeat("e", 40)}}
	md, js, more, err := fitTaskFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(md) > maxTaskFile || len(js) > maxTaskFile || more == nil {
		t.Fatalf("task.md %d, task.json %d bytes, task-more.json %v", len(md), len(js), more != nil)
	}
	var kept TaskFile
	var cut TaskMore
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(more, &cut); err != nil {
		t.Fatal(err)
	}
	if kept.More != taskMoreFile || len(kept.Cut) == 0 || !strings.Contains(string(md), "## Cut to fit") || !strings.Contains(string(md), taskMoreFile) {
		t.Fatalf("what task.json and task.md say of the cut: %q %v", kept.More, kept.Cut)
	}
	if kept.Goal != f.Goal || kept.Role != f.Role || kept.Instructions != f.Instructions || !strings.Contains(string(md), f.Instructions) {
		t.Fatal("the crew's own text was cut")
	}
	if !reflect.DeepEqual(kept.Ticket, f.Ticket) || !strings.Contains(string(md), "## Your ticket\n\nThis run works for the ticket "+f.Ticket.Key) {
		t.Fatalf("the ticket was cut: %+v", kept.Ticket)
	}
	if kept.Manifest == nil || !reflect.DeepEqual(*kept.Manifest, *manifest) || kept.Unusable == nil || *kept.Unusable != *unusable || kept.Previous == nil || kept.Previous.Checkpoint != f.Previous.Checkpoint ||
		!strings.Contains(string(md), "The project manifest `conductor.manifest.json` as this commit holds it") || !strings.Contains(string(md), unusable.Error) ||
		!strings.Contains(string(md), "The tree's state directory is `.conductor/state`") ||
		!strings.Contains(string(md), "Its services "+strings.Join(manifest.Services, ", ")+" run beside your session") ||
		!strings.Contains(string(md), "is "+manifest.Preview+" (`CONDUCTOR_PREVIEW_URL`)") {
		t.Fatalf("the manifest's section or the attempt before was cut: %+v %+v %+v", kept.Manifest, kept.Unusable, kept.Previous)
	}
	for name, n := range manifest.Ports {
		if want := fmt.Sprintf("%s %d (`CONDUCTOR_PORT_%s`)", name, n, strings.ToUpper(name)); !strings.Contains(string(md), want) {
			t.Fatalf("task.md does not name the port %s: %q", name, want)
		}
	}
	keptFindings := 0
	if kept.Repair != nil {
		keptFindings = len(kept.Repair.Findings)
	}
	if keptFindings+len(cut.Findings) != maxFindings || cut.Checkpoint == nil || len(cut.Inputs) != len(inputs) {
		t.Fatalf("findings %d kept and %d cut; checkpoint cut %v; summaries cut %d", keptFindings, len(cut.Findings), cut.Checkpoint != nil, len(cut.Inputs))
	}
	// Every input is in one place or the other, each once, with its commit.
	for _, in := range inputs[1:] {
		n := 0
		for _, c := range cut.Inputs {
			if c.Task == in.Task && c.Commit == in.Commit && c.Summary == in.Summary {
				n++
			}
		}
		if n != 1 && !slices.ContainsFunc(kept.Inputs, func(k TaskInput) bool { return k.Task == in.Task }) {
			t.Fatalf("input %s is not kept once: %d", in.Task, n)
		}
	}
	if len(kept.Checks) > 0 && len(cut.Checks) > 0 || len(kept.Checks)+len(cut.Checks) != len(checks) {
		t.Fatalf("checks kept %d, cut %d", len(kept.Checks), len(cut.Checks))
	}
	if strings.Contains(string(js), "\\"+"u003c") {
		t.Fatal("task.json escapes <")
	}
	if inputs[0].Summary == "" || len(checks[0].Argv) == 0 {
		t.Fatal("the caller's task file was cut")
	}
	// A task file that fits is written whole, with no task-more.json.
	small := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Instructions: "Do it.", Inputs: inputs[:1]}
	if _, js, more, err := fitTaskFile(small); err != nil || more != nil || strings.Contains(string(js), `"more"`) {
		t.Fatalf("a small task file: more %v, %v", more != nil, err)
	}
}

// A dispatch whose repair would pass the task file's bound writes the cut
// files and launches: no attempt fails at "task file" for its size.
func TestARepairPastTheTaskFilesBoundStillLaunches(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	// Four bytes a character, which JSON keeps as they are: a repair the
	// ledger keeps (about 40 KB) beside 32 KB of instructions.
	clef := "\U0001D11E"
	build := work("build", "lead")
	build.Instructions = strings.Repeat(clef, maxInstructions)
	run := launchManaged(t, e, repoCrew(dir, build, review("check", "critic", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	c := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	var findings []Finding
	for i := range 10 {
		findings = append(findings, Finding{Severity: "major", Path: fmt.Sprintf("f%d.go", i), Message: strings.Repeat(clef, maxFindingText)})
	}
	submit(t, e, c.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Findings: findings})
	b2 := waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	for _, name := range []string{"task.md", "task.json", taskMoreFile} {
		fi, err := os.Stat(filepath.Join(b2.Attempt.Worktree, taskDir, name))
		if err != nil || (name != taskMoreFile && fi.Size() > maxTaskFile) {
			t.Fatalf("%s: %v %v", name, fi, err)
		}
	}
	stopRun(t, e, run.ID)
}

// A command's receipt keeps what idempotency needs, not the whole task: an
// Accept anyway of a result whose issues and summary are near their
// bounds, beside a checkpoint near its own, settles on the real ledger,
// and the same command again answers with the same receipt.
func TestAReceiptFitsWhateverTheTaskHolds(t *testing.T) {
	dir := newRepo(t)
	e, _ := sqlEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "never", Argv: []string{"false"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	ctx := context.Background()
	var issues []string
	for range maxIssues {
		issues = append(issues, strings.Repeat("<", maxIssue))
	}
	big := Result{Summary: strings.Repeat("<", maxSummary), Issues: issues}
	for n := 1; n <= maxAttemptsPerTask; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", "running", func(ts TaskState) bool {
			return ts.Attempts == n && ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
		})
		writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), strings.Repeat("try\n", n))
		commitAll(t, a.Attempt.Worktree, "try")
		if n == maxAttemptsPerTask {
			if _, err := e.SaveCheckpoint(ctx, a.Attempt.SessionID, strings.Repeat("<", 1000), strings.Repeat("<", 1000), ""); err != nil {
				t.Fatal(err)
			}
		}
		submit(t, e, a.Attempt.SessionID, big)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	rc, err := e.Accept(ctx, run.ID, "admin", Command{ID: "big"}, "build")
	if err != nil || rc.Status != ledger.CommandDone || rc.Task == nil || rc.Task.State != ledger.TaskSucceeded || rc.Task.Result == nil || rc.Task.Result.Attempt != 3 {
		t.Fatalf("accept: %+v %v", rc, err)
	}
	if b, _ := json.Marshal(rc); len(b) > 4<<10 {
		t.Fatalf("the receipt is %d bytes", len(b))
	}
	if again, err := e.Accept(ctx, run.ID, "admin", Command{ID: "big"}, "build"); err != nil || !again.Repeat || again.Task == nil || again.Task.Result == nil || again.Task.Result.Commit != rc.Task.Result.Commit {
		t.Fatalf("the same command again: %+v %v", again, err)
	}
	stopRun(t, e, run.ID)
}

// A refused change's events are kept with its rows as the refusal comes,
// under the run's transaction lock: the next commit writes them first,
// whoever makes it (another task's change before any pause), and a pause
// that is refused too keeps its own for the commit after.
func TestARefusedChangesEventsGoWithTheNextCommitWhoeverMakesIt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	ctx := context.Background()
	commit := func(kind string) error {
		return e.transact(ctx, r, func() tx { return ev(kind, map[string]any{"n": kind}) }, nil)
	}
	l.mu.Lock()
	l.failOn, l.failOnce = "first", true
	l.mu.Unlock()
	if err := commit("first"); err == nil {
		t.Fatal("the refused commit was taken")
	}
	l.mu.Lock()
	l.failOn, l.failOnce = "second", true
	l.mu.Unlock()
	if err := commit("second"); err == nil {
		t.Fatal("the second refused commit was taken")
	}
	if err := commit("third"); err != nil {
		t.Fatal(err)
	}
	kinds := l.kinds(run.ID)
	i1, i2, i3 := slices.Index(kinds, "first"), slices.Index(kinds, "second"), slices.Index(kinds, "third")
	if i1 < 0 || i2 != i1+1 || i3 != i2+1 {
		t.Fatalf("the events in order with the commit that took them: %v", kinds)
	}
	// A change given up drops its events.
	l.mu.Lock()
	l.failOn, l.failOnce = "dropped", true
	l.mu.Unlock()
	if err := e.transact(ctx, r, func() tx { return ev("dropped", nil) }, giveUp); err == nil {
		t.Fatal("the refused commit was taken")
	}
	if err := commit("fourth"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.kinds(run.ID), "dropped") {
		t.Fatalf("a change given up was written: %v", l.kinds(run.ID))
	}
	stopRun(t, e, run.ID)
}

// A crew whose task could make a task file with no room left is refused
// as it is saved: a need twice, and a goal, role and instructions that as
// JSON leave no room for the rest.
func TestACrewIsRefusedWhenItsTaskFileCouldNotFit(t *testing.T) {
	dir := newRepo(t)
	twice := repoCrew(dir, work("build", "lead"), work("docs", "lead", "build", "build"))
	if err := twice.ValidateSave(); err == nil || !strings.Contains(err.Error(), "needs \"build\" twice") {
		t.Fatalf("a need twice: %v", err)
	}
	// Read or continued, a crew saved before is held to the rules it was
	// saved under.
	if err := twice.Validate(); err != nil {
		t.Fatalf("a crew saved before, read: %v", err)
	}
	wide := "\U0001D11E"
	big := repoCrew(dir, work("build", "lead"))
	big.Goal = strings.Repeat(wide, maxGoal)
	big.Members[0].Prompt = strings.Repeat(wide, maxPrompt)
	big.Tasks[0].Instructions = strings.Repeat(wide, maxInstructions)
	if err := big.ValidateSave(); err == nil || !strings.Contains(err.Error(), "as its task file keeps them") {
		t.Fatalf("a task's text past its room: %v", err)
	}
	if err := big.Validate(); err != nil {
		t.Fatalf("a crew saved before, read: %v", err)
	}
	big.Members[0].Prompt = ""
	if err := big.ValidateSave(); err != nil {
		t.Fatalf("within its room: %v", err)
	}
	// A run of a crew with a need twice (saved before the rule) starts the
	// task with that input once.
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, twice)
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	commitResult(t, e, b, "built.txt", "b\n", "built")
	d := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	if _, f := taskFileOf(t, d); len(f.Inputs) != 1 {
		t.Fatalf("docs' inputs: %+v", f.Inputs)
	}
	stopRun(t, e, run.ID)
}

// A message's typing record the ledger refuses is not left set: the next
// delivery records the intent before it types the line.
func TestARefusedTypingRecordIsMadeAgainBeforeTheLine(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer", "build")))
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	m, err := e.SendMessage(context.Background(), b.Attempt.SessionID, "writer", "the API is final")
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	l.mu.Lock()
	l.failOn, l.failOnce = "message.typing", true
	l.mu.Unlock()
	if e.typing(r, m.Seq) {
		t.Fatal("a refused typing record was taken")
	}
	e.mu.Lock()
	set := !r.messageBySeq(m.Seq).typingAt.IsZero()
	e.mu.Unlock()
	if set {
		t.Fatal("the refused typing record is left set")
	}
	if !e.typing(r, m.Seq) {
		t.Fatal("the typing record was not made again")
	}
	if n := l.count(run.ID, "message.typing"); n != 1 {
		t.Fatalf("message.typing events: %d", n)
	}
	stopRun(t, e, run.ID)
}

// Every cut ends: with nothing but the attempt before's error to cut, the
// task file is cut once and fits, or is an error, never a loop.
func TestTheTaskFilesCutsEnd(t *testing.T) {
	f := TaskFile{Task: "build", Attempt: 2, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Instructions: strings.Repeat("<", maxInstructions),
		Previous: &TaskPrevious{Attempt: 1, State: ledger.AttemptFailed, Stage: "check c0", Error: strings.Repeat("e", 100<<10)}}
	md, js, more, err := fitTaskFile(f)
	if err != nil || len(md) > maxTaskFile || len(js) > maxTaskFile || more == nil {
		t.Fatalf("md %d, js %d, more %v, %v", len(md), len(js), more != nil, err)
	}
	var cut TaskMore
	if err := json.Unmarshal(more, &cut); err != nil || cut.Previous == nil || len(cut.Previous.Error) != 100<<10 {
		t.Fatalf("the whole error in task-more.json: %v", err)
	}
	// Text no cut can make fit (no crew's validation lets it through): an
	// error, after each cut once.
	f.Instructions = strings.Repeat("<", 70<<10)
	if _, _, _, err := fitTaskFile(f); err == nil || !strings.Contains(err.Error(), "with every part cut that can be") {
		t.Fatalf("an uncuttable task file: %v", err)
	}
}

// A command whose taking the ledger refuses is not taken: no acceptance is
// written for it later, and sent again it is taken once.
func TestARefusedCommandIsNotAcceptedLater(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ctx := context.Background()
	l.mu.Lock()
	l.failOn, l.failOnce = "command.accepted", true
	l.mu.Unlock()
	if _, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true); err == nil {
		t.Fatal("a refused command was taken")
	}
	if rc, err := e.Pause(ctx, run.ID, "admin", Command{ID: "p1"}, true); err != nil || rc.Status != ledger.CommandDone || rc.Repeat {
		t.Fatalf("the command again: %+v %v", rc, err)
	}
	if n := l.count(run.ID, "command.accepted"); n != 1 {
		t.Fatalf("command.accepted events: %d", n)
	}
	stopRun(t, e, run.ID)
}

// What happened stays in the ledger when its record is refused once: a
// launch, and a setup command that ran, are written with the failure
// that follows.
func TestALaunchAndASetupThatHappenedKeepTheirEvents(t *testing.T) {
	for _, kind := range []string{"attempt.launched", "step.finished"} {
		t.Run(kind, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			c := repoCrew(dir, work("build", "lead"))
			c.Setup = []Check{{ID: "deps", Argv: []string{"true"}}}
			l.mu.Lock()
			l.failOn, l.failOnce = kind, true
			l.mu.Unlock()
			run := launchManaged(t, e, c)
			if kind == "step.finished" {
				// The setup step's receipt not recorded fails the worktree at
				// that step: not counted, the dispatch paused; unpaused, the
				// next attempt runs.
				attemptFailed(t, e, run.ID, "build", 1)
				// The run as read is one change ahead of the ledger while
				// that change commits: the row is waited for in the ledger.
				if row := l.attemptWhen(t, run.ID, "build", 1, failedRow); row.Stage != "setup deps" || !strings.Contains(row.Error, "the ledger could not record the step deps") {
					t.Fatalf("attempt 1: %+v", row)
				}
				waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused })
				pauseRun(t, e, run.ID, false)
			}
			waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
				return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
			})
			kinds := l.kinds(run.ID)
			i, f := slices.Index(kinds, kind), slices.Index(kinds, "attempt.failed")
			if i < 0 || f < 0 || i > f {
				t.Fatalf("%s with the failure after it: %v", kind, kinds)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A refused change's rows of its own (a message's, a command's receipt)
// wait with its events: the next commit writes both, so the ledger never
// holds the events of a change without its rows.
func TestARefusedChangesOwnRowsGoWithItsEvents(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "lead", "build")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	ctx := context.Background()
	row := ledger.MessageRow{RunID: run.ID, Seq: 77, From: "lead", To: "lead", Text: "kept", CreatedAt: time.Now().UTC()}
	l.mu.Lock()
	l.failOn, l.failOnce = "with.row", true
	l.mu.Unlock()
	if err := e.transact(ctx, r, func() tx {
		w := ev("with.row", nil)
		w.more = func(ltx LedgerTx) error { return ltx.PutMessage(row) }
		return w
	}, nil); err == nil {
		t.Fatal("the refused commit was taken")
	}
	if err := e.transact(ctx, r, func() tx { return ev("after", nil) }, nil); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	_, kept := l.messages[fmt.Sprintf("%s/%d", run.ID, row.Seq)]
	l.mu.Unlock()
	if !kept || !slices.Contains(l.kinds(run.ID), "with.row") {
		t.Fatalf("the refused change's row %v, its event %v", kept, l.kinds(run.ID))
	}
	// A cancel whose settling is refused once: the receipt is settled by
	// the commit after, not left accepted.
	l.mu.Lock()
	l.failOn, l.failOnce = "command.cancel", true
	l.mu.Unlock()
	if _, err := e.Cancel(ctx, run.ID, "admin", Command{ID: "c1"}, "docs"); err == nil {
		t.Fatal("the refused cancel was taken")
	}
	if err := e.transact(ctx, r, func() tx { return ev("after.cancel", nil) }, nil); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	cmdRow := l.commands[run.ID+"/admin/c1"]
	l.mu.Unlock()
	if cmdRow.Status != ledger.CommandDone || cmdRow.Receipt == "" {
		t.Fatalf("the cancel's command: %+v", cmdRow)
	}
	stopRun(t, e, run.ID)
}

// A run holding a refused change's events or rows is not idle, so it is
// not forgotten (evict) before they are written: a stopped run with a
// write pending is kept until the next commit takes it.
func TestARunWithAWritePendingIsNotForgotten(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	e.mu.Lock()
	r := e.runs[run.ID]
	e.mu.Unlock()
	idle := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.idle(r)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !idle() {
		if time.Now().After(deadline) {
			t.Fatal("the stopped run is not idle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx := context.Background()
	l.mu.Lock()
	l.failOn, l.failOnce = "late", true
	l.mu.Unlock()
	if err := e.transact(ctx, r, func() tx {
		w := ev("late", nil)
		w.more = func(LedgerTx) error { return nil }
		return w
	}, nil); err == nil {
		t.Fatal("the refused commit was taken")
	}
	if idle() {
		t.Fatal("a run with a write pending is idle: it could be forgotten with it")
	}
	if err := e.transact(ctx, r, func() tx { return ev("flush", nil) }, nil); err != nil {
		t.Fatal(err)
	}
	if !idle() || !slices.Contains(l.kinds(run.ID), "late") {
		t.Fatalf("after the flush: idle %v, events %v", idle(), l.kinds(run.ID))
	}
}
