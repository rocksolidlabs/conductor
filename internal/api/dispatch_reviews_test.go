package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A person's review sent, applied, reconciled and marked
// (docs/plans/review-repair.md, RV1 PR-4). The tests wait on the
// mechanism: the ledger's command rows, the store's review, the engine's
// attempts, the store's claims, and the review gate's seams (beforeApply,
// afterRegister, afterReadBack, beforeSettleRead, listFails, afterListing,
// retryWaits, workerSkips, and ticks, the worker's clock); a ledger that refuses or answers falsely is a wrapper
// of the engine's (refuseAfterLedger, lostReplyLedger).

// candName is a candidate's name: <run>/<task>/<n>@<commit>.
func candName(run, task string, n int, commit string) string {
	return fmt.Sprintf("%s/%s/%d@%s", run, task, n, commit)
}

// reviewOf is a review's body: a new op id, the candidate, the base, the
// verdict and the findings.
func reviewOf(candidate, base, verdict string, findings ...map[string]any) map[string]any {
	if findings == nil {
		findings = []map[string]any{}
	}
	return map[string]any{"opId": opID(), "candidate": candidate, "base": base, "verdict": verdict, "summary": "Please look again.", "findings": findings, "byName": "Nate"}
}

// changeOf is a change asked for on the candidate as a whole.
func changeOf(message string) map[string]any {
	return map[string]any{"kind": "general", "intent": "change", "severity": "major", "message": message}
}

// lineOf is a change asked for on lines of a file, on a side.
func lineOf(path, side string, from, to int, lines ...string) map[string]any {
	return map[string]any{"kind": "line", "intent": "change", "severity": "minor", "path": path, "side": side, "from": from, "to": to, "lines": lines,
		"message": "Rename these.", "resolution": "Use clearer names."}
}

// fileOf is a note on a whole file, on the candidate's side.
func fileOf(path string) map[string]any {
	return map[string]any{"kind": "file", "intent": "note", "path": path, "side": "candidate", "message": "Look at this file."}
}

// sendReview posts a review of CND-1 (or key), failing t unless it answers
// the status and code.
func sendReview(t *testing.T, e *testEnv, key string, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/reviews", body, status, code)
}

// applicationOf is a reply's review's application.
func applicationOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	return entityOf(t, out, "review")["application"].(map[string]any)
}

// reviewNow is the review with the id as the store has it.
func reviewNow(t *testing.T, srv *Server, id string) dispatch.Review {
	t.Helper()
	v, err := srv.dispatch.Review(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// reviewsOn is how many reviews the ticket has.
func reviewsOn(t *testing.T, srv *Server, key string) int {
	t.Helper()
	page, err := srv.dispatch.Reviews(context.Background(), key, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Reviews)
}

// commandIn is the command's row in the run's ledger, under admin.
func commandIn(t *testing.T, srv *Server, run, id string) (ledger.CommandRow, bool) {
	t.Helper()
	row, found, err := srv.ledger.Receipt(context.Background(), run, adminActor, id)
	if err != nil {
		t.Fatal(err)
	}
	return row, found
}

// waitApplication waits for the review's application to leave pending,
// failing t if it was ever refused on the way when want is applied.
func waitApplication(t *testing.T, srv *Server, id, want string) dispatch.ReviewApplication {
	t.Helper()
	var a dispatch.ReviewApplication
	waitUntil(t, "the review "+want, func() bool {
		a = reviewNow(t, srv, id).Application
		if want == dispatch.ApplicationApplied && a.State == dispatch.ApplicationRefused {
			t.Fatalf("the review was refused on its way to applied: %+v", a)
		}
		return a.State == want
	})
	return a
}

// A request for changes of a standing candidate is recorded, then applied
// by Yardmaster: the application names the run, the command rv.<id>.1,
// the run's revision after it and the repair attempt (2), which starts at
// the candidate's commit with the person's request in its task file; the
// ledger keeps the command done.
func TestSubmitReviewAppliesARepair(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string {
		return commitFile(t, dir, "users.go", "package users\n\nfunc A() {}\nfunc B() {}\n")
	})
	before := runOf(t, l.testEnv, run)["managed"].(map[string]any)["revision"].(float64)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes",
		lineOf("users.go", "candidate", 3, 4, "func A() {}", "func B() {}"), changeOf("Handle the error.")), http.StatusCreated, "")
	rv := entityOf(t, out, "review")
	id := rv["id"].(string)
	app := applicationOf(t, out)
	if app["state"] != dispatch.ApplicationApplied || app["run"] != run || app["command"] != "rv."+id+".1" || app["attempt"] != 2.0 ||
		app["revision"].(float64) <= before || app["try"] != 1.0 {
		t.Fatalf("the application: %v (the run was at %v)", app, before)
	}
	if rc := receiptOf(t, out); rc["id"] != id || rc["status"] != dispatch.StatusDone {
		t.Fatalf("the receipt: %v", rc)
	}
	if row, found := commandIn(t, l.srv, run, "rv."+id+".1"); !found || row.Status != ledger.CommandDone || row.Intent != "request_changes" {
		t.Fatalf("the ledger's command: %+v %v", row, found)
	}
	a2 := runningAttempt(t, l.testEnv, run, "build", 2)
	if a2["start"] != c1 {
		t.Fatalf("the repair attempt starts at %v, the candidate is %s", a2["start"], c1)
	}
	md := string(l.treeFile(t, run, "build", 2, "task.md"))
	if !strings.Contains(md, id) || !strings.Contains(md, "Handle the error.") || !strings.Contains(md, "users.go") {
		t.Fatalf("the repair attempt's task.md has not the request:\n%s", md)
	}
	// The same opId again answers the same review, applied once.
	again := sendReview(t, l.testEnv, "CND-1", map[string]any{"opId": receiptOf(t, out)["opId"], "candidate": candName(run, "build", 1, c1), "base": l.head,
		"verdict": "request_changes", "summary": "Please look again.", "byName": "Nate", "findings": []map[string]any{
			lineOf("users.go", "candidate", 3, 4, "func A() {}", "func B() {}"), changeOf("Handle the error.")}}, http.StatusOK, "")
	if receiptOf(t, again)["repeat"] != true || entityOf(t, again, "review")["id"] != id || applicationOf(t, again)["state"] != dispatch.ApplicationApplied {
		t.Fatalf("the resend: %v", again)
	}
	if reviewsOn(t, l.srv, "CND-1") != 1 {
		t.Fatal("the resend recorded a second review")
	}
}

// A request for changes of a run the server let go (stopped, from before
// a restart), sent with continueRun, is carried by a continuation of the
// run: applied in the new run, its repair attempt 2 starting at the
// candidate, the old run continued by it.
func TestSubmitReviewOnARunTheServerLetGoContinuesIt(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	again := l.restartAfterStop(t, run)
	body := reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Again, please."))
	body["continueRun"] = true
	out := sendReview(t, again, "CND-1", body, http.StatusCreated, "")
	app := applicationOf(t, out)
	to, _ := app["run"].(string)
	if app["state"] != dispatch.ApplicationApplied || to == "" || to == run || app["attempt"] != 2.0 {
		t.Fatalf("the application: %v", app)
	}
	if row, found, err := again.srv.ledger.Run(t.Context(), run); err != nil || !found || row.ContinuedBy != to {
		t.Fatalf("the run's row: %+v %v %v, want continued by %s", row, found, err, to)
	}
	if a := runningAttempt(t, again, to, "build", 2); a["start"] != c1 {
		t.Fatalf("the continuation's repair attempt starts at %v, not %s", a["start"], c1)
	}
}

// A stopped run the server still holds is continued the same way.
func TestSubmitReviewOnAStoppedRunContinuesIt(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	body := reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Again, please."))
	sendReview(t, l.testEnv, "CND-1", body, http.StatusConflict, "continuation_needed")
	body = reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Again, please."))
	body["continueRun"] = true
	app := applicationOf(t, sendReview(t, l.testEnv, "CND-1", body, http.StatusCreated, ""))
	to, _ := app["run"].(string)
	if app["state"] != dispatch.ApplicationApplied || to == run || to == "" {
		t.Fatalf("the application: %v", app)
	}
	if a := runningAttempt(t, l.testEnv, to, "build", 2); a["start"] != c1 {
		t.Fatalf("the continuation's repair attempt starts at %v, not %s", a["start"], c1)
	}
}

// A request on a candidate that no longer stands when Yardmaster takes it
// is recorded, then refused with the receipt's words, never applied to
// the newer result; the ledger keeps the command failed stale_candidate.
func TestSubmitReviewRefusedWhenTheCandidateMoved(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("First.")), http.StatusCreated, "")
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Second.")), http.StatusCreated, "")
	id := entityOf(t, out, "review")["id"].(string)
	app := applicationOf(t, out)
	if app["state"] != dispatch.ApplicationRefused || !strings.Contains(app["why"].(string), crew.ErrStaleCandidate.Error()) {
		t.Fatalf("the second review's application: %v", app)
	}
	row, found := commandIn(t, l.srv, run, "rv."+id+".1")
	var rc crew.Receipt
	_ = json.Unmarshal([]byte(row.Receipt), &rc)
	if !found || row.Status != ledger.CommandFailed || rc.Refusal != crew.RefusalStaleCandidate {
		t.Fatalf("the ledger's command: %+v %v", row, found)
	}
	if reviewsOn(t, l.srv, "CND-1") != 2 {
		t.Fatal("the refused review was not recorded")
	}
}

// A review names a candidate of a run whose chain the ticket holds alone:
// a run no ticket holds, another ticket's, and a contested chain are each
// 409 not_ticket_run, with nothing recorded and no receipt kept.
func TestSubmitReviewNeedsTheTicketsRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	l.newDispatchTicket(t, "CND", "Two", nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	refused := func(key, candidate, holder string) {
		t.Helper()
		body := reviewOf(candidate, l.head, "comment", changeOf("x"))
		out := sendReview(t, l.testEnv, key, body, http.StatusConflict, "not_ticket_run")
		if holder != "" && out["key"] != holder {
			t.Fatalf("the refusal names %v, not %s: %v", out["key"], holder, out)
		}
		if _, ok, err := l.srv.dispatch.Receipt(t.Context(), adminActor, body["opId"].(string)); err != nil || ok {
			t.Fatalf("a receipt kept: %v %v", ok, err)
		}
	}
	// Another ticket's run.
	refused("CND-2", candName(run, "build", 1, c1), "CND-1")
	// A run no ticket holds.
	c := l.crew
	c.Cwd = l.repo
	free, release, err := l.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	refused("CND-1", candName(free.ID, "build", 1, c1), "")
	// A contested chain: a continuation of the launch's run that CND-2
	// links by hand.
	x := "legacy-000000d1"
	saveRecord(t, l.srv, crew.Run{ID: x, StartedAt: time.Now().UTC()})
	linkRun(t, l.testEnv, "CND-2", x, http.StatusCreated, "")
	crewJSON, _ := json.Marshal(l.crew)
	putLedgerRun(t, l.srv.ledger, ledger.RunRow{ID: x, CrewID: l.crew.ID, Crew: string(crewJSON), Base: l.head, State: ledger.RunFinished, ContinuedFrom: run})
	refused("CND-1", candName(run, "build", 1, c1), "")
	if reviewsOn(t, l.srv, "CND-1")+reviewsOn(t, l.srv, "CND-2") != 0 {
		t.Fatal("a refused review was recorded")
	}
}

// A request whose repair the run could not keep, or whose repair
// attempt's task file could not hold it, is 413 too_large before anything
// is recorded: no review, no receipt.
func TestSubmitReviewTooLargeKeepsNothing(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	var findings []map[string]any
	for range dispatch.MaxReviewFindings {
		// Two bytes a character, none escaped: 100 KB of words, more than
		// the ledger's 64 KiB payload, under the body's 192 KiB.
		findings = append(findings, changeOf(strings.Repeat("é", dispatch.MaxFindingMessage)))
	}
	body := reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", findings...)
	out := sendReview(t, l.testEnv, "CND-1", body, http.StatusRequestEntityTooLarge, "too_large")
	if !strings.Contains(apiMessage(out), "shorten") {
		t.Fatalf("the refusal's words: %v", out)
	}
	if _, ok, err := l.srv.dispatch.Receipt(t.Context(), adminActor, body["opId"].(string)); err != nil || ok || reviewsOn(t, l.srv, "CND-1") != 0 {
		t.Fatalf("something was kept: %v %v", ok, err)
	}
	if row, found, err := l.srv.ledger.Run(t.Context(), run); err != nil || !found || row.State != ledger.RunFinished {
		t.Fatalf("the run after a refused request: %+v %v %v", row, found, err)
	}
}

// A request this server cannot apply is refused before anything is
// recorded: while the ledger is being taken over (503
// ledger_unavailable), and with crews.managed off (409 managed_off). A
// comment is kept all the same.
func TestSubmitReviewWithoutTheLedgerKeepsNothing(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	l.srv.launches.ready.Store(false)
	body := reviewOf(cand, l.head, "request_changes", changeOf("x"))
	sendReview(t, l.testEnv, "CND-1", body, http.StatusServiceUnavailable, "ledger_unavailable")
	l.srv.launches.ready.Store(true)
	off := false
	l.srv.cfg.Crews.Managed = &off
	sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "request_changes", changeOf("x")), http.StatusConflict, "managed_off")
	if reviewsOn(t, l.srv, "CND-1") != 0 {
		t.Fatal("a refused request was recorded")
	}
	if _, ok, _ := l.srv.dispatch.Receipt(t.Context(), adminActor, body["opId"].(string)); ok {
		t.Fatal("a receipt was kept")
	}
	if app := applicationOf(t, sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", changeOf("x")), http.StatusCreated, "")); app["state"] != dispatch.ApplicationNone {
		t.Fatalf("a comment's application: %v", app)
	}
}

// lostReplyLedger is the engine's ledger answering, once armed, the
// commit that settles the command cmd under actor admin with an error
// although it committed: a failure after the command was taken.
type lostReplyLedger struct {
	crew.Ledger
	cmd   atomic.Pointer[string]
	fired atomic.Bool
}

func (g *lostReplyLedger) Transition(ctx context.Context, runID string, fn func(crew.LedgerTx) error) (int64, error) {
	rev, err := g.Ledger.Transition(ctx, runID, fn)
	if cmd := g.cmd.Load(); err == nil && cmd != nil && !g.fired.Load() {
		if row, found, rerr := g.Ledger.Receipt(ctx, runID, adminActor, *cmd); rerr == nil && found && row.Status == ledger.CommandDone && g.fired.CompareAndSwap(false, true) {
			return rev, errors.New("the reply was lost")
		}
	}
	return rev, err
}

// A command that committed but whose answer was an error is read back
// from the ledger by its id and recorded applied, never told refused.
func TestApplicationAfterTheCommandIsReadBack(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	g := &lostReplyLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.beforeApply = func(review string) {
			cmd := reviewCommandID(review, 1)
			g.cmd.Store(&cmd)
		}
	})
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	if !g.fired.Load() {
		t.Fatal("the lost reply never happened")
	}
	app := applicationOf(t, out)
	if app["state"] != dispatch.ApplicationApplied || app["run"] != run || app["attempt"] != 2.0 {
		t.Fatalf("a command read back done: %v", app)
	}
}

// refuseAfterLedger is the engine's ledger refusing, once armed, the
// transactions of the run after the first it lets through: the command
// taken, its change and the pause after it refused, so the change and
// its settling write wait in the run's memory for its next commit.
type refuseAfterLedger struct {
	crew.Ledger
	mu      sync.Mutex
	run     string
	pass    int
	refuse  int
	refused int
}

func (g *refuseAfterLedger) arm(run string, pass, refuse int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.run, g.pass, g.refuse, g.refused = run, pass, refuse, 0
}

func (g *refuseAfterLedger) Transition(ctx context.Context, runID string, fn func(crew.LedgerTx) error) (int64, error) {
	g.mu.Lock()
	refuse := false
	if runID == g.run {
		switch {
		case g.pass > 0:
			g.pass--
		case g.refuse > 0:
			g.refuse--
			g.refused++
			refuse = true
		}
	}
	g.mu.Unlock()
	if refuse {
		return 0, errors.New("the ledger refused the transaction")
	}
	return g.Ledger.Transition(ctx, runID, fn)
}

// retainedReview sends a request whose change and pause the ledger
// refuses: the review stays pending, the command accepted, the change in
// the run's memory. It answers the review's id.
func retainedReview(t *testing.T, l *launchTest, g *refuseAfterLedger) (run, id string) {
	t.Helper()
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	g.arm(run, 1, 2)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	id = entityOf(t, out, "review")["id"].(string)
	if app := applicationOf(t, out); app["state"] != dispatch.ApplicationPending {
		t.Fatalf("a retained change's review: %v", app)
	}
	if row, found := commandIn(t, l.srv, run, reviewCommandID(id, 1)); !found || row.Status != ledger.CommandAccepted {
		t.Fatalf("the command: %+v %v", row, found)
	}
	return run, id
}

// commitRun makes the run's next commit: a pause, with a new command id.
func commitRun(t *testing.T, e *testEnv, run string) {
	t.Helper()
	e.expect(t, "POST", "/api/runs/"+run+"/pause", map[string]any{"commandId": opID()}, http.StatusOK, "")
}

// A command read back accepted while the engine holds its run is not a
// refusal: the review stays pending until the run's next commit writes
// the change and its receipt, then is recorded applied; a run let go with
// its change never written is recorded refused; and the run's next
// commit made at each point of the settlement's order (after the
// registration, after the read-back) is never missed.
func TestApplicationWaitsForARetainedSettle(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	g := &refuseAfterLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)

	run, id := retainedReview(t, l, g)
	commitRun(t, l.testEnv, run)
	if a := waitApplication(t, l.srv, id, dispatch.ApplicationApplied); a.Run != run || a.Attempt != 2 {
		t.Fatalf("the settled application: %+v", a)
	}

	// The run let go before any commit: nothing left can commit it.
	run, id = retainedReview(t, l, g)
	l.srv.runs.ForgetForTest(run)
	if a := waitApplication(t, l.srv, id, dispatch.ApplicationRefused); a.Why != wordsTakenLetGo {
		t.Fatalf("a let-go run's review: %+v", a)
	}

	// The run's next commit after the registration, before the read-back.
	for _, at := range []string{"afterRegister", "afterReadBack"} {
		run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", at+"\n") })
		var once sync.Once
		commit := func(string) { once.Do(func() { commitRun(t, l.testEnv, run) }) }
		l.srv.reviews.setSeams(func(rg *reviewGate) {
			rg.afterRegister, rg.afterReadBack = nil, nil
			if at == "afterRegister" {
				rg.afterRegister = commit
			} else {
				rg.afterReadBack = commit
			}
		})
		g.arm(run, 1, 2)
		out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
		id := entityOf(t, out, "review")["id"].(string)
		if a := waitApplication(t, l.srv, id, dispatch.ApplicationApplied); a.Run != run {
			t.Fatalf("%s: %+v", at, a)
		}
	}

	// The run's commit and its release both land between the read-back's
	// first read (accepted) and its look at the engine's hold: the command
	// is read again after the hold is seen, so the release does not make
	// the stale accepted row a refusal. The worker is held away from the
	// run, so the read-back's own answer is the one recorded.
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "let go\n") })
	release := make(chan struct{})
	var let, once sync.Once
	t.Cleanup(func() { let.Do(func() { close(release) }) })
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.afterRegister = nil
		rg.beforeSettleRead = func(_ context.Context, r string) {
			if r == run {
				<-release
			}
		}
		rg.afterReadBack = func(string) {
			once.Do(func() {
				commitRun(t, l.testEnv, run)
				l.srv.runs.ForgetForTest(run)
			})
		}
	})
	g.arm(run, 1, 2)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	if app := applicationOf(t, out); app["state"] != dispatch.ApplicationApplied || app["run"] != run {
		t.Fatalf("committed and let go between the read-back's reads: %v", app)
	}
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.afterRegister, rg.afterReadBack, rg.beforeSettleRead = nil, nil, nil })
	let.Do(func() { close(release) })
}

// The settlement runs on the worker, outside the engine's lock: its read
// of the ledger held at a gate, the engine commits the same run again and
// another run launches, neither waiting on it; released, the review is
// applied. The server's end waits for the worker before its stores close.
func TestSettlementRunsOutsideTheEngine(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	g := &refuseAfterLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)
	run, id := retainedReview(t, l, g)
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.beforeSettleRead = func(_ context.Context, r string) {
			if r == run {
				once.Do(func() { close(reached) })
				<-release
			}
		}
	})
	var let sync.Once
	t.Cleanup(func() { let.Do(func() { close(release) }) })
	commitRun(t, l.testEnv, run)
	select {
	case <-reached:
	case <-time.After(testWait):
		t.Fatal("the worker never looked at the run")
	}
	// Held: the engine commits the run again, and another run launches.
	l.expect(t, "POST", "/api/runs/"+run+"/unpause", map[string]any{"commandId": opID()}, http.StatusOK, "")
	l.launch(t, l.testEnv, nil, http.StatusCreated, "")
	if a := reviewNow(t, l.srv, id).Application; a.State != dispatch.ApplicationPending {
		t.Fatalf("settled while its read was held: %+v", a)
	}
	let.Do(func() { close(release) })
	waitApplication(t, l.srv, id, dispatch.ApplicationApplied)
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.beforeSettleRead = nil })
	l.srv.stopReviews()
	select {
	case <-l.srv.reviews.done:
	default:
		t.Fatal("the worker outlived stopReviews")
	}
}

// Each finding is bound by the server: a line's anchor is the candidate's
// commit on its side candidate and the base on its side base; a check
// finding's receipt is read from the ledger (its class, its subject, its
// argv), and refused when it is another attempt's or one that passed; a
// client's outcome fields are unknown fields.
func TestSubmitReviewBindsEachFindingToItsCandidate(t *testing.T) {
	l, run, _, commits := failingLaunch(t)
	c3 := commits[2]
	rc3 := receiptK(t, l.testEnv, run, 3, "test")
	rc2 := receiptK(t, l.testEnv, run, 2, "test")
	rc3.RunID, rc2.RunID = run, run
	cand := candName(run, "build", 3, c3)
	check := func(rc ledger.CheckRow, extra map[string]any) map[string]any {
		c := map[string]any{"source": "ledger", "receipt": fmt.Sprintf("%s/build/%d/check/test/%d", rc.RunID, rc.N, rc.K), "chosen": "infrastructure", "quote": "the end"}
		for k, v := range extra {
			c[k] = v
		}
		return map[string]any{"kind": "check", "intent": "change", "severity": "blocker", "check": c}
	}
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", check(rc2, nil)), http.StatusConflict, "invalid")
	if !strings.Contains(apiMessage(out), "not of the candidate reviewed") {
		t.Fatalf("another attempt's receipt: %v", out)
	}
	sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", check(rc3, map[string]any{"exit": 0})), http.StatusBadRequest, "invalid_request")
	out = sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "comment", check(rc3, nil), lineOf("README", "base", 1, 1, "hello"), lineOf("built.txt", "candidate", 1, 1, "x")),
		http.StatusCreated, "")
	fs := entityOf(t, out, "review")["findings"].([]any)
	ref := fs[0].(map[string]any)["check"].(map[string]any)
	if ref["subject"] != c3 || ref["class"] != "code" || ref["chosen"] != "infrastructure" || ref["quote"] != "the end" || ref["attempt"] != 3.0 || len(ref["argv"].([]any)) == 0 {
		t.Fatalf("the check finding as kept: %v", ref)
	}
	if fs[1].(map[string]any)["anchorCommit"] != l.head || fs[2].(map[string]any)["anchorCommit"] != c3 {
		t.Fatalf("the anchors: %v %v", fs[1], fs[2])
	}
	// A passing receipt is no failure to repair.
	l2 := newLaunchTest(t, nil, nil)
	run2, c1 := succeededCandidate(t, l2, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	rows, err := l2.srv.ledger.Checks(t.Context(), run2, "build", 1, false)
	if err != nil || len(rows) == 0 {
		t.Fatalf("the passing receipts: %v %v", rows, err)
	}
	pass := slices.IndexFunc(rows, func(r ledger.CheckRow) bool { return r.ID == "test" })
	rows[pass].RunID, rows[pass].N = run2, 1
	out = sendReview(t, l2.testEnv, "CND-1", reviewOf(candName(run2, "build", 1, c1), l2.head, "comment", check(rows[pass], nil)), http.StatusConflict, "invalid")
	if !strings.Contains(apiMessage(out), "passed") {
		t.Fatalf("a passing receipt: %v", out)
	}
}

// A request of a run the server does not run, sent without continueRun,
// is 409 continuation_needed with the run and in words what a
// continuation starts, nothing recorded and no run made; a run held at the
// check and stopped before the application is recorded refused in those
// words, not continued.
func TestSubmitReviewOnARunTheServerLetGoAsksFirst(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	again := l.restartAfterStop(t, run)
	out := sendReview(t, again, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusConflict, "continuation_needed")
	words, _ := out["words"].(string)
	if out["run"] != run || !strings.Contains(words, "Yardmaster continues run "+run+" as a new run") || !strings.Contains(words, "`build`'s repair attempt 2 starts at "+c1[:7]) {
		t.Fatalf("the answer: %v", out)
	}
	if reviewsOn(t, again.srv, "CND-1") != 0 {
		t.Fatal("a review was recorded")
	}
	if row, _, _ := again.srv.ledger.Run(t.Context(), run); row.ContinuedBy != "" {
		t.Fatalf("a run was made: %s", row.ContinuedBy)
	}

	l2 := newLaunchTest(t, nil, nil)
	run2, c2 := succeededCandidate(t, l2, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	l2.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.beforeApply = func(string) { l2.expect(t, "POST", "/api/runs/"+run2+"/stop", nil, http.StatusOK, "") }
	})
	app := applicationOf(t, sendReview(t, l2.testEnv, "CND-1", reviewOf(candName(run2, "build", 1, c2), l2.head, "request_changes", changeOf("x")), http.StatusCreated, ""))
	if app["state"] != dispatch.ApplicationRefused || !strings.Contains(app["why"].(string), "Yardmaster continues run "+run2) {
		t.Fatalf("a run stopped before its application: %v", app)
	}
	if row, _, _ := l2.srv.ledger.Run(t.Context(), run2); row.ContinuedBy != "" {
		t.Fatalf("the run was continued: %s", row.ContinuedBy)
	}
}

// submitPending records a request for changes of the candidate in the
// store alone, pending at try 1, as a server that ended before applying it
// left it.
//
// Such a review is no request's of this server, so the worker's passes
// would judge it as the start does at once: a test that keeps it for the
// next start holds the worker off it first (holdWorkerOff).
func submitPending(t *testing.T, srv *Server, candidate, base string) dispatch.Review {
	t.Helper()
	rc, v, err := srv.dispatch.SubmitReview(t.Context(), adminOp("Nate"), "CND-1",
		dispatch.NewReview{Candidate: candidate, Base: base, Verdict: dispatch.VerdictRequestChanges, Findings: []dispatch.NewFinding{
			{Kind: "general", Intent: "change", Severity: "major", Message: "Fix it."}}},
		dispatch.ReviewEvidence{Findings: []dispatch.FindingEvidence{{K: 1}}})
	if err != nil || rc.Status != dispatch.StatusDone || v.Application.State != dispatch.ApplicationPending {
		t.Fatalf("the pending review: %+v %+v %v", rc, v.Application, err)
	}
	return v
}

// holdWorkerOff has the worker's passes leave every review alone until
// the returned func lets them go again; the start's pass is not held.
func holdWorkerOff(srv *Server) func() {
	var off atomic.Bool
	off.Store(true)
	srv.reviews.setSeams(func(rg *reviewGate) { rg.workerSkips = func(string) bool { return off.Load() } })
	return func() { off.Store(false) }
}

// At the start every pending review whose run the ledger holds is settled
// by its command: done, applied; failed, refused with its words; accepted
// and uncertain, refused ("the server stopped before it made the
// repair"); none, refused ("before Yardmaster took the request"); one of a
// run this ledger does not hold stays pending; nothing is applied.
func TestReviewsReconciledAtStart(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	holdWorkerOff(l.srv)
	done, failed, accepted, uncertain, none := submitPending(t, l.srv, cand, l.head), submitPending(t, l.srv, cand, l.head),
		submitPending(t, l.srv, cand, l.head), submitPending(t, l.srv, cand, l.head), submitPending(t, l.srv, cand, l.head)
	rq := func(v dispatch.Review) crew.RepairRequest {
		return crew.RepairRequest{Task: "build", Attempt: 1, Commit: c1, Request: reviewRequest(v)}
	}
	if rc, err := l.srv.runs.RequestChanges(t.Context(), run, adminActor, crew.Command{ID: reviewCommandID(done.ID, 1)}, rq(done)); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("the done command: %+v %v", rc, err)
	}
	if rc, _ := l.srv.runs.RequestChanges(t.Context(), run, adminActor, crew.Command{ID: reviewCommandID(failed.ID, 1)}, rq(failed)); rc.Status != ledger.CommandFailed {
		t.Fatalf("the failed command: %+v", rc)
	}
	// A run this ledger does not hold: a legacy run the ticket links.
	x := "legacy-000000e1"
	saveRecord(t, l.srv, crew.Run{ID: x, StartedAt: time.Now().UTC()})
	linkRun(t, l.testEnv, "CND-1", x, http.StatusCreated, "")
	elsewhere := submitPending(t, l.srv, candName(x, "build", 1, c1), l.head)
	runningAttempt(t, l.testEnv, run, "build", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	// The two taken and never settled, written as a server that ended
	// between act's two transactions left them.
	ld, err := ledger.Open(filepath.Join(l.srv.cfg.DataDir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ld.Transition(ctx, run, func(tx *ledger.Tx) error {
		if _, _, err := tx.Command(adminActor, reviewCommandID(accepted.ID, 1), "request_changes", "fp-a"); err != nil {
			return err
		}
		if _, _, err := tx.Command(adminActor, reviewCommandID(uncertain.ID, 1), "request_changes", "fp-u"); err != nil {
			return err
		}
		return tx.Settle(adminActor, reviewCommandID(uncertain.ID, 1), ledger.CommandUncertain, map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	if err := ld.Close(); err != nil {
		t.Fatal(err)
	}
	attemptsBefore := 2
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	for _, tc := range []struct {
		v     dispatch.Review
		state string
		why   string
	}{
		{done, dispatch.ApplicationApplied, ""},
		{failed, dispatch.ApplicationRefused, crew.ErrStaleCandidate.Error()},
		{accepted, dispatch.ApplicationRefused, wordsTakenStopped},
		{uncertain, dispatch.ApplicationRefused, wordsTakenStopped},
		{none, dispatch.ApplicationRefused, wordsNotTaken},
		{elsewhere, dispatch.ApplicationPending, ""},
	} {
		a := reviewNow(t, again.srv, tc.v.ID).Application
		if a.State != tc.state || !strings.Contains(a.Why, tc.why) {
			t.Fatalf("review %s: %+v, want %s %q", tc.v.ID, a, tc.state, tc.why)
		}
	}
	if a := reviewNow(t, again.srv, done.ID).Application; a.Run != run || a.Command != reviewCommandID(done.ID, 1) || a.Attempt != 2 {
		t.Fatalf("the applied review: %+v", a)
	}
	// Nothing applied by the reconcile: no attempt past the one the done
	// command made, the commands as they were.
	attempts, err := again.srv.ledger.Attempts(t.Context(), run)
	if err != nil || len(attempts) != attemptsBefore {
		t.Fatalf("the run's attempts after the start: %d %v", len(attempts), err)
	}
	if row, found := commandIn(t, again.srv, run, reviewCommandID(none.ID, 1)); found {
		t.Fatalf("a command was sent at the start: %+v", row)
	}
}

// A server that ends between act's two transactions (the command taken,
// its change refused) leaves the review pending; at the next start it is
// recorded refused, and the task is as it was.
func TestReviewReconciledBetweenActsTwoTransactions(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	g := &refuseAfterLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)
	run, id := retainedReview(t, l, g)
	// Nothing of the run commits again in this server: it ends as one
	// that crashed would leave it.
	g.arm(run, 0, 1<<30)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	if a := reviewNow(t, again.srv, id).Application; a.State != dispatch.ApplicationRefused || a.Why != wordsTakenStopped {
		t.Fatalf("at the start: %+v", a)
	}
	tasks, err := again.srv.ledger.Tasks(t.Context(), run)
	if err != nil || len(tasks) != 1 || tasks[0].State != ledger.TaskSucceeded || tasks[0].Attempts != 1 {
		t.Fatalf("the task after the start: %+v %v", tasks, err)
	}
}

// A resend after a lost reply is answered from its receipt before any
// evidence is read: with the ledger closed (nothing of the candidate, its
// receipts or its files readable) and with the receipt pruned from
// Dispatch, the review and the mark are each the same, recorded once.
func TestResendAfterTheEvidenceChanged(t *testing.T) {
	l, run, _, commits := failingLaunch(t)
	c3 := commits[2]
	rc3 := receiptK(t, l.testEnv, run, 3, "test")
	body := reviewOf(candName(run, "build", 3, c3), l.head, "comment",
		map[string]any{"kind": "check", "intent": "change", "severity": "blocker", "check": map[string]any{"source": "ledger", "receipt": fmt.Sprintf("%s/build/3/check/test/%d", run, rc3.K)}},
		lineOf("built.txt", "candidate", 1, 1, "x"))
	first := sendReview(t, l.testEnv, "CND-1", body, http.StatusCreated, "")
	id := entityOf(t, first, "review")["id"].(string)
	finding := entityOf(t, first, "review")["findings"].([]any)[1].(map[string]any)["id"].(string)
	// Check again makes attempt 4, the same commit, a later candidate to
	// mark the finding on.
	status, out := recheckOn(t, l.testEnv, run, map[string]any{"commandId": opID(), "attempt": 3, "commit": c3})
	if status != http.StatusOK {
		t.Fatalf("Check again: %d %v", status, out)
	}
	mark := map[string]any{"opId": opID(), "candidate": candName(run, "build", 4, c3), "verdict": "open", "note": "still there"}
	markOut := l.expect(t, "POST", "/api/dispatch/findings/"+finding+"/marks", mark, http.StatusCreated, "")
	markID := entityOf(t, markOut, "mark")["id"].(string)
	same := func() {
		t.Helper()
		out := sendReview(t, l.testEnv, "CND-1", body, http.StatusOK, "")
		if receiptOf(t, out)["repeat"] != true || entityOf(t, out, "review")["id"] != id {
			t.Fatalf("the review resent: %v", out)
		}
		out = l.expect(t, "POST", "/api/dispatch/findings/"+finding+"/marks", mark, http.StatusOK, "")
		if receiptOf(t, out)["repeat"] != true || entityOf(t, out, "mark")["id"] != markID {
			t.Fatalf("the mark resent: %v", out)
		}
	}
	same()
	waitUntil(t, "attempt 4 verified", func() bool {
		a, _ := ledgerAttemptRow(t, l.srv, run, "build", 4)
		return a.State != ledger.AttemptVerifying
	})
	if err := l.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	same()
	db := openRaw(t, filepath.Join(l.srv.dispatch.Dir(), dispatch.File))
	for _, op := range []string{body["opId"].(string), mark["opId"].(string)} {
		if res, err := db.Exec("DELETE FROM ops WHERE id = ?", op); err != nil {
			t.Fatal(err)
		} else if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("pruned %d receipts of %s", n, op)
		}
	}
	same()
	if reviewsOn(t, l.srv, "CND-1") != 1 {
		t.Fatal("the review was recorded twice")
	}
	page, err := l.srv.dispatch.Marks(t.Context(), finding, "")
	if err != nil || len(page.Marks) != 1 {
		t.Fatalf("the marks: %+v %v", page, err)
	}
}

// ledgerAttemptRow is the attempt's row in srv's ledger, if any.
func ledgerAttemptRow(t *testing.T, srv *Server, run, task string, n int) (ledger.AttemptRow, bool) {
	t.Helper()
	rows, err := srv.ledger.Attempts(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.TaskID == task && r.N == n {
			return r, true
		}
	}
	return ledger.AttemptRow{}, false
}

// via is the client's own label, kept beside the actor, the server's:
// web and cli are kept as said; brakeman is refused from every body, the
// admin's included, nothing kept.
func TestReviewViaIsTheServers(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	body := reviewOf(cand, l.head, "comment", changeOf("x"))
	body["via"] = "brakeman"
	sendReview(t, l.testEnv, "CND-1", body, http.StatusBadRequest, "invalid_request")
	if reviewsOn(t, l.srv, "CND-1") != 0 {
		t.Fatal("a review said Brakeman's was kept")
	}
	for _, via := range []string{"web", "cli"} {
		body := reviewOf(cand, l.head, "approve")
		body["via"] = via
		rv := entityOf(t, sendReview(t, l.testEnv, "CND-1", body, http.StatusCreated, ""), "review")
		if rv["via"] != via || rv["actor"] != adminActor || rv["createdBy"] != "Nate" {
			t.Fatalf("via %s: %v", via, rv)
		}
	}
}

// retryReview sends Request again of the review, failing t unless it
// answers the status and code.
func retryReview(t *testing.T, e *testEnv, id string, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["opId"]; !ok {
		body["opId"] = opID()
	}
	return e.expect(t, "POST", "/api/dispatch/reviews/"+id+"/retry", body, status, code)
}

// Request again of a refused request is a new try with a command of its
// own (rv.<id>.2), judged against the run as it is then; the same opId
// again answers the same try.
func TestRequestAgainIsANewCommand(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("First.")), http.StatusCreated, "")
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Second.")), http.StatusCreated, "")
	id := entityOf(t, out, "review")["id"].(string)
	if applicationOf(t, out)["state"] != dispatch.ApplicationRefused {
		t.Fatalf("the second review: %v", out)
	}
	body := map[string]any{"opId": opID()}
	out = retryReview(t, l.testEnv, id, body, http.StatusOK, "")
	app := applicationOf(t, out)
	if app["try"] != 2.0 || app["state"] != dispatch.ApplicationRefused {
		t.Fatalf("Request again: %v", app)
	}
	for try := 1; try <= 2; try++ {
		if row, found := commandIn(t, l.srv, run, reviewCommandID(id, try)); !found || row.Status != ledger.CommandFailed {
			t.Fatalf("try %d's command: %+v %v", try, row, found)
		}
	}
	again := retryReview(t, l.testEnv, id, map[string]any{"opId": body["opId"]}, http.StatusOK, "")
	if receiptOf(t, again)["repeat"] != true || applicationOf(t, again)["try"] != 2.0 {
		t.Fatalf("the resend: %v", again)
	}
	if _, found := commandIn(t, l.srv, run, reviewCommandID(id, 3)); found {
		t.Fatal("the resend sent a third command")
	}
}

// A review refused at the start's reconcile, its run no longer held, is
// sent again only with the word to continue: without it 409
// continuation_needed, its try unchanged; with it the continuation carries
// the repair and the review is applied. A run stopped after a refused
// first submit is the same.
func TestRetryOfAReconciledReviewAsksToContinue(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	holdWorkerOff(l.srv)
	v := submitPending(t, l.srv, candName(run, "build", 1, c1), l.head)
	again := l.restartAfterStop(t, run)
	if a := reviewNow(t, again.srv, v.ID).Application; a.State != dispatch.ApplicationRefused || a.Why != wordsNotTaken {
		t.Fatalf("at the start: %+v", a)
	}
	retryReview(t, again, v.ID, nil, http.StatusConflict, "continuation_needed")
	if a := reviewNow(t, again.srv, v.ID).Application; a.Try != 1 || a.State != dispatch.ApplicationRefused {
		t.Fatalf("after continuation_needed: %+v", a)
	}
	app := applicationOf(t, retryReview(t, again, v.ID, map[string]any{"continueRun": true}, http.StatusOK, ""))
	if app["state"] != dispatch.ApplicationApplied || app["try"] != 2.0 || app["run"] == run {
		t.Fatalf("Request again with the word: %v", app)
	}

	l2 := newLaunchTest(t, nil, nil)
	run2, c2 := succeededCandidate(t, l2, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	body := reviewOf(candName(run2, "build", 1, c2), l2.head, "request_changes", changeOf("x"))
	body["expectedRevision"] = 1
	out := sendReview(t, l2.testEnv, "CND-1", body, http.StatusCreated, "")
	id := entityOf(t, out, "review")["id"].(string)
	if a := applicationOf(t, out); a["state"] != dispatch.ApplicationRefused || !strings.Contains(a["why"].(string), crew.ErrStaleRevision.Error()) {
		t.Fatalf("a stale revision: %v", a)
	}
	l2.expect(t, "POST", "/api/runs/"+run2+"/stop", nil, http.StatusOK, "")
	retryReview(t, l2.testEnv, id, nil, http.StatusConflict, "continuation_needed")
	app = applicationOf(t, retryReview(t, l2.testEnv, id, map[string]any{"continueRun": true}, http.StatusOK, ""))
	if app["state"] != dispatch.ApplicationApplied || app["run"] == run2 {
		t.Fatalf("Request again of the stopped run: %v", app)
	}
}

// twoCandidates launches CND-1, has attempt 1 commit files, sends a
// request for changes of five notes (lines of a.txt, b.txt, d.txt and
// e.txt, and the file c.txt), and has the repair attempt 2 insert two
// lines at the top of a.txt, change b.txt's first line, delete c.txt and
// rename d.txt, then submit: the run, both candidates and the review's
// findings, by path.
func twoCandidates(t *testing.T, l *launchTest) (run, c1, c2 string, findings map[string]string) {
	t.Helper()
	run, c1 = succeededCandidate(t, l, func(dir string) string {
		for name, body := range map[string]string{"a.txt": "one\ntwo\nthree\nfour\nfive\n", "b.txt": "alpha\nbeta\n", "c.txt": "x\n", "d.txt": "keep me\nand me\n", "e.txt": "same\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return commitFile(t, dir, "e.txt", "same\n")
	})
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes",
		lineOf("a.txt", "candidate", 3, 4, "three", "four"), lineOf("b.txt", "candidate", 1, 1, "alpha"), fileOf("c.txt"),
		lineOf("d.txt", "candidate", 1, 2, "keep me", "and me"), lineOf("e.txt", "candidate", 1, 1, "same")), http.StatusCreated, "")
	if applicationOf(t, out)["state"] != dispatch.ApplicationApplied {
		t.Fatalf("the request: %v", out)
	}
	findings = map[string]string{}
	for _, f := range entityOf(t, out, "review")["findings"].([]any) {
		f := f.(map[string]any)
		findings[f["path"].(string)] = f["id"].(string)
	}
	a := runningAttempt(t, l.testEnv, run, "build", 2)
	dir := a["worktree"].(string)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("new\nnewer\none\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("ALPHA\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "rm", "-q", "c.txt")
	gitRun(t, dir, "mv", "d.txt", "d2.txt")
	c2 = commitFile(t, dir, "a.txt", "new\nnewer\none\ntwo\nthree\nfour\nfive\n")
	l.submit(t, a["sessionId"].(string))
	if row := ledgerAttemptIn(t, l.srv, run, "build", 2, ledger.AttemptSucceeded); row.ResultCommit != c2 {
		t.Fatalf("attempt 2's result %s, committed %s", row.ResultCommit, c2)
	}
	return run, c1, c2, findings
}

// findingsOf reads every page of the ticket's findings with the query.
func findingsOf(t *testing.T, e *testEnv, key, query string) []map[string]any {
	t.Helper()
	var out []map[string]any
	after := ""
	for {
		q := query
		if after != "" {
			q += "&after=" + after
		}
		status, data, page := e.call(t, "GET", "/api/dispatch/tickets/"+key+"/findings?"+strings.TrimPrefix(q, "&"), nil)
		if status != http.StatusOK || len(data) > dispatch.MaxFindingsReply {
			t.Fatalf("the findings: %d, %d bytes: %.300s", status, len(data), data)
		}
		for _, f := range page["findings"].([]any) {
			out = append(out, f.(map[string]any))
		}
		next, _ := page["next"].(string)
		if next == "" {
			return out
		}
		after = next
	}
}

// A ticket's findings are located on a later candidate, the one asked or
// the task's newest result: same, moved (now 3–4 to 5–6), changed, the
// file gone, renamed (and where in the renamed file), and unknown past the
// files one reply reads.
func TestFindingsLocatedOnTheNewCandidate(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1, c2, ids := twoCandidates(t, l)
	want := map[string]map[string]any{
		"a.txt": {"state": "moved", "from": 5.0, "to": 6.0},
		"b.txt": {"state": "changed"},
		"c.txt": {"state": "file_gone"},
		"d.txt": {"state": "renamed", "path": "d2.txt", "located": map[string]any{"state": "same"}},
		"e.txt": {"state": "same"},
	}
	at := candName(run, "build", 2, c2)
	for _, q := range []string{"&candidate=" + at, ""} {
		fs := findingsOf(t, l.testEnv, "CND-1", q)
		if len(fs) != 5 {
			t.Fatalf("%q: %d findings", q, len(fs))
		}
		for _, f := range fs {
			path := f["path"].(string)
			if f["id"] != ids[path] || f["locatedOn"] != at || f["state"] != "open" {
				t.Fatalf("%q, %s: %v", q, path, f)
			}
			if got, _ := json.Marshal(f["located"]); string(got) != string(mustMarshal(t, want[path])) {
				t.Fatalf("%q, %s located %s, want %s", q, path, got, mustMarshal(t, want[path]))
			}
		}
	}
	// On its own candidate a finding's lines are where they were.
	for _, f := range findingsOf(t, l.testEnv, "CND-1", "&candidate="+candName(run, "build", 1, c1)) {
		if f["located"].(map[string]any)["state"] != "same" {
			t.Fatalf("on its own candidate: %v", f)
		}
	}
	// Past the files one reply reads: a findings page holds fewer findings
	// than that (the store keeps dispatch.FindingReplyReserve beside each),
	// so the locator's own bound is read through it, past fifty files.
	lc := l.srv.newLocator()
	two, err := dispatch.ParseCandidate(at)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxLocateFiles + 2 {
		f := dispatch.Finding{Kind: dispatch.FindingKindFile, Path: fmt.Sprintf("f%02d.txt", i), Side: dispatch.SideCandidate, AnchorCommit: c1}
		st := lc.locate(t.Context(), f, two)
		switch {
		case i < maxLocateFiles && st.State != locatedFileGone:
			t.Fatalf("file %d: %+v", i, st)
		case i >= maxLocateFiles && (st.State != locatedUnknown || !strings.Contains(st.Why, "more findings' files than one reply reads")):
			t.Fatalf("file %d, past the bound: %+v", i, st)
		}
	}
}

// mustMarshal is v as JSON.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// markOf posts a mark of the finding, failing t unless it answers the
// status and code.
func markOf(t *testing.T, e *testEnv, finding, candidate, verdict string, status int, code string) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/dispatch/findings/"+finding+"/marks", map[string]any{"opId": opID(), "candidate": candidate, "verdict": verdict, "note": "seen"}, status, code)
}

// Where a finding's lines are never changes its state: a finding whose
// lines moved, changed or went stays open until a person marks it; a mark
// keeps where the page showed the lines, and only the mark decides.
func TestLocatedNeverResolves(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, _, c2, ids := twoCandidates(t, l)
	at := candName(run, "build", 2, c2)
	for _, f := range findingsOf(t, l.testEnv, "CND-1", "&candidate="+at) {
		if f["state"] != "open" || f["latest"] != nil {
			t.Fatalf("a located finding changed state: %v", f)
		}
	}
	mk := entityOf(t, markOf(t, l.testEnv, ids["a.txt"], at, "open", http.StatusCreated, ""), "mark")
	if loc, _ := json.Marshal(mk["located"]); string(loc) != `{"from":5,"state":"moved","to":6}` {
		t.Fatalf("the mark's located: %s", loc)
	}
	markOf(t, l.testEnv, ids["c.txt"], at, "resolved", http.StatusCreated, "")
	for _, f := range findingsOf(t, l.testEnv, "CND-1", "&candidate="+at) {
		want := "open"
		if f["id"] == ids["c.txt"] {
			want = "resolved"
		}
		if f["state"] != want {
			t.Fatalf("%v: %v, want %s", f["path"], f["state"], want)
		}
	}
}

// A mark names a later attempt's result of the finding's task in the
// chain of the finding's candidate: its own candidate, a commit not the
// attempt's result and a run of another chain are 409 invalid; a later
// candidate is taken.
func TestMarkNeedsALaterCandidateOfTheTask(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1, c2, ids := twoCandidates(t, l)
	f := ids["e.txt"]
	markOf(t, l.testEnv, f, candName(run, "build", 1, c1), "open", http.StatusConflict, "invalid")
	markOf(t, l.testEnv, f, candName(run, "build", 2, c1), "open", http.StatusConflict, "invalid")
	other, oc := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "z.txt", "z\n") })
	out := markOf(t, l.testEnv, f, candName(other, "build", 1, oc), "open", http.StatusConflict, "invalid")
	if !strings.Contains(apiMessage(out), "not after") && !strings.Contains(apiMessage(out), "not of the chain") {
		t.Fatalf("another chain's candidate: %v", out)
	}
	markOf(t, l.testEnv, f, candName(run, "build", 2, c2), "withdrawn", http.StatusCreated, "")
	page, err := l.srv.dispatch.Marks(t.Context(), f, "")
	if err != nil || len(page.Marks) != 1 {
		t.Fatalf("the finding's marks: %+v %v", page, err)
	}
}

// A repair's new candidate has its own receipts, and the old candidate
// keeps its own, as the candidates route reads them before and after.
func TestOldCandidateKeepsItsReceiptsAfterARepair(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	receipts := func(n int) []any {
		t.Helper()
		for _, c := range candidateList(taskOn(t, candidatesOf(t, l.testEnv, run, ""), "build")) {
			if c["attempt"] == float64(n) {
				return c["receipts"].([]any)
			}
		}
		return nil
	}
	before := receipts(1)
	if len(before) == 0 {
		t.Fatal("attempt 1's candidate has no receipt")
	}
	sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	a := runningAttempt(t, l.testEnv, run, "build", 2)
	c2 := commitFile(t, a["worktree"].(string), "b.txt", "b\n")
	l.submit(t, a["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, run, "build", 2, ledger.AttemptSucceeded)
	after := receipts(1)
	if string(mustMarshal(t, after)) != string(mustMarshal(t, before)) {
		t.Fatalf("attempt 1's receipts changed:\n%v\n%v", before, after)
	}
	own := receipts(2)
	if len(own) == 0 {
		t.Fatal("the repair's candidate has no receipt")
	}
	for _, r := range own {
		if r.(map[string]any)["subject"] != nil && r.(map[string]any)["subject"] != c2 {
			t.Fatalf("a receipt of attempt 2 not of its commit: %v", r)
		}
	}
}

// The review routes are the admin's: an attempt's session token and a
// run's view link are refused, and a switchyard answers 403 on each.
func TestReviewRoutesAreTheAdmins(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	l.srv.limiter = newRateLimiter(1e6, 1e6)
	run := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	a := runningAttempt(t, l.testEnv, run, "build", 1)
	link := l.expect(t, "POST", "/api/runs/"+run+"/links", map[string]any{"role": "view"}, http.StatusCreated, "")["token"].(string)
	id := strings.Repeat("a", dispatch.IDLen)
	routes := []struct{ method, path string }{
		{"POST", "/api/dispatch/tickets/CND-1/reviews"},
		{"GET", "/api/dispatch/tickets/CND-1/reviews"},
		{"POST", "/api/dispatch/reviews/" + id + "/retry"},
		{"GET", "/api/dispatch/reviews/" + id},
		{"GET", "/api/dispatch/tickets/CND-1/findings"},
		{"GET", "/api/dispatch/findings/" + id + "/marks"},
		{"POST", "/api/dispatch/findings/" + id + "/marks"},
	}
	for _, tok := range []string{l.agentToken(a["sessionId"].(string)), link, "", "wrong"} {
		for _, rt := range routes {
			var body any
			if rt.method == "POST" {
				body = map[string]any{"opId": "o1"}
			}
			if status, data, _ := l.callAs(t, tok, rt.method, rt.path, body); status != http.StatusUnauthorized {
				t.Fatalf("%s %s with a token not the admin's: %d %.200s", rt.method, rt.path, status, data)
			}
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, rt := range routes {
		var body any
		if rt.method == "POST" {
			body = map[string]any{"opId": "o1"}
		}
		if status, data, out := yard.callAs(t, adminToken, rt.method, rt.path, body); status != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Fatalf("%s %s on a switchyard: %d %.200s", rt.method, rt.path, status, data)
		}
	}
}

// Each read stays in its bound: the reviews' pages (twenty a page, every
// review reached), a review's detail, its marks and the findings.
func TestReviewRepliesStayInTheirBounds(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	var ids []string
	for i := range dispatch.ReviewsPerPage + 5 {
		out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "comment", changeOf(fmt.Sprintf("note %d", i))), http.StatusCreated, "")
		ids = append(ids, entityOf(t, out, "review")["id"].(string))
	}
	seen := map[string]bool{}
	before := ""
	for pages := 0; ; pages++ {
		q := ""
		if before != "" {
			q = "?before=" + before
		}
		status, data, page := l.call(t, "GET", "/api/dispatch/tickets/CND-1/reviews"+q, nil)
		if status != http.StatusOK || len(data) > dispatch.MaxReviewsReply || len(page["reviews"].([]any)) > dispatch.ReviewsPerPage {
			t.Fatalf("a page of reviews: %d, %d bytes", status, len(data))
		}
		for _, r := range page["reviews"].([]any) {
			seen[r.(map[string]any)["id"].(string)] = true
		}
		next, _ := page["next"].(string)
		if next == "" {
			break
		}
		before = next
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("review %s is on no page", id)
		}
		status, data, out := l.call(t, "GET", "/api/dispatch/reviews/"+id, nil)
		if status != http.StatusOK || len(data) > dispatch.MaxReviewReply || entityOf(t, out, "review")["id"] != id {
			t.Fatalf("review %s: %d, %d bytes", id, status, len(data))
		}
	}
	l.expect(t, "GET", "/api/dispatch/reviews/"+strings.Repeat("0", dispatch.IDLen), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", "/api/dispatch/tickets/CND-1/reviews?before=nope", nil, http.StatusBadRequest, "invalid_request")
	l.expect(t, "GET", "/api/dispatch/tickets/CND-1/findings?state=closed", nil, http.StatusBadRequest, "invalid_request")
	l.expect(t, "GET", "/api/dispatch/tickets/CND-1/findings?candidate=nope", nil, http.StatusBadRequest, "invalid_request")
	if fs := findingsOf(t, l.testEnv, "CND-1", "&state=all"); len(fs) != len(ids) {
		t.Fatalf("%d findings, %d reviews of one each", len(fs), len(ids))
	}
}

// At the combined bounds: a review of fifty findings at their bounds, each
// with fifty marks at theirs (a note of 1,000 characters each escaped, a
// located state at its bound), read through the routes: the detail, every
// page of every finding's marks and every page of the findings, each
// within its bound, and every mark read.
func TestReviewRepliesAtTheCombinedBounds(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	ds := l.srv.dispatch
	var findings []dispatch.NewFinding
	var ev []dispatch.FindingEvidence
	line := strings.Repeat("l", dispatch.MaxAnchorLine)
	for k := 1; k <= dispatch.MaxReviewFindings; k++ {
		lines := make([]string, dispatch.MaxAnchorLines)
		for i := range lines {
			lines[i] = line
		}
		findings = append(findings, dispatch.NewFinding{Kind: "line", Intent: "change", Severity: "nit", Path: fmt.Sprintf("%0*d", dispatch.MaxFindingPath, k), Side: "candidate",
			From: 1, To: dispatch.MaxAnchorLines, Lines: lines, Message: strings.Repeat("m", dispatch.MaxFindingMessage), Resolution: strings.Repeat("r", dispatch.MaxFindingResolution)})
		ev = append(ev, dispatch.FindingEvidence{K: k, AnchorCommit: c1})
	}
	rc, v, err := ds.SubmitReview(t.Context(), adminOp("Nate"), "CND-1", dispatch.NewReview{Candidate: candName(run, "build", 1, c1), Base: l.head,
		Verdict: dispatch.VerdictComment, Summary: strings.Repeat("s", dispatch.MaxReviewSummary), Findings: findings}, dispatch.ReviewEvidence{Findings: ev})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the review at its bounds: %+v %v", rc, err)
	}
	note := strings.Repeat(string(rune(0x2028)), dispatch.MaxMarkNote)
	located := mustMarshal(t, locatedState{State: locatedRenamed, Path: strings.Repeat("p", dispatch.MaxMarkLocated-200), Located: &locatedState{State: locatedMoved, From: 1, To: 2}})
	later := candName(run, "build", 2, strings.Repeat("e", 40))
	for _, f := range v.Findings {
		for range dispatch.MaxMarks {
			if rc, _, err := ds.MarkFinding(t.Context(), adminOp("Nate"), f.ID, dispatch.NewMark{Candidate: later, Verdict: "open", Note: note}, located); err != nil || rc.Status != dispatch.StatusDone {
				t.Fatalf("a mark: %+v %v", rc, err)
			}
		}
	}
	status, data, _ := l.call(t, "GET", "/api/dispatch/reviews/"+v.ID, nil)
	if status != http.StatusOK || len(data) > dispatch.MaxReviewReply {
		t.Fatalf("the detail: %d, %d bytes", status, len(data))
	}
	for _, f := range v.Findings {
		n := 0
		for after := ""; ; {
			q := ""
			if after != "" {
				q = "?after=" + after
			}
			status, data, page := l.call(t, "GET", "/api/dispatch/findings/"+f.ID+"/marks"+q, nil)
			if status != http.StatusOK || len(data) > dispatch.MaxMarksReply {
				t.Fatalf("a page of marks: %d, %d bytes", status, len(data))
			}
			n += len(page["marks"].([]any))
			next, _ := page["next"].(string)
			if next == "" {
				break
			}
			after = next
		}
		if n != dispatch.MaxMarks {
			t.Fatalf("finding %s: %d marks read of %d", f.ID, n, dispatch.MaxMarks)
		}
	}
	if fs := findingsOf(t, l.testEnv, "CND-1", ""); len(fs) != dispatch.MaxReviewFindings {
		t.Fatalf("%d findings read", len(fs))
	}
}

// A review's commit reaches the pages as dispatch events: the review and
// each of its findings, naming their ticket and project; a mark as its
// finding's.
func TestReviewEventsReachTheHub(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	ch := l.srv.events.subscribe()
	defer l.srv.events.unsubscribe(ch)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "comment", changeOf("x"), changeOf("y")), http.StatusCreated, "")
	rv := entityOf(t, out, "review")
	want := map[string]bool{"review/" + rv["id"].(string): false}
	for _, f := range rv["findings"].([]any) {
		want["finding/"+f.(map[string]any)["id"].(string)] = false
	}
	end := time.After(testWait)
	for slices.Contains(mapValues(want), false) {
		select {
		case msg := <-ch:
			if !strings.HasPrefix(string(msg), "event: dispatch\n") {
				continue
			}
			for _, data := range dispatchData(t, msg) {
				var ev dispatchEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatal(err)
				}
				k := ev.Entity + "/" + ev.ID
				if _, ok := want[k]; ok {
					if ev.Ticket != l.ticketID || ev.Project != "CND" {
						t.Fatalf("%s names %s of %s", k, ev.Ticket, ev.Project)
					}
					want[k] = true
				}
			}
		case <-end:
			t.Fatalf("the events never came: %v", want)
		}
	}
}

// mapValues is m's values.
func mapValues(m map[string]bool) []bool {
	out := make([]bool, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// A continuation's command is settled in the run that holds it, never
// refused in the source: the source let go while the request reads its
// command back, a pass made then leaves the review alone (the request
// holds it), and the read-back finds the command done in the
// continuation; a pass after the request agrees.
func TestAContinuationsCommandIsSettledInItsRun(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	var during atomic.Value
	var once sync.Once
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.afterRegister = func(review string) {
			once.Do(func() {
				l.srv.runs.ForgetForTest(run)
				if err := l.srv.passReviews(context.Background(), l.srv.ledger, true); err != nil {
					t.Errorf("a pass during the read-back: %v", err)
				}
				if v, err := l.srv.dispatch.Review(context.Background(), review); err == nil {
					during.Store(v.Application)
				}
			})
		}
	})
	body := reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Again, please."))
	body["continueRun"] = true
	out := sendReview(t, l.testEnv, "CND-1", body, http.StatusCreated, "")
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.afterRegister = nil })
	app, id := applicationOf(t, out), entityOf(t, out, "review")["id"].(string)
	to, _ := app["run"].(string)
	if app["state"] != dispatch.ApplicationApplied || to == "" || to == run {
		t.Fatalf("the application: %v", app)
	}
	if a, ok := during.Load().(dispatch.ReviewApplication); !ok || a.State != dispatch.ApplicationPending {
		t.Fatalf("a pass during the read-back settled the review as %+v", a)
	}
	if err := l.srv.passReviews(t.Context(), l.srv.ledger, true); err != nil {
		t.Fatal(err)
	}
	if a := reviewNow(t, l.srv, id).Application; a.State != dispatch.ApplicationApplied || a.Run != to {
		t.Fatalf("after a pass: %+v, not applied in %s", a, to)
	}
}

// A read-back whose read failed leaves the review pending on the run it
// went to, with the worker; the source let go then, the worker finds the
// command along the chain, done in the continuation: applied, never
// refused because the source holds no such command.
func TestAFailedReadBackIsSettledInTheContinuation(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	var armed atomic.Bool
	fail := func(id string) error {
		if armed.Load() && id == run {
			return errors.New("the ledger could not be read")
		}
		return nil
	}
	chainWalkSeam.Store(&fail)
	t.Cleanup(func() { chainWalkSeam.Store(nil) })
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.afterRegister = func(string) { armed.Store(true) }
	})
	body := reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Again, please."))
	body["continueRun"] = true
	out := sendReview(t, l.testEnv, "CND-1", body, http.StatusCreated, "")
	armed.Store(false)
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.afterRegister = nil })
	id := entityOf(t, out, "review")["id"].(string)
	if app := applicationOf(t, out); app["state"] != dispatch.ApplicationPending {
		t.Fatalf("a read-back that failed: %v", app)
	}
	row, found, err := l.srv.ledger.Run(t.Context(), run)
	if err != nil || !found || row.ContinuedBy == "" {
		t.Fatalf("the source's row: %+v %v %v", row, found, err)
	}
	l.srv.runs.ForgetForTest(run)
	if a := waitApplication(t, l.srv, id, dispatch.ApplicationApplied); a.Run != row.ContinuedBy {
		t.Fatalf("applied in %s, not in the continuation %s", a.Run, row.ContinuedBy)
	}
}

// failRunLedger is the ledger the start's reconcile reads, failing the
// read of one run's row.
type failRunLedger struct {
	reviewLedger
	fail string
}

func (f failRunLedger) Run(ctx context.Context, id string) (ledger.RunRow, bool, error) {
	if id == f.fail {
		return ledger.RunRow{}, false, errors.New("the ledger could not read the run")
	}
	return f.reviewLedger.Run(ctx, id)
}

// The start's reconcile whose read of a continuation's row fails settles
// nothing: the review stays pending (the continuation may hold its
// command done), and the worker's next pass reads it again and settles it
// as the start would: applied in the continuation.
func TestReconcileThatCouldNotReadLeavesItPending(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	let := holdWorkerOff(l.srv)
	v := submitPending(t, l.srv, candName(run, "build", 1, c1), l.head)
	l.expect(t, "POST", "/api/runs/"+run+"/stop", nil, http.StatusOK, "")
	rq := crew.RepairRequest{Task: "build", Attempt: 1, Commit: c1, Request: reviewRequest(v)}
	cont, err := l.srv.runs.ContinueRunWith(t.Context(), run, "", crew.ContinueOptions{Repair: &rq, Actor: adminActor, Command: crew.Command{ID: reviewCommandID(v.ID, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if row, found := commandIn(t, l.srv, cont.ID, reviewCommandID(v.ID, 1)); !found || row.Status != ledger.CommandDone {
		t.Fatalf("the continuation's command: %+v %v", row, found)
	}
	if err := l.srv.passReviews(t.Context(), failRunLedger{reviewLedger: l.srv.ledger, fail: cont.ID}, false); err != nil {
		t.Fatal(err)
	}
	if a := reviewNow(t, l.srv, v.ID).Application; a.State != dispatch.ApplicationPending {
		t.Fatalf("settled although the continuation could not be read: %+v", a)
	}
	// The worker's next pass, nothing remembered of the failure.
	let()
	l.srv.reviews.poke()
	if a := waitApplication(t, l.srv, v.ID, dispatch.ApplicationApplied); a.Run != cont.ID || a.Command != reviewCommandID(v.ID, 1) {
		t.Fatalf("the application: %+v, want the continuation %s", a, cont.ID)
	}
}

// An outcome Dispatch could not record stays with the worker, which
// writes it again without a restart: the write of a repair applied at once
// fails, and the review is still recorded applied.
func TestAnOutcomeNotRecordedIsWrittenAgain(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	var failed atomic.Bool
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.recordFails = func(string) error {
			if failed.CompareAndSwap(false, true) {
				return errors.New("the store is busy")
			}
			return nil
		}
	})
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	if !failed.Load() {
		t.Fatal("the outcome's write never failed")
	}
	id := entityOf(t, out, "review")["id"].(string)
	if a := waitApplication(t, l.srv, id, dispatch.ApplicationApplied); a.Run != run || a.Attempt != 2 {
		t.Fatalf("the application written again: %+v", a)
	}
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.recordFails = nil })
}

// The hints inform a pass, never decide whether it settles (the store's
// claim does): a hint is of one try, so a later try's notes replace an
// earlier try's, an outcome kept for a try is read for that try alone,
// and dropping a try's hint, a late settlement of an earlier try, leaves
// a later try's as it is; a commit wakes the worker only for a run a
// review is watched on.
func TestAHintIsOfOneTry(t *testing.T) {
	var g reviewGate
	g.wake = make(chan struct{}, 1)
	woke := func() bool {
		select {
		case <-g.wake:
			return true
		default:
			return false
		}
	}
	if how := settleHow(g.hintOf("r0", 1)); how != settleAsStart {
		t.Fatalf("a review not known: %d", how)
	}
	g.ours("r1", 1)
	if how := settleHow(g.hintOf("r1", 1)); how != settleAsOurs {
		t.Fatalf("a try of ours: %d", how)
	}
	g.keep("r1", 1, applyOutcome{why: "late"})
	if h := g.hintOf("r1", 1); settleHow(h) != settleKnown || h.out.why != "late" {
		t.Fatalf("an outcome kept for try 1: %+v", h)
	}
	if how := settleHow(g.hintOf("r1", 2)); how != settleAsStart {
		t.Fatalf("try 2 read try 1's hint: %d", how)
	}
	g.ours("r1", 2)
	if h := g.hintOf("r1", 2); settleHow(h) != settleAsOurs || h.out != nil {
		t.Fatalf("try 2 after try 1's outcome kept: %+v", h)
	}
	g.settled("r1", 1)
	if how := settleHow(g.hintOf("r1", 2)); how != settleAsOurs {
		t.Fatalf("dropping try 1's hint dropped try 2's: %d", how)
	}
	g.watch("r1", 2, "run-a")
	g.runChanged("run-b")
	if woke() {
		t.Fatal("a commit of a run no review is watched on woke the worker")
	}
	g.runChanged("run-a")
	if !woke() {
		t.Fatal("a commit of a watched run did not wake the worker")
	}
	g.settled("r1", 2)
	if _, ok := g.hints["r1"]; ok {
		t.Fatal("try 2's hint kept after its outcome was recorded")
	}
}

// A pass acts on a listed try only under the store's claim on that exact
// try, taken when it acts: a pass lists try 1 pending, and before it
// reaches it the try is recorded refused and Request again leaves try 2
// pending (its command accepted, its change retained in the run's
// memory). The pass's claim of try 1 fails (stale), so it records nothing
// and drops nothing of try 2: a pass after it judges try 2 as ours (it
// waits while the engine holds the run, never refused as a command a
// server before this one left), and the run's next commit applies it.
func TestAPassLeavesAStaleListedTry(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	g := &refuseAfterLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	let := holdWorkerOff(l.srv)
	v := submitPending(t, l.srv, candName(run, "build", 1, c1), l.head)
	var once sync.Once
	reached := false
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.afterListing = func() {
			once.Do(func() {
				reached = true
				if rc, err := l.srv.dispatch.RefusedReview(context.Background(), adminOp("Nate"), v.ID, 1, "no"); err != nil || rc.Status != dispatch.StatusDone {
					t.Fatalf("try 1 refused: %+v %v", rc, err)
				}
				g.arm(run, 1, 2)
				out := retryReview(t, l.testEnv, v.ID, nil, http.StatusOK, "")
				if a := applicationOf(t, out); a["try"] != 2.0 || a["state"] != dispatch.ApplicationPending {
					t.Fatalf("Request again, its change retained: %v", a)
				}
			})
		}
	})
	if err := l.srv.passReviews(t.Context(), l.srv.ledger, false); err != nil {
		t.Fatal(err)
	}
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.afterListing = nil })
	if !reached {
		t.Fatal("the pass never listed")
	}
	if a := reviewNow(t, l.srv, v.ID).Application; a.Try != 2 || a.State != dispatch.ApplicationPending {
		t.Fatalf("after the pass that listed try 1: %+v", a)
	}
	if h := l.srv.reviews.hintOf(v.ID, 2); !h.ours || h.watch != run {
		t.Fatalf("try 2's hint after the pass that listed try 1: %+v", h)
	}
	if err := l.srv.passReviews(t.Context(), l.srv.ledger, false); err != nil {
		t.Fatal(err)
	}
	if a := reviewNow(t, l.srv, v.ID).Application; a.Try != 2 || a.State != dispatch.ApplicationPending {
		t.Fatalf("try 2, its command accepted and its run held: %+v", a)
	}
	let()
	commitRun(t, l.testEnv, run)
	if a := waitApplication(t, l.srv, v.ID, dispatch.ApplicationApplied); a.Try != 2 || a.Run != run || a.Command != reviewCommandID(v.ID, 2) {
		t.Fatalf("try 2's application: %+v", a)
	}
}

// The submit holds the store's claim on its try from the commit that
// makes it pending until its command's receipt is recorded: Request again
// sent between the commit and the command waits for the claim, making no
// try of its own, and a pass made then leaves the try alone (its claim
// fails), so nothing records the request not taken while its command is
// still to be sent. Released, the review is applied, and Request again
// is answered 409 invalid: only a refused request is sent again.
func TestASubmitsClaimHoldsOffARetryAndAPass(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	type answer struct {
		status int
		out    map[string]any
	}
	retried, waiting := make(chan answer, 1), make(chan struct{})
	var once, wonce sync.Once
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.retryWaits = func(string) { wonce.Do(func() { close(waiting) }) }
		rg.beforeApply = func(review string) {
			once.Do(func() {
				go func() {
					status, _, out := l.call(t, "POST", "/api/dispatch/reviews/"+review+"/retry", map[string]any{"opId": opID()})
					retried <- answer{status, out}
				}()
				select {
				case <-waiting:
				case <-time.After(testWait):
					t.Error("Request again never waited for the submit's claim")
					return
				}
				if err := l.srv.passReviews(context.Background(), l.srv.ledger, true); err != nil {
					t.Errorf("a pass during the submit: %v", err)
				}
				if v, err := l.srv.dispatch.Review(context.Background(), review); err != nil || v.Application.State != dispatch.ApplicationPending || v.Application.Try != 1 {
					t.Errorf("settled while the submit holds its claim: %+v %v", v.Application, err)
				}
			})
		}
	})
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.beforeApply, rg.retryWaits = nil, nil })
	id := entityOf(t, out, "review")["id"].(string)
	if app := applicationOf(t, out); app["state"] != dispatch.ApplicationApplied || app["try"] != 1.0 || app["run"] != run {
		t.Fatalf("the submit's application: %v", app)
	}
	select {
	case a := <-retried:
		if e, _ := a.out["error"].(map[string]any); a.status != http.StatusConflict || e == nil || e["code"] != dispatch.RefusalInvalid {
			if rc, _ := a.out["receipt"].(map[string]any); a.status != http.StatusConflict || rc == nil || rc["refusal"] != dispatch.RefusalInvalid {
				t.Fatalf("Request again after the submit: %d %v", a.status, a.out)
			}
		}
	case <-time.After(testWait):
		t.Fatal("Request again never answered")
	}
	if a := reviewNow(t, l.srv, id).Application; a.State != dispatch.ApplicationApplied || a.Try != 1 {
		t.Fatalf("the review: %+v", a)
	}
}

// A review committed whose read after its commit failed is not lost: the
// submit answers its receipt, done, and the worker's pass settles the
// review (no command was sent: refused, to be sent again); Request again
// whose read after its commit failed is the same at its next try.
func TestACommittedReviewWhoseReadFailedIsSettled(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	// Armed, the first read of a review pending (its try committed) fails:
	// the read after the commit, not a read before it.
	var armed atomic.Bool
	l.srv.dispatch.SetReviewReadFailsForTest(func(id string) error {
		if !armed.Load() {
			return nil
		}
		pending, err := l.srv.dispatch.PendingReviews(context.Background())
		if err != nil || !slices.ContainsFunc(pending, func(v dispatch.ReviewSummary) bool { return v.ID == id }) {
			return nil
		}
		if armed.CompareAndSwap(true, false) {
			return errors.New("the store could not read the review")
		}
		return nil
	})
	t.Cleanup(func() { l.srv.dispatch.SetReviewReadFailsForTest(nil) })
	armed.Store(true)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("x")), http.StatusCreated, "")
	if armed.Load() {
		t.Fatal("the read after the commit never failed")
	}
	rc := receiptOf(t, out)
	id, _ := rc["id"].(string)
	if rc["status"] != dispatch.StatusDone || id == "" {
		t.Fatalf("the receipt: %v", rc)
	}
	if a := waitApplication(t, l.srv, id, dispatch.ApplicationRefused); a.Try != 1 || a.Why != wordsNotFound {
		t.Fatalf("try 1: %+v", a)
	}
	armed.Store(true)
	out = retryReview(t, l.testEnv, id, nil, http.StatusOK, "")
	if armed.Load() {
		t.Fatal("the read after Request again's commit never failed")
	}
	if rc := receiptOf(t, out); rc["status"] != dispatch.StatusDone {
		t.Fatalf("Request again's receipt: %v", rc)
	}
	var a dispatch.ReviewApplication
	waitUntil(t, "try 2 settled", func() bool {
		a = reviewNow(t, l.srv, id).Application
		return a.Try == 2 && a.State != dispatch.ApplicationPending
	})
	if a.State != dispatch.ApplicationRefused || a.Why != wordsNotFound {
		t.Fatalf("try 2: %+v", a)
	}
	for try := 1; try <= 2; try++ {
		if row, found := commandIn(t, l.srv, run, reviewCommandID(id, try)); found {
			t.Fatalf("try %d's command was sent: %+v", try, row)
		}
	}
}

// onTestClock puts srv's worker on the test's clock: from its return the
// worker passes on a send to the channel (or a wake), never on its own
// interval; a send returns once the pass before it has ended.
func onTestClock(t *testing.T, srv *Server) chan time.Time {
	t.Helper()
	ticks := make(chan time.Time)
	srv.reviews.setSeams(func(rg *reviewGate) { rg.ticks = ticks })
	srv.reviews.poke()
	tick(t, ticks)
	return ticks
}

// tick sends the worker one tick of the test's clock.
func tick(t *testing.T, ticks chan time.Time) {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-time.After(testWait):
		t.Fatal("the worker never took the tick")
	}
}

// A settlement past its budget leaves the review pending, and the worker's
// next pass, on its clock alone (no commit, no wake, nothing remembered of
// the failure), settles it: the store is the worklist, not a retry the
// failed settlement had to schedule. The budget bounds the claim too: one
// spent before the claim (the first, held so; any on a loaded machine,
// whose claim can take longer than the test's 50 ms) leaves the review
// unclaimed, and a later pass claims it.
func TestASettlementPastItsBudgetIsTakenUpByTheNextPass(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	let := holdWorkerOff(l.srv)
	ticks := onTestClock(t, l.srv)
	expired := make(chan error, 1)
	var first atomic.Bool
	first.Store(true)
	var claims atomic.Int32
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.settleWait = 50 * time.Millisecond
		rg.beforeClaim = func(ctx context.Context, _ string) {
			if claims.Add(1) == 1 {
				<-ctx.Done()
			}
		}
		rg.beforeSettleRead = func(ctx context.Context, _ string) {
			if !first.CompareAndSwap(true, false) {
				return
			}
			<-ctx.Done()
			l.srv.reviews.setSeams(func(rg *reviewGate) { rg.settleWait = 0 })
			expired <- ctx.Err()
		}
	})
	v := submitPending(t, l.srv, candName(run, "build", 1, c1), l.head)
	let()
	tick(t, ticks)
	// A tick is taken once the pass before it has ended: one taken before
	// the read's budget ran out followed a pass that never claimed.
	for read := false; !read; {
		select {
		case err := <-expired:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the first settlement ended with %v, not its budget", err)
			}
			read = true
		case ticks <- time.Now():
		case <-time.After(testWait):
			t.Fatal("the pass never settled the review")
		}
	}
	if n := claims.Load(); n < 2 {
		t.Fatalf("the read came from claim %d, not one after the claim past its budget", n)
	}
	tick(t, ticks)
	if a := waitApplication(t, l.srv, v.ID, dispatch.ApplicationRefused); a.Why != wordsNotTaken {
		t.Fatalf("the application: %+v", a)
	}
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.beforeClaim, rg.beforeSettleRead = nil, nil })
}

// A listing of the pending reviews that fails, the start's (its budget
// spent before it, as the launches' reconcile before it may spend it) and
// then a worker's, settles nothing and leaves nothing unwatched: the next
// pass lists every pending review again and settles each.
func TestAFailedListingIsListedAgain(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	let := holdWorkerOff(l.srv)
	ticks := onTestClock(t, l.srv)
	reviews := []dispatch.Review{submitPending(t, l.srv, cand, l.head), submitPending(t, l.srv, cand, l.head)}
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	l.srv.reconcileReviews(spent, l.srv.ledger)
	for _, v := range reviews {
		if a := reviewNow(t, l.srv, v.ID).Application; a.State != dispatch.ApplicationPending {
			t.Fatalf("settled by a start that could not list: %+v", a)
		}
	}
	let()
	var lists atomic.Int32
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.listFails = func() error {
			if lists.Add(1) == 1 {
				return errors.New("Dispatch could not list the reviews")
			}
			for _, v := range reviews {
				if now, err := l.srv.dispatch.Review(context.Background(), v.ID); err != nil || now.Application.State != dispatch.ApplicationPending {
					t.Errorf("before the second listing: %+v %v", now.Application, err)
				}
			}
			return nil
		}
	})
	tick(t, ticks)
	tick(t, ticks)
	for _, v := range reviews {
		if a := waitApplication(t, l.srv, v.ID, dispatch.ApplicationRefused); a.Why != wordsNotTaken {
			t.Fatalf("review %s: %+v", v.ID, a)
		}
	}
	if n := lists.Load(); n < 2 {
		t.Fatalf("%d listings", n)
	}
	l.srv.reviews.setSeams(func(rg *reviewGate) { rg.listFails = nil })
}

// A candidate refused before its checks (a setup step failed on its
// result) is repaired from where its attempt started, as the engine
// repairs it (crew.RepairStart): continuation_needed says that start and
// tells it apart from the commit reviewed.
func TestContinuationWordsNameTheRepairsStart(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Setup = []crew.Check{{ID: "deps", Argv: []string{"sh", "-c", "test ! -e broken.txt"}}}
	})
	run := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	commits := spendBuild(t, l.testEnv, run, "broken.txt")
	a3, _ := ledgerAttemptRow(t, l.srv, run, "build", 3)
	if !strings.HasPrefix(a3.Stage, "setup") || a3.StartCommit == "" || a3.StartCommit == commits[2] || crew.RepairStart(a3) != a3.StartCommit {
		t.Fatalf("attempt 3: stage %q, start %s, result %s", a3.Stage, a3.StartCommit, commits[2])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	again := l.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	out := sendReview(t, again, "CND-1", reviewOf(candName(run, "build", 3, commits[2]), l.head, "request_changes", changeOf("x")), http.StatusConflict, "continuation_needed")
	words, _ := out["words"].(string)
	want := fmt.Sprintf("`build`'s repair attempt 4 starts at %s, where attempt 3 started (its result %s, the commit reviewed, was refused before its checks)",
		a3.StartCommit[:7], commits[2][:7])
	if !strings.Contains(words, want) {
		t.Fatalf("the words: %q\nwant: %q", words, want)
	}
}

// A request resent with its opId and without continueRun is answered
// from its receipt before the gate and before continuation_needed: its
// run let go (a restart), the ledger not taken over, crews.managed off and
// the ledger closed, the request and a Request again's opId are each
// answered 200 with the review as it stands, repeat true, never 409
// continuation_needed, managed_off or 503.
func TestAResentRequestIsAnsweredBeforeTheGate(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	cand := candName(run, "build", 1, c1)
	first := reviewOf(cand, l.head, "request_changes", changeOf("First."))
	applied := entityOf(t, sendReview(t, l.testEnv, "CND-1", first, http.StatusCreated, ""), "review")["id"].(string)
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(cand, l.head, "request_changes", changeOf("Second.")), http.StatusCreated, "")
	refused := entityOf(t, out, "review")["id"].(string)
	if applicationOf(t, out)["state"] != dispatch.ApplicationRefused {
		t.Fatalf("the second review: %v", out)
	}
	retry := map[string]any{"opId": opID()}
	if app := applicationOf(t, retryReview(t, l.testEnv, refused, retry, http.StatusOK, "")); app["try"] != 2.0 {
		t.Fatalf("Request again: %v", app)
	}
	again := l.restartAfterStop(t, run)
	resent := func(when string) {
		t.Helper()
		out := sendReview(t, again, "CND-1", first, http.StatusOK, "")
		if receiptOf(t, out)["repeat"] != true || entityOf(t, out, "review")["id"] != applied || applicationOf(t, out)["state"] != dispatch.ApplicationApplied {
			t.Fatalf("%s: the request resent: %v", when, out)
		}
		out = retryReview(t, again, refused, map[string]any{"opId": retry["opId"]}, http.StatusOK, "")
		if receiptOf(t, out)["repeat"] != true || applicationOf(t, out)["try"] != 2.0 {
			t.Fatalf("%s: Request again resent: %v", when, out)
		}
	}
	resent("the run let go")
	again.srv.launches.ready.Store(false)
	resent("the ledger not taken over")
	again.srv.launches.ready.Store(true)
	managed := again.srv.cfg.Crews.Managed
	off := false
	again.srv.cfg.Crews.Managed = &off
	resent("crews.managed off")
	again.srv.cfg.Crews.Managed = managed
	if err := again.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	resent("the ledger closed")
	if reviewsOn(t, again.srv, "CND-1") != 2 {
		t.Fatal("a resend recorded a review")
	}
	if a := reviewNow(t, again.srv, refused).Application; a.Try != 2 {
		t.Fatalf("a resend made a try: %+v", a)
	}
}

// The server's Shutdown ends the reviews' worker before it closes the
// stores: the worker, held inside its settlement when Shutdown begins,
// sees its context cut by the shutdown while Dispatch's store and the
// ledger are still open, and Shutdown returns only after the worker has
// ended. What the worker had not written is the next start's.
func TestShutdownEndsTheWorkerBeforeTheStoresClose(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	g := &refuseAfterLedger{Ledger: crew.SQLLedger(l.srv.ledger)}
	l.srv.runs.UseLedger(g)
	run, _ := retainedReview(t, l, g)
	type seen struct{ cut, dispatchErr, ledgerErr error }
	reached, result := make(chan struct{}), make(chan seen, 1)
	var once sync.Once
	l.srv.reviews.setSeams(func(rg *reviewGate) {
		rg.beforeSettleRead = func(ctx context.Context, r string) {
			held := false
			once.Do(func() { held = r == run })
			if !held {
				return
			}
			close(reached)
			select {
			case <-ctx.Done():
			case <-time.After(testWait):
			}
			var sn seen
			sn.cut = ctx.Err()
			_, sn.dispatchErr = l.srv.dispatch.PendingReviews(context.Background())
			_, _, sn.ledgerErr = l.srv.ledger.Run(context.Background(), run)
			result <- sn
		}
	})
	commitRun(t, l.testEnv, run)
	select {
	case <-reached:
	case <-time.After(testWait):
		t.Fatal("the worker never looked at the run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
	var sn seen
	select {
	case sn = <-result:
	default:
		t.Fatal("Shutdown returned before the worker it holds ended")
	}
	if !errors.Is(sn.cut, context.Canceled) {
		t.Fatalf("the worker's context at the shutdown: %v, not cut by it", sn.cut)
	}
	if sn.dispatchErr != nil || sn.ledgerErr != nil {
		t.Fatalf("a store closed before the worker ended: Dispatch %v, the ledger %v", sn.dispatchErr, sn.ledgerErr)
	}
	select {
	case <-l.srv.reviews.done:
	default:
		t.Fatal("the worker outlived Shutdown")
	}
}

// Request again holds the candidate to the review's ticket as the review
// was: a refused request whose run's chain became contested since (CND-2
// links a continuation of it) is 409 not_ticket_run, its try unchanged and
// no command sent.
func TestRequestAgainNeedsTheTicketsRunStill(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	l.newDispatchTicket(t, "CND", "Two", nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "a\n") })
	sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("First.")), http.StatusCreated, "")
	out := sendReview(t, l.testEnv, "CND-1", reviewOf(candName(run, "build", 1, c1), l.head, "request_changes", changeOf("Second.")), http.StatusCreated, "")
	id := entityOf(t, out, "review")["id"].(string)
	if applicationOf(t, out)["state"] != dispatch.ApplicationRefused {
		t.Fatalf("the second review: %v", out)
	}
	x := "legacy-000000d2"
	saveRecord(t, l.srv, crew.Run{ID: x, StartedAt: time.Now().UTC()})
	linkRun(t, l.testEnv, "CND-2", x, http.StatusCreated, "")
	crewJSON, _ := json.Marshal(l.crew)
	putLedgerRun(t, l.srv.ledger, ledger.RunRow{ID: x, CrewID: l.crew.ID, Crew: string(crewJSON), Base: l.head, State: ledger.RunFinished, ContinuedFrom: run})
	retryReview(t, l.testEnv, id, nil, http.StatusConflict, "not_ticket_run")
	if a := reviewNow(t, l.srv, id).Application; a.State != dispatch.ApplicationRefused || a.Try != 1 {
		t.Fatalf("after not_ticket_run: %+v", a)
	}
	if _, found := commandIn(t, l.srv, run, reviewCommandID(id, 2)); found {
		t.Fatal("a command was sent")
	}
}
