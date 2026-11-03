package api

import "net/http"

// GET /api/capabilities (docs/plans/ext.md, C9): what the composition root
// registered, so the web app shows what is there instead of checking a plan
// (decision O16). Each row is a provider's kind, its name and its state,
// and nothing a caller could gate a feature on: no value of the provider is
// read, and no provider is asked anything to answer it. A plain build
// answers {"providers": []}.
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
}

type capabilitiesReply struct {
	Providers []capabilityRow `json:"providers"`
}

// handleCapabilities answers GET /api/capabilities: every registered
// provider, in the registry's order (the kinds' order, then a list's
// registration order), with its state. Admin only (decision E16).
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.capabilities())
}

// capabilities is the reply: never nil rows, so a plain build's is [].
func (s *Server) capabilities() capabilitiesReply {
	regs := s.Registered()
	out := capabilitiesReply{Providers: make([]capabilityRow, 0, len(regs))}
	for _, r := range regs {
		out.Providers = append(out.Providers, capabilityRow{Kind: r.Kind, Name: r.Name, State: s.extState(r.Name)})
	}
	return out
}
