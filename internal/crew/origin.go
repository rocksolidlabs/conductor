package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A run a registered provider launched (LaunchOpts.Origin): where it came
// from, what it is about and its time limit, kept in the ledger's
// run_origin row with the run's first row, before any attempt starts. The
// ledger is the authority for the origin, the operation id and the
// deadline; the record's Origin is a cache of the row, as Ticket is of
// the package.

// RunOrigin is a run's origin as its record and GET /api/runs/{run} carry
// it: the provider, its reference and label, the subject's reference, link
// and earlier run, the Dispatch project, the start commit asked for, the
// instant the engine stops the run and its limit in seconds, and whether
// the run is a continuation (a person's run that says where its chain came
// from). The operation id, the digest and the agents' selections are not
// in it: no page needs them.
type RunOrigin struct {
	Provider     string    `json:"provider"`
	Ref          string    `json:"ref"`
	Label        string    `json:"label"`
	Subject      string    `json:"subject,omitempty"`
	SubjectURL   string    `json:"subjectUrl,omitempty"`
	Prior        string    `json:"prior,omitempty"`
	Project      string    `json:"project,omitempty"`
	StartCommit  string    `json:"startCommit,omitempty"`
	Deadline     time.Time `json:"deadline,omitzero"`
	LimitSeconds int       `json:"limitSeconds,omitempty"`
	Continued    bool      `json:"continued,omitempty"`
}

// maxRunOrigin bounds a run's origin as its record encodes it: every
// member at its bound, each byte one the encoder writes as six, is under
// it.
const maxRunOrigin = 20 << 10

// ErrTimeLimitPassed refuses a continuation of a run whose launch's time
// limit has passed, and a change of it that would start an attempt (Retry,
// Accept anyway, Check again, Request changes: pastDeadline): the chain
// takes neither after its deadline, a person's request for changes among
// them.
var ErrTimeLimitPassed = errors.New("the run's time limit has passed: it takes no change that would start an attempt, and its chain no continuation")

// ErrRunNotRunning answers a stop of a run that is not running (StopIfRunning):
// stopped, finished, or let go by the engine. Nothing changed.
var ErrRunNotRunning = errors.New("the run is not running")

// originOfRow is the record's origin of a run_origin row; nil for none.
func originOfRow(row *ledger.OriginRow) *RunOrigin {
	if row == nil {
		return nil
	}
	return &RunOrigin{Provider: row.Provider, Ref: row.Ref, Label: row.Label, Subject: row.Subject, SubjectURL: row.SubjectURL,
		Prior: row.Prior, Project: row.Project, StartCommit: row.StartCommit, Deadline: row.Deadline, LimitSeconds: row.LimitSeconds,
		Continued: row.OpID == ""}
}

// continuedOrigin is the row a continuation of a run with origin from
// writes: the same provider, reference, subject, deadline and the rest, as
// a person's run, with no operation id, so no provider reads or stops it.
func continuedOrigin(from ledger.OriginRow, runID string) ledger.OriginRow {
	from.RunID, from.OpID = runID, ""
	return from
}

// deadlineOf is the instant a run launched at now with a limit of limit
// seconds is stopped: UTC, whole seconds; zero for no limit.
func deadlineOf(now time.Time, limit int) time.Time {
	if limit <= 0 {
		return time.Time{}
	}
	return now.UTC().Truncate(time.Second).Add(time.Duration(limit) * time.Second)
}

// deadlinePeriod is the longest the deadline's watch waits before it
// reads the engine's clock again: a timer counts on a clock that does not
// run while the machine sleeps, so the watch wakes at the lesser of what
// is left and this, and stops the run once the clock says the instant has
// passed.
const deadlinePeriod = time.Minute

// deadlineAfter is the deadline watch's timer: time.After, or a test's
// (Engine.deadlineTimer).
func (e *Engine) deadlineAfter(d time.Duration) <-chan time.Time {
	if e.deadlineTimer != nil {
		return e.deadlineTimer(d)
	}
	return time.After(d)
}

// watchDeadline arms r's deadline, once its first transaction has
// committed: at the instant its origin row keeps, a run that is running
// or waits for input, paused or not, is stopped as Stop stops it, its log
// saying the limit; a run already stopped or finished is left as it is.
// The watch ends with the run's stop and with the engine's life. A run
// with no deadline arms nothing.
func (e *Engine) watchDeadline(r *run) {
	e.mu.Lock()
	var at time.Time
	limit := 0
	if o := r.origin; o != nil {
		at, limit = o.Deadline, o.LimitSeconds
	}
	e.mu.Unlock()
	if at.IsZero() {
		return
	}
	go func() {
		for {
			left := at.Sub(e.now())
			if left <= 0 {
				ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
				err := e.stopIfRunning(ctx, r, fmt.Sprintf("stopped: its launch's time limit of %d minutes", limit/60))
				cancel()
				if f := e.afterDeadline; f != nil {
					f(r.id, err)
				}
				return
			}
			select {
			case <-e.deadlineAfter(min(left, deadlinePeriod)):
			case <-r.ctx.Done():
				return
			case <-e.life.Done():
				return
			}
		}
	}()
}

// StopIfRunning stops the run as Stop does, its log saying note in place
// of "stopped", when it is running or waits for input (paused or not):
// the look and the claim of the stop are one step under the engine's
// lock, so a run that finishes meanwhile is never turned into a stopped
// one. A run that is stopped, being stopped or finished answers
// ErrRunNotRunning and nothing changes; a run the engine does not hold
// ErrRunNotFound. A stop begun runs to its end on ctx.
func (e *Engine) StopIfRunning(ctx context.Context, runID, note string) error {
	e.mu.Lock()
	r, ok := e.runs[runID]
	e.mu.Unlock()
	if !ok || e.unmake(r) {
		return ErrRunNotFound
	}
	return e.stopIfRunning(ctx, r, note)
}

// stopIfRunning is StopIfRunning of r.
func (e *Engine) stopIfRunning(ctx context.Context, r *run, note string) error {
	e.mu.Lock()
	running := e.runs[r.id] == r && !r.stopping && r.stoppedAt == nil
	if running && r.managed {
		running = r.outcome == ""
	} else if running {
		st, _ := runState(r.snapshot(false, false))
		running = st == RunRunning || st == RunNeedsInput
	}
	if !running {
		e.mu.Unlock()
		return ErrRunNotRunning
	}
	// Claimed here: a finish after this is the stop's, and a second stop
	// finds the run stopping.
	r.stopping, r.stopNote = true, note
	e.mu.Unlock()
	return e.stop(ctx, r)
}

// confirmedAgent is one entry of a run_origin row's agents: an agent of
// the crew and the selection of its catalogue entry the launch confirmed.
type confirmedAgent struct {
	Agent     string `json:"agent"`
	Selection string `json:"selection"`
}

// ConfirmedAgents is the agents' selections a launch held to a resolved
// launch confirmed, as its origin row keeps them (OriginRow.Agents): a
// JSON array of {agent, selection}, one per agent, sorted by the agent's
// id.
func ConfirmedAgents(byAgent map[string]string) (string, error) {
	out := make([]confirmedAgent, 0, len(byAgent))
	for _, id := range slices.Sorted(maps.Keys(byAgent)) {
		out = append(out, confirmedAgent{Agent: id, Selection: byAgent[id]})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// confirmedAgents is the selections r's launch confirmed, by agent id, and
// whether its launch was held to a resolved launch (its origin has a
// digest): every attempt of such a run starts its agent only from the
// entry confirmed for it, and an agent with none (a member added later, a
// row whose selections are gone or unreadable) fails at "agent". The
// caller holds e.mu.
func (r *run) confirmedAgents() (map[string]string, bool) {
	if r.origin == nil || r.origin.ResolvedDigest == "" {
		return nil, false
	}
	var list []confirmedAgent
	out := map[string]string{}
	if json.Unmarshal([]byte(r.origin.Agents), &list) == nil {
		for _, a := range list {
			out[a.Agent] = a.Selection
		}
	}
	return out, true
}

// confirmedChangedWords is what an attempt's ErrAgentChanged says in a run
// its launch held to a resolved launch: the launcher's own words when it
// gave any, else the agent.
func confirmedChangedWords(err error, agentID string) error {
	if err == ErrAgentChanged {
		return &wordsError{msg: fmt.Sprintf("the agent %s is not the one this run's launch confirmed: its catalog entry changed since", agentID), err: err}
	}
	return err
}

// startsAttempts are the commands that can start an attempt of a run:
// Retry, Accept anyway, Check again and Request changes, each of which may
// reopen a finished run.
var startsAttempts = map[string]bool{"retry": true, "accept": true, TaskRecheck: true, intentRequestChanges: true}

// pastDeadline refuses intent, when it is one that can start an attempt,
// of a run whose launch's time limit has passed (ErrTimeLimitPassed): past
// its deadline a run takes no change that would start an attempt, as its
// chain takes no continuation. Every other command (pause, cancel, a stop)
// is as before. The caller holds e.mu, in the command's change.
func (e *Engine) pastDeadline(r *run, intent string) error {
	if !startsAttempts[intent] || r.origin == nil || r.origin.Deadline.IsZero() || e.now().Before(r.origin.Deadline) {
		return nil
	}
	return fmt.Errorf("%w (it was %s)", ErrTimeLimitPassed, r.origin.Deadline.Format(time.RFC3339))
}

// SetClockForTest gives the engine's clock to a test: the clock a run's
// deadline is set by, watched against and asked by the commands that
// would start an attempt (pastDeadline). It is called before the engine
// launches any run: the clock is read without a lock.
func (e *Engine) SetClockForTest(now func() time.Time) { e.now = now }
