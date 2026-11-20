package brakeman

import (
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// A notification's ref (ext.ParseDecisionRef, ext.DecisionRef) is written
// again in ext, which cannot import this package: over a table of refs it
// takes exactly those ParseRef reads as a decision's, and DecisionRef
// writes what Ref.String writes.
func TestDecisionRefIsBrakemansForm(t *testing.T) {
	refs := []string{
		"decision:dr-1", "decision:dr-7", "decision:dr-10", "decision:dr-999999999999999999",
		"decision:dr-1000000000000000000", "decision:dr-0", "decision:dr-07", "decision:dr-",
		"decision:7", "decision:dr-7 ", " decision:dr-7", "decision:dr-7\n", "decision:dr-7\ndecision:dr-8",
		"decision:dr-2,dr-7", "decisions:dr-2", "decision:dr-7/1", "decision:DR-7", "decision:dr--1",
		"decision:dr-+1", "decision:", "decision", "dr-7", "question:x/y", "",
		"decision:dr-" + strings.Repeat("9", 17), "decision:dr-1" + strings.Repeat("0", 17), "decision:dr-1" + strings.Repeat("0", 18),
	}
	for _, s := range refs {
		r, err := ParseRef(s)
		brakemans := err == nil && r.Kind == RefDecision
		id, ok := ext.ParseDecisionRef(s)
		if ok != brakemans {
			t.Errorf("%q: ext takes it %v, Brakeman reads it as a decision's %v (%v)", s, ok, brakemans, err)
			continue
		}
		if !ok {
			continue
		}
		if id != r.Decision {
			t.Errorf("%q: ext's id %q, Brakeman's %q", s, id, r.Decision)
		}
		if got, want := ext.DecisionRef(id), r.String(); got != want || got != s {
			t.Errorf("%q: DecisionRef writes %q, Ref.String %q", s, got, want)
		}
	}
}
