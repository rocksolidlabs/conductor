package session

import (
	"context"
	"errors"
	"time"
)

// Kind says where the PTY lives.
type Kind string

const (
	KindServer Kind = "server"
	KindHosted Kind = "hosted"
)

// Status is the session lifecycle state.
type Status string

const (
	StatusStarting         Status = "starting"
	StatusRunning          Status = "running"
	StatusExited           Status = "exited"
	StatusStopped          Status = "stopped"
	StatusHostDisconnected Status = "host_disconnected"
)

// What a hosted session's host key was made from (Info.HostKeyFrom): the
// machine its host reported (proto.HostInfo.Machine), its host's process
// (an instance, but no machine), or the session alone (neither, the oldest
// hosts).
const (
	HostKeyMachine = "machine"
	HostKeyProcess = "process"
	HostKeySession = "session"
)

// A hosted session's control connection as it is (Info.HostLink).
const (
	HostLinkUp   = "up"
	HostLinkDown = "down"
)

// Ended reports whether the process is gone for good.
func (s Status) Ended() bool { return s == StatusExited || s == StatusStopped }

// Role is what an attached client may do.
type Role string

const (
	RoleView    Role = "view"
	RoleControl Role = "control"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleView || r == RoleControl }

// Info is the public description of a session.
type Info struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	AgentID  string   `json:"agentId"`
	Command  []string `json:"command"`
	Cwd      string   `json:"cwd"`
	Status   Status   `json:"status"`
	ExitCode *int     `json:"exitCode,omitempty"`
	Cols     uint16   `json:"cols"`
	Rows     uint16   `json:"rows"`
	Viewers  int      `json:"viewers"`
	HostName string   `json:"hostName,omitempty"`
	// HostUser is the OS user running `conductor host` (hosted sessions).
	HostUser string `json:"hostUser,omitempty"`
	// HostPlatform and HostVersion are what a hosted session's host said of
	// itself as it registered (proto.HostInfo's Platform and Version), each
	// only in its form and omitted otherwise: a platform out of its form
	// refuses the register, a version out of its form is not kept.
	HostPlatform string `json:"hostPlatform,omitempty"`
	HostVersion  string `json:"hostVersion,omitempty"`
	// HostKey is the key a hosted session's machine is grouped by on this
	// server's pages, 32 lowercase hex digits, and HostKeyFrom what it was
	// made from (HostKeyMachine, HostKeyProcess or HostKeySession). A key a
	// host sent is never kept or served, only a hash of it; a key made from
	// a process or a session is hashed under another prefix and never equals
	// a machine's. It groups what a page shows and decides nothing.
	HostKey     string `json:"hostKey,omitempty"`
	HostKeyFrom string `json:"hostKeyFrom,omitempty"`
	// HostLink is a hosted session's control connection as it is, apart from
	// its status: HostLinkUp while the session holds its host's connection,
	// HostLinkDown once the host has gone; HostAwaySince is when it went, set
	// exactly while HostLink is down. An ended session keeps its status when
	// its host leaves, and a session its host came back to keeps the time it
	// last left, so neither the status nor that time can say this.
	HostLink      string     `json:"hostLink,omitempty"`
	HostAwaySince *time.Time `json:"hostAwaySince,omitempty"`
	// Branch is the git branch of Cwd when it is inside a repository.
	Branch string `json:"branch,omitempty"`
	// Crew names the crew run the session is a member of; nil for a session
	// launched on its own.
	Crew *CrewRef `json:"crew,omitempty"`
	// Yolo says the session was launched with its agent's yolo recipe
	// applied (the agent has one, and yolo was on for the launch).
	Yolo bool `json:"yolo,omitempty"`
	// AgentSession is the agent's own session, when known (Resume).
	AgentSession *AgentSession `json:"agentSession,omitempty"`
	// ResumedFrom is the session this one resumed or relaunched.
	ResumedFrom string    `json:"resumedFrom,omitempty"`
	Attention   Attention `json:"attention"`
	// Turns says the session's agent reports its turns: it has reported a
	// working or a done of its own (ReportsTurn: its hooks, its window
	// title, its program status). A bell, an OSC notification, the screen
	// pattern and an operator's mark leave it false. Set under the lock
	// that sets Attention, read with it in one snapshot, never cleared; a
	// hosted session's copy learns it from the reports it carries and from
	// its host's word (proto.HostAttentionMsg.Turns).
	Turns bool `json:"turns,omitempty"`
	// FirstTurn says the session's terminal has reported a turn that ended
	// (a done from a source that reports turns, ReportsTurn): the agent's
	// first reply has ended. A move to Chat is refused before it
	// (Local.SwapWay judges by this field), and a page offers the move by
	// it. Set under the lock that sets Attention, read with it in one
	// snapshot, never cleared; a resumed session's starts false. A hosted
	// session's copy never carries it: a hosted session never moves.
	FirstTurn bool `json:"firstTurn,omitempty"`
	// FirstMessage is "waiting" while the line the session was launched
	// with (POST /api/sessions' message) waits to be typed into its agent;
	// "typed" once it is typed into the agent's input and its Enter left
	// out (a question came, or the screen was not the prompt holding it),
	// until a person's next key or line (MarkFirstMessageTyped); and ""
	// once it is entered, withdrawn or dropped (SetFirstMessage). The text
	// itself is never in Info.
	FirstMessage string `json:"firstMessage,omitempty"`
	// Kept is the ids of the questions the session keeps without showing
	// them (the agent's own question while it is set aside, the question a
	// held turn took the place of), at most MaxKept, read with Attention
	// in one snapshot (Local.Info): what the decision router tells a
	// question set aside from one that ended by. Never on the API.
	Kept []string `json:"-"`
	// LastAnswer is who most recently cleared a needs-input prompt by typing.
	LastAnswer *Answer    `json:"lastAnswer,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	// Way is how the session runs its agent: "protocol" for a chat (the
	// agent's own structured interface), absent for its terminal. Protocol
	// is then the protocol's name (at most 32 bytes, of the catalog's closed
	// set) and Verbatim whether its driver sends a message as written;
	// Cols and Rows are 0. Set by Local.Info from the way, never stored.
	Way      string `json:"way,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Verbatim bool   `json:"verbatim,omitempty"`
}

// CrewRef ties a server session to the crew run it is a member of: the run,
// the saved crew it was launched from and the member's name in it.
type CrewRef struct {
	RunID  string `json:"runId"`
	CrewID string `json:"crewId"`
	Member string `json:"member"`
	// Task and Attempt are set when the session is an attempt at a task of
	// a managed run (#334): the task's id and the attempt's number.
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	// Brakeman is set when the session is a Brakeman turn's member
	// (docs/plans/brakeman.md): the request it does and whether its
	// questions page no one (quiet, a background turn's). It is set at
	// the session's creation, from the launch, so every snapshot that can
	// need input carries it.
	Brakeman *BrakemanMark `json:"brakeman,omitempty"`
	// Lane is set when the session is a member of a comparison's run
	// (docs/plans/compare-crew.md): the lane its member works in, which
	// the engine decides as it launches the session, so that every
	// snapshot carries it from the session's creation. The server's doors
	// for a comparison read it. Never on the API.
	Lane *LaneMark `json:"-"`
}

// LaneMark is a comparison's session's lane (CrewRef.Lane): the candidate
// task its member's tasks belong to, "" for a member of no lane.
type LaneMark struct {
	Candidate string
}

// BrakemanMark is a Brakeman turn's mark on its session (CrewRef.Brakeman).
type BrakemanMark struct {
	Request string `json:"request"`
	// Quiet says the session's questions reach no notification, Chime or
	// webhook while Needs you still lists them: a background turn's.
	Quiet bool `json:"quiet"`
}

// Driver is the minimal interface the registry and API need from any session
// implementation (local PTY or hosted).
type Driver interface {
	Info() Info
	Stop(ctx context.Context) error
	// DisconnectLink closes the clients attached through a link that was
	// revoked (or expired, or no longer opens the session), as revoked:
	// they are written nothing more.
	DisconnectLink(linkID string)
	// EndLink closes the clients attached through one of the session's own
	// links as the session's end, which ends its links with it: they are
	// written what was queued for them first, its last output.
	EndLink(linkID string)
	// LinkViewers counts attached clients per share link id.
	LinkViewers() map[string]int
}

// Typed errors surfaced to transports, which map them to close codes.
var (
	ErrReadOnly     = errors.New("session: read-only role")
	ErrSessionEnded = errors.New("session: ended")
	ErrSlowConsumer = errors.New("session: slow consumer")
	ErrRevoked      = errors.New("session: link revoked")
	ErrBadDimension = errors.New("session: invalid terminal dimension")
	// ErrNotSizer refuses a passive resize from a viewer that does not size
	// the terminal (Local.ResizeWith): it follows the size, and takes it only
	// by asking.
	ErrNotSizer        = errors.New("session: another viewer sizes the terminal")
	ErrTooManyViewers  = errors.New("session: too many viewers")
	ErrFileDenied      = errors.New("session: file access denied")
	ErrTooManyRequests = errors.New("session: too many in-flight requests")
	// ErrExpired closes a viewer whose link reached its expiry, and refuses
	// one that attaches through it after. It is a kind of ErrRevoked
	// (errors.Is(ErrExpired, ErrRevoked) holds): whatever a revoke does to a
	// viewer, an expiry does too; only the words differ.
	ErrExpired error = linkExpired{}
)

// linkExpired is ErrExpired's type: an error that is also ErrRevoked.
type linkExpired struct{}

func (linkExpired) Error() string { return "session: link expired" }

// Is makes errors.Is(ErrExpired, ErrRevoked) hold.
func (linkExpired) Is(target error) bool { return target == ErrRevoked }

// Grant is what a viewer's credential gives it on a session as it stands
// when the viewer attaches: its role, and when the credential lapses (zero
// for never). See AttachOptions.Authorize.
type Grant struct {
	Role  Role
	Until time.Time
}
