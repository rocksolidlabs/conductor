package api

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The self-service routes: what an agent may do with its own session's
// token (docs/protocol.md, "Agents that form crews"). Each takes the agent
// token of the session in the path (or the workbench token), refuses a hosted
// session, and is bounded per session: an agent never holds the admin
// token, and what it forms runs with its own session's working directory
// and yolo choice, never more.
const (
	selfCrewsPerHour = 2
	selfLinksPerDay  = 5
	selfLinkMaxTTL   = 24 * time.Hour
	selfLinkTTL      = 2 * time.Hour
	selfRunLogTail   = 50
)

// selfCrewInput is the body of POST /api/sessions/{id}/crew: a crew without
// where, cwd, yolo or the link settings (the session's own decide), and the
// member the calling session becomes.
type selfCrewInput struct {
	Name      string        `json:"name"`
	Goal      string        `json:"goal"`
	Isolation string        `json:"isolation"`
	Members   []crew.Member `json:"members"`
	Self      string        `json:"self"`
	// Open asks the workbench to offer the run (a toast with Open); the
	// link entry on the session carries it.
	Open bool `json:"open"`
}

// selfLinkInput is the body of POST /api/sessions/{id}/links.
type selfLinkInput struct {
	TTLSeconds int64  `json:"ttlSeconds"`
	Label      string `json:"label"`
}

// selfCounters bound what each session may do: crews formed per hour,
// links minted per day.
type selfCounters struct {
	mu    sync.Mutex
	crews map[string][]time.Time
	links map[string][]time.Time
	now   func() time.Time
}

func newSelfCounters() *selfCounters {
	return &selfCounters{crews: map[string][]time.Time{}, links: map[string][]time.Time{}, now: time.Now}
}

// take records one use for id in m within window when fewer than limit are
// there, and reports whether it was allowed.
func (c *selfCounters) take(m map[string][]time.Time, id string, limit int, window time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var kept []time.Time
	for _, t := range m[id] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		m[id] = kept
		return false
	}
	m[id] = append(kept, now)
	return true
}

// selfSession authenticates a self-service request: the session must be a
// server session and the caller its agent (or the admin); the response is
// written and ok false otherwise.
func (s *Server) selfSession(w http.ResponseWriter, r *http.Request) (*session.Local, bool) {
	if !s.cfg.SelfService() {
		writeError(w, http.StatusForbidden, "self_service_off", "agents may not serve themselves on this server (agents.selfService)")
		return nil, false
	}
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return nil, false
	}
	if _, ok := s.attentionPrincipal(w, r, d); !ok {
		return nil, false
	}
	local, ok := d.(*session.Local)
	if !ok {
		writeError(w, http.StatusBadRequest, "hosted_session", "a hosted session cannot form a crew on the server: its working directory is on its own machine")
		return nil, false
	}
	return local, true
}

// handleSelfCrew forms a crew around the calling session and launches it:
// the crew is saved under the session's working directory and yolo choice,
// the member named self is the session itself, the others start as at
// launch. 201 {run, crew, member, url}.
func (s *Server) handleSelfCrew(w http.ResponseWriter, r *http.Request) {
	local, ok := s.selfSession(w, r)
	if !ok || brakemanCannot(w, local) {
		return
	}
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	var in selfCrewInput
	if err := decodeJSONLimit(w, r, &in, maxCrewBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	info := local.Info()
	if info.Status.Ended() {
		writeError(w, http.StatusConflict, "session_ended", "the session has ended")
		return
	}
	if _, _, inRun := s.runs.MemberOf(info.ID); inRun {
		writeError(w, http.StatusConflict, "in_a_run", "the session is a member of a run already; add members to it instead")
		return
	}
	// The self member's agent is this session's: an empty one means that.
	for i := range in.Members {
		if in.Members[i].Name == strings.TrimSpace(in.Self) && in.Members[i].AgentID == "" {
			in.Members[i].AgentID = info.AgentID
		}
	}
	yolo := info.Yolo
	c := crew.Crew{Name: strings.TrimSpace(in.Name), Goal: in.Goal, Cwd: info.Cwd, Where: crew.WhereServer, Isolation: cmp.Or(in.Isolation, crew.IsolationNone), Yolo: &yolo, Members: in.Members}
	if err := c.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	if err := c.CheckAgents(s.Catalog()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	self := strings.TrimSpace(in.Self)
	var selfDef *crew.Member
	var others []crew.Member
	for i := range c.Members {
		if c.Members[i].Name == self {
			selfDef = &c.Members[i]
		} else {
			others = append(others, c.Members[i])
		}
	}
	if selfDef == nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", "self must name one of the members: the one this session becomes")
		return
	}
	if selfDef.AgentID != info.AgentID {
		writeError(w, http.StatusBadRequest, "invalid_crew", "the self member's agent must be this session's ("+info.AgentID+")")
		return
	}
	cat := s.Catalog()
	if err := checkLaunch(others, cat, s.installedFor(r.Context(), cat, others), s.identityFor(r.Context(), cat, others)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	if !s.selfLimits.take(s.selfLimits.crews, info.ID, selfCrewsPerHour, time.Hour) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "this session has formed its crews for the hour")
		return
	}
	saved, err := s.crews.Create(c)
	if err != nil {
		s.crewStoreError(w, "", err)
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	run, release, err := s.runs.LaunchAdopting(ctx, saved, self, info.ID)
	defer release()
	if err != nil {
		s.runError(w, "self crew", saved.ID, err)
		return
	}
	local.SetCrew(session.CrewRef{RunID: run.ID, CrewID: saved.ID, Member: self})
	url := s.publicBase(r) + "/runs/" + run.ID
	s.log.Info("crew formed by its own member", "crew", saved.ID, "run", run.ID, "session", info.ID, "member", self)
	local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: "formed crew " + saved.Name + ": " + itoa(len(c.Members)) + " members, as " + self})
	msg := "formed crew " + saved.Name
	if in.Open {
		msg += " (open)"
	}
	local.Record(session.ActivityEntry{Type: session.ActivityLink, Message: msg, URL: url})
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "crew": saved, "member": self, "url": url})
}

func itoa(n int) string { return strconv.Itoa(n) }

// handleSelfAddMember adds a member to the calling session's run: 201 {run}.
func (s *Server) handleSelfAddMember(w http.ResponseWriter, r *http.Request) {
	local, ok := s.selfSession(w, r)
	if !ok || brakemanCannot(w, local) {
		return
	}
	runID, _, inRun := s.runs.MemberOf(local.Info().ID)
	if !inRun {
		writeError(w, http.StatusConflict, "no_run", "the session is not a member of a run; form a crew first")
		return
	}
	var m crew.Member
	if err := decodeJSONLimit(w, r, &m, maxMemberBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cat := s.Catalog()
	if _, ok := cat.Get(m.AgentID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_crew", "unknown agent "+m.AgentID)
		return
	}
	if err := checkLaunch([]crew.Member{m}, cat, s.installedFor(r.Context(), cat, []crew.Member{m}), s.identityFor(r.Context(), cat, []crew.Member{m})); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	if err := s.runs.AddMember(ctx, runID, m); err != nil {
		s.runError(w, "self add member", runID, err)
		return
	}
	run, _ := s.runs.Get(runID)
	local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: "added " + m.Name + " (" + m.AgentID + ") to the run"})
	writeJSON(w, http.StatusCreated, map[string]any{"run": run})
}

// handleSelfRun answers the calling session's run, its log cut to the last
// selfRunLogTail entries: 200 {run, member}; 404 no_run.
func (s *Server) handleSelfRun(w http.ResponseWriter, r *http.Request) {
	local, ok := s.selfSession(w, r)
	if !ok {
		return
	}
	runID, member, inRun := s.runs.MemberOf(local.Info().ID)
	if !inRun {
		writeError(w, http.StatusNotFound, "no_run", "the session is not a member of a run")
		return
	}
	run, ok := s.runs.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "no_run", "the run is gone")
		return
	}
	if len(run.Log) > selfRunLogTail {
		run.Log = run.Log[len(run.Log)-selfRunLogTail:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "member": member})
}

// handleSelfLink mints a view-only link to the calling session: 201
// {link, token, url}; the TTL defaults to two hours and tops at a day.
func (s *Server) handleSelfLink(w http.ResponseWriter, r *http.Request) {
	local, ok := s.selfSession(w, r)
	if !ok {
		return
	}
	var in selfLinkInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ttl := selfLinkTTL
	if in.TTLSeconds > 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}
	if ttl > selfLinkMaxTTL {
		writeError(w, http.StatusBadRequest, "invalid_request", "ttlSeconds is at most 86400: an agent's link lasts a day at most")
		return
	}
	id := local.Info().ID
	if !s.selfLimits.take(s.selfLimits.links, id, selfLinksPerDay, 24*time.Hour) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "this session has minted its links for the day")
		return
	}
	link, token, err := s.links.Create(id, session.RoleView, in.Label, ttl)
	if err != nil {
		writeLinkError(w, err, err.Error())
		return
	}
	s.recordLink(id, "link created by the agent: "+linkLabelOr(link.Label)+" (view)")
	s.writeLink(w, r, link, token)
}
