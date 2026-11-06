package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// A managed run stopped is continued through the resume route (#334, row
// 8): a new run naming the old, its results kept by reference, the rest
// dispatched again; the old run names the continuation and is continued
// once.
func TestAManagedRunIsContinuedThroughTheResumeRoute(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	build := taskVia(t, e, run.ID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	commit := build["result"].(map[string]any)["commit"]
	runningAttempt(t, e, run.ID, "docs", 1)
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/resume", adminToken, nil); resp.StatusCode != http.StatusConflict || errCodeOf(out) != "run_running" {
		t.Fatalf("a continuation of a live run: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	resp, out := e.do("POST", "/api/runs/"+run.ID+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("continue: %d %v", resp.StatusCode, out)
	}
	next := out["run"].(map[string]any)
	nextID, _ := next["id"].(string)
	if nextID == "" || nextID == run.ID || next["resumedFrom"] != run.ID || next["managed"] == nil {
		t.Fatalf("the continuation: %v", next)
	}
	t.Cleanup(func() { e.do("POST", "/api/runs/"+nextID+"/stop", adminToken, nil) })
	// build kept by reference, docs dispatched again (attempt 2, the stopped one counted) at build's result.
	kept := taskVia(t, e, nextID, "build", "kept", func(task map[string]any) bool { return task["state"] == "succeeded" })
	if kept["importedFrom"] != run.ID+"/build/1" || kept["result"].(map[string]any)["commit"] != commit || kept["attempt"] != nil || kept["attempts"] != float64(1) {
		t.Fatalf("build in the continuation: %v", kept)
	}
	if d := runningAttempt(t, e, nextID, "docs", 2); d["start"] != commit {
		t.Fatalf("docs starts at %v, build's result is %v", d["start"], commit)
	}
	// The old run names the continuation, and is continued once.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := e.do("GET", "/api/runs/"+run.ID, adminToken, nil)
		if r := out["run"].(map[string]any); r["resumedBy"] == nextID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old run does not name the continuation: %v", out["run"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	if resp, out := e.do("POST", "/api/runs/"+run.ID+"/resume", adminToken, nil); resp.StatusCode != http.StatusConflict || errCodeOf(out) != "already_resumed" || out["error"].(map[string]any)["run"] != nextID {
		t.Fatalf("a second continuation: %d %v", resp.StatusCode, out)
	}
	_ = crew.RunInterrupted
}

// A continuation refused for a chain longer than the server reads is 409
// chain_too_long, with the engine's words (#636, review round 4).
func TestAChainTooLongIsItsOwnRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).runError(rec, "resume run", "team-00000001", fmt.Errorf("%w: team-00000001 continues more than the 10000 runs a server reads of a chain", crew.ErrChainTooLong))
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || body.Error.Code != "chain_too_long" || !strings.Contains(body.Error.Message, "10000 runs") {
		t.Fatalf("%d %+v", rec.Code, body)
	}
}
