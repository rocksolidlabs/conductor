package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A chat's permission request (docs/plans/chat-or-terminal.md, design 7,
// the boundary): a call the agent puts to Conductor's prompt tool
// (RequestPermission, held open by its POST) or hands over in its stream
// (ProtocolEvents.Permission) is a permission entry and the session's
// question, answered by the operator alone (AnswerPermission), bound to
// the request by its call and its input's digest. Nothing but that answer,
// delivered, allows: every end, bound, timer and withdrawal answers deny or
// withdraws, and answers the held request at once.

// The permission request's bounds and waits.
const (
	// AnnounceWait is how long a request whose call the stream has not yet
	// announced waits for it (Options.AnnounceWait): then it is denied.
	AnnounceWait = 10 * time.Second
	// PermissionWait is how long a request waits for the operator before
	// Conductor denies it (Options.PermissionWait): the bound in effect for
	// the agent less one minute, Claude Code's being the 8 hours Conductor
	// gives its MCP server (claude-protocol-mcp.json).
	PermissionWait = 8*time.Hour - time.Minute
	// maxAside bounds the attention changes kept aside while a request
	// shows: past it the oldest that is no question goes.
	maxAside = 16
	// permissionToolName is Conductor's own prompt tool, as the agent's
	// stream names its calls: a request about a call of it is none.
	permissionToolName = "mcp__conductor__permission"
)

// The words of the permission request (the plan's, design 13).
const (
	FloorWords              = "Conductor keeps agents out of its own files."
	NobodyAnsweredWords     = "Nobody answered in time."
	NotACallWords           = "Not a call this session is making."
	AlreadyAskedWords       = "Already asked."
	NotExactWords           = "Conductor cannot show this call exactly."
	WithdrawnElsewhereWords = "Withdrawn: answered elsewhere."
	TooLargeWords           = "This call's input is too large for Conductor to show it whole."
	// NotDeliveredWords is the activity line of an operator's answer
	// recorded and then not delivered: its request had gone.
	NotDeliveredWords = "The answer was not delivered: the request was lost."
	RequestLostWords  = "The request was lost."
)

// PermissionAsk is a request a chat's agent put to Conductor's prompt tool
// (POST /api/sessions/{id}/permission): the agent's id of the call ("" when
// its request names none), the tool's name and the call's whole input, the
// bytes as the agent sent them.
type PermissionAsk struct {
	Call  string
	Tool  string
	Input []byte
}

// heldRequest is a request held open by its POST: the session hands it its
// verdict, and it tells the session whether the verdict was delivered.
type heldRequest struct {
	verdict chan heldVerdict
	// done is closed as the request has returned; delivered, read once
	// done is closed, says its reply was written.
	done      chan struct{}
	delivered bool
}

// heldVerdict is a verdict handed to a held request: operator says it is
// the operator's answer, whose entry is recorded allowed or denied only
// once it is delivered.
type heldVerdict struct {
	v        Verdict
	operator bool
}

// asideChange is an attention change made while a permission request
// shows (I18): kept, in order, and applied once no request is open.
type asideChange struct {
	state   AttentionState
	message string
	source  string
	kind    string
	options []Option
	rules   attentionRules
	turn    uint64
	plan    string
	opt     attentionOpt
	// hasOpt says the change was made with an attentionOpt: one made
	// without is replayed without, as setAttentionLooked tells the two
	// apart (a change for one question has one).
	hasOpt bool
}

func (s *Local) announceWait() time.Duration {
	if s.opts.AnnounceWait > 0 {
		return s.opts.AnnounceWait
	}
	return AnnounceWait
}

func (s *Local) permissionWait() time.Duration {
	if s.opts.PermissionWait > 0 {
		return s.opts.PermissionWait
	}
	return PermissionWait
}

// afterFunc runs f once d has passed (Options.AfterFunc), answering the
// timer's stop.
func (s *Local) afterFunc(d time.Duration, f func()) func() bool {
	if s.opts.AfterFunc != nil {
		return s.opts.AfterFunc(d, f)
	}
	return time.AfterFunc(d, f).Stop
}

// OpenPermissions is the number of a chat's permission requests open (asked
// and not yet answered, withdrawn or denied) and waiting for their call's
// announcement; 0 for a session in its terminal.
func (s *Local) OpenPermissions() (open, waiting int) {
	p := s.way.Load()
	if p == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(p.perms), p.waiting
}

// announceLocked wakes the requests that wait for their call's
// announcement: a call announced or resulted, a turn's end, the session's
// end. The caller holds s.mu.
func (p *protocolWay) announceLocked() {
	if p.announced != nil {
		close(p.announced)
	}
	p.announced = make(chan struct{})
}

// inStreamLocked counts the open requests the stream handed over (no POST
// holds them). The caller holds s.mu.
func (p *protocolWay) inStreamLocked() int {
	n := 0
	for _, r := range p.perms {
		if r.held == nil {
			n++
		}
	}
	return n
}

// RequestPermission is a request a chat's agent put to Conductor's prompt
// tool, held open by its caller until it is answered. It names a call of
// the current turn the stream announced, before it or within AnnounceWait
// (I8), with the same name and digest and no result yet, one request per
// call; Conductor's floor denies a call into its own files before any
// question; at most MaxPendingPermissions are open or waiting. A request
// taken is the session's question until the operator answers it
// (AnswerPermission), the stream shows its call's result, the turn ends,
// PermissionWait passes, the session ends, or ctx ends (its POST gone:
// withdrawn "The request was lost.", recorded before any later answer can
// be applied). reply is called once with the verdict (never after ctx
// has ended), and the operator's is recorded allowed or denied only once
// reply returned nil. A verdict with Cancel is a withdrawal. Nothing but
// the operator's answer allows. Neither the input nor anything of it is
// logged.
func (s *Local) RequestPermission(ctx context.Context, a PermissionAsk, reply func(Verdict) error) {
	deny := func(words string) { _ = reply(Verdict{Message: words}) }
	p := s.way.Load()
	if p == nil {
		deny(NotACallWords)
		return
	}
	canon, ok := strictCanonical(a.Input)
	if !ok {
		deny(NotExactWords)
		return
	}
	if a.Tool == permissionToolName || len(a.Call) > MaxEntryRef || len(a.Tool) > MaxEventTool {
		deny(NotACallWords)
		return
	}
	digest := digestOf(a.Tool, canon)

	s.mu.Lock()
	if s.info.Status.Ended() || p.stopping || s.way.Load() != p {
		s.mu.Unlock()
		deny(EndedBeforeAnswerWords)
		return
	}
	if s.moveDone != nil || !p.isReady || p.restarting {
		s.mu.Unlock()
		deny(NotACallWords)
		return
	}
	if p.requests+p.inStreamLocked() >= MaxPendingPermissions {
		s.mu.Unlock()
		deny(TooManyRequestsWords)
		return
	}
	p.requests++
	p.waiting++
	defer func() {
		s.mu.Lock()
		p.requests--
		s.mu.Unlock()
		s.tryNoted()
	}()

	// The announcement wait: until the stream shows the call, the turn
	// ends, the session ends, AnnounceWait passes, or the POST goes.
	timeout := make(chan struct{})
	stopTimer := s.afterFunc(s.announceWait(), func() { close(timeout) })
	turnEnds := p.turnEnds
	var ref string
	for {
		if s.info.Status.Ended() || p.stopping {
			p.waiting--
			s.mu.Unlock()
			stopTimer()
			deny(EndedBeforeAnswerWords)
			return
		}
		if p.turnEnds != turnEnds {
			p.waiting--
			s.mu.Unlock()
			stopTimer()
			deny(NotACallWords)
			return
		}
		var words string
		ref, words = s.bindLocked(p, a.Call, a.Tool, digest)
		if words != "" {
			p.waiting--
			s.mu.Unlock()
			stopTimer()
			deny(words)
			return
		}
		if ref != "" {
			break
		}
		woken := p.announced
		s.mu.Unlock()
		if seam := s.permissionWaits.Load(); seam != nil {
			(*seam)()
		}
		select {
		case <-woken:
		case <-timeout:
			s.mu.Lock()
			p.waiting--
			s.mu.Unlock()
			deny(NotACallWords)
			return
		case <-ctx.Done():
			s.mu.Lock()
			p.waiting--
			s.mu.Unlock()
			stopTimer()
			return
		}
		s.mu.Lock()
	}
	stopTimer()
	p.waiting--
	p.asked[ref] = true

	var out applied
	out.event = "permission"
	entry := s.permissionEntryLocked(p, ref, a.Tool, a.Input, digest, "")
	if s.flooredLocked(a.Tool, a.Input) {
		// Conductor's own files: denied before any question.
		entry.State, entry.Why = PermissionDenied, FloorWords
		seq := s.addEntryLocked(entry, &out)
		out.captured = append(out.captured, capturedPiece{key: "", b: a.Input}, capturedPiece{key: pieceKey(seq, "text"), b: []byte(entry.Summary), again: true})
		s.mu.Unlock()
		s.sendApplied(out)
		deny(FloorWords)
		return
	}
	held := &heldRequest{verdict: make(chan heldVerdict, 1), done: make(chan struct{})}
	r := s.takeLocked(p, entry, ref, "", a.Input, held, &out)
	s.mu.Unlock()
	s.sendApplied(out)

	select {
	case hv := <-held.verdict:
		err := reply(hv.v)
		out = applied{event: "permission_delivered"}
		s.mu.Lock()
		if hv.operator {
			state := PermissionDenied
			if hv.v.Allow {
				state = PermissionAllowed
			}
			if err != nil {
				state = PermissionWithdrawn
			}
			s.reviseLocked(r.seq, func(e *TranscriptEntry) {
				e.State = state
				if err != nil {
					e.Why = RequestLostWords
				}
			}, &out)
			s.unpinLocked(r)
		}
		held.delivered = err == nil
		close(held.done)
		s.mu.Unlock()
		s.sendApplied(out)
		if hv.operator && err != nil {
			s.RecordServer(ActivityEntry{Type: ActivityStatus, Message: NotDeliveredWords})
		}
	case <-ctx.Done():
		// The POST went before a verdict reached it: withdrawn, recorded
		// before any later answer can be applied.
		out = applied{event: "permission_lost"}
		s.mu.Lock()
		if s.listedLocked(p, r) {
			s.closePermissionLocked(p, r, PermissionWithdrawn, RequestLostWords, nil, &out)
		} else if r.handed {
			// The operator's verdict on its way to it, never delivered.
			s.reviseLocked(r.seq, func(e *TranscriptEntry) { e.State, e.Why = PermissionWithdrawn, RequestLostWords }, &out)
			s.unpinLocked(r)
		}
		lost := r.handed
		close(held.done)
		s.mu.Unlock()
		s.sendApplied(out)
		if lost {
			s.RecordServer(ActivityEntry{Type: ActivityStatus, Message: NotDeliveredWords})
		}
	}
}

// bindLocked is the call a request names, as the stream announced it
// (I8): its ref once the stream showed it, in the current turn, with the
// same name and digest and no result; "" and no words while it may yet be
// announced; else the words it is denied with. A request that names no
// call takes the one call of the turn that matches, and is denied when
// more than one does. The caller holds s.mu.
func (s *Local) bindLocked(p *protocolWay, call, tool, digest string) (ref, words string) {
	if call != "" {
		if p.resulted[call] {
			return "", NotACallWords
		}
		t := p.tools[call]
		if t == nil {
			return "", ""
		}
		if !p.inTurn || t.turn != p.turns || t.rawName != tool || t.digest != digest || t.rawName == permissionToolName {
			return "", NotACallWords
		}
		if p.asked[call] {
			return "", AlreadyAskedWords
		}
		return call, ""
	}
	if !p.inTurn {
		return "", ""
	}
	var found []string
	asked := false
	for _, r := range p.toolOrder {
		t := p.tools[r]
		if t == nil || t.turn != p.turns || t.rawName != tool || t.digest != digest || t.rawName == permissionToolName {
			continue
		}
		if p.asked[r] {
			asked = true
			continue
		}
		found = append(found, r)
	}
	switch {
	case len(found) == 1:
		return found[0], ""
	case len(found) > 1:
		return "", NotACallWords
	case asked:
		return "", AlreadyAskedWords
	}
	return "", ""
}

// permissionEntryLocked is a request's entry, open, as the transcript
// keeps it. The caller holds s.mu.
func (s *Local) permissionEntryLocked(p *protocolWay, call, tool string, input []byte, digest, cwd string) TranscriptEntry {
	return TranscriptEntry{
		Kind: EntryPermission, Turn: p.turns, Call: cutAt(call, MaxEntryRef),
		Tool: cutAt(CleanMessage(tool), MaxEventTool), Plain: plainOf(tool),
		Summary:    requestSummary(input),
		Cwd:        cutAt(strings.Map(dropControl, cwd), MaxEventPath),
		InputBytes: len(input), InputSha256: digest,
		Choices: []string{proto.PermissionAllowOnce, proto.PermissionDeny}, State: PermissionOpen,
	}
}

// takeLocked takes a request: its entry open with its question's id, held
// with its input while open, put in the list (the oldest shown as the
// session's question), and PermissionWait armed. ref is an in-stream
// request's own id ("" for a held one), held a held request's. The caller
// holds s.mu.
func (s *Local) takeLocked(p *protocolWay, entry TranscriptEntry, call, ref string, input []byte, held *heldRequest, out *applied) *openPermission {
	entry.Question = NewID()
	entry.Ref = cutAt(ref, MaxEntryRef)
	seq := s.addEntryLocked(entry, out)
	s.transcript.putPart(seq, PartInput, input, false, true)
	s.transcript.pin(seq, true)
	out.captured = append(out.captured, capturedPiece{key: pieceKey(seq, PartInput), b: input}, capturedPiece{key: pieceKey(seq, "text"), b: []byte(entry.Summary), again: true})
	r := &openPermission{seq: seq, ref: ref, call: entry.Call, question: entry.Question, digest: entry.InputSha256, plain: entry.Plain, held: held}
	p.perms = append(p.perms, r)
	gen := p.gen
	r.stop = s.afterFunc(s.permissionWait(), func() { s.permissionTimedOut(p, gen, r) })
	s.showPermissionsLocked(p, out)
	return r
}

// permissionTimedOut is PermissionWait passed with r unanswered: denied.
func (s *Local) permissionTimedOut(p *protocolWay, gen uint64, r *openPermission) {
	var out applied
	out.event = "permission_timeout"
	s.mu.Lock()
	if p.gen != gen || !s.listedLocked(p, r) || r.claim != 0 {
		s.mu.Unlock()
		return
	}
	s.closePermissionLocked(p, r, PermissionDenied, NobodyAnsweredWords, nil, &out)
	s.mu.Unlock()
	s.sendApplied(out)
}

// listedLocked says r is still in the list of open requests. The caller
// holds s.mu.
func (s *Local) listedLocked(p *protocolWay, r *openPermission) bool {
	for _, o := range p.perms {
		if o == r {
			return true
		}
	}
	return false
}

// permissionWords is the question's plain words, by its tool's.
func permissionWords(plain string) string {
	switch plain {
	case "command":
		return "Run a command?"
	case "file":
		return "Change a file?"
	case "read":
		return "Read a file?"
	}
	return "Use a tool?"
}

// showPermissionsLocked has the attention show the oldest open request
// (I18): the attention as it was is kept aside as the first one shows, and
// shown again, with the changes made meanwhile applied after it, once the
// list is empty. The caller holds s.mu.
func (s *Local) showPermissionsLocked(p *protocolWay, out *applied) {
	want := ""
	var oldest *openPermission
	if len(p.perms) > 0 {
		oldest = p.perms[0]
		want = oldest.question
	}
	if want == p.showing {
		return
	}
	if p.showing == "" {
		p.under = s.info.Attention
	}
	if oldest != nil {
		now := s.activity.Stamp(1)
		att := Attention{State: AttentionNeedsInput, Message: permissionWords(oldest.plain), Source: SourceProtocol, Kind: KindPermission, ID: oldest.question, Since: &now}
		s.info.Attention = att
		out.own = append(out.own, ActivityEntry{At: now, Type: ActivityAttention, Message: att.Message})
	} else {
		s.info.Attention = p.under
		p.under = Attention{}
		out.replay = append(out.replay, p.aside...)
		p.aside = nil
	}
	p.showing = want
	s.noteResumedLocked(s.info.Attention)
	s.noteGoneLocked()
	s.signalAttention()
	s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
	out.changed = true
}

// asideLocked keeps an attention change made while a request shows (I18):
// no other setter replaces or withdraws it. A question asked meanwhile is
// shown once the list is empty, its id the one the ask is told now; a
// withdrawal of one asked meanwhile, or of the question the request took
// the place of, takes it away at once. The caller holds s.mu.
func (s *Local) asideLocked(p *protocolWay, c asideChange) {
	if only := c.opt.only; only != "" && c.opt.asked == nil {
		kept := p.aside[:0]
		for _, a := range p.aside {
			if a.opt.id != only {
				kept = append(kept, a)
			}
		}
		p.aside = kept
		if p.under.ID == only {
			p.under = Attention{}
			s.forgetLocked(only)
		}
		if s.own.ID == only {
			s.dropOwnLocked(ChatLeftAgent)
		}
		return
	}
	if c.state == AttentionNeedsInput {
		c.opt.id = NewID()
		c.hasOpt = true
		if c.opt.asked != nil {
			*c.opt.asked = c.opt.id
			c.opt.asked = nil
		}
	}
	c.rules &^= spendToken
	p.aside = append(p.aside, c)
	if len(p.aside) > maxAside {
		// Past the bound the oldest change that is no question goes: a
		// question's id is its asker's already. Only questions kept: the
		// oldest of them.
		drop := 0
		for i, a := range p.aside {
			if a.state != AttentionNeedsInput {
				drop = i
				break
			}
		}
		p.aside = append(p.aside[:drop], p.aside[drop+1:]...)
	}
}

// removePermissionLocked takes r out of the list, its timer stopped, the
// next oldest shown. Its entry and input stay held (pinned) until the
// caller has written how it ended (unpinLocked): the ring never drops a
// request's entry before its end is in it. The caller holds s.mu.
func (s *Local) removePermissionLocked(p *protocolWay, r *openPermission, out *applied) {
	for i, o := range p.perms {
		if o == r {
			p.perms = append(p.perms[:i], p.perms[i+1:]...)
			break
		}
	}
	if r.stop != nil {
		r.stop()
	}
	s.showPermissionsLocked(p, out)
}

// unpinLocked makes r's entry and input droppable, its end written. The
// caller holds s.mu.
func (s *Local) unpinLocked(r *openPermission) { s.transcript.pin(r.seq, false) }

// strictCanonical is input's canonical JSON (numbers as written, objects'
// keys sorted, no insignificant space, no HTML escaping), false when input
// does not decode cleanly: not UTF-8, a lone surrogate escape, a key twice,
// not one JSON value. So a digest is only ever of one form.
func strictCanonical(input []byte) ([]byte, bool) {
	if !utf8.Valid(input) || loneSurrogate(input) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil, false
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), true
}

// maxInputDepth bounds the nesting of a request's input.
const maxInputDepth = 256

var errNotClean = errors.New("session: the input does not decode cleanly")

// strictValue reads one JSON value, refusing a key twice in an object.
func strictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxInputDepth {
		return nil, errNotClean
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errNotClean
				}
				if _, twice := m[k]; twice {
					return nil, errNotClean
				}
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			l := []any{}
			for dec.More() {
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return l, nil
		}
		return nil, errNotClean
	}
	return tok, nil
}

// loneSurrogate reports whether input holds a \u escape of a surrogate
// that is not one half of a pair: a decoder would take it for U+FFFD.
func loneSurrogate(input []byte) bool {
	hexAt := func(i int) (rune, bool) {
		if i+6 > len(input) || input[i] != '\\' || input[i+1] != 'u' {
			return 0, false
		}
		var r rune
		for _, c := range input[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				r = r<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				r = r<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				r = r<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	for i := 0; i < len(input); i++ {
		if input[i] != '\\' {
			continue
		}
		if i+1 < len(input) && input[i+1] != 'u' {
			i++
			continue
		}
		r, ok := hexAt(i)
		if !ok {
			continue
		}
		switch {
		case r >= 0xD800 && r <= 0xDBFF:
			lo, ok := hexAt(i + 6)
			if !ok || lo < 0xDC00 || lo > 0xDFFF {
				return true
			}
			i += 11
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		default:
			i += 5
		}
	}
	return false
}

// digestOf is SHA-256 over the tool's name, a newline and the canonical
// input, lowercase hex (inputDigest's, of an input already canonical).
func digestOf(tool string, canon []byte) string { return inputDigest(tool, canon) }

// flooredLocked is Conductor's floor (design 7): a file tool's path under
// the server's deny list, or a shell command whose text names a path of
// it as commandNamesDenied says. A command that computes a path (a
// variable, a cd, a script, a glob) is not caught: the floor does not
// promise it. The caller holds s.mu.
func (s *Local) flooredLocked(tool string, input []byte) bool {
	deny := s.fileDeny()
	if len(deny) == 0 {
		return false
	}
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return false
	}
	switch plainOf(tool) {
	case "command":
		return commandNamesDenied(commandText(fields), deny, s.info.Cwd)
	case "file", "read":
		for _, k := range []string{"file_path", "notebook_path", "path", "pattern"} {
			v, ok := fields[k].(string)
			if !ok || v == "" {
				continue
			}
			if k == "pattern" {
				// A pattern is a path only for the tool whose pattern is
				// one (Glob): Grep's is a regular expression.
				if !strings.EqualFold(tool, "glob") {
					continue
				}
				v = literalPrefix(v)
				if v == "" || !filepath.IsAbs(v) {
					continue
				}
			}
			if s.pathDenied(v, deny) {
				return true
			}
		}
	}
	return false
}

// commandText is a shell call's command line: Claude Code's "command",
// or an argv joined.
func commandText(fields map[string]any) string {
	for _, k := range []string{"command", "cmd"} {
		switch v := fields[k].(type) {
		case string:
			return v
		case []any:
			var parts []string
			for _, a := range v {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// literalPrefix is a glob pattern up to its first wildcard.
func literalPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*?[{"); i >= 0 {
		pattern = pattern[:i]
	}
	return pattern
}

// pathDenied reports whether path, relative to the session's folder when it
// is not absolute, is under the deny list, its links followed.
func (s *Local) pathDenied(path string, deny []string) bool {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		if s.info.Cwd == "" {
			return false
		}
		path = filepath.Join(s.info.Cwd, path)
	}
	path = filepath.Clean(path)
	if real, err := evalExisting(path); err == nil && insideAny(real, deny) {
		return true
	}
	return insideAny(path, deny)
}

// CommandNamesDenied reports whether a command's text names a path of the
// deny list, for conductor notify --protocol-floor, which knows no folder:
// commandNamesDenied with none.
func CommandNamesDenied(cmd string, deny []string) bool { return commandNamesDenied(cmd, deny, "") }

// commandNamesDenied is the floor's judgement of a shell command's text,
// and exactly this: the text is lowercased, its quotes (' and ") and
// backslashes taken out, and every "//" and "/./" made one "/"; then it
// names a path of the deny list when it holds the path as the list spells
// it, or with ~, $HOME or ${HOME} for the home folder before the rest, or,
// with cwd (the session's folder), the path relative to cwd where the path
// is inside it, after the start of the text, a space or one of =:(<>|;&.
// A name entry (dir/*name*) is named by its name and its directory. What is
// not judged a path, and is asked like any other call (or runs unasked in
// a yolo session): a variable other than HOME, a glob, a cd, a script, a
// path computed any other way, a path relative to another folder.
func commandNamesDenied(cmd string, deny []string, cwd string) bool {
	if cmd == "" {
		return false
	}
	text := strings.ToLower(strings.NewReplacer(`"`, "", "'", "", `\`, "").Replace(cmd))
	for {
		next := strings.ReplaceAll(strings.ReplaceAll(text, "/./", "/"), "//", "/")
		if next == text {
			break
		}
		text = next
	}
	home, _ := os.UserHomeDir()
	forms := func(p string) []string {
		out := []string{p}
		if home != "" && home != "/" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
			rest := strings.TrimPrefix(p, home)
			out = append(out, "~"+rest, "$home"+rest, "${home}"+rest)
		}
		return out
	}
	relative := func(p string) string {
		if cwd == "" {
			return ""
		}
		r, err := filepath.Rel(cwd, p)
		if err != nil || r == "." || !filepath.IsLocal(r) {
			return ""
		}
		return strings.ToLower(filepath.ToSlash(r))
	}
	// atWord says the text holds f where a word begins.
	atWord := func(f string) bool {
		for i := 0; ; {
			j := strings.Index(text[i:], f)
			if j < 0 {
				return false
			}
			at := i + j
			if at == 0 || strings.ContainsRune(" \t\n=:(<>|;&", rune(text[at-1])) || strings.HasSuffix(text[:at], "./") && (at == 2 || strings.ContainsRune(" \t\n=:(<>|;&", rune(text[at-3]))) {
				return true
			}
			i = at + 1
		}
	}
	names := func(p string) bool {
		for _, f := range forms(p) {
			if strings.Contains(text, strings.ToLower(f)) {
				return true
			}
		}
		if r := relative(p); r != "" && atWord(r) {
			return true
		}
		return false
	}
	for _, e := range deny {
		if e == "" {
			continue
		}
		base := filepath.Base(e)
		if len(base) > 2 && strings.HasPrefix(base, "*") && strings.HasSuffix(base, "*") {
			core := strings.ToLower(strings.Trim(base, "*"))
			if core != "" && strings.Contains(text, core) && (names(filepath.Dir(e)) || relative(filepath.Dir(e)) == "" && cwd != "" && sameDir(cwd, filepath.Dir(e))) {
				return true
			}
			continue
		}
		if names(e) {
			return true
		}
	}
	return false
}

// sameDir says a and b are one folder, as cleaned paths.
func sameDir(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

// SummaryCutMark ends a request's summary that was cut: the mark a
// question's words cut to fit end with (shownWords).
const SummaryCutMark = "…"

// requestSummary is a request's summary: its input as compact JSON, its
// control characters dropped and every run of white space one space, then
// cut to MaxEntrySummary with SummaryCutMark at its end when cut, so a
// command padded with spaces never reads as all there is. The whole input
// is the request's part.
func requestSummary(input []byte) string {
	words := strings.Join(strings.Fields(strings.Map(dropControl, compactJSON(input))), " ")
	if len(words) <= MaxEntrySummary {
		return words
	}
	return cutAt(words, MaxEntrySummary-len(SummaryCutMark)) + SummaryCutMark
}
