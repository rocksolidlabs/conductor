package export_test

import (
	"context"
	"path/filepath"
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
