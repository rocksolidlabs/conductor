package brakeman

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/quoted"
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

// ErrHandoffText is a record whose text does not match its size and
// SHA-256, or that is not one a task file writes: it is handed to no one.
var ErrHandoffText = errors.New("brakeman: a handoff package's record does not hold the text it was sent with")

// FullError is ErrHandoffsFull with the records that wait, so the person
// can find each one's brief and withdraw it there.
type FullError struct{ Waiting []SentHandoff }

func (e *FullError) Error() string { return ErrHandoffsFull.Error() }
func (e *FullError) Unwrap() error { return ErrHandoffsFull }

// handoffIDPattern is a record's id, ho-<n>.
var handoffIDPattern = regexp.MustCompile(`^ho-[1-9][0-9]{0,17}$`)

// ValidHandoffID reports whether id is a record's id, ho-<n>.
func ValidHandoffID(id string) bool { return handoffIDPattern.MatchString(id) }

// HandoffByOK reports whether by is a name a send takes: empty, or at
// most MaxBy bytes on one plain line (quoted.Plain), which the task file
// writes as it is (crew.HandoffByOK is the same rule).
func HandoffByOK(by string) bool { return len(by) <= MaxBy && quoted.Plain(by) }

// HandoffText is a handoff package's text, as a person reads it before
// sending and as the task file of the attempt it is handed to holds it:
// "## Handoff package: <objective>", then its constraints, the decisions
// made, the revision, the open questions and the evidence, each under its
// own heading only when it holds something, list items as "- ", refs as
// their own text. It is cleaned as the task file's quote cleans it
// (quoted.Lines: each line end a reader may take as one written "\n",
// each character that draws nothing and each byte that is not UTF-8
// written U+FFFD, each line's trailing spaces left out), and ends with one
// line break, so the record, its SHA-256, the preview, task.json and the
// quote hold the same characters.
func HandoffText(h Handoff) string {
	var b strings.Builder
	b.WriteString("## Handoff package: " + quoted.Flat(h.Objective) + "\n")
	part := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			b.WriteString("\n### " + title + "\n\n" + body + "\n")
		}
	}
	list := func(title string, items []string) {
		var out []string
		for _, it := range items {
			if l := strings.TrimSpace(quoted.Flat(it)); l != "" {
				out = append(out, "- "+l)
			}
		}
		part(title, strings.Join(out, "\n"))
	}
	part("Constraints", h.Constraints)
	list("Decisions made", h.Decisions)
	if h.Revision != nil {
		part("Revision", h.Revision.String())
	}
	list("Open questions", h.Questions)
	refs := make([]string, 0, len(h.Evidence))
	for _, r := range h.Evidence {
		refs = append(refs, r.String())
	}
	list("Evidence", refs)
	return cleanHandoffText(b.String())
}

// cleanHandoffText is text as HandoffText answers it: cleaned as
// quoted.Lines cleans it, each line's trailing spaces left out, one line
// break at the end. It changes nothing of a text it answered.
func cleanHandoffText(text string) string {
	lines := quoted.Lines(text)
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n") + "\n"
}

// TextSum is the SHA-256 of a package's text, in hex.
func TextSum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// handoffsDoc is brakeman/handoffs.json.
type handoffsDoc struct {
	Schema   int           `json:"schema"`
	Next     int64         `json:"next"`
	Handoffs []SentHandoff `json:"handoffs"`
}

// handoffSeam, when a test sets it, runs inside the store's lock as a
// send ("send"), a withdraw ("withdraw") or a take ("take") begins, before
// the document is read: a test holds one there to order it against
// another.
var handoffSeam func(op string)

// Send keeps a send and answers its record (without its text), created
// false for a repeat of an op id that sent the same request's item, which
// answers the record it made. The op id of another item is ErrOpConflict.
// A waiting record of the same task on a run of in.Chain becomes replaced.
// Past MaxHandoffsWaiting waiting records the send is a *FullError, never
// the oldest dropped. Only the store that owns the records writes
// (ErrNotOwner); a damaged document is ErrHandoffsDamaged, kept as it is.
func (s *Store) Send(in SendHandoff) (SentHandoff, bool, error) {
	if err := checkSend(in); err != nil {
		return SentHandoff{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return SentHandoff{}, false, err
	}
	if f := handoffSeam; f != nil {
		f("send")
	}
	doc, err := s.loadHandoffs()
	if err != nil {
		return SentHandoff{}, false, err
	}
	if i := slices.IndexFunc(doc.Handoffs, func(h SentHandoff) bool { return h.OpID == in.OpID }); i >= 0 {
		h := doc.Handoffs[i]
		if h.Request != in.Request || h.Item != in.Item {
			return SentHandoff{}, false, fmt.Errorf("%w: %s sent %s's item %d", ErrOpConflict, in.OpID, h.Request, h.Item)
		}
		return answered(h), false, nil
	}
	now := s.now().UTC()
	var replace []int
	waiting := []SentHandoff{}
	for i, h := range doc.Handoffs {
		if h.State != HandoffWaiting {
			continue
		}
		if h.Task == in.Task && slices.Contains(in.Chain, h.Run) {
			replace = append(replace, i)
			continue
		}
		waiting = append(waiting, answered(h))
	}
	if len(waiting) >= MaxHandoffsWaiting {
		return SentHandoff{}, false, &FullError{Waiting: waiting}
	}
	for _, i := range replace {
		old := &doc.Handoffs[i]
		old.State, old.Text, old.EndedAt = HandoffReplaced, "", &now
	}
	doc.Next = max(doc.Next, 1)
	h := SentHandoff{ID: "ho-" + strconv.FormatInt(doc.Next, 10), OpID: in.OpID, Request: in.Request, Item: in.Item, Run: in.Run, Task: in.Task,
		Actor: in.Actor, By: in.By, SentAt: now, Text: in.Text, Bytes: len(in.Text), SHA256: TextSum(in.Text), State: HandoffWaiting}
	doc.Next++
	doc.Handoffs = append(doc.Handoffs, h)
	pruneHandoffs(&doc)
	if err := s.write(s.st, HandoffsFile, doc, MaxHandoffsDoc); err != nil {
		return SentHandoff{}, false, err
	}
	return answered(h), true, nil
}

// checkSend refuses a send the routes would never make: every id of its
// shape, the chain holding the run, the actor and the name within their
// bounds, the text not empty, within MaxHandoffText (ErrHandoffTooLarge
// past it) and as HandoffText answers it.
func checkSend(in SendHandoff) error {
	switch {
	case !opIDPattern.MatchString(in.OpID):
		return invalidRequest("opId", "1 to 32 of A-Z, a-z, 0-9, . _ ~ -")
	case !requestIDPattern.MatchString(in.Request) || in.Item < 0 || in.Item >= MaxItems:
		return invalidRequest("item", "a request's id and an item's index")
	case !runIDPattern.MatchString(in.Run) || !taskIDPattern.MatchString(in.Task) || !slices.Contains(in.Chain, in.Run):
		return invalidRequest("target", "a run of its chain and a task")
	case in.Actor == "" || len(in.Actor) > MaxOpenedActor:
		return invalidRequest("actor", "1 to %d bytes", MaxOpenedActor)
	case !HandoffByOK(in.By):
		return invalidRequest("by", "one plain line of at most %d bytes", MaxBy)
	case len(in.Text) > MaxHandoffText:
		return fmt.Errorf("%w: %d bytes", ErrHandoffTooLarge, len(in.Text))
	case in.Text == "" || cleanHandoffText(in.Text) != in.Text:
		return invalidRequest("text", "a handoff package's text as the server writes it")
	}
	for _, r := range in.Chain {
		if !runIDPattern.MatchString(r) {
			return invalidRequest("target", "a run of its chain and a task")
		}
	}
	return nil
}

// Withdraw withdraws the record id on a person's word (by, the name they
// give) and answers it, withdrawn true when this call withdrew it; one
// that is not waiting is answered as it stands, so a withdraw that lost a
// race with a take answers handed. No record is ErrHandoffNotFound.
func (s *Store) Withdraw(id, by string) (h SentHandoff, withdrawn bool, err error) {
	if !handoffIDPattern.MatchString(id) {
		return SentHandoff{}, false, ErrHandoffNotFound
	}
	if !HandoffByOK(by) {
		return SentHandoff{}, false, invalidRequest("by", "one plain line of at most %d bytes", MaxBy)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return SentHandoff{}, false, err
	}
	if f := handoffSeam; f != nil {
		f("withdraw")
	}
	doc, err := s.loadHandoffs()
	if err != nil {
		return SentHandoff{}, false, err
	}
	i := slices.IndexFunc(doc.Handoffs, func(h SentHandoff) bool { return h.ID == id })
	if i < 0 {
		return SentHandoff{}, false, ErrHandoffNotFound
	}
	r := &doc.Handoffs[i]
	if r.State != HandoffWaiting {
		return answered(*r), false, nil
	}
	now := s.now().UTC()
	r.State, r.Text, r.EndedAt, r.EndedBy = HandoffWithdrawn, "", &now, by
	if err := s.write(s.st, HandoffsFile, doc, MaxHandoffsDoc); err != nil {
		return SentHandoff{}, false, err
	}
	return answered(*r), true, nil
}

// HandoffByOp is the record a send under opID made, without its text;
// found false for none kept.
func (s *Store) HandoffByOp(opID string) (SentHandoff, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadHandoffs()
	if err != nil {
		return SentHandoff{}, false, err
	}
	if i := slices.IndexFunc(doc.Handoffs, func(h SentHandoff) bool { return h.OpID == opID }); i >= 0 {
		return answered(doc.Handoffs[i]), true, nil
	}
	return SentHandoff{}, false, nil
}

// Take takes the package waiting for the attempt q names, for the engine
// about to write its task file: the newest waiting record of q's task on
// a run of q's chain whose text matches its size and SHA-256 and which
// writes (it may refuse a record the task file would not write). The take
// is written first, the record handed to {q.Run, q.Attempt}, and only then
// is the record and its text answered; a write that fails answers nothing
// and leaves the record waiting. A record handed is taken by no later
// attempt; a withdrawn or replaced one never. found false is no package.
func (s *Store) Take(q TakeQuery, writes func(SentHandoff) bool) (h SentHandoff, text string, found bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return SentHandoff{}, "", false, err
	}
	if f := handoffSeam; f != nil {
		f("take")
	}
	doc, err := s.loadHandoffs()
	if err != nil {
		return SentHandoff{}, "", false, err
	}
	pick := -1
	for i, r := range doc.Handoffs {
		if r.State == HandoffWaiting && r.Task == q.Task && slices.Contains(q.Chain, r.Run) && (pick < 0 || handoffNumber(r.ID) > handoffNumber(doc.Handoffs[pick].ID)) {
			pick = i
		}
	}
	if pick < 0 {
		return SentHandoff{}, "", false, nil
	}
	r := &doc.Handoffs[pick]
	if r.Text == "" || len(r.Text) != r.Bytes || len(r.Text) > MaxHandoffText || TextSum(r.Text) != r.SHA256 || cleanHandoffText(r.Text) != r.Text || (writes != nil && !writes(*r)) {
		return SentHandoff{}, "", false, fmt.Errorf("%w: %s", ErrHandoffText, r.ID)
	}
	text = r.Text
	now := s.now().UTC()
	r.State, r.Text, r.EndedAt = HandoffHanded, "", &now
	r.Handed = append(r.Handed, Handed{Run: q.Run, Attempt: q.Attempt, At: now})
	if len(r.Handed) > MaxHanded {
		r.Handed = r.Handed[len(r.Handed)-MaxHanded:]
	}
	if err := s.write(s.st, HandoffsFile, doc, MaxHandoffsDoc); err != nil {
		return SentHandoff{}, "", false, err
	}
	return answered(*r), text, true, nil
}

// HandoffOf is the newest record made from request's item, without its
// text; found false for none.
func (s *Store) HandoffOf(request string, item int) (SentHandoff, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadHandoffs()
	if err != nil {
		return SentHandoff{}, false, err
	}
	pick := -1
	for i, r := range doc.Handoffs {
		if r.Request == request && r.Item == item && (pick < 0 || handoffNumber(r.ID) > handoffNumber(doc.Handoffs[pick].ID)) {
			pick = i
		}
	}
	if pick < 0 {
		return SentHandoff{}, false, nil
	}
	return answered(doc.Handoffs[pick]), true, nil
}

// answered is a record as the store answers it: never with its text.
func answered(h SentHandoff) SentHandoff {
	h.Text = ""
	h.Handed = slices.Clone(h.Handed)
	return h
}

// handoffNumber is a record id's n.
func handoffNumber(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "ho-"), 10, 64)
	return n
}

// pruneHandoffs keeps the document to MaxHandoffsKept records: past them
// the closed one closed longest ago goes, a waiting one never.
func pruneHandoffs(doc *handoffsDoc) {
	for len(doc.Handoffs) > MaxHandoffsKept {
		oldest := -1
		for i, h := range doc.Handoffs {
			if h.State == HandoffWaiting || h.EndedAt == nil {
				continue
			}
			if oldest < 0 || h.EndedAt.Before(*doc.Handoffs[oldest].EndedAt) {
				oldest = i
			}
		}
		if oldest < 0 {
			return
		}
		doc.Handoffs = slices.Delete(doc.Handoffs, oldest, oldest+1)
	}
}

// closeHandoffsOfLocked closes as withdrawn every waiting record made from a
// request in gone, which a prune is about to remove: no package outlives
// its brief unseen. A document that does not read is left as it is. The
// caller holds mu.
func (s *Store) closeHandoffsOfLocked(gone []string) error {
	doc, err := s.loadHandoffs()
	if errors.Is(err, ErrHandoffsDamaged) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	changed := false
	for i := range doc.Handoffs {
		h := &doc.Handoffs[i]
		if h.State == HandoffWaiting && slices.Contains(gone, h.Request) {
			h.State, h.Text, h.EndedAt, h.EndedBy = HandoffWithdrawn, "", &now, ""
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.write(s.st, HandoffsFile, doc, MaxHandoffsDoc)
}

// loadHandoffs reads the document; none yet is an empty one. One that
// does not read, or of a later schema, is ErrHandoffsDamaged, kept as it
// is. The caller holds mu.
func (s *Store) loadHandoffs() (handoffsDoc, error) {
	var doc handoffsDoc
	ok, err := s.st.LoadLimit(HandoffsFile, &doc, MaxHandoffsDoc)
	if err != nil {
		return handoffsDoc{}, fmt.Errorf("%w: %v", ErrHandoffsDamaged, err)
	}
	if !ok {
		return handoffsDoc{Schema: HandoffsSchema, Next: 1, Handoffs: []SentHandoff{}}, nil
	}
	if doc.Schema != HandoffsSchema || doc.Next < 1 {
		return handoffsDoc{}, fmt.Errorf("%w: schema %d", ErrHandoffsDamaged, doc.Schema)
	}
	if doc.Handoffs == nil {
		doc.Handoffs = []SentHandoff{}
	}
	return doc, nil
}
