package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Chat beside the terminal (docs/protocol.md, Chat): the people on a session
// talk to each other over the connection the terminal takes, so it works
// wherever a share link works. Every viewer may post, whatever its role; a
// controller may also have a message typed into the agent (ChatSend), which
// leaves a marker in the chat. The session keeps the last MaxChat messages
// and replays them to a new viewer; they go with the session. A chat line
// is not an activity entry: it has its own ring, frames and hook (OnChat).

// MaxChat is how many messages a session keeps; MaxRunChat how many a run's
// chat keeps (ChatRoom), which goes into the run's record.
const (
	MaxChat    = 200
	MaxRunChat = 500
)

var (
	ErrChatEmpty       = errors.New("chat: nothing to say")
	ErrChatTooLong     = errors.New("chat: message too long")
	ErrChatRateLimited = errors.New("chat: too many messages")
	ErrChatUnknownRef  = errors.New("chat: no such message")
	ErrChatBadScope    = errors.New("chat: no such chat here")
	ErrNoRunChat       = errors.New("chat: this session is in no run")
)

// Why a run chat's send was not typed into the member it names
// (ErrNotSent.Reason); a broadcast skips a member for the same reasons.
const (
	NotSentNeedsInput = "needs_input" // its session waits on a prompt, which the text must not answer
	NotSentNotRunning = "not_running" // it is not running: no session yet, its prompt not typed yet, or ended
	NotSentUnknown    = "unknown"     // no member of the run has the name
	NotSentNoEnter    = "no_enter"    // typed, but a prompt came up before its Enter: the line waits in its input
)

// ErrNotSent is a run chat's send that was not typed into the member it
// names; Reason says why (NotSentNeedsInput and the rest).
type ErrNotSent struct{ Reason string }

func (e ErrNotSent) Error() string { return "chat: not sent: " + e.Reason }

// ChatBy is who a message is from: the viewer's subscription, as the roster names it.
type ChatBy struct {
	ID   string
	Name string
	Role Role
}

// ChatMessage is one message of a chat (proto.ChatMessage, with the time as
// a time). A question (proto.ChatKindQuestion) carries the agent's choices.
type ChatMessage struct {
	ID      string
	At      time.Time
	Scope   string
	Kind    string
	By      ChatBy
	Text    string
	Ref     string
	To      string
	On      string
	Event   string
	Nonce   string
	Options []Option
	// Quote is the lines of a file the message is about (F7), cleaned and bounded.
	Quote *proto.ChatQuote
}

// RoleAgent is the By.Role of a question in the chat: the agent, no viewer
// (Role.Valid is for subscriptions and stays view or control).
const RoleAgent Role = proto.ChatRoleAgent

// ChatToProto encodes m as the `chat` message of docs/protocol.md.
func ChatToProto(m ChatMessage) proto.ChatMessage {
	out := proto.ChatMessage{T: proto.CtlChat, ID: m.ID, At: m.At.UTC().Format(time.RFC3339Nano), Scope: m.Scope, Kind: m.Kind,
		By: proto.ChatBy{ID: m.By.ID, Name: m.By.Name, Role: string(m.By.Role)}, Text: m.Text, Ref: m.Ref, To: m.To, On: m.On, Event: m.Event, Nonce: m.Nonce}
	for _, o := range m.Options {
		out.Options = append(out.Options, proto.AttentionOption{Label: o.Label, Input: o.Input, Mode: o.Mode})
	}
	if m.Quote != nil {
		q := *m.Quote
		q.Lines = append([]string(nil), m.Quote.Lines...)
		out.Quote = &q
	}
	return out
}

// cleanQuote bounds a quote (F7): a path (at most MaxQuotePath bytes), a
// range from 1 with To at least From, at most MaxQuoteLines lines of at most
// MaxQuoteLine bytes each, control characters but tabs dropped. Nil when
// there is no path or no range.
func cleanQuote(q *proto.ChatQuote) *proto.ChatQuote {
	if q == nil {
		return nil
	}
	path := strings.TrimSpace(CleanChatText(q.Path))
	if path == "" || q.From < 1 || q.To < q.From || q.To-q.From > 1_000_000 {
		return nil
	}
	out := &proto.ChatQuote{Path: truncateRunes(path, proto.MaxQuotePath), From: q.From, To: q.To, Cut: q.Cut}
	for i, l := range q.Lines {
		if i == proto.MaxQuoteLines {
			out.Cut = true
			break
		}
		var b strings.Builder
		for _, r := range strings.ToValidUTF8(l, "\uFFFD") {
			if r == '\t' || !unicode.IsControl(r) {
				b.WriteRune(r)
			}
		}
		line := b.String()
		if len(line) > proto.MaxQuoteLine {
			line = truncateRunes(line, proto.MaxQuoteLine)
		}
		out.Lines = append(out.Lines, line)
	}
	if len(q.Lines) > proto.MaxQuoteLines {
		out.Cut = true
	}
	return out
}

// fitQuote leaves out a quote's last lines until the message's frame fits
// MaxControl: the text alone always does (MaxChatText), a full quote beside
// a full text of quotes and backslashes may not.
func fitQuote(m *ChatMessage) {
	for m.Quote != nil && len(m.Quote.Lines) > 0 && len(proto.MustControlRaw(ChatToProto(*m))) > proto.MaxControl {
		m.Quote.Lines = m.Quote.Lines[:len(m.Quote.Lines)-1]
		m.Quote.Cut = true
	}
}

// AgentText is what the agent is given for a message: the text alone, or,
// for a message about lines of a file (F7), where they are as the agent
// reads a location (internal/api/users.go:14-16), the lines quoted, then the
// words; at most MaxSubmit bytes, quoted lines left out past it.
func AgentText(m ChatMessage) string {
	if m.Quote == nil {
		return m.Text
	}
	loc := fmt.Sprintf("%s:%d", m.Quote.Path, m.Quote.From)
	if m.Quote.To > m.Quote.From {
		loc = fmt.Sprintf("%s-%d", loc, m.Quote.To)
	}
	head := loc
	var quoted []string
	for _, l := range m.Quote.Lines {
		quoted = append(quoted, "> "+l)
	}
	for {
		parts := []string{head}
		if len(quoted) > 0 {
			parts = append(parts, strings.Join(quoted, "\n"))
		}
		if m.Text != "" {
			parts = append(parts, m.Text)
		}
		out := strings.Join(parts, "\n")
		if len(out) <= proto.MaxSubmit || len(quoted) == 0 {
			return truncateRunes(out, proto.MaxSubmit)
		}
		quoted = quoted[:len(quoted)-1]
	}
}

// askInChat keeps the agent's question in the session's chat, as the session
// goes needs_input: its message and choices, from the agent. The caller
// holds s.mu; the line and the session's id come back for the hook and for
// the run's chat, which take it outside the lock.
func (s *Local) askInChat(att Attention) (ChatMessage, string) {
	text := att.Message
	if text == "" {
		text = "Waiting for input"
	}
	at := time.Now().UTC()
	if att.Since != nil {
		at = *att.Since
	}
	// The question's line is the question's own id (Attention.ID), so that a
	// choice clicked in the chat names the question it was shown for.
	id := att.ID
	if id == "" {
		id = NewID()
	}
	q := ChatMessage{ID: id, At: at, Scope: proto.ChatScopeSession, Kind: proto.ChatKindQuestion, By: ChatBy{ID: "agent", Name: s.info.AgentID, Role: RoleAgent}, Text: text, Options: append([]Option(nil), att.Options...)}
	s.question = q.ID
	if s.askedInChat == nil {
		s.askedInChat = map[string]struct{}{}
	}
	s.askedInChat[q.ID] = struct{}{}
	return q, s.keepChat(q)
}

// answeredInChat keeps the line that says the question named id was
// answered, by whom (Conductor itself when by is ""): it refers to that
// question's own line in the chat (Ref: its id, the line's), not to the
// one asked last (another question may have shown over a held turn since,
// and gone, the first restored by the hold's end). The caller holds s.mu;
// nothing when no question was asked in the chat.
func (s *Local) answeredInChat(id, by, byName, answer string) (ChatMessage, string, bool) {
	ref := id
	if ref == "" {
		ref = s.question
	}
	if ref == "" {
		return ChatMessage{}, "", false
	}
	if byName == "" {
		byName = "Conductor"
	}
	m := ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindSystem, Event: "answered", Ref: ref, Text: answer, By: ChatBy{ID: by, Name: byName, Role: RoleControl}}
	if s.question == ref {
		s.question = ""
	}
	delete(s.askedInChat, ref)
	return m, s.keepChat(m), true
}

// answerWords is what the chat's answered line says the reply answered
// the question with (#364): the label of the choice whose keys it typed
// (keys), for any question. A line typed, at a prompt on the screen or at
// the agent's own question, is never repeated: the agent may ask for a
// credential (the skill says to), the terminal may not echo it, and the
// chat goes to every viewer; the browser that typed it shows it to its
// own person. A digit that is no choice's keys says nothing (the choices
// shown need not be the screen's numbering), as do keys that say nothing
// readable: the line says only who answered then.
func answerWords(options []Option, keys []byte) string {
	k := string(keys)
	for _, o := range options {
		if o.Input != "" && o.Input == k {
			return o.Label
		}
	}
	return ""
}

// The reasons a question's line in the chat gives for leaving without an
// answer through Conductor (#358): keys typed in the terminal answered it,
// or the agent moved on.
const (
	ChatLeftTerminal = "terminal"
	ChatLeftAgent    = "agent"
)

// leftInChatLocked keeps the line that says the question named id, asked in
// the chat, left without an answer through Conductor: answered by keys typed
// in the terminal (reason ChatLeftTerminal) or withdrawn as the agent moved
// on (ChatLeftAgent). The chat's card then offers its choices no longer
// (#358). Nothing for a question the chat never showed. The caller holds
// s.mu; the line waits in chatLeft for notifyChange, which tells it outside
// the lock, after the ring has it and before the next question's line.
func (s *Local) leftInChatLocked(id, reason string) {
	if id == "" {
		return
	}
	if s.own.ID == id {
		// The agent's own question, kept: shown again at its turn's done, its
		// card stays answerable; dropOwnLocked says when it goes for good.
		return
	}
	if _, asked := s.askedInChat[id]; !asked {
		return
	}
	delete(s.askedInChat, id)
	if s.question == id {
		s.question = ""
	}
	m := ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindSystem, Event: "withdrawn", Ref: id, Text: reason, By: ChatBy{ID: "", Name: "Conductor", Role: RoleControl}}
	key := s.keepChat(m)
	s.chatLeft = append(s.chatLeft, chatLeft{key: key, m: m})
}

// leftReasonLocked is why a question would leave without an answer through
// Conductor as things stand: keys typed in the terminal reached it, or the
// agent moved on. The caller holds s.mu.
func (s *Local) leftReasonLocked(id string) string {
	if s.consumed == id {
		return ChatLeftTerminal
	}
	return ChatLeftAgent
}

// chatLeft is a withdrawn line kept under the lock, told by tellChatLeft.
type chatLeft struct {
	key string
	m   ChatMessage
}

// tellChatLeft tells the withdrawn lines kept since the last time, to the
// session's viewers and its run's chat. The caller does not hold s.mu.
func (s *Local) tellChatLeft() {
	s.mu.Lock()
	left := s.chatLeft
	s.chatLeft = nil
	s.mu.Unlock()
	for _, l := range left {
		s.chatHook(l.key, l.m)
		s.tellRun(l.m)
	}
}

// tellRun copies a line of the session's chat into its run's chat, on this
// member, outside the session's lock.
func (s *Local) tellRun(m ChatMessage) {
	room := s.opts.RunChat
	if room == nil {
		return
	}
	m.Scope = proto.ChatScopeRun
	m.On = room.nameOf(s)
	room.post(m)
}

// CleanChatText is a message's text as kept: valid UTF-8, line breaks as
// \n, every other control character but a tab dropped, trimmed.
func CleanChatText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// cleanNonce keeps a post's own id as printable ASCII of at most MaxChatNonce bytes.
func cleanNonce(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > ' ' && r < 0x7f && b.Len() < proto.MaxChatNonce {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chatRing keeps the last limit messages, MaxChat when zero. The owner's
// lock guards it.
type chatRing struct {
	buf   []ChatMessage
	limit int
}

func (r *chatRing) add(m ChatMessage) {
	limit := r.limit
	if limit <= 0 {
		limit = MaxChat
	}
	r.buf = append(r.buf, m)
	if len(r.buf) > limit {
		r.buf = r.buf[len(r.buf)-limit:]
	}
}

func (r *chatRing) find(id string) (ChatMessage, bool) {
	for i := len(r.buf) - 1; i >= 0; i-- {
		if r.buf[i].ID == id {
			return r.buf[i], true
		}
	}
	return ChatMessage{}, false
}

func (r *chatRing) snapshot() []ChatMessage {
	return append([]ChatMessage(nil), r.buf...)
}

// chatReplayFrames packs the newest messages that fit proto.ChatReplayBytes
// into chat_history frames of at most proto.MaxControl each, oldest first,
// More set on every frame but the last. None for no messages.
func chatReplayFrames(scope string, msgs []ChatMessage) [][]byte {
	budget := proto.ChatReplayBytes
	start := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		n := len(proto.MustControlRaw(ChatToProto(msgs[i])))
		if n > budget {
			break
		}
		budget -= n
		start = i
	}
	kept := msgs[start:]
	if len(kept) == 0 {
		return nil
	}
	var batches [][]proto.ChatMessage
	var batch []proto.ChatMessage
	for _, m := range kept {
		next := append(batch, ChatToProto(m))
		if len(batch) > 0 && len(proto.MustControlRaw(proto.ChatHistory{T: proto.CtlChatHistory, Scope: scope, Messages: next, More: true})) > proto.MaxControl {
			batches = append(batches, batch)
			next = []proto.ChatMessage{ChatToProto(m)}
		}
		batch = next
	}
	batches = append(batches, batch)
	frames := make([][]byte, 0, len(batches))
	for i, b := range batches {
		frames = append(frames, proto.MustControlRaw(proto.ChatHistory{T: proto.CtlChatHistory, Scope: scope, Messages: b, More: i < len(batches)-1}))
	}
	return frames
}

// chatBucket bounds one connection's posts: proto.ChatBurst at once, then
// proto.ChatRatePerSecond, unless rate and burst say otherwise (the Neovim
// keys' bucket, NvimInput). A clock that steps back earns nothing.
type chatBucket struct {
	mu          sync.Mutex
	tokens      float64
	last        time.Time
	rate, burst float64
}

func (b *chatBucket) take(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	rate, burst := b.rate, b.burst
	if rate == 0 {
		rate, burst = proto.ChatRatePerSecond, proto.ChatBurst
	}
	switch {
	case b.last.IsZero():
		b.tokens, b.last = burst, now
	case now.After(b.last):
		b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func chatBy(sub *Subscription) ChatBy {
	return ChatBy{ID: sub.ID, Name: sub.Name, Role: sub.Role}
}

// Chat keeps and sends a viewer's post to every viewer, and returns it as
// kept. Every role may post. With To ("agent", or a member of the run in
// scope "run"; a controller's) the caller goes on with ChatSend on the
// message. An ended session takes no post of its own; the run's chat stays
// open while the run is kept. A post of a connection whose credential no
// longer stands (a link revoked or expired) is refused with why (revoked),
// asked after the role, and nothing of it is kept or sent.
func (s *Local) Chat(sub *Subscription, post proto.ChatPost) (ChatMessage, error) {
	if !sub.chat.take(time.Now()) {
		return ChatMessage{}, ErrChatRateLimited
	}
	scope := post.Scope
	if scope == "" {
		scope = proto.ChatScopeSession
	}
	var room *ChatRoom
	switch scope {
	case proto.ChatScopeSession:
		if post.To != "" && post.To != proto.ChatToAgent {
			return ChatMessage{}, ErrChatBadScope
		}
	case proto.ChatScopeRun:
		if room = s.opts.RunChat; room == nil {
			return ChatMessage{}, ErrNoRunChat
		}
	default:
		return ChatMessage{}, ErrChatBadScope
	}
	if post.To != "" && sub.Role != RoleControl {
		return ChatMessage{}, ErrReadOnly
	}
	if err := revoked(sub); err != nil {
		return ChatMessage{}, err
	}
	text := CleanChatText(post.Text)
	quote := cleanQuote(post.Quote)
	if text == "" && quote == nil {
		return ChatMessage{}, ErrChatEmpty
	}
	if len(text) > proto.MaxChatText {
		return ChatMessage{}, ErrChatTooLong
	}
	m := ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: scope, Kind: proto.ChatKindMessage, By: chatBy(sub), Text: text, Nonce: cleanNonce(post.Nonce), Quote: quote}
	fitQuote(&m)
	if room != nil {
		// The member the sender looks at, as the client says it, kept when it is one.
		if room.hasMember(post.On) {
			m.On = post.On
		}
		room.post(m)
		return m, nil
	}
	s.mu.Lock()
	if s.info.Status.Ended() {
		s.mu.Unlock()
		return ChatMessage{}, ErrSessionEnded
	}
	id := s.keepChat(m)
	s.mu.Unlock()
	s.chatHook(id, m)
	return m, nil
}

// ChatSend types the text of the kept message Ref into the agent, as a reply
// box does (Submit), and marks it in the chat; in scope "run", into the
// member To of the run as a broadcast does (nothing while it waits on a
// prompt), with the marker in the run's chat, or ErrNotSent says why not.
// Controllers only; one whose credential no longer stands is refused with
// why (revoked), as it is taken, and its text is held to the credential
// again as it is typed, as any submission's is.
func (s *Local) ChatSend(ctx context.Context, sub *Subscription, send proto.ChatSend) error {
	if sub.Role != RoleControl {
		return ErrReadOnly
	}
	if err := revoked(sub); err != nil {
		return err
	}
	switch send.Scope {
	case "", proto.ChatScopeSession:
	case proto.ChatScopeRun:
		room := s.opts.RunChat
		if room == nil {
			return ErrNoRunChat
		}
		ref, ok := room.find(send.Ref)
		if !ok || ref.Kind != proto.ChatKindMessage {
			return ErrChatUnknownRef
		}
		if send.To == "" {
			return ErrChatBadScope
		}
		if err := room.sendTo(ctx, send.To, AgentText(ref), sub.Name, sub); err != nil {
			return err
		}
		room.post(ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeRun, Kind: proto.ChatKindSentToAgent, By: chatBy(sub), Ref: ref.ID, To: send.To})
		return nil
	default:
		return ErrChatBadScope
	}
	s.mu.Lock()
	ref, ok := s.chat.find(send.Ref)
	s.mu.Unlock()
	if !ok || ref.Kind != proto.ChatKindMessage {
		return ErrChatUnknownRef
	}
	if _, err := s.Submit(ctx, Submission{Text: AgentText(ref), By: sub}); err != nil && !errors.Is(err, ErrSubmitPending) {
		return err
	}
	m := ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindSentToAgent, By: chatBy(sub), Ref: ref.ID}
	s.mu.Lock()
	id := s.keepChat(m)
	s.mu.Unlock()
	s.chatHook(id, m)
	return nil
}

// RunChat is the chat of the run this session is a member of, or nil.
func (s *Local) RunChat() *ChatRoom { return s.opts.RunChat }

// LeaveRunChat takes the session out of its run's chat: it left the server
// (the registry's OnRemove). Its viewers are gone with it.
func (s *Local) LeaveRunChat() {
	if room := s.opts.RunChat; room != nil {
		room.Leave(s)
	}
}

// ChatHistory is the kept chat, oldest first.
func (s *Local) ChatHistory() []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chat.snapshot()
}

// keepChat adds m to the ring and sends it to every viewer, in one critical
// section (as record does), so a viewer attaching meanwhile finds it in its
// replay or receives it, never both. The caller holds s.mu; the session's
// id comes back for the hook, which runs without the lock.
func (s *Local) keepChat(m ChatMessage) string {
	s.chat.add(m)
	s.hub.Broadcast(proto.MustControlRaw(ChatToProto(m)))
	return s.info.ID
}

func (s *Local) chatHook(id string, m ChatMessage) {
	if s.opts.OnChat != nil {
		s.opts.OnChat(id, m)
	}
}

// chatSystem keeps a join or leave line about sub, unless another connection
// of the same name is there already (a join) or still (a leave): a person on
// two tabs joins and leaves once. The caller holds s.mu; the line, if any,
// comes back for the hook.
func (s *Local) chatSystem(sub *Subscription, event string) (ChatMessage, bool) {
	if s.hub.namePresent(sub.Name, sub.ID) {
		return ChatMessage{}, false
	}
	m := ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeSession, Kind: proto.ChatKindSystem, By: chatBy(sub), Event: event}
	s.keepChat(m)
	return m, true
}

// ChatErrorFrame is the error frame a chat post or send gets for err, naming
// the post's nonce or the send's ref as its requestId: revoked or expired
// for a link that no longer opens the session (LinkRefusal).
func ChatErrorFrame(err error, requestID string) []byte {
	code, msg := "input_failed", "could not be delivered"
	var notSent ErrNotSent
	if c, m, ok := LinkRefusal(err); ok {
		return proto.MustControl(proto.ErrorMsg{T: proto.CtlError, Code: c, Message: m, RequestID: requestID})
	}
	switch {
	case errors.As(err, &notSent):
		code, msg = proto.ErrCodeNotSent, notSent.Reason
	case errors.Is(err, ErrTrustQuestion):
		code, msg = proto.ErrCodeNotSent, TrustQuestionWords
	case errors.Is(err, ErrQuestion):
		code, msg = proto.ErrCodeNotSent, QuestionWords
	case errors.Is(err, ErrChatRateLimited):
		code, msg = proto.ErrCodeTooManyRequests, "too many messages at once"
	case errors.Is(err, ErrReadOnly):
		code, msg = proto.ErrCodeReadOnly, "this link is view-only"
	case errors.Is(err, ErrSessionEnded):
		code, msg = proto.ErrCodeSessionEnded, "the session has ended"
	case errors.Is(err, ErrChatEmpty), errors.Is(err, ErrChatTooLong), errors.Is(err, ErrChatUnknownRef), errors.Is(err, ErrChatBadScope), errors.Is(err, ErrNoRunChat):
		code, msg = proto.ErrCodeBadFrame, err.Error()
	}
	return proto.MustControl(proto.ErrorMsg{T: proto.CtlError, Code: code, Message: msg, RequestID: requestID})
}

// ChatRoom is a run's chat (docs/protocol.md, Chat): one thread across every
// member session, kept by the run engine's run and gone into its record,
// read and posted over any member's connection with scope "run", so a run
// published through a switchyard, or a guest on a run link, has it too. A
// post reaches every member's viewers; the people on any member make its
// roster (chat_roster). A controller's send types a kept message into the
// member it names, as a broadcast does: nothing while that member waits on
// a prompt. Lock order: a member's lock, then the room's.
type ChatRoom struct {
	mu      sync.Mutex
	members []roomMember
	ring    chatRing
	// onPost is told of every message kept, outside the lock; running says
	// whether the named member runs (its prompt typed), for sendTo.
	onPost  func(m ChatMessage)
	running func(member string) bool
}

type roomMember struct {
	l    *Local
	name string
}

// NewChatRoom makes an empty room. onPost, when set, is called with each
// message kept, outside any lock, on the posting goroutine; running, when
// set, says whether a member is running (a send to one that is not is
// NotSentNotRunning).
func NewChatRoom(onPost func(m ChatMessage), running func(member string) bool) *ChatRoom {
	return &ChatRoom{ring: chatRing{limit: MaxRunChat}, onPost: onPost, running: running}
}

// Join adds l as the member name: its viewers read and post the run's chat
// from now on, and one attaching later is replayed it. The session must be
// made with the room (Options.RunChat).
func (r *ChatRoom) Join(l *Local, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members = append(r.members, roomMember{l: l, name: name})
}

// Leave takes l out of the room: a session gone from the server.
func (r *ChatRoom) Leave(l *Local) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members = slices.DeleteFunc(r.members, func(m roomMember) bool { return m.l == l })
}

// History is the kept chat, oldest first.
func (r *ChatRoom) History() []ChatMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ring.snapshot()
}

// readySent, when set, runs as an attach has sent its client ready, before
// the client is on the session's hub: tests read a run's roster there.
var readySent func()

// roomReplayed, when set, runs as a client of a run's member has been sent
// the run chat's replay, before it is on the session's hub: tests post to
// the run there.
var roomReplayed func()

// attach sends sub the kept chat and puts it on its session's hub, under
// the room's lock, which post keeps a message under: a message posted
// meanwhile is in the replay, or is sent once sub is on the hub. Called with
// the session's lock held.
func (r *ChatRoom) attach(sub *Subscription, hub *Hub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range chatReplayFrames(proto.ChatScopeRun, r.ring.snapshot()) {
		sub.send(f)
	}
	if roomReplayed != nil {
		roomReplayed()
	}
	hub.add(sub)
}

func (r *ChatRoom) find(id string) (ChatMessage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ring.find(id)
}

func (r *ChatRoom) snapshotMembers() []roomMember {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.members)
}

func (r *ChatRoom) memberNamed(name string) (*Local, bool) {
	for _, m := range r.snapshotMembers() {
		if m.name == name {
			return m.l, true
		}
	}
	return nil, false
}

func (r *ChatRoom) hasMember(name string) bool {
	_, ok := r.memberNamed(name)
	return ok
}

// nameOf is the member name l joined as, "" when it is no member.
func (r *ChatRoom) nameOf(l *Local) string {
	for _, m := range r.snapshotMembers() {
		if m.l == l {
			return m.name
		}
	}
	return ""
}

// post keeps m and sends it to every member's viewers, then tells onPost.
func (r *ChatRoom) post(m ChatMessage) {
	r.mu.Lock()
	r.ring.add(m)
	members := slices.Clone(r.members)
	r.mu.Unlock()
	frame := proto.MustControlRaw(ChatToProto(m))
	for _, mem := range members {
		mem.l.hub.Broadcast(frame)
	}
	if r.onPost != nil {
		r.onPost(m)
	}
}

// sendTo types text into the member name as a broadcast does (nothing
// while its session waits on a prompt), recorded as byName, for the
// connection from (Submission.From); an ErrNotSent says why it was not,
// unless from's credential no longer stands, which is said as it is
// (revoked: ErrRevoked, ErrExpired or a kind of either).
func (r *ChatRoom) sendTo(ctx context.Context, name, text, byName string, from *Subscription) error {
	l, ok := r.memberNamed(name)
	if !ok {
		return ErrNotSent{NotSentUnknown}
	}
	if r.running != nil && !r.running(name) {
		return ErrNotSent{NotSentNotRunning}
	}
	res, err := l.Submit(ctx, Submission{Text: text, ByName: byName, From: from, UnlessWaiting: true})
	switch {
	case errors.Is(err, ErrRevoked):
		// Withdrawn for the sender's link, its text typed or not: why.
		return err
	case errors.Is(err, ErrSubmitPending):
		// Accepted: entered as the program reads.
		return nil
	case err != nil && !res.Typed:
		return ErrNotSent{NotSentNotRunning}
	case err != nil:
		return ErrNotSent{NotSentNoEnter}
	case !res.Typed:
		return ErrNotSent{NotSentNeedsInput}
	case !res.Entered:
		return ErrNotSent{NotSentNoEnter}
	}
	return nil
}

// namePresent reports whether a live connection of the name other than
// except is on any member: the same person elsewhere on the run.
func (r *ChatRoom) namePresent(name, except string) bool {
	for _, m := range r.snapshotMembers() {
		if m.l.hub.namePresent(name, except) {
			return true
		}
	}
	return false
}

// system keeps a join or leave line about sub unless the same name is on
// the run elsewhere: a person on three members' tiles joins and leaves once.
func (r *ChatRoom) system(sub *Subscription, event string) {
	if r.namePresent(sub.Name, sub.ID) {
		return
	}
	r.post(ChatMessage{ID: NewID(), At: time.Now().UTC(), Scope: proto.ChatScopeRun, Kind: proto.ChatKindSystem, By: chatBy(sub), Event: event})
}

// Roster is who is on the run's chat: every live connection of any member,
// one row per name, with the member a person looks at (a connection that is
// not quiet) when they look at one; Count counts them all, List the first
// proto.MaxChatRoster.
func (r *ChatRoom) Roster() proto.ChatRoster {
	members := r.snapshotMembers()
	seen := map[string]bool{}
	out := proto.ChatRoster{T: proto.CtlChatRoster, Scope: proto.ChatScopeRun, List: []proto.ChatPerson{}}
	add := func(quiet bool) {
		for _, mem := range members {
			for _, v := range mem.l.hub.Roster() {
				if v.Quiet != quiet || seen[v.Name] {
					continue
				}
				seen[v.Name] = true
				out.Count++
				if len(out.List) < proto.MaxChatRoster {
					p := proto.ChatPerson{ID: v.ID, Name: v.Name, Role: v.Role}
					if !quiet {
						p.On = mem.name
					}
					out.List = append(out.List, p)
				}
			}
		}
	}
	add(false)
	add(true)
	return out
}

// broadcastRoster sends the roster to every member's viewers.
func (r *ChatRoom) broadcastRoster() {
	frame := proto.MustControl(r.Roster())
	for _, mem := range r.snapshotMembers() {
		mem.l.hub.Broadcast(frame)
	}
}
