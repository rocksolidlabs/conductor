package crew

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Durable decisions (Program item DR1, docs/plans/decisions.md, The changes,
// internal/crew). A decision request is a question an attempt's agent, or a
// ticket's plan revision (a gate), puts to a person, kept in the ledger with
// its scope, the response a person gives and the receipt of Yardmaster
// applying it. Yardmaster owns the wait: a decision holds the tasks it is
// about (the asking task, or the tasks a gate's plan decision names
// before), and pump dispatches no held task; an answer is recorded
// (RespondDecision), then validated and applied (applyDecisions) in a
// transaction of its own: typed into the asking attempt's live session by
// its member's deliverer, or its holds released (a gate's tasks may
// start), or a rejected gate's tasks cancelled. Every change of a decision
// is a transaction of the run that holds it, with its task rows, its
// history and the run's event; an attempt's session shows the decision as
// its agent's own question (session.Local.TryAskDecision), which only the
// decision's application answers.
//
// The keys a decision is looked up by, and which a caller controls: the
// package's doc comment (crew.go).

// The policy of this repository (local/1): one response from a principal
// with the admin grant decides a decision.
const (
	policyLocal = "local/1"
	// grantAdmin is an authority's admin grant (ext.GrantAdmin's words).
	grantAdmin = "admin"
	// The ways a response's principal authenticated (Authority.Via).
	ViaWorkbench = "workbench"
	ViaIdentity  = ledger.ViaIdentity
	ViaRouter    = "router"
)

// policyJSON is the policy every decision of this repository is opened
// under.
const policyJSON = `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`

// Bounds of a response (The changes, Responding).
const (
	maxAnswerText    = 4096    // ext.MaxAnswerText
	maxRationale     = 2 << 10 // as encoded
	maxDecisionTries = 3       // a response retried against the run that holds it
)

// The codes a decision's refusal names (DecisionError.Code), the routes'
// error codes (internal/api, PR-4).
const (
	CodeInvalid             = "invalid"
	CodeNotFound            = "not_found"
	CodeNotRunning          = "not_running"
	CodeArtifactUncommitted = "artifact_uncommitted"
	CodeDecisionConflict    = "decision_conflict"
	CodeDecisionLimit       = "decision_limit"
	CodeDecisionOpen        = "decision_open"
	CodeDecisionAnswered    = "decision_answered"
	CodeDecisionClosed      = "decision_closed"
	CodeStaleDecision       = "stale_decision"
	CodeStalePolicy         = "stale_policy"
	CodeResponseConflict    = "response_conflict"
	CodeNotHeld             = "not_held"
	CodeNotApprover         = "not_approver"
	CodeDecisionMoved       = "decision_moved"
)

// ErrDecision matches every DecisionError (errors.Is).
var ErrDecision = errors.New("decision refused")

// DecisionError is a decision's refusal: its code and its words, which say
// what a person or an agent does next.
type DecisionError struct {
	Code  string
	Words string
}

func (e *DecisionError) Error() string        { return e.Words }
func (e *DecisionError) Is(target error) bool { return target == ErrDecision }

func refuse(code, format string, args ...any) *DecisionError {
	return &DecisionError{Code: code, Words: fmt.Sprintf(format, args...)}
}

// DecisionOption is one of a request's alternatives.
type DecisionOption struct {
	Label     string `json:"label"`
	Tradeoffs string `json:"tradeoffs,omitempty"`
}

// DecisionSubject is the exact thing a decision is about: a commit, a
// candidate (a task's submitted result), an artifact (a file's blob at a
// commit) or a plan revision (a gate's).
type DecisionSubject struct {
	Kind   string `json:"kind"`
	Commit string `json:"commit,omitempty"`
	Run    string `json:"run,omitempty"`
	Task   string `json:"task,omitempty"`
	N      int    `json:"n,omitempty"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Plan   string `json:"plan,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// DecisionRequest is a request as an attempt's agent sends it.
type DecisionRequest struct {
	RequestID      string           `json:"requestId,omitempty"`
	Type           string           `json:"type"`
	Question       string           `json:"question"`
	Context        string           `json:"context,omitempty"`
	Options        []DecisionOption `json:"options,omitempty"`
	Recommendation *int             `json:"recommendation,omitempty"`
	Uncertainty    string           `json:"uncertainty,omitempty"`
	Authorizes     string           `json:"authorizes,omitempty"`
	Evidence       []string         `json:"evidence,omitempty"`
	Expertise      []string         `json:"expertise,omitempty"`
	Subject        *DecisionSubject `json:"subject,omitempty"`
	Supersedes     string           `json:"supersedes,omitempty"`
	// Baseline is the evidence's baseline the server made at the request
	// (one {ref, seen} per evidence ref, in order: The changes, evidence),
	// never the requester's; nil for none.
	Baseline json.RawMessage `json:"-"`
}

// requestBody is a request as the ledger's request column keeps it.
type requestBody struct {
	Question       string           `json:"question"`
	Context        string           `json:"context,omitempty"`
	Options        []DecisionOption `json:"options,omitempty"`
	Recommendation *int             `json:"recommendation,omitempty"`
	Uncertainty    string           `json:"uncertainty,omitempty"`
	Authorizes     string           `json:"authorizes,omitempty"`
	Evidence       []string         `json:"evidence,omitempty"`
	Expertise      []string         `json:"expertise,omitempty"`
	Subject        *DecisionSubject `json:"subject"`
	Supersedes     string           `json:"supersedes,omitempty"`
}

// Authority is what a response's principal authenticated as, which the
// policy checks: its grant (admin, or none), how (the workbench token, an
// identity provider's principal, a router's answer), the identity's
// subject, and the server process that accepted it (Engine.Boot), which
// RespondDecision sets: a caller's Boot is not read.
type Authority struct {
	Grant   string `json:"grant,omitempty"`
	Via     string `json:"via"`
	Subject string `json:"subject,omitempty"`
	Boot    string `json:"boot"`
}

// Response is a person's answer to a decision: an option's index, a text
// (a clarification's), or both, with a rationale, against the rev the
// person saw.
type Response struct {
	ResponseID  string `json:"responseId"`
	Choice      *int   `json:"choice,omitempty"`
	Text        string `json:"text,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
	ExpectedRev int    `json:"expectedRev"`
	ByName      string `json:"byName,omitempty"`
}

// ResponseReceipt is what a response gets, kept with it: accepted, or
// refused with its code and words. Repeat marks the receipt given before
// for the same response; Kept false a refusal past the decision's bound,
// which keeps no row and binds nothing.
type ResponseReceipt struct {
	Decision  string    `json:"decision"`
	K         int       `json:"k,omitempty"`
	Status    string    `json:"status"`
	Code      string    `json:"code,omitempty"`
	Words     string    `json:"words"`
	Actor     string    `json:"actor"`
	ByName    string    `json:"byName,omitempty"`
	Choice    *int      `json:"choice,omitempty"`
	Text      string    `json:"text,omitempty"`
	Rationale string    `json:"rationale,omitempty"`
	At        time.Time `json:"at"`
	Rev       int       `json:"rev"`
	Repeat    bool      `json:"repeat,omitempty"`
	Kept      *bool     `json:"kept,omitempty"`
}

// The ways an answer is applied (Application.How).
const (
	HowTyped     = "typed"
	HowTaskFile  = "task_file"
	HowReleased  = "released"
	HowCancelled = "cancelled"
	HowNoted     = "noted"
)

// Application is Yardmaster's receipt of an answer applied.
type Application struct {
	At        time.Time `json:"at"`
	By        string    `json:"by"`
	Run       string    `json:"run"`
	Task      string    `json:"task,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
	Response  int       `json:"response"`
	Validated bool      `json:"validated"`
	How       string    `json:"how"`
	Tasks     []string  `json:"tasks,omitempty"`
	Words     string    `json:"words"`
}

// DecisionAsker is an agent's decision's asking attempt, and whether it is
// live: the same run, task, attempt and session, the session not ended.
type DecisionAsker struct {
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Live    bool   `json:"live"`
}

// DecisionEntry is a row of a decision's history.
type DecisionEntry struct {
	Seq    int             `json:"seq"`
	At     time.Time       `json:"at"`
	Kind   string          `json:"kind"`
	By     string          `json:"by,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Decision is a decision as the engine and the routes report it.
type Decision struct {
	ID           string            `json:"id"`
	Run          string            `json:"run"`
	OpenedIn     string            `json:"openedIn"`
	Origin       string            `json:"origin"`
	Type         string            `json:"type"`
	Gate         string            `json:"gate,omitempty"`
	Asker        *DecisionAsker    `json:"asker,omitempty"`
	Member       string            `json:"member,omitempty"`
	Ticket       string            `json:"ticket,omitempty"`
	Plan         string            `json:"plan,omitempty"`
	PlanDigest   string            `json:"planDigest,omitempty"`
	RequestID    string            `json:"requestId,omitempty"`
	Request      json.RawMessage   `json:"request"`
	Evidence     json.RawMessage   `json:"evidence,omitempty"`
	Policy       json.RawMessage   `json:"policy"`
	State        string            `json:"state"`
	Rev          int               `json:"rev"`
	Why          string            `json:"why,omitempty"`
	SupersededBy string            `json:"supersededBy,omitempty"`
	Answer       string            `json:"answer,omitempty"` // the accepted response in words
	Responses    []ResponseReceipt `json:"responses,omitempty"`
	Applied      *Application      `json:"applied,omitempty"`
	History      []DecisionEntry   `json:"history,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	AnsweredAt   *time.Time        `json:"answeredAt,omitempty"`
	ClosedAt     *time.Time        `json:"closedAt,omitempty"`
}

// decision is an open or answered decision as a run holds it (run.decisions):
// what pump, the result's refusal, the application and the mirror read
// under e.mu. n is 0 until its opening commits.
type decision struct {
	n           int64
	origin, typ string
	gate        string
	t           *task    // the asking task (origin agent)
	a           *attempt // the asking attempt (origin agent)
	question    string
	options     []string // the labels
	state       string
	rev         int
	holds       []*task
	mirror      string // the id of the question that mirrors it in a's session
	answer      *accepted
	applying    bool
	// subject is what the decision is about (S1 to S4); openedAt when its
	// opening committed, which a done must come after to end its agent's
	// turn (turnEnded).
	subject  *DecisionSubject
	openedAt time.Time
	// delivery is how its applied answer's line went: delivery.typing
	// while it is queued or being typed, then entered, untyped or
	// uncertain; "" before. withdrawn is closed when the line must not go
	// in any more (a supersession: session.Submission.Withdrawn).
	// taskFiled says delivery.task_file is written.
	delivery  string
	withdrawn chan struct{}
	taskFiled bool
}

// accepted is a decision's accepted response, for its application.
type accepted struct {
	k                       int
	choice                  *int
	text, rationale, byName string
	actor                   string
	auth                    Authority
}

// id is the decision's id, "dr-<n>".
func (d *decision) id() string {
	if d.n == 0 {
		return "a decision"
	}
	return ledger.DecisionID(d.n)
}

// waits reports whether the decision is open, or answered and not applied.
func (d *decision) waits() bool {
	return d.state == ledger.DecisionOpen || d.state == ledger.DecisionAnswered
}

// decisionTx is a transaction's writer of decisions: ledger.Tx. A test's
// fake ledger that keeps none refuses every decision (errNoDecisions).
type decisionTx interface {
	OpenDecision(ledger.DecisionRow) (ledger.DecisionRow, bool, error)
	PutDecision(ledger.DecisionRow) error
	Answer(n int64, run string, rev int) (bool, error)
	Respond(n int64, r ledger.ResponseRow) (ledger.Responded, error)
	History(n int64, h ledger.HistoryRow) (ledger.HistoryRow, bool, error)
	Hold(run, task string, n int64) error
	Release(run, task string, n int64) (bool, error)
	Decision(n int64) (ledger.DecisionRow, bool, error)
	Record(n int64) (ledger.DecisionRecord, bool, error)
	Now() time.Time
}

// decisionReader is the ledger's read of a decision: ledger.Ledger's.
type decisionReader interface {
	Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error)
}

// requestReader is the ledger's read of a decision by its request id, and
// of one decision for one task: ledger.Ledger's.
type requestReader interface {
	DecisionByRequest(ctx context.Context, run, member, task, requestID string) (ledger.DecisionRow, bool, error)
	TaskDecision(ctx context.Context, n int64, run, task string) (ledger.DecisionRecord, bool, error)
}

var errNoDecisions = errors.New("the ledger keeps no decisions")

func decisionsOf(ltx LedgerTx) (decisionTx, error) {
	if dtx, ok := ltx.(decisionTx); ok {
		return dtx, nil
	}
	return nil, errNoDecisions
}

func (s sqlLedger) Decision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error) {
	return s.l.Decision(ctx, n)
}

func (s sqlLedger) DecisionByRequest(ctx context.Context, run, member, task, requestID string) (ledger.DecisionRow, bool, error) {
	return s.l.DecisionByRequest(ctx, run, member, task, requestID)
}

func (s sqlLedger) TaskDecision(ctx context.Context, n int64, run, task string) (ledger.DecisionRecord, bool, error) {
	return s.l.TaskDecision(ctx, n, run, task)
}

// readDecision reads decision n from the ledger.
func (e *Engine) readDecision(ctx context.Context, n int64) (ledger.DecisionRecord, bool, error) {
	dr, ok := e.ledger.(decisionReader)
	if !ok {
		return ledger.DecisionRecord{}, false, errNoDecisions
	}
	return dr.Decision(ctx, n)
}

// newBoot mints the id of this server process (Authority.Boot).
func newBoot() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Boot is the id of this server process, minted at its start: an
// identity's response is applied only by the process that accepted it.
func (e *Engine) Boot() string { return e.boot }

// responseID is the shape of a response's, a command's and an operation's
// id: 1 to 32 of A-Z a-z 0-9 . _ ~ -.
var responseID = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)

// evidenceRef is the shape an evidence ref is checked for: a kind and what
// it names (Brakeman's ref syntax), no space or control character.
var evidenceRef = regexp.MustCompile(`^[a-z][a-z_]*:[^\s\x00-\x1f\x7f]+$`)

// holdWords is what a held task's reason says: the decisions it waits for.
func holdWords(t *task) string { return wordsOf(t.holds, (*decision).id) }

// wordsOf is holdWords for the holds given, each named by id.
func wordsOf(holds []*decision, id func(*decision) string) string {
	if len(holds) == 0 {
		return ""
	}
	ids := make([]string, 0, len(holds))
	for _, d := range holds {
		ids = append(ids, id(d))
	}
	if len(ids) == 1 {
		return "waits for decision " + ids[0]
	}
	return "waits for decisions " + strings.Join(ids, ", ")
}

// hold puts d's hold on t, in memory: a ready task says so. The caller
// holds e.mu, in a transaction whose rows hold it too.
func (r *run) hold(t *task, d *decision) {
	if !slices.Contains(t.holds, d) {
		t.holds = append(t.holds, d)
	}
	if !slices.Contains(d.holds, t) {
		d.holds = append(d.holds, t)
	}
	r.holdReason(t)
}

// release takes d's hold off t, in memory. The caller holds e.mu.
func (r *run) release(t *task, d *decision) {
	t.holds = slices.DeleteFunc(t.holds, func(x *decision) bool { return x == d })
	d.holds = slices.DeleteFunc(d.holds, func(x *task) bool { return x == t })
	r.holdReason(t)
}

// holdReason gives a ready task the words of its holds.
func (r *run) holdReason(t *task) {
	if t.state != ledger.TaskReady {
		return
	}
	if w := holdWords(t); w != t.reason {
		t.reason = w
		r.markTask(t)
	}
}

// heldRow is a ready task's row in a snapshot (snapshot.held) whose reason
// names a decision the ledger has not numbered yet: the commit that opens
// that decision writes the row again with its number (renumber). i is the
// row's index among the snapshot's tasks; ns the holds' numbers as the
// snapshot was taken, 0 for one not numbered; reason what the commit
// wrote, set once it has.
type heldRow struct {
	i      int
	holds  []*decision
	ns     []int64
	reason string
}

// unnumbered is t's heldRow as row i of a snapshot, ok when t is ready and
// one of its holds is not numbered yet. The caller holds e.mu.
func unnumbered(t *task, i int) (heldRow, bool) {
	if t.state != ledger.TaskReady || !slices.ContainsFunc(t.holds, func(d *decision) bool { return d.n == 0 }) {
		return heldRow{}, false
	}
	h := heldRow{i: i, holds: slices.Clone(t.holds)}
	for _, d := range t.holds {
		h.ns = append(h.ns, d.n)
	}
	return h, true
}

// opened records that the transaction under way opened d as decision n:
// the snapshot's held rows name it by n (renumber). Called in a
// transaction's writes, under txMu.
func (r *run) opened(d *decision, n int64) {
	if r.numbered != nil {
		r.numbered[d] = n
	}
}

// renumber writes the snapshot's held rows again, as the snapshot has
// them, their reasons naming the decisions the transaction opened by their
// numbers (numbered): the row is the task's newest, whatever change wrote
// it, so a carried change's opening never puts back a state a later change
// left (a task cancelled since). A row whose words do not change is not
// written again.
func (s *snapshot) renumber(ltx LedgerTx, numbered map[*decision]int64) error {
	for k := range s.held {
		h := &s.held[k]
		row := s.tasks[h.i]
		row.Reason = wordsOf(h.holds, func(d *decision) string {
			n := h.ns[slices.Index(h.holds, d)]
			if n == 0 {
				n = numbered[d]
			}
			if n == 0 {
				return "a decision"
			}
			return ledger.DecisionID(n)
		})
		h.reason = row.Reason
		if row.Reason == s.tasks[h.i].Reason {
			continue
		}
		if err := ltx.PutTask(row); err != nil {
			return err
		}
	}
	return nil
}

// written gives each held row's task, once the snapshot committed and its
// decisions have their numbers, the words its row was written with, or
// marks it for the next transaction when they are not its words now (its
// holds changed since, or it is no longer ready). The caller holds e.mu.
func (r *run) written(s snapshot) {
	for _, h := range s.held {
		t := s.ts[h.i]
		if t.state == ledger.TaskReady && h.reason != "" && holdWords(t) == h.reason {
			t.reason = h.reason
			continue
		}
		r.holdReason(t)
	}
}

// waitingDecision is a decision the attempt asked that is open, or
// answered and not applied, nil for none. The caller holds e.mu.
func (r *run) waitingDecision(a *attempt) *decision {
	for _, d := range r.decisions {
		if d.a == a && d.waits() {
			return d
		}
	}
	return nil
}

// decisionN is the run's decision n, nil when the run does not hold it.
func (r *run) decisionN(n int64) *decision {
	for _, d := range r.decisions {
		if d.n == n {
			return d
		}
	}
	return nil
}

// dropDecision takes d from the run's open and answered ones: the place an
// identity's answer held may be free now, so a gate found with no room is
// asked again at the next pump (regateLater).
func (r *run) dropDecision(d *decision) {
	r.decisions = slices.DeleteFunc(r.decisions, func(x *decision) bool { return x == d })
	r.dec.noRoom = false
}

// cleanRequest cleans a request and checks it against its bounds: the
// body the ledger keeps, less its subject, and the fingerprint of the
// request as sent.
func cleanRequest(req DecisionRequest) (requestBody, string, error) {
	sent, err := json.Marshal(req)
	if err != nil {
		return requestBody{}, "", refuse(CodeInvalid, "the request: %v", err)
	}
	sum := sha256.Sum256(sent)
	fp := hex.EncodeToString(sum[:])
	if req.RequestID != "" && !responseID.MatchString(req.RequestID) {
		return requestBody{}, "", refuse(CodeInvalid, "a request id is 1 to 32 of A-Z a-z 0-9 . _ ~ -")
	}
	switch req.Type {
	case ledger.DecisionClarification, ledger.DecisionApproval, ledger.DecisionAssignment:
	default:
		return requestBody{}, "", refuse(CodeInvalid, "a decision is a clarification, an approval or an assignment, not %q", req.Type)
	}
	b := requestBody{
		Question:       strings.TrimSpace(oneLine.Replace(cleanText(req.Question))),
		Context:        cleanText(req.Context),
		Recommendation: req.Recommendation,
		Uncertainty:    cleanText(req.Uncertainty),
		Authorizes:     cleanText(req.Authorizes),
		Supersedes:     req.Supersedes,
	}
	if n := utf8.RuneCountInString(b.Question); n < 1 || n > ledger.MaxDecisionQuestion {
		return requestBody{}, "", refuse(CodeInvalid, "the question is one line of 1 to %d characters", ledger.MaxDecisionQuestion)
	}
	if utf8.RuneCountInString(b.Context) > ledger.MaxDecisionContext {
		return requestBody{}, "", refuse(CodeInvalid, "the context is more than %d characters", ledger.MaxDecisionContext)
	}
	lo, hi := 0, ledger.MaxDecisionOptions
	switch req.Type {
	case ledger.DecisionApproval:
		lo = 2
	case ledger.DecisionAssignment:
		hi = 0
	}
	if len(req.Options) < lo || len(req.Options) > hi {
		return requestBody{}, "", refuse(CodeInvalid, "a %s has %d to %d options, not %d", req.Type, lo, hi, len(req.Options))
	}
	for _, o := range req.Options {
		label := strings.TrimSpace(oneLine.Replace(cleanText(o.Label)))
		if n := utf8.RuneCountInString(label); n < 1 || n > ledger.MaxOptionLabel {
			return requestBody{}, "", refuse(CodeInvalid, "an option's label is one line of 1 to %d characters", ledger.MaxOptionLabel)
		}
		trade := cleanText(o.Tradeoffs)
		if len(trade) > ledger.MaxOptionTradeoffs {
			return requestBody{}, "", refuse(CodeInvalid, "an option's tradeoffs are more than %d bytes", ledger.MaxOptionTradeoffs)
		}
		b.Options = append(b.Options, DecisionOption{Label: label, Tradeoffs: trade})
	}
	if rec := b.Recommendation; rec != nil && (*rec < -1 || *rec >= len(b.Options)) {
		return requestBody{}, "", refuse(CodeInvalid, "the recommendation %d names no option", *rec)
	}
	if len(b.Uncertainty) > ledger.MaxDecisionUncertainty || len(b.Authorizes) > ledger.MaxDecisionAuthorizes {
		return requestBody{}, "", refuse(CodeInvalid, "the uncertainty and what a yes authorizes are at most %d bytes each", ledger.MaxDecisionAuthorizes)
	}
	if req.Type == ledger.DecisionApproval && b.Authorizes == "" {
		return requestBody{}, "", refuse(CodeInvalid, "an approval says what a yes authorizes")
	}
	if len(req.Evidence) > ledger.MaxDecisionEvidence {
		return requestBody{}, "", refuse(CodeInvalid, "%d evidence refs, more than %d", len(req.Evidence), ledger.MaxDecisionEvidence)
	}
	for _, ref := range req.Evidence {
		if len(ref) > ledger.MaxEvidenceRef || !evidenceRef.MatchString(ref) {
			return requestBody{}, "", refuse(CodeInvalid, "an evidence ref is a kind and what it names, at most %d bytes, not %q", ledger.MaxEvidenceRef, shortText(ref))
		}
		b.Evidence = append(b.Evidence, ref)
	}
	if len(req.Expertise) > ledger.MaxDecisionExpertise {
		return requestBody{}, "", refuse(CodeInvalid, "%d expertise labels, more than %d", len(req.Expertise), ledger.MaxDecisionExpertise)
	}
	for _, x := range req.Expertise {
		x = strings.TrimSpace(oneLine.Replace(cleanText(x)))
		if n := utf8.RuneCountInString(x); n < 1 || n > ledger.MaxExpertiseLabel {
			return requestBody{}, "", refuse(CodeInvalid, "an expertise label is 1 to %d characters", ledger.MaxExpertiseLabel)
		}
		b.Expertise = append(b.Expertise, x)
	}
	if b.Supersedes != "" {
		if _, ok := ledger.ParseDecisionID(b.Supersedes); !ok {
			return requestBody{}, "", refuse(CodeInvalid, "supersedes %q is not a decision's id", b.Supersedes)
		}
	}
	return b, fp, nil
}

// artifactPath cleans an artifact subject's path: relative to the
// worktree, inside it, and not Conductor's.
func artifactPath(p string) (string, error) {
	c := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || path.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", refuse(CodeInvalid, "an artifact's path is a file's, relative to the worktree, not %q", shortText(p))
	}
	if slices.Contains(strings.Split(c, "/"), taskDir) {
		return "", refuse(CodeInvalid, "an artifact's path is not Conductor's %s directory", taskDir)
	}
	return c, nil
}

// blobDigest is the SHA-256 of the blob path names at commit in the
// repository dir is in, read from its object store with git cat-file (an
// argv), never from a file in a working tree.
func blobDigest(ctx context.Context, dir, commit, p string) (string, error) {
	oid, err := blobOID(ctx, dir, commit, p)
	if err != nil {
		return "", err
	}
	return oidDigest(ctx, dir, commit, p, oid)
}

// blobOID is the object id of the blob p names at commit.
func blobOID(ctx context.Context, dir, commit, p string) (string, error) {
	oid, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", commit+":"+p)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read at %s: %w", p, short(commit), err)
	}
	oid = strings.TrimSpace(oid)
	if kind, err := git(ctx, dir, "cat-file", "-t", oid); err != nil || strings.TrimSpace(kind) != "blob" {
		return "", fmt.Errorf("%s at %s is not a file", p, short(commit))
	}
	return oid, nil
}

// oidDigest is the SHA-256 of blob oid (p's at commit), hashed as git
// writes it out (gitcli.RunTo): however large the file, the server holds
// no copy of it.
func oidDigest(ctx context.Context, dir, commit, p, oid string) (string, error) {
	h := sha256.New()
	if err := gitcli.RunTo(ctx, dir, h, "cat-file", "blob", oid); err != nil {
		return "", fmt.Errorf("%s cannot be read at %s: %w", p, short(commit), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// asCommitted reports whether the worktree's file p is blob oid, compared
// through an index of its own (read-tree, add -u), as checkoutIntact
// compares a checkout: no flag of the worktree's index (assume-unchanged,
// skip-worktree) hides a change from it, as it hides one from git status.
func asCommitted(ctx context.Context, dir, commit, p, oid string) (bool, error) {
	f, err := os.CreateTemp("", "conductor-index-")
	if err != nil {
		return false, err
	}
	index := f.Name()
	f.Close()
	os.Remove(index) // git makes it
	defer os.Remove(index)
	env := map[string]string{"GIT_INDEX_FILE": index}
	if _, err := gitcli.RunEnv(ctx, dir, env, "read-tree", commit); err != nil {
		return false, err
	}
	if _, err := gitcli.RunEnv(ctx, dir, env, "add", "-u", "--", ":(literal)"+p); err != nil {
		return false, err
	}
	out, err := gitcli.RunEnv(ctx, dir, env, "ls-files", "-s", "-z", "--", ":(literal)"+p)
	if err != nil {
		return false, err
	}
	// "<mode> <oid> <stage>\t<path>\x00", none when the file is gone.
	fields := strings.Fields(strings.SplitN(out, "\t", 2)[0])
	return len(fields) == 3 && fields[1] == oid, nil
}

// subjectOf binds a request's subject: a commit (the attempt's HEAD,
// filled in when the request names none), an artifact (a file as committed
// at HEAD, its blob's digest, refused artifact_uncommitted when the
// worktree's file differs from it), or a candidate (a submitted result of
// the run). The caller does not hold e.mu.
func (e *Engine) subjectOf(ctx context.Context, r *run, worktree string, s *DecisionSubject) (*DecisionSubject, error) {
	if s == nil {
		s = &DecisionSubject{Kind: "commit"}
	}
	switch s.Kind {
	case "commit", "artifact":
	case "candidate":
		e.mu.Lock()
		defer e.mu.Unlock()
		if s.Run != r.id {
			return nil, refuse(CodeInvalid, "a candidate subject is a result of this run, %s", r.id)
		}
		t := r.task(s.Task)
		if t == nil {
			return nil, refuse(CodeInvalid, "the run has no task %q", s.Task)
		}
		for _, a := range t.attempts {
			if a.n == s.N && a.submitted != nil && a.result != "" && (s.Commit == a.result || len(s.Commit) >= 7 && strings.HasPrefix(a.result, s.Commit)) {
				return &DecisionSubject{Kind: "candidate", Run: r.id, Task: t.def.ID, N: a.n, Commit: a.result}, nil
			}
		}
		return nil, refuse(CodeInvalid, "attempt %d of %s submitted no result %s", s.N, s.Task, shortText(s.Commit))
	default:
		return nil, refuse(CodeInvalid, "a request's subject is a commit, a candidate or an artifact, not %q", s.Kind)
	}
	if worktree == "" {
		return nil, refuse(CodeNotRunning, "the attempt has no worktree yet")
	}
	head, err := headCommit(ctx, worktree)
	if err != nil || head == "" {
		return nil, refuse(CodeInvalid, "the worktree has no commit for the decision to be about")
	}
	if s.Kind == "commit" {
		if s.Commit != "" && s.Commit != head && !(len(s.Commit) >= 7 && strings.HasPrefix(head, s.Commit)) {
			return nil, refuse(CodeInvalid, "a commit subject is the attempt's HEAD, %s, not %s", short(head), shortText(s.Commit))
		}
		return &DecisionSubject{Kind: "commit", Commit: head}, nil
	}
	p, err := artifactPath(s.Path)
	if err != nil {
		return nil, err
	}
	status, err := git(ctx, worktree, "status", "--porcelain=v1", "--untracked-files=all", "--", ":(literal)"+p)
	if err != nil {
		return nil, refuse(CodeInvalid, "%s's status cannot be read: %v", p, err)
	}
	if strings.TrimSpace(status) != "" {
		return nil, refuse(CodeArtifactUncommitted, "%s has changes not committed: commit what you ask about, then ask", p)
	}
	oid, err := blobOID(ctx, worktree, head, p)
	if err != nil {
		return nil, refuse(CodeArtifactUncommitted, "%v: commit what you ask about, then ask", err)
	}
	if same, err := asCommitted(ctx, worktree, head, p, oid); err != nil {
		return nil, refuse(CodeInvalid, "%s cannot be compared with its commit: %v", p, err)
	} else if !same {
		return nil, refuse(CodeArtifactUncommitted, "%s differs from its blob at %s: commit what you ask about, then ask", p, short(head))
	}
	digest, err := oidDigest(ctx, worktree, head, p, oid)
	if err != nil {
		return nil, refuse(CodeArtifactUncommitted, "%v: commit what you ask about, then ask", err)
	}
	return &DecisionSubject{Kind: "artifact", Path: p, Commit: head, SHA256: digest}, nil
}

// decisionFailure is a decision's transaction's refusal by the ledger (a
// bound, a repeat's conflict, a shape), as a DecisionError; nil for an
// error that is the ledger's failure.
func decisionFailure(err error) *DecisionError {
	var de *DecisionError
	switch {
	case errors.As(err, &de):
		return de
	case errors.Is(err, ledger.ErrDecisionLimit):
		return refuse(CodeDecisionLimit, "%s", strings.TrimPrefix(err.Error(), "ledger: "))
	case errors.Is(err, ledger.ErrDecisionConflict):
		return refuse(CodeDecisionConflict, "%s", strings.TrimPrefix(err.Error(), "ledger: "))
	case errors.Is(err, ledger.ErrInvalidDecision), errors.Is(err, ledger.ErrTooLarge):
		return refuse(CodeInvalid, "%s", strings.TrimPrefix(err.Error(), "ledger: "))
	case errors.Is(err, errNoDecisions):
		return refuse(CodeInvalid, "%s", err.Error())
	}
	return nil
}

// repeatRequest aborts an opening whose request id was sent before: the
// decision is that one.
type repeatRequest struct{ n int64 }

func (repeatRequest) Error() string { return "the request was sent before" }

// historyDetail encodes a history row's detail within the ledger's bound
// (ledger.MaxHistoryDetail), whatever it holds: a change that hands its
// commit a row the ledger would refuse for its size has already changed
// the engine, and a refused commit carries its writes to every later one,
// which the same row then fails too (#636, review round 3). Past the
// bound, its longest strings and lists are cut, longest first, the detail
// marked "cut" (fitObject).
func historyDetail(v any) string {
	return fitJSON(v, ledger.MaxHistoryDetail)
}

// fitJSON encodes v, a JSON object, in at most limit bytes: as it is when it
// fits, else as fitObject cuts it. An unencodable v is "" (no detail).
func fitJSON(v any, limit int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) <= limit {
		return string(b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	return fitObject(m, len(b), limit)
}

// fitObject encodes m, whose encoding was size bytes, in at most limit: the
// longest of its strings and lists cut first (a string to what fits, with
// "…"; a list by its last elements), m["cut"] true, until it fits; when
// nothing is left to cut, only its size, which always fits.
func fitObject(m map[string]any, size, limit int) string {
	m["cut"] = true
	for range 64 {
		b, err := json.Marshal(m)
		if err != nil {
			break
		}
		if len(b) <= limit {
			return string(b)
		}
		over := len(b) - limit
		key, longest := "", 0
		for k, v := range m {
			n := 0
			switch v := v.(type) {
			case string:
				if len(v) > len("…") {
					n = len(v)
				}
			case []any:
				if len(v) > 0 {
					vb, _ := json.Marshal(v)
					n = len(vb)
				}
			}
			if n > longest || n == longest && n > 0 && k < key {
				key, longest = k, n
			}
		}
		if longest == 0 {
			break
		}
		switch v := m[key].(type) {
		case string:
			keep := max(len(v)-over-8, 0)
			for keep > 0 && !utf8.RuneStart(v[keep]) {
				keep--
			}
			m[key] = v[:keep] + "…"
		case []any:
			m[key] = v[:len(v)/2]
		}
	}
	b, _ := json.Marshal(map[string]any{"cut": true, "oversized": size})
	return string(b)
}

// routedOperator is the routed row of a decision opened under local/1.
var routedOperator = historyDetail(map[string]any{"to": []map[string]string{{"kind": "operator"}}, "rule": "local/1: the operator answers", "by": "policy"})

// DecisionSent answers the decision a request names when the attempt's
// member sent its request id before (sent true): that decision as it is
// now, or decision_conflict for another body. The server asks it before
// it resolves the request's evidence again (internal/api), so a request
// sent again after a lost reply answers its decision whatever its
// evidence has become. sent false for a request not sent before, or one
// that is not a request (RequestDecision refuses it).
func (e *Engine) DecisionSent(ctx context.Context, sessionID string, req DecisionRequest) (Decision, bool, error) {
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return Decision{}, false, nil
	}
	_, fp, err := cleanRequest(req)
	if err != nil {
		return Decision{}, false, nil
	}
	return e.requestSent(ctx, r, a, req.RequestID, fp)
}

// RequestDecision opens a decision that the session's attempt asks
// (docs/plans/decisions.md, Opening): the attempt must be running; the
// request is cleaned and checked against its bounds, its subject bound
// (subjectOf); one transaction writes the decision's row, its opened and
// routed history, the hold of the asking task, a superseded request's
// supersession (made final before the opening, so its place among the
// bounds is this one's; its hold moved to this one) and the run's event. A request
// id the attempt's member sent before answers that decision (created
// false), or decision_conflict for another body, before anything that may
// have changed since it was first sent is checked again (the attempt
// running, the decision it supersedes, its subject). After the commit, and
// never before it, the session's question mirrors the decision, on the
// attempt's askMu; the reply does not wait for it.
func (e *Engine) RequestDecision(ctx context.Context, sessionID string, req DecisionRequest) (Decision, bool, error) {
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return Decision{}, false, err
	}
	body, fp, err := cleanRequest(req)
	if err != nil {
		return Decision{}, false, err
	}
	if dec, ok, err := e.requestSent(ctx, r, a, req.RequestID, fp); ok || err != nil {
		return dec, false, err
	}
	e.mu.Lock()
	running := a.closedAt.IsZero() && a.state == ledger.AttemptRunning && !r.stopping
	worktree := a.worktree
	e.mu.Unlock()
	if !running {
		return Decision{}, false, refuse(CodeNotRunning, "the attempt is not running: a decision is asked while it runs")
	}
	if body.Subject, err = e.subjectOf(ctx, r, worktree, req.Subject); err != nil {
		return Decision{}, false, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Decision{}, false, err
	}
	if len(raw) > ledger.MaxDecisionRequest {
		return Decision{}, false, refuse(CodeInvalid, "the request is %d bytes as kept, more than %d", len(raw), ledger.MaxDecisionRequest)
	}
	baseline := "[]"
	if len(req.Baseline) > 0 {
		baseline = string(req.Baseline)
	}
	var supN int64
	if body.Supersedes != "" {
		supN, _ = ledger.ParseDecisionID(body.Supersedes)
	}
	var (
		d, sup  *decision
		opened  int64
		refused *DecisionError
	)
	err = e.transact(ctx, r, func() tx {
		if !a.closedAt.IsZero() || a.state != ledger.AttemptRunning || r.stopping {
			refused = refuse(CodeNotRunning, "the attempt is not running: a decision is asked while it runs")
			return tx{}
		}
		if supN != 0 {
			sup = r.decisionN(supN)
			if sup == nil || sup.origin != ledger.OriginAgent || sup.t != a.t || sup.state != ledger.DecisionOpen {
				sup = nil
				refused = refuse(CodeInvalid, "supersedes names %s, which is no open decision of task %s", body.Supersedes, a.t.def.ID)
				return tx{}
			}
		}
		t := a.t
		d = &decision{origin: ledger.OriginAgent, typ: req.Type, t: t, a: a, question: body.Question, state: ledger.DecisionOpen, rev: 1, subject: body.Subject}
		for _, o := range body.Options {
			d.options = append(d.options, o.Label)
		}
		r.decisions = append(r.decisions, d)
		r.hold(t, d)
		if sup != nil {
			sup.state = ledger.DecisionSuperseded
			r.dropDecision(sup)
			r.release(t, sup)
		}
		row := ledger.DecisionRow{Origin: ledger.OriginAgent, Type: req.Type, TaskID: t.def.ID, Attempt: a.n, SessionID: a.sessionID, Member: a.m.def.Name,
			RequestID: req.RequestID, Fingerprint: fp, Request: string(raw), Evidence: baseline, Policy: policyJSON}
		if r.pkg != nil {
			row.Ticket, row.Plan, row.PlanDigest = r.pkg.p.Ticket.ID, r.pkg.p.Plan.ID, r.pkg.p.Plan.Digest
		}
		supRev := 0
		if sup != nil {
			supRev = sup.rev
		}
		by := a.m.def.Name
		return tx{more: func(ltx LedgerTx) error {
			dtx, err := decisionsOf(ltx)
			if err != nil {
				return err
			}
			// The decision it supersedes final first: its place among the
			// bounds is the new one's, as a replacement's is (Tx.OpenDecision),
			// so a request that takes an open decision's place at the
			// attempt's or the run's bound opens (#635, review round 2).
			var prior ledger.DecisionRow
			if supN != 0 {
				var ok bool
				if prior, ok, err = dtx.Decision(supN); err != nil {
					return err
				}
				if !ok || prior.State != ledger.DecisionOpen || prior.CurrentRun != r.id || prior.Origin != ledger.OriginAgent || prior.TaskID != t.def.ID {
					// A request sent before answers its decision first, as it
					// did when the opening came first; anything this opened
					// is rolled back with the refusal.
					if got, created, err := dtx.OpenDecision(row); err == nil && !created {
						return repeatRequest{got.N}
					}
					return refuse(CodeInvalid, "supersedes names %s, which is no open decision of task %s", body.Supersedes, t.def.ID)
				}
				prior.State, prior.Rev, prior.Why = ledger.DecisionSuperseded, max(prior.Rev, supRev)+1, "a newer request of "+t.def.ID+" takes its place"
				if err := dtx.PutDecision(prior); err != nil {
					return err
				}
			}
			got, created, err := dtx.OpenDecision(row)
			if err != nil {
				return err
			}
			if !created {
				return repeatRequest{got.N}
			}
			opened = got.N
			r.opened(d, got.N)
			if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryOpened, By: by, Detail: historyDetail(map[string]any{"task": t.def.ID, "attempt": a.n})}); err != nil {
				return err
			}
			if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryRouted, By: policyLocal, Detail: routedOperator}); err != nil {
				return err
			}
			if err := dtx.Hold(r.id, t.def.ID, got.N); err != nil {
				return err
			}
			if err := ltx.Event("decision.opened", map[string]any{"decision": got.ID(), "origin": ledger.OriginAgent, "type": req.Type, "task": t.def.ID, "attempt": a.n}); err != nil {
				return err
			}
			if supN == 0 {
				return nil
			}
			why := fmt.Sprintf("%s takes its place", got.ID())
			prior.Why, prior.SupersededBy = why, got.ID()
			if err := dtx.PutDecision(prior); err != nil {
				return err
			}
			if _, _, err := dtx.History(supN, ledger.HistoryRow{Kind: ledger.HistorySuperseded, By: by, Detail: historyDetail(map[string]any{"by": got.ID(), "why": why})}); err != nil {
				return err
			}
			if _, err := dtx.Release(r.id, t.def.ID, supN); err != nil {
				return err
			}
			return ltx.Event("decision.superseded", map[string]any{"decision": prior.ID(), "by": got.ID(), "task": t.def.ID})
		}, done: func() {
			d.n, d.openedAt = opened, time.Now().UTC()
			r.dec.opened++
			r.holdReason(t)
			r.note(session.ActivityStatus, "task %s: attempt %d asks decision %s: %s", t.def.ID, a.n, d.id(), shortText(d.question))
		}}
	}, func() {
		if d != nil {
			r.dropDecision(d)
			r.release(a.t, d)
		}
		if sup != nil {
			sup.state = ledger.DecisionOpen
			r.decisions = append(r.decisions, sup)
			r.hold(a.t, sup)
		}
	})
	if refused != nil {
		// The same request sent twice at once: the other one's commit is
		// its decision.
		if dec, ok, err := e.requestSent(ctx, r, a, req.RequestID, fp); ok || err != nil {
			return dec, false, err
		}
		return Decision{}, false, refused
	}
	var repeat repeatRequest
	switch {
	case errors.As(err, &repeat):
		dec, err := e.Decision(ctx, ledger.DecisionID(repeat.n))
		return dec, false, err
	case err != nil:
		if de := decisionFailure(err); de != nil {
			return Decision{}, false, de
		}
		e.afterTransact(r, err)
		return Decision{}, false, err
	}
	if sup != nil {
		e.withdrawMirror(a, sup)
	}
	go e.askMirror(r, a, d)
	dec, err := e.Decision(ctx, ledger.DecisionID(opened))
	return dec, true, err
}

// requestSent is the decision the attempt's member opened in r for the
// attempt's task under request id, read from the ledger (ok), refused
// decision_conflict when it was sent with another body (fp the
// fingerprint of this one's); not ok for none, or no request id. The task
// is part of the key the ledger looks it up by (DecisionByRequest), so
// another task's request under the same id is none.
func (e *Engine) requestSent(ctx context.Context, r *run, a *attempt, id, fp string) (Decision, bool, error) {
	rr, can := e.ledger.(requestReader)
	if id == "" || !can {
		return Decision{}, false, nil
	}
	row, ok, err := rr.DecisionByRequest(ctx, r.id, a.m.def.Name, a.t.def.ID, id)
	if err != nil || !ok {
		return Decision{}, false, err
	}
	if row.Fingerprint != fp {
		return Decision{}, true, refuse(CodeDecisionConflict, "request %s was sent before with another body, as %s", id, row.ID())
	}
	dec, err := e.Decision(ctx, row.ID())
	return dec, true, err
}

// askMirror puts the decision's question to the asking attempt's session
// as its agent's own (session.Local.TryAskDecision), on the attempt's
// askMu: the decision is read as open before the ask and again after it,
// and one answered, superseded or cancelled meanwhile withdraws the
// question at once by its id. The question's id is kept on the decision,
// for its application and its withdrawal. A decision the attempt opened
// after this one and still open is asked instead (only the newest's
// mirror shows): asks that take askMu out of their order never put an
// older question back over it.
func (e *Engine) askMirror(r *run, a *attempt, d *decision) {
	a.askMu.Lock()
	defer a.askMu.Unlock()
	e.mu.Lock()
	open := d.state == ledger.DecisionOpen && a.closedAt.IsZero()
	sid, id, question, n := a.sessionID, d.id(), d.question, d.n
	e.mu.Unlock()
	if !open {
		return
	}
	if e.beforeMirror != nil {
		// Read as open, before the ask: a test holds it here while the
		// decision is answered or superseded.
		e.beforeMirror(n)
	}
	e.mu.Lock()
	newer := r.newerOpen(a, d)
	e.mu.Unlock()
	if newer {
		// A newer decision's ask follows on askMu, or came first: its
		// question is the one that shows.
		return
	}
	l, ok := e.lookup(sid)
	if !ok {
		return
	}
	ask := l.TryAskDecision
	if e.tryAsk != nil {
		ask = func(id, question string) (string, error) { return e.tryAsk(l, id, question) }
	}
	q, err := ask(id, question)
	if err != nil || q == "" {
		return
	}
	e.mu.Lock()
	still := d.state == ledger.DecisionOpen && a.closedAt.IsZero()
	if still {
		d.mirror = q
	}
	e.mu.Unlock()
	if !still {
		l.WithdrawQuestion(q)
	}
}

// newerOpen reports whether the attempt asked a decision after d that is
// open and numbered: their numbers are their openings' order, which no
// change of r.decisions moves (a supersession refused puts its target
// back last, #635, review round 2). One not numbered yet is not counted:
// its ask follows its commit, and a commit refused never asks it. d is
// numbered (its ask follows its commit). The caller holds e.mu.
func (r *run) newerOpen(a *attempt, d *decision) bool {
	return slices.ContainsFunc(r.decisions, func(x *decision) bool {
		return x != d && x.a == a && x.state == ledger.DecisionOpen && x.n > d.n
	})
}

// withdrawMirror takes away the question that mirrors d, if one was put
// up, on the asking attempt's askMu: after any ask under way.
func (e *Engine) withdrawMirror(a *attempt, d *decision) {
	if a == nil {
		return
	}
	go func() {
		a.askMu.Lock()
		defer a.askMu.Unlock()
		e.mu.Lock()
		q, sid := d.mirror, a.sessionID
		d.mirror = ""
		e.mu.Unlock()
		if q == "" {
			return
		}
		if l, ok := e.lookup(sid); ok {
			l.WithdrawQuestion(q)
		}
	}()
}

// gate is a plan decision's opening, in a run's first transaction.
type gate struct {
	d   *decision
	row ledger.DecisionRow
	pd  PackageDecision
}

// openGates opens one decision per decision the run's launch package
// names (a gate: origin plan, its plan decision's id, the plan revision as
// its subject, an approval's options Approve and Reject), each holding
// every task its before names, in the run's first transaction: so no
// attempt of a held task is reserved before its hold exists (O2). It
// returns the transaction's part and its undo; nothing for a run launched
// without a ticket (Owner decisions, 6). The caller holds e.mu, in that
// transaction's change.
func (e *Engine) openGates(r *run) (tx, func()) {
	if r.pkg == nil || len(r.pkg.p.Decisions) == 0 {
		return tx{}, func() {}
	}
	p := r.pkg.p
	var gates []*gate
	// The run's gates' requirements, none released yet (I14).
	r.dec.gates = map[string]bool{}
	for _, pd := range p.Decisions {
		r.dec.gates[pd.ID] = false
		typ, body := gateRequest(p, pd)
		raw, _ := json.Marshal(body)
		sum := sha256.Sum256(raw)
		d := &decision{origin: ledger.OriginPlan, typ: typ, gate: pd.ID, question: body.Question, state: ledger.DecisionOpen, rev: 1, subject: body.Subject}
		for _, o := range body.Options {
			d.options = append(d.options, o.Label)
		}
		g := &gate{d: d, pd: pd, row: ledger.DecisionRow{Origin: ledger.OriginPlan, Type: typ, Gate: pd.ID, Ticket: p.Ticket.ID, Plan: p.Plan.ID, PlanDigest: p.Plan.Digest,
			Fingerprint: hex.EncodeToString(sum[:]), Request: string(raw), Policy: policyJSON}}
		r.decisions = append(r.decisions, d)
		for _, id := range pd.Before {
			if t := r.task(id); t != nil {
				r.hold(t, d)
			}
		}
		gates = append(gates, g)
	}
	opened := make([]int64, len(gates))
	out := tx{last: func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		for i, g := range gates {
			got, _, err := dtx.OpenDecision(g.row)
			if err != nil {
				return err
			}
			opened[i] = got.N
			r.opened(g.d, got.N)
			if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryOpened, By: "plan", Detail: historyDetail(map[string]any{"gate": g.pd.ID, "before": g.pd.Before})}); err != nil {
				return err
			}
			if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryRouted, By: policyLocal, Detail: routedOperator}); err != nil {
				return err
			}
			for _, t := range g.d.holds {
				if err := dtx.Hold(r.id, t.def.ID, got.N); err != nil {
					return err
				}
			}
			if err := ltx.Event("decision.opened", map[string]any{"decision": got.ID(), "origin": ledger.OriginPlan, "type": g.row.Type, "gate": g.pd.ID, "before": g.pd.Before}); err != nil {
				return err
			}
		}
		// The held tasks' rows name their gates by number (renumber).
		return nil
	}, done: func() {
		r.dec.opened += len(gates)
		for i, g := range gates {
			g.d.n = opened[i]
		}
	}}
	undo := func() {
		r.dec.gates = nil
		for _, g := range gates {
			for _, t := range slices.Clone(g.d.holds) {
				r.release(t, g.d)
			}
			r.dropDecision(g.d)
		}
	}
	return out, undo
}

// Decision reads decision id through the ledger, with the asking attempt's
// liveness from the engine.
func (e *Engine) Decision(ctx context.Context, id string) (Decision, error) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return Decision{}, refuse(CodeNotFound, "%q is not a decision's id", shortText(id))
	}
	rec, ok, err := e.readDecision(ctx, n)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return Decision{}, refuse(CodeNotFound, "no decision %s", id)
	}
	return e.decisionOf(rec), nil
}

// TaskDecision reads decision id for the session's attempt: one of its
// task's in the run that holds the attempt (asked by the task, or holding
// it), read whole in the one read that judges it so (the ledger's
// TaskDecision). Another task's decision is refused not_found in the
// words of an id no decision has.
func (e *Engine) TaskDecision(ctx context.Context, sessionID, id string) (Decision, error) {
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return Decision{}, err
	}
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return Decision{}, refuse(CodeNotFound, "%q is not a decision's id", shortText(id))
	}
	rr, can := e.ledger.(requestReader)
	if !can {
		return Decision{}, errNoDecisions
	}
	rec, ok, err := rr.TaskDecision(ctx, n, r.id, a.t.def.ID)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return Decision{}, refuse(CodeNotFound, "no decision %s of task %s", id, a.t.def.ID)
	}
	return e.decisionOf(rec), nil
}

// decisionOf is a decision's record as the engine reports it.
func (e *Engine) decisionOf(rec ledger.DecisionRecord) Decision {
	d := Decision{ID: rec.ID(), Run: rec.CurrentRun, OpenedIn: rec.RunID, Origin: rec.Origin, Type: rec.Type, Gate: rec.Gate, Member: rec.Member,
		Ticket: rec.Ticket, Plan: rec.Plan, PlanDigest: rec.PlanDigest, RequestID: rec.RequestID, Request: json.RawMessage(rec.Request),
		Policy: json.RawMessage(rec.Policy), State: rec.State, Rev: rec.Rev, Why: rec.Why, SupersededBy: rec.SupersededBy, CreatedAt: rec.CreatedAt}
	if rec.Evidence != "" && rec.Evidence != "[]" {
		d.Evidence = json.RawMessage(rec.Evidence)
	}
	if !rec.AnsweredAt.IsZero() {
		at := rec.AnsweredAt
		d.AnsweredAt = &at
	}
	d.Answer, _, _ = answerOf(rec)
	if !rec.ClosedAt.IsZero() {
		at := rec.ClosedAt
		d.ClosedAt = &at
	}
	if rec.Applied != "" {
		var app Application
		if json.Unmarshal([]byte(rec.Applied), &app) == nil {
			d.Applied = &app
		}
	}
	for _, rr := range rec.Responses {
		var rc ResponseReceipt
		if json.Unmarshal([]byte(rr.Receipt), &rc) == nil {
			// The receipt is kept as made before its row was numbered.
			rc.K = rr.K
			d.Responses = append(d.Responses, rc)
		}
	}
	for _, h := range rec.History {
		entry := DecisionEntry{Seq: h.Seq, At: h.At, Kind: h.Kind, By: h.By}
		if h.Detail != "" {
			entry.Detail = json.RawMessage(h.Detail)
		}
		d.History = append(d.History, entry)
	}
	if rec.Origin == ledger.OriginAgent {
		d.Asker = &DecisionAsker{Task: rec.TaskID, Attempt: rec.Attempt}
		e.mu.Lock()
		if r := e.runs[rec.CurrentRun]; r != nil {
			if t := r.task(rec.TaskID); t != nil {
				if a := t.active(); a != nil && a.n == rec.Attempt && a.sessionID == rec.SessionID && e.sessionLive(a.sessionID) {
					d.Asker.Live = true
				}
			}
		}
		e.mu.Unlock()
	}
	return d
}

// sessionLive reports whether the session is the server's and has not
// ended.
func (e *Engine) sessionLive(id string) bool {
	if id == "" {
		return false
	}
	l, ok := e.lookup(id)
	return ok && !isClosed(l.Ended())
}

// fingerprintOf is the SHA-256 of a body as sent.
func fingerprintOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkResponse refuses a response that is not of its shape (invalid, no
// row): its id, its text and rationale within their bounds, cleaned.
func checkResponse(resp Response) (Response, error) {
	if !responseID.MatchString(resp.ResponseID) {
		return resp, refuse(CodeInvalid, "a response id is 1 to 32 of A-Z a-z 0-9 . _ ~ -")
	}
	resp.Text = cleanText(resp.Text)
	resp.Rationale = cleanText(resp.Rationale)
	resp.ByName = strings.TrimSpace(oneLine.Replace(cleanText(resp.ByName)))
	if len(resp.Text) > maxAnswerText {
		return resp, refuse(CodeInvalid, "an answer's text is at most %d bytes", maxAnswerText)
	}
	if b, _ := json.Marshal(resp.Rationale); len(b) > maxRationale {
		return resp, refuse(CodeInvalid, "a rationale is at most %d bytes as encoded", maxRationale)
	}
	if utf8.RuneCountInString(resp.ByName) > 64 {
		resp.ByName = string([]rune(resp.ByName)[:64])
	}
	if resp.Choice != nil && *resp.Choice < 0 || resp.ExpectedRev < 1 {
		return resp, refuse(CodeInvalid, "a response names an option's index and the rev it answers")
	}
	return resp, nil
}

// answerFits refuses an answer that does not fit the decision's type
// (invalid, no row): an approval and an assignment take a choice, a
// clarification a choice or a text or both.
func answerFits(typ string, options int, resp Response) error {
	if resp.Choice != nil && *resp.Choice >= options {
		return refuse(CodeInvalid, "choice %d names no option of the %d", *resp.Choice, options)
	}
	switch typ {
	case ledger.DecisionApproval, ledger.DecisionAssignment:
		if resp.Choice == nil {
			return refuse(CodeInvalid, "this %s is answered with a choice", typ)
		}
	default:
		if resp.Choice == nil && resp.Text == "" {
			return refuse(CodeInvalid, "a clarification is answered with a choice or a text")
		}
	}
	return nil
}

// optionsOf is the labels of a decision's options as its request keeps
// them: an assignment's two fixed answers.
func optionsOf(typ, request string) []string {
	if typ == ledger.DecisionAssignment {
		return []string{"Done", "Not done"}
	}
	var b requestBody
	_ = json.Unmarshal([]byte(request), &b)
	out := make([]string, 0, len(b.Options))
	for _, o := range b.Options {
		out = append(out, o.Label)
	}
	return out
}

// membership is what the engine knows of a held run's asking attempt, for
// a response's recheck: whether the asking task is a task of the run and
// the attempt one a withdrawal did not take back.
type membership struct {
	known, member bool
}

// respond is a response's transaction (Responding, 1 to 3): the repeat,
// the rechecks, the row and its receipt, and, accepted, the decision
// answered by the conditional Tx.Answer. moved says the decision is held
// by another run than run now: nothing was written.
type respondOutcome struct {
	receipt  ResponseReceipt
	accepted bool
	moved    bool
	// unheld says the run stopped between RespondDecision's choice of it
	// and its transaction: nothing was written, and the response is
	// chosen again as one to a run not held.
	unheld  bool
	refusal *DecisionError
	rev     int
}

func respondTx(ltx LedgerTx, run string, n int64, resp Response, actor string, auth Authority, authRaw, fp string, mem membership, moved string, out *respondOutcome) error {
	dtx, err := decisionsOf(ltx)
	if err != nil {
		return err
	}
	// The record as this transaction judges it: a refusal's words (who
	// answered first, what changed since the rev read) are its, never a
	// read made before another response committed (#635, review round 2).
	rec, ok, err := dtx.Record(n)
	if err != nil {
		return err
	}
	d := rec.DecisionRow
	if !ok {
		return refuse(CodeNotFound, "no decision %s", ledger.DecisionID(n))
	}
	if d.CurrentRun != run {
		out.moved = true
		return nil
	}
	id := d.ID()
	if resp.ExpectedRev > d.Rev {
		return refuse(CodeInvalid, "%s is at rev %d: no rev %d has been read", id, d.Rev, resp.ExpectedRev)
	}
	options := optionsOf(d.Type, d.Request)
	if err := answerFits(d.Type, len(options), resp); err != nil {
		return err
	}
	rc := ResponseReceipt{Decision: id, Actor: actor, ByName: resp.ByName, Choice: resp.Choice, Text: resp.Text, Rationale: resp.Rationale, At: dtx.Now(), Rev: d.Rev}
	var policy struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal([]byte(d.Policy), &policy)
	switch {
	case auth.Via == ViaRouter || auth.Grant != grantAdmin || auth.Via != ViaWorkbench && auth.Via != ViaIdentity:
		rc.Code, rc.Words = CodeNotApprover, fmt.Sprintf("%s is the operator's to answer: refused under local/1", id)
	case policy.Revision != policyLocal:
		rc.Code, rc.Words = CodeStalePolicy, fmt.Sprintf("%s was opened under policy %s, and %s is in force", id, policy.Revision, policyLocal)
	case d.State == ledger.DecisionAnswered || d.State == ledger.DecisionApplied:
		rc.Code, rc.Words = CodeDecisionAnswered, answeredWords(id, rec)
	case moved != "" && (d.State == ledger.DecisionOpen || d.State == ledger.DecisionSuperseded):
		// Its subject moved (S2, S4): superseded before this transaction,
		// in a change of its own (supersede), whose rows a commit refused
		// meanwhile carries ahead of these.
		rc.Code, rc.Words = CodeStaleDecision, fmt.Sprintf("%s was superseded: %s", id, moved)
	case d.State != ledger.DecisionOpen:
		why := d.Why
		if why == "" {
			why = "it is " + d.State
		}
		rc.Code, rc.Words = CodeDecisionClosed, fmt.Sprintf("%s is %s: %s", id, d.State, why)
	case resp.ExpectedRev < d.Rev:
		rc.Code, rc.Words = CodeStaleDecision, staleWords(id, resp.ExpectedRev, rec)
	case mem.known && !mem.member:
		rc.Code, rc.Words = CodeStaleDecision, fmt.Sprintf("%s's asking attempt is no longer its task's in this run", id)
	}
	status := ledger.ResponseAccepted
	if rc.Code != "" {
		status = ledger.ResponseRefused
		rc.Status = "refused"
	} else {
		rc.Status = "accepted"
		rc.Words = fmt.Sprintf("Recorded: %s answered by %s", id, nameOf(resp.ByName, actor))
		rc.Rev = d.Rev + 1
	}
	got, err := dtx.Respond(n, ledger.ResponseRow{Actor: actor, Authority: authRaw, ResponseID: resp.ResponseID, Fingerprint: fp, Status: status, Receipt: receiptJSON(rc)})
	switch {
	case errors.Is(err, ledger.ErrDecisionConflict):
		return refuse(CodeResponseConflict, "response %s to %s was sent before with another body", resp.ResponseID, id)
	case errors.Is(err, ledger.ErrDecisionLimit) && auth.Via == ViaIdentity:
		return refuse(CodeDecisionLimit, "%s's run has no room for the decision this answer would need if its recheck failed: answer with the workbench token", id)
	case err != nil:
		return err
	}
	if got.Repeat {
		var prev ResponseReceipt
		_ = json.Unmarshal([]byte(got.Row.Receipt), &prev)
		prev.Repeat, prev.K = true, got.Row.K
		out.receipt, out.accepted, out.rev = prev, got.Row.Status == ledger.ResponseAccepted, d.Rev
		if prev.Code != "" {
			out.refusal = refuse(prev.Code, "%s", prev.Words)
		}
		return nil
	}
	rc.K = got.Row.K
	if !got.Kept {
		kept := false
		rc.Kept = &kept
		rc.Words += ": this refusal was not kept: sending it again is judged again"
		out.receipt, out.refusal, out.rev = rc, refuse(rc.Code, "%s", rc.Words), d.Rev
		return nil
	}
	if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryResponded, By: actor, Detail: historyDetail(map[string]any{"k": got.Row.K, "status": status})}); err != nil {
		return err
	}
	if status == ledger.ResponseRefused {
		out.receipt, out.refusal, out.rev = rc, refuse(rc.Code, "%s", rc.Words), d.Rev
		return nil
	}
	answered, err := dtx.Answer(n, run, d.Rev)
	if err != nil {
		return err
	}
	if !answered {
		// Nothing a transaction of this run did since its look: another run
		// holds the decision now (a continuation), its response is made there.
		return errDecisionMoved
	}
	if err := ltx.Event("decision.answered", map[string]any{"decision": id, "k": got.Row.K, "actor": actor}); err != nil {
		return err
	}
	out.receipt, out.accepted, out.rev = rc, true, d.Rev+1
	return nil
}

var errDecisionMoved = errors.New("the decision moved to another run")

// nameOf is the name a responder is told by: the name it gave, or its
// actor.
func nameOf(byName, actor string) string {
	if byName != "" {
		return byName
	}
	return actor
}

// receiptJSON encodes a receipt as the ledger keeps it.
func receiptJSON(rc ResponseReceipt) string {
	b, _ := json.Marshal(rc)
	return string(b)
}

// answeredWords say who answered a decision and when, from its record as
// the transaction reads it.
func answeredWords(id string, rec ledger.DecisionRecord) string {
	for _, r := range rec.Responses {
		if r.Status != ledger.ResponseAccepted {
			continue
		}
		var rc ResponseReceipt
		_ = json.Unmarshal([]byte(r.Receipt), &rc)
		return fmt.Sprintf("%s was answered by %s at %s", id, nameOf(rc.ByName, rc.Actor), r.At.UTC().Format(time.RFC3339))
	}
	if !rec.AnsweredAt.IsZero() {
		return fmt.Sprintf("%s was answered at %s", id, rec.AnsweredAt.UTC().Format(time.RFC3339))
	}
	return id + " was answered"
}

// staleWords say what changed a decision since the rev a response read,
// from its record as the transaction reads it.
func staleWords(id string, read int, rec ledger.DecisionRecord) string {
	now := rec.Rev
	var kinds []string
	for _, h := range rec.History {
		switch h.Kind {
		case ledger.HistoryCommented, ledger.HistoryInfoRequested, ledger.HistoryResponded, ledger.HistorySuperseded, ledger.HistoryCancelled, ledger.HistoryApplied:
			kinds = append(kinds, h.Kind)
		}
	}
	words := fmt.Sprintf("%s changed since rev %d: it is at rev %d", id, read, now)
	if len(kinds) > 0 {
		words += " (" + strings.Join(kinds, ", ") + ")"
	}
	return words + ": read it again, then answer"
}

// RespondDecision records a response to decision id (Responding), under
// the actor the API names and the authority it authenticated: through the
// run that holds the decision when the engine holds it (its transact), else
// through the ledger on that run directly, changing no engine state, for
// the workbench token only (not_held for an identity). The transaction
// does not trust that choice: its answer is conditional on the run
// (Tx.Answer), and a decision moved meanwhile is looked up and answered
// again, at most maxDecisionTries times (decision_moved after). The
// receipt is returned, with the refusal's DecisionError; an accepted
// response of a held run is then applied (applyDecisions).
func (e *Engine) RespondDecision(ctx context.Context, id string, resp Response, actor string, auth Authority) (ResponseReceipt, error) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return ResponseReceipt{}, refuse(CodeNotFound, "%q is not a decision's id", shortText(id))
	}
	resp, err := checkResponse(resp)
	if err != nil {
		return ResponseReceipt{}, err
	}
	if actor == "" || len(actor) > ledger.MaxDecisionActor {
		return ResponseReceipt{}, refuse(CodeInvalid, "a response names its actor")
	}
	// The process that accepts the response is this one, whatever the
	// caller says: an identity's is applied only here (applyDecisions).
	auth.Boot = e.boot
	authRaw, _ := json.Marshal(auth)
	fp := fingerprintOf(resp)
	for range maxDecisionTries {
		prior, ok, err := e.readDecision(ctx, n)
		if err != nil {
			return ResponseReceipt{}, err
		}
		if !ok {
			return ResponseReceipt{}, refuse(CodeNotFound, "no decision %s", id)
		}
		r, held := e.holder(prior.CurrentRun)
		if e.beforeRespond != nil {
			e.beforeRespond(n)
		}
		var out respondOutcome
		if held {
			err = e.respondHeld(ctx, r, n, resp, actor, auth, string(authRaw), fp, prior, &out)
		} else {
			if auth.Via != ViaWorkbench {
				return ResponseReceipt{}, refuse(CodeNotHeld, "%s's run is not running: continue it, or answer with the workbench token", id)
			}
			_, err = e.ledger.Transition(ctx, prior.CurrentRun, func(ltx LedgerTx) error {
				return respondTx(ltx, prior.CurrentRun, n, resp, actor, auth, string(authRaw), fp, membership{}, "", &out)
			})
		}
		if errors.Is(err, errDecisionMoved) || err == nil && (out.moved || out.unheld) {
			continue
		}
		if err != nil {
			if de := decisionFailure(err); de != nil {
				return ResponseReceipt{}, de
			}
			return ResponseReceipt{}, err
		}
		if out.accepted && held && !out.receipt.Repeat {
			e.applyDecisions(r)
		} else if held {
			// A refusal may have superseded the decision (its subject
			// moved): what it held may start.
			e.pumpLocked(r)
		}
		if out.refusal != nil {
			return out.receipt, out.refusal
		}
		return out.receipt, nil
	}
	return ResponseReceipt{}, refuse(CodeDecisionMoved, "%s moved to another run while it was answered: send it again", id)
}

// holder is the run the engine holds a decision through, the run the
// ledger says holds it (current), and whether it is held: its launch
// recorded, not stopping. A transaction of the run under way is waited
// for first (txMu): the launch's first commit writes the run's gates to
// the ledger before it marks the run created, and a response read
// between the two answered the ledger past the engine, whose gate then
// stayed open (#635, review round 2). Once it is through, a run the ledger
// holds a decision of is created, or its first commit was refused and
// wrote none. The caller holds no lock.
func (e *Engine) holder(current string) (*run, bool) {
	e.mu.Lock()
	r := e.runs[current]
	managed := r != nil && r.managed
	e.mu.Unlock()
	if !managed {
		return r, false
	}
	r.txMu.Lock()
	defer r.txMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	return r, r.created && !r.stopping && r.ctx.Err() == nil
}

// respondHeld is RespondDecision's transaction through a run the engine
// holds: the asking attempt's membership read under e.mu, the row written
// in its transaction, the engine's copy answered once it commits. A
// subject that moved (S2, S4) is superseded first, in a change of its own
// that is never undone (supersede): what the response's transaction then
// refuses or fails, it has nothing in the engine to put back (#636,
// review round 1).
func (e *Engine) respondHeld(ctx context.Context, r *run, n int64, resp Response, actor string, auth Authority, authRaw, fp string, prior ledger.DecisionRecord, out *respondOutcome) error {
	// The subject rechecked first, read before the transaction at the
	// place the supersession checks it is still read at.
	e.mu.Lock()
	d := r.decisionN(n)
	e.mu.Unlock()
	moved := ""
	if d != nil {
		at, words, err := e.recheckSubject(ctx, r, d)
		if err != nil {
			// The recheck was cut short by the response's own context: it
			// says nothing of the subject, and nothing is changed (#636,
			// review round 2).
			return err
		}
		if words != "" {
			moved = e.supersede(r, n, at, words)
		}
	}
	return e.transact(ctx, r, func() tx {
		if r.stopping || r.ctx.Err() != nil {
			// Stopped since RespondDecision chose it: no longer held.
			out.unheld = true
			return tx{}
		}
		d = r.decisionN(n)
		mem := membership{}
		if prior.Origin == ledger.OriginAgent {
			mem.known = true
			if t := r.task(prior.TaskID); t != nil {
				mem.member = t.kept(prior.Attempt)
			}
		}
		return tx{more: func(ltx LedgerTx) error {
			return respondTx(ltx, r.id, n, resp, actor, auth, authRaw, fp, mem, moved, out)
		}, done: func() {
			if !out.accepted || out.receipt.Repeat || d == nil || d.state != ledger.DecisionOpen {
				return
			}
			d.state, d.rev = ledger.DecisionAnswered, out.rev
			d.answer = &accepted{k: out.receipt.K, choice: resp.Choice, text: resp.Text, rationale: resp.Rationale, byName: resp.ByName, actor: actor, auth: auth}
			r.note(session.ActivityStatus, "decision %s answered by %s", d.id(), nameOf(resp.ByName, actor))
		}}
	}, func() {})
}

// supersede supersedes the run's open decision n, whose subject the read
// at found moved (words), in a transaction of its own with no undo: a
// refused commit carries it, and the run pauses until the pause's commit
// writes it, so the engine, which has superseded it, and the ledger never
// disagree for good. It answers the words it was superseded with, "" when
// the decision was no longer open at at by the change (answered, closed,
// or its subject read elsewhere since). Under no request's context: a
// caller gone does not refuse it. The caller holds no lock.
func (e *Engine) supersede(r *run, n int64, at subjectAt, words string) string {
	done := ""
	err := e.transact(context.Background(), r, func() tx {
		d := r.decisionN(n)
		if r.stopping || r.ctx.Err() != nil || d == nil || d.state != ledger.DecisionOpen || !e.sameSubject(r, d, at) {
			return tx{}
		}
		e.supersedeAgent(r, d, words)
		done = words
		return tx{}
	}, nil)
	e.afterTransact(r, err)
	return done
}

// confirmKey names an accepted response: its decision and its k.
type confirmKey struct {
	n int64
	k int
}

// confirmation is what the recheck of d's accepted identity response
// answered (ConfirmAuthority): 1 admitted again, -1 not, 0 not yet told.
// The caller holds e.mu.
func (e *Engine) confirmation(d *decision) int {
	if d.answer == nil {
		return 0
	}
	return e.confirms[confirmKey{d.n, d.answer.k}]
}

// ConfirmAuthority tells the engine whether the identity provider admitted
// the token of decision id's accepted response k again after the response
// committed (the response route's recheck, Applying): an identity's
// response is applied only once admitted; not admitted, the decision is
// superseded by a fresh one with its holds. The answer is the engine's,
// kept by the response it names, not by a run's copy of the decision:
// whichever run holds the decision reads it, a continuation that moved it
// included, and a run being continued or continued leaves it to its
// continuation, which applies what it moved once its move commits (#636,
// review round 2).
func (e *Engine) ConfirmAuthority(id string, k int, admitted bool) {
	n, ok := ledger.ParseDecisionID(id)
	if !ok {
		return
	}
	e.mu.Lock()
	var holders []*run
	known := false
	for _, r := range e.runs {
		d := r.decisionN(n)
		if d == nil || d.answer == nil || d.answer.k != k || d.state != ledger.DecisionAnswered {
			continue
		}
		known = true
		if e.continued(r) == nil && !r.unrecorded() {
			holders = append(holders, r)
		}
	}
	if known {
		if e.confirms == nil {
			e.confirms = map[confirmKey]int{}
		}
		e.confirms[confirmKey{n, k}] = -1
		if admitted {
			e.confirms[confirmKey{n, k}] = 1
		}
	}
	e.mu.Unlock()
	for _, r := range holders {
		e.applyDecisions(r)
	}
}

// applyDecisions applies the run's answered decisions, one transaction
// each (Applying): the rechecks again, then the hold released and the
// answer typed into the asking attempt's live session, or its task's next
// attempt reads it; a gate's tasks released, or cancelled once the
// decision is final; the receipt, applied, the event. An identity's
// response waits for its recheck (ConfirmAuthority); one not admitted, or
// accepted by another server process, is superseded by a fresh decision
// with its holds. A decision is applied at most once (I1). The caller
// holds no lock.
func (e *Engine) applyDecisions(r *run) {
	for {
		e.mu.Lock()
		if e.continued(r) != nil {
			// Its decisions are its continuation's to apply (ConfirmAuthority).
			e.mu.Unlock()
			return
		}
		var next *decision
		for _, d := range r.decisions {
			if d.state != ledger.DecisionAnswered || d.applying || d.answer == nil || d.n == 0 {
				continue
			}
			if d.answer.auth.Via == ViaIdentity && d.answer.auth.Boot == e.boot && e.confirmation(d) == 0 {
				continue // its recheck has not answered yet
			}
			next = d
			break
		}
		if next != nil {
			next.applying = true
		}
		e.mu.Unlock()
		if next == nil {
			return
		}
		e.applyOne(r, next)
		e.mu.Lock()
		next.applying = false
		e.mu.Unlock()
	}
}

// applyOne applies d, answered, in one transaction of r's. What follows
// the application (its line queued, its mirror withdrawn, a replacement's
// question asked) is the transaction's done: it follows the commit that
// writes the application, this one or the next (a refused commit's change
// is carried), and never one that did not.
func (e *Engine) applyOne(r *run, d *decision) {
	var err error
	for try := 0; ; try++ {
		// Under no request's context: nothing cuts the recheck short.
		at, moved, _ := e.recheckSubject(context.Background(), r, d)
		again := false
		err = e.transact(context.Background(), r, func() tx {
			if d.state != ledger.DecisionAnswered || r.decisionN(d.n) != d || e.continued(r) != nil {
				return tx{}
			}
			if !e.sameSubject(r, d, at) && try < maxDecisionTries {
				// Its asking attempt went, or its start moved, since the
				// read: read again.
				again = true
				return tx{}
			}
			if moved != "" {
				e.supersedeAgent(r, d, moved)
				return tx{}
			}
			return e.applyTx(r, d)
		}, nil)
		if !again {
			break
		}
	}
	if err != nil {
		// Carried by the pause's commit, whose done then queues the line.
		e.afterTransact(r, err)
		return
	}
	e.pumpLocked(r)
}

// applyTx is applyOne's change: the application receipt, applied, the
// holds released or the gate's tasks cancelled, the delivery's intent, and
// what follows its commit. The caller holds e.mu, in the transaction's
// change.
func (e *Engine) applyTx(r *run, d *decision) tx {
	ans := d.answer
	revoked := e.confirmation(d) < 0
	delete(e.confirms, confirmKey{d.n, ans.k})
	if ans.auth.Via == ViaIdentity && (ans.auth.Boot != e.boot || revoked) {
		replace, t := e.replaceDecision(r, d, fmt.Sprintf("%s's answer could not be rechecked", ans.actor))
		opened := t.done
		t.done = func() {
			opened()
			e.withdrawMirror(d.a, d)
			if replace.a != nil {
				go e.askMirror(r, replace.a, replace)
			}
		}
		return t
	}
	var line *answerLine
	app := Application{At: time.Now().UTC(), By: "yardmaster", Run: r.id, Response: ans.k, Validated: true}
	words := answerWords(d, ans)
	var events []event
	held := slices.Clone(d.holds)
	switch {
	case d.origin == ledger.OriginAgent && d.a != nil && d.t.active() == d.a && d.a.closedAt.IsZero() && e.sessionLive(d.a.sessionID):
		app.How, app.Task, app.Attempt = HowTyped, d.t.def.ID, d.a.n
		app.Words = fmt.Sprintf("typed into %s's attempt %d", d.t.def.ID, d.a.n)
		line = &answerLine{d: d, a: d.a, text: words, byName: nameOf(ans.byName, ans.actor)}
		// Its typing's intent, before the line (I6), and the channel a
		// supersession withdraws the line by.
		d.delivery, d.withdrawn = ledgerTyping, make(chan struct{})
	case d.origin == ledger.OriginAgent:
		app.How, app.Task = HowTaskFile, d.t.def.ID
		if d.a != nil {
			app.Attempt = d.a.n
		}
		app.Words = fmt.Sprintf("%s's next attempt reads the answer in its task file", d.t.def.ID)
	case len(held) == 0:
		app.How, app.Words = HowNoted, "noted: the plan's decision holds no task"
	case rejects(d, ans):
		app.How = HowCancelled
		for _, t := range held {
			app.Tasks = append(app.Tasks, t.def.ID)
		}
		app.Words = "cancelled " + strings.Join(app.Tasks, ", ")
	default:
		app.How = HowReleased
		for _, t := range held {
			app.Tasks = append(app.Tasks, t.def.ID)
		}
		app.Words = "released " + strings.Join(app.Tasks, ", ")
	}
	// Final first (A decision is final before the tasks it cancels):
	// the engine's copy and its holds, then the tasks it cancels, whose
	// settlement finds it final.
	d.state, d.rev = ledger.DecisionApplied, d.rev+1
	r.dropDecision(d)
	for _, t := range held {
		r.release(t, d)
	}
	switch {
	case d.origin == ledger.OriginAgent && d.typ == ledger.DecisionApproval:
		// Superseded still by a new candidate (S1), or rechecked at the
		// next start while its line is not known to have gone in (S4).
		r.dec.settled = append(r.dec.settled, d)
	case d.gate != "" && app.How == HowCancelled:
		r.dec.endGate(d.gate, gateEnd{id: d.id(), rejected: true})
	case d.gate != "" && r.dec.gates != nil:
		// Released for the run chain (I14).
		r.dec.gates[d.gate] = true
	}
	typing := app.How == HowTyped
	if app.How == HowCancelled {
		why := fmt.Sprintf("rejected in %s by %s", d.id(), nameOf(ans.byName, ans.actor))
		if ans.rationale != "" {
			why += ": " + shortText(ans.rationale)
		}
		// The run's end once, after every cancellation: one that dooms a
		// task held with it settles the run before that task's own.
		was := r.outcome
		for _, t := range held {
			if cancellable(t) == nil {
				for _, ev := range e.cancelTask(r, t) {
					if ev.kind != "run.finished" {
						events = append(events, ev)
					}
				}
				if t.state == ledger.TaskCancelled {
					t.reason = why
					r.markTask(t)
				}
			}
		}
		if was == "" && r.outcome != "" {
			events = append(events, event{"run.finished", map[string]any{"outcome": r.outcome}})
		}
	}
	r.note(session.ActivityStatus, "decision %s applied: %s", d.id(), app.Words)
	// Within the ledger's bound whatever the gate held (128 tasks' ids are
	// more than it): the change below cannot be put back (#636, review
	// round 3).
	appJSON := fitJSON(app, ledger.MaxDecisionApplied)
	n, rev := d.n, d.rev
	return tx{events: events, more: func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		row, ok, err := dtx.Decision(n)
		if err != nil {
			return err
		}
		if !ok || row.State != ledger.DecisionAnswered || row.CurrentRun != r.id {
			return fmt.Errorf("%w: %s is not answered in %s", ledger.ErrDecisionState, ledger.DecisionID(n), r.id)
		}
		row.State, row.Rev, row.Applied = ledger.DecisionApplied, max(rev, row.Rev+1), appJSON
		if err := dtx.PutDecision(row); err != nil {
			return err
		}
		if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryApplied, By: "yardmaster", Detail: historyDetail(map[string]any{"how": app.How, "tasks": app.Tasks})}); err != nil {
			return err
		}
		if typing {
			if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledger.HistoryDeliveryTyping, By: "yardmaster", Detail: historyDetail(map[string]any{"task": app.Task, "attempt": app.Attempt})}); err != nil {
				return err
			}
		}
		for _, t := range held {
			if _, err := dtx.Release(r.id, t.def.ID, n); err != nil {
				return err
			}
		}
		return ltx.Event("decision.applied", map[string]any{"decision": row.ID(), "how": app.How, "tasks": app.Tasks})
	}, done: func() {
		if line != nil {
			e.queueAnswer(r, line)
		} else if d.a != nil {
			e.withdrawMirror(d.a, d)
		}
	}}
}

// rejects reports whether a gate's answer cancels its tasks: an approval's
// Reject, an assignment's Not done.
func rejects(d *decision, ans *accepted) bool {
	if ans.choice == nil {
		return false
	}
	switch d.typ {
	case ledger.DecisionApproval, ledger.DecisionAssignment:
		return *ans.choice == 1
	}
	return false
}

// answerWords is the line an answer is typed as.
func answerWords(d *decision, ans *accepted) string {
	var answer string
	if ans.choice != nil && *ans.choice < len(d.options) {
		answer = d.options[*ans.choice]
	} else if ans.choice != nil && d.typ == ledger.DecisionAssignment {
		answer = []string{"Done", "Not done"}[min(*ans.choice, 1)]
	}
	if ans.text != "" {
		if answer != "" {
			answer += ": "
		}
		answer += ans.text
	}
	line := fmt.Sprintf("Decision %s (%s) answered by %s: %s.", d.id(), d.typ, nameOf(ans.byName, ans.actor), strings.TrimSuffix(answer, "."))
	if ans.rationale != "" {
		line += " Why: " + strings.TrimSuffix(ans.rationale, ".") + "."
	}
	line += fmt.Sprintf(" (receipt %s/%d)", d.id(), ans.k)
	return strings.TrimSpace(oneLine.Replace(line))
}

// replaceDecision supersedes answered d, whose answer's authority could
// not be rechecked, by a fresh decision of the same request with the same
// holds, in the caller's transaction: the operator answers again. The
// caller holds e.mu, in that transaction's change.
func (e *Engine) replaceDecision(r *run, d *decision, why string) (*decision, tx) {
	fresh := &decision{origin: d.origin, typ: d.typ, gate: d.gate, t: d.t, a: d.a, question: d.question, options: slices.Clone(d.options), state: ledger.DecisionOpen, rev: 1, subject: d.subject}
	held := slices.Clone(d.holds)
	d.state = ledger.DecisionSuperseded
	r.dropDecision(d)
	r.decisions = append(r.decisions, fresh)
	for _, t := range held {
		r.release(t, d)
		r.hold(t, fresh)
	}
	var opened int64
	n := d.n
	return fresh, tx{more: func(ltx LedgerTx) error {
		dtx, err := decisionsOf(ltx)
		if err != nil {
			return err
		}
		old, ok, err := dtx.Decision(n)
		if err != nil {
			return err
		}
		if !ok || old.State != ledger.DecisionAnswered || old.CurrentRun != r.id {
			return fmt.Errorf("%w: %s is not answered in %s", ledger.ErrDecisionState, ledger.DecisionID(n), r.id)
		}
		row := old
		// Opened in the run that holds the decision now, a continuation's
		// when it moved there.
		row.N, row.RequestID, row.RunID = 0, "", ""
		// Superseded first: the answer's place among the run's bounds is the
		// replacement's (A replacement always has its place).
		old.State, old.Rev, old.Why = ledger.DecisionSuperseded, old.Rev+1, why
		if err := dtx.PutDecision(old); err != nil {
			return err
		}
		got, _, err := dtx.OpenDecision(row)
		if err != nil {
			return err
		}
		opened = got.N
		r.opened(fresh, got.N)
		words := fmt.Sprintf("%s; %s asks again", why, got.ID())
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryOpened, By: "yardmaster", Detail: historyDetail(map[string]any{"follows": old.ID(), "why": words})}); err != nil {
			return err
		}
		if _, _, err := dtx.History(got.N, ledger.HistoryRow{Kind: ledger.HistoryRouted, By: policyLocal, Detail: routedOperator}); err != nil {
			return err
		}
		old.Why, old.SupersededBy = words, got.ID()
		if err := dtx.PutDecision(old); err != nil {
			return err
		}
		if _, _, err := dtx.History(n, ledger.HistoryRow{Kind: ledger.HistorySuperseded, By: "yardmaster", Detail: historyDetail(map[string]any{"by": got.ID(), "why": words})}); err != nil {
			return err
		}
		for _, t := range held {
			if _, err := dtx.Release(r.id, t.def.ID, n); err != nil {
				return err
			}
			if err := dtx.Hold(r.id, t.def.ID, got.N); err != nil {
				return err
			}
		}
		if err := ltx.Event("decision.superseded", map[string]any{"decision": old.ID(), "by": got.ID(), "why": words}); err != nil {
			return err
		}
		return ltx.Event("decision.opened", map[string]any{"decision": got.ID(), "origin": old.Origin, "type": old.Type, "follows": old.ID()})
	}, done: func() {
		fresh.n, fresh.openedAt = opened, time.Now().UTC()
		r.dec.opened++
		r.note(session.ActivityStatus, "decision %s superseded: %s; %s asks again", d.id(), why, fresh.id())
	}}
}

// answerLine is a decision's answer waiting to be typed into the asking
// attempt's session by its member's deliverer (handoff.go): ahead of the
// member's handoffs, the one whose mirror shows first.
type answerLine struct {
	d      *decision
	a      *attempt
	text   string
	byName string
	// plain says the line goes as a line naming its decision, never as
	// its mirror's answer: the mirror was set aside between the look and
	// the line (typeAnswer). Its id stays, for its withdrawal.
	plain bool
	// notice marks a line about the decision that is not its answer (a
	// supersession told, the operator's request for more): typed as a line
	// naming the decision, never as the answer to its question, and with
	// no delivery of its own.
	notice bool
}

// queueAnswer queues an applied answer for the asking attempt's member's
// deliverer and wakes it, starting one when none runs. The caller holds
// e.mu: the application's commit (its done).
func (e *Engine) queueAnswer(r *run, line *answerLine) {
	m := line.a.m
	m.answers = append(m.answers, line)
	if m.delivering.Load() {
		m.poke()
		return
	}
	l, ok := e.lookup(line.a.sessionID)
	if !ok || isClosed(l.Ended()) {
		m.answers = slices.DeleteFunc(m.answers, func(x *answerLine) bool { return x == line })
		r.note(session.ActivityError, "decision %s's answer was not typed: %s's session has ended", line.d.id(), m.def.Name)
		if !line.notice {
			go e.lineEnded(r, line.d, ledgerUntyped, "the asking session has ended")
		}
		return
	}
	m.delivering.Store(true)
	r.deliveries.Add(1)
	go e.deliver(r, m, l)
}

// nextAnswer takes the decision answer the deliverer types next into l, its
// member's session: the one whose mirror is the question l shows, read on
// its attempt's askMu, else the first in their order; nil for none. An
// answer whose attempt's session is not l, or has ended, is dropped (it
// reaches only the work it is for, I4). The caller holds no lock.
func (e *Engine) nextAnswer(r *run, m *member, l *session.Local) (*answerLine, string) {
	e.mu.Lock()
	lines := slices.Clone(m.answers)
	e.mu.Unlock()
	if len(lines) == 0 {
		return nil, ""
	}
	shown := l.Info().Attention.ID
	var pick *answerLine
	mirror := ""
	for _, al := range lines {
		al.a.askMu.Lock()
		e.mu.Lock()
		q := al.d.mirror
		e.mu.Unlock()
		al.a.askMu.Unlock()
		if q != "" && q == shown && !al.plain && !al.notice {
			pick, mirror = al, q
			break
		}
	}
	if pick == nil {
		pick = lines[0]
	}
	e.mu.Lock()
	m.answers = slices.DeleteFunc(m.answers, func(x *answerLine) bool { return x == pick })
	e.mu.Unlock()
	return pick, mirror
}

// typeAnswer types a decision's answer into l: answering its mirror when
// that is the question shown (Question and Decision), else as a line that
// names its decision (Decision, UnlessWaiting), which passes every other
// decision's mirror. It reports whether the line must wait (back to the
// head of the answers, until the session's next activity wakes the
// deliverer). An answer leaves the queue only for good: typed, or its
// session or run gone; a refusal that typed nothing because a question or
// a dialog came first puts it back (#635, review round 2). The mirror is
// withdrawn once the line is in: at once, or when a line the program took
// slowly has finished (Submission.Finished), never while its Enter may
// still answer it.
func (e *Engine) typeAnswer(r *run, m *member, l *session.Local, al *answerLine, mirror string) (wait bool) {
	e.mu.Lock()
	ok := al.a.sessionID == l.Info().ID && al.a.closedAt.IsZero()
	e.mu.Unlock()
	if !ok {
		e.mu.Lock()
		r.note(session.ActivityError, "decision %s's answer was not typed: its attempt's session is not %s's now", al.d.id(), m.def.Name)
		e.mu.Unlock()
		if !al.notice {
			e.lineEnded(r, al.d, ledgerUntyped, "the asking attempt's session has ended")
		}
		return false
	}
	sub := session.Submission{Text: al.text, ByName: al.byName, Decision: al.d.id(), Finished: func(res session.SubmitResult, err error) {
		// Accepted while the program took it slowly (ErrSubmitPending): how
		// it ended, and the mirror withdrawn only now, off the session's
		// goroutine (lineDone commits a delivery row).
		e.mu.Lock()
		e.noteAnswer(r, m, al, res, err)
		e.mu.Unlock()
		go e.lineDone(r, al, res, err)
	}}
	if mirror != "" {
		sub.Question = mirror
	} else {
		sub.UnlessWaiting = true
	}
	if !al.notice {
		e.mu.Lock()
		if al.d.withdrawn != nil {
			sub.Withdrawn = al.d.withdrawn
		}
		e.mu.Unlock()
		if e.beforeTyping != nil {
			e.beforeTyping(al.d.n)
		}
	}
	submit := l.Submit
	if e.submitAnswer != nil {
		submit = func(ctx context.Context, sub session.Submission) (session.SubmitResult, error) {
			return e.submitAnswer(ctx, l, sub)
		}
	}
	res, err := submit(r.ctx, sub)
	if e.tried != nil {
		e.tried(m.def.Name, res.Typed)
	}
	switch {
	case mirror != "" && errors.Is(err, session.ErrQuestionAnswered) && !res.Typed:
		// Its mirror set aside or gone between the look and the line: typed
		// again as a line naming its decision, at the next pass, and the
		// mirror, which the session keeps aside, withdrawn by its id once
		// the line is in (#635, review round 1).
		e.mu.Lock()
		al.plain = true
		m.answers = slices.Insert(m.answers, 0, al)
		e.mu.Unlock()
		return false
	case !res.Typed && (errors.Is(err, session.ErrQuestion) || errors.Is(err, session.ErrTrustQuestion)):
		// A dialog or a question found on the screen as the line began, or
		// as its text was taken (the session's look, its writer's guard),
		// nothing typed: the answer waits for it to be answered, as a line
		// that waits on a prompt does, and is looked at again then.
		e.mu.Lock()
		m.answers = slices.Insert(m.answers, 0, al)
		r.noteQueued(m, "is waiting for input")
		e.mu.Unlock()
		return true
	case err == nil && !res.Typed:
		e.mu.Lock()
		m.answers = slices.Insert(m.answers, 0, al)
		r.noteQueued(m, "is waiting for input")
		e.mu.Unlock()
		return true
	case errors.Is(err, session.ErrSubmitPending):
		e.mu.Lock()
		r.note(session.ActivityStatus, "decision %s's answer waits for %s to take its input: it is entered once it has", al.d.id(), m.def.Name)
		e.mu.Unlock()
		return false
	}
	e.mu.Lock()
	e.noteAnswer(r, m, al, res, err)
	e.mu.Unlock()
	e.lineDone(r, al, res, err)
	return false
}

// noteAnswer notes how a decision's line ended: entered, or not. The
// caller holds e.mu.
func (e *Engine) noteAnswer(r *run, m *member, al *answerLine, res session.SubmitResult, err error) {
	if err == nil && res.Entered {
		r.note(session.ActivityStatus, "decision %s's answer typed into %s", al.d.id(), m.def.Name)
		return
	}
	if err == nil {
		err = errors.New("typed without its Enter")
	}
	r.note(session.ActivityError, "decision %s's answer was not entered into %s: %v", al.d.id(), m.def.Name, err)
}

// lineDone follows a decision's line that ended, now or once its program
// took it (ErrSubmitPending): an answer's delivery row, entered or
// untyped (I6), and its mirror withdrawn; a notice's line (a
// supersession's, a cancel's, the operator's request for more) answers
// nothing, and leaves the decision's mirror be. The caller holds no lock.
func (e *Engine) lineDone(r *run, al *answerLine, res session.SubmitResult, err error) {
	if al.notice {
		return
	}
	if err == nil && res.Entered {
		e.lineEnded(r, al.d, ledgerEntered, "")
	} else {
		e.lineEnded(r, al.d, ledgerUntyped, errWords(err))
	}
	e.withdrawMirror(al.a, al.d)
}

// errWords is an error in words.
func errWords(err error) string {
	if err == nil {
		return "its Enter was not written"
	}
	return session.CleanMessage(err.Error())
}
