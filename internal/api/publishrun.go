package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A crew run's link through the switchyard: the switchyard runs no crews,
// so this server tells it the run's members (a proto.RunGroup, each member's
// session as published there) over any member's publication (the carrier)
// and keeps it current as members start, end or join (link_run_update), and
// keeps a record of each such link for the run's Share dialog, as it does a
// session's remote links.

// runSyncDelay coalesces a run's changes before the switchyard is told: the
// engine reports a change on every log line. runSyncWait is how long a sync
// waits for the switchyard's answer. runSyncRetry is how long after a sync
// that failed (no answer, or any refusal but not_found) the run's members
// are sent again (Server.runSyncRetry), twice as long after each failure
// in a row, runSyncRetryMax at most.
const (
	runSyncDelay    = 2 * time.Second
	runSyncWait     = 15 * time.Second
	runSyncRetry    = 5 * time.Second
	runSyncRetryMax = time.Minute
)

// nextRunRevision is the revision of a run's members as they are about to be
// sent (proto.RunGroup.Revision), higher than every one this server gave
// before, whatever the run: the switchyard refuses members of a revision
// not above those it holds, so members sent before others and delivered
// after them (a sync whose wait ran out, over another member's connection
// or one about to reconnect) change nothing. Called in the run's turn
// (lockRunSync), as its members are read, so a later turn's members always
// have the higher revision. It starts from the clock, in milliseconds, so
// it goes on rising past a restart of this server too as long as the clock
// does (today no run's links at a switchyard outlive the start that made
// them: it holds them under the publisher's instance, made anew at each
// start).
func (s *Server) nextRunRevision() int64 {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.runRevision = max(s.runRevision+1, time.Now().UnixMilli())
	return s.runRevision
}

// runGroup is the run as the switchyard should show it, and a publication to
// carry requests about it (nil when no member is published).
func (s *Server) runGroup(run crew.Run) (proto.RunGroup, PublishedSession) {
	g := proto.RunGroup{ID: run.ID, Name: cmpLabel(run.Label, run.Name)}
	var carrier PublishedSession
	for _, m := range run.Members {
		pm := proto.RunMember{Name: m.Name, AgentID: m.AgentID, Status: m.Status}
		if m.SessionID != "" {
			if pub := s.publishedOf(m.SessionID); pub != nil {
				pm.SessionID = pub.CurrentID()
				if carrier == nil {
					carrier = pub
				}
			}
		}
		g.Members = append(g.Members, pm)
	}
	if len(g.Members) > proto.MaxRunLinkMembers {
		g.Members = g.Members[:proto.MaxRunLinkMembers]
	}
	// Its desk, while it has one, is named with its members: a sync that
	// left it out would take it from the run's links there.
	if d := s.liveDesk(run.ID); d != nil {
		g.Desk = d.CurrentID()
	}
	return g, carrier
}

func cmpLabel(label, name string) string {
	if label != "" {
		return label
	}
	return name
}

// awaitRunPublished waits, as long as ctx allows, for the member
// publications still being made, and says whether a member is published
// then (a carrier, as runGroup gives it); why says what keeps a run from
// the switchyard when none is.
func (s *Server) awaitRunPublished(ctx context.Context, runID string) (PublishedSession, string) {
	run, ok := s.runs.Get(runID)
	if !ok {
		return nil, "no such run"
	}
	why := ""
	for _, m := range run.Members {
		if m.SessionID != "" {
			s.republish(m.SessionID)
		}
	}
	for _, m := range run.Members {
		if m.SessionID == "" {
			continue
		}
		if _, w := s.awaitPublication(ctx, m.SessionID); w != "" && why == "" {
			why = w
		}
	}
	run, _ = s.runs.Get(runID)
	_, carrier := s.runGroup(run)
	if carrier == nil && why == "" {
		why = "no member of the run is published yet"
	}
	return carrier, why
}

// recordRemoteRunLink keeps a link minted at the rendezvous for a run.
func (s *Server) recordRemoteRunLink(runID string, res proto.LinkCreated, decide bool) {
	rl := remoteLink{ID: res.LinkID, Role: session.Role(res.Role), Label: res.Label, CreatedAt: time.Now(), Decide: decide}
	if t, err := time.Parse(time.RFC3339, res.ExpiresAt); err == nil && res.ExpiresAt != "" {
		rl.ExpiresAt = &t
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.remoteRunLinks == nil {
		s.remoteRunLinks = map[string]map[string]remoteLink{}
	}
	if s.remoteRunLinks[runID] == nil {
		s.remoteRunLinks[runID] = map[string]remoteLink{}
	}
	s.remoteRunLinks[runID][res.LinkID] = rl
}

// remoteRunLinksOf lists the records of a run's links minted at the rendezvous, newest first.
func (s *Server) remoteRunLinksOf(runID string) []remoteLink {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	out := make([]remoteLink, 0, len(s.remoteRunLinks[runID]))
	for _, rl := range s.remoteRunLinks[runID] {
		out = append(out, rl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// forgetRemoteRunLinks drops one record of a run's, or all of them ("").
func (s *Server) forgetRemoteRunLinks(runID, linkID string) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if linkID == "" {
		delete(s.remoteRunLinks, runID)
		delete(s.runSent, runID)
		delete(s.runSyncFails, runID)
		return
	}
	delete(s.remoteRunLinks[runID], linkID)
}

// liveCarriers are the publications that can carry a request about run to
// the switchyard now: its members' still up, first, then every other of
// this server's still up (the run's links there are the publisher's, which
// any of its publications may revoke: a run the engine evicts has no member
// running, so their publications have ended or are about to, and one that
// ends as the requests go leaves the rest to the next carrier). A
// publication that ended (its session's end, and the while it lingers
// after, or its connection's) stays in the published map until its session
// leaves the server, and would answer nothing but the wait; it is left out.
// None when nothing of this server's is published.
func (s *Server) liveCarriers(run crew.Run) []PublishedSession {
	live := func(pub PublishedSession) bool {
		select {
		case <-pub.Done():
			return false
		default:
			return pub != nil
		}
	}
	var out []PublishedSession
	members := map[string]bool{}
	for _, m := range run.Members {
		if m.SessionID == "" {
			continue
		}
		members[m.SessionID] = true
		if pub := s.publishedOf(m.SessionID); pub != nil && live(pub) {
			out = append(out, pub)
		}
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	for id, pub := range s.published {
		if !members[id] && live(pub) {
			out = append(out, pub)
		}
	}
	return out
}

// revokeRemoteRunLinks revokes at the switchyard the links of a run the
// engine forgot that were minted there (their records already dropped,
// forgetRemoteRunLinks), and one minted as it was forgotten (links.go): each
// over the carriers in turn, within runSyncWait each, as a revoke of one of
// them from the Share dialog goes, until one revokes it. A carrier that
// holds no such link (not_found) is passed: the link is the publisher's
// there, and a publication of another publisher (SetPublisher changed
// while sessions were published) holds none of its links; a link every
// carrier answers so is gone. A link no carrier revoked otherwise is logged
// and left to its expiry there or to the switchyard's sweep.
func (s *Server) revokeRemoteRunLinks(runID string, carriers []runCarrier, links []remoteLink) {
	for _, rl := range links {
		var last error
		for _, carrier := range carriers {
			ctx, cancel := context.WithTimeout(context.Background(), runSyncWait)
			err := carrier.Revoke(ctx, rl.ID)
			cancel()
			if err == nil {
				last = nil
				break
			}
			if last == nil || !refusedAs(err, "not_found") {
				last = err
			}
		}
		if last != nil && !refusedAs(last, "not_found") {
			s.log.Warn("a forgotten run's link was not revoked at the rendezvous", "run", runID, "link", rl.ID, "err", last)
		}
	}
}

// journalDelay coalesces a run's changes before its record is written again
// (scheduleJournal): the engine reports a change on every log line.
const journalDelay = 2 * time.Second

// scheduleJournal writes a run's record again after journalDelay, once for
// the changes in between (Engine.Journal): a run's record follows it from
// its launch on, so that one cut short by the server's end has a record,
// marked interrupted as the records open next. Nothing without records.
func (s *Server) scheduleJournal(runID string) {
	if s.records == nil {
		return
	}
	s.journalMu.Lock()
	if s.journaling[runID] {
		s.journalMu.Unlock()
		return
	}
	if s.journaling == nil {
		s.journaling = map[string]bool{}
	}
	s.journaling[runID] = true
	s.journalMu.Unlock()
	time.AfterFunc(journalDelay, func() {
		s.journalMu.Lock()
		delete(s.journaling, runID)
		s.journalMu.Unlock()
		s.runs.Journal(runID)
	})
}

// scheduleRunSync tells the switchyard a run's members after runSyncDelay,
// once for the changes in between; nothing for a run without remote links.
func (s *Server) scheduleRunSync(runID string) { s.scheduleRunSyncAfter(runID, false) }

// scheduleRunSyncAfter is scheduleRunSync, or with retry the next try of a
// sync that failed: after the server's runSyncRetry, twice as long for each
// failure in a row before it, runSyncRetryMax at most. The two are
// scheduled apart, once each at a time, so a try waiting long never holds
// back the sync of a change.
func (s *Server) scheduleRunSyncAfter(runID string, retry bool) {
	s.pubMu.Lock()
	pending, wait := &s.runSyncing, runSyncDelay
	if retry {
		pending, wait = &s.runRetrying, s.runSyncRetry
		for range min(s.runSyncFails[runID]-1, 8) {
			wait *= 2
		}
		wait = min(wait, runSyncRetryMax)
	}
	if len(s.remoteRunLinks[runID]) == 0 || (*pending)[runID] {
		s.pubMu.Unlock()
		return
	}
	if *pending == nil {
		*pending = map[string]bool{}
	}
	(*pending)[runID] = true
	s.pubMu.Unlock()
	time.AfterFunc(wait, func() {
		s.pubMu.Lock()
		delete(*pending, runID)
		s.pubMu.Unlock()
		s.track(func() { s.syncRunLinks(runID) })
	})
}

// lockRunSync takes the run's turn at telling the switchyard its members
// and returns the unlock. The run's syncs and its links' minting take turns:
// each carrier is a member's own connection, so two of them in flight at once
// could be applied in either order, an older membership last.
func (s *Server) lockRunSync(runID string) func() {
	s.pubMu.Lock()
	if s.runSyncMu == nil {
		s.runSyncMu = map[string]*sync.Mutex{}
	}
	mu := s.runSyncMu[runID]
	if mu == nil {
		mu = &sync.Mutex{}
		s.runSyncMu[runID] = mu
	}
	s.pubMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// syncRunLinks sends the run's members to the switchyard when they changed
// since the last time; a switchyard that holds no link for the run any more
// (not_found) has the records dropped here too. Syncs of one run take turns
// (lockRunSync), each reading the members as they are when its turn comes
// and giving them the next revision (nextRunRevision).
//
// A sync that is not answered leaves what the switchyard holds unknown: it
// may hold these members, those before, or (the update still on its way)
// take these later, when its revision has it refuse them if newer ones came
// first. So the next sync sends the members whatever was sent before, and
// one that failed is made again (scheduleRunSyncAfter): the switchyard ends
// up with the members as they are, not as they were when an update went
// astray. Every failure but not_found is tried again: no answer, stale,
// rate_limited, and invalid_request too, which a member not registered
// there yet answers (after a switchyard restart, the members' connections
// come back one by one), as long as the run has links there.
func (s *Server) syncRunLinks(runID string) {
	defer s.lockRunSync(runID)()
	run, ok := s.runs.Get(runID)
	if !ok {
		return
	}
	g, member := s.runGroup(run)
	// A member's publication carries it, else the run's desk.
	var carrier runCarrier = member
	if carrier == nil {
		if d := s.liveDesk(runID); d != nil {
			carrier = d
		}
	}
	if carrier == nil {
		return
	}
	b, _ := json.Marshal(g)
	s.pubMu.Lock()
	same := s.runSent[runID] == string(b)
	s.pubMu.Unlock()
	if same {
		return
	}
	g.Revision = s.nextRunRevision()
	ctx, cancel := context.WithTimeout(context.Background(), runSyncWait)
	defer cancel()
	err := carrier.RunLinkUpdate(ctx, g)
	s.pubMu.Lock()
	if err == nil {
		if s.runSent == nil {
			s.runSent = map[string]string{}
		}
		s.runSent[runID] = string(b)
		delete(s.runSyncFails, runID)
	} else {
		delete(s.runSent, runID)
		if s.runSyncFails == nil {
			s.runSyncFails = map[string]int{}
		}
		s.runSyncFails[runID]++
	}
	s.pubMu.Unlock()
	switch {
	case err == nil:
	case refusedAs(err, "not_found"):
		s.forgetRemoteRunLinks(runID, "")
	default:
		s.scheduleRunSyncAfter(runID, true)
	}
}

// refusedAs says whether err is the switchyard's refusal of a request with
// code (the host's error is "<code>: <message>").
func refusedAs(err error, code string) bool {
	return err != nil && strings.HasPrefix(err.Error(), code+": ")
}

// hasRemoteRunLink says whether a run has a record of the link.
func (s *Server) hasRemoteRunLink(runID, linkID string) bool {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	_, ok := s.remoteRunLinks[runID][linkID]
	return ok
}
