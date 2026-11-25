package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/session/internal/way"
)

// A session's protocol way (docs/plans/chat-or-terminal.md, design 1): the
// agent's own documented non-interactive interface, a child with pipes
// (Child) that a driver (Protocol) speaks to, in place of the terminal. The
// session stays the one Local: its attention, questions, activity, people's
// chat and links are the same whichever way it runs. No constructor starts
// a session in this way: a move from a terminal session at rest enters it
// (the tests enter it through sessiontest.EnterProtocol).

// The protocol way's types, named in this package (their home is a package
// of their own that the session's test kit can implement them in).
type (
	Child             = way.Child
	Protocol          = way.Protocol
	ProtocolEvents    = way.ProtocolEvents
	Message           = way.Message
	ToolStep          = way.ToolStep
	ToolResult        = way.ToolResult
	FileChange        = way.FileChange
	Usage             = way.Usage
	PermissionRequest = way.PermissionRequest
	Verdict           = way.Verdict
	// ProtocolStart starts a protocol way's child and its driver on the
	// agent's conversation ("" for a new one).
	ProtocolStart = way.Start
)

// MaxProtocolLine bounds one line of a child's output (way.MaxProtocolLine).
const MaxProtocolLine = way.MaxProtocolLine

// ErrNoInterrupt is a driver's answer to Interrupt when its agent offers
// none it confirms: the session ends the child and starts it again on the
// same conversation.
var ErrNoInterrupt = way.ErrNoInterrupt

// A tool step's statuses (ToolResult.Status), and a file change's ops
// (FileChange.Op).
const (
	ToolOK          = way.ToolOK
	ToolError       = way.ToolError
	ToolDenied      = way.ToolDenied
	ToolInterrupted = way.ToolInterrupted
	ChangeCreate    = way.FileCreate
	ChangeEdit      = way.FileEdit
	ChangeDelete    = way.FileDelete
)

func init() {
	way.Enter = func(local any, start way.Start) error {
		s, ok := local.(*Local)
		if !ok || s == nil {
			return errors.New("session: only a *session.Local enters the protocol way")
		}
		return s.enterProtocol(context.Background(), start)
	}
}

// The ways a session runs (Local.Way).
const (
	WayTerminal = "terminal"
	WayProtocol = "protocol"
)

// Defaults and bounds of the protocol way.
const (
	// MaxQueuedMessages bounds the messages waiting to be written into a
	// protocol session: one more is refused (ErrTooManyQueued).
	MaxQueuedMessages = 16
	// AcceptWait is how long a message written waits for the agent's
	// acknowledgement before it is unconfirmed (Options.AcceptWait). It is
	// never written again.
	AcceptWait = 30 * time.Second
	// protocolReadyCap bounds a child's Begin.
	protocolReadyCap = 60 * time.Second
	// maxOpenSteps bounds the tool steps and streamed texts a session
	// follows at once: past it the oldest is no longer followed (its entry
	// stays as it was).
	maxOpenSteps = MaxTranscriptEntries
	// maxUnconfirmedKept bounds the unconfirmed messages kept for a late
	// acknowledgement.
	maxUnconfirmedKept = MaxQueuedMessages
	// maxPromptName bounds a prompt's byName, in runes.
	maxPromptName = 40
	// maxTurnMessage bounds the words a turn's done shows.
	maxTurnMessage = 200
)

// The words of the protocol way's refusals and answers (the plan's,
// design 13).
const (
	NotTerminalWords    = "This session is a chat: it has no terminal."
	NotProtocolWords    = "Stop is for a chat; use the agent's own key in its terminal."
	NoTurnWords         = "The agent is not working on anything."
	UnconfirmedWords    = "Sent, not yet confirmed."
	QueuedWords         = "Sends when the turn ends."
	TooManyQueuedWords  = "Too many messages are waiting."
	PermissionCardWords = "Answer this request with its own buttons."

	// What a permission request's entry says once it is no longer open,
	// and what the agent is told with a deny.
	AnsweredByAgentWords   = "Answered by the agent's own rules."
	TurnStoppedWords       = "The turn was stopped."
	TooManyRequestsWords   = "Too many requests are waiting; this one was denied."
	EndedBeforeAnswerWords = "The session ended before anyone answered."

	// The notices the session writes itself.
	RestartedWords    = "The agent was started again to stop its turn."
	RestartFailWords  = "The agent could not be started again; the session ended."
	UnreadableWords   = "The agent sent something Conductor cannot read; the session ended."
	noticeRestarted   = "restarted"
	noticeRestartFail = "restart_failed"
	noticeUnreadable  = "unreadable"
)

var (
	// ErrNotTerminal refuses a terminal's call (keys, a resize) on a
	// session that runs in the protocol way: nothing is written.
	ErrNotTerminal error = &wayWords{NotTerminalWords}
	// ErrNotProtocol refuses a protocol way's call (Interrupt) on a
	// session that runs in its terminal.
	ErrNotProtocol error = &wayWords{NotProtocolWords}
	// ErrNoTurn refuses an interrupt while no turn runs.
	ErrNoTurn error = &wayWords{NoTurnWords}
	// ErrTooManyQueued refuses a message while MaxQueuedMessages wait.
	ErrTooManyQueued error = &wayWords{TooManyQueuedWords}
	// ErrUnconfirmed is a message's word when the agent did not
	// acknowledge it within AcceptWait (Submission.Finished): it was
	// written once and is never written again.
	ErrUnconfirmed error = &wayWords{UnconfirmedWords}
	// ErrPermissionCard refuses a line or an answer naming an open
	// permission request: it is answered with its own buttons alone.
	ErrPermissionCard error = &wayWords{PermissionCardWords}
)

// wayWords is an error whose words are a person's to read, one sentence
// as the plan gives it (design 13).
type wayWords struct{ words string }

func (e *wayWords) Error() string { return e.words }

// protocolWay is a session's state in the protocol way. Its fields are
// guarded by the session's lock.
type protocolWay struct {
	start    ProtocolStart
	name     string
	verbatim bool
	// gen counts the children the way started: an event from a child
	// before the current one is dropped.
	gen    uint64
	child  Child
	driver Protocol
	// readDone is closed as the current child's read returns.
	readDone chan struct{}
	// ctx ends with the session: what the way writes waits no longer.
	ctx    context.Context
	cancel context.CancelFunc

	// ready is closed once the first child's Begin returned nil.
	ready   chan struct{}
	isReady bool
	// readEnded says the first child's output ended before it was ready.
	readEnded  bool
	restarting bool
	stopping   bool
	// restartAtEnd says the driver asked for its child to be started again
	// at the turn's end (RestartProtocol), and restartCode and restartWords
	// are the notice that restart writes ("" for the interrupt's).
	restartAtEnd              bool
	restartCode, restartWords string

	// turns is Conductor's count of turns; inTurn says one runs (from
	// TurnStarted to TurnEnded), accepted that a message was taken since
	// the last turn's end, turnAt when the turn began (the session's
	// clock), and lastText the seq of its last words.
	turns    uint32
	inTurn   bool
	accepted bool
	turnAt   time.Time
	lastText uint64
	// ended says a turn has ended: the conversation can be resumed.
	ended bool

	// The messages: written and waiting for the agent's acknowledgement
	// (inflight), waiting to be written (queue), being written (sending),
	// and the last unconfirmed, by ref, for a late acknowledgement.
	inflight    *queuedMessage
	queue       []*queuedMessage
	sending     bool
	unconfirmed []*queuedMessage
	// kick wakes the sender.
	kick chan struct{}

	background bool
	// lastEvent is when the driver last handed an event over (wall clock).
	lastEvent time.Time

	// What the stream has open: tool steps and streamed texts by ref, in
	// the order they opened, and the permission requests, oldest first.
	tools     map[string]*openTool
	toolOrder []string
	texts     map[string]*openText
	textOrder []string
	perms     []*openPermission

	// The permission requests' own state (permission.go): requests counts
	// the RequestPermission calls under way (open, waiting or being
	// delivered) and waiting those that wait for their call's
	// announcement, woken as announced is closed; turnEnds counts the
	// turns' ends; asked and resulted are the calls of the current turn a
	// request was made for and that have a result; showing is the question the
	// attention shows of the list, under the attention it took the place
	// of, and aside the changes made meanwhile.
	requests  int
	waiting   int
	announced chan struct{}
	turnEnds  uint64
	asked     map[string]bool
	resulted  map[string]bool
	showing   string
	under     Attention
	aside     []asideChange
}

// queuedMessage is a message on its way into the conversation.
type queuedMessage struct {
	sub    Submission
	msg    Message
	seq    uint64
	by     string
	byName string
	claim  uint64
	timer  *time.Timer
	// state is its entry's state (a Prompt* state).
	state string
	// told says its Finished has been called.
	told bool
}

type openTool struct {
	seq  uint64
	name string
	at   time.Time
	// rawName, digest and turn are what a permission request is bound
	// by: the tool's name and input's digest as the stream announced
	// them, and the turn it was announced in.
	rawName string
	digest  string
	turn    uint32
}

type openText struct {
	seq        uint64
	pending    string
	lastChange time.Time
	timer      *time.Timer
}

type openPermission struct {
	seq      uint64
	ref      string
	call     string
	question string
	// digest is the input's (inputSha256), plain the question's plain
	// words' kind; held the POST that holds a request put to the prompt
	// tool (nil for an in-stream one); claim the operator's answer's while
	// it is checked, handed once its verdict is on its way; stop ends its
	// PermissionWait.
	digest string
	plain  string
	held   *heldRequest
	claim  uint64
	handed bool
	stop   func() bool
}

// busyLocked says a message's turn is under way or a turn runs: the next
// message waits for the turn's end.
func (p *protocolWay) busyLocked() bool {
	return p.inTurn || p.accepted || p.inflight != nil || p.sending
}

// Way says how the session runs its agent: WayTerminal, or WayProtocol.
func (s *Local) Way() string {
	if s.way.Load() != nil {
		return WayProtocol
	}
	return WayTerminal
}

// Ready is closed once a protocol session's first child answered its
// driver's Begin; nil for a session that runs in its terminal.
func (s *Local) Ready() <-chan struct{} {
	if p := s.way.Load(); p != nil {
		return p.ready
	}
	return nil
}

// now is the protocol way's clock (Options.Now).
func (s *Local) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func (s *Local) acceptWait() time.Duration {
	if s.opts.AcceptWait > 0 {
		return s.opts.AcceptWait
	}
	return AcceptWait
}

// enterProtocol puts the session in the protocol way: start's child on the
// agent's conversation, its output read at once, its driver's Begin
// waited for. A Begin that fails leaves the session in its terminal, the
// child ended. Only a session that runs in its terminal and has not ended
// enters it.
func (s *Local) enterProtocol(ctx context.Context, start ProtocolStart) error {
	_, err := s.enterProtocolGroup(ctx, start)
	return err
}

// enterProtocolGroup is enterProtocol answering, for a start whose child
// never became ready, that child's process group (Grouped), stopped: the
// move proves it gone before it goes back (I1).
func (s *Local) enterProtocolGroup(ctx context.Context, start ProtocolStart) (int, error) {
	if start == nil {
		return 0, errors.New("session: a protocol way needs its start")
	}
	s.mu.Lock()
	if s.info.Status.Ended() {
		s.mu.Unlock()
		return 0, ErrSessionEnded
	}
	if s.way.Load() != nil {
		s.mu.Unlock()
		return 0, errors.New("session: the session runs in the protocol way already")
	}
	conv := ""
	if s.info.AgentSession != nil {
		conv = s.info.AgentSession.ID
	}
	s.mu.Unlock()
	child, driver, err := start(ctx, conv)
	if err != nil {
		return 0, err
	}
	wctx, cancel := context.WithCancel(context.Background())
	p := &protocolWay{
		start: start, name: cutAt(driver.Name(), 32), verbatim: driver.Verbatim(),
		gen: 1, child: child, driver: driver, readDone: make(chan struct{}),
		ctx: wctx, cancel: cancel, ready: make(chan struct{}), kick: make(chan struct{}, 1),
		tools: map[string]*openTool{}, texts: map[string]*openText{},
		announced: make(chan struct{}), asked: map[string]bool{}, resulted: map[string]bool{},
	}
	s.mu.Lock()
	if s.info.Status.Ended() || s.way.Load() != nil {
		s.mu.Unlock()
		cancel()
		_ = driver.CloseInput()
		_ = child.Stop(context.Background(), s.opts.StopGrace)
		return groupOf(child), ErrSessionEnded
	}
	s.way.Store(p)
	s.mu.Unlock()
	go s.readProtocol(p, p.gen, driver, child, p.readDone)
	bctx, bcancel := context.WithTimeout(ctx, protocolReadyCap)
	err = driver.Begin(bctx)
	bcancel()
	s.mu.Lock()
	if err == nil && p.readEnded {
		err = errors.New("session: the agent's output ended as it started")
	}
	if err == nil {
		p.isReady = true
		close(p.ready)
	}
	s.mu.Unlock()
	if err != nil {
		// Back to the terminal: the child that never became ready ends,
		// nothing it sent is applied, and a message queued meanwhile is
		// told why it was never written.
		var out applied
		s.mu.Lock()
		p.gen++
		s.way.Store(nil)
		for _, q := range p.queue {
			if q.claim != 0 {
				out.release = append(out.release, questionClaim{id: q.sub.Question, token: q.claim})
			}
			out.finish(q, SubmitResult{}, err)
		}
		p.queue = nil
		s.mu.Unlock()
		s.sendApplied(out)
		cancel()
		_ = driver.CloseInput()
		_ = child.Stop(context.Background(), s.opts.StopGrace)
		return groupOf(child), err
	}
	go s.protocolSender(p)
	p.wake()
	// Its Info says the way now.
	s.notifyChange()
	return 0, nil
}

// wake wakes the sender.
func (p *protocolWay) wake() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// readProtocol runs the driver's Read for the child of generation gen
// until its output ends. The end of the current child's output, not one
// the session caused (a restart, a stop), ends the session; a line the
// driver cannot read ends the child first, with a notice.
func (s *Local) readProtocol(p *protocolWay, gen uint64, d Protocol, child Child, done chan struct{}) {
	defer close(done)
	err := d.Read(p.ctx, protocolEvents{s: s, p: p, gen: gen})
	s.mu.Lock()
	if p.gen == gen && (!p.isReady || p.restarting) {
		// Ended before it was ready: its start fails (enterProtocol,
		// restartProtocol).
		p.readEnded = true
	}
	stale := p.gen != gen || p.restarting || p.stopping || !p.isReady || s.info.Status.Ended()
	s.mu.Unlock()
	if stale {
		return
	}
	var back backToTerminal
	if err != nil && errors.As(err, &back) && back.BackToTerminal() {
		// The driver says the chat cannot go on but its terminal can (an
		// agent not signed in, design 4): the session goes back to its
		// terminal on the same conversation, with the driver's sentence,
		// when it came into the chat by a move and nothing else is under
		// way. Otherwise it ends, as below.
		s.mu.Lock()
		m := s.movedWith
		can := m.Terminal != nil && s.moveDone == nil && p.gen == gen && !p.stopping && !p.restarting && !s.info.Status.Ended()
		if can {
			s.clearNotedLocked()
			s.beginMoveLocked()
		}
		s.mu.Unlock()
		if can {
			go s.move(WayProtocol, WayTerminal, m, cutAt(CleanMessage(err.Error()), MaxNoticeWords))
			return
		}
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		// The driver's error may name the end in its agent's words, or say
		// it has told the session already (EndNotice's empty code).
		code, words := noticeUnreadable, UnreadableWords
		var told endNoticer
		if errors.As(err, &told) {
			code, words = told.EndNotice()
		}
		var out applied
		s.mu.Lock()
		if code != "" {
			s.noticeLocked(p, code, words, &out)
		}
		s.mu.Unlock()
		s.sendApplied(out)
		_ = d.CloseInput()
		_ = child.Stop(context.Background(), s.opts.StopGrace)
	}
	select {
	case <-child.Done():
	case <-time.After(5 * time.Second):
	}
	s.markEnded(StatusExited)
}

// protocolSender writes the queued messages, one at a time, each once,
// none while a turn is under way, until the session ends.
func (s *Local) protocolSender(p *protocolWay) {
	for {
		select {
		case <-p.kick:
		case <-p.ctx.Done():
			return
		}
		for s.sendNext(p) {
		}
	}
}

// sendNext writes the next queued message, when one waits and nothing is
// under way; it reports whether it took one.
func (s *Local) sendNext(p *protocolWay) bool {
	s.mu.Lock()
	if s.info.Status.Ended() || s.moveDone != nil || p.stopping || p.restarting || !p.isReady || p.busyLocked() || len(p.queue) == 0 {
		s.mu.Unlock()
		return false
	}
	q := p.queue[0]
	p.queue = p.queue[1:]
	var out applied
	if err := (writeWait{from: submissionFrom(q.sub), gone: q.sub.Withdrawn}).withdrawn(); err != nil {
		// Its sender no longer stands: never written.
		s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State = PromptWithdrawn }, &out)
		out.release = append(out.release, questionClaim{id: q.sub.Question, token: q.claim})
		out.finish(q, SubmitResult{}, err)
		s.mu.Unlock()
		s.sendApplied(out)
		return true
	}
	// In flight from before its write: the agent's acknowledgement may
	// come before Send returns.
	p.sending, p.inflight = true, q
	d := p.driver
	s.mu.Unlock()
	if q.claim != 0 {
		// A reply's claim resolves its question as its write is made, as
		// a line's Enter does in the terminal: before the agent's turn,
		// which takes the question's place, can begin.
		by := ""
		if q.sub.By != nil {
			by = q.sub.By.ID
		}
		s.resolve(q.sub.Question, q.claim, by, q.byName, q.sub.ReqID, nil)
	}

	err := d.Send(p.ctx, q.msg)

	s.mu.Lock()
	p.sending = false
	if err != nil {
		// A write that failed may have written some of it: never written
		// again.
		if p.inflight == q {
			p.inflight = nil
		}
		if q.state != PromptAccepted {
			q.state = PromptUnconfirmed
			s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State = PromptUnconfirmed }, &out)
		}
		out.finish(q, SubmitResult{}, err)
		out.kick = true
		s.mu.Unlock()
		s.sendApplied(out)
		return true
	}
	if q.state != PromptAccepted && p.inflight == q {
		q.state = PromptWritten
		turn := p.turns + 1
		s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State, e.Turn = PromptWritten, turn }, &out)
		q.timer = time.AfterFunc(s.acceptWait(), func() { s.unconfirm(p, q) })
	}
	if q.sub.By == nil && (q.sub.Question == "" || q.sub.Decision != "") {
		// A line Conductor wrote itself, as the terminal's typedEntry
		// records one.
		out.records = append(out.records, ActivityEntry{Type: ActivityInput, ByName: q.byName, Message: q.msg.Text, Op: OpTyped})
	}
	s.mu.Unlock()
	s.sendApplied(out)
	return true
}

// unconfirm is a written message's AcceptWait passed with no
// acknowledgement: unconfirmed, never written again.
func (s *Local) unconfirm(p *protocolWay, q *queuedMessage) {
	var out applied
	s.mu.Lock()
	if p.inflight != q || q.told {
		s.mu.Unlock()
		return
	}
	s.unconfirmLocked(p, q, &out)
	s.mu.Unlock()
	s.sendApplied(out)
}

// unconfirmLocked marks the message in flight unconfirmed and tells its
// sender so. The caller holds s.mu.
func (s *Local) unconfirmLocked(p *protocolWay, q *queuedMessage, out *applied) {
	if q.timer != nil {
		q.timer.Stop()
	}
	p.inflight = nil
	q.state = PromptUnconfirmed
	s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State = PromptUnconfirmed }, out)
	p.unconfirmed = append(p.unconfirmed, q)
	if len(p.unconfirmed) > maxUnconfirmedKept {
		p.unconfirmed = p.unconfirmed[1:]
	}
	out.finish(q, SubmitResult{Unconfirmed: true}, ErrUnconfirmed)
	out.kick = true
}

// submissionFrom is whose connection a submission is.
func submissionFrom(sub Submission) *Subscription {
	if sub.By != nil {
		return sub.By
	}
	return sub.From
}

// ProtocolLine cleans a message's text for the protocol way: its line
// breaks kept (a carriage return and line feed, or a lone carriage return,
// one line feed), a tab a space, every other control character dropped.
func ProtocolLine(text string) string {
	text = strings.ReplaceAll(strings.ToValidUTF8(text, ""), "\r\n", "\n")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\r':
			return '\n'
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, text))
}

// submitProtocol is Submit on a session that runs in the protocol way: the
// guards that are not the terminal's (role, which Submit has looked at,
// length, the sender withdrawn, the session ended, the question and
// decision rules), then the message queued and ErrSubmitPending at once.
// A line naming an open permission request is refused before any claim
// (ErrPermissionCard). With UnlessWaiting nothing is queued while a
// question shows or a turn is under way. A message is written once the
// turn before it has ended, in order, each once; Finished is told Entered
// at the agent's acknowledgement, or Unconfirmed (ErrUnconfirmed) once
// AcceptWait has passed from the write.
func (s *Local) submitProtocol(p *protocolWay, sub Submission) (SubmitResult, error) {
	var res SubmitResult
	text := ProtocolLine(sub.Text)
	if len(text) > MaxSubmitText {
		return res, ErrTextTooLong
	}
	if err := (writeWait{from: submissionFrom(sub), gone: sub.Withdrawn}).withdrawn(); err != nil {
		return res, err
	}
	s.mu.Lock()
	if s.info.Status.Ended() {
		s.mu.Unlock()
		return res, ErrSessionEnded
	}
	if s.moveDone != nil || s.way.Load() != p {
		// A move began since Submit looked (move.go): refused here, under
		// the lock the move begins under, never queued on a way that ends.
		s.mu.Unlock()
		return res, ErrMoving
	}
	if sub.Question != "" && s.permissionQuestionLocked(sub.Question) {
		s.mu.Unlock()
		return res, ErrPermissionCard
	}
	if sub.Question != "" {
		if err := s.waitsLocked(sub.Question, sub.Decision); err != nil {
			s.mu.Unlock()
			return res, err
		}
	}
	showing := s.info.Attention.State == AttentionNeedsInput
	if sub.Decision != "" && s.info.Attention.Decision != "" {
		showing = false
	}
	if sub.UnlessWaiting && (showing || p.busyLocked() || len(p.queue) > 0) {
		s.mu.Unlock()
		return res, nil
	}
	if len(p.queue) >= MaxQueuedMessages {
		s.mu.Unlock()
		return res, ErrTooManyQueued
	}
	q := &queuedMessage{sub: sub, msg: Message{Ref: NewID(), Text: text}, by: "conductor", byName: CleanName(sub.ByName), state: PromptQueued}
	shownName := oneLine(sub.ByName, maxPromptName)
	if from := submissionFrom(sub); from != nil {
		q.by = "person"
		if sub.By != nil {
			q.byName = sub.By.answerer()
			shownName = oneLine(q.byName, maxPromptName)
		}
	}
	if sub.Question != "" {
		token, err := s.claimLocked(sub.Question, sub.Decision)
		if err != nil {
			s.mu.Unlock()
			return res, err
		}
		q.claim = token
	}
	e := TranscriptEntry{Kind: EntryPrompt, Turn: p.turns + 1, By: q.by, ByName: shownName, State: PromptQueued}
	if sub.Question != "" {
		// An answer carries the question it answers and never its text:
		// its sender's own page shows it (the people's chat's rule).
		e.Answers = sub.Question
	} else {
		e.Text = cutAt(text, MaxEntryText)
	}
	var out applied
	out.event = "submit"
	q.seq = s.addEntryLocked(e, &out)
	p.queue = append(p.queue, q)
	out.kick = true
	s.mu.Unlock()
	s.sendApplied(out)
	return res, ErrSubmitPending
}

// isPermissionQuestion is permissionQuestionLocked, its lock taken.
func (s *Local) isPermissionQuestion(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissionQuestionLocked(id)
}

// permissionQuestionLocked says id names an open permission request's
// question. The caller holds s.mu.
func (s *Local) permissionQuestionLocked(id string) bool {
	if att := s.info.Attention; att.ID == id && att.Source == SourceProtocol && att.Kind == KindPermission {
		return true
	}
	if p := s.way.Load(); p != nil {
		for _, r := range p.perms {
			if r.question == id {
				return true
			}
		}
	}
	return false
}

// Interrupt ends the turn a protocol session's agent runs, keeping the
// conversation: a controller's (by nil for Conductor's own). The turn's
// end arrives from the agent; an agent that offers no interrupt
// (ErrNoInterrupt) has its child ended and started again on the same
// conversation, the session open. ErrNotProtocol on a session that runs
// in its terminal, ErrReadOnly for a view role, ErrNoTurn while no turn
// is under way.
func (s *Local) Interrupt(ctx context.Context, by *Subscription) error {
	if s.Moving() {
		// Between ways (move.go): nothing is stopped until the move ends.
		return ErrMoving
	}
	p := s.way.Load()
	if p == nil {
		return ErrNotProtocol
	}
	if by != nil && by.Role != RoleControl {
		return ErrReadOnly
	}
	s.mu.Lock()
	if s.info.Status.Ended() {
		s.mu.Unlock()
		return ErrSessionEnded
	}
	if s.moveDone != nil {
		s.mu.Unlock()
		return ErrMoving
	}
	if !p.isReady || p.restarting || p.stopping || !(p.inTurn || p.accepted || p.inflight != nil) {
		s.mu.Unlock()
		return ErrNoTurn
	}
	d, gen := p.driver, p.gen
	s.mu.Unlock()
	err := d.Interrupt(ctx)
	if !errors.Is(err, ErrNoInterrupt) {
		return err
	}
	s.mu.Lock()
	if p.gen != gen || p.restarting || p.stopping || s.info.Status.Ended() {
		s.mu.Unlock()
		return nil
	}
	p.restarting = true
	s.mu.Unlock()
	go s.restartProtocol(p)
	return nil
}

// restartProtocol ends the child whose agent offers no interrupt and starts
// it again on the same conversation: the turn ends interrupted, a notice
// says so, and the session stays open. A start that fails ends the session.
func (s *Local) restartProtocol(p *protocolWay) {
	var out applied
	out.event = "restart"
	s.mu.Lock()
	p.gen++
	// This restart is the one a restart at the turn's end asked for, if one
	// did: none is left for the next turn's end.
	p.restartAtEnd = false
	oldDriver, oldChild, oldRead := p.driver, p.child, p.readDone
	if q := p.inflight; q != nil && !q.told {
		// Written to the child that ends: never written again.
		s.unconfirmLocked(p, q, &out)
	}
	if p.inTurn || p.accepted {
		s.endTurnLocked(p, "interrupted", Usage{}, &out)
	}
	conv := ""
	if s.info.AgentSession != nil {
		conv = s.info.AgentSession.ID
	}
	s.mu.Unlock()
	s.sendApplied(out)

	_ = oldDriver.CloseInput()
	_ = oldChild.Stop(context.Background(), s.opts.StopGrace)
	select {
	case <-oldRead:
	case <-time.After(s.opts.StopGrace + 5*time.Second):
	}
	fail := func() {
		var out applied
		s.mu.Lock()
		p.restarting = false
		if !s.info.Status.Ended() && !p.stopping {
			s.noticeLocked(p, noticeRestartFail, RestartFailWords, &out)
		}
		s.mu.Unlock()
		s.sendApplied(out)
		s.markEnded(StatusExited)
	}
	ctx, cancel := context.WithTimeout(p.ctx, protocolReadyCap)
	defer cancel()
	child, driver, err := p.start(ctx, conv)
	if err != nil {
		fail()
		return
	}
	s.mu.Lock()
	if s.info.Status.Ended() || p.stopping {
		s.mu.Unlock()
		_ = driver.CloseInput()
		_ = child.Stop(context.Background(), s.opts.StopGrace)
		return
	}
	// The new child's work is its own: what the old one reported running
	// ended with its group.
	p.child, p.driver, p.readDone, p.readEnded, p.background = child, driver, make(chan struct{}), false, false
	gen, done := p.gen, p.readDone
	s.mu.Unlock()
	go s.readProtocol(p, gen, driver, child, done)
	err = driver.Begin(ctx)
	// The output's end and the restart's end are judged in one lock section:
	// an end that comes between them is seen here, or by readProtocol once
	// restarting is false.
	s.mu.Lock()
	if err == nil && p.readEnded {
		err = errors.New("session: the agent's output ended as it started")
	}
	if err != nil {
		s.mu.Unlock()
		fail()
		_ = driver.CloseInput()
		_ = child.Stop(context.Background(), s.opts.StopGrace)
		return
	}
	out = applied{event: "restarted", kick: true}
	p.restarting = false
	code, words := noticeRestarted, RestartedWords
	if p.restartCode != "" {
		code, words = p.restartCode, p.restartWords
		p.restartCode, p.restartWords = "", ""
	}
	s.noticeLocked(p, code, words, &out)
	s.mu.Unlock()
	s.sendApplied(out)
}

// stopProtocol is Stop on a session in the protocol way: every open
// permission request denied, then the agent's input closed, then the
// child stopped (StopGrace), then the session ended.
func (s *Local) stopProtocol(ctx context.Context, p *protocolWay) error {
	var out applied
	out.event = "stop"
	s.mu.Lock()
	ended := s.info.Status.Ended()
	s.stopRequested = true
	p.stopping = true
	child, d := p.child, p.driver
	p.announceLocked()
	for len(p.perms) > 0 {
		s.closePermissionLocked(p, p.perms[0], PermissionDenied, EndedBeforeAnswerWords, nil, &out)
	}
	replies := out.replies
	out.replies = nil
	s.mu.Unlock()
	s.sendApplied(out)
	select {
	case <-child.Done():
		if ended {
			return nil
		}
	default:
	}
	for _, r := range replies {
		_ = d.Reply(ctx, r.ref, r.v)
	}
	_ = d.CloseInput()
	err := child.Stop(ctx, s.opts.StopGrace)
	s.markEnded(StatusStopped)
	return err
}

// endProtocolLocked is the protocol way's part of the session's end
// (markEnded): what waits to be written, or for its acknowledgement, is
// told the session ended, the open requests are denied, and nothing the
// way runs waits any more. It returns what to do once the lock is let go.
// The caller holds s.mu.
func (s *Local) endProtocolLocked() func() {
	p := s.way.Load()
	if p == nil {
		return func() {}
	}
	var out applied
	out.event = "ended"
	p.announceLocked()
	for len(p.perms) > 0 {
		s.closePermissionLocked(p, p.perms[0], PermissionDenied, EndedBeforeAnswerWords, nil, &out)
	}
	out.replies = nil
	pending := append([]*queuedMessage(nil), p.queue...)
	if p.inflight != nil {
		pending = append(pending, p.inflight)
	}
	p.queue, p.inflight = nil, nil
	for _, q := range pending {
		if q.timer != nil {
			q.timer.Stop()
		}
		if q.claim != 0 {
			out.settle = append(out.settle, questionClaim{id: q.sub.Question, token: q.claim})
		}
		out.finish(q, SubmitResult{}, ErrSessionEnded)
	}
	for _, t := range p.texts {
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	p.cancel()
	return func() { s.sendApplied(out) }
}

// applied is what an event applied under the session's lock leaves to do
// once it is let go, in this order: the captured bytes to the
// copied-output check, the test's seam, the attention change, the
// activity entries, the entries' TRANSCRIPT frames (sendEntries), the
// requests' replies, the claims, the submissions' words, the sender woken.
type applied struct {
	event    string
	id       string
	captured []capturedPiece
	attn     *attnChange
	records  []ActivityEntry
	replies  []verdictReply
	release  []questionClaim
	settle   []questionClaim
	finished []func()
	conv     string
	resumed  bool
	kick     bool
	p        *protocolWay
	// entries are the seqs of the entries the event added or revised.
	entries []uint64
	// own are the attention entries of a permission request shown,
	// recorded as the session's own; replay the attention changes kept
	// aside while requests showed, applied once none is open; changed
	// says what the session shows changed (OnChange).
	own     []ActivityEntry
	replay  []asideChange
	changed bool
}

// capturedPiece is bytes a driver captured (OnCaptured, whole), with the
// key the transcript keeps them under (OnTranscriptPiece, cut as kept).
type capturedPiece struct {
	key string
	b   []byte
	// again says the bytes reached OnCaptured already: they are only kept
	// under one more key (words that went on into a new entry).
	again bool
}

// pieceKey is a transcript piece's key: "<seq>/<part>".
func pieceKey(seq uint64, part string) string { return strconv.FormatUint(seq, 10) + "/" + part }

type attnChange struct {
	state   AttentionState
	message string
	kind    string
}

type verdictReply struct {
	d   Protocol
	ref string
	v   Verdict
}

// finish tells q's Finished res and err, once.
func (out *applied) finish(q *queuedMessage, res SubmitResult, err error) {
	if q.told {
		return
	}
	q.told = true
	if f := q.sub.Finished; f != nil {
		out.finished = append(out.finished, func() { f(res, err) })
	}
}

// sendApplied does what an applied event left to do (applied).
func (s *Local) sendApplied(out applied) {
	var dropped []string
	if s.opts.OnTranscriptDropped != nil {
		s.mu.Lock()
		dropped = s.transcript.takeDropped()
		s.mu.Unlock()
	}
	if out.id == "" && (len(out.captured) > 0 || len(dropped) > 0) {
		s.mu.Lock()
		out.id = s.info.ID
		s.mu.Unlock()
	}
	for _, c := range out.captured {
		if len(c.b) == 0 {
			continue
		}
		if s.opts.OnCaptured != nil && !c.again {
			s.opts.OnCaptured(out.id, c.b)
		}
		if s.opts.OnTranscriptPiece != nil && c.key != "" {
			// What the transcript keeps of it: a part past its bound is
			// kept cut, and nothing past the cut is ever shown.
			b := c.b
			if len(b) > MaxTranscriptPart {
				b = b[:MaxTranscriptPart]
			}
			s.opts.OnTranscriptPiece(out.id, c.key, b)
		}
	}
	if len(dropped) > 0 {
		s.opts.OnTranscriptDropped(out.id, dropped)
	}
	if hook := s.afterApply.Load(); hook != nil && out.event != "" {
		(*hook)(out.event)
	}
	if a := out.attn; a != nil {
		_ = s.setAttentionLooked(a.state, a.message, SourceProtocol, a.kind, nil, unlessEnded, anyTurn, 0, "")
	}
	for _, e := range out.records {
		s.Record(e)
	}
	for _, e := range out.own {
		s.recordOwn(e, AttentionNeedsInput)
	}
	for _, c := range out.replay {
		if !c.hasOpt {
			_ = s.setAttentionLooked(c.state, c.message, c.source, c.kind, c.options, c.rules, c.turn, 0, c.plan)
			continue
		}
		_ = s.setAttentionLooked(c.state, c.message, c.source, c.kind, c.options, c.rules, c.turn, 0, c.plan, c.opt)
	}
	if out.changed {
		s.notifyChange()
	}
	if len(out.entries) > 0 {
		s.sendEntries(out.entries)
	}
	for _, r := range out.replies {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), s.opts.StopGrace)
			defer cancel()
			_ = r.d.Reply(ctx, r.ref, r.v)
		}()
	}
	for _, c := range out.release {
		if c.token != 0 {
			s.release(c.id, c.token)
			s.claimSettled(c.id, c.token)
		}
	}
	for _, c := range out.settle {
		s.claimSettled(c.id, c.token)
	}
	if out.conv != "" {
		s.ReportAgentSession(out.conv, PolicyLatest, out.resumed)
	}
	for _, f := range out.finished {
		f()
	}
	if out.kick && out.p != nil {
		out.p.wake()
	}
	// A move noted during the turn, made once the session is at rest
	// (move.go): the event may have ended what held it.
	s.tryNoted()
}

// addEntryLocked adds e to the transcript at the session's clock. The
// caller holds s.mu.
func (s *Local) addEntryLocked(e TranscriptEntry, out *applied) uint64 {
	out.id = s.info.ID
	out.p = s.way.Load()
	seq := s.transcript.add(e, s.now())
	out.entries = append(out.entries, seq)
	return seq
}

// reviseLocked revises the entry seq. The caller holds s.mu.
func (s *Local) reviseLocked(seq uint64, change func(*TranscriptEntry), out *applied) bool {
	out.id = s.info.ID
	out.p = s.way.Load()
	out.entries = append(out.entries, seq)
	return s.transcript.revise(seq, change)
}

// noticeLocked adds a notice of the session's own. The caller holds s.mu.
func (s *Local) noticeLocked(p *protocolWay, code, words string, out *applied) {
	s.addEntryLocked(TranscriptEntry{Kind: EntryNotice, Turn: p.turns, Code: cutAt(code, MaxNoticeCode), Words: cutAt(CleanMessage(words), MaxNoticeWords)}, out)
}

// closePermissionLocked closes the open request r with state and why, its
// entry and input let go; reply, when set, is the verdict its driver is
// sent. The caller holds s.mu.
func (s *Local) closePermissionLocked(p *protocolWay, r *openPermission, state, why string, reply *Verdict, out *applied) {
	s.removePermissionLocked(p, r, out)
	s.reviseLocked(r.seq, func(e *TranscriptEntry) { e.State, e.Why = state, why }, out)
	s.unpinLocked(r)
	v := Verdict{Message: why}
	if reply != nil {
		v = *reply
	}
	if v.Allow {
		// Never: no end allows (I11).
		v = Verdict{Message: why}
	}
	if r.held != nil {
		// Its POST is answered at once, never allow.
		select {
		case r.held.verdict <- heldVerdict{v: v}:
		default:
		}
		return
	}
	out.replies = append(out.replies, verdictReply{d: p.driver, ref: r.ref, v: v})
}

// endTurnLocked is a turn's end (TurnEnded, or the session's own as it
// restarts a child): the open texts closed, an interrupted turn's steps
// interrupted, the open requests withdrawn, the turn's entry, and the
// attention done (KindInterrupted for an interrupted turn). The caller
// holds s.mu.
func (s *Local) endTurnLocked(p *protocolWay, outcome string, u Usage, out *applied) {
	switch outcome {
	case "done", "interrupted", "error", "limit":
	default:
		outcome = "error"
	}
	if !p.inTurn {
		// A turn whose start the agent did not tell.
		p.turns++
		p.turnAt = s.now()
	}
	for _, ref := range append([]string(nil), p.textOrder...) {
		if t := p.texts[ref]; t != nil {
			s.flushTextLocked(p, ref, t, true, out)
		}
	}
	if outcome == "interrupted" {
		for _, ref := range append([]string(nil), p.toolOrder...) {
			if t := p.tools[ref]; t != nil {
				s.reviseLocked(t.seq, func(e *TranscriptEntry) { e.Status = ToolInterrupted }, out)
				s.forgetToolLocked(p, ref)
			}
		}
	}
	why := AnsweredByAgentWords
	if outcome == "interrupted" {
		why = TurnStoppedWords
	}
	for len(p.perms) > 0 {
		s.closePermissionLocked(p, p.perms[0], PermissionWithdrawn, why, &Verdict{Cancel: true, Message: why}, out)
	}
	// A request waiting for its call is denied: the turn it named ended.
	p.turnEnds++
	clear(p.asked)
	clear(p.resulted)
	p.announceLocked()
	d := s.now().Sub(p.turnAt).Milliseconds()
	if d < 0 {
		d = 0
	}
	e := TranscriptEntry{Kind: EntryTurn, Turn: p.turns, Phase: "end", Outcome: outcome, DurationMs: &d}
	if u != (Usage{}) {
		e.Usage = &TurnUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: u.CostUSD}
	}
	s.addEntryLocked(e, out)
	message := ""
	if re := s.transcript.find(p.lastText); re != nil && re.e.Turn == p.turns {
		message = cutAt(CleanMessage(re.e.Text), maxTurnMessage)
	}
	kind := KindDone
	if outcome == "interrupted" {
		kind = KindInterrupted
	}
	out.attn = &attnChange{state: AttentionDone, message: message, kind: kind}
	p.inTurn, p.accepted, p.ended = false, false, true
	if s.info.AgentSession != nil {
		out.conv, out.resumed = s.info.AgentSession.ID, true
	}
	out.kick = true
}

// flushTextLocked applies a streamed text's pending words to its entry,
// an agent's message past MaxEntryText going on in the next entry with
// the same ref; final closes it. The caller holds s.mu.
func (s *Local) flushTextLocked(p *protocolWay, ref string, t *openText, final bool, out *applied) {
	pending := t.pending
	t.pending = ""
	if re := s.transcript.find(t.seq); re != nil {
		take := cutAt(pending, MaxEntryText-len(re.e.Text))
		pending = pending[len(take):]
		if take != "" || final && pending == "" {
			s.reviseLocked(t.seq, func(e *TranscriptEntry) {
				e.Text += take
				e.Final = final && pending == ""
			}, out)
		}
	}
	for pending != "" {
		chunk := cutAt(pending, MaxEntryText)
		if chunk == "" {
			// Not a character boundary within reach: drop the rest.
			break
		}
		pending = pending[len(chunk):]
		t.seq = s.addEntryLocked(TranscriptEntry{Kind: EntryText, Turn: p.turns, Ref: ref, Text: chunk, Final: final && pending == ""}, out)
		out.captured = append(out.captured, capturedPiece{key: pieceKey(t.seq, "text"), b: []byte(chunk), again: true})
	}
	p.lastText = t.seq
	t.lastChange = s.now()
	if final {
		if t.timer != nil {
			t.timer.Stop()
		}
		s.forgetTextLocked(p, ref)
	}
}

func (s *Local) forgetTextLocked(p *protocolWay, ref string) {
	delete(p.texts, ref)
	for i, r := range p.textOrder {
		if r == ref {
			p.textOrder = append(p.textOrder[:i], p.textOrder[i+1:]...)
			break
		}
	}
}

func (s *Local) forgetToolLocked(p *protocolWay, ref string) {
	delete(p.tools, ref)
	for i, r := range p.toolOrder {
		if r == ref {
			p.toolOrder = append(p.toolOrder[:i], p.toolOrder[i+1:]...)
			break
		}
	}
}

// flushTextTimer applies a streamed text's pending words once
// TextCoalesce has passed since its entry's last change, by the
// session's clock.
func (s *Local) flushTextTimer(p *protocolWay, gen uint64, ref string, t *openText) {
	var out applied
	out.event = "text"
	s.mu.Lock()
	if p.gen != gen || p.texts[ref] != t || t.pending == "" || s.info.Status.Ended() {
		s.mu.Unlock()
		return
	}
	if wait := TextCoalesce - s.now().Sub(t.lastChange); wait > 0 {
		t.timer = time.AfterFunc(wait, func() { s.flushTextTimer(p, gen, ref, t) })
		s.mu.Unlock()
		return
	}
	t.timer = nil
	s.flushTextLocked(p, ref, t, false, &out)
	s.mu.Unlock()
	s.sendApplied(out)
}

// protocolEvents is the session's side of a driver (ProtocolEvents), for
// the child of generation gen: what an older child sends is dropped.
type protocolEvents struct {
	s   *Local
	p   *protocolWay
	gen uint64
}

// begin takes the session's lock for an event: false, the lock let go,
// when the event is to be dropped.
func (e protocolEvents) begin() bool {
	e.s.mu.Lock()
	if e.p.gen != e.gen || e.s.info.Status.Ended() {
		e.s.mu.Unlock()
		return false
	}
	e.p.lastEvent = time.Now()
	return true
}

func (e protocolEvents) Conversation(id string) {
	if len(id) > MaxAgentSessionID || !agentSessionShape.MatchString(id) || !e.begin() {
		return
	}
	resumed := e.p.ended
	e.s.mu.Unlock()
	e.s.sendApplied(applied{event: "conversation", conv: id, resumed: resumed})
}

func (e protocolEvents) Accepted(ref string) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "accepted"}
	q := p.inflight
	switch {
	case q != nil && q.msg.Ref == ref:
		if q.timer != nil {
			q.timer.Stop()
		}
		p.inflight, p.accepted = nil, true
		q.state = PromptAccepted
		s.reviseLocked(q.seq, func(e *TranscriptEntry) { e.State = PromptAccepted }, &out)
		out.finish(q, SubmitResult{Entered: true}, nil)
	default:
		// An acknowledgement after AcceptWait: the message was taken, and
		// its word (unconfirmed) stands.
		for i, u := range p.unconfirmed {
			if u.msg.Ref == ref {
				p.unconfirmed = append(p.unconfirmed[:i], p.unconfirmed[i+1:]...)
				p.accepted = true
				s.reviseLocked(u.seq, func(e *TranscriptEntry) { e.State = PromptAccepted }, &out)
				break
			}
		}
	}
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) TurnStarted(string) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "turn_started"}
	if !p.inTurn {
		p.turns++
		p.inTurn = true
		p.turnAt = s.now()
		s.addEntryLocked(TranscriptEntry{Kind: EntryTurn, Turn: p.turns, Phase: "start"}, &out)
		out.attn = &attnChange{state: AttentionWorking}
	}
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) Text(ref, delta string, final bool) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "text"}
	ref = cutAt(ref, MaxEntryRef)
	t := p.texts[ref]
	if t == nil {
		chunk := cutAt(delta, MaxEntryText)
		t = &openText{pending: delta[len(chunk):]}
		t.seq = s.addEntryLocked(TranscriptEntry{Kind: EntryText, Turn: p.turns, Ref: ref, Text: chunk, Final: final && len(chunk) == len(delta)}, &out)
		t.lastChange = s.now()
		p.lastText = t.seq
		p.texts[ref] = t
		p.textOrder = append(p.textOrder, ref)
		if len(p.textOrder) > maxOpenSteps {
			s.forgetTextLocked(p, p.textOrder[0])
		}
		switch {
		case t.pending != "":
			// Past one entry's text: it goes on in the next.
			s.flushTextLocked(p, ref, t, final, &out)
		case final:
			// Whole and closed as it came.
			s.forgetTextLocked(p, ref)
		}
	} else {
		t.pending += delta
		if final || s.now().Sub(t.lastChange) >= TextCoalesce {
			s.flushTextLocked(p, ref, t, final, &out)
		}
	}
	if !final && t.pending != "" && t.timer == nil && p.texts[ref] == t {
		gen := e.gen
		t.timer = time.AfterFunc(TextCoalesce, func() { s.flushTextTimer(p, gen, ref, t) })
	}
	// Kept under the entry the words end in for now; words that go on
	// into a new entry are kept under it too as it is made
	// (flushTextLocked).
	out.captured = append(out.captured, capturedPiece{key: pieceKey(t.seq, "text"), b: []byte(delta)})
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) ToolStarted(t ToolStep) {
	if hook := e.s.beforeToolStarted.Load(); hook != nil {
		(*hook)()
	}
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "tool_started"}
	ref := cutAt(t.Ref, MaxEntryRef)
	name := cutAt(CleanMessage(t.Name), MaxEventTool)
	summary := t.Summary
	if summary == "" {
		summary = compactJSON(t.Input)
	}
	summary = cutAt(strings.Join(strings.Fields(CleanMessage(summary)), " "), MaxEntrySummary)
	seq := s.addEntryLocked(TranscriptEntry{
		Kind: EntryTool, Turn: p.turns, Ref: ref, Parent: cutAt(t.Parent, MaxEntryRef), Tool: name, Summary: summary,
		InputBytes: len(t.Input), InputSha256: inputDigest(t.Name, t.Input), Status: "running",
	}, &out)
	s.transcript.putPart(seq, PartInput, t.Input, false, false)
	out.captured = append(out.captured, capturedPiece{key: pieceKey(seq, PartInput), b: t.Input}, capturedPiece{key: pieceKey(seq, "text"), b: []byte(summary), again: true})
	if ref != "" {
		if _, open := p.tools[ref]; open {
			s.forgetToolLocked(p, ref)
		}
		p.tools[ref] = &openTool{seq: seq, name: name, at: s.now(), rawName: t.Name, digest: inputDigest(t.Name, t.Input), turn: p.turns}
		p.toolOrder = append(p.toolOrder, ref)
		if len(p.toolOrder) > maxOpenSteps {
			s.forgetToolLocked(p, p.toolOrder[0])
		}
		// A request waiting for this call takes it now.
		p.announceLocked()
	}
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) ToolEnded(ref string, r ToolResult) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "tool_ended"}
	ref = cutAt(ref, MaxEntryRef)
	t := p.tools[ref]
	if t == nil {
		s.mu.Unlock()
		return
	}
	s.forgetToolLocked(p, ref)
	status := r.Status
	switch status {
	case ToolOK, ToolError, ToolDenied, ToolInterrupted:
	default:
		status = ToolOK
	}
	var exit *int
	if r.Exit != nil {
		x := *r.Exit
		exit = &x
	}
	measured := ""
	ms := r.Duration.Milliseconds()
	if r.Duration <= 0 {
		ms, measured = max(s.now().Sub(t.at).Milliseconds(), 0), "conductor"
	}
	s.reviseLocked(t.seq, func(e *TranscriptEntry) {
		e.Status, e.Exit, e.DurationMs, e.Measured, e.OutputBytes = status, exit, &ms, measured, len(r.Output)
	}, &out)
	s.transcript.putPart(t.seq, PartOutput, r.Output, r.Cut, false)
	out.captured = append(out.captured, capturedPiece{key: pieceKey(t.seq, PartOutput), b: r.Output})
	// The stream showed the call's result: a request about it was
	// answered by the agent's own rules.
	for _, pr := range append([]*openPermission(nil), p.perms...) {
		if pr.call != "" && pr.call == ref {
			s.closePermissionLocked(p, pr, PermissionWithdrawn, AnsweredByAgentWords, &Verdict{Cancel: true, Message: AnsweredByAgentWords}, &out)
		}
	}
	if len(p.resulted) >= 2*MaxTranscriptEntries {
		clear(p.resulted)
	}
	p.resulted[ref] = true
	p.announceLocked()
	// Activity as the hook mappers make it (internal/notify).
	entry := ActivityEntry{Type: ActivityToolUse, Tool: t.name, ByName: "agent"}
	switch status {
	case ToolDenied:
		entry.Type = ActivityToolDenied
	case ToolError:
		entry.Type = ActivityError
		entry.Message = cutAt(firstLine(string(r.Output)), maxTurnMessage)
	}
	out.records = append(out.records, CleanEntry(entry))
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) FileChanged(f FileChange) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "file_changed"}
	op := f.Op
	switch op {
	case ChangeCreate, ChangeEdit, ChangeDelete:
	default:
		op = ChangeEdit
	}
	step := cutAt(f.Tool, MaxEntryRef)
	rel, outside := s.relativeLocked(f.Path)
	seq := s.addEntryLocked(TranscriptEntry{
		Kind: EntryFile, Turn: p.turns, Path: rel, Outside: outside, Op: op,
		Added: max(f.Added, 0), Removed: max(f.Removed, 0), Tool: step,
	}, &out)
	if len(f.Diff) > 0 {
		s.transcript.putPart(seq, PartDiff, f.Diff, false, false)
		out.captured = append(out.captured, capturedPiece{key: pieceKey(seq, PartDiff), b: f.Diff})
	}
	activityOp := FileOpEdit
	switch op {
	case ChangeCreate:
		activityOp = FileOpWrite
	case ChangeDelete:
		activityOp = FileOpDelete
	}
	if path := strings.TrimSpace(f.Path); path != "" {
		out.records = append(out.records, CleanEntry(ActivityEntry{Type: ActivityFile, Op: activityOp, Path: path, Tool: s.toolNameLocked(p, step), ByName: "agent"}))
	}
	s.mu.Unlock()
	s.sendApplied(out)
}

// relativeLocked is path relative to the session's folder, and whether it
// is outside it (then no path). The caller holds s.mu.
func (s *Local) relativeLocked(path string) (string, bool) {
	path = strings.TrimSpace(strings.Map(dropControl, path))
	if path == "" || len(path) > MaxEventPath {
		return "", true
	}
	rel := path
	if filepath.IsAbs(path) {
		if s.info.Cwd == "" {
			return "", true
		}
		r, err := filepath.Rel(s.info.Cwd, path)
		if err != nil {
			return "", true
		}
		rel = r
	}
	rel = filepath.Clean(rel)
	if !filepath.IsLocal(rel) {
		return "", true
	}
	return filepath.ToSlash(rel), false
}

// toolNameLocked is the name of the step ref, open or kept. The caller
// holds s.mu.
func (s *Local) toolNameLocked(p *protocolWay, ref string) string {
	if t := p.tools[ref]; t != nil {
		return t.name
	}
	if ref == "" {
		return ""
	}
	for i := len(s.transcript.entries) - 1; i >= 0; i-- {
		if e := s.transcript.entries[i].e; e.Kind == EntryTool && e.Ref == ref {
			return e.Tool
		}
	}
	return ""
}

func (e protocolEvents) Permission(r PermissionRequest) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "permission"}
	// An in-stream request is judged at once (the driver hands events in
	// order): a digest of one form, the bound, the floor; then it is the
	// session's question as a request to the prompt tool is.
	canon, clean := strictCanonical(r.Input)
	digest := inputDigest(r.Tool, r.Input)
	if clean {
		digest = digestOf(r.Tool, canon)
	}
	entry := s.permissionEntryLocked(p, r.Call, r.Tool, r.Input, digest, r.Cwd)
	entry.Ref = cutAt(r.Ref, MaxEntryRef)
	why := ""
	switch {
	case len(r.Input) > MaxTranscriptPart:
		// Kept whole or not shown: a part past its bound would be cut
		// under a digest of the whole.
		why = TooLargeWords
	case !clean:
		why = NotExactWords
	case p.requests+p.inStreamLocked() >= MaxPendingPermissions:
		// Past the bound: denied, never left waiting.
		why = TooManyRequestsWords
	case s.flooredLocked(r.Tool, r.Input):
		why = FloorWords
	}
	if why != "" {
		entry.State, entry.Why = PermissionDenied, why
		seq := s.addEntryLocked(entry, &out)
		out.captured = append(out.captured, capturedPiece{key: "", b: r.Input}, capturedPiece{key: pieceKey(seq, "text"), b: []byte(entry.Summary), again: true})
		out.replies = append(out.replies, verdictReply{d: p.driver, ref: r.Ref, v: Verdict{Message: why}})
		s.mu.Unlock()
		s.sendApplied(out)
		return
	}
	s.takeLocked(p, entry, entry.Call, r.Ref, r.Input, nil, &out)
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) PermissionWithdrawn(ref string) {
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	out := applied{event: "permission_withdrawn"}
	for _, r := range p.perms {
		if r.ref == ref {
			s.closePermissionLocked(p, r, PermissionWithdrawn, AnsweredByAgentWords, nil, &out)
			// The agent took it back itself: nothing to answer.
			out.replies = nil
			break
		}
	}
	s.mu.Unlock()
	s.sendApplied(out)
}

func (e protocolEvents) Background(running bool) {
	if !e.begin() {
		return
	}
	e.p.background = running
	e.s.mu.Unlock()
	e.s.tryNoted()
}

func (e protocolEvents) TurnEnded(outcome string, u Usage) {
	if !e.begin() {
		return
	}
	out := applied{event: "turn_ended"}
	e.s.endTurnLocked(e.p, outcome, u, &out)
	// A restart the driver asked for during the turn: no queued message is
	// written to the child that ends, and the next one takes them.
	restart := e.p.restartAtEnd && !e.p.restarting && !e.p.stopping
	if restart {
		e.p.restartAtEnd, e.p.restarting, out.kick = false, true, false
	}
	e.s.mu.Unlock()
	e.s.sendApplied(out)
	if restart {
		go e.s.restartProtocol(e.p)
	}
}

// RestartProtocol has a session in the protocol way end its child and start
// it again on the same conversation, a notice of code and words written once
// it is: at the end of the turn under way (before any queued message is
// written), or at once when none is. A driver asks it for a reason of its
// own (Claude Code's: what it wrote reached its bound). ErrNotProtocol on a
// session in its terminal; nil when a restart or a stop is under way.
func (s *Local) RestartProtocol(code, words string) error {
	p := s.way.Load()
	if p == nil {
		return ErrNotProtocol
	}
	s.mu.Lock()
	if s.info.Status.Ended() || p.restarting || p.stopping || !p.isReady {
		s.mu.Unlock()
		return nil
	}
	p.restartCode, p.restartWords = cutAt(code, MaxNoticeCode), cutAt(CleanMessage(words), MaxNoticeWords)
	if p.inTurn || p.accepted || p.inflight != nil || p.sending {
		p.restartAtEnd = true
		s.mu.Unlock()
		return nil
	}
	p.restarting = true
	s.mu.Unlock()
	go s.restartProtocol(p)
	return nil
}

// endNoticer is a driver's Read error that says how the session tells its
// end: the notice's code and words in place of the unreadable one, or an
// empty code when the driver has told it already.
type endNoticer interface{ EndNotice() (code, words string) }

// backToTerminal is a driver's Read error that says the chat cannot go on
// and its terminal can (Claude Code not signed in): the session moves back
// to its terminal on the same conversation (readProtocol).
type backToTerminal interface{ BackToTerminal() bool }

func (e protocolEvents) Notice(code, words string) {
	if !e.begin() {
		return
	}
	out := applied{event: "notice"}
	e.s.noticeLocked(e.p, code, words, &out)
	e.s.mu.Unlock()
	e.s.sendApplied(out)
}

// ProtocolName is the protocol a session in the protocol way speaks (its
// driver's Name) and whether its driver sends a message as written
// (Verbatim); "" and false for a session in its terminal.
func (s *Local) ProtocolName() (name string, verbatim bool) {
	if p := s.way.Load(); p != nil {
		return p.name, p.verbatim
	}
	return "", false
}

// BackgroundWork says the protocol way's agent reported work it started
// that still runs after its turn; false for a session in its terminal.
func (s *Local) BackgroundWork() bool {
	p := s.way.Load()
	if p == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return p.background
}

// compactJSON is input as compact JSON, or as it is when it is none.
func compactJSON(input []byte) string {
	var b bytes.Buffer
	if json.Compact(&b, input) != nil {
		return string(input)
	}
	return b.String()
}

// inputDigest is SHA-256 over the tool's name, a newline and the input's
// canonical JSON (numbers as written, keys sorted, no space), lowercase
// hex; of the input's bytes as they are when they are not JSON.
func inputDigest(tool string, input []byte) string {
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{'\n'})
	h.Write(canonicalJSON(input))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalJSON(input []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return input
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return input
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}

// plainOf is the closed set of a request's plain words by its tool.
func plainOf(tool string) string {
	switch strings.ToLower(tool) {
	case "bash", "shell", "exec", "command", "commandexecution":
		return "command"
	case "edit", "write", "multiedit", "notebookedit", "filechange":
		return "file"
	case "read", "grep", "glob", "ls":
		return "read"
	}
	return "tool"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return CleanMessage(s)
}
