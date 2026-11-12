package api

import (
	"context"
	"net/http"

	"github.com/rocksolidlabs/conductor/ext"
)

// GET /api/capabilities (docs/plans/ext.md, C9): what the composition root
// registered, so the web app shows what is there instead of checking a plan
// (decision O16). Each row is a provider's kind, its name and its state,
// and nothing a caller could gate a feature on: no value of the provider is
// read, and no provider is asked anything to answer it but the identity
// provider its detail, who the server is signed in as (docs/plans/
// identity-uplink.md), through the guard. A plain build answers
// {"providers": []}.
//
// A kind joins the reply by being in ext.Providers.All: the route lists
// every entry there, whatever its kind (the entitlement source's and the
// runtime's slots among them), so a new kind needs no change here. A field
// a later kind needs (a runtime's where) is added to capabilityRow,
// omitted when empty, so a reader of today's rows reads tomorrow's.

// capabilitiesReplyMax is what the reply may weigh at most: one row per
// one-value slot and MaxPerList per list, each name at its longest, fits
// it (TestCapabilitiesReplyBounded).
const capabilitiesReplyMax = 8 << 10

// capabilityRow is one registered provider in one slot.
type capabilityRow struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// State is extStarted ("ready") once the provider's Start has
	// succeeded, or for one that needs none, and extStarting ("starting")
	// until then: its Start is tried again, and its slot answers the free
	// default meanwhile, so the app never claims a feature that is not
	// there.
	State string `json:"state"`
	// Detail is who the server is signed in as, on the identity
	// provider's row alone, from an ext.IdentityDetail that has started;
	// left out otherwise and in the guide's rows.
	Detail string `json:"detail,omitempty"`
}

type capabilitiesReply struct {
	Providers []capabilityRow `json:"providers"`
}

// handleCapabilities answers GET /api/capabilities: every registered
// provider, in the registry's order (the kinds' order, then a list's
// registration order), with its state. Admin only (decision E16). The
// identity provider is asked one thing, through the guard: its detail.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.capabilities(r.Context(), true))
}

// capabilities is the reply: never nil rows, so a plain build's is [].
// withDetail asks the identity provider its detail (guardIdentity.detail);
// one detail of 80 runes is at most 320 bytes, inside capabilitiesReplyMax.
func (s *Server) capabilities(ctx context.Context, withDetail bool) capabilitiesReply {
	regs := s.Registered()
	out := capabilitiesReply{Providers: make([]capabilityRow, 0, len(regs))}
	for _, r := range regs {
		row := capabilityRow{Kind: r.Kind, Name: r.Name, State: s.extState(r.Name)}
		if g, ok := s.exts.identity.(guardIdentity); ok && withDetail && r.Kind == ext.KindIdentity {
			row.Detail, _ = g.detail(ctx)
		}
		out.Providers = append(out.Providers, row)
	}
	return out
}
