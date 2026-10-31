package proto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Control message discriminators (the "t" field).
const (
	// client -> owner
	CtlHello   = "hello"
	CtlResize  = "resize"
	CtlPing    = "ping"
	CtlFileGet = "file_get"
	CtlSubmit  = "submit"
	// CtlAnswer replies to the question a client was shown (Answer);
	// controllers only, and only to an owner whose welcome says Answer.
	CtlAnswer = "answer"
	// CtlChat is a post from a viewer and, owner -> client, one message of the chat (ChatPost, ChatMessage).
	CtlChat = "chat"
	// CtlChatSend types a kept chat message into the agent (ChatSend); controllers only.
	CtlChatSend = "chat_send"
	// CtlChatRoster lists the people on a run's chat (ChatRoster); owner -> client.
	CtlChatRoster = "chat_roster"

	// owner -> client
	CtlAttention   = "attention"
	CtlActivity    = "activity"
	CtlWelcome     = "welcome"
	CtlReady       = "ready"
	CtlStatus      = "status"
	CtlViewers     = "viewers"
	CtlError       = "error"
	CtlPong        = "pong"
	CtlChatHistory = "chat_history"
	// CtlAnswerOK says the owner took an answer (AnswerOK).
	CtlAnswerOK = "answer_ok"
	// CtlSubmitOK says the owner entered a submit that named a request id (SubmitOK, #322).
	CtlSubmitOK = "submit_ok"
)

// Error codes carried by CtlError.
const (
	ErrCodeReadOnly         = "read_only"
	ErrCodeSlowConsumer     = "slow_consumer"
	ErrCodeBadFrame         = "bad_frame"
	ErrCodeHelloTimeout     = "hello_timeout"
	ErrCodeRevoked          = "revoked"
	ErrCodeExpired          = "expired" // the viewer's link reached its expiry; closed with CloseForbidden as on a revoke
	ErrCodeSessionEnded     = "session_ended"
	ErrCodeRelayOff         = "relay_off" // a switchyard without a relay refused the viewer's relay request
	ErrCodeHostDisconnected = "host_disconnected"
	ErrCodeFileDenied       = "file_denied"
	ErrCodeTooManyRequests  = "too_many_requests"
	// ErrCodeNotSent answers a chat_send whose text could not be typed into
	// the member it names (a run's chat): the message says why.
	ErrCodeNotSent = "not_sent"
	// ErrCodeQuestionAnswered answers an answer whose question is no longer
	// the one waiting (answered by another reply, or gone): nothing was
	// written.
	ErrCodeQuestionAnswered = "question_answered"
)

// Transport labels reported in Welcome.
const (
	TransportWS     = "ws"
	TransportWebRTC = "webrtc"
	TransportRelay  = "relay"
)

// WebSocket close codes.
const (
	CloseProtocolError   = 4400
	CloseUnauthorized    = 4401
	CloseForbidden       = 4403
	CloseNotFound        = 4404
	CloseTooManyViewers  = 4409
	CloseSessionEnded    = 4410
	CloseServerShutdown  = 1001
	CloseNormal          = 1000
	MaxTerminalDimension = 500
)

// Header is the common discriminator of every control and signal message.
type Header struct {
	T string `json:"t"`
}

// MaxNameLen bounds a viewer display name in runes (see session.CleanName).
const MaxNameLen = 40

// MaxSessionName bounds a session's name in bytes: the name a launch gives
// it (POST /api/sessions) and the one a rename does (PATCH
// /api/sessions/{id}, session.CleanSessionName), which its Info and its
// `session` events carry.
const MaxSessionName = 120

// Hello is the first message a client sends.
type Hello struct {
	T      string `json:"t"`
	Proto  int    `json:"proto"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	Client string `json:"client,omitempty"`
	// Name is the display name other viewers see; a label, not authentication.
	Name string `json:"name,omitempty"`
	// ChatOnly asks for a quiet connection, for a run's chat alone: no
	// scrollback, output or files, not counted among the session's viewers
	// and no join line in its chat; it is on the run's roster. An older
	// owner ignores it and sends output, which the client drops.
	ChatOnly bool `json:"chatOnly,omitempty"`
}

// Resize is sent by controllers to change the PTY size and broadcast by the
// owner to every attached client. One viewer sizes the PTY (round 14, an
// owner whose welcome says Sizer): a controller's resize sizes it only while
// that controller is the sizer, except Take (Fit to my window), which makes
// it the sizer. The broadcast's By names the sizer; absent, nobody holds the
// size. An owner without the rule (an older host) takes every controller's
// resize, and By names whoever sent it.
type Resize struct {
	T    string `json:"t"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	By   string `json:"by,omitempty"`
	Take bool   `json:"take,omitempty"`
}

// Submit asks the owner to submit Text as a line, as a reply box does: the
// text, as a paste when the program asks for one, then Enter on its own after
// a pause (session.Local.Submit). Controllers only; Text is at most MaxSubmit
// bytes.
//
// With ReqID (#322; at most MaxAnswerReqID bytes of printable ASCII, as an
// answer's), the owner answers it: SubmitOK once the line is entered, or an
// ErrorMsg naming it (ErrCodeNotSent when UnlessWaiting held it back and
// the session was asking; input_failed when the line was typed and not
// entered; too_many_requests, session_ended, bad_frame). Without ReqID
// nothing is answered, as before. UnlessWaiting writes nothing while the
// session waits on a prompt, looked at as the owner holds the session's
// turn, so no prompt slips between the look and the write: what a line
// typed from the palette asks for (session.Submission.UnlessWaiting, the
// broadcast's guard). Sent only to an owner whose welcome says SubmitAck;
// an older one takes the line and answers nothing.
type Submit struct {
	T             string `json:"t"`
	Text          string `json:"text"`
	ReqID         string `json:"reqId,omitempty"`
	UnlessWaiting bool   `json:"unlessWaiting,omitempty"`
}

// MaxSubmit bounds the text of a submit message, in bytes: a reply box's line.
const MaxSubmit = 4096

// Valid reports whether m is within its bounds: Text at most MaxSubmit
// bytes and ReqID none or at most MaxAnswerReqID of printable ASCII.
func (m Submit) Valid() bool {
	return len(m.Text) <= MaxSubmit && ValidAnswerReqID(m.ReqID)
}

// SubmitOK says the owner entered the line of the submit RequestID (owner
// -> client, #322): its text was typed and its Enter written.
type SubmitOK struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
}

// Answer replies to the question the client was shown (client -> owner,
// controllers only), sent only to an owner whose welcome says Answer (an
// older one closes the connection on a message it does not know). Question
// is that question's id, the attention's ID as the client had it. Exactly
// one of Input and Text is set: Input is a choice's keys, written as INPUT
// is; Text a line, submitted as a submit's is. The owner writes nothing
// unless Question is still the one waiting as the keys (or the line's text)
// are written and no earlier write answered it; it answers ReqID, when
// given, with AnswerOK once they are written (a line once accepted), or
// with an ErrorMsg naming it (ErrCodeQuestionAnswered for a question no
// longer waiting).
type Answer struct {
	T        string `json:"t"`
	ReqID    string `json:"reqId,omitempty"`
	Question string `json:"question"`
	Input    string `json:"input,omitempty"`
	Text     string `json:"text,omitempty"`
}

// The bounds of an answer: its question's id and its request's, in bytes;
// its Input, a choice's keys (session.MaxOptionInput); its Text, MaxSubmit.
const (
	MaxQuestionID  = 32
	MaxAnswerReqID = 32
	MaxAnswerInput = 48
)

// Valid reports whether a is within its bounds: a question named, an id and
// a request id of printable ASCII within theirs, and exactly one of Input
// (at most MaxAnswerInput bytes) and Text (at most MaxSubmit).
func (a Answer) Valid() bool {
	if !ValidQuestionID(a.Question) || !ValidAnswerReqID(a.ReqID) {
		return false
	}
	if (a.Input == "") == (a.Text == "") {
		return false
	}
	return len(a.Input) <= MaxAnswerInput && len(a.Text) <= MaxSubmit
}

// ValidQuestionID reports whether id can be a question's id: 1 to
// MaxQuestionID bytes of printable ASCII.
func ValidQuestionID(id string) bool {
	return id != "" && len(id) <= MaxQuestionID && printable(id)
}

// ValidAnswerReqID reports whether id can be an answer's request id: none,
// or at most MaxAnswerReqID bytes of printable ASCII.
func ValidAnswerReqID(id string) bool {
	return len(id) <= MaxAnswerReqID && printable(id)
}

// printable reports whether s is printable ASCII, no space.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// AnswerOK says the owner took the answer RequestID (owner -> client): its
// keys are written, or its line accepted.
type AnswerOK struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
}

// Chat beside the terminal (docs/protocol.md, Chat): the people on a session
// talk to each other over the connection the terminal takes. A viewer posts
// with `chat`; the owner keeps the last session.MaxChat messages, sends each
// to every viewer as `chat`, and the kept ones to a new viewer as
// `chat_history`. `chat_send` types a kept message into the agent.
const (
	ChatScopeSession = "session"
	ChatScopeRun     = "run"

	ChatKindMessage     = "message"
	ChatKindSystem      = "system"
	ChatKindSentToAgent = "sent_to_agent"
	// ChatKindQuestion is the agent's question when its session needs
	// input: the attention's message as Text, its choices as Options, By the
	// agent (role "agent"). A system line with Event "answered" and Ref the
	// question follows the input that answered it.
	ChatKindQuestion = "question"
	// ChatRoleAgent is the By.Role of a question: the agent, no viewer.
	ChatRoleAgent = "agent"

	// ChatToAgent is the `to` of a post that is also typed into this session's agent (controllers only).
	ChatToAgent = "agent"

	// MaxChatText bounds a message's text in bytes, after cleaning. A frame of
	// one message always fits MaxControl: chat frames are encoded without
	// HTML escaping (MustControlRaw), so the worst case doubles it.
	MaxChatText = 2048
	// MaxChatNonce bounds a post's own id, echoed in the message so the sender knows it.
	MaxChatNonce = 32
	// ChatRatePerSecond and ChatBurst bound the posts of one connection.
	ChatRatePerSecond = 10
	ChatBurst         = 20
	// ChatReplayBytes bounds what a new viewer is sent of the kept chat: the newest messages that fit.
	ChatReplayBytes = 128 << 10
	// MaxChatRoster bounds the people a chat_roster lists (a full list of the
	// longest names fits MaxControl); Count says how many there are.
	MaxChatRoster = 32
)

// ChatPost is a viewer's message (client -> owner). Scope is "session" when
// empty. To is "agent" for a controller's post that is also typed into the
// agent, or a member's name in a run's chat. On is the member the sender is
// looking at (a run's chat). Nonce is the client's own id of the post.
type ChatPost struct {
	T     string `json:"t"`
	Nonce string `json:"nonce,omitempty"`
	Scope string `json:"scope,omitempty"`
	Text  string `json:"text"`
	On    string `json:"on,omitempty"`
	To    string `json:"to,omitempty"`
	// Quote is lines of a file the message is about (design round 12, F7):
	// a comment on a line, or a question to the agent about it.
	Quote *ChatQuote `json:"quote,omitempty"`
}

// ChatQuote is the lines From..To (1-based) of the file at Path as the
// person saw them, at most MaxQuoteLines of at most MaxQuoteLine bytes; Cut
// says lines were left out to keep the message in one frame.
type ChatQuote struct {
	Path  string   `json:"path"`
	From  int      `json:"from"`
	To    int      `json:"to"`
	Lines []string `json:"lines"`
	Cut   bool     `json:"cut,omitempty"`
}

// The bounds of a quote.
const (
	MaxQuoteLines = 12
	MaxQuoteLine  = 200
	MaxQuotePath  = 512
)

// ChatSend asks the owner to type the text of the kept message Ref into the
// agent (client -> owner, controllers only), or into member To of a run.
type ChatSend struct {
	T     string `json:"t"`
	Ref   string `json:"ref"`
	Scope string `json:"scope,omitempty"`
	To    string `json:"to,omitempty"`
}

// ChatBy is who a chat message is from: the viewer's subscription id, name and role.
type ChatBy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// ChatMessage is one message of a chat (owner -> client). Kind "message"
// carries Text; "system" carries Event (join, leave, or answered with Ref the
// question) about By; "sent_to_agent" marks that By typed the message Ref into
// the agent (To names a run's member); "question" is the agent's (By.Role
// "agent"), its Text the question and Options its choices, On the member in a
// run's chat.
type ChatMessage struct {
	T       string            `json:"t"`
	ID      string            `json:"id"`
	At      string            `json:"at"`
	Scope   string            `json:"scope"`
	Kind    string            `json:"kind"`
	By      ChatBy            `json:"by"`
	Text    string            `json:"text,omitempty"`
	Ref     string            `json:"ref,omitempty"`
	To      string            `json:"to,omitempty"`
	On      string            `json:"on,omitempty"`
	Event   string            `json:"event,omitempty"`
	Nonce   string            `json:"nonce,omitempty"`
	Options []AttentionOption `json:"options,omitempty"`
	Quote   *ChatQuote        `json:"quote,omitempty"`
}

// ChatHistory replays the kept messages to a new viewer, oldest first, in
// frames of at most MaxControl; More says another frame follows.
type ChatHistory struct {
	T        string        `json:"t"`
	Scope    string        `json:"scope"`
	Messages []ChatMessage `json:"messages"`
	More     bool          `json:"more,omitempty"`
}

// ChatPerson is one person on a run's chat: a viewer of any of its members,
// one row per name. On is the member they look at, when they look at one.
type ChatPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	On   string `json:"on,omitempty"`
}

// ChatRoster lists who is on a run's chat (owner -> client), sent to every
// member's viewers whenever any member's roster changes: Count is how many,
// List the first MaxChatRoster of them.
type ChatRoster struct {
	T     string       `json:"t"`
	Scope string       `json:"scope"`
	Count int          `json:"count"`
	List  []ChatPerson `json:"list"`
}

// Ping and Pong carry an opaque client timestamp.
type Ping struct {
	T  string `json:"t"`
	TS int64  `json:"ts"`
}

// FileGet asks the owner to read a file or directory relative to the session cwd.
type FileGet struct {
	T     string `json:"t"`
	ReqID string `json:"reqId"`
	Path  string `json:"path"`
	Stat  bool   `json:"stat,omitempty"`
	// Op asks for git instead of a read (design 4d, 4e): `status`, the
	// working directory's changes against Base (HEAD when empty), answered
	// with a FILE frame of kind `status`; `show`, Path at revision Rev,
	// answered with kind `show` and the content as the body; `log`, the
	// commits on HEAD after Base (a run's base) or since the session
	// started, answered with kind `log`; `commit`, commit Rev's files
	// against its first parent, answered with kind `commit`. All go through
	// the session's file policy and deny list as a read does.
	Op   string `json:"op,omitempty"`
	Base string `json:"base,omitempty"`
	Rev  string `json:"rev,omitempty"`
}

// The git operations a FileGet may ask for.
const (
	FileOpStatus = "status"
	FileOpShow   = "show"
	FileOpLog    = "log"
	FileOpCommit = "commit"
	// FileOpFind finds files whose name holds Path (the Explorer's filter)
	// anywhere under the working directory, answered with kind `find`.
	FileOpFind = "find"
	// FileOpTouched lists the files the agent touched since the session
	// started, from the session's own index (no walk, no git), answered
	// with kind `touched`.
	FileOpTouched = "touched"
	// FileOpPlans lists the agent's plans outside the working directory
	// (#289: the files it reported in its plans directory, which it may
	// read and save as the folder's files), answered with kind `plans`:
	// Path the plans directory, Entries the plans by their paths from it.
	FileOpPlans = "plans"
)

// MaxCwd bounds a session's working directory where the protocol carries
// it: HostSession.Cwd and Welcome.Cwd.
const MaxCwd = 4096

// MaxPlanPath bounds a plan's path where the protocol carries it
// (Attention.Plan, HostAttentionMsg.Plan, and the attention route's
// `plan`): a path, as the agent names its files (session.MaxEventPath).
const MaxPlanPath = 1024

// Welcome is the owner's first message after hello (or before signaling for hosted sessions).
type Welcome struct {
	T            string `json:"t"`
	Proto        int    `json:"proto"`
	SessionID    string `json:"sessionId"`
	Role         string `json:"role"`
	SubscriberID string `json:"subscriberId,omitempty"`
	ViewerID     string `json:"viewerId,omitempty"`
	Cols         uint16 `json:"cols"`
	Rows         uint16 `json:"rows"`
	// Sizer says the owner sizes the PTY by one viewer (round 14, see
	// Resize), and SizedBy which viewer that is now ("" none).
	Sizer           bool   `json:"sizer,omitempty"`
	SizedBy         string `json:"sizedBy,omitempty"`
	Status          string `json:"status"`
	ScrollbackBytes int    `json:"scrollbackBytes"`
	Transport       string `json:"transport"`
	FileView        bool   `json:"fileView"`
	// FileEdit says this connection may edit files (control, on a session
	// whose fileEdit setting allows it); Nvim that the machine has `nvim`,
	// so nvim_open may be sent (design round 12, F8).
	FileEdit bool `json:"fileEdit,omitempty"`
	Nvim     bool `json:"nvim,omitempty"`
	// NvimOwnerOnly says this connection may edit but not through Neovim,
	// which is kept for the owner's own connections: a link's controller
	// edits with the page's keys (Nvim is then false).
	NvimOwnerOnly bool `json:"nvimOwnerOnly,omitempty"`
	// NvimSettled, with Nvim, says the owner's Neovim says when keys are
	// done (an nvim_event of kind settled): a page waits for that word. An
	// older owner sends neither, and a page takes a cursor's
	// acknowledgement for it, as it did.
	NvimSettled bool `json:"nvimSettled,omitempty"`
	// Chat says the owner takes chat and chat_send: a client sends neither
	// to an owner whose welcome lacks it (an older server closes on them).
	Chat bool `json:"chat,omitempty"`
	// RunChat says the session is a run's member with a run chat: `chat` and
	// `chat_send` take scope `run`, and `chat_roster` arrives.
	RunChat bool `json:"runChat,omitempty"`
	// Answer says the owner takes `answer` and refuses one whose question
	// is no longer the one waiting: a client sends none to an owner whose
	// welcome lacks it, and replies with INPUT and `submit` as before.
	Answer bool `json:"answer,omitempty"`
	// SubmitAck says the owner answers a submit that names a reqId with
	// submit_ok or an error naming it, and takes unlessWaiting (#322):
	// absent from an older owner, which types the line and says nothing.
	SubmitAck bool `json:"submitAck,omitempty"`
	// Cwd is the session's working directory on the machine that runs it,
	// from the owner itself, to a viewer whose role may read files: the root
	// of a guest's Files pane. A switchyard lists a published session with
	// its folder's last element alone. Absent past MaxCwd bytes as encoded
	// (escapes included), to a role that may read no file, and from an older
	// owner.
	Cwd            string      `json:"cwd,omitempty"`
	ICEServers     []ICEServer `json:"iceServers,omitempty"`
	RelayTimeoutMs int         `json:"relayTimeoutMs,omitempty"`
	RelayOnly      bool        `json:"relayOnly,omitempty"`
}

// ICEServer is the WebRTC ICE server description sent to clients.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Status reports session lifecycle changes.
type Status struct {
	T        string `json:"t"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

// ViewerInfo describes one attached client in the viewers roster.
type ViewerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// Link is the label of the share link the viewer joined through, if any.
	Link  string `json:"link,omitempty"`
	Since string `json:"since"`
	// LastInputAt is refreshed at most every 2 s while the viewer types.
	LastInputAt string `json:"lastInputAt,omitempty"`
	// Quiet marks a connection for a run's chat alone (hello.chatOnly): it
	// is left out of a session's `viewers` and listed on the run's roster.
	Quiet bool `json:"quiet,omitempty"`
}

// Viewers reports the attached clients: the count and the full roster.
type Viewers struct {
	T     string       `json:"t"`
	Count int          `json:"count"`
	List  []ViewerInfo `json:"list,omitempty"`
}

// ErrorMsg is a non-fatal or fatal error notification.
type ErrorMsg struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// RequestID names the request an error answers: a host's link request
	// (host control connection); a viewer's chat post (its nonce),
	// chat_send (its ref) or answer (its reqId). Empty otherwise.
	RequestID string `json:"requestId,omitempty"`
}

// ErrorInfo is the nested error object used inside other messages.
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AttentionOption is a quick-reply choice; Input is sent verbatim as INPUT.
// Mode marks a choice that changes the session's permission mode: a page
// gives it no number key and never offers it first (docs/protocol.md,
// Attention).
type AttentionOption struct {
	Label string `json:"label"`
	Input string `json:"input"`
	Mode  bool   `json:"mode,omitempty"`
}

// Attention reports whether the agent is waiting for a human. Kind and
// Options describe the prompt's shape (see docs/protocol.md, Attention). ID
// names this change of attention (a question), new with each: an answer
// carries it (Answer.Question). Absent with the empty state and from an
// older owner.
type Attention struct {
	T       string            `json:"t"`
	State   string            `json:"state"`
	Message string            `json:"message,omitempty"`
	Source  string            `json:"source,omitempty"`
	Kind    string            `json:"kind,omitempty"`
	Options []AttentionOption `json:"options,omitempty"`
	ID      string            `json:"id,omitempty"`
	// Answered is "terminal" for a question that keys typed in the
	// session's terminal have answered, shown still while the program's
	// dialog stays on the screen: no client offers it (docs/protocol.md,
	// Attention).
	Answered string `json:"answered,omitempty"`
	// Plan is the path of the plan the question asks approval for (Claude
	// Code's ExitPlanMode, #289), at most MaxPlanPath bytes, which the
	// session reads and saves as the folder's files (a `file_get` of it):
	// a page offers to open it. Absent from any other question, and from
	// an older owner.
	Plan string `json:"plan,omitempty"`
}

// Activity is one entry of the session activity log (owner -> client). The
// last 50 entries are replayed after ready; new ones follow live. URL, To and
// Tool belong to the event types (docs/protocol.md, Events). The owner bounds
// the text fields (session.CleanEntry) so that the message fits MaxControl.
type Activity struct {
	T       string `json:"t"`
	At      string `json:"at"`
	Type    string `json:"type"`
	By      string `json:"by,omitempty"`
	ByName  string `json:"byName,omitempty"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
	To      string `json:"to,omitempty"`
	Tool    string `json:"tool,omitempty"`
	// A `file` event: what the agent did (read, edit, write, delete) and to which path.
	Op   string `json:"op,omitempty"`
	Path string `json:"path,omitempty"`
}

// Simple is a message with only a discriminator (ready).
type Simple struct {
	T string `json:"t"`
}

// ErrUnknownControl is returned for an unrecognised control discriminator.
var ErrUnknownControl = errors.New("proto: unknown control message")

// ParseHeader extracts the discriminator from a JSON payload.
func ParseHeader(payload []byte) (string, error) {
	var h Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return "", fmt.Errorf("proto: bad control json: %w", err)
	}
	if h.T == "" {
		return "", errors.New("proto: control message missing t")
	}
	return h.T, nil
}

// ValidDimension reports whether a terminal dimension is acceptable.
func ValidDimension(v uint16) bool {
	return v >= 1 && v <= MaxTerminalDimension
}

// HelloSize reports whether a hello's size asks for a size: two dimensions in
// 1–MaxTerminalDimension do, and a controller's hello sets the session to it
// (latest controller wins). (0, 0) is the hello of a viewer that shows the
// session at the session's size, a scaled tile or a quick reply: it follows
// the current size and changes nothing, and so does any other pair (a zero
// with a nonzero, a dimension over 500), as such a hello always did. A resize
// message has no such case: there zero is invalid. The size never decides the
// role, which comes from the token on both transports.
func HelloSize(cols, rows uint16) bool {
	return ValidDimension(cols) && ValidDimension(rows)
}

// NewError builds an encoded error control frame.
func NewError(code, message string) []byte {
	return MustControl(ErrorMsg{T: CtlError, Code: code, Message: message})
}
