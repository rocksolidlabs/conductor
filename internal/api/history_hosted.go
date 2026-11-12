package api

import (
	"context"
	"errors"
	"time"

	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// A hosted session's record (docs/plans/history.md, PR-12). It holds what
// its host reports of the session (signal.HostedSession.Report: where it
// runs, its agent's version, its repository and start commit; its end
// commit by session_info before the status that ends it) and borrows
// nothing of the server's machine: no probe, no git read, no Runtime. Its
// agent's own session is captured here, from the hook that reports it to
// this server (captureHostedAgentSession), by the catalog's recipe for the
// agent, from what the record holds whichever registration captured it,
// and kept in the record, never in the hosted session's Info, which is its
// host's.
//
// It follows its session by the same rule as a server session's
// (reconcile in history_journal.go): the hub's OnAdded begins it, the
// session's changes are hints, its activity and chat streams, and the
// registry's removal and the shutdown's final pass are final. An end is
// observed when the host says its session ended; a session the hub let go
// (its host away past the grace) or the server stopped observing (the
// final pass) while it ran is "host away", never lost and never ended. A
// hosted id comes back (signal.HostedID), so the same host's next
// registration takes its record up again (journal.BeginHosted).

// beginHostedRecord is the hub's OnAdded: the record of a hosted session a
// register made, begun or taken up again, its recorder in recCur and
// reconciled once. A recorder of the same id still held (the hub's earlier
// session of it, the registry no longer holding it) is reconciled first,
// final, so what it observed stands before its record is taken up. Nothing
// is begun once the records are closed (the admission rule).
func (s *Server) beginHostedRecord(hs *signal.HostedSession) {
	if !s.recording() {
		return
	}
	info := hs.Info()
	id := info.ID
	s.recMu.Lock()
	if s.recordsClosed {
		s.recMu.Unlock()
		return
	}
	if old := s.recCur[id]; old != nil && old.h != nil && old.h != hs {
		s.reconcileLocked(old, false)
	}
	s.recMu.Unlock()
	rep := hs.Report()
	r := journal.Record{
		ID:         id,
		Name:       info.Name,
		Kind:       string(info.Kind),
		Agent:      journal.Agent{ID: info.AgentID, Version: rep.AgentVersion},
		Host:       journal.Host{Name: info.HostName, User: info.HostUser, Owner: hs.Owner(), Hosted: true},
		Where:      rep.Where,
		Cwd:        info.Cwd,
		Repo:       rep.Repo,
		RepoCommon: rep.RepoCommon,
		Branch:     info.Branch,
		Start:      rep.StartCommit,
		StartedAt:  info.CreatedAt,
	}
	if r.Start != "" {
		r.StartFrom = journal.StartFromHost
	}
	// Outside recMu: BeginHosted writes the record.
	in, err := s.sessionRecords.BeginHosted(r)
	if err != nil {
		if !errors.Is(err, journal.ErrNotOwner) && !errors.Is(err, journal.ErrClosed) {
			s.log.Warn("the hosted session's record was not begun", "session", id, "err", err)
		}
		return
	}
	s.recordSeam("hostedBegun", id)
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.recordsClosed || s.recCur[id] != nil {
		// Left open on disk, for the next start to say its host away.
		return
	}
	rc := &recorder{in: in, cwd: info.Cwd, h: hs, attached: true, gone: make(chan struct{})}
	s.recCur[id] = rc
	s.reconcileLocked(rc, false)
}

// reconcileHostedLocked is reconcileLocked for a hosted session's recorder
// (the snapshot rule): membership first, then the session's Info. Final
// when membership was read absent or in the final pass. An end is the one
// the host reported (its status ended), with the end commit it sent;
// final with none is host away. The caller holds recMu.
func (s *Server) reconcileHostedLocked(rc *recorder, finalPass bool) {
	id := rc.in.ID()
	if s.recCur[id] != rc || rc.leaving || (!finalPass && s.recordsClosed) {
		return
	}
	cur, ok := s.registry.Get(id)
	member := ok && cur == session.Driver(rc.h)
	info := rc.h.Info()
	final := finalPass || !member
	st := journal.State{Name: info.Name, AgentSession: agentSessionOf(rc.as), Final: final}
	switch {
	case info.Status.Ended():
		end := journal.Ending{How: endingOf(info), Commit: rc.endCommit}
		if info.EndedAt != nil {
			end.At = *info.EndedAt
		}
		st.End = &end
	case final:
		st.End = &journal.Ending{How: journal.EndingHostAway, Lost: true}
	}
	s.sessionRecords.Settle(rc.in, st)
	if final {
		delete(s.recCur, id)
	}
}

// hostedRecorder is the recorder of the hosted session id, nil for none.
func (s *Server) hostedRecorder(id string) *recorder {
	if s.sessionRecords == nil {
		return nil
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if rc := s.recCur[id]; rc != nil && rc.h != nil {
		return rc
	}
	return nil
}

// recordHostedHint is a hosted session's change, a hint (the hub's
// OnChange): its recorder reconciled.
func (s *Server) recordHostedHint(id string) {
	if rc := s.hostedRecorder(id); rc != nil {
		s.deliver(id, nil, func() { s.reconcile(rc) })
	}
}

// recordHostedActivity and recordHostedChat are a hosted session's
// streams (the hub's OnActivity and OnChat), as its host reported them.
func (s *Server) recordHostedActivity(id string, e session.ActivityEntry) {
	if rc := s.hostedRecorder(id); rc != nil {
		s.deliver(id, e, func() { s.recordActivity(rc, e) })
	}
}

func (s *Server) recordHostedChat(id string, m session.ChatMessage) {
	if rc := s.hostedRecorder(id); rc != nil {
		s.deliver(id, m, func() { s.recordChat(rc, m) })
	}
}

// recordSessionInfo is the hub's OnSessionInfo: the end commit the host
// read once its process ended, kept by the session's recorder for the end
// its status brings next (the host sends it first); one that comes after
// the end is recorded changes nothing, the end standing.
func (s *Server) recordSessionInfo(hs *signal.HostedSession, m proto.HostSessionInfoMsg) {
	id := hs.Info().ID
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if rc := s.recCur[id]; rc != nil && rc.h == hs && s.held(rc) {
		rc.endCommit = m.EndCommit
	}
}

// captureHostedAgentSession is captureAgentSession for a hosted session:
// the id its agent's hook reported to this server, by the catalog's recipe
// for the agent (its pattern and policy: none, nothing captured), applied
// to the agent session the record holds (journal.AgentSession), whichever
// registration of the session captured it, and settled into the record;
// the hosted Info is untouched. A hook that comes while the registration
// is still putting the session's recorder in place (the registry holds
// the session before the hub's OnAdded returns) waits for it, bounded as
// a report for a launch under way is (launchWait), and is then applied.
//
// registering true says that wait ran out on its bound, the registration
// still under way: nothing was captured, and the route refuses the report
// to be sent again. False is a capture made or nothing to capture by
// design (no recipe, no change, a record not kept or held by a newer
// registration).
func (s *Server) captureHostedAgentSession(ctx context.Context, hs *signal.HostedSession, agentSession string, turn bool) (registering bool) {
	info := hs.Info()
	policy, ok := s.agentSessionPolicy(info.AgentID, agentSession)
	if !ok {
		s.log.Debug("agent session id dropped", "session", info.ID)
		return false
	}
	select {
	case <-hs.Added():
	default:
		s.recordSeam("hostedCaptureWaits", info.ID)
		bound := launchWait
		if d := s.captureWait.Load(); d > 0 {
			bound = time.Duration(d)
		}
		t := time.NewTimer(bound)
		defer t.Stop()
		select {
		case <-hs.Added():
		case <-ctx.Done():
		case <-t.C:
			select {
			case <-hs.Added():
			default:
				return true
			}
		}
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	rc := s.recCur[info.ID]
	if rc == nil || rc.h != hs || !s.held(rc) {
		return false
	}
	var cur *session.AgentSession
	if as := s.sessionRecords.AgentSession(rc.in); as != nil {
		cur = &session.AgentSession{ID: as.ID, Source: as.Source, Resumable: as.Resumable}
	}
	next, changed := session.NextAgentSession(cur, agentSession, policy, turn)
	if !changed {
		return false
	}
	rc.as = &next
	s.reconcileLocked(rc, false)
	return false
}
