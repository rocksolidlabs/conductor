package exttest

import (
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// TestSuitePassesEveryDefault runs the suite over ext/local's defaults: in
// the typed slots, as local.Fill puts them, and as entries of All, the
// entitlement source's among them, as a registry holding them would.
func TestSuitePassesEveryDefault(t *testing.T) {
	n := NewNetwork()
	RunAll(t, n, local.Fill(ext.Providers{}))
	RunAll(t, nil, ext.Providers{All: []ext.Entry{
		{Kind: ext.KindIdentity, Name: local.Name, Value: local.Identity{}},
		{Kind: ext.KindDecisions, Name: local.Name, Value: local.Router{}},
		{Kind: ext.KindSync, Name: local.Name, Value: local.Continuity{}},
		{Kind: ext.KindEntitlements, Name: local.Name, Value: local.AllowAll{}},
		{Kind: ext.KindMedia, Name: local.Name, Value: local.Media{}},
	}})
	if dials, _ := n.Dials(); len(dials) != 0 {
		t.Errorf("the defaults dialled %d times; they reach no service", len(dials))
	}
}
