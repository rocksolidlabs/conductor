package api

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Resume. A server session whose agent has a session recipe
// (catalog.SessionRecipe) knows its agent's own session: the id it chose at
// launch, or the one the agent's hooks report. An ended session can be
// resumed: a new session with the same agent, name, directory, arguments,
// yolo choice and crew membership, launched with the recipe's resume
// arguments; without a recipe, or before the agent has had a turn, it is
// relaunched plainly and the reply says so.

// sessionPattern returns agent's id pattern and its recipe, or nil when it
// has no recipe.
func sessionPattern(agent catalog.Agent) (*regexp.Regexp, *catalog.SessionRecipe) {
	r := agent.Session
	if r.Empty() {
		return nil, nil
	}
	re, err := regexp.Compile(r.IDPattern)
	if err != nil {
		return nil, nil
	}
	return re, r
}

// captureAgentSession keeps the agent session id a report of local's agent
// names, when the agent's recipe takes ids from its hooks and the id has the
// recipe's shape; anything else is dropped.
func (s *Server) captureAgentSession(local *session.Local, id string, turn bool) {
	agent, ok := s.Catalog().Get(local.Info().AgentID)
	if !ok {
		return
	}
	re, r := sessionPattern(agent)
	if r == nil || r.IDFrom != "hook" || !session.ValidAgentSessionID(id, re) {
		s.log.Debug("agent session id dropped", "session", local.Info().ID)
		return
	}
	policy := r.IDPolicy
	if policy == "" {
		policy = session.PolicyLatest
	}
	local.ReportAgentSession(id, policy, turn)
}

// newAgentSessionID makes a fresh id for a recipe's StartArgs: a UUID, or
// "cdr-" and one for NewID "name".
func newAgentSessionID(kind string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	u := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	if kind == "name" {
		return "cdr-" + u
	}
	return u
}

// resumeID is the agent session id a resume of info would take up: its
// agent's, when the agent has a recipe and has had a turn, and the id has
// the recipe's shape; "" for a plain relaunch.
func resumeID(agent catalog.Agent, as *session.AgentSession) string {
	re, r := sessionPattern(agent)
	if r == nil || as == nil || !as.Resumable || !session.ValidAgentSessionID(as.ID, re) {
		return ""
	}
	return as.ID
}

// takeResume takes the agent conversation id for a session being made
// (Server.resuming), unless a session that runs holds it or one is being
// made for it; releaseResume lets it go.
func (s *Server) takeResume(id string) bool {
	s.resumingMu.Lock()
	defer s.resumingMu.Unlock()
	if _, taken := s.resuming[id]; taken || s.liveAgentSession(id) {
		return false
	}
	if s.resuming == nil {
		s.resuming = map[string]struct{}{}
	}
	s.resuming[id] = struct{}{}
	return true
}

func (s *Server) releaseResume(id string) {
	s.resumingMu.Lock()
	defer s.resumingMu.Unlock()
	delete(s.resuming, id)
}

// liveAgentSession reports whether a session that runs holds the agent
// session id: one conversation is resumed by one session at a time.
func (s *Server) liveAgentSession(id string) bool {
	for _, info := range s.registry.List() {
		if !info.Status.Ended() && info.AgentSession != nil && info.AgentSession.ID == id {
			return true
		}
	}
	return false
}

// resumeReply is the reply of the resume routes.
func resumeReply(info session.Info, resumed bool, agent catalog.Agent) map[string]any {
	out := map[string]any{"session": info, "resumed": resumed}
	if !resumed {
		if agent.Session.Empty() {
			out["notice"] = agent.Name + " has no session recipe: started anew"
		} else {
			out["notice"] = "nothing to resume yet (the agent had no turn): started anew"
		}
	}
	return out
}

// handleResumeSession resumes an ended server session: 201 {session,
// resumed, notice?}. A crew member's session is resumed as the member
// (Engine.ResumeMember).
func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok {
		writeError(w, http.StatusBadRequest, "hosted_session", "a hosted session is resumed from its machine")
		return
	}
	info := local.Info()
	if !info.Status.Ended() {
		writeError(w, http.StatusConflict, "still_running", "the session is still running")
		return
	}
	if info.Crew != nil {
		s.resumeMember(w, r, info.Crew.RunID, info.Crew.Member)
		return
	}
	launched := local.Launched()
	agent, ok := s.Catalog().Get(launched.AgentID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_agent", "the session's agent is no longer in the catalog")
		return
	}
	id := resumeID(agent, info.AgentSession)
	if id != "" && s.liveAgentSession(id) {
		writeError(w, http.StatusConflict, "already_resumed", "a running session holds that conversation")
		return
	}
	// The session's directory, unless it is gone: then an agent that resumes
	// only where it ran is refused, and anything else starts in the agent's
	// or the server's default directory.
	cwd := launched.Cwd
	if _, err := s.resolveCwd(cwd); err != nil {
		if id != "" && agent.Session.ResumeNeedsCwd {
			writeError(w, http.StatusBadRequest, "invalid_cwd", "the session's working directory is gone: "+err.Error())
			return
		}
		cwd = ""
	}
	yolo := launched.Yolo
	next, aerr := s.createLocalSession(createSessionRequest{AgentID: launched.AgentID, Name: launched.Name, Cwd: cwd,
		Args: launched.Args, Env: launched.Env, Yolo: &yolo, resume: id, resumedFrom: info.ID, renamed: launched.Renamed}, nil)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, resumeReply(next.Info(), id != "", agent))
}

// handleResumeRunMember resumes an ended member of a run, in its worktree:
// 201 {session, resumed, notice?}.
func (s *Server) handleResumeRunMember(w http.ResponseWriter, r *http.Request) {
	s.resumeMember(w, r, r.PathValue("run"), r.PathValue("name"))
}

func (s *Server) resumeMember(w http.ResponseWriter, r *http.Request, runID, name string) {
	run, ok := s.runs.Get(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return
	}
	var m *crew.MemberState
	for i := range run.Members {
		if run.Members[i].Name == name {
			m = &run.Members[i]
		}
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not_found", "no such member in the run")
		return
	}
	agent, ok := s.Catalog().Get(m.AgentID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_agent", "the member's agent is no longer in the catalog")
		return
	}
	id := resumeID(agent, m.AgentSession)
	if id != "" && s.liveAgentSession(id) {
		writeError(w, http.StatusConflict, "already_resumed", "a running session holds that conversation")
		return
	}
	ctx, cancel := runContext(r)
	defer cancel()
	sessionID, err := s.runs.ResumeMember(ctx, runID, name, id)
	if err != nil {
		s.runError(w, "resume member", runID, err)
		return
	}
	d, ok := s.registry.Get(sessionID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "launch_failed", "the resumed session is gone")
		return
	}
	writeJSON(w, http.StatusCreated, resumeReply(d.Info(), id != "", agent))
}
