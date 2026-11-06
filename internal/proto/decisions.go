package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// A decision link's lane (docs/protocol.md, Decision links;
// docs/plans/decision-inbox.md, PR-5): over the connection a decision
// link opens (a session's viewer, or a run's desk), the holder lists the
// decisions in the link's scope, reads one, unfolds its evidence, reads
// the four steps of what it did, and acts on one. Reads are controls
// (their fields are ids, refs and cursors); an action is a frame of its
// own, TypeDecide, because a line MaxSubmit permits encodes past
// MaxControl. Every read and every decide is answered by exactly one
// reply naming its reqId: a TypeDecision frame, decide_ok or
// decide_refused. decisions_changed carries no content: the viewer
// lists again.

// The lane's control messages.
const (
	// client -> owner
	CtlDecisionsList    = "decisions_list"
	CtlDecisionRead     = "decision_read"
	CtlDecisionTrack    = "decision_track"
	CtlDecisionRecord   = "decision_record"
	CtlDecisionEvidence = "decision_evidence"
	// owner -> client
	CtlDecisionsChanged = "decisions_changed"
	CtlDecideOK         = "decide_ok"
	CtlDecideRefused    = "decide_refused"
)

// The lane's bounds, in bytes but where said.
const (
	// MaxDecision bounds a TypeDecision frame's payload: a page of
	// summaries, one item, four steps, an evidence read or a record's
	// part. Over a data channel it is fragmented by TypeChunk.
	MaxDecision = 128 << 10
	// MaxDecide bounds a TypeDecide frame's payload, encoded: the sum of
	// its fields' encoded worst cases (a text of MaxSubmit control
	// characters is 24 KiB) under a data channel's message cap.
	MaxDecide = 32 << 10
	// MaxDecisionReqID bounds a request's id, MaxDecisionRef an item's or
	// an evidence entry's ref, MaxDecisionCursor a list's after,
	// MaxDecisionReceipt a receipt, MaxDecisionAttempt an action's
	// identity (its decide's opId).
	MaxDecisionReqID   = 32
	MaxDecisionRef     = 512
	MaxDecisionCursor  = 600
	MaxDecisionReceipt = 512
	MaxDecisionAttempt = 32
	// MaxDecisionRecordRev bounds a record's revision (20 digits) and
	// MaxDecisionRecordAfter a record part's page cursor.
	MaxDecisionRecordRev   = 20
	MaxDecisionRecordAfter = 64
	// MaxDecideAction bounds an action's name, MaxDecideRevision the
	// revision it carries (the action's Expect), MaxRationale a DR1
	// response's rationale, encoded; MaxDecideChoice is a DR1 option's
	// highest index.
	MaxDecideAction   = 32
	MaxDecideRevision = 64
	MaxRationale      = 2 << 10
	MaxDecideChoice   = 5
	// MaxDecideWords bounds a decide_refused's message, encoded: the
	// owner cuts its words to fit, so a refusal with its ref and receipt
	// at their bounds fits MaxControl.
	MaxDecideWords = 500
	// DecisionPage is how many summaries a page of the lane holds.
	DecisionPage = 25
	// MaxDecideInFlight and MaxDecisionReadsInFlight bound one
	// connection's decides and reads under way: past them a request is
	// refused too_many_requests.
	MaxDecideInFlight        = 4
	MaxDecisionReadsInFlight = 4
)

// The parts of a DR1 record a decision_record asks for.
const (
	RecordHead      = "head"
	RecordResponses = "responses"
	RecordHistory   = "history"
)

// The codes a decide_refused carries.
const (
	DecideStaleRevision   = "stale_revision"
	DecideNotInScope      = "not_in_scope"
	DecideNotDecisionLink = "not_decision_link"
	DecideNotFound        = "not_found"
	DecideAnswered        = ErrCodeQuestionAnswered
	DecideTooMany         = ErrCodeTooManyRequests
	DecideUnsupported     = "unsupported"
	DecideInvalid         = "invalid"
	DecideRecordMoved     = "record_moved"
	DecideRevoked         = ErrCodeRevoked
	DecideExpired         = ErrCodeExpired
)

// OpIDPattern bounds a decide's opId: the action's identity, which the
// operation keeps its receipt under (a task command's id, a question's
// answer's request id), as a command id is bounded.
var OpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)

// DecisionsList asks for a page of the scope's items after After (the
// cursor the page before gave; empty for the first).
type DecisionsList struct {
	T     string `json:"t"`
	ReqID string `json:"reqId"`
	After string `json:"after,omitempty"`
}

// DecisionRead asks for one item's detail.
type DecisionRead struct {
	T     string `json:"t"`
	ReqID string `json:"reqId"`
	Ref   string `json:"ref"`
}

// DecisionTrack asks for the four steps of one action taken through this
// link: the item Ref, the Receipt its steps are read by, and Attempt, the
// action's own identity (its decide's opId), apart from ReqID, which names
// this request.
type DecisionTrack struct {
	T       string `json:"t"`
	ReqID   string `json:"reqId"`
	Ref     string `json:"ref"`
	Receipt string `json:"receipt"`
	Attempt string `json:"attempt"`
}

// DecisionRecord asks for one part of a DR1 record in scope (head,
// responses or history), a page of the last two after After, Rev the
// revision the head carried (required after head). Answered unsupported
// until DR1's records are items.
type DecisionRecord struct {
	T     string `json:"t"`
	ReqID string `json:"reqId"`
	Ref   string `json:"ref"`
	Part  string `json:"part"`
	Rev   string `json:"rev,omitempty"`
	After string `json:"after,omitempty"`
}

// DecisionEvidence asks for a bounded read of one evidence entry of the
// item Ref, named by the entry's own ref (Evidence), never by its place.
type DecisionEvidence struct {
	T        string `json:"t"`
	ReqID    string `json:"reqId"`
	Ref      string `json:"ref"`
	Evidence string `json:"evidence"`
}

// DecisionsChanged says the scope's items changed (owner -> client): the
// viewer lists again, and tracks again what it acted on.
type DecisionsChanged struct {
	T   string `json:"t"`
	Seq int64  `json:"seq"`
}

// DecideOK says the owner took a decide (owner -> client): Ref and
// Receipt are what its steps are read by (decision_track), Repeat that
// the operation already held the decide's opId and answered its first
// receipt.
type DecideOK struct {
	T       string `json:"t"`
	ReqID   string `json:"reqId"`
	Ref     string `json:"ref"`
	Receipt string `json:"receipt"`
	Repeat  bool   `json:"repeat,omitempty"`
}

// DecideRefused refuses a lane request (owner -> client), a decide or a
// read: Code one of the Decide* codes, Message the words; Rev a record's
// new revision with record_moved, Ref and Receipt a refusal the
// operation kept as a receipt (a command refused stale_revision), whose
// steps are read by them.
type DecideRefused struct {
	T       string `json:"t"`
	ReqID   string `json:"reqId"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Rev     string `json:"rev,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Receipt string `json:"receipt,omitempty"`
}

// Decide is one action on an item (client -> owner, a TypeDecide frame):
// Action one of the item's actions' names, Revision that action's Expect
// and TicketRev its TicketRev; Input a question's choice's keys, exact;
// Choice a DR1 option's index; Text a question's line, or a DR1
// response's or comment's text; Rationale a DR1 response's; OpID the
// action's own identity, which the operation keeps its receipt under, so
// the same OpID sent again is answered with the first receipt. The actor
// is the connection's link, never a field of the message.
type Decide struct {
	ReqID     string `json:"reqId"`
	Ref       string `json:"ref"`
	Action    string `json:"action"`
	Revision  string `json:"revision"`
	TicketRev int64  `json:"ticketRev,omitempty"`
	Input     string `json:"input,omitempty"`
	Choice    *int   `json:"choice,omitempty"`
	Text      string `json:"text,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	OpID      string `json:"opId"`
}

// Decision is a TypeDecision frame's payload (owner -> client): the reply
// to the read ReqID names, one of its fields set. Each field is the
// owner's JSON (docs/protocol.md, Decision links).
type Decision struct {
	ReqID    string          `json:"reqId"`
	List     json.RawMessage `json:"list,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Track    json.RawMessage `json:"track,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
	Record   json.RawMessage `json:"record,omitempty"`
}

// ErrBadDecide refuses a TypeDecide frame that does not decode strictly or
// is past its bounds.
var ErrBadDecide = errors.New("proto: malformed decide")

// ValidDecisionReqID reports whether id can name a lane request: 1 to
// MaxDecisionReqID bytes of printable ASCII.
func ValidDecisionReqID(id string) bool {
	return id != "" && len(id) <= MaxDecisionReqID && printable(id)
}

// validRef reports whether ref can be a lane ref: 1 to MaxDecisionRef
// bytes of UTF-8.
func validRef(ref string) bool {
	return ref != "" && len(ref) <= MaxDecisionRef && utf8.ValidString(ref)
}

// Valid reports whether m is within its bounds.
func (m DecisionsList) Valid() bool {
	return ValidDecisionReqID(m.ReqID) && len(m.After) <= MaxDecisionCursor && utf8.ValidString(m.After)
}

// Valid reports whether m is within its bounds.
func (m DecisionRead) Valid() bool { return ValidDecisionReqID(m.ReqID) && validRef(m.Ref) }

// Valid reports whether m is within its bounds: Receipt 1 to
// MaxDecisionReceipt bytes, Attempt an opId.
func (m DecisionTrack) Valid() bool {
	return ValidDecisionReqID(m.ReqID) && validRef(m.Ref) && m.Receipt != "" && len(m.Receipt) <= MaxDecisionReceipt &&
		utf8.ValidString(m.Receipt) && OpIDPattern.MatchString(m.Attempt)
}

// Valid reports whether m is within its bounds: Part one of the three, Rev
// none or at most MaxDecisionRecordRev digits, and required for a part
// after the head.
func (m DecisionRecord) Valid() bool {
	if !ValidDecisionReqID(m.ReqID) || !validRef(m.Ref) || len(m.After) > MaxDecisionRecordAfter || !printable(m.After) {
		return false
	}
	switch m.Part {
	case RecordHead:
	case RecordResponses, RecordHistory:
		if m.Rev == "" {
			return false
		}
	default:
		return false
	}
	if len(m.Rev) > MaxDecisionRecordRev {
		return false
	}
	for i := 0; i < len(m.Rev); i++ {
		if m.Rev[i] < '0' || m.Rev[i] > '9' {
			return false
		}
	}
	return true
}

// Valid reports whether m is within its bounds.
func (m DecisionEvidence) Valid() bool {
	return ValidDecisionReqID(m.ReqID) && validRef(m.Ref) && validRef(m.Evidence)
}

// Valid reports whether m is within its bounds: its ids, its ref, its
// action's name (printable ASCII), its revision, Input at most
// MaxAnswerInput raw bytes, Choice 0 to MaxDecideChoice, Text at most
// MaxSubmit raw bytes, Rationale at most MaxRationale encoded, TicketRev
// not negative. Which of Input, Choice, Text and Rationale an action takes
// is the owner's to judge, per item.
func (m Decide) Valid() bool {
	switch {
	case !ValidDecisionReqID(m.ReqID), !OpIDPattern.MatchString(m.OpID), !validRef(m.Ref):
		return false
	case m.Action == "" || len(m.Action) > MaxDecideAction || !printable(m.Action):
		return false
	case len(m.Revision) > MaxDecideRevision || !utf8.ValidString(m.Revision):
		return false
	case m.TicketRev < 0:
		return false
	case len(m.Input) > MaxAnswerInput || len(m.Text) > MaxSubmit:
		return false
	case m.Choice != nil && (*m.Choice < 0 || *m.Choice > MaxDecideChoice):
		return false
	}
	return encodedLen(m.Rationale) <= MaxRationale
}

// encodedLen is how many bytes s takes in JSON, its quotes left out.
func encodedLen(s string) int {
	b, _ := json.Marshal(s)
	return len(b) - 2
}

// DecodeDecide reads a TypeDecide frame's payload strictly: one JSON
// object, no field it does not know, nothing after it, within its bounds
// (ErrBadDecide otherwise). The frame's size was checked by Decode.
func DecodeDecide(payload []byte) (Decide, error) {
	var m Decide
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Decide{}, fmt.Errorf("%w: %v", ErrBadDecide, err)
	}
	if dec.More() {
		return Decide{}, fmt.Errorf("%w: more than one object", ErrBadDecide)
	}
	if !m.Valid() {
		return Decide{}, fmt.Errorf("%w: past its bounds", ErrBadDecide)
	}
	return m, nil
}

// EncodeDecide builds a TypeDecide frame for m; an error for one past its
// bounds or whose encoding passes MaxDecide.
func EncodeDecide(m Decide) ([]byte, error) {
	if !m.Valid() {
		return nil, ErrBadDecide
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxDecide {
		return nil, fmt.Errorf("%w: %d bytes encoded", ErrTooLarge, len(b))
	}
	return Encode(TypeDecide, b), nil
}

// EncodeDecision builds a TypeDecision frame for d; ErrTooLarge past
// MaxDecision.
func EncodeDecision(d Decision) ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxDecision {
		return nil, fmt.Errorf("%w: a decision of %d bytes", ErrTooLarge, len(b))
	}
	return Encode(TypeDecision, b), nil
}

// DecodeLane reads a lane control message strictly (DisallowUnknownFields)
// by its discriminator t: a *DecisionsList, *DecisionRead,
// *DecisionTrack, *DecisionRecord or *DecisionEvidence within its bounds;
// ok false for a t that is no lane request, err for one that does not
// decode or is past its bounds.
func DecodeLane(t string, payload []byte) (msg any, reqID string, ok bool, err error) {
	var v interface {
		Valid() bool
	}
	switch t {
	case CtlDecisionsList:
		v = &DecisionsList{}
	case CtlDecisionRead:
		v = &DecisionRead{}
	case CtlDecisionTrack:
		v = &DecisionTrack{}
	case CtlDecisionRecord:
		v = &DecisionRecord{}
	case CtlDecisionEvidence:
		v = &DecisionEvidence{}
	default:
		return nil, "", false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	derr := dec.Decode(v)
	reqID = laneReqID(payload)
	if derr != nil || !v.Valid() {
		return nil, reqID, true, fmt.Errorf("proto: a malformed %s", t)
	}
	return v, reqID, true, nil
}

// laneReqID is the reqId a lane request names, read leniently so a
// refusal can name it; "" when it names none within its bounds.
func laneReqID(payload []byte) string {
	var h struct {
		ReqID string `json:"reqId"`
	}
	if json.Unmarshal(payload, &h) != nil || !ValidDecisionReqID(h.ReqID) {
		return ""
	}
	return h.ReqID
}

// DecideReqID is the reqId a TypeDecide payload names, read leniently, so
// that a refusal of a malformed one can name it.
func DecideReqID(payload []byte) string { return laneReqID(payload) }
