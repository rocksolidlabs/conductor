package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A ticket's plan revisions over HTTP (docs/plans/dispatch-evidence.md,
// PR-6): a proposal snapshots the saved crew, the agents as the catalog
// resolves them, the base made a commit and the references at it; the
// reads say when the crew or an agent changed since; the approval and the
// withdrawal. Each test that runs git has a repository of its own under
// a HOME of its own (t.Setenv), so none runs in parallel.

// planTest is a server with a Dispatch store, a project whose folder is a
// repository with one commit, and a ticket of it, CND-1.
type planTest struct {
	*testEnv
	repo string // the project's folder
	head string // its first commit
}

// newPlanTest makes a planTest with the extra agents in its catalog.
func newPlanTest(t *testing.T, mutate func(*config.Config), agents ...catalog.Agent) *planTest {
	t.Helper()
	e := newTestEnvAgents(t, func(cfg *config.Config) {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		if mutate != nil {
			mutate(cfg)
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), agents)
	repo := filepath.Join(e.root, "repo")
	head := planRepo(t, repo)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": repo})
	e.newDispatchTicket(t, "CND", "Fix the login loop", map[string]any{"description": "It loops.", "acceptance": "It does not."})
	return &planTest{testEnv: e, repo: repo, head: head}
}

// planRepo makes dir a git repository with one commit, a README, and
// returns the commit; HOME is a temporary directory, so no configuration
// of the user running the tests applies. It skips the test without git.
func planRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	return commitFile(t, dir, "README", "hello\n")
}

// commitFile writes the file in dir, commits it and returns the commit.
func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "add "+name)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// teamCrew is a managed crew of one member on the agent, with one work
// task and one check.
func teamCrew(name, agent string) crew.Crew {
	return crew.Crew{Name: name, Goal: "fix it", Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: agent, Start: crew.Start{When: crew.StartTask}}},
		Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it.",
			Checks: []crew.Check{{ID: "test", Argv: []string{"true"}}}}}}
}

// saveCrew saves c as a new crew and returns it as saved.
func (p *planTest) saveCrew(t *testing.T, c crew.Crew) crew.Crew {
	t.Helper()
	saved, err := p.srv.crews.Create(c)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// propose sends a proposal for the ticket, an op id minted when the body
// names none, and fails t unless it answers the status and code.
func (p *planTest) propose(t *testing.T, key string, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	if _, ok := body["opId"]; !ok {
		body["opId"] = opID()
	}
	return p.expect(t, "POST", "/api/dispatch/tickets/"+key+"/plans", body, status, code)
}

// proposed proposes the crew for CND-1 with the fields given and returns
// the revision's id.
func (p *planTest) proposed(t *testing.T, crewID string, fields map[string]any) string {
	t.Helper()
	body := map[string]any{"crew": crewID}
	for k, v := range fields {
		body[k] = v
	}
	out := p.propose(t, "CND-1", body, http.StatusCreated, "")
	plan := entityOf(t, out, "plan")
	if plan["id"] != out["receipt"].(map[string]any)["id"] {
		t.Fatalf("the receipt names %v, the reply %v", out["receipt"], plan["id"])
	}
	return plan["id"].(string)
}

// planBodyOf is the body of the revision with the id as the store keeps
// it, its bytes and decoded, its crew decoded too.
func (p *planTest) planBodyOf(t *testing.T, id string) ([]byte, dispatch.PlanBody, crew.Crew) {
	t.Helper()
	v, err := p.srv.dispatch.Plan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var b dispatch.PlanBody
	if err := json.Unmarshal(v.Body, &b); err != nil {
		t.Fatal(err)
	}
	var c crew.Crew
	if err := json.Unmarshal(b.Crew, &c); err != nil {
		t.Fatal(err)
	}
	return v.Body, b, c
}

// plansOf is the ticket's revisions as the list route answers them, by id.
func (p *planTest) plansOf(t *testing.T, key string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, v := range listOf(t, p.expect(t, "GET", "/api/dispatch/tickets/"+key+"/plans", nil, http.StatusOK, ""), "plans") {
		out[v["id"].(string)] = v
	}
	return out
}

// A proposal takes the saved crew as it is: its members, tasks and
// checks, its working directory the project's folder and its yolo choice
// fixed; the crew edited afterwards leaves the body as it was, byte for
// byte, and the list says crewChanged, as it does once the crew is gone.
func TestProposeSnapshotsTheCrew(t *testing.T) {
	p := newPlanTest(t, nil)
	c := teamCrew("Team", "cat")
	c.Cwd = filepath.Join(p.root, "elsewhere")
	saved := p.saveCrew(t, c)
	comment := entityOf(t, p.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "Mind the cookie."}, http.StatusCreated, ""), "comment")
	id := p.proposed(t, saved.ID, map[string]any{"note": "First cut.", "constraints": "No new dependency.", "comments": []string{comment["id"].(string)},
		"decisions": []any{map[string]any{"id": "scope", "question": "Both browsers?", "kind": "clarification", "before": []string{"build"}}}})
	raw, body, snap := p.planBodyOf(t, id)
	sum := sha256.Sum256([]byte("Mind the cookie."))
	if len(body.Comments) != 1 || body.Comments[0].ID != comment["id"] || body.Comments[0].SHA256 != hex.EncodeToString(sum[:]) ||
		len(body.Decisions) != 1 || body.Decisions[0].Before[0] != "build" {
		t.Fatalf("what the revision hands over: %+v %+v", body.Comments, body.Decisions)
	}
	// An attachment that is not the ticket's: the store's not_found, its
	// receipt kept.
	if out := p.propose(t, "CND-1", map[string]any{"crew": saved.ID, "attachments": []string{"0123456789abcdef"}}, http.StatusNotFound, "not_found"); out["receipt"] == nil {
		t.Fatalf("an attachment missing: %v", out)
	}
	if snap.ID != saved.ID || snap.Name != "Team" || len(snap.Members) != 1 || len(snap.Tasks) != 1 || len(snap.Tasks[0].Checks) != 1 ||
		!snap.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatalf("the snapshot: %+v", snap)
	}
	if snap.Cwd != p.repo || snap.Yolo == nil || *snap.Yolo {
		t.Fatalf("the snapshot's directory %q and yolo %v: the project's folder and the server's default", snap.Cwd, snap.Yolo)
	}
	if body.Base != p.head || body.Note != "First cut." || body.Constraints != "No new dependency." {
		t.Fatalf("the body: %+v", body)
	}
	v := p.plansOf(t, "CND-1")[id]
	if v["n"] != 1.0 || v["state"] != dispatch.PlanProposed || v["crewChanged"] != false || v["agentsChanged"] != false || v["expectedChecks"] != 1.0 ||
		v["crewName"] != "Team" || v["base"] != p.head || v["body"] != nil {
		t.Fatalf("the list's revision: %v", v)
	}
	// Edited: the body as it was, and the list says so.
	c = saved
	c.Tasks[0].Instructions = "Build it differently."
	if _, err := p.srv.crews.Update(saved.ID, c); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := p.planBodyOf(t, id); !bytes.Equal(again, raw) {
		t.Fatal("editing the crew changed the revision's body")
	}
	if v := p.plansOf(t, "CND-1")[id]; v["crewChanged"] != true {
		t.Fatalf("after the edit: %v", v)
	}
	read := entityOf(t, p.expect(t, "GET", "/api/dispatch/plans/"+id, nil, http.StatusOK, ""), "plan")
	if read["crewChanged"] != true || read["digest"] != v["digest"] || read["body"] == nil {
		t.Fatalf("the revision's read: %v", read)
	}
	// Gone: changed still.
	if ok, err := p.srv.crews.Delete(saved.ID); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if v := p.plansOf(t, "CND-1")[id]; v["crewChanged"] != true {
		t.Fatalf("after the delete: %v", v)
	}
}

// The base is pinned when the revision is made: a branch's name is made
// its commit, no base is HEAD's, and HEAD moving after changes neither;
// a base that names no commit, or reads as an option, is refused.
func TestProposePinsTheBase(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	gitIn(t, p.repo, "checkout", "-q", "-b", "topic")
	topic := commitFile(t, p.repo, "topic.txt", "topic\n")
	gitIn(t, p.repo, "checkout", "-q", "-")
	onTopic := p.proposed(t, saved.ID, map[string]any{"base": "topic"})
	onHead := p.proposed(t, saved.ID, nil)
	byID := p.proposed(t, saved.ID, map[string]any{"base": p.head})
	moved := commitFile(t, p.repo, "later.txt", "later\n")
	for id, want := range map[string]string{onTopic: topic, onHead: p.head, byID: p.head} {
		if _, body, _ := p.planBodyOf(t, id); body.Base != want {
			t.Fatalf("revision %s's base is %s, not %s", id, body.Base, want)
		}
		if v := p.plansOf(t, "CND-1")[id]; v["base"] != want {
			t.Fatalf("the list's base: %v", v)
		}
	}
	if latest := p.proposed(t, saved.ID, nil); func() string { _, b, _ := p.planBodyOf(t, latest); return b.Base }() != moved {
		t.Fatal("a new revision did not take HEAD as it is now")
	}
	for _, base := range []string{"nope", "--all", "HEAD~9", "main branch", strings.Repeat("a", maxPlanBase+1)} {
		out := p.propose(t, "CND-1", map[string]any{"crew": saved.ID, "base": base}, http.StatusConflict, dispatch.RefusalInvalid)
		if out["receipt"] != nil {
			t.Fatalf("a refusal of the API's kept a receipt: %v", out)
		}
	}
	if got := len(p.plansOf(t, "CND-1")); got != 4 {
		t.Fatalf("%d revisions, not the 4 taken", got)
	}
}

// Each reference is resolved at the base: a file and a directory there
// are taken with their objects and kinds, the path cleaned and relative
// to the crew's working directory (the crew's prefix in its repository
// applied); a file only in the working tree, one added after the base, a
// path that leaves the directory and an absolute one are refused.
func TestProposeChecksReferencesAtTheBase(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	base := commitFile(t, p.repo, "src/a.go", "package a\n\nfunc A() {}\n")
	if err := os.WriteFile(filepath.Join(p.repo, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := p.proposed(t, saved.ID, map[string]any{"references": []any{
		map[string]any{"path": "src/a.go", "start": 3, "end": 3, "note": "the function"},
		map[string]any{"path": "src"},
		map[string]any{"path": "./src/../README"},
		map[string]any{"path": "."},
	}})
	commitFile(t, p.repo, "src/b.go", "package a\n")
	_, body, _ := p.planBodyOf(t, id)
	want := []dispatch.PlanReference{
		{Path: "src/a.go", Start: 3, End: 3, Note: "the function", Object: gitIn(t, p.repo, "rev-parse", base+":src/a.go"), Kind: dispatch.ObjectBlob},
		{Path: "src", Object: gitIn(t, p.repo, "rev-parse", base+":src"), Kind: dispatch.ObjectTree},
		{Path: "README", Object: gitIn(t, p.repo, "rev-parse", base+":README"), Kind: dispatch.ObjectBlob},
		{Path: ".", Object: gitIn(t, p.repo, "rev-parse", base+"^{tree}"), Kind: dispatch.ObjectTree},
	}
	if body.Base != base || !slices.Equal(body.References, want) {
		t.Fatalf("the references: %+v, want %+v", body.References, want)
	}
	for path, words := range map[string]string{
		"notes.txt":   "is not at the base",
		"src/b.go":    "is not at the base",
		"..":          "leaves the crew's working directory",
		"src/../../x": "leaves the crew's working directory",
		"/etc/passwd": "is absolute",
		`src\a.go`:    "is not a slash-separated path",
	} {
		out := p.propose(t, "CND-1", map[string]any{"crew": saved.ID, "base": base, "references": []any{map[string]any{"path": path}}}, http.StatusConflict, dispatch.RefusalInvalid)
		if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, words) {
			t.Fatalf("the reference %q: %q, want %q", path, msg, words)
		}
	}
	// A project whose folder is below the repository's top: its
	// references are relative to it, resolved with its prefix.
	p.newDispatchProject(t, "SUB", map[string]any{"cwd": filepath.Join(p.repo, "src")})
	p.newDispatchTicket(t, "SUB", "Below the top", nil)
	out := p.propose(t, "SUB-1", map[string]any{"crew": saved.ID, "base": base, "references": []any{map[string]any{"path": "a.go"}}}, http.StatusCreated, "")
	_, body, snap := p.planBodyOf(t, entityOf(t, out, "plan")["id"].(string))
	if snap.Cwd != filepath.Join(p.repo, "src") || len(body.References) != 1 || body.References[0].Path != "a.go" || body.References[0].Object != want[0].Object {
		t.Fatalf("below the top: %s %+v", snap.Cwd, body.References)
	}
	// More references than a revision holds: refused before any is read.
	many := make([]any, dispatch.MaxPlanReferences+1)
	for i := range many {
		many[i] = map[string]any{"path": "README"}
	}
	p.propose(t, "CND-1", map[string]any{"crew": saved.ID, "references": many}, http.StatusConflict, dispatch.RefusalBound)
}

// The yolo choice is fixed in the body: the crew's when it makes one,
// else the server's default when the revision is made, and the agents'
// selections follow it (a yolo recipe's arguments with it on, none off).
func TestProposeFixesTheYoloChoice(t *testing.T) {
	bold := catalog.Agent{ID: "bold", Name: "bold", Command: []string{"/bin/cat"}, Yolo: &catalog.Yolo{Args: []string{"--yes"}}}
	for _, def := range []bool{false, true} {
		p := newPlanTest(t, func(cfg *config.Config) { cfg.Yolo = def }, bold)
		on, off := true, false
		for _, tc := range []struct {
			crew *bool
			want bool
		}{{nil, def}, {&on, true}, {&off, false}} {
			c := teamCrew("Team", "bold")
			c.Yolo = tc.crew
			id := p.proposed(t, p.saveCrew(t, c).ID, nil)
			_, body, snap := p.planBodyOf(t, id)
			if snap.Yolo == nil || *snap.Yolo != tc.want {
				t.Fatalf("server %v, crew %v: the body's yolo is %v, not %v", def, tc.crew, snap.Yolo, tc.want)
			}
			if got := body.Agents[0].YoloArgs; tc.want != (len(got) == 1 && got[0] == "--yes") || (!tc.want && len(got) != 0) {
				t.Fatalf("server %v, crew %v: the yolo arguments %q", def, tc.crew, got)
			}
		}
	}
}

// What a revision cannot be made of is refused by the API, nothing kept
// and no receipt: a crew that is not there (404), and with the words
// (409 invalid) a crew without tasks, one that runs on a host, one whose
// agent the catalog lacks, and a working directory that is no
// repository; a request with no crew is 400.
func TestProposeRefusals(t *testing.T) {
	p := newPlanTest(t, nil)
	refuse := func(body map[string]any, status int, code, words string) {
		t.Helper()
		out := p.propose(t, "CND-1", body, status, code)
		if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, words) || out["receipt"] != nil {
			t.Fatalf("%v: %v, want %q and no receipt", body, out, words)
		}
	}
	refuse(map[string]any{"crew": "nobody"}, http.StatusNotFound, "not_found", "no crew nobody")
	refuse(map[string]any{}, http.StatusBadRequest, "invalid_request", "crew is the id of a saved crew")
	plain := p.saveCrew(t, crew.Crew{Name: "Plain", Where: crew.WhereServer, Isolation: crew.IsolationNone,
		Members: []crew.Member{{Name: "lead", AgentID: "cat", Start: crew.Start{When: crew.StartManual}}}})
	refuse(map[string]any{"crew": plain.ID}, http.StatusConflict, dispatch.RefusalInvalid, "has no tasks")
	host := teamCrew("Hosted", "cat")
	host.Where = crew.WhereHost
	refuse(map[string]any{"crew": p.saveCrew(t, host).ID}, http.StatusConflict, dispatch.RefusalInvalid, "cannot be launched")
	p.save(agentBody("passing"))
	gone := p.saveCrew(t, teamCrew("Gone", "passing"))
	if status := p.del("passing"); status != http.StatusOK && status != http.StatusNoContent {
		t.Fatalf("delete the agent: %d", status)
	}
	refuse(map[string]any{"crew": gone.ID}, http.StatusConflict, dispatch.RefusalInvalid, "unknown agent")
	// A project whose folder is no repository.
	plainDir := filepath.Join(p.root, "plain")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p.newDispatchProject(t, "NOR", map[string]any{"cwd": plainDir})
	p.newDispatchTicket(t, "NOR", "Nowhere", nil)
	team := p.saveCrew(t, teamCrew("Team", "cat"))
	out := p.propose(t, "NOR-1", map[string]any{"crew": team.ID}, http.StatusConflict, dispatch.RefusalInvalid)
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "not in a git repository") {
		t.Fatalf("no repository: %q", msg)
	}
	// A folder outside the server's roots.
	p.newDispatchProject(t, "OUT", map[string]any{"cwd": t.TempDir()})
	p.newDispatchTicket(t, "OUT", "Outside", nil)
	out = p.propose(t, "OUT-1", map[string]any{"crew": team.ID}, http.StatusConflict, dispatch.RefusalInvalid)
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "outside the allowed roots") {
		t.Fatalf("outside the roots: %q", msg)
	}
	// The store's refusals keep their receipts: a ticket missing.
	out = p.propose(t, "CND-9", map[string]any{"crew": team.ID}, http.StatusNotFound, "not_found")
	if out["receipt"] == nil {
		t.Fatalf("the store's refusal kept no receipt: %v", out)
	}
	if got := len(p.plansOf(t, "CND-1")); got != 0 {
		t.Fatalf("%d revisions after refusals", got)
	}
}

// A proposal sent again under its op id is answered from its receipt
// before anything is read: with the crew deleted and HEAD moved between,
// the same request answers the same revision, a repeat, and nothing more
// is made; the id with another request is op_conflict.
func TestProposeResentFromItsReceipt(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	body := map[string]any{"opId": "propose-once", "crew": saved.ID, "note": "the one"}
	first := p.propose(t, "CND-1", body, http.StatusCreated, "")
	id := entityOf(t, first, "plan")["id"]
	if ok, err := p.srv.crews.Delete(saved.ID); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	commitFile(t, p.repo, "later.txt", "later\n")
	again := p.propose(t, "CND-1", map[string]any{"opId": "propose-once", "crew": saved.ID, "note": "the one"}, http.StatusOK, "")
	rc := again["receipt"].(map[string]any)
	if rc["repeat"] != true || rc["id"] != id || entityOf(t, again, "plan")["id"] != id {
		t.Fatalf("the resend: %v", again)
	}
	other := p.propose(t, "CND-1", map[string]any{"opId": "propose-once", "crew": saved.ID, "note": "another"}, http.StatusConflict, dispatch.RefusalOpConflict)
	if other["receipt"].(map[string]any)["refusal"] != dispatch.RefusalOpConflict {
		t.Fatalf("another request: %v", other)
	}
	if got := len(p.plansOf(t, "CND-1")); got != 1 {
		t.Fatalf("%d revisions, not 1", got)
	}
}

// Each agent the crew's members name is recorded as the catalog resolves
// it: its command, its session recipe's start arguments, its yolo
// recipe's arguments with yolo on and none with it off, and its
// environment's names, the yolo recipe's overlaid, with the SHA-256 of
// names and values; no value is in the body. An edit of the agent in the
// catalog after shows agentsChanged, and the approval is refused
// stale_plan naming the agent and what changed.
func TestProposeSnapshotsTheAgents(t *testing.T) {
	p := newPlanTest(t, nil)
	p.save(map[string]any{"id": "bold", "name": "bold", "command": []string{"/bin/sh", "-c", "exec cat", "sh", "--model", "sonnet"},
		"env":     map[string]string{"AGENT_VAR": "s3cret-agent-value", "YOLO_MODE": "careful-value"},
		"session": map[string]any{"startArgs": []string{"--session-id", "{id}"}, "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]{36}$"},
		"yolo":    map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO_MODE": "yolo-auto-value", "YOLO_ONLY": "only-value"}}})
	on, off := true, false
	two := func(name string, yolo *bool) crew.Crew {
		c := teamCrew(name, "bold")
		c.Yolo = yolo
		c.Members = append(c.Members, crew.Member{Name: "helper", AgentID: "cat", Start: crew.Start{When: crew.StartTask}})
		c.Tasks = append(c.Tasks, crew.Task{ID: "docs", Member: "helper", Kind: crew.TaskWork, Instructions: "Write it."})
		return c
	}
	envSHA := func(pairs string) string {
		sum := sha256.Sum256([]byte(pairs))
		return hex.EncodeToString(sum[:])
	}
	withYolo := p.proposed(t, p.saveCrew(t, two("Bold", &on)).ID, nil)
	withoutYolo := p.proposed(t, p.saveCrew(t, two("Careful", &off)).ID, nil)
	for _, tc := range []struct {
		id       string
		yoloArgs []string
		envNames []string
		pairs    string
	}{
		{withYolo, []string{"--yes"}, []string{"AGENT_VAR", "YOLO_MODE", "YOLO_ONLY"},
			`[["AGENT_VAR","s3cret-agent-value"],["YOLO_MODE","yolo-auto-value"],["YOLO_ONLY","only-value"]]`},
		{withoutYolo, []string{}, []string{"AGENT_VAR", "YOLO_MODE"}, `[["AGENT_VAR","s3cret-agent-value"],["YOLO_MODE","careful-value"]]`},
	} {
		raw, body, _ := p.planBodyOf(t, tc.id)
		if len(body.Agents) != 2 {
			t.Fatalf("the agents: %+v", body.Agents)
		}
		a, cat := body.Agents[0], body.Agents[1]
		if a.ID != "bold" || !slices.Equal(a.Command, []string{"/bin/sh", "-c", "exec cat", "sh", "--model", "sonnet"}) ||
			!slices.Equal(a.StartArgs, []string{"--session-id", "{id}"}) || !slices.Equal(a.YoloArgs, tc.yoloArgs) ||
			!slices.Equal(a.EnvNames, tc.envNames) || a.EnvSHA256 != envSHA(tc.pairs) || len(a.SHA256) != 64 {
			t.Fatalf("bold: %+v", a)
		}
		if cat.ID != "cat" || !slices.Equal(cat.Command, []string{"/bin/cat"}) || len(cat.StartArgs)+len(cat.YoloArgs)+len(cat.EnvNames) != 0 || cat.EnvSHA256 != "" {
			t.Fatalf("cat: %+v", cat)
		}
		for _, value := range []string{"s3cret-agent-value", "careful-value", "yolo-auto-value", "only-value"} {
			if bytes.Contains(raw, []byte(value)) {
				t.Fatalf("the body holds the environment's value %q", value)
			}
		}
	}
	// The selection is the API's one function's: what agentSelection makes
	// of the entry now.
	entry, _ := p.srv.Catalog().Get("bold")
	if _, body, _ := p.planBodyOf(t, withYolo); body.Agents[0].SHA256 != agentSelection(entry, true).SHA256 {
		t.Fatal("the recorded selection is not agentSelection's")
	}
	if v := p.plansOf(t, "CND-1")[withYolo]; v["agentsChanged"] != false {
		t.Fatalf("before the edit: %v", v)
	}
	// Each part edited in the catalog, one at a time: the list says so,
	// and the approval is refused naming it, nothing kept.
	for _, tc := range []struct {
		edit  func(map[string]any)
		words string
	}{
		{func(b map[string]any) { b["command"] = []string{"/bin/sh", "-c", "exec cat", "sh", "--model", "opus"} }, "the agent bold's command changed"},
		{func(b map[string]any) {
			b["session"] = map[string]any{"startArgs": []string{"--session", "{id}"}, "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]{36}$"}
		}, "the agent bold's session recipe's start arguments changed"},
		{func(b map[string]any) {
			b["yolo"] = map[string]any{"args": []string{"--yes", "--model", "opus"}, "env": map[string]string{"YOLO_MODE": "yolo-auto-value", "YOLO_ONLY": "only-value"}}
		}, "the agent bold's yolo recipe's arguments changed"},
		{func(b map[string]any) {
			b["yolo"] = map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO_MODE": "another", "YOLO_ONLY": "only-value"}}
		}, "the agent bold's environment changed"},
	} {
		body := map[string]any{"id": "bold", "name": "bold", "command": []string{"/bin/sh", "-c", "exec cat", "sh", "--model", "sonnet"},
			"env":     map[string]string{"AGENT_VAR": "s3cret-agent-value", "YOLO_MODE": "careful-value"},
			"session": map[string]any{"startArgs": []string{"--session-id", "{id}"}, "resumeArgs": []string{"--resume", "{id}"}, "idPattern": "^[0-9a-f-]{36}$"},
			"yolo":    map[string]any{"args": []string{"--yes"}, "env": map[string]string{"YOLO_MODE": "yolo-auto-value", "YOLO_ONLY": "only-value"}}}
		tc.edit(body)
		p.save(body)
		if v := p.plansOf(t, "CND-1")[withYolo]; v["agentsChanged"] != true {
			t.Fatalf("%s: the list: %v", tc.words, v)
		}
		out := p.expect(t, "POST", "/api/dispatch/plans/"+withYolo+"/approve", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalStalePlan)
		if msg := out["error"].(map[string]any)["message"].(string); msg != tc.words+" since revision 1 was made: revise it" {
			t.Fatalf("the approval's words: %q", msg)
		}
	}
	// A yolo recipe edited is no change for a revision with yolo off.
	if v := p.plansOf(t, "CND-1")[withoutYolo]; v["agentsChanged"] != false {
		t.Fatalf("a yolo recipe edited, yolo off: %v", v)
	}
	if v := p.plansOf(t, "CND-1")[withYolo]; v["state"] != dispatch.PlanProposed {
		t.Fatalf("a refused approval approved it: %v", v)
	}
}

// A file-form project manifest is held at the base: its blob and the
// manifest as the engine reads it, its two verify steps counted among the
// expected checks; the file changed at HEAD after leaves the body as it
// was; a file at the base that is not a manifest is refused.
func TestProposeSnapshotsAFileManifest(t *testing.T) {
	p := newPlanTest(t, nil)
	c := teamCrew("Manifested", "cat")
	c.Manifest = &crew.Manifest{File: true}
	saved := p.saveCrew(t, c)
	blob := commitManifest(t, p.repo, `{"schema": 1, "verify": [{"id": "lint", "argv": ["true"]}, {"id": "vet", "argv": ["true"]}]}`)
	id := p.proposed(t, saved.ID, nil)
	raw, body, _ := p.planBodyOf(t, id)
	if body.ManifestFile == nil || body.ManifestFile.Path != "conductor.manifest.json" || body.ManifestFile.Blob != blob {
		t.Fatalf("the manifest file: %+v", body.ManifestFile)
	}
	var m crew.Manifest
	if err := json.Unmarshal(body.ManifestFile.Manifest, &m); err != nil || len(m.Verify) != 2 || m.Verify[0].ID != "lint" || m.Verify[1].ID != "vet" {
		t.Fatalf("the manifest: %s %v", body.ManifestFile.Manifest, err)
	}
	if v := p.plansOf(t, "CND-1")[id]; v["expectedChecks"] != 3.0 {
		t.Fatalf("the expected checks: %v", v)
	}
	commitManifest(t, p.repo, `{"schema": 1, "verify": [{"id": "lint", "argv": ["false"]}]}`)
	if again, _, _ := p.planBodyOf(t, id); !bytes.Equal(again, raw) {
		t.Fatal("the file changed at HEAD changed the body")
	}
	commitManifest(t, p.repo, `{"schema": 1, "verify": "not steps"}`)
	out := p.propose(t, "CND-1", map[string]any{"crew": saved.ID}, http.StatusConflict, dispatch.RefusalInvalid)
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "the crew's project manifest") || !strings.Contains(msg, "conductor.manifest.json") {
		t.Fatalf("not a manifest: %q", msg)
	}
}

// boundAgents are n agents whose commands are at the catalog's bounds:
// 32 elements, the 31 after the program each 4,096 backslashes, which
// JSON doubles.
func boundAgents(n int) []catalog.Agent {
	arg := strings.Repeat(`\`, 4096)
	out := make([]catalog.Agent, n)
	for i := range out {
		cmd := []string{"/bin/echo"}
		for range 31 {
			cmd = append(cmd, arg)
		}
		id := "wide" + string(rune('a'+i))
		out[i] = catalog.Agent{ID: id, Name: id, Command: cmd}
	}
	return out
}

// wideCrew is a managed crew with one member on each agent and tasks
// tasks, each task's instructions fill characters long, at fixed times,
// so that two saves of it encode alike.
func wideCrew(id string, agents []catalog.Agent, tasks int, fill []int) crew.Crew {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	c := crew.Crew{ID: id, Name: id, Goal: "fix it", Where: crew.WhereServer, Isolation: crew.IsolationWorktree, CreatedAt: at, UpdatedAt: at}
	for i, a := range agents {
		c.Members = append(c.Members, crew.Member{Name: "m" + string(rune('a'+i)), AgentID: a.ID, Start: crew.Start{When: crew.StartTask}})
	}
	for i := range tasks {
		c.Tasks = append(c.Tasks, crew.Task{ID: "t" + string(rune('a'+i)), Member: c.Members[i%len(c.Members)].Name, Kind: crew.TaskWork,
			Instructions: "x" + strings.Repeat("a", fill[i])})
	}
	return c
}

// A body past 2 MiB as encoded is refused bound, its words naming its
// largest part (twelve agents' commands at the catalog's bounds), and no
// revision is kept; one at exactly 2 MiB is taken, and read back whole
// with every text of its summary at its escaped worst under MaxPlanReply.
func TestProposeRefusesAnOversizedBody(t *testing.T) {
	agents := boundAgents(12)
	p := newPlanTest(t, nil, agents...)
	put := func(c crew.Crew) {
		t.Helper()
		if err := p.srv.crews.Put(c); err != nil {
			t.Fatal(err)
		}
	}
	put(wideCrew("wide", agents, 12, make([]int, 12)))
	out := p.propose(t, "CND-1", map[string]any{"crew": "wide"}, http.StatusConflict, dispatch.RefusalBound)
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.HasPrefix(msg, "the agents' commands are 2.9 MiB of the revision's ") || out["receipt"].(map[string]any)["refusal"] != dispatch.RefusalBound {
		t.Fatalf("the refusal: %v", out)
	}
	if got := len(p.plansOf(t, "CND-1")); got != 0 {
		t.Fatalf("%d revisions kept", got)
	}
	// Eight such agents and ten tasks: under the bound; the tasks'
	// instructions then grown by what is left, to exactly 2 MiB.
	fill := make([]int, 10)
	put(wideCrew("exact", agents[:8], 10, fill))
	first := entityOf(t, p.propose(t, "CND-1", map[string]any{"crew": "exact"}, http.StatusCreated, ""), "plan")
	gap := dispatch.MaxPlanBody - int(first["bytes"].(float64))
	if gap <= 0 || gap > 10*7999 {
		t.Fatalf("a first body of %v bytes", first["bytes"])
	}
	for i := range fill {
		fill[i] = min(gap, 7999)
		gap -= fill[i]
	}
	put(wideCrew("exact", agents[:8], 10, fill))
	exact := entityOf(t, p.propose(t, "CND-1", map[string]any{"crew": "exact"}, http.StatusCreated, ""), "plan")
	if exact["bytes"] != float64(dispatch.MaxPlanBody) {
		t.Fatalf("a body of %v bytes, not %d", exact["bytes"], dispatch.MaxPlanBody)
	}
	// Its read, the summary's texts at their escaped worst: approved with
	// a note and by a name at their bounds, then withdrawn by a name at
	// its bound.
	id := exact["id"].(string)
	f := worstFiller
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", map[string]any{"opId": opID(), "byName": f.runes(dispatch.MaxByName, 1),
		"note": f.runes(dispatch.MaxApprovedNote, 2)}, http.StatusOK, "")
	p.expect(t, "POST", "/api/dispatch/plans/"+id+"/withdraw", map[string]any{"opId": opID(), "byName": f.runes(dispatch.MaxByName, 3)}, http.StatusOK, "")
	status, raw, read := p.call(t, "GET", "/api/dispatch/plans/"+id, nil)
	plan := entityOf(t, read, "plan")
	if status != http.StatusOK || plan["approvedNote"] != f.runes(dispatch.MaxApprovedNote, 2) || plan["withdrawnBy"] != f.runes(dispatch.MaxByName, 3) ||
		plan["bytes"] != float64(dispatch.MaxPlanBody) {
		t.Fatalf("the read: %d %.300s", status, raw)
	}
	stored, _, _ := p.planBodyOf(t, id)
	if len(stored) != dispatch.MaxPlanBody || !bytes.Contains(raw, append(append([]byte(`"body":`), stored...), '}')) {
		t.Fatal("the read does not carry the body as it is kept")
	}
	t.Logf("the largest revision's read: %d bytes of %d", len(raw), MaxPlanReply)
	if len(raw) >= MaxPlanReply {
		t.Fatalf("the largest revision's read is %d bytes, not under %d", len(raw), MaxPlanReply)
	}
}

// A ticket's revisions at their bound, every text of every summary at its
// escaped worst (the crew's name and id, the approval's note, the
// approver, the withdrawer and the author, a SHA-256 base), the newest
// approved and stale with its longest words, are listed whole under
// MaxPlansReply, newest first, with no body.
func TestThePlansListAtItsWorstIsUnderItsBound(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	done := doneOf(t)
	ds, f, ctx := e.srv.dispatch, worstFiller, context.Background()
	by := func(i int) dispatch.Op { return adminOp(f.runes(dispatch.MaxByName, i)) }
	done(ds.CreateProject(ctx, by(0), dispatch.NewProject{Key: "BIG", Name: "Big"}))
	done(ds.CreateTicket(ctx, by(1), dispatch.NewTicket{Project: "BIG", Title: "Big", Acceptance: "it holds"}))
	done(ds.AddComment(ctx, by(2), "BIG-1", "handed over"))
	tk, err := ds.Ticket(ctx, "BIG-1")
	if err != nil {
		t.Fatal(err)
	}
	crewID := "c" + strings.Repeat("0", 63)
	base := strings.Repeat("ab", 32)
	for i := range dispatch.MaxPlans {
		crewJSON, _ := json.Marshal(map[string]any{"id": crewID, "name": f.runes(dispatch.MaxCrewName, i), "updatedAt": time.Now().UTC(),
			"tasks": []any{map[string]any{"id": "build"}}})
		body := dispatch.PlanBody{Base: base, Crew: crewJSON, Comments: []dispatch.PlanComment{{ID: tk.Comments[0].ID}}}
		rc := done(ds.ProposePlan(ctx, by(i), "BIG-1", dispatch.PlanRequest{Crew: crewID, Note: f.text(8, i)}, body))
		done(ds.ApprovePlan(ctx, by(i+1), rc.ID, 0, f.runes(dispatch.MaxApprovedNote, i)))
		if i < dispatch.MaxPlans-1 {
			done(ds.WithdrawPlan(ctx, by(i+2), rc.ID))
		}
	}
	// The newest approval stale for both reasons: its comment edited, and
	// the title, the description and the criteria changed.
	done(ds.EditComment(ctx, by(3), "BIG-1", tk.Comments[0].ID, "edited"))
	title, description, acceptance := "Bigger", "now described", "it holds better"
	done(ds.EditTicket(ctx, by(4), "BIG-1", dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &title, Description: &description, Acceptance: &acceptance}}))
	status, raw, out := e.call(t, "GET", "/api/dispatch/tickets/BIG-1/plans", nil)
	if status != http.StatusOK {
		t.Fatalf("the list: %d %.300s", status, raw)
	}
	plans := listOf(t, out, "plans")
	if len(plans) != dispatch.MaxPlans || plans[0]["n"] != float64(dispatch.MaxPlans) || plans[0]["stale"] == nil || plans[0]["body"] != nil ||
		plans[0]["crewChanged"] != true || plans[len(plans)-1]["state"] != dispatch.PlanWithdrawn {
		t.Fatalf("the list: %d revisions, the newest %v", len(plans), plans[0])
	}
	t.Logf("the largest list of revisions: %d bytes of %d", len(raw), MaxPlansReply)
	if len(raw) >= MaxPlansReply {
		t.Fatalf("the largest list of revisions is %d bytes, not under %d", len(raw), MaxPlansReply)
	}
}

// The approval and the withdrawal: approved with a note, the revision
// before superseded; approving the approved one again invalid; the
// revision expected and the ticket's revision expected each
// stale_revision; withdrawn, it cannot be approved; a revision missing is
// not_found with its receipt; an intent edit makes the approval stale,
// its remedy approve, and approving again cures it; a resend of an
// approval is answered from its receipt even after an agent changed.
func TestDispatchPlanApproveAndWithdraw(t *testing.T) {
	p := newPlanTest(t, nil)
	saved := p.saveCrew(t, teamCrew("Team", "cat"))
	r1, r2 := p.proposed(t, saved.ID, nil), p.proposed(t, saved.ID, nil)
	approve := func(id string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		if _, ok := body["opId"]; !ok {
			body["opId"] = opID()
		}
		return p.expect(t, "POST", "/api/dispatch/plans/"+id+"/approve", body, status, code)
	}
	out := approve(r1, map[string]any{"opId": "approve-r1", "byName": "Nate", "note": "Looks right."}, http.StatusOK, "")
	if plan := entityOf(t, out, "plan"); plan["state"] != dispatch.PlanApproved || plan["approvedNote"] != "Looks right." || plan["approvedBy"] != "Nate" ||
		plan["approvedRev"] != 1.0 || plan["body"] != nil {
		t.Fatalf("approved: %v", plan)
	}
	approve(r1, map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid)
	approve(r2, map[string]any{"expectedRev": 9}, http.StatusConflict, dispatch.RefusalStaleRevision)
	approve(r2, map[string]any{"ticketRev": 9}, http.StatusConflict, dispatch.RefusalStaleRevision)
	approve(r2, map[string]any{"ticketRev": 1}, http.StatusOK, "")
	plans := p.plansOf(t, "CND-1")
	if plans[r1]["state"] != dispatch.PlanSuperseded || plans[r2]["state"] != dispatch.PlanApproved || plans[r1]["n"] != 1.0 || plans[r2]["n"] != 2.0 {
		t.Fatalf("after the second approval: %v", plans)
	}
	// Stale with the intent; approved again.
	p.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"description": "It loops twice."}}, http.StatusOK, "")
	stale, _ := p.plansOf(t, "CND-1")[r2]["stale"].(map[string]any)
	if stale == nil || stale["remedy"] != dispatch.RemedyApprove || stale["words"] != "the description changed since revision 2 was approved" {
		t.Fatalf("stale: %v", stale)
	}
	approve(r2, map[string]any{"note": "Still right."}, http.StatusOK, "")
	if v := p.plansOf(t, "CND-1")[r2]; v["stale"] != nil || v["approvedNote"] != "Still right." {
		t.Fatalf("approved again: %v", v)
	}
	// Withdrawn.
	out = p.expect(t, "POST", "/api/dispatch/plans/"+r2+"/withdraw", map[string]any{"opId": opID(), "byName": "Nate"}, http.StatusOK, "")
	if plan := entityOf(t, out, "plan"); plan["state"] != dispatch.PlanWithdrawn || plan["withdrawnBy"] != "Nate" {
		t.Fatalf("withdrawn: %v", plan)
	}
	p.expect(t, "POST", "/api/dispatch/plans/"+r2+"/withdraw", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid)
	approve(r2, map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid)
	// Missing: the store's not_found, its receipt kept.
	for _, route := range []string{"approve", "withdraw"} {
		out := p.expect(t, "POST", "/api/dispatch/plans/0123456789abcdef/"+route, map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
		if out["receipt"] == nil {
			t.Fatalf("%s of a revision missing: %v", route, out)
		}
	}
	p.expect(t, "GET", "/api/dispatch/plans/0123456789abcdef", nil, http.StatusNotFound, "not_found")
	p.expect(t, "GET", "/api/dispatch/plans/nope", nil, http.StatusBadRequest, "invalid_request")
	p.expect(t, "GET", "/api/dispatch/plans/"+r1+"?x=1", nil, http.StatusBadRequest, "invalid_request")
	p.expect(t, "GET", "/api/dispatch/tickets/CND-9/plans", nil, http.StatusNotFound, "not_found")
	// The first approval resent after its agent changed: its receipt,
	// never stale_plan.
	p.save(map[string]any{"id": "cat", "name": "cat", "command": []string{"/bin/cat", "-u"}})
	out = approve(r1, map[string]any{"opId": "approve-r1", "byName": "Nate", "note": "Looks right."}, http.StatusOK, "")
	if out["receipt"].(map[string]any)["repeat"] != true {
		t.Fatalf("the resend: %v", out)
	}
}
