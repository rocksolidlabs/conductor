package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// Every attempt and its evidence say where they ran (docs/plans/runtime.md,
// PR-4): the task read's attempts and their receipts, the steps route, the
// prerequisites route and the ledger's rows give the Where of the Runtime
// the run ran in.

// whereRun runs manifestRunCrew's task to its end on e's real ledger: the
// attempt's session, its trees' setup steps, its check and the run's
// prerequisite.
func whereRun(t *testing.T, e *testEnv) string {
	t.Helper()
	e.gitRepo(t)
	runID := launchOne(t, e, manifestRunCrew(e.root, filepath.Join(t.TempDir(), "down")))
	sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, runID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	return runID
}

// reportedWheres is every where the routes and the ledger give the run's
// attempts and receipts, by what gave it: the run's read, the task read's
// attempts and their steps, the steps route, the prerequisites route, and
// the attempts, checks and prerequisites rows.
func reportedWheres(t *testing.T, e *testEnv, runID string) map[string][]any {
	t.Helper()
	out := map[string][]any{}
	task := taskVia(t, e, runID, "build", "read", func(map[string]any) bool { return true })
	out["run's task"] = append(out["run's task"], task["attempt"].(map[string]any)["where"])
	resp, read := e.do("GET", "/api/runs/"+runID+"/tasks/build", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the task read: %d %v", resp.StatusCode, read)
	}
	for _, a := range read["attempts"].([]any) {
		a := a.(map[string]any)
		out["task read attempts"] = append(out["task read attempts"], a["where"])
		steps, _ := a["steps"].([]any)
		for _, s := range steps {
			out["task read steps"] = append(out["task read steps"], s.(map[string]any)["where"])
		}
	}
	for _, s := range stepsOf(t, e, runID, "build", 1) {
		out["steps route"] = append(out["steps route"], s["where"])
	}
	resp, prereqs := e.do("GET", "/api/runs/"+runID+"/prerequisites", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the prerequisites: %d %v", resp.StatusCode, prereqs)
	}
	for _, s := range prereqs["steps"].([]any) {
		out["prerequisites route"] = append(out["prerequisites route"], s.(map[string]any)["where"])
	}
	ctx := t.Context()
	attempts, err := e.srv.ledger.Attempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		out["attempts rows"] = append(out["attempts rows"], a.Where)
	}
	checks, err := e.srv.ledger.Checks(ctx, runID, "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		out["checks rows"] = append(out["checks rows"], c.Where)
	}
	rows, err := e.srv.ledger.Prerequisites(ctx, runID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rows {
		out["prerequisites rows"] = append(out["prerequisites rows"], p.Where)
	}
	return out
}

// allSay fails unless every kind of report gave something, and each said
// want.
func allSay(t *testing.T, got map[string][]any, want string) {
	t.Helper()
	for _, kind := range []string{"run's task", "task read attempts", "task read steps", "steps route", "prerequisites route", "attempts rows", "checks rows", "prerequisites rows"} {
		if len(got[kind]) == 0 {
			t.Fatalf("no %s: %v", kind, got)
		}
		for _, w := range got[kind] {
			if w != want {
				t.Fatalf("the %s say %v, want %q (all: %v)", kind, got[kind], want, got)
			}
		}
	}
	// A task's check, a setup step in each tree: three receipts.
	if n := len(got["steps route"]); n != 3 {
		t.Fatalf("%d receipts: %v", n, got)
	}
}

// An attempt and its checks ran where they report: a managed run of the
// ready cat on the server's real ledger, under a fake Runtime that takes
// it and does the work natively under its own Where. The fake started the
// attempt's session and ran its check and its prerequisite, and the run's
// read, the task read's attempts and steps, the steps route, the
// prerequisites route and the ledger's rows all say fake.
func TestAnAttemptAndItsChecksRanWhereTheyReport(t *testing.T) {
	seam := nativeSeam()
	offered := &sessiontest.FakeRuntime{Base: local.Native()}
	e := runtimeEnv(t, seam, nil, withRuntime(offered))
	runID := whereRun(t, e)
	allSay(t, reportedWheres(t, e, runID), "fake")
	if got := terminalRefs(offered); len(got) != 1 || got[0].Run != runID || got[0].Task != "build" || got[0].Attempt != 1 {
		t.Fatalf("the attempt's session started in %+v", got)
	}
	var ran []string
	for _, c := range offered.Asked("Run") {
		s := c.Spec.(ext.CommandSpec)
		ran = append(ran, s.Argv[len(s.Argv)-1])
	}
	for _, want := range []string{"echo checked", "echo tools-ok"} {
		if !slices.Contains(ran, want) {
			t.Fatalf("the fake ran %q, not %q", ran, want)
		}
	}
	if n := len(seam.Calls()); n != 0 {
		t.Fatalf("the native Runtime was asked %v", methodsOf(seam))
	}
}

// With nothing registered the same run reports native everywhere, read
// while the engine holds it and from the ledger once it has let go.
func TestTheTaskReadReportsWhere(t *testing.T) {
	e := readyEnv(t, nil)
	runID := whereRun(t, e)
	allSay(t, reportedWheres(t, e, runID), ledger.WhereNative)
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	e.srv.runs.ForgetForTest(runID)
	if _, ok := e.srv.runs.Get(runID); ok {
		t.Fatal("the run is still held")
	}
	_, read := e.do("GET", "/api/runs/"+runID+"/tasks/build", adminToken, nil)
	attempts, _ := read["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("the task read from the ledger: %v", read)
	}
	a := attempts[0].(map[string]any)
	steps, _ := a["steps"].([]any)
	if a["where"] != ledger.WhereNative || len(steps) != 3 {
		t.Fatalf("the attempt read from the ledger: %v", a)
	}
	for _, s := range steps {
		if s.(map[string]any)["where"] != ledger.WhereNative {
			t.Fatalf("a receipt read from the ledger: %v", s)
		}
	}
}

// A run the offered Runtime declines runs natively, and its rows and
// reports say native: the offered one is asked Takes and nothing else.
func TestADeclinedRunsRowsSayNative(t *testing.T) {
	seam := nativeSeam()
	offered := &sessiontest.FakeRuntime{Base: local.Native(), TakesRun: func(ext.ProcessRef) bool { return false }}
	e := runtimeEnv(t, seam, nil, withRuntime(offered))
	runID := whereRun(t, e)
	allSay(t, reportedWheres(t, e, runID), ledger.WhereNative)
	if got := methodsOf(offered); !slices.Equal(got, []string{"Takes"}) {
		t.Fatalf("the declining Runtime was asked %v", got)
	}
	if got := terminalRefs(seam); len(got) != 1 || got[0].Run != runID {
		t.Fatalf("the attempt's session started natively as %+v", got)
	}
}
