package local

import (
	"context"
	"io"

	"github.com/rocksolidlabs/conductor/ext"
)

// Each default implements its interface.
var (
	_ ext.IdentityProvider  = Identity{}
	_ ext.DecisionRouter    = Router{}
	_ ext.ContinuityStore   = Continuity{}
	_ ext.EntitlementSource = AllowAll{}
	_ ext.MediaRoomProvider = Media{}
)

// Identity is the free identity provider. It recognises no token: without
// a provider, main authenticates a request by its own chain alone (the
// workbench token, the host tokens, the share links), and a token that
// chain does not know is refused as unknown.
type Identity struct{}

// Name answers Name.
func (Identity) Name() string { return Name }

// Authenticate answers ok false for every token: not this provider's.
func (Identity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}

// Router is the free decision router. It answers the empty route and keeps
// nothing it is told: without a router, main shows a question to the
// people watching its session, and the first response wins.
type Router struct{}

// Name answers Name.
func (Router) Name() string { return Name }

// Route answers the empty route: no destination, no rule, no answer.
func (Router) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{}, nil
}

// Resolved keeps nothing.
func (Router) Resolved(context.Context, ext.Resolution) {}

// Continuity is the free sync store. It keeps nothing: without a sync
// store, main's data directory is the one copy of its documents and stays
// authoritative, and a new device starts from an empty data directory (or
// from an export).
type Continuity struct{}

// Name answers Name.
func (Continuity) Name() string { return Name }

// Saved keeps nothing and acknowledges the document.
func (Continuity) Saved(context.Context, ext.Doc) error { return nil }

// Deleted keeps nothing and acknowledges the document.
func (Continuity) Deleted(context.Context, ext.Doc) error { return nil }

// Restore writes nothing and answers ext.ErrNothingToRestore: it keeps no
// copy.
func (Continuity) Restore(context.Context, ext.RestoreRequest, io.Writer) error {
	return ext.ErrNothingToRestore
}

// AllowedWhy is why AllowAll allows a feature.
const AllowedWhy = "allowed: no entitlement source is registered"

// AllowAll is the free entitlement source. It allows every feature with no
// limit: without a source, main gates nothing on an entitlement, and the
// configuration keeps its own limits and switches (MaxSessions, the
// switchyard's, Crews.Managed). No code of this repository asks it; the
// providers of the private repositories do.
type AllowAll struct{}

// Name answers Name.
func (AllowAll) Name() string { return Name }

// Entitled answers every feature allowed, with no limit and no end, and
// AllowedWhy.
func (AllowAll) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true, Why: AllowedWhy}, nil
}

// Media is the free media room provider. It adds no ICE server and has no
// room: without a provider, main's connections use the configured ICE
// servers alone (STUN, and any TURN entries the configuration names), with
// the WebSocket relay beside them, and there are no rooms.
type Media struct{}

// Name answers Name.
func (Media) Name() string { return Name }

// ICEServers answers no server beside the configured ones.
func (Media) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return nil, nil
}

// Room answers ok false: no room.
func (Media) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, nil
}
