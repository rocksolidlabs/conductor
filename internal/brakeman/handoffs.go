package brakeman

import (
	"errors"
	"time"
)

// Sent handoff packages (issue #1012): a person sends a handoff package
// Brakeman wrote (a handoff/1 item of a kept brief) to the next attempt
// of the one task its request was about. The server keeps what was sent
// in one document of its own, brakeman/handoffs.json, outside requests/,
// which no older build opens; no request, brief, state or ledger record
// changes. Brakeman sends nothing: only the admin routes write here, and
// the engine takes a waiting record for the attempt it is about to start.

// The sent packages' document and bounds.
const (
	HandoffsFile   = "handoffs.json"
	HandoffsSchema = 1
	MaxHandoffsDoc = 2 << 20
	// MaxHandoffText is a package's text at the send, in bytes as
	// HandoffText writes it: a longer one is refused in words, never cut.
	MaxHandoffText = 16 << 10
	// MaxHandoffsWaiting are the records waiting at once: past them a send
	// is refused (ErrHandoffsFull), never the oldest dropped.
	MaxHandoffsWaiting = 20
	// MaxHandoffsKept are the records kept: past them the closed one
	// written longest ago goes, a waiting one never.
	MaxHandoffsKept = 40
	// MaxHandoffRecord is a record as the routes answer it (never with its
	// text), in bytes.
	MaxHandoffRecord = 4 << 10
	// MaxHanded bounds a record's handed list; one send is taken by one
	// attempt, so it holds one entry.
	MaxHanded = 16
)

// A sent package's states.
const (
	HandoffWaiting   = "waiting"   // sent, for the task's next attempt
	HandoffHanded    = "handed"    // taken for an attempt (Handed names it)
	HandoffWithdrawn = "withdrawn" // withdrawn by a person, or its request was pruned
	HandoffReplaced  = "replaced"  // a newer send for the same task replaced it
)

// The store's refusals of a send, a withdraw and a take.
var (
	// ErrHandoffsDamaged is a document that does not read or is of a later
	// schema: it is kept as it is, nothing is answered from it and nothing
	// is handed.
	ErrHandoffsDamaged = errors.New("brakeman: handoffs.json is damaged or of a later schema, kept as it is")
	// ErrHandoffsFull is a send while MaxHandoffsWaiting records wait.
	ErrHandoffsFull = errors.New("brakeman: handoffs_full: as many handoff packages wait as the server keeps")
	// ErrHandoffNotFound is a withdraw of no record.
	ErrHandoffNotFound = errors.New("brakeman: no such handoff package")
	// ErrHandoffTooLarge is a send of a text past MaxHandoffText.
	ErrHandoffTooLarge = errors.New("brakeman: package_too_large: a handoff package is sent at most 16 KiB")
)

// SentHandoff is one record of handoffs.json: which item of which kept
// brief was sent (Request, Item), to which run and task (the request's
// own subject as the server resolved it; nothing of the item names it),
// by whom (Actor from the credential, By the name the person gave, which
// may be empty), when, and the text's size and SHA-256. Text is kept only
// while the record waits; a closed record keeps Bytes and SHA256 alone.
type SentHandoff struct {
	ID      string     `json:"id"`
	OpID    string     `json:"opId"`
	Request string     `json:"request"`
	Item    int        `json:"item"`
	Run     string     `json:"run"`
	Task    string     `json:"task"`
	Actor   string     `json:"actor"`
	By      string     `json:"by,omitempty"`
	SentAt  time.Time  `json:"sentAt"`
	Text    string     `json:"text,omitempty"`
	Bytes   int        `json:"bytes"`
	SHA256  string     `json:"sha256"`
	State   string     `json:"state"`
	Handed  []Handed   `json:"handed,omitempty"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
	EndedBy string     `json:"endedBy,omitempty"`
}

// Handed is the attempt a record was taken for: its run (the sent run or
// a continuation of it), the attempt's number and when it was taken. The
// take is written before the text is answered to the engine, so a record
// is never handed twice; whether that attempt then started its agent is
// the run's to say (its attempt's session), not the record's.
type Handed struct {
	Run     string    `json:"run"`
	Attempt int       `json:"attempt"`
	At      time.Time `json:"at"`
}

// SendHandoff is what a send asks of the store: the request and item, the
// run and task the server resolved from the request's subject, the run's
// chain (the run and every run it continues or that continues it, which
// a waiting record of the same task on any of them is replaced within),
// who sends, and the text the person was shown (HandoffText of the item)
// with its SHA-256.
type SendHandoff struct {
	OpID    string
	Request string
	Item    int
	Run     string
	Chain   []string
	Task    string
	Actor   string
	By      string
	Text    string
	SHA256  string
}

// TakeQuery is the attempt the engine is about to write the task file of:
// its run, that run's chain (newest first, the run itself included), its
// task and its number.
type TakeQuery struct {
	Run     string
	Chain   []string
	Task    string
	Attempt int
}
