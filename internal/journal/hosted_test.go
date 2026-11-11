package journal

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A hosted record let go without an observed end is taken up again by the
// same host: its entries and head kept, its end cleared, Reopened counted
// and said, what the host reports filling only what it lacked; another
// host's registration of the id, and one after an observed end, is refused.
func TestBeginHostedReopensTheSameHostsRecord(t *testing.T) {
	j, dir := newJournal(t, Options{})
	id := sid(1)
	first := hostedRecord(id, "owner-a")
	first.Where, first.Start, first.StartFrom = "native", strings.Repeat("a", 40), StartFromHost
	in, err := j.BeginHosted(first)
	if err != nil {
		t.Fatal(err)
	}
	j.Note(in, Activity{Type: actProgress, Message: "before"})
	finish(j, in, Ending{How: EndingHostAway, Lost: true})
	j.Flush()
	if r, _ := onDisk(t, dir, id); r.Ending != EndingHostAway || r.LostAt.IsZero() {
		t.Fatalf("let go: %+v", r)
	}
	again := hostedRecord(id, "owner-a")
	again.Agent.Version, again.Start = "2.1.304", strings.Repeat("b", 40)
	if _, err := j.BeginHosted(hostedRecord(id, "owner-b")); !errors.Is(err, ErrExists) {
		t.Fatalf("another host's registration: %v", err)
	}
	in2, err := j.BeginHosted(again)
	if err != nil {
		t.Fatal(err)
	}
	if in2 == in {
		t.Fatal("the record was taken up under its earlier instance")
	}
	j.Note(in, Activity{Type: actProgress, Message: "from the earlier instance"})
	j.Note(in2, Activity{Type: actProgress, Message: "after"})
	r, _ := onDisk(t, dir, id)
	if r.Reopened != 1 || r.Ending != "" || !r.LostAt.IsZero() || r.Start != first.Start || r.Agent.Version != "2.1.304" || r.Where != "native" {
		t.Fatalf("taken up again: %+v", r)
	}
	j.Flush()
	r, _ = onDisk(t, dir, id)
	if got := messages(r); !slices.Equal(got, []string{"before", Reopening, "after"}) {
		t.Fatalf("the entries: %q", got)
	}
	// An observed end stands: the same host's next registration is refused.
	finish(j, in2, Ending{How: "exited (exit 0)", Commit: strings.Repeat("c", 64)})
	j.Flush()
	if r, _ := onDisk(t, dir, id); r.End != strings.Repeat("c", 64) || r.EndedAt.IsZero() {
		t.Fatalf("the end with its host's commit: %+v", r)
	}
	if _, err := j.BeginHosted(again); !errors.Is(err, ErrExists) {
		t.Fatalf("a registration after an observed end: %v", err)
	}
	// A server session's record is never a hosted one's to take.
	begin(t, j, serverRecord(sid(2)))
	finish(j, j.at(sid(2)), Ending{How: EndingLost, Lost: true})
	if _, err := j.BeginHosted(hostedRecord(sid(2), "owner-a")); !errors.Is(err, ErrExists) {
		t.Fatalf("a server session's record taken by a host: %v", err)
	}
}

// A live hosted record is never taken up by a second registration of its
// id; once let go, a record only on disk (the writer let it go, or a
// restart) is taken up from its file.
func TestBeginHostedRefusesALiveRecordAndTakesOneFromItsFile(t *testing.T) {
	dir := t.TempDir()
	j := openAt(t, dir, Options{})
	id := sid(1)
	in, err := j.BeginHosted(hostedRecord(id, "owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.BeginHosted(hostedRecord(id, "owner-a")); !errors.Is(err, ErrExists) {
		t.Fatalf("a live record taken up again: %v", err)
	}
	j.Note(in, Activity{Type: actProgress, Message: "before"})
	j.Close()
	// The next start finds it open: its host is away, never lost.
	j2 := openAt(t, dir, Options{})
	if r, _ := onDisk(t, dir, id); r.Ending != EndingHostAway || r.LostAt.IsZero() || !r.EndedAt.IsZero() {
		t.Fatalf("recovered: %+v", r)
	}
	if _, err := j2.BeginHosted(hostedRecord(id, "owner-a")); err != nil {
		t.Fatal(err)
	}
	j2.Flush()
	if r, _ := onDisk(t, dir, id); r.Reopened != 1 || r.Ending != "" || !slices.Equal(messages(r), []string{"before", Reopening}) {
		t.Fatalf("taken up after the restart: %+v", r)
	}
}

// A path a host reports is its host's: the deny list a server session's
// paths go through (Denied) is never asked of it, and it is kept as it
// came, whatever it names on this machine; a server session's paths beside
// it are asked as before. A hosted end with no commit of its host's asks
// the writer for none either.
func TestAHostedRecordAsksNothingOfThisMachine(t *testing.T) {
	var mu sync.Mutex
	var denied, ended []string
	j, dir := newJournal(t, Options{
		Denied: func(path string) bool {
			mu.Lock()
			defer mu.Unlock()
			denied = append(denied, path)
			return strings.HasPrefix(path, "/data/")
		},
		EndCommit: func(_ context.Context, r Record) string {
			mu.Lock()
			defer mu.Unlock()
			ended = append(ended, r.ID)
			return strings.Repeat("e", 40)
		},
	})
	hosted, err := j.BeginHosted(hostedRecord(sid(1), "owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	server := begin(t, j, serverRecord(sid(2)))
	for _, in := range []Instance{hosted, server} {
		j.Note(in, Activity{Type: actFile, Op: "edit", Path: "/data/conductor.json"})
		j.Note(in, Activity{Type: actFile, Op: "edit", Path: "/w/main.go"})
	}
	j.Flush()
	finish(j, hosted, Ending{How: "exited (exit 0)"})
	finish(j, server, Ending{How: "exited (exit 0)"})
	j.Flush()
	h, _ := onDisk(t, dir, sid(1))
	if len(h.Files) != 2 || !slices.ContainsFunc(h.Files, func(f File) bool { return f.Path == "/data/conductor.json" }) || h.End != "" {
		t.Fatalf("the hosted record: files %+v end %q", h.Files, h.End)
	}
	if s, _ := onDisk(t, dir, sid(2)); len(s.Files) != 1 || s.Files[0].Path != "/w/main.go" || s.End != strings.Repeat("e", 40) {
		t.Fatalf("the server record: files %+v end %q", s.Files, s.End)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(denied, []string{"/data/conductor.json", "/w/main.go"}) || !slices.Equal(ended, []string{sid(2)}) {
		t.Fatalf("this machine was asked of %q and for the end commits of %q", denied, ended)
	}
}

// Past the journal's bound a hosted session's ended record goes before any
// of the server's own sessions', the oldest first: no number of hosted
// records removes a server session's record; with the hosted ones gone,
// the server's own past the bound go as before.
func TestAHostsRecordsGoBeforeTheServersOwn(t *testing.T) {
	j, dir := newJournal(t, Options{maxRecords: new(3)})
	endedRecords(t, j, 0, 3)
	for i := 10; i < 20; i++ {
		in, err := j.BeginHosted(hostedRecord(sid(i), "owner-a"))
		if err != nil {
			t.Fatal(err)
		}
		finish(j, in, Ending{How: "exited (exit 0)"})
		j.Flush()
	}
	j.Prune()
	for i := range 3 {
		if !exists(dir, sid(i)) {
			t.Fatalf("the server's record %d was pruned for hosted ones", i)
		}
	}
	for i := 10; i < 20; i++ {
		if exists(dir, sid(i)) {
			t.Fatalf("hosted record %d kept past the bound", i)
		}
	}
	endedRecords(t, j, 3, 2)
	j.Prune()
	for i, want := range []bool{false, false, true, true, true} {
		if exists(dir, sid(i)) != want {
			t.Fatalf("server record %d kept %v, want %v", i, !want, want)
		}
	}
}

// An end that carries its host's commit asks the writer for none.
func TestAHostsEndCommitIsWrittenWithItsEnd(t *testing.T) {
	asked := make(chan string, 4)
	j, dir := newJournal(t, Options{EndCommit: func(_ context.Context, r Record) string {
		asked <- r.ID
		return ""
	}})
	in, err := j.BeginHosted(hostedRecord(sid(1), "owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	finish(j, in, Ending{How: "exited (exit 0)", Commit: strings.Repeat("d", 40)})
	begin(t, j, serverRecord(sid(2)))
	finish(j, j.at(sid(2)), Ending{How: "exited (exit 0)"})
	j.Flush()
	if r, _ := onDisk(t, dir, sid(1)); r.End != strings.Repeat("d", 40) {
		t.Fatalf("the hosted end: %+v", r)
	}
	close(asked)
	var ids []string
	for id := range asked {
		ids = append(ids, id)
	}
	if !slices.Equal(ids, []string{sid(2)}) {
		t.Fatalf("the writer was asked the end commits of %q", ids)
	}
}
