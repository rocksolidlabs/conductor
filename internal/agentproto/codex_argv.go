package agentproto

import (
	"errors"
	"fmt"
	"strings"
)

// codexAppServerArgs start Codex's app server on stdio: JSON-RPC 2.0 with
// the header left out, one message a line (its app-server page).
var codexAppServerArgs = []string{"app-server", "--listen", "stdio://"}

// CodexLaunch is what a chat's child of Codex is started from: the
// session's terminal launch, as the catalog and the launch gave it.
type CodexLaunch struct {
	// Command is the catalog's command (its program first).
	Command []string
	// Protocol is the catalog's protocol recipe's arguments, put after the
	// app server's own.
	Protocol []string
	// Conversation is the thread resumed: the terminal's conversation id.
	Conversation string
	// Model is the model flag the terminal was started with ("--model=x"),
	// none for the agent's own default.
	Model []string
	// Yolo says the terminal was a yolo launch, whose catalog arguments are
	// YoloArgs.
	Yolo     bool
	YoloArgs []string
	// LaunchArgs are the terminal's launch arguments (the person's own).
	LaunchArgs []string
	// MCP is Conductor's MCP server as configuration overrides
	// (agents.MCPFor), none when the server's MCP is off.
	MCP []string
	// Dir is the session's folder, the thread's working directory.
	Dir string
}

// CodexResume is what the thread is resumed with (thread/resume's stable
// parameters): the terminal's model, approval policy and sandbox where its
// arguments chose them, and the session's folder. "" leaves one to the
// thread and the person's configuration, as the terminal left it.
type CodexResume struct {
	Thread         string
	Model          string
	ApprovalPolicy string
	Sandbox        string
	Cwd            string
}

// CodexArgWords is what a person reads when a session whose arguments hold
// flag, which a chat cannot carry, is asked to move to Chat.
func CodexArgWords(flag string) string {
	return "This session's arguments hold " + flag + ", which a chat cannot take; it stays in its terminal."
}

// CodexArgError is CodexArgs's refusal of an argument of the terminal's that
// the app server takes no counterpart of.
type CodexArgError struct{ Flag string }

func (e *CodexArgError) Error() string {
	return fmt.Sprintf("agentproto: %s is not an argument a chat of Codex can take", e.Flag)
}

// The values thread/resume takes (its schema's AskForApproval and
// SandboxMode, as strings).
var (
	codexApprovals = []string{"untrusted", "on-request", "never"}
	codexSandboxes = []string{"read-only", "workspace-write", "danger-full-access"}
)

// CodexArgs is the argv of a chat's child of Codex and what its thread is
// resumed with, never a shell string: the catalog's command, the app server
// on stdio, the protocol's arguments, the configuration overrides of the
// terminal's arguments (-c and --config, --enable, --disable,
// --strict-config) and Conductor's MCP server. The model, the approval
// policy and the sandbox the terminal's arguments chose (a yolo launch's
// --dangerously-bypass-approvals-and-sandbox: never asking, no sandbox) go
// to thread/resume, the app server taking no such flags. A flag that only
// draws the terminal (--no-alt-screen, --no-daemon) is left out. Any other
// argument is refused (CodexArgError), an added folder as ErrAddedFolder:
// the move asks this before it stops anything. The terminal way's notify,
// title and trust overrides are not passed.
func CodexArgs(l CodexLaunch) ([]string, CodexResume, error) {
	var res CodexResume
	if len(l.Command) == 0 || strings.TrimSpace(l.Command[0]) == "" {
		return nil, res, errors.New("agentproto: a chat's child needs the agent's command")
	}
	if !conversationShape(l.Conversation) {
		return nil, res, fmt.Errorf("agentproto: a chat's child resumes a thread, and %q is none", cutWords(l.Conversation))
	}
	res.Thread, res.Cwd = l.Conversation, l.Dir
	for _, set := range [][]string{l.Command[1:], l.Protocol, l.Model, l.YoloArgs, l.LaunchArgs} {
		if addedFolder(set) {
			return nil, res, ErrAddedFolder
		}
	}
	var overrides []string
	sets := [][]string{l.Model, l.LaunchArgs}
	if l.Yolo {
		sets = append(sets, l.YoloArgs)
	}
	for _, set := range sets {
		o, err := codexTake(set, &res)
		if err != nil {
			return nil, res, err
		}
		overrides = append(overrides, o...)
	}
	if f := codexOwnArg(l.Protocol); f != "" {
		return nil, res, &CodexArgError{Flag: f}
	}
	var argv []string
	argv = append(argv, l.Command...)
	argv = append(argv, codexAppServerArgs...)
	argv = append(argv, l.Protocol...)
	argv = append(argv, overrides...)
	argv = append(argv, l.MCP...)
	return argv, res, nil
}

// codexOwnArg is the first of args (the catalog's protocol recipe) that
// would choose the app server's transport or start something else: the
// driver's own.
func codexOwnArg(args []string) string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--listen", "--stdio", "app-server", "proxy", "daemon":
			return name
		}
	}
	return ""
}

// codexTakes are the flags codexTake carries over to a chat (each case of
// its switch): any other flag keeps a session in its terminal
// (StaysInTerminal).
var codexTakes = []string{"-c", "--config", "--enable", "--disable", "--strict-config", "-m", "--model", "-s", "--sandbox", "-a", "--ask-for-approval", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--no-alt-screen", "--no-daemon"}

// codexTake reads a terminal's arguments: the configuration overrides it
// returns, to go on the app server's argv; the model, the approval policy
// and the sandbox into res. Refused: any other flag, and a word that is no
// flag's value (a prompt, a subcommand).
func codexTake(args []string, res *CodexResume) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, attached := strings.Cut(a, "=")
		// A short flag carries its value attached, with "=" or without:
		// -mo3, -m=o3, -cx=y.
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			name, value, attached = a[:2], strings.TrimPrefix(a[2:], "="), true
		}
		next := func() (string, bool) {
			if attached {
				return value, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch name {
		case "-c", "--config", "--enable", "--disable":
			// A value is a setting or a feature's name, never a word that
			// starts with a dash: one would be read as the next option, or
			// as the end of them ("--"), by the app server's parser, and what
			// the driver adds after the overrides with it.
			v, ok := next()
			if !ok || v == "" || strings.HasPrefix(v, "-") {
				return nil, &CodexArgError{Flag: name}
			}
			out = append(out, name, v)
		case "--strict-config":
			if attached {
				return nil, &CodexArgError{Flag: name}
			}
			out = append(out, name)
		case "-m", "--model":
			v, ok := next()
			if !ok || v == "" {
				return nil, &CodexArgError{Flag: name}
			}
			res.Model = v
		case "-s", "--sandbox":
			v, ok := next()
			if !ok || !contains(codexSandboxes, v) {
				return nil, &CodexArgError{Flag: name}
			}
			res.Sandbox = v
		case "-a", "--ask-for-approval":
			v, ok := next()
			if !ok || !contains(codexApprovals, v) {
				return nil, &CodexArgError{Flag: name}
			}
			res.ApprovalPolicy = v
		case "--dangerously-bypass-approvals-and-sandbox", "--yolo":
			if attached {
				return nil, &CodexArgError{Flag: name}
			}
			res.ApprovalPolicy, res.Sandbox = "never", "danger-full-access"
		case "--no-alt-screen", "--no-daemon":
			// How the terminal draws, or which server it talks to: nothing
			// of a chat's.
			if attached {
				return nil, &CodexArgError{Flag: name}
			}
		default:
			if strings.HasPrefix(a, "-") {
				return nil, &CodexArgError{Flag: name}
			}
			return nil, &CodexArgError{Flag: cutWords(a)}
		}
	}
	return out, nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
