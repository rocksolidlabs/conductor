package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A new project (issue 1179): the operator's own proposal of an agent
// trial's plan revision, and the operator's own launch of a crew with
// worktrees, make a new folder's first commit; nobody else's request does,
// nor any check or read, and a folder agents may not use is refused before
// anything is made. Each test runs git under a HOME of its own (planRepo's
// t.Setenv), so none runs in parallel.

// newFolder is an empty folder under the server's allowed root, no
// repository yet, for a server whose first commits read no system
// configuration.
func newFolder(t *testing.T, p *planTest, name string) string {
	t.Helper()
	// The first commit's gits read no system configuration of the machine's.
	p.srv.firstCommitEnv.Store(&map[string]string{"GIT_CONFIG_NOSYSTEM": "1"})
	dir := filepath.Join(realRoot(t, p.root), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// noGit fails t when dir holds a repository.
func noGit(t *testing.T, dir, who string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: a repository was made in %s (%v)", who, dir, err)
	}
}

// headOf is dir's HEAD, as git reads it.
func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// The others a proposal or a launch may come from: an agent's session
// token, a share link, a host token and no token at all.
func notTheOperator(t *testing.T, p *planTest) map[string]string {
	t.Helper()
	sid := p.createSession("ready-cat")
	_, link, err := p.srv.links.Create(sid, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p.srv.limiter = newRateLimiter(1e6, 1e6)
	return map[string]string{"an agent's token": p.agentToken(sid), "a share link": link, "a host token": "test-host-token", "no token": ""}
}

// The operator proposes a trial's plan revision on a ticket of a project
// whose folder is a new one: the folder's first commit is made and is the
// revision's base. Anyone else's proposal of it is refused before anything
// is made, and so is the operator's proposal of a crew that is no trial.
func TestTheOperatorsTrialProposalMakesANewFoldersFirstCommit(t *testing.T) {
	p := newCompareTicket(t)
	fresh := newFolder(t, p, "new-shop")
	p.newDispatchProject(t, "NEW", map[string]any{"cwd": fresh})
	p.newDispatchTicket(t, "NEW", "Add rate limiting", map[string]any{"description": "It has none."})
	trial := entityOf(t, p.expect(t, "POST", "/api/crews/templates/compare", compareSpecBody("Rate limit", fresh, 2, false, nil), http.StatusCreated, ""), "crew")["id"].(string)
	team := p.saveCrew(t, teamCrew("Team", "ready-cat"))
	noGit(t, fresh, "saving the trial")

	for who, tok := range notTheOperator(t, p) {
		body := map[string]any{"opId": opID(), "crew": trial}
		if resp, out := p.do("POST", "/api/dispatch/tickets/NEW-1/plans", tok, body); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %v", who, resp.StatusCode, out)
		}
		noGit(t, fresh, who)
	}
	// The operator's proposal of a crew that is no trial: refused as before.
	p.propose(t, "NEW-1", map[string]any{"crew": team.ID}, http.StatusConflict, dispatch.RefusalInvalid)
	noGit(t, fresh, "a crew that is no trial")

	out := p.propose(t, "NEW-1", map[string]any{"crew": trial}, http.StatusCreated, "")
	_, body, _ := p.planBodyOf(t, entityOf(t, out, "plan")["id"].(string))
	head := headOf(t, fresh)
	if body.Base != head || !crew.IsFirstCommit(context.Background(), fresh, head) {
		t.Fatalf("the revision's base is %s, the folder's first commit %s", body.Base, head)
	}
}

// A Brakeman turn's follow-up reaches the same proposal with a guard, and
// the make of a revision Brakeman drafted reaches it as the operator: no
// proposal the person wrote, so a new folder is refused before anything is
// made.
func TestAGuardedProposalMakesNoFirstCommit(t *testing.T) {
	p := newCompareTicket(t)
	fresh := newFolder(t, p, "new-shop")
	p.newDispatchProject(t, "NEW", map[string]any{"cwd": fresh})
	p.newDispatchTicket(t, "NEW", "Add rate limiting", nil)
	trial := entityOf(t, p.expect(t, "POST", "/api/crews/templates/compare", compareSpecBody("Rate limit", fresh, 2, false, nil), http.StatusCreated, ""), "crew")["id"].(string)
	rec := httptest.NewRecorder()
	// The operator's flag set as the route would for an admin: the guard alone keeps it off.
	p.srv.ticketPropose(context.Background(), rec, p.srv.dispatch, adminOp("Nate"), "NEW-1", dispatch.PlanRequest{Crew: trial}, &planGuard{}, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not in a git repository") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	noGit(t, fresh, "a guarded proposal")
	// The person's make of a revision Brakeman drafted reaches the route as the operator, and is no proposal the person wrote.
	got, err := p.srv.proposeAsOperator(context.Background(), planMake{Key: "NEW-1", Request: dispatch.PlanRequest{Crew: trial}}, opID(), "Nate")
	if err != nil || got.done || !strings.Contains(got.why, "not in a git repository") {
		t.Fatalf("the make of a drafted revision: %+v %v", got, err)
	}
	noGit(t, fresh, "the make of a drafted revision")
}

// The operator's launch of a crew with worktrees in a new folder makes its
// first commit and says so in the run's log, before launched; anyone
// else's is refused and nothing is made; a folder agents may not use is
// refused before anything is made.
func TestTheOperatorsLaunchMakesANewFoldersFirstCommit(t *testing.T) {
	p := newPlanTest(t, nil, readyCat)
	srv := p.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	fresh := newFolder(t, p, "new-app")
	c := p.saveCrew(t, crew.Crew{Name: "Pair", Cwd: fresh, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartImmediately}}}})
	for who, tok := range notTheOperator(t, p) {
		if resp, out := p.do("POST", "/api/crews/"+c.ID+"/launch", tok, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d %v", who, resp.StatusCode, out)
		}
		noGit(t, fresh, who)
	}
	// Outside the allowed root: refused before anything is made.
	outside := t.TempDir()
	far := p.saveCrew(t, crew.Crew{Name: "Far", Cwd: outside, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartImmediately}}}})
	p.expect(t, "POST", "/api/crews/"+far.ID+"/launch", nil, http.StatusBadRequest, "invalid_cwd")
	noGit(t, outside, "a folder outside the roots")

	run := entityOf(t, p.expect(t, "POST", "/api/crews/"+c.ID+"/launch", nil, http.StatusCreated, ""), "run")
	head := headOf(t, fresh)
	if !crew.IsFirstCommit(context.Background(), fresh, head) {
		t.Fatalf("HEAD %s is not Conductor's first commit", head)
	}
	got, ok := srv.runs.Get(run["id"].(string))
	if !ok {
		t.Fatal("the run is gone")
	}
	made, launched := -1, -1
	for i, e := range got.Log {
		switch {
		case e.Message == "made the first commit in "+fresh:
			made = i
		case strings.HasPrefix(e.Message, "launched "):
			launched = i
		}
	}
	if made < 0 || launched < made {
		t.Fatalf("the run's log: %+v", got.Log)
	}
	// A second launch finds the commit: no second one, no second entry.
	stopEveryRun(srv)
	again := entityOf(t, p.expect(t, "POST", "/api/crews/"+c.ID+"/launch", nil, http.StatusCreated, ""), "run")
	if h := headOf(t, fresh); h != head {
		t.Fatalf("a second commit: %s after %s", h, head)
	}
	second, _ := srv.runs.Get(again["id"].(string))
	for _, e := range second.Log {
		if strings.HasPrefix(e.Message, "made the first commit") {
			t.Fatalf("the second run says it made the commit: %+v", second.Log)
		}
	}
}

// Every other way to a launch or a proposal makes no first commit, the
// folder a new one: a crew an agent forms round its session with the
// agent's token, a provider's launch, and the operator's proposal of a
// trial whose folder is outside the folders agents may use. Each is
// refused as before, and no .git appears.
func TestNoOtherWayMakesAFirstCommit(t *testing.T) {
	p := newCompareTicket(t)
	fresh := newFolder(t, p, "new-team")

	// A crew an agent forms round its session in a new folder.
	resp, out := p.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "ready-cat", "cwd": fresh})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("session: %d %v", resp.StatusCode, out)
	}
	sid := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := p.srv.registry.Get(sid); ok {
			_ = d.Stop(context.Background())
		}
	})
	body := crewBody("lead", memberDef("lead", "ready-cat", "manual"), memberDef("other", "ready-cat", "manual"))
	body["isolation"] = "worktree"
	if resp, out := p.do("POST", "/api/sessions/"+sid+"/crew", agentOf(t, p.testEnv, sid), body); resp.StatusCode != http.StatusConflict || out["error"].(map[string]any)["code"] != "not_a_repo" {
		t.Fatalf("an agent's crew: %d %v", resp.StatusCode, out)
	}
	noGit(t, fresh, "an agent's crew")

	// A provider's launch of a saved crew with worktrees there.
	c := p.saveCrew(t, crew.Crew{Name: "Nightly", Cwd: fresh, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartImmediately}}}})
	if _, err := testHost(p.srv, t.Context(), "sched").Launch(t.Context(), ext.LaunchRequest{Crew: c.ID, By: ada}); err == nil {
		t.Fatal("a provider's launch in a new folder started")
	}
	noGit(t, fresh, "a provider's launch")

	// The operator's proposal of a trial whose folder is outside the roots.
	outside := t.TempDir()
	trial := entityOf(t, p.expect(t, "POST", "/api/crews/templates/compare", compareSpecBody("Far", fresh, 2, false, nil), http.StatusCreated, ""), "crew")["id"].(string)
	saved, err := p.srv.crews.Get(trial)
	if err != nil {
		t.Fatal(err)
	}
	saved.Cwd = outside
	if _, err := p.srv.crews.Update(saved.ID, saved); err != nil {
		t.Fatal(err)
	}
	// A project with no folder of its own: the proposal's folder is the crew's.
	p.newDispatchProject(t, "FAR", nil)
	p.newDispatchTicket(t, "FAR", "Far away", nil)
	out = p.propose(t, "FAR-1", map[string]any{"crew": trial}, http.StatusConflict, dispatch.RefusalInvalid)
	if !strings.Contains(fmt.Sprint(out), "outside the allowed roots") {
		t.Fatalf("refused for another reason: %v", out)
	}
	noGit(t, outside, "a proposal outside the roots")
	noGit(t, fresh, "a proposal outside the roots")
}
