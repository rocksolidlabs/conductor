// Package local is a fixture of internal/opencore's test: under ext/, the
// free entitlement source is declared and answers.
package local

import (
	"context"

	"github.com/rocksolidlabs/conductor/ext"
)

var _ ext.EntitlementSource = AllowAll{}

// AllowAll allows every feature.
type AllowAll struct{}

// Name is the default's name.
func (AllowAll) Name() string { return "local" }

// Entitled allows the feature.
func (AllowAll) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}
