package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The session clone (#434), POST /api/sessions/{id}/clone. clone_test.go is
// the agent clone (#291).

// cloneSession posts a clone of id with body and returns the reply.
func (e *testEnv) cloneSession(id string, body map[string]any) (int, map[string]any) {
	e.t.Helper()
	resp, out := e.do("POST", "/api/sessions/"+id+"/clone", adminToken, body)
	if resp.StatusCode == http.StatusCreated {
		cid := out["id"].(string)
		e.t.Cleanup(func() {
			if d, ok := e.srv.registry.Get(cid); ok {
				_ = d.Stop(e.t.Context())
			}
		})
	}
	return resp.StatusCode, out
}

// mustClone is cloneSession, requiring a 201.
func (e *testEnv) mustClone(id string, body map[string]any) map[string]any {
	e.t.Helper()
	code, out := e.cloneSession(id, body)
	if code != http.StatusCreated {
		e.t.Fatalf("clone of %s: %d %v", id, code, out)
	}
	return out
}

// argsScript prints its arguments one to a line between brackets, then waits.
var argsScript = []string{"/bin/sh", "-c", `for a in "$@"; do printf 'ARG[%s]\n' "$a"; done; printf 'DONE\n'; exec /bin/cat`, "sh"}

// A clone runs the source's agent in its directory with its arguments as
// recorded, one holding a space or a quote among them; what the request
// changes is changed, and an empty argument list launches with none.
func TestCloneCopiesTheAgentTheDirectoryAndTheArguments(t *testing.T) {
	e := newTestEnv(t, nil)
	body := agentBody("echo")
	body["command"] = argsScript
	body["allowArgs"] = true
	e.save(body)
	dir := filepath.Join(e.root, "repo")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--model", "opus", "it's two words", `say "hi"`}
	resp, src := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "echo", "name": "api-sweep", "cwd": dir, "args": args})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, src)
	}
	t.Cleanup(func() { e.stopAndWait(src["id"].(string)) })
	c := e.mustClone(src["id"].(string), map[string]any{"name": "api-sweep-2"})
	if c["agentId"] != "echo" || c["cwd"] != dir || c["name"] != "api-sweep-2" || c["crew"] != nil {
		t.Fatalf("clone: %v", c)
	}
	if got := commandOf(c); !slices.Equal(got, append(slices.Clone(argsScript), args...)) {
		t.Fatalf("clone's command %q", got)
	}
	if l := e.local(c["id"].(string)).Launched(); !slices.Equal(l.Args, args) || l.Cwd != dir || len(l.Env) != 0 {
		t.Fatalf("clone's launch %+v", l)
	}
	v := dialViewer(t, e, c["id"].(string), adminToken)
	v.hello(80, 24)
	v.printedUntil("ARG[--model]\nARG[opus]\nARG[it's two words]\nARG[say \"hi\"]\nDONE")

	// What the request names is the clone's: another agent, directory and
	// arguments; an empty list is no arguments.
	c = e.mustClone(src["id"].(string), map[string]any{"name": "other", "agentId": "sh", "cwd": e.root, "args": []string{"-i"}})
	if c["agentId"] != "sh" || c["cwd"] != e.root || !slices.Equal(commandOf(c), []string{"/bin/sh", "-i"}) {
		t.Fatalf("changed clone: %v", c)
	}
	c = e.mustClone(src["id"].(string), map[string]any{"name": "bare", "args": []string{}})
	if !slices.Equal(commandOf(c), argsScript) || c["cwd"] != dir {
		t.Fatalf("clone with no arguments: %v", c)
	}
}

// The source's yolo choice is the clone's, each way and whatever the
// server's default, sent as the choice; a request's choice is taken over it.
func TestCloneCopiesTheYoloChoice(t *testing.T) {
	for _, def := range []bool{false, true} {
		e := newTestEnv(t, func(c *config.Config) { c.Yolo = def })
		e.saveYoloAgent("bold", "", map[string]any{"args": []string{"--yes"}})
		for _, src := range []bool{false, true} {
			resp, info := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "bold", "yolo": src})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("launch: %d %v", resp.StatusCode, info)
			}
			id := info["id"].(string)
			t.Cleanup(func() { _ = e.local(id).Stop(t.Context()) })
			c := e.mustClone(id, map[string]any{"name": "copy-" + strings.Repeat("y", len(e.srv.registry.List()))})
			if (c["yolo"] == true) != src || slices.Contains(commandOf(c), "--yes") != src || e.local(c["id"].(string)).Launched().Yolo != src {
				t.Fatalf("default %v, source %v: clone %v", def, src, c)
			}
			c = e.mustClone(id, map[string]any{"name": "flip-" + strings.Repeat("y", len(e.srv.registry.List())), "yolo": !src})
			if (c["yolo"] == true) == src {
				t.Fatalf("default %v, source %v: flipped clone %v", def, src, c)
			}
		}
	}
}

// A clone's name is cleaned as a rename's (trimmed, its control characters
// dropped, cut to 120 bytes at a character) and required.
func TestCloneNameIsTrimmedCutAndRequired(t *testing.T) {
	e := newTestEnv(t, nil)
	src := e.launch("cat", nil)["id"].(string)
	if c := e.mustClone(src, map[string]any{"name": "  twin \t"}); c["name"] != "twin" {
		t.Fatalf("trimmed: %v", c["name"])
	}
	long := strings.Repeat("é", 100)
	if c := e.mustClone(src, map[string]any{"name": long}); c["name"] != strings.Repeat("é", 60) {
		t.Fatalf("cut: %q", c["name"])
	}
	for _, name := range []string{"", "   ", "\x01\x02", "\t\n"} {
		if code, out := e.cloneSession(src, map[string]any{"name": name}); code != http.StatusBadRequest || errorCode(out) != "invalid_name" {
			t.Fatalf("%q: %d %v", name, code, out)
		}
	}
	if code, out := e.cloneSession(src, nil); code != http.StatusBadRequest {
		t.Fatalf("no body: %d %v", code, out)
	}
	if code, out := e.cloneSession(src, map[string]any{"name": "x", "env": map[string]string{"A": "b"}}); code != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("env: %d %v", code, out)
	}
}

// A body past the route's bound (maxBody, 64 KiB, as docs/protocol.md says)
// is refused 400 invalid_request, and nothing is made: the bound is the
// decoder's, before the name or the source is looked at. One just under it
// is read.
func TestCloneBodyIsBounded(t *testing.T) {
	e := newTestEnv(t, nil)
	src := e.launch("cat", nil)["id"].(string)
	// The JSON's own bytes around the padding: {"name":"…","args":["…"]}.
	pad := func(name string, total int) map[string]any {
		frame := len(`{"name":"` + name + `","args":[""]}`)
		return map[string]any{"name": name, "args": []string{strings.Repeat("a", total-frame)}}
	}
	if code, out := e.cloneSession(src, pad("over-the-bound", maxBody+1)); code != http.StatusBadRequest || errorCode(out) != "invalid_request" {
		t.Fatalf("a body of maxBody+1 bytes: %d %v", code, out)
	}
	for _, info := range e.srv.registry.List() {
		if info.Name == "over-the-bound" {
			t.Fatalf("a clone was made from a body past the bound: %+v", info)
		}
	}
	// At the bound the body is read: cat takes no arguments, so the
	// launch's own refusal answers, past the decoder.
	if code, out := e.cloneSession(src, pad("at-the-bound", maxBody)); code != http.StatusBadRequest || errorCode(out) != "args_not_allowed" {
		t.Fatalf("a body of maxBody bytes: %d %v", code, out)
	}
}

// A name a listed session has, ignoring case, running or ended, is refused
// 409 name_taken, and nothing is made.
func TestCloneRefusesANameInUse(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, src := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "name": "api-sweep"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, src)
	}
	id := src["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })
	resp, ended := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "exit", "name": "Gone Run"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, ended)
	}
	<-e.local(ended["id"].(string)).Ended()
	before := len(e.srv.registry.List())
	for _, name := range []string{"api-sweep", "API-Sweep", " api-sweep ", "gone run", "GONE RUN"} {
		if code, out := e.cloneSession(id, map[string]any{"name": name}); code != http.StatusConflict || errorCode(out) != "name_taken" {
			t.Fatalf("%q: %d %v", name, code, out)
		}
	}
	if n := len(e.srv.registry.List()); n != before {
		t.Fatalf("%d sessions listed after the refusals, %d before", n, before)
	}
	// The clone's own name is in use once it is made.
	e.mustClone(id, map[string]any{"name": "api-sweep-2"})
	if code, out := e.cloneSession(id, map[string]any{"name": "Api-Sweep-2"}); code != http.StatusConflict || errorCode(out) != "name_taken" {
		t.Fatalf("the clone's name again: %d %v", code, out)
	}
}

// Clones of one session fired together under one name make one session:
// the name is held from its check until the clone is registered, so the
// others are refused name_taken.
func TestClonesFiredTogetherWithOneNameMakeOneSession(t *testing.T) {
	e := newTestEnv(t, nil)
	src := e.launch("cat", nil)["id"].(string)
	const n = 8
	type reply struct {
		status int
		out    map[string]any
	}
	replies := make(chan reply, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			code, out := e.cloneSession(src, map[string]any{"name": "twin"})
			replies <- reply{code, out}
		})
	}
	wg.Wait()
	close(replies)
	created, refused := 0, 0
	for r := range replies {
		switch {
		case r.status == http.StatusCreated:
			created++
		case r.status == http.StatusConflict && errorCode(r.out) == "name_taken":
			refused++
		default:
			t.Fatalf("a clone answered %d %v", r.status, r.out)
		}
	}
	named := 0
	for _, info := range e.srv.registry.List() {
		if info.Name == "twin" {
			named++
		}
	}
	if created != 1 || refused != n-1 || named != 1 {
		t.Fatalf("%d made, %d refused, %d named twin, of %d clones at once", created, refused, named, n)
	}
	// Each name is let go once its clone is made or refused.
	if len(e.srv.cloneNames.held) != 0 {
		t.Fatalf("names still held: %q", e.srv.cloneNames.held)
	}
}

// A hosted session is not cloned (Conductor starts no session on a host),
// and its name is one in use.
func TestCloneRefusesAHostedSession(t *testing.T) {
	e := newTestEnv(t, nil)
	host := dialFakeHost(t, e, "hosted-agent-token")
	code, out := e.cloneSession(host.sessionID, map[string]any{"name": "copy"})
	if code != http.StatusBadRequest || errorCode(out) != "hosted_session" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "laptop") {
		t.Fatalf("hosted: %d %v", code, out)
	}
	src := e.launch("cat", nil)["id"].(string)
	if code, out := e.cloneSession(src, map[string]any{"name": "Hosted"}); code != http.StatusConflict || errorCode(out) != "name_taken" {
		t.Fatalf("a hosted session's name: %d %v", code, out)
	}
	if code, out := e.cloneSession("nope", map[string]any{"name": "copy"}); code != http.StatusNotFound {
		t.Fatalf("unknown: %d %v", code, out)
	}
}

// The route needs the workbench token: none, and a share link's, are
// refused.
func TestCloneNeedsTheWorkbenchToken(t *testing.T) {
	e := newTestEnv(t, nil)
	src := e.launch("cat", nil)["id"].(string)
	_, link := e.do("POST", "/api/sessions/"+src+"/links", adminToken, map[string]any{"role": "control"})
	for _, tok := range []string{"", link["token"].(string), "test-host-token"} {
		resp, out := e.do("POST", "/api/sessions/"+src+"/clone", tok, map[string]any{"name": "copy"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: %d %v", tok, resp.StatusCode, out)
		}
	}
	if n := len(e.srv.registry.List()); n != 1 {
		t.Fatalf("%d sessions", n)
	}
}

// An agent gone from the catalog is refused invalid_agent, and a directory
// gone, or outside the allowed roots, invalid_cwd, with no default
// directory instead.
func TestCloneRefusesAGoneAgentAndAGoneDirectory(t *testing.T) {
	e := newTestEnv(t, nil)
	e.save(agentBody("temp"))
	src := e.launch("temp", nil)["id"].(string)
	if code := e.del("temp"); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code, out := e.cloneSession(src, map[string]any{"name": "copy"}); code != http.StatusBadRequest || errorCode(out) != "invalid_agent" {
		t.Fatalf("gone agent: %d %v", code, out)
	}

	dir := filepath.Join(e.root, "gone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp, info := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": dir})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, info)
	}
	id := info["id"].(string)
	e.stopAndWait(id)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	before := len(e.srv.registry.List())
	if code, out := e.cloneSession(id, map[string]any{"name": "copy"}); code != http.StatusBadRequest || errorCode(out) != "invalid_cwd" {
		t.Fatalf("gone directory: %d %v", code, out)
	}
	if code, out := e.cloneSession(id, map[string]any{"name": "copy", "cwd": "/"}); code != http.StatusBadRequest || errorCode(out) != "invalid_cwd" {
		t.Fatalf("outside the roots: %d %v", code, out)
	}
	if n := len(e.srv.registry.List()); n != before {
		t.Fatalf("%d sessions after the refusals, %d before", n, before)
	}
	// The name a refused clone asked for is free again.
	if code, out := e.cloneSession(id, map[string]any{"name": "copy", "cwd": e.root}); code != http.StatusCreated {
		t.Fatalf("after: %d %v", code, out)
	}
}

// envScript prints what a run gives its members' processes, then waits.
var envScript = []string{"/bin/sh", "-c", `printf 'GOAL[%s] CREW[%s] RUN[%s] MEMBER[%s] TASK[%s]\n' "$GOAL" "$CONDUCTOR_CREW" "$CONDUCTOR_RUN" "$CONDUCTOR_MEMBER" "$CONDUCTOR_TASK"; exec /bin/cat`}

// A run member's clone is a loose session in the member's directory: no
// crew of its own, none of the member's variables, no run chat; the member
// is left as it was.
func TestAMembersCloneIsLoose(t *testing.T) {
	e := newTestEnv(t, nil)
	body := agentBody("envy")
	body["command"] = envScript
	e.save(body)
	runID := e.launchCrew(t, "Env", map[string]any{"name": "a", "agentId": "envy", "prompt": "", "start": map[string]any{"when": "immediately"}})
	sid := e.waitRunning(t, runID, "a")
	t.Cleanup(func() { e.stopAndWait(sid) })
	src := e.local(sid)
	sv := dialViewer(t, e, sid, adminToken)
	sv.hello(80, 24)
	sv.expectOutput("GOAL[ship] CREW[")
	c := e.mustClone(sid, map[string]any{"name": "a-alone"})
	if c["crew"] != nil || c["cwd"] != src.Info().Cwd {
		t.Fatalf("clone: %v", c)
	}
	clone := e.local(c["id"].(string))
	if len(clone.Launched().Env) != 0 || clone.RunChat() != nil {
		t.Fatalf("the clone has the member's run: %+v", clone.Launched())
	}
	v := dialViewer(t, e, c["id"].(string), adminToken)
	v.hello(80, 24)
	v.expectOutput("GOAL[] CREW[] RUN[] MEMBER[] TASK[]")
	if got := e.waitRunning(t, runID, "a"); got != sid {
		t.Fatalf("the member's session is %s, want %s", got, sid)
	}
}

// The clone's agent conversation is its own: a recipe that names it at
// launch names it afresh, never with the source's id or its resume.
func TestACloneStartsANewConversation(t *testing.T) {
	e := newTestEnv(t, nil)
	e.saveResumable("talker")
	src := e.launch("talker", []string{"--verbose"})
	id := src["id"].(string)
	pid := src["agentSession"].(map[string]any)["id"].(string)
	// A turn: the source's conversation is resumable.
	e.do("POST", "/api/sessions/"+id+"/attention", e.agentToken(id), map[string]any{"state": "done", "agentSession": pid, "turn": true})
	c := e.mustClone(id, map[string]any{"name": "talker-2"})
	as, _ := c["agentSession"].(map[string]any)
	if as == nil || as["id"] == pid || !uuidRe.MatchString(as["id"].(string)) || as["source"] != "set" || as["resumable"] != false {
		t.Fatalf("clone's agent session %v, source's %s", as, pid)
	}
	if got := commandOf(c); !slices.Equal(got[4:], []string{"--session-id", as["id"].(string), "--verbose"}) || slices.Contains(got, pid) {
		t.Fatalf("clone's command %q", got)
	}
}

// Nothing of the source changes: its process, name, links, viewers and
// chat.
func TestACloneLeavesTheSourceAsItWas(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, src := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "name": "keep"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, src)
	}
	id := src["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })
	l := e.local(id)
	e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "qa"})
	v := dialViewer(t, e, id, adminToken)
	v.hello(80, 24)
	v.expectControl(proto.CtlWelcome)
	before := l.Info()
	_, linksBefore := e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil)
	chatBefore := len(l.ChatHistory())
	e.mustClone(id, map[string]any{"name": "copy"})
	after := l.Info()
	if after.Name != "keep" || after.Status != session.StatusRunning || after.Viewers != before.Viewers || before.Viewers != 1 ||
		!slices.Equal(after.Command, before.Command) || l.Launched().Name != "keep" || len(l.ChatHistory()) != chatBefore {
		t.Fatalf("source before %+v, after %+v", before, after)
	}
	if _, links := e.do("GET", "/api/sessions/"+id+"/links", adminToken, nil); len(links["links"].([]any)) != len(linksBefore["links"].([]any)) || len(links["links"].([]any)) != 1 {
		t.Fatalf("links before %v, after %v", linksBefore, links)
	}
	// The viewer is still attached and still reads the source.
	v.send(proto.Encode(proto.TypeInput, []byte("still here\n")))
	v.expectOutput("still here")
}

// GET /api/sessions/{id} gives an admin a server session's launch, never
// its environment; a share link's holder and a hosted session get none.
func TestTheLaunchGoesToAnAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	body := agentBody("envy")
	body["command"] = envScript
	e.save(body)
	runID := e.launchCrew(t, "Env", map[string]any{"name": "a", "agentId": "envy", "prompt": "", "args": []string{}, "start": map[string]any{"when": "immediately"}})
	sid := e.waitRunning(t, runID, "a")
	t.Cleanup(func() { e.stopAndWait(sid) })
	// An agent that waits whatever its arguments: a session that ended would take its links with it.
	echo := agentBody("echo")
	echo["command"] = argsScript
	echo["allowArgs"] = true
	e.save(echo)
	resp, src := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "echo", "args": []string{"-i", "it's"}, "yolo": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, src)
	}
	id := src["id"].(string)
	t.Cleanup(func() { e.stopAndWait(id) })

	_, out := e.do("GET", "/api/sessions/"+id, adminToken, nil)
	launch, _ := out["launch"].(map[string]any)
	// Its login by its id and source, never a folder or a fingerprint.
	if launch == nil || launch["agentId"] != "echo" || launch["cwd"] != e.root || launch["yolo"] != true || len(launch) != 5 {
		t.Fatalf("launch %v", out)
	}
	if acct, _ := launch["account"].(map[string]any); acct["id"] != "default" || acct["source"] != "own" || len(acct) != 2 {
		t.Fatalf("launch's account %v", launch["account"])
	}
	if args := launch["args"].([]any); len(args) != 2 || args[0] != "-i" || args[1] != "it's" {
		t.Fatalf("args %v", args)
	}
	// A member's: no env, though its launch had a GOAL.
	_, out = e.do("GET", "/api/sessions/"+sid, adminToken, nil)
	launch, _ = out["launch"].(map[string]any)
	if launch == nil || launch["env"] != nil || len(launch) != 5 || launch["args"] == nil {
		t.Fatalf("member's launch %v", out)
	}
	// A link's holder reads the session without it.
	resp, link := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link: %d %v", resp.StatusCode, link)
	}
	resp, out = e.do("GET", "/api/sessions/"+id, link["token"].(string), nil)
	if resp.StatusCode != http.StatusOK || out["session"] == nil || out["launch"] != nil {
		t.Fatalf("link: %d %v", resp.StatusCode, out)
	}
	// A hosted session has none.
	host := dialFakeHost(t, e, "hosted-agent-token")
	_, out = e.do("GET", "/api/sessions/"+host.sessionID, adminToken, nil)
	if out["session"] == nil || out["launch"] != nil {
		t.Fatalf("hosted: %v", out)
	}
}
