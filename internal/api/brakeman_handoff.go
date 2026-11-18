package api

import (
	"github.com/rocksolidlabs/conductor/internal/brakeman"
)

// Sending a handoff package to a task's next attempt (issue #1012): three
// admin routes, each through requireAdmin and brakemanDeskOf, none taking
// a query (a query is 400 invalid_request, and is never logged):
//
//	GET  /api/brakeman/requests/{id}/items/{i}/hand   the preview: the exact text the server would hand, where it goes, or why it cannot go
//	POST /api/brakeman/requests/{id}/items/{i}/hand   the send: {opId, sha256, by?}
//	POST /api/brakeman/handoffs/{hid}/withdraw        the withdraw: {by?}
//
// The target is the request's own subject as the server resolves it (a
// request of kind handoff about a task or an attempt); nothing of the
// item, the brief or a body names a run, a task or a text. Brakeman's
// turn token reaches none of them (they are admin routes), no grant names
// them, and no tool calls them.

// The routes' bounds.
const (
	// maxHandBody bounds the send's and the withdraw's bodies.
	maxHandBody = 1 << 10
	// maxHandPreview bounds the preview's reply as written: encoded
	// without HTML escaping, a text of MaxHandoffText bytes takes at most
	// twice that (a line break, a tab, a quote and a backslash are the
	// only characters written as two), beside a record of at most
	// MaxHandoffRecord and the target, the refusal and the field names.
	maxHandPreview = 2*brakeman.MaxHandoffText + brakeman.MaxHandoffRecord + 2<<10
)

// The refusals of a preview (answered 200 with the code in refusal) and of
// a send (answered with the status beside each). An unknown code is a
// newer server's: a page draws it as "cannot be sent now".
const (
	refusalNoTask        = "no_task"           // 409: the request is not of kind handoff, or its subject is a session
	refusalRunNotRunning = "run_not_running"   // 409: the newest run of the subject's chain is not held by this server, or is stopped
	refusalRunContinuing = "run_continuing"    // 409: a run of the subject's chain is being continued now
	refusalNotManaged    = "not_managed"       // 409: the run has no tasks, or is a Brakeman turn's own run
	refusalComparison    = "comparison"        // 409: the run compares candidates
	refusalTaskNotFound  = "task_not_found"    // 404: the run's crew as launched has no such task
	refusalTooLarge      = "package_too_large" // 409: the text is past MaxHandoffText (16 KiB)
)

// The send's own refusals, beside the preview's: 400 invalid_request (a
// body that does not decode, an unknown field, an opId or a sha256 of the
// wrong form, a by refused by crew.HandoffByOK), 413 bound (a body past
// maxHandBody), 404 not_found (no request, no brief, no such item),
// 409 not_a_handoff (the item is not handoff/1), 409 op_conflict (the
// opId sent another item), 409 text_changed (the text is not the one
// whose sha256 was sent), 409 handoffs_full (MaxHandoffsWaiting wait: the
// reply's error names them, handFullError), 503 brakeman_not_owner,
// store_unavailable, brakeman_recovering, shutting_down, 403 switchyard,
// and 401 for any credential but the admin's.

// handBody is the send's body. opId is the page's own (Dispatch's op id
// form); sha256 is that of the text the preview showed; by is the name the
// person gives, at most brakeman.MaxBy bytes on one line (crew.HandoffByOK).
type handBody struct {
	OpID   string `json:"opId"`
	SHA256 string `json:"sha256"`
	By     string `json:"by,omitempty"`
}

// withdrawBody is the withdraw's body; it may be absent.
type withdrawBody struct {
	By string `json:"by,omitempty"`
}

// handPreview is the preview's reply: the text exactly as the server would
// hand it (HandoffText of the item: "" when it is past MaxHandoffText,
// whose refusal is then package_too_large), its size in bytes and its
// SHA-256, and exactly one of target and refusal. handoff is the newest
// record made from this item, absent when none was sent.
type handPreview struct {
	Text    string       `json:"text"`
	Bytes   int          `json:"bytes"`
	SHA256  string       `json:"sha256"`
	Target  *handTarget  `json:"target,omitempty"`
	Refusal *handRefusal `json:"refusal,omitempty"`
	Handoff *sentRecord  `json:"handoff,omitempty"`
}

// handTarget is where a send would go: the run (the newest of the
// subject's chain, held by this server), its name, the task, the task's
// state now (crew's task states) and the number of its latest attempt (0
// for none).
type handTarget struct {
	Run     string `json:"run"`
	RunName string `json:"runName"`
	Task    string `json:"task"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
}

// handRefusal is why a preview's package cannot be sent now: one of the
// refusal codes above and the server's words for other callers.
type handRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// sentRecord is a record as the routes answer it: the store's record
// without its text, at most brakeman.MaxHandoffRecord bytes, and, for a
// handed record whose attempt the engine holds and which closed without
// its agent ever starting, notStarted: the attempt took the package and
// never read it, so the person may send it again.
type sentRecord struct {
	brakeman.SentHandoff
	NotStarted bool `json:"notStarted,omitempty"`
}

// handSent is the send's and the withdraw's reply: 201 for a new send,
// 200 for a repeat of the same opId and for every withdraw.
type handSent struct {
	Handoff sentRecord `json:"handoff"`
}

// handFullError is 409 handoffs_full's error: the waiting records, so a
// page can link each one's brief and withdraw it there.
type handFullError struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Waiting []handWaiting `json:"waiting"`
}

// handWaiting is one waiting record as handoffs_full names it.
type handWaiting struct {
	ID      string `json:"id"`
	Request string `json:"request"`
	Run     string `json:"run"`
	Task    string `json:"task"`
}

// handContract holds the contract's names until the routes are built on
// them.
var _ = []any{handBody{}, withdrawBody{}, handPreview{}, handSent{}, handFullError{}, maxHandBody, maxHandPreview,
	refusalNoTask, refusalRunNotRunning, refusalRunContinuing, refusalNotManaged, refusalComparison, refusalTaskNotFound, refusalTooLarge}
