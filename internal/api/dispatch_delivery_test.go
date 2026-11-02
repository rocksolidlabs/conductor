package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// deliveryCommit is the nth commit of a delivery test's run, in full.
func deliveryCommit(n int) string { return fmt.Sprintf("%040x", n) }

// deliveryRun puts a managed run in the ledger as a server before this
// one left it, its record saved so a hand link can name it: a work task
// build and a review rv. build's attempt 1 failed its check test on its
// result; attempt 2 succeeded, its check test and its verify step lint
// passing on its result, lint's leftover child killed after it exited 0
// (Killed, no failure of the step), a setup step's row, and a failing
// row of another commit, beside them, neither the candidate's; attempt 3
// failed its check and was accepted anyway; attempt 4 succeeded with no
// check at all; attempt 5 passed its check and was stopped before it
// succeeded; attempt 6 passed its check and is still verifying, attempt
// 7 still submitting, both with their result.
// rv's attempt 1 reviewed attempt 2's result. A from not "" is the
// run it continues, marked continued by it in the ledger.
func deliveryRun(t *testing.T, e *testEnv, run, from string) {
	t.Helper()
	c := crew.Crew{ID: strings.Split(run, "-")[0], Name: "Delivered", Members: []crew.Member{{Name: "lead", AgentID: "cat"}},
		Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}, {ID: "rv", Member: "lead", Kind: crew.TaskReview, Needs: []string{"build"}}}}
	raw, _ := json.Marshal(c)
	result := func(r crew.Result) string { b, _ := json.Marshal(r); return string(b) }
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: run, CrewID: c.ID, Crew: string(raw), Base: deliveryCommit(0), State: ledger.RunFinished, ContinuedFrom: from, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if from != "" {
			if err := tx.Continued(from); err != nil {
				return err
			}
		}
		if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 7, ResultAttempt: 2, ResultCommit: deliveryCommit(2)}); err != nil {
			return err
		}
		if err := tx.PutTask(ledger.TaskRow{ID: "rv", Member: "lead", Kind: crew.TaskReview, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1}); err != nil {
			return err
		}
		for n, state := range map[int]string{1: ledger.AttemptFailed, 2: ledger.AttemptSucceeded, 3: ledger.AttemptFailed, 4: ledger.AttemptSucceeded, 5: ledger.AttemptStopped,
			6: ledger.AttemptVerifying, 7: ledger.AttemptSubmitting} {
			a := ledger.AttemptRow{TaskID: "build", N: n, Member: "lead", StartCommit: deliveryCommit(n - 1), ResultCommit: deliveryCommit(n), State: state, Result: result(crew.Result{Commit: deliveryCommit(n)})}
			if state == ledger.AttemptFailed {
				a.Stage = "check"
			}
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		if err := tx.PutAttempt(ledger.AttemptRow{TaskID: "rv", N: 1, Member: "lead", StartCommit: deliveryCommit(2), State: ledger.AttemptSucceeded,
			Result: result(crew.Result{Verdict: crew.VerdictApproved})}); err != nil {
			return err
		}
		for _, row := range []ledger.CheckRow{
			{TaskID: "build", N: 1, ID: "test", K: 1, Subject: deliveryCommit(1), State: ledger.CheckFinished, Exit: 1, Stable: true, LogSHA256: strings.Repeat("1", 64)},
			{TaskID: "build", N: 2, ID: "deps", K: 1, Phase: ledger.PhaseSetup, Subject: deliveryCommit(2), State: ledger.CheckFinished, Exit: 1},
			{TaskID: "build", N: 2, ID: "test", K: 2, Subject: deliveryCommit(2), State: ledger.CheckFinished, Exit: 0, Stable: true, LogSHA256: strings.Repeat("2", 64)},
			{TaskID: "build", N: 2, ID: "lint", K: 3, Phase: ledger.PhaseVerify, Subject: deliveryCommit(2), State: ledger.CheckFinished, Exit: 0, Stable: true, Killed: true},
			{TaskID: "build", N: 2, ID: "old", K: 4, Subject: deliveryCommit(1), State: ledger.CheckFinished, Exit: 1},
			{TaskID: "build", N: 3, ID: "test", K: 1, Subject: deliveryCommit(3), State: ledger.CheckFinished, Exit: 2, Stable: true},
			{TaskID: "build", N: 5, ID: "test", K: 1, Subject: deliveryCommit(5), State: ledger.CheckFinished, Exit: 0, Stable: true},
			{TaskID: "build", N: 6, ID: "test", K: 1, Subject: deliveryCommit(6), State: ledger.CheckFinished, Exit: 0, Stable: true},
		} {
			row.StartedAt, row.FinishedAt = at, at.Add(time.Second)
			if err := tx.PutCheck(row); err != nil {
				return err
			}
		}
		if _, _, err := tx.Command("admin", "acc-1", "accept", "fp"); err != nil {
			return err
		}
		return tx.Settle("admin", "acc-1", ledger.CommandDone, crew.Receipt{CommandID: "acc-1", Intent: "accept", Status: ledger.CommandDone,
			Task: &crew.ReceiptTask{ID: "build", State: ledger.TaskSucceeded, Result: &crew.ReceiptResult{Attempt: 3, Commit: deliveryCommit(3)}}})
	}); err != nil {
		t.Fatal(err)
	}
	saveRecord(t, e.srv, crew.Run{ID: run, CrewID: c.ID, Name: c.Name, StartedAt: at, State: crew.RunStopped})
}

// deliveryEnv is a Dispatch with a ledger: the project CND, its tickets
// CND-1 and CND-2, the run synth-0000abcd (deliveryRun) linked to CND-1
// by hand, and other-0000abcd, a run of the same shape, linked to CND-2.
func deliveryEnv(t *testing.T) *testEnv {
	t.Helper()
	e := dispatchEnv(t)
	if e.srv.ledger == nil {
		t.Fatal("no ledger")
	}
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchTicket(t, "CND", "Delivered", nil)
	e.newDispatchTicket(t, "CND", "Elsewhere", nil)
	for key, run := range map[string]string{"CND-1": "synth-0000abcd", "CND-2": "other-0000abcd"} {
		deliveryRun(t, e, run, "")
		linkRun(t, e, key, run, http.StatusCreated, "")
	}
	return e
}

// deliver adds to the ticket a delivery link to the URL naming the
// candidate under the op id, failing t unless the route answers the
// status and code.
func (e *testEnv) deliver(t *testing.T, key, op, url, candidate string, status int, code string) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": op, "kind": dispatch.LinkDelivery, "url": url, "candidate": candidate}, status, code)
}

// deliveryOf is the link with the id among the ticket's, its recorded
// receipt decoded.
func deliveryOf(t *testing.T, tk map[string]any, id string) (map[string]any, dispatch.Verified) {
	t.Helper()
	for _, l := range listOf(t, tk, "links") {
		if l["id"] != id {
			continue
		}
		raw, _ := json.Marshal(l["verified"])
		var v dispatch.Verified
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("the link's receipt: %v: %s", err, raw)
		}
		return l, v
	}
	t.Fatalf("no link %s in %v", id, tk["links"])
	return nil, dispatch.Verified{}
}

// linkCount is how many live links the ticket has, read from the store.
func linkCount(t *testing.T, e *testEnv, key string) int {
	t.Helper()
	links, err := e.srv.dispatch.Links(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return len(links)
}

// A delivery link names its candidate and keeps its verification receipt
// as the ledger had it: a candidate that passed (its check and its verify
// step on its commit, where they ran, the setup step and another commit's
// row left out), one that failed, one accepted anyway (not verified), one
// with no check (not verified), one whose verify step's leftover child was
// killed after it passed (verified). Refused, nothing kept: a commit that
// is not the attempt's result, a review, an attempt the run has not, an
// attempt still verifying or submitting (its receipt not finished), a run of
// another ticket's (run_linked naming it), a run no ticket holds, a run the
// ledger has not. A candidate not of its shape is the store's refusal, its
// receipt kept. The close policy's verified through the project's edit
// route: done refused with the failed candidate's words, then taken.
func TestDeliveryLinkNamesACandidate(t *testing.T) {
	t.Parallel()
	e := deliveryEnv(t)
	run := "synth-0000abcd"
	cand := func(task string, n, commit int) string {
		return fmt.Sprintf("%s/%s/%d@%s", run, task, n, deliveryCommit(commit))
	}

	// Failed first, so the close policy can be shown unmet by it. The
	// receipt's time is when the route read it.
	asked := time.Now()
	out := e.deliver(t, "CND-1", opID(), "https://example.com/pull/1", cand("build", 1, 1), http.StatusCreated, "")
	answered := time.Now()
	l, v := deliveryOf(t, entityOf(t, out, "ticket"), receiptOf(t, out)["id"].(string))
	if l["candidate"] != cand("build", 1, 1) || v.Passed || v.Attempt != 1 || len(v.Receipts) != 1 || *v.Receipts[0].Exit != 1 || v.Receipts[0].Where != "native" ||
		v.Receipts[0].LogSHA256 != strings.Repeat("1", 64) || v.At.Before(asked) || v.At.After(answered) {
		t.Fatalf("a failed candidate: %v %+v (asked %s, answered %s)", l, v, asked, answered)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "closeRequires": []string{"verified"}}, http.StatusOK, "")
	p := entityOf(t, e.expect(t, "GET", "/api/dispatch/projects/CND", nil, http.StatusOK, ""), "project")
	if fmt.Sprint(p["closeRequires"]) != "[verified]" {
		t.Fatalf("the policy: %v", p["closeRequires"])
	}
	out = e.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}, http.StatusConflict, "close_policy")
	if rc := receiptOf(t, out); fmt.Sprint(rc["missing"]) != "[verified]" || !strings.Contains(rc["error"].(string), "the delivered candidate build#1 failed the check test (exit 1)") {
		t.Fatalf("done refused: %v", rc)
	}

	// Accepted anyway: not verified; no check at all, or an attempt that
	// did not succeed: not verified.
	out = e.deliver(t, "CND-1", opID(), "https://example.com/pull/3", cand("build", 3, 3), http.StatusCreated, "")
	if _, v := deliveryOf(t, entityOf(t, out, "ticket"), receiptOf(t, out)["id"].(string)); v.Passed || len(v.Receipts) != 1 || *v.Receipts[0].Exit != 2 {
		t.Fatalf("a candidate accepted anyway: %+v", v)
	}
	out = e.deliver(t, "CND-1", opID(), "https://example.com/pull/4", cand("build", 4, 4), http.StatusCreated, "")
	tk := entityOf(t, out, "ticket")
	if _, v := deliveryOf(t, tk, receiptOf(t, out)["id"].(string)); v.Passed || len(v.Receipts) != 0 {
		t.Fatalf("a candidate with no check: %+v", v)
	}
	if c := tk["close"].(map[string]any); c["met"].(map[string]any)["verified"] != false || c["verifiedWhy"] != "the delivered candidate build#4 ran no check" {
		t.Fatalf("the close state: %v", c)
	}
	out = e.deliver(t, "CND-1", opID(), "https://example.com/pull/5", cand("build", 5, 5), http.StatusCreated, "")
	tk = entityOf(t, out, "ticket")
	if _, v := deliveryOf(t, tk, receiptOf(t, out)["id"].(string)); v.Passed || len(v.Receipts) != 1 || *v.Receipts[0].Exit != 0 {
		t.Fatalf("a stopped attempt's candidate: %+v", v)
	}
	if c := tk["close"].(map[string]any); c["verifiedWhy"] != "the delivered candidate build#5 passed its checks, but its attempt did not succeed" {
		t.Fatalf("the close state: %v", c)
	}

	// Refused with nothing kept.
	before := linkCount(t, e, "CND-1")
	for _, c := range []struct {
		candidate string
		status    int
		code      string
		words     string
	}{
		{cand("build", 2, 1), http.StatusConflict, "invalid", "build#2's commit is not " + deliveryCommit(1)},
		{cand("rv", 1, 2), http.StatusConflict, "invalid", "is a review"},
		{cand("build", 9, 2), http.StatusConflict, "invalid", "no attempt 9"},
		{cand("build", 6, 6), http.StatusConflict, "invalid", "build#6 has not ended (its attempt is verifying)"},
		{cand("build", 7, 7), http.StatusConflict, "invalid", "build#7 has not ended (its attempt is submitting)"},
		{"other-0000abcd/build/2@" + deliveryCommit(2), http.StatusConflict, "run_linked", "among CND-2's runs"},
		{"loose-0000abcd/build/2@" + deliveryCommit(2), http.StatusConflict, "invalid", "none of CND-1's runs"},
	} {
		op := opID()
		out := e.deliver(t, "CND-1", op, "https://example.com/pull/9", c.candidate, c.status, c.code)
		if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, c.words) || out["receipt"] != nil {
			t.Fatalf("%s: %v", c.candidate, out)
		}
		if c.code == "run_linked" && out["key"] != "CND-2" {
			t.Fatalf("another ticket's run names it: %v", out)
		}
		if _, ok, err := e.srv.dispatch.Receipt(t.Context(), adminActor, op); err != nil || ok {
			t.Fatalf("%s kept a receipt: %v %v", c.candidate, ok, err)
		}
	}
	// A run whose record a hand link names but that the ledger has not.
	saveRecord(t, e.srv, crew.Run{ID: "legacy-0000abcd", StartedAt: time.Now().UTC()})
	e.newDispatchTicket(t, "CND", "Legacy", nil)
	linkRun(t, e, "CND-3", "legacy-0000abcd", http.StatusCreated, "")
	e.deliver(t, "CND-3", opID(), "https://example.com/pull/9", "legacy-0000abcd/build/1@"+deliveryCommit(1), http.StatusConflict, "invalid")
	// The store refuses a candidate not of its shape, its receipt kept.
	out = e.deliver(t, "CND-1", opID(), "https://example.com/pull/9", run+"/build/2@abc", http.StatusConflict, "invalid")
	if rc := receiptOf(t, out); rc["status"] != "failed" {
		t.Fatalf("a bad candidate: %v", out)
	}
	if n := linkCount(t, e, "CND-1"); n != before {
		t.Fatalf("%d links after the refusals, %d before", n, before)
	}

	// The candidate that passed: its receipts the commit's, and done taken.
	out = e.deliver(t, "CND-1", opID(), "https://example.com/pull/2", cand("build", 2, 2), http.StatusCreated, "")
	tk = entityOf(t, out, "ticket")
	_, v = deliveryOf(t, tk, receiptOf(t, out)["id"].(string))
	if !v.Passed || v.Attempt != 2 || len(v.Receipts) != 2 {
		t.Fatalf("a passed candidate: %+v", v)
	}
	// In the ledger's order: by id, then k; lint passed, its child killed.
	for i, want := range []dispatch.VerifiedReceipt{
		{Phase: dispatch.PhaseVerify, ID: "lint", K: 3, Where: "native", Stable: true},
		{Phase: dispatch.PhaseCheck, ID: "test", K: 2, Where: "native", Stable: true, LogSHA256: strings.Repeat("2", 64)},
	} {
		got := v.Receipts[i]
		if got.Phase != want.Phase || got.ID != want.ID || got.K != want.K || got.Where != want.Where || got.Stable != want.Stable ||
			got.LogSHA256 != want.LogSHA256 || got.Exit == nil || *got.Exit != 0 {
			t.Fatalf("receipt %d: %+v, want %+v", i, got, want)
		}
	}
	if c := tk["close"].(map[string]any); c["met"].(map[string]any)["verified"] != true || c["verifiedWhy"] != nil {
		t.Fatalf("the close state: %v", c)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}, http.StatusOK, "")
	// A continuation of the hand-linked run is of its chain: its candidates
	// are the ticket's too.
	deliveryRun(t, e, "synth-0000abce", run)
	e.deliver(t, "CND-1", opID(), "https://example.com/pull/6", "synth-0000abce/build/2@"+deliveryCommit(2), http.StatusCreated, "")
}

// The same opId resent answers its first receipt and the receipt it
// recorded, whatever the ledger holds now and whether it can be read at
// all: after a newer check row of the attempt, after the ledger was
// closed, and once its receipt is pruned; another candidate under that id
// is op_conflict, before and after the prune. A new operation with the
// ledger closed is 503 ledger_unavailable, nothing kept.
func TestDeliveryLinkResentAfterTheEvidenceChanged(t *testing.T) {
	t.Parallel()
	e := deliveryEnv(t)
	run := "synth-0000abcd"
	candidate := run + "/build/2@" + deliveryCommit(2)
	op, url := opID(), "https://example.com/pull/2"
	out := e.deliver(t, "CND-1", op, url, candidate, http.StatusCreated, "")
	id := receiptOf(t, out)["id"].(string)
	first, v := deliveryOf(t, entityOf(t, out, "ticket"), id)
	if !v.Passed {
		t.Fatalf("the first receipt: %+v", v)
	}
	same := func(out map[string]any) {
		t.Helper()
		if rc := receiptOf(t, out); rc["repeat"] != true || rc["id"] != id {
			t.Fatalf("the resend: %v", rc)
		}
		l, _ := deliveryOf(t, entityOf(t, out, "ticket"), id)
		if fmt.Sprint(l["verified"]) != fmt.Sprint(first["verified"]) {
			t.Fatalf("the resend's receipt %v, the first's %v", l["verified"], first["verified"])
		}
	}
	// Newer evidence: the attempt's check run again, failing.
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		return tx.PutCheck(ledger.CheckRow{TaskID: "build", N: 2, ID: "test", K: 9, Subject: deliveryCommit(2), State: ledger.CheckFinished, Exit: 1, StartedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	same(e.deliver(t, "CND-1", op, url, candidate, http.StatusOK, ""))
	e.deliver(t, "CND-1", op, url, run+"/build/4@"+deliveryCommit(4), http.StatusConflict, "op_conflict")
	// The ledger closed: the resend is still answered, nothing read.
	if err := e.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	same(e.deliver(t, "CND-1", op, url, candidate, http.StatusOK, ""))
	before := linkCount(t, e, "CND-1")
	fresh := opID()
	e.deliver(t, "CND-1", fresh, "https://example.com/pull/1", run+"/build/1@"+deliveryCommit(1), http.StatusServiceUnavailable, "ledger_unavailable")
	if _, ok, err := e.srv.dispatch.Receipt(t.Context(), adminActor, fresh); err != nil || ok || linkCount(t, e, "CND-1") != before {
		t.Fatalf("a new operation with no ledger kept something: %v %v", ok, err)
	}
	// Its receipt pruned: the same through the add row and the link's
	// fingerprint.
	db := openRaw(t, filepath.Join(e.srv.dispatch.Dir(), dispatch.File))
	if res, err := db.Exec("DELETE FROM ops WHERE id = ?", op); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("pruned %d receipts", n)
	}
	same(e.deliver(t, "CND-1", op, url, candidate, http.StatusOK, ""))
	e.deliver(t, "CND-1", op, url, run+"/build/4@"+deliveryCommit(4), http.StatusConflict, "op_conflict")
	if n := linkCount(t, e, "CND-1"); n != before {
		t.Fatalf("%d links, %d before", n, before)
	}
}

// A cancel commits the attempt's end before it ends the check under it,
// and the check writes its interrupted receipt, its log's hash with it,
// in a later transaction: between the two the ledger holds an ended
// attempt whose receipt is still started or running. The ledger is held
// in that state here, the check's finalization not yet written: the
// delivery is refused with nothing kept, the same opId too, and once the
// receipts are finished as the check finishes them the same opId is taken
// with the final receipt, its log's hash in it. A stopped attempt whose
// unfinished receipt is past the 16 a delivery keeps is refused alike.
func TestDeliveryWaitsForAnEndedAttemptsReceipts(t *testing.T) {
	t.Parallel()
	e := deliveryEnv(t)
	run := "synth-0000abcd"
	at := time.Now().UTC().Add(-time.Minute)
	put := func(rows ...ledger.CheckRow) {
		t.Helper()
		if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
			for _, row := range rows {
				if err := tx.PutCheck(row); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Attempt 8 cancelled under its check test (running) and its verify
	// step lint (started); attempt 9 stopped with sixteen finished receipts
	// and a seventeenth still running.
	if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
		for n, state := range map[int]string{8: ledger.AttemptCancelled, 9: ledger.AttemptStopped} {
			if err := tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: n, Member: "lead", StartCommit: deliveryCommit(n - 1), ResultCommit: deliveryCommit(n), State: state}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	test := ledger.CheckRow{TaskID: "build", N: 8, ID: "test", K: 1, Subject: deliveryCommit(8), State: ledger.CheckRunning, Exit: -1, StartedAt: at, PID: 4242}
	lint := ledger.CheckRow{TaskID: "build", N: 8, ID: "lint", K: 2, Phase: ledger.PhaseVerify, Subject: deliveryCommit(8), State: ledger.CheckStarted, Exit: -1, StartedAt: at}
	put(test, lint)
	var many []ledger.CheckRow
	for k := 1; k <= 16; k++ {
		many = append(many, ledger.CheckRow{TaskID: "build", N: 9, ID: fmt.Sprintf("a%02d", k), K: k, Subject: deliveryCommit(9), State: ledger.CheckFinished, Exit: 0, Stable: true, StartedAt: at, FinishedAt: at})
	}
	many = append(many, ledger.CheckRow{TaskID: "build", N: 9, ID: "z", K: 17, Subject: deliveryCommit(9), State: ledger.CheckRunning, Exit: -1, StartedAt: at})
	put(many...)

	before := linkCount(t, e, "CND-1")
	refused := func(op, candidate, words string) {
		t.Helper()
		out := e.deliver(t, "CND-1", op, "https://example.com/pull/8", candidate, http.StatusConflict, "invalid")
		if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, words) || out["receipt"] != nil {
			t.Fatalf("%s: %v", candidate, out)
		}
		if _, ok, err := e.srv.dispatch.Receipt(t.Context(), adminActor, op); err != nil || ok {
			t.Fatalf("%s kept a receipt: %v %v", candidate, ok, err)
		}
		if n := linkCount(t, e, "CND-1"); n != before {
			t.Fatalf("%d links after the refusal, %d before", n, before)
		}
	}
	cancelled := run + "/build/8@" + deliveryCommit(8)
	op := opID()
	refused(op, cancelled, "build#8's receipt lint is still started (its attempt is cancelled)")
	refused(opID(), run+"/build/9@"+deliveryCommit(9), "build#9's receipt z is still running (its attempt is stopped)")
	// lint's end written, test's not yet: the same opId still refused.
	lint.State, lint.Exit, lint.FinishedAt, lint.Error = ledger.CheckInterrupted, -1, at.Add(time.Second), "the attempt closed under the check"
	lint.LogSHA256 = strings.Repeat("c", 64)
	put(lint)
	refused(op, cancelled, "build#8's receipt test is still running (its attempt is cancelled)")
	// test's interrupted receipt written, as the check writes it once the
	// cancel ended it: the same opId taken, the final receipts kept.
	test.State, test.Exit, test.Signal, test.FinishedAt, test.Error = ledger.CheckInterrupted, -1, "killed", at.Add(2*time.Second), "the attempt closed under the check"
	test.LogSHA256 = strings.Repeat("d", 64)
	put(test)
	out := e.deliver(t, "CND-1", op, "https://example.com/pull/8", cancelled, http.StatusCreated, "")
	_, v := deliveryOf(t, entityOf(t, out, "ticket"), receiptOf(t, out)["id"].(string))
	if v.Passed || v.Attempt != 8 || len(v.Receipts) != 2 || v.Receipts[0].ID != "lint" || v.Receipts[0].LogSHA256 != strings.Repeat("c", 64) ||
		v.Receipts[1].ID != "test" || v.Receipts[1].LogSHA256 != strings.Repeat("d", 64) {
		t.Fatalf("the cancelled candidate's final receipt: %+v", v)
	}
}

// Two tries of one delivery in flight at once both find no receipt in
// the first replay; the twin that commits first wins. A try that then
// meets a refusal (its run's link removed meanwhile, so the run is none
// of the ticket's) or a ledger it cannot read (closed meanwhile) answers
// the twin's receipt, a repeat with the twin's recorded receipt, never
// its own failure. The seam commits the twin between the first replay
// and the reads that would fail.
func TestDeliveryOverlappingTriesAnswerTheFirst(t *testing.T) {
	t.Parallel()
	run := "synth-0000abcd"
	candidate := run + "/build/2@" + deliveryCommit(2)
	for _, c := range []struct {
		name  string
		after func(t *testing.T, e *testEnv)
	}{
		{"the run's link removed", func(t *testing.T, e *testEnv) {
			links, err := e.srv.dispatch.Links(t.Context(), "CND-1")
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range links {
				if l.Kind == dispatch.LinkRun {
					if rc, err := e.srv.dispatch.RemoveLink(t.Context(), adminOp(""), "CND-1", l.ID); err != nil || rc.Status != dispatch.StatusDone {
						t.Fatalf("the run link's remove: %+v %v", rc, err)
					}
				}
			}
		}},
		{"the ledger closed", func(t *testing.T, e *testEnv) {
			if err := e.srv.ledger.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := deliveryEnv(t)
			twin := &dispatch.Verified{At: time.Now().UTC(), Passed: true, Attempt: 2, Receipts: []dispatch.VerifiedReceipt{
				{Phase: dispatch.PhaseCheck, ID: "test", K: 2, Where: "native", Exit: new(int), Stable: true}}}
			var twinID string
			e.srv.deliveryReplayed = func(ctx context.Context, op dispatch.Op, key string, l dispatch.NewLink) {
				rc, err := e.srv.dispatch.AddLink(ctx, op, key, l, twin)
				if err != nil || rc.Status != dispatch.StatusDone {
					t.Errorf("the twin: %+v %v", rc, err)
					return
				}
				twinID = rc.ID
				c.after(t, e)
			}
			op := opID()
			out := e.deliver(t, "CND-1", op, "https://example.com/pull/2", candidate, http.StatusOK, "")
			if rc := receiptOf(t, out); rc["repeat"] != true || rc["id"] != twinID || twinID == "" {
				t.Fatalf("the second try: %v, the twin's %s", rc, twinID)
			}
			if _, v := deliveryOf(t, entityOf(t, out, "ticket"), twinID); !v.Passed || !v.At.Equal(twin.At) {
				t.Fatalf("the receipt answered %+v, the twin's %+v", v, twin)
			}
		})
	}
}

// Two tickets holding one chain by hand links (X on CND-1, Y on CND-2,
// linked while unrelated, as a version 1 store let them, and Y then found
// to continue X): a delivery naming a candidate of either run, from either
// ticket, is refused run_linked naming both, nothing kept, the ledger
// never read.
func TestDeliveryRefusedForAContestedChain(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchTicket(t, "CND", "T1", nil)
	e.newDispatchTicket(t, "CND", "T2", nil)
	now := time.Now().UTC()
	x, y := "legacy-000000c1", "legacy-000000c2"
	saveRecord(t, e.srv, crew.Run{ID: x, StartedAt: now.Add(-2 * time.Hour)})
	saveRecord(t, e.srv, crew.Run{ID: y, StartedAt: now.Add(-time.Hour)})
	linkRun(t, e, "CND-1", x, http.StatusCreated, "")
	linkRun(t, e, "CND-2", y, http.StatusCreated, "")
	saveRecord(t, e.srv, crew.Run{ID: x, StartedAt: now.Add(-2 * time.Hour), ResumedBy: y})
	saveRecord(t, e.srv, crew.Run{ID: y, StartedAt: now.Add(-time.Hour), ResumedFrom: x})
	for _, c := range []struct{ key, run string }{{"CND-1", x}, {"CND-1", y}, {"CND-2", x}, {"CND-2", y}} {
		op := opID()
		out := e.deliver(t, c.key, op, "https://example.com/pull/1", c.run+"/build/1@"+deliveryCommit(1), http.StatusConflict, "run_linked")
		if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "contested: CND-1 and CND-2") || out["key"] != "CND-1" {
			t.Fatalf("%s naming %s: %v", c.key, c.run, out)
		}
		if _, ok, err := e.srv.dispatch.Receipt(t.Context(), adminActor, op); err != nil || ok {
			t.Fatalf("a receipt kept: %v %v", ok, err)
		}
	}
	for _, key := range []string{"CND-1", "CND-2"} {
		if n := linkCount(t, e, key); n != 1 {
			t.Fatalf("%s has %d links", key, n)
		}
	}
}
