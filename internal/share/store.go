package share

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// Link is a share link record. The token itself is never stored. A link is
// either a session's (SessionID) or a crew run's (RunID, SessionID empty): a
// run link grants its role on the session of every member of the run.
type Link struct {
	ID        string       `json:"id"`
	SessionID string       `json:"sessionId"`
	RunID     string       `json:"runId,omitempty"`
	Label     string       `json:"label,omitempty"`
	Role      session.Role `json:"role"`
	CreatedAt time.Time    `json:"createdAt"`
	ExpiresAt *time.Time   `json:"expiresAt,omitempty"`
	Revoked   bool         `json:"revoked"`
	// Decide marks a decision link (docs/plans/decision-inbox.md, PR-5): a
	// control link whose holder may also list, read and act on the
	// decisions in its scope over its connection's lane. Only CreateDecision
	// and CreateRunDecision set it, and only on a control link.
	Decide bool `json:"decide,omitempty"`
	// Owner, on a switchyard, is the hash of the host instance a durable
	// link was minted for (kept on disk, outliving the host's absences);
	// Addr the open host's address it counts against. Never in a reply.
	Owner string `json:"-"`
	Addr  string `json:"-"`
	// Group, on a switchyard, is the crew run a host's run link opens: its
	// members and their sessions there, kept current by the host, since a
	// switchyard runs no crews. RunID is then the host's run id.
	Group *Group `json:"-"`

	hash Hash
	// lapsed says ExpireDue has told the hooks this link expired.
	lapsed bool
	// recent is a decision link's Recent: what was acted on through it
	// (NoteRecent), in memory with the link and gone with it.
	recent []Recent
	// acts is held by each act taken through the link while it is written
	// (Hold) and taken whole by a revoke (Revoke, RevokeRun), which so
	// waits for the acts under way and refuses those after it.
	acts *sync.RWMutex
	// ops are the operations a decision link took, by their opId (ClaimOp),
	// in memory with the link and gone with it.
	ops map[string]*op
}

// Recent is one action taken through a decision link: the item's ref,
// the receipt its steps are read by, the action's own identity (its
// decide's opId) and when it was taken.
type Recent struct {
	Ref     string    `json:"ref"`
	Receipt string    `json:"receipt"`
	Attempt string    `json:"attempt"`
	At      time.Time `json:"at"`
}

// MaxRecent bounds a decision link's Recent: the newest kept.
const MaxRecent = 50

// Group is a crew run as a switchyard knows it: its name and its members.
//
// Revision is the host's revision of the members (proto.RunGroup.Revision),
// 0 from a host that numbers none. It is held in memory alone, never in a
// kept link's file: a group sent before the switchyard started cannot reach
// it after, for the connection it came on ended with the switchyard.
type Group struct {
	Name     string
	Members  []GroupMember
	Revision int64 `json:"-"`
}

// GroupMember is a member of a Group: its session there, "" while it has none.
type GroupMember struct {
	Name, SessionID, AgentID, Status string
}

// Names says whether the group names the session.
func (g *Group) Names(sessionID string) bool {
	if g == nil || sessionID == "" {
		return false
	}
	for _, m := range g.Members {
		if m.SessionID == sessionID {
			return true
		}
	}
	return false
}

func groupKey(owner, runID string) string { return owner + "/" + runID }

// Errors returned by the store.
var (
	ErrUnknownToken = errors.New("share: unknown token")
	ErrRevoked      = errors.New("share: link revoked")
	ErrExpired      = errors.New("share: link expired")
	ErrInvalidRole  = errors.New("share: invalid role")
	ErrTooManyLinks = errors.New("share: too many links for session")
	ErrUnknownLink  = errors.New("share: unknown link")
	// ErrStaleGroup refuses a run's group whose revision is not above the
	// highest the run's links hold.
	ErrStaleGroup = errors.New("share: the run's links hold a newer revision of its members")
)

// MaxLinksPerSession bounds link creation, for each session and for each run.
const MaxLinksPerSession = 100

// Store is an in-memory link index.
type Store struct {
	mu        sync.RWMutex
	byHash    map[Hash]*Link
	byID      map[string]*Link
	bySession map[string]map[string]*Link
	byRun     map[string]map[string]*Link
	now       func() time.Time

	// OnRevoke is invoked, outside the store's lock, after each revoke of a
	// session's link (a link revoked before included) and once the link is
	// found expired (ExpireDue), so live viewers can be closed. The link is
	// marked first: a viewer that attaches after the mark is refused (Live).
	OnRevoke func(sessionID, linkID string)
	// OnRevokeRun is OnRevoke for a run's link.
	OnRevokeRun func(runID, linkID string)
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{byHash: map[Hash]*Link{}, byID: map[string]*Link{}, bySession: map[string]map[string]*Link{},
		byRun: map[string]map[string]*Link{}, now: func() time.Time { return time.Now().UTC() }}
}

// Create issues a new link to a session and returns the record plus the
// plaintext token.
func (s *Store) Create(sessionID string, role session.Role, label string, ttl time.Duration) (*Link, string, error) {
	return s.create(&Link{SessionID: sessionID}, s.bySession, sessionID, role, label, ttl, nil)
}

// CreateRunLink issues a new link to a crew run and returns the record plus
// the plaintext token.
func (s *Store) CreateRunLink(runID string, role session.Role, label string, ttl time.Duration) (*Link, string, error) {
	return s.create(&Link{RunID: runID}, s.byRun, runID, role, label, ttl, nil)
}

// ErrDecideNeedsControl refuses a decision link of any role but control.
var ErrDecideNeedsControl = errors.New("share: a decision link is a control link")

// CreateDecision issues a decision link to a session (Link.Decide): a
// control link only (ErrDecideNeedsControl).
func (s *Store) CreateDecision(sessionID string, role session.Role, label string, ttl time.Duration) (*Link, string, error) {
	if role != session.RoleControl {
		return nil, "", ErrDecideNeedsControl
	}
	return s.create(&Link{SessionID: sessionID, Decide: true}, s.bySession, sessionID, role, label, ttl, nil)
}

// CreateRunDecision issues a decision link to a crew run, as CreateDecision.
func (s *Store) CreateRunDecision(runID string, role session.Role, label string, ttl time.Duration) (*Link, string, error) {
	if role != session.RoleControl {
		return nil, "", ErrDecideNeedsControl
	}
	return s.create(&Link{RunID: runID, Decide: true}, s.byRun, runID, role, label, ttl, nil)
}

// NoteRecent keeps r in the decision link linkID's Recent, the newest
// MaxRecent, one entry per ref and attempt (the newest kept); false when
// the store holds no such decision link.
func (s *Store) NoteRecent(linkID string, r Recent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[linkID]
	if !ok || !l.Decide {
		return false
	}
	kept := l.recent[:0:0]
	for _, o := range l.recent {
		if o.Ref != r.Ref || o.Attempt != r.Attempt {
			kept = append(kept, o)
		}
	}
	kept = append(kept, r)
	if len(kept) > MaxRecent {
		kept = kept[len(kept)-MaxRecent:]
	}
	l.recent = kept
	return true
}

// RecentOf is the decision link linkID's Recent, oldest first.
func (s *Store) RecentOf(linkID string) []Recent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if l, ok := s.byID[linkID]; ok {
		return append([]Recent(nil), l.recent...)
	}
	return nil
}

// MaxOps bounds the operations a decision link keeps by their opId for its
// life: past it the link takes no new one (ErrTooManyOps), so an opId is
// never forgotten while the link stands.
const MaxOps = 1024

// Errors of ClaimOp.
var (
	// ErrOpConflict refuses an opId the link took before for another
	// request: an opId names one operation.
	ErrOpConflict = errors.New("share: the opId was used before for another request")
	// ErrTooManyOps refuses a new opId past MaxOps.
	ErrTooManyOps = errors.New("share: the link has taken as many operations as it keeps")
)

// op is one operation of a decision link, under the store's lock.
type op struct {
	fingerprint, outcome string
	settled              bool
	done                 chan struct{}
}

// Op is an operation a decision link holds by its opId, as ClaimOp found
// it: Settled with its Outcome (what its taker kept of it), or under way,
// Done closed once it is settled or let go.
type Op struct {
	Outcome string
	Settled bool
	Done    <-chan struct{}
}

// ClaimOp claims the opId opID of the decision link linkID for a request
// whose fingerprint is fp (the whole request as it was sent). An opId names
// one operation, for as long as the link stands:
//
//   - none held: it is held for this request, under way, and mine is true:
//     the caller settles it (SettleOp) on every way out, kept once its
//     operation was begun, let go when nothing was;
//   - held for another fingerprint: ErrOpConflict, whether settled or not;
//   - held for this one: the Op as it stands, mine false: a settled one is
//     answered from its Outcome, one under way (a twin from another
//     connection) is waited for (Done) and claimed again.
//
// ErrUnknownLink for a link the store does not hold as a decision link,
// ErrTooManyOps for a new opId past MaxOps.
func (s *Store) ClaimOp(linkID, opID, fp string) (Op, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[linkID]
	if !ok || !l.Decide {
		return Op{}, false, ErrUnknownLink
	}
	if o, ok := l.ops[opID]; ok {
		if o.fingerprint != fp {
			return Op{}, false, ErrOpConflict
		}
		return Op{Outcome: o.outcome, Settled: o.settled, Done: o.done}, false, nil
	}
	if len(l.ops) >= MaxOps {
		return Op{}, false, ErrTooManyOps
	}
	if l.ops == nil {
		l.ops = map[string]*op{}
	}
	o := &op{fingerprint: fp, done: make(chan struct{})}
	l.ops[opID] = o
	return Op{Done: o.done}, true, nil
}

// SettleOp settles the opId opID the caller claimed (ClaimOp, mine): kept
// with outcome once its operation was begun, so the same request again is
// answered from it and another is a conflict; let go when nothing was
// begun, the opId free again. Its waiters are woken either way.
func (s *Store) SettleOp(linkID, opID string, keep bool, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[linkID]
	if !ok {
		return
	}
	o, ok := l.ops[opID]
	if !ok || o.settled {
		return
	}
	if keep {
		o.outcome, o.settled = outcome, true
	} else {
		delete(l.ops, opID)
	}
	close(o.done)
}

// create completes link, whose scope is set, and indexes it in scopes under
// key: at most MaxLinksPerSession links per key, and only when admit (when
// not nil, called with mu held) returns nil.
func (s *Store) create(link *Link, scopes map[string]map[string]*Link, key string, role session.Role, label string, ttl time.Duration, admit func() error) (*Link, string, error) {
	if !role.Valid() {
		return nil, "", ErrInvalidRole
	}
	if len(label) > 120 {
		label = label[:120]
	}
	tok, hash := NewToken()
	link.ID, link.Label, link.Role, link.CreatedAt, link.hash, link.acts = session.NewID(), label, role, s.now(), hash, new(sync.RWMutex)
	if ttl > 0 {
		exp := link.CreatedAt.Add(ttl)
		link.ExpiresAt = &exp
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(scopes[key]) >= MaxLinksPerSession {
		return nil, "", ErrTooManyLinks
	}
	if admit != nil {
		if err := admit(); err != nil {
			return nil, "", err
		}
	}
	s.byHash[hash] = link
	s.byID[link.ID] = link
	if scopes[key] == nil {
		scopes[key] = map[string]*Link{}
	}
	scopes[key][link.ID] = link
	return copyLink(link), tok, nil
}

// Resolve returns the live link for a presented token. It reads the link
// under the lock, as a revoke writes it.
func (s *Store) Resolve(tok string) (*Link, error) {
	h := HashToken(tok)
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, ok := s.byHash[h]
	if !ok {
		return nil, ErrUnknownToken
	}
	if link.Revoked {
		return nil, ErrRevoked
	}
	if link.ExpiresAt != nil && !s.now().Before(*link.ExpiresAt) {
		return nil, ErrExpired
	}
	return copyLink(link), nil
}

// Live returns the link with linkID while it still opens what it was made
// for: ErrUnknownLink once it is forgotten (its session or run gone, or
// dropped), ErrRevoked once revoked, ErrExpired once expired. A viewer's
// attach asks it under the session's lock (session.AttachOptions.Authorize).
func (s *Store) Live(linkID string) (*Link, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, ok := s.byID[linkID]
	if !ok {
		return nil, ErrUnknownLink
	}
	if link.Revoked {
		return nil, ErrRevoked
	}
	if link.ExpiresAt != nil && !s.now().Before(*link.ExpiresAt) {
		return nil, ErrExpired
	}
	return copyLink(link), nil
}

// Get returns a link by ID.
func (s *Store) Get(linkID string) (*Link, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.byID[linkID]
	if !ok {
		return nil, false
	}
	return copyLink(l), true
}

// Revoke marks a session's link unusable and notifies OnRevoke, every time
// (a viewer still attached through a link revoked before is closed too).
// found is false when the link does not belong to sessionID; revoked says
// whether this call revoked it (false when it was revoked before).
func (s *Store) Revoke(sessionID, linkID string) (found, revoked bool) {
	return s.revoke(linkID, func(l *Link) bool { return l.RunID == "" && l.SessionID == sessionID }, true, func() {
		if s.OnRevoke != nil {
			s.OnRevoke(sessionID, linkID)
		}
	})
}

// End marks a session's link unusable as its session ends, as Revoke does,
// but tells no hook: whoever ends the session closes the viewers attached
// through it as the session's end (session.Driver.EndLink), after the mark
// as a revoke does, where OnRevoke would close them as revoked. It reports
// whether this call marked it: a link of another session, or one revoked
// already, is not the end's to close, for a revoke closes its viewers as
// revoked (its OnRevoke may still be on its way).
func (s *Store) End(sessionID, linkID string) bool {
	_, ended := s.revoke(linkID, func(l *Link) bool { return l.RunID == "" && l.SessionID == sessionID }, false, func() {})
	return ended
}

// RevokeRun is Revoke for a run's link, notifying OnRevokeRun.
func (s *Store) RevokeRun(runID, linkID string) (found, revoked bool) {
	return s.revoke(linkID, func(l *Link) bool { return l.RunID != "" && l.RunID == runID }, true, func() {
		if s.OnRevokeRun != nil {
			s.OnRevokeRun(runID, linkID)
		}
	})
}

// revoke marks the link with linkID revoked when owned says it is the
// caller's, and then calls notify, outside the lock, each time: the mark
// comes first, so a viewer attaching meanwhile either sees it or is there
// for notify's close to find. With wait (a revoke, not a session's end,
// whose session refuses what comes after on its own) the mark is made
// holding the link's acts whole: after the acts under way (Hold) are
// written, and before any other is held, so none of the link's acts is
// written after the revoke returns.
func (s *Store) revoke(linkID string, owned func(*Link) bool, wait bool, notify func()) (found, revoked bool) {
	s.mu.Lock()
	link, ok := s.byID[linkID]
	if !ok || !owned(link) {
		s.mu.Unlock()
		return false, false
	}
	if wait && link.acts != nil {
		s.mu.Unlock()
		link.acts.Lock()
		s.mu.Lock()
	}
	already := link.Revoked
	link.Revoked = true
	s.mu.Unlock()
	if wait && link.acts != nil {
		link.acts.Unlock()
	}
	notify()
	return true, !already
}

// Hold is the link linkID as it stands, held while an act taken through it
// is written (a decision link's answer as the session's writer takes it, a
// task command as the engine commits it): the link is checked with the
// write, not before it. Until release (once; again does nothing) a Revoke
// or RevokeRun of the link waits, so an act held is written before the
// revoke returns and never after it. Hold itself never waits: while a
// revoke of the link is under way it is refused ErrRevoked, as after it;
// an unknown, revoked or expired link is refused as Live refuses it.
func (s *Store) Hold(linkID string) (*Link, func(), error) {
	s.mu.RLock()
	l, ok := s.byID[linkID]
	s.mu.RUnlock()
	if !ok || l.acts == nil {
		return nil, nil, ErrUnknownLink
	}
	if !l.acts.TryRLock() {
		return nil, nil, ErrRevoked
	}
	link, err := s.Live(linkID)
	if err != nil {
		l.acts.RUnlock()
		return nil, nil, err
	}
	return link, sync.OnceFunc(l.acts.RUnlock), nil
}

// ExpireDue tells the hooks of each link that has expired by now, once per
// link, as a revoke does (OnRevoke for a session's link, OnRevokeRun for a
// run's), so the viewers still attached through it are closed. A link
// revoked before is not told again. It returns how many it told.
func (s *Store) ExpireDue(now time.Time) int {
	type due struct {
		run         bool
		scope, link string
	}
	var todo []due
	s.mu.Lock()
	for _, l := range s.byID {
		if l.Revoked || l.lapsed || l.ExpiresAt == nil || now.Before(*l.ExpiresAt) {
			continue
		}
		l.lapsed = true
		if l.RunID != "" {
			todo = append(todo, due{run: true, scope: l.RunID, link: l.ID})
		} else {
			todo = append(todo, due{scope: l.SessionID, link: l.ID})
		}
	}
	s.mu.Unlock()
	for _, d := range todo {
		if d.run {
			if s.OnRevokeRun != nil {
				s.OnRevokeRun(d.scope, d.link)
			}
		} else if s.OnRevoke != nil {
			s.OnRevoke(d.scope, d.link)
		}
	}
	return len(todo)
}

// ListBySession returns links for a session, newest first.
func (s *Store) ListBySession(sessionID string) []*Link {
	return s.list(s.bySession, sessionID)
}

// ListByRun returns links for a run, newest first.
func (s *Store) ListByRun(runID string) []*Link {
	return s.list(s.byRun, runID)
}

func (s *Store) list(scopes map[string]map[string]*Link, key string) []*Link {
	s.mu.RLock()
	out := make([]*Link, 0, len(scopes[key]))
	for _, l := range scopes[key] {
		out = append(out, copyLink(l))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// DeleteSession forgets the links of a session and returns the IDs it
// dropped. A durable link (one with an Owner) stays unless durableToo: its
// session left only because its host went away, and comes back under the
// same id when the host does.
func (s *Store) DeleteSession(sessionID string, durableToo bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var dropped []string
	for id, l := range s.bySession[sessionID] {
		if l.Owner != "" && !durableToo {
			continue
		}
		delete(s.byHash, l.hash)
		delete(s.byID, id)
		delete(s.bySession[sessionID], id)
		dropped = append(dropped, id)
	}
	if len(s.bySession[sessionID]) == 0 {
		delete(s.bySession, sessionID)
	}
	return dropped
}

// Restore puts back a link kept on disk, under the hash of its token.
func (s *Store) Restore(l Link, hash Hash) {
	link := l
	link.hash, link.acts, link.recent, link.ops = hash, new(sync.RWMutex), nil, nil
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byHash[hash] = &link
	s.byID[link.ID] = &link
	scopes, key := s.bySession, link.SessionID
	if link.RunID != "" {
		scopes, key = s.byRun, link.RunID
		if link.Group != nil {
			key = groupKey(link.Owner, link.RunID)
		}
	}
	if scopes[key] == nil {
		scopes[key] = map[string]*Link{}
	}
	scopes[key][link.ID] = &link
}

// SetOwner makes a link durable: minted for the host instance owner, from
// the address addr ("" for a tokened host). False, and nothing changed,
// when the store no longer holds the link or it was revoked: it is not to
// be kept.
func (s *Store) SetOwner(linkID, owner, addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[linkID]
	if !ok || l.Revoked {
		return false
	}
	l.Owner, l.Addr = owner, addr
	return true
}

// Drop forgets one link outright (its file swept), whatever its state, and
// returns it as it stood when it went (its group's sessions then, which
// are those its viewers can be on), or false when there was none.
func (s *Store) Drop(linkID string) (*Link, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[linkID]
	if !ok {
		return nil, false
	}
	delete(s.byHash, l.hash)
	delete(s.byID, linkID)
	for _, scopes := range []map[string]map[string]*Link{s.bySession, s.byRun} {
		for key, m := range scopes {
			if _, ok := m[linkID]; ok {
				delete(m, linkID)
				if len(m) == 0 {
					delete(scopes, key)
				}
			}
		}
	}
	return copyLink(l), true
}

// Durable lists the durable links, for the sweep.
func (s *Store) Durable() []*Link {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Link
	for _, l := range s.byID {
		if l.Owner != "" {
			out = append(out, copyLink(l))
		}
	}
	return out
}

// Count is how many links the store holds.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// DeleteRun forgets every link of a run, as DeleteSession does a session's,
// and returns the IDs of those that were not revoked: viewers may still be
// attached through them.
func (s *Store) DeleteRun(runID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var live []string
	for id, l := range s.byRun[runID] {
		if !l.Revoked {
			live = append(live, id)
		}
		delete(s.byHash, l.hash)
		delete(s.byID, id)
	}
	delete(s.byRun, runID)
	return live
}

func copyLink(l *Link) *Link {
	cp := *l
	cp.recent, cp.acts, cp.ops = nil, nil, nil
	if l.Group != nil {
		g := l.Group.clone()
		cp.Group = &g
	}
	return &cp
}

// clone copies g, with members of its own.
func (g *Group) clone() Group {
	return Group{Name: g.Name, Members: append([]GroupMember(nil), g.Members...), Revision: g.Revision}
}

// staleFor says whether a group of revision rev comes too late for the
// links under key: it has a revision, and one of them, revoked or not,
// holds one as high. Called with mu held.
func (s *Store) staleFor(key string, rev int64) bool {
	if rev <= 0 {
		return false
	}
	for _, l := range s.byRun[key] {
		if l.Group != nil && l.Group.Revision >= rev {
			return true
		}
	}
	return false
}

// CreateGroupLink issues a link to a host's crew run, as a switchyard holds
// it: the members' sessions are named in g, which SetGroup keeps current.
// Indexed under the owner and the run, so two hosts' run ids never meet.
// ErrStaleGroup, and no link, when the run's links hold a revision of its
// members as high as g's or higher.
func (s *Store) CreateGroupLink(runID, owner string, g Group, role session.Role, label string, ttl time.Duration) (*Link, string, error) {
	gc := g.clone()
	key := groupKey(owner, runID)
	return s.create(&Link{RunID: runID, Owner: owner, Group: &gc}, s.byRun, key, role, label, ttl, func() error {
		if s.staleFor(key, gc.Revision) {
			return ErrStaleGroup
		}
		return nil
	})
}

// SetGroup gives the live links of owner's run the members in g. It returns
// the ids of the links it changed, and those of the sessions they named
// before and g no longer does: whoever is attached to one of those sessions
// through one of those links is to be closed. The links name g's sessions
// alone from the moment it returns, so a viewer attaching to a session that
// left is refused (Live, then Group.Names).
//
// A g with a revision changes nothing when the run's links hold one as high
// or higher, sent after it (ErrStaleGroup); one without (an older host's)
// is taken whatever they hold, and they keep the highest they held. With no
// live link, nothing changes and changed is empty, whatever g's revision.
func (s *Store) SetGroup(owner, runID string, g Group) (changed, left []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := groupKey(owner, runID)
	live := false
	for _, l := range s.byRun[key] {
		live = live || (!l.Revoked && l.Group != nil)
	}
	if !live {
		return nil, nil, nil
	}
	if s.staleFor(key, g.Revision) {
		return nil, nil, ErrStaleGroup
	}
	gone := map[string]bool{}
	for _, l := range s.byRun[key] {
		if l.Revoked || l.Group == nil {
			continue
		}
		for _, m := range l.Group.Members {
			if m.SessionID != "" && !g.Names(m.SessionID) && !gone[m.SessionID] {
				gone[m.SessionID] = true
				left = append(left, m.SessionID)
			}
		}
		next := g.clone()
		next.Revision = max(next.Revision, l.Group.Revision)
		l.Group = &next
		changed = append(changed, l.ID)
	}
	sort.Strings(changed)
	sort.Strings(left)
	return changed, left, nil
}

// RevokeGroup revokes owner's run link linkID, notifying OnRevokeRun.
func (s *Store) RevokeGroup(owner, linkID string) (found, revoked bool) {
	var runID string
	return s.revoke(linkID, func(l *Link) bool {
		runID = l.RunID
		return l.Group != nil && l.Owner == owner
	}, true, func() {
		if s.OnRevokeRun != nil {
			s.OnRevokeRun(runID, linkID)
		}
	})
}

// ListNaming lists the live group links that name a session.
func (s *Store) ListNaming(sessionID string) []*Link {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Link
	for _, m := range s.byRun {
		for _, l := range m {
			if !l.Revoked && l.Group.Names(sessionID) {
				out = append(out, copyLink(l))
			}
		}
	}
	return out
}
