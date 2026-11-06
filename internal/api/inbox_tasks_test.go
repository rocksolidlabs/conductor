package api

import (
	"context"
	"net/http"
	"net/url"
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
	// The Cancel is tracked as the task's (its track), never as the
	// question it rides on: under the question's ref its receipt is
	// refused; under its track, refused stale_revision.
	if cancel.Track != "task:"+run.ID+"/build" || actionOf(t, it, "answer").Track != "" {
		t.Fatalf("the cancel's track: %+v", cancel)
	}
	if status, _ := e.get(t, "/api/inbox/track?ref="+url.QueryEscape(ref)+"&receipt=c1"); status != http.StatusBadRequest {
		t.Fatalf("a command's receipt under the question's ref: %d", status)
	}
	if got := e.trackOf(t, cancel.Track, "c1", ""); got[stepValidated].State != stepRefused || !strings.Contains(got[stepValidated].Words, "stale_revision") {
		t.Fatalf("the stale cancel's track: %+v", got)
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
	if got := e.trackOf(t, fresh.Track, "c2", ""); got[stepRecorded].State != stepDone || got[stepValidated].State != stepDone || got[stepResumed].State != stepNotApplicable {
		t.Fatalf("the cancel's track: %+v", got)
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
	// One dependent, or none, is said as one or none (the page shows the words as they are).
	if it, _ := rd.taskItem(run, run.Tasks[1]); !strings.HasPrefix(it.Blocking.Words, "1 task waits for left") {
		t.Fatalf("left: %q", it.Blocking.Words)
	}
	if it, _ := rd.taskItem(run, run.Tasks[4]); !strings.HasPrefix(it.Blocking.Words, "No task waits for apart") {
		t.Fatalf("apart: %q", it.Blocking.Words)
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
	ref := "task:" + run + "/build"
	// A retry held between its two transactions (the engine's seam):
	// recorded done, validated waiting (the engine applying it), not final;
	// let go, validated done. The run paused first, so nothing starts.
	if resp, out := e.do("POST", "/api/runs/"+run+"/pause", adminToken, map[string]any{"commandId": "p1"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d %v", resp.StatusCode, out)
	}
	retry := actionOf(t, e.itemOf(t, ref), crew.TaskRetry)
	taken, release := make(chan struct{}), make(chan struct{})
	e.srv.runs.SetAfterTakenForTest(func(_, id string) {
		if id == "held" {
			close(taken)
			<-release
		}
	})
	t.Cleanup(func() { e.srv.runs.SetAfterTakenForTest(nil) })
	sent := make(chan int, 1)
	go func() {
		status, _ := e.command(t, run, "build", crew.TaskRetry, "held", retry.Expect)
		sent <- status
	}()
	<-taken
	held := e.trackOf(t, ref, "held", "")
	if held[stepRecorded].State != stepDone || held[stepValidated].State != stepWaiting || !strings.Contains(held[stepValidated].Words, "applying it") || held[stepResumed].State != stepWaiting {
		t.Fatalf("held between its transactions: %+v", held)
	}
	close(release)
	if status := <-sent; status != http.StatusOK {
		t.Fatalf("the held retry: %d", status)
	}
	if got := e.trackOf(t, ref, "held", ""); got[stepValidated].State != stepDone || got[stepResumed].State != stepWaiting {
		t.Fatalf("let go: %+v", got)
	}
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
	for range 2 {
		steps := again.trackOf(t, ref, "taken", "")
		if steps[stepRecorded].State != stepDone || steps[stepValidated].State != stepUnknown || !strings.Contains(steps[stepValidated].Words, "the server stopped after the engine took this") ||
			steps[stepResumed].State != stepNotApplicable || !strings.Contains(steps[stepResumed].Words, "nothing of it was made") {
			t.Fatalf("after the restart: %+v", steps)
		}
	}
	// The item of a task whose run the engine no longer holds is read
	// from the run's record: no actions, its now saying why.
	if _, held := again.srv.runs.Get(run); !held {
		it := again.itemOf(t, ref)
		if len(it.Actions) != 0 || !strings.Contains(it.Now, "no longer held by the engine") {
			t.Fatalf("the item after the restart: %+v", it)
		}
	}
}

// An uncertain command made nothing (its change commits with its
// receipt): its resumed step is not applicable whatever the task shows,
// a success or a cancel that predates it or another command's since.
func TestInboxTrackUncertainMadeNothing(t *testing.T) {
	if got := uncertainResumed(); got.State != stepNotApplicable || !strings.Contains(got.Words, "nothing of it was made") {
		t.Fatalf("uncertain: %+v", got)
	}
}

// An Accept on a run the engine let go (stopped, then forgotten): the
// item is read from the run's record, with no actions; the Accept's
// resumed step reads the dependents from it too (docs needs build and
// has not started: waiting), never "nothing else needs it"; a run found
// nowhere is unknown.
func TestInboxAcceptOnARunLetGo(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run := failBuild(t, e, failingCrew(e.root))
	ref := "task:" + run + "/build"
	if resp, out := e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	accept := actionOf(t, e.itemOf(t, ref), crew.TaskAccept)
	if status, code := e.command(t, run, "build", crew.TaskAccept, "a1", accept.Expect); status != http.StatusOK {
		t.Fatalf("the accept: %d %s", status, code)
	}
	e.srv.runs.ForgetForTest(run)
	if _, held := e.srv.runs.Get(run); held {
		t.Fatal("the run still held")
	}
	it := e.itemOf(t, ref)
	if len(it.Actions) != 0 || !strings.Contains(it.Now, "no longer held by the engine") {
		t.Fatalf("the item of a run let go: %+v", it)
	}
	got := e.trackOf(t, ref, "a1", "")
	if got[stepValidated].State != stepDone || got[stepResumed].State != stepWaiting || strings.Contains(got[stepResumed].Words, "nothing else needs it") {
		t.Fatalf("the accept's track: %+v", got)
	}
	if got := doneResumed(ledger.CommandRow{Intent: crew.TaskAccept}, crew.Receipt{}, nil, nil, nil, false); got.State != stepUnknown {
		t.Fatalf("a run found nowhere: %+v", got)
	}
}
