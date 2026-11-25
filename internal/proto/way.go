package proto

import (
	"fmt"
	"unicode/utf8"
)

// The move between ways (docs/protocol.md, Chat sessions;
// docs/plans/chat-or-terminal.md, design 9): a person's own server session
// runs its agent in its terminal or as a chat, and the operator moves it
// from one to the other once a turn has ended. The owner tells every viewer
// with a way message when the session's way has changed, and when a move is
// noted (pressed during a turn: made at its end), replaced or pressed away;
// the welcome carries the noted move too (Welcome.Noted), so another window
// or a reload shows it.

// CtlWay is the way message's type (owner -> client).
const CtlWay = "way"

// The ways a session runs (Way.Way, Way.Noted).
const (
	WayTerminal = "terminal"
	WayProtocol = "protocol"
)

// The move's error codes (ErrorMsg.Code).
const (
	// ErrCodeMoving refuses a submission, an answer or an interrupt while
	// the session moves between ways.
	ErrCodeMoving = "moving"
	// ErrCodeMoveRefused is a move whose conditions do not hold: its words
	// say which.
	ErrCodeMoveRefused = "move_refused"
)

// OlderSwitchyardWords say why a chat is not published: the switchyard's
// registered did not say transcript (Registered.Transcript), so its frames
// would close a viewer there.
const OlderSwitchyardWords = "Chats are not published: the switchyard is older than this server."

// The way message's bounds, in bytes.
const (
	// MaxWayProtocol bounds Way.Protocol, as the session cuts a driver's
	// name.
	MaxWayProtocol = 32
	// MaxWayWhy bounds Way.Why: one of the plan's sentences, or a start's
	// failure in a person's words.
	MaxWayWhy = 200
)

// Way says how the session runs its agent now (owner -> client): Way is
// "terminal" or "protocol", Protocol the protocol a chat speaks (absent in
// the terminal), Noted the way a move pressed during a turn goes to at the
// turn's end (absent when none is noted), and Why one sentence: why a
// noted move waits, or why the session is back the way it came from.
type Way struct {
	T        string `json:"t"`
	Way      string `json:"way"`
	Protocol string `json:"protocol,omitempty"`
	Noted    string `json:"noted,omitempty"`
	Why      string `json:"why,omitempty"`
}

// ValidWay reports whether w is a way a session runs: "terminal" or
// "protocol".
func ValidWay(w string) bool { return w == WayTerminal || w == WayProtocol }

// Valid reports whether m is within its bounds: its way one of the two, its
// noted move one of them or none, its protocol at most MaxWayProtocol bytes
// and its words at most MaxWayWhy, each valid UTF-8 with no control
// character.
func (m Way) Valid() bool {
	if m.T != CtlWay || !ValidWay(m.Way) || (m.Noted != "" && !ValidWay(m.Noted)) {
		return false
	}
	if len(m.Protocol) > MaxWayProtocol || !printable(m.Protocol) {
		return false
	}
	return len(m.Why) <= MaxWayWhy && utf8.ValidString(m.Why) && noControl(m.Why)
}

// EncodeWay builds the way message's frame (without HTML escaping, so it
// is at most MaxControl whatever its words), refusing one past its bounds.
func EncodeWay(m Way) ([]byte, error) {
	m.T = CtlWay
	if !m.Valid() {
		return nil, fmt.Errorf("proto: a way message past its bounds")
	}
	return MustControlRaw(m), nil
}

// noControl reports whether s holds no control character.
func noControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}
