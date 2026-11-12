package proto

import "time"

// The decide grant's wire (docs/protocol.md, Decisions;
// docs/plans/decide-grant.md): the four routes a principal holding the
// decide grant and not the admin grant is admitted to, GET /api/me, GET
// /api/me/decisions, and the read and the respond route of a decision it is
// admitted to. Each reply is one of these types, and nothing else of a
// decision reaches such a principal.

// The decide grant's bounds, in bytes but where said.
const (
	// MaxMeReply bounds GET /api/me's reply: a subject of 48 bytes and a
	// name of 40 characters, each of characters encoding/json escapes to
	// six bytes, are 528 bytes; the three grants and the rest are under 100.
	MaxMeReply = 1 << 10
	// MaxDecideView bounds a decide view: the request's fields at most the
	// ledger's 16 KiB as it keeps them encoded, eight receipts of at most
	// 256 bytes, the rest under 1 KiB.
	MaxDecideView = 32 << 10
	// MaxDecideReply bounds the respond route's reply to a decide
	// principal: its receipt and the decide view.
	MaxDecideReply = MaxDecideView + 1<<10
	// MaxDecideRefusalWords bounds a refusal's message to a decide
	// principal: the server's fixed words of its code.
	MaxDecideRefusalWords = 200
	// MaxDecideList bounds GET /api/me/decisions's reply and
	// MaxDecideListEntries its entries: 200 entries at their widest (an id
	// of dr- and 19 digits, a rev of 19 digits) are 11,626 bytes with the
	// envelope, so the count is always the bound that cuts.
	MaxDecideList        = 16 << 10
	MaxDecideListEntries = 200
	// MaxDecideOwn is how many of its own receipts a decide view carries,
	// the newest.
	MaxDecideOwn = 8
)

// The outcome of a decide principal's own answer, in a decide view.
const (
	OutcomeApplied = "applied"  // the application names its accepted answer
	OutcomeWaiting = "waiting"  // its answer is the accepted one, not yet applied
	OutcomeNotUsed = "not_used" // its answer was accepted, and the decision ended without it
	OutcomeRefused = "refused"  // none of its answers was accepted: outcomeCode is the newest's code
)

// MeReply is GET /api/me's reply: who the credential is, and what it may do
// here. subject and name are an identity provider's principal's.
type MeReply struct {
	Subject string   `json:"subject,omitempty"`
	Name    string   `json:"name,omitempty"`
	Grants  []string `json:"grants"`
}

// DecideOption is one of a decision's options as a decide view gives it.
type DecideOption struct {
	Label     string `json:"label"`
	Tradeoffs string `json:"tradeoffs,omitempty"`
}

// DecideReceipt is one of a decide principal's own responses: never its
// words, its actor or what it sent beside the choice.
type DecideReceipt struct {
	K          int       `json:"k"`
	ResponseID string    `json:"responseId"`
	Status     string    `json:"status"`
	Code       string    `json:"code,omitempty"`
	Rev        int       `json:"rev"`
	At         time.Time `json:"at"`
	Choice     *int      `json:"choice,omitempty"`
}

// DecideView is what a decide principal reads of a decision it is admitted
// to: the question whole while it is open; once answered or final, its id,
// rev, own receipts and the outcome alone.
type DecideView struct {
	ID             string          `json:"id"`
	Rev            int             `json:"rev"`
	State          string          `json:"state,omitempty"`
	Type           string          `json:"type,omitempty"`
	Question       string          `json:"question,omitempty"`
	Context        string          `json:"context,omitempty"`
	Options        []DecideOption  `json:"options,omitempty"`
	Recommendation *int            `json:"recommendation,omitempty"`
	Uncertainty    string          `json:"uncertainty,omitempty"`
	Authorizes     string          `json:"authorizes,omitempty"`
	CreatedAt      *time.Time      `json:"createdAt,omitempty"`
	Own            []DecideReceipt `json:"own"`
	Outcome        string          `json:"outcome,omitempty"`
	OutcomeCode    string          `json:"outcomeCode,omitempty"`
}

// DecideReply is the respond route's reply to a decide principal: its own
// receipt, and the decide view of the decision as it is after the commit
// (none when it no longer admits the principal).
type DecideReply struct {
	Receipt  DecideReceipt `json:"receipt"`
	Decision *DecideView   `json:"decision,omitempty"`
}

// DecideError is a refusal's code and the server's fixed words for it.
type DecideError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DecideRefusal is the respond route's refusal of a decide principal: the
// code, its words, and its own receipt when the refusal was kept.
type DecideRefusal struct {
	Error   DecideError    `json:"error"`
	Receipt *DecideReceipt `json:"receipt,omitempty"`
}

// DecideList is GET /api/me/decisions's reply: the open decisions that
// admit the caller, newest first, and cut when more may await it.
type DecideList struct {
	Decisions []DecideEntry `json:"decisions"`
	Cut       bool          `json:"cut,omitempty"`
}

// DecideEntry is one decision of a DecideList.
type DecideEntry struct {
	ID  string `json:"id"`
	Rev int    `json:"rev"`
}
