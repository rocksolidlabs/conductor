package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// raw is do for a route that answers other than JSON: the reply and its
// body, with the headers given set on the request.
func (e *testEnv) raw(path, token string, header map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.http.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// manifestRunCrew is a managed crew of one task, build, with a check, and a
// project manifest: a prerequisite and a setup step that print and pass,
// the setup failing in a worktree while mark exists.
func manifestRunCrew(root, mark string) crew.Crew {
	c := crew.Crew{ID: "receipts", Name: "Receipts", Goal: "build it", Cwd: root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it.",
			Checks: []crew.Check{{ID: "unit", Argv: []string{"sh", "-c", "echo checked"}}}}}}
	c.Manifest = &crew.Manifest{
		Prerequisites: []crew.Check{{ID: "tools", Argv: []string{"sh", "-c", "echo tools-ok"}}},
		Setup:         []crew.Check{{ID: "deps", Argv: []string{"sh", "-c", `case "$(pwd)" in *.check) echo set-up; exit 0;; esac; if [ -e "` + mark + `" ]; then echo no registry; exit 1; fi; echo set-up`}}},
	}
	return c
}

// stepsOf reads an attempt's receipts through the route.
func stepsOf(t *testing.T, e *testEnv, runID, taskID string, n int) []map[string]any {
	t.Helper()
	resp, out := e.do("GET", "/api/runs/"+runID+"/tasks/"+taskID+"/attempts/"+strconv.Itoa(n)+"/steps", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("steps of %s/%d: %d %v", taskID, n, resp.StatusCode, out)
	}
	var steps []map[string]any
	for _, s := range out["steps"].([]any) {
		steps = append(steps, s.(map[string]any))
	}
	return steps
}

// The receipt routes answer from the ledger: an attempt's steps (its
// trees' setup and its check, by when each started) and each one's log
// with its SHA-256 in a header the app's script can read from a dev
// origin; the run's prerequisites the same way; 404 for what the ledger
// has not, 410 for a log dropped, admin only; and a run the engine has
// let go answers the same, its task read from the ledger.
func TestTheReceiptRoutesAnswerFromTheLedger(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), manifestRunCrew(e.root, filepath.Join(t.TempDir(), "down")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	base := "/api/runs/" + run.ID
	check := func(when string) {
		steps := stepsOf(t, e, run.ID, "build", 1)
		if len(steps) != 3 || steps[0]["phase"] != "setup" || steps[0]["k"] != float64(1) || steps[1]["phase"] != "setup" || steps[1]["k"] != float64(2) ||
			steps[2]["phase"] != "check" || steps[2]["id"] != "unit" || steps[0]["manifest"] != "crew:0" || steps[0]["exit"] != float64(0) || steps[0]["dir"] == steps[1]["dir"] {
			t.Fatalf("%s: the steps: %v", when, steps)
		}
		resp, body := e.raw(base+"/tasks/build/attempts/1/steps/deps/1/log", adminToken, nil)
		if resp.StatusCode != http.StatusOK || string(body) != "set-up\n" || resp.Header.Get(logHashHeader) != shaHex(body) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") ||
			steps[0]["logSha256"] != shaHex(body) || steps[0]["logBytes"] != float64(len(body)) {
			t.Fatalf("%s: the log: %d %q %s", when, resp.StatusCode, body, resp.Header.Get(logHashHeader))
		}
		resp, out := e.do("GET", base+"/prerequisites", adminToken, nil)
		prereqs, _ := out["steps"].([]any)
		if resp.StatusCode != http.StatusOK || len(prereqs) != 1 || prereqs[0].(map[string]any)["phase"] != "prerequisite" || prereqs[0].(map[string]any)["id"] != "tools" {
			t.Fatalf("%s: the prerequisites: %d %v", when, resp.StatusCode, out)
		}
		resp, body = e.raw(base+"/prerequisites/tools/1/log", adminToken, nil)
		if resp.StatusCode != http.StatusOK || string(body) != "tools-ok\n" || resp.Header.Get(logHashHeader) != shaHex(body) {
			t.Fatalf("%s: the prerequisite's log: %d %q", when, resp.StatusCode, body)
		}
		for _, path := range []string{base + "/tasks/build/attempts/1/steps/deps/9/log", base + "/tasks/build/attempts/7/steps", base + "/prerequisites/tools/2/log", "/api/runs/nope-00000000/prerequisites"} {
			if resp, body := e.raw(path, adminToken, nil); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s: %s: %d %s", when, path, resp.StatusCode, body)
			}
		}
		for _, path := range []string{base + "/tasks/build/attempts/0/steps", base + "/tasks/build/attempts/1/steps/Deps/1/log", base + "/prerequisites/tools/01/log"} {
			if resp, body := e.raw(path, adminToken, nil); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s: %s: %d %s", when, path, resp.StatusCode, body)
			}
		}
		for _, path := range []string{base + "/tasks/build/attempts/1/steps", base + "/tasks/build/attempts/1/steps/deps/1/log", base + "/prerequisites", base + "/prerequisites/tools/1/log"} {
			if resp, _ := e.raw(path, "", nil); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s: %s without a token: %d", when, path, resp.StatusCode)
			}
		}
		// The hash header reaches a dev origin's script.
		resp, _ = e.raw(base+"/tasks/build/attempts/1/steps/deps/1/log", adminToken, map[string]string{"Origin": "http://localhost:3000"})
		if !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), logHashHeader) {
			t.Fatalf("%s: exposed headers %q", when, resp.Header.Get("Access-Control-Expose-Headers"))
		}
		// The task's read carries the receipts.
		resp, out = e.do("GET", base+"/tasks/build", adminToken, nil)
		attempts, _ := out["attempts"].([]any)
		if resp.StatusCode != http.StatusOK || len(attempts) != 1 {
			t.Fatalf("%s: the task: %d %v", when, resp.StatusCode, out)
		}
		if got, _ := attempts[0].(map[string]any)["steps"].([]any); len(got) != 3 {
			t.Fatalf("%s: the task's receipts: %v", when, attempts[0])
		}
	}
	check("held")
	if resp, out := e.do("POST", base+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	e.srv.runs.ForgetForTest(run.ID)
	if _, ok := e.srv.runs.Get(run.ID); ok {
		t.Fatal("the run is still held")
	}
	check("let go")
	// A log the ledger dropped past the run's bound answers 410; a step
	// still running has no log yet.
	if _, err := e.srv.ledger.Transition(context.Background(), run.ID, func(tx *ledger.Tx) error {
		if err := tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, RunID: run.ID, TaskID: "build", N: 1, ID: "big", K: 1, Phase: ledger.PhaseSetup, State: ledger.CheckFinished, StartedAt: tx.Now(), LogDropped: true, LogSHA256: "h"}); err != nil {
			return err
		}
		return tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, RunID: run.ID, TaskID: "build", N: 1, ID: "slow", K: 1, Phase: ledger.PhaseSetup, State: ledger.CheckRunning, StartedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.raw(base+"/tasks/build/attempts/1/steps/big/1/log", adminToken, nil); resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "log_dropped") {
		t.Fatalf("a dropped log: %d %s", resp.StatusCode, body)
	}
	if resp, body := e.raw(base+"/tasks/build/attempts/1/steps/slow/1/log", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a running step's log: %d %s", resp.StatusCode, body)
	}
}

// A record at the ledger's revision that says a tree is being ended or a
// prerequisite runs is written again at the next start as the ledger has
// it (Codex's review of #422, round 2): the journal can write it between
// a commit and the end of what that commit finished, the server can end
// before the next, and the recovery, finding nothing open, moves no
// revision. Nothing runs after the server's end: the run reads with
// nothing cleaning and its prerequisites as their receipts say, at the
// revision it had.
func TestARecordThatSaysSomethingRunsIsWrittenAgainAtTheNextStart(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), manifestRunCrew(e.root, filepath.Join(t.TempDir(), "down")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	deadline := time.Now().Add(10 * time.Second)
	for got, _ := e.srv.runs.Get(run.ID); got.Cleaning != 0; got, _ = e.srv.runs.Get(run.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("the run's trees still ending after 10 s: cleaning %d", got.Cleaning)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	ld, err := ledger.Open(filepath.Join(e.srv.store.Dir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := ld.Run(ctx, run.ID)
	ld.Close()
	if err != nil || !ok {
		t.Fatalf("the run's row: %v %v", ok, err)
	}
	for _, tc := range []struct {
		name string
		set  func(*crew.Run)
	}{
		{"a tree being ended", func(rec *crew.Run) { rec.Cleaning = 2 }},
		{"a prerequisite running", func(rec *crew.Run) {
			rec.Manifest.Prerequisites[0] = crew.PrerequisiteState{ID: "tools", K: 1, State: "running"}
		}},
	} {
		// The record as the journal can have written it, at the ledger's
		// revision.
		records, err := crew.NewRecords(e.srv.store)
		if err != nil {
			t.Fatal(err)
		}
		rec, ok, err := records.Get(run.ID)
		if err != nil || !ok || rec.Manifest == nil || len(rec.Manifest.Prerequisites) != 1 {
			t.Fatalf("%s: the record: %v %v %+v", tc.name, ok, err, rec.Manifest)
		}
		rec.Managed = &crew.Managed{Revision: row.Revision}
		tc.set(&rec)
		if !crew.UnderWay(rec) {
			t.Fatalf("%s: the record does not say so", tc.name)
		}
		if err := records.Save(rec); err != nil {
			t.Fatal(err)
		}
		records.Close()
		again := e.restart()
		resp, out := again.do("GET", "/api/runs/"+run.ID, adminToken, nil)
		got, _ := out["run"].(map[string]any)
		if resp.StatusCode != http.StatusOK || out["record"] != true || got == nil {
			t.Fatalf("%s: the run after the restart: %d %v", tc.name, resp.StatusCode, out)
		}
		managed, _ := got["managed"].(map[string]any)
		prereqs, _ := got["manifest"].(map[string]any)["prerequisites"].([]any)
		if got["cleaning"] != nil || managed == nil || managed["revision"] != float64(row.Revision) || len(prereqs) != 1 {
			t.Fatalf("%s: the run after the restart: cleaning %v, managed %v, prerequisites %v", tc.name, got["cleaning"], managed, prereqs)
		}
		if p := prereqs[0].(map[string]any); p["id"] != "tools" || p["state"] != "passed" || p["k"] != float64(1) || p["exit"] != float64(0) {
			t.Fatalf("%s: the prerequisite after the restart: %v", tc.name, p)
		}
		again.srv.Shutdown(ctx)
	}
}

// A task's read for a run the engine does not hold answers from the
// ledger: after a restart (a second server on the same data directory,
// the first shut down), a task with two attempts, the first uncounted at
// its setup step and the second its result, is answered whole: each
// attempt's state, commits, what it submitted and its receipts as the
// rows have them, no preview, each log read through the log route; a task
// the ledger has not is 404.
func TestATasksReadAfterARestartAnswersFromTheLedger(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	mark := filepath.Join(t.TempDir(), "down")
	if err := os.WriteFile(mark, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), manifestRunCrew(e.root, mark), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/runs/" + run.ID
	taskVia(t, e, run.ID, "build", "paused at attempt 1", func(task map[string]any) bool {
		a, _ := task["attempt"].(map[string]any)
		return a != nil && a["state"] == "failed" && a["stage"] == "setup deps"
	})
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	if resp, out := e.do("POST", base+"/unpause", adminToken, map[string]any{"commandId": "go-on"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("unpause: %d %v", resp.StatusCode, out)
	}
	a2 := runningAttempt(t, e, run.ID, "build", 2)
	sid := a2["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built at last"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	_, held := e.do("GET", base+"/tasks/build", adminToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	again := e.restart()
	resp, out := again.do("GET", base+"/tasks/build", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the task after a restart: %d %v", resp.StatusCode, out)
	}
	task := out["task"].(map[string]any)
	attempts := out["attempts"].([]any)
	if task["state"] != "succeeded" || task["attempts"] != float64(2) || len(attempts) != 2 {
		t.Fatalf("the task after a restart: %v\n%v", task, attempts)
	}
	heldAttempts := held["attempts"].([]any)
	for i, raw := range attempts {
		got, was := raw.(map[string]any), heldAttempts[i].(map[string]any)
		for _, k := range []string{"n", "state", "stage", "error", "start", "result", "worktree", "branch"} {
			if got[k] != was[k] {
				t.Fatalf("attempt %d's %s after a restart: %v, held %v", i+1, k, got[k], was[k])
			}
		}
		if got["preview"] != nil {
			t.Fatalf("attempt %d has a preview after a restart: %v", i+1, got)
		}
		steps, _ := got["steps"].([]any)
		if len(steps) != len(was["steps"].([]any)) || len(steps) == 0 {
			t.Fatalf("attempt %d's receipts after a restart: %v, held %v", i+1, steps, was["steps"])
		}
		first := steps[0].(map[string]any)
		resp, body := again.raw(base+"/tasks/build/attempts/"+strconv.Itoa(i+1)+"/steps/"+first["id"].(string)+"/"+strconv.Itoa(int(first["k"].(float64)))+"/log", adminToken, nil)
		if resp.StatusCode != http.StatusOK || resp.Header.Get(logHashHeader) != shaHex(body) || first["logSha256"] != shaHex(body) {
			t.Fatalf("attempt %d's first log after a restart: %d %q", i+1, resp.StatusCode, body)
		}
	}
	if a1 := attempts[0].(map[string]any); a1["stage"] != "setup deps" || a1["submitted"] != nil {
		t.Fatalf("attempt 1 after a restart: %v", a1)
	}
	if a2 := attempts[1].(map[string]any); a2["submitted"] == nil || a2["submitted"].(map[string]any)["summary"] != "built at last" {
		t.Fatalf("attempt 2 after a restart: %v", a2)
	}
	if resp, _ := again.do("GET", base+"/tasks/nope", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a task the ledger has not: %d", resp.StatusCode)
	}
	if resp, _ := again.do("GET", "/api/runs/nope-00000000/tasks/build", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a run the ledger has not: %d", resp.StatusCode)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	again.srv.Shutdown(ctx2)
}

// gitIn runs git in dir as the test's repository is made (gitRepo).
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitManifest commits conductor.manifest.json in dir and returns its
// blob id.
func commitManifest(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "conductor.manifest.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "the manifest")
	return gitIn(t, dir, "rev-parse", "HEAD:conductor.manifest.json")
}

// The routes say what the rows say once the engine has let the run go: a
// step whose program could not start, with its words; and the two
// manifests one attempt's trees ran (the worktree's file at its start, the
// checkout's at the result), each step naming its own.
func TestTheReceiptRoutesSayWhatTheRowsSay(t *testing.T) {
	t.Run("a program that cannot start", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		c := manifestRunCrew(e.root, "")
		c.Manifest = &crew.Manifest{Setup: []crew.Check{{ID: "deps", Argv: []string{filepath.Join(t.TempDir(), "no-such-program")}}}}
		run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
		release()
		if err != nil {
			t.Fatal(err)
		}
		taskVia(t, e, run.ID, "build", "failed at its setup", func(task map[string]any) bool {
			a, _ := task["attempt"].(map[string]any)
			return a != nil && a["state"] == "failed" && a["stage"] == "setup deps"
		})
		if resp, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("stop: %d %v", resp.StatusCode, out)
		}
		e.srv.runs.ForgetForTest(run.ID)
		steps := stepsOf(t, e, run.ID, "build", 1)
		if len(steps) != 1 || !strings.HasPrefix(steps[0]["error"].(string), "deps could not run: ") || !strings.Contains(steps[0]["error"].(string), "no-such-program") {
			t.Fatalf("the step: %v", steps)
		}
	})
	t.Run("two manifests on one attempt", func(t *testing.T) {
		e := readyEnv(t, nil)
		e.gitRepo(t)
		first := commitManifest(t, e.root, `{"schema": 1, "setup": [{"id": "deps", "argv": ["sh", "-c", "echo first"]}]}`)
		c := manifestRunCrew(e.root, "")
		c.Manifest = &crew.Manifest{File: true}
		run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
		release()
		if err != nil {
			t.Fatal(err)
		}
		a := runningAttempt(t, e, run.ID, "build", 1)
		second := commitManifest(t, a["worktree"].(string), `{"schema": 1, "setup": [{"id": "install", "argv": ["sh", "-c", "echo second"]}]}`)
		sid := a["sessionId"].(string)
		if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "a new manifest"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result: %d %v", resp.StatusCode, out)
		}
		taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
		if resp, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("stop: %d %v", resp.StatusCode, out)
		}
		e.srv.runs.ForgetForTest(run.ID)
		steps := stepsOf(t, e, run.ID, "build", 1)
		if len(steps) != 3 || steps[0]["id"] != "deps" || steps[0]["manifest"] != "file:"+first || steps[1]["id"] != "install" || steps[1]["manifest"] != "file:"+second {
			t.Fatalf("the steps: %v", steps)
		}
		for _, s := range steps[:2] {
			resp, body := e.raw("/api/runs/"+run.ID+"/tasks/build/attempts/1/steps/"+s["id"].(string)+"/1/log", adminToken, nil)
			if resp.StatusCode != http.StatusOK || resp.Header.Get(logHashHeader) != shaHex(body) || len(body) == 0 {
				t.Fatalf("%s's log: %d %q", s["id"], resp.StatusCode, body)
			}
		}
	})
}

// A setup step and a check that each write 2 MiB come back through the log
// route as the ledger keeps them (EN1 PR-2's test, read through the route
// as the independent review of #422 asked): at most MaxLog bytes, the
// dropped line and the output's last bytes inside them, a Content-Length
// that matches, and the hash header the SHA-256 of the body served, as the
// receipt says.
func TestTwoMiBOfOutputComeBackThroughTheLogRoute(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	big := func(letter, end string) []string {
		return []string{"sh", "-c", `head -c 2097152 /dev/zero | tr '\000' ` + letter + `; echo; echo ` + end}
	}
	c := manifestRunCrew(e.root, "")
	c.Manifest.Setup = []crew.Check{{ID: "deps", Argv: big("s", "SETUP-END")}}
	c.Tasks[0].Checks = []crew.Check{{ID: "unit", Argv: big("c", "CHECK-END")}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil) })
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	steps := stepsOf(t, e, run.ID, "build", 1)
	if len(steps) != 3 {
		t.Fatalf("the steps: %v", steps)
	}
	for _, step := range steps {
		end := "SETUP-END\n"
		if step["phase"] == "check" {
			end = "CHECK-END\n"
		}
		path := "/api/runs/" + run.ID + "/tasks/build/attempts/1/steps/" + step["id"].(string) + "/" + strconv.Itoa(int(step["k"].(float64))) + "/log"
		resp, body := e.raw(path, adminToken, nil)
		if resp.StatusCode != http.StatusOK || len(body) > ledger.MaxLog || !bytes.Contains(body, []byte(" bytes dropped …\n")) || !bytes.HasSuffix(body, []byte(end)) ||
			resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) || resp.Header.Get(logHashHeader) != shaHex(body) ||
			step["logSha256"] != shaHex(body) || step["logBytes"] != float64(len(body)) || step["exit"] != float64(0) {
			t.Fatalf("%s: %d, %d bytes (at most %d), Content-Length %s, hash %s (the body's %s, the step's %v), ends %q", path, resp.StatusCode, len(body), ledger.MaxLog,
				resp.Header.Get("Content-Length"), resp.Header.Get(logHashHeader), shaHex(body), step["logSha256"], body[max(0, len(body)-12):])
		}
	}
}

// A receipt that ended without a log answers 404, never an empty 200 with
// an empty hash (Codex's review of #422 and the independent one): a check
// the attempt closed under, and a step and a prerequisite still open when
// the server ended, which the recovery concludes interrupted at the next
// start. A log kept empty (a step that printed nothing) has its hash and
// answers 200, empty.
func TestAReceiptWithoutALogIsNotFound(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), manifestRunCrew(e.root, filepath.Join(t.TempDir(), "down")), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	if _, err := e.srv.ledger.Transition(context.Background(), run.ID, func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, row := range []ledger.CheckRow{
			{RunID: run.ID, TaskID: "build", N: 1, ID: "unit", K: 1, Phase: ledger.PhaseCheck, State: ledger.CheckInterrupted, Exit: -1, StartedAt: now, FinishedAt: now, Error: "the attempt closed under the check"},
			{RunID: run.ID, TaskID: "build", N: 1, ID: "slow", K: 1, Phase: ledger.PhaseSetup, State: ledger.CheckRunning, Exit: -1, StartedAt: now},
			{RunID: run.ID, TaskID: "build", N: 1, ID: "quiet", K: 1, Phase: ledger.PhaseSetup, State: ledger.CheckFinished, StartedAt: now, FinishedAt: now, LogSHA256: shaHex(nil)},
		} {
			row.Where = ledger.WhereNative
			if err := tx.PutCheck(row); err != nil {
				return err
			}
		}
		return tx.PutPrerequisite(ledger.PrerequisiteRow{Where: ledger.WhereNative, RunID: run.ID, ID: "late", K: 1, State: ledger.CheckRunning, Exit: -1, StartedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	again := e.restart()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		again.srv.Shutdown(ctx)
	})
	base := "/api/runs/" + run.ID
	for _, path := range []string{base + "/tasks/build/attempts/1/steps/unit/1/log", base + "/tasks/build/attempts/1/steps/slow/1/log", base + "/prerequisites/late/1/log"} {
		resp, body := again.raw(path, adminToken, nil)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "not_found") || resp.Header.Get(logHashHeader) != "" {
			t.Fatalf("%s: %d %s, hash %q", path, resp.StatusCode, body, resp.Header.Get(logHashHeader))
		}
	}
	for _, step := range stepsOf(t, again, run.ID, "build", 1) {
		if id := step["id"]; (id == "slow" || id == "unit") && step["state"] != ledger.CheckInterrupted {
			t.Fatalf("%s after the recovery: %v", id, step)
		}
	}
	resp, body := again.raw(base+"/tasks/build/attempts/1/steps/quiet/1/log", adminToken, nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 || resp.Header.Get(logHashHeader) != shaHex(nil) || resp.Header.Get("Content-Length") != "0" {
		t.Fatalf("a log kept empty: %d %q, hash %q", resp.StatusCode, body, resp.Header.Get(logHashHeader))
	}
}

// A continued run's task whose result was imported by reference reads the
// same from the ledger as while the engine held the run (Codex's review of
// #422): its summary, issues and time come from the attempt row in the
// run that made it, which the task row names (importedFrom).
func TestAnImportedResultReadsTheSameAfterTheRunIsLetGo(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built it", "issues": []string{"the docs lag"}}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	resp, out := e.do("POST", "/api/runs/"+run.ID+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("continue: %d %v", resp.StatusCode, out)
	}
	next := out["run"].(map[string]any)["id"].(string)
	taskVia(t, e, next, "build", "kept", func(task map[string]any) bool { return task["state"] == "succeeded" })
	read := func(when string) map[string]any {
		resp, out := e.do("GET", "/api/runs/"+next+"/tasks/build", adminToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: the task: %d %v", when, resp.StatusCode, out)
		}
		task := out["task"].(map[string]any)
		res, _ := task["result"].(map[string]any)
		issues, _ := res["issues"].([]any)
		if task["importedFrom"] != run.ID+"/build/1" || res["summary"] != "built it" || len(issues) != 1 || issues[0] != "the docs lag" || res["at"] == nil || res["attempt"] != float64(1) {
			t.Fatalf("%s: the imported result: %v", when, task)
		}
		return res
	}
	held := read("held")
	if resp, out := e.do("POST", "/api/runs/"+next+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	e.srv.runs.ForgetForTest(next)
	if _, ok := e.srv.runs.Get(next); ok {
		t.Fatal("the continuation is still held")
	}
	if got := read("let go"); got["at"] != held["at"] || got["commit"] != held["commit"] {
		t.Fatalf("the imported result let go: %v, held %v", got, held)
	}
}
