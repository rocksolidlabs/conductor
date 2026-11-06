package history

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A new index is version 1 of generation 1 with every table; an index of
// another version (a newer one, an older one, one of another shape) is
// cleared in place at the owner's open: the same file, this version's
// schema, the generation past the one it held, its rows gone.
func TestSchemaV1AndARebuildOfAnotherVersion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	ix := openIndex(t, dir, Options{})
	v, gen, err := readVersion(ctx, ix.db)
	if err != nil || v != Version || gen != 1 {
		t.Fatalf("version %d generation %d: %v", v, gen, err)
	}
	for _, table := range []string{"schema_version", "sources", "entries", "links", "entries_fts"} {
		count(t, ix, table)
	}
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reopen := func(prepare func(ix *Index)) *Index {
		t.Helper()
		prepare(ix)
		ix.Close()
		ix = openIndex(t, dir, Options{})
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("the index file was made again, not cleared in place")
		}
		if v, _, err := readVersion(ctx, ix.db); err != nil || v != Version {
			t.Fatalf("version %d: %v", v, err)
		}
		if n := count(t, ix, "entries"); n != 0 {
			t.Fatalf("%d entries kept across a rebuild", n)
		}
		ftsOK(t, ix)
		return ix
	}
	// A newer version, then an older one.
	ix = reopen(func(ix *Index) {
		if err := ix.clearTo(ctx, Version+1); err != nil {
			t.Fatal(err)
		}
		put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	})
	if _, gen, _ := readVersion(ctx, ix.db); gen != 3 {
		t.Fatalf("generation %d after two clears, want 3", gen)
	}
	ix = reopen(func(ix *Index) {
		if err := ix.clearTo(ctx, 0); err != nil {
			t.Fatal(err)
		}
		put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	})
	// One of another shape altogether: no schema_version, a table of its
	// own.
	ix = reopen(func(ix *Index) {
		for _, stmt := range []string{"DROP TABLE schema_version", "CREATE TABLE other (x)"} {
			if _, err := ix.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	})
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'other'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("the other shape's table kept: %d %v", n, err)
	}
	// Clear does the same, and a cleared index takes entries again.
	if err := ix.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, ix, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	wantKeys(t, ix, "alpha", "session:s1#e1")
}

// A file SQLite cannot read as a database is moved aside at the owner's
// open (with its -wal and -shm) and made again; a reader open on the old
// file reads the new one at its next search; what was moved aside is
// removed at the owner's next start.
func TestADamagedIndexIsMovedAsideAndReadersReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	a := openIndex(t, dir, Options{})
	put(t, a, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	reader, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	wantKeys(t, reader, "alpha", "session:s1#e1")
	a.Close()
	// The file's pages overwritten and its log lost, as a crash on a
	// failing disk would leave them (the reader still holds the old ones
	// open).
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	b := openIndex(t, dir, Options{})
	if _, err := os.Stat(path + damaged); err != nil {
		t.Fatalf("the damaged file was not moved aside: %v", err)
	}
	put(t, b, "session:s2", chat("session:s2#e1", "s2", "alpha", t0))
	wantKeys(t, reader, "alpha", "session:s2#e1")
	b.Close()
	openIndex(t, dir, Options{})
	if _, err := os.Stat(path + damaged); !os.IsNotExist(err) {
		t.Fatalf("the damaged file was kept past the next start: %v", err)
	}
}

// The helper reads the index at HISTORY_READ as a reader, in a process
// of its own: each line of its stdin a search, each answered on a line of
// its stdout as JSON.
func TestHelperReadsTheIndex(t *testing.T) {
	path := os.Getenv("HISTORY_READ")
	if path == "" {
		t.Skip("a helper")
	}
	ctx := context.Background()
	ix, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		fmt.Println(`{"err":"` + err.Error() + `"}`)
		os.Exit(2)
	}
	defer ix.Close()
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var answer helperAnswer
		q, err := ParseQuery(in.Text())
		if err == nil {
			var p Page
			p, err = ix.Search(ctx, q)
			answer.State, answer.Keys = p.State, keysOf(p.Entries)
		}
		if err != nil {
			answer.Err = err.Error()
		}
		b, _ := json.Marshal(answer)
		fmt.Println(string(b))
	}
}

type helperAnswer struct {
	State string   `json:"state"`
	Keys  []string `json:"keys"`
	Err   string   `json:"err"`
}

// A reader in another process, its connection open throughout, outlives
// a reindex and a change of the schema's version: each of its searches
// answers the index as rebuilt, or other_version with no rows, never an
// error.
func TestAReaderOutlivesAReindexAndASchemaChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	owner := openIndex(t, dir, Options{})
	put(t, owner, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))

	helper := exec.Command(os.Args[0], "-test.run=^TestHelperReadsTheIndex$")
	helper.Env = append(os.Environ(), "HISTORY_READ="+path)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		helper.Wait()
	}()
	out := bufio.NewScanner(stdout)
	ask := func(line string) helperAnswer {
		t.Helper()
		if _, err := fmt.Fprintln(stdin, line); err != nil {
			t.Fatal(err)
		}
		for out.Scan() {
			var a helperAnswer
			if json.Unmarshal(out.Bytes(), &a) == nil {
				if a.Err != "" {
					t.Fatalf("%q: the reader answered an error: %s", line, a.Err)
				}
				return a
			}
		}
		t.Fatalf("%q: the reader ended: %v", line, out.Err())
		return helperAnswer{}
	}
	if a := ask("alpha"); a.State != "" || !slices.Equal(a.Keys, []string{"session:s1#e1"}) {
		t.Fatalf("before: %+v", a)
	}
	// A reindex: cleared in place, rebuilt with what the stores hold now.
	if err := owner.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if a := ask("alpha"); a.State != "" || len(a.Keys) != 0 {
		t.Fatalf("after the clear: %+v", a)
	}
	put(t, owner, "session:s2", chat("session:s2#e1", "s2", "alpha", t0))
	if a := ask("alpha"); a.State != "" || !slices.Equal(a.Keys, []string{"session:s2#e1"}) {
		t.Fatalf("after the reindex: %+v", a)
	}
	// Another version's schema, as a newer Conductor would leave it.
	if err := owner.clearTo(ctx, Version+1); err != nil {
		t.Fatal(err)
	}
	if a := ask("alpha"); a.State != StateOtherVersion || len(a.Keys) != 0 {
		t.Fatalf("another version: %+v", a)
	}
	// And this version's again.
	if err := owner.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	put(t, owner, "session:s3", chat("session:s3#e1", "s3", "alpha", t0))
	if a := ask("alpha"); a.State != "" || !slices.Equal(a.Keys, []string{"session:s3#e1"}) {
		t.Fatalf("rebuilt again: %+v", a)
	}
}

// A reader opened before its owner made the file answers missing, then
// the index once it is there; a reader takes no writes.
func TestAReaderBeforeTheFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	reader, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if p := search(t, reader, "alpha", 0); p.State != StateMissing {
		t.Fatalf("state %q, want missing", p.State)
	}
	if _, err := reader.Entry(ctx, "session:s1#e1"); !strings.Contains(fmt.Sprint(err), StateMissing) {
		t.Fatalf("an entry of no index: %v", err)
	}
	owner := openIndex(t, dir, Options{})
	put(t, owner, "session:s1", chat("session:s1#e1", "s1", "alpha", t0))
	wantKeys(t, reader, "alpha", "session:s1#e1")
	wantErr(t, reader.Replace(ctx, Source{Key: "x"}, nil, nil), ErrReadOnly)
	wantErr(t, reader.Clear(ctx), ErrReadOnly)
	owner.Close()
	wantErr(t, owner.Replace(ctx, Source{Key: "x"}, nil, nil), ErrClosed)
}
