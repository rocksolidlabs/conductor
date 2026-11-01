package dispatch

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// Two operations, the first held by a seam after its commit while the
// second is under way: the observer is told in commit order, each call
// with what its commit changed and a Seq greater than the last's; a
// refused operation and one answered from its receipt are not told; Head
// is the last change's position.
func TestOnCommitInOrder(t *testing.T) {
	s := openStore(t, storeDir(t))
	ctx := context.Background()
	if head, err := s.Head(ctx); err != nil || head != 0 {
		t.Fatalf("Head of a new store: %d %v", head, err)
	}
	var mu sync.Mutex
	var calls [][]Change
	s.OnCommit(func(cs []Change) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, cs)
	})
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.afterCommit = func() {
		first := false
		once.Do(func() { first = true })
		if first {
			close(held)
			<-release
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if rc, err := s.CreateProject(ctx, testOp("p-a"), NewProject{Key: "AAA", Name: "a"}); err != nil || rc.Status != StatusDone {
			t.Errorf("project a: %+v %v", rc, err)
		}
	})
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatal("the first operation never committed")
	}
	wg.Go(func() {
		if rc, err := s.CreateProject(ctx, testOp("p-b"), NewProject{Key: "BBB", Name: "b"}); err != nil || rc.Status != StatusDone {
			t.Errorf("project b: %+v %v", rc, err)
		}
	})
	// The second waits for the first's call: the lock is the first's still.
	if s.mu.TryLock() {
		s.mu.Unlock()
		t.Fatal("the first operation let its lock go before its observer was told")
	}
	close(release)
	wg.Wait()
	s.afterCommit = nil
	// A refusal (the key taken) and a repeat (the same op id and content)
	// commit no change, and are not told.
	if rc, err := s.CreateProject(ctx, testOp("p-c"), NewProject{Key: "AAA", Name: "c"}); err != nil || rc.Status != StatusFailed {
		t.Fatalf("a taken key: %+v %v", rc, err)
	}
	if rc, err := s.CreateProject(ctx, testOp("p-b"), NewProject{Key: "BBB", Name: "b"}); err != nil || !rc.Repeat {
		t.Fatalf("a repeat: %+v %v", rc, err)
	}
	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	if len(got) != 2 || len(got[0]) == 0 || len(got[1]) == 0 {
		t.Fatalf("told %+v, want the two commits", got)
	}
	if got[0][0].Entity != "project" || got[1][0].Entity != "project" || got[0][0].ID == got[1][0].ID {
		t.Fatalf("told %+v", got)
	}
	if !(got[0][0].Seq < got[1][0].Seq) {
		t.Fatalf("Seq %d then %d: not in commit order", got[0][0].Seq, got[1][0].Seq)
	}
	head, err := s.Head(ctx)
	if err != nil || head != got[1][len(got[1])-1].Seq {
		t.Fatalf("Head %d %v, want the last change's %d", head, err, got[1][len(got[1])-1].Seq)
	}
	// The observer's copy is its own: OnChange's listeners are handed
	// theirs, as before.
	pages := listen(s)
	newProject(t, s, "CCC")
	if cs := pages.one(t); len(cs) == 0 || cs[0].Entity != "project" {
		t.Fatalf("OnChange %+v", cs)
	}
}
