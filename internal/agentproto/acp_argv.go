package agentproto

import (
	"errors"
	"fmt"
	"strings"
)

// ACPLaunch is what a chat's child of an agent of the Agent Client
// Protocol is started from: the session's terminal launch.
type ACPLaunch struct {
	// Command is the catalog's command (its program first).
	Command []string
	// Protocol is the catalog's protocol recipe's arguments: the agent's
	// own start of its ACP server ("acp", "--acp", "--profile acp").
	Protocol []string
	// Conversation is the session taken up: the terminal's conversation.
	Conversation string
	// Model is the model flag the terminal was started with, none for the
	// agent's own default; LaunchArgs the terminal's launch arguments.
	Model      []string
	LaunchArgs []string
}

// ACPArgError is ACPArgs's refusal of a terminal's argument that a chat of
// the agent cannot carry: the protocol's start takes the agent's own
// configuration alone, and nothing says what a terminal's flag means to it.
type ACPArgError struct{ Flag string }

func (e *ACPArgError) Error() string {
	return fmt.Sprintf("agentproto: %s is not an argument a chat of this agent can take", e.Flag)
}

// ACPArgs is the argv of a chat's child of an ACP agent, never a shell
// string: the catalog's command and the protocol recipe's arguments, which
// start the agent's ACP server on stdio. The session is taken up by the
// protocol (session/resume or session/load), the folder is the session's.
// A terminal launched with arguments of its own or a model flag is refused
// (ACPArgError), an added folder as ErrAddedFolder: a chat started without
// them would not be the session the terminal ran. A yolo launch's
// arguments are not carried: the chat's permission requests are the
// session's questions.
func ACPArgs(l ACPLaunch) ([]string, error) {
	if len(l.Command) == 0 || strings.TrimSpace(l.Command[0]) == "" {
		return nil, errors.New("agentproto: a chat's child needs the agent's command")
	}
	if len(l.Protocol) == 0 {
		return nil, errors.New("agentproto: an ACP agent's chat needs the arguments that start its ACP server")
	}
	if !acpSessionShape(l.Conversation) {
		return nil, fmt.Errorf("agentproto: a chat's child takes up a session, and %q is none", cutWords(l.Conversation))
	}
	for _, set := range [][]string{l.Command[1:], l.Protocol, l.Model, l.LaunchArgs} {
		if addedFolder(set) {
			return nil, ErrAddedFolder
		}
	}
	for _, set := range [][]string{l.Model, l.LaunchArgs} {
		if len(set) > 0 {
			name, _, _ := strings.Cut(set[0], "=")
			return nil, &ACPArgError{Flag: cutWords(name)}
		}
	}
	argv := append([]string{}, l.Command...)
	return append(argv, l.Protocol...), nil
}

// ACPArgWords is what a person reads when a session whose arguments hold
// flag is asked to move to Chat.
func ACPArgWords(flag string) string {
	return "This session's arguments hold " + flag + ", which a chat cannot take; it stays in its terminal."
}

// acpSessionShape says id can be a session's id through the protocol: the
// agent's own string ("ses_…" for one), 1 to 256 bytes with no space or
// control character. It is carried in a message, never on an argv.
func acpSessionShape(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
