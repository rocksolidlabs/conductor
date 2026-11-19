package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// A lookup an attempt's agent asks a context provider (docs/plans/
// context-seams.md, CT-2, extlookup.go).

// looker is a context provider that answers lookups: what answer does
// (an answer of one item at the request's own start when nil), every
// request recorded, each call waiting at gate while it is set.
type looker struct {
	*fakeContext
	answer func(ctx context.Context, r ext.LookupRequest) (ext.LookupAnswer, error)
	gate   chan struct{}

	lmu  sync.Mutex
	reqs []ext.LookupRequest
}

func (l *looker) Lookup(ctx context.Context, r ext.LookupRequest) (ext.LookupAnswer, error) {
	l.lmu.Lock()
	l.reqs = append(l.reqs, r)
	gate := l.gate
	l.lmu.Unlock()
	if gate != nil {
		<-gate
	}
	if l.answer != nil {
		return l.answer(ctx, r)
	}
	return ext.LookupAnswer{Items: []ext.LookupItem{{Kind: ext.ItemSymbol, Path: "build.go", Line: 3, Symbol: r.Symbol, Relation: ext.RelationCalledBy, Mark: ext.MarkSupported, Why: "main calls it"}},
		Coverage: ext.LookupCoverage{Commit: r.Start, Files: 2}}, nil
}

func (l *looker) asked() []ext.LookupRequest {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	return slices.Clone(l.reqs)
}

// lookupAttempt is a server with the context providers ps, a repository,
// and a managed run whose task build's first attempt runs: its run's id,
// the attempt and its session's agent token.
func lookupAttempt(t *testing.T, ps ...ext.ContextProvider) (*testEnv, string, map[string]any, string) {
	t.Helper()
	e := contextEnv(t, ps...)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopEveryRun(e.srv) })
	a := runningAttempt(t, e, run.ID, "build", 1)
	return e, run.ID, a, e.agentToken(a["sessionId"].(string))
}

// lookupPath is the lookup route of the session sid.
func lookupPath(sid string) string { return "/api/sessions/" + sid + "/context/lookup" }

// The route answers only where a provider answers lookups: with none
// registered, with one that answers no lookups, and naming a provider
// that does not, 404 no_context_lookup, and nothing is asked.
func TestLookupRefusedWithoutProvider(t *testing.T) {
	e, _, a, tok := lookupAttempt(t)
	sid := a["sessionId"].(string)
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusNotFound, "no_context_lookup")

	plain := &fakeContext{name: "graph"}
	e2, _, a2, tok2 := lookupAttempt(t, plain)
	e2.expectAs(t, tok2, "POST", lookupPath(a2["sessionId"].(string)), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusNotFound, "no_context_lookup")

	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e3, _, a3, tok3 := lookupAttempt(t, &fakeContext{name: "notes"}, l)
	sid3 := a3["sessionId"].(string)
	e3.expectAs(t, tok3, "POST", lookupPath(sid3), map[string]any{"op": "symbol", "symbol": "Build", "source": "notes"}, http.StatusNotFound, "no_context_lookup")
	e3.expectAs(t, tok3, "POST", lookupPath(sid3), map[string]any{"op": "symbol", "symbol": "Build", "source": "elsewhere"}, http.StatusNotFound, "no_context_lookup")
	if n := len(l.asked()); n != 0 {
		t.Fatalf("a refused lookup asked the provider %d times", n)
	}
	// Unnamed, the first that answers lookups is asked.
	out := e3.expectAs(t, tok3, "POST", lookupPath(sid3), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusOK, "")
	if out["source"] != "graph" || len(l.asked()) != 1 {
		t.Fatalf("the lookup: %v, asked %d", out, len(l.asked()))
	}
}

// Only the attempt's own agent asks: the workbench token is 403
// agent_only, another session's token 401, a session that is no attempt
// 404 not_an_attempt; nothing reaches the provider.
func TestLookupRefusedToTheAdmin(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, runID, a, _ := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	body := map[string]any{"op": "symbol", "symbol": "Build"}
	e.expectAs(t, adminToken, "POST", lookupPath(sid), body, http.StatusForbidden, "agent_only")
	other := e.createSession("cat")
	e.expectAs(t, e.agentToken(other), "POST", lookupPath(sid), body, http.StatusUnauthorized, "unauthorized")
	e.expectAs(t, e.agentToken(other), "POST", lookupPath(other), body, http.StatusNotFound, "not_an_attempt")
	e.expectAs(t, "", "POST", lookupPath(sid), body, http.StatusUnauthorized, "unauthorized")
	if n := len(l.asked()); n != 0 {
		t.Fatalf("the provider was asked %d times", n)
	}
	_ = runID
}

// A comparison's lane asks no context: its own token on its own session
// is 409 lane, as on every door closed to a lane.
func TestLookupRefusedToALane(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	status, raw, out := ce.callAs(t, ce.bTok, "POST", lookupPath(ce.b), map[string]any{"op": "symbol", "symbol": "Build"})
	laneRefused(t, "context lookup", status, raw, out)
}

// The server fills in which attempt the question is about, never the
// body: a body naming the run, task, attempt, session or a commit is
// refused as an unknown field; the provider is asked with the session's
// own run, task, attempt, kind, session, start, the run's base and the
// worktree's HEAD, and the agent's words as given.
func TestLookupFillsTheAttemptNotTheBody(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, runID, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	for _, field := range []string{"run", "task", "attempt", "kind", "session", "start", "base", "head", "worktree"} {
		e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build", field: "x"}, http.StatusBadRequest, "invalid_request")
	}
	if n := len(l.asked()); n != 0 {
		t.Fatalf("a refused body asked the provider %d times", n)
	}
	base := gitIn(t, e.root, "rev-parse", "HEAD")
	out := e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "references", "symbol": "Build", "path": "build.go", "query": "who calls it", "limit": 7}, http.StatusOK, "")
	asked := l.asked()
	want := ext.LookupRequest{Op: "references", Query: "who calls it", Path: "build.go", Symbol: "Build", Limit: 7,
		Run: runID, Task: "build", Attempt: 1, Kind: "work", Session: sid, Start: a["start"].(string), Base: base, Head: a["start"].(string)}
	if len(asked) != 1 || asked[0] != want {
		t.Fatalf("the provider was asked\n%+v\nwant\n%+v", asked, want)
	}
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["mark"] != "supported" || out["source"] != "graph" || out["coverage"].(map[string]any)["commit"] != a["start"] {
		t.Fatalf("the answer: %v", out)
	}
	// Without a limit, 20.
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusOK, "")
	if got := l.asked(); got[len(got)-1].Limit != 20 {
		t.Fatalf("the default limit: %d", got[len(got)-1].Limit)
	}
}

// The head is the worktree's own, read at each lookup: a commit the agent
// makes is the next lookup's head, the start unchanged.
func TestLookupFillsHeadFromTheWorktree(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, _, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusOK, "")
	next := commitFile(t, a["worktree"].(string), "built.txt", "yes\n")
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "impact", "path": "built.txt"}, http.StatusOK, "")
	asked := l.asked()
	if len(asked) != 2 || asked[0].Head != a["start"] || asked[1].Head != next || asked[1].Start != a["start"] {
		t.Fatalf("the heads: %+v", asked)
	}
	if got := worktreeHead(t.Context(), t.TempDir()); got != "" {
		t.Fatalf("a folder with no repository: %q", got)
	}
}

// The agent's words are held to their bounds before anything is asked:
// an op that is none, each field one past its bound, a path outside the
// tree or of pathspec magic, a limit of 0 or 201, a body past 4 KiB.
func TestLookupBodyBounded(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, _, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	for what, body := range map[string]map[string]any{
		"an op that is none":   {"op": "grep"},
		"no op":                {"symbol": "Build"},
		"a query of 201 bytes": {"op": "symbol", "query": strings.Repeat("q", ext.MaxLookupQuery+1)},
		"a query of two lines": {"op": "symbol", "query": "a\nb"},
		"a symbol past 512":    {"op": "symbol", "symbol": strings.Repeat("s", ext.MaxLookupSymbol+1)},
		"a path past 1024":     {"op": "impact", "path": strings.Repeat("p", ext.MaxLookupPath+1)},
		"a path outside":       {"op": "impact", "path": "../secret"},
		"pathspec magic":       {"op": "impact", "path": ":(top)secret"},
		"a limit of 0":         {"op": "symbol", "limit": 0},
		"a limit of 201":       {"op": "symbol", "limit": ext.MaxLookupLimit + 1},
		"a body past 4 KiB":    {"op": "symbol", "source": strings.Repeat("s", maxLookupBody)},
	} {
		status, raw, out := e.callAs(t, tok, "POST", lookupPath(sid), body)
		if status != http.StatusBadRequest || errorCode(out) != "invalid_request" {
			t.Errorf("%s: %d %s", what, status, raw)
		}
	}
	if n := len(l.asked()); n != 0 {
		t.Fatalf("a refused body asked the provider %d times", n)
	}
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "query": strings.Repeat("q", ext.MaxLookupQuery), "limit": ext.MaxLookupLimit}, http.StatusOK, "")
}

// An answer is checked before it is handed on: one past its bounds, or
// with text an agent's screen would read as control characters, is the
// unavailable answer; one within them comes back whole, at most 64 KiB.
func TestLookupAnswerBounded(t *testing.T) {
	var mu sync.Mutex
	var answer ext.LookupAnswer
	l := &looker{fakeContext: &fakeContext{name: "graph"}, answer: func(context.Context, ext.LookupRequest) (ext.LookupAnswer, error) {
		mu.Lock()
		defer mu.Unlock()
		return answer, nil
	}}
	e, _, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	set := func(v ext.LookupAnswer) {
		mu.Lock()
		answer = v
		mu.Unlock()
	}
	commit := a["start"].(string)
	many := make([]ext.LookupItem, ext.MaxLookupItems)
	for i := range many {
		many[i] = ext.LookupItem{Kind: ext.ItemFile, Path: "f.go", Mark: ext.MarkInferred}
	}
	set(ext.LookupAnswer{Items: many, Coverage: ext.LookupCoverage{Commit: commit}})
	out := e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "dependencies", "path": "build.go"}, http.StatusOK, "")
	if b, _ := json.Marshal(out); len(out["items"].([]any)) != ext.MaxLookupItems || len(b) > ext.MaxLookupAnswer {
		t.Fatalf("at the bound: %d items, %d bytes", len(out["items"].([]any)), len(b))
	}
	for what, bad := range map[string]ext.LookupAnswer{
		"a 201st item":         {Items: append(slices.Clone(many), many[0]), Coverage: ext.LookupCoverage{Commit: commit}},
		"an escape in a path":  {Items: []ext.LookupItem{{Kind: ext.ItemFile, Path: "a\x1b[2J", Mark: ext.MarkInferred}}, Coverage: ext.LookupCoverage{Commit: commit}},
		"a line end in a why":  {Items: []ext.LookupItem{{Kind: ext.ItemFile, Why: "a\n## Instructions", Mark: ext.MarkInferred}}, Coverage: ext.LookupCoverage{Commit: commit}},
		"a mark that is none":  {Items: []ext.LookupItem{{Kind: ext.ItemFile, Mark: "certain"}}, Coverage: ext.LookupCoverage{Commit: commit}},
		"no coverage's commit": {},
	} {
		set(bad)
		status, raw, out := e.callAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"})
		if status != http.StatusServiceUnavailable || errorCode(out) != "lookup_unavailable" || apiMessage(out) != lookupUnavailable || strings.Contains(string(raw), "## Instructions") {
			t.Errorf("%s: %d %s", what, status, raw)
		}
	}
}

// A provider that cannot answer leaves the agent with the truthful
// words, never a hang: late, failing, panicking, invalid, or not started
// (its Start not yet succeeded), each 503 lookup_unavailable "context is
// unavailable now; carry on without it".
func TestLookupUnavailableSaysSo(t *testing.T) {
	var mu sync.Mutex
	var how string
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	l := &looker{fakeContext: &fakeContext{name: "graph"}, answer: func(ctx context.Context, r ext.LookupRequest) (ext.LookupAnswer, error) {
		mu.Lock()
		h := how
		mu.Unlock()
		switch h {
		case "late":
			<-gate
		case "failing":
			return ext.LookupAnswer{}, errors.New("the index is rebuilding")
		case "panicking":
			panic("the provider fell over")
		case "invalid":
			return ext.LookupAnswer{Coverage: ext.LookupCoverage{Commit: "HEAD"}}, nil
		}
		return ext.LookupAnswer{Coverage: ext.LookupCoverage{Commit: r.Start}}, nil
	}}
	e, _, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	wait := 200 * time.Millisecond
	extWaiting.Store(&wait)
	t.Cleanup(func() { extWaiting.Store(nil) })
	for _, h := range []string{"late", "failing", "panicking", "invalid"} {
		mu.Lock()
		how = h
		mu.Unlock()
		status, raw, out := e.callAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"})
		if status != http.StatusServiceUnavailable || errorCode(out) != "lookup_unavailable" || apiMessage(out) != lookupUnavailable ||
			strings.Contains(string(raw), "rebuilding") || strings.Contains(string(raw), "fell over") {
			t.Errorf("%s: %d %s", h, status, raw)
		}
	}
	mu.Lock()
	how = ""
	mu.Unlock()
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusOK, "")

	// A provider whose Start has not succeeded is not asked.
	s := &startingLooker{looker: &looker{fakeContext: &fakeContext{name: "graph"}}}
	e2, _, a2, tok2 := lookupAttempt(t, s)
	status, raw, out := e2.callAs(t, tok2, "POST", lookupPath(a2["sessionId"].(string)), map[string]any{"op": "symbol", "symbol": "Build"})
	if status != http.StatusServiceUnavailable || errorCode(out) != "lookup_unavailable" || apiMessage(out) != lookupUnavailable || len(s.asked()) != 0 {
		t.Fatalf("not started: %d %s, asked %d", status, raw, len(s.asked()))
	}
}

// startingLooker is a looker that needs the server (an ext.Starter) and
// is never started here: its slots answer the free default.
type startingLooker struct{ *looker }

func (startingLooker) Start(context.Context, ext.Host) error { return errors.New("not yet") }

// A session's lookups in flight are counted until the provider's call
// returns: two held in the provider past its wait are answered 503, and
// a third is 429 lookup_busy while they are held, whatever the route
// answered them; once one returns, the session asks again.
func TestLookupBusyPastTwo(t *testing.T) {
	gate := make(chan struct{})
	l := &looker{fakeContext: &fakeContext{name: "graph"}, gate: gate}
	e, _, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	wait := 100 * time.Millisecond
	extWaiting.Store(&wait)
	t.Cleanup(func() { extWaiting.Store(nil) })
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate)
		}
	})
	for i := range 2 {
		status, raw, out := e.callAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"})
		if status != http.StatusServiceUnavailable || errorCode(out) != "lookup_unavailable" {
			t.Fatalf("held lookup %d: %d %s", i, status, raw)
		}
	}
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusTooManyRequests, "lookup_busy")
	if n := len(l.asked()); n != 2 {
		t.Fatalf("the provider was asked %d times", n)
	}
	// The held calls return; the session's count drops once each has.
	l.lmu.Lock()
	l.gate = nil
	l.lmu.Unlock()
	close(gate)
	released = true
	waitUntil(t, "the held lookups returned", func() bool {
		e.srv.contextLookups.mu.Lock()
		defer e.srv.contextLookups.mu.Unlock()
		return e.srv.contextLookups.n[sid] == 0
	})
	extWaiting.Store(nil)
	e.expectAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusOK, "")
}

// Lookups have slots of their own: with every lookup slot of the provider
// held, a lookup is 429 lookup_busy, and an attempt that starts then still
// has the provider's blocks in its task file.
func TestLookupSlotsLeaveTheStartAlone(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph", answer: blocksOf(ext.ContextBlock{Title: "Callers of Build", Body: "main calls Build."})}}
	e, runID, a, tok := lookupAttempt(t, l)
	var held []extSlot
	for range extInFlight {
		slot, ok := e.srv.extTake(extKindLookup, "graph")
		if !ok {
			t.Fatal("a lookup slot was not free")
		}
		held = append(held, slot)
	}
	t.Cleanup(func() {
		for _, sl := range held {
			sl.free()
		}
	})
	e.expectAs(t, tok, "POST", lookupPath(a["sessionId"].(string)), map[string]any{"op": "symbol", "symbol": "Build"}, http.StatusTooManyRequests, "lookup_busy")
	// The next task's attempt starts with its blocks.
	commitFile(t, a["worktree"].(string), "built.txt", "yes\n")
	if resp, out := e.do("POST", "/api/sessions/"+a["sessionId"].(string)+"/task/result", tok, map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	_, f, _ := attemptTaskFiles(t, e, runID, "docs")
	if len(f.Context) != 1 || f.Context[0].Title != "Callers of Build" {
		t.Fatalf("docs' context: %+v", f.Context)
	}
}

// A pack's blocks are not handed to an agent until they can be recorded
// against its attempt: the answer carries none.
func TestLookupHandsNoBlocksYet(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}, answer: func(_ context.Context, r ext.LookupRequest) (ext.LookupAnswer, error) {
		return ext.LookupAnswer{Blocks: []ext.ContextBlock{{Title: "The pack", Body: "a block of the pack", Pack: "p-1"}}, Coverage: ext.LookupCoverage{Commit: r.Start}}, nil
	}}
	e, _, a, tok := lookupAttempt(t, l)
	_, raw, out := e.callAs(t, tok, "POST", lookupPath(a["sessionId"].(string)), map[string]any{"op": "pack"})
	if _, ok := out["blocks"]; ok || strings.Contains(string(raw), "a block of the pack") || out["source"] != "graph" {
		t.Fatalf("the pack's answer: %s", raw)
	}
}

// The guide lists context_lookup to an attempt only where a registered
// provider answers lookups: not with none, not with one that answers
// none; and never to the admin, whose token the route refuses.
func TestToolListedOnlyWithProvider(t *testing.T) {
	listed := func(e *testEnv, sid, tok string) bool {
		ov := entityOf(t, e.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide", nil, http.StatusOK, ""), "overview")
		for _, v := range ov["may"].([]any) {
			if v == "context_lookup" {
				return true
			}
		}
		return false
	}
	e, _, a, tok := lookupAttempt(t)
	if listed(e, a["sessionId"].(string), tok) {
		t.Fatal("listed with no provider")
	}
	e2, _, a2, tok2 := lookupAttempt(t, &fakeContext{name: "graph"})
	if listed(e2, a2["sessionId"].(string), tok2) {
		t.Fatal("listed with a provider that answers no lookups")
	}
	e3, _, a3, tok3 := lookupAttempt(t, &looker{fakeContext: &fakeContext{name: "graph"}})
	sid3 := a3["sessionId"].(string)
	if !listed(e3, sid3, tok3) {
		t.Fatal("not listed with a provider that answers lookups")
	}
	if listed(e3, e3.createSession("cat"), adminToken) {
		t.Fatal("listed to a session that is no attempt")
	}
	// A provider not started yet is not listed either.
	e4, _, a4, tok4 := lookupAttempt(t, &startingLooker{looker: &looker{fakeContext: &fakeContext{name: "graph"}}})
	if listed(e4, a4["sessionId"].(string), tok4) {
		t.Fatal("listed with a provider not started")
	}
}

// The protocol's route table names the route.
func TestLookupRouteDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.Join(strings.Fields(string(doc)), " ")
	if !strings.Contains(flat, "| `POST /api/sessions/{id}/context/lookup` | the session's agent token only |") || !strings.Contains(flat, lookupUnavailable) {
		t.Fatal("docs/protocol.md does not name the lookup route or its words")
	}
}

// An attempt that has closed asks nothing: once its result is in and the
// task has succeeded, its session's lookup is 409 not_running.
func TestLookupAfterTheAttemptClosed(t *testing.T) {
	l := &looker{fakeContext: &fakeContext{name: "graph"}}
	e, runID, a, tok := lookupAttempt(t, l)
	sid := a["sessionId"].(string)
	commitFile(t, a["worktree"].(string), "built.txt", "yes\n")
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", tok, map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	taskVia(t, e, runID, "build", "succeeded", func(task map[string]any) bool { return task["state"] == "succeeded" })
	status, raw, out := e.callAs(t, tok, "POST", lookupPath(sid), map[string]any{"op": "symbol", "symbol": "Build"})
	if status != http.StatusConflict || errorCode(out) != "not_running" {
		t.Fatalf("a closed attempt's lookup: %d %s", status, raw)
	}
	if n := len(l.asked()); n != 0 {
		t.Fatalf("the provider was asked %d times", n)
	}
}
