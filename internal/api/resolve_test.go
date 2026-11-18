package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The resolved launch and a launch held to it (resolve.go).

// resolveEnv is a ready environment with a repository, a crew of one
// task, and a Dispatch project whose folder is the repository.
func resolveEnv(t *testing.T, mutate func(*config.Config)) (*testEnv, string) {
	t.Helper()
	e := readyEnv(t, mutate)
	e.gitRepo(t)
	endRunsAtCleanup(t, e)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": e.root})
	return e, originCrew(t, e, "Reviews")
}

// resolve resolves the crew's launch for project CND at commit.
func resolve(t *testing.T, e *testEnv, crewID, commit string) ext.ResolvedLaunch {
	t.Helper()
	rl, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: crewID, Project: "CND", Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	if err := rl.Valid(); err != nil {
		t.Fatalf("the resolved launch: %v", err)
	}
	return rl
}

// launchHeldTo is a provider's launch held to digest.
func launchHeldTo(crewID, opID, digest string) ext.LaunchRequest {
	r := originLaunch(crewID, opID)
	r.Project, r.ResolvedDigest = "CND", digest
	return r
}

// A resolve answers the crew, its folder, whether the folder is in the
// project, its manifest, each member's agent selection and yolo, and a
// digest; a nil yolo and an empty folder resolve to the server's, and a
// change of either changes the digest. Two resolves of an unchanged crew,
// server and commit, and a resolve after a restart, give one digest. A
// resolve keeps nothing: no run, no row, no record.
func TestResolveLaunchFollowsTheServerDefaults(t *testing.T) {
	e, id := resolveEnv(t, func(c *config.Config) { c.DefaultCwd = "" })
	rl := resolve(t, e, id, "")
	if rl.Crew != id || !rl.Managed || rl.Tasks != 1 || rl.Where != ext.RunsOnServer || rl.Cwd == "" || !rl.InProject || rl.ProjectCwd != rl.Cwd ||
		rl.Manifest.Source != ext.ManifestNone || len(rl.Members) != 1 || rl.Members[0].Yolo != e.srv.cfg.Yolo || len(rl.Members[0].Selection) != 64 {
		t.Fatalf("the resolved launch: %+v", rl)
	}
	if again := resolve(t, e, id, ""); again.Digest != rl.Digest {
		t.Fatalf("two resolves: %s and %s", rl.Digest, again.Digest)
	}
	if n := ledgerRuns(t, e); n != 0 || len(e.srv.runs.List()) != 0 {
		t.Fatalf("a resolve made %d runs", n)
	}
	// The server's yolo default, which the crew does not set.
	e.srv.cfg.Yolo = !e.srv.cfg.Yolo
	flipped := resolve(t, e, id, "")
	if flipped.Digest == rl.Digest || flipped.Members[0].Yolo == rl.Members[0].Yolo {
		t.Fatalf("the server's yolo changed, the digest did not: %+v", flipped)
	}
	e.srv.cfg.Yolo = !e.srv.cfg.Yolo
	// A crew with no folder takes the server's default folder.
	plain := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Nofolder", "goal": "g", "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks":   []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusCreated)["id"].(string)
	e.srv.cfg.DefaultCwd = e.root
	at := resolve(t, e, plain, "")
	sub := filepath.Join(e.root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	e.srv.cfg.DefaultCwd = sub
	moved := resolve(t, e, plain, "")
	if at.Cwd == moved.Cwd || at.Digest == moved.Digest || !moved.InProject {
		t.Fatalf("the default folder changed: %+v then %+v", at, moved)
	}
	e.srv.cfg.DefaultCwd = ""
	shutdown(e)
	if after := resolve(t, restartEnding(t, e), id, ""); after.Digest != rl.Digest {
		t.Fatalf("after a restart: %s, was %s", after.Digest, rl.Digest)
	}
}

// A launch held to a digest of the server's yolo default before it
// changed is refused launch_changed, nothing launched.
func TestServerYoloDefaultChangeRefusesTheLaunch(t *testing.T) {
	e, id := resolveEnv(t, nil)
	rl := resolve(t, e, id, "")
	e.srv.cfg.Yolo = !e.srv.cfg.Yolo
	defer func() { e.srv.cfg.Yolo = !e.srv.cfg.Yolo }()
	if _, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), launchHeldTo(id, "op-1", rl.Digest)); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
		t.Fatalf("a launch after the yolo default changed: %v", err)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs", n)
	}
}

// A member's catalogue entry changed in its command, its session recipe
// or its environment's names: another digest, and a launch held to the
// earlier one refused launch_changed.
func TestResolvedLaunchPinsTheAgentSelection(t *testing.T) {
	e, id := resolveEnv(t, nil)
	rl := resolve(t, e, id, "")
	cat := e.srv.Catalog()
	for name, change := range map[string]func(*catalog.Agent){
		"command": func(a *catalog.Agent) { a.Command = append(a.Command, "--more") },
		"session": func(a *catalog.Agent) {
			a.Session = &catalog.SessionRecipe{StartArgs: []string{"--session-id", "{id}"}, ResumeArgs: []string{"--resume", "{id}"}, IDPattern: "^[a-z0-9][a-z0-9-]{0,63}$"}
		},
		"env": func(a *catalog.Agent) { a.Env = map[string]string{"EXTRA": "1"} },
	} {
		edited := cat.Clone()
		a, _ := edited.Get("ready-cat")
		change(&a)
		if err := edited.Upsert(a); err != nil {
			t.Fatal(err)
		}
		e.setCatalog(edited)
		if got := resolve(t, e, id, ""); got.Digest == rl.Digest || got.Members[0].Selection == rl.Members[0].Selection {
			t.Fatalf("the entry's %s changed, the digest did not", name)
		}
		if _, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), launchHeldTo(id, "op-"+name, rl.Digest)); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
			t.Fatalf("a launch after the entry's %s changed: %v", name, err)
		}
		e.setCatalog(cat)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs", n)
	}
	// Unchanged: the launch is held to it and runs, its row keeping the
	// digest, the project and the confirmed selection.
	ref, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), launchHeldTo(id, "op-ok", rl.Digest))
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ := e.srv.ledger.Origin(t.Context(), ref.ID)
	if row.ResolvedDigest != rl.Digest || row.Project != "CND" || !strings.Contains(row.Agents, rl.Members[0].Selection) {
		t.Fatalf("the origin row: %+v", row)
	}
	_ = e.srv.runs.Stop(t.Context(), ref.ID)
}

// A crew on a host resolves with Where host, its folder as saved; one
// saved with no folder of its own is refused invalid_cwd.
func TestHostCrewResolvesWhereHost(t *testing.T) {
	e, _ := resolveEnv(t, nil)
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Remote", "goal": "g", "cwd": "/home/ada/src", "where": "host", "isolation": "none",
		"members": []any{catMember("core", "immediately")}}, http.StatusCreated)["id"].(string)
	rl := resolve(t, e, id, "")
	if rl.Where != ext.RunsOnHost || rl.Managed || rl.Cwd != "/home/ada/src" || rl.InProject {
		t.Fatalf("a host's crew: %+v", rl)
	}
	// One saved with no folder of its own: refused, never a record that
	// fails its own bounds.
	none := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Remote default", "goal": "g", "where": "host", "isolation": "none",
		"members": []any{catMember("core", "immediately")}}, http.StatusCreated)["id"].(string)
	if _, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: none, Project: "CND"}); ext.RefusalCode(err) != ext.RefusalInvalidCwd {
		t.Fatalf("a host's crew with no folder: %v", err)
	}
}

// InProject is the launch folder itself or a folder under it, by a path
// separator; a sibling whose name begins the same is not, nor a folder
// with no project.
func TestInProject(t *testing.T) {
	for _, tc := range []struct {
		cwd, project string
		want         bool
	}{
		{"/src/acme", "/src/acme", true},
		{"/src/acme/web", "/src/acme", true},
		{"/src/acme-evil", "/src/acme", false},
		{"/src/acme", "", false},
		{"/src", "/src/acme", false},
		{"/src/acme/x", "/", true},
	} {
		if got := inProject(tc.cwd, tc.project); got != tc.want {
			t.Fatalf("inProject(%q, %q) = %v", tc.cwd, tc.project, got)
		}
	}
	// A symbolic link inside the project leading out is resolved first.
	e, _ := resolveEnv(t, nil)
	out := t.TempDir()
	e.srv.cfg.AllowedRoots = append(e.srv.cfg.AllowedRoots, out)
	link := filepath.Join(e.root, "out")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Linked", "goal": "g", "cwd": link, "where": "server", "isolation": "none",
		"members": []any{catMember("core", "immediately")}}, http.StatusCreated)["id"].(string)
	if rl := resolve(t, e, id, ""); rl.InProject {
		t.Fatalf("a folder linked out of the project: %+v", rl)
	}
	// A project with no folder: not in it.
	e.newDispatchProject(t, "NOF", nil)
	rl, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "NOF"})
	if err != nil || rl.InProject || rl.ProjectCwd != "" {
		t.Fatalf("a project with no folder: %+v %v", rl, err)
	}
}

// The project is checked again in the launch: its folder moved (to the
// folder above the launch folder, which stays in it) after the resolve
// is launch_changed. An unknown project is project_unknown, an archived
// one project_archived; a digest with no project is refused by Valid. What
// the engine is handed is what was compared: a change between the
// compare and the engine does not reach the run.
func TestLaunchRechecksTheProjectUnderTheLock(t *testing.T) {
	e, id := resolveEnv(t, nil)
	app := filepath.Join(e.root, "app")
	commitFile(t, e.root, "app/README", "app\n")
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "cwd": app}, http.StatusOK, "")
	e.sendCrew("PUT", "/api/crews/"+id, map[string]any{"name": "Reviews", "goal": "g", "cwd": app, "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks":   []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusOK)
	rl := resolve(t, e, id, "")
	h := testHost(e.srv, t.Context(), "example")
	// The project's folder moved to the repository's root: the launch
	// folder is still in it, and the launch is refused all the same.
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "cwd": e.root}, http.StatusOK, "")
	if moved := resolve(t, e, id, ""); !moved.InProject || moved.Digest == rl.Digest {
		t.Fatalf("the project moved over the launch folder: %+v", moved)
	}
	if _, err := h.Launch(t.Context(), launchHeldTo(id, "op-1", rl.Digest)); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
		t.Fatalf("a launch after the project's folder moved: %v", err)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "cwd": app}, http.StatusOK, "")
	r := launchHeldTo(id, "op-2", rl.Digest)
	r.Project = "ZZZ"
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalProjectUnknown {
		t.Fatalf("an unknown project: %v", err)
	}
	r.Project = ""
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Fatalf("a digest with no project: %v", err)
	}
	e.newDispatchProject(t, "OLD", map[string]any{"cwd": e.root})
	e.expect(t, "POST", "/api/dispatch/projects/OLD/archive", map[string]any{"opId": opID()}, http.StatusOK, "")
	if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "OLD"}); ext.RefusalCode(err) != ext.RefusalProjectArchived {
		t.Fatalf("an archived project: %v", err)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs after the refusals", n)
	}
	// Between the compare and the engine the crew is saved again with
	// another task: the run is the compared crew's.
	afterResolveCompare = func() {
		e.sendCrew("PUT", "/api/crews/"+id, map[string]any{"name": "Reviews", "goal": "g", "cwd": app, "where": "server", "isolation": "worktree",
			"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
			"tasks":   []any{map[string]any{"id": "other", "member": "lead", "kind": "work", "instructions": "Something else."}}}, http.StatusOK)
	}
	t.Cleanup(func() { afterResolveCompare = nil })
	ref, err := h.Launch(t.Context(), launchHeldTo(id, "op-3", rl.Digest))
	afterResolveCompare = nil
	if err != nil {
		t.Fatal(err)
	}
	run, _ := e.srv.runs.Get(ref.ID)
	if len(run.Tasks) != 1 || run.Tasks[0].ID != "build" {
		t.Fatalf("the run's tasks are not the compared crew's: %+v", run.Tasks)
	}
	_ = e.srv.runs.Stop(t.Context(), ref.ID)
}

// manifestCrew saves a crew whose project manifest is the repository's
// file.
func manifestCrew(t *testing.T, e *testEnv) string {
	t.Helper()
	return e.sendCrew("POST", "/api/crews", map[string]any{"name": "Manifested", "goal": "g", "cwd": e.root, "where": "server", "isolation": "worktree",
		"manifest": map[string]any{"file": true},
		"members":  []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks":    []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusCreated)["id"].(string)
}

// The manifest file's blob at the commit is in the digest: a commit that
// changes conductor.manifest.json alone changes it; a launch held to the
// digest of one commit at a start commit with another manifest is refused
// launch_changed.
func TestResolveLaunchPinsTheManifestFile(t *testing.T) {
	e, _ := resolveEnv(t, nil)
	first := commitFile(t, e.root, "conductor.manifest.json", `{"schema":1,"setup":[{"id":"one","argv":["true"]}]}`)
	id := manifestCrew(t, e)
	a := resolve(t, e, id, first)
	second := commitFile(t, e.root, "conductor.manifest.json", `{"schema":1,"setup":[{"id":"two","argv":["true"]}]}`)
	b := resolve(t, e, id, second)
	if a.Manifest.Source != ext.ManifestFile || a.Manifest.Blob == "" || a.Manifest.Blob == b.Manifest.Blob || a.Digest == b.Digest {
		t.Fatalf("two manifests: %+v and %+v", a.Manifest, b.Manifest)
	}
	if head := resolve(t, e, id, ""); head.Digest != b.Digest {
		t.Fatalf("HEAD is the second commit, its digest another: %+v", head)
	}
}

func TestLaunchRefusesAChangedManifestAtTheStartCommit(t *testing.T) {
	e, _ := resolveEnv(t, nil)
	first := commitFile(t, e.root, "conductor.manifest.json", `{"schema":1,"setup":[{"id":"one","argv":["true"]}]}`)
	id := manifestCrew(t, e)
	a := resolve(t, e, id, first)
	second := commitFile(t, e.root, "conductor.manifest.json", `{"schema":1,"setup":[{"id":"two","argv":["true"]}]}`)
	r := launchHeldTo(id, "op-1", a.Digest)
	r.StartCommit = second
	if _, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
		t.Fatalf("a launch at a start commit with another manifest: %v", err)
	}
	if n := ledgerRuns(t, e); n != 0 {
		t.Fatalf("%d runs", n)
	}
}

// The start commit is the run's base: its task with no input starts
// there, and the ledger and the origin keep it; a commit the repository
// lacks is start_commit_missing; an annotated tag's own id is no commit
// (invalid_request); a short id and a ref name are refused by Valid.
func TestStartCommitIsTheRunBase(t *testing.T) {
	e, id := resolveEnv(t, nil)
	base := gitIn(t, e.root, "rev-parse", "HEAD")
	commitFile(t, e.root, "later.txt", "later\n")
	h := testHost(e.srv, t.Context(), "example")
	r := originLaunch(id, "op-1")
	r.StartCommit = base
	ref, err := h.Launch(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if a := runningAttempt(t, e, ref.ID, "build", 1); a["start"] != base {
		t.Fatalf("the attempt starts at %v, not %s", a["start"], base)
	}
	if row, _, _ := e.srv.ledger.Run(t.Context(), ref.ID); row.Base != base {
		t.Fatalf("the run's base: %s", row.Base)
	}
	if st, _ := h.Run(t.Context(), ref.ID); st.Origin.StartCommit != base {
		t.Fatalf("the origin's start commit: %+v", st.Origin)
	}
	_ = e.srv.runs.Stop(t.Context(), ref.ID)
	r = originLaunch(id, "op-2")
	r.StartCommit = strings.Repeat("e", 40)
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalStartCommitMissing {
		t.Fatalf("a commit the repository lacks: %v", err)
	}
	if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND", Commit: strings.Repeat("e", 40)}); ext.RefusalCode(err) != ext.RefusalStartCommitMissing {
		t.Fatalf("a resolve at a commit the repository lacks: %v", err)
	}
	// An annotated tag's own id peels to a commit, and is no commit.
	gitIn(t, e.root, "-c", "tag.gpgSign=false", "tag", "-a", "v1", "-m", "x", base)
	tag := gitIn(t, e.root, "rev-parse", "v1")
	r.StartCommit = tag
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalInvalidRequest || !strings.Contains(err.Error(), "not a commit") {
		t.Fatalf("a tag's id as the start commit: %v", err)
	}
	if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND", Commit: tag}); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Fatalf("a resolve at a tag's id: %v", err)
	}
	// A tree's and a blob's ids name objects the repository holds, and no
	// commit: invalid_request, never start_commit_missing.
	for kind, obj := range map[string]string{"tree": gitIn(t, e.root, "rev-parse", "HEAD^{tree}"), "blob": gitIn(t, e.root, "rev-parse", "HEAD:README")} {
		r.StartCommit = obj
		if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalInvalidRequest || !strings.Contains(err.Error(), "names a "+kind) {
			t.Fatalf("a %s's id as the start commit: %v", kind, err)
		}
		if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND", Commit: obj}); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
			t.Fatalf("a resolve at a %s's id: %v", kind, err)
		}
	}
	for _, bad := range []string{base[:12], "HEAD~1", "main"} {
		r.StartCommit = bad
		if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
			t.Fatalf("start commit %q: %v", bad, err)
		}
	}
}

// A provider's launch mints no view link, whatever the crew's
// ViewLinkTTLSeconds says: none in the store, none in the run's log.
func TestProviderLaunchMintsNoViewLink(t *testing.T) {
	e, _ := resolveEnv(t, nil)
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Shared", "goal": "g", "cwd": e.root, "where": "server", "isolation": "worktree",
		"viewLinkTtlSeconds": 3600,
		"members":            []any{map[string]any{"name": "lead", "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks":              []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}, http.StatusCreated)["id"].(string)
	if c, err := e.srv.crews.Get(id); err != nil || c.ViewLinkTTLSeconds != 3600 {
		t.Fatalf("the crew asks for no view link: %+v %v", c.ViewLinkTTLSeconds, err)
	}
	h := testHost(e.srv, t.Context(), "example")
	for i, r := range []ext.LaunchRequest{originLaunch(id, "op-1"), launchHeldTo(id, "op-2", resolve(t, e, id, "").Digest)} {
		ref, err := h.Launch(t.Context(), r)
		if err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
		if links := e.srv.links.ListByRun(ref.ID); len(links) != 0 {
			t.Fatalf("launch %d minted links: %+v", i, links)
		}
		run, _ := e.srv.runs.Get(ref.ID)
		for _, l := range run.Log {
			if strings.Contains(l.Message, "link created") {
				t.Fatalf("launch %d's log: %+v", i, run.Log)
			}
		}
		_ = e.srv.runs.Stop(t.Context(), ref.ID)
	}
}

// A resolve keeps nothing: no reservation once it has answered (the
// account it bound can be removed at once), no run, no row, no record.
func TestResolveLaunchKeepsNothing(t *testing.T) {
	e, id := resolveEnv(t, nil)
	before, _ := os.ReadDir(filepath.Join(e.srv.cfg.DataDir, "runs"))
	for range 3 {
		resolve(t, e, id, "")
	}
	after, _ := os.ReadDir(filepath.Join(e.srv.cfg.DataDir, "runs"))
	if len(after) != len(before) || ledgerRuns(t, e) != 0 {
		t.Fatalf("a resolve wrote: %d entries, were %d", len(after), len(before))
	}
}

// A resolved launch past its bound (twelve members with names at their
// bound in a long folder) is refused resolved_too_large, from the resolve
// and from a launch, never cut.
func TestAResolvedLaunchPastItsBoundIsRefused(t *testing.T) {
	e, _ := resolveEnv(t, nil)
	deep := e.root
	for len(deep) < 3000 {
		deep = filepath.Join(deep, strings.Repeat("d", 200))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Skipf("no folder that deep here: %v", err)
	}
	var members []any
	var tasks []any
	for i := range 12 {
		name := strings.Repeat(string(rune('a'+i)), 40)
		members = append(members, map[string]any{"name": name, "agentId": "ready-cat", "prompt": "", "start": map[string]any{"when": "task"}})
		tasks = append(tasks, map[string]any{"id": "t" + string(rune('a'+i)), "member": name, "kind": "work", "instructions": "Do it."})
	}
	id := e.sendCrew("POST", "/api/crews", map[string]any{"name": "Wide", "goal": "g", "cwd": deep, "where": "server", "isolation": "worktree",
		"members": members, "tasks": tasks}, http.StatusCreated)["id"].(string)
	// The project's folder as deep: both are in the record.
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "cwd": deep}, http.StatusOK, "")
	h := testHost(e.srv, t.Context(), "example")
	if _, err := h.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND"}); ext.RefusalCode(err) != ext.RefusalResolvedTooLarge {
		t.Fatalf("a resolve past the bound: %v", err)
	}
	r := originLaunch(id, "op-1")
	r.Project = "CND"
	if _, err := h.Launch(t.Context(), r); ext.RefusalCode(err) != ext.RefusalResolvedTooLarge {
		t.Fatalf("a launch past the bound: %v", err)
	}
}

// The desk's Host resolves nothing, nor a server with no Dispatch store;
// an unknown crew is ext.ErrNotFound and a commit of another shape
// invalid_request.
func TestResolveLaunchDoors(t *testing.T) {
	e, id := resolveEnv(t, nil)
	desk := &extHost{s: e.srv, name: "brakeman", ctx: t.Context(), desk: true}
	if _, err := desk.ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("the desk's resolve: %v", err)
	}
	if _, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: "nope"}); !errors.Is(err, ext.ErrNotFound) {
		t.Fatalf("no such crew: %v", err)
	}
	if _, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Commit: "HEAD"}); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Fatalf("a commit of another shape: %v", err)
	}
	// A server with no Dispatch store resolves nothing, and holds no
	// launch to a project.
	store := e.srv.dispatch
	e.srv.dispatch = nil
	t.Cleanup(func() { e.srv.dispatch = store })
	if _, err := testHost(e.srv, t.Context(), "example").ResolveLaunch(t.Context(), ext.ResolveRequest{Crew: id, Project: "CND"}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a resolve with no Dispatch store: %v", err)
	}
	r := originLaunch(id, "op-1")
	r.Project = "CND"
	if _, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), r); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a launch for a project with no Dispatch store: %v", err)
	}
	e.srv.dispatch = store
	_ = crew.ErrInvalid
}

// An attempt of a run whose launch was held to a resolved launch starts
// its agent only from the entry the launch confirmed: the entry changed
// between the first attempt and the second, the second fails at "agent",
// not counted, no session started, and the run pauses, in words that name
// no plan revision.
func TestAttemptRefusesAChangedAgentSelection(t *testing.T) {
	e, id := resolveEnv(t, nil)
	rl := resolve(t, e, id, "")
	ref, err := testHost(e.srv, t.Context(), "example").Launch(t.Context(), launchHeldTo(id, "op-1", rl.Digest))
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, ref.ID, "build", 1)
	cat := e.srv.Catalog()
	edited := cat.Clone()
	a, _ := edited.Get("ready-cat")
	a.Command = append(a.Command, "--more")
	if err := edited.Upsert(a); err != nil {
		t.Fatal(err)
	}
	e.setCatalog(edited)
	command(t, e, ref.ID, "tasks/build/cancel")
	command(t, e, ref.ID, "tasks/build/retry")
	task := taskVia(t, e, ref.ID, "build", "attempt 2 failed at agent", func(task map[string]any) bool {
		at, _ := task["attempt"].(map[string]any)
		return at != nil && at["n"] == float64(2) && at["state"] == "failed" && at["stage"] == "agent"
	})
	at := task["attempt"].(map[string]any)
	if at["sessionId"] != nil || !strings.Contains(at["error"].(string), "this run's launch") || strings.Contains(at["error"].(string), "plan revision") {
		t.Fatalf("attempt 2: %v", at)
	}
	if !waitFor(testWait, func() bool { r, _ := e.srv.runs.Get(ref.ID); return r.Paused }) {
		t.Fatal("the run did not pause")
	}
	_ = e.srv.runs.Stop(t.Context(), ref.ID)
}
