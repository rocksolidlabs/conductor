package cli

import (
	"archive/zip"
	"bytes"
	"context"
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
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// exportToken is the workbench token of the servers these tests make.
const exportToken = "export-cli-test-token"

// catAgent is the one agent of the servers' configured catalog.
var catAgent = catalog.Agent{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}

// source is a server over a data directory, as conductor serve makes one,
// with its HTTP test server: what conductor export asks.
type source struct {
	dir  string
	root string // the allowed root, where its crews work
	srv  *api.Server
	hs   *httptest.Server
	once sync.Once
}

// serveSource starts a server over dir whose configured catalog is
// catAgent and agents, and stops it with the test.
func serveSource(t *testing.T, dir string, agents ...catalog.Agent) *source {
	t.Helper()
	root := realTempDir(t)
	cfg := config.Defaults()
	cfg.WorkbenchToken = exportToken
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.PublicURL = "http://example.test"
	cfg.DataDir = dir
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: append([]catalog.Agent{catAgent}, agents...)})
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
	s := &source{dir: dir, root: root, srv: srv, hs: httptest.NewServer(srv.Handler())}
	t.Cleanup(s.stop)
	return s
}

// stop ends the server: its listener closed, its ledger's owner lock and
// its stores let go.
func (s *source) stop() {
	s.once.Do(func() {
		s.hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.srv.WaitBackground(ctx)
	})
}

// do sends one admin request and decodes its reply into out (nil: none),
// failing the test on any status but want.
func (s *source) do(t *testing.T, method, path string, body any, want int, out any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.hs.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+exportToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
}

// crew saves a crew named name and returns its id.
func (s *source) crew(t *testing.T, name string) string {
	t.Helper()
	var c struct {
		Crew struct {
			ID string `json:"id"`
		} `json:"crew"`
	}
	s.do(t, "POST", "/api/crews", map[string]any{
		"name": name, "cwd": s.root, "where": "server", "isolation": "none",
		"members": []any{map[string]any{"name": "lead", "agentId": "cat", "start": map[string]any{"when": "immediately"}}},
	}, http.StatusCreated, &c)
	return c.Crew.ID
}

// sourceDir is a data directory a server made and let go: a crew saved,
// its databases there, and a finished managed run in its ledger with its
// record. It returns the directory and the crew's id.
func sourceDir(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(realTempDir(t), "data")
	s := serveSource(t, dir)
	id := s.crew(t, "Export crew")
	s.stop()
	finishedRun(t, dir, "team-0000d0e1")
	return dir, id
}

// finishedRun makes a managed run in the ledger of dir, a data directory no
// server runs on, finishes it and writes its record, as the server's
// syncRecords would.
func finishedRun(t *testing.T, dir, id string) {
	t.Helper()
	ctx := context.Background()
	l, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Transition(ctx, id, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: id, CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: ledger.RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Event("run.created", map[string]string{"by": "test"})
	}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := l.Run(ctx, id)
	if err != nil || !ok {
		t.Fatalf("run %s: %v %v", id, ok, err)
	}
	if _, err := l.Transition(ctx, id, func(tx *ledger.Tx) error {
		row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeSucceeded, tx.Now()
		if err := tx.PutRun(row); err != nil {
			return err
		}
		return tx.Event("run.finished", nil)
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := crew.NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	from, ok, err := crew.SQLLedger(l).Load(ctx, id)
	if err != nil || !ok {
		t.Fatalf("load %s: %v %v", id, ok, err)
	}
	rec, _, err := records.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Save(crew.Recovered(rec, from)); err != nil {
		t.Fatal(err)
	}
}

// exportCmd runs conductor export.
func exportCmd(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	return runWith(t, runExport, args...)
}

// restoreCmd runs conductor restore.
func restoreCmd(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	return runWith(t, runRestore, args...)
}

// archiveFiles is the archive at path: its entries' names in order and
// their bytes.
func archiveFiles(t *testing.T, path string) ([]string, map[string][]byte) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("%s is no archive: %v", path, err)
	}
	defer zr.Close()
	var names []string
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		files[f.Name] = b
	}
	return names, files
}

// tree is every path under dir, a directory's ending in a slash and a
// file's with its content, a link's with where it goes: what a refused
// command must leave as it was.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = ""
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			to, err := os.Readlink(p)
			out[filepath.ToSlash(rel)] = "-> " + to
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails the test when dir is not as want.
func sameTree(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := tree(t, dir)
	for p := range want {
		if _, ok := got[p]; !ok {
			t.Errorf("%s: %s is gone", dir, p)
		} else if got[p] != want[p] {
			t.Errorf("%s: %s changed", dir, p)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("%s: %s appeared", dir, p)
		}
	}
}

// writeFile writes content to rel under dir, its directories made.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// exportOf writes the archive of dir, a data directory no server runs on,
// to a new file and returns its path.
func exportOf(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(realTempDir(t), "archive.zip")
	if code, stdout, stderr, err := exportCmd(t, "--data-dir", dir, "--out", out); code != 0 || err != nil {
		t.Fatalf("export: %d %v\n%s%s", code, err, stdout, stderr)
	}
	return out
}

// conductor export asks the running server: the archive it streams is
// written to --out, 0600, whole (the manifest last), and restores; the
// token is sent and never printed.
func TestExportThroughTheServer(t *testing.T) {
	clearConductorEnv(t)
	dir := filepath.Join(realTempDir(t), "data")
	s := serveSource(t, dir)
	id := s.crew(t, "Through the server")
	out := filepath.Join(realTempDir(t), "through.zip")
	code, stdout, stderr, err := exportCmd(t, "--server", s.hs.URL, "--token", exportToken, "--out", out)
	if code != 0 || err != nil {
		t.Fatalf("export: %d %v\n%s%s", code, err, stdout, stderr)
	}
	noToken(t, exportToken, stdout, stderr, err)
	if !strings.HasPrefix(stdout, "wrote "+out+": ") {
		t.Fatalf("stdout: %q", stdout)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", out, fi, err)
	}
	names, _ := archiveFiles(t, out)
	if names[len(names)-1] != export.ManifestName {
		t.Fatalf("the last entry is %s", names[len(names)-1])
	}
	for _, want := range []string{export.DataDir + "crews/" + id + ".json", export.DataDir + export.LedgerPath, export.DataDir + export.DispatchPath} {
		if !slices.Contains(names, want) {
			t.Errorf("the archive holds no %s: %v", want, names)
		}
	}
	// The token through the environment, as conductor up takes it.
	t.Setenv("CONDUCTOR_SERVER", s.hs.URL)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", exportToken)
	again := filepath.Join(realTempDir(t), "again.zip")
	if code, stdout, stderr, err := exportCmd(t, "--out", again); code != 0 || err != nil {
		t.Fatalf("export from the environment: %d %v\n%s%s", code, err, stdout, stderr)
	}
	s.stop()

	into := filepath.Join(realTempDir(t), "into")
	if code, stdout, stderr, err := restoreCmd(t, out, "--data-dir", into); code != 0 || err != nil {
		t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(into, "crews", id+".json")); err != nil || !bytes.Contains(b, []byte("Through the server")) {
		t.Fatalf("the restored crew: %s %v", b, err)
	}

	// A wrong token is the server's refusal, and nothing is kept.
	s2 := serveSource(t, filepath.Join(realTempDir(t), "other"))
	refused := filepath.Join(realTempDir(t), "refused.zip")
	code, _, _, err = exportCmd(t, "--server", s2.hs.URL, "--token", "wrong", "--out", refused)
	if code != 1 || err == nil {
		t.Fatalf("a wrong token: %d %v", code, err)
	}
	if _, err := os.Stat(refused); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused export left %s: %v", refused, err)
	}
	// A plain server on another machine is refused before any request.
	code, _, _, err = exportCmd(t, "--server", "http://192.0.2.1:8080", "--token", exportToken, "--out", refused)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "--insecure") {
		t.Fatalf("a plain server elsewhere: %d %v", code, err)
	}
}

// A reply cut short (the server failed after its first byte) is no
// archive: the command says so and keeps nothing.
func TestExportThroughTheServerCutShort(t *testing.T) {
	clearConductorEnv(t)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("PK\x03\x04 cut short"))
	}))
	t.Cleanup(hs.Close)
	out := filepath.Join(realTempDir(t), "short.zip")
	code, _, _, err := exportCmd(t, "--server", hs.URL, "--token", exportToken, "--out", out)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a cut reply: %d %v", code, err)
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s kept: %v", out, err)
	}
}

// conductor export --data-dir reads a stopped server's directory itself:
// the archive is whole and restores; while a server holds the ledger's
// owner lock, or restore.lock is there, it is refused, naming the server
// form, and nothing is written.
func TestExportOffline(t *testing.T) {
	clearConductorEnv(t)
	dir, id := sourceDir(t)
	out := exportOf(t, dir)
	names, files := archiveFiles(t, out)
	for _, want := range []string{export.DataDir + "crews/" + id + ".json", export.DataDir + "runs/team-0000d0e1.json", export.DataDir + export.LedgerPath, export.DataDir + export.DispatchPath} {
		if !slices.Contains(names, want) {
			t.Errorf("the archive holds no %s: %v", want, names)
		}
	}
	var m export.Manifest
	if err := json.Unmarshal(files[export.ManifestName], &m); err != nil || m.Format != export.Format {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	into := filepath.Join(realTempDir(t), "into")
	if code, stdout, stderr, err := restoreCmd(t, out, "--data-dir", into); code != 0 || err != nil {
		t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
	}
	l, err := ledger.Open(filepath.Join(into, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := l.Run(context.Background(), "team-0000d0e1")
	l.Close()
	if err != nil || !ok || row.State != ledger.RunFinished {
		t.Fatalf("the restored ledger's run: %+v %v %v", row, ok, err)
	}

	// A server holds the owner lock: refused.
	held, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(realTempDir(t), "refused.zip")
	code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", refused)
	held.Close()
	if code != 1 || err == nil || !strings.Contains(err.Error(), "a server is running") || !strings.Contains(err.Error(), "--server") {
		t.Fatalf("with the owner lock held: %d %v", code, err)
	}
	if _, err := os.Stat(refused); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused export wrote %s: %v", refused, err)
	}
	// A restore runs, or a server starts: refused.
	before := tree(t, dir)
	writeFile(t, dir, export.LockFile, "pid 1 since then\n")
	code, _, _, err = exportCmd(t, "--data-dir", dir, "--out", refused)
	if code != 1 || err == nil || !strings.Contains(err.Error(), export.LockFile) {
		t.Fatalf("with restore.lock there: %d %v", code, err)
	}
	os.Remove(filepath.Join(dir, export.LockFile))
	sameTree(t, dir, before)

	// The flags of the two forms do not mix.
	for _, args := range [][]string{{"--data-dir", dir, "--server", "http://localhost:1"}, {"--data-dir", dir, "--token", "x"}, {"--config", "c.json"}, {"--data-dir", ""}, {"extra"}} {
		if code, _, _, err := exportCmd(t, args...); code != 2 || err == nil {
			t.Errorf("%v: %d %v", args, code, err)
		}
	}
}

// conductor export --data-dir takes a data directory only: one with no
// server data (a home directory, a project) is refused, with no archive
// written and the directory as it was.
func TestExportOfflineRefusesNoServerData(t *testing.T) {
	clearConductorEnv(t)
	dir := realTempDir(t)
	writeFile(t, dir, ".ssh/id_ed25519", "a key")
	writeFile(t, dir, "project/main.go", "package main")
	writeFile(t, dir, "hooks/x", "conductor host's")
	before := tree(t, dir)
	out := filepath.Join(realTempDir(t), "home.zip")
	code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", out)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "holds no server data") {
		t.Fatalf("a directory with no server data: %d %v", code, err)
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused export wrote %s: %v", out, err)
	}
	sameTree(t, dir, before)
}

// The archive never goes inside the data directory the export reads (it
// would read itself as it grows): --out there, the default name from the
// data directory as the current directory (--data-dir .), and an --out
// through a link to it are refused, nothing written.
func TestExportOfflineOutsideTheDataDir(t *testing.T) {
	clearConductorEnv(t)
	dir, _ := sourceDir(t)
	before := tree(t, dir)
	link := filepath.Join(realTempDir(t), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{filepath.Join(dir, "a.zip"), filepath.Join(dir, "crews", "a.zip"), filepath.Join(link, "a.zip")} {
		code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", out)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "inside the data directory") {
			t.Fatalf("--out %s: %d %v", out, code, err)
		}
	}
	t.Chdir(dir)
	if code, _, _, err := exportCmd(t, "--data-dir", "."); code != 1 || err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Fatalf("--data-dir . from it: %d %v", code, err)
	}
	sameTree(t, dir, before)
	// Beside it is outside.
	beside := filepath.Join(filepath.Dir(dir), "beside.zip")
	if code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", beside); code != 0 || err != nil {
		t.Fatalf("beside the data directory: %d %v", code, err)
	}
}

// A ".." after a link is taken from where the link goes, as the system
// takes it, never by cleaning the path: with sub a link to the data
// directory's crews, sub/../a.zip is inside it (and so is a path that
// climbs back into crews through it), whether --out is absolute or
// relative; with up a link to the data directory, up/../a.zip is beside
// it, and the archive is written there, not beside the link.
func TestExportOfflineLinkThenDotDot(t *testing.T) {
	clearConductorEnv(t)
	dir, _ := sourceDir(t)
	links := realTempDir(t)
	sub := filepath.Join(links, "sub")
	up := filepath.Join(links, "up")
	if err := os.Symlink(filepath.Join(dir, "crews"), sub); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, up); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	sep := string(os.PathSeparator)
	inside := []string{
		sub + sep + ".." + sep + "a.zip",
		sub + sep + ".." + sep + "crews" + sep + ".." + sep + "crews" + sep + "a.zip",
		sub + sep + ".." + sep + "crews" + sep + "a.zip",
	}
	for _, out := range inside {
		code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", out)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "inside the data directory") {
			t.Fatalf("--out %s: %d %v", out, code, err)
		}
	}
	t.Chdir(links)
	if code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", "sub"+sep+".."+sep+"a.zip"); code != 1 || err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Fatalf("relative --out: %d %v", code, err)
	}
	sameTree(t, dir, before)
	if _, err := os.Lstat(filepath.Join(links, "a.zip")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an archive beside the links: %v", err)
	}

	out := up + sep + ".." + sep + "beside.zip"
	if code, stdout, stderr, err := exportCmd(t, "--data-dir", dir, "--out", out); code != 0 || err != nil {
		t.Fatalf("--out %s: %d %v\n%s%s", out, code, err, stdout, stderr)
	}
	if names, _ := archiveFiles(t, filepath.Join(filepath.Dir(dir), "beside.zip")); len(names) == 0 {
		t.Fatal("the archive beside the data directory is empty")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(links), "beside.zip")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an archive beside the links' directory: %v", err)
	}

	// An --out that names a directory is refused before anything is read.
	for _, out := range []string{links + sep, links + sep + ".", links + sep + ".."} {
		if code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", out); code != 2 || err == nil || !strings.Contains(err.Error(), "names a directory") {
			t.Fatalf("--out %s: %d %v", out, code, err)
		}
	}
}

// --data-dir is resolved once, as the system resolves it, and that one
// directory is checked against --out, reserved and read: a ".." after a
// child that is not there is refused, not cleaned into the data directory
// with an --out inside it; with sub a link from the data directory to a
// directory elsewhere, dir/sub/.. is that elsewhere (which holds no server
// data), never dir with the archive inside it; with crews a link from
// beside the data directory to its crews, crews/.. is the data directory,
// exported whole, and an --out in it is inside.
func TestExportOfflineDataDirAsTheSystemResolvesIt(t *testing.T) {
	clearConductorEnv(t)
	dir, id := sourceDir(t)
	elsewhere := filepath.Join(realTempDir(t), "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "inner"), filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	links := realTempDir(t)
	if err := os.Symlink(filepath.Join(dir, "crews"), filepath.Join(links, "crews")); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	sep := string(os.PathSeparator)
	for _, c := range []struct{ dataDir, why string }{
		{dir + sep + "missing" + sep + "..", "the data directory"},
		{dir + sep + "sub" + sep + "..", "holds no server data"},
	} {
		out := filepath.Join(dir, "zzz.zip")
		code, _, _, err := exportCmd(t, "--data-dir", c.dataDir, "--out", out)
		if code != 1 || err == nil || !strings.Contains(err.Error(), c.why) {
			t.Fatalf("--data-dir %s --out %s: %d %v", c.dataDir, out, code, err)
		}
		if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("--data-dir %s wrote %s: %v", c.dataDir, out, err)
		}
	}
	sameTree(t, dir, before)

	viaLink := links + sep + "crews" + sep + ".."
	inside := filepath.Join(dir, "a.zip")
	if code, _, _, err := exportCmd(t, "--data-dir", viaLink, "--out", inside); code != 1 || err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Fatalf("--data-dir %s --out %s: %d %v", viaLink, inside, code, err)
	}
	sameTree(t, dir, before)
	out := filepath.Join(links, "a.zip")
	if code, stdout, stderr, err := exportCmd(t, "--data-dir", viaLink, "--out", out); code != 0 || err != nil {
		t.Fatalf("--data-dir %s --out %s: %d %v\n%s%s", viaLink, out, code, err, stdout, stderr)
	}
	if names, _ := archiveFiles(t, out); !slices.Contains(names, export.DataDir+"crews/"+id+".json") {
		t.Fatalf("the archive of %s: %v", viaLink, names)
	}
}

// An export from a data directory holds its reservation while it reads:
// paused there, restore.lock names the export, a serve and a restore on
// the directory are refused; let go, the archive is whole, without
// restore.lock, which is gone. A serve that holds the reservation refuses
// the export in turn.
func TestExportOfflineHoldsTheReservation(t *testing.T) {
	clearConductorEnv(t)
	dir, id := sourceDir(t)
	cfg := dataDirConfig(t, dir)
	archive := exportOf(t, dir)
	g := newGate()
	old := exportReserved
	exportReserved = func(string) { g.hold() }
	t.Cleanup(func() { exportReserved = old })
	out := filepath.Join(realTempDir(t), "held.zip")
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		code, err := runExport(context.Background(), []string{"--data-dir", dir, "--out", out}, io.Discard, io.Discard)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d", code)
		}
		done <- err
	}()
	t.Cleanup(func() { g.release(); <-finished })
	g.waitReached(t, done)
	lock := filepath.Join(dir, export.LockFile)
	if b, err := os.ReadFile(lock); err != nil || !strings.HasSuffix(string(b), ": export\n") {
		t.Fatalf("restore.lock while the export reads: %q %v", b, err)
	}
	serveRefused(t, cfg)
	code, _, _, err := restoreCmd(t, archive, "--data-dir", dir)
	if code != 1 || !errors.Is(err, export.ErrReserved) {
		t.Fatalf("a restore while the export reads: %d %v", code, err)
	}
	g.release()
	if err := <-done; err != nil {
		t.Fatalf("the export: %v", err)
	}
	if _, err := os.Stat(lock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore.lock left: %v", err)
	}
	names, _ := archiveFiles(t, out)
	if slices.Contains(names, export.DataDir+export.LockFile) || !slices.Contains(names, export.DataDir+"crews/"+id+".json") {
		t.Fatalf("the archive: %v", names)
	}

	// A serve inside its reservation: the export is refused, naming it.
	g2 := newGate()
	oldServe := serveReserved
	serveReserved = func(string) { g2.hold() }
	t.Cleanup(agents.ForgetBinary())
	var logs syncBuffer
	sv := startServing(t, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &logs)
	t.Cleanup(func() { g2.release(); sv.stop(); serveReserved = oldServe })
	deadline, wait := startDeadline(t)
	select {
	case <-g2.reached:
	case <-deadline:
		t.Fatalf("serve did not reach its reservation in %s:\n%s", wait, logs.String())
	}
	refused := filepath.Join(realTempDir(t), "refused.zip")
	code, _, _, err = exportCmd(t, "--data-dir", dir, "--out", refused)
	if code != 1 || !errors.Is(err, export.ErrReserved) || !strings.Contains(err.Error(), ": serve") {
		t.Fatalf("an export while serve starts: %d %v", code, err)
	}
	if _, err := os.Stat(refused); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused export wrote %s: %v", refused, err)
	}
}

// A serve or an export killed inside its reservation leaves restore.lock
// with no process holding it: the next serve takes it over and starts,
// letting it go as it does its own. A restore's, left by a restore cut
// short, still refuses the serve, naming the file to remove.
func TestServeAfterAKilledServesReservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no flock: restore.lock is its file alone")
	}
	clearConductorEnv(t)
	for _, by := range []string{"serve", "export"} {
		t.Run(by, func(t *testing.T) {
			clearConductorEnv(t)
			into := filepath.Join(realTempDir(t), "data")
			writeFile(t, into, export.LockFile, "pid 999999 since 2026-10-01T00:00:00Z: "+by+"\n")
			cfg := dataDirConfig(t, into)
			logs := serveUntilListening(t, "--config", cfg)
			if len(logLines(logs, "conductor serving")) != 1 {
				t.Fatalf("serve did not start:\n%s", logs)
			}
			if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("serve kept restore.lock: %v", err)
			}
		})
	}
	into := filepath.Join(realTempDir(t), "data")
	writeFile(t, into, export.LockFile, "pid 999999 since 2026-10-01T00:00:00Z: restore\n")
	serveRefused(t, dataDirConfig(t, into))
}

// An export never writes over a file, through the server or offline: an
// existing --out refuses it and keeps its bytes; with no --out the archive
// is conductor-export-<UTC time>.zip in the current directory.
func TestExportNeverOverwrites(t *testing.T) {
	clearConductorEnv(t)
	dir, _ := sourceDir(t)
	out := filepath.Join(realTempDir(t), "taken.zip")
	writeFile(t, filepath.Dir(out), "taken.zip", "mine")
	code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", out)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "never writes over") {
		t.Fatalf("offline over a file: %d %v", code, err)
	}
	s := serveSource(t, filepath.Join(realTempDir(t), "served"))
	code, _, _, err = exportCmd(t, "--server", s.hs.URL, "--token", exportToken, "--out", out)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "never writes over") {
		t.Fatalf("through the server over a file: %d %v", code, err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "mine" {
		t.Fatalf("the file was written over: %q %v", b, err)
	}

	cwd := realTempDir(t)
	t.Chdir(cwd)
	if code, stdout, stderr, err := exportCmd(t, "--data-dir", dir); code != 0 || err != nil {
		t.Fatalf("export: %d %v\n%s%s", code, err, stdout, stderr)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the current directory: %v %v", entries, err)
	}
	name := entries[0].Name()
	at, err := time.Parse("20060102T150405Z", strings.TrimSuffix(strings.TrimPrefix(name, "conductor-export-"), ".zip"))
	if err != nil || !strings.HasPrefix(name, "conductor-export-") || time.Since(at) > time.Hour || time.Until(at) > time.Minute {
		t.Fatalf("the default name %q: %v", name, err)
	}
}

// conductor export says on stderr that the archive holds env values, which
// it does in full.
func TestExportSaysEnvIsKept(t *testing.T) {
	clearConductorEnv(t)
	dir := filepath.Join(realTempDir(t), "data")
	s := serveSource(t, dir)
	s.do(t, "POST", "/api/catalog", map[string]any{"id": "mine", "name": "Mine", "command": []string{"/bin/cat"}, "env": map[string]string{"KEY": "mine-0123"}}, http.StatusOK, nil)
	out := filepath.Join(realTempDir(t), "env.zip")
	code, stdout, stderr, err := exportCmd(t, "--server", s.hs.URL, "--token", exportToken, "--out", out)
	if code != 0 || err != nil {
		t.Fatalf("export: %d %v\n%s%s", code, err, stdout, stderr)
	}
	if !strings.Contains(stderr, envKept) {
		t.Fatalf("stderr: %q", stderr)
	}
	_, files := archiveFiles(t, out)
	if !bytes.Contains(files[export.DataDir+"catalog.json"], []byte("mine-0123")) {
		t.Fatalf("the archive's catalog: %s", files[export.DataDir+"catalog.json"])
	}
	s.stop()
	offline := filepath.Join(realTempDir(t), "offline.zip")
	if code, _, stderr, err := exportCmd(t, "--data-dir", dir, "--out", offline); code != 0 || err != nil || !strings.Contains(stderr, envKept) {
		t.Fatalf("offline: %d %v %q", code, err, stderr)
	}
}

// --data-dir with --config: a saved agent over a configured one, stored
// with the mask for its env value and without its adapter, is in the
// archive with the configured value and adapter, as the server's own
// export resolves it.
func TestExportOfflineResolvesCatalog(t *testing.T) {
	clearConductorEnv(t)
	const secret = "configured-0123456789"
	dir := filepath.Join(realTempDir(t), "data")
	ov := catalog.Overlay{Agents: []catalog.Agent{{ID: "keyed", Name: "Keyed", Command: []string{"/bin/cat"}, Env: map[string]string{"TOKEN": catalog.RedactedValue}}}}
	b, err := store.Encode(ov)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "catalog.json", string(b))
	cfgDir := realTempDir(t)
	cfg := writeServeConfig(t, cfgDir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "catalog": {"disableDefaults": true, "agents": [{"id": "keyed", "name": "keyed", "command": ["/bin/cat"], "adapter": "claude", "env": {"TOKEN": %q}}]}}`, cfgDir, cfgDir, secret))
	out := filepath.Join(realTempDir(t), "resolved.zip")
	if code, stdout, stderr, err := exportCmd(t, "--data-dir", dir, "--config", cfg, "--out", out); code != 0 || err != nil {
		t.Fatalf("export: %d %v\n%s%s", code, err, stdout, stderr)
	}
	_, files := archiveFiles(t, out)
	var got catalog.Overlay
	if err := json.Unmarshal(files[export.DataDir+"catalog.json"], &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Env["TOKEN"] != secret || got.Agents[0].Adapter != "claude" || got.Agents[0].Name != "Keyed" {
		t.Fatalf("the archive's agent: %+v", got.Agents)
	}
	// The source's file is as it was.
	if after, err := os.ReadFile(filepath.Join(dir, "catalog.json")); err != nil || !bytes.Equal(after, b) {
		t.Fatalf("the source's catalog changed: %s %v", after, err)
	}
	// Without the config, the mask has no value to stand for: the agent is
	// the overlay's own.
	bare := filepath.Join(realTempDir(t), "bare.zip")
	if code, _, _, err := exportCmd(t, "--data-dir", dir, "--out", bare); code != 0 || err != nil {
		t.Fatalf("export with no config: %d %v", code, err)
	}
	if _, files := archiveFiles(t, bare); bytes.Contains(files[export.DataDir+"catalog.json"], []byte(secret)) {
		t.Fatal("the configured value with no config")
	}
}

// conductor restore FILE installs the archive into an empty data directory,
// printed before anything is written, and lets restore.lock go.
func TestRestoreInstalls(t *testing.T) {
	clearConductorEnv(t)
	dir, id := sourceDir(t)
	out := exportOf(t, dir)
	into := filepath.Join(realTempDir(t), "into")
	code, stdout, stderr, err := restoreCmd(t, out, "--data-dir", into)
	if code != 0 || err != nil {
		t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
	}
	if first, _, _ := strings.Cut(stdout, "\n"); first != "restoring into "+into {
		t.Fatalf("the first line: %q", first)
	}
	if !strings.Contains(stdout, "restored ") || !strings.Contains(stdout, "conductor serve") {
		t.Fatalf("stdout: %q", stdout)
	}
	for _, rel := range []string{"crews/" + id + ".json", "runs/team-0000d0e1.json", export.LedgerPath, export.DispatchPath} {
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(into, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		// The databases are snapshots: the same rows, other bytes.
		if !strings.HasSuffix(rel, ".sqlite") && !bytes.Equal(got, want) {
			t.Errorf("%s differs", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore.lock is still there: %v", err)
	}
	// A file that is no archive is refused, and leaves nothing where the
	// directory was not.
	junk := filepath.Join(realTempDir(t), "junk.zip")
	writeFile(t, filepath.Dir(junk), "junk.zip", "not an archive")
	none := filepath.Join(realTempDir(t), "none")
	if code, _, _, err := restoreCmd(t, junk, "--data-dir", none); code != 1 || err == nil {
		t.Fatalf("junk: %d %v", code, err)
	}
	if _, err := os.Stat(none); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused restore left %s: %v", none, err)
	}
	// Two levels above it missing too: none of them is left.
	parent := realTempDir(t)
	deep := filepath.Join(parent, "new", "a", "b")
	if code, _, _, err := restoreCmd(t, junk, "--data-dir", deep); code != 1 || err == nil {
		t.Fatalf("junk, deep: %d %v", code, err)
	}
	if entries := mustReadDir(t, parent); len(entries) != 0 {
		t.Fatalf("a refused restore left %v in %s", entries, parent)
	}
	for _, args := range [][]string{{}, {out, out}, {"--provider", out}} {
		if code, _, _, err := restoreCmd(t, args...); code != 2 || err == nil {
			t.Errorf("%v: %d %v", args, code, err)
		}
	}
}

// A data directory that holds server data is refused, with nothing
// written and no restore.lock left.
func TestRestoreRefusesServerData(t *testing.T) {
	clearConductorEnv(t)
	dir, _ := sourceDir(t)
	out := exportOf(t, dir)
	for _, name := range []string{"catalog.json", "crews/x.json", "crews.json", "runs/x.json", "dispatch/x", "links/x"} {
		t.Run(name, func(t *testing.T) {
			into := filepath.Join(realTempDir(t), "into")
			writeFile(t, into, name, "{}")
			before := tree(t, into)
			code, _, _, err := restoreCmd(t, out, "--data-dir", into)
			if code != 1 || err == nil || !strings.Contains(err.Error(), "server data") {
				t.Fatalf("restore: %d %v", code, err)
			}
			sameTree(t, into, before)
		})
	}
	// A running server's directory: refused by its ledger.
	s := serveSource(t, filepath.Join(realTempDir(t), "running"))
	before := tree(t, s.dir)
	if code, _, _, err := restoreCmd(t, out, "--data-dir", s.dir); code != 1 || err == nil {
		t.Fatalf("a running server's directory: %d %v", code, err)
	}
	if after := tree(t, s.dir); after[export.LockFile] != "" || len(after) < len(before) {
		t.Fatalf("the running server's directory changed")
	}
}

// Without --data-dir, a restore goes where conductor serve would go with the
// same config and environment: a config's dataDir, CONDUCTOR_DATA_DIR, the
// legacy conductor.d beside the config (with serve's notice), or
// ~/.conductor; the directory printed first, and serve, started the same
// way, opens it.
func TestRestoreDataDirAsServe(t *testing.T) {
	clearConductorEnv(t)
	src, id := sourceDir(t)
	out := exportOf(t, src)
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, cfgDir string) (config, want string)
		notice bool
	}{
		{name: "config", setup: func(t *testing.T, cfgDir string) (string, string) {
			want := filepath.Join(cfgDir, "state")
			return fmt.Sprintf(`, "dataDir": %q`, want), want
		}},
		{name: "env", setup: func(t *testing.T, cfgDir string) (string, string) {
			want := filepath.Join(cfgDir, "from-env")
			t.Setenv("CONDUCTOR_DATA_DIR", want)
			return "", want
		}},
		{name: "legacy", notice: true, setup: func(t *testing.T, cfgDir string) (string, string) {
			want := filepath.Join(cfgDir, "conductor.d")
			if err := os.Mkdir(want, 0o700); err != nil {
				t.Fatal(err)
			}
			return "", want
		}},
		{name: "default", setup: func(t *testing.T, cfgDir string) (string, string) {
			return "", filepath.Join(os.Getenv("HOME"), ".conductor")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearConductorEnv(t)
			cfgDir := realTempDir(t)
			work := filepath.Join(cfgDir, "work")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			extra, want := c.setup(t, cfgDir)
			cfg := writeServeConfig(t, cfgDir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q%s}`, work, work, extra))
			code, stdout, stderr, err := restoreCmd(t, out, "--config", cfg)
			if code != 0 || err != nil {
				t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
			}
			if first, _, _ := strings.Cut(stdout, "\n"); first != "restoring into "+want {
				t.Fatalf("the first line: %q, want the directory %s", first, want)
			}
			if c.notice != strings.Contains(stderr, want) {
				t.Fatalf("the notice (want %v): %q", c.notice, stderr)
			}
			if _, err := os.Stat(filepath.Join(want, "crews", id+".json")); err != nil {
				t.Fatalf("not restored into %s: %v", want, err)
			}
			logs := serveUntilListening(t, "--config", cfg)
			if lines := logLines(logs, "conductor serving", "dataDir="+want); len(lines) != 1 {
				t.Fatalf("serve does not use %s:\n%s", want, logs)
			}
		})
	}
}

// A fresh directory holding conductor host's hooks/ takes the restore: the
// archive's hooks/ in place, the earlier one gone.
func TestRestoreReplacesHooks(t *testing.T) {
	clearConductorEnv(t)
	dir, _ := sourceDir(t)
	writeFile(t, dir, "hooks/from-the-source", "source")
	out := exportOf(t, dir)
	into := filepath.Join(realTempDir(t), "into")
	writeFile(t, into, "hooks/from-host", "host")
	if code, stdout, stderr, err := restoreCmd(t, out, "--data-dir", into); code != 0 || err != nil {
		t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(into, "hooks", "from-the-source")); err != nil || string(b) != "source" {
		t.Fatalf("the archive's hooks: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(into, "hooks", "from-host")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the host's hooks are still there: %v", err)
	}
	for _, e := range mustReadDir(t, into) {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Fatalf("the staging directory %s is left", e.Name())
		}
	}
}

// mustReadDir is dir's entries.
func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// gate is a seam that holds whoever reaches it until the test opens it,
// and says when one has.
type gate struct {
	reached chan struct{}
	open    chan struct{}
	once    sync.Once
	freed   sync.Once
}

func newGate() *gate { return &gate{reached: make(chan struct{}), open: make(chan struct{})} }

// hold is the seam's body: the first caller says it is there and waits.
func (g *gate) hold() {
	first := false
	g.once.Do(func() { first = true; close(g.reached) })
	if first {
		<-g.open
	}
}

// release opens the gate, once: the test's own step, and its cleanup, so
// a test that fails while a command waits there never leaves it waiting.
func (g *gate) release() { g.freed.Do(func() { close(g.open) }) }

// waitReached waits for the seam to be reached, or for done to say the
// command returned first.
func (g *gate) waitReached(t *testing.T, done <-chan error) {
	t.Helper()
	deadline, wait := startDeadline(t)
	select {
	case <-g.reached:
	case err := <-done:
		t.Fatalf("returned before the seam: %v", err)
	case <-deadline:
		t.Fatalf("the seam was not reached in %s", wait)
	}
}

// setRestoreAt sets the restoreAt seam for the test.
func setRestoreAt(t *testing.T, f func(stage string)) {
	t.Helper()
	old := restoreAt
	restoreAt = f
	t.Cleanup(func() { restoreAt = old })
}

// serveRefused runs conductor serve on the data directory of cfg and
// checks it is refused, naming restore.lock, before it listens.
func serveRefused(t *testing.T, cfg string) {
	t.Helper()
	if err := serveRefusedErr(cfg); err != nil {
		t.Fatal(err)
	}
}

// serveRefusedErr is serveRefused's check as an error, for a seam that
// runs off the test's goroutine, where t.Fatal must not be called.
func serveRefusedErr(cfg string) error {
	var logs syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), serveStartWait)
	defer cancel()
	code, err := runServe(ctx, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &logs)
	if code != 1 || err == nil || !errors.Is(err, export.ErrReserved) || !strings.Contains(err.Error(), export.LockFile) {
		return fmt.Errorf("serve while reserved: %d %v\n%s", code, err, logs.String())
	}
	if strings.Contains(logs.String(), "conductor serving") {
		return fmt.Errorf("serve listened while reserved:\n%s", logs.String())
	}
	return nil
}

// dataDirConfig is a config whose dataDir is dir.
func dataDirConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgDir := realTempDir(t)
	return writeServeConfig(t, cfgDir, fmt.Sprintf(`{"workbenchToken": "t", "allowedRoots": [%q], "defaultCwd": %q, "dataDir": %q}`, cfgDir, cfgDir, dir))
}

// A restore paused before its install, its reservation held: a serve and a
// second restore on the directory are refused, naming restore.lock, and
// write nothing; let go, the restore installs.
func TestServeRefusesWhileRestoring(t *testing.T) {
	clearConductorEnv(t)
	src, id := sourceDir(t)
	out := exportOf(t, src)
	into := filepath.Join(realTempDir(t), "into")
	cfg := dataDirConfig(t, into)
	g := newGate()
	setRestoreAt(t, func(stage string) {
		if stage == "reserved" {
			g.hold()
		}
	})
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		code, err := runRestore(context.Background(), []string{out, "--data-dir", into}, io.Discard, io.Discard)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d", code)
		}
		done <- err
	}()
	t.Cleanup(func() { g.release(); <-finished })
	g.waitReached(t, done)
	before := tree(t, into)
	if _, ok := before[export.LockFile]; !ok || len(before) != 2 {
		t.Fatalf("the directory while reserved: %v", before)
	}
	serveRefused(t, cfg)
	code, _, _, err := restoreCmd(t, out, "--data-dir", into)
	if code != 1 || !errors.Is(err, export.ErrReserved) || !strings.Contains(err.Error(), export.LockFile) {
		t.Fatalf("a second restore: %d %v", code, err)
	}
	sameTree(t, into, before)
	g.release()
	if err := <-done; err != nil {
		t.Fatalf("the restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(into, "crews", id+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore.lock left: %v", err)
	}
}

// A serve paused inside its reservation, before it opens the store: a
// restore is refused, naming restore.lock, and nothing is written; let go,
// the serve starts, and a restore is refused by its ledger and its data.
func TestRestoreRefusesWhileServeStarts(t *testing.T) {
	clearConductorEnv(t)
	src, _ := sourceDir(t)
	out := exportOf(t, src)
	into := filepath.Join(realTempDir(t), "into")
	cfg := dataDirConfig(t, into)
	g := newGate()
	old := serveReserved
	serveReserved = func(string) { g.hold() }
	t.Cleanup(agents.ForgetBinary())
	var logs syncBuffer
	sv := startServing(t, []string{"--config", cfg, "--listen", "127.0.0.1:0"}, strings.NewReader(""), io.Discard, &logs)
	// Put back once the server has returned: it reads the seam. The gate
	// opens first, so a failed test never leaves the server waiting there.
	t.Cleanup(func() { g.release(); sv.stop(); serveReserved = old })
	deadline, wait := startDeadline(t)
	select {
	case <-g.reached:
	case <-deadline:
		t.Fatalf("serve did not reach its reservation in %s:\n%s", wait, logs.String())
	}
	before := tree(t, into)
	if _, ok := before[export.LockFile]; !ok {
		t.Fatalf("serve holds no restore.lock: %v", before)
	}
	code, _, _, err := restoreCmd(t, out, "--data-dir", into)
	if code != 1 || !errors.Is(err, export.ErrReserved) || !strings.Contains(err.Error(), export.LockFile) {
		t.Fatalf("a restore while serve starts: %d %v", code, err)
	}
	sameTree(t, into, before)
	g.release()
	for !strings.Contains(logs.String(), "conductor serving") {
		if err, ok := sv.returned(); ok {
			t.Fatalf("serve returned: %v\n%s", err, logs.String())
		}
		select {
		case <-deadline:
			t.Fatalf("serve did not start:\n%s", logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("serve kept restore.lock: %v", err)
	}
	code, _, _, err = restoreCmd(t, out, "--data-dir", into)
	if code != 1 || err == nil || errors.Is(err, export.ErrReserved) {
		t.Fatalf("a restore while serve runs: %d %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused restore left restore.lock: %v", err)
	}
	if err := sv.stop(); err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
}

// fakeSync is a sync store whose Restore runs restore with what it is
// given.
type fakeSync struct {
	restore func(ctx context.Context, r ext.RestoreRequest, w io.Writer) error
}

func (fakeSync) Name() string                           { return "fake" }
func (fakeSync) Saved(context.Context, ext.Doc) error   { return nil }
func (fakeSync) Deleted(context.Context, ext.Doc) error { return nil }
func (f fakeSync) Restore(ctx context.Context, r ext.RestoreRequest, w io.Writer) error {
	return f.restore(ctx, r, w)
}

// withSync makes the registry the command reads hold s alone (an empty
// one for nil), for the test.
func withSync(t *testing.T, s ext.ContinuityStore) {
	t.Helper()
	old := extensions
	t.Cleanup(func() { extensions = old })
	extensions = func() ext.Providers {
		if s == nil {
			return ext.Providers{}
		}
		return ext.Providers{Continuity: s, All: []ext.Entry{{Kind: ext.KindSync, Name: s.Name(), Value: s}}}
	}
}

// fromFile is a fake's Restore that copies the archive at path.
func fromFile(path string) func(context.Context, ext.RestoreRequest, io.Writer) error {
	return func(_ context.Context, _ ext.RestoreRequest, w io.Writer) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
}

// noTemporary fails the test when a restore's temporary file is left
// beside dir.
func noTemporary(t *testing.T, dir string) {
	t.Helper()
	for _, e := range mustReadDir(t, filepath.Dir(dir)) {
		if strings.HasPrefix(e.Name(), ".conductor-restore-") {
			t.Fatalf("the temporary file %s is left beside %s", e.Name(), dir)
		}
	}
}

// chunked is an httptest server that answers the archive at path in
// chunks of size bytes, flushed one by one; after the first, it waits
// for more to be closed (nil: it does not wait).
func chunked(t *testing.T, path string, size int, more <-chan struct{}) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		for i := 0; i < len(b); i += size {
			w.Write(b[i:min(i+size, len(b))])
			w.(http.Flusher).Flush()
			if i == 0 && more != nil {
				select {
				case <-more:
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	t.Cleanup(hs.Close)
	return hs
}

// download is a fake's Restore that streams the archive from url into w,
// ending with ctx.
func download(url string) func(context.Context, ext.RestoreRequest, io.Writer) error {
	return func(ctx context.Context, _ ext.RestoreRequest, w io.Writer) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the service answered %s", resp.Status)
		}
		n, err := io.Copy(w, resp.Body)
		if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
			err = fmt.Errorf("%d of %d bytes", n, resp.ContentLength)
		}
		return err
	}
}

// --provider: a fake whose Restore downloads the archive from its service
// in chunks; the bytes are counted as they arrive (the second chunk is
// sent only once the first was said), and the archive is installed.
func TestRestoreFromProviderStreams(t *testing.T) {
	clearConductorEnv(t)
	src, id := sourceDir(t)
	out := exportOf(t, src)
	old := progressStep
	progressStep = 1024
	t.Cleanup(func() { progressStep = old })
	more := make(chan struct{})
	var sent sync.Once
	send := func() { sent.Do(func() { close(more) }) }
	hs := chunked(t, out, 2048, more)
	// Before the server closes: a failed test never leaves its handler
	// waiting for the rest.
	t.Cleanup(send)
	withSync(t, fakeSync{restore: download(hs.URL)})
	into := filepath.Join(realTempDir(t), "into")
	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		code, err := runRestore(context.Background(), []string{"--provider", "--data-dir", into}, &stdout, &stderr)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d", code)
		}
		done <- err
	}()
	deadline, wait := startDeadline(t)
	for !strings.Contains(stdout.String(), "fake: 2048 bytes\n") {
		select {
		case err := <-done:
			t.Fatalf("returned before the first chunk was counted: %v\n%s%s", err, stdout.String(), stderr.String())
		case <-deadline:
			t.Fatalf("the first chunk was not counted in %s:\n%s", wait, stdout.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	send()
	if err := <-done; err != nil {
		t.Fatalf("restore: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := logLines(stdout.String(), "fake: ")
	if len(lines) < 3 || !strings.Contains(stdout.String(), fmt.Sprintf("fake: %d bytes, all of them", fi.Size())) {
		t.Fatalf("the bytes as they arrived: %q", lines)
	}
	if !strings.Contains(stdout.String(), "the sync store fake") {
		t.Fatalf("the provider is not named: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(into, "crews", id+".json")); err != nil {
		t.Fatal(err)
	}
	noTemporary(t, into)
	if _, err := os.Stat(filepath.Join(into, export.LockFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore.lock left: %v", err)
	}
}

// A download that fails halfway, and one whose ctx ends while it waits on
// its service: the temporary file gone, the reservation released, the
// data directory as it was, exit 1 with the provider's error.
func TestRestoreFromProviderEndedShort(t *testing.T) {
	clearConductorEnv(t)
	src, _ := sourceDir(t)
	out := exportOf(t, src)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	half := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(half.Close)
	reached := make(chan struct{})
	var reachedOnce sync.Once
	waits := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedOnce.Do(func() { close(reached) })
		<-r.Context().Done()
	}))
	t.Cleanup(waits.Close)

	check := func(t *testing.T, into string, before map[string]string, code int, err error) {
		t.Helper()
		if code != 1 || err == nil {
			t.Fatalf("restore: %d %v", code, err)
		}
		noTemporary(t, into)
		sameTree(t, into, before)
		res, rerr := export.Reserve(into, export.HolderRestore)
		if rerr != nil {
			t.Fatalf("the reservation was not released: %v", rerr)
		}
		res.Release()
	}

	t.Run("halfway", func(t *testing.T) {
		withSync(t, fakeSync{restore: download(half.URL)})
		into := filepath.Join(realTempDir(t), "into")
		writeFile(t, into, "config.json", "{}")
		before := tree(t, into)
		code, _, _, err := restoreCmd(t, "--provider", "--data-dir", into)
		check(t, into, before, code, err)
		if !strings.Contains(err.Error(), "the sync store fake") {
			t.Fatalf("the provider's error: %v", err)
		}
	})
	t.Run("ctx", func(t *testing.T) {
		withSync(t, fakeSync{restore: download(waits.URL)})
		into := filepath.Join(realTempDir(t), "into")
		writeFile(t, into, "config.json", "{}")
		before := tree(t, into)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type result struct {
			code int
			err  error
		}
		done := make(chan result, 1)
		go func() {
			code, err := runRestore(ctx, []string{"--provider", "--data-dir", into}, io.Discard, io.Discard)
			done <- result{code, err}
		}()
		deadline, wait := startDeadline(t)
		select {
		case <-reached:
		case r := <-done:
			t.Fatalf("returned before the service was asked: %d %v", r.code, r.err)
		case <-deadline:
			t.Fatalf("the service was not asked in %s", wait)
		}
		cancel()
		r := <-done
		check(t, into, before, r.code, r.err)
		if !strings.Contains(r.err.Error(), "stopped") {
			t.Fatalf("the stop: %v", r.err)
		}
	})
}

// One reservation from before the download to after the install: as the
// download starts, between it and the install, between two of the
// install's renames and after its last, restore.lock is the same file
// with the same pid and start time, a serve and a second restore are
// refused; it is gone once the command returns. The seams run on the
// command's goroutines (the provider's own, for the first), so each
// records what it found for the test to judge, and a failed check ends
// the command rather than the goroutine.
func TestRestoreFromProviderHoldsTheReservation(t *testing.T) {
	clearConductorEnv(t)
	src, _ := sourceDir(t)
	out := exportOf(t, src)
	into := filepath.Join(realTempDir(t), "into")
	cfg := dataDirConfig(t, into)
	lock := filepath.Join(into, export.LockFile)
	var mu sync.Mutex
	var first os.FileInfo
	var stamp []byte
	var seen, failed []string
	check := func(at string) error {
		mu.Lock()
		defer mu.Unlock()
		err := func() error {
			fi, err := os.Stat(lock)
			if err != nil {
				return fmt.Errorf("no restore.lock: %v", err)
			}
			b, err := os.ReadFile(lock)
			if err != nil {
				return err
			}
			if first == nil {
				first, stamp = fi, b
			} else if !os.SameFile(first, fi) || !bytes.Equal(b, stamp) {
				return fmt.Errorf("restore.lock is another (%q, was %q)", b, stamp)
			}
			if !strings.HasPrefix(string(b), fmt.Sprintf("pid %d since ", os.Getpid())) {
				return fmt.Errorf("restore.lock says %q", b)
			}
			if err := serveRefusedErr(cfg); err != nil {
				return err
			}
			if code, _, _, err := restoreCmd(t, out, "--data-dir", into); code != 1 || !errors.Is(err, export.ErrReserved) {
				return fmt.Errorf("a second restore: %d %v", code, err)
			}
			return nil
		}()
		seen = append(seen, at)
		if err != nil {
			failed = append(failed, at+": "+err.Error())
		}
		return err
	}
	setRestoreAt(t, func(stage string) {
		if stage == "downloaded" || stage == "installed" {
			check(stage)
		}
	})
	// The install's renames, through internal/export's seam: the check
	// before the second, with the first renamed and the rest not.
	renames := 0
	oldRename := export.BeforeRename
	export.BeforeRename = func(string) {
		if renames++; renames == 2 {
			check("renaming")
		}
	}
	t.Cleanup(func() { export.BeforeRename = oldRename })
	fetch := fromFile(out)
	withSync(t, fakeSync{restore: func(ctx context.Context, r ext.RestoreRequest, w io.Writer) error {
		if err := check("download"); err != nil {
			return err
		}
		return fetch(ctx, r, w)
	}})
	code, stdout, stderr, err := restoreCmd(t, "--provider", "--data-dir", into)
	if len(failed) != 0 {
		t.Fatalf("under the reservation:\n%s", strings.Join(failed, "\n"))
	}
	if code != 0 || err != nil {
		t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
	}
	if renames < 2 {
		t.Fatalf("the install renamed %d entries; the test needs two", renames)
	}
	if !slices.Equal(seen, []string{"download", "downloaded", "renaming", "installed"}) {
		t.Fatalf("the seams reached: %v", seen)
	}
	if _, err := os.Stat(lock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("restore.lock left: %v", err)
	}
}

// A provider's Restore that ends its goroutine with runtime.Goexit (as a
// t.Fatal there does) returns no error and raises no panic: the command
// still ends, exit 1 naming it, the temporary file gone, the reservation
// released and the data directory as it was.
func TestRestoreFromProviderGoexit(t *testing.T) {
	clearConductorEnv(t)
	withSync(t, fakeSync{restore: func(context.Context, ext.RestoreRequest, io.Writer) error {
		runtime.Goexit()
		return nil
	}})
	into := filepath.Join(realTempDir(t), "into")
	writeFile(t, into, "config.json", "{}")
	before := tree(t, into)
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := runRestore(t.Context(), []string{"--provider", "--data-dir", into}, io.Discard, io.Discard)
		done <- result{code, err}
	}()
	deadline, wait := startDeadline(t)
	var r result
	select {
	case r = <-done:
	case <-deadline:
		t.Fatalf("the command did not return in %s after the provider's Goexit", wait)
	}
	if r.code != 1 || r.err == nil || !strings.Contains(r.err.Error(), "Goexit") || !strings.Contains(r.err.Error(), "the sync store fake") {
		t.Fatalf("restore: %d %v", r.code, r.err)
	}
	noTemporary(t, into)
	sameTree(t, into, before)
	res, err := export.Reserve(into, export.HolderRestore)
	if err != nil {
		t.Fatalf("the reservation was not released: %v", err)
	}
	res.Release()
}

// The free default: nothing to restore, nothing changed. A fake's whole
// archive is installed; a fake's partial one (the catalog, a crew and a
// finished run's record, no database) is installed and opened by serve,
// the crew and the run listed. The fake is given ext/<its name> in the
// data directory, 0700, and what it keeps there stays.
func TestRestoreFromProvider(t *testing.T) {
	clearConductorEnv(t)
	src, id := sourceDir(t)
	out := exportOf(t, src)

	t.Run("default", func(t *testing.T) {
		withSync(t, nil)
		for _, made := range []bool{false, true} {
			parent := realTempDir(t)
			into := filepath.Join(parent, "new", "a", "into")
			if made {
				writeFile(t, into, "hooks/x", "host")
			}
			before := tree(t, into)
			code, stdout, _, err := restoreCmd(t, "--provider", "--data-dir", into)
			if code != 1 || err == nil || !strings.Contains(err.Error(), "nothing to restore") || !strings.Contains(stdout, "the sync store local") {
				t.Fatalf("the default: %d %v %q", code, err, stdout)
			}
			if made {
				sameTree(t, into, before)
				noTemporary(t, into)
			} else if entries := mustReadDir(t, parent); len(entries) != 0 {
				t.Fatalf("the default made %v in %s", entries, parent)
			}
		}
	})

	t.Run("server data", func(t *testing.T) {
		asked := false
		withSync(t, fakeSync{restore: func(context.Context, ext.RestoreRequest, io.Writer) error {
			asked = true
			return nil
		}})
		into := filepath.Join(realTempDir(t), "into")
		writeFile(t, into, "catalog.json", "{}")
		before := tree(t, into)
		code, _, _, err := restoreCmd(t, "--provider", "--data-dir", into)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "server data") || asked {
			t.Fatalf("into server data: %d %v (provider asked: %v)", code, err, asked)
		}
		sameTree(t, into, before)
	})

	t.Run("whole", func(t *testing.T) {
		var given ext.RestoreRequest
		fetch := fromFile(out)
		withSync(t, fakeSync{restore: func(ctx context.Context, r ext.RestoreRequest, w io.Writer) error {
			given = r
			if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				return fmt.Errorf("the provider's directory: %v %v", fi, err)
			}
			if err := os.WriteFile(filepath.Join(r.Dir, "signin"), []byte("kept"), 0o600); err != nil {
				return err
			}
			r.Log.Info("fetching")
			return fetch(ctx, r, w)
		}})
		into := filepath.Join(realTempDir(t), "into")
		code, stdout, stderr, err := restoreCmd(t, "--provider", "--data-dir", into)
		if code != 0 || err != nil {
			t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
		}
		if given.Dir != filepath.Join(into, "ext", "fake") {
			t.Fatalf("the provider's directory: %q", given.Dir)
		}
		if !strings.Contains(stderr, "provider=fake") {
			t.Fatalf("the provider's log does not name it: %q", stderr)
		}
		if b, err := os.ReadFile(filepath.Join(into, "ext", "fake", "signin")); err != nil || string(b) != "kept" {
			t.Fatalf("the provider's sign-in: %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(into, export.LedgerPath)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("partial", func(t *testing.T) {
		part := filepath.Join(realTempDir(t), "part")
		for _, rel := range []string{"crews/" + id + ".json", "runs/team-0000d0e1.json"} {
			b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, part, rel, string(b))
		}
		ov, err := store.Encode(catalog.Overlay{Agents: []catalog.Agent{{ID: "mine", Name: "Mine", Command: []string{"/bin/cat"}}}})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, part, "catalog.json", string(ov))
		partial := exportOf(t, part)
		if names, _ := archiveFiles(t, partial); slices.Contains(names, export.DataDir+export.LedgerPath) {
			t.Fatalf("the partial archive holds a database: %v", names)
		}
		withSync(t, fakeSync{restore: fromFile(partial)})
		into := filepath.Join(realTempDir(t), "into")
		if code, stdout, stderr, err := restoreCmd(t, "--provider", "--data-dir", into); code != 0 || err != nil {
			t.Fatalf("restore: %d %v\n%s%s", code, err, stdout, stderr)
		}
		// The provider's directory stays for its next start, whatever it holds.
		if fi, err := os.Stat(filepath.Join(into, "ext", "fake")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("the provider's directory: %v %v", fi, err)
		}
		cfg := dataDirConfig(t, into)
		serveWhile(t, "conductor serving", func(logs string) {
			base := "http://" + listenOf(t, logs)
			var crews struct {
				Crews []struct {
					ID string `json:"id"`
				} `json:"crews"`
			}
			getJSON(t, base+"/api/crews", &crews)
			if len(crews.Crews) != 1 || crews.Crews[0].ID != id {
				t.Errorf("the crews: %+v", crews)
			}
			// A finished run is answered from its record, the ledger empty.
			var run struct {
				Run struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"run"`
				Record bool `json:"record"`
			}
			getJSON(t, base+"/api/runs/team-0000d0e1", &run)
			if run.Run.ID != "team-0000d0e1" || !run.Record {
				t.Errorf("the run: %+v", run)
			}
			var agents []struct {
				ID string `json:"id"`
			}
			var cat struct {
				Agents json.RawMessage `json:"agents"`
			}
			getJSON(t, base+"/api/catalog", &cat)
			if err := json.Unmarshal(cat.Agents, &agents); err != nil || !slices.ContainsFunc(agents, func(a struct {
				ID string `json:"id"`
			}) bool {
				return a.ID == "mine"
			}) {
				t.Errorf("the catalog: %s %v", cat.Agents, err)
			}
		}, "--config", cfg)
	})
}

// listenOf is the address the "conductor serving" line names.
func listenOf(t *testing.T, logs string) string {
	t.Helper()
	lines := logLines(logs, "conductor serving")
	if len(lines) != 1 {
		t.Fatalf("no serving line:\n%s", logs)
	}
	for _, f := range strings.Fields(lines[0]) {
		if v, ok := strings.CutPrefix(f, "listen="); ok {
			return v
		}
	}
	t.Fatalf("no listen= in %q", lines[0])
	return ""
}

// getJSON GETs url with the workbench token "t" and decodes the reply.
func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("GET %s: %v: %s", url, err, b)
	}
}
