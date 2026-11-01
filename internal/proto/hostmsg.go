package proto

import (
	"errors"
	"fmt"
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
}

// LinkGone tells the host that a link through which a viewer of its
// session joined (ViewerJoin.LinkID) no longer opens the session: revoked,
// expired (on the server's clock), its run forgotten, its run's session no
// longer named. It comes whether or not a viewer is still attached through
// it, ahead of the leaves of those that are, so that what a viewer of the
// link asked for and that is still under way (one that left once it had
// sent) is withdrawn. LinkID is at most MaxLinkID bytes; an older host
// ignores the message.
type LinkGone struct {
	T      string `json:"t"`
	LinkID string `json:"linkId"`
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
// one of the four statuses, and a revision from 0 to MaxRunRevision.
func (g RunGroup) Validate() error {
	if g.ID == "" || len(g.ID) > MaxRunID || g.Name == "" || len(g.Name) > MaxRunName {
		return errors.New("run: id and name required, within bounds")
	}
	if g.Revision < 0 || g.Revision > MaxRunRevision {
		return fmt.Errorf("run: a revision from 0 to %d", int64(MaxRunRevision))
	}
	if len(g.Members) == 0 || len(g.Members) > MaxRunLinkMembers {
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
