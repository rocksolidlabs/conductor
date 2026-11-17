package brakeman

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// briefNow keeps a briefed request under op, a minute after the last.
func (f *fixture) briefNow(op string) string {
	f.t.Helper()
	f.clock.add(time.Minute)
	return f.keepBriefed(op, goodBrief(f.t)).ID
}

func (f *fixture) openedPath() string { return filepath.Join(f.data.Dir(), Dir, OpenedFile) }

// unread is actor's unread ids, failing the test on an error.
func (f *fixture) unread(actor string) []string {
	f.t.Helper()
	u, err := f.s.Opened(actor)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.Unread
}

// TestTheFirstReadBeginsTheCount: an actor's first read begins its count:
// a brief kept before it is not unread, one kept after is, newest first,
// and the beginning stays put across reads and a reopen.
func TestTheFirstReadBeginsTheCount(t *testing.T) {
	f := newFixture(t)
	before := f.briefNow("op-1")
	f.clock.add(time.Minute)
	u, err := f.s.Opened("admin")
	if err != nil || len(u.Unread) != 0 || !u.Begun.Equal(f.clock.now()) {
		t.Fatalf("the first read: %+v %v", u, err)
	}
	a := f.briefNow("op-2")
	b := f.briefNow("op-3")
	f.create("op-4") // no brief
	if got := f.unread("admin"); !slices.Equal(got, []string{b, a}) {
		t.Fatalf("unread %v, want %v", got, []string{b, a})
	}
	f.reopen()
	u, err = f.s.Opened("admin")
	if err != nil || !slices.Equal(u.Unread, []string{b, a}) || u.Begun.After(f.clock.now()) || slices.Contains(u.Unread, before) {
		t.Fatalf("after a reopen: %+v %v", u, err)
	}
}

// TestOpenedMarksArePerActor: one actor's marks are its own: another's
// count is not changed by them, and each begins at its own first read.
func TestOpenedMarksArePerActor(t *testing.T) {
	f := newFixture(t)
	f.unread("admin")
	f.unread("user:ann")
	a, b := f.briefNow("op-1"), f.briefNow("op-2")
	u, changed, err := f.s.MarkOpened("admin", []string{a})
	if err != nil || !changed || !slices.Equal(u.Unread, []string{b}) {
		t.Fatalf("marked: %+v %v %v", u, changed, err)
	}
	if got := f.unread("user:ann"); !slices.Equal(got, []string{b, a}) {
		t.Fatalf("another actor's unread %v", got)
	}
	f.unread("user:bob") // begins after both
	if got := f.unread("user:bob"); len(got) != 0 {
		t.Fatalf("a later actor's unread %v", got)
	}
}

// TestMarkingIsASetUnion: marking again changes nothing; an id not kept,
// one with no brief and one marked already are dropped without error;
// an id that is not one, none, and past MaxOpenedMark are refused, and
// nothing is written for them.
func TestMarkingIsASetUnion(t *testing.T) {
	f := newFixture(t)
	f.unread("admin")
	a := f.briefNow("op-1")
	queued := f.create("op-2").ID
	if _, changed, err := f.s.MarkOpened("admin", []string{a}); err != nil || !changed {
		t.Fatalf("the first mark: %v %v", changed, err)
	}
	u, changed, err := f.s.MarkOpened("admin", []string{a, queued, "br-999"})
	if err != nil || changed || len(u.Unread) != 0 {
		t.Fatalf("again: %+v %v %v", u, changed, err)
	}
	before, _ := os.ReadFile(f.openedPath())
	for _, ids := range [][]string{nil, {}, {"br-0"}, {"x"}, {a, "br-01"}, slices.Repeat([]string{a}, MaxOpenedMark+1)} {
		_, _, err := f.s.MarkOpened("admin", ids)
		wantInvalid(t, err, map[bool]string{true: "ids", false: fmt.Sprintf("ids[%d]", len(ids)-1)}[len(ids) == 0 || len(ids) > MaxOpenedMark])
	}
	if after, _ := os.ReadFile(f.openedPath()); string(after) != string(before) {
		t.Fatal("a refused mark wrote the document")
	}
	if _, _, err := f.s.MarkOpened("admin", slices.Repeat([]string{a}, MaxOpenedMark)); err != nil {
		t.Fatalf("at the bound: %v", err)
	}
	if _, err := f.s.Opened(""); !errors.Is(err, ErrBound) {
		t.Fatalf("no actor: %v", err)
	}
	if _, err := f.s.Opened(strings.Repeat("a", MaxOpenedActor+1)); !errors.Is(err, ErrBound) {
		t.Fatalf("a long actor: %v", err)
	}
}

// TestOpenedIsBounded: past MaxOpenedActors the actor written longest
// ago goes (never the caller); an actor keeps its newest MaxOpenedIDs
// marks; a pruned request's mark goes; the document at its largest, 32
// actors of 500 of the longest ids, fits MaxOpenedDoc.
func TestOpenedIsBounded(t *testing.T) {
	f := newFixture(t)
	for i := range MaxOpenedActors {
		f.clock.add(time.Second)
		f.unread(fmt.Sprintf("user:%02d", i))
	}
	f.clock.add(time.Second)
	f.unread("user:new")
	doc := f.loadOpened()
	if len(doc.People) != MaxOpenedActors || slices.ContainsFunc(doc.People, func(e openedEntry) bool { return e.Actor == "user:00" }) {
		t.Fatalf("%d actors, user:00 kept %v", len(doc.People), doc.People[0].Actor)
	}
	// The newest 500 marks of an actor, and a pruned request's dropped.
	ids := make([]int64, 0, MaxOpenedIDs+1)
	for i := range MaxOpenedIDs + 1 {
		ids = append(ids, requestNumber(f.create(fmt.Sprintf("op-%d", i)).ID))
	}
	gone := f.close(f.create("op-gone").ID, StateCancelled)
	f.s.mu.Lock()
	doc.People[len(doc.People)-1].Opened = append(slices.Clone(ids), requestNumber(gone.ID))
	err := f.s.write(f.s.st, OpenedFile, doc, MaxOpenedDoc)
	f.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.PruneKeeping(0); err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	f.unread("user:new")
	kept := f.loadOpened().People[len(doc.People)-1].Opened
	if len(kept) != MaxOpenedIDs || kept[0] != ids[len(ids)-1] || slices.Contains(kept, ids[0]) || slices.Contains(kept, requestNumber(gone.ID)) {
		t.Fatalf("kept %d marks, newest %d", len(kept), kept[0])
	}
	// The largest document.
	big := openedDoc{Schema: OpenedSchema}
	for i := range MaxOpenedActors {
		e := openedEntry{Actor: "user:" + strings.Repeat("s", 48), Begun: f.clock.now(), At: f.clock.now()}
		for j := range MaxOpenedIDs {
			e.Opened = append(e.Opened, int64(999999999999999999-i*MaxOpenedIDs-j))
		}
		big.People = append(big.People, e)
	}
	b, err := encode(big)
	if err != nil || len(b) > MaxOpenedDoc {
		t.Fatalf("the largest document is %d bytes, at most %d: %v", len(b), MaxOpenedDoc, err)
	}
	f.s.mu.Lock()
	err = f.s.write(f.s.st, OpenedFile, big, MaxOpenedDoc)
	if err == nil {
		_, err = f.s.loadOpened()
	}
	f.s.mu.Unlock()
	if err != nil {
		t.Fatalf("the largest document written and read: %v", err)
	}
}

func (f *fixture) loadOpened() openedDoc {
	f.t.Helper()
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	doc, err := f.s.loadOpened()
	if err != nil {
		f.t.Fatal(err)
	}
	return doc
}

// TestOpenedIsTheOwnersAlone: a store that does not own the records
// answers neither read nor mark, and writes nothing.
func TestOpenedIsTheOwnersAlone(t *testing.T) {
	f := newFixture(t)
	a := f.briefNow("op-1")
	second := f.open()
	t.Cleanup(func() { second.Close() })
	if _, err := second.Opened("admin"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("a read: %v", err)
	}
	if _, _, err := second.MarkOpened("admin", []string{a}); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("a mark: %v", err)
	}
	if exists(t, f.openedPath()) {
		t.Fatal("a store that does not own the records wrote the marks")
	}
}

// TestADamagedOpenedDocumentIsKept: a document that does not read, or of
// a later schema, answers ErrOpenedDamaged and is kept as it is.
func TestADamagedOpenedDocumentIsKept(t *testing.T) {
	f := newFixture(t)
	a := f.briefNow("op-1")
	for _, doc := range []string{`{"schema":1,"people":[],"later":true}`, `{"schema":2,"people":[]}`, `not json`} {
		if err := os.WriteFile(f.openedPath(), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Opened("admin"); !errors.Is(err, ErrOpenedDamaged) {
			t.Fatalf("%s: a read %v", doc, err)
		}
		if _, _, err := f.s.MarkOpened("admin", []string{a}); !errors.Is(err, ErrOpenedDamaged) {
			t.Fatalf("%s: a mark %v", doc, err)
		}
		if b, _ := os.ReadFile(f.openedPath()); string(b) != doc {
			t.Fatalf("%s was rewritten: %s", doc, b)
		}
	}
}
