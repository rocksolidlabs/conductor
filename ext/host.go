package ext

import (
	"context"
	"errors"
	"log/slog"
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
