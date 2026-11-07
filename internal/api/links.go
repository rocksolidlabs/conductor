package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

type createLinkRequest struct {
	Role       session.Role `json:"role"`
	Label      string       `json:"label"`
	TTLSeconds int64        `json:"ttlSeconds"`
	// Decide makes a decision link (lane.go): control only.
	Decide bool `json:"decide"`
}

// linkView is a share link plus the number of viewers attached through it;
// Remote marks one minted at the rendezvous, whose viewers are counted there.
type linkView struct {
	*share.Link
	Active int  `json:"active"`
	Remote bool `json:"remote,omitempty"`
	// RevokePending says a decision link minted at the rendezvous was
	// revoked here and the rendezvous has not confirmed it yet: it is
	// tried again until it does, and a revoke again from the dialog tries
	// at once.
	RevokePending bool `json:"revokePending,omitempty"`
}

// sessionLinks lists a session's links: the ones made here, with their
// viewers, then the ones minted at the rendezvous for it.
func (s *Server) sessionLinks(id string, d session.Driver) []linkView {
	active := d.LinkViewers()
	links := s.links.ListBySession(id)
	out := make([]linkView, 0, len(links))
	for _, l := range links {
		if l.Remote {
			continue // listed below, as the rendezvous's
		}
		out = append(out, linkView{Link: l, Active: active[l.ID]})
	}
	for _, rl := range s.remoteLinksOf(id) {
		out = append(out, linkView{Link: &share.Link{ID: rl.ID, SessionID: id, Label: rl.Label, Role: rl.Role, CreatedAt: rl.CreatedAt, ExpiresAt: rl.ExpiresAt, Decide: rl.Decide}, Remote: true, RevokePending: rl.revokePending})
	}
	return out
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	out := map[string]any{"links": s.sessionLinks(id, d)}
	// With a switchyard configured, whether the session is published there
	// now, and whether the switchyard ended its publication, which a link
	// made now makes again: the Share dialog makes a link anew rather than
	// show one it made here while the session was not there, or one from
	// before the switchyard ended it.
	if s.publisherServer() != "" {
		out["published"] = s.publishedOf(id) != nil
		if s.owedPublication(id) {
			out["republish"] = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	// A session published to a switchyard is shared by the machine that
	// publishes it, over its own connection (link); the switchyard's
	// workbench token mints nothing for it.
	if s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusForbidden, "switchyard", "on a switchyard, links to a session are minted by the machine that publishes it")
		return
	}
	req, ok := readLinkRequest(w, r)
	if !ok {
		return
	}
	if _, local := d.(*session.Local); req.Decide && !local {
		writeError(w, http.StatusConflict, "unsupported", "a hosted session's decisions are answered on the machine that runs it")
		return
	}
	// A session published to a switchyard is viewed there: its links are
	// minted there, over the host connection. A publication still being made
	// is waited for; one that failed gives a local link that says why.
	server := s.publisherServer()
	var pub PublishedSession
	why := ""
	if server != "" {
		s.republish(id)
		wctx, wcancel := context.WithTimeout(r.Context(), publicationWait)
		pub, why = s.awaitPublication(wctx, id)
		wcancel()
	}
	if pub != nil {
		// A decision link is minted only at a switchyard that carries
		// decisions (its registered said so): an older one would mint a
		// control link that answers none.
		if req.Decide && !carriesDecisions(pub) {
			switchyardTooOld(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		res, err := pub.Link(ctx, string(req.Role), req.ttl(), req.Label)
		if err != nil {
			writeError(w, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not mint the link: "+err.Error())
			return
		}
		if req.Decide && !s.holdRemoteDecision(res, id, "") {
			// Not held here: no lane would open for it, so it is revoked there.
			_ = pub.Revoke(ctx, res.LinkID)
			writeError(w, http.StatusConflict, "too_many_links", "too many links for this session")
			return
		}
		role := res.Role
		if req.Decide {
			role = "decision link, " + laneActor(res.LinkID)
		}
		s.recordLink(id, "link created at the rendezvous: "+linkLabelOr(res.Label)+" ("+role+")")
		s.recordRemoteLink(id, res, pub.CurrentID(), req.Decide)
		link := map[string]any{"id": res.LinkID, "role": res.Role, "label": res.Label, "expiresAt": res.ExpiresAt, "sessionId": id, "remote": true}
		if req.Decide {
			link["decide"] = true
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"link":   link,
			"url":    res.URL,
			"invite": res.Invite,
			"remote": true,
		})
		return
	}
	create := s.links.Create
	if req.Decide {
		create = s.links.CreateDecision
	}
	link, token, err := create(id, req.Role, req.Label, req.ttl())
	if err != nil {
		writeLinkError(w, err, err.Error())
		return
	}
	s.recordLink(id, "link created: "+linkLabelOr(link.Label)+" ("+linkRole(link)+")")
	reply := s.linkReply(r, link, token)
	if server != "" {
		reply["rendezvous"] = map[string]any{"server": server, "error": why}
	}
	writeJSON(w, http.StatusCreated, reply)
}

// readLinkRequest reads and checks the body of a link creation. It writes the
// response and returns false when the body is refused.
func readLinkRequest(w http.ResponseWriter, r *http.Request) (createLinkRequest, bool) {
	var req createLinkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return req, false
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be view or control")
		return req, false
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > 365*24*3600 {
		writeError(w, http.StatusBadRequest, "invalid_request", "ttlSeconds out of range")
		return req, false
	}
	if req.Decide && req.Role != session.RoleControl {
		writeError(w, http.StatusBadRequest, "bad_request", "a decision link is a control link")
		return req, false
	}
	return req, true
}

// switchyardTooOld refuses a decision link a switchyard would mint whose
// registered did not say it carries decisions: its link would answer none
// (desk_publish.go).
func switchyardTooOld(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "switchyard_too_old", tooOldWords)
}

// linkRole is a link's role as the activity says it; a decision link's
// says so with its actor, link:<id>, which its receipts are kept under, so
// a reader of them finds whose link acted after the link is gone.
func linkRole(l *share.Link) string {
	if l.Decide {
		return string(l.Role) + ", answers decisions, " + laneActor(l.ID)
	}
	return string(l.Role)
}

func (req createLinkRequest) ttl() time.Duration { return time.Duration(req.TTLSeconds) * time.Second }

// writeLinkError answers a link the store would not create; tooMany is the
// message for share.ErrTooManyLinks.
func writeLinkError(w http.ResponseWriter, err error, tooMany string) {
	if errors.Is(err, share.ErrTooManyLinks) {
		writeError(w, http.StatusConflict, "too_many_links", tooMany)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "could not create link")
}

// writeLink answers a link just created: 201 {link, token, url}, the token
// shown this once and the URL the join page's.
func (s *Server) writeLink(w http.ResponseWriter, r *http.Request, link *share.Link, token string) {
	writeJSON(w, http.StatusCreated, s.linkReply(r, link, token))
}

// linkReply is a link just created as a reply carries it: {link, token, url},
// the URL on the base publicBase gives for r.
func (s *Server) linkReply(r *http.Request, link *share.Link, token string) map[string]any {
	base := s.publicBase(r)
	return map[string]any{
		"link":   link,
		"token":  token,
		"url":    base + "/join/" + url.PathEscape(token),
		"invite": inviteFor(base, token),
	}
}

// forwardedHost matches what a Host header or a reverse proxy's
// X-Forwarded-Host may carry: a host name or address, with a port.
var forwardedHost = regexp.MustCompile(`^[A-Za-z0-9.\-]+(?::\d{1,5})?$|^\[[0-9A-Fa-f:.]+\](?::\d{1,5})?$`)

// publicBase is the base URL a share link made for r is built on: publicUrl
// when it names another machine; otherwise (unset, or localhost, as the
// default and the example config have it) the public address reach found
// and mapped, once a certificate for it is ready (discoveredBase); otherwise
// the address r came through, which is the address the workbench was opened
// at: the scheme r arrived with (or the one a reverse proxy sets in
// X-Forwarded-Proto) and r's Host (or X-Forwarded-Host). A request whose
// host is missing or malformed falls back to publicUrl.
func (s *Server) publicBase(r *http.Request) string {
	if !s.cfg.PublicURLIsLocal() {
		return s.cfg.PublicURL
	}
	if base := s.discoveredBase(); base != "" {
		return base
	}
	if r == nil {
		return s.cfg.PublicURL
	}
	first := func(header string) string {
		return strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get(header), ",")[0]))
	}
	host := first("X-Forwarded-Host")
	if host == "" {
		host = strings.ToLower(r.Host)
	}
	if !forwardedHost.MatchString(host) {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := first("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + host
}

// handleCreateRunLink creates a link to a crew run: its role on the session
// of every member of the run, those that join later included.
func (s *Server) handleCreateRunLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	if _, ok := s.runs.Get(id); !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	req, ok := readLinkRequest(w, r)
	if !ok {
		return
	}
	// A run whose members are published to a switchyard is shared there:
	// one link to every member's session, minted over a member's
	// publication; with none published yet the link is made here and says why.
	server := s.publisherServer()
	why := ""
	if server != "" {
		// A decision link is minted over the run's desk (desk_publish.go),
		// opened for it when the run has none: it needs no member alive. Any
		// other link, over a member's publication.
		var carrier runCarrier
		if req.Decide {
			desk, status, code, msg := s.openRunDesk(r.Context(), id)
			if desk == nil {
				writeError(w, status, code, msg)
				return
			}
			defer s.deskMinted(id)
			carrier = desk
		} else {
			wctx, wcancel := context.WithTimeout(r.Context(), publicationWait)
			pub, w2 := s.awaitRunPublished(wctx, id)
			wcancel()
			why = w2
			if pub != nil {
				carrier = pub
			}
		}
		var (
			g      proto.RunGroup
			unlock func()
		)
		if carrier != nil {
			// In turn with the run's syncs (syncRunLinks): an update the
			// switchyard applies after this link is made would rename its
			// members. The members are read in the turn, as they are then,
			// and given the turn's revision: the link never names members
			// older than those an update sent before it.
			unlock = s.lockRunSync(id)
			run, ok := s.runs.Get(id)
			var member PublishedSession
			if ok {
				g, member = s.runGroup(run)
			}
			switch {
			case !ok:
				carrier = nil
			case !req.Decide:
				carrier = nil
				if member != nil {
					carrier = member
				}
			}
			if carrier == nil {
				unlock()
				if req.Decide {
					writeError(w, http.StatusNotFound, "not_found", "no such run")
					return
				}
				if why == "" {
					why = "no member of the run is published yet"
				}
			}
		}
		if carrier != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			sent := g
			sent.Revision = s.nextRunRevision()
			res, err := carrier.RunLink(ctx, string(req.Role), req.ttl(), req.Label, sent)
			if err != nil {
				unlock()
				writeError(w, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not mint the link: "+err.Error())
				return
			}
			// Recorded while the engine keeps the run: one forgotten meanwhile
			// took its links there with it (OnForget), this one not among
			// them, so it is revoked here and the request answered as a
			// forgotten run's. A decision link is held here too, for its lane.
			held := true
			if !s.runs.IfKept(id, func() {
				s.recordRemoteRunLink(id, res, req.Decide)
				if req.Decide {
					held = s.holdRemoteDecision(res, "", id)
				}
			}) || !held {
				unlock()
				s.forgetRemoteRunLinks(id, res.LinkID)
				s.revokeRemoteRunLinks(id, []runCarrier{carrier}, []remoteLink{{ID: res.LinkID}})
				if !held {
					writeError(w, http.StatusConflict, "too_many_links", "too many links for this run")
					return
				}
				writeError(w, http.StatusNotFound, "not_found", "no such run")
				return
			}
			// The new link names the members as they are now; the run's other
			// links there may not (a member published again just now, for one):
			// with others, the members are sent again so all of them follow.
			others := false
			if b, err := json.Marshal(g); err == nil {
				s.pubMu.Lock()
				others = len(s.remoteRunLinks[id]) > 1
				if others {
					delete(s.runSent, id)
				} else {
					if s.runSent == nil {
						s.runSent = map[string]string{}
					}
					s.runSent[id] = string(b)
				}
				s.pubMu.Unlock()
			}
			unlock()
			if others {
				s.scheduleRunSync(id)
			}
			role := res.Role
			if req.Decide {
				role = "decision link, " + laneActor(res.LinkID)
			}
			s.runs.Note(id, session.ActivityLink, "link created at the rendezvous: "+linkLabelOr(res.Label)+" ("+role+")")
			link := map[string]any{"id": res.LinkID, "role": res.Role, "label": res.Label, "expiresAt": res.ExpiresAt, "runId": id, "remote": true}
			if req.Decide {
				link["decide"] = true
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"link":   link,
				"url":    res.URL,
				"invite": res.Invite,
				"remote": true,
			})
			return
		}
	}
	// The link is made under the check that the run is kept: a run forgotten
	// in between takes its links (OnForget), and this one would be left over.
	var (
		link  *share.Link
		token string
		err   error
	)
	create := s.links.CreateRunLink
	if req.Decide {
		create = s.links.CreateRunDecision
	}
	if !s.runs.IfKept(id, func() { link, token, err = create(id, req.Role, req.Label, req.ttl()) }) {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	if err != nil {
		writeLinkError(w, err, "too many links for this run")
		return
	}
	s.runs.Note(id, session.ActivityLink, "link created: "+linkLabelOr(link.Label)+" ("+linkRole(link)+")")
	if server != "" {
		reply := s.linkReply(r, link, token)
		reply["rendezvous"] = map[string]any{"server": server, "error": why}
		writeJSON(w, http.StatusCreated, reply)
		return
	}
	s.writeLink(w, r, link, token)
}

// handleListRunLinks lists a run's links, each with the viewers attached
// through it to the sessions of the run's members.
func (s *Server) handleListRunLinks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	run, ok := s.runs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	active := map[string]int{}
	for _, m := range run.Members {
		if m.SessionID == "" {
			continue
		}
		if d, ok := s.registry.Get(m.SessionID); ok {
			for linkID, n := range d.LinkViewers() {
				active[linkID] += n
			}
		}
	}
	links := s.links.ListByRun(id)
	out := make([]linkView, 0, len(links))
	for _, l := range links {
		if l.Remote {
			continue // listed below, as the rendezvous's
		}
		out = append(out, linkView{Link: l, Active: active[l.ID]})
	}
	for _, rl := range s.remoteRunLinksOf(id) {
		out = append(out, linkView{Link: &share.Link{ID: rl.ID, RunID: id, Role: rl.Role, Label: rl.Label, CreatedAt: rl.CreatedAt, ExpiresAt: rl.ExpiresAt, Decide: rl.Decide}, Remote: true, RevokePending: rl.revokePending})
	}
	reply := map[string]any{"links": out}
	// As a session's (handleListLinks): whether a member is published, and
	// whether a member's publication the switchyard ended is made again by
	// a run link made now.
	if s.publisherServer() != "" {
		published, owed := false, false
		for _, m := range run.Members {
			if m.SessionID == "" {
				continue
			}
			published = published || s.publishedOf(m.SessionID) != nil
			owed = owed || s.owedPublication(m.SessionID)
		}
		reply["published"] = published
		if owed {
			reply["republish"] = true
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// handleRevokeRunLink revokes a run's link, which closes every viewer
// attached through it, and notes it in the run's log.
func (s *Server) handleRevokeRunLink(w http.ResponseWriter, r *http.Request) {
	id, linkID := r.PathValue("run"), r.PathValue("linkId")
	label := ""
	remote := false
	if l, ok := s.links.Get(linkID); ok {
		label, remote = l.Label, l.Remote && l.RunID == id
	}
	found, revoked := false, false
	if remote {
		// A decision link minted at the rendezvous ends here first, through
		// its barrier (nothing of its lane begun after), then there; the
		// run's desk, which may carry the revoke, is held until it has.
		// Its record stays, listed as owing its revoke there, until the
		// rendezvous confirms it (remoteRevoked): one it did not is tried
		// again (retryRevokeThere), and a revoke again from here tries at
		// once.
		defer s.holdDesk(id)()
		s.links.RevokeRun(id, linkID)
	} else {
		found, revoked = s.links.RevokeRun(id, linkID)
	}
	if !found {
		// A link minted at the rendezvous is revoked there, over any member's
		// publication, else over the run's desk.
		if s.hasRemoteRunLink(id, linkID) || remote {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if err := s.carryRunRevoke(ctx, id, linkID); err != nil {
				if _, owed := s.revokeOwed("", id, linkID); owed {
					s.retryRevokeThere("", id, linkID)
				}
				if errors.Is(err, errNoCarrier) {
					writeError(w, http.StatusBadGateway, "rendezvous_unavailable", err.Error())
					return
				}
				writeError(w, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not revoke the link: "+err.Error())
				return
			}
			s.forgetRemoteRunLinks(id, linkID)
			s.runs.Note(id, session.ActivityLink, "link revoked at the rendezvous: "+linkLabelOr(label))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	if revoked {
		s.runs.Note(id, session.ActivityLink, "link revoked: "+linkLabelOr(label))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeLink(w http.ResponseWriter, r *http.Request) {
	id, linkID := r.PathValue("id"), r.PathValue("linkId")
	label := ""
	remote := false
	if l, ok := s.links.Get(linkID); ok {
		label, remote = l.Label, l.Remote && l.RunID == "" && l.SessionID == id
	}
	found, revoked := false, false
	if remote {
		// A decision link minted at the rendezvous ends here first, through
		// its barrier (nothing of its lane begun after), then there.
		s.links.Revoke(id, linkID)
	} else {
		found, revoked = s.links.Revoke(id, linkID)
	}
	if !found && remote && s.publishedOf(id) == nil {
		// Its publication is gone, and the link with it there.
		s.forgetRemoteLink(id, linkID)
		s.recordLink(id, "link revoked: "+linkLabelOr(label))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !found {
		// A link minted at the rendezvous is revoked there, over the host
		// connection; one it no longer knows is gone either way.
		if pub := s.publishedOf(id); pub != nil && (remote || s.hasRemoteLink(id, linkID)) {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if err := pub.Revoke(ctx, linkID); err != nil && !strings.Contains(err.Error(), "not_found") {
				// A decision link revoked here stays listed, owing its
				// revoke there, which is tried again until it is done.
				if _, owed := s.revokeOwed(id, "", linkID); owed {
					s.retryRevokeThere(id, "", linkID)
				}
				writeError(w, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not revoke the link: "+err.Error())
				return
			}
			s.forgetRemoteLink(id, linkID)
			s.recordLink(id, "link revoked at the rendezvous: "+linkLabelOr(label))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// An ended session took its links with it: revoking one a dialog
		// still shows is done. Its viewers may still be written what the
		// session's end owed them (for closeWait at most): the revoke stops
		// that, here, and at the rendezvous while the publication outlasts
		// the end for as long (hostagent's endLinger); one it cannot carry
		// there then says so.
		if d, ok := s.registry.Get(id); ok && d.Info().Status.Ended() {
			d.DisconnectLink(linkID)
			if pub := s.publishedOf(id); pub != nil && !ended(pub.Done()) {
				ctx, cancel := context.WithTimeout(r.Context(), closeWait)
				err := pub.Revoke(ctx, linkID)
				cancel()
				if err != nil && !strings.Contains(err.Error(), "not_found") {
					writeError(w, http.StatusBadGateway, "rendezvous_unavailable", "the rendezvous did not revoke the link: "+err.Error())
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such link")
		return
	}
	if revoked {
		s.recordLink(id, "link revoked: "+linkLabelOr(label))
	}
	w.WriteHeader(http.StatusNoContent)
}

// ended says whether done is closed.
func ended(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// hasRemoteLink says whether session id's current publication holds linkID.
func (s *Server) hasRemoteLink(id, linkID string) bool {
	for _, rl := range s.remoteLinksOf(id) {
		if rl.ID == linkID {
			return true
		}
	}
	return false
}

// recordLink adds a link event to a server session's activity log. Hosted
// sessions keep their log on the host, which never learns about links.
func (s *Server) recordLink(sessionID, message string) {
	if d, ok := s.registry.Get(sessionID); ok {
		if l, ok := d.(*session.Local); ok {
			l.Record(session.ActivityEntry{Type: session.ActivityLink, Message: message})
		}
	}
}

func linkLabelOr(label string) string {
	if label == "" {
		return "unlabelled"
	}
	return label
}

// handleJoin resolves a share token for the join page.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	link, err := s.links.Resolve(r.PathValue("token"))
	if err != nil {
		code := "invalid_link"
		switch {
		case errors.Is(err, share.ErrRevoked):
			code = "revoked"
		case errors.Is(err, share.ErrExpired):
			code = "expired"
		}
		writeError(w, http.StatusNotFound, code, "this link is not valid")
		return
	}
	if link.Group != nil {
		s.joinGroup(w, link)
		return
	}
	if link.RunID != "" {
		s.joinRun(w, link)
		return
	}
	d, ok := s.registry.Get(link.SessionID)
	if !ok {
		if link.Owner != "" {
			// A kept link whose host is away: it works again once the host registers.
			writeError(w, http.StatusServiceUnavailable, "host_offline", "the machine that shared this session is not connected to the switchyard right now; try again in a moment")
			return
		}
		writeError(w, http.StatusNotFound, "session_gone", "the shared session no longer exists")
		return
	}
	info := d.Info()
	writeJSON(w, http.StatusOK, map[string]any{
		"session": map[string]any{
			"id":       info.ID,
			"name":     info.Name,
			"agentId":  info.AgentID,
			"kind":     info.Kind,
			"status":   info.Status,
			"cols":     info.Cols,
			"rows":     info.Rows,
			"hostName": info.HostName,
			"hostUser": info.HostUser,
			// The working directory roots the guest's Files pane (design 4g); reads are still the session's to allow.
			"cwd": info.Cwd,
		},
		"role":       link.Role,
		"label":      link.Label,
		"switchyard": s.cfg.Switchyard.Enabled,
	})
}

// joinMember is a member of a run as the join page sees it: SessionID,
// AgentID and Status those of its session while that runs; otherwise its
// state in the run, pending, starting or ended, and no session.
type joinMember struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId,omitempty"`
	AgentID   string `json:"agentId"`
	Status    string `json:"status"`
	// Kind is hosted for a member of a run shared through a switchyard,
	// whose session the page reaches over WebRTC.
	Kind string `json:"kind,omitempty"`
}

// joinGroup answers the join page for a host's run link on a switchyard: the
// run as its host last told it, each member's session as registered here
// now. No member registered at all is the host away (503 host_offline).
func (s *Server) joinGroup(w http.ResponseWriter, link *share.Link) {
	members := make([]joinMember, 0, len(link.Group.Members))
	seen := false
	for _, m := range link.Group.Members {
		jm := joinMember{Name: m.Name, AgentID: m.AgentID, Status: m.Status, Kind: string(session.KindHosted)}
		if m.SessionID != "" {
			jm.Status = crew.MemberEnded
			if d, ok := s.registry.Get(m.SessionID); ok {
				seen = true
				if info := d.Info(); !info.Status.Ended() {
					jm.SessionID, jm.AgentID, jm.Status = info.ID, info.AgentID, string(info.Status)
				}
			}
		}
		members = append(members, jm)
	}
	// The run's desk, while it is here: what a decision link's viewer joins
	// for the run's decisions, with no member alive too (PR-6).
	desk := ""
	if id := link.Group.Desk; id != "" {
		if _, ok := s.hosts.Desk(id); ok {
			desk, seen = id, true
		}
	}
	if !seen && (link.Group.Desk != "" || slices.ContainsFunc(link.Group.Members, func(m share.GroupMember) bool { return m.SessionID != "" })) {
		writeError(w, http.StatusServiceUnavailable, "host_offline", "the machine that shared this crew is not connected to the switchyard right now; try again in a moment")
		return
	}
	run := map[string]any{"id": link.RunID, "name": link.Group.Name, "members": members}
	if desk != "" {
		run["desk"] = desk
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":        run,
		"role":       link.Role,
		"label":      link.Label,
		"switchyard": s.cfg.Switchyard.Enabled,
	})
}

// joinRun answers the join page for a run link: the run and its members.
func (s *Server) joinRun(w http.ResponseWriter, link *share.Link) {
	run, ok := s.runs.Get(link.RunID)
	if !ok {
		writeError(w, http.StatusNotFound, "run_gone", "the shared run no longer exists")
		return
	}
	members := make([]joinMember, 0, len(run.Members))
	for _, m := range run.Members {
		jm := joinMember{Name: m.Name, AgentID: m.AgentID, Status: m.Status}
		if m.SessionID != "" {
			jm.Status = crew.MemberEnded
			if d, ok := s.registry.Get(m.SessionID); ok && m.Status != crew.MemberEnded {
				if info := d.Info(); !info.Status.Ended() {
					jm.SessionID, jm.AgentID, jm.Status = info.ID, info.AgentID, string(info.Status)
				}
			}
		}
		members = append(members, jm)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":        map[string]any{"id": run.ID, "name": run.Name, "members": members},
		"role":       link.Role,
		"label":      link.Label,
		"switchyard": s.cfg.Switchyard.Enabled,
	})
}
