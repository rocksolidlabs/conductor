package history

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// t0 is the tests' now.
var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// openIndex opens an index at dir/index.sqlite as its owner, its clock at
// t0, closed at the test's end.
func openIndex(t testing.TB, dir string, opts Options) *Index {
	t.Helper()
	ix, err := Open(context.Background(), filepath.Join(dir, File), opts)
	if err != nil {
		t.Fatal(err)
	}
	ix.now = func() time.Time { return t0 }
	t.Cleanup(func() { ix.Close() })
	return ix
}

// chat is a said entry of a session's chat.
func chat(key, session, body string, at time.Time) Entry {
	return Entry{Key: key, Kind: "session.chat", Ref: "session:" + session, Anchor: "session:" + session, Title: "chat", Body: body, At: at}
}

// receipt is a check receipt of a run's build task.
func receipt(key, run, subject, outcome, body string, at time.Time) Entry {
	return Entry{Key: key, Kind: "check", Ref: key, Anchor: "task:" + run + "/build", Run: run, Subject: subject, Outcome: outcome,
		Commit: "0123456789abcdef0123456789abcdef01234567", Title: "unit on build", Summary: "go test ./...", Body: body, At: at, Where: "native"}
}

// put replaces a source with the entries.
func put(t testing.TB, ix *Index, source string, entries ...Entry) {
	t.Helper()
	if err := ix.Replace(context.Background(), Source{Key: source, Mark: "m1"}, entries, nil); err != nil {
		t.Fatal(err)
	}
}

// search parses the line and answers its first page of at most limit.
func search(t testing.TB, ix *Index, line string, limit int) Page {
	t.Helper()
	q, err := ParseQuery(line)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	q.Limit = limit
	p, err := ix.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return p
}

// keysOf is a page's keys in its order.
func keysOf(hits []Hit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.Key)
	}
	return out
}

// wantKeys fails unless the line's first page holds exactly the keys, in
// that order.
func wantKeys(t *testing.T, ix *Index, line string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := keysOf(search(t, ix, line, MaxLimit).Entries); !slices.Equal(got, want) {
		t.Errorf("%q: %v, want %v", line, got, want)
	}
}

// wantSameKeys fails unless the line's first page holds exactly the keys,
// in any order.
func wantSameKeys(t *testing.T, ix *Index, line string, want ...string) {
	t.Helper()
	got := keysOf(search(t, ix, line, MaxLimit).Entries)
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%q: %v, want %v", line, got, want)
	}
}

// ftsOK fails unless the search table agrees with the entries it indexes.
func ftsOK(t *testing.T, ix *Index) {
	t.Helper()
	if _, err := ix.db.Exec("INSERT INTO entries_fts (entries_fts, rank) VALUES ('integrity-check', 1)"); err != nil {
		t.Fatalf("the search table: %v", err)
	}
}

// count is a table's rows.
func count(t *testing.T, ix *Index, table string) int {
	t.Helper()
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%v, want %v", err, target)
	}
}
