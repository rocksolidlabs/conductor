package crew

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// terminalRunOnMain is what two runs of crews with no way made, on the
// engine before members could run as chats: a managed run whose done
// without a result is reminded while its program reads slowly, and a run
// of two members with a handoff, then stopped. For each, the lines written
// into its terminals, the run's log, and what it ended with; for the
// managed run the ledger's events with their payloads and its rows, each
// with the run's, the sessions' and the commits' ids taken out
// (terminalRun). It was written by this test on that engine, not by the
// engine it now tests.
const terminalRunOnMain = `typed:
"<line>\r"
"Conductor: a done does not finish task \"build\". Commit your work and submit it with the task_result tool or \"conductor task result --summary ...\"; a review submits --verdict.\r"
log:
status launched API sweep: 1 members, 0 starting now
status task build: attempt 1 reserved for lead
status task build: attempt 1 started as lead
status typed lead's prompt
status task build: lead reports done without a result; reminded once
status task build: attempt 1 submitted commit <commit>
status task build: attempt 1 succeeded
status every task is done: the run succeeded
events:
run.created base=<commit> tasks=1
attempt.reserved attempt=1 inputs=0 member=lead start=<commit> task=build
attempt.workspace attempt=1 branch=crew/<run>/build.1 start=<commit> task=build worktree=<dir>/.conductor/worktrees/<run>/build.1
attempt.launched attempt=1 session=<session> task=build
attempt.prompted attempt=1 task=build
result.submitted attempt=1 commit=<commit> findings=0 issues=0 summary=built it task=build verdict=
task.succeeded attempt=1 commit=<commit> error= stage= state=succeeded task=build
run.finished outcome=succeeded
rows:
attempt succeeded <session> crew/<run>/build.1
task succeeded
launch lead · build #1 "<dir>/.conductor/worktrees/<run>/build.1" yolo=false
handoff run typed:
"lead Plan it.\r"
"tests Test it.\r"
"tests Handoff from lead: the handlers are in\r"
handoff run log:
status handoff delivered from lead to tests
status launched API sweep: 2 members, 2 starting now
status lead started
status stopped
status tests started
status typed lead's prompt
status typed tests's prompt
handoff run: stopped
member lead ended
member tests ended
launch lead "/work" yolo=false
launch tests "/work" yolo=false
`

// A crew with no way runs as it did before a member could run as a chat:
// the same writes into its terminals, the same lines in the run's log, the
// same ledger events and rows, and every launch with no way.
func TestTerminalCrewUnchanged(t *testing.T) {
	got, launchers := terminalRun(t)
	for _, fl := range launchers {
		fl.mu.Lock()
		for _, call := range fl.calls {
			if call.way != "" {
				t.Errorf("%s was launched with way %q", call.name, call.way)
			}
		}
		fl.mu.Unlock()
	}
	if os.Getenv("CONDUCTOR_PRINT_TERMINAL_RUN") != "" {
		fmt.Printf("----8<----\n%s----8<----\n", got)
	}
	if got != terminalRunOnMain {
		t.Fatalf("the terminal run changed:\n%s\nwant:\n%s", got, terminalRunOnMain)
	}
}

// terminalRun runs two runs whose members run in their terminals: a
// managed run of one task, its task line typed, a done without a result
// reminded once while its program is slow to read (the reminder accepted
// pending), its result submitted and the run finished; and a run of two
// members, one's handoff typed into the other's terminal, then stopped. It
// answers what the runs wrote, each id taken out, and their launchers.
func terminalRun(t *testing.T) (string, []*fakeLauncher) {
	t.Helper()
	var b strings.Builder
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	// The program slow to read the reminder: its write waits past the
	// session's InputWait, accepted pending, and lands once read.
	fl.inputWait = 200 * time.Millisecond
	c := repoCrew(dir, work("build", "lead"))
	run := launchManaged(t, e, c)
	build := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	local, p := fl.attemptSession(t, build)
	var lines []string
	line := waitTyped(t, p, 10*time.Second)
	if line != taskLine(c.Tasks[0], 1)+"\r" {
		t.Fatalf("the task line: %q", line)
	}
	lines = append(lines, "<line>\r")
	// The terminal's input full: nothing more is read until the test reads.
	for len(p.Input) < cap(p.Input) {
		p.Input <- nil
	}
	local.SetAttention(session.AttentionDone, "all done", session.SourceAPI)
	waitLogged(t, e, run.ID, "reminded once", 1, 10*time.Second)
	lines = append(lines, waitTyped(t, p, 10*time.Second))
	writeFile(t, filepath.Join(build.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, build.Attempt.Worktree, "build it")
	submit(t, e, build.Attempt.SessionID, Result{Summary: "built it"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	waitFake(t, "the run's end committed", func() bool { return slices.Contains(l.kinds(run.ID), "run.finished") })
	lines = append(lines, typed(p)...)

	ids := map[string]string{run.ID: "<run>", build.Attempt.SessionID: "<session>", dir: "<dir>"}
	hex := regexp.MustCompile(`\b[0-9a-f]{40}\b|\b[0-9a-f]{12}\b`)
	clean := func(s string) string {
		for id, as := range ids {
			s = strings.ReplaceAll(s, id, as)
		}
		return hex.ReplaceAllString(s, "<commit>")
	}
	b.WriteString("typed:\n")
	for _, s := range lines {
		fmt.Fprintf(&b, "%q\n", s)
	}
	b.WriteString("log:\n")
	r, _ := e.Get(run.ID)
	for _, en := range r.Log {
		fmt.Fprintf(&b, "%s %s\n", en.Type, clean(en.Message))
	}
	b.WriteString("events:\n")
	l.mu.Lock()
	for _, ev := range l.events {
		if ev.runID != run.ID {
			continue
		}
		fmt.Fprintf(&b, "%s %s\n", ev.kind, clean(payloadWords(ev.payload)))
	}
	attempt := l.attempts[fmt.Sprintf("%s/%s/%d", run.ID, "build", 1)]
	task := l.tasks[run.ID+"/build"]
	l.mu.Unlock()
	fmt.Fprintf(&b, "rows:\nattempt %s %s %s\ntask %s\n", attempt.State, clean(attempt.SessionID), clean(attempt.Branch), task.State)
	fl.mu.Lock()
	for _, call := range fl.calls {
		fmt.Fprintf(&b, "launch %s %q yolo=%v\n", call.name, clean(call.cwd), call.yolo)
	}
	fl.mu.Unlock()
	stopRun(t, e, run.ID)

	// A run of two members: lead's handoff typed into tests's terminal, and
	// the run stopped.
	e2, fl2 := newEngine(t)
	fl2.onLaunch = askAtOnce
	run2, err := e2.Launch(t.Context(), testCrew(immediate("lead", "Plan it."), immediate("tests", "Test it.")))
	if err != nil {
		t.Fatal(err)
	}
	var typed2 []string
	for _, name := range []string{"lead", "tests"} {
		waitStatus(t, e2, run2.ID, name, MemberRunning, 10*time.Second)
		ml, mp := fl2.member(name)
		typed2 = append(typed2, name+" "+waitTyped(t, mp, 10*time.Second))
		ml.SetAttention(session.AttentionWorking, "", session.SourceAPI)
	}
	lead, _ := fl2.member("lead")
	_, tp := fl2.member("tests")
	lead.Record(handoffEntry("tests", "the handlers are in"))
	typed2 = append(typed2, "tests "+waitTyped(t, tp, 10*time.Second))
	waitLogged(t, e2, run2.ID, "handoff delivered from lead to tests", 1, 10*time.Second)
	stopRun(t, e2, run2.ID)
	clean2 := func(s string) string { return strings.ReplaceAll(s, run2.ID, "<run>") }
	b.WriteString("handoff run typed:\n")
	for _, s := range typed2 {
		fmt.Fprintf(&b, "%q\n", s)
	}
	// Its log's lines in order of their words: its two members start at
	// once, in no set order.
	b.WriteString("handoff run log:\n")
	r2, _ := e2.Get(run2.ID)
	var logged []string
	for _, en := range r2.Log {
		logged = append(logged, fmt.Sprintf("%s %s\n", en.Type, clean2(en.Message)))
	}
	sort.Strings(logged)
	b.WriteString(strings.Join(logged, ""))
	fmt.Fprintf(&b, "handoff run: %s\n", r2.State)
	for _, m := range r2.Members {
		fmt.Fprintf(&b, "member %s %s\n", m.Name, m.Status)
	}
	fl2.mu.Lock()
	for _, call := range fl2.calls {
		fmt.Fprintf(&b, "launch %s %q yolo=%v\n", call.name, call.cwd, call.yolo)
	}
	fl2.mu.Unlock()
	return b.String(), []*fakeLauncher{fl, fl2}
}

// payloadWords is an event's payload with its keys in order, each value
// as fmt prints it; a time is "<time>".
func payloadWords(payload any) string {
	m, ok := payload.(map[string]any)
	if !ok {
		return fmt.Sprint(payload)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := m[k]
		if _, isTime := v.(time.Time); isTime {
			v = "<time>"
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}
