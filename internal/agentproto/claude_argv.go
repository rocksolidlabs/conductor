package agentproto

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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

// ErrAddedFolder is ClaudeArgs's refusal of a launch whose arguments name
// an added folder (--add-dir): such a session keeps to its terminal, and a
// chat's child is started in the session's own folder alone.
var ErrAddedFolder = errors.New("agentproto: the launch's arguments name an added folder, which stays with the terminal")

// ErrOptionsEnd is a chat's refusal of a launch whose arguments hold a lone
// "--": the agent's parser reads every word after it as a plain word, and a
// chat's child is started with what the chat adds after the terminal's
// arguments. Such a session keeps to its terminal.
var ErrOptionsEnd = errors.New("agentproto: the launch's arguments hold a lone --, which stays with the terminal")

// OptionsEndWords is what a person reads when a session launched with a lone
// "--" in its arguments is asked to move to Chat, or launched as one.
const OptionsEndWords = "Sessions launched with -- in their arguments stay in their terminal."

// optionsEnd is the lone "--" that ends an agent's options.
const optionsEnd = "--"

// endsOptions reports whether args hold a lone "--".
func endsOptions(args []string) bool {
	for _, a := range args {
		if a == optionsEnd {
			return true
		}
	}
	return false
}

// AddedFolderWords is what a person reads when a session launched with an
// added folder is asked to move to Chat (the plan's words, design 13).
const AddedFolderWords = "Sessions launched with an added folder stay in their terminal."

// OwnFlagError is ClaudeArgs's refusal of an argument of the terminal's
// (or of the catalog's protocol recipe) that a chat sets itself.
type OwnFlagError struct{ Flag string }

func (e *OwnFlagError) Error() string {
	return fmt.Sprintf("agentproto: %s is set by the chat itself, and the terminal's arguments hold it", e.Flag)
}

// OwnFlagWords is what a person reads when a session whose arguments hold
// flag is asked to move to Chat (the plan's words, design 13).
func OwnFlagWords(flag string) string {
	return "This session's arguments hold " + flag + ", which a chat sets itself; it stays in its terminal."
}

// addedFolderFlag is the CLI reference's flag that adds a working
// directory to a session.
const addedFolderFlag = "--add-dir"

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
// arguments), Conductor's MCP server alone (--strict-mcp-config, always:
// the child loads no tool server but the one named here, and none when the
// server's MCP is off; the person's own tool servers and a folder's stay
// with the session's terminal) and, with a permission host, its permission
// tool and exactly one --settings, each in one word (--flag=value), then
// the terminal's arguments. Never
// --bare (which reads no sign-in) or --setting-sources: the child reads
// the setting sources its terminal read. The terminal way's hook settings,
// trust arguments and screen recipe are not passed. A launch whose
// arguments name an added folder is refused (ErrAddedFolder), and one that
// holds a flag of the chat's own (OwnFlagError): the move asks this before
// it stops anything.
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
	for _, set := range [][]string{l.Command[1:], l.Protocol, l.Model, l.YoloArgs, l.LaunchArgs} {
		if addedFolder(set) {
			return nil, ErrAddedFolder
		}
		if endsOptions(set) {
			return nil, ErrOptionsEnd
		}
	}
	for _, set := range [][]string{l.Protocol, l.Model, l.LaunchArgs} {
		if f := ownFlag(set); f != "" {
			return nil, &OwnFlagError{Flag: f}
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
	// What the chat adds comes before the terminal's arguments, each flag
	// in ONE word: a flag that takes a value gets it after its "=", which
	// the agent's parser reads as that one value (a list flag too), so no
	// word of the person's can become one of them, and none of them can
	// become the value of a last flag of the person's that wants one (that
	// flag then lacks its value, and the agent refuses to start).
	if !l.NoMCP {
		argv = append(argv, "--mcp-config="+filepath.Join(l.HooksDir, ClaudeProtocolMCP))
	}
	argv = append(argv, "--strict-mcp-config")
	if l.PermissionHost {
		settings := ClaudeProtocolSettings
		if l.Yolo {
			settings = ClaudeProtocolYoloSettings
		}
		if !l.NoMCP {
			// The prompt tool is Conductor's MCP server's: with the server's
			// MCP off, Claude Code itself denies what needs approval.
			argv = append(argv, "--permission-prompt-tool="+ClaudePermissionTool)
		}
		argv = append(argv, "--settings="+filepath.Join(l.HooksDir, settings))
	}
	argv = append(argv, l.LaunchArgs...)
	return argv, nil
}

// StaysInTerminal says whether a launch whose arguments hold arg (a flag,
// alone or with "=value") is one the chat of protocol name refuses, so
// that the session stays in its terminal: for Claude Code, an added folder
// or a flag the chat sets itself, judged by the same lists ClaudeArgs
// refuses by; for Codex, an added folder or any flag its app server takes
// no counterpart of (codexTakes, the list CodexArgs reads by); for an agent
// of the Agent Client Protocol, any word (ACPArgs takes none of the
// terminal's). A lone "--" keeps every chat's session in its terminal. A
// name Conductor does not speak: false.
func StaysInTerminal(name, arg string) bool {
	switch name {
	case ClaudeStreamJSON:
		return arg == optionsEnd || addedFolder([]string{arg}) || ownFlag([]string{arg}) != ""
	case ACPv1:
		return arg != ""
	case CodexAppServer:
		if addedFolder([]string{arg}) {
			return true
		}
		flag, _, _ := strings.Cut(arg, "=")
		if len(flag) > 2 && flag[0] == '-' && flag[1] != '-' {
			flag = flag[:2]
		}
		return strings.HasPrefix(flag, "-") && !slices.Contains(codexTakes, flag)
	}
	return false
}

// addedFolder reports whether args name an added folder: the flag alone
// (its folders follow) or with "=value".
func addedFolder(args []string) bool {
	for _, a := range args {
		if name, _, _ := strings.Cut(a, "="); name == addedFolderFlag {
			return true
		}
	}
	return false
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
