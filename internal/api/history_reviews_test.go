package api

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// RV1's review batches and findings in the history (docs/plans/history.md,
// PR-10): a person's batch a decision bound to its candidate and base,
// each finding said, and a finding's resolution, verified against a later
// candidate of another commit as RV1's mark says, an entry of its own,
// checked, while the finding stays said. Every review is written through
// Dispatch's own operations; every wait is on the indexer (histSettle),
// never on a clock.

// histReviewed is a run of the hist crew whose task build's attempt 1
// submitted commitID(1) and attempt 2 commitID(2), linked to the ticket
// CND-1 so that the ticket holds its chain, the request-applying worker
// held off its reviews (the test settles them through the store).
func histReviewed(t *testing.T, e *testEnv) (run string) {
	t.Helper()
	ld := e.srv.ledger
	run = "hist-000000a1"
	histCreate(t, ld, run, "", histCrew(e.root))
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		for n := 1; n <= 2; n++ {
			if err := tx.PutAttempt(histAttempt("build", n, ledger.AttemptSucceeded, "", commitID(n-1), commitID(n))); err != nil {
				return err
			}
		}
		return histTask(tx, ledger.TaskSucceeded, 2)
	})
	// The run's record, by which Dispatch knows the run a link names.
	if err := e.srv.records.Save(crew.Run{ID: run, CrewID: "hist", Name: "Hist", Cwd: e.root, State: crew.RunStopped, Managed: &crew.Managed{Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	histProject(t, e, "CND", e.root)
	ds := e.srv.dispatch
	if _, err := ds.CreateTicket(t.Context(), adminOp(""), dispatch.NewTicket{Project: "CND", Title: "The login loop"}); err != nil {
		t.Fatal(err)
	}
	doneOf(t)(ds.AddLink(t.Context(), adminOp("Ann"), "CND-1", dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}, nil))
	t.Cleanup(holdWorkerOff(e.srv))
	return run
}

// histFailedReceipt is the ledger's failed receipt of build #1's check
// unit, as the server fills a check finding's.
func histFailedReceipt(run string) *dispatch.CheckRef {
	exit := 1
	return &dispatch.CheckRef{Source: dispatch.CheckSourceLedger, Receipt: run + "/build/1/check/unit/1", Run: run, Task: "build", Attempt: 1,
		Phase: "check", ID: "unit", K: 1, Subject: commitID(1), Argv: []string{"go", "test", "./..."}, Exit: &exit, Stable: true,
		State: dispatch.ReceiptFinished, Where: "native", LogSHA256: strings.Repeat("d", 64), LogBytes: 30, Tail: "--- FAIL: TestGrebe\nFAIL", Class: "code"}
}

// histReview sends Nate's request for changes of build #1 against
// commitID(0): a change on lines of users.go, a check finding on the
// failed receipt, a question on the candidate as a whole; and answers it
// whole.
func histReview(t *testing.T, e *testEnv, run string) dispatch.Review {
	t.Helper()
	cand := candName(run, "build", 1, commitID(1))
	_, v, err := e.srv.dispatch.SubmitReview(t.Context(), adminOp("Nate"), "CND-1", dispatch.NewReview{Candidate: cand, Base: commitID(0),
		Verdict: dispatch.VerdictRequestChanges, Summary: "The pelican handler leaks its sessions.", Via: dispatch.ViaWeb, Findings: []dispatch.NewFinding{
			{Kind: dispatch.FindingKindLine, Intent: dispatch.FindingIntentChange, Severity: "blocker", Path: "users.go", Side: dispatch.SideCandidate,
				From: 14, To: 16, Lines: []string{"func users() {", "\treturn nil", "}"}, Message: "The albatross field is never closed.", Resolution: "Close it on logout."},
			{Kind: dispatch.FindingKindCheck, Intent: dispatch.FindingIntentChange, Severity: "major",
				Check: &dispatch.CheckRequest{Source: dispatch.CheckSourceLedger, Receipt: run + "/build/1/check/unit/1", Chosen: "flaky", Quote: "the cormorant test broke"}},
			{Kind: dispatch.FindingKindGeneral, Intent: dispatch.FindingIntentQuestion, Message: "Why does the heron retry twice?"}}},
		dispatch.ReviewEvidence{Findings: []dispatch.FindingEvidence{{K: 1, AnchorCommit: commitID(1)}, {K: 2, Check: histFailedReceipt(run)}, {K: 3}}})
	if err != nil || v.ID == "" || len(v.Findings) != 3 {
		t.Fatalf("the review: %+v %v", v, err)
	}
	return v
}

// histMarkFinding marks finding id against build #n's candidate.
func histMarkFinding(t *testing.T, e *testEnv, run, id string, n int, verdict, note string) dispatch.Mark {
	t.Helper()
	_, m, err := e.srv.dispatch.MarkFinding(t.Context(), adminOp("Nate"), id, dispatch.NewMark{Candidate: candName(run, "build", n, commitID(n)), Verdict: verdict, Note: note, Via: dispatch.ViaWeb}, nil)
	if err != nil || m.ID == "" {
		t.Fatalf("the mark %s of %s: %+v %v", verdict, id, m, err)
	}
	return m
}

// A batch and its findings are indexed and found by their keys: the batch
// a decision by its reviewer, bound to its candidate (its run, commit and
// name) and its base, its summary searchable, linked reviews to the
// attempt it reviewed and reopened to the repair Yardmaster applied; each
// finding said, its outcome its state now, a check finding citing its
// receipt; a resolved finding's resolution checked, on the later
// candidate it was verified on, linked resolves to it. The search serves
// each with the ticket's Review view as its page and Dispatch as its
// store, and the ticket's own entries stay as they were.
func TestAReviewBatchAndItsFindingsAreIndexedByTheirKeys(t *testing.T) {
	e := newTestEnv(t, nil)
	run := histReviewed(t, e)
	v := histReview(t, e, run)
	line, check, question := v.Findings[0].ID, v.Findings[1].ID, v.Findings[2].ID
	if _, err := e.srv.dispatch.AppliedReview(t.Context(), dispatch.Op{Actor: adminActor, ID: opID()}, v.ID, 1,
		dispatch.Applied{Run: run, Command: "rv." + v.ID + ".1", Revision: 7, Attempt: 2}); err != nil {
		t.Fatal(err)
	}
	m := histMarkFinding(t, e, run, line, 2, dispatch.FindingResolved, "Closed in the osprey hook.")
	histMarkFinding(t, e, run, question, 2, dispatch.FindingWithdrawn, "")
	ix := histSettle(t, e)

	cand1, cand2 := candName(run, "build", 1, commitID(1)), candName(run, "build", 2, commitID(2))
	b := histEntry(t, ix, hReviewRef("CND-1", v.ID))
	if b.Kind != "review.batch" || b.Class != history.ClassDecided || b.Outcome != dispatch.VerdictRequestChanges || b.Actor != adminActor ||
		b.Run != run || b.Commit != commitID(1) || b.Revision != "build#1" || b.Ticket != "CND-1" || b.Project != "CND" || b.Anchor != "ticket:CND-1" ||
		b.Title != "Request changes on build#1: The pelican handler leaks its sessions." {
		t.Fatalf("the batch: %+v", b.Entry)
	}
	for _, w := range []string{"by Nate", "against base " + hShort(commitID(0)), "3 findings: 1 open, 1 resolved, 1 withdrawn", "applied: build#2 starts at"} {
		if !strings.Contains(b.Summary, w) {
			t.Fatalf("the batch's summary %q has not %q", b.Summary, w)
		}
	}
	if got := histOut(t, ix, b.Ref, relReviews); !slices.Equal(got, []string{attemptKey(run, "build", 1)}) {
		t.Fatalf("the batch reviews %v", got)
	}
	if got := histOut(t, ix, b.Ref, relReopened); !slices.Equal(got, []string{attemptKey(run, "build", 2)}) {
		t.Fatalf("the batch reopened %v", got)
	}

	for id, want := range map[string]struct{ outcome, title string }{
		line:     {dispatch.FindingResolved, "blocker: The albatross field is never closed."},
		check:    {dispatch.FindingOpen, "major: the check unit failed on build#1"},
		question: {dispatch.FindingWithdrawn, "question: Why does the heron retry twice?"},
	} {
		f := histEntry(t, ix, hFindingRef("CND-1", id))
		if f.Kind != "review.finding" || f.Class != history.ClassSaid || f.Outcome != want.outcome || f.Title != want.title || f.Actor != adminActor ||
			f.Commit != commitID(1) || f.Revision != "build#1" {
			t.Fatalf("the finding %s: %+v", id, f.Entry)
		}
		if got := histOut(t, ix, f.Ref, relReviews); !slices.Equal(got, []string{attemptKey(run, "build", 1)}) {
			t.Fatalf("the finding %s reviews %v", id, got)
		}
	}
	if got := histOut(t, ix, hFindingRef("CND-1", check), relCites); !slices.Equal(got, []string{hCheckRef(run, "build", 1, "unit", 1)}) {
		t.Fatalf("the check finding cites %v", got)
	}

	r := histEntry(t, ix, hResolvedRef("CND-1", m.ID))
	if r.Kind != "review.resolved" || r.Class != history.ClassChecked || r.Outcome != dispatch.FindingResolved || r.Actor != adminActor ||
		r.Run != run || r.Commit != commitID(2) || r.Revision != "build#2" || !strings.Contains(r.Summary, "made on build#1") {
		t.Fatalf("the resolution: %+v", r.Entry)
	}
	if got := histOut(t, ix, r.Ref, relResolves); !slices.Equal(got, []string{hFindingRef("CND-1", line)}) {
		t.Fatalf("the resolution resolves %v", got)
	}

	// Found by their words, each served with its page, its store and its
	// availability; the person's words and quote are searched, never the
	// ledger's tail.
	for word, want := range map[string]struct{ key, url string }{
		"pelican":   {hReviewRef("CND-1", v.ID), "/dispatch/CND/1?view=review&candidate=" + url.QueryEscape(cand1)},
		"albatross": {hFindingRef("CND-1", line), "/dispatch/CND/1?view=review&candidate=" + url.QueryEscape(cand1) + "&focus=findings"},
		"cormorant": {hFindingRef("CND-1", check), "/dispatch/CND/1?view=review&candidate=" + url.QueryEscape(cand1) + "&focus=findings"},
		"osprey":    {hResolvedRef("CND-1", m.ID), "/dispatch/CND/1?view=review&candidate=" + url.QueryEscape(cand2) + "&focus=findings"},
	} {
		x := hsEntry(hsSearch(t, e, word), want.key)
		if x == nil || x["url"] != want.url || x["source"].(map[string]any)["store"] != "dispatch" || availOf(x)["state"] != availKept {
			t.Fatalf("%q: %v, want %s at %s", word, x, want.key, want.url)
		}
	}
	if keys := hsKeys(hsSearch(t, e, "TestGrebe")); len(keys) != 0 {
		t.Fatalf("the receipt's tail is searched in a finding: %v", keys)
	}
	// The link's far end opens its own page.
	full := hsEntryOf(t, e, r.Key)
	found := false
	for _, l := range full["links"].([]any) {
		l := l.(map[string]any)
		if l["rel"] == relResolves && l["key"] == hFindingRef("CND-1", line) {
			found = l["url"] == "/dispatch/CND/1?view=review&candidate="+url.QueryEscape(cand1)+"&focus=findings" && l["class"] == history.ClassSaid
		}
	}
	if !found {
		t.Fatalf("the resolution's link to its finding: %v", full["links"])
	}
	// The checked class holds only the resolution; the decided only the
	// batch.
	if keys := hsKeys(hsSearch(t, e, "ticket:CND-1 class:checked")); !slices.Equal(keys, []string{hResolvedRef("CND-1", m.ID)}) {
		t.Fatalf("checked: %v", keys)
	}
	if keys := hsKeys(hsSearch(t, e, "ticket:CND-1 class:decided")); !slices.Equal(keys, []string{hReviewRef("CND-1", v.ID)}) {
		t.Fatalf("decided: %v", keys)
	}
	histEntry(t, ix, "ticket:CND-1")
}

// A finding stays said beside its resolution: marked open again on the
// later candidate, its resolution's entry leaves with the fact it stood
// for and the finding says it is open; resolved once more, the new mark
// is an entry of its own, under its own key. A batch's verdict other than
// a request (an approval) is a decision too, with no application.
func TestAFindingStaysSaidBesideItsResolution(t *testing.T) {
	e := newTestEnv(t, nil)
	run := histReviewed(t, e)
	v := histReview(t, e, run)
	line := v.Findings[0].ID
	first := histMarkFinding(t, e, run, line, 2, dispatch.FindingResolved, "")
	ix := histSettle(t, e)
	if r := histEntry(t, ix, hResolvedRef("CND-1", first.ID)); r.Class != history.ClassChecked {
		t.Fatalf("the resolution: %+v", r.Entry)
	}
	if f := histEntry(t, ix, hFindingRef("CND-1", line)); f.Class != history.ClassSaid || f.Outcome != dispatch.FindingResolved {
		t.Fatalf("the resolved finding: %+v", f.Entry)
	}

	histMarkFinding(t, e, run, line, 2, dispatch.FindingOpen, "the leak is back")
	ix = histSettle(t, e)
	histGone(t, ix, hResolvedRef("CND-1", first.ID))
	if f := histEntry(t, ix, hFindingRef("CND-1", line)); f.Class != history.ClassSaid || f.Outcome != dispatch.FindingOpen {
		t.Fatalf("the reopened finding: %+v", f.Entry)
	}

	again := histMarkFinding(t, e, run, line, 2, dispatch.FindingResolved, "fixed for good")
	ix = histSettle(t, e)
	histGone(t, ix, hResolvedRef("CND-1", first.ID))
	if r := histEntry(t, ix, hResolvedRef("CND-1", again.ID)); r.Class != history.ClassChecked || r.Body != "fixed for good" {
		t.Fatalf("the second resolution: %+v", r.Entry)
	}

	_, ok, err := e.srv.dispatch.SubmitReview(t.Context(), adminOp("Nate"), "CND-1", dispatch.NewReview{Candidate: candName(run, "build", 2, commitID(2)),
		Base: commitID(0), Verdict: dispatch.VerdictApprove, Summary: "Ship the egret.", Findings: []dispatch.NewFinding{}},
		dispatch.ReviewEvidence{Findings: []dispatch.FindingEvidence{}})
	if err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	a := histEntry(t, ix, hReviewRef("CND-1", ok.ID))
	if a.Class != history.ClassDecided || a.Outcome != dispatch.VerdictApprove || a.Commit != commitID(2) || strings.Contains(a.Summary, "applied") ||
		!strings.Contains(a.Summary, "no findings") {
		t.Fatalf("the approval: %+v", a.Entry)
	}
	if got := histOut(t, ix, a.Ref, relReopened); len(got) != 0 {
		t.Fatalf("an approval reopened %v", got)
	}
}

// Every text of a review's entries holds the whole of what RV1 keeps
// within its bound: a summary of 4,000 characters, a finding's words and
// resolution of 1,000 characters each with its twelve lines of 200 bytes
// and a check's quote of 2 KiB, a mark's note of 1,000 characters, each
// character four bytes; the index takes them, nothing cut.
func TestAReviewAtItsBoundsIsIndexedWhole(t *testing.T) {
	e := newTestEnv(t, nil)
	ix := histSettle(t, e)
	run := "hist-000000a2"
	wide := func(n int) string { return strings.Repeat("𝄞", n) }
	lines := make([]string, dispatch.MaxAnchorLines)
	for i := range lines {
		lines[i] = strings.Repeat(string(rune('a'+i)), dispatch.MaxAnchorLine)
	}
	check := histFailedReceipt(run)
	check.Quote, check.Chosen = strings.Repeat("q", dispatch.MaxCheckExcerpt), "infrastructure"
	at := histNow
	f := dispatch.Finding{ID: "00000000000000f1", Kind: dispatch.FindingKindCheck, Intent: dispatch.FindingIntentChange, Severity: "blocker",
		Lines: lines, Message: wide(dispatch.MaxFindingMessage), Resolution: wide(dispatch.MaxFindingResolution), Check: check, CreatedAt: at,
		State: dispatch.FindingResolved, Latest: &dispatch.Mark{ID: "00000000000000m1", Candidate: candName(run, "build", 2, commitID(2)),
			Verdict: dispatch.FindingResolved, Note: wide(dispatch.MaxMarkNote), CreatedAt: at.Add(time.Minute), Actor: adminActor}}
	r := dispatch.Review{ReviewSummary: dispatch.ReviewSummary{ID: "00000000000000r1", Candidate: candName(run, "build", 1, commitID(1)), Base: commitID(0),
		Verdict: dispatch.VerdictRequestChanges, Summary: wide(dispatch.MaxReviewSummary), CreatedAt: at, Actor: adminActor,
		Application: dispatch.ReviewApplication{State: dispatch.ApplicationRefused, Why: wide(500)}}, Findings: []dispatch.Finding{f}}
	entries, links := reviewEntries(history.Entry{Anchor: "ticket:CND-9", Project: "CND", Ticket: "CND-9", At: at}, "CND-9", []dispatch.Review{r})
	if len(entries) != 3 {
		t.Fatalf("%d entries", len(entries))
	}
	if err := ix.Replace(t.Context(), history.Source{Key: "ticket:CND-9", Mark: "Q1"}, entries, links); err != nil {
		t.Fatal(err)
	}
	for _, x := range entries {
		var whole []string
		switch x.Kind {
		case "review.batch":
			whole = []string{r.Summary}
		case "review.finding":
			whole = append([]string{f.Message, "Asked: " + f.Resolution, "Quote: " + check.Quote, "class code, chosen infrastructure"}, lines...)
		case "review.resolved":
			whole = []string{f.Latest.Note}
		}
		got := histEntry(t, ix, x.Key)
		for _, w := range whole {
			if !strings.Contains(got.Body, w) {
				t.Fatalf("%s's body (%d bytes) lost %d bytes of what it keeps", x.Kind, len(got.Body), len(w))
			}
		}
		if len(got.Title) > history.MaxTitle || len(got.Summary) > history.MaxSummary {
			t.Fatalf("%s: a title of %d bytes, a summary of %d", x.Kind, len(got.Title), len(got.Summary))
		}
	}
}
