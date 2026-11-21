package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Blockers and checkpoints (#334, row 10). A member reports a blocker: what
// stops its attempt (a missing credential, a decision), in words for the
// people watching, who see the task as blocked and the session as needing
// input; it clears once the member works on or says it is done, and when
// the attempt ends. A member saves a checkpoint: what is done, what
// remains and notes, tied to its worktree's commit, kept on the attempt
// and in the ledger; the task's next attempt reads it in its task file,
// and one after an attempt that ended without a result, or a
// continuation's first, starts at the checkpoint's commit.

const maxCheckpointField = 4000

// Blocker and checkpoint errors.
var (
	ErrBadBlocker    = errors.New("a blocker is a reason of one to 500 characters, with a message of at most 4000")
	ErrBadCheckpoint = errors.New("a checkpoint says what is done and what remains, each at most 4000 characters, with notes of at most 4000, 16 KiB in all")
	ErrNoCommit      = errors.New("the worktree has no commit to tie the checkpoint to")
	ErrReviewNoCheck = errors.New("a review saves no checkpoint: it commits nothing, and its attempt always starts at the candidate")
)

// blocker is what an attempt says stops it.
type blocker struct {
	reason, message, by string
	at                  time.Time
	// asked is the id of the question the blocker put to the session
	// (session's Attention.ID) and session the session's id, so that the
	// question is withdrawn when the blocker clears (#353).
	asked, session string
}

// Blocker is a blocker as the run reports it.
type Blocker struct {
	Reason  string    `json:"reason"`
	Message string    `json:"message,omitempty"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

// checkpoint is where an attempt got to.
type checkpoint struct {
	done, remaining, notes string
	commit                 string
	at                     time.Time
	// n is the attempt that saved it: one taken back by a withdrawal
	// (task.withdrawn) is not used again. start and branch are that
	// attempt's: where an attempt made at the checkpoint that could not be
	// made usable sends the next one (steps.go), and where the checkpoint
	// stays for a look.
	n             int
	start, branch string
}

// Checkpoint is a checkpoint as the run reports it and the ledger keeps it.
type Checkpoint struct {
	Done      string    `json:"done"`
	Remaining string    `json:"remaining"`
	Notes     string    `json:"notes,omitempty"`
	Commit    string    `json:"commit"`
	At        time.Time `json:"at"`
}

func (c *checkpoint) api() *Checkpoint {
	return &Checkpoint{Done: c.done, Remaining: c.remaining, Notes: c.notes, Commit: c.commit, At: c.at}
}

// decodeCheckpoint reads a checkpoint as the ledger keeps it; nil for an
// empty or unreadable one.
func decodeCheckpoint(raw string) *checkpoint {
	var c Checkpoint
	if raw == "" || json.Unmarshal([]byte(raw), &c) != nil {
		return nil
	}
	return &checkpoint{done: c.Done, remaining: c.Remaining, notes: c.Notes, commit: c.Commit, at: c.At}
}

// Blocked reports what stops the session's attempt: the task shows it,
// and the session asks for it (the message, else the reason) as the
// agent's own question. The task is returned as it then is.
func (e *Engine) Blocked(ctx context.Context, sessionID, reason, message string) (TaskState, error) {
	reason, message = cleanText(reason), cleanText(message)
	if reason == "" || utf8.RuneCountInString(reason) > 500 || utf8.RuneCountInString(message) > maxCheckpointField {
		return TaskState{}, ErrBadBlocker
	}
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return TaskState{}, err
	}
	var st TaskState
	var mine *blocker
	err = e.transact(ctx, r, func() tx {
		if !a.closedAt.IsZero() || a.state != ledger.AttemptRunning {
			return tx{}
		}
		t := a.t
		mine = &blocker{reason: reason, message: message, by: a.m.def.Name, at: time.Now().UTC(), session: a.sessionID}
		t.blocker = mine
		r.markTask(t)
		r.note(session.ActivityStatus, "task %s: %s is blocked: %s", t.def.ID, a.m.def.Name, reason)
		st = r.taskStateOf(t)
		return ev("task.blocked", map[string]any{"task": t.def.ID, "attempt": a.n, "reason": reason})
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
		return TaskState{}, err
	}
	if st.ID == "" {
		return TaskState{}, fmt.Errorf("%w: the attempt is not running", ErrNotRunning)
	}
	ask := message
	if ask == "" {
		ask = reason
	}
	if l, ok := e.lookup(a.sessionID); ok {
		// The question this ask put up, by its id (read with the ask, so
		// another asked meanwhile is never taken for it), kept on this
		// blocker to withdraw when it clears; a blocker cleared before the
		// id is kept (a result, a working report in between) withdraws it
		// at once (Codex's review of #353).
		if blockedBeforeAsk != nil {
			blockedBeforeAsk()
		}
		a.askMu.Lock()
		defer a.askMu.Unlock()
		e.mu.Lock()
		current := a.t.blocker == mine && a.closedAt.IsZero()
		e.mu.Unlock()
		// A report superseded by a later one, or a blocker cleared already,
		// asks nothing: the question is the current blocker's to ask.
		if current {
			if id, err := l.TryAskID(ask, session.SourceAPI, "", nil); err == nil && id != "" {
				e.mu.Lock()
				still := a.t.blocker == mine && a.closedAt.IsZero()
				if still {
					mine.asked = id
				}
				e.mu.Unlock()
				if !still {
					// Cleared while it asked: its question goes now, by the
					// id at hand, before another blocker of this attempt
					// asks (askMu); the detached blocker is not written.
					l.WithdrawQuestion(id)
				}
			}
		}
	}
	return st, nil
}

// blockedBeforeAsk, when set, runs in Blocked between the blocker's record
// and its question: a test holds a report there while a later one lands.
var blockedBeforeAsk func()

// withdrawBeforeLock, when set, runs as a cleared blocker's withdrawal
// begins, before it takes the attempt's askMu: a test holds it there.
var withdrawBeforeLock func()

// withdrawAfter, when set, runs as a cleared blocker's withdrawal ends.
var withdrawAfter func()

// withdrawBlocker takes away the question blocker b put to a's session, if
// that question still shows: a cleared blocker, its task working on or
// ended, leaves no "needs you" behind (#353). It runs apart from e.mu, on
// a's askMu, after any blocker's ask under way, and withdraws nothing a
// blocker of the attempt has since taken for its own (the same question
// asked again keeps its id; Codex's review of #353).
func (e *Engine) withdrawBlocker(a *attempt, b *blocker) {
	if a == nil || b == nil || b.asked == "" || b.session == "" {
		return
	}
	id, sid := b.asked, b.session
	before, after := withdrawBeforeLock, withdrawAfter
	go func() {
		if after != nil {
			defer after()
		}
		if before != nil {
			before()
		}
		a.askMu.Lock()
		defer a.askMu.Unlock()
		e.mu.Lock()
		owned := a.t.blocker != nil && a.t.blocker.asked == id
		e.mu.Unlock()
		if owned {
			return
		}
		if l, ok := e.lookup(sid); ok {
			l.WithdrawQuestion(id)
		}
	}()
}

// unblock clears the attempt's task's blocker, as the attempt works on or
// says it is done. The caller does not hold e.mu.
func (e *Engine) unblock(r *run, a *attempt, state session.AttentionState) {
	e.mu.Lock()
	blocked := a.t.blocker != nil && a.closedAt.IsZero()
	e.mu.Unlock()
	if !blocked {
		return
	}
	var cleared *blocker
	err := e.transact(context.Background(), r, func() tx {
		t := a.t
		if t.blocker == nil || !a.closedAt.IsZero() {
			return tx{}
		}
		cleared = t.blocker
		t.blocker = nil
		r.markTask(t)
		r.note(session.ActivityStatus, "task %s: %s is no longer blocked (%s)", t.def.ID, a.m.def.Name, state)
		return ev("task.unblocked", map[string]any{"task": t.def.ID, "attempt": a.n, "state": string(state)})
	}, nil)
	if err != nil {
		e.afterTransact(r, err)
		return
	}
	e.withdrawBlocker(a, cleared)
}

// SaveCheckpoint keeps where the session's attempt got to, tied to its
// worktree's commit: on the attempt, as the task's latest, and in the
// ledger. The attempt as it then is, is returned.
func (e *Engine) SaveCheckpoint(ctx context.Context, sessionID, done, remaining, notes string) (AttemptState, error) {
	done, remaining, notes = cleanText(done), cleanText(remaining), cleanText(notes)
	if done == "" || utf8.RuneCountInString(done) > maxCheckpointField || utf8.RuneCountInString(remaining) > maxCheckpointField || utf8.RuneCountInString(notes) > maxCheckpointField {
		return AttemptState{}, ErrBadCheckpoint
	}
	r, a, err := e.attemptOf(sessionID)
	if err != nil {
		return AttemptState{}, err
	}
	e.mu.Lock()
	worktree, kind := a.worktree, a.t.def.Kind
	e.mu.Unlock()
	if kind == TaskReview {
		return AttemptState{}, ErrReviewNoCheck
	}
	if worktree == "" {
		return AttemptState{}, fmt.Errorf("%w: the attempt has no worktree yet", ErrNotRunning)
	}
	head, err := headCommit(ctx, worktree)
	if err != nil || head == "" {
		return AttemptState{}, ErrNoCommit
	}
	e.mu.Lock()
	start, branch := a.start, a.branch
	e.mu.Unlock()
	c := &checkpoint{done: done, remaining: remaining, notes: notes, commit: head, at: time.Now().UTC(), n: a.n, start: start, branch: branch}
	// As the ledger keeps it: within its bound before anything changes, so
	// a refused row never stays marked for every transaction after.
	if b, err := json.Marshal(c.api()); err != nil || len(b) > ledger.MaxCheckpoint {
		return AttemptState{}, ErrBadCheckpoint
	}
	var out AttemptState
	var prevA, prevT *checkpoint
	err = e.transact(ctx, r, func() tx {
		if !a.closedAt.IsZero() || a.state != ledger.AttemptRunning {
			return tx{}
		}
		prevA, prevT = a.checkpoint, a.t.checkpoint
		a.checkpoint, a.t.checkpoint = c, c
		r.markAttempt(a)
		r.note(session.ActivityStatus, "task %s: attempt %d checkpoint at %s: %s", a.t.def.ID, a.n, short(head), oneLine.Replace(shortText(done)))
		out = attemptStateOf(a)
		return ev("task.checkpoint", map[string]any{"task": a.t.def.ID, "attempt": a.n, "commit": head})
	}, func() { a.checkpoint, a.t.checkpoint = prevA, prevT })
	if err != nil {
		e.afterTransact(r, err)
		return AttemptState{}, err
	}
	if out.N == 0 {
		return AttemptState{}, fmt.Errorf("%w: the attempt is not running", ErrNotRunning)
	}
	return out, nil
}

// cleanText takes the control characters out of what an agent sends
// (a message is typed into a terminal; a blocker and a checkpoint are
// shown), keeping line breaks and tabs; the length is bounded by the
// caller, in characters and in the bytes the ledger keeps.
// CleanText is cleanText: what a response's text and rationale are kept
// as.
func CleanText(s string) string { return cleanText(s) }

func cleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// attemptOf is the session's run and attempt; ErrNotAnAttempt otherwise.
func (e *Engine) attemptOf(sessionID string) (*run, *attempt, error) {
	v, ok := e.bySession.Load(sessionID)
	if !ok {
		return nil, nil, ErrNotAnAttempt
	}
	sm := v.(sessionMember)
	if sm.a == nil {
		return nil, nil, ErrNotAnAttempt
	}
	return sm.r, sm.a, nil
}
