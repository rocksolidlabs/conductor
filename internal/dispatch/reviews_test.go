package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A ticket's reviews (Program item RV1, docs/plans/review-repair.md,
// PR-1): a person's batch of notes on one candidate against a base,
// recorded with its findings in one transaction; a request's application
// settled try by try; findings marked against later candidates, their
// state read from their marks.

// commitBase is a review's base, in full.
const commitBase = "cccccccccccccccccccccccccccccccccccccccc"

// reviewTicket is a store with a ticket that launched a run of the test
// crew, the ticket's key and the run's id: the ticket holds the run's
// chain, so a review of its candidates is the ticket's.
func reviewTicket(t *testing.T) (s *Store, key, run string) {
	t.Helper()
	s, _, key = planTicket(t)
	l := mustLaunch(t, s, "launch-"+newID(), key, simplePlan(t, s, key), "")
	return s, key, l.RunID
}

// candidateOf is the candidate of the run's task build at attempt n with
// the commit.
func candidateOf(run string, n int, commit string) string {
	return fmt.Sprintf("%s/build/%d@%s", run, n, commit)
}

// lineChange is a change asked for on lines 14 to 16 of a file of the
// candidate, a blocker.
func lineChange(words string) NewFinding {
	return NewFinding{Kind: FindingKindLine, Intent: FindingIntentChange, Severity: "blocker", Path: "internal/api/users.go", Side: SideCandidate,
		From: 14, To: 16, Lines: []string{"func users() {", "\treturn nil", "}"}, Message: words, Resolution: "return the users"}
}

// checkNote is a check finding naming the candidate's receipt of its check
// test, k 1, a change asked for.
func checkNote(candidate string) NewFinding {
	c, _ := ParseCandidate(candidate)
	return NewFinding{Kind: FindingKindCheck, Intent: FindingIntentChange, Severity: "major",
		Check: &CheckRequest{Source: CheckSourceLedger, Receipt: fmt.Sprintf("%s/%s/%d/check/test/1", c.Run, c.Task, c.Attempt), Chosen: "flaky", Quote: "FAIL: TestUsers"}}
}

// failedRef is the ledger's receipt the request's check names, as the
// server fills it: failed with exit 1 on the candidate's commit, its words
// saying so, of the class given.
func failedRef(req CheckRequest, subject, class string) *CheckRef {
	ref, _ := parseReceiptRef(req.Receipt)
	return &CheckRef{Source: req.Source, Receipt: req.Receipt, Run: ref.run, Task: ref.task, Attempt: ref.attempt, Phase: ref.phase, ID: ref.id,
		K: ref.k, Subject: subject, Argv: []string{"go", "test", "./..."}, Exit: exit(1), Stable: true, State: ReceiptFinished, Error: ref.id + " exited 1",
		Where: "native", LogSHA256: strings.Repeat("d", 64), LogBytes: 4096, Tail: "--- FAIL: TestUsers\nFAIL", Class: class,
		Previous: &CheckPrevious{Attempt: ref.attempt - 1, State: "failed", Stage: "checks", Commit: commitB[:40]}}
}

// evidenceOf is the evidence the server works out for the review: each
// finding's anchor by its side, a check finding's receipt failed on the
// candidate's commit, its class code.
func evidenceOf(r NewReview) ReviewEvidence {
	c, _ := ParseCandidate(r.Candidate)
	ev := ReviewEvidence{Findings: []FindingEvidence{}}
	for i, f := range r.Findings {
		e := FindingEvidence{K: i + 1}
		switch {
		case f.Kind != FindingKindLine && f.Kind != FindingKindFile:
		case f.Side == SideBase:
			e.AnchorCommit = r.Base
		default:
			e.AnchorCommit = c.Commit
		}
		if f.Kind == FindingKindCheck && f.Check != nil {
			e.Check = failedRef(*f.Check, c.Commit, "code")
		}
		ev.Findings = append(ev.Findings, e)
	}
	return ev
}

// submit submits the review with its evidence (evidenceOf) under the op
// id.
func submit(s *Store, opID, key string, r NewReview) (Receipt, Review, error) {
	return s.SubmitReview(context.Background(), testOp(opID), key, r, evidenceOf(r))
}

// mustSubmit submits the review, failing the test unless it was done, and
// returns it as it stands.
func mustSubmit(t *testing.T, s *Store, opID, key string, r NewReview) Review {
	t.Helper()
	rc, v, err := submit(s, opID, key, r)
	want(t, rc, err, StatusDone, "")
	if v.ID == "" || v.ID != rc.ID {
		t.Fatalf("%s: the review read %q for the receipt %+v", opID, v.ID, rc)
	}
	return v
}

// request is a request for changes of the candidate against the base with
// the findings.
func request(candidate string, findings ...NewFinding) NewReview {
	return NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictRequestChanges, Summary: "Two things to fix.", Via: ViaWeb, Findings: findings}
}

// mark marks the finding against the candidate, with located.
func mark(s *Store, opID, finding, candidate, verdict string, located string) (Receipt, Mark, error) {
	var loc json.RawMessage
	if located != "" {
		loc = json.RawMessage(located)
	}
	return s.MarkFinding(context.Background(), testOp(opID), finding, NewMark{Candidate: candidate, Verdict: verdict, Via: ViaWeb}, loc)
}

// reviewOf reads the review with the id whole.
func reviewOf(t *testing.T, s *Store, id string) Review {
	t.Helper()
	v, err := s.Review(context.Background(), id)
	if err != nil {
		t.Fatalf("review %s: %v", id, err)
	}
	return v
}

// A request is recorded whole: its candidate and base as sent, its
// verdict, summary, actor and via; its application pending at try 1; each
// finding with its place, its anchor the side's commit (the candidate's,
// or the base for a removed line), a check finding's receipt as the
// server read it with the person's class and quote beside the server's.
// It bumps no ticket's rev. A comment is applied to nothing.
func TestSubmitReviewKeepsItsCandidateAndBase(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	before := ticketOf(t, s, key).Rev
	candidate := candidateOf(run, 2, commitA)
	removed := lineChange("Keep this check.")
	removed.Side, removed.Severity = SideBase, "minor"
	file := NewFinding{Kind: FindingKindFile, Intent: FindingIntentQuestion, Path: "README.md", Side: SideCandidate, Message: "Why this file?"}
	general := NewFinding{Kind: FindingKindGeneral, Intent: FindingIntentNote, Message: "Nice work otherwise."}
	r := request(candidate, lineChange("Return the users."), removed, file, general, checkNote(candidate))
	v := mustSubmit(t, s, "rv1", key, r)
	if v.Candidate != candidate || v.Base != commitBase || v.Verdict != VerdictRequestChanges || v.Summary != r.Summary || v.Actor != "test" ||
		v.CreatedBy != "Tester" || v.Via != ViaWeb || v.TicketKey != key || v.Rev != 1 || v.Fingerprint == "" {
		t.Fatalf("the review: %+v", v.ReviewSummary)
	}
	if a := v.Application; a.State != ApplicationPending || a.Try != 1 || a.Run != "" || !a.At.IsZero() {
		t.Fatalf("the application: %+v", a)
	}
	if v.Counts != (FindingCounts{Open: 5}) || len(v.Findings) != 5 {
		t.Fatalf("the findings: %+v, %d", v.Counts, len(v.Findings))
	}
	for i, f := range v.Findings {
		if f.K != i+1 || f.ReviewID != v.ID || f.Candidate != candidate || f.State != FindingOpen || f.Marks != 0 || f.Latest != nil {
			t.Fatalf("finding %d: %+v", i+1, f)
		}
	}
	if f := v.Findings[0]; f.AnchorCommit != commitA || f.Path != "internal/api/users.go" || f.From != 14 || f.To != 16 || len(f.Lines) != 3 ||
		f.Lines[1] != "\treturn nil" || f.Resolution != "return the users" {
		t.Fatalf("the line finding: %+v", f)
	}
	if f := v.Findings[1]; f.AnchorCommit != commitBase || f.Side != SideBase {
		t.Fatalf("the base-side finding: %+v", f)
	}
	if f := v.Findings[2]; f.AnchorCommit != commitA || f.Severity != "" || len(f.Lines) != 0 {
		t.Fatalf("the file finding: %+v", f)
	}
	if f := v.Findings[3]; f.AnchorCommit != "" || f.Path != "" {
		t.Fatalf("the general finding: %+v", f)
	}
	c := v.Findings[4].Check
	if c == nil || c.Receipt != run+"/build/2/check/test/1" || c.Subject != commitA || c.Class != "code" || c.Chosen != "flaky" ||
		c.Quote != "FAIL: TestUsers" || *c.Exit != 1 || c.Previous == nil || c.Previous.Attempt != 1 {
		t.Fatalf("the check finding's receipt: %+v", c)
	}
	if got := ticketOf(t, s, key).Rev; got != before {
		t.Fatalf("the ticket's rev moved from %d to %d", before, got)
	}
	// Its rows: the review's create, its application's two sets, then each
	// finding's create.
	rows := changesOf(t, s, "test", "rv1")
	var got []string
	for _, c := range rows {
		got = append(got, c.Entity+" "+c.Op+" "+c.Field)
	}
	wantRows := []string{"review create ", "review set app_state", "review set app_try"}
	for range 5 {
		wantRows = append(wantRows, "finding create ")
	}
	if !slices.Equal(got, wantRows) {
		t.Fatalf("the change rows: %v", got)
	}
	// A comment is recorded and applied to nothing.
	cm := mustSubmit(t, s, "rv2", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "Looked."})
	if cm.Application.State != ApplicationNone || cm.Application.Try != 0 || cm.Rev != 1 || len(cm.Findings) != 0 {
		t.Fatalf("the comment: %+v", cm.ReviewSummary)
	}
	page, err := s.Reviews(ctx, key, "")
	if err != nil || len(page.Reviews) != 2 || page.Reviews[0].ID != cm.ID || page.Reviews[1].ID != v.ID || page.Next != "" {
		t.Fatalf("the reviews: %+v %v", page, err)
	}
	if page.Reviews[1].Counts.Open != 5 {
		t.Fatalf("the request's counts in the list: %+v", page.Reviews[1].Counts)
	}
	// A step of the tree that failed the attempt, setup, service or ready,
	// is named as a check's receipt is.
	for i, phase := range []string{PhaseSetup, PhaseService, PhaseReady} {
		f := checkNote(candidate)
		f.Check.Receipt = fmt.Sprintf("%s/build/2/%s/deps/%d", run, phase, i)
		got := mustSubmit(t, s, opID("rv-step", i), key, request(candidate, f))
		if c := got.Findings[0].Check; c == nil || c.Receipt != f.Check.Receipt || c.Phase != phase || c.ID != "deps" || c.K != i {
			t.Fatalf("a %s step's receipt: %+v", phase, c)
		}
	}
}

// A review that cannot be repaired or does not say what it means is
// refused, nothing of it kept: a request with no change, a finding whose
// kind and fields disagree, a check of an external source or of a receipt
// not the candidate's, one that passed or never finished, a review with
// nothing in it, a candidate of a run the ticket does not hold.
func TestSubmitReviewRefusesWhatCannotBeRepaired(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another"})
	otherLaunch := mustLaunch(t, s, "l-other", other, simplePlan(t, s, other), "")
	question := NewFinding{Kind: FindingKindGeneral, Intent: FindingIntentQuestion, Message: "Why?"}
	with := func(f func(*NewFinding)) NewFinding {
		x := lineChange("Fix it.")
		f(&x)
		return x
	}
	external := checkNote(candidate)
	external.Check.Source = CheckSourceExternal
	otherAttempt := checkNote(candidateOf(run, 1, commitA))
	ofPhase := func(phase string) NewFinding {
		f := checkNote(candidate)
		f.Check.Receipt = run + "/build/2/" + phase + "/deps/1"
		return f
	}
	cases := []struct {
		name    string
		review  NewReview
		refusal string
	}{
		{"a request with no change", request(candidate, question), RefusalInvalid},
		{"nothing at all", NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment}, RefusalInvalid},
		{"a verdict of none", NewReview{Candidate: candidate, Base: commitBase, Verdict: "merge", Summary: "x"}, RefusalInvalid},
		{"a base not in full", NewReview{Candidate: candidate, Base: "main", Verdict: VerdictComment, Summary: "x"}, RefusalInvalid},
		{"a candidate not of its shape", NewReview{Candidate: "build#2", Base: commitBase, Verdict: VerdictComment, Summary: "x"}, RefusalInvalid},
		{"a via of none", NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "x", Via: "mail"}, RefusalInvalid},
		{"a line finding with no path", request(candidate, with(func(f *NewFinding) { f.Path = "" })), RefusalInvalid},
		{"a line finding with no lines", request(candidate, with(func(f *NewFinding) { f.Lines = nil })), RefusalInvalid},
		{"a line finding with no side", request(candidate, with(func(f *NewFinding) { f.Side = "" })), RefusalInvalid},
		{"a general finding with a path", request(candidate, with(func(f *NewFinding) { f.Kind, f.Lines, f.From, f.To, f.Side = FindingKindGeneral, nil, 0, 0, "" })), RefusalInvalid},
		{"a file finding with lines", request(candidate, with(func(f *NewFinding) { f.Kind = FindingKindFile })), RefusalInvalid},
		{"a check finding with no receipt", request(candidate, NewFinding{Kind: FindingKindCheck, Intent: FindingIntentChange, Severity: "major"}), RefusalInvalid},
		{"a receipt on a line finding", request(candidate, with(func(f *NewFinding) { f.Check = checkNote(candidate).Check })), RefusalInvalid},
		{"a change with no severity", request(candidate, with(func(f *NewFinding) { f.Severity = "" })), RefusalInvalid},
		{"a question with a severity", request(candidate, lineChange("x"), NewFinding{Kind: FindingKindGeneral, Intent: FindingIntentQuestion, Severity: "nit", Message: "Why?"}), RefusalInvalid},
		{"a finding with no words", request(candidate, with(func(f *NewFinding) { f.Message = " " })), RefusalInvalid},
		{"a kind of none", request(candidate, with(func(f *NewFinding) { f.Kind = "range" })), RefusalInvalid},
		{"an intent of none", request(candidate, with(func(f *NewFinding) { f.Intent = "demand" })), RefusalInvalid},
		{"an external source", request(candidate, external), RefusalInvalid},
		{"a receipt of another attempt", request(candidate, otherAttempt), RefusalInvalid},
		{"a cleanup step's receipt", request(candidate, ofPhase("cleanup")), RefusalInvalid},
		{"a prerequisite's receipt", request(candidate, ofPhase("prerequisite")), RefusalInvalid},
		{"a class of none", request(candidate, func() NewFinding { f := checkNote(candidate); f.Check.Chosen = "gremlins"; return f }()), RefusalInvalid},
		{"a run no ticket holds", request(candidateOf("loose-0000cafe", 2, commitA), lineChange("x")), RefusalInvalid},
		{"another ticket's run", request(candidateOf(otherLaunch.RunID, 2, commitA), lineChange("x")), RefusalRunLinked},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc, v, err := submit(s, opID("bad", i), key, tc.review)
			want(t, rc, err, StatusFailed, tc.refusal)
			if v.ID != "" {
				t.Fatalf("a refusal read a review: %+v", v)
			}
		})
	}
	// The receipt as the ledger holds it: of another commit, passed, or not
	// finished (TestACheckFindingsReceiptIsJudgedByItsPhase has each way).
	for i, fill := range []func(*CheckRef){
		func(c *CheckRef) { c.Subject = commitB[:40] },
		func(c *CheckRef) { c.Exit, c.Error = exit(0), "" },
		func(c *CheckRef) { c.State, c.Exit = ReceiptRunning, nil },
	} {
		r := request(candidate, checkNote(candidate))
		ev := evidenceOf(r)
		fill(ev.Findings[0].Check)
		rc, _, err := s.SubmitReview(ctx, testOp(opID("receipt", i)), key, r, ev)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	// Evidence that does not answer its request is the caller's error:
	// nothing written, no receipt, and the same id goes on to be done.
	r := request(candidate, lineChange("x"), checkNote(candidate))
	for _, broken := range []func(ReviewEvidence) ReviewEvidence{
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings = ev.Findings[:1]; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[0].AnchorCommit = commitBase; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[0].Check = ev.Findings[1].Check; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[1].Check = nil; return ev },
		func(ev ReviewEvidence) ReviewEvidence {
			ev.Findings[1].Check.Receipt = run + "/build/2/check/lint/1"
			return ev
		},
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[1].Check.Class = "who knows"; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[1].Check.State = ""; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[1].Check.State = "done"; return ev },
		func(ev ReviewEvidence) ReviewEvidence { ev.Findings[1].Check.Exit = nil; return ev },
		func(ev ReviewEvidence) ReviewEvidence {
			ev.Findings[1].Check.Argv = slices.Repeat([]string{"x"}, MaxCheckArgv+1)
			return ev
		},
	} {
		rc, _, err := s.SubmitReview(ctx, testOp("evidence"), key, r, broken(evidenceOf(r)))
		if err == nil || !strings.Contains(err.Error(), "evidence") {
			t.Fatalf("broken evidence: %+v %v", rc, err)
		}
	}
	if _, found, _ := s.Receipt(ctx, "test", "evidence"); found {
		t.Fatal("broken evidence kept a receipt")
	}
	if n := count(t, s, "SELECT COUNT(*) FROM reviews"); n != 0 {
		t.Fatalf("%d reviews kept", n)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM review_findings"); n != 0 {
		t.Fatalf("%d findings kept", n)
	}
	rc, _, err := submit(s, "evidence", key, r)
	want(t, rc, err, StatusDone, "")
}

// A receipt is judged failed or passed by exactly the fields the ledger
// says it by, per phase and per way a step ends (CheckRef.passed, whose
// table this test holds a case of for every row): a row still started or
// running is refused as not ended; an interrupted row failed in every
// phase, the engine's with its words and a crashed server's recovered
// with none and exit -1 (Ledger.Recover), its exit given or not; a
// finished check or verify step by how it ended and its words; a finished
// step of the tree (setup, service, ready) by its words alone, as the
// engine writes them; and a step never run whose start was not recorded
// has no row, so no receipt. Each way that passed is refused (nothing
// failed to repair), each way that failed is kept as it was read.
func TestACheckFindingsReceiptIsJudgedByItsPhase(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	// as is the receipt as the ledger's row holds it: its state, its exit
	// (none: the fill gives none, as for a row not finished), signal,
	// timeout, killed, stable and words.
	type as struct {
		state    string
		exit     *int
		signal   string
		timedOut bool
		killed   bool
		stable   bool
		words    string
	}
	// The judgements: kept as failed, refused as passed, refused as not
	// ended, or no receipt at all (the server's fill finds no row).
	const (
		failed = iota
		passed
		unended
		noRow
	)
	type tcase struct {
		name, phase string
		receipt     as
		judged      int
	}
	fin := ReceiptFinished
	cases := []tcase{
		{"a check that passed", PhaseCheck, as{state: fin, exit: exit(0), stable: true}, passed},
		{"a check that passed, a leftover killed with its group", PhaseCheck, as{state: fin, exit: exit(0), stable: true, killed: true}, passed},
		{"a check that exited 1", PhaseCheck, as{state: fin, exit: exit(1), stable: true, words: "exited 1"}, failed},
		{"a check that exited 1, a leftover killed with its group", PhaseCheck, as{state: fin, exit: exit(1), stable: true, killed: true, words: "exited 1"}, failed},
		{"a check of a version 4 row that exited 1, no words", PhaseCheck, as{state: fin, exit: exit(1), stable: true}, failed},
		{"a check ended by a signal", PhaseCheck, as{state: fin, exit: exit(-1), signal: "killed", stable: true, words: "exited -1"}, failed},
		{"a check of a version 4 row ended by a signal, no words", PhaseCheck, as{state: fin, exit: exit(-1), signal: "killed", stable: true}, failed},
		{"a check that timed out", PhaseCheck, as{state: fin, exit: exit(-1), timedOut: true, stable: true, words: "timed out after 10m0s"}, failed},
		{"a check of a version 4 row that timed out, no words", PhaseCheck, as{state: fin, exit: exit(-1), timedOut: true, stable: true}, failed},
		{"a check that left the checkout changed", PhaseCheck, as{state: fin, exit: exit(0), words: "changed tracked files: go.sum"}, failed},
		{"a check of a version 4 row that left the checkout changed, no words", PhaseCheck, as{state: fin, exit: exit(0)}, failed},
		{"a check that could not start", PhaseCheck, as{state: fin, exit: exit(-1), words: "could not run: no such file"}, failed},
		{"a check of a version 4 row that could not start, no words", PhaseCheck, as{state: fin, exit: exit(-1)}, failed},
		{"a check whose outcome the ledger refused", PhaseCheck, as{state: fin, exit: exit(0), stable: true, words: "its outcome was not recorded: the disk is full"}, failed},
		{"a verify step that passed", PhaseVerify, as{state: fin, exit: exit(0), stable: true}, passed},
		{"a verify step that passed, a leftover killed with its group", PhaseVerify, as{state: fin, exit: exit(0), stable: true, killed: true}, passed},
		{"a verify step that exited 2", PhaseVerify, as{state: fin, exit: exit(2), stable: true, words: "lint exited 2"}, failed},
		{"a verify step ended by a signal", PhaseVerify, as{state: fin, exit: exit(-1), signal: "terminated", stable: true, words: "lint exited -1"}, failed},
		{"a verify step that timed out", PhaseVerify, as{state: fin, exit: exit(-1), timedOut: true, stable: true, words: "lint timed out after 10m0s"}, failed},
		{"a verify step that left the checkout changed", PhaseVerify, as{state: fin, exit: exit(0), words: "verify lint changed tracked files: a.go"}, failed},
		{"a verify step that could not start", PhaseVerify, as{state: fin, exit: exit(-1), words: "could not run: no such file"}, failed},
		{"a verify step whose outcome the ledger refused", PhaseVerify, as{state: fin, exit: exit(0), stable: true, words: "the ledger could not record the outcome of the step lint"}, failed},
		{"a worktree's setup that passed (stable unset)", PhaseSetup, as{state: fin, exit: exit(0)}, passed},
		{"a checkout's setup that passed", PhaseSetup, as{state: fin, exit: exit(0), stable: true}, passed},
		{"a setup that passed, a leftover killed with its group", PhaseSetup, as{state: fin, exit: exit(0), killed: true}, passed},
		{"a setup that exited 1", PhaseSetup, as{state: fin, exit: exit(1), words: "deps exited 1"}, failed},
		{"a setup ended by a signal", PhaseSetup, as{state: fin, exit: exit(-1), signal: "killed", words: "deps exited -1"}, failed},
		{"a setup that timed out", PhaseSetup, as{state: fin, exit: exit(-1), timedOut: true, words: "deps timed out after 10m0s"}, failed},
		{"a setup that could not start", PhaseSetup, as{state: fin, exit: exit(-1), words: "deps could not run: no such file"}, failed},
		{"a checkout's setup that changed tracked files", PhaseSetup, as{state: fin, exit: exit(0), words: "setup deps changed tracked files: go.sum"}, failed},
		{"a setup whose running row the ledger refused", PhaseSetup, as{state: fin, exit: exit(-1), signal: "killed", words: "the ledger could not record the step deps: the disk is full"}, failed},
		{"a service its tree's end stopped", PhaseService, as{state: fin, exit: exit(-1), signal: "terminated"}, passed},
		{"a service the server's shutdown cut", PhaseService, as{state: fin, exit: exit(-1), signal: "killed", killed: true}, passed},
		{"a service that exited before the tree was ready", PhaseService, as{state: fin, exit: exit(1), words: "db exited 1 before the tree was ready"}, failed},
		{"a service that exited 1 while the tree was in use", PhaseService, as{state: fin, exit: exit(1), words: "db exited 1 while the tree was in use"}, failed},
		{"a service that exited 0 while the tree was in use", PhaseService, as{state: fin, exit: exit(0), words: "db exited 0 while the tree was in use"}, failed},
		{"a service that could not start", PhaseService, as{state: fin, exit: exit(-1), words: "db could not run: no such file"}, failed},
		{"a URL probe that passed (stable unset)", PhaseReady, as{state: fin, exit: exit(0)}, passed},
		{"a command probe that passed (stable unset)", PhaseReady, as{state: fin, exit: exit(0)}, passed},
		{"a command probe that passed, a leftover killed with its group", PhaseReady, as{state: fin, exit: exit(0), killed: true}, passed},
		{"a URL probe that did not pass", PhaseReady, as{state: fin, exit: exit(-1), words: "up did not pass within 30s (5 tries): answered 503 Service Unavailable"}, failed},
		{"a command probe that exited 1", PhaseReady, as{state: fin, exit: exit(1), words: "up did not pass within 30s (5 tries): exited 1"}, failed},
		{"a command probe that timed out", PhaseReady, as{state: fin, exit: exit(-1), timedOut: true, words: "up did not pass within 30s (1 try): its last try was still running at the timeout"}, failed},
		{"a command probe that could not start", PhaseReady, as{state: fin, exit: exit(-1), words: "up did not pass within 30s (5 tries): could not run: no such file"}, failed},
		{"a probe cut by a service's exit", PhaseReady, as{state: fin, exit: exit(-1), words: "db exited 1 before the tree was ready"}, failed},
	}
	// The rows of every phase alike: not ended, interrupted by the engine,
	// recovered after a crash (exit -1 as the recovery leaves it, or none
	// given), and never run with no row.
	for _, phase := range []string{PhaseCheck, PhaseVerify, PhaseSetup, PhaseService, PhaseReady} {
		cases = append(cases,
			tcase{"a " + phase + " receipt started", phase, as{state: ReceiptStarted}, unended},
			tcase{"a " + phase + " receipt running", phase, as{state: ReceiptRunning}, unended},
			tcase{"a " + phase + " receipt the run's stop interrupted", phase, as{state: ReceiptInterrupted, exit: exit(-1), signal: "terminated", words: "up was interrupted: the run was stopped"}, failed},
			tcase{"a " + phase + " receipt the attempt's close interrupted, no exit given", phase, as{state: ReceiptInterrupted, words: "the attempt closed under the check"}, failed},
			tcase{"a " + phase + " receipt recovered after a crash", phase, as{state: ReceiptInterrupted, exit: exit(-1)}, failed},
			tcase{"a " + phase + " receipt recovered after a crash, its stable kept", phase, as{state: ReceiptInterrupted, exit: exit(-1), stable: true}, failed},
			tcase{"a " + phase + " receipt recovered after a crash, no exit given", phase, as{state: ReceiptInterrupted}, failed},
			tcase{"a " + phase + " step never run, its start not recorded", phase, as{}, noRow},
		)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := checkNote(candidate)
			f.Check.Receipt = fmt.Sprintf("%s/build/2/%s/up/%d", run, tc.phase, i)
			r := request(candidate, f)
			ev := evidenceOf(r)
			c := ev.Findings[0].Check
			c.State, c.Exit, c.Signal, c.TimedOut, c.Killed, c.Stable, c.Error = tc.receipt.state, tc.receipt.exit, tc.receipt.signal, tc.receipt.timedOut,
				tc.receipt.killed, tc.receipt.stable, tc.receipt.words
			if tc.judged == noRow {
				ev.Findings[0].Check = nil
			}
			rc, v, err := s.SubmitReview(ctx, testOp(opID("judged", i)), key, r, ev)
			switch tc.judged {
			case noRow:
				if err == nil || !strings.Contains(err.Error(), "has no receipt") {
					t.Fatalf("a finding with no receipt: %+v %v", rc, err)
				}
				return
			case unended, passed:
				want(t, rc, err, StatusFailed, RefusalInvalid)
				words := "has not finished"
				if tc.judged == passed {
					words = "passed: nothing failed to repair"
				}
				if !strings.Contains(rc.Error, words) {
					t.Fatalf("the words: %q, not %q", rc.Error, words)
				}
				return
			}
			want(t, rc, err, StatusDone, "")
			got := v.Findings[0].Check
			if got == nil || got.Phase != tc.phase || got.State != tc.receipt.state || got.Error != tc.receipt.words || got.Stable != tc.receipt.stable ||
				got.Killed != tc.receipt.killed || (got.Exit == nil) != (tc.receipt.exit == nil) || (got.Exit != nil && *got.Exit != *tc.receipt.exit) {
				t.Fatalf("the receipt kept: %+v", got)
			}
		})
	}
}

// A receipt's words of its failure are kept in UTF-8 within MaxCheckError
// bytes: words the engine cut inside a character end at the character
// before it, and words past the bound are cut at a character's start.
func TestACheckFindingKeepsItsReceiptsWordsInBound(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	for i, tc := range []struct{ words, want string }{
		{strings.Repeat("x", MaxCheckError-1) + "\xe2", strings.Repeat("x", MaxCheckError-1)},
		{strings.Repeat("é", MaxCheckError), strings.Repeat("é", MaxCheckError/2)},
		{strings.Repeat("x", MaxCheckError), strings.Repeat("x", MaxCheckError)},
	} {
		r := request(candidate, checkNote(candidate))
		ev := evidenceOf(r)
		ev.Findings[0].Check.Error = tc.words
		rc, v, err := s.SubmitReview(ctx, testOp(opID("words", i)), key, r, ev)
		want(t, rc, err, StatusDone, "")
		if got := v.Findings[0].Check.Error; got != tc.want || !utf8.ValidString(got) || len(got) > MaxCheckError {
			t.Fatalf("case %d: the words kept are %d bytes, UTF-8 %v, not the %d wanted", i, len(got), utf8.ValidString(got), len(tc.want))
		}
	}
}

// A receipt the ledger may hold is kept, made to fit: an argv of 32
// arguments and 8 KiB, every byte a control character (some 48 KiB as
// JSON), cut from its end to MaxCheckRef and marked cut; a tail cut by
// the ledger inside a character kept from the next one; a tail of bytes
// that are no UTF-8 kept as its last 2 KiB with U+FFFD for them.
func TestACheckFindingKeepsTheLedgersWorstReceipt(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	argv := append([]string{"sh"}, slices.Repeat([]string{strings.Repeat("\x01", 250)}, MaxCheckArgv-1)...)
	for i, tc := range []struct{ tail, want string }{
		{"\xa6" + strings.Repeat("x", MaxCheckExcerpt-1), strings.Repeat("x", MaxCheckExcerpt-1)},
		{strings.Repeat("\xffy", MaxCheckExcerpt/2), strings.Repeat("\uFFFDy", MaxCheckExcerpt/4)},
	} {
		r := request(candidate, checkNote(candidate))
		r.Findings[0].Check.Quote = strings.Repeat(`"`, MaxCheckExcerpt)
		ev := evidenceOf(r)
		ev.Findings[0].Check.Argv, ev.Findings[0].Check.Tail = slices.Clone(argv), tc.tail
		rc, v, err := s.SubmitReview(ctx, testOp(opID("worst", i)), key, r, ev)
		want(t, rc, err, StatusDone, "")
		c := v.Findings[0].Check
		value, _ := encodeValue(*c)
		if len(value) > MaxCheckRef || !c.ArgvCut || len(c.Argv) == 0 || len(c.Argv) >= len(argv) {
			t.Fatalf("case %d: the receipt kept is %d bytes, cut %v, %d arguments", i, len(value), c.ArgvCut, len(c.Argv))
		}
		last := len(c.Argv) - 1
		if !slices.Equal(c.Argv[:last], argv[:last]) || !strings.HasPrefix(argv[last], c.Argv[last]) {
			t.Fatalf("case %d: the argv kept is no start of the argv: %q", i, c.Argv)
		}
		if c.Tail != tc.want {
			t.Fatalf("case %d: the tail kept is %d bytes (%q…), not %d", i, len(c.Tail), c.Tail[:min(len(c.Tail), 8)], len(tc.want))
		}
		if !utf8.ValidString(c.Tail) || len(c.Tail) > MaxCheckExcerpt {
			t.Fatalf("case %d: the tail kept is %d bytes, UTF-8 %v", i, len(c.Tail), utf8.ValidString(c.Tail))
		}
	}
}

// Each bound at its edge and one past, end to end: a review's findings,
// summary, words, resolution, path, lines and their bytes and numbers, a
// check's quote, a ticket's reviews and a finding's marks.
func TestReviewBounds(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	n := 0
	check := func(name, refusal string, r NewReview) {
		t.Helper()
		n++
		rc, _, err := submit(s, opID("b", n), key, r)
		if err != nil || rc.Status != map[bool]string{true: StatusDone, false: StatusFailed}[refusal == ""] || rc.Refusal != refusal {
			t.Fatalf("%s: %+v %v, want %q", name, rc, err, refusal)
		}
	}
	findings := func(k int) []NewFinding {
		out := make([]NewFinding, k)
		for i := range out {
			out[i] = lineChange(fmt.Sprintf("Fix %d.", i))
		}
		return out
	}
	check("50 findings", "", request(candidate, findings(MaxReviewFindings)...))
	check("51 findings", RefusalBound, request(candidate, findings(MaxReviewFindings+1)...))
	summary := request(candidate, lineChange("x"))
	summary.Summary = strings.Repeat("é", MaxReviewSummary)
	check("a summary of 4,000 characters", "", summary)
	summary.Summary += "é"
	check("a summary of 4,001", RefusalBound, summary)
	for _, tc := range []struct {
		name  string
		edit  func(f *NewFinding, past bool)
		bound string
	}{
		{"words of 1,000 characters", func(f *NewFinding, p bool) { f.Message = strings.Repeat("é", MaxFindingMessage+b2i(p)) }, RefusalBound},
		{"a resolution of 1,000 characters", func(f *NewFinding, p bool) { f.Resolution = strings.Repeat("é", MaxFindingResolution+b2i(p)) }, RefusalBound},
		{"a path of 1,024 bytes", func(f *NewFinding, p bool) { f.Path = strings.Repeat("a", MaxFindingPath+b2i(p)) }, RefusalBound},
		{"12 lines", func(f *NewFinding, p bool) { f.Lines = slices.Repeat([]string{"x"}, MaxAnchorLines+b2i(p)) }, RefusalBound},
		{"a line of 200 bytes", func(f *NewFinding, p bool) { f.Lines = []string{strings.Repeat("a", MaxAnchorLine+b2i(p))} }, RefusalBound},
		{"the last line number", func(f *NewFinding, p bool) { f.From, f.To = 1, MaxFindingLine+b2i(p) }, RefusalInvalid},
		{"the first line number", func(f *NewFinding, p bool) { f.From, f.To = 1-b2i(p), 1 }, RefusalInvalid},
		{"a range of one line", func(f *NewFinding, p bool) { f.From, f.To = 9, 9-b2i(p) }, RefusalInvalid},
	} {
		for _, p := range []bool{false, true} {
			f := lineChange("Fix it.")
			tc.edit(&f, p)
			refusal := ""
			if p {
				refusal = tc.bound
			}
			check(fmt.Sprintf("%s (past %v)", tc.name, p), refusal, request(candidate, f))
		}
	}
	for _, p := range []bool{false, true} {
		f := checkNote(candidate)
		f.Check.Quote = strings.Repeat("a", MaxCheckExcerpt+b2i(p))
		check(fmt.Sprintf("a quote of 2 KiB (past %v)", p), map[bool]string{false: "", true: RefusalBound}[p], request(candidate, f))
	}
	// A check finding's receipt as the server fills it: an argv of 32
	// arguments (33 is no ledger's: an error), a receipt of MaxCheckRef
	// bytes kept whole and one a byte past it kept at MaxCheckRef with its
	// argv cut, a tail of 2 KiB kept whole and one a byte past it kept as
	// its last 2 KiB.
	withRef := func(fill func(*CheckRef)) (Receipt, *CheckRef, error) {
		t.Helper()
		n++
		r := request(candidate, checkNote(candidate))
		ev := evidenceOf(r)
		ev.Findings[0].Check.Chosen, ev.Findings[0].Check.Quote = r.Findings[0].Check.Chosen, r.Findings[0].Check.Quote
		fill(ev.Findings[0].Check)
		rc, v, err := s.SubmitReview(ctx, testOp(opID("b", n)), key, r, ev)
		if err != nil || rc.Status != StatusDone {
			return rc, nil, err
		}
		return rc, v.Findings[0].Check, nil
	}
	for _, p := range []bool{false, true} {
		rc, c, err := withRef(func(c *CheckRef) { c.Argv = slices.Repeat([]string{"x"}, MaxCheckArgv+b2i(p)) })
		switch {
		case !p && (err != nil || len(c.Argv) != MaxCheckArgv):
			t.Fatalf("an argv of 32: %+v %v", rc, err)
		case p && (err == nil || !strings.Contains(err.Error(), "evidence")):
			t.Fatalf("an argv of 33: %+v %v", rc, err)
		}
		var whole string
		rc, c, err = withRef(func(c *CheckRef) {
			c.Argv = []string{"go", ""}
			v, _ := encodeValue(*c)
			c.Argv[1] = strings.Repeat("a", MaxCheckRef-len(v)+b2i(p))
			whole = c.Argv[1]
		})
		if err != nil || c == nil {
			t.Fatalf("a receipt of MaxCheckRef (past %v): %+v %v", p, rc, err)
		}
		v, _ := encodeValue(*c)
		if len(v) != MaxCheckRef || c.ArgvCut != p || len(c.Argv) != 2 || !strings.HasPrefix(whole, c.Argv[1]) || (c.Argv[1] == whole) == p {
			t.Fatalf("a receipt of MaxCheckRef (past %v) kept as %d bytes, cut %v, its argument %d bytes of %d", p, len(v), c.ArgvCut, len(c.Argv[1]), len(whole))
		}
		tail := strings.Repeat("é", MaxCheckExcerpt/2)
		rc, c, err = withRef(func(c *CheckRef) { c.Tail = strings.Repeat("x", b2i(p)) + tail })
		if err != nil || c == nil || c.Tail != tail {
			t.Fatalf("a tail of 2 KiB (past %v): %+v %v", p, rc, err)
		}
	}
	// A ticket's reviews: 500, the rest written as rows.
	tk := ticketOf(t, s, key)
	have := count(t, s, "SELECT COUNT(*) FROM reviews WHERE ticket_id = ?", tk.ID)
	for i := have; i < MaxReviews-1; i++ {
		if _, err := s.db.Exec(`INSERT INTO reviews (id, ticket_id, candidate, base, verdict, created_at, actor) VALUES (?, ?, ?, ?, 'comment', ?, 'test')`,
			newID(), tk.ID, candidate, commitBase, stamp(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	comment := NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "x"}
	check("the 500th review", "", comment)
	check("the 501st", RefusalBound, comment)
	// A finding's marks: 50, the rest written as rows.
	finding := firstFindingOf(t, s, key)
	later := candidateOf(run, 3, commitB[:40])
	for range MaxMarks - 1 {
		insertMark(t, s, finding, later, FindingOpen, stamp(time.Now()), "test", newID(), "", "")
	}
	rc, _, err := mark(s, "m50", finding, later, FindingResolved, "")
	want(t, rc, err, StatusDone, "")
	rc, _, err = mark(s, "m51", finding, later, FindingResolved, "")
	want(t, rc, err, StatusFailed, RefusalBound)
	// A mark's note.
	other := firstFindingOf(t, s, key, 1)
	for i, p := range []bool{false, true} {
		rc, _, err := s.MarkFinding(ctx, testOp(opID("note", i)), other, NewMark{Candidate: later, Verdict: FindingOpen, Note: strings.Repeat("é", MaxMarkNote+b2i(p))}, nil)
		want(t, rc, err, map[bool]string{false: StatusDone, true: StatusFailed}[p], map[bool]string{false: "", true: RefusalBound}[p])
	}
}

// b2i is 1 for true.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// firstFindingOf is the id of the ticket's oldest finding, or of the one
// skip places after it.
func firstFindingOf(t *testing.T, s *Store, key string, skip ...int) string {
	t.Helper()
	page, err := s.Findings(context.Background(), key, FindingFilter{}, "")
	if err != nil || len(page.Findings) == 0 {
		t.Fatalf("the findings of %s: %+v %v", key, page, err)
	}
	i := 0
	if len(skip) > 0 {
		i = skip[0]
	}
	return page.Findings[i].ID
}

// insertMark writes a mark's row as a test needs it, past finding.mark's
// rules (a time of the test's, many at once).
func insertMark(t *testing.T, s *Store, finding, candidate, verdict, at, actor, op, note, located string) string {
	t.Helper()
	var ticket string
	if err := s.db.QueryRow("SELECT ticket_id FROM review_findings WHERE id = ?", finding).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	id := newID()
	if _, err := s.db.Exec(`INSERT INTO finding_marks (id, finding_id, ticket_id, candidate, verdict, located, note, created_at, actor, op_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, finding, ticket, candidate, verdict, located, note, at, actor, op); err != nil {
		t.Fatal(err)
	}
	return id
}

// A review sent again under its id is its receipt's repeat, the review as
// it stands; once the receipt is pruned, the changes log and the review's
// fingerprint answer it, done with its id, and Replay answers it the same
// before anything is read. A mark the same.
func TestReviewReplayAnswersTheSameReceipt(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	r := request(candidate, lineChange("Fix it."))
	first := mustSubmit(t, s, "rv", key, r)
	rc, again, err := submit(s, "rv", key, r)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat || rc.ID != first.ID || again.ID != first.ID || count(t, s, "SELECT COUNT(*) FROM reviews") != 1 {
		t.Fatalf("the resend: %+v %+v", rc, again.ReviewSummary)
	}
	later := candidateOf(run, 3, commitB[:40])
	mrc, m, err := mark(s, "mk", first.Findings[0].ID, later, FindingResolved, `{"state":"same"}`)
	want(t, mrc, err, StatusDone, "")
	for _, pruned := range []bool{false, true} {
		if pruned {
			if _, err := s.db.Exec("DELETE FROM ops WHERE id IN ('rv', 'mk')"); err != nil {
				t.Fatal(err)
			}
		}
		rc, found := replayed(t, s, testOp("rv"), IntentReviewSubmit, ReviewSubmitArgs{Key: key, Review: r})
		if !found || rc.Status != StatusDone || rc.ID != first.ID {
			t.Fatalf("the replay (pruned %v): %+v %v", pruned, rc, found)
		}
		rc, again, err := submit(s, "rv", key, r)
		want(t, rc, err, StatusDone, "")
		if !rc.Repeat || rc.ID != first.ID || again.ID != first.ID {
			t.Fatalf("the resend (pruned %v): %+v", pruned, rc)
		}
		rc, found = replayed(t, s, testOp("mk"), IntentFindingMark, FindingMarkArgs{ID: first.Findings[0].ID, Mark: NewMark{Candidate: later, Verdict: FindingResolved, Via: ViaWeb}})
		if !found || rc.Status != StatusDone || rc.ID != m.ID {
			t.Fatalf("the mark's replay (pruned %v): %+v %v", pruned, rc, found)
		}
		rc, m2, err := mark(s, "mk", first.Findings[0].ID, later, FindingResolved, `{"state":"same"}`)
		want(t, rc, err, StatusDone, "")
		if !rc.Repeat || rc.ID != m.ID || m2.ID != m.ID {
			t.Fatalf("the mark's resend (pruned %v): %+v %+v", pruned, rc, m2)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM finding_marks"); n != 1 {
		t.Fatalf("%d marks", n)
	}
	if _, err := s.Review(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
}

// The fingerprint is the request, never the evidence: a review sent again
// with another class and a check whose log was pruned since, and a mark
// sent again with another located, are the same request, answered by the
// same receipt, before and after its pruning; another finding's words
// under the same op id are op_conflict.
func TestReplayIgnoresTheEvidence(t *testing.T) {
	s, key, run := reviewTicket(t)
	candidate := candidateOf(run, 2, commitA)
	r := request(candidate, lineChange("Fix it."), checkNote(candidate))
	ev := evidenceOf(r)
	rc, first, err := s.SubmitReview(context.Background(), testOp("rv"), key, r, ev)
	want(t, rc, err, StatusDone, "")
	later := candidateOf(run, 3, commitB[:40])
	// Located past its bound or no JSON is the caller's error, not a
	// refusal: no receipt kept, and the same op id then marks.
	for _, bad := range []string{`{"why":"` + strings.Repeat("x", MaxMarkLocated) + `"}`, `{"state":`} {
		rc, _, err := mark(s, "mk", first.Findings[0].ID, later, FindingOpen, bad)
		if err == nil || !strings.Contains(err.Error(), "evidence") {
			t.Fatalf("located of %d bytes: %+v %v", len(bad), rc, err)
		}
		if _, found, _ := s.Receipt(context.Background(), "test", "mk"); found {
			t.Fatalf("located of %d bytes kept a receipt", len(bad))
		}
	}
	mrc, m, err := mark(s, "mk", first.Findings[0].ID, later, FindingOpen, `{"state":"moved","from":20,"to":22}`)
	want(t, mrc, err, StatusDone, "")
	changed := evidenceOf(r)
	changed.Findings[1].Check.Class, changed.Findings[1].Check.LogSHA256, changed.Findings[1].Check.Tail = "flaky", "", ""
	other := request(candidate, lineChange("Fix it differently."), checkNote(candidate))
	for _, pruned := range []bool{false, true} {
		if pruned {
			if _, err := s.db.Exec("DELETE FROM ops WHERE id IN ('rv', 'mk')"); err != nil {
				t.Fatal(err)
			}
		}
		rc, again, err := s.SubmitReview(context.Background(), testOp("rv"), key, r, changed)
		want(t, rc, err, StatusDone, "")
		if !rc.Repeat || rc.ID != first.ID || again.Findings[1].Check.Class != "code" {
			t.Fatalf("other evidence (pruned %v): %+v, the class kept %q", pruned, rc, again.Findings[1].Check.Class)
		}
		rc, _, err = s.SubmitReview(context.Background(), testOp("rv"), key, other, evidenceOf(other))
		want(t, rc, err, StatusFailed, RefusalOpConflict)
		rc, m2, err := mark(s, "mk", first.Findings[0].ID, later, FindingOpen, `{"state":"same"}`)
		want(t, rc, err, StatusDone, "")
		if !rc.Repeat || m2.ID != m.ID || string(m2.Located) != `{"state":"moved","from":20,"to":22}` {
			t.Fatalf("another located (pruned %v): %+v %s", pruned, rc, m2.Located)
		}
		rc, _, err = mark(s, "mk", first.Findings[0].ID, later, FindingWithdrawn, "")
		want(t, rc, err, StatusFailed, RefusalOpConflict)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM reviews"); n != 1 {
		t.Fatalf("%d reviews", n)
	}
}

// An outcome settles only its own try: one not yet begun is refused with
// no receipt kept, so it may be recorded later; a second record of one
// outcome is the first's repeat; the other outcome of a settled try, and a
// late answer of an earlier try, are refused and change nothing.
func TestApplicationSettlesOnlyItsTry(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	v := mustSubmit(t, s, "rv", key, request(candidate, lineChange("Fix it.")))
	op := testOp("ignored")
	rc, err := s.RefusedReview(ctx, op, v.ID, 2, "too soon")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, found, _ := s.Receipt(ctx, ActorServer, v.ID+".2.refused"); found {
		t.Fatal("a try not begun kept a receipt")
	}
	// An outcome of fields not their shapes: answered, kept nowhere.
	rc, err = s.AppliedReview(ctx, op, v.ID, 1, Applied{Run: "not a run", Command: "rv." + v.ID + ".1", Revision: 4, Attempt: 3})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	applied := Applied{Run: run, Command: "rv." + v.ID + ".1", Revision: 4, Attempt: 3}
	rc, err = s.AppliedReview(ctx, op, v.ID, 1, applied)
	want(t, rc, err, StatusDone, "")
	if rc.OpID != v.ID+".1.applied" {
		t.Fatalf("the outcome's op id: %q", rc.OpID)
	}
	got := reviewOf(t, s, v.ID).Application
	if got.State != ApplicationApplied || got.Try != 1 || got.Run != run || got.Command != applied.Command || got.Revision != 4 || got.Attempt != 3 || got.At.IsZero() {
		t.Fatalf("applied: %+v", got)
	}
	rc, err = s.AppliedReview(ctx, op, v.ID, 1, applied)
	want(t, rc, err, StatusDone, "")
	if !rc.Repeat {
		t.Fatalf("a second record: %+v", rc)
	}
	rc, err = s.RefusedReview(ctx, op, v.ID, 1, "late")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if a := reviewOf(t, s, v.ID).Application; a.State != ApplicationApplied {
		t.Fatalf("a refusal after the application: %+v", a)
	}
	// A request refused, sent again: try 1's late answer changes nothing.
	w := mustSubmit(t, s, "rw", key, request(candidate, lineChange("Fix it.")))
	rc, err = s.RefusedReview(ctx, op, w.ID, 1, "stale_candidate: the task's result is now attempt 4")
	want(t, rc, err, StatusDone, "")
	rc, _, err = s.ApplyingReview(ctx, testOp("again"), w.ID)
	want(t, rc, err, StatusDone, "")
	rc, err = s.AppliedReview(ctx, op, w.ID, 1, applied)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if a := reviewOf(t, s, w.ID).Application; a.State != ApplicationPending || a.Try != 2 || a.Run != "" || a.Why != "" {
		t.Fatalf("try 1's late answer: %+v", a)
	}
	rc, err = s.AppliedReview(ctx, op, w.ID, 2, applied)
	want(t, rc, err, StatusDone, "")
	// A comment is applied to nothing; a review of no id, nothing.
	c := mustSubmit(t, s, "rc", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "x"})
	rc, err = s.AppliedReview(ctx, op, c.ID, 1, applied)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, err := s.AppliedReview(ctx, op, "nope", 1, applied); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such id: %v", err)
	}
	if _, err := s.RefusedReview(ctx, op, w.ID, MaxReviewTries+1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a try past the bound: %v", err)
	}
	// Why is cut and made text.
	x := mustSubmit(t, s, "rx", key, request(candidate, lineChange("Fix it.")))
	rc, err = s.RefusedReview(ctx, op, x.ID, 1, "a\x00b"+strings.Repeat("é", MaxReviewWhy))
	want(t, rc, err, StatusDone, "")
	if why := reviewOf(t, s, x.ID).Application.Why; !strings.HasPrefix(why, "a b") || len([]rune(why)) != MaxReviewWhy || !strings.HasSuffix(why, "…") {
		t.Fatalf("why: %q", why)
	}
}

// Request again makes a refused request pending at its next try, five at
// most; it refuses a request pending or applied, a comment, a closed
// ticket's request.
func TestRetryIsTheNextTryAndStopsAtFive(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	v := mustSubmit(t, s, "rv", key, request(candidate, lineChange("Fix it.")))
	rc, _, err := s.ApplyingReview(ctx, testOp("early"), v.ID)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	for try := 1; try <= MaxReviewTries; try++ {
		rc, err := s.RefusedReview(ctx, testOp("x"), v.ID, try, "no")
		want(t, rc, err, StatusDone, "")
		rc, got, err := s.ApplyingReview(ctx, testOp(opID("retry", try)), v.ID)
		if try == MaxReviewTries {
			want(t, rc, err, StatusFailed, RefusalBound)
			break
		}
		want(t, rc, err, StatusDone, "")
		if a := got.Application; a.State != ApplicationPending || a.Try != try+1 || a.Why != "" || !a.At.IsZero() {
			t.Fatalf("try %d: %+v", try+1, a)
		}
	}
	if a := reviewOf(t, s, v.ID).Application; a.State != ApplicationRefused || a.Try != MaxReviewTries {
		t.Fatalf("after five: %+v", a)
	}
	// An expected revision is the review's.
	w := mustSubmit(t, s, "rw", key, request(candidate, lineChange("Fix it.")))
	rc, err = s.RefusedReview(ctx, testOp("x"), w.ID, 1, "no")
	want(t, rc, err, StatusDone, "")
	stale := testOp("stale")
	stale.ExpectedRev = 1
	rc, _, err = s.ApplyingReview(ctx, stale, w.ID)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	// Applied is final.
	rc, err = s.AppliedReview(ctx, testOp("x"), mustSubmit(t, s, "ra", key, request(candidate, lineChange("Fix it."))).ID, 1,
		Applied{Run: run, Command: "c1", Revision: 1, Attempt: 3})
	want(t, rc, err, StatusDone, "")
	a := reviewsOf(t, s, key)[0]
	rc, _, err = s.ApplyingReview(ctx, testOp("final"), a)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	c := mustSubmit(t, s, "rc", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictApprove, Summary: "Good."})
	rc, _, err = s.ApplyingReview(ctx, testOp("approval"), c.ID)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	// A closed ticket's request waits for the ticket to reopen.
	mustEdit(t, s, "cancel", key, TicketEdit{Set: TicketSet{Status: ptr(TicketCancelled)}})
	rc, _, err = s.ApplyingReview(ctx, testOp("closed"), w.ID)
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if _, _, err := s.ApplyingReview(ctx, testOp("x"), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such id: %v", err)
	}
}

// Request again holds the candidate to its ticket as a review and a mark
// are, in its own transaction: a refused request whose run's chain became
// contested since (another ticket links a run the chain now holds) is
// refused run_linked, naming the holders, its try unchanged; the chain the
// ticket's alone again, it is sent again.
func TestRetryNeedsTheTicketsRunStill(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("Fix it.")))
	rc, err := s.RefusedReview(ctx, testOp("x"), v.ID, 1, "no")
	want(t, rc, err, StatusDone, "")
	_, other := makeTicket(t, s, NewTicket{Project: "CND", Title: "Another"})
	x := "legacy-000000d1"
	s.SetRunExists(runs(run, x))
	rc, err = addLink(s, "lx", other, NewLink{Kind: LinkRun, Ref: x})
	want(t, rc, err, StatusDone, "")
	s.SetRunChain(chains([]string{run, x}))
	rc, _, err = s.ApplyingReview(ctx, testOp("contested"), v.ID)
	want(t, rc, err, StatusFailed, RefusalRunLinked)
	if !strings.Contains(rc.Error, "contested") {
		t.Fatalf("the refusal's words: %+v", rc)
	}
	if a := reviewOf(t, s, v.ID).Application; a.State != ApplicationRefused || a.Try != 1 {
		t.Fatalf("after a refused retry: %+v", a)
	}
	links, err := s.Links(ctx, other)
	if err != nil || len(links) != 1 {
		t.Fatalf("the other ticket's links: %+v %v", links, err)
	}
	rc, err = s.RemoveLink(ctx, testOp("unlink"), other, links[0].ID)
	want(t, rc, err, StatusDone, "")
	rc, got, err := s.ApplyingReview(ctx, testOp("again"), v.ID)
	want(t, rc, err, StatusDone, "")
	if a := got.Application; a.State != ApplicationPending || a.Try != 2 {
		t.Fatalf("Request again of the ticket's own chain: %+v", a)
	}
}

// reviewsOf is the ids of the ticket's reviews, newest first.
func reviewsOf(t *testing.T, s *Store, key string) []string {
	t.Helper()
	page, err := s.Reviews(context.Background(), key, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range page.Reviews {
		out = append(out, r.ID)
	}
	return out
}

// A finding's marks are a log: each mark a row of its own and an add row
// of the finding's marks naming it whole, none edited by a later one, read
// oldest first.
func TestMarksAreALog(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("Fix it.")))
	f := v.Findings[0].ID
	var ids []string
	for i, verdict := range []string{FindingResolved, FindingOpen, FindingWithdrawn} {
		rc, m, err := mark(s, opID("m", i), f, candidateOf(run, 3+i, commitB[:40]), verdict, fmt.Sprintf(`{"n":%d}`, i))
		want(t, rc, err, StatusDone, "")
		if m.ID != rc.ID || m.Verdict != verdict || m.Actor != "test" || m.CreatedBy != "Tester" || m.Via != ViaWeb || m.FindingID != f {
			t.Fatalf("mark %d: %+v", i, m)
		}
		ids = append(ids, m.ID)
		rows := changesOf(t, s, "test", opID("m", i))
		var added markAdded
		if len(rows) != 1 || rows[0].Entity != EntityFinding || rows[0].EntityID != f || rows[0].Field != "marks" || rows[0].Op != OpAdd ||
			json.Unmarshal([]byte(rows[0].Value), &added) != nil || added.ID != m.ID || added.Fingerprint == "" {
			t.Fatalf("mark %d's rows: %+v", i, rows)
		}
	}
	page, err := s.Marks(ctx, f, "")
	if err != nil || len(page.Marks) != 3 || page.Next != "" {
		t.Fatalf("the marks: %+v %v", page, err)
	}
	for i, m := range page.Marks {
		if m.ID != ids[i] || string(m.Located) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Fatalf("mark %d read: %+v", i, m)
		}
	}
	if _, err := s.Marks(ctx, "0123456789abcdef", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such finding: %v", err)
	}
	if _, err := s.Marks(ctx, f, "0123456789abcdef"); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("a cursor of none: %v", err)
	}
	rc, _, err := mark(s, "nope", "0123456789abcdef", candidateOf(run, 3, commitB[:40]), FindingOpen, "")
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

// A finding's state is its latest mark's verdict in the log's one order
// (at, actor, op id), open with none: two marks at one time are both
// kept, the later in that order deciding; a review counts its findings by
// it and a page of findings is narrowed by it.
func TestFindingStateIsTheLatestMark(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("One."), lineChange("Two."), lineChange("Three.")))
	stateOf := func(id string) string {
		t.Helper()
		for _, f := range reviewOf(t, s, v.ID).Findings {
			if f.ID == id {
				return f.State
			}
		}
		t.Fatalf("no finding %s", id)
		return ""
	}
	one, two := v.Findings[0].ID, v.Findings[1].ID
	later := candidateOf(run, 3, commitB[:40])
	if stateOf(one) != FindingOpen {
		t.Fatal("a finding with no mark is not open")
	}
	rc, _, err := mark(s, "m1", one, later, FindingResolved, "")
	want(t, rc, err, StatusDone, "")
	if stateOf(one) != FindingResolved {
		t.Fatal("resolved")
	}
	rc, _, err = mark(s, "m2", one, later, FindingOpen, "")
	want(t, rc, err, StatusDone, "")
	if stateOf(one) != FindingOpen {
		t.Fatal("open again")
	}
	// Two marks at one time, by two actors: the greater actor decides,
	// whichever was written first.
	at := stamp(time.Now().Add(time.Hour))
	insertMark(t, s, two, later, FindingResolved, at, "zed", "a", "", "")
	insertMark(t, s, two, later, FindingWithdrawn, at, "amy", "z", "", "")
	if got := stateOf(two); got != FindingResolved {
		t.Fatalf("two marks at one time: %s", got)
	}
	// One actor, one time: the greater op id.
	insertMark(t, s, two, later, FindingWithdrawn, at, "zed", "b", "", "")
	if got := stateOf(two); got != FindingWithdrawn {
		t.Fatalf("two marks at one time by one actor: %s", got)
	}
	got := reviewOf(t, s, v.ID)
	if got.Counts != (FindingCounts{Open: 2, Withdrawn: 1}) || got.Findings[1].Marks != 3 || got.Findings[1].Latest == nil ||
		got.Findings[1].Latest.OpID != "b" || got.Findings[1].Latest.Located != nil {
		t.Fatalf("the review read: %+v %+v", got.Counts, got.Findings[1])
	}
	for state, n := range map[string]int{FindingOpen: 2, FindingWithdrawn: 1, FindingResolved: 0, "": 3} {
		page, err := s.Findings(ctx, key, FindingFilter{State: state}, "")
		if err != nil || len(page.Findings) != n {
			t.Fatalf("the findings %q: %d %v", state, len(page.Findings), err)
		}
	}
	if page, err := s.Findings(ctx, key, FindingFilter{Task: "build"}, ""); err != nil || len(page.Findings) != 3 {
		t.Fatalf("the findings of build: %+v %v", page, err)
	}
	if page, err := s.Findings(ctx, key, FindingFilter{Task: "buil"}, ""); err != nil || len(page.Findings) != 0 {
		t.Fatalf("the findings of buil: %+v %v", page, err)
	}
	if _, err := s.Findings(ctx, key, FindingFilter{State: "done"}, ""); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("a state of none: %v", err)
	}
}

// A resolved mark names a candidate that changed the code: one of the
// finding's own commit is refused whatever its attempt (a check made
// again, a repair that committed nothing); open and withdrawn take it; a
// candidate of another task is refused.
func TestResolvedNeedsALaterCandidate(t *testing.T) {
	s, key, run := reviewTicket(t)
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("Fix it.")))
	f := v.Findings[0].ID
	for i, c := range []string{candidateOf(run, 2, commitA), candidateOf(run, 3, commitA), candidateOf(run, 7, commitA)} {
		rc, _, err := mark(s, opID("same", i), f, c, FindingResolved, "")
		want(t, rc, err, StatusFailed, RefusalInvalid)
		if !strings.Contains(rc.Error, "changed the code") {
			t.Fatalf("the words: %q", rc.Error)
		}
	}
	rc, _, err := mark(s, "withdrawn", f, candidateOf(run, 3, commitA), FindingWithdrawn, "")
	want(t, rc, err, StatusDone, "")
	rc, _, err = mark(s, "task", f, run+"/docs/3@"+commitB[:40], FindingResolved, "")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	rc, _, err = mark(s, "later", f, candidateOf(run, 3, commitB[:40]), FindingResolved, "")
	want(t, rc, err, StatusDone, "")
	rc, _, err = mark(s, "loose", f, "loose-0000cafe/build/3@"+commitB[:40], FindingResolved, "")
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// A page of a finding's marks ends at MarksPerPage, or earlier when the
// next mark would take it past MaxMarksReply as its marks encode, escapes
// counted, its first mark whatever its size; walked by its cursor, every
// mark is read once, in the one order. A page of a ticket's findings the
// same, at FindingsPerPage and MaxFindingsReply with each finding's
// reserve.
func TestMarksPageEndsAtItsBound(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("Fix it.")))
	f := v.Findings[0].ID
	later := candidateOf(run, 3, commitB[:40])
	base := time.Now()
	// Small marks: pages of MarksPerPage.
	var want []string
	for i := range MarksPerPage + 5 {
		want = append(want, insertMark(t, s, f, later, FindingOpen, stamp(base.Add(time.Duration(i)*time.Second)), "test", opID("o", i), "", ""))
	}
	walk := func() []int {
		t.Helper()
		var got []string
		var sizes []int
		after := ""
		for range 100 {
			page, err := s.Marks(ctx, f, after)
			if err != nil || len(page.Marks) == 0 {
				t.Fatalf("a page after %q: %+v %v", after, page, err)
			}
			b, _ := json.Marshal(page)
			if len(b) > MaxMarksReply || len(page.Marks) > MarksPerPage {
				t.Fatalf("a page of %d marks is %d bytes", len(page.Marks), len(b))
			}
			for _, m := range page.Marks {
				got = append(got, m.ID)
			}
			sizes = append(sizes, len(page.Marks))
			if page.Next == "" {
				break
			}
			after = page.Next
		}
		if !slices.Equal(got, want) {
			t.Fatalf("the walk read %d marks, not the %d in order", len(got), len(want))
		}
		return sizes
	}
	if sizes := walk(); !slices.Equal(sizes, []int{MarksPerPage, 5}) {
		t.Fatalf("pages of small marks: %v", sizes)
	}
	// Marks at their bounds, every character six bytes escaped: fewer a
	// page, and still every mark.
	located := `{"why":"` + strings.Repeat("<", MaxMarkLocated-len(`{"why":""}`)) + `"}`
	for i := range 30 {
		want = append(want, insertMark(t, s, f, later, FindingOpen, stamp(base.Add(time.Duration(100+i)*time.Second)), "test", opID("big", i),
			strings.Repeat("<", MaxMarkNote), located))
	}
	if sizes := walk(); len(sizes) < 3 || sizes[1] >= MarksPerPage {
		t.Fatalf("pages of large marks: %v", sizes)
	}
	// Findings at their bounds: pages short of FindingsPerPage, every
	// finding read once.
	big := lineChange(strings.Repeat("<", MaxFindingMessage))
	big.Resolution = strings.Repeat("<", MaxFindingResolution)
	big.Lines = slices.Repeat([]string{strings.Repeat("<", MaxAnchorLine)}, MaxAnchorLines)
	var findings []string
	for _, f := range v.Findings {
		findings = append(findings, f.ID)
	}
	for i := range 3 {
		r := request(candidateOf(run, 2, commitA), slices.Repeat([]NewFinding{big}, 10)...)
		for _, f := range mustSubmit(t, s, opID("big", i), key, r).Findings {
			findings = append(findings, f.ID)
		}
	}
	var got []string
	var sizes []int
	after := ""
	for range 100 {
		page, err := s.Findings(ctx, key, FindingFilter{}, after)
		if err != nil || len(page.Findings) == 0 {
			t.Fatalf("a page after %q: %+v %v", after, page, err)
		}
		b, _ := json.Marshal(page)
		if len(b)+len(page.Findings)*FindingReplyReserve > MaxFindingsReply {
			t.Fatalf("a page of %d findings is %d bytes beside their reserve", len(page.Findings), len(b))
		}
		for _, f := range page.Findings {
			got = append(got, f.ID)
		}
		sizes = append(sizes, len(page.Findings))
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !slices.Equal(got, findings) || len(sizes) < 2 {
		t.Fatalf("the findings' walk: %d of %d, pages %v", len(got), len(findings), sizes)
	}
}

// MaxReviewDetail is measured at the submit on the review as its read
// encodes it, escapes counted: the same findings in characters that
// encode as one byte fit, in characters that encode as six do not; and a
// review a little under the bound is kept, a little over refused.
func TestReviewDetailBoundAtSubmit(t *testing.T) {
	s, key, run := reviewTicket(t)
	candidate := candidateOf(run, 2, commitA)
	findings := func(c string) []NewFinding {
		f := lineChange(strings.Repeat(c, MaxFindingMessage))
		f.Resolution = strings.Repeat(c, MaxFindingResolution)
		f.Lines = slices.Repeat([]string{strings.Repeat(c, MaxAnchorLine)}, MaxAnchorLines)
		return slices.Repeat([]NewFinding{f}, MaxReviewFindings)
	}
	plain := mustSubmit(t, s, "plain", key, request(candidate, findings("a")...))
	b, _ := json.Marshal(plain)
	if len(b) > MaxReviewDetail {
		t.Fatalf("the plain review is %d bytes", len(b))
	}
	rc, _, err := submit(s, "escaped", key, request(candidate, findings("<")...))
	want(t, rc, err, StatusFailed, RefusalBound)
	if n := count(t, s, "SELECT COUNT(*) FROM review_findings"); n != MaxReviewFindings {
		t.Fatalf("%d findings kept", n)
	}
	// Near the edge: a probe of findings in six-byte characters and a few
	// in one-byte ones, grown until the bound is within what a summary
	// can fill; then a summary that takes the same findings within a
	// margin under the bound, and one past it. The margin covers what
	// differs between two submits (the times' fractional digits).
	const margin = 1000
	var probe NewReview
	gap := 0
	for plain := 0; ; plain++ {
		if plain > 20 {
			t.Fatalf("no probe within a summary of the bound: %d bytes left", gap)
		}
		probe = request(candidate, append(findings("<")[:18], findings("a")[:plain]...)...)
		probe.Summary = ""
		pv := mustSubmit(t, s, opID("probe", plain), key, probe)
		b, _ = json.Marshal(pv)
		if gap = MaxReviewDetail - len(b); gap < 6*MaxReviewSummary-2*margin {
			break
		}
	}
	if gap < 2*margin {
		t.Fatalf("the probe leaves %d bytes", gap)
	}
	fill := func(n int) string { return strings.Repeat("<", n/6) + strings.Repeat("a", n%6) }
	under := probe
	under.Summary = fill(gap - margin)
	got := mustSubmit(t, s, "under", key, under)
	if b, _ := json.Marshal(got); len(b) > MaxReviewDetail || len(b) < MaxReviewDetail-2*margin {
		t.Fatalf("under the bound: %d bytes", len(b))
	}
	over := probe
	over.Summary = fill(gap + margin)
	rc, _, err = submit(s, "over", key, over)
	want(t, rc, err, StatusFailed, RefusalBound)
}

// A request on a closed ticket is refused until it is reopened; a comment
// or an approval of a closed ticket's candidate is kept. An archived
// project refuses a review, a mark and a request sent again; a deleted
// ticket is not found.
func TestReviewOnAClosedTicketOrArchivedProject(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	refused := mustSubmit(t, s, "open", key, request(candidate, lineChange("Fix it.")))
	rc, err := s.RefusedReview(ctx, testOp("x"), refused.ID, 1, "no")
	want(t, rc, err, StatusDone, "")
	mustEdit(t, s, "cancel", key, TicketEdit{Set: TicketSet{Status: ptr(TicketCancelled)}})
	rc, _, err = submit(s, "closed", key, request(candidate, lineChange("Fix it.")))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if !strings.Contains(rc.Error, "reopen the ticket") {
		t.Fatalf("the words: %q", rc.Error)
	}
	mustSubmit(t, s, "comment", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "Noted."})
	mustSubmit(t, s, "approve", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictApprove, Summary: "Good."})
	// A mark of a closed ticket's finding is kept: verifying needs no open
	// ticket.
	rc, _, err = mark(s, "m-closed", refused.Findings[0].ID, candidateOf(run, 3, commitB[:40]), FindingResolved, "")
	want(t, rc, err, StatusDone, "")
	mustEdit(t, s, "reopen", key, TicketEdit{Set: TicketSet{Status: ptr(TicketReady)}})
	mustSubmit(t, s, "reopened", key, request(candidate, lineChange("Fix it.")))
	rc, err = s.ArchiveProject(ctx, testOp("archive"), "CND")
	want(t, rc, err, StatusDone, "")
	rc, _, err = submit(s, "archived", key, NewReview{Candidate: candidate, Base: commitBase, Verdict: VerdictComment, Summary: "x"})
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, _, err = mark(s, "m-archived", refused.Findings[0].ID, candidateOf(run, 3, commitB[:40]), FindingOpen, "")
	want(t, rc, err, StatusFailed, RefusalArchived)
	rc, _, err = s.ApplyingReview(ctx, testOp("retry-archived"), refused.ID)
	want(t, rc, err, StatusFailed, RefusalArchived)
	// The outcome of an application is recorded whatever became of the
	// project: the repair was made.
	pending := reviewsOf(t, s, key)[0]
	rc, err = s.AppliedReview(ctx, testOp("x"), pending, 1, Applied{Run: run, Command: "c1", Revision: 2, Attempt: 3})
	want(t, rc, err, StatusDone, "")
	// A deleted ticket's.
	s2, key2, run2 := reviewTicket(t)
	v := mustSubmit(t, s2, "rv", key2, request(candidateOf(run2, 2, commitA), lineChange("Fix it.")))
	rc, err = s2.DeleteTicket(ctx, testOp("delete"), key2)
	want(t, rc, err, StatusDone, "")
	rc, _, err = submit(s2, "deleted", key2, request(candidateOf(run2, 2, commitA), lineChange("Fix it.")))
	want(t, rc, err, StatusFailed, RefusalNotFound)
	rc, _, err = mark(s2, "m-deleted", v.Findings[0].ID, candidateOf(run2, 3, commitB[:40]), FindingOpen, "")
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

// fixtureV2 is a copy of testdata/dispatch-v2.sqlite in a store directory
// of the test's: a version 2 database a DS2 binary wrote (main at
// 2829dd55's store, its pages then compacted at 1 KiB), holding two
// projects (one archived), three tickets, a label, a milestone, a
// relation, a comment, an attachment's row, a delivery link with its
// candidate and receipt, an external link, an approved plan revision and
// a launch started.
func fixtureV2(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A version 2 database is brought to version 3 at open (and on to this
// binary's version): three tables and five indexes made, every row of every table as it was, no migrate row,
// the schema read back a fresh store's; and it writes on: a review of the
// launched run's candidate.
func TestMigrationTwoToThreeKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV2(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	before := map[string][]string{}
	cols := map[string][]string{}
	for _, table := range tables {
		if slices.Contains([]string{"reviews", "review_findings", "finding_marks", "link_reads", "ticket_reads", "store_instance", "publications",
			"tickets_fts", "schema_version"}, table) || slices.Contains(version5Tables, table) {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, cols[table])
	}
	if len(before["tickets"]) != 3 || len(before["launches"]) != 1 || len(before["links"]) != 2 || len(before["changes"]) == 0 {
		t.Fatalf("the fixture: %d tickets, %d launches, %d links", len(before["tickets"]), len(before["launches"]), len(before["links"]))
	}
	raw.Close()
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed:\n got %v\nwant %v", table, got, before[table])
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	if got := uniqueIndexes(t, s.db); !slices.Equal(got, theUniqueIndexes) {
		t.Fatalf("the unique indexes: %v", got)
	}
	launches, err := s.Launches(ctx, "CND-1", 0)
	if err != nil || len(launches) != 1 {
		t.Fatalf("the launches: %+v %v", launches, err)
	}
	if page, err := s.Reviews(ctx, "CND-1", ""); err != nil || len(page.Reviews) != 0 {
		t.Fatalf("the reviews: %+v %v", page, err)
	}
	mustSubmit(t, s, "rv", "CND-1", request(candidateOf(launches[0].RunID, 2, commitA), lineChange("Fix it.")))
	if v, err := s.Ticket(ctx, "CND-2"); err != nil || len(v.Comments) != 1 || len(v.Links) != 1 {
		t.Fatalf("a ticket after the migration: %+v %v", v, err)
	}
}

// A version 3 database (and so this binary's, a later one) opens read-only
// under a binary that writes version 2 (a DS2 binary: its rule for a later
// version, the one this store applies, with the version it writes set to
// 2), its reviews kept: reads work, a write is refused read_only, and the
// file stays the version it was.
func TestVersionThreeIsReadOnlyForOlder(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	newProject(t, s, "CND")
	_, key := newTicket(t, s, "CND", "Reviewed")
	l := mustLaunch(t, s, "l1", key, simplePlan(t, s, key), "")
	v := mustSubmit(t, s, "rv", key, request(candidateOf(l.RunID, 2, commitA), lineChange("Fix it.")))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = 2 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("version %d, written by a newer Conductor", Version)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if tk, err := older.Ticket(ctx, key); err != nil || tk.Title != "Reviewed" {
		t.Fatalf("a read: %+v %v", tk, err)
	}
	if h, err := older.History(ctx, key, 0, 0); err != nil || len(h.Changes) == 0 {
		t.Fatalf("the history: %+v %v", h, err)
	}
	rc, err := older.AddComment(ctx, testOp("c1"), key, "x")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	older.Close()
	again := openStore(t, dir)
	if got := reviewOf(t, again, v.ID); len(got.Findings) != 1 {
		t.Fatalf("the review after: %+v", got)
	}
	var version int
	if err := again.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
}

// The field rules of a review, a finding and a mark (rules.go): what the
// person sent is immutable, the application gated and merged as a scalar,
// a finding's marks a log, every mark's field immutable, and every column
// of the three tables classed (but a review's rev, a replica's own count).
func TestReviewFieldClasses(t *testing.T) {
	s := openStore(t, storeDir(t))
	want := map[string]map[string]FieldClass{
		EntityReview: {
			"id": ClassImmutable, "ticket_id": ClassImmutable, "candidate": ClassImmutable, "base": ClassImmutable, "verdict": ClassImmutable,
			"summary": ClassImmutable, "created_at": ClassImmutable, "created_by": ClassImmutable, "actor": ClassImmutable, "via": ClassImmutable,
			"fingerprint": ClassImmutable, "app_state": ClassGated, "app_try": ClassGated, "app_run": ClassGated, "app_command": ClassGated,
			"app_revision": ClassGated, "app_attempt": ClassGated, "app_why": ClassGated, "app_at": ClassGated,
		},
		EntityFinding: {},
		"mark":        {},
	}
	tableOf := map[string]string{EntityReview: "reviews", EntityFinding: "review_findings", "mark": "finding_marks"}
	for _, col := range columnsOf(t, s.db, "review_findings") {
		want[EntityFinding][col] = ClassImmutable
	}
	for _, col := range columnsOf(t, s.db, "finding_marks") {
		want["mark"][col] = ClassImmutable
	}
	for entity, classes := range want {
		for _, col := range columnsOf(t, s.db, tableOf[entity]) {
			if col == "rev" {
				if got := Class(entity, col); got != "" {
					t.Errorf("%s.rev has the class %q", entity, got)
				}
				continue
			}
			c, ok := classes[col]
			if !ok {
				t.Errorf("%s.%s is a column the test does not know", entity, col)
				continue
			}
			if got := Class(entity, col); got != c {
				t.Errorf("Class(%s, %s) = %q, want %q", entity, col, got, c)
			}
		}
	}
	if Class(EntityFinding, "marks") != ClassLog || Class("finding_marks", "verdict") != ClassImmutable {
		t.Error("a finding's marks are no log")
	}
	t1 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := Merge(row(EntityReview, "app_state", OpSet, t1, "a", "x", 0), row(EntityReview, "app_state", OpSet, t1.Add(time.Second), "b", "y", 0)); got != TakeRemote {
		t.Errorf("a review's later application: %v", got)
	}
	if got := Merge(row(EntityReview, "summary", OpSet, t1, "a", "x", 0), row(EntityReview, "summary", OpSet, t1.Add(time.Second), "b", "y", 0)); got != KeepLocal {
		t.Errorf("a review's summary written twice: %v", got)
	}
	if got := Merge(row(EntityFinding, "marks", OpAdd, t1, "a", "x", 0), row(EntityFinding, "marks", OpAdd, t1, "b", "y", 0)); got != KeepBoth {
		t.Errorf("two marks: %v", got)
	}
}

// A snapshot of the store carries its reviews, findings and marks: a store
// opened over the copy reads them as the original does.
func TestSnapshotCarriesReviews(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("Fix it."), checkNote(candidateOf(run, 2, commitA))))
	rc, _, err := mark(s, "m1", v.Findings[0].ID, candidateOf(run, 3, commitB[:40]), FindingResolved, `{"state":"same"}`)
	want(t, rc, err, StatusDone, "")
	dst := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := s.Snapshot(ctx, dst); err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dst, filepath.Join(dir, File)); err != nil {
		t.Fatal(err)
	}
	copied := openStore(t, dir)
	a, b := reviewOf(t, s, v.ID), reviewOf(t, copied, v.ID)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("the copy reads\n%s\nnot\n%s", jb, ja)
	}
	m1, err1 := s.Marks(ctx, v.Findings[0].ID, "")
	m2, err2 := copied.Marks(ctx, v.Findings[0].ID, "")
	if err1 != nil || err2 != nil || len(m2.Marks) != 1 || m2.Marks[0].ID != m1.Marks[0].ID || string(m2.Marks[0].Located) != `{"state":"same"}` {
		t.Fatalf("the marks: %+v %v; %+v %v", m1, err1, m2, err2)
	}
}

// A review's commit reports the review, its subject, then each finding,
// each naming its ticket and its project; a mark reports its finding, an
// outcome and a retry the review. None names the ticket itself, whose rev
// stays.
func TestReviewChangesNameTheirTicket(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	tk := ticketOf(t, s, key)
	log := listen(s)
	v := mustSubmit(t, s, "rv", key, request(candidateOf(run, 2, commitA), lineChange("One."), lineChange("Two.")))
	changes := log.one(t)
	if len(changes) != 3 || changes[0].Entity != EntityReview || changes[0].ID != v.ID {
		t.Fatalf("the submit's changes: %+v", changes)
	}
	for i, c := range changes {
		if c.Ticket != tk.ID || c.Project != "CND" || c.Seq == 0 {
			t.Fatalf("change %d: %+v", i, c)
		}
		if i > 0 && (c.Entity != EntityFinding || c.ID != v.Findings[i-1].ID) {
			t.Fatalf("change %d: %+v", i, c)
		}
	}
	rc, _, err := mark(s, "m1", v.Findings[1].ID, candidateOf(run, 3, commitB[:40]), FindingResolved, "")
	want(t, rc, err, StatusDone, "")
	if c := log.one(t); len(c) != 1 || c[0].Entity != EntityFinding || c[0].ID != v.Findings[1].ID || c[0].Ticket != tk.ID || c[0].Project != "CND" {
		t.Fatalf("the mark's changes: %+v", c)
	}
	rc, err = s.RefusedReview(ctx, testOp("x"), v.ID, 1, "no")
	want(t, rc, err, StatusDone, "")
	if c := log.one(t); len(c) != 1 || c[0].Entity != EntityReview || c[0].ID != v.ID || c[0].Ticket != tk.ID {
		t.Fatalf("the outcome's changes: %+v", c)
	}
	rc, _, err = s.ApplyingReview(ctx, testOp("again"), v.ID)
	want(t, rc, err, StatusDone, "")
	if c := log.one(t); len(c) != 1 || c[0].Entity != EntityReview || c[0].ID != v.ID {
		t.Fatalf("the retry's changes: %+v", c)
	}
	// A resend changes nothing and reports nothing.
	rc, _, err = s.ApplyingReview(ctx, testOp("again"), v.ID)
	want(t, rc, err, StatusDone, "")
	if len(*log) != 0 {
		t.Fatalf("a resend reported %+v", *log)
	}
	if got := ticketOf(t, s, key).Rev; got != tk.Rev {
		t.Fatalf("the ticket's rev moved from %d to %d", tk.Rev, got)
	}
}

// The largest rows a review writes are under a change row's bound, every
// text at its bound and its escaped worst as a structured value encodes
// it (U+2028, six bytes; a '"', two): a finding's create row with its
// receipt at MaxCheckRef, a review's create row, a mark's add row.
func TestTheLargestReviewRowsAreUnderTheRowBound(t *testing.T) {
	worst := func(n int) string { return strings.Repeat(" ", n) }
	quoted := func(n int) string { return strings.Repeat(`"`, n) }
	ref := CheckRef{Source: CheckSourceLedger, Receipt: "crew-0123abcd/build/999999/verify/" + strings.Repeat("t", 40) + "/999999",
		Run: "crew-0123abcd", Task: strings.Repeat("b", 40), Attempt: 999999, Phase: PhaseVerify, ID: strings.Repeat("t", 40), K: 999999,
		Subject: strings.Repeat("a", 64), Exit: exit(-1), Signal: "killed", State: ReceiptInterrupted, Where: strings.Repeat("n", 32), LogSHA256: strings.Repeat("d", 64),
		LogBytes: 1 << 40, Tail: quoted(MaxCheckExcerpt), Error: quoted(MaxCheckError), Class: "infrastructure", Chosen: "infrastructure", Quote: quoted(MaxCheckExcerpt),
		Previous: &CheckPrevious{Attempt: 999998, State: "failed", Stage: "checks", Commit: strings.Repeat("b", 64)}}
	// The argv grown to the receipt's bound.
	for {
		ref.Argv = append(ref.Argv, quoted(256))
		if v, _ := encodeValue(ref); len(v) > MaxCheckRef {
			ref.Argv = ref.Argv[:len(ref.Argv)-1]
			break
		}
	}
	f := findingCreated{ID: strings.Repeat("f", IDLen), ReviewID: strings.Repeat("r", IDLen), TicketID: strings.Repeat("t", IDLen), K: MaxReviewFindings,
		Kind: FindingKindCheck, Intent: FindingIntentChange, Severity: "blocker", Path: quoted(MaxFindingPath), Side: SideCandidate,
		From: MaxFindingLine, To: MaxFindingLine, AnchorCommit: strings.Repeat("a", 64), Lines: slices.Repeat([]string{quoted(MaxAnchorLine)}, MaxAnchorLines),
		Message: worst(MaxFindingMessage), Resolution: worst(MaxFindingResolution), Check: &ref, CreatedAt: time.Now()}
	r := reviewCreated{ID: strings.Repeat("r", IDLen), TicketID: strings.Repeat("t", IDLen), Candidate: strings.Repeat("c", MaxLinkCandidate),
		Base: strings.Repeat("b", 64), Verdict: VerdictRequestChanges, Summary: worst(MaxReviewSummary), Via: ViaBrakeman, Actor: quoted(MaxActor),
		Fingerprint: strings.Repeat("f", 64), CreatedAt: time.Now(), CreatedBy: worst(MaxByName)}
	m := markAdded{Mark: Mark{ID: strings.Repeat("m", IDLen), FindingID: strings.Repeat("f", IDLen), TicketID: strings.Repeat("t", IDLen),
		Candidate: strings.Repeat("c", MaxLinkCandidate), Verdict: FindingWithdrawn, Located: json.RawMessage(`"` + worst(MaxMarkLocated/3-1) + `"`),
		Note: worst(MaxMarkNote), CreatedAt: time.Now(), CreatedBy: worst(MaxByName), Actor: quoted(MaxActor), Via: ViaBrakeman},
		OpID: strings.Repeat("o", 32), Fingerprint: strings.Repeat("f", 64)}
	for name, v := range map[string]any{"a finding's create": f, "a review's create": r, "a mark's add": m} {
		value, err := encodeValue(v)
		if err != nil {
			t.Fatal(err)
		}
		if len(value) > MaxChangeValue {
			t.Errorf("%s row is %d bytes, more than %d", name, len(value), MaxChangeValue)
		}
		t.Logf("%s row: %d bytes", name, len(value))
	}
}

// A review's detail read stays under MaxReviewReply whatever marks follow
// its submit: the review at MaxReviewDetail, and beside each of its
// findings the latest mark as the read carries it (no located), at its
// bounds and its escaped worst, with its count.
func TestAReviewsReadStaysUnderItsBoundWhateverMarksFollow(t *testing.T) {
	latest := Mark{ID: strings.Repeat("m", IDLen), FindingID: strings.Repeat("f", IDLen), TicketID: strings.Repeat("t", IDLen),
		Candidate: strings.Repeat("c", MaxLinkCandidate), Verdict: FindingWithdrawn, Note: strings.Repeat("<", MaxMarkNote), CreatedAt: time.Now(),
		CreatedBy: strings.Repeat("<", MaxByName), Actor: strings.Repeat("<", MaxActor), Via: ViaBrakeman}
	b, err := json.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	per := len(b) + len(`,"marks":50,"latest":`)
	if got := MaxReviewDetail + MaxReviewFindings*per; got > MaxReviewReply {
		t.Fatalf("a review at its bound with its latest marks is %d bytes, more than %d", got, MaxReviewReply)
	}
}

// A review's try is settled only by whoever holds the store's claim on
// that exact try: a submit's request comes with its try 1 claimed in its
// commit; nobody else claims it, records it or makes it pending again
// while it is held (an outcome recorded without the claim is answered
// failed, no receipt kept, and Request again is ErrReviewClaimed);
// released, Request again's try 2 comes claimed in its commit, try 1 is
// stale to a claim, and a release of the old claim drops nothing of the
// new one; the claim's waiters are woken by its release alone.
func TestAReviewsTryIsSettledByItsClaimAlone(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	r := request(candidateOf(run, 2, commitA), lineChange("Fix it."))
	rc, v, c1, err := s.SubmitReviewClaimed(ctx, testOp("rv"), key, r, evidenceOf(r))
	want(t, rc, err, StatusDone, "")
	if !c1.Held() || c1.Review != v.ID || c1.Try != 1 {
		t.Fatalf("the submit's claim: %+v", c1)
	}
	if _, _, ok, err := s.ClaimReview(ctx, v.ID, 1); ok || err != nil {
		t.Fatalf("a claimed try claimed again: %v %v", ok, err)
	}
	free := s.ReviewFree(v.ID)
	if free == nil {
		t.Fatal("no wait for a claimed review")
	}
	rc, err = s.RefusedReview(ctx, testOp("x"), v.ID, 1, "no")
	want(t, rc, err, StatusFailed, RefusalInvalid)
	if a := reviewOf(t, s, v.ID).Application; a.State != ApplicationPending {
		t.Fatalf("settled without the claim: %+v", a)
	}
	rc, err = s.RefusedReviewClaimed(ctx, testOp("x"), c1, "no")
	want(t, rc, err, StatusDone, "")
	if _, _, _, err := s.ApplyingReviewClaimed(ctx, testOp("again"), v.ID); !errors.Is(err, ErrReviewClaimed) {
		t.Fatalf("Request again while the claim is held: %v", err)
	}
	select {
	case <-free:
		t.Fatal("the claim's waiters woken before its release")
	default:
	}
	s.ReleaseReview(c1)
	select {
	case <-free:
	default:
		t.Fatal("the release did not wake the claim's waiters")
	}
	rc, got, c2, err := s.ApplyingReviewClaimed(ctx, testOp("again"), v.ID)
	want(t, rc, err, StatusDone, "")
	if !c2.Held() || c2.Try != 2 || got.Application.Try != 2 || got.Application.State != ApplicationPending {
		t.Fatalf("Request again's claim: %+v %+v", c2, got.Application)
	}
	if _, _, ok, err := s.ClaimReview(ctx, v.ID, 2); ok || err != nil {
		t.Fatalf("Request again's try claimed by another: %v %v", ok, err)
	}
	s.ReleaseReview(c1)
	if s.ReviewFree(v.ID) == nil {
		t.Fatal("an old claim's release dropped the new one")
	}
	rc, err = s.AppliedReviewClaimed(ctx, testOp("x"), c1, Applied{Run: run, Command: "rv." + v.ID + ".1", Revision: 3, Attempt: 2})
	want(t, rc, err, StatusFailed, RefusalInvalid)
	s.ReleaseReview(c2)
	if _, now, ok, err := s.ClaimReview(ctx, v.ID, 1); ok || err != nil || now.Application.Try != 2 {
		t.Fatalf("a stale try claimed: %v %+v %v", ok, now.Application, err)
	}
	c3, now, ok, err := s.ClaimReview(ctx, v.ID, 2)
	if !ok || err != nil || now.Application.Try != 2 || c3.Try != 2 {
		t.Fatalf("a pending try, free: %v %+v %v", ok, now.Application, err)
	}
	rc, err = s.AppliedReviewClaimed(ctx, testOp("x"), c3, Applied{Run: run, Command: "rv." + v.ID + ".2", Revision: 3, Attempt: 2})
	want(t, rc, err, StatusDone, "")
	s.ReleaseReview(c3)
	if _, _, ok, _ := s.ClaimReview(ctx, v.ID, 2); ok {
		t.Fatal("a settled try claimed")
	}
	// A comment is claimed by nothing: it is applied to nothing.
	cm := NewReview{Candidate: candidateOf(run, 2, commitA), Base: commitBase, Verdict: VerdictComment, Summary: "Nice.", Via: ViaWeb}
	rc, _, c4, err := s.SubmitReviewClaimed(ctx, testOp("cm"), key, cm, evidenceOf(cm))
	want(t, rc, err, StatusDone, "")
	if c4.Held() {
		t.Fatalf("a comment claimed: %+v", c4)
	}
}
