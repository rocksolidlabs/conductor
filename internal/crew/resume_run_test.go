package crew

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// stopRun stops the run and waits for its stop to complete.
func stopRun(t *testing.T, e *Engine, runID string) {
	t.Helper()
	if err := e.Stop(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Get(runID)
	if got.StoppedAt == nil || got.State != RunStopped {
		t.Fatalf("not stopped: %+v", got)
	}
}

func TestResumeMemberReopensACompletedStop(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	stopRun(t, e, run.ID)
	id, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatalf("resume on a stopped run: %v", err)
	}
	got, _ := e.Get(run.ID)
	// Reopened: no longer stopped (the fake sessions ask at once, so the
	// state is needs_input rather than running).
	if got.StoppedAt != nil || got.State == RunStopped || got.State == RunFinished {
		t.Fatalf("not reopened: %+v", got)
	}
	if !logged(got, "reopened: lead resumes") || !logged(got, "lead resumed its conversation") {
		t.Fatalf("log %+v", got.Log)
	}
	// Running once its session is ready for a prompt (#155), which the
	// fake session is at once.
	lead := waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	core := memberState(t, e, run.ID, "core")
	if lead.SessionID != id || core.Status != MemberEnded {
		t.Fatalf("lead %+v core %+v", lead, core)
	}
	// The other ended member is resumed on its own, afresh.
	if _, err := e.ResumeMember(t.Context(), run.ID, "core", ""); err != nil {
		t.Fatal(err)
	}
	_, p := fl.member("core")
	if got := waitTyped(t, p, 5*time.Second); got != "Build it.\r" {
		t.Fatalf("core anew: typed %q", got)
	}
	// A reopened run stops again.
	stopRun(t, e, run.ID)
	if st := memberState(t, e, run.ID, "lead"); st.Status != MemberEnded {
		t.Fatalf("lead after the second stop: %+v", st)
	}
	if got, _ := e.Get(run.ID); strings.Count(entriesText(got), "stopped") < 2 {
		t.Fatalf("log %+v", got.Log)
	}
}

func entriesText(r Run) string {
	var b strings.Builder
	for _, e := range r.Log {
		b.WriteString(e.Message + "\n")
	}
	return b.String()
}

func TestResumeMemberRefusesAStopCutShort(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	fl.block = map[string]bool{"slow": true}
	run, _, err := e.LaunchHeld(t.Context(), testCrew(immediate("lead", "Plan it."), manual("slow", "Later.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	// A start that never ends: the stop, given no time, is cut short.
	go func() { _ = e.StartMember(context.Background(), run.ID, "slow") }()
	waitStatus(t, e, run.ID, "slow", MemberStarting, 5*time.Second)
	cut, cancel := context.WithCancel(t.Context())
	cancel()
	_ = e.Stop(cut, run.ID)
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", ""); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("a stop cut short reopened: %v", err)
	}
	if _, err := e.ResumeRun(t.Context(), run.ID); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("a stop cut short resumed as a new run: %v", err)
	}
}

func TestResumeRunResumesEveryConversationInItsWorktree(t *testing.T) {
	repo := newRepo(t)
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	c := testCrew(immediate("lead", "Plan it."), after("core", "Build it.", "lead"), immediate("docs", "Write it."), manual("qa", "Check it."))
	c.Isolation, c.Cwd = IsolationWorktree, repo
	run, err := e.Launch(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, run.ID, "docs", MemberRunning, 5*time.Second)
	lead, _ := fl.member("lead")
	lead.SetAgentSession(session.AgentSession{ID: "3f80c8bd-0000-4000-8000-00000000lead", Resumable: true, Source: session.AgentSessionHook})
	leadWorktree := memberState(t, e, run.ID, "lead").Worktree
	stopRun(t, e, run.ID)
	worktrees := runGit(t, repo, "worktree", "list", "--porcelain")
	if _, err := e.ResumeRun(t.Context(), "nope"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	next, err := e.ResumeRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == run.ID || next.CrewID != run.CrewID || next.ResumedFrom != run.ID || next.Goal != run.Goal || next.Isolation != IsolationWorktree {
		t.Fatalf("next %+v", next)
	}
	old, _ := e.Get(run.ID)
	if old.ResumedBy != next.ID || old.State != RunStopped || !logged(old, "resumed as "+next.ID) {
		t.Fatalf("old %+v", old)
	}
	// lead continues in its kept worktree and branch, without a prompt. Its
	// launch runs on its own goroutine: the state is read once it runs, as
	// the first launch's was (read at once, a loaded machine found it still
	// starting: the PR #320 checks, twice).
	waitStatus(t, e, next.ID, "lead", MemberRunning, 5*time.Second)
	nl := memberState(t, e, next.ID, "lead")
	if nl.Status != MemberRunning || nl.Worktree != leadWorktree || nl.Branch != "crew/"+run.ID+"/lead" || nl.SessionID == "" {
		t.Fatalf("lead %+v", nl)
	}
	fl.mu.Lock()
	var leadCall launchCall
	for _, c := range fl.calls {
		if c.ref.RunID == next.ID && c.name == "lead" {
			leadCall = c
		}
	}
	fl.mu.Unlock()
	if leadCall.resume != "3f80c8bd-0000-4000-8000-00000000lead" || leadCall.cwd != leadWorktree || leadCall.ref.RunID != next.ID {
		t.Fatalf("lead's launch %+v", leadCall)
	}
	_, lp := fl.member("lead")
	time.Sleep(100 * time.Millisecond)
	if w := typed(lp); len(w) != 0 {
		t.Fatalf("a resumed lead was typed %q", w)
	}
	if got := runGit(t, repo, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != strings.Count(worktrees, "worktree ")+1 {
		t.Fatalf("worktrees: only docs (afresh) makes a new one:\n%s", got)
	}
	// docs had no resumable conversation: it starts afresh, with its prompt, in a worktree of the new run.
	waitStatus(t, e, next.ID, "docs", MemberRunning, 5*time.Second)
	nd := memberState(t, e, next.ID, "docs")
	if nd.Worktree != filepath.Join(repo, ".conductor", "worktrees", next.ID, "docs") {
		t.Fatalf("docs %+v", nd)
	}
	_, dp := fl.member("docs")
	if got := waitTyped(t, dp, 5*time.Second); got != "Write it.\r" {
		t.Fatalf("docs: typed %q", got)
	}
	// core waits for lead's next done; qa for a person.
	if st := memberState(t, e, next.ID, "core"); st.Status != MemberPending {
		t.Fatalf("core %+v", st)
	}
	if st := memberState(t, e, next.ID, "qa"); st.Status != MemberPending {
		t.Fatalf("qa %+v", st)
	}
	nlead, _ := fl.member("lead")
	nlead.SetAttention(session.AttentionDone, "planned", session.SourceAPI)
	waitStatus(t, e, next.ID, "core", MemberRunning, 5*time.Second)
	if got, _ := e.Get(next.ID); !logged(got, "lead is done: starting core") || !logged(got, "resumes "+run.ID) {
		t.Fatalf("log %+v", got.Log)
	}
	if _, err := e.ResumeRun(t.Context(), next.ID); !errors.Is(err, ErrRunRunning) {
		t.Fatalf("a running run resumed: %v", err)
	}
}

func TestResumeRunOfAFinishedRunStartsTheOthersAnew(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("core", "Build it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, run.ID, "core", MemberRunning, 5*time.Second)
	lead, lp := fl.member("lead")
	lead.SetAgentSession(session.AgentSession{ID: "3f80c8bd-0000-4000-8000-00000000lead", Resumable: true, Source: session.AgentSessionHook})
	core, cp := fl.member("core")
	lp.End(0)
	cp.End(0)
	<-lead.Ended()
	<-core.Ended()
	if got, _ := e.Get(run.ID); got.State != RunFinished {
		t.Fatalf("not finished: %+v", got)
	}
	next, err := e.ResumeRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Starting until its session is ready for a prompt (#155), then running.
	waitStatus(t, e, next.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, next.ID, "core", MemberRunning, 5*time.Second)
	_, ncp := fl.member("core")
	if got := waitTyped(t, ncp, 5*time.Second); got != "Build it.\r" {
		t.Fatalf("core anew: typed %q", got)
	}
	if st := memberState(t, e, next.ID, "core"); st.SessionID == "" || st.Status != MemberRunning {
		t.Fatalf("core %+v", st)
	}
}

// A resumed member is starting until its session is ready for a prompt, as
// a started member is until its prompt is typed (#155): a handoff typed
// into it meanwhile waits, queued, and is typed once the session is ready;
// the readiness wait is held by the test until then. A resumed session
// that ends before it is ready ends the member, with the reason.
func TestAResumedMemberIsRunningOnceItsSessionIsReady(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	ready := make(chan struct{})
	var gating atomic.Bool
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if !gating.Load() {
			return AwaitReady(ctx, l, held)
		}
		select {
		case <-ready:
			return nil
		case <-l.Ended():
			return session.ErrSessionEnded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("qa", "Check it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	waitStatus(t, e, run.ID, "qa", MemberRunning, 5*time.Second)
	lead, p := fl.member("lead")
	p.End(0)
	<-lead.Ended()
	gating.Store(true)
	id, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if m := memberState(t, e, run.ID, "lead"); m.Status != MemberStarting || m.SessionID != id {
		t.Fatalf("resumed, before its session is ready: %+v", m)
	}
	// A handoff to lead meanwhile: queued, nothing typed.
	qa, _ := fl.member("qa")
	e.OnActivity(qa.Info().ID, handoffEntry("lead", "over to you"), "", "")
	time.Sleep(100 * time.Millisecond)
	_, p2 := fl.member("lead")
	if w := typed(p2); len(w) != 0 {
		t.Fatalf("typed into lead before it was ready: %q", w)
	}
	if got, _ := e.Get(run.ID); !logged(got, "handoff queued from qa to lead: lead has not had its prompt yet") {
		t.Fatalf("log %+v", got.Log)
	}
	close(ready)
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	// The fake session asks at once; a handoff waits while a member waits
	// on a prompt, so lead is at work before it is typed.
	l2, _ := fl.member("lead")
	l2.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	if got := waitTyped(t, p2, 5*time.Second); got != "Handoff from qa: over to you\r" {
		t.Fatalf("typed once ready: %q", got)
	}

	// Ended before it was ready: the member ends with the reason.
	ready = make(chan struct{})
	_, p3 := fl.member("lead")
	p3.End(0)
	<-l2.Ended()
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	l3, p4 := fl.member("lead")
	p4.End(3)
	<-l3.Ended()
	m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second)
	if !strings.Contains(m.Err, "before it was ready again") {
		t.Fatalf("ended: %+v", m)
	}
}

// The source of a resume is not evicted while its continuation is made: at
// the cap, the launch of the continuation forgets another idle run, and the
// source is still there to name the continuation (Codex's review of #153's
// change).
func TestTheSourceOfAResumeIsNotEvictedBeforeItIsMarked(t *testing.T) {
	old := maxRuns
	maxRuns = 3
	t.Cleanup(func() { maxRuns = old })
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	var ids []string
	for range 3 {
		run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
		waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
		stopRun(t, e, run.ID)
	}
	oldest := ids[0]
	next, err := e.ResumeRun(t.Context(), oldest)
	if err != nil {
		t.Fatalf("resume the oldest at the cap: %v", err)
	}
	src, ok := e.Get(oldest)
	if !ok {
		t.Fatal("the source was evicted as its continuation was made")
	}
	if src.ResumedBy != next.ID {
		t.Fatalf("the source names %q, want %s", src.ResumedBy, next.ID)
	}
	if _, ok := e.Get(ids[1]); ok {
		t.Fatal("the next idle run was kept: nothing was evicted for the continuation")
	}
}

// A run stopped while a resumed member waits for its session to be ready
// ends that member with the stop (nothing of a completed stop is left
// starting), so the member is resumed again afterwards and the run can be
// evicted (Codex's review of #153's change).
func TestAStopDuringAResumedMembersReadinessWaitEndsIt(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	var gating atomic.Bool
	e.await = func(ctx context.Context, l *session.Local, held func(string)) error {
		if !gating.Load() {
			return AwaitReady(ctx, l, held)
		}
		select {
		case <-l.Ended():
			return session.ErrSessionEnded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	lead, p := fl.member("lead")
	p.End(0)
	<-lead.Ended()
	gating.Store(true)
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if m := memberState(t, e, run.ID, "lead"); m.Status != MemberStarting {
		t.Fatalf("before the stop: %+v", m)
	}
	stopRun(t, e, run.ID)
	if m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second); m.Ended == nil {
		t.Fatalf("after the stop: %+v", m)
	}
	gating.Store(false)
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", ""); err != nil {
		t.Fatalf("resumed again after the stop: %v", err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
}

// A second resume of a member while the first makes its session is refused,
// whatever the ended session still on the member says, and never touches
// the member: the first's failure ends the member, which is then resumed
// again (Codex's review of #153's change).
func TestASecondResumeOfAMemberWhileTheFirstLaunchesIsRefused(t *testing.T) {
	e, fl := newEngine(t)
	fl.onLaunch = askAtOnce
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "Plan it.")))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
	lead, p := fl.member("lead")
	p.End(0)
	<-lead.Ended()
	// The first resume's launch blocks until its context ends.
	fl.mu.Lock()
	if fl.block == nil {
		fl.block = map[string]bool{}
	}
	fl.block["lead"] = true
	fl.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := e.ResumeMember(ctx, run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001")
		first <- err
	}()
	waitStatus(t, e, run.ID, "lead", MemberStarting, 5*time.Second)
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", "3f80c8bd-0000-4000-8000-000000000001"); !errors.Is(err, ErrMemberRunning) {
		t.Fatalf("a second resume while the first launches: %v", err)
	}
	cancel()
	if err := <-first; err == nil {
		t.Fatal("the first resume, its launch cut short, succeeded")
	}
	if m := waitStatus(t, e, run.ID, "lead", MemberEnded, 5*time.Second); m.Err == "" {
		t.Fatalf("after the failed resume: %+v", m)
	}
	fl.mu.Lock()
	fl.block["lead"] = false
	fl.mu.Unlock()
	if _, err := e.ResumeMember(t.Context(), run.ID, "lead", ""); err != nil {
		t.Fatalf("resumed again: %v", err)
	}
	waitStatus(t, e, run.ID, "lead", MemberRunning, 5*time.Second)
}
