package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// A chat's permission request (docs/plans/chat-or-terminal.md, design 7):
// Claude Code calls Conductor's MCP tool permission (its
// --permission-prompt-tool), and conductor mcp posts the call here with the
// session's agent token, holding the request open until the session
// answers it. The answer is the documented one: {"behavior":"allow"}, or
// {"behavior":"deny","message":...} with one of the plan's sentences (or the
// operator's own words with a deny). Nothing of the call's input is logged.

// permissionAnswerWait bounds how long an operator's permission_answer
// waits for its verdict's delivery (the held POST's reply written, or the
// driver's Reply): past it the answer is told question_answered.
const permissionAnswerWait = 15 * time.Second

// MaxPermissionBody bounds a permission request's body, every byte of it
// (its call, tool and input, as conductor mcp's permission tool encodes
// them): past it, 413 and no question asked. The tool bounds what it posts
// by the same constant, so no call it posts meets the 413.
const MaxPermissionBody = 256 << 10

// permissionRequest is the body: the agent's id of the call ("" when its
// request names none), the tool's name and the call's whole input, as the
// agent sent it.
type permissionRequest struct {
	Call  string          `json:"call"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// permissionReply is the answer, as the prompt tool returns it to Claude
// Code.
type permissionReply struct {
	Behavior string  `json:"behavior"`
	Message  *string `json:"message,omitempty"`
}

// permissionReplyOf is v as the prompt tool's answer: allow, or a deny with its
// words; a withdrawal is the deny Claude Code drops for a call it decided.
func permissionReplyOf(v session.Verdict) permissionReply {
	switch {
	case v.Allow:
		return permissionReply{Behavior: "allow"}
	case v.Cancel:
		m := session.WithdrawnElsewhereWords
		return permissionReply{Behavior: "deny", Message: &m}
	}
	m := v.Message
	return permissionReply{Behavior: "deny", Message: &m}
}

// handlePermission takes a permission request of the session's own agent
// (its agent token, as the attention route takes it, a comparison's lane's
// too: doorOwn) and holds it open until the session answers it
// (session.Local.RequestPermission). The end of the request before an
// answer is read as its withdrawal, recorded before any later answer can be
// applied. A session that is not a chat answers the deny of a call it is
// not making.
func (s *Server) handlePermission(w http.ResponseWriter, r *http.Request) {
	d, ok := s.reportTarget(w, r)
	if !ok {
		return
	}
	switch s.agentTokenOK(r, d) {
	case agentTokenServed:
	case agentTokenLaneClosed:
		writeLaneClosed(w)
		return
	default:
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "the session's agent token required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPermissionBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "a permission request's body is at most 256 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "the body could not be read")
		return
	}
	var req permissionRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() || req.Tool == "" || len(req.Input) == 0 {
		// The input's words are never in an error's.
		writeError(w, http.StatusBadRequest, "invalid_request", `the body is {"call", "tool", "input"}`)
		return
	}
	replied := false
	reply := func(v session.Verdict) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		replied = true
		writeJSON(w, http.StatusOK, permissionReplyOf(v))
		if err := http.NewResponseController(w).Flush(); err != nil {
			return err
		}
		return r.Context().Err()
	}
	local, ok := d.(*session.Local)
	if !ok {
		_ = reply(session.Verdict{Message: session.NotACallWords})
		return
	}
	local.RequestPermission(r.Context(), session.PermissionAsk{Call: req.Call, Tool: req.Tool, Input: req.Input}, reply)
	if !replied {
		// The request's context ended before a verdict reached it: its
		// client went, or the server stops (its base context ends first).
		// Its entry says it was lost; the deny goes to a client still
		// there, never an empty answer.
		writeJSON(w, http.StatusOK, permissionReplyOf(session.Verdict{Message: session.EndedBeforeAnswerWords}))
	}
}
