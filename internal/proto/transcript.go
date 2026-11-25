package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// A chat's wire (docs/protocol.md, Chat sessions; docs/plans/chat-or-terminal.md,
// design 6): a session that runs its agent through the agent's own protocol
// keeps its conversation as typed entries (session.TranscriptEntry), and a
// viewer is sent them in TRANSCRIPT frames (TypeTranscript), owner to client:
// each new or revised entry as it happens, the entries changed after the
// viewer's resume point (Hello.TranscriptAfter) after its ready, and a reply
// to each transcript_get. A viewer stops the agent's turn with interrupt. A
// session that runs in its terminal sends no TRANSCRIPT and answers both
// messages not_protocol.

// The chat's control messages.
const (
	// client -> owner
	CtlTranscriptGet = "transcript_get"
	CtlInterrupt     = "interrupt"
	// owner -> client
	CtlInterruptOK = "interrupt_ok"
)

// The kinds of a TRANSCRIPT frame's message (its "t").
const (
	TranscriptEntryT = "entry"
	TranscriptPageT  = "page"
	TranscriptPartT  = "part"
)

// The parts of an entry a transcript_get may read.
const (
	TranscriptPartInput  = "input"
	TranscriptPartOutput = "output"
	TranscriptPartDiff   = "diff"
)

// The chat's error codes (ErrorMsg.Code).
const (
	// ErrCodeNotTerminal refuses keys or a resize on a chat: it has no terminal.
	ErrCodeNotTerminal = "not_terminal"
	// ErrCodeNotProtocol refuses interrupt and transcript_get on a session
	// that runs in its terminal.
	ErrCodeNotProtocol = "not_protocol"
	// ErrCodeNoTurn refuses an interrupt while the agent works on nothing.
	ErrCodeNoTurn = "no_turn"
	// ErrCodeTranscriptGone answers a read of entries or a part the
	// transcript no longer keeps.
	ErrCodeTranscriptGone = "transcript_gone"
	// ErrCodeUnconfirmed is a message the agent did not acknowledge in time:
	// written once, never written again.
	ErrCodeUnconfirmed = "unconfirmed"
	// ErrCodePermission refuses a line or an answer naming a permission
	// request: it is answered with its own buttons.
	ErrCodePermission = "permission"
)

// The chat's bounds, in bytes but where said.
const (
	// MaxTranscript bounds a TRANSCRIPT frame's payload: one entry, a page
	// or a piece of a part. Over a data channel it is fragmented by CHUNK.
	MaxTranscript = 128 << 10
	// MaxTranscriptEntry bounds one entry as a TRANSCRIPT entry message
	// encodes it, without HTML escaping: the owner cuts an entry's words to
	// fit (an entry at its session's bounds always fits).
	MaxTranscriptEntry = 12 << 10
	// MaxTranscriptPage bounds the entries of a page (a reply's limit), and
	// MaxTranscriptData the bytes of a part one piece carries (in data,
	// base64, so the piece fits MaxTranscript).
	MaxTranscriptPage = 100
	MaxTranscriptData = 95 << 10
	// TranscriptReplayBytes bounds the entries a viewer is sent after its
	// ready, the open permission requests' outside it: past it the newest
	// that fit go, with more.
	TranscriptReplayBytes = 256 << 10
	// MaxTranscriptReqID bounds a transcript_get's and an interrupt's reqId.
	MaxTranscriptReqID = 32
	// MaxTranscriptInFlight bounds one connection's transcript_get replies
	// not yet written to it: past it a request is refused too_many_requests.
	MaxTranscriptInFlight = 4
)

// TranscriptGet reads a chat's transcript (client -> owner, every role): a
// page of the entries after After (oldest first) or, with Before, the newest
// before it that are also after After; or, with Part, a piece of one entry's
// part from its Offset. ReqID names the reply: a TRANSCRIPT page or part
// naming it, or an ErrorMsg naming it (transcript_gone, too_many_requests,
// not_protocol, bad_frame). Limit is at most MaxTranscriptPage, 0 for it.
type TranscriptGet struct {
	T      string             `json:"t"`
	ReqID  string             `json:"reqId"`
	After  uint64             `json:"after,omitempty"`
	Before uint64             `json:"before,omitempty"`
	Limit  int                `json:"limit,omitempty"`
	Part   *TranscriptPartRef `json:"part,omitempty"`
}

// TranscriptPartRef names a part to read: its entry, its name (input,
// output, diff) and the offset to read from.
type TranscriptPartRef struct {
	Seq    uint64 `json:"seq"`
	Name   string `json:"name"`
	Offset int64  `json:"offset,omitempty"`
}

// Valid reports whether m is within its bounds: a reqId of 1 to
// MaxTranscriptReqID printable ASCII, a limit of 0 to MaxTranscriptPage, and
// a part of the closed set at an offset not below 0, named alone.
func (m TranscriptGet) Valid() bool {
	if !validReqID(m.ReqID) || m.Limit < 0 || m.Limit > MaxTranscriptPage {
		return false
	}
	if p := m.Part; p != nil {
		switch p.Name {
		case TranscriptPartInput, TranscriptPartOutput, TranscriptPartDiff:
		default:
			return false
		}
		return p.Seq > 0 && p.Offset >= 0 && m.After == 0 && m.Before == 0 && m.Limit == 0
	}
	return true
}

// Interrupt stops the agent's turn on a chat (client -> owner, controllers
// only): the chat's Stop. Answered InterruptOK naming ReqID, or an ErrorMsg
// naming it (not_protocol, no_turn, read_only, bad_frame, too_many_requests).
type Interrupt struct {
	T     string `json:"t"`
	ReqID string `json:"reqId"`
}

// Valid reports whether m names a reqId of 1 to MaxTranscriptReqID
// printable ASCII.
func (m Interrupt) Valid() bool { return validReqID(m.ReqID) }

// InterruptOK says the owner asked the agent to stop its turn (owner ->
// client): the turn's end follows in the transcript.
type InterruptOK struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
}

func validReqID(id string) bool {
	return id != "" && len(id) <= MaxTranscriptReqID && printable(id)
}

// DecodeTranscriptGet reads a transcript_get strictly (no field it does not
// know), within its bounds.
func DecodeTranscriptGet(payload []byte) (TranscriptGet, error) {
	var m TranscriptGet
	if err := decodeStrict(payload, &m); err != nil {
		return m, fmt.Errorf("proto: bad transcript_get: %w", err)
	}
	if m.T != CtlTranscriptGet || !m.Valid() {
		return m, fmt.Errorf("proto: transcript_get past its bounds")
	}
	return m, nil
}

// DecodeInterrupt reads an interrupt strictly, within its bounds.
func DecodeInterrupt(payload []byte) (Interrupt, error) {
	var m Interrupt
	if err := decodeStrict(payload, &m); err != nil {
		return m, fmt.Errorf("proto: bad interrupt: %w", err)
	}
	if m.T != CtlInterrupt || !m.Valid() {
		return m, fmt.Errorf("proto: interrupt past its bounds")
	}
	return m, nil
}

// RequestIDOf is the reqId a chat request names, read leniently so a
// refusal can name it; "" when it names none within its bounds.
func RequestIDOf(payload []byte) string {
	var h struct {
		ReqID string `json:"reqId"`
	}
	if json.Unmarshal(payload, &h) != nil || !validReqID(h.ReqID) {
		return ""
	}
	return h.ReqID
}

// TranscriptEntryMsg is one entry, new or revised (owner -> client), as
// session.TranscriptEntry encodes it.
type TranscriptEntryMsg struct {
	T     string          `json:"t"`
	Entry json.RawMessage `json:"entry"`
}

// TranscriptPageMsg is a page of entries, oldest first: a reply to a
// transcript_get (ReqID), or, with no ReqID, part of the replay after a
// viewer's ready. First and Last are the first and last seq the transcript
// keeps now (0 when it keeps none). More says, of a read after a seq, that
// entries after the page's last are kept; of a read before one, that
// entries before the page's first were (kept or no longer); of the replay,
// that it did not send every change after the viewer's resume point: the
// viewer pages back with before.
type TranscriptPageMsg struct {
	T       string            `json:"t"`
	ReqID   string            `json:"reqId,omitempty"`
	Entries []json.RawMessage `json:"entries"`
	First   uint64            `json:"first"`
	Last    uint64            `json:"last"`
	More    bool              `json:"more,omitempty"`
}

// TranscriptPartMsg is a piece of a part (owner -> client), a reply to a
// transcript_get naming it: Data the bytes from Offset (base64, at most
// MaxTranscriptData), Kept how many the owner keeps of it (the next offset
// to read is Offset+len(Data), until Kept), Total its whole size as the
// agent sent it, Cut that the owner kept less than Total.
type TranscriptPartMsg struct {
	T      string `json:"t"`
	ReqID  string `json:"reqId"`
	Seq    uint64 `json:"seq"`
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Total  int64  `json:"total"`
	Kept   int64  `json:"kept"`
	Data   []byte `json:"data"`
	Cut    bool   `json:"cut,omitempty"`
}

// WelcomeTranscript is what a chat's welcome says of its transcript: the
// first and last seq it keeps (0 when none) and its mark, the count of its
// changes, which a viewer's resume point is.
type WelcomeTranscript struct {
	First uint64 `json:"first"`
	Last  uint64 `json:"last"`
	Mark  uint64 `json:"mark"`
}

// TranscriptPageRoom is what a page's entries may take of MaxTranscript, the
// commas between them included: MaxTranscript less the largest page with no
// entry (a reqId at its bound, first and last at theirs, more).
var TranscriptPageRoom = func() int {
	b, err := MarshalRaw(TranscriptPageMsg{T: TranscriptPageT, ReqID: string(bytes.Repeat([]byte("r"), MaxTranscriptReqID)),
		Entries: []json.RawMessage{}, First: ^uint64(0), Last: ^uint64(0), More: true})
	if err != nil {
		panic(err)
	}
	return MaxTranscript - len(b)
}()

// EncodeTranscript builds a TRANSCRIPT frame of v, encoded without HTML
// escaping; ErrTooLarge past MaxTranscript.
func EncodeTranscript(v any) ([]byte, error) {
	b, err := MarshalRaw(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxTranscript {
		return nil, fmt.Errorf("%w: a transcript frame of %d bytes", ErrTooLarge, len(b))
	}
	return Encode(TypeTranscript, b), nil
}

// MarshalRaw encodes v as JSON without HTML escaping and without the
// encoder's trailing newline.
func MarshalRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
