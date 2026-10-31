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
	Authenticate(ctx context.Context, token string) (p Principal, ok bool, err error)
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
func (g Grant) Valid() bool { return g == GrantAdmin || g == GrantHost }

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
			return invalid(r, "grant %q is not admin or host", g)
		}
		if seen[g] {
			return invalid(r, "grant %q is given twice", g)
		}
		seen[g] = true
	}
	return nil
}
