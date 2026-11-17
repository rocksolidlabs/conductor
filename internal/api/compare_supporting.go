package api

import (
	"context"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// comparisonSupporting is what was said of a comparison beside what was
// measured: the candidates' claims, their reviews and Brakeman's draft.
type comparisonSupporting struct{}

// comparisonSupporting reads the supporting part of the comparison c: nil
// until it is built.
func (s *Server) comparisonSupporting(ctx context.Context, c crew.Comparison) *comparisonSupporting {
	return nil
}
