package export_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/export"
)

// The history's index (docs/plans/history.md) is derived: an export
// leaves it out, with the file a damaged one was moved to and the history's
// owner lock, and a restore refuses an archive that holds any of them;
// the session records and their counter ride in the archive.
func TestTheIndexIsLeftOutOfAnExportAndRefusedInOne(t *testing.T) {
	f := newFixture(t)
	for rel, content := range map[string]string{
		"history/index.sqlite":                   "SQLite format 3",
		"history/index.sqlite-wal":               "wal",
		"history/index.sqlite.damaged":           "junk",
		"history/owner.lock":                     "pid 1",
		"history/state.json":                     `{"v":1,"seq":64}`,
		"history/sessions/0123456789abcdef.json": `{"v":1,"id":"0123456789abcdef"}`,
	} {
		put(t, f.dir, rel, content)
	}
	b, m := archive(t, f.source())
	left := map[string]string{}
	for _, l := range m.Left {
		left[l.Path] = l.Why
	}
	for p, why := range map[string]string{
		"history/index.sqlite":         export.WhyDerived,
		"history/index.sqlite-wal":     export.WhyTemporary,
		"history/index.sqlite.damaged": export.WhyDerived,
		"history/owner.lock":           export.WhyLock,
	} {
		if left[p] != why {
			t.Errorf("%s: left %q, not %q", p, left[p], why)
		}
	}
	names, _ := entries(t, b)
	for _, name := range names {
		if strings.Contains(name, "index.sqlite") || strings.HasSuffix(name, "owner.lock") {
			t.Errorf("the archive holds %s", name)
		}
	}
	for _, want := range []string{"history/state.json", "history/sessions/0123456789abcdef.json"} {
		if !slices.Contains(names, export.DataDir+want) {
			t.Errorf("the archive does not hold %s: %v", want, names)
		}
	}
	for _, rel := range []string{"history/index.sqlite", "History/Index.SQLITE", "history/index.sqlite.damaged", "history/owner.lock"} {
		refused(t, craft(t, [2]string{"crews/team.json", "{}"}, [2]string{rel, "x"}), "which no restore takes")
	}
}
