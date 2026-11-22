package crew

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A context provider's seams in the engine (docs/plans/context-seams.md,
// CT-1): what the query tells of a run, the pack mark in the task file,
// and the commit notes.

// queries records the queries the engine asks with, answering no block.
type queries struct {
	mu sync.Mutex
	qs []ext.ContextQuery
}

func (r *queries) ask(_ context.Context, q ext.ContextQuery) []TaskContext {
	r.mu.Lock()
	r.qs = append(r.qs, q)
	r.mu.Unlock()
	return nil
}

// of is the query asked for task's attempt n, waiting for it.
func (r *queries) of(t *testing.T, task string, n int) ext.ContextQuery {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.mu.Lock()
		i := slices.IndexFunc(r.qs, func(q ext.ContextQuery) bool { return q.Task == task && q.Attempt == n })
		var q ext.ContextQuery
		if i >= 0 {
			q = r.qs[i]
		}
		r.mu.Unlock()
		if i >= 0 {
			return q
		}
		if time.Now().After(deadline) {
			t.Fatalf("no query for %s #%d", task, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A run launched from a ticket's plan revision tells a provider its base,
// the ticket and revision with the ticket's people and the package's
// digest, the task's checks, who launched it, and the evidence: a review
// is told its input's attempt and start, the candidate and its checks'
// receipts before the ticket and the plan. Each query is one Valid takes.
func TestQueryCarriesTicketBaseAndEvidence(t *testing.T) {
	dir := newRepo(t)
	e, _, _ := managedEngine(t)
	rec := &queries{}
	useContextFiles(t, e)
	e.SetContext(rec.ask)
	base := head(t, dir)
	runID := "api-sweep-0000c0de"
	var p Package
	if err := json.Unmarshal(testPackage(t, runID, base), &p); err != nil {
		t.Fatal(err)
	}
	p.Ticket.Owner, p.Ticket.Assignees, p.Launch.By = "Ada", []string{"Grace", "Line\nbreak", "Linus"}, "Nate"
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	build := work("build", "lead")
	build.Checks = []Check{{ID: "ok", Argv: []string{"sh", "-c", "test -e built.txt"}}}
	c := repoCrew(dir, build, review("check", "critic", "build"))
	c.ID = "api-sweep"
	run := launchPlanned(t, e, c, Planned{RunID: runID, Base: base, Digest: packageDigest(raw), Package: raw})
	defer stopRun(t, e, run.ID)

	q := rec.of(t, "build", 1)
	ticket := &ext.ContextTicket{Key: "CND-12", Rev: 7, Plan: testPlanID, PlanN: 2, Digest: packageDigest(raw), Owner: "Ada", Assignees: []string{"Grace", "Linus"}}
	if q.Base != base || !reflect.DeepEqual(q.Ticket, ticket) || q.LaunchedBy != "Nate" ||
		!reflect.DeepEqual(q.Checks, []ext.ContextCheck{{ID: "ok", Argv: []string{"sh", "-c", "test -e built.txt"}}}) ||
		!reflect.DeepEqual(q.Evidence, []string{"ticket:CND-12", "plan:CND-12/2"}) || q.Inputs != nil {
		t.Fatalf("build's query: %+v %+v", q, q.Ticket)
	}
	if err := q.Valid(); err != nil {
		t.Fatalf("build's query: %v", err)
	}

	result := commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
	_ = result
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	q = rec.of(t, "check", 1)
	cand := taskOf(t, e, run.ID, "build").Result.Commit
	if !reflect.DeepEqual(q.Inputs, []ext.ContextInput{{Task: "build", Commit: cand, Attempt: 1, Start: base}}) || q.Start != cand || q.Base != base || q.Checks != nil {
		t.Fatalf("the review's query: %+v", q)
	}
	ev := q.Evidence
	if len(ev) < 4 || ev[0] != "candidate:"+run.ID+"/build/1" || !slices.Contains(ev, "check:"+run.ID+"/build/1/ok/1") ||
		!slices.Equal(ev[len(ev)-2:], []string{"ticket:CND-12", "plan:CND-12/2"}) {
		t.Fatalf("the review's evidence: %v", ev)
	}
	if err := q.Valid(); err != nil {
		t.Fatalf("the review's query: %v", err)
	}
}

// A check's argv past the query's bounds, or with an entry it cannot
// hand, is left out and said cut; one within them is handed as the crew
// holds it. A review's findings come with the input they are of, each
// message cut at a character's start to the bound. The query is valid.
func TestQueryCutsALongArgv(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	long := make([]string, ext.MaxContextArgv+1)
	for i := range long {
		long[i] = "x"
	}
	fix := &task{def: Task{ID: "fix", Kind: TaskWork, Instructions: "Fix it.", Checks: []Check{
		{ID: "long", Argv: long},
		{ID: "wide", Argv: []string{"echo", strings.Repeat("w", ext.MaxContextArg+1)}},
		{ID: "odd", Argv: []string{"echo", "\xff"}},
		{ID: "fine", Argv: long[:ext.MaxContextArgv]},
	}}}
	msg := strings.Repeat("é", 800) // 1600 bytes
	rv := &task{def: Task{ID: "check", Kind: TaskReview}, result: &taskResult{attempt: 2, verdict: VerdictNeedsChanges, reviewed: sha,
		findings: []Finding{{Severity: "major", Path: "a.go", Line: 3, Message: msg}}}}
	r := &run{id: "api-0a0b0c0d", crewID: "api", goal: "Ship it.", base: sha, tasks: []*task{rv, fix}}
	a := &attempt{t: fix, n: 1, worktree: "/w", start: sha, inputs: []input{{task: "check", attempt: 2, commit: sha, verdict: VerdictNeedsChanges}}}
	q := contextQuery(r, a)
	want := []ext.ContextCheck{{ID: "long", Cut: true}, {ID: "wide", Cut: true}, {ID: "odd", Cut: true}, {ID: "fine", Argv: long[:ext.MaxContextArgv]}}
	if !reflect.DeepEqual(q.Checks, want) {
		t.Fatalf("the checks: %+v", q.Checks)
	}
	in := q.Inputs[0]
	if in.Start != sha || in.Verdict != VerdictNeedsChanges || len(in.Findings) != 1 || len(in.Findings[0].Message) > ext.MaxContextFinding ||
		!utf8.ValidString(in.Findings[0].Message) || !strings.HasPrefix(msg, in.Findings[0].Message) || in.Findings[0].Path != "a.go" {
		t.Fatalf("the input: %+v", in)
	}
	if err := q.Valid(); err != nil {
		t.Fatal(err)
	}
	// The crew's own argv stays its own.
	q.Checks[3].Argv[0] = "changed"
	if fix.def.Checks[3].Argv[0] != "x" {
		t.Fatal("the query shares the crew's argv")
	}
}

// A block with a pack mark is written with "Pack: <mark>" under its
// title, in task.md, and with its mark in task.json; one with none is
// written as before marks existed.
func TestTaskFileShowsThePackMark(t *testing.T) {
	f := goldenTaskFiles()["work"]
	f.Context = []TaskContext{
		{Source: "graph", Title: "Callers of Parse", Ref: "parse.go", Body: "Parse is called from main.go.", Pack: "p-4f2c-1"},
		{Source: "graph", Title: "No mark", Ref: "lex.go", Body: "Lex is pure."},
	}
	md, js, _, err := fitTaskFile(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Callers of Parse (from graph)\n\nPack: p-4f2c-1\n\nAbout: parse.go\n\n```\nParse is called from main.go.\n```\n\n",
		"### No mark (from graph)\n\nAbout: lex.go\n\n```\nLex is pure.\n```\n\n",
	} {
		if !strings.Contains(string(md), want) {
			t.Errorf("task.md lacks %q:\n%s", want, md)
		}
	}
	if strings.Count(string(md), "Pack:") != 1 || strings.Count(string(js), `"pack"`) != 1 || !strings.Contains(string(js), `"pack": "p-4f2c-1"`) {
		t.Fatalf("the marks: %s", js)
	}
	if TaskContextBytes(f.Context[0]) != TaskContextBytes(TaskContext{Source: "graph", Title: "Callers of Parse", Ref: "parse.go", Body: "Parse is called from main.go."})+len(`,"pack":"p-4f2c-1"`) {
		t.Fatal("TaskContextBytes does not count the mark")
	}
}

// notes records the commit notes the engine tells.
type notes struct {
	mu sync.Mutex
	ns []ext.CommitNote
}

func (n *notes) note(c ext.CommitNote) {
	n.mu.Lock()
	n.ns = append(n.ns, c)
	n.mu.Unlock()
}

func (n *notes) all() []ext.CommitNote {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.ns)
}

// await waits for the notes to number count.
func (n *notes) await(t *testing.T, count int) []ext.CommitNote {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got := n.all(); len(got) >= count {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d notes, waiting for %d: %+v", len(n.all()), count, n.all())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A provider is told a run's base as its first transaction commits, and
// again as a continuation of it starts; a work attempt's candidate as its
// submit commits; a review's submit tells nothing.
func TestBaseAndCandidateNoted(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	got := &notes{}
	e.SetCommitNotes(got.note)
	base := head(t, dir)
	run := launchManaged(t, e, repoCrew(dir, work("build", "lead"), review("check", "critic", "build")))
	ns := got.await(t, 1)
	if ns[0].Run != run.ID || ns[0].Kind != ext.CommitBase || ns[0].Commit != base || ns[0].Task != "" || ns[0].At.IsZero() {
		t.Fatalf("the base: %+v", ns[0])
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "yes\n", "built")
	cand := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	ns = got.await(t, 2)
	if c := ns[1]; c.Run != run.ID || c.Kind != ext.CommitCandidate || c.Task != "build" || c.Attempt != 1 || c.Commit != cand || c.Valid() != nil {
		t.Fatalf("the candidate: %+v", c)
	}
	rev := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	interrupt(l, run.ID, map[string][2]string{"check/1": {ledger.AttemptInterrupted, "running: the owner stopped"}})
	_ = rev

	e2, _ := readyEngine(t)
	e2.UseLedger(l)
	again := &notes{}
	e2.SetCommitNotes(again.note)
	next, err := e2.ContinueRun(context.Background(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stopRun(t, e2, next.ID)
	ns = again.await(t, 1)
	if ns[0].Run != next.ID || ns[0].Kind != ext.CommitBase || ns[0].Commit != base {
		t.Fatalf("the continuation's base: %+v", ns[0])
	}
	r2 := waitTaskWhere(t, e2, next.ID, "check", "a running attempt", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.State == ledger.AttemptRunning
	})
	submit(t, e2, r2.Attempt.SessionID, Result{Verdict: VerdictApproved, Summary: "fine"})
	waitTask(t, e2, next.ID, "check", ledger.TaskSucceeded, r2.Attempt.N)
	if ns := again.all(); len(ns) != 1 {
		t.Fatalf("a review's submit noted: %+v", ns)
	}
	if len(got.all()) != 2 {
		t.Fatalf("the first engine noted more: %+v", got.all())
	}
}

// A query names each decision once, by its id and state; past
// ext.MaxContextDecisions the open, answered and applied ones are kept
// before the other final states, and then the newest, whatever order the
// reads gave them in, and they are named in the order they were opened.
func TestQueryKeepsTheStandingDecisionsFirst(t *testing.T) {
	closed := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var rows []ledger.DecisionRow
	for n := int64(1); n <= 30; n++ {
		row := ledger.DecisionRow{N: n, State: ledger.DecisionCancelled, Rev: 2, ClosedAt: closed}
		switch n {
		case 2:
			row.State, row.ClosedAt = ledger.DecisionOpen, time.Time{}
		case 4:
			row.State, row.ClosedAt = ledger.DecisionAnswered, time.Time{}
		case 6:
			row.State = ledger.DecisionApplied
		}
		rows = append(rows, row)
	}
	// The ticket's read and the task's give a gate twice, out of order.
	rows = append(rows, rows[1], rows[0])
	slices.Reverse(rows[:15])
	got := decisionStates(rows)
	var want []string
	for _, n := range []int{2, 4, 6} {
		want = append(want, ledger.DecisionID(int64(n)))
	}
	for n := 14; n <= 30; n++ {
		if n != 2 && n != 4 && n != 6 {
			want = append(want, ledger.DecisionID(int64(n)))
		}
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.Request)
	}
	if len(got) != ext.MaxContextDecisions || !slices.Equal(ids, want) {
		t.Fatalf("the decisions: %v, want %v", ids, want)
	}
	if got[0] != (ext.DecisionState{Request: "dr-2", State: ledger.DecisionOpen, Rev: 2}) ||
		got[2] != (ext.DecisionState{Request: "dr-6", State: ledger.DecisionApplied, Final: true, Rev: 2, ClosedAt: closed}) {
		t.Fatalf("the states: %+v", got[:3])
	}
	q := ext.ContextQuery{Run: "api-0a0b0c0d", Crew: "api", Task: "build", Attempt: 1, Kind: "work", Worktree: "/w", Dir: "/w", Decisions: got}
	if err := q.Valid(); err != nil {
		t.Fatal(err)
	}
}

// A decision that still waits is never cut for a closed one: one open
// decision and twenty newer applied ones keep the open one and the
// newest nineteen applied; the applied ones are kept before the other
// final states, newest first within each rank.
func TestQueryKeepsAWaitingDecisionBeforeAClosedOne(t *testing.T) {
	closed := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	rows := []ledger.DecisionRow{{N: 1, State: ledger.DecisionOpen, Rev: 1}}
	for n := int64(2); n <= 21; n++ {
		rows = append(rows, ledger.DecisionRow{N: n, State: ledger.DecisionApplied, Rev: 3, ClosedAt: closed})
	}
	got := decisionStates(slices.Clone(rows))
	if len(got) != ext.MaxContextDecisions || got[0].Request != "dr-1" || got[0].State != ledger.DecisionOpen || got[1].Request != "dr-3" {
		t.Fatalf("one open and twenty applied: %+v", got[:2])
	}
	// Ten cancelled newer than all of them: the applied ones stay ahead.
	for n := int64(22); n <= 31; n++ {
		rows = append(rows, ledger.DecisionRow{N: n, State: ledger.DecisionCancelled, Rev: 2, ClosedAt: closed})
	}
	got = decisionStates(rows)
	if got[0].Request != "dr-1" || got[len(got)-1].Request != "dr-21" || slices.ContainsFunc(got, func(d ext.DecisionState) bool { return d.State == ledger.DecisionCancelled }) {
		t.Fatalf("with ten newer cancelled: %+v", got)
	}
}
