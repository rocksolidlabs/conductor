package api

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A crew member's chat (docs/plans/chat-or-terminal.md, design 14): the
// engine asks the server whether a member can run as a chat at its launch
// (ChatCheck), and, with the member's terminal at its prompt and still
// running, for what its move starts the chat with (MemberMove), whose
// checks refuse it first. The words of a refusal are the run's log's after
// "in its terminal: " (crew.TerminalReason).

// The run's log's words for a member kept in its terminal (design 14's
// table), after "in its terminal: ".
const (
	memberNotQualifiedWords = "this version of Claude Code can only run in its terminal."
	memberAddedFolderWords  = "it was launched with an added folder."
	memberNoPipesWords      = "the run's Runtime has no pipes."
	memberNoChatWords       = "its agent has no chat."
)

// memberOwnFlagWords is the run's log's words for a member whose
// arguments hold flag, a chat's own.
func memberOwnFlagWords(flag string) string {
	return "its arguments hold " + flag + ", which a chat sets itself."
}

// memberReason is err, a member's chat refused, as the run's log says it.
func memberReason(err error) error {
	var own *agentproto.OwnFlagError
	switch {
	case errors.Is(err, agentproto.ErrNotQualified):
		return &crew.TerminalReason{Words: memberNotQualifiedWords}
	case errors.Is(err, agentproto.ErrAddedFolder):
		return &crew.TerminalReason{Words: memberAddedFolderWords}
	case errors.As(err, &own):
		return &crew.TerminalReason{Words: memberOwnFlagWords(own.Flag)}
	case errors.Is(err, errNoPipes):
		return &crew.TerminalReason{Words: memberNoPipesWords}
	}
	return &crew.TerminalReason{Words: memberNoChatWords}
}

// memberProbeConversation stands for a conversation in a check of a
// member's arguments: only its shape is read.
const memberProbeConversation = "00000000-0000-4000-8000-000000000000"

// ChatCheck says whether a crew member of agentID launched with args can
// run as a chat (crew.ChatChecker), asked as a Chat run's attempt is
// launched: its catalog entry declares Claude Code's protocol and a
// session recipe that starts a conversation on an id, its installed
// version, probed now, is one its driver runs, and its arguments name no
// added folder and no flag a chat sets itself. A refusal is a
// crew.TerminalReason in the run's log's words. The run's Runtime is the
// engine's to look at.
func (s *Server) ChatCheck(ctx context.Context, agentID string, args []string) error {
	a, ok := s.Catalog().Get(agentID)
	if !ok || a.Protocol.Off() || agentproto.Check(a.Protocol) != nil || a.Protocol.Name != agentproto.ClaudeStreamJSON || a.Session.Empty() || len(a.Session.StartArgs) == 0 {
		return &crew.TerminalReason{Words: memberNoChatWords}
	}
	if _, err := agentproto.ClaudeArgs(agentproto.ClaudeLaunch{Command: a.Command, Protocol: a.Protocol.Args, Conversation: memberProbeConversation,
		Fresh: catalog.Expand(a.Session.StartArgs, memberProbeConversation), Member: true, LaunchArgs: args,
		HooksDir: agents.HooksDir(s.cfg.DataDir), NoMCP: !s.cfg.MCP(), PermissionHost: true}); err != nil {
		return memberReason(err)
	}
	version := ""
	if id := s.identityNow(ctx)(a); id != nil && id.Identified {
		version = id.Version
	}
	if !agentproto.Qualified(a.Protocol.Name, version) {
		return memberReason(agentproto.ErrNotQualified)
	}
	return nil
}

// MemberMove is what the engine's move of local, a crew member launched to
// run as a chat, starts with (crew.ChatChecker), asked with its terminal
// still running: refused first (a crew.TerminalReason) when the launch's
// arguments hold an added folder or a flag a chat sets itself, when the
// version its launch's check qualified is not kept, or when its Runtime has
// no pipes. fresh, for a launch that resumed no conversation: the chat's
// child starts a new conversation by the session recipe's start, and the
// way back is a terminal started afresh by it (session.MoveFresh); else the
// chat resumes the launch's conversation and the way back resumes it in
// the terminal (session.MoveResumed). A member that is not yolo is given
// no permission mode (agentproto.ClaudeLaunch.Member).
func (s *Server) MemberMove(_ context.Context, local *session.Local, fresh bool) (session.MoveStarts, error) {
	v, ok := s.chatLaunches.Load(local.Info().ID)
	if !ok {
		return session.MoveStarts{}, memberReason(errNoChat)
	}
	l := v.(*chatLaunch)
	if l.agent.Protocol.Name != agentproto.ClaudeStreamJSON || l.agent.Session.Empty() || fresh && len(l.agent.Session.StartArgs) == 0 {
		return session.MoveStarts{}, memberReason(errNoChat)
	}
	if _, err := memberChatArgv(fresh)(l, memberProbeConversation, agents.HooksDir(s.cfg.DataDir), s.cfg.MCP()); err != nil {
		return session.MoveStarts{}, memberReason(err)
	}
	if !l.gate.qualified.Load() {
		return session.MoveStarts{}, memberReason(agentproto.ErrNotQualified)
	}
	chat, err := s.chatStartWith(local, memberChatArgv(false))
	if err != nil {
		return session.MoveStarts{}, memberReason(err)
	}
	term := s.terminalStart(l)
	if fresh {
		if chat, term, err = s.freshMemberStarts(local, l, chat, term); err != nil {
			return session.MoveStarts{}, memberReason(err)
		}
	}
	id := local.Info().ID
	m := session.MoveStarts{Protocol: chat, Terminal: term, Gone: session.GroupGone, Words: moveWords}
	if seam := s.moveSeam; seam != nil {
		m.AfterOldExit = func() { seam(id) }
	}
	return m, nil
}

// freshMemberStarts are the starts of a fresh member's move (MoveFresh):
// only a conversation's FIRST start is fresh. The chat's child starts a
// new conversation by the session recipe's start, and the terminal the move
// falls back to starts afresh on another; once a chat's child has started
// on a conversation, every later start on it (the chat's restart past its
// input bound, the way back to its terminal) resumes it, as any other
// conversation is resumed, for the recipe's start on an id in use is
// refused. resumeChat and resumeTerm are those resuming starts.
func (s *Server) freshMemberStarts(local *session.Local, l *chatLaunch, resumeChat session.ProtocolStart, resumeTerm func(context.Context, string, uint16, uint16) (session.Process, error)) (session.ProtocolStart, func(context.Context, string, uint16, uint16) (session.Process, error), error) {
	freshChat, err := s.chatStartWith(local, memberChatArgv(true))
	if err != nil {
		return nil, nil, err
	}
	freshTerm := s.freshTerminalStart(l)
	// begun is the conversation a chat's child started on.
	var begun atomic.Pointer[string]
	started := func(conversation string) bool {
		on := begun.Load()
		return on != nil && *on == conversation
	}
	chat := func(ctx context.Context, conversation string) (session.Child, session.Protocol, error) {
		if started(conversation) {
			return resumeChat(ctx, conversation)
		}
		child, driver, err := freshChat(ctx, conversation)
		if err == nil {
			begun.Store(&conversation)
		}
		return child, driver, err
	}
	term := func(ctx context.Context, conversation string, cols, rows uint16) (session.Process, error) {
		if started(conversation) {
			return resumeTerm(ctx, conversation, cols, rows)
		}
		return freshTerm(ctx, conversation, cols, rows)
	}
	return chat, term, nil
}

// memberChatArgv is the argv of a crew member's chat's child (design 4 and
// 14): a person's chat's, with no permission mode of Conductor's for a
// member that is not yolo and, fresh, the session recipe's start on the
// new conversation in place of --resume.
func memberChatArgv(fresh bool) func(l *chatLaunch, conversation, hooks string, mcp bool) ([]string, error) {
	return func(l *chatLaunch, conversation, hooks string, mcp bool) ([]string, error) {
		var yoloArgs []string
		if l.yolo && l.agent.Yolo != nil {
			yoloArgs = slices.Clone(l.agent.Yolo.Args)
		}
		cl := agentproto.ClaudeLaunch{
			Command: l.agent.Command, Protocol: l.agent.Protocol.Args, Conversation: conversation,
			Model: l.model, Yolo: l.yolo, YoloArgs: yoloArgs, LaunchArgs: l.args, HooksDir: hooks, NoMCP: !mcp,
			PermissionHost: true, Member: true,
		}
		if fresh && l.agent.Session != nil {
			cl.Fresh = catalog.Expand(l.agent.Session.StartArgs, conversation)
		}
		return agentproto.ClaudeArgs(cl)
	}
}

// freshTerminalStart starts a crew member's terminal afresh on a new
// conversation, as its launch started it: the agent's command, its
// session recipe's start on the conversation, and the rest of the
// launch's arguments, on the same Runtime, in the same folder, with the
// same environment, at the session's size.
func (s *Server) freshTerminalStart(l *chatLaunch) func(context.Context, string, uint16, uint16) (session.Process, error) {
	return func(ctx context.Context, conversation string, cols, rows uint16) (session.Process, error) {
		if conversation == "" || l.agent.Session == nil || len(l.agent.Session.StartArgs) == 0 {
			return nil, errNoConversation
		}
		rt, ok := s.runtimeAt(l.where)
		if !ok {
			return nil, errNoPipes
		}
		argv := slices.Clone(l.agent.Command)
		argv = append(argv, catalog.Expand(l.agent.Session.StartArgs, conversation)...)
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
