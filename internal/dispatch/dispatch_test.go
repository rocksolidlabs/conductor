package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenMakesTheSchemaWhole(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the directory: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, File)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the database: %v %v", fi, err)
	}
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name != 'sqlite_sequence' AND name NOT LIKE 'tickets\\_fts\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got = append(got, n)
	}
	rows.Close()
	if strings.Join(got, " ") != strings.Join(tables, " ") {
		t.Fatalf("tables %v", got)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM schema_version"); n != 1 {
		t.Fatalf("%d schema_version rows", n)
	}
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	var fk, sync int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys %d %v", fk, err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("synchronous %d %v", sync, err)
	}
	// The FTS index is there with its tokenizer: diacritics removed.
	if _, err := s.db.Exec("INSERT INTO tickets_fts (rowid, key, title, description, acceptance, body) VALUES (1, 'CND-1', 'Crème brûlée', '', '', '')"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM tickets_fts WHERE tickets_fts MATCH '"creme"'`); n != 1 {
		t.Fatalf("the FTS match found %d", n)
	}
	rc, err := s.AddPerson(context.Background(), testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Closed with its write-ahead log checkpointed, and opened again
	// whole: the schema is found, not made again.
	if fi, err := os.Stat(filepath.Join(dir, File+"-wal")); err == nil && fi.Size() != 0 {
		t.Fatalf("the WAL holds %d bytes after Close", fi.Size())
	}
	again := openStore(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after a reopen: %s", why)
	}
	if n := count(t, again, "SELECT COUNT(*) FROM schema_version"); n != 1 {
		t.Fatalf("%d schema_version rows after a reopen", n)
	}
	people, err := again.People(context.Background())
	if err != nil || len(people) != 1 || people[0].Name != "Alice" || people[0].CreatedAt.IsZero() {
		t.Fatalf("people after a reopen: %+v %v", people, err)
	}
}

func TestANewerDatabaseOpensReadOnly(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	rc, err := s.AddPerson(context.Background(), testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if _, err := s.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again := openStore(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "newer") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	// A write is refused read_only with the words, and written nowhere.
	rc, err = again.AddPerson(context.Background(), testOp("a2"), "Bob")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if !strings.Contains(rc.Error, "newer") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
	if _, found, err := again.Receipt(context.Background(), "test", "a2"); found || err != nil {
		t.Fatalf("a receipt was kept: %v %v", found, err)
	}
	if err := again.Checkpoint(context.Background()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a checkpoint: %v", err)
	}
	// A read works.
	if people, err := again.People(context.Background()); err != nil || len(people) != 1 {
		t.Fatalf("a read: %+v %v", people, err)
	}
	if rc, found, err := again.Receipt(context.Background(), "test", "a1"); !found || err != nil || rc.Status != StatusDone {
		t.Fatalf("the receipt kept: %+v %v %v", rc, found, err)
	}
}

func TestADamagedDatabaseOpensReadOnly(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	for i := range 20 {
		rc, err := s.AddPerson(context.Background(), testOp(opID("a", i)), fmt.Sprintf("Person %02d %s", i, strings.Repeat("x", 40)))
		want(t, rc, err, StatusDone, "")
	}
	// The receipts' root page: a page of content past the header and the
	// schema, so the database opens and fails its check.
	var page, pageSize int64
	if err := s.db.QueryRow("SELECT rootpage FROM sqlite_master WHERE name = 'ops'").Scan(&page); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	s.Close()
	path := filepath.Join(dir, File)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", int(pageSize))), (page-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	f.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed on a damaged database: %v", err)
	}
	defer again.Close()
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "check") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err := again.AddPerson(context.Background(), testOp("b1"), "Bob")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if !strings.Contains(rc.Error, "check") {
		t.Fatalf("the refusal's words: %q", rc.Error)
	}
}

func TestASymbolicLinkRefusesOpen(t *testing.T) {
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.sqlite"), filepath.Join(dir, File)); err != nil {
		t.Skip(err)
	}
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatal("a symbolic link opened")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refused as: %v", err)
	}
}

func TestADirectoryThatCannotBeMadeRefusesOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(file, "dispatch")); err == nil {
		s.Close()
		t.Fatal("a directory under a file opened")
	}
}

func TestCheckpointOnAnOpenStore(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	for i := range 3 {
		rc, err := s.AddPerson(context.Background(), testOp(opID("a", i)), fmt.Sprintf("Person %d", i))
		want(t, rc, err, StatusDone, "")
	}
	wal := filepath.Join(dir, File+"-wal")
	if fi, err := os.Stat(wal); err != nil || fi.Size() == 0 {
		t.Fatalf("no write-ahead log after writes: %v %v", fi, err)
	}
	if err := s.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(wal); err == nil && fi.Size() != 0 {
		t.Fatalf("the WAL holds %d bytes after Checkpoint", fi.Size())
	}
	rc, err := s.AddPerson(context.Background(), testOp("after"), "After")
	want(t, rc, err, StatusDone, "")
	if people, err := s.People(context.Background()); err != nil || len(people) != 4 {
		t.Fatalf("people after the checkpoint: %+v %v", people, err)
	}
}

// Eight stores opened at once on one empty directory all open writable:
// the second and later wait for the first's schema transaction on the
// write lock, then read the version it wrote and find nothing to do.
func TestEightOpenersMakeTheSchemaOnce(t *testing.T) {
	dir := storeDir(t)
	const n = 8
	stores := make([]*Store, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stores[i], errs[i] = Open(dir)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("opener %d: %v", i, errs[i])
		}
		t.Cleanup(func() { stores[i].Close() })
		if ro, why := stores[i].ReadOnly(); ro {
			t.Fatalf("opener %d is read-only: %s", i, why)
		}
	}
	for i, s := range stores {
		rc, err := s.AddPerson(context.Background(), testOp(opID("o", i)), fmt.Sprintf("Person %d", i))
		want(t, rc, err, StatusDone, "")
	}
	if n := count(t, stores[0], "SELECT COUNT(*) FROM schema_version"); n != 1 {
		t.Fatalf("%d schema_version rows", n)
	}
	for i, s := range stores {
		people, err := s.People(context.Background())
		if err != nil || len(people) != n {
			t.Fatalf("store %d sees %d people: %v", i, len(people), err)
		}
	}
}

// rawConn is a second connection on the store's file, to hold its write
// lock as another process would.
func rawConn(t *testing.T, s *Store) *sql.DB {
	t.Helper()
	return rawConnAt(t, s.path)
}

// rawConnAt is a connection on the database file at path, made when
// missing.
func rawConnAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// rollbackConnAt is a connection on the database file at path that sets
// no journal mode, as another tool's would: it finds the file in the mode
// the file is in.
func rollbackConnAt(t *testing.T, path string) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestAWriterHoldingTheDatabasePastTheTimeoutOpensReadOnly(t *testing.T) {
	dir := storeDir(t)
	s := openStore(t, dir)
	raw := rawConn(t, s)
	hold, err := raw.BeginTx(context.Background(), nil) // BEGIN IMMEDIATE: the write lock
	if err != nil {
		t.Fatal(err)
	}
	old := busyTimeout
	busyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { busyTimeout = old })
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ro, why := second.ReadOnly()
	if !ro || !strings.Contains(why, "another writer holds the database") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err := second.AddPerson(context.Background(), testOp("b1"), "Bob")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if err := hold.Rollback(); err != nil {
		t.Fatal(err)
	}
	// It reads; the first store writes on.
	if _, err := second.People(context.Background()); err != nil {
		t.Fatalf("a read: %v", err)
	}
	rc, err = s.AddPerson(context.Background(), testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
}

// A database in a rollback journal's mode, as another tool can leave it,
// that the store cannot write is read all the same: the read-only
// connection sets no journal mode, since a switch to WAL writes the file's
// header, which read-only mode refuses, and a refused open would leave the
// store nothing to read. The file stays in the mode it was found in. The
// writable open fails two ways: a file the store may not write, and a
// writer holding the database past the busy timeout.
func TestARollbackJournalDatabaseItCannotWriteIsRead(t *testing.T) {
	cases := []struct {
		name string
		// fail makes the store's writable open of the file at path fail,
		// conn being a connection on it, and gives the words ReadOnly
		// says why with.
		fail func(t *testing.T, path string, conn *sql.Conn) string
	}{
		{"a file it may not write", func(t *testing.T, path string, _ *sql.Conn) string {
			if os.Geteuid() == 0 {
				t.Skip("root writes a file whatever its mode")
			}
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o600) })
			return "readonly database"
		}},
		{"a writer holding it past the busy timeout", func(t *testing.T, _ string, conn *sql.Conn) string {
			if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.ExecContext(context.Background(), "ROLLBACK") })
			old := busyTimeout
			busyTimeout = 50 * time.Millisecond
			t.Cleanup(func() { busyTimeout = old })
			return "another writer holds the database"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := storeDir(t)
			s := openStore(t, dir)
			rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
			want(t, rc, err, StatusDone, "")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, File)
			conn := rollbackConnAt(t, path)
			var mode string
			if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&mode); err != nil || mode != "delete" {
				t.Fatalf("the rollback journal's mode: %q %v", mode, err)
			}
			words := tc.fail(t, path, conn)
			again := openStore(t, dir)
			ro, why := again.ReadOnly()
			if !ro || !strings.Contains(why, words) || strings.Contains(why, "nor read") {
				t.Fatalf("read-only %v: %s", ro, why)
			}
			// It reads what the file holds, the writable open still failing.
			if people, err := again.People(ctx); err != nil || len(people) != 1 || people[0].Name != "Alice" {
				t.Fatalf("a read: %+v %v", people, err)
			}
			if rc, found, err := again.Receipt(ctx, "test", "a1"); !found || err != nil || rc.Status != StatusDone {
				t.Fatalf("the receipt kept: %+v %v %v", rc, found, err)
			}
			rc, err = again.AddPerson(ctx, testOp("b1"), "Bob")
			want(t, rc, err, StatusFailed, RefusalReadOnly)
			if err := again.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
				t.Fatalf("the journal mode the store left: %q %v", mode, err)
			}
		})
	}
}

// A database in WAL mode, the mode the store leaves it in, that the store
// cannot write opens read-only (#444). SQLite opens a file the process may
// not write read-only without an error, and with the file already in WAL
// mode the connection's journal_mode(WAL) has nothing to write, so the
// open succeeds and only a write would show it: the store tries one at
// open, rolled back. It reads what the file holds, refuses a write as
// read-only rather than failing with SQLite's error, and leaves the file's
// mode as it found it; made writable again, it takes writes.
func TestAWALDatabaseItCannotWriteIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a file whatever its mode")
	}
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, File)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	again := openStore(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "readonly database") || strings.Contains(why, "nor read") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	var mode string
	if err := again.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("the journal mode: %q %v", mode, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("the file's mode after the open: %v %v", fi.Mode(), err)
	}
	if people, err := again.People(ctx); err != nil || len(people) != 1 || people[0].Name != "Alice" {
		t.Fatalf("a read: %+v %v", people, err)
	}
	if rc, found, err := again.Receipt(ctx, "test", "a1"); !found || err != nil || rc.Status != StatusDone {
		t.Fatalf("the receipt kept: %+v %v %v", rc, found, err)
	}
	rc, err = again.AddPerson(ctx, testOp("b1"), "Bob")
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if err := again.Checkpoint(ctx); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a checkpoint: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}

	// The file made writable again, the WAL and its index beside it are
	// not: SQLite made them with the file's mode, and a connection that
	// may not write the WAL does not write. The store says so, as it does
	// for the file.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	half := openStore(t, dir)
	if ro, why := half.ReadOnly(); !ro || !strings.Contains(why, "readonly database") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if err := half.Close(); err != nil {
		t.Fatal(err)
	}
	// All three writable, the store takes writes, and nothing of the
	// opens' tries is in the file.
	for _, p := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	owner := openStore(t, dir)
	if ro, why := owner.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	rc, err = owner.AddPerson(ctx, testOp("b1"), "Bob")
	want(t, rc, err, StatusDone, "")
	if people, err := owner.People(ctx); err != nil || len(people) != 2 {
		t.Fatalf("a read: %+v %v", people, err)
	}
}

func TestAWriterLettingGoWhileOpenWaitsLeavesItWritable(t *testing.T) {
	// The write lock is held on a fresh file in an empty directory, so the
	// opener that waits on it is the one that makes the schema.
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := rawConnAt(t, filepath.Join(dir, File))
	hold, err := raw.BeginTx(context.Background(), nil) // BEGIN IMMEDIATE: the write lock
	if err != nil {
		t.Fatal(err)
	}
	var made int
	if err := hold.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&made); err != nil || made != 0 {
		t.Fatalf("the file held is not fresh: %d entries %v", made, err)
	}
	// The lock is let go once Open is past the check and on its way to
	// the schema transaction: with the default busy timeout, a lock
	// still held then is waited for, never refused.
	released := make(chan error, 1)
	beforePrepareTx = func() {
		beforePrepareTx = nil
		go func() { released <- hold.Rollback() }()
	}
	t.Cleanup(func() { beforePrepareTx = nil })
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	// The opener that waited made the schema, whole and once.
	if n := count(t, s, "SELECT COUNT(*) FROM schema_version"); n != 1 {
		t.Fatalf("%d schema_version rows", n)
	}
	if got := tableNames(t, s); strings.Join(got, " ") != strings.Join(tables, " ") {
		t.Fatalf("tables %v", got)
	}
	rc, err := s.AddPerson(context.Background(), testOp("b1"), "Bob")
	want(t, rc, err, StatusDone, "")
	// A store opened after it finds the schema made.
	again := openStore(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("the next store is read-only: %s", why)
	}
	if n := count(t, again, "SELECT COUNT(*) FROM schema_version"); n != 1 {
		t.Fatalf("%d schema_version rows after the next open", n)
	}
	if people, err := again.People(context.Background()); err != nil || len(people) != 1 {
		t.Fatalf("the next store sees %+v %v", people, err)
	}
}

// A checkpoint another connection keeps from finishing is an error, never
// taken for done: wal_checkpoint says so in its result row and not as an
// error, and an export that trusted it would copy a file the log still
// holds commits for. Close says so too, and closes.
func TestACheckpointKeptFromFinishingIsAnError(t *testing.T) {
	old := busyTimeout
	busyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { busyTimeout = old })
	dir := storeDir(t)
	s := openStore(t, dir)
	ctx := context.Background()
	rc, err := s.AddPerson(ctx, testOp("a1"), "Alice")
	want(t, rc, err, StatusDone, "")
	reader, err := rawConn(t, s).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// A reader holds a snapshot while the store writes on.
	read := func() {
		t.Helper()
		var n int
		if _, err := reader.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM people").Scan(&n); err != nil || n == 0 {
			t.Fatalf("the reader read %d: %v", n, err)
		}
	}
	read()
	rc, err = s.AddPerson(ctx, testOp("a2"), "Bob")
	want(t, rc, err, StatusDone, "")
	if err := s.Checkpoint(ctx); err == nil || !strings.Contains(err.Error(), "kept the checkpoint from finishing") {
		t.Fatalf("a checkpoint a reader kept from finishing: %v", err)
	}
	// The reader lets go: the checkpoint finishes and empties the log.
	if _, err := reader.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatalf("the checkpoint once the reader let go: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, File+"-wal")); err == nil && fi.Size() != 0 {
		t.Fatalf("the WAL holds %d bytes after the checkpoint", fi.Size())
	}
	read()
	rc, err = s.AddPerson(ctx, testOp("a3"), "Carol")
	want(t, rc, err, StatusDone, "")
	if err := s.Close(); err == nil || !strings.Contains(err.Error(), "kept the checkpoint from finishing") {
		t.Fatalf("a Close whose checkpoint a reader kept from finishing: %v", err)
	}
	if _, err := s.People(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("a read after the Close: %v", err)
	}
	if _, err := reader.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
}

// The database is opened by a file: URI built from the absolute path: a
// relative directory is the one it names under the working directory
// (made relative, its first element would be the URI's authority, which
// SQLite refuses), and a directory whose name holds what a URI reads as
// syntax ('#', '?', '%', a space) is the directory it is.
func TestADirectoryOpensWhateverItsPathForm(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, dir := range []string{filepath.Join("data", "dispatch"), filepath.Join(root, "a #b ?c %d", "dispatch")} {
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		t.Cleanup(func() { s.Close() })
		if ro, why := s.ReadOnly(); ro {
			t.Fatalf("%s is read-only: %s", dir, why)
		}
		rc, err := s.AddPerson(context.Background(), testOp("a1"), "Alice")
		want(t, rc, err, StatusDone, "")
		if !filepath.IsAbs(s.Dir()) {
			t.Fatalf("the store's directory %q", s.Dir())
		}
		abs := dir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, dir)
		}
		if fi, err := os.Stat(filepath.Join(abs, File)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: the database %v %v", dir, fi, err)
		}
	}
	// Every form as the URI SQLite is given: the path escaped, after
	// file:// and its empty authority, a Windows drive (as filepath.Abs
	// and ToSlash give it there) after a '/'.
	for path, want := range map[string]string{
		"/srv/data/dispatch/dispatch.sqlite":   "file:///srv/data/dispatch/dispatch.sqlite",
		"/a #b ?c %d/dispatch/dispatch.sqlite": "file:///a%20%23b%20%3Fc%20%25d/dispatch/dispatch.sqlite",
		"C:/Users/x/dispatch/dispatch.sqlite":  "file:///C:/Users/x/dispatch/dispatch.sqlite",
	} {
		if uri, _, _ := strings.Cut(dsn(path, false), "?"); uri != want {
			t.Errorf("the URI of %s: %s, want %s", path, uri, want)
		}
	}
}

// The unique indexes are those of the keys never minted twice and
// nothing else: every other uniqueness is a rule in the operation, so
// that no index refuses a replicated add.
func TestTheUniqueIndexesAreTheKeysNeverMintedTwice(t *testing.T) {
	s := openStore(t, storeDir(t))
	got := uniqueIndexes(t, s.db)
	if strings.Join(got, "; ") != strings.Join(theUniqueIndexes, "; ") {
		t.Fatalf("unique indexes:\n got %v\nwant %v", got, theUniqueIndexes)
	}
}

// uniqueIndexes is every unique index of the database's tables but the
// FTS index's, as table(columns), in the order of their tables' names
// and their own.
func uniqueIndexes(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT m.name, i.name FROM sqlite_master m JOIN pragma_index_list(m.name) i
		WHERE m.type = 'table' AND i."unique" = 1 AND m.name NOT LIKE 'tickets\_fts%' ESCAPE '\' ORDER BY m.name, i.name`)
	if err != nil {
		t.Fatal(err)
	}
	var indexes [][2]string
	for rows.Next() {
		var table, index string
		if err := rows.Scan(&table, &index); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, [2]string{table, index})
	}
	rows.Close()
	var got []string
	for _, ix := range indexes {
		cols, err := db.Query("SELECT name FROM pragma_index_info(?) ORDER BY seqno", ix[1])
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var c string
			cols.Scan(&c)
			names = append(names, c)
		}
		cols.Close()
		got = append(got, ix[0]+"("+strings.Join(names, ", ")+")")
	}
	return got
}

// theUniqueIndexes is the unique indexes of the schema: every row's own
// id and the keys never minted twice, and nothing else. Version 2 adds
// none but its tables' primary keys.
var theUniqueIndexes = []string{
	"attachments(id)", "changes(actor, op_id, n)", "comments(id)", "labels(id)", "launch_files(launch_id, sha256)", "launches(id)",
	"links(id)", "milestones(id)", "ops(actor, id)", "people(name)", "plans(id)", "projects(id)", "projects(key)", "relations(id)",
	"ticket_assignees(ticket_id, actor)",
	"ticket_labels(ticket_id, label_id)", "tickets(id)", "tickets(key)", "tickets(project_id, number)", "uploads(id)", "views(id)",
}

func TestAClosedStoreRefusesEverything(t *testing.T) {
	s := openStore(t, storeDir(t))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPerson(context.Background(), testOp("a1"), "Alice"); !errors.Is(err, ErrClosed) {
		t.Fatalf("a write: %v", err)
	}
	if _, err := s.People(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("a read: %v", err)
	}
	if err := s.Checkpoint(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("a checkpoint: %v", err)
	}
	if _, err := s.prune(context.Background(), time.Now()); !errors.Is(err, ErrClosed) {
		t.Fatalf("a pruning: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// The receipts are pruned by a goroutine Open starts, every pruneEvery
// (a day; a millisecond here), seen on the pruned counter, which the test
// polls; Close stops it before the database closes. Run with
// -count=30: nothing here waits on the clock but the goroutine's ticks.
func TestThePruningGoroutinePrunesUntilClose(t *testing.T) {
	dir := storeDir(t)
	s, err := openWith(dir, func(s *Store) { s.pruneEvery = time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if s.pruneEvery != time.Millisecond || s.stopPruning == nil {
		t.Fatalf("the goroutine: every %v, started %v", s.pruneEvery, s.stopPruning != nil)
	}
	// 20,001 receipts in one statement: the oldest is the one past the
	// bound.
	if _, err := s.db.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO ops (actor, id, intent, fingerprint, status, receipt, created_at)
		SELECT 'test', printf('r%05d', i), 'x', 'f', 'done', '{}', printf('2026-10-28T12:00:00.%09dZ', i) FROM seq`, maxReceipts); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for s.pruned.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the goroutine never pruned")
		}
		time.Sleep(time.Millisecond)
	}
	// Close ends the goroutine, a pruning in flight among it, before the
	// database closes.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.pruneDone:
	default:
		t.Fatal("the goroutine outlived Close")
	}
	// What it left, read on a connection of the test's: the newest
	// receipts, the oldest gone.
	raw := rawConnAt(t, filepath.Join(dir, File))
	var kept, oldest int
	if err := raw.QueryRow("SELECT COUNT(*), COUNT(*) FILTER (WHERE id = 'r00000') FROM ops").Scan(&kept, &oldest); err != nil {
		t.Fatal(err)
	}
	if kept != maxReceipts || oldest != 0 {
		t.Fatalf("%d receipts kept, the oldest %d times", kept, oldest)
	}
	// A store opened plainly prunes every day; one opened read-only takes
	// no writes and starts no goroutine.
	other := storeDir(t)
	plain, err := Open(other)
	if err != nil {
		t.Fatal(err)
	}
	if plain.pruneEvery != defaultPruneEvery || defaultPruneEvery != 24*time.Hour || plain.stopPruning == nil {
		t.Fatalf("a plain store prunes every %v, started %v", plain.pruneEvery, plain.stopPruning != nil)
	}
	if _, err := plain.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	plain.Close()
	ro := openStore(t, other)
	if readOnly, _ := ro.ReadOnly(); !readOnly || ro.stopPruning != nil {
		t.Fatalf("a read-only store: read-only %v, pruning %v", readOnly, ro.stopPruning != nil)
	}
}
