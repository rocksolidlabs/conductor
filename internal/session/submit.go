package session

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// Submitting a line. What Conductor types itself (a crew member's role
// prompt, a handoff, a broadcast) and what a person sends from a reply box
// reach the program the way a terminal sends a paste followed by a key press:
// the text, wrapped in the bracketed-paste markers while the program has that
// mode on, then, SubmitPause later, a carriage return written on its own. A
// TUI that reads the text and its carriage return in one read takes the
// carriage return as part of a paste (Codex's paste-burst detector turns it
// into a newline; Claude Code collapses a read of over 800 bytes into a
// pasted block); written apart, it is Enter. A person's own keystrokes (Input)
// stay raw.

// Defaults of the submission routine (Options.SubmitPause, Options.ConfirmWait).
const (
	// SubmitPause is the pause between the text and its Enter.
	SubmitPause = 250 * time.Millisecond
	// ConfirmWait is how long Submit waits, for a prompt whose agent reports
	// taking it (Options.ConfirmSubmit and Submission.Confirm), before it
	// presses Enter once more.
	ConfirmWait = 3 * time.Second
)

// The bracketed-paste markers a terminal wraps a paste in.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// MaxSubmitText bounds the text of a submission, in bytes: with the paste
// markers around it, it fits one INPUT frame.
const MaxSubmitText = proto.MaxInput - len(pasteStart) - len(pasteEnd)

// TrustQuestionWords say why a person's typed text is refused while the
// agent's trust question shows: its Enter would pick whatever the dialog
// highlights (Claude Code highlights "No, exit"). The question's own choices
// (Options.TrustAnswers) or the terminal answer it.
const TrustQuestionWords = "the agent asks whether to trust the folder: answer with the question's choices, or in its terminal"

// ErrTrustQuestion is Submit's refusal of a person's text while the trust
// question shows.
var ErrTrustQuestion = errors.New(TrustQuestionWords)

// QuestionWords say why a person's typed text is refused while a question
// the agent asks during its work shows (source "question"): its letters
// would reach the dialog's keys (Codex's command approval takes y, p and
// Esc) and its Enter would pick what the dialog highlights (Yes, proceed).
const QuestionWords = "the agent asks a question on its screen: answer with the question's choices, or in its terminal"

// ErrQuestion is Submit's refusal of a person's text while a question asked
// during the work shows.
var ErrQuestion = errors.New(QuestionWords)

// ErrBadSubmit is a submit past its bounds (proto.Submit.Valid): refused
// with bad_frame, the request named when its id is within its own.
var ErrBadSubmit = errors.New("submit: text past 4096 bytes, or a request id past 32 bytes of printable ASCII")

// HeldWords say why the session needs input while its turn is held for a
// submission's text typed without its Enter, its link no longer opening the
// session (holdTyped, source SourceSubmit): the text waits in the program's
// input, and the next submission waits for a person to type into the
// terminal.
const HeldWords = "Text from a link that was let go was typed and not entered: type in the terminal to continue"

// Submission is a line to submit (Local.Submit).
type Submission struct {
	// Text is the line. Submit makes each line break, carriage return and
	// tab a space and drops the other control characters, so that the text
	// can neither end the paste early nor press a key of its own. Empty, only
	// the Enter is written.
	Text string
	// By is the controller who sends it, from a reply box; nil for what
	// Conductor types itself, recorded as ByName (cleaned as a display name).
	By     *Subscription
	ByName string
	// From is the connection whose request this is when By is not set (a
	// run's chat sent to another member's agent, recorded as ByName): its
	// link's revoke or expiry withdraws the submission as By's does
	// (revoked). Nil for what Conductor types on its own.
	From *Subscription
	// UnlessWaiting writes nothing while the session waits on a prompt: for
	// what must never answer one (handoffs, broadcasts).
	UnlessWaiting bool
	// Confirm presses Enter once more when the session's agent reports
	// taking a prompt (Options.ConfirmSubmit) and has not done so within
	// ConfirmWait: a role prompt typed while the program was still starting
	// can sit in its input box. Never for a reply.
	Confirm bool
	// BeforeEnter, when set, runs just before the Enter is written, outside
	// the session's lock: the run engine stamps a member prompted there, so
	// that a done reported before the Enter never counts for the prompt.
	BeforeEnter func()
	// Finished, when set, is told how a submission accepted while its
	// program took it slowly (ErrSubmitPending) ended, once it has: its
	// result (Entered, or typed without its Enter) and the error that
	// ended it, if any.
	Finished func(SubmitResult, error)
	// Question, when set, makes the line a reply to the question of that id
	// (Attention.ID, as the person was shown it): it is typed only while
	// that question is the one shown, nobody is answering it and nobody
	// answered it in the terminal, looked at as the submission begins and
	// again, together with claiming the question, as the writer takes its
	// text (its Enter, for a line with no text); ErrQuestionAnswered
	// otherwise, nothing written. Its Enter resolves the question; a line
	// typed whose Enter is not written lets the claim go (ErrLineStranded,
	// the turn held). A line naming no question answers nothing.
	Question string
	// Withdrawn, when set, is closed once whoever sent the submission no
	// longer stands (an extension's Host whose try was given up): as a
	// revoked link does for By or From, it withdraws the submission
	// (a kind of ErrRevoked) while it waits for the session's turn and
	// until its text's write begins, looked at as the writer takes the
	// text; once the text is typed, its Enter is left out and the
	// session's turn is held for a person (holdTyped), a reply's claim let
	// go as the hold shows. A closed channel is seen at once: nothing
	// waits for it to be passed on.
	Withdrawn <-chan struct{}
}

// ErrSubmitPending says a submission's text was being taken when its time
// ran out: the program has not read it all yet. The submission is accepted:
// it is finished as the program reads (its Enter, as it would have been),
// holding the session's turn until then, and it is never typed again. Its
// word comes once it is finished (Submission.Finished): never before.
var ErrSubmitPending = errors.New("session: the program is taking its input slowly; the line is entered once it has")

// LineStrandedWords say why a reply's line answered nothing: its text was
// typed and its Enter not written (a question came in its pause, or its
// link went), and the session's turn is held until a person types in the
// terminal (holdTyped).
const LineStrandedWords = "the line was typed and not entered: type in the terminal to continue"

// ErrLineStranded is a reply's word when its line was typed and its Enter
// not written: its claim let go, the question held over by the hold of the
// turn (Submit).
var ErrLineStranded = errors.New(LineStrandedWords)

// AnswerWord is the word a reply's line gets once finished (a transport's
// Submission.Finished): err when it ended with one, nil when it was
// entered, ErrLineStranded when its text was typed and its Enter not.
func AnswerWord(res SubmitResult, err error) error {
	switch {
	case err != nil:
		return err
	case res.Entered:
		return nil
	}
	return ErrLineStranded
}

// submitState is what a submission carries from its text to its Enter.
// aside is the id of the agent's own question set aside as the submission
// began (own): shown again at its turn's done before the Enter, it is no
// prompt that leaves the Enter out (comesAfter). claim is the token of the
// claim a reply's text or Enter made on its question (Submission.Question)
// as the writer took it (claimQuestion), 0 for none: the reply resolves
// the question once its Enter is written (resolve), or lets the claim go
// when its line is stranded (release).
type submitState struct {
	promptSince *time.Time
	byName      string
	typedAt     time.Time
	wait        writeWait
	aside       string
	claim       *atomic.Uint64
}

// claimed reports whether the submission holds a claim on its question.
func (st submitState) claimed() bool { return st.claim != nil && st.claim.Load() != 0 }

// strand is the submission's line typed and its Enter not written for
// good: its claim is taken from it and kept as the stranded line's
// (strandedClaim, with holdPending), to be let go as the hold of the turn
// shows in the question's place (holdTyped, in one step with it: let go
// before, a reply could claim the question and write its keys after the
// text) or as keys typed in the terminal deal with the text before the
// hold (rawKeys, endHoldTyped), so that the question, held over, can be
// answered again once a person's keys show it. res and err are how the
// line ended; it reports whether the line is stranded (the hold follows),
// and ErrLineStranded is the word for one that ended with no error.
func (st submitState) strand(s *Local, sub Submission, res SubmitResult, err error) (bool, error) {
	if !res.Typed || res.Entered {
		return false, err
	}
	stranded := errors.Is(err, ErrRevoked) || err == nil && st.claimed()
	if !stranded {
		return false, err
	}
	var claim questionClaim
	if st.claim != nil {
		if token := st.claim.Swap(0); token != 0 {
			claim = questionClaim{id: sub.Question, token: token}
		}
	}
	s.mu.Lock()
	s.strandedClaim, s.holdPending = claim, true
	s.mu.Unlock()
	if err == nil {
		err = ErrLineStranded
	}
	return true, err
}

// comesAfter reports whether att is a prompt come since the submission
// began, which its Enter must not answer: needs_input, stamped otherwise
// than the attention it began with, and not the agent's own question it
// began with set aside (aside), shown again by its turn's done (a line
// typed while the agent worked, its Enter after the done): that Enter is
// written all the same, so that no text is left typed without it. A
// line naming no question answers nothing, and begins the next turn, whose
// work removes the question; a reply naming it claims and resolves it.
func (st submitState) comesAfter(att Attention) bool {
	return att.State == AttentionNeedsInput && att.Since != st.promptSince && !(st.aside != "" && att.ID == st.aside)
}

// SubmitResult says how far a submission went.
type SubmitResult struct {
	// Typed is set once the text was written, or its write begun: with an
	// error, the text may land when the program reads again (see
	// Local.Submit).
	Typed bool
	// Entered is set once its Enter was written. Typed without Entered: a
	// prompt appeared during the pause, and the text waits in the program's
	// input without its Enter; nothing is ever written again for it.
	Entered bool
	// Reentered is set when a second Enter was written (Confirm).
	Reentered bool
	// landed, when set, is the Enter's write begun and not finished when
	// its time ran out: the program has not taken it yet, and it gives the
	// write's result once it has (landEnter).
	landed <-chan error
}

// Submit types sub.Text and presses Enter, as two writes Options.SubmitPause
// apart, one submission at a time per session. It holds no lock of the
// session across the pause, and a person's keystrokes are not held back
// meanwhile.
//
//   - With UnlessWaiting it writes nothing while the session waits on a
//     prompt, and returns a zero result.
//   - The Enter answers the prompt that was showing when Submit began, as
//     typing does; a prompt raised during the pause (one with a new Since) is
//     never answered: the Enter is left out. In a session with a trust
//     watcher the pause ends only once the watcher has looked at what was
//     drawn since the text (awaitLook), so a trust question drawn during the
//     pause holds the Enter back too; with a watcher for the questions asked
//     during the work, once it has looked at everything drawn so far.
//   - A submission is refused while a question the session holds shows,
//     a person's and Conductor's own alike (with UnlessWaiting it writes
//     nothing and returns a zero result, as for any prompt):
//     ErrTrustQuestion for a trust or startup question, ErrQuestion for
//     one asked during the work. A session with a watcher for the latter
//     looks at its screen first, so that a dialog drawn and not yet seen
//     quiet counts.
//   - It records one input entry: always, with the text, for what Conductor
//     types; for a controller's reply, the question it answered, when it
//     answered one.
//   - ctx ends the wait for the session's turn, the pause and each write;
//     a write also waits at most Options.InputWait for the program to take
//     it (ErrInputStalled), so a program that stops reading holds the
//     session's turn no longer than that, unless its text's write has
//     begun: then the submission is accepted and finished as the program
//     reads (ErrSubmitPending, Typed set), holding the turn till then; so
//     is one whose text is typed and whose Enter had no turn in time. An
//     Enter whose write began counts as entered, with no error: it lands
//     when the program reads, and Confirm presses nothing more for it.
//     BeforeEnter runs as the writer takes the Enter.
//
// Errors: ErrReadOnly for a view-role By, ErrTextTooLong for a text over
// MaxSubmitText bytes once cleaned, ErrSessionEnded, ctx's error,
// ErrInputStalled, ErrSubmitPending (accepted), ErrTooManyRequests, the
// process's write error, or why the session ended By (or From) for its
// link (ErrRevoked, ErrExpired), or its credential no longer stands, or
// its sender was withdrawn (Submission.Withdrawn, a kind of ErrRevoked),
// before its Enter was written. Before the text is typed, nothing of it
// is written. Once it is typed (or its write begun), its Enter is not
// written either, and the text cannot be taken back out of the program:
// the session's turn stays held (holdTyped), so that no other line is
// typed after the text and entered with it, until a person types into the
// terminal, who deals with the text, or the session ends. A submission
// whose sender only went (a reply box closes its connection once it has
// sent; one that failed or was too slow) or whose time ran out is
// finished as above.
func (s *Local) Submit(ctx context.Context, sub Submission) (SubmitResult, error) {
	var res SubmitResult
	if sub.By != nil && sub.By.Role != RoleControl {
		return res, ErrReadOnly
	}
	text := SubmitLine(sub.Text)
	if len(text) > MaxSubmitText {
		return res, ErrTextTooLong
	}
	// A controller's submission goes on when the controller goes (a reply
	// box closes its connection once it has sent, and one that fails or is
	// too slow is no different), but not once the session has ended the
	// controller for its link (revoked or expired): until its Enter is
	// written, it waits with that in view. Once its text is typed, the
	// session's turn is held for a person instead of its Enter (holdTyped).
	from := sub.By
	if from == nil {
		from = sub.From
	}
	var fromEnded <-chan struct{}
	if from != nil {
		fromEnded = from.done
	}
	if err := s.awaitTurn(ctx, from, fromEnded, sub.Withdrawn); err != nil {
		return res, err
	}
	// The turn is given back as Submit returns, unless the submission goes
	// on without its caller (ErrSubmitPending), which gives it back then.
	release := true
	defer func() {
		if release {
			<-s.submitting
		}
	}()
	// Its link, or its sender, withdrawn while it waited for the turn (the
	// turn given back just as it was): nothing of it is written.
	if err := (writeWait{from: from, gone: sub.Withdrawn}).withdrawn(); err != nil {
		return res, err
	}

	// A dialog already drawn and not yet seen quiet takes a paste and drops
	// it (Codex routes a paste to its overlay): look at the screen before the
	// text, so that a handoff waits for the answer instead. A dialog on the
	// screen holds every submission, whatever the session shows.
	onScreen, err := s.lookAsked(ctx)
	if err != nil {
		return res, err
	}
	s.mu.Lock()
	ended := s.info.Status.Ended()
	promptSince := s.info.Attention.Since
	showing := s.info.Attention.State == AttentionNeedsInput
	source := s.info.Attention.Source
	var gone error
	if sub.Question != "" {
		gone = s.waitsLocked(sub.Question)
	}
	// The agent's own question set aside as the submission begins
	// (submitState.comesAfter): a person's line withdraws it as its text
	// is written (rawKeys); Conductor's own leaves it, and its Enter,
	// written after the done showed the question again, answers nothing.
	aside := s.asideOwn()
	s.mu.Unlock()
	if ended {
		return res, ErrSessionEnded
	}
	// A reply to a question answered or gone (one before it in the
	// session's turn answered it) types nothing.
	if gone != nil {
		return res, gone
	}
	if sub.UnlessWaiting && showing {
		return res, nil
	}
	// Nothing is typed into a question the session holds, whoever types:
	// a person's reply, and Conductor's own text too (a crew's prompt, which
	// waits for the answer and is typed then).
	if showing && source == SourceTrust {
		return res, ErrTrustQuestion
	}
	if showing && source == SourceQuestion {
		return res, ErrQuestion
	}
	if onScreen {
		if sub.UnlessWaiting {
			return res, nil
		}
		return res, ErrQuestion
	}
	// A startup question the screen still shows, answered or not (the
	// program slow to move on from it): nothing is typed into it either.
	if s.startupOnScreen() {
		if sub.UnlessWaiting {
			return res, nil
		}
		return res, ErrTrustQuestion
	}
	st := submitState{
		promptSince: promptSince,
		byName:      CleanName(sub.ByName),
		typedAt:     time.Now(),
		// Each write waits Options.InputWait at most, is refused at once
		// while the program is known not to read, and is withdrawn when
		// the session ends its controller (not when the controller only
		// goes): its text, and its Enter once the text is typed.
		wait:  writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait, from: from, gone: sub.Withdrawn},
		aside: aside,
		claim: new(atomic.Uint64),
	}
	if text != "" {
		data := text
		if s.paste.on() {
			data = pasteStart + text + pasteEnd
		}
		// Looked at again as the writer takes the text: a question come
		// meanwhile, or a dialog drawn, holds it as one seen before does;
		// a reply's question is noted answered as it is written.
		textWait := st.wait
		textWait.guard = s.textGuard(sub, st)
		textWait.before = func() {
			keys, _, _ := s.keysWritten([]byte(data))
			if sub.Question == "" {
				// A line naming no question answers nothing: a person's
				// withdraws the agent's own question set aside (rawKeys).
				// A startup question the screen shows as the text is
				// written keeps a pending hold (startupMayShow); the guard
				// holds the text back while one shows (textGuard).
				s.rawKeys([]byte(data), keys, nil, s.startupMayShow(), false, sub.By != nil)
			}
		}
		landed, err := s.writeProc(ctx, []byte(data), textWait)
		if errors.Is(err, errWaiting) {
			return res, nil
		}
		if err != nil {
			if landed == nil || errors.Is(err, ErrSessionEnded) || s.isEnded() {
				return res, err
			}
			// Begun, the text lands when the program reads, and the
			// submission is finished then as it would have been, holding
			// the session's turn: no later submission's text joins it, and
			// it is never typed again. Its caller is answered now: the
			// session's end of its controller for its link, the turn held
			// for a person once the text lands (finishSubmission); else
			// accepted.
			res.Typed = true
			release = false
			go s.finishSubmission(landed, sub, text, st)
			if errors.Is(err, ErrRevoked) {
				return res, err
			}
			return res, ErrSubmitPending
		}
		res.Typed = true
		s.typedEntry(sub, text, st)
	}
	res, err = s.enter(ctx, sub, res, st)
	err = st.withdrawnTyped(res, err)
	if stranded, why := st.strand(s, sub, res, err); stranded {
		// Its text is typed and its Enter not written for good (its link no
		// longer opens the session; a reply's, a question come in its
		// pause): the turn held for a person (holdTyped), who deals with
		// the text, its claim let go as the hold shows.
		release = false
		go func() {
			defer func() { <-s.submitting }()
			s.holdTyped()
		}()
		return res, why
	}
	if err == nil && res.landed != nil {
		// Its Enter's write began and its time ran out: accepted, entered
		// as the program reads, the question its claim holds resolved and
		// its word given then (finishEntered), never before the last byte
		// lands. The turn is given back: the Enter is in the writer's
		// hands ahead of whatever comes next.
		go s.finishEntered(sub, res, st)
		res.landed = nil
		return res, ErrSubmitPending
	}
	if res.Typed && !res.Entered && (errors.Is(err, errEnterLater) || errors.Is(err, context.DeadlineExceeded)) {
		// Its text is typed and its time ran out before its Enter (in its
		// pause, its look, or another's write the program does not take
		// ahead of it): accepted, and the rest of the line finished as
		// the program reads, holding the turn till then, so no other line
		// joins its text.
		release = false
		go s.finishEnter(sub, res, st)
		return res, ErrSubmitPending
	}
	return res, err
}

// maxTurnWaiters bounds the submissions that wait for the session's turn
// together, every caller's: one more is refused (ErrTooManyRequests). A
// turn held for a typed text (holdTyped) waits on a person, and what waits
// meanwhile, a connection's that has gone among it, stays bounded.
const maxTurnWaiters = 64

// awaitTurn takes the session's turn (submitting) for a submission of from
// (nil for Conductor's own), waiting until ctx ends, the session ends, the
// session ends from for its link (revoked) or gone closes (errWithdrawn,
// Submission.Withdrawn): their errors then. ErrTooManyRequests when
// maxTurnWaiters wait already.
func (s *Local) awaitTurn(ctx context.Context, from *Subscription, fromEnded, gone <-chan struct{}) error {
	select {
	case s.submitting <- struct{}{}:
		return nil
	default:
	}
	if s.turnWaiters.Add(1) > maxTurnWaiters {
		s.turnWaiters.Add(-1)
		return ErrTooManyRequests
	}
	defer s.turnWaiters.Add(-1)
	for {
		select {
		case s.submitting <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ended:
			return ErrSessionEnded
		case <-gone:
			return errWithdrawn
		case <-fromEnded:
			if err := revoked(from); err != nil {
				return err
			}
			fromEnded = nil
		}
	}
}

// errEnterLater says a submission's text is typed and its Enter's write had
// no turn in time and was not made (enter): its Enter is to be pressed
// later, the turn held (finishEnter).
var errEnterLater = errors.New("session: the Enter is to be pressed later")

// finishEnter presses the Enter of a submission whose text is typed and
// whose Enter had no turn in time (errEnterLater): as long as the session
// lasts, holding its turn, with no second Enter, its caller having had its
// answer; then it gives the turn back. The checks before an Enter are made
// again, as for any.
func (s *Local) finishEnter(sub Submission, res SubmitResult, st submitState) {
	defer func() { <-s.submitting }()
	s.finishLine(sub, res, st)
}

// finishLine finishes a line whose text is typed, the session's turn held
// by its caller: its pause and looks again, then its Enter, with no time of
// its own and no second Enter, its caller having had its answer; and tells
// how it ended (finished). One whose link no longer opens the session by
// then has no Enter, and holds the turn for a person (holdTyped).
func (s *Local) finishLine(sub Submission, res SubmitResult, st submitState) {
	sub.Confirm = false
	st.wait.wait, st.wait.stall = 0, 0
	res, err := s.enter(context.Background(), sub, res, st)
	if errors.Is(err, errEnterLater) {
		res, err = s.pressEnterHeld(sub, res, st)
	}
	if err == nil && res.landed != nil {
		// Its Enter's write began and its time ran out: its word once the
		// Enter has landed, the turn held till then.
		res, err = s.landEnter(sub, res, st)
	}
	err = st.withdrawnTyped(res, err)
	stranded, err := st.strand(s, sub, res, err)
	s.finished(sub, res, err)
	if stranded {
		s.holdTyped()
	}
}

// withdrawnTyped is err for a submission that ended with res, err, its
// sender's link looked at once more when its text is typed and its Enter
// was left out with no error (a prompt come meanwhile, a dialog drawn on
// the screen): why the link no longer opens the session then, so that the
// turn is held for a person as for any typed text whose link went, rather
// than given to the next line, which would join the text.
func (st submitState) withdrawnTyped(res SubmitResult, err error) error {
	if err == nil && res.Typed && !res.Entered {
		if why := st.wait.withdrawn(); why != nil {
			return why
		}
	}
	return err
}

// holdTyped holds the session's turn, which its caller holds and gives
// back once it returns, after a submission whose text was typed and whose
// Enter was not written, its link no longer opening the session: no other
// submission (a person's reply, a crew's prompt, a handoff) is typed after
// the text, to be entered with it, until a person types into the terminal
// (input), who deals with what is left in the program's input, or the
// session ends. The session shows needs_input meanwhile, with HeldWords
// (source SourceSubmit), which the keys clear; another attention change is
// recorded and not shown, but for a question on the screen (setAttention).
func (s *Local) holdTyped() {
	if hook := s.beforeHold.Load(); hook != nil {
		(*hook)()
	}
	s.mu.Lock()
	if s.info.Status.Ended() || !s.holdPending {
		// Ended; or keys typed in the terminal dealt with the stranded
		// text before the hold was installed (rawKeys): none follows.
		s.holdPending = false
		s.releaseStrandedLocked()
		s.mu.Unlock()
		return
	}
	held := make(chan struct{})
	s.held = held
	s.heldOver = Attention{}
	if s.heldNext != nil {
		close(s.heldNext)
		s.heldNext = nil
	}
	// The stranded line's claim (strandedClaim, the empty claim for a line
	// naming no question) stands until the hold's words take the
	// question's place, and is let go in that step
	// (releaseStrandedLocked), so that no reply claims the question
	// meanwhile and writes its keys after the text. Keys that end the
	// hold before then (endHoldTyped) let it go themselves, the question
	// consumed when they entered the text.
	s.mu.Unlock()
	if hook := s.beforeHoldShown.Load(); hook != nil {
		(*hook)()
	}
	s.log.Info("a submission's text was typed and not entered; the next waits for keys typed in the terminal")
	s.setAttention(AttentionNeedsInput, HeldWords, SourceSubmit, "", nil, whileHeld)
	s.mu.Lock()
	// The hold's words refused (a question on the screen shows over the
	// hold, or keys ended the hold as it began): the claim goes now.
	s.holdPending = false
	s.releaseStrandedLocked()
	s.mu.Unlock()
	select {
	case <-held:
	case <-s.ended:
	}
	s.mu.Lock()
	if s.held == held {
		s.held = nil
	}
	// Keys that ended the hold as it began, before it showed, answered
	// nothing: what it shows goes with it.
	shown := s.info.Attention.State == AttentionNeedsInput && s.info.Attention.Source == SourceSubmit
	if shown {
		s.info.Attention = Attention{State: AttentionNone, Source: SourceInput}
		s.settleLocked(s.info.Attention)
		s.signalAttention()
		s.hub.Broadcast(proto.MustControl(attentionMessage(s.info.Attention)))
	}
	s.mu.Unlock()
	if shown {
		s.notifyChange()
	}
}

// endHoldTyped ends the hold of the session's turn for a typed text, if
// one stands, as a person's keys (data, read as maybe) are written: the
// question the hold took the place of (heldOver) shows again, with its id,
// OPEN (Ctrl+U, the held text killed: a reply to it waiting for the turn
// answers it), or consumed when the keys entered the held text or picked
// one of its choices (the program has its answer; a reply to it is refused
// until the agent moves on). A claim on it (a reply's, made before the hold
// or over it) stands: that reply resolves it. Keys that end the hold before
// its words show (holdPending) deal the same way with the question shown,
// the one the hold was to hold over, and let the stranded line's claim go.
func (s *Local) endHoldTyped(data []byte, maybe [][]byte) {
	s.mu.Lock()
	if s.held == nil {
		s.mu.Unlock()
		return
	}
	close(s.held)
	s.held = nil
	over := s.heldOver
	s.heldOver = Attention{}
	cur := s.info.Attention
	if s.holdPending {
		s.holdPending = false
		s.releaseStrandedLocked()
		if cur.State == AttentionNeedsInput && cur.ID != "" && cur.Source != SourceSubmit && !heldSource(cur.Source) && picksChoice(cur.Options, data, maybe) {
			s.consumeLocked(cur.ID)
		}
	}
	restore := over.ID != "" && cur.State == AttentionNeedsInput && cur.Source == SourceSubmit && !s.info.Status.Ended()
	if over.ID != "" && !restore {
		// Gone with the hold (the session ended, or a question on the
		// screen took the hold's place and the agent's went meanwhile).
		s.forgetLocked(over.ID)
	}
	if restore {
		// Shown again, it is a wait a title that says the agent works may
		// settle from now (settleLocked); the agent's own question while
		// the session keeps it (own).
		over.ask = over.ID == s.own.ID
		s.info.Attention = over
		s.settleLocked(over)
		if picksChoice(over.Options, data, maybe) {
			s.consumeLocked(over.ID)
		}
		s.signalAttention()
		s.hub.Broadcast(proto.MustControl(attentionMessage(s.attentionLocked())))
	}
	s.mu.Unlock()
	if restore {
		s.notifyChange()
	}
}

// Held reports whether the session's turn is held for a submission's text
// typed without its Enter (holdTyped), until a person types into the
// terminal.
func (s *Local) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held != nil
}

// Holding returns a channel closed once the session's turn is held for a
// submission's text typed without its Enter (holdTyped): already closed
// while it is. Whoever waits on behalf of a connection that may have to
// type into the terminal to end the hold stops waiting then.
func (s *Local) Holding() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held != nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if s.heldNext == nil {
		s.heldNext = make(chan struct{})
	}
	return s.heldNext
}

// finished tells how a submission finished in the background ended: logged
// when it was not entered, and to its Finished.
func (s *Local) finished(sub Submission, res SubmitResult, err error) {
	if err != nil || !res.Entered {
		s.log.Info("a submission finished as its program read was typed without its Enter", "err", err)
	}
	if sub.Finished != nil {
		sub.Finished(res, err)
	}
}

// pressEnterHeld presses a submission's Enter, the session's turn held by
// its caller, as long as the session lasts: with no time of its own, and
// again a moment later while the writer's queue is full. It returns how
// that ended (not entered: the session's end, a prompt, its link).
func (s *Local) pressEnterHeld(sub Submission, res SubmitResult, st submitState) (SubmitResult, error) {
	sub.Confirm = false
	st.wait.wait, st.wait.stall = 0, 0
	for {
		got, err := s.pressEnter(context.Background(), sub, res, st)
		if errors.Is(err, errEnterLater) {
			select {
			case <-time.After(100 * time.Millisecond):
				continue
			case <-s.ended:
				return got, ErrSessionEnded
			}
		}
		return got, err
	}
}

// typedEntry records what Conductor typed itself (By unset), once its text
// is written.
func (s *Local) typedEntry(sub Submission, text string, st submitState) {
	if sub.By == nil {
		// A line Conductor typed (a crew's prompt, a broadcast), not an
		// answer to a question: Op says so (OpTyped), and the inspector
		// reads it as sent, not answered.
		s.Record(ActivityEntry{Type: ActivityInput, ByName: st.byName, Message: text, Op: OpTyped})
	}
}

// finishSubmission finishes a submission whose text was being taken when
// its caller was answered (ErrSubmitPending): once the text has landed, the
// pause and its Enter, as enter does, BeforeEnter included, with no second
// Enter, for its caller has had its answer. The Enter waits as long as the
// session lasts, with no time of its own: the submission holds the
// session's turn until then, so no other line joins its text, and gives it
// back. The session's end or a prompt come meanwhile ends it without its
// Enter, which is logged.
func (s *Local) finishSubmission(landed <-chan error, sub Submission, text string, st submitState) {
	defer func() { <-s.submitting }()
	select {
	case err := <-landed:
		if err != nil {
			s.finished(sub, SubmitResult{}, err)
			return
		}
	case <-s.ended:
		s.finished(sub, SubmitResult{}, ErrSessionEnded)
		return
	}
	s.typedEntry(sub, text, st)
	s.finishLine(sub, SubmitResult{Typed: true}, st)
}

// enter is the rest of a submission once its text is written (res.Typed),
// or when it has none: the pause and the look for a trust question after a
// text, then its Enter, unless a prompt came meanwhile (or, with no text,
// its controller's link no longer stands). Its sender withdrawn
// (Submission.Withdrawn) ends the pause: no Enter (errWithdrawn).
func (s *Local) enter(ctx context.Context, sub Submission, res SubmitResult, st submitState) (SubmitResult, error) {
	if res.Typed {
		t := time.NewTimer(s.opts.SubmitPause)
		select {
		case <-t.C:
		case <-st.wait.gone:
			t.Stop()
			return res, errWithdrawn
		case <-ctx.Done():
			t.Stop()
			return res, ctx.Err()
		case <-s.ended:
			t.Stop()
			return res, ErrSessionEnded
		}
	}
	// The screen watchers look before the Enter (awaitLook). A trust
	// question drawn during the pause is found only once the screen has
	// been quiet for patternQuiet: the trust watcher looks at what was drawn
	// since the text. A question asked during the work can be drawn at any
	// time, also just before the text, and an Enter that lands on it picks
	// what it highlights (Codex's "Yes, proceed"): its watcher waits for a
	// quiet screen, then looks at the screen as it is whether quiet or not
	// (a program that never stops drawing runs the wait out), so that a
	// dialog on the screen has raised the question before the Enter, text
	// or none.
	if s.asked != nil {
		if err := s.awaitLook(ctx, time.Time{}); err != nil {
			return res, err
		}
		if onScreen, err := s.lookAsked(ctx); err != nil || onScreen {
			return res, err
		}
	} else if res.Typed && s.trust != nil {
		if err := s.awaitLook(ctx, st.typedAt); err != nil {
			return res, err
		}
	}
	return s.pressEnter(ctx, sub, res, st)
}

// pressEnter writes a submission's Enter, unless the session has ended, its
// controller's link no longer stands (st.wait.from: why, its text typed or
// not) or a prompt came since the submission began; BeforeEnter runs as
// the writer takes it. An Enter whose
// write has begun counts as entered. With its text typed, an Enter whose
// write had no turn in time (its context's deadline, InputWait, a stall)
// and was not made is errEnterLater.
func (s *Local) pressEnter(ctx context.Context, sub Submission, res SubmitResult, st submitState) (SubmitResult, error) {
	if hook := s.beforeEnter.Load(); hook != nil {
		(*hook)()
	}
	wait := st.wait
	s.mu.Lock()
	ended := s.info.Status.Ended()
	att := s.info.Attention
	s.mu.Unlock()
	if ended {
		return res, ErrSessionEnded
	}
	if err := wait.withdrawn(); err != nil {
		// The session ended its controller for its link: nothing more of it
		// is written, and a text it typed holds the turn (holdTyped), a
		// prompt come meanwhile or not.
		return res, err
	}
	if st.comesAfter(att) {
		return res, nil
	}
	// As the writer takes the Enter it looks again: a prompt raised while
	// the Enter waited behind another write, or a dialog drawn on the
	// screen meanwhile, is never answered by it.
	wait.guard = s.enterGuard(st.comesAfter)
	if sub.Question != "" && !res.Typed {
		// A reply that is an Enter alone: its question still waits, and is
		// noted answered as the Enter is taken (its text did so for a line).
		enter := wait.guard
		wait.guard = func() error {
			if err := s.questionWaits(sub.Question); err != nil {
				return err
			}
			if err := enter(); err != nil {
				return err
			}
			if err := s.stillOnScreen(sub.Question); err != nil {
				return err
			}
			token, err := s.claimQuestion(sub.Question)
			if err != nil {
				return err
			}
			st.claim.Store(token)
			return nil
		}
	} else if sub.Question != "" {
		// A reply's line, its text typed: its Enter is left out once the
		// screen shows another question in its question's place (a
		// startup question drawn in the pause, which the question's
		// waiting keeps from being asked).
		enter := wait.guard
		wait.guard = func() error {
			if err := enter(); err != nil {
				return err
			}
			if s.stillOnScreen(sub.Question) != nil {
				return errPromptCame
			}
			return nil
		}
	}
	// As the writer takes the Enter, never for one not written: the
	// caller's BeforeEnter, what an Enter naming no question does to the
	// questions (rawKeys: a bell's wait withdrawn), and the turn a done
	// ended is behind (nextTurn).
	wait.before = func() {
		if sub.BeforeEnter != nil {
			sub.BeforeEnter()
		}
		keys, _, _ := s.keysWritten([]byte{'\r'})
		if sub.Question == "" {
			// A startup question the screen shows as the Enter is written
			// keeps a pending hold (startupMayShow); the Enter's own guard
			// does not hold it back from such a dialog yet (#263).
			s.rawKeys([]byte{'\r'}, keys, nil, s.startupMayShow(), true, sub.By != nil)
		}
		s.nextTurn()
	}
	enterAt := time.Now().UTC()
	landed, err := s.writeProc(ctx, []byte{'\r'}, wait)
	pending := landed != nil
	if errors.Is(err, errPromptCame) {
		// Left out, as one seen before the Enter is.
		return res, nil
	}
	if err != nil && !pending && res.Typed && waitedOut(err) {
		return res, errEnterLater
	}
	if err != nil && (!pending || errors.Is(err, ErrSessionEnded) || s.isEnded()) {
		// Not begun, or the session ended under it: never entered.
		if pending && !errors.Is(err, ErrSessionEnded) {
			err = ErrSessionEnded
		}
		return res, err
	}
	// An Enter whose write has begun lands when the program reads again:
	// the submission is entered, and nothing more is pressed for it.
	res.Entered = true
	if s.trust != nil {
		// A trust question comes before the first prompt, never after.
		s.trust.Stop()
		if s.asked != nil && len(s.askedQuestions) == 0 {
			// The screen kept for the startup questions alone.
			s.asked.Stop()
		}
	}
	if sub.By != nil {
		s.stampTyping(sub.By)
	}
	if pending {
		// Begun, the Enter lands when the program reads again: the
		// question a reply's claim holds is resolved then, and the line's
		// word comes then (landEnter: through Submit's ErrSubmitPending,
		// or the held turn's finishLine), never before the last byte
		// lands.
		s.log.Info("a submission's Enter waits for the program to take input")
		res.landed = landed
		return res, nil
	}
	s.resolveLine(sub, st)
	if sub.Confirm && s.opts.ConfirmSubmit && !s.confirmed(ctx, enterAt) {
		res.Reentered = s.enterAgain(ctx)
	}
	return res, nil
}

// resolveLine resolves the question a reply's line claimed (its text's
// write or its Enter: submitState.claim), by whoever sent it, once its
// Enter has landed; nothing for a line naming no question or whose claim
// was let go.
func (s *Local) resolveLine(sub Submission, st submitState) {
	token := st.claim.Load()
	if token == 0 {
		return
	}
	by, name := "", st.byName
	if sub.By != nil {
		by, name = sub.By.ID, sub.By.Name
	}
	// A line's text is never repeated in the chat (answerWords): the line resolves the question by whom alone.
	s.resolve(sub.Question, token, by, name, nil)
}

// landEnter waits for a submission's Enter whose write had begun when its
// time ran out (SubmitResult.landed) to land, as the program reads again
// or the session ends: entered then, the question its claim holds
// resolved (resolveLine); or never entered (the write failed: the session
// ended), its claim let go.
func (s *Local) landEnter(sub Submission, res SubmitResult, st submitState) (SubmitResult, error) {
	landed := res.landed
	res.landed = nil
	var err error
	select {
	case err = <-landed:
	case <-s.ended:
		select {
		case err = <-landed:
		default:
			err = ErrSessionEnded
		}
	}
	if err != nil {
		res.Entered = false
		if token := st.claim.Swap(0); token != 0 {
			s.release(sub.Question, token)
		}
		return res, err
	}
	s.resolveLine(sub, st)
	return res, nil
}

// finishEntered is landEnter for a submission accepted with its Enter
// pending (Submit's ErrSubmitPending): its word once the Enter has landed.
func (s *Local) finishEntered(sub Submission, res SubmitResult, st submitState) {
	res, err := s.landEnter(sub, res, st)
	s.finished(sub, res, err)
}

// lookAsked looks at the screen for a question asked during the work, quiet
// or not, and reports whether one is on it (false without that watcher).
// Right after a key that answers such a dialog (Hold), the screen is still
// the answered one: it waits until the program has drawn again or the end
// of a quiet period has looked, since the key may have done nothing (a
// digit past the dialog's lines); at most patternQuiet and a margin, after
// which it counts the dialog as on the screen.
func (s *Local) lookAsked(ctx context.Context) (bool, error) {
	if s.asked == nil {
		return false, nil
	}
	deadline := time.Now().Add(2 * patternQuiet)
	for s.asked.Held() {
		if time.Now().After(deadline) {
			return true, nil
		}
		t := time.NewTimer(10 * time.Millisecond)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return false, ctx.Err()
		case <-s.ended:
			t.Stop()
			return false, ErrSessionEnded
		}
	}
	return s.asked.CheckNow(), nil
}

// lookBudget bounds how long Submit waits, before its Enter, for the screen
// watchers to look at what the program drew: the echo of a paste moves the
// output, and a program that keeps drawing is not held up for longer than
// this.
const lookBudget = 2 * time.Second

// errWaiting refuses the text of a submission with UnlessWaiting that a
// prompt or a dialog come meanwhile would take (textGuard): Submit returns
// a zero result then, as for one seen before the text.
var errWaiting = errors.New("session: the session waits on a prompt")

// textGuard is a submission's text's guard (writeWait.guard), asked as the
// writer takes it: what Submit refuses before the text, looked at again,
// for a question or a dialog come while the text waited behind another
// write. With UnlessWaiting, any prompt (errWaiting); else a startup
// question (ErrTrustQuestion), a question asked during the work or a
// dialog on the screen now or Held (ErrQuestion). A reply to a question
// (Question) is refused once that question no longer waits
// (ErrQuestionAnswered), and passing, it claims the question in the same
// critical section (claimQuestion), the claim kept in st.
func (s *Local) textGuard(sub Submission, st submitState) func() error {
	return func() error {
		if sub.Question != "" {
			if err := s.questionWaits(sub.Question); err != nil {
				return err
			}
		}
		dialog := s.asked != nil && (s.asked.Held() || s.asked.CheckNow())
		s.mu.Lock()
		att := s.info.Attention
		s.mu.Unlock()
		showing := att.State == AttentionNeedsInput
		startup := s.startupOnScreen()
		switch {
		case sub.UnlessWaiting && (showing || dialog || startup):
			return errWaiting
		case showing && att.Source == SourceTrust || startup:
			return ErrTrustQuestion
		case dialog || showing && att.Source == SourceQuestion:
			return ErrQuestion
		}
		if sub.Question != "" {
			// The screen still shows the question, and no other drawn in
			// its place (a startup question its own waiting keeps from
			// being asked), which the line would reach.
			if err := s.stillOnScreen(sub.Question); err != nil {
				return err
			}
			token, err := s.claimQuestion(sub.Question)
			if err != nil {
				return err
			}
			st.claim.Store(token)
			return nil
		}
		return nil
	}
}

// errPromptCame refuses a submission's Enter that a prompt raised since the
// submission began would take (pressEnter's guard).
var errPromptCame = errors.New("session: a prompt came before the Enter")

// enterGuard is an Enter's guard (writeWait.guard), asked as the writer
// takes it: errPromptCame when the session's attention is one waits says
// the Enter must not answer, or when a dialog asked during the work is on
// the screen as it is now (looked at whether quiet or not, and fired
// then), or a key that answered one has not been followed by a look yet
// (Held), or a startup question may show (startupMayShow: the screen
// shows one, or is half a frame or cut by a window made smaller, so that
// what it shows is not known, or shows a choice of the one the tail saw
// last; a wait showing keeps the question from being raised,
// unlessWaiting, so the attention alone does not tell of it, #263): the
// Enter would pick what the dialog highlights.
func (s *Local) enterGuard(waits func(Attention) bool) func() error {
	return func() error {
		if s.asked != nil && (s.asked.Held() || s.asked.CheckNow()) {
			return errPromptCame
		}
		if s.startupMayShow() {
			return errPromptCame
		}
		s.mu.Lock()
		att := s.info.Attention
		s.mu.Unlock()
		if waits(att) {
			return errPromptCame
		}
		return nil
	}
}

// waitedOut says a write was not made for want of a turn in time: its
// context's deadline, InputWait, a stall, a full queue; not its sender's
// withdrawal, its context's cancel or the session's end.
func waitedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInputStalled) || errors.Is(err, ErrTooManyRequests)
}

// awaitLook waits until the screen watchers have had their quiet period
// (patternQuiet) after the last output, when that output came after since,
// so that a question drawn meanwhile has raised needs_input before the
// Enter is written; at most lookBudget, or until ctx or the session ends.
func (s *Local) awaitLook(ctx context.Context, since time.Time) error {
	deadline := time.Now().Add(lookBudget)
	for {
		last := s.LastOutputAt()
		if !last.After(since) {
			return nil
		}
		wait := time.Until(last.Add(patternQuiet + 50*time.Millisecond))
		if wait <= 0 {
			return nil
		}
		if left := time.Until(deadline); left < wait {
			if left <= 0 {
				return nil
			}
			wait = left
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-s.ended:
			t.Stop()
			return ErrSessionEnded
		}
	}
}

// confirmed waits up to Options.ConfirmWait for the agent to show that it
// took what was submitted at enterAt: an attention change stamped after it
// that typing did not make (Claude Code's UserPromptSubmit hook reports
// working). It returns true when ctx ends or the session ends meanwhile:
// nothing is pressed then.
func (s *Local) confirmed(ctx context.Context, enterAt time.Time) bool {
	deadline := time.NewTimer(s.opts.ConfirmWait)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		att := s.info.Attention
		changed := s.attnChanged
		s.mu.Unlock()
		if att.Since != nil && att.Since.After(enterAt) && att.Source != SourceInput {
			return true
		}
		select {
		case <-changed:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return true
		case <-s.ended:
			return true
		}
	}
}

// enterAgain writes one more carriage return, unless the session has ended or
// waits on a prompt, which the Enter must not answer. It records nothing.
func (s *Local) enterAgain(ctx context.Context) bool {
	s.mu.Lock()
	skip := s.info.Status.Ended() || s.info.Attention.State == AttentionNeedsInput
	s.mu.Unlock()
	if skip {
		return false
	}
	// Looked at again as the writer takes it: no prompt or dialog come
	// meanwhile is answered by it.
	again := writeWait{wait: s.opts.InputWait, stall: s.opts.InputWait, guard: s.enterGuard(func(att Attention) bool {
		return att.State == AttentionNeedsInput
	}), before: func() { s.keysWritten([]byte{'\r'}) }}
	if _, err := s.writeProc(ctx, []byte{'\r'}, again); err != nil {
		return false
	}
	s.log.Info("pressed Enter again: the agent did not report taking the prompt", "after", s.opts.ConfirmWait)
	return true
}

// SubmitLine is the text Submit writes for text: valid UTF-8, each line
// break, carriage return and tab a space, every other control character (ESC
// among them, so the text cannot end a paste early) dropped, trimmed.
func SubmitLine(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, "")))
}

// pasteMode follows the bracketed-paste mode a program sets in its output
// (ESC[?2004h on, ESC[?2004l off), as a terminal does, carrying the end of a
// chunk over to the next so a sequence split between two reads still counts.
// Only the pump writes it; anyone reads it.
type pasteMode struct {
	state atomic.Int32 // 0 never set, 1 on, 2 off
	tail  []byte       // the pump's alone
}

var (
	pasteOnSeq  = []byte("\x1b[?2004h")
	pasteOffSeq = []byte("\x1b[?2004l")
)

// feed takes a chunk of output. Only the pump calls it.
func (p *pasteMode) feed(chunk []byte) {
	data := append(p.tail, chunk...)
	on, off := bytes.LastIndex(data, pasteOnSeq), bytes.LastIndex(data, pasteOffSeq)
	switch {
	case on > off:
		p.state.Store(1)
	case off > on:
		p.state.Store(2)
	}
	n := min(len(data), len(pasteOnSeq)-1)
	p.tail = append(p.tail[:0], data[len(data)-n:]...)
}

func (p *pasteMode) on() bool { return p.state.Load() == 1 }

// BracketedPaste reports whether the program has turned bracketed paste on
// at least once (seen) and whether it is on now. A program that turned it on
// and then off again is between screens (Claude Code does while it starts):
// the run engine does not type into it then.
func (s *Local) BracketedPaste() (seen, on bool) {
	st := s.paste.state.Load()
	return st != 0, st == 1
}

// titleOnly matches output that only sets the window title (OSC 0 or 2), as
// an agent that animates a spinner in its title does while it waits (Codex
// with terminal_title set): such output does not move LastOutputAt, so the
// program is seen to be quiet. A title sequence cut between two reads is
// output like any other.
var titleOnly = regexp.MustCompile(`^(?:\x1b\][02];[^\x07\x1b]*(?:\x07|\x1b\\))+$`)

// SubmitWord is the owner's word on a submit that named a request id
// (#322, proto.Submit.ReqID): nil for one that named none (nothing is
// said, as before) and for one accepted while the program takes it slowly
// (ErrSubmitPending: the word comes from Submission.Finished, through
// submit_ok once the line was entered; else the error
// naming the request, as AnswerResult names an answer's: not_sent with the
// question's words when the unless-waiting guard held the line back (typed
// nothing, no error), input_failed when the line was typed and not entered
// (the session's turn is held), too_many_requests, session_ended,
// read_only, bad_frame.
func SubmitWord(reqID string, res SubmitResult, err error) []byte {
	if reqID == "" || errors.Is(err, ErrSubmitPending) {
		return nil
	}
	if err == nil && !res.Typed && !res.Entered {
		// The guard held it: the session was asking.
		err = ErrQuestion
	}
	if err == nil {
		if !res.Entered {
			err = ErrLineStranded
		} else {
			return proto.MustControl(proto.SubmitOK{T: proto.CtlSubmitOK, RequestID: reqID})
		}
	}
	return AnswerResult(reqID, err)
}
