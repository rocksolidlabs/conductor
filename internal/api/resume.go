package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/capacity"
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
	policy, ok := s.agentSessionPolicy(local.Info().AgentID, id)
	if !ok {
		s.log.Debug("agent session id dropped", "session", local.Info().ID)
		return
	}
	local.ReportAgentSession(id, policy, turn)
}

// agentSessionPolicy is the policy by which an agent session id reported
// by a hook of agentID's is taken: the catalog's recipe for the agent's
// (latest when it names none), ok false when the catalog has no such
// agent, its recipe takes no id from a hook, or id is not of its pattern.
func (s *Server) agentSessionPolicy(agentID, id string) (string, bool) {
	agent, ok := s.Catalog().Get(agentID)
	if !ok {
		return "", false
	}
	re, r := sessionPattern(agent)
	if r == nil || r.IDFrom != "hook" || !session.ValidAgentSessionID(id, re) {
		return "", false
	}
	if r.IDPolicy == "" {
		return session.PolicyLatest, true
	}
	return r.IDPolicy, true
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

// takeConversation takes the agent conversation conv for a resume of the
// session from being launched now, by the one rule a resume launches on,
// read at this moment under resumingMu (which every resume's launch takes
// and holds until its session is registered): no session is being made
// for conv, no running session holds it (liveAgentSession), and no local
// session of the conversation has a process that is not done
// (conversationHolder). Whatever a route read before (its judgement, a
// record's reservation) decides nothing here. releaseResume lets it go.
func (s *Server) takeConversation(conv, from string) *apiError {
	s.resumingMu.Lock()
	defer s.resumingMu.Unlock()
	if _, taken := s.resuming[conv]; taken || s.liveAgentSession(conv) {
		return newAPIError(http.StatusConflict, "already_resumed", "a running session holds that conversation, or one is being made for it")
	}
	if aerr := s.conversationHolder(conv, from); aerr != nil {
		return aerr
	}
	if s.resuming == nil {
		s.resuming = map[string]struct{}{}
	}
	s.resuming[conv] = struct{}{}
	return nil
}

// maxChainWalk bounds the steps of a walk up a session's resumedFrom
// chain (chainAbove).
const maxChainWalk = 64

// conversationHolder is the refusal a resume of the conversation conv,
// from the session from, meets when a local session of that conversation
// has a process that is not done (Registry.Holders: listed, running or
// ended by a stop that failed, or removed while its process ran): one
// that holds conv, from's own, any session from resumed from (at any
// remove, up its resumedFrom chain) and any session resumed from it. A
// running one is already_resumed (still_running for from's own); one
// ended whose process is not, still_running: its stop failed. nil when
// none.
func (s *Server) conversationHolder(conv, from string) *apiError {
	holders := map[string]session.Info{}
	for _, d := range s.registry.Holders() {
		info := d.Info()
		holders[info.ID] = info
	}
	if len(holders) == 0 {
		return nil
	}
	var above map[string]bool
	if from != "" {
		above = s.chainAbove(from, holders)
	}
	for id, info := range holders {
		held := (conv != "" && info.AgentSession != nil && info.AgentSession.ID == conv) ||
			(from != "" && (id == from || above[id] || s.chainAbove(id, holders)[from]))
		switch {
		case !held:
		case info.Status.Ended() && id == from:
			return newAPIError(http.StatusConflict, "still_running", "the session's process is still running: its stop did not end it; stop it again")
		case info.Status.Ended():
			return newAPIError(http.StatusConflict, "still_running", fmt.Sprintf("the process of %s, a session of this conversation, is still running: its stop did not end it; stop it again", id))
		case id == from:
			return newAPIError(http.StatusConflict, "still_running", "the session is still running")
		default:
			return newAPIError(http.StatusConflict, "already_resumed", "a running session holds that conversation")
		}
	}
	return nil
}

// chainAbove is the sessions id resumed from, at any remove: each one's
// resumedFrom, read from holders, the registry, else its record.
func (s *Server) chainAbove(id string, holders map[string]session.Info) map[string]bool {
	above := map[string]bool{}
	for range maxChainWalk {
		next := ""
		if info, ok := holders[id]; ok {
			next = info.ResumedFrom
		} else if d, ok := s.registry.Get(id); ok {
			next = d.Info().ResumedFrom
		} else if s.sessionRecords != nil {
			if r, _, err := s.sessionRecords.Get(id); err == nil {
				next = r.ResumedFrom
			}
		}
		if next == "" || above[next] {
			break
		}
		above[next] = true
		id = next
	}
	return above
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

// resumePlan is what a resume of a server session takes up: its agent,
// the agent's own conversation ("" for a plain relaunch) and the folder it
// starts in ("" for the agent's or the server's default, its own being
// gone).
type resumePlan struct {
	agent catalog.Agent
	id    string
	cwd   string
	// cwdErr is why the folder was refused (resolveCwd's error), with the
	// plan's refusal when the agent resumes only where it ran: the
	// history's offer tells a folder gone from one it could not read.
	cwdErr error
}

// resumeCheck applies the resume routes' rules to a session of the agent
// agentID whose agent's conversation is as and whose folder was cwd: the
// agent still in the catalog (400 invalid_agent); its conversation, when
// it has one to resume, held by no running session (409
// already_resumed); its folder there through resolveCwd, or the agent
// not needing it (400 invalid_cwd), else started in the default folder.
// handleResumeSession answers with it, and the history's availability
// offers Resume by it (history_availability.go), so the two never differ.
func (s *Server) resumeCheck(agentID string, as *session.AgentSession, cwd string) (resumePlan, *apiError) {
	agent, ok := s.Catalog().Get(agentID)
	if !ok {
		return resumePlan{}, newAPIError(http.StatusBadRequest, "invalid_agent", "the session's agent is no longer in the catalog")
	}
	id := resumeID(agent, as)
	if id != "" && s.liveAgentSession(id) {
		return resumePlan{}, newAPIError(http.StatusConflict, "already_resumed", "a running session holds that conversation")
	}
	// The session's directory, unless it is gone: then an agent that resumes
	// only where it ran is refused, and anything else starts in the agent's
	// or the server's default directory.
	if _, err := s.resolveCwd(cwd); err != nil {
		if id != "" && agent.Session.ResumeNeedsCwd {
			return resumePlan{cwdErr: err}, newAPIError(http.StatusBadRequest, "invalid_cwd", "the session's working directory is gone: "+err.Error())
		}
		cwd = ""
	}
	return resumePlan{agent: agent, id: id, cwd: cwd}, nil
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
	plan, aerr := s.resumeCheck(launched.AgentID, info.AgentSession, launched.Cwd)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	agent, id := plan.agent, plan.id
	account, login := boundOf(launched)
	next, aerr := s.resumeLaunch(context.WithoutCancel(r.Context()), resumeSpec{agentID: launched.AgentID, name: launched.Name, cwd: plan.cwd,
		args: launched.Args, env: launched.Env, yolo: launched.Yolo, renamed: launched.Renamed, resume: id, from: info.ID,
		account: account, login: login, model: launched.Model})
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, resumeReply(next.Info(), id != "", agent))
}

// resumeSpec is what a resume launches: the agent, the name, the folder
// (resumeCheck's), the arguments, the variables, the yolo choice and
// whether the name is a person's, the Runtime (where; "" is the native
// one), the agent's conversation to take up ("" for a plain relaunch),
// the session it resumes (from), the new session's id (id; "" makes
// one, a record's resume allocates it with its receipt), a record's
// last read at the launch (atLaunch, createSessionRequest's), and the
// login and the model it was launched with (a record from before them:
// account, the tool's own login, as it resumed then).
type resumeSpec struct {
	agentID, name, cwd string
	args               []string
	env                map[string]string
	yolo, renamed      bool
	where              ext.Where
	resume, from, id   string
	atLaunch           func() *apiError
	account, model     string
	login              loginWant
}

// boundOf is what a resume of a session launched with l asks of its
// login: the binding l holds, which it must get again; for a session
// made with no binding (none is launched so; a test's process double),
// the tool's own login.
func boundOf(l session.Launched) (string, loginWant) {
	if l.Account.Source == "" {
		return capacity.DefaultID, loginWant{}
	}
	b := l.Account
	return "", loginWant{bound: &b}
}

// resumeLaunch is the launch both resume routes make
// (handleResumeSession for a listed session, handleResumeHistorySession
// for a record, a listed session's among them): a session made as every
// server session is (createLocalSession: the folder through openCwd, the
// process through the Runtime at where), its agent's conversation taken
// for it by the one rule a resume launches on (takeConversation) and its
// resumedFrom set.
func (s *Server) resumeLaunch(ctx context.Context, sp resumeSpec) (*session.Local, *apiError) {
	yolo := sp.yolo
	return s.createLocalSession(createSessionRequest{AgentID: sp.agentID, Name: sp.name, Cwd: sp.cwd, Args: sp.args, Env: sp.env, Yolo: &yolo,
		resume: sp.resume, resumedFrom: sp.from, renamed: sp.renamed, where: sp.where, id: sp.id, atLaunch: sp.atLaunch, ctx: ctx,
		Account: sp.account, Model: sp.model, login: sp.login}, nil)
}

// handleResumeRunMember resumes an ended member of a run, in its worktree:
// 201 {session, resumed, notice?}.
func (s *Server) handleResumeRunMember(w http.ResponseWriter, r *http.Request) {
	s.resumeMember(w, r, r.PathValue("run"), r.PathValue("name"))
}

// resumeMember resumes the member name of the run runID, for the run's
// route and for its session's own (POST /api/sessions/{id}/resume); a
// Brakeman turn's run is never resumed (409 brakeman_run) on either.
func (s *Server) resumeMember(w http.ResponseWriter, r *http.Request, runID, name string) {
	if s.brakemanRun(w, runID) {
		return
	}
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
