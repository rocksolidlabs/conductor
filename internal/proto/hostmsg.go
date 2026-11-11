package proto

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Host control messages are JSON text frames on /ws/host. Relayed terminal
// frames travel as binary TypeRelay envelopes on the same connection.
const (
	// host -> server
	HostRegister    = "register"
	HostStatus      = "status"
	HostResize      = "resize"
	HostAnswer      = "answer"
	HostICE         = "ice"
	HostViewerError = "viewer_error"
	HostViewerClose = "viewer_closed"
	// HostChat carries a chat message the host's session kept (HostChatMsg):
	// host -> server, for the browsers' unread counts on the admin stream.
	HostChat = "chat"
	// both directions
	HostAttention = "attention"
	HostActivity  = "activity"
	// HostLink asks the server for a share link to the session (a host
	// behind a rendezvous mints its links there); HostLinkCreated answers.
	HostLink        = "link"
	HostLinkCreated = "link_created"
	// HostLinkRevoke asks the server to revoke a link it minted for the
	// session; HostLinkRevoked answers, error not_found when it has none.
	HostLinkRevoke  = "link_revoke"
	HostLinkRevoked = "link_revoked"
	// HostRunLink asks the server for one link to several of the host's
	// sessions, the members of a crew run (answered by link_created with
	// runId); HostRunLinkUpdate tells it the run's members now, answered by
	// HostRunLinkUpdated.
	HostRunLink        = "link_run"
	HostRunLinkUpdate  = "link_run_update"
	HostRunLinkUpdated = "link_run_updated"
	// HostRegisterDesk is the first message of a run's desk's connection
	// (RegisterDesk), in place of register.
	HostRegisterDesk = "register_desk"

	// server -> host
	HostRegistered  = "registered"
	HostViewerJoin  = "viewer_join"
	HostOffer       = "offer"
	HostRelayStart  = "relay_start"
	HostViewerLeave = "viewer_leave"
	HostStop        = "stop"
	HostError       = "error"
	// HostLinkGone tells the host a link a viewer joined through no longer
	// opens the session (LinkGone).
	HostLinkGone = "link_gone"
	// HostViewerBehind tells the host a relayed viewer fell behind what the
	// server relays to it, or caught up (ViewerBehind).
	HostViewerBehind = "viewer_behind"
	// HostAnswerAs carries an answer a decision router or a provider holds
	// for the question the host's session shows (HostAnswerAsMsg), only to
	// a host whose register said AnswerAs.
	HostAnswerAs = "answer_as"
)

// HostInfo describes the host process.
type HostInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// User is the OS user running the host (≤ 64 bytes), for "hosted by".
	User string `json:"user,omitempty"`
	// Instance is a secret of the host process (≤ MaxHostInstance bytes):
	// with HostSession.LocalID it fixes the session's id on the server
	// (signal.HostedID), so the same session has the same id after the
	// server restarts and a link minted before still opens it. Only its
	// holder can register under that id. Never logged.
	Instance string `json:"instance,omitempty"`
	// AnswerAs says the host takes answer_as (HostAnswerAsMsg) and sends
	// kept with its attention (HostAttentionMsg.Kept). A host publishing to
	// a switchyard never says it; an older server ignores it.
	AnswerAs bool `json:"answerAs,omitempty"`
}

// MaxHostUser bounds HostInfo.User; MaxHostInstance and MaxLocalID bound
// HostInfo.Instance and HostSession.LocalID.
const (
	MaxHostUser     = 64
	MaxHostInstance = 64
	MaxLocalID      = 64
)

// HostSession describes the session a host offers.
type HostSession struct {
	Name    string   `json:"name"`
	AgentID string   `json:"agentId"`
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
	Cols    uint16   `json:"cols"`
	Rows    uint16   `json:"rows"`
	// RelayOnly tells viewers to skip WebRTC and use the server relay.
	RelayOnly bool `json:"relayOnly,omitempty"`
	// AgentToken authorizes the hosted agent to report attention to the server.
	AgentToken string `json:"agentToken,omitempty"`
	// Branch is the git branch of Cwd when known (≤ 200 bytes).
	Branch string `json:"branch,omitempty"`
	// LocalID is the session's id on the host; with HostInfo.Instance,
	// both or neither.
	LocalID string `json:"localId,omitempty"`
}

// HostResume lets a reconnecting host reclaim its session.
type HostResume struct {
	SessionID string `json:"sessionId"`
	Secret    string `json:"secret"`
}

// Register is the host's first message.
type Register struct {
	T       string      `json:"t"`
	Proto   int         `json:"proto"`
	Host    HostInfo    `json:"host"`
	Session HostSession `json:"session"`
	Resume  *HostResume `json:"resume,omitempty"`
}

// Registered acknowledges a registration.
type Registered struct {
	T            string      `json:"t"`
	SessionID    string      `json:"sessionId"`
	Secret       string      `json:"secret"`
	ShareBaseURL string      `json:"shareBaseUrl"`
	Resumed      bool        `json:"resumed"`
	ICEServers   []ICEServer `json:"iceServers,omitempty"`
	// Links are the ids of the live links the server holds for the
	// session, so a host registering again after the server restarted
	// knows which of the links it minted still open it. Never omitted: a
	// server without links sends []; an older server sends no field (nil
	// to the host, which says nothing).
	Links []string `json:"links"`
	// Recipe is what the server's catalog says to watch on the screen of
	// the agent the host registered (HostSession.AgentID), so that a hosted
	// session watches what a server session of the same agent watches
	// (#293). Absent when the catalog has no such agent, when it has
	// nothing to watch, from an older server, when its encoding is past
	// MaxHostRecipe, and when registered with it would be past
	// MaxHostMessage; a host that is not given one, or is given one it
	// refuses (HostRecipe.Check), works as it did without.
	Recipe *HostRecipe `json:"recipe,omitempty"`
	// Decisions says the switchyard carries a decision link's lane
	// (docs/protocol.md, Decision links): it relays TypeDecision and
	// TypeDecide, keeps a run's desk (RegisterDesk) and serves the join
	// page's Decide tab. Only then does a publisher mint a decision link
	// there; absent from an older switchyard and from one built without
	// the tab, at which the publisher refuses one (switchyard_too_old).
	Decisions bool `json:"decisions,omitempty"`
}

// RegisterDesk is the first message of a run's desk's connection, in
// place of register (docs/plans/decision-inbox.md, PR-6): one more host
// connection of the publisher's, for a run with a live decision link at
// the switchyard, with no PTY and no terminal frames, only the lane, so a
// run's decisions are reached with no member alive. It is the
// publisher's by the identity a hosted session has: Host is the uplink's
// HostInfo, whose Instance it requires, and LocalID is DeskLocalID of the
// run, so the desk's id is the same after its connection drops (Resume,
// with the secret its registered gave) and after the switchyard
// restarts (a fresh register_desk). An older switchyard closes the
// connection: its first message must be a register.
type RegisterDesk struct {
	T       string      `json:"t"`
	Proto   int         `json:"proto"`
	Host    HostInfo    `json:"host"`
	LocalID string      `json:"localId"`
	Run     DeskRun     `json:"run"`
	Resume  *HostResume `json:"resume,omitempty"`
}

// DeskRun is the run a desk carries: its id on the publisher (opaque to
// the switchyard) and its name, within a run group's bounds.
type DeskRun struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DeskLocalID is the local id of the desk of the run runID: "desk:" and
// the run's id.
func DeskLocalID(runID string) string { return "desk:" + runID }

// Validate checks a register_desk: its type and protocol, the host's
// bounds with an instance (ErrNoInstance without one), a run with an id
// and a name within a run group's bounds, and the local id the run's
// (DeskLocalID), so one run has one desk per publisher.
func (m RegisterDesk) Validate() error {
	if m.T != HostRegisterDesk || m.Proto != ProtoVersion {
		return errors.New("register_desk: type or protocol")
	}
	if m.Host.Instance == "" {
		return ErrNoInstance
	}
	if len(m.Host.Name) > 120 || len(m.Host.User) > MaxHostUser || len(m.Host.Instance) > MaxHostInstance {
		return errors.New("register_desk: host out of bounds")
	}
	if m.Run.ID == "" || len(m.Run.ID) > MaxRunID || m.Run.Name == "" || len(m.Run.Name) > MaxRunName {
		return errors.New("register_desk: run id and name required, within bounds")
	}
	if m.LocalID != DeskLocalID(m.Run.ID) {
		return errors.New("register_desk: localId is desk: and the run's id")
	}
	if m.Resume != nil && (m.Resume.SessionID == "" || len(m.Resume.SessionID) > MaxDeskID || m.Resume.Secret == "" || len(m.Resume.Secret) > 128) {
		return errors.New("register_desk: resume out of bounds")
	}
	return nil
}

// ErrNoInstance refuses a register_desk without a host instance: a desk
// is its publisher's by that instance (no_instance, as a run link is).
var ErrNoInstance = errors.New("register_desk: a desk needs a host that registers with an instance")

// MaxDeskID bounds a desk's id where a message carries it (RunGroup.Desk,
// a resume): a hosted session's id.
const MaxDeskID = 64

// HostRecipe is the part of an agent's catalog entry a session watches its
// screen by (#293): the trust question and its answers, the other
// questions, startup and anytime, the window title that says the agent
// works, and whether the agent reports taking a prompt. The patterns are
// the catalog's, RE2 source; the host compiles them as the server does.
// ConfirmSubmit is set on the hosted session as on a server one, but only
// a submission that asks for the second Enter gets it (a crew's prompt,
// session.Submission.Confirm), and no submission on a host asks for one
// yet: hosted crews are to come.
type HostRecipe struct {
	TrustPrompt   string           `json:"trustPrompt,omitempty"`
	TrustAnswers  []RecipeAnswer   `json:"trustAnswers,omitempty"`
	Questions     []RecipeQuestion `json:"questions,omitempty"`
	WorkingTitle  string           `json:"workingTitle,omitempty"`
	ConfirmSubmit bool             `json:"confirmSubmit,omitempty"`
}

// RecipeAnswer is one of a question's answers: its label and the keys that
// pick it.
type RecipeAnswer struct {
	Label string `json:"label"`
	Input string `json:"input"`
}

// RecipeQuestion is one of the agent's questions, as the catalog has it
// (catalog.Question).
type RecipeQuestion struct {
	Prompt            string         `json:"prompt"`
	Words             string         `json:"words,omitempty"`
	Anytime           bool           `json:"anytime,omitempty"`
	Answers           []RecipeAnswer `json:"answers,omitempty"`
	AnswersFromScreen bool           `json:"answersFromScreen,omitempty"`
	ModeAnswers       string         `json:"modeAnswers,omitempty"`
	TypedAnswers      string         `json:"typedAnswers,omitempty"`
}

// Bounds of a HostRecipe: the catalog's own (catalog.MaxQuestions,
// MaxTrustAnswers and the rest, a pattern's 200 bytes), and MaxHostRecipe
// for its encoding. Beside the ICE servers and the links, which have no
// bound of their own here, a recipe could still take registered past
// MaxHostMessage: the server then sends it without the recipe.
const (
	MaxRecipeQuestions   = 8
	MaxRecipeAnswers     = 6
	MaxRecipeAnswerLabel = 60 // runes
	MaxRecipeAnswerInput = 48 // bytes
	MaxRecipePattern     = 200
	MaxHostRecipe        = 24 << 10
)

// Check reports whether r is within its bounds: at most MaxRecipeQuestions
// questions, each with a prompt; at most MaxRecipeAnswers answers to the
// trust question and to each question, each with a label (at most
// MaxRecipeAnswerLabel runes once trimmed, as the catalog counts) and
// keys; every pattern at most MaxRecipePattern bytes. Whether a pattern
// compiles is the host's to find, as the server's is when it starts a
// session.
func (r HostRecipe) Check() error {
	if len(r.TrustPrompt) > MaxRecipePattern || len(r.WorkingTitle) > MaxRecipePattern {
		return errors.New("recipe: a pattern past its bound")
	}
	if err := checkRecipeAnswers(r.TrustAnswers); err != nil {
		return fmt.Errorf("recipe: trust answers: %w", err)
	}
	if len(r.Questions) > MaxRecipeQuestions {
		return fmt.Errorf("recipe: more than %d questions", MaxRecipeQuestions)
	}
	for i, q := range r.Questions {
		if q.Prompt == "" {
			return fmt.Errorf("recipe: question %d has no prompt", i+1)
		}
		for _, p := range []string{q.Prompt, q.Words, q.ModeAnswers, q.TypedAnswers} {
			if len(p) > MaxRecipePattern {
				return fmt.Errorf("recipe: question %d: a pattern past its bound", i+1)
			}
		}
		if err := checkRecipeAnswers(q.Answers); err != nil {
			return fmt.Errorf("recipe: question %d: %w", i+1, err)
		}
	}
	return nil
}

func checkRecipeAnswers(as []RecipeAnswer) error {
	if len(as) > MaxRecipeAnswers {
		return fmt.Errorf("more than %d answers", MaxRecipeAnswers)
	}
	for i, a := range as {
		// The label is counted as the catalog counts it, trimmed: a label
		// the catalog takes (padded, at its bound) is one Check takes, or a
		// hosted session would lose its whole recipe over it.
		label := strings.TrimSpace(a.Label)
		if label == "" || utf8.RuneCountInString(label) > MaxRecipeAnswerLabel || !utf8.ValidString(a.Label) {
			return fmt.Errorf("answer %d: a label of 1 to %d characters", i+1, MaxRecipeAnswerLabel)
		}
		if a.Input == "" || len(a.Input) > MaxRecipeAnswerInput {
			return fmt.Errorf("answer %d: keys of 1 to %d bytes", i+1, MaxRecipeAnswerInput)
		}
	}
	return nil
}

// HostStatusMsg reports the hosted process state.
type HostStatusMsg struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exitCode,omitempty"`
}

// HostResizeMsg reports the current PTY size for listings.
type HostResizeMsg struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// ViewerJoin tells the host a viewer has been authenticated.
type ViewerJoin struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
	Role     string `json:"role"`
	LinkID   string `json:"linkId,omitempty"`
	// LinkLabel is shown in the viewers roster; the host never sees the token.
	LinkLabel string `json:"linkLabel,omitempty"`
	// Principal says the viewer came as an identity provider's principal,
	// a credential that is not a link and that the server may revoke at
	// any time (its viewer_leave then says revoked): the host holds what
	// the viewer has under way to its connection to the server, as it does
	// a link's viewer's, withdrawn when the connection is lost and acted on
	// only while the host vouches for its server. An older host ignores it.
	Principal bool `json:"principal,omitempty"`
}

// LinkGone tells the host that a link through which a viewer of its
// session joined (ViewerJoin.LinkID) no longer opens the session: revoked,
// expired (on the server's clock), its run forgotten, its run's session no
// longer named. It comes whether or not a viewer is still attached through
// it, ahead of the leaves of those that are, so that what a viewer of the
// link asked for and that is still under way (one that left once it had
// sent) is withdrawn. A switchyard also sends it for a link it ends of
// itself at an open admission past its bound, whether or not a viewer
// joined through it, to the connection that holds the link (its
// session's, or its run's desk's): on the connection that asked for the
// link, after its link_created; to each member's connection of a run link
// whose run's desk was not told (none, away or gone). Such a one says Ended: the link itself opens
// nothing any more, so its publisher stops listing it; without Ended (an
// older switchyard's, every other end) a member's connection hears only
// that its session is no longer opened. LinkID is at most MaxLinkID bytes;
// the message is a host message (MaxHostMessage); an older host ignores it.
type LinkGone struct {
	T      string `json:"t"`
	LinkID string `json:"linkId"`
	Ended  bool   `json:"ended,omitempty"`
}

// ViewerRef addresses one viewer (relay_start, viewer_closed; a
// viewer_leave without a reason).
type ViewerRef struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
}

// ViewerBehind tells the host whether the server holds more of what it
// relayed to a viewer, unwritten to the viewer, than a viewer that keeps up
// would (Behind), or the viewer caught up (Behind false): it comes as the
// viewer passes either mark, never twice the same in a row. A host gives
// the frames it relays to a viewer behind no turn on the connection while
// another viewer's frame waits for one, so a viewer that reads slowly, or
// not at all, never takes the connection from those that keep up. An older
// host ignores the message, and an older server never sends it.
type ViewerBehind struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
	Behind   bool   `json:"behind"`
}

// ViewerLeave tells the host a viewer left, or was let go. Reason says the
// viewer's link no longer opens the session (LeaveRevoked: revoked, its
// run's link forgotten, gone; LeaveExpired), or the identity provider no
// longer admits the principal the viewer came as (LeaveRevoked), and is
// empty for any other
// end (the viewer left, its connection failed, it was too slow). A host
// takes a reason it does not know, or one over MaxLeaveReason bytes, as
// none; an older server sends none, and an older host ignores the field.
type ViewerLeave struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
	Reason   string `json:"reason,omitempty"`
}

// The reasons a viewer_leave gives, and their bound.
const (
	LeaveRevoked   = "revoked"
	LeaveExpired   = "expired"
	MaxLeaveReason = 16
)

// LeaveReason is the reason a host acts on for a viewer_leave's reason r:
// r when it is one of the reasons above, else "" (none).
func LeaveReason(r string) string {
	if len(r) > MaxLeaveReason {
		return ""
	}
	switch r {
	case LeaveRevoked, LeaveExpired:
		return r
	}
	return ""
}

// ViewerSDP carries an offer or answer for one viewer.
type ViewerSDP struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
	SDP      string `json:"sdp"`
}

// ViewerICE carries one candidate for one viewer.
type ViewerICE struct {
	T         string       `json:"t"`
	ViewerID  string       `json:"viewerId"`
	Candidate ICECandidate `json:"candidate"`
}

// ViewerError reports a per-viewer failure from the host.
type ViewerError struct {
	T        string `json:"t"`
	ViewerID string `json:"viewerId"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// HostAttentionMsg carries an attention change between host and server.
// SessionID and Source are set by the host; the server omits them. ID goes
// host to server only: the id the host's session gave the change
// (Attention.ID), which the server keeps for its listings, so that an
// answer from them names the question the host holds; the server keeps it
// only when it is a question's id (ValidQuestionID), and an older server
// ignores it. Ask goes both ways, with needs_input only: the agent's own
// question (an attention report with ask), which a session keeps through
// its turn's done. Server to host, a report that says so, which the host's
// session applies as the agent's question; host to server, the question
// the host's session shows is one, so that the server's copy keeps it
// through a done it forwards. An older host or server ignores it, and the
// question shows there as any other.
type HostAttentionMsg struct {
	T         string            `json:"t"`
	SessionID string            `json:"sessionId,omitempty"`
	State     string            `json:"state"`
	Message   string            `json:"message,omitempty"`
	Source    string            `json:"source,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Options   []AttentionOption `json:"options,omitempty"`
	ID        string            `json:"id,omitempty"`
	Ask       bool              `json:"ask,omitempty"`
	// Answered is the host's Attention.Answered: "terminal" for a question
	// answered by keys typed in its terminal (Attention).
	Answered string `json:"answered,omitempty"`
	// Plan is the host's Attention.Plan: the plan a question asks approval
	// for, at most MaxPlanPath bytes (#289).
	Plan string `json:"plan,omitempty"`
	// Kept goes host to server only, from a host whose register said
	// AnswerAs: the ids of the questions its session keeps without showing
	// them (session.Info.Kept: the agent's own question set aside, a
	// question held over by a held turn), from the same Info as the
	// message's attention, so that the server's decision router tells a
	// question set aside from one that ended. At most MaxKept ids, each
	// ValidQuestionID (KeptValid); an older server ignores it.
	Kept []string `json:"kept,omitempty"`
}

// MaxKept bounds HostAttentionMsg.Kept (session.MaxKept).
const MaxKept = 2

// KeptValid reports whether m's Kept is within its bounds: at most MaxKept
// ids, each a question's (ValidQuestionID). A message out of them is
// dropped whole.
func (m HostAttentionMsg) KeptValid() bool {
	if len(m.Kept) > MaxKept {
		return false
	}
	for _, id := range m.Kept {
		if !ValidQuestionID(id) {
			return false
		}
	}
	return true
}

// HostAnswerAsMsg (server -> host) is an answer a decision router or a
// provider holds for the question the host's session shows: the host
// applies it as its own viewers' answers are applied (Local.AnswerAs for
// Input, Local.Submit with Question for Text), once, under the question's
// claim, the first answer winning; nothing else types into the session
// for it. Question is that question's id; exactly one of Input (a
// choice's keys, which the host writes only when they are one of the
// question's own choices) and Text (a line); ByName and BySubject say who
// answered, recorded as the answer's name: who answered, never a right to
// answer. Sent only to a host whose register said AnswerAs, and never by a
// switchyard; a host in switchyard mode drops one that arrives anyway,
// and an older host ignores a type it does not know. At its largest (every
// field at its bound, the text's bytes each escaped) the message is about
// 25 KB, under MaxHostMessage; a plain line's is about 4.4 KB.
type HostAnswerAsMsg struct {
	T         string `json:"t"`
	Question  string `json:"question"`
	Input     string `json:"input,omitempty"`
	Text      string `json:"text,omitempty"`
	ByName    string `json:"byName"`
	BySubject string `json:"bySubject,omitempty"`
}

// MaxAnswerAsSubject bounds HostAnswerAsMsg.BySubject, in bytes: a
// principal's subject (ext.MaxSubject).
const MaxAnswerAsSubject = 48

// Valid reports whether m is within its bounds: a question's id
// (ValidQuestionID); exactly one of Input (at most MaxAnswerInput bytes)
// and Text (at most MaxSubmit); ByName 1 to MaxNameLen characters of one
// line of UTF-8 text; BySubject none or at most MaxAnswerAsSubject bytes of
// printable ASCII, no space.
func (m HostAnswerAsMsg) Valid() bool {
	if !ValidQuestionID(m.Question) || (m.Input == "") == (m.Text == "") {
		return false
	}
	if len(m.Input) > MaxAnswerInput || len(m.Text) > MaxSubmit {
		return false
	}
	if len(m.BySubject) > MaxAnswerAsSubject || !printable(m.BySubject) {
		return false
	}
	return oneLineName(m.ByName, MaxNameLen)
}

// oneLineName reports whether s is 1 to max characters of UTF-8 text with
// no control character: a display name, one line (as ext's Principal.Name).
func oneLineName(s string, max int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// HostActivityMsg carries one activity-log entry between host and server.
// Host to server: an entry the host's session recorded. Server to host: an
// event an agent reported through the events route for the hosted session,
// which the host records like any other. SessionID is set by the host and
// ignored by the server, which takes the session from the connection; the
// server omits it. Entry is bounded by session.CleanEntry so that it fits one
// CONTROL frame (MaxControl); the envelope adds a few dozen bytes, far below
// MaxHostMessage.
//
// State goes host to server only, with an attention entry: the attention
// state the entry records (needs_input, working or done), which the host's
// session hands on with the entry, set with the entry's stamp. The server
// keeps it only on an attention entry and only as one of those three.
type HostActivityMsg struct {
	T         string   `json:"t"`
	SessionID string   `json:"sessionId,omitempty"`
	Entry     Activity `json:"entry"`
	State     string   `json:"state,omitempty"`
}

// HostChatMsg carries a chat message the host's session kept, as `chat`
// carries it to a viewer. The server ignores SessionID and takes the
// connection's, and cleans the message before it reaches the stream.
type HostChatMsg struct {
	T         string      `json:"t"`
	SessionID string      `json:"sessionId,omitempty"`
	Message   ChatMessage `json:"message"`
}

// HostStopMsg asks the host to stop its process.
type HostStopMsg struct {
	T         string `json:"t"`
	SessionID string `json:"sessionId"`
}

// MaxHostMessage bounds a host control text frame.
const MaxHostMessage = 64 << 10

// Limits of a host's link request.
const (
	MaxLinkLabel      = 120       // bytes
	MaxLinkTTLSeconds = 24 * 3600 // a day
	MaxLinkRequestID  = 32        // bytes
	MaxLinkID         = 64        // bytes, a link id in a revoke
	// LinkRequestsPerMinute is how many link requests a host connection may
	// make a minute; past it the server answers error rate_limited.
	LinkRequestsPerMinute = 5
)

// HostLinkMsg is a host's request for a share link to its session: the
// role, how long the link lasts (the server's default when 0, at most a
// day) and a label. RequestID ties the answer to the request.
type HostLinkMsg struct {
	T          string `json:"t"`
	RequestID  string `json:"requestId"`
	Role       string `json:"role"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
	Label      string `json:"label,omitempty"`
}

// LinkCreated answers HostLinkMsg: the link's URL on the server's public
// base, the same as an invite for the desktop app (conductor://…), and the
// link as the server lists it. An error{requestId} answers a refused one.
type LinkCreated struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
	URL       string `json:"url"`
	Invite    string `json:"invite"`
	LinkID    string `json:"linkId"`
	Role      string `json:"role"`
	Label     string `json:"label,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	// RunID is the run of a link minted by link_run.
	RunID string `json:"runId,omitempty"`
}

// The bounds of a run link's group.
const (
	MaxRunLinkMembers = 32 // a run has at most 12 members at launch; those added later come after
	MaxRunID          = 64
	MaxRunName        = 120
	MaxRunMemberName  = 40
	MaxRunAgentID     = 64
	// RunLinkUpdatesPerMinute bounds link_run_update per connection; link_run
	// shares the link bucket (LinkRequestsPerMinute).
	RunLinkUpdatesPerMinute = 30
	// MaxRunRevision bounds RunGroup.Revision: the largest integer every
	// JSON reader holds exactly (2^53 - 1).
	MaxRunRevision = 1<<53 - 1
)

// RunGroup is a crew run as a switchyard shows it on the join page: the
// host's run id (opaque there), its name, and its members with the id each
// one's session has there, empty while it has none.
//
// Revision orders the groups a host sends of one run, in link_run and
// link_run_update alike: each is higher than every one the host sent before
// it, and a switchyard refuses a group whose revision is not above the
// highest it holds for the run (error stale), so a group sent before
// another and delivered after it never replaces it. 0 (no field) is a host
// that numbers nothing, an older one, whose groups are taken in the order
// they come; a switchyard that knows no revision ignores the field.
type RunGroup struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Members  []RunMember `json:"members"`
	Revision int64       `json:"revision,omitempty"`
	// Desk is the id of the run's desk there (RegisterDesk), sent in the
	// run's sync as the members are, absent with no desk: a run link's
	// viewer joins it for the run's decisions. A switchyard takes only a
	// desk of the same publisher for the same run; an older one ignores
	// the field.
	Desk string `json:"desk,omitempty"`
}

// RunMember is a member of a RunGroup.
type RunMember struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId,omitempty"`
	AgentID   string `json:"agentId"`
	Status    string `json:"status"` // pending | starting | running | ended
}

// Validate checks a group against the bounds above: an id and a name, at
// most MaxRunLinkMembers members with unique names, each with an agent and
// one of the four statuses, and a revision from 0 to MaxRunRevision; at
// least one member, unless the group names its desk (a run reached for its
// decisions before any member is), whose id is at most MaxDeskID bytes.
func (g RunGroup) Validate() error {
	if g.ID == "" || len(g.ID) > MaxRunID || g.Name == "" || len(g.Name) > MaxRunName {
		return errors.New("run: id and name required, within bounds")
	}
	if g.Revision < 0 || g.Revision > MaxRunRevision {
		return fmt.Errorf("run: a revision from 0 to %d", int64(MaxRunRevision))
	}
	if len(g.Desk) > MaxDeskID {
		return fmt.Errorf("run: a desk id of at most %d bytes", MaxDeskID)
	}
	if (len(g.Members) == 0 && g.Desk == "") || len(g.Members) > MaxRunLinkMembers {
		return fmt.Errorf("run: 1 to %d members", MaxRunLinkMembers)
	}
	names := map[string]bool{}
	for _, m := range g.Members {
		if m.Name == "" || len(m.Name) > MaxRunMemberName || names[m.Name] {
			return fmt.Errorf("run: member %q: a unique name of at most %d bytes", m.Name, MaxRunMemberName)
		}
		names[m.Name] = true
		if m.AgentID == "" || len(m.AgentID) > MaxRunAgentID || len(m.SessionID) > 64 {
			return fmt.Errorf("run: member %q: agent or session out of bounds", m.Name)
		}
		switch m.Status {
		case "pending", "starting", "running", "ended":
		default:
			return fmt.Errorf("run: member %q: status %q", m.Name, m.Status)
		}
	}
	return nil
}

// HostRunLinkMsg asks for one link to the sessions of a run's members.
type HostRunLinkMsg struct {
	T          string   `json:"t"`
	RequestID  string   `json:"requestId"`
	Role       string   `json:"role"`
	TTLSeconds int      `json:"ttlSeconds,omitempty"`
	Label      string   `json:"label,omitempty"`
	Run        RunGroup `json:"run"`
}

// HostRunLinkUpdateMsg tells the server a run's members now: the links
// minted for the run follow them, unless they hold a group of a higher
// revision already (RunGroup.Revision; error stale).
type HostRunLinkUpdateMsg struct {
	T         string   `json:"t"`
	RequestID string   `json:"requestId"`
	Run       RunGroup `json:"run"`
}

// RunLinkUpdated answers HostRunLinkUpdateMsg: how many of the run's links
// now follow the new members.
type RunLinkUpdated struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
	RunID     string `json:"runId"`
	Links     int    `json:"links"`
}

// HostLinkRevokeMsg is a host's request to revoke a link the server minted
// for its session (LinkCreated.LinkID). RequestID ties the answer to it.
type HostLinkRevokeMsg struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
	LinkID    string `json:"linkId"`
}

// LinkRevoked answers HostLinkRevokeMsg: the link is revoked and its
// viewers are gone.
type LinkRevoked struct {
	T         string `json:"t"`
	RequestID string `json:"requestId"`
	LinkID    string `json:"linkId"`
}
