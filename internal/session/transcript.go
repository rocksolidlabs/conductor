package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode/utf8"
)

// The transcript: a protocol session keeps its conversation as typed
// entries in a bounded ring in memory (docs/plans/chat-or-terminal.md,
// design 5). Nothing of it is written to disk, and the agent's own
// transcript files are never opened.

// Transcript entry kinds (TranscriptEntry.Kind).
const (
	EntryPrompt     = "prompt"
	EntryText       = "text"
	EntryTool       = "tool"
	EntryFile       = "file"
	EntryPermission = "permission"
	EntryTurn       = "turn"
	EntryNotice     = "notice"
)

// Parts: what an entry keeps beside the ring, fetched when a person opens
// its row (TranscriptPart).
const (
	PartInput  = "input"
	PartOutput = "output"
	PartDiff   = "diff"
)

// Prompt states (TranscriptEntry.State of a prompt). PromptWithdrawn is a
// message whose sender no longer stood when its turn to be written came:
// it was never written.
const (
	PromptQueued      = "queued"
	PromptWritten     = "written"
	PromptAccepted    = "accepted"
	PromptUnconfirmed = "unconfirmed"
	PromptWithdrawn   = "withdrawn"
)

// Permission request states (TranscriptEntry.State of a permission).
const (
	PermissionOpen      = "open"
	PermissionAllowed   = "allowed"
	PermissionDenied    = "denied"
	PermissionWithdrawn = "withdrawn"
)

// Bounds of the transcript.
const (
	// MaxTranscriptEntries and MaxTranscriptBytes bound the ring: past
	// either the oldest entries go (an open request's never).
	MaxTranscriptEntries = 1000
	MaxTranscriptBytes   = 2 << 20
	// MaxTranscriptPart bounds one part: a longer one is kept cut, its
	// whole size said. MaxTranscriptParts bounds them all: past it the
	// oldest go (an open request's input never).
	MaxTranscriptPart  = 256 << 10
	MaxTranscriptParts = 8 << 20
	// MaxPendingPermissions bounds the open permission requests of a
	// session; their entries and input parts are held while they are open.
	MaxPendingPermissions = 8
	// MaxTranscriptPage bounds the entries one read returns; MaxTranscriptPiece
	// the bytes of a part one read returns.
	MaxTranscriptPage  = 100
	MaxTranscriptPiece = 96 << 10
	// MaxEntryText bounds a prompt's or a text entry's text, in bytes: an
	// agent's longer message goes on in the next entry, with the same ref.
	MaxEntryText = 4 << 10
	// MaxEntryRef bounds an entry's ref and parent, an agent's ids.
	MaxEntryRef = 128
	// MaxEntrySummary bounds a tool step's or a request's summary.
	MaxEntrySummary = 1 << 10
	// MaxNoticeWords bounds a notice's words, and MaxNoticeCode its code.
	MaxNoticeWords = 500
	MaxNoticeCode  = 32
	// TextCoalesce is the least time between two revisions of an agent's
	// streamed words in one entry; the final one closes it at once.
	TextCoalesce = 100 * time.Millisecond
)

// TranscriptGoneWords are ErrTranscriptGone's.
const TranscriptGoneWords = "That part of the chat is no longer kept."

// ErrTranscriptGone answers a read of an entry or a part the transcript
// no longer keeps.
var ErrTranscriptGone error = &wayWords{TranscriptGoneWords}

// ErrNoSuchPart answers a read of a part by a name outside the closed set.
var ErrNoSuchPart = errors.New("session: a part is named input, output or diff")

// TranscriptEntry is one entry of a protocol session's transcript.
type TranscriptEntry struct {
	// Seq is its place in the session, from 1, rising by one in the order
	// the events were handed over, never reused.
	Seq uint64 `json:"seq"`
	// Mark is the session's count of changes as of the entry's last one:
	// every entry added or revised takes the next.
	Mark uint64 `json:"mark"`
	// Rev is 0, raised by each revision.
	Rev uint32 `json:"rev"`
	// At is when the session applied it, to the millisecond.
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Turn is Conductor's count of the session's turns, from 1.
	Turn uint32 `json:"turn"`
	// Ref is the agent's own id of the thing; Parent the ref of the tool
	// step a subagent's entry belongs to.
	Ref    string `json:"ref,omitempty"`
	Parent string `json:"parent,omitempty"`

	// A prompt: who sent it ("person" or "conductor") and their name, its
	// text (none for an answer to a question: Answers names the question),
	// and its state. A text: its words, Final once closed.
	By      string `json:"by,omitempty"`
	ByName  string `json:"byName,omitempty"`
	Text    string `json:"text,omitempty"`
	State   string `json:"state,omitempty"`
	Answers string `json:"answers,omitempty"`
	Final   bool   `json:"final,omitempty"`

	// A tool step, and a permission request: the tool's name (for a file,
	// the ref of the step that changed it), a summary, the input's size and
	// digest. A step's status, exit code, duration (measured "conductor"
	// when the agent gave none) and output's size.
	Tool        string `json:"tool,omitempty"`
	Summary     string `json:"summary,omitempty"`
	InputBytes  int    `json:"inputBytes,omitempty"`
	InputSha256 string `json:"inputSha256,omitempty"`
	Status      string `json:"status,omitempty"`
	Exit        *int   `json:"exit,omitempty"`
	DurationMs  *int64 `json:"durationMs,omitempty"`
	Measured    string `json:"measured,omitempty"`
	OutputBytes int    `json:"outputBytes,omitempty"`

	// A file: its path relative to the session's folder (none, with
	// Outside, for one outside it), the op and the lines added and removed.
	Path    string `json:"path,omitempty"`
	Outside bool   `json:"outside,omitempty"`
	Op      string `json:"op,omitempty"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`

	// A permission request: its question's id, the agent's id of the call,
	// the question's plain words, the folder it names, its choices, and,
	// once it is no longer open, why.
	Question string   `json:"question,omitempty"`
	Call     string   `json:"call,omitempty"`
	Plain    string   `json:"plain,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Why      string   `json:"why,omitempty"`

	// A turn: its phase and, at its end, how it ended and what it cost.
	Phase   string     `json:"phase,omitempty"`
	Outcome string     `json:"outcome,omitempty"`
	Usage   *TurnUsage `json:"usage,omitempty"`

	// A notice: its code and fixed words.
	Code  string `json:"code,omitempty"`
	Words string `json:"words,omitempty"`
}

// TurnUsage is a turn's cost where the agent reports it.
type TurnUsage struct {
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// ringEntry is an entry as the ring keeps it: its encoded size, and
// whether it is held (an open request's).
type ringEntry struct {
	e      TranscriptEntry
	size   int
	pinned bool
}

// partKey names a part: its entry and its name.
type partKey struct {
	seq  uint64
	name string
}

// ringPart is a part as the ring keeps it: its bytes (cut to
// MaxTranscriptPart), its whole size, and whether it is held.
type ringPart struct {
	data   []byte
	total  int
	cut    bool
	pinned bool
}

// transcriptRing is a protocol session's transcript (design 5): entries in
// seq order, at most MaxTranscriptEntries and MaxTranscriptBytes of them,
// and their parts, at most MaxTranscriptParts, the oldest dropped first,
// what is held (an open request's entry and its input) dropped never.
// Guarded by the session's lock.
type transcriptRing struct {
	entries []*ringEntry
	bytes   int
	// seq is the last seq given, mark the last mark.
	seq  uint64
	mark uint64
	// droppedMark is the highest mark of an entry dropped.
	droppedMark uint64

	parts     map[partKey]*ringPart
	partOrder []partKey
	partBytes int
}

// entrySize is what an entry counts toward MaxTranscriptBytes: its JSON.
func entrySize(e TranscriptEntry) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(e) != nil {
		return 0
	}
	return b.Len() - 1
}

// add appends e as the next entry, stamped at, and drops what the bounds
// say. It returns the entry's seq.
func (r *transcriptRing) add(e TranscriptEntry, at time.Time) uint64 {
	r.seq++
	r.mark++
	e.Seq, e.Mark, e.Rev = r.seq, r.mark, 0
	e.At = at.UTC().Truncate(time.Millisecond)
	re := &ringEntry{e: e, size: entrySize(e)}
	r.entries = append(r.entries, re)
	r.bytes += re.size
	r.trim()
	return e.Seq
}

// find is the entry of seq the ring keeps, nil when none.
func (r *transcriptRing) find(seq uint64) *ringEntry {
	i := sort.Search(len(r.entries), func(i int) bool { return r.entries[i].e.Seq >= seq })
	if i < len(r.entries) && r.entries[i].e.Seq == seq {
		return r.entries[i]
	}
	return nil
}

// revise changes the entry of seq with change, a new mark and the next
// rev, its seq kept; false when the ring no longer keeps it.
func (r *transcriptRing) revise(seq uint64, change func(*TranscriptEntry)) bool {
	re := r.find(seq)
	if re == nil {
		return false
	}
	change(&re.e)
	r.mark++
	re.e.Seq, re.e.Mark = seq, r.mark
	re.e.Rev++
	size := entrySize(re.e)
	r.bytes += size - re.size
	re.size = size
	r.trim()
	return true
}

// pin holds the entry of seq, and its input part, or lets them go: held,
// they are never dropped; let go, they go on the next overflow.
func (r *transcriptRing) pin(seq uint64, held bool) {
	if re := r.find(seq); re != nil {
		re.pinned = held
	}
	if p := r.parts[partKey{seq, PartInput}]; p != nil {
		p.pinned = held
	}
}

// trim drops the oldest entries not held, with their parts, while the ring
// is past a bound.
func (r *transcriptRing) trim() {
	for len(r.entries) > MaxTranscriptEntries || r.bytes > MaxTranscriptBytes {
		i := 0
		for i < len(r.entries) && r.entries[i].pinned {
			i++
		}
		if i == len(r.entries) {
			return
		}
		re := r.entries[i]
		r.entries = append(r.entries[:i], r.entries[i+1:]...)
		r.bytes -= re.size
		r.droppedMark = max(r.droppedMark, re.e.Mark)
		for _, name := range []string{PartInput, PartOutput, PartDiff} {
			r.dropPart(partKey{re.e.Seq, name})
		}
	}
}

// putPart keeps data as the part name of the entry seq: cut to
// MaxTranscriptPart (cut, with its whole size), the oldest parts not held
// dropped past MaxTranscriptParts. cut says the driver kept less than the
// agent sent.
func (r *transcriptRing) putPart(seq uint64, name string, data []byte, cut, held bool) {
	if r.find(seq) == nil {
		return
	}
	k := partKey{seq, name}
	r.dropPart(k)
	p := &ringPart{total: len(data), cut: cut, pinned: held}
	if len(data) > MaxTranscriptPart {
		data, p.cut = data[:MaxTranscriptPart], true
	}
	p.data = bytes.Clone(data)
	if r.parts == nil {
		r.parts = map[partKey]*ringPart{}
	}
	r.parts[k] = p
	r.partOrder = append(r.partOrder, k)
	r.partBytes += len(p.data)
	for r.partBytes > MaxTranscriptParts {
		i := 0
		for i < len(r.partOrder) && r.parts[r.partOrder[i]].pinned {
			i++
		}
		if i == len(r.partOrder) {
			return
		}
		r.dropPart(r.partOrder[i])
	}
}

// dropPart lets the part k go, if the ring keeps it.
func (r *transcriptRing) dropPart(k partKey) {
	p := r.parts[k]
	if p == nil {
		return
	}
	delete(r.parts, k)
	r.partBytes -= len(p.data)
	for i, o := range r.partOrder {
		if o == k {
			r.partOrder = append(r.partOrder[:i], r.partOrder[i+1:]...)
			break
		}
	}
}

// after is the entries whose seq is above seq, oldest first, at most
// limit; ErrTranscriptGone when the one right after seq was given and is
// no longer kept.
func (r *transcriptRing) after(seq uint64, limit int) ([]TranscriptEntry, error) {
	if limit <= 0 || limit > MaxTranscriptPage {
		limit = MaxTranscriptPage
	}
	if seq < r.seq && r.find(seq+1) == nil {
		return nil, ErrTranscriptGone
	}
	i := sort.Search(len(r.entries), func(i int) bool { return r.entries[i].e.Seq > seq })
	var out []TranscriptEntry
	for ; i < len(r.entries) && len(out) < limit; i++ {
		out = append(out, r.entries[i].e)
	}
	return out, nil
}

// part is a piece of the part name of seq from offset, at most
// MaxTranscriptPiece bytes, with the part's whole size and whether it was
// kept cut; ErrTranscriptGone when the ring no longer keeps it.
func (r *transcriptRing) part(seq uint64, name string, offset int) (data []byte, total int, cut bool, err error) {
	switch name {
	case PartInput, PartOutput, PartDiff:
	default:
		return nil, 0, false, ErrNoSuchPart
	}
	p := r.parts[partKey{seq, name}]
	if p == nil {
		return nil, 0, false, ErrTranscriptGone
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(p.data) {
		offset = len(p.data)
	}
	end := min(offset+MaxTranscriptPiece, len(p.data))
	return bytes.Clone(p.data[offset:end]), p.total, p.cut, nil
}

// cutAt is the longest prefix of s of at most n bytes that ends at a
// character boundary.
func cutAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Transcript returns the transcript's entries after seq after, oldest
// first, at most limit (MaxTranscriptPage when limit is 0 or past it):
// ErrTranscriptGone when the entry right after after is no longer kept,
// ErrNotProtocol on a session that runs in its terminal.
func (s *Local) Transcript(after uint64, limit int) ([]TranscriptEntry, error) {
	if s.way.Load() == nil {
		return nil, ErrNotProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript.after(after, limit)
}

// TranscriptPart returns a piece of the part name (PartInput, PartOutput,
// PartDiff) of the entry seq from offset, at most MaxTranscriptPiece
// bytes, with the part's whole size as the agent sent it and whether it
// was kept cut: ErrTranscriptGone once the transcript no longer keeps it,
// ErrNotProtocol on a session that runs in its terminal.
func (s *Local) TranscriptPart(seq uint64, name string, offset int) (data []byte, total int, cut bool, err error) {
	if s.way.Load() == nil {
		return nil, 0, false, ErrNotProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript.part(seq, name, offset)
}
