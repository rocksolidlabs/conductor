package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// refused restores b into a fresh directory (holding what has says) and
// fails t unless the restore is refused with words containing want and
// the directory is as it was, nothing written into it.
func refused(t *testing.T, b []byte, want string, has ...[2]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range has {
		put(t, dir, h[0], h[1])
	}
	before := listing(t, dir)
	_, err := restore(b, dir)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the restore: %v, not refused with %q", err, want)
	}
	sameListing(t, dir, before)
}

// An archive the installer refuses, whole, before a byte is written: a
// directory that is not fresh, a hash or a size that differs, an entry
// the manifest does not list or one it lists that is not there, "..", an
// absolute path, a backslash, a link, a duplicate, past MaxFiles, a
// manifest that is not last, of another format, or with a field it does
// not know.
func TestRestoreRefuses(t *testing.T) {
	good := [2]string{"crews/team.json", `{"id":"team"}`}
	refused(t, craft(t, good), "holds server data (crews)", [2]string{"crews/other.json", "{}"})
	refused(t, craft(t, good), "holds server data (catalog.json)", [2]string{"catalog.json", "{}"})

	m := export.Manifest{Format: export.Format, Files: []export.File{fileOf(good[0], []byte(good[1]))}}
	item0 := item{name: export.DataDir + good[0], data: []byte(good[1])}

	other := m
	other.Files = []export.File{fileOf(good[0], []byte("something else"))}
	other.Files[0].Size = int64(len(good[1]))
	refused(t, zipOf(t, []item{item0, manifestItem(t, other)}), "hashes to")

	short := m
	short.Files = []export.File{{Path: good[0], Size: 3, SHA256: m.Files[0].SHA256}}
	refused(t, zipOf(t, []item{item0, manifestItem(t, short)}), "its manifest says 3")

	refused(t, zipOf(t, []item{item0, {name: export.DataDir + "extra.json", data: []byte("{}")}, manifestItem(t, m)}), "not listed")

	both := m
	both.Files = append(both.Files, fileOf("missing.json", []byte("{}")))
	refused(t, zipOf(t, []item{item0, manifestItem(t, both)}), "which it does not hold")

	for _, bad := range []string{"../escape.json", "crews/../../escape.json", "/abs.json", `crews\team.json`, "C:/x.json", "crews//x.json", "./x.json", "x y.json"} {
		bm := export.Manifest{Format: export.Format, Files: []export.File{fileOf(bad, []byte("{}"))}}
		refused(t, zipOf(t, []item{{name: export.DataDir + bad, data: []byte("{}")}, manifestItem(t, bm)}), "is not one an archive may hold")
	}

	link := export.Manifest{Format: export.Format, Files: []export.File{fileOf("crews/link.json", []byte("/etc/passwd"))}}
	refused(t, zipOf(t, []item{{name: export.DataDir + "crews/link.json", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}, manifestItem(t, link)}), "is not a regular file")

	dir := export.Manifest{Format: export.Format, Files: []export.File{fileOf("crews/", nil)}}
	refused(t, zipOf(t, []item{{name: export.DataDir + "crews/", mode: fs.ModeDir | 0o700}, manifestItem(t, dir)}), "is not one an archive may hold")

	refused(t, zipOf(t, []item{item0, item0, manifestItem(t, m)}), "twice")
	twice := m
	twice.Files = append(twice.Files, twice.Files[0])
	refused(t, zipOf(t, []item{item0, manifestItem(t, twice)}), "twice")

	under := export.Manifest{Format: export.Format, Files: []export.File{fileOf("crews", []byte("x")), fileOf("crews/team.json", []byte("{}"))}}
	refused(t, zipOf(t, []item{{name: export.DataDir + "crews", data: []byte("x")}, {name: export.DataDir + "crews/team.json", data: []byte("{}")}, manifestItem(t, under)}), "is under it")

	many := export.Manifest{Format: export.Format, Files: make([]export.File, export.MaxFiles+1)}
	for i := range many.Files {
		many.Files[i] = export.File{Path: fmt.Sprintf("f/%d", i), SHA256: strings.Repeat("0", 64)}
	}
	refused(t, zipOf(t, []item{manifestItem(t, many)}), fmt.Sprintf("more than %d files", export.MaxFiles))

	refused(t, zipOf(t, []item{manifestItem(t, m), item0}), "its last entry is not")
	refused(t, zipOf(t, []item{item0}), "its last entry is not")
	future := m
	future.Format = export.Format + 1
	refused(t, zipOf(t, []item{item0, manifestItem(t, future)}), "its format is 2")
	refused(t, zipOf(t, []item{item0, {name: export.ManifestName, data: []byte(`{"format":1,"files":[],"extra":true}`)}}), "unknown field")
	refused(t, []byte("not a zip file"), "not an archive")
}

// Whatever the export leaves out the installer refuses: an archive
// holding restore.lock, tls/x, ext/p/x, workbench-token, a database's
// -wal, a .tmp file or a .restore- directory can neither plant a
// credential, a lock or a provider's state nor name the restore's own
// files, and each is refused before a byte is written. So is each in
// another case or with a trailing dot, the names a filesystem may resolve
// to the same file, and anything under a path the table names as a file.
func TestRestoreRefusesReservedPaths(t *testing.T) {
	for _, rel := range []string{export.LockFile, "tls/x", "ext/p/x", "workbench-token", "workbench-token.lock", "runs/owner.lock", "runs/managed.sqlite-wal",
		"dispatch/dispatch.sqlite-shm", "crews/team.json.tmp", ".probe-1", ".restore-0123/catalog.json", "dispatch/attachments/tmp/up",
		"Restore.Lock", "TLS/account.key", "tls./account.key", "Ext/provider/state.json", "Workbench-Token", "workbench-token.", "WORKBENCH-TOKEN.LOCK",
		"runs/Owner.Lock", "runs/managed.SQLITE-WAL", "crews/team.json.TMP", ".Probe-1", ".RESTORE-0123/catalog.json", "dispatch/Attachments/TMP/up",
		"runs/owner.lock/child", "workbench-token/child", "workbench-token.lock/child", "restore.lock/child", "Runs/Owner.Lock./child", "WORKBENCH-TOKEN/a/b"} {
		refused(t, craft(t, [2]string{"crews/team.json", "{}"}, [2]string{rel, "x"}), "which no restore takes")
	}
}

// A failure before the renames leaves nothing: at the last seam before
// them, the archive's files are staged in a .restore- directory and none
// is in the data directory itself; the restore ended there (its context
// cancelled) removes the staging directory and lets the reservation go,
// the directory as it was.
func TestRestoreStages(t *testing.T) {
	b := craft(t, [2]string{"catalog.json", `{"agents":[]}`}, [2]string{"crews/team.json", `{"id":"team"}`})
	dir := filepath.Join(t.TempDir(), "data")
	put(t, dir, "hooks/claude-settings.json", "{}")
	before := listing(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var staged string
	export.SetBeforeRenames(t, func() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			switch name := e.Name(); {
			case strings.HasPrefix(name, ".restore-"):
				staged = name
			case name == "catalog.json" || name == "crews":
				t.Errorf("%s is in the data directory before the renames", name)
			}
		}
		if staged == "" {
			t.Fatal("no staging directory")
		}
		if got, err := os.ReadFile(filepath.Join(dir, staged, "crews", "team.json")); err != nil || string(got) != `{"id":"team"}` {
			t.Fatalf("the staged crew: %q %v", got, err)
		}
		cancel()
	})
	_, err := export.Restore(ctx, bytes.NewReader(b), int64(len(b)), dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the restore: %v", err)
	}
	sameListing(t, dir, before)
}

// Reserve is exclusive: a second reservation, and a restore, are refused
// with restore.lock named; the file holds the pid and the process's start;
// Release removes it once, and only the file it made; a Restore holds it,
// the same file, through every rename and lets it go after the last.
func TestReserve(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	res, err := export.Reserve(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, export.LockFile)
	stamp, err := os.ReadFile(lock)
	if err != nil || !strings.Contains(string(stamp), fmt.Sprintf("pid %d since ", os.Getpid())) {
		t.Fatalf("restore.lock: %q %v", stamp, err)
	}
	if _, err := export.Reserve(dir); !errors.Is(err, export.ErrReserved) || !strings.Contains(err.Error(), lock) {
		t.Fatalf("a second reservation: %v", err)
	}
	b := craft(t, [2]string{"crews/team.json", "{}"})
	if _, err := restore(b, dir); !errors.Is(err, export.ErrReserved) {
		t.Fatalf("a restore of a reserved directory: %v", err)
	}
	res.Release()
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released, restore.lock: %v", err)
	}
	res.Release()
	// A file of someone else's in its place is not the reservation's to
	// remove.
	res, err = export.Reserve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte("pid 1 since then\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res.Release()
	if got, err := os.ReadFile(lock); err != nil || string(got) != "pid 1 since then\n" {
		t.Fatalf("another's restore.lock: %q %v", got, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}

	b = craft(t, [2]string{"catalog.json", `{"agents":[]}`}, [2]string{"crews/team.json", "{}"}, [2]string{"runs/team-00000001.json", "{}"})
	var renamed []string
	export.SetBeforeRename(t, func(name string) {
		got, err := os.ReadFile(lock)
		if err != nil || !bytes.Equal(got, stamp) {
			t.Fatalf("restore.lock at the rename of %s: %q %v", name, got, err)
		}
		if _, err := export.Reserve(dir); !errors.Is(err, export.ErrReserved) {
			t.Fatalf("a reservation during the renames: %v", err)
		}
		renamed = append(renamed, name)
	})
	if _, err := restore(b, dir); err != nil {
		t.Fatal(err)
	}
	if strings.Join(renamed, " ") != "catalog.json crews runs" {
		t.Fatalf("renamed %v", renamed)
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after the renames, restore.lock: %v", err)
	}
}

// Install runs under the reservation its caller holds: it installs and
// leaves restore.lock in place for the caller to release; with a
// reservation already released it is refused before a byte is written.
func TestInstallUnderItsCallersReservation(t *testing.T) {
	b := craft(t, [2]string{"crews/team.json", `{"id":"team"}`})
	dir := filepath.Join(t.TempDir(), "data")
	res, err := export.Reserve(dir)
	if err != nil {
		t.Fatal(err)
	}
	stamp, _ := os.ReadFile(filepath.Join(dir, export.LockFile))
	if _, err := export.Install(context.Background(), res, bytes.NewReader(b), int64(len(b))); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, export.LockFile)); err != nil || !bytes.Equal(got, stamp) {
		t.Fatalf("after Install, restore.lock: %q %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "crews", "team.json")); err != nil || string(got) != `{"id":"team"}` {
		t.Fatalf("installed: %q %v", got, err)
	}
	res.Release()

	other := filepath.Join(t.TempDir(), "other")
	res, err = export.Reserve(other)
	if err != nil {
		t.Fatal(err)
	}
	res.Release()
	before := listing(t, other)
	if _, err := export.Install(context.Background(), res, bytes.NewReader(b), int64(len(b))); err == nil || !strings.Contains(err.Error(), "not held") {
		t.Fatalf("Install under a released reservation: %v", err)
	}
	sameListing(t, other, before)
	if _, err := export.Install(context.Background(), nil, bytes.NewReader(b), int64(len(b))); err == nil {
		t.Fatal("Install with no reservation")
	}
}

// Nothing is renamed over anything: a fresh directory holding an
// unrelated config.json refuses an archive holding one too, before any
// rename, the file as it was; an archive without it installs and keeps
// it. hooks/, which a fresh directory may hold, is the one entry replaced:
// moved aside and removed with the staging directory.
func TestRestoreNeverRenamesOver(t *testing.T) {
	b := craft(t, [2]string{"config.json", `{"from":"the archive"}`}, [2]string{"crews/team.json", "{}"})
	dir := filepath.Join(t.TempDir(), "data")
	put(t, dir, "config.json", `{"mine":true}`)
	before := listing(t, dir)
	export.SetBeforeRename(t, func(name string) { t.Errorf("renamed %s", name) })
	if _, err := restore(b, dir); err == nil || !strings.Contains(err.Error(), "config.json") || !strings.Contains(err.Error(), "nothing was renamed") {
		t.Fatalf("the restore: %v", err)
	}
	sameListing(t, dir, before)
	export.SetBeforeRename(t, nil)

	b = craft(t, [2]string{"crews/team.json", "{}"}, [2]string{"hooks/new.json", "new"})
	put(t, dir, "hooks/old.json", "old")
	if _, err := restore(b, dir); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"config.json": `{"mine":true}`, "crews/": "", "crews/team.json": "{}", "hooks/": "", "hooks/new.json": "new"}
	sameListing(t, dir, want)
}

// An archive whose Dispatch database names a file it does not hold is
// refused before any rename, the directory as it was.
func TestRestoreRefusesAMissingAttachment(t *testing.T) {
	f := newFixture(t)
	_, atts := f.ticket(t, [2]string{"a.txt", "alpha"}, [2]string{"b.txt", "beta"})
	b, m := archive(t, f.source())
	_, files := entries(t, b)
	drop := "dispatch/" + dispatch.AttachmentPath(atts[1].SHA256)
	cut := m
	cut.Files = nil
	var items []item
	for _, file := range m.Files {
		if file.Path == drop {
			continue
		}
		cut.Files = append(cut.Files, file)
		items = append(items, item{name: export.DataDir + file.Path, data: files[export.DataDir+file.Path]})
	}
	if len(cut.Files) != len(m.Files)-1 {
		t.Fatalf("the archive lacks %s to drop", drop)
	}
	cut.CreatedAt = time.Now().UTC()
	export.SetBeforeRename(t, func(name string) { t.Errorf("renamed %s", name) })
	refused(t, zipOf(t, append(items, manifestItem(t, cut))), atts[1].SHA256)
}

// An attachment's file whose bytes are not its name is refused before a
// byte is written, though its manifest lists the bytes' own SHA-256 and
// size: omega's under alpha's hash, and under alpha's hash in capitals.
func TestRestoreRefusesAnAttachmentNotItsName(t *testing.T) {
	f := newFixture(t)
	_, atts := f.ticket(t, [2]string{"a.txt", "alpha"})
	b, m := archive(t, f.source())
	_, files := entries(t, b)
	named := "dispatch/" + dispatch.AttachmentPath(atts[0].SHA256)
	for _, as := range []string{named, strings.ToUpper(named[:len("dispatch/attachments/")]) + strings.ToUpper(named[len("dispatch/attachments/"):])} {
		swapped := m
		swapped.Files = nil
		var items []item
		found := false
		for _, file := range m.Files {
			data := files[export.DataDir+file.Path]
			if file.Path == named {
				found = true
				file, data = fileOf(as, []byte("omega")), []byte("omega")
			}
			swapped.Files = append(swapped.Files, file)
			items = append(items, item{name: export.DataDir + file.Path, data: data})
		}
		if !found {
			t.Fatalf("the archive lacks %s", named)
		}
		export.SetBeforeRename(t, func(name string) { t.Errorf("renamed %s", name) })
		refused(t, zipOf(t, append(items, manifestItem(t, swapped))), "is an attachment's file named for")
	}
}

// A restore cancelled while it reads an entry ends there, before the
// entry's end: a large entry is not read through after its context ends.
func TestRestoreCancelledWithinAnEntry(t *testing.T) {
	// The big entry first, and after it an entry larger than the tail
	// archive/zip reads to find its directory (65 KiB at most), so that
	// only the big entry's own reads touch its bytes.
	rng := rand.NewChaCha8([32]byte{1})
	big, pad := make([]byte, 1<<20), make([]byte, 128<<10)
	rng.Read(big)
	rng.Read(pad)
	b := craft(t, [2]string{"crews/big.bin", string(big)}, [2]string{"crews/pad.bin", string(pad)})
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	from, err := zr.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancelWithin{r: bytes.NewReader(b), from: from, to: from + int64(zr.File[0].CompressedSize64), cancel: cancel}
	dir := filepath.Join(t.TempDir(), "data")
	_, err = export.Restore(ctx, r, int64(len(b)), dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the restore: %v", err)
	}
	if got := r.read.Load(); got >= r.to-r.from {
		t.Fatalf("%d of the entry's %d bytes read after its context ended", got, r.to-r.from)
	}
	sameListing(t, dir, map[string]string{})
}

// cancelWithin is r, cancelling at its first read of [from, to) and
// counting the bytes of that range it is asked for.
type cancelWithin struct {
	r        io.ReaderAt
	from, to int64
	cancel   func()
	read     atomic.Int64
}

func (c *cancelWithin) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	if lo, hi := max(off, c.from), min(off+int64(n), c.to); lo < hi {
		c.read.Add(hi - lo)
		c.cancel()
	}
	return n, err
}

// Two paths a filesystem that folds case or trailing dots resolves to one
// name, spelled two ways in the archive, are refused before a byte is
// written: two files, two directories over files, a file and a directory.
// Staged on such a filesystem, both would land in one place and the
// renames would stop part way.
func TestRestoreRefusesAliases(t *testing.T) {
	for _, pair := range [][2]string{
		{"Crews/a.json", "crews/b.json"},
		{"crews/Team.json", "crews/team.json"},
		{"crews./a.json", "crews/b.json"},
		{"runs/x/a.json", "runs/X./b.json"},
		{"Links", "links/a.json"},
	} {
		refused(t, craft(t, [2]string{pair[0], "{}"}, [2]string{pair[1], "{}"}), "are one name on a filesystem that folds")
	}
}

// A name made in the data directory after the last check, while the
// restore runs, is never renamed over: a file made before the rename of
// its name keeps its bytes, and an empty directory made before the rename
// of its name stays empty; the restore stops there with fs.ErrExist.
func TestRestoreNeverRenamesOverANameMadeMeanwhile(t *testing.T) {
	b := craft(t, [2]string{"catalog.json", `{"agents":[]}`}, [2]string{"crews/team.json", "{}"})

	dir := filepath.Join(t.TempDir(), "data")
	export.SetBeforeRename(t, func(name string) {
		if name == "catalog.json" {
			put(t, dir, "catalog.json", "mine")
		}
	})
	if _, err := restore(b, dir); !errors.Is(err, fs.ErrExist) || !strings.Contains(err.Error(), "nothing was renamed over it") {
		t.Fatalf("the restore: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "catalog.json")); err != nil || string(got) != "mine" {
		t.Fatalf("the file made meanwhile: %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "crews")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renamed past the refusal, crews: %v", err)
	}

	dir = filepath.Join(t.TempDir(), "data")
	export.SetBeforeRename(t, func(name string) {
		if name == "crews" {
			if err := os.Mkdir(filepath.Join(dir, "crews"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	})
	if _, err := restore(b, dir); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("the restore: %v", err)
	}
	if got, err := os.ReadDir(filepath.Join(dir, "crews")); err != nil || len(got) != 0 {
		t.Fatalf("the directory made meanwhile: %v %v", got, err)
	}
}
