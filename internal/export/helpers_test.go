package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// fixture is a data directory as a running server holds it: its store,
// its ledger and its Dispatch store open, the runs' records beside them.
type fixture struct {
	dir     string
	st      *store.Store
	l       *ledger.Ledger
	ds      *dispatch.Store
	records *crew.Records
	ops     int
}

// newFixture makes an empty data directory and opens what a server would.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ds, err := dispatch.Open(filepath.Join(dir, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	records, err := crew.NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(records.Close)
	return &fixture{dir: dir, st: st, l: l, ds: ds, records: records}
}

// source is the fixture as an export's source: the snapshots on its own
// connections, as the server's are; the catalog copied as it is.
func (f *fixture) source() export.Source {
	return export.Source{Dir: f.dir, Snapshot: func(ctx context.Context, rel, dst string) error {
		switch rel {
		case export.LedgerPath:
			return f.l.Snapshot(ctx, dst)
		case export.DispatchPath:
			return f.ds.Snapshot(ctx, dst)
		}
		return fmt.Errorf("no database %s", rel)
	}}
}

// op is a new Dispatch operation of the admin.
func (f *fixture) op() dispatch.Op {
	f.ops++
	return dispatch.Op{Actor: dispatch.ActorAdmin, ID: fmt.Sprintf("op-%d", f.ops), By: "Ada"}
}

// run makes a managed run in the ledger in the given state, with an
// event, and writes its record as the server's syncRecords would.
func (f *fixture) run(t *testing.T, id, state string) {
	t.Helper()
	f.ledgerRun(t, id, state)
	f.record(t, id)
}

// ledgerRun makes a managed run in the ledger alone, with no record.
func (f *fixture) ledgerRun(t *testing.T, id, state string) {
	t.Helper()
	if _, err := f.l.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: id, CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: state, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Event("run.created", map[string]string{"by": "test"})
	}); err != nil {
		t.Fatal(err)
	}
}

// record writes the run's record from the ledger.
func (f *fixture) record(t *testing.T, id string) {
	t.Helper()
	from, ok, err := crew.SQLLedger(f.l).Load(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("load %s: %v %v", id, ok, err)
	}
	rec, _, err := f.records.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.records.Save(crew.Recovered(rec, from)); err != nil {
		t.Fatal(err)
	}
}

// finish moves the run to finished, in one transition.
func (f *fixture) finish(t *testing.T, id string) {
	t.Helper()
	// Read before the transition: the ledger's one connection is the
	// transaction's while it runs.
	row, ok, err := f.l.Run(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("run %s: %v %v", id, ok, err)
	}
	if _, err := f.l.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeSucceeded, tx.Now()
		if err := tx.PutRun(row); err != nil {
			return err
		}
		return tx.Event("run.finished", nil)
	}); err != nil {
		t.Fatal(err)
	}
}

// ticket makes a project CND with a ticket, a comment on it and the given
// attachments (name to content), and returns the ticket's key and the
// attachments.
func (f *fixture) ticket(t *testing.T, files ...[2]string) (string, []dispatch.Attachment) {
	t.Helper()
	ctx := context.Background()
	if rc, err := f.ds.CreateProject(ctx, f.op(), dispatch.NewProject{Key: "CND", Name: "Conductor"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("project: %+v %v", rc, err)
	}
	rc, err := f.ds.CreateTicket(ctx, f.op(), dispatch.NewTicket{Project: "CND", Title: "Export it"})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("ticket: %+v %v", rc, err)
	}
	key := rc.Key
	if rc, err := f.ds.AddComment(ctx, f.op(), key, "and restore it"); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("comment: %+v %v", rc, err)
	}
	var out []dispatch.Attachment
	for _, file := range files {
		rc, a, err := f.ds.AddAttachment(ctx, f.op(), key, file[0], "text/plain", strings.NewReader(file[1]))
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("attachment %s: %+v %v", file[0], rc, err)
		}
		out = append(out, a)
	}
	return key, out
}

// put writes a file of the data directory, its directories made.
func put(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// archive writes src's archive and returns it with its manifest.
func archive(t *testing.T, src export.Source) ([]byte, export.Manifest) {
	t.Helper()
	var buf bytes.Buffer
	m, err := export.Write(context.Background(), &buf, src)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.Bytes(), m
}

// restore installs the archive b into dir (Restore).
func restore(b []byte, dir string) (export.Manifest, error) {
	return export.Restore(context.Background(), bytes.NewReader(b), int64(len(b)), dir)
}

// entries is the archive's entries' names, in order, and their bytes.
func entries(t *testing.T, b []byte) ([]string, map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		files[f.Name] = data
	}
	return names, files
}

// item is an entry of an archive a test makes by hand.
type item struct {
	name string
	data []byte
	mode fs.FileMode // a regular file's 0600 when zero
}

// zipOf is the items as a zip file, in order.
func zipOf(t *testing.T, items []item) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, it := range items {
		h := &zip.FileHeader{Name: it.name, Method: zip.Deflate}
		mode := it.mode
		if mode == 0 {
			mode = 0o600
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(it.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fileOf is what a manifest says of data.
func fileOf(path string, data []byte) export.File {
	sum := sha256.Sum256(data)
	return export.File{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

// manifestItem is m as the archive's manifest entry.
func manifestItem(t *testing.T, m export.Manifest) item {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return item{name: export.ManifestName, data: b}
}

// craft is an archive of the files (path to content, in the order given),
// each under data/ and listed in its manifest, the manifest last.
func craft(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	m := export.Manifest{Format: export.Format, Version: "test", CreatedAt: time.Now().UTC()}
	var items []item
	for _, f := range files {
		items = append(items, item{name: export.DataDir + f[0], data: []byte(f[1])})
		m.Files = append(m.Files, fileOf(f[0], []byte(f[1])))
	}
	return zipOf(t, append(items, manifestItem(t, m)))
}

// listing is every path under dir, a directory's with a slash, a file's
// with its content, a link's with its target: what a refused restore must
// leave as it was.
func listing(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
		case d.IsDir():
			out[rel+"/"] = ""
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameListing fails t unless dir lists as want.
func sameListing(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := listing(t, dir)
	for p, c := range want {
		if g, ok := got[p]; !ok || g != c {
			t.Errorf("%s: changed or gone (had %d bytes)", p, len(c))
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("%s: appeared", p)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

// dump is every row of every table of the database at path, as text,
// sorted: a database compared row for row.
func dump(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables []string
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	var out []string
	for _, table := range tables {
		rows, err := db.Query(fmt.Sprintf("SELECT * FROM %q", table))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var parts []string
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				parts = append(parts, fmt.Sprintf("%s=%q", cols[i], fmt.Sprint(v)))
			}
			out = append(out, table+": "+strings.Join(parts, " "))
		}
		rows.Close()
	}
	slices.Sort(out)
	return out
}

// token is the workbench token of the servers the tests make.
const token = "export-test-token"

// served is a server over a data directory, as `conductor serve` makes
// one, and its HTTP test server.
type served struct {
	srv *api.Server
	hs  *httptest.Server
}

// plain is the one agent of a config with none of the test's (a catalog
// holds one at least).
var plain = catalog.Agent{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}

// serve starts a server over dir whose configured catalog is agents (and
// plain), and ends it with the test.
func serve(t *testing.T, dir string, agents ...catalog.Agent) *served {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.WorkbenchToken = token
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.PublicURL = "http://example.test"
	cfg.Dev = true
	cfg.DataDir = dir
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append([]catalog.Agent{plain}, agents...)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, cat, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	s := &served{srv: srv, hs: httptest.NewServer(srv.Handler())}
	t.Cleanup(s.stop)
	return s
}

// stop ends the server: its listener closed, its ledger's lock and its
// stores let go.
func (s *served) stop() {
	s.hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.srv.WaitBackground(ctx)
}

// export asks the server for its archive.
func (s *served) export(t *testing.T) []byte {
	t.Helper()
	req, err := http.NewRequest("GET", s.hs.URL+"/api/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("GET /api/export: %d %s: %.200s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	return b
}

// overlayIn reads dir's catalog.json.
func overlayIn(t *testing.T, dir string) catalog.Overlay {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ov catalog.Overlay
	if err := json.Unmarshal(b, &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}
