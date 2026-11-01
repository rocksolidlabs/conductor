package api

import (
	"errors"
	"net/http"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

type attentionRequest struct {
	State   string           `json:"state"`
	Message string           `json:"message"`
	Kind    string           `json:"kind"`
	Options []session.Option `json:"options"`
	// AgentSession and Turn: the agent's own session id its report names,
	// and whether the report is of a turn (Resume).
	AgentSession string `json:"agentSession"`
	Turn         bool   `json:"turn"`
	// Ask, with needs_input, says the question is the agent's own (the MCP
	// tool ask, conductor notify --ask), which the session keeps through
	// its turn's done (session.Local.Ask); with another state it changes
	// nothing.
	Ask bool `json:"ask"`
	// Plan, with needs_input, is the path of the plan the question asks
	// approval for (Claude Code's ExitPlanMode hook, #289): the session
	// keeps it as one of its plans when it is a file in the agent's plans
	// directory, and the question carries it (Attention.Plan). At most
	// proto.MaxPlanPath bytes. A server session's alone.
	Plan string `json:"plan"`
}

// eventRequest is the body of POST /api/sessions/{id}/events. Type is one of
// the six event types or an attention word; Kind and Options go with the
// attention words, URL, To and Tool with the events that use them.
type eventRequest struct {
	Type    string           `json:"type"`
	Message string           `json:"message"`
	URL     string           `json:"url"`
	To      string           `json:"to"`
	Tool    string           `json:"tool"`
	Kind    string           `json:"kind"`
	Options []session.Option `json:"options"`
	// A `file` event: what the agent did to the file (read, edit, write, delete) and its path.
	Op   string `json:"op"`
	Path string `json:"path"`
	// Ask goes with the attention word needs_input, as the attention
	// route's (attentionRequest.Ask).
	Ask bool `json:"ask"`
}

// eventActor is the display name on an entry reported through the events
// route: the agent said it, whoever holds the token.
const eventActor = "agent"

// agentTokenOK reports whether the presented token is the session's agent token.
func agentTokenOK(d session.Driver, tok string) bool {
	switch drv := d.(type) {
	case *session.Local:
		return drv.AgentTokenOK(tok)
	case *signal.HostedSession:
		return drv.AgentTokenOK(tok)
	}
	return false
}

// reportTarget finds the session a report is about. An unknown session is a
// 404 that counts against the caller's rate limit, so ids cannot be guessed
// at speed. It writes the response and returns false when there is none.
func (s *Server) reportTarget(w http.ResponseWriter, r *http.Request) (session.Driver, bool) {
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return nil, false
		}
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return nil, false
	}
	return d, true
}

// attentionPrincipal decides who is reporting on d and whether they may: an
// admin (source "admin") or the holder of d's agent token (source "api"),
// presented in the Authorization header. A switchyard's workbench token has
// no role on the sessions published there, and reports nothing for them.
// Anyone else is refused with a 401 that counts against their rate limit;
// the response is written and ok is false. Both the attention route and the
// events route use it. The agent token is checked first: it is a token of
// this server's own, so it never reaches an identity provider
// (tokenPrincipal), and an agent's hooks, which report often, never wait on
// one.
func (s *Server) attentionPrincipal(w http.ResponseWriter, r *http.Request, d session.Driver) (source string, ok bool) {
	if agentTokenOK(d, presentedToken(r, false)) {
		return session.SourceAPI, true
	}
	if p := s.authenticate(r, false); p.admin && !p.yard {
		return session.SourceAdmin, true
	}
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return "", false
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "agent or workbench token required")
	return "", false
}

// attentionState reads the word an agent reports: needs_input, working and
// done are states, and clear is no state at all. ok is false for anything else.
func attentionState(word string) (state session.AttentionState, ok bool) {
	if word == "clear" {
		return session.AttentionNone, true
	}
	state = session.AttentionState(word)
	return state, word != "" && state.Valid()
}

// eventType reports whether t is one of the six types of event an agent may
// report. The entries a session records itself (join, leave, input, link,
// status, and the attention entry of a change it applied) are not among them:
// they skip the session's rate bucket, which is only safe while an agent
// cannot write them.
func eventType(t string) bool {
	switch t {
	case session.ActivityProgress, session.ActivityArtifact, session.ActivityHandoff,
		session.ActivityToolUse, session.ActivityToolDenied, session.ActivityError, session.ActivityFile:
		return true
	}
	return false
}

// reportAttention checks an attention report and applies it to d: to a
// server session directly, to a hosted one on the server and then on its host.
// Either way the report first spends a token of the session's bucket, a
// server session's own or the one the server keeps for a hosted session's
// host, and with none left it is refused with 429 rate_limited and nothing
// changes. The attention route and the attention types of the events route
// share it, so they cannot drift apart. It writes the response and returns
// false when the report is refused. ask, with needs_input, reports the
// agent's own question (session.Local.Ask).
func reportAttention(w http.ResponseWriter, d session.Driver, source string, state session.AttentionState, message, kind string, options []session.Option, ask bool, plan string) bool {
	if len(message) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message too long")
		return false
	}
	if !session.ValidKind(kind) {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be permission, prompt, done or empty")
		return false
	}
	if len(options) > 32 {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many options")
		return false
	}
	if d.Info().Status.Ended() {
		writeError(w, http.StatusConflict, "session_ended", "the session has ended")
		return false
	}
	ask = ask && state == session.AttentionNeedsInput
	// The plan the question is about (#289) goes with the change itself:
	// a server session learns it as one of its plans as the question is
	// set, a hosted one sends it to its host with the question. A report
	// refused above, or by the bucket, names nothing.
	if state != session.AttentionNeedsInput || ask {
		plan = ""
	}
	switch drv := d.(type) {
	case *session.Local:
		var err error
		switch {
		case ask:
			err = drv.TryAsk(message, source, kind, options)
		case plan != "":
			err = drv.TrySetAttentionPlan(state, message, source, kind, options, plan)
		default:
			err = drv.TrySetAttentionFull(state, message, source, kind, options)
		}
		if err != nil {
			if errors.Is(err, session.ErrRateLimited) {
				writeSessionLimited(w)
				return false
			}
			writeError(w, http.StatusInternalServerError, "internal", "the report could not be applied")
			return false
		}
	case *signal.HostedSession:
		// The only refusal is an empty bucket, host or no host.
		var err error
		if ask {
			err = drv.Ask(message, source, kind, options)
		} else {
			err = drv.SetAttentionPlan(state, message, source, kind, options, true, plan)
		}
		if err != nil {
			writeSessionLimited(w)
			return false
		}
	}
	return true
}

// writeSessionLimited answers a report the session's bucket has no token for
// (session.ErrRateLimited): a server session's own, or the one that protects a
// hosted session's host. (The per-client limiter answers with the same code,
// for repeated failures.)
func writeSessionLimited(w http.ResponseWriter) {
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many reports for this session")
}

// handleAttention lets the agent running in a session (or an admin) report
// whether it is waiting for a human.
func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	d, ok := s.reportTarget(w, r)
	if !ok {
		return
	}
	source, ok := s.attentionPrincipal(w, r, d)
	if !ok {
		return
	}
	var req attentionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state, ok := attentionState(req.State)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_state", "state must be needs_input, working, done or clear")
		return
	}
	if len(req.AgentSession) > session.MaxAgentSessionID {
		writeError(w, http.StatusBadRequest, "invalid_request", "agentSession is longer than 128 bytes")
		return
	}
	if len(req.Plan) > proto.MaxPlanPath {
		writeError(w, http.StatusBadRequest, "invalid_request", "plan is longer than 1024 bytes")
		return
	}
	if !reportAttention(w, d, source, state, req.Message, req.Kind, req.Options, req.Ask, req.Plan) {
		return
	}
	if local, ok := d.(*session.Local); ok && req.AgentSession != "" {
		s.captureAgentSession(local, req.AgentSession, req.Turn)
	}
	writeJSON(w, http.StatusOK, map[string]any{"attention": d.Info().Attention})
}

// handleEvent lets the agent running in a session (or an admin) report what
// it did: one of the six event types, or an attention word, which is applied
// exactly as the attention route applies it.
//
// A server session records an event in its own log, which streams it to the
// admins, unless its rate bucket is empty: that is a 429. A hosted session
// has no log on the server, so the event is sent to its host, which records
// it and reports it back; the server cannot know whether the host's bucket
// took it, so the answer is 202 once the event is on its way, 429 when the
// server-side bucket for that host is empty, or 409 when the host is not
// connected. An attention word spends a token of the same bucket on either
// kind of session and is refused whole, with a 429, when there is none.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	d, ok := s.reportTarget(w, r)
	if !ok {
		return
	}
	source, ok := s.attentionPrincipal(w, r, d)
	if !ok {
		return
	}
	var req eventRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if state, isAttention := attentionState(req.Type); isAttention {
		if !reportAttention(w, d, source, state, req.Message, req.Kind, req.Options, req.Ask, "") {
			return
		}
	} else if eventType(req.Type) {
		if !reportEvent(w, d, req) {
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "invalid_type", "type must be progress, artifact, handoff, tool_use, tool_denied, error, file, needs_input, working, done or clear")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// reportEvent records one of the six event types in d. It writes the response
// and returns false when the event is refused.
func reportEvent(w http.ResponseWriter, d session.Driver, req eventRequest) bool {
	if d.Info().Status.Ended() {
		writeError(w, http.StatusConflict, "session_ended", "the session has ended")
		return false
	}
	entry := session.CleanEntry(session.ActivityEntry{Type: req.Type, Message: req.Message, URL: req.URL, To: req.To, Tool: req.Tool, Op: req.Op, Path: req.Path, ByName: eventActor})
	if req.Type == session.ActivityFile && (entry.Path == "" || entry.Op == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "a file event names its op (read, edit, write or delete) and its path")
		return false
	}
	switch drv := d.(type) {
	case *session.Local:
		if !drv.Record(entry) {
			writeSessionLimited(w)
			return false
		}
	case *signal.HostedSession:
		if err := drv.ForwardActivity(entry); err != nil {
			if errors.Is(err, session.ErrRateLimited) {
				writeSessionLimited(w)
				return false
			}
			// No host is connected (signal.ErrHostGone), or its queue is full,
			// which closes its connection (signal.ErrSlowHost): either way,
			// the host is not connected.
			writeError(w, http.StatusConflict, "host_disconnected", "the session's host is not connected")
			return false
		}
	}
	return true
}
