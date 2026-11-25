package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// chatLaunch is what a server session's terminal launch leaves for the start
// of its chat's child, the protocol way (docs/plans/chat-or-terminal.md,
// designs 2 and 4): the Runtime and the folder the terminal started on, the
// agent's catalog entry as the terminal took it, the launch's own arguments,
// its model flag and its yolo choice, and the terminal's environment made a
// chat's. Kept in memory while the session is listed, gone with it; never
// recorded, logged or put on the wire.
type chatLaunch struct {
	where  ext.Where
	agent  catalog.Agent
	yolo   bool
	model  []string
	args   []string
	env    []string
	folder *session.Folder
	ref    ext.ProcessRef
	// What a move back to the terminal starts it again with
	// (terminalStart): the launch's arguments after the command and the
	// conversation's (the user's, the model's, yolo's, the hooks', the MCP
	// server's), the terminal's own environment, and its size at launch
	// (the session's own size is the one a move starts it at).
	termTail   []string
	termEnv    []string
	cols, rows uint16
	// gate holds that the agent's probed version qualified for a chat once:
	// a later start of this session's child (a restart's, a move's again)
	// reuses it, and the initialize answer's shape check, with its version,
	// still guards every start.
	gate *chatGate
}

// chatGate is a session's version check, made once.
type chatGate struct{ qualified atomic.Bool }

// Where a chat's child's start is told apart.
var (
	// errNoChat refuses a chat for a session whose launch left none: an
	// agent with no protocol, a hosted session, one launched before.
	errNoChat = errors.New("this session's agent has no chat")
	// errNoPipes refuses a chat on a Runtime that cannot start one.
	errNoPipes = errors.New("this session's Runtime cannot start a chat")
	// errNoConversation refuses a chat with no conversation to resume.
	errNoConversation = errors.New("this session has no conversation to resume yet")
)

// restartedInputWords is the notice of a chat's child started again because what
// Conductor wrote into it reached its bound (agentproto.MaxClaudeInput).
const restartedInputWords = "The agent was started again on the same conversation: its input reached its bound."

// chatEnv is a terminal's environment as a chat's child takes it: its
// TERM and COLORTERM taken out (pty.BuildEnv always sets them), NO_COLOR=1
// and CONDUCTOR_PROTOCOL=1 set; nothing else added, no vendor credential
// among it.
func chatEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "TERM", "COLORTERM", "NO_COLOR", "CONDUCTOR_PROTOCOL":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "NO_COLOR=1", "CONDUCTOR_PROTOCOL=1")
}

// keepChatLaunch keeps what a session's chat's child starts from, for an
// agent whose catalog entry declares a protocol Conductor speaks.
func (s *Server) keepChatLaunch(id string, l chatLaunch) {
	if l.agent.Protocol.Off() || agentproto.Check(l.agent.Protocol) != nil {
		return
	}
	l.gate = &chatGate{}
	s.chatLaunches.Store(id, &l)
}

// forgetChatLaunch lets a removed session's go.
func (s *Server) forgetChatLaunch(id string) { s.chatLaunches.Delete(id) }

// chatStart is the start of a chat's child for the server session local:
// the piece the move between ways (row 5's SwapWay) calls, and the tests'
// way in until then (sessiontest.EnterProtocol). Its first start checks the
// agent's probed version against its driver's (agentproto.ErrNotQualified,
// the session staying in its terminal), and every later one, a restart's
// too, reuses that answer; each builds
// the child's argv of design 4 (agentproto.ClaudeArgs) on the conversation
// it is given, and starts it through the session's own Runtime's
// StartPipes, in the terminal's folder, with the terminal's environment
// made a chat's (chatEnv), under the start's context alone. The driver's
// restart past its input's bound is the session's RestartProtocol.
func (s *Server) chatStart(local *session.Local) (session.ProtocolStart, error) {
	v, ok := s.chatLaunches.Load(local.Info().ID)
	if !ok {
		return nil, errNoChat
	}
	l := v.(*chatLaunch)
	rt, ok := s.runtimeAt(l.where)
	if !ok {
		return nil, errNoPipes
	}
	ps, ok := rt.(ext.PipeStarter)
	if !ok {
		return nil, errNoPipes
	}
	if l.agent.Protocol.Name != agentproto.ClaudeStreamJSON {
		return nil, errNoChat
	}
	hooks := agents.HooksDir(s.cfg.DataDir)
	mcp := s.cfg.MCP()
	initWait, maxInput := s.chatInitWait, s.chatMaxInput
	return func(ctx context.Context, conversation string) (session.Child, session.Protocol, error) {
		if conversation == "" {
			return nil, nil, errNoConversation
		}
		if !l.gate.qualified.Load() {
			// The probe answers within its own short wait, and a slow one
			// answers none: asked once per session, so that a running
			// chat's restart never meets it.
			version := ""
			if id := s.identityNow(ctx)(l.agent); id != nil && id.Identified {
				version = id.Version
			}
			if !agentproto.Qualified(l.agent.Protocol.Name, version) {
				return nil, nil, fmt.Errorf("%w (version %q)", agentproto.ErrNotQualified, version)
			}
			l.gate.qualified.Store(true)
		}
		var yoloArgs []string
		if l.yolo && l.agent.Yolo != nil {
			yoloArgs = slices.Clone(l.agent.Yolo.Args)
		}
		argv, err := agentproto.ClaudeArgs(agentproto.ClaudeLaunch{
			Command: l.agent.Command, Protocol: l.agent.Protocol.Args, Conversation: conversation,
			Model: l.model, Yolo: l.yolo, YoloArgs: yoloArgs, LaunchArgs: l.args, HooksDir: hooks, NoMCP: !mcp,
		})
		if err != nil {
			return nil, nil, err
		}
		dirFile, err := l.folder.Dir()
		if err != nil {
			return nil, nil, fmt.Errorf("the session's folder: %w", err)
		}
		pipes, err := ps.StartPipes(ctx, ext.PipeSpec{Argv: argv, Dir: l.folder.Path(), DirFile: dirFile, Env: slices.Clone(l.env), Ref: l.ref})
		dirFile.Close()
		if err != nil {
			s.log.Warn("a chat's child did not start", "session", local.Info().ID, "agent", l.agent.ID, "err", err)
			return nil, nil, fmt.Errorf("%w (its Runtime did not start it)", agentproto.ErrNotStarted)
		}
		driver := agentproto.NewClaude(pipes.Stdin(), pipes.Stdout(), agentproto.ClaudeOptions{
			Restart:  func() { _ = local.RestartProtocol("restarted", restartedInputWords) },
			InitWait: initWait,
			MaxInput: maxInput,
		})
		return session.PipesChild(pipes), driver, nil
	}, nil
}
