package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The prepared failed-check request and Check again (docs/plans/
// review-repair.md, RV1 PR-3): a failed receipt read as a draft note of
// kind check, with the criteria and whether Check again would be taken
// now; Check again sent to the run of the chain the candidate is in.

// failingLaunch launches CND-1 with build's check failing until mark
// exists (a file outside the repository the test owns): each attempt
// commits a file of its own and submits it, until every attempt is spent
// and the task fails. It returns the run, the mark and each attempt's
// commit, once the ledger has attempt 3 failed and the task failed.
func failingLaunch(t *testing.T) (l *launchTest, run, mark string, commits []string) {
	t.Helper()
	mark = filepath.Join(t.TempDir(), "passes")
	l = newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Tasks[0].Checks = []crew.Check{{ID: "test", Argv: []string{"sh", "-c", "test -e " + mark}}}
	})
	run = entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	commits = spendBuild(t, l.testEnv, run, "built.txt")
	return l, run, mark, commits
}

// spendBuild has each of build's attempts commit the file, its content
// its own, and submit it, until the task fails with every attempt spent
// and the run, finished failed, has ended its trees; it returns each
// attempt's commit.
func spendBuild(t *testing.T, e *testEnv, run, file string) []string {
	t.Helper()
	var commits []string
	for n := 1; n <= 3; n++ {
		a := runningAttempt(t, e, run, "build", n)
		commits = append(commits, commitFile(t, a["worktree"].(string), file, strings.Repeat("x\n", n)))
		sid := a["sessionId"].(string)
		if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result: %d %v", resp.StatusCode, out)
		}
		ledgerAttemptIn(t, e.srv, run, "build", n, ledger.AttemptFailed)
	}
	taskVia(t, e, run, "build", "failed", func(task map[string]any) bool { return task["state"] == ledger.TaskFailed })
	waitUntil(t, "the run finished failed, its trees ended", func() bool {
		r := runOf(t, e, run)
		return r["outcome"] == ledger.OutcomeFailed && r["cleaning"] == nil
	})
	return commits
}

// receiptK is the k of the attempt's receipt of the step id, from the
// ledger.
func receiptK(t *testing.T, e *testEnv, run string, n int, id string) ledger.CheckRow {
	t.Helper()
	rows, err := e.srv.ledger.Checks(context.Background(), run, "build", n, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range slices.Backward(rows) {
		if r.ID == id && r.State == ledger.CheckFinished && !receiptPassed(r) {
			return r
		}
	}
	t.Fatalf("attempt %d has no failed receipt of %s: %+v", n, id, rows)
	return ledger.CheckRow{}
}

// repairPath is the prepared request's route of a receipt.
func repairPath(run, task string, n int, id string, k int) string {
	return fmt.Sprintf("/api/runs/%s/tasks/%s/attempts/%d/steps/%s/%d/repair", run, task, n, id, k)
}

// prepared reads the prepared request of a receipt, as the admin, 200.
func prepared(t *testing.T, e *testEnv, run, task string, n int, id string, k int) preparedRepair {
	t.Helper()
	status, data, _ := e.call(t, "GET", repairPath(run, task, n, id, k), nil)
	if status != http.StatusOK {
		t.Fatalf("the prepared request: %d %.300s", status, data)
	}
	var out preparedRepair
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("%v: %.300s", err, data)
	}
	return out
}

// recheckOn sends Check again of the candidate to the run's route, as the
// admin, and answers the status and the reply.
func recheckOn(t *testing.T, e *testEnv, run string, body map[string]any) (int, map[string]any) {
	t.Helper()
	status, _, out := e.call(t, "POST", "/api/runs/"+run+"/tasks/build/recheck", body)
	return status, out
}

// The prepared request of a failed check names the receipt (its identity,
// the commit it tested, its argv, how it ended, where, its log's hash as
// the step log route answers it, its tail, quoted), the class and the
// person's choice set to it, the attempt before, and the criteria the run
// was launched with: the ticket's acceptance, its plan revision and the
// ticket. A receipt that passed, one still running, a cleanup step and a
// receipt the ledger has not are refused.
func TestPrepareRepairNamesTheReceiptAndTheCriteria(t *testing.T) {
	l, run, mark, commits := failingLaunch(t)
	rc := receiptK(t, l.testEnv, run, 3, "test")
	got := prepared(t, l.testEnv, run, "build", 3, "test", rc.K)
	resp, _ := l.do("GET", fmt.Sprintf("/api/runs/%s/tasks/build/attempts/3/steps/test/%d/log", run, rc.K), adminToken, nil)
	logSHA := resp.Header.Get(logHashHeader)
	exit := 1
	want := dispatch.CheckRef{Source: "ledger", Receipt: fmt.Sprintf("%s/build/3/check/test/%d", run, rc.K), Run: run, Task: "build", Attempt: 3, Phase: ledger.PhaseCheck,
		ID: "test", K: rc.K, Subject: commits[2], Argv: []string{"sh", "-c", "test -e " + mark}, Exit: &exit, State: ledger.CheckFinished, Stable: true,
		Error: rc.Error, Where: ledger.WhereNative, LogSHA256: logSHA, LogBytes: rc.LogBytes, Tail: rc.Tail, Class: crew.FailureCode, Chosen: crew.FailureCode, Quote: rc.Tail,
		Previous: &dispatch.CheckPrevious{Attempt: 2, State: ledger.AttemptFailed, Stage: "check test", Commit: commits[1]}}
	if logSHA == "" || !reflect.DeepEqual(got.Finding.Check, want) {
		t.Fatalf("the finding's receipt:\n%+v\nwant\n%+v", got.Finding.Check, want)
	}
	if got.Finding.Kind != "check" || got.Finding.Intent != "change" || !strings.Contains(got.Finding.Message, "exited 1") || !strings.Contains(got.Finding.Message, commits[2][:7]) {
		t.Fatalf("the finding: %+v", got.Finding)
	}
	if got.Criteria.Source != "ticket" || got.Criteria.Acceptance != "It does not." || got.Criteria.Checks != nil ||
		!reflect.DeepEqual(got.Plan, &repairPlan{ID: l.plan, N: 1}) || !reflect.DeepEqual(got.Ticket, &repairTicket{ID: l.ticketID, Key: "CND-1"}) {
		t.Fatalf("the criteria: %+v %+v %+v", got.Criteria, got.Plan, got.Ticket)
	}
	// Attempt 1 has none before it.
	if first := prepared(t, l.testEnv, run, "build", 1, "test", receiptK(t, l.testEnv, run, 1, "test").K); first.Finding.Check.Previous != nil || first.Finding.Check.Subject != commits[0] {
		t.Fatalf("attempt 1's: %+v", first.Finding.Check)
	}
	// Refused: a receipt that passed, one running, a cleanup step's, one the ledger has not.
	if _, err := l.srv.ledger.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		for _, r := range []ledger.CheckRow{
			{TaskID: "build", N: 3, ID: "ok", K: 90, Subject: commits[2], State: ledger.CheckFinished, Exit: 0, Stable: true},
			{TaskID: "build", N: 3, ID: "slow", K: 91, Subject: commits[2], State: ledger.CheckRunning},
			{TaskID: "build", N: 3, ID: "tidy", K: 92, Phase: ledger.PhaseCleanup, Subject: commits[2], State: ledger.CheckFinished, Exit: 1},
		} {
			r.Argv, r.Where, r.StartedAt = []string{"true"}, ledger.WhereNative, tx.Now()
			if err := tx.PutCheck(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id     string
		k      int
		status int
		code   string
	}{{"ok", 90, http.StatusConflict, "task_state"}, {"slow", 91, http.StatusConflict, "task_state"}, {"tidy", 92, http.StatusConflict, "task_state"},
		{"test", 99, http.StatusNotFound, "not_found"}} {
		l.expect(t, "GET", repairPath(run, "build", 3, c.id, c.k), nil, c.status, c.code)
	}
	l.expect(t, "GET", repairPath(run, "build", 7, "test", 1), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", repairPath("nope-0000abcd", "build", 3, "test", 1), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", repairPath(run, "build", 3, "test", rc.K)+"?x=1", nil, http.StatusBadRequest, "invalid_request")
	l.expect(t, "GET", repairPath(run, "build", 3, "test", 0), nil, http.StatusBadRequest, "invalid_request")
}

// synthRun writes a run as the ledger alone has it: its crew of one work
// task build (with the checks given) in the repository, its package
// (none for ""), continuing from (none for ""), its task failed; and the
// attempts and receipts given, every one where it ran native.
func synthRun(t *testing.T, ld *ledger.Ledger, id, repo, base, pkg, from string, checks []crew.Check, attempts []ledger.AttemptRow, receipts []ledger.CheckRow) {
	t.Helper()
	c, err := json.Marshal(crew.Crew{ID: "synth", Name: "Synth", Cwd: repo, Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Checks: checks}}})
	if err != nil {
		t.Fatal(err)
	}
	putLedgerRun(t, ld, ledger.RunRow{ID: id, CrewID: "synth", Crew: string(c), Base: base, State: ledger.RunFinished, Package: pkg, ContinuedFrom: from})
	if _, err := ld.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskFailed, Attempts: len(attempts)}); err != nil {
			return err
		}
		for _, a := range attempts {
			a.Member, a.Where = "lead", ledger.WhereNative
			if a.TaskID == "" {
				a.TaskID = "build"
			}
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		for _, r := range receipts {
			r.Where, r.StartedAt = ledger.WhereNative, tx.Now()
			if r.TaskID == "" {
				r.TaskID = "build"
			}
			if err := tx.PutCheck(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The class is the engine's (crew.ClassifyFailure) over the receipt's own
// row, its own attempt and the same check's receipts on the same commit in
// the run's chain: a code failure, one that never ran, a log dropped with
// no tail, a cancelled attempt's, and one that passed on the same commit
// in the continuation (flaky). Each is classed the same after a restart.
func TestPrepareRepairClassifies(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	cm := func(n int) string { return fmt.Sprintf("%040x", n) }
	argv := []string{"go", "test", "./..."}
	finished := func(n, k, exit int, subject string) ledger.CheckRow {
		return ledger.CheckRow{N: n, ID: "test", K: k, Argv: argv, Subject: subject, State: ledger.CheckFinished, Exit: exit, Stable: true, Tail: "FAIL\n", LogSHA256: strings.Repeat("c", 64)}
	}
	never := finished(2, 1, -1, cm(2))
	never.Error, never.Tail, never.LogSHA256 = "could not start go: no such file", "", ""
	dropped := finished(3, 1, 1, cm(3))
	dropped.LogDropped, dropped.Tail = true, ""
	cancelled := finished(4, 1, 1, cm(4))
	cancelled.State, cancelled.Exit = ledger.CheckInterrupted, 0
	failed := func(n int, state, stage string) ledger.AttemptRow {
		return ledger.AttemptRow{N: n, StartCommit: cm(n - 1), ResultCommit: cm(n), State: state, Stage: stage, Result: `{"commit":"` + cm(n) + `"}`}
	}
	synthRun(t, l.srv.ledger, "synth-00000001", l.repo, cm(0), "", "", []crew.Check{{ID: "test", Argv: argv}},
		[]ledger.AttemptRow{failed(1, ledger.AttemptFailed, "check test"), failed(2, ledger.AttemptFailed, "check test"), failed(3, ledger.AttemptFailed, "check test"),
			failed(4, ledger.AttemptCancelled, ""), failed(5, ledger.AttemptFailed, "check test")},
		[]ledger.CheckRow{finished(1, 1, 1, cm(1)), never, dropped, cancelled, finished(5, 1, 1, cm(5))})
	// The continuation checked attempt 5's commit again, and it passed.
	synthRun(t, l.srv.ledger, "synth-00000002", l.repo, cm(0), "", "synth-00000001", []crew.Check{{ID: "test", Argv: argv}},
		[]ledger.AttemptRow{{N: 6, StartCommit: cm(4), ResultCommit: cm(5), State: ledger.AttemptSucceeded}}, []ledger.CheckRow{finished(6, 1, 0, cm(5))})
	want := map[int]string{1: crew.FailureCode, 2: crew.FailureInfrastructure, 3: crew.FailureUnavailable, 4: crew.FailureCancelled, 5: crew.FailureFlaky}
	classes := func(e *testEnv) map[int]string {
		out := map[int]string{}
		for n := 1; n <= 5; n++ {
			p := prepared(t, e, "synth-00000001", "build", n, "test", 1)
			if p.Finding.Check.Class != p.Finding.Check.Chosen || p.Criteria.Source != "checks" || !reflect.DeepEqual(p.Criteria.Checks, []crew.Check{{ID: "test", Argv: argv}}) || p.Plan != nil || p.Ticket != nil {
				t.Fatalf("attempt %d: %+v", n, p)
			}
			// The engine's class over the ledger's rows.
			attempts, _ := e.srv.ledger.Attempts(context.Background(), "synth-00000001")
			rows, _ := e.srv.ledger.Checks(context.Background(), "synth-00000001", "build", n, false)
			same, _ := e.srv.ledger.ChecksOn(context.Background(), []string{"synth-00000001", "synth-00000002"}, "build", ledger.PhaseCheck, "test", cm(n))
			i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.N == n })
			rows[0].RunID, rows[0].TaskID, rows[0].N = "synth-00000001", "build", n
			if f := crew.ClassifyFailure(attempts[i], rows[0], same); f.Class != p.Finding.Check.Class || !strings.Contains(p.Finding.Message, f.Words[:min(40, len(f.Words))]) {
				t.Fatalf("attempt %d: the engine says %+v, the request %q %q", n, f, p.Finding.Check.Class, p.Finding.Message)
			}
			// Not held: Check again waits for a Continue.
			if p.Recheck.Allowed || p.Recheck.Run != "synth-00000002" || p.Recheck.Why != "continue run synth-00000002 first" {
				t.Fatalf("attempt %d's recheck: %+v", n, p.Recheck)
			}
			out[n] = p.Finding.Check.Class
		}
		return out
	}
	if got := classes(l.testEnv); !reflect.DeepEqual(got, want) {
		t.Fatalf("classes %v, want %v", got, want)
	}
	again := l.restartAfterStop(t, entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string))
	if got := classes(again); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a restart, classes %v, want %v", got, want)
	}
}

// The prepared request at its bounds is whole and under MaxRepairReply: a
// failing check's argv of 8 KiB of control characters, its tail of 2 KiB
// of '<' (and words past their bound), once beside a package's acceptance
// at 16 KiB of '<' and U+2028, once beside eight checks' argv at their
// worst as the criteria.
func TestPrepareRepairBound(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	cm := func(n int) string { return fmt.Sprintf("%040x", n) }
	worstArgv := func(c byte) []string {
		out := make([]string, 32)
		for i := range out {
			out[i] = strings.Repeat(string(c), 8<<10/32)
		}
		return out
	}
	var checks []crew.Check
	for i := range 8 {
		checks = append(checks, crew.Check{ID: fmt.Sprintf("c%039d", i), Argv: worstArgv(1), TimeoutSeconds: 3600})
	}
	receipt := ledger.CheckRow{N: 1, ID: checks[0].ID, K: 1, Argv: worstArgv(2), Subject: cm(1), State: ledger.CheckFinished, Exit: 1, Signal: "",
		Tail: strings.Repeat("<", dispatch.MaxCheckExcerpt), Error: strings.Repeat(" ", ledger.MaxPayload/3), LogSHA256: strings.Repeat("d", 64)}
	attempt := ledger.AttemptRow{N: 1, StartCommit: cm(0), ResultCommit: cm(1), State: ledger.AttemptFailed, Stage: "check " + checks[0].ID, Error: strings.Repeat(" ", ledger.MaxPayload/3)}
	acceptance := strings.Repeat("<", dispatch.MaxAcceptance-3) + " "
	pkg, err := json.Marshal(dispatch.Package{Version: dispatch.PackageVersion, Ticket: dispatch.PackageTicket{ID: strings.Repeat("e", 16), Key: "CND-99999", Acceptance: acceptance},
		Plan: dispatch.PackagePlan{ID: strings.Repeat("f", 16), N: 999}})
	if err != nil {
		t.Fatal(err)
	}
	synthRun(t, l.srv.ledger, "synth-0000b001", l.repo, cm(0), string(pkg), "", checks, []ledger.AttemptRow{attempt}, []ledger.CheckRow{receipt})
	synthRun(t, l.srv.ledger, "synth-0000b002", l.repo, cm(0), "", "", checks, []ledger.AttemptRow{attempt}, []ledger.CheckRow{receipt})
	for _, run := range []string{"synth-0000b001", "synth-0000b002"} {
		status, data, _ := l.call(t, "GET", repairPath(run, "build", 1, checks[0].ID, 1), nil)
		if status != http.StatusOK || len(data) > MaxRepairReply {
			t.Fatalf("%s: %d, %d bytes", run, status, len(data))
		}
		var p preparedRepair
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		ref := p.Finding.Check
		if !slices.Equal(ref.Argv, receipt.Argv) || ref.Tail != receipt.Tail || ref.Quote != receipt.Tail || len(ref.Error) > dispatch.MaxCheckError ||
			len([]rune(p.Finding.Message)) > dispatch.MaxFindingMessage {
			t.Fatalf("%s: the receipt was cut: argv %d, tail %d, error %d, words %d", run, len(ref.Argv), len(ref.Tail), len(ref.Error), len([]rune(p.Finding.Message)))
		}
		if run == "synth-0000b001" && (p.Criteria.Source != "ticket" || p.Criteria.Acceptance != acceptance) {
			t.Fatalf("%s: the criteria: %s %d", run, p.Criteria.Source, len(p.Criteria.Acceptance))
		}
		if run == "synth-0000b002" && (p.Criteria.Source != "checks" || !reflect.DeepEqual(p.Criteria.Checks, checks)) {
			t.Fatalf("%s: the criteria: %s %d", run, p.Criteria.Source, len(p.Criteria.Checks))
		}
		t.Logf("%s: %d bytes of %d", run, len(data), MaxRepairReply)
	}
}

// Whether Check again would be taken now, as the prepared request says:
// a code failure of the task's standing failed candidate, yes, on its run
// at its revision; a candidate the task moved on from, no, with the
// engine's words; a checkout's setup failure, yes; a run the server let
// go, no: "continue run X first".
func TestPrepareRepairSaysWhetherCheckAgainIsTaken(t *testing.T) {
	l, run, _, commits := failingLaunch(t)
	p := prepared(t, l.testEnv, run, "build", 3, "test", receiptK(t, l.testEnv, run, 3, "test").K)
	if p.Recheck != (recheckOffer{Allowed: true, Run: run, Revision: int64(runOf(t, l.testEnv, run)["managed"].(map[string]any)["revision"].(float64)), Attempt: 3, Commit: commits[2]}) {
		t.Fatalf("a code failure: %+v", p.Recheck)
	}
	moved := prepared(t, l.testEnv, run, "build", 2, "test", receiptK(t, l.testEnv, run, 2, "test").K)
	if moved.Recheck.Allowed || moved.Recheck.Commit != commits[1] || !strings.Contains(moved.Recheck.Why, "attempt 3 failed since") {
		t.Fatalf("a candidate moved on from: %+v", moved.Recheck)
	}
	// A run the server let go.
	again := l.restartAfterStop(t, run)
	gone := prepared(t, again, run, "build", 3, "test", receiptK(t, again, run, 3, "test").K)
	if gone.Recheck.Allowed || gone.Recheck.Run != run || gone.Recheck.Why != "continue run "+run+" first" || gone.Recheck.Revision == 0 {
		t.Fatalf("a run let go: %+v", gone.Recheck)
	}

	// A checkout's setup that fails while a result holds broken.txt.
	mark := filepath.Join(t.TempDir(), "fixed")
	s := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Setup = []crew.Check{{ID: "deps", Argv: []string{"sh", "-c", "test -e " + mark + " || test ! -e broken.txt"}}}
	})
	srun := entityOf(t, s.launch(t, s.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	spendBuild(t, s.testEnv, srun, "broken.txt")
	setup := receiptK(t, s.testEnv, srun, 3, "deps")
	sp := prepared(t, s.testEnv, srun, "build", 3, "deps", setup.K)
	if !sp.Recheck.Allowed || sp.Finding.Check.Phase != ledger.PhaseSetup || sp.Finding.Check.Class != crew.FailureInfrastructure {
		t.Fatalf("a checkout's setup failure: %+v %s %s", sp.Recheck, sp.Finding.Check.Phase, sp.Finding.Check.Class)
	}
}

// Check again through its route: bound to the candidate named (one the
// task moved on from is a recorded stale_candidate refusal), at the
// revision the prepared request read; taken, the same commit is checked
// again as attempt 4, which passes once the mark is made; the same command
// again answers the same receipt, another content under its id is a
// conflict; a body that is not one, and a run the ledger has not, are
// refused.
func TestRecheckRoute(t *testing.T) {
	l, run, mark, commits := failingLaunch(t)
	status, out := recheckOn(t, l.testEnv, run, map[string]any{"commandId": "old", "attempt": 2, "commit": commits[1]})
	if rc := receiptOf(t, out); status != http.StatusConflict || errorCode(out) != crew.RefusalStaleCandidate || rc["status"] != ledger.CommandFailed || out["run"] != run {
		t.Fatalf("a candidate moved on from: %d %v", status, out)
	}
	k := receiptK(t, l.testEnv, run, 3, "test").K
	p := prepared(t, l.testEnv, run, "build", 3, "test", k)
	if !p.Recheck.Allowed {
		t.Fatalf("the prepared request: %+v", p.Recheck)
	}
	mustMark(t, mark)
	body := map[string]any{"commandId": "again", "attempt": 3, "commit": commits[2], "expectedRevision": p.Recheck.Revision}
	status, out = recheckOn(t, l.testEnv, run, body)
	if rc := receiptOf(t, out); status != http.StatusOK || rc["status"] != ledger.CommandDone || rc["intent"] != "recheck" || out["run"] != run || out["attempt"] != 4.0 {
		t.Fatalf("Check again: %d %v", status, out)
	}
	if row := ledgerAttemptIn(t, l.srv, run, "build", 4, ledger.AttemptSucceeded); row.ResultCommit != commits[2] || row.SessionID != "" {
		t.Fatalf("attempt 4: %+v", row)
	}
	taskVia(t, l.testEnv, run, "build", "succeeded at attempt 4", func(task map[string]any) bool {
		r, _ := task["result"].(map[string]any)
		return task["state"] == ledger.TaskSucceeded && r["attempt"] == 4.0 && r["commit"] == commits[2]
	})
	status, again := recheckOn(t, l.testEnv, run, body)
	if rc := receiptOf(t, again); status != http.StatusOK || rc["repeat"] != true || rc["status"] != ledger.CommandDone || again["attempt"] != 4.0 || again["run"] != run {
		t.Fatalf("the same command again: %d %v", status, again)
	}
	recheckBad := func(b map[string]any, status int, code string) {
		t.Helper()
		if got, out := recheckOn(t, l.testEnv, run, b); got != status || errorCode(out) != code {
			t.Fatalf("%v: %d %v", b, got, out)
		}
	}
	recheckBad(map[string]any{"commandId": "again", "attempt": 2, "commit": commits[1]}, http.StatusConflict, "command_conflict")
	recheckBad(map[string]any{"commandId": "x1", "attempt": 0, "commit": commits[2]}, http.StatusBadRequest, "invalid_request")
	recheckBad(map[string]any{"commandId": "x1", "attempt": 3, "commit": commits[2][:12]}, http.StatusBadRequest, "invalid_request")
	recheckBad(map[string]any{"commandId": "", "attempt": 3, "commit": commits[2]}, http.StatusBadRequest, "invalid_request")
	recheckBad(map[string]any{"commandId": "x1", "attempt": 3, "commit": commits[2], "task": "build"}, http.StatusBadRequest, "invalid_request")
	recheckBad(map[string]any{"commandId": "x1", "attempt": 3, "commit": commits[2], "expectedRevision": -1}, http.StatusBadRequest, "invalid_request")
	if got, out := recheckOn(t, l.testEnv, "nope-0000abcd", body); got != http.StatusNotFound || errorCode(out) != "not_found" {
		t.Fatalf("an unknown run: %d %v", got, out)
	}
	// A succeeded task is checked again no more.
	if got, out := recheckOn(t, l.testEnv, run, map[string]any{"commandId": "late", "attempt": 4, "commit": commits[2]}); got != http.StatusConflict || errorCode(out) != crew.RefusalTaskState {
		t.Fatalf("a succeeded task: %d %v", got, out)
	}
}

// mustMark makes the file a marked check waits for.
func mustMark(t *testing.T, mark string) {
	t.Helper()
	if err := os.WriteFile(mark, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Run A's task failed and A was let go: its receipt's prepared request
// says Continue first, and Check again through A's route is refused with
// those words, nothing checked. Once A is continued as B, the prepared
// request says yes, in B; Check again posted with A's candidate checks it
// in B and answers B and the attempt there; the same command again answers
// the same receipt from B.
func TestRecheckAfterContinueThroughTheRoute(t *testing.T) {
	l, runA, mark, commits := failingLaunch(t)
	e := l.restartAfterStop(t, runA)
	k := receiptK(t, e, runA, 3, "test").K
	if p := prepared(t, e, runA, "build", 3, "test", k); p.Recheck.Allowed || p.Recheck.Run != runA || p.Recheck.Why != "continue run "+runA+" first" {
		t.Fatalf("before the Continue: %+v", p.Recheck)
	}
	body := map[string]any{"commandId": "again", "attempt": 3, "commit": commits[2]}
	if status, out := recheckOn(t, e, runA, body); status != http.StatusConflict || errorCode(out) != crew.RefusalTaskState || !strings.Contains(apiMessage(out), "continue run "+runA+" first") {
		t.Fatalf("Check again before the Continue: %d %v", status, out)
	}
	resp, out := e.do("POST", "/api/runs/"+runA+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("continue: %d %v", resp.StatusCode, out)
	}
	runB := out["run"].(map[string]any)["id"].(string)
	taskVia(t, e, runB, "build", "failed, carried", func(task map[string]any) bool { return task["state"] == ledger.TaskFailed })
	waitUntil(t, "A names B", func() bool {
		p, ok, err := e.srv.ledger.Pointer(context.Background(), runA)
		return err == nil && ok && p.ContinuedBy == runB
	})
	p := prepared(t, e, runA, "build", 3, "test", k)
	if !p.Recheck.Allowed || p.Recheck.Run != runB || p.Recheck.Attempt != 3 || p.Recheck.Commit != commits[2] {
		t.Fatalf("after the Continue: %+v", p.Recheck)
	}
	mustMark(t, mark)
	body["expectedRevision"] = p.Recheck.Revision
	status, out := recheckOn(t, e, runA, body)
	if rc := receiptOf(t, out); status != http.StatusOK || rc["status"] != ledger.CommandDone || out["run"] != runB || out["attempt"] != 4.0 {
		t.Fatalf("Check again in the continuation: %d %v", status, out)
	}
	if row := ledgerAttemptIn(t, e.srv, runB, "build", 4, ledger.AttemptSucceeded); row.ResultCommit != commits[2] {
		t.Fatalf("B's attempt 4: %+v", row)
	}
	status, again := recheckOn(t, e, runA, body)
	if rc := receiptOf(t, again); status != http.StatusOK || rc["repeat"] != true || again["run"] != runB || again["attempt"] != 4.0 {
		t.Fatalf("the same command again: %d %v", status, again)
	}
	// A's own receipts never changed: attempt 3 still failed, its check's row as it was.
	if rows, err := e.srv.ledger.Checks(context.Background(), runA, "build", 3, false); err != nil || len(rows) == 0 || receiptPassed(rows[len(rows)-1]) {
		t.Fatalf("A's attempt 3's receipts: %+v %v", rows, err)
	}
}

// A candidate checked again after failures is verified for a delivery as
// its own attempt: attempt 4, checked again and failing, is not; attempt
// 5, checked again once the mark is made, is, with its own receipts; the
// failed attempts before stay unverified.
func TestRecheckedCandidateIsVerifiedForADelivery(t *testing.T) {
	l, run, mark, commits := failingLaunch(t)
	c3 := commits[2]
	if status, out := recheckOn(t, l.testEnv, run, map[string]any{"commandId": "r4", "attempt": 3, "commit": c3}); status != http.StatusOK || out["attempt"] != 4.0 {
		t.Fatalf("Check again, failing: %d %v", status, out)
	}
	ledgerAttemptIn(t, l.srv, run, "build", 4, ledger.AttemptFailed)
	taskVia(t, l.testEnv, run, "build", "failed at attempt 4", func(task map[string]any) bool {
		a, _ := task["attempt"].(map[string]any)
		return task["state"] == ledger.TaskFailed && a != nil && a["n"] == 4.0
	})
	mustMark(t, mark)
	if status, out := recheckOn(t, l.testEnv, run, map[string]any{"commandId": "r5", "attempt": 4, "commit": c3}); status != http.StatusOK || out["attempt"] != 5.0 {
		t.Fatalf("Check again, passing: %d %v", status, out)
	}
	ledgerAttemptIn(t, l.srv, run, "build", 5, ledger.AttemptSucceeded)
	for n, want := range map[int]bool{3: false, 4: false, 5: true} {
		v, code, words, err := l.srv.candidateVerified(context.Background(), dispatch.Candidate{Run: run, Task: "build", Attempt: n, Commit: c3})
		if err != nil || code != "" || v == nil || v.Passed != want || v.Attempt != n || len(v.Receipts) != 1 {
			t.Fatalf("attempt %d: %+v %q %q %v", n, v, code, words, err)
		}
	}
}
