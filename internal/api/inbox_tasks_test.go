package api

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// failingCrew is managedCrew with build's one check failing on every
// attempt.
func failingCrew(root string) crew.Crew {
	c := managedCrew(root)
	c.Tasks[0].Checks = []crew.Check{{ID: "never", Argv: []string{"sh", "-c", "exit 1"}}}
	return c
}

// failBuild launches c and has build's three attempts each submit HEAD,
// which its check fails, until build is failed; the run is stopped at the
// test's end.
func failBuild(t *testing.T, e *testEnv, c crew.Crew) string {
	t.Helper()
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	for n := 1; n <= 3; n++ {
		a := runningAttempt(t, e, run.ID, "build", n)
		sid := a["sessionId"].(string)
		if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "try"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result %d: %d %v", n, resp.StatusCode, out)
		}
	}
	taskVia(t, e, run.ID, "build", "failed", func(task map[string]any) bool { return task["state"] == "failed" })
	return run.ID
}

// command sends the task command at expect, answering the status and the
// error's code.
func (e *testEnv) command(t *testing.T, run, task, name, id, expect string) (int, string) {
	t.Helper()
	rev, err := strconv.ParseInt(expect, 10, 64)
	if err != nil {
		t.Fatalf("expect %q: %v", expect, err)
	}
	resp, out := e.do("POST", "/api/runs/"+run+"/tasks/"+task+"/"+name, adminToken, map[string]any{"commandId": id, "expectedRevision": rev})
	return resp.StatusCode, errCodeOf(out)
}

// actionOf is the item's action named name.
func actionOf(t *testing.T, it inboxItem, name string) inboxAction {
	t.Helper()
	for _, a := range it.Actions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no %s among %v", name, actionNames(it))
	return inboxAction{}
}

// A failed task's item offers what the engine then takes: Retry and
// Accept anyway (attempts submitted commits), each at the run's revision;
// a Cancel it leaves out is refused by the task's state; a command at an
// old revision is refused stale_revision; the Accept at its action's
// Expect taken, the task no longer an item. The fixture's every row is
// held against the engine in internal/crew (TestTaskCommandsFollowTheFixture).
func TestInboxTaskItemsFollowTheEngine(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failBuild(t, e, failingCrew(e.root))
	ref := "task:" + run + "/build"
	page, _ := e.inboxList(t, "")
	if !listed(page, ref) || page.Total != 1 {
		t.Fatalf("the list: %+v", page)
	}
	it := e.itemOf(t, ref)
	if !slices.Equal(actionNames(it), []string{crew.TaskRetry, crew.TaskAccept}) || it.Revision == "" {
		t.Fatalf("the actions: %v at %q", actionNames(it), it.Revision)
	}
	for _, a := range it.Actions {
		if a.Expect != it.Revision || !strings.HasPrefix(a.Route, "POST /api/runs/"+run+"/tasks/build/") || !strings.Contains(a.Scope, "run revision "+it.Revision) {
			t.Fatalf("action %+v", a)
		}
	}
	if !actionOf(t, it, crew.TaskAccept).Confirm || !strings.Contains(actionOf(t, it, crew.TaskAccept).Scope, "attempt 3") {
		t.Fatalf("accept: %+v", actionOf(t, it, crew.TaskAccept))
	}
	if !slices.ContainsFunc(it.Evidence, func(r inboxRef) bool { return strings.HasPrefix(r.Ref, "check:"+run+"/build/3/never/") }) {
		t.Fatalf("the failed check in the evidence: %+v", it.Evidence)
	}
	if status, code := e.command(t, run, "build", crew.TaskCancel, "c1", it.Revision); status != http.StatusConflict || code != "task_state" {
		t.Fatalf("a cancel the item leaves out: %d %s", status, code)
	}
	if status, code := e.command(t, run, "build", crew.TaskAccept, "c2", it.Revision); status != http.StatusConflict || code != "stale_revision" {
		t.Fatalf("an accept at the old revision: %d %s", status, code)
	}
	again := e.itemOf(t, ref)
	if again.Revision == it.Revision || actionOf(t, again, crew.TaskAccept).Expect != again.Revision {
		t.Fatalf("read again: %q after %q", again.Revision, it.Revision)
	}
	if status, code := e.command(t, run, "build", crew.TaskAccept, "c3", again.Revision); status != http.StatusOK {
		t.Fatalf("the accept at its Expect: %d %s", status, code)
	}
	if page, _ := e.inboxList(t, ""); listed(page, ref) {
		t.Fatalf("an accepted task listed: %+v", page)
	}
	if done := e.itemOf(t, ref); len(done.Actions) != 0 || !strings.Contains(done.Now, "succeeded") {
		t.Fatalf("the accepted task's item: %+v", done)
	}
}

// An attempt that reports a blocker is one item, its session's question,
// naming the task and its blocker and carrying Cancel at the run's
// revision (its answers at the question's id); no item for the task. The
// run moved while the same question shows: the Cancel at its Expect is
// refused stale_revision, nothing cancelled, and the item read again
// carries the new revision, at which the Cancel is taken.
func TestABlockerIsOneItem(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/blocked", e.agentToken(sid), map[string]any{"reason": "the API key is missing", "message": "Where is the key?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("blocked: %d %v", resp.StatusCode, out)
	}
	var q string
	until(t, "the blocker asked", func() bool {
		a := e.local(sid).Info().Attention
		q = a.ID
		return a.Message == "Where is the key?" && q != ""
	})
	ref := questionRef(sid, q)
	page, _ := e.inboxList(t, "")
	if page.Total != 1 || !listed(page, ref) {
		t.Fatalf("the list: %+v", page)
	}
	it := e.itemOf(t, ref)
	cancel := actionOf(t, it, crew.TaskCancel)
	if !strings.Contains(it.Brief, "build is blocked: the API key is missing") || cancel.Label != "Cancel task build" || cancel.Expect == q || actionOf(t, it, "answer").Expect != q {
		t.Fatalf("the blocker's item: %+v", it)
	}
	if !slices.Contains(it.Blocking.Refs, "task:"+run.ID+"/build") || !slices.Contains(it.Blocking.Refs, "task:"+run.ID+"/docs") {
		t.Fatalf("blocking: %+v", it.Blocking)
	}
	// The run moves: another command (a pause), the question still shown.
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/pause", adminToken, map[string]any{"commandId": "p1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d %v", resp.StatusCode, out)
	}
	if status, code := e.command(t, run.ID, "build", crew.TaskCancel, "c1", cancel.Expect); status != http.StatusConflict || code != "stale_revision" {
		t.Fatalf("the cancel at the old revision: %d %s", status, code)
	}
	if st := taskVia(t, e, run.ID, "build", "running", func(task map[string]any) bool { return true }); st["state"] != "running" {
		t.Fatalf("cancelled at the old revision: %v", st)
	}
	again := e.itemOf(t, ref)
	fresh := actionOf(t, again, crew.TaskCancel)
	if fresh.Expect == cancel.Expect || again.Question != "Where is the key?" {
		t.Fatalf("read again: %+v", again)
	}
	if status, code := e.command(t, run.ID, "build", crew.TaskCancel, "c2", fresh.Expect); status != http.StatusOK {
		t.Fatalf("the cancel at the new revision: %d %s", status, code)
	}
}

// TestABlockersCancelCarriesTheRunsRevision is TestABlockerIsOneItem's
// second half named as the plan names it: the Cancel's Expect is the run's
// revision as the engine judged the commands, never the question's id.
func TestABlockersCancelCarriesTheRunsRevision(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	if resp, _ := e.do("POST", "/api/sessions/"+sid+"/task/blocked", e.agentToken(sid), map[string]any{"reason": "stuck"}); resp.StatusCode != http.StatusOK {
		t.Fatal("blocked")
	}
	var q string
	until(t, "the blocker asked", func() bool {
		q = e.local(sid).Info().Attention.ID
		return e.local(sid).Info().Attention.Message == "stuck"
	})
	it := e.itemOf(t, questionRef(sid, q))
	set, err := e.srv.runs.TaskCommands(run.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if c := actionOf(t, it, crew.TaskCancel); c.Expect != strconv.FormatInt(set.Revision, 10) {
		t.Fatalf("the cancel's Expect %q, the run at %d", c.Expect, set.Revision)
	}
}

// What waits for a failed root: a diamond's three dependents, then past
// 20, how many more.
func TestInboxBlockingNamesTheDependents(t *testing.T) {
	e := newTestEnv(t, nil)
	rd := e.srv.newInboxRead(context.Background())
	run := crew.Run{ID: "diamond-00000001", Name: "diamond", Managed: &crew.Managed{Revision: 4}, Tasks: []crew.TaskState{
		{ID: "root", State: "failed"}, {ID: "left", Needs: []string{"root"}, State: "blocked"},
		{ID: "right", Needs: []string{"root"}, State: "blocked"}, {ID: "join", Needs: []string{"left", "right"}, State: "blocked"},
		{ID: "apart", State: "running"},
	}}
	it, _ := rd.taskItem(run, run.Tasks[0])
	want := []string{"task:diamond-00000001/left", "task:diamond-00000001/right", "task:diamond-00000001/join", "run:diamond-00000001"}
	if !slices.Equal(it.Blocking.Refs, want) || it.Blocking.More != 0 || !strings.HasPrefix(it.Blocking.Words, "3 tasks wait for root") {
		t.Fatalf("the diamond: %+v", it.Blocking)
	}
	for i := range 25 {
		run.Tasks = append(run.Tasks, crew.TaskState{ID: "more" + strconv.Itoa(i), Needs: []string{"join"}, State: "blocked"})
	}
	it, _ = rd.taskItem(run, run.Tasks[0])
	if len(it.Blocking.Refs) != inboxRefs || it.Blocking.More != 3+25+1-inboxRefs {
		t.Fatalf("past 20: %d refs, %d more", len(it.Blocking.Refs), it.Blocking.More)
	}
}

// A retry's track: at its action's Expect, recorded and validated done,
// resumed waiting while the run is paused (no attempt starts), then done
// once the next attempt runs; at an old revision, validated refused with
// stale_revision and resumed not applicable.
func TestInboxTrackCommand(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failBuild(t, e, failingCrew(e.root))
	ref := "task:" + run + "/build"
	old := e.itemOf(t, ref)
	if resp, out := e.do("POST", "/api/runs/"+run+"/pause", adminToken, map[string]any{"commandId": "p1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d %v", resp.StatusCode, out)
	}
	if status, code := e.command(t, run, "build", crew.TaskRetry, "stale", old.Revision); status != http.StatusConflict || code != "stale_revision" {
		t.Fatalf("a retry at the old revision: %d %s", status, code)
	}
	stale := e.trackOf(t, ref, "stale", "")
	if stale[stepRecorded].State != stepDone || stale[stepValidated].State != stepRefused || !strings.Contains(stale[stepValidated].Words, "stale_revision") || stale[stepResumed].State != stepNotApplicable {
		t.Fatalf("the stale retry: %+v", stale)
	}
	it := e.itemOf(t, ref)
	retry := actionOf(t, it, crew.TaskRetry)
	if status, code := e.command(t, run, "build", crew.TaskRetry, "r1", retry.Expect); status != http.StatusOK {
		t.Fatalf("the retry: %d %s", status, code)
	}
	steps := e.trackOf(t, ref, "r1", "")
	if steps[stepRecorded].State != stepDone || steps[stepRecorded].Receipt != "command:"+run+"/r1" || steps[stepValidated].State != stepDone || steps[stepResumed].State != stepWaiting {
		t.Fatalf("while paused: %+v", steps)
	}
	if resp, out := e.do("POST", "/api/runs/"+run+"/unpause", adminToken, map[string]any{"commandId": "u1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("unpause: %d %v", resp.StatusCode, out)
	}
	runningAttempt(t, e, run, "build", 4)
	if got := e.trackOf(t, ref, "r1", "")[stepResumed]; got.State != stepDone || !strings.Contains(got.Words, "attempt 4") {
		t.Fatalf("once attempt 4 runs: %+v", got)
	}
	if none := e.trackOf(t, ref, "never-sent", ""); none[stepRecorded].State != stepUnknown {
		t.Fatalf("a command never sent: %+v", none)
	}
}

// A command the ledger took and the engine never settled, the server
// stopped between its two transactions: once the server is started again
// over its data directory, the recovery leaves the receipt uncertain, and
// the track reads validated unknown with its words and resumed from the
// task (no effect: not applicable), final, the same from a second read.
// The servers over the directory end the later first (AGENTS.md).
func TestInboxTrackACommandAcrossARestart(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failBuild(t, e, failingCrew(e.root))
	if resp, out := e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	// The command taken as act's first transaction takes it (its row
	// accepted), the change never made: the seam between the two.
	l, err := ledger.Open(filepath.Join(e.srv.cfg.DataDir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Transition(ctx, run, func(tx *ledger.Tx) error {
		_, _, err := tx.Command(adminActor, "taken", crew.TaskRetry, "fingerprint")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if row, ok, _ := l.Receipt(ctx, run, adminActor, "taken"); !ok || row.Status != ledger.CommandAccepted {
		t.Fatalf("taken: %+v %v", row, ok)
	}
	l.Close()
	again := e.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	ref := "task:" + run + "/build"
	for range 2 {
		steps := again.trackOf(t, ref, "taken", "")
		if steps[stepRecorded].State != stepDone || steps[stepValidated].State != stepUnknown || !strings.Contains(steps[stepValidated].Words, "the server stopped while the engine applied this") ||
			steps[stepResumed].State != stepNotApplicable || !strings.Contains(steps[stepResumed].Words, "nothing shows it took effect") {
			t.Fatalf("after the restart: %+v", steps)
		}
	}
}

// The resumed step of an uncertain command read from the task: an
// attempt started after it, the task accepted or cancelled after it, is
// done ("it took effect before the stop"); none is not applicable.
func TestInboxTrackUncertainReadsTheTask(t *testing.T) {
	at := time.Now().UTC()
	later := at.Add(time.Second)
	for _, c := range []struct {
		intent   string
		task     crew.TaskState
		attempts []crew.AttemptState
		want     string
	}{
		{crew.TaskRetry, crew.TaskState{State: "running"}, []crew.AttemptState{{N: 4, StartedAt: &later}}, stepDone},
		{crew.TaskRetry, crew.TaskState{State: "failed"}, nil, stepNotApplicable},
		{crew.TaskAccept, crew.TaskState{State: ledger.TaskSucceeded, Result: &crew.ResultState{}}, nil, stepDone},
		{crew.TaskAccept, crew.TaskState{State: "failed"}, nil, stepNotApplicable},
		{crew.TaskCancel, crew.TaskState{State: ledger.TaskCancelled}, nil, stepDone},
	} {
		got := uncertainResumed(ledger.CommandRow{Intent: c.intent, CreatedAt: at}, &c.task, c.attempts)
		if got.State != c.want {
			t.Errorf("%s on %+v: %+v", c.intent, c.task, got)
		}
	}
}
