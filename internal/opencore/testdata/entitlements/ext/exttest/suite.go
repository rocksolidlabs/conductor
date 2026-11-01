// Package exttest is a fixture of internal/opencore's test: the contract
// suite, under ext/, asks the entitlement source as a check of it.
package exttest

import (
	"context"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// Entitlements checks an entitlement source against the free default.
func Entitlements(ctx context.Context, e ext.EntitlementSource) bool {
	got, err := e.Entitled(ctx, "feature")
	want, _ := local.AllowAll{}.Entitled(ctx, "feature")
	return err == nil && got == want
}
