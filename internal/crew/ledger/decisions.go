package ledger

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Decision requests (DR1, docs/plans/decisions.md, The changes, the
// ledger): a question an attempt's agent or a plan revision's gate puts to
// a person, its responses, its history, the holds it puts on tasks and the
// attempts given back while it waited. They are tables of the ledger, not a
// store of their own, so that a decision's opening, its response, its
// application and its holds commit with the task rows and the run event of
// the same transition (Owner decisions, 2). The id is "dr-<n>", n from
// AUTOINCREMENT, never given twice in a data directory (Owner decisions,
// 3). Every writer here is a Tx method, called inside a run's Transition;
// nothing here starts a process or reads a file.

// versionDecisions is the first schema version with the decision tables:
// a reader of an older database, opened read-only and unmigrated, finds
// no decision.
const versionDecisions = 8

// decisionsRequestV9 is version 9's key of a request id: the run that
// opened the decision, its member, its asking task and the id (migration
// 8, and a new database's after decisionTables). A plan's decision has no
// request id and no task.
var decisionsRequestV9 = []string{
	`DROP INDEX decisions_request`,
	`CREATE UNIQUE INDEX decisions_request ON decisions(run_id, member, task_id, request_id) WHERE request_id != ''`,
}

// decisionTables are the decision tables and their indexes as version 8
// made them: a new database's (schema) and migration 7's. A migration's
// statements are history: a later version's change to these tables is
// that version's migration, never this text's.
var decisionTables = []string{
	`CREATE TABLE decisions (
		n INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		current_run TEXT NOT NULL,
		origin TEXT NOT NULL,
		type TEXT NOT NULL,
		gate TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		member TEXT NOT NULL DEFAULT '',
		ticket TEXT NOT NULL DEFAULT '',
		plan TEXT NOT NULL DEFAULT '',
		plan_digest TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL,
		request TEXT NOT NULL,
		evidence TEXT NOT NULL DEFAULT '[]',
		policy TEXT NOT NULL,
		state TEXT NOT NULL,
		rev INTEGER NOT NULL DEFAULT 1,
		why TEXT NOT NULL DEFAULT '',
		superseded_by TEXT NOT NULL DEFAULT '',
		applied TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		answered_at TEXT NOT NULL DEFAULT '',
		closed_at TEXT NOT NULL DEFAULT '',
		dropped INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE UNIQUE INDEX decisions_request ON decisions(run_id, member, request_id) WHERE request_id != ''`,
	`CREATE INDEX decisions_current ON decisions(current_run, state)`,
	`CREATE TABLE decision_responses (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		k INTEGER NOT NULL,
		actor TEXT NOT NULL,
		authority TEXT NOT NULL,
		response_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		status TEXT NOT NULL,
		receipt TEXT NOT NULL,
		at TEXT NOT NULL,
		PRIMARY KEY (decision, k),
		UNIQUE (decision, actor, response_id)
	)`,
	`CREATE TABLE decision_history (
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		at TEXT NOT NULL,
		kind TEXT NOT NULL,
		by TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		op_id TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (decision, seq)
	)`,
	`CREATE UNIQUE INDEX decision_ops ON decision_history(decision, by, op_id) WHERE op_id != ''`,
	`CREATE TABLE decision_holds (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		released_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, task_id, decision),
		FOREIGN KEY (run_id, task_id) REFERENCES tasks(run_id, id) ON DELETE CASCADE
	)`,
	`CREATE INDEX decision_holds_decision ON decision_holds(decision)`,
	`CREATE TABLE decision_refunds (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		decision INTEGER NOT NULL REFERENCES decisions(n) ON DELETE CASCADE,
		at TEXT NOT NULL,
		PRIMARY KEY (run_id, task_id, n),
		FOREIGN KEY (run_id, task_id, n) REFERENCES attempts(run_id, task_id, n) ON DELETE CASCADE
	)`,
}

// A decision's origins, types and states. open → answered (a response
// accepted) → applied; open or answered → superseded or cancelled; open →
// expired, a provider's alone. Applied, superseded, cancelled and expired
// are final: nothing changes a final decision but its history and one
// transition, applied → superseded (an applied approval whose subject
// moved).
const (
	OriginAgent = "agent"
	OriginPlan  = "plan"

	DecisionClarification = "clarification"
	DecisionApproval      = "approval"
	DecisionAssignment    = "assignment"

	DecisionOpen       = "open"
	DecisionAnswered   = "answered"
	DecisionApplied    = "applied"
	DecisionSuperseded = "superseded"
	DecisionCancelled  = "cancelled"
	DecisionExpired    = "expired"

	ResponseAccepted = "accepted"
	ResponseRefused  = "refused"

	// ViaIdentity is the authority of a response an identity provider's
	// principal gave: its acceptance holds a place for the decision that
	// would replace it (A replacement always has its place).
	ViaIdentity = "identity"
)

// History kinds, each written in the transaction of the change it records.
// The last four are a provider's: no code of this repository writes them.
const (
	HistoryOpened            = "opened"
	HistoryRouted            = "routed"
	HistoryNotified          = "notified"
	HistoryResponded         = "responded"
	HistoryApplied           = "applied"
	HistoryRefunded          = "refunded"
	HistoryDeliveryTyping    = "delivery.typing"
	HistoryDeliveryEntered   = "delivery.entered"
	HistoryDeliveryUntyped   = "delivery.untyped"
	HistoryDeliveryUncertain = "delivery.uncertain"
	HistoryDeliveryTaskFile  = "delivery.task_file"
	HistoryCommented         = "commented"
	HistoryInfoRequested     = "info_requested"
	HistorySuperseded        = "superseded"
	HistoryCancelled         = "cancelled"
	HistoryAssigned          = "assigned"
	HistoryDelegated         = "delegated"
	HistoryEscalated         = "escalated"
	HistoryExpired           = "expired"
)

// Bounds of a decision's record (Owner decisions, 15): each part keeps its
// own, so the record keeps their sum.
const (
	// MaxDecisionRequest bounds the request as encoded; the rest bound its
	// fields, in characters where they say so, else in bytes.
	MaxDecisionRequest     = 16 << 10
	MaxDecisionQuestion    = 500 // characters, one line: Dispatch's MaxDecisionQuestion
	MaxDecisionContext     = 4000
	MaxDecisionOptions     = 6
	MaxOptionLabel         = 80 // characters
	MaxOptionTradeoffs     = 1 << 10
	MaxDecisionUncertainty = 1 << 10
	MaxDecisionAuthorizes  = 1 << 10
	MaxDecisionEvidence    = 20
	MaxEvidenceRef         = 300
	MaxDecisionExpertise   = 4
	MaxExpertiseLabel      = 40 // characters
	// MaxDecisionBaseline bounds the evidence baseline as encoded, and
	// MaxBaselineEntry each of its entries.
	MaxDecisionBaseline = 10 << 10
	MaxBaselineEntry    = 512
	// MaxDecisionPolicy bounds the policy and MaxAuthority a response's
	// authority, as encoded.
	MaxDecisionPolicy = 1 << 10
	MaxAuthority      = 1 << 10
	// MaxDecisionReceipt bounds a response's receipt, MaxDecisionApplied
	// the application receipt, MaxHistoryDetail a history row's detail
	// and MaxDecisionWhy a final state's words, as encoded.
	MaxDecisionReceipt = 8 << 10
	MaxDecisionApplied = 4 << 10
	MaxHistoryDetail   = 2 << 10
	MaxDecisionWhy     = 2 << 10
	// MaxDecisionKey bounds an idempotency key (a request's, a response's,
	// an operation's), a fingerprint and a row's identifiers.
	MaxDecisionKey = 128
	// MaxDecisionActor bounds a response's actor and a history row's by.
	MaxDecisionActor = 256

	// MaxOpenPerAttempt, MaxOpenPerRun and MaxDecisionsPerRun bound the
	// decisions open per attempt, open per run, and in all per run.
	MaxOpenPerAttempt  = 4
	MaxOpenPerRun      = 64
	MaxDecisionsPerRun = 1000
	// MaxDecisionResponses bounds a decision's response rows, of which at
	// most MaxDecisionRefusals refused: the accepted one always has its
	// place.
	MaxDecisionResponses = 32
	MaxDecisionRefusals  = MaxDecisionResponses - 1
	// MaxDecisionOperations bounds the operator's operations on a
	// decision (comments, requests for information, re-routes).
	MaxDecisionOperations = 32
	// MaxDecisionHistory bounds a decision's history rows.
	MaxDecisionHistory = 256
	// MaxDecisionsPage bounds a page of Decisions.
	MaxDecisionsPage = 100
)

// historyBounds is how many rows each kind may hold; the kinds that share
// a bound share a group. Operations (an operator's comment, request for
// information or re-route, each with its op id) share MaxDecisionOperations.
// The groups' sum is the mandatory rows' room (mandatoryHistory); notified
// rows, and a provider's, take the rest of MaxDecisionHistory.
var historyBounds = map[string]struct {
	group string
	max   int
}{
	HistoryOpened:            {"opened", 1},
	HistoryRouted:            {"routed", 2},
	HistoryResponded:         {"responded", MaxDecisionResponses},
	HistoryApplied:           {"applied", 1},
	HistorySuperseded:        {"final", 1},
	HistoryCancelled:         {"final", 1},
	HistoryRefunded:          {"refunded", 1},
	HistoryDeliveryTyping:    {"delivery", 3},
	HistoryDeliveryEntered:   {"delivery", 3},
	HistoryDeliveryUntyped:   {"delivery", 3},
	HistoryDeliveryUncertain: {"delivery", 3},
	HistoryDeliveryTaskFile:  {"delivery", 3},
	HistoryCommented:         {"ops", MaxDecisionOperations},
	HistoryInfoRequested:     {"ops", MaxDecisionOperations},
	HistoryNotified:          {"rest", 0},
	HistoryAssigned:          {"rest", 0},
	HistoryDelegated:         {"rest", 0},
	HistoryEscalated:         {"rest", 0},
	HistoryExpired:           {"rest", 0},
}

// mandatoryHistory is the room the bounded kinds keep: one opened, two
// routed, the operations, the responses, one applied, one supersession or
// cancellation, one refund and three deliveries.
const mandatoryHistory = 1 + 2 + MaxDecisionOperations + MaxDecisionResponses + 1 + 1 + 1 + 3

// Errors of the decision writers.
var (
	// ErrDecisionLimit says a bound of the store would be passed: the
	// decisions open per attempt or per run, a run's in all, a decision's
	// responses, operations or history. Nothing is written.
	ErrDecisionLimit = errors.New("ledger: decision_limit")
	// ErrNoDecision says the decision has no row.
	ErrNoDecision = errors.New("ledger: no such decision")
	// ErrDecisionConflict says a request, response or operation id was
	// sent again with another body.
	ErrDecisionConflict = errors.New("ledger: decision_conflict")
	// ErrNotCurrent says the run named does not hold the decision now: its
	// current_run is another's.
	ErrNotCurrent = errors.New("ledger: the run does not hold the decision")
	// ErrDecisionState says the change is not one the decision's state
	// allows.
	ErrDecisionState = errors.New("ledger: the decision's state does not allow it")
	// ErrInvalidDecision says a row's content is not of its shape.
	ErrInvalidDecision = errors.New("ledger: invalid decision")
)

// DecisionID is the id of decision n, "dr-<n>".
func DecisionID(n int64) string { return "dr-" + strconv.FormatInt(n, 10) }

// ParseDecisionID is the n of a decision id, false for text that is not one.
func ParseDecisionID(id string) (int64, bool) {
	s, ok := strings.CutPrefix(id, "dr-")
	if !ok || s == "" || s[0] == '0' || len(s) > 18 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// DecisionRow is a decision request's row. Request, Evidence, Policy and
// Applied are JSON (The changes: request, evidence, policy, applied);
// Request, Evidence and Policy are written by the opening and never after.
type DecisionRow struct {
	N          int64
	RunID      string // the run it was opened in
	CurrentRun string // the run that holds it now (a continuation's)
	Origin     string
	Type       string
	Gate       string // a plan decision's id (origin plan)
	TaskID     string // the asking task (origin agent)
	Attempt    int
	SessionID  string
	Member     string
	Ticket     string
	Plan       string
	PlanDigest string
	RequestID  string
	// Fingerprint is the SHA-256 of the request as sent.
	Fingerprint  string
	Request      string
	Evidence     string
	Policy       string
	State        string
	Rev          int
	Why          string
	SupersededBy string
	Applied      string
	CreatedAt    time.Time
	AnsweredAt   time.Time
	ClosedAt     time.Time
}

// ID is the row's id, "dr-<n>".
func (d DecisionRow) ID() string { return DecisionID(d.N) }

// Final says the state is one nothing moves out of but applied's
// supersession.
func (d DecisionRow) Final() bool { return finalDecision(d.State) }

func finalDecision(state string) bool {
	switch state {
	case DecisionApplied, DecisionSuperseded, DecisionCancelled, DecisionExpired:
		return true
	}
	return false
}

// ResponseRow is one response to a decision, accepted or refused, with its
// receipt (JSON): K in the order received; Authority (JSON {grant, via,
// subject?, boot}) is what the policy checks, kept apart from Actor, the
// audit's words, and kept in its canonical text, whatever spelling of it
// Respond was given (authorityOf).
type ResponseRow struct {
	Decision    int64
	K           int
	Actor       string
	Authority   string
	ResponseID  string
	Fingerprint string
	Status      string
	Receipt     string
	At          time.Time
}

// HistoryRow is one row of a decision's history: Detail JSON, at most
// MaxHistoryDetail bytes; OpID and Fingerprint an operator operation's
// idempotency key and the SHA-256 of its body, "" for any other row.
type HistoryRow struct {
	Decision    int64
	Seq         int
	At          time.Time
	Kind        string
	By          string
	Detail      string
	OpID        string
	Fingerprint string
}

// HoldRow is a decision's hold on a task of a run: active while ReleasedAt
// is zero; a released hold keeps its row.
type HoldRow struct {
	RunID      string
	TaskID     string
	Decision   int64
	ReleasedAt time.Time
}

// DecisionRecord is a decision with its responses and history, in order.
type DecisionRecord struct {
	DecisionRow
	Responses []ResponseRow
	History   []HistoryRow
}

// Respond's outcomes beside its row: Repeat when the actor sent the same
// response id before (the row is that response's), and Kept false for a
// refusal past MaxDecisionRefusals, which keeps no row and binds nothing.
type Responded struct {
	Row    ResponseRow
	Repeat bool
	Kept   bool
}

// requestShape is a request as its column keeps it (The changes,
// `request`).
type requestShape struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
	Options  []struct {
		Label     string `json:"label"`
		Tradeoffs string `json:"tradeoffs,omitempty"`
	} `json:"options,omitempty"`
	Recommendation *int          `json:"recommendation,omitempty"`
	Uncertainty    string        `json:"uncertainty,omitempty"`
	Authorizes     string        `json:"authorizes,omitempty"`
	Evidence       []string      `json:"evidence,omitempty"`
	Expertise      []string      `json:"expertise,omitempty"`
	Subject        *subjectShape `json:"subject"`
	Supersedes     string        `json:"supersedes,omitempty"`
}

// subjectShape is the exact thing a decision is about: a commit, a
// candidate (a task's submitted result), an artifact (a file's blob at a
// commit) or a plan revision.
type subjectShape struct {
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

type baselineEntry struct {
	Ref  string          `json:"ref"`
	Seen json.RawMessage `json:"seen"`
}

type policyShape struct {
	Revision  string            `json:"revision"`
	Approver  string            `json:"approver"`
	Quorum    int               `json:"quorum"`
	Authority []json.RawMessage `json:"authority"`
}

type authorityShape struct {
	Grant   string `json:"grant,omitempty"`
	Via     string `json:"via"`
	Subject string `json:"subject,omitempty"`
	Boot    string `json:"boot"`
}

// strict decodes raw into v, refusing a field v does not name and anything
// after the value but space: a second value, or a closing ] or } (which
// Decoder.More would pass, since it only asks whether an array or object
// goes on).
func strict(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var after json.RawMessage
	if err := dec.Decode(&after); !errors.Is(err, io.EOF) {
		return errors.New("text after the value")
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidDecision, fmt.Sprintf(format, args...))
}

func tooLarge(what string, n int) error {
	return fmt.Errorf("%w: %s is %d bytes", ErrTooLarge, what, n)
}

// checkRequest refuses a request that is not of its shape or passes a
// bound, for a decision of type typ.
func checkRequest(raw, typ string) error {
	if len(raw) > MaxDecisionRequest {
		return tooLarge("the request", len(raw))
	}
	var r requestShape
	if err := strict(raw, &r); err != nil {
		return invalid("the request: %v", err)
	}
	if n := utf8.RuneCountInString(r.Question); n < 1 || n > MaxDecisionQuestion || strings.ContainsAny(r.Question, "\r\n") {
		return invalid("the question is one line of 1 to %d characters", MaxDecisionQuestion)
	}
	if utf8.RuneCountInString(r.Context) > MaxDecisionContext {
		return invalid("the context is more than %d characters", MaxDecisionContext)
	}
	lo, hi := 0, MaxDecisionOptions
	switch typ {
	case DecisionApproval:
		lo = 2
	case DecisionAssignment:
		hi = 0
	}
	if len(r.Options) < lo || len(r.Options) > hi {
		return invalid("a %s has %d to %d options, not %d", typ, lo, hi, len(r.Options))
	}
	for _, o := range r.Options {
		if n := utf8.RuneCountInString(o.Label); n < 1 || n > MaxOptionLabel {
			return invalid("an option's label is 1 to %d characters", MaxOptionLabel)
		}
		if len(o.Tradeoffs) > MaxOptionTradeoffs {
			return invalid("an option's tradeoffs are more than %d bytes", MaxOptionTradeoffs)
		}
	}
	if rec := r.Recommendation; rec != nil && (*rec < -1 || *rec >= len(r.Options)) {
		return invalid("the recommendation %d names no option", *rec)
	}
	if len(r.Uncertainty) > MaxDecisionUncertainty || len(r.Authorizes) > MaxDecisionAuthorizes {
		return invalid("the uncertainty and what a yes authorizes are at most %d bytes each", MaxDecisionAuthorizes)
	}
	if len(r.Evidence) > MaxDecisionEvidence {
		return invalid("%d evidence refs, more than %d", len(r.Evidence), MaxDecisionEvidence)
	}
	for _, ref := range r.Evidence {
		if ref == "" || len(ref) > MaxEvidenceRef {
			return invalid("an evidence ref is 1 to %d bytes", MaxEvidenceRef)
		}
	}
	if len(r.Expertise) > MaxDecisionExpertise {
		return invalid("%d expertise labels, more than %d", len(r.Expertise), MaxDecisionExpertise)
	}
	for _, e := range r.Expertise {
		if n := utf8.RuneCountInString(e); n < 1 || n > MaxExpertiseLabel {
			return invalid("an expertise label is 1 to %d characters", MaxExpertiseLabel)
		}
	}
	if r.Supersedes != "" {
		if _, ok := ParseDecisionID(r.Supersedes); !ok {
			return invalid("supersedes %q is not a decision's id", r.Supersedes)
		}
	}
	return checkSubject(r.Subject)
}

func checkSubject(s *subjectShape) error {
	if s == nil {
		return invalid("the request names no subject")
	}
	var need map[string]string
	switch s.Kind {
	case "commit":
		need = map[string]string{"commit": s.Commit}
	case "candidate":
		need = map[string]string{"run": s.Run, "task": s.Task, "commit": s.Commit}
		if s.N < 1 {
			return invalid("a candidate subject names its attempt")
		}
	case "artifact":
		need = map[string]string{"path": s.Path, "commit": s.Commit, "sha256": s.SHA256}
	case "plan":
		need = map[string]string{"plan": s.Plan, "digest": s.Digest}
	default:
		return invalid("a subject is a commit, a candidate, an artifact or a plan, not %q", s.Kind)
	}
	for name, v := range need {
		if v == "" {
			return invalid("a %s subject names its %s", s.Kind, name)
		}
	}
	return nil
}

// checkBaseline refuses an evidence baseline that is not one entry per
// ref of the request, in its order, or passes its bounds. An empty one
// is the baseline of a request made before its refs were resolved.
func checkBaseline(raw, request string) error {
	if len(raw) > MaxDecisionBaseline {
		return tooLarge("the evidence baseline", len(raw))
	}
	var entries []json.RawMessage
	if err := strict(raw, &entries); err != nil {
		return invalid("the evidence baseline: %v", err)
	}
	if len(entries) == 0 {
		return nil
	}
	var r requestShape
	_ = strict(request, &r) // checked already
	if len(entries) != len(r.Evidence) {
		return invalid("the evidence baseline has %d entries for %d refs", len(entries), len(r.Evidence))
	}
	for i, e := range entries {
		if len(e) > MaxBaselineEntry {
			return tooLarge("an evidence baseline entry", len(e))
		}
		var b baselineEntry
		if err := strict(string(e), &b); err != nil {
			return invalid("an evidence baseline entry: %v", err)
		}
		if b.Ref != r.Evidence[i] {
			return invalid("the evidence baseline's entry %d is of %q, not %q", i+1, b.Ref, r.Evidence[i])
		}
	}
	return nil
}

func checkPolicy(raw string) error {
	if len(raw) > MaxDecisionPolicy {
		return tooLarge("the policy", len(raw))
	}
	var p policyShape
	if err := strict(raw, &p); err != nil {
		return invalid("the policy: %v", err)
	}
	if p.Revision == "" {
		return invalid("the policy names no revision")
	}
	return nil
}

// authorityOf decodes a response's authority, refusing one not of its
// shape, and answers it with its canonical text, the one form a response
// row keeps. The decoder takes spellings SQL reads otherwise: a key in
// any case ("Via") or escaped ("\u0076ia"), a key given twice (the
// decoder keeps the last, json_extract the first), keys in any order. A
// row kept as typed could be an identity's to the policy and not to
// placeCond's '$.via', and hold no place for its replacement; the
// canonical text is the decoded value encoded again, so both read it
// alike.
func authorityOf(raw string) (authorityShape, string, error) {
	var a authorityShape
	if len(raw) > MaxAuthority {
		return a, "", tooLarge("the authority", len(raw))
	}
	if err := strict(raw, &a); err != nil {
		return a, "", invalid("the authority: %v", err)
	}
	switch a.Via {
	case "workbench", ViaIdentity, "router":
	default:
		return a, "", invalid("an authority is via the workbench, an identity or a router, not %q", a.Via)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(a); err != nil {
		return a, "", invalid("the authority: %v", err)
	}
	canon := strings.TrimSuffix(b.String(), "\n")
	if len(canon) > MaxAuthority {
		return a, "", tooLarge("the authority", len(canon))
	}
	return a, canon, nil
}

// checkObject refuses text that is not "" or one JSON object within max
// bytes.
func checkObject(what, raw string, max int, empty bool) error {
	if raw == "" && empty {
		return nil
	}
	if len(raw) > max {
		return tooLarge(what, len(raw))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return invalid("%s is not a JSON object", what)
	}
	return nil
}

func checkKeys(texts map[string]string, max int) error {
	for name, v := range texts {
		if len(v) > max {
			return tooLarge(name, len(v))
		}
	}
	return nil
}

// openish is the SQL condition of a decision that counts as open for the
// bounds (alias d): open, or answered by an identity's response, whose
// replacement would be open where it is (A replacement always has its
// place).
const openish = `(d.state = 'open' OR ` + placeCond + `)`

// placeCond is the SQL condition of a decision (alias d) that holds a
// place for its replacement: answered, its accepted response an
// identity's.
const placeCond = `(d.state = 'answered' AND EXISTS (SELECT 1 FROM decision_responses r
	WHERE r.decision = d.n AND r.status = 'accepted' AND json_extract(r.authority, '$.via') = 'identity'))`

// count answers a COUNT query.
func (t *Tx) count(query string, args ...any) (int, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, query, args...).Scan(&n)
	return n, err
}

// room says whether run has room for one more decision in all: the
// decisions it opened and the places it holds for replacements together
// stay within MaxDecisionsPerRun.
func (t *Tx) room(run string) (bool, error) {
	opened, err := t.count("SELECT COUNT(*) FROM decisions WHERE run_id = ?", run)
	if err != nil {
		return false, err
	}
	places, err := t.count("SELECT COUNT(*) FROM decisions d WHERE d.current_run = ? AND "+placeCond, run)
	if err != nil {
		return false, err
	}
	return opened+places < MaxDecisionsPerRun, nil
}

// OpenDecision opens a decision in the transaction's run: its row, open at
// rev 1, its n new and never given before. A row whose RequestID the run's
// member sent before for the same task answers that decision's row and
// opens nothing (created false), or ErrDecisionConflict when its
// fingerprint differs; the same id sent for another task is another
// request (decisionsRequestV9). A
// bound passed is ErrDecisionLimit: MaxOpenPerAttempt open of its asking
// attempt, MaxOpenPerRun open in the run, MaxDecisionsPerRun in all with
// the places held for replacements; an answered decision with an
// identity's response counts as open and as a place. The opened history
// row, its routing, its holds and its event are the caller's, in the same
// transaction.
//
// A replacement (a fresh decision in the place of one whose identity
// answer could not be rechecked) has its place only in this order, in one
// transition: the replaced decision made final first (PutDecision,
// superseded, no superseded_by yet), so its place is free; then the
// replacement opened here; then the replaced one put again in the same
// state with superseded_by naming it. Opened while the replaced one is
// still answered, the replacement finds the place taken and is
// ErrDecisionLimit at the bound.
func (t *Tx) OpenDecision(d DecisionRow) (DecisionRow, bool, error) {
	if d.RunID == "" {
		d.RunID = t.runID
	}
	if d.RunID != t.runID {
		return DecisionRow{}, false, fmt.Errorf("ledger: a decision of %s opened in %s's transition", d.RunID, t.runID)
	}
	d.CurrentRun, d.State, d.Rev, d.CreatedAt = d.RunID, DecisionOpen, 1, t.now
	d.Why, d.SupersededBy, d.Applied, d.AnsweredAt, d.ClosedAt = "", "", "", time.Time{}, time.Time{}
	if d.Evidence == "" {
		d.Evidence = "[]"
	}
	if err := checkOpening(d); err != nil {
		return DecisionRow{}, false, err
	}
	if d.RequestID != "" {
		prior, ok, err := t.decision("WHERE run_id = ? AND member = ? AND task_id = ? AND request_id = ?", d.RunID, d.Member, d.TaskID, d.RequestID)
		if err != nil {
			return DecisionRow{}, false, err
		}
		if ok {
			if prior.Fingerprint != d.Fingerprint {
				return DecisionRow{}, false, fmt.Errorf("%w: request %s was %s", ErrDecisionConflict, d.RequestID, prior.ID())
			}
			return prior, false, nil
		}
	}
	if d.Attempt > 0 {
		n, err := t.count("SELECT COUNT(*) FROM decisions d WHERE d.run_id = ? AND d.task_id = ? AND d.attempt = ? AND "+openish, d.RunID, d.TaskID, d.Attempt)
		if err != nil {
			return DecisionRow{}, false, err
		}
		if n >= MaxOpenPerAttempt {
			return DecisionRow{}, false, fmt.Errorf("%w: attempt %d of %s has %d decisions open", ErrDecisionLimit, d.Attempt, d.TaskID, n)
		}
	}
	n, err := t.count("SELECT COUNT(*) FROM decisions d WHERE d.current_run = ? AND "+openish, d.RunID)
	if err != nil {
		return DecisionRow{}, false, err
	}
	if n >= MaxOpenPerRun {
		return DecisionRow{}, false, fmt.Errorf("%w: the run has %d decisions open", ErrDecisionLimit, n)
	}
	if ok, err := t.room(d.RunID); err != nil {
		return DecisionRow{}, false, err
	} else if !ok {
		return DecisionRow{}, false, fmt.Errorf("%w: the run has %d decisions", ErrDecisionLimit, MaxDecisionsPerRun)
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO decisions (run_id, current_run, origin, type, gate, task_id, attempt, session_id, member, ticket, plan, plan_digest,
		request_id, fingerprint, request, evidence, policy, state, rev, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.RunID, d.CurrentRun, d.Origin, d.Type, d.Gate, d.TaskID, d.Attempt, d.SessionID, d.Member, d.Ticket, d.Plan, d.PlanDigest,
		d.RequestID, d.Fingerprint, d.Request, d.Evidence, d.Policy, d.State, d.Rev, stamp(d.CreatedAt))
	if err != nil {
		return DecisionRow{}, false, err
	}
	if d.N, err = res.LastInsertId(); err != nil {
		return DecisionRow{}, false, err
	}
	return d, true, nil
}

// checkOpening refuses a new decision's row that is not of its shape.
func checkOpening(d DecisionRow) error {
	switch d.Origin {
	case OriginAgent, OriginPlan:
	default:
		return invalid("origin %q", d.Origin)
	}
	switch d.Type {
	case DecisionClarification, DecisionApproval, DecisionAssignment:
	default:
		return invalid("type %q", d.Type)
	}
	if d.Fingerprint == "" {
		return invalid("the request has no fingerprint")
	}
	if d.Attempt < 0 {
		return invalid("attempt %d", d.Attempt)
	}
	if err := checkKeys(map[string]string{"the gate": d.Gate, "the task": d.TaskID, "the session": d.SessionID, "the member": d.Member,
		"the ticket": d.Ticket, "the plan": d.Plan, "the plan's digest": d.PlanDigest, "the request id": d.RequestID, "the fingerprint": d.Fingerprint}, MaxDecisionKey); err != nil {
		return err
	}
	if err := checkRequest(d.Request, d.Type); err != nil {
		return err
	}
	if err := checkBaseline(d.Evidence, d.Request); err != nil {
		return err
	}
	return checkPolicy(d.Policy)
}

// decisionCols are the decisions columns a DecisionRow is read from, in
// scanDecision's order.
const decisionCols = `n, run_id, current_run, origin, type, gate, task_id, attempt, session_id, member, ticket, plan, plan_digest,
	request_id, fingerprint, request, evidence, policy, state, rev, why, superseded_by, applied, created_at, answered_at, closed_at`

func scanDecision(sc scanner) (DecisionRow, error) {
	var d DecisionRow
	var created, answered, closed string
	err := sc.Scan(&d.N, &d.RunID, &d.CurrentRun, &d.Origin, &d.Type, &d.Gate, &d.TaskID, &d.Attempt, &d.SessionID, &d.Member, &d.Ticket, &d.Plan, &d.PlanDigest,
		&d.RequestID, &d.Fingerprint, &d.Request, &d.Evidence, &d.Policy, &d.State, &d.Rev, &d.Why, &d.SupersededBy, &d.Applied, &created, &answered, &closed)
	d.CreatedAt, d.AnsweredAt, d.ClosedAt = unstamp(created), unstamp(answered), unstamp(closed)
	return d, err
}

// decision reads one decision's row within the transaction.
func (t *Tx) decision(where string, args ...any) (DecisionRow, bool, error) {
	d, err := scanDecision(t.tx.QueryRowContext(t.ctx, "SELECT "+decisionCols+" FROM decisions "+where, args...))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DecisionRow{}, false, nil
	case err != nil:
		return DecisionRow{}, false, err
	}
	return d, true, nil
}

// Decision reads decision n's row within the transaction.
func (t *Tx) Decision(n int64) (DecisionRow, bool, error) {
	return t.decision("WHERE n = ?", n)
}

func (t *Tx) mustDecision(n int64) (DecisionRow, error) {
	d, ok, err := t.Decision(n)
	if err != nil {
		return DecisionRow{}, err
	}
	if !ok {
		return DecisionRow{}, fmt.Errorf("%w: %s", ErrNoDecision, DecisionID(n))
	}
	return d, nil
}

// PutDecision changes decision n's state, rev, why, superseded_by, applied
// receipt and current run, as d says; its request, evidence and policy
// stay what the opening wrote. A state moves only forward (open →
// answered → applied; open or answered → superseded, cancelled or
// expired; applied → superseded), a rev never goes back, and an applied
// receipt once written never changes, so a second application of one
// decision cannot commit (I1); a decision is applied only with its
// receipt (ErrInvalidDecision without); a final state sets closed_at. open →
// answered is not PutDecision's: a response's answer is Answer's, the
// conditional write.
func (t *Tx) PutDecision(d DecisionRow) error {
	if err := checkObject("the application receipt", d.Applied, MaxDecisionApplied, true); err != nil {
		return err
	}
	if len(d.Why) > MaxDecisionWhy {
		return tooLarge("the decision's words", len(d.Why))
	}
	cur, err := t.mustDecision(d.N)
	if err != nil {
		return err
	}
	if !stateMoves(cur.State, d.State) {
		return fmt.Errorf("%w: %s is %s, not to be %s", ErrDecisionState, cur.ID(), cur.State, d.State)
	}
	if d.Rev < cur.Rev {
		return fmt.Errorf("%w: %s is at rev %d, not %d", ErrDecisionState, cur.ID(), cur.Rev, d.Rev)
	}
	if cur.Applied != "" && d.Applied != cur.Applied {
		return fmt.Errorf("%w: %s was applied already", ErrDecisionState, cur.ID())
	}
	if d.Applied != "" && cur.Applied == "" && (d.State != DecisionApplied || cur.State != DecisionAnswered) {
		return fmt.Errorf("%w: %s is applied only from answered", ErrDecisionState, cur.ID())
	}
	if d.State == DecisionApplied && d.Applied == "" {
		return invalid("%s is applied with no application receipt", cur.ID())
	}
	if d.SupersededBy != "" {
		if _, ok := ParseDecisionID(d.SupersededBy); !ok {
			return invalid("superseded by %q", d.SupersededBy)
		}
	}
	if d.CurrentRun == "" {
		d.CurrentRun = cur.CurrentRun
	}
	closed := cur.ClosedAt
	if finalDecision(d.State) && closed.IsZero() {
		closed = t.now
	}
	_, err = t.tx.ExecContext(t.ctx, `UPDATE decisions SET state = ?, rev = ?, why = ?, superseded_by = ?, applied = ?, current_run = ?, closed_at = ? WHERE n = ?`,
		d.State, d.Rev, d.Why, d.SupersededBy, d.Applied, d.CurrentRun, stamp(closed), d.N)
	return err
}

// stateMoves says whether a decision may go from one state to the other
// (the same state is no move: a comment's rev, say).
func stateMoves(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case DecisionOpen:
		// open → answered is Answer's alone, the conditional write.
		return to == DecisionSuperseded || to == DecisionCancelled || to == DecisionExpired
	case DecisionAnswered:
		return to == DecisionApplied || to == DecisionSuperseded || to == DecisionCancelled
	case DecisionApplied:
		return to == DecisionSuperseded
	}
	return false
}

// Answer is a response's conditional write: decision n becomes answered,
// its rev bumped, only while run holds it (its current_run), it is open,
// its rev is rev and an accepted response of it is kept. It says whether
// it changed the row: false once a continuation moved the decision, or
// another response or change came first.
func (t *Tx) Answer(n int64, run string, rev int) (bool, error) {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE decisions SET state = 'answered', rev = rev + 1, answered_at = ?
		WHERE n = ? AND current_run = ? AND state = 'open' AND rev = ?
		AND EXISTS (SELECT 1 FROM decision_responses r WHERE r.decision = decisions.n AND r.status = 'accepted')`, stamp(t.now), n, run, rev)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	return changed == 1, err
}

// MoveDecisions is a continuation's: every open or answered decision whose
// current run is from is held by to from now on, and the rows are answered
// as moved, their revs unchanged (a response read before the move still
// answers what it read). A final decision stays where it is.
func (t *Tx) MoveDecisions(from, to string) ([]DecisionRow, error) {
	rows, err := t.tx.QueryContext(t.ctx, `UPDATE decisions SET current_run = ? WHERE current_run = ? AND state IN ('open', 'answered') RETURNING `+decisionCols, to, from)
	if err != nil {
		return nil, err
	}
	var out []DecisionRow
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b DecisionRow) int { return cmp.Compare(a.N, b.N) })
	return out, nil
}

// Respond keeps a response to decision n, refused or accepted, with its
// receipt, numbered in the order received. A response id its actor sent
// before answers that response's row (Repeat), whatever it was, or
// ErrDecisionConflict when its fingerprint differs. A decision keeps at
// most MaxDecisionRefusals refused rows: a refusal past them keeps no row
// (Kept false), so it binds nothing, and the one accepted response always
// has its place. A decision accepts one response in its life, while open
// (ErrDecisionState otherwise). An identity's acceptance holds a place
// for the decision that would replace it, so it is refused
// ErrDecisionLimit when the run that holds the decision has no room for
// one; any other authority's is never refused for room. A receipt past
// MaxDecisionReceipt is ErrTooLarge, with no row. Answer, Respond's
// conditional write of the decision, follows an acceptance in the same
// transaction.
func (t *Tx) Respond(n int64, r ResponseRow) (Responded, error) {
	d, err := t.mustDecision(n)
	if err != nil {
		return Responded{}, err
	}
	r.Decision = n
	switch r.Status {
	case ResponseAccepted, ResponseRefused:
	default:
		return Responded{}, invalid("a response is accepted or refused, not %q", r.Status)
	}
	if r.Actor == "" || r.ResponseID == "" || r.Fingerprint == "" {
		return Responded{}, invalid("a response names its actor, its id and its fingerprint")
	}
	if len(r.Actor) > MaxDecisionActor {
		return Responded{}, tooLarge("the actor", len(r.Actor))
	}
	if err := checkKeys(map[string]string{"the response id": r.ResponseID, "the fingerprint": r.Fingerprint}, MaxDecisionKey); err != nil {
		return Responded{}, err
	}
	auth, canon, err := authorityOf(r.Authority)
	if err != nil {
		return Responded{}, err
	}
	r.Authority = canon
	if err := checkObject("the response receipt", r.Receipt, MaxDecisionReceipt, false); err != nil {
		return Responded{}, err
	}
	prior, ok, err := t.response("WHERE decision = ? AND actor = ? AND response_id = ?", n, r.Actor, r.ResponseID)
	if err != nil {
		return Responded{}, err
	}
	if ok {
		if prior.Fingerprint != r.Fingerprint {
			return Responded{}, fmt.Errorf("%w: response %s to %s", ErrDecisionConflict, r.ResponseID, d.ID())
		}
		return Responded{Row: prior, Repeat: true, Kept: true}, nil
	}
	var all, refused, accepted int
	if err := t.tx.QueryRowContext(t.ctx, `SELECT COUNT(*), COALESCE(SUM(status = 'refused'), 0), COALESCE(SUM(status = 'accepted'), 0) FROM decision_responses WHERE decision = ?`, n).
		Scan(&all, &refused, &accepted); err != nil {
		return Responded{}, err
	}
	if r.Status == ResponseRefused {
		if refused >= MaxDecisionRefusals {
			r.K, r.At = 0, t.now
			return Responded{Row: r, Kept: false}, nil
		}
	} else {
		if accepted > 0 || d.State != DecisionOpen {
			return Responded{}, fmt.Errorf("%w: %s is %s and has its answer", ErrDecisionState, d.ID(), d.State)
		}
		if auth.Via == ViaIdentity {
			if ok, err := t.room(d.CurrentRun); err != nil {
				return Responded{}, err
			} else if !ok {
				return Responded{}, fmt.Errorf("%w: %s's run has no room for the decision this answer would need if its recheck failed", ErrDecisionLimit, d.ID())
			}
		}
	}
	if all >= MaxDecisionResponses {
		return Responded{}, fmt.Errorf("%w: %s has %d responses", ErrDecisionLimit, d.ID(), all)
	}
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(k), 0) + 1 FROM decision_responses WHERE decision = ?", n).Scan(&r.K); err != nil {
		return Responded{}, err
	}
	r.At = t.now
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_responses (decision, k, actor, authority, response_id, fingerprint, status, receipt, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, n, r.K, r.Actor, r.Authority, r.ResponseID, r.Fingerprint, r.Status, r.Receipt, stamp(r.At)); err != nil {
		return Responded{}, err
	}
	return Responded{Row: r, Kept: true}, nil
}

const responseCols = `decision, k, actor, authority, response_id, fingerprint, status, receipt, at`

func scanResponse(sc scanner) (ResponseRow, error) {
	var r ResponseRow
	var at string
	err := sc.Scan(&r.Decision, &r.K, &r.Actor, &r.Authority, &r.ResponseID, &r.Fingerprint, &r.Status, &r.Receipt, &at)
	r.At = unstamp(at)
	return r, err
}

// AcceptedActor is the actor of decision n's accepted response, "" for
// none, read within the transaction.
func (t *Tx) AcceptedActor(n int64) (string, error) {
	r, ok, err := t.response("WHERE decision = ? AND status = ? ORDER BY k DESC LIMIT 1", n, ResponseAccepted)
	if err != nil || !ok {
		return "", err
	}
	return r.Actor, nil
}

func (t *Tx) response(where string, args ...any) (ResponseRow, bool, error) {
	r, err := scanResponse(t.tx.QueryRowContext(t.ctx, "SELECT "+responseCols+" FROM decision_responses "+where, args...))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ResponseRow{}, false, nil
	case err != nil:
		return ResponseRow{}, false, err
	}
	return r, true, nil
}

// History writes a row of decision n's history, next in its sequence, and
// answers it. An operation (h.OpID set: an operator's comment, request for
// information or re-route, or a cancel with its command id) its author
// sent before answers that row (repeat), or ErrDecisionConflict when its
// fingerprint differs. Each kind keeps its own bound (historyBounds),
// ErrDecisionLimit past it: one opened, two routed besides the re-routes,
// MaxDecisionOperations operations (a cancel is not one of them: it has
// the final row's own place), one row per response, one applied, one
// supersession or cancellation, one refund, three deliveries. Notified
// rows, and a provider's, take what MaxDecisionHistory leaves them: past
// it a notified row is not written (the row answered has Seq 0) and is
// counted, as dropped, in the detail of the next row written whose detail
// keeps MaxHistoryDetail with the count in it (a row with no room for it
// is written as sent, and the count waits for the next); a provider's row
// is ErrDecisionLimit.
func (t *Tx) History(n int64, h HistoryRow) (HistoryRow, bool, error) {
	if _, err := t.mustDecision(n); err != nil {
		return HistoryRow{}, false, err
	}
	h.Decision = n
	bound, ok := historyBounds[h.Kind]
	if !ok {
		return HistoryRow{}, false, invalid("history kind %q", h.Kind)
	}
	op := h.OpID != ""
	if op && h.Kind != HistoryCommented && h.Kind != HistoryInfoRequested && h.Kind != HistoryRouted && h.Kind != HistoryCancelled {
		return HistoryRow{}, false, invalid("a %s row is no operation", h.Kind)
	}
	if !op && h.Fingerprint != "" {
		return HistoryRow{}, false, invalid("a fingerprint without an operation")
	}
	if len(h.By) > MaxDecisionActor {
		return HistoryRow{}, false, tooLarge("the history's by", len(h.By))
	}
	if err := checkKeys(map[string]string{"the operation id": h.OpID, "the fingerprint": h.Fingerprint}, MaxDecisionKey); err != nil {
		return HistoryRow{}, false, err
	}
	if len(h.Detail) > MaxHistoryDetail {
		return HistoryRow{}, false, tooLarge("the history's detail", len(h.Detail))
	}
	if h.Detail != "" && !json.Valid([]byte(h.Detail)) {
		return HistoryRow{}, false, invalid("the history's detail is not JSON")
	}
	if op {
		prior, ok, err := t.historyRow("WHERE decision = ? AND by = ? AND op_id = ?", n, h.By, h.OpID)
		if err != nil {
			return HistoryRow{}, false, err
		}
		if ok {
			if prior.Fingerprint != h.Fingerprint {
				return HistoryRow{}, false, fmt.Errorf("%w: operation %s on %s", ErrDecisionConflict, h.OpID, DecisionID(n))
			}
			return prior, true, nil
		}
	}
	group, max := bound.group, bound.max
	if op && h.Kind != HistoryCancelled {
		group, max = "ops", MaxDecisionOperations
	}
	var have int
	var err error
	switch group {
	case "ops":
		have, err = t.count(`SELECT COUNT(*) FROM decision_history WHERE decision = ? AND (kind IN ('commented', 'info_requested') OR (kind = 'routed' AND op_id != ''))`, n)
	case "rest":
		max = MaxDecisionHistory - mandatoryHistory
		have, err = t.count(`SELECT COUNT(*) FROM decision_history WHERE decision = ? AND kind IN ('notified', 'assigned', 'delegated', 'escalated', 'expired')`, n)
	default:
		kinds := []any{n}
		marks := []string{}
		for k, b := range historyBounds {
			if b.group == group {
				kinds, marks = append(kinds, k), append(marks, "?")
			}
		}
		q := `SELECT COUNT(*) FROM decision_history WHERE decision = ? AND kind IN (` + strings.Join(marks, ", ") + `)`
		if group == "routed" {
			q += ` AND op_id = ''`
		}
		have, err = t.count(q, kinds...)
	}
	if err != nil {
		return HistoryRow{}, false, err
	}
	if have >= max {
		if h.Kind == HistoryNotified {
			_, err := t.tx.ExecContext(t.ctx, "UPDATE decisions SET dropped = dropped + 1 WHERE n = ?", n)
			return HistoryRow{Decision: n, Kind: h.Kind, At: t.now}, false, err
		}
		return HistoryRow{}, false, fmt.Errorf("%w: %s has %d %s rows", ErrDecisionLimit, DecisionID(n), have, h.Kind)
	}
	var dropped int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT dropped FROM decisions WHERE n = ?", n).Scan(&dropped); err != nil {
		return HistoryRow{}, false, err
	}
	if dropped > 0 {
		merged, err := withDropped(h.Detail, dropped)
		if err != nil {
			return HistoryRow{}, false, err
		}
		// The count goes in only where the row keeps its bound with it.
		if len(merged) <= MaxHistoryDetail {
			h.Detail = merged
			if _, err := t.tx.ExecContext(t.ctx, "UPDATE decisions SET dropped = 0 WHERE n = ?", n); err != nil {
				return HistoryRow{}, false, err
			}
		}
	}
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(seq), 0) + 1 FROM decision_history WHERE decision = ?", n).Scan(&h.Seq); err != nil {
		return HistoryRow{}, false, err
	}
	h.At = t.now
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO decision_history (decision, seq, at, kind, by, detail, op_id, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n, h.Seq, stamp(h.At), h.Kind, h.By, h.Detail, h.OpID, h.Fingerprint)
	return h, false, err
}

// withDropped is a detail with the count of the notified rows not written
// before it: its object's dropped, or an object of that alone.
func withDropped(detail string, dropped int) (string, error) {
	m := map[string]json.RawMessage{}
	if detail != "" && bytes.HasPrefix(bytes.TrimSpace([]byte(detail)), []byte("{")) {
		if err := json.Unmarshal([]byte(detail), &m); err != nil {
			return "", invalid("the history's detail: %v", err)
		}
	} else if detail != "" {
		m["value"] = json.RawMessage(detail)
	}
	m["dropped"] = json.RawMessage(strconv.Itoa(dropped))
	b, err := json.Marshal(m)
	return string(b), err
}

const historyCols = `decision, seq, at, kind, by, detail, op_id, fingerprint`

func scanHistory(sc scanner) (HistoryRow, error) {
	var h HistoryRow
	var at string
	err := sc.Scan(&h.Decision, &h.Seq, &at, &h.Kind, &h.By, &h.Detail, &h.OpID, &h.Fingerprint)
	h.At = unstamp(at)
	return h, err
}

func (t *Tx) historyRow(where string, args ...any) (HistoryRow, bool, error) {
	h, err := scanHistory(t.tx.QueryRowContext(t.ctx, "SELECT "+historyCols+" FROM decision_history "+where, args...))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return HistoryRow{}, false, nil
	case err != nil:
		return HistoryRow{}, false, err
	}
	return h, true, nil
}

// current refuses a run that does not hold decision n (ErrNotCurrent),
// and answers the decision.
func (t *Tx) current(n int64, run string) (DecisionRow, error) {
	d, err := t.mustDecision(n)
	if err != nil {
		return DecisionRow{}, err
	}
	if d.CurrentRun != run {
		return DecisionRow{}, fmt.Errorf("%w: %s is held by %s, not %s", ErrNotCurrent, d.ID(), d.CurrentRun, run)
	}
	return d, nil
}

// Hold puts decision n's hold on a task of run, active. run must hold the
// decision (ErrNotCurrent), which must not be final (ErrDecisionState):
// no final decision holds a task (I10). A released hold of the same task
// is made active again.
func (t *Tx) Hold(run, task string, n int64) error {
	d, err := t.current(n, run)
	if err != nil {
		return err
	}
	if d.Final() {
		return fmt.Errorf("%w: %s is %s", ErrDecisionState, d.ID(), d.State)
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO decision_holds (run_id, task_id, decision, released_at) VALUES (?, ?, ?, '')
		ON CONFLICT(run_id, task_id, decision) DO UPDATE SET released_at = ''`, run, task, n)
	return err
}

// Release releases decision n's active hold on a task of run, keeping its
// row, and says whether there was one. run must hold the decision
// (ErrNotCurrent).
func (t *Tx) Release(run, task string, n int64) (bool, error) {
	if _, err := t.current(n, run); err != nil {
		return false, err
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE decision_holds SET released_at = ? WHERE run_id = ? AND task_id = ? AND decision = ? AND released_at = ''`, stamp(t.now), run, task, n)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	return changed == 1, err
}

// MoveHolds is a continuation's, after MoveDecisions: every hold of from
// of a decision to now holds (one MoveDecisions moved) is written under to
// as it is (active or released), and the active ones of from are released
// at at, so an active hold is under its decision's current run alone
// (I16). The holds of a decision to does not hold (a final one, left
// behind) stay under from alone. An active hold of a decision that to
// does not hold is ErrNotCurrent. It answers how many active holds moved.
func (t *Tx) MoveHolds(from, to string, at time.Time) (int, error) {
	stray, err := t.count(`SELECT COUNT(*) FROM decision_holds h JOIN decisions d ON d.n = h.decision WHERE h.run_id = ? AND h.released_at = '' AND d.current_run != ?`, from, to)
	if err != nil {
		return 0, err
	}
	if stray > 0 {
		return 0, fmt.Errorf("%w: %d active holds of %s are of decisions %s does not hold", ErrNotCurrent, stray, from, to)
	}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_holds (run_id, task_id, decision, released_at)
		SELECT ?, task_id, decision, released_at FROM decision_holds
		WHERE run_id = ? AND decision IN (SELECT n FROM decisions WHERE current_run = ?) ORDER BY task_id, decision
		ON CONFLICT(run_id, task_id, decision) DO UPDATE SET released_at = excluded.released_at`, to, from, to); err != nil {
		return 0, err
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE decision_holds SET released_at = ? WHERE run_id = ? AND released_at = ''`, stamp(at), from)
	if err != nil {
		return 0, err
	}
	moved, err := res.RowsAffected()
	return int(moved), err
}

// Refund records that attempt n of a task of run was given back while it
// waited on decision d, and says whether it wrote: false when that attempt
// was given back already, for this decision or any other (I3).
func (t *Tx) Refund(run, task string, attempt int, d int64) (bool, error) {
	if _, err := t.mustDecision(d); err != nil {
		return false, err
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO decision_refunds (run_id, task_id, n, decision, at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(run_id, task_id, n) DO NOTHING`, run, task, attempt, d, stamp(t.now))
	if err != nil {
		return false, err
	}
	wrote, err := res.RowsAffected()
	return wrote == 1, err
}

// Readers.

// DecisionFilter narrows Decisions: by states (any of them; none for
// every state), the run that holds a decision, its ticket, and a task (the
// asking task, or one it holds or held); Before, a decision's n, pages
// back from it (0 for the newest); Limit at most MaxDecisionsPage (0 for
// that).
type DecisionFilter struct {
	States     []string
	CurrentRun string
	Ticket     string
	Task       string
	Before     int64
	Limit      int
}

// Decision reads decision n with its responses and history, all three of
// one commit (one read transaction), so a transition committed meanwhile
// is seen whole or not at all.
func (l *Ledger) Decision(ctx context.Context, n int64) (DecisionRecord, bool, error) {
	return l.decisionWhere(ctx, n, "")
}

// TaskDecision is Decision for one task: decision n only when run holds
// it and it is task's (asked by it, or held by it, as DecisionFilter's
// Task reads), judged in the read that finds it, so a decision of another
// task's is not found, as one that does not exist is not.
func (l *Ledger) TaskDecision(ctx context.Context, n int64, run, task string) (DecisionRecord, bool, error) {
	if run == "" || task == "" {
		return DecisionRecord{}, false, nil
	}
	return l.decisionWhere(ctx, n, " AND d.current_run = ? AND (d.task_id = ? OR EXISTS (SELECT 1 FROM decision_holds h WHERE h.decision = d.n AND h.task_id = ?))", run, task, task)
}

// decisionWhere reads decision n whole when its row also meets cond.
func (l *Ledger) decisionWhere(ctx context.Context, n int64, cond string, args ...any) (DecisionRecord, bool, error) {
	var rec DecisionRecord
	found := false
	err := l.readSnapshot(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		d, err := scanDecision(q.QueryRowContext(ctx, "SELECT "+decisionCols+" FROM decisions d WHERE d.n = ?"+cond, append([]any{n}, args...)...))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		rec.DecisionRow, found = d, true
		if l.inDecision != nil {
			l.inDecision()
		}
		return readRecord(ctx, q, n, &rec)
	})
	if err != nil || !found {
		return DecisionRecord{}, false, err
	}
	return rec, true, nil
}

// readRecord reads decision n's responses and history into rec, through q.
func readRecord(ctx context.Context, q querier, n int64, rec *DecisionRecord) error {
	rows, err := q.QueryContext(ctx, "SELECT "+responseCols+" FROM decision_responses WHERE decision = ? ORDER BY k", n)
	if err != nil {
		return err
	}
	for rows.Next() {
		r, err := scanResponse(rows)
		if err != nil {
			rows.Close()
			return err
		}
		rec.Responses = append(rec.Responses, r)
	}
	if err := cmp.Or(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, "SELECT "+historyCols+" FROM decision_history WHERE decision = ? ORDER BY seq", n)
	if err != nil {
		return err
	}
	for rows.Next() {
		h, err := scanHistory(rows)
		if err != nil {
			rows.Close()
			return err
		}
		rec.History = append(rec.History, h)
	}
	return cmp.Or(rows.Err(), rows.Close())
}

// Record reads decision n with its responses and history within the
// transaction: what a response's refusal says (who answered first, what
// changed) is read with the state it judges, never before the run's
// transaction began (#635, review round 2).
func (t *Tx) Record(n int64) (DecisionRecord, bool, error) {
	d, ok, err := t.Decision(n)
	if err != nil || !ok {
		return DecisionRecord{}, ok, err
	}
	rec := DecisionRecord{DecisionRow: d}
	if err := readRecord(t.ctx, t.tx, n, &rec); err != nil {
		return DecisionRecord{}, false, err
	}
	return rec, true, nil
}

// DecisionByRequest reads the decision run's member opened for task under
// request id requestID (OpenDecision's repeat), ok false for none, the
// same id another task's included: the request sent again is answered
// with it before anything that may have changed since is checked again (a
// decision it superseded, the file it is about).
func (l *Ledger) DecisionByRequest(ctx context.Context, run, member, task, requestID string) (DecisionRow, bool, error) {
	if requestID == "" {
		return DecisionRow{}, false, nil
	}
	var d DecisionRow
	found := false
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		row, err := scanDecision(q.QueryRowContext(ctx, "SELECT "+decisionCols+" FROM decisions WHERE run_id = ? AND member = ? AND task_id = ? AND request_id = ?", run, member, task, requestID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		d, found = row, true
		return nil
	})
	if err != nil || !found {
		return DecisionRow{}, false, err
	}
	return d, true, nil
}

// Decisions reads the decisions f names, newest first, a page of at most
// MaxDecisionsPage.
func (l *Ledger) Decisions(ctx context.Context, f DecisionFilter) ([]DecisionRow, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxDecisionsPage {
		limit = MaxDecisionsPage
	}
	var where []string
	var args []any
	if len(f.States) > 0 {
		marks := make([]string, len(f.States))
		for i, s := range f.States {
			marks[i] = "?"
			args = append(args, s)
		}
		where = append(where, "d.state IN ("+strings.Join(marks, ", ")+")")
	}
	if f.CurrentRun != "" {
		where, args = append(where, "d.current_run = ?"), append(args, f.CurrentRun)
	}
	if f.Ticket != "" {
		where, args = append(where, "d.ticket = ?"), append(args, f.Ticket)
	}
	if f.Task != "" {
		where, args = append(where, "(d.task_id = ? OR EXISTS (SELECT 1 FROM decision_holds h WHERE h.decision = d.n AND h.task_id = ?))"), append(args, f.Task, f.Task)
	}
	if f.Before > 0 {
		where, args = append(where, "d.n < ?"), append(args, f.Before)
	}
	query := "SELECT " + decisionCols + " FROM decisions d"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY d.n DESC LIMIT ?"
	args = append(args, limit)
	var out []DecisionRow
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			d, err := scanDecision(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, d)
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	return out, err
}

// Holds reads the holds under a run, active and released, by task and
// decision.
func (l *Ledger) Holds(ctx context.Context, run string) ([]HoldRow, error) {
	var out []HoldRow
	err := l.readAt(ctx, func(q querier, version int) error {
		if version < versionDecisions {
			return nil
		}
		rows, err := q.QueryContext(ctx, "SELECT task_id, decision, released_at FROM decision_holds WHERE run_id = ? ORDER BY task_id, decision", run)
		if err != nil {
			return err
		}
		for rows.Next() {
			h := HoldRow{RunID: run}
			var released string
			if err := rows.Scan(&h.TaskID, &h.Decision, &released); err != nil {
				rows.Close()
				return err
			}
			h.ReleasedAt = unstamp(released)
			out = append(out, h)
		}
		return cmp.Or(rows.Err(), rows.Close())
	})
	return out, err
}

// pruneDecisions is Prune's part for the decisions, in its transaction:
// ErrNotPrunable while the run holds or opened a decision that is not
// final (I8); a final decision the run holds and an ancestor opened is
// handed back to the run the pruned one continued, so no decision names a
// run the ledger no longer has. The final decisions the run opened go
// with its row (run_id's cascade), their hold rows with them; its hold
// rows of the decisions handed back go with its tasks. A hand-back is a
// change of the run it hands to, as every other change of a decision is a
// transition of the run that holds it: that run's revision is bumped with
// it and its pointer, read here, is returned for Prune to tell (OnCommit);
// none when nothing was handed back.
func pruneDecisions(ctx context.Context, tx *sql.Tx, runID string) ([]RunPointer, error) {
	var n int64
	var state string
	err := tx.QueryRowContext(ctx, `SELECT n, state FROM decisions WHERE (run_id = ? OR current_run = ?) AND state IN ('open', 'answered') ORDER BY n LIMIT 1`, runID, runID).Scan(&n, &state)
	switch {
	case err == nil:
		return nil, fmt.Errorf("%w: decision %s is %s", ErrNotPrunable, DecisionID(n), state)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var from string
	if err := tx.QueryRowContext(ctx, "SELECT continued_from FROM runs WHERE id = ?", runID).Scan(&from); err != nil {
		return nil, err
	}
	var handed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM decisions WHERE current_run = ? AND run_id != ?", runID, runID).Scan(&handed); err != nil {
		return nil, err
	}
	if handed == 0 {
		return nil, nil
	}
	if from == "" {
		return nil, fmt.Errorf("%w: it holds %d decisions of other runs and continues none", ErrNotPrunable, handed)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE decisions SET current_run = ? WHERE current_run = ? AND run_id != ?", from, runID, runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET revision = revision + 1 WHERE id = ?", from); err != nil {
		return nil, err
	}
	p, err := scanPointer(tx.QueryRowContext(ctx, `SELECT `+pointerCols+` FROM runs WHERE id = ?`, from))
	if err != nil {
		return nil, err
	}
	return []RunPointer{p}, nil
}
