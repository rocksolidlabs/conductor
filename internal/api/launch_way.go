package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A launch as a chat (issue 1174): POST /api/sessions' `way`, "terminal"
// (the default, and what a client that says nothing means) or "protocol",
// the Launch agent dialog's Run as Chat. The session still starts in its
// terminal (I21): the launch notes a move to Chat (session.Local.NoteWay),
// and the move's own path makes it at the first moment design 9's
// conditions hold, the first reply's end among them. A launch as a chat is
// accepted only for an agent the move would take, judged by the move's own
// conditions (chatRefusal) before anything starts, and refused in the
// switch's words otherwise.

// launchWay is the way a launch's body asks for, or a refusal of the field:
// "" and "terminal" are the terminal, "protocol" a chat; anything else is
// not a way.
func launchWay(way string) (string, bool) {
	if way == "" {
		return session.WayTerminal, true
	}
	return way, proto.ValidWay(way)
}

// launchWayRefusal is the switch's sentence for why a person's launch of
// req as a chat cannot be one ("" when it can), before anything starts:
// the move's own conditions on the agent and the launch's arguments
// (chatRefusal) as the launch would keep them for its chat
// (keepChatLaunch). What the launch itself refuses (an unknown agent,
// arguments or a model it does not take) is left to it; a switchyard
// launches no session at all (403 switchyard, before this route).
func (s *Server) launchWayRefusal(ctx context.Context, req createSessionRequest) string {
	agent, ok := s.Catalog().Get(req.AgentID)
	if !ok {
		return ""
	}
	yolo := s.cfg.Yolo
	if req.Yolo != nil {
		yolo = *req.Yolo
	}
	modelArgs, aerr := launchModel(agent, req.Model, req.Args)
	if aerr != nil {
		return ""
	}
	l := chatLaunch{where: ext.WhereNative, agent: agent.Copy(), yolo: yolo && !agent.Yolo.Empty(), model: slices.Clone(modelArgs), args: slices.Clone(req.Args)}
	return s.chatRefusal(ctx, &l, true)
}

// noteChatAtLaunch notes local's move to Chat, its launch having asked for
// one. A move its own conditions refuse now (judged on the session as
// launched) leaves it in its terminal, and its activity says why in the
// switch's words.
func (s *Server) noteChatAtLaunch(ctx context.Context, local *session.Local) {
	starts, words := s.moveStarts(ctx, local, session.WayProtocol)
	if words == "" {
		_, err := local.NoteWay(session.WayProtocol, starts)
		var refusal *session.MoveRefusal
		if !errors.As(err, &refusal) {
			// Noted; or the session ended or moves already: nothing to say.
			return
		}
		words = refusal.Words
	}
	s.log.Info("a launch as a chat stays in its terminal", "session", local.Info().ID, "why", words)
	local.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: words})
}

// refuseLaunchWay answers a launch whose way is not one, or one the agent
// cannot run, and reports whether it did.
func (s *Server) refuseLaunchWay(w http.ResponseWriter, r *http.Request, req *createSessionRequest) bool {
	way, ok := launchWay(req.Way)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", `"way" is "terminal" or "protocol"`)
		return true
	}
	req.Way = way
	if way != session.WayProtocol {
		return false
	}
	if words := s.launchWayRefusal(r.Context(), *req); words != "" {
		writeError(w, http.StatusConflict, proto.ErrCodeMoveRefused, words)
		return true
	}
	return false
}
