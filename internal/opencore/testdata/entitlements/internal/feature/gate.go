// Package feature is a fixture of internal/opencore's test: one planted
// entitlement call site outside ext/, a free feature asking the source
// through the registry's entries without naming its type.
package feature

import (
	"context"

	plans "github.com/rocksolidlabs/conductor/ext"
)

// allowed asks whichever registered provider answers Entitled.
func allowed(ctx context.Context, all []plans.Entry) bool {
	for _, e := range all {
		if s, ok := e.Value.(interface {
			Entitled(context.Context, string) (struct{ Allowed bool }, error)
		}); ok {
			got, err := s.Entitled(ctx, "context")
			return err == nil && got.Allowed
		}
	}
	return true
}
