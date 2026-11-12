package ext

import (
	"context"
	"time"
)

// IdentityProvider resolves a credential the server's own chain does not
// know. Its free default recognises no token: the workbench token, the host
// tokens and the share links are all there is.
type IdentityProvider interface {
	Provider
	// Authenticate resolves a bearer token the server's own chain does not
	// know (the workbench token, the host tokens, the share links). ok
	// false: not the provider's. It answers from what it holds (a signature
	// checked against keys it keeps), never with a call per request; the
	// token is never logged or kept. Safe for concurrent use.
	//
	// It is asked again about a token it admitted for as long as a
	// long-lived connection opened with it lasts (a viewer's WebSocket, the
	// events stream), and that answer is a revocation when it is ok false,
	// another subject or a principal without the admin grant: the
	// connection is closed. A host connection (the host grant) is asked
	// about the same way. A provider whose admission has ended (its lease
	// with its service past, its workspace lapsed, its own registration
	// revoked) answers ok false, the free default, which ends the token's
	// connections at the next recheck. An error is only for a moment it
	// cannot tell (the keys it checks against out of reach for now): an
	// error ends no connection, and for a new request it is the free
	// default's answer, an unknown token.
	Authenticate(ctx context.Context, token string) (p Principal, ok bool, err error)
}

// UplinkIdentity is an identity provider that chooses what the uplink
// presents to a switchyard. Both are answered from what the provider
// holds, never with a round trip, and are asked through a guard.
type UplinkIdentity interface {
	// OwnsHostToken says configured, the host token, is the provider's
	// own: a pure check of its form. A late or panicking answer counts as
	// owned.
	OwnsHostToken(configured string) bool
	// UplinkToken is what to present to the switchyard server (its URL,
	// as the uplink dials it) for configured: ok false for nothing. When
	// the provider replaces a credential, it answers the replacement at
	// least UplinkTokenLead before the one it replaces stops being
	// admitted. It never answers configured itself when it owns it.
	UplinkToken(server, configured string) (token string, ok bool)
}

// IdentityDetail is an identity provider that says who the server is
// signed in as: one line of at most MaxIdentityDetail characters
// ("signed in as Ana · workspace Acme"), "" for nothing.
type IdentityDetail interface{ Detail() string }

const (
	MaxHostToken      = 2048 // bytes, proto.MaxHostToken
	MaxIdentityDetail = 80   // characters (runes)
	// UplinkTokenLead is how long, at least, before a credential stops
	// being admitted its replacement is answered; how much earlier is the
	// provider's choice. A renewal is in time only if it is delivered
	// before the switchyard's recheck finds the old credential ended;
	// when it is not, the connection closes and the host registers again
	// (docs/plans/identity-uplink.md, Two timers).
	UplinkTokenLead = 2 * time.Minute
)

// ValidHostToken reports whether t can be presented: 1 to MaxHostToken
// bytes of RFC 6750's token68 characters (A-Z a-z 0-9 - . _ ~ + / =),
// which a header carries as they are and JSON never escapes.
func ValidHostToken(t string) bool {
	if t == "" || len(t) > MaxHostToken {
		return false
	}
	for i := 0; i < len(t); i++ {
		switch c := t[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-' || c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=':
		default:
			return false
		}
	}
	return true
}

// Principal is who presented a credential, or who acts (an answer's By, a
// launch's By): a subject, a display name and, from Authenticate, what it
// may do here.
type Principal struct {
	Subject string  `json:"subject"`          // 1 to MaxSubject bytes of printable ASCII, no space
	Name    string  `json:"name"`             // 1 to MaxPrincipalName characters, one line
	Grants  []Grant `json:"grants,omitempty"` // from Authenticate: at least one
}

// Grant is what a principal may do here.
type Grant string

const (
	GrantAdmin Grant = "admin" // what the workbench token may do
	GrantHost  Grant = "host"  // register a hosted session, as a host token
	// GrantDecide answers a decision routed to the grant, and reads nothing
	// else (docs/plans/decide-grant.md).
	GrantDecide Grant = "decide"
)

// SubjectActor is the prefix a principal's subject takes as the actor of
// the receipts it makes: "user:" + Subject, at most 53 bytes, inside
// Dispatch's MaxActor (60) and never one of the actors this repository
// keeps for itself ("admin", "merge", "agent:<member>").
const SubjectActor = "user:"

const (
	MaxSubject       = 48
	MaxPrincipalName = 40 // proto.MaxNameLen, a viewer's display name
	IdentityWait     = time.Second
)

// Valid reports whether g is a grant this repository knows.
func (g Grant) Valid() bool { return g == GrantAdmin || g == GrantHost || g == GrantDecide }

// Valid checks p against its bounds: a subject of 1 to MaxSubject bytes of
// printable ASCII with no space, a name of 1 to MaxPrincipalName characters
// on one line, and grants this repository knows, none twice. Whether a
// principal needs a grant is its use's to say: Authenticate's answer needs
// at least one, an answer's By none.
func (p Principal) Valid() error {
	const r = "principal"
	switch {
	case p.Subject == "":
		return invalid(r, "subject is empty")
	case len(p.Subject) > MaxSubject:
		return invalid(r, "subject is %d bytes, past %d", len(p.Subject), MaxSubject)
	case !printableASCII(p.Subject):
		return invalid(r, "subject is not printable ASCII without spaces")
	}
	if err := name(r, "name", p.Name, MaxPrincipalName); err != nil {
		return err
	}
	seen := make(map[Grant]bool, len(p.Grants))
	for _, g := range p.Grants {
		if !g.Valid() {
			return invalid(r, "grant %q is not admin, host or decide", g)
		}
		if seen[g] {
			return invalid(r, "grant %q is given twice", g)
		}
		seen[g] = true
	}
	return nil
}
