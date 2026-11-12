package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The message route answers the engine's lane refusal 409 lane, with its
// words; the engine writes nothing for it (internal/crew,
// TestMessagesStayInTheirLane). A command a comparison does not take is a
// task_state refusal.
func TestALaneRefusalIsAnswered409Lane(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: cand-b is in another lane of this comparison: a message stays in its lane", crew.ErrLane), http.StatusConflict, "lane"},
		{crew.ErrNotSelected, http.StatusConflict, "task_state"},
	} {
		w := httptest.NewRecorder()
		(&Server{}).taskError(w, "message", "s-1", tc.err)
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || body.Error.Code != tc.code || !strings.Contains(body.Error.Message, tc.err.Error()) {
			t.Fatalf("%v: %d %+v", tc.err, w.Code, body)
		}
	}
}

// A comparison's crew saves through the crew routes, held to the block's
// rules (400 invalid_crew in their words), and reads back with its block
// whole; its launch is refused before a run exists.
func TestACompareCrewSavesAndIsNotLaunchedYet(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	c, err := crew.CompareTemplate(crew.CompareSpec{Name: "Compare", Cwd: e.root, Goal: "g", Instructions: "Do it.",
		Criteria:   []crew.Criterion{{ID: "fast", Text: "It is fast."}},
		Checks:     []crew.Check{{ID: "ok", Argv: []string{"true"}}},
		Candidates: []crew.CandidateSpec{{AgentID: "ready-cat", Approach: "one way"}, {AgentID: "ready-cat"}}})
	if err != nil {
		t.Fatal(err)
	}
	body := func(edit func(*crew.Compare)) map[string]any {
		cmp := *c.Compare
		cmp.Criteria = append([]crew.Criterion(nil), cmp.Criteria...)
		if edit != nil {
			edit(&cmp)
		}
		return map[string]any{"name": c.Name, "goal": c.Goal, "cwd": c.Cwd, "where": c.Where, "isolation": c.Isolation,
			"members": c.Members, "tasks": c.Tasks, "compare": cmp}
	}
	if resp, out := e.do("POST", "/api/crews", adminToken, body(func(cmp *crew.Compare) { cmp.Selector = "person:ana" })); resp.StatusCode != http.StatusBadRequest ||
		errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "selector is not taken yet") {
		t.Fatalf("a selector: %d %v", resp.StatusCode, out)
	}
	saved := e.sendCrew("POST", "/api/crews", body(nil), http.StatusCreated)
	id := saved["id"].(string)
	read := e.crew(id)
	cmp, _ := read["compare"].(map[string]any)
	if cands, _ := cmp["candidates"].([]any); len(cands) != 2 || cands[0].(map[string]any)["approach"] != "one way" {
		t.Fatalf("the crew read back: %v", read["compare"])
	}
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusBadRequest || errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "comparisons arrive with their launch statement") {
		t.Fatalf("the launch: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/runs", adminToken, nil); resp.StatusCode != http.StatusOK || len(out["runs"].([]any)) != 0 {
		t.Fatalf("runs after the refused launch: %v", out)
	}
}
