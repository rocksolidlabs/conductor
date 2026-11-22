package export_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/machine"
)

// No archive carries this machine's id, whatever data directory was
// chosen: one that is the machine's directory (the file at its top) and
// one that holds it (the file deeper). Each is named left out with
// WhyMachine, and no entry holds the id's bytes. An archive holding one, in
// any spelling a filesystem resolves to the same name, is refused by the
// restore before a byte is written.
func TestExportLeavesOutMachineID(t *testing.T) {
	f := newFixture(t)
	id, err := machine.ID(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	other, err := machine.ID(filepath.Join(f.dir, "home", ".config", "conductor"))
	if err != nil {
		t.Fatal(err)
	}
	b, m := archive(t, f.source())
	left := map[string]string{}
	for _, l := range m.Left {
		left[l.Path] = l.Why
	}
	for _, p := range []string{machine.FileName, "home/.config/conductor/" + machine.FileName} {
		if left[p] != export.WhyMachine {
			t.Errorf("%s left out for %q: %v", p, left[p], m.Left)
		}
	}
	names, contents := entries(t, b)
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), machine.FileName) {
			t.Errorf("the archive holds %s", name)
		}
	}
	for name, data := range contents {
		if bytes.Contains(data, []byte(id)) || bytes.Contains(data, []byte(other)) {
			t.Errorf("%s holds a machine's id", name)
		}
	}
	for _, rel := range []string{machine.FileName, "a/b/" + machine.FileName, "Machine-Id", "x/MACHINE-ID.", machine.FileName + "/child"} {
		refused(t, craft(t, [2]string{"crews/team.json", "{}"}, [2]string{rel, id + "\n"}), export.WhyMachine)
	}
}
