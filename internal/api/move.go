package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The move between ways (docs/plans/chat-or-terminal.md, design 9): POST
// /api/sessions/{id}/way {"to": "terminal" | "protocol"} moves a person's
// own server session between its terminal and its chat, once a turn has
// ended (session.Local.SwapWay). The route is the operator's alone
// (requireAdmin, as Stop's), refused on a switchyard.

// maxWayBody bounds the move's body: {"to":"protocol"} with room to spare.
const maxWayBody = 64

// wayRequest is the move's body.
type wayRequest struct {
	To string `json:"to"`
}

// handleSessionWay is the move's route: 200 with where the session stands
// (session.MoveState: moving, or the move noted with why it waits), 409
// move_refused with the plan's sentence for a move that cannot be made, 409
// moving while one is under way.
func (s *Server) handleSessionWay(w http.ResponseWriter, r *http.Request) {
	var req wayRequest
	if err := decodeJSONLimit(w, r, &req, maxWayBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the move's body is past its bound")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !proto.ValidWay(req.To) {
		writeError(w, http.StatusBadRequest, "invalid_request", `"to" is "terminal" or "protocol"`)
		return
	}
	if s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusConflict, proto.ErrCodeMoveRefused, session.HostedStaysWords)
		return
	}
	d, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok {
		writeError(w, http.StatusConflict, proto.ErrCodeMoveRefused, session.HostedStaysWords)
		return
	}
	starts, words := s.moveStarts(r.Context(), local, req.To)
	if words != "" && req.To != local.Way() {
		writeError(w, http.StatusConflict, proto.ErrCodeMoveRefused, words)
		return
	}
	st, err := local.SwapWay(req.To, starts)
	var refusal *session.MoveRefusal
	switch {
	case errors.As(err, &refusal):
		writeError(w, http.StatusConflict, proto.ErrCodeMoveRefused, refusal.Words)
	case errors.Is(err, session.ErrMoving):
		writeError(w, http.StatusConflict, proto.ErrCodeMoving, session.MovingWords)
	case errors.Is(err, session.ErrSessionEnded):
		writeError(w, http.StatusConflict, proto.ErrCodeSessionEnded, "the session has ended")
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeJSON(w, http.StatusOK, st)
	}
}

// moveStarts is what a move of local starts each way with, from its
// launch (chatLaunch), or the plan's sentence for why it cannot move to
// to: a crew member's and a hosted session's are the session's own words
// (SwapWay); here an agent the server cannot move (no protocol it speaks,
// a driver whose Moves is false, a Runtime other than the native one, no
// recipe to resume its terminal) and, to Chat, arguments its chat is not
// started with (an added folder, a flag a chat sets itself) and a version
// its driver did not qualify.
func (s *Server) moveStarts(ctx context.Context, local *session.Local, to string) (session.MoveStarts, string) {
	info := local.Info()
	if info.Crew != nil {
		return session.MoveStarts{}, session.CrewStaysWords
	}
	v, ok := s.chatLaunches.Load(info.ID)
	if !ok {
		return session.MoveStarts{}, session.CannotMoveWords
	}
	l := v.(*chatLaunch)
	if l.where != ext.WhereNative || !s.moves(l.agent.Protocol.Name) || l.agent.Session.Empty() || len(l.agent.Session.ResumeArgs) == 0 {
		return session.MoveStarts{}, session.CannotMoveWords
	}
	chat, err := s.chatStart(local)
	if err != nil {
		return session.MoveStarts{}, session.CannotMoveWords
	}
	if to == session.WayProtocol {
		// What the child's argv would refuse is told now, with the
		// terminal still running: an added folder, a flag a chat sets.
		if err := s.chatArgsCheck(l); err != nil {
			return session.MoveStarts{}, chatArgsWords(err)
		}
		version := ""
		if id, ok := s.cachedIdentity(l.agent); ok && id.Identified {
			version = id.Version
		} else if id := s.identityNow(ctx)(l.agent); id != nil && id.Identified {
			version = id.Version
		}
		if !agentproto.Qualified(l.agent.Protocol.Name, version) {
			return session.MoveStarts{}, agentproto.StartWords(agentproto.NotQualifiedFor(l.agent.Protocol.Name))
		}
	}
	id := info.ID
	m := session.MoveStarts{
		Protocol: chat,
		Terminal: s.terminalStart(l),
		Gone:     session.GroupGone,
		Words:    moveWordsFor(l.agent.Protocol.Name),
	}
	if seam := s.moveSeam; seam != nil {
		m.AfterOldExit = func() { seam(id) }
	}
	return m, ""
}

// chatArgsWords is the plan's sentence for arguments a chat is not
// started with (design 13).
func chatArgsWords(err error) string {
	var own *agentproto.OwnFlagError
	var codexArg *agentproto.CodexArgError
	switch {
	case errors.Is(err, agentproto.ErrAddedFolder):
		return agentproto.AddedFolderWords
	case errors.As(err, &own):
		return agentproto.OwnFlagWords(own.Flag)
	case errors.As(err, &codexArg):
		return agentproto.CodexArgWords(codexArg.Flag)
	}
	return session.CannotMoveWords
}

// moves is the driver's Moves for the protocol name.
func (s *Server) moves(name string) bool { return agentproto.Moves(name) }

// terminalStart starts the session's terminal again on a conversation, as
// a resume starts it: the agent's command, its recipe's resume arguments on
// the conversation, and the rest of the launch's arguments, on the same
// Runtime, in the same folder, with the same environment (the same login
// binding and agent token), at the session's size (the launch's when it
// has none).
func (s *Server) terminalStart(l *chatLaunch) func(context.Context, string, uint16, uint16) (session.Process, error) {
	return func(ctx context.Context, conversation string, cols, rows uint16) (session.Process, error) {
		if conversation == "" {
			return nil, errNoConversation
		}
		rt, ok := s.runtimeAt(l.where)
		if !ok {
			return nil, errNoPipes
		}
		argv := slices.Clone(l.agent.Command)
		argv = append(argv, catalog.Expand(l.agent.Session.ResumeArgs, conversation)...)
		argv = append(argv, l.termTail...)
		dirFile, err := l.folder.Dir()
		if err != nil {
			return nil, err
		}
		defer dirFile.Close()
		term, err := rt.StartTerminal(ctx, ext.TerminalSpec{Argv: argv, Dir: l.folder.Path(), DirFile: dirFile, Env: slices.Clone(l.termEnv), Cols: cmp.Or(cols, l.cols), Rows: cmp.Or(rows, l.rows), Ref: l.ref})
		if err != nil {
			return nil, err
		}
		return session.RuntimeProcess(term), nil
	}
}

// moveWordsFor is a failed start's sentence for an agent of the protocol
// name: its driver's own for a chat that did not start (design 13), the
// terminal's for one that did not.
func moveWordsFor(name string) func(error) string {
	return func(err error) string {
		if w := agentproto.StartWords(err); w != "" {
			return w
		}
		if errors.Is(err, errNoConversation) || errors.Is(err, errNoPipes) {
			return agentproto.StartWords(agentproto.NotStartedFor(name))
		}
		return ""
	}
}

// endChatOnOlderSwitchyard ends the publication of a session that has
// become a chat on a switchyard whose registered did not say transcript:
// its viewers there would be refused and its frames dropped (I14). It ends
// it as a publication the switchyard stopped is ended (watchPublication):
// the session stays owed one, so a Share publishes it again (refused in
// the same words while it is still a chat, made once it is back in its
// terminal). The session's line says why.
func (s *Server) endChatOnOlderSwitchyard(info session.Info) {
	if info.Way != session.WayProtocol {
		return
	}
	id := info.ID
	pub := s.publishedOf(id)
	t, ok := pub.(interface{ TakesTranscript() bool })
	if pub == nil || !ok || t.TakesTranscript() {
		return
	}
	s.pubMu.Lock()
	ours := s.published[id] == pub
	if ours {
		delete(s.published, id)
		s.forgetRemoteLocked(id, "", "", true)
		if s.pubErr == nil {
			s.pubErr = map[string]string{}
		}
		s.pubErr[id] = proto.OlderSwitchyardWords
		if s.pubStopped == nil {
			s.pubStopped = map[string]bool{}
		}
		s.pubStopped[id] = true
	}
	s.pubMu.Unlock()
	if !ours {
		return
	}
	pub.Stop()
	if l, ok := s.lookupLocal(id); ok {
		l.Record(session.ActivityEntry{Type: session.ActivityStatus, Message: proto.OlderSwitchyardWords})
	}
	if runID, _, ok := s.runs.MemberOf(id); ok {
		s.scheduleRunSync(runID)
	}
}
