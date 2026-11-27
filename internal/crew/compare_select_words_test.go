package crew

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A selection's words (docs/plans/compare-crew.md, PR-6a, Which record):
// each option, and the record's context, say of the candidate what the
// comparison says of its checks: each check's outcome and its row's
// verdict, with why in the comparison's own words. A person answering on
// Needs you, the ticket or a phone reads them there and never sees the
// comparison's rows. Each test reads the words from the decision record in
// the ledger, and the verdict from the comparison itself.

// launchWords launches a comparison of two candidates, without reviews,
// whose one check runs argv, over a base that also holds files.
func launchWords(t *testing.T, argv []string, files ...edit) *selectEnv {
	t.Helper()
	se := &selectEnv{dir: newRepo(t), ldir: t.TempDir()}
	if len(files) > 0 {
		applyEdits(t, se.dir, files)
		commitAll(t, se.dir, "the check")
	}
	se.e, se.fl, se.ld = decisionEngineAt(t, se.ldir)
	se.e.compareOpen = true
	se.c = compareCrew(t, se.dir, 2, false)
	for i := range se.c.Tasks {
		se.c.Tasks[i].Checks = []Check{{ID: "ok", Argv: argv}}
	}
	se.run = launchPlanned(t, se.e, se.c, planned(t, cmpRunID, head(t, se.dir)))
	return se
}

// verdictOf is the comparison's one check row as it reads now.
func verdictOf(t *testing.T, se *selectEnv) ComparedCheck {
	t.Helper()
	c, err := se.e.Comparison(t.Context(), se.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 1 {
		t.Fatalf("the comparison's rows: %+v", c.Checks)
	}
	return c.Checks[0]
}

// optionWords are the tradeoffs of a selection's options, by label.
func optionWords(b selectionBody) map[string]string {
	out := map[string]string{}
	for _, o := range b.Options {
		out[o.Label] = o.Tradeoffs
	}
	return out
}

func TestTheSelectionSaysTheComparisonsVerdict(t *testing.T) {
	t.Run("every check comparable", func(t *testing.T) {
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		if rw := verdictOf(t, se); rw.Verdict != VerdictComparable {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		words := optionWords(body)
		for _, c := range []TaskState{a, b} {
			label := c.ID + " at " + c.Result.Commit[:7]
			if got, want := words[label], "attempt 1; check ok: passed, comparable"; got != want {
				t.Fatalf("%s says %q, want %q", label, got, want)
			}
			if want := fmt.Sprintf("%s: %s, attempt 1; check ok: passed, comparable.", c.ID, c.Result.Commit[:7]); !strings.Contains(body.Context, want) {
				t.Fatalf("the context %q has no %q", body.Context, want)
			}
		}
	})

	t.Run("a check not comparable", func(t *testing.T) {
		// The check runs check.sh, and approach-a adds a case to it: the
		// comparison's row is not comparable, though both passed.
		se := launchWords(t, []string{"sh", "check.sh"}, put("check.sh", "test ! -e broken\n"))
		defer stopRun(t, se.e, se.run.ID)
		ts := waitTaskWhere(t, se.e, se.run.ID, "approach-a", "at attempt 1, running", func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == 1 && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, se.e, ts, "check.sh", "test ! -e broken\ntest ! -e broken.txt\n", "one more case")
		a := waitTask(t, se.e, se.run.ID, "approach-a", ledger.TaskSucceeded, 0)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		why := "approach-a changed check.sh, which the check runs"
		if rw := verdictOf(t, se); rw.Verdict != VerdictNotComparable || !whyHas(rw, why) {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		words := optionWords(body)
		want := "attempt 1; check ok: passed, not comparable (" + why + ")"
		for _, c := range []TaskState{a, b} {
			label := c.ID + " at " + c.Result.Commit[:7]
			if got := words[label]; got != want {
				t.Fatalf("%s says %q, want %q", label, got, want)
			}
			if w := fmt.Sprintf("%s: %s, %s.", c.ID, c.Result.Commit[:7], want); !strings.Contains(body.Context, w) {
				t.Fatalf("the context %q has no %q", body.Context, w)
			}
		}
		if strings.Contains(body.Context, "its checks passed") {
			t.Fatalf("the context says its checks passed: %q", body.Context)
		}
	})

	t.Run("a failed check", func(t *testing.T) {
		// approach-b's every attempt fails the check: the selection has
		// approach-a, and its context says what approach-b's last commit
		// did; accepted anyway, approach-b's option says its check failed.
		se := launchSelect(t, 2)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		failed := se.fail(t, se.run.ID, "approach-b")
		first := selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID)
		if rw := verdictOf(t, se); rw.Verdict != VerdictComparable {
			t.Fatalf("the comparison's row: %+v", rw)
		}
		fb := selectBody(t, first)
		passed := "attempt 1; check ok: passed, comparable"
		if got := optionWords(fb)["approach-a at "+a.Result.Commit[:7]]; got != passed {
			t.Fatalf("approach-a says %q", got)
		}
		n := failed.Attempt.N
		failedWords := fmt.Sprintf("approach-b: no result (failed); at %s, attempt %d: check ok: failed (exit 1), comparable.", failed.Attempt.Result[:7], n)
		if !strings.Contains(fb.Context, failedWords) {
			t.Fatalf("the context %q has no %q", fb.Context, failedWords)
		}

		if rc, err := se.e.Accept(t.Context(), se.run.ID, "admin", Command{ID: "a1"}, "approach-b"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("accept: %+v %v", rc, err)
		}
		next := selectionWhen(t, se.ld, "a second, open", func(row ledger.DecisionRow) bool { return row.N != first.N && row.State == ledger.DecisionOpen }, se.run.ID)
		nb := selectBody(t, next)
		words := optionWords(nb)
		if got := words["approach-a at "+a.Result.Commit[:7]]; got != passed {
			t.Fatalf("approach-a says %q", got)
		}
		want := fmt.Sprintf("attempt %d; check ok: failed (exit 1), comparable; accepted after a failed check", n)
		if got := words["approach-b at "+failed.Attempt.Result[:7]]; got != want {
			t.Fatalf("approach-b says %q, want %q", got, want)
		}
	})
}

// With no comparison read before the opening, or one read before a change
// of the run committed, an option says that its checks were not compared:
// never a bare "its checks passed".
func TestAnOptionReadsOnlyACurrentComparison(t *testing.T) {
	exit0 := 0
	c := SettledCandidate{Task: "approach-a", Run: "r1", Attempt: 1, Commit: "abcdef0123"}
	cmp := Comparison{
		Candidates: []ComparedCandidate{{Task: "approach-a", Run: "r1", Attempt: 1, Commit: "abcdef0123"}},
		Checks: []ComparedCheck{{Phase: ledger.PhaseCheck, ID: "ok", Verdict: VerdictNotComparable, Why: []string{"approach-a changed check.sh, which the check runs"},
			Cells: []CheckCell{{Task: "approach-a", Attempt: 1, State: ledger.CheckFinished, Exit: &exit0}}}},
	}
	r := &run{id: "r1", revision: 7}
	fallback := "attempt 1; its checks passed, " + notCompared
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with none read: %q", got)
	}
	r.sel.measured = &aheadMeasure{rev: 6, cmp: cmp}
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with one read before a change: %q", got)
	}
	r.sel.measured.rev = 7
	if got, want := r.tradeoffs(c), "attempt 1; check ok: passed, not comparable (approach-a changed check.sh, which the check runs)"; got != want {
		t.Fatalf("with a current one: %q, want %q", got, want)
	}
	// Another commit of the lane than the comparison shows: not its words.
	other := c
	other.Commit = "0123abcdef"
	if got := r.tradeoffs(other); got != fallback {
		t.Fatalf("for another commit: %q", got)
	}
	// A change carried ahead of the ledger: the read is not the state.
	r.pendingEvents = []event{{"x", nil}}
	if got := r.tradeoffs(c); got != fallback {
		t.Fatalf("with a change carried: %q", got)
	}
}

// launchTwoChecks launches a comparison of two candidates, without
// reviews, whose checks are ok (failing on a commit that holds the file
// "broken") and then two (taking two minutes on a commit that holds the
// file "slow", so that a cancel finds it in flight; ended by the cancel).
func launchTwoChecks(t *testing.T) *selectEnv {
	t.Helper()
	se := &selectEnv{dir: newRepo(t), ldir: t.TempDir()}
	se.e, se.fl, se.ld = decisionEngineAt(t, se.ldir)
	se.e.compareOpen = true
	se.c = compareCrew(t, se.dir, 2, false)
	for i := range se.c.Tasks {
		se.c.Tasks[i].Checks = []Check{
			{ID: "ok", Argv: []string{"sh", "-c", "test ! -e broken"}},
			{ID: "two", Argv: []string{"sh", "-c", "test ! -e slow || sleep 120"}},
		}
	}
	se.run = launchPlanned(t, se.e, se.c, planned(t, cmpRunID, head(t, se.dir)))
	return se
}

// checkRowOf is the comparison's row of check id as it reads now.
func checkRowOf(t *testing.T, se *selectEnv, id string) ComparedCheck {
	t.Helper()
	c, err := se.e.Comparison(t.Context(), se.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rw := range c.Checks {
		if rw.ID == id {
			return rw
		}
	}
	t.Fatalf("no row %s: %+v", id, c.Checks)
	return ComparedCheck{}
}

// noneUnderWay fails when a selection's words say a check is under way.
func noneUnderWay(t *testing.T, b selectionBody) {
	t.Helper()
	for _, w := range append([]string{b.Context}, slices.Collect(maps.Values(optionWords(b)))...) {
		if strings.Contains(w, "under way") || strings.Contains(w, "(running)") || strings.Contains(w, "(started)") || strings.Contains(w, notCompared) {
			t.Fatalf("the selection says %q", w)
		}
	}
}

// The selection opens in the last lane's own change: the words say of that
// lane what the change leaves of it (its result taken, its last attempt
// failed at a check, its attempt cancelled with a check in flight), never
// that its checks are under way, as they were in the comparison read just
// before the change.
func TestTheLastLanesOwnChangeIsReadAsItLeavesIt(t *testing.T) {
	passed := "check ok: passed, comparable; check two: passed, comparable"
	t.Run("accepted", func(t *testing.T) {
		se := launchTwoChecks(t)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		b := se.pass(t, se.run.ID, "approach-b", 1)
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		noneUnderWay(t, body)
		words := optionWords(body)
		for _, c := range []TaskState{a, b} {
			if got, want := words[c.ID+" at "+c.Result.Commit[:7]], "attempt 1; "+passed; got != want {
				t.Fatalf("%s says %q, want %q", c.ID, got, want)
			}
		}
	})
	t.Run("failed", func(t *testing.T) {
		se := launchTwoChecks(t)
		defer stopRun(t, se.e, se.run.ID)
		a := se.pass(t, se.run.ID, "approach-a", 1)
		failed := se.fail(t, se.run.ID, "approach-b")
		body := selectBody(t, selectionWhen(t, se.ld, "open", rowIn(ledger.DecisionOpen), se.run.ID))
		noneUnderWay(t, body)
		why := "approach-b: not run: an earlier check failed"
		if rw := checkRowOf(t, se, "two"); rw.Verdict != VerdictNotComparable || !whyHas(rw, why) {
			t.Fatalf("the comparison's row two: %+v", rw)
		}
		want := "attempt 1; check ok: passed, comparable; check two: passed, not comparable (" + why + ")"
		if got := optionWords(body)["approach-a at "+a.Result.Commit[:7]]; got != want {
			t.Fatalf("approach-a says %q, want %q", got, want)
		}
		lane := fmt.Sprintf("approach-b: no result (failed); at %s, attempt %d: check ok: failed (exit 1), comparable; check two: not run, not comparable (%s).", failed.Attempt.Result[:7], failed.Attempt.N, why)
		if !strings.Contains(body.Context, lane) {
			t.Fatalf("the context %q has no %q", body.Context, lane)
		}
	})
	// inFlight has approach-a pass, and approach-b submit a result whose
	// check two is in flight, its process running as the ledger holds it:
	// nothing of the run is written again until it ends.
	inFlight := func(t *testing.T, se *selectEnv) TaskState {
		t.Helper()
		a := se.pass(t, se.run.ID, "approach-a", 1)
		ts := waitTaskWhere(t, se.e, se.run.ID, "approach-b", "at attempt 1, running", func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == 1 && ts.Attempt.State == ledger.AttemptRunning
		})
		commitResult(t, se.e, ts, "slow", "x\n", "a slow one")
		waitTrue(t, "approach-b's check two running", func() bool {
			rows, err := se.ld.Checks(t.Context(), se.run.ID, "approach-b", 1, false)
			if err != nil {
				t.Fatal(err)
			}
			return slices.ContainsFunc(rows, func(row ledger.CheckRow) bool {
				return row.ID == "two" && row.State == ledger.CheckRunning
			})
		})
		return a
	}
	t.Run("cancelled", func(t *testing.T) {
		se := launchTwoChecks(t)
		defer stopRun(t, se.e, se.run.ID)
		a := inFlight(t, se)
		se.cancel(t, se.run.ID, "approach-b", "c1")
		// Opened in the cancel's own change: there once the command returns.
		rows := selections(t, se.ld, se.run.ID)
		if len(rows) != 1 || rows[0].State != ledger.DecisionOpen {
			t.Fatalf("the records after the cancel: %+v", rows)
		}
		body := selectBody(t, rows[0])
		noneUnderWay(t, body)
		why := "approach-b: its run did not finish (interrupted)"
		want := "attempt 1; check ok: passed, comparable; check two: passed, not comparable (" + why + ")"
		if got := optionWords(body)["approach-a at "+a.Result.Commit[:7]]; got != want {
			t.Fatalf("approach-a says %q, want %q", got, want)
		}
		// The comparison says the same once the check's end is recorded.
		waitTrue(t, "the comparison's row two as the record says", func() bool {
			rw := checkRowOf(t, se, "two")
			return rw.Verdict == VerdictNotComparable && whyHas(rw, why)
		})
	})
	// A cancel sent with an expected revision is not held between its
	// taking and its change for the read, where a commit of the lane would
	// make it stale: its selection says the checks were not compared.
	t.Run("cancelled with an expected revision", func(t *testing.T) {
		se := launchTwoChecks(t)
		defer stopRun(t, se.e, se.run.ID)
		a := inFlight(t, se)
		got, _ := se.e.Get(se.run.ID)
		if rc, err := se.e.Cancel(t.Context(), se.run.ID, "admin", Command{ID: "c1", ExpectedRevision: got.Managed.Revision}, "approach-b"); err != nil || rc.Status != ledger.CommandDone {
			t.Fatalf("cancel: %+v %v", rc, err)
		}
		rows := selections(t, se.ld, se.run.ID)
		if len(rows) != 1 || rows[0].State != ledger.DecisionOpen {
			t.Fatalf("the records after the cancel: %+v", rows)
		}
		if got, want := optionWords(selectBody(t, rows[0]))["approach-a at "+a.Result.Commit[:7]], "attempt 1; its checks passed, "+notCompared; got != want {
			t.Fatalf("approach-a says %q, want %q", got, want)
		}
	})
}

// longComparison is a measured comparison of the candidates named, with n
// rows, each not comparable for long reasons.
func longComparison(cands []SettledCandidate, n int) Comparison {
	exit0 := 0
	c := Comparison{}
	for _, sc := range cands {
		c.Candidates = append(c.Candidates, ComparedCandidate{Task: sc.Task, Run: sc.Run, Attempt: sc.Attempt, Commit: sc.Commit})
	}
	for i := range n {
		row := ComparedCheck{Phase: ledger.PhaseVerify, ID: fmt.Sprintf("verify-step-with-a-long-name-%02d", i), Verdict: VerdictNotComparable}
		for _, sc := range cands {
			row.Cells = append(row.Cells, CheckCell{Task: sc.Task, Attempt: sc.Attempt, State: ledger.CheckFinished, Exit: &exit0})
			row.Why = append(row.Why, sc.Task+" changed the project manifest", "the manifest: approach-a and "+sc.Task+" differ")
		}
		c.Checks = append(c.Checks, row)
	}
	return c
}

// Words past the ledger's bounds keep whole clauses and say what they
// leave out: an option's checks ("and N more checks: the comparison has
// them"), the context's candidates and criteria; never a clause cut in
// its middle.
func TestTheWordsKeepWholeClausesAtTheBound(t *testing.T) {
	var cands []SettledCandidate
	for _, task := range []string{"approach-a", "approach-b", "approach-c", "approach-d"} {
		cands = append(cands, SettledCandidate{Task: task, Run: "r1", Attempt: 1, Commit: strings.Repeat(task[len(task)-1:], 40)})
	}
	r := &run{id: "r1", revision: 3, base: strings.Repeat("0", 40), compare: &Compare{}}
	for i := range maxCompareCriteria {
		r.compare.Criteria = append(r.compare.Criteria, Criterion{ID: fmt.Sprintf("c%d", i), Text: strings.Repeat("a criterion's words ", 24)})
	}
	for _, sc := range cands {
		r.compare.Candidates = append(r.compare.Candidates, Candidate{Task: sc.Task})
	}
	r.sel.measured = &aheadMeasure{rev: 3, cmp: longComparison(cands, 12)}

	words := r.tradeoffs(cands[0])
	if len(words) > ledger.MaxOptionTradeoffs || encodedLen(words) > ledger.MaxOptionTradeoffs {
		t.Fatalf("the option is %d bytes", len(words))
	}
	more := regexp.MustCompile(`; and (\d+) more checks: the trial's results have them$`).FindStringSubmatch(words)
	if more == nil {
		t.Fatalf("the option does not say what it leaves out: %q", words)
	}
	clauses := strings.Split(strings.TrimSuffix(strings.TrimPrefix(words, "attempt 1; "), more[0]), "; verify ")
	full := 0
	for i, cl := range clauses {
		if i > 0 {
			cl = "verify " + cl
		}
		// A clause holds its row's every reason, whole.
		if !strings.HasPrefix(cl, "verify verify-step-with-a-long-name-") || !strings.HasSuffix(cl, "approach-a and approach-d differ)") {
			t.Fatalf("clause %d is not whole: %q", i, cl)
		}
		full++
	}
	if n, _ := strconv.Atoi(more[1]); full+n != 12 || full == 0 {
		t.Fatalf("%d clauses and %s more, of 12", full, more[1])
	}

	ctx := r.selectionContext(cands)
	if n := utf8.RuneCountInString(ctx); n > ledger.MaxDecisionContext {
		t.Fatalf("the context is %d characters", n)
	}
	if !regexp.MustCompile(`and \d+ more criteria: the trial's results have them\.`).MatchString(ctx) {
		t.Fatalf("the context does not say which criteria it leaves out: %q", ctx)
	}
	m := regexp.MustCompile(` And (\d+) more candidates?: the trial's results have (them|it)\.$`).FindStringSubmatch(ctx)
	if m == nil {
		t.Fatalf("the context does not say which candidates it leaves out: %q", ctx)
	}
	// Each candidate it keeps is its whole sentence: the option's words.
	shown := 0
	for _, sc := range cands {
		if strings.Contains(ctx, " "+sc.Task+": ") {
			if !strings.Contains(ctx, " "+sc.Task+": "+sc.Commit[:7]+", "+r.tradeoffs(sc)+".") {
				t.Fatalf("%s is not whole in %q", sc.Task, ctx)
			}
			shown++
		}
	}
	if n, _ := strconv.Atoi(m[1]); shown+n != len(cands) || shown == 0 {
		t.Fatalf("%d candidates shown and %s more, of %d", shown, m[1], len(cands))
	}
}
