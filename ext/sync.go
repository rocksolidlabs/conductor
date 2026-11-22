package ext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Sync's seams (docs/plans/sync-seams.md, Program item CS1): the types a
// sync store (the registered ContinuityStore) builds on, and the doors of
// Host it alone may call. Every door answers ErrUnsupported on a Host
// given to any other provider, on a switchyard and in host mode. With no
// sync store registered nothing here changes what a server does.

// SyncDoors is the part of Host the registered sync store calls: Dispatch's
// rows and shares (OS-1a, OS-1b), the UI documents and a replay on request
// (OS-2), and the decisions handed out and the answers handed in (OS-3).
type SyncDoors interface {
	// DispatchJoin makes tag the store's active replica tag for workspace
	// and device, with the overflow range a create takes numbers from when
	// its project has no block left (dispatch.Store.Join). The rows written
	// before the first join take tag as their origin.
	DispatchJoin(ctx context.Context, tag, workspace, device string, overflowFrom, overflowTo int64) error
	// DispatchRetire drops every allocation of the active tag (its blocks,
	// its overflow range, a share in progress): the provider calls it when
	// the identity the tag was joined under is no longer its own
	// (dispatch.Store.Retire).
	DispatchRetire(ctx context.Context) error
	// DispatchChanges hands at most n (at most MaxDispatchBatch) rows that
	// replicate after the position after, within MaxDispatchBytes, and the
	// position to ask from next (past the rows it skipped). It stops before
	// the first row whose copied-output verdict is pending, so the cursor
	// never passes one.
	DispatchChanges(ctx context.Context, after int64, n int) ([]DispatchChange, int64, error)
	// DispatchPlanView is a revision's view (what of its body leaves the
	// machine) by its view digest; ErrNotFound for none.
	DispatchPlanView(ctx context.Context, viewDigest string) ([]byte, error)
	// DispatchApply applies another replica's rows in one transaction,
	// with the plan views they name (by view digest, each at most
	// MaxPlanView) and each project's key in the workspace's registry (by
	// project id): at most MaxDispatchBatch rows and MaxDispatchBytes, each
	// row's Valid (dispatch.Store.ApplyRemote). A row's Refs are computed
	// again here, never taken from the provider. ErrRefused is a refusal of
	// what was handed in; ErrNotNow is this store unable to take it in the
	// state it is in (it holds as many rows from elsewhere as it may, or has
	// joined no workspace): nothing is written, and the provider hands the
	// rows again later.
	DispatchApply(ctx context.Context, rows []DispatchChange, views map[string][]byte, keys map[string]string) (DispatchApplied, error)
	// DispatchBlock writes a shared project's current block of numbers, or
	// with next the block asked ahead, handed to the active tag.
	DispatchBlock(ctx context.Context, project string, from, to int64, next bool) error
	// DispatchBlocks is each shared project's blocks, at most
	// MaxDispatchProjects.
	DispatchBlocks(ctx context.Context) ([]BlockState, error)
	// DispatchShared is each project with a share, at most
	// MaxDispatchProjects.
	DispatchShared(ctx context.Context) ([]SharedProject, error)
	// DispatchCompleteShare moves a pending share to completing in the
	// workspace, with every row of the project up to upto handed out; an
	// ErrShareMoved when the log holds a row of the project past upto.
	DispatchCompleteShare(ctx context.Context, project, workspace string, upto int64) error
	// DispatchConfirmShare makes a completing share shared.
	DispatchConfirmShare(ctx context.Context, project string) error
	// DispatchRefuseShare takes a share back to none, keeping the words
	// (at most MaxSyncWords bytes) for the project's page.
	DispatchRefuseShare(ctx context.Context, project, words string) error
	// DispatchUnshared marks every project of the workspace no longer
	// shared with this machine, with the words.
	DispatchUnshared(ctx context.Context, workspace, words string) error
	// DispatchDormant makes every shared project of the workspace dormant
	// (its blocks kept, nothing of it handed out) when its shared copy is
	// gone, with the words.
	DispatchDormant(ctx context.Context, workspace, words string) error
	// DispatchNames gives other replicas' device names, tag to name: at
	// most MaxDispatchNames, each name at most MaxDeviceName bytes of one
	// line of words (no address).
	DispatchNames(ctx context.Context, names map[string]string) error

	// SetUIState writes the UI document name (layout or palette, never
	// pins) as another machine left it, keeping its at and by; refused
	// (ErrRefused, "an older document") for an (at, by) not newer than
	// the one held.
	SetUIState(ctx context.Context, name string, data []byte) error
	// SyncReplay asks for a new epoch beginning with a replay of the whole
	// state, as the adapter's own overflow does. Requests coalesce (at most
	// one waiting and one running); it returns once the request is
	// recorded, never waiting for the replay.
	SyncReplay(ctx context.Context) error
	// SyncResend marks one document (its Doc.Store and Doc.Path) pending
	// again as if its source had committed.
	SyncResend(ctx context.Context, store, path string) error

	// RespondDecision hands in an answer held elsewhere for a decision of
	// this server. The server sets r.Check's ContentSHA256 and
	// RequestSHA256 itself and judges it by the record's policy and the
	// identity provider's SubjectChecker.
	RespondDecision(ctx context.Context, r HeldAnswer) (AnswerOutcome, error)
	// SyncDelivery reads what the ledger holds for a held answer's
	// response: its outcome as a repeat would answer, its fence, and
	// whether there is one. It never writes.
	SyncDelivery(ctx context.Context, decision, responseID string) (AnswerOutcome, string, bool, error)
	// LastDecision is the highest decision number the ledger holds (the n
	// of dr-n, 0 for none). It never writes.
	LastDecision(ctx context.Context) (int64, error)

	// PutRemote keeps a record another machine holds (OS-4), read only
	// here: written under remote/<device>/<kind>-<id>.json, never exported
	// and never handed out; r must be Valid.
	PutRemote(ctx context.Context, r Remote) error
	// DeleteRemote removes one such record.
	DeleteRemote(ctx context.Context, kind, device, id string) error
	// ClearRemote removes every such record.
	ClearRemote(ctx context.Context) error
}

// UnsupportedSync answers every door of SyncDoors with ErrUnsupported: a
// Host that is not the sync store's embeds it.
type UnsupportedSync struct{}

var _ SyncDoors = UnsupportedSync{}

func (UnsupportedSync) DispatchJoin(context.Context, string, string, string, int64, int64) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchRetire(context.Context) error { return ErrUnsupported }
func (UnsupportedSync) DispatchChanges(context.Context, int64, int) ([]DispatchChange, int64, error) {
	return nil, 0, ErrUnsupported
}
func (UnsupportedSync) DispatchPlanView(context.Context, string) ([]byte, error) {
	return nil, ErrUnsupported
}
func (UnsupportedSync) DispatchApply(context.Context, []DispatchChange, map[string][]byte, map[string]string) (DispatchApplied, error) {
	return DispatchApplied{}, ErrUnsupported
}
func (UnsupportedSync) DispatchBlock(context.Context, string, int64, int64, bool) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchBlocks(context.Context) ([]BlockState, error) {
	return nil, ErrUnsupported
}
func (UnsupportedSync) DispatchShared(context.Context) ([]SharedProject, error) {
	return nil, ErrUnsupported
}
func (UnsupportedSync) DispatchCompleteShare(context.Context, string, string, int64) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchConfirmShare(context.Context, string) error { return ErrUnsupported }
func (UnsupportedSync) DispatchRefuseShare(context.Context, string, string) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchUnshared(context.Context, string, string) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchDormant(context.Context, string, string) error {
	return ErrUnsupported
}
func (UnsupportedSync) DispatchNames(context.Context, map[string]string) error {
	return ErrUnsupported
}
func (UnsupportedSync) SetUIState(context.Context, string, []byte) error { return ErrUnsupported }
func (UnsupportedSync) SyncReplay(context.Context) error                 { return ErrUnsupported }
func (UnsupportedSync) SyncResend(context.Context, string, string) error { return ErrUnsupported }
func (UnsupportedSync) RespondDecision(context.Context, HeldAnswer) (AnswerOutcome, error) {
	return AnswerOutcome{}, ErrUnsupported
}
func (UnsupportedSync) SyncDelivery(context.Context, string, string) (AnswerOutcome, string, bool, error) {
	return AnswerOutcome{}, "", false, ErrUnsupported
}
func (UnsupportedSync) LastDecision(context.Context) (int64, error) { return 0, ErrUnsupported }
func (UnsupportedSync) PutRemote(context.Context, Remote) error     { return ErrUnsupported }
func (UnsupportedSync) DeleteRemote(context.Context, string, string, string) error {
	return ErrUnsupported
}
func (UnsupportedSync) ClearRemote(context.Context) error { return ErrUnsupported }

// DispatchChange is one row of Dispatch's log as the sync store is handed
// it, or hands back from another replica (OS-1a).
type DispatchChange struct {
	Seq      int64  `json:"seq,omitempty"` // this store's position; never compared across replicas
	Entity   string `json:"entity"`
	EntityID string `json:"entityId"`
	TicketID string `json:"ticketId,omitempty"`
	// ProjectID is the project the row belongs to: every row that
	// replicates names one but a person's (entity person, which names
	// none), and a project's own rows name its own id (EntityID).
	ProjectID       string    `json:"projectId,omitempty"`
	Field           string    `json:"field,omitempty"`
	Op              string    `json:"op"`
	Value           string    `json:"value"`           // at most MaxDispatchValue bytes
	Prior           string    `json:"prior,omitempty"` // the same
	PriorKept       bool      `json:"priorKept,omitempty"`
	At              time.Time `json:"at"`
	Actor           string    `json:"actor"` // the raw actor, at most MaxDispatchActor bytes
	By              string    `json:"by,omitempty"`
	OpID            string    `json:"opId"`
	N               int       `json:"n"`
	Origin          string    `json:"origin"` // a tag
	RecoveredBy     string    `json:"recoveredBy,omitempty"`
	ViewDigest      string    `json:"viewDigest,omitempty"` // a plan's create
	Remote          bool      `json:"remote,omitempty"`     // applied here from another replica
	Refs            []string  `json:"refs,omitempty"`       // the entities the row needs, each "kind:id", at most MaxDispatchRefs
	OutputLike      bool      `json:"outputLike,omitempty"`
	PriorOutputLike bool      `json:"priorOutputLike,omitempty"`
}

// Valid checks every bound of a row: its entity, id, op and identity
// given; its op id of Dispatch's shape; an origin that is a valid tag
// (and a recovery's the same); its raw actor of 1 to MaxDispatchActor
// bytes, never qualified (@); By of at most MaxDispatchBy characters;
// value and prior of at most MaxDispatchValue bytes; Refs of at most
// MaxDispatchRefs, each kind:id; a view digest of 64 lower-case hex or
// none; a time given and before DispatchAtBound; and no control character
// in any identifier.
func (c DispatchChange) Valid() error {
	switch {
	case c.Entity == "" || c.EntityID == "" || c.Op == "" || c.N < 0:
		return fmt.Errorf("%w: a row names its entity, its id, its op and its place", ErrRefused)
	case !dispatchOpID.MatchString(c.OpID):
		return fmt.Errorf("%w: the op id %q", ErrRefused, c.OpID)
	case !ValidTag(c.Origin), c.RecoveredBy != "" && !ValidTag(c.RecoveredBy):
		return fmt.Errorf("%w: a row's origin is a replica's tag", ErrRefused)
	case c.Actor == "" || len(c.Actor) > MaxDispatchActor || c.Actor[0] == '@':
		return fmt.Errorf("%w: the actor %q", ErrRefused, c.Actor)
	case utf8.RuneCountInString(c.By) > MaxDispatchBy:
		return fmt.Errorf("%w: by is more than %d characters", ErrRefused, MaxDispatchBy)
	case len(c.Value) > MaxDispatchValue || len(c.Prior) > MaxDispatchValue:
		return fmt.Errorf("%w: a row's value or prior is more than %d bytes", ErrRefused, MaxDispatchValue)
	case len(c.Refs) > MaxDispatchRefs:
		return fmt.Errorf("%w: more than %d references", ErrRefused, MaxDispatchRefs)
	case c.ViewDigest != "" && !sha256Hex.MatchString(c.ViewDigest):
		return fmt.Errorf("%w: the view digest %q", ErrRefused, c.ViewDigest)
	case c.At.IsZero() || !c.At.Before(DispatchAtBound):
		return fmt.Errorf("%w: a row's time is given and before %s", ErrRefused, DispatchAtBound.Format(time.DateOnly))
	}
	for _, f := range []string{c.Entity, c.EntityID, c.TicketID, c.ProjectID, c.Field, c.Op, c.Actor, c.By} {
		if !utf8.ValidString(f) || strings.ContainsFunc(f, unicode.IsControl) || len(f) > MaxDispatchID && f != c.By {
			return fmt.Errorf("%w: an identifier of a row", ErrRefused)
		}
	}
	for _, ref := range c.Refs {
		if !dispatchRef.MatchString(ref) {
			return fmt.Errorf("%w: the reference %q", ErrRefused, ref)
		}
	}
	return nil
}

// DispatchAtBound bounds a row's time: a row is written before it, so a
// row from a clock set at the end of time never orders past every edit.
var DispatchAtBound = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// The shapes a row's parts take: Dispatch's op id, a reference kind:id,
// and a SHA-256 in hex.
var (
	dispatchOpID = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)
	dispatchRef  = regexp.MustCompile(`^[a-z_]{1,16}:[^\x00-\x1f\x7f]{1,64}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// DispatchApplied is what DispatchApply did: how many rows it applied, the
// held rows' words (at most MaxHeldWords, each at most MaxHeldWord bytes)
// and one line for the page.
type DispatchApplied struct {
	Applied int      `json:"applied"`
	Held    []string `json:"held,omitempty"`
	Words   string   `json:"words,omitempty"`
}

// BlockState is a shared project's current and next block of numbers and
// how much of the current one is left.
type BlockState struct {
	Project  string `json:"project"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Next     int64  `json:"next"`
	NextFrom int64  `json:"nextFrom,omitempty"`
	NextTo   int64  `json:"nextTo,omitempty"`
	Left     int64  `json:"left"`
}

// SharedProject is a project with a share: its state (pending, completing,
// shared, dormant, refused), the words kept for it, the numbers its share
// reserved and its highest number.
type SharedProject struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Words      string `json:"words,omitempty"`
	ReservedTo int64  `json:"reservedTo,omitempty"`
	Highest    int64  `json:"highest"`
}

// ErrShareMoved is DispatchCompleteShare's refusal when the log holds a
// row of the project past the position the share was handed out to:
// Newest is that row's position.
type ErrShareMoved struct {
	Newest int64
}

func (e ErrShareMoved) Error() string {
	return fmt.Sprintf("ext: the project has a row past the share's position, at %d", e.Newest)
}

// DispatchWatcher is an optional interface of the sync store: BlockLow is
// called after a commit that took a shared project past half of its
// current block, from the store's own goroutine, never blocking a write (a
// call that has not returned drops the next until it does).
type DispatchWatcher interface {
	BlockLow(project string)
}

// The bounds of Dispatch's rows and of the doors.
const (
	MaxDispatchValue    = 64 << 10 // a row's value, Dispatch's own bound
	MaxDispatchActor    = 60       // dispatch.MaxActor
	MaxDispatchBy       = 60       // dispatch.MaxByName, in characters
	MaxDispatchID       = 64       // an entity's id, a field's or an op's name, in bytes
	MaxTag              = 8        // a replica's tag: 1 to 8 lower-case letters and digits
	MaxDispatchRefs     = 3        // the most any row needs is two
	MaxDispatchBatch    = 1000     // rows a batch holds, either way
	MaxDispatchBytes    = 4 << 20  // a batch's rows encoded
	MaxPlanView         = 2 << 20  // a revision's view, dispatch.MaxPlanBody
	MaxDispatchProjects = 1000     // the entries of DispatchBlocks and DispatchShared
	MaxDispatchNames    = 1000     // the names DispatchNames takes
	MaxDeviceName       = 64       // a device's name, in bytes, one line
	MaxSyncWords        = 200      // the words a share's refusal or a workspace's state keeps
	MaxHeldWords        = 50       // DispatchApplied.Held's entries
	MaxHeldWord         = 200      // each of them, in bytes
)

// ValidTag reports whether tag is a replica's tag: 1 to MaxTag bytes of
// lower-case letters and digits.
func ValidTag(tag string) bool {
	if tag == "" || len(tag) > MaxTag {
		return false
	}
	for i := 0; i < len(tag); i++ {
		if c := tag[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// DecisionDoc is a decision as it leaves the machine (OS-3, the adapter's
// syncDecision), at most MaxDecisionDoc: built from the record and the
// verdicts its parts are judged by, the same record and the same verdicts
// giving the same bytes. A verdict settled after the record's last change
// (a part judged pending, settled clean or output) has the document
// delivered again at the same rev, its request whole, cut or kept back as
// settled: a delivery at a rev the store already holds REPLACES the
// document it holds, and a store that skipped a save at a rev it holds
// would never show a settled request. It holds what a person needs to
// answer the decision elsewhere and never the session, the request's id,
// a fingerprint, a response's authority beyond its via, the text of a
// response not accepted, the evidence's baseline, the history, the holds
// or the refunds, or any evidence a ref names.
type DecisionDoc struct {
	// The request part (DecisionRequestPart), hashed as RequestSHA256, is
	// the target's identifiers and Request.
	ID         string `json:"id"`       // dr-n
	Run        string `json:"run"`      // its current run: moves with a continuation, outside the request part
	OpenedIn   string `json:"openedIn"` // the run it was opened in, which never changes
	Origin     string `json:"origin"`   // one of DecisionOrigins, the ledger's own words
	Type       string `json:"type"`
	Gate       string `json:"gate,omitempty"`
	Task       string `json:"task,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Member     string `json:"member,omitempty"` // the requesting member's name
	Ticket     string `json:"ticket,omitempty"`
	Plan       string `json:"plan,omitempty"`
	PlanDigest string `json:"planDigest,omitempty"`

	// Request is the request as the document gives it, or nil when it is
	// kept on the machine; RequestKept then says why. Exactly one of the
	// two is set (RequestPart).
	Request       *DecisionDocRequest `json:"request,omitempty"`
	RequestSHA256 string              `json:"requestSHA256"`
	// RequestKept is DecisionRequestKeptOutput when the request's text is
	// kept on the machine (an output-like or pending question, label,
	// recommendation, uncertainty or authorizes, or an agent's request of an
	// earlier schema): the document then carries no request, its request
	// part none either, and nothing can answer the decision away from the
	// machine.
	RequestKept string `json:"requestKept,omitempty"`

	// OpenedTo is whom the decision is opened to, written by the server
	// from its own record: DecisionOpenedToDecide when its request was made
	// for the decide grant (its for names the grant, and it is not a
	// comparison's selection, which is the operator's alone), else
	// DecisionOpenedToOperator. Whoever holds the document for an absent
	// operator reads here who may answer it. It is outside the request
	// part, so the request part and its hash are as they would be without
	// it.
	OpenedTo string `json:"openedTo"`
	// Policy is the record's policy whole, at most MaxDecisionDocPolicy.
	Policy json.RawMessage `json:"policy"`

	State        string     `json:"state"`
	Rev          int        `json:"rev"`
	Why          string     `json:"why,omitempty"`
	SupersededBy string     `json:"supersededBy,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	AnsweredAt   *time.Time `json:"answeredAt,omitempty"`
	ClosedAt     *time.Time `json:"closedAt,omitempty"`

	// Responses is the accepted response and the newest others, at most
	// MaxDecisionDocResponses; ResponsesOmitted how many others were left
	// out.
	Responses        []DecisionDocResponse `json:"responses,omitempty"`
	ResponsesOmitted int                   `json:"responsesOmitted,omitempty"`
	Applied          *DecisionDocApplied   `json:"applied,omitempty"`
	States           DecisionDocStates     `json:"states"`
}

// The origins of a decision, the ledger's own words (internal/crew/ledger
// takes its constants from these): an agent's request, a plan's gate, a
// comparison's selection.
const (
	DecisionOriginAgent   = "agent"
	DecisionOriginPlan    = "plan"
	DecisionOriginCompare = "compare"
)

// DecisionOrigins is every origin a decision may have.
var DecisionOrigins = []string{DecisionOriginAgent, DecisionOriginPlan, DecisionOriginCompare}

// DecisionRequestKeptOutput is DecisionDoc.RequestKept's one value: the
// request's text reads as copied output.
const DecisionRequestKeptOutput = "output"

// The values of DecisionDoc.OpenedTo.
const (
	DecisionOpenedToOperator = "operator" // the operator alone
	DecisionOpenedToDecide   = "decide"   // the operator and the holders of the decide grant
)

// DecisionRequestPart is what a person answers and what a yes acts on:
// DecisionDoc.RequestSHA256 is its SHA256. Request is nil when the
// document keeps its request back, and the part then leaves it out.
type DecisionRequestPart struct {
	Decision   string              `json:"decision"`
	OpenedIn   string              `json:"openedIn"`
	Origin     string              `json:"origin"`
	Type       string              `json:"type"`
	Gate       string              `json:"gate,omitempty"`
	Task       string              `json:"task,omitempty"`
	Attempt    int                 `json:"attempt,omitempty"`
	Ticket     string              `json:"ticket,omitempty"`
	Plan       string              `json:"plan,omitempty"`
	PlanDigest string              `json:"planDigest,omitempty"`
	Request    *DecisionDocRequest `json:"request,omitempty"`
}

// RequestPart is the document's request part, or why the document holds
// none: exactly one of Request and RequestKept is set, and RequestKept is
// DecisionRequestKeptOutput when set.
func (d DecisionDoc) RequestPart() (DecisionRequestPart, error) {
	switch {
	case d.Request != nil && d.RequestKept != "":
		return DecisionRequestPart{}, fmt.Errorf("%w: %s carries its request and says it kept it", ErrRefused, d.ID)
	case d.Request == nil && d.RequestKept == "":
		return DecisionRequestPart{}, fmt.Errorf("%w: %s carries no request and says nothing kept it", ErrRefused, d.ID)
	case d.RequestKept != "" && d.RequestKept != DecisionRequestKeptOutput:
		return DecisionRequestPart{}, fmt.Errorf("%w: %s's request kept for %q", ErrRefused, d.ID, d.RequestKept)
	}
	return DecisionRequestPart{Decision: d.ID, OpenedIn: d.OpenedIn, Origin: d.Origin, Type: d.Type, Gate: d.Gate, Task: d.Task,
		Attempt: d.Attempt, Ticket: d.Ticket, Plan: d.Plan, PlanDigest: d.PlanDigest, Request: d.Request}, nil
}

// SHA256 is the SHA-256, in lower-case hex, of the part's canonical JSON:
// every object's keys sorted, no insignificant space, strings as
// encoding/json writes them with HTML escaping off, numbers as integers.
func (p DecisionRequestPart) SHA256() (string, error) {
	b, err := canonicalJSON(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON is v's JSON with every object's keys sorted, no
// insignificant space and HTML escaping off.
func canonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// DecisionDocRequest is a decision's request as the document gives it,
// at most MaxDecisionDocRequest: past it Context and then the options'
// Tradeoffs are cut at a character's end, Cut "size"; an output-like
// Context, Tradeoffs or words are cut at the run, Cut "output".
type DecisionDocRequest struct {
	Question       string              `json:"question"`
	Context        string              `json:"context,omitempty"`
	Options        []DecisionDocOption `json:"options,omitempty"`
	Recommendation *int                `json:"recommendation,omitempty"`
	Uncertainty    string              `json:"uncertainty,omitempty"`
	Authorizes     string              `json:"authorizes,omitempty"`
	Expertise      []string            `json:"expertise,omitempty"`
	Subject        *DecisionDocSubject `json:"subject,omitempty"`
	Supersedes     string              `json:"supersedes,omitempty"`
	Evidence       []string            `json:"evidence,omitempty"` // the refs alone
	Cut            string              `json:"cut,omitempty"`      // size | output
}

// DecisionDocOption is one of a request's alternatives.
type DecisionDocOption struct {
	Label     string `json:"label"`
	Tradeoffs string `json:"tradeoffs,omitempty"`
}

// DecisionDocSubject is the exact thing a decision is about: its kind and
// its commit, candidate, relative path and SHA-256, or plan digest.
type DecisionDocSubject struct {
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

// DecisionDocResponse is a response as the document gives it: its place,
// who, its status, its id, when, its authority's via alone, its choice and
// its receipt's code and words (cut at 500 bytes); the accepted one also
// with what was answered, its text (at most MaxAnswerText) and rationale
// (at most MaxAnswerRationale).
type DecisionDocResponse struct {
	K          int       `json:"k"`
	Actor      string    `json:"actor"`
	Status     string    `json:"status"`
	ResponseID string    `json:"responseId"`
	At         time.Time `json:"at"`
	Via        string    `json:"via"`
	Choice     *int      `json:"choice,omitempty"` // present only where the request has options, and below their count
	Code       string    `json:"code,omitempty"`
	Words      string    `json:"words,omitempty"`
	Text       string    `json:"text,omitempty"`
	Rationale  string    `json:"rationale,omitempty"`
	Cut        string    `json:"cut,omitempty"` // output, or size (an accepted response cut to the responses' bound)
}

// DecisionDocApplied is Yardmaster's application of the accepted answer.
type DecisionDocApplied struct {
	At       time.Time `json:"at"`
	How      string    `json:"how"`
	Run      string    `json:"run"`
	Task     string    `json:"task,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
	Response int       `json:"response"`
	Words    string    `json:"words,omitempty"`
}

// DecisionDocStates is a decision's four states as the record reports
// them: open; the response recorded; validated by Yardmaster; applied, or
// why not. Each but the first with its time and words, nil while not
// reached.
type DecisionDocStates struct {
	Open      bool              `json:"open"`
	Recorded  *DecisionDocState `json:"recorded,omitempty"`
	Validated *DecisionDocState `json:"validated,omitempty"`
	Applied   *DecisionDocState `json:"applied,omitempty"`
}

// DecisionDocState is one of a decision's states reached: when, and its
// words (for applied: how, or why not).
type DecisionDocState struct {
	At    time.Time `json:"at"`
	Words string    `json:"words,omitempty"`
}

// The bounds of a decision's document, part by part, and the whole their
// sum, so the whole always fits.
const (
	MaxDecisionDocRequest   = 40 << 10
	MaxDecisionDocResponses = 16 << 10
	MaxDecisionDocApplied   = 4 << 10 // applied and states together
	MaxDecisionDocIDs       = 4 << 10 // the ids and enums
	MaxDecisionDocPolicy    = 16 << 10
	MaxDecisionDoc          = MaxDecisionDocRequest + MaxDecisionDocResponses + MaxDecisionDocApplied + MaxDecisionDocIDs + MaxDecisionDocPolicy
)

// AnswerCheck is what a held answer says of who answered, when and what,
// for the identity provider to judge (SubjectChecker).
type AnswerCheck struct {
	Subject       string    `json:"subject"`             // the person, at most MaxSubject
	Membership    string    `json:"membership"`          // the membership the answer was made under: opaque, at most MaxHeldID bytes of [A-Za-z0-9._~:-]
	SignIn        string    `json:"signIn,omitempty"`    // the sign-in it was made through, the same shape
	AppDevice     string    `json:"appDevice,omitempty"` // or the device, the same shape; exactly one of the two
	At            time.Time `json:"at"`
	Statement     string    `json:"statement"`     // the identity provider's signed statement of the answer, at most MaxStatement, opaque here
	Decision      string    `json:"decision"`      // dr-n
	ResponseID    string    `json:"responseId"`    // the response's id
	ExpectedRev   int       `json:"expectedRev"`   // the decision's rev the person saw
	ContentSHA256 string    `json:"contentSha256"` // set by the server, never by the provider
	RequestSHA256 string    `json:"requestSha256"` // the same
}

// Admission is a SubjectChecker's answer.
type Admission int

const (
	AdmitNo      Admission = iota // false by what the provider knows for certain
	AdmitYes                      // verified, and the grant held now
	AdmitUnknown                  // the provider cannot know now
)

// SubjectChecker is an optional interface beside IdentityProvider: it
// answers whether a held answer's statement verifies against its keys and
// claims and whether its subject holds g on this server now under the
// same membership, the sign-in or device not revoked. It is called through
// the guard (IdentityWait); an error or a late answer is AdmitUnknown.
type SubjectChecker interface {
	AdmitsAnswer(ctx context.Context, a AnswerCheck, g Grant) (Admission, error)
}

// HeldAnswer is an answer made elsewhere to a decision of this server,
// handed in by the sync store (RespondDecision).
type HeldAnswer struct {
	Decision    string `json:"decision"`
	ExpectedRev int    `json:"expectedRev"`
	ResponseID  string `json:"responseId"`
	// Choice is the option chosen: present only where the decision's
	// request has options, and then below their count.
	Choice    *int        `json:"choice,omitempty"`
	Text      string      `json:"text,omitempty"`      // at most MaxAnswerText
	Rationale string      `json:"rationale,omitempty"` // at most MaxAnswerRationale
	Check     AnswerCheck `json:"check"`
	HeldAt    time.Time   `json:"heldAt"`
	Held      string      `json:"held"`  // the provider's id of the held answer, the shape of AnswerCheck.Membership
	Fence     string      `json:"fence"` // the provider's token for this hand-out, the same shape
}

// Valid checks a held answer against its bounds and shapes: its decision
// a dr-n; its rev at least 1; its response id DR1's shape; a choice not
// below 0; its text at most MaxAnswerText and its rationale at most
// MaxAnswerRationale bytes, each UTF-8; its check naming the same
// decision, response id and rev, a subject a Principal may have, a
// membership, exactly one of a sign-in and a device, and a held id and a
// fence, each opaque of heldID's shape; a statement of 1 to MaxStatement
// bytes; its times set. ContentSHA256 and RequestSHA256 are the server's
// to set and not looked at here.
func (h HeldAnswer) Valid() error {
	c := h.Check
	switch {
	case !decisionDocID.MatchString(h.Decision) || h.ExpectedRev < 1:
		return fmt.Errorf("%w: a held answer names a decision dr-n and the rev it answers", ErrRefused)
	case !answerResponseID.MatchString(h.ResponseID):
		return fmt.Errorf("%w: a held answer's response id is 1 to 32 of A-Z a-z 0-9 . _ ~ -", ErrRefused)
	case h.Choice != nil && *h.Choice < 0:
		return fmt.Errorf("%w: a held answer's choice is an option's index", ErrRefused)
	case len(h.Text) > MaxAnswerText || !utf8.ValidString(h.Text):
		return fmt.Errorf("%w: a held answer's text is UTF-8 of at most %d bytes", ErrRefused, MaxAnswerText)
	case len(h.Rationale) > MaxAnswerRationale || !utf8.ValidString(h.Rationale):
		return fmt.Errorf("%w: a held answer's rationale is UTF-8 of at most %d bytes", ErrRefused, MaxAnswerRationale)
	case c.Decision != h.Decision || c.ResponseID != h.ResponseID || c.ExpectedRev != h.ExpectedRev:
		return fmt.Errorf("%w: a held answer's check names another decision, response or rev than the answer", ErrRefused)
	case c.Subject == "" || len(c.Subject) > MaxSubject || !printableASCII(c.Subject):
		return fmt.Errorf("%w: a held answer's subject is 1 to %d bytes of printable ASCII", ErrRefused, MaxSubject)
	case !heldID.MatchString(c.Membership) || !heldID.MatchString(h.Held) || !heldID.MatchString(h.Fence):
		return fmt.Errorf("%w: a held answer's membership, held id and fence are each 1 to %d bytes of A-Z a-z 0-9 . _ ~ : -", ErrRefused, MaxHeldID)
	case (c.SignIn == "") == (c.AppDevice == ""),
		c.SignIn != "" && !heldID.MatchString(c.SignIn), c.AppDevice != "" && !heldID.MatchString(c.AppDevice):
		return fmt.Errorf("%w: a held answer names exactly one of a sign-in and a device, each 1 to %d bytes of A-Z a-z 0-9 . _ ~ : -", ErrRefused, MaxHeldID)
	case c.Statement == "" || len(c.Statement) > MaxStatement:
		return fmt.Errorf("%w: a held answer's statement is 1 to %d bytes", ErrRefused, MaxStatement)
	case c.At.IsZero() || h.HeldAt.IsZero():
		return fmt.Errorf("%w: a held answer says when it was made and when it was held", ErrRefused)
	}
	return nil
}

// AnswerContentSHA256 is the SHA-256, in lower-case hex, of an answer's
// content as a held answer's statement signs it and the server checks it:
// the canonical JSON (as DecisionRequestPart.SHA256's) of {choice, text,
// rationale} with the fields the answer has, a key left out when the
// answer has no choice, no text or no rationale. A choice of the first
// option is 0 and is there; no choice is no key.
func AnswerContentSHA256(choice *int, text, rationale string) string {
	b, _ := canonicalJSON(struct {
		Choice    *int   `json:"choice,omitempty"`
		Text      string `json:"text,omitempty"`
		Rationale string `json:"rationale,omitempty"`
	}{choice, text, rationale})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The shapes of a held answer's parts: a decision's id, DR1's response id
// and an opaque id of the provider's.
var (
	decisionDocID    = regexp.MustCompile(`^dr-[1-9][0-9]{0,17}$`)
	answerResponseID = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)
	heldID           = regexp.MustCompile(`^[A-Za-z0-9._~:-]{1,64}$`)
)

// AnswerOutcome is what became of a held answer handed in.
type AnswerOutcome struct {
	Status  string `json:"status"` // AnswerRecorded, AnswerRefused, AnswerNotHeld, AnswerMoved or AnswerUnknownAuthority
	Repeat  bool   `json:"repeat,omitempty"`
	Code    string `json:"code,omitempty"` // for refused: the decisions' codes, and answered_at_server
	Words   string `json:"words,omitempty"`
	Receipt []byte `json:"receipt,omitempty"` // the response's receipt as JSON; the whole at most MaxAnswerOutcome
}

// Valid checks an outcome against its bounds: one of the statuses, a code
// only for a refusal and one for every refusal, words of at most 1 KiB, a
// receipt that is JSON, and the whole at most MaxAnswerOutcome bytes.
func (o AnswerOutcome) Valid() error {
	switch o.Status {
	case AnswerRecorded, AnswerNotHeld, AnswerMoved, AnswerUnknownAuthority:
		if o.Code != "" {
			return fmt.Errorf("%w: only a refusal carries a code", ErrRefused)
		}
	case AnswerRefused:
		if o.Code == "" {
			return fmt.Errorf("%w: a refusal carries its code", ErrRefused)
		}
	default:
		return fmt.Errorf("%w: an outcome's status %q", ErrRefused, o.Status)
	}
	switch {
	case len(o.Code) > 64 || len(o.Words) > 1<<10 || !utf8.ValidString(o.Words):
		return fmt.Errorf("%w: an outcome's code or words past their bound", ErrRefused)
	case len(o.Receipt) > 0 && !json.Valid(o.Receipt):
		return fmt.Errorf("%w: an outcome's receipt is not JSON", ErrRefused)
	}
	if b, err := json.Marshal(o); err != nil || len(b) > MaxAnswerOutcome {
		return fmt.Errorf("%w: an outcome is past %d bytes", ErrRefused, MaxAnswerOutcome)
	}
	return nil
}

// The statuses of an AnswerOutcome.
const (
	AnswerRecorded         = "recorded"
	AnswerRefused          = "refused"
	AnswerNotHeld          = "not_held"
	AnswerMoved            = "moved"
	AnswerUnknownAuthority = "unknown_authority"
)

// The bounds of an answer handed in.
const (
	MaxAnswerRationale = 2 << 10
	MaxStatement       = 2 << 10
	MaxHeldID          = 64
	MaxAnswerOutcome   = 8 << 10
)

// Remote is a record another machine holds, as the sync store reports it
// (OS-4): its kind, the machine (the provider's id of it and its name),
// the record's id there, its revision and time, and its body.
type Remote struct {
	Kind       string    `json:"kind"`       // RemoteDecision, RemotePointer, RemotePins, RemoteDevice or RemoteHosts
	Device     string    `json:"device"`     // the provider's id of the machine that reported it, at most MaxHeldID bytes of [A-Za-z0-9._-]
	DeviceName string    `json:"deviceName"` // at most MaxDeviceName bytes, one line
	ID         string    `json:"id"`         // the record's id on that machine (dr-n, a run's id, "pins", a device's id, "summary"), the shape of Device
	Rev        int64     `json:"rev"`
	At         time.Time `json:"at"`
	Data       []byte    `json:"data"` // the kind's body: a DecisionDoc, a run pointer, a pins document, a device, a host summary
}

// Valid checks a record against its kind's bound and shape: a kind of the
// five; its device and id 1 to MaxHeldID bytes of [A-Za-z0-9._-]; its
// device's name one line of at most MaxDeviceName bytes; its time set;
// its body a JSON object of its kind, within its kind's bound:
//   - decision: a DecisionDoc whose id is the record's, its request part
//     sound (RequestPart), at most MaxDecisionDoc;
//   - pointer: a run pointer whose id is the record's, at most
//     MaxRemoteData;
//   - pins: a pins document, the record's id "pins", at most MaxRemoteData;
//   - device: a live server device, {id, name, kind, platform, version,
//     lastReport, revoked}, its id the record's and its kind never "host",
//     at most MaxRemoteDevice;
//   - hosts: another of the person's servers' host summary, the record's
//     id "summary", its body a HostSummary as ParseHostSummary takes one
//     (every field of its form and bound, none unknown, nothing trimmed),
//     at most MaxRemoteHosts.
//
// Every refusal wraps ErrRefused and the record is not kept.
func (r Remote) Valid() error {
	switch {
	case !remoteID.MatchString(r.Device) || !remoteID.MatchString(r.ID):
		return fmt.Errorf("%w: a remote record's device and id are 1 to %d bytes of A-Z a-z 0-9 . _ -", ErrRefused, MaxHeldID)
	case r.DeviceName == "" || len(r.DeviceName) > MaxDeviceName || !oneLine(r.DeviceName):
		return fmt.Errorf("%w: a remote record's device name is one line of 1 to %d bytes", ErrRefused, MaxDeviceName)
	case r.At.IsZero() || r.Rev < 0:
		return fmt.Errorf("%w: a remote record says its revision and when", ErrRefused)
	}
	bound := map[string]int{RemoteDecision: MaxDecisionDoc, RemotePointer: MaxRemoteData, RemotePins: MaxRemoteData,
		RemoteDevice: MaxRemoteDevice, RemoteHosts: MaxRemoteHosts}[r.Kind]
	switch {
	case bound == 0:
		return fmt.Errorf("%w: a remote record's kind %q", ErrRefused, r.Kind)
	case len(r.Data) > bound:
		return fmt.Errorf("%w: a remote %s is %d bytes, past %d", ErrRefused, r.Kind, len(r.Data), bound)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(r.Data, &obj) != nil || obj == nil {
		return fmt.Errorf("%w: a remote %s is not a JSON object", ErrRefused, r.Kind)
	}
	idOf := func() string {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r.Data, &body)
		return body.ID
	}
	switch r.Kind {
	case RemoteDecision:
		var doc DecisionDoc
		if err := json.Unmarshal(r.Data, &doc); err != nil || doc.ID != r.ID {
			return fmt.Errorf("%w: a remote decision is a decision's document of %s", ErrRefused, r.ID)
		}
		if _, err := doc.RequestPart(); err != nil {
			return err
		}
	case RemotePointer:
		if idOf() != r.ID {
			return fmt.Errorf("%w: a remote pointer is the pointer of run %s", ErrRefused, r.ID)
		}
	case RemotePins, RemoteHosts:
		if want := map[string]string{RemotePins: "pins", RemoteHosts: "summary"}[r.Kind]; r.ID != want {
			return fmt.Errorf("%w: a remote %s's id is %q", ErrRefused, r.Kind, want)
		}
		if r.Kind == RemoteHosts {
			if _, err := ParseHostSummary(r.Data); err != nil {
				return err
			}
		}
	case RemoteDevice:
		var d struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Revoked bool   `json:"revoked"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil || d.ID != r.ID || d.Name == "" || !oneLine(d.Name) || len(d.Name) > MaxDeviceName {
			return fmt.Errorf("%w: a remote device is the device %s with its name", ErrRefused, r.ID)
		}
		if d.Kind == "host" {
			return fmt.Errorf("%w: a host is never a remote device", ErrRefused)
		}
	}
	return nil
}

// remoteID is the shape of a remote record's device and id.
var remoteID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// The kinds of a Remote.
const (
	RemoteDecision = "decision"
	RemotePointer  = "pointer"
	RemotePins     = "pins"
	RemoteDevice   = "device"
	RemoteHosts    = "hosts"
)

// The bounds of what other machines hold.
const (
	MaxRemoteData   = 64 << 10 // a pointer or a pins document; a decision is at most MaxDecisionDoc
	MaxRemoteDevice = 1 << 10
	MaxRemoteHosts  = 16 << 10
	MaxRemoteFiles  = 10_000
)

// SyncStatus is an optional interface beside ContinuityStore: the sync
// store's state, answered from memory through the guard (StatusWait),
// for the Sync row of the app's settings. A store with nothing to report
// (registered, and not in use) answers the zero report, and the server
// shows no report for it, as for a store that offers no SyncStatus.
type SyncStatus interface {
	SyncStatus() SyncReport
}

// SyncReport is the sync store's state: SyncCurrent, SyncBehind,
// SyncUnreachable, SyncLapsed or SyncSignedOut, since when, how many
// changes wait, what was kept back and why, where a remote decision is
// answered, and this server's own device, workspace and person as the
// provider names them (empty while it is not signed in). The zero report
// (no state and no other field, IsZero) says there is nothing to report.
type SyncReport struct {
	State      string         `json:"state"`
	Since      time.Time      `json:"since,omitzero"`
	Waiting    int            `json:"waiting,omitempty"`
	Withheld   []SyncWithheld `json:"withheld,omitempty"`  // at most MaxSyncWithheld
	AnswerURL  string         `json:"answerUrl,omitempty"` // https, at most MaxAnswerURL bytes
	Device     string         `json:"device,omitempty"`    // as Remote.Device names devices
	DeviceName string         `json:"deviceName,omitempty"`
	Workspace  string         `json:"workspace,omitempty"` // opaque, at most MaxHeldID bytes
	Person     string         `json:"person,omitempty"`    // the same
}

// SyncWithheld is a change kept back: what (CND-12's comment, dr-7), at
// most MaxHeldID bytes, and why, at most MaxSyncWords characters of one
// line.
type SyncWithheld struct {
	Ref   string `json:"ref"`
	Words string `json:"words"`
}

// IsZero reports whether r is the zero report: no state and no other
// field (an empty list of changes kept back counts as none), nothing to
// report.
func (r SyncReport) IsZero() bool {
	return r.State == "" && r.Since.IsZero() && r.Waiting == 0 && len(r.Withheld) == 0 && r.AnswerURL == "" && r.Device == "" &&
		r.DeviceName == "" && r.Workspace == "" && r.Person == ""
}

// Valid checks a report against its bounds: the zero report, nothing to
// report, is valid; any other report has a state of the five; waiting
// not below 0; at most MaxSyncWithheld changes kept back, each a ref of 1
// to MaxHeldID bytes of one line and words of at most MaxSyncWords
// characters of one line; an answer page that is an https URL of at most
// MaxAnswerURL bytes, or none; its own device the shape of a remote
// record's, its name one line of at most MaxDeviceName bytes, its
// workspace and person opaque ids of at most MaxHeldID bytes.
func (r SyncReport) Valid() error {
	if r.IsZero() {
		return nil
	}
	switch r.State {
	case SyncCurrent, SyncBehind, SyncUnreachable, SyncLapsed, SyncSignedOut:
	default:
		return fmt.Errorf("%w: a sync report's state %q", ErrRefused, r.State)
	}
	switch {
	case r.Waiting < 0:
		return fmt.Errorf("%w: a sync report's waiting changes", ErrRefused)
	case len(r.Withheld) > MaxSyncWithheld:
		return fmt.Errorf("%w: a sync report keeps back %d changes, past %d", ErrRefused, len(r.Withheld), MaxSyncWithheld)
	case r.Device != "" && !remoteID.MatchString(r.Device), len(r.DeviceName) > MaxDeviceName, r.DeviceName != "" && !oneLine(r.DeviceName):
		return fmt.Errorf("%w: a sync report's own device", ErrRefused)
	case r.Workspace != "" && !heldID.MatchString(r.Workspace), r.Person != "" && !heldID.MatchString(r.Person):
		return fmt.Errorf("%w: a sync report's workspace and person are opaque ids of at most %d bytes", ErrRefused, MaxHeldID)
	}
	if r.AnswerURL != "" {
		u, err := url.Parse(r.AnswerURL)
		if err != nil || len(r.AnswerURL) > MaxAnswerURL || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%w: a sync report's answer page is an https URL of at most %d bytes", ErrRefused, MaxAnswerURL)
		}
	}
	for _, w := range r.Withheld {
		if w.Ref == "" || len(w.Ref) > MaxHeldID || !oneLine(w.Ref) || !oneLine(w.Words) || utf8.RuneCountInString(w.Words) > MaxSyncWords {
			return fmt.Errorf("%w: a change kept back names what in %d bytes and why in %d characters, each one line", ErrRefused, MaxHeldID, MaxSyncWords)
		}
	}
	return nil
}

// The states of a SyncReport; the zero report has none (IsZero).
const (
	SyncCurrent     = "current"
	SyncBehind      = "behind"
	SyncUnreachable = "unreachable"
	SyncLapsed      = "lapsed"
	SyncSignedOut   = "signed_out"
)

const (
	MaxSyncWithheld = 50
	MaxAnswerURL    = 2 << 10
	// StatusWait bounds a SyncStatus call; past it the server answers as
	// if no provider were registered.
	StatusWait = 100 * time.Millisecond
)
