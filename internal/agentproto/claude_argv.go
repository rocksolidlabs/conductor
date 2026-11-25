package agentproto

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// The files of the hooks directory a chat's child of Claude Code reads.
const (
	// ClaudeProtocolMCP is Conductor's MCP server for a chat's child, with
	// its per-server timeout (internal/agents).
	ClaudeProtocolMCP = "claude-protocol-mcp.json"
	// ClaudeProtocolSettings and ClaudeProtocolYoloSettings are the floor
	// (Conductor's deny rules), the second with the yolo key: the one
	// --settings a chat's child takes once Conductor answers its permission
	// prompts (ClaudeLaunch.PermissionHost).
	ClaudeProtocolSettings     = "claude-protocol.json"
	ClaudeProtocolYoloSettings = "claude-protocol-yolo.json"
	// ClaudePermissionTool is Conductor's MCP tool that answers a chat's
	// permission prompts.
	ClaudePermissionTool = "mcp__conductor__permission"
)

// claudeStreamArgs put Claude Code in print mode with stream-json in and
// out, its partial messages and its replay of each message written: every
// flag as its CLI reference names it.
var claudeStreamArgs = []string{
	"-p",
	"--input-format", "stream-json",
	"--output-format", "stream-json",
	"--verbose",
	"--include-partial-messages",
	"--replay-user-messages",
}

// claudeOwnFlags are the flags a chat's child gets from the driver alone,
// or must never get: an argument of the terminal's (or of the catalog's
// protocol recipe) that is one of them, alone or with "=value", is refused,
// so that the child reads one --settings, the same setting sources as its
// terminal, its sign-in, and the permission mode the driver passes.
var claudeOwnFlags = []string{
	"-p", "--print", "--input-format", "--output-format", "--verbose",
	"--include-partial-messages", "--replay-user-messages",
	"--resume", "-r", "--continue", "-c", "--session-id", "--fork-session",
	"--permission-mode", "--permission-prompt-tool", "--permission-prompts",
	"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
	"--settings", "--setting-sources", "--strict-mcp-config", "--bare",
}

// ClaudeLaunch is what a chat's child of Claude Code is started from: the
// session's terminal launch, as the catalog and the launch gave it.
type ClaudeLaunch struct {
	// Command is the catalog's command (its program first).
	Command []string
	// Protocol is the catalog's protocol recipe's arguments.
	Protocol []string
	// Conversation is the agent's conversation id, resumed.
	Conversation string
	// Model is the model flag the terminal was started with ("--model=x"),
	// none for the agent's own default.
	Model []string
	// Yolo says the terminal was a yolo launch: the catalog's yolo
	// arguments (YoloArgs) take the place of --permission-mode default.
	Yolo     bool
	YoloArgs []string
	// LaunchArgs are the terminal's launch arguments (the person's own).
	LaunchArgs []string
	// HooksDir is the absolute hooks directory the assets are in.
	HooksDir string
	// NoMCP leaves Conductor's MCP server out, as a terminal's launch does
	// when the server's MCP is off.
	NoMCP bool
	// PermissionHost says Conductor answers the child's permission
	// prompts: its MCP tool is named (unless NoMCP) and the floor is its
	// one --settings.
	// Until then a call that needs approval is denied by Claude Code
	// itself (a print-mode run with no host denies it).
	PermissionHost bool
}

// ClaudeArgs is the argv of a chat's child of Claude Code, every element
// as design 4 of the plan lists it, never a shell string: the catalog's
// command, print mode with stream-json, the protocol's arguments, the
// resumed conversation, the model, the permission mode (or the yolo
// arguments), the terminal's arguments, Conductor's MCP server and, with a
// permission host, its permission tool and exactly one --settings. Never
// --bare (which reads no sign-in), --setting-sources or
// --strict-mcp-config: the child reads the setting sources its terminal
// read. The terminal way's hook settings, trust arguments and screen
// recipe are not passed.
func ClaudeArgs(l ClaudeLaunch) ([]string, error) {
	if len(l.Command) == 0 || strings.TrimSpace(l.Command[0]) == "" {
		return nil, errors.New("agentproto: a chat's child needs the agent's command")
	}
	if !conversationShape(l.Conversation) {
		return nil, fmt.Errorf("agentproto: a chat's child resumes a conversation, and %q is none", cutWords(l.Conversation))
	}
	if !filepath.IsAbs(l.HooksDir) {
		return nil, fmt.Errorf("agentproto: the hooks directory %q is not absolute", l.HooksDir)
	}
	for _, set := range [][]string{l.Protocol, l.Model, l.LaunchArgs} {
		if f := ownFlag(set); f != "" {
			return nil, fmt.Errorf("agentproto: %s is set by the chat itself, and the terminal's arguments hold it", f)
		}
	}
	var argv []string
	argv = append(argv, l.Command...)
	argv = append(argv, claudeStreamArgs...)
	argv = append(argv, l.Protocol...)
	argv = append(argv, "--resume", l.Conversation)
	argv = append(argv, l.Model...)
	if l.Yolo && len(l.YoloArgs) > 0 {
		argv = append(argv, l.YoloArgs...)
	} else {
		argv = append(argv, "--permission-mode", "default")
	}
	argv = append(argv, l.LaunchArgs...)
	if !l.NoMCP {
		argv = append(argv, "--mcp-config", filepath.Join(l.HooksDir, ClaudeProtocolMCP))
	}
	if l.PermissionHost {
		settings := ClaudeProtocolSettings
		if l.Yolo {
			settings = ClaudeProtocolYoloSettings
		}
		if !l.NoMCP {
			// The prompt tool is Conductor's MCP server's: with the server's
			// MCP off, Claude Code itself denies what needs approval.
			argv = append(argv, "--permission-prompt-tool", ClaudePermissionTool)
		}
		argv = append(argv, "--settings", filepath.Join(l.HooksDir, settings))
	}
	return argv, nil
}

// ownFlag is the first of args that is one of the driver's own flags: by
// its name (alone or with "=value"), or as a short flag within a cluster of
// them (shortOwnFlag).
func ownFlag(args []string) string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		for _, f := range claudeOwnFlags {
			if name == f {
				return f
			}
		}
		if f := shortOwnFlag(a); f != "" {
			return f
		}
	}
	return ""
}

// Claude Code's documented short flags (its CLI reference): -p (--print),
// -r (--resume), -c (--continue), -n (--name), -v (--version), -w
// (--worktree). Of them -p, -r and -c are the chat's own; -r, -n and -w
// take a value, which a cluster may carry attached ("-rID", "-nname").
const (
	claudeShortOwn   = "prc"
	claudeShortValue = "rnw"
)

// shortOwnFlag is the chat's own short flag a short-flag argument holds:
// "-p" alone, or among others as "-cp" or "-pc", or carrying its value as
// "-rID". The letters after one that takes a value are that value, and a
// digit ends the flags (a negative number is no flag). "" for none, and for
// a long flag ("--…").
func shortOwnFlag(a string) string {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return ""
	}
	for _, r := range a[1:] {
		switch {
		case strings.ContainsRune(claudeShortOwn, r):
			return "-" + string(r)
		case strings.ContainsRune(claudeShortValue, r), r >= '0' && r <= '9', r == '=':
			return ""
		}
	}
	return ""
}

// conversationShape says id can be resumed by an argument: 1 to 128 bytes
// of letters, digits and dashes, not starting with a dash.
func conversationShape(id string) bool {
	if id == "" || len(id) > 128 || id[0] == '-' {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// cutWords cuts s for an error's words.
func cutWords(s string) string {
	if len(s) > 40 {
		return strings.ToValidUTF8(s[:40], "") + "…"
	}
	return s
}
