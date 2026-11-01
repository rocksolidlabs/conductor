// Package export is the data directory as one archive and back
// (docs/plans/ext.md, Program item C6): Write puts a data directory into a
// zip file, consistent while a server writes it, and Install (Restore)
// checks one and puts it into a data directory with no server data, on
// any machine, where the next `conductor serve` opens it as its own.
//
// The archive holds data/ as the data directory holds it, the two SQLite
// databases as snapshots (VACUUM INTO), the attachment files Dispatch's
// snapshot requires, and conductor-export.json last: the manifest, every
// file with its size and SHA-256, and what was left out and why. One
// table (leftOut) says what Write leaves out and Install refuses. The
// archive holds saved agents' env values in full, the configured ones they
// inherit among them, so that the restored agents launch as the source's
// did with no config: it is the person's own file, and its writers make it
// 0600.
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// Format is the archive's format, which the manifest names: Install takes
// this one only.
const Format = 1

// ManifestName is the manifest's entry, the archive's last; every other
// entry is under DataDir.
const (
	ManifestName = "conductor-export.json"
	DataDir      = "data/"
)

// Bounds of an archive: its files, and its manifest's bytes.
const (
	MaxFiles    = 100_000
	MaxManifest = 32 << 20
)

// The paths, relative to the data directory, an export takes in its own
// way: the ledger's and Dispatch's databases (snapshots), Dispatch's
// attachments (the files its snapshot requires), the catalog (resolved).
const (
	LedgerPath      = "runs/" + ledger.File
	DispatchPath    = "dispatch/" + dispatch.File
	attachmentsPath = "dispatch/attachments"
	catalogPath     = "catalog.json"
	runsPath        = "runs"
)

// Manifest is conductor-export.json, the archive's last entry.
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"version"` // the version that wrote it
	CreatedAt time.Time `json:"createdAt"`
	Files     []File    `json:"files"`
	Left      []Left    `json:"left,omitempty"` // what the export left out, and why
}

// File is a file the archive holds, at DataDir + Path.
type File struct {
	Path   string `json:"path"` // under data/, slash separated
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Left is a path the export left out (a directory's ends in a slash), and
// why: one of the Why constants.
type Left struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// Source is what Write reads: the data directory, a snapshot of one of
// its two databases (LedgerPath, DispatchPath) into dst, a file that does
// not exist yet (a running server's own connections; a stopped one's,
// ledger.SnapshotFile and dispatch.SnapshotFile), and catalog.json
// resolved against the configured catalog (ResolveCatalog), asked only
// when the data directory holds one; with no Catalog the file is copied
// as it is.
type Source struct {
	Dir      string
	Snapshot func(ctx context.Context, rel, dst string) error
	Catalog  func() ([]byte, error)
}

// dispatchSnapshots is how many snapshots of Dispatch Write takes at most
// before a file one names is still missing.
const dispatchSnapshots = 3

// Seams a test sets (export_test.go): after the runs' records are
// copied, and after each snapshot of Dispatch (its number, from 1).
var (
	afterRecords          func()
	afterDispatchSnapshot func(n int)
)

// Write writes the data directory src.Dir to w as an archive and returns
// its manifest. Its order is what keeps the archive consistent while a
// server writes: the runs' records first, then the ledger's snapshot (a
// record may lag its ledger, never lead it; the restored server's
// syncRecords mends one that lags), then Dispatch's snapshot and the
// attachment files it requires (a snapshot taken again, at most three,
// when one went meanwhile), then the other documents and files, then the
// manifest. A document written meanwhile is one version or the other,
// never torn: the store renames. Every file is read through an os.Root on
// the data directory. Nothing is buffered whole; w gets the archive as it
// is made, so an error leaves it cut short, with no manifest, which no
// restore takes.
func Write(ctx context.Context, w io.Writer, src Source) (Manifest, error) {
	if src.Snapshot == nil {
		return Manifest{}, errors.New("export: no snapshot of the databases")
	}
	root, err := os.OpenRoot(src.Dir)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	tmp, err := os.MkdirTemp("", "conductor-export-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	e := &writer{
		ctx:  ctx,
		src:  src,
		root: root,
		tmp:  tmp,
		zw:   zip.NewWriter(w),
		done: map[string]bool{},
		m:    Manifest{Format: Format, Version: version.String(), CreatedAt: time.Now().UTC().Truncate(time.Second), Files: []File{}},
	}
	if err := e.write(); err != nil {
		return Manifest{}, err
	}
	return e.m, nil
}

// writer is one Write.
type writer struct {
	ctx  context.Context
	src  Source
	root *os.Root
	tmp  string
	zw   *zip.Writer
	m    Manifest
	// done is the paths taken already (or left), which the walk skips.
	done map[string]bool
}

func (e *writer) write() error {
	if err := e.records(); err != nil {
		return err
	}
	if afterRecords != nil {
		afterRecords()
	}
	if err := e.ledger(); err != nil {
		return err
	}
	if err := e.dispatch(); err != nil {
		return err
	}
	if err := e.rest(); err != nil {
		return err
	}
	return e.manifest()
}

// records copies the runs' records, runs/<id>.json.
func (e *writer) records() error {
	if ok, err := e.isDir(runsPath); err != nil || !ok {
		return err
	}
	entries, err := fs.ReadDir(e.root.FS(), runsPath)
	if err != nil {
		return err
	}
	for _, d := range entries {
		rel := runsPath + "/" + d.Name()
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".json") || !validPath(rel) {
			continue // the walk lists it
		}
		if _, ok := reserved(rel); ok {
			continue
		}
		if err := e.copyFile(rel); err != nil {
			return err
		}
	}
	return nil
}

// ledger snapshots the ledger's database, when the data directory has one.
func (e *writer) ledger() error {
	if ok, err := e.database(LedgerPath); err != nil || !ok {
		return err
	}
	return e.addPath(LedgerPath, e.snapshotPath(LedgerPath, 1))
}

// dispatch snapshots Dispatch's database, when the data directory has one,
// and copies the attachment files the snapshot's live rows require, read
// from the snapshot itself (never a walk of attachments/: a file the
// snapshot names may have been removed and swept since). A missing one
// has the snapshot taken again, which no longer names a file a remove
// tombstoned before its sweep; only the last snapshot goes in.
func (e *writer) dispatch() error {
	if ok, err := e.database(DispatchPath); err != nil || !ok {
		return err
	}
	copied := map[string]bool{}
	var missing []string
	for n := 1; n <= dispatchSnapshots; n++ {
		if n > 1 {
			if err := e.snapshot(DispatchPath, n); err != nil {
				return err
			}
		}
		if afterDispatchSnapshot != nil {
			afterDispatchSnapshot(n)
		}
		dst := e.snapshotPath(DispatchPath, n)
		sums, err := dispatch.SnapshotHashes(e.ctx, dst)
		if err != nil {
			return err
		}
		missing = missing[:0]
		for _, sum := range sums {
			if copied[sum] {
				continue
			}
			rel := "dispatch/" + dispatch.AttachmentPath(sum)
			f, err := e.open(rel)
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, sum)
				continue
			}
			if err != nil {
				return err
			}
			file, err := e.add(rel, f)
			f.Close()
			if err != nil {
				return err
			}
			if file.SHA256 != sum {
				return fmt.Errorf("export: %s holds bytes whose SHA-256 is %s, not its name", rel, file.SHA256)
			}
			copied[sum] = true
		}
		if len(missing) == 0 {
			return e.addPath(DispatchPath, dst)
		}
	}
	return fmt.Errorf("export: Dispatch's attachment %s, which a live row names, is missing after %d snapshots", missing[0], dispatchSnapshots)
}

// database checks the database at rel and takes its first snapshot: false
// when the data directory has none, or something not a regular file in
// its place (left out).
func (e *writer) database(rel string) (bool, error) {
	if ok, err := e.isDir(path.Dir(rel)); err != nil || !ok {
		return false, err
	}
	e.done[rel] = true
	fi, err := e.root.Lstat(filepath.FromSlash(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		e.leave(rel, WhyNotRegular)
		return false, nil
	}
	if err := e.snapshot(rel, 1); err != nil {
		return false, err
	}
	return true, nil
}

// isDir says the data directory's rel is a directory itself, not a link
// to one (which the walk lists as left out) nor missing.
func (e *writer) isDir(rel string) (bool, error) {
	fi, err := e.root.Lstat(filepath.FromSlash(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

func (e *writer) snapshotPath(rel string, n int) string {
	return filepath.Join(e.tmp, fmt.Sprintf("%s.%d", filepath.Base(rel), n))
}

func (e *writer) snapshot(rel string, n int) error {
	if err := e.src.Snapshot(e.ctx, rel, e.snapshotPath(rel, n)); err != nil {
		return fmt.Errorf("export: the snapshot of %s: %w", rel, err)
	}
	return nil
}

// rest walks the data directory for every other document and file, in
// lexical order, leaving out what leftOut names and anything not a
// regular file or a directory.
func (e *writer) rest() error {
	// A data directory that holds the temporary directory (os.TempDir
	// itself) does not export it.
	tmpRel, _ := filepath.Rel(e.src.Dir, e.tmp)
	return fs.WalkDir(e.root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := e.ctx.Err(); err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == attachmentsPath && d.IsDir() {
			// What the snapshot requires is in already (dispatch); the
			// rest is orphans the next sweep deletes, and the uploads'
			// temporary files.
			if fi, err := e.root.Lstat(filepath.FromSlash(attachmentsPath + "/tmp")); err == nil {
				why, _ := reserved(attachmentsPath + "/tmp")
				e.leave(attachmentsPath+"/tmp"+slashIf(fi.IsDir()), why)
			}
			return fs.SkipDir
		}
		if e.done[rel] || filepath.FromSlash(rel) == tmpRel {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if why, ok := reserved(rel); ok {
			e.leave(rel+slashIf(d.IsDir()), why)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !validPath(rel) {
			e.leave(rel+slashIf(d.IsDir()), WhyName)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			e.leave(rel, WhyNotRegular)
			return nil
		case rel == catalogPath && e.src.Catalog != nil:
			b, err := e.src.Catalog()
			if err != nil {
				return fmt.Errorf("export: the catalog: %w", err)
			}
			_, err = e.add(rel, bytes.NewReader(b))
			return err
		}
		return e.copyFile(rel)
	})
}

func slashIf(dir bool) string {
	if dir {
		return "/"
	}
	return ""
}

// leave lists rel as left out, for why.
func (e *writer) leave(rel, why string) {
	e.done[strings.TrimSuffix(rel, "/")] = true
	e.m.Left = append(e.m.Left, Left{Path: rel, Why: why})
}

// copyFile adds the data directory's regular file at rel.
func (e *writer) copyFile(rel string) error {
	e.done[rel] = true
	f, err := e.open(rel)
	if errors.Is(err, errNotRegular) {
		e.leave(rel, WhyNotRegular)
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		// Deleted since it was listed: not in the archive.
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = e.add(rel, f)
	return err
}

var errNotRegular = errors.New("not a regular file")

// open opens the data directory's regular file at rel through the root:
// what is there is looked at first (a link or a pipe is never opened),
// and the file opened must be that one, else it is looked at again (a
// save renamed a new version in between).
func (e *writer) open(rel string) (*os.File, error) {
	name := filepath.FromSlash(rel)
	for range 3 {
		fi, err := e.root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, errNotRegular
		}
		f, err := e.root.Open(name)
		if err != nil {
			return nil, err
		}
		ofi, err := f.Stat()
		if err == nil && os.SameFile(fi, ofi) {
			return f, nil
		}
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("export: %s keeps changing as it is read", rel)
}

// addPath adds the file at path (a snapshot) as rel.
func (e *writer) addPath(rel, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = e.add(rel, f)
	return err
}

// add writes r's bytes as the entry DataDir + rel and lists it.
func (e *writer) add(rel string, r io.Reader) (File, error) {
	if len(e.m.Files) >= MaxFiles {
		return File{}, fmt.Errorf("export: the data directory holds more than %d files", MaxFiles)
	}
	h := &zip.FileHeader{Name: DataDir + rel, Method: zip.Deflate, Modified: e.m.CreatedAt}
	h.SetMode(0o600)
	zf, err := e.zw.CreateHeader(h)
	if err != nil {
		return File{}, err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(zf, sum), ctxReader{e.ctx, r})
	if err != nil {
		return File{}, fmt.Errorf("export: %s: %w", rel, err)
	}
	file := File{Path: rel, Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}
	e.m.Files = append(e.m.Files, file)
	return file, nil
}

// manifest writes the manifest, the archive's last entry, and ends the
// archive.
func (e *writer) manifest() error {
	b, err := json.MarshalIndent(e.m, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxManifest {
		return fmt.Errorf("export: the manifest is %d bytes, past its bound of %d", len(b), MaxManifest)
	}
	h := &zip.FileHeader{Name: ManifestName, Method: zip.Deflate, Modified: e.m.CreatedAt}
	h.SetMode(0o600)
	zf, err := e.zw.CreateHeader(h)
	if err != nil {
		return err
	}
	if _, err := zf.Write(append(b, '\n')); err != nil {
		return err
	}
	return e.zw.Close()
}

// ctxReader is r, ended by ctx between reads.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
