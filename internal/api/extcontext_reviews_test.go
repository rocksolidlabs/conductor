package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// What a context provider is told of the decisions about the work and of
// what people's reviews found (docs/plans/context-seams.md, Decisions and
// reviews in the query). The test waits on the mechanism: the provider's
// queries, the decisions' records, the ledger's attempts and the store's
// reviews.

// queryOf waits for the query the provider was asked for the run's task's
// attempt n, and answers it.
func queryOf(t *testing.T, f *fakeContext, run, task string, n int) ext.ContextQuery {
	t.Helper()
	var q ext.ContextQuery
	waitUntil(t, fmt.Sprintf("the query of %s #%d", task, n), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := slices.IndexFunc(f.asked, func(q ext.ContextQuery) bool { return q.Run == run && q.Task == task && q.Attempt == n })
		if i >= 0 {
			q = f.asked[i]
		}
		return i >= 0
	})
	if err := q.Valid(); err != nil {
		t.Fatalf("the query of %s #%d: %v", task, n, err)
	}
	return q
}

// decisionsOf is q's decisions as "<id> <state>".
func decisionsOf(q ext.ContextQuery) []string {
	var out []string
	for _, d := range q.Decisions {
		out = append(out, d.Request+" "+d.State)
	}
	return out
}

// reviewIDs is q's reviews' ids.
func reviewIDs(q ext.ContextQuery) []string {
	var out []string
	for _, rv := range q.Reviews {
		out = append(out, rv.ID)
	}
	return out
}

// A run of a ticket's plan revision with a gate tells each attempt's
// provider the gate (the ticket's decision) and the task's own decisions,
// by id and state, and none of another task's; the attempt that repairs a
// person's request the findings of that review, by id and state, the same
// ones its task file carries; a review the findings of the people's
// reviews of the task it reviews, with the states their marks gave them,
// and none of another task's; the attempt that repairs a review task's
// verdict no review; and a review asked about again past the bound the
// newest reviews that found anything. Every query is one Valid takes.
func TestQueryCarriesDecisionsAndReviews(t *testing.T) {
	rec := &fakeContext{name: "graph"}
	l := newLaunchTestWith(t, nil, func(c *crew.Crew) {
		c.Members = append(c.Members,
			crew.Member{Name: "critic", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}},
			crew.Member{Name: "writer", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}})
		c.Tasks = append(c.Tasks,
			crew.Task{ID: "docs", Member: "writer", Kind: crew.TaskWork, Instructions: "Write it."},
			crew.Task{ID: "check", Member: "critic", Kind: crew.TaskReview, Instructions: "Review build.", Needs: []string{"build"}})
	}, []Option{withContext(rec)})
	l.srv.limiter = newRateLimiter(1e6, 1e6)
	plan := l.approved(t, map[string]any{"decisions": []any{map[string]any{"id": "ship", "question": "Ship it?", "kind": "approval", "before": []string{"check"}}}})
	run := entityOf(t, l.launch(t, l.testEnv, map[string]any{"plan": plan}, http.StatusCreated, ""), "run")["id"].(string)

	// build #1: the gate holds the review, not build; the ticket's
	// decision is named all the same.
	q := queryOf(t, rec, run, "build", 1)
	if got := decisionsOf(q); !slices.Equal(got, []string{"dr-1 open"}) || q.Reviews != nil {
		t.Fatalf("build #1's query: %v %v", got, q.Reviews)
	}
	// Its attempt asks two decisions: one applied, one withdrawn.
	a1 := runningAttempt(t, l.testEnv, run, "build", 1)
	sid := a1["sessionId"].(string)
	tok := l.agentToken(sid)
	l.expectAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/decisions", approvalBody("r1"), http.StatusCreated, "")
	l.respond(t, adminToken, "dr-2", "a1", 0, 1, http.StatusCreated, "")
	l.decisionWhen(t, "dr-2", "applied", inState(ledger.DecisionApplied))
	l.expectAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/decisions", approvalBody("r2"), http.StatusCreated, "")
	l.expectAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/decisions/dr-3/withdraw", map[string]any{}, http.StatusOK, "")
	c1 := commitFile(t, a1["worktree"].(string), "users.go", "package users\n\nfunc A() {}\nfunc B() {}\n")
	l.submit(t, sid)
	ledgerAttemptIn(t, l.srv, run, "build", 1, ledger.AttemptSucceeded)

	// Another task's candidate, reviewed: never a review of build's.
	d1 := runningAttempt(t, l.testEnv, run, "docs", 1)
	cd := commitFile(t, d1["worktree"].(string), "docs.md", "# Docs\n")
	l.submit(t, d1["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, run, "docs", 1, ledger.AttemptSucceeded)
	docsReview := entityOf(t, sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "docs", 1, cd), l.head, "comment", fileOf("docs.md")), http.StatusCreated, ""), "review")["id"].(string)

	// A person's request for changes of build #1: its repair attempt is
	// told the review's findings, each open, the same ones its task file
	// carries, and the task's decisions in their states beside the gate.
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes",
		lineOf("users.go", "candidate", 3, 4, "func A() {}", "func B() {}"), changeOf("Handle the error.")), http.StatusCreated, "")
	rv1 := entityOf(t, out, "review")["id"].(string)
	if app := applicationOf(t, out); app["state"] != dispatch.ApplicationApplied || app["attempt"] != 2.0 {
		t.Fatalf("the request's application: %v", app)
	}
	fs := reviewNow(t, l.srv, rv1).Findings
	if len(fs) != 2 {
		t.Fatalf("the review's findings: %+v", fs)
	}
	q = queryOf(t, rec, run, "build", 2)
	if got := decisionsOf(q); !slices.Equal(got, []string{"dr-1 open", "dr-2 applied", "dr-3 cancelled"}) {
		t.Fatalf("build #2's decisions: %v", got)
	}
	want := []ext.ContextReview{{ID: rv1, Findings: []ext.ContextReviewFinding{
		{ID: fs[0].ID, State: ext.FindingOpen, Path: "users.go", Line: 3}, {ID: fs[1].ID, State: ext.FindingOpen}}}}
	if !reflect.DeepEqual(q.Reviews, want) {
		t.Fatalf("build #2's reviews: %+v, want %+v", q.Reviews, want)
	}
	// The query is asked before the task files are written; the attempt
	// runs once they are.
	a2 := runningAttempt(t, l.testEnv, run, "build", 2)
	var file crew.TaskFile
	if err := json.Unmarshal(l.treeFile(t, run, "build", 2, "task.json"), &file); err != nil {
		t.Fatal(err)
	}
	if r := file.Repair; r == nil || r.Request == nil || r.Request.Review != rv1 || len(r.Request.Changes) != 2 ||
		r.Request.Changes[0].ID != fs[0].ID || r.Request.Changes[1].ID != fs[1].ID {
		t.Fatalf("the task file's repair: %+v", file.Repair)
	}

	// build #2's candidate; the person marks the findings against it and
	// comments on it; the gate is answered, and the review starts.
	c2 := commitFile(t, a2["worktree"].(string), "users.go", "package users\n\nfunc Alpha() {}\nfunc Beta() {}\n")
	l.submit(t, a2["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, run, "build", 2, ledger.AttemptSucceeded)
	cand2 := candName(run, "build", 2, c2)
	markOf(t, l.testEnv, fs[0].ID, cand2, dispatch.FindingResolved, http.StatusCreated, "")
	markOf(t, l.testEnv, fs[1].ID, cand2, dispatch.FindingWithdrawn, http.StatusCreated, "")
	rv2 := entityOf(t, sendReview(t, l.testEnv, "CND-1", reviewOf(cand2, l.head, "comment", fileOf("users.go")), http.StatusCreated, ""), "review")["id"].(string)
	g := reviewNow(t, l.srv, rv2).Findings
	l.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	l.decisionWhen(t, "dr-1", "applied", inState(ledger.DecisionApplied))

	q = queryOf(t, rec, run, "check", 1)
	if got := decisionsOf(q); !slices.Equal(got, []string{"dr-1 applied"}) {
		t.Fatalf("the review's decisions (build's are its own): %v", got)
	}
	want = []ext.ContextReview{
		{ID: rv1, Findings: []ext.ContextReviewFinding{{ID: fs[0].ID, State: ext.FindingResolved, Path: "users.go", Line: 3}, {ID: fs[1].ID, State: ext.FindingWithdrawn}}},
		{ID: rv2, Findings: []ext.ContextReviewFinding{{ID: g[0].ID, State: ext.FindingOpen, Path: "users.go"}}},
	}
	if !reflect.DeepEqual(q.Reviews, want) || slices.Contains(reviewIDs(q), docsReview) {
		t.Fatalf("the review's reviews: %+v, want %+v", q.Reviews, want)
	}

	// The review sends build back: build #3 repairs a review task's
	// verdict, and is told no person's review.
	r1 := runningAttempt(t, l.testEnv, run, "check", 1)
	rsid := r1["sessionId"].(string)
	l.expectAs(t, l.agentToken(rsid), http.MethodPost, "/api/sessions/"+rsid+"/task/result", map[string]any{"summary": "Not yet.", "verdict": crew.VerdictNeedsChanges,
		"findings": []any{map[string]any{"severity": "major", "path": "users.go", "line": 3, "message": "Say what Alpha does."}}}, http.StatusCreated, "")
	q = queryOf(t, rec, run, "build", 3)
	if got := decisionsOf(q); q.Reviews != nil || !slices.Equal(got, []string{"dr-1 applied", "dr-2 applied", "dr-3 cancelled"}) {
		t.Fatalf("build #3's query: %v %+v", got, q.Reviews)
	}

	// Past the bound: five more reviews that found something and a newest
	// that found nothing. The review's second attempt is told the newest
	// five that found something, in the order they were made.
	var newest []string
	for range ext.MaxContextReviews {
		id := entityOf(t, sendReview(t, l.testEnv, "CND-1", reviewOf(cand2, l.head, "comment", fileOf("users.go")), http.StatusCreated, ""), "review")["id"].(string)
		newest = append(newest, id)
	}
	sendReview(t, l.testEnv, "CND-1", reviewOf(cand2, l.head, "comment"), http.StatusCreated, "")
	a3 := runningAttempt(t, l.testEnv, run, "build", 3)
	commitFile(t, a3["worktree"].(string), "users.go", "package users\n\n// Alpha says hello.\nfunc Alpha() {}\nfunc Beta() {}\n")
	l.submit(t, a3["sessionId"].(string))
	q = queryOf(t, rec, run, "check", 2)
	if got := reviewIDs(q); !slices.Equal(got, newest) {
		t.Fatalf("the second review's reviews: %v, want the newest %v", got, newest)
	}
	if got := decisionsOf(q); !slices.Equal(got, []string{"dr-1 applied"}) {
		t.Fatalf("the second review's decisions: %v", got)
	}
}

// A server with no context provider builds no query and reads no
// decision for it; one with a provider reads them for its attempt.
func TestDecisionsReadOnlyWithAProvider(t *testing.T) {
	for _, tc := range []struct {
		name      string
		providers []ext.ContextProvider
		read      bool
	}{{"no provider", nil, false}, {"a provider", []ext.ContextProvider{&fakeContext{name: "graph"}}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := contextEnv(t, tc.providers...)
			var reads atomic.Int64
			e.srv.runs.SetContextDecisionsReadForTest(func() { reads.Add(1) })
			e.gitRepo(t)
			run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
			release()
			if err != nil {
				t.Fatal(err)
			}
			// The read comes before the task files, which come before the
			// attempt runs.
			runningAttempt(t, e, run.ID, "build", 1)
			if got := reads.Load(); (got > 0) != tc.read {
				t.Fatalf("%d reads of the query's decisions", got)
			}
			stopRunVia(t, e, run.ID)
		})
	}
}

// A finding's path is handed only when it names a file inside the tree
// that the tree's reads hand a provider: an absolute path, one that
// climbs or has a '.' part, and one the deny list refuses are named by
// id and state alone. A finding on the base's side has its path and no
// line: its line is of the base's file.
func TestAFindingsPathIsHeldToTheTree(t *testing.T) {
	e := contextEnv(t, &fakeContext{name: "graph"})
	tree := filepath.Join(e.root, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	e.srv.fileDeny = func() []string { return []string{filepath.Join(tree, "secret.env")} }
	id := func(n int) string { return fmt.Sprintf("%016x", n) }
	rv := dispatch.Review{ReviewSummary: dispatch.ReviewSummary{ID: id(99)}, Findings: []dispatch.Finding{
		{ID: id(1), State: ext.FindingOpen, Path: "src/a.go", Side: dispatch.SideCandidate, From: 7},
		{ID: id(2), State: ext.FindingOpen, Path: "src/a.go", Side: dispatch.SideBase, From: 7},
		{ID: id(3), State: ext.FindingOpen, Path: "/etc/passwd", Side: dispatch.SideCandidate, From: 1},
		{ID: id(4), State: ext.FindingOpen, Path: "../outside.go", Side: dispatch.SideCandidate, From: 1},
		{ID: id(5), State: ext.FindingOpen, Path: "src/../../outside.go", Side: dispatch.SideCandidate, From: 1},
		{ID: id(6), State: ext.FindingOpen, Path: "./src/a.go", Side: dispatch.SideCandidate, From: 1},
		{ID: id(7), State: ext.FindingResolved, Path: "secret.env", Side: dispatch.SideCandidate, From: 2},
	}}
	got := contextReview(rv, treePathOK(e.srv, tree))
	want := ext.ContextReview{ID: id(99), Findings: []ext.ContextReviewFinding{
		{ID: id(1), State: ext.FindingOpen, Path: "src/a.go", Line: 7},
		{ID: id(2), State: ext.FindingOpen, Path: "src/a.go"},
		{ID: id(3), State: ext.FindingOpen}, {ID: id(4), State: ext.FindingOpen}, {ID: id(5), State: ext.FindingOpen},
		{ID: id(6), State: ext.FindingOpen}, {ID: id(7), State: ext.FindingResolved},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the review: %+v\nwant %+v", got, want)
	}
	// A worktree that does not resolve passes no path.
	if got := contextReview(rv, treePathOK(e.srv, filepath.Join(e.root, "gone"))); got.Findings[0] != (ext.ContextReviewFinding{ID: id(1), State: ext.FindingOpen}) {
		t.Fatalf("under a worktree that is gone: %+v", got.Findings[0])
	}
}
