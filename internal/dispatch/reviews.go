package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A ticket's reviews (Program item RV1, docs/plans/review-repair.md,
// "internal/dispatch: reviews, findings and marks"): a person's batch of
// notes on one candidate, <run>/<task>/<n>@<commit>, against a named base,
// sent as one review with a verdict. Its findings are entities of their
// own, made with it and never edited; a finding's marks are a log, a
// person's verdict on it against a later candidate, and its state is not
// stored but read from them: the latest mark's verdict in the log's one
// order, open with none. A request for changes is applied by Yardmaster
// (the API, the ledger's owner, records the outcome here as the review's
// application: pending, then applied or refused, at most MaxReviewTries
// tries); a comment or an approval is applied to nothing. Nothing here
// reads the ledger: what the server worked out from it (a finding's
// anchor, a check finding's receipt, where a finding's lines were on a
// candidate) is evidence, an argument apart from the request, stored with
// the rows and never part of the operation's fingerprint, so a resend
// whose evidence changed since (a receipt's class, a log pruned, a line
// located elsewhere) is the same request. A review's rows are entity
// review and a finding's entity finding, naming their ticket and its
// project; a review bumps its own rev and a finding has none, so neither
// makes a ticket's edit stale.

// The verdicts of a review.
const (
	VerdictRequestChanges = "request_changes"
	VerdictComment        = "comment"
	VerdictApprove        = "approve"
)

// ReviewVerdicts is a review's verdicts.
var ReviewVerdicts = []string{VerdictRequestChanges, VerdictComment, VerdictApprove}

// What a client says it is (via): the app's screens, the CLI, or Brakeman,
// which the API records only for Brakeman's principal; "" for none said.
const (
	ViaWeb      = "web"
	ViaCLI      = "cli"
	ViaBrakeman = "brakeman"
)

// ReviewVias is what via may be, none among it.
var ReviewVias = []string{"", ViaWeb, ViaCLI, ViaBrakeman}

// The states of a review's application: none for a comment or an
// approval, for good; for a request, pending (its try) and then applied,
// which is final, or refused, which a try sent again makes pending.
const (
	ApplicationNone    = "none"
	ApplicationPending = "pending"
	ApplicationApplied = "applied"
	ApplicationRefused = "refused"
)

// The kinds of a finding: on lines of a file, on a file, on the candidate
// as a whole, or on a failed check's receipt.
const (
	FindingKindLine    = "line"
	FindingKindFile    = "file"
	FindingKindGeneral = "general"
	FindingKindCheck   = "check"
)

// FindingKinds is a finding's kinds.
var FindingKinds = []string{FindingKindLine, FindingKindFile, FindingKindGeneral, FindingKindCheck}

// The intents of a finding: a change asked for, a question, a note.
const (
	FindingIntentChange   = "change"
	FindingIntentQuestion = "question"
	FindingIntentNote     = "note"
)

// FindingIntents is a finding's intents.
var FindingIntents = []string{FindingIntentChange, FindingIntentQuestion, FindingIntentNote}

// Severities is a change's severities, the worst first; a question and a
// note have none.
var Severities = []string{"blocker", "major", "minor", "nit"}

// The sides of a diff a finding's lines are on: the candidate's, or the
// base's (a removed line).
const (
	SideCandidate = "candidate"
	SideBase      = "base"
)

// The verdicts of a mark, and so the states of a finding: open with no
// mark.
const (
	FindingOpen      = "open"
	FindingResolved  = "resolved"
	FindingWithdrawn = "withdrawn"
)

// FindingStates is a finding's states, and a mark's verdicts.
var FindingStates = []string{FindingOpen, FindingResolved, FindingWithdrawn}

// The sources of a check finding's receipt: Yardmaster's ledger; external
// is reserved for a CI job a linked pull request reports (Program item
// IN1a, docs/plans/tracker-linking.md) and refused until that item
// defines it.
const (
	CheckSourceLedger   = "ledger"
	CheckSourceExternal = "external"
)

// FailureClasses is what the server works out a failed receipt's class to
// be, and what the person may choose it is (internal/crew's
// ClassifyFailure): a defect of the code, a failure of the machine, a
// check that passed on the same commit elsewhere in the chain, a
// cancellation, evidence the ledger no longer holds.
var FailureClasses = []string{"code", "infrastructure", "flaky", "cancelled", "unavailable"}

// The intents of a review's and a finding's operations beside
// IntentReviewSubmit and IntentFindingMark (ops.go).
const (
	IntentReviewRetry   = "review.retry"
	IntentReviewApplied = "review.applied"
	IntentReviewRefused = "review.refused"
)

// NewReview is what review.submit takes, as the client sent it: the
// candidate, the base (a full commit), the verdict, a summary, what the
// client says it is, and the findings in their order. It is the
// operation's content, its fingerprint, with the ticket's key
// (ReviewSubmitArgs).
type NewReview struct {
	Candidate string       `json:"candidate"`
	Base      string       `json:"base"`
	Verdict   string       `json:"verdict"`
	Summary   string       `json:"summary,omitempty"`
	Via       string       `json:"via,omitempty"`
	Findings  []NewFinding `json:"findings"`
}

// NewFinding is one finding as the client sent it: its kind and intent, a
// change's severity, a path and the side it is on (a line or a file
// finding), the line range and the lines as the person saw them (a line
// finding), the words and the resolution asked for, and for a check
// finding the receipt it names (CheckRequest).
type NewFinding struct {
	Kind       string        `json:"kind"`
	Intent     string        `json:"intent"`
	Severity   string        `json:"severity,omitempty"`
	Path       string        `json:"path,omitempty"`
	Side       string        `json:"side,omitempty"`
	From       int           `json:"from,omitempty"`
	To         int           `json:"to,omitempty"`
	Lines      []string      `json:"lines,omitempty"`
	Message    string        `json:"message,omitempty"`
	Resolution string        `json:"resolution,omitempty"`
	Check      *CheckRequest `json:"check,omitempty"`
}

// CheckRequest is what a client sends of a check finding's receipt: its
// source, its identity (<run>/<task>/<n>/<phase>/<id>/<k>), the class the
// person chose and what they quote of its output. The server fills the
// rest from the ledger (CheckRef).
type CheckRequest struct {
	Source  string `json:"source"`
	Receipt string `json:"receipt"`
	Chosen  string `json:"chosen,omitempty"`
	Quote   string `json:"quote,omitempty"`
}

// ReviewSubmitArgs is review.submit's content, its fingerprint: the
// ticket's key and the review as the client sent it, never the evidence.
// Replay takes it.
type ReviewSubmitArgs struct {
	Key    string    `json:"key"`
	Review NewReview `json:"review"`
}

// ReviewEvidence is what the server worked out from a review's request:
// one entry per finding, in the request's order, each naming its place k
// (1 to MaxReviewFindings). It is stored with the rows and never
// fingerprinted.
type ReviewEvidence struct {
	Findings []FindingEvidence `json:"findings"`
}

// FindingEvidence is a finding's evidence: the commit its lines are
// anchored on (the candidate's for a candidate-side line or file finding,
// the base for a base-side one, none otherwise), and a check finding's
// receipt as the server read it from the ledger.
type FindingEvidence struct {
	K            int       `json:"k"`
	AnchorCommit string    `json:"anchorCommit,omitempty"`
	Check        *CheckRef `json:"check,omitempty"`
}

// CheckRef is a check finding's receipt as the server read it from the
// ledger, with the person's choice and quote beside it: its source and
// identity, the identity's parts, the commit it tested (subject), the
// check's argv, how it ended (the row's state, exit, signal, timed out,
// killed, stable, and the ledger's words of its failure, error), where it
// ran, its log's SHA-256 and size, the ledger's tail of the log,
// the class the server worked out, the class the person chose and their
// quote, and the attempt before the failing one. The log itself stays the
// ledger's, opened by the identity, and so does the whole argv when the
// one kept is cut to fit MaxCheckRef (ArgvCut, fitCheckRef).
type CheckRef struct {
	Source    string         `json:"source"`
	Receipt   string         `json:"receipt"`
	Run       string         `json:"run"`
	Task      string         `json:"task"`
	Attempt   int            `json:"attempt"`
	Phase     string         `json:"phase"`
	ID        string         `json:"id"`
	K         int            `json:"k"`
	Subject   string         `json:"subject"`
	Argv      []string       `json:"argv"`
	ArgvCut   bool           `json:"argvCut,omitempty"`
	Exit      *int           `json:"exit,omitempty"`
	Signal    string         `json:"signal,omitempty"`
	TimedOut  bool           `json:"timedOut"`
	Killed    bool           `json:"killed"`
	Stable    bool           `json:"stable"`
	State     string         `json:"state"`
	Error     string         `json:"error,omitempty"`
	Where     string         `json:"where"`
	LogSHA256 string         `json:"logSha256,omitempty"`
	LogBytes  int64          `json:"logBytes"`
	Tail      string         `json:"tail"`
	Class     string         `json:"class"`
	Chosen    string         `json:"chosen,omitempty"`
	Quote     string         `json:"quote,omitempty"`
	Previous  *CheckPrevious `json:"previous,omitempty"`
}

// CheckPrevious is the attempt before a check finding's failing one: its
// number, state, stage and result commit, as the ledger keeps them.
type CheckPrevious struct {
	Attempt int    `json:"attempt"`
	State   string `json:"state"`
	Stage   string `json:"stage,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// passed says the ended receipt failed nothing, judged by exactly the
// fields the ledger says it by, per phase and per way a step ends. The
// engine writes every row it ends (verify.go for a check and a verify
// step, runStep for a setup, runProbe for a ready probe, watchService and
// serviceNotRun for a service) and Ledger.Recover the rows a server that
// crashed left open:
//
//	phase          ends                                   the row says                                judged
//	any            still started or running               state started|running                       not ended: refused (bindCheck)
//	any            recovered after a crash (Recover)      state interrupted, no words, exit -1        failed
//	any            interrupted (stop, cancel, close)      state interrupted, words                    failed
//	any            never run, its start not recorded      no row                                      no receipt (the fill's not_found)
//	check, verify  passed                                 finished, exit 0, stable, no words          passed
//	check, verify  passed, killed (a leftover held        finished, exit 0, stable, killed, no words  passed (killed decides nothing)
//	               the output, ended with the group)
//	check, verify  exited non-zero (killed or not)        finished, exit n; words, none in version 4  failed
//	check, verify  ended by a signal                      finished, signal; words, none in version 4  failed
//	check, verify  timed out                              finished, timed out; words, none in v4      failed
//	check, verify  left the checkout changed              finished, stable false; words, none in v4   failed
//	check, verify  never run (could not start)            finished, exit -1; words, none in v4        failed
//	check, verify  its outcome refused by the ledger      finished, exit 0, stable, words             failed
//	setup          passed (a worktree's: stable unset)    finished, exit 0, no words                  passed
//	setup          passed, killed                         finished, exit 0, killed, no words          passed
//	setup          exited non-zero, a signal, timed out,  finished, words                             failed
//	               never run, its checkout changed,
//	               its running row refused
//	service        stopped by its tree's end or the       finished, a signal, no words                passed (the tree's end, not
//	               server's shutdown                                                                  the service's; a probe cut by
//	                                                                                                  it says so in its own words)
//	service        exited before the tree was ready,      finished, words                             failed
//	               or while it was in use (exit 0 too)
//	service        never run (could not start)            finished, exit -1, words                    failed
//	ready          passed (a URL or a command; stable     finished, exit 0, no words                  passed
//	               unset)
//	ready          did not pass in time, cut by a         finished, words                             failed
//	               service's exit
//
// So: a row not finished failed (bindCheck refuses one not ended), words
// fail a finished row of every phase, and a finished check or verify step
// passed only with exit 0, stable, in time and by no signal besides,
// because a version 4 database kept no words (crew's FailedOutcome) and
// stable false is a checkout the command left changed. A step of the tree
// is judged by its words alone: its stable is set by a checkout's setup
// alone, its exit is -1 for a URL probe that did not pass and a service
// stopped, and each way it fails has words.
// TestACheckFindingsReceiptIsJudgedByItsPhase holds a case for every row.
func (c CheckRef) passed() bool {
	if c.State != ReceiptFinished || c.Error != "" {
		return false
	}
	switch c.Phase {
	case PhaseCheck, PhaseVerify:
		return c.Exit != nil && *c.Exit == 0 && c.Stable && !c.TimedOut && c.Signal == ""
	}
	return true
}

// NewMark is what finding.mark takes, as the client sent it: the candidate
// the finding is marked against, the verdict, a note, what the client
// says it is.
type NewMark struct {
	Candidate string `json:"candidate"`
	Verdict   string `json:"verdict"`
	Note      string `json:"note,omitempty"`
	Via       string `json:"via,omitempty"`
}

// FindingMarkArgs is finding.mark's content, its fingerprint: the
// finding's id and the mark as the client sent it, never where the
// server located the finding's lines. Replay takes it.
type FindingMarkArgs struct {
	ID   string  `json:"id"`
	Mark NewMark `json:"mark"`
}

// Applied is a request's application as the ledger's owner recorded it
// (AppliedReview): the run the repair was made in (the reviewed run, or
// the continuation made for it), the command's id, the run's revision
// after it, and the number of the attempt that starts at the candidate.
type Applied struct {
	Run      string `json:"run"`
	Command  string `json:"command"`
	Revision int64  `json:"revision"`
	Attempt  int    `json:"attempt"`
}

// ReviewApplication is a review's application: its state, the try it is
// at (from 1, 0 for a comment or an approval), the run, command, revision
// and attempt of an applied one, why a refused one was, and when the last
// outcome was recorded.
type ReviewApplication struct {
	State    string    `json:"state"`
	Try      int       `json:"try,omitempty"`
	Run      string    `json:"run,omitempty"`
	Command  string    `json:"command,omitempty"`
	Revision int64     `json:"revision,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
	Why      string    `json:"why,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

// FindingCounts is a review's findings counted by state.
type FindingCounts struct {
	Open      int `json:"open"`
	Resolved  int `json:"resolved"`
	Withdrawn int `json:"withdrawn"`
}

// ReviewSummary is a review as a list shows it: its row, its ticket's
// key, its application, and its findings counted by state.
type ReviewSummary struct {
	ID          string            `json:"id"`
	TicketID    string            `json:"ticketId"`
	TicketKey   string            `json:"ticketKey"`
	Candidate   string            `json:"candidate"`
	Base        string            `json:"base"`
	Verdict     string            `json:"verdict"`
	Summary     string            `json:"summary"`
	CreatedAt   time.Time         `json:"createdAt"`
	CreatedBy   string            `json:"createdBy"`
	Actor       string            `json:"actor"`
	Via         string            `json:"via"`
	Fingerprint string            `json:"-"`
	Application ReviewApplication `json:"application"`
	Rev         int64             `json:"rev"`
	Counts      FindingCounts     `json:"counts"`
}

// Review is a review read whole: its summary and its findings in their
// order, each with its marks counted and its latest mark.
type Review struct {
	ReviewSummary
	Findings []Finding `json:"findings"`
}

// Finding is a finding's row, with its review's candidate beside it, and
// derived at read: its state (its latest mark's verdict, open with none),
// how many marks it has, and the latest of them (without where it located
// the lines, which Marks reads).
type Finding struct {
	ID           string    `json:"id"`
	ReviewID     string    `json:"reviewId"`
	TicketID     string    `json:"ticketId"`
	K            int       `json:"k"`
	Kind         string    `json:"kind"`
	Intent       string    `json:"intent"`
	Severity     string    `json:"severity,omitempty"`
	Path         string    `json:"path,omitempty"`
	Side         string    `json:"side,omitempty"`
	From         int       `json:"from,omitempty"`
	To           int       `json:"to,omitempty"`
	AnchorCommit string    `json:"anchorCommit,omitempty"`
	Lines        []string  `json:"lines"`
	Message      string    `json:"message"`
	Resolution   string    `json:"resolution,omitempty"`
	Check        *CheckRef `json:"check,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	Candidate    string    `json:"candidate"`
	State        string    `json:"state"`
	Marks        int       `json:"marks"`
	Latest       *Mark     `json:"latest,omitempty"`
}

// Mark is a mark's row: the candidate the finding was marked against, the
// verdict, what the page showed of the lines there (the server's JSON),
// the note, and who made it.
type Mark struct {
	ID          string          `json:"id"`
	FindingID   string          `json:"findingId"`
	TicketID    string          `json:"ticketId"`
	Candidate   string          `json:"candidate"`
	Verdict     string          `json:"verdict"`
	Located     json.RawMessage `json:"located,omitempty"`
	Note        string          `json:"note,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	CreatedBy   string          `json:"createdBy"`
	Actor       string          `json:"actor"`
	Via         string          `json:"via"`
	OpID        string          `json:"-"`
	Fingerprint string          `json:"-"`
}

// ReviewsPage is a page of a ticket's reviews, newest first, and the
// cursor of the next ("" on the last): the last review's id.
type ReviewsPage struct {
	Reviews []ReviewSummary `json:"reviews"`
	Next    string          `json:"next,omitempty"`
}

// MarksPage is a page of a finding's marks, oldest first, and the cursor
// of the next ("" on the last): the last mark's id.
type MarksPage struct {
	Marks []Mark `json:"marks"`
	Next  string `json:"next,omitempty"`
}

// FindingsPage is a page of a ticket's findings, in their reviews' order
// and their own, and the cursor of the next ("" on the last): the last
// finding's id.
type FindingsPage struct {
	Findings []Finding `json:"findings"`
	Next     string    `json:"next,omitempty"`
}

// FindingFilter is what a page of a ticket's findings is narrowed to: a
// state (FindingStates, "" for every one) and the task of the findings'
// candidates ("" for every task).
type FindingFilter struct {
	State string `json:"state,omitempty"`
	Task  string `json:"task,omitempty"`
}

// reviewCreated is a review's create row's value: what never changes (the
// field rules' immutable class). Its application is set rows of its own
// after it, as it later changes.
type reviewCreated struct {
	ID          string    `json:"id"`
	TicketID    string    `json:"ticketId"`
	Candidate   string    `json:"candidate"`
	Base        string    `json:"base"`
	Verdict     string    `json:"verdict"`
	Summary     string    `json:"summary"`
	Via         string    `json:"via"`
	Actor       string    `json:"actor"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
	CreatedBy   string    `json:"createdBy"`
}

// findingCreated is a finding's create row's value: the finding whole,
// every field immutable.
type findingCreated struct {
	ID           string    `json:"id"`
	ReviewID     string    `json:"reviewId"`
	TicketID     string    `json:"ticketId"`
	K            int       `json:"k"`
	Kind         string    `json:"kind"`
	Intent       string    `json:"intent"`
	Severity     string    `json:"severity,omitempty"`
	Path         string    `json:"path,omitempty"`
	Side         string    `json:"side,omitempty"`
	From         int       `json:"from,omitempty"`
	To           int       `json:"to,omitempty"`
	AnchorCommit string    `json:"anchorCommit,omitempty"`
	Lines        []string  `json:"lines"`
	Message      string    `json:"message"`
	Resolution   string    `json:"resolution,omitempty"`
	Check        *CheckRef `json:"check,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// markAdded is a mark's add row's value, a row of its finding's marks log:
// the mark whole and the fingerprint of the operation that made it, which
// the mark's row keeps too, so a replica can compare a resend once its
// receipt is pruned.
type markAdded struct {
	Mark
	OpID        string `json:"opId"`
	Fingerprint string `json:"fingerprint"`
}

// The phases of a manifest's steps that make an attempt's tree, beside
// PhaseCheck and PhaseVerify (the ledger's PhaseSetup, PhaseService and
// PhaseReady): a step that failed there failed its attempt, so a person's
// check finding may name its receipt as it names a check's.
const (
	PhaseSetup   = "setup"
	PhaseService = "service"
	PhaseReady   = "ready"
)

// The states a check finding's receipt is in (CheckRef.State), the
// ledger's row's (internal/crew/ledger's CheckStarted, CheckRunning,
// CheckFinished and CheckInterrupted): started or running is not ended;
// finished is ended by the engine, judged by how (CheckRef.passed);
// interrupted is ended under the step, by the engine (a stop, a cancel,
// the attempt's close) or by a server's recovery after a crash, and never
// passed.
const (
	ReceiptStarted     = "started"
	ReceiptRunning     = "running"
	ReceiptFinished    = "finished"
	ReceiptInterrupted = "interrupted"
)

// receiptStates is every state a receipt's row is in.
var receiptStates = []string{ReceiptStarted, ReceiptRunning, ReceiptFinished, ReceiptInterrupted}

// receiptRefPattern is a ledger receipt's identity:
// <run>/<task>/<n>/<phase>/<id>/<k>, its k 0 or more with no leading zero,
// its phase one whose failure fails an attempt: a task's check, a verify
// step, or a setup, service or ready step of the tree (not cleanup, which
// runs after the attempt is judged, nor a prerequisite, which is the run's
// and no attempt's).
var receiptRefPattern = regexp.MustCompile(`^([^/]+)/([^/]+)/([1-9][0-9]{0,5})/(check|verify|setup|service|ready)/([^/]+)/(0|[1-9][0-9]{0,5})$`)

// receiptRef is a receipt's identity, parsed.
type receiptRef struct {
	run, task  string
	attempt, k int
	phase, id  string
}

// String is the identity as a check finding names it.
func (r receiptRef) String() string {
	return r.run + "/" + r.task + "/" + strconv.Itoa(r.attempt) + "/" + r.phase + "/" + r.id + "/" + strconv.Itoa(r.k)
}

// parseReceiptRef reads a receipt's identity; an error is a Refusal
// (invalid).
func parseReceiptRef(s string) (receiptRef, error) {
	m := receiptRefPattern.FindStringSubmatch(s)
	if m == nil || !validRunID(m[1]) || !taskIDPattern.MatchString(m[2]) || !taskIDPattern.MatchString(m[5]) {
		return receiptRef{}, refuse(RefusalInvalid, "the receipt %q is not <run>/<task>/<n>/<phase>/<id>/<k>", s)
	}
	attempt, _ := strconv.Atoi(m[3])
	k, _ := strconv.Atoi(m[6])
	return receiptRef{run: m[1], task: m[2], attempt: attempt, phase: m[4], id: m[5], k: k}, nil
}

// validRunID says run is a run id of the engine's shape: a crew's id, a
// dash, eight hex digits.
func validRunID(run string) bool {
	i := strings.LastIndexByte(run, '-')
	return i > 0 && crewIDPattern.MatchString(run[:i]) && runSuffixPattern.MatchString(run[i+1:])
}

// checkVia checks what a client says it is (ReviewVias).
func checkVia(via string) error {
	if !slices.Contains(ReviewVias, via) {
		return refuse(RefusalInvalid, "via %q is none of web, cli and brakeman", via)
	}
	return nil
}

// errEvidence is evidence that does not answer its request: the caller's
// fault, never a refusal, so nothing is written and the operation may be
// sent again with the evidence it needs.
func errEvidence(format string, args ...any) error {
	return fmt.Errorf("dispatch: the review's evidence: "+format, args...)
}

// SubmitReview records a review of the candidate r names on the live
// ticket with the key (review.submit), its findings with it, in one
// transaction: the review's row and its create row, then for a request
// its application pending at try 1 (set rows), then each finding's row and
// its create row, each finding an entity of its own. The candidate is
// <run>/<task>/<n>@<commit> (ParseCandidate) of a run of a chain the
// ticket holds alone (run_linked for another ticket's or a contested
// chain, invalid for a chain no ticket holds), the base a full commit; ev
// is the server's evidence, one entry per finding (errEvidence when it
// does not answer the request), stored with the rows (a check finding's
// receipt made to fit as the ledger may hold it: fitTail, fitError,
// fitCheckRef)
// and never part of the fingerprint, which is the key and r alone. Refused: an archived
// project (archived), a deleted ticket (not_found), a request on a closed
// ticket (invalid: "reopen the ticket to ask for changes"; a comment or
// an approval of a closed ticket's candidate is kept), past MaxReviews
// (bound), no finding and no summary, a request with no finding of intent
// change, a finding whose kind and fields disagree, a check finding of an
// external source or of a receipt not the candidate's (each invalid), and
// a review past MaxReviewDetail as its read encodes it (bound). An
// expected revision is the ticket's. The receipt carries the review's id
// and its rev, 1 or 2; the review is read after the commit as it stands,
// a resend's included (none for a refusal).
func (s *Store) SubmitReview(ctx context.Context, op Op, key string, r NewReview, ev ReviewEvidence) (Receipt, Review, error) {
	rc, err := s.Apply(ctx, op, IntentReviewSubmit, ReviewSubmitArgs{Key: key, Review: r}, func(t *tx) error {
		return t.submitReview(key, r, ev)
	})
	if err != nil || rc.Status != StatusDone || rc.ID == "" {
		return rc, Review{}, err
	}
	v, err := s.Review(ctx, rc.ID)
	return rc, v, err
}

// submitReview is review.submit's change.
func (t *tx) submitReview(key string, r NewReview, ev ReviewEvidence) error {
	tk, _, err := t.ticket(key, false)
	if err != nil {
		return err
	}
	if !slices.Contains(ReviewVerdicts, r.Verdict) {
		return refuse(RefusalInvalid, "the verdict %q is none of %s", r.Verdict, strings.Join(ReviewVerdicts, ", "))
	}
	request := r.Verdict == VerdictRequestChanges
	if request && closed(tk.Status) {
		return refuse(RefusalInvalid, "%s is %s: reopen the ticket to ask for changes", tk.Key, tk.Status)
	}
	c, err := ParseCandidate(r.Candidate)
	if err != nil {
		return err
	}
	if !objectIDPattern.MatchString(r.Base) {
		return refuse(RefusalInvalid, "the base %q is not a commit in full", r.Base)
	}
	if err := checkVia(r.Via); err != nil {
		return err
	}
	if err := checkField("review.summary", "the summary", r.Summary); err != nil {
		return err
	}
	if err := checkCount("findings of a review", len(r.Findings), MaxReviewFindings); err != nil {
		return err
	}
	if len(r.Findings) == 0 && r.Summary == "" {
		return refuse(RefusalInvalid, "a review holds a finding or a summary")
	}
	if request && !slices.ContainsFunc(r.Findings, func(f NewFinding) bool { return f.Intent == FindingIntentChange }) {
		return refuse(RefusalInvalid, "a request for changes asks for one: none of its findings is a change")
	}
	for i, f := range r.Findings {
		if err := checkFinding(i+1, c, f); err != nil {
			return err
		}
	}
	rows, err := bindEvidence(c, r, ev)
	if err != nil {
		return err
	}
	if err := t.holdsCandidate(tk, c.Run); err != nil {
		return err
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM reviews WHERE ticket_id = ?", tk.ID).Scan(&n); err != nil {
		return err
	}
	if err := checkCount("reviews of a ticket", n+1, MaxReviews); err != nil {
		return err
	}
	rv := reviewCreated{ID: newID(), TicketID: tk.ID, Candidate: r.Candidate, Base: r.Base, Verdict: r.Verdict, Summary: r.Summary, Via: r.Via,
		Actor: t.op.Actor, Fingerprint: t.fp, CreatedAt: t.now, CreatedBy: t.op.author()}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO reviews (id, ticket_id, candidate, base, verdict, summary, created_at, created_by, actor, via,
		fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rv.ID, rv.TicketID, rv.Candidate, rv.Base, rv.Verdict, rv.Summary, stamp(rv.CreatedAt), rv.CreatedBy, rv.Actor, rv.Via, rv.Fingerprint); err != nil {
		return err
	}
	t.created(rv.ID, "")
	value, err := encodeValue(rv)
	if err != nil {
		return err
	}
	base := ChangeRow{Entity: EntityReview, EntityID: rv.ID, TicketID: tk.ID, ProjectID: tk.ProjectID}
	create := base
	create.Op, create.Value = OpCreate, value
	if err := t.record(create); err != nil {
		return err
	}
	if request {
		if err := t.setScalars("reviews", base, []fieldValue{{"app_state", ApplicationNone}, {"app_try", "0"}},
			[]fieldValue{{"app_state", ApplicationPending}, {"app_try", "1"}}); err != nil {
			return err
		}
	}
	for _, f := range rows {
		f.ID, f.ReviewID, f.TicketID, f.CreatedAt = newID(), rv.ID, tk.ID, t.now
		if err := t.insertFinding(f, tk.ProjectID); err != nil {
			return err
		}
	}
	// The detail read's bound, measured on the review as its read will
	// encode it, its findings with it and no mark yet.
	v, err := readReview(t.ctx, t.tx, rv.ID)
	if err != nil {
		return err
	}
	detail, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(detail) > MaxReviewDetail {
		return refuse(RefusalBound, "the review is %d bytes as its read encodes it, more than %d: shorten its notes", len(detail), MaxReviewDetail)
	}
	return nil
}

// checkFinding checks the finding at place k of a review of the candidate
// c, as the client sent it: its kind and intent, a severity for a change
// and none otherwise, and the fields its kind takes and no others: a line
// finding a path, a side, a range of 1 to MaxFindingLine and its lines
// (1 to MaxAnchorLines of at most MaxAnchorLine bytes, no newline); a
// file finding a path and a side; a general finding none of them; a check
// finding its receipt alone, of the ledger and of the candidate's attempt.
// Every finding but a check's has words. An error is a Refusal.
func checkFinding(k int, c Candidate, f NewFinding) error {
	what := fmt.Sprintf("finding %d", k)
	if !slices.Contains(FindingKinds, f.Kind) {
		return refuse(RefusalInvalid, "%s: the kind %q is none of %s", what, f.Kind, strings.Join(FindingKinds, ", "))
	}
	if !slices.Contains(FindingIntents, f.Intent) {
		return refuse(RefusalInvalid, "%s: the intent %q is none of %s", what, f.Intent, strings.Join(FindingIntents, ", "))
	}
	switch {
	case f.Intent == FindingIntentChange && !slices.Contains(Severities, f.Severity):
		return refuse(RefusalInvalid, "%s: a change's severity %q is none of %s", what, f.Severity, strings.Join(Severities, ", "))
	case f.Intent != FindingIntentChange && f.Severity != "":
		return refuse(RefusalInvalid, "%s: a %s has no severity", what, f.Intent)
	}
	for _, x := range []struct{ key, label, value string }{
		{"finding.message", what + "'s words", f.Message},
		{"finding.resolution", what + "'s resolution", f.Resolution},
	} {
		if err := checkField(x.key, x.label, x.value); err != nil {
			return err
		}
	}
	located := f.Kind == FindingKindLine || f.Kind == FindingKindFile
	switch {
	case located && f.Path == "":
		return refuse(RefusalInvalid, "%s: a %s finding names its path", what, f.Kind)
	case located && f.Side != SideCandidate && f.Side != SideBase:
		return refuse(RefusalInvalid, "%s: the side %q is neither candidate nor base", what, f.Side)
	case !located && (f.Path != "" || f.Side != ""):
		return refuse(RefusalInvalid, "%s: a %s finding names no path and no side", what, f.Kind)
	case f.Kind != FindingKindLine && (f.From != 0 || f.To != 0 || len(f.Lines) > 0):
		return refuse(RefusalInvalid, "%s: only a line finding names lines", what)
	case f.Kind != FindingKindCheck && f.Check != nil:
		return refuse(RefusalInvalid, "%s: only a check finding names a receipt", what)
	case f.Kind == FindingKindCheck && f.Check == nil:
		return refuse(RefusalInvalid, "%s: a check finding names its receipt", what)
	case f.Kind != FindingKindCheck && strings.TrimSpace(f.Message) == "":
		return refuse(RefusalInvalid, "%s: a %s finding has words", what, f.Kind)
	}
	if located {
		if err := checkField("finding.path", what+"'s path", f.Path); err != nil {
			return err
		}
	}
	if f.Kind == FindingKindLine {
		switch {
		case f.From < 1 || f.From > MaxFindingLine || f.To < 1 || f.To > MaxFindingLine:
			return refuse(RefusalInvalid, "%s: the lines %d to %d are not 1 to %d", what, f.From, f.To, MaxFindingLine)
		case f.To < f.From:
			return refuse(RefusalInvalid, "%s: the lines end at %d, before they begin at %d", what, f.To, f.From)
		case len(f.Lines) == 0:
			return refuse(RefusalInvalid, "%s: a line finding keeps the lines as the person saw them", what)
		}
		if err := checkCount(what+"'s lines", len(f.Lines), MaxAnchorLines); err != nil {
			return err
		}
		for i, line := range f.Lines {
			label := fmt.Sprintf("%s's line %d", what, i+1)
			if err := checkField("finding.line", label, line); err != nil {
				return err
			}
			if strings.ContainsAny(line, "\n") {
				return refuse(RefusalInvalid, "%s holds a newline", label)
			}
		}
	}
	if f.Kind == FindingKindCheck {
		return checkCheckRequest(what, c, *f.Check)
	}
	return nil
}

// checkCheckRequest checks a check finding's receipt as the client named
// it: of the ledger (external is reserved), an identity of the
// candidate's run, task and attempt, a class chosen among
// FailureClasses or none, a quote by its rule. An error is a Refusal.
func checkCheckRequest(what string, c Candidate, r CheckRequest) error {
	switch r.Source {
	case CheckSourceLedger:
	case CheckSourceExternal:
		return refuse(RefusalInvalid, "%s: a receipt of an external CI job is reserved for a linked pull request's checks (Program item IN1a)", what)
	default:
		return refuse(RefusalInvalid, "%s: the receipt's source %q is not the ledger", what, r.Source)
	}
	ref, err := parseReceiptRef(r.Receipt)
	if err != nil {
		return err
	}
	if ref.run != c.Run || ref.task != c.Task || ref.attempt != c.Attempt {
		return refuse(RefusalInvalid, "%s: the receipt %s is of %s/%s attempt %d, not of the candidate reviewed (%s)", what, r.Receipt, ref.run, ref.task,
			ref.attempt, c.Name())
	}
	if r.Chosen != "" && !slices.Contains(FailureClasses, r.Chosen) {
		return refuse(RefusalInvalid, "%s: the class %q is none of %s", what, r.Chosen, strings.Join(FailureClasses, ", "))
	}
	return checkField("finding.quote", what+"'s quote", r.Quote)
}

// bindEvidence binds the evidence to the review's findings, which
// checkFinding has checked, and gives each finding's row but its ids and
// time: one entry per finding with its place, an anchor only on a line or
// a file finding and the side's commit (the candidate's or the base),
// and a check finding's receipt, of the identity the request named and of
// the server's filling (its shapes and bounds), its source, identity,
// chosen and quote the request's. Evidence that does not answer the
// request is errEvidence; a receipt that tested another commit than the
// candidate's, or did not finish and fail, is refused invalid.
func bindEvidence(c Candidate, r NewReview, ev ReviewEvidence) ([]findingCreated, error) {
	if len(ev.Findings) != len(r.Findings) {
		return nil, errEvidence("%d entries for %d findings", len(ev.Findings), len(r.Findings))
	}
	out := make([]findingCreated, len(r.Findings))
	for i, f := range r.Findings {
		k, e := i+1, ev.Findings[i]
		if e.K != k {
			return nil, errEvidence("entry %d is finding %d's", k, e.K)
		}
		anchor := ""
		switch {
		case f.Kind != FindingKindLine && f.Kind != FindingKindFile:
		case f.Side == SideBase:
			anchor = r.Base
		default:
			anchor = c.Commit
		}
		if e.AnchorCommit != anchor {
			return nil, errEvidence("finding %d is anchored on %q, not %q", k, e.AnchorCommit, anchor)
		}
		row := findingCreated{K: k, Kind: f.Kind, Intent: f.Intent, Severity: f.Severity, Path: f.Path, Side: f.Side, From: f.From, To: f.To,
			AnchorCommit: anchor, Lines: f.Lines, Message: f.Message, Resolution: f.Resolution}
		if row.Lines == nil {
			row.Lines = []string{}
		}
		switch {
		case f.Kind != FindingKindCheck && e.Check != nil:
			return nil, errEvidence("finding %d is no check's, and has a receipt", k)
		case f.Kind == FindingKindCheck:
			ref, err := bindCheck(k, c, *f.Check, e.Check)
			if err != nil {
				return nil, err
			}
			row.Check = ref
		}
		out[i] = row
	}
	return out, nil
}

// bindCheck is a check finding's receipt as it is kept: the server's
// filling (got) of the receipt the request named, checked to be that
// receipt's, of its shapes and bounds, with the request's source,
// identity, chosen and quote.
func bindCheck(k int, c Candidate, req CheckRequest, got *CheckRef) (*CheckRef, error) {
	if got == nil {
		return nil, errEvidence("finding %d is a check's, and has no receipt", k)
	}
	ref, _ := parseReceiptRef(req.Receipt)
	switch {
	case got.Receipt != req.Receipt || got.Run != ref.run || got.Task != ref.task || got.Attempt != ref.attempt || got.Phase != ref.phase ||
		got.ID != ref.id || got.K != ref.k:
		return nil, errEvidence("finding %d's receipt is %s, not the one it names, %s", k, got.Receipt, req.Receipt)
	case !slices.Contains(FailureClasses, got.Class):
		return nil, errEvidence("finding %d's class %q is none of %s", k, got.Class, strings.Join(FailureClasses, ", "))
	case len(got.Argv) > MaxCheckArgv:
		return nil, errEvidence("finding %d's argv holds %d arguments, more than %d", k, len(got.Argv), MaxCheckArgv)
	case !whereIDPattern.MatchString(got.Where):
		return nil, errEvidence("finding %d's receipt ran at %q, no runtime's name", k, got.Where)
	case got.LogSHA256 != "" && !sha256Pattern.MatchString(got.LogSHA256):
		return nil, errEvidence("finding %d's log hash is no SHA-256", k)
	case got.Previous != nil && got.Previous.Commit != "" && !objectIDPattern.MatchString(got.Previous.Commit):
		return nil, errEvidence("finding %d's attempt before has a commit not in full", k)
	case !slices.Contains(receiptStates, got.State):
		return nil, errEvidence("finding %d's receipt is %q, none of the ledger's states", k, got.State)
	case got.State == ReceiptFinished && got.Exit == nil:
		return nil, errEvidence("finding %d's receipt finished with no exit", k)
	}
	if got.Subject != c.Commit {
		return nil, refuse(RefusalInvalid, "finding %d: the receipt %s tested %s, not the candidate reviewed (%s)", k, req.Receipt, short(got.Subject), short(c.Commit))
	}
	if got.State == ReceiptStarted || got.State == ReceiptRunning {
		return nil, refuse(RefusalInvalid, "finding %d: the receipt %s has not finished", k, req.Receipt)
	}
	if got.passed() {
		return nil, refuse(RefusalInvalid, "finding %d: the receipt %s passed: nothing failed to repair", k, req.Receipt)
	}
	kept := *got
	kept.Source, kept.Receipt, kept.Chosen, kept.Quote = req.Source, req.Receipt, req.Chosen, req.Quote
	kept.Argv = slices.Clone(kept.Argv)
	if kept.Argv == nil {
		kept.Argv = []string{}
	}
	kept.Tail, kept.Error = fitTail(kept.Tail), fitError(kept.Error)
	size, err := fitCheckRef(&kept)
	if err != nil {
		return nil, err
	}
	if size > MaxCheckRef {
		return nil, errEvidence("finding %d's receipt is %d bytes with no argv, more than %d", k, size, MaxCheckRef)
	}
	return &kept, nil
}

// fitTail is a receipt's tail as a check finding keeps it: the last
// MaxCheckExcerpt bytes of the output, in UTF-8. The ledger keeps the
// output's last bytes cut at a byte offset, so the cut can fall inside a
// character and binary output is no UTF-8 at all: a character cut at the
// start is dropped, a run of bytes that is no UTF-8 becomes one U+FFFD,
// and what that grew past the bound goes from the start, a character at a
// time.
func fitTail(tail string) string {
	if len(tail) > MaxCheckExcerpt {
		tail = tail[len(tail)-MaxCheckExcerpt:]
	}
	for i := 0; i < utf8.UTFMax-1 && tail != "" && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	tail = strings.ToValidUTF8(tail, string(utf8.RuneError))
	for len(tail) > MaxCheckExcerpt {
		_, n := utf8.DecodeRuneInString(tail)
		tail = tail[n:]
	}
	return tail
}

// fitError is a receipt's words of its failure as a check finding keeps
// them: in UTF-8, at most MaxCheckError bytes. The engine cuts its words
// at a byte offset (session.CleanMessage), which can fall inside a
// character: a run of bytes that is no UTF-8 becomes one U+FFFD, and what
// that grew past the bound goes from the end, a character at a time.
func fitError(words string) string {
	words = strings.ToValidUTF8(words, string(utf8.RuneError))
	for len(words) > MaxCheckError {
		_, n := utf8.DecodeLastRuneInString(words)
		words = words[:len(words)-n]
	}
	return words
}

// fitCheckRef cuts the argv of a check finding's receipt from its end
// until the receipt encodes in MaxCheckRef bytes, and marks it cut
// (ArgvCut): a check's argv may hold the 8 KiB the engine allows, every
// byte a control character, six bytes as JSON, which is more than the
// bound beside the rest. Whole arguments go from the end, then the last one
// kept is cut to fit at a character's start; the whole argv stays the
// ledger's, by the receipt's identity. It returns the receipt's size as
// kept, over MaxCheckRef only when the rest is over it with no argv.
func fitCheckRef(ref *CheckRef) (int, error) {
	value, err := encodeValue(*ref)
	if err != nil || len(value) <= MaxCheckRef {
		return len(value), err
	}
	ref.ArgvCut = true
	if value, err = encodeValue(*ref); err != nil {
		return 0, err
	}
	// The argv encodes as [a,b,…]: an argument is its own encoding and,
	// but the first, a comma.
	over := len(value) - MaxCheckRef
	for over > 0 && len(ref.Argv) > 0 {
		last := len(ref.Argv) - 1
		arg := ref.Argv[last]
		size, err := encodedLen(arg)
		if err != nil {
			return 0, err
		}
		comma := min(last, 1)
		if size+comma <= over || size-over < len(`""`) {
			ref.Argv = ref.Argv[:last]
			over -= size + comma
			continue
		}
		// The longest start of arg whose encoding is size-over bytes at
		// most, cut at a character's start (an empty one fits).
		starts := []int{}
		for i := range arg {
			starts = append(starts, i)
		}
		lo, hi := 0, len(starts)-1 // starts[lo] fits; starts[0] is 0, ""
		for lo < hi {
			mid := (lo + hi + 1) / 2
			n, err := encodedLen(arg[:starts[mid]])
			if err != nil {
				return 0, err
			}
			if n <= size-over {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		ref.Argv[last] = arg[:starts[lo]]
		break
	}
	value, err = encodeValue(*ref)
	return len(value), err
}

// encodedLen is the bytes s takes as a stored value encodes it.
func encodedLen(s string) (int, error) {
	v, err := encodeValue(s)
	return len(v), err
}

// short is a commit as words name it: its first seven characters.
func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// holdsCandidate refuses a review or a mark of a candidate of the run
// unless the ticket tk holds the run's chain alone, inside the
// operation's transaction, as a delivery's candidate is held
// (candidateRefusal's rule, in a review's words): run_linked for another
// ticket's chain or a contested one, invalid for one no ticket holds.
func (t *tx) holdsCandidate(tk Ticket, run string) error {
	chain, err := t.sight.chainOf(run)
	if err != nil {
		return err
	}
	launched, linked, err := chainHolders(t.ctx, t.tx, t.sight, chain)
	if err != nil {
		return err
	}
	holders := heldBy(launched, linked)
	switch {
	case len(holders) == 1 && holders[0].TicketID == tk.ID:
		return nil
	case len(holders) == 1:
		h := holders[0]
		return &Refusal{Code: RefusalRunLinked, Key: h.Key,
			Words: fmt.Sprintf("the run %s is among %s's runs: a review names a candidate of its own ticket's runs", run, h.Key)}
	case len(holders) > 1:
		keys := make([]string, len(holders))
		for i, h := range holders {
			keys[i] = h.Key
		}
		return &Refusal{Code: RefusalRunLinked, Key: keys[0],
			Words: fmt.Sprintf("the chain of run %s is contested: %s each hold a run of it; settle the chain first", run, strings.Join(keys, " and "))}
	}
	return refuse(RefusalInvalid, "the run %s is none of %s's runs: neither launched from it nor linked to it", run, tk.Key)
}

// insertFinding writes a finding's row and its create row, the finding
// whole.
func (t *tx) insertFinding(f findingCreated, projectID string) error {
	lines, err := encodeValue(f.Lines)
	if err != nil {
		return err
	}
	check := ""
	if f.Check != nil {
		if check, err = encodeValue(f.Check); err != nil {
			return err
		}
	}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO review_findings (id, review_id, ticket_id, k, kind, intent, severity, path, side, from_line,
		to_line, anchor_commit, lines, message, resolution, check_ref, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ReviewID, f.TicketID, f.K, f.Kind, f.Intent, f.Severity, f.Path, f.Side, f.From, f.To, f.AnchorCommit, lines, f.Message,
		f.Resolution, check, stamp(f.CreatedAt)); err != nil {
		return err
	}
	value, err := encodeValue(f)
	if err != nil {
		return err
	}
	return t.record(ChangeRow{Entity: EntityFinding, EntityID: f.ID, TicketID: f.TicketID, ProjectID: projectID, Op: OpCreate, Value: value})
}

// reviewIDArgs is an operation's content that names a review alone.
type reviewIDArgs struct {
	ID string `json:"id"`
}

// ApplyingReview makes the refused request with the id pending again as
// its next try (review.retry): app_state pending, app_try one more, its
// why and time cleared. Refused: a review that is not a request (invalid),
// one not refused (invalid, in its state's words), one at MaxReviewTries
// already (bound), a deleted ticket (not_found), an archived project
// (archived), a closed ticket (invalid: "reopen the ticket to ask for
// changes"). An expected revision is the review's. The review is read
// after the commit as it stands (none for a refusal). ErrNotFound for an
// id not of an id's shape.
func (s *Store) ApplyingReview(ctx context.Context, op Op, id string) (Receipt, Review, error) {
	if !ValidID(id) {
		return Receipt{}, Review{}, ErrNotFound
	}
	rc, err := s.Apply(ctx, op, IntentReviewRetry, reviewIDArgs{ID: id}, func(t *tx) error {
		base, rv, err := t.reviewRow(id)
		if err != nil {
			return err
		}
		if err := t.expect(rv.Rev); err != nil {
			return err
		}
		tk, err := ticketByID(t.ctx, t.tx, rv.TicketID)
		if err != nil {
			return err
		}
		if !tk.DeletedAt.IsZero() {
			return refuse(RefusalNotFound, "%s is deleted", tk.Key)
		}
		if _, err := t.open(tk); err != nil {
			return err
		}
		switch {
		case rv.Verdict != VerdictRequestChanges:
			return refuse(RefusalInvalid, "the review %s is a %s, applied to nothing", id, rv.Verdict)
		case rv.Application.State != ApplicationRefused:
			return refuse(RefusalInvalid, "the review %s is %s: only a refused request is sent again", id, rv.Application.State)
		case rv.Application.Try >= MaxReviewTries:
			return refuse(RefusalBound, "the review %s was tried %d times, the most a review is: send a new review", id, rv.Application.Try)
		case closed(tk.Status):
			return refuse(RefusalInvalid, "%s is %s: reopen the ticket to ask for changes", tk.Key, tk.Status)
		}
		before, err := t.scalars("reviews", id, applicationColumns)
		if err != nil {
			return err
		}
		after := fieldValues(before)
		setField(after, "app_state", ApplicationPending)
		setField(after, "app_try", strconv.Itoa(rv.Application.Try+1))
		setField(after, "app_why", "")
		setField(after, "app_at", "")
		return t.setScalars("reviews", base, before, after)
	})
	if err != nil || rc.Status != StatusDone {
		return rc, Review{}, err
	}
	v, err := s.Review(ctx, id)
	return rc, v, err
}

// applicationColumns is a review's application's columns, in the order an
// outcome records them.
var applicationColumns = []string{"app_state", "app_try", "app_run", "app_command", "app_revision", "app_attempt", "app_why", "app_at"}

// reviewTryArgs is an outcome's content: the review and the try it
// settles.
type reviewTryArgs struct {
	ID  string `json:"id"`
	Try int    `json:"try"`
}

// AppliedReview records the try of the request with the id applied
// (review.applied), as the ledger's owner read it: app_state applied, the
// run, the command, the revision and the attempt, and the time. The
// operation is the actor server's under the op id <id>.<try>.applied,
// whatever op names (its display name alone is kept), so a second record
// of one outcome is the first's repeat. It settles only its own try: a
// try settled already, or a review past it, is invalid and kept (a late
// answer of an earlier try changes nothing); a try not yet begun, and an
// outcome whose fields are not of their shapes (a run id of the engine's,
// a command id, a revision from 0, an attempt from 1), are answered
// failed with no receipt kept, so a later record may be done. A review
// that is not a request is invalid; none is not_found. It is recorded
// whatever became of the ticket and its project since, since the repair
// was made. ErrNotFound for an id not of an id's shape or a try not 1 to
// MaxReviewTries.
func (s *Store) AppliedReview(ctx context.Context, op Op, id string, try int, a Applied) (Receipt, error) {
	return s.settleReview(ctx, op, id, try, IntentReviewApplied, "applied", func() ([]fieldValue, error) {
		switch {
		case !validRunID(a.Run):
			return nil, refuse(RefusalInvalid, "the run %q is not a run id", a.Run)
		case !OpIDPattern.MatchString(a.Command):
			return nil, refuse(RefusalInvalid, "the command %q is not a command id", a.Command)
		case a.Revision < 0:
			return nil, refuse(RefusalInvalid, "the run's revision %d is below 0", a.Revision)
		case a.Attempt < 1 || a.Attempt > 999999:
			return nil, refuse(RefusalInvalid, "the attempt %d is not 1 to 999999", a.Attempt)
		}
		return []fieldValue{{"app_state", ApplicationApplied}, {"app_run", a.Run}, {"app_command", a.Command},
			{"app_revision", strconv.FormatInt(a.Revision, 10)}, {"app_attempt", strconv.Itoa(a.Attempt)}}, nil
	})
}

// RefusedReview records the try of the request with the id refused
// (review.refused), with why, in at most MaxReviewWhy characters (cut, its
// control characters but newlines and tabs made spaces; empty is
// "Yardmaster did not apply the request"): app_state refused, why and the
// time. As AppliedReview, the operation is the actor server's under the
// op id <id>.<try>.refused, and it settles only its own try.
func (s *Store) RefusedReview(ctx context.Context, op Op, id string, try int, why string) (Receipt, error) {
	return s.settleReview(ctx, op, id, try, IntentReviewRefused, "refused", func() ([]fieldValue, error) {
		return []fieldValue{{"app_state", ApplicationRefused}, {"app_why", reviewWhy(why)}}, nil
	})
}

// settleReview is AppliedReview's and RefusedReview's operation: fields
// gives the outcome's fields (a refusal it gives is unkept), set beside
// the time on the review pending at that try.
func (s *Store) settleReview(ctx context.Context, op Op, id string, try int, intent, suffix string, fields func() ([]fieldValue, error)) (Receipt, error) {
	if !ValidID(id) || try < 1 || try > MaxReviewTries {
		return Receipt{}, ErrNotFound
	}
	op = outcomeOp(op, id+"."+strconv.Itoa(try)+"."+suffix)
	rc, err := s.Apply(ctx, op, intent, reviewTryArgs{ID: id, Try: try}, func(t *tx) error {
		base, rv, err := t.reviewRow(id)
		if err != nil {
			return err
		}
		switch {
		case rv.Verdict != VerdictRequestChanges:
			return refuse(RefusalInvalid, "the review %s is a %s, applied to nothing", id, rv.Verdict)
		case rv.Application.Try < try:
			return unkeptRefusal{&Refusal{Code: RefusalInvalid, Words: fmt.Sprintf("the review %s's try %d has not begun: it is at try %d", id, try, rv.Application.Try)}}
		case rv.Application.Try > try:
			return refuse(RefusalInvalid, "the review %s's try %d is over: it is at try %d", id, try, rv.Application.Try)
		case rv.Application.State != ApplicationPending:
			return refuse(RefusalInvalid, "the review %s's try %d is %s already", id, try, rv.Application.State)
		}
		after, err := fields()
		if err != nil {
			var ref *Refusal
			if errors.As(err, &ref) {
				return unkeptRefusal{ref}
			}
			return err
		}
		before, err := t.scalars("reviews", id, applicationColumns)
		if err != nil {
			return err
		}
		return t.setScalars("reviews", base, before, append(after, fieldValue{"app_at", stamp(t.now)}))
	})
	var unkept unkeptRefusal
	if errors.As(err, &unkept) {
		r := unkept.ref
		return fitWords(Receipt{OpID: op.ID, Intent: intent, Status: StatusFailed, Refusal: r.Code, Error: words(r.Words)}), nil
	}
	return rc, err
}

// reviewWhy is why a request's application was refused as its column
// holds it: text (fitText, newlines and tabs kept), cut to MaxReviewWhy
// characters, "Yardmaster did not apply the request" for none.
func reviewWhy(why string) string {
	why = strings.TrimSpace(fitText(why, true))
	if why == "" {
		return "Yardmaster did not apply the request"
	}
	if r := []rune(why); len(r) > MaxReviewWhy {
		why = string(r[:MaxReviewWhy-1]) + "…"
	}
	return why
}

// reviewRow reads the review with the id inside an operation: not_found
// when there is none. It gives the base of its rows (entity review, its
// ticket and project) and the review's summary (its counts not read).
func (t *tx) reviewRow(id string) (ChangeRow, ReviewSummary, error) {
	rv, err := scanReview(t.tx.QueryRowContext(t.ctx, "SELECT "+reviewColumns+reviewFrom+" WHERE r.id = ?", id))
	switch {
	case isNoRows(err):
		return ChangeRow{}, ReviewSummary{}, refuse(RefusalNotFound, "no review %s", id)
	case err != nil:
		return ChangeRow{}, ReviewSummary{}, err
	}
	var projectID string
	if err := t.tx.QueryRowContext(t.ctx, "SELECT project_id FROM tickets WHERE id = ?", rv.TicketID).Scan(&projectID); err != nil {
		return ChangeRow{}, ReviewSummary{}, err
	}
	return ChangeRow{Entity: EntityReview, EntityID: id, TicketID: rv.TicketID, ProjectID: projectID}, rv, nil
}

// MarkFinding marks the finding with the id against a candidate
// (finding.mark): one mark's row, a row of the finding's marks log, and
// one add row of the finding's marks naming it whole, with located, what
// the server worked out of where the finding's lines are on that
// candidate (JSON of at most MaxMarkLocated bytes, or none; an error
// otherwise, never a refusal, so nothing is kept and the same op id may be
// sent again with it right), kept with the mark and never part of the
// fingerprint, which is the id and m alone.
// The candidate is of the finding's task, of a run of a chain the
// finding's ticket holds alone (holdsCandidate). Refused: a resolved mark
// on a candidate whose commit is the finding's own candidate's, whatever
// its attempt ("resolve it on a candidate that changed the code"), a
// verdict not among FindingStates, a note past its bound, past MaxMarks
// (bound), a deleted ticket (not_found), an archived project (archived).
// That the candidate is a later attempt of the finding's task in the
// ticket's chain is the API's check, which reads the ledger. The receipt
// carries the mark's id; the mark is read after the commit (none for a
// refusal). ErrNotFound for an id not of an id's shape.
func (s *Store) MarkFinding(ctx context.Context, op Op, id string, m NewMark, located json.RawMessage) (Receipt, Mark, error) {
	if !ValidID(id) {
		return Receipt{}, Mark{}, ErrNotFound
	}
	rc, err := s.Apply(ctx, op, IntentFindingMark, FindingMarkArgs{ID: id, Mark: m}, func(t *tx) error {
		return t.markFinding(id, m, located)
	})
	if err != nil || rc.Status != StatusDone || rc.ID == "" {
		return rc, Mark{}, err
	}
	var out Mark
	err = s.read(ctx, func(q queryer) error {
		var err error
		out, err = scanMark(q.QueryRowContext(ctx, "SELECT "+markColumns+" FROM finding_marks WHERE id = ?", rc.ID))
		return err
	})
	return rc, out, err
}

// markFinding is finding.mark's change.
func (t *tx) markFinding(id string, m NewMark, located json.RawMessage) error {
	var ticketID, reviewed string
	switch err := t.tx.QueryRowContext(t.ctx, "SELECT f.ticket_id, r.candidate FROM review_findings f JOIN reviews r ON r.id = f.review_id WHERE f.id = ?",
		id).Scan(&ticketID, &reviewed); {
	case isNoRows(err):
		return refuse(RefusalNotFound, "no finding %s", id)
	case err != nil:
		return err
	}
	tk, err := ticketByID(t.ctx, t.tx, ticketID)
	if err != nil {
		return err
	}
	if !tk.DeletedAt.IsZero() {
		return refuse(RefusalNotFound, "%s is deleted", tk.Key)
	}
	if _, err := t.open(tk); err != nil {
		return err
	}
	if !slices.Contains(FindingStates, m.Verdict) {
		return refuse(RefusalInvalid, "the verdict %q is none of %s", m.Verdict, strings.Join(FindingStates, ", "))
	}
	c, err := ParseCandidate(m.Candidate)
	if err != nil {
		return err
	}
	own, err := ParseCandidate(reviewed)
	if err != nil {
		return fmt.Errorf("dispatch: the finding %s's candidate: %w", id, err)
	}
	switch {
	case c.Task != own.Task:
		return refuse(RefusalInvalid, "the candidate is of the task %s; the finding is of %s's", c.Task, own.Task)
	case m.Verdict == FindingResolved && c.Commit == own.Commit:
		return refuse(RefusalInvalid, "%s holds the code the finding was made on (%s): resolve it on a candidate that changed the code", c.Name(), short(own.Commit))
	}
	if err := checkField("mark.note", "the note", m.Note); err != nil {
		return err
	}
	if err := checkVia(m.Via); err != nil {
		return err
	}
	if len(located) > 0 {
		if err := checkField("mark.located", "where the lines are", string(located)); err != nil {
			// Located is the server's, outside the fingerprint: a plain
			// error, never a refusal kept for the same op id.
			return fmt.Errorf("dispatch: the mark's evidence: %v", err)
		}
	}
	if err := t.holdsCandidate(tk, c.Run); err != nil {
		return err
	}
	var n int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM finding_marks WHERE finding_id = ?", id).Scan(&n); err != nil {
		return err
	}
	if err := checkCount("marks of a finding", n+1, MaxMarks); err != nil {
		return err
	}
	mk := Mark{ID: newID(), FindingID: id, TicketID: tk.ID, Candidate: m.Candidate, Verdict: m.Verdict, Located: located, Note: m.Note,
		CreatedAt: t.now, CreatedBy: t.op.author(), Actor: t.op.Actor, Via: m.Via, OpID: t.op.ID, Fingerprint: t.fp}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO finding_marks (id, finding_id, ticket_id, candidate, verdict, located, note, created_at,
		created_by, actor, via, op_id, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mk.ID, mk.FindingID, mk.TicketID, mk.Candidate, mk.Verdict, string(located), mk.Note, stamp(mk.CreatedAt), mk.CreatedBy, mk.Actor,
		mk.Via, mk.OpID, mk.Fingerprint); err != nil {
		return err
	}
	t.created(mk.ID, "")
	value, err := encodeValue(markAdded{Mark: mk, OpID: mk.OpID, Fingerprint: mk.Fingerprint})
	if err != nil {
		return err
	}
	return t.record(ChangeRow{Entity: EntityFinding, EntityID: id, TicketID: tk.ID, ProjectID: tk.ProjectID, Field: "marks", Op: OpAdd, Value: value})
}

// reviewColumns is a review's row as scanReview reads it, from reviews r
// joined to its ticket t.
const reviewColumns = `r.id, r.ticket_id, t.key, r.candidate, r.base, r.verdict, r.summary, r.created_at, r.created_by, r.actor, r.via,
	r.fingerprint, r.app_state, r.app_try, r.app_run, r.app_command, r.app_revision, r.app_attempt, r.app_why, r.app_at, r.rev`

// reviewFrom is the reviews joined to their tickets, as reviewColumns
// reads them.
const reviewFrom = " FROM reviews r JOIN tickets t ON t.id = r.ticket_id"

// scanReview reads a review's row (reviewColumns), its counts not.
func scanReview(row interface{ Scan(...any) error }) (ReviewSummary, error) {
	var r ReviewSummary
	var created, at string
	a := &r.Application
	if err := row.Scan(&r.ID, &r.TicketID, &r.TicketKey, &r.Candidate, &r.Base, &r.Verdict, &r.Summary, &created, &r.CreatedBy, &r.Actor, &r.Via,
		&r.Fingerprint, &a.State, &a.Try, &a.Run, &a.Command, &a.Revision, &a.Attempt, &a.Why, &at, &r.Rev); err != nil {
		return ReviewSummary{}, err
	}
	r.CreatedAt, a.At = unstamp(created), unstamp(at)
	return r, nil
}

// findingState is a finding f's state as SQL reads it: its latest mark's
// verdict in the log's one order, open with none.
const findingState = `COALESCE((SELECT m.verdict FROM finding_marks m WHERE m.finding_id = f.id
	ORDER BY m.created_at DESC, m.actor DESC, m.op_id DESC LIMIT 1), 'open')`

// countFindings counts the findings of the review with the id by state.
func countFindings(ctx context.Context, q queryer, id string) (FindingCounts, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+findingState+" AS state, COUNT(*) FROM review_findings f WHERE f.review_id = ? GROUP BY state", id)
	if err != nil {
		return FindingCounts{}, err
	}
	defer rows.Close()
	var c FindingCounts
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return FindingCounts{}, err
		}
		switch state {
		case FindingResolved:
			c.Resolved += n
		case FindingWithdrawn:
			c.Withdrawn += n
		default:
			c.Open += n
		}
	}
	return c, rows.Err()
}

// scanReviews reads the reviews a query of reviewColumns gives, each with
// its findings counted.
func scanReviews(ctx context.Context, q queryer, query string, args ...any) ([]ReviewSummary, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := []ReviewSummary{}
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Counts, err = countFindings(ctx, q, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// findingColumns is a finding's row as scanFinding reads it, from
// review_findings f joined to its review r: its review's candidate, its
// state and its marks' count beside it.
const findingColumns = `f.id, f.review_id, f.ticket_id, f.k, f.kind, f.intent, f.severity, f.path, f.side, f.from_line, f.to_line,
	f.anchor_commit, f.lines, f.message, f.resolution, f.check_ref, f.created_at, r.candidate, ` + findingState + `,
	(SELECT COUNT(*) FROM finding_marks m WHERE m.finding_id = f.id)`

// findingFrom is the findings joined to their reviews, as findingColumns
// reads them.
const findingFrom = " FROM review_findings f JOIN reviews r ON r.id = f.review_id"

// scanFinding reads a finding's row (findingColumns), its latest mark not.
func scanFinding(row interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var lines, check, created string
	if err := row.Scan(&f.ID, &f.ReviewID, &f.TicketID, &f.K, &f.Kind, &f.Intent, &f.Severity, &f.Path, &f.Side, &f.From, &f.To, &f.AnchorCommit,
		&lines, &f.Message, &f.Resolution, &check, &created, &f.Candidate, &f.State, &f.Marks); err != nil {
		return Finding{}, err
	}
	if err := json.Unmarshal([]byte(lines), &f.Lines); err != nil {
		return Finding{}, fmt.Errorf("dispatch: the lines of finding %s: %w", f.ID, err)
	}
	if f.Lines == nil {
		f.Lines = []string{}
	}
	if check != "" {
		f.Check = &CheckRef{}
		if err := json.Unmarshal([]byte(check), f.Check); err != nil {
			return Finding{}, fmt.Errorf("dispatch: the receipt of finding %s: %w", f.ID, err)
		}
	}
	f.CreatedAt = unstamp(created)
	return f, nil
}

// scanFindings reads the findings a query of findingColumns gives, each
// with its latest mark (latestMarks).
func scanFindings(ctx context.Context, q queryer, query string, args ...any) ([]Finding, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, latestMarks(ctx, q, out)
}

// latestMarks sets each finding's latest mark, in the log's one order,
// without where it located the lines (Marks reads that).
func latestMarks(ctx context.Context, q queryer, fs []Finding) error {
	for i := range fs {
		if fs[i].Marks == 0 {
			continue
		}
		m, err := scanMark(q.QueryRowContext(ctx, "SELECT "+markColumns+" FROM finding_marks WHERE finding_id = ? ORDER BY created_at DESC, actor DESC, op_id DESC LIMIT 1",
			fs[i].ID))
		if err != nil {
			return err
		}
		m.Located = nil
		fs[i].Latest = &m
	}
	return nil
}

// markColumns is a mark's row as scanMark reads it.
const markColumns = "id, finding_id, ticket_id, candidate, verdict, located, note, created_at, created_by, actor, via, op_id, fingerprint"

// scanMark reads a mark's row (markColumns).
func scanMark(row interface{ Scan(...any) error }) (Mark, error) {
	var m Mark
	var located, created string
	if err := row.Scan(&m.ID, &m.FindingID, &m.TicketID, &m.Candidate, &m.Verdict, &located, &m.Note, &created, &m.CreatedBy, &m.Actor, &m.Via,
		&m.OpID, &m.Fingerprint); err != nil {
		return Mark{}, err
	}
	if located != "" {
		m.Located = json.RawMessage(located)
	}
	m.CreatedAt = unstamp(created)
	return m, nil
}

// readReview reads the review with the id whole through q: its summary,
// its findings counted, and its findings in their order, each with its
// latest mark. sql.ErrNoRows when there is none.
func readReview(ctx context.Context, q queryer, id string) (Review, error) {
	r, err := scanReview(q.QueryRowContext(ctx, "SELECT "+reviewColumns+reviewFrom+" WHERE r.id = ?", id))
	if err != nil {
		return Review{}, err
	}
	if r.Counts, err = countFindings(ctx, q, id); err != nil {
		return Review{}, err
	}
	fs, err := scanFindings(ctx, q, "SELECT "+findingColumns+findingFrom+" WHERE f.review_id = ? ORDER BY f.k, f.id", id)
	if err != nil {
		return Review{}, err
	}
	return Review{ReviewSummary: r, Findings: fs}, nil
}

// Review reads the review with the id whole: its summary, its findings
// counted by state, and its findings in their order, each with its marks
// counted and its latest mark. ErrNotFound when there is none; a version
// 2 database, read as it was found, has none.
func (s *Store) Review(ctx context.Context, id string) (Review, error) {
	var out Review
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 3 {
			return ErrNotFound
		}
		var err error
		out, err = readReview(ctx, q, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// Finding reads the finding with the id, with its review's candidate, its
// state, its marks counted and its latest mark, and the key of its
// ticket: what a mark's route checks the mark against (the API's,
// docs/plans/review-repair.md, PR-4). ErrNotFound when there is none; a
// version 2 database, read as it was found, has none.
func (s *Store) Finding(ctx context.Context, id string) (Finding, string, error) {
	var out Finding
	var key string
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 3 || !ValidID(id) {
			return ErrNotFound
		}
		fs, err := scanFindings(ctx, q, "SELECT "+findingColumns+findingFrom+" WHERE f.id = ?", id)
		if err != nil {
			return err
		}
		if len(fs) == 0 {
			return ErrNotFound
		}
		out = fs[0]
		return q.QueryRowContext(ctx, "SELECT key FROM tickets WHERE id = ?", out.TicketID).Scan(&key)
	})
	return out, key, err
}

// Reviews reads a page of the reviews of the ticket with the key, deleted
// or not, newest first: ReviewsPerPage of them before the review with the id
// before ("" for the newest), each with its findings counted by state,
// and the next page's cursor when there are more. ErrNotFound when there
// is no such ticket; ErrBadQuery for a cursor that is none of the
// ticket's reviews; a version 2 database, read as it was found, has none.
func (s *Store) Reviews(ctx context.Context, key, before string) (ReviewsPage, error) {
	var out ReviewsPage
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = ReviewsPage{Reviews: []ReviewSummary{}}
		if schemaOf(q) < 3 {
			if before != "" {
				return badQuery("the cursor is none of %s's reviews", key)
			}
			return nil
		}
		where, args := " WHERE r.ticket_id = ?", []any{tk.ID}
		if before != "" {
			var at string
			if err := q.QueryRowContext(ctx, "SELECT created_at FROM reviews WHERE id = ? AND ticket_id = ?", before, tk.ID).Scan(&at); err != nil {
				if isNoRows(err) {
					return badQuery("the cursor is none of %s's reviews", key)
				}
				return err
			}
			where += " AND (r.created_at, r.id) < (?, ?)"
			args = append(args, at, before)
		}
		rs, err := scanReviews(ctx, q, "SELECT "+reviewColumns+reviewFrom+where+" ORDER BY r.created_at DESC, r.id DESC LIMIT ?", append(args, ReviewsPerPage+1)...)
		if err != nil {
			return err
		}
		if len(rs) > ReviewsPerPage {
			rs = rs[:ReviewsPerPage]
			out.Next = rs[len(rs)-1].ID
		}
		out.Reviews = rs
		return nil
	})
	return out, err
}

// PendingReviews reads every request whose application is pending, oldest
// first: what the API's reconcile at start settles by what the ledger
// holds. A version 2 database, read as it was found, has none.
func (s *Store) PendingReviews(ctx context.Context) ([]ReviewSummary, error) {
	var out []ReviewSummary
	err := s.read(ctx, func(q queryer) error {
		out = []ReviewSummary{}
		if schemaOf(q) < 3 {
			return nil
		}
		var err error
		out, err = scanReviews(ctx, q, "SELECT "+reviewColumns+reviewFrom+" WHERE r.app_state = ? ORDER BY r.created_at, r.id", ApplicationPending)
		return err
	})
	return out, err
}

// pageEnvelope is the room a page's reply takes beside its entries: its
// object, its list's brackets and its next cursor.
const pageEnvelope = 64

// Marks reads a page of the marks of the finding with the id, oldest
// first in the log's one order: those after the mark with the id after
// ("" for the first), at most MarksPerPage, and fewer when the next would
// take the page past MaxMarksReply as its marks encode (Go's JSON, its
// escapes counted), its first mark whatever its size, with the next
// page's cursor when there are more. ErrNotFound when there is no such
// finding; ErrBadQuery for a cursor that is none of its marks.
func (s *Store) Marks(ctx context.Context, findingID, after string) (MarksPage, error) {
	var out MarksPage
	err := s.read(ctx, func(q queryer) error {
		if schemaOf(q) < 3 {
			return ErrNotFound
		}
		var n int
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM review_findings WHERE id = ?", findingID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		where, args := " WHERE finding_id = ?", []any{findingID}
		if after != "" {
			var at, actor, op string
			if err := q.QueryRowContext(ctx, "SELECT created_at, actor, op_id FROM finding_marks WHERE id = ? AND finding_id = ?", after, findingID).Scan(&at, &actor, &op); err != nil {
				if isNoRows(err) {
					return badQuery("the cursor is none of the finding's marks")
				}
				return err
			}
			where += " AND (created_at, actor, op_id) > (?, ?, ?)"
			args = append(args, at, actor, op)
		}
		rows, err := q.QueryContext(ctx, "SELECT "+markColumns+" FROM finding_marks"+where+" ORDER BY created_at, actor, op_id LIMIT ?", append(args, MarksPerPage+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = MarksPage{Marks: []Mark{}}
		size := pageEnvelope
		for rows.Next() {
			m, err := scanMark(rows)
			if err != nil {
				return err
			}
			b, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if len(out.Marks) == MarksPerPage || (len(out.Marks) > 0 && size+len(b)+1 > MaxMarksReply) {
				out.Next = out.Marks[len(out.Marks)-1].ID
				break
			}
			out.Marks = append(out.Marks, m)
			size += len(b) + 1
		}
		return rows.Err()
	})
	return out, err
}

// Findings reads a page of the findings of the ticket with the key,
// deleted or not, narrowed by the filter (a state, a task), in their
// reviews' order and their own: those after the finding with the id after
// ("" for the first), at most FindingsPerPage, and fewer when the next would
// take the page past MaxFindingsReply, each finding counted as it encodes
// (Go's JSON, its escapes counted) and FindingReplyReserve beside it, its
// first finding whatever its size, with the next page's cursor when there
// are more. Each finding has its review's candidate, its state, its marks
// counted and its latest mark. ErrNotFound when there is no such ticket;
// ErrBadQuery for a state none of FindingStates, a task not of a task
// id's shape, or a cursor that is none of the ticket's findings; a
// version 2 database, read as it was found, has none.
func (s *Store) Findings(ctx context.Context, key string, filter FindingFilter, after string) (FindingsPage, error) {
	if filter.State != "" && !slices.Contains(FindingStates, filter.State) {
		return FindingsPage{}, badQuery("the state %q is none of %s", filter.State, strings.Join(FindingStates, ", "))
	}
	if filter.Task != "" && !taskIDPattern.MatchString(filter.Task) {
		return FindingsPage{}, badQuery("the task %q is not a task's id", filter.Task)
	}
	var out FindingsPage
	err := s.read(ctx, func(q queryer) error {
		tk, err := ticketByKey(ctx, q, key)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = FindingsPage{Findings: []Finding{}}
		if schemaOf(q) < 3 {
			if after != "" {
				return badQuery("the cursor is none of %s's findings", key)
			}
			return nil
		}
		where, args := " WHERE f.ticket_id = ?", []any{tk.ID}
		if filter.State != "" {
			where += " AND " + findingState + " = ?"
			args = append(args, filter.State)
		}
		if filter.Task != "" {
			// The candidate is <run>/<task>/<n>@<commit>, and a run id holds
			// no '/': the task is between its first two.
			where += " AND instr(r.candidate, '/' || ? || '/') = instr(r.candidate, '/')"
			args = append(args, filter.Task)
		}
		if after != "" {
			var at, review string
			var k int
			if err := q.QueryRowContext(ctx, "SELECT created_at, review_id, k FROM review_findings WHERE id = ? AND ticket_id = ?", after, tk.ID).Scan(&at, &review, &k); err != nil {
				if isNoRows(err) {
					return badQuery("the cursor is none of %s's findings", key)
				}
				return err
			}
			where += " AND (f.created_at, f.review_id, f.k, f.id) > (?, ?, ?, ?)"
			args = append(args, at, review, k, after)
		}
		fs, err := scanFindings(ctx, q, "SELECT "+findingColumns+findingFrom+where+" ORDER BY f.created_at, f.review_id, f.k, f.id LIMIT ?",
			append(args, FindingsPerPage+1)...)
		if err != nil {
			return err
		}
		size := pageEnvelope
		for _, f := range fs {
			b, err := json.Marshal(f)
			if err != nil {
				return err
			}
			n := len(b) + 1 + FindingReplyReserve
			if len(out.Findings) == FindingsPerPage || (len(out.Findings) > 0 && size+n > MaxFindingsReply) {
				out.Next = out.Findings[len(out.Findings)-1].ID
				break
			}
			out.Findings = append(out.Findings, f)
			size += n
		}
		return nil
	})
	return out, err
}
