package ext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
	// again here, never taken from the provider.
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

// errNotBuilt is the answer of a check whose row has not been built yet:
// it refuses everything, so nothing passes it before its rules do.
var errNotBuilt = errors.New("ext: this check is not built yet")

// DispatchChange is one row of Dispatch's log as the sync store is handed
// it, or hands back from another replica (OS-1a).
type DispatchChange struct {
	Seq             int64     `json:"seq,omitempty"` // this store's position; never compared across replicas
	Entity          string    `json:"entity"`
	EntityID        string    `json:"entityId"`
	TicketID        string    `json:"ticketId,omitempty"`
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

// Valid checks every bound of a row, its op id's shape, an origin that is
// a valid tag, By's length, Refs' bound and shape, and no control
// character where Dispatch's fields refuse one. Until OS-1a it refuses
// every row.
func (c DispatchChange) Valid() error { return errNotBuilt }

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
// syncDecision): deterministic, the same record at the same rev giving the
// same bytes, at most MaxDecisionDoc. It holds what a person needs to
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
	Origin     string `json:"origin"`   // agent | gate | compare
	Type       string `json:"type"`
	Gate       string `json:"gate,omitempty"`
	Task       string `json:"task,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Member     string `json:"member,omitempty"` // the requesting member's name
	Ticket     string `json:"ticket,omitempty"`
	Plan       string `json:"plan,omitempty"`
	PlanDigest string `json:"planDigest,omitempty"`

	Request       DecisionDocRequest `json:"request"`
	RequestSHA256 string             `json:"requestSha256"`
	// RequestKept is "output" when the request's text is kept on the
	// machine (an output-like or pending question, label, recommendation,
	// uncertainty or authorizes, or an agent's request of an earlier
	// schema): Request then holds nothing of it, and nothing can answer
	// the decision away from the machine.
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

// The values of DecisionDoc.OpenedTo.
const (
	DecisionOpenedToOperator = "operator" // the operator alone
	DecisionOpenedToDecide   = "decide"   // the operator and the holders of the decide grant
)

// DecisionRequestPart is what a person answers and what a yes acts on:
// RequestSHA256 is the SHA-256 of its canonical JSON (keys sorted, no
// insignificant space).
type DecisionRequestPart struct {
	Decision   string             `json:"decision"`
	OpenedIn   string             `json:"openedIn"`
	Origin     string             `json:"origin"`
	Type       string             `json:"type"`
	Gate       string             `json:"gate,omitempty"`
	Task       string             `json:"task,omitempty"`
	Attempt    int                `json:"attempt,omitempty"`
	Ticket     string             `json:"ticket,omitempty"`
	Plan       string             `json:"plan,omitempty"`
	PlanDigest string             `json:"planDigest,omitempty"`
	Request    DecisionDocRequest `json:"request"`
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
	Choice     *int      `json:"choice,omitempty"`
	Code       string    `json:"code,omitempty"`
	Words      string    `json:"words,omitempty"`
	Text       string    `json:"text,omitempty"`
	Rationale  string    `json:"rationale,omitempty"`
	Cut        string    `json:"cut,omitempty"` // output
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
	Decision    string      `json:"decision"`
	ExpectedRev int         `json:"expectedRev"`
	ResponseID  string      `json:"responseId"`
	Choice      *int        `json:"choice,omitempty"`
	Text        string      `json:"text,omitempty"`      // at most MaxAnswerText
	Rationale   string      `json:"rationale,omitempty"` // at most MaxAnswerRationale
	Check       AnswerCheck `json:"check"`
	HeldAt      time.Time   `json:"heldAt"`
	Held        string      `json:"held"`  // the provider's id of the held answer, the shape of AnswerCheck.Membership
	Fence       string      `json:"fence"` // the provider's token for this hand-out, the same shape
}

// Valid checks a held answer against its bounds and shapes. Until OS-3 it
// refuses every one.
func (h HeldAnswer) Valid() error { return errNotBuilt }

// AnswerOutcome is what became of a held answer handed in.
type AnswerOutcome struct {
	Status  string `json:"status"` // AnswerRecorded, AnswerRefused, AnswerNotHeld, AnswerMoved or AnswerUnknownAuthority
	Repeat  bool   `json:"repeat,omitempty"`
	Code    string `json:"code,omitempty"` // for refused: the decisions' codes, and answered_at_server
	Words   string `json:"words,omitempty"`
	Receipt []byte `json:"receipt,omitempty"` // the response's receipt as JSON; the whole at most MaxAnswerOutcome
}

// Valid checks an outcome against its bounds. Until OS-3 it refuses every
// one.
func (o AnswerOutcome) Valid() error { return errNotBuilt }

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
