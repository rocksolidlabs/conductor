package proto

import (
	"fmt"
	"unicode/utf8"
)

// A chat's permission request (docs/protocol.md, Chat sessions;
// docs/plans/chat-or-terminal.md, design 7): a call the agent puts to
// Conductor's prompt tool is a permission entry in the transcript, shown as
// the session's question, and the operator alone answers it with
// permission_answer, bound to the request by its call and its input's
// digest. Every other answer path refuses it (ErrCodePermission).

// CtlPermissionAnswer is the operator's answer to a permission request
// (client -> owner).
const CtlPermissionAnswer = "permission_answer"

// The choices of a permission_answer.
const (
	PermissionAllowOnce = "allow_once"
	PermissionDeny      = "deny"
)

// The permission request's error codes (ErrorMsg.Code).
const (
	// ErrCodeStaleRequest answers a permission_answer whose call, digest or
	// claim no longer matches the open request, or whose call has a result:
	// the request stays open.
	ErrCodeStaleRequest = "stale_request"
	// ErrCodeNotAnswerer answers a permission_answer from anyone but the
	// operator: a link's viewer of any role, a decision link, a relayed
	// viewer.
	ErrCodeNotAnswerer = "not_answerer"
)

// The permission_answer's bounds, in bytes.
const (
	// MaxPermissionQuestion bounds its question, a session question's id.
	MaxPermissionQuestion = 32
	// MaxPermissionCall bounds its call, the agent's id of the tool call.
	MaxPermissionCall = 128
	// PermissionSha256Len is its digest's length: 64 lowercase hex digits.
	PermissionSha256Len = 64
	// MaxPermissionMessage bounds the message a deny tells the agent.
	MaxPermissionMessage = 500
)

// PermissionAnswer answers an open permission request (client -> owner, the
// operator alone): Question is the request's question id, Call the agent's
// id of the call and Sha256 the input's digest, as the request's entry gave
// them; Choice allow_once or deny; Message, with a deny, what the agent is
// told. Answered answer_ok naming ReqID once the verdict was delivered, or
// an ErrorMsg naming it (question_answered, stale_request, not_answerer,
// read_only, moving, not_protocol, bad_frame).
type PermissionAnswer struct {
	T        string `json:"t"`
	ReqID    string `json:"reqId"`
	Question string `json:"question"`
	Call     string `json:"call"`
	Sha256   string `json:"sha256"`
	Choice   string `json:"choice"`
	Message  string `json:"message,omitempty"`
}

// Valid reports whether m is within its bounds: a reqId of 1 to
// MaxTranscriptReqID printable ASCII, a question of 1 to
// MaxPermissionQuestion printable ASCII, a call of at most
// MaxPermissionCall printable ASCII, a digest of 64 lowercase hex digits, a
// choice of the closed set, and a message of at most MaxPermissionMessage
// bytes of valid UTF-8 with no control character, with a deny alone.
func (m PermissionAnswer) Valid() bool {
	if m.T != CtlPermissionAnswer || !validReqID(m.ReqID) {
		return false
	}
	if m.Question == "" || len(m.Question) > MaxPermissionQuestion || !printable(m.Question) {
		return false
	}
	if len(m.Call) > MaxPermissionCall || !printable(m.Call) || !lowerHex(m.Sha256, PermissionSha256Len) {
		return false
	}
	switch m.Choice {
	case PermissionAllowOnce:
		if m.Message != "" {
			return false
		}
	case PermissionDeny:
	default:
		return false
	}
	return len(m.Message) <= MaxPermissionMessage && utf8.ValidString(m.Message) && noControl(m.Message)
}

// lowerHex reports whether s is n lowercase hex digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// DecodePermissionAnswer reads a permission_answer strictly (no field it
// does not know), within its bounds.
func DecodePermissionAnswer(payload []byte) (PermissionAnswer, error) {
	var m PermissionAnswer
	if err := decodeStrict(payload, &m); err != nil {
		return m, fmt.Errorf("proto: bad permission_answer: %w", err)
	}
	if !m.Valid() {
		return m, fmt.Errorf("proto: permission_answer past its bounds")
	}
	return m, nil
}
