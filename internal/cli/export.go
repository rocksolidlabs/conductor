package cli

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The commands of docs/plans/ext.md, C6: conductor export writes a data
// directory as one archive (through the running server, or offline from a
// stopped one's directory), and conductor restore puts one into an empty
// data directory, the one serve would use unless told, where the next
// serve opens it as its own. internal/export does the work; these parse,
// resolve the directory, reserve it and say what happened.

const exportUsage = `Usage:
  conductor export [--server URL] [--token T] [--insecure] [--out FILE]
      write the running server's data directory as one zip archive
      (GET /api/export): the catalog, the crews, the runs' records and
      the ledger, Dispatch with its attachments, a switchyard's links and
      the hook assets; never the workbench token, a lock, tls/ or a
      provider's own state
  conductor export --data-dir DIR [--config FILE] [--out FILE]
      the same from a data directory no server runs on (refused while one
      does: export through it instead); the catalog is resolved against
      the configuration --config gives, as serve would load it

The archive goes to --out, by default conductor-export-<UTC time>.zip in
the current directory, made 0600 and never written over an existing file.
It holds saved agents' env values in full, configured ones they inherit
among them, so that the agents launch as here on the machine it is
restored on: keep it as you keep the config. The server defaults to
CONDUCTOR_SERVER, else http://localhost:8080; the workbench token to
CONDUCTOR_WORKBENCH_TOKEN; a plain http:// server on another machine is
refused unless --insecure says to go ahead.
`

const restoreUsage = `Usage:
  conductor restore FILE [--config FILE] [--data-dir DIR]
      put the archive FILE (from conductor export) into an empty data
      directory: --data-dir, or the one conductor serve would use with the
      same --config and environment (dataDir, CONDUCTOR_DATA_DIR, else
      ~/.conductor), printed before anything is written; then start
      conductor serve on it, which opens what was restored as its own
  conductor restore --provider [--config FILE] [--data-dir DIR]
      the same with this device's latest copy from the sync store, when one
      is built in; without one there is nothing to restore and nothing
      changes

A data directory that holds server data, or that a server runs on or is
starting on, is refused with nothing written. The directory is reserved
(restore.lock) from before the first byte to after the last rename, so no
server starts on it meanwhile. Ctrl-C stops a restore before its renames
with the data directory as it was.
`

// The seams a test sets: serveReserved runs once serve holds the data
// directory's reservation, restoreAt at each stage of a restore under its
// reservation ("reserved", "downloaded" for --provider, "installed").
var (
	serveReserved func(dir string)
	restoreAt     func(stage string)
)

// restoreStage runs the restoreAt seam.
func restoreStage(stage string) {
	if restoreAt != nil {
		restoreAt(stage)
	}
}

// exportName is the default archive's name for the time t.
func exportName(t time.Time) string {
	return "conductor-export-" + t.UTC().Format("20060102T150405Z") + ".zip"
}

// envKept is what conductor export says on stderr of every archive it
// writes.
const envKept = "the archive holds your agents' env values, configured ones among them: keep it as you keep the config"

// runExport writes the data directory as an archive, through the running
// server or, with --data-dir, from a stopped one's directory.
func runExport(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fl := flag.NewFlagSet("export", flag.ContinueOnError)
	fl.SetOutput(stderr)
	api := addAPIFlags(fl)
	dataDir := fl.String("data-dir", "", "read this data directory itself, with no server running on it, instead of asking the server")
	configPath := fl.String("config", "", "with --data-dir: the config file serve runs with, whose catalog the archive's agents are resolved against")
	out := fl.String("out", "", "the archive to write (default conductor-export-<UTC time>.zip in the current directory); never an existing file")
	fl.Usage = func() {
		fmt.Fprint(stderr, exportUsage)
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if fl.NArg() != 0 {
		fl.Usage()
		return 2, errors.New("export: takes no arguments; name the archive with --out")
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	offline := set["data-dir"]
	switch {
	case offline && *dataDir == "":
		return 2, errors.New("export: --data-dir names a data directory")
	case offline && (set["server"] || set["token"] || set["insecure"]):
		return 2, errors.New("export: --data-dir reads the directory itself; --server, --token and --insecure ask a server, and do not go with it")
	case !offline && set["config"]:
		return 2, errors.New("export: --config goes with --data-dir; a running server resolves the catalog against its own")
	}
	path := *out
	if path == "" {
		path = exportName(time.Now())
	}
	var c *apiClient
	var src export.Source
	if offline {
		var err error
		if src, err = offlineSource(*dataDir, *configPath); err != nil {
			return 1, err
		}
	} else {
		var err error
		if c, err = api.client(stderr); err != nil {
			return 2, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return 1, fmt.Errorf("export: %s exists, and an export never writes over a file: name another with --out", path)
	}
	if err != nil {
		return 1, fmt.Errorf("export: %w", err)
	}
	written := false
	defer func() {
		if !written {
			f.Close()
			os.Remove(path)
		}
	}()
	if offline {
		bw := bufio.NewWriterSize(f, 64<<10)
		if _, err := export.Write(ctx, bw, src); err != nil {
			return 1, fmt.Errorf("export: %w", err)
		}
		if err := bw.Flush(); err != nil {
			return 1, fmt.Errorf("export: %s: %w", path, err)
		}
	} else if err := c.download(ctx, "/api/export", f); err != nil {
		return 1, fmt.Errorf("export: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 1, fmt.Errorf("export: %s: %w", path, err)
	}
	m, size, err := archiveManifest(f)
	if err != nil {
		return 1, fmt.Errorf("export: the archive is incomplete (%v); nothing kept", err)
	}
	if err := f.Close(); err != nil {
		return 1, fmt.Errorf("export: %s: %w", path, err)
	}
	written = true
	fmt.Fprintf(stdout, "wrote %s: %d files, %d bytes; %d left out (in its manifest, with why)\n", path, len(m.Files), size, len(m.Left))
	fmt.Fprintln(stderr, "conductor export: "+envKept)
	return 0, nil
}

// offlineSource is the export's view of a data directory no server runs
// on: its databases snapshotted through read-only connections of the
// export's, and its catalog resolved against the configuration
// config.Load(configPath) gives, as serve would load it. A directory
// whose ledger a server holds, or that restore.lock reserves, is refused.
func offlineSource(dir, configPath string) (export.Source, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return export.Source{}, fmt.Errorf("export: --data-dir: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return export.Source{}, fmt.Errorf("export: the data directory %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return export.Source{}, fmt.Errorf("export: the data directory %s is not a directory", abs)
	}
	held, err := ledger.Held(filepath.Join(abs, "runs"))
	if err != nil {
		return export.Source{}, fmt.Errorf("export: %w", err)
	}
	if held {
		return export.Source{}, fmt.Errorf("export: a server is running on %s: export through it (conductor export --server URL), or stop it and run this again", abs)
	}
	if _, err := os.Lstat(filepath.Join(abs, export.LockFile)); err == nil {
		return export.Source{}, fmt.Errorf("export: %s is there: a restore is running or a server is starting on %s, or one was cut short; export once it is gone", filepath.Join(abs, export.LockFile), abs)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return export.Source{}, fmt.Errorf("export: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return export.Source{}, fmt.Errorf("export: %w", err)
	}
	base, err := cfg.LoadCatalog()
	if err != nil {
		return export.Source{}, fmt.Errorf("export: %w", err)
	}
	return export.Source{
		Dir: abs,
		Snapshot: func(ctx context.Context, rel, dst string) error {
			path := filepath.Join(abs, filepath.FromSlash(rel))
			switch rel {
			case export.LedgerPath:
				return ledger.SnapshotFile(ctx, path, dst)
			case export.DispatchPath:
				return dispatch.SnapshotFile(ctx, path, dst)
			}
			return fmt.Errorf("no database at %s", rel)
		},
		Catalog: func() ([]byte, error) {
			ov, err := readOverlay(abs)
			if err != nil {
				return nil, err
			}
			return export.ResolveCatalog(ov, base)
		},
	}, nil
}

// readOverlay is the catalog.json of the data directory dir, read through
// an os.Root on it and decoded as the server decodes it.
func readOverlay(dir string) (catalog.Overlay, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return catalog.Overlay{}, err
	}
	defer root.Close()
	f, err := root.Open("catalog.json")
	if err != nil {
		return catalog.Overlay{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return catalog.Overlay{}, err
	}
	var ov catalog.Overlay
	if err := store.DecodeStrict(b, &ov); err != nil {
		return catalog.Overlay{}, fmt.Errorf("%s: %w", filepath.Join(dir, "catalog.json"), err)
	}
	return ov, nil
}

// archiveManifest reads the manifest of the archive in f, its last entry,
// and the archive's size: an archive cut short has no central directory
// or no manifest, and is no archive.
func archiveManifest(f *os.File) (export.Manifest, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return export.Manifest{}, 0, err
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return export.Manifest{}, 0, err
	}
	n := len(zr.File)
	if n == 0 || zr.File[n-1].Name != export.ManifestName || zr.File[n-1].UncompressedSize64 > export.MaxManifest {
		return export.Manifest{}, 0, fmt.Errorf("its last entry is not %s", export.ManifestName)
	}
	rc, err := zr.File[n-1].Open()
	if err != nil {
		return export.Manifest{}, 0, err
	}
	defer rc.Close()
	var m export.Manifest
	if err := json.NewDecoder(io.LimitReader(rc, export.MaxManifest)).Decode(&m); err != nil {
		return export.Manifest{}, 0, fmt.Errorf("its manifest: %w", err)
	}
	return m, fi.Size(), nil
}

// download writes the body of GET path, a zip archive, to w as it comes:
// no bound on its size or its time but ctx (the person's Ctrl-C), and
// requestTimeout for the reply's headers. The token goes to the server
// named and no other: a redirect is an answer.
func (c *apiClient) download(ctx context.Context, path string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("cannot build a request for %s", c.host)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/zip")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = requestTimeout
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return c.transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReply))
		return c.statusError(resp.Status, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		return fmt.Errorf("%s answered %s, not an archive", c.host, strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		if ctx.Err() != nil {
			return errors.New("interrupted")
		}
		return fmt.Errorf("the archive from %s was cut short: %w", c.host, err)
	}
	return nil
}

// runRestore puts an archive, a file or the sync store's copy, into an
// empty data directory.
func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fl := flag.NewFlagSet("restore", flag.ContinueOnError)
	fl.SetOutput(stderr)
	configPath := fl.String("config", "", "the config file conductor serve runs with, which chooses the data directory (dataDir), as serve chooses it")
	dataDir := fl.String("data-dir", "", "the data directory to restore into, instead of the one serve would use")
	provider := fl.Bool("provider", false, "restore this device's latest copy from the sync store instead of a file")
	fl.Usage = func() {
		fmt.Fprint(stderr, restoreUsage)
		fl.PrintDefaults()
	}
	rest, err := parseInterspersed(fl, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	switch {
	case *provider && len(rest) != 0:
		fl.Usage()
		return 2, errors.New("restore: --provider takes the sync store's copy; no file with it")
	case !*provider && (len(rest) != 1 || rest[0] == ""):
		fl.Usage()
		return 2, errors.New("restore: name one archive: conductor restore FILE")
	}
	dir, err := restoreDir(*dataDir, *configPath, stderr)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "restoring into %s\n", dir)
	if *provider {
		return restoreFromProvider(ctx, dir, stdout, stderr)
	}
	f, err := os.Open(rest[0])
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return 1, fmt.Errorf("restore: %s is not a file", rest[0])
	}
	installed := false
	defer leaveAsItWas(dir, &installed)()
	res, err := export.Reserve(dir)
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	defer res.Release()
	restoreStage("reserved")
	m, err := export.Install(ctx, res, f, fi.Size())
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	installed = true
	restoreStage("installed")
	restored(stdout, dir, m)
	return 0, nil
}

// restoreDir is the data directory a restore goes into: dataDir, made
// absolute, or the one conductor serve would use with the same config
// and environment, resolved as runServe resolves it (config.Load, which
// applies CONDUCTOR_DATA_DIR, then ResolveDataDir with its legacy rule,
// whose notice goes to stderr).
func restoreDir(dataDir, configPath string, stderr io.Writer) (string, error) {
	if dataDir != "" {
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return "", fmt.Errorf("restore: --data-dir: %w", err)
		}
		return abs, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	notice, err := cfg.ResolveDataDir(configPath)
	if err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	if notice != "" {
		fmt.Fprintln(stderr, "conductor restore: "+notice)
	}
	return cfg.DataDir, nil
}

// restored says what an install put where, and what to do next.
func restored(stdout io.Writer, dir string, m export.Manifest) {
	fmt.Fprintf(stdout, "restored %d files into %s (exported by Conductor %s at %s)\nstart conductor serve on it: it opens them as its own\n", len(m.Files), dir, m.Version, m.CreatedAt.UTC().Format(time.RFC3339))
}

// The bounds of a restore from the sync store: progressStep is how many
// bytes pass between two lines that count them; restoreGrace how long the
// command waits, once its context has ended, for a provider's Restore to
// return before it lets the download go and cleans up (a provider must
// end with ctx; this bounds one that does not).
var (
	progressStep = int64(4 << 20)
	restoreGrace = 10 * time.Second
)

// errRestoreEnded is what a write of a provider's Restore gets once the
// command has stopped taking its download.
var errRestoreEnded = errors.New("restore: the download is no longer taken")

// restoreFromProvider restores this device's latest copy from the sync
// store into dir (docs/plans/ext.md, C6, "the commands"): one reservation,
// taken before the freshness check and held without a gap until after the
// install's last rename or the failure that ends it; the provider's
// Restore streams into a temporary file beside dir, given ext/<name> in dir
// (made 0700: its sign-in goes there), on a goroutine of its own, with no
// wait of the guard's, until it ends or ctx does; then Install under the
// same reservation. Whatever ends it short removes the temporary file and
// leaves dir as it was, ext/<name> aside, and exits 1.
func restoreFromProvider(ctx context.Context, dir string, stdout, stderr io.Writer) (int, error) {
	installed := false
	defer leaveAsItWas(dir, &installed)()
	res, err := export.Reserve(dir)
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	defer res.Release()
	fresh, err := export.Fresh(dir)
	if err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	if !fresh {
		return 1, fmt.Errorf("restore: %s holds server data: a restore goes into a data directory with none; choose another with --data-dir", dir)
	}
	restoreStage("reserved")
	// The sync slot alone, read once: local.Fill is the one place that
	// asks whether anything is registered; the free default keeps nothing.
	cs := local.Fill(extensions()).Continuity
	name := cs.Name()
	if !ext.ValidName(name) {
		return 1, fmt.Errorf("restore: the sync store's name %q is not a provider's name", name)
	}
	extDir := filepath.Join(dir, "ext", name)
	madeExt := !exists(filepath.Join(dir, "ext"))
	if err := os.MkdirAll(extDir, 0o700); err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	// What the provider left in its directory (a sign-in) stays; an empty
	// one, and the ext/ made for it, go when nothing was installed.
	defer func() {
		if !installed {
			if os.Remove(extDir) == nil && madeExt {
				os.Remove(filepath.Join(dir, "ext"))
			}
		}
	}()
	tmp, err := os.CreateTemp(filepath.Dir(dir), ".conductor-restore-*.zip")
	if err != nil {
		return 1, fmt.Errorf("restore: a temporary file beside %s: %w", dir, err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	fmt.Fprintf(stdout, "asking the sync store %s for this device's latest copy\n", name)
	pw := &progressWriter{f: tmp, out: stdout, name: name, next: progressStep}
	log := slog.New(slog.NewTextHandler(stderr, nil)).With("provider", name)
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("it panicked: %v", p)
			}
		}()
		done <- cs.Restore(ctx, ext.RestoreRequest{Dir: extDir, Log: log}, pw)
	}()
	var rerr error
	select {
	case rerr = <-done:
	case <-ctx.Done():
		select {
		case rerr = <-done:
		case <-time.After(restoreGrace):
			rerr = ctx.Err()
		}
	}
	n := pw.end()
	switch {
	case errors.Is(rerr, ext.ErrNothingToRestore):
		return 1, fmt.Errorf("restore: the sync store %s keeps no copy of this device's data: nothing to restore, and %s is as it was", name, dir)
	case ctx.Err() != nil:
		return 1, fmt.Errorf("restore: stopped after %d bytes from the sync store %s: nothing installed, and %s is as it was", n, name, dir)
	case rerr != nil:
		return 1, fmt.Errorf("restore: the sync store %s: %w; nothing installed, and %s is as it was", name, rerr, dir)
	}
	fmt.Fprintf(stdout, "%s: %d bytes, all of them; installing\n", name, n)
	if err := tmp.Sync(); err != nil {
		return 1, fmt.Errorf("restore: %w", err)
	}
	restoreStage("downloaded")
	m, err := export.Install(ctx, res, tmp, n)
	if err != nil {
		return 1, fmt.Errorf("restore: the sync store %s's copy: %w", name, err)
	}
	installed = true
	restoreStage("installed")
	restored(stdout, dir, m)
	return 0, nil
}

// leaveAsItWas is, deferred before the reservation is taken, what puts
// back a data directory a restore made (Reserve makes a missing one) and
// installed nothing into: removed again once the reservation is let go,
// when it is empty still. The returned func runs it.
func leaveAsItWas(dir string, installed *bool) func() {
	made := !exists(dir)
	return func() {
		if made && !*installed {
			os.Remove(dir)
		}
	}
}

// exists says whether p is there, of whatever kind.
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// progressWriter is the temporary file a provider's Restore writes into:
// the bytes counted and said, with the provider's name, each time another
// progressStep has come; once ended, every write is refused, so a
// provider that outlives the command writes nowhere.
type progressWriter struct {
	mu    sync.Mutex
	f     *os.File
	out   io.Writer
	name  string
	n     int64
	next  int64
	ended bool
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return 0, errRestoreEnded
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	if w.n >= w.next {
		fmt.Fprintf(w.out, "%s: %d bytes\n", w.name, w.n)
		w.next = (w.n/progressStep + 1) * progressStep
	}
	return n, err
}

// end stops taking writes and returns how many bytes were written.
func (w *progressWriter) end() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	return w.n
}
