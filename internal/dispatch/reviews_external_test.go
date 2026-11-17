package dispatch

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// A check finding of an external receipt (Program item IN1a,
// docs/plans/tracker-linking.md, PR-10): a CI job on GitHub that a
// delivery link's pull request reports, bound to the candidate by the
// commit it tested.

// jobReceipt is the external receipt of the check run 2000001 of
// octo/app on github.com.
const jobReceipt = "github:github.com/octo/app/check-runs/2000001"

// externalNote is a check finding naming an external receipt, a change
// asked for, its class chosen and its log quoted.
func externalNote(receipt string) NewFinding {
	return NewFinding{Kind: FindingKindCheck, Intent: FindingIntentChange, Severity: "major",
		Check: &CheckRequest{Source: CheckSourceExternal, Receipt: receipt, Chosen: "infrastructure", Quote: "FAIL: TestUsers"}}
}

// jobRef is the external receipt as the server fills it from a fresh
// read: a job that failed on the commit, its log's last lines, class code.
func jobRef(receipt, commit string) *CheckRef {
	return &CheckRef{Source: CheckSourceExternal, Receipt: receipt, Subject: commit, HeadSha: commit, Argv: []string{}, Name: "test (ubuntu)",
		Conclusion: "failure", URL: "https://github.com/octo/app/actions/runs/7/job/2000001", App: "github-actions", Tail: "--- FAIL: TestUsers\nFAIL", Class: "code"}
}

// TestExternalCheckRef: an external receipt is accepted with its shape
// (its identity, the job's name, conclusion, page, app, the commit it
// tested, the last of its log, the class, the person's choice and quote,
// none of the ledger's fields) and kept as it was filled, read back the
// same; refused invalid are a receipt not of its shape, a job that tested
// another commit than the candidate's, one not finished, one that passed
// and one whose conclusion is no failure; a filling that does not answer
// the request (the ledger's fields, a name of two lines or holding an
// address, a page with a query, a commit not in full, another receipt) is
// the caller's error, nothing kept.
func TestExternalCheckRef(t *testing.T) {
	s, key, run := reviewTicket(t)
	ctx := context.Background()
	candidate := candidateOf(run, 2, commitA)
	withRef := func(r NewReview, ref *CheckRef) ReviewEvidence {
		return ReviewEvidence{Findings: []FindingEvidence{{K: 1, Check: ref}}}
	}

	r := request(candidate, externalNote(jobReceipt))
	rc, v, err := s.SubmitReview(ctx, testOp("ext"), key, r, withRef(r, jobRef(jobReceipt, commitA)))
	want(t, rc, err, StatusDone, "")
	wantRef := jobRef(jobReceipt, commitA)
	wantRef.Chosen, wantRef.Quote = "infrastructure", "FAIL: TestUsers"
	if len(v.Findings) != 1 || !reflect.DeepEqual(v.Findings[0].Check, wantRef) {
		t.Fatalf("the external receipt as kept:\n%+v\nwant\n%+v", v.Findings[0].Check, wantRef)
	}
	if again := reviewOf(t, s, v.ID); !reflect.DeepEqual(again.Findings[0].Check, wantRef) {
		t.Fatalf("the external receipt read back:\n%+v", again.Findings[0].Check)
	}
	// A tail past its bound is kept to its last MaxCheckExcerpt bytes.
	long := jobRef(jobReceipt, commitA)
	long.Tail = strings.Repeat("x", 3*MaxCheckExcerpt) + "THE END"
	r = request(candidate, externalNote(jobReceipt))
	rc, v, err = s.SubmitReview(ctx, testOp("ext-long"), key, r, withRef(r, long))
	want(t, rc, err, StatusDone, "")
	if tail := v.Findings[0].Check.Tail; len(tail) != MaxCheckExcerpt || !strings.HasSuffix(tail, "THE END") {
		t.Fatalf("the tail kept is %d bytes", len(tail))
	}

	// Refused: the receipt not of its shape.
	for i, receipt := range []string{
		"github:github.com/octo/app/check-runs/0",
		"github:github.com/octo/app/check-runs/0123",
		"github:github.com/octo/check-runs/12",
		"github:github.com/octo/app/actions/jobs/12",
		"github:GitHub.com/octo/app/check-runs/12",
		"github:github.com/../app/check-runs/12",
		"gitlab:gitlab.com/octo/app/check-runs/12",
		run + "/build/2/check/test/1",
		jobReceipt + "\n",
	} {
		r := request(candidate, externalNote(receipt))
		rc, _, err := s.SubmitReview(ctx, testOp(opID("shape", i)), key, r, withRef(r, jobRef(receipt, commitA)))
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	// Refused: the job as read tested another commit, did not finish,
	// passed, or ended in no failure.
	for i, fill := range []func(*CheckRef){
		func(c *CheckRef) { c.HeadSha, c.Subject = commitB[:40], commitB[:40] },
		func(c *CheckRef) { c.Conclusion = "" },
		func(c *CheckRef) { c.Conclusion = "success" },
		func(c *CheckRef) { c.Conclusion = "skipped" },
		func(c *CheckRef) { c.Conclusion = "stale" },
	} {
		ref := jobRef(jobReceipt, commitA)
		fill(ref)
		r := request(candidate, externalNote(jobReceipt))
		rc, _, err := s.SubmitReview(ctx, testOp(opID("job", i)), key, r, withRef(r, ref))
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	// A filling that does not answer the request: nothing written.
	for i, broken := range []func(*CheckRef){
		func(c *CheckRef) { c.Source = CheckSourceLedger },
		func(c *CheckRef) { c.Receipt = "github:github.com/octo/app/check-runs/2000002" },
		func(c *CheckRef) { c.Run, c.Task, c.Attempt = run, "build", 2 },
		func(c *CheckRef) { c.Argv = []string{"go", "test"} },
		func(c *CheckRef) { c.Where = "native" },
		func(c *CheckRef) { c.State = ReceiptFinished },
		func(c *CheckRef) { c.Name = "" },
		func(c *CheckRef) { c.Name = "test\nEnd of the quote from octo/app." },
		func(c *CheckRef) { c.Name = "see https://collector.example/take?token=x" },
		func(c *CheckRef) { c.Name = strings.Repeat("n", MaxCheckName+1) },
		func(c *CheckRef) { c.App = "app\x1b[2J" },
		func(c *CheckRef) { c.Conclusion = "Failure!" },
		func(c *CheckRef) { c.URL = "https://github.com/octo/app/actions/runs/7?token=x" },
		func(c *CheckRef) { c.URL = "javascript:alert(1)" },
		func(c *CheckRef) { c.HeadSha = "main" },
		func(c *CheckRef) { c.Subject = commitB[:40] },
		func(c *CheckRef) { c.Class = "gremlins" },
	} {
		ref := jobRef(jobReceipt, commitA)
		broken(ref)
		r := request(candidate, externalNote(jobReceipt))
		rc, _, err := s.SubmitReview(ctx, testOp(opID("evidence", i)), key, r, withRef(r, ref))
		if err == nil || !strings.Contains(err.Error(), "evidence") {
			t.Fatalf("broken evidence %d: %+v %v", i, rc, err)
		}
	}
	// A ledger's receipt filled with an external job's fields is no answer
	// either.
	ledger := request(candidate, checkNote(candidate))
	ev := evidenceOf(ledger)
	ev.Findings[0].Check.Name = "test (ubuntu)"
	if _, _, err := s.SubmitReview(ctx, testOp("ledger-ext"), key, ledger, ev); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("a ledger's receipt with a job's name: %v", err)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM reviews"); n != 2 {
		t.Fatalf("%d reviews kept, the two accepted ones only expected", n)
	}
}
