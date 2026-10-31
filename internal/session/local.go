package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// Process is what Local needs from a PTY-backed process.
type Process interface {
	io.Reader
	io.Writer
	Resize(cols, rows uint16) error
	Done() <-chan struct{}
	Exit() pty.ExitStatus
	Stop(ctx context.Context, grace time.Duration) error
}

// Options tune a Local session.
type Options struct {
	ScrollbackBytes int
	MaxViewers      int
	// FileView decides which roles may read files: "view" (both), "control", "off".
	FileView string
	// FileEdit decides whether a controller may edit files through the
	// editor's Neovim (design round 12, F8): "control" (the default), "off".
	FileEdit string
	// FileDeny lists what no file read, save or editor may reach, even inside
	// the working directory: a directory with everything in it, a single
	// file, or a name entry (see insideAny); DenyList builds one. The server
	// passes its data directory, whose catalog.json holds agent secrets, its
	// config file and its catalog file.
	FileDeny []string
	// FileDenyFunc, when set, gives the deny list for each file request in
	// place of FileDeny: a list that may grow while the session runs, as
	// `conductor host`'s follows the server on its machine
	// (config.LocalFileDenyFunc). It is called from many goroutines.
	FileDenyFunc func() []string
	// Transport is reported in welcome messages ("ws" on the server, "webrtc"/"relay" on hosts).
	Transport string
	Log       *slog.Logger
	// StopGrace is the SIGTERM grace period before SIGKILL.
	StopGrace time.Duration
	// OnChange is called (outside the session lock) after status, viewer
	// count or attention changes so listings and event streams stay current.
	OnChange func(Info)
	// OnActivity is called (outside the session lock) with the session ID,
	// the stored entry and, for the attention entry of a change the session
	// applied, the attention state it records: the state set in the same
	// critical section as the entry's stamp ("" for every other entry). It
	// runs on the recording goroutine, which may be the one reading the
	// process, so it must not block. Because it runs after the lock is
	// released, calls for different entries can overlap and arrive out of
	// order: implementations must be safe for concurrent use, and must take
	// the state from here, never from Info, which may have moved on. An entry
	// the event bucket drops never reaches it; the attention entry of an
	// attention change the session applied always does.
	OnActivity func(sessionID string, e ActivityEntry, state AttentionState)
	// OnChat is called (outside the session lock) with the session ID and a
	// chat message as kept: a post, a join or leave line, a sent-to-agent
	// marker. The same contract as OnActivity: it runs on the posting
	// goroutine and must not block; calls can overlap.
	OnChat func(sessionID string, m ChatMessage)
	// RunChat, when set, is the chat of the run this session is a member of
	// (ChatRoom): its viewers read and post it over their own connection,
	// with scope "run", and are listed on its roster.
	RunChat *ChatRoom
	// Pattern, when set, is matched against the last line of the terminal
	// after patternQuiet without output. A match marks the session needs_input
	// (source "pattern", kind "prompt") unless it is that already. It is for
	// agents that neither run hooks nor ring the bell; a TUI that redraws
	// without pause never goes quiet and is not served by it.
	Pattern *regexp.Regexp
	// TrustPattern, when set, matches the agent's workspace-trust question in
	// the text of its screen, each escape sequence read as a space
	// (ScreenTail), after patternQuiet without output other than title
	// updates. A match marks the session needs_input (source "trust", kind
	// "prompt", the matched words as the message) unless it waits already,
	// until the first submission's Enter: the run engine types no prompt
	// while it shows, and a person answers the question.
	TrustPattern *regexp.Regexp
	// TrustAnswers are the question's answers as its quick-reply choices (the
	// catalog's trustAnswers): a label and the keys that pick it, cleaned and
	// bounded as any prompt's choices are (CleanOptions). A person's typed
	// text (Submit) is refused while the question shows (ErrTrustQuestion),
	// since its Enter would pick whatever the dialog highlights.
	TrustAnswers []Option
	// Questions are the agent's other startup questions (the catalog's
	// questions; round 13, G2c), held exactly as the trust question is:
	// source "trust", their answers the choices, typed text refused while
	// one shows. Codex's "Hooks need review" is one. A question marked
	// Anytime is one the agent asks during its work (Codex's command
	// approval): it is watched for the session's whole life on a model of the
	// screen (Screen), by a watcher of its own, with source "question"
	// (fireAsked, missAsked).
	Questions []StartupQuestion
	// WorkingTitle, when set, matches the window title (OSC 0 or 2) the
	// agent writes while it works (the catalog's signal.workingTitle:
	// Codex's "⠋ Working", with the title Conductor's launch asks it for).
	// The session keeps whether the last title it read matches
	// (readTitle), and a title that matches shows working (source
	// "title"), as the agent's working report would: over a bell, an OSC
	// notification, the agent's report, the screen pattern, the empty state
	// or the done of a turn an Enter has followed; never over a question
	// the session holds (a startup question, a dialog on the screen), a
	// held turn, the done of the turn just ended, nor a wait younger than
	// TitleSettle. A wait raised while the title says the agent works, or
	// met by such a title before its TitleSettle has passed, gives way to
	// working once it has, unless a title that does not match came between
	// (settled). Titles are not weighed without it.
	WorkingTitle *regexp.Regexp
	// TitleSettle is how long a wait stands before a title that says the
	// agent works takes its place; TitleSettle when zero.
	TitleSettle time.Duration
	// SubmitPause is the pause between a submission's text and its Enter;
	// SubmitPause when zero.
	SubmitPause time.Duration
	// ConfirmSubmit says the agent reports taking a prompt (Claude Code's
	// UserPromptSubmit hook reports working): a submission that asks for it
	// (Submission.Confirm) and is not taken within ConfirmWait gets one more
	// Enter.
	ConfirmSubmit bool
	// ConfirmWait is that wait; ConfirmWait when zero.
	ConfirmWait time.Duration
	// InputWait bounds how long a write to the process waits for the
	// program to take it (input.go); InputWait when zero.
	InputWait time.Duration
	// WatchGit records what the agent changed in a git working directory
	// without a hook naming the file (files seen by git, gitseen.go): the
	// server's and the host's agent sessions set it.
	WatchGit bool
	// Launched is what the session was launched with, for Resume.
	Launched Launched
	// Folder is the session's working directory, opened (and checked) by
	// whoever started the process in it: the Files tab reads and saves
	// through it, and the session closes it when it is retired. NewLocal
	// opens Info.Cwd itself when it is nil.
	Folder *Folder
	// Plans is the directory the agent keeps its plans in outside the
	// working directory (agents.PlansDir: Claude Code's ~/.claude/plans),
	// opened as a second folder for the plans the agent reports (plans.go);
	// "" for an agent without one.
	Plans string
}

// patternQuiet is how long the output must stay silent before the last line is
// matched against Options.Pattern.
const patternQuiet = 500 * time.Millisecond

// bellPace is the least time between two changes the bell or OSC
// notifications make (bellPaced): a burst of bells makes one.
const bellPace = 500 * time.Millisecond

// TitleSettle is how long a wait (a bell, an OSC notification, the agent's
// report) stands before a window title that says the agent works takes its
// place (Options.WorkingTitle): long enough for Codex to draw what it rang
// the bell for (an approval, whose title then says Action Required), short
// enough that a question Codex goes on from does not hold the session.
const TitleSettle = time.Second

// Local owns a PTY process and serves attached clients. It is used by the
// server for server-hosted sessions and by `conductor host` for hosted ones.
type Local struct {
	opts Options
	proc Process
	ring *Ring
	hub  *Hub
	log  *slog.Logger
	// folder is the working directory as opened (Options.Folder); nil when
	// it could not be opened, which refuses every file request.
	folder *Folder
	// plans is the agent's plans directory (Options.Plans) as opened, once
	// the agent reported a plan (openPlansLocked), plansReal its path with
	// its links resolved, and planFiles the session's plans by their real
	// paths (planKey). All under mu.
	plans     *Folder
	plansReal string
	planFiles map[string]bool

	mu   sync.Mutex // guards info, events and lastOutput, and orders ring writes against attaches
	info Info
	// renamed says a person renamed the session (Rename): a resume of it
	// starts under info.Name, not the name it was launched with. Under mu.
	renamed bool
	// stopRequested makes an exit observed by the pump report "stopped".
	stopRequested bool
	// retired says the session left the server's registry (Retire): no
	// client attaches any more.
	retired bool
	// closing holds the clients that have left the hub (Detach) while their
	// transport still closes: with the hub's, every client until its
	// transport has closed, for DisconnectLink and CloseAll to wait for.
	closing map[*Subscription]struct{}
	// passedOn holds the clients whose slot toward MaxViewers another took
	// over (AttachOptions.Replaces), still attached or still closing: they
	// no longer count toward the cap, while revokes and shutdowns still wait
	// for them. A client leaves it as its transport closes.
	passedOn map[*Subscription]struct{}
	// lastOutput is when the pump last read output; zero until it has.
	lastOutput time.Time

	activity activityRing
	// touched is the Touched section's index of the files the agent touched (touched.go), under mu.
	touched TouchedIndex
	// gitSeen watches the working tree for files no hook named (gitseen.go); its own lock.
	gitSeen gitSeen
	// sizer is the viewer whose window sizes the PTY, "" while none holds it
	// (round 14): one of the owner's own windows takes it as it attaches,
	// any controller by asking (ResizeWith take, Fit to my window), and
	// every other viewer follows the size, scaled to its window. Under mu.
	sizer string
	// takes counts the times the size was taken by asking (ResizeWith take,
	// Fit to my window), for a sizer's move to tell a Fit made meanwhile
	// from the hand-over its leave made (Subscription.leftSize). Under mu.
	takes uint64
	// nvimCount is the editors open on the session (nvim.go), every
	// connection's together.
	nvimCount atomic.Int32
	// editors are the editors open on the session, every connection's,
	// from their open to their forget (forgetNvim): the shutdown ends them
	// (endEditorsBy), those of a connection that went before it included,
	// which its subscription no longer names. Guarded by editorsMu.
	editorsMu sync.Mutex
	editors   map[*nvimEditor]struct{}
	chat      chatRing // guarded by mu
	// question is the id of the agent's question in the chat while one
	// stands (askInChat), for the line that says it was answered.
	question string
	// askedInChat is the set of questions the chat shows a card for (askInChat), until their answered or withdrawn line (#358); chatLeft the withdrawn lines not yet told. Guarded by mu.
	askedInChat map[string]struct{}
	chatLeft    []chatLeft
	// claim is the structured answer being written to a question (CLAIMED:
	// a reply naming it, claimLocked), its id and the claim's token, fresh
	// for every claim (claimSeq); the empty claim while none stands. The
	// question shows, or is held over by the hold of the turn (heldOver),
	// until the claim's write is made and resolve clears it, or the claim
	// is released (release) or the question withdrawn (forgetLocked); a
	// reply to it meanwhile is refused (waitsLocked). consumed is the id
	// of the question instance shown that keys typed in the terminal
	// reached (consumeLocked: a startup question's or a dialog's answer
	// picked, the hold's end entering its text): it takes no answer, and
	// goes once the screen shows it gone (escGone, missAsked) or the next
	// takes its place. dialogAnswered is the words of the dialog asked
	// during the work that a write was last made to answer or that keys
	// reached, published or not yet, until the screen's watcher sees it
	// go (a quiet look with no dialog, missAsked) or another dialog in its
	// place: a dialog with those words asked meanwhile is that question
	// still, consumed (sameDialog). dialogAnsweredAt is how many Feeds the
	// watcher had seen as it was noted (PatternWatcher.Fed): a look that
	// took its text before that (LookedAt) does not see it go. results
	// keeps the last resultsKept questions' results by id, oldest first
	// in resultIDs, for the word a late or duplicate reply gets. claims
	// are the claims standing, by question: on the one shown, and on one
	// held over by the hold of the turn (a dialog shown over the hold
	// takes a reply of its own, and the held-over question keeps its
	// claim, which resolves it hidden); a fresh token for every claim
	// (claimSeq). Guarded by mu.
	claims   map[string]questionClaim
	claimSeq uint64
	consumed string
	// strandedClaim is the claim of a reply's line typed and not entered
	// for good (holdTyped), let go in the step that shows the hold of the
	// turn in the question's place (setAttentionLooked,
	// releaseStrandedLocked): until then it stands, so that no reply
	// claims the question and writes its keys after the text. The empty
	// claim while none waits. holdPending says a hold of the turn is to
	// follow a stranded line (strand) and its words are not shown yet
	// (holdTyped): keys meanwhile deal with the question shown as the
	// hold's end would with the question held over, before the hold is
	// installed (rawKeys: then none is) or after (endHoldTyped).
	strandedClaim    questionClaim
	holdPending      bool
	dialogAnswered   string
	dialogAnsweredAt uint64
	results          map[string]*Answer
	resultIDs        []string
	// keysIn splits what is written to the program into the keys it reads
	// (keysWritten), every write made, in the writer's order. Guarded by mu.
	keysIn keyReader
	// startupAnswered says a write answered a startup question, and
	// startupAnsweredAt how many Feeds the startup questions' watcher had
	// seen then (Fed), as its text was put behind: a look of that watcher
	// that took its text no later (LookedAt) saw the answered screen, and
	// asks nothing (fireTrust). Guarded by mu.
	startupAnswered   bool
	startupAnsweredAt uint64
	// startupAnsweredWords are the words of the startup question last
	// answered, until another startup question takes its place: asked
	// again meanwhile (the program draws it again before it moves on), it
	// is asked answered, with an id no reply is taken for. Guarded by mu.
	startupAnsweredWords string
	// startupAnsweredFed is how many Feeds the screen the questions asked
	// during the work are read from had seen when a startup question was
	// last answered: asked again answered (startupAnsweredWords), it is held
	// so (heldAnswer) from then, and goes once that screen shows no startup
	// question. Guarded by mu.
	startupAnsweredFed uint64
	// heldAnswer is the startup question an Escape a write ended
	// with may have answered (holdStartupAnswered): its id once asked, its
	// words while not asked yet, who typed it, and how much output the
	// screen had been fed then. Held answered, it is cleared once the
	// screen shows no startup question (escGone). Guarded by mu.
	heldAnswer heldStartup
	// syncCut is how much of a synchronized update's end (syncEnd) the
	// output fed to the screen the questions asked during the work are read
	// from last ended with: a read that cut it off (feedAsked). Guarded by
	// feedMu.
	syncCut int
	// feedMu is held while output is fed to the startup questions' watcher
	// and the screen's (pump), and while escGone and heldGone look at the
	// screen and put the startup output behind: what they look at is all
	// that has been fed, and none is put behind unseen. Taken before mu.
	feedMu sync.Mutex

	events    EventBucket // guarded by mu
	dropped   atomic.Uint64
	scanner   Scanner
	pattern   *PatternWatcher   // nil without Options.Pattern
	trust     *PatternWatcher   // nil without a startup question
	questions []StartupQuestion // Options.TrustPattern's, then Options.Questions not marked Anytime
	// asked watches the screen for the questions the agent asks during its
	// work (Options.Questions marked Anytime, askedQuestions) until the
	// session ends; nil without one. askedKeys are their answers' keys.
	asked          *PatternWatcher
	askedQuestions []StartupQuestion
	askedKeys      map[string]bool
	// shot is a model of the screen every session keeps (Screen, no
	// pattern), looked at each time the output has been quiet for
	// patternQuiet (screenQuiet): a bell or OSC wait, which has no rows of
	// its own, is withdrawn once the screen has moved on from the rows it
	// showed at the first quiet after the wait (shotRows, the baseline;
	// shotWait the wait's id) by shotRowsChanged rows or more (movedOn).
	// shotRows and shotWait are guarded by mu.
	shot     *PatternWatcher
	shotWait string
	shotRows []string
	// lastBell is when the bell or an OSC notification last changed the
	// attention (bellPaced); zero once a new turn has begun (beginTurn).
	// Guarded by mu.
	lastBell time.Time
	// titleWorking says the last window title read matched
	// Options.WorkingTitle (readTitle). settleID is the id of the wait
	// shown last that a title may settle (needs_input, but no question the
	// session holds and no held turn; empty while none shows), settleAt the
	// time (monotonic) its TitleSettle ends, and settleTimer the timer that
	// gives it way to working then (settled), armed while the title says
	// the agent works, nil otherwise; settleGen tells a timer from the one
	// before. Kept only with Options.WorkingTitle. Guarded by mu.
	titleWorking bool
	settleID     string
	settleAt     time.Time
	settleTimer  *time.Timer
	settleGen    uint64
	// statusSeen says the agent has declared its program status in this
	// session (readStatus: an OSC 7501 sequence, Codex 0.163 and later),
	// after which the window title is not weighed. lastStatus is the state
	// it declared last and statusKind what blocks it then (StatusBlocked
	// with a kind): the agent writes its status only when it changes, so
	// neither is forgotten at a turn boundary (beginTurn), unlike the
	// title's reading; the rules of a turn apply as a status is read and as
	// a wait is settled (saysWorkingLocked). Guarded by mu.
	statusSeen bool
	lastStatus string
	statusKind string
	// statusReconciled says withdrawShownLocked put the status's wait in a
	// held question's place; the caller records its entry once unlocked
	// (recordStatusReconciled). Guarded by mu.
	statusReconciled bool
	// turnEnded says a done was reported and neither an Enter has been
	// written since (Input, Submit) nor another state shown: the turn it
	// ended is still the last one, and a bell or OSC notification now is
	// that turn's own (unlessTurnEnded). Guarded by mu.
	turnEnded bool
	// turn counts the turns begun after a done (beginTurn), changed under
	// mu: output read before one began, and scanned after, is the ended
	// turn's own (scanOutput). The pump takes it as soon as a read
	// returns, before anything else can hold it up.
	turn atomic.Uint64
	// scanBegun is turn as the chunk was read in which the escape sequence
	// the scanner has under way began (scanOutput); the pump's own.
	scanBegun uint64
	// beforeScan, a test's hook, runs with each chunk read between the
	// read (and the turn taken with it) and everything the pump does with
	// it.
	beforeScan atomic.Pointer[func(chunk []byte)]
	// beforeSettle, a test's hook, runs as the settle timer fires, before
	// it looks at anything (settled); afterTitle between readTitle's look
	// at a title that matches and its change.
	beforeSettle atomic.Pointer[func()]
	afterTitle   atomic.Pointer[func()]
	// beforeEnter, a test's hook, runs as a submission comes to its Enter
	// (pressEnter), before it looks at anything; beforeWritten as keys
	// written (input) come to what follows their write, with the keys.
	beforeEnter   atomic.Pointer[func()]
	beforeWritten atomic.Pointer[func(data []byte)]
	// beforeShot, a test's hook, runs as the screen snapshot's quiet look
	// begins (screenQuiet), before it takes the session's lock; beforeHold
	// as holdTyped begins, before the hold of the turn is installed;
	// beforeHoldShown once it is installed, before its words show.
	beforeShot      atomic.Pointer[func()]
	beforeHold      atomic.Pointer[func()]
	beforeHoldShown atomic.Pointer[func()]
	agentTokenHash  [32]byte
	hasAgentToken   bool

	ended chan struct{}

	// submitting holds a token while a submission runs (Submit): one at a
	// time per session. turnWaiters counts the submissions waiting for it
	// (awaitTurn), at most maxTurnWaiters.
	submitting  chan struct{}
	turnWaiters atomic.Int32
	// held is closed by the first keys a person types into the terminal
	// (input) while the session's turn is held for a submission's text
	// typed without its Enter (holdTyped); nil while it is not. heldNext is
	// closed as the next such hold begins, nil until someone asks
	// (Holding). Under mu.
	held     chan struct{}
	heldNext chan struct{}
	// heldOver is the question the hold of the turn took the place of as
	// it showed (an agent's, waiting on a line: the held text was typed
	// for it), shown again when a person's keys end the hold without
	// answering it (endHoldTyped); empty when none. Under mu.
	heldOver Attention
	// own is the agent's own question (Ask: a needs_input report that says
	// it is the agent's), kept until it is answered: shown (the attention
	// showing is it, its id), set aside (ownAside: in the turn it was
	// asked in, another state took its place, and the turn's done shows it
	// again, the same Attention), or held over by the hold of the turn
	// (heldOver is it). ownTurn is the turn it was asked in (turn): it is
	// in its turn while turn is that and no done has ended it (turnEnded).
	// A question a reply or a person's keys reached is removed, never set
	// aside (keepOwnLocked). Empty while none is kept. Under mu.
	own      Attention
	ownAside bool
	ownTurn  uint64
	// writer makes every write to the process, in order (input.go).
	writer procWriter
	// nvimEndWait is nvimEndWait as the session was made: its editors'.
	nvimEndWait time.Duration
	// paste follows the program's bracketed-paste mode (Submit, BracketedPaste).
	paste pasteMode
	// attnChanged is closed and replaced, under mu, on every attention change:
	// Submit waits on it for the agent to take a prompt.
	attnChanged chan struct{}
}

// NewLocal wraps a started process and begins pumping its output.
func NewLocal(info Info, proc Process, opts Options) *Local {
	if opts.ScrollbackBytes <= 0 {
		opts.ScrollbackBytes = 256 << 10
	}
	if opts.MaxViewers <= 0 {
		opts.MaxViewers = 32
	}
	if opts.FileView == "" {
		opts.FileView = "view"
	}
	if opts.Transport == "" {
		opts.Transport = proto.TransportWS
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = 5 * time.Second
	}
	if opts.SubmitPause <= 0 {
		opts.SubmitPause = SubmitPause
	}
	if opts.ConfirmWait <= 0 {
		opts.ConfirmWait = ConfirmWait
	}
	if opts.InputWait <= 0 {
		opts.InputWait = InputWait
	}
	if opts.TitleSettle <= 0 {
		opts.TitleSettle = TitleSettle
	}
	if info.Status == "" {
		info.Status = StatusRunning
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = time.Now().UTC()
	}
	folder := opts.Folder
	if folder == nil {
		folder, _ = OpenFolder(info.Cwd)
	}
	s := &Local{
		opts:   opts,
		folder: folder,
		proc:   proc,
		ring:   NewRing(opts.ScrollbackBytes),
		hub:    NewHub(),
		log:    opts.Log.With("session", info.ID),
		info:   info,
		ended:  make(chan struct{}),

		submitting:  make(chan struct{}, 1),
		attnChanged: make(chan struct{}),
		nvimEndWait: nvimEndWait,
	}
	if opts.Pattern != nil {
		s.pattern = NewPatternWatcher(opts.Pattern, patternQuiet, s.firePattern)
	}
	if opts.TrustPattern != nil {
		s.questions = append(s.questions, StartupQuestion{Pattern: opts.TrustPattern, Answers: opts.TrustAnswers})
	}
	for _, q := range opts.Questions {
		switch {
		case q.Pattern == nil:
		case q.Anytime:
			s.askedQuestions = append(s.askedQuestions, q)
		default:
			s.questions = append(s.questions, q)
		}
	}
	if len(s.questions) > 0 {
		// One watcher for every question: its pattern is theirs together, and
		// the fire tells which one shows.
		s.trust = NewScreenWatcher(anyOf(s.questions), patternQuiet, s.fireTrust)
	}
	if len(s.askedQuestions) > 0 || len(s.questions) > 0 {
		// The screen the questions asked during the work are read from. It
		// is also what tells a startup question held answered gone
		// (escGone): with startup questions alone it is kept for that,
		// matching nothing, until the startup questions' watch ends.
		re := matchNothing
		if len(s.askedQuestions) > 0 {
			re = anyOf(s.askedQuestions)
		}
		s.asked = NewScreenModelWatcher(re, patternQuiet, int(info.Cols), int(info.Rows), s.fireAsked, s.missAsked)
		s.askedKeys = map[string]bool{}
		for _, q := range s.askedQuestions {
			for _, a := range q.Answers {
				s.askedKeys[a.Input] = true
			}
		}
	}
	s.shot = NewScreenModelWatcher(matchNothing, patternQuiet, int(info.Cols), int(info.Rows), func(string) {}, s.screenQuiet)
	if opts.WatchGit && info.Cwd != "" {
		s.gitSeenStart()
	}
	go s.pump()
	return s
}

func (s *Local) pump() {
	buf := make([]byte, proto.MaxOutput)
	for {
		n, err := s.proc.Read(buf)
		// The turn the output was read in: taken at once, so that a turn
		// begun while the pump waits for the lock below leaves it the
		// ended turn's (scanOutput).
		turn := s.turn.Load()
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if hook := s.beforeScan.Load(); hook != nil {
				(*hook)(chunk)
			}
			// Output that only sets the window title (a spinner in it) does
			// not count as output for the quiet the run engine and the
			// trust watcher wait for.
			title := titleOnly.Match(chunk)
			s.mu.Lock()
			s.ring.Write(chunk)
			s.hub.BroadcastLoud(proto.Encode(proto.TypeOutput, chunk))
			if !title {
				s.lastOutput = time.Now()
			}
			s.mu.Unlock()
			s.paste.feed(chunk)
			s.scanOutput(chunk, turn)
			if s.pattern != nil {
				s.pattern.Feed(chunk)
			}
			var left string
			s.feedMu.Lock()
			if s.trust != nil && !title {
				s.trust.Feed(chunk)
			}
			if s.asked != nil && !title {
				left = s.feedAsked(chunk)
			}
			s.feedMu.Unlock()
			if left != "" {
				s.withdraw(left)
			}
			if !title {
				s.shot.Feed(chunk)
			}
		}
		if err != nil {
			break
		}
	}
	// The read side closes when the child exits; wait briefly for the status.
	select {
	case <-s.proc.Done():
	case <-time.After(5 * time.Second):
	}
	s.mu.Lock()
	status := StatusExited
	if s.stopRequested {
		status = StatusStopped
	}
	s.mu.Unlock()
	s.markEnded(status)
}

func (s *Local) markEnded(status Status) {
	// A prompt left on the screen by a process that is gone must not raise
	// needs_input after the session has ended, where typing could not clear it.
	// Stop waits for a fire in flight, so it comes before the lock: that fire
	// takes it.
	if s.pattern != nil {
		s.pattern.Stop()
	}
	if s.trust != nil {
		s.trust.Stop()
	}
	if s.asked != nil {
		s.asked.Stop()
	}
	s.shot.Stop()
	s.mu.Lock()
	if s.info.Status.Ended() {
		s.mu.Unlock()
		return
	}
	s.info.Status = status
	// An ended session needs nothing: what it waited on goes with its
	// process, in the critical section that ends it, so nothing ever sees an
	// ended session that still needs input. Its history stays in the activity
	// log.
	cleared := s.info.Attention.State != AttentionNone
	if cleared {
		s.info.Attention = Attention{}
		s.signalAttention()
	}
	// Nor is anything kept to show again: the agent's own question goes
	// with it, shown or set aside, and so do a claim on it, its
	// consumption and the screen's baseline.
	s.dropOwnLocked(ChatLeftAgent)
	s.claims, s.consumed = nil, ""
	s.shotWait, s.shotRows = "", nil
	s.settleLocked(s.info.Attention)
	now := time.Now().UTC()
	s.info.EndedAt = &now
	var exitCode *int
	select {
	case <-s.proc.Done():
		st := s.proc.Exit()
		code := st.Code
		exitCode = &code
		s.info.ExitCode = exitCode
	default:
	}
	if cleared {
		s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
	}
	frame := proto.MustControl(proto.Status{T: proto.CtlStatus, Status: string(status), ExitCode: exitCode})
	s.hub.Broadcast(frame)
	s.mu.Unlock()
	close(s.ended)
	attrs := []any{"status", status}
	msg := string(status)
	if exitCode != nil {
		attrs = append(attrs, "exitCode", *exitCode)
		msg = fmt.Sprintf("%s (exit %d)", status, *exitCode)
	}
	s.log.Info("session ended", attrs...)
	s.Record(ActivityEntry{Type: ActivityStatus, Message: msg})
	s.notifyChange()
}

// Record appends an activity entry, broadcasts it to attached clients and
// passes it to OnActivity. What an agent reports (the six event types) is
// limited to EventRatePerSecond entries a second on average and EventBurst at
// once: beyond that Record does nothing else, counts the entry in Dropped and
// returns false. join, leave, input, link and status entries are the
// session's and the server's own; they skip the limit and spend no tokens, so
// a chatty hook cannot starve the roster rows or the final status row. The
// attention entry of an attention change the session applies skips it too:
// the session records that one itself (recordOwn). An attention entry handed
// to Record comes from outside the session and is limited like an event,
// although nothing records one that way today.
func (s *Local) Record(e ActivityEntry) bool {
	return s.record(e, bucketed(e.Type), "")
}

// recordOwn records an entry the session makes itself without asking the
// event bucket, whatever its type, and hands OnActivity the attention state
// it records. It records the attention entry of an attention change the
// session has applied, which must not be dropped while the state it records
// is showing: a report from outside paid its token before the change
// (TrySetAttentionFull), and what the session observes itself (the bell, OSC
// notifications, the screen pattern) is held to its own pace and spends none.
func (s *Local) recordOwn(e ActivityEntry, state AttentionState) {
	s.record(e, false, state)
}

// record is Record, with limited saying whether the entry spends a token of
// the event bucket, and state what OnActivity is told the entry records.
func (s *Local) record(e ActivityEntry, limited bool, state AttentionState) bool {
	s.mu.Lock()
	if limited && !s.events.Take(time.Now()) {
		log := s.log
		s.mu.Unlock()
		countDrop(&s.dropped, log, e.Type)
		return false
	}
	// A file event repeating the newest one (the same path and op within
	// FileCoalesce: an agent saving a file forty times) is one entry: the
	// newest's time moves, nothing is appended or broadcast again.
	if e.Type == ActivityFile && s.activity.CoalesceFile(CleanEntry(e)) {
		s.noteTouched(CleanEntry(e), true)
		s.notePlanLocked(CleanEntry(e))
		s.mu.Unlock()
		s.gitSeenReported(e)
		return true
	}
	// The ring write and the broadcast share one critical section, so a client
	// attaching meanwhile finds the entry in its replay or receives the
	// broadcast, never both.
	e = s.activity.Add(e)
	if e.Type == ActivityFile {
		s.noteTouched(e, false)
		s.notePlanLocked(e)
	}
	s.hub.Broadcast(proto.MustControl(EntryToProto(e)))
	id := s.info.ID
	s.mu.Unlock()
	switch e.Type {
	case ActivityFile:
		s.gitSeenReported(e)
	case ActivityToolUse, ActivityAttention:
		// A tool ran, or a turn ended or asked: a look at the tree a moment later.
		s.gitSeenPoke()
	}
	if s.opts.OnActivity != nil {
		s.opts.OnActivity(id, e, state)
	}
	return true
}

// countDrop counts in dropped something of type typ that the event bucket
// refused, an entry or a report, and logs the first and every 100th at debug
// level, so that a flood does not become a log flood.
func countDrop(dropped *atomic.Uint64, log *slog.Logger, typ string) {
	if n := dropped.Add(1); n == 1 || n%100 == 0 {
		log.Debug("activity dropped by the event rate limit", "type", typ, "dropped", n)
	}
}

// Dropped counts what the session's event bucket refused: the entries Record
// did not record and the reports TrySetAttentionFull did not apply.
func (s *Local) Dropped() uint64 { return s.dropped.Load() }

// Activity returns the activity log, oldest first.
func (s *Local) Activity() []ActivityEntry { return s.activity.Snapshot() }

// notifyChange hands a fresh Info snapshot to the OnChange hook.
func (s *Local) notifyChange() {
	// A question's withdrawn line (#358) goes first: its card loses its choices as the state that replaced the question is told.
	s.tellChatLeft()
	if s.opts.OnChange != nil {
		s.opts.OnChange(s.Info())
	}
}

// scanOutput looks for bell/OSC signals in a chunk of terminal output. A
// burst of bells makes at most one change per bellPace (bellPaced). They
// are the session's own observations and spend no token of the event
// bucket.
//
// A bare bell says only that something waits: a session that waits already
// keeps what it waits on (a question's words and choices, a hook's report),
// which the bell would have replaced with "terminal bell" and no choice. An
// OSC notification has words of its own and replaces a wait that has none
// to answer it with, but never a question with its choices or one the
// session holds (unlessChoices). Neither undoes a done until the next Enter
// (unlessTurnEnded): it is the ended turn's own, as Codex's bell is. Nor
// does one read during the ended turn (turn, the count of turns begun as
// the chunk was read; for a notification begun in an earlier chunk, that
// chunk's) and looked at once the next has begun: a done and an Enter that
// come between the read and the look leave it the last turn's. A window
// title is weighed only with Options.WorkingTitle (readTitle), with the
// same turn. Only the pump calls it.
func (s *Local) scanOutput(chunk []byte, turn uint64) {
	begun := s.scanBegun
	events := s.scanner.Scan(chunk)
	if !s.scanner.Through() {
		// A sequence left unfinished began in this chunk.
		s.scanBegun = turn
	}
	for i, ev := range events {
		read := turn
		if i == 0 && s.scanner.Carried() {
			read = begun
		}
		if ev.Source == SourceStatus {
			s.readStatus(ev.Message, ev.Kind, read)
			continue
		}
		if ev.Source == SourceTitle {
			if s.opts.WorkingTitle != nil {
				s.readTitle(ev.Message, read)
			}
			continue
		}
		msg, rule := ev.Message, unlessChoices
		if msg == "" {
			msg, rule = "terminal bell", unlessWaiting
		}
		s.setAttentionRead(AttentionNeedsInput, msg, ev.Source, "", nil, rule|unlessTurnEnded|bellPaced, read)
	}
}

// titleRules are how a window title that says the agent works is applied
// (readTitle, settled): as the agent's working report would be, but over
// nothing the screen or the session holds, not over the done of the turn
// just ended, not over a wait younger than its TitleSettle, and as no
// change while the session shows working already or has ended. It spends
// no token and stamps no bell's pace: it is the session's own observation,
// recorded only when it changes the state.
const titleRules = unlessTurnEnded | unlessHeld | unlessOnScreen | unlessFresh | unlessWorking | unlessEnded

// readTitle weighs a window title read in turn (Options.WorkingTitle). One
// that does not match ("[ ! ] Action Required", "Ready", "Starting", a cut
// one) says the agent shows something for a person, or is at rest: it
// changes nothing, and the wait showing stands (the settle timer is
// disarmed). One that matches shows working (titleRules); a wait younger
// than its TitleSettle is left to the settle timer, armed for what is left
// of it: the title is written only when its text changes, once per state
// with animations off, so a later title may never come.
//
// A title that matches read during the ended turn and looked at once the
// next has begun (turn) is that turn's own: it changes nothing, not even
// what the last title says, so that it arms no timer for the next turn's
// wait. One that does not match is weighed whenever it was read: it can
// only keep a wait standing.
func (s *Local) readTitle(title string, turn uint64) {
	working := s.opts.WorkingTitle.MatchString(title)
	s.mu.Lock()
	if s.statusSeen {
		// The agent declares its state in its program status (readStatus):
		// the title says the same, or less (Codex's "Action Required" over
		// an async question it works on).
		s.mu.Unlock()
		return
	}
	if working && turn != anyTurn && turn != s.turn.Load() {
		s.mu.Unlock()
		return
	}
	s.titleWorking = working
	if !working {
		s.disarmSettleLocked()
		s.mu.Unlock()
		return
	}
	if cur := s.info.Attention; cur.State == AttentionNeedsInput && cur.ID != "" && cur.ID == s.settleID && time.Now().Before(s.settleAt) {
		s.armSettleLocked()
	}
	s.mu.Unlock()
	if hook := s.afterTitle.Load(); hook != nil {
		(*hook)()
	}
	s.setAttentionRead(AttentionWorking, "", SourceTitle, "", nil, titleRules, turn)
}

// saysWorkingLocked reports whether the agent's last word on its state
// says it works: its program status once it has declared one (lastStatus),
// its window title before that (titleWorking). The caller holds s.mu.
func (s *Local) saysWorkingLocked() bool {
	if s.statusSeen {
		return s.lastStatus == StatusWorking
	}
	return s.titleWorking
}

// statusWords are the words of the wait a blocked status raises, by what
// blocks the program (Event.Kind); statusKindOf the attention kind of it.
func statusWords(kind string) string {
	switch kind {
	case "permission":
		return "The agent asks for a permission"
	case "question":
		return "The agent asks a question"
	case "auth":
		return "The agent needs you to sign in"
	}
	return "The agent waits"
}

func statusKindOf(kind string) string {
	switch kind {
	case "permission":
		return KindPermission
	case "question":
		return KindPrompt
	}
	return ""
}

// readStatus weighs a program status read in turn (SourceStatus: Codex
// 0.163 and later declare theirs, see Scanner). It is the agent's own word
// on its state, kept as read (lastStatus, statusKind): the agent writes it
// only when it changes, so it is never forgotten at a turn boundary, and
// after the first the window title is not weighed (readTitle).
//
// Working is weighed as a title that matches (titleRules): a wait younger
// than its TitleSettle is left to the settle timer, armed for what is left
// of it; one read during the ended turn changes nothing shown (turn), but
// remains the state the next turn's wait settles against. Blocked, idle,
// done, error, clear and a state not understood (StatusUnknown) are weighed
// as a title that does not match: the wait showing stands, the settle timer
// is disarmed. Blocked with a kind (0.164: what blocks the program) raises
// a wait of its own, with words naming the kind, over nothing with words of
// its own (unlessWorded: a bare bell's wait gives way to it) and not over
// the done of the turn just ended; blocked without one (0.163, where an
// async question the agent works on blocks too) raises nothing. A wait the
// status raised is the status's to withdraw: a later state that is neither
// blocked nor working (the form cancelled, the turn interrupted, the agent
// gone) withdraws it at once; working lets it settle as any wait.
func (s *Local) readStatus(state, kind string, turn uint64) {
	s.mu.Lock()
	s.statusSeen = true
	s.lastStatus, s.statusKind = state, kind
	if state == StatusWorking {
		if cur := s.info.Attention; cur.State == AttentionNeedsInput && cur.ID != "" && cur.ID == s.settleID && time.Now().Before(s.settleAt) {
			s.armSettleLocked()
		}
		s.mu.Unlock()
		s.setAttentionRead(AttentionWorking, "", SourceStatus, "", nil, titleRules, turn)
		return
	}
	s.disarmSettleLocked()
	if state != StatusBlocked {
		if att := s.info.Attention; att.State == AttentionNeedsInput && att.Source == SourceStatus && att.ID != "" && !s.claimedLocked(att.ID) {
			s.withdrawShownLocked(SourceScreen)
			s.mu.Unlock()
			s.notifyChange()
			return
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if kind != "" {
		s.setAttentionRead(AttentionNeedsInput, statusWords(kind), SourceStatus, statusKindOf(kind), nil, unlessWorded|unlessTurnEnded, turn)
	}
}

// settleLocked follows the attention now showing, att, for the settle
// timer: a wait a title may settle (needs_input, but no question the
// session holds and no held turn) begins its TitleSettle, and the timer is
// armed for it while the agent's last word says it works (saysWorkingLocked);
// anything else ends the last one's. Only with Options.WorkingTitle, or
// once the agent has declared its program status. The caller holds s.mu,
// and calls it wherever the attention shown changes.
func (s *Local) settleLocked(att Attention) {
	if s.opts.WorkingTitle == nil && !s.statusSeen {
		return
	}
	if att.State == AttentionNeedsInput && att.ID != "" && att.ID == s.settleID {
		return // the same wait, still showing
	}
	s.disarmSettleLocked()
	s.settleID = ""
	if att.State != AttentionNeedsInput || att.ID == "" || att.Source == SourceSubmit || heldSource(att.Source) {
		return
	}
	s.settleID, s.settleAt = att.ID, time.Now().Add(s.opts.TitleSettle)
	if s.saysWorkingLocked() {
		s.armSettleLocked()
	}
}

// armSettleLocked arms the settle timer for the wait settleID names, to
// fire as its TitleSettle ends (settleAt), unless it is armed already. The
// caller holds s.mu.
func (s *Local) armSettleLocked() {
	if s.settleTimer != nil || s.settleID == "" {
		return
	}
	s.settleGen++
	gen := s.settleGen
	s.settleTimer = time.AfterFunc(time.Until(s.settleAt), func() { s.settled(gen) })
}

// disarmSettleLocked stops the settle timer: a timer that has fired
// already finds itself disarmed (settled). The caller holds s.mu.
func (s *Local) disarmSettleLocked() {
	if s.settleTimer != nil {
		s.settleTimer.Stop()
		s.settleTimer = nil
	}
}

// settled is the settle timer's fire, gen the timer's: the wait it was
// armed for, if it still shows, the timer still armed for it and the last
// title still says the agent works (no title that does not match came
// since, which disarms it), gives way to working, with the title's rules
// (titleRules): a done, a question on the screen, a held turn or another
// wait that came meanwhile stays. All of that is weighed under the lock
// that applies the change (onlySettling): a title read between the fire
// and the change counts.
func (s *Local) settled(gen uint64) {
	if hook := s.beforeSettle.Load(); hook != nil {
		(*hook)()
	}
	// The working shown is the witness's: the status's once the agent has
	// declared one, the title's before.
	source := SourceTitle
	s.mu.Lock()
	if s.statusSeen {
		source = SourceStatus
	}
	s.mu.Unlock()
	s.setAttentionLooked(AttentionWorking, "", source, "", nil, titleRules|onlySettling, anyTurn, gen, "")
}

// firePattern is the pattern watcher's fire: the last line matched after the
// output went quiet. A session that is waiting already keeps what marked it so
// (a hook's message, kind and options, the bell), and the same prompt does not
// report itself again while it stays on the screen. That is decided under the
// lock that sets the state: a report that lands between a look at the state and
// the set would be overwritten.
func (s *Local) firePattern(line string) {
	s.setAttention(AttentionNeedsInput, promptMessage(line), SourcePattern, KindPrompt, nil, unlessWaiting)
}

// fireTrust is the trust watcher's fire: one of the agent's startup
// questions (the workspace-trust question, then Options.Questions) is on
// the screen, text its last ScreenTail. The session needs input, with the
// words of the question drawn last in that tail (one that keeps what came
// before, lastStartup) and its answers, unless it waits already (on
// anything but a startup question answered, or one the screen has left for
// this one). A look
// that took its text before a startup question was answered (Reset does
// not wait for one under way) saw the answered screen, and asks nothing:
// asked again, the question would take a second answer, which the next
// would get (Codex's update question after its background-server one,
// where 1 runs npm install). That is weighed under the lock that sets the
// question (startupLook).
func (s *Local) fireTrust(text string) {
	if q, words := s.startupShown(text); q != nil {
		if s.heldGone(words) {
			// An Escape typed at it before it was asked took it away.
			return
		}
		s.setAttentionLooked(AttentionNeedsInput, words, SourceTrust, KindPrompt, CleanOptions(q.Answers), unlessWaiting|startupLook, anyTurn, s.trust.LookedAt(), "")
		// One held answered that the screen no longer shows (the program
		// took the Escape) is cleared.
		s.escGone()
	}
}

// fireAsked is the fire of the watcher for the questions the agent asks
// during its work (Options.Questions marked Anytime): one is on the screen,
// text the screen's (Screen.Text). The session needs input with source
// "question", the words of the first question found and its answers. It
// takes the place of whatever the session shows but a startup question
// (unlessStartup): the bell that rang for the dialog, an OSC notification,
// a report, with choices of its own or none, a done, its own question with
// other words (the next of a queue). The dialog on the screen is what the
// program waits on now, and its choices are the only safe answer: typed
// text, or another question's choice, would reach the dialog's keys. A
// dialog with other words than the one last answered is that one gone
// (dialogAnswered); one with its words is that question still (sameDialog).
// The question it takes the place of, answered by a key typed while it
// showed that did not clear it (an Escape, which may have been read with
// what came next), is answered by that key, as when it goes (missAsked).
func (s *Local) fireAsked(text string) {
	s.escGone()
	// The look's own (Look): the width text was read at, and whether it
	// is a whole frame. Half a frame (a synchronized update under way, or
	// one drawn at another size not yet erased and drawn again) shows what
	// the program has not finished drawing: it asks, and forgets no dialog
	// answered. Output since the look (the frame's end, a resize) does not
	// change that: the look decides on the screen it saw.
	look, settled, cols := s.asked.Look()
	_, words, answers, whole := s.askedIn(text, cols)
	if words == "" {
		// Found, and no list to read on it (a question whose answers are
		// read off the screen): the screen shows none of the questions.
		s.missAsked(text)
		return
	}
	s.mu.Lock()
	if settled && !sameWords(s.dialogAnswered, words) {
		s.dialogGone(look)
	}
	s.mu.Unlock()
	if whole != "" {
		words += "\n" + listMark + "\n" + whole
	}
	// The plan's approval ends in a footer of its own (#289): the dialog is
	// marked the approval (planApproval), and carries the plan's path the
	// footer prints, once the path is seen to be a plan, in the one step
	// that sets it.
	rules := unlessStartup | sameDialog
	if planApprovalShown(text) {
		rules |= planApproval
	}
	s.setAttentionWith(AttentionNeedsInput, words, SourceQuestion, KindPermission, CleanOptions(answers), rules, planFooter(text))
}

// missAsked is the same watcher's look at a quiet screen that shows none of
// the questions: the dialog last answered has gone (dialogAnswered), and a
// question asked during the work still shown is withdrawn: the screen
// moved on from it (the program took a key, Conductor's or not, a click,
// or went on by itself), and one drawn again is asked again. A reply being
// written to it keeps its claim (withdraw): it resolves or lets go.
func (s *Local) missAsked(string) {
	s.escGone()
	look, settled, _ := s.asked.Look()
	if !settled {
		// Half a frame (fireAsked), the look's own: the dialog answered
		// may be drawn again in the rest of it.
		return
	}
	s.mu.Lock()
	s.dialogGone(look)
	att := s.info.Attention
	gone := att.State == AttentionNeedsInput && att.Source == SourceQuestion && att.ID != ""
	s.mu.Unlock()
	if gone {
		s.withdraw(att.ID)
	}
}

// startupOnScreen reports whether the screen still shows a startup
// question while the startup questions' watch lasts (before the first
// submission's Enter): typed text is refused meanwhile (Submit), the
// question asked, answered or not, since its Enter would pick what the
// dialog highlights (Claude Code's "No, exit") and its letters reach the
// dialog's keys. The screen is read as a frame the program has finished
// drawing with nothing lost to a window made smaller (Screen.DrawnAt), the
// window grown since counting, its wrapped rows joined. Nothing without
// the screen the questions asked during the work are read from, kept for
// this with startup questions alone.
func (s *Local) startupOnScreen() bool {
	if s.trust == nil || !s.trust.Watching() || s.asked == nil {
		return false
	}
	whole, _, _, _ := s.asked.Whole()
	uncut, _, _ := s.asked.DrawnAt()
	return uncut && s.lastStartup(whole) != ""
}

// startupMayShow reports whether keys typed in the terminal may reach a
// startup question, while the startup questions' watch lasts: the screen
// (the frame the questions asked during the work are read from, taken as
// one reading, Frame) shows one; or is half a frame (a synchronized update
// under way) or cut by a window made smaller (DrawnAt) while the output's
// tail saw a startup question drawn, so that what it shows is not known and
// the keys are taken to have reached that dialog rather than the text
// (rawKeys keeps the hold); or shows a dialog's numbered
// choices with no heading (startupChoiceShown), the heading scrolled off a
// short screen while its choices stay. A window grown, nothing lost,
// leaves the frame readable though not laid out for its size until erased
// whole (Settled): what it shows is read. A dialog the program erased, row
// by row or whole, leaves none of these, and the keys enter the text. The
// tail alone is no witness for this: a footer redrawn in place evicts a
// heading still on the screen (and may leave the tail unable to say which
// question was drawn), and an erased dialog stays in it.
func (s *Local) startupMayShow() bool {
	if s.trust == nil || !s.trust.Watching() || s.asked == nil {
		return false
	}
	whole, _, uncut, _, _ := s.asked.Frame()
	if !uncut {
		// Half a frame, or one cut by a window made smaller: what it shows
		// is not known. A startup question the output's tail saw drawn may
		// be on it, and the keys are taken to have reached it. With none
		// drawn there is no dialog to reach: a screen cut at the start (the
		// first page attaching resizes it) by a program that prints lines
		// and never erases whole stays cut, and a submission's Enter would
		// otherwise never be written (the crews' first prompts, #263).
		return s.lastStartup(s.trust.Text()) != ""
	}
	if s.lastStartup(whole) != "" {
		return true
	}
	return startupChoiceShown(whole)
}

// choiceRow is a dialog's numbered choice as a row of the screen shows it:
// the number and the choice's words ("  2. Quit"), with the marker of the
// one highlighted before it or not ("› 1. Trust and continue", Codex; "❯ 1.
// Yes, I trust this folder", Claude Code).
var choiceRow = regexp.MustCompile(`^\s*(?:[›>❯]\s*)?\d+\.\s+\S`)

// startupChoiceShown reports whether whole, a screen's text with no startup
// question's heading on it, shows a choice of a dialog: one numbered row
// with words is enough, since on a short screen, or after the dialog's
// trailing line scrolled it up once more, the heading and the highlighted
// first choice are gone and the last choice alone stays with the footer. A
// dialog whose heading scrolled off, or was evicted from the output's tail
// by a footer redrawn in place, still shows what stays of its choices, and
// a key typed then reaches it. The choices are read by their shape, not
// their words: the catalog's answer labels name what an answer does ("Yes,
// trust this folder"), not what the dialog draws ("Trust and continue"). A
// numbered line in the program's own output during its startup reads as a
// choice too, and keeps a hold the keys may have entered: that errs on the
// side of keeping the hold, never of dropping it.
func startupChoiceShown(whole string) bool {
	for _, row := range strings.Split(whole, "\n") {
		if choiceRow.MatchString(row) {
			return true
		}
	}
	return false
}

// heldSource reports whether a prompt of source src is a question the
// session holds: the agent's trust or other startup question, or a
// question it asks during its work. Typed text is refused while one shows
// (Submit), and only its choice or an Enter answers it (Input).
func heldSource(src string) bool { return src == SourceTrust || src == SourceQuestion }

// matchNothing is a pattern no text matches.
var matchNothing = regexp.MustCompile(`[^\s\S]`)

// anyOf is one pattern for the questions together: a watcher's, whose fire
// tells which one shows.
func anyOf(qs []StartupQuestion) *regexp.Regexp {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = "(?:" + q.Pattern.String() + ")"
	}
	return regexp.MustCompile(strings.Join(parts, "|"))
}

// StartupQuestion is one question an agent asks as it starts (Options.Questions):
// the pattern of its words on the screen and its answers as choices. One
// marked Anytime is asked during the agent's work instead.
type StartupQuestion struct {
	Pattern *regexp.Regexp
	// Anytime marks a question the agent can ask at any time in the
	// session's life (the catalog's anytime): watched until the session
	// ends, not only until the first submission.
	Anytime bool
	// Words, when set, picks the words a page shows out of the same text
	// (the catalog's words): its last match's capturing groups joined by a
	// space, or that whole match without a group. Without it, or when it
	// matches nothing, the words are what Pattern matched last: a text that
	// keeps what came before (the startup watcher's ScreenTail) shows the
	// question drawn last there.
	Words   *regexp.Regexp
	Answers []Option
	// FromScreen has the answers read off the screen each time the
	// question is asked (the catalog's answersFromScreen, with Anytime):
	// the numbered list its dialog draws (readList), in place of Answers,
	// and without Words its words are the rows above that list.
	FromScreen bool
	// Mode, when set, matches the labels of the answers that change the
	// session's permission mode (the catalog's modeAnswers): they are
	// marked so (Option.Mode).
	Mode *regexp.Regexp
	// Typed, when set, matches the answers read off the screen that take
	// typed words, each whole (dialogList.whole: Claude Code's "Tell Claude
	// what to change" with its hint, the catalog's typedAnswers): while the
	// highlight stands before one, the dialog's keys type into its field,
	// and the question offers no choices.
	Typed *regexp.Regexp
}

// read returns what text, the screen the questions asked during the work
// are read from, shows of q: its words (words) and its answers, those its
// dialog lists when q reads them off the screen (FromScreen: readList,
// the labels the words when no row stands above the list), q's own
// otherwise, the ones that change the permission mode marked (Mode); and
// for answers read off the screen the whole list (readList's), what a
// reply is weighed against (stillOnScreen). While the highlight stands
// before a choice that takes typed words (Typed), the dialog's keys type
// into its field: no answers, and no list (it changes with each letter).
// "" and none when text does not show it, or shows no list to read.
func (q StartupQuestion) read(text string) (words string, answers []Option, whole string) {
	if words = q.words(text); words == "" {
		return "", nil, ""
	}
	if !q.FromScreen {
		answers = q.Answers
		if q.Mode != nil {
			answers = slices.Clone(answers)
			for i := range answers {
				answers[i].Mode = q.Mode.MatchString(answers[i].Label)
			}
		}
		return words, answers, ""
	}
	l, ok := readList(text, q.Mode)
	if !ok {
		return "", nil, ""
	}
	answers, whole = l.choices, strings.Join(l.whole, "\n")
	if q.Typed != nil && l.at >= 0 && q.Typed.MatchString(l.whole[l.at]) {
		answers, whole = nil, ""
	}
	if q.Words == nil {
		if words = l.words; words == "" {
			labels := make([]string, len(l.choices))
			for i, o := range l.choices {
				labels[i] = o.Label
			}
			words = strings.Join(labels, "\n")
		}
	}
	return words, answers, whole
}

// listMark begins, in the words fireAsked asks a dialog with, its whole
// list (StartupQuestion.read), which setAttentionLooked keeps apart
// (Attention.list): neither what tells the question from the next nor
// what it shows. A character of Unicode's private use area, which a
// screen's words do not hold.
const listMark = "\ue001"

// words returns the question's words when text shows it, "" when it does
// not: as rows (row breaks kept, each row trimmed, blank rows left out),
// whole. A dialog asked during the work (Anytime), read off the screen,
// keeps the white space within its rows as drawn (a command's own: two
// spaces in a quoted path are not one); a startup question's runs of white
// space are one space each, as the output its watcher keeps reads them.
// The rows are what tells one question from the next (sameWords: a dialog
// answered, asked again; a startup question still on the screen); the
// message shown is cut from them (shownWords).
func (q StartupQuestion) words(text string) string {
	all := q.Pattern.FindAllString(text, -1)
	if len(all) == 0 || all[len(all)-1] == "" {
		return ""
	}
	words := all[len(all)-1]
	if q.Words != nil {
		if all := q.Words.FindAllStringSubmatch(text, -1); len(all) > 0 {
			last := all[len(all)-1]
			groups := last[1:]
			if len(groups) == 0 {
				groups = last[:1]
			}
			var parts []string
			for _, g := range groups {
				if g = strings.TrimSpace(g); g != "" {
					parts = append(parts, g)
				}
			}
			if len(parts) > 0 {
				words = strings.Join(parts, " ")
			}
		}
	}
	return asRows(words, q.Anytime)
}

// asRows is text as a question's rows (words): each row trimmed, blank rows
// left out, the rows joined by line breaks; within a row its white space as
// it is with keep, else each run of it one space.
func asRows(text string, keep bool) string {
	var rows []string
	for _, row := range strings.Split(text, "\n") {
		if keep {
			row = strings.TrimSpace(row)
		} else {
			row = strings.Join(strings.Fields(row), " ")
		}
		if row != "" {
			rows = append(rows, row)
		}
	}
	return strings.Join(rows, "\n")
}

// cleanRows is m without its control characters but line breaks and tabs,
// as CleanMessage, and not cut.
func cleanRows(m string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, m)
}

// shownWords is the message a question's rows show as: every run of white
// space one space, and words longer than a message can be cut on a
// character boundary with an ellipsis at their end, so that a long command
// reads as cut rather than as all there is.
func shownWords(rows string) string {
	_, rows = widthOf(rows)
	words := strings.Join(strings.Fields(rows), " ")
	const more = "…"
	if len(words) > MaxAttentionMessage {
		cut := MaxAttentionMessage - len(more)
		for cut > 0 && !utf8.RuneStart(words[cut]) {
			cut--
		}
		words = words[:cut] + more
	}
	return words
}

// sameWords reports whether a and b, questions' words as rows (words), are
// the words of one question: the same but where a row break in one stands
// where the other has a space, or nothing (the screen's width broke the
// rows there: between two words, or inside one the program cut at the
// edge). Elsewhere their white space is the same, run for run: a space in
// one and none in the other ("build output" and "buildoutput", each on one
// row), or one space and two ("build output" and "build  output"), is two
// questions.
func sameWords(a, b string) bool {
	if a == b {
		return true
	}
	ca, a := widthOf(a)
	cb, b := widthOf(b)
	if ca != 0 && ca == cb {
		// Read at one width, its rows broken where the program breaks
		// them: a row break is the program's own (a line break in a
		// command), and the words are the same row for row.
		return a == b
	}
	ta, ga, ba := wordMarks(a)
	tb, gb, bb := wordMarks(b)
	if ta != tb {
		return false
	}
	for i := range ga {
		if ga[i] != gb[i] && !ba[i] && !bb[i] {
			return false
		}
	}
	return true
}

// wordMarks returns words without their white space, and for each place in
// that (before the byte at its index, its end included), the white space
// words has there within a row ("" for none), and whether a row break.
func wordMarks(words string) (string, []string, []bool) {
	var b strings.Builder
	gap, brk := []string{""}, []bool{false}
	for _, r := range words {
		at := b.Len()
		switch r {
		case '\n':
			brk[at] = true
		case ' ', '\t':
			gap[at] += string(r)
		default:
			b.WriteRune(r)
			for len(gap) <= b.Len() {
				gap, brk = append(gap, ""), append(brk, false)
			}
		}
	}
	return b.String(), gap, brk
}

// widthMark begins and ends the width a dialog's words were read at
// (atWidth): a character of Unicode's private use area, which a screen's
// words do not begin with.
const widthMark = "\ue000"

// atWidth is a dialog's words (StartupQuestion.words) read off a screen
// cols wide, with that width before them (widthOf), which tells whether a
// row break is the program's own (sameWords); "" for none.
func atWidth(words string, cols int) string {
	if words == "" || cols <= 0 {
		return words
	}
	return widthMark + strconv.Itoa(cols) + widthMark + words
}

// widthOf returns the width words were read at (atWidth), 0 for none, and
// the words without it.
func widthOf(words string) (int, string) {
	rest, ok := strings.CutPrefix(words, widthMark)
	if !ok {
		return 0, words
	}
	num, after, ok := strings.Cut(rest, widthMark)
	if !ok {
		return 0, words
	}
	cols, err := strconv.Atoi(num)
	if err != nil || cols <= 0 {
		return 0, words
	}
	return cols, after
}

// words returns the question's words a's are, as rows (StartupQuestion.words):
// those it was asked with, or its message.
func (a Attention) words() string {
	if a.rows != "" {
		return a.rows
	}
	return a.Message
}

// signalAttention wakes whoever waits for an attention change (Submit). The
// caller holds s.mu.
func (s *Local) signalAttention() {
	close(s.attnChanged)
	s.attnChanged = make(chan struct{})
}

// promptMessage is the attention message for the prompt on a line. A message
// is cut at MaxAttentionMessage bytes from its start, and it is the end of a
// long line that says what the agent waits for, so the line is trimmed at its
// start instead, on a character boundary.
func promptMessage(line string) string {
	const prefix = "prompt: "
	if room := MaxAttentionMessage - len(prefix); len(line) > room {
		line = line[len(line)-room:]
		// The cut may have landed inside a character (so may the tracker's, on
		// a line longer than it keeps): the next character starts at most 3
		// bytes on.
		for i := 0; i < utf8.UTFMax-1 && len(line) > 0 && !utf8.RuneStart(line[0]); i++ {
			line = line[1:]
		}
	}
	return prefix + line
}

// SetAttention records an attention change without prompt details. See
// SetAttentionFull.
func (s *Local) SetAttention(state AttentionState, message, source string) {
	s.SetAttentionFull(state, message, source, "", nil)
}

// SetAttentionFull records an attention change, broadcasts it to attached
// clients and notifies OnChange. AttentionNone clears the signal along with
// any kind and options. Unknown kinds are dropped rather than rejected. It
// asks the event bucket for nothing, and the attention entry of the change is
// always recorded: it is for what the session observes itself and for a
// report that has paid already, such as one the server sends on to a hosted
// session's host once it has spent the token it keeps for that host. A report
// from outside the session goes through TrySetAttentionFull.
func (s *Local) SetAttentionFull(state AttentionState, message, source, kind string, options []Option) {
	s.setAttention(state, message, source, kind, options, fromReport)
}

// TrySetAttentionFull is SetAttentionFull for a report from outside the
// session, an agent's or an admin's through the API. The report spends a
// token of the session's event bucket, the one Record spends on the events an
// agent reports, before it changes anything and whether or not it changes
// anything; the attention entry of the change it applies spends no second
// one. With no token left it returns ErrRateLimited having changed nothing,
// and the refusal counts in Dropped. The server spends a token of a bucket of
// the same size before it sends a report on to a hosted session's host, so a
// report is limited alike on both kinds of session.
func (s *Local) TrySetAttentionFull(state AttentionState, message, source, kind string, options []Option) error {
	return s.setAttention(state, message, source, kind, options, spendToken|fromReport)
}

// SetAttentionPlan is SetAttentionFull for a report that names the plan
// its question asks approval for (#289, the ExitPlanMode hook's
// planFilePath): in the one step the change takes, the plan is learnt as
// one of the session's plans (plans.go) and set on the question the
// change shows, or on the dialog of that approval already showing; a
// change refused names nothing, and a path that is not a plan's is
// nothing. It is for a report that has paid already (a host applying what
// its server sends on).
func (s *Local) SetAttentionPlan(state AttentionState, message, source, kind string, options []Option, plan string) {
	s.setAttentionWith(state, message, source, kind, options, fromReport, plan)
}

// TrySetAttentionPlan is SetAttentionPlan for a report from outside the
// session, as TrySetAttentionFull: the token spent first, and with none
// left ErrRateLimited, nothing changed and no plan learnt.
func (s *Local) TrySetAttentionPlan(state AttentionState, message, source, kind string, options []Option, plan string) error {
	return s.setAttentionWith(state, message, source, kind, options, spendToken|fromReport, plan)
}

// Ask is SetAttentionFull for the agent's own question: needs_input, from a
// report that says the agent asks it (the MCP tool ask, conductor notify
// --ask), which the session keeps until it is answered (own). In the turn it
// was asked in, the agent's work (a working report, its working title) and
// another wait (a hook's prompt, a dialog on the screen) take its place and
// set it aside; that turn's done shows it again, the same Attention (its id
// and Since), and a done leaves it showing. It goes with an answer (a
// person's keys or reply, which also remove it while it is set aside), the
// agent's clear or next question, another state after its turn's done (the
// agent at work in a new turn), or the session's end. The bell, an OSC
// notification and the screen pattern never take its place. It is for a
// report that has paid already: the one the server sends on to a hosted
// session's host.
func (s *Local) Ask(message, source, kind string, options []Option) {
	s.setAttention(AttentionNeedsInput, message, source, kind, options, agentAsks)
}

// TryAsk is Ask for a report from outside the session, as
// TrySetAttentionFull: it spends a token of the event bucket first, and with
// none left returns ErrRateLimited having changed nothing.
func (s *Local) TryAsk(message, source, kind string, options []Option) error {
	return s.setAttention(AttentionNeedsInput, message, source, kind, options, spendToken|agentAsks)
}

// WithdrawQuestion takes the question named id away, as the agent's own
// clear does, while it is the question shown or the agent's own kept
// aside; any other question, one asked since, stays. For what asked on the
// agent's behalf and no longer stands: a crew task's blocker, cleared as
// its task works on or ends (#353).
func (s *Local) WithdrawQuestion(id string) {
	if id == "" {
		return
	}
	// One critical section looks where the question is and takes it from
	// there (setAttentionLooked's only): shown, kept aside or held over.
	_ = s.setAttentionLooked(AttentionNone, "", SourceAPI, "", nil, 0, anyTurn, 0, "", attentionOpt{only: id})
}

// TryAskID is TryAsk that says which question it put up: the id of the
// agent's own question the ask keeps, read under the change's lock, so
// that a question asked by another meanwhile is never taken for it; ""
// when the ask kept none.
func (s *Local) TryAskID(message, source, kind string, options []Option) (string, error) {
	var id string
	err := s.setAttentionLooked(AttentionNeedsInput, message, source, kind, options, spendToken|agentAsks, anyTurn, 0, "", attentionOpt{asked: &id})
	return id, err
}

// attentionRules say how setAttention applies a change.
type attentionRules uint32

const (
	// unlessWaiting changes nothing when the session is needs_input already.
	unlessWaiting attentionRules = 1 << iota
	// spendToken spends a token of the event bucket before anything changes,
	// and refuses the change with ErrRateLimited when there is none.
	spendToken
	// unlessChoices changes nothing when the session is needs_input on a
	// prompt that has choices, on a question it holds (heldSource) or on
	// the agent's own question (own): a wait without anything to answer it
	// with gives way, one with its answers stays.
	unlessChoices
	// unlessTurnEnded changes nothing when the session shows done, or the
	// agent's own question its turn's done kept (own), and no Enter has
	// been written since (turnEnded): Codex rings the bell it is asked for
	// as its turn ends, after or before its notify reports done, and the
	// turn that ended asks nothing more. The next Enter, a person's or a
	// submission's, starts the next turn, whose bell is a question again;
	// so does any change of state (a working report, a question).
	unlessTurnEnded
	// unlessStartup changes nothing while the session holds a startup
	// question (source trust): the program shows that one.
	unlessStartup
	// bellPaced changes nothing within bellPace of the last change the
	// bell or an OSC notification made (lastBell), and stamps the change
	// it applies as the last: a burst of bells makes at most one change
	// per bellPace. Only a change is stamped: a bell that changes nothing
	// (its ended turn's own, or one at a wait that stays) has nothing to
	// pace, and must not keep the next turn's bell from asking.
	bellPaced
	// whileHeld changes nothing unless the session's turn is held for a
	// typed text (held) and the session has not ended: the hold's own
	// attention, which keys that ended the hold before it showed, or the
	// session's end, must not leave behind.
	whileHeld
	// sameDialog (a dialog asked during the work, fireAsked): a dialog with
	// the words of the one a write was last made to answer
	// (dialogAnswered), asked before the screen's watcher has seen that one
	// go or another dialog take its place, is that question still: the
	// program may not have taken the key yet (it is slow, or draws only
	// what leaves the dialog as it was). It is asked answered, with an id
	// no reply is taken for, so that no reply to it is written twice.
	sameDialog
	// startupLook (fireTrust): the change comes from a look of the startup
	// questions' watcher that took its text when it had seen look Feeds;
	// one no later than a startup answer (startupAnsweredAt) saw the
	// answered screen, and changes nothing. Weighed under the lock that
	// sets the change, so an answer that comes between the look and the
	// set is not overtaken.
	startupLook
	// unlessHeld changes nothing, shown or recorded, while the session's
	// turn is held for a typed text (held): the hold records every change
	// it hides as an entry, and a title comes ten times a second.
	unlessHeld
	// unlessOnScreen changes nothing while the session shows a question it
	// holds (heldSource: a startup question, a dialog on the screen): the
	// screen decides those.
	unlessOnScreen
	// unlessFresh changes nothing while the wait showing is one a title may
	// settle whose Options.TitleSettle has not passed (settleID, settleAt):
	// the settle timer decides it. A frame drawn between an approval's bell
	// and its dialog does not clear the bell.
	unlessFresh
	// unlessWorking changes nothing while the session shows working,
	// whatever reported it.
	unlessWorking
	// unlessEnded changes nothing once the session has ended.
	unlessEnded
	// onlySettling (the settle timer, settled) changes nothing unless the
	// timer of generation look (settleGen) is still armed, for the wait
	// the session still shows (settleID), and the agent's last word still
	// says it works (saysWorkingLocked: the title, or the program status).
	onlySettling
	// unlessWorded changes nothing when the session is needs_input already
	// with words of its own: anything but a bare bell's wait ("terminal
	// bell"), whose words a program status with a kind replaces.
	unlessWorded
	// agentAsks (Ask) marks a needs_input change as the agent's own
	// question, which the session keeps (own); with another state it
	// changes nothing.
	agentAsks
	// planApproval (fireAsked) marks a dialog read off the screen as a
	// plan's approval, by its footer (Attention.approval, #289).
	planApproval
	// fromReport marks a report from outside the session (an agent's or an
	// admin's through the API, or one the server sends on to a hosted
	// session's host): one that says the session waits does not hide a
	// question asked during the work (keepAsked), whether or not it spends
	// a token here (a forwarded report paid on the server).
	fromReport
)

// setAttention is SetAttentionFull, applied as rules say. The token, the look
// at the current state and the change share one critical section. The
// attention entry of an applied change is recorded without asking the bucket
// (recordOwn), stamped with the change's Since, taken in that section: whoever
// sees the new state sees it no earlier than the entry's At, however late the
// entry reaches OnActivity. An invalid state changes nothing and spends
// nothing.
func (s *Local) setAttention(state AttentionState, message, source, kind string, options []Option, rules attentionRules) error {
	return s.setAttentionRead(state, message, source, kind, options, rules, anyTurn)
}

// setAttentionWith is setAttention for a change that names the plan its
// question asks approval for (SetAttentionPlan; the dialog's footer).
func (s *Local) setAttentionWith(state AttentionState, message, source, kind string, options []Option, rules attentionRules, plan string) error {
	return s.setAttentionLooked(state, message, source, kind, options, rules, anyTurn, 0, plan)
}

// anyTurn is the turn of a change that is not output read (setAttentionRead).
const anyTurn = ^uint64(0)

// setAttentionRead is setAttention for what output read during turn (the
// count of turns begun after a done as it was read, Local.turn) says: it
// changes nothing once another turn has begun, the turn that ended asking
// nothing more. anyTurn is setAttention's.
func (s *Local) setAttentionRead(state AttentionState, message, source, kind string, options []Option, rules attentionRules, turn uint64) error {
	return s.setAttentionLooked(state, message, source, kind, options, rules, turn, 0, "")
}

// setAttentionLooked is setAttentionRead for a change a look of the startup
// questions' watcher found (startupLook): look is the Feeds that look's text
// had seen (PatternWatcher.LookedAt); for the settle timer's change
// (onlySettling), the timer's generation. plan is the plan the change's
// question asks approval for, when the change names one (#289): learnt
// once the change is past the bucket, and set on the question shown, or
// on the dialog of that approval already showing.
// attentionOpt narrows a change of setAttentionLooked: only, the question
// it applies to (WithdrawQuestion: nothing changes unless that is the one
// shown); asked, where the id of the agent's own question the change keeps
// is written, under the change's lock (TryAskID).
type attentionOpt struct {
	only  string
	asked *string
}

func (s *Local) setAttentionLooked(state AttentionState, message, source, kind string, options []Option, rules attentionRules, turn, look uint64, plan string, opts ...attentionOpt) error {
	var opt attentionOpt
	if len(opts) > 0 {
		opt = opts[0]
	}
	if !state.Valid() {
		return nil
	}
	// A question asked from the screen comes with its rows (a startup
	// question, a dialog: StartupQuestion.words), which tell it from the
	// next, and shows as a message cut from them.
	var rows, list string
	if heldSource(source) && state == AttentionNeedsInput {
		rows = asRows(cleanRows(message), true)
		// A dialog's whole list, read off the screen, is kept apart
		// (listMark): what a reply is weighed against.
		if before, after, ok := strings.Cut(rows, "\n"+listMark+"\n"); ok {
			rows, list = before, after
		}
		message = shownWords(rows)
	}
	message = CleanMessage(message)
	words := rows
	if words == "" {
		words = message
	}
	if !ValidKind(kind) {
		kind = ""
	}
	options = CleanOptions(options)
	if state == AttentionNone {
		kind, options = "", nil
	}
	s.mu.Lock()
	if rules&spendToken != 0 && !s.events.Take(time.Now()) {
		log := s.log
		s.mu.Unlock()
		countDrop(&s.dropped, log, ActivityAttention)
		return ErrRateLimited
	}
	if len(opts) > 0 && opt.asked == nil && (opt.only == "" || s.info.Attention.ID != opt.only) {
		// A change for one question (WithdrawQuestion) that is not the one
		// shown: under this same lock, the agent's own kept aside and the
		// question the hold of the turn holds over go if they are that one
		// (the hold's end would show it again otherwise), and the question
		// shown stays as it is (Codex's review of #353).
		changed := false
		if opt.only != "" && s.own.ID == opt.only {
			s.dropOwnLocked(ChatLeftAgent)
			changed = true
		}
		if opt.only != "" && s.heldOver.ID == opt.only {
			s.heldOver = Attention{}
			changed = true
		}
		s.mu.Unlock()
		if changed {
			s.notifyChange()
		}
		return nil
	}
	// The plan the change names (#289), learnt now, past the bucket, under
	// the lock the change takes: a question's alone, and nothing when the
	// path is not a plan's.
	if plan != "" {
		if abs := planPath(plan); state == AttentionNeedsInput && s.learnPlanLocked(abs) {
			plan = abs
		} else {
			plan = ""
		}
	}
	if rules&whileHeld != 0 && (s.held == nil || s.info.Status.Ended() ||
		s.info.Attention.State == AttentionNeedsInput && heldSource(s.info.Attention.Source)) {
		// The hold shows only while it stands, and never in place of a
		// question on the screen: that one is what the program waits on,
		// its words and choices what a person needs (answer shows the hold
		// once it is answered, if the hold still stands).
		s.mu.Unlock()
		return nil
	}
	cur := s.info.Attention
	// The agent's own question (Ask), and the one the session keeps (own),
	// shown or not.
	ask := rules&agentAsks != 0 && state == AttentionNeedsInput
	own := s.own
	ownShown := own.ID != "" && cur.ID == own.ID
	// Held over by the hold of the turn, which shows in its place.
	ownHeld := own.ID != "" && s.heldOver.ID == own.ID && cur.State == AttentionNeedsInput && cur.Source == SourceSubmit
	// The hold's words give way to a question drawn on the screen (a
	// startup question's unlessWaiting): the program waits on that one now.
	waiting := cur.State == AttentionNeedsInput && !(cur.Source == SourceSubmit && heldSource(source))
	answerable := waiting && (len(cur.Options) > 0 || heldSource(cur.Source) || ownShown || ownHeld)
	// A report from outside that the session waits, with choices or
	// without, does not hide a question asked during the work: the dialog
	// on the screen is what answers go to (a hook that reports the same
	// dialog late, say, or another question whose choices would land on
	// it).
	keepAsked := rules&fromReport != 0 && source != SourceQuestion && state == AttentionNeedsInput && waiting && cur.Source == SourceQuestion
	turnsOwn := rules&unlessTurnEnded != 0 && (cur.State == AttentionDone || ownShown || ownHeld) && s.turnEnded
	startup := rules&unlessStartup != 0 && waiting && cur.Source == SourceTrust
	paced := rules&bellPaced != 0 && !s.lastBell.IsZero() && time.Since(s.lastBell) < bellPace
	lastTurns := turn != anyTurn && turn != s.turn.Load()
	staleLook := rules&startupLook != 0 && s.startupAnswered && look <= s.startupAnsweredAt
	// A window title's (titleRules), and the settle timer's.
	held := rules&unlessHeld != 0 && s.held != nil
	onScreen := rules&unlessOnScreen != 0 && cur.State == AttentionNeedsInput && heldSource(cur.Source)
	fresh := rules&unlessFresh != 0 && cur.State == AttentionNeedsInput && cur.ID != "" && cur.ID == s.settleID && time.Now().Before(s.settleAt)
	working := rules&unlessWorking != 0 && cur.State == AttentionWorking
	ended := rules&unlessEnded != 0 && s.info.Status.Ended()
	worded := rules&unlessWorded != 0 && waiting && cur.Source != SourceBell
	notSettling := rules&onlySettling != 0 && (s.settleTimer == nil || s.settleGen != look || !s.saysWorkingLocked() ||
		cur.State != AttentionNeedsInput || cur.ID == "" || cur.ID != s.settleID)
	// A startup question answered and still shown (its write not made yet,
	// or the session slow to clear it) waits on nothing: the next startup
	// question takes its place.
	answeredStartup := rules&startupLook != 0 && waiting && cur.Source == SourceTrust && cur.ID != "" && (s.claimedLocked(cur.ID) || cur.ID == s.consumed)
	// A startup question still waiting that the screen has left for
	// another (drawn after it, lastStartupQ: the program took an answer not
	// read as one) waits on nothing either: the question the screen shows
	// takes its place, with an id of its own, and a reply naming the one
	// left is refused.
	movedOn := rules&startupLook != 0 && waiting && cur.Source == SourceTrust && state == AttentionNeedsInput && source == SourceTrust && !sameWords(words, cur.words())
	if (rules&unlessWaiting != 0 && waiting && !answeredStartup && !movedOn) || (rules&unlessChoices != 0 && answerable) || keepAsked || turnsOwn || startup || paced || lastTurns || staleLook ||
		held || onScreen || fresh || working || ended || notSettling || worded {
		// The report of an approval that names its plan, while the dialog
		// of that approval shows (keepAsked, and the dialog's footer says
		// it is one: another dialog is not that approval's): the plan set
		// on the dialog.
		told := keepAsked && plan != "" && s.info.Attention.approval && s.planOnShownLocked(plan)
		s.mu.Unlock()
		if told {
			s.notifyChange()
		}
		return nil
	}
	// Whether the change comes in the turn the agent's own question was
	// asked in (no done since), and whether a reply or a person's keys
	// reached it (a claim on it, or consumed: the held text entered, its
	// choice picked), weighed before the change moves the turn on: a
	// question reached is never set aside to show again at the done,
	// where a reply would be taken for an answer the program has.
	inTurn := own.ID != "" && s.turn.Load() == s.ownTurn && !s.turnEnded
	reached := own.ID != "" && (s.claimedLocked(own.ID) || s.consumed == own.ID)
	// Read here, before forgetLocked lets the question's consumption go: why its
	// card says it left, should the change below let it go for good (#358).
	ownConsumed := own.ID != "" && s.consumed == own.ID
	if s.held != nil && source != SourceSubmit && !heldSource(source) {
		// The session's turn is held for a typed text (holdTyped): the hold
		// is what shows, until a person types into the terminal, but for a
		// question on the screen, whose answer ends the hold too. Another
		// change, past the rules above (the bell's pace among them), is
		// recorded, as the report it is, and not shown; its turn and pace
		// are kept as a change shown keeps them.
		if state == AttentionDone {
			s.turnEnded = true
		} else {
			s.beginTurn()
		}
		if rules&bellPaced != 0 {
			s.lastBell = time.Now()
		}
		// The agent reports another state (at work, done, another
		// question): the question the hold took the place of waits no
		// longer, and does not show again as the hold ends. The agent's
		// own question waits through its turn's done and its work in that
		// turn, as it would shown.
		if o := s.heldOver; o.ID != "" && !(state == o.State && message == o.Message && source == o.Source && kind == o.Kind && sameOptions(options, o.Options)) &&
			!(o.ID == own.ID && (state == AttentionDone || state == AttentionWorking && inTurn)) {
			// Withdrawn: a claim on it resolves nothing.
			s.heldOver = Attention{}
			s.forgetLocked(o.ID)
		}
		switch {
		case own.ID == "" || s.heldOver.ID == own.ID:
			// None kept, or held over still.
		case s.ownAside && state == AttentionDone && s.heldOver.ID == "":
			// Set aside, its turn's done would show it again: the hold
			// shows, and it shows once a person's keys end the hold
			// without answering it.
			s.heldOver, s.ownAside = own, false
		case s.ownAside && inTurn && !ask && state != AttentionNone:
			// Set aside still: another state in its turn.
		default:
			s.dropOwnLocked(ChatLeftAgent)
		}
		s.mu.Unlock()
		// The question let go above leaves a withdrawn line (#358) this path tells no other way.
		s.tellChatLeft()
		if state != AttentionNone {
			label := message
			if label == "" {
				label = string(state)
			}
			s.recordOwn(ActivityEntry{At: time.Now().UTC(), Type: ActivityAttention, Message: label}, state)
		}
		return nil
	}
	if state == AttentionDone {
		// Also a done that changes nothing shown (the same words as the
		// last turn's): the turn it reports has ended.
		s.turnEnded = true
	}
	if state == AttentionDone && own.ID != "" && (ownShown || s.ownAside) {
		// The agent's own question outlasts the done (own): a done, any
		// (its turn's end, with turn or without; Claude Code's idle_prompt
		// a minute later), leaves it showing, or shows it again from aside
		// in place of what shows, the same Attention, its id and Since: a
		// reply under way compares that stamp (pressEnter, answer), and
		// its line in the chat is the one an answer refers to. The done
		// has ended the turn, and is recorded as the report it is.
		now := time.Now().UTC()
		if !ownShown {
			// What showed leaves, and a claim on it with it.
			if cur.ID != "" && cur.ID != own.ID {
				s.forgetLocked(cur.ID)
			}
			s.info.Attention, s.ownAside = own, false
			s.settleLocked(own)
			s.signalAttention()
			s.hub.Broadcast(proto.MustControl(attentionMessage(own)))
			s.question = own.ID
		}
		s.mu.Unlock()
		label := message
		if label == "" {
			label = string(state)
		}
		s.recordOwn(ActivityEntry{At: now, Type: ActivityAttention, Message: label}, AttentionDone)
		if !ownShown {
			// Shown again, it has its entry, as any state that shows.
			label = own.Message
			if label == "" {
				label = string(AttentionNeedsInput)
			}
			s.recordOwn(ActivityEntry{At: now, Type: ActivityAttention, Message: label}, AttentionNeedsInput)
			s.notifyChange()
		}
		return nil
	}
	// The program moved on (its status or title says it works, its hook or
	// a report says the turn is done, a person marked it so): the dialog
	// last answered is history, whatever the screen's looks can settle on.
	// A terminal resized with the dialog or its aftermath on the screen (the
	// reply bar's rows coming and going around every question) settles only
	// once the program erases its screen whole, which may be never; until
	// then the quiet looks forget nothing, and the next dialog with the same
	// words would be taken for the one answered (#258).
	if state == AttentionWorking || state == AttentionDone {
		s.dialogAnswered = ""
	}
	// The dialog last answered, asked before it has gone (sameDialog): its
	// question is answered, whatever id it shows with.
	stillAnswered := rules&sameDialog != 0 && state == AttentionNeedsInput && s.dialogAnswered != "" && sameWords(s.dialogAnswered, words)
	// The startup question last answered, asked again before another
	// startup question took its place (the program draws it again before
	// it moves on), is asked answered too; another startup question asked
	// ends that.
	// The startup question an Escape a write ended with may have
	// answered before it was asked (heldAnswer's words): asked answered,
	// and held so until the screen shows it gone (escGone).
	heldAsked, startupStill := false, false
	if rules&startupLook != 0 && state == AttentionNeedsInput {
		if s.startupAnsweredWords != "" && sameWords(s.startupAnsweredWords, words) {
			stillAnswered, startupStill = true, true
		} else {
			s.startupAnsweredWords = ""
		}
		if s.heldAnswer.words != "" {
			if sameWords(s.heldAnswer.words, words) {
				stillAnswered, heldAsked = true, true
			} else {
				s.heldAnswer.words = ""
			}
		}
	}
	// The same question again: its words, its choices and, read off the
	// screen, its whole list (a choice changed past what its label shows is
	// another question, whose reply is weighed against the list now).
	if cur.State == state && sameWords(cur.words(), words) && cur.Source == source && cur.Kind == kind && sameOptions(cur.Options, options) && cur.list == list {
		if rows != "" && rows != cur.rows {
			// The same question read at another width (the window
			// resized, its rows broken elsewhere): it is told by its rows
			// as they are now, so that at this width a row break is the
			// program's own against the next question (sameWords), and
			// so is the dialog last answered, when it is this one.
			s.info.Attention.rows = rows
			if rules&sameDialog != 0 && s.dialogAnswered != "" && sameWords(s.dialogAnswered, words) {
				s.dialogAnswered = words
			}
		}
		if stillAnswered && cur.ID != "" {
			// Asked again as the one answered: consumed, no answer taken.
			s.consumeLocked(cur.ID)
			s.holdAskedAgain(cur.ID, words, heldAsked, startupStill)
		}
		if ask && cur.ID != "" && !ownShown {
			// The question showing, now said to be the agent's own: kept
			// from here, as asked in this turn.
			s.info.Attention.ask = true
			s.own, s.ownAside, s.ownTurn = s.info.Attention, false, s.turn.Load()
		}
		if ask && opt.asked != nil && s.own.ID == cur.ID {
			// The same question asked again, the agent's own already or
			// from now: its id is the ask's (TryAskID).
			*opt.asked = cur.ID
		}
		// The same question, now with its plan named (the dialog's footer
		// read on a later look): set on it.
		told := plan != "" && s.planOnShownLocked(plan)
		s.mu.Unlock()
		if told {
			s.notifyChange()
		}
		return nil
	}
	if state != AttentionDone {
		// Another state after a done (a working report: the agent went on
		// by itself, a question, a clear) begins the next turn, as an
		// Enter does.
		s.beginTurn()
	}
	if rules&bellPaced != 0 {
		s.lastBell = time.Now()
	}
	att := Attention{State: state, Message: message, Source: source, Kind: kind, Options: options, rows: rows, list: list}
	// The plan the change names rides its question; the approval's dialog
	// read off the screen (planApproval) that takes the place of a wait
	// carrying one (the hook's report of that approval) keeps it; any
	// other question carries none (another dialog is not that approval's;
	// the dialog that follows an answered approval takes the place of a
	// dialog, which passes nothing on) (#289).
	if state == AttentionNeedsInput {
		att.Plan = plan
		att.approval = rules&planApproval != 0
		if att.Plan == "" && att.approval && cur.State == AttentionNeedsInput && cur.Source != SourceQuestion {
			att.Plan = cur.Plan
		}
	}
	if source == SourceSubmit {
		// The hold of the turn shows (holdTyped): the question it takes the
		// place of, one the agent asked and not a question on the screen
		// (which shows over the hold), shows again when a person's keys
		// end the hold without answering it (endHoldTyped).
		s.heldOver = Attention{}
		if cur.State == AttentionNeedsInput && cur.ID != "" && cur.Source != SourceSubmit && !heldSource(cur.Source) {
			s.heldOver = cur
		}
		// The stranded line's claim is let go here, in the step that
		// hides the question (strandedClaim): let go before it, a reply
		// could claim the question and write its keys after the text.
		s.holdPending = false
		s.releaseStrandedLocked()
	}
	if state != AttentionNone {
		now := time.Now().UTC()
		att.Since = &now
		att.ID = NewID()
		if stillAnswered {
			// A new instance of the question answered, asked again before
			// the program drew what follows: consumed, no answer taken.
			s.consumeLocked(att.ID)
			s.holdAskedAgain(att.ID, words, heldAsked, startupStill)
		}
	}
	if cur.ID != "" && cur.ID != att.ID && source != SourceSubmit {
		// The question showing leaves (withdrawn, or replaced by the next):
		// a claim on it resolves nothing, and nothing of it is kept. The
		// hold of the turn holds it over instead, its claim with it.
		s.forgetLocked(cur.ID)
	}
	// The agent's own question (own) against the change.
	switch {
	case ask:
		// Kept from now, asked in the turn now running; the one kept
		// before, if any, goes (its next question).
		att.ask = true
		prev := s.own.ID
		s.own, s.ownAside, s.ownTurn = att, false, s.turn.Load()
		if opt.asked != nil {
			*opt.asked = att.ID
		}
		if prev != "" && prev != att.ID {
			s.leftInChatLocked(prev, ChatLeftAgent)
		}
	case own.ID == "" || source == SourceSubmit || s.heldOver.ID == own.ID:
		// None kept; or the hold of the turn takes its place, holding it
		// over (heldOver, above) or leaving it set aside; or held over
		// already, a question on the screen showing over the hold.
	case (ownShown || s.ownAside) && inTurn && !reached && state != AttentionNone:
		// In its turn: set aside, to show again at the turn's done.
		s.ownAside = true
	default:
		// A state after its turn's done (the agent at work in a new turn,
		// another wait), a clear, or a question a reply or a person's
		// keys reached: it goes for good, its card saying why (a reply
		// under way resolves nothing once the question has left).
		reason := ChatLeftAgent
		if ownConsumed {
			reason = ChatLeftTerminal
		}
		s.dropOwnLocked(reason)
	}
	s.info.Attention = att
	s.settleLocked(att)
	s.signalAttention()
	s.hub.Broadcast(proto.MustControl(attentionMessage(att)))
	// The agent's question goes into the chat too (design: the agents' questions in the chat);
	// the session's own hold is no question of the agent's.
	var asked ChatMessage
	var askedID string
	if state == AttentionNeedsInput && source != SourceSubmit {
		asked, askedID = s.askInChat(att)
	}
	s.mu.Unlock()
	if askedID != "" {
		s.chatHook(askedID, asked)
		s.tellRun(asked)
	}
	if state != AttentionNone {
		label := message
		if label == "" {
			label = string(state)
		}
		s.recordOwn(ActivityEntry{At: *att.Since, Type: ActivityAttention, Message: label}, state)
	}
	s.notifyChange()
	return nil
}

func sameOptions(a, b []Option) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// attentionMessage encodes an Attention as the wire control message.
func attentionMessage(att Attention) proto.Attention {
	m := proto.Attention{T: proto.CtlAttention, State: string(att.State), Message: att.Message, Source: att.Source, Kind: att.Kind, ID: att.ID, Answered: att.Answered, Plan: att.Plan}
	for _, o := range att.Options {
		m.Options = append(m.Options, proto.AttentionOption{Label: o.Label, Input: o.Input, Mode: o.Mode})
	}
	return m
}

// SetAgentToken records the per-session token agents use to report attention.
func (s *Local) SetAgentToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentTokenHash = sha256.Sum256([]byte(tok))
	s.hasAgentToken = tok != ""
}

// AgentTokenOK compares a presented agent token in constant time.
func (s *Local) AgentTokenOK(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasAgentToken || tok == "" {
		return false
	}
	h := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(h[:], s.agentTokenHash[:]) == 1
}

// SetID assigns the session ID once it is known (hosts learn it from the
// server after registration). It only applies while the ID is empty.
func (s *Local) SetID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.info.ID == "" {
		s.info.ID = id
		s.log = s.log.With("session", id)
	}
}

// Ended is closed once the process has exited or been stopped.
func (s *Local) Ended() <-chan struct{} { return s.ended }

// Info returns a snapshot of the session description.
func (s *Local) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.info
	info.Attention = s.attentionLocked()
	info.Viewers = s.hub.LoudCount()
	if info.AgentSession != nil {
		as := *info.AgentSession
		info.AgentSession = &as
	}
	return info
}

// LastOutputAt is when the process last wrote output, zero until it has. A
// crew run reads it to tell when a member's terminal has gone quiet.
func (s *Local) LastOutputAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastOutput
}

// AttachOptions describe a client joining a session.
type AttachOptions struct {
	ID        string // empty: generated
	Role      Role
	LinkID    string
	LinkLabel string
	Name      string // cleaned with CleanName
	Cols      uint16
	Rows      uint16
	// ChatOnly makes the connection quiet (hello.chatOnly): for a run's
	// chat alone, with no scrollback or output, uncounted among the viewers.
	ChatOnly bool
	// Authorize, when set, says what the client's credential gives it now.
	// AttachWith calls it under the session's lock, in the critical section
	// that registers the client, and attaches with the role it returns in
	// place of Role; an error refuses the attach and AttachWith returns it
	// (ErrRevoked, or ErrExpired, for a link that no longer opens the
	// session). Whoever revokes a link marks it first and then closes its
	// clients (DisconnectLink, which takes the same lock), so either this
	// check sees the mark or that close sees the client. A Grant with Until
	// closes the client with ErrExpired then. Authorize must not call the
	// session.
	Authorize func() (Grant, error)
	// Fresh, when set, says whether the client's credential may be acted
	// on now, beside what it gives (Authorize): on a host, a link's viewer
	// needs the host to vouch for its server (an answered ping within its
	// bound). AttachWith does not ask it, so a client attaches on its
	// credential alone; it is asked with the credential wherever what the
	// client sent is acted on (a write, a save's part and its rename, a
	// read, an editor's open, a chat post), and an error refuses that as a
	// link gone would (ErrRevoked, or a kind of it). Fresh must not call
	// the session.
	Fresh func() error
	// Owner marks one of the owner's own connections: the workbench's on a
	// server, a host's viewer with no link from a server the person said is
	// their own (never a switchyard's). Only such a
	// connection is offered the editor's Neovim (see nvimAllowed); a link's
	// never is, nor a paste invite's, whatever the caller says.
	Owner bool
	// Replaces is the subscription this client takes over: the same viewer
	// moving to another transport (a host's viewer falling back from WebRTC
	// to the relay). While that one's transport still closes, its slot is
	// this client's and is not counted twice toward MaxViewers. Only a
	// subscription of this session already ended counts.
	Replaces *Subscription
}

// Attach registers a client without a display name. See AttachWith.
func (s *Local) Attach(id string, role Role, linkID string, cols, rows uint16, sink Sink) (*Subscription, error) {
	return s.AttachWith(AttachOptions{ID: id, Role: role, LinkID: linkID, Cols: cols, Rows: rows}, sink)
}

// viewersFrame encodes the current roster: the session's viewers, a quiet
// connection for the run's chat left out. Callers hold s.mu.
func (s *Local) viewersFrame() []byte {
	roster := make([]proto.ViewerInfo, 0)
	for _, v := range s.hub.Roster() {
		if !v.Quiet {
			roster = append(roster, v)
		}
	}
	return proto.MustControl(proto.Viewers{T: proto.CtlViewers, Count: len(roster), List: roster})
}

// AttachWith registers a client. The welcome, scrollback replay and ready
// marker are queued before any live output so the client sees a consistent
// stream; the roster is then broadcast to everyone.
func (s *Local) AttachWith(o AttachOptions, sink Sink) (*Subscription, error) {
	role, id, cols, rows := o.Role, o.ID, o.Cols, o.Rows
	if o.Authorize == nil && !role.Valid() {
		return nil, errors.New("session: invalid role")
	}
	if id == "" {
		id = NewID()
	}
	s.mu.Lock()
	if s.retired {
		s.mu.Unlock()
		return nil, ErrSessionEnded
	}
	// The credential is checked under the lock that registers the client
	// (see AttachOptions.Authorize).
	var until time.Time
	if o.Authorize != nil {
		g, err := o.Authorize()
		if err == nil && !g.Role.Valid() {
			err = errors.New("session: invalid role")
		}
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		role, until = g.Role, g.Until
	}
	// Every client whose transport is still open counts, an ended one whose
	// transport still closes included: at most MaxViewers connections at once.
	// The one this client replaces gives it its slot.
	// The client this one takes over gives it its slot, once. One still
	// attached (the move seen before its old transport's end) ends here: its
	// own Detach later finds it gone, so the move makes no leave and no join.
	r := o.Replaces
	_, given := s.passedOn[r]
	replacing := r != nil && !given
	if replacing && s.hub.remove(r) {
		r.closeWith(nil)
		// The size stays with the viewer's id, which a replacement under the
		// same id keeps; under another id it is handed on as at a leave.
		if s.sizer == r.ID && o.ID != r.ID {
			s.handOverSize()
		}
		select {
		case <-r.closed:
		default:
			if s.closing == nil {
				s.closing = map[*Subscription]struct{}{}
			}
			s.closing[r] = struct{}{}
		}
	}
	_, closing := s.closing[r]
	replacing = replacing && closing
	open := s.hub.Len() + len(s.closing) - len(s.passedOn)
	if replacing {
		open--
	}
	if open >= s.opts.MaxViewers {
		s.mu.Unlock()
		return nil, ErrTooManyViewers
	}
	// The hello's size ((0, 0) follows; proto.HelloSize). One viewer sizes
	// the PTY: one of the owner's own windows (a controller with no link)
	// takes the size as it attaches while nobody holds it; any other
	// controller's size is kept as what its window would want, and it takes
	// the size only by asking (Fit to my window). So a coworker's laptop
	// joining through a link never shrinks the session on the owner's
	// screen, and two of the owner's windows never take turns. The role
	// comes from the token, never from the hello.
	sized := role == RoleControl && proto.HelloSize(cols, rows) && !s.info.Status.Ended()
	// A viewer moving to the relay under the same id (Replaces) whose old
	// connection's leave handed the size on, the move seen after that leave,
	// takes the size back: a move keeps the size where it was, to whichever
	// window it went or to nobody, unless someone took it by asking (Fit to
	// my window) meanwhile.
	back := sized && !o.ChatOnly && r != nil && r.ID == id && r.leftSize && r.takesThen == s.takes
	if back {
		r.leftSize = false
	}
	if back || sized && o.LinkID == "" && !o.ChatOnly && s.sizer == "" {
		s.sizer = id
		if cols != s.info.Cols || rows != s.info.Rows {
			if err := s.proc.Resize(cols, rows); err == nil {
				s.sized(cols, rows)
			}
		}
		s.hub.Broadcast(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: s.info.Cols, Rows: s.info.Rows, By: id}))
	}
	transport := s.opts.Transport
	if named, ok := sink.(interface{ Transport() string }); ok {
		transport = named.Transport()
	}
	sub := newSubscription(id, role, o.LinkID, sink)
	sub.Name = CleanName(o.Name)
	sub.LinkLabel = o.LinkLabel
	sub.quiet = o.ChatOnly
	sub.owner = o.Owner && o.LinkID == ""
	sub.authorize = o.Authorize
	sub.fresh = o.Fresh
	if sized {
		sub.want = [2]uint16{cols, rows}
	}
	room := s.opts.RunChat
	offerNvim := nvimAvailable() && !s.nvimOwnerOnly(sub)
	sub.send(proto.MustControl(proto.Welcome{
		T:               proto.CtlWelcome,
		Proto:           proto.ProtoVersion,
		SessionID:       s.info.ID,
		Role:            string(role),
		SubscriberID:    id,
		Cols:            s.info.Cols,
		Rows:            s.info.Rows,
		Sizer:           true,
		SizedBy:         s.sizer,
		Status:          string(s.info.Status),
		ScrollbackBytes: s.ring.Cap(),
		Transport:       transport,
		FileView:        s.fileAllowed(role),
		FileEdit:        s.editAllowed(role),
		Nvim:            offerNvim,
		NvimOwnerOnly:   s.nvimOwnerOnly(sub),
		NvimSettled:     offerNvim,
		Chat:            true,
		RunChat:         room != nil,
		Answer:          true,
		SubmitAck:       true,
		Cwd:             welcomeCwd(s.info.Cwd, s.fileAllowed(role)),
	}))
	if !sub.quiet {
		snap := s.ring.Snapshot()
		for len(snap) > 0 {
			n := min(len(snap), proto.MaxOutput)
			sub.send(proto.Encode(proto.TypeScrollback, snap[:n]))
			snap = snap[n:]
		}
	}
	sub.send(proto.MustControl(proto.Simple{T: proto.CtlReady}))
	for _, e := range s.activity.Tail(ActivityReplay) {
		sub.send(proto.MustControl(EntryToProto(e)))
	}
	// The kept chat, after the activity replay and before the hub holds the
	// viewer: a message posted meanwhile reaches it live, never twice. The
	// run's chat follows the session's. The room posts without this lock: its
	// replay and the hub's add are made under the room's (attach), so a run
	// message posted meanwhile reaches the viewer in the replay or live, or
	// both, which clients take once by id; never neither.
	for _, f := range chatReplayFrames(proto.ChatScopeSession, s.chat.snapshot()) {
		sub.send(f)
	}
	if room != nil {
		room.attach(sub, s.hub)
	} else {
		s.hub.add(sub)
	}
	if replacing {
		// Its slot is this client's now, for every later check too.
		if s.passedOn == nil {
			s.passedOn = map[*Subscription]struct{}{}
		}
		s.passedOn[r] = struct{}{}
	}
	// A credential that lapses closes the client as it lapses, one that
	// attached a moment before included (at once when that moment has
	// passed); one closed before (the session's end) is written nothing
	// more from then on (Subscription.expire), until its transport closes.
	var lapse *time.Timer
	if !until.IsZero() {
		lapse = time.AfterFunc(time.Until(until), sub.expire)
	}
	s.hub.Broadcast(s.viewersFrame())
	if s.info.Attention.State != AttentionNone {
		sub.send(proto.MustControl(attentionMessage(s.attentionLocked())))
	}
	var joined ChatMessage
	var told bool
	if !sub.quiet {
		joined, told = s.chatSystem(sub, "join")
	}
	sessionID := s.info.ID
	s.mu.Unlock()
	if told {
		s.chatHook(sessionID, joined)
	}
	if room != nil {
		room.system(sub, "join")
		room.broadcastRoster()
	}
	go func() {
		<-sub.done
		s.Detach(sub)
		// Detach keeps it among the closing until its transport has closed.
		<-sub.closed
		if lapse != nil {
			lapse.Stop()
		}
		s.mu.Lock()
		delete(s.closing, sub)
		delete(s.passedOn, sub)
		s.mu.Unlock()
	}()
	if !sub.quiet {
		s.Record(ActivityEntry{Type: ActivityJoin, By: sub.ID, ByName: sub.Name})
	}
	s.notifyChange()
	return sub, nil
}

// Detach removes a client. It is safe to call more than once.
func (s *Local) Detach(sub *Subscription) {
	s.closeNvims(sub)
	s.mu.Lock()
	present := s.hub.remove(sub)
	sub.closeWith(nil)
	// Out of the hub, it stays among the closing until its transport has
	// closed (AttachWith's watcher takes it out), for a revoke to wait for.
	select {
	case <-sub.closed:
		// Closed already: gone from the closing and from the slots passed on
		// together, so no check in between counts it again.
		delete(s.closing, sub)
		delete(s.passedOn, sub)
	default:
		if s.closing == nil {
			s.closing = map[*Subscription]struct{}{}
		}
		s.closing[sub] = struct{}{}
	}
	// Only the client that held the size as it left hands it on: a late
	// Detach of one a replacement took over (the same id) leaves the size
	// with the replacement. A replacement attached after this leave takes
	// the size back (AttachWith, leftSize).
	if present && s.sizer == sub.ID {
		sub.leftSize, sub.takesThen = true, s.takes
		s.handOverSize()
	}
	var left ChatMessage
	var told bool
	if present {
		s.hub.Broadcast(s.viewersFrame())
		if !sub.quiet {
			left, told = s.chatSystem(sub, "leave")
		}
	}
	sessionID := s.info.ID
	room := s.opts.RunChat
	s.mu.Unlock()
	if told {
		s.chatHook(sessionID, left)
	}
	if present && room != nil {
		room.system(sub, "leave")
		room.broadcastRoster()
	}
	if present {
		if !sub.quiet {
			s.Record(ActivityEntry{Type: ActivityLeave, By: sub.ID, ByName: sub.Name})
		}
		s.notifyChange()
	}
}

// Input forwards keystrokes from a controller, raw. They answer the
// needs_input prompt that was showing as they were written, unless they are
// only a terminal's own report (isTerminalReport: xterm answering a cursor
// position query), which is written and answers nothing. While a question
// asked during the work shows, a write of several keys with an Enter that
// is not one of its choices is refused with ErrQuestion, nothing written.
// They are written in their turn among the session's writes (input.go), and
// Input waits for that at most Options.InputWait: a program that takes
// nothing meanwhile gets ErrInputStalled, the keystrokes written later if
// the write had begun (see writeProc) and never otherwise. Keystrokes not
// yet written when sub ends (revoked, detached), or once its credential no
// longer stands (AttachOptions.Authorize: ErrRevoked, ErrExpired, or a kind
// of either), are never written: refused with that error, asked as they are
// queued and again as the writer takes them. A viewer with no credential
// (one of the owner's windows, a host's own terminal) is never refused so.
// Keys written (a terminal's own report aside) end the hold of the session's
// turn for a submission's text typed without its Enter (holdTyped). data
// may be reused once Input returns.
func (s *Local) Input(sub *Subscription, data []byte) error {
	return s.input(context.Background(), sub, "", data, writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait}, nil)
}

// AnswerInput is Input for a reply to the question named question (its
// Attention.ID, as the person was shown it: a choice's keys clicked or
// pressed), waiting also until ctx ends. The keys are written only while
// that question is the one shown, nobody is answering it and nobody
// answered it in the terminal, which is looked at as they are queued and
// again, together with claiming it, as the writer takes them (the last of
// their checks); otherwise nothing is written and it returns
// ErrQuestionAnswered. Written, they resolve the question (resolve). Of
// two replies to one question the first is written and the second
// refused, so that a late click never lands on the question that came
// next.
func (s *Local) AnswerInput(ctx context.Context, sub *Subscription, question string, data []byte) error {
	return s.input(ctx, sub, question, data, writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait}, nil)
}

// AnswerInputFinished is AnswerInput whose word waits for its keys to
// land: when their write has begun and not landed by the time the wait
// allows, it returns ErrSubmitPending (accepted) and calls finished, on
// another goroutine, once they have landed (nil: the question resolved,
// or withdrawn meanwhile) or the session ended (ErrSessionEnded); any
// other return is the word, and finished is not called.
func (s *Local) AnswerInputFinished(ctx context.Context, sub *Subscription, question string, data []byte, finished func(error)) error {
	return s.input(ctx, sub, question, data, writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait}, finished)
}

// InputContext is Input waiting until ctx ends in place of
// Options.InputWait (ctx's error then), and refused at once, as Input is,
// while the program is known not to read: a host hands a viewer's
// keystrokes on from its frame loop and gives each its time from its
// arrival.
func (s *Local) InputContext(ctx context.Context, sub *Subscription, data []byte) error {
	return s.input(ctx, sub, "", data, writeWait{stall: s.opts.InputWait}, nil)
}

// InputWaiting is InputContext for a terminal of the person's own (a
// host's): it waits for the program to read, however long it has not, as
// a terminal does, until ctx ends.
func (s *Local) InputWaiting(ctx context.Context, sub *Subscription, data []byte) error {
	return s.input(ctx, sub, "", data, writeWait{}, nil)
}

// input is Input waiting as o says, until ctx ends; with question, the
// keys are a reply to that question (AnswerInput), and finished, when
// set, is told their word once they land late (AnswerInputFinished).
func (s *Local) input(ctx context.Context, sub *Subscription, question string, data []byte, o writeWait, finished func(error)) error {
	if sub.Role != RoleControl {
		return ErrReadOnly
	}
	// The keys are this call's own: what runs as they are written, or once
	// they land late, reads them after Input has returned, and the caller
	// may reuse its buffer then.
	data = bytes.Clone(data)
	if question != "" {
		// A reply to a question answered or gone is refused before
		// anything else is looked at: nothing of it is written.
		if err := s.questionWaits(question); err != nil {
			return err
		}
		if err := s.stillOnScreen(question); err != nil {
			return err
		}
	}
	if s.asked != nil && len(data) > 1 && bytes.IndexByte(data, '\r') >= 0 && !isTerminalReport(data) {
		// Several keys with an Enter (a page's choice, a paste): look at the
		// screen first, so that a dialog drawn and not yet seen quiet is the
		// question they are weighed against, not the one it replaced. Right
		// after a key that answered a dialog the screen is not looked at yet
		// (Hold): they wait for the look, refused meanwhile, since the key
		// may have done nothing and left the dialog.
		if s.asked.Held() {
			return ErrQuestion
		}
		s.asked.CheckNow()
	}
	s.mu.Lock()
	ended := s.info.Status.Ended()
	// A question's own choice is its answer, with an Enter or without:
	// some dialogs pick on a digit (Codex's background-server question) or
	// skip on Escape (its update question), and an Enter after those would
	// land on the next screen.
	choice := false
	for _, o := range s.info.Attention.Options {
		if o.Input != "" && o.Input == string(data) {
			choice = true
			break
		}
	}
	// A question asked during the work is a dialog's numbered list: a digit
	// picks a line at once and Ctrl+C cancels it (Codex 0.161's approval
	// overlay), whether the choices offer those keys or not. A key that
	// leaves the dialog where it was reaches it all the same; the screen,
	// looked at again, asks it again.
	if s.info.Attention.State == AttentionNeedsInput && s.info.Attention.Source == SourceQuestion && s.info.Attention.Options != nil && listKey(data) {
		choice = true
	}
	enter := choice || bytes.IndexByte(data, '\r') >= 0
	// More than one key with an Enter among them, while a question asked
	// during the work shows, is no key of its dialog: a choice of the
	// question it took the place of (a page's button clicked late), or a
	// paste. Its Enter would pick the dialog's highlighted line.
	stale := s.info.Attention.State == AttentionNeedsInput && s.info.Attention.Source == SourceQuestion && !choice && len(data) > 1 && bytes.IndexByte(data, '\r') >= 0 && !isTerminalReport(data)
	s.mu.Unlock()
	if ended {
		return ErrSessionEnded
	}
	if stale {
		return ErrQuestion
	}
	// What a key does to the session's questions is done as the writer
	// takes it, just before it is written, never for keys not written (a
	// time run out, a stall, a connection gone). A person's keys answer no
	// question (rawKeys): they consume one read off the screen, withdraw a
	// bell's wait with their Enter, and withdraw the agent's own question
	// set aside; the question clears when the agent or its screen moves
	// on. A reply's keys (question) claim their question as the writer
	// takes them (token, claimQuestion) and resolve it once written.
	var token uint64
	// Set as the writer takes the keys, as keysWritten reads them (a paste
	// or an escape sequence a write before left unfinished carried in):
	// typedEnter says they end the turn a done ended (enter, when the write
	// is its own keys, else read from its keys: a paste's text is none);
	// quiet that they are no key a person typed (the rest of a terminal's
	// report, or of a sequence not ended yet), which reach nothing and
	// stamp nobody typing.
	//
	// An Escape that ends a write is held (keysWritten): it may be the
	// Escape key and may be the start of what the next write goes on with
	// (a report's rest, a key read with it as Alt and that key), and
	// nothing tells which. It counts for what puts a question behind (a
	// reply to it refused, the safer mistake), never for what clears one,
	// puts its screen behind or keeps it from being asked: a startup
	// question it may answer is held answered (heldAnswer) until
	// the screen shows it gone (escGone), a key surely answers it, or the
	// next startup question takes its place, so that no submission's Enter
	// reaches a question that still shows.
	typedEnter := enter
	var quiet bool
	// Keys are withdrawn as the connection ends, and, as a submission is,
	// once its credential no longer stands (a link's: revoked or expired,
	// or on a host its server not heard from lately), asked as they are
	// queued and again as the writer takes them (writeWait.from): a write
	// begun before lands as the program reads, and none after it begins.
	o.from, o.anyEnd = sub, true
	// Whether a person typed anything here, or only the terminal reported
	// (its replies, the pointer moved or clicked), as many reports as the
	// write holds, one the connection's last write began included.
	typed := sub.typesSomething(data)
	if s.asked != nil && len(data) > 1 && bytes.IndexByte(data, '\r') >= 0 && !isTerminalReport(data) {
		// Several keys with an Enter, looked at again as the writer takes
		// them (they may have waited behind another write): a key that
		// answered a dialog not looked after yet, or a dialog on the screen
		// now whose choices they are not, refuses them (ErrQuestion), as
		// before they were queued.
		o.guard = func() error {
			if s.asked.Held() {
				return ErrQuestion
			}
			s.asked.CheckNow()
			s.mu.Lock()
			att := s.info.Attention
			s.mu.Unlock()
			if att.State == AttentionNeedsInput && att.Source == SourceQuestion && !isChoice(att, data) {
				return ErrQuestion
			}
			return nil
		}
	}
	if question != "" {
		// A reply to a question, looked at again as the writer takes it
		// (other writes may have gone before it): still the question
		// waiting, and claimed in the same critical section, so that a
		// reply to it queued behind is refused although the question shows
		// until this one is written and resolves it.
		dialog := o.guard
		o.guard = func() error {
			if err := s.questionWaits(question); err != nil {
				return err
			}
			if dialog != nil {
				if err := dialog(); err != nil {
					return err
				}
			}
			if err := s.stillOnScreen(question); err != nil {
				return err
			}
			t, err := s.claimQuestion(question)
			if err != nil {
				return err
			}
			token = t
			return nil
		}
	}
	o.before = func() {
		// The keys the program reads from the write, a paste or an escape
		// sequence a write before it left unfinished carried in.
		keys, unkeyed, extra := s.keysWritten(data)
		// The keys that may reach a question: keys, and those the write
		// may hold besides (the Escape held, an Alt chord's key alone). A
		// write with those is never matched whole: Alt and Enter is no
		// Enter.
		held := len(extra) > 0
		maybe := keys
		if held {
			maybe = append(keys[:len(keys):len(keys)], extra...)
		}
		if typed && question == "" {
			// A person types into the terminal: a submission's text typed
			// without its Enter no longer holds the session's turn
			// (holdTyped), and what comes next is written after these keys.
			// The terminal's reports, the pointer's among them, type nothing.
			// A reply's choice (an answer to a question on the screen over
			// the hold) is no person at the terminal: the text still waits
			// there, and the hold shows again once the question is
			// resolved. The question the hold took the place of shows
			// again, consumed when these keys entered the held text or
			// picked its choice (endHoldTyped).
			s.endHoldTyped(data, maybe)
		}
		own := ownKeys(data, keys) && !held
		quiet = unkeyed || allReports(keys) && !held
		s.mu.Lock()
		shown := s.info.Attention
		s.mu.Unlock()
		if !own {
			typedEnter = anyKey(keys, func(k []byte) bool {
				return string(k) == "\r" || isChoice(shown, k) || shown.Source == SourceQuestion && listKey(k)
			})
		}
		if quiet {
			typedEnter = false
		}
		// The dialog the screen the dialogs are read from shows as keys
		// are written that may answer one (any key but an escape sequence:
		// a paste alone, an arrow alone, do not read it).
		var words string
		var onScreen *dialogView
		if s.asked != nil && mayAnswerDialog(maybe) && !isTerminalReport(data) {
			screen, cols := s.asked.TextAt()
			var q *StartupQuestion
			var answers []Option
			if q, words, answers, _ = s.askedIn(screen, cols); q != nil {
				onScreen = readDialog(words, answers, screen)
				// Read off the screen and offering none: the highlight is in
				// a field, where a digit is typed.
				onScreen.field = q.FromScreen && answers == nil
			}
		}
		// A startup question a key surely answers is consumed, its screen
		// put behind, and withdrawn once the screen shows it gone; one
		// only the Escape held may answer is held answered (heldAnswer).
		startupAnswer, heldAnswer := false, false
		// The startup question the keys reach is the one drawn last (the
		// output's tail tells which): the one the session shows, unless
		// another has been drawn since and not asked yet, which they reach
		// instead (shownDrawn false): a choice of the older one is no
		// answer to it.
		shownDrawn := true
		if s.trust != nil && shown.State == AttentionNeedsInput && shown.Source == SourceTrust && s.trust.Watching() {
			if _, drawn := s.startupShown(s.trust.Text()); drawn != "" && !sameWords(drawn, shown.words()) {
				shownDrawn = false
			}
		}
		// Whether the keys may reach a startup question at all, asked or
		// not yet, read off the screen (startupMayShow): then they did not
		// enter a stranded line's text, and its hold is kept (rawKeys, #224).
		startup := s.startupMayShow()
		// A reply's keys to the startup question shown (claimed as the
		// writer took them): its screen goes behind as a sure answer's does.
		ownAnswer := question != "" && shownDrawn && shown.State == AttentionNeedsInput && shown.Source == SourceTrust && shown.ID == question
		if !shownDrawn {
			// Nor is the turn begun by them: the question gives way to the
			// one drawn once that is asked, the session held meanwhile.
			typedEnter = false
		}
		if !isTerminalReport(data) && !quiet && question == "" {
			// A person's keys: never an answer. They consume the dialog
			// they reach, withdraw a bell's wait with their Enter, and
			// withdraw the agent's own question set aside.
			s.rawKeys(data, maybe, onScreen, startup, typedEnter || anyKey(maybe, isEnterKey), typed)
			// The startup question showing as the keys are written, when
			// they pick one of its answers (picksStartup: a digit of its
			// items it offers no reply for too; one key of several, an
			// arrow then its digit; keys typed before it showed), is
			// consumed as its choice alone answers it: its screen behind
			// now, the question withdrawn once the screen shows it gone.
			if shownDrawn && shown.State == AttentionNeedsInput && shown.Source == SourceTrust && s.startupPicks(shown, data, maybe) {
				if s.startupPicks(shown, data, keys) {
					startupAnswer = true
				} else {
					heldAnswer = true
				}
			}
		}
		// The startup question the screen shows as the keys are written,
		// asked or not yet (the watcher asks once the screen is quiet): keys
		// that pick one of its answers (picksStartup) reach it, and its
		// screen goes behind, so that the watcher asks only what is drawn
		// after.
		startupOnScreen, onScreenHeld := false, false
		var startupWords string
		if s.trust != nil && !quiet && !isTerminalReport(data) && s.trust.Watching() {
			text := s.trust.Text()
			if q, words := s.startupShown(text); q != nil && picksStartup(CleanOptions(q.Answers), startupItems(q, text), data, maybe) {
				startupWords = words
				if picksStartup(CleanOptions(q.Answers), startupItems(q, text), data, keys) {
					startupOnScreen = true
				} else {
					onScreenHeld = true
				}
			}
		}
		if startupOnScreen && !startupAnswer && !ownAnswer {
			s.mu.Lock()
			att := s.info.Attention
			asked := att.State == AttentionNeedsInput && att.Source == SourceTrust && att.ID != "" && sameWords(att.words(), startupWords)
			s.mu.Unlock()
			if !asked {
				// Drawn and not asked yet (the watcher asks once the screen
				// is quiet): the program may not have read the key, for
				// Codex discards what came before its question's screen took
				// input. It is held answered by its words and asked so,
				// nothing put behind, until the screen shows it gone or the
				// next takes its place. A question asked has had its screen
				// quiet since it was drawn, the discard long done.
				startupOnScreen, onScreenHeld = false, true
			}
		}
		if (heldAnswer || onScreenHeld) && !startupAnswer && !startupOnScreen && !ownAnswer {
			// Only the Escape held may answer it: the startup question
			// showing (or, not asked yet, the one the screen shows, by its
			// words) is held answered, nothing else done.
			s.holdStartupAnswered(startupWords, sub)
		}
		if (ownAnswer || startupAnswer || startupOnScreen) && s.trust != nil {
			// The answered screen behind, a look that took its text before
			// now asking nothing as it fires (fireTrust), and the question
			// answered, asked again before another takes its place, asked
			// answered. The question the session shows now is read again
			// in the same step: one asked since the keys were looked at
			// (shown) is the one they reach, consumed (a person's keys) or
			// claimed (a reply's, by its guard).
			s.mu.Lock()
			s.startupAnswered, s.startupAnsweredAt = true, s.trust.Fed()
			if s.asked != nil {
				s.startupAnsweredFed = s.asked.Fed()
			}
			if startupWords == "" && shown.Source == SourceTrust {
				startupWords = shown.words()
			}
			if startupWords != "" {
				s.startupAnsweredWords = startupWords
			}
			s.mu.Unlock()
			if question == "" {
				s.holdStartupAnswered(startupWords, sub)
			}
			s.trust.Reset()
		}
		if typedEnter {
			// The turn a done ended is behind as the Enter is written (an
			// encoded one too).
			s.nextTurn()
		}
		answering := enter || s.askedKeys[string(data)] || listKey(data)
		if !own {
			answering = anyKey(maybe, func(k []byte) bool { return string(k) == "\r" || s.askedKeys[string(k)] || listKey(k) })
		}
		if words != "" && answering {
			// A key that answers a dialog on the screen: the look before a
			// submission does not ask the dialog again before the program
			// draws what follows (the look at the end of a quiet period
			// still does).
			s.asked.Hold()
		}
	}
	// written is what follows the keys once written: the viewer typing,
	// the screen looked at again, and a reply's question resolved by the
	// claim its keys made (resolve).
	written := func() {
		if !quiet {
			s.typedKeys(sub, data, typedEnter)
		}
		if token != 0 {
			s.resolve(question, token, sub.ID, sub.Name, data)
		}
	}
	landed, err := s.writeProc(ctx, data, o)
	if err != nil {
		if landed != nil && !errors.Is(err, ErrSessionEnded) {
			// Begun: the keys land as the program reads, and what follows
			// written keys is done then. At most one write is begun at a
			// time, so at most one such wait is under way. A reply's word
			// (finished) waits for the landing: accepted meanwhile
			// (ErrSubmitPending).
			go func() {
				select {
				case werr := <-landed:
					if werr == nil {
						written()
					}
					if finished != nil {
						finished(werr)
					}
				case <-s.ended:
					if finished != nil {
						finished(ErrSessionEnded)
					}
				}
			}()
			if finished != nil {
				return ErrSubmitPending
			}
		}
		return err
	}
	if hook := s.beforeWritten.Load(); hook != nil {
		(*hook)(data)
	}
	written()
	return nil
}

// isEnterKey reports whether k is the Enter key.
func isEnterKey(k []byte) bool { return string(k) == "\r" }

// isChoice reports whether data is the input of one of att's choices.
func isChoice(att Attention, data []byte) bool {
	for _, o := range att.Options {
		if o.Input != "" && o.Input == string(data) {
			return true
		}
	}
	return false
}

// typedKeys is what follows keys written to the program (input): sub is
// typing, and the screen is looked at again for a dialog the keys may have
// answered (enter: an Enter, or a dialog's key). They answer no question.
func (s *Local) typedKeys(sub *Subscription, data []byte, enter bool) {
	if isTerminalReport(data) {
		return
	}
	s.stampTyping(sub)
	if s.asked != nil && (enter || s.askedKeys[string(data)]) {
		// Look at the screen again even if the program draws nothing: a
		// dialog the key did not take away is asked again, and one it took
		// away is withdrawn (missAsked).
		s.asked.Poke()
	}
}

// askedIn returns the question asked during the work that text (the
// screen's, read cols wide: TextAt) shows, and its words; nil and ""
// when it shows none.
func (s *Local) askedIn(text string, cols int) (q *StartupQuestion, words string, answers []Option, whole string) {
	for i := range s.askedQuestions {
		if words, answers, whole := s.askedQuestions[i].read(text); words != "" {
			return &s.askedQuestions[i], atWidth(words, cols), answers, whole
		}
	}
	return nil, "", nil, ""
}

// sized records the PTY's new size, which the screen the questions asked
// during the work are read from follows. The caller holds s.mu.
func (s *Local) sized(cols, rows uint16) {
	s.info.Cols, s.info.Rows = cols, rows
	if s.asked != nil {
		s.asked.Resize(int(cols), int(rows))
	}
	s.shot.Resize(int(cols), int(rows))
	// The baseline is taken again at the next quiet (screenQuiet): a
	// program that redraws everything after a resize brings that quiet
	// with its output, one that draws nothing for it (a line reader) is
	// looked at after the quiet period from now (Poke), so that the
	// output it prints next is compared with the screen as it is now.
	s.shotWait, s.shotRows = "", nil
	s.shot.Poke()
}

// listKey reports whether data is a key a dialog's list takes as an answer
// of its own: a digit from 1 to 9, or Ctrl+C.
func listKey(data []byte) bool {
	return len(data) == 1 && (data[0] >= '1' && data[0] <= '9' || data[0] == 0x03)
}

// nextTurn says an Enter is about to be written: the turn a done ended is
// behind, and a bell is a question again (unlessTurnEnded).
func (s *Local) nextTurn() {
	s.mu.Lock()
	s.beginTurn()
	s.mu.Unlock()
}

// beginTurn begins the next turn after a done, whichever comes first: an
// Enter (nextTurn) or another state shown (setAttention). Nothing when no
// done ended the last one. The new turn's bells are no burst of the last
// turn's: the pace starts again (bellPaced), so that its first bell asks
// however soon it rings after one of the last turn's (Codex's can ring
// before its done, and a crew's next prompt follows the done at once).
// What was read before it is the last turn's (turn), the last window title
// read included (titleWorking): it says nothing of the next turn, whose
// waits it neither settles nor arms a timer for (readTitle). An Enter with
// no done before it in its turn (an answer, a line typed during the turn)
// keeps the pace: a burst's tail does not ask again. The caller holds
// s.mu.
func (s *Local) beginTurn() {
	if !s.turnEnded {
		return
	}
	s.turnEnded = false
	s.lastBell = time.Time{}
	s.turn.Add(1)
	s.titleWorking = false
	s.disarmSettleLocked()
}

// dropOwnLocked forgets the agent's own question the session keeps (own):
// it shows again nowhere. The attention showing is the caller's to change.
// The caller holds s.mu.
func (s *Local) dropOwnLocked(reason string) {
	id := s.own.ID
	s.own, s.ownAside, s.ownTurn = Attention{}, false, 0
	// Gone for good without an answer through Conductor: its card in the chat
	// says why (#358); nothing when the answer's own line follows (reason "").
	if reason != "" {
		s.leftInChatLocked(id, reason)
	}
}

// questionClaim is the structured answer being written to a question
// (CLAIMED, Local.claim): the question's id and the claim's token, fresh
// for every claim (claimSeq), so that a claim on a question shown again
// with the same id (after a hold, or by its turn's done) is never taken for
// an earlier one. The empty claim while none stands.
type questionClaim struct {
	id    string
	token uint64
}

// resultsKept is how many questions' results the session keeps by id
// (results): the word a late or duplicate reply gets names who answered.
const resultsKept = 64

// claimLocked claims the question named id for a structured answer about
// to be written (OPEN and shown, to CLAIMED): refused while it is not the
// one shown (waitsLocked), is being answered or was answered in the
// terminal, or while a write is being made (ErrInputBusy). Returns the
// claim's token: the write resolves the question with it once made
// (resolve), or releases it (release). A dialog or a startup question
// claimed is noted by its words, so that one drawn again before the
// program moves on is that question still (sameDialog, startupStill). The
// caller holds s.mu.
func (s *Local) claimLocked(id string) (uint64, error) {
	if err := s.waitsLocked(id); err != nil {
		return 0, err
	}
	if s.inputBusy() {
		return 0, ErrInputBusy
	}
	s.claimSeq++
	c := questionClaim{id: id, token: s.claimSeq}
	if s.claims == nil {
		s.claims = map[string]questionClaim{}
	}
	s.claims[id] = c
	att := s.info.Attention
	switch att.Source {
	case SourceQuestion:
		s.noteDialogAnswered(att.words())
	case SourceTrust:
		s.startupAnsweredWords = att.words()
	}
	return c.token, nil
}

// claimedLocked reports whether a claim stands on the question named id.
// The caller holds s.mu.
func (s *Local) claimedLocked(id string) bool { return id != "" && s.claims[id].id == id }

// dropClaimLocked lets the claim c go, if it still stands. The caller
// holds s.mu.
func (s *Local) dropClaimLocked(c questionClaim) {
	if c.id != "" && s.claims[c.id] == c {
		delete(s.claims, c.id)
	}
}

// claimQuestion is claimLocked, asked as the writer takes a reply's write,
// the last of its checks (writeWait.guard), so that the write is made when
// it returns nil, and a reply to the same question after it is refused.
func (s *Local) claimQuestion(id string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimLocked(id)
}

// release lets the claim (id, token) go (CLAIMED to OPEN): its write was
// refused before it began, or its line was typed and its Enter not written
// (the hold of the turn then holds the question over). Nothing unless that
// claim still stands.
func (s *Local) release(id string, token uint64) {
	s.mu.Lock()
	s.dropClaimLocked(questionClaim{id: id, token: token})
	s.mu.Unlock()
}

// resolve is the one transition from CLAIMED to RESOLVED: the claim (id,
// token) whose write was made (a choice's keys, a line's Enter) answers
// the question, by by (a subscriber ID) and byName. Nothing unless that
// claim still stands and the question is still there, shown or held over
// by the hold of the turn (withdrawn meanwhile, the agent having moved
// on, it answers nothing). The check, the result and the clear share one
// critical section: the result is recorded (LastAnswer, results, the
// chat's answered line) before anything is broadcast, and the question
// then leaves as answered: the empty state with source input, or the hold
// of the turn shown again when one stands; one held over goes without
// showing again. The agent's own question kept (own) goes with it. An
// input entry with the question records the answer.
func (s *Local) resolve(id string, token uint64, by, byName string, keys []byte) {
	s.mu.Lock()
	if s.claims[id] != (questionClaim{id: id, token: token}) {
		s.mu.Unlock()
		return
	}
	delete(s.claims, id)
	att := s.info.Attention
	shown := att.State == AttentionNeedsInput && att.ID == id
	over := s.heldOver.ID == id
	if !shown && !over {
		s.mu.Unlock()
		return
	}
	q := att
	if !shown {
		q = s.heldOver
		s.heldOver = Attention{}
	}
	result := &Answer{By: by, ByName: byName, At: time.Now().UTC(), Message: q.Message}
	s.info.LastAnswer = result
	s.keepResultLocked(id, result)
	if s.own.ID == id {
		s.dropOwnLocked("")
	}
	if s.consumed == id {
		s.consumed = ""
	}
	if shown {
		s.info.Attention = Attention{State: AttentionNone, Source: SourceInput}
		if s.held != nil && !s.info.Status.Ended() {
			// A question answered while the turn is still held for a typed
			// text (one gone with keys typed before the hold): the hold
			// shows again.
			now := time.Now().UTC()
			s.info.Attention = Attention{State: AttentionNeedsInput, Message: HeldWords, Source: SourceSubmit, Since: &now, ID: NewID()}
		} else if heldSource(q.Source) && s.statusSeen && s.lastStatus == StatusWorking && !s.info.Status.Ended() {
			// A dialog answered while the program's status says it works:
			// Codex writes working as its overlay closes, before the answer
			// is taken or after it; read before, the status was kept out by
			// the dialog on the screen (unlessOnScreen) and is not written
			// again (readStatus), so working shows at the answer, as the
			// status's, recorded below (statusReconciled) as it is when the
			// answer comes after (#281).
			now := time.Now().UTC()
			s.info.Attention = Attention{State: AttentionWorking, Source: SourceStatus, Since: &now}
			s.statusReconciled = true
		}
		s.settleLocked(s.info.Attention)
		s.signalAttention()
		s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
	}
	answered, answeredID, told := s.answeredInChat(id, by, byName, answerWords(q.Options, keys))
	s.mu.Unlock()
	if told {
		s.chatHook(answeredID, answered)
		s.tellRun(answered)
	}
	s.Record(ActivityEntry{Type: ActivityInput, By: by, ByName: byName, Message: q.Message})
	s.recordStatusReconciled()
	s.notifyChange()
}

// keepResultLocked keeps result as the question id's, the oldest of
// resultsKept forgotten. The caller holds s.mu.
func (s *Local) keepResultLocked(id string, result *Answer) {
	if s.results == nil {
		s.results = map[string]*Answer{}
	}
	if _, kept := s.results[id]; !kept {
		s.resultIDs = append(s.resultIDs, id)
		if len(s.resultIDs) > resultsKept {
			delete(s.results, s.resultIDs[0])
			s.resultIDs = s.resultIDs[1:]
		}
	}
	s.results[id] = result
}

// consumeLocked marks the question instance named id, shown, as reached by
// keys typed in the terminal (consumed): it takes no answer (waitsLocked),
// and goes once the screen shows it gone or the next takes its place. Only
// an OPEN instance: one being answered keeps its claim, which resolves it
// or lets it go. The caller holds s.mu.
func (s *Local) consumeLocked(id string) {
	if id == "" || s.claimedLocked(id) {
		return
	}
	s.consumed = id
	if att := s.info.Attention; att.State == AttentionNeedsInput && att.ID == id {
		// The question shown reads answered in the terminal from here
		// (attentionLocked, #258): every client hears so at once, through
		// the stream and the terminal's control channel, and offers it no
		// longer; it stays until the screen shows it gone.
		s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
		go s.notifyChange()
	}
}

// attentionLocked is the attention as the session reports it: the question
// shown that keys typed in the terminal have answered (consumed) carries the
// mark AnsweredTerminal, so that no page offers choices nobody can take
// while the dialog waits for the program to move on (#258). The caller
// holds s.mu.
func (s *Local) attentionLocked() Attention {
	att := s.info.Attention
	if att.State == AttentionNeedsInput && att.ID != "" && att.ID == s.consumed {
		att.Answered = AnsweredTerminal
	}
	return att
}

// releaseStrandedLocked lets the stranded line's claim go (strandedClaim),
// if it still stands: as the hold of the turn shows (setAttentionLooked),
// or, its words refused, as holdTyped goes on without them. The caller
// holds s.mu.
func (s *Local) releaseStrandedLocked() {
	c := s.strandedClaim
	if c == (questionClaim{}) {
		return
	}
	s.strandedClaim = questionClaim{}
	s.dropClaimLocked(c)
}

// forgetLocked is the question instance named id leaving (withdrawn, or
// replaced by the next): a claim on it resolves nothing, its consumption
// and the screen's baseline for it go. The caller holds s.mu.
func (s *Local) forgetLocked(id string) {
	if id == "" {
		return
	}
	s.leftInChatLocked(id, s.leftReasonLocked(id))
	delete(s.claims, id)
	if s.consumed == id {
		s.consumed = ""
	}
	if s.shotWait == id {
		s.shotWait, s.shotRows = "", nil
	}
}

// withdrawShownLocked withdraws the question showing without an answer:
// the empty state with source (SourceScreen for the screen moved on,
// SourceInput for an Enter typed at a bell's wait), nothing recorded, or
// the hold of the turn shown again when one stands. The caller holds s.mu
// and tells the change (notifyChange) once it has let it go.
func (s *Local) withdrawShownLocked(source string) {
	att := s.info.Attention
	reason := s.leftReasonLocked(att.ID)
	s.forgetLocked(att.ID)
	if att.ID != "" && att.ID == s.own.ID {
		s.dropOwnLocked(reason)
	}
	s.info.Attention = Attention{State: AttentionNone, Source: source}
	if s.held != nil && att.Source != SourceSubmit && !s.info.Status.Ended() {
		now := time.Now().UTC()
		s.info.Attention = Attention{State: AttentionNeedsInput, Message: HeldWords, Source: SourceSubmit, Since: &now, ID: NewID()}
	} else if source == SourceScreen && heldSource(att.Source) && s.statusSeen && s.lastStatus == StatusBlocked && s.statusKind != "" && !s.info.Status.Ended() {
		// The screen moved on from a question the session held while the
		// program's status says it is blocked still: that status was read
		// as the question showed and kept out by its better words, and the
		// program will not write it again (readStatus). Its wait shows now,
		// recorded by the caller (statusReconciled).
		now := time.Now().UTC()
		s.info.Attention = Attention{State: AttentionNeedsInput, Message: statusWords(s.statusKind), Source: SourceStatus, Kind: statusKindOf(s.statusKind), Since: &now, ID: NewID()}
		s.statusReconciled = true
	} else if source == SourceScreen && heldSource(att.Source) && s.statusSeen && s.lastStatus == StatusWorking && !s.info.Status.Ended() {
		// The screen moved on from a question the session held while the
		// program's status says it works: read as the question showed and
		// kept out by the dialog on the screen (unlessOnScreen), and not
		// written again (readStatus). Working shows now, as the status's,
		// recorded by the caller (statusReconciled); Codex writes it as its
		// overlay closes, before or after the screen shows it gone (#281).
		now := time.Now().UTC()
		s.info.Attention = Attention{State: AttentionWorking, Source: SourceStatus, Since: &now}
		s.statusReconciled = true
	}
	s.settleLocked(s.info.Attention)
	s.signalAttention()
	s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
}

// withdraw withdraws the question named id when it is the one shown and
// nobody is answering it (a claim stands while its write can land): the
// screen moved on from it (escGone, missAsked, screenQuiet). It reports
// whether it did.
func (s *Local) withdraw(id string) bool {
	s.mu.Lock()
	att := s.info.Attention
	if id == "" || att.State != AttentionNeedsInput || att.ID != id || s.claimedLocked(id) {
		s.mu.Unlock()
		return false
	}
	s.withdrawShownLocked(SourceScreen)
	s.mu.Unlock()
	s.recordStatusReconciled()
	s.notifyChange()
	return true
}

// recordStatusReconciled records the attention entry of the status's wait
// withdrawShownLocked put in a held question's place (statusReconciled), as
// any state that shows has one.
func (s *Local) recordStatusReconciled() {
	s.mu.Lock()
	att, done := s.info.Attention, s.statusReconciled
	s.statusReconciled = false
	s.mu.Unlock()
	if !done || att.Source != SourceStatus {
		return
	}
	switch att.State {
	case AttentionNeedsInput:
		s.recordOwn(ActivityEntry{At: time.Now().UTC(), Type: ActivityAttention, Message: att.Message}, AttentionNeedsInput)
	case AttentionWorking:
		// Labelled as a working change is (setAttentionLooked): by its state.
		s.recordOwn(ActivityEntry{At: time.Now().UTC(), Type: ActivityAttention, Message: string(AttentionWorking)}, AttentionWorking)
	}
}

// The screen snapshot's threshold (screenQuiet, movedOn): a bell or OSC
// wait is withdrawn once at least shotRowsChanged rows of the screen
// differ from the baseline, a row counting as changed when more than
// shotCellsChanged of its cells differ. A spinner's frame changes one
// cell, a countdown or a clock a few of one row, a status line whose
// words change wholesale one row; the agent moving on prints output
// (every row shifts), or redraws its prompt area (three rows or more).
const (
	shotRowsChanged  = 3
	shotCellsChanged = 8
)

// screenQuiet is the screen model's look at the end of a quiet period
// (shot): a bell or OSC wait shown, with nobody answering it, has its
// baseline taken at the first quiet after it showed (shotRows: the
// program draws what it rang for in the same read or the next), and is
// withdrawn at a later quiet once the screen has moved on from it
// (movedOn). A question read off the screen has rows of its own and its
// watcher; the pattern's prompt its line.
func (s *Local) screenQuiet(string) {
	if hook := s.beforeShot.Load(); hook != nil {
		(*hook)()
	}
	s.mu.Lock()
	// The rows are read under the session's lock, which a resize holds
	// as it changes them and drops the baseline (sized): rows read before
	// one would be installed as the baseline, and the next quiet would
	// find the screen moved on by the resize alone.
	rows, drawn := s.shot.Rows()
	if !drawn {
		s.mu.Unlock()
		return
	}
	att := s.info.Attention
	if att.State != AttentionNeedsInput || att.ID == "" || att.Source != SourceBell && att.Source != SourceOSC && att.Source != SourceStatus || s.claimedLocked(att.ID) {
		s.shotWait, s.shotRows = "", nil
		s.mu.Unlock()
		return
	}
	if s.shotWait != att.ID || s.shotRows == nil {
		s.shotWait, s.shotRows = att.ID, rows
		s.mu.Unlock()
		return
	}
	if !movedOn(s.shotRows, rows) {
		s.mu.Unlock()
		return
	}
	s.withdrawShownLocked(SourceScreen)
	s.mu.Unlock()
	s.recordStatusReconciled()
	s.notifyChange()
}

// movedOn reports whether the screen's rows now differ from base, the
// baseline, past the snapshot's threshold: at least shotRowsChanged rows
// with more than shotCellsChanged cells each not the same (a cell missing
// in one is a space).
func movedOn(base, now []string) bool {
	changed := 0
	for i := range max(len(base), len(now)) {
		var a, b []rune
		if i < len(base) {
			a = []rune(base[i])
		}
		if i < len(now) {
			b = []rune(now[i])
		}
		diff := 0
		for j := range max(len(a), len(b)) {
			x, y := ' ', ' '
			if j < len(a) {
				x = a[j]
			}
			if j < len(b) {
				y = b[j]
			}
			if x != y {
				diff++
			}
		}
		if diff > shotCellsChanged {
			changed++
		}
	}
	return changed >= shotRowsChanged
}

// asideOwn is the id of the agent's own question while it is set aside,
// "" while none is: what a write notes as it begins (input,
// submitState.aside). The caller holds s.mu.
func (s *Local) asideOwn() string {
	if s.own.ID != "" && s.ownAside {
		return s.own.ID
	}
	return ""
}

// ErrTextTooLong refuses a submission whose text is longer than MaxSubmitText
// bytes once cleaned: nothing was written.
var ErrTextTooLong = fmt.Errorf("session: text to submit is longer than %d bytes", MaxSubmitText)

// stampTyping marks sub as typing now and refreshes the roster at most every
// 2 s (presence).
func (s *Local) stampTyping(sub *Subscription) {
	now := time.Now().UnixMilli()
	if prev := sub.lastInput.Swap(now); now-prev > 2000 {
		s.mu.Lock()
		s.hub.Broadcast(s.viewersFrame())
		s.mu.Unlock()
	}
}

// The words a reply to a question is refused with (ErrQuestionAnswered,
// questionAnsweredError): nothing of it was written.
const (
	// QuestionAnsweredWords: the question it was shown for is no longer the
	// one shown: gone (the agent moved on), or set aside or held over for
	// now.
	QuestionAnsweredWords = "this question was already answered: nothing was sent"
	// QuestionClaimedWords: another reply is being written to it (CLAIMED).
	QuestionClaimedWords = "this question is being answered: nothing was sent"
	// QuestionConsumedWords: keys typed in the terminal reached it (consumed).
	QuestionConsumedWords = "this question was answered in the terminal: nothing was sent"
)

// ErrQuestionAnswered refuses a reply to a question that is no longer the
// one waiting (AnswerInput, Submission.Question); a refusal that says more
// (who answered, or that it is being answered) is a questionAnsweredError,
// which is one (errors.Is).
var ErrQuestionAnswered = errors.New(QuestionAnsweredWords)

// questionAnsweredError is ErrQuestionAnswered with words of its own.
type questionAnsweredError struct{ words string }

func (e *questionAnsweredError) Error() string        { return e.words }
func (e *questionAnsweredError) Is(target error) bool { return target == ErrQuestionAnswered }

// questionWaits reports whether the question named id takes a reply: it is
// the session's needs_input shown, nobody is answering it and nobody
// answered it in the terminal (OPEN). ErrQuestionAnswered otherwise, with
// the result when one is recorded for it.
func (s *Local) questionWaits(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitsLocked(id)
}

// waitsLocked is questionWaits for a caller that holds s.mu.
func (s *Local) waitsLocked(id string) error {
	if id == "" {
		return ErrQuestionAnswered
	}
	att := s.info.Attention
	// The session's turn held for a typed text (source submit) is no
	// question: a person at the terminal deals with it, and a reply naming
	// it writes nothing.
	if att.State == AttentionNeedsInput && att.ID == id && att.Source != SourceSubmit {
		switch {
		case s.claimedLocked(id):
			return &questionAnsweredError{QuestionClaimedWords}
		case s.consumed == id:
			return &questionAnsweredError{QuestionConsumedWords}
		}
		return nil
	}
	if r := s.results[id]; r != nil {
		name := r.ByName
		if name == "" {
			name = "Conductor"
		}
		return &questionAnsweredError{"this question was already answered by " + name + ": nothing was sent"}
	}
	return ErrQuestionAnswered
}

// stillOnScreen reports, for a reply to the question named id, whether the
// screen still shows that question and no other the reply's keys would
// reach: ErrQuestionAnswered when it does not. A question the session reads
// off the screen (a dialog asked during the work, a startup question) is
// still on it, and no other is drawn in its place, not asked yet (a
// watcher asks once the screen is quiet). Any other question (an agent's
// report, a bell's) has no dialog drawn over it, nor a startup question:
// the program waits on that one now, and a key for the report (its `1`)
// would answer it.
func (s *Local) stillOnScreen(id string) error {
	s.mu.Lock()
	att := s.info.Attention
	s.mu.Unlock()
	if att.ID != id {
		return nil
	}
	if s.asked != nil {
		text, cols := s.asked.TextAt()
		_, words, _, whole := s.askedIn(text, cols)
		// The dialog on the screen is the question's when its words are,
		// and its whole list for one read off the screen: a list whose
		// choices changed is another dialog, labels alike but past what a
		// label shows, or the words typed into a choice's field (Claude
		// Code's "Tell Claude what to change", where a digit would be
		// typed into them).
		if att.Source == SourceQuestion && (!sameWords(words, att.words()) || whole != att.list) || att.Source != SourceQuestion && words != "" {
			return ErrQuestionAnswered
		}
	}
	switch {
	case att.Source == SourceTrust && s.trust != nil:
		if _, words := s.startupShown(s.trust.Text()); !sameWords(words, att.words()) {
			return ErrQuestionAnswered
		}
		// The output the watcher keeps holds what came before a screen
		// erased: a question the program left by a key Conductor does not
		// read as an answer (one its choices leave out) is still in it. The
		// screen as it is now, a whole frame, shows the question, or the
		// reply would reach what took its place.
		if s.asked != nil {
			if text, settled, _, _ := s.asked.Whole(); settled && !sameWords(s.lastStartup(text), att.words()) {
				return ErrQuestionAnswered
			}
		}
	case att.Source != SourceQuestion && s.trust != nil && s.trust.Watching():
		// A startup question drawn: the screen as it is now, a whole frame,
		// or else the output the watcher keeps, shows one, other than the
		// one last answered (still shown, the program slow to go on).
		text := s.trust.Text()
		if s.asked != nil {
			if whole, settled, _, _ := s.asked.Whole(); settled {
				text = whole
			}
		}
		if words := s.lastStartup(text); words != "" {
			s.mu.Lock()
			answered := sameWords(words, s.startupAnsweredWords)
			s.mu.Unlock()
			if !answered {
				return ErrQuestionAnswered
			}
		}
	}
	return nil
}

// lastStartup returns the words of the startup question drawn last in text,
// the output's tail the startup questions' watcher keeps (ScreenTail: what
// came before stays in it), "" when it shows none.
func (s *Local) lastStartup(text string) string {
	_, words := s.lastStartupQ(text)
	return words
}

// lastStartupQ is lastStartup with the question: nil and "" for none.
func (s *Local) lastStartupQ(text string) (*StartupQuestion, string) {
	var last *StartupQuestion
	words, at := "", -1
	for i := range s.questions {
		q := &s.questions[i]
		m := q.Pattern.FindAllStringIndex(text, -1)
		if len(m) == 0 {
			continue
		}
		// The question is told by where its pattern matched last; its
		// words (a heading drawn before what finds it) by the whole text.
		if start := m[len(m)-1][0]; start > at {
			last, words, at = q, q.words(text), start
		}
	}
	return last, words
}

// startupShown is the startup question the output shows and its words: the
// question the output's tail finds (lastStartupQ over text, the startup
// watcher's ScreenTail), its words read off the screen when that shows
// them. A TUI draws a frame over the last one cell by cell and writes only
// the cells that differ, so a heading drawn over the previous frame's words
// reaches the tail in pieces (Codex's update heading over its splash reads
// "Updat available" there, the "e" left by "OpenAI" skipped, #165), where
// the screen reads it whole. The words are what tells the question from
// another (sameWords): fireTrust raises it with them, and what compares the
// tail's words with the question's reads them the same way. Without the
// screen, or when it does not show them, the tail's words.
func (s *Local) startupShown(text string) (*StartupQuestion, string) {
	q, words := s.lastStartupQ(text)
	if q != nil && s.asked != nil {
		if whole, _, _, _ := s.asked.Whole(); whole != "" {
			if w := q.words(whole); w != "" {
				return q, w
			}
		}
	}
	return q, words
}

// noteDialogAnswered notes the dialog of words as answered (dialogAnswered),
// as the screen is now. The caller holds s.mu.
func (s *Local) noteDialogAnswered(words string) {
	s.dialogAnswered = words
	if s.asked != nil {
		s.dialogAnsweredAt = s.asked.Fed()
	}
}

// dialogGone forgets the dialog answered when the look in progress, which
// saw it go (no dialog, or another), took its text after it was noted: a
// look that took its text before saw an older screen. The caller holds
// s.mu; look is the watcher's LookedAt, read before.
func (s *Local) dialogGone(look uint64) {
	if look > s.dialogAnsweredAt {
		s.dialogAnswered = ""
	}
}

// syncEnd ends a synchronized update (DEC mode 2026): a frame the program
// finished drawing.
var syncEnd = []byte("\x1b[?2026l")

// feedAsked feeds output to the screen the questions asked during the work
// are read from (asked), and while a dialog answered is remembered looks
// at it after each synchronized update the output ends (answeredLeft): one
// read may bring a frame without the dialog and the dialog drawn again
// (the same command asked again), and a look after the read would see
// only the second. It returns the question answeredLeft names. The caller
// holds feedMu.
func (s *Local) feedAsked(chunk []byte) string {
	left := ""
	for len(chunk) > 0 {
		piece := chunk
		s.mu.Lock()
		remembered := s.dialogAnswered != ""
		s.mu.Unlock()
		if rest := syncEnd[s.syncCut:]; s.syncCut > 0 && bytes.HasPrefix(chunk, rest) {
			// The end the read before cut off.
			piece = chunk[:len(rest)]
		} else if i := bytes.Index(chunk, syncEnd); remembered && i >= 0 {
			piece = chunk[:i+len(syncEnd)]
		}
		s.asked.Feed(piece)
		s.syncCut = cutEnd(piece, s.syncCut)
		chunk = chunk[len(piece):]
		if id := s.answeredLeft(); left == "" {
			left = id
		}
	}
	return left
}

// cutEnd is how much of syncEnd the stream ends with after piece, the
// stream before it having ended with cut bytes of it: the start of an end
// that the next read finishes, which feedAsked looks for there.
func cutEnd(piece []byte, cut int) int {
	if cut > 0 && cut+len(piece) < len(syncEnd) && bytes.Equal(piece, syncEnd[cut:cut+len(piece)]) {
		return cut + len(piece)
	}
	for n := min(len(piece), len(syncEnd)-1); n > 0; n-- {
		if bytes.HasSuffix(piece, syncEnd[:n]) {
			return n
		}
	}
	return 0
}

// answeredLeft forgets the dialog answered (dialogAnswered) once a whole
// frame drawn after the answer no longer shows it, without waiting for
// the screen to go quiet: a program that animates as it works (Claude
// Code's spinner draws every 100 ms, each frame a synchronized update)
// gives no quiet look before its next dialog, which may have the answered
// one's words (the same command asked again) and is a question of its own.
// A frame under way decides nothing, nor does one drawn before the answer.
// It returns the id of the question still shown for that dialog (a key
// typed in the terminal answered it, consumed), which the caller withdraws
// as missAsked would at a quiet look, after feedMu: the next dialog with
// its words is asked anew rather than taken for it. The caller holds
// feedMu, which keeps the screen as fed while it looks.
func (s *Local) answeredLeft() string {
	s.mu.Lock()
	words, at := s.dialogAnswered, s.dialogAnsweredAt
	s.mu.Unlock()
	if words == "" {
		return ""
	}
	fed := s.asked.Fed()
	text, settled, cols := s.asked.TextSettled()
	if fed <= at || !settled {
		return ""
	}
	if _, shown, _, _ := s.askedIn(text, cols); shown != "" && sameWords(shown, words) {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dialogAnswered != words {
		return ""
	}
	s.dialogGone(fed)
	if att := s.info.Attention; s.dialogAnswered == "" && att.State == AttentionNeedsInput && att.Source == SourceQuestion && att.ID != "" && sameWords(att.words(), words) {
		return att.ID
	}
	return ""
}

// rawKeys is what keys typed in the terminal, or a line naming no question
// (its text, its Enter), do to the session's questions as the writer takes
// them: never an answer. A question read off the screen whose answer they
// pick (an Enter, one of its choices, and for a dialog Ctrl+C, a key its
// rows name or a digit of its numbered lines: dialogView.picks) is
// consumed: it takes no reply, for the program's dialog has the keys, and
// it goes once the screen shows it gone. That is so for whatever dialog
// shows as they are written (keys typed before it showed and written once
// it shows, they having waited behind another write, reach it), and for
// the dialog on the screen not asked yet (onScreen, read by the caller
// before the session's lock: noted by its words, so that it is asked
// consumed). A wait with no words of the agent's (the bell, an OSC
// notification, the screen pattern) is withdrawn by their Enter (enter): a
// person typed a line into the program. The agent's own question set
// aside is withdrawn by a person's keys (person): the person has taken
// part, and it does not show again at the done. A question being answered
// (claimed) is left as it is: the claim resolves it or lets it go. Keys
// that enter the text of a stranded line (typed, its Enter never written,
// the hold of the turn to follow: holdPending), or pick the question's
// choice, before the hold is installed give the program its answer: no
// hold follows, the stranded line's claim (a reply's, strandedClaim) goes,
// and the question is consumed, or withdrawn when it is a wait with no
// words; a question on the screen (a dialog drawn during the work and not
// asked yet, onScreen; a startup question the screen shows or may show,
// startup, see startupMayShow) keeps its hold, the keys having reached
// the dialog and not the text.
func (s *Local) rawKeys(data []byte, maybe [][]byte, onScreen *dialogView, startup, enter, person bool) {
	s.mu.Lock()
	// The agent's own question let go here leaves a withdrawn line (#358) that
	// no state change follows: told once the lock is let go.
	defer s.tellChatLeft()
	defer s.mu.Unlock()
	if onScreen != nil && onScreen.picks(onScreen.answers, data, maybe) {
		s.noteDialogAnswered(onScreen.words)
	}
	if person && s.own.ID != "" && s.ownAside {
		s.dropOwnLocked(ChatLeftTerminal)
	}
	att := s.info.Attention
	if att.State != AttentionNeedsInput || att.ID == "" {
		return
	}
	// A dialog on the screen and not asked yet (onScreen), or a startup
	// question drawn (startup), took the keys, as a question on the screen
	// shown would: not the stranded text.
	entered := s.holdPending && s.held == nil && onScreen == nil && !startup && !heldSource(att.Source) && picksChoice(att.Options, data, maybe)
	if entered {
		s.holdPending = false
		s.releaseStrandedLocked()
	}
	if s.claimedLocked(att.ID) {
		return
	}
	switch att.Source {
	case SourceQuestion:
		if onScreen != nil && !sameWords(onScreen.words, att.words()) {
			// The keys reach the dialog on the screen, not the one shown
			// (answered and asked again, before the watcher has seen the
			// next).
			return
		}
		if onScreen.picks(att.Options, data, maybe) {
			s.consumeLocked(att.ID)
		}
	case SourceBell, SourceOSC, SourcePattern, SourceStatus:
		if enter {
			s.withdrawShownLocked(SourceInput)
			go s.notifyChange()
		}
	default:
		if entered {
			s.consumeLocked(att.ID)
		}
	}
}

// dialogView is a dialog asked during the work as the screen shows it: its
// words, its question's choices (answers), and what its own rows offer:
// the keys they name ("(y)", "(p)", "(esc)", "esc to cancel") and how many
// numbered lines they have (0 when none were read).
type dialogView struct {
	words   string
	answers []Option
	keys    map[string]bool
	lines   int
	// field says the dialog's highlight stands in a field (a choice that
	// takes typed words, StartupQuestion.Typed): a printable key is typed
	// into it, no choice or key of the dialog's.
	field bool
}

// picks reports whether data, whose keys are keys (keyReader), picks an
// answer of the dialog v shows (a nil v: one whose rows were not read)
// whose choices are answers: data is one of its choices, or one of the
// keys does (a batch of keys from a terminal, an arrow then y, is each of
// them): an Enter (the highlighted line), one of its choices' keys, a key
// its rows name (in either case), Escape where they name it, Ctrl+C
// (cancel), or a digit of one of its numbered lines (a digit past them does
// nothing; with none read, every digit counts). A bracketed paste is text,
// no key.
func (v *dialogView) picks(answers []Option, data []byte, keys [][]byte) bool {
	if ownKeys(data, keys) && isAnswer(answers, data) {
		return true
	}
	for _, k := range keys {
		if v.picksKey(answers, k) {
			return true
		}
	}
	return false
}

// picksChoice reports whether data, whose keys are keys, picks one of a
// question's choices (answers) or presses Enter: data is one (when its
// keys are its own, ownKeys), or one of its keys is an Enter or a choice. A
// write that is all paste text (no key), or the rest of a sequence a write
// before began, is none.
func picksChoice(answers []Option, data []byte, keys [][]byte) bool {
	if ownKeys(data, keys) && isAnswer(answers, data) {
		return true
	}
	for _, k := range keys {
		if string(k) == "\r" || isAnswer(answers, k) {
			return true
		}
	}
	return false
}

// takesDigit reports whether a startup question whose answers are answers
// picks one of its numbered items on a digit at once: one of its answers is
// a digit alone (Codex's background-server and update questions). One whose
// digit only moves the highlight (Codex's hooks review: its answers are a
// digit and an Enter) does not.
func takesDigit(answers []Option) bool {
	for _, o := range answers {
		if len(o.Input) == 1 && o.Input[0] >= '1' && o.Input[0] <= '9' {
			return true
		}
	}
	return false
}

// startupItem matches a numbered item of a startup question in the text the
// startup questions' watcher keeps (ScreenTail: its rows run together, each
// run of white space or control one space): "1. ", "› 2. ".
var startupItem = regexp.MustCompile(`(?:^|\s)([1-9])\.\s`)

// startupItems returns how many numbered items the startup question q shows
// in text (the watcher's): items 1, 2, … in order from its heading on (the
// last match of its words or of its pattern, whichever begins first; the
// update question's pattern is in its first item); 0 when none is read.
func startupItems(q *StartupQuestion, text string) int {
	start := -1
	if m := q.Pattern.FindAllStringIndex(text, -1); len(m) > 0 {
		start = m[len(m)-1][0]
	}
	if start < 0 {
		return 0
	}
	if q.Words != nil {
		if m := q.Words.FindAllStringIndex(text, -1); len(m) > 0 && m[len(m)-1][0] < start {
			start = m[len(m)-1][0]
		}
	}
	n := 0
	for _, m := range startupItem.FindAllStringSubmatch(text[start:], -1) {
		if d := int(m[1][0] - '0'); d == n+1 {
			n = d
		}
	}
	return n
}

// picksStartup reports whether data, whose keys are keys, answers a startup
// question whose answers are answers and that shows items numbered items (0
// when they were not read): one of its answers, or an Enter (picksChoice),
// or, when a digit picks at once (takesDigit), the digit of one of its
// items. Codex's background-server question takes 2, to restart, though no
// quick reply offers it. With its items not read (its heading gone from
// the text the watcher keeps), a digit it offers no reply for answers
// nothing: one that does nothing must not clear a question that still
// shows (a submission's Enter would reach it); a reply to it is refused
// then anyway (stillOnScreen).
func picksStartup(answers []Option, items int, data []byte, keys [][]byte) bool {
	if picksChoice(answers, data, keys) {
		return true
	}
	if !takesDigit(answers) {
		return false
	}
	return anyKey(keys, func(k []byte) bool {
		return len(k) == 1 && k[0] >= '1' && k[0] <= '9' && int(k[0]-'0') <= items
	})
}

// startupPicks is picksStartup for the startup question att shows, its
// items read from the text the startup questions' watcher keeps while that
// still shows it.
func (s *Local) startupPicks(att Attention, data []byte, keys [][]byte) bool {
	items := 0
	if s.trust != nil {
		text := s.trust.Text()
		if q, words := s.startupShown(text); q != nil && sameWords(words, att.words()) {
			items = startupItems(q, text)
		}
	}
	return picksStartup(att.Options, items, data, keys)
}

// picksKey is picks for one key.
func (v *dialogView) picksKey(answers []Option, key []byte) bool {
	if v != nil && v.field && len(key) == 1 && key[0] >= 0x20 && key[0] != 0x7f {
		// The highlight is in a field: a printable key is a word typed
		// there, whatever the field's row now reads ("(y)" typed is no
		// key of the dialog's). An Enter, Escape or Ctrl+C still answers.
		return false
	}
	if isAnswer(answers, key) {
		return true
	}
	if v != nil && (v.keys[string(key)] || v.keys[strings.ToLower(string(key))]) {
		return true
	}
	if len(key) != 1 {
		return false
	}
	switch d := key[0]; {
	case d == '\r', d == 0x03:
		return true
	case d >= '1' && d <= '9':
		return v == nil || v.lines == 0 || int(d-'0') <= v.lines
	}
	return false
}

// mayAnswerDialog reports whether keys hold one that may answer a dialog:
// one that is not an escape sequence (a lone Escape is).
func mayAnswerDialog(keys [][]byte) bool {
	for _, k := range keys {
		if len(k) == 1 {
			return true
		}
	}
	return false
}

// isAnswer reports whether data is the input of one of answers.
func isAnswer(answers []Option, data []byte) bool {
	for _, o := range answers {
		if o.Input != "" && o.Input == string(data) {
			return true
		}
	}
	return false
}

// keysWritten returns the keys the program reads from data, a write the
// writer is taking (its before), whatever a write before it left
// unfinished (a paste, an escape sequence, a control string) carried in,
// and whether data is none of a person's typing: no key and no paste's text,
// all of it the rest of a sequence or a control string a write before
// began (a terminal's report cut across its reads), or the start of one the
// next ends. Every write made goes through it once, in order.
//
// maybe are the keys data may hold besides keys, never surely: an Escape
// that ends it, held (keyReader.escHeld: it may be the Escape key and may
// be the start of what the next write goes on with, and nothing tells
// which, the program reading two writes as one however they came), and the
// keys an Alt chord's (ESC and a key) may be read as alone. data is no
// quiet write with them.
func (s *Local) keysWritten(data []byte) (keys [][]byte, quiet bool, maybe [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys = s.keysIn.keys(data)
	maybe = s.keysIn.maybe
	if s.keysIn.escHeld {
		maybe = append(maybe, []byte{0x1b})
	}
	return keys, len(keys) == 0 && !s.keysIn.pasted && len(maybe) == 0, maybe
}

// escWait is how long an Escape a write ended with stays unfinished for a
// reply (inputBusy): a report a terminal's read cut comes on at once, and a
// reply written right after it could be read with it as one key.
const escWait = 50 * time.Millisecond

// ErrInputBusy refuses a reply to a question while what was written to the
// session before it is unfinished (a paste, an escape sequence another
// connection's terminal began): its keys would be read as part of it.
// Nothing was written, and the question was not noted; it can be tried
// again.
var ErrInputBusy = errors.New("another connection's paste or key is being written to the session: try again in a moment")

// inputBusy reports whether a write before the next left something
// unfinished (keyReader). The caller holds s.mu.
func (s *Local) inputBusy() bool {
	if s.keysIn.escHeld && time.Since(s.keysIn.escAt) > escWait {
		// An Escape a write ended with, long enough ago to have been read
		// alone.
		return false
	}
	return s.keysIn.pasting || s.keysIn.inString || s.keysIn.inCSI || len(s.keysIn.carry) > 0
}

// ownKeys reports whether keys, as keysWritten read them from data, are
// data's own bytes and all of them: nothing carried in, nothing left
// unfinished, no paste. Only then is data matched whole (a choice's input
// of several keys).
func ownKeys(data []byte, keys [][]byte) bool {
	return len(keys) > 0 && bytes.Equal(bytes.Join(keys, nil), data)
}

// allReports reports whether every key is a terminal's own report
// (isTerminalReport): one cut across writes, read whole.
func allReports(keys [][]byte) bool {
	for _, k := range keys {
		if !isTerminalReport(k) {
			return false
		}
	}
	return len(keys) > 0
}

// anyKey reports whether one of keys is one ok says.
func anyKey(keys [][]byte, ok func(k []byte) bool) bool {
	for _, k := range keys {
		if ok(k) {
			return true
		}
	}
	return false
}

// maxKeyCarry bounds the end of a write a keyReader keeps for the next: an
// escape sequence cut by a terminal's read.
const maxKeyCarry = 32

// keyReader splits the writes to a program, in order, into the keys it
// reads from them: a terminal's report or a mouse report is one key, read
// by reportLen as typesSomething reads it (one grammar: what is a report
// here is one there), an escape sequence (CSI, ESC [ … final byte; SS3,
// ESC O x) is one key, as is a control string (OSC ESC ], DCS ESC P, SOS ESC X,
// PM ESC ^, APC ESC _, through its BEL or ST: a terminal's colour or
// version report), an Escape not starting one is a key, a bracketed paste
// is text and no key, every other byte is a key. A paste, a control string
// or an escape sequence one write begins and a later one ends (a
// terminal's reads cut them anywhere) is carried over: pasting says a
// paste goes on, inString a control string (its bytes no keys), carry is
// the unfinished end of the last write (at most maxKeyCarry bytes).
//
// An Escape that ends a write outside a paste is kept too (escHeld, the
// carry its ESC): a terminal's read can cut a report right after its ESC,
// the rest coming in the next, and two writes (a page's frames, ESC then
// z) can reach the program as one read (Alt+z). The next write that goes
// on with a sequence makes it that sequence's start; one that does not
// makes it, with that write's first key, one Alt chord (the key alone one
// it may be read as), and an ESC after it the Escape key.
type keyReader struct {
	pasting  bool
	inString bool
	// inCSI says an escape sequence cut longer than maxKeyCarry goes on:
	// its bytes are skipped, no key, through its final byte.
	inCSI bool
	carry []byte
	// pasted says the last write held a paste's text.
	pasted bool
	// escHeld says the last write ended in an Escape kept, at escAt.
	escHeld bool
	escAt   time.Time
	// maybe are the keys the last write's Alt chords may be read as alone.
	maybe [][]byte
}

// stringEnd returns the index just past the BEL or ST that ends a control
// string in data from i, -1 when data ends first (cutAt says where an ESC
// of a cut ST begins then, -1 when none).
func stringEnd(data []byte, i int) (end, cutAt int) {
	for j := i; j < len(data); j++ {
		switch data[j] {
		case 0x07:
			return j + 1, -1
		case 0x1b:
			if j+1 == len(data) {
				return -1, j
			}
			if data[j+1] == '\\' {
				return j + 2, -1
			}
		}
	}
	return -1, -1
}

// holdAskedAgain holds the startup question asked again answered as id
// (heldAnswer), so that it goes once the screen shows no startup question
// (escGone): one an Escape may have answered before it was asked
// (heldAsked) keeps who typed it; one asked again after its answer
// (startupStill: the program drew it again, then may move on without
// another) is held from that answer, by who gave it. The caller holds
// s.mu.
func (s *Local) holdAskedAgain(id, words string, heldAsked, startupStill bool) {
	switch {
	case heldAsked:
		s.heldAnswer.id, s.heldAnswer.words = id, ""
	case s.heldAnswer.id == id:
		// Held already (an Escape put it behind, which noted its words
		// answered): who typed it stays.
	case startupStill:
		held := heldStartup{id: id, fed: s.startupAnsweredFed}
		if s.asked != nil {
			held.cols, held.rows = s.asked.Size()
			_, held.drawnCols, held.drawnRows = s.asked.DrawnAt()
		}
		if last := s.info.LastAnswer; last != nil && sameWords(last.Message, words) {
			held.by, held.name = last.By, last.ByName
		}
		s.heldAnswer = held
	}
}

// heldStartup is a startup question an Escape a write ended with
// may have answered: its id (asked) or its words (not asked yet), who typed
// it, and how many Feeds the screen the questions asked during the work are
// read from had seen as it was written (fed).
type heldStartup struct {
	id, words, by, name string
	fed                 uint64
	// cols and rows are that screen's size then, drawnCols and drawnRows
	// the size what it showed was laid out for (Screen.DrawnAt): on a
	// smaller one, or laid out for a smaller one, the question may not
	// read as itself (cut, or drawn narrower), and what it shows proves
	// nothing gone.
	cols, rows           int
	drawnCols, drawnRows int
}

// holdStartupAnswered notes the startup question showing, or (words, not
// asked yet) the one the screen shows, as one that keys typed by sub
// reached: surely (its choice, an Enter) or maybe (an Escape a write ended
// with). Consumed (a reply to it refused), not cleared, until the screen
// shows it gone (escGone: withdrawn) or the next startup question takes
// its place.
func (s *Local) holdStartupAnswered(words string, sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := heldStartup{by: sub.ID, name: sub.Name}
	if s.asked != nil {
		held.fed = s.asked.Fed()
		held.cols, held.rows = s.asked.Size()
		_, held.drawnCols, held.drawnRows = s.asked.DrawnAt()
	}
	att := s.info.Attention
	if att.State == AttentionNeedsInput && att.Source == SourceTrust && att.ID != "" && (words == "" || sameWords(att.words(), words)) {
		s.consumeLocked(att.ID)
		held.id = att.ID
		if prev := s.heldAnswer; prev.id == att.ID {
			// Held already: by who held it first, the screen at least as
			// large as either asked for (a key typed at a smaller size
			// asks no less), output after this key.
			held.by, held.name = prev.by, prev.name
			held.cols, held.rows = max(held.cols, prev.cols), max(held.rows, prev.rows)
			held.drawnCols, held.drawnRows = max(held.drawnCols, prev.drawnCols), max(held.drawnRows, prev.drawnRows)
		}
		s.heldAnswer = held
		return
	}
	if words != "" {
		held.words = words
		if prev := s.heldAnswer; sameWords(prev.words, held.words) {
			held.by, held.name = prev.by, prev.name
			held.cols, held.rows = max(held.cols, prev.cols), max(held.rows, prev.rows)
			held.drawnCols, held.drawnRows = max(held.drawnCols, prev.drawnCols), max(held.drawnRows, prev.drawnRows)
		}
		s.heldAnswer = held
	}
}

// escGone withdraws the startup question keys reached (heldAnswer) once the
// screen the questions asked during the work are read from shows no
// startup question, as it is now and after output fed since the keys were
// written: the program took them and moved on. The startup output is put
// behind then (startupGone). The look and that are made with no output fed meanwhile
// (feedMu): a look that took its text earlier (a watcher's, its callback
// late) is never what decides, and no startup question drawn since is put
// behind unseen. Asked at each look of that screen, and as a startup
// question is asked; a session without that screen has nothing to tell it
// by, and keeps the question until a key surely answers it or the next
// takes its place.
func (s *Local) escGone() {
	if s.asked == nil {
		return
	}
	s.feedMu.Lock()
	fed := s.asked.Fed()
	s.mu.Lock()
	att, held := s.info.Attention, s.heldAnswer
	s.mu.Unlock()
	if held.id == "" || att.ID != held.id || att.State != AttentionNeedsInput || att.Source != SourceTrust || fed <= held.fed || !s.screenShowsNoStartup(held) {
		s.feedMu.Unlock()
		return
	}
	s.mu.Lock()
	if s.heldAnswer != held || s.info.Attention.ID != held.id {
		s.mu.Unlock()
		s.feedMu.Unlock()
		return
	}
	s.startupGone(att.words())
	s.mu.Unlock()
	s.trust.Reset()
	s.feedMu.Unlock()
	s.withdraw(att.ID)
}

// screenShowsNoStartup reports whether the screen the questions asked
// during the work are read from, whole, shows no startup question, and
// shows everything the program drew as it drew it (no synchronized update
// under way, which a terminal would still show the frame before; nothing
// cut by a window made smaller since the program last erased the screen:
// Screen.DrawnAt), at least the size it was when the question was held
// (held), and laid out for at least the size it was laid out for then:
// smaller, the question may not read as itself. The window grown since,
// which a program that erases its dialog's lines rather than the whole
// screen (Codex) draws nothing for, counts: the question is read as it
// was drawn. The caller holds feedMu.
func (s *Local) screenShowsNoStartup(held heldStartup) bool {
	text, _, cols, rows := s.asked.Whole()
	uncut, drawnCols, drawnRows := s.asked.DrawnAt()
	return uncut && cols >= held.cols && rows >= held.rows && drawnCols >= held.drawnCols && drawnRows >= held.drawnRows && s.lastStartup(text) == ""
}

// heldGone reports whether the startup question with words, about to be
// asked, is the one an Escape a write ended with may have
// answered before it was asked (heldAnswer's words), and the screen the
// questions asked during the work are read from shows no startup question,
// as it is now and after output fed since the Escape was written: the
// program took the Escape and moved on, and it is not asked; the startup
// output is put behind (startupGone). Made as escGone's is, with no output
// fed meanwhile.
func (s *Local) heldGone(words string) bool {
	if s.asked == nil {
		return false
	}
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	fed := s.asked.Fed()
	s.mu.Lock()
	held := s.heldAnswer
	s.mu.Unlock()
	if held.words == "" || !sameWords(held.words, words) || fed <= held.fed || !s.screenShowsNoStartup(held) {
		return false
	}
	s.mu.Lock()
	if s.heldAnswer != held {
		s.mu.Unlock()
		return false
	}
	s.startupGone(held.words)
	s.mu.Unlock()
	s.trust.Reset()
	return true
}

// startupGone notes the startup question with words, held answered by an
// Escape a write ended with, as surely answered: a look of the
// startup questions' watcher that took its text before now asks nothing,
// and the question asked again before another takes its place is asked
// answered (as a key that surely answers one does in input). The caller
// holds s.mu, and resets the watcher's text once it has let go.
func (s *Local) startupGone(words string) {
	s.heldAnswer = heldStartup{}
	s.startupAnswered, s.startupAnsweredAt = true, s.trust.Fed()
	s.startupAnsweredWords = words
	if s.asked != nil {
		s.startupAnsweredFed = s.asked.Fed()
	}
}

// keys returns the keys of data, the next write.
func (r *keyReader) keys(data []byte) [][]byte {
	// An Escape held from the write before is read with data as one write
	// is: the start of the sequence data goes on with, the Escape key with
	// an ESC after it, else an Alt chord with data's first key (the program
	// may read the two writes as one).
	if len(r.carry) > 0 {
		data = append(r.carry, data...)
		r.carry = nil
	}
	r.pasted, r.escHeld, r.maybe = false, false, nil
	var keys [][]byte
	i := 0
	cut := func(from int) {
		if len(data)-from <= maxKeyCarry {
			r.carry = bytes.Clone(data[from:])
		}
	}
	for i < len(data) {
		if r.inCSI {
			for i < len(data) && (data[i] < 0x40 || data[i] > 0x7e) {
				i++
			}
			if i == len(data) {
				return keys
			}
			i++ // its final byte
			r.inCSI = false
			continue
		}
		if r.inString {
			end, cutAt := stringEnd(data, i)
			if end < 0 {
				if cutAt >= 0 {
					cut(cutAt)
				}
				return keys
			}
			i = end
			r.inString = false
			continue
		}
		if r.pasting {
			end := bytes.Index(data[i:], []byte(pasteEnd))
			if end != 0 {
				r.pasted = true
			}
			if end < 0 {
				// The end marker may be cut: keep what could begin it.
				for n := min(len(pasteEnd)-1, len(data)-i); n > 0; n-- {
					if bytes.HasPrefix([]byte(pasteEnd), data[len(data)-n:]) {
						cut(len(data) - n)
						break
					}
				}
				return keys
			}
			i += end + len(pasteEnd)
			r.pasting = false
			continue
		}
		report := 0
		if data[i] == 0x1b && i+1 < len(data) {
			report = reportLen(data[i:])
		}
		switch {
		case data[i] != 0x1b:
			keys = append(keys, data[i:i+1])
			i++
		case i+1 == len(data):
			// An Escape that ends a write: kept until the next write says
			// whether it began a sequence (escHeld).
			r.carry, r.escHeld, r.escAt = []byte{0x1b}, true, time.Now()
			return keys
		case report > 0:
			// A terminal's report or a mouse report, read as
			// typesSomething reads it (one grammar for both): one key,
			// its bytes none of their own.
			keys = append(keys, data[i:i+report])
			i += report
		case legacyBegun(data[i:]):
			// An X10 mouse report (ESC [ M and three bytes of 32 or more:
			// the button and the cell) cut short: carried to the next
			// write. With a byte under 32 among them it is no mouse
			// report: CSI M, and that byte a key.
			cut(i)
			return keys
		case data[i+1] == '[':
			j := i + 2
			for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
				j++
			}
			if j == len(data) {
				if len(data)-i > maxKeyCarry {
					// Too long to carry: skipped through its final byte.
					r.inCSI = true
					return keys
				}
				cut(i)
				return keys
			}
			if seq := data[i : j+1]; string(seq) == pasteStart {
				r.pasting = true
			} else if key, maybe, encoded := encodedKey(seq); encoded {
				// A key the terminal encodes (CSI u): the key it is, a
				// release none.
				if key != nil {
					keys = append(keys, key)
				}
			} else {
				keys = append(keys, seq)
				if maybe != nil {
					r.maybe = append(r.maybe, maybe)
				}
			}
			i = j + 1
		case data[i+1] == 'O':
			if i+2 >= len(data) {
				cut(i)
				return keys
			}
			keys = append(keys, data[i:i+3])
			i += 3
		case data[i+1] == ']' || data[i+1] == 'P' || data[i+1] == 'X' || data[i+1] == '^' || data[i+1] == '_':
			end, cutAt := stringEnd(data, i+2)
			if end < 0 {
				// Its end comes in a later write: its bytes are no keys
				// until then.
				r.inString = true
				if cutAt >= 0 {
					cut(cutAt)
				}
				return keys
			}
			keys = append(keys, data[i:end])
			i = end
		case data[i+1] == 0x1b:
			// An ESC another follows: the Escape key, however the program
			// reads them (Codex's crossterm reads two at once as Esc); the
			// second is read as it goes on (a report's start, an Alt chord).
			keys = append(keys, data[i:i+1])
			i++
		default:
			// ESC and a key at once: one key, the key with Alt (a page's
			// xterm sends Alt+z as ESC z; Codex's crossterm reads one Alt
			// chord, which its questions ignore), never the Escape key.
			// The key alone is one it may be read as (a program that
			// ignores Alt), counted where that is the safer mistake.
			_, size := utf8.DecodeRune(data[i+1:])
			keys = append(keys, data[i:i+1+size])
			r.maybe = append(r.maybe, data[i+1:i+1+size])
			i += 1 + size
		}
	}
	return keys
}

// keypadKeys are the kitty keyboard protocol's codes for the keypad's keys
// that type something (KP_0 to KP_9, the decimal point, the operators,
// Enter, equals, the separator), and the key each types.
var keypadKeys = map[int]byte{
	57399: '0', 57400: '1', 57401: '2', 57402: '3', 57403: '4', 57404: '5', 57405: '6', 57406: '7', 57407: '8', 57408: '9',
	57409: '.', 57410: '/', 57411: '*', 57412: '-', 57413: '+', 57414: '\r', 57415: '=', 57416: ',',
}

// encodedKey reads seq, one escape sequence a terminal sent, as a key the
// terminal encodes once a program asks it to tell keys apart: the kitty
// keyboard protocol's CSI u (CSI code[:alternates] ; mods[:event] ; text u,
// which Codex asks for: Escape is ESC [ 2 7 u, Ctrl+C ESC [ 9 9 ; 5 u).
// xterm's modifyOtherKeys (CSI 27 ; mods ; code ~) is not decoded: Codex's
// crossterm does not read it as the key it encodes. A press of a key read
// the same unencoded is that key: Escape, Enter, Tab, Backspace, a
// keypad key (its digit, its Enter), a character (Shift and a letter its
// capital, or the shifted key the
// terminal names), Ctrl and a letter its control byte (not Ctrl+H, I, J or
// M, whose bytes are other keys'); a release is no key (nil). encoded is
// false for anything else (a repeat, an arrow, Shift+Enter, Ctrl+M, a
// terminal's report such as CSI ? flags u): seq stays the key it is, and
// maybe is the key a program may read it as (Enter for a repeated Enter,
// or for Shift+Enter: a handler may take repeats, or look at the key, not
// its modifiers). Caps Lock and Num Lock change nothing.
func encodedKey(seq []byte) (key, maybe []byte, encoded bool) {
	if len(seq) < 4 || seq[0] != 0x1b || seq[1] != '[' {
		return nil, nil, false
	}
	params, final := string(seq[2:len(seq)-1]), seq[len(seq)-1]
	if strings.Trim(params, "0123456789:;") != "" || params == "" {
		return nil, nil, false
	}
	fields := strings.Split(params, ";")
	field := func(i int) []string {
		if i < len(fields) {
			return strings.Split(fields[i], ":")
		}
		return nil
	}
	number := func(subs []string, j, def int) int {
		if j >= len(subs) || subs[j] == "" {
			return def
		}
		n, err := strconv.Atoi(subs[j])
		if err != nil || n > 0x10ffff {
			return -1
		}
		return n
	}
	var code, shifted, mods, event int
	switch final {
	case 'u':
		codes, mod := field(0), field(1)
		code, shifted = number(codes, 0, -1), number(codes, 1, -1)
		mods, event = number(mod, 0, 1), number(mod, 1, 1)
	default:
		return nil, nil, false
	}
	if code < 0 || mods < 1 || event < 1 {
		return nil, nil, false
	}
	switch event {
	case 1, 2:
	case 3:
		return nil, nil, true
	default:
		return nil, nil, false
	}
	const shift, ctrl, locks = 1, 4, 64 | 128
	printable := func(c int) bool { return c >= ' ' && c <= '~' }
	if k, ok := keypadKeys[code]; ok {
		// A keypad key (the protocol's own codes): the key it types, as
		// Codex's crossterm reads it (its digits, its Enter).
		code = int(k)
	}
	var pressed []byte
	switch m := (mods - 1) &^ locks; {
	case m == 0 && (code == 27 || code == '\r' || code == '\t' || code == 0x7f || printable(code)):
		pressed = []byte{byte(code)}
	case m == shift && printable(shifted):
		pressed = []byte{byte(shifted)}
	case m == shift && code >= 'a' && code <= 'z':
		pressed = []byte{byte(code - 'a' + 'A')}
	case m == ctrl && code >= 'a' && code <= 'z' && !strings.ContainsRune("hijm", rune(code)):
		// Ctrl and a letter, its control byte, but for those whose byte
		// is another key's (Backspace, Tab, a line feed, Enter): the
		// protocol sends those keys as their own codes, and Codex's
		// crossterm reads Ctrl+M as Ctrl and m, never Enter.
		pressed = []byte{byte(code - 'a' + 1)}
	}
	if pressed != nil {
		if event == 2 {
			// A repeat (a key held down): a program may take it as the
			// key or ignore it (Codex's trust question ignores repeats),
			// so it is the key only maybe.
			return nil, pressed, false
		}
		return pressed, nil, true
	}
	if code == 27 || code == '\r' || code == '\t' || code == 0x7f || printable(code) {
		// A key with modifiers read as no other (Shift+Enter, Ctrl+M,
		// Ctrl+3): a program may read it as its key alone (a handler that
		// looks at the key, not the modifiers), which maybe is, counted
		// where that is the safer mistake.
		return nil, []byte{byte(code)}, false
	}
	return nil, nil, false
}

// numberedLine matches a numbered line of a dialog's list ("› 1. Yes,
// proceed (y)", "  2. No, …"): its number.
var numberedLine = regexp.MustCompile(`^\s*(?:[›❯>]\s*)?([1-9])\.\s`)

// namedKey matches a key a dialog's row names at its end: "(y)", "(esc)".
var namedKey = regexp.MustCompile(`\(\s*([A-Za-z]{1,3})\s*\)\s*$`)

// escToCancel matches a dialog's footer that names Escape.
var escToCancel = regexp.MustCompile(`(?i)\besc\b`)

// readDialog is the dialog view of words, a question's with its answers,
// as screen (its text, rows joined by line breaks) shows it: its numbered lines and the
// keys its rows name are read from its own rows, the run of rows without a
// blank one that holds the last numbered line, and the rows below it (the
// footer); numbered lines elsewhere (a list in the output above) are not
// its own.
func readDialog(words string, answers []Option, screen string) *dialogView {
	v := &dialogView{words: words, answers: CleanOptions(answers), keys: map[string]bool{}}
	rows := strings.Split(screen, "\n")
	last := -1
	for i := len(rows) - 1; i >= 0 && last < 0; i-- {
		if numberedLine.MatchString(rows[i]) {
			last = i
		}
	}
	if last < 0 {
		return v
	}
	first := last
	for first > 0 && strings.TrimSpace(rows[first-1]) != "" {
		first--
	}
	for _, row := range rows[first:] {
		if m := numberedLine.FindStringSubmatch(row); m != nil {
			v.lines = max(v.lines, int(m[1][0]-'0'))
		}
		if m := namedKey.FindStringSubmatch(row); m != nil {
			key := strings.ToLower(m[1])
			if key == "esc" {
				key = "\x1b"
			}
			if len(key) == 1 {
				v.keys[key] = true
			}
		}
		if escToCancel.MatchString(row) {
			v.keys["\x1b"] = true
		}
	}
	return v
}

// ErrBadAnswer refuses an answer message out of its bounds
// (proto.Answer.Valid): nothing is written.
var ErrBadAnswer = errors.New("bad answer message")

// AnswerResult is the owner's word on a viewer's answer (proto.Answer)
// that ended with err, to that viewer: an answer_ok naming reqID when err is
// nil (nil when there is no reqID to name: nothing is sent), else an error
// naming it, question_answered for ErrQuestionAnswered. A line accepted
// while its program takes it slowly (ErrSubmitPending) is taken. A reqID
// out of its bounds is named by nothing.
func AnswerResult(reqID string, err error) []byte {
	if !proto.ValidAnswerReqID(reqID) {
		reqID = ""
	}
	if errors.Is(err, ErrSubmitPending) {
		// Accepted, its outcome not known yet: the word comes once it is
		// (Submission.Finished, AnswerInputFinished).
		return nil
	}
	if err == nil {
		if reqID == "" {
			return nil
		}
		return proto.MustControl(proto.AnswerOK{T: proto.CtlAnswerOK, RequestID: reqID})
	}
	code, msg := "input_failed", "input could not be delivered"
	switch {
	case errors.Is(err, ErrQuestionAnswered):
		code, msg = proto.ErrCodeQuestionAnswered, err.Error()
	case errors.Is(err, ErrLineStranded):
		msg = LineStrandedWords
	case errors.Is(err, ErrTrustQuestion):
		code, msg = proto.ErrCodeNotSent, TrustQuestionWords
	case errors.Is(err, ErrQuestion):
		code, msg = proto.ErrCodeNotSent, QuestionWords
	case errors.Is(err, ErrReadOnly):
		code, msg = proto.ErrCodeReadOnly, "this link is view-only"
	case errors.Is(err, ErrSessionEnded):
		code, msg = proto.ErrCodeSessionEnded, "the session has ended"
	case errors.Is(err, ErrTooManyRequests):
		code, msg = proto.ErrCodeTooManyRequests, "too many requests at once"
	case errors.Is(err, ErrTextTooLong), errors.Is(err, ErrBadAnswer), errors.Is(err, ErrBadSubmit):
		code, msg = proto.ErrCodeBadFrame, err.Error()
	case errors.Is(err, ErrInputStalled), errors.Is(err, context.DeadlineExceeded):
		msg = ErrInputStalled.Error()
	case errors.Is(err, ErrInputBusy):
		msg = ErrInputBusy.Error()
	}
	return proto.MustControl(proto.ErrorMsg{T: proto.CtlError, Code: code, Message: msg, RequestID: reqID})
}

// Resize applies the latest-controller-wins policy and broadcasts the result.
func (s *Local) Resize(sub *Subscription, cols, rows uint16) error {
	return s.ResizeWith(sub, cols, rows, false)
}

// ResizeWith sizes the PTY for sub's window. Only the sizer's window sizes
// it (see AttachWith): a passive resize (the window changed, a tab came
// back) from another viewer is kept as what its window would want and
// refused with ErrNotSizer, except from one of the owner's own windows
// while nobody holds the size. With take (Fit to my window) any controller
// becomes the sizer. Every viewer hears who sizes it in the resize frame's
// By, a change of sizer at the same size included.
func (s *Local) ResizeWith(sub *Subscription, cols, rows uint16, take bool) error {
	if sub.Role != RoleControl {
		return ErrReadOnly
	}
	if !proto.ValidDimension(cols) || !proto.ValidDimension(rows) {
		return ErrBadDimension
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.info.Status.Ended() {
		return ErrSessionEnded
	}
	sub.want = [2]uint16{cols, rows}
	if !take && s.sizer != sub.ID && (s.sizer != "" || !sub.ownsSize()) {
		return ErrNotSizer
	}
	if take {
		// Asked for, at the same size by the sizer itself too: a viewer
		// moving meanwhile does not undo it (leftSize).
		s.takes++
	}
	handed := s.sizer != sub.ID
	s.sizer = sub.ID
	if cols == s.info.Cols && rows == s.info.Rows && !handed {
		return nil
	}
	if cols != s.info.Cols || rows != s.info.Rows {
		if err := s.proc.Resize(cols, rows); err != nil {
			return err
		}
		s.sized(cols, rows)
	}
	s.hub.Broadcast(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: cols, Rows: rows, By: sub.ID}))
	return nil
}

// handOverSize finds the size a new holder as the sizer leaves: the
// owner's own window that attached last and said its size, at that size,
// else nobody (the size stays, and every viewer hears By ""). Callers hold
// s.mu; the leaving viewer is out of the hub already.
func (s *Local) handOverSize() {
	s.sizer = ""
	var next *Subscription
	s.hub.Each(func(c *Subscription) {
		if c.ownsSize() && proto.HelloSize(c.want[0], c.want[1]) && (next == nil || c.Since.After(next.Since)) {
			next = c
		}
	})
	if next != nil && !s.info.Status.Ended() {
		s.sizer = next.ID
		if cols, rows := next.want[0], next.want[1]; cols != s.info.Cols || rows != s.info.Rows {
			if err := s.proc.Resize(cols, rows); err == nil {
				s.sized(cols, rows)
			}
		}
	}
	s.hub.Broadcast(proto.MustControl(proto.Resize{T: proto.CtlResize, Cols: s.info.Cols, Rows: s.info.Rows, By: s.sizer}))
}

// Stop terminates the process.
func (s *Local) Stop(ctx context.Context) error {
	s.mu.Lock()
	ended := s.info.Status.Ended()
	s.stopRequested = true
	s.mu.Unlock()
	if ended {
		return nil
	}
	err := s.proc.Stop(ctx, s.opts.StopGrace)
	s.markEnded(StatusStopped)
	return err
}

// DisconnectLink evicts every subscription created through linkID as
// revoked, whether or not the session has ended (a run's link keeps its
// viewers on a member that ended, until it is revoked): each is written
// nothing more that was queued for it. It ends them under the lock
// AttachWith registers them under, so a caller that marked the link first
// (a revoke) misses none: one that attaches after this finds the mark
// (AttachOptions.Authorize); and nothing broadcast after reaches them. It
// returns once their transports have closed, which they do together, each
// on its own goroutine, holding no lock: as long as the slowest takes. That
// includes the link's clients that had ended already (a slow consumer, an
// earlier revoke, the session's end) and whose transport still closes: what
// the session's end still writes them stops (Subscription.revoke).
func (s *Local) DisconnectLink(linkID string) { s.closeLink(linkID, true) }

// EndLink evicts every subscription created through linkID, one of the
// session's own links, as the session's end, which ends its links with it
// (the caller marks the link first, as a revoke does): each is still
// written what was queued for it, the session's last output and its
// status, before its close (Subscription.owed). Otherwise it is
// DisconnectLink.
func (s *Local) EndLink(linkID string) { s.closeLink(linkID, false) }

// closeLink is DisconnectLink (revoked) and EndLink. A revoke also stops
// what the session's end still writes the link's clients
// (Subscription.revoke), should it have closed them first.
func (s *Local) closeLink(linkID string, revoked bool) {
	end := func(sub *Subscription) {
		if revoked {
			sub.revoke()
		} else {
			sub.closeWith(ErrSessionEnded)
		}
	}
	var hit []*Subscription
	s.mu.Lock()
	s.hub.Each(func(sub *Subscription) {
		if sub.LinkID == linkID {
			end(sub)
			hit = append(hit, sub)
		}
	})
	for sub := range s.closing {
		if sub.LinkID == linkID {
			end(sub)
			hit = append(hit, sub)
		}
	}
	s.mu.Unlock()
	waitClosed(hit)
}

// Retire is the session leaving the server's registry, which only an ended
// session does: every client is closed as at the session's end, and none
// attaches after (ErrSessionEnded). Whoever closes a link's clients finds
// them through the registry, so none may stay where it no longer looks:
// none is written anything more, whatever it was owed (the session's end
// of its links may still be writing theirs), as a revoke would stop it.
// It does not wait: each client's transport closes on the client's own
// goroutine, and its editors go as it detaches, so one that does not read
// holds up only its own.
func (s *Local) Retire() {
	s.mu.Lock()
	s.retired = true
	s.hub.Each(func(sub *Subscription) { sub.retire() })
	for sub := range s.closing {
		sub.retire()
	}
	s.mu.Unlock()
	if s.folder != nil {
		// A file request under way finishes or fails; none comes after.
		s.folder.Close()
	}
	if s.plans != nil {
		s.plans.Close()
	}
}

// CloseAll evicts every subscription with the given reason (server shutdown)
// and ends their editors (closeNvims), waiting for those (WaitEditors). It
// returns once their transports have closed,
// those of the clients that had ended already included, together, as
// DisconnectLink's do.
// CloseAllBy closes every connection with reason and ends the session's
// editors: they may leave on their own until deadline, shared by every
// session a shutdown closes, then are ended through their contexts
// (endEditorsBy).
func (s *Local) CloseAllBy(reason error, deadline time.Time) {
	var all []*Subscription
	s.mu.Lock()
	s.hub.Each(func(sub *Subscription) {
		sub.closeWith(reason)
		all = append(all, sub)
	})
	for sub := range s.closing {
		all = append(all, sub)
	}
	s.mu.Unlock()
	for _, sub := range all {
		s.closeNvims(sub)
	}
	waitClosed(all)
	// The editors get the shutdown's short wait, then are ended: the
	// server must be gone before the app's own grace runs out.
	s.endEditorsBy(deadline)
}

// CloseAll is CloseAllBy with the shutdown's own wait for the editors
// (EditorShutdownWait from now).
func (s *Local) CloseAll(reason error) {
	s.CloseAllBy(reason, time.Now().Add(EditorShutdownWait))
}

// Send queues an arbitrary frame to one subscription (used for file responses).
func (s *Local) Send(sub *Subscription, frame []byte) { sub.send(frame) }

// Viewers returns the number of attached clients.
func (s *Local) Viewers() int { return s.hub.LoudCount() }

// Drained reports whether every attached client has been handed all the frames
// the session sent it, the final status among them once the session has ended.
func (s *Local) Drained() bool { return s.hub.Drained() }

// LinkViewers counts attached clients per share link id.
func (s *Local) LinkViewers() map[string]int { return s.hub.CountByLink() }

// welcomeCwd is the working directory a welcome carries: the session's own,
// to a viewer whose role may read files (it roots the Files pane, which is
// all it is for), or "" when its JSON string, as the welcome encodes it
// (`&`, `<` and `>` escaped to six bytes each, a control character or a byte
// that is not UTF-8 likewise), passes proto.MaxCwd bytes: the welcome must
// fit one CONTROL frame on every path, the relay's included.
func welcomeCwd(cwd string, files bool) string {
	if !files || len(cwd) > proto.MaxCwd {
		return ""
	}
	if b, err := json.Marshal(cwd); err != nil || len(b) > proto.MaxCwd {
		return ""
	}
	return cwd
}
