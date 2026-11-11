package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Publisher publishes a server session to a rendezvous Conductor
// (hostagent.Uplink); PublishedSession is one publication.
type Publisher interface {
	Publish(ctx context.Context, local *session.Local) (PublishedSession, error)
	// Server is the rendezvous's URL, for a reply that says where a session
	// was meant to be published: its scheme, host and path, never user
	// information, a query or a fragment.
	Server() string
}

// PublishedSession is a session published to the rendezvous: its hooks get
// the local session's changes and entries, Stop ends the publication.
type PublishedSession interface {
	OnChange(info session.Info)
	OnActivity(id string, e session.ActivityEntry, state session.AttentionState)
	OnChat(id string, m session.ChatMessage)
	Stop()
	Base() string
	ID() string
	// Link mints a share link to the session at the rendezvous.
	Link(ctx context.Context, role string, ttl time.Duration, label string) (proto.LinkCreated, error)
	// Revoke revokes a link minted there; one it no longer knows answers an
	// error naming not_found.
	Revoke(ctx context.Context, linkID string) error
	// CurrentID is the rendezvous's id for the session now: ID unless the
	// rendezvous restarted and the session was registered afresh under
	// another id (an older rendezvous, which ignores host instances).
	CurrentID() string
	// HeldLinks is what the rendezvous last said it holds of the links
	// minted for the session; false when it never said (an older one).
	HeldLinks() ([]string, bool)
	// Done is closed once the publication has ended, whatever ended it;
	// StoppedThere says the rendezvous ended it with a stop of its own, the
	// session going on here.
	Done() <-chan struct{}
	StoppedThere() bool
	// RunLink mints, at the rendezvous, one link to the sessions of a run's
	// members there; RunLinkUpdate tells it the members now. Either goes over
	// this session's connection, which must be a member's.
	RunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error)
	RunLinkUpdate(ctx context.Context, run proto.RunGroup) error
}

// remoteLink is a link minted at the rendezvous for a published session,
// kept here so the session's links list it and can revoke it; hostedID is
// the rendezvous's session id it was minted for, so a re-registration after
// a rendezvous restart drops it (the rendezvous keeps links in memory).
type remoteLink struct {
	ID        string
	Role      session.Role
	Label     string
	CreatedAt time.Time
	ExpiresAt *time.Time
	hostedID  string
	// Decide says it is a decision link (desk_publish.go): this server
	// holds it as a remote link for its lane.
	Decide bool
	// revokePending says the decision link was revoked here and its revoke
	// is still owed at the rendezvous (desk_publish.go, remoteRevoked): it
	// is listed so, and its record kept, until the rendezvous confirms.
	revokePending bool
}

// recordRemoteLink keeps a link minted at the rendezvous for session id.
func (s *Server) recordRemoteLink(id string, res proto.LinkCreated, hostedID string, decide bool) {
	rl := remoteLink{ID: res.LinkID, Role: session.Role(res.Role), Label: res.Label, CreatedAt: time.Now(), hostedID: hostedID, Decide: decide}
	if t, err := time.Parse(time.RFC3339, res.ExpiresAt); err == nil && res.ExpiresAt != "" {
		rl.ExpiresAt = &t
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.remoteLinks == nil {
		s.remoteLinks = map[string]map[string]remoteLink{}
	}
	if s.remoteLinks[id] == nil {
		s.remoteLinks[id] = map[string]remoteLink{}
	}
	s.remoteLinks[id][res.LinkID] = rl
}

// remoteLinksOf lists the links minted at the rendezvous for session id that
// its current publication still holds, newest first; stale ones are dropped.
func (s *Server) remoteLinksOf(id string) []remoteLink {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	pub := s.published[id]
	var held []string
	known := false
	if pub != nil {
		held, known = pub.HeldLinks()
	}
	var out []remoteLink
	for lid, rl := range s.remoteLinks[id] {
		// Gone with the publication; else the rendezvous's word, when it
		// gives one; else (an older one) a session registered afresh lost them.
		gone := pub == nil
		switch {
		case gone:
		case known:
			gone = !slices.Contains(held, lid)
		default:
			gone = rl.hostedID != pub.CurrentID()
		}
		if gone {
			// The rendezvous holds it no more: settled with the publication
			// gone. Its word may also be a link_gone (ended), which comes
			// before its revoke there is confirmed: a decision link owed it
			// stays, listed (forgetRemote).
			s.forgetRemoteLocked(id, "", lid, pub == nil)
			if _, kept := s.remoteLinks[id][lid]; !kept {
				continue
			}
		}
		out = append(out, rl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// forgetRemoteLink drops the record of a remote link, unsettled
// (forgetRemote): a decision link's still owed its revoke at the
// rendezvous is kept.
func (s *Server) forgetRemoteLink(id, linkID string) {
	s.forgetRemote(id, "", linkID, false)
}

// SetPublisher makes every server session created from now on published
// to the rendezvous; nil stops that.
func (s *Server) SetPublisher(p Publisher) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.publisher = p
}

// publish publishes local on a goroutine of the server's own: the dial to
// the rendezvous must not hold the launch. The result is recorded in the
// session's activity.
func (s *Server) publish(local *session.Local) {
	s.pubMu.Lock()
	p := s.publisher
	s.pubMu.Unlock()
	if p == nil {
		return
	}
	// A link request made meanwhile waits for this to end (awaitPublication).
	// One publication of a session is made at a time: a second asked for
	// while one is under way (two Shares republishing at once) is that one.
	id := local.Info().ID
	pending := make(chan struct{})
	s.pubMu.Lock()
	if s.pubPending == nil {
		s.pubPending = map[string]chan struct{}{}
	}
	if s.pubPending[id] != nil {
		s.pubMu.Unlock()
		return
	}
	s.pubPending[id] = pending
	delete(s.pubErr, id)
	s.pubMu.Unlock()
	s.track(func() {
		defer func() {
			s.pubMu.Lock()
			delete(s.pubPending, id)
			s.pubMu.Unlock()
			close(pending)
		}()
		setWhy := func(why string) {
			s.pubMu.Lock()
			if s.pubErr == nil {
				s.pubErr = map[string]string{}
			}
			s.pubErr[id] = why
			s.pubMu.Unlock()
		}
		// A refusal that passes with time (the switchyard's 429: a rate, or
		// its limit of live sessions per address) is tried again a few times
		// over some minutes. Not publishing is never the agent's error: the
		// session's activity says so as a status line, not an "error", which
		// put a red badge on a crew member that had done nothing (round 14).
		var pub PublishedSession
		for attempt := 0; ; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var err error
			pub, err = p.Publish(ctx, local)
			cancel()
			if err == nil {
				if attempt > 0 {
					local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: fmt.Sprintf("shared through the switchyard after %d tries", attempt+1)})
				}
				break
			}
			wait, again := publishRetry(err, attempt)
			s.log.Warn("publish to the rendezvous failed", "session", id, "err", err.Error(), "tryAgain", again)
			if !again {
				local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: "not shared through the switchyard: " + err.Error()})
				setWhy(err.Error())
				return
			}
			if attempt == 0 {
				local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: "not shared through the switchyard yet: " + err.Error() + "; trying again"})
			}
			setWhy(err.Error() + "; trying again")
			select {
			case <-time.After(wait):
			case <-local.Ended():
				setWhy(err.Error())
				return
			}
		}
		s.pubMu.Lock()
		if s.published == nil {
			s.published = map[string]PublishedSession{}
		}
		gone := s.pubGone[local.Info().ID]
		if !gone {
			s.published[local.Info().ID] = pub
			delete(s.pubStopped, local.Info().ID)
		}
		s.pubMu.Unlock()
		if gone {
			pub.Stop()
			return
		}
		go s.watchPublication(local, pub)
		url := pub.Base() + "/sessions/" + pub.ID()
		s.log.Info("published to the rendezvous", "session", local.Info().ID, "at", url)
		local.Record(session.ActivityEntry{Type: session.ActivityLink, Message: "published at the rendezvous", URL: url})
		// What changed before the publication reached the rendezvous.
		pub.OnChange(local.Info())
		// A member published now: its run's links there name its session.
		if runID, _, ok := s.runs.MemberOf(local.Info().ID); ok {
			s.scheduleRunSync(runID)
		}
	})
}

// stoppedThere is why a session is not published once the switchyard ended
// its publication with a stop of its own.
const stoppedThere = "the switchyard ended its publication; Share publishes it again"

// watchPublication waits for pub to end while local runs. A publication the
// rendezvous ended with a stop of its own (StoppedThere) is forgotten here,
// its links there with it, and the session's activity says so: the session
// goes on, unpublished, until its owner shares it again (republish).
func (s *Server) watchPublication(local *session.Local, pub PublishedSession) {
	select {
	case <-pub.Done():
	case <-local.Ended():
		return
	}
	if !pub.StoppedThere() {
		return
	}
	id := local.Info().ID
	s.pubMu.Lock()
	ours := s.published[id] == pub
	if ours {
		delete(s.published, id)
		s.forgetRemoteLocked(id, "", "", true)
		if s.pubErr == nil {
			s.pubErr = map[string]string{}
		}
		s.pubErr[id] = stoppedThere
		if s.pubStopped == nil {
			s.pubStopped = map[string]bool{}
		}
		s.pubStopped[id] = true
	}
	s.pubMu.Unlock()
	if !ours {
		return
	}
	s.log.Info("the rendezvous ended a publication", "session", id)
	local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: "no longer shared through the switchyard: it ended the publication. The session goes on here; Share publishes it again"})
	if runID, _, ok := s.runs.MemberOf(id); ok {
		s.scheduleRunSync(runID)
	}
}

// owedPublication says the switchyard ended session id's publication and
// none has been made since: the next link made for it publishes it again.
func (s *Server) owedPublication(id string) bool {
	s.pubMu.Lock()
	owed := s.pubStopped[id] && s.publisher != nil && s.published[id] == nil
	s.pubMu.Unlock()
	if !owed {
		return false
	}
	// A session that ended is owed nothing: there is nothing left to share.
	d, ok := s.registry.Get(id)
	_, isLocal := d.(*session.Local)
	return ok && isLocal && !d.Info().Status.Ended()
}

// republish publishes session id again when the switchyard ended its
// publication: its owner's Share is the word to. The session stays owed a
// publication until one is made, so a Share whose try failed (the
// switchyard unreachable for a while) is followed by another try at the
// next. It reports whether it tried.
func (s *Server) republish(id string) bool {
	s.pubMu.Lock()
	again := s.pubStopped[id] && s.publisher != nil && s.published[id] == nil && s.pubPending[id] == nil
	s.pubMu.Unlock()
	if !again {
		return false
	}
	d, ok := s.registry.Get(id)
	local, isLocal := d.(*session.Local)
	if !ok || !isLocal || local.Info().Status.Ended() {
		return false
	}
	s.publish(local)
	return true
}

// awaitPublication is the publication of session id, waiting as long as ctx
// allows for one still being made; when there is none it says why: the
// publish's error, or that it is still connecting.
func (s *Server) awaitPublication(ctx context.Context, id string) (PublishedSession, string) {
	s.pubMu.Lock()
	pub, pending, why := s.published[id], s.pubPending[id], s.pubErr[id]
	s.pubMu.Unlock()
	if pub != nil {
		return pub, ""
	}
	if pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
			// Tried again after a refusal: what it said; else still connecting.
			s.pubMu.Lock()
			why = s.pubErr[id]
			s.pubMu.Unlock()
			if why != "" {
				return nil, why
			}
			return nil, "still connecting to the switchyard"
		}
		s.pubMu.Lock()
		pub, why = s.published[id], s.pubErr[id]
		s.pubMu.Unlock()
		if pub != nil {
			return pub, ""
		}
	}
	if why == "" {
		why = "not published"
	}
	return nil, why
}

// publisherServer is the rendezvous sessions are published to, "" for none.
func (s *Server) publisherServer() string {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.publisher == nil {
		return ""
	}
	return s.publisher.Server()
}

// publishedOf is the publication of a session, nil for none.
func (s *Server) publishedOf(id string) PublishedSession {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	return s.published[id]
}

// unpublish ends a session's publication when it leaves the registry.
func (s *Server) unpublish(id string) {
	s.pubMu.Lock()
	pub := s.published[id]
	delete(s.published, id)
	s.forgetRemoteLocked(id, "", "", true)
	delete(s.pubErr, id)
	delete(s.pubStopped, id)
	if s.publisher != nil {
		if s.pubGone == nil {
			s.pubGone = map[string]bool{}
		}
		s.pubGone[id] = true
	}
	s.pubMu.Unlock()
	if pub != nil {
		pub.Stop()
	}
}

// localActivity is the OnActivity hook of a server session: the entry goes
// to the event hub (and through it to the runs and the webhooks), and to the
// rendezvous when the session is published. Neither waits.
func (s *Server) localActivity(id string, e session.ActivityEntry, state session.AttentionState) {
	s.events.activity(id, e, state)
	if pub := s.publishedOf(id); pub != nil {
		pub.OnActivity(id, e, state)
	}
}

// endLinks takes a session's links with it when it ends, a server
// session's (localChange) or a hosted one's (the hub's OnChange): there is
// nothing left to share. Its links here are marked revoked and forgotten,
// and whoever used them is closed as the session's end (EndLink), written
// what was queued for them first, not as revoked (a revoke writes nothing
// more). Those minted at the rendezvous are forgotten here and not revoked
// there: the rendezvous ends them itself, the same way, as it hears the
// session ended (the publication's status), where a revocation from here
// could reach it first and cut their viewers off as revoked. Calling it
// again finds nothing to do.
func (s *Server) endLinks(id string) {
	d, _ := s.registry.Get(id)
	for _, l := range s.links.ListBySession(id) {
		if s.links.End(id, l.ID) && d != nil {
			d.EndLink(l.ID)
		}
	}
	for _, lid := range s.links.DeleteSession(id, true) {
		s.dropLinkFile(lid)
	}
	s.pubMu.Lock()
	s.forgetRemoteLocked(id, "", "", true)
	delete(s.pubStopped, id)
	s.pubMu.Unlock()
}

// publicationWait is how long a link request waits for a publication under
// way before it makes a local link that says why.
var publicationWait = 10 * time.Second

// publishBackoff is how long a publication refused for now waits before each
// try again: about six minutes in all.
var publishBackoff = []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 2 * time.Minute, 2 * time.Minute}

// publishRetry says whether a failed publication is tried again after
// attempt, and when: a refusal that passes with time (Retryable), at most
// len(publishBackoff) times, after the backoff or the wait the rendezvous
// asked for, whichever is longer (at most five minutes).
func publishRetry(err error, attempt int) (time.Duration, bool) {
	var r interface{ Retryable() (bool, time.Duration) }
	if !errors.As(err, &r) || attempt >= len(publishBackoff) {
		return 0, false
	}
	ok, after := r.Retryable()
	if !ok {
		return 0, false
	}
	return max(publishBackoff[attempt], min(after, 5*time.Minute)), true
}
