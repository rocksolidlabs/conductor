package api

import (
	"context"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// comparisonSelection is the selection part of a comparison: its record,
// the options, the answer and how it was applied.
type comparisonSelection struct{}

// comparisonSelection reads the selection part of the comparison c: nil
// until it is built.
func (s *Server) comparisonSelection(ctx context.Context, c crew.Comparison) *comparisonSelection {
	return nil
}
