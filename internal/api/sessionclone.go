package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// Clone (#434). A server session is started again under another name from
// what it was launched with (session.Launched): its agent, its working
// directory, its launch's arguments and its yolo choice, each of which the
// request may change. Never its environment: a crew member's GOAL, and
// everything else a run gives its members, stay the member's, and the
// clone is a loose session in the member's directory. A clone is a new
// launch through createLocalSession, so its agent conversation is a new
// one (a recipe that names it at launch names it afresh), and nothing of
// the source changes.

// cloneRequest is the body of POST /api/sessions/{id}/clone: the clone's
// name, and what it changes of the source's launch. An agent or a directory
// left out (or empty) is the source's, and so are arguments left out (an
// empty list launches with none) and a yolo choice left out.
type cloneRequest struct {
	Name    string    `json:"name"`
	AgentID string    `json:"agentId,omitempty"`
	Cwd     string    `json:"cwd,omitempty"`
	Args    *[]string `json:"args,omitempty"`
	Yolo    *bool     `json:"yolo,omitempty"`
}

// launchRecord is what GET /api/sessions/{id} gives an admin of a server
// session's launch (the `launch` field), what a clone takes by default:
// never its environment.
type launchRecord struct {
	AgentID string   `json:"agentId"`
	Cwd     string   `json:"cwd"`
	Args    []string `json:"args"`
	Yolo    bool     `json:"yolo"`
}

// launchOf is l's launchRecord.
func launchOf(l session.Launched) launchRecord {
	args := l.Args
	if args == nil {
		args = []string{}
	}
	return launchRecord{AgentID: l.AgentID, Cwd: l.Cwd, Args: args, Yolo: l.Yolo}
}

// cloneNames holds the names clones are being made under, from their check
// until the clone is registered (the listing holds the name from then) or
// its launch failed, as takeResume holds a conversation: two clones asking
// for one name at once make one session.
type cloneNames struct {
	mu   sync.Mutex
	held []string
}

// nameInUse reports whether name, ignoring case, is a listed session's
// (server or hosted, running or ended: a resume brings an ended one back
// under its name).
func nameInUse(list []session.Info, name string) bool {
	return slices.ContainsFunc(list, func(info session.Info) bool { return strings.EqualFold(info.Name, name) })
}

// take holds name unless a listed session has it or a clone is being made
// under it; release lets it go.
func (c *cloneNames) take(name string, list func() []session.Info) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.ContainsFunc(c.held, func(h string) bool { return strings.EqualFold(h, name) }) || nameInUse(list(), name) {
		return false
	}
	c.held = append(c.held, name)
	return true
}

func (c *cloneNames) release(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.held, name); i >= 0 {
		c.held = slices.Delete(c.held, i, i+1)
	}
}

// handleCloneSession starts a clone of a server session, running or ended:
// 201 with the clone's Info. 404 for an unknown session, 400 hosted_session
// for a hosted one (Conductor starts no session on a host), 400
// invalid_name for a name with nothing left once cleaned
// (session.CleanSessionName, as a rename's), 409 name_taken for one a
// listed session has, ignoring case, or a clone is being made under; and
// whatever POST /api/sessions answers the launch: 400 invalid_agent for an
// agent gone from the catalog, 400 invalid_cwd for a directory gone or
// outside the allowed roots (no default directory instead, unlike a
// resume), 400 args_not_allowed, 409 too_many_sessions.
func (s *Server) handleCloneSession(w http.ResponseWriter, r *http.Request) {
	var req cloneRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok {
		machine := d.Info().HostName
		if machine == "" {
			machine = "another machine"
		}
		writeError(w, http.StatusBadRequest, "hosted_session", "the session runs on "+machine+": Conductor cannot start a session there yet")
		return
	}
	name := session.CleanSessionName(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "the name is empty without its control characters and surrounding space")
		return
	}
	if !s.cloneNames.take(name, s.registry.List) {
		writeError(w, http.StatusConflict, "name_taken", "a session is named "+name+" already")
		return
	}
	defer s.cloneNames.release(name)
	launched := local.Launched()
	next := createSessionRequest{AgentID: launched.AgentID, Name: name, Cwd: launched.Cwd, Args: launched.Args, ctx: context.WithoutCancel(r.Context())}
	if req.AgentID != "" {
		next.AgentID = req.AgentID
	}
	if req.Cwd != "" {
		next.Cwd = req.Cwd
	}
	if req.Args != nil {
		next.Args = *req.Args
	}
	// The yolo choice is the clone's own from here, never the server's
	// default of the moment.
	yolo := launched.Yolo
	if req.Yolo != nil {
		yolo = *req.Yolo
	}
	next.Yolo = &yolo
	agent, known := s.Catalog().Get(next.AgentID)
	if known {
		if aerr := s.notTheAgent(agent); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
	}
	clone, aerr := s.createLocalSession(next, nil)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if known {
		s.noteIdentity(clone, agent)
	}
	writeJSON(w, http.StatusCreated, clone.Info())
}
