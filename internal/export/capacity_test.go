package export_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/export"
)

// Capacity's documents (docs/plans/capacity.md, PR-3) ride in an export as
// the plain files they are; its owner lock and the usage readers' working
// folder are left out, and a restore refuses an archive that holds either.
func TestLeftOutCapacity(t *testing.T) {
	f := newFixture(t)
	for rel, content := range map[string]string{
		"capacity/accounts.json":   `{"rev":1,"accounts":[]}`,
		"capacity/versions.json":   `{"schema":1,"checking":[],"checked":[],"turned":[],"interrupted":[]}`,
		"capacity/owner.lock":      "pid 1",
		"capacity/run/projects.js": "x",
	} {
		put(t, f.dir, rel, content)
	}
	b, m := archive(t, f.source())
	left := map[string]string{}
	for _, l := range m.Left {
		left[l.Path] = l.Why
	}
	if left["capacity/owner.lock"] != export.WhyLock {
		t.Errorf("capacity/owner.lock: left %q", left["capacity/owner.lock"])
	}
	if left["capacity/run/"] != export.WhyTemporary {
		t.Errorf("capacity/run: left %v", left)
	}
	names, _ := entries(t, b)
	for _, name := range names {
		if strings.HasSuffix(name, "owner.lock") || strings.Contains(name, "capacity/run") {
			t.Errorf("the archive holds %s", name)
		}
	}
	for _, want := range []string{"capacity/accounts.json", "capacity/versions.json"} {
		if !slices.Contains(names, export.DataDir+want) {
			t.Errorf("the archive does not hold %s: %v", want, names)
		}
	}
	for _, rel := range []string{"capacity/owner.lock", "Capacity/Owner.Lock", "capacity/run/x", "capacity/run"} {
		refused(t, craft(t, [2]string{"crews/team.json", "{}"}, [2]string{rel, "x"}), "which no restore takes")
	}
}
