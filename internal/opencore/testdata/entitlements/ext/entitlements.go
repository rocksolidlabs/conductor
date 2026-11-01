// Package ext is a fixture of internal/opencore's test: under ext/, the
// entitlement source is declared and may be named.
package ext

import "context"

// Provider is a registered provider.
type Provider interface{ Name() string }

// EntitlementSource answers what a workspace may use of a paid feature.
type EntitlementSource interface {
	Provider
	Entitled(ctx context.Context, feature string) (Entitlement, error)
}

// Entitlement is what a workspace may use of one feature.
type Entitlement struct{ Allowed bool }

// Entry is one registered provider in one slot.
type Entry struct {
	Kind  string
	Value Provider
}

// KindEntitlements is the entitlement source's kind.
const KindEntitlements = "entitlements"

var source EntitlementSource

// RegisterEntitlements registers the entitlement source.
func RegisterEntitlements(e EntitlementSource) { source = e }

// Entitlements answers the registered entitlement source.
func Entitlements() (EntitlementSource, bool) { return source, source != nil }
