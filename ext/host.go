package ext

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// Starter is a provider that needs the server: Start is called after the
// server is made and before it serves, with the provider's one door into
// the app. It returns at once, never waiting on its service (it begins
// goroutines that reach it), and it does not fail for an outage: an error
// is for what no retry mends (a bad configuration). One that errs, panics
// or has not returned within StartWait is called again, with a new ctx and
// a new Host, after a backoff, and only once the earlier try has returned:
// a provider never has two Starts running. ctx ends when the server shuts
// down or gives that try up, and the provider's goroutines end with it; the
// Host of a try given up refuses every call.
type Starter interface {
	Start(ctx context.Context, h Host) error
}

// Host is a provider's one door into the app.
type Host interface {
	// Launch launches a saved crew as a run, as POST /api/crews/{id}/launch
	// does: through the engine, the one scheduler.
	Launch(ctx context.Context, r LaunchRequest) (RunRef, error)
	// Submit types a line into a server session, never into a question it
	// shows; or, with Question, answers that question with a choice (Input)
	// or a line (Text), once, as a viewer's reply would.
	Submit(ctx context.Context, r SubmitRequest) (SubmitResult, error)
	// Notify hands a notice to the notification sinks.
	Notify(ctx context.Context, n Notification) error
	// DataDir is a directory of the provider's own, <dataDir>/ext/<name>.
	DataDir() (string, error)
	// Log is the server's logger, naming the provider.
	Log() *slog.Logger
	// LaunchTurn launches a Brakeman turn as a run: an unsaved one-member
	// crew of the agent in its adapter's read-only mode, never yolo,
	// through the engine, the one scheduler. Only the Host the server
	// gives Brakeman's desk launches one, for the request holding the
	// desk's slot; a provider's Host answers ErrUnsupported (a turn starts
	// from a request Brakeman's desk holds).
	LaunchTurn(ctx context.Context, r TurnLaunch) (RunRef, error)
	// LaunchApproved launches a managed run from a ticket's plan revision
	// that the operator approved and that still holds, as POST
	// /api/dispatch/tickets/{key}/launch does: OpID is the launch's
	// operation id, so a retry answers the launch it made and never starts
	// a second run.
	LaunchApproved(ctx context.Context, r ApprovedLaunch) (RunRef, error)
}

// TurnLaunch is a Brakeman turn to launch: the catalog agent, its model
// ("" for its default), whether Conductor's tools are allowed without
// asking (where its adapter can say so), the folder the turn runs in, the
// line typed once its session is ready, the run's label and the request
// the turn is.
type TurnLaunch struct {
	Agent      string `json:"agent"`
	Model      string `json:"model,omitempty"`
	AllowTools bool   `json:"allowTools,omitempty"`
	Folder     string `json:"folder"`
	Line       string `json:"line"`
	Label      string `json:"label"`
	Request    string `json:"request"`
}

// ApprovedLaunch is a launch from a ticket's approved plan revision: the
// ticket's key, the revision's id, the ticket's revision the caller read
// (ExpectedRev, 0 for none: a ticket that moved since is refused), the
// launch's operation id, the run's label ("" for the ticket's key and
// title) and who asks.
type ApprovedLaunch struct {
	Ticket      string    `json:"ticket"`
	Plan        string    `json:"plan"`
	ExpectedRev int64     `json:"expectedRev,omitempty"`
	OpID        string    `json:"opId"`
	Label       string    `json:"label,omitempty"`
	By          Principal `json:"by"`
}

// LaunchRequest is a saved crew to launch as a run, and who asks.
type LaunchRequest struct {
	Crew  string    `json:"crew"`
	Label string    `json:"label,omitempty"`
	By    Principal `json:"by"`
}

// RunRef is a run a launch made.
type RunRef struct {
	ID   string `json:"id"`
	Crew string `json:"crew"`
	URL  string `json:"url"`
}

// SubmitRequest is a line to type into a session, or an answer to the
// question it shows, and who gives it.
type SubmitRequest struct {
	Session  string    `json:"session"`
	Question string    `json:"question,omitempty"`
	Input    string    `json:"input,omitempty"`
	Text     string    `json:"text,omitempty"`
	By       Principal `json:"by"`
}

// SubmitResult is what became of a submission.
type SubmitResult struct {
	Status string `json:"status"` // entered | pending (accepted, the program reading slowly) | sent (to a host)
}

var (
	ErrUnsupported      = errors.New("ext: not supported here")
	ErrNotFound         = errors.New("ext: not found")
	ErrQuestionAnswered = errors.New("ext: the question no longer waits")
	ErrRefused          = errors.New("ext: refused") // wrapped with the words why
)

const (
	StartWait = 5 * time.Second
	// MaxCrewID and MaxRunLabel are a crew id's and a run label's bounds,
	// in bytes and in characters: the crews' own (internal/crew).
	MaxCrewID   = 64
	MaxRunLabel = 60
	// MaxSessionRef bounds a session id a request names, in bytes.
	MaxSessionRef = 64
	// MaxQuestionRef bounds a question id, in bytes (proto.MaxQuestionID).
	MaxQuestionRef = 32
	// MaxAnswerInput bounds a choice's keys, in bytes (proto.MaxAnswerInput).
	MaxAnswerInput = 48
	// MaxAgentID bounds a catalog agent's id, in bytes (the catalog's own).
	MaxAgentID = 32
	// MaxModel bounds a model's name, in bytes (Brakeman's configuration's).
	MaxModel = 100
	// MaxFolder bounds a folder's absolute path, in bytes.
	MaxFolder = 4096
	// MaxTurnLine bounds the line typed into a turn's session, in bytes.
	MaxTurnLine = 300
	// MaxRequestRef bounds a Brakeman request's id (br-<n>), in bytes.
	MaxRequestRef = 12
	// MaxOpID bounds an operation's id, in bytes (Dispatch's OpIDPattern).
	MaxOpID = 32
	// PlanIDLen is a plan revision's id's length: lower-case hex.
	PlanIDLen = 16
)

// Valid checks r against its bounds: a crew id of 1 to MaxCrewID of a-z,
// 0-9 and '-' beginning with a letter or a digit (the shape of a crew's
// id), a label of at most MaxRunLabel characters with no control
// character, and a valid By. Whether the crew exists is the server's to
// answer (ErrNotFound).
func (r LaunchRequest) Valid() error {
	const rec = "launch request"
	if !slug(r.Crew, MaxCrewID, false) {
		return invalid(rec, "crew %q is not a crew id", r.Crew)
	}
	switch {
	case !oneLine(r.Label):
		return invalid(rec, "label is not one line of UTF-8 text")
	case utf8.RuneCountInString(r.Label) > MaxRunLabel:
		return invalid(rec, "label is past %d characters", MaxRunLabel)
	}
	if err := r.By.Valid(); err != nil {
		return invalid(rec, "by: %v", err)
	}
	return nil
}

// Valid checks r against its bounds: a session id of 1 to MaxSessionRef
// bytes of printable ASCII; exactly one of Input and Text; Input, a
// choice's keys of at most MaxAnswerInput bytes, only with Question, a
// question id of 1 to MaxQuestionRef bytes of printable ASCII; Text at
// most MaxAnswerText bytes of text, tabs and line ends its only control
// characters; and a valid By. Whether the session
// and its question are there is the server's to answer.
func (r SubmitRequest) Valid() error {
	const rec = "submit request"
	switch {
	case r.Session == "" || len(r.Session) > MaxSessionRef || !printableASCII(r.Session):
		return invalid(rec, "session %q is not a session id", r.Session)
	case r.Question != "" && (len(r.Question) > MaxQuestionRef || !printableASCII(r.Question)):
		return invalid(rec, "question %q is not a question id", r.Question)
	case (r.Input == "") == (r.Text == ""):
		return invalid(rec, "a submission is exactly one of a choice and a line")
	case r.Input != "" && r.Question == "":
		return invalid(rec, "a choice answers a question, and none is named")
	case len(r.Input) > MaxAnswerInput:
		return invalid(rec, "input is %d bytes, past %d", len(r.Input), MaxAnswerInput)
	}
	if r.Text != "" {
		if err := typed(rec, "text", r.Text); err != nil {
			return err
		}
	}
	if err := r.By.Valid(); err != nil {
		return invalid(rec, "by: %v", err)
	}
	return nil
}

// Valid checks r against its bounds: an agent id of 1 to MaxAgentID of
// a-z, 0-9 and '-'; a model of at most MaxModel of letters, digits and
// ._:/@- beginning with a letter or a digit, or none; an absolute folder
// of at most MaxFolder bytes on one line; a line of 1 to MaxTurnLine
// bytes on one line; a label of 1 to MaxRunLabel characters on one line;
// and a request id br-<n>, n from 1 with no leading zero. Whether the
// agent can be Brakeman's and the request holds the slot is the server's
// to answer.
func (r TurnLaunch) Valid() error {
	const rec = "turn launch"
	switch {
	case !agentID(r.Agent):
		return invalid(rec, "agent %q is not a catalog agent's id", r.Agent)
	case r.Model != "" && !model(r.Model):
		return invalid(rec, "model %q is not a model's name", r.Model)
	case r.Folder == "" || len(r.Folder) > MaxFolder || !oneLine(r.Folder) || !filepath.IsAbs(r.Folder):
		return invalid(rec, "folder is not an absolute path of at most %d bytes", MaxFolder)
	case r.Line == "" || len(r.Line) > MaxTurnLine || !oneLine(r.Line):
		return invalid(rec, "line is not one line of 1 to %d bytes", MaxTurnLine)
	case r.Label == "" || !oneLine(r.Label) || utf8.RuneCountInString(r.Label) > MaxRunLabel:
		return invalid(rec, "label is not one line of 1 to %d characters", MaxRunLabel)
	case !requestID(r.Request):
		return invalid(rec, "request %q is not a Brakeman request's id", r.Request)
	}
	return nil
}

// Valid checks r against its bounds: a ticket's key (a project key of an
// upper-case letter and 1 to 5 upper-case letters or digits, a dash, a
// number from 1 with no leading zero); a plan revision's id of PlanIDLen
// lower-case hex; an ExpectedRev not below 0; an op id of 1 to MaxOpID of
// letters, digits and ._~-; a label of at most MaxRunLabel characters on
// one line; and a valid By. Whether the revision is the ticket's, approved
// and still holding is the server's to answer.
func (r ApprovedLaunch) Valid() error {
	const rec = "approved launch"
	switch {
	case !ticketKey(r.Ticket):
		return invalid(rec, "ticket %q is not a ticket's key", r.Ticket)
	case !lowerHex(r.Plan, PlanIDLen):
		return invalid(rec, "plan %q is not a plan revision's id", r.Plan)
	case r.ExpectedRev < 0:
		return invalid(rec, "expectedRev is below 0")
	case !opID(r.OpID):
		return invalid(rec, "opId %q is not an operation's id", r.OpID)
	case !oneLine(r.Label) || utf8.RuneCountInString(r.Label) > MaxRunLabel:
		return invalid(rec, "label is not one line of at most %d characters", MaxRunLabel)
	}
	if err := r.By.Valid(); err != nil {
		return invalid(rec, "by: %v", err)
	}
	return nil
}
