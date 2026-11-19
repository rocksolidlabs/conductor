package ext_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// largestQuery is a query with every field at its bound: valid.
func largestQuery() ext.ContextQuery {
	argv := make([]string, ext.MaxContextArgv)
	for i := range argv {
		argv[i] = strings.Repeat("a", ext.MaxContextArg)
	}
	checks := make([]ext.ContextCheck, ext.MaxContextChecks)
	for i := range checks {
		checks[i] = ext.ContextCheck{ID: "check-" + string(rune('a'+i)), Argv: argv}
	}
	findings := make([]ext.ContextFinding, ext.MaxContextFindings)
	for i := range findings {
		findings[i] = ext.ContextFinding{Severity: "minor", Path: "a/b.go", Line: i, Message: strings.Repeat("m", ext.MaxContextFinding)}
	}
	assignees := make([]string, ext.MaxContextAssignees)
	for i := range assignees {
		assignees[i] = strings.Repeat("é", ext.MaxContextPerson)
	}
	evidence := make([]string, ext.MaxContextEvidence)
	for i := range evidence {
		evidence[i] = "ticket:CND-1" + strings.Repeat("0", ext.MaxContextEvidenceRef-len("ticket:CND-1"))
	}
	return ext.ContextQuery{Run: "api-0a0b0c0d", Crew: "api", Task: "build", Attempt: 2, Kind: "work", Goal: "Ship it.", Instructions: "Do it.",
		Worktree: "/r/.conductor/worktrees/api-0a0b0c0d/build.2", Dir: "/r/.conductor/worktrees/api-0a0b0c0d/build.2", Start: sha40, Base: sha64,
		Inputs:     []ext.ContextInput{{Task: "check", Commit: sha40, Attempt: 1, Start: sha40, Verdict: "needs_changes", Findings: findings}},
		Ticket:     &ext.ContextTicket{Key: "CND-12", Rev: 9, Plan: strings.Repeat("ab", 8), PlanN: 3, Digest: sha64, Owner: strings.Repeat("ü", ext.MaxContextPerson), Assignees: assignees},
		Checks:     checks,
		Evidence:   evidence,
		LaunchedBy: strings.Repeat("Ä", ext.MaxContextPerson),
	}
}

// Every new field of a query is checked against its bound: at the bound
// it passes, one past it it is refused; and main's fields keep their
// shapes.
func TestContextQueryValid(t *testing.T) {
	if err := largestQuery().Valid(); err != nil {
		t.Fatalf("the largest query: %v", err)
	}
	if err := (ext.ContextQuery{Run: "api-0a0b0c0d", Crew: "api", Task: "build", Attempt: 1, Kind: "review", Worktree: "/w", Dir: "/w", Start: sha40}).Valid(); err != nil {
		t.Fatalf("a query of main's fields: %v", err)
	}
	past := map[string]func(q *ext.ContextQuery){
		"a ninth check":                func(q *ext.ContextQuery) { q.Checks = append(q.Checks, ext.ContextCheck{ID: "more"}) },
		"an argv of 33 entries":        func(q *ext.ContextQuery) { q.Checks[0].Argv = append(q.Checks[0].Argv, "x") },
		"an argv entry of 513 bytes":   func(q *ext.ContextQuery) { q.Checks[0].Argv[0] += "x" },
		"an argv entry with a NUL":     func(q *ext.ContextQuery) { q.Checks[0].Argv[0] = "a\x00b" },
		"a cut check with its argv":    func(q *ext.ContextQuery) { q.Checks[0].Cut = true },
		"a check id that is no id":     func(q *ext.ContextQuery) { q.Checks[0].ID = "Check" },
		"a 33rd evidence ref":          func(q *ext.ContextQuery) { q.Evidence = append(q.Evidence, "run:api-0a0b0c0d") },
		"an evidence ref of 513 bytes": func(q *ext.ContextQuery) { q.Evidence[0] += "0" },
		"an empty evidence ref":        func(q *ext.ContextQuery) { q.Evidence[0] = "" },
		"an evidence ref of two lines": func(q *ext.ContextQuery) { q.Evidence[0] = "ticket:CND-1\nrun:x" },
		"a launcher of 61 characters":  func(q *ext.ContextQuery) { q.LaunchedBy += "x" },
		"a launcher of two lines":      func(q *ext.ContextQuery) { q.LaunchedBy = "a\nb" },
		"a ninth assignee":             func(q *ext.ContextQuery) { q.Ticket.Assignees = append(q.Ticket.Assignees, "Ada") },
		"an assignee of 61 characters": func(q *ext.ContextQuery) { q.Ticket.Assignees[0] += "x" },
		"an owner of 61 characters":    func(q *ext.ContextQuery) { q.Ticket.Owner += "x" },
		"a ticket's key that is none":  func(q *ext.ContextQuery) { q.Ticket.Key = "cnd-12" },
		"a plan id of 15 hex":          func(q *ext.ContextQuery) { q.Ticket.Plan = q.Ticket.Plan[1:] },
		"a plan number of 0":           func(q *ext.ContextQuery) { q.Ticket.PlanN = 0 },
		"a digest in upper case":       func(q *ext.ContextQuery) { q.Ticket.Digest = strings.ToUpper(q.Ticket.Digest) },
		"a 51st finding": func(q *ext.ContextQuery) {
			q.Inputs[0].Findings = append(q.Inputs[0].Findings, q.Inputs[0].Findings[0])
		},
		"a finding of 1001 bytes":       func(q *ext.ContextQuery) { q.Inputs[0].Findings[0].Message += "m" },
		"a finding of no severity":      func(q *ext.ContextQuery) { q.Inputs[0].Findings[0].Severity = "fatal" },
		"an input's verdict that is no": func(q *ext.ContextQuery) { q.Inputs[0].Verdict = "maybe" },
		"an input's start abbreviated":  func(q *ext.ContextQuery) { q.Inputs[0].Start = sha40[:12] },
		"a base that is a ref":          func(q *ext.ContextQuery) { q.Base = "HEAD" },
		"a start that is an expression": func(q *ext.ContextQuery) { q.Start = sha40 + "^" },
		"a kind that is none":           func(q *ext.ContextQuery) { q.Kind = "plan" },
		"a run that is no run id":       func(q *ext.ContextQuery) { q.Run = "api" },
		"a worktree that is relative":   func(q *ext.ContextQuery) { q.Worktree = "w" },
		"attempt 0":                     func(q *ext.ContextQuery) { q.Attempt = 0 },
	}
	for what, change := range past {
		q := largestQuery()
		change(&q)
		if err := q.Valid(); err == nil {
			t.Errorf("%s: valid", what)
		}
	}
	// One past the bound of 129 inputs, at the bound 128.
	q := largestQuery()
	q.Inputs = make([]ext.ContextInput, ext.MaxContextInputs)
	for i := range q.Inputs {
		q.Inputs[i] = ext.ContextInput{Task: "t", Commit: sha40}
	}
	if err := q.Valid(); err != nil {
		t.Fatalf("%d inputs: %v", ext.MaxContextInputs, err)
	}
	q.Inputs = append(q.Inputs, ext.ContextInput{Task: "t"})
	if q.Valid() == nil {
		t.Fatalf("%d inputs: valid", len(q.Inputs))
	}
	if !ext.ArgOK(strings.Repeat("a", ext.MaxContextArg)) || ext.ArgOK(strings.Repeat("a", ext.MaxContextArg+1)) || ext.ArgOK("\xff") {
		t.Fatal("ArgOK")
	}
}

// A block's pack mark is empty or 1 to MaxContextPack bytes of printable
// ASCII, spaces inside it only.
func TestContextBlockPackValid(t *testing.T) {
	b := ext.ContextBlock{Title: "Callers", Body: "x"}
	for _, ok := range []string{"", "p-4f2c-1", "a b", "~!@#$%^&*()[]{}<>`", strings.Repeat("p", ext.MaxContextPack)} {
		b.Pack = ok
		if err := b.Valid(); err != nil {
			t.Errorf("pack %q: %v", ok, err)
		}
	}
	for _, bad := range []string{" p", "p ", "p\n", "p\tq", "pé", "p\x7f", strings.Repeat("p", ext.MaxContextPack+1)} {
		b.Pack = bad
		if err := b.Valid(); err == nil {
			t.Errorf("pack %q: valid", bad)
		}
	}
}

// A commit note is a base (no task, no attempt) or a candidate (its task
// and attempt), of a full commit id, with its time.
func TestCommitNoteValid(t *testing.T) {
	at := time.Now()
	for _, n := range []ext.CommitNote{
		{Run: "api-0a0b0c0d", Kind: ext.CommitBase, Commit: sha40, At: at},
		{Run: "api-0a0b0c0d", Kind: ext.CommitCandidate, Task: "build", Attempt: 2, Commit: sha64, At: at},
	} {
		if err := n.Valid(); err != nil {
			t.Errorf("%+v: %v", n, err)
		}
	}
	for what, n := range map[string]ext.CommitNote{
		"a base with a task":          {Run: "api-0a0b0c0d", Kind: ext.CommitBase, Task: "build", Commit: sha40, At: at},
		"a candidate with no attempt": {Run: "api-0a0b0c0d", Kind: ext.CommitCandidate, Task: "build", Commit: sha40, At: at},
		"a kind that is none":         {Run: "api-0a0b0c0d", Kind: "head", Commit: sha40, At: at},
		"an abbreviated commit":       {Run: "api-0a0b0c0d", Kind: ext.CommitBase, Commit: sha40[:7], At: at},
		"no time":                     {Run: "api-0a0b0c0d", Kind: ext.CommitBase, Commit: sha40},
		"a run that is none":          {Run: "api", Kind: ext.CommitBase, Commit: sha40, At: at},
	} {
		if n.Valid() == nil {
			t.Errorf("%s: valid", what)
		}
	}
}

// A read names a commit by its full id and a path inside the tree: a ref,
// HEAD, an abbreviated id, an id in upper case, a parent or a path
// expression is refused, and so is a path that could name anything
// outside the tree.
func TestTreeRequestValid(t *testing.T) {
	for _, c := range []string{sha40, sha64} {
		if err := (ext.TreeRequest{Run: "api-0a0b0c0d", Commit: c}).Valid(); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{"", "HEAD", "main", "refs/heads/main", sha40[:12], strings.ToUpper(sha40), sha40 + "^", sha40 + "~1", sha40 + ":README", sha40 + "^{tree}", "-" + sha40[1:], sha40 + "0"} {
		if (ext.TreeRequest{Run: "api-0a0b0c0d", Commit: c}).Valid() == nil {
			t.Errorf("commit %q: valid", c)
		}
		if (ext.BlobRequest{Run: "api-0a0b0c0d", Commit: c, Path: "README"}).Valid() == nil {
			t.Errorf("a blob at %q: valid", c)
		}
	}
	if (ext.TreeRequest{Run: "../x", Commit: sha40}).Valid() == nil {
		t.Error("a run that is no run id: valid")
	}
	for _, p := range []string{"README", "a/b/c.go", ".github/x", "a..b", strings.Repeat("p", ext.MaxTreePath)} {
		if !ext.TreePathOK(p) {
			t.Errorf("path %q: refused", p)
		}
	}
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../b", "a/./b", "a//b", "a/", ".", "a\x00b", ":(top)a", ":/a", ":!x", strings.Repeat("p", ext.MaxTreePath+1)} {
		if ext.TreePathOK(p) {
			t.Errorf("path %q: taken", p)
		}
		if (ext.BlobRequest{Run: "api-0a0b0c0d", Commit: sha40, Path: p}).Valid() == nil {
			t.Errorf("a blob at path %q: valid", p)
		}
	}
	paths := make([]string, ext.MaxDeniedPaths)
	for i := range paths {
		paths[i] = "a"
	}
	if err := (ext.DeniedRequest{Run: "api-0a0b0c0d", Paths: paths}).Valid(); err != nil {
		t.Fatalf("%d paths: %v", len(paths), err)
	}
	if (ext.DeniedRequest{Run: "api-0a0b0c0d", Paths: append(paths, "a")}).Valid() == nil {
		t.Fatalf("%d paths: valid", len(paths)+1)
	}
}
