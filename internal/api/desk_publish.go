package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
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

// endRunDesk stops the run's desk, if it has one.
func (s *Server) endRunDesk(runID string) {
	s.pubMu.Lock()
	rd := s.desks[runID]
	delete(s.desks, runID)
	if rd != nil && rd.stopLinger != nil {
		rd.stopLinger()
	}
	s.pubMu.Unlock()
	if rd != nil {
		rd.desk.Stop()
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

// liveRemoteDecision says whether a decision link of the run minted at the
// rendezvous still stands here.
func (s *Server) liveRemoteDecision(runID string) bool {
	for _, l := range s.links.ListByRun(runID) {
		if l.Remote {
			if _, err := s.links.Live(l.ID); err == nil {
				return true
			}
		}
	}
	return false
}

// deskCheck ends the run's desk once nothing needs it: at once when its
// last decision link no longer stands, after deskLinger when the engine
// takes the run's commands no more (disarmed should it take them again).
func (s *Server) deskCheck(runID string) {
	if s.liveDesk(runID) == nil {
		return
	}
	s.pubMu.Lock()
	minting := s.desks[runID] != nil && s.desks[runID].minting > 0
	s.pubMu.Unlock()
	if !minting && !s.liveRemoteDecision(runID) {
		s.endRunDesk(runID)
		return
	}
	takes := s.runTakesCommands(runID)
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	rd := s.desks[runID]
	switch {
	case rd == nil:
	case takes && rd.stopLinger != nil:
		rd.stopLinger()
		rd.stopLinger = nil
	case !takes && rd.stopLinger == nil:
		fire := func() {
			if !s.runTakesCommands(runID) {
				s.endRunDesk(runID)
			}
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

// remoteRevoked is a remote decision link's end here (revoked, expired):
// its record goes, and the run's desk is judged again.
func (s *Server) remoteRevoked(l *share.Link) {
	if !l.Remote {
		return
	}
	if l.RunID != "" {
		s.forgetRemoteRunLinks(l.RunID, l.ID)
		s.track(func() { s.deskCheck(l.RunID) })
		return
	}
	s.forgetRemoteLink(l.SessionID, l.ID)
}

// RemoteLinkGone is told a link the switchyard says no longer opens what
// it was minted for (hostagent.Uplink.LinkGone): a decision link of this
// server's there ends here too, through its barrier, as a revoke.
func (s *Server) RemoteLinkGone(linkID string) {
	l, ok := s.links.Get(linkID)
	if !ok || !l.Remote {
		return
	}
	if l.RunID != "" {
		s.links.RevokeRun(l.RunID, linkID)
		return
	}
	s.links.Revoke(l.SessionID, linkID)
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
