package hostagent

import (
	"sync"
	"sync/atomic"
	"weak"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// linkCred is what the host knows of a link its viewers joined through:
// whether it still opens the session. The viewers of a link share one,
// each holding it for as long as it or anything it asked for lives (a
// submission finished in the background after its viewer left included), so
// that the link going reaches whatever is still under way of theirs; once
// gone it stays gone.
type linkCred struct {
	gone atomic.Pointer[error]
}

// goneFor marks the link gone, for why, unless it is already.
func (c *linkCred) goneFor(why error) { c.gone.CompareAndSwap(nil, &why) }

// err is why the link no longer opens the session, nil while it does.
func (c *linkCred) err() error {
	if c == nil {
		return nil
	}
	if why := c.gone.Load(); why != nil {
		return *why
	}
	return nil
}

// linkCreds finds the linkCred of a link by its id. It holds them weakly:
// one nobody holds any more (no viewer of the link, nothing of theirs under
// way) goes, and so is the bound of what it keeps. Its zero value is ready.
type linkCreds struct {
	mu sync.Mutex
	m  map[string]weak.Pointer[linkCred]
	// principals are the creds of the viewers that came as an identity
	// provider's principal (principal), one each, held weakly as the
	// links' are; no link_gone names them.
	principals []weak.Pointer[linkCred]
	// pruneAt is the size past which the entries gone are swept;
	// principalsAt the same for principals.
	pruneAt, principalsAt int
}

// of returns the linkCred of linkID that someone still holds, or a new one;
// nil for a viewer with no link.
func (l *linkCreds) of(linkID string) *linkCred {
	if linkID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.m[linkID].Value(); c != nil {
		return c
	}
	if l.m == nil {
		l.m = map[string]weak.Pointer[linkCred]{}
	}
	c := &linkCred{}
	l.m[linkID] = weak.Make(c)
	if len(l.m) > max(l.pruneAt, 64) {
		l.sweep()
	}
	return c
}

// principal returns a new cred for a viewer that came as an identity
// provider's principal, a credential that is not a link: it stands until
// the server's leave says the viewer was revoked (peer.closeFor marks the
// viewer itself) or the connection to the server is lost (lost), which
// may have kept that leave from coming. Like a link's, it is held for as
// long as the viewer or anything it asked for lives, and counts as a link
// held (held), so that the host pings its server while it is.
func (l *linkCreds) principal() *linkCred {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := &linkCred{}
	l.principals = append(l.principals, weak.Make(c))
	if len(l.principals) > max(l.principalsAt, 64) {
		kept := l.principals[:0]
		for _, p := range l.principals {
			if p.Value() != nil {
				kept = append(kept, p)
			}
		}
		clear(l.principals[len(kept):])
		l.principals = kept
		l.principalsAt = 2 * len(kept)
	}
	return c
}

// held says whether someone holds a link still standing: a viewer who
// joined through it, or something of theirs under way; or a principal's
// cred still standing. One the collector has not swept yet counts until
// it has.
func (l *linkCreds) held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.m {
		if c := p.Value(); c != nil && c.err() == nil {
			return true
		}
	}
	for _, p := range l.principals {
		if c := p.Value(); c != nil && c.err() == nil {
			return true
		}
	}
	return false
}

// sweep forgets the links nobody holds any more; the next sweep comes once
// the map has doubled. The caller holds l.mu.
func (l *linkCreds) sweep() {
	for id, p := range l.m {
		if p.Value() == nil {
			delete(l.m, id)
		}
	}
	l.pruneAt = 2 * len(l.m)
}

// gone marks linkID gone (session.ErrRevoked) for whatever still holds it,
// and forgets it: one nobody holds needs nothing, the server refusing a
// viewer through it from now on, and a viewer it lets join through it later
// (one the server stopped keeping track of, and named gone to be safe)
// starts afresh.
func (l *linkCreds) gone(linkID string) {
	l.mu.Lock()
	c := l.m[linkID].Value()
	delete(l.m, linkID)
	l.mu.Unlock()
	if c != nil {
		c.goneFor(session.ErrRevoked)
	}
}

// lost marks every link gone and forgets them all, as the connection to the
// server is lost: the host is not told of a link going until it is back,
// so what is still under way of a link's viewers (gone with the connection)
// is withdrawn rather than finished. A viewer joining through a link once
// the connection is back gets a linkCred of its own. Every principal's cred
// (principal) is marked gone too: the leave that would have said its
// principal was revoked may be what the connection did not carry.
func (l *linkCreds) lost() {
	l.mu.Lock()
	held, principals := l.m, l.principals
	l.m, l.principals = nil, nil
	l.pruneAt, l.principalsAt = 0, 0
	l.mu.Unlock()
	for _, p := range held {
		if c := p.Value(); c != nil {
			c.goneFor(session.ErrRevoked)
		}
	}
	for _, p := range principals {
		if c := p.Value(); c != nil {
			c.goneFor(session.ErrRevoked)
		}
	}
}
