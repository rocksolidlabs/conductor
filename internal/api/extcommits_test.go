package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The commit notes, the pack mark and the free build (docs/plans/
// context-seams.md, CT-1).

// noter is a context provider that takes commit notes: each is sent to
// notes, after gate (when set) lets it.
type noter struct {
	*fakeContext
	notes chan ext.CommitNote
	gate  chan struct{}
}

func (n *noter) NoteCommit(_ context.Context, c ext.CommitNote) {
	if n.gate != nil {
		<-n.gate
	}
	n.notes <- c
}

// A provider that takes commit notes is told the run's base once the run
// has started, in its own queue; one that never returns from NoteCommit
// holds only its own queue, which drops its oldest notes past
// CommitQueue, and the run goes on.
func TestCommitNotesQueuedNeverBlock(t *testing.T) {
	gate := make(chan struct{})
	n := &noter{fakeContext: &fakeContext{name: "graph"}, notes: make(chan ext.CommitNote, ext.CommitQueue+16), gate: gate}
	e := contextEnv(t, n)
	t.Cleanup(func() { close(gate) }) // before the server's own cleanup ends its queues
	e.gitRepo(t)
	base := gitIn(t, e.root, "rev-parse", "HEAD")
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	// The provider holds the base's note at its gate; the run goes on.
	a := runningAttempt(t, e, run.ID, "build", 1)
	if a["start"] != base {
		t.Fatalf("the attempt: %v", a)
	}
	g := e.srv.exts.context[0]
	if g.commits == nil {
		t.Fatal("no queue of commit notes for a provider that takes them")
	}
	note := ext.CommitNote{Run: run.ID, Kind: ext.CommitBase, Commit: base, At: time.Now().UTC()}
	done := make(chan struct{})
	go func() {
		for range ext.CommitQueue + 10 {
			g.commits.push(note)
		}
		close(done)
	}()
	<-done
	queued, dropped := g.commits.stats()
	if queued != ext.CommitQueue || dropped < 10 {
		t.Fatalf("queued %d, dropped %d", queued, dropped)
	}
	stopRunVia(t, e, run.ID)
}

// The base is noted when a run starts, and a work attempt's candidate
// when its submit is recorded, to the provider's own NoteCommit.
func TestCommitNotesReachTheProvider(t *testing.T) {
	n := &noter{fakeContext: &fakeContext{name: "graph"}, notes: make(chan ext.CommitNote, 16)}
	e := contextEnv(t, n)
	e.gitRepo(t)
	base := gitIn(t, e.root, "rev-parse", "HEAD")
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	defer stopRunVia(t, e, run.ID)
	if c := <-n.notes; c.Run != run.ID || c.Kind != ext.CommitBase || c.Commit != base {
		t.Fatalf("the base: %+v", c)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	tree := a["worktree"].(string)
	cand := commitFile(t, tree, "built.txt", "yes\n")
	if _, err := e.srv.runs.SubmitResult(t.Context(), a["sessionId"].(string), crew.Result{Summary: "built"}); err != nil {
		t.Fatal(err)
	}
	if c := <-n.notes; c.Kind != ext.CommitCandidate || c.Task != "build" || c.Attempt != 1 || c.Commit != cand {
		t.Fatalf("the candidate: %+v", c)
	}
}

// A block's pack mark reaches the task files: task.json keeps it and
// task.md writes it under the block's title.
func TestContextKeepsThePackMark(t *testing.T) {
	graph := &fakeContext{name: "graph", answer: blocksOf(ext.ContextBlock{Title: "Callers of Build", Ref: "build.go", Body: "Build is called from main.go.", Pack: "p-4f2c-1"})}
	e := contextEnv(t, graph)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	defer stopRunVia(t, e, run.ID)
	md, f, _ := attemptTaskFiles(t, e, run.ID, "build")
	if len(f.Context) != 1 || f.Context[0].Pack != "p-4f2c-1" || !strings.Contains(md, "### Callers of Build (from graph)\n\nPack: p-4f2c-1\n\nAbout: build.go\n") {
		t.Fatalf("the task files: %+v\n%s", f.Context, md)
	}
}

// With no context provider registered nothing of the seams runs: an
// attempt's task files hold no Context section and no mark, no queue of
// commit notes is made, and no Host reads a repository.
func TestFreeBuildUnchanged(t *testing.T) {
	e := contextEnv(t)
	e.gitRepo(t)
	if len(e.srv.exts.context) != 0 {
		t.Fatalf("%d context providers", len(e.srv.exts.context))
	}
	log := gitLog(t, e)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	defer stopRunVia(t, e, run.ID)
	md, f, a := attemptTaskFiles(t, e, run.ID, "build")
	js, err := os.ReadFile(filepath.Join(a["worktree"].(string), ".conductor", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Context != nil || strings.Contains(md, "## Context") || strings.Contains(md, "Pack:") || strings.Contains(string(js), `"context"`) || strings.Contains(string(js), `"pack"`) {
		t.Fatalf("the free build's task files:\n%s\n%s", md, js)
	}
	if s := ran(t, log); strings.Contains(s, "GIT_LITERAL") || strings.Contains(s, "NR=1 LP=1") {
		t.Fatalf("a read's git ran in the free build:\n%s", s)
	}
	ledgerAttemptIn(t, e.srv, run.ID, "build", 1, ledger.AttemptRunning)
	if _, err := testHost(e.srv, t.Context(), "graph").ReadTree(t.Context(), ext.TreeRequest{Run: run.ID, Commit: a["start"].(string)}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a read with no context provider: %v", err)
	}
}
