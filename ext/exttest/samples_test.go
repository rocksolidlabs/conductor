package exttest

import (
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// FullQuery names as many decisions and reviews as a query holds, and the
// query stays one Valid takes; the query it is made from is left as it
// was.
func TestFullQueryIsAtItsBounds(t *testing.T) {
	q := SampleQuery(t.TempDir())
	q.Run = "exttest-0a0b0c0d" // the sample's run is a name, not a run's id
	full := FullQuery(q)
	if err := full.Valid(); err != nil {
		t.Fatalf("the full query: %v", err)
	}
	if len(full.Decisions) != ext.MaxContextDecisions || len(full.Reviews) != ext.MaxContextReviews {
		t.Fatalf("%d decisions, %d reviews", len(full.Decisions), len(full.Reviews))
	}
	for _, rv := range full.Reviews {
		if len(rv.Findings) != ext.MaxContextReviewFindings {
			t.Fatalf("review %s has %d findings", rv.ID, len(rv.Findings))
		}
	}
	if q.Decisions != nil || q.Reviews != nil {
		t.Fatal("FullQuery changed the query it was given")
	}
}
