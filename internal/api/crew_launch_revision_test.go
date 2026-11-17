package api

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// crewRevisionOf saves body by method at path and answers the crew's id and
// the revision the reply names (crewReply), as the launch sheet reads it.
func (e *testEnv) crewRevisionOf(t *testing.T, method, path string, body any, status int) (id, revision string) {
	t.Helper()
	resp, out := e.do(method, path, adminToken, body)
	c, _ := out["crew"].(map[string]any)
	revision, _ = out["revision"].(string)
	if resp.StatusCode != status || c == nil || revision == "" {
		t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, out)
	}
	return c["id"].(string), revision
}

// crewRunIDs is the ids of the runs GET /api/crews/{id}/runs lists for the
// crew: the engine's and the records'.
func (e *testEnv) crewRunIDs(t *testing.T, id string) []string {
	t.Helper()
	resp, out := e.do("GET", "/api/crews/"+id+"/runs", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runs of %s: %d %v", id, resp.StatusCode, out)
	}
	ids := []string{}
	for _, r := range out["runs"].([]any) {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	return ids
}

// runMemberNames is the names of a run's members as a launch's reply has them.
func runMemberNames(out map[string]any) []string {
	names := []string{}
	for _, m := range out["run"].(map[string]any)["members"].([]any) {
		names = append(names, m.(map[string]any)["name"].(string))
	}
	return names
}

// A launch that names the crew's revision starts the crew only at that
// revision (#957): after a save, the revision read before it is refused
// 409 crew_changed and nothing is started or recorded; the revision of the
// copy stored starts one run of that copy, as often as it is asked while
// no save comes between; a save between two launches of one revision
// refuses the second; a launch that names none starts the crew as stored;
// a revision past its bound or out of form is refused as invalid.
func TestCrewLaunchNamesTheRevisionItRead(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	body := map[string]any{"name": "Pinned", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "members": []any{catMember("core", "manual")}}
	id, first := e.crewRevisionOf(t, "POST", "/api/crews", body, http.StatusCreated)
	body["members"] = []any{catMember("renamed", "manual")}
	_, second := e.crewRevisionOf(t, "PUT", "/api/crews/"+id, body, http.StatusOK)
	if first == second {
		t.Fatalf("a save kept the revision %s", first)
	}
	launch := func(rev string) (*http.Response, map[string]any) {
		var b any
		if rev != "" {
			b = map[string]any{"revision": rev}
		}
		return e.do("POST", "/api/crews/"+id+"/launch", adminToken, b)
	}

	resp, out := launch(first)
	wantAPIError(t, "the revision read before the save", resp, out, http.StatusConflict, "crew_changed", "read it again")
	for _, bad := range []string{strings.Repeat("a", 65), strings.ToUpper(second), second[:63], "not-a-revision"} {
		resp, out := launch(bad)
		wantAPIError(t, "revision "+bad, resp, out, http.StatusBadRequest, "invalid_request", "64 lower-case hex digits")
	}
	if runs := e.crewRunIDs(t, id); len(runs) != 0 {
		t.Fatalf("a refused launch started or recorded runs %v", runs)
	}
	if _, out := e.do("GET", "/api/runs", adminToken, nil); len(out["runs"].([]any)) != 0 {
		t.Fatalf("a refused launch started a run: %v", out)
	}

	// The revision stored: one run of that copy, again while nothing is saved.
	var made []string
	for range 2 {
		resp, out := launch(second)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("the current revision: %d %v", resp.StatusCode, out)
		}
		if names := runMemberNames(out); len(names) != 1 || names[0] != "renamed" {
			t.Fatalf("the run's members %v, want the saved copy's", names)
		}
		made = append(made, out["run"].(map[string]any)["id"].(string))
	}
	// A save between two launches of one revision refuses the second.
	body["goal"] = "ship again"
	_, third := e.crewRevisionOf(t, "PUT", "/api/crews/"+id, body, http.StatusOK)
	resp, out = launch(second)
	wantAPIError(t, "a launch of the revision before a save", resp, out, http.StatusConflict, "crew_changed", "")
	// No revision: the crew as stored, as before.
	resp, out = launch("")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("no revision: %d %v", resp.StatusCode, out)
	}
	made = append(made, out["run"].(map[string]any)["id"].(string))
	resp, out = launch(third)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the revision after the save: %d %v", resp.StatusCode, out)
	}
	made = append(made, out["run"].(map[string]any)["id"].(string))
	if runs := e.crewRunIDs(t, id); len(runs) != len(made) {
		t.Fatalf("runs %v, want exactly %v", runs, made)
	}
}

// The launch judges the revision on its one read of the crew and starts
// that read: a save made while the launch is held after its read (in its
// agents' lookup, the seam) changes the stored crew, not the run, which
// is the copy whose revision the launch named. The comparison and the
// start are of one copy, so no save can come between them.
func TestALaunchStartsTheCopyWhoseRevisionItJudged(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.lookups.stub(func(program string) (string, error) {
		if program == "/bin/cat" {
			once.Do(func() { close(entered); <-release })
		}
		return program, nil
	})
	e.srv.lookups.stubWait(hangWait)
	body := map[string]any{"name": "Held", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "members": []any{catMember("core", "manual")}}
	id, read := e.crewRevisionOf(t, "POST", "/api/crews", body, http.StatusCreated)
	type answer struct {
		status int
		out    map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{"revision": read})
		done <- answer{resp.StatusCode, out}
	}()
	<-entered
	body["members"] = []any{catMember("saved-meanwhile", "manual")}
	e.crewRevisionOf(t, "PUT", "/api/crews/"+id, body, http.StatusOK)
	close(release)
	got := <-done
	if got.status != http.StatusCreated {
		t.Fatalf("launch: %d %v", got.status, got.out)
	}
	if names := runMemberNames(got.out); len(names) != 1 || names[0] != "core" {
		t.Fatalf("the run's members %v, want the copy the launch judged (core)", names)
	}
}
