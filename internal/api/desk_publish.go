package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// Decision links through a switchyard, on the publisher's side
// (docs/plans/decision-inbox.md, PR-6): a decision link minted at the
// switchyard is an ordinary control link there; this server keeps that it
// is a decision link (remoteLink.Decide) and holds it in its own link
// store as a remote link (share.Link.Remote: no token, so it opens
// nothing here), under the switchyard's id, so the lane's rules hold it
// as they hold a link made here: its acts held until they have taken
// effect and nothing begun after its end, one operation per opId, its
// Recent. Its viewers come through the switchyard, and the publisher's
// connections (a member's publication, or the run's desk) ask the lane
// whether their link is one (Lane.DecisionLink).
//
// A run's desk is the publisher's host connection that carries the run's
// decisions with no member alive (hostagent.Desk): opened with the run's
// first decision link there, it lives while the engine still takes the
// run's commands (a run finished failed included: a Retry reopens it) and
// one of the run's decision links stands, and ends at the first of the
// run stopped, continued or forgotten (after deskLinger), its last
// decision link revoked or expired, or the server's stop.

// Desk is a run's desk at the rendezvous (hostagent.Desk).
type Desk interface {
	// CurrentID is the rendezvous's id for it, which the run's group names.
	CurrentID() string
	// Decisions says the rendezvous carries decisions (its registered).
	Decisions() bool
	RunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error)
	RunLinkUpdate(ctx context.Context, run proto.RunGroup) error
	Revoke(ctx context.Context, linkID string) error
	Done() <-chan struct{}
	Stop()
}

// DeskOpener is a Publisher that opens runs' desks at its rendezvous.
type DeskOpener interface {
	OpenDesk(ctx context.Context, run proto.DeskRun) (Desk, error)
}

// ErrNoDesks says the rendezvous keeps no desks: an older switchyard,
// which closes a register_desk.
var ErrNoDesks = errors.New("the switchyard keeps no desks")

// runCarrier carries a request about a run's links to the rendezvous: a
// member's publication, or the run's desk.
type runCarrier interface {
	RunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error)
	RunLinkUpdate(ctx context.Context, run proto.RunGroup) error
	Revoke(ctx context.Context, linkID string) error
}

// deskLinger is how long a run's desk outlasts the engine's last command
// of the run (its stop, continuation or forgetting), as a publication
// outlasts its session's end.
const deskLinger = 5 * time.Second

// runDesk is a run's desk as this server holds it.
type runDesk struct {
	desk Desk
	// minting counts the decision links being minted over it
	// (openRunDesk to deskMinted): it is not ended for want of one meanwhile.
	minting int
	// stopLinger disarms the linger armed (nil while none is).
	stopLinger func()
}

// carriesDecisions says whether a publication's rendezvous carries
// decisions; one that cannot say carries none.
func carriesDecisions(pub any) bool {
	d, ok := pub.(interface{ Decisions() bool })
	return ok && d.Decisions()
}

// liveDesk is the run's desk while it is open, nil otherwise.
func (s *Server) liveDesk(runID string) Desk {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	rd := s.desks[runID]
	if rd == nil || ended(rd.desk.Done()) {
		return nil
	}
	return rd.desk
}

// openRunDesk is the run's desk, opened when it has none, for a decision
// link to be minted over it (deskMinted ends that, whatever came of it):
// a refusal's status, code and words when the rendezvous keeps none or
// carries no decisions (switchyard_too_old) or could not be reached.
func (s *Server) openRunDesk(ctx context.Context, runID string) (Desk, int, string, string) {
	s.pubMu.Lock()
	if rd := s.desks[runID]; rd != nil && !ended(rd.desk.Done()) {
		if !rd.desk.Decisions() {
			s.pubMu.Unlock()
			return nil, http.StatusConflict, "switchyard_too_old", tooOldWords
		}
		rd.minting++
		s.pubMu.Unlock()
		return rd.desk, 0, "", ""
	}
	s.pubMu.Unlock()
	s.pubMu.Lock()
	opener, ok := s.publisher.(DeskOpener)
	s.pubMu.Unlock()
	run, kept := s.runs.Get(runID)
	switch {
	case !kept:
		return nil, http.StatusNotFound, "not_found", "no such run"
	case !ok:
		return nil, http.StatusConflict, "switchyard_too_old", tooOldWords
	}
	name := cmpLabel(run.Label, run.Name)
	if len(name) > proto.MaxRunName {
		name = name[:proto.MaxRunName]
	}
	d, err := opener.OpenDesk(ctx, proto.DeskRun{ID: runID, Name: name})
	switch {
	case errors.Is(err, ErrNoDesks):
		return nil, http.StatusConflict, "switchyard_too_old", tooOldWords
	case err != nil:
		return nil, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not open the run's desk: " + err.Error()
	case !d.Decisions():
		d.Stop()
		return nil, http.StatusConflict, "switchyard_too_old", tooOldWords
	}
	s.pubMu.Lock()
	if rd := s.desks[runID]; rd != nil && !ended(rd.desk.Done()) {
		// Another request opened it first: that one is the run's.
		rd.minting++
		s.pubMu.Unlock()
		go d.Stop()
		return rd.desk, 0, "", ""
	}
	if s.desks == nil {
		s.desks = map[string]*runDesk{}
	}
	s.desks[runID] = &runDesk{desk: d, minting: 1}
	s.pubMu.Unlock()
	go func() {
		// Ended there (the switchyard's stop), it is forgotten here.
		<-d.Done()
		s.pubMu.Lock()
		if rd := s.desks[runID]; rd != nil && rd.desk == d {
			if rd.stopLinger != nil {
				rd.stopLinger()
			}
			delete(s.desks, runID)
		}
		s.pubMu.Unlock()
	}()
	return d, 0, "", ""
}

// holdDesk keeps the run's desk, if it has one, from ending until the
// release it returns (a revoke it may carry); the release judges it again.
func (s *Server) holdDesk(runID string) func() {
	s.pubMu.Lock()
	rd := s.desks[runID]
	if rd != nil {
		rd.minting++
	}
	s.pubMu.Unlock()
	if rd == nil {
		return func() {}
	}
	return func() { s.deskMinted(runID) }
}

// deskMinted ends a minting openRunDesk began, and judges the desk again:
// one whose link was not made ends once no other is.
func (s *Server) deskMinted(runID string) {
	s.pubMu.Lock()
	if rd := s.desks[runID]; rd != nil && rd.minting > 0 {
		rd.minting--
	}
	s.pubMu.Unlock()
	s.deskCheck(runID)
}

// tooOldWords say why a decision link is not minted at a switchyard.
const tooOldWords = "This switchyard must be updated to answer decisions through its links"

// endRunDesk stops the run's desk, if it has one, whatever holds it (the
// run forgotten: no decision of it is answered).
func (s *Server) endRunDesk(runID string) {
	s.pubMu.Lock()
	rd := s.desks[runID]
	if rd != nil {
		s.dropDeskLocked(runID, rd)
	}
	s.pubMu.Unlock()
	if rd != nil {
		rd.desk.Stop()
	}
}

// dropDeskLocked forgets rd as the run's desk, its linger disarmed; the
// caller holds pubMu, and stops the desk once it has let it go.
func (s *Server) dropDeskLocked(runID string, rd *runDesk) {
	if s.desks[runID] == rd {
		delete(s.desks, runID)
	}
	if rd.stopLinger != nil {
		rd.stopLinger()
		rd.stopLinger = nil
	}
}

// stopRunDesks stops every desk, as the server stops: together, so the
// whole takes one desk's last word at most.
func (s *Server) stopRunDesks() {
	s.pubMu.Lock()
	desks := s.desks
	s.desks = nil
	s.pubMu.Unlock()
	var wg sync.WaitGroup
	for _, rd := range desks {
		if rd.stopLinger != nil {
			rd.stopLinger()
		}
		wg.Go(rd.desk.Stop)
	}
	wg.Wait()
}

// runTakesCommands says whether the engine still takes the run's
// commands: kept, not stopped, not continued by another run. A run that
// finished (every task terminal) still does: a Retry reopens it.
func (s *Server) runTakesCommands(runID string) bool {
	run, ok := s.runs.Get(runID)
	return ok && run.StoppedAt == nil && run.ResumedBy == ""
}

// deskNeededLocked says whether the run's desk is still needed, minting
// aside: a decision link of the run minted at the rendezvous still stands
// here, or one revoked here is still owed its revoke there (revokePending:
// the desk may be what carries it). The caller holds pubMu; the link
// store's lock is taken under it, never the other way round (the store
// tells its hooks outside its lock).
func (s *Server) deskNeededLocked(runID string) bool {
	for _, rl := range s.remoteRunLinks[runID] {
		if rl.Decide && rl.revokePending {
			return true
		}
	}
	for _, l := range s.links.ListByRun(runID) {
		if l.Remote {
			if _, err := s.links.Live(l.ID); err == nil {
				return true
			}
		}
	}
	return false
}

// deskCheck ends the run's desk once nothing needs it: at once when no
// decision link of the run stands and no revoke of one is owed at the
// rendezvous, after deskLinger when the engine takes the run's commands no
// more (disarmed should it take them again). Its judgement and the end are
// made under one hold of pubMu, the lock a mint takes to count itself on
// the desk (openRunDesk): a mint counted before is seen, and one after
// finds no desk and opens another, so no mint is handed a desk that is
// then stopped. The linger's fire judges the same way.
func (s *Server) deskCheck(runID string) {
	if s.liveDesk(runID) == nil {
		return
	}
	// The engine is asked off pubMu (its hooks take pubMu).
	takes := s.runTakesCommands(runID)
	s.pubMu.Lock()
	seam := s.deskCheckSeam
	s.pubMu.Unlock()
	if seam != nil {
		seam(runID)
	}
	s.pubMu.Lock()
	rd := s.desks[runID]
	if rd == nil || ended(rd.desk.Done()) {
		s.pubMu.Unlock()
		return
	}
	if rd.minting == 0 && !s.deskNeededLocked(runID) {
		s.dropDeskLocked(runID, rd)
		s.pubMu.Unlock()
		rd.desk.Stop()
		return
	}
	defer s.pubMu.Unlock()
	switch {
	case takes && rd.stopLinger != nil:
		rd.stopLinger()
		rd.stopLinger = nil
	case !takes && rd.stopLinger == nil:
		fire := func() {
			if s.runTakesCommands(runID) {
				return
			}
			s.pubMu.Lock()
			if s.desks[runID] != rd {
				s.pubMu.Unlock()
				return
			}
			if rd.minting > 0 || s.revokeOwedLocked(runID) {
				// A mint under way, or a revoke still owed there that the
				// desk may carry: judged again as that ends (deskMinted,
				// revokeConfirmed), which arms the linger anew.
				rd.stopLinger = nil
				s.pubMu.Unlock()
				return
			}
			s.dropDeskLocked(runID, rd)
			s.pubMu.Unlock()
			rd.desk.Stop()
		}
		t := time.AfterFunc(deskLinger, func() { s.track(fire) })
		rd.stopLinger = func() { t.Stop() }
		if s.deskLingerSeam != nil {
			// The test fires it when it chooses; the timer is put off.
			t.Stop()
			s.deskLingerSeam(runID, func() { s.track(fire) })
		}
	}
}

// revokeOwedLocked says whether a revoke of one of the run's decision
// links minted at the rendezvous is still owed there. The caller holds
// pubMu.
func (s *Server) revokeOwedLocked(runID string) bool {
	for _, rl := range s.remoteRunLinks[runID] {
		if rl.Decide && rl.revokePending {
			return true
		}
	}
	return false
}

// remoteRevoked is a remote decision link's end here (revoked, expired),
// told by the link store's hooks. Expired, its record goes, as the link
// expires there by the same deadline, and the run's desk is judged again.
// Revoked here, it is not yet revoked there: its record stays, listed and
// marked revokePending, and the run's desk with it, until the rendezvous
// confirms the revoke (the revoke's handler, or its retries:
// retryRevokeThere), so a revoke that fails there never leaves a link
// standing at the switchyard that nothing here lists or can revoke again.
func (s *Server) remoteRevoked(l *share.Link) {
	if !l.Remote {
		return
	}
	if l.Revoked {
		s.markRevokeOwed(l.SessionID, l.RunID, l.ID)
		return
	}
	if l.RunID != "" {
		s.forgetRemoteRunLinks(l.RunID, l.ID)
		s.track(func() { s.deskCheck(l.RunID) })
		return
	}
	s.forgetRemoteLink(l.SessionID, l.ID)
}

// markRevokeOwed marks the record of a decision link minted at the
// rendezvous, a session's or a run's, as revoked here and owed its revoke
// there.
func (s *Server) markRevokeOwed(sessionID, runID, linkID string) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	m := s.remoteLinks[sessionID]
	if runID != "" {
		m = s.remoteRunLinks[runID]
	}
	if rl, ok := m[linkID]; ok {
		rl.revokePending = true
		m[linkID] = rl
	}
}

// revokeOwed says whether the record of the decision link is still kept
// and owed its revoke at the rendezvous.
func (s *Server) revokeOwed(sessionID, runID, linkID string) (remoteLink, bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	m := s.remoteLinks[sessionID]
	if runID != "" {
		m = s.remoteRunLinks[runID]
	}
	rl, ok := m[linkID]
	return rl, ok && rl.revokePending
}

// errNoCarrier says nothing of this server's can carry a request about a
// run's link to the rendezvous now.
var errNoCarrier = errors.New("no member of the run is published to the rendezvous now")

// carryRunRevoke revokes a run's link at the rendezvous over a member's
// publication still up, else the run's desk: nil once it is revoked there
// or the rendezvous no longer holds it (not_found).
func (s *Server) carryRunRevoke(ctx context.Context, runID, linkID string) error {
	run, ok := s.runs.Get(runID)
	if !ok {
		return errNoCarrier
	}
	var carrier runCarrier
	if _, member := s.runGroup(run); member != nil {
		carrier = member
	} else if d := s.liveDesk(runID); d != nil {
		carrier = d
	}
	if carrier == nil {
		return errNoCarrier
	}
	if err := carrier.Revoke(ctx, linkID); err != nil && !strings.Contains(err.Error(), "not_found") {
		return err
	}
	return nil
}

// revokeRetry is how long after a revoke the rendezvous did not confirm it
// is carried again, twice as long after each failure in a row, up to
// revokeRetryMax.
const (
	revokeRetry    = 2 * time.Second
	revokeRetryMax = time.Minute
)

// retryRevokeThere carries the revoke of a decision link minted at the
// rendezvous (a session's: sessionID; a run's: runID), revoked here and
// not confirmed there, again and again until the rendezvous confirms it
// (or no longer holds it), the record is forgotten otherwise (a revoke
// again from the Share dialog that went through, the session gone, the
// run forgotten), or the server stops. A run's carrier is a member's
// publication, else the run's desk, which the owed revoke keeps open (and
// opens again should the switchyard have ended it). One retry per link at
// a time.
func (s *Server) retryRevokeThere(sessionID, runID, linkID string) {
	s.pubMu.Lock()
	if s.revokesStopped || s.revokeRetries[linkID] {
		s.pubMu.Unlock()
		return
	}
	if s.revokeRetries == nil {
		s.revokeRetries = map[string]bool{}
	}
	s.revokeRetries[linkID] = true
	if s.revokesCtx == nil {
		s.revokesCtx, s.revokesCancel = context.WithCancel(context.Background())
	}
	stop := s.revokesCtx
	seam := s.revokeRetrySeam
	s.pubMu.Unlock()
	s.track(func() {
		defer func() {
			s.pubMu.Lock()
			delete(s.revokeRetries, linkID)
			s.pubMu.Unlock()
		}()
		wait := revokeRetry
		kick := make(chan struct{}, 1)
		next := func() {
			select {
			case kick <- struct{}{}:
			default:
			}
		}
		for {
			var t *time.Timer
			if seam != nil {
				// The test says when the next try goes.
				seam(linkID, next)
			} else {
				t = time.AfterFunc(wait, next)
			}
			select {
			case <-stop.Done():
				if t != nil {
					t.Stop()
				}
				return
			case <-kick:
			}
			if _, owed := s.revokeOwed(sessionID, runID, linkID); !owed {
				return
			}
			ctx, cancel := context.WithTimeout(stop, runSyncWait)
			err := s.carryRevoke(ctx, sessionID, runID, linkID)
			cancel()
			if err == nil {
				s.revokeConfirmed(sessionID, runID, linkID)
				return
			}
			s.log.Warn("a decision link revoked here is not revoked at the rendezvous yet; trying again", "link", linkID, "err", err)
			wait = min(wait*2, revokeRetryMax)
		}
	})
}

// carryRevoke carries a decision link's revoke to the rendezvous once:
// nil when it is revoked there, or gone there (not_found, or a session's
// publication gone with its session).
func (s *Server) carryRevoke(ctx context.Context, sessionID, runID, linkID string) error {
	if runID != "" {
		err := s.carryRunRevoke(ctx, runID, linkID)
		if !errors.Is(err, errNoCarrier) {
			return err
		}
		// The desk ended there meanwhile, and no member is up: one is
		// opened again to carry it.
		d, _, _, words := s.openRunDesk(ctx, runID)
		if d == nil {
			return errors.New(words)
		}
		defer s.deskMinted(runID)
		if err := d.Revoke(ctx, linkID); err != nil && !strings.Contains(err.Error(), "not_found") {
			return err
		}
		return nil
	}
	pub := s.publishedOf(sessionID)
	if pub == nil {
		return nil
	}
	if ended(pub.Done()) {
		return errors.New("the session is not published to the rendezvous now")
	}
	if err := pub.Revoke(ctx, linkID); err != nil && !strings.Contains(err.Error(), "not_found") {
		return err
	}
	return nil
}

// revokeConfirmed ends the record of a decision link the rendezvous has
// revoked, noted in its run's or session's log, and judges the run's desk
// again.
func (s *Server) revokeConfirmed(sessionID, runID, linkID string) {
	rl, _ := s.revokeOwed(sessionID, runID, linkID)
	if runID != "" {
		s.forgetRemoteRunLinks(runID, linkID)
		s.runs.Note(runID, session.ActivityLink, "link revoked at the rendezvous: "+linkLabelOr(rl.Label))
		s.deskCheck(runID)
		return
	}
	s.forgetRemoteLink(sessionID, linkID)
	s.recordLink(sessionID, "link revoked at the rendezvous: "+linkLabelOr(rl.Label))
}

// stopRevokeRetries ends every retry of a revoke owed at the rendezvous, as
// the server stops: none is begun after.
func (s *Server) stopRevokeRetries() {
	s.pubMu.Lock()
	s.revokesStopped = true
	cancel := s.revokesCancel
	s.pubMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RemoteLinkGone is told a link the switchyard says no longer opens what
// one of the publisher's connections carries (hostagent.Uplink.LinkGone):
// the server's own id of a published session, or the run of a desk. A
// decision link of this server's there ends here too, through its barrier,
// and its record with it, the switchyard having let it go, where that word
// is the link's own end: a session link's, from its session's connection;
// a run link's, from the run's desk alone. A member's connection is told
// so when the member only left the run's group (the run's next attempt
// replacing it), which ends nothing of the link.
func (s *Server) RemoteLinkGone(linkID, sessionID, runID string) {
	l, ok := s.links.Get(linkID)
	if !ok || !l.Remote {
		return
	}
	if l.RunID != "" {
		if runID != l.RunID {
			return
		}
		s.links.RevokeRun(l.RunID, linkID)
		s.forgetRemoteRunLinks(l.RunID, linkID)
		s.track(func() { s.deskCheck(l.RunID) })
		return
	}
	if sessionID == "" || sessionID != l.SessionID {
		return
	}
	s.links.Revoke(l.SessionID, linkID)
	s.forgetRemoteLink(l.SessionID, linkID)
}

// DecisionLink says whether linkID is a decision link this server minted
// at its switchyard that opens a lane for the publisher's connection
// (hostagent.Decisions): a session's (sessionID, a link of the session or
// of its run) or a desk's (runID, a link of the run), as it stands.
func (l *Lane) DecisionLink(linkID, sessionID, runID string) bool {
	link, err := l.s.links.Live(linkID)
	if err != nil || !link.Remote || !link.Decide {
		return false
	}
	if runID != "" {
		return link.RunID == runID
	}
	if sessionID == "" {
		return false
	}
	if link.RunID == "" {
		return link.SessionID == sessionID
	}
	run, ok := l.s.runOf(sessionID)
	return ok && run == link.RunID
}

// holdRemoteDecision holds here the decision link the rendezvous minted
// (res), a session's or a run's, for its lane; false when it could not be
// held (too many links), and then it opens no lane.
func (s *Server) holdRemoteDecision(res proto.LinkCreated, sessionID, runID string) bool {
	var exp *time.Time
	if t, err := time.Parse(time.RFC3339, res.ExpiresAt); err == nil && res.ExpiresAt != "" {
		exp = &t
	}
	return s.links.AddRemote(res.LinkID, sessionID, runID, res.Label, time.Now().UTC(), exp) == nil
}
