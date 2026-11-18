package ext

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// Origin is where a provider's launch comes from, for people and for the
// provider: its reference (an id of the provider's own, never a link or a
// secret) and a label the run page shows.
type Origin struct {
	Ref   string `json:"ref"`   // 1 to MaxOriginRef bytes of printable ASCII, no space
	Label string `json:"label"` // 1 to MaxRunLabel characters on one line
}

// Subject is what a provider's launch is about: a reference of the form
// <kind>:<parts> (a change: or job: reference is held to its pattern,
// SubjectRef), its link for people, and an earlier run of the same
// provider that the run follows. The run's agents read it in their task
// file, as data.
type Subject struct {
	Ref   string `json:"ref"`             // 1 to MaxSubjectRef bytes on one line, <kind>:<parts>
	URL   string `json:"url,omitempty"`   // https, at most MaxSubjectURL bytes; no userinfo, query or fragment
	Prior string `json:"prior,omitempty"` // a run id (a crew id, a dash, 8 lower-case hex), at most MaxRunID bytes
}

// RunStatus is a run a provider launched, as the server holds it now
// (Host.Run, Host.RunByOp).
type RunStatus struct {
	ID         string    `json:"id"`
	Crew       string    `json:"crew"`
	State      string    `json:"state"`             // RunRunning | RunNeedsInput | RunStopped | RunFinished | RunInterrupted
	Outcome    string    `json:"outcome,omitempty"` // succeeded | failed | cancelled, once every task is terminal
	Paused     bool      `json:"paused,omitempty"`
	NeedsInput int       `json:"needsInput,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	StoppedAt  time.Time `json:"stoppedAt,omitzero"`
	Origin     RunOrigin `json:"origin"`
}

// RunOrigin is what a run was launched with, as the server's ledger keeps
// it: a retry with the same operation id is answered this run whatever it
// asks, so the caller reads here what the run was launched with.
type RunOrigin struct {
	OpID           string    `json:"opId"`
	Ref            string    `json:"ref"`
	Label          string    `json:"label"`
	Subject        Subject   `json:"subject,omitzero"`
	Project        string    `json:"project,omitempty"`
	StartCommit    string    `json:"startCommit,omitempty"`
	ResolvedDigest string    `json:"resolvedDigest,omitempty"`
	Deadline       time.Time `json:"deadline,omitzero"` // when the engine stops the run, UTC, whole seconds
}

// StopRequest is a run a provider launched to stop, who asks, and why (in
// the run's log).
type StopRequest struct {
	Run string    `json:"run"`
	By  Principal `json:"by"`
	Why string    `json:"why,omitempty"` // at most MaxStopWhy bytes on one line
}

// ErrNotRunning answers a stop of a run that is not running (stopped,
// finished or interrupted): nothing changed.
var ErrNotRunning = errors.New("ext: the run is not running")

const (
	// MaxOriginRef bounds an origin's reference, in bytes.
	MaxOriginRef = 200
	// MaxSubjectRef bounds a subject's reference, in bytes.
	MaxSubjectRef = 512
	// MaxSubjectURL bounds a subject's link, in bytes.
	MaxSubjectURL = 2048
	// MaxRunID bounds a run's id, in bytes: a crew id, a dash and 8 hex.
	MaxRunID = MaxCrewID + 9
	// MaxStopWhy bounds a stop's why, in bytes.
	MaxStopWhy = 200
	// MinStopAfter and MaxStopAfter bound a launch's time limit, a whole
	// number of minutes.
	MinStopAfter = 10 * time.Minute
	MaxStopAfter = 24 * time.Hour
	// MaxProjectKey bounds a Dispatch project's key, in bytes.
	MaxProjectKey = 6
	// DigestLen is a resolved launch's digest's length: lower-case hex.
	DigestLen = 64
)

// A run's states (RunStatus.State).
const (
	RunRunning     = "running"     // a member is pending, starting or running, and none waits on a prompt
	RunNeedsInput  = "needs_input" // a member's session waits on a prompt
	RunStopped     = "stopped"     // stopped: its sessions were stopped
	RunFinished    = "finished"    // every member ended, and none is pending
	RunInterrupted = "interrupted" // the server ended while it ran: nothing runs
)

// A run's outcomes (RunStatus.Outcome), once every task is terminal.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
)

// Refusal is a refusal a caller may act on by its code. Every refusal of
// Launch, ResolveLaunch, Run, RunByOp and StopRun that is ErrRefused is a
// *Refusal; the codes are the Refusal... constants and the server's
// launch route's own.
type Refusal struct {
	Code string // 1 to MaxRefusalCode of a-z, 0-9 and '_'
	Why  string // for people
}

// MaxRefusalCode bounds a refusal's code, in bytes.
const MaxRefusalCode = 40

// The codes of a refusal a caller may act on (Refusal.Code). A launch the
// server's launch route refuses keeps that route's code (crew_changed,
// managed_off, ledger_unavailable, invalid_account, capacity_unavailable,
// account_gone, among others).
const (
	RefusalInvalidRequest     = "invalid_request"      // the record's Valid refuses it
	RefusalNotManaged         = "not_managed"          // a launch with an origin of a crew without tasks
	RefusalInvalidSubject     = "invalid_subject"      // a subject reference in no form the server knows
	RefusalPriorUnknown       = "prior_unknown"        // the earlier run is not one this provider launched, or is not there
	RefusalProjectUnknown     = "project_unknown"      // no such Dispatch project
	RefusalProjectArchived    = "project_archived"     // the Dispatch project is archived
	RefusalLaunchChanged      = "launch_changed"       // the launch resolves to another digest than ResolvedDigest
	RefusalStartCommitMissing = "start_commit_missing" // the folder's repository does not hold the commit
	RefusalResolvedTooLarge   = "resolved_too_large"   // the resolved launch is past MaxResolvedLaunch
	RefusalInvalidCrew        = "invalid_crew"         // the crew cannot be launched as it is
	RefusalInvalidCwd         = "invalid_cwd"          // the launch folder cannot be used
)

// Error is "ext: refused: <code>: <why>".
func (e *Refusal) Error() string {
	if e.Why == "" {
		return ErrRefused.Error() + ": " + e.Code
	}
	return ErrRefused.Error() + ": " + e.Code + ": " + e.Why
}

// Is reports whether target is ErrRefused, so errors.Is(err, ErrRefused)
// holds for every refusal with a code.
func (e *Refusal) Is(target error) bool { return target == ErrRefused }

// RefusalCode is the code of the *Refusal err is or wraps; "" for an error
// that carries none (a plain ErrRefused among them).
func RefusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r.Code
	}
	return ""
}

// ValidRefusalCode reports whether code can be a refusal's: 1 to
// MaxRefusalCode of a-z, 0-9 and '_'.
func ValidRefusalCode(code string) bool {
	if code == "" || len(code) > MaxRefusalCode {
		return false
	}
	for i := 0; i < len(code); i++ {
		if c := code[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// Valid checks o against its bounds: a reference of 1 to MaxOriginRef bytes
// of printable ASCII with no space, and a label of 1 to MaxRunLabel
// characters on one line.
func (o Origin) Valid() error {
	const rec = "origin"
	if o.Ref == "" || len(o.Ref) > MaxOriginRef || !printableASCII(o.Ref) {
		return invalid(rec, "ref is not 1 to %d bytes of printable ASCII with no space", MaxOriginRef)
	}
	return name(rec, "label", o.Label, MaxRunLabel)
}

// Valid checks s against its bounds: a reference of 1 to MaxSubjectRef
// bytes on one line, <kind>:<parts> (SubjectRef); a link, when given,
// https with a host and no userinfo, query or fragment, of at most
// MaxSubjectURL bytes of printable ASCII; an earlier run, when given, of a
// run id's shape. Whether the server knows the reference's kind, and
// whether the earlier run is the provider's, is the server's to answer
// (invalid_subject, prior_unknown).
func (s Subject) Valid() error {
	const rec = "subject"
	if err := SubjectRef(s.Ref); err != nil {
		return err
	}
	if s.URL != "" && !subjectURL(s.URL) {
		return invalid(rec, "url is not an https link of at most %d bytes with no userinfo, query or fragment", MaxSubjectURL)
	}
	if s.Prior != "" && !runID(s.Prior) {
		return invalid(rec, "prior %q is not a run's id", s.Prior)
	}
	return nil
}

// SubjectRef checks a subject's reference: 1 to MaxSubjectRef bytes on one
// line, a kind (1 to 32 of a-z, 0-9 and '-', beginning with a letter), a
// colon and parts that are not empty. Two kinds are held to their pattern:
//
//	change:<host>/<owner>/<name>#<n>@<commit>
//	job:<host>/<owner>/<name>#<n>@<commit>/<check>
//
// a host of 1 to 253 of a-z, 0-9, '.' and '-'; an owner and a name each of
// 1 to 100 of A-Z, a-z, 0-9, '.', '_' and '-'; n a number from 1 of at most
// 18 digits; a commit of 40 or 64 lower-case hex; a check of 1 to 64 of
// A-Z, a-z, 0-9, '.', '_' and '-'.
func SubjectRef(ref string) error {
	const rec = "subject"
	if err := line(rec, "ref", ref, MaxSubjectRef, true); err != nil {
		return err
	}
	kind, parts, ok := strings.Cut(ref, ":")
	if !ok || !slug(kind, 32, true) || parts == "" {
		return invalid(rec, "ref is not <kind>:<parts>")
	}
	switch kind {
	case "change":
		if !changeRef(parts) {
			return invalid(rec, "ref is not change:<host>/<owner>/<name>#<n>@<commit>")
		}
	case "job":
		i := strings.LastIndexByte(parts, '/')
		if i < 0 || !changeRef(parts[:i]) || !refWord(parts[i+1:], 64) {
			return invalid(rec, "ref is not job:<host>/<owner>/<name>#<n>@<commit>/<check>")
		}
	}
	return nil
}

// changeRef reports whether s is <host>/<owner>/<name>#<n>@<commit>.
func changeRef(s string) bool {
	s, commit, ok := strings.Cut(s, "@")
	if !ok || !commitID(commit) {
		return false
	}
	s, n, ok := strings.Cut(s, "#")
	if !ok || !digits(n, 18) {
		return false
	}
	host, rest, ok := strings.Cut(s, "/")
	if !ok || !refHost(host) {
		return false
	}
	owner, repo, ok := strings.Cut(rest, "/")
	return ok && refWord(owner, 100) && refWord(repo, 100)
}

// refHost reports whether s is 1 to 253 of a-z, 0-9, '.' and '-'.
func refHost(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// refWord reports whether s is 1 to max of A-Z, a-z, 0-9, '.', '_' and '-'.
func refWord(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// commitID reports whether s is a full commit id: 40 or 64 lower-case hex.
func commitID(s string) bool { return lowerHex(s, 40) || lowerHex(s, 64) }

// subjectURL reports whether s is an https link with a host and no
// userinfo, query or fragment, of at most MaxSubjectURL bytes of printable
// ASCII.
func subjectURL(s string) bool {
	if len(s) > MaxSubjectURL || !printableASCII(s) || !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, "?#") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == "" && u.Hostname() != ""
}

// runID reports whether s has a run id's shape: a crew id, a dash and 8
// lower-case hex, at most MaxRunID bytes.
func runID(s string) bool {
	i := strings.LastIndexByte(s, '-')
	return len(s) <= MaxRunID && i > 0 && lowerHex(s[i+1:], 8) && slug(s[:i], MaxCrewID, false)
}

// projectKey reports whether s is a Dispatch project's key: an upper-case
// letter, then 1 to 5 upper-case letters or digits.
func projectKey(s string) bool {
	if len(s) < 2 || len(s) > MaxProjectKey || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// stopAfter reports whether d is a launch's time limit: a whole number of
// minutes from MinStopAfter to MaxStopAfter.
func stopAfter(d time.Duration) bool {
	return d >= MinStopAfter && d <= MaxStopAfter && d%time.Minute == 0
}

// Valid checks r against its bounds: a run id's shape, a valid By, and a
// why of at most MaxStopWhy bytes on one line. Whether the run is the
// caller's and runs is the server's to answer (ErrNotFound,
// ErrNotRunning).
func (r StopRequest) Valid() error {
	const rec = "stop request"
	if !runID(r.Run) {
		return invalid(rec, "run %q is not a run's id", r.Run)
	}
	if err := r.By.Valid(); err != nil {
		return invalid(rec, "by: %v", err)
	}
	return line(rec, "why", r.Why, MaxStopWhy, false)
}

// Valid checks o, a run's origin as the server answers it: an operation id
// (opId's rule), a valid Origin of its Ref and Label, a valid Subject or
// none, a project's key or none, a full commit id or none, a digest of
// DigestLen lower-case hex or none (and then a project), and a deadline
// in UTC or none.
func (o RunOrigin) Valid() error {
	const rec = "run origin"
	switch {
	case !opID(o.OpID):
		return invalid(rec, "opId %q is not an operation's id", o.OpID)
	case o.Project != "" && !projectKey(o.Project):
		return invalid(rec, "project %q is not a project's key", o.Project)
	case o.StartCommit != "" && !commitID(o.StartCommit):
		return invalid(rec, "startCommit %q is not a full commit id", o.StartCommit)
	case o.ResolvedDigest != "" && !lowerHex(o.ResolvedDigest, DigestLen):
		return invalid(rec, "resolvedDigest is not %d lower-case hex", DigestLen)
	case o.ResolvedDigest != "" && o.Project == "":
		return invalid(rec, "a resolved digest is of a project, and none is named")
	case !o.Deadline.IsZero() && o.Deadline.Location() != time.UTC:
		return invalid(rec, "deadline is not in UTC")
	}
	if err := (Origin{Ref: o.Ref, Label: o.Label}).Valid(); err != nil {
		return invalid(rec, "%v", err)
	}
	if o.Subject != (Subject{}) {
		if err := o.Subject.Valid(); err != nil {
			return invalid(rec, "%v", err)
		}
	}
	return nil
}

// Valid checks s, a run as the server answers it: a run id's shape, a crew
// id, one of the run's states, one of its outcomes or none, NeedsInput not
// below 0, and a valid Origin.
func (s RunStatus) Valid() error {
	const rec = "run status"
	switch {
	case !runID(s.ID):
		return invalid(rec, "id %q is not a run's id", s.ID)
	case !slug(s.Crew, MaxCrewID, false):
		return invalid(rec, "crew %q is not a crew id", s.Crew)
	case s.State != RunRunning && s.State != RunNeedsInput && s.State != RunStopped && s.State != RunFinished && s.State != RunInterrupted:
		return invalid(rec, "state %q is not a run's state", s.State)
	case s.Outcome != "" && s.Outcome != OutcomeSucceeded && s.Outcome != OutcomeFailed && s.Outcome != OutcomeCancelled:
		return invalid(rec, "outcome %q is not a run's outcome", s.Outcome)
	case s.NeedsInput < 0:
		return invalid(rec, "needsInput is below 0")
	}
	if err := s.Origin.Valid(); err != nil {
		return invalid(rec, "%v", err)
	}
	return nil
}
