package ledger

import (
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// Every origin the ledger writes is one a decision's document carries
// (ext.DecisionOrigins, docs/plans/sync-seams.md): each origin constant is
// one of ext's, and a decision of any other origin is refused at its
// opening, so no record holds one.
func TestEveryOriginTheLedgerWritesIsOneTheDocumentCarries(t *testing.T) {
	for _, o := range []string{OriginAgent, OriginPlan, OriginCompare} {
		if !slices.Contains(ext.DecisionOrigins, o) {
			t.Errorf("the ledger's origin %q is not one the document carries %v", o, ext.DecisionOrigins)
		}
	}
	for _, o := range []string{"gate", "router", "", "Plan", "agents"} {
		err := checkOpening(DecisionRow{Origin: o, Type: DecisionApproval, Fingerprint: "f"})
		if err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("an opening of origin %q: %v", o, err)
		}
	}
}
