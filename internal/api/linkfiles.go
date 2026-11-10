package api

import (
	"cmp"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A switchyard keeps the links it mints for hosts that register under an
// instance (proto.HostInfo.Instance) in links/ of its data directory, one
// document per link: the hash of its token (never the token), what the join
// page shows, and the session's id, which the same host gets again when it
// registers after a restart. So a restart loses no link; until the host is
// back a join says so (503 host_offline). A revoked link's file goes at once,
// an expired one at the next sweep, and a link whose host has been gone for
// linkOrphanAfter with it (a host connected is never gone, however long ago
// it registered). An open host's link also gives its place in its address's
// count to a new link of that address once its host has been gone for
// linkYieldAfter and the address has no other room: it is ended then. A
// link minted while its host presented a host token counts against no
// address but its host instance's: when that instance asks as an open host
// (a resume, a take-back, another of its sessions, after a restart too), it
// counts toward what the open host may keep (keepLink).

// linkFile is a link as a switchyard keeps it.
type linkFile struct {
	ID        string `json:"id"`
	TokenHash string `json:"tokenHash"`
	SessionID string `json:"sessionId,omitempty"`
	// RunID and Run are a host's run link's: its run, and its members as the host last told them.
	RunID     string       `json:"runId,omitempty"`
	Run       *share.Group `json:"run,omitempty"`
	Role      session.Role `json:"role"`
	Label     string       `json:"label,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	ExpiresAt *time.Time   `json:"expiresAt,omitempty"`
	Owner     string       `json:"owner"`
	Addr      string       `json:"addr,omitempty"`
}

const (
	// maxLinkFile bounds one file as it is read back.
	maxLinkFile = 16 << 10
	// maxDurableLinks bounds the links a switchyard keeps in all: past it, it mints none.
	maxDurableLinks = 20000
	// linkOrphanAfter is how long a kept link waits for its host to come back.
	linkOrphanAfter = 7 * 24 * time.Hour
	// linkYieldAfter is how long an open host's kept link holds its place in
	// its address's count once its host has gone, whatever else the address
	// needs: well past what a host's reconnect takes (its retries are 30 s
	// apart at most; after a restart the switchyard counts from its start),
	// so a host that only lost its connection finds its links as it left
	// them. Past it the link keeps its place until a new link of the
	// address has no other.
	linkYieldAfter = 5 * time.Minute
)

// keptLink is what the keeper holds of a kept link beside the store.
type keptLink struct {
	// hash is its token's hash (hex), for writing its file again.
	hash    string
	owner   string
	addr    string
	created time.Time
}

// linkKeeper holds what keeping links needs beside the store: the files, the
// links counted against each open-host address, and whether each host
// instance is connected or since when it is gone.
//
// Locks go in this order: fmu, then mu, then the link store's own.
type linkKeeper struct {
	files *store.Store
	// fmu orders the files' saves and deletes. A save looks, under it,
	// whether its link is still kept and still opens anything, and a link
	// stops being kept under it before its file goes: no save of a link's
	// file lands after its delete (a run update writing its run's links
	// again while one of them is revoked), so a revoked link is never back
	// after a restart.
	fmu sync.Mutex
	mu  sync.Mutex
	// kept are the kept links, by id. A link leaves them, and its
	// address's count, once, however often it is dropped (a revoke tells
	// its hook each time, an expiry and the sweep both).
	kept map[string]keptLink
	// addrs are the ids of the links counted against each open-host address.
	addrs map[string]map[string]struct{}
	// tokened are the ids of the links each host instance minted while it
	// presented a host token (a kept link with no address), which an open
	// host of the instance counts beside its address's.
	tokened map[string]map[string]struct{}
	// live counts each host instance's connections; seen is when an
	// instance was last connected (as it registered or left; for the owners
	// of the links restored at startup, the start).
	live map[string]int
	seen map[string]time.Time
	// undeleted are the links no longer kept whose file could not be
	// deleted (its directory not writable for a while), with the host
	// instance each was kept for: each sweep, and that instance's revoke of
	// the link again (even once the link is gone from the store), tries
	// again, so the file does not bring the link back at the next start;
	// until it goes, the host's revoke is answered that it is not done, and
	// a revoked link is still listed as its host's (liveLinkIDs). Under fmu.
	undeleted map[string]string
	// keeping is nil but in tests: called as keepLink begins, before it
	// takes the locks, with the address it counts against ("" for a host
	// with the host token).
	keeping atomic.Pointer[func(addr string)]
}

// linkFileUndeleted says whether the file of linkID, no longer kept, is
// still waiting to be deleted: a revoke is not reported done until it is.
func (s *Server) linkFileUndeleted(linkID string) bool {
	k := s.keeper
	if k == nil {
		return false
	}
	k.fmu.Lock()
	defer k.fmu.Unlock()
	_, ok := k.undeleted[linkID]
	return ok
}

// retryUndeleted tries again to delete the file of linkID when it waits to
// be deleted and was kept for owner; false when it does not wait for owner.
func (s *Server) retryUndeleted(linkID, owner string) bool {
	k := s.keeper
	if k == nil || owner == "" {
		return false
	}
	k.fmu.Lock()
	defer k.fmu.Unlock()
	if o, ok := k.undeleted[linkID]; !ok || o != owner {
		return false
	}
	s.deleteFile(linkID, owner)
	return true
}

// undeletedLinks are the links whose file waits to be deleted.
func (s *Server) undeletedLinks() []string {
	k := s.keeper
	if k == nil {
		return nil
	}
	k.fmu.Lock()
	defer k.fmu.Unlock()
	ids := make([]string, 0, len(k.undeleted))
	for id := range k.undeleted {
		ids = append(ids, id)
	}
	return ids
}

// deleteFile deletes the file of a link no longer kept for owner ("" when
// the keeper no longer knows it: the instance noted before stays), or
// notes it for the sweep (and the link's next revoke) to try again. Called
// with fmu held.
func (s *Server) deleteFile(id, owner string) {
	k := s.keeper
	if err := k.files.Delete(id + ".json"); err != nil {
		known, again := k.undeleted[id]
		if !again {
			s.log.Error("a link's file is not deleted yet; the sweep tries again", "link", id, "err", err)
		}
		if owner == "" {
			owner = known
		}
		k.undeleted[id] = owner
		return
	}
	delete(k.undeleted, id)
}

// away is how long the host instance owner has been gone at now: none while
// it holds a connection. Called with mu held.
func (k *linkKeeper) away(owner string, now time.Time) time.Duration {
	if k.live[owner] > 0 {
		return 0
	}
	return now.Sub(k.seen[owner])
}

// countedIn is where a kept link is counted: against its open host's
// address, or, minted with a host token, against its host instance.
func (k *linkKeeper) countedIn(kl keptLink) (map[string]map[string]struct{}, string) {
	if kl.addr != "" {
		return k.addrs, kl.addr
	}
	return k.tokened, kl.owner
}

// hold counts a kept link in. Called with mu held.
func (k *linkKeeper) hold(id string, kl keptLink) {
	k.kept[id] = kl
	m, key := k.countedIn(kl)
	if m[key] == nil {
		m[key] = map[string]struct{}{}
	}
	m[key][id] = struct{}{}
}

// release forgets a kept link: its hash and its place in its address's
// count; it returns the host instance it was kept for ("" when it was not
// kept). Releasing it again changes nothing. Called with mu held.
func (k *linkKeeper) release(id string) string {
	kl, ok := k.kept[id]
	if !ok {
		return ""
	}
	delete(k.kept, id)
	m, key := k.countedIn(kl)
	delete(m[key], id)
	if len(m[key]) == 0 {
		delete(m, key)
	}
	return kl.owner
}

// yielding is the n links counted against addr that give their places up
// first: of those whose host has been gone longer than linkYieldAfter, the
// oldest of the hosts gone longest; nil when fewer than n may, so that none
// is ended for a link that would still find no room. Called with mu held.
func (k *linkKeeper) yielding(addr string, now time.Time, n int) []string {
	type candidate struct {
		id   string
		away time.Duration
		made time.Time
	}
	var cs []candidate
	for id := range k.addrs[addr] {
		kl := k.kept[id]
		if away := k.away(kl.owner, now); away > linkYieldAfter {
			cs = append(cs, candidate{id, away, kl.created})
		}
	}
	if len(cs) < n {
		return nil
	}
	slices.SortFunc(cs, func(a, b candidate) int {
		if c := cmp.Compare(b.away, a.away); c != 0 {
			return c
		}
		if c := a.made.Compare(b.made); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	ids := make([]string, n)
	for i := range ids {
		ids[i] = cs[i].id
	}
	return ids
}

// openLinkKeeper opens links/ under st and puts the links kept there back
// into links: expired ones are deleted, an unreadable one logged and left
// out, never fatal.
func (s *Server) openLinkKeeper(st *store.Store) {
	files, err := st.Sub("links")
	if err != nil {
		s.log.Error("links are not kept across restarts: cannot open the links directory", "err", err)
		return
	}
	k := &linkKeeper{files: files, kept: map[string]keptLink{}, addrs: map[string]map[string]struct{}{}, tokened: map[string]map[string]struct{}{}, live: map[string]int{}, seen: map[string]time.Time{}, undeleted: map[string]string{}}
	s.keeper = k
	entries, err := files.List()
	if err != nil {
		s.log.Error("cannot list the kept links", "err", err)
		return
	}
	now := time.Now()
	restored := 0
	for _, e := range entries {
		var f linkFile
		if ok, err := files.LoadLimit(e.Name, &f, maxLinkFile); !ok || err != nil {
			s.log.Error("a kept link is left out", "file", e.Name, "err", err)
			continue
		}
		raw, err := hex.DecodeString(f.TokenHash)
		if err != nil || len(raw) != len(share.Hash{}) || f.ID == "" || (f.SessionID == "") == (f.RunID == "" || f.Run == nil) || f.Owner == "" || !f.Role.Valid() {
			s.log.Error("a kept link is left out", "file", e.Name)
			continue
		}
		if f.ExpiresAt != nil && !now.Before(*f.ExpiresAt) {
			_ = files.Delete(e.Name)
			continue
		}
		var h share.Hash
		copy(h[:], raw)
		s.links.Restore(share.Link{ID: f.ID, SessionID: f.SessionID, RunID: f.RunID, Group: f.Run, Role: f.Role, Label: f.Label, CreatedAt: f.CreatedAt, ExpiresAt: f.ExpiresAt, Owner: f.Owner, Addr: f.Addr}, h)
		k.hold(f.ID, keptLink{hash: f.TokenHash, owner: f.Owner, addr: f.Addr, created: f.CreatedAt})
		k.seen[f.Owner] = now
		restored++
	}
	if restored > 0 {
		s.log.Info("kept links restored", "links", restored)
	}
}

// Why keepLink refuses a link.
var (
	errLinksFull   = errors.New("this address holds as many links as this switchyard keeps; revoke one, or ask the operator for a host token")
	errHostFull    = errors.New("this address, with the links this host made with a host token, holds as many links as an open host may keep; revoke one, or connect with the host token")
	errLinkGone    = errors.New("the link was revoked or expired, or its session ended, as it was made")
	errLinkNotKept = errors.New("the switchyard could not keep the link; try again")
)

// keepLink makes a link minted for a hosted session of owner durable and
// writes its file, within the bounds. An open host's (addr set) counts the
// links of its address and those its instance minted with a host token
// (tokened): at or past the bound it mints none, and those keep working
// until they end as any link does. At an open host's address with no
// room left, links whose host has been gone longer than linkYieldAfter
// give their places up (yielding) and are ended, only when that leaves the
// new link room and once its file is written: a link that needed them and
// whose file cannot be written is refused, and they stay as they were. It
// refuses a link when the bounds leave no room, ending no other link, and
// keeps nothing of a link the store no longer opens: one revoked, expired
// or dropped since it was made is neither counted nor written, nor has any
// other ended for it. A link refused is dropped from the store.
func (s *Server) keepLink(link *share.Link, token, owner, addr string) error {
	k := s.keeper
	if k == nil || owner == "" {
		return nil
	}
	if f := k.keeping.Load(); f != nil {
		(*f)(addr)
	}
	h := share.HashToken(token)
	k.fmu.Lock()
	k.mu.Lock()
	var (
		err     error
		yielded []string
	)
	if _, lerr := s.links.Live(link.ID); lerr != nil {
		// Revoked, expired or dropped since it was made: no place is given
		// up for it.
		err = errLinkGone
	} else if s.links.Count() > maxDurableLinks {
		err = errLinksFull
	} else if over := len(k.addrs[addr]) + len(k.tokened[owner]) - s.cfg.Switchyard.OpenHostLinks + 1; addr != "" && over > 0 {
		// Only the address's links of hosts gone a while give places up:
		// the instance's own, made with a token, are its host's, connected.
		if yielded = k.yielding(addr, time.Now(), over); yielded == nil {
			err = errLinksFull
			if len(k.tokened[owner]) > 0 {
				err = errHostFull
			}
		}
	}
	// Under mu, so a drop of the link (its file and its count, which
	// follows the store's) comes after this whole or finds nothing.
	if err == nil && !s.links.SetOwner(link.ID, owner, addr) {
		err = errLinkGone
	}
	if err == nil {
		k.hold(link.ID, keptLink{hash: hex.EncodeToString(h[:]), owner: owner, addr: addr, created: link.CreatedAt})
	} else {
		// A link refused is forgotten: its host never hears its token, and
		// it takes no place among its session's links.
		s.links.Drop(link.ID)
	}
	k.mu.Unlock()
	if err != nil {
		k.fmu.Unlock()
		return err
	}
	link.Owner, link.Addr = owner, addr
	if serr := s.saveLinkFile(link.ID); serr != nil && len(yielded) > 0 {
		// No link gives its place up for one that is not kept: neither one
		// whose file cannot be written nor one revoked, expired or dropped
		// since it was counted. It is refused, and forgotten.
		k.mu.Lock()
		k.release(link.ID)
		s.links.Drop(link.ID)
		k.mu.Unlock()
		k.fmu.Unlock()
		if errors.Is(serr, errNotSaved) {
			return errLinkGone
		}
		return errLinkNotKept
	}
	// The links that give their places up end now, leaving the keeper and
	// the store together. fmu is held from their choice to here, so none
	// of them was dropped meanwhile and none of their hosts is back
	// (hostUp takes fmu): the host is told they are gone.
	var (
		ended []*share.Link
		gone  = map[string]string{}
	)
	k.mu.Lock()
	for _, id := range yielded {
		gone[id] = k.release(id)
		if l, ok := s.links.Drop(id); ok {
			ended = append(ended, l)
		}
	}
	k.mu.Unlock()
	for id, o := range gone {
		s.deleteFile(id, o)
		s.log.Info("a kept link gave its place to a new one, its host gone", "link", id)
	}
	k.fmu.Unlock()
	for _, l := range ended {
		s.closeLinkViewers(l)
	}
	return nil
}

// writeLinkFile writes a kept link's file as the store holds it now.
func (s *Server) writeLinkFile(linkID string) {
	k := s.keeper
	k.fmu.Lock()
	defer k.fmu.Unlock()
	_ = s.saveLinkFile(linkID)
}

// errNotSaved is saveLinkFile's for a link it writes nothing of.
var errNotSaved = errors.New("the link is revoked, expired, dropped or no longer kept")

// saveLinkFile is writeLinkFile with fmu held: the link as the store holds
// it now, while it is kept and opens anything. It writes nothing of one
// revoked, expired, dropped or no longer kept (errNotSaved); another error
// is the save's (logged).
func (s *Server) saveLinkFile(linkID string) error {
	k := s.keeper
	k.mu.Lock()
	hash := k.kept[linkID].hash
	k.mu.Unlock()
	if hash == "" {
		return errNotSaved
	}
	link, ok := s.links.Get(linkID)
	if !ok || link.Revoked || link.Owner == "" || (link.ExpiresAt != nil && !time.Now().Before(*link.ExpiresAt)) {
		return errNotSaved
	}
	f := linkFile{ID: link.ID, TokenHash: hash, SessionID: link.SessionID, Role: link.Role, Label: link.Label, CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt, Owner: link.Owner, Addr: link.Addr}
	if link.Group != nil {
		f.RunID, f.Run = link.RunID, link.Group
	}
	err := k.files.Save(link.ID+".json", f)
	if err != nil {
		s.log.Error("a link is not kept across restarts", "link", link.ID, "err", err)
	}
	return err
}

// keepGroup writes again the files of owner's run links, whose members changed.
func (s *Server) keepGroup(owner, runID string) {
	if s.keeper == nil {
		return
	}
	for _, l := range s.links.Durable() {
		if l.Owner == owner && l.RunID == runID && l.Group != nil && !l.Revoked {
			s.writeLinkFile(l.ID)
		}
	}
}

// dropLinkFile forgets a kept link's file and its place in its address's
// count. Calling it again for the same link changes nothing. A save of the
// file that has begun ends before the delete; one after it writes nothing.
func (s *Server) dropLinkFile(linkID string) {
	k := s.keeper
	if k == nil {
		return
	}
	k.fmu.Lock()
	defer k.fmu.Unlock()
	k.mu.Lock()
	owner := k.release(linkID)
	k.mu.Unlock()
	s.deleteFile(linkID, owner)
}

// hostUp notes that a host instance holds a connection, until the function
// it returns is called as the connection ends: while an instance holds one,
// its kept links are neither swept nor give their places up, and its time
// away starts when its last one ends. Called before the host is told which
// of its links the switchyard holds, so a link ended for its place is
// either ended before (and not told) or kept. It takes fmu too: a link
// that is giving its place up (keepLink, from its choice until it ends)
// or being swept (endKept) is done before its host is back.
func (s *Server) hostUp(owner string) func() {
	k := s.keeper
	if k == nil || owner == "" {
		return func() {}
	}
	k.fmu.Lock()
	k.mu.Lock()
	k.live[owner]++
	k.seen[owner] = time.Now()
	k.mu.Unlock()
	k.fmu.Unlock()
	return func() {
		k.mu.Lock()
		if k.live[owner]--; k.live[owner] <= 0 {
			delete(k.live, owner)
		}
		k.seen[owner] = time.Now()
		k.mu.Unlock()
	}
}

// sweepLinks drops the kept links that expired, and those whose host has
// been gone for linkOrphanAfter, closing whoever is attached through them,
// deletes the files that could not be deleted before, and forgets the host
// instances neither connected nor holding a kept link.
func (s *Server) sweepLinks(now time.Time) {
	k := s.keeper
	if k == nil {
		return
	}
	k.fmu.Lock()
	for id, owner := range k.undeleted {
		s.deleteFile(id, owner)
	}
	k.fmu.Unlock()
	// due is whether l goes now. Called with mu held.
	due := func(l *share.Link) bool {
		return k.away(l.Owner, now) > linkOrphanAfter || (l.ExpiresAt != nil && !now.Before(*l.ExpiresAt))
	}
	holding := map[string]bool{}
	for _, l := range s.links.Durable() {
		k.mu.Lock()
		d := due(l)
		k.mu.Unlock()
		if !d || !s.endKept(l.ID, due) {
			holding[l.Owner] = true
		}
	}
	k.mu.Lock()
	// And the links kept since the list above.
	for _, kl := range k.kept {
		holding[kl.owner] = true
	}
	for owner := range k.seen {
		if k.live[owner] == 0 && !holding[owner] {
			delete(k.seen, owner)
		}
	}
	k.mu.Unlock()
}

// endKept ends the kept link id if due still says so, looked at again under
// the keeper's locks (its host may be back meanwhile): it leaves the keeper
// and the store together, its file goes, and whoever is still attached
// through it is closed, as on a revoke, on the sessions it named as it
// went (a run update may have changed them since it was chosen). False
// when it stays.
func (s *Server) endKept(id string, due func(*share.Link) bool) bool {
	k := s.keeper
	k.fmu.Lock()
	k.mu.Lock()
	l, ok := s.links.Get(id)
	if !ok || !due(l) {
		k.mu.Unlock()
		k.fmu.Unlock()
		return !ok
	}
	owner := k.release(id)
	gone, ok := s.links.Drop(id)
	k.mu.Unlock()
	s.deleteFile(id, owner)
	k.fmu.Unlock()
	if ok {
		s.closeLinkViewers(gone)
	}
	return true
}
