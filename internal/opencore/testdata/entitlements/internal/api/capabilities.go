// Package api is a fixture of internal/opencore's test: outside ext/, the
// entitlement source's kind and the registry's entries may be named, and a
// provider listed by kind, as GET /api/capabilities lists it.
package api

import (
	_ "github.com/rocksolidlabs/conductor/ext/local"

	"github.com/rocksolidlabs/conductor/ext"
)

// kinds lists the kinds of the registered providers, the entitlement
// source's among them.
func kinds(all []ext.Entry) (out []string, entitlements bool) {
	for _, e := range all {
		out = append(out, e.Kind)
		entitlements = entitlements || e.Kind == ext.KindEntitlements
	}
	return out, entitlements
}
