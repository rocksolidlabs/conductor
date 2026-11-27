package proto

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// The agent's own commands (docs/plans/chat-or-terminal.md, row 12; I19): a
// chat whose agent lists the commands its program runs sends them, as the
// session keeps them, in a TRANSCRIPT message of their own to the operator's
// own chat connection alone, for its box's / menu. A link's viewer, a peer
// over the host link or a switchyard and a quiet connection are never sent
// it: the list names what is installed on the operator's machine.

// TranscriptCommandsT is the commands message's t.
const TranscriptCommandsT = "commands"

// The commands message's bounds, in bytes but where said.
const (
	// MaxCommands bounds the commands a message carries; More says the
	// agent listed more.
	MaxCommands = 200
	// MaxCommandName bounds a command's name, of CommandNameShape.
	MaxCommandName = 64
	// MaxCommandAliases bounds a command's aliases, each at most
	// MaxCommandAlias bytes of CommandNameShape.
	MaxCommandAliases = 2
	MaxCommandAlias   = 32
	// MaxCommandWords bounds the agent's own words of a command, cut with
	// an ellipsis; MaxCommandHint its argument hint.
	MaxCommandWords = 120
	MaxCommandHint  = 64
)

// CommandNameShape is what a command's name and an alias are made of: a
// letter, a digit or an underscore, then those, a dot, a colon or a dash.
// Nothing that could write another instruction into the operator's line (a
// space, a quote, an @, a slash, a mark one cannot see).
var CommandNameShape = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)

// Command is one of the agent's commands: its name (without the slash), the
// agent's own words for it, its argument hint, and its aliases.
type Command struct {
	Name    string   `json:"name"`
	Words   string   `json:"words,omitempty"`
	Hint    string   `json:"hint,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// TranscriptCommandsMsg is the agent's commands (owner -> client): Rev
// raised by each change of the list, More when the agent listed more than
// MaxCommands.
type TranscriptCommandsMsg struct {
	T        string    `json:"t"`
	Rev      uint64    `json:"rev"`
	Commands []Command `json:"commands"`
	More     bool      `json:"more,omitempty"`
}

// Valid reports whether m is within its bounds: at most MaxCommands, each
// name of CommandNameShape within MaxCommandName, no name twice, at most
// MaxCommandAliases aliases of the same shape within MaxCommandAlias, the
// words within MaxCommandWords and the hint within MaxCommandHint, each
// valid UTF-8 with no control character.
func (m TranscriptCommandsMsg) Valid() bool {
	if m.T != TranscriptCommandsT || len(m.Commands) > MaxCommands {
		return false
	}
	seen := make(map[string]bool, len(m.Commands))
	for _, c := range m.Commands {
		if len(c.Name) > MaxCommandName || !CommandNameShape.MatchString(c.Name) || seen[c.Name] {
			return false
		}
		seen[c.Name] = true
		if len(c.Aliases) > MaxCommandAliases {
			return false
		}
		for _, a := range c.Aliases {
			if len(a) > MaxCommandAlias || !CommandNameShape.MatchString(a) {
				return false
			}
		}
		if len(c.Words) > MaxCommandWords || len(c.Hint) > MaxCommandHint {
			return false
		}
		for _, s := range []string{c.Words, c.Hint} {
			if !utf8.ValidString(s) || !noControl(s) {
				return false
			}
		}
	}
	return true
}

// EncodeCommands builds the commands message's TRANSCRIPT frame: an error
// for a message past its bounds, never a frame past MaxTranscript.
func EncodeCommands(m TranscriptCommandsMsg) ([]byte, error) {
	m.T = TranscriptCommandsT
	if m.Commands == nil {
		m.Commands = []Command{}
	}
	if !m.Valid() {
		return nil, fmt.Errorf("proto: a commands message past its bounds")
	}
	return EncodeTranscript(m)
}

// DecodeCommands reads a commands message strictly (no field it does not
// know), within its bounds.
func DecodeCommands(payload []byte) (TranscriptCommandsMsg, error) {
	var m TranscriptCommandsMsg
	if err := decodeStrict(payload, &m); err != nil {
		return m, fmt.Errorf("proto: bad commands message: %w", err)
	}
	if !m.Valid() {
		return m, fmt.Errorf("proto: a commands message past its bounds")
	}
	return m, nil
}
