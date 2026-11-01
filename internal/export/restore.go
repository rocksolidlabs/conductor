package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
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
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// LockFile is the reservation's file in the data directory: a restore, or
// a starting server, holds the directory while it is there.
const LockFile = "restore.lock"

// serverData is what makes a data directory not fresh: a server's data.
// hooks/ (which `conductor host` writes, and every `serve` writes anew),
// the workbench token, ext/ (a provider's own state, a sign-in among it)
// and restore.lock aside.
var serverData = []string{"catalog.json", "crews", "crews.json", "runs", "dispatch", "links"}

// hooksDir is the one top-level entry a fresh directory may hold that an
// archive may hold too: the restore moves it aside first.
const hooksDir = "hooks"

// started is when this process started, near enough: what restore.lock
// says beside the pid.
var started = time.Now().UTC()

// ErrReserved is a reservation refused: restore.lock is there.
var ErrReserved = errors.New("export: the data directory is reserved")

// Fresh reports whether dir holds no server data: none of catalog.json,
// crews/, crews.json, runs/, dispatch/ and links/, of whatever kind. A
// directory that is not there is fresh. It is a moment's answer: a
// restore holds a Reservation around it.
func Fresh(dir string) (bool, error) {
	found, err := serverDataIn(dir)
	return found == "", err
}

// serverDataIn is the first of serverData that dir holds, or "".
func serverDataIn(dir string) (string, error) {
	for _, name := range serverData {
		_, err := os.Lstat(filepath.Join(dir, name))
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

// Reservation is restore.lock held: created exclusively by Reserve,
// removed by Release. While it is there no restore and no server takes
// the directory.
type Reservation struct {
	dir   string
	stamp []byte
	mu    sync.Mutex
	done  bool
}

// Reserve takes the data directory dir for a restore (or a starting
// server): it makes dir (0700) when it is missing and creates
// restore.lock in it exclusively (O_CREATE|O_EXCL, 0600), holding this
// process's pid and start time. A file already there refuses it with
// words naming it (ErrReserved): a restore runs, or a server starts, or
// one was cut short, and the file to remove to start over.
func Reserve(dir string) (*Reservation, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, LockFile)
	stamp := []byte("pid " + strconv.Itoa(os.Getpid()) + " since " + started.Format(time.RFC3339) + "\n")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		holder := readHolder(p)
		return nil, fmt.Errorf("%w: %s is there (%s): a restore is running or a server is starting on it, or one was cut short; if none is, remove %s to start over", ErrReserved, p, holder, p)
	}
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(stamp)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(p)
		return nil, err
	}
	return &Reservation{dir: dir, stamp: stamp}, nil
}

// readHolder is what restore.lock says of its holder, bounded.
func readHolder(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 200))
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "empty"
}

// Dir is the data directory the reservation holds.
func (r *Reservation) Dir() string { return r.dir }

// Release lets the directory go: restore.lock removed, when it is still
// the one Reserve made. A second Release does nothing.
func (r *Reservation) Release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	p := filepath.Join(r.dir, LockFile)
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, r.stamp) {
		os.Remove(p)
	}
}

// held says the reservation is held still: not released, its file there
// and its own.
func (r *Reservation) held() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return false
	}
	b, err := os.ReadFile(filepath.Join(r.dir, LockFile))
	return err == nil && bytes.Equal(b, r.stamp)
}

// Restore is Reserve, Install and Release, for an archive already at hand:
// the reservation is let go after the last rename, or the failure that
// ends the install.
func Restore(ctx context.Context, r io.ReaderAt, size int64, dir string) (Manifest, error) {
	res, err := Reserve(dir)
	if err != nil {
		return Manifest{}, err
	}
	defer res.Release()
	return Install(ctx, res, r, size)
}

// Seams a test sets (export_test.go): before the renames, once every check
// has passed; and before each rename, its name.
var (
	beforeRenames func()
	beforeRename  func(name string)
)

// Install checks the archive r (size bytes) and installs it into the data
// directory of res, under the reservation its caller holds: it never
// takes or lets go of one, and refuses one already released. It refuses
// a directory a server holds (its ledger's owner lock) or that is not
// Fresh, then checks the whole archive before it writes a byte: format 1,
// the manifest within its bound and last, every entry listed with its
// size and SHA-256 and no other, every path under data/ of [A-Za-z0-9._-]
// elements (no "..", no backslash, nothing absolute), no link, no
// duplicate, at most MaxFiles, no path leftOut names. It writes
// everything into <dir>/.restore-<random> through an os.Root (files 0600,
// directories 0700), syncs it, checks that Dispatch's staged database
// names no attachment the archive lacks, checks the directory is Fresh
// still and that no top-level name it will rename is there (hooks/ aside,
// moved into the staging directory first and removed with it), then
// renames each top-level entry into place, never over anything. A failure
// before the renames leaves the data directory as it was.
func Install(ctx context.Context, res *Reservation, r io.ReaderAt, size int64) (Manifest, error) {
	if res == nil || !res.held() {
		return Manifest{}, errors.New("export: the data directory's reservation is not held: nothing installed")
	}
	dir := res.dir
	if err := checkTarget(dir); err != nil {
		return Manifest{}, err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Manifest{}, fmt.Errorf("export: not an archive: %w", err)
	}
	m, entries, err := checkArchive(zr)
	if err != nil {
		return Manifest{}, err
	}
	// Every byte checked before one is written.
	for _, en := range entries {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if err := copyChecked(io.Discard, en); err != nil {
			return Manifest{}, err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	staging, err := stagingName()
	if err != nil {
		return Manifest{}, err
	}
	if err := root.Mkdir(staging, 0o700); err != nil {
		return Manifest{}, err
	}
	renaming := false
	defer func() {
		if !renaming {
			root.RemoveAll(staging)
		}
	}()
	tops, err := stage(ctx, root, staging, entries)
	if err != nil {
		return Manifest{}, err
	}
	if err := checkAttachments(ctx, root, dir, staging); err != nil {
		return Manifest{}, err
	}
	// Under the reservation, once more: fresh still, and no name the
	// renames will take is there.
	if err := checkTarget(dir); err != nil {
		return Manifest{}, err
	}
	moveHooks := false
	for _, name := range tops {
		_, err := root.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return Manifest{}, err
		case name == hooksDir:
			moveHooks = true
		default:
			return Manifest{}, fmt.Errorf("export: %s holds %s, which the archive holds too: nothing was renamed; move it away and restore again", dir, name)
		}
	}
	if beforeRenames != nil {
		beforeRenames()
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if moveHooks {
		// The hook assets a fresh directory may hold go with the staging
		// directory; the archive's take their place. The name has a
		// character no archive's path has.
		if err := root.Rename(hooksDir, staging+"/"+hooksDir+"~aside"); err != nil {
			return Manifest{}, fmt.Errorf("export: moving %s aside: %w", filepath.Join(dir, hooksDir), err)
		}
	}
	renaming = true
	for _, name := range tops {
		if beforeRename != nil {
			beforeRename(name)
		}
		if err := root.Rename(staging+"/"+name, name); err != nil {
			return Manifest{}, fmt.Errorf("export: the restore stopped part way, at %s: %w; remove what it renamed into %s (and %s) to start over", name, err, dir, filepath.Join(dir, staging))
		}
	}
	syncDir(dir)
	root.RemoveAll(staging)
	return m, nil
}

// checkTarget refuses a directory a server holds or that holds server
// data.
func checkTarget(dir string) error {
	held, err := ledger.Held(filepath.Join(dir, runsPath))
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("export: a server is running on %s: stop it; a restore goes into a data directory with no server data", dir)
	}
	found, err := serverDataIn(dir)
	if err != nil {
		return err
	}
	if found != "" {
		return fmt.Errorf("export: %s holds server data (%s): a restore goes into a data directory with none; choose another, or move %s away", dir, found, filepath.Join(dir, found))
	}
	return nil
}

// entry is a file of the archive with what its manifest says of it.
type entry struct {
	f    *zip.File
	file File
}

var hexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checkArchive checks the archive's shape and its manifest, before any
// byte of a file is read: the manifest last and within its bound, of
// Format, every file valid, listed once, unreserved, its entry a regular
// file of the size listed, and no entry the manifest does not list.
func checkArchive(zr *zip.Reader) (Manifest, []entry, error) {
	refuse := func(format string, args ...any) (Manifest, []entry, error) {
		return Manifest{}, nil, fmt.Errorf("export: the archive is refused: "+format, args...)
	}
	n := len(zr.File)
	if n == 0 || zr.File[n-1].Name != ManifestName {
		return refuse("its last entry is not %s", ManifestName)
	}
	if n-1 > MaxFiles {
		return refuse("it holds more than %d files", MaxFiles)
	}
	mf := zr.File[n-1]
	if !mf.Mode().IsRegular() || mf.UncompressedSize64 > MaxManifest {
		return refuse("its manifest is past its bound of %d bytes", MaxManifest)
	}
	rc, err := mf.Open()
	if err != nil {
		return refuse("its manifest: %v", err)
	}
	b, err := io.ReadAll(io.LimitReader(rc, MaxManifest+1))
	rc.Close()
	if err != nil {
		return refuse("its manifest: %v", err)
	}
	if len(b) > MaxManifest {
		return refuse("its manifest is past its bound of %d bytes", MaxManifest)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return refuse("its manifest: %v", err)
	}
	if dec.More() {
		return refuse("its manifest holds more than one value")
	}
	if m.Format != Format {
		return refuse("its format is %d; this Conductor restores format %d", m.Format, Format)
	}
	if len(m.Files) > MaxFiles {
		return refuse("it lists more than %d files", MaxFiles)
	}
	listed := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		switch {
		case !validPath(f.Path):
			return refuse("the path %q is not one an archive may hold (relative, of [A-Za-z0-9._-] elements, no '..')", f.Path)
		case f.Size < 0 || !hexPattern.MatchString(f.SHA256):
			return refuse("%s: a size or a SHA-256 that is none", f.Path)
		}
		if why, ok := reserved(f.Path); ok {
			return refuse("it holds %s, which no restore takes (%s)", f.Path, why)
		}
		if _, dup := listed[f.Path]; dup {
			return refuse("it lists %s twice", f.Path)
		}
		listed[f.Path] = f
	}
	// No file is a directory another file is under.
	for p := range listed {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if _, ok := listed[d]; ok {
				return refuse("%s is a file, and %s is under it", d, p)
			}
		}
	}
	entries := make([]entry, 0, n-1)
	seen := make(map[string]bool, n-1)
	for _, f := range zr.File[:n-1] {
		rel, ok := strings.CutPrefix(f.Name, DataDir)
		file, listedOK := listed[rel]
		switch {
		case !ok || !listedOK:
			return refuse("its entry %q is not listed in its manifest", f.Name)
		case seen[rel]:
			return refuse("it holds %s twice", f.Name)
		case !f.Mode().IsRegular():
			return refuse("%s is not a regular file (%s)", f.Name, f.Mode().Type())
		case f.UncompressedSize64 != uint64(file.Size):
			return refuse("%s is %d bytes, and its manifest says %d", f.Name, f.UncompressedSize64, file.Size)
		}
		seen[rel] = true
		entries = append(entries, entry{f: f, file: file})
	}
	if len(entries) != len(listed) {
		for p := range listed {
			if !seen[p] {
				return refuse("its manifest lists %s, which it does not hold", p)
			}
		}
	}
	return m, entries, nil
}

// copyChecked copies the entry's bytes to w, refusing them unless they are
// its size and hash to its SHA-256 (and archive/zip's CRC-32, read to its
// end, agrees).
func copyChecked(w io.Writer, en entry) error {
	rc, err := en.f.Open()
	if err != nil {
		return fmt.Errorf("export: the archive is refused: %s: %w", en.f.Name, err)
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(rc, en.file.Size+1))
	if err != nil {
		return fmt.Errorf("export: the archive is refused: %s: %w", en.f.Name, err)
	}
	if n != en.file.Size {
		return fmt.Errorf("export: the archive is refused: %s is %d bytes, and its manifest says %d", en.f.Name, n, en.file.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != en.file.SHA256 {
		return fmt.Errorf("export: the archive is refused: %s hashes to %s, and its manifest says %s", en.f.Name, got, en.file.SHA256)
	}
	return nil
}

// stagingName is .restore-<random>.
func stagingName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ".restore-" + hex.EncodeToString(b), nil
}

// stage writes every entry under the staging directory through root
// (files 0600, directories 0700, a file never over another), syncs each
// file and directory, and returns the staging directory's top-level names,
// sorted.
func stage(ctx context.Context, root *os.Root, staging string, entries []entry) ([]string, error) {
	sroot, err := root.OpenRoot(staging)
	if err != nil {
		return nil, err
	}
	defer sroot.Close()
	dirs := map[string]bool{".": true}
	var tops []string
	for _, en := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel := en.file.Path
		top, _, _ := strings.Cut(rel, "/")
		if !slices.Contains(tops, top) {
			tops = append(tops, top)
		}
		if d := path.Dir(rel); d != "." {
			if err := sroot.MkdirAll(filepath.FromSlash(d), 0o700); err != nil {
				return nil, fmt.Errorf("export: staging %s: %w", rel, err)
			}
			for ; d != "."; d = path.Dir(d) {
				dirs[d] = true
			}
		}
		f, err := sroot.OpenFile(filepath.FromSlash(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("export: staging %s: %w", rel, err)
		}
		err = copyChecked(f, en)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	if runtime.GOOS != "windows" {
		for d := range dirs {
			if df, err := sroot.Open(filepath.FromSlash(d)); err == nil {
				df.Sync()
				df.Close()
			}
		}
	}
	slices.Sort(tops)
	return tops, nil
}

// checkAttachments refuses an archive whose staged Dispatch database
// names an attachment's content the archive does not hold: every live row
// it restores must open its file.
func checkAttachments(ctx context.Context, root *os.Root, dir, staging string) error {
	db := staging + "/" + DispatchPath
	fi, err := root.Lstat(filepath.FromSlash(db))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("export: the archive is refused: %s is not a regular file", DispatchPath)
	}
	sums, err := dispatch.SnapshotHashes(ctx, filepath.Join(dir, filepath.FromSlash(db)))
	if err != nil {
		return fmt.Errorf("export: the archive is refused: its Dispatch database: %w", err)
	}
	for _, sum := range sums {
		p := staging + "/dispatch/" + dispatch.AttachmentPath(sum)
		fi, err := root.Lstat(filepath.FromSlash(p))
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("export: the archive is refused: its Dispatch database names the attachment %s, which it does not hold", sum)
		}
	}
	return nil
}

// syncDir makes a directory's entries durable where the platform can:
// on Windows a directory is not opened for a sync.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
