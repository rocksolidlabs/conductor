package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A chat's wire (docs/plans/chat-or-terminal.md, design 6; proto's
// transcript.go): each entry added or revised goes to every viewer in a
// TRANSCRIPT frame once its event's state is applied (sendApplied, after the
// attention and the activity); a viewer that attaches is sent, after its
// ready and the activity and chat replays, every open permission request's
// entry and then the entries changed after its resume point; it reads more
// with transcript_get and stops a turn with interrupt.

// NoChatWords are ErrNoChat's.
const NoChatWords = "This session runs in its terminal: it has no chat."

// interruptWait bounds a viewer's interrupt: the driver's ask of its agent.
const interruptWait = 10 * time.Second

var (
	// ErrNoChat refuses a transcript_get on a session that runs in its
	// terminal (not_protocol).
	ErrNoChat error = &wayWords{NoChatWords}
	// ErrBadTranscriptGet refuses a transcript_get past its bounds.
	ErrBadTranscriptGet = errors.New("session: a transcript read past its bounds")
)

// entryEnvelope is what an entry message adds to its entry's JSON.
var entryEnvelope = len(`{"t":"entry","entry":}`)

// wireText is s as a TRANSCRIPT frame carries it: valid UTF-8 (a byte that
// is not dropped), its control characters but tab and line break dropped,
// so that it encodes to at most twice its bytes.
func wireText(s string) string {
	clean := utf8.ValidString(s)
	for i := 0; clean && i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			clean = false
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

// wireEntry is e with every word cleaned for the wire (wireText).
func wireEntry(e TranscriptEntry) TranscriptEntry {
	for _, p := range []*string{&e.Ref, &e.Parent, &e.By, &e.ByName, &e.Text, &e.State, &e.Answers, &e.Tool, &e.Summary,
		&e.InputSha256, &e.Status, &e.Measured, &e.Path, &e.Op, &e.Question, &e.Call, &e.Plain, &e.Cwd, &e.Why,
		&e.Phase, &e.Outcome, &e.Code, &e.Words, &e.Kind} {
		*p = wireText(*p)
	}
	if len(e.Choices) > 0 {
		e.Choices = slices.Clone(e.Choices)
		for i := range e.Choices {
			e.Choices[i] = wireText(e.Choices[i])
		}
	}
	return e
}

// entryJSON is e as a TRANSCRIPT frame carries it: cleaned (wireEntry) and
// encoded without HTML escaping, its words cut, in their order, until
// its entry message fits proto.MaxTranscriptEntry. An entry at the
// session's bounds needs no cut (TestTheLargestEntryIsTheFixture).
func entryJSON(e TranscriptEntry) []byte {
	e = wireEntry(e)
	room := proto.MaxTranscriptEntry - entryEnvelope
	b, err := proto.MarshalRaw(e)
	for err == nil && len(b) > room {
		// A byte of a word cleaned for the wire encodes to one or two: half
		// of what is over, cut, takes off at least that and at most all of
		// it, so the words are cut no shorter than they must be.
		over := len(b) - room
		cut := false
		for _, p := range []*string{&e.Text, &e.Summary, &e.Cwd, &e.Path, &e.Words, &e.Why} {
			if len(*p) > 0 {
				*p = cutAt(*p, max(len(*p)-max((over+1)/2, 1), 0))
				cut = true
				break
			}
		}
		if !cut {
			// Nothing left to cut: the entry's place alone.
			e = TranscriptEntry{Seq: e.Seq, Mark: e.Mark, Rev: e.Rev, At: e.At, Kind: e.Kind, Turn: e.Turn}
		}
		b, err = proto.MarshalRaw(e)
	}
	if err != nil {
		b, _ = proto.MarshalRaw(TranscriptEntry{Seq: e.Seq, Mark: e.Mark, Rev: e.Rev, At: e.At, Kind: e.Kind, Turn: e.Turn})
	}
	return b
}

// entryFrame is the TRANSCRIPT frame of one entry, new or revised.
func entryFrame(e TranscriptEntry) []byte {
	b := entryJSON(e)
	out := make([]byte, 0, 1+entryEnvelope+len(b))
	out = append(out, proto.TypeTranscript)
	out = append(out, `{"t":"entry","entry":`...)
	out = append(out, b...)
	return append(out, '}')
}

// span is the first and last seq the ring keeps (0, 0 when none).
func (r *transcriptRing) span() (first, last uint64) {
	if len(r.entries) == 0 {
		return 0, 0
	}
	return r.entries[0].e.Seq, r.entries[len(r.entries)-1].e.Seq
}

// pageFrames cuts entries, each as entryJSON makes it, into TRANSCRIPT pages
// of at most proto.TranscriptPageRoom of entries, in order, each naming
// reqID and saying first, last and more; one empty page when there are
// none and empty is true.
func pageFrames(reqID string, entries [][]byte, first, last uint64, more, empty bool) [][]byte {
	var frames [][]byte
	page := proto.TranscriptPageMsg{T: proto.TranscriptPageT, ReqID: reqID, Entries: []json.RawMessage{}, First: first, Last: last, More: more}
	used := -1
	flush := func() {
		if f, err := proto.EncodeTranscript(page); err == nil {
			frames = append(frames, f)
		}
		page.Entries, used = []json.RawMessage{}, -1
	}
	for _, b := range entries {
		if len(page.Entries) > 0 && used+1+len(b) > proto.TranscriptPageRoom {
			flush()
		}
		page.Entries = append(page.Entries, b)
		used += 1 + len(b)
	}
	if len(page.Entries) > 0 || empty && len(frames) == 0 {
		flush()
	}
	return frames
}

// readLocked answers a read of entries (m names no part): after m.After,
// oldest first, at most m.Limit; or with m.Before, the newest of those
// before it. The page holds what fits proto.TranscriptPageRoom, the rest
// left to more. The caller holds s.mu.
func (s *Local) readLocked(m proto.TranscriptGet) ([]byte, error) {
	r := &s.transcript
	limit := m.Limit
	if limit <= 0 || limit > proto.MaxTranscriptPage {
		limit = proto.MaxTranscriptPage
	}
	first, last := r.span()
	var page [][]byte
	more := false
	if m.Before == 0 {
		entries, err := r.after(m.After, limit)
		if err != nil {
			return nil, err
		}
		used := -1
		for _, e := range entries {
			b := entryJSON(e)
			if used+1+len(b) > proto.TranscriptPageRoom {
				break
			}
			page = append(page, b)
			used += 1 + len(b)
		}
		if n := len(page); n > 0 {
			more = entries[n-1].Seq < last
		}
	} else {
		before := min(m.Before, r.seq+1)
		if before > m.After+1 && r.find(before-1) == nil {
			return nil, ErrTranscriptGone
		}
		i := sort.Search(len(r.entries), func(i int) bool { return r.entries[i].e.Seq >= before })
		used := -1
		lowest := before
		for j := i - 1; j >= 0 && len(page) < limit; j-- {
			e := r.entries[j].e
			if e.Seq <= m.After {
				break
			}
			b := entryJSON(e)
			if used+1+len(b) > proto.TranscriptPageRoom {
				break
			}
			page = append(page, b)
			used += 1 + len(b)
			lowest = e.Seq
		}
		slices.Reverse(page)
		more = lowest > m.After+1 && lowest > 1
	}
	frames := pageFrames(m.ReqID, page, first, last, more, true)
	if len(frames) != 1 {
		return nil, ErrTranscriptGone
	}
	return frames[0], nil
}

// partLocked answers a read of a part: a piece of at most
// proto.MaxTranscriptData bytes from its offset. The caller holds s.mu.
func (s *Local) partLocked(m proto.TranscriptGet) ([]byte, error) {
	ref := m.Part
	data, total, cut, err := s.transcript.part(ref.Seq, ref.Name, int(min(ref.Offset, int64(MaxTranscriptPart))))
	if err != nil {
		if errors.Is(err, ErrNoSuchPart) {
			return nil, ErrBadTranscriptGet
		}
		return nil, err
	}
	if len(data) > proto.MaxTranscriptData {
		data = data[:proto.MaxTranscriptData]
	}
	kept := int64(len(s.transcript.parts[partKey{ref.Seq, ref.Name}].data))
	return proto.EncodeTranscript(proto.TranscriptPartMsg{T: proto.TranscriptPartT, ReqID: m.ReqID, Seq: ref.Seq, Name: ref.Name,
		Offset: min(ref.Offset, kept), Total: int64(total), Kept: kept, Data: data, Cut: cut})
}

// TranscriptGet answers sub's transcript_get m: a TRANSCRIPT page or part
// naming m.ReqID, queued to sub. Every role reads, while its link stands;
// at most proto.MaxTranscriptInFlight replies of sub's wait to be written
// to it (ErrTooManyRequests past them). A refusal is returned for the
// caller to answer with TranscriptRefused: ErrNoChat on a session that runs
// in its terminal, ErrTranscriptGone for what the transcript no longer
// keeps, ErrBadTranscriptGet for a read past its bounds, a link's refusal.
func (s *Local) TranscriptGet(sub *Subscription, m proto.TranscriptGet) error {
	if s.way.Load() == nil {
		return ErrNoChat
	}
	if !m.Valid() {
		return ErrBadTranscriptGet
	}
	if err := revoked(sub); err != nil {
		return err
	}
	if sub.transcriptReads.Add(1) > proto.MaxTranscriptInFlight {
		sub.transcriptReads.Add(-1)
		return ErrTooManyRequests
	}
	s.mu.Lock()
	var frame []byte
	var err error
	switch {
	case s.way.Load() == nil:
		err = ErrNoChat
	case m.Part != nil:
		frame, err = s.partLocked(m)
	default:
		frame, err = s.readLocked(m)
	}
	s.mu.Unlock()
	if err != nil {
		sub.transcriptReads.Add(-1)
		return err
	}
	sub.sendThen(frame, func() { sub.transcriptReads.Add(-1) })
	return nil
}

// chatCode is the code and words a viewer is told for err, a refusal of the
// chat's or of a message's: "" for an error that is none of them.
func chatCode(err error) (code, words string) {
	switch {
	case errors.Is(err, ErrNotTerminal):
		return proto.ErrCodeNotTerminal, NotTerminalWords
	case errors.Is(err, ErrNotProtocol):
		return proto.ErrCodeNotProtocol, NotProtocolWords
	case errors.Is(err, ErrNoChat):
		return proto.ErrCodeNotProtocol, NoChatWords
	case errors.Is(err, ErrNoTurn):
		return proto.ErrCodeNoTurn, NoTurnWords
	case errors.Is(err, ErrTranscriptGone):
		return proto.ErrCodeTranscriptGone, TranscriptGoneWords
	case errors.Is(err, ErrUnconfirmed):
		return proto.ErrCodeUnconfirmed, UnconfirmedWords
	case errors.Is(err, ErrTooManyQueued):
		return proto.ErrCodeTooManyRequests, TooManyQueuedWords
	case errors.Is(err, ErrPermissionCard):
		return proto.ErrCodePermission, PermissionCardWords
	case errors.Is(err, ErrNoExpand):
		return proto.ErrCodeNotSent, NoExpandWords
	case errors.Is(err, ErrNotAnswerer):
		return proto.ErrCodeNotAnswerer, NotAnswererWords
	case errors.Is(err, ErrStaleRequest):
		return proto.ErrCodeStaleRequest, StaleRequestWords
	case errors.Is(err, ErrBadTranscriptGet):
		return proto.ErrCodeBadFrame, "bad transcript_get message"
	case errors.Is(err, ErrMoving):
		return proto.ErrCodeMoving, MovingWords
	case errors.Is(err, ErrMoveRefused):
		return proto.ErrCodeMoveRefused, err.Error()
	}
	return "", ""
}

// TranscriptRefused is the error that answers a transcript_get of reqID
// that TranscriptGet refused with err, naming it.
func TranscriptRefused(reqID string, err error) []byte {
	return chatWord(reqID, err)
}

// chatWord is the error naming reqID that answers a chat request refused
// with err: a link's refusal (revoked, expired), the chat's codes, else as
// an answer is told (AnswerResult).
func chatWord(reqID string, err error) []byte {
	if !proto.ValidAnswerReqID(reqID) {
		reqID = ""
	}
	code, msg, ok := LinkRefusal(err)
	if !ok {
		code, msg = chatCode(err)
	}
	if code == "" {
		return AnswerResult(reqID, err)
	}
	return proto.MustControl(proto.ErrorMsg{T: proto.CtlError, Code: code, Message: msg, RequestID: reqID})
}

// InterruptWord answers the interrupt reqID: interrupt_ok when err is nil,
// else the error naming it (not_protocol, no_turn, read_only,
// too_many_requests, a link's refusal).
func InterruptWord(reqID string, err error) []byte {
	if err == nil {
		return proto.MustControl(proto.InterruptOK{T: proto.CtlInterruptOK, RequestID: reqID})
	}
	return chatWord(reqID, err)
}

// InterruptAsk answers sub's interrupt reqID, the chat's Stop, off the
// caller's goroutine (a socket's read loop goes on at once): a controller's,
// one at a time per connection (too_many_requests while one is under way),
// held to its link as it is acted on; wait, when not nil, is waited for
// first (a host vouching for its server), within the interrupt's own
// bound. Its word (InterruptWord) is queued to sub once the driver has
// asked the agent. A session that runs in its terminal answers not_protocol
// and a view role read_only, at once.
func (s *Local) InterruptAsk(sub *Subscription, reqID string, wait func(context.Context) error) {
	switch {
	case s.Moving():
		sub.send(InterruptWord(reqID, ErrMoving))
		return
	case s.way.Load() == nil:
		sub.send(InterruptWord(reqID, ErrNotProtocol))
		return
	case sub.Role != RoleControl:
		sub.send(InterruptWord(reqID, ErrReadOnly))
		return
	case !sub.interrupting.CompareAndSwap(false, true):
		sub.send(InterruptWord(reqID, ErrTooManyRequests))
		return
	}
	go func() {
		defer sub.interrupting.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), interruptWait)
		defer cancel()
		var err error
		if wait != nil {
			err = wait(ctx)
		}
		if err == nil {
			err = revoked(sub)
		}
		if err == nil {
			err = s.Interrupt(ctx, sub)
		}
		sub.send(InterruptWord(reqID, err))
	}()
}

// sendEntries sends every viewer, the quiet ones but, the entries seqs as
// the ring keeps them now (an entry dropped meanwhile is sent no more), each
// once, under the session's lock: a viewer that attaches meanwhile has them
// in its replay or after it, never before it. Nothing while the session
// runs in its terminal, but with anyWay (a move's notice).
func (s *Local) sendEntries(seqs []uint64, anyWay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.way.Load() == nil && !anyWay {
		return
	}
	sent := make(map[uint64]bool, len(seqs))
	for _, seq := range seqs {
		if sent[seq] {
			continue
		}
		sent[seq] = true
		if re := s.transcript.find(seq); re != nil {
			s.hub.BroadcastLoud(entryFrame(re.e))
		}
	}
}

// transcriptReplayLocked is what a viewer attaching with the resume point
// after (the highest mark it holds) is sent after its ready: every open
// permission request's entry first, whatever after is and outside the
// budget; then, in mark order, the entries whose mark is above after, the
// newest that fit proto.TranscriptReplayBytes. Its pages say more when it
// left a change out: past the budget, or when the ring no longer holds
// every change after after (an entry dropped since, or a mark this
// transcript never gave). Nothing when there is nothing to send. The
// caller holds s.mu.
func (s *Local) transcriptReplayLocked(after uint64) [][]byte {
	r := &s.transcript
	first, last := r.span()
	covered := after <= r.mark && r.droppedMark <= after
	if after > r.mark {
		// A mark this transcript never gave (another's): the client holds
		// nothing of this one.
		after = 0
	}
	var open [][]byte
	var changed []*ringEntry
	for _, re := range r.entries {
		switch {
		case re.pinned:
			open = append(open, entryJSON(re.e))
		case re.e.Mark > after:
			changed = append(changed, re)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].e.Mark < changed[j].e.Mark })
	enc := make([][]byte, len(changed))
	i, used := len(changed), 0
	for i > 0 {
		b := entryJSON(changed[i-1].e)
		if used+len(b) > proto.TranscriptReplayBytes {
			break
		}
		used += len(b)
		i--
		enc[i] = b
	}
	more := !covered || i > 0
	frames := pageFrames("", open, first, last, more, false)
	return append(frames, pageFrames("", enc[i:], first, last, more, more && len(frames) == 0)...)
}

// welcomeTranscriptLocked is what a chat's welcome says of its transcript.
// The caller holds s.mu.
func (s *Local) welcomeTranscriptLocked() *proto.WelcomeTranscript {
	first, last := s.transcript.span()
	return &proto.WelcomeTranscript{First: first, Last: last, Mark: s.transcript.mark}
}
