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
// task_state refusal; a lane the applied selection did not select is
// not_selected, with the engine's words.
func TestALaneRefusalIsAnswered409Lane(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: cand-b is in another lane of this comparison: a message stays in its lane", crew.ErrLane), http.StatusConflict, "lane"},
		{crew.ErrNotListed, http.StatusConflict, "task_state"},
		{&crew.NotSelectedError{Words: "approach-a was not selected in dr-9: run a new trial to try it again"}, http.StatusConflict, "not_selected"},
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
// whole; its launch from the crews page is refused before a run exists: a
// comparison is launched from its ticket.
func TestACompareCrewSavesAndIsNotLaunchedFromTheCrewsPage(t *testing.T) {
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
		errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "compare: selector: no person of crew") {
		t.Fatalf("a selector naming no person of the crew: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/crews", adminToken, body(func(cmp *crew.Compare) { cmp.Selector = crew.GrantDecideLabel })); resp.StatusCode != http.StatusBadRequest ||
		errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "must be role:<responsibility> or person:<name>") {
		t.Fatalf("a selector of the decide grant: %d %v", resp.StatusCode, out)
	}
	saved := e.sendCrew("POST", "/api/crews", body(nil), http.StatusCreated)
	id := saved["id"].(string)
	read := e.crew(id)
	cmp, _ := read["compare"].(map[string]any)
	if cands, _ := cmp["candidates"].([]any); len(cands) != 2 || cands[0].(map[string]any)["approach"] != "one way" {
		t.Fatalf("the crew read back: %v", read["compare"])
	}
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusBadRequest || errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "an agent trial starts from a ticket's approved plan revision") {
		t.Fatalf("the launch: %d %v", resp.StatusCode, out)
	}
	// The crews page's launch names the copy it read: a stale one is
	// crew_changed first, the copy read refused for being a comparison.
	resp, out = e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{"revision": strings.Repeat("0", 64)})
	if resp.StatusCode != http.StatusConflict || errCodeOf(out) != "crew_changed" {
		t.Fatalf("the launch of a stale copy: %d %v", resp.StatusCode, out)
	}
	_, got := e.do("GET", "/api/crews/"+id, adminToken, nil)
	resp, out = e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{"revision": got["revision"]})
	if resp.StatusCode != http.StatusBadRequest || errCodeOf(out) != "invalid_crew" || !strings.Contains(fmt.Sprint(out), "an agent trial starts from a ticket's approved plan revision") {
		t.Fatalf("the launch of the copy read: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/runs", adminToken, nil); resp.StatusCode != http.StatusOK || len(out["runs"].([]any)) != 0 {
		t.Fatalf("runs after the refused launch: %v", out)
	}
}
