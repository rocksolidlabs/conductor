package export_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// notInstance is a dump's rows less the store's instance, which a restore
// writes anew (dispatch.Reissue).
func notInstance(rows []string) []string {
	return slices.DeleteFunc(slices.Clone(rows), func(r string) bool { return strings.HasPrefix(r, "store_instance: ") })
}

// restoredReissued fails unless the restored Dispatch database at got has
// one instance, another than the exported one's at was.
func restoredReissued(t *testing.T, was, got string) {
	t.Helper()
	instance := func(path string) []string {
		return slices.DeleteFunc(dump(t, path), func(r string) bool { return !strings.HasPrefix(r, "store_instance: ") })
	}
	before, after := instance(was), instance(got)
	if len(before) != 1 || len(after) != 1 || before[0] == after[0] {
		t.Fatalf("the instance: exported %v, restored %v", before, after)
	}
}

// A restore makes the restored Dispatch store a new instance and changes
// nothing else of it: every other row the exported one's, and the store
// opened over it names the new instance, so no publication prepared in
// the exported store is sent from the copy.
func TestRestoreReissues(t *testing.T) {
	f := newFixture(t)
	f.ticket(t)
	was, err := f.ds.Instance(context.Background())
	if err != nil || len(was) != 32 {
		t.Fatalf("the instance: %q %v", was, err)
	}
	b, _ := archive(t, f.source())
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := restore(b, dst); err != nil {
		t.Fatal(err)
	}
	src, got := filepath.Join(f.dir, filepath.FromSlash(export.DispatchPath)), filepath.Join(dst, filepath.FromSlash(export.DispatchPath))
	if !slices.Equal(notInstance(dump(t, src)), notInstance(dump(t, got))) {
		t.Fatal("a row other than the instance changed")
	}
	restoredReissued(t, src, got)
	ds, err := dispatch.Open(filepath.Join(dst, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	now, err := ds.Instance(context.Background())
	if err != nil || len(now) != 32 || now == was {
		t.Fatalf("the restored instance %q, the exported %q: %v", now, was, err)
	}
}

// A look at the staged Dispatch store that fails for another reason than
// its absence stops the restore: nothing is renamed into place, and the
// data directory is as it was.
func TestRestoreStopsWhenTheStoreCannotBeLooked(t *testing.T) {
	f := newFixture(t)
	f.ticket(t)
	b, _ := archive(t, f.source())
	dst := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	before := listing(t, dst)
	export.SetStagedLook(t, func(error) error { return fs.ErrPermission })
	export.SetBeforeRename(t, func(name string) { t.Errorf("renamed %s", name) })
	if _, err := restore(b, dst); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("the restore: %v", err)
	}
	sameListing(t, dst, before)
}

// A restore makes its staged Dispatch store a new instance, then syncs the
// store's folder, before any rename: what the trace records after the look
// is the folder's sync, the file holding the new instance by then, the
// renames, and the data directory's sync.
func TestRestoreSyncsTheNewInstance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows a folder is not synced: its renames write through")
	}
	f := newFixture(t)
	f.ticket(t)
	b, _ := archive(t, f.source())
	dst := filepath.Join(t.TempDir(), "restored")
	var trace []string
	var staged []byte
	looked := false
	export.SetStagedLook(t, func(err error) error {
		looked = true
		return err
	})
	export.SetSyncDir(t, func(rel string) error {
		if !looked {
			return nil
		}
		trace = append(trace, "sync "+path.Base(rel))
		if path.Base(rel) == "dispatch" {
			var err error
			if staged, err = os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel), dispatch.File)); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	export.SetBeforeRename(t, func(name string) { trace = append(trace, "rename") })
	if _, err := restore(b, dst); err != nil {
		t.Fatal(err)
	}
	if len(trace) < 3 || trace[0] != "sync dispatch" || trace[1] != "rename" || trace[len(trace)-1] != "sync ." {
		t.Fatalf("after the look: %v", trace)
	}
	ds, err := dispatch.Open(filepath.Join(dst, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	now, err := ds.Instance(context.Background())
	if err != nil || !bytes.Contains(staged, []byte(now)) {
		t.Fatalf("the staged store at its sync does not hold the new instance %q: %v", now, err)
	}
}
