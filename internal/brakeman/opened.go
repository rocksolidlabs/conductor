package brakeman

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Opened marks (BKUI, #993): which briefs each person has opened, kept by
// the server so that a phone and a laptop holding one credential count
// the same unread briefs. A person here is a credential's actor (the
// workbench token's "admin", an identity provider's "user:<subject>"),
// which the API takes from the credential alone. The marks are one
// document of their own, brakeman/opened.json, outside requests/, which
// no older build opens; no request, brief or state document changes.

// The opened marks' document and bounds.
const (
	OpenedFile      = "opened.json"
	OpenedSchema    = 1
	MaxOpenedDoc    = 512 << 10
	MaxOpenedActors = 32  // actors kept; past them the one written longest ago goes
	MaxOpenedIDs    = 500 // ids marked an actor, and ids an answer's unread holds
	MaxOpenedMark   = 100 // ids one mark takes
	MaxOpenedActor  = 64  // an actor's bytes (an API actor is at most 53)
)

// ErrOpenedDamaged is an opened document that does not read: it is kept
// as it is and nothing is answered from it.
var ErrOpenedDamaged = errors.New("brakeman: opened.json is damaged or of a later schema, kept as it is")

// openedDoc is brakeman/opened.json.
type openedDoc struct {
	Schema int           `json:"schema"`
	People []openedEntry `json:"people"`
}

// openedEntry is one actor's marks: when its count began (every brief
// kept before then counts as opened), when the entry was last written,
// and the requests it marked opened, by number (br-<n>'s n), newest
// first. Numbers, not ids, so the largest document fits its bound.
type openedEntry struct {
	Actor  string    `json:"actor"`
	Begun  time.Time `json:"begun"`
	At     time.Time `json:"at"`
	Opened []int64   `json:"opened"`
}

// Unopened is an actor's count: the briefs not yet opened, newest first,
// at most MaxOpenedIDs, and when the count began.
type Unopened struct {
	Unread []string  `json:"unread"`
	Begun  time.Time `json:"begun"`
}

// Opened answers actor's unread briefs: the briefed requests the store
// keeps, briefed after its count began and not marked opened. The first
// read of an actor begins its count now and writes its entry. Only the
// store that owns the records answers (ErrNotOwner otherwise): one that
// does not could not begin a count.
func (s *Store) Opened(actor string) (Unopened, error) {
	return s.opened(actor, nil)
}

// MarkOpened marks ids (br-<n>, 1 to MaxOpenedMark of them) opened for
// actor, a set union, and answers its count as Opened does. An id the
// store does not keep, or one with no brief, is dropped without error.
// changed says whether a mark was added.
func (s *Store) MarkOpened(actor string, ids []string) (Unopened, bool, error) {
	if len(ids) < 1 || len(ids) > MaxOpenedMark {
		return Unopened{}, false, invalidRequest("ids", "1 to %d request ids", MaxOpenedMark)
	}
	for i, id := range ids {
		if !requestIDPattern.MatchString(id) {
			return Unopened{}, false, invalidRequest(fmt.Sprintf("ids[%d]", i), "a request's id, br-<n>")
		}
	}
	var changed bool
	u, err := s.opened(actor, func(e *openedEntry, briefed map[int64]bool) {
		for _, id := range ids {
			n := requestNumber(id)
			if briefed[n] && !slices.Contains(e.Opened, n) {
				e.Opened = append(e.Opened, n)
				changed = true
			}
		}
	})
	return u, changed, err
}

// opened reads actor's entry (made, its count begun now, when there is
// none), lets mark add to it, keeps it to the requests still kept and to
// its bounds, writes the document when anything changed, and answers the
// count. The caller holds nothing; opened takes mu.
func (s *Store) opened(actor string, mark func(e *openedEntry, briefed map[int64]bool)) (Unopened, error) {
	if actor == "" || len(actor) > MaxOpenedActor {
		return Unopened{}, fmt.Errorf("%w: an actor of %d bytes", ErrBound, len(actor))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return Unopened{}, err
	}
	doc, err := s.loadOpened()
	if err != nil {
		return Unopened{}, err
	}
	// What the store keeps, newest first, and which of it is briefed.
	ids, err := s.ids()
	if err != nil {
		return Unopened{}, err
	}
	kept := make(map[int64]bool, len(ids))
	briefed := map[int64]bool{}
	var briefs []Request
	for _, id := range ids {
		kept[requestNumber(id)] = true
		r, err := s.loadLocked(id)
		if skippable(err) {
			continue
		}
		if err != nil {
			return Unopened{}, err
		}
		if r.HasBrief {
			briefed[r.number()] = true
			briefs = append(briefs, r)
		}
	}
	now := s.now().UTC()
	write := false
	i := slices.IndexFunc(doc.People, func(e openedEntry) bool { return e.Actor == actor })
	if i < 0 {
		doc.People = append(doc.People, openedEntry{Actor: actor, Begun: now, At: now, Opened: []int64{}})
		i, write = len(doc.People)-1, true
	}
	e := &doc.People[i]
	if mark != nil {
		before := len(e.Opened)
		mark(e, briefed)
		if len(e.Opened) != before {
			e.At, write = now, true
		}
	}
	// Each entry keeps only the requests still kept, newest first, at
	// most MaxOpenedIDs.
	for j := range doc.People {
		p := &doc.People[j]
		n := len(p.Opened)
		p.Opened = slices.DeleteFunc(p.Opened, func(id int64) bool { return !kept[id] })
		slices.SortFunc(p.Opened, func(a, b int64) int { return cmp.Compare(b, a) })
		p.Opened = slices.Compact(p.Opened)
		if len(p.Opened) > MaxOpenedIDs {
			p.Opened = p.Opened[:MaxOpenedIDs]
		}
		if len(p.Opened) != n {
			write = true
		}
	}
	out := Unopened{Unread: []string{}, Begun: e.Begun}
	marked := make(map[int64]bool, len(e.Opened))
	for _, n := range e.Opened {
		marked[n] = true
	}
	for _, r := range briefs {
		if len(out.Unread) == MaxOpenedIDs {
			break
		}
		if r.BriefAt.After(e.Begun) && !marked[r.number()] {
			out.Unread = append(out.Unread, r.ID)
		}
	}
	// Past MaxOpenedActors the entry written longest ago goes, never the
	// caller's.
	for len(doc.People) > MaxOpenedActors {
		oldest := -1
		for j, p := range doc.People {
			if p.Actor != actor && (oldest < 0 || p.At.Before(doc.People[oldest].At)) {
				oldest = j
			}
		}
		doc.People = slices.Delete(doc.People, oldest, oldest+1)
		write = true
	}
	if write {
		if err := s.write(s.st, s.openedPath(), doc, MaxOpenedDoc); err != nil {
			return Unopened{}, err
		}
	}
	return out, nil
}

// openedPath is the opened document's name in brakeman/.
func (s *Store) openedPath() string { return OpenedFile }

// loadOpened reads the opened document; none yet is an empty one. One
// that does not read, or of a later schema, is ErrOpenedDamaged, kept as
// it is. The caller holds mu.
func (s *Store) loadOpened() (openedDoc, error) {
	var doc openedDoc
	ok, err := s.st.LoadLimit(s.openedPath(), &doc, MaxOpenedDoc)
	if err != nil {
		return openedDoc{}, fmt.Errorf("%w: %v", ErrOpenedDamaged, err)
	}
	if !ok {
		return openedDoc{Schema: OpenedSchema, People: []openedEntry{}}, nil
	}
	if doc.Schema != OpenedSchema {
		return openedDoc{}, fmt.Errorf("%w: schema %d", ErrOpenedDamaged, doc.Schema)
	}
	if doc.People == nil {
		doc.People = []openedEntry{}
	}
	return doc, nil
}

// number is the request's n.
func (r Request) number() int64 { return requestNumber(r.ID) }
