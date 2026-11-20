package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The context an attempt was given, as the server shows it
// (docs/plans/context-seams.md, CT-3, attempt_context.go).

// contextAttempt is a server whose provider gives every attempt two
// blocks, and a managed run whose build's first attempt runs: its run's
// id and the attempt.
func contextAttempt(t *testing.T) (*testEnv, string, map[string]any) {
	t.Helper()
	p := &fakeContext{name: "graph", answer: blocksOf(
		ext.ContextBlock{Title: "Callers of Build", Ref: "build.go", Body: "main calls Build.", Pack: "p-4f2c-1"},
		ext.ContextBlock{Title: "Recent changes", Body: "build.go changed twice."},
	)}
	e := contextEnv(t, p)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopEveryRun(e.srv) })
	return e, run.ID, runningAttempt(t, e, run.ID, "build", 1)
}

// contextOf is the context the task's read shows for its first attempt.
func contextOf(t *testing.T, e *testEnv, runID string) []map[string]any {
	t.Helper()
	task := e.expect(t, "GET", "/api/runs/"+runID+"/tasks/build", nil, http.StatusOK, "")
	var out []map[string]any
	for _, v := range task["attempts"].([]any)[0].(map[string]any)["context"].([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}

// The task's read lists the blocks an attempt was given at its start,
// never their text, and the body route gives each one's text with its
// digest; an id not shaped as one is refused, and a block the ledger has
// not is no block.
func TestAttemptContextShownAndRead(t *testing.T) {
	e, runID, _ := contextAttempt(t)
	rows := contextOf(t, e, runID)
	if len(rows) != 2 {
		t.Fatalf("the attempt's context: %v", rows)
	}
	want := []struct{ title, ref, pack, body string }{
		{"Callers of Build", "build.go", "p-4f2c-1", "main calls Build."},
		{"Recent changes", "", "", "build.go changed twice."},
	}
	for i, r := range rows {
		w := want[i]
		if r["seq"] != float64(i+1) || r["phase"] != "task" || r["source"] != "graph" || r["title"] != w.title || r["ref"] != nil && r["ref"] != w.ref ||
			r["bytes"] != float64(len(w.body)) || r["at"] == nil {
			t.Fatalf("row %d: %v", i, r)
		}
		if _, ok := r["body"]; ok {
			t.Fatalf("row %d carries its text: %v", i, r)
		}
		resp, body := e.raw(fmt.Sprintf("/api/runs/%s/tasks/build/attempts/1/context/%d", runID, i+1), adminToken, nil)
		if resp.StatusCode != http.StatusOK || string(body) != w.body || resp.Header.Get(contextSHAHeader) != r["sha256"] ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Fatalf("block %d: %d %q %v", i+1, resp.StatusCode, body, resp.Header)
		}
	}
	for path, status := range map[string]int{
		"/api/runs/" + runID + "/tasks/build/attempts/1/context/3":  http.StatusNotFound,
		"/api/runs/" + runID + "/tasks/build/attempts/2/context/1":  http.StatusNotFound,
		"/api/runs/" + runID + "/tasks/build/attempts/1/context/01": http.StatusBadRequest,
		"/api/runs/" + runID + "/tasks/build/attempts/0/context/1":  http.StatusBadRequest,
		"/api/runs/" + runID + "/tasks/Build!/attempts/1/context/1": http.StatusBadRequest,
	} {
		if resp, body := e.raw(path, adminToken, nil); resp.StatusCode != status {
			t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, body, status)
		}
	}
	if resp, _ := e.raw("/api/runs/"+runID+"/tasks/build/attempts/1/context/1", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

// A text whose digest is not its row's is never shown: changed in the
// data directory, the block reads as no block.
func TestBlockRouteChecksTheDigest(t *testing.T) {
	e, runID, _ := contextAttempt(t)
	contextOf(t, e, runID)
	path := filepath.Join(e.srv.ledger.Dir(), "context", runID, "build.1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(b), "main calls Build.", "main calls Make!.", 1)
	if changed == string(b) {
		t.Fatalf("the file: %s", b)
	}
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, body := e.raw("/api/runs/"+runID+"/tasks/build/attempts/1/context/1", adminToken, nil)
	if resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "Make!") {
		t.Fatalf("a changed text: %d %s", resp.StatusCode, body)
	}
	if resp, body := e.raw("/api/runs/"+runID+"/tasks/build/attempts/1/context/2", adminToken, nil); resp.StatusCode != http.StatusOK || string(body) != "build.go changed twice." {
		t.Fatalf("the unchanged block: %d %s", resp.StatusCode, body)
	}
}

// A candidate counts the blocks its attempt was given.
func TestCandidatesCountContext(t *testing.T) {
	e, runID, a := contextAttempt(t)
	sid := a["sessionId"].(string)
	commitFile(t, a["worktree"].(string), "built.txt", "yes\n")
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, runID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	out := e.expect(t, "GET", "/api/runs/"+runID+"/candidates?task=build", nil, http.StatusOK, "")
	c := out["candidates"].([]any)[0].(map[string]any)["candidates"].([]any)[0].(map[string]any)
	if c["context"] != float64(2) {
		t.Fatalf("the candidate: %v", c)
	}
}

// The folder of the context's text is no run's record: the runs list
// reads on with it there, and the data store lists no document in it.
func TestRunRecordsIgnoreTheContextFolder(t *testing.T) {
	e, runID, _ := contextAttempt(t)
	if _, err := os.Stat(filepath.Join(e.srv.ledger.Dir(), "context", runID, "build.1.json")); err != nil {
		t.Fatal(err)
	}
	list := e.expect(t, "GET", "/api/runs", nil, http.StatusOK, "")
	if !slices.ContainsFunc(list["runs"].([]any), func(r any) bool { return r.(map[string]any)["id"] == runID }) {
		t.Fatalf("the runs list: %v", list)
	}
	docs, err := e.srv.store.ListDocs("runs")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Name == "context" || !strings.HasSuffix(d.Name, ".json") {
			t.Fatalf("the runs' documents list %q", d.Name)
		}
	}
}

// A sync store is never handed the context's text: through the start's
// replay and a commit after it, no document under runs/context/.
func TestContextFileNeverHandedToSync(t *testing.T) {
	dir := syncDataDir(t)
	const runID = "team-00000001"
	ctxFile := filepath.Join(dir, "runs", "context", runID, "build.1.json")
	if err := os.MkdirAll(filepath.Dir(ctxFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctxFile, []byte(`{"v":1,"blocks":[{"seq":1,"phase":"task","source":"graph","title":"T","body":"the context's text"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, dir, f)
	e.srv.StartExtensions(t.Context())
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	if err := e.srv.records.Save(crew.Run{ID: "todo-app-0000beef", CrewID: "todo-app", Name: "Todo", StartedAt: time.Now().UTC(), State: crew.RunFinished}); err != nil {
		t.Fatal(err)
	}
	calls := f.until(t, "the record's commit", func(c []syncCall) bool {
		return slices.Contains(keysOf(c, ""), syncKey{syncData, "runs/todo-app-0000beef.json"})
	})
	for _, c := range calls {
		if strings.HasPrefix(c.d.Path, "runs/context") || strings.Contains(string(c.d.Data), "the context's text") {
			t.Fatalf("%s %s reached the sync store", c.d.Store, c.d.Path)
		}
	}
}

// A candidate given no context carries no count.
func TestACandidateGivenNoContextCountsNone(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, runID, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	commitFile(t, a["worktree"].(string), "built.txt", "yes\n")
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", tok, map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, runID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	out := e.expect(t, "GET", "/api/runs/"+runID+"/candidates?task=build", nil, http.StatusOK, "")
	c := out["candidates"].([]any)[0].(map[string]any)["candidates"].([]any)[0].(map[string]any)
	if _, ok := c["context"]; ok {
		t.Fatalf("the candidate: %v", c)
	}
	if _, ok := e.expect(t, "GET", "/api/runs/"+runID+"/tasks/build", nil, http.StatusOK, "")["attempts"].([]any)[0].(map[string]any)["context"]; ok {
		t.Fatal("an attempt given none lists context")
	}
}

// brakemanContext is the observe fixture with a block of context
// recorded for the run's build's first attempt, and the turn: the
// block's digest.
func brakemanContext(t *testing.T) (*obsFixture, string) {
	t.Helper()
	f := newObsFixture(t)
	sum := strings.Repeat("ab", 32)
	if _, err := f.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		return tx.AddAttemptContext([]ledger.AttemptContextRow{{RunID: f.run, TaskID: "build", N: 1, Seq: 1, Phase: ledger.PhaseTask, Source: "graph",
			Title: "Callers of Build", Ref: "build.go", Pack: "p-4f2c-1", SHA256: sum, Bytes: 17}})
	}); err != nil {
		t.Fatal(err)
	}
	f.turn(t, func(c map[string]any) { c["scope"] = map[string]any{"runs": []string{f.run}} })
	return f, sum
}

// A Brakeman turn reads an attempt's context as its shape alone: each
// block's phase, source, title, pack mark and digest; never its ref or
// its size (and its text is never in the task's read).
func TestBrakemanTaskReadCarriesContextAsFiltered(t *testing.T) {
	f, sum := brakemanContext(t)
	task := f.expectRead(t, "/api/runs/"+f.run+"/tasks/build", http.StatusOK, "")
	got, _ := json.Marshal(task["attempts"].([]any)[0].(map[string]any)["context"])
	// (The decoded map's keys come back in order.)
	want := `[{"pack":"p-4f2c-1","phase":"task","sha256":"` + sum + `","source":"graph","title":"Callers of Build"}]`
	if string(got) != want {
		t.Fatalf("the turn's context: %s, want %s", got, want)
	}
}

// The text's route refuses a turn as it refuses no token: it has no
// entry in the observe list.
func TestBrakemanCannotReadABlock(t *testing.T) {
	f, _ := brakemanContext(t)
	path := "/api/runs/" + f.run + "/tasks/build/attempts/1/context/1"
	resp, _ := f.readRaw(t, path)
	if none, _ := f.raw(path, "", nil); resp.StatusCode != none.StatusCode || resp.StatusCode == http.StatusOK {
		t.Fatalf("the text's route to the turn: %d (no token %d)", resp.StatusCode, none.StatusCode)
	}
	if admin, _ := f.raw(path, adminToken, nil); admin.StatusCode != http.StatusNotFound {
		// The row is there, its text not: the admin reaches the route.
		t.Fatalf("the admin: %d", admin.StatusCode)
	}
}

// A server whose context files could not be opened asks no provider for
// an attempt's context: no block reaches an agent unrecorded.
func TestNoContextWithoutContextFiles(t *testing.T) {
	p := &fakeContext{name: "graph", answer: blocksOf(ext.ContextBlock{Title: "T", Body: "b"})}
	e := contextEnv(t, p)
	if e.srv.contextFiles == nil {
		t.Fatal("the test server opened no context files")
	}
	q := ext.ContextQuery{Run: "team-00000001", Task: "build", Attempt: 1}
	if got := e.srv.contextFor(t.Context(), q); len(got) != 1 {
		t.Fatalf("with the files: %v", got)
	}
	cf := e.srv.contextFiles
	e.srv.contextFiles = nil
	t.Cleanup(func() { e.srv.contextFiles = cf })
	if got := e.srv.contextFor(t.Context(), q); len(got) != 0 {
		t.Fatalf("without the files: %v", got)
	}
	p.mu.Lock()
	asked := len(p.asked)
	p.mu.Unlock()
	if asked != 1 {
		t.Fatalf("the provider was asked %d times", asked)
	}
}
