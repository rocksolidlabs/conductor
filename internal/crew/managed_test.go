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
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// fakeLedger is the managed runs' ledger in memory: every transition's
// events and rows, a revision per transition, and one event kind whose
// transition fails when failOn names it.
type fakeLedger struct {
	mu       sync.Mutex
	rev      int64
	events   []fakeEvent
	runs     map[string]ledger.RunRow
	tasks    map[string]ledger.TaskRow
	attempts map[string]ledger.AttemptRow
	checks   []ledger.CheckRow
	commands map[string]ledger.CommandRow
	messages map[string]ledger.MessageRow
	// prerequisites are every prerequisite's row as put, in order, and
	// trees each tree's row as last put, by run, task, attempt and kind.
	prerequisites []ledger.PrerequisiteRow
	trees         map[string]ledger.TreeRow
	// commandCap, when set, is how many commands a run takes (the ledger's
	// 4096), for a test of the cap.
	commandCap int
	// failOn names the events whose transaction fails (comma-separated;
	// "kind[key=value]" matches a payload field too); failOnce clears it
	// after one failure. blockOn names events whose transaction waits,
	// once blocked is told, for release to close.
	failOn   string
	failOnce bool
	blockOn  string
	blocked  chan struct{}
	release  chan struct{}
	// unavailable, when set, is what Available returns.
	unavailable error
	// failLoad, when set, names a run whose next Load fails, once.
	failLoad string
}

// matches reports whether an event is named by spec.
func matches(spec string, ev fakeEvent) bool {
	for _, one := range strings.Split(spec, ",") {
		one = strings.TrimSpace(one)
		if one == "" {
			continue
		}
		kind, cond, _ := strings.Cut(one, "[")
		if kind != ev.kind {
			continue
		}
		if cond == "" {
			return true
		}
		key, value, _ := strings.Cut(strings.TrimSuffix(cond, "]"), "=")
		if payload, ok := ev.payload.(map[string]any); ok && fmt.Sprint(payload[key]) == value {
			return true
		}
	}
	return false
}

type fakeEvent struct {
	runID, kind string
	payload     any
}

type fakeTx struct {
	l        *fakeLedger
	runID    string
	events   []fakeEvent
	runs     []ledger.RunRow
	tasks    []ledger.TaskRow
	attempts []ledger.AttemptRow
	checks   []ledger.CheckRow
	commands []ledger.CommandRow
	messages []ledger.MessageRow
	prereqs  []ledger.PrerequisiteRow
	trees    []ledger.TreeRow
	// settles are the outcomes of commands taken before, applied at commit.
	settles []ledger.CommandRow
	// continued is the run this transaction's run continues, marked at commit.
	continued string
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{runs: map[string]ledger.RunRow{}, tasks: map[string]ledger.TaskRow{}, attempts: map[string]ledger.AttemptRow{}, commands: map[string]ledger.CommandRow{}, messages: map[string]ledger.MessageRow{}, trees: map[string]ledger.TreeRow{}}
}

func (l *fakeLedger) Load(ctx context.Context, runID string) (Loaded, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failLoad != "" && l.failLoad == runID {
		l.failLoad = ""
		return Loaded{}, false, errors.New("ledger: disk I/O error (injected)")
	}
	row, ok := l.runs[runID]
	if !ok {
		return Loaded{}, false, nil
	}
	out := Loaded{Run: row}
	for _, t := range l.tasks {
		if t.RunID == runID {
			out.Tasks = append(out.Tasks, t)
		}
	}
	for _, a := range l.attempts {
		if a.RunID == runID {
			out.Attempts = append(out.Attempts, a)
		}
	}
	for _, m := range l.messages {
		if m.RunID == runID {
			out.Messages = append(out.Messages, m)
		}
	}
	slices.SortFunc(out.Messages, func(a, b ledger.MessageRow) int { return int(a.Seq - b.Seq) })
	// The prerequisites' rows as the ledger keeps them: each (id, k) as
	// last put, without its log.
	at := map[string]int{}
	for _, p := range l.prerequisites {
		if p.RunID != runID {
			continue
		}
		p.Log = nil
		key := fmt.Sprintf("%s/%d", p.ID, p.K)
		if i, ok := at[key]; ok {
			out.Prerequisites[i] = p
			continue
		}
		at[key] = len(out.Prerequisites)
		out.Prerequisites = append(out.Prerequisites, p)
	}
	return out, true, nil
}

func (tx *fakeTx) PutMessage(r ledger.MessageRow) error {
	if len(r.Text) > ledger.MaxMessageText {
		return errors.New("ledger: the message is too long")
	}
	tx.messages = append(tx.messages, r)
	return nil
}

func (l *fakeLedger) Receipt(ctx context.Context, runID, actor, id string) (ledger.CommandRow, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.commands[runID+"/"+actor+"/"+id]
	return row, ok, nil
}

// Command and Settle act on the transaction's copy of the commands,
// applied with its rows when it commits.
func (tx *fakeTx) Command(actor, id, intent, fingerprint string) (ledger.CommandRow, bool, error) {
	tx.l.mu.Lock()
	prior, ok := tx.l.commands[tx.runID+"/"+actor+"/"+id]
	n := 0
	for k := range tx.l.commands {
		if strings.HasPrefix(k, tx.runID+"/") {
			n++
		}
	}
	cap := tx.l.commandCap
	tx.l.mu.Unlock()
	if ok {
		if prior.Intent != intent || prior.Fingerprint != fingerprint {
			return ledger.CommandRow{}, false, ledger.ErrCommandConflict
		}
		return prior, true, nil
	}
	if cap > 0 && n >= cap {
		return ledger.CommandRow{}, false, ledger.ErrTooManyCommands
	}
	tx.commands = append(tx.commands, ledger.CommandRow{RunID: tx.runID, Actor: actor, ID: id, Intent: intent, Fingerprint: fingerprint, Status: ledger.CommandAccepted, CreatedAt: time.Now().UTC()})
	return ledger.CommandRow{}, false, nil
}

func (tx *fakeTx) Continued(from string) error {
	tx.l.mu.Lock()
	row, ok := tx.l.runs[from]
	tx.l.mu.Unlock()
	if !ok || row.ContinuedBy != "" {
		return fmt.Errorf("%w: %s", ledger.ErrContinued, from)
	}
	tx.continued = from
	return nil
}

func (tx *fakeTx) Settle(actor, id, status string, receipt any) error {
	b, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	for i := range tx.commands {
		if tx.commands[i].Actor == actor && tx.commands[i].ID == id {
			tx.commands[i].Status, tx.commands[i].Receipt = status, string(b)
			return nil
		}
	}
	// Taken in an earlier transaction (act's first): settled at commit.
	tx.l.mu.Lock()
	_, ok := tx.l.commands[tx.runID+"/"+actor+"/"+id]
	tx.l.mu.Unlock()
	if !ok {
		return fmt.Errorf("fake ledger: no command %s of %s to settle", id, actor)
	}
	tx.settles = append(tx.settles, ledger.CommandRow{RunID: tx.runID, Actor: actor, ID: id, Status: status, Receipt: string(b)})
	return nil
}

func (l *fakeLedger) Available() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unavailable
}

func (l *fakeLedger) Transition(ctx context.Context, runID string, fn func(LedgerTx) error) (int64, error) {
	tx := &fakeTx{l: l, runID: runID}
	if err := fn(tx); err != nil {
		return 0, err
	}
	l.mu.Lock()
	blockOn, blocked, release := l.blockOn, l.blocked, l.release
	l.mu.Unlock()
	if blockOn != "" && slices.ContainsFunc(tx.events, func(ev fakeEvent) bool { return matches(blockOn, ev) }) {
		select {
		case blocked <- struct{}{}:
		default:
		}
		<-release
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range tx.events {
		if l.failOn != "" && matches(l.failOn, ev) {
			if l.failOnce {
				l.failOn, l.failOnce = "", false
			}
			return 0, errors.New("ledger: disk full (injected)")
		}
	}
	// A transaction whose context ended meanwhile is refused with that
	// error, as database/sql refuses a commit on a context that is done.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("ledger: %w", err)
	}
	l.rev++
	for _, r := range tx.runs {
		l.runs[r.ID] = r
	}
	for _, t := range tx.tasks {
		l.tasks[t.RunID+"/"+t.ID] = t
	}
	for _, a := range tx.attempts {
		l.attempts[fmt.Sprintf("%s/%s/%d", a.RunID, a.TaskID, a.N)] = a
	}
	l.checks = append(l.checks, tx.checks...)
	l.prerequisites = append(l.prerequisites, tx.prereqs...)
	for _, tr := range tx.trees {
		l.trees[fmt.Sprintf("%s/%s/%d/%s", tr.RunID, tr.TaskID, tr.N, tr.Kind)] = tr
	}
	for _, c := range tx.commands {
		l.commands[c.RunID+"/"+c.Actor+"/"+c.ID] = c
	}
	for _, st := range tx.settles {
		key := st.RunID + "/" + st.Actor + "/" + st.ID
		row := l.commands[key]
		row.Status, row.Receipt = st.Status, st.Receipt
		l.commands[key] = row
	}
	if tx.continued != "" {
		row := l.runs[tx.continued]
		row.ContinuedBy = runID
		l.runs[tx.continued] = row
	}
	for _, m := range tx.messages {
		l.messages[fmt.Sprintf("%s/%d", m.RunID, m.Seq)] = m
	}
	l.events = append(l.events, tx.events...)
	return l.rev, nil
}

func (tx *fakeTx) Event(kind string, payload any) error {
	tx.events = append(tx.events, fakeEvent{tx.runID, kind, payload})
	return nil
}
func (tx *fakeTx) PutRun(r ledger.RunRow) error { tx.runs = append(tx.runs, r); return nil }

// CreateRun refuses an id the fake ledger has, as the ledger's insert does.
func (tx *fakeTx) CreateRun(r ledger.RunRow) error {
	tx.l.mu.Lock()
	_, ok := tx.l.runs[r.ID]
	tx.l.mu.Unlock()
	if ok {
		return fmt.Errorf("%w: %s", ledger.ErrRunExists, r.ID)
	}
	tx.runs = append(tx.runs, r)
	return nil
}
func (tx *fakeTx) PutTask(r ledger.TaskRow) error { tx.tasks = append(tx.tasks, r); return nil }
func (tx *fakeTx) PutAttempt(r ledger.AttemptRow) error {
	tx.attempts = append(tx.attempts, r)
	return nil
}
func (tx *fakeTx) PutCheck(r ledger.CheckRow) error { tx.checks = append(tx.checks, r); return nil }
func (tx *fakeTx) PutPrerequisite(r ledger.PrerequisiteRow) error {
	tx.prereqs = append(tx.prereqs, r)
	return nil
}
func (tx *fakeTx) PutTree(r ledger.TreeRow) error { tx.trees = append(tx.trees, r); return nil }

// kinds is the kinds of the run's events, in order.
func (l *fakeLedger) kinds(runID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ev := range l.events {
		if ev.runID == runID {
			out = append(out, ev.kind)
		}
	}
	return out
}

// count is how many events of the kind the run has.
func (l *fakeLedger) count(runID, kind string) int {
	n := 0
	for _, k := range l.kinds(runID) {
		if k == kind {
			n++
		}
	}
	return n
}

// attemptRow is the attempt's row as the fake ledger last committed it.
func (l *fakeLedger) attemptRow(t *testing.T, runID, taskID string, n int) ledger.AttemptRow {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.attempts[fmt.Sprintf("%s/%s/%d", runID, taskID, n)]
	if !ok {
		t.Fatalf("no row for %s/%s/%d", runID, taskID, n)
	}
	return row
}

// managedEngine is an engine with a fake ledger over a fake launcher
// whose sessions print a prompt once, so they are ready for their line.
func managedEngine(t *testing.T) (*Engine, *fakeLauncher, *fakeLedger) {
	t.Helper()
	e, fl := readyEngine(t)
	l := newFakeLedger()
	e.UseLedger(l)
	return e, fl, l
}

// repoCrew is a managed crew in the repository dir: one member per
// distinct task member, every task given.
func repoCrew(dir string, tasks ...Task) Crew {
	var members []Member
	for _, t := range tasks {
		if !slices.ContainsFunc(members, func(m Member) bool { return m.Name == t.Member }) {
			members = append(members, taskMember(t.Member))
		}
	}
	c := testCrew(members...)
	c.Cwd, c.Isolation, c.Tasks = dir, IsolationWorktree, tasks
	return c
}

func work(id, member string, needs ...string) Task {
	return Task{ID: id, Member: member, Kind: TaskWork, Instructions: "Do " + id + ".", Needs: needs}
}

func review(id, member, of string) Task {
	return Task{ID: id, Member: member, Kind: TaskReview, Instructions: "Review " + of + ".", Needs: []string{of}}
}

// launchManaged launches c and returns its run.
func launchManaged(t *testing.T, e *Engine, c Crew) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run, release, err := e.LaunchNamed(ctx, c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	return *run
}

// taskOf is the task as the run reports it.
func taskOf(t *testing.T, e *Engine, runID, taskID string) TaskState {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s is gone", runID)
	}
	for _, ts := range r.Tasks {
		if ts.ID == taskID {
			return ts
		}
	}
	t.Fatalf("no task %s in %+v", taskID, r.Tasks)
	return TaskState{}
}

// waitTask waits for the task to be in the state, with the attempt count
// when n is not 0.
func waitTask(t *testing.T, e *Engine, runID, taskID, state string, n int) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, taskID, fmt.Sprintf("%s (attempt %d)", state, n), func(ts TaskState) bool {
		return ts.State == state && (n == 0 || ts.Attempts == n)
	})
}

// waitAttempt waits for the task's latest attempt to be in the state and
// returns the task.
func waitAttempt(t *testing.T, e *Engine, runID, taskID, state string) TaskState {
	t.Helper()
	return waitTaskWhere(t, e, runID, taskID, "attempt "+state, func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.State == state
	})
}

// waitTaskWhere waits up to 10 s for the task to satisfy ok, and fails
// with the task, its attempt and the run's log otherwise.
func waitTaskWhere(t *testing.T, e *Engine, runID, taskID, what string, ok func(TaskState) bool) TaskState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ts := taskOf(t, e, runID, taskID)
		if ok(ts) {
			return ts
		}
		if time.Now().After(deadline) {
			r, _ := e.Get(runID)
			var log []string
			for _, en := range r.Log {
				log = append(log, en.Message)
			}
			t.Fatalf("task %s is not %s after 10 s: %+v\nattempt: %+v\nlog: %s", taskID, what, ts, ts.Attempt, strings.Join(log, " | "))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// attemptSession is the task's latest attempt's session and process.
func (f *fakeLauncher) attemptSession(t *testing.T, ts TaskState) (*session.Local, *sessiontest.FakeProc) {
	t.Helper()
	if ts.Attempt == nil || ts.Attempt.SessionID == "" {
		t.Fatalf("task %s has no session: %+v", ts.ID, ts.Attempt)
	}
	name := fmt.Sprintf("%s · %s #%d", ts.Member, ts.ID, ts.Attempt.N)
	f.mu.Lock()
	defer f.mu.Unlock()
	l, p := f.byID[ts.Attempt.SessionID], f.procs[name]
	if l == nil || p == nil {
		t.Fatalf("no session %s / process %q", ts.Attempt.SessionID, name)
	}
	return l, p
}

func submit(t *testing.T, e *Engine, sessionID string, res Result) TaskState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := e.SubmitResult(ctx, sessionID, res)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAManagedRunDispatchesTasksAsTheirNeedsAreMet(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	c := repoCrew(dir, work("build", "lead"), work("test", "lead", "build"), review("review", "critic", "build"))
	run := launchManaged(t, e, c)
	if run.Managed == nil || run.State != RunRunning || run.Outcome != "" {
		t.Fatalf("at launch: %+v", run)
	}
	base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))

	// Only build is ready: its attempt has a worktree at the base, on its
	// branch, with its task files, and its session was typed the task line.
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if build.State != ledger.TaskRunning || build.Attempts != 1 || build.Attempt.N != 1 || build.Attempt.Start != base {
		t.Fatalf("build: %+v %+v", build, build.Attempt)
	}
	wantTree := filepath.Join(dir, ".conductor", "worktrees", run.ID, "build.1")
	if build.Attempt.Worktree != wantTree || build.Attempt.Branch != "crew/"+run.ID+"/build.1" {
		t.Fatalf("build's worktree: %+v", build.Attempt)
	}
	if head := strings.TrimSpace(runGit(t, wantTree, "rev-parse", "HEAD")); head != base {
		t.Fatalf("the worktree is at %s, not the base %s", head, base)
	}
	md, err := os.ReadFile(filepath.Join(wantTree, ".conductor", "task.md"))
	if err != nil || !strings.Contains(string(md), "Do build.") || !strings.Contains(string(md), "crew/"+run.ID+"/build.1") {
		t.Fatalf("task.md: %v %q", err, md)
	}
	if _, err := os.Stat(filepath.Join(wantTree, ".conductor", "task.json")); err != nil {
		t.Fatal(err)
	}
	if ignore, _ := os.ReadFile(filepath.Join(wantTree, ".conductor", ".gitignore")); string(ignore) != "*\n" {
		t.Fatalf(".gitignore: %q", ignore)
	}
	for _, id := range []string{"test", "review"} {
		if ts := taskOf(t, e, run.ID, id); ts.State != ledger.TaskBlocked || !strings.Contains(ts.Reason, "needs build") || ts.Attempts != 0 {
			t.Fatalf("%s before build's result: %+v", id, ts)
		}
	}
	_, p := fl.attemptSession(t, build)
	if got := waitTyped(t, p, 10*time.Second); got != taskLine(c.Tasks[0], 1)+"\r" {
		t.Fatalf("typed %q", got)
	}
	calls := fl.launched()
	if len(calls) != 1 {
		t.Fatalf("launched %v", calls)
	}
	fl.mu.Lock()
	call := fl.calls[0]
	fl.mu.Unlock()
	if call.ref.Task != "build" || call.ref.Attempt != 1 || call.cwd != wantTree || call.env["GOAL"] != c.Goal {
		t.Fatalf("the launch: %+v", call)
	}
	if m := memberState(t, e, run.ID, "lead"); m.Task != "build" || m.Attempt != 1 || m.Worktree != wantTree {
		t.Fatalf("lead: %+v", m)
	}

	// The member's result: a commit, submitted. With no checks the task
	// succeeds at once; test (lead again) and review start at its commit.
	writeFile(t, filepath.Join(wantTree, "built.txt"), "yes\n")
	commitAll(t, wantTree, "build it")
	result := strings.TrimSpace(runGit(t, wantTree, "rev-parse", "HEAD"))
	st := submit(t, e, build.Attempt.SessionID, Result{Summary: "built it"})
	if st.State != ledger.TaskVerifying && st.State != ledger.TaskSucceeded {
		t.Fatalf("after submit: %+v", st)
	}
	done := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if done.Result == nil || done.Result.Commit != result || done.Result.Summary != "built it" || done.Attempt.State != ledger.AttemptSucceeded {
		t.Fatalf("build's result: %+v %+v", done.Result, done.Attempt)
	}
	test := waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	for _, ts := range []TaskState{test, rev} {
		if ts.Attempt.Start != result {
			t.Fatalf("%s starts at %s, not build's result %s", ts.ID, ts.Attempt.Start, result)
		}
		if head := strings.TrimSpace(runGit(t, ts.Attempt.Worktree, "rev-parse", "HEAD")); head != result {
			t.Fatalf("%s's worktree is at %s", ts.ID, head)
		}
		if _, err := os.Stat(filepath.Join(ts.Attempt.Worktree, "built.txt")); err != nil {
			t.Fatalf("%s does not see build's tree: %v", ts.ID, err)
		}
	}
	md, _ = os.ReadFile(filepath.Join(rev.Attempt.Worktree, ".conductor", "task.md"))
	if !strings.Contains(string(md), result) || !strings.Contains(string(md), "built it") || !strings.Contains(string(md), "Do not commit") {
		t.Fatalf("the review's task.md: %q", md)
	}
	// Lead's build session was let go for its test session: one live
	// session per member.
	if n := len(fl.launched()); n != 3 {
		t.Fatalf("launched %d", n)
	}
	if !slices.Contains(l.kinds(run.ID), "session.released") {
		t.Fatalf("events: %v", l.kinds(run.ID))
	}

	// The review's verdict and test's commit end the run: every task
	// terminal, the outcome succeeded, the state finished while sessions
	// still run.
	submit(t, e, rev.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "fine"})
	if got := waitTask(t, e, run.ID, "review", ledger.TaskSucceeded, 1); got.Result.Verdict != VerdictApproved {
		t.Fatalf("review: %+v", got.Result)
	}
	writeFile(t, filepath.Join(test.Attempt.Worktree, "tested.txt"), "yes\n")
	commitAll(t, test.Attempt.Worktree, "test it")
	submit(t, e, test.Attempt.SessionID, Result{Summary: "tested"})
	waitTask(t, e, run.ID, "test", ledger.TaskSucceeded, 1)
	waitTrue(t, "the run finished", func() bool {
		r, _ := e.Get(run.ID)
		return r.State == RunFinished && r.Outcome == ledger.OutcomeSucceeded
	})
	kinds := l.kinds(run.ID)
	for _, k := range []string{"run.created", "attempt.reserved", "attempt.workspace", "attempt.launched", "attempt.prompted", "result.submitted", "task.succeeded", "run.finished"} {
		if !slices.Contains(kinds, k) {
			t.Fatalf("no %s event: %v", k, kinds)
		}
	}
	if slices.Index(kinds, "attempt.reserved") > slices.Index(kinds, "attempt.launched") {
		t.Fatalf("the launch was not recorded after the reservation: %v", kinds)
	}
	if r, _ := e.Get(run.ID); r.Managed.Revision == 0 {
		t.Fatalf("no revision: %+v", r.Managed)
	}
	stopRun(t, e, run.ID)
}

func TestSubmitResultRefusesWhatIsNotAResult(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	c := repoCrew(dir, work("build", "lead"), work("test", "lead", "build"), review("review", "critic", "build"))
	run := launchManaged(t, e, c)
	ctx := context.Background()
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	id, tree := build.Attempt.SessionID, build.Attempt.Worktree

	if _, err := e.SubmitResult(ctx, "nope", Result{}); !errors.Is(err, ErrNotAnAttempt) {
		t.Fatalf("an unknown session: %v", err)
	}
	if _, err := e.SubmitResult(ctx, id, Result{Verdict: VerdictApproved}); !errors.Is(err, ErrBadResult) {
		t.Fatalf("a verdict from a work task: %v", err)
	}
	if _, err := e.SubmitResult(ctx, id, Result{Summary: strings.Repeat("x", maxSummary+1)}); !errors.Is(err, ErrBadResult) {
		t.Fatalf("a long summary: %v", err)
	}
	// Uncommitted work is refused with its paths; the task files are not
	// work.
	writeFile(t, filepath.Join(tree, "draft.txt"), "wip\n")
	var dirty *DirtyWorktreeError
	if _, err := e.SubmitResult(ctx, id, Result{}); !errors.As(err, &dirty) || !slices.Equal(dirty.Paths, []string{"draft.txt"}) {
		t.Fatalf("a dirty worktree: %v", err)
	}
	commitAll(t, tree, "draft")
	head := strings.TrimSpace(runGit(t, tree, "rev-parse", "HEAD"))
	if _, err := e.SubmitResult(ctx, id, Result{Commit: build.Attempt.Start}); !errors.Is(err, ErrCommitMissing) {
		t.Fatalf("a commit that is not HEAD: %v", err)
	}
	// A result tree with .conductor/ in it, forced past the ignore.
	runGit(t, tree, "add", "-f", ".conductor/task.md")
	runGit(t, tree, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "oops")
	if _, err := e.SubmitResult(ctx, id, Result{}); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("a reserved path: %v", err)
	}
	runGit(t, tree, "reset", "-q", "--hard", head)
	if ts := taskOf(t, e, run.ID, "build"); ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("a refusal moved the attempt: %+v", ts.Attempt)
	}
	submit(t, e, id, Result{Summary: "done", Commit: head})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	// Again: the attempt is closed.
	if _, err := e.SubmitResult(ctx, id, Result{Summary: "again"}); !errors.Is(err, ErrStaleAttempt) {
		t.Fatalf("a second result: %v", err)
	}

	// A dependent's result must descend from its input; a review's tree
	// must be the candidate's.
	test := waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	runGit(t, test.Attempt.Worktree, "reset", "-q", "--hard", build.Attempt.Start)
	writeFile(t, filepath.Join(test.Attempt.Worktree, "elsewhere.txt"), "x\n")
	commitAll(t, test.Attempt.Worktree, "from the base")
	if _, err := e.SubmitResult(ctx, test.Attempt.SessionID, Result{}); !errors.Is(err, ErrNotFromInput) {
		t.Fatalf("a result off the input: %v", err)
	}
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	writeFile(t, filepath.Join(rev.Attempt.Worktree, "note.txt"), "x\n")
	commitAll(t, rev.Attempt.Worktree, "a review that commits")
	if _, err := e.SubmitResult(ctx, rev.Attempt.SessionID, Result{Verdict: VerdictApproved}); !errors.Is(err, ErrReviewChanged) {
		t.Fatalf("a review that moved: %v", err)
	}
	if _, err := e.SubmitResult(ctx, rev.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges}); !errors.Is(err, ErrBadResult) {
		t.Fatalf("needs_changes with no finding: %v", err)
	}
	stopRun(t, e, run.ID)
}

func TestChecksDecideAWorkTask(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "has-it", Argv: []string{"sh", "-c", "test -f built.txt"}}, {ID: "pure", Argv: []string{"sh", "-c", "echo changed >> README"}}}
	run := launchManaged(t, e, repoCrew(dir, build))

	// Attempt 1 commits the wrong file: the first check fails, the attempt
	// fails at it, and attempt 2 starts from that commit with the failure
	// in its task file.
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a1.Attempt.Worktree, "other.txt"), "x\n")
	commitAll(t, a1.Attempt.Worktree, "wrong file")
	first := strings.TrimSpace(runGit(t, a1.Attempt.Worktree, "rev-parse", "HEAD"))
	submit(t, e, a1.Attempt.SessionID, Result{Summary: "tried"})
	a2 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a2.Attempts != 2 || a2.Attempt.N != 2 || a2.Attempt.Start != first {
		t.Fatalf("attempt 2: %+v %+v", a2, a2.Attempt)
	}
	if !strings.Contains(a2.Reason, "") {
		t.Fatal()
	}
	md, _ := os.ReadFile(filepath.Join(a2.Attempt.Worktree, ".conductor", "task.md"))
	if !strings.Contains(string(md), "Attempt 1 failed at check has-it") || !strings.Contains(string(md), "exited 1") {
		t.Fatalf("task.md after a failed check: %q", md)
	}
	if _, err := os.Stat(a1.Attempt.Worktree + ".check"); err != nil {
		t.Fatalf("no detached checkout for the check: %v", err)
	}
	if n := l.count(run.ID, "check.finished"); n != 1 {
		t.Fatalf("check.finished events: %d", n)
	}
	l.mu.Lock()
	rows := slices.Clone(l.checks)
	l.mu.Unlock()
	fin := rows[len(rows)-1]
	if fin.State != ledger.CheckFinished || fin.Exit != 1 || fin.Subject != first || fin.ID != "has-it" || !fin.Stable || fin.PID == 0 {
		t.Fatalf("the check row: %+v", fin)
	}

	// Attempt 2 has the file: the first check passes, and the second
	// changes a tracked file, which fails it, however it exits.
	writeFile(t, filepath.Join(a2.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a2.Attempt.Worktree, "built")
	submit(t, e, a2.Attempt.SessionID, Result{Summary: "built"})
	a3 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a3.Attempts != 3 {
		t.Fatalf("attempt 3: %+v", a3)
	}
	md, _ = os.ReadFile(filepath.Join(a3.Attempt.Worktree, ".conductor", "task.md"))
	if !strings.Contains(string(md), "Attempt 2 failed at check pure") || !strings.Contains(string(md), "changed tracked files: README") {
		t.Fatalf("task.md after an unstable check: %q", md)
	}
	// Attempt 3 is the last: a failure ends the task and the run.
	writeFile(t, filepath.Join(a3.Attempt.Worktree, "more.txt"), "x\n")
	commitAll(t, a3.Attempt.Worktree, "more")
	submit(t, e, a3.Attempt.SessionID, Result{Summary: "more"})
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, 3)
	if !strings.Contains(failed.Reason, "attempt 3 failed at check pure") {
		t.Fatalf("the failed task: %+v", failed)
	}
	waitTrue(t, "the run failed", func() bool {
		r, _ := e.Get(run.ID)
		return r.State == RunFinished && r.Outcome == ledger.OutcomeFailed
	})
	// A retry grants one more attempt.
	if err := e.RetryTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	a4 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a4.Attempts != 4 {
		t.Fatalf("after the retry: %+v", a4)
	}
	if r, _ := e.Get(run.ID); r.State != RunRunning || r.Outcome != "" {
		t.Fatalf("the run after the retry: %s %q", r.State, r.Outcome)
	}
	stopRun(t, e, run.ID)
}

func TestAPassingCheckRunsInADetachedCheckoutOfTheResult(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "where", Argv: []string{"sh", "-c", "test -f built.txt && git rev-parse HEAD > /dev/null && echo out > untracked.txt"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a1.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a1.Attempt.Worktree, "built")
	submit(t, e, a1.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	// The untracked output stayed in the checkout, not the worktree, and
	// is listed with the check.
	if _, err := os.Stat(filepath.Join(a1.Attempt.Worktree+".check", "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a1.Attempt.Worktree, "untracked.txt")); err == nil {
		t.Fatal("the check wrote in the attempt's worktree")
	}
	l.mu.Lock()
	var fin fakeEvent
	for _, ev := range l.events {
		if ev.kind == "check.finished" {
			fin = ev
		}
	}
	l.mu.Unlock()
	payload, _ := fin.payload.(map[string]any)
	if untracked, _ := payload["untracked"].([]string); !slices.Equal(untracked, []string{"untracked.txt"}) || payload["stable"] != true {
		t.Fatalf("check.finished: %+v", payload)
	}
	stopRun(t, e, run.ID)
}

func TestDoneAndHandoffFinishNothingInAManagedRun(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("review", "critic", "build")))
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, build)
	waitTyped(t, p, 10*time.Second)
	done := func() {
		e.OnActivity(build.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityAttention, Message: "done", At: time.Now().UTC()}, session.AttentionDone)
	}
	done()
	// One reminder, typed; a second done gets none.
	if got := waitTyped(t, p, 10*time.Second); !strings.Contains(got, "does not finish task") {
		t.Fatalf("the reminder: %q", got)
	}
	done()
	if got, ok := readSubmission(p, 500*time.Millisecond); ok {
		t.Fatalf("a second reminder: %q", got)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskRunning || ts.Attempt.State != ledger.AttemptRunning {
		t.Fatalf("done moved the task: %+v", ts)
	}
	if ts := taskOf(t, e, run.ID, "review"); ts.State != ledger.TaskBlocked || ts.Attempts != 0 {
		t.Fatalf("done started the review: %+v", ts)
	}
	// A handoff is noted, never typed, and starts no one.
	e.OnActivity(build.Attempt.SessionID, session.ActivityEntry{Type: session.ActivityHandoff, To: "critic", Message: "look at this", At: time.Now().UTC()}, session.AttentionDone)
	waitLogged(t, e, run.ID, "sent as a message", 1, 5*time.Second)
	if n := len(fl.launched()); n != 1 {
		t.Fatalf("launched %d", n)
	}
	if logCount(t, e, run.ID, "is done: starting") != 0 {
		t.Fatal("a done started someone")
	}
	stopRun(t, e, run.ID)
}

func TestASessionEndingBeforeItsResultFailsTheAttempt(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	for n := 1; n <= maxAttemptsPerTask; n++ {
		ts := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
		if ts.Attempt.N != n {
			t.Fatalf("attempt %d: %+v", n, ts.Attempt)
		}
		_, p := fl.attemptSession(t, ts)
		waitTyped(t, p, 10*time.Second)
		p.End(0)
		if n < maxAttemptsPerTask {
			// The next attempt follows at once; the failure is in the log.
			waitTaskWhere(t, e, run.ID, "build", "at its next attempt", func(ts TaskState) bool { return ts.Attempts == n+1 })
			waitLogged(t, e, run.ID, fmt.Sprintf("attempt %d failed at running: the session ended before a result was submitted", n), 1, 5*time.Second)
		}
	}
	failed := waitTask(t, e, run.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	if !strings.Contains(failed.Reason, "the session ended before a result was submitted") || failed.Attempt.Stage != ledger.AttemptRunning {
		t.Fatalf("the failed task: %+v %+v", failed, failed.Attempt)
	}
	if m := memberState(t, e, run.ID, "lead"); m.Status != MemberEnded {
		t.Fatalf("lead: %+v", m)
	}
	waitTrue(t, "the run failed", func() bool {
		r, _ := e.Get(run.ID)
		return r.State == RunFinished && r.Outcome == ledger.OutcomeFailed
	})
	stopRun(t, e, run.ID)
}

func TestStopEndsTheAttemptsAndTheirChecks(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "slow", Argv: []string{"sh", "-c", "sleep 30"}, TimeoutSeconds: 60}}
	run := launchManaged(t, e, repoCrew(dir, build, work("docs", "writer")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	docs := waitAttempt(t, e, run.ID, "docs", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTrue(t, "the check started", func() bool { return l.count(run.ID, "check.running") == 1 })
	began := time.Now()
	stopRun(t, e, run.ID)
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("the stop waited %v for a 30 s check", took)
	}
	r, _ := e.Get(run.ID)
	if r.State != RunStopped {
		t.Fatalf("after the stop: %s", r.State)
	}
	for _, ts := range r.Tasks {
		if ts.Attempt == nil || ts.Attempt.State != ledger.AttemptStopped || ts.State != ledger.TaskReady {
			t.Fatalf("%s after the stop: %+v %+v", ts.ID, ts, ts.Attempt)
		}
	}
	if l.count(run.ID, "check.finished") != 1 || !slices.Contains(l.kinds(run.ID), "run.stopped") {
		t.Fatalf("events: %v", l.kinds(run.ID))
	}
	_, p := fl.attemptSession(t, docs)
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the docs session was not stopped")
	}
}

func TestPauseCancelAndRetry(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build"), work("docs", "writer", "build")))
	ctx := context.Background()
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	// Paused: build's result makes test and docs ready, and none starts.
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(build.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, build.Attempt.Worktree, "built")
	submit(t, e, build.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	time.Sleep(300 * time.Millisecond)
	r, _ := e.Get(run.ID)
	if !r.Paused || taskOf(t, e, run.ID, "test").State != ledger.TaskReady || taskOf(t, e, run.ID, "docs").Attempts != 0 {
		t.Fatalf("paused: %+v", r.Tasks)
	}
	// Cancelled while ready: docs is cancelled, nothing started.
	if err := e.CancelTask(ctx, run.ID, "docs"); err != nil {
		t.Fatal(err)
	}
	if ts := taskOf(t, e, run.ID, "docs"); ts.State != ledger.TaskCancelled || ts.Attempts != 0 {
		t.Fatalf("docs: %+v", ts)
	}
	if err := e.CancelTask(ctx, run.ID, "docs"); !errors.Is(err, ErrTaskState) {
		t.Fatalf("cancelling again: %v", err)
	}
	if err := e.CancelTask(ctx, run.ID, "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("an unknown task: %v", err)
	}
	// Unpaused: test starts; cancelled while running, its session is
	// stopped and the attempt cancelled, and the run's outcome is
	// cancelled once every task is terminal.
	if err := e.PauseRun(ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	test := waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, test)
	if err := e.CancelTask(ctx, run.ID, "test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("test's session was not stopped")
	}
	if ts := taskOf(t, e, run.ID, "test"); ts.State != ledger.TaskCancelled || ts.Attempt.State != ledger.AttemptCancelled {
		t.Fatalf("test: %+v %+v", ts, ts.Attempt)
	}
	waitTrue(t, "the run's outcome", func() bool {
		r, _ := e.Get(run.ID)
		return r.State == RunFinished && r.Outcome == ledger.OutcomeCancelled
	})
	// Retried: test is ready again and starts, from build's result.
	if err := e.RetryTask(ctx, run.ID, "test"); err != nil {
		t.Fatal(err)
	}
	again := waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	if again.Attempts != 2 || again.Attempt.Start != taskOf(t, e, run.ID, "build").Result.Commit {
		t.Fatalf("test retried: %+v %+v", again, again.Attempt)
	}
	if err := e.RetryTask(ctx, run.ID, "test"); !errors.Is(err, ErrTaskState) {
		t.Fatalf("retrying a running task: %v", err)
	}
	for _, k := range []string{"run.paused", "task.cancelled", "task.retried"} {
		if !slices.Contains(l.kinds(run.ID), k) {
			t.Fatalf("no %s: %v", k, l.kinds(run.ID))
		}
	}
	stopRun(t, e, run.ID)
}

func TestNoPlaceAmongTheSessionsSpendsNoAttempt(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	fl.mu.Lock()
	fl.reserveFails = session.ErrTooManySessions
	fl.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitLogged(t, e, run.ID, "no place for attempt 1 yet", 1, 5*time.Second)
	ts := taskOf(t, e, run.ID, "build")
	if ts.State != ledger.TaskReady || ts.Attempts != 0 || !strings.Contains(ts.Reason, "waiting for a place") {
		t.Fatalf("build: %+v", ts)
	}
	if l.count(run.ID, "attempt.reserved") != 0 || len(fl.launched()) != 0 {
		t.Fatalf("something was spent: %v %v", l.kinds(run.ID), fl.launched())
	}
	if _, err := os.Stat(filepath.Join(dir, ".conductor", "worktrees", run.ID)); err == nil {
		t.Fatal("a worktree was made")
	}
	// A place later: the next pump dispatches it.
	fl.mu.Lock()
	fl.reserveFails = nil
	fl.mu.Unlock()
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func TestALedgerThatCannotRecordAnAttemptPausesDispatch(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	l.failOn = "attempt.reserved"
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitLogged(t, e, run.ID, "scheduling paused", 1, 5*time.Second)
	r, _ := e.Get(run.ID)
	ts := taskOf(t, e, run.ID, "build")
	if !r.Paused || ts.State != ledger.TaskReady || ts.Attempts != 0 || !strings.Contains(ts.Reason, "the ledger could not record") {
		t.Fatalf("paused=%v build: %+v", r.Paused, ts)
	}
	if len(fl.launched()) != 0 {
		t.Fatal("a session was launched")
	}
	stopRun(t, e, run.ID)
}

func TestAManagedLaunchNeedsALedger(t *testing.T) {
	dir := newRepo(t)
	e, _ := newEngine(t)
	ctx := context.Background()
	if _, _, err := e.LaunchNamed(ctx, repoCrew(dir, work("build", "lead")), ""); !errors.Is(err, ErrNoLedger) {
		t.Fatalf("without a ledger: %v", err)
	}
	if runs := e.List(); len(runs) != 0 {
		t.Fatalf("a run was kept: %+v", runs)
	}
	// One not available (read-only while another server holds the owner
	// lock) refuses with why.
	l := newFakeLedger()
	l.unavailable = errors.New("waiting for the owner lock: another server holds it")
	e.UseLedger(l)
	_, _, err := e.LaunchNamed(ctx, repoCrew(dir, work("build", "lead")), "")
	if !errors.Is(err, ErrNoLedger) || !strings.Contains(err.Error(), "another server holds") {
		t.Fatalf("with a ledger not available: %v", err)
	}
	l.mu.Lock()
	l.unavailable = nil
	l.mu.Unlock()
	run, release, err := e.LaunchNamed(ctx, repoCrew(dir, work("build", "lead")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	stopRun(t, e, run.ID)
}

func TestRunStateOfAManagedRunFollowsItsOutcome(t *testing.T) {
	members := []MemberState{{Name: "lead", Status: MemberRunning}, {Name: "critic", Status: MemberRunning, NeedsInput: true}}
	if state, _ := runState(Run{Members: members, Managed: &Managed{Revision: 3}, Outcome: ledger.OutcomeSucceeded}); state != RunFinished {
		t.Fatalf("finished with sessions up: %s", state)
	}
	if state, needs := runState(Run{Members: members, Managed: &Managed{Revision: 3}}); state != RunNeedsInput || needs != 1 {
		t.Fatalf("still going: %s %d", state, needs)
	}
	ended := []MemberState{{Name: "lead", Status: MemberEnded}}
	if state, _ := runState(Run{Members: ended, Managed: &Managed{}}); state != RunRunning {
		t.Fatalf("a managed run whose sessions ended is not finished by that: %s", state)
	}
	if state, _ := runState(Run{Members: ended}); state != RunFinished {
		t.Fatalf("a legacy run whose members ended: %s", state)
	}
}

func TestAChecksChangesAreSeenWhateverItDoesToTheIndexOrHEAD(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	hide := work("hide", "lead")
	hide.Checks = []Check{{ID: "hide", Argv: []string{"sh", "-c", "echo x >> README && git update-index --assume-unchanged README"}}}
	move := work("move", "writer")
	move.Checks = []Check{{ID: "move", Argv: []string{"sh", "-c", "git reset -q --hard HEAD~1"}}}
	run := launchManaged(t, e, repoCrew(dir, hide, move))
	for _, id := range []string{"hide", "move"} {
		a := waitAttempt(t, e, run.ID, id, ledger.AttemptRunning)
		writeFile(t, filepath.Join(a.Attempt.Worktree, id+".txt"), "x\n")
		commitAll(t, a.Attempt.Worktree, id)
		submit(t, e, a.Attempt.SessionID, Result{Summary: id})
	}
	// Each check exits 0 and hides what it did: the comparison sees it,
	// and the attempt fails at the check, recorded so.
	waitTaskWhere(t, e, run.ID, "hide", "at its next attempt", func(ts TaskState) bool { return ts.Attempts == 2 })
	if row := l.attemptRow(t, run.ID, "hide", 1); row.State != ledger.AttemptFailed || row.Stage != "check hide" || row.Error != "changed tracked files: README" {
		t.Fatalf("hide's attempt 1: %+v", row)
	}
	waitTaskWhere(t, e, run.ID, "move", "at its next attempt", func(ts TaskState) bool { return ts.Attempts == 2 })
	if row := l.attemptRow(t, run.ID, "move", 1); row.State != ledger.AttemptFailed || row.Stage != "check move" || row.Error != "moved the checkout to another commit" {
		t.Fatalf("move's attempt 1: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestTheSetupRunsInTheChecksCheckoutAndMayChangeNothingTracked(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "dep", Argv: []string{"sh", "-c", "test -f dep.txt"}}}
	c := repoCrew(dir, build)
	c.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "echo dep > dep.txt"}}}
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if _, err := os.Stat(filepath.Join(a.Attempt.Worktree, "dep.txt")); err != nil {
		t.Fatalf("the setup did not run in the worktree: %v", err)
	}
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	// The check finds what the setup made in the checkout, listed as an
	// untracked output. The setup ran once there: one row of phase setup in
	// the checkout, after the worktree's.
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	var inCheckout int
	for _, row := range l.stepRows(run.ID, "build", 1) {
		if row.Phase == ledger.PhaseSetup && row.Dir == a.Attempt.Worktree+".check" {
			inCheckout++
			if row.K != 2 || row.State != ledger.CheckFinished || row.Exit != 0 || row.Error != "" || !row.Stable {
				t.Fatalf("the checkout's setup row: %+v", row)
			}
		}
	}
	if inCheckout != 1 {
		t.Fatalf("setup rows in the checkout: %d\n%s", inCheckout, rowWords(l.stepRows(run.ID, "build", 1)))
	}
	stopRun(t, e, run.ID)

	// A setup that changes tracked content fails the attempt before any
	// check runs.
	dir2 := newRepo(t)
	e2, _, l2 := managedEngine(t)
	c2 := repoCrew(dir2, build)
	c2.Setup = []Check{{ID: "deps", Argv: []string{"sh", "-c", "echo x >> README"}}}
	run2 := launchManaged(t, e2, c2)
	a2 := waitAttempt(t, e2, run2.ID, "build", ledger.AttemptRunning)
	// The worker's worktree took the change too: commit it as the work.
	commitAll(t, a2.Attempt.Worktree, "the setup's change and the work")
	submit(t, e2, a2.Attempt.SessionID, Result{Summary: "built"})
	// The next attempt starts where attempt 1 did, not at the refused result.
	next := waitTaskWhere(t, e2, run2.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if next.Attempt.Start != a2.Attempt.Start {
		t.Fatalf("attempt 2 starts at %s, not where attempt 1 did (%s)", short(next.Attempt.Start), short(a2.Attempt.Start))
	}
	// The checkout is compared after each step: the change is that step's,
	// in its receipt and its event as in the attempt's words (Codex's
	// review of #422: the receipt said exit 0 and nothing else).
	changed := "setup deps changed tracked files: README"
	if row := l2.attemptRow(t, run2.ID, "build", 1); row.State != ledger.AttemptFailed || row.Stage != "setup deps" || row.Error != changed {
		t.Fatalf("after a setup that changes tracked content: %+v", row)
	}
	var receipt *ledger.CheckRow
	for _, row := range l2.stepRows(run2.ID, "build", 1) {
		if row.Phase == ledger.PhaseSetup && row.Dir == a2.Attempt.Worktree+".check" {
			receipt = &row
		}
	}
	if receipt == nil || receipt.K != 2 || receipt.State != ledger.CheckFinished || receipt.Exit != 0 || receipt.Error != changed || receipt.Stable {
		t.Fatalf("the checkout's setup receipt: %+v\n%s", receipt, rowWords(l2.stepRows(run2.ID, "build", 1)))
	}
	found := false
	for _, p := range l2.payloads(run2.ID, "step.finished") {
		found = found || p["tree"] == ledger.TreeCheckout && p["id"] == "deps" && p["error"] == changed
	}
	if !found {
		t.Fatalf("no step.finished of the checkout's setup says why: %v", l2.payloads(run2.ID, "step.finished"))
	}
	if l2.count(run2.ID, "check.started") != 0 {
		t.Fatal("a check ran after the setup changed the checkout")
	}
	stopRun(t, e2, run2.ID)
}

func TestACancelDuringTheReservationIsAccountedForOnce(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	hold := make(chan struct{})
	fl.mu.Lock()
	fl.reserveHold = hold
	fl.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptReserved)
	if err := e.CancelTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	fl.reserveHold = nil
	fl.mu.Unlock()
	close(hold)
	// The dispatch ends with its reservation refused by the cancelled
	// context: the run's starts drain.
	done := make(chan struct{})
	go func() {
		e.mu.Lock()
		r := e.runs[run.ID]
		e.mu.Unlock()
		r.starts.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the dispatch did not end")
	}
	// The attempt stays cancelled and counted once; nothing is in flight.
	e.mu.Lock()
	r := e.runs[run.ID]
	active, attempts := r.active, len(r.task("build").attempts)
	e.mu.Unlock()
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled || ts.Attempt.State != ledger.AttemptCancelled || attempts != 1 || active != 0 {
		t.Fatalf("after the cancel: %+v %+v active=%d attempts=%d", ts, ts.Attempt, active, attempts)
	}
	if err := e.RetryTask(context.Background(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	again := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if again.Attempts != 2 || again.Attempt.N != 2 {
		t.Fatalf("after the retry: %+v", again)
	}
	stopRun(t, e, run.ID)
}

func TestAPrefixedCrewKeepsItsTaskFilesInItsDirectory(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "web", "app.txt"), "app\n")
	commitAll(t, dir, "web")
	e, fl, _ := managedEngine(t)
	c := repoCrew(dir, work("build", "lead"))
	c.Cwd = filepath.Join(dir, "web")
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	tree := a.Attempt.Worktree
	if _, err := os.Stat(filepath.Join(tree, "web", ".conductor", "task.md")); err != nil {
		t.Fatalf("the task file is not in the crew's directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, ".conductor")); err == nil {
		t.Fatal("a .conductor at the worktree's root")
	}
	_, p := fl.attemptSession(t, a)
	waitTyped(t, p, 10*time.Second)
	// A .conductor committed under the prefix is a reserved path.
	writeFile(t, filepath.Join(tree, "web", ".conductor", "extra.txt"), "x\n")
	runGit(t, tree, "add", "-f", "web/.conductor/extra.txt")
	runGit(t, tree, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "reserved")
	if _, err := e.SubmitResult(context.Background(), a.Attempt.SessionID, Result{}); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("a .conductor under the prefix: %v", err)
	}
	stopRun(t, e, run.ID)

	// A commit that tracks .conductor in the crew's directory cannot take
	// the task files: the attempt fails before the worktree is used.
	writeFile(t, filepath.Join(dir, "web", ".conductor", "x"), "x\n")
	runGit(t, dir, "add", "-f", "web/.conductor/x")
	runGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "tracks .conductor")
	e2, _, _ := managedEngine(t)
	run2 := launchManaged(t, e2, c)
	failed := waitTaskWhere(t, e2, run2.ID, "build", "failed at the worktree", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.State == ledger.AttemptFailed && ts.Attempt.Stage == "worktree"
	})
	if !strings.Contains(failed.Attempt.Error, "tracks .conductor/") {
		t.Fatalf("the attempt: %+v", failed.Attempt)
	}
	stopRun(t, e2, run2.ID)
}

func TestAResultOffTheAttemptsBranchIsRefused(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	runGit(t, a.Attempt.Worktree, "checkout", "-q", "--detach")
	if _, err := e.SubmitResult(context.Background(), a.Attempt.SessionID, Result{}); !errors.Is(err, ErrOffBranch) {
		t.Fatalf("detached: %v", err)
	}
	runGit(t, a.Attempt.Worktree, "checkout", "-q", a.Attempt.Branch)
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
}

func TestAPausedRunStillSettlesAndTheRunRowFollows(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if err := e.PauseRun(context.Background(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	row := l.runs[run.ID]
	l.mu.Unlock()
	if !row.Paused || row.State != ledger.RunRunning {
		t.Fatalf("the run row after the pause: %+v", row)
	}
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitTrue(t, "the paused run settled", func() bool {
		r, _ := e.Get(run.ID)
		return r.State == RunFinished && r.Outcome == ledger.OutcomeSucceeded && r.Paused
	})
	l.mu.Lock()
	row = l.runs[run.ID]
	l.mu.Unlock()
	if row.State != ledger.RunFinished || row.Outcome != ledger.OutcomeSucceeded || !row.Paused {
		t.Fatalf("the run row after the outcome: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestAResultNotRecordedLeavesTheAttemptRunning(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	l.mu.Lock()
	l.failOn = "result.submitted"
	l.mu.Unlock()
	if _, err := e.SubmitResult(context.Background(), a.Attempt.SessionID, Result{Summary: "built"}); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("with the ledger refusing: %v", err)
	}
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskRunning || ts.Attempt.State != ledger.AttemptRunning || ts.Attempt.Result != "" {
		t.Fatalf("after the refusal: %+v %+v", ts, ts.Attempt)
	}
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	stopRun(t, e, run.ID)
}

func TestASessionEndedDuringASubmissionFailsTheRefusedAttempt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "draft.txt"), "wip\n")
	// The session ends while the result is being taken (as managedEnded
	// remembers it); the refusal then fails the attempt.
	e.mu.Lock()
	e.runs[run.ID].task("build").attempts[0].sessionEnded = true
	e.mu.Unlock()
	var dirty *DirtyWorktreeError
	if _, err := e.SubmitResult(context.Background(), a.Attempt.SessionID, Result{}); !errors.As(err, &dirty) {
		t.Fatalf("a dirty worktree: %v", err)
	}
	waitTaskWhere(t, e, run.ID, "build", "at its next attempt", func(ts TaskState) bool { return ts.Attempts == 2 })
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptFailed || row.Stage != ledger.AttemptRunning || row.Error != "the session ended before a result was submitted" {
		t.Fatalf("after the refusal: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestAFailedCommitPausesDispatchUntilUnpaused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	// Refused once (a disk full a moment): the event itself is writable.
	l.mu.Lock()
	l.failOn, l.failOnce = "task.succeeded", true
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	// The acceptance could not be committed: the run's dispatch pauses,
	// nothing rests on it, and the pause's own commit carries the rows and
	// the acceptance's events, before its own.
	waitLogged(t, e, run.ID, "scheduling paused", 1, 10*time.Second)
	r, _ := e.Get(run.ID)
	if !r.Paused || taskOf(t, e, run.ID, "test").Attempts != 0 {
		t.Fatalf("after the failed commit: paused=%v test=%+v", r.Paused, taskOf(t, e, run.ID, "test"))
	}
	// The log's note is made inside the pause's change, and its commit comes
	// after it, outside the lock: wait on the ledger for what it must hold
	// (the pause's own event committed), never read it at the note.
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	waitFake(t, "the pause committed", func() bool { return slices.Contains(l.kinds(run.ID), "run.paused") })
	l.mu.Lock()
	row := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	if row.State != ledger.TaskSucceeded {
		t.Fatalf("build's row after the pause: %+v", row)
	}
	if kinds := l.kinds(run.ID); slices.Index(kinds, "task.succeeded") < 0 || slices.Index(kinds, "task.succeeded") > slices.Index(kinds, "run.paused") {
		t.Fatalf("the acceptance's event with the pause: %v", kinds)
	}
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func TestACheckWhoseOutcomeIsNotRecordedFailsTheAttempt(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	l.mu.Lock()
	l.failOn = "check.finished"
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	a2 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a2.Attempts != 2 {
		t.Fatalf("attempt 2: %+v", a2)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptFailed || row.Stage != "check ok" || !strings.Contains(row.Error, "could not record the check's outcome") {
		t.Fatalf("attempt 1: %+v", row)
	}
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	submit(t, e, a2.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 2)
	stopRun(t, e, run.ID)
}

func TestAResultThatLinksTheCrewsDirectoryAwayFailsItsChecks(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "web", "app.txt"), "app\n")
	commitAll(t, dir, "web")
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	c := repoCrew(dir, build)
	c.Cwd = filepath.Join(dir, "web")
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	// The result replaces the crew's directory with a link out of the tree.
	if err := os.RemoveAll(filepath.Join(a.Attempt.Worktree, "web")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(a.Attempt.Worktree, "web")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, a.Attempt.Worktree, "web is a link now")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "linked"})
	// The next attempt starts where attempt 1 did, not at the result that
	// could not be checked out: built on, it would fail at once at its
	// worktree and spend an attempt nobody ran.
	a2 := waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptFailed || row.Stage != "checkout" {
		t.Fatalf("attempt 1: %+v", row)
	}
	if a2.Attempt.Start != a.Attempt.Start {
		t.Fatalf("attempt 2 starts at %s, not where attempt 1 did (%s)", short(a2.Attempt.Start), short(a.Attempt.Start))
	}
	js, err := os.ReadFile(filepath.Join(a2.Attempt.Worktree, "web", taskDir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f TaskFile
	if err := json.Unmarshal(js, &f); err != nil {
		t.Fatal(err)
	}
	if f.StartFrom != "where attempt 1 started: its result was refused at checkout" || f.Previous == nil || f.Previous.Branch != a.Attempt.Branch || f.Previous.Result == "" {
		t.Fatalf("attempt 2's task file: starts from %q, the attempt before %+v", f.StartFrom, f.Previous)
	}
	if l.count(run.ID, "check.started") != 0 {
		t.Fatal("a check ran through the link")
	}
	stopRun(t, e, run.ID)
}

func TestABroadGraphIsReadiedQuickly(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	var tasks []Task
	for i := range 100 {
		id := fmt.Sprintf("t%d", i)
		member := fmt.Sprintf("m%d", i%12)
		var needs []string
		for _, j := range []int{i - 1, i - 2} {
			if j >= 0 {
				needs = append(needs, fmt.Sprintf("t%d", j))
			}
		}
		tasks = append(tasks, work(id, member, needs...))
	}
	began := time.Now()
	run := launchManaged(t, e, repoCrew(dir, tasks...))
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the launch took %v", took)
	}
	if ts := taskOf(t, e, run.ID, "t50"); ts.State != ledger.TaskBlocked || ts.Reason != "needs t49, t48" {
		t.Fatalf("t50: %+v", ts)
	}
	waitAttempt(t, e, run.ID, "t0", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func TestAFailedUnpauseKeepsTheRunPaused(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ctx := context.Background()
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.failOn = "run.paused"
	l.mu.Unlock()
	if err := e.PauseRun(ctx, run.ID, false); err == nil {
		t.Fatal("an unpause the ledger refused was taken")
	}
	if r, _ := e.Get(run.ID); !r.Paused {
		t.Fatal("the run is not paused after the refused unpause")
	}
	// The attempt in flight ends; nothing rests on the refused unpause.
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	time.Sleep(300 * time.Millisecond)
	if ts := taskOf(t, e, run.ID, "test"); ts.Attempts != 0 || ts.State != ledger.TaskReady {
		t.Fatalf("test while paused: %+v", ts)
	}
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	if err := e.PauseRun(ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func TestAnExecutableBitChangeIsSeenWhateverCoreFileMode(t *testing.T) {
	dir := newRepo(t)
	runGit(t, dir, "config", "core.fileMode", "false")
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "mode", Argv: []string{"sh", "-c", "chmod +x README"}}}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTaskWhere(t, e, run.ID, "build", "at its next attempt", func(ts TaskState) bool { return ts.Attempts == 2 })
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptFailed || row.Error != "changed tracked files: README" {
		t.Fatalf("attempt 1: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestAReviewsFindingsAreKeptInTheLedger(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("review", "critic", "build")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built", Issues: []string{"no tests yet"}})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	rev := waitAttempt(t, e, run.ID, "review", ledger.AttemptRunning)
	findings := []Finding{{Severity: "major", Path: "built.txt", Line: 1, Message: "says yes without saying why"}}
	submit(t, e, rev.Attempt.SessionID, Result{Verdict: VerdictNeedsChanges, Summary: "one thing", Findings: findings})
	// The review's verdict reopens build (row 6): the review waits again with
	// its result withdrawn, and build's second attempt carries the findings.
	waitTaskWhere(t, e, run.ID, "build", "at attempt 2", func(ts TaskState) bool { return ts.Attempts == 2 })
	if rv := taskOf(t, e, run.ID, "review"); rv.State != ledger.TaskBlocked || rv.Result != nil || rv.Attempt == nil || rv.Attempt.State != ledger.AttemptSucceeded {
		t.Fatalf("the review after its verdict: %+v", rv)
	}
	row := l.attemptRow(t, run.ID, "review", 1)
	var kept Result
	if err := json.Unmarshal([]byte(row.Result), &kept); err != nil || kept.Verdict != VerdictNeedsChanges || len(kept.Findings) != 1 || kept.Findings[0].Path != "built.txt" {
		t.Fatalf("the ledger's result: %q %v", row.Result, err)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); !strings.Contains(row.Result, "no tests yet") {
		t.Fatalf("build's result in the ledger: %q", row.Result)
	}
	stopRun(t, e, run.ID)
}

func TestEarlierPendingRowsSurviveALaterUndo(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	l.mu.Lock()
	l.failOn = "task.succeeded,run.paused"
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	// The acceptance and the pause it leads to both fail: the rows wait.
	waitLogged(t, e, run.ID, "scheduling paused", 1, 10*time.Second)
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if err := e.PauseRun(context.Background(), run.ID, false); err == nil {
		t.Fatal("an unpause the ledger refused was taken")
	}
	if r, _ := e.Get(run.ID); !r.Paused {
		t.Fatal("the undone unpause left the run unpaused")
	}
	l.mu.Lock()
	if row := l.tasks[run.ID+"/build"]; row.State == ledger.TaskSucceeded {
		l.mu.Unlock()
		t.Fatal("the task's row landed before any commit")
	}
	l.failOn = ""
	l.mu.Unlock()
	// The next commit carries everything that waited, and the end.
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	trow, rrow := l.tasks[run.ID+"/build"], l.runs[run.ID]
	l.mu.Unlock()
	if trow.State != ledger.TaskSucceeded || rrow.Outcome != ledger.OutcomeSucceeded || rrow.State != ledger.RunFinished || rrow.Paused {
		t.Fatalf("after the commit that carried the rows: task %+v run %+v", trow, rrow)
	}
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptSucceeded {
		t.Fatalf("the attempt's row: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestADispatchDecidesOnCommittedState(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	ctx := context.Background()
	if err := e.PauseRun(ctx, run.ID, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	// An unpause held in its commit and then refused: while it hangs the
	// run reads as unpaused, and nothing dispatches on that.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "run.paused[paused=false]", make(chan struct{}, 1), make(chan struct{})
	l.failOn, l.failOnce = "run.paused[paused=false]", true
	l.mu.Unlock()
	unpaused := make(chan error, 1)
	go func() { unpaused <- e.PauseRun(ctx, run.ID, false) }()
	select {
	case <-l.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the unpause did not reach its commit")
	}
	submitted := make(chan error, 1)
	go func() {
		_, err := e.SubmitResult(ctx, a.Attempt.SessionID, Result{Summary: "built"})
		submitted <- err
	}()
	time.Sleep(200 * time.Millisecond)
	close(l.release)
	if err := <-unpaused; err == nil {
		t.Fatal("the refused unpause was taken")
	}
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	time.Sleep(300 * time.Millisecond)
	r, _ := e.Get(run.ID)
	if !r.Paused || taskOf(t, e, run.ID, "test").Attempts != 0 {
		t.Fatalf("after the refused unpause: paused=%v test=%+v", r.Paused, taskOf(t, e, run.ID, "test"))
	}
	if err := e.PauseRun(ctx, run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
}

func TestASecondSubmitWaitsForTheFirstsCommit(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	ctx := context.Background()
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "result.submitted", make(chan struct{}, 1), make(chan struct{})
	l.failOn, l.failOnce = "result.submitted", true
	l.mu.Unlock()
	first := make(chan error, 1)
	go func() {
		_, err := e.SubmitResult(ctx, a.Attempt.SessionID, Result{Summary: "built"})
		first <- err
	}()
	select {
	case <-l.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the first submit did not reach its commit")
	}
	second := make(chan error, 1)
	go func() {
		_, err := e.SubmitResult(ctx, a.Attempt.SessionID, Result{Summary: "built"})
		second <- err
	}()
	// The second waits: it is not answered on a submission not committed.
	select {
	case err := <-second:
		t.Fatalf("the second submit answered during the first's commit: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(l.release)
	if err := <-first; err == nil {
		t.Fatal("the first submit's refused commit was taken")
	}
	if err := <-second; err != nil {
		t.Fatalf("the second submit, after the first's failure: %v", err)
	}
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if row := l.attemptRow(t, run.ID, "build", 1); row.State != ledger.AttemptSucceeded || row.Result == "" {
		t.Fatalf("the attempt's row: %+v", row)
	}
	stopRun(t, e, run.ID)
}

func TestALaunchNotRecordedEndsItsSession(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	l.mu.Lock()
	l.failOn, l.failOnce = "attempt.launched", true
	l.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	// Attempt 1's session is launched and not recorded: it is stopped and
	// gets no task; attempt 2 is recorded and runs.
	a2 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if a2.Attempts != 2 {
		t.Fatalf("attempt 2: %+v", a2)
	}
	row := l.attemptRow(t, run.ID, "build", 1)
	if row.State != ledger.AttemptFailed || row.Stage != "launch" || !strings.Contains(row.Error, "could not record the launch") || row.SessionID == "" {
		t.Fatalf("attempt 1: %+v", row)
	}
	fl.mu.Lock()
	first := fl.byID[row.SessionID]
	fl.mu.Unlock()
	select {
	case <-first.Ended():
	case <-time.After(5 * time.Second):
		t.Fatal("the unrecorded session was not stopped")
	}
	stopRun(t, e, run.ID)
}

func TestACancelsSessionStopsOnceTheCancelIsDurable(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, a)
	l.mu.Lock()
	l.failOn, l.failOnce = "task.cancelled", true
	l.mu.Unlock()
	if err := e.CancelTask(context.Background(), run.ID, "build"); err == nil {
		t.Fatal("a cancel the ledger refused was reported taken")
	}
	// The pause's commit carried the cancel: the session goes with it.
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled attempt's session was not stopped")
	}
	l.mu.Lock()
	trow := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	if trow.State != ledger.TaskCancelled {
		t.Fatalf("the task's row: %+v", trow)
	}
	if r, _ := e.Get(run.ID); !r.Paused {
		t.Fatal("the run is not paused after the refused commit")
	}
	stopRun(t, e, run.ID)
}

func TestAStopDuringTheChecksSetupLeavesTheAttemptStopped(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"true"}}}
	c := repoCrew(dir, build)
	c.Setup = []Check{{ID: "slow", Argv: []string{"sh", "-c", "test -f built.txt && sleep 30 || true"}, TimeoutSeconds: 60}}
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitAttempt(t, e, run.ID, "build", ledger.AttemptVerifying)
	time.Sleep(500 * time.Millisecond) // the checkout made, the setup sleeping
	began := time.Now()
	stopRun(t, e, run.ID)
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("the stop waited %v", took)
	}
	ts := taskOf(t, e, run.ID, "build")
	if ts.Attempt.State != ledger.AttemptStopped || ts.State != ledger.TaskReady {
		t.Fatalf("after the stop: %+v %+v", ts, ts.Attempt)
	}
}

func TestForgettingARunDropsEveryAttemptsSession(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("test", "lead", "build")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	b := waitAttempt(t, e, run.ID, "test", ledger.AttemptRunning)
	stopRun(t, e, run.ID)
	e.mu.Lock()
	e.forget(e.runs[run.ID])
	e.mu.Unlock()
	for _, id := range []string{a.Attempt.SessionID, b.Attempt.SessionID} {
		if _, ok := e.bySession.Load(id); ok {
			t.Fatalf("session %s still maps to the forgotten run", id)
		}
	}
}

func TestACancelsSessionStopsWithWhateverCommitCarriesIt(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, a)
	l.mu.Lock()
	l.failOn = "task.cancelled,run.paused"
	l.mu.Unlock()
	if err := e.CancelTask(context.Background(), run.ID, "build"); err == nil {
		t.Fatal("a cancel the ledger refused was reported taken")
	}
	// Neither the cancel nor the pause landed: the session waits for a
	// commit, and goes with the first that lands.
	select {
	case <-p.Done():
		t.Fatal("the session was stopped before the cancel was durable")
	case <-time.After(500 * time.Millisecond):
	}
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled attempt's session was not stopped once durable")
	}
	l.mu.Lock()
	trow := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	if trow.State != ledger.TaskCancelled {
		t.Fatalf("the task's row: %+v", trow)
	}
	stopRun(t, e, run.ID)
}

func TestALiveManagedRunIsNeverEvicted(t *testing.T) {
	old := maxRuns
	maxRuns = 1
	t.Cleanup(func() { maxRuns = old })
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	c := repoCrew(dir, work("build", "lead"))
	c.Setup = []Check{{ID: "slow", Argv: []string{"sleep", "30"}, TimeoutSeconds: 60}}
	run := launchManaged(t, e, c)
	// The attempt is in its setup: no member starting, no session, and
	// the run must stay through another launch at the bound.
	waitLogged(t, e, run.ID, "attempt 1 reserved", 1, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	other, release, err := e.LaunchNamed(ctx, testCrew(immediate("lead", "hello")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the managed run was evicted while its attempt was in setup")
	}
	stopRun(t, e, run.ID)
	stopRun(t, e, other.ID)
}

func TestAManagedRunsEndIsToldOnceItIsCommitted(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	ends := make(chan string, 4)
	e.OnEnd = func(r Run) { ends <- r.State }
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, a)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	// The acceptance's commit held: the run reads as finished meanwhile,
	// and its session ending then tells no end.
	l.mu.Lock()
	l.blockOn, l.blocked, l.release = "task.succeeded", make(chan struct{}, 1), make(chan struct{})
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	select {
	case <-l.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the acceptance did not reach its commit")
	}
	if r, _ := e.Get(run.ID); r.State != RunFinished {
		t.Fatalf("the run while the acceptance commits: %s", r.State)
	}
	p.End(0)
	select {
	case st := <-ends:
		t.Fatalf("an end (%s) was told before the acceptance was committed", st)
	case <-time.After(500 * time.Millisecond):
	}
	close(l.release)
	select {
	case st := <-ends:
		if st != RunFinished {
			t.Fatalf("the end told: %s", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end was told once the acceptance was committed")
	}
	stopRun(t, e, run.ID)
}

func TestAStopTheLedgerCouldNotRecordIsTheStopsError(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	ends := make(chan string, 4)
	e.OnEnd = func(r Run) { ends <- r.State }
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	l.mu.Lock()
	l.failOn = "run.stopped"
	l.mu.Unlock()
	err := e.Stop(context.Background(), run.ID)
	if err == nil || !strings.Contains(err.Error(), "could not record the stop") {
		t.Fatalf("a stop the ledger refused: %v", err)
	}
	if r, _ := e.Get(run.ID); r.State != RunStopped || r.Tasks[0].Attempt.State != ledger.AttemptStopped {
		t.Fatalf("the run after the refused stop: %s %+v", r.State, r.Tasks[0].Attempt)
	}
	select {
	case st := <-ends:
		t.Fatalf("an end (%s) was told for a stop the ledger does not have", st)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestARefusedReservationLeavesNoRowBehind(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	fl.mu.Lock()
	fl.reserveFails = session.ErrTooManySessions
	fl.mu.Unlock()
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), work("docs", "writer")))
	waitLogged(t, e, run.ID, "no place for attempt 1 yet", 2, 5*time.Second)
	// The note is made in the change, its event committed after: wait for
	// the ledger to hold both.
	waitTrue(t, "two refusals in the ledger", func() bool { return l.count(run.ID, "attempt.unreserved") == 2 })
	l.mu.Lock()
	rows := len(l.attempts)
	build, docs := l.tasks[run.ID+"/build"], l.tasks[run.ID+"/docs"]
	l.mu.Unlock()
	if rows != 0 || build.Attempts != 0 || docs.Attempts != 0 {
		t.Fatalf("the ledger after two refused reservations: %d attempt rows, build %+v, docs %+v", rows, build, docs)
	}
	stopRun(t, e, run.ID)
}

func TestARunWithAChangeTheLedgerHasNotTakenIsNotEvicted(t *testing.T) {
	old := maxRuns
	maxRuns = 1
	t.Cleanup(func() { maxRuns = old })
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	_, p := fl.attemptSession(t, a)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	l.mu.Lock()
	l.failOn = "task.succeeded,run.paused"
	l.mu.Unlock()
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitLogged(t, e, run.ID, "scheduling paused", 1, 10*time.Second)
	// Every task terminal and the session gone, but the acceptance not
	// committed: a launch at the bound must not evict the run.
	p.End(0)
	waitTrue(t, "the session ended", func() bool { return memberState(t, e, run.ID, "lead").Status == MemberEnded })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	other, release, err := e.LaunchNamed(ctx, testCrew(immediate("lead", "hello")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); !ok {
		t.Fatal("the run was evicted with its acceptance uncommitted")
	}
	// Committed, nothing holds it: the next launch may take its place.
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	stopRun(t, e, other.ID)
	another, release2, err := e.LaunchNamed(ctx, testCrew(immediate("lead", "hello again")), "")
	release2()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(run.ID); ok {
		t.Fatal("the finished, committed run was kept past the bound")
	}
	stopRun(t, e, another.ID)
}

func TestACancelledChecksProcessRunsUntilTheCancelIsDurable(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "slow", Argv: []string{"sh", "-c", "sleep 30"}, TimeoutSeconds: 60}}
	run := launchManaged(t, e, repoCrew(dir, build))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTrue(t, "the check running", func() bool { return l.count(run.ID, "check.running") == 1 })
	l.mu.Lock()
	l.failOn = "task.cancelled,run.paused"
	l.mu.Unlock()
	if err := e.CancelTask(context.Background(), run.ID, "build"); err == nil {
		t.Fatal("a cancel the ledger refused was reported taken")
	}
	// Not durable: the check runs on.
	time.Sleep(500 * time.Millisecond)
	if n := l.count(run.ID, "check.finished"); n != 0 {
		t.Fatalf("the check ended before the cancel was durable (%d finished)", n)
	}
	l.mu.Lock()
	l.failOn = ""
	l.mu.Unlock()
	if err := e.PauseRun(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "the check interrupted once the cancel was durable", func() bool { return l.count(run.ID, "check.finished") == 1 })
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskCancelled {
		t.Fatalf("the task: %+v", ts)
	}
	stopRun(t, e, run.ID)
}
