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
