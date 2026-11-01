package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// fakeContext is a context provider: Context answers what answer does
// (no block when nil), and every touch it is told goes to touches (when
// set), after gate (when set) lets it.
type fakeContext struct {
	name    string
	answer  func(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error)
	touches chan ext.TouchedFile
	gate    chan struct{}

	mu    sync.Mutex
	asked []ext.ContextQuery
}

func (f *fakeContext) Name() string { return f.name }

func (f *fakeContext) NoteTouch(_ context.Context, t ext.TouchedFile) {
	if f.gate != nil {
		<-f.gate
	}
	if f.touches != nil {
		f.touches <- t
	}
}

func (f *fakeContext) Context(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	f.mu.Lock()
	f.asked = append(f.asked, q)
	f.mu.Unlock()
	if f.answer == nil {
		return nil, nil
	}
	return f.answer(ctx, q)
}

// blocksOf is an answer of bs.
func blocksOf(bs ...ext.ContextBlock) func(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	return func(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) { return bs, nil }
}

// withContext is the option of a server whose context providers are ps,
// registered under their names in that order, every other slot its free
// default.
func withContext(ps ...ext.ContextProvider) Option {
	p := ext.Providers{}
	for _, c := range ps {
		p.Context = append(p.Context, c)
		p.All = append(p.All, ext.Entry{Kind: ext.KindContext, Name: c.Name(), Value: c})
	}
	return WithExtensions(local.Fill(p))
}

// contextEnv is a server over a data directory with the managed runs'
// ready cat and the context providers ps.
func contextEnv(t *testing.T, ps ...ext.ContextProvider) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}}, withContext(ps...))
}

// attemptTaskFiles is the task.md and task.json of the run's task's first
// attempt, once it runs.
func attemptTaskFiles(t *testing.T, e *testEnv, runID, taskID string) (string, crew.TaskFile, map[string]any) {
	t.Helper()
	a := runningAttempt(t, e, runID, taskID, 1)
	tree := a["worktree"].(string)
	md, err := os.ReadFile(filepath.Join(tree, ".conductor", "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(tree, ".conductor", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f crew.TaskFile
	if err := json.Unmarshal(js, &f); err != nil {
		t.Fatal(err)
	}
	return string(md), f, a
}

// The context providers' blocks reach an attempt's task files, each named
// by its provider, in registration order, every provider asked about the
// attempt it is for.
func TestContextInTaskFile(t *testing.T) {
	graph := &fakeContext{name: "graph", answer: blocksOf(
		ext.ContextBlock{Title: "Callers of Build", Ref: "build.go", Body: "Build is called from main.go."},
		ext.ContextBlock{Title: "Recent changes", Body: "build.go changed twice this week."})}
	notes := &fakeContext{name: "notes", answer: blocksOf(ext.ContextBlock{Title: "Team notes", Ref: "https://example.test/notes/1", Body: "Keep Build pure."})}
	e := contextEnv(t, graph, notes)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	md, f, a := attemptTaskFiles(t, e, run.ID, "build")
	want := []crew.TaskContext{
		{Source: "graph", Title: "Callers of Build", Ref: "build.go", Body: "Build is called from main.go."},
		{Source: "graph", Title: "Recent changes", Body: "build.go changed twice this week."},
		{Source: "notes", Title: "Team notes", Ref: "https://example.test/notes/1", Body: "Keep Build pure."},
	}
	if !reflect.DeepEqual(f.Context, want) {
		t.Fatalf("task.json's context: %+v", f.Context)
	}
	for _, s := range []string{"## Context\n", "### Callers of Build (from graph)\n\nAbout: build.go\n\n```\nBuild is called from main.go.\n```", "### Team notes (from notes)\n\nAbout: https://example.test/notes/1\n"} {
		if !strings.Contains(md, s) {
			t.Fatalf("task.md lacks %q:\n%s", s, md)
		}
	}
	for _, p := range []*fakeContext{graph, notes} {
		p.mu.Lock()
		asked := append([]ext.ContextQuery(nil), p.asked...)
		p.mu.Unlock()
		tree := a["worktree"].(string)
		if len(asked) != 1 || asked[0].Run != run.ID || asked[0].Crew != "routes" || asked[0].Task != "build" || asked[0].Attempt != 1 || asked[0].Kind != crew.TaskWork ||
			asked[0].Goal != "build it" || asked[0].Instructions != "Build it." || asked[0].Worktree != tree || asked[0].Dir != tree || asked[0].Start != a["start"] {
			t.Fatalf("%s was asked %+v", p.name, asked)
		}
	}
	// Nothing of a block is in the run as the server reports it: the
	// blocks are the task files' alone.
	resp, out := e.do("GET", "/api/runs/"+run.ID, adminToken, nil)
	if b, _ := json.Marshal(out); resp.StatusCode != http.StatusOK || strings.Contains(string(b), "Keep Build pure") {
		t.Fatalf("the run: %d %s", resp.StatusCode, b)
	}
	stopRunVia(t, e, run.ID)
}

// stopRunVia stops the run through its route.
func stopRunVia(t *testing.T, e *testEnv, runID string) {
	t.Helper()
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
}

// What the providers answer is bounded: an answer with a block past its
// bound is none of that provider's, a ninth block is never read, and past
// ext.MaxTaskContext together the last blocks are left out until the rest
// fit, the run's log saying how many and whose, in notes none of which is
// cut, however many providers there are and however long their names.
func TestContextBounds(t *testing.T) {
	block := func(title string, n int) ext.ContextBlock {
		return ext.ContextBlock{Title: title, Body: strings.Repeat("b", n)}
	}
	var nine []ext.ContextBlock
	for i := range 9 {
		nine = append(nine, block("nine "+string(rune('a'+i)), 10))
	}
	bad := &fakeContext{name: "bad", answer: blocksOf(block("fine", 10), block(strings.Repeat("t", ext.MaxContextTitle+1), 10))}
	many := &fakeContext{name: "many", answer: blocksOf(nine...)}
	srv := newExtServer(t, nil, withContext(bad, many))
	got := srv.contextFor(t.Context(), ext.ContextQuery{Run: "no-such-run", Task: "build", Attempt: 1})
	if len(got) != ext.MaxContextBlocks {
		t.Fatalf("%d blocks: %+v", len(got), got)
	}
	for i, c := range got {
		if c.Source != "many" || c.Title != nine[i].Title {
			t.Fatalf("block %d: %+v", i, c)
		}
	}

	// Past MaxTaskContext: alpha's four blocks pass it alone, beta's one
	// is the last; beta's and alpha's last are left out, and the rest fit.
	alpha := &fakeContext{name: "alpha", answer: blocksOf(block("a1", 4100), block("a2", 4100), block("a3", 4100), block("a4", 4100))}
	beta := &fakeContext{name: "beta", answer: blocksOf(block("b1", 100))}
	e := contextEnv(t, alpha, beta)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	_, f, _ := attemptTaskFiles(t, e, run.ID, "build")
	size := 0
	var titles []string
	for _, c := range f.Context {
		size += crew.TaskContextBytes(c)
		titles = append(titles, c.Title)
	}
	if !reflect.DeepEqual(titles, []string{"a1", "a2", "a3"}) || size > ext.MaxTaskContext {
		t.Fatalf("kept %v, %d bytes", titles, size)
	}
	r, _ := e.srv.runs.Get(run.ID)
	note := "context for build #1: 2 blocks left out past 16 KiB together (alpha 1, beta 1)"
	if !slices.ContainsFunc(r.Log, func(en session.ActivityEntry) bool { return en.Message == note }) {
		var msgs []string
		for _, en := range r.Log {
			msgs = append(msgs, en.Message)
		}
		t.Fatalf("the run's log lacks %q: %v", note, msgs)
	}
	stopRunVia(t, e, run.ID)

	// The most providers, their names the longest, each a block of the
	// most: all but the first's are left out, and the log names every one
	// with its count, in notes none of which is cut.
	var ps []ext.ContextProvider
	var want []string
	for i := range ext.MaxPerList {
		name := fmt.Sprintf("p%02d", i) + strings.Repeat("x", ext.MaxProviderName-3)
		ps = append(ps, &fakeContext{name: name, answer: blocksOf(block("t", ext.MaxContextBody))})
		if i > 0 {
			want = append(want, name+" 1")
		}
	}
	e = contextEnv(t, ps...)
	e.gitRepo(t)
	run, release, err = e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	if _, f, _ = attemptTaskFiles(t, e, run.ID, "build"); len(f.Context) != 1 || f.Context[0].Source != ps[0].Name() {
		t.Fatalf("kept %+v", f.Context)
	}
	r, _ = e.srv.runs.Get(run.ID)
	var notes []string
	for _, en := range r.Log {
		if strings.HasPrefix(en.Message, "context for build #1:") {
			notes = append(notes, en.Message)
		}
	}
	head := fmt.Sprintf("context for build #1: %d blocks left out past 16 KiB together (", ext.MaxPerList-1)
	var named []string
	for i, m := range notes {
		prefix := "context for build #1: left out also ("
		if i == 0 {
			prefix = head
		}
		if len(m) > session.MaxAttentionMessage || !strings.HasPrefix(m, prefix) || !strings.HasSuffix(m, ")") {
			t.Fatalf("note %d of %d (%d bytes): %q", i, len(notes), len(m), m)
		}
		named = append(named, strings.Split(strings.TrimSuffix(strings.TrimPrefix(m, prefix), ")"), ", ")...)
	}
	if len(notes) < 2 || !reflect.DeepEqual(named, want) {
		t.Fatalf("the notes name %v, want %v: %q", named, want, notes)
	}
	stopRunVia(t, e, run.ID)
}

// A provider late, failing or panicking adds nothing: the attempt starts
// with no block once ext.ContextWait has ended the late one's call, even
// when that provider ignores its context and never returns, and a working
// provider's blocks are kept beside them.
func TestContextLateOrFailing(t *testing.T) {
	type seen struct {
		deadline time.Time
		ok       bool
		asked    time.Time
	}
	lateSaw := make(chan seen, 1)
	lateGate := make(chan struct{}) // closed as the test ends
	var lateReturned atomic.Bool
	late := &fakeContext{name: "late", answer: func(ctx context.Context, _ ext.ContextQuery) ([]ext.ContextBlock, error) {
		d, ok := ctx.Deadline()
		lateSaw <- seen{d, ok, time.Now()}
		<-lateGate // ignores its context: returns only once the test ends
		lateReturned.Store(true)
		return []ext.ContextBlock{{Title: "late", Body: "too late"}}, nil
	}}
	failing := &fakeContext{name: "failing", answer: func(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
		return []ext.ContextBlock{{Title: "x", Body: "y"}}, errors.New("the graph is down")
	}}
	panicking := &fakeContext{name: "panicking", answer: func(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) { panic("boom") }}
	fine := &fakeContext{name: "fine", answer: blocksOf(ext.ContextBlock{Title: "Kept", Body: "From a provider that answers."})}
	e := contextEnv(t, late, failing, panicking, fine)
	// Run before the server's cleanup (LIFO): the late call returns.
	t.Cleanup(func() { close(lateGate) })
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	s := <-lateSaw
	// The late provider's call is bounded by ContextWait, the guard's.
	if !s.ok || s.deadline.Sub(s.asked) > ext.ContextWait {
		t.Fatalf("the late call's deadline: %v (%v after it was asked)", s.ok, s.deadline.Sub(s.asked))
	}
	md, f, _ := attemptTaskFiles(t, e, run.ID, "build")
	if !reflect.DeepEqual(f.Context, []crew.TaskContext{{Source: "fine", Title: "Kept", Body: "From a provider that answers."}}) || strings.Contains(md, "too late") || strings.Contains(md, "(from late)") {
		t.Fatalf("the context: %+v", f.Context)
	}
	// The attempt runs while the late provider's call has still not
	// returned: the guard's deadline ended the wait, not the provider.
	if lateReturned.Load() {
		t.Fatal("the late provider returned before the test let it")
	}
	stopRunVia(t, e, run.ID)

	// With every provider adding nothing the task files are as with none.
	e2 := contextEnv(t, failing, panicking)
	e2.gitRepo(t)
	run2, release, err := e2.srv.runs.LaunchNamed(t.Context(), managedCrew(e2.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	md, f, _ = attemptTaskFiles(t, e2, run2.ID, "build")
	if f.Context != nil || strings.Contains(md, "## Context") {
		t.Fatalf("no block: %+v", f.Context)
	}
	stopRunVia(t, e2, run2.ID)
}

// task_context serves the blocks: the route's markdown has the Context
// section, and its task the blocks.
func TestTaskContextServesTheBlocks(t *testing.T) {
	graph := &fakeContext{name: "graph", answer: blocksOf(ext.ContextBlock{Title: "Callers of Build", Ref: "build.go", Body: "Build is called from ``main.go``."})}
	e := contextEnv(t, graph)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	resp, out := e.do("GET", "/api/sessions/"+sid+"/task", e.agentToken(sid), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task: %d %v", resp.StatusCode, out)
	}
	md, _ := out["markdown"].(string)
	if !strings.Contains(md, "## Context\n") || !strings.Contains(md, "### Callers of Build (from graph)\n\nAbout: build.go\n\n```\nBuild is called from ``main.go``.\n```") {
		t.Fatalf("the route's markdown: %s", md)
	}
	task, _ := out["task"].(map[string]any)
	ctx, _ := task["context"].([]any)
	if len(ctx) != 1 || ctx[0].(map[string]any)["source"] != "graph" || ctx[0].(map[string]any)["ref"] != "build.go" {
		t.Fatalf("the route's task: %v", task["context"])
	}
	stopRunVia(t, e, run.ID)
}

// The files the agents touch reach every context provider: a server
// session's and a hosted session's, each path absolute as the session's
// touched index keys it, a server session's denied path never offered; a
// provider that never returns drops its oldest touches and holds neither
// the sessions nor the other provider.
func TestTouchesReachProviders(t *testing.T) {
	seen := &fakeContext{name: "seen", touches: make(chan ext.TouchedFile, 4096)}
	blocked := &fakeContext{name: "blocked", gate: make(chan struct{})}
	e := contextEnv(t, seen, blocked)
	// Run before the server's cleanup (LIFO): the blocked delivery returns.
	t.Cleanup(func() { close(blocked.gate) })
	// The touch sink is the hub's last, after the run engine's (a server
	// with no context provider has none: TestSinksInMainsOrder).
	e.srv.events.mu.Lock()
	sinks := slices.Clone(e.srv.events.sinks)
	e.srv.events.mu.Unlock()
	if n := len(sinks); n < 2 || funcName(sinks[n-1]) != funcName(e.srv.noteTouches) || funcName(sinks[n-2]) != funcName(e.srv.runs.OnActivity) {
		t.Fatalf("the hub's sinks: %d", n)
	}
	next := func() ext.TouchedFile {
		t.Helper()
		return <-seen.touches
	}

	id := e.createSession("cat")
	event := func(body map[string]any) {
		t.Helper()
		if resp, out := e.do("POST", "/api/sessions/"+id+"/events", adminToken, body); resp.StatusCode >= 300 {
			t.Fatalf("event %v: %d %v", body, resp.StatusCode, out)
		}
	}
	event(map[string]any{"type": "file", "op": "edit", "path": "internal/users.go", "tool": "Edit"})
	event(map[string]any{"type": "progress", "message": "not a file"})
	// The data directory is on the deny list: never offered.
	event(map[string]any{"type": "file", "op": "read", "path": filepath.Join(e.srv.cfg.DataDir, "store.db"), "tool": "Read"})
	event(map[string]any{"type": "file", "op": "write", "path": filepath.Join(e.root, "notes.md"), "tool": "Write"})
	got := next()
	if got.Path != filepath.Join(e.root, "internal/users.go") || got.Op != "edit" || got.Tool != "Edit" || got.Session.ID != id || got.Session.AgentID != "cat" || got.Session.Name == "" || got.At.IsZero() {
		t.Fatalf("the first touch: %+v", got)
	}
	// In order: the denied read is skipped, the write is next.
	if got = next(); got.Path != filepath.Join(e.root, "notes.md") || got.Op != "write" {
		t.Fatalf("after the denied path: %+v", got)
	}

	host := dialFakeHost(t, e, "hosted-agent-token")
	host.send(hostActivity(session.ActivityFile, func(a *proto.Activity) { a.Op = "edit"; a.Path = "lib/x.go"; a.Tool = "Edit"; a.ByName = "codex" }))
	if got = next(); got.Path != filepath.Join(e.root, "lib/x.go") || got.Session.ID != host.sessionID || got.Op != "edit" || got.By != "codex" {
		t.Fatalf("the hosted session's touch: %+v", got)
	}

	// The blocked provider holds one delivery and queues ext.TouchQueue
	// more, dropping the oldest past them; every touch still reaches the
	// other provider, in order, and the hub never waited for either. The
	// touches go in chunks the other provider takes before the next, so
	// its own queue never fills.
	g := e.srv.exts.context[1]
	const extra = 50
	for sent := 0; sent < ext.TouchQueue+extra; {
		n := min(ext.TouchQueue/2, ext.TouchQueue+extra-sent)
		for i := range n {
			e.srv.events.activity(id, session.ActivityEntry{Type: session.ActivityFile, Op: "read", Path: fmt.Sprintf("flood/%d.go", sent+i)}, "")
		}
		for i := range n {
			if got := next(); got.Path != filepath.Join(e.root, fmt.Sprintf("flood/%d.go", sent+i)) {
				t.Fatalf("flood touch %d: %+v", sent+i, got)
			}
		}
		sent += n
	}
	queued, dropped := g.touches.stats()
	if queued != ext.TouchQueue || dropped < extra {
		t.Fatalf("the blocked provider's queue: %d queued, %d dropped", queued, dropped)
	}
}
