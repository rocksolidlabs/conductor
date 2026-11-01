package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// events is an observer that keeps what it is told.
type events struct {
	mu  sync.Mutex
	got []Event
}

func (e *events) add(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev)
}

func (e *events) all() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.got)
}

// A commit is told after its rename or remove has landed, never for one
// that failed, nor for a delete of what is not there.
func TestObserveAfterRename(t *testing.T) {
	s, _ := Open(t.TempDir())
	var ev events
	s.Observe(func(e Event) {
		// Told after the rename: the document is in place, with its bytes.
		if e.Op == OpSaved {
			if _, err := os.Stat(filepath.Join(s.Dir(), e.Path)); err != nil {
				t.Errorf("told of %s before it landed: %v", e.Path, err)
			}
		}
		ev.add(e)
	})
	if err := s.Save("c.json", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("rename refused")
	rename = func(string, string) error { return refused }
	if err := s.Save("c.json", doc{N: 2}); !errors.Is(err, refused) {
		t.Fatalf("Save with a failing rename: %v", err)
	}
	if err := s.WriteFile("token", []byte("x")); !errors.Is(err, refused) {
		t.Fatalf("WriteFile with a failing rename: %v", err)
	}
	rename = os.Rename
	if err := s.Delete("missing.json"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFile("missing"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("c.json"); err != nil {
		t.Fatal(err)
	}
	want := []Event{{OpSaved, "c.json", 1}, {OpDeleted, "c.json", 2}}
	if got := ev.all(); !slices.Equal(got, want) {
		t.Fatalf("told %+v, want %+v", got, want)
	}
	if n := s.Seq(); n != 2 {
		t.Fatalf("Seq %d, want 2", n)
	}
}

// A Sub, made before Observe or after, of the store or of another Sub,
// shares its observer and its sequence, its events' paths from the
// directory Open was given.
func TestObserveInheritedBySub(t *testing.T) {
	root, _ := Open(t.TempDir())
	before, err := root.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	var ev events
	root.Observe(ev.add)
	after, err := root.Sub("runs")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := after.Sub("deep")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return before.Save("todo.json", doc{N: 1}) },
		func() error { return after.Save("r-1.json", doc{N: 2}) },
		func() error { return nested.Save("x.json", doc{N: 3}) },
		func() error { return root.Save("catalog.json", doc{N: 4}) },
		func() error { return before.Delete("todo.json") },
		func() error { return nested.WriteFile("plain", []byte("p")) },
		func() error { return nested.RemoveFile("plain") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	want := []Event{
		{OpSaved, "crews/todo.json", 1},
		{OpSaved, "runs/r-1.json", 2},
		{OpSaved, "runs/deep/x.json", 3},
		{OpSaved, "catalog.json", 4},
		{OpDeleted, "crews/todo.json", 5},
		{OpSaved, "runs/deep/plain", 6},
		{OpDeleted, "runs/deep/plain", 7},
	}
	if got := ev.all(); !slices.Equal(got, want) {
		t.Fatalf("told %+v, want %+v", got, want)
	}
	// A store Open made on its own has an observer of its own.
	other, _ := Open(filepath.Join(root.Dir(), "crews"))
	if err := other.Save("alone.json", doc{}); err != nil {
		t.Fatal(err)
	}
	if got := ev.all(); len(got) != len(want) {
		t.Fatalf("a store of its own told the root's observer: %+v", got[len(want):])
	}
	root.Observe(nil)
	if err := before.Save("todo.json", doc{}); err != nil {
		t.Fatal(err)
	}
	if got := ev.all(); len(got) != len(want) {
		t.Fatalf("told after Observe(nil): %+v", got[len(want):])
	}
}

// A plain file's event names it and nothing more: its bytes never leave
// the package, and OpenDoc, the one way the sync seam reads a document,
// opens a regular JSON document only.
func TestPlainFileNoBytes(t *testing.T) {
	s, _ := Open(t.TempDir())
	var ev events
	s.Observe(ev.add)
	const secret = "workbench-secret-0123456789"
	if err := s.WriteFile("workbench-token", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	got := ev.all()
	if len(got) != 1 || got[0] != (Event{OpSaved, "workbench-token", 1}) {
		t.Fatalf("told %+v", got)
	}
	if strings.Contains(fmt.Sprintf("%#v", got), secret) {
		t.Fatal("an event carries the file's bytes")
	}
	if _, err := s.OpenDoc("workbench-token"); err == nil {
		t.Fatal("OpenDoc opened a plain file")
	}
	// A document is opened, read-only, one version of it.
	if err := s.Save("catalog.json", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	f, err := s.OpenDoc("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("catalog.json", doc{N: 2}); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	if string(b) != "{\n  \"n\": 1\n}\n" {
		t.Fatalf("the open document reads %q, want the version opened", b)
	}
	// Tried while the file is open: a closed one refuses every write.
	if _, err := f.Write([]byte("x")); err == nil || errors.Is(err, os.ErrClosed) {
		t.Fatalf("OpenDoc's file takes writes: %v", err)
	}
	f.Close()
	sub, _ := s.Sub("runs")
	if err := sub.Save("r-1.json", doc{N: 3}); err != nil {
		t.Fatal(err)
	}
	if f, err := s.OpenDoc("runs/r-1.json"); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	if _, err := s.OpenDoc("runs/gone.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenDoc of a document not there: %v", err)
	}
	// A link in a document's place, a directory, a path out of the store
	// and a name no document has are refused.
	if err := os.Symlink(filepath.Join(s.Dir(), "catalog.json"), filepath.Join(s.Dir(), "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir(), "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link.json", "dir.json", "../x.json", "/etc/x.json", "runs/../catalog.json", "Catalog.json", "runs\\r-1.json", ""} {
		if f, err := s.OpenDoc(p); err == nil {
			f.Close()
			t.Fatalf("OpenDoc(%q) opened it", p)
		}
	}
}

// Two writers of one document through two Subs of its directory (each with
// a mutex of its own) and the root's observer: the first held after its
// rename while the second commits. They are told in the order of their
// renames, numbered so, and the document holds the second's.
func TestObserveInCommitOrder(t *testing.T) {
	root, _ := Open(t.TempDir())
	var ev events
	root.Observe(ev.add)
	a, _ := root.Sub("crews")
	b, _ := root.Sub("crews")
	if err := root.Save("catalog.json", doc{}); err != nil { // Seq 1, before the race
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	arrived := make(chan struct{})
	var once, arrivedOnce sync.Once
	var first sync.Once
	firstPath := ""
	afterCommit = func(path string) {
		isFirst := false
		first.Do(func() { isFirst, firstPath = true, path })
		if isFirst {
			once.Do(func() { close(held) })
			<-release
		}
	}
	beforeCommit = func(path string) {
		if firstPath != "" {
			arrivedOnce.Do(func() { close(arrived) })
		}
	}
	t.Cleanup(func() { afterCommit, beforeCommit = nil, nil })
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := a.Save("todo.json", doc{N: 1}); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatal("the first save never landed")
	}
	wg.Go(func() {
		if err := b.Save("todo.json", doc{N: 2}); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("the second save never came to its commit")
	}
	// The first still holds the commit lock past its rename, so the second
	// cannot rename, nor be told, before the first is numbered and told.
	if root.obs.mu.TryLock() {
		root.obs.mu.Unlock()
		t.Fatal("the first save let the commit lock go before it was told")
	}
	close(release)
	wg.Wait()
	want := []Event{{OpSaved, "catalog.json", 1}, {OpSaved, "crews/todo.json", 2}, {OpSaved, "crews/todo.json", 3}}
	if got := ev.all(); !slices.Equal(got, want) {
		t.Fatalf("told %+v, want %+v", got, want)
	}
	var d doc
	if ok, err := a.Load("todo.json", &d); !ok || err != nil || d.N != 2 {
		t.Fatalf("the document holds %+v (%v %v), want the second save's", d, ok, err)
	}
}

// ListDocs lists a directory of the store as Sub would open it, and makes
// none: one that is not there holds no document.
func TestListDocs(t *testing.T) {
	s, _ := Open(t.TempDir())
	if got, err := s.ListDocs("runs"); err != nil || got != nil {
		t.Fatalf("ListDocs of a missing directory: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ListDocs made the directory: %v", err)
	}
	sub, _ := s.Sub("runs")
	_ = sub.Save("b.json", doc{})
	_ = sub.Save("a.json", doc{})
	_ = sub.WriteFile("plain", []byte("x"))
	_ = s.Save("catalog.json", doc{})
	got, err := s.ListDocs("runs")
	if err != nil || len(got) != 2 || got[0].Name != "a.json" || got[1].Name != "b.json" {
		t.Fatalf("ListDocs(runs): %+v %v", got, err)
	}
	if got, err := s.ListDocs(""); err != nil || len(got) != 1 || got[0].Name != "catalog.json" {
		t.Fatalf("ListDocs(\"\"): %+v %v", got, err)
	}
	if _, err := s.ListDocs("../x"); err == nil {
		t.Fatal("ListDocs took a path out of the store")
	}
}
