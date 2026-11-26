package crew

import (
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// deniedEntry is the entry a member's hook reports when its agent's own
// permission check refused a call (Claude Code's PermissionDenied, as
// notify.MapClaudeHook maps it).
func deniedEntry(tool string) session.ActivityEntry {
	return session.ActivityEntry{Type: session.ActivityToolDenied, ByName: "agent", Tool: tool, At: time.Now().UTC()}
}

// A member's call to one of Conductor's own tools refused by its agent's
// own permission check is said in the run's log, once for the attempt and
// the tool, with the rule that allows it and where: a run that would have
// waited with only "reports done without a result" says why (#1143). A
// refusal of another tool, or of another server's, is the agent's own
// business and says nothing; another of Conductor's tools is said too.
func TestARefusedCallToConductorIsSaidInTheRunsLog(t *testing.T) {
	e, _, _ := managedEngine(t)
	run := launchManaged(t, e, repoCrew(newRepo(t), work("build", "lead")))
	defer stopRun(t, e, run.ID)
	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	sid := b.Attempt.SessionID
	words := "task build: lead's call to Conductor's task_result was refused by its agent's own permission check; " +
		"allow mcp__conductor__task_result in its permissions (in Claude Code, /permissions) for it to call again"
	for range 2 {
		e.OnActivity(sid, deniedEntry("mcp__conductor__task_result"), "")
	}
	for _, tool := range []string{"Bash", "mcp__other__task_result", "mcp__conductor__", "conductor__task_result"} {
		e.OnActivity(sid, deniedEntry(tool), "")
	}
	e.OnActivity(sid, deniedEntry("mcp__conductor__report"), "")
	if n := logExact(t, e, run.ID, words); n != 1 {
		t.Fatalf("%d lines of %q", n, words)
	}
	if n := logCount(t, e, run.ID, "lead's call to Conductor's report was refused by its agent's own permission check; allow mcp__conductor__report in"); n != 1 {
		t.Fatalf("%d lines for the report", n)
	}
	if n := logCount(t, e, run.ID, "permission check"); n != 2 {
		r, _ := e.Get(run.ID)
		t.Fatalf("%d lines of a refusal, want 2: %+v", n, r.Log)
	}
}

// In a run of no tasks the member's refusal is said under its name alone.
func TestARefusedCallToConductorIsSaidInALegacyRun(t *testing.T) {
	e, fl := newEngine(t)
	run := runningPair(t, e, fl)
	lead, _ := fl.member("lead")
	lead.Record(deniedEntry("mcp__conductor__set_state"))
	lead.Record(deniedEntry("mcp__conductor__set_state"))
	words := "lead's call to Conductor's set_state was refused by its agent's own permission check; " +
		"allow mcp__conductor__set_state in its permissions (in Claude Code, /permissions) for it to call again"
	waitLogged(t, e, run.ID, "permission check", 1, 10*time.Second)
	if n := logExact(t, e, run.ID, words); n != 1 {
		r, _ := e.Get(run.ID)
		t.Fatalf("%d lines of %q: %+v", n, words, r.Log)
	}
}

// logExact counts the entries of a run's log whose message is text.
func logExact(t *testing.T, e *Engine, runID, text string) int {
	t.Helper()
	r, ok := e.Get(runID)
	if !ok {
		t.Fatalf("run %s not found", runID)
	}
	n := 0
	for _, entry := range r.Log {
		if entry.Message == text {
			n++
		}
	}
	return n
}
